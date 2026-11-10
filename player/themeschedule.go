package main

// themeschedule.go — light and dark by the time of day.
//
// Settings → Display & theme → "Light & dark by time of day" switches between
// a day theme and a night theme at two times the user sets (07:00 and 19:00
// unless changed). It replaces the four fixed palettes the switch used to cycle
// through (sunset at dawn, light by day, midnight in the evening, OLED at
// night), which were re-applied every hour and so undid a theme picked by hand
// within the hour, and which could not be chosen.
//
// The rules:
//   - the theme changes at the two switch times (and when the setting, its
//     themes or its times change); in between, a theme picked by hand stands;
//   - a colour theme picked by hand while the schedule is on becomes the theme
//     of the part of the day it was picked in, so tomorrow keeps the choice;
//   - the accessibility palettes (high contrast, the colour-blind ones) and the
//     battery saver's OLED own the theme while they are on;
//   - total conversions are never scheduled: they rearrange Home.
//
// The switch itself is still the "timeOfDayTheme" setting, so a device that had
// the old behaviour on keeps a schedule on after the update.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// The Custom keys: the two themes and the two switch times ("HH:MM").
	themeDayKey        = "themeDay"
	themeNightKey      = "themeNight"
	themeDayStartKey   = "themeDayStart"
	themeNightStartKey = "themeNightStart"

	themeDefaultDay        = "light"
	themeDefaultNight      = "hub" // JukaHub's own glass, the out-of-the-box look
	themeDefaultDayStart   = 7 * 60
	themeDefaultNightStart = 19 * 60

	// themeScheduleStepMinutes is the step of the two time rows.
	themeScheduleStepMinutes = 30
)

// The part of the day the last tick acted on (lastTimeOfDayBucket, widgets.go).
const (
	themePhaseUnknown = -1
	themePhaseNight   = 0
	themePhaseDay     = 1
)

// parseThemeClock reads "HH:MM" (or "H:MM") as minutes after midnight.
func parseThemeClock(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hour, err1 := strconv.Atoi(strings.TrimSpace(h))
	minute, err2 := strconv.Atoi(strings.TrimSpace(m))
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

// themeScheduleMinutes is a stored switch time in minutes after midnight, or
// def when it is missing or not a time of day. A hand-edited config may hold
// minutes as a number, which JSON hands back as a float64.
func themeScheduleMinutes(config *Config, key string, def int) int {
	if config == nil {
		return def
	}
	v, ok := customValue(config.Variables.Custom, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case string:
		if m, ok := parseThemeClock(t); ok {
			return m
		}
	case float64:
		if t >= 0 && t < 24*60 && t == float64(int(t)) {
			return int(t)
		}
	case int:
		if t >= 0 && t < 24*60 {
			return t
		}
	}
	return def
}

// themeScheduleClockValue is the stored form of a switch time, "07:05".
func themeScheduleClockValue(minutes int) string {
	minutes = ((minutes % (24 * 60)) + 24*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// themeScheduleClockLabel is a switch time as the clock prints times.
func themeScheduleClockLabel(config *Config, minutes int) string {
	return clockDigitsHM(config, minutes/60, minutes%60)
}

// themeScheduleIsDay reports whether now falls in the day part: from dayStart
// up to nightStart. A day part that wraps midnight (a night shift's 20:00 to
// 08:00) works the same way, and two equal times mean no switch: day all day.
func themeScheduleIsDay(now time.Time, dayStart, nightStart int) bool {
	m := now.Hour()*60 + now.Minute()
	switch {
	case dayStart == nightStart:
		return true
	case dayStart < nightStart:
		return m >= dayStart && m < nightStart
	default:
		return m >= dayStart || m < nightStart
	}
}

// themePresetIsLight reports whether a preset is a light palette, by the same
// measure the glass uses (glassLightTheme): its background's luminance.
func themePresetIsLight(name string) bool {
	p, ok := themePresets[name]
	if !ok {
		return false
	}
	c, ok := parseColorValue(p.Background)
	if !ok {
		return false
	}
	return wcagLuminance(sdl.Color{R: uint8(c.R), G: uint8(c.G), B: uint8(c.B), A: 255}) > 0.4
}

// themeScheduleSlot is the theme stored under key, or def when it is missing,
// unknown, a total conversion or an accessibility palette (those stay with the
// Accessibility panel, and the rows do not offer them).
func themeScheduleSlot(config *Config, key, def string) string {
	if config == nil {
		return def
	}
	name, _ := customString(config.Variables.Custom, key)
	name = strings.TrimSpace(name)
	if p, ok := themePresets[name]; ok && p.Kind == themeKindColour && !a11yIsA11yPreset(name) {
		return name
	}
	return def
}

// themeScheduleUserPreset is the theme the user has on, looking through the
// battery saver: while the saver shows its OLED, the user's own theme is the
// one it will put back.
func themeScheduleUserPreset(config *Config) string {
	if batterySaverActive && batterySaverSaved.valid {
		return batterySaverSaved.themePreset
	}
	return themePresetName(config)
}

// themeScheduleHandsOff reports whether the theme on screen is not the
// schedule's to replace: an accessibility palette or a total conversion picked
// in the gallery. Switching a conversion's palette at 19:00 would leave its
// rearranged Home in another theme's colours.
func themeScheduleHandsOff(current string) bool {
	if strings.TrimSpace(current) == "" {
		return false // no preset chosen: the built-in palette
	}
	if a11yIsA11yPreset(current) {
		return true
	}
	p, ok := themePresets[current]
	return ok && p.Kind == themeKindConversion
}

// themeScheduleWanted is the theme for now and whether now is the day part.
func themeScheduleWanted(config *Config, now time.Time) (string, bool) {
	day := themeScheduleIsDay(now,
		themeScheduleMinutes(config, themeDayStartKey, themeDefaultDayStart),
		themeScheduleMinutes(config, themeNightStartKey, themeDefaultNightStart))
	if day {
		return themeScheduleSlot(config, themeDayKey, themeDefaultDay), true
	}
	return themeScheduleSlot(config, themeNightKey, themeDefaultNight), false
}

// themeScheduleYields reports whether an accessibility palette owns the theme.
// A switch at 19:00 must not take high contrast from someone who needs it.
func themeScheduleYields() bool {
	return a11y.highContrast || a11yColorblindPreset(a11y.colorblind) != ""
}

// themeScheduleStep is one tick's decision: the preset to apply ("" for none)
// and the part of the day to remember. It only acts when the part of the day
// differs from lastPhase, so a theme picked by hand in between stands.
func themeScheduleStep(config *Config, now time.Time, lastPhase int) (apply string, phase int) {
	if config == nil || !timeOfDayThemeEnabled(config) || themeScheduleYields() ||
		themeScheduleHandsOff(themePresetName(config)) {
		return "", lastPhase
	}
	want, day := themeScheduleWanted(config, now)
	phase = themePhaseNight
	if day {
		phase = themePhaseDay
	}
	if phase == lastPhase || want == themePresetName(config) {
		return "", phase
	}
	return want, phase
}

// themeScheduleNoteManualPick records a colour theme picked by hand while the
// schedule is on as the theme of the part of the day it was picked in, so the
// next switch does not undo it tomorrow (ApplyThemePreset calls it).
func themeScheduleNoteManualPick(config *Config, presetName string) {
	themeScheduleNoteManualPickAt(config, presetName, time.Now())
}

func themeScheduleNoteManualPickAt(config *Config, presetName string, now time.Time) {
	if config == nil || !timeOfDayThemeEnabled(config) {
		return
	}
	if p, ok := themePresets[presetName]; !ok || p.Kind != themeKindColour || a11yIsA11yPreset(presetName) {
		return
	}
	key := themeNightKey
	if _, day := themeScheduleWanted(config, now); day {
		key = themeDayKey
	}
	setStoreString(config, key, presetName)
}

// themeScheduleSeed fills the empty slots when the schedule is turned on: the
// theme on screen keeps the part of the day it suits, and the other part gets
// its default. Slots already set are never touched.
func themeScheduleSeed(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	_, haveDay := customValue(config.Variables.Custom, themeDayKey)
	_, haveNight := customValue(config.Variables.Custom, themeNightKey)
	current := themeScheduleUserPreset(config)
	if p, ok := themePresets[current]; ok && p.Kind == themeKindColour && !a11yIsA11yPreset(current) {
		switch {
		case themePresetIsLight(current) && !haveDay:
			config.Variables.Custom[themeDayKey] = current
			haveDay = true
		case !themePresetIsLight(current) && !haveNight:
			config.Variables.Custom[themeNightKey] = current
			haveNight = true
		}
	}
	if !haveDay {
		config.Variables.Custom[themeDayKey] = themeDefaultDay
	}
	if !haveNight {
		config.Variables.Custom[themeNightKey] = themeDefaultNight
	}
}

// themeScheduleRefresh applies the schedule now, after one of its settings
// changed, instead of at the next switch. The battery saver keeps the theme
// while it is on; the first tick after it lets go applies the schedule.
func themeScheduleRefresh(config *Config) {
	lastTimeOfDayBucket = themePhaseUnknown
	if batterySaverActive {
		return
	}
	applyTimeOfDayTheme(config, time.Now())
}

// ── Settings → Display & theme ─────────────────────────────────────────────

// themeScheduleDetail is the switch's second line: what it does when off, and
// what is on screen and when it changes next when on.
func themeScheduleDetail(c *Config) string {
	dayAt := themeScheduleMinutes(c, themeDayStartKey, themeDefaultDayStart)
	nightAt := themeScheduleMinutes(c, themeNightStartKey, themeDefaultNightStart)
	if !timeOfDayThemeEnabled(c) {
		return "A day theme from " + themeScheduleClockLabel(c, dayAt) +
			", a night theme from " + themeScheduleClockLabel(c, nightAt)
	}
	if themeScheduleYields() {
		return "Paused while an accessibility palette is on"
	}
	if batterySaverActive {
		return "Paused while the battery saver is on"
	}
	if themeScheduleHandsOff(themePresetName(c)) {
		return "Paused while " + GetThemePreset(themePresetName(c)).Name + " is on"
	}
	want, day := themeScheduleWanted(c, time.Now())
	if day {
		return GetThemePreset(want).Name + " now · night theme from " + themeScheduleClockLabel(c, nightAt)
	}
	return GetThemePreset(want).Name + " now · day theme from " + themeScheduleClockLabel(c, dayAt)
}

// themeScheduleToggleRow is the switch.
func themeScheduleToggleRow() setRow {
	return setRow{
		ID: "timeTheme", Label: "Light & dark by time of day", Kind: setKindToggle,
		Detail: themeScheduleDetail,
		Get:    timeOfDayThemeEnabled,
		Set: func(c *Config, on bool) {
			if on {
				themeScheduleSeed(c)
			}
			setStoreBool(c, "timeOfDayTheme", on)
			widgetToggleSideEffect(c, "timeOfDayTheme", on)
		},
	}
}

// themeScheduleThemeOptions are the colour themes a schedule slot can hold:
// the accessibility palettes stay with the Accessibility panel, and the total
// conversions rearrange Home.
func themeScheduleThemeOptions() []setOption {
	names := ListThemePresetsFor(themeKindColour)
	out := make([]setOption, 0, len(names))
	for _, name := range names {
		if a11yIsA11yPreset(name) {
			continue
		}
		p := themePresets[name]
		note := "Dark"
		if themePresetIsLight(name) {
			note = "Light"
		}
		out = append(out, setOption{Value: name, Label: p.Name, Note: note, Swatch: p.Background})
	}
	return out
}

// themeScheduleThemeRow is the day (or night) theme.
func themeScheduleThemeRow(day bool) setRow {
	id, label, key, def := "timeThemeNight", "Night theme", themeNightKey, themeDefaultNight
	if day {
		id, label, key, def = "timeThemeDay", "Day theme", themeDayKey, themeDefaultDay
	}
	options := func(*Config) []setOption { return themeScheduleThemeOptions() }
	return setRow{
		ID: id, Label: label, Kind: setKindChoice,
		Detail: func(c *Config) string {
			if !timeOfDayThemeEnabled(c) {
				return "Turn Light & dark by time of day on to use it"
			}
			return "A lists every theme · picking one by hand sets it too"
		},
		Disabled: func(c *Config) bool { return !timeOfDayThemeEnabled(c) },
		Options:  options,
		Current: func(c *Config) int {
			current := themeScheduleSlot(c, key, def)
			for i, o := range options(c) {
				if o.Value == current {
					return i
				}
			}
			return -1
		},
		Sheet:      options,
		SheetValue: func(c *Config) string { return themeScheduleSlot(c, key, def) },
		SheetTitle: label,
		Value:      func(c *Config) string { return GetThemePreset(themeScheduleSlot(c, key, def)).Name },
		Choose: func(c *Config, value string) {
			setStoreString(c, key, value)
			themeScheduleRefresh(c)
		},
	}
}

// themeScheduleTimeOptions are the switch times, every half hour.
func themeScheduleTimeOptions(c *Config) []setOption {
	out := make([]setOption, 0, 24*60/themeScheduleStepMinutes)
	for m := 0; m < 24*60; m += themeScheduleStepMinutes {
		out = append(out, setOption{Value: themeScheduleClockValue(m), Label: themeScheduleClockLabel(c, m)})
	}
	return out
}

// themeScheduleTimeRow is the time the day (or night) theme starts.
func themeScheduleTimeRow(day bool) setRow {
	id, label, key, def := "timeThemeNightAt", "Night theme from", themeNightStartKey, themeDefaultNightStart
	if day {
		id, label, key, def = "timeThemeDayAt", "Day theme from", themeDayStartKey, themeDefaultDayStart
	}
	stored := func(c *Config) string { return themeScheduleClockValue(themeScheduleMinutes(c, key, def)) }
	options := func(c *Config) []setOption {
		out := themeScheduleTimeOptions(c)
		m := themeScheduleMinutes(c, key, def)
		if m%themeScheduleStepMinutes == 0 {
			return out
		}
		// A hand-edited time between the stops is a stop of its own, so
		// stepping from it moves to its neighbours instead of jumping to an end.
		at := m/themeScheduleStepMinutes + 1
		out = append(out, setOption{})
		copy(out[at+1:], out[at:])
		out[at] = setOption{Value: themeScheduleClockValue(m), Label: themeScheduleClockLabel(c, m)}
		return out
	}
	return setRow{
		ID: id, Label: label, Kind: setKindChoice,
		Detail: func(c *Config) string {
			if !timeOfDayThemeEnabled(c) {
				return "Turn Light & dark by time of day on to use it"
			}
			return "Left and right step half an hour · A lists the times"
		},
		Disabled: func(c *Config) bool { return !timeOfDayThemeEnabled(c) },
		Options:  options,
		Current: func(c *Config) int {
			want := stored(c)
			for i, o := range options(c) {
				if o.Value == want {
					return i
				}
			}
			return -1
		},
		Sheet:      options,
		SheetValue: stored,
		SheetTitle: label,
		Value: func(c *Config) string {
			return themeScheduleClockLabel(c, themeScheduleMinutes(c, key, def))
		},
		Choose: func(c *Config, value string) {
			if _, ok := parseThemeClock(value); !ok {
				return
			}
			setStoreString(c, key, value)
			themeScheduleRefresh(c)
		},
	}
}
