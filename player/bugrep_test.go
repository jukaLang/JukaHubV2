package main

// Tests for Report a bug's sending side: where reports may go, what is
// scrubbed before anything leaves the device, and that the preview shows
// exactly what is sent.

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBugrepEndpointPrecedence(t *testing.T) {
	prevBuild, prevTele := bugrepEndpoint, telemetryEndpoint
	t.Cleanup(func() { bugrepEndpoint, telemetryEndpoint = prevBuild, prevTele })
	t.Setenv("JUKAHUB_BUGREPORT_URL", "")
	t.Setenv("JUKAHUB_TELEMETRY_URL", "")
	bugrepEndpoint, telemetryEndpoint = "", ""
	if got := bugrepEndpointFor(nil); got != "" {
		t.Fatalf("no collector is configured, got %q", got)
	}
	telemetryEndpoint = "https://telemetry.example/v1"
	if got := bugrepEndpointFor(nil); got != telemetryEndpoint {
		t.Fatalf("telemetry fallback = %q", got)
	}
	bugrepEndpoint = "https://build.example/bug"
	if got := bugrepEndpointFor(nil); got != bugrepEndpoint {
		t.Fatalf("the build's collector must win over telemetry, got %q", got)
	}
	custom := map[string]interface{}{bugrepConfigKey: "https://config.example/bug"}
	if got := bugrepEndpointFor(custom); got != "https://config.example/bug" {
		t.Fatalf("the config must win over the build, got %q", got)
	}
	t.Setenv("JUKAHUB_BUGREPORT_URL", "https://env.example/bug")
	if got := bugrepEndpointFor(custom); got != "https://env.example/bug" {
		t.Fatalf("the environment must win, got %q", got)
	}
	// A plain-http collector on the network is skipped, not used.
	t.Setenv("JUKAHUB_BUGREPORT_URL", "http://reports.example/bug")
	if got := bugrepEndpointFor(custom); got != "https://config.example/bug" {
		t.Fatalf("an http collector was accepted: %q", got)
	}
	// A config value of the wrong type is ignored.
	if got := bugrepEndpointFor(map[string]interface{}{bugrepConfigKey: 42}); got != bugrepEndpoint {
		t.Fatalf("a non-string config value = %q", got)
	}
}

func TestBugrepEndpointAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://reports.example/v1/bug": true,
		"http://localhost:8080/bug":      true,
		"http://127.0.0.1:9000/bug":      true,
		"http://[::1]:9000/bug":          true,
		"http://reports.example/bug":     false,
		"ftp://reports.example/bug":      false,
		"https://":                       false,
		"reports.example/bug":            false,
		"":                               false,
	} {
		if got := bugrepEndpointAllowed(raw); got != want {
			t.Errorf("bugrepEndpointAllowed(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestBugrepScrubRemovesPersonalData(t *testing.T) {
	cases := []struct {
		in   string
		gone []string
		keep []string
	}{
		{"open http://bob:hunter2@media.example/private/list.m3u8?key=abc", []string{"hunter2", "bob", "private", "abc"}, []string{"media.example"}},
		{"write to alice@example.com", []string{"alice@example.com"}, []string{"write to"}},
		{"wlan0 aa:bb:cc:dd:ee:ff up", []string{"aa:bb:cc:dd:ee:ff"}, []string{"wlan0", "up"}},
		{"peer 10.1.2.3 joined", []string{"10.1.2.3"}, []string{"peer", "joined"}},
		{"dns 8.8.8.8 answered", nil, []string{"8.8.8.8"}},
		{`saved C:\Users\Bob\clip.mp4 ok`, []string{"Bob", "clip.mp4"}, []string{"saved", "ok"}},
		{"missing /mnt/SDCARD/Roms/game.zip now", []string{"SDCARD", "game.zip"}, []string{"missing", "now"}},
		{"config at /home/carol/.config/jukahub", []string{"carol"}, []string{"config at"}},
		{"password: swordfish", []string{"swordfish"}, []string{"password"}},
	}
	for _, c := range cases {
		got := bugrepScrub(c.in)
		for _, g := range c.gone {
			if strings.Contains(got, g) {
				t.Errorf("scrub(%q) = %q still contains %q", c.in, got, g)
			}
		}
		for _, k := range c.keep {
			if !strings.Contains(got, k) {
				t.Errorf("scrub(%q) = %q lost %q", c.in, got, k)
			}
		}
	}
}

func TestBugrepBuildHonoursTheToggles(t *testing.T) {
	withTempPatchState(t) // DiagnosticsText creates patch/ state folders
	config := &Config{Version: "1.2.3"}
	when := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	r := bugrepBuild(config, bugrepForm{Category: 99, Title: "  Hi  "}, when)
	if r.Category != "other" || r.Title != "Hi" || r.Diagnostics != "" || r.Log != "" || r.Created != "2026-11-08T10:00:00Z" {
		t.Fatalf("report with everything off = %+v", r)
	}
	r = bugrepBuild(config, bugrepForm{Category: 2, Diagnostics: true, Log: true}, when)
	if r.Category != "controls" || r.Diagnostics == "" || r.Log == "" {
		t.Fatalf("report with everything on = %+v", r)
	}
	if r.OS != runtime.GOOS || r.Arch != runtime.GOARCH || r.Schema != 1 || r.App != "JukaHub" {
		t.Fatalf("report facts = %+v", r)
	}
}

func TestBugrepPreviewShowsWhatIsSent(t *testing.T) {
	r := bugrepReport{Category: "media", Description: "Audio stutters", Version: "1.2.3", Device: "TrimUI Smart Pro", Platform: "TrimUI", OS: "linux", Arch: "arm64"}
	text := bugrepPreviewText(r)
	for _, want := range []string{"Category: media", "Title: (no title)", "Audio stutters", "JukaHub 1.2.3", "Not included.", "## Never included"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, text)
		}
	}
	r.Release, r.Diagnostics, r.Log = "JukaHub-20261101000000", "fps 60", "[ui] opened"
	text = bugrepPreviewText(r)
	for _, want := range []string{"(JukaHub-20261101000000)", "## Diagnostics (redacted)\nfps 60", "## Recent log (redacted)\n[ui] opened"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, text)
		}
	}
}
