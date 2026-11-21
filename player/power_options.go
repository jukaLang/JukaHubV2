package main

// power_options.go — three settings for power off, the battery chip and the
// stick lights:
//
//   - "Ask before power off" (Settings → Power & battery, default On).
//     JukaHub has exactly two places whose only job is to confirm a power
//     off: the Power quick tile on Home, which opens the power card on Power
//     off (home_quicktiles.go), and holding the power button, which opens the
//     same card (powerkey.go). With the switch Off both power off at once,
//     through the card's own path: embedded playback stops, a RetroArch game
//     is saved for the next power-on, then the clean teardown
//     (devPowerRequest). Everywhere the card is a choice rather than a
//     confirmation — Home's power pill, Quick Settings' Power row, the
//     power-key short press in the Exit-card mode, the lock screen's power
//     menu — it stays: Sleep, Restart and Exit live there. Restart never had
//     a confirm-only step. Nothing destructive (factory reset, erase) is
//     touched.
//   - "Battery percentage" (Settings → Power & battery: Always, 20% or less,
//     Never; default Always, today's look). It decides whether the top bar's
//     battery chip — Home's header and every screen's status bar share it,
//     statusbar_status.go — draws the level beside the gauge. The gauge, its
//     colour, the blink and the charging bolt stay in every mode. The idle
//     screen's chip, Quick Settings' battery line and the battery widgets are
//     read-outs of the battery itself and keep their numbers.
//   - "Lights in games" (Settings → Stick lights, default On). Today a game
//     hands the stick lights back to the firmware's own colour, unless Screen
//     colours, an indicator or the achievement rainbow holds them (ledsync.go
//     runLEDLoop). Off keeps the rings dark while a game, emulator, port or
//     app owns the screen
//     (gameScreenSuspended): no firmware colour, no Screen colours, no
//     rainbow. The indicators — low battery, charging, standby — and the
//     in-game battery flash still light, so a warning is never lost, and so
//     does Juka's layer over a paused game (the in-game assistant is JukaHub
//     on screen). Back in JukaHub the user's lights return on the next frame.
//     With Stick lights off the rings are the firmware's and this does
//     nothing.
//
// The low-battery warning level is not here: batterywarn.go already turns
// the guard's fixed 5% message into "Low battery warning at" with
// RetroArch's on-screen line over games.
//
// Threads: the config is main-thread data. The power key's goroutines read
// "Ask before power off" through an atomic mirror refreshed every frame by
// powerKeyTick (powerOptKeyTick), and the LED loop reads "Lights in games"
// through one published by syncLEDLoopSettings (powerOptLEDPublish), the
// way its other settings reach it.

import (
	"strings"
	"sync/atomic"
	"time"
)

// ── Ask before power off ─────────────────────────────────────────────────

// powerOffConfirmKey is the Custom switch behind the row; missing is On.
const powerOffConfirmKey = "powerOffConfirm"

var (
	// powerOptSkipAsk mirrors "Ask before power off" being Off for the
	// power key's goroutines. False (ask) until the first frame mirrors it.
	powerOptSkipAsk atomic.Bool
	// powerOptOffAsked is a held power key's power off, waiting for the
	// main loop (powerOptKeyTick).
	powerOptOffAsked atomic.Bool
)

// powerOffConfirmWanted reports whether a power off asks first. A nil config
// asks. Main thread.
func powerOffConfirmWanted(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, powerOffConfirmKey, true)
}

// powerOffConfirmSettingsRow is Settings → Power & battery → Ask before power
// off, on the handheld only (devPowerSupported: nowhere else can JukaHub
// power the device off).
func powerOffConfirmSettingsRow() setRow {
	return setRow{
		ID: powerOffConfirmKey, Label: "Ask before power off", Kind: setKindToggle,
		Visible: func(*Config) bool { return devPowerSupported() },
		Detail: func(c *Config) string {
			key := powerKeyWatcherLive() && powerKeyModeFor(c) != powerKeyNothing
			switch {
			case powerOffConfirmWanted(c) && key:
				return "Holding the power button or the Power tile opens the power card first"
			case powerOffConfirmWanted(c):
				return "The Power tile on Home opens the power card first"
			case key:
				return "Holding the power button or the Power tile powers off at once"
			}
			return "The Power tile on Home powers off at once"
		},
		Get: powerOffConfirmWanted,
		Set: func(c *Config, on bool) {
			setStoreBool(c, powerOffConfirmKey, on)
			powerOptSkipAsk.Store(!on)
			setToast("Ask before power off: " + setOnOff(on))
		},
	}
}

// powerOptOffNow is the power card's Power off without the card
// (home_power.go homePowerPress): embedded playback stops, then
// devPowerRequest saves a running RetroArch game and powers off through the
// clean teardown. sound plays the activation sound, for callers whose press
// has not played one. Main thread.
func powerOptOffNow(why string, sound bool) {
	logInfo("power", "power off without asking (%s)", why)
	if sound {
		PlayActivateSound()
	}
	if snapshotVideoState().phase != "idle" {
		StopEmbeddedPlayback()
	}
	devPowerRequest(devPowerPoweroff)
}

// powerOptTileOff is the Power quick tile's press (home_quicktiles.go
// hqActivate): with "Ask before power off" Off it powers off and reports
// true; otherwise the tile opens the card on Power off as before. The press
// has played its sound (homeActivate). Main thread.
func powerOptTileOff(config *Config) bool {
	if !devPowerSupported() || powerOffConfirmWanted(config) {
		return false
	}
	powerOptOffNow("power tile", false)
	return true
}

// powerOptKeyHoldOff is a held power key (powerkey.go powerKeyLongDue,
// after its own game and sleep checks): with "Ask before power off" Off it
// asks the main loop for the power off and reports true, so no card opens.
// The key's goroutine.
func powerOptKeyHoldOff() bool {
	if !powerOptSkipAsk.Load() || !devPowerSupported() {
		return false
	}
	logInfo("power", "power key held: powering off (Ask before power off is Off)")
	powerOptOffAsked.Store(true)
	return true
}

// powerOptKeyMirrorAt throttles powerOptKeyTick's mirror (the row's
// Set mirrors at once). Main thread.
var powerOptKeyMirrorAt time.Time

// powerOptKeyTick is powerKeyTick's part, once a frame: it mirrors the
// setting for the key's goroutines once a second and runs a held key's power
// off. A game that took the screen since the hold (parked) drops the request,
// as it drops the card's. Main thread.
func powerOptKeyTick(config *Config, parked bool) {
	if now := time.Now(); now.Sub(powerOptKeyMirrorAt) >= time.Second {
		powerOptKeyMirrorAt = now
		powerOptSkipAsk.Store(!powerOffConfirmWanted(config))
	}
	if !powerOptOffAsked.Swap(false) || parked {
		return
	}
	powerOptOffNow("power key held", true)
}

// ── Battery percentage in the top bar ────────────────────────────────────

const (
	// batteryPercentKey is the Custom choice behind the row; missing or
	// unknown is Always.
	batteryPercentKey = "batteryPercent"

	batteryPctAlways = "always"
	batteryPctLow    = "low"
	batteryPctNever  = "never"

	// batteryPctLowAt is "20% or less": the battery saver's level, where the
	// number starts to matter.
	batteryPctLowAt = 20
)

// batteryPctModes are the row's stops, in carousel order.
var batteryPctModes = []setOption{
	{Value: batteryPctAlways, Label: "Always", Note: "The level beside the battery icon"},
	{Value: batteryPctLow, Label: "20% or less", Note: "The level only once the battery is low"},
	{Value: batteryPctNever, Label: "Never", Note: "The icon alone; its colour still warns"},
}

// batteryPercentMode reads the stored choice: the exact key only, so a
// missing one costs a map lookup, not a walk of every Custom key. Main thread.
func batteryPercentMode(c *Config) string {
	if c == nil {
		return batteryPctAlways
	}
	switch v := strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, batteryPercentKey))); v {
	case batteryPctLow, batteryPctNever:
		return v
	}
	return batteryPctAlways
}

// statusBatteryTextMode is batteryPercentMode for the bar, which asks several
// times a frame: read again once a second, and at once after the row changes
// it (mode ""). Render thread.
var statusBatteryTextMode struct {
	mode string
	at   time.Time
}

// statusBatteryTextShown reports whether the top bar's battery chip draws its
// level at pct. The chip's measure and its paint both ask
// (statusbar_status.go), so the bar never keeps room for a hidden number.
// Render thread (appConfig; nil in the render tests: Always).
func statusBatteryTextShown(pct int) bool {
	m := &statusBatteryTextMode
	if now := time.Now(); m.mode == "" || now.Sub(m.at) >= time.Second {
		m.mode, m.at = batteryPercentMode(appConfig), now
	}
	switch m.mode {
	case batteryPctNever:
		return false
	case batteryPctLow:
		return pct <= batteryPctLowAt
	}
	return true
}

// batteryPercentLabel is a stop's label.
func batteryPercentLabel(value string) string {
	for _, m := range batteryPctModes {
		if m.Value == value {
			return m.Label
		}
	}
	return batteryPctModes[0].Label
}

// batteryPercentSettingsRow is Settings → Power & battery → Battery
// percentage, on a device with a battery.
func batteryPercentSettingsRow() setRow {
	return setRow{
		ID: batteryPercentKey, Label: "Battery percentage", Kind: setKindChoice,
		Visible: func(*Config) bool { return getBatteryPercent() >= 0 },
		Detail: func(c *Config) string {
			switch batteryPercentMode(c) {
			case batteryPctLow:
				return "The top bar shows the level at 20% or less, the icon always"
			case batteryPctNever:
				return "The top bar shows the battery icon alone; its colour still warns"
			}
			return "The top bar shows the level beside the battery icon"
		},
		Options: func(*Config) []setOption { return batteryPctModes },
		Current: func(c *Config) int {
			mode := batteryPercentMode(c)
			for i, m := range batteryPctModes {
				if m.Value == mode {
					return i
				}
			}
			return 0
		},
		Value: func(c *Config) string { return batteryPercentLabel(batteryPercentMode(c)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, batteryPercentKey, value)
			statusBatteryTextMode.mode = "" // the bar reads it again this frame
			setToast("Battery percentage: " + batteryPercentLabel(value))
		},
	}
}

// ── Lights in games ──────────────────────────────────────────────────────

// ledGamesKey is the Custom switch behind the row; missing is On.
const ledGamesKey = "led_games"

// powerOptLEDGamesDark is "Lights in games" Off, published for the LED
// loop (powerOptLEDPublish).
var powerOptLEDGamesDark atomic.Bool

// ledGamesLit reports whether the lights may show in games (today's
// behaviour). Main thread.
func ledGamesLit(c *Config) bool {
	return c == nil || customBool(c.Variables.Custom, ledGamesKey, true)
}

// powerOptLEDPublish hands the switch to the LED loop. Main thread;
// syncLEDLoopSettings calls it with the loop's other settings.
func powerOptLEDPublish(c *Config) {
	powerOptLEDGamesDark.Store(!ledGamesLit(c))
}

// ledGamesOffLayers applies "Lights in games" Off to one frame's layers while
// a game owns the screen: Screen colours and the achievement rainbow are
// dropped (the FN switch's dark stays), and when nothing else is left to show
// — no indicator, no battery flash, no Juka — it reports dark: the loop then
// keeps the rings and paints them black instead of handing them back to the
// firmware's colour. With the user's lights off (the loop runs for Juka
// alone, ledJukaOnly) the rings are the firmware's and nothing changes. LED
// loop (ledsync.go runLEDLoop).
func ledGamesOffLayers(l ledLayers, now time.Time) (ledLayers, bool) {
	if !powerOptLEDGamesDark.Load() || ledJukaOnly.Load() || !gameScreenSuspended() {
		return l, false
	}
	l.screen = false
	if !l.silky.dark {
		l.silky = ledSilkyLayer{} // the rainbow goes, the FN switch's dark stays
	}
	return l, l.ind == ledIndNone && !l.level.on && !l.silky.holds() && !ledJukaHolds(now)
}

// ledGamesSettingsRow is Settings → Stick lights → Lights in games, shown
// while JukaHub drives the lights.
func ledGamesSettingsRow() setRow {
	return setRow{
		ID: "ledGames", Label: "Lights in games", Kind: setKindToggle,
		Visible: ledSettingsOn,
		Detail: func(c *Config) string {
			if ledGamesLit(c) {
				return "On: games get the firmware's own colour, or Screen colours"
			}
			return "Off: dark in games; low battery, charging and standby still show"
		},
		Get: ledGamesLit,
		Set: func(c *Config, on bool) {
			setStoreBool(c, ledGamesKey, on)
			powerOptLEDPublish(c)
			setToast("Lights in games: " + setOnOff(on))
		},
	}
}
