package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiagnosticsTextRedactsConfigValues(t *testing.T) {
	withTempPatchState(t) // DiagnosticsText creates patch/ state folders
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

// The bug report carries the redacted diagnostics and the user's words, and
// nothing that must stay on the device; the phone path's issue link is short
// enough for a QR code and still names the build.
func TestBugReportBundlesRedactedDiagnostics(t *testing.T) {
	withTempPatchState(t) // DiagnosticsText creates patch/ state folders
	config := &Config{
		Version:        "1.2.3",
		ChannelProfile: ChannelProfileConfig{Token: "channel-secret"},
		Variables:      Variables{Custom: map[string]interface{}{"apiKey": "sk-live-secret"}},
	}
	form := bugrepForm{
		Category:    0,
		Title:       `Crash opening C:\Users\Alice\Videos\clip.mp4`,
		Description: "It froze. token=abc123def456 at 192.168.1.20 — mail me at alice@example.com",
		Diagnostics: true,
	}
	r := bugrepBuild(config, form, time.Now())
	if r.Category != "crash" || r.Kind != "bug_report" || !bugrepRefValid(r.ID) {
		t.Fatalf("report header = %+v", r)
	}
	if !strings.Contains(r.Diagnostics, "1.2.3") {
		t.Fatalf("diagnostics missing from the report:\n%s", r.Diagnostics)
	}
	whole := r.Title + "\n" + r.Description + "\n" + r.Diagnostics + "\n" + r.Log
	for _, secret := range []string{"channel-secret", "sk-live-secret", "abc123def456", "192.168.1.20", "alice@example.com", `\Users\Alice`} {
		if strings.Contains(whole, secret) {
			t.Fatalf("the report leaked %q:\n%s", secret, whole)
		}
	}

	raw := bugrepIssueURL(r, 900)
	if len(raw) > 900 {
		t.Fatalf("issue link is %d bytes, want it to fit the QR budget", len(raw))
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "github.com" || !strings.HasPrefix(parsed.Path, "/jukaLang/JukaHubV2/issues/new") {
		t.Fatalf("unexpected issue URL: %s", raw)
	}
	if !strings.Contains(parsed.Query().Get("title"), "[Bug]") {
		t.Fatalf("issue title was not prefilled: %q", parsed.Query().Get("title"))
	}
	if body := parsed.Query().Get("body"); !strings.Contains(body, "1.2.3") || !strings.Contains(body, r.ID) {
		t.Fatalf("issue body lost the build or the reference:\n%s", body)
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

func TestRedactDiagnosticTextRemovesCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"token assignment", "discord token=abc123, retrying", "discord token=[redacted], retrying"},
		{"api key with colon", "groq api_key: sk-live-8891", "groq api_key: [redacted]"},
		{"password field", `login password="hunter2" failed`, "login password=[redacted] failed"},
		{"bearer header", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9", "Authorization: [redacted]"},
		{"bare bearer", "sent bearer abcdefghijkl", "sent bearer [redacted]"},
		{"url userinfo", "GET https://media:secret@example.org/a.mp4", "GET https://[credentials]@example.org/a.mp4"},
		{"unix home", "open /home/stepan/.config/jukauser.json", "open ~/.config/jukauser.json"},
		{"mac home", "open /Users/alice/Library/x", "open ~/Library/x"},
		{"windows home", `open C:\Users\stepan\AppData\Roaming\x`, `open C:\Users\[user]\AppData\Roaming\x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactDiagnosticText(tt.in); got != tt.want {
				t.Errorf("redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactDiagnosticTextLeavesDiagnosticsAlone(t *testing.T) {
	// Ordinary support lines must survive untouched, or the tail stops being
	// useful exactly when it matters.
	for _, line := range []string{
		"[netplay] listening on port 55435",
		"[startup] splash 750ms, first frame 1.9s",
		"error: could not start RetroArch: executable file not found",
		"[wifi] wlan0 obtained 192.168.1.24",
	} {
		if got := redactDiagnosticText(line); got != line {
			t.Errorf("redact(%q) = %q, want it unchanged", line, got)
		}
	}
}
