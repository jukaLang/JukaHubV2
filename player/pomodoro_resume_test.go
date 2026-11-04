package main

import (
	"testing"
	"time"
)

// A pause resumes the phase it interrupted. The old guess from the time left
// turned a work session paused with ten minutes to go into a break.
func TestPomodoroPauseResumesTheInterruptedPhase(t *testing.T) {
	prev := pom
	t.Cleanup(func() { pom = prev })

	pom = pomodoroState{phase: pomWork, total: pomodoroWorkMin * time.Minute, remaining: 10 * time.Minute}
	pomodoroTogglePause()
	if pom.phase != pomPaused {
		t.Fatalf("pause left phase %d", pom.phase)
	}
	pomodoroTogglePause()
	if pom.phase != pomWork {
		t.Fatalf("a work session paused with 10 minutes left resumed as phase %d, want work", pom.phase)
	}

	pom = pomodoroState{phase: pomLongBreak, total: pomodoroLongBreakMin * time.Minute, remaining: 14 * time.Minute}
	pomodoroTogglePause()
	pomodoroTogglePause()
	if pom.phase != pomLongBreak {
		t.Fatalf("a long break resumed as phase %d", pom.phase)
	}
}
