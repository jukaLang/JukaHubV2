package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func widgetTestConfig() *Config {
	return &Config{Variables: Variables{Custom: map[string]interface{}{}}}
}

// The saver engages at 20% and only lets go at 25%: a battery sitting on 21%
// must not flip the mode (and the theme) on every tick.
func TestBatterySaverHysteresis(t *testing.T) {
	cases := []struct {
		name     string
		percent  int
		charging bool
		active   bool
		want     bool
	}{
		{"full battery", 100, false, false, false},
		{"just above enter threshold", 21, false, false, false},
		{"at enter threshold", 20, false, false, true},
		{"critical", 4, false, false, true},
		{"unknown battery never engages", -1, false, false, false},
		{"charging never engages", 8, true, false, false},
		{"charging releases an active saver", 8, true, true, false},
		{"holds state inside the band", 22, false, true, true},
		{"releases above the exit threshold", 26, false, true, false},
		{"stays off at 24 when not active", 24, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := batterySaverShouldEngage(tc.percent, tc.charging, tc.active); got != tc.want {
				t.Fatalf("batterySaverShouldEngage(%d, charging=%v, active=%v) = %v, want %v",
					tc.percent, tc.charging, tc.active, got, tc.want)
			}
		})
	}
}

// The saver is on unless the user turns it off; the time-of-day theme is the
// opposite, opt-in only.
func TestWidgetDefaults(t *testing.T) {
	cfg := widgetTestConfig()
	if !batterySaverEnabled(cfg) {
		t.Fatal("battery saver must default to on")
	}
	if timeOfDayThemeEnabled(cfg) {
		t.Fatal("time-of-day theme must default to off")
	}
	if autoStartEnabled(cfg) {
		t.Fatal("autostart must default to off")
	}
	cfg.Variables.Custom["batterySaver"] = "false"
	if batterySaverEnabled(cfg) {
		t.Fatal("an explicit false must disable the battery saver")
	}
	cfg.Variables.Custom["batterySaver"] = false // JSON loads a real bool
	if batterySaverEnabled(cfg) {
		t.Fatal("a boolean false must disable the battery saver")
	}
	cfg.Variables.Custom["timeOfDayTheme"] = "true"
	if !timeOfDayThemeEnabled(cfg) {
		t.Fatal("time-of-day theme did not read its enabling value")
	}
}

func TestTimeOfDayPresetFor(t *testing.T) {
	cases := map[int]string{
		0: "oled", 4: "oled", 5: "sunset", 6: "sunset", 7: "light",
		12: "light", 18: "light", 19: "midnight", 21: "midnight", 22: "oled", 23: "oled",
	}
	for hour, want := range cases {
		if got := timeOfDayPresetFor(hour); got != want {
			t.Errorf("hour %02d: got %q, want %q", hour, got, want)
		}
		if _, ok := themePresets[want]; !ok {
			t.Errorf("hour %02d maps to %q, which is not a theme preset", hour, want)
		}
	}
}

// The widget settings must live on their own page with the right defaults,
// the entry button must appear once in General, and running the injection
// twice must not duplicate anything.
func TestEnsureSettingsWidgets(t *testing.T) {
	cfg := widgetTestConfig()
	cfg.Scenes = []SceneConfig{{
		Name: "SettingsGeneral",
		Elements: []Element{
			{Type: "toggle", Text: "Fullscreen", Variable: "fullscreen", X: 40, Y: 96, Height: "48"},
			{Type: "button", Text: "Save", X: 1040, Y: 592, Width: "216", Height: "48"},
		},
	}}

	ensureSettingsWidgets(cfg)
	ensureSettingsWidgets(cfg) // idempotent

	smart := sceneByName(cfg, smartSettingsScene)
	if smart == nil {
		t.Fatalf("%s scene was not created", smartSettingsScene)
	}

	wantDefaults := map[string]string{"autoStart": "false", "batterySaver": "true", "timeOfDayTheme": "false", "lowBatteryGuard": "true"}
	for variable, wantDefault := range wantDefaults {
		count := 0
		var found Element
		for _, el := range smart.Elements {
			if el.Variable == variable {
				count++
				found = el
			}
		}
		if count != 1 {
			t.Fatalf("%s: %d toggle rows, want exactly 1", variable, count)
		}
		if found.Type != "toggle" {
			t.Errorf("%s: row type %q, want toggle", variable, found.Type)
		}
		if found.TriggerValue != wantDefault {
			t.Errorf("%s: default %q, want %q", variable, found.TriggerValue, wantDefault)
		}
		if strings.TrimSpace(found.Text) == "" {
			t.Errorf("%s: row has no label", variable)
		}
		// The rows must fit the 720px panel with room for the frame footer.
		if found.Y < 96 || found.Y+52 > 660 {
			t.Errorf("%s: row at y=%d does not fit the page", variable, found.Y)
		}
	}

	// One entry button in General, pointing at the page.
	scene := settingsGeneralScene(cfg)
	entries := 0
	for _, el := range scene.Elements {
		if el.TriggerTarget == smartSettingsScene {
			entries++
			if el.Type != "button" {
				t.Errorf("entry type %q, want button", el.Type)
			}
			if !strings.Contains(el.Trigger, smartSettingsScene) {
				t.Errorf("entry trigger %q does not name the page", el.Trigger)
			}
		}
	}
	if entries != 1 {
		t.Fatalf("%d entry buttons in General, want exactly 1", entries)
	}
	// It must not sit on top of the existing rows or the Save button.
	for _, el := range scene.Elements {
		if el.TriggerTarget != smartSettingsScene {
			continue
		}
		if el.X+280 > 1040 || el.Y < 488 {
			t.Errorf("entry button at %d,%d is outside the free area of General", el.X, el.Y)
		}
	}

	// A config without Settings → General, or one that already ships the page,
	// must not panic or duplicate.
	ensureSettingsWidgets(widgetTestConfig())
	cfg2 := widgetTestConfig()
	cfg2.Scenes = []SceneConfig{
		{Name: "SettingsGeneral", Elements: []Element{{Type: "button", TriggerTarget: smartSettingsScene}}},
		{Name: smartSettingsScene, Elements: []Element{{Type: "toggle", Variable: "batterySaver"}}},
	}
	ensureSettingsWidgets(cfg2)
	if n := len(cfg2.Scenes); n != 2 {
		t.Fatalf("re-injected a shipped page: %d scenes, want 2", n)
	}
	if n := len(cfg2.Scenes[0].Elements); n != 1 {
		t.Fatalf("duplicated the entry button: %d elements, want 1", n)
	}
}

// The page is only useful if a controller can reach and flip the toggles: this
// builds the real focus graph and navigates it with the D-pad actions.
func TestSmartWidgetsPageIsNavigable(t *testing.T) {
	// Stale chrome rects from an earlier render would add top-bar nodes to the
	// graph; the focus graph is rebuilt from live rects in the app.
	prevBack, prevStart, prevClock, prevBell := sceneTitleBackRect, tb.startRect, statusClockRect, nc.bellRect
	sceneTitleBackRect, tb.startRect, statusClockRect, nc.bellRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	t.Cleanup(func() {
		sceneTitleBackRect, tb.startRect, statusClockRect, nc.bellRect = prevBack, prevStart, prevClock, prevBell
	})

	cfg := widgetTestConfig()
	cfg.Scenes = []SceneConfig{{Name: "SettingsGeneral", Elements: []Element{
		{Type: "button", Text: "Save", X: 1040, Y: 592, Width: "216", Height: "48"},
	}}}
	ensureSettingsWidgets(cfg)

	smart := sceneByName(cfg, smartSettingsScene)
	engine := NewFocusEngine()
	engine.SetGraph(smartSettingsScene, engine.BuildGraph(*smart))

	var toggleIdx []int
	for _, node := range engine.graph.Nodes {
		if node.Type == "toggle" {
			toggleIdx = append(toggleIdx, node.Index)
		}
	}
	if len(toggleIdx) != 4 {
		t.Fatalf("%d toggles are focusable on the page, want 4", len(toggleIdx))
	}

	// Down the list, and the focused element must follow.
	if !engine.SetByElementIndex(toggleIdx[0]) {
		t.Fatal("could not focus the first toggle")
	}
	for i := 1; i < len(toggleIdx); i++ {
		if got := engine.Navigate(ActionNavigateDown); got != toggleIdx[i] {
			t.Fatalf("down from toggle %d focused element %d, want %d", toggleIdx[i-1], got, toggleIdx[i])
		}
	}

	// And the entry button in General is part of that scene's focus graph.
	general := settingsGeneralScene(cfg)
	entryIdx := -1
	for i, el := range general.Elements {
		if el.TriggerTarget == smartSettingsScene {
			entryIdx = i
		}
	}
	if entryIdx < 0 {
		t.Fatal("no entry button to focus")
	}
	entryEngine := NewFocusEngine()
	entryEngine.SetGraph("SettingsGeneral", entryEngine.BuildGraph(*general))
	if !entryEngine.SetByElementIndex(entryIdx) {
		t.Fatal("the Smart Widgets entry button is not focusable")
	}
}

// A theme the user picks by hand must survive the automatic switcher until the
// time of day moves on, and the switch must land on the new bucket's preset.
func TestTimeOfDayThemeRespectsManualChoice(t *testing.T) {
	t.Chdir(t.TempDir()) // applyThemePreset persists jukauser.json
	prevBucket := lastTimeOfDayBucket
	lastTimeOfDayBucket = -1
	t.Cleanup(func() { lastTimeOfDayBucket = prevBucket })

	cfg := widgetTestConfig()
	cfg.Variables.Custom["timeOfDayTheme"] = "true"
	noon := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if got := applyTimeOfDayTheme(cfg, noon); got != "light" {
		t.Fatalf("noon applied %q, want light", got)
	}

	// The user overrides it by hand; the same hour must leave that alone.
	applyThemePreset(cfg, "dark", false)
	if got := applyTimeOfDayTheme(cfg, noon.Add(45*time.Minute)); got != "" {
		t.Fatalf("manual theme was reverted by the automatic switch (%q)", got)
	}
	if got := themePresetName(cfg); got != "dark" {
		t.Fatalf("theme = %q, want the user's dark", got)
	}

	// Evening arrives: the switch takes over again.
	if got := applyTimeOfDayTheme(cfg, time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)); got != "midnight" {
		t.Fatalf("evening applied %q, want midnight", got)
	}
	if got := themePresetName(cfg); got != "midnight" {
		t.Fatalf("theme = %q, want midnight", got)
	}

	// Disabled feature: nothing happens at all.
	cfg.Variables.Custom["timeOfDayTheme"] = "false"
	lastTimeOfDayBucket = -1
	if got := applyTimeOfDayTheme(cfg, time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)); got != "" {
		t.Fatalf("disabled feature applied %q", got)
	}
}

// The saver temporarily overrides motion, low-power and the theme, and hands
// the user's own values back when it releases.
func TestBatterySaverPreservesUserSettings(t *testing.T) {
	t.Chdir(t.TempDir()) // applyThemePreset persists jukauser.json
	prevDelay := userConfigSaveDelay
	userConfigSaveDelay = time.Hour
	t.Cleanup(func() {
		userConfigSaveDelay = prevDelay
		batterySaverActive = false
		batterySaverSaved = batterySaverSnapshot{}
	})

	cfg := widgetTestConfig()
	cfg.Variables.Custom["theme_preset"] = "light"
	cfg.Variables.ReducedMotion = false
	cfg.Variables.LowPower = false

	applyBatterySaver(cfg, true, 9)
	if !batterySaverState() {
		t.Fatal("saver did not engage")
	}
	if !cfg.Variables.ReducedMotion || !cfg.Variables.LowPower {
		t.Fatalf("saver did not enable both efficiency settings: %+v", cfg.Variables)
	}
	if got := themePresetName(cfg); got != "oled" {
		t.Fatalf("saver theme = %q, want oled", got)
	}

	applyBatterySaver(cfg, false, 90)
	if batterySaverState() {
		t.Fatal("saver did not release")
	}
	if cfg.Variables.ReducedMotion || cfg.Variables.LowPower {
		t.Fatalf("user efficiency settings were not restored: %+v", cfg.Variables)
	}
	if got := themePresetName(cfg); got != "light" {
		t.Fatalf("theme after release = %q, want the user's light", got)
	}
}

// Enabling autostart writes exactly one launcher and disabling removes it; the
// launcher points at this executable.
func TestAutoStartLauncherLifecycle(t *testing.T) {
	dir := t.TempDir()
	exe := "/opt/jukahub/JukaHub"
	prevDir, prevExe := autostartDirFn, autostartExeFn
	autostartDirFn = func() (string, error) { return dir, nil }
	autostartExeFn = func() (string, error) { return exe, nil }
	t.Cleanup(func() { autostartDirFn, autostartExeFn = prevDir, prevExe })

	if autoStartInstalled() {
		t.Fatal("a fresh startup directory already reports a launcher")
	}
	if err := setAutoStart(true); err != nil {
		t.Fatalf("setAutoStart(true): %v", err)
	}
	path, err := autoStartLauncherPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("launcher not written: %v", err)
	}
	if !strings.Contains(string(data), exe) {
		t.Fatalf("launcher does not point at the executable:\n%s", data)
	}
	if !autoStartInstalled() {
		t.Fatal("launcher written but autoStartInstalled() is false")
	}

	if err := setAutoStart(false); err != nil {
		t.Fatalf("setAutoStart(false): %v", err)
	}
	if autoStartInstalled() {
		t.Fatal("launcher still present after disabling autostart")
	}
	// Disabling twice is not an error: the toggle must be safe to flip.
	if err := setAutoStart(false); err != nil {
		t.Fatalf("second disable: %v", err)
	}
}

// Flipping the toggle in the UI must reach the platform code, not just store a
// boolean: this is the wiring between the element and setAutoStart.
func TestToggleVariableRunsAutoStartSideEffect(t *testing.T) {
	if !autostartSupported() {
		t.Skip("this device manages startup apps itself")
	}
	t.Chdir(t.TempDir()) // syncVariableOverrides persists jukauser.json
	prevDelay := userConfigSaveDelay
	userConfigSaveDelay = time.Hour
	dir := t.TempDir() // one stable directory: each call must resolve to it
	prevDir, prevExe := autostartDirFn, autostartExeFn
	autostartDirFn = func() (string, error) { return dir, nil }
	autostartExeFn = func() (string, error) { return "/opt/JukaHub", nil }
	t.Cleanup(func() {
		userConfigSaveDelay = prevDelay
		autostartDirFn, autostartExeFn = prevDir, prevExe
	})

	cfg := widgetTestConfig()
	el := Element{Type: "toggle", Text: "Start JukaHub at login", Variable: "autoStart", TriggerValue: "false"}

	toggleVariable(cfg, el)
	if got := inputVariableValue(cfg, "autoStart"); got != "true" {
		t.Fatalf("toggle stored %q, want true", got)
	}
	if !autoStartInstalled() {
		t.Fatal("flipping the toggle did not install the launcher")
	}

	toggleVariable(cfg, el)
	if got := inputVariableValue(cfg, "autoStart"); got != "false" {
		t.Fatalf("second flip stored %q, want false", got)
	}
	if autoStartInstalled() {
		t.Fatal("second flip did not remove the launcher")
	}
}

func TestAutoStartLauncherFormat(t *testing.T) {
	win := autoStartLauncherFor("windows", `C:\Games\JukaHub.exe`)
	if !strings.HasPrefix(win, "@echo off") || !strings.Contains(win, `"C:\Games\JukaHub.exe"`) {
		t.Fatalf("windows launcher looks wrong: %q", win)
	}
	unix := autoStartLauncherFor("linux", "/mnt/SD card/JukaHub")
	for _, want := range []string{"[Desktop Entry]", "Type=Application", `Exec="/mnt/SD card/JukaHub"`, "Terminal=false"} {
		if !strings.Contains(unix, want) {
			t.Fatalf("desktop entry missing %q:\n%s", want, unix)
		}
	}
	if path, _ := autoStartLauncherPathFor("linux"); !strings.HasSuffix(path, ".desktop") {
		t.Fatalf("linux launcher path = %q, want a .desktop file", path)
	}
	if path, _ := autoStartLauncherPathFor("windows"); !strings.HasSuffix(path, ".cmd") {
		t.Fatalf("windows launcher path = %q, want a .cmd file", path)
	}
}

// The Exec field must not be shell-quoted: freedesktop readers do not strip
// single quotes, and unescaped percent signs are field codes.
func TestDesktopExecQuoting(t *testing.T) {
	if got := quoteDesktopExecArg(`/opt/100% Juka/$HOME`); got != `"/opt/100%% Juka/\$HOME"` {
		t.Fatalf("Exec quoting = %s", got)
	}
	if got := quoteDesktopExecArg(`/a"b`); got != `"/a\"b"` {
		t.Fatalf("quote escaping = %s", got)
	}
}
