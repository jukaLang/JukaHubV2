package main

// batterywarn.go — the low-battery warning levels, as one setting.
//
// Settings → Power has four rows for them:
//
//   - "Low battery warning at" (Auto, 5–30%): where the header's battery chip
//     turns red, the stick lights' low-battery pulse starts (it clears 3%
//     above it, ledindicators.go), the "Battery 5% — plug in now" message
//     fires (lowbattery.go) and, with "Show over games", where RetroArch's own
//     message line starts saying "Battery 9%".
//   - "Blink from" (Auto: half the warning level, at least 3%): where the
//     header chip blinks.
//   - "Show over games": in a RetroArch game the message goes to RetroArch's
//     message line through its network commands (SHOW_MSG), once for every
//     percent the charge falls at or below the warning level, and the lights
//     flash the level (ledbattlevel.go). Nothing is ever drawn over the
//     game's picture. While Battery in games' lines run for the game
//     (gamehud.go) they are the ones RetroArch shows down to their 5% line,
//     and only the lights flash here; below that this line speaks again.
//   - "Save and suspend at" (Auto: 3%): where the guard in lowbattery.go saves
//     the running game and suspends.
//
// Auto keeps every surface exactly where it was before the setting existed:
// the header red below 15% and blinking below 5%, the lights at 10% (clear at
// 13%), the message at 5%, the guard at 3%. A level the user picks moves the
// header, the lights and the message to that one number.
//
// Nothing here warns while the battery holds its charge (a cable in and the
// level not falling, batteryHoldingCharge), or when the level is unknown, and
// the header never tints a charging battery (its green wins).
//
// The config is main-thread only. The levels are read there (batteryWarnTick,
// from the once-a-second guard tick, and every change in Settings) into an
// atomic copy the header reads every frame; the LED loop gets its level
// through ledExtrasFrom like its other settings.

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	// The custom keys behind the rows; "auto" (or nothing) is Auto.
	batteryWarnKeyLevel = "battery_warn_pct"
	batteryWarnKeyBlink = "battery_blink_pct"
	batteryWarnKeyGames = "battery_warn_games"
	batteryWarnKeySave  = "battery_save_pct"

	batteryWarnAuto = "auto"

	// The ranges the rows offer.
	batteryWarnMin  = 5
	batteryWarnMax  = 30
	batteryBlinkMin = 3
	batterySaveMin  = 3
	batterySaveMax  = 10

	// batteryWarnHeaderRedAuto and batteryWarnHeaderBlinkAuto are the header
	// chip's levels under Auto: red below 15%, blinking below 5%
	// (statusBatteryColor, statusBatteryMotion).
	batteryWarnHeaderRedAuto   = 15
	batteryWarnHeaderBlinkAuto = 5
	// batteryWarnClearAbove is how far above the warning level the charge
	// must climb before a warning can come again (the guard's +2, the
	// lights' +3 are their own).
	batteryWarnClearAbove = 2
)

// batteryWarnLevels is the four rows as numbers. warn, blink and save are 0
// under Auto. Pure value.
type batteryWarnLevels struct {
	warn  int
	blink int
	save  int
	games bool
}

// batteryWarnCur is the copy the header and the guard read. nil until the
// first tick: Auto.
var batteryWarnCur atomic.Pointer[batteryWarnLevels]

// batteryWarnNow is the published levels (Auto before the first publish). Any
// goroutine.
func batteryWarnNow() batteryWarnLevels {
	if p := batteryWarnCur.Load(); p != nil {
		return *p
	}
	return batteryWarnLevels{games: true}
}

// batteryWarnParse reads one stored level: 0 for Auto, a missing or
// unreadable value, or one outside lo..hi. Pure.
func batteryWarnParse(raw string, lo, hi int) int {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	if raw == "" || strings.EqualFold(raw, batteryWarnAuto) {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0
	}
	return n
}

// batteryWarnLevelsFrom reads the rows out of config. Main thread.
func batteryWarnLevelsFrom(c *Config) batteryWarnLevels {
	lv := batteryWarnLevels{games: true}
	if c == nil || c.Variables.Custom == nil {
		return lv
	}
	m := c.Variables.Custom
	if raw, ok := customString(m, batteryWarnKeyLevel); ok {
		lv.warn = batteryWarnParse(raw, batteryWarnMin, batteryWarnMax)
	}
	if raw, ok := customString(m, batteryWarnKeyBlink); ok {
		lv.blink = batteryWarnParse(raw, batteryBlinkMin, batteryWarnMax)
	}
	if raw, ok := customString(m, batteryWarnKeySave); ok {
		lv.save = batteryWarnParse(raw, batterySaveMin, batterySaveMax)
	}
	lv.games = customBool(m, batteryWarnKeyGames, true)
	return lv
}

// batteryWarnPublish reads the rows and hands them to the readers. Main thread.
func batteryWarnPublish(c *Config) batteryWarnLevels {
	lv := batteryWarnLevelsFrom(c)
	batteryWarnCur.Store(&lv)
	return lv
}

// messagePct is where the "plug in now" message (and the in-game line) comes:
// the picked level, or the guard's own 5% under Auto. Pure.
func (lv batteryWarnLevels) messagePct() int {
	if lv.warn > 0 {
		return lv.warn
	}
	return lowBatteryWarnPct
}

// ledPct is where the lights' low-battery pulse starts: the picked level, or
// 10% under Auto. Pure.
func (lv batteryWarnLevels) ledPct() int {
	if lv.warn > 0 {
		return lv.warn
	}
	return ledIndLowBatteryPct
}

// savePct is where the guard saves and suspends: the picked level, or 3%
// under Auto. Pure.
func (lv batteryWarnLevels) savePct() int {
	if lv.save > 0 {
		return lv.save
	}
	return criticalBatteryPct
}

// headerRed reports whether the header chip wears the warning red at pct: at
// or below the picked level, or below 15% under Auto. Pure.
func (lv batteryWarnLevels) headerRed(pct int) bool {
	if lv.warn > 0 {
		return pct <= lv.warn
	}
	return pct < batteryWarnHeaderRedAuto
}

// blinkPct is the level at and below which the header chip blinks: the
// picked one (never above the red), half the warning level (at least 3%)
// when only a warning level is picked, and below 5% under Auto. Pure.
func (lv batteryWarnLevels) blinkPct() int {
	red := batteryWarnHeaderRedAuto - 1
	if lv.warn > 0 {
		red = lv.warn
	}
	switch {
	case lv.blink > 0:
		return min(lv.blink, red)
	case lv.warn > 0:
		return max(batteryBlinkMin, lv.warn/2)
	}
	return batteryWarnHeaderBlinkAuto - 1
}

// headerBlinks reports whether the header chip blinks at pct. Pure.
func (lv batteryWarnLevels) headerBlinks(pct int) bool {
	return pct >= 0 && pct <= lv.blinkPct()
}

// batteryWarnHeaderRed and batteryWarnHeaderBlinks are the header's questions
// (statusbar_status.go), on the published levels. Render thread.
func batteryWarnHeaderRed(pct int) bool    { return batteryWarnNow().headerRed(pct) }
func batteryWarnHeaderBlinks(pct int) bool { return batteryWarnNow().headerBlinks(pct) }

// batteryWarnLEDPctFrom is the lights' low-battery level for ledExtrasFrom.
// Main thread.
func batteryWarnLEDPctFrom(c *Config) int { return batteryWarnLevelsFrom(c).ledPct() }

// ── Over games ─────────────────────────────────────────────────────────────

// batteryWarnGame is the in-game announcer's memory. Main thread.
var batteryWarnGame struct {
	// last is the percent last announced, 0 when none since the charge was
	// last above the warning level.
	last int
}

// batteryWarnTick publishes the levels and, in a RetroArch game, puts one
// line on RetroArch's message line for every percent the charge falls at or
// below the warning level. Called first thing by lowBatteryTick (once a
// second, the parked game loop too), whether or not the guard itself is on.
// Main thread.
func batteryWarnTick(config *Config) batteryWarnLevels {
	lv := batteryWarnPublish(config)
	percent := getBatteryPercent()
	limit := lv.messagePct()
	if percent > limit+batteryWarnClearAbove {
		batteryWarnGame.last = 0
		return lv
	}
	if percent < 0 || !lv.games || percent > limit {
		return lv
	}
	// Not while the battery holds its charge. The memory stays: an unknown
	// reading or a charger whose status flickers must not bring the same
	// line back each time; only a charge back above the level does.
	if batteryHoldingCharge() {
		return lv
	}
	if batteryWarnGame.last != 0 && percent >= batteryWarnGame.last {
		return lv
	}
	if !retroArchRunningCached() {
		return lv
	}
	// One try per percent, port or not: raLibraryPort scans /proc itself.
	batteryWarnGame.last = percent
	port := raLibraryPort()
	if port <= 0 {
		// A RetroArch the library did not start: its config says whether
		// the command port is on.
		if cfg, _, ok := retroArchConfig(); ok && networkCmdEnabled(cfg) {
			port = networkCmdPort(cfg)
		}
	}
	msg := batteryWarnGameLine(percent, batteryETALeft())
	// While Battery in games' lines run for this game, they reach the player
	// down to their 5% "plug in now" (gamehud.go): one voice on RetroArch's
	// message line, not two. Below that, and for a RetroArch the library did
	// not start (no lines run), this one speaks.
	if port > 0 && !(gameHudActive() && percent >= gameHudPlugAt) {
		go func() { _ = sendRetroArchNetCmd(port, "SHOW_MSG "+msg) }()
	}
	// The lights show the level too, where there are lights (ledbattlevel.go).
	ledBattLevelFlash(config, ledBattFlashLen)
	logInfo("battery", "in-game warning at %d%% (RetroArch port %d)", percent, port)
	return lv
}

// batteryWarnGameLine is the in-game line: "Battery 9%", with the measured
// time left when battery_eta.go has one. Pure.
func batteryWarnGameLine(percent int, left string) string {
	msg := fmt.Sprintf("Battery %d%%", percent)
	if left != "" {
		msg += " — " + left
	}
	return msg
}

// ── Settings → Power ───────────────────────────────────────────────────────

// batteryWarnRowVisible shows the rows on a device with a battery.
func batteryWarnRowVisible(*Config) bool { return getBatteryPercent() >= 0 }

// batteryWarnChoiceRow is one level row: Auto first, then the stops.
func batteryWarnChoiceRow(id, label, key string, auto setOption, stops []int, detail func(*Config) string,
	visible func(*Config) bool, toast string) setRow {
	options := func(*Config) []setOption {
		out := make([]setOption, 0, len(stops)+1)
		out = append(out, auto)
		for _, n := range stops {
			out = append(out, setOption{Value: strconv.Itoa(n), Label: strconv.Itoa(n) + "%"})
		}
		return out
	}
	return setRow{
		ID: id, Label: label, Kind: setKindChoice, Clamp: true,
		Detail:  detail,
		Visible: visible,
		Options: options,
		Current: func(c *Config) int {
			if c == nil {
				return 0
			}
			raw, _ := customString(c.Variables.Custom, key)
			n := batteryWarnParse(raw, 1, 100)
			for i, s := range stops {
				if s == n {
					return i + 1
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			setStoreString(c, key, value)
			batteryWarnPublish(c)
			// The lights' level rides on their published settings.
			if LEDSyncRunning() {
				syncLEDLoopSettings(c)
			}
			word := value + "%"
			if value == batteryWarnAuto {
				word = "Auto"
			}
			setToast(toast + ": " + word)
		},
	}
}

// batteryWarnLevelRow is "Low battery warning at".
func batteryWarnLevelRow() setRow {
	return batteryWarnChoiceRow("batteryWarnLevel", "Low battery warning at", batteryWarnKeyLevel,
		setOption{Value: batteryWarnAuto, Label: "Auto", Note: "Message at 5%, lights at 10%, red under 15%"},
		[]int{5, 8, 10, 12, 15, 20, 25, 30},
		func(c *Config) string {
			lv := batteryWarnLevelsFrom(c)
			if lv.warn <= 0 {
				return "Plug-in message at 5%, lights at 10%, red battery icon under 15%"
			}
			return fmt.Sprintf("Message, red battery icon and lights at %d%%", lv.warn)
		},
		batteryWarnRowVisible, "Low battery warning")
}

// batteryWarnBlinkRow is "Blink from": the header chip's blink.
func batteryWarnBlinkRow() setRow {
	return batteryWarnChoiceRow("batteryWarnBlink", "Blink from", batteryWarnKeyBlink,
		setOption{Value: batteryWarnAuto, Label: "Auto", Note: "Half the warning level, at least 3%"},
		[]int{3, 4, 5, 6, 8, 10, 12, 15},
		func(c *Config) string {
			return fmt.Sprintf("The battery icon blinks at %d%% or less", batteryWarnLevelsFrom(c).blinkPct())
		},
		batteryWarnRowVisible, "Battery icon blinks")
}

// batteryWarnGamesRow is "Show over games".
func batteryWarnGamesRow() setRow {
	return setRow{
		ID: "batteryWarnGames", Label: "Show over games", Kind: setKindToggle,
		Detail:  setStatic("RetroArch shows \"Battery 9%\" as the charge falls, and the lights flash it"),
		Visible: batteryWarnRowVisible,
		Get: func(c *Config) bool {
			return c != nil && customBool(c.Variables.Custom, batteryWarnKeyGames, true)
		},
		Set: func(c *Config, on bool) {
			setStoreBool(c, batteryWarnKeyGames, on)
			batteryWarnPublish(c)
			setToast("Battery warning over games: " + setOnOff(on))
		},
	}
}

// batterySaveLevelRow is "Save and suspend at", under the guard's switch.
func batterySaveLevelRow() setRow {
	return batteryWarnChoiceRow("batterySaveLevel", "Save and suspend at", batteryWarnKeySave,
		setOption{Value: batteryWarnAuto, Label: "Auto (3%)", Note: "Save the game and suspend at 3%"},
		[]int{4, 5, 6, 7, 8, 9, 10},
		func(c *Config) string {
			return fmt.Sprintf("While a game runs at %d%%: save state, sync, then suspend", batteryWarnLevelsFrom(c).savePct())
		},
		func(c *Config) bool { return batteryWarnRowVisible(c) && lowBatteryGuardEnabled(c) },
		"Save and suspend at")
}
