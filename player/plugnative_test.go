package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The world clock's POSIX rules must agree with the real zones on both sides
// of each daylight-saving switch.
func TestPlugWorldClockDST(t *testing.T) {
	cases := []struct {
		city string
		utc  time.Time
		want string
	}{
		// New York: EDT until 2026-11-01 06:00 UTC, EST after.
		{"New York", time.Date(2026, 11, 1, 5, 59, 0, 0, time.UTC), "01:59"},
		{"New York", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), "01:00"},
		{"New York", time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), "03:00"},
		// Berlin: CEST from 2026-03-29 01:00 UTC to 2026-10-25 01:00 UTC.
		{"Berlin", time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC), "01:59"},
		{"Berlin", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC), "03:00"},
		{"Berlin", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), "02:00"},
		// London: BST from 2026-03-29 01:00 UTC.
		{"London", time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), "13:00"},
		{"London", time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC), "12:00"},
		// Sydney: AEDT (summer) in January, AEST in July.
		{"Sydney", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "11:00"},
		{"Sydney", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), "10:00"},
		// AEDT ends 2026-04-05 03:00 local = 2026-04-04 16:00 UTC.
		{"Sydney", time.Date(2026, 4, 4, 15, 59, 0, 0, time.UTC), "02:59"},
		{"Sydney", time.Date(2026, 4, 4, 16, 0, 0, 0, time.UTC), "02:00"},
		{"Tokyo", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), "09:00"},
		{"Los Angeles", time.Date(2026, 7, 1, 19, 0, 0, 0, time.UTC), "12:00"},
	}
	for _, c := range cases {
		if got := plugTZZones[c.city].In(c.utc).Format("15:04"); got != c.want {
			t.Errorf("%s at %s = %s, want %s", c.city, c.utc.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestPlugVersionLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.1.0", true},
		{"1.1.0", "1.1.0", false},
		{"1.2.0", "1.10.0", true},
		{"1.10", "1.9.9", false},
		{"", "1.0.0", true},
		{"2", "1.9", false},
	} {
		if got := plugVersionLess(c.a, c.b); got != c.want {
			t.Errorf("plugVersionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPlugQuickAndKnownActions(t *testing.T) {
	acts := []plugAction{{ID: "now", Label: "Time"}, {ID: "both", Label: "Date and time"}}
	if q := plugQuickActions(acts, nil); len(q) != 1 || q[0].ID != "now" {
		t.Fatalf("default quick = %+v, want the first action", q)
	}
	if q := plugQuickActions(acts, []string{"both", "missing"}); len(q) != 1 || q[0].ID != "both" {
		t.Fatalf("named quick = %+v, want just both", q)
	}
	if plugQuickActions(nil, []string{"x"}) != nil {
		t.Fatal("a plugin without actions has no quick actions")
	}
	if plugKnownAction(acts, "both") != "both" || plugKnownAction(acts, "nope") != "" {
		t.Fatal("plugKnownAction must only accept the plugin's own actions")
	}
}

func TestPlugNativeNotes(t *testing.T) {
	dir := t.TempDir()
	if out, _ := plugNativeNotes("last", nil, dir); out != "No notes yet" {
		t.Fatalf("empty pad: %q", out)
	}
	if _, err := plugNativeNotes("add", map[string]interface{}{"notes_text": "  buy   milk "}, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := plugNotesAdd(dir, "call home"); err != nil {
		t.Fatal(err)
	}
	if out, _ := plugNativeNotes("count", nil, dir); out != "2 notes saved" {
		t.Fatalf("count = %q", out)
	}
	if out, _ := plugNativeNotes("last", nil, dir); !strings.HasSuffix(out, "call home") {
		t.Fatalf("last = %q", out)
	}
	if _, err := plugNotesAdd(dir, "   "); err == nil {
		t.Fatal("an empty note must be refused")
	}
	if out, _ := plugNativeNotes("clear", nil, dir); out != "Notes cleared" {
		t.Fatalf("clear = %q", out)
	}
	if out, _ := plugNativeNotes("count", nil, dir); out != "No notes yet" {
		t.Fatalf("after clear = %q", out)
	}
}

func TestPlugNativeGamePick(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"SNES/Mario.sfc", "SNES/Imgs/Mario.png", "SNES/Mario.srm",
		"PS/Game One/disc.cue", "GB/.hidden.gb", "GB/Tetris.gb",
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]interface{}{"gamepick_dir": root}
	if out, _ := plugNativeGamePick("count", vars, ""); out != "3 games on the card" {
		t.Fatalf("count = %q", out)
	}
	out, err := plugNativeGamePick("pick", vars, "")
	if err != nil || !strings.HasPrefix(out, "Try: ") {
		t.Fatalf("pick = %q, %v", out, err)
	}
	ok := false
	for _, want := range []string{"Try: Mario (SNES)", "Try: disc (PS)", "Try: Tetris (GB)"} {
		ok = ok || out == want
	}
	if !ok {
		t.Fatalf("pick chose %q", out)
	}
}

func TestPlugNativeDiceAndQuote(t *testing.T) {
	for i := 0; i < 50; i++ {
		out, err := plugNativeDice("roll", map[string]interface{}{"dice_sides": 3}, "")
		if err != nil || !(out == "Rolled 1 of 3" || out == "Rolled 2 of 3" || out == "Rolled 3 of 3") {
			t.Fatalf("roll = %q, %v", out, err)
		}
	}
	if _, err := plugNativeDice("roll", map[string]interface{}{"dice_sides": 1}, ""); err == nil {
		t.Fatal("one side must be refused")
	}
	if out, _ := plugNativeDice("pick", map[string]interface{}{"dice_list": "only"}, ""); out != "Picked: only" {
		t.Fatalf("pick = %q", out)
	}
	day := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if plugQuoteOfDay(day) != plugQuotes[1%len(plugQuotes)] || plugQuoteOfDay(day) != plugQuoteOfDay(day.Add(6*time.Hour)) {
		t.Fatal("the quote of the day must follow the day of the year")
	}
	if _, err := plugNativeQuote("nope", nil, ""); err == nil {
		t.Fatal("an unknown action must be an error")
	}
}

// Every action a bundled manifest lists must be handled: by the Go version,
// or by JukaHub itself (an "@" or main-thread action).
func TestPlugBundledActionsAllHandled(t *testing.T) {
	for _, name := range plugBundledNames() {
		data, err := plugBundleFS.ReadFile("plugins/" + name + "/plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		var info PluginInfo
		if err := json.Unmarshal(data, &info); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		info.Name = name
		fn := plugNativeFor(info)
		if fn == nil {
			t.Errorf("%s has no Go version", name)
			continue
		}
		for _, a := range info.Actions {
			if plugUIActionFor(info, a.ID) != nil {
				continue
			}
			if strings.HasPrefix(a.ID, "@") {
				t.Errorf("%s: JukaHub has no action %s", name, a.ID)
				continue
			}
			out := plugRunNative(fn, a.ID, map[string]interface{}{}, t.TempDir())
			if strings.HasPrefix(out, "Plugin error:") {
				t.Errorf("%s/%s: %s", name, a.ID, out)
			}
		}
		for _, id := range append([]string{info.OnEnable, info.OnStartup}, info.Quick...) {
			if id != "" && plugKnownAction(plugCleanActions(info.Actions), id) == "" {
				t.Errorf("%s: hook names %q, which is not one of its actions", name, id)
			}
		}
	}
}
