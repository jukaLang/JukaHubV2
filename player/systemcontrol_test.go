package main

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests cover the pure half of the Quick Settings control layer: the
// backlight allowlist and scaling, mixer output parsing, radio state parsing and
// the timeout cycle. Nothing here touches the host (no command runs, no sysfs
// write), so the suite stays safe on a developer machine.

func TestFindBacklightPicksValidDevice(t *testing.T) {
	root := t.TempDir()

	// A device with no max_brightness must be skipped, not selected.
	broken := filepath.Join(root, "aaa-broken")
	if err := os.Mkdir(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "brightness"), []byte("10"), 0o644); err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(root, "bbb-good")
	if err := os.Mkdir(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "max_brightness"), []byte("255\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "brightness"), []byte("128\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, max, err := findBacklight(root)
	if err != nil {
		t.Fatalf("findBacklight: %v", err)
	}
	if dir != good {
		t.Fatalf("dir = %q, want %q", dir, good)
	}
	if max != 255 {
		t.Fatalf("max = %d, want 255", max)
	}
}

func TestFindBacklightRejectsEmptyRoot(t *testing.T) {
	if _, _, err := findBacklight(t.TempDir()); err == nil {
		t.Fatal("expected an error for a root with no backlight device")
	}
	if _, _, err := findBacklight(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing root")
	}
}

// A file (not a directory) in the backlight root must never be treated as a
// device: the writer only ever opens "brightness" inside a real child dir.
func TestFindBacklightIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "brightness"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := findBacklight(root); err == nil {
		t.Fatal("expected a plain file to be rejected as a backlight device")
	}
}

func TestBrightnessRawFor(t *testing.T) {
	cases := []struct {
		pct, max, want int
	}{
		{100, 255, 255},
		{50, 255, 128},
		{0, 255, (qsBrightnessFloorPct*255 + 50) / 100}, // floor, never black
		{1, 255, (qsBrightnessFloorPct*255 + 50) / 100},
		{150, 255, 255},
		{100, 0, 0},
		{100, 1, 1},
	}
	for _, c := range cases {
		if got := brightnessRawFor(c.pct, c.max); got != c.want {
			t.Fatalf("brightnessRawFor(%d, %d) = %d, want %d", c.pct, c.max, got, c.want)
		}
	}
}

func TestBrightnessPercentFor(t *testing.T) {
	cases := []struct {
		raw, max, want int
	}{
		{255, 255, 100},
		{128, 255, 50},
		{0, 255, 0},
		{5, 0, 0},
		{999, 255, 100},
	}
	for _, c := range cases {
		if got := brightnessPercentFor(c.raw, c.max); got != c.want {
			t.Fatalf("brightnessPercentFor(%d, %d) = %d, want %d", c.raw, c.max, got, c.want)
		}
	}
}

// A written level must read back as the same percentage, so the slider does not
// drift between what it shows and what the device reports. The raw scale is
// coarse, so one percentage point of slack is allowed.
func TestBrightnessRoundTrip(t *testing.T) {
	for _, max := range []int{100, 255, 1024} {
		for pct := qsBrightnessFloorPct; pct <= 100; pct += 5 {
			raw := brightnessRawFor(pct, max)
			got := brightnessPercentFor(raw, max)
			if diff := got - pct; diff > 1 || diff < -1 {
				t.Fatalf("max %d: %d%% -> raw %d -> %d%%", max, pct, raw, got)
			}
		}
	}
	// A single-step backlight is a switch: every non-zero level is the same
	// hardware state, so only the direction is meaningful there.
	if brightnessRawFor(qsBrightnessFloorPct, 1) != 1 || brightnessRawFor(100, 1) != 1 {
		t.Fatal("a one-step backlight must saturate at 1")
	}
	t.Logf("255-step round trip of 50%% = %d%%", brightnessPercentFor(brightnessRawFor(50, 255), 255))
}

func TestParseFirstPercent(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"Volume: front-left: 65536 / 100% / 0.00 dB", 100, true},
		{"  Front Left: Playback 51 [80%] [-10.00dB] [on]", 80, true},
		{"Mono: Playback 0 [0%] [-51.00dB] [on]", 0, true},
		{"no percentage here", 0, false},
		{"%", 0, false},
	}
	for _, c := range cases {
		got, ok := parseFirstPercent(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("parseFirstPercent(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRfkillState(t *testing.T) {
	cases := []struct {
		in      string
		enabled bool
		ok      bool
	}{
		{"0: phy0: Wireless LAN\n\tSoft blocked: yes\n\tHard blocked: no\n", false, true},
		{"0: phy0: Wireless LAN\n\tSoft blocked: no\n\tHard blocked: no\n", true, true},
		{"", false, false},
		{"0: hci0: Bluetooth\n", false, false},
	}
	for _, c := range cases {
		enabled, ok := rfkillState(c.in)
		if ok != c.ok || (ok && enabled != c.enabled) {
			t.Fatalf("rfkillState(%q) = %v, %v; want %v, %v", c.in, enabled, ok, c.enabled, c.ok)
		}
	}
}

func TestScreenTimeoutLabel(t *testing.T) {
	cases := []struct {
		sec  int
		want string
	}{
		{0, "Off"},
		{15, "15s"},
		{60, "1m"},
		{300, "5m"},
		{1800, "30m"},
		{90, "1m 30s"},
	}
	for _, c := range cases {
		if got := screenTimeoutLabel(c.sec); got != c.want {
			t.Fatalf("screenTimeoutLabel(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func TestNextScreenTimeoutCyclesAndWraps(t *testing.T) {
	if got := nextScreenTimeout(0); got != screenTimeoutSteps[0] {
		t.Fatalf("from 0: %d, want %d", got, screenTimeoutSteps[0])
	}
	if got := nextScreenTimeout(15); got != 25 {
		t.Fatalf("from 15s: %d, want the 25s default step", got)
	}
	// Unknown values (an old config) move to the first step above them.
	if got := nextScreenTimeout(120); got != 300 {
		t.Fatalf("from 120s: %d, want 300", got)
	}
	last := screenTimeoutSteps[len(screenTimeoutSteps)-1]
	if got := nextScreenTimeout(last); got != screenTimeoutSteps[0] {
		t.Fatalf("from the last step: %d, want wrap to %d", got, screenTimeoutSteps[0])
	}
}

// The steps must stay ascending: the cycle and the label mapping both assume it.
func TestScreenTimeoutStepsAreAscending(t *testing.T) {
	for i := 1; i < len(screenTimeoutSteps); i++ {
		if screenTimeoutSteps[i] <= screenTimeoutSteps[i-1] {
			t.Fatalf("steps not ascending at %d: %v", i, screenTimeoutSteps)
		}
	}
}
