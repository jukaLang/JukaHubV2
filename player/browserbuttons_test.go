package main

// browserbuttons_test.go — the Browser scene's own chips.
//
// The injected toolbar is thirteen buttons, and a chip is only a button if three
// things hold: its trigger is implemented, its rect is drawn where the row
// expects it, and a press on that rect reaches a live action. The first two are
// checked for every chip; the third is driven for every chip whose action needs
// neither the on-screen keyboard nor the card.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// browserChips returns the Browser scene's buttons, injecting the scene first
// the way the tile does.
func browserChips(t *testing.T, config *Config) []Element {
	t.Helper()
	ensureBrowserScene(config)
	var chips []Element
	for _, scene := range config.Scenes {
		if scene.Name != browserSceneName {
			continue
		}
		for _, elem := range scene.Elements {
			if elem.Type == "button" && elem.Trigger != "" {
				chips = append(chips, elem)
			}
		}
	}
	if len(chips) == 0 {
		t.Fatal("the injected Browser scene has no chips")
	}
	return chips
}

func browserChipsByTrigger(t *testing.T, config *Config) map[string]Element {
	t.Helper()
	out := map[string]Element{}
	for _, chip := range browserChips(t, config) {
		if _, dup := out[chip.Trigger]; dup {
			t.Errorf("two chips fire %q; the second is unreachable", chip.Trigger)
		}
		out[chip.Trigger] = chip
	}
	return out
}

// TestEveryBrowserChipHasATriggerCase: a chip whose trigger nobody implements
// is a button that eats the press and does nothing.
func TestEveryBrowserChipHasATriggerCase(t *testing.T) {
	config := layoutTestConfig(t)
	chips := browserChips(t, config)

	source, err := os.ReadFile("core.go")
	if err != nil {
		t.Fatalf("cannot read the trigger dispatch: %v", err)
	}
	body := string(source)
	for _, chip := range chips {
		if !strings.Contains(body, `case "`+chip.Trigger+`":`) {
			t.Errorf("the %s chip fires %q, which handleTrigger does not implement", chip.Text, chip.Trigger)
		}
	}
}

// TestEveryBrowserChipIsDrawnInsideItsOwnButton: a chip is only pressable where
// it is drawn, so the hit rect must be on the panel, big enough for its own
// label, and clear of its neighbours — two overlapping chips mean one of them
// can never be pressed.
func TestEveryBrowserChipIsDrawnInsideItsOwnButton(t *testing.T) {
	config := layoutTestConfig(t)
	checkBrowserChipGeometry(t, config)
}

// checkBrowserChipGeometry pins every chip's hit rect at the canvas the scene
// was injected for: on the panel, wide enough for its own label, and clear of
// its neighbours.
func checkBrowserChipGeometry(t *testing.T, config *Config) {
	t.Helper()
	chips := browserChips(t, config)

	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("no chip font: the geometry checked here would be the fallback, not what is drawn")
	}

	type placedChip struct {
		label string
		rect  sdl.Rect
	}
	var placed []placedChip
	for _, chip := range chips {
		w, h := buttonHitSize(chip, font)
		if w <= 0 || h <= 0 {
			t.Errorf("the %s chip is %dx%d px: nothing to press", chip.Text, w, h)
			continue
		}
		rect := sdl.Rect{X: chip.X, Y: chip.Y, W: w, H: h}
		if rect.X < 0 || rect.X+rect.W > screenWidth || rect.Y < 0 || rect.Y+rect.H > screenHeight {
			t.Errorf("the %s chip is off the panel: %+v (panel %dx%d)", chip.Text, rect, screenWidth, screenHeight)
		}
		if label, _, err := font.SizeUTF8(chip.Text); err == nil && int32(label) > rect.W {
			t.Errorf("the %s chip's label is %d px inside a %d px button: the word is clipped", chip.Text, label, rect.W)
		}
		for _, other := range placed {
			if rect.X < other.rect.X+other.rect.W && other.rect.X < rect.X+rect.W &&
				rect.Y < other.rect.Y+other.rect.H && other.rect.Y < rect.Y+rect.H {
				t.Errorf("the %s and %s chips overlap (%+v, %+v): one of them cannot be pressed",
					chip.Text, other.label, rect, other.rect)
			}
		}
		placed = append(placed, placedChip{label: chip.Text, rect: rect})
	}
	if len(placed) != len(chips) {
		t.Errorf("%d of %d chips have a pressable rect", len(placed), len(chips))
	}
}

// TestBrowserChipsAnswerWhenThereIsNothingToShow: these four chips act on a
// list that can be empty. The empty case is the one a dead chip hides behind, so
// each press has to say something rather than swallow the press.
func TestBrowserChipsAnswerWhenThereIsNothingToShow(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)

	config := layoutTestConfig(t)
	byTrigger := browserChipsByTrigger(t, config)

	for _, tc := range []struct {
		trigger string
		why     string
	}{
		{"browser_links", "the page lists no links"},
		{"browser_cookie_sites", "the jar is empty"},
		{"browser_bookmarks", "nothing has been saved"},
		{"browser_forget_cookies", "there is nothing to forget"},
	} {
		chip, ok := byTrigger[tc.trigger]
		if !ok {
			t.Fatalf("the browser has no %s chip", tc.trigger)
		}
		clearToastAfterTest(t)
		handleTrigger(nil, config, chip)
		if strings.TrimSpace(currentToast.Message) == "" {
			t.Errorf("the %s chip answered a press with nothing at all, and %s", chip.Text, tc.why)
		}
	}
}

// browserFetchLog records the page loads a chip starts.
type browserFetchLog struct {
	mu   sync.Mutex
	urls []string
}

func (f *browserFetchLog) record(url string) {
	f.mu.Lock()
	f.urls = append(f.urls, url)
	f.mu.Unlock()
}

func (f *browserFetchLog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.urls)
}

// waitBrowserAddress polls for the address a chip should land on. A load runs on
// its own goroutine, so the address is written a moment after the press.
func waitBrowserAddress(t *testing.T, config *Config, want, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if textBrowserAddress(config) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: address is %q, want %q", why, textBrowserAddress(config), want)
}

// TestBrowserChipsNavigateAndReload drives the chips that only need a network
// seam: Home, Issues, Back, Forward and Reload.
func TestBrowserChipsNavigateAndReload(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)

	config := layoutTestConfig(t)
	byTrigger := browserChipsByTrigger(t, config)

	var log browserFetchLog
	restore := setBrowserPageFetcherForTest(func(url string) browserPage {
		log.record(url)
		return browserPage{Text: "stub page", FinalURL: url}
	})
	t.Cleanup(restore)

	press := func(trigger string) {
		t.Helper()
		chip, ok := byTrigger[trigger]
		if !ok {
			t.Fatalf("the browser has no %s chip", trigger)
		}
		handleTrigger(nil, config, chip)
	}

	press("browser_home")
	waitBrowserAddress(t, config, browserHomeURL, "the Home chip")

	press("browser_issues")
	waitBrowserAddress(t, config, browserIssuesURL, "the Issues chip")

	// Back returns to the start page, Forward to the issue list, and Reload
	// asks for the page on screen again.
	press("browser_back")
	waitBrowserAddress(t, config, browserHomeURL, "the Back chip")

	press("browser_forward")
	waitBrowserAddress(t, config, browserIssuesURL, "the Forward chip")

	before := log.count()
	press("browser_reload")
	deadline := time.Now().Add(3 * time.Second)
	for log.count() <= before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if log.count() <= before {
		t.Fatal("the Reload chip did not ask the server for the page again")
	}
}

// TestBrowserChipsFitBothCanvases runs that geometry on the 16:9 panel and on
// the Brick's 960-wide canvas. The chip row is the one strip that has to hold
// thirteen labels at once, so the compact canvas — where every label is scaled
// but the row is not — is where a chip that fits at 1280 loses its word.
func TestBrowserChipsFitBothCanvases(t *testing.T) {
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		size := size
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			config := layoutTestConfig(t)
			withScreen(t, size.w, size.h)
			checkBrowserChipGeometry(t, config)
		})
	}
}
