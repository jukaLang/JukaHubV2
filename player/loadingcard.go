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
	// 208 rather than 196 leaves the bottom band deep enough for the Cancel
	// button that a video prepare draws under the slogan block.
	h = 208 + int32(loadingSloganMaxLines)*loadingSloganLineH
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

// loadingCancelPillH is the Cancel button's height: a 26px pill reads as a
// button at arm's length without eating the slogan's band.
const loadingCancelPillH = 26

// loadingCancelLayout returns the Cancel button's pill and the label's
// top-left corner, centred in the card's bottom band. It is split from the
// render so the geometry can be checked without a renderer.
func loadingCancelLayout(font *ttf.Font, cardX, cardW, y int32) (pill sdl.Rect, labelX, labelY int32) {
	const (
		gap   = int32(10)
		padX  = int32(10)
		label = "Cancel"
	)
	bw, bh, _ := font.SizeUTF8(loadingCancelButton)
	lw, _, _ := font.SizeUTF8(label)
	pillW := int32(bw) + padX*2
	total := pillW + gap + int32(lw)
	x := cardX + (cardW-total)/2
	textY := y + (loadingCancelPillH-int32(bh))/2
	return sdl.Rect{X: x, Y: y, W: pillW, H: loadingCancelPillH},
		x + pillW + gap, textY
}

// loadingCancelButton is the one button the card accepts while it is up.
const loadingCancelButton = "B"

// loadingCancelLabel is the word next to that badge.
const loadingCancelLabel = "Cancel"

// renderLoadingCancelButton draws the "B" badge and its Cancel label centred
// in the card's bottom band, the same pill-plus-label pairing the scene footers
// use for controller hints.
func renderLoadingCancelButton(renderer *sdl.Renderer, config *Config, cardX, cardW, y int32, fallback *ttf.Font) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		font = fallback
	}
	if font == nil {
		return
	}
	pill, labelX, labelY := loadingCancelLayout(font, cardX, cardW, y)
	fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, ColorIconSurface)
	// The badge letter is the button's own name, so it is centred in its pill
	// rather than padded to the left like a footer badge.
	bw, bh, _ := font.SizeUTF8(loadingCancelButton)
	renderText(renderer, config, font, loadingCancelButton, ColorTextPrimary(),
		pill.X+(pill.W-int32(bw))/2, pill.Y+(pill.H-int32(bh))/2)
	renderText(renderer, config, font, loadingCancelLabel, ColorTextSecondary(), labelX, labelY)
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
		// The card blocks navigation, so the way out has to look like a
		// control: a B button badge and its label, drawn the way the scene
		// footers draw their button hints, instead of a line of grey text.
		// It sits in the band under the slogan block so it cannot collide.
		renderLoadingCancelButton(renderer, config, cardX, cardW, cardY+cardH-36, statusFont)
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
