package main

// powerkey.go — the handheld's power button (PWR-1).
//
// The key is read from the kernel's input device for the PMIC's power key
// (powerkey_linux.go: "axp2202-pek" on the Smart Pro), the way
// volumebuttons.go reads the volume keys: never grabbed (no EVIOCGRAB), so the
// firmware and a running game still get every event, and the gamepad's own
// device is never opened at all. That is what lets the key work on Home, on
// every JukaHub screen and while a game, emulator or port owns the screen.
//
// What a press does (Settings → Power & battery → Power button):
//   - Sleep (the default): a short press sleeps the device (sleepNow,
//     powersleep.go), game paused and all;
//   - Exit card: a short press opens Home's "Exit JukaHub?" card
//     (home_power.go) instead;
//   - Nothing: JukaHub leaves the key to the firmware entirely.
//
// A long press (powerKeyLongPress) on Home or any JukaHub screen opens the
// Exit card in both Sleep and Exit-card modes. While a game owns the screen a
// long press does nothing in JukaHub: the firmware's own long press (its
// force-quit, then the PMIC's hard power-off) is left exactly as it was, and
// JukaHub cannot draw a card over a running game anyway.
//
// Double handling. On some firmwares the stock daemon (keymon) suspends the
// device on the power key by itself (wake_resume.go was written for exactly
// that). JukaHub must not sleep a second time on top of it, so:
//   - a short press is acted on only after powerKeyFirmwareWait; if the
//     device slept in that time (CLOCK_BOOTTIME ran ahead of CLOCK_MONOTONIC),
//     the firmware did it, JukaHub stands down and remembers that for the
//     session (powerKeyFirmwareSleeps): later presses are left to the firmware;
//   - a press whose read finds the device just slept is the wake press, and
//     is swallowed (as is every press during and just after a JukaHub sleep);
//   - and if the device sleeps again on its own within a few seconds of
//     waking from a JukaHub sleep, the firmware saw the same press: JukaHub
//     stands down the same way and says so once.
//
// The watcher is Linux-only; elsewhere powerKeyWatcherLive is false and the
// Settings row is hidden.

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// powerButtonKey is the Custom setting behind the Settings row.
	powerButtonKey = "powerButton"
	// powerKeyLongPress is how long a hold must last to open the Exit card.
	powerKeyLongPress = 1500 * time.Millisecond
	// powerKeyFirmwareWait is how long after a short press JukaHub waits to
	// see whether the firmware put the device to sleep itself.
	powerKeyFirmwareWait = 900 * time.Millisecond
	// powerKeySleptMin is how far the time-asleep clock must move for "the
	// device slept" — far above the clocks' jitter, far below any real sleep.
	powerKeySleptMin = 400 * time.Millisecond
	// powerKeyWakeGuard is how long after a JukaHub wake a power press is
	// still taken for the wake press itself.
	powerKeyWakeGuard = 1500 * time.Millisecond
	// powerKeyDoubleWindow is how soon after a JukaHub wake another sleep
	// counts as the firmware acting on the same press.
	powerKeyDoubleWindow = 5 * time.Second
	// powerKeySampleTick is how often the time-asleep clock is sampled while
	// the watcher runs (two cheap clock reads).
	powerKeySampleTick = 500 * time.Millisecond
	// powerKeyCardWait bounds the wait for Home after a long press elsewhere.
	powerKeyCardWait = 3 * time.Second
)

// powerKeyMode is what a short press does.
type powerKeyMode int32

const (
	powerKeySleep powerKeyMode = iota
	powerKeyExitCard
	powerKeyNothing
)

// powerKeyModes are the Settings choices, in carousel order.
var powerKeyModes = []struct {
	mode  powerKeyMode
	value string
	label string
}{
	{powerKeySleep, "sleep", "Sleep"},
	{powerKeyExitCard, "exit", "Exit card"},
	{powerKeyNothing, "off", "Nothing"},
}

var (
	// powerKeyModeLive is the setting, mirrored for the watcher goroutines.
	powerKeyModeLive atomic.Int32
	// powerKeyLive is set once at least one power-key device is being read.
	powerKeyLive atomic.Bool
	// powerKeyFirmwareSleeps is set once the firmware was seen sleeping the
	// device on the key itself; JukaHub then only swallows the wake press.
	powerKeyFirmwareSleeps atomic.Bool
	// powerKeyCardRequested asks the main loop to open the Exit card.
	powerKeyCardRequested atomic.Bool
	// powerMonoBase is the reference for the monotonic stamps below; Go's
	// monotonic clock does not count time suspended on Linux.
	powerMonoBase = time.Now()
	// powerOwnWakeMono is when (monotonic, since powerMonoBase) the last
	// JukaHub sleep ended; 0 before the first.
	powerOwnWakeMono atomic.Int64
)

// powerMonoNow is the monotonic time since powerMonoBase.
func powerMonoNow() time.Duration { return time.Since(powerMonoBase) }

// powerKeyWatcherLive reports whether the power key is being read.
func powerKeyWatcherLive() bool { return powerKeyLive.Load() }

// powerKeyModeFor reads the setting; anything unknown is Sleep.
func powerKeyModeFor(config *Config) powerKeyMode {
	if config == nil {
		return powerKeySleep
	}
	v, _ := customString(config.Variables.Custom, powerButtonKey)
	v = strings.ToLower(strings.TrimSpace(v))
	for _, m := range powerKeyModes {
		if m.value == v {
			return m.mode
		}
	}
	return powerKeySleep
}

// powerKeyNoteConfig mirrors the setting for the watcher.
func powerKeyNoteConfig(config *Config) {
	powerKeyModeLive.Store(int32(powerKeyModeFor(config)))
}

// powerKeyModeNow is the mirrored setting.
func powerKeyModeNow() powerKeyMode { return powerKeyMode(powerKeyModeLive.Load()) }

// powerButtonSettingsRow is Settings → Power & battery → Power button, shown
// only where the key can be read.
func powerButtonSettingsRow() setRow {
	return setRow{
		ID: powerButtonKey, Label: "Power button", Kind: setKindChoice,
		Visible: func(*Config) bool { return powerKeyWatcherLive() },
		Detail: func(c *Config) string {
			switch powerKeyModeFor(c) {
			case powerKeyExitCard:
				return "A press asks to exit JukaHub"
			case powerKeyNothing:
				return "JukaHub leaves the power button to the firmware"
			}
			if powerKeyFirmwareSleeps.Load() {
				return "The firmware already sleeps on a press, so JukaHub leaves it to it"
			}
			return "A press sleeps, even in a game; hold it to exit"
		},
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(powerKeyModes))
			for _, m := range powerKeyModes {
				out = append(out, setOption{Value: m.value, Label: m.label})
			}
			return out
		},
		Current: func(c *Config) int {
			mode := powerKeyModeFor(c)
			for i, m := range powerKeyModes {
				if m.mode == mode {
					return i
				}
			}
			return 0
		},
		Value: func(c *Config) string {
			mode := powerKeyModeFor(c)
			for _, m := range powerKeyModes {
				if m.mode == mode {
					return m.label
				}
			}
			return ""
		},
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			c.Variables.Custom[powerButtonKey] = value
			saveUserConfigDebounced(userConfigFromVariables(c))
			powerKeyNoteConfig(c)
			for _, m := range powerKeyModes {
				if m.value == value {
					setToast("Power button: " + m.label)
				}
			}
		},
	}
}

// ── The press state machine ─────────────────────────────────────────────

// powerKey is the one key's state. Several devices may report it; a press
// while it is already down (or a release while up) is ignored.
var powerKey struct {
	mu        sync.Mutex
	down      bool
	gen       uint64
	swallow   bool // this press was the wake press
	longFired bool
	pressOff  time.Duration // time asleep when the press came
	pressOK   bool
	// lastOff and lastOffOK are the sampler's latest time-asleep reading;
	// lastMono is when (monotonic) it was taken.
	lastOff   time.Duration
	lastOffOK bool
	lastMono  time.Duration
	// jumpMono is when (monotonic) the sampler last saw the device had
	// slept; a press soon after is the wake press even when the sampler ran
	// before the press was read.
	jumpMono time.Duration
}

// powerKeyEdge feeds one press (down) or release from a device reader.
func powerKeyEdge(down bool) {
	off, offOK := powerSleepAsleepClock()
	powerKey.mu.Lock()
	if down {
		if powerKey.down {
			powerKey.mu.Unlock()
			return
		}
		powerKey.down = true
		powerKey.gen++
		gen := powerKey.gen
		powerKey.longFired = false
		swallow := false
		switch {
		case powerSleepActive():
			// Asleep in JukaHub (or on the way in or out): a press during the
			// screen-off sleep is the wake; any other press is not acted on.
			powerSleepWakeFromPress()
			swallow = true
		case powerSleepJustWoke(powerKeyWakeGuard):
			swallow = true
		case offOK && powerKey.lastOffOK && off-powerKey.lastOff > powerKeySleptMin:
			// The device slept since the sampler last looked (the firmware's
			// sleep, or an RTC wake): this press woke it.
			swallow = true
		case powerKey.jumpMono != 0 && powerMonoNow()-powerKey.jumpMono < powerKeyWakeGuard:
			// The sampler got there first after the same wake.
			swallow = true
		}
		powerKey.swallow = swallow
		powerKey.pressOff, powerKey.pressOK = off, offOK
		if offOK {
			powerKey.lastOff, powerKey.lastOffOK, powerKey.lastMono = off, true, powerMonoNow()
		}
		powerKey.mu.Unlock()
		if swallow {
			logDebug("power", "power key: wake press swallowed")
			return
		}
		time.AfterFunc(powerKeyLongPress, func() { powerKeyLongDue(gen) })
		return
	}
	if !powerKey.down {
		powerKey.mu.Unlock()
		return
	}
	powerKey.down = false
	act := !powerKey.swallow && !powerKey.longFired
	gen, pressOff, pressOK := powerKey.gen, powerKey.pressOff, powerKey.pressOK
	powerKey.mu.Unlock()
	if act {
		go powerKeyShortPress(gen, pressOff, pressOK)
	}
}

// powerKeyLongDue fires powerKeyLongPress after a press that is still held.
func powerKeyLongDue(gen uint64) {
	powerKey.mu.Lock()
	if !powerKey.down || powerKey.gen != gen || powerKey.swallow {
		powerKey.mu.Unlock()
		return
	}
	powerKey.longFired = true
	powerKey.mu.Unlock()
	if powerKeyModeNow() == powerKeyNothing || gameScreenSuspended() || powerSleepActive() {
		return
	}
	logInfo("power", "power key held: Exit card")
	powerKeyCardRequested.Store(true)
}

// powerKeyShortPress acts on a short press once the firmware has had its
// chance. Its own goroutine.
func powerKeyShortPress(gen uint64, pressOff time.Duration, pressOK bool) {
	mode := powerKeyModeNow()
	if mode == powerKeyNothing {
		return
	}
	if powerKeyFirmwareSleeps.Load() && mode == powerKeySleep {
		return
	}
	time.Sleep(powerKeyFirmwareWait)
	powerKey.mu.Lock()
	newer := powerKey.gen != gen
	powerKey.mu.Unlock()
	if newer {
		return
	}
	if pressOK {
		if off, ok := powerSleepAsleepClock(); ok && off-pressOff > powerKeySleptMin {
			powerKeyLearnFirmware("the device slept on the press without JukaHub")
			return
		}
	}
	switch mode {
	case powerKeySleep:
		sleepNow("power-button")
	case powerKeyExitCard:
		if !gameScreenSuspended() && !powerSleepActive() {
			powerKeyCardRequested.Store(true)
		}
	}
}

// powerKeyLearnFirmware records that the firmware sleeps on the key itself.
func powerKeyLearnFirmware(why string) {
	if powerKeyFirmwareSleeps.Swap(true) {
		return
	}
	logInfo("power", "firmware handles the power key (%s); JukaHub stands down for this session", why)
}

// powerKeyNoteOwnWake is called when a JukaHub sleep ends with a real suspend.
func powerKeyNoteOwnWake() {
	powerOwnWakeMono.Store(int64(powerMonoNow()))
}

// powerKeySampler keeps the time-asleep clock's latest reading for the wake
// press test, and spots the firmware sleeping again right after a JukaHub
// sleep. Its own goroutine, started with the watcher.
func powerKeySampler() {
	ticker := time.NewTicker(powerKeySampleTick)
	defer ticker.Stop()
	for range ticker.C {
		off, ok := powerSleepAsleepClock()
		if !ok {
			continue
		}
		now := powerMonoNow()
		powerKey.mu.Lock()
		prevOff, prevOK, prevMono := powerKey.lastOff, powerKey.lastOffOK, powerKey.lastMono
		powerKey.lastOff, powerKey.lastOffOK, powerKey.lastMono = off, true, now
		if prevOK && off-prevOff > powerKeySleptMin {
			powerKey.jumpMono = now
		}
		powerKey.mu.Unlock()
		if !prevOK || off-prevOff < time.Second || powerSleepActive() {
			continue
		}
		own := time.Duration(powerOwnWakeMono.Load())
		if own == 0 || own > prevMono {
			continue // no JukaHub sleep yet, or this gap was JukaHub's own
		}
		if prevMono-own < powerKeyDoubleWindow && powerKeyModeNow() == powerKeySleep && !powerKeyFirmwareSleeps.Load() {
			powerKeyLearnFirmware("it slept again right after JukaHub's sleep")
			showToast("The firmware sleeps on the power button itself — JukaHub leaves it to it", ToastInfo())
		}
	}
}

// ── Main loop ─────────────────────────────────────────────────────────────

// powerKeyCard is a long press's wait for Home. Main thread.
var powerKeyCard struct {
	pending bool
	since   time.Time
}

// powerKeyTickAt throttles the once-a-second work. Main thread.
var powerKeyTickAt time.Time

// powerKeyTick is the main loop's part, once a frame — and once per parked
// iteration while a game owns the screen (parked): it mirrors the setting,
// keeps the next alarm's time for the sleep's RTC wake (the alarm list is
// main-thread data), and opens the Exit card a long press asked for.
func powerKeyTick(config *Config, parked bool) {
	now := time.Now()
	if now.Sub(powerKeyTickAt) >= time.Second {
		powerKeyTickAt = now
		powerKeyNoteConfig(config)
		if at := powerSleepNextAlarmAt(now); at.IsZero() {
			powerSleepNextAlarm.Store(0)
		} else {
			powerSleepNextAlarm.Store(at.UnixNano())
		}
	}
	if parked {
		// No card over a running game; a request made just before it took
		// the screen is dropped rather than popping up after it.
		powerKeyCardRequested.Store(false)
		powerKeyCard.pending = false
		return
	}
	if powerKeyCardRequested.Swap(false) {
		powerKeyCard.pending, powerKeyCard.since = true, now
	}
	if powerKeyCard.pending {
		if now.Sub(powerKeyCard.since) > powerKeyCardWait {
			powerKeyCard.pending = false
			return
		}
		if homePowerOpenFromKey(config) {
			powerKeyCard.pending = false
		}
	}
}

// ── Finding the key ──────────────────────────────────────────────────────

// evdevKeyPower is KEY_POWER.
const evdevKeyPower = 116

// powerKeyPickDevices chooses the input devices to read the power key from
// (their "eventN" nodes): those that can send KEY_POWER and are not a gamepad
// (BTN_SOUTH) — the pad is the game's and is never opened here. A PMIC power
// key by name ("axp2202-pek", any "*-pek", "power") wins over the rest, so a
// USB keyboard's power key is only used where nothing better exists. Pure.
func powerKeyPickDevices(devs []evdevDevice) []string {
	var named, other []string
	for _, d := range devs {
		if d.Handler == "" || d.Name == mbridgeMouseName || !evdevHas(d.Key, evdevKeyPower) || evdevHas(d.Key, evdevBtnSouth) {
			continue
		}
		name := strings.ToLower(d.Name)
		if strings.Contains(name, "pek") || strings.Contains(name, "power") {
			named = append(named, d.Handler)
		} else {
			other = append(other, d.Handler)
		}
	}
	if len(named) > 0 {
		return named
	}
	return other
}

// startPowerKeyWatcher starts reading the power key where the platform can.
func startPowerKeyWatcher() {
	powerKeyWatchOnce.Do(func() {
		// The handheld only (devPowerSupported): a Linux PC's power key is
		// its desktop's, not a launcher's.
		if devPowerSupported() && powerKeyStartDevices() {
			powerKeyLive.Store(true)
			go powerKeySampler()
		}
	})
}

var powerKeyWatchOnce sync.Once
