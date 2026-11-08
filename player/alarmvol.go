package main

// alarmvol.go — what a ringing alarm takes over besides the screen.
//
// An alarm has to be heard across a room and seen whatever was open, so for the
// length of a ring it borrows three things and gives each back exactly:
//
//   - the speaker: the system mixer goes to 100% (pactl/amixer, the same
//     allowlisted backends Quick Settings uses) and returns to the level it was
//     found at on snooze, dismiss or exit. The level being restored is written
//     to a marker in the temp directory before the mixer is touched, so a crash
//     mid-ring is undone by the next start (the temp directory is RAM on the
//     handheld, and a reboot resets the mixer anyway). Windows has no mixer
//     backend here; the ring is simply synthesised at full scale.
//   - the media: music and an embedded video are paused under the ring and
//     resumed after it, if the alarm was what paused them.
//   - a running game: on the handheld it is paused (SIGSTOP to its group) so
//     the alarm can be drawn over its last frame, and resumed on the way out.
//
// The mixer calls run a fixed command with a timeout, so they never run on the
// render thread: one worker goroutine applies raise and restore requests in
// the order they were made, which keeps a quick dismiss from racing the raise.

import (
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// alarmVolumeMarkerPath is where the level to restore is kept while the mixer
// is raised. A variable so tests can point it at a temp dir.
var alarmVolumeMarkerPath = func() string {
	return filepath.Join(os.TempDir(), "jukahub-alarm-volume.json")
}

// The mixer seams: the real backends, swapped by tests so no host mixer is
// touched during a test run.
var (
	alarmVolumeAvailable = func() bool { return volumeBackend() != "" }
	alarmVolumeGet       = systemVolume
	alarmVolumeSet       = setSystemVolume
)

// alarmVolumeMarker is the marker's content.
type alarmVolumeMarker struct {
	Level int `json:"level"`
}

type alarmVolumeRequest struct {
	raise bool
	done  chan struct{}
}

var alarmVol struct {
	once   sync.Once
	reqs   chan alarmVolumeRequest
	saved  int  // worker goroutine only
	raised bool // worker goroutine only
}

func alarmVolumeWorker() {
	alarmVol.once.Do(func() {
		alarmVol.reqs = make(chan alarmVolumeRequest, 8)
		go func() {
			for req := range alarmVol.reqs {
				if req.raise {
					alarmVolumeDoRaise()
				} else {
					alarmVolumeDoRestore()
				}
				if req.done != nil {
					close(req.done)
				}
			}
		}()
	})
}

// alarmVolumeRaise asks for the mixer at full volume. It never blocks.
func alarmVolumeRaise() {
	alarmVolumeWorker()
	select {
	case alarmVol.reqs <- alarmVolumeRequest{raise: true}:
	default:
	}
}

// alarmVolumeRestore asks for the level the ring found. It never blocks.
func alarmVolumeRestore() {
	alarmVolumeWorker()
	select {
	case alarmVol.reqs <- alarmVolumeRequest{}:
	default:
	}
}

// alarmVolumeRestoreWait asks for the restore and waits up to timeout for it
// (and everything queued before it) to be applied: the exit path, which must
// not leave the speaker at 100% behind it. It reports whether it finished.
func alarmVolumeRestoreWait(timeout time.Duration) bool {
	alarmVolumeWorker()
	done := make(chan struct{})
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case alarmVol.reqs <- alarmVolumeRequest{done: done}:
	case <-deadline.C:
		return false
	}
	select {
	case <-done:
		return true
	case <-deadline.C:
		return false
	}
}

func alarmVolumeDoRaise() {
	if alarmVol.raised || !alarmVolumeAvailable() {
		return
	}
	level, ok := alarmVolumeGet()
	if !ok {
		return
	}
	if level >= 100 {
		return // already as loud as it goes: nothing to give back later
	}
	// The marker goes down before the mixer moves: a crash between the two
	// then restores a level that is still correct.
	alarmVolumeWriteMarker(level)
	if err := alarmVolumeSet(100); err != nil {
		log.Printf("[alarm] could not raise the volume: %v", err)
		alarmVolumeRemoveMarker()
		return
	}
	alarmVol.saved, alarmVol.raised = level, true
	log.Printf("[alarm] volume %d%% -> 100%% for the ring", level)
}

func alarmVolumeDoRestore() {
	if !alarmVol.raised {
		return
	}
	alarmVol.raised = false
	if err := alarmVolumeSet(alarmVol.saved); err != nil {
		log.Printf("[alarm] could not restore the volume to %d%%: %v", alarmVol.saved, err)
		return // the marker stays, so the next start tries again
	}
	alarmVolumeRemoveMarker()
	log.Printf("[alarm] volume restored to %d%%", alarmVol.saved)
}

func alarmVolumeWriteMarker(level int) {
	data, err := json.Marshal(alarmVolumeMarker{Level: level})
	if err != nil {
		return
	}
	if err := os.WriteFile(alarmVolumeMarkerPath(), data, 0o600); err != nil {
		log.Printf("[alarm] could not record the volume to restore: %v", err)
	}
}

func alarmVolumeRemoveMarker() {
	if err := os.Remove(alarmVolumeMarkerPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("[alarm] could not remove %s: %v", alarmVolumeMarkerPath(), err)
	}
}

// alarmVolumeMarkerExists reports whether a previous run left the mixer
// raised. Boot checks it on the main thread to decide whether the card needs a
// row for it.
func alarmVolumeMarkerExists() bool {
	_, err := os.Stat(alarmVolumeMarkerPath())
	return err == nil
}

// alarmVolumeRecover puts back the level a ring that never finished left
// behind. It is a boot check (bootchecks.go), off the render thread.
func alarmVolumeRecover(note func(string)) error {
	data, err := os.ReadFile(alarmVolumeMarkerPath())
	if err != nil {
		return nil
	}
	var marker alarmVolumeMarker
	if json.Unmarshal(data, &marker) != nil || marker.Level < 0 || marker.Level > 100 {
		alarmVolumeRemoveMarker()
		return nil
	}
	if !alarmVolumeAvailable() {
		alarmVolumeRemoveMarker()
		return nil
	}
	if err := alarmVolumeSet(marker.Level); err != nil {
		note("could not restore the volume")
		return err
	}
	alarmVolumeRemoveMarker()
	note("volume back to " + strconv.Itoa(marker.Level) + "%")
	return nil
}

// ── Media and games under the ring ───────────────────────────────────────

// alarmHeld records what the ring paused, so only that is resumed. Main
// thread only (alarmTrigger and the stop paths run there, the exit path after
// the loop).
var alarmHeld struct {
	music    bool
	video    bool
	children []*exec.Cmd
}

// alarmHoldMedia pauses what is playing and the game owning the screen.
func alarmHoldMedia() {
	music.mu.Lock()
	musicPlaying := music.playing && !music.paused
	if musicPlaying {
		// What musicTogglePause does, minus its UI sound: the decoder stops
		// queueing and the buffered audio is dropped.
		music.paused = true
	}
	music.mu.Unlock()
	if musicPlaying {
		ClearVideoAudioQueue()
		alarmHeld.music = true
	}
	if playerPhase() == "playing" {
		ToggleVideoPlayback()
		alarmHeld.video = true
	}
	if alarmCanPauseChildren() {
		for _, cmd := range liveScreenChildren() {
			if err := alarmSignalChild(cmd, true); err != nil {
				log.Printf("[alarm] could not pause the running game: %v", err)
				continue
			}
			alarmHeld.children = append(alarmHeld.children, cmd)
		}
	}
}

// alarmReleaseMedia resumes what alarmHoldMedia paused. Children go first: a
// game must never be left stopped, least of all on the way out of the app,
// where a stopped child would also ignore the quit request.
func alarmReleaseMedia() {
	for _, cmd := range alarmHeld.children {
		if err := alarmSignalChild(cmd, false); err != nil {
			log.Printf("[alarm] could not resume the game: %v", err)
		}
	}
	alarmHeld.children = nil
	if alarmHeld.video {
		alarmHeld.video = false
		if playerPhase() == "paused" {
			ToggleVideoPlayback()
		}
	}
	if alarmHeld.music {
		alarmHeld.music = false
		music.mu.Lock()
		if music.playing && music.paused {
			music.paused = false
		}
		music.mu.Unlock()
	}
}
