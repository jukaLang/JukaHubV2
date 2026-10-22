package main

// loadingcard.go — the loading overlay shown while a long operation runs and
// between scenes.
//
// The card used to be a fixed 340px box that drew one unwrapped slogan line, so
// most JukaLang slogans were cut off mid-sentence and the text sat half outside
// the panel. It is wider now, the status line and slogan are centred with real
// padding, and the slogan wraps to as many lines as it needs (up to three).

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// loadingCardMaxW is the widest the card grows; narrow screens shrink it.
	loadingCardMaxW = 560
	// loadingCardPad is the horizontal inset the slogan wraps inside.
	loadingCardPad = 28
	// loadingSloganMaxLines keeps the card height predictable.
	loadingSloganMaxLines = 3
	// loadingSloganLineH is the slogan line spacing.
	loadingSloganLineH = 22
	// loadingJokeIntervalMs is how long one slogan stays on screen.
	loadingJokeIntervalMs = 2800
)

// loadingCardSize returns the card geometry for the current screen. The width is
// clamped so the card always keeps a margin on small displays.
func loadingCardSize() (w, h int32) {
	w = loadingCardMaxW
	if w > screenWidth-64 {
		w = screenWidth - 64
	}
	if w < 280 {
		w = 280
	}
	h = 196 + int32(loadingSloganMaxLines)*loadingSloganLineH
	if h > screenHeight-32 {
		h = screenHeight - 32
	}
	return w, h
}

// loadingJokeIndex picks the slogan for a given tick count. -1 means the list
// is empty.
func loadingJokeIndex(ticks uint64) int {
	if len(loadingJokes) == 0 {
		return -1
	}
	return int(ticks / loadingJokeIntervalMs % uint64(len(loadingJokes)))
}

// wrapLoadingText wraps text into at most maxLines lines that each fit maxW. A
// single word wider than the card is ellipsized, and any text beyond the line
// budget is folded into the last line and ellipsized there.
func wrapLoadingText(font *ttf.Font, text string, maxW int32, maxLines int) []string {
	text = strings.TrimSpace(text)
	if font == nil || text == "" || maxLines < 1 {
		return []string{text}
	}
	measure := func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}

	var all []string
	cur := ""
	for _, word := range strings.Fields(text) {
		candidate := word
		if cur != "" {
			candidate = cur + " " + word
		}
		if cur != "" && measure(candidate) > maxW {
			all = append(all, cur)
			cur = word
			continue
		}
		cur = candidate
	}
	if cur != "" {
		all = append(all, cur)
	}
	if len(all) == 0 {
		return []string{text}
	}

	if len(all) > maxLines {
		merged := strings.Join(all[maxLines-1:], " ")
		all = append(append([]string{}, all[:maxLines-1]...), ellipsize(font, merged, maxW))
	}
	for i, line := range all {
		if measure(line) > maxW {
			all[i] = ellipsize(font, line, maxW)
		}
	}
	return all
}

// renderLoadingCard draws the modal loading overlay: spinner, status line and a
// wrapped slogan, all inside a card that fits the screen.
func renderLoadingCard(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil {
		return
	}

	// Frosted backdrop.
	fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, sdl.Color{R: 6, G: 8, B: 14, A: 200})

	cardW, cardH := loadingCardSize()
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2

	fillRoundedRect(renderer, cardX+4, cardY+6, cardW, cardH, 20, ShadowFill(80))
	fillRoundedRect(renderer, cardX, cardY, cardW, cardH, 20, sdl.Color{R: 18, G: 24, B: 36, A: 238})
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(255, 255, 255, 14)
	renderer.FillRect(&sdl.Rect{X: cardX + 12, Y: cardY + 1, W: cardW - 24, H: 1})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 90)
	renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + 1, W: cardW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + 1, W: 1, H: cardH - 2})
	renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + cardH - 2, W: cardW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: cardX + cardW - 2, Y: cardY + 1, W: 1, H: cardH - 2})

	renderSpinner(renderer, cardX+cardW/2, cardY+62, 40, sdl.Color{R: 140, G: 180, B: 255, A: 220})

	statusFont, _ := getCachedFont(config, "medium")
	if statusFont == nil {
		statusFont, _ = getCachedFont(config, "small")
	}
	if statusFont == nil {
		return
	}
	status := strings.TrimSpace(config.Variables.SpinnerText)
	if status == "" {
		status = "Loading..."
	}
	// A video being resolved names what is loading, so the wait is obviously
	// about the item the user just picked rather than a frozen app.
	preparing, prepareTitle := videoPrepareStatus()
	if preparing {
		if prepareTitle != "" {
			status = "Loading " + ellipsize(statusFont, prepareTitle, cardW-2*loadingCardPad)
		} else {
			status = "Loading video…"
		}
	}
	sw, _, _ := statusFont.SizeUTF8(status)
	renderText(renderer, config, statusFont, status, sdl.Color{R: 226, G: 234, B: 246, A: 255},
		cardX+(cardW-int32(sw))/2, cardY+116)
	if preparing {
		// The spinner blocks navigation; say how to back out of it. The hint
		// sits below the slogan block so it cannot collide with it.
		hintFont, _ := getCachedFont(config, "small")
		if hintFont == nil {
			hintFont = statusFont
		}
		hint := "B / Esc cancels"
		hw, _, _ := hintFont.SizeUTF8(hint)
		renderText(renderer, config, hintFont, hint, sdl.Color{R: 150, G: 162, B: 184, A: 255},
			cardX+(cardW-int32(hw))/2, cardY+cardH-30)
	}

	sloganFont, _ := getCachedFont(config, "small")
	if sloganFont == nil {
		sloganFont = statusFont
	}
	idx := loadingJokeIndex(sdl.GetTicks64())
	if idx < 0 {
		return
	}
	lines := wrapLoadingText(sloganFont, loadingJokes[idx], cardW-2*loadingCardPad, loadingSloganMaxLines)
	y := cardY + 160
	for _, line := range lines {
		// The slogan sits in a slightly brighter tone than before so it reads
		// at arm's length on the handheld.
		lw, _, _ := sloganFont.SizeUTF8(line)
		x := cardX + (cardW-int32(lw))/2
		if x < cardX+loadingCardPad {
			x = cardX + loadingCardPad
		}
		renderText(renderer, config, sloganFont, line, sdl.Color{R: 176, G: 194, B: 220, A: 235}, x, y)
		y += loadingSloganLineH
	}
}
