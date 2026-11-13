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
	"sync"

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
	// Measured as drawGlassHintBadge names it: Esc while the hints name keys.
	bw, bh, _ := font.SizeUTF8(hintKeyFor(loadingCancelButton, label))
	lw, _, _ := font.SizeUTF8(label)
	pillW := int32(bw) + padX*2
	total := pillW + gap + int32(lw)
	x := cardX + (cardW-total)/2
	textY := y + (loadingCancelPillH-int32(bh))/2
	return sdl.Rect{X: x, Y: y, W: pillW, H: loadingCancelPillH},
		x + pillW + gap, textY
}

// loadingCancelButton is the card's way out. While a download is on the card
// it accepts a second button, Y (ytdlBgButton), which sends the download to
// the background.
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
	if ytdlBgOffered() {
		renderLoadingTwoButtons(renderer, config, font, cardX, cardW, y)
		return
	}
	pill, labelX, labelY := loadingCancelLayout(font, cardX, cardW, y)
	// Home's footer hint exactly: the button's name centred in a glass pill,
	// its action in the muted ink beside it.
	drawGlassHintBadge(renderer, config, font, pill, loadingCancelButton, loadingCancelLabel, labelX, labelY)
	lw, _, _ := font.SizeUTF8(loadingCancelLabel)
	loadingCardHitsSet(sdl.Rect{X: pill.X, Y: pill.Y, W: labelX + int32(lw) - pill.X, H: pill.H}, sdl.Rect{})
}

// loadingCardHits are the card's buttons as the last frame drew them, so a tap
// on one does what its button does (the card swallows every other tap).
var loadingCardHits struct {
	mu                 sync.Mutex
	cancel, background sdl.Rect
}

func loadingCardHitsSet(cancel, background sdl.Rect) {
	loadingCardHits.mu.Lock()
	loadingCardHits.cancel, loadingCardHits.background = cancel, background
	loadingCardHits.mu.Unlock()
}

// loadingCardClick routes a tap while a video is being prepared: Cancel, or
// sending its download to the background (ytdownload_bg.go). Main thread.
func loadingCardClick(mx, my int32) {
	loadingCardHits.mu.Lock()
	cancel, background := loadingCardHits.cancel, loadingCardHits.background
	loadingCardHits.mu.Unlock()
	in := func(r sdl.Rect) bool {
		return r.W > 0 && mx >= r.X && mx < r.X+r.W && my >= r.Y && my < r.Y+r.H
	}
	switch {
	case in(background):
		ytdlSendToBackground()
	case in(cancel):
		cancelVideoPrepare()
	}
}

// renderLoadingTwoButtons draws "B Cancel" and "Y Download in background" side
// by side while a download is on the card; the long label shortens to
// "Background" on a narrow card.
func renderLoadingTwoButtons(renderer *sdl.Renderer, config *Config, font *ttf.Font, cardX, cardW, y int32) {
	const (
		gap    = int32(10) // pill to its label
		padX   = int32(10) // inside a pill
		spread = int32(28) // between the two hints
	)
	type hint struct {
		button, label string
		pillW, labelW int32
	}
	measure := func(button, label string) hint {
		bw, _, _ := font.SizeUTF8(hintKeyFor(button, label))
		lw, _, _ := font.SizeUTF8(label)
		return hint{button: button, label: label, pillW: int32(bw) + padX*2, labelW: int32(lw)}
	}
	width := func(hs ...hint) int32 {
		w := spread * int32(len(hs)-1)
		for _, h := range hs {
			w += h.pillW + gap + h.labelW
		}
		return w
	}
	cancel := measure(loadingCancelButton, loadingCancelLabel)
	bg := measure(ytdlBgButton, ytdlBgLabel)
	if width(cancel, bg) > cardW-2*loadingCardPad {
		bg = measure(ytdlBgButton, ytdlBgLabelShort)
	}
	_, bh, _ := font.SizeUTF8(hintKeyFor(loadingCancelButton, loadingCancelLabel))
	textY := y + (loadingCancelPillH-int32(bh))/2
	x := cardX + (cardW-width(cancel, bg))/2
	var rects [2]sdl.Rect
	for i, h := range [...]hint{cancel, bg} {
		pill := sdl.Rect{X: x, Y: y, W: h.pillW, H: loadingCancelPillH}
		drawGlassHintBadge(renderer, config, font, pill, h.button, h.label, x+h.pillW+gap, textY)
		rects[i] = sdl.Rect{X: x, Y: y, W: h.pillW + gap + h.labelW, H: loadingCancelPillH}
		x += h.pillW + gap + h.labelW + spread
	}
	loadingCardHitsSet(rects[0], rects[1])
}

// loadingSloganInk is the slogan's tone: between the secondary and primary
// text inks, so it reads at arm's length on the handheld without competing
// with the status line, and it follows a light preset the way the card does.
func loadingSloganInk() sdl.Color {
	return glassMix(ColorTextSecondary(), ColorTextPrimary(), 0.35)
}

// renderLoadingCard draws the modal loading overlay: spinner, status line and a
// wrapped slogan, all inside a card that fits the screen.
func renderLoadingCard(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil {
		return
	}

	// The shared modal dim and glass sheet (components.go), so the loading card
	// is the same surface as every other dialog. It was a black wash, a drop
	// shadow, a navy slab and a square 1px outline drawn straight across the
	// card's rounded corners.
	drawOverlayScrim(renderer)

	cardW, cardH := loadingCardSize()
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2
	drawOverlayCard(renderer, sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH})
	drawOverlayCardAccent(renderer, cardX, cardY, cardW)

	renderSpinner(renderer, cardX+cardW/2, cardY+62, 40, WithAlpha(glassAccent(), 220))

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
		// A failed stream's download says so, with its progress below in
		// place of the slogan (ytdownload_ui.go).
		if dl := ytDownloadCardStatus(prepareTitle); dl != "" {
			status = ellipsize(statusFont, dl, cardW-2*loadingCardPad)
		}
	}
	sw, _, _ := statusFont.SizeUTF8(status)
	renderText(renderer, config, statusFont, status, ColorTextPrimary(),
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
	if preparing && renderYtDownloadCardBody(renderer, config, sloganFont, cardX, cardY, cardW) {
		return
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
		renderText(renderer, config, sloganFont, line, loadingSloganInk(), x, y)
		y += loadingSloganLineH
	}
}
