package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPlainReleaseNotes(t *testing.T) {
	body := "## What's new\n\n" +
		"- **Sharing**: share a theme with a link\n" +
		"* Fixed a crash on [issue 12](https://github.com/jukaLang/JukaHubV2/issues/12)\n" +
		"<!-- internal note -->\n" +
		"<details><summary>More</summary>\n\n" +
		"`code` stays readable\n"
	lines := plainReleaseNotes(body)
	joined := strings.Join(lines, "\n")
	for _, unwanted := range []string{"##", "**", "](", "<!--", "<details>", "<summary>", "`"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("markdown %q survived: %q", unwanted, joined)
		}
	}
	if !strings.Contains(joined, "Sharing: share a theme with a link") {
		t.Fatalf("bullet text missing: %q", joined)
	}
	if !strings.Contains(joined, "Fixed a crash on issue 12") {
		t.Fatalf("link text was not unwrapped: %q", joined)
	}
	if strings.Contains(joined, "internal note") {
		t.Fatalf("HTML comment leaked: %q", joined)
	}
}

func TestPlainReleaseNotesEmpty(t *testing.T) {
	if lines := plainReleaseNotes("  \n\n <!-- only a comment -->\n"); len(lines) != 0 {
		t.Fatalf("empty notes produced %v", lines)
	}
}

func TestChangelogDue(t *testing.T) {
	cases := []struct {
		installed, seen string
		want            bool
	}{
		{"", "", false},
		{"v1", "", true},
		{"v1", "v1", false},
		{" v1 ", "v1", false},
		{"v2", "v1", true},
	}
	for _, c := range cases {
		if got := changelogDue(c.installed, c.seen); got != c.want {
			t.Errorf("changelogDue(%q,%q) = %v, want %v", c.installed, c.seen, got, c.want)
		}
	}
}

func TestDiagnosticsTextRedactsConfigValues(t *testing.T) {
	config := &Config{
		Version:        "9.9.9",
		Author:         "Alice",
		SchemaVersion:  1,
		ChannelProfile: ChannelProfileConfig{Token: "channel-secret"},
		Variables:      Variables{Custom: map[string]interface{}{"apiKey": "sk-live-secret"}},
		Scenes:         []SceneConfig{{Name: "Home", Elements: []Element{{Text: "private title"}}}},
	}
	text := DiagnosticsText(config)
	for _, secret := range []string{"channel-secret", "sk-live-secret", "Alice", "private title"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics leaked %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "9.9.9") || !strings.Contains(text, "1 scene(s), 1 element(s)") {
		t.Fatalf("diagnostics missing build facts:\n%s", text)
	}
}

func TestBugReportURLBundlesRedactedDiagnostics(t *testing.T) {
	config := &Config{
		Version:        "1.2.3",
		ChannelProfile: ChannelProfileConfig{Token: "channel-secret"},
		Variables:      Variables{Custom: map[string]interface{}{"apiKey": "sk-live-secret"}},
	}
	raw := BugReportURL(config)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "github.com" || !strings.HasPrefix(parsed.Path, "/jukaLang/JukaHubV2/issues/new") {
		t.Fatalf("unexpected issue URL: %s", raw)
	}
	body := parsed.Query().Get("body")
	if !strings.Contains(body, "Diagnostics (redacted)") || !strings.Contains(body, "1.2.3") {
		t.Fatalf("diagnostics missing from issue body:\n%s", body)
	}
	for _, secret := range []string{"channel-secret", "sk-live-secret"} {
		if strings.Contains(raw, secret) || strings.Contains(body, secret) {
			t.Fatalf("issue report leaked %q", secret)
		}
	}
	if !strings.Contains(parsed.Query().Get("title"), "Bug") {
		t.Fatalf("issue title was not prefilled: %q", parsed.Query().Get("title"))
	}
}

func TestTelemetryOptedInFrom(t *testing.T) {
	if telemetryOptedInFrom(nil) {
		t.Fatal("nil custom map must default to off")
	}
	if telemetryOptedInFrom(map[string]interface{}{}) {
		t.Fatal("missing key must default to off")
	}
	for _, v := range []interface{}{true, "true", "TRUE", 1} {
		if !telemetryOptedInFrom(map[string]interface{}{telemetryOptInKey: v}) {
			t.Fatalf("value %v should opt in", v)
		}
	}
	for _, v := range []interface{}{false, "false", 0} {
		if telemetryOptedInFrom(map[string]interface{}{telemetryOptInKey: v}) {
			t.Fatalf("value %v should stay off", v)
		}
	}
}

func TestTelemetryPayloadIsDeviceOnly(t *testing.T) {
	config := &Config{
		Version: "1.2.3",
		Author:  "Alice",
		Variables: Variables{Custom: map[string]interface{}{
			"apiKey": "sk-live-secret",
		}},
	}
	payload := telemetryPayload(config, "device", "")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"Alice", "sk-live-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("telemetry leaked %q: %s", secret, text)
		}
	}
	for _, key := range []string{"kind", "version", "device", "platform", "os", "arch", "release"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing %q: %s", key, text)
		}
	}
	if _, ok := payload["crash"]; ok {
		t.Fatalf("device event must not carry a crash field: %s", text)
	}
	crash := telemetryPayload(config, "crash", "panic")
	if crash["crash"] != "panic" {
		t.Fatalf("crash event = %v", crash)
	}
}

func TestTelemetryCrashRespectsConsent(t *testing.T) {
	t.Setenv("JUKAHUB_TELEMETRY_URL", "")
	prevEndpoint, prevCache := telemetryEndpoint, userConfigCache
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["kind"] != "crash" || payload["crash"] != "panic" {
			t.Errorf("unexpected crash payload: %v", payload)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	telemetryEndpoint = srv.URL
	t.Cleanup(func() {
		telemetryEndpoint, userConfigCache = prevEndpoint, prevCache
	})

	userConfigCache = &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{telemetryOptInKey: false}}}
	TelemetryCrash(&Config{Version: "1.0.0"})
	if requests.Load() != 0 {
		t.Fatalf("opted-out crash sent %d request(s)", requests.Load())
	}

	userConfigCache = &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{telemetryOptInKey: true}}}
	TelemetryCrash(&Config{Version: "1.0.0"})
	if requests.Load() != 1 {
		t.Fatalf("opted-in crash sent %d request(s), want 1", requests.Load())
	}
}

func TestPostTelemetry(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := postTelemetry(srv.URL, map[string]interface{}{"kind": "device", "device": "TrimUI Smart Pro"}); err != nil {
		t.Fatal(err)
	}
	if got["kind"] != "device" || got["device"] != "TrimUI Smart Pro" {
		t.Fatalf("server received %v", got)
	}
}

func TestCommunityRowsExposeAllActions(t *testing.T) {
	rows := communityRows(&Config{Version: "1.0.0"})
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want changelog, bug report, telemetry and close", len(rows))
	}
	want := []communityAction{
		communityActionWhatsNew, communityActionReportBug,
		communityActionTelemetry, communityActionClose,
	}
	for i, action := range want {
		if rows[i].action != action {
			t.Fatalf("row %d action = %v, want %v", i, rows[i].action, action)
		}
	}
}

func TestCommunityPanelRender(t *testing.T) {
	prevState, prevDir := patchState, patchStateDir
	patchState = &PatchState{Schema: 1, ReleaseNotesTag: "v2", ReleaseNotesText: "## Fixed\n- Sharing works"}
	patchStateDir = t.TempDir()
	prevOpen, prevPage, prevCursor := communityPanel.open, communityPanel.page, communityPanel.cursor
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	communityPanel.open = true
	communityPanel.page = communityPageHome
	communityPanel.cursor = 0
	t.Cleanup(func() {
		patchState, patchStateDir = prevState, prevDir
		communityPanel.open, communityPanel.page, communityPanel.cursor = prevOpen, prevPage, prevCursor
		screenWidth, screenHeight = prevW, prevH
		communityRowRects = nil
	})
	if font, _ := getCachedFont(&Config{}, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}
	renderer := probeRenderer(t)
	ClearTextCache()
	renderCommunityPanel(renderer, &Config{})
	if communityPanelRect.W <= 0 || communityPanelRect.H <= 0 {
		t.Fatalf("panel was not measured: %+v", communityPanelRect)
	}
	if len(communityRowRects) != 4 {
		t.Fatalf("row rects = %d, want 4", len(communityRowRects))
	}

	communityPanel.page = communityPageChangelog
	renderCommunityPanel(renderer, &Config{})
	if communityPanelRect.H <= 0 {
		t.Fatal("changelog page was not measured")
	}
}

func TestChangelogOpensOnceAfterUpdate(t *testing.T) {
	prevState, prevDir := patchState, patchStateDir
	patchState = &PatchState{
		Schema:              1,
		InstalledReleaseTag: "v42",
		ReleaseNotesTag:     "v42",
		ReleaseNotesText:    "## Fixed\n- Something",
	}
	patchStateDir = t.TempDir()
	prevOpen, prevPage := communityPanel.open, communityPanel.page
	communityPanel.open = false
	t.Cleanup(func() {
		patchState, patchStateDir = prevState, prevDir
		communityPanel.open, communityPanel.page = prevOpen, prevPage
	})

	maybeShowChangelogAfterUpdate(&Config{})
	if !communityPanel.open || communityPanel.page != communityPageChangelog {
		t.Fatalf("changelog did not open: open=%v page=%v", communityPanel.open, communityPanel.page)
	}
	if tag, _ := cachedReleaseNotes(); tag != "v42" {
		t.Fatalf("notes cache = %q", tag)
	}
	patchStateMu.Lock()
	seen := patchState.ChangelogSeenTag
	patchStateMu.Unlock()
	if seen != "v42" {
		t.Fatalf("seen tag = %q, want v42", seen)
	}

	communityPanel.open = false
	communityPanel.page = communityPageHome
	maybeShowChangelogAfterUpdate(&Config{})
	if communityPanel.open {
		t.Fatal("the same release reopened the changelog")
	}
}

func TestStoreReleaseNotes(t *testing.T) {
	prevState, prevDir := patchState, patchStateDir
	patchState = &PatchState{Schema: 1}
	patchStateDir = t.TempDir()
	t.Cleanup(func() {
		patchState, patchStateDir = prevState, prevDir
	})

	storeReleaseNotes(&githubRelease{TagName: "v9", Body: "## Notes\n- Fixed things"})
	tag, notes := cachedReleaseNotes()
	if tag != "v9" || !strings.Contains(notes, "Fixed things") {
		t.Fatalf("notes not cached: tag=%q notes=%q", tag, notes)
	}
	if !strings.Contains(plainReleaseNotes(notes)[0], "Notes") {
		t.Fatalf("cached notes lost their heading: %q", notes)
	}
}
