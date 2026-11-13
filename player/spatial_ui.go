package main

// spatial_ui.go — where spatial audio is switched: four rows in the Audio panel
// (musicsettings.go asks spatialPanelValue / spatialPanelAdjust first) and one
// choice row in Settings → Sound & haptics (spatialSettingsRow, setmodel.go).

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// The Audio panel's spatial rows. The numbers sit far above the panel's own
// iota rows, so neither list can shift the other.
const (
	msRowSpatialMode musicSettingsRowKind = 100 + iota
	msRowSpatialStrength
	msRowSpatialRoom
	msRowSpatialGames
)

// The rows' explanations, shown under the list when the row is focused.
const (
	spatialModeHint = "Virtual surround for what you hear: Headphones places the sound around you, " +
		"Speakers widens the built-in pair, Auto picks by the output in use. Games and apps follow it too (row below)."
	spatialStrengthHint = "How far the effect goes: 25% is a light touch, 100% the full room and rear speakers."
	spatialRoomHint     = "The virtual room the headphone mode plays in: Small is close and dry, Large is a hall."
	spatialGamesHint    = "Games and apps through JukaHub's audio plugin, from the next one you start, with the mode above: " +
		"on the handheld every emulator, port and app, elsewhere RetroArch."
)

// spatialOffNote is what a change while the mode is Off adds to its toast:
// the setting is kept, but nothing is heard until a mode is chosen.
const spatialOffNote = " (Spatial audio is off)"

// spatialPanelValue is the right-hand text of a spatial row; ok is false for
// every other row.
func spatialPanelValue(kind musicSettingsRowKind, config *Config) (string, bool) {
	switch kind {
	case msRowSpatialMode:
		return spatialModeValue(config), true
	case msRowSpatialStrength:
		return fmt.Sprintf("%d%%", spatialStrengthPercent(config)), true
	case msRowSpatialRoom:
		return spatialRoomLabel(spatialSettingsFor(config).Room), true
	case msRowSpatialGames:
		on := spatialSettingsFor(config).Games
		ok, why, known := spatialGamesAvailabilityCached()
		switch {
		case !known || spatialGamesChecking(ok, why):
			return "Checking…", true
		case !ok && on:
			// Still on in the settings, but nothing can happen: say both.
			return "On · not available", true
		case !ok:
			return "Not available", true
		case on && strings.HasPrefix(why, spatialGamesRetroArchOnly):
			// The system route said no here; RetroArch's filter still works.
			return "On · RetroArch only", true
		}
		return onOff(on), true
	}
	return "", false
}

// spatialPanelAdjust changes a spatial row (dir < 0 left, > 0 right) and
// reports whether the row was one of them. It saves, publishes and plays its own
// feedback, like the panel's other rows.
func spatialPanelAdjust(kind musicSettingsRowKind, dir int, config *Config) bool {
	switch kind {
	case msRowSpatialMode:
		ShowToast(spatialCycleMode(config, dir), ToastKindInfo)
	case msRowSpatialStrength:
		pct := spatialStepStrength(config, dir)
		msg := fmt.Sprintf("Spatial strength: %d%%", pct)
		if spatialNormalizeMode(spatialSettingsFor(config).Mode) == "off" {
			msg += spatialOffNote
		}
		ShowToast(msg, ToastKindInfo)
	case msRowSpatialRoom:
		room := spatialCycleRoom(config, dir)
		ShowToast("Virtual room: "+spatialRoomLabel(room)+spatialRoomNote(spatialSettingsFor(config)), ToastKindInfo)
	case msRowSpatialGames:
		return spatialGamesAdjust(config)
	default:
		return false
	}
	PlayNavSound()
	return true
}

// spatialRoomNote is what the room toast adds when the room is not what is
// heard now: Off, or a speakers mode (the room belongs to the headphone model).
func spatialRoomNote(s spatialSettings) string {
	switch {
	case spatialNormalizeMode(s.Mode) == "off":
		return spatialOffNote
	case spatialRoomHeard(s):
		return ""
	case spatialNormalizeMode(s.Mode) == "auto":
		return " (heard on headphones)"
	}
	return " (the Headphones mode uses it)"
}

// spatialGamesAdjust is a press on the games row. Turning it on needs the
// plugin and a route for it (spatialGamesAvailability); turning it off always works, so a
// setting left on after the plugin went away can still be cleared.
func spatialGamesAdjust(config *Config) bool {
	s := spatialSettingsFor(config)
	if s.Games {
		spatialStore(config, spatialGamesKey, boolString(false))
		ShowToast("Spatial audio in games: Off", ToastKindInfo)
		PlayNavSound()
		return true
	}
	ok, why, known := spatialGamesAvailabilityCached()
	switch {
	case !known || spatialGamesChecking(ok, why):
		// Not an error: the answer is a moment away.
		ShowToast("Checking whether games can play through JukaHub's audio plugin — try again in a moment", ToastKindInfo)
		PlayNavSound()
		return true
	case !ok:
		if why == "" {
			why = "Spatial audio in games is not available here"
		}
		ShowToast(why, ToastKindInfo)
		PlayErrorSound()
		return true
	}
	spatialStore(config, spatialGamesKey, boolString(true))
	if spatialEffectiveMode(spatialSettingsFor(config)) == spatialDSPOff {
		// prepareSpatialAudio skips a launch while the mode is off, so the
		// switch alone changes nothing yet.
		ShowToast("Spatial audio in games: On — choose a Spatial audio mode to hear it", ToastKindInfo)
	} else {
		ShowToast("Spatial audio in games: On — from the next game you start", ToastKindSuccess)
	}
	PlayNavSound()
	return true
}

// ── Whether games can have it ─────────────────────────────────────────────

// spatialGamesAvailState is the last answer of spatialGamesAvailability
// (spatial_games.go), for the Audio panel, which asks on every frame it draws.
// The check stats files, looks RetroArch up on PATH and may start a probe of
// its binary — work that does not belong on the render thread — so the answer
// is fetched on a goroutine and the panel reads the cache.
type spatialGamesAvailState struct {
	at      time.Time // when the answer arrived
	have    bool      // there is an answer at all
	ok      bool
	why     string
	running bool // a refresh is out
}

var (
	spatialGamesAvailMu sync.Mutex
	spatialGamesAvail   spatialGamesAvailState
	// Test seams: the check itself, and how a refresh is run.
	spatialGamesAvailFn = spatialGamesAvailability
	spatialGamesAvailGo = func(run func()) { go run() }
)

const (
	// spatialGamesAvailTTL is how long one answer is reused.
	spatialGamesAvailTTL = 2 * time.Second
	// spatialGamesCheckTTL is the shorter wait while RetroArch is being
	// probed, so the row turns from "Checking…" to its answer promptly.
	spatialGamesCheckTTL = 500 * time.Millisecond
)

// spatialGamesChecking reports whether an answer means "still probing":
// spatialGamesAvailability says so with a reason that starts with "Checking".
func spatialGamesChecking(ok bool, why string) bool {
	return !ok && strings.HasPrefix(why, "Checking")
}

// spatialGamesAvailabilityCached returns the last answer and whether there is
// one yet. A missing or stale answer starts one refresh in the background; it
// never runs the check on the caller's thread.
func spatialGamesAvailabilityCached() (ok bool, why string, known bool) {
	now := time.Now()
	spatialGamesAvailMu.Lock()
	a := spatialGamesAvail
	ttl := spatialGamesAvailTTL
	if spatialGamesChecking(a.ok, a.why) {
		ttl = spatialGamesCheckTTL
	}
	refresh := !a.running && (!a.have || now.Sub(a.at) > ttl)
	if refresh {
		spatialGamesAvail.running = true
	}
	spatialGamesAvailMu.Unlock()
	if refresh {
		spatialGamesAvailGo(spatialGamesAvailRefresh)
	}
	return a.ok, a.why, a.have
}

// spatialGamesAvailRefresh runs the check and stores the answer. A panic in the
// check must not take the app down from a goroutine, nor leave the row on
// "Checking…" for ever: the starting answer below is what is stored then.
func spatialGamesAvailRefresh() {
	ok, why := false, "JukaHub could not check spatial audio in games"
	defer func() {
		if r := recover(); r != nil {
			logWarn("spatial", "the games availability check failed: %v", r)
		}
		spatialGamesAvailMu.Lock()
		spatialGamesAvail = spatialGamesAvailState{at: time.Now(), have: true, ok: ok, why: why}
		spatialGamesAvailMu.Unlock()
	}()
	ok, why = spatialGamesAvailFn()
}

// spatialSettingsRow is the Settings → Sound & haptics row: the mode as a
// choice; strength, room and games live in the Audio panel.
func spatialSettingsRow() setRow {
	return setRow{
		ID: "spatialAudio", Label: "Spatial audio", Kind: setKindChoice, Quiet: true,
		Detail: spatialStatusLine,
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(spatialModeNames))
			for _, name := range spatialModeNames {
				out = append(out, setOption{Value: name, Label: spatialModeLabel(name)})
			}
			return out
		},
		Current: func(c *Config) int {
			cur := spatialNormalizeMode(spatialSettingsFor(c).Mode)
			for i, name := range spatialModeNames {
				if name == cur {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			ShowToast(spatialSetMode(c, value), ToastKindInfo)
			PlayNavSound()
		},
	}
}
