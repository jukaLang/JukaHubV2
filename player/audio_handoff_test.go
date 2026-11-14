package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// A game must find both the speaker and the headset free before its first
// audio open. JukaHub's virtual outputs must remain usable while it is away.
func TestAudioHandoffDetachesOutputs(t *testing.T) {
	outAudio.mu.Lock()
	oldDevs, oldNext := outAudio.devs, outAudio.nextID
	oldMix, oldStop, oldPCM := outAudio.mixDev, outAudio.mixStop, outAudio.pcm
	oldSuspended := outAudio.suspended
	outAudio.mu.Unlock()
	defer func() {
		outAudio.mu.Lock()
		outAudio.devs, outAudio.nextID = oldDevs, oldNext
		outAudio.mixDev, outAudio.mixStop, outAudio.pcm = oldMix, oldStop, oldPCM
		outAudio.suspended = oldSuspended
		outAudio.mu.Unlock()
	}()
	stop := make(chan struct{})
	func() {
		outAudio.mu.Lock()
		defer outAudio.mu.Unlock()
		outAudio.devs = map[sdl.AudioDeviceID]*outDev{
			outVirtualBase + 1: {real: 11, draining: 12, fifo: []byte{1, 2}, head: 1},
			outVirtualBase + 2: {real: 13},
		}
		outAudio.mixDev, outAudio.mixStop, outAudio.pcm = 14, stop, "bluealsa:test"
		outAudio.suspended = false
		ids := outDetachForChildLocked()
		if len(ids) != 4 {
			t.Fatalf("closed devices = %v, want mixer plus three outputs", ids)
		}
		seen := make(map[sdl.AudioDeviceID]bool)
		for _, id := range ids {
			seen[id] = true
		}
		for _, id := range []sdl.AudioDeviceID{11, 12, 13, 14} {
			if !seen[id] {
				t.Errorf("device %d was kept open", id)
			}
		}
		for _, d := range outAudio.devs {
			if d.real != 0 || d.draining != 0 || len(d.fifo) != 0 {
				t.Fatalf("output still owns audio: %+v", d)
			}
		}
		if !outAudio.suspended || outAudio.mixDev != 0 || outAudio.pcm != "" {
			t.Fatal("audio route was not suspended")
		}
	}()
	select {
	case <-stop:
	default:
		t.Fatal("headset mixer was not stopped")
	}
	var got sdl.AudioSpec
	want := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16SYS, Channels: 1, Samples: 512}
	id, err := outOpen(&want, &got, 0)
	if err != nil || id == 0 || got.Freq != want.Freq {
		t.Fatalf("virtual device during handoff: id=%d spec=%+v error=%v", id, got, err)
	}
	if err := outQueue(id, []byte{1, 2}); err == nil || outQueued(id) != 0 {
		t.Fatalf("queued audio reached the game: error=%v queued=%d", err, outQueued(id))
	}
	if outSetRoute("bluealsa:other", 1) {
		t.Fatal("headset reopened while the game owns audio")
	}
	outClose(id)
}

func TestScreenChildAudioReturnsAfterFailedLaunch(t *testing.T) {
	oldNeeded := screenChildAudioNeeded
	screenChildAudioNeeded = func() bool { return true }
	defer func() { screenChildAudioNeeded = oldNeeded }()
	outAudio.mu.Lock()
	oldDevs, oldSuspended := outAudio.devs, outAudio.suspended
	oldMix, oldStop, oldPCM := outAudio.mixDev, outAudio.mixStop, outAudio.pcm
	outAudio.devs, outAudio.suspended = nil, false
	outAudio.mixDev, outAudio.mixStop, outAudio.pcm = 0, nil, ""
	outAudio.mu.Unlock()
	defer func() {
		outAudio.mu.Lock()
		outAudio.devs, outAudio.suspended = oldDevs, oldSuspended
		outAudio.mixDev, outAudio.mixStop, outAudio.pcm = oldMix, oldStop, oldPCM
		outAudio.mu.Unlock()
	}()
	screenChildAudio.mu.Lock()
	oldCount, oldOverlays := screenChildAudio.count, screenChildAudio.overlays
	screenChildAudio.count, screenChildAudio.overlays = 0, 0
	screenChildAudio.mu.Unlock()
	defer func() {
		screenChildAudio.mu.Lock()
		screenChildAudio.count, screenChildAudio.overlays = oldCount, oldOverlays
		screenChildAudio.mu.Unlock()
	}()

	cmd := exec.Command(filepath.Join(t.TempDir(), "missing-emulator"))
	if err := screenChildStart(cmd); err == nil {
		t.Fatal("missing emulator started")
	}
	screenChildAudio.mu.Lock()
	count := screenChildAudio.count
	screenChildAudio.mu.Unlock()
	outAudio.mu.Lock()
	suspended := outAudio.suspended
	outAudio.mu.Unlock()
	if count != 0 || suspended {
		t.Fatalf("failed launch kept audio: count=%d suspended=%v", count, suspended)
	}
}

func TestScreenChildAudioBorrowedByPausedOverlay(t *testing.T) {
	oldNeeded := screenChildAudioNeeded
	screenChildAudioNeeded = func() bool { return true }
	defer func() { screenChildAudioNeeded = oldNeeded }()
	outAudio.mu.Lock()
	oldDevs, oldSuspended := outAudio.devs, outAudio.suspended
	oldMix, oldStop, oldPCM := outAudio.mixDev, outAudio.mixStop, outAudio.pcm
	outAudio.devs, outAudio.suspended = nil, false
	outAudio.mixDev, outAudio.mixStop, outAudio.pcm = 0, nil, ""
	outAudio.mu.Unlock()
	defer func() {
		outAudio.mu.Lock()
		outAudio.devs, outAudio.suspended = oldDevs, oldSuspended
		outAudio.mixDev, outAudio.mixStop, outAudio.pcm = oldMix, oldStop, oldPCM
		outAudio.mu.Unlock()
	}()
	screenChildAudio.mu.Lock()
	oldCount, oldOverlays := screenChildAudio.count, screenChildAudio.overlays
	screenChildAudio.count, screenChildAudio.overlays = 1, 0
	screenChildAudio.mu.Unlock()
	defer func() {
		screenChildAudio.mu.Lock()
		screenChildAudio.count, screenChildAudio.overlays = oldCount, oldOverlays
		screenChildAudio.mu.Unlock()
	}()

	audioRouteReleaseForChild()
	if !screenChildAudioOverlayBegin() || !screenChildAudioOverlayBegin() {
		t.Fatal("paused overlays could not borrow audio")
	}
	outAudio.mu.Lock()
	suspended := outAudio.suspended
	outAudio.mu.Unlock()
	if suspended {
		t.Fatal("audio stayed suspended during the overlay")
	}
	screenChildAudioOverlayEnd()
	outAudio.mu.Lock()
	suspended = outAudio.suspended
	outAudio.mu.Unlock()
	if suspended {
		t.Fatal("one overlay returned audio while another still needed it")
	}
	screenChildAudioOverlayEnd()
	outAudio.mu.Lock()
	suspended = outAudio.suspended
	outAudio.mu.Unlock()
	if !suspended {
		t.Fatal("game did not get audio back after the overlay")
	}
	screenChildAudioDone()
	outAudio.mu.Lock()
	suspended = outAudio.suspended
	outAudio.mu.Unlock()
	if suspended {
		t.Fatal("audio was not restored after the game exited")
	}
}
