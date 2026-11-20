package main

// startcheck.go — the new build's half of the update check (updrescue.go).
//
// While launch.sh's probation plan (.update-probation) is beside the
// executable, this start reports how far it got in .start-check, one line
// "<stage> <pid>" that launch.sh polls:
//
//   - "frame" once the first frame has been presented (main.go, beside the
//     boot guard's first-frame hook);
//   - "ready" once the main loop has kept running startCheckSteady past it and
//     input works: on a handheld a pad SDL can see or any input event, on
//     anything else no condition. The main loop itself answers the question
//     (tbxPost), so a loop stuck after its first frame never says ready. A
//     game on the screen (a resume at boot) is a working start too, and so
//     is the dock's dark screen while the loop's heartbeat goes on (neither
//     drains the queue). The plan is deleted then: the update has proved
//     itself on this device;
//   - "noinput" when a handheld still has no pad and no input event
//     startCheckNoInput after the first frame: launch.sh treats it as a failed
//     start.
//
// Without a plan nothing is written. The question costs one stat on a
// goroutine after the first frame, and one atomic load per input event.
//
// startCheckTestName is the hardware check's way to simulate a bad update
// (docs: the device check): "hang" makes the next start under a plan hang in
// package init the way the 2026-11-19 release did, "crash" makes it panic
// there, "noinput" makes its start check find no input. Only while a plan is
// in force; the restored build deletes the file, and so does any start
// without a plan (updRescueSettle), so a flag left behind after the check
// cannot stop a later real update.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// startCheckSteady is how long the main loop must keep running past the
	// first frame before the start counts as working.
	startCheckSteady = 10 * time.Second
	// startCheckNoInput is when a handheld with no input says so.
	startCheckNoInput = 30 * time.Second
	// startCheckGiveUp ends a check whose main loop never answered: launch.sh
	// has stopped the start long before.
	startCheckGiveUp = 5 * time.Minute
	// startCheckTestName is the simulation file beside the executable.
	startCheckTestName = ".test-update-hang"
	// startCheckFactName (in versions/) is the last build that passed.
	startCheckFactName = "started.json"
)

// startCheck is this start's check. started is the main thread's; the rest is
// shared with the checking goroutine.
var startCheck struct {
	started     bool
	input       atomic.Bool // an input event arrived
	testNoInput atomic.Bool // the simulation asked for "no input"
}

// The simulation (see the header): read in package init, so "hang" stops the
// process where the 2026-11-19 release stopped. Nothing is logged here, and
// nothing but two small files is read, only on Linux and only under a plan.
func init() {
	if runtime.GOOS != "linux" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	if _, err := os.Stat(filepath.Join(dir, updProbationName)); err != nil {
		return
	}
	f, err := os.Open(filepath.Join(dir, startCheckTestName))
	if err != nil {
		return
	}
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	_ = f.Close()
	switch strings.TrimSpace(string(buf[:n])) {
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	case "crash":
		panic("update check simulation: " + startCheckTestName + " asked this start to crash")
	case "noinput":
		startCheck.testNoInput.Store(true)
	}
}

// startCheckFirstFrame starts the check with the first frame. main.go calls
// it once, beside bootGuardFirstFrame; the main thread does no I/O for it.
func startCheckFirstFrame() {
	if runtime.GOOS != "linux" || startCheck.started {
		return
	}
	startCheck.started = true
	go startCheckRun(time.Now())
}

// startCheckInput notes that input arrives. main.go calls it for every polled
// event; after the first input it is one atomic load.
func startCheckInput(event sdl.Event) {
	if startCheck.input.Load() {
		return
	}
	switch event.(type) {
	case *sdl.KeyboardEvent, *sdl.ControllerButtonEvent, *sdl.ControllerAxisEvent, *sdl.ControllerDeviceEvent,
		*sdl.JoyButtonEvent, *sdl.JoyAxisEvent, *sdl.JoyHatEvent, *sdl.JoyDeviceAddedEvent,
		*sdl.MouseButtonEvent, *sdl.TouchFingerEvent:
		startCheck.input.Store(true)
	}
}

// startCheckRun is the check, on its own goroutine from the first frame.
func startCheckRun(framed time.Time) {
	dir, err := updAppDirFn()
	if err != nil || dir == "" {
		return
	}
	plan := filepath.Join(dir, updProbationName)
	if _, err := os.Stat(plan); err != nil {
		return
	}
	pid := os.Getpid()
	startCheckWrite(dir, "frame", pid)
	logInfo("update", "update check: first frame of an update on probation; it counts as started after %s with input", startCheckSteady)
	handheld := DetectDevice().IsTrimui()
	answer := make(chan bool, 1)
	asked := false
	for {
		time.Sleep(time.Second)
		since := time.Since(framed)
		if since < startCheckSteady {
			continue
		}
		if !pathExists(plan) {
			// Withdrawn: a rollback, or another update, took over.
			return
		}
		if since > startCheckGiveUp {
			logWarn("update", "update check: the main loop did not answer for %s; giving up", startCheckGiveUp)
			return
		}
		if !startCheck.testNoInput.Load() {
			if gameScreenSuspended() {
				// A game owns the screen (the main loop is parked behind it and
				// drains no queue): JukaHub got as far as starting it.
				startCheckPassed(dir, pid, "a game is on the screen")
				return
			}
			if ambientIsScreenOff() && startCheckLoopBeating() {
				// The dock's dark screen (ambient.go): the loop goes round
				// there without draining the queue, so it is asked by its
				// heartbeat instead.
				startCheckPassed(dir, pid, "the dock's dark screen is up")
				return
			}
		}
		if !asked {
			asked = true
			tbxPost(func() {
				ok := !startCheck.testNoInput.Load() &&
					(!handheld || startCheck.input.Load() || sdl.NumJoysticks() > 0)
				select {
				case answer <- ok:
				default:
				}
			})
		}
		select {
		case ok := <-answer:
			asked = false
			if ok {
				startCheckPassed(dir, pid, "")
				return
			}
			if since >= startCheckNoInput {
				startCheckWrite(dir, "noinput", pid)
				logWarn("update", "update check: no pad and no input %s after the first frame; launch.sh puts the previous version back",
					since.Round(time.Second))
				return
			}
		default:
			// The main loop has not run the question yet: not ready, and
			// launch.sh's deadline decides.
		}
	}
}

// startCheckLoopBeating reports whether the main loop went round in the last
// two seconds (stallwatch.go's heartbeat, one atomic load).
func startCheckLoopBeating() bool {
	beat := stallBeatNanos.Load()
	return beat != 0 && time.Since(stallWatchEpoch)-time.Duration(beat) < 2*time.Second
}

// startCheckPassed records that this update started fine on this device and
// ends its probation.
func startCheckPassed(dir string, pid int, why string) {
	startCheckWrite(dir, "ready", pid)
	if err := os.Remove(filepath.Join(dir, updProbationName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logWarn("update", "update check: the plan could not be removed (%v); launch.sh removes it", err)
	}
	_ = os.Remove(filepath.Join(dir, updProbationTriesName))
	syncDirectory(dir)
	if base, err := PatchStateDir(); err == nil {
		_ = os.RemoveAll(filepath.Join(base, updProbationDir))
	}
	tag := strings.TrimSpace(readInstalledReleaseInfo().Tag)
	model := string(DetectDevice())
	if versions, err := uhistDir(); err == nil && tag != "" {
		fact := struct {
			Tag   string `json:"tag"`
			Model string `json:"model"`
			Date  string `json:"date,omitempty"`
		}{tag, model, vfDateNow()}
		if err := upchWriteJSON(filepath.Join(versions, startCheckFactName), fact); err != nil {
			logPatch("update check: %v", err)
		}
	}
	if why != "" {
		why = " (" + why + ")"
	}
	logInfo("update", "update check: %s started fine on this %s%s; launch.sh no longer watches it",
		vfVersionLabel(tag), vfModelShort(model), why)
}

// startCheckWrite replaces the start check line, so launch.sh never reads
// half of one.
func startCheckWrite(dir, stage string, pid int) {
	path := filepath.Join(dir, updStartCheckName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(stage+" "+strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		logWarn("update", "update check: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("update", "update check: %v", err)
	}
}
