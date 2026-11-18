package main

// displaysettings.go — Settings → Display, the screen's own page.
//
// The owner asked (2026-11-17) for one place to set the screen: brightness,
// the LED lights and their brightness, how soon the lock screen comes,
// contrast, saturation and exposure. They were spread over three pages —
// the backlight in Display & theme, the lock-screen time as "Screensaver
// after" in Power & battery, the lights on Stick lights — and the picture rows
// were folded under More and hidden wherever the driver lacked a node. The
// section "screen" now holds them together, up front; the old Display &
// theme page is "Theme & look" and keeps the looks.
//
// Moved rows keep their ids ("brightness", "screensaver", "display_*",
// "textSize", "boldText", "fullscreen"), so search, voice and deep links land
// here. The light rows are the Stick lights page's own rows under new ids (the
// same keys and writers, ledsettings.go), so the two pages are one setting;
// search lists both pages' rows, each under its own label.

import (
	"strconv"
)

// displaySettingsSection is the Display section's id ("display" stays the
// id of Theme & look, for its many old links).
const displaySettingsSection = "screen"

// setJoinRows is the parts of a section's row list in order, as one list.
func setJoinRows(parts ...[]setRow) []setRow {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]setRow, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// lockScreenAfterRow is the idle time before the lock screen — the clock dock
// or the starfield, with the lock-screen picture (screenlock.go) — comes up:
// the screensaver timeout, shared with Quick Settings.
func lockScreenAfterRow() setRow {
	return setRow{
		ID: "screensaver", Label: "Lock screen after", Kind: setKindChoice,
		Detail: setStatic("Idle time before the lock screen shows the clock"),
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(screenTimeoutSteps))
			for _, s := range screenTimeoutSteps {
				out = append(out, setOption{Value: strconv.Itoa(s), Label: screenTimeoutLabel(s)})
			}
			return out
		},
		Current: func(c *Config) int {
			for i, s := range screenTimeoutSteps {
				if s == c.Variables.ScreensaverTimeout {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return screenTimeoutLabel(c.Variables.ScreensaverTimeout) },
		Choose: func(c *Config, value string) {
			if n, err := strconv.Atoi(value); err == nil {
				n = applyScreensaverTimeout(c, n)
				setToast("Lock screen after " + screenTimeoutLabel(n))
			}
		},
	}
}

// displayLEDRows are the lights on the Display page: the Stick lights page's
// switch and brightness under their own ids and the owner's words, a line
// where the device has none, and a door to the colours and modes.
func displayLEDRows() []setRow {
	src := ledSettingsRows()
	pick := func(id string) (setRow, bool) {
		for _, r := range src {
			if r.ID == id {
				return r, true
			}
		}
		return setRow{}, false
	}
	out := make([]setRow, 0, 4)
	if r, ok := pick("lightsMissing"); ok {
		r.ID, r.Label = "displayLedMissing", "LED lights"
		// Only a handheld is expected to have lights; a PC just leaves the
		// rows out.
		r.Visible = func(c *Config) bool { return setHandheld() && !ledSettingsAvailable(c) }
		out = append(out, r)
	}
	if r, ok := pick("ledsync"); ok {
		r.ID, r.Label = "displayLed", "LED lights"
		// The Stick lights page's line points at rows below it; here the
		// mode itself is what matters.
		r.Detail = func(c *Config) string {
			if c == nil || !c.Variables.LEDSync {
				return "Off: the lights keep the firmware's own colour"
			}
			label, _ := ledSettingsMode(ledPolicyFromConfig(c))
			return "Showing: " + label
		}
		out = append(out, r)
	}
	if r, ok := pick("ledBrightness"); ok {
		r.ID, r.Label = "displayLedBrightness", "LED brightness"
		// Shown with the lights off too: picking a level turns them on
		// (ledSettingsApply), so the row is never a dead end.
		r.Visible = ledSettingsAvailable
		r.Detail = func(c *Config) string {
			if c == nil || !c.Variables.LEDSync {
				return "Picking a level turns the lights on"
			}
			return "Off keeps the lights dark while JukaHub runs"
		}
		// Off with the lights already off would switch them on only to keep
		// them dark (ledSettingsApply turns them on): nothing to do.
		choose := r.Choose
		r.Choose = func(c *Config, value string) {
			if c != nil && !c.Variables.LEDSync && value == "0" {
				return
			}
			choose(c, value)
		}
		out = append(out, r)
	}
	out = append(out, setRow{
		ID: "displayLedMore", Label: "More light settings", Kind: setKindAction,
		Detail:  setStatic("Colour, effects, music, battery and screen modes"),
		Visible: ledSettingsAvailable,
		Open: func(c *Config) {
			setOpenSettingRow(c, ledSettingsSection, "ledMode", "")
		},
	})
	return out
}
