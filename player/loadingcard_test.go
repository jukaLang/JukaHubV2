package main

// The loading card used to clip its slogan: one unwrapped line inside a fixed
// 340px box. These tests pin the contract the fix promises — every slogan wraps
// inside the card at both handheld and desktop sizes, and none is truncated.

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// TestEverySloganFitsTheLoadingCard is the direct check for "the slogans do not
// fit": each one must wrap into at most loadingSloganMaxLines lines, every line
// must fit the card's text width, and no text may be ellipsized away.
func TestEverySloganFitsTheLoadingCard(t *testing.T) {
	_ = newProbeFrame(t)
	t.Cleanup(ClearTextCache)
	withScreen(t, 1280, 720)

	config := &Config{}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Skip("no font available for measurement")
	}

	cardW, _ := loadingCardSize()
	maxW := cardW - 2*loadingCardPad

	for _, slogan := range loadingJokes {
		lines := wrapLoadingText(font, slogan, maxW, loadingSloganMaxLines)
		if len(lines) == 0 {
			t.Errorf("slogan %q produced no lines", slogan)
			continue
		}
		if len(lines) > loadingSloganMaxLines {
			t.Errorf("slogan %q needs %d lines, card allows %d", slogan, len(lines), loadingSloganMaxLines)
		}
		for _, line := range lines {
			if w, _, _ := font.SizeUTF8(line); int32(w) > maxW {
				t.Errorf("slogan %q line %q is %dpx wide, card allows %dpx", slogan, line, w, maxW)
			}
			if strings.Contains(line, "…") {
				t.Errorf("slogan %q was truncated on screen: %q", slogan, line)
			}
		}
		// The rendered text must still read as the whole slogan.
		joined := strings.Join(lines, " ")
		if strings.Join(strings.Fields(joined), " ") != strings.Join(strings.Fields(slogan), " ") {
			t.Errorf("slogan changed while wrapping:\n got %q\nwant %q", joined, slogan)
		}
	}
}

// TestSloganRotationIncreases checks the rotation actually grew and holds no
// empty or duplicated entries.
func TestSloganRotationIncreases(t *testing.T) {
	if len(loadingJokes) < 53 {
		t.Fatalf("loading slogans = %d, want at least the original 33 plus 20 new", len(loadingJokes))
	}
	seen := map[string]bool{}
	for _, s := range loadingJokes {
		if strings.TrimSpace(s) == "" {
			t.Error("empty slogan in the rotation")
		}
		if seen[s] {
			t.Errorf("duplicate slogan %q", s)
		}
		seen[s] = true
	}
}

func TestLoadingJokeIndexWalksTheWholeList(t *testing.T) {
	seen := map[int]bool{}
	step := uint64(loadingJokeIntervalMs)
	for i := uint64(0); i < uint64(len(loadingJokes)); i++ {
		idx := loadingJokeIndex(i * step)
		if idx < 0 || idx >= len(loadingJokes) {
			t.Fatalf("index %d out of range for tick %d", idx, i*step)
		}
		seen[idx] = true
	}
	if len(seen) != len(loadingJokes) {
		t.Fatalf("rotation reached %d of %d slogans", len(seen), len(loadingJokes))
	}
}

// TestLoadingCardFitsSmallScreens covers the handheld's resolution as well as
// the desktop one, since the card is centred and clamped.
func TestLoadingCardFitsSmallScreens(t *testing.T) {
	for _, screen := range []struct{ w, h int32 }{{1280, 720}, {640, 480}, {1024, 600}} {
		withScreen(t, screen.w, screen.h)
		cardW, cardH := loadingCardSize()
		if cardW <= 0 || cardH <= 0 {
			t.Fatalf("%dx%d: card size %dx%d", screen.w, screen.h, cardW, cardH)
		}
		if cardW > screen.w-32 || cardH > screen.h-32 {
			t.Errorf("%dx%d: card %dx%d does not leave a margin", screen.w, screen.h, cardW, cardH)
		}
	}
}

// TestLoadingCardDrawsSloganInsideTheCard renders the overlay and checks no
// slogan-coloured pixel is painted outside the panel.
func TestLoadingCardDrawsSloganInsideTheCard(t *testing.T) {
	_ = newProbeFrame(t)
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer r.Destroy()
	withScreen(t, probeW, probeH)
	t.Cleanup(ClearTextCache)

	cfg := &Config{}
	cfg.Variables.SpinnerText = "Loading videos..."
	r.SetDrawColor(9, 14, 25, 255)
	r.Clear()
	renderLoadingCard(r, cfg)

	cardW, cardH := loadingCardSize()
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2

	buf := make([]byte, probeW*probeH*4)
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	slogan := sdl.Color{R: 176, G: 194, B: 220}
	outside := 0
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			if x >= cardX-6 && x <= cardX+cardW+6 && y >= cardY-6 && y <= cardY+cardH+6 {
				continue
			}
			i := (y*probeW + x) * 4
			// Antialiased glyph cores keep the exact text colour; the card
			// background and the backdrop never do.
			if buf[i] == slogan.B && buf[i+1] == slogan.G && buf[i+2] == slogan.R {
				outside++
			}
		}
	}
	if outside != 0 {
		t.Fatalf("%d slogan-coloured pixels painted outside the card", outside)
	}
}
