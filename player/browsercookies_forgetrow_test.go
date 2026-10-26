package main

// "Forget now" beside the cookie retention row: clearing the saved session from
// Settings, with the same two-press rule the Browser chip uses. These tests
// drive the press itself — the row is just the way in — and check that the jar,
// the file and the confirm window all agree afterwards.

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// forgetRow finds the Settings → General "Forget now" press.
func forgetRow(t *testing.T, config *Config) *Element {
	t.Helper()
	scene := settingsGeneralScene(config)
	if scene == nil {
		t.Fatal("no SettingsGeneral scene")
	}
	for i := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(scene.Elements[i].Trigger), "forget_saved_cookies") {
			return &scene.Elements[i]
		}
	}
	t.Fatal("Settings → General has no Forget now press")
	return nil
}

// settingsWithCookies builds the Settings page and a jar holding two cookies,
// and reports the file the jar writes to.
func settingsWithCookies(t *testing.T) (*Config, string) {
	t.Helper()
	path := useTempBrowserCookiePath(t)
	prev := browserForgetConfirm.armedAt
	t.Cleanup(func() { browserForgetConfirm.armedAt = prev })

	config := &Config{Scenes: []SceneConfig{{Name: "SettingsGeneral"}}}
	config.Variables.Custom = map[string]interface{}{}
	ensureSettingsWidgets(config)

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "session", Value: "abc", Path: "/"},
		{Name: "consent", Value: "yes", Path: "/"},
	})
	return config, path
}

// TestSettingsForgetRowAsksTwiceAndDrops: one press arms (and keeps
// everything), the second drops the jar and the file on the card.
func TestSettingsForgetRowAsksTwiceAndDrops(t *testing.T) {
	config, path := settingsWithCookies(t)
	browserForgetConfirm.armedAt = time.Time{}

	row := forgetRow(t, config)
	if got := substituteVars(row.Text, config.Variables.Custom); got != "Forget now" {
		t.Errorf("the row reads %q, want \"Forget now\"", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the probe has no saved session to drop: %v", err)
	}

	if forgetSavedCookiesFromSettings(config) {
		t.Fatal("the first press dropped the session instead of arming")
	}
	if got := browserCookiesCount(); got != 2 {
		t.Fatalf("the first press left %d cookies, want both still there", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the first press touched the file: %v", err)
	}

	if !forgetSavedCookiesFromSettings(config) {
		t.Fatal("the second press did not drop the session")
	}
	if got := browserCookiesCount(); got != 0 {
		t.Fatalf("%d cookies survived the drop", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file survived the drop (err=%v)", err)
	}

	// With nothing stored the press says so instead of arming.
	if forgetSavedCookiesFromSettings(config) {
		t.Fatal("an empty jar reported a drop")
	}
	if !browserForgetConfirm.armedAt.IsZero() {
		t.Fatal("an empty jar left the confirm window armed")
	}
}

// TestSettingsForgetRowDisarmsAfterTheWindow: a press that is never confirmed
// goes away on its own, so a stray press cannot be completed minutes later.
func TestSettingsForgetRowDisarmsAfterTheWindow(t *testing.T) {
	config, _ := settingsWithCookies(t)

	browserForgetConfirm.armedAt = time.Now().Add(-2 * browserForgetConfirmWindow)
	if forgetSavedCookiesFromSettings(config) {
		t.Fatal("an expired arm still dropped the session")
	}
	if got := browserCookiesCount(); got != 2 {
		t.Fatalf("%d cookies left after an expired arm, want both", got)
	}
	if browserForgetConfirm.armedAt.IsZero() {
		t.Fatal("the press did not re-arm")
	}
}

// TestSettingsForgetSharesTheBrowserWindow: both entry points drop the same
// thing, so an arm from one is honoured by the other inside the window instead
// of asking again.
func TestSettingsForgetSharesTheBrowserWindow(t *testing.T) {
	config, path := settingsWithCookies(t)
	browserForgetConfirm.armedAt = time.Time{}

	if forgetSavedCookiesFromSettings(config) {
		t.Fatal("the settings press dropped without arming")
	}
	// Armed here, pressed there: the Browser's action completes it.
	if !browserForgetCookiesAction(config) {
		t.Fatal("the Browser did not honour the arm from Settings")
	}
	if got := browserCookiesCount(); got != 0 {
		t.Fatalf("%d cookies survived", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file survived (err=%v)", err)
	}
}

// TestSettingsForgetRowIsInjectedOnce keeps the row singular: the page is built
// twice per launch in some paths, and two "Forget now" pills would be a second
// control for one action.
func TestSettingsForgetRowIsInjectedOnce(t *testing.T) {
	// The page's Save button is authored in the shipped JSON; this probe builds
	// the scene by hand, so the anchor the press is placed above is seeded here
	// at the same rect the configs use.
	config := &Config{Scenes: []SceneConfig{{
		Name: "SettingsGeneral",
		Elements: []Element{{
			Type: "button", Text: "Save", Trigger: "save_config",
			X: 1040, Y: 592, Width: "216", Height: "48",
		}},
	}}}
	config.Variables.Custom = map[string]interface{}{}
	ensureSettingsWidgets(config)
	ensureSettingsWidgets(config)

	count := 0
	for _, el := range settingsGeneralScene(config).Elements {
		if el.Trigger == "forget_saved_cookies" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the press was injected %d times, want 1", count)
	}
	// And it stays in the cookie cluster the retention row opens: right edge,
	// one row up, directly above Save. Sharing the retention row's band was
	// tried and does not fit — 360px holds the row's own label (319–332px at
	// this font) and nothing else — so the press sits above it rather than
	// ellipsizing both labels.
	row := forgetRow(t, config)
	scene := settingsGeneralScene(config)
	var retention, save *Element
	for i := range scene.Elements {
		switch scene.Elements[i].Trigger {
		case "cycle_cookie_retention":
			retention = &scene.Elements[i]
		case "save_config":
			save = &scene.Elements[i]
		}
	}
	if retention == nil {
		t.Fatal("no retention row to pair the press with")
	}
	if save == nil {
		t.Fatal("no Save button to anchor the press above")
	}
	rowW, rowH := resolveElementDimensions(*row, 320, 48)
	retW, _ := resolveElementDimensions(*retention, 320, 48)
	saveW, _ := resolveElementDimensions(*save, 320, 48)
	// The press starts where the retention row ends: the two share a band but
	// not a pixel, which is what lets it sit slightly higher without overlap.
	if row.X < retention.X+retW {
		t.Fatalf("the press at x=%d runs into the retention row (ends at %d)", row.X, retention.X+retW)
	}
	if row.Y+rowH > save.Y {
		t.Fatalf("the press bottom at %d reaches Save at %d", row.Y+rowH, save.Y)
	}
	if row.X+rowW != save.X+saveW {
		t.Fatalf("the press ends at %d, Save at %d: they should share the right edge", row.X+rowW, save.X+saveW)
	}
}

// TestSettingsForgetRowRenders draws the real page: the press has to be on
// screen, above the footer, and its whole label has to fit — a "Forget now"
// ellipsized to "Forget…" would read as a different control.
func TestSettingsForgetRowRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := widgetRenderConfig(t)
	frame, scene := renderNamedScene(t, config, "SettingsGeneral")

	var row *Element
	for i := range scene.Elements {
		if scene.Elements[i].Trigger == "forget_saved_cookies" {
			row = &scene.Elements[i]
			break
		}
	}
	if row == nil {
		t.Fatal("Settings → General has no Forget now press")
	}
	// The content panel ends 16px above the 48px footer.
	if row.Y+48 > 656 {
		t.Fatalf("the press bottom at y=%d runs into the footer", row.Y+48)
	}

	font, _ := getCachedFont(config, row.Font)
	if font == nil {
		t.Fatal("no font for the press")
	}
	label := substituteVars(row.Text, config.Variables.Custom)
	if label != "Forget now" {
		t.Fatalf("the press reads %q, want \"Forget now\"", label)
	}
	textW, _, err := font.SizeUTF8(label)
	if err != nil {
		t.Fatalf("cannot measure the label: %v", err)
	}
	boxW, _ := resolveElementDimensions(*row, 320, 48)
	if int32(textW) > boxW-2*SpaceXS {
		t.Fatalf("the label needs %dpx in a %dpx button: it would be ellipsized", textW, boxW)
	}
	if got := brightPixelsIn(frame, *row, config); got < 40 {
		t.Fatalf("the press drew %d bright pixels: the label is missing", got)
	}
}
