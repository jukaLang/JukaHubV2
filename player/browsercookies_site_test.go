package main

// Forgetting one site without dropping every other login: the jar keeps what
// the other sites hold, the file on the card agrees, and the picker asks twice
// before it acts. These tests drive the press, not just the drawing — the panel
// is only the way in.

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// siteJar leaves the jar holding two cookies for youtube.com and one for
// github.com, the shape the picker exists for.
func siteJar(t *testing.T) string {
	t.Helper()
	path := useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://www.youtube.com/watch"), []*http.Cookie{
		testCookie("session"),
		testCookie("consent"),
	})
	jar.SetCookies(mustURL(t, "https://github.com/login"), []*http.Cookie{
		testCookie("logged_in"),
	})
	return path
}

// TestForgetOneSiteKeepsTheOthers: dropping youtube.com leaves github.com's
// session alone, in memory and in the file a relaunch reads.
func TestForgetOneSiteKeepsTheOthers(t *testing.T) {
	path := siteJar(t)

	dropped, err := browserForgetSiteCookies("youtube.com")
	if err != nil {
		t.Fatalf("forget one site: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("dropped %d cookies, want the 2 youtube.com ones", dropped)
	}
	if got := browserCookiesCount(); got != 1 {
		t.Fatalf("%d cookies left, want github.com's one", got)
	}
	sites := browserCookieSites()
	if len(sites) != 1 || sites[0].Domain != "github.com" {
		t.Fatalf("sites after the drop = %+v, want only github.com", sites)
	}

	file := savedCookieFile(t, path)
	if len(file.Cookies) != 1 || file.Cookies[0].Name != "logged_in" {
		t.Fatalf("the file holds %+v, want only github.com's cookie", file.Cookies)
	}

	// And a relaunch reads exactly that back: the dropped site must not return
	// from the copy on the card.
	simulateBrowserRelaunch()
	if got := browserCookiesCount(); got != 1 {
		t.Fatalf("after a relaunch %d cookies came back, want 1", got)
	}
	if sites := browserCookieSites(); len(sites) != 1 || sites[0].Domain != "github.com" {
		t.Fatalf("a relaunch restored %+v", sites)
	}
}

// TestForgetOneSiteThatWasTheLastDeletesTheFile: the file is a second place to
// look, so an emptied jar takes it with it and the Forget chip goes back to its
// plain label.
func TestForgetOneSiteThatWasTheLastDeletesTheFile(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		testCookie("session"),
	})

	dropped, err := browserForgetSiteCookies("example.com")
	if err != nil {
		t.Fatalf("forget one site: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d, want 1", dropped)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file survived the last site going (err=%v)", err)
	}
	if got := browserCookieCountLabel(browserCookiesCount()); got != "0 cookies" {
		t.Fatalf("count label after the drop = %q", got)
	}
}

// TestForgetSiteIgnoresAnotherSite: naming a host the jar has nothing for must
// not touch the jar, and must not rewrite the file for nothing.
func TestForgetSiteIgnoresAnotherSite(t *testing.T) {
	path := siteJar(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if dropped, err := browserForgetSiteCookies("nowhere.example"); err != nil || dropped != 0 {
		t.Fatalf("forget of an unknown site returned (%d, %v), want (0, nil)", dropped, err)
	}
	if got := browserCookiesCount(); got != 3 {
		t.Fatalf("%d cookies left, want all 3", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unknown site rewrote the cookie file")
	}
}

// TestCookiePickerListsSitesAndForgetsOne drives the panel: one row per host in
// the strip's order, the whole-jar row last, and a press that asks before it
// drops anything.
func TestCookiePickerListsSitesAndForgetsOne(t *testing.T) {
	siteJar(t)
	prev := browserCookiePanel
	t.Cleanup(func() { browserCookiePanel = prev })

	rows := browserCookieRows()
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want youtube.com, github.com and the forget-all row", rows)
	}
	if rows[0].Site != "youtube.com" || rows[0].Count != 2 {
		t.Fatalf("first row = %+v, want youtube.com with 2", rows[0])
	}
	if rows[1].Site != "github.com" || rows[1].Count != 1 {
		t.Fatalf("second row = %+v, want github.com with 1", rows[1])
	}
	if !rows[2].ForgetAll || rows[2].Count != 3 {
		t.Fatalf("last row = %+v, want the forget-all row carrying 3", rows[2])
	}

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	browserCookiePanel = browserCookiePanelState{open: true}
	browserCookiePanelActivate(config) // arms youtube.com
	if browserCookiePanel.armed != "youtube.com" {
		t.Fatalf("the first press left %q armed, want youtube.com", browserCookiePanel.armed)
	}
	if got := browserCookiesCount(); got != 3 {
		t.Fatalf("the arming press dropped cookies (%d left)", got)
	}

	// Moving to another site and pressing there arms that one instead; the
	// first site's arm does not carry over.
	browserCookiePanelMove(1)
	browserCookiePanelActivate(config)
	if browserCookiePanel.armed != "github.com" {
		t.Fatalf("after moving, %q is armed, want github.com", browserCookiePanel.armed)
	}
	if got := browserCookiesCount(); got != 3 {
		t.Fatalf("the second site's first press dropped cookies (%d left)", got)
	}

	// The second press inside the window is the one that acts.
	browserCookiePanelActivate(config)
	if got := browserCookiesCount(); got != 2 {
		t.Fatalf("%d cookies left after dropping github.com, want youtube.com's 2", got)
	}
	sites := browserCookieSites()
	if len(sites) != 1 || sites[0].Domain != "youtube.com" {
		t.Fatalf("sites after the drop = %+v, want only youtube.com", sites)
	}
	if browserCookiePanel.open {
		t.Fatal("the picker stayed open after a drop")
	}
}

// TestCookiePickerArmExpires: an arm that is never confirmed goes away on its
// own, so a stray press cannot be completed minutes later.
func TestCookiePickerArmExpires(t *testing.T) {
	siteJar(t)
	prev := browserCookiePanel
	t.Cleanup(func() { browserCookiePanel = prev })

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	browserCookiePanel = browserCookiePanelState{
		open:    true,
		armed:   "youtube.com",
		armedAt: time.Now().Add(-2 * browserForgetConfirmWindow),
	}
	browserCookiePanelActivate(config)
	if got := browserCookiesCount(); got != 3 {
		t.Fatalf("an expired arm dropped cookies (%d left)", got)
	}
	if browserCookiePanel.armed != "youtube.com" {
		t.Fatalf("an expired arm was not re-armed (armed=%q)", browserCookiePanel.armed)
	}
}

// TestCookiePickerKeysDriveIt: the controller and keyboard route has to work
// without a mouse, and B backs out without dropping anything.
func TestCookiePickerKeysDriveIt(t *testing.T) {
	siteJar(t)
	prev := browserCookiePanel
	t.Cleanup(func() { browserCookiePanel = prev })

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	openBrowserCookiePanel(config)
	if !browserCookiePanel.open {
		t.Fatal("the picker did not open for a jar with cookies")
	}
	ctrlBrowserCookies(&sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}, config)
	if browserCookiePanel.cursor != 1 {
		t.Fatalf("cursor after one Down = %d, want 1", browserCookiePanel.cursor)
	}
	ctrlBrowserCookies(&sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_DPAD_UP}, config)
	if browserCookiePanel.cursor != 0 {
		t.Fatalf("cursor after Up = %d, want 0", browserCookiePanel.cursor)
	}
	// Up from the first row wraps to the forget-all row, the way the link
	// picker wraps.
	ctrlBrowserCookies(&sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_DPAD_UP}, config)
	if got := browserCookiePanel.cursor; got != len(browserCookieRows())-1 {
		t.Fatalf("cursor after wrapping Up = %d, want the last row", got)
	}
	ctrlBrowserCookies(&sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_B}, config)
	if browserCookiePanel.open {
		t.Fatal("B did not close the picker")
	}
	if got := browserCookiesCount(); got != 3 {
		t.Fatalf("closing the picker dropped cookies (%d left)", got)
	}
}

// TestCookiePickerSaysSoWhenEmpty: with nothing stored there is no list to show,
// and the press has to say that instead of opening an empty panel.
func TestCookiePickerSaysSoWhenEmpty(t *testing.T) {
	useTempBrowserCookiePath(t)
	prev := browserCookiePanel
	t.Cleanup(func() { browserCookiePanel = prev })

	openBrowserCookiePanel(&Config{})
	if browserCookiePanel.open {
		t.Fatal("the picker opened with an empty jar")
	}
}

// TestCookieStripTapOpensThePicker is the touch route: the line under the page
// names the hosts, so tapping it opens the picker, and a tap anywhere else on
// the page is left to the page.
func TestCookieStripTapOpensThePicker(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	siteJar(t)
	prev := browserCookiePanel
	prevHit := browserCookieStripHit
	t.Cleanup(func() {
		browserCookiePanel = prev
		browserCookieStripHit = prevHit
	})
	browserCookiePanel = browserCookiePanelState{}

	config := layoutTestConfig(t)
	ensureBrowserScene(config)
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	elem := browserCursorTestElement("browser_cookie_strip_tap")
	renderTextBrowser(renderer, config, elem)

	hit := browserCookieStripHit
	if hit.W <= 0 || hit.H <= 0 {
		t.Fatal("the page did not lay out the cookie strip")
	}
	if bottom := elem.Y + getElementHeight(elem, 480); hit.Y+hit.H > bottom {
		t.Fatalf("the strip's tap area (y=%d h=%d) runs past the panel's bottom at %d", hit.Y, hit.H, bottom)
	}

	if browserCookieStripTap(hit.X+4, elem.Y+4, config) {
		t.Fatal("a tap on the page's header opened the cookie picker")
	}
	if browserCookiePanel.open {
		t.Fatal("a tap outside the strip opened the picker")
	}
	if !browserCookieStripTap(hit.X+hit.W/2, hit.Y+hit.H/2, config) {
		t.Fatal("a tap on the strip did not open the picker")
	}
	if !browserCookiePanel.open {
		t.Fatal("the strip tap returned true but opened nothing")
	}
}

// TestCookiePickerRenders draws the overlay on a real screen: every row gets a
// hit rectangle the mouse can land on, and the site names reach the screen.
func TestCookiePickerRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	siteJar(t)
	prev := browserCookiePanel
	t.Cleanup(func() { browserCookiePanel = prev })
	withScreen(t, 1280, 720)
	browserCookiePanel = browserCookiePanelState{open: true}

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	config := layoutTestConfig(t)
	renderBrowserCookiePanel(renderer, config)

	rows := len(browserCookieRows())
	if len(browserCookieRowHits) != rows {
		t.Fatalf("%d row hit boxes for %d rows", len(browserCookieRowHits), rows)
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatalf("no font available for the text cache probe: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	if GetTextCache().Get(font, "youtube.com", ColorTextPrimary()) == nil {
		t.Fatal("the picker did not draw the first site's name")
	}
	if browserCookiePanelRect.W <= 0 {
		t.Fatal("the picker has no panel rectangle for a click to be inside of")
	}
	for _, hit := range browserCookieRowHits {
		if !pointInRect(hit.rect.X+4, hit.rect.Y+4, browserCookiePanelRect) {
			t.Fatalf("row %d sits outside the panel", hit.index)
		}
	}
}

// TestSitesChipFitsTheChipRow guards the cost of the new chip: the toolbar's
// width is now shared ten ways instead of nine, so the widest labels still have
// to fit their share on the smallest panel the browser opens on. A chip whose
// label is ellipsized is a control the user has to guess at.
func TestSitesChipFitsTheChipRow(t *testing.T) {
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {800, 480}} {
		withScreen(t, size.w, size.h)
		// The fonts are built for the screen that is up, so this is looked up per
		// size: a chip label that fits at 1280 can overflow at 800.
		font, _ := getCachedFont(&Config{}, "small")
		if font == nil {
			t.Fatalf("no font available: the shipped Inter-Regular.ttf did not load, so this check cannot run")
		}
		config := &Config{}
		ensureBrowserScene(config)
		found := false
		for _, elem := range config.Scenes[0].Elements {
			if elem.Type != "button" {
				continue
			}
			if elem.Trigger == "browser_cookie_sites" {
				found = true
			}
			w := getElementWidth(elem, 0)
			if w <= 0 {
				t.Fatalf("%dx%d: chip %q has no width", size.w, size.h, elem.Text)
			}
			labelW, _, err := font.SizeUTF8(elem.Text)
			if err != nil {
				continue
			}
			if int32(labelW) > w-2*SpaceXS {
				t.Errorf("%dx%d: chip %q needs %dpx in %dpx", size.w, size.h, elem.Text, labelW, w)
			}
		}
		if !found {
			t.Fatalf("%dx%d: the Browser has no Sites chip", size.w, size.h)
		}
	}
}

// TestForgetAllRowStillAsksTwice: the last row is the chip's action, so it must
// keep the chip's two-press rule rather than dropping everything on one press.
func TestForgetAllRowStillAsksTwice(t *testing.T) {
	siteJar(t)
	prev := browserCookiePanel
	prevArm := browserForgetConfirm.armedAt
	t.Cleanup(func() {
		browserCookiePanel = prev
		browserForgetConfirm.armedAt = prevArm
	})
	browserForgetConfirm.armedAt = time.Time{}

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	browserCookiePanel = browserCookiePanelState{open: true, cursor: len(browserCookieRows()) - 1}
	browserCookiePanelActivate(config)
	if got := browserCookiesCount(); got != 3 {
		t.Fatalf("the forget-all row dropped on the first press (%d left)", got)
	}
	if browserForgetConfirm.armedAt.IsZero() {
		t.Fatal("the forget-all row did not arm the chip's confirm window")
	}
}
