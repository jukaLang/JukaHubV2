package main

// uimode_settings.go — Settings for the interface modes (uimode.go).
//
// In Full mode Settings has a "Kid & Kiosk mode" section (one line in
// setBuildSections): the mode itself (choosing Kiosk or Kid opens the parent
// code card, uimode_unlock.go), how many games are marked for kids and where
// to mark them, a link to the daily game limit and break reminder (they stay
// playtime_limits.go's rows in Games & emulators, so there is one place they
// are kept), and how a forgotten code is removed.
//
// While Kiosk or Kid mode is on, Settings is one page (setSections asks
// uiModeSettingsSections): volume and brightness — the same rows Sound and
// Theme & look show, picked from the catalogue by id — Wi-Fi on or off, and
// Leave, which opens the Unlock card.

import (
	"strconv"
)

// uiModeSectionID is the Full-mode section's id; uiModeLockedSectionID the
// locked page's.
const (
	uiModeSectionID       = "uimode"
	uiModeLockedSectionID = "uimodeLocked"
)

// uiModeLockedCache holds the locked page per mode, built from the catalogue
// once: rows hold functions, not values.
var uiModeLockedCache struct {
	kiosk, kid []setSection
}

// uiModeSettingsSection is the Full-mode section (setBuildSections).
func uiModeSettingsSection() setSection {
	return setSection{
		ID: uiModeSectionID, Title: "Kid & Kiosk mode", Glyph: "lock",
		Subtitle: "Hand it over: play only, or kids' games only",
		Rows: []setRow{
			{
				ID: "uiMode", Label: "Interface mode", Kind: setKindChoice,
				Detail: func(*Config) string {
					return "Kiosk: play only, nothing can be changed · Kid: Kiosk with kids' games only"
				},
				Options: func(*Config) []setOption {
					return []setOption{
						{Value: uiModeFull, Label: "Full", Note: "Everything, as always"},
						{Value: uiModeKiosk, Label: "Kiosk", Note: "Games, music and videos; Settings, stores and files locked"},
						{Value: uiModeKid, Label: "Kid", Note: "Kiosk with only the games marked for kids, and no internet apps"},
					}
				},
				Current: func(*Config) int { return int(uiModeLevelOf(uiModeNow())) },
				Choose: func(c *Config, value string) {
					if value == uiModeFull {
						return
					}
					uiModeBeginTurnOn(c, value)
				},
			},
			{
				ID: "uiModeKidGames", Label: "Games for kids", Kind: setKindAction,
				Detail: setStatic("A game's page → More → For kids: this game, or its whole console"),
				Value: func(*Config) string {
					games, consoles := uiModeKidCount()
					return uiModeCountLabel(games, "game") + " · " + uiModeCountLabel(consoles, "console")
				},
				Open: func(*Config) { openGameLibraryPanel() },
			},
			{
				ID: "uiModeTimeLimits", Label: "Time limits", Kind: setKindAction,
				Detail: setStatic("The daily game limit and break reminder, in Games & emulators"),
				Value: func(c *Config) string {
					if n := playtimeDailyLimitMinutes(c); n > 0 {
						return playtimeDurationLabel(n) + " a day"
					}
					return "No daily limit"
				},
				Open: func(c *Config) { setOpenSettingRow(c, "games", "playtimeDailyLimit", "") },
			},
			{
				ID: "uiModeForgot", Label: "Forgot the code?", Kind: setKindInfo,
				Detail: setStatic("Delete ui-mode.json in JukaHub's data folder from a computer, or start JukaHub with JUKAHUB_UI_MODE=full"),
			},
		},
	}
}

// uiModeCountLabel is "1 game", "3 consoles".
func uiModeCountLabel(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// uiModeSettingsSections is setSections' hook: the whole catalogue in Full
// mode, the one small page while locked.
func uiModeSettingsSections(all []setSection) []setSection {
	if !uiModeLocked() {
		return all
	}
	cache := &uiModeLockedCache.kiosk
	if uiModeIsKid() {
		cache = &uiModeLockedCache.kid
	}
	if *cache == nil {
		*cache = []setSection{uiModeLockedSection(all, uiModeNow())}
	}
	return *cache
}

// uiModeLockedSection builds the locked page for mode from the catalogue.
func uiModeLockedSection(all []setSection, mode string) setSection {
	find := func(id string) (setRow, bool) {
		for _, s := range all {
			for _, r := range s.Rows {
				if r.ID == id {
					return r, true
				}
			}
		}
		return setRow{}, false
	}
	var rows []setRow
	for _, id := range []string{"volume", "brightness"} {
		if r, ok := find(id); ok {
			rows = append(rows, r)
		}
	}
	rows = append(rows,
		setRow{
			ID: "uiModeWifi", Label: "Wi-Fi", Kind: setKindToggle, Quiet: true,
			Detail:  setStatic("On or off · networks are chosen in Full mode"),
			Visible: setQSAvailable(qsActionWifi),
			Get:     setQSRead(qsActionWifi),
			Set:     setQSWrite(qsActionWifi),
		},
		setRow{
			ID: "uiModeLeave", Label: "Leave " + uiModeLabel(mode) + " mode", Kind: setKindAction,
			Detail: setStatic("Asks for the parent code"),
			Open:   uiModeOpenUnlock,
		},
	)
	return setSection{
		ID: uiModeLockedSectionID, Title: uiModeLabel(mode) + " mode", Glyph: "lock",
		Subtitle: "Volume, brightness and Wi-Fi",
		Rows:     rows,
	}
}
