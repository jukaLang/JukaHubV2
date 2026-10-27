package main

// header.go — the app's one top bar.
//
// Home and every scene draw the same band: the wordmark and version on the
// left, that screen's own chrome (Back, Menu) after it, and the system corner on
// the right — the bell at the margin, the release offer immediately left of it,
// and the network/weather/clock/battery cluster to its left. Before this file
// the two screens each laid that band out by hand: Home used a 26px wordmark and
// a 16px caption on a 56px-scaled band with the update offer inside the status
// cluster, while a scene used a 24px wordmark and a 24px caption on a 56/40px
// band with the offer next to the bell. Same header, four different numbers.
//
// Everything the bar decides — its height, the wordmark and caption type, the
// height every button shares, the corner's order — is decided here, so a screen
// that draws through these helpers cannot invent a fifth. Home and the
// config-driven scenes draw it through renderStatusBar; a module scene that
// keeps its own body returns true from its renderer, which is the early return
// that skips the shared bars in renderScene, so it has to call renderStatusBar
// and renderFooter itself — the music player, the terminal, About, and the
// applets (Notes, Calculator, the PDF reader, the alarm and the Pomodoro timer)
// all do. TestToolScreensDrawTheSharedBar is the guard for those applets: one
// that stops calling them comes out with no Menu pill, no Back pill and no
// wordmark, and the test says so.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// headerBrandSize is the wordmark's size in design pixels.
	headerBrandSize = 26
	// headerCaptionSize is the muted " - vX.Y.Z - Scene" line. It is also the
	// size of every secondary label in the bar, so the bar reads as one row.
	headerCaptionSize = 16
	// headerBackFontSize is two steps larger than the caption: Back is the bar's
	// primary control, and at 16px a word next to a chevron disappeared into
	// the same weight as Menu. It is drawn twice, one pixel apart, because the
	// bundled Inter has no bold face and a handheld reads weight at arm's
	// length — a 20px double-strike is the difference between "a chip next to
	// the title" and "the button you press to leave".
	headerBackFontSize = 20
	// headerBackLift is how much taller the Back pill is than the rest of the
	// bar's buttons. Same height would keep it a peer of Menu; one step taller
	// makes it the primary control without letting it fill the whole band.
	headerBackLift = int32(4)
	// headerPillMinH and headerPillMaxH bound a header button: the bar's height
	// decides where inside them it lands.
	headerPillMinH = 28
	// headerPillMaxH keeps a tall bar from turning its buttons into slabs.
	headerPillMaxH = 34
	// headerCaptionGap is the space between the wordmark and its caption, and
	// between a button's icon and its label.
	headerCaptionGap = int32(6)
	// headerCornerGap is the space between the system corner's controls.
	headerCornerGap = int32(10)
	// headerBrandText is the wordmark itself.
	headerBrandText = "JukaHub"
)

// headerBarHeight is the bar's height on this canvas. It is a band with two
// sizes rather than a scaled one, so the bar, its buttons and the page below it
// agree on where the header ends on both handheld canvases.
func headerBarHeight() int32 {
	if screenHeight > 0 && screenHeight < 600 {
		return HomeTopBarHSmall
	}
	return HomeTopBarH
}

// headerAccent is the bar's accent colour. It falls back to the design accent
// for the frames drawn before a theme is applied — the splash and the offscreen
// render tests — where accentColor is still the zero value and every accent
// control would otherwise come out black on black.
func headerAccent() sdl.Color {
	if accentColor.A == 0 {
		return ColorAccent
	}
	return accentColor
}

// headerBrandFont is the wordmark. It is one size on Home and in every scene,
// and it scales with the canvas and the accessibility setting through
// getFontSized rather than through each caller's own arithmetic.
func headerBrandFont(config *Config) *ttf.Font {
	return getFontSized(config, "small", headerBrandSize)
}

// headerCaptionFont is the muted line after the wordmark; every header button
// uses it too, so the bar's caption and its controls match.
func headerCaptionFont(config *Config) *ttf.Font {
	return getFontSized(config, "small", headerCaptionSize)
}

// headerPillHeight is the height of every header button. One number keeps Back,
// Menu and the release offer on one baseline instead of three.
func headerPillHeight() int32 {
	h := headerBarHeight() - 22
	if h < headerPillMinH {
		h = headerPillMinH
	}
	if h > headerPillMaxH {
		h = headerPillMaxH
	}
	return h
}

// headerBackPillHeight is the Back control's own height: the shared button
// height plus one step, clamped so the pill still sits clear of the bar's top
// and bottom edges on the compact canvas.
func headerBackPillHeight() int32 {
	h := headerPillHeight() + headerBackLift
	if limit := headerBarHeight() - 8; h > limit {
		h = limit
	}
	return h
}

// headerCaptionText is the muted line after the wordmark: the version, and the
// scene on every screen but Home.
func headerCaptionText(version, scene string) string {
	var b strings.Builder
	if v := strings.TrimSpace(version); v != "" {
		b.WriteString(" - ")
		b.WriteString(v)
	}
	if s := strings.TrimSpace(scene); s != "" {
		b.WriteString(" - ")
		b.WriteString(s)
	}
	return b.String()
}

// drawHeaderBrand paints the wordmark and its caption and returns the x just
// past them, which is where the screen's own chrome starts.
func drawHeaderBrand(renderer *sdl.Renderer, config *Config, barH int32, caption string) int32 {
	x := StatusBarMargin
	if renderer == nil {
		return x
	}
	brand := headerBrandFont(config)
	if brand == nil {
		return x
	}
	bw, _ := measureText(brand, headerBrandText)
	bh := int32(brand.Height())
	renderText(renderer, config, brand, headerBrandText, ColorTextPrimary(), x, (barH-bh)/2)
	x += bw
	captionFont := headerCaptionFont(config)
	if caption == "" || captionFont == nil {
		return x
	}
	cw, _ := measureText(captionFont, caption)
	ch := int32(captionFont.Height())
	renderText(renderer, config, captionFont, caption, ColorTextSecondary(), x+headerCaptionGap, (barH-ch)/2)
	return x + headerCaptionGap + cw
}

// headerBackLabel is the word on the Back button. It replaced a bare "<" glyph,
// which read as punctuation at the bar's size rather than as a control, and was
// drawn in a 24px font inside a 22px pill.
const headerBackLabel = "Back"

// headerBackChevronW is the drawn arrow's advance inside the Back pill: the
// strokes span six pixels plus the two the weight pass adds.
const headerBackChevronW = int32(11)

// drawHeaderBack paints the Back control and returns the rect it drew, so the
// click handler and the focus graph use the same geometry. It is deliberately
// the loudest control in the bar: an accent-tinted pill while idle, a solid
// accent pill with dark type while the D-pad holds it — the colour change is the
// focus indicator, so a controller user can see it without a pointer — and a
// chevron drawn as strokes because no glyph is this thick at this size.
func drawHeaderBack(renderer *sdl.Renderer, config *Config, x, barH int32, focused bool) sdl.Rect {
	font := getFontSized(config, "small", headerBackFontSize)
	if renderer == nil || font == nil {
		return sdl.Rect{}
	}
	h := headerBackPillHeight()
	pad := int32(14)
	tw, _ := measureText(font, headerBackLabel)
	w := pad + headerBackChevronW + headerCaptionGap + tw + pad
	rect := sdl.Rect{X: x, Y: (barH - h) / 2, W: w, H: h}

	accent := headerAccent()
	fill := WithAlpha(accent, 38)
	border := WithAlpha(accent, 225)
	text := lighten(accent, 60)
	if focused {
		fill = accent
		border = accent
		text = ColorTopBar
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, 1, border)
	if focused {
		renderer.SetDrawColor(accent.R, accent.G, accent.B, 230)
		renderer.DrawRect(&sdl.Rect{X: rect.X - 3, Y: rect.Y - 3, W: rect.W + 6, H: rect.H + 6})
	}

	arrowX := rect.X + pad
	cy := rect.Y + h/2
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(text.R, text.G, text.B, 255)
	// The chevron points left, the direction the control moves: its tip is at
	// arrowX and its arms open to the right. Four passes of the same two
	// strokes, one pixel apart, because a single 1px line reads as a hairline
	// on the panel and this is the control that has to be unmistakable.
	for i := int32(0); i < 4; i++ {
		renderer.DrawLine(arrowX+6+i, cy-4, arrowX+i, cy)
		renderer.DrawLine(arrowX+6+i, cy+4, arrowX+i, cy)
	}
	labelX := arrowX + headerBackChevronW + headerCaptionGap
	labelY := rect.Y + (h-int32(font.Height()))/2
	renderText(renderer, config, font, headerBackLabel, text, labelX, labelY)
	// The faux-bold pass: the bundled Inter ships only a regular face, so the
	// weight comes from drawing the same cached glyphs again one pixel right.
	// The texture is already cached, so this costs one blit.
	renderText(renderer, config, font, headerBackLabel, text, labelX+1, labelY)
	return rect
}

// headerMenuLabel is the app-menu button's label. "Menu" says what it opens and
// cannot collide with a scene title the way the old "Apps" button did on the
// Apps screen.
const headerMenuLabel = "Menu"

// drawHeaderMenu paints the app-menu button and returns its rect.
func drawHeaderMenu(renderer *sdl.Renderer, config *Config, x, barH int32, open, focused bool) sdl.Rect {
	font := headerCaptionFont(config)
	if renderer == nil || font == nil {
		return sdl.Rect{}
	}
	h := headerPillHeight()
	pad := int32(12)
	tw, _ := measureText(font, headerMenuLabel)
	rect := sdl.Rect{X: x, Y: (barH - h) / 2, W: tw + pad*2, H: h}
	accent := headerAccent()
	fill := ColorIconSurface
	if open {
		fill = WithAlpha(accent, 70)
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, 1, ColorBorder)
	renderText(renderer, config, font, headerMenuLabel, ColorTextSecondary(),
		rect.X+pad, rect.Y+(h-int32(font.Height()))/2)
	if focused {
		renderer.SetDrawColor(accent.R, accent.G, accent.B, 230)
		renderer.DrawRect(&sdl.Rect{X: rect.X - 3, Y: rect.Y - 3, W: rect.W + 6, H: rect.H + 6})
	}
	return rect
}
