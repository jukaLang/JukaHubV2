package main

// stageui.go — the glass card both ends of a session are drawn on.
//
// The boot splash and the shutdown teardown answer the same question ("what is
// the hub doing, and how far through is it?") on the same pane of liquid glass
// (bootliquid.go): the JukaHub wordmark, what the card is doing, a real
// progress track and the list of steps that actually ran, each with its result.
// Sharing the drawing keeps the two ends of a session looking like one product.
//
// The per-frame cost is cached shapes and cached text: the snapshot is filled
// into a reused buffer, the count and status strings are rebuilt only when the
// queue changes, and text fades in quarter steps so a fade does not rasterise
// every label once per frame.

import (
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// stageState is how one step of a stage is doing.
type stageState uint8

const (
	stagePending stageState = iota
	stageActive
	stageDone
	stageFailed
)

// stageItem is one row on the card: the step, its result, and its state.
type stageItem struct {
	Label  string
	Detail string
	State  stageState
}

// stageSnapshot is a view of a stage, handed to the renderer so the drawing
// code never reads a mutating structure.
type stageSnapshot struct {
	Title   string
	Items   []stageItem
	Done    int
	Total   int
	Active  int
	Failed  int
	Current string
	Detail  string
}

// fraction is the share of steps that have finished, failed included: a failed
// step is not a step the boot is still waiting on.
func (s stageSnapshot) fraction() float64 {
	if s.Total <= 0 {
		return 1
	}
	return float64(s.Done) / float64(s.Total)
}

// stageTrack is a stage driven step by step (the teardown). The boot queue
// builds the same snapshot from its own state, because its steps are dynamic.
type stageTrack struct {
	mu    sync.Mutex
	title string
	items []stageItem
}

func newStageTrack(title string, labels []string) *stageTrack {
	t := &stageTrack{}
	t.reset(title, labels)
	return t
}

func (t *stageTrack) reset(title string, labels []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.title = title
	t.items = make([]stageItem, len(labels))
	for i, label := range labels {
		t.items[i] = stageItem{Label: label, State: stagePending}
	}
}

func (t *stageTrack) beginAt(i int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < 0 || i >= len(t.items) {
		return
	}
	t.items[i].State = stageActive
}

func (t *stageTrack) finishAt(i int, detail string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < 0 || i >= len(t.items) {
		return
	}
	if detail != "" {
		t.items[i].Detail = detail
	}
	if err != nil {
		t.items[i].State = stageFailed
		return
	}
	t.items[i].State = stageDone
}

// snapshotInto fills dst, reusing dst.Items.
func (t *stageTrack) snapshotInto(dst *stageSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	dst.Title = t.title
	dst.Active = -1
	dst.Total = len(t.items)
	dst.Done, dst.Failed = 0, 0
	dst.Current, dst.Detail = "", ""
	dst.Items = append(dst.Items[:0], t.items...)
	for i, item := range t.items {
		switch item.State {
		case stageDone, stageFailed:
			dst.Done++
		case stageActive:
			// Several rows run at once in a teardown group; the status line
			// names the same one the list centres on, the first.
			if dst.Active < 0 {
				dst.Active = i
				dst.Current = item.Label
				dst.Detail = item.Detail
			}
		}
		if item.State == stageFailed {
			dst.Failed++
		}
	}
}

func (t *stageTrack) snapshot() stageSnapshot {
	var snap stageSnapshot
	t.snapshotInto(&snap)
	return snap
}

// ── Drawing ──────────────────────────────────────────────────────────────

const (
	stageCardW      = int32(720)
	stageCardH      = int32(460)
	stageCardRadius = int32(28)
)

// stageCardRect is the card's screen rect, shrunk to fit a small canvas.
func stageCardRect() sdl.Rect {
	w, h := stageCardW, stageCardH
	if w > screenWidth-64 {
		w = screenWidth - 64
	}
	if h > screenHeight-64 {
		h = screenHeight - 64
	}
	return sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
}

// stageCardStyle is one frame's look of the card: how present the pane and its
// content are, where the wordmark is in its materialise / dissolve, and the
// strings for the footer.
type stageCardStyle struct {
	t       float64 // seconds since the card came up (drives the liquid)
	pane    float64 // 0..1: the glass pane's presence
	content float64 // 0..1: the text and rows on it
	scale   float64 // the pane's scale about its centre (1 = at rest)
	word    float64 // 0..1: the wordmark's materialise progress
	glint   float64 // 0..1: a glint crossing the wordmark (0 = none)
	motion  bool    // false under reduced motion
	footer  string
	hint    string
}

// stageWordSize is the wordmark's design size: the hero of the card.
const stageWordSize = 88

// stageWordFont is the wordmark face: Inter at the hero size, through the
// shared font cache (so it follows the Inter-only rule and the accessibility
// scale, and closes with the rest of the cache).
func stageWordFont(config *Config) *ttf.Font {
	if config == nil {
		return nil
	}
	return getFontSized(config, "big", stageWordSize)
}

// stageStrings caches the formatted strings a frame shows, rebuilt only when
// their inputs change, so the card does not format a string per frame.
var stageStrings struct {
	countDone, countTotal int
	count                 string
	statusCur, statusDet  string
	status                string
	kickerTitle           string
	kicker                string
}

func stageCountText(done, total int) string {
	s := &stageStrings
	if s.count == "" || s.countDone != done || s.countTotal != total {
		s.countDone, s.countTotal = done, total
		s.count = strconv.Itoa(done) + " of " + strconv.Itoa(total)
	}
	return s.count
}

func stageStatusText(current, detail string, done, total int) string {
	if current == "" {
		if total > 0 && done >= total {
			current = "Ready"
		} else {
			current = "Starting up"
		}
	}
	s := &stageStrings
	if s.status == "" || s.statusCur != current || s.statusDet != detail {
		s.statusCur, s.statusDet = current, detail
		s.status = current
		if detail != "" {
			s.status += " · " + detail
		}
	}
	return s.status
}

func stageKickerText(title string) string {
	s := &stageStrings
	if s.kicker == "" || s.kickerTitle != title {
		s.kickerTitle = title
		s.kicker = strings.ToUpper(title)
	}
	return s.kicker
}

// stageInk scales a text colour's alpha by a, quantised to quarter steps: text
// textures are cached per colour, so a continuous fade would rasterise every
// label once per frame, while four steps read as a smooth fade at 30 fps.
func stageInk(c sdl.Color, a float64) sdl.Color {
	q := math.Round(bootClamp01(a)*4) / 4
	c.A = uint8(math.Round(float64(c.A) * q))
	return c
}

// stageFade scales a shape colour's alpha by a (shapes alpha-modulate one
// cached texture, so they fade continuously for free).
func stageFade(c sdl.Color, a float64) sdl.Color {
	c.A = uint8(math.Round(float64(c.A) * bootClamp01(a)))
	return c
}

// stageDrawBackdrop paints the backdrop the card floats on: the liquid one when
// the kit is ready, Home's own otherwise.
func stageDrawBackdrop(renderer *sdl.Renderer, config *Config, elapsedMS float64) {
	if renderer == nil {
		return
	}
	if bootLiquid.ready && bootLiquid.renderer == renderer {
		bootLiquidDrawBackdrop(renderer, elapsedMS/1000, 1, !effectiveReducedMotion(config))
		return
	}
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
}

// stageDrawCard paints the pane and its contents: the wordmark, what the card
// is doing, the live step, the progress track, the step list and a footer.
func stageDrawCard(renderer *sdl.Renderer, config *Config, snap *stageSnapshot, st stageCardStyle) {
	if renderer == nil || snap == nil {
		return
	}
	card := stageCardRect()
	if card.W <= 0 || card.H <= 0 {
		return
	}
	radius := stageCardRadius
	if radius > card.W/6 {
		radius = card.W / 6
	}
	pad := scaleX(40, screenWidth)
	if pad*2 > card.W {
		pad = card.W / 8
	}
	if st.scale <= 0 {
		st.scale = 1
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetClipRect(nil)

	liquid := bootLiquid.ready && bootLiquid.renderer == renderer
	if liquid {
		bootLiquidDrawSheet(renderer, card, st.pane, st.scale, st.t, st.motion)
	} else if st.pane > 0 {
		// The flat glass card: what a renderer that refused the liquid
		// textures still gets.
		fillRoundedRect(renderer, card.X, card.Y+scaleY(10, screenHeight), card.W, card.H, radius, stageFade(sdl.Color{R: 0, G: 0, B: 0, A: 96}, st.pane))
		fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, radius, stageFade(glassSheetFill(), st.pane))
		strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, radius, 1, stageFade(glassSheetEdge(), st.pane))
	}
	content := bootClamp01(st.content)
	if content <= 0 && st.word <= 0 {
		return
	}

	small, _ := getCachedFont(config, "small")
	cx := card.X + card.W/2

	// The wordmark, centred at the top of the pane.
	wordTop := card.Y + pad*3/4
	wordH := bootLiquidWordHeight()
	drewWord := false
	if liquid && wordH > 0 {
		_, drewWord = bootLiquidDrawWordmark(renderer, cx, wordTop, st.word, bootClamp01(st.pane), st.glint, ColorTextPrimary(), st.motion)
	}
	if !drewWord {
		font := stageWordFont(config)
		if font == nil {
			font = small
		}
		if font != nil {
			ww, wh := measureText(font, "JukaHub")
			if wh > card.H/4 {
				font = small
				ww, wh = measureText(font, "JukaHub")
			}
			wordH = wh
			if a := math.Min(st.word, content); a > 0 {
				renderText(renderer, config, font, "JukaHub", stageInk(ColorTextPrimary(), a), cx-ww/2, wordTop)
			}
		}
	}
	if small == nil || content <= 0 {
		return
	}
	_, smallH := measureText(small, "Ag")

	// Kicker: what this card is doing, under the wordmark.
	y := wordTop + wordH + scaleY(4, screenHeight)
	if snap.Title != "" {
		kicker := stageKickerText(snap.Title)
		kw, _ := measureText(small, kicker)
		renderText(renderer, config, small, kicker, stageInk(WithAlpha(glassAccent(), 220), content), cx-kw/2, y)
	}
	y += smallH + scaleY(18, screenHeight)

	// Progress track, the full width of the pane's content.
	trackX := card.X + pad
	trackW := card.W - 2*pad
	trackH := scaleY(6, screenHeight)
	if trackH < 4 {
		trackH = 4
	}
	fillRoundedRect(renderer, trackX, y, trackW, trackH, trackH/2, stageFade(glassA(24), content))
	if frac := snap.fraction(); frac > 0 {
		fw := int32(float64(trackW) * frac)
		if fw < trackH {
			fw = trackH
		}
		accent := glassAccent()
		fillRoundedRect(renderer, trackX, y, fw, trackH, trackH/2, stageFade(accent, content))
		headX := trackX + fw
		pulse := 0.5
		if st.motion {
			pulse = 0.5 + 0.5*math.Sin(st.t*5)
		}
		fillCircle(renderer, headX, y+trackH/2, trackH+4, stageFade(sdl.Color{R: accent.R, G: accent.G, B: accent.B, A: uint8(40 + 30*pulse)}, content))
		fillCircle(renderer, headX, y+trackH/2, trackH/2+1, stageFade(sdl.Color{R: 255, G: 255, B: 255, A: 230}, content))
	}
	y += trackH + scaleY(14, screenHeight)

	// Status: the step headlining now, its result, and the count.
	count := stageCountText(snap.Done, snap.Total)
	cw, _ := measureText(small, count)
	status := stageStatusText(snap.Current, snap.Detail, snap.Done, snap.Total)
	maxW := trackW - cw - scaleX(24, screenWidth)
	if maxW < 40 {
		maxW = trackW
	}
	renderText(renderer, config, small, ellipsize(small, status, maxW), stageInk(ColorTextPrimary(), content), trackX, y)
	if maxW < trackW {
		renderText(renderer, config, small, count, stageInk(ColorTextMuted(), content), trackX+trackW-cw, y)
	}
	y += smallH + scaleY(16, screenHeight)

	// Footer line, then the steps between. The window follows the headline
	// row, so a long queue never hides the step that is actually running.
	footY := card.Y + card.H - pad*3/4 - smallH
	rowH := scaleY(28, screenHeight)
	if rowH < smallH+2 {
		rowH = smallH + 2
	}
	maxRows := int((footY - scaleY(8, screenHeight) - y) / rowH)
	if maxRows > len(snap.Items) {
		maxRows = len(snap.Items)
	}
	start := 0
	if len(snap.Items) > maxRows && maxRows > 0 {
		start = len(snap.Items) - maxRows
		if snap.Active >= 0 {
			start = snap.Active - maxRows/2
		}
		if start < 0 {
			start = 0
		}
		if start > len(snap.Items)-maxRows {
			start = len(snap.Items) - maxRows
		}
	}
	pulse := 0.5
	if st.motion {
		pulse = 0.5 + 0.5*math.Sin(st.t*6)
	}
	for row := 0; row < maxRows && start+row < len(snap.Items); row++ {
		item := snap.Items[start+row]
		ry := y + int32(row)*rowH
		glyphR := rowH/6 + 2
		stageDrawGlyph(renderer, trackX+glyphR, ry+rowH/2, item.State, glyphR, pulse, content)
		labelX := trackX + 2*glyphR + scaleX(12, screenWidth)
		labelW := trackW - 2*glyphR - scaleX(12, screenWidth)
		textY := ry + (rowH-smallH)/2
		if item.Detail != "" {
			detail := ellipsize(small, item.Detail, trackW/3)
			dw, _ := measureText(small, detail)
			ink := ColorTextMuted()
			if item.State == stageFailed {
				ink = sdl.Color{R: 236, G: 112, B: 108, A: 255}
			}
			renderText(renderer, config, small, detail, stageInk(ink, content), trackX+trackW-dw, textY)
			labelW -= dw + scaleX(12, screenWidth)
		}
		if labelW < 40 {
			labelW = 40
		}
		ink := ColorTextMuted()
		if item.State == stageActive {
			ink = ColorTextPrimary()
		}
		renderText(renderer, config, small, ellipsize(small, item.Label, labelW), stageInk(ink, content), labelX, textY)
	}

	if st.footer != "" {
		renderText(renderer, config, small, ellipsize(small, st.footer, trackW/2), stageInk(ColorTextTertiary(), content), trackX, footY)
	}
	if st.hint != "" {
		hint := ellipsize(small, st.hint, trackW/2)
		hw, _ := measureText(small, hint)
		renderText(renderer, config, small, hint, stageInk(ColorTextTertiary(), content), trackX+trackW-hw, footY)
	}
}

// stageDrawGlyph is one row's state, in primitives: a filled accent dot with a
// tick for a finished step, a pulsing dot for the running one, a red dot for a
// refusal and a hollow dot for what has not started.
func stageDrawGlyph(renderer *sdl.Renderer, cx, cy int32, st stageState, r int32, pulse, alpha float64) {
	if renderer == nil || alpha <= 0 {
		return
	}
	if r < 2 {
		r = 2
	}
	accent := glassAccent()
	switch st {
	case stageDone:
		fillCircle(renderer, cx, cy, r, stageFade(accent, alpha))
		setDrawColor(renderer, stageFade(glassMix(ColorBackground, glassInk(), 0.9), alpha))
		_ = renderer.DrawLine(cx-r/2, cy, cx-1, cy+r/2)
		_ = renderer.DrawLine(cx-1, cy+r/2, cx+r/2, cy-r/2)
	case stageFailed:
		fillCircle(renderer, cx, cy, r, stageFade(sdl.Color{R: 236, G: 112, B: 108, A: 255}, alpha))
		setDrawColor(renderer, stageFade(sdl.Color{R: 255, G: 240, B: 238, A: 240}, alpha))
		_ = renderer.FillRect(&sdl.Rect{X: cx - max32(1, r/4)/2, Y: cy - r/2, W: max32(1, r/4), H: r * 3 / 4})
	case stageActive:
		rr := r + int32(pulse*2)
		fillCircle(renderer, cx, cy, rr+r/2, stageFade(sdl.Color{R: accent.R, G: accent.G, B: accent.B, A: uint8(30 + 26*pulse)}, alpha))
		fillCircle(renderer, cx, cy, rr, stageFade(sdl.Color{R: accent.R, G: accent.G, B: accent.B, A: 235}, alpha))
		fillCircle(renderer, cx, cy, max32(1, r/2), stageFade(glassMix(ColorBackground, glassInk(), 0.55), alpha))
	default:
		fillCircle(renderer, cx, cy, r, stageFade(glassA(26), alpha))
		fillCircle(renderer, cx, cy, max32(1, r-2), stageFade(glassA(6), alpha))
	}
}

// drawParticle draws a small filled circle with a soft edge, using horizontal
// lines. The fireworks and the idle screen's drifting motes draw through it.
func drawParticle(renderer *sdl.Renderer, cx, cy, r int32, red, green, blue, alpha uint8) {
	if r <= 0 || alpha == 0 {
		return
	}
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for dy := -r; dy <= r; dy++ {
		dx := int32(math.Sqrt(float64(r*r - dy*dy)))
		edgeFade := 1.0 - float64(abs(int(dy)))/float64(r)
		a := uint8(float64(alpha) * edgeFade * edgeFade)
		renderer.SetDrawColor(red, green, blue, a)
		_ = renderer.DrawLine(cx-dx, cy+dy, cx+dx, cy+dy)
	}
}
