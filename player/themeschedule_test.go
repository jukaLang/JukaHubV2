package main

import (
	"testing"
	"time"
)

func themeScheduleTestConfig(custom map[string]interface{}) *Config {
	c := &Config{}
	c.Variables.Custom = custom
	return c
}

func themeScheduleAt(hour, minute int) time.Time {
	return time.Date(2026, time.November, 9, hour, minute, 0, 0, time.Local)
}

func TestThemeScheduleIsDay(t *testing.T) {
	cases := []struct {
		name              string
		dayStart, nightAt int
		hour, minute      int
		want              bool
	}{
		{"before sunrise", 7 * 60, 19 * 60, 6, 59, false},
		{"at the day switch", 7 * 60, 19 * 60, 7, 0, true},
		{"afternoon", 7 * 60, 19 * 60, 18, 59, true},
		{"at the night switch", 7 * 60, 19 * 60, 19, 0, false},
		{"midnight", 7 * 60, 19 * 60, 0, 0, false},
		// A night shift: the light part of the day wraps midnight.
		{"wrapped, evening", 20 * 60, 8 * 60, 21, 0, true},
		{"wrapped, small hours", 20 * 60, 8 * 60, 3, 0, true},
		{"wrapped, at its night switch", 20 * 60, 8 * 60, 8, 0, false},
		{"wrapped, noon", 20 * 60, 8 * 60, 12, 0, false},
		// The same time for both is not a schedule: it stays light.
		{"no window", 7 * 60, 7 * 60, 23, 0, true},
	}
	for _, c := range cases {
		if got := themeScheduleIsDay(themeScheduleAt(c.hour, c.minute), c.dayStart, c.nightAt); got != c.want {
			t.Errorf("%s: themeScheduleIsDay = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestThemeScheduleTimes(t *testing.T) {
	cases := []struct {
		stored interface{}
		want   int
	}{
		{"07:30", 7*60 + 30},
		{"7:05", 7*60 + 5},
		{"19:00", 19 * 60},
		{" 21:45 ", 21*60 + 45},
		{float64(390), 390}, // minutes, as JSON numbers come back
		{"24:00", themeDefaultDayStart},
		{"7:60", themeDefaultDayStart},
		{"-1:00", themeDefaultDayStart},
		{"noon", themeDefaultDayStart},
		{"", themeDefaultDayStart},
		{float64(-5), themeDefaultDayStart},
		{float64(24 * 60), themeDefaultDayStart},
	}
	for _, c := range cases {
		cfg := themeScheduleTestConfig(map[string]interface{}{themeDayStartKey: c.stored})
		if got := themeScheduleMinutes(cfg, themeDayStartKey, themeDefaultDayStart); got != c.want {
			t.Errorf("stored %#v: %d minutes, want %d", c.stored, got, c.want)
		}
	}
	if got := themeScheduleMinutes(themeScheduleTestConfig(map[string]interface{}{}), themeNightStartKey, themeDefaultNightStart); got != 19*60 {
		t.Fatalf("unset night switch = %d, want the 19:00 default", got)
	}
	if got := themeScheduleClockValue(7*60 + 5); got != "07:05" {
		t.Fatalf("themeScheduleClockValue = %q", got)
	}
}

// The schedule picks the theme of the part of the day it is, falls back to the
// defaults for a missing or unknown preset, and never hands out a total
// conversion (it would rearrange Home twice a day).
func TestThemeScheduleWanted(t *testing.T) {
	cfg := themeScheduleTestConfig(map[string]interface{}{
		"timeOfDayTheme":   "true",
		themeDayKey:        "paper",
		themeNightKey:      "midnight",
		themeDayStartKey:   "07:00",
		themeNightStartKey: "19:00",
	})
	if got, day := themeScheduleWanted(cfg, themeScheduleAt(10, 0)); got != "paper" || !day {
		t.Fatalf("10:00 wants %q (day %v), want paper by day", got, day)
	}
	if got, day := themeScheduleWanted(cfg, themeScheduleAt(22, 0)); got != "midnight" || day {
		t.Fatalf("22:00 wants %q (day %v), want midnight by night", got, day)
	}
	cfg.Variables.Custom[themeDayKey] = "no-such-theme"
	cfg.Variables.Custom[themeNightKey] = "convert-grid"
	if got, _ := themeScheduleWanted(cfg, themeScheduleAt(10, 0)); got != themeDefaultDay {
		t.Fatalf("an unknown day theme gave %q, want the %q default", got, themeDefaultDay)
	}
	if got, _ := themeScheduleWanted(cfg, themeScheduleAt(22, 0)); got != themeDefaultNight {
		t.Fatalf("a total conversion as night theme gave %q, want the %q default", got, themeDefaultNight)
	}
	for _, name := range []string{themeDefaultDay, themeDefaultNight} {
		if p, ok := themePresets[name]; !ok || p.Kind != themeKindColour {
			t.Fatalf("default %q is not a colour preset", name)
		}
	}
	if !themePresetIsLight(themeDefaultDay) || themePresetIsLight(themeDefaultNight) {
		t.Fatal("the day default must be a light palette and the night default a dark one")
	}
}

// The tick acts at the switches only. In between it leaves the theme alone, so
// a theme picked by hand stands until the next switch instead of being undone
// within a second.
func TestThemeScheduleStepSwitchesOnlyAtBoundaries(t *testing.T) {
	cfg := themeScheduleTestConfig(map[string]interface{}{
		"timeOfDayTheme":   "true",
		"theme_preset":     "midnight",
		themeDayKey:        "paper",
		themeNightKey:      "midnight",
		themeDayStartKey:   "07:00",
		themeNightStartKey: "19:00",
	})
	apply, phase := themeScheduleStep(cfg, themeScheduleAt(10, 0), themePhaseUnknown)
	if apply != "paper" || phase != themePhaseDay {
		t.Fatalf("first tick by day: apply %q phase %d, want paper / day", apply, phase)
	}
	cfg.Variables.Custom["theme_preset"] = "paper"
	if apply, phase = themeScheduleStep(cfg, themeScheduleAt(10, 5), phase); apply != "" {
		t.Fatalf("a tick inside the same part of the day applied %q", apply)
	}
	// Picked by hand at noon: kept, not reverted.
	cfg.Variables.Custom["theme_preset"] = "ocean"
	if apply, phase = themeScheduleStep(cfg, themeScheduleAt(12, 0), phase); apply != "" {
		t.Fatalf("a theme picked by hand was reverted to %q", apply)
	}
	if apply, phase = themeScheduleStep(cfg, themeScheduleAt(19, 0), phase); apply != "midnight" || phase != themePhaseNight {
		t.Fatalf("19:00: apply %q phase %d, want midnight / night", apply, phase)
	}
	// Already showing the wanted theme: the phase moves, nothing is applied.
	cfg.Variables.Custom["theme_preset"] = "paper"
	if apply, phase = themeScheduleStep(cfg, themeScheduleAt(7, 0), phase); apply != "" || phase != themePhaseDay {
		t.Fatalf("07:00 with the day theme on screen: apply %q phase %d", apply, phase)
	}
	// Switched off: nothing, whatever the hour.
	cfg.Variables.Custom["timeOfDayTheme"] = "false"
	if apply, _ = themeScheduleStep(cfg, themeScheduleAt(22, 0), themePhaseDay); apply != "" {
		t.Fatalf("the schedule applied %q while off", apply)
	}
}

// Accessibility palettes own the theme while they are on: a switch at 19:00
// must not take high contrast away from someone who needs it.
func TestThemeScheduleYieldsToAccessibilityPalettes(t *testing.T) {
	cfg := themeScheduleTestConfig(map[string]interface{}{
		"timeOfDayTheme": "true",
		"theme_preset":   "highcontrast",
	})
	prev := a11y.highContrast
	a11y.highContrast = true
	t.Cleanup(func() { a11y.highContrast = prev })
	if apply, phase := themeScheduleStep(cfg, themeScheduleAt(22, 0), themePhaseDay); apply != "" || phase != themePhaseDay {
		t.Fatalf("with high contrast on the schedule applied %q (phase %d)", apply, phase)
	}
}

// A palette picked in the gallery that the schedule must not replace — an
// accessibility palette, a total conversion — pauses it, flags or no flags.
func TestThemeScheduleLeavesHandsOffThemesAlone(t *testing.T) {
	for _, current := range []string{"highcontrast", "cb-deuteranopia", "convert-grid"} {
		cfg := themeScheduleTestConfig(map[string]interface{}{
			"timeOfDayTheme": "true",
			"theme_preset":   current,
		})
		if apply, phase := themeScheduleStep(cfg, themeScheduleAt(22, 0), themePhaseDay); apply != "" || phase != themePhaseDay {
			t.Errorf("%s on screen: the schedule applied %q (phase %d)", current, apply, phase)
		}
	}
	// No preset at all is the built-in palette, which the schedule may replace.
	cfg := themeScheduleTestConfig(map[string]interface{}{"timeOfDayTheme": "true"})
	if apply, _ := themeScheduleStep(cfg, themeScheduleAt(22, 0), themePhaseDay); apply != themeDefaultNight {
		t.Fatalf("with no preset chosen the schedule applied %q, want %q", apply, themeDefaultNight)
	}
}

// Turned on while the battery saver shows its OLED, the schedule seeds from
// the theme the saver will put back, not from OLED.
func TestThemeScheduleSeedsThroughTheBatterySaver(t *testing.T) {
	prevActive, prevSaved := batterySaverActive, batterySaverSaved
	t.Cleanup(func() { batterySaverActive, batterySaverSaved = prevActive, prevSaved })
	batterySaverActive = true
	batterySaverSaved = batterySaverSnapshot{valid: true, themePreset: "ocean"}
	cfg := themeScheduleTestConfig(map[string]interface{}{"theme_preset": "oled"})
	themeScheduleSeed(cfg)
	if got, _ := customString(cfg.Variables.Custom, themeNightKey); got != "ocean" {
		t.Fatalf("night seeded %q under the saver, want the user's ocean", got)
	}
}

// A switch time between the half-hour stops is offered as a stop of its own,
// between its neighbours.
func TestThemeScheduleTimeRowKeepsAnOffGridTime(t *testing.T) {
	cfg := themeScheduleTestConfig(map[string]interface{}{"timeOfDayTheme": "true", themeDayStartKey: "07:15"})
	row := themeScheduleTimeRow(true)
	opts := row.Options(cfg)
	cur := row.Current(cfg)
	if cur < 1 || cur >= len(opts)-1 || opts[cur].Value != "07:15" {
		t.Fatalf("current %d of %d options; want 07:15 inside the list", cur, len(opts))
	}
	if opts[cur-1].Value != "07:00" || opts[cur+1].Value != "07:30" {
		t.Fatalf("07:15 sits between %q and %q, want 07:00 and 07:30", opts[cur-1].Value, opts[cur+1].Value)
	}
	if len(opts) != 24*60/themeScheduleStepMinutes+1 {
		t.Fatalf("%d options, want the %d stops plus the stored time", len(opts), 24*60/themeScheduleStepMinutes)
	}
}

// With the schedule on, a theme picked by hand becomes the theme of that part
// of the day; with it off, picking a theme leaves the schedule's slots alone.
func TestThemeScheduleRemembersManualPick(t *testing.T) {
	cfg := themeScheduleTestConfig(map[string]interface{}{
		"timeOfDayTheme":   "true",
		themeDayKey:        "paper",
		themeNightKey:      "midnight",
		themeDayStartKey:   "07:00",
		themeNightStartKey: "19:00",
	})
	themeScheduleNoteManualPickAt(cfg, "ocean", themeScheduleAt(21, 0))
	if got, _ := customString(cfg.Variables.Custom, themeNightKey); got != "ocean" {
		t.Fatalf("night theme after a pick at 21:00 = %q, want ocean", got)
	}
	if got, _ := customString(cfg.Variables.Custom, themeDayKey); got != "paper" {
		t.Fatalf("a night pick changed the day theme to %q", got)
	}
	// Total conversions rearrange Home; they are not a day or a night palette.
	themeScheduleNoteManualPickAt(cfg, "convert-grid", themeScheduleAt(10, 0))
	if got, _ := customString(cfg.Variables.Custom, themeDayKey); got != "paper" {
		t.Fatalf("a total conversion became the day theme (%q)", got)
	}
	cfg.Variables.Custom["timeOfDayTheme"] = "false"
	themeScheduleNoteManualPickAt(cfg, "sand", themeScheduleAt(10, 0))
	if got, _ := customString(cfg.Variables.Custom, themeDayKey); got != "paper" {
		t.Fatalf("with the schedule off a pick still changed the day theme to %q", got)
	}
}

// Turning the schedule on keeps the look the user already has for the part of
// the day it suits: a dark theme becomes the night theme, a light one the day
// theme, and the other slot gets its default.
func TestThemeScheduleSeedsFromCurrentTheme(t *testing.T) {
	cfg := themeScheduleTestConfig(map[string]interface{}{"theme_preset": "ocean"})
	themeScheduleSeed(cfg)
	if got, _ := customString(cfg.Variables.Custom, themeNightKey); got != "ocean" {
		t.Fatalf("a dark current theme seeded night = %q, want ocean", got)
	}
	if got, _ := customString(cfg.Variables.Custom, themeDayKey); got != themeDefaultDay {
		t.Fatalf("day seeded %q, want the %q default", got, themeDefaultDay)
	}

	cfg = themeScheduleTestConfig(map[string]interface{}{"theme_preset": "paper"})
	themeScheduleSeed(cfg)
	if got, _ := customString(cfg.Variables.Custom, themeDayKey); got != "paper" {
		t.Fatalf("a light current theme seeded day = %q, want paper", got)
	}
	if got, _ := customString(cfg.Variables.Custom, themeNightKey); got != themeDefaultNight {
		t.Fatalf("night seeded %q, want the %q default", got, themeDefaultNight)
	}

	// Slots the user already set are never overwritten.
	cfg = themeScheduleTestConfig(map[string]interface{}{"theme_preset": "ocean", themeNightKey: "oled", themeDayKey: "sand"})
	themeScheduleSeed(cfg)
	if got, _ := customString(cfg.Variables.Custom, themeNightKey); got != "oled" {
		t.Fatalf("seeding overwrote the night theme with %q", got)
	}
}
