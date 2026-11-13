package main

// ledsettings.go — Settings → Stick lights.
//
// The stick LEDs could already do a lot (ledsync.go, ledanim.go), but the only
// way to change their colour was the LED style overlay: a hue that moves 15° per
// press, reached from Quick Settings or from two rows buried at the bottom of
// Sound & haptics. Worse, a colour picked there only showed while the "Stick
// lights" switch was on; with it off, the overlay's live preview handed the
// rings back to the firmware the moment it closed, so the pick seemed lost.
//
// This page gives the lights their own Settings section with every setting
// inline: the switch, what the lights show, a palette of colours, the effect,
// the brightness (with Off for dark sticks) and the mic rows. Each row writes the
// same custom-map keys the overlay and Quick Settings write and saves through
// the overlay's writer (ledPanelPersist), so the three stay one setting. Picking
// a look turns the lights on, because a choice nobody can see reads as broken.

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ledSettingsSection is the section's id in the Settings rail.
const ledSettingsSection = "lights"

// ledSettingsModes are the Mode row's stops, in the order a user reaches for
// them: a colour of their own first. The keys are ledsync.go's policies.
var ledSettingsModes = []struct {
	key, label, note string
}{
	{ledPolicyCustom, "Colour", "The colour and effect you pick below"},
	{ledPolicyAudio, "Music", "Pulse with whatever is playing"},
	{ledPolicyMic, "Microphone", "React to sound around you; nothing is recorded"},
	{ledPolicyBattery, "Battery", "Red when low, green when full"},
	{ledPolicyCharging, "Charging", "Breathe green while charging"},
	{ledPolicyTemperature, "Temperature", "Green when cool, red when hot"},
}

// ledSettingsPalette is the Colour row's swatches. Every one is fully
// saturated with a 255 peak: the TrimUI rings split a colour into hue and level
// (ledAnimSplit), so a dark swatch would only read as a dimmer version of a
// brighter one. Brightness has its own row.
var ledSettingsPalette = []setOption{
	{Value: "#ff0000", Label: "Red", Swatch: "#ff0000"},
	{Value: "#ff5000", Label: "Orange", Swatch: "#ff5000"},
	{Value: "#ffa000", Label: "Amber", Swatch: "#ffa000"},
	{Value: "#ffe000", Label: "Yellow", Swatch: "#ffe000"},
	{Value: "#80ff00", Label: "Lime", Swatch: "#80ff00"},
	{Value: "#00ff00", Label: "Green", Swatch: "#00ff00"},
	{Value: "#00ff80", Label: "Mint", Swatch: "#00ff80"},
	{Value: "#00e0ff", Label: "Cyan", Swatch: "#00e0ff"},
	{Value: "#0080ff", Label: "Sky", Swatch: "#0080ff"},
	{Value: "#0000ff", Label: "Blue", Swatch: "#0000ff"},
	{Value: "#8000ff", Label: "Violet", Swatch: "#8000ff"},
	{Value: "#ff00ff", Label: "Magenta", Swatch: "#ff00ff"},
	{Value: "#ff0060", Label: "Pink", Swatch: "#ff0060"},
	{Value: "#ffffff", Label: "White", Swatch: "#ffffff"},
}

// ledSettingsAvailCache holds whether this device has lights JukaHub can
// drive: every row asks on every refresh, and the answer is a sysfs scan.
// Main thread.
var ledSettingsAvailCache struct {
	at time.Time
	ok bool
}

func ledSettingsAvailable(*Config) bool {
	c := &ledSettingsAvailCache
	if now := time.Now(); c.at.IsZero() || now.Sub(c.at) >= 5*time.Second {
		c.at = now
		c.ok = ledSyncAvailable()
	}
	return c.ok
}

// ledSettingsMissing is the page's one row on a device without usable lights:
// a TrimUI whose driver is there but read-only says so, rather than claiming
// the handheld has no lights at all.
func ledSettingsMissing(*Config) string {
	if _, err := os.Stat(ledAnimDir); err == nil {
		return "The stick-light driver is there, but JukaHub can't write to it"
	}
	return "This device has no stick lights JukaHub can drive"
}

// ledSettingsColorHex is the stored custom colour as "#rrggbb", or "" when
// none (or an unreadable one) is stored.
func ledSettingsColorHex(c *Config) string {
	if c == nil {
		return ""
	}
	raw, ok := customString(c.Variables.Custom, ledKeyColor)
	if !ok {
		return ""
	}
	r, g, b, ok := parseLEDHex(raw)
	if !ok {
		return ""
	}
	return rgbHexLower(r, g, b)
}

func rgbHexLower(r, g, b uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{'#',
		digits[r>>4], digits[r&15],
		digits[g>>4], digits[g&15],
		digits[b>>4], digits[b&15]})
}

// ledSettingsColorOptions is the palette after the theme's accent, so the
// sticks can match the UI with one press.
func ledSettingsColorOptions(*Config) []setOption {
	accent := rgbHexLower(accentColor.R, accentColor.G, accentColor.B)
	out := make([]setOption, 0, len(ledSettingsPalette)+1)
	out = append(out, setOption{Value: accent, Label: "Theme accent", Swatch: accent})
	return append(out, ledSettingsPalette...)
}

// ledSettingsEnsureOn starts the lights for a look the user just picked and
// records it as their choice (not the overlay's temporary preview).
func ledSettingsEnsureOn(c *Config) bool {
	if c == nil {
		return false
	}
	if err := SetLEDSync(true); err != nil {
		ShowToast("Stick lights: "+firstLine(err.Error()), ToastKindWarning)
		return false
	}
	c.Variables.LEDSync = true
	ledPanel.forcedSync = false
	return true
}

// ledSettingsApply publishes the keys to the running loop, makes sure it runs
// and saves.
func ledSettingsApply(c *Config) {
	syncLEDLoopSettings(c)
	ledSettingsEnsureOn(c)
	ledPanelPersist(c)
}

// ledSettingsUseCustom switches to the Colour mode for a colour or effect
// pick, and lifts a brightness of Off, so the pick is what the sticks show.
func ledSettingsUseCustom(c *Config) {
	c.Variables.Custom[ledKeyPolicy] = ledPolicyCustom
	if ledBrightnessFromConfig(c) <= 0 {
		c.Variables.Custom[ledKeyBrightness] = ledBrightnessDefault
	}
}

// ledOptionIndex is the stop whose value is value (case-insensitive), or -1.
func ledOptionIndex(options []setOption, value string) int {
	for i, o := range options {
		if strings.EqualFold(o.Value, value) {
			return i
		}
	}
	return -1
}

func ledSettingsModeOptions(c *Config) []setOption {
	mic := ledMicRowsVisible(c)
	out := make([]setOption, 0, len(ledSettingsModes))
	for _, m := range ledSettingsModes {
		if m.key == ledPolicyMic && !mic {
			continue
		}
		out = append(out, setOption{Value: m.key, Label: m.label, Note: m.note})
	}
	return out
}

// ledSettingsMode is the page's entry for a policy: its own label and note
// ("Colour", not the overlay's "Custom"), so the row's value is always one of
// the choices its sheet lists.
func ledSettingsMode(key string) (label, note string) {
	key = ledPolicyKey(key)
	for _, m := range ledSettingsModes {
		if m.key == key {
			return m.label, m.note
		}
	}
	return ledPolicyLabel(key), ""
}

// ledSettingsOn reports whether the page's look rows apply: the lights exist
// and are on. With the switch off the sticks show the firmware's colour, so
// rows that change JukaHub's look would only seem broken.
func ledSettingsOn(c *Config) bool {
	return c != nil && c.Variables.LEDSync && ledSettingsAvailable(c)
}

// ledSettingsInMode is ledSettingsOn for one mode's own rows.
func ledSettingsInMode(policy string) func(*Config) bool {
	return func(c *Config) bool {
		return ledSettingsOn(c) && ledPolicyFromConfig(c) == policy
	}
}

func ledSettingsBrightnessOptions(*Config) []setOption {
	out := []setOption{{Value: "0", Label: "Off"}}
	return append(out, setLevelOptions(ledBrightnessMin)...)
}

// ledSettingsRows is the Stick lights page: the switch, then what the lights
// show, then only the rows that mode uses (Colour: colour, effect, wheel;
// Microphone: sensitivity), then the brightness every mode shares. With the
// switch off only the switch is left.
func ledSettingsRows() []setRow {
	return []setRow{
		{
			ID: "lightsMissing", Label: "Stick lights", Kind: setKindInfo,
			Detail:  ledSettingsMissing,
			Visible: func(c *Config) bool { return !ledSettingsAvailable(c) },
		},
		{
			ID: "ledsync", Label: "Stick lights", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if c == nil || !c.Variables.LEDSync {
					return "Off: the sticks keep the firmware's own colour"
				}
				_, note := ledSettingsMode(ledPolicyFromConfig(c))
				return note
			},
			Visible: ledSettingsAvailable,
			Get:     func(c *Config) bool { return c.Variables.LEDSync },
			Set: func(c *Config, on bool) {
				if c == nil {
					return
				}
				if on {
					if ledSettingsEnsureOn(c) {
						ledPanelPersist(c)
						setToast("Stick lights: On")
					}
					return
				}
				_ = SetLEDSync(false)
				c.Variables.LEDSync = false
				ledPanel.forcedSync = false
				ledPanelPersist(c)
				setToast("Stick lights: Off")
			},
		},
		{
			ID: "ledMode", Label: "Lights show", Kind: setKindChoice,
			Detail:     setStatic("A colour of your own, or something the lights follow"),
			Visible:    ledSettingsOn,
			Options:    ledSettingsModeOptions,
			Sheet:      ledSettingsModeOptions,
			SheetValue: ledPolicyFromConfig,
			SheetTitle: "Stick lights show",
			Current: func(c *Config) int {
				return ledOptionIndex(ledSettingsModeOptions(c), ledPolicyFromConfig(c))
			},
			Value: func(c *Config) string {
				label, _ := ledSettingsMode(ledPolicyFromConfig(c))
				return label
			},
			Choose: func(c *Config, value string) {
				if c == nil {
					return
				}
				key := ledPolicyKey(value)
				if key == ledPolicyMic {
					// The mic needs its retry state reset too (ledmic.go).
					ledMicSetMode(c, true)
					return
				}
				ledPanelEnsureKeys(c)
				c.Variables.Custom[ledKeyPolicy] = key
				if key == ledPolicyCustom && ledSettingsColorHex(c) == "" {
					// No colour stored yet would light the sticks black: start
					// on the theme's accent, the overlay's default too.
					c.Variables.Custom[ledKeyColor] = rgbHexLower(accentColor.R, accentColor.G, accentColor.B)
				}
				ledSettingsApply(c)
			},
		},
		{
			ID: "ledColor", Label: "Colour", Kind: setKindChoice,
			Detail:     setStatic("The colour of both stick rings"),
			Visible:    ledSettingsInMode(ledPolicyCustom),
			Options:    ledSettingsColorOptions,
			Sheet:      ledSettingsColorOptions,
			SheetValue: ledSettingsColorHex,
			SheetTitle: "Stick light colour",
			Current: func(c *Config) int {
				hex := ledSettingsColorHex(c)
				if hex == "" {
					return -1
				}
				return ledOptionIndex(ledSettingsColorOptions(c), hex)
			},
			Value: func(c *Config) string {
				hex := ledSettingsColorHex(c)
				if hex == "" {
					return "Not set"
				}
				// A swatch shows its name; a wheel colour its hex.
				opts := ledSettingsColorOptions(c)
				if i := ledOptionIndex(opts, hex); i >= 0 {
					return opts[i].Label
				}
				return strings.ToUpper(hex)
			},
			Choose: func(c *Config, value string) {
				if c == nil {
					return
				}
				r, g, b, ok := parseLEDHex(value)
				if !ok {
					return
				}
				ledPanelEnsureKeys(c)
				c.Variables.Custom[ledKeyColor] = rgbHexLower(r, g, b)
				ledSettingsUseCustom(c)
				ledSettingsApply(c)
			},
		},
		{
			ID: "ledEffect", Label: "Effect", Kind: setKindChoice,
			Detail:  setStatic("Steady, a slow smooth glow, a quicker pulse, or every colour in turn"),
			Visible: ledSettingsInMode(ledPolicyCustom),
			Options: func(*Config) []setOption {
				out := make([]setOption, 0, len(ledEffectOrder))
				for _, e := range ledEffectOrder {
					out = append(out, setOption{Value: e.key, Label: e.label})
				}
				return out
			},
			Current: func(c *Config) int { return ledEffectIndex(ledEffectFromConfig(c)) },
			Choose: func(c *Config, value string) {
				if c == nil {
					return
				}
				ledPanelEnsureKeys(c)
				c.Variables.Custom[ledKeyEffect] = ledEffectKey(value)
				ledSettingsUseCustom(c)
				ledSettingsApply(c)
			},
		},
		{
			ID: "ledWheel", Label: "Colour wheel", Kind: setKindAction,
			Detail:  setStatic("Any hue in between, with a live preview"),
			Visible: ledSettingsInMode(ledPolicyCustom),
			Open:    func(*Config) { openLEDPanel() },
		},
		// The microphone mode's own row (ledmic.go); it shows in that mode only.
		ledMicSensitivityRow(),
		{
			ID: "ledBrightness", Label: "Brightness", Kind: setKindChoice, Clamp: true,
			Detail:  setStatic("Off keeps the sticks dark while JukaHub runs"),
			Visible: ledSettingsOn,
			Options: ledSettingsBrightnessOptions,
			Current: func(c *Config) int {
				return setNearestStop(ledSettingsBrightnessOptions(c), ledBrightnessFromConfig(c))
			},
			Choose: func(c *Config, value string) {
				pct, err := strconv.Atoi(value)
				if c == nil || err != nil {
					return
				}
				if pct < 0 {
					pct = 0
				}
				if pct > ledBrightnessMax {
					pct = ledBrightnessMax
				}
				ledPanelEnsureKeys(c)
				c.Variables.Custom[ledKeyBrightness] = pct
				ledSettingsApply(c)
			},
		},
	}
}
