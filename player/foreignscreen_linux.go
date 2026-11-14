//go:build linux

package main

// A firmware shortcut can start an emulator while JukaHub is still on screen.
// That game is not in runningGameChildren, so the normal launch handover cannot
// park the UI. A launcher that exits before a detached emulator does the same.
// Look for known emulator processes that actually hold a display device; only
// then let the game own the framebuffer. The probe is off the render thread.

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	foreignScreenProbeEvery = 250 * time.Millisecond
	foreignScreenSettle     = 350 * time.Millisecond
)

var foreignScreen struct {
	mu       sync.Mutex
	next     time.Time
	probing  bool
	lastSeen time.Time
	active   atomic.Bool
}

func foreignScreenSuspended() bool { return foreignScreen.active.Load() }

func foreignScreenTick(now time.Time) {
	if !IsTrimuiDevice() {
		return
	}
	foreignScreen.mu.Lock()
	if foreignScreen.probing || now.Before(foreignScreen.next) {
		foreignScreen.mu.Unlock()
		return
	}
	foreignScreen.probing = true
	foreignScreen.next = now.Add(foreignScreenProbeEvery)
	foreignScreen.mu.Unlock()
	go foreignScreenProbe()
}

func foreignScreenProbe() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("handover", "the emulator display check failed: %v", r)
		}
		foreignScreen.mu.Lock()
		foreignScreen.probing = false
		foreignScreen.mu.Unlock()
	}()
	procs := displayGuardProcs()
	view := make([]foreignScreenProcess, 0, len(procs))
	self := os.Getpid()
	for _, p := range procs {
		if p.pid != self {
			view = append(view, foreignScreenProcess{pid: p.pid, name: p.comm, state: p.state})
		}
	}
	owner, node := foreignGameDisplayOwner(view, displayGuardHeldNode)
	now := time.Now()
	foreignScreen.mu.Lock()
	wasActive := foreignScreen.active.Load()
	switch {
	case owner.pid != 0:
		foreignScreen.lastSeen = now
		foreignScreen.active.Store(true)
	case wasActive && now.Sub(foreignScreen.lastSeen) >= foreignScreenSettle:
		foreignScreen.active.Store(false)
	}
	isActive := foreignScreen.active.Load()
	foreignScreen.mu.Unlock()
	if !wasActive && isActive && runningGameChildren.Load() == 0 {
		if installBarCancelActive() {
			logWarn("handover", "cancelled an install because an emulator took the screen")
		}
		logWarn("handover", "%s pid %d owns %s; parking JukaHub until the emulator leaves the display", owner.name, owner.pid, node)
	}
	if wasActive && !isActive && runningGameChildren.Load() == 0 {
		wakeForegroundWork()
		raScopeScreenFree()
	}
}
