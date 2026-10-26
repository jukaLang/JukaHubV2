package main

import "testing"

// containsFold is already provided by vkeyboard_test.go in this package.

// TestTextlogEmptyStateNamesItsOwnSubject pins the empty-state copy for every
// variable-backed textlog. All of these share renderTextLog, which used to
// hardcode "Reading hardware…" for all of them — so the Weather screen, the
// NetSpeed screen and the plugin list each claimed to be reading hardware.
// A panel must never announce a subject it does not have.
func TestTextlogEmptyStateNamesItsOwnSubject(t *testing.T) {
	cases := []struct {
		variable   string
		wantTitle  string
		wantInBody string
	}{
		{"hw_text", "Reading hardware…", "snapshot"},
		{"weather_screen_text", "Loading forecast…", "weather"},
		{"netspeed_text", "Measuring speed…", "sample"},
		{"benchmark_text", "Running benchmark…", "benchmark"},
		{"cron_text", "Nothing scheduled.", "crontab"},
		{"startup_text", "Nothing scheduled.", "crontab"},
		{"plugin_list", "No plugins installed.", "plugins"},
		{"terminal_text", "No output yet.", "command"},
	}
	for _, c := range cases {
		t.Run(c.variable, func(t *testing.T) {
			title, body := textlogEmptyState(c.variable)
			if title != c.wantTitle {
				t.Errorf("title = %q, want %q", title, c.wantTitle)
			}
			// The subject has to appear in the card — the title, the body, or
			// both — so a panel always says what it is waiting for.
			if !containsFold(title, c.wantInBody) && !containsFold(body, c.wantInBody) {
				t.Errorf("empty state %q / %q never mentions %q", title, body, c.wantInBody)
			}
		})
	}
}

// TestWeatherTextlogNeverSaysHardware is the specific regression the Weather
// render exposed: its panel must not carry the Hardware scene's sentence.
func TestWeatherTextlogNeverSaysHardware(t *testing.T) {
	title, body := textlogEmptyState("weather_screen_text")
	if containsFold(title, "hardware") || containsFold(body, "hardware") {
		t.Errorf("Weather's empty state borrows the hardware copy: %q / %q", title, body)
	}
}

// TestUnknownTextlogVariableStillExplains covers a variable added to the config
// without a matching case: it still gets a usable, subject-neutral card.
func TestUnknownTextlogVariableStillExplains(t *testing.T) {
	title, body := textlogEmptyState("brand_new_panel")
	if title == "" || body == "" {
		t.Fatalf("empty state for an unknown variable is blank: %q / %q", title, body)
	}
	if containsFold(title, "hardware") {
		t.Errorf("unknown variable fell back to the hardware copy: %q", title)
	}
}
