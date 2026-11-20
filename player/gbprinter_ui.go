package main

// gbprinter_ui.go — the Game Boy Printer scrapbook: every print, newest first.
//
// One screen in two places. From a game's page (More → Game Boy Printer →
// Scrapbook) it opens over the page with that game's prints; from the Tools
// hub ("Printer scrapbook") it is a page of its own with every game's. Both
// draw the same grid and the same big view, so the keys are the same:
//
//	grid      D-pad moves · A views one big · X (Delete) deletes · Y (or the
//	          keyboard's Y) shows the code that opens the scrapbook on a phone
//	          · B goes back
//	big view  Left/Right the previous and next print · L1/R1 (- and +) zoom ·
//	          Up/Down scroll a long print · A steps the zoom · B back to the grid
//
// A print is a few dozen pixels of four greys, so it is drawn in whole-pixel
// steps with nearest-neighbour sampling: crisp, like the paper. The pictures
// are decoded on a goroutine (two at a time), shrunk by a whole factor when a
// side is over 1024 pixels (a DIN A4 upscale), and only the main thread makes
// their textures; closing the screen frees them. Deleting asks once, then
// removes the print's file and its line in the list (gbprinter_collect.go).

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"strconv"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gbpSceneName is the scrapbook's page (the Tools hub's tile opens it).
	gbpSceneName = "PrinterScrapbook"
	// gbpTexMaxSide is the longest side a print's texture keeps.
	gbpTexMaxSide = 1024
	// gbpPhoneCard is the companion page's card (gbprinter_companion.go).
	gbpPhoneCard phoneCardKind = "gbprint"
)

// gbpBook is the scrapbook screen's state. Main thread only.
var gbpBook gbpBookState

// gbpBookState is the scrapbook screen: its list, focus, view and pictures.
type gbpBookState struct {
	overlay bool   // open over a game's page
	scene   bool   // open as the Tools page
	rom     string // "" every print, else this game's
	title   string
	prints  []gbpPrint
	rev     int64
	loading bool
	cursor  int
	first   int
	cols    int
	rows    int
	big     bool
	zoom    int // 0 fits the picture, else whole pixels per print pixel
	scrollY int32
	confirm bool
	tex     map[string]*sdl.Texture
	texW    map[string]int32
	texH    map[string]int32
	decoded map[string]*image.NRGBA
	pending map[string]bool
	hits    []gameLibRowHit
	visit   tbxVisit
}

// gbpDecodeSlots limits the decodes running at once.
var gbpDecodeSlots = make(chan struct{}, 2)

// gbpBookOpenOverlay opens the scrapbook over game's page.
func gbpBookOpenOverlay(game libraryGame) {
	gbpBookReset()
	gbpBook.overlay = true
	gbpBook.rom = game.Path
	gbpBook.title = game.Label()
	gbpBookReload()
	PlayActivateSound()
}

// gbpBookOpenScene opens the scrapbook as a page (the Tools hub's tile). The
// scene lives in memory only, like the Battery page's.
func gbpBookOpenScene(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	if sceneIndexExact(config, gbpSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: gbpSceneName})
	}
	gbpBookReset()
	gbpBook.scene = true
	gbpBook.title = "Every game"
	gbpBookReload()
	tbxOpenScene(config, gbpSceneName)
}

// gbpBookReset frees the textures and forgets the list.
func gbpBookReset() {
	for _, t := range gbpBook.tex {
		if t != nil {
			t.Destroy()
		}
	}
	gbpBook = gbpBookState{visit: gbpBook.visit}
	gbpBook.tex = map[string]*sdl.Texture{}
	gbpBook.texW = map[string]int32{}
	gbpBook.texH = map[string]int32{}
	gbpBook.decoded = map[string]*image.NRGBA{}
	gbpBook.pending = map[string]bool{}
}

// gbpBookRelease frees the pictures of the page being left but keeps its list,
// which is still drawn while the page fades out (no new decodes start: the
// maps are gone). The next visit reads everything again.
func gbpBookRelease() {
	for _, t := range gbpBook.tex {
		if t != nil {
			t.Destroy()
		}
	}
	gbpBook.tex, gbpBook.texW, gbpBook.texH = nil, nil, nil
	gbpBook.decoded, gbpBook.pending = nil, nil
	gbpBook.scene = false
}

// gbpBookClose closes the overlay (the page's B, or the page closing).
func gbpBookClose() {
	gbpBookReset()
}

// gbpBookReload reads the list on a goroutine.
func gbpBookReload() {
	rom := gbpBook.rom
	gbpBook.loading = true
	go func() {
		prints, rev := gbpPrints(rom)
		gbpCountsLoaded.Store(true)
		tbxPost(func() {
			if !(gbpBook.overlay || gbpBook.scene) || gbpBook.rom != rom {
				return
			}
			gbpBook.prints, gbpBook.rev, gbpBook.loading = prints, rev, false
			if gbpBook.cursor >= len(prints) {
				gbpBook.cursor = len(prints) - 1
			}
			if gbpBook.cursor < 0 {
				gbpBook.cursor = 0
			}
			if len(prints) == 0 {
				gbpBook.big = false
			}
		})
	}()
}

// gbpBookCurrent is the focused print.
func gbpBookCurrent() (gbpPrint, bool) {
	if gbpBook.cursor < 0 || gbpBook.cursor >= len(gbpBook.prints) {
		return gbpPrint{}, false
	}
	return gbpBook.prints[gbpBook.cursor], true
}

// ── Pictures ─────────────────────────────────────────────────────────────

// gbpDecode reads one print into memory, shrunk by a whole factor when a side
// is over gbpTexMaxSide. Off the main thread.
func gbpDecode(p gbpPrint) (*image.NRGBA, error) {
	f, err := gbpOpenPrint(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Measured first: a picture too large to hold is not decoded at all.
	if _, _, err := gbpPrintSize(f); err != nil {
		return nil, err
	}
	src, err := png.Decode(io.LimitReader(f, gbpPrintMaxBytes))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	step := 1
	for b.Dx()/step > gbpTexMaxSide || b.Dy()/step > gbpTexMaxSide {
		step++
	}
	if step == 1 {
		dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
		return dst, nil
	}
	w, h := b.Dx()/step, b.Dy()/step
	dst := image.NewNRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	for y := 0; y < dst.Rect.Dy(); y++ {
		for x := 0; x < dst.Rect.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x*step, b.Min.Y+y*step))
		}
	}
	return dst, nil
}

// gbpTexture is p's texture, or nil while it is decoded (which this starts).
// Render thread.
func gbpTexture(renderer *sdl.Renderer, p gbpPrint) *sdl.Texture {
	if gbpBook.tex == nil || gbpBook.decoded == nil || gbpBook.pending == nil {
		return nil // not opened through gbpBookReset
	}
	if t := gbpBook.tex[p.ID]; t != nil {
		return t
	}
	if pic := gbpBook.decoded[p.ID]; pic != nil {
		delete(gbpBook.decoded, p.ID)
		w, h := int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
		prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "0")
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, h)
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
		if err != nil || tex == nil {
			return nil
		}
		if err := tex.Update(nil, unsafe.Pointer(&pic.Pix[0]), pic.Stride); err != nil {
			tex.Destroy()
			return nil
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		gbpBook.tex[p.ID], gbpBook.texW[p.ID], gbpBook.texH[p.ID] = tex, w, h
		return tex
	}
	if !gbpBook.pending[p.ID] {
		gbpBook.pending[p.ID] = true
		rom := gbpBook.rom
		go func() {
			gbpDecodeSlots <- struct{}{}
			pic, err := gbpDecode(p)
			<-gbpDecodeSlots
			if err != nil {
				logWarn("gbprinter", "cannot show print %s: %v", p.ID, err)
			}
			tbxPost(func() {
				if !(gbpBook.overlay || gbpBook.scene) || gbpBook.rom != rom || gbpBook.pending == nil {
					return
				}
				if pic != nil && len(pic.Pix) > 0 {
					gbpBook.decoded[p.ID] = pic
				}
			})
		}()
	}
	return nil
}

// gbpFitPicture places a w×h picture in box: whole-pixel steps when it fits
// at least once, centred.
func gbpFitPicture(box sdl.Rect, w, h int32) sdl.Rect {
	if w <= 0 || h <= 0 || box.W <= 0 || box.H <= 0 {
		return sdl.Rect{}
	}
	sx, sy := float64(box.W)/float64(w), float64(box.H)/float64(h)
	s := min(sx, sy)
	if s >= 1 {
		s = float64(int(s))
	}
	dw, dh := int32(float64(w)*s), int32(float64(h)*s)
	return sdl.Rect{X: box.X + (box.W-dw)/2, Y: box.Y + (box.H-dh)/2, W: dw, H: dh}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gbpBookDrawBody draws the grid, the big view or the empty page in rect.
func gbpBookDrawBody(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect) {
	gbpBook.hits = gbpBook.hits[:0]
	if gbpRev() != gbpBook.rev && !gbpBook.loading {
		gbpBookReload() // a game printed, or the phone page changed nothing (read-only)
	}
	if len(gbpBook.prints) == 0 {
		title, body := "No prints yet", "Open a Game Boy game's page, More → Game Boy Printer → Play with the printer, and print from the game's own menu."
		if gbpBook.loading {
			title, body = "Opening the scrapbook…", ""
		}
		tbxDrawEmpty(renderer, config, rect, "image", title, body)
		return
	}
	if gbpBook.big {
		gbpBookDrawBig(renderer, config, fonts, rect)
	} else {
		gbpBookDrawGrid(renderer, config, fonts, rect)
	}
	if gbpBook.confirm {
		gbpBookDrawConfirm(renderer, config, fonts, rect)
	}
}

func gbpBookDrawGrid(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect) {
	cellW := int32(176)
	if rect.W < 3*cellW {
		cellW = max32(rect.W/3, 100)
	}
	capH := fonts.tinyH + 6
	cellH := cellW*9/10 + capH
	cols := max(int(rect.W/cellW), 1)
	rows := max(int(rect.H/cellH), 1)
	gbpBook.cols, gbpBook.rows = cols, rows
	gapX := (rect.W - int32(cols)*cellW) / int32(cols)
	curRow := gbpBook.cursor / cols
	if curRow < gbpBook.first {
		gbpBook.first = curRow
	}
	if curRow >= gbpBook.first+rows {
		gbpBook.first = curRow - rows + 1
	}
	for i := gbpBook.first * cols; i < len(gbpBook.prints) && i < (gbpBook.first+rows)*cols; i++ {
		p := gbpBook.prints[i]
		col, row := i%cols, i/cols-gbpBook.first
		cell := sdl.Rect{X: rect.X + int32(col)*(cellW+gapX) + gapX/2, Y: rect.Y + int32(row)*cellH, W: cellW, H: cellH - 6}
		UIListRow(renderer, cell, glassRowRadius, i == gbpBook.cursor, false)
		box := sdl.Rect{X: cell.X + 8, Y: cell.Y + 8, W: cell.W - 16, H: cell.H - capH - 12}
		if tex := gbpTexture(renderer, p); tex != nil {
			dst := gbpFitPicture(box, gbpBook.texW[p.ID], gbpBook.texH[p.ID])
			_ = renderer.Copy(tex, nil, &dst)
		} else {
			fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 6, WithAlpha(ColorTextSecondary(), 30))
		}
		caption := "#" + strconv.Itoa(gbpBookNumber(i))
		if gbpBook.rom == "" {
			caption = p.Game
		}
		caption = ellipsize(fonts.tiny, caption, cell.W-16)
		tw, _ := measureText(fonts.tiny, caption)
		renderText(renderer, config, fonts.tiny, caption, ColorTextSecondary(), cell.X+(cell.W-tw)/2, cell.Y+cell.H-capH-2)
		gbpBook.hits = append(gbpBook.hits, gameLibRowHit{index: i, rect: cell})
	}
}

// gbpBookNumber is the print's number in the list, oldest 1.
func gbpBookNumber(i int) int { return len(gbpBook.prints) - i }

func gbpBookDrawBig(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect) {
	p, ok := gbpBookCurrent()
	if !ok {
		return
	}
	info := fmt.Sprintf("%s · print %d of %d · %d×%d", p.Game, gbpBookNumber(gbpBook.cursor), len(gbpBook.prints), p.W, p.H)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, info, rect.W), ColorTextSecondary(), rect.X, rect.Y)
	box := sdl.Rect{X: rect.X, Y: rect.Y + fonts.smallH + 8, W: rect.W, H: rect.H - fonts.smallH - 8}
	tex := gbpTexture(renderer, p)
	if tex == nil {
		return
	}
	w, h := gbpBook.texW[p.ID], gbpBook.texH[p.ID]
	dst := gbpFitPicture(box, w, h)
	if gbpBook.zoom > 0 {
		dst.W, dst.H = w*int32(gbpBook.zoom), h*int32(gbpBook.zoom)
		dst.X = box.X + (box.W-dst.W)/2
		dst.Y = box.Y
		if dst.H < box.H {
			dst.Y = box.Y + (box.H-dst.H)/2
		}
	}
	// A long print scrolls; the offset is clamped to the picture.
	over := dst.H - box.H
	if over < 0 {
		over = 0
	}
	gbpBook.scrollY = max32(0, min32(gbpBook.scrollY, over))
	if over > 0 {
		dst.Y = box.Y - gbpBook.scrollY
	}
	_ = renderer.SetClipRect(&box)
	_ = renderer.Copy(tex, nil, &dst)
	_ = renderer.SetClipRect(nil)
}

func gbpBookDrawConfirm(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect) {
	w := min32(rect.W, 460)
	h := fonts.bodyH + fonts.smallH + 40
	card := sdl.Rect{X: rect.X + (rect.W-w)/2, Y: rect.Y + (rect.H-h)/2, W: w, H: h}
	drawOverlayCard(renderer, card)
	renderText(renderer, config, fonts.body, "Delete this print?", ColorTextPrimary(), card.X+20, card.Y+14)
	hint := hintLine(FooterHint{"A", "Delete"}, FooterHint{"B", "Keep"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-40), ColorTextSecondary(), card.X+20, card.Y+20+fonts.bodyH)
}

// gbpBookHints are the scrapbook's keys for what is on screen.
func gbpBookHints() []FooterHint {
	switch {
	case gbpBook.confirm:
		return []FooterHint{{"A", "Delete"}, {"B", "Keep"}}
	case gbpBook.big:
		return []FooterHint{{"Left/Right", "Previous / next"}, {"L1/R1 / - / +", "Zoom"}, {"X / Del", "Delete"}, {"Y", "Phone"}, {"B", "Back"}}
	case len(gbpBook.prints) == 0:
		return []FooterHint{{"B", "Back"}}
	}
	return []FooterHint{{"D-Pad", "Move"}, {"A", "View"}, {"X / Del", "Delete"}, {"Y", "Phone"}, {"B", "Back"}}
}

// gbpFooterHints is the scrapbook page's footer (extras.go).
func gbpFooterHints(name string) ([]FooterHint, bool) {
	if name != gbpSceneName {
		return nil, false
	}
	return gbpBookHints(), true
}

// gbpBookRenderOverlay draws the scrapbook over a game's page.
func gbpBookRenderOverlay(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("Scrapbook", gbpBook.title, "image")
	headerH := UIPanelHeaderBand(config, header, 10)
	card := sdl.Rect{X: 24, Y: 20, W: screenWidth - 48, H: screenHeight - 40}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	footerH := fonts.smallH + 16
	body := sdl.Rect{X: card.X + OverlayPadX, Y: card.Y + headerH, W: card.W - 2*OverlayPadX, H: card.H - headerH - footerH - 8}
	gbpBookDrawBody(renderer, config, fonts, body)
	hints := gbpBookHints()
	hint := hintLine(hints...)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+2)
}

// gbpBookRenderScene draws the scrapbook page.
func gbpBookRenderScene(renderer *sdl.Renderer, config *Config) {
	if gbpBook.visit.begin() {
		if !gbpBook.scene {
			// Back on the page by B from somewhere else: the list again.
			gbpBookReset()
			gbpBook.scene = true
			gbpBook.title = "Every game"
			gbpBookReload()
		}
		// Leaving the page (any way) ends this visit's scene work: the
		// pictures' textures are freed then, not when the page is next opened.
		visit := gbpBook.visit.ctx
		runSceneTask(func(ctx context.Context) {
			<-ctx.Done()
			tbxPost(func() {
				if gbpBook.scene && !gbpBook.overlay && gbpBook.visit.ctx == visit {
					gbpBookRelease()
				}
			})
		})
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	status := ""
	if n := len(gbpBook.prints); n > 0 {
		status = strconv.Itoa(n) + " prints"
		if n == 1 {
			status = "1 print"
		}
	}
	header := tbxHeader("Printer scrapbook", "What your Game Boy games printed", "image", status, ColorTextSecondary())
	f := tbxBeginFrame(renderer, config, header)
	gbpBookDrawBody(renderer, config, fonts, f.bodyRect())
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

// gbpBookKeyZoom maps + and - (the keyboard's zoom keys).
func gbpBookKeyZoom(e *sdl.KeyboardEvent) int {
	if e == nil || e.Type != sdl.KEYDOWN {
		return 0
	}
	switch e.Keysym.Sym {
	case sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS:
		return 1
	case sdl.K_MINUS, sdl.K_KP_MINUS:
		return -1
	}
	return 0
}

// gbpBookZoom steps the big view's zoom: 0 fits, then whole steps to 8.
func gbpBookZoom(delta int) {
	if !gbpBook.big {
		return
	}
	z := gbpBook.zoom + delta
	if z < 0 {
		z = 0
	}
	if z > 8 {
		z = 8
	}
	if z != gbpBook.zoom {
		gbpBook.zoom = z
		gbpBook.scrollY = 0
		PlayNavSound()
	}
}

// gbpBookAction is one input; false leaves it unused (an unused Back closes
// the screen).
func gbpBookAction(act tbxAction) bool {
	if gbpBook.confirm {
		switch act {
		case tbxActConfirm:
			gbpBook.confirm = false
			gbpBookDelete()
		case tbxActBack:
			gbpBook.confirm = false
			PlayBackSound()
		}
		return act != tbxActNone
	}
	n := len(gbpBook.prints)
	move := func(to int) {
		if n == 0 {
			return
		}
		to = max(0, min(to, n-1))
		if to != gbpBook.cursor {
			gbpBook.cursor = to
			gbpBook.scrollY = 0
			PlayNavSound()
		}
	}
	cols := max(gbpBook.cols, 1)
	switch act {
	case tbxActBack:
		if gbpBook.big {
			gbpBook.big = false
			PlayBackSound()
			return true
		}
		return false
	case tbxActX:
		if n > 0 {
			gbpBook.confirm = true
			PlayActivateSound()
		}
	case tbxActY:
		gbpBookShare()
	case tbxActConfirm:
		if n == 0 {
			return true
		}
		if !gbpBook.big {
			gbpBook.big, gbpBook.zoom, gbpBook.scrollY = true, 0, 0
			PlayActivateSound()
		} else {
			// A steps the zoom and wraps back to fit.
			gbpBook.zoom = (gbpBook.zoom + 1) % 5
			gbpBook.scrollY = 0
			PlayNavSound()
		}
	case tbxActLeft:
		move(gbpBook.cursor - 1)
	case tbxActRight:
		move(gbpBook.cursor + 1)
	case tbxActUp:
		if gbpBook.big {
			gbpBook.scrollY -= 48
			return true
		}
		if gbpBook.cursor-cols < 0 {
			return gbpBook.overlay // the page's top bar takes an unused Up
		}
		move(gbpBook.cursor - cols)
	case tbxActDown:
		if gbpBook.big {
			gbpBook.scrollY += 48
			return true
		}
		move(gbpBook.cursor + cols)
	case tbxActPageUp:
		if gbpBook.big {
			gbpBookZoom(-1)
		} else {
			move(gbpBook.cursor - cols*max(gbpBook.rows, 1))
		}
	case tbxActPageDown:
		if gbpBook.big {
			gbpBookZoom(1)
		} else {
			move(gbpBook.cursor + cols*max(gbpBook.rows, 1))
		}
	case tbxActHome:
		move(0)
	case tbxActEnd:
		move(n - 1)
	default:
		return false
	}
	return true
}

// gbpBookMouse handles a click: a print opens big, a second click on it
// steps back to the grid.
func gbpBookMouse(mx, my int32) bool {
	if gbpBook.confirm {
		return true
	}
	if gbpBook.big {
		gbpBook.big = false
		PlayBackSound()
		return true
	}
	for _, hit := range gbpBook.hits {
		if pointInRect(mx, my, hit.rect) {
			gbpBook.cursor = hit.index
			gbpBook.big, gbpBook.zoom, gbpBook.scrollY = true, 0, 0
			PlayActivateSound()
			return true
		}
	}
	return false
}

// gbpBookDelete deletes the focused print on a goroutine.
func gbpBookDelete() {
	p, ok := gbpBookCurrent()
	if !ok {
		return
	}
	PlayActivateSound()
	go func() {
		err := gbpDeletePrint(p.ID)
		tbxPost(func() {
			if err != nil {
				ShowToast("Print not deleted: "+err.Error(), ToastKindWarning)
				return
			}
			if t := gbpBook.tex[p.ID]; t != nil {
				t.Destroy()
				delete(gbpBook.tex, p.ID)
			}
			ShowToast("Print deleted", ToastKindInfo)
			if gbpBook.overlay || gbpBook.scene {
				gbpBookReload()
			}
		})
	}()
}

// gbpBookShare shows the code that opens the scrapbook card on a phone.
func gbpBookShare() {
	if phoneCardOpen(gbpPhoneCard, "Printer scrapbook on your phone",
		"Scan it with the phone's camera: every print opens there to save, share or print on paper (2× and 4× for paper)") {
		PlayActivateSound()
	}
}

// ── Hosts ────────────────────────────────────────────────────────────────

type gbpBookScene struct{ sceneBase }

func (gbpBookScene) render(r *sdl.Renderer, c *Config) bool {
	gbpBookRenderScene(r, c)
	return true
}

func (gbpBookScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if z := gbpBookKeyZoom(e); z != 0 && !gbpBook.confirm {
		gbpBookZoom(z)
		return true
	}
	return tbxDispatch(c, tbxKeyAction(e), func(_ *Config, act tbxAction) bool { return gbpBookAction(act) })
}

func (gbpBookScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), func(_ *Config, act tbxAction) bool { return gbpBookAction(act) })
}

// gbpHubTile is the Tools hub's tile (toolshub.go).
func gbpHubTile() toolsHubTile {
	tile := toolsHubTile{id: "gbprinter", label: "Printer scrapbook", icon: "image", value: "What your Game Boy games printed"}
	if n, ok := gbpCountIfLoaded(""); ok && n > 0 {
		tile.value = strconv.Itoa(n) + " prints from your Game Boy games"
		if n == 1 {
			tile.value = "1 print from your Game Boy games"
		}
		tile.tone = toolsHubToneGood
	}
	return tile
}

func init() {
	registerScene(gbpBookScene{sceneBase{name: gbpSceneName}})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "gbprinter-scrapbook",
		IsOpen: func() bool { return gbpBook.overlay },
		Key: func(e *sdl.KeyboardEvent) bool {
			if z := gbpBookKeyZoom(e); z != 0 && !gbpBook.confirm {
				gbpBookZoom(z)
				return true
			}
			if act := tbxKeyAction(e); act != tbxActNone && !gbpBookAction(act) && act == tbxActBack {
				gbpBookClose()
				PlayBackSound()
			}
			return true
		},
		Pad: func(e *sdl.ControllerButtonEvent) bool {
			if act := tbxPadAction(e); act != tbxActNone && !gbpBookAction(act) && act == tbxActBack {
				gbpBookClose()
				PlayBackSound()
			}
			return true
		},
		Mouse:  func(mx, my int32) bool { return gbpBookMouse(mx, my) },
		Render: gbpBookRenderOverlay,
		Close:  gbpBookClose,
	})
}
