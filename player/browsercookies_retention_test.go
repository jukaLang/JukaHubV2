package main

// The saved session has an age policy: a day count chosen in Settings →
// General, after which a cookie is no longer restored. These tests pin the
// label and the steps, the restore-time filter, the immediate prune on a
// setting change, and the Settings row itself.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// retentionConfig builds a config that carries the policy, which is what the
// Settings row writes.
func retentionConfig(days int) *Config {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"cookieRetentionDays": days}
	return config
}

// writeCookieFile writes a cookie document the way a previous launch would
// have, so a test can start from records of a known age.
func writeCookieFile(t *testing.T, path string, records []browserCookieRecord) {
	t.Helper()
	data, err := json.Marshal(browserCookieFile{
		SchemaVersion: browserCookieSchemaVersion,
		SavedAt:       time.Now().UTC().Format(time.RFC3339),
		Cookies:       records,
	})
	if err != nil {
		t.Fatalf("marshal cookies: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}
}

// readCookieFile is writeCookieFile's other half.
func readCookieFile(t *testing.T, path string) browserCookieFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cookie file: %v", err)
	}
	var file browserCookieFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode cookie file: %v", err)
	}
	return file
}

func ageInDays(days int) string {
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
}

// TestCookieRetentionStepsAndLabels keeps the row readable and the cycle
// closed: Keep first, then the day counts, wrapping at the end.
func TestCookieRetentionStepsAndLabels(t *testing.T) {
	cases := map[int]string{
		-1: "Keep",
		0:  "Keep",
		1:  "1 day",
		7:  "7 days",
		30: "30 days",
	}
	for days, want := range cases {
		if got := cookieRetentionLabel(days); got != want {
			t.Errorf("cookieRetentionLabel(%d) = %q, want %q", days, got, want)
		}
	}

	if got := nextCookieRetentionDays(0); got != 7 {
		t.Errorf("first step = %d, want 7", got)
	}
	if got := nextCookieRetentionDays(30); got != 90 {
		t.Errorf("after 30 = %d, want 90", got)
	}
	if got := nextCookieRetentionDays(90); got != 0 {
		t.Errorf("the last step wraps to %d, want 0 (Keep)", got)
	}
	if got := nextCookieRetentionDays(45); got != 90 {
		t.Errorf("a hand-edited 45 days moves to %d, want 90", got)
	}
}

// TestCookieRetentionDropsStaleRecordsOnRestore is the enforcement that
// matters: a cookie older than the window is not restored, and the file is
// rewritten without it.
func TestCookieRetentionDropsStaleRecordsOnRestore(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	prev := appConfig
	appConfig = retentionConfig(7)
	t.Cleanup(func() { appConfig = prev })

	writeCookieFile(t, path, []browserCookieRecord{
		{Host: "example.com", Path: "/", Name: "old_session", Value: "a", FirstSeen: ageInDays(30)},
		{Host: "example.com", Path: "/", Name: "fresh", Value: "b", FirstSeen: ageInDays(1)},
		// A file from before the field existed: unknown age, so it is kept and
		// stamped, not dropped.
		{Host: "example.com", Path: "/", Name: "legacy", Value: "c"},
	})

	simulateBrowserRelaunch()
	if got := browserCookiesCount(); got != 2 {
		t.Fatalf("restored %d cookies, want the two within the window", got)
	}
	file := readCookieFile(t, path)
	if len(file.Cookies) != 2 {
		t.Fatalf("the file still holds %d cookies", len(file.Cookies))
	}
	for _, rec := range file.Cookies {
		if rec.Name == "old_session" {
			t.Fatal("the 30-day-old cookie was written back")
		}
		if rec.FirstSeen == "" {
			t.Fatalf("%q was restored without a first-seen stamp", rec.Name)
		}
	}
}

// TestCookieRetentionKeepRestoresEverything is the shipped default: with no
// policy, age changes nothing.
func TestCookieRetentionKeepRestoresEverything(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	prev := appConfig
	appConfig = retentionConfig(0)
	t.Cleanup(func() { appConfig = prev })

	writeCookieFile(t, path, []browserCookieRecord{
		{Host: "example.com", Path: "/", Name: "old_session", Value: "a", FirstSeen: ageInDays(400)},
		{Host: "example.com", Path: "/", Name: "fresh", Value: "b", FirstSeen: ageInDays(1)},
	})

	simulateBrowserRelaunch()
	if got := browserCookiesCount(); got != 2 {
		t.Fatalf("Keep restored %d of 2 cookies", got)
	}
}

// TestApplyCookieRetentionPrunesImmediately checks the press that picks a
// window: choosing 7 days must drop the 10-day-old session in the live jar now,
// not at the next launch, so the Forget chip's count stays truthful.
func TestApplyCookieRetentionPrunesImmediately(t *testing.T) {
	path := useTempBrowserCookiePath(t)

	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		t.Fatal("no cookie jar")
	}
	stale := browserCookieRecord{
		Host: "example.com", Path: "/", Name: "stale", Value: "a",
		FirstSeen: ageInDays(10),
	}
	fresh := browserCookieRecord{
		Host: "example.com", Path: "/", Name: "fresh", Value: "b",
		FirstSeen: ageInDays(2),
	}
	jar.mu.Lock()
	jar.recs[browserCookieKey(stale)] = stale
	jar.recs[browserCookieKey(fresh)] = fresh
	jar.mu.Unlock()
	// The inner jar is what "does it still send" asks; without this the prune
	// snapshot would treat both as already-expired and the assertion below
	// would pass for the wrong reason.
	jar.mu.Lock()
	jar.jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "stale", Value: "a", Path: "/"},
		{Name: "fresh", Value: "b", Path: "/"},
	})
	jar.mu.Unlock()

	config := retentionConfig(0)
	pruned := applyCookieRetention(config, 7)
	if pruned != 1 {
		t.Fatalf("pruned %d cookies, want 1", pruned)
	}
	if got := browserCookiesCount(); got != 1 {
		t.Fatalf("jar holds %d cookies after the prune, want 1", got)
	}
	if got := config.Variables.Custom["cookieRetentionDays"]; got != 7 {
		t.Fatalf("the setting was not written: %v", got)
	}
	if got := config.Variables.Custom["cookieRetentionLabel"]; got != "7 days" {
		t.Fatalf("the label was not written: %v", got)
	}
	file := readCookieFile(t, path)
	if len(file.Cookies) != 1 || file.Cookies[0].Name != "fresh" {
		t.Fatalf("the file holds %+v, want only the fresh cookie", file.Cookies)
	}
}

// TestEnsureCookieRetentionSettingIsIdempotent pins the Settings row: one row,
// a label that resolves, and no second copy when the injection runs twice.
func TestEnsureCookieRetentionSettingIsIdempotent(t *testing.T) {
	// Building the row now consults the jar for the session's start date, so the
	// suite's shared jar is swapped for an empty one: the seeded value has to be
	// the same on a run where an earlier test left a session behind.
	useTempBrowserCookiePath(t)
	config := retentionConfig(30)
	config.Scenes = []SceneConfig{{Name: "SettingsGeneral"}}
	ensureSettingsWidgets(config)
	ensureSettingsWidgets(config)

	scene := settingsGeneralScene(config)
	if scene == nil {
		t.Fatal("no SettingsGeneral scene")
	}
	rows := 0
	for _, el := range scene.Elements {
		if el.Trigger == "cycle_cookie_retention" {
			rows++
			if !strings.Contains(el.Text, "$cookieRowLabel") {
				t.Errorf("row text %q does not show the current window and session", el.Text)
			}
			// Empty jar: window only, no date to point at.
			if got := substituteVars(el.Text, config.Variables.Custom); got != "Cookies · 30d" {
				t.Errorf("row renders %q, want %q", got, "Cookies · 30d")
			}
		}
	}
	if rows != 1 {
		t.Fatalf("Settings → General has %d cookie rows, want 1", rows)
	}
	if got := config.Variables.Get("cookieRetentionLabel"); got != "30 days" {
		t.Fatalf("resolved label = %q, want 30 days", got)
	}
	if got := substituteVars("Saved cookies · $cookieRetentionLabel", config.Variables.Custom); got != "Saved cookies · 30 days" {
		t.Fatalf("the row renders %q", got)
	}
}

// TestCookieRetentionRowRenders checks the row on the real Settings page: the
// label resolves and draws, the row clears the footer, and it does not sit on
// top of another control. The page has no free space left, so this is the test
// that notices when the geometry is chosen badly.
func TestCookieRetentionRowRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := widgetRenderConfig(t)
	frame, scene := renderNamedScene(t, config, "SettingsGeneral")

	var row *Element
	for i := range scene.Elements {
		if scene.Elements[i].Trigger == "cycle_cookie_retention" {
			row = &scene.Elements[i]
			break
		}
	}
	if row == nil {
		t.Fatal("Settings → General has no cookie retention row")
	}
	// The content panel ends 16px above the 48px footer.
	if row.Y+44 > 656 {
		t.Fatalf("row bottom at y=%d runs into the footer", row.Y+44)
	}
	rowW, rowH := resolveElementDimensions(*row, 320, 44)
	for _, other := range scene.Elements {
		if other.Trigger == "cycle_cookie_retention" || other.Type == "label" {
			continue
		}
		otherW, otherH := resolveElementDimensions(other, 320, 44)
		overlapsX := row.X < other.X+otherW && other.X < row.X+rowW
		overlapsY := row.Y < other.Y+otherH && other.Y < row.Y+rowH
		if overlapsX && overlapsY {
			t.Fatalf("row overlaps %q (%s) at %d,%d", other.Text, other.Type, other.X, other.Y)
		}
	}
	if got := brightPixelsIn(frame, *row, config); got < 40 {
		t.Fatalf("row drew %d bright pixels: the label is missing or clipped", got)
	}
}

// TestCookieRetentionTriggerCycles drives the row itself, so the switch case is
// covered as the user reaches it.
func TestCookieRetentionTriggerCycles(t *testing.T) {
	useTempBrowserCookiePath(t)
	withScreen(t, 1280, 720)
	prev := appConfig
	config := retentionConfig(0)
	appConfig = config
	t.Cleanup(func() { appConfig = prev })

	click := func() int {
		handleTrigger(nil, config, Element{Type: "button", Trigger: "cycle_cookie_retention"})
		return cookieRetentionDays(config)
	}
	if days := click(); days != 7 {
		t.Fatalf("first press chose %d days, want 7", days)
	}
	if days := click(); days != 14 {
		t.Fatalf("second press chose %d days, want 14", days)
	}
	if got := substituteVars("$cookieRetentionLabel", config.Variables.Custom); got != "14 days" {
		t.Fatalf("the label did not follow the value: %q", got)
	}
}
