//go:build linux

package main

// A firmware shortcut can start an emulator while JukaHub is still on screen.
// That game is not in runningGameChildren, so the normal launch handover cannot
// park the UI. A launcher that exits before a detached emulator does the same.
// Look for known emulator processes that actually hold a display device; only
// then let the game own the framebuffer. The probe is off the render thread.
//
// A sleep stops the game (state T) without it leaving the display: the owner
// last seen running keeps the screen while JukaHub's sleep runs and for
// foreignScreenStoppedHold of awake time after (foreignGameDisplayOwner), so
// Home is not drawn under a paused game and left to take turns with it after
// the wake.

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	foreignScreenProbeEvery = 250 * time.Millisecond
	foreignScreenSettle     = 350 * time.Millisecond
	// foreignScreenStoppedHold is how long a stopped owner keeps the screen,
	// counted on the monotonic clock (which a suspend does not move): a
	// sleep's way in and out, with room to spare, but not a game stopped for
	// good.
	foreignScreenStoppedHold = 2 * time.Minute
)

var foreignScreen struct {
	mu       sync.Mutex
	next     time.Time
	probing  bool
	lastSeen time.Time
	active   atomic.Bool
	// owner is the emulator last seen running with the display, and
	// ownerRunAt when that was.
	owner      foreignScreenProcess
	ownerRunAt time.Time
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
			view = append(view, foreignScreenProcess{pid: p.pid, name: p.comm, state: p.state, start: p.start})
		}
	}
	// The owner a sleep stopped still holds the screen (the header).
	var keep foreignScreenProcess
	foreignScreen.mu.Lock()
	if foreignScreen.owner.pid > 0 && (powerSleepActive() || time.Since(foreignScreen.ownerRunAt) < foreignScreenStoppedHold) {
		keep = foreignScreen.owner
	}
	foreignScreen.mu.Unlock()
	owner, node := foreignGameDisplayOwner(view, displayGuardHeldNode, keep)
	now := time.Now()
	foreignScreen.mu.Lock()
	wasActive := foreignScreen.active.Load()
	switch {
	case owner.pid != 0:
		foreignScreen.lastSeen = now
		foreignScreen.active.Store(true)
		if owner.state != "T" && owner.state != "t" {
			foreignScreen.owner, foreignScreen.ownerRunAt = owner, now
		}
	case wasActive && now.Sub(foreignScreen.lastSeen) >= foreignScreenSettle:
		foreignScreen.active.Store(false)
		foreignScreen.owner = foreignScreenProcess{}
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
