package main

// volumebuttons_test.go — the kernel input-event decoder and the long-press
// state machine that turns a held volume key into a track skip.

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// inputRecord builds one input_event in either the 32-bit (16 byte) or 64-bit
// (24 byte) layout, so both can be exercised on any dev machine.
func inputRecord(typ, code uint16, value int32, wide bool) []byte {
	rec := make([]byte, 16)
	if wide {
		rec = make([]byte, 24)
	}
	binary.LittleEndian.PutUint64(rec[0:8], 12345) // timeval seconds
	if wide {
		binary.LittleEndian.PutUint64(rec[8:16], 678)
	}
	tail := rec[len(rec)-8:]
	binary.LittleEndian.PutUint16(tail[0:2], typ)
	binary.LittleEndian.PutUint16(tail[2:4], code)
	binary.LittleEndian.PutUint32(tail[4:8], uint32(value))
	return rec
}

func TestParseInputEventLayouts(t *testing.T) {
	for _, wide := range []bool{false, true} {
		rec := inputRecord(evKey, keyVolumeUp, 1, wide)
		typ, code, value, ok := parseInputEvent(rec)
		if !ok || typ != evKey || code != keyVolumeUp || value != 1 {
			t.Errorf("wide=%v -> (%d, %d, %d, %v)", wide, typ, code, value, ok)
		}
		if _, _, _, ok := parseInputEvent(rec[:12]); ok {
			t.Errorf("wide=%v: a short record was accepted", wide)
		}
	}
}

func TestVolumeEventsFromBuffer(t *testing.T) {
	var buf []byte
	buf = append(buf, inputRecord(evKey, keyVolumeUp, 1, true)...)   // press up
	buf = append(buf, inputRecord(3, 0, 0, true)...)                 // EV_ABS, ignored
	buf = append(buf, inputRecord(evKey, 30, 1, true)...)            // KEY_A, ignored
	buf = append(buf, inputRecord(evKey, keyVolumeDown, 2, true)...) // autorepeat, ignored
	buf = append(buf, inputRecord(evKey, keyVolumeDown, 1, true)...) // press down
	buf = append(buf, inputRecord(evKey, keyVolumeUp, 0, true)...)   // release up

	events := volumeEventsFromBuffer(buf)
	want := []volumeButtonEvent{
		{dir: 1, down: true},
		{dir: -1, down: true},
		{dir: 1, down: false},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}

	// The 32-bit layout parses the same way.
	events = volumeEventsFromBuffer(inputRecord(evKey, keyVolumeDown, 1, false))
	if len(events) != 1 || events[0] != (volumeButtonEvent{dir: -1, down: true}) {
		t.Errorf("32-bit layout -> %+v", events)
	}
}

func TestHardwareVolumeHold(t *testing.T) {
	start := time.Unix(3000, 0)
	var hold hardwareVolumeHold

	hold.press(1, start)
	if dir := hold.due(start.Add(100 * time.Millisecond)); dir != 0 {
		t.Fatalf("early due = %d, want 0", dir)
	}
	if dir := hold.due(start.Add(musicHoldSkipDelay + time.Millisecond)); dir != 1 {
		t.Fatalf("due after the delay = %d, want 1", dir)
	}
	// One skip per press, however long it is held.
	if dir := hold.due(start.Add(5 * time.Second)); dir != 0 {
		t.Fatalf("second due = %d, want 0", dir)
	}
	if _, wasHold := hold.release(1); !wasHold {
		t.Error("release did not report the hold")
	}

	// A short tap never fires.
	hold.press(-1, start)
	if _, wasHold := hold.release(-1); wasHold {
		t.Error("a short tap was reported as a hold")
	}
	if dir := hold.due(start.Add(time.Second)); dir != 0 {
		t.Errorf("released hold still fired: %d", dir)
	}

	// Releasing the other direction must not clear the current press.
	hold.press(1, start)
	if _, wasHold := hold.release(-1); wasHold {
		t.Error("the wrong direction reported a hold")
	}
	if dir := hold.due(start.Add(musicHoldSkipDelay)); dir != 1 {
		t.Errorf("due after a stray release = %d, want 1", dir)
	}
}

func TestVolumeKeysyms(t *testing.T) {
	for _, key := range []sdl.Keycode{sdl.K_VOLUMEUP, sdl.K_VOLUMEDOWN} {
		if !musicVolumeKey(key) {
			t.Errorf("%v is not a volume key", key)
		}
	}
	if dir, ok := musicVolumeDirection(sdl.K_VOLUMEDOWN); !ok || dir != -1 {
		t.Errorf("volume down -> %d, %v", dir, ok)
	}
	if dir, ok := musicVolumeDirection(sdl.K_VOLUMEUP); !ok || dir != 1 {
		t.Errorf("volume up -> %d, %v", dir, ok)
	}
	if _, ok := musicVolumeDirection(sdl.K_SPACE); ok {
		t.Error("space is not a volume key")
	}

	// The hold-to-skip tracker reports the right direction for both.
	var hold musicVolumeHold
	hold.press(sdl.K_VOLUMEUP, time.Unix(0, 0))
	if delta, _ := hold.press(sdl.K_VOLUMEUP, time.Unix(0, 0).Add(musicHoldSkipDelay)); delta != 1 {
		t.Errorf("held volume up delta = %d, want 1", delta)
	}
	var down musicVolumeHold
	down.press(sdl.K_VOLUMEDOWN, time.Unix(0, 0))
	if delta, _ := down.press(sdl.K_VOLUMEDOWN, time.Unix(0, 0).Add(musicHoldSkipDelay)); delta != -1 {
		t.Errorf("held volume down delta = %d, want -1", delta)
	}
}

// Volume keys must not wake the dark panel: they belong to the mixer, and the
// hold-to-skip path needs to see them.
func TestScreenOffVolumeKeysPassThrough(t *testing.T) {
	for _, mode := range []musicScreenMode{musicScreenAny, musicScreenControls, musicScreenSelect} {
		for _, key := range []sdl.Keycode{sdl.K_VOLUMEUP, sdl.K_VOLUMEDOWN, sdl.K_EQUALS, sdl.K_MINUS} {
			e := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: key}}
			if musicHandleScreenOffKey(e, mode) {
				t.Errorf("%v in mode %v was consumed; volume keys must pass through", key, mode)
			}
		}
	}
	// A transport key is still gated by the mode.
	e := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_SPACE}}
	if musicHandleScreenOffKey(e, musicScreenSelect) != true {
		t.Error("Select-only mode let play/pause through")
	}
}
