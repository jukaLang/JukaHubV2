package main

import (
	"testing"
	"time"
)

func TestWineWestonWatchRecoversAStoppedDisplay(t *testing.T) {
	script := "/tmp/wine start.sh"
	wrap := wineGroupProc{comm: "bash", argv: []string{"bash", "westonwrap.sh", script}}
	weston := wineGroupProc{comm: "wp_weston", argv: []string{"wp_weston"}}
	now := time.Now()
	var watch wineWestonWatch

	if got := watch.step([]wineGroupProc{wrap}, script, false, false, time.Second, now); got != wineWestonWaiting {
		t.Fatalf("wrapper still waiting: got state %d", got)
	}
	if got := watch.step([]wineGroupProc{wrap, weston}, script, false, false, 2*time.Second, now.Add(time.Second)); got != wineWestonWaiting {
		t.Fatalf("Weston appeared: got state %d", got)
	}
	if got := watch.step([]wineGroupProc{wrap}, script, false, false, 3*time.Second, now.Add(2*time.Second)); got != wineWestonWaiting {
		t.Fatalf("Weston just disappeared: got state %d", got)
	}
	if got := watch.step([]wineGroupProc{wrap}, script, false, false, 5*time.Second, now.Add(2*time.Second+wineWestonGoneGrace)); got != wineWestonDied {
		t.Fatalf("Weston stayed gone past grace period: got state %d", got)
	}
}

func TestWineWestonWatchFindsAQuickDisplayCrash(t *testing.T) {
	script := "/tmp/wine start.sh"
	wrap := wineGroupProc{comm: "bash", argv: []string{"bash", "westonwrap.sh", script}}
	now := time.Now()
	var watch wineWestonWatch

	if got := watch.step([]wineGroupProc{wrap}, script, false, true, time.Second, now); got != wineWestonWaiting {
		t.Fatalf("wrapper announced Weston but it exited before polling: got state %d", got)
	}
	if got := watch.step([]wineGroupProc{wrap}, script, false, true, time.Second+wineWestonGoneGrace, now.Add(wineWestonGoneGrace)); got != wineWestonDied {
		t.Fatalf("Weston vanished after announcement: got state %d", got)
	}
}

func TestWineWestonWatchRecognizesProgramAndTimeout(t *testing.T) {
	script := "/tmp/wine start.sh"
	wrap := wineGroupProc{comm: "bash", argv: []string{"bash", "westonwrap.sh", script}}
	program := wineGroupProc{comm: "sh", argv: []string{"sh", script}}
	now := time.Now()
	var watch wineWestonWatch

	if got := watch.step([]wineGroupProc{wrap, program}, script, false, false, time.Second, now); got != wineWestonStarted {
		t.Fatalf("program launch script is running: got state %d", got)
	}
	if got := watch.step(nil, script, true, false, time.Second, now); got != wineWestonStarted {
		t.Fatalf("program already wrote exit status: got state %d", got)
	}
	if got := watch.step([]wineGroupProc{wrap}, script, false, false, wineWestonStartLimit, now); got != wineWestonTimedOut {
		t.Fatalf("program never started by deadline: got state %d", got)
	}
	if got := watch.step(nil, script, false, false, time.Second, now); got != wineWestonOver {
		t.Fatalf("session ended by itself: got state %d", got)
	}
}
