package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

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
		{"netspeed_text", "Speed test not started.", "Run"},
		{"benchmark_text", "Running benchmark…", "benchmark"},
		{"cron_text", "Scheduled tasks not loaded.", "scheduled"},
		{"startup_text", "Startup items not loaded.", "startup"},
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

func TestTaskSchedulerCSV(t *testing.T) {
	rows := []string{`"\Example, quoted","11/2/2026 11:30 PM","Ready"`}
	for i := 0; i < 11; i++ {
		rows = append(rows, fmt.Sprintf(`"task%d","N/A","Disabled"`, i))
	}
	got, err := formatWindowsTasks([]byte(strings.Join(rows, string(rune(10)))))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Example, quoted", "11/2/2026 11:30 PM", "Ready", "12 tasks; first entries shown"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "task10") {
		t.Error("display did not limit rows")
	}
	if got, err := formatWindowsTasks(nil); err != nil || !strings.Contains(got, "No scheduled tasks") {
		t.Fatalf("empty result = %q, %v", got, err)
	}
	if _, err := formatWindowsTasks([]byte(`"broken","row"`)); err == nil {
		t.Fatal("malformed row accepted")
	}
}

func TestTasksRefreshButton(t *testing.T) {
	for path, config := range loadAuditConfigs(t) {
		ensureTasksRefreshLabel(config)
		ensureTasksRefreshLabel(config)
		scene := sceneByName(config, "Cron")
		if scene == nil {
			t.Fatalf("%s: missing Tasks scene", path)
		}
		found := false
		for _, elem := range scene.Elements {
			if elem.Trigger == "cron_load" {
				found = true
				if elem.Text != "Refresh" {
					t.Errorf("%s: refresh button says %q", path, elem.Text)
				}
			}
		}
		if !found {
			t.Errorf("%s: no refresh action", path)
		}
	}
	if runtime.GOOS != "windows" {
		return
	}
	drainCustomForTest()
	prev := windowsTaskQuery
	t.Cleanup(func() { windowsTaskQuery = prev; drainCustomForTest() })
	windowsTaskQuery = func(context.Context) ([]byte, error) { return []byte(`"example","tomorrow","Ready"`), nil }
	enterSceneWork("Cron")
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	handleTrigger(nil, config, Element{Type: "button", Trigger: "cron_load"})
	waitForSceneWork()
	drainCustom(config)
	if text, _ := config.Variables.Custom["cron_text"].(string); !strings.Contains(text, "example | tomorrow | Ready") {
		t.Fatalf("task panel = %q", text)
	}
	if text, _ := config.Variables.Custom[cronStartupVar].(string); !strings.Contains(text, "not available") {
		t.Fatalf("startup panel = %q", text)
	}
	windowsTaskQuery = func(context.Context) ([]byte, error) { return nil, errors.New("access denied") }
	handleTrigger(nil, config, Element{Type: "button", Trigger: "cron_load"})
	waitForSceneWork()
	drainCustom(config)
	if text, _ := config.Variables.Custom["cron_text"].(string); !strings.Contains(text, "access denied") {
		t.Fatalf("error not reported: %q", text)
	}
}
