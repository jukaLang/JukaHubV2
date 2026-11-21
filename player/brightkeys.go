package main

// brightkeys.go — Menu + volume sets the brightness.
//
// Holding Menu and pressing volume up or down steps the screen brightness by
// one step of the backend's grid (brightnessStepBy, brightnessctl.go) instead
// of the volume, in JukaHub and in games alike: the keys are read straight
// from the kernel's input devices by volumebuttons.go's reader, which is not
// tied to a screen and never grabs a device.
//
// Settings → Display → "Menu + volume sets brightness":
//   - Auto (the default): only where the firmware does not already do it — a
//     JukaHub OS boot (jukaos_native.go), or no stock keymon process running.
//     On stock firmware its keymon steps the brightness on the same chord, and
//     two steps per press would be one too many.
//   - On: always (where keymon also does it, a press may move two steps).
//   - Off: never; the press is the volume's, as before.
//
// The OS may still change the volume for that press (a mixer daemon that
// knows nothing of the chord), and so may JukaHub's own volume paths (the
// music player's volume keys). The level read just before the press is put
// back once the press has settled (brightKeysUndoAfter), only when it moved
// and only within brightKeysUndoWindow, so a real volume change later is never
// undone. The press is still a chord for the voice orb and the in-game menu
// (voiceOrbNoteVolumeKey), and never a held key's track skip.
//
// A release the reader missed must not turn every later volume press into a
// brightness step: before a chord counts, the kernel is asked whether Menu is
// really down (EVIOCGKEY, brightkeys_linux.go).
//
// Threading: the reader is a goroutine, while brightnessStepBy and
// volumeSetLevel are main-thread only. The reader queues the steps here and
// hwKeysTick applies them from the main loop (the parked game loop too).

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// brightKeysKey is the setting: brightKeysAuto (or unset), On or Off.
	brightKeysKey  = "menu_volume_brightness"
	brightKeysAuto = "auto"
	brightKeysOn   = "on"
	brightKeysOff  = "off"

	// brightKeysUndoAfter is how long after the last chord press the volume is
	// looked at: the OS's own step and the read back the press asked for
	// (volumeHardwareKeyDelay) both land inside it.
	brightKeysUndoAfter = volumeHardwareKeyDelay + 450*time.Millisecond
	// brightKeysUndoWindow bounds the undo: later, a change is not the chord's.
	brightKeysUndoWindow = 3 * time.Second
	// brightKeysKeymonEvery is how often Auto looks for the stock keymon again.
	brightKeysKeymonEvery = 30 * time.Second
	// brightKeysModeEvery is how often the tick reads the setting again.
	brightKeysModeEvery = time.Second
)

// The setting as published to the key reader.
const (
	brightKeysModeAuto int32 = iota
	brightKeysModeOn
	brightKeysModeOff
)

var brightKeys struct {
	// mode is the setting, published by brightKeysTick. Any goroutine.
	mode atomic.Int32
	// menuDown is Menu as the kernel last reported it, menuDev the device it
	// came from (asked again before a chord counts). Any goroutine.
	menuDown atomic.Bool
	menuDev  atomic.Pointer[os.File]

	mu sync.Mutex
	// steps are the brightness steps queued for the main thread (+ brighter).
	steps int
	// held: a volume key (0 down, 1 up) went down as part of a chord, so its
	// repeats and its release are the chord's too.
	held [2]bool
	// undo: the volume before the chord, put back if the OS moved it; undoAt
	// is the last chord press (powerMonoNow).
	undo    bool
	undoPct int
	undoAt  time.Duration
	// keymon: whether the stock keymon runs, as last looked at (keymonAt).
	keymon   bool
	keymonOK bool
	keymonAt time.Duration

	// modeAt is when the tick last read the setting. Main thread only.
	modeAt time.Duration
}

// brightKeysModeFrom reads the setting. Main thread.
func brightKeysModeFrom(c *Config) int32 {
	if c == nil {
		return brightKeysModeAuto
	}
	raw, _ := customString(c.Variables.Custom, brightKeysKey)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case brightKeysOn:
		return brightKeysModeOn
	case brightKeysOff:
		return brightKeysModeOff
	}
	return brightKeysModeAuto
}

// brightKeysFirmwareDoesIt reports whether the firmware steps the brightness
// on Menu + volume itself: not on a JukaHub OS boot, and on stock firmware
// while its keymon runs. The /proc scan is cached. Any goroutine.
func brightKeysFirmwareDoesIt() bool {
	if jukaosNativeActive() {
		return false
	}
	now := powerMonoNow()
	brightKeys.mu.Lock()
	if brightKeys.keymonOK && now-brightKeys.keymonAt < brightKeysKeymonEvery {
		running := brightKeys.keymon
		brightKeys.mu.Unlock()
		return running
	}
	brightKeys.mu.Unlock()
	running := brightKeysKeymonRunning()
	brightKeys.mu.Lock()
	brightKeys.keymon, brightKeys.keymonOK, brightKeys.keymonAt = running, true, now
	brightKeys.mu.Unlock()
	return running
}

// brightKeysActive reports whether a chord steps the brightness here and now.
// Any goroutine.
func brightKeysActive() bool {
	switch brightKeys.mode.Load() {
	case brightKeysModeOn:
		return true
	case brightKeysModeOff:
		return false
	}
	return !brightKeysFirmwareDoesIt()
}

// brightKeysNoteInput follows Menu in one read from an input device
// (volumeDeviceLoop). Key reader goroutine.
func brightKeysNoteInput(dev *os.File, buf []byte) {
	size := inputRecordSize(len(buf))
	for i := 0; i+size <= len(buf); i += size {
		typ, code, value, ok := parseInputEvent(buf[i : i+size])
		if !ok || typ != evKey || code != evdevBtnMode || value == 2 {
			continue
		}
		down := value == 1
		if down {
			// The device first, so a Menu seen down always has one to ask.
			brightKeys.menuDev.Store(dev)
		}
		brightKeys.menuDown.Store(down)
		if down {
			if brightKeysActive() {
				// A fresh volume reading before a volume key can come: the
				// level the chord may have to put back.
				volumeRefreshNow()
			}
		}
	}
}

// brightKeysChord is the reader's question for each volume-key edge: is it
// Menu + volume? Then the brightness steps (on the main thread) and the edge
// is no volume press for the rest of the watcher. A release reports whether
// its press was a chord. Key reader goroutine.
func brightKeysChord(dir int, down bool) bool {
	idx := 0
	if dir > 0 {
		idx = 1
	}
	if !down {
		brightKeys.mu.Lock()
		was := brightKeys.held[idx]
		brightKeys.held[idx] = false
		brightKeys.mu.Unlock()
		return was
	}
	if !brightKeys.menuDown.Load() || !brightKeysActive() {
		brightKeysUndoCancel()
		return false
	}
	if held, ok := brightKeysMenuHeldNow(brightKeys.menuDev.Load()); !ok || !held {
		// The kernel says Menu is up (its release was missed), or the device
		// that reported it is gone (its reader ended with Menu down): a Menu
		// nobody can confirm must not turn every later volume press into a
		// brightness step.
		brightKeys.menuDown.Store(false)
		brightKeysUndoCancel()
		return false
	}
	pct, known := volumeCurrent()
	brightKeys.mu.Lock()
	brightKeys.steps += dir
	brightKeys.held[idx] = true
	if known && !brightKeys.undo {
		brightKeys.undo, brightKeys.undoPct = true, pct
	}
	brightKeys.undoAt = powerMonoNow()
	brightKeys.mu.Unlock()
	// The OS may have moved the volume for this press: read it back.
	time.AfterFunc(volumeHardwareKeyDelay, volumeRefreshNow)
	return true
}

// brightKeysChordHeld reports whether a volume key is down as part of a
// chord: its repeats are not the volume card's. Any goroutine.
func brightKeysChordHeld() bool {
	brightKeys.mu.Lock()
	defer brightKeys.mu.Unlock()
	return brightKeys.held[0] || brightKeys.held[1]
}

// brightKeysUndoCancel drops a pending volume undo: a volume press that is
// no chord is the player's own, and the level it leads to stays (the undo
// would put back the level from before the chord). Any goroutine.
func brightKeysUndoCancel() {
	brightKeys.mu.Lock()
	brightKeys.undo = false
	brightKeys.mu.Unlock()
}

// brightKeysTick applies what the reader queued: the brightness steps, and
// the volume put back once the press has settled. Main thread, every frame.
func brightKeysTick(config *Config) {
	now := powerMonoNow()
	if brightKeys.modeAt == 0 || now-brightKeys.modeAt >= brightKeysModeEvery {
		brightKeys.modeAt = now
		brightKeys.mode.Store(brightKeysModeFrom(config))
	}
	brightKeys.mu.Lock()
	steps := brightKeys.steps
	brightKeys.steps = 0
	undo, undoPct, undoAt := brightKeys.undo, brightKeys.undoPct, brightKeys.undoAt
	if undo && now-undoAt >= brightKeysUndoAfter {
		brightKeys.undo = false
	} else {
		undo = false
	}
	brightKeys.mu.Unlock()
	if steps != 0 && brightnessKnownBackend() != "" {
		if before, after := brightnessStepBy(config, steps*brightnessGrid()); before != after {
			logDebug("brightness", "Menu + volume: %d%% -> %d%%", before, after)
		}
	}
	if undo && now-undoAt <= brightKeysUndoWindow {
		if cur, ok := volumeCurrent(); ok && cur != undoPct {
			volumeSetLevel(config, undoPct)
			logInfo("brightness", "Menu + volume: put the volume back from %d%% to %d%%", cur, undoPct)
		}
	}
}

// hwKeysTick is the main loop's call for the hardware chords: Menu + volume
// (here) and L2 + R2 on Home with the battery flash's own LED loop
// (ledbattlevel.go). overGame: the parked loop of a running game. Main thread,
// every frame (main.go, both loops).
func hwKeysTick(config *Config, overGame bool) {
	brightKeysTick(config)
	battChordTick(config, overGame)
	ledBattLevelTick(config)
}

// ── Settings → Display ─────────────────────────────────────────────────────

var brightKeysModeOrder = []struct {
	key, label, note string
}{
	{brightKeysAuto, "Auto", "Where the firmware's own keys do not already"},
	{brightKeysOn, "On", "Always; the firmware may step it too"},
	{brightKeysOff, "Off", "Menu + volume changes the volume"},
}

// brightKeysSettingsRow is "Menu + volume sets brightness".
func brightKeysSettingsRow() setRow {
	return setRow{
		ID: "menuVolumeBrightness", Label: "Menu + volume sets brightness", Kind: setKindChoice, Clamp: true,
		Detail: func(c *Config) string {
			switch brightKeysModeFrom(c) {
			case brightKeysModeOn:
				return "Hold Menu and press volume up or down, in games too"
			case brightKeysModeOff:
				return "Off: the volume keys only change the volume"
			}
			if brightKeysFirmwareDoesIt() {
				return "Auto: the firmware's own keys already do it here"
			}
			return "Auto: hold Menu and press volume up or down, in games too"
		},
		// Only where the kernel's input devices are read (volumebuttons.go):
		// a Windows handheld never sees the chord.
		Visible: func(*Config) bool { return setHandheld() && !IsWindows() },
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(brightKeysModeOrder))
			for _, m := range brightKeysModeOrder {
				out = append(out, setOption{Value: m.key, Label: m.label, Note: m.note})
			}
			return out
		},
		Current: func(c *Config) int {
			return int(brightKeysModeFrom(c))
		},
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			setStoreString(c, brightKeysKey, value)
			brightKeys.mode.Store(brightKeysModeFrom(c))
			for _, m := range brightKeysModeOrder {
				if m.key == value {
					setToast("Menu + volume sets brightness: " + m.label)
				}
			}
		},
	}
}
