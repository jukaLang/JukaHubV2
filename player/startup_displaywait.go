package main

// startup_displaywait.go — JukaHub OS on the Smart Pro starts JukaHub before
// its init has finished loading the display, GPU, input and sound modules
// (os/smartpro/init), so JukaHub's own start — the Go runtime, the logs, the
// design file decoding on its goroutine — runs while the drivers load instead
// of after them. The image's init names a marker file in JUKAHUB_WAIT_DISPLAY
// and writes it once those modules are in and the stick daemon is up; JukaHub
// waits for it here, right before SDL opens the display and looks for pads.
//
// Everywhere else (stock firmware, the plastron image, a PC) the variable is
// unset and this returns at once. A marker that never comes costs at most
// startupDisplayWaitMax: JukaHub then starts SDL anyway, as it did before the
// wait existed. Unset JUKAHUB_WAIT_DISPLAY (or EARLY_START=0 in boot.env, which
// makes the init load the drivers first) turns the overlap off for an A/B.

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// startupDisplayWaitPoll is how often the marker is looked for: a stat
	// every few milliseconds is nothing next to the module loading it waits on.
	startupDisplayWaitPoll = 3 * time.Millisecond
	// startupDisplayWaitMax bounds the wait.
	startupDisplayWaitMax = 8 * time.Second
)

// startupWaitDisplay waits for the marker JUKAHUB_WAIT_DISPLAY names, if any,
// and logs how long it took (boot.log on the image), so a device run shows
// what the overlap saved. Main goroutine, before sdl.Init.
func startupWaitDisplay() {
	path := strings.TrimSpace(os.Getenv("JUKAHUB_WAIT_DISPLAY"))
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		logWarn("startup", "JUKAHUB_WAIT_DISPLAY is not an absolute path (%q): not waiting", path)
		return
	}
	began := time.Now()
	deadline := began.Add(startupDisplayWaitMax)
	for {
		if _, err := os.Stat(path); err == nil {
			if waited := time.Since(began); waited >= time.Millisecond {
				logInfo("startup", "display ready after %d ms (drivers loaded beside JukaHub's start)", waited.Milliseconds())
			} else {
				logInfo("startup", "display already ready")
			}
			return
		}
		if !time.Now().Before(deadline) {
			logWarn("startup", "no display-ready marker after %s (%s): starting the display anyway", startupDisplayWaitMax, path)
			return
		}
		time.Sleep(startupDisplayWaitPoll)
	}
}
