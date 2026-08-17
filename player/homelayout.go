package main

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// homeLayoutState stores computed home screen layout for render/mouse hit-testing sync
type homeLayout struct {
	Cols        int32      // columns in the grid (4 / 3 / 2)
	TileRects   []sdl.Rect // index 0..n for the home tiles
	RecentRect  sdl.Rect   // Continue panel
	TileElement []int      // slot -> scene element index (0-7) or -1 if missing
	HeadingRect sdl.Rect   // page heading area (optional)
}

// computeHomeLayout returns the shared home-screen geometry for render + mouse.
// tileCount is the number of tile-style buttons in the current home scene.
// Layout is deterministic and side-effect free.
func computeHomeLayout(screenW, screenH int32, tileCount int) homeLayout {
	margin := HomeMargin
	if screenW < 1024 {
		margin = 20
	}
	if screenW < 720 {
		margin = 16
	}

	topBarH := HomeTopBarH
	if screenH < 600 {
		topBarH = HomeTopBarHSmall
	}
	footerH := HomeFooterH
	if screenH < 600 {
		footerH = HomeFooterHSmall
	}

	cols := HomeCols
	if screenW < 1024 {
		cols = HomeColsSmall
	}
	if screenW < 720 {
		cols = HomeColsTiny
	}

	gap := HomeCardGap
	if screenW < 1024 {
		gap = 14
	}

	contentW := screenW - 2*margin

	// Safe area: from topBarH to screenH - footerH.
	safeTop := topBarH + SpaceLG
	safeBottom := screenH - footerH - SpaceLG
	if safeBottom < safeTop {
		safeBottom = safeTop + 10
	}

	// Heading area (compact, below status bar).
	headingH := int32(28)
	headingY := safeTop
	headingRect := sdl.Rect{X: margin, Y: headingY, W: contentW, H: headingH}

	// Continue panel.
	continueW := contentW
	continueH := HomeContinueH
	if screenH < 600 {
		continueH = 96
	}
	continueX := margin
	continueY := headingY + headingH + SpaceSM
	recent := sdl.Rect{X: continueX, Y: continueY, W: continueW, H: continueH}

	// Grid area between Continue and footer.
	gridTop := continueY + continueH + HomeSectionGap
	gridBottom := safeBottom
	if gridBottom < gridTop {
		gridBottom = gridTop + 10
	}

	cardW := (contentW - gap*(cols-1)) / cols
	if cardW > 320 {
		cardW = 320
	}
	if cardW < 120 {
		cardW = 120
	}

	// Fit up to 2 rows; cap card height.
	rows := int32(1)
	if tileCount > int(cols) {
		rows = 2
	}
	availH := gridBottom - gridTop
	cardH := HomeCardHMax
	if rows == 2 {
		needed := HomeCardHMax*2 + gap
		if needed > availH {
			cardH = (availH - gap) / 2
		}
	} else {
		cardH = availH
		if cardH > HomeCardHMax {
			cardH = HomeCardHMax
		}
	}
	if cardH < HomeCardHMin {
		cardH = HomeCardHMin
	}
	if cardH > HomeCardHMax {
		cardH = HomeCardHMax
	}

	gridH := rows*cardH + gap*(rows-1)
	gridY := gridTop
	if gridBottom-gridTop > gridH {
		gridY = gridTop + (gridBottom-gridTop-gridH)/2
	}

	tileRects := make([]sdl.Rect, 0, tileCount)
	for i := 0; i < tileCount; i++ {
		col := int32(i) % cols
		row := int32(i) / cols
		x := margin + col*(cardW+gap)
		y := gridY + row*(cardH+gap)
		tileRects = append(tileRects, sdl.Rect{X: x, Y: y, W: cardW, H: cardH})
	}

	return homeLayout{
		Cols:        cols,
		TileRects:   tileRects,
		RecentRect:  recent,
		TileElement: make([]int, tileCount),
		HeadingRect: headingRect,
	}
}

// homeSceneIndex returns the index of the "Main" scene, or -1.
func homeSceneIndex(config *Config) int {
	for i, s := range config.Scenes {
		if s.Name == "Main" && s.Layout == "home" {
			return i
		}
	}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) && config.Scenes[currentSceneIndex].Layout == "home" {
		return currentSceneIndex
	}
	return -1
}

// refreshHomeLayout recomputes the home layout from the current viewport and
// scene config. It writes into the package-level homeLayoutActive/homeLayoutState
// state used by tile rendering + mouse hit-testing. Safe to call every frame.
func refreshHomeLayout(config *Config) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		homeLayoutActive = false
		homeTileElementIndex = nil
		return
	}
	scene := config.Scenes[currentSceneIndex]
	if scene.Layout != "home" {
		homeLayoutActive = false
		homeTileElementIndex = nil
		return
	}
	var slots []int
	for i, el := range scene.Elements {
		if el.Type == "button" && el.Style == "tile" {
			slots = append(slots, i)
		}
	}
	hl := computeHomeLayout(screenWidth, screenHeight, len(slots))
	for i := range slots {
		if i < len(hl.TileRects) {
			hl.TileElement[i] = slots[i]
		} else {
			hl.TileElement[i] = HomeTileSlotMissing
		}
	}
	homeLayoutState = hl
	homeLayoutActive = true
	homeTileElementIndex = slots
}

// homeTileRect returns the computed rect for the tile element at elemIndex in a
// home scene, or false if the element is not a home tile.
func homeTileRect(elemIndex int) (sdl.Rect, bool) {
	if !homeLayoutActive {
		return sdl.Rect{}, false
	}
	for slot, idx := range homeTileElementIndex {
		if idx == elemIndex && slot < len(homeLayoutState.TileRects) {
			return homeLayoutState.TileRects[slot], true
		}
	}
	return sdl.Rect{}, false
}

// homeRecentRect returns the Continue panel rect for the active home scene.
func homeRecentRect() (sdl.Rect, bool) {
	if !homeLayoutActive {
		return sdl.Rect{}, false
	}
	return homeLayoutState.RecentRect, true
}

// focusColorForAccent returns the cyan/violet focus ring color (matches accent warmth).
func focusColorForAccent(accent sdl.Color) sdl.Color {
	if int(accent.R)+int(accent.G) > 420 && int(accent.R) > int(accent.B) {
		return sdl.Color{R: 139, G: 124, B: 255, A: 255}
	}
	return sdl.Color{R: 85, G: 216, B: 255, A: 255}
}

// fitTextWidth truncates `text` (single line) to fit within maxW using the
// given font, appending an ellipsis when truncated.
func fitTextWidth(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return text
	}
	tw, _, _ := font.SizeUTF8(text)
	if int32(tw) <= maxW {
		return text
	}
	runes := []rune(text)
	cur := ""
	for _, r := range runes {
		test := cur + string(r) + "…"
		tw, _, _ := font.SizeUTF8(test)
		if int32(tw) > maxW {
			break
		}
		cur += string(r)
	}
	return cur + "…"
}

// renderHomeHeading draws the page heading for the home screen.
func renderHomeHeading(renderer *sdl.Renderer, config *Config, heading string) {
	if !homeLayoutActive {
		return
	}
	headingRect := homeLayoutState.HeadingRect
	if headingRect.W <= 0 || headingRect.H <= 0 {
		return
	}
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	renderText(renderer, config, font, heading, ColorTextPrimary(), headingRect.X+SpaceXS, headingRect.Y+SpaceXS)
}
