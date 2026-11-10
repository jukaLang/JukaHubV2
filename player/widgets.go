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
	"sync"
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

	// lastTimeOfDayBucket is the part of the day (themePhaseDay or
	// themePhaseNight, themeschedule.go) the schedule last acted on. Tracking
	// the part of the day (not the preset) is what lets a manual theme choice
	// stick: the automatic switch only fires when the day actually moves on.
	lastTimeOfDayBucket = themePhaseUnknown
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

// powerSupplyRoot is the kernel's power-supply class: the battery and every
// charger input (USB, mains, wireless) appear here as one directory each.
const powerSupplyRoot = "/sys/class/power_supply"

// batteryChargingCache keeps the power-supply walk off the render path. The
// status bar's battery gauge draws the charging bolt from this on every frame,
// and on the handheld each uncached read is a directory walk plus a handful of
// sysfs opens.
var batteryChargingCache struct {
	mu       sync.Mutex
	at       time.Time
	charging bool
}

// getBatteryCharging reports whether the device is on external power: the
// battery says it is charging (or held full on the cable), or a charger input
// reports that it is plugged in. Unknown (no sysfs entry, desktop machine)
// counts as not charging, which lets the saver engage on a low battery rather
// than never engaging at all. The answer is reused for batteryPercentRefresh,
// the same window the level uses.
//
// The battery's own status was the only source, and the TrimUI Smart Pro's
// AXP2202 driver does not reliably report "Charging" there: plugged in, the
// corner showed a plain gauge. Its USB input (axp2202-usb/online) is the
// reading MinUI and NextUI use for the same question, so a charger that is
// online now counts too.
func getBatteryCharging() bool {
	batteryChargingCache.mu.Lock()
	defer batteryChargingCache.mu.Unlock()
	if !batteryChargingCache.at.IsZero() && time.Since(batteryChargingCache.at) < batteryPercentRefresh {
		return batteryChargingCache.charging
	}
	charging := batteryChargingIn(powerSupplyRoot)
	batteryChargingCache.at = time.Now()
	batteryChargingCache.charging = charging
	return charging
}

// batteryChargingIn is getBatteryCharging for one power_supply directory, so a
// test can lay out a fake sysfs. A battery that says "Charging" is on external
// power whatever else is there. Otherwise, on a device with a charger input,
// that input's `online` decides: "Full" and "Not charging" alone are only
// trusted where there is no charger entry at all, because some fuel gauges
// latch Full at 100% or report "Not charging" when the current reads 0 with no
// cable in — and with the bolt in the corner, such a false positive is on
// screen. ("Not charging" is otherwise a battery on the cable held where it is:
// full, or paused for heat.)
func batteryChargingIn(base string) bool {
	status := ""
	if dir := batterySysfsDirIn(base); dir != "" {
		status = batteryStatusIn(dir)
	}
	if status == "charging" {
		return true
	}
	if chargers, online := powerSupplyChargersIn(base); chargers > 0 {
		return online
	}
	return status == "full" || status == "not charging"
}

// batteryStatusIn reads a battery's status file, lower-cased: "charging",
// "discharging", "full", "not charging" or "unknown"; "" when it cannot be
// read.
func batteryStatusIn(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(data)))
}

// powerSupplyChargersIn counts the charger inputs in one power_supply
// directory and reports whether any of them has power on it.
func powerSupplyChargersIn(base string) (chargers int, online bool) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, false
	}
	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		// Entries are symlinks into the device tree: Stat follows them, the
		// way batterySysfsDirIn does (DirEntry.IsDir reports false for a link).
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		isCharger, on := powerSupplyCharger(path, entry.Name())
		if !isCharger {
			continue
		}
		chargers++
		if on {
			online = true
		}
	}
	return chargers, online
}

// powerSupplyCharger reports whether one power_supply entry is a charger input
// — one with an `online` reading — and whether power is on it. The kernel's
// own type decides when it names one (Mains, USB and its USB_* variants,
// Wireless); a driver that leaves the type empty or vague is judged by its name
// (axp2202-usb, *-ac, *charger*). A battery is never a charger, whatever its
// name says.
func powerSupplyCharger(dir, name string) (isCharger, online bool) {
	kind := ""
	if data, err := os.ReadFile(filepath.Join(dir, "type")); err == nil {
		kind = strings.ToLower(strings.TrimSpace(string(data)))
	}
	if kind == "battery" {
		return false, false
	}
	value, ok := readBatteryCounter(filepath.Join(dir, "online"))
	if !ok {
		return false, false
	}
	switch {
	case kind == "mains", kind == "usb", kind == "wireless", strings.HasPrefix(kind, "usb_"):
	case powerSupplyNameLooksLikeCharger(name):
	default:
		return false, false
	}
	return true, value > 0
}

// powerSupplyNameLooksLikeCharger is the name half of powerSupplyCharger:
// whole words like "ac", "usb", "dc" or "vbus" (so "axp2202-usb" and "bq-ac"
// count but "tracker" does not), or a name containing charger/adapter/mains.
func powerSupplyNameLooksLikeCharger(name string) bool {
	n := strings.ToLower(name)
	for _, word := range []string{"charger", "adapter", "mains"} {
		if strings.Contains(n, word) {
			return true
		}
	}
	for _, field := range strings.FieldsFunc(n, func(r rune) bool { return r < 'a' || r > 'z' }) {
		switch field {
		case "ac", "usb", "dc", "vbus", "pd":
			return true
		}
	}
	return false
}

// --- is the battery holding up? ----------------------------------------------
//
// getBatteryCharging answers "is a cable in?", which is what the bolt, Quick
// Settings and the update and LED policies want. The low-battery guard and the
// battery saver need a different answer — "is the battery not draining?" — and
// the two part ways on a supply that cannot keep up: a 500 mA PC port under an
// emulator holds the cable in while the level still falls, and the guard
// standing down for the cable let the PMIC cut power at its floor with no save.
// So those two stand down only while the battery itself says it is charging,
// or while a cable is in and the level has not fallen since it went in.

// batteryTrend is the level since the cable last went in, fed by widgetsTick.
var batteryTrend struct {
	mu      sync.Mutex
	cableIn bool
	state   batteryTrendState
}

// batteryTrendState is the history batteryHoldingCharge reads: the highest
// level seen since the cable went in and the newest one. have is false until
// the first reading.
type batteryTrendState struct {
	have   bool
	peak   int
	newest int
}

// note returns the state after one more reading; an unknown level (-1) changes
// nothing. The peak only ever rises. It used to be the highest reading of the
// last ten minutes, and on a charger that loses a percent every ten minutes or
// more that window aged each drop out: the battery read "holding" again until
// the next one, so near 20-25% the battery saver turned off and on every cycle
// (a toast, a theme switch and a settings save each time) and the low-battery
// warning repeated. Pure.
func (s batteryTrendState) note(pct int) batteryTrendState {
	if pct < 0 {
		return s
	}
	if !s.have || pct > s.peak {
		s.peak = pct
	}
	s.newest = pct
	s.have = true
	return s
}

// falling reports whether the level is below the highest seen since the cable
// went in. It latches: the battery reads as holding again only once the level
// is back at that peak (a charger catching up) or the cable state changes. A
// cable that just went in has nothing to fall from and gets the benefit of the
// doubt. Pure.
func (s batteryTrendState) falling() bool {
	return s.have && s.newest < s.peak
}

// batteryTrendNote records one reading. A change of the cable state starts the
// history over, so the drain before a cable went in never counts against it.
func batteryTrendNote(pct int, cableIn bool) {
	batteryTrend.mu.Lock()
	defer batteryTrend.mu.Unlock()
	if cableIn != batteryTrend.cableIn {
		batteryTrend.cableIn = cableIn
		batteryTrend.state = batteryTrendState{}
	}
	if !cableIn {
		return
	}
	batteryTrend.state = batteryTrend.state.note(pct)
}

// batteryHoldingCharge is the "on external power" the low-battery guard and
// the battery saver use: the battery says it is charging (or full), or a cable
// is in and the level has not fallen below its peak since it went in.
func batteryHoldingCharge() bool {
	if dir := batterySysfsDir(); dir != "" {
		switch batteryStatusIn(dir) {
		case "charging", "full":
			return true
		}
	}
	if !getBatteryCharging() {
		return false
	}
	batteryTrend.mu.Lock()
	defer batteryTrend.mu.Unlock()
	return !batteryTrend.state.falling()
}

// batterySysfsDir returns the battery directory under /sys/class/power_supply.
func batterySysfsDir() string {
	return batterySysfsDirIn(powerSupplyRoot)
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
		return pct
	}
	for _, pair := range [][2]string{{"charge_now", "charge_full"}, {"energy_now", "energy_full"}} {
		now, okNow := readBatteryCounter(filepath.Join(dir, pair[0]))
		full, okFull := readBatteryCounter(filepath.Join(dir, pair[1]))
		if !okNow || !okFull || full <= 0 {
			continue
		}
		// Charge counters are microamp-hours, a few million at most, but the
		// multiply could still wrap a 32-bit int on a tiny kernel build. Divide
		// first so the product never leaves the int range.
		pct := (now/full)*100 + (now%full)*100/full
		if pct > 100 {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		}
		return pct
	}
	return -1
}

// readBatteryCounter parses one sysfs counter. Some kernels add a newline and
// a few append units, so only the leading integer is significant. It returns an
// int: every counter it reads is a percentage or a charge total far inside the
// range, and staying in one integer type keeps the callers free of casts.
func readBatteryCounter(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[0])
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
		// The saver's values reach jukauser.json (the save below, and every
		// save while it is on), so what they replace is saved with them. Kept
		// in memory only, it was lost when the battery died or JukaHub
		// restarted under the saver, and the OLED theme, reduced motion and
		// low power then stayed for good (restoreBatterySaverLeftover).
		config.Variables.Custom[batterySaverRestoreKey] = map[string]interface{}{
			"reducedMotion": batterySaverSaved.reducedMotion,
			"lowPower":      batterySaverSaved.lowPower,
			"themePreset":   batterySaverSaved.themePreset,
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
			} else {
				clearThemePreset(config)
			}
			batterySaverSaved = batterySaverSnapshot{}
		}
		delete(config.Variables.Custom, batterySaverRestoreKey)
		batterySaverActive = false
		// With light & dark by time of day on, the next tick puts on the theme
		// for this part of the day — which the saver's snapshot may no longer
		// be: a switch time passed, or a theme was picked, under the saver.
		if timeOfDayThemeEnabled(config) {
			lastTimeOfDayBucket = themePhaseUnknown
		}
		showToast(saverMessage("Battery saver off", percent), ToastSuccess())
	}
	// The cadence controller reads LowPower from the config each frame, so the
	// change takes effect on the next tick.
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// batterySaverRestoreKey holds, in the saved settings, what the battery saver
// replaced, for as long as it is engaged.
const batterySaverRestoreKey = "batterySaverRestore"

// restoreBatterySaverLeftover undoes a battery saver that was still engaged
// when JukaHub last stopped. Boot calls it once the theme files are loaded; a
// saver that is still needed engages again at the next battery check, over
// the user's own values this time.
func restoreBatterySaverLeftover(config *Config) {
	if config == nil || config.Variables.Custom == nil {
		return
	}
	stored, present := config.Variables.Custom[batterySaverRestoreKey]
	if !present {
		return
	}
	delete(config.Variables.Custom, batterySaverRestoreKey)
	saved, ok := stored.(map[string]interface{})
	if !ok {
		return
	}
	if v, ok := saved["reducedMotion"].(bool); ok {
		config.Variables.ReducedMotion = v
		config.Variables.Custom["reducedMotion"] = boolString(v)
	}
	if v, ok := saved["lowPower"].(bool); ok {
		config.Variables.LowPower = v
		config.Variables.Custom["lowPower"] = boolString(v)
	}
	if preset, _ := saved["themePreset"].(string); preset != "" {
		applyThemePreset(config, preset, false)
	} else {
		clearThemePreset(config)
	}
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

// applyTimeOfDayTheme applies the day or the night theme of the light & dark
// schedule (themeschedule.go). It returns the preset it applied, or "" when
// the part of the day has not moved on (or the theme already matches), so a
// theme the user picked by hand is not reverted a second later — it stands
// until the next switch.
func applyTimeOfDayTheme(config *Config, now time.Time) string {
	// Not under a running game: a switch clears the texture caches and saves
	// the settings, which can wait until the game hands the screen back.
	if gameScreenSuspended() {
		return ""
	}
	want, phase := themeScheduleStep(config, now, lastTimeOfDayBucket)
	lastTimeOfDayBucket = phase
	if want == "" {
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
	// the clock is adjusted. It stands down for a battery that is holding its
	// charge, not merely for a cable (batteryHoldingCharge); the level's recent
	// history behind that answer is kept here, once a second.
	percent := getBatteryPercent()
	batteryTrendNote(percent, getBatteryCharging())
	if batterySaverEnabled(config) {
		engage := batterySaverShouldEngage(percent, batteryHoldingCharge(), batterySaverActive)
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

	// A first-start default applied before the loop ran is announced here,
	// on a frame the user can see (autostart_firstrun.go).
	autoStartShowPendingNotice()
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
	ensureWeatherUnitSetting(config)
	// The Terminal, Scheduled tasks and the tool screens draw their own
	// controls; their old config buttons go (systool.go).
	sysToolMigrateScenes(config)
	ensureScreensaverTimeoutSetting(config)
	ensureCookieRetentionSetting(config)
	ensureSnapPreviewSetting(config)
	ensureOSDPresetSetting(config)
}

// Replace the old free-text weather unit field in existing designs as well as
// shipped configs; preserve its geometry and the separate Weather Unit label.
func ensureWeatherUnitSetting(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil {
		return
	}
	for i := range scene.Elements {
		elem := &scene.Elements[i]
		if elem.Type == "input" && strings.EqualFold(strings.TrimSpace(elem.Variable), "weatherUnit") {
			elem.Type = "button"
			elem.Text = "Weather unit"
			elem.Trigger = "cycle_weather_unit"
			elem.Variable = ""
			elem.Placeholder = ""
			elem.BgColor = "$buttonColor"
		}
	}
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
			Type: "label",
			Text: "Idle time before the screensaver starts. Tap to change.",
			Font: "small",
			X:    340,
			// Below the retention row's band (556..604), not at 576: the line
			// is ~616px wide, so at 576 its tail ran under the cookie row in
			// the right column and was cut mid-word. 612 clears the row by 8px
			// and still ends above the panel's bottom edge.
			Y:     612,
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
			"On from a TrimUI's first start; elsewhere off until you turn it on."},
		{"Battery saver (automatic)", "batterySaver",
			"At 20% or less: slower cadence, no motion, OLED theme."},
		{"Light & dark by time of day", "timeOfDayTheme",
			"Day and night themes and times: Settings → Display & theme."},
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
	case "weatherenabled":
		// The switch used to stop at the config: the poller kept fetching and
		// the forecast stayed on every screen. weather_toggle.go makes it live.
		weatherApplyEnabled(on)
		return false
	case "timeofdaytheme":
		// Re-evaluate from scratch so turning it on applies the theme for this
		// part of the day right away.
		lastTimeOfDayBucket = themePhaseUnknown
		if !on {
			showToast("Light & dark by time of day is off", ToastInfo())
			return true
		}
		// The Smart Widgets page turns it on without the Settings row's seeding:
		// the theme on screen keeps the part of the day it suits.
		if config != nil {
			themeScheduleSeed(config)
			saveUserConfigDebounced(userConfigFromVariables(config))
		}
		// The battery saver keeps its OLED while it is on; the first tick after
		// it lets go applies the schedule (the phase is unknown until then).
		if batterySaverActive {
			showToast("Light & dark by time of day is on · after the battery saver", ToastInfo())
			return true
		}
		if preset := applyTimeOfDayTheme(config, time.Now()); preset != "" {
			showToast("Light & dark by time of day: "+GetThemePreset(preset).Name+" now", ToastSuccess())
		} else {
			showToast("Light & dark by time of day is on", ToastInfo())
		}
		return true
	}
	return false
}
