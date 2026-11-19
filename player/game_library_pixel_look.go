package main

// game_library_pixel_look.go — the Games library's "Console look": how the
// consoles grid draws each console.
//
//   - Covers (the default): the glass button with the system's icon, name,
//     game count, cover meter and first cover (game_library_ui.go).
//   - Pixel names: each button is the console's name in big pixel letters
//     (game_library_pixel.go) — the name is the art — over a small count.
//     Only the focused button is a glass card; the others are a faint plate.
//   - Pixel cards: a card per console like a cartridge, its name in pixel
//     letters on an inset label, the system's icon and the game count above
//     it and a grip along the bottom edge.
//
// Landing on a console flies its letters together (drawConsolePixelCells).
// In the pixel looks the flight is drawn over the whole grid once every
// button is down (drawGameLibraryPixelFlight), so squares that start past
// their own button cross the neighbours instead of hiding under them.
//
// The choice is saved with the library's other view choices in
// game-library.json ("console_look", game_library_view.go), and is offered by
// X on the consoles grid, the game options sheet's Console look row and
// Settings → Games & emulators → Console look.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// The looks, as game-library.json stores them.
const (
	libraryLookCovers     = "covers"
	libraryLookPixelNames = "pixel_names"
	libraryLookPixelCards = "pixel_cards"
)

// libraryConsoleLookOptions are the looks in the order X and the rows step
// through them, with the line each one's list entry shows.
var libraryConsoleLookOptions = []setOption{
	{Value: libraryLookCovers, Label: "Covers", Note: "Each console's icon, name, game count and first cover"},
	{Value: libraryLookPixelNames, Label: "Pixel names", Note: "Each console is its name in big pixel letters"},
	{Value: libraryLookPixelCards, Label: "Pixel cards", Note: "A card per console with its name as a pixel label"},
}

// libraryConsoleLookValue is a stored look, Covers for anything unknown.
func libraryConsoleLookValue(look string) string {
	switch look {
	case libraryLookPixelNames, libraryLookPixelCards:
		return look
	}
	return libraryLookCovers
}

// libraryConsoleLookMode is the saved look. Cheap enough for every button of
// every frame: a lock and a field read once the file has been loaded.
func libraryConsoleLookMode() string {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	return libraryConsoleLookValue(libraryViewStore.data.ConsoleLook)
}

// setLibraryConsoleLook saves a look and returns the one now in force.
func setLibraryConsoleLook(look string) string {
	look = libraryConsoleLookValue(look)
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	if libraryViewStore.data.ConsoleLook != look {
		libraryViewStore.data.ConsoleLook = look
		libraryViewSaveLocked()
	}
	return look
}

// libraryConsoleLookIndex is a look's place in libraryConsoleLookOptions.
func libraryConsoleLookIndex(look string) int {
	for i, option := range libraryConsoleLookOptions {
		if option.Value == look {
			return i
		}
	}
	return 0
}

// libraryConsoleLookLabel is the saved look's name.
func libraryConsoleLookLabel() string {
	return libraryConsoleLookOptions[libraryConsoleLookIndex(libraryConsoleLookMode())].Label
}

// stepLibraryConsoleLook moves the look delta steps (wrapping), says which
// one is on, and lets the focused console's letters fly in again in it.
func stepLibraryConsoleLook(delta int) {
	n := len(libraryConsoleLookOptions)
	idx := ((libraryConsoleLookIndex(libraryConsoleLookMode())+delta)%n + n) % n
	setLibraryConsoleLook(libraryConsoleLookOptions[idx].Value)
	gameLib.logoSystem = ""
	ShowToast("Console look: "+libraryConsoleLookOptions[idx].Label, ToastKindInfo)
}

// gameLibraryLookPress is X on the consoles grid: the next look, at once.
func gameLibraryLookPress() {
	stepLibraryConsoleLook(1)
	PlayNavSound()
}

// gameOptionsConsoleLookHint is the options sheet's line under Console look.
func gameOptionsConsoleLookHint() string {
	return "How the console buttons look: covers, pixel names or pixel cards"
}

// libraryConsoleLookRow is Settings → Games & emulators → Console look.
func libraryConsoleLookRow() setRow {
	return setRow{
		ID: "libraryConsoleLook", Label: "Console look", Kind: setKindChoice,
		Detail: func(*Config) string {
			switch libraryConsoleLookMode() {
			case libraryLookPixelNames:
				return "Games library: each console is its name in big pixel letters"
			case libraryLookPixelCards:
				return "Games library: a card per console with its name as a pixel label"
			}
			return "Games library: each console shows its icon, name, game count and a cover"
		},
		Options: func(*Config) []setOption { return libraryConsoleLookOptions },
		Current: func(*Config) int { return libraryConsoleLookIndex(libraryConsoleLookMode()) },
		Choose: func(_ *Config, value string) {
			look := setLibraryConsoleLook(value)
			gameLib.logoSystem = ""
			setToast("Console look: " + libraryConsoleLookOptions[libraryConsoleLookIndex(look)].Label)
		},
	}
}

// drawGameLibraryConsoleButton draws one console of the grid in the saved
// look; the grid's loop (renderGameLibraryGrid) calls it for every button on
// screen.
func drawGameLibraryConsoleButton(renderer *sdl.Renderer, config *Config, system librarySystem, rect sdl.Rect, focused bool, dim float64) {
	switch libraryConsoleLookMode() {
	case libraryLookPixelNames:
		drawGameLibraryPixelNameButton(renderer, config, system, rect, focused, dim)
	case libraryLookPixelCards:
		drawGameLibraryPixelCard(renderer, config, system, rect, focused, dim)
	default:
		drawGameLibrarySystemTile(renderer, config, system, rect, focused, dim)
	}
}

// gameLibPixelPad is a pixel button's inner margin, from its height.
func gameLibPixelPad(rect sdl.Rect, div, lo, hi int32) int32 {
	return min32(hi, max32(lo, rect.H/div))
}

// drawGameLibraryPixelNameButton is a Pixel names button: the name in pixel
// letters filling the button over its count, centred. Minimal chrome: the
// focused one is Home's glass card with its glow and ring, the others a faint
// plate so the grid still reads as buttons.
func drawGameLibraryPixelNameButton(renderer *sdl.Renderer, config *Config, system librarySystem, rect sdl.Rect, focused bool, dim float64) {
	if focused {
		drawHomeFocusGlow(renderer, rect)
		drawGlassTile(renderer, rect, rect, true, false)
	} else {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, homeTileRadius, homeGlass(8))
	}
	pad := gameLibPixelPad(rect, 9, 10, 18)
	bottom := rect.H - pad
	if font := FontForRole(config, TypeCaption); font != nil {
		meta := ellipsize(font, gameLibraryTileMeta(system), rect.W-2*pad)
		w, _, _ := font.SizeUTF8(meta)
		bottom -= int32(font.Height())
		drawGlassText(renderer, config, font, meta, ColorTextMuted(), rect.X+(rect.W-int32(w))/2, rect.Y+bottom)
		bottom -= 4
	}
	box := sdl.Rect{X: pad, Y: pad, W: rect.W - 2*pad, H: bottom - pad}
	drawGameLibraryPixelName(renderer, config, system.Name, box, rect, focused, dim)
}

// gameLibPixelRidgeBuf holds a card's grip ridges for their one FillRects.
var gameLibPixelRidgeBuf [7]sdl.Rect

// drawGameLibraryPixelCard is a Pixel cards card: the system's icon and the
// game count along the top, the name in pixel letters on an inset label like
// a cartridge's, and a grip of ridges along the bottom edge.
func drawGameLibraryPixelCard(renderer *sdl.Renderer, config *Config, system librarySystem, rect sdl.Rect, focused bool, dim float64) {
	if focused {
		drawHomeFocusGlow(renderer, rect)
	}
	drawGlassTile(renderer, rect, rect, focused, false)
	pad := gameLibPixelPad(rect, 11, 8, 14)
	accent := glassAccent()

	badge := min32(36, max32(24, rect.H*22/100))
	glyphBox := drawIconBadge(renderer, sdl.Rect{X: rect.X + pad, Y: rect.Y + pad, W: badge, H: badge}, focused)
	glyphInk := ColorTextPrimary()
	if focused {
		glyphInk = accent
	}
	drawHomeGlyphTinted(renderer, librarySystemGlyph(system.Name), glyphBox, glyphInk)
	if font := FontForRole(config, TypeLabel); font != nil {
		meta := ellipsize(font, gameLibraryTileMeta(system), rect.W-3*pad-badge)
		w, _, _ := font.SizeUTF8(meta)
		drawGlassText(renderer, config, font, meta, ColorTextMuted(),
			rect.X+rect.W-pad-int32(w), rect.Y+pad+(badge-int32(font.Height()))/2)
	}

	top := pad + badge + pad/2
	label := sdl.Rect{X: rect.X + pad, Y: rect.Y + top, W: rect.W - 2*pad, H: rect.H - top - pad}
	// The grip only where the label keeps room for two lines of letters.
	if ridgeH := max32(4, rect.H/28); rect.H >= 120 && label.H-ridgeH-pad/2 >= 40 {
		label.H -= ridgeH + pad/2
		n := int32(len(gameLibPixelRidgeBuf))
		w := max32(3, rect.W/70)
		gap := w + w/2 + 2
		x := rect.X + (rect.W-(n*w+(n-1)*gap))/2
		y := rect.Y + rect.H - pad - ridgeH
		for i := range gameLibPixelRidgeBuf {
			gameLibPixelRidgeBuf[i] = sdl.Rect{X: x + int32(i)*(w+gap), Y: y, W: w, H: ridgeH}
		}
		ridge := homeGlass(34)
		if focused {
			ridge = WithAlpha(accent, 90)
		}
		// The ridges are see-through: blend them whatever the glyph left set.
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		consolePixelFill(renderer, gameLibPixelRidgeBuf[:], ridge)
	}
	if label.H <= 8 || label.W <= 8 {
		return
	}
	fill, edge := homeGlass(14), homeGlass(28)
	if focused {
		fill, edge = WithAlpha(accent, 28), WithAlpha(accent, 110)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, label.X, label.Y, label.W, label.H, homeIconRadius, fill)
	strokeRoundedRect(renderer, label.X, label.Y, label.W, label.H, homeIconRadius, 1, edge)
	inset := max32(4, label.H/10)
	box := sdl.Rect{X: label.X - rect.X + inset, Y: label.Y - rect.Y + inset, W: label.W - 2*inset, H: label.H - 2*inset}
	drawGameLibraryPixelName(renderer, config, system.Name, box, rect, focused, dim)
}

// gameLibPixelFlight is the focused console's flight, kept while the buttons
// are drawn and drawn over them by drawGameLibraryPixelFlight in the same
// frame (frame says which).
var gameLibPixelFlight struct {
	frame       uint64
	layout      *consolePixelLayout
	ox, oy      int32
	ink, shadow sdl.Color
	elapsed     uint64
}

// drawGameLibraryPixelName letters a console's name in box (from the corner
// of the button rect): accent on the focused button, the text colour greyed
// by dim (Dim unselected art) on the others, light letters over a soft
// shadow. The focused name's flight is left for drawGameLibraryPixelFlight.
func drawGameLibraryPixelName(renderer *sdl.Renderer, config *Config, name string, box, rect sdl.Rect, focused bool, dim float64) {
	if box.W <= 0 || box.H <= 0 {
		return
	}
	l := consolePixelLayoutFor(name, box, rect.W, rect.H, min32(9, max32(2, box.H/9)))
	ink := WithAlpha(ColorTextPrimary(), uint8(210-90*dim))
	if focused {
		ink = glassAccent()
	}
	// A light theme's dark letters get no shadow: under them it reads as a blur.
	shadow := sdl.Color{A: 90}
	if int(ink.R)*299+int(ink.G)*587+int(ink.B)*114 < 128*1000 {
		shadow.A = 0
	}
	if !focused {
		drawConsolePixelCells(renderer, l, rect.X, rect.Y, rect, ink, shadow, 0, false)
		return
	}
	elapsed, flying := gameLibPixelLanding(config, name)
	if !flying {
		drawConsolePixelCells(renderer, l, rect.X, rect.Y, rect, ink, shadow, elapsed, false)
		return
	}
	f := &gameLibPixelFlight
	f.frame, f.layout, f.ox, f.oy, f.ink, f.shadow, f.elapsed = frameCount, l, rect.X, rect.Y, ink, shadow, elapsed
}

// gameLibPixelLanding notes the console the focus is on and returns how long
// ago it landed there, and whether its letters are still flying. Reduce
// motion (and Battery Saver) puts them straight in place.
func gameLibPixelLanding(config *Config, name string) (uint64, bool) {
	now := sdl.GetTicks64()
	if gameLib.logoSystem != name {
		gameLib.logoSystem, gameLib.logoAt = name, now
	}
	if effectiveReducedMotion(config) || now < gameLib.logoAt {
		return consolePixelTotalMS, false
	}
	elapsed := now - gameLib.logoAt
	return elapsed, elapsed < consolePixelTotalMS
}

// drawGameLibraryPixelFlight draws the focused console's flying letters over
// the grid's buttons, cut to the grid's area. renderGameLibraryGrid calls it
// once its buttons are down; a frame with nothing in flight draws nothing.
func drawGameLibraryPixelFlight(renderer *sdl.Renderer, area sdl.Rect) {
	f := &gameLibPixelFlight
	if f.layout == nil || f.frame != frameCount {
		f.layout = nil
		return
	}
	drawConsolePixelCells(renderer, f.layout, f.ox, f.oy, area, f.ink, f.shadow, f.elapsed, true)
	f.layout = nil
}
