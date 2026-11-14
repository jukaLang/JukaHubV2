package main

// RetroArch's native command interface drives the in-game rewind and fast
// forward rows. Rewind is scoped to light cores so a heavy system does not
// allocate a large rewind buffer or lose speed merely by launching.

import (
	"os"
	"strings"
	"sync"
	"time"
)

var retroTransport struct {
	sync.Mutex
	rom    string
	port   int
	rewind bool
	fast   bool
}

func retroTransportLightSystem(system string) bool {
	switch strings.ToLower(strings.TrimSpace(system)) {
	case "nes", "snes", "game boy", "game boy color", "game boy advance",
		"mega drive", "master system", "game gear", "pc engine", "neo geo":
		return true
	}
	return false
}

func prepareRetroTransport(launcher, rom, system string) {
	retroTransport.Lock()
	retroTransport.rom, retroTransport.port, retroTransport.rewind, retroTransport.fast = "", 0, false, false
	retroTransport.Unlock()
	if !retroArchLauncher(launcher) {
		return
	}
	gmStates.mu.Lock()
	port := 0
	if gmStates.rom == rom {
		port = gmStates.port
	}
	gmStates.mu.Unlock()
	if port == 0 {
		return
	}
	rewind := retroTransportLightSystem(system)
	if rewind {
		for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
			if raw, err := os.ReadFile(path); err == nil {
				for _, key := range []string{"cheevos_hardcore_mode_enable", "cheevos_hardcore_mode"} {
					if value, ok := retroArchConfigValue(string(raw), key); ok &&
						(strings.EqualFold(value, "true") || value == "1") {
						rewind = false
					}
				}
			}
		}
	}
	if rewind {
		configured := false
		for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
			if _, err := raScopeApply(path, []raScopeValue{{Key: "rewind_enable", Value: "true"}}); err == nil {
				configured = true
			}
		}
		rewind = configured
	}
	retroTransport.Lock()
	retroTransport.rom, retroTransport.port, retroTransport.rewind = rom, port, rewind
	retroTransport.Unlock()
}

func retroTransportAvailable() (rewind, fast bool) {
	retroTransport.Lock()
	defer retroTransport.Unlock()
	if retroTransport.port == 0 || retroTransport.rom == "" || !gmStatesAvailable() {
		return false, false
	}
	return retroTransport.rewind, retroTransport.fast
}

func retroTransportCommand(rewind bool) {
	retroTransport.Lock()
	rom, port := retroTransport.rom, retroTransport.port
	if !rewind {
		retroTransport.fast = !retroTransport.fast
	}
	retroTransport.Unlock()
	if port == 0 {
		return
	}
	gaClose("the player changed game speed", true)
	go func() {
		time.Sleep(gmStateResumeDelay)
		retroTransport.Lock()
		same := retroTransport.rom == rom && retroTransport.port == port
		retroTransport.Unlock()
		if !same {
			return
		}
		if !rewind {
			if err := sendRetroArchNetCmd(port, "FAST_FORWARD"); err != nil {
				logWarn("gamemenu", "fast forward command failed: %v", err)
				retroTransport.Lock()
				if retroTransport.rom == rom {
					retroTransport.fast = !retroTransport.fast
				}
				retroTransport.Unlock()
			}
			return
		}
		// RetroArch's REWIND command steps back one frame. Repeated presses
		// move through a short stretch of gameplay after the menu gives the
		// controller and process back to RetroArch.
		for i := 0; i < 180; i++ {
			if !retroArchRunning() {
				return
			}
			if err := sendRetroArchNetCmd(port, "REWIND"); err != nil {
				logWarn("gamemenu", "rewind command failed: %v", err)
				return
			}
			time.Sleep(6 * time.Millisecond)
		}
	}()
}
