package main

// widgets.go holds the "contextual smart widgets": behaviour that reacts to the
// device instead of to the user.
//
//  1. Battery saver (on by default). At or below 20% charge, and not while
//     charging, JukaHub drops to the low-power cadence, stops UI motion and
//     switches to the OLED theme. The user's own settings are remembered and
//     restored once the battery climbs back above 25%, so the mode is a
//     temporary reaction rather than a permanent configuration change.
//  2. Time-of-day theme (off by default). Picks a theme preset from the clock
//     and re-applies it as the day moves on.
//
// Both are ordinary settings: they appear as toggles on the Smart Widgets page
// (reached from Settings → General, injected in memory by ensureSettingsWidgets)
// and persist in jukauser.json like every other toggle.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// batterySaverEnterPercent is where the saver engages; Exit is where it
	// lets go. The gap stops a battery hovering at one percent from flapping.
	batterySaverEnterPercent = 20
	batterySaverExitPercent  = 25

	// widgetTickInterval throttles the battery/clock checks: they read sysfs and
	// must not run on every frame.
	widgetTickInterval = time.Second
)

// batterySaverSnapshot remembers what the user had configured before the saver
// overrode it.
type batterySaverSnapshot struct {
	valid         bool
	reducedMotion bool
	lowPower      bool
	themePreset   string
}

var (
	batterySaverActive bool
	batterySaverSaved  batterySaverSnapshot
	lastWidgetTick     time.Time

	// lastTimeOfDayBucket is the hour whose preset was last applied. Tracking
	// the bucket (not the preset) is what lets a manual theme choice stick: the
	// automatic switch only fires when the time of day actually moves on.
	lastTimeOfDayBucket = -1
)

// --- settings ---------------------------------------------------------------

// batterySaverEnabled reads the setting; it defaults to on, so the device looks
// after itself until the user says otherwise. An absent key must not read as
// "off": only an explicit false disables it.
func batterySaverEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "batterySaver"); ok {
		return coerceBool(v)
	}
	return true
}

// timeOfDayThemeEnabled reads the setting; off by default.
func timeOfDayThemeEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "timeOfDayTheme"); ok {
		return coerceBool(v)
	}
	return false
}

// --- battery ----------------------------------------------------------------

// getBatteryCharging reports whether the battery is charging/full. Unknown
// (no sysfs entry, desktop machine) counts as not charging, which lets the
// saver engage on a low battery rather than never engaging at all.
func getBatteryCharging() bool {
	dir := batterySysfsDir()
	if dir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(data))) {
	case "charging", "full", "not charging": // "not charging" = plugged in, held full
		return true
	}
	return false
}

// batterySysfsDir returns the battery directory under /sys/class/power_supply.
func batterySysfsDir() string {
	return batterySysfsDirIn("/sys/class/power_supply")
}

// batterySysfsDirIn picks the battery from one power_supply directory. The
// entries there are symlinks into the device tree, and DirEntry.IsDir reports
// false for a link, so the old IsDir check skipped every battery and the Smart
// Pro read "unknown". The lookup therefore follows the link with os.Stat and
// prefers an entry that actually exposes a charge level.
func batterySysfsDirIn(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var named, withCapacity string
	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		info, err := os.Stat(path) // follows the symlink
		if err != nil || !info.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		looksLikeBattery := strings.Contains(name, "bat")
		switch {
		case looksLikeBattery && batteryLevelFile(path) != "":
			return path
		case looksLikeBattery && named == "":
			named = path
		case batteryLevelFile(path) != "" && withCapacity == "":
			withCapacity = path
		}
	}
	if named != "" {
		return named
	}
	return withCapacity
}

// batteryLevelFile names the first readable charge source in a battery
// directory. Kernels expose either a ready-made percentage or raw counters.
func batteryLevelFile(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "capacity")); err == nil {
		return "capacity"
	}
	for _, counter := range [][2]string{{"charge_now", "charge_full"}, {"energy_now", "energy_full"}} {
		if _, err := os.Stat(filepath.Join(dir, counter[0])); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, counter[1])); err == nil {
			return counter[0]
		}
	}
	return ""
}

// batteryPercentIn reads the charge percentage from one battery directory,
// returning -1 when the kernel exposes no usable number.
func batteryPercentIn(dir string) int {
	if dir == "" {
		return -1
	}
	if pct, ok := readBatteryCounter(filepath.Join(dir, "capacity")); ok {
		if pct > 100 {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		}
		return int(pct)
	}
	for _, pair := range [][2]string{{"charge_now", "charge_full"}, {"energy_now", "energy_full"}} {
		now, okNow := readBatteryCounter(filepath.Join(dir, pair[0]))
		full, okFull := readBatteryCounter(filepath.Join(dir, pair[1]))
		if !okNow || !okFull || full <= 0 {
			continue
		}
		pct := now * 100 / full
		if pct > 100 {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		}
		return int(pct)
	}
	return -1
}

// readBatteryCounter parses one sysfs counter. Some kernels add a newline and
// a few append units, so only the leading integer is significant.
func readBatteryCounter(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// batterySaverShouldEngage is the pure decision: engage at or below the enter
// threshold while unplugged, release above the exit threshold, and hold the
// current state anywhere in between.
func batterySaverShouldEngage(percent int, charging, active bool) bool {
	if percent < 0 { // unknown battery: never engage
		return false
	}
	if charging {
		return false
	}
	if active {
		return percent <= batterySaverExitPercent
	}
	return percent <= batterySaverEnterPercent
}

// applyBatterySaver switches the device into (or out of) saver mode, preserving
// the user's own motion/low-power/theme settings across the transition.
func applyBatterySaver(config *Config, engage bool, percent int) {
	if config == nil || engage == batterySaverActive {
		return
	}
	if engage {
		batterySaverSaved = batterySaverSnapshot{
			valid:         true,
			reducedMotion: config.Variables.ReducedMotion,
			lowPower:      config.Variables.LowPower,
			themePreset:   themePresetName(config),
		}
		config.Variables.ReducedMotion = true
		config.Variables.LowPower = true
		config.Variables.Custom["reducedMotion"] = "true"
		config.Variables.Custom["lowPower"] = "true"
		applyThemePreset(config, "oled", false)
		batterySaverActive = true
		showToast(saverMessage("Battery saver on", percent), ToastInfo())
	} else {
		if batterySaverSaved.valid {
			config.Variables.ReducedMotion = batterySaverSaved.reducedMotion
			config.Variables.LowPower = batterySaverSaved.lowPower
			config.Variables.Custom["reducedMotion"] = boolString(batterySaverSaved.reducedMotion)
			config.Variables.Custom["lowPower"] = boolString(batterySaverSaved.lowPower)
			if preset := batterySaverSaved.themePreset; preset != "" {
				applyThemePreset(config, preset, false)
			}
			batterySaverSaved = batterySaverSnapshot{}
		}
		batterySaverActive = false
		showToast(saverMessage("Battery saver off", percent), ToastSuccess())
	}
	// The cadence controller reads LowPower from the config each frame, so the
	// change takes effect on the next tick.
	saveUserConfigDebounced(userConfigFromVariables(config))
}

func saverMessage(prefix string, percent int) string {
	if percent < 0 {
		return prefix
	}
	return prefix + " — battery " + strconv.Itoa(percent) + "%"
}

func themePresetName(config *Config) string {
	if config == nil {
		return ""
	}
	if v, ok := customValue(config.Variables.Custom, "theme_preset"); ok {
		return coerceString(v)
	}
	return ""
}

// --- time of day ------------------------------------------------------------

// timeOfDayPresetFor maps an hour to a theme preset. Every name exists in the
// theme gallery, so a preset rename shows up here as a compile-time no-op
// rather than a crash.
func timeOfDayPresetFor(hour int) string {
	switch {
	case hour >= 5 && hour < 7:
		return "sunset" // dawn
	case hour >= 7 && hour < 19:
		return "light"
	case hour >= 19 && hour < 22:
		return "midnight"
	default:
		return "oled" // night
	}
}

// applyTimeOfDayTheme switches to the preset for the current hour. It returns
// the preset it applied, or "" when the hour has not moved on (or the theme
// already matches), so a theme the user picked by hand is not reverted a
// second later — it stands until the next time-of-day bucket.
func applyTimeOfDayTheme(config *Config, now time.Time) string {
	if config == nil || !timeOfDayThemeEnabled(config) {
		return ""
	}
	if now.Hour() == lastTimeOfDayBucket {
		return ""
	}
	lastTimeOfDayBucket = now.Hour()
	want := timeOfDayPresetFor(now.Hour())
	if want == themePresetName(config) {
		return ""
	}
	applyThemePreset(config, want, false)
	return want
}

// --- per-frame tick ---------------------------------------------------------

// widgetsTick runs the contextual checks on a timer. Call it from the render
// loop; it is cheap because it only does real work once per interval.
func widgetsTick(config *Config) {
	now := time.Now()
	if !lastWidgetTick.IsZero() && now.Sub(lastWidgetTick) < widgetTickInterval {
		return
	}
	lastWidgetTick = now

	// Battery saver. `sdl.GetTicks64()` keeps the toast timing honest even when
	// the clock is adjusted.
	percent := getBatteryPercent()
	if batterySaverEnabled(config) {
		engage := batterySaverShouldEngage(percent, getBatteryCharging(), batterySaverActive)
		applyBatterySaver(config, engage, percent)
	} else if batterySaverActive {
		applyBatterySaver(config, false, percent)
	}

	// Low-battery guard: save state and suspend before the card loses power.
	lowBatteryTick(config)

	// Time-of-day theme, unless the saver owns the theme right now.
	if !batterySaverActive {
		applyTimeOfDayTheme(config, now)
	}
}

// --- settings scene ---------------------------------------------------------

// smartSettingsScene is the scene the widget toggles live in. Settings → General
// has no room left for three more rows, so they get their own page reachable
// from a button there.
const smartSettingsScene = "SettingsSmart"

// ensureSettingsWidgets adds the Smart Widgets page and a way into it from
// Settings → General. Like ensureAlarmScene this edits the loaded config in
// memory only: no shipped jukaconfig.json is rewritten, and the desktop and
// handheld configs both get the page without editing two files.
func ensureSettingsWidgets(config *Config) {
	if config == nil {
		return
	}
	ensureSmartSettingsScene(config)
	ensureSmartSettingsEntry(config)
	ensureScreensaverTimeoutSetting(config)
	ensureCookieRetentionSetting(config)
	ensureSnapPreviewSetting(config)
}

// ensureScreensaverTimeoutSetting puts the screensaver timeout in Settings →
// General next to the Smart Widgets button. One tap cycles the same steps Quick
// Settings uses, so the device can be set to sleep after 15–25 seconds without
// opening the overlay.
func ensureScreensaverTimeoutSetting(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil {
		return
	}
	// The row's label is a $ replacement, and substitution only reads the
	// custom block, so the derived text has to be seeded there. Without this
	// the row rendered the placeholder itself.
	config.Variables.Custom["screensaverTimeoutLabel"] = screenTimeoutLabel(config.Variables.ScreensaverTimeout)
	for _, el := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(el.Trigger), "cycle_screensaver_timeout") {
			return
		}
	}
	scene.Elements = append(scene.Elements,
		Element{
			Type:    "button",
			Text:    "Screensaver · $screensaverTimeoutLabel",
			X:       340,
			Y:       520,
			Width:   "300",
			Height:  "48",
			Trigger: "cycle_screensaver_timeout",
			Color:   "$labelColor",
			BgColor: "$buttonColor",
		},
		Element{
			Type:  "label",
			Text:  "Idle time before the screensaver starts. Tap to change.",
			Font:  "small",
			X:     340,
			Y:     576,
			Color: "$theme_text_secondary",
		},
	)
}

// ensureCookieRetentionSetting puts the saved-session age policy in Settings →
// General, beside the screensaver row, together with the press that drops the
// session already on the card. Like the rest of this file it edits the
// in-memory config, so no shipped JSON is rewritten, and it seeds the labels the
// row substitutes ($cookieRetentionLabel for the toast wording, $cookieRowLabel
// for the row itself) from the value already merged in from jukauser.json.
func ensureCookieRetentionSetting(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil {
		return
	}
	days := cookieRetentionDays(config)
	config.Variables.Custom["cookieRetentionLabel"] = cookieRetentionLabel(days)
	// The row also says when the session it measures began, which is the
	// reference point "30 days" is missing on its own.
	refreshCookieRetentionRow(config)
	// Every slot on this page is taken: Smart Widgets at 40,520, Screensaver at
	// 340,520, the Vibration toggle (when a motor exists) at 40,592, and Save at
	// 1040,592. The retention row's band is the right column under the Audio
	// Backend read-out: 556..604 clears that label by a few pixels and stays
	// above the footer.
	//
	// "Forget now" goes one row up against the right edge, above Save. It cannot
	// share the retention row's 360px: at this font the row's own label runs
	// 319–332px and "Forget now" 127px, so splitting the band would ellipsize
	// both.
	// The slot above Save is free ("Replay tour" moved to the page's top-right
	// corner) and keeps the two cookie controls in one cluster.
	//
	// The row's own label is "Cookies · 30d · since Oct 12". The session date is
	// the reference point the window needs, and fitting both into the cell means
	// shortening both: "Saved cookies · 30 days · since Oct 12" is 436px against
	// 352px of inner width, while the shipped form fits — widest with "Keep"
	// ("Cookies · Keep · since Oct 12", 332px). The row still cycles the policy,
	// so the window stays first.
	// Coordinates are scaled to the compact canvas the same way the rest of the
	// runtime scales authored geometry.
	width, height := screenWidth, screenHeight
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 720
	}
	hasCycle, hasForget := false, false
	for _, el := range scene.Elements {
		switch strings.ToLower(strings.TrimSpace(el.Trigger)) {
		case "cycle_cookie_retention":
			hasCycle = true
		case "forget_saved_cookies":
			hasForget = true
		}
	}
	if !hasCycle {
		scene.Elements = append(scene.Elements, Element{
			Type:    "button",
			Text:    "Cookies · $cookieRowLabel",
			X:       scaleX(660, width),
			Y:       scaleY(556, height),
			Width:   StringOrInt(strconv.Itoa(int(scaleX(360, width)))),
			Height:  StringOrInt(strconv.Itoa(int(scaleY(48, height)))),
			Trigger: "cycle_cookie_retention",
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			Font:    "small",
		})
	}
	if !hasForget {
		scene.Elements = append(scene.Elements, Element{
			Type:    "button",
			Text:    "Forget now",
			X:       scaleX(1040, width),
			Y:       scaleY(520, height),
			Width:   StringOrInt(strconv.Itoa(int(scaleX(216, width)))),
			Height:  StringOrInt(strconv.Itoa(int(scaleY(48, height)))),
			Trigger: "forget_saved_cookies",
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			Font:    "small",
		})
	}
}

// ensureSmartSettingsScene creates the page unless the config already has it
// (a future build can ship the scene in JSON and win over this default).
func ensureSmartSettingsScene(config *Config) {
	if sceneByName(config, smartSettingsScene) != nil {
		return
	}
	rows := []struct {
		label    string
		variable string
		note     string
	}{
		{"Start JukaHub at login", "autoStart",
			"Off by default. Adds one launcher to your startup folder."},
		{"Battery saver (automatic)", "batterySaver",
			"At 20% or less: slower cadence, no motion, OLED theme."},
		{"Theme follows time of day", "timeOfDayTheme",
			"Dawn sunset · day light · evening midnight · night OLED."},
		{"Save & suspend at 3%", "lowBatteryGuard",
			"While a game runs: save state, sync, then suspend."},
	}
	elems := []Element{{
		Type: "label",
		Text: "Smart widgets react to the device, not to you.",
		X:    40, Y: 96, Color: "$labelColor",
	}}
	y := int32(140)
	for _, row := range rows {
		elems = append(elems, Element{
			Type:         "toggle",
			Text:         row.label,
			Variable:     row.variable,
			X:            40,
			Y:            y,
			Width:        "560",
			Height:       "52",
			TriggerValue: defaultWidgetValue(row.variable),
		})
		elems = append(elems, Element{
			Type:  "label",
			Text:  row.note,
			Font:  "small",
			X:     40,
			Y:     y + 58,
			Color: "$theme_text_secondary",
		})
		y += 100
	}
	elems = append(elems, Element{
		Type:  "label",
		Text:  "On the handheld the hook is System/starts on the SD card; here it is your login folder.",
		Font:  "small",
		X:     40,
		Y:     y + 10,
		Color: "$theme_text_secondary",
	})

	config.Scenes = append(config.Scenes, SceneConfig{
		Name:       smartSettingsScene,
		Background: "#0B0F17",
		Elements:   elems,
	})
}

// ensureSmartSettingsEntry puts the button that opens the page into
// Settings → General, in the free space under its left column (the right
// column's Save button sits at x=1040, y=592).
func ensureSmartSettingsEntry(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil {
		return
	}
	for _, el := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(el.TriggerTarget), smartSettingsScene) ||
			strings.EqualFold(strings.TrimSpace(el.Trigger), "change_scene:"+smartSettingsScene) {
			return
		}
	}
	scene.Elements = append(scene.Elements, Element{
		Type:          "button",
		Text:          "Smart Widgets",
		X:             40,
		Y:             520,
		Width:         "280",
		Height:        "48",
		Trigger:       "change_scene:" + smartSettingsScene,
		TriggerTarget: smartSettingsScene,
		Color:         "$labelColor",
		BgColor:       "$buttonColor",
	})
}

// defaultWidgetValue gives a new toggle its default in the same encoding the
// toggle element already uses ("true"/"false").
func defaultWidgetValue(variable string) string {
	switch variable {
	case "batterySaver", "lowBatteryGuard":
		return "true" // on by default
	default:
		return "false" // autoStart and timeOfDayTheme are opt-in
	}
}

func settingsGeneralScene(config *Config) *SceneConfig {
	return sceneByName(config, "SettingsGeneral")
}

func sceneByName(config *Config, name string) *SceneConfig {
	if idx := sceneIndexExact(config, name); idx >= 0 {
		return &config.Scenes[idx]
	}
	return nil
}

// widgetToggleSideEffect applies the non-visual half of a widget toggle right
// away, so flipping the switch has an immediate effect instead of waiting for
// the next tick. It reports whether it handled the toggle (and therefore
// whether the caller should skip its generic "On/Off" toast).
func widgetToggleSideEffect(config *Config, variable string, on bool) bool {
	switch strings.ToLower(strings.TrimSpace(variable)) {
	case "autostart":
		if err := setAutoStart(on); err != nil {
			// Leave the switch where the user put it: the setting is honest about
			// what the device can do, and the error says why nothing changed.
			showToast("Start at login: "+err.Error(), ToastWarn())
			return true
		}
		if on {
			showToast("JukaHub will start at login", ToastSuccess())
		} else {
			showToast("JukaHub will not start at login", ToastInfo())
		}
		return true
	case "batterysaver":
		if !on && batterySaverActive {
			applyBatterySaver(config, false, getBatteryPercent())
			showToast("Battery saver will stay off", ToastInfo())
			return true
		}
		return false
	case "hapticsenabled":
		// Apply at once. The switch's own click already played under the old
		// value, so turning feedback on confirms itself with one pulse here.
		hapticsSetEnabled(on)
		if on {
			playHaptic(hapticTogglePulse)
		}
		return false
	case "timeofdaytheme":
		// Re-evaluate from scratch so turning it on applies the preset for this
		// hour right away.
		lastTimeOfDayBucket = -1
		if !on {
			showToast("Theme no longer follows time of day", ToastInfo())
			return true
		}
		if preset := applyTimeOfDayTheme(config, time.Now()); preset != "" {
			showToast("Theme follows time of day: "+GetThemePreset(preset).Name, ToastSuccess())
		} else {
			showToast("Theme already matches the time of day", ToastInfo())
		}
		return true
	}
	return false
}

// batterySaverState is used by diagnostics and tests.
func batterySaverState() bool { return batterySaverActive }
