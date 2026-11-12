package main

// setmodel.go — what Settings contains: its sections, every row, and what each
// row reads and changes.
//
// The old Settings was four config pages of absolutely placed inputs and
// buttons. Each feature that wanted a switch injected one more button into
// whichever corner of General was still free (snaps, cookies, the screensaver,
// vibration, the OSD, the tour), which is how rows ended up cut off on the
// 960-wide canvas, and why half the device's settings (Quick Settings' power
// mode, the battery saver, accessibility, the keyboard, Wi-Fi) could not be
// found from Settings at all.
//
// Here every setting is one declarative row in one section. A row does not own
// its logic: it calls the code path the feature already has (Quick Settings'
// toggles and levels, the accessibility store, the haptics, cookie and snap
// setters, the Settings triggers), so there is still exactly one place where
// each value becomes live state and is saved. Every change applies at once and
// goes through the shared debounced user-config writer; there is no Save
// button to forget.

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// setRowKind is how a row is drawn and what A does on it.
type setRowKind int

const (
	setKindToggle setRowKind = iota // a switch; A flips it, Left/Right set off/on
	setKindChoice                   // a carousel; Left/Right step, A steps or opens a list
	setKindAction                   // opens a screen or a panel
	setKindText                     // a text value; A opens the keyboard
	setKindInfo                     // a read-out
)

// setOption is one stop of a choice row.
type setOption struct {
	Value string
	Label string
	// Note is the second line the list sheet shows under Label.
	Note string
	// Swatch is a #rrggbb chip drawn before the label (the colour rows).
	Swatch string
}

// setRow is one setting. Only the fields its Kind uses are set.
type setRow struct {
	ID    string
	Label string
	Kind  setRowKind
	// Detail is the line under the label: what the setting does, or why it is
	// unavailable.
	Detail func(c *Config) string
	// Visible hides a row that means nothing here (no motor, a desktop-only
	// switch on the handheld). nil is always visible.
	Visible func(c *Config) bool
	// Disabled greys a row out; Detail says why. nil is enabled.
	Disabled func(c *Config) bool

	// Toggle.
	Get func(c *Config) bool
	Set func(c *Config, on bool)

	// Choice: the stops, the index of the current one (-1 when the stored value
	// is not a stop) and how a stop is applied. Sheet, when set, makes A open a
	// list of these options instead of stepping (every subtitle language), with
	// SheetValue's answer marked as the stored one.
	Options    func(c *Config) []setOption
	Current    func(c *Config) int
	Choose     func(c *Config, value string)
	Sheet      func(c *Config) []setOption
	SheetValue func(c *Config) string
	SheetTitle string
	// Clamp makes a choice stop at its ends instead of wrapping: a level
	// (volume, brightness, a delay) must not jump from 100% to 0% on one more
	// Right, or from the dimmest backlight to full on one more Left.
	Clamp      bool

	// Quiet rows give their own feedback (the Quick Settings paths play their
	// sound), so the hub does not play a second one.
	Quiet bool

	// Value is the right-hand read-out of an action, text or info row, and the
	// label a choice row shows when its value is not one of the stops.
	Value func(c *Config) string
	// Open is an action row's press.
	Open func(c *Config)
	// Field is the input variable a text row edits with the keyboard; a choice
	// row with a Field opens the keyboard on A (a custom colour).
	Field string
	// Secret masks a text row's value (API keys).
	Secret bool
}

// setSection is one entry of the rail and the page of rows it opens.
type setSection struct {
	ID       string
	Title    string
	Subtitle string
	Glyph    string
	Rows     []setRow
	// Dynamic builds the rows from live data (the installed plugins). It is
	// called when the section is refreshed, not every frame.
	Dynamic func(c *Config) []setRow
}

// setSectionsCache is built once: rows hold functions, not values, so the
// catalogue itself never changes.
var setSectionsCache []setSection

// setSections is the Settings catalogue in rail order.
func setSections() []setSection {
	if setSectionsCache == nil {
		setSectionsCache = setBuildSections()
	}
	return setSectionsCache
}

// setSectionIndex finds a section by id (-1 when unknown).
func setSectionIndex(id string) int {
	for i, s := range setSections() {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// setSectionRows is a section's rows as they stand: the static list, or the
// dynamic one built from live data.
func setSectionRows(c *Config, s setSection) []setRow {
	if s.Dynamic != nil {
		return s.Dynamic(c)
	}
	return s.Rows
}

// setLegacySection maps the old Settings pages onto the section that replaced
// them, so every place that still opens SettingsGeneral or SettingsAppearance
// (search, achievements, old configs) lands on the right rows.
func setLegacySection(scene string) string {
	switch scene {
	case "SettingsGeneral":
		return "general"
	case "SettingsAppearance":
		return "display"
	case "SettingsSmart":
		return "power"
	}
	return ""
}

// ── Small helpers ─────────────────────────────────────────────────────────

func setStatic(text string) func(*Config) string {
	return func(*Config) string { return text }
}

func setOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// setToast is the confirmation a change shows.
func setToast(msg string) { ShowToast(msg, ToastKindInfo) }

// setOnOff is the word a toast uses for a switch.
func setOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// setStoreString writes a setting the way the old Settings fields did: under
// its exact key, so it wins over the capitalised mirror the shipped configs
// carry ("PlaybackResolution"), then through the shared override-and-save path
// that also refreshes the typed field.
func setStoreString(c *Config, key, value string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[key] = value
	syncVariableOverrides(c)
}

// setStoreBool is setStoreString for a switch, in the "true"/"false" form the
// toggle element writes.
func setStoreBool(c *Config, key string, on bool) {
	setStoreString(c, key, boolString(on))
}

// setOpenScene returns an action that opens a config scene by name.
func setOpenScene(name string) func(*Config) {
	return func(c *Config) {
		if c == nil {
			return
		}
		idx := sceneIndexExact(c, name)
		if idx < 0 {
			setToast("That screen is not part of this setup")
			return
		}
		if idx != currentSceneIndex {
			changeSceneTo(c, idx)
		}
	}
}

// setHandheld reports whether this is the handheld build's platform.
func setHandheld() bool { return P().IsHandheld() }

// ── Quick Settings rows ───────────────────────────────────────────────────

// setQSToggle finds a Quick Settings switch by what it does.
func setQSToggle(action qsAction) (int, bool) {
	for i := range qs.toggles {
		if qs.toggles[i].Action == action {
			return i, true
		}
	}
	return -1, false
}

// setQSAvailable shows a row only where its Quick Settings backend exists.
func setQSAvailable(action qsAction) func(*Config) bool {
	return func(*Config) bool {
		i, ok := setQSToggle(action)
		return ok && qs.toggles[i].Available
	}
}

// setQSRead is the switch's state as Quick Settings last probed it (radios).
func setQSRead(action qsAction) func(*Config) bool {
	return func(*Config) bool {
		i, ok := setQSToggle(action)
		return ok && qs.toggles[i].Value
	}
}

// setQSWrite flips a Quick Settings switch to on through the panel's own path,
// which applies it, explains a switch the power mode manages, and saves. The
// panel flips relative to what it last knew, so it is primed with the opposite
// of the wanted state first.
func setQSWrite(action qsAction) func(*Config, bool) {
	return func(_ *Config, on bool) {
		i, ok := setQSToggle(action)
		if !ok {
			return
		}
		qs.toggles[i].Value = !on
		qsToggleRow(i)
	}
}

// setLevelOptions are the stops of a level row, from lowest to 100%.
func setLevelOptions(lowest int) []setOption {
	out := make([]setOption, 0, 11)
	for pct := 0; pct <= 100; pct += 10 {
		if pct < lowest {
			continue
		}
		out = append(out, setOption{Value: strconv.Itoa(pct), Label: strconv.Itoa(pct) + "%"})
	}
	return out
}

// setNearestStop is the index of the level stop closest to want (percent), or
// -1 when there are no stops.
func setNearestStop(options []setOption, want int) int {
	best, bestD := -1, 1000
	for i, o := range options {
		v, err := strconv.Atoi(o.Value)
		if err != nil {
			continue
		}
		d := v - want
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// setSliderRow builds a level row over one Quick Settings slider (0 volume,
// 1 brightness), so the device is set through the panel's own writer.
func setSliderRow(id, label, detail string, idx int) setRow {
	lowest := func() int {
		if idx < 0 || idx >= len(qs.sliders) {
			return 0
		}
		lo := int(qs.sliders[idx].Min*100 + 0.5)
		return ((lo + 9) / 10) * 10
	}
	current := func() int {
		if idx < 0 || idx >= len(qs.sliders) {
			return 0
		}
		return int(qs.sliders[idx].Value*100 + 0.5)
	}
	return setRow{
		ID: id, Label: label, Kind: setKindChoice, Quiet: true, Clamp: true,
		Detail: setStatic(detail),
		Visible: func(*Config) bool {
			return idx >= 0 && idx < len(qs.sliders) && qs.sliders[idx].Available
		},
		Options: func(*Config) []setOption { return setLevelOptions(lowest()) },
		Current: func(*Config) int { return setNearestStop(setLevelOptions(lowest()), current()) },
		Choose: func(_ *Config, value string) {
			target, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			if delta := target - current(); delta != 0 {
				qsStepSlider(idx, delta)
			}
		},
	}
}

// ── Interface sounds ──────────────────────────────────────────────────────

// setUISoundsOn is the interface-sounds switch as stored: on unless the user
// turned it off (sound.go reads the same key once a second).
func setUISoundsOn(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, bootSfxEnabledKey); ok {
			return bootSfxParseBool(v, true)
		}
	}
	return true
}

// setUISoundLevel is the interface-sound volume as stored, in percent.
func setUISoundLevel(c *Config) int {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, bootSfxVolumeKey); ok {
			if n, ok := bootSfxParsePercent(v); ok {
				return n
			}
		}
	}
	return 100
}

// ── Weather, video and language rows ──────────────────────────────────────

func setWeatherUnitOptions(*Config) []setOption {
	auto := "C"
	if weatherCountryUsesFahrenheit(weatherCountryCode()) {
		auto = "F"
	}
	return []setOption{
		{Value: "", Label: "Auto · °" + auto},
		{Value: "C", Label: "Celsius · °C"},
		{Value: "F", Label: "Fahrenheit · °F"},
	}
}

func setWeatherUnitCurrent(c *Config) int {
	switch strings.ToUpper(strings.TrimSpace(c.Variables.WeatherUnit)) {
	case "C":
		return 1
	case "F":
		return 2
	}
	return 0
}

// setResolutionOptions are the ceilings playbackMaxHeight understands; empty is
// Auto, which it reads as 720p.
func setResolutionOptions(*Config) []setOption {
	return []setOption{
		{Value: "", Label: "Auto · 720p"},
		{Value: "360", Label: "360p"},
		{Value: "480", Label: "480p"},
		{Value: "720", Label: "720p"},
		{Value: "1080", Label: "1080p"},
	}
}

func setResolutionCurrent(c *Config) int {
	value := strings.TrimSpace(c.Variables.PlaybackResolution)
	if strings.EqualFold(value, "best") {
		value = ""
	}
	for i, o := range setResolutionOptions(c) {
		if o.Value == value {
			return i
		}
	}
	return -1
}

func setSubtitleCurrent(c *Config) int {
	code := strings.TrimSpace(c.Variables.SubtitleLanguage)
	for i, o := range setSubtitleQuickOptions() {
		if strings.EqualFold(o.Value, code) {
			return i
		}
	}
	return -1
}

// ── Appearance rows ───────────────────────────────────────────────────────

// setColourPalette is the curated swatches each colour row steps through,
// after "Theme colour". They suit the dark glass and keep their label legible.
var setColourPalette = map[string][]setOption{
	"buttonColor": {
		{Value: "#2E4061", Label: "Slate", Swatch: "#2E4061"},
		{Value: "#1F4E79", Label: "Ocean", Swatch: "#1F4E79"},
		{Value: "#0F5C5C", Label: "Teal", Swatch: "#0F5C5C"},
		{Value: "#1E5631", Label: "Forest", Swatch: "#1E5631"},
		{Value: "#4B2A6B", Label: "Plum", Swatch: "#4B2A6B"},
		{Value: "#7A2E4A", Label: "Rose", Swatch: "#7A2E4A"},
		{Value: "#8A3B12", Label: "Ember", Swatch: "#8A3B12"},
		{Value: "#333842", Label: "Graphite", Swatch: "#333842"},
	},
	"labelColor": {
		{Value: "#F8FAFC", Label: "Snow", Swatch: "#F8FAFC"},
		{Value: "#CBD5E1", Label: "Mist", Swatch: "#CBD5E1"},
		{Value: "#BAE6FD", Label: "Sky", Swatch: "#BAE6FD"},
		{Value: "#BBF7D0", Label: "Mint", Swatch: "#BBF7D0"},
		{Value: "#FEF08A", Label: "Lemon", Swatch: "#FEF08A"},
		{Value: "#FED7AA", Label: "Peach", Swatch: "#FED7AA"},
		{Value: "#FECDD3", Label: "Blush", Swatch: "#FECDD3"},
		{Value: "#0F172A", Label: "Ink", Swatch: "#0F172A"},
	},
	"inputColor": {
		{Value: "#1C2839", Label: "Deep", Swatch: "#1C2839"},
		{Value: "#0F172A", Label: "Night", Swatch: "#0F172A"},
		{Value: "#1E293B", Label: "Slate", Swatch: "#1E293B"},
		{Value: "#0B3B3B", Label: "Lagoon", Swatch: "#0B3B3B"},
		{Value: "#2A1838", Label: "Plum", Swatch: "#2A1838"},
		{Value: "#E8EAEF", Label: "Paper", Swatch: "#E8EAEF"},
	},
}

// setColourRow is a theme colour as a swatch carousel: the theme's own colour
// first, then the curated swatches; A types any #rrggbb with the keyboard.
func setColourRow(name, label, detail string) setRow {
	options := func(c *Config) []setOption {
		theme := rgbHex(themePresetColor(c, name))
		out := make([]setOption, 0, len(setColourPalette[name])+1)
		out = append(out, setOption{Value: "", Label: "Theme colour", Swatch: theme})
		return append(out, setColourPalette[name]...)
	}
	return setRow{
		ID: name, Label: label, Kind: setKindChoice, Field: name,
		Detail:  setStatic(detail),
		Options: options,
		Current: func(c *Config) int {
			hex := rgbHex(effectiveThemeColor(c, name))
			if strings.EqualFold(hex, rgbHex(themePresetColor(c, name))) {
				return 0
			}
			for i, o := range options(c) {
				if i > 0 && strings.EqualFold(o.Value, hex) {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string {
			return "Custom · " + rgbHex(effectiveThemeColor(c, name))
		},
		Choose: func(c *Config, value string) {
			if value == "" {
				value = rgbHex(themePresetColor(c, name))
			}
			applyColorVar(c, name, value)
			saveUserConfigDebounced(userConfigFromVariables(c))
		},
	}
}

// setTextSizeOptions are the accessibility panel's text scale steps.
func setTextSizeOptions(*Config) []setOption {
	out := make([]setOption, 0, len(a11yFontScaleSteps))
	for _, step := range a11yFontScaleSteps {
		label := strconv.Itoa(int(step*100+0.5)) + "%"
		if step > 0.99 && step < 1.01 {
			label += " · default"
		}
		out = append(out, setOption{Value: strconv.FormatFloat(step, 'f', 2, 64), Label: label})
	}
	return out
}

// setPowerModeRow is Power & battery's first row and Accessibility's Power
// mode row: one setting, answered by the same helper both pages use.
func setPowerModeRow() setRow {
	return setRow{
		ID: "powerMode", Label: "Power mode", Kind: setKindChoice,
		Detail: setStatic("Battery Saver slows the interface to stretch a charge"),
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(powerModeOrder))
			for _, m := range powerModeOrder {
				out = append(out, setOption{Value: m, Label: powerModeLabel(m)})
			}
			return out
		},
		Current: func(c *Config) int { return powerModeIndex(currentPowerMode(c)) },
		Choose: func(c *Config, value string) {
			label := cyclePowerModeTo(c, value)
			if len(qs.cycles) > qsCycleRowPowerMode {
				qs.cycles[qsCycleRowPowerMode].Value = qsPowerModeRowIndex()
			}
			setToast("Power mode: " + label)
		},
	}
}

// setVibrationLevelRow is Sound & haptics' strength row, shared with
// Accessibility: feedback strength reads as one value wherever it is set.
func setVibrationLevelRow() setRow {
	return setRow{
		ID: "vibrationLevel", Label: "Vibration strength", Kind: setKindChoice,
		Detail:   setStatic("How hard the motor pulses"),
		Visible:  func(*Config) bool { return hapticsAvailable() },
		Disabled: func(c *Config) bool { return !hapticsConfiguredOn(c) },
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(hapticLevels))
			for _, level := range hapticLevels {
				out = append(out, setOption{Value: level.key, Label: level.label})
			}
			return out
		},
		Current: func(*Config) int { return hapticsLevelIndex(hapticsLevelNow().key) },
		Choose: func(c *Config, value string) {
			hapticsSetLevel(c, hapticsLevelIndex(value))
			saveUserConfigDebounced(userConfigFromVariables(c))
			playHaptic(hapticActivatePulse)
		},
	}
}

// setHomeLayoutOptions are the stops of Display & theme → Home layout, in the
// order Home's edit strip lists them, each with the line the list sheet shows.
func setHomeLayoutOptions(*Config) []setOption {
	out := make([]setOption, 0, len(homeArrangementChoiceList))
	for _, name := range homeArrangementChoiceList {
		note := "Your config's dashboard"
		switch name {
		case homeArrangementGrid:
			note = "The same cards as equal tiles"
		case homeArrangementCarousel:
			note = "Big cards in a row"
		case homeArrangementWidgets:
			note = "Move, resize, add and remove tiles"
		}
		out = append(out, setOption{Value: name, Label: homeArrangementChoiceLabel(name), Note: note})
	}
	return out
}

// setHomeLayoutValue is the stop the list sheet marks: the stored arrangement,
// with the Dashboard reading as Standard.
func setHomeLayoutValue(c *Config) string {
	if i := homeArrangementChoiceIndex(homeArrangement(c)); i >= 0 && i < len(homeArrangementChoiceList) {
		return homeArrangementChoiceList[i]
	}
	return homeArrangementCustom
}

// setHighContrast switches the accessibility palette through the panel's own
// helpers. Turning it off with no earlier theme to return to clears the
// preset, so the switch never leaves the black-and-yellow palette behind.
func setHighContrast(c *Config, on bool) {
	a11y.highContrast = on
	a11yApplyPalette(c)
	if !on && themePresetName(c) == "highcontrast" {
		clearThemePreset(c)
	}
	a11ySave(c)
	setToast("High contrast: " + setOnOff(on))
}

// setReduceTransparency flips the shared glass token. The token itself is read
// every frame, so no screen needs to be told to repaint.
func setReduceTransparency(c *Config, on bool) {
	a11y.reduceTransparency = on
	a11ySave(c)
	setToast("Reduce transparency: " + setOnOff(on))
}

// setIncreaseContrast flips the shared contrast tokens and resizes the focus
// ring. a11yApplyVision also runs at launch and at the panel's Reset row.
func setIncreaseContrast(c *Config, on bool) {
	a11y.increaseContrast = on
	a11yApplyVision()
	a11ySave(c)
	setToast("Increase contrast: " + setOnOff(on))
}

// setBoldText swaps the shared UI face and drops every font-dependent cache.
func setBoldText(c *Config, on bool) {
	a11y.boldText = on
	a11yRefreshFonts()
	a11ySave(c)
	setToast("Bold text: " + setOnOff(on))
}

// setThemeName is the active theme's display name.
func setThemeName(c *Config) string {
	name := themePresetName(c)
	if name == "" {
		return "Hub · default"
	}
	return GetThemePreset(name).Name
}

// setWallpaperName is the wallpaper's file name, or None for the backdrop (and
// in screenshot mode, which draws no wallpaper).
func setWallpaperName(c *Config) string {
	path := c.Variables.BackgroundImage
	if appWallpaperIsShippedDefault(path) || shotModeOn() {
		return "None"
	}
	return filepath.Base(path)
}

// ── The catalogue ─────────────────────────────────────────────────────────

func setBuildSections() []setSection {
	return []setSection{
		{
			ID: "general", Title: "General", Glyph: "settings",
			Subtitle: "You, your Groq key, the weather and everyday basics",
			Rows: []setRow{
				{
					ID: "name", Label: "Your name", Kind: setKindText, Field: "tspUsername",
					Detail: setStatic("Home greets you with it · Chat signs with it"),
					Value: func(c *Config) string {
						return setOr(userName(c), "Not set")
					},
				},
				{
					// The interface language (i18n.go); each stop is named in
					// its own language so it can be found from any of them.
					ID: "uiLanguage", Label: "Language", Kind: setKindChoice,
					Detail:     setStatic("Menus, hints and messages · A lists them all"),
					Options:    uiLanguageOptions,
					Current:    uiLanguageCurrent,
					Choose:     uiLanguageChoose,
					Sheet:      uiLanguageOptions,
					SheetValue: func(*Config) string { return uiLanguageCode() },
					SheetTitle: "Language",
				},
				// The key Juka AI, voice typing and voice commands need, with
				// a sheet that takes it from the phone or the keyboard
				// (groqkey_sheet.go). The stored key is never shown.
				groqKeyRow(),
				{
					ID: "weather", Label: "Weather", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if weatherEnabledFor(c) {
							return "Forecast in the header, on Home and on the dock"
						}
						return "Off: nothing is fetched and the forecast is hidden"
					},
					Get: weatherEnabledFor,
					Set: func(c *Config, on bool) {
						weatherSetEnabled(c, on)
						setToast("Weather: " + setOnOff(on))
					},
				},
				{
					ID: "weatherUnit", Label: "Temperature unit", Kind: setKindChoice,
					Detail: func(c *Config) string {
						if !weatherEnabledFor(c) {
							return "Turn Weather on to use it"
						}
						return "Auto follows the country your connection is in"
					},
					Disabled: func(c *Config) bool { return !weatherEnabledFor(c) },
					Options:  setWeatherUnitOptions,
					Current:  setWeatherUnitCurrent,
					Choose: func(c *Config, value string) {
						weatherSetUnit(c, value)
						setToast("Temperature unit: " + weatherUnitSettingLabel(c))
					},
				},
				{
					ID: "autostart", Label: "Start JukaHub at login", Kind: setKindToggle,
					Detail: func(*Config) string {
						if setHandheld() {
							return "Open JukaHub when the device starts"
						}
						return "Adds JukaHub to your startup folder"
					},
					Get: autoStartEnabled,
					Set: func(c *Config, on bool) {
						setStoreBool(c, "autoStart", on)
						widgetToggleSideEffect(c, "autoStart", on)
					},
				},
				{
					// The full boot at power-on, on every start, or never
					// (bootos.go).
					ID: "startupAnimation", Label: "Startup animation", Kind: setKindChoice,
					Detail:  bootOSAnimationDetail,
					Options: bootOSAnimationOptions,
					Current: bootOSAnimationCurrent,
					Choose:  bootOSAnimationChoose,
				},
				{
					// The firmware's power-on picture (bootlogo.go): A twice
					// installs JukaHub's, A twice again puts the previous one back.
					ID: "bootLogo", Label: "Boot logo", Kind: setKindAction,
					Detail:  bootLogoRowDetail,
					Visible: bootLogoRowVisible,
					Value:   bootLogoRowValue,
					Open:    bootLogoRowOpen,
				},
				{
					ID: "bootPreview", Label: "Watch the boot animation", Kind: setKindAction,
					Detail: setStatic("The full boot JukaHub plays when the device starts"),
					Open:   bootOSPreviewOpen,
				},
				{
					ID: "fileRoot", Label: "Files start folder", Kind: setKindText, Field: "fileExplorerRoot",
					Detail: setStatic("Where Files opens; empty uses the card"),
					Value: func(c *Config) string {
						return setOr(strings.TrimSpace(c.Variables.FileExplorerRoot), "Default")
					},
				},
			},
		},
		{
			// Network time, the zone, 12/24 h and the date format
			// (datetime.go). The 12-hour switch lived under Display & theme.
			ID: "datetime", Title: "Date & time", Glyph: "clock",
			Subtitle: "The clock, your time zone and how dates read",
			Rows:     dateTimeSettingsRows(),
		},
		{
			ID: "display", Title: "Display & theme", Glyph: "palette",
			Subtitle: "Themes, colours, text size and motion",
			Rows: []setRow{
				{
					ID: "themes", Label: "Theme presets", Kind: setKindAction,
					Detail: setStatic("Colour palettes and total conversions"),
					Value:  setThemeName,
					Open:   setOpenScene("ThemePresets"),
				},
				{
					// A whole setup in one switch: theme, Home, sounds, start
					// screen and more, with Undo (xpack.go).
					ID: "xpack", Label: "Experience pack", Kind: setKindAction,
					Detail: setStatic("Theme, Home layout, sounds and start screen in one switch"),
					Value:  func(*Config) string { return xpackQSLabel() },
					Open:   xpackOpenGallery,
				},
				{
					// Which Home: the shipped dashboard, the Hero grid, or one of
					// the two that read the homeTiles list — a carousel and the
					// widgets you arrange (home_edit.go). A opens the list with a
					// line on each; Left/Right step through them.
					ID: "homeLayout", Label: "Home layout", Kind: setKindChoice,
					Detail:     setStatic("Standard, Hero grid, a carousel or widgets you arrange"),
					Options:    setHomeLayoutOptions,
					Current:    func(c *Config) int { return homeArrangementChoiceIndex(homeArrangement(c)) },
					Sheet:      setHomeLayoutOptions,
					SheetValue: setHomeLayoutValue,
					Choose: func(c *Config, value string) {
						homeSetArrangement(c, value)
						setToast("Home layout: " + homeArrangementChoiceLabel(value))
					},
				},
				{
					// Home's edit mode from here (home_edit.go): it opens Home
					// first, switching to Widgets when Home is not a tile layout.
					ID: "homeEdit", Label: "Edit Home", Kind: setKindAction,
					Detail: func(*Config) string {
						if hintsUseKeyboard() {
							return "Move, resize, add and remove Home tiles — Q on Home"
						}
						return "Move, resize, add and remove Home tiles — L2 on Home"
					},
					Open: homeEditOpen,
				},
				{
					ID: "community", Label: "Community themes", Kind: setKindAction,
					Detail: setStatic("Browse and install themes from the gallery"),
					Open:   openThemeCommunityPanel,
				},
				{
					ID: "wallpaper", Label: "Wallpaper", Kind: setKindAction,
					Detail: setStatic("A picture under the glass on every screen"),
					Value:  setWallpaperName,
					Open: func(*Config) {
						wpOpen = true
						initWallpaperPicker()
					},
				},
				// A GIF wallpaper plays unless this is off (gifwall.go).
				wallpaperAnimateRow(),
				setSliderRow("brightness", "Brightness", "The screen's backlight", 1),
				{
					ID: "textSize", Label: "Text size", Kind: setKindChoice, Clamp: true,
					Detail:  setStatic("Every label in JukaHub, Home included"),
					Options: setTextSizeOptions,
					Current: func(*Config) int { return a11yScaleIndex() },
					// The Accessibility page's setter (a11y_settings.go): it
					// confirms the size with a toast, like every other row
					// here, and skips the save when nothing changed.
					Choose: func(c *Config, value string) {
						f, err := strconv.ParseFloat(value, 64)
						if err != nil {
							return
						}
						if a11ySetScale(f) {
							a11ySave(c)
						}
					},
				},
				{
					ID: "contrast", Label: "High contrast", Kind: setKindToggle,
					Detail: setStatic("Black and white with a yellow focus ring"),
					Get:    func(*Config) bool { return a11y.highContrast },
					Set:    setHighContrast,
				},
				{
					ID: "reduceTransparency", Label: "Reduce transparency", Kind: setKindToggle,
					Detail: setStatic("Opaque glass; the wallpaper cannot show through"),
					Get:    func(*Config) bool { return a11y.reduceTransparency },
					Set:    setReduceTransparency,
				},
				{
					ID: "increaseContrast", Label: "Increase contrast", Kind: setKindToggle,
					Detail: setStatic("Stronger edges, wider focus rings, 7:1 text"),
					Get:    func(*Config) bool { return a11y.increaseContrast },
					Set:    setIncreaseContrast,
				},
				{
					ID: "boldText", Label: "Bold text", Kind: setKindToggle,
					Detail: setStatic("Inter SemiBold in every menu, list and Home label"),
					Get:    func(*Config) bool { return a11y.boldText },
					Set:    setBoldText,
				},
				{
					ID: "motion", Label: "Reduce motion", Kind: setKindToggle, Quiet: true,
					Detail: setStatic("Calmer transitions; easier on the battery"),
					Get:    effectiveReducedMotion,
					Set:    setQSWrite(qsActionReducedMotion),
				},
				{
					ID: "livebg", Label: "Living background", Kind: setKindToggle,
					Detail: setStatic("Colours drift with the time of day and the weather"),
					Get:    func(c *Config) bool { return c.Variables.DynamicBg },
					Set: func(c *Config, on bool) {
						setStoreBool(c, "dynamicBg", on)
						setToast("Living background: " + setOnOff(on))
					},
				},
				// The water under the glass, and tilt where the device can feel
				// it (liquidbg.go).
				liquidLevelRow(),
				liquidTiltRow(),
				// Light & dark by time of day: the switch, the two themes and the
				// two times they start (themeschedule.go).
				themeScheduleToggleRow(),
				themeScheduleThemeRow(true),
				themeScheduleThemeRow(false),
				themeScheduleTimeRow(true),
				themeScheduleTimeRow(false),
				// The 12-hour clock moved to Date & time (datetime.go).
				setColourRow("buttonColor", "Button colour", "A tint under every button · A types a colour"),
				setColourRow("labelColor", "Label colour", "Text on buttons and labels · A types a colour"),
				setColourRow("inputColor", "Input colour", "The wash under text fields · A types a colour"),
				{
					ID: "fullscreen", Label: "Full screen", Kind: setKindToggle, Quiet: true,
					Detail:  setStatic("Fill the monitor instead of a window"),
					Visible: func(*Config) bool { return !setHandheld() },
					Get:     func(c *Config) bool { return c.Variables.Fullscreen },
					Set:     setQSWrite(qsActionFullscreen),
				},
				{
					ID: "a11y", Label: "More accessibility", Kind: setKindAction,
					Detail: setStatic("Button remapping, one-handed mode, colour filters"),
					Open:   openAccessibilityPanel,
				},
			},
		},
		{
			// Every accessibility setting as an ordinary page, so the modal is
			// no longer the only way in (a11y_settings.go).
			ID: a11ySettingsSection, Title: "Accessibility", Glyph: "user",
			Subtitle: "Text, contrast, audio and controls, on every screen",
			Rows:     a11ySettingsRows(),
		},
		{
			ID: "sound", Title: "Sound & haptics", Glyph: "music",
			Subtitle: "Volume, interface sounds and vibration",
			Rows: []setRow{
				setSliderRow("volume", "Volume", "The device's speaker and headphone level", 0),
				// Virtual surround for what JukaHub plays (spatial_ui.go).
				spatialSettingsRow(),
				{
					// The keys and the setter are sound.go's; the switch plays
					// its own click once the sounds are back on, so the hub
					// stays quiet.
					ID: "uiSounds", Label: "Interface sounds", Kind: setKindToggle, Quiet: true,
					Detail: setStatic("Soft clicks as you move and press"),
					Get:    setUISoundsOn,
					Set: func(c *Config, on bool) {
						pct := setUISoundLevel(c)
						if on && pct == 0 {
							pct = 100
						}
						bootSfxSetPrefs(c, on, pct)
						saveUserConfigDebounced(userConfigFromVariables(c))
						if on {
							PlayToggleSound()
						}
					},
				},
				{
					ID: "uiSoundLevel", Label: "Interface sound level", Kind: setKindChoice, Quiet: true,
					Detail: func(c *Config) string {
						if !setUISoundsOn(c) {
							return "Turn Interface sounds on to use it"
						}
						return "How loud the clicks are; media keeps its own volume"
					},
					Disabled: func(c *Config) bool { return !setUISoundsOn(c) },
					Options:  func(*Config) []setOption { return setLevelOptions(10) },
					Current:  func(c *Config) int { return setNearestStop(setLevelOptions(10), setUISoundLevel(c)) },
					Choose: func(c *Config, value string) {
						pct, err := strconv.Atoi(value)
						if err != nil {
							return
						}
						bootSfxSetPrefs(c, setUISoundsOn(c), pct)
						saveUserConfigDebounced(userConfigFromVariables(c))
						// A click at the new level, so the step can be heard.
						PlayNavSound()
					},
				},
				{
					ID: "startupSound", Label: "Startup & shutdown sound", Kind: setKindToggle, Quiet: true,
					Detail: func(c *Config) string {
						if !setUISoundsOn(c) {
							return "Turn Interface sounds on to use it"
						}
						if quietModeOn() || quietModeFromConfig(c) {
							return "Quiet Mode is on: no chimes until it is off"
						}
						return "A chime when JukaHub starts and closes"
					},
					// bootChimeOn also answers false under Quiet Mode, so the
					// row is greyed then too: a live switch reading Off that
					// cannot be turned on (every press stored "on" and still
					// showed Off) looked broken.
					Disabled: func(c *Config) bool {
						return !setUISoundsOn(c) || quietModeOn() || quietModeFromConfig(c)
					},
					Get: bootChimeOn,
					Set: func(c *Config, on bool) {
						setStoreBool(c, bootChimeEnabledKey, on)
						saveUserConfigDebounced(userConfigFromVariables(c))
						if on {
							bootChimePreview(c)
						}
					},
				},
				// Music under the menus (bgmusic.go): the switch, which music,
				// how loud, and a skip for the shuffled library.
				{
					ID: "bgMusic", Label: "Background music", Kind: setKindToggle,
					Detail: bgmStatus,
					Get:    bgmEnabledIn,
					Set: func(c *Config, on bool) {
						bgmSetPrefs(c, on, bgmSourceIn(c), bgmVolumeIn(c))
					},
				},
				{
					ID: "bgMusicSource", Label: "Music", Kind: setKindChoice,
					Detail: func(c *Config) string {
						if !bgmEnabledIn(c) {
							return "Turn Background music on to use it"
						}
						return "Built-in songs, your Synth song or your own music"
					},
					Disabled:   func(c *Config) bool { return !bgmEnabledIn(c) },
					Options:    bgmSourceOptions,
					Current:    bgmSourceIndex,
					Choose:     func(c *Config, value string) { bgmSetPrefs(c, true, value, bgmVolumeIn(c)) },
					Sheet:      bgmSourceOptions,
					SheetValue: bgmSourceIn,
					SheetTitle: "Background music",
				},
				{
					ID: "bgMusicVolume", Label: "Music level", Kind: setKindChoice, Clamp: true,
					Detail: func(c *Config) string {
						if !bgmEnabledIn(c) {
							return "Turn Background music on to use it"
						}
						return "Kept low under the menus; media keeps its own volume"
					},
					Disabled: func(c *Config) bool { return !bgmEnabledIn(c) },
					Options:  func(*Config) []setOption { return setLevelOptions(10) },
					Current:  func(c *Config) int { return setNearestStop(setLevelOptions(10), bgmVolumeIn(c)) },
					Choose: func(c *Config, value string) {
						if pct, err := strconv.Atoi(value); err == nil {
							bgmSetPrefs(c, true, bgmSourceIn(c), pct)
						}
					},
				},
				{
					ID: "bgMusicSkip", Label: "Next song", Kind: setKindAction,
					Detail:  setStatic("Skip to another song from your library"),
					Visible: func(c *Config) bool { return bgmEnabledIn(c) && bgmSourceIn(c) == bgmSrcLibrary },
					Open: func(*Config) {
						bgmSkip()
						setToast("Next song")
					},
				},
				{
					ID: "vibration", Label: "Vibration", Kind: setKindToggle,
					Detail:  setStatic("A short buzz when you navigate and press"),
					Visible: func(*Config) bool { return hapticsAvailable() },
					Get:     hapticsConfiguredOn,
					Set: func(c *Config, on bool) {
						setStoreBool(c, hapticsVariable, on)
						hapticsSetEnabled(on)
						if on {
							playHaptic(hapticTogglePulse)
						}
					},
				},
				setVibrationLevelRow(),
				{
					ID: "ledsync", Label: "Lights follow the music", Kind: setKindToggle, Quiet: true,
					Detail:  setStatic("The stick lights pulse with what is playing"),
					Visible: setQSAvailable(qsActionLedSync),
					Get:     func(c *Config) bool { return c.Variables.LEDSync },
					Set:     setQSWrite(qsActionLedSync),
				},
				{
					ID: "lights", Label: "Light style", Kind: setKindAction,
					Detail:  setStatic("Colour, effect and brightness of the lights"),
					Visible: func(*Config) bool { return ledSyncAvailable() },
					Open:    func(*Config) { openLEDPanel() },
				},
				{
					ID: "audio", Label: "Audio output & equalizer", Kind: setKindAction,
					Detail: setStatic("Output device, EQ presets and loudness"),
					Open:   openMusicSettingsPanel,
				},
			},
		},
		{
			ID: "network", Title: "Network", Glyph: "wifi",
			Subtitle: "Wi-Fi, VPN, radios and your phone",
			Rows: []setRow{
				{
					ID: "wifi", Label: "Wi-Fi", Kind: setKindAction,
					Detail: setStatic("Networks, passwords and signal"),
					Open:   wifiScrOpen,
				},
				// The WireGuard VPN (vpn.go): the switch, the configuration,
				// what goes through it, and how the tunnel is doing.
				vpnToggleRow(),
				vpnProfileRow(),
				vpnRouteRow(),
				vpnStatusRow(),
				{
					ID: "bluetooth", Label: "Bluetooth", Kind: setKindToggle, Quiet: true,
					Detail:  setStatic("Headphones and controllers"),
					Visible: setQSAvailable(qsActionBluetooth),
					Get:     setQSRead(qsActionBluetooth),
					Set:     setQSWrite(qsActionBluetooth),
				},
				{
					// Pairing, connecting and forgetting (bluetooth_devices_ui.go).
					ID: "btdevices", Label: "Bluetooth devices", Kind: setKindAction,
					Detail:  setStatic("Pair controllers for games and Remote Play, and headphones"),
					Visible: func(*Config) bool { return btDevicesAvailable() },
					Value:   btSettingsValue,
					Open:    btScreenOpen,
				},
				{
					ID: "airplane", Label: "Airplane mode", Kind: setKindToggle, Quiet: true,
					Detail:  setStatic("Turns every radio off; back on as you left them"),
					Visible: setQSAvailable(qsActionAirplane),
					Get:     setQSRead(qsActionAirplane),
					Set:     setQSWrite(qsActionAirplane),
				},
				{
					ID: "lanplay", Label: "LAN play", Kind: setKindToggle, Quiet: true,
					Detail: setStatic("Offer local multiplayer in the game library"),
					Get:    netplayEnabled,
					Set:    setQSWrite(qsActionNetplay),
				},
				{
					ID: "companion", Label: "Phone companion", Kind: setKindAction,
					Detail: setStatic("Type, send files and control JukaHub from your phone"),
					Open:   func(*Config) { openCompanionOverlay() },
				},
				{
					ID: "nettools", Label: "Servers & network tools", Kind: setKindAction,
					Detail: setStatic("FTP and SSH servers, speed test and lookups"),
					Open:   netToolsOpen,
				},
				// The web builder's receiver (livebuilder.go): off on the
				// handheld until switched on, off again after 30 idle minutes.
				// The pairing code is shown here and nowhere else.
				{
					ID: "liveBuilder", Label: "Live Builder", Kind: setKindToggle,
					Detail: liveBuilderSettingDetail,
					Get:    liveBuilderSettingOn,
					Set:    liveBuilderSettingSet,
				},
				{
					ID: "liveBuilderNewCode", Label: "New pairing code", Kind: setKindAction,
					Detail:  setStatic("The current code stops working; type the new one into the builder"),
					Visible: liveBuilderSettingOn,
					Open:    liveBuilderNewCode,
				},
				{
					ID: "liveBuilderPending", Label: "Design waiting for approval", Kind: setKindAction,
					Detail:  liveBuilderPendingDetail,
					Visible: liveBuilderPendingVisible,
					Open:    liveBuilderShowApproval,
				},
				// Your other JukaHubs on this Wi-Fi (peer.go): pairing, save
				// sync, handoff and Nearby. Everything is off until switched on.
				{
					ID: "peerDevices", Label: "Nearby devices", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if peerSettingEnabled(c) {
							return "Visible on this Wi-Fi as " + peerDeviceName() + " · pair, sync saves, continue games"
						}
						return "Off: this device is invisible on the network and listens for nothing"
					},
					Get: peerSettingEnabled,
					Set: peerSettingSetEnabled,
				},
				{
					ID: "peerName", Label: "Device name", Kind: setKindAction,
					Detail: setStatic("How your other JukaHubs see this one"),
					Value:  func(*Config) string { return peerDeviceName() },
					Open:   peerOpenRenamePrompt,
				},
				{
					ID: "peerPaired", Label: "Paired devices", Kind: setKindAction,
					Detail: setStatic("Pair with a code shown on both screens, or forget a device"),
					Value:  peerPairedSummary,
					Open:   peerDevicesOpen,
				},
				{
					ID: "nearbyShare", Label: "Share what I'm playing", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if !peerSettingEnabled(c) {
							return "Turn Nearby devices on to use it"
						}
						return "Others on this Wi-Fi see your game and can check which of theirs you have — not your list"
					},
					Disabled: func(c *Config) bool { return !peerSettingEnabled(c) },
					Get:      nearbySettingShare,
					Set:      nearbySettingSetShare,
				},
				{
					// Greyed with Nearby devices off, so the line says why, as
					// Share what I'm playing does.
					ID: "hoffOffers", Label: "Continue from other devices", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if !peerSettingEnabled(c) {
							return "Turn Nearby devices on to use it"
						}
						return "Home offers to pick up what you were doing on a paired device"
					},
					Disabled: func(c *Config) bool { return !peerSettingEnabled(c) },
					Get:      hoffSettingOffers,
					Set:      hoffSettingSetOffers,
				},
				{
					ID: "hoffSettings", Label: "Bring my settings along", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if !peerSettingEnabled(c) {
							return "Turn Nearby devices on to use it"
						}
						return "Theme, text size and clock travel with a handoff when both devices allow it"
					},
					Disabled: func(c *Config) bool { return !peerSettingEnabled(c) },
					Get:      hoffSettingShareSettings,
					Set:      hoffSettingSetShareSettings,
				},
			},
		},
		{
			ID: "keyboard", Title: "Keyboard", Glyph: "keyboard",
			Subtitle: "The on-screen keyboard",
			Rows: []setRow{
				{
					ID: "vk", Label: "On-screen keyboard", Kind: setKindAction,
					Detail: setStatic("Languages, word suggestions and voice typing"),
					Open:   vkOpenSettings,
				},
				{
					ID: "slowRepeat", Label: "Slower key repeat", Kind: setKindToggle,
					Detail: setStatic("A held direction moves across the keys more calmly"),
					Get:    func(*Config) bool { return a11y.slowRepeat },
					Set: func(c *Config, on bool) {
						a11y.slowRepeat = on
						a11ySave(c)
						setToast("Slower key repeat: " + setOnOff(on))
					},
				},
			},
		},
		{
			// Juka's voice, voice shortcuts, the game assistant and voice
			// typing (voiceai_settings.go).
			ID: voiceAISettingsSection, Title: "Voice & AI", Glyph: "mic",
			Subtitle: "Juka's voice, voice shortcuts and the game assistant",
			Rows:     voiceAISettingsRows(),
		},
		{
			// Stick calibration, control profiles, D-pad modes and Local play
			// (inmap_settings.go).
			ID: "controls", Title: "Controls", Glyph: "gamepad",
			Subtitle: "Stick calibration, profiles and D-pad modes",
			Rows:     inmapSettingsRows(),
		},
		{
			ID: "media", Title: "Media & playback", Glyph: "video",
			Subtitle: "Video quality, subtitles and downloads",
			Rows: []setRow{
				{
					ID: "resolution", Label: "Playback resolution", Kind: setKindChoice,
					Detail:  setStatic("The highest quality a stream starts at"),
					Options: setResolutionOptions,
					Current: setResolutionCurrent,
					Choose: func(c *Config, value string) {
						setStoreString(c, "playbackResolution", value)
					},
				},
				{
					ID: "subtitles", Label: "Subtitle language", Kind: setKindChoice,
					Detail:     setStatic("Used when CC is on · A lists them all"),
					Options:    func(*Config) []setOption { return setSubtitleQuickOptions() },
					Current:    setSubtitleCurrent,
					Sheet:      func(*Config) []setOption { return setSubtitleAllOptions() },
					SheetValue: func(c *Config) string { return strings.TrimSpace(c.Variables.SubtitleLanguage) },
					SheetTitle: "Subtitle language",
					Value: func(c *Config) string {
						return setSubtitleLabel(c.Variables.SubtitleLanguage)
					},
					Choose: func(c *Config, value string) {
						setStoreString(c, "subtitleLanguage", value)
					},
				},
				{
					ID: "downloads", Label: "Downloads", Kind: setKindAction,
					Detail: setStatic("Videos and music saved for offline"),
					Open:   mediaOpenDownloads,
				},
				{
					ID: "keepWatched", Label: "Keep fully watched videos", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if mediaKeepWatchedEnabled(c) {
							return "A video downloaded while you watch stays in Downloads"
						}
						return "Off: a watched download is deleted when it ends"
					},
					Get: mediaKeepWatchedEnabled,
					Set: func(c *Config, on bool) {
						mediaSetKeepWatched(c, on)
						setToast("Keep fully watched videos: " + setOnOff(on))
					},
				},
				{
					ID: "decoder", Label: "Audio and video decoding", Kind: setKindInfo,
					Detail: setStatic("Built into JukaHub; nothing to install"),
					Value:  setStatic("ffmpeg"),
				},
			},
		},
		{
			ID: "games", Title: "Games & emulators", Glyph: "rom",
			Subtitle: "Buttons, previews and emulator care",
			Rows: []setRow{
				{
					// Accessibility's button layout swaps on top of this switch
					// (a11ySwapButton), so with both on A and B are back where
					// they were and this row alone seemed not to work; the
					// line says so.
					ID: "swapAB", Label: "Swap A and B", Kind: setKindToggle,
					Detail: func(*Config) string {
						if a11y.swapAB {
							return "Accessibility's button layout swaps A and B too; both on cancel out"
						}
						return "For pads that put confirm on the other button"
					},
					Get: abButtonsSwapped,
					Set: func(c *Config, on bool) {
						setStoreBool(c, "swapABButtons", on)
						if on {
							setToast("A and B swapped")
						} else {
							setToast("A and B are back in their usual places")
						}
					},
				},
				{
					// The TrimUI firmware mirrors X and Y the same way it mirrors A
					// and B, so the switch starts on there (xyButtonsSwapped);
					// elsewhere it is for a pad with Nintendo-style labels.
					ID: "swapXY", Label: "Swap X and Y", Kind: setKindToggle,
					Detail: func(*Config) string {
						if a11y.swapXY {
							return "Accessibility's button layout swaps X and Y too; both on cancel out"
						}
						if P().IsHandheld() {
							return "On by default here: the firmware reports X and Y mirrored"
						}
						return "For pads that print X and Y the other way round"
					},
					Get: xyButtonsSwapped,
					Set: func(c *Config, on bool) {
						setStoreBool(c, "swapXYButtons", on)
						if on {
							setToast("X and Y swapped")
						} else {
							setToast("X and Y are back in their usual places")
						}
					},
				},
				{
					ID: "snaps", Label: "Video snaps", Kind: setKindToggle,
					Detail: func(*Config) string {
						with, _, known := snapClipCount()
						switch {
						case !known:
							return "A clip of the focused game · counting clips…"
						case with == 0:
							return "A clip of the focused game · none on the card yet"
						case with == 1:
							return "A clip of the focused game · 1 game has one"
						}
						return "A clip of the focused game · " + strconv.Itoa(with) + " games have one"
					},
					Get: snapPreviewEnabled,
					Set: func(c *Config, on bool) {
						applySnapPreviewEnabled(c, on)
						setToast("Video snaps: " + setOnOff(on))
					},
				},
				{
					ID: "osd", Label: "In-game overlay", Kind: setKindChoice,
					Detail: setStatic("RetroArch's on-screen notifications and counters"),
					Options: func(*Config) []setOption {
						out := make([]setOption, 0, len(osdPresetOrder))
						for _, p := range osdPresetOrder {
							label := osdPresetLabel(p)
							if label != "" {
								label = strings.ToUpper(label[:1]) + label[1:]
							}
							out = append(out, setOption{Value: p, Label: label})
						}
						return out
					},
					Current: func(c *Config) int {
						cur := osdPresetForConfig(c)
						for i, p := range osdPresetOrder {
							if p == cur {
								return i
							}
						}
						return -1
					},
					Choose: func(c *Config, value string) {
						label := setOSDPresetChoice(c, value)
						syncVariableOverrides(c)
						applyOSDPresetToFoundConfig(osdPresetForConfig(c))
						setToast("In-game overlay: " + label)
					},
				},
				{
					// RetroArch's picture for JukaHub launches; a game can
					// override it in its options sheet (videofilter.go).
					ID: "videoFilter", Label: "Video filter", Kind: setKindChoice,
					Detail:  videoFilterSettingDetail,
					Options: videoFilterSettingOptions,
					Current: videoFilterSettingIndex,
					Choose:  func(c *Config, value string) { setToast(videoFilterSettingChoose(c, value)) },
				},
				{
					ID: "coreUpdate", Label: "Emulator updates", Kind: setKindChoice,
					Detail:  coreUpdateModeDetail,
					Visible: coreUpdateModeVisible,
					Options: coreUpdateModeOptions,
					Current: coreUpdateModeIndex,
					Choose:  func(c *Config, value string) { setToast(coreUpdateModeChoose(c, value)) },
				},
				{
					// PS Remote Play's helpers, fetched in the background
					// while they are missing (psremote_helpers_bg.go).
					ID: "remotePlayAuto", Label: "Download Remote Play automatically", Kind: setKindToggle,
					Visible: func(*Config) bool { return psRemoteSupported() },
					Detail:  psBgSettingDetail,
					Get:     psBgEnabled,
					Set: func(c *Config, on bool) {
						psBgSetEnabled(c, on)
						setToast("Download Remote Play automatically: " + setOnOff(on))
					},
				},
				{
					ID: "cpuBoost", Label: "Full speed while playing", Kind: setKindToggle,
					Detail:  setStatic("Lets games use the fastest CPU clock, then backs off"),
					Visible: func(*Config) bool { return setHandheld() },
					Get:     func(c *Config) bool { return loadPowerProfileConfig(c).Enabled },
					Set: func(c *Config, on bool) {
						setStoreBool(c, "powerProfiles", on)
						setToast("Full speed while playing: " + setOnOff(on))
					},
				},
				{
					ID: "emulators", Label: "Emulators", Kind: setKindAction,
					Detail: setStatic("Install, update and check emulators and cores"),
					Open:   func(*Config) { openEmulatorPanel() },
				},
				{
					ID: "library", Label: "Game library", Kind: setKindAction,
					Detail: setStatic("Every game on the card, by system"),
					Open:   func(*Config) { openGameLibraryPanel() },
				},
				{
					ID: "romManager", Label: "ROM manager", Kind: setKindAction,
					Detail: setStatic("File loose games, BIOS files and duplicates"),
					Open:   rommOpen,
				},
				{
					ID: "gameInfo", Label: "Look up game details", Kind: setKindToggle,
					Detail: setStatic("Genre, year and players from the libretro database, when online"),
					Get:    gmetaAutoLookup,
					Set: func(c *Config, on bool) {
						gmetaSetAutoLookup(c, on)
						setToast("Game details lookup: " + setOnOff(on))
					},
				},
				{
					// The background box-art passes (boxart_auto.go).
					ID: "artAuto", Label: "Find box art automatically", Kind: setKindToggle,
					Detail: func(c *Config) string {
						if boxArtAutoEnabled(c) {
							return "Covers for new games from the libretro thumbnails, when online"
						}
						return "Off: covers download only from the library's Fill gaps"
					},
					Get: boxArtAutoEnabled,
					Set: func(c *Config, on bool) {
						boxArtSetAutoEnabled(c, on)
						setToast("Find box art automatically: " + setOnOff(on))
					},
				},
				playtimeDailyLimitRow(),
				playtimeBreakReminderRow(),
				{
					ID: "saves", Label: "Save manager", Kind: setKindAction,
					Detail: setStatic("Back up and restore save files"),
					Open:   func(*Config) { openSaveManagerPanel() },
				},
			},
		},
		{
			ID: "power", Title: "Power & battery", Glyph: "battery",
			Subtitle: "Battery life, sleep and the idle screen",
			Rows: []setRow{
				setPowerModeRow(),
				{
					// The level and how long it will last (battery_eta.go);
					// the detail says what the estimate rests on. Second, so
					// the page still opens on Power mode, where Left/Right
					// change the mode as before.
					ID: "batteryNow", Label: "Battery", Kind: setKindInfo,
					Visible: func(*Config) bool { return getBatteryPercent() >= 0 },
					Value:   batteryETASettingsValue,
					Detail:  batteryETASettingsDetail,
				},
				{
					ID: "screensaver", Label: "Screensaver after", Kind: setKindChoice,
					Detail: setStatic("Idle time before the idle screen starts"),
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
							applyScreensaverTimeout(c, n)
						}
					},
				},
				{
					ID: "idleScreen", Label: "Idle screen", Kind: setKindChoice,
					Detail: setStatic("The clock dock shows the time, the weather and what is playing"),
					Options: func(*Config) []setOption {
						return []setOption{{Value: "dock", Label: "Clock dock"}, {Value: "stars", Label: "Starfield"}}
					},
					Current: func(c *Config) int {
						if ambientModeEnabled(c) {
							return 0
						}
						return 1
					},
					Choose: func(c *Config, value string) {
						if c.Variables.Custom == nil {
							c.Variables.Custom = map[string]interface{}{}
						}
						c.Variables.Custom["ambientMode"] = value == "dock"
						saveUserConfigDebounced(userConfigFromVariables(c))
					},
				},
				// Off until chosen; handheld only (devpower.go).
				autoSleepSettingsRow(),
				// What the hardware power key does (powerkey.go).
				powerButtonSettingsRow(),
				{
					ID: "batterySaver", Label: "Battery saver at 20%", Kind: setKindToggle,
					Detail: setStatic("Low battery: slower cadence, no motion, OLED theme"),
					Get:    batterySaverEnabled,
					Set: func(c *Config, on bool) {
						setStoreBool(c, "batterySaver", on)
						if !widgetToggleSideEffect(c, "batterySaver", on) {
							setToast("Battery saver: " + setOnOff(on))
						}
					},
				},
				{
					ID: "lowBattery", Label: "Save and suspend at 3%", Kind: setKindToggle,
					Detail: setStatic("While a game runs: save state, sync, then suspend"),
					Get:    lowBatteryGuardEnabled,
					Set: func(c *Config, on bool) {
						setStoreBool(c, "lowBatteryGuard", on)
						setToast("Save and suspend at 3%: " + setOnOff(on))
					},
				},
				{
					ID: "lowPower", Label: "Low power", Kind: setKindToggle, Quiet: true,
					Detail: setStatic("A slower frame rate when nothing moves"),
					Get:    effectiveLowPower,
					Set:    setQSWrite(qsActionLowPower),
				},
			},
		},
		{
			// The search engine and the home page (browser_engines.go), the
			// page-script switch (webdom_runtime.go), the full-screen layout
			// (brw_fullscreen.go), the ad and tracker filter (brw_adblock.go) and,
			// on the handheld, WPE Browser (brw_wpe.go). It sits beside Privacy &
			// data, whose first rows are the Browser's sign-ins.
			ID: "browser", Title: "Browser", Glyph: "browser",
			Subtitle: "Search engine, home page, JavaScript, full screen and ads",
			Rows:     append(brwSettingsRows(), webJSSettingsRow(), brwFullScreenSettingsRow(), brwAdBlockSettingsRow(), brwWpeSettingsRow()),
		},
		{
			ID: "privacy", Title: "Privacy & data", Glyph: "shield",
			Subtitle: "Sign-ins, diagnostics and stored data",
			Rows: []setRow{
				{
					ID: "cookies", Label: "Keep browser sign-ins", Kind: setKindChoice,
					Detail: func(*Config) string {
						if since, ok := browserSessionSince(); ok {
							return "The saved session began " + since.Local().Format("Jan 2")
						}
						return "No sign-ins are saved right now"
					},
					Options: func(*Config) []setOption {
						out := make([]setOption, 0, len(cookieRetentionSteps))
						for _, d := range cookieRetentionSteps {
							label := cookieRetentionLabel(d)
							if d <= 0 {
								label = "Until I forget them"
							}
							out = append(out, setOption{Value: strconv.Itoa(d), Label: label})
						}
						return out
					},
					Current: func(c *Config) int {
						days := cookieRetentionDays(c)
						for i, d := range cookieRetentionSteps {
							if d == days {
								return i
							}
						}
						return -1
					},
					Value: func(c *Config) string { return cookieRetentionLabel(cookieRetentionDays(c)) },
					Choose: func(c *Config, value string) {
						days, err := strconv.Atoi(value)
						if err != nil {
							return
						}
						pruned := applyCookieRetention(c, days)
						msg := "Browser sign-ins are kept until you forget them"
						if days > 0 {
							msg = "Browser sign-ins expire after " + cookieRetentionLabel(days)
						}
						if pruned > 0 {
							msg += " · " + strconv.Itoa(pruned) + " removed"
						}
						setToast(msg)
					},
				},
				{
					ID: "forgetCookies", Label: "Forget browser sign-ins now", Kind: setKindAction,
					Detail: setStatic("Press twice: signs you out of every site"),
					Value: func(*Config) string {
						if n := browserCookiesCount(); n > 0 {
							return browserCookieCountLabel(n)
						}
						return "None saved"
					},
					Open: func(c *Config) { forgetSavedCookiesFromSettings(c) },
				},
				{
					ID: "diagnostics", Label: "Send anonymous diagnostics", Kind: setKindToggle,
					Detail: func(*Config) string {
						return "Public Info issue every 10 launches: anonymous device facts only"
					},
					Get: func(c *Config) bool { return telemetryOptedInFrom(c.Variables.Custom) },
					Set: func(c *Config, on bool) {
						if err := setTelemetryOptedIn(c, on); err != nil {
							ShowToast("Diagnostics: "+err.Error(), ToastKindWarning)
							return
						}
						setToast("Anonymous diagnostics: " + setOnOff(on))
					},
				},
				{
					ID: "clearCache", Label: "Clear cache", Kind: setKindAction,
					// "Saved pages" is the Browser's bookmarks list, which this
					// keeps: the old line read as if it deleted them.
					Detail: setStatic("Thumbnails and cached pages; files and saved pages stay"),
					Open: func(c *Config) {
						browserClearPageCache()
						handleTrigger(nil, c, Element{Trigger: "cache_clear"})
					},
				},
			},
		},
		{
			ID: "plugins", Title: "Plugins", Glyph: "plugin",
			Subtitle: "Small scripts that add actions to JukaHub",
			Dynamic:  setPluginRows,
		},
		{
			ID: "about", Title: "About", Glyph: "info",
			Subtitle: "This JukaHub, updates and help",
			Rows: []setRow{
				{
					ID: "device", Label: "Device", Kind: setKindInfo,
					Value: func(*Config) string { return deviceDisplayName(DetectDevice()) },
				},
				{
					ID: "system", Label: "System", Kind: setKindInfo,
					Value: setStatic(runtime.GOOS + " · " + runtime.GOARCH),
				},
				{
					// The update channel's state in one line (upch.go).
					ID: "updates", Label: "Updates", Kind: setKindAction,
					Detail: func(*Config) string { return upchStatusView().Line },
					Open:   setOpenScene("Patch"),
				},
				{
					ID: "updatesAuto", Label: "Download updates on Wi-Fi", Kind: setKindToggle,
					Detail: upchAutoDetail,
					Get:    upchAutoDownloadEnabled,
					Set: func(c *Config, on bool) {
						upchSetAutoDownload(c, on)
						setToast("Automatic update downloads: " + setOnOff(on))
					},
				},
				{
					ID: "updatesKeep", Label: "Keep previous versions", Kind: setKindChoice,
					Detail:  func(*Config) string { return uhistKeepDetail() },
					Options: func(*Config) []setOption { return uhistKeepOptions() },
					Current: uhistKeepIndex,
					Choose:  uhistChooseKeep,
				},
				{
					ID: "updatesCheck", Label: "Check for updates now", Kind: setKindAction,
					Detail: func(*Config) string { return upchLastCheckLine() },
					Open:   upchCheckNowFromSettings,
				},
				{
					ID: "aboutPage", Label: "About JukaHub", Kind: setKindAction,
					Detail: setStatic("Build, credits and licences"),
					Open:   setOpenScene("About"),
				},
				{
					// Every setting that differs from the defaults, as one
					// versioned file to diff, export, import or reset (cfgdoc.go).
					ID: "configInspector", Label: "Config", Kind: setKindAction,
					Detail: setStatic("What you changed from the defaults; export, import, reset"),
					Value:  cfgdocUpstreamLabel,
					Open:   cfgdocOpenInspector,
				},
				{
					ID: "backupRestore", Label: "Backup & restore", Kind: setKindAction,
					Detail: setStatic("Your whole setup in one file you can copy to any device"),
					Open:   bkupOpenScreen,
				},
				{
					ID: "restoreDefaults", Label: "Restore all defaults", Kind: setKindAction,
					Detail: setStatic("Reset settings and Home layout; keep games and saves"),
					Open: func(c *Config) {
						recoveryConfirmReset(c, func(*Config) { recoveryCardClose() })
					},
				},
				{
					ID: "bug", Label: "Report a bug", Kind: setKindAction,
					Detail: setStatic("Tell us what went wrong, with a diagnostics bundle"),
					Open:   bugrepOpenScreen,
				},
				{
					ID: "trust", Label: "Security & trust", Kind: setKindAction,
					Detail: setStatic("What JukaHub may do, and how updates are signed"),
					Open:   sectrustOpenScreen,
				},
				{
					ID: "tour", Label: "Replay the welcome tour", Kind: setKindAction,
					Detail: setStatic("The first-run walkthrough, again"),
					Open:   func(*Config) { onboardingStart() },
				},
			},
		},
	}
}

// setPluginRows lists the installed plugins as switches, then the page with
// their actions. The scan is the one LoadPlugins keeps; the hub refreshes it
// when the section opens.
func setPluginRows(*Config) []setRow {
	entries := plugCatalogEntries()
	rows := make([]setRow, 0, len(entries)+1)
	if len(entries) == 0 {
		rows = append(rows, setRow{
			ID: "noPlugins", Label: "No plugins installed", Kind: setKindInfo,
			Detail: setStatic("Add a folder with a plugin.json to plugins/"),
		})
	}
	for _, e := range entries {
		name := e.Name
		detail := e.Description
		if e.Problem != "" {
			detail = "Can't run here: " + e.Problem
		}
		rows = append(rows, setRow{
			ID: "plugin:" + name, Label: e.Title, Kind: setKindToggle,
			Detail: setStatic(setOr(detail, "No description")),
			Get: func(*Config) bool {
				entry, ok := plugEntryNamed(name)
				return ok && entry.Enabled
			},
			Set: func(c *Config, on bool) {
				plugSetEnabled(c, name, on)
				// The Plugins page's own switch path: switched on, the plugin's
				// onEnable action runs, and the toast says what it added or
				// withdrew. This row used to skip both (plughooks.go).
				plugAfterToggle(c, name, on)
			},
		})
	}
	rows = append(rows, setRow{
		ID: "pluginPage", Label: "Plugin actions and details", Kind: setKindAction,
		Detail: setStatic("Run a plugin, see what it printed, read about it"),
		Open:   setOpenScene("Plugins"),
	})
	return rows
}
