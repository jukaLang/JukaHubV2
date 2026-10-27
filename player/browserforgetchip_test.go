package main

// The Browser toolbar's Forget chip carries the saved-session count, so the
// answer to "am I still signed in somewhere?" is on screen instead of behind a
// press. These tests pin the label, the count it shows, and the wire that
// refreshes it while the Browser draws.

import (
	"net/http"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// forgetChipElement returns the Forget chip from the config's Browser scene.
func forgetChipElement(t *testing.T, config *Config) *Element {
	t.Helper()
	for s := range config.Scenes {
		if config.Scenes[s].Name != browserSceneName {
			continue
		}
		for i := range config.Scenes[s].Elements {
			if config.Scenes[s].Elements[i].Trigger == "browser_forget_cookies" {
				return &config.Scenes[s].Elements[i]
			}
		}
	}
	t.Fatal("the Browser scene has no Forget chip")
	return nil
}

// TestBrowserForgetChipCarriesTheCount walks the chip through the session it
// describes: empty, holding a few cookies, and emptied by Forget.
func TestBrowserForgetChipCarriesTheCount(t *testing.T) {
	useTempBrowserCookiePath(t)

	config := &Config{}
	ensureBrowserScene(config)
	chip := forgetChipElement(t, config)

	browserSyncForgetChip(config)
	if chip.Text != "Forget" {
		t.Fatalf("an empty jar shows %q, want the plain label", chip.Text)
	}

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "session", Value: "abc", Path: "/"},
		{Name: "consent", Value: "yes", Path: "/"},
		{Name: "theme", Value: "dark", Path: "/"},
	})
	browserSyncForgetChip(config)
	if chip.Text != "Forget 3" {
		t.Fatalf("three stored cookies show %q, want \"Forget 3\"", chip.Text)
	}

	if _, err := browserForgetCookies(); err != nil {
		t.Fatalf("forget: %v", err)
	}
	browserSyncForgetChip(config)
	if chip.Text != "Forget" {
		t.Fatalf("after Forget the chip shows %q, want the plain label", chip.Text)
	}
}

// TestBrowserForgetChipLabel keeps the chip inside its fixed share of the row:
// the count is capped, and zero is the plain word rather than "Forget 0".
func TestBrowserForgetChipLabel(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, "Forget"},
		{0, "Forget"},
		{1, "Forget 1"},
		{12, "Forget 12"},
		{99, "Forget 99"},
		{100, "Forget 99+"},
		{5000, "Forget 99+"},
	}
	for _, c := range cases {
		if got := browserForgetChipLabel(c.n); got != c.want {
			t.Errorf("browserForgetChipLabel(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestBrowserRenderSyncsTheForgetChip is the wiring: drawing the web panel is
// what refreshes the chip, so a page that stores a cookie updates the toolbar
// without a scene change or a relaunch. The stale label is what a scene saved
// before this feature would carry.
func TestBrowserRenderSyncsTheForgetChip(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	useTempBrowserCookiePath(t)

	config := layoutTestConfig(t)
	ensureBrowserScene(config)
	chip := forgetChipElement(t, config)
	chip.Text = "Forget"

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "session", Value: "abc", Path: "/"},
		{Name: "consent", Value: "yes", Path: "/"},
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderTextBrowser(renderer, config, browserCursorTestElement("browser_chip_sync"))

	if chip.Text != "Forget 2" {
		t.Fatalf("drawing the page left the chip at %q, want \"Forget 2\"", chip.Text)
	}
}
