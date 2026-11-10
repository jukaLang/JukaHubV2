package main

import (
	"testing"
	"time"
)

// TestShotParseOptions pins the screenshot-mode flags: published pictures
// depend on the mode being on when it was asked for, and on a stray argument
// never turning it on.
func TestShotParseOptions(t *testing.T) {
	envOf := func(vals map[string]string) func(string) string {
		return func(key string) string { return vals[key] }
	}
	none := envOf(nil)
	cases := []struct {
		name string
		args []string
		env  func(string) string
		want shotOptions
	}{
		{"nothing", nil, none, shotOptions{}},
		{"mode flag", []string{"--screenshot-mode"}, none, shotOptions{on: true}},
		{"one dash", []string{"-screenshot-mode"}, none, shotOptions{on: true}},
		{"mode switched off", []string{"--screenshot-mode=false"}, none, shotOptions{}},
		{"tour with a folder", []string{"--screenshot-tour", "../screenshots"}, none, shotOptions{on: true, tourDir: "../screenshots"}},
		{"tour with equals", []string{"--screenshot-tour=out"}, none, shotOptions{on: true, tourDir: "out"}},
		{"tour without a folder", []string{"--screenshot-tour"}, none, shotOptions{on: true, tourDir: "screenshots"}},
		{"tour followed by a flag", []string{"--screenshot-tour", "--safe-mode"}, none, shotOptions{on: true, tourDir: "screenshots"}},
		{"capture folder", []string{"--screenshot-mode", "--screenshot-dir", "shots"}, none, shotOptions{on: true, dir: "shots"}},
		{"a folder alone stays off", []string{"--screenshot-dir=shots"}, none, shotOptions{dir: "shots"}},
		{"environment", nil, envOf(map[string]string{"JUKAHUB_SCREENSHOT_MODE": "1", "JUKAHUB_SCREENSHOT_DIR": "pics"}), shotOptions{on: true, dir: "pics"}},
		{"flag folder beats the environment", []string{"--screenshot-dir", "a"}, envOf(map[string]string{"JUKAHUB_SCREENSHOT_MODE": "yes", "JUKAHUB_SCREENSHOT_DIR": "b"}), shotOptions{on: true, dir: "a"}},
		{"environment off", nil, envOf(map[string]string{"JUKAHUB_SCREENSHOT_MODE": "0"}), shotOptions{}},
		{"unrelated arguments", []string{"--safe-mode", "screenshot-mode"}, none, shotOptions{}},
	}
	for _, tc := range cases {
		if got := shotParseOptions(tc.args, tc.env); got != tc.want {
			t.Errorf("%s: shotParseOptions(%q) = %+v, want %+v", tc.name, tc.args, got, tc.want)
		}
	}
}

// TestShotClockFor pins which times a screenshot replaces: the present moment
// reads 9:41 on Saturday 14 November, any other moment and any time outside
// the mode is left alone.
func TestShotClockFor(t *testing.T) {
	now := time.Date(2026, time.March, 3, 18, 7, 30, 0, time.UTC)
	got := shotClockFor(now, now, true)
	if got.Hour() != 9 || got.Minute() != 41 || got.Day() != 14 || got.Month() != time.November || got.Weekday() != time.Saturday {
		t.Fatalf("the present moment reads %v, want 09:41 on Saturday 14 November", got)
	}
	if got := shotClockFor(now.Add(-2*time.Second), now, true); got.Hour() != 9 {
		t.Fatalf("a time read a moment ago reads %v, want the 09:41 stand-in", got)
	}
	for _, other := range []time.Time{now.Add(-time.Hour), now.Add(10 * time.Minute)} {
		if got := shotClockFor(other, now, true); !got.Equal(other) {
			t.Fatalf("%v is not now but reads %v", other, got)
		}
	}
	if got := shotClockFor(now, now, false); !got.Equal(now) {
		t.Fatalf("outside screenshot mode the clock reads %v, want %v", got, now)
	}
}
