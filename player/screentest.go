package main

// screentest.go — Tools → System → Screen test: full-screen solid colours and
// patterns for checking a display — dead and stuck pixels, backlight bleed,
// uneven colour, banding, sharpness, a picture cut off at the edges (a TV) and
// ghosting — the checks people make on a new handheld or a replaced screen.
//
// The page draws the whole screen: no status bar, no footer, nothing that
// could hide a pixel. A caption says which pattern is up and which keys do
// what, and fades three and a half seconds after the last press; X hides it at
// once. A, Right or a tap is the next pattern, Left the previous, Y steps on
// its own every three seconds (slowly: a test card is no strobe), B leaves.

import (
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The kinds of pattern.
const (
	stSolid = iota
	stGradient
	stBars
	stFine
	stGrid
	stMotion
)

// screenTestPattern is one test card.
type screenTestPattern struct {
	name string
	tip  string
	kind int
	c    sdl.Color
}

// screenTestPatterns are the cards, in the order they are stepped through:
// the solids people look for dead pixels on first.
var screenTestPatterns = [...]screenTestPattern{
	{"White", "Dark dots are dead pixels; darker patches are pressure marks", stSolid, sdl.Color{R: 255, G: 255, B: 255, A: 255}},
	{"Black", "Bright dots are stuck pixels; a glow along the edges is backlight bleed", stSolid, sdl.Color{R: 0, G: 0, B: 0, A: 255}},
	{"Red", "A dot of another colour is a stuck sub-pixel", stSolid, sdl.Color{R: 255, G: 0, B: 0, A: 255}},
	{"Green", "A dot of another colour is a stuck sub-pixel", stSolid, sdl.Color{R: 0, G: 255, B: 0, A: 255}},
	{"Blue", "A dot of another colour is a stuck sub-pixel", stSolid, sdl.Color{R: 0, G: 0, B: 255, A: 255}},
	{"Grey", "Blotches or a tint across the screen are uneven backlight", stSolid, sdl.Color{R: 128, G: 128, B: 128, A: 255}},
	{"Gradients", "Smooth ramps from dark to bright: visible steps are banding", stGradient, sdl.Color{}},
	{"Colour bars", "Eight bars, each one clean colour", stBars, sdl.Color{}},
	{"Fine lines", "One-pixel lines: they should look even, without shimmer", stFine, sdl.Color{}},
	{"Grid", "A 16-pixel grid with the edges marked: the whole picture should show", stGrid, sdl.Color{}},
	{"Motion", "A white block crossing the screen: a trail behind it is ghosting", stMotion, sdl.Color{}},
}

// screenTestCaptionMs is how long the caption stays after a press, and
// screenTestAutoMs how long each card shows in Auto.
const (
	screenTestCaptionMs = 3500
	screenTestAutoMs    = 3000
)

// screenTest is the page's state. Main thread only.
var screenTest struct {
	idx       int
	captionAt uint64
	hidden    bool
	auto      bool
	autoAt    uint64
	// The line patterns' rects, built for one canvas size.
	cacheW, cacheH int32
	fine           []sdl.Rect
	gridDim        []sdl.Rect
	gridBright     []sdl.Rect
	gridEdge       []sdl.Rect
	gridCorner     []sdl.Rect
}

// screenTestOpen opens the page (cross-area contract: the Tools hub and Home
// search call it).
func screenTestOpen(config *Config) {
	toolsPagesOpen(config, screenTestSceneName)
}

// screenTestEnter starts at the first card with the caption up.
func screenTestEnter() {
	screenTest.idx = 0
	screenTest.auto = false
	screenTest.hidden = false
	screenTest.captionAt = sdl.GetTicks64()
}

// screenTestLeave drops the cached rects with the page.
func screenTestLeave() {
	screenTest.auto = false
	screenTest.fine, screenTest.gridDim, screenTest.gridBright, screenTest.gridEdge, screenTest.gridCorner = nil, nil, nil, nil, nil
	screenTest.cacheW, screenTest.cacheH = 0, 0
}

// screenTestHubTile is the Tools hub's tile (System).
func screenTestHubTile() toolsHubTile {
	return toolsHubTile{id: "screentest", label: "Screen test", icon: "monitor", value: "Dead pixels, bleed, banding and ghosting"}
}

// screenTestStep moves delta cards and brings the caption back.
func screenTestStep(delta int) {
	n := len(screenTestPatterns)
	screenTest.idx = ((screenTest.idx+delta)%n + n) % n
	screenTest.hidden = false
	now := sdl.GetTicks64()
	screenTest.captionAt = now
	screenTest.autoAt = now
	PlayNavSound()
}

type screenTestScene struct{ sceneBase }

func (screenTestScene) render(r *sdl.Renderer, c *Config) bool {
	screenTestRender(r, c)
	return true
}

func (screenTestScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch sym := e.Keysym.Sym; {
	case sym == sdl.K_ESCAPE || sym == sdl.K_BACKSPACE || sym == sdl.K_b:
		goBackScene(c)
	case sym == sdl.K_RIGHT || sym == sdl.K_DOWN || sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER ||
		sym == sdl.K_SPACE || sym == sdl.K_PAGEDOWN || sym == sdl.K_a:
		screenTestStep(1)
	case sym == sdl.K_LEFT || sym == sdl.K_UP || sym == sdl.K_PAGEUP:
		screenTestStep(-1)
	case sym >= sdl.K_1 && sym <= sdl.K_9:
		if i := int(sym - sdl.K_1); i < len(screenTestPatterns) {
			screenTestStep(i - screenTest.idx)
		}
	case sym == sdl.K_y:
		screenTestToggleAuto()
	case sym == sdl.K_x || sym == sdl.K_h:
		screenTest.hidden = true
	}
	// Every key is the page's: Up must not reach a top bar that is not drawn.
	return true
}

func (screenTestScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(c)
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_DOWN, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		screenTestStep(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		screenTestStep(-1)
	case sdl.CONTROLLER_BUTTON_Y:
		screenTestToggleAuto()
	case sdl.CONTROLLER_BUTTON_X:
		screenTest.hidden = true
	default:
		return false
	}
	return true
}

func init() { registerScene(screenTestScene{sceneBase{name: screenTestSceneName}}) }

// screenTestToggleAuto is Y: step on a timer, or stop.
func screenTestToggleAuto() {
	screenTest.auto = !screenTest.auto
	now := sdl.GetTicks64()
	screenTest.autoAt = now
	screenTest.captionAt = now
	screenTest.hidden = false
	PlayActivateSound()
}

// screenTestClick is a click or tap: the next card.
func screenTestClick(mx, my int32) bool {
	_, _ = mx, my
	screenTestStep(1)
	return true
}

// screenTestHints are the page's keys, for the caption and the Controls card.
func screenTestHints() []FooterHint {
	auto := "Auto"
	if screenTest.auto {
		auto = "Stop auto"
	}
	return []FooterHint{
		{Button: "A", Label: "Next"},
		{Button: "Left/Right", Label: "Previous / next"},
		{Button: "Y", Label: auto},
		{Button: "X", Label: "Hide this"},
		{Button: "B", Label: "Exit"},
	}
}

func screenTestRender(renderer *sdl.Renderer, config *Config) {
	now := sdl.GetTicks64()
	if screenTest.auto && now-screenTest.autoAt >= screenTestAutoMs {
		screenTest.idx = (screenTest.idx + 1) % len(screenTestPatterns)
		screenTest.autoAt = now
		// A card stepping on its own is someone watching, not an idle screen.
		noteUserInputTime(time.Now())
	}
	w, h := screenWidth, screenHeight
	if w != screenTest.cacheW || h != screenTest.cacheH {
		screenTestBuildRects(w, h)
	}
	renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	p := screenTestPatterns[screenTest.idx]
	full := sdl.Rect{X: 0, Y: 0, W: w, H: h}
	switch p.kind {
	case stSolid:
		setDrawColor(renderer, p.c)
		_ = renderer.FillRect(&full)
	case stGradient:
		screenTestGradients(renderer, w, h)
	case stBars:
		screenTestBars(renderer, w, h)
	case stFine:
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRect(&full)
		_ = renderer.SetDrawColor(255, 255, 255, 255)
		_ = renderer.FillRects(screenTest.fine)
	case stGrid:
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRect(&full)
		_ = renderer.SetDrawColor(60, 60, 60, 255)
		_ = renderer.FillRects(screenTest.gridDim)
		_ = renderer.SetDrawColor(140, 140, 140, 255)
		_ = renderer.FillRects(screenTest.gridBright)
		_ = renderer.SetDrawColor(255, 255, 255, 255)
		_ = renderer.FillRects(screenTest.gridEdge)
		_ = renderer.SetDrawColor(255, 40, 40, 255)
		_ = renderer.FillRects(screenTest.gridCorner)
	case stMotion:
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRect(&full)
		side := h / 4
		const period = 2000
		x := int32(int64(now%period)*int64(w+side)/period) - side
		_ = renderer.SetDrawColor(255, 255, 255, 255)
		_ = renderer.FillRect(&sdl.Rect{X: x, Y: (h - side) / 2, W: side, H: side})
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	screenTestCaption(renderer, config, now)
}

// screenTestGradients draws four ramps of all 256 levels: grey across the top
// half, then red, green and blue.
func screenTestGradients(renderer *sdl.Renderer, w, h int32) {
	bands := [4][3]bool{{true, true, true}, {true, false, false}, {false, true, false}, {false, false, true}}
	tops := [5]int32{0, h / 2, h/2 + h/6, h/2 + 2*h/6, h}
	for b, mask := range bands {
		y0, y1 := tops[b], tops[b+1]
		for i := int32(0); i < 256; i++ {
			x0, x1 := i*w/256, (i+1)*w/256
			if x1 <= x0 {
				continue
			}
			v := uint8(i)
			var r, g, bl uint8
			if mask[0] {
				r = v
			}
			if mask[1] {
				g = v
			}
			if mask[2] {
				bl = v
			}
			_ = renderer.SetDrawColor(r, g, bl, 255)
			_ = renderer.FillRect(&sdl.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0})
		}
	}
}

// screenTestBars draws the eight full-strength colour bars.
func screenTestBars(renderer *sdl.Renderer, w, h int32) {
	bars := [8][3]uint8{{255, 255, 255}, {255, 255, 0}, {0, 255, 255}, {0, 255, 0}, {255, 0, 255}, {255, 0, 0}, {0, 0, 255}, {0, 0, 0}}
	for i, c := range bars {
		x0, x1 := int32(i)*w/8, int32(i+1)*w/8
		_ = renderer.SetDrawColor(c[0], c[1], c[2], 255)
		_ = renderer.FillRect(&sdl.Rect{X: x0, Y: 0, W: x1 - x0, H: h})
	}
}

// screenTestBuildRects lays the line patterns out for a w×h canvas: vertical
// one-pixel lines on the left half and horizontal ones on the right, and the
// grid with its edge and corner marks.
func screenTestBuildRects(w, h int32) {
	screenTest.cacheW, screenTest.cacheH = w, h
	fine := make([]sdl.Rect, 0, w/4+h/2+2)
	for x := int32(0); x < w/2; x += 2 {
		fine = append(fine, sdl.Rect{X: x, Y: 0, W: 1, H: h})
	}
	for y := int32(0); y < h; y += 2 {
		fine = append(fine, sdl.Rect{X: w / 2, Y: y, W: w - w/2, H: 1})
	}
	screenTest.fine = fine
	var dim, bright []sdl.Rect
	for x := int32(16); x < w; x += 16 {
		r := sdl.Rect{X: x, Y: 0, W: 1, H: h}
		if x%64 == 0 {
			bright = append(bright, r)
		} else {
			dim = append(dim, r)
		}
	}
	for y := int32(16); y < h; y += 16 {
		r := sdl.Rect{X: 0, Y: y, W: w, H: 1}
		if y%64 == 0 {
			bright = append(bright, r)
		} else {
			dim = append(dim, r)
		}
	}
	screenTest.gridDim, screenTest.gridBright = dim, bright
	screenTest.gridEdge = []sdl.Rect{
		{X: 0, Y: 0, W: w, H: 1}, {X: 0, Y: h - 1, W: w, H: 1},
		{X: 0, Y: 0, W: 1, H: h}, {X: w - 1, Y: 0, W: 1, H: h},
		{X: w/2 - 12, Y: h / 2, W: 25, H: 1}, {X: w / 2, Y: h/2 - 12, W: 1, H: 25},
	}
	const arm, th = int32(28), int32(3)
	screenTest.gridCorner = []sdl.Rect{
		{X: 0, Y: 0, W: arm, H: th}, {X: 0, Y: 0, W: th, H: arm},
		{X: w - arm, Y: 0, W: arm, H: th}, {X: w - th, Y: 0, W: th, H: arm},
		{X: 0, Y: h - th, W: arm, H: th}, {X: 0, Y: h - arm, W: th, H: arm},
		{X: w - arm, Y: h - th, W: arm, H: th}, {X: w - th, Y: h - arm, W: th, H: arm},
	}
}

// screenTestCaption draws the caption pill at the bottom while it is shown:
// the card's number and name, what to look for, and the keys.
func screenTestCaption(renderer *sdl.Renderer, config *Config, now uint64) {
	if screenTest.hidden || now-screenTest.captionAt > screenTestCaptionMs {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	p := screenTestPatterns[screenTest.idx]
	title := strconv.Itoa(screenTest.idx+1) + " / " + strconv.Itoa(len(screenTestPatterns)) + "  ·  " + p.name
	if screenTest.auto {
		title += "  ·  Auto"
	}
	keys := hintLine(screenTestHints()...)
	maxW := screenWidth - 48
	tw, _ := measureText(fonts.title, title)
	pw, _ := measureText(fonts.small, p.tip)
	kw, _ := measureText(fonts.tiny, keys)
	boxW := tw
	if pw > boxW {
		boxW = pw
	}
	if kw > boxW {
		boxW = kw
	}
	boxW += 40
	if boxW > maxW {
		boxW = maxW
	}
	boxH := fonts.titleH + fonts.smallH + fonts.tinyH + 34
	box := sdl.Rect{X: (screenWidth - boxW) / 2, Y: screenHeight - boxH - 24, W: boxW, H: boxH}
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 16, sdl.Color{R: 12, G: 14, B: 20, A: 215})
	strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, 16, 1, sdl.Color{R: 255, G: 255, B: 255, A: 60})
	inner := box.W - 40
	white := sdl.Color{R: 240, G: 242, B: 248, A: 255}
	soft := sdl.Color{R: 200, G: 204, B: 214, A: 255}
	y := box.Y + 12
	drawText(renderer, fonts.title, ellipsize(fonts.title, title, inner), box.X+20, y, white, textAlignLeft)
	y += fonts.titleH + 4
	drawText(renderer, fonts.small, ellipsize(fonts.small, p.tip, inner), box.X+20, y, soft, textAlignLeft)
	y += fonts.smallH + 6
	drawText(renderer, fonts.tiny, ellipsize(fonts.tiny, keys, inner), box.X+20, y, soft, textAlignLeft)
}
