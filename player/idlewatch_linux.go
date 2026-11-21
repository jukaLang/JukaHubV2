//go:build linux

package main

// idlewatch_linux.go — the device half of idle in a game (idlewatch.go has
// the rest): reading the input devices beside the game, the dim, and the
// hook scripts.
//
// Input. Every device /proc/bus/input/devices lists with keys, axes or
// relative motion (idleInputWanted) is opened read-only and non-blocking,
// and never grabbed: the kernel gives each open file its own copy of the
// events, so the game, the firmware's daemon and JukaHub's other readers
// (the power key, the game assistant's pad watcher) see everything as
// before. Records are decoded with volumebuttons.go's parser. Each axis gets
// a threshold of an eighth of its range from EVIOCGABS (a hat's is 1), so a
// stick's jitter at rest is not input; a device that cannot say its range
// only counts its hats. The list is read again every idleRescanEvery for a
// pad connected during the game; a device that goes away (ENODEV) is closed.
//
// The dim writes the panel's raw level directly, the way the QR card's borrow
// does not need to (it goes through the worker): the level to put back is
// the hardware's own, read the moment before, not JukaHub's cached
// percentage, which the firmware's keys may have moved during the game.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// idleAbsCount is ABS_CNT: the axis codes an EVIOCGABS can ask about.
const idleAbsCount = 0x40

// idleAbsHatLast is ABS_HAT3Y, the last of the hats.
const idleAbsHatLast = 0x17

// idleInputDev is one open input device.
type idleInputDev struct {
	fd   int
	name string
	// thr is each axis's least change that counts as input (0: the axis is
	// not counted); base is the value it was last counted at.
	thr  [idleAbsCount]int64
	base [idleAbsCount]int64
}

// idleInputs is the watcher's set of open devices. Its goroutine only.
type idleInputs struct {
	devs   map[string]*idleInputDev // by handler ("event3")
	scanAt time.Time
	buf    []byte
}

func newIdleInputs() *idleInputs {
	return &idleInputs{devs: map[string]*idleInputDev{}, buf: make([]byte, evdevEventSize*64)}
}

// rescan opens the wanted devices not open yet and closes the ones gone (or
// whose node now belongs to another device).
func (in *idleInputs) rescan(now time.Time) {
	in.scanAt = now
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, d := range evdevParseDevices(string(data)) {
		if !idleInputWanted(d) {
			continue
		}
		if old := in.devs[d.Handler]; old != nil {
			if old.name == d.Name {
				seen[d.Handler] = true
				continue
			}
			_ = syscall.Close(old.fd)
			delete(in.devs, d.Handler)
		}
		dev, err := idleInputOpen(d)
		if err != nil {
			logDebug("idle", "cannot read %s (%s): %v", d.Handler, d.Name, err)
			continue
		}
		seen[d.Handler] = true
		in.devs[d.Handler] = dev
	}
	for h, dev := range in.devs {
		if !seen[h] {
			_ = syscall.Close(dev.fd)
			delete(in.devs, h)
		}
	}
}

// idleInputOpen opens one device and reads its axes' ranges.
func idleInputOpen(d evdevDevice) (*idleInputDev, error) {
	fd, err := syscall.Open(filepath.Join("/dev/input", d.Handler), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dev := &idleInputDev{fd: fd, name: d.Name}
	if evdevHas(d.EV, evdevEvAbs) {
		buf := make([]byte, evdevAbsInfoSize)
		for code := 0; code < idleAbsCount; code++ {
			if !evdevHas(d.Abs, code) {
				continue
			}
			hat := code >= evdevAbsHat0X && code <= idleAbsHatLast
			if mbridgeIoctlBuf(fd, evdevGetAbs(uint16(code)), buf) != nil {
				if hat {
					dev.thr[code] = 1
				}
				continue
			}
			value, r, ok := evdevParseAbsInfo(buf)
			if !ok {
				if hat {
					dev.thr[code] = 1
				}
				continue
			}
			thr := (int64(r.max) - int64(r.min)) / 8
			if thr < 1 {
				thr = 1
			}
			dev.thr[code], dev.base[code] = thr, int64(value)
		}
	}
	return dev, nil
}

// poll reads everything waiting on every device and reports whether any of
// it was input.
func (in *idleInputs) poll() bool {
	active := false
	for h, dev := range in.devs {
		for i := 0; i < 16; i++ {
			n, err := syscall.Read(dev.fd, in.buf)
			if n > 0 && dev.consume(in.buf[:n]) {
				active = true
			}
			if err != nil {
				if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
					break
				}
				// Unplugged (ENODEV) or broken: the next scan opens it
				// again if it is still listed.
				_ = syscall.Close(dev.fd)
				delete(in.devs, h)
				break
			}
			if n < len(in.buf) {
				break
			}
		}
	}
	return active
}

// consume reports whether the records in b hold input: a key or button
// (pressed, released or repeating), relative motion, an axis moved past its
// threshold, or a queue overflow (SYN_DROPPED: a lot happened).
func (dev *idleInputDev) consume(b []byte) bool {
	size := inputRecordSize(len(b))
	active := false
	for i := 0; i+size <= len(b); i += size {
		typ, code, value, ok := parseInputEvent(b[i : i+size])
		if !ok {
			continue
		}
		switch typ {
		case evdevEvKey, evdevEvRel:
			active = true
		case evdevEvAbs:
			if int(code) >= idleAbsCount || dev.thr[code] <= 0 {
				continue
			}
			d := int64(value) - dev.base[code]
			if d < 0 {
				d = -d
			}
			if d >= dev.thr[code] {
				dev.base[code] = int64(value)
				active = true
			}
		case evdevEvSyn:
			if code == evdevSynDropped {
				active = true
			}
		}
	}
	return active
}

// closeAll closes every device.
func (in *idleInputs) closeAll() {
	for h, dev := range in.devs {
		_ = syscall.Close(dev.fd)
		delete(in.devs, h)
	}
}

// idleWatchRun is the session's goroutine: read, decide, act.
func idleWatchRun(s *idleWatchSession) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("idle", "idle watcher stopped: %v", r)
		}
	}()
	defer idleWatchUndim("watcher ended")
	in := newIdleInputs()
	defer in.closeAll()
	in.rescan(time.Now())
	if len(in.devs) == 0 {
		logWarn("idle", "no input device to watch: idle in games is off for this game")
		return
	}
	logInfo("idle", "watching %d input device(s) for %s (dim after %s)", len(in.devs), idleGameName(s.game), s.dim)
	last := time.Now()
	stage := idleStageActive
	blockedWhy := ""
	timer := time.NewTimer(idleWatchSlow)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-timer.C:
		}
		if exitRequested.Load() {
			return
		}
		now := time.Now()
		if now.Sub(in.scanAt) >= idleRescanEvery {
			in.rescan(now)
		}
		input := in.poll()
		if powerSleepActive() {
			// The sleep owns the light and the game until the wake, which
			// starts the idle clock again.
			last = now
			timer.Reset(idleWatchSlow)
			continue
		}
		if s.poke.Swap(false) || input || idleWatchGamePaused() {
			last = now
		}
		want := idleWatchStageFor(now.Sub(last), s.dim, s.after)
		switch {
		case want == idleStageActive && stage != idleStageActive:
			stage, blockedWhy = idleStageActive, ""
			idleWatchUndim("input")
			idleHooksFire(idleStateActive, idleWhereGame, "input")
		case want != idleStageActive && stage == idleStageActive:
			stage = idleStageDimmed
			idleWatchDim()
			idleHooksFire(idleStateIdle, idleWhereGame, "idle "+idleWatchMinutesLabel(int(s.dim/time.Minute))+" in a game")
		case want == idleStageExtended && stage == idleStageDimmed:
			if why := idleWatchActBlocker(); why != "" {
				if why != blockedWhy {
					blockedWhy = why
					logInfo("idle", "time to %s, but %s: the screen stays dim", s.action, why)
				}
				break
			}
			stage = idleStageExtended
			stillIdle := func() bool { return !in.poll() && !s.poke.Load() && !idleWatchGamePaused() }
			if !idleWatchAct(s, stillIdle) {
				// Input while the extended scripts ran: the next look
				// brings the screen back and says "active".
				s.poke.Store(true)
			}
		}
		next := idleWatchSlow
		if stage != idleStageActive {
			next = idleWatchFast
		}
		timer.Reset(next)
	}
}

// ── The dim ──────────────────────────────────────────────────────────────

// idleDim is the dim in force: the backend, the raw level the panel had and
// the one written over it.
var idleDim struct {
	mu      sync.Mutex
	on      bool
	backend string
	raw     int
	low     int
}

// idleSetRaw writes a raw backlight level through b.
func idleSetRaw(b string, raw int) error {
	switch b {
	case brightnessBackendDisp:
		return dispSetBrightnessRaw(raw)
	case brightnessBackendSysfs:
		return sysfsSetBacklightRaw(raw)
	}
	return errors.New("no raw backlight on " + b)
}

// idleWatchDim takes the backlight down to the floor level, remembering the
// raw level it had. Nothing when the light is off, already at or below the
// floor, or not a hardware backlight. Any goroutine.
func idleWatchDim() {
	b := brightnessKnownBackend()
	if b != brightnessBackendDisp && b != brightnessBackendSysfs {
		return
	}
	idleDim.mu.Lock()
	defer idleDim.mu.Unlock()
	if idleDim.on || backlightIsOff.Load() {
		return
	}
	raw, max, err := brightnessHardwareRaw(b)
	if err != nil || raw <= 0 || max <= 0 {
		return
	}
	low := brightnessRawForLevel(b, brightnessFloorFor(b), max)
	if low < 1 {
		low = 1
	}
	if raw <= low {
		return
	}
	// The brightness curve never learns this level as a firmware step
	// (brightnessIsOwnRaw).
	brightnessLastRaw.Store(brightnessRawAtomic(low))
	if err := idleSetRaw(b, low); err != nil {
		logWarn("idle", "could not dim the screen: %v", err)
		return
	}
	idleDim.on, idleDim.backend, idleDim.raw, idleDim.low = true, b, raw, low
	logInfo("idle", "screen dimmed (%d → %d)", raw, low)
}

// idleWatchUndim puts back the level the dim replaced — only while the panel
// still shows the dim level: a change made meanwhile (the firmware's keys)
// wins. While a sleep has the light off it waits for the wake. Any
// goroutine; cheap when nothing is dimmed.
func idleWatchUndim(why string) {
	idleDim.mu.Lock()
	defer idleDim.mu.Unlock()
	if !idleDim.on || backlightIsOff.Load() {
		return
	}
	idleDim.on = false
	if cur, _, err := brightnessHardwareRaw(idleDim.backend); err == nil && (cur < idleDim.low-1 || cur > idleDim.low+1) {
		logInfo("idle", "screen brightness changed during the dim (%d); left as it is", cur)
		return
	}
	if err := idleSetRaw(idleDim.backend, idleDim.raw); err != nil {
		logWarn("idle", "could not bring the screen back (%s): %v", why, err)
		return
	}
	logDebug("idle", "screen brightness back to %d (%s)", idleDim.raw, why)
}

// ── Hook scripts ─────────────────────────────────────────────────────────

// idleHookOutputMax bounds what a script's output is kept of, for the log.
const idleHookOutputMax = 2 << 10

// idleHookOutput keeps the start of a script's output. exec serialises the
// writes when stdout and stderr are the same writer.
type idleHookOutput struct {
	b []byte
}

func (o *idleHookOutput) Write(p []byte) (int, error) {
	if room := idleHookOutputMax - len(o.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		o.b = append(o.b, p[:room]...)
	}
	return len(p), nil
}

// idleHookRunScripts runs one state's scripts in name order, each bounded.
// The hook worker's goroutine.
func idleHookRunScripts(state, where, source string) {
	dir := idleHooksDir(state)
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // no folder: no scripts
	}
	for _, name := range idleHookScriptNames(entries) {
		idleHookRunOne(dir, name, state, where, source)
	}
}

// idleHookRunOne runs one script with /bin/sh in its own process group, so a
// script that overruns idleHookTimeout is stopped with everything it started.
func idleHookRunOne(dir, name, state, where, source string) {
	ctx, cancel := context.WithTimeout(context.Background(), idleHookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(dir, name))
	cmd.Dir = dir
	env := screenChildEnvFor(os.Environ(), childEnvAppDir())
	cmd.Env = append(env,
		"JUKAHUB_IDLE_STATE="+state,
		"JUKAHUB_IDLE_WHERE="+where,
		"JUKAHUB_IDLE_SOURCE="+source,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	out := &idleHookOutput{}
	cmd.Stdout, cmd.Stderr = out, out
	began := time.Now()
	err := cmd.Run()
	took := time.Since(began).Round(10 * time.Millisecond)
	first := strings.TrimSpace(string(out.b))
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		logWarn("idle", "hook %s/%s took longer than %s and was stopped", state, name, idleHookTimeout)
	case err != nil:
		logWarn("idle", "hook %s/%s (%s) failed after %s: %v %s", state, name, where, took, err, first)
	default:
		logInfo("idle", "hook %s/%s (%s) ran in %s %s", state, name, where, took, first)
	}
}
