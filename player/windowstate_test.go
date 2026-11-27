package main

import "testing"

func TestWindowRectRoundTrip(t *testing.T) {
	want := windowRect{X: -120, Y: 40, W: 1366, H: 768, Display: 1, Maximized: true, Borderless: false}
	text := formatWindowRect(want)
	got, ok := parseWindowRect(text)
	if !ok || got != want {
		t.Fatalf("round trip %q -> %+v (%v)", text, got, ok)
	}
	for _, bad := range []string{
		"", "-120,40,1366,768,1,true",
		"a,40,1366,768,1,true,false",
		"-120,40,100,768,1,true,false",
		"-120,40,1366,768,-2,true,false",
		"-120,40,1366,768,1,perhaps,false",
	} {
		if _, ok := parseWindowRect(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestWindowRectClamp(t *testing.T) {
	displays := []displayBounds{{X: 0, Y: 0, W: 1920, H: 1080}, {X: -1920, Y: 200, W: 1600, H: 900}}
	r := windowRect{X: -1700, Y: 300, W: 1200, H: 800, Display: 1}
	got, ok := clampWindowRect(r, displays)
	if !ok || got.Display != 1 || got.X != -1700 || got.Y != 300 {
		t.Fatalf("a valid second-monitor rect moved: %+v (%v)", got, ok)
	}

	// Monitor 2 is gone: fall back to the primary and centre there.
	r.Display = 5
	got, ok = clampWindowRect(r, displays)
	if !ok || got.Display != 0 || got.X < 0 || got.X > 1920 || got.Y < 0 || got.Y > 1080 {
		t.Fatalf("a missing monitor was not clamped: %+v (%v)", got, ok)
	}

	// A remembered window larger than the monitor fits after an unplug.
	r = windowRect{X: 100, Y: 100, W: 2560, H: 1440, Display: 0}
	got, ok = clampWindowRect(r, displays[:1])
	if !ok || got.W > 1920 || got.H > 1080 || got.W < windowMinW {
		t.Fatalf("oversized window not clamped: %+v (%v)", got, ok)
	}

	// Maximised fills the chosen monitor again.
	r = windowRect{X: 9, Y: 9, W: 800, H: 600, Display: 1, Maximized: true}
	got, ok = clampWindowRect(r, displays)
	if !ok || got.X != -1920 || got.Y != 200 || got.W != 1600 || got.H != 900 {
		t.Fatalf("maximised rect does not fill its monitor: %+v (%v)", got, ok)
	}

	if _, ok := clampWindowRect(r, nil); ok {
		t.Fatal("a missing display list was reported as valid")
	}
}
