package main

// gameswitcher_ui.go — drawing the Game switcher (gameswitcher.go).
//
// One row of cards across the screen, the game in view in the middle at full
// size and its neighbours smaller and dimmed on either side. Each card is a picture of where that game was left — the
// state screenshot RetroArch wrote beside its save — letterboxed on a dark
// "screen", or its box art, or for a game with neither a plain card with its
// console's name. Under the strip: the title, the console and when the game
// was auto-saved (or played, for an emulator that keeps no auto state).
//
// The strip slides: its position eases toward the cursor every frame (an
// exponential follow, so a held direction or a jump to an end glides rather
// than snaps) and asks for a frame burst while it moves, as the focus bounce
// does (homefocusglide.go). Reduced motion and Battery Saver snap it.
//
// The page is a tool screen's frame (tbxui.go): Home's backdrop, the page
// header with the count opposite the title, the status bar and the footer
// with the screen's own buttons, so it wears the current theme like the
// screens around it. Nothing is allocated per frame: the words under the
// strip are rebuilt only when the card or the second changes.

import (
	"math"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gswSideScale is a neighbour's size against the card in view.
	gswSideScale = 0.78
	// gswSlideTau is the strip's ease time constant in milliseconds: 95% of
	// a step in about three of them.
	gswSlideTau = 55.0
	// gswGap is the space between cards on a 1280-wide canvas.
	gswGap = 28
	// gswDimAlpha is how dark a neighbour gets.
	gswDimAlpha = 120
)

var (
	gswHints      = []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Resume"}, {Button: "X", Label: "Remove"}, {Button: "B", Label: "Back"}}
	gswHintsEmpty = []FooterHint{{Button: "B", Label: "Back"}}
	gswScreenInk  = sdl.Color{R: 0, G: 0, B: 0, A: 210}
)

// gswRender draws the screen. A fresh visit, or a game started since the
// cards were read, reads them again.
func gswRender(renderer *sdl.Renderer, config *Config) {
	now := uiClockMS()
	if gsw.visit.begin() || (gsw.loaded && !gswRecentsStamp().Equal(gsw.stamp)) {
		gswEnter()
	}
	gswStep(config, now)
	gswTake(renderer)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	gswInfo(now)
	header := tbxHeader("Game switcher", "Pick up where you left off", "games", gsw.infoCount, ColorTextSecondary())
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	hints := gswHints
	switch {
	case !gsw.loaded:
		drawText(renderer, fonts.small, "Reading your recent games…", body.X+body.W/2, body.Y+(body.H-fonts.smallH)/2,
			ColorTextSecondary(), textAlignCenter)
	case len(gsw.cards) == 0:
		hints = gswHintsEmpty
		gswDrawEmpty(renderer, config, fonts, body)
	default:
		gswDrawStrip(renderer, config, fonts, body)
	}
	peerEndFrame(renderer, config, hints)
}

// gswMotion reports whether the strip slides: not with reduced motion, nor
// under Battery Saver, which keeps its frame-rate cap and its stillness.
func gswMotion(config *Config) bool {
	return !effectiveReducedMotion(config) && currentPowerMode(config) != PowerModeBatterySaver && !batterySaverActive
}

// gswStep eases the strip toward the cursor.
func gswStep(config *Config, now uint64) {
	dt := 16.0
	if gsw.lastMS > 0 && now > gsw.lastMS {
		dt = float64(now - gsw.lastMS)
	}
	gsw.lastMS = now
	if dt > 100 {
		dt = 100 // a stall (a slow frame, the screen away) is not a jump
	}
	target := float64(gsw.cursor)
	diff := target - gsw.pos
	if math.Abs(diff) < 0.002 || !gswMotion(config) {
		gsw.pos = target
		return
	}
	gsw.pos += diff * (1 - math.Exp(-dt/gswSlideTau))
	requestFrameBurst()
}

// gswInfo rebuilds the words under the strip and the header's count when the
// card, the number of cards or the second has changed.
func gswInfo(now uint64) {
	n := len(gsw.cards)
	if gsw.infoFor == gsw.cursor && gsw.infoLen == n && now-gsw.infoAt < 1000 {
		return
	}
	gsw.infoFor, gsw.infoLen, gsw.infoAt = gsw.cursor, n, now
	gsw.infoTitle, gsw.infoLine, gsw.infoCount = "", "", ""
	if !gsw.loaded || gsw.cursor < 0 || gsw.cursor >= n {
		return
	}
	if n > 1 {
		gsw.infoCount = strconv.Itoa(gsw.cursor+1) + " / " + strconv.Itoa(n)
	}
	c := &gsw.cards[gsw.cursor]
	gsw.infoTitle = gswTitle(c.game)
	line := c.game.System
	when := ""
	switch {
	case !c.auto.IsZero():
		when = "Auto-saved " + gmetaWhen(c.auto, time.Now())
	case !c.game.PlayedAt.IsZero():
		when = "Played " + gmetaWhen(c.game.PlayedAt, time.Now())
	}
	if when != "" {
		if line != "" {
			line += " · "
		}
		line += when
	}
	gsw.infoLine = line
}

// gswCardRect places the card d positions from the strip's centre (d may be
// fractional while it slides). t is how far it is from being the card in
// view: 0 in the middle, 1 for a neighbour and beyond.
func gswCardRect(d float64, cardW, cardH, cx, cy, gap int32) (sdl.Rect, float64) {
	a := math.Abs(d)
	t := math.Min(a, 1)
	scale := 1 - (1-gswSideScale)*t
	near := float64(cardW)*(1+gswSideScale)/2 + float64(gap)
	far := float64(cardW)*gswSideScale + float64(gap)
	off := t*near + math.Max(a-1, 0)*far
	if d < 0 {
		off = -off
	}
	w := int32(float64(cardW) * scale)
	h := int32(float64(cardH) * scale)
	return sdl.Rect{X: cx + int32(math.Round(off)) - w/2, Y: cy - h/2, W: w, H: h}, t
}

// gswDrawStrip draws the cards and, under them, the card in view's words.
func gswDrawStrip(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	head := FontForRole(config, TypeHeading)
	if head == nil {
		head = fonts.title
	}
	headH := int32(head.Height())
	infoH := headH + 6 + fonts.smallH
	areaTop := body.Y + 8
	areaH := body.H - infoH - 28
	if areaH < 60 {
		areaH = 60
	}
	cardH := areaH
	cardW := cardH * 4 / 3
	if maxW := body.W * 58 / 100; cardW > maxW {
		cardW = maxW
		cardH = cardW * 3 / 4
	}
	cx := body.X + body.W/2
	cy := areaTop + areaH/2
	gap := scaleX(gswGap, screenWidth)
	// The ring sits on the card in view unless the top bar has the focus:
	// two rings would claim one press (tbxChromeFocused).
	ringOn := !tbxChromeFocused()
	near := int(math.Round(gsw.pos))
	// The neighbours first, the card nearest the middle last, so a card
	// sliding past never covers the one arriving.
	for pass := 0; pass < 2; pass++ {
		for i := range gsw.cards {
			if (i == near) != (pass == 1) {
				continue
			}
			d := float64(i) - gsw.pos
			if math.Abs(d) > 3.5 {
				continue
			}
			r, t := gswCardRect(d, cardW, cardH, cx, cy, gap)
			if r.X+r.W < 0 || r.X > screenWidth {
				continue
			}
			gswDrawCard(renderer, fonts, &gsw.cards[i], r, t, ringOn && i == gsw.cursor)
		}
	}
	y := areaTop + areaH + 20
	maxW := body.W * 80 / 100
	drawText(renderer, head, ellipsize(head, gsw.infoTitle, maxW), cx, y, ColorTextPrimary(), textAlignCenter)
	drawText(renderer, fonts.small, ellipsize(fonts.small, gsw.infoLine, maxW), cx, y+headH+6, ColorTextSecondary(), textAlignCenter)
}

// gswDrawCard draws one card: its picture (or the console's name), the ring
// when it is the one A starts, and the dimming of a neighbour.
func gswDrawCard(renderer *sdl.Renderer, fonts tbxFonts, c *gswCard, r sdl.Rect, t float64, focused bool) {
	radius := homeTileRadius
	if focused {
		strokeRoundedRect(renderer, r.X-5, r.Y-5, r.W+10, r.H+10, radius+5, 3, glassAccent())
	}
	fill := glassSheetFill()
	fill.A = 235
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, fill)
	inset := max32(4, radius/3)
	inner := sdl.Rect{X: r.X + inset, Y: r.Y + inset, W: r.W - 2*inset, H: r.H - 2*inset}
	switch {
	case c.tex != nil && c.texW > 0 && c.texH > 0 && inner.W > 0 && inner.H > 0:
		if c.fromShot {
			// A state screenshot sits on a dark screen, letterboxed like the
			// game's own picture on the handheld.
			fillRoundedRect(renderer, inner.X, inner.Y, inner.W, inner.H, max32(radius-inset, 2), gswScreenInk)
		}
		dst := gswFit(c.texW, c.texH, inner)
		_ = renderer.Copy(c.tex, nil, &dst)
	default:
		size := min32(inner.W, inner.H) / 3
		iconY := inner.Y + inner.H/2 - fonts.smallH/2 - 4
		drawTileIcon(renderer, inner.X+inner.W/2, iconY, size, "games", ColorTextSecondary(), fill)
		if c.game.System != "" {
			drawText(renderer, fonts.small, ellipsize(fonts.small, c.game.System, inner.W-16),
				inner.X+inner.W/2, iconY+size/2+8, ColorTextSecondary(), textAlignCenter)
		}
	}
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, glassSheetEdge())
	if t > 0.01 {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, sdl.Color{A: uint8(gswDimAlpha * t)})
	}
}

// gswFit is the largest rect of a w×h picture's shape inside box, centred.
func gswFit(w, h int32, box sdl.Rect) sdl.Rect {
	scale := math.Min(float64(box.W)/float64(w), float64(box.H)/float64(h))
	fw := int32(float64(w) * scale)
	fh := int32(float64(h) * scale)
	return sdl.Rect{X: box.X + (box.W-fw)/2, Y: box.Y + (box.H-fh)/2, W: fw, H: fh}
}

// gswDrawEmpty is the screen before any game was played (or after the last
// one was removed): what will be here, and B back.
func gswDrawEmpty(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	head := FontForRole(config, TypeHeading)
	if head == nil {
		head = fonts.title
	}
	cx := body.X + body.W/2
	size := min32(body.H/4, 96)
	y := body.Y + body.H/2 - size
	drawTileIcon(renderer, cx, y, size, "games", ColorTextSecondary(), glassSheetFill())
	y += size/2 + 16
	drawText(renderer, head, "No recent games yet", cx, y, ColorTextPrimary(), textAlignCenter)
	y += int32(head.Height()) + 6
	drawText(renderer, fonts.small, ellipsize(fonts.small, "Games you play show up here, each where you left it", body.W-32),
		cx, y, ColorTextSecondary(), textAlignCenter)
}
