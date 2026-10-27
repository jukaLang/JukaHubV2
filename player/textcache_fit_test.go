package main

// The fitted-label, width and substitution caches sit between the UI and
// FreeType: they turned the home frame from ~342 µs to ~70 µs on the desktop
// proxy, but a cache that returns the wrong answer is a rendering bug, not a
// slow frame. These checks pin every cached path to the uncached function it
// replaced, and pin the bounds so a long session cannot grow them without end.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

func testCacheFont(t *testing.T) *ttf.Font {
	t.Helper()
	newProbeFrame(t) // SDL video + TTF
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("the shipped Inter-Regular.ttf did not load, so the text-cache checks cannot run")
	}
	return font
}

// TestFitTextCacheMatchesUncached checks the cached clip against the binary
// search it wraps, including the widths that force a truncation.
func TestFitTextCacheMatchesUncached(t *testing.T) {
	font := testCacheFont(t)
	texts := []string{
		"Good afternoon",
		"Nothing played recently",
		"Open Media to start watching or listening.",
		"媒體播放器",
		"",
	}
	for _, text := range texts {
		for _, maxW := range []int32{8, 40, 100, 400} {
			want := fitTextToWidth(font, text, maxW)
			// Twice: the first call is the miss, the second must agree.
			for i := 0; i < 2; i++ {
				if got := ellipsize(font, text, maxW); got != want {
					t.Fatalf("ellipsize(%q, %d) = %q, want %q", text, maxW, got, want)
				}
			}
		}
	}
}

// TestMeasureCacheMatchesSizeUTF8 checks the cached width against FreeType.
func TestMeasureCacheMatchesSizeUTF8(t *testing.T) {
	font := testCacheFont(t)
	for _, text := range []string{"A", "L2/R2", "Sections", "媒體", "12:34", ""} {
		w, h, err := font.SizeUTF8(text)
		if err != nil {
			continue
		}
		for i := 0; i < 2; i++ {
			gw, gh := measureText(font, text)
			// The empty string is passed through as (0, 0).
			if text == "" {
				if gw != 0 || gh != 0 {
					t.Fatalf("measureText(%q) = %d,%d, want 0,0", text, gw, gh)
				}
				continue
			}
			if gw != int32(w) || gh != int32(h) {
				t.Fatalf("measureText(%q) = %d,%d, want %d,%d", text, gw, gh, w, h)
			}
		}
	}
}

// TestSanitizeCacheMatchesUncached checks the substitution cache, including a
// string that really is rewritten.
func TestSanitizeCacheMatchesUncached(t *testing.T) {
	font := testCacheFont(t)
	for _, text := range []string{
		"Score 0",
		"媒體播放器",
		"日本語のテキスト",
		"",
	} {
		want := sanitizeForFont(font, text)
		for i := 0; i < 2; i++ {
			if got := sanitizeCached(font, text); got != want {
				t.Fatalf("sanitizeCached(%q) = %q, want %q", text, got, want)
			}
		}
	}
}

// TestTextCachesStayBounded pushes far more strings through each cache than it
// is allowed to keep, then checks the caps held. The handheld runs for hours;
// an unbounded label cache would be a slow leak.
func TestTextCachesStayBounded(t *testing.T) {
	font := testCacheFont(t)
	for i := 0; i < maxTextFitEntries*2; i++ {
		ellipsize(font, fmt.Sprintf("label %d with some padding", i), 120)
	}
	if _, _, entries := textFitStats(); entries > maxTextFitEntries {
		t.Errorf("fit cache holds %d entries, cap is %d", entries, maxTextFitEntries)
	}

	for i := 0; i < maxTextMeasureEntries*2; i++ {
		measureText(font, fmt.Sprintf("width %d", i))
	}
	if _, entries := textMeasureStats(); entries > maxTextMeasureEntries {
		t.Errorf("width cache holds %d entries, cap is %d", entries, maxTextMeasureEntries)
	}

	for i := 0; i < maxSanitizeEntries*2; i++ {
		sanitizeCached(font, fmt.Sprintf("sanitize %d", i))
	}
	if _, entries := sanitizeStats(); entries > maxSanitizeEntries {
		t.Errorf("substitution cache holds %d entries, cap is %d", entries, maxSanitizeEntries)
	}
}

// TestTextCachesAreClearedWithTheTextures guards the invalidation hook: a theme
// or font-size change replaces the faces, so the cached answers must go with
// them.
func TestTextCachesAreClearedWithTheTextures(t *testing.T) {
	font := testCacheFont(t)
	ellipsize(font, "Open Media", 200)
	measureText(font, "Open Media")
	sanitizeCached(font, "Open Media")
	// Repeat so the hit counters move off zero before the clear.
	ellipsize(font, "Open Media", 200)
	measureText(font, "Open Media")
	sanitizeCached(font, "Open Media")

	if hits, _, _ := textFitStats(); hits == 0 {
		t.Fatal("fit cache recorded no hits for a repeated label")
	}

	ClearTextCache()

	if _, _, entries := textFitStats(); entries != 0 {
		t.Errorf("fit cache still holds %d entries after ClearTextCache", entries)
	}
	if _, entries := textMeasureStats(); entries != 0 {
		t.Errorf("width cache still holds %d entries after ClearTextCache", entries)
	}
	if _, entries := sanitizeStats(); entries != 0 {
		t.Errorf("substitution cache still holds %d entries after ClearTextCache", entries)
	}
	// And the caches still answer correctly afterwards.
	if got := ellipsize(font, "Open Media", 200); got != fitTextToWidth(font, "Open Media", 200) {
		t.Errorf("fit after clear = %q", got)
	}
	if strings.TrimSpace(sanitizeCached(font, "Open Media")) == "" {
		t.Error("substitution after clear returned nothing")
	}
}
