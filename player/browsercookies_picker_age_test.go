package main

// browsercookies_picker_age_test.go — each site's own session start on its row
// in the saved-cookie picker. The Settings row quotes one date for the whole
// jar; this is the per-site answer: two sites signed in on different days say
// so on their own rows, so the age of one site's login is visible before the
// press that forgets it. A host with no stamp shows no date — an unknown age
// must not be printed as an old one.

import (
	"net/http"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// siteRow finds a site's row by host, the way the picker's cursor does.
func siteRow(t *testing.T, rows []browserCookieRow, site string) browserCookieRow {
	t.Helper()
	for _, row := range rows {
		if row.Site == site {
			return row
		}
	}
	t.Fatalf("no row for %s in %+v", site, rows)
	return browserCookieRow{}
}

// TestCookiePickerRowCarriesEachSitesOwnStart: a row's date is the oldest
// first-seen stamp among that host's cookies — the 40-day-old youtube.com
// session must not borrow github.com's two-day-old date, and a host whose
// cookie was refreshed reports the older stamp, not the refresh, because the
// retention window measures from the same day.
func TestCookiePickerRowCarriesEachSitesOwnStart(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	prevConfig := appConfig
	appConfig = retentionConfig(0) // keep everything while the file loads
	t.Cleanup(func() { appConfig = prevConfig })

	oldYT := ageInDays(40)
	refreshedYT := ageInDays(5)
	gh := ageInDays(2)
	writeCookieFile(t, path, []browserCookieRecord{
		{Host: "youtube.com", Path: "/", Name: "session", Value: "a", FirstSeen: oldYT},
		// Refreshed five days ago: the site's start stays the older stamp.
		{Host: "youtube.com", Path: "/", Name: "consent", Value: "b", FirstSeen: refreshedYT},
		{Host: "github.com", Path: "/", Name: "logged_in", Value: "c", FirstSeen: gh},
	})
	simulateBrowserRelaunch()

	rows := browserCookieRows()
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want two site rows and the forget-all row", rows)
	}
	wantYT, err := time.Parse(time.RFC3339, oldYT)
	if err != nil {
		t.Fatalf("parse the youtube stamp the file was written with: %v", err)
	}
	yt := siteRow(t, rows, "youtube.com")
	if !yt.Since.Equal(wantYT) {
		t.Errorf("youtube.com's row dates %v, want the 40-day-old cookie (%v)", yt.Since, wantYT)
	}
	// The fresher cookie on the same host must not win: equal-stamp bugs hide
	// behind "some date is there", so the exact one is asserted.
	wantRefreshed, err := time.Parse(time.RFC3339, refreshedYT)
	if err != nil {
		t.Fatalf("parse the refresh stamp: %v", err)
	}
	if yt.Since.Equal(wantRefreshed) {
		t.Errorf("youtube.com's row quotes the refresh, not the session it began on")
	}
	wantGH, err := time.Parse(time.RFC3339, gh)
	if err != nil {
		t.Fatalf("parse the github stamp the file was written with: %v", err)
	}
	if got := siteRow(t, rows, "github.com"); !got.Since.Equal(wantGH) {
		t.Errorf("github.com's row dates %v, want its own two-day-old stamp (%v)", got.Since, wantGH)
	}
	// The forget-all row is not a site: it carries the total, not a date.
	if last := rows[len(rows)-1]; !last.ForgetAll || !last.Since.IsZero() {
		t.Errorf("last row = %+v, want the forget-all row without a date", last)
	}
}

// TestCookiePickerDatesALiveSession: a jar built by browsing this session —
// never reloaded from the card — still carries stamps, so the picker shows
// today on the row instead of waiting for a relaunch to discover a date.
func TestCookiePickerDatesALiveSession(t *testing.T) {
	useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{testCookie("session")})

	row := siteRow(t, browserCookieRows(), "example.com")
	if row.Since.IsZero() {
		t.Fatal("a cookie stamped when it was set has no date on its row")
	}
	if age := time.Since(row.Since); age < 0 || age > time.Minute {
		t.Fatalf("row date is %v away from now, want the stamp this session", age)
	}
}

// TestCookiePickerDrawsTheSiteAge is the rendering half: the date reaches the
// screen on the row's right edge, the site's name keeps its room beside it —
// the label grew by a date, so the name's budget has to shrink with it instead
// of the two being drawn through each other — and both survive on every canvas
// the app ships on.
func TestCookiePickerDrawsTheSiteAge(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	siteJar(t)
	prev := browserCookiePanel
	t.Cleanup(func() {
		browserCookiePanel = prev
		ClearTextCache()
	})

	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {800, 480}} {
		withScreen(t, size.w, size.h)
		browserCookiePanel = browserCookiePanelState{open: true}
		newProbeFrame(t)
		renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
		}
		ClearTextCache()
		config := layoutTestConfig(t)
		renderBrowserCookiePanel(renderer, config)

		font, _ := getCachedFont(config, "small")
		if font == nil {
			destroyTestRenderer(renderer)
			t.Fatalf("no font available for the text cache probe: the shipped Inter-Regular.ttf did not load, so this check cannot run")
		}
		yt := siteRow(t, browserCookieRows(), "youtube.com")
		if yt.Since.IsZero() {
			destroyTestRenderer(renderer)
			t.Fatalf("%dx%d: the site row carries no date to draw", size.w, size.h)
		}
		meta := browserCookieCountLabel(yt.Count) + " · since " + yt.Since.Local().Format("Jan 2")
		if GetTextCache().Get(font, meta, ColorTextSecondary()) == nil {
			destroyTestRenderer(renderer)
			t.Fatalf("%dx%d: the picker did not draw %q on the site's row", size.w, size.h, meta)
		}
		if GetTextCache().Get(font, "youtube.com", ColorTextPrimary()) == nil {
			destroyTestRenderer(renderer)
			t.Fatalf("%dx%d: the site's name is missing from its row", size.w, size.h)
		}
		// Both are right-placed in one line: the name and the metadata plus
		// the row's padding have to fit, or one draws over the other.
		nameW, _, _ := font.SizeUTF8("youtube.com")
		metaW, _, _ := font.SizeUTF8(meta)
		if len(browserCookieRowHits) == 0 {
			destroyTestRenderer(renderer)
			t.Fatalf("%dx%d: the picker drew no row hit boxes", size.w, size.h)
		}
		rowW := browserCookieRowHits[0].rect.W
		if int32(nameW+metaW)+24 > rowW {
			destroyTestRenderer(renderer)
			t.Errorf("%dx%d: name (%dpx) + age (%dpx) need %dpx with padding in %dpx",
				size.w, size.h, nameW, metaW, nameW+metaW+24, rowW)
			continue
		}
		destroyTestRenderer(renderer)
	}
}
