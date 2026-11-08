package main

// setmodel_test.go — the Settings catalogue itself: every area the hub is meant
// to cover has its section, every row is wired to a real code path for its
// kind, ids are unique (search and deep links address rows by id), and the
// values the carousels store are the ones the features read back.

import (
	"strings"
	"testing"
)

// setTestRow finds a row of a section by id, without drawing anything.
func setTestRow(t *testing.T, sectionID, rowID string) setRow {
	t.Helper()
	i := setSectionIndex(sectionID)
	if i < 0 {
		t.Fatalf("Settings has no %q section", sectionID)
	}
	for _, row := range setSectionRows(nil, setSections()[i]) {
		if row.ID == rowID {
			return row
		}
	}
	t.Fatalf("the %q section has no %q row", sectionID, rowID)
	return setRow{}
}

func TestSettingsSectionsCoverEveryArea(t *testing.T) {
	want := []string{"general", "display", "sound", "network", "keyboard", "controls", "media",
		"games", "power", "privacy", "plugins", "about"}
	sections := setSections()
	if len(sections) != len(want) {
		t.Errorf("%d sections, want %d: %v", len(sections), len(want), want)
	}
	for _, id := range want {
		if setSectionIndex(id) < 0 {
			t.Errorf("no %q section", id)
		}
	}
	seen := map[string]bool{}
	for _, s := range sections {
		if s.ID == "" || strings.TrimSpace(s.Title) == "" || strings.TrimSpace(s.Subtitle) == "" {
			t.Errorf("section %+v needs an id, a title and a line under it", s.ID)
		}
		if seen[s.ID] {
			t.Errorf("section id %q is used twice", s.ID)
		}
		seen[s.ID] = true
		if !iconKnown(s.Glyph) {
			t.Errorf("%s: glyph %q is not one of the shared icons", s.ID, s.Glyph)
		}
		if len(s.Rows) == 0 && s.Dynamic == nil {
			t.Errorf("%s: the section has no rows", s.ID)
		}
	}
	for _, legacy := range []string{"SettingsGeneral", "SettingsAppearance", "SettingsSmart"} {
		if !setIsHubScene(legacy) {
			t.Errorf("%s is not drawn by the hub", legacy)
		}
		if setSectionIndex(setLegacySection(legacy)) < 0 {
			t.Errorf("%s does not open a section", legacy)
		}
	}
	if !setIsHubScene("Settings") || setLegacySection("Settings") != "" {
		t.Error("Settings itself must be the hub and open on the last section used")
	}
}

func TestSettingsRowsAreWired(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	owner := map[string]string{}
	for _, s := range setSections() {
		for _, row := range setSectionRows(config, s) {
			where := s.ID + "/" + row.ID
			if row.ID == "" || strings.TrimSpace(row.Label) == "" {
				t.Errorf("%s: a row needs an id and a label (%+v)", where, row.Label)
			}
			if other, dup := owner[row.ID]; dup {
				t.Errorf("row id %q is in both %s and %s", row.ID, other, s.ID)
			}
			owner[row.ID] = s.ID
			switch row.Kind {
			case setKindToggle:
				if row.Get == nil || row.Set == nil {
					t.Errorf("%s: a switch needs Get and Set", where)
				}
			case setKindChoice:
				if row.Options == nil || row.Current == nil || row.Choose == nil {
					t.Errorf("%s: a carousel needs Options, Current and Choose", where)
				}
				if row.Sheet != nil && row.SheetValue == nil {
					t.Errorf("%s: a list sheet needs to know the stored value", where)
				}
			case setKindAction:
				if row.Open == nil {
					t.Errorf("%s: a link row needs Open", where)
				}
			case setKindText:
				if row.Field == "" {
					t.Errorf("%s: a text row needs the variable it edits", where)
				}
			case setKindInfo:
				if row.Value == nil && row.Detail == nil {
					t.Errorf("%s: a read-out with nothing to read out", where)
				}
			default:
				t.Errorf("%s: unknown row kind %d", where, row.Kind)
			}
		}
	}
	// The rows the old pages could not reach at all are here now.
	for _, id := range []string{"wifi", "companion", "vk", "powerMode", "a11y", "weather", "resolution", "subtitles", "snaps", "cookies"} {
		if _, ok := owner[id]; !ok {
			t.Errorf("no %q row in Settings", id)
		}
	}
}

func TestSettingsResolutionStopsArePlayable(t *testing.T) {
	config := &Config{}
	want := map[string]int{"": 720, "360": 360, "480": 480, "720": 720, "1080": 1080}
	options := setResolutionOptions(config)
	if len(options) != len(want) {
		t.Fatalf("%d resolution stops, want %d", len(options), len(want))
	}
	for i, o := range options {
		h, known := want[o.Value]
		if !known {
			t.Errorf("stop %q is not one the player understands", o.Value)
			continue
		}
		config.Variables.PlaybackResolution = o.Value
		if got := playbackMaxHeight(config); got != h {
			t.Errorf("stop %q plays at %dp, want %dp", o.Label, got, h)
		}
		if got := setResolutionCurrent(config); got != i {
			t.Errorf("stored %q, the carousel shows stop %d, want %d", o.Value, got, i)
		}
	}
	config.Variables.PlaybackResolution = "best"
	if got := setResolutionCurrent(config); got != 0 {
		t.Errorf("an old \"best\" shows stop %d, want Auto", got)
	}
}

// TestSettingsCarouselsStoreWhatTheFeaturesRead drives the rows' own Choose,
// against a config carrying the capitalised mirrors the shipped configs have:
// the choice must still be what the player reads afterwards.
func TestSettingsCarouselsStoreWhatTheFeaturesRead(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"PlaybackResolution": "360", "SubtitleLanguage": "fr"}

	resolution := setTestRow(t, "media", "resolution")
	resolution.Choose(config, "1080")
	if config.Variables.PlaybackResolution != "1080" || playbackMaxHeight(config) != 1080 {
		t.Fatalf("chose 1080p, the player reads %q (%dp)", config.Variables.PlaybackResolution, playbackMaxHeight(config))
	}

	subtitles := setTestRow(t, "media", "subtitles")
	subtitles.Choose(config, "ja")
	if config.Variables.SubtitleLanguage != "ja" {
		t.Fatalf("chose Japanese, the player reads %q", config.Variables.SubtitleLanguage)
	}
	if i := subtitles.Current(config); i < 0 || subtitles.Options(config)[i].Value != "ja" {
		t.Fatalf("after choosing Japanese the carousel shows stop %d", i)
	}
	// A language from the full list is stored too, and named on the row even
	// though the carousel has no stop for it.
	subtitles.Choose(config, "pt-BR")
	if config.Variables.SubtitleLanguage != "pt-BR" {
		t.Fatalf("chose Brazilian Portuguese, the player reads %q", config.Variables.SubtitleLanguage)
	}
	if i := subtitles.Current(config); i != -1 {
		t.Fatalf("a language off the carousel shows as stop %d", i)
	}
	if got := subtitles.Value(config); got != "Português (Brasil)" {
		t.Fatalf("the row names the stored language %q", got)
	}
	found := false
	for _, o := range subtitles.Sheet(config) {
		if o.Value == "pt-BR" {
			found = true
		}
	}
	if !found {
		t.Fatal("the full list has no Brazilian Portuguese to pick")
	}
}

// TestSettingsWeatherRowsFollowTheSwitch: the unit carousel is greyed out, and
// says why, exactly while the forecast is switched off.
func TestSettingsWeatherRowsFollowTheSwitch(t *testing.T) {
	weatherTestSwitch(t)
	withWeatherCacheFile(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	weather := setTestRow(t, "general", "weather")
	unit := setTestRow(t, "general", "weatherUnit")

	if !weather.Get(config) || unit.Disabled(config) {
		t.Fatal("a fresh config must show the forecast on and its unit available")
	}
	weather.Set(config, false)
	if weather.Get(config) || weatherIsEnabled() {
		t.Fatal("the switch did not turn the forecast off")
	}
	if !unit.Disabled(config) || !strings.Contains(unit.Detail(config), "Turn Weather on") {
		t.Fatalf("with the forecast off the unit row reads %q and is not greyed out", unit.Detail(config))
	}
	weather.Set(config, true)
	if !weather.Get(config) || !weatherIsEnabled() || unit.Disabled(config) {
		t.Fatal("the switch did not turn the forecast back on")
	}
}

// TestSettingsVideoSnapsRowIsTheLibrarySwitch: the row reads and writes the key
// the library's preview obeys.
func TestSettingsVideoSnapsRowIsTheLibrarySwitch(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	snaps := setTestRow(t, "games", "snaps")
	if !snaps.Get(config) {
		t.Fatal("video snaps are on by default, but the row shows them off")
	}
	snaps.Set(config, false)
	if snapPreviewEnabled(config) || snaps.Get(config) {
		t.Fatal("the row turned snaps off but the library still plays them")
	}
	snaps.Set(config, true)
	if !snapPreviewEnabled(config) {
		t.Fatal("the row did not turn snaps back on")
	}
}

// TestSettingsInterfaceSoundRows: the switch and the level write the keys
// sound.go reads, apply at once, and a switch turned back on over a silent
// level is audible again.
func TestSettingsInterfaceSoundRows(t *testing.T) {
	prevMuted, prevLevel, prevSet := bootSfxMuted.Load(), bootSfxLevel.Load(), bootSfxLevelSet.Load()
	t.Cleanup(func() {
		bootSfxMuted.Store(prevMuted)
		bootSfxLevel.Store(prevLevel)
		bootSfxLevelSet.Store(prevSet)
	})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	sounds := setTestRow(t, "sound", "uiSounds")
	level := setTestRow(t, "sound", "uiSoundLevel")

	if !sounds.Get(config) || level.Disabled(config) {
		t.Fatal("interface sounds are on by default, with their level available")
	}
	level.Choose(config, "40")
	if bootSfxVolume() != 40 || config.Variables.Custom[bootSfxVolumeKey] != "40" {
		t.Fatalf("chose 40%%: live %d, stored %v", bootSfxVolume(), config.Variables.Custom[bootSfxVolumeKey])
	}
	if i := level.Current(config); i < 0 || level.Options(config)[i].Value != "40" {
		t.Fatalf("after choosing 40%% the carousel shows stop %d", i)
	}
	sounds.Set(config, false)
	if bootSfxEnabled() || sounds.Get(config) || !level.Disabled(config) {
		t.Fatal("the switch did not turn the interface sounds off")
	}
	sounds.Set(config, true)
	if !bootSfxEnabled() || bootSfxVolume() != 40 {
		t.Fatalf("back on: enabled %v at %d%%, want the 40%% chosen before", bootSfxEnabled(), bootSfxVolume())
	}

	config.Variables.Custom[bootSfxVolumeKey] = "0"
	sounds.Set(config, false)
	sounds.Set(config, true)
	if !bootSfxEnabled() || bootSfxVolume() == 0 {
		t.Fatal("turned back on over a silent level, the sounds stay silent")
	}
}

func TestSettingsLevelStopsStartAtTheFloor(t *testing.T) {
	got := setLevelOptions(25)
	if len(got) == 0 || got[0].Value != "30" || got[len(got)-1].Value != "100" {
		t.Fatalf("levels from a 25%% floor run %v, want 30%%..100%% in tens", got)
	}
	if all := setLevelOptions(0); len(all) != 11 || all[0].Label != "0%" {
		t.Fatalf("levels from 0 = %v, want eleven stops from 0%%", all)
	}
}
