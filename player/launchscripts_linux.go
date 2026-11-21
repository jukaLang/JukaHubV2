//go:build linux

package main

// launchscripts_linux.go — the device half of launch scripts and the
// screenshot request file (launchscripts.go has the rest): running one script
// in its own process group, and watching /tmp/jukahub while a child runs.

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// launchScriptRunOne runs one script with /bin/sh in its own process group,
// so a script that overruns launchScriptTimeout is stopped with everything it
// started. It returns the exit code (-1 when it did not exit by itself), how
// long it took and the start of its output.
func launchScriptRunOne(path string, env []string) (int, time.Duration, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchScriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	out := &launchScriptOutput{}
	cmd.Stdout, cmd.Stderr = out, out
	began := time.Now()
	err := cmd.Run()
	took := time.Since(began)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return -1, took, out.b, errLaunchScriptTimeout
	}
	if err == nil {
		return 0, took, out.b, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), took, out.b, nil
	}
	return -1, took, out.b, err
}

// ── Screenshot request file ──────────────────────────────────────────────

const (
	// shotRequestDir holds the request and the answer; tmpfs on a TrimUI.
	shotRequestDir = "/tmp/jukahub"
	// shotRequestFile appearing asks for a screenshot of the game's screen.
	shotRequestFile = shotRequestDir + "/screenshot.request"
	// shotRequestLast gets the saved PNG's path, one line.
	shotRequestLast = shotRequestDir + "/screenshot.last"
	// shotRequestPoll is how often the request is looked for while a child
	// runs.
	shotRequestPoll = 500 * time.Millisecond
)

// shotRequestRunning is set while the watcher goroutine runs.
var shotRequestRunning atomic.Bool

// shotRequestChildEnv adds the screenshot files to a child's environment,
// unless it names them already. Pure.
func shotRequestChildEnv(env []string) []string {
	has := func(key string) bool {
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				return true
			}
		}
		return false
	}
	if !has("JUKAHUB_SCREENSHOT_REQUEST") {
		env = append(env, "JUKAHUB_SCREENSHOT_REQUEST="+shotRequestFile)
	}
	if !has("JUKAHUB_SCREENSHOT_LAST") {
		env = append(env, "JUKAHUB_SCREENSHOT_LAST="+shotRequestLast)
	}
	return env
}

// shotRequestWatch starts the watcher when a screen-taking child starts
// (gamesuspend.go noteGameChildStarted). It runs until no child is left, then
// stops; a child that starts as it stops starts it again. Never blocks.
func shotRequestWatch() {
	if !shotRequestRunning.CompareAndSwap(false, true) {
		return
	}
	_ = os.MkdirAll(shotRequestDir, 0o755)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("screenshot", "screenshot request watcher stopped: %v", r)
				shotRequestRunning.Store(false)
			}
		}()
		for {
			shotRequestLoop()
			shotRequestRunning.Store(false)
			if runningGameChildren.Load() == 0 || !shotRequestRunning.CompareAndSwap(false, true) {
				return
			}
		}
	}()
}

// shotRequestLoop looks for the request every shotRequestPoll while a child
// runs.
func shotRequestLoop() {
	t := time.NewTicker(shotRequestPoll)
	defer t.Stop()
	for range t.C {
		if runningGameChildren.Load() == 0 {
			return
		}
		if _, err := os.Lstat(shotRequestFile); err != nil {
			continue
		}
		shotRequestTake()
	}
}

// shotRequestTake answers one request: the game's screen as a PNG in the
// screenshots folder, its path in screenshot.last, then the request deleted.
// A failure deletes screenshot.last, so a script waiting for the request to
// go away can tell.
func shotRequestTake() {
	path, err := shotRequestSave()
	if err != nil {
		logWarn("screenshot", "requested screenshot not saved: %v", err)
		_ = os.Remove(shotRequestLast)
	} else if werr := AtomicWrite(shotRequestLast, []byte(path+"\n"), 0o644); werr != nil {
		logWarn("screenshot", "could not write %s: %v", shotRequestLast, werr)
	}
	_ = os.Remove(shotRequestFile)
}

// shotRequestSave reads the panel now and writes it like a game screenshot
// (menushot.go gameShotSave, without the motor and the preview card).
func shotRequestSave() (string, error) {
	fr, err := gameShotGrab(nil)
	if err != nil {
		return "", err
	}
	img, err := gameShotImage(fr, int(gameShotLive.rotate.Load()),
		int(gameShotLive.canvasW.Load()), int(gameShotLive.canvasH.Load()))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return "", err
	}
	path, err := screenshotPath("game", time.Now())
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	logInfo("screenshot", "saved the game's screen to %s (requested by a script)", path)
	gameShotTell(path, nil)
	return path, nil
}
