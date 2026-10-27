package main

// browsercookies_age_test.go — the reference point on the Settings cookie row:
// the day the saved session was first stored, so the retention window it cycles
// has a "from when?" beside it. These tests pin the date's content, which
// cookie it comes from (the oldest still on the card), that it follows a prune
// and a Forget, and that the longer label still fits the row's cell on every
// canvas the app ships on.

import (
	"testing"
	"time"
)

// cookieAgeRow finds the retention row on the page the way the user reaches it.
func cookieAgeRow(t *testing.T, config *Config) *Element {
	t.Helper()
	scene := settingsGeneralScene(config)
	if scene == nil {
		t.Fatal("no SettingsGeneral scene")
	}
	for i := range scene.Elements {
		if scene.Elements[i].Trigger == "cycle_cookie_retention" {
			return &scene.Elements[i]
		}
	}
	t.Fatal("Settings → General has no retention row")
	return nil
}

// TestCookieRowNamesTheSessionStart is the feature itself: the row carries the
// day the session on the card began, and drops the date when the session is
// forgotten, so the window never quotes a day no cookie remembers.
func TestCookieRowNamesTheSessionStart(t *testing.T) {
	config, _ := settingsWithCookies(t)

	// The page is built before the jar has anything in it: window alone,
	// because there is no session yet to point at.
	if got := config.Variables.Custom[cookieRowLabelKey]; got != "Keep" {
		t.Fatalf("row value before the session = %q, want the window alone", got)
	}

	config.Variables.Custom["cookieRetentionDays"] = 30
	refreshCookieRetentionRow(config)
	since, ok := browserSessionSince()
	if !ok {
		t.Fatal("the jar holds two cookies but reports no session start")
	}
	want := "30d · since " + since.Local().Format("Jan 2")
	if got := config.Variables.Custom[cookieRowLabelKey]; got != want {
		t.Fatalf("row value = %q, want %q", got, want)
	}

	// Forget empties the jar (two presses, same arm the Browser chip shares),
	// and the date goes with it.
	browserForgetConfirm.armedAt = time.Time{}
	if forgetSavedCookiesFromSettings(config) {
		t.Fatal("the first press dropped the session instead of arming")
	}
	if !forgetSavedCookiesFromSettings(config) {
		t.Fatal("the second press did not drop the session")
	}
	if got := config.Variables.Custom[cookieRowLabelKey]; got != "30d" {
		t.Fatalf("row value after Forget = %q, want the window without a date", got)
	}
}

// TestSessionStartIsTheOldestSurvivingCookie pins which cookie the date comes
// from: the oldest first-seen stamp still on the card. A session the retention
// window starts measuring is the one that remains — choosing a window that
// drops the oldest cookie has to move the date the row quotes.
func TestSessionStartIsTheOldestSurvivingCookie(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	prev := appConfig
	appConfig = retentionConfig(0) // keep everything while the file loads
	t.Cleanup(func() { appConfig = prev })

	old := ageInDays(40)
	writeCookieFile(t, path, []browserCookieRecord{
		{Host: "recent.example", Path: "/", Name: "recent", Value: "b", FirstSeen: ageInDays(2)},
		{Host: "oldest.example", Path: "/", Name: "oldest", Value: "a", FirstSeen: old},
	})
	simulateBrowserRelaunch()

	since, ok := browserSessionSince()
	if !ok {
		t.Fatal("a card with two stamped cookies reports no session start")
	}
	wantOldest, err := time.Parse(time.RFC3339, old)
	if err != nil {
		t.Fatalf("parse the stamp the file was written with: %v", err)
	}
	if !since.Equal(wantOldest) {
		t.Fatalf("session start = %v, want the 40-day-old cookie (%v)", since, wantOldest)
	}

	// Choosing a window that drops that oldest cookie moves the start: the
	// reference point follows what is still on the card, and applyCookieRetention
	// is the press that re-seeds the row.
	config := retentionConfig(30)
	applyCookieRetention(config, 30)
	if got := browserCookiesCount(); got != 1 {
		t.Fatalf("the prune left %d cookies, want only the recent one", got)
	}
	fresh, ok := browserSessionSince()
	if !ok {
		t.Fatal("the surviving cookie has no stamp")
	}
	want := "30d · since " + fresh.Local().Format("Jan 2")
	if got := config.Variables.Custom[cookieRowLabelKey]; got != want {
		t.Fatalf("row value after the prune = %q, want %q", got, want)
	}
}

// TestCookieRowWithTheSessionDateFitsTheSlot pins every state of the row
// against the cell it draws in, on each canvas the app ships on. The date is
// the part that can overrun: the row is built once at startup, scaled to the
// screen the app runs at, so each size gets the row it would really have.
func TestCookieRowWithTheSessionDateFitsTheSlot(t *testing.T) {
	states := []struct {
		name  string
		value string
	}{
		{"no session yet", "30d"},
		{"window with the date", "30d · since Oct 12"},
		{"Keep with the date", "Keep · since Oct 12"},
		{"single-digit day", "90d · since Sep 5"},
	}
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {800, 480}} {
		withScreen(t, size.w, size.h)
		newProbeFrame(t)
		config, err := loadConfig("jukaconfig.json")
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		ensureSettingsWidgets(config)
		row := cookieAgeRow(t, config)
		inner := getElementWidth(*row, 0) - 2*SpaceXS
		for _, state := range states {
			config.Variables.Custom[cookieRowLabelKey] = state.value
			text := substituteVars(row.Text, config.Variables.Custom)
			base, _ := getCachedFont(config, row.Font)
			if base == nil {
				t.Fatalf("no font available: the shipped Inter-Regular.ttf did not load, so this check cannot run")
			}
			// The draw path's own font choice and budget: the fitted font is
			// picked from the template, then the substituted value is what has
			// to survive the ellipsis.
			font := buttonLabelFont(config, row.Font, row.Text, getElementWidth(*row, 0))
			if got := ellipsize(font, text, inner); got != text {
				t.Errorf("%dx%d, %s: %q is clipped to %q in %dpx", size.w, size.h, state.name, text, got, inner)
			}
		}
	}
}
