package main

// alarmvol_test.go — the speaker during a ring: raised, restored, and
// recovered after a crash, against a fake mixer.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// alarmFakeMixer stands in for pactl/amixer and records every level written.
type alarmFakeMixer struct {
	mu    sync.Mutex
	level int
	sets  []int
}

func (m *alarmFakeMixer) written() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.sets...)
}

// alarmUseFakeMixer swaps the mixer seams and the marker path for one test.
func alarmUseFakeMixer(t *testing.T, level int) *alarmFakeMixer {
	t.Helper()
	dir := t.TempDir()
	mixer := &alarmFakeMixer{level: level}
	prevPath, prevAvail, prevGet, prevSet := alarmVolumeMarkerPath, alarmVolumeAvailable, alarmVolumeGet, alarmVolumeSet
	alarmVolumeMarkerPath = func() string { return filepath.Join(dir, "alarm-volume.json") }
	alarmVolumeAvailable = func() bool { return true }
	alarmVolumeGet = func() (int, bool) {
		mixer.mu.Lock()
		defer mixer.mu.Unlock()
		return mixer.level, true
	}
	alarmVolumeSet = func(pct int) error {
		mixer.mu.Lock()
		defer mixer.mu.Unlock()
		mixer.level = pct
		mixer.sets = append(mixer.sets, pct)
		return nil
	}
	t.Cleanup(func() {
		// Leave the worker lowered before the real mixer comes back.
		alarmVolumeRestoreWait(time.Second)
		alarmVolumeMarkerPath, alarmVolumeAvailable, alarmVolumeGet, alarmVolumeSet = prevPath, prevAvail, prevGet, prevSet
	})
	return mixer
}

// A ring raises the mixer to 100% after recording the level it found, and the
// end of the ring puts that level back and forgets the record.
func TestAlarmVolumeRaiseAndRestore(t *testing.T) {
	mixer := alarmUseFakeMixer(t, 40)
	alarmVolumeRaise()
	deadline := time.Now().Add(2 * time.Second)
	for !alarmVolumeMarkerExists() {
		if time.Now().After(deadline) {
			t.Fatal("the raise never recorded the level to restore")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !alarmVolumeRestoreWait(time.Second) {
		t.Fatal("the restore did not finish")
	}
	got := mixer.written()
	if len(got) != 2 || got[0] != 100 || got[1] != 40 {
		t.Fatalf("mixer writes = %v, want [100 40]", got)
	}
	if alarmVolumeMarkerExists() {
		t.Fatal("the marker outlived the restore")
	}
	// A second restore has nothing to give back.
	alarmVolumeRestoreWait(time.Second)
	if n := len(mixer.written()); n != 2 {
		t.Fatalf("an extra restore wrote the mixer (%d writes)", n)
	}
}

// A mixer already at full volume is left alone: there is nothing to restore.
func TestAlarmVolumeAlreadyFull(t *testing.T) {
	mixer := alarmUseFakeMixer(t, 100)
	alarmVolumeRaise()
	alarmVolumeRestoreWait(time.Second)
	if got := mixer.written(); len(got) != 0 {
		t.Fatalf("mixer writes = %v, want none", got)
	}
	if alarmVolumeMarkerExists() {
		t.Fatal("a marker was left for a mixer that was never moved")
	}
}

// A ring that never finished (a crash at 100%) is undone by the next start, and
// a marker that does not hold a level is dropped without touching the mixer.
func TestAlarmVolumeRecoverAfterACrash(t *testing.T) {
	mixer := alarmUseFakeMixer(t, 100)
	if err := os.WriteFile(alarmVolumeMarkerPath(), []byte(`{"level":35}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !alarmVolumeMarkerExists() {
		t.Fatal("the boot check would not see the marker")
	}
	var notes []string
	if err := alarmVolumeRecover(func(s string) { notes = append(notes, s) }); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := mixer.written(); len(got) != 1 || got[0] != 35 {
		t.Fatalf("mixer writes = %v, want [35]", got)
	}
	if alarmVolumeMarkerExists() {
		t.Fatal("the marker outlived its recovery")
	}
	if len(notes) == 0 || notes[len(notes)-1] != "volume back to 35%" {
		t.Fatalf("boot row notes = %v", notes)
	}

	if err := os.WriteFile(alarmVolumeMarkerPath(), []byte(`{"level":300}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := alarmVolumeRecover(func(string) {}); err != nil {
		t.Fatalf("recover of a bad marker: %v", err)
	}
	if got := mixer.written(); len(got) != 1 {
		t.Fatalf("a bad marker wrote the mixer: %v", got)
	}
	if alarmVolumeMarkerExists() {
		t.Fatal("a bad marker was kept")
	}
}

// Music playing under a ring is paused for it and resumed after it — and only
// what the ring paused is resumed.
func TestAlarmHoldsAndReleasesMusic(t *testing.T) {
	music.mu.Lock()
	prevPlaying, prevPaused := music.playing, music.paused
	music.playing, music.paused = true, false
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.playing, music.paused = prevPlaying, prevPaused
		music.mu.Unlock()
		alarmHeld.music, alarmHeld.video, alarmHeld.children = false, false, nil
	})

	alarmHoldMedia()
	music.mu.Lock()
	paused := music.paused
	music.mu.Unlock()
	if !paused {
		t.Fatal("music kept playing under the ring")
	}
	alarmReleaseMedia()
	music.mu.Lock()
	paused = music.paused
	music.mu.Unlock()
	if paused {
		t.Fatal("music stayed paused after the ring")
	}

	// Music the user had paused before the ring stays paused after it.
	music.mu.Lock()
	music.paused = true
	music.mu.Unlock()
	alarmHoldMedia()
	alarmReleaseMedia()
	music.mu.Lock()
	paused = music.paused
	music.mu.Unlock()
	if !paused {
		t.Fatal("the ring resumed music the user had paused")
	}
}
