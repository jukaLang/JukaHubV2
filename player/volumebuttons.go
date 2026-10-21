package main

// volumebuttons.go — hardware volume buttons.
//
// On the handheld the volume keys are handled by the OS mixer, not by SDL: a
// press changes the ALSA level and the app never sees a keysym for it. Holding
// one of those keys should skip a track instead of ramping the volume to the
// end of its range, and it should do that with the screen off — which is what
// this file adds.
//
// The buttons are read straight from the kernel's input devices. The app never
// grabs the device (no EVIOCGRAB), so the OS still gets the press and still
// changes the volume; only a long hold is turned into a track change.
//
// Everything except the device poll is plain data: the record parser understands
// both the 16-byte (32-bit) and 24-byte (64-bit) input_event layouts, and the
// hold state machine takes its clock as an argument, so both are unit tested.

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	evKey          = 1
	keyVolumeDown  = 114
	keyVolumeUp    = 115
	volumeDevices  = 32 // /dev/input/event0..31
	volumeReadTick = 40 * time.Millisecond
)

// volumeButtonEvent is one volume-key edge decoded from an input device.
type volumeButtonEvent struct {
	dir  int // +1 volume up, -1 volume down
	down bool
}

// parseInputEvent decodes one input_event record. The layout is a timeval
// followed by type, code and value, and the timeval is 8 bytes on 32-bit
// kernels and 16 on 64-bit ones, so only the trailing fields are read.
func parseInputEvent(record []byte) (typ, code uint16, value int32, ok bool) {
	if len(record) < 16 {
		return 0, 0, 0, false
	}
	tail := record[len(record)-8:]
	typ = binary.LittleEndian.Uint16(tail[0:2])
	code = binary.LittleEndian.Uint16(tail[2:4])
	value = int32(binary.LittleEndian.Uint32(tail[4:8]))
	// EV_SYN..EV_MSC is the whole range anyone writes.
	if typ > 5 {
		return 0, 0, 0, false
	}
	return typ, code, value, true
}

// inputRecordSize infers the input_event size from a read. The kernel only ever
// returns whole records, so the length decides: 24 bytes on 64-bit kernels (the
// handheld) and 16 on 32-bit ones.
func inputRecordSize(n int) int {
	if n >= 24 && n%24 == 0 {
		return 24
	}
	if n >= 16 && n%16 == 0 {
		return 16
	}
	if n >= 24 {
		return 24
	}
	return 16
}

// volumeEventsFromBuffer turns a read from an input device into volume-key
// edges, walking whole records of the size the read implies.
func volumeEventsFromBuffer(buf []byte) []volumeButtonEvent {
	var events []volumeButtonEvent
	size := inputRecordSize(len(buf))
	for i := 0; i+size <= len(buf); i += size {
		typ, code, value, ok := parseInputEvent(buf[i : i+size])
		if !ok || typ != evKey {
			continue
		}
		var dir int
		switch code {
		case keyVolumeUp:
			dir = 1
		case keyVolumeDown:
			dir = -1
		default:
			continue
		}
		switch value {
		case 1:
			events = append(events, volumeButtonEvent{dir: dir, down: true})
		case 0:
			events = append(events, volumeButtonEvent{dir: dir, down: false})
		}
		// value 2 is an autorepeat; the hold timer already covers it.
	}
	return events
}

// hardwareVolumeHold turns a press-and-hold into one skip. A short press does
// nothing at all here: the OS already handled it as a volume step.
type hardwareVolumeHold struct {
	dir    int
	since  time.Time
	fired  bool
	active bool
}

func (h *hardwareVolumeHold) press(dir int, now time.Time) {
	h.dir = dir
	h.since = now
	h.fired = false
	h.active = true
}

// release ends the hold. It reports the direction when the press had not yet
// turned into a skip, so the caller can tell a tap from a hold if it wants to.
func (h *hardwareVolumeHold) release(dir int) (held int, wasHold bool) {
	if !h.active || dir != h.dir {
		return 0, false
	}
	held, wasHold = h.dir, h.fired
	h.active = false
	h.fired = false
	return held, wasHold
}

// due reports the direction once the press has lasted long enough, and only
// once per press.
func (h *hardwareVolumeHold) due(now time.Time) int {
	if !h.active || h.fired {
		return 0
	}
	if now.Sub(h.since) < musicHoldSkipDelay {
		return 0
	}
	h.fired = true
	return h.dir
}

// volumeButtonState is the shared, locked wrapper the event loop and the ticker
// both use. The UI thread reads nothing from it; it only schedules.
type volumeButtonState struct {
	mu   sync.Mutex
	hold hardwareVolumeHold
	// lastSDLVolume is when SDL last delivered a volume keysym. On desktops the
	// same physical key arrives both ways, and one press must not skip twice.
	lastSDLVolume time.Time
}

var volumeButtons volumeButtonState

// volumeButtonSkipDelay is the ticker interval; the hold comparison is done
// inside due(), so this only bounds the reaction time.
func noteSDLVolumeKey() {
	volumeButtons.mu.Lock()
	volumeButtons.lastSDLVolume = time.Now()
	volumeButtons.mu.Unlock()
}

func sdlVolumeKeyRecent(now time.Time) bool {
	volumeButtons.mu.Lock()
	defer volumeButtons.mu.Unlock()
	return !volumeButtons.lastSDLVolume.IsZero() && now.Sub(volumeButtons.lastSDLVolume) < 250*time.Millisecond
}

// volumeButtonPress feeds one hardware edge into the hold state machine.
func volumeButtonPress(dir int, down bool) {
	now := time.Now()
	volumeButtons.mu.Lock()
	if down {
		volumeButtons.hold.press(dir, now)
	} else {
		volumeButtons.hold.release(dir)
	}
	volumeButtons.mu.Unlock()
	if down && sdlVolumeKeyRecent(now) {
		// Already handled through SDL; drop this duplicate so a desktop press
		// does not skip twice.
		volumeButtons.mu.Lock()
		volumeButtons.hold.active = false
		volumeButtons.mu.Unlock()
	}
}

// volumeButtonSkip performs the long-press action. It works from any scene,
// because the point of the feature is skipping while the panel is dark and the
// player is not on screen.
func volumeButtonSkip(dir int) {
	if sdlVolumeKeyRecent(time.Now()) {
		return
	}
	music.mu.Lock()
	playing := music.playing
	music.mu.Unlock()
	if !playing {
		return
	}
	if dir > 0 {
		musicNext()
		showToast("Next track", ToastInfo())
	} else {
		musicPrev()
		showToast("Previous track", ToastInfo())
	}
	PlayNavSound()
}

// startVolumeButtonWatcher begins watching the kernel input devices and the
// hold ticker. It is safe to call twice; the second call does nothing. When no
// device can be opened (a desktop without permission, Windows), it quietly
// leaves the SDL key path in charge.
func startVolumeButtonWatcher() {
	volumeWatcherOnce.Do(func() {
		go volumeHoldTicker()
		go volumeDeviceReader()
	})
}

var volumeWatcherOnce sync.Once

func volumeHoldTicker() {
	ticker := time.NewTicker(volumeReadTick)
	defer ticker.Stop()
	for now := range ticker.C {
		volumeButtons.mu.Lock()
		dir := volumeButtons.hold.due(now)
		volumeButtons.mu.Unlock()
		if dir != 0 {
			volumeButtonSkip(dir)
		}
	}
}

// volumeDeviceReader opens every input device it can and forwards volume-key
// edges. Devices that are not keyboards simply never produce them.
func volumeDeviceReader() {
	opened := 0
	for i := 0; i < volumeDevices; i++ {
		path := fmt.Sprintf("/dev/input/event%d", i)
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		opened++
		go volumeDeviceLoop(file)
	}
	if opened == 0 {
		LogScene("music").Debug("no input devices readable; hardware volume hold is unavailable")
	}
}

func volumeDeviceLoop(file *os.File) {
	defer file.Close()
	buf := make([]byte, 24*32)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			for _, ev := range volumeEventsFromBuffer(buf[:n]) {
				volumeButtonPress(ev.dir, ev.down)
			}
		}
		if err != nil {
			return
		}
	}
}
