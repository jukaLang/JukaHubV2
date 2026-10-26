package main

// The Settings → General switch for video snaps: the row itself, the value it
// writes, and the gate the library's preview reads. The point of the row is
// that nobody has to hand-edit jukauser.json to stop the preview box moving.

import (
	"strings"
	"testing"
	"time"
)

// snapSettingRow finds the video-snaps row in Settings → General.
func snapSettingRow(t *testing.T, config *Config) *Element {
	t.Helper()
	scene := settingsGeneralScene(config)
	if scene == nil {
		t.Fatal("no SettingsGeneral scene")
	}
	for i := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(scene.Elements[i].Trigger), "toggle_game_snaps") {
			return &scene.Elements[i]
		}
	}
	t.Fatal("Settings → General has no video-snaps row")
	return nil
}

// TestSettingsOffersVideoSnapsSwitch: the row exists once, shows the live state
// rather than a placeholder, and starts from the feature's own default (on).
func TestSettingsOffersVideoSnapsSwitch(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "SettingsGeneral"}}}
	config.Variables.Custom = map[string]interface{}{}
	ensureSettingsWidgets(config)
	ensureSettingsWidgets(config) // idempotent, like every other injected row

	count := 0
	for _, el := range config.Scenes[0].Elements {
		if el.Trigger == "toggle_game_snaps" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the row was injected %d times, want 1", count)
	}

	row := snapSettingRow(t, config)
	if !strings.Contains(row.Text, "$gameSnapsLabel") {
		t.Errorf("row text %q does not read the live state", row.Text)
	}
	if got := substituteVars(row.Text, config.Variables.Custom); got != "Video snaps · On" {
		t.Errorf("row renders %q, want \"Video snaps · On\"", got)
	}

	// A config that already switched the feature off says so before a press.
	config.Variables.Custom[snapPreviewSettingKey] = false
	ensureSettingsWidgets(config)
	if got := substituteVars(snapSettingRow(t, config).Text, config.Variables.Custom); got != "Video snaps · Off" {
		t.Errorf("disabled row renders %q, want \"Video snaps · Off\"", got)
	}
}

// TestVideoSnapsSwitchFlipsAndPersists walks the switch through both states and
// checks the key the library reads, including a key written as a string by an
// older config or a toggle element.
func TestVideoSnapsSwitchFlipsAndPersists(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if !snapPreviewEnabled(config) {
		t.Fatal("an unset key must keep the previews on")
	}
	// It starts on, so the first press is the one that turns it off.
	if on := toggleSnapPreview(config); on {
		t.Fatal("the first press left the previews on")
	}
	if snapPreviewEnabled(config) {
		t.Fatal("the library still sees previews after the row turned them off")
	}
	if got := config.Variables.Custom[snapPreviewLabelKey]; got != "Off" {
		t.Fatalf("label key = %v, want Off", got)
	}
	if got := config.Variables.Custom[snapPreviewSettingKey]; got != false {
		t.Fatalf("setting key = %v, want the bool false", got)
	}
	if !toggleSnapPreview(config) || !snapPreviewEnabled(config) {
		t.Fatal("the second press did not turn the previews back on")
	}

	// A string value - what a hand-edited file or a toggle element writes - is
	// read as a boolean instead of being ignored and left on.
	config.Variables.Custom[snapPreviewSettingKey] = "false"
	if snapPreviewEnabled(config) {
		t.Fatal("the string \"false\" did not switch the previews off")
	}
	config.Variables.Custom[snapPreviewSettingKey] = "true"
	if !snapPreviewEnabled(config) {
		t.Fatal("the string \"true\" did not switch the previews on")
	}
}

// TestVideoSnapsSwitchGatesTheDecoder is the reason the row exists: with the
// switch off, a focused game with a clip on the card starts nothing.
func TestVideoSnapsSwitchGatesTheDecoder(t *testing.T) {
	fake := installSnapFake(t)
	snapTestSetup(t, []libraryGame{snapGame("/roms/nes/A.nes", "A")}, map[string]string{"A": ".mp4"})
	base := time.Now()

	snapPreviewSync(base)
	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 1 })

	// Switching it off stops the decoder that is already running, and nothing
	// restarts while the focus stays put.
	applySnapPreviewEnabled(appConfig, false)
	snapPreviewSync(base.Add(time.Second))
	waitForSnap(t, func() bool { return fake.cancels() == 1 })
	for i := 2; i <= 4; i++ {
		snapPreviewSync(base.Add(time.Duration(i) * time.Second))
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("the off switch started %d more decoders", got-1)
	}

	// And back on: the same focused game is allowed a preview again. One frame
	// re-arms the focus, the next one starts the decoder once the arm delay has
	// passed - exactly the path a first visit to the row takes.
	applySnapPreviewEnabled(appConfig, true)
	snapPreviewSync(base.Add(5 * time.Second))
	snapPreviewSync(base.Add(5*time.Second + snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 2 })
}

// TestVideoSnapsRowFitsTheSettingsPage keeps the new row inside the content
// panel and clear of every other control on the page - the band it was placed
// in is the page's last free one.
func TestVideoSnapsRowFitsTheSettingsPage(t *testing.T) {
	config := layoutTestConfig(t)
	ensureSettingsWidgets(config)
	ensureOnboardingEntry(config)

	row := snapSettingRow(t, config)
	rowW, rowH := resolveElementDimensions(*row, 320, 48)
	if row.X < 24 || row.Y < 24 || row.X+rowW > screenWidth-24 {
		t.Fatalf("row leaves the content panel: %+v (%dx%d)", *row, rowW, rowH)
	}
	// The footer is 48px plus a 16px gap, so a row may not cross y=656.
	if row.Y+rowH > 656 {
		t.Fatalf("row bottom at %d runs into the footer", row.Y+rowH)
	}

	for _, other := range settingsGeneralScene(config).Elements {
		if other.Trigger == row.Trigger || other.Type == "label" || other.Type == "input" {
			continue
		}
		otherW, otherH := resolveElementDimensions(other, 320, 48)
		if row.X < other.X+otherW && other.X < row.X+rowW &&
			row.Y < other.Y+otherH && other.Y < row.Y+rowH {
			t.Fatalf("row overlaps %q (%s) at %d,%d", other.Text, other.Type, other.X, other.Y)
		}
	}
}

// TestVideoSnapsRowRenders draws the page: the row's label has to be on
// screen, not just in the config, and a switched-off feature draws the off
// state in the same place.
func TestVideoSnapsRowRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := widgetRenderConfig(t)
	row := snapSettingRow(t, config)
	f, _ := renderNamedScene(t, config, "SettingsGeneral")
	if got := brightPixelsIn(f, *row, config); got < 40 {
		t.Fatalf("the row drew %d bright pixels: its label is missing", got)
	}

	applySnapPreviewEnabled(config, false)
	f, _ = renderNamedScene(t, config, "SettingsGeneral")
	if got := brightPixelsIn(f, *row, config); got < 40 {
		t.Fatalf("the off state drew %d bright pixels", got)
	}
	if got := substituteVars(row.Text, config.Variables.Custom); got != "Video snaps · Off" {
		t.Fatalf("row renders %q after the switch, want \"Video snaps · Off\"", got)
	}
}

// TestSettingsGeneralControlsDoNotStack is the wider guard the new row needed:
// two controls at the same rect means whichever is drawn first cannot be
// clicked. "Replay tour" used to sit exactly on the screensaver row.
func TestSettingsGeneralControlsDoNotStack(t *testing.T) {
	config := layoutTestConfig(t)
	ensureSettingsWidgets(config)
	ensureOnboardingEntry(config)

	var controls []Element
	for _, el := range settingsGeneralScene(config).Elements {
		if el.Type == "button" || el.Type == "toggle" {
			controls = append(controls, el)
		}
	}
	if len(controls) < 5 {
		t.Fatalf("only %d controls on the page: the probe is not looking at the real scene", len(controls))
	}
	for i := 0; i < len(controls); i++ {
		for j := i + 1; j < len(controls); j++ {
			a, b := controls[i], controls[j]
			aw, ah := resolveElementDimensions(a, 320, 48)
			bw, bh := resolveElementDimensions(b, 320, 48)
			if a.X < b.X+bw && b.X < a.X+aw && a.Y < b.Y+bh && b.Y < a.Y+ah {
				t.Fatalf("%q (%d,%d %dx%d) stacks on %q (%d,%d %dx%d)",
					a.Text, a.X, a.Y, aw, ah, b.Text, b.X, b.Y, bw, bh)
			}
		}
	}
}
