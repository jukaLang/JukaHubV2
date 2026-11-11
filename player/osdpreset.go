package main

// In-game OSD preset manager.
//
// JukaHub cannot draw over an emulator: the emulator owns the framebuffer and
// the hub is blocked until it exits, so a hub-drawn overlay would never appear.
// What the hub *can* do is configure the emulator's own on-screen display
// before a RetroArch launch — the same line-based, backup-once edit the
// save-state switches already use (savestate_resume.go).
//
// Scope is deliberately display-only. RetroArch hotkeys are numeric joypad
// button codes (`input_menu_toggle_btn = "5"`, plus an enable-hotkey modifier),
// and the code for "hold Select and press X" differs between firmwares. Writing
// a guessed code silently breaks a control the user relies on, so this manager
// never writes an input_* key. The presets turn the message font, the menu
// widgets and the stats read-out on or off; everything else in retroarch.cfg is
// left byte-for-byte alone.
//
// The choice is stored in the user config (`osdPreset`) and written into
// retroarch.cfg for each RetroArch launch from JukaHub only: the user's own
// values go back when the game exits (racfgscope.go), so RetroArch started
// from the firmware's menu keeps its own OSD. It used to be written for good,
// even the moment the row was pressed. The original config is kept once as
// `retroarch.cfg.jukahub-backup` by the shared writer.

import (
	"strings"
)

const osdPresetConfigKey = "osdPreset"

// osdPresetLabels maps the stored value to the words on the row. "unmanaged" is
// the default: a build that never chose a preset must not rewrite the user's
// emulator settings.
var osdPresetLabels = map[string]string{
	"unmanaged": "leave as is",
	"quiet":     "quiet",
	"minimal":   "notifications",
	"stats":     "notifications + stats",
}

// osdPresetOrder is the cycle order of the settings row.
var osdPresetOrder = []string{"unmanaged", "quiet", "minimal", "stats"}

// osdPresetValues is the whole surface this manager owns. Every key is a
// boolean display switch that RetroArch has carried for years; none of them is
// an input binding. Ordered so a preset applies deterministically.
var osdPresetValues = map[string][][2]string{
	// Notifications off, menu widget animations off, no stats.
	"quiet": {
		{"video_font_enable", "false"},
		{"menu_enable_widgets", "false"},
		{"fps_show", "false"},
		{"framecount_show", "false"},
		{"memory_show", "false"},
	},
	// The stock notification line, without the animated menu widgets or the
	// stats counters.
	"minimal": {
		{"video_font_enable", "true"},
		{"menu_enable_widgets", "false"},
		{"fps_show", "false"},
		{"framecount_show", "false"},
		{"memory_show", "false"},
	},
	// Notifications and the counters: what a player debugging a slow game wants.
	"stats": {
		{"video_font_enable", "true"},
		{"menu_enable_widgets", "true"},
		{"fps_show", "true"},
		{"framecount_show", "true"},
		{"memory_show", "true"},
	},
}

// osdPresetCandidates is a variable so tests can point at a temporary config
// instead of the developer's own RetroArch installation.
var osdPresetCandidates = retroArchConfigCandidates

func normalizeOSDPreset(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := osdPresetLabels[value]; ok {
		return value
	}
	return "unmanaged"
}

func osdPresetLabel(value string) string {
	if label, ok := osdPresetLabels[normalizeOSDPreset(value)]; ok {
		return label
	}
	return osdPresetLabels["unmanaged"]
}

// osdPresetForConfig reads the stored choice. A missing key means the build
// predates the manager, which must leave the emulator alone.
func osdPresetForConfig(config *Config) string {
	if config == nil {
		return "unmanaged"
	}
	if v, ok := customValue(config.Variables.Custom, osdPresetConfigKey); ok {
		return normalizeOSDPreset(coerceString(v))
	}
	return "unmanaged"
}

func nextOSDPreset(current string) string {
	current = normalizeOSDPreset(current)
	for i, name := range osdPresetOrder {
		if name == current {
			return osdPresetOrder[(i+1)%len(osdPresetOrder)]
		}
	}
	return osdPresetOrder[0]
}

// osdPresetScopeValues is a preset's keys and values as the scoped edit takes
// them (racfgscope.go): only the preset's own display switches, and nothing at
// all for "unmanaged".
func osdPresetScopeValues(preset string) []raScopeValue {
	pairs := osdPresetValues[normalizeOSDPreset(preset)]
	values := make([]raScopeValue, 0, len(pairs))
	for _, pair := range pairs {
		values = append(values, raScopeValue{Key: pair[0], Value: pair[1]})
	}
	return values
}

// applyOSDPresetToFoundConfig no longer writes anything: a preset changes
// RetroArch only for JukaHub's own launches, and the choice row (Settings,
// Smart Widgets) only stores it. It reports whether a RetroArch config is
// there for the next launch to use, and logs the choice.
func applyOSDPresetToFoundConfig(preset string) bool {
	if normalizeOSDPreset(preset) == "unmanaged" {
		return false
	}
	for _, path := range osdPresetCandidates() {
		if regularFile(path) {
			logDebug("osd", "%s preset chosen; it applies to RetroArch games started from JukaHub (%s)", preset, path)
			return true
		}
	}
	return false
}

// prepareOSDPreset is the launch-time entry point. Like save-state resume it
// never blocks a launch: a read-only card or a missing config is logged and the
// game still starts, and the preset holds for this launch only. Standalone
// emulators own their own OSD and are skipped.
func prepareOSDPreset(launcher string) bool {
	if !retroArchLauncher(launcher) {
		return false
	}
	preset := osdPresetSetting()
	values := osdPresetScopeValues(preset)
	if len(values) == 0 {
		return false
	}
	configured := false
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, osdPresetCandidates())) {
		changed, err := raScopeApply(path, values)
		if err != nil {
			logWarn("osd", "cannot apply the %s preset in %s: %v", preset, path, err)
			continue
		}
		configured = true
		if changed {
			Log().Info("in-game OSD preset applied for this launch", "preset", preset, "config", path)
		}
	}
	return configured
}

// osdPresetSetting reads the choice for the launch path. appConfig is the live
// config the app was started with; nil means the manager stays out of the way.
func osdPresetSetting() string {
	return osdPresetForConfig(appConfig)
}

// setOSDPresetChoice stores one choice (and the row's label word) without
// touching the disk, so the row, the tests and a config loader share one path.
// It returns the label the toast shows.
func setOSDPresetChoice(config *Config, preset string) string {
	preset = normalizeOSDPreset(preset)
	if config == nil {
		return osdPresetLabel(preset)
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[osdPresetConfigKey] = preset
	config.Variables.Custom[osdPresetConfigKey+"Label"] = osdPresetLabel(preset)
	return osdPresetLabel(preset)
}

// cycleOSDPreset advances the row and persists the choice with the rest of the
// settings; the next RetroArch launch from JukaHub applies it. It returns the
// new label for the toast and the row.
func cycleOSDPreset(config *Config) string {
	if config == nil {
		return osdPresetLabel("unmanaged")
	}
	label := setOSDPresetChoice(config, nextOSDPreset(osdPresetForConfig(config)))
	syncVariableOverrides(config)
	applyOSDPresetToFoundConfig(osdPresetForConfig(config))
	return label
}

// ensureOSDPresetSetting puts the row on the Smart Widgets page, where the other
// device-behaviour switches live; Settings → General has no free slot left.
// Injected in memory like the rest of that page, so no shipped JSON changes.
func ensureOSDPresetSetting(config *Config) {
	scene := sceneByName(config, smartSettingsScene)
	if scene == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	label := osdPresetLabel(osdPresetForConfig(config))
	config.Variables.Custom[osdPresetConfigKey+"Label"] = label
	const rowText = "In-game OSD · $" + osdPresetConfigKey + "Label"
	for i := range scene.Elements {
		el := &scene.Elements[i]
		if strings.EqualFold(strings.TrimSpace(el.Trigger), "cycle_osd_preset") {
			el.Text = rowText
			return
		}
	}

	// Below the last row on the page, whatever that is: the page is generated
	// and grows as widgets are added, so a fixed Y would collide with them.
	bottom := int32(0)
	for _, el := range scene.Elements {
		_, h := resolveElementDimensions(el, 320, 48)
		if next := el.Y + h; next > bottom {
			bottom = next
		}
	}
	y := bottom + 16
	// screenHeight is zero while the config is still loading and the window does
	// not exist yet; clamping then would drop the row onto the first one.
	if screenHeight > 0 && y+48 > screenHeight-24 {
		if limit := screenHeight - 24 - 48; limit > 40 {
			y = limit
		}
	}
	if y < 40 {
		y = 40
	}
	scene.Elements = append(scene.Elements, Element{
		Type:    "button",
		Text:    rowText,
		Trigger: "cycle_osd_preset",
		X:       40,
		Y:       y,
		Width:   "320",
		Height:  "48",
		Color:   "$labelColor",
		BgColor: "$buttonColor",
	})
}
