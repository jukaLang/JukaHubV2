package main

import (
	"testing"
	"time"
)

func TestSeasonalPaletteFor(t *testing.T) {
	for month, want := range map[time.Month]string{
		time.January: "lavender", time.February: "lavender", time.December: "lavender",
		time.March: "sakura", time.April: "sakura", time.May: "sakura",
		time.June: "sand", time.July: "sand", time.August: "sand",
		time.September: "sunset", time.October: "sunset", time.November: "sunset",
	} {
		if got := seasonalPaletteFor(month); got != want {
			t.Errorf("%s = %q, want %q", month, got, want)
		}
		if _, ok := themePresets[seasonalPaletteFor(month)]; !ok {
			t.Fatalf("%s names no shipped theme", month)
		}
	}
}

func TestSeasonalThemeDoesNotOverrideAChosenDayTheme(t *testing.T) {
	c := &Config{Variables: Variables{Custom: map[string]interface{}{
		"timeOfDayTheme": true,
		"seasonalTheme":  true,
	}}}
	noon := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	got, day := themeScheduleWanted(c, noon)
	if !day || got != "sand" {
		t.Fatalf("summer day = %q, day=%v; want sand", got, day)
	}
	c.Variables.Custom[themeDayKey] = "light"
	got, _ = themeScheduleWanted(c, noon)
	if got != "light" {
		t.Fatalf("a chosen day theme was replaced by the season: %q", got)
	}
	c.Variables.Custom["seasonalTheme"] = false
	delete(c.Variables.Custom, themeDayKey)
	got, _ = themeScheduleWanted(c, noon)
	if got != themeDefaultDay {
		t.Fatalf("seasonal off did not restore the default day theme: %q", got)
	}
}
