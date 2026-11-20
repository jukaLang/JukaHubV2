package main

// paint_ui.go — what Paint draws: the start screen, the canvas with its
// checkerboard, grid and cursor, the tool strip above it, and the sheets
// (tools, colours, File, the Open list and the game list for a cover), with
// the footer hints that follow them and the sheets' own keys.
//
// Every size comes from the screen (1280×720 on the Smart Pro, 1024×768 on
// the Brick, smaller canvases too): the body is the space between the shared
// header and footer, and the canvas viewport is that body under the strip
// (paintLayout).

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// paintSwatch is one palette colour.
type paintSwatch struct {
	name string
	c    color.NRGBA
}

// paintMarkupRed is the markup colour a screenshot opens with.
var paintMarkupRed = color.NRGBA{R: 0xFF, G: 0x1E, B: 0x1E, A: 0xFF}

// paintPalette is PICO-8's sixteen colours, then pure white and the two
// bright markup colours. The eyedropper's colour joins the sheet as a
// twentieth swatch.
var paintPalette = [...]paintSwatch{
	{"Black", color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xFF}},
	{"Dark blue", color.NRGBA{R: 0x1D, G: 0x2B, B: 0x53, A: 0xFF}},
	{"Dark purple", color.NRGBA{R: 0x7E, G: 0x25, B: 0x53, A: 0xFF}},
	{"Dark green", color.NRGBA{R: 0x00, G: 0x87, B: 0x51, A: 0xFF}},
	{"Brown", color.NRGBA{R: 0xAB, G: 0x52, B: 0x36, A: 0xFF}},
	{"Dark grey", color.NRGBA{R: 0x5F, G: 0x57, B: 0x4F, A: 0xFF}},
	{"Light grey", color.NRGBA{R: 0xC2, G: 0xC3, B: 0xC7, A: 0xFF}},
	{"Off-white", color.NRGBA{R: 0xFF, G: 0xF1, B: 0xE8, A: 0xFF}},
	{"Red", color.NRGBA{R: 0xFF, G: 0x00, B: 0x4D, A: 0xFF}},
	{"Orange", color.NRGBA{R: 0xFF, G: 0xA3, B: 0x00, A: 0xFF}},
	{"Yellow", color.NRGBA{R: 0xFF, G: 0xEC, B: 0x27, A: 0xFF}},
	{"Green", color.NRGBA{R: 0x00, G: 0xE4, B: 0x36, A: 0xFF}},
	{"Blue", color.NRGBA{R: 0x29, G: 0xAD, B: 0xFF, A: 0xFF}},
	{"Lavender", color.NRGBA{R: 0x83, G: 0x76, B: 0x9C, A: 0xFF}},
	{"Pink", color.NRGBA{R: 0xFF, G: 0x77, B: 0xA8, A: 0xFF}},
	{"Peach", color.NRGBA{R: 0xFF, G: 0xCC, B: 0xAA, A: 0xFF}},
	{"White", color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}},
	{"Markup red", paintMarkupRed},
	{"Markup yellow", color.NRGBA{R: 0xFF, G: 0xE6, B: 0x00, A: 0xFF}},
}

// paintPaletteCols is the colour sheet's width in swatches.
const paintPaletteCols = 5

// paintSwatches is the colour sheet: the palette and the picked colour.
func paintSwatches() []paintSwatch {
	out := paintPalette[:]
	if paint.picked.A != 0 {
		out = append(append([]paintSwatch(nil), out...), paintSwatch{"Picked", paint.picked})
	}
	return out
}

// ── The frame ─────────────────────────────────────────────────────────────

// renderPaint draws the app.
func renderPaint(renderer *sdl.Renderer, config *Config) {
	paint.config = config
	paintLayout()
	paintTick(config)
	paint.hits = paint.hits[:0]
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	if paint.view == paintViewCanvas && paint.doc != nil {
		paintDrawCanvas(renderer)
		paintDrawStrip(renderer, config)
	} else {
		paint.view = paintViewStart
		paintDrawStart(renderer, config)
	}
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if paint.sheet != paintSheetNone {
		// Only the sheet answers clicks while it is up.
		paint.hits = paint.hits[:0]
		paintDrawSheet(renderer, config)
	}
	if paint.loading {
		paintDrawLoading(renderer, config)
	}
}

// paintFonts are the two sizes Paint writes in.
func paintFonts(config *Config) (*ttf.Font, *ttf.Font) {
	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		medium = small
	}
	return small, medium
}

// paintBody is the space between the shared bars.
func paintBody() sdl.Rect {
	top := headerBarHeight()
	return sdl.Rect{X: 0, Y: top, W: screenWidth, H: screenHeight - footerBarHeight() - top}
}

// ── The start screen ──────────────────────────────────────────────────────

// paintStartRow is one of the start screen's choices.
type paintStartRow struct {
	id, title, sub, glyph string
}

// paintStartRows are the start screen's choices: Continue when there is
// something to continue, the canvas sizes, and Open.
func paintStartRows() []paintStartRow {
	var rows []paintStartRow
	switch {
	case paint.doc != nil:
		sub := paintDocDesc(paint.doc)
		if paint.doc.changed {
			sub += " · not saved yet"
		}
		rows = append(rows, paintStartRow{"continue", "Continue", sub, "continue"})
	case paintWIP.exists:
		rows = append(rows, paintStartRow{"continue", "Continue", paintWIP.desc + " · autosaved", "continue"})
	}
	return append(rows,
		paintStartRow{"new16", "New 16×16 pixel art", "Tiny icons and cursors", "grid"},
		paintStartRow{"new32", "New 32×32 pixel art", "Sprites and emoji", "grid"},
		paintStartRow{"new64", "New 64×64 pixel art", "Characters, logos, game covers", "grid"},
		paintStartRow{"new128", "New 128×128 pixel art", "Scenes and big covers", "grid"},
		paintStartRow{"sketch", fmt.Sprintf("New %d×%d sketch", screenWidth, screenHeight), "A white page the size of the screen", "brush"},
		paintStartRow{"open", "Open a screenshot or drawing", "Circle something, point at it, save a marked-up copy", "image"},
	)
}

// paintStartIndex is the highlighted row: the one chosen, else Continue,
// else the 64×64 canvas.
func paintStartIndex(rows []paintStartRow) int {
	for i, r := range rows {
		if r.id == paint.startSel {
			return i
		}
	}
	def := 0
	for i, r := range rows {
		if r.id == "continue" {
			return i
		}
		if r.id == "new64" {
			def = i
		}
	}
	return def
}

// paintDrawStart draws the start screen: one column of glass rows.
func paintDrawStart(renderer *sdl.Renderer, config *Config) {
	small, medium := paintFonts(config)
	rows := paintStartRows()
	cur := paintStartIndex(rows)
	body := paintBody()
	w := min32(760, screenWidth-48)
	x := (screenWidth - w) / 2
	rh := int32(64)
	if screenHeight < 640 {
		rh = 50
	}
	gap := int32(8)
	top := body.Y + 16
	room := body.H - 32
	visible := max(int(room/(rh+gap)), 1)
	first := 0
	if cur >= visible {
		first = cur - visible + 1
	}
	for i := first; i < len(rows) && i < first+visible; i++ {
		r := rows[i]
		rect := sdl.Rect{X: x, Y: top + int32(i-first)*(rh+gap), W: w, H: rh}
		focused := i == cur
		if focused {
			drawGlassFocusGlow(renderer, rect)
		}
		drawGlassSurface(renderer, rect, glassControlRadius, glassState{Focused: focused})
		icon := rh - 28
		iconDraw(renderer, r.glyph, sdl.Rect{X: rect.X + 16, Y: rect.Y + (rh-icon)/2, W: icon, H: icon}, ColorTextPrimary())
		tx := rect.X + 16 + icon + 14
		tw := rect.W - (tx - rect.X) - 16
		if medium != nil {
			_, th := measureText(medium, r.title)
			ty := rect.Y + rh/2 - th
			if small == nil {
				ty = rect.Y + (rh-th)/2
			}
			drawText(renderer, medium, ellipsize(medium, r.title, tw), tx, ty, ColorTextPrimary(), textAlignLeft)
		}
		if small != nil {
			drawText(renderer, small, ellipsize(small, r.sub, tw), tx, rect.Y+rh/2+2, ColorTextSecondary(), textAlignLeft)
		}
		paint.hits = append(paint.hits, paintHit{r: rect, id: "start", index: i})
	}
}

// paintStartButton answers the pad (and the keys standing for it) on the
// start screen.
func paintStartButton(b uint8, config *Config) {
	rows := paintStartRows()
	cur := paintStartIndex(rows)
	switch b {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if cur > 0 {
			paint.startSel = rows[cur-1].id
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if cur < len(rows)-1 {
			paint.startSel = rows[cur+1].id
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_A:
		if cur < 0 || cur >= len(rows) {
			return
		}
		id := rows[cur].id
		if id != "continue" && id != "open" && !paintConfirmDiscard(id) {
			return
		}
		switch id {
		case "continue":
			paintContinueWIP()
		case "new16":
			paintNewCanvas(16, 16, true)
		case "new32":
			paintNewCanvas(32, 32, true)
		case "new64":
			paintNewCanvas(64, 64, true)
		case "new128":
			paintNewCanvas(128, 128, true)
		case "sketch":
			paintNewCanvas(int(screenWidth), int(screenHeight), false)
		case "open":
			paint.sheetCur = 0
			paintOpenPicker()
			PlayActivateSound()
		}
	}
}

// paintConfirmDiscard lets an action that replaces an unsaved drawing run
// on its second press only.
func paintConfirmDiscard(id string) bool {
	unsaved := paint.doc != nil && paint.doc.changed
	msg := "Your drawing is not saved — press again to replace it, or save it first (File)"
	if paint.doc == nil {
		// Since a restart the drawing is only in the autosave, which the
		// new canvas's autosave replaces.
		unsaved = paintWIP.exists && paintWIP.unsaved
		msg = "Your autosaved drawing is not saved — press again to replace it, or Continue and save it"
	}
	if !unsaved {
		return true
	}
	if paint.confirmID == id && time.Since(paint.confirmAt) < 4*time.Second {
		paint.confirmID = ""
		return true
	}
	paint.confirmID, paint.confirmAt = id, time.Now()
	ShowToast(msg, ToastKindWarning)
	PlayErrorSound()
	return false
}

// ── The canvas ────────────────────────────────────────────────────────────

// paintCanvasRect is the whole canvas on screen at the current zoom.
func paintCanvasRect() sdl.Rect {
	d := paint.doc
	z := paint.zoom
	return sdl.Rect{
		X: paint.viewport.X + int32(math.Round(-paint.panX*z)),
		Y: paint.viewport.Y + int32(math.Round(-paint.panY*z)),
		W: int32(math.Round(float64(d.w) * z)),
		H: int32(math.Round(float64(d.h) * z)),
	}
}

// paintDrawCanvas draws the canvas: the checkerboard under see-through
// pixels, the picture, the grid at high zoom and the cursor.
func paintDrawCanvas(renderer *sdl.Renderer) {
	d := paint.doc
	if paint.zoom <= 0 {
		paint.zoom = paintFitZoom(d)
		paintClampPan(true)
	}
	v := paint.viewport
	dst := paintCanvasRect()
	vis, ok := dst.Intersect(&v)
	if !ok {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// A soft edge so a white sketch does not melt into a light theme.
	fillRoundedRect(renderer, vis.X-3, vis.Y-3, vis.W+6, vis.H+6, 6, ShadowFill(60))
	_ = renderer.SetClipRect(&vis)
	if checker := paintCheckerTexture(renderer); checker != nil {
		for ty := vis.Y; ty < vis.Y+vis.H; ty += paintCheckerSize {
			for tx := vis.X; tx < vis.X+vis.W; tx += paintCheckerSize {
				r := sdl.Rect{X: tx, Y: ty, W: paintCheckerSize, H: paintCheckerSize}
				_ = renderer.Copy(checker, nil, &r)
			}
		}
	}
	if tex := paintTexture(renderer, d, paint.zoom < 1); tex != nil {
		_ = renderer.Copy(tex, nil, &dst)
	}
	z := paint.zoom
	if paint.grid && z >= 6 {
		zi := int32(z)
		c0 := max(int((vis.X-dst.X)/zi), 0)
		c1 := min(int((vis.X+vis.W-dst.X)/zi)+1, d.w)
		r0 := max(int((vis.Y-dst.Y)/zi), 0)
		r1 := min(int((vis.Y+vis.H-dst.Y)/zi)+1, d.h)
		for c := c0; c <= c1; c++ {
			a := uint8(40)
			if c%8 == 0 {
				a = 90
			}
			_ = renderer.SetDrawColor(0, 0, 0, a)
			x := dst.X + int32(c)*zi
			_ = renderer.DrawLine(x, vis.Y, x, vis.Y+vis.H)
		}
		for r := r0; r <= r1; r++ {
			a := uint8(40)
			if r%8 == 0 {
				a = 90
			}
			_ = renderer.SetDrawColor(0, 0, 0, a)
			y := dst.Y + int32(r)*zi
			_ = renderer.DrawLine(vis.X, y, vis.X+vis.W, y)
		}
	}
	// The cursor may stand at the canvas's edge: it is clipped to the
	// viewport only, so its outline is never cut in half.
	_ = renderer.SetClipRect(&v)
	paintDrawCursor(renderer, dst)
	_ = renderer.SetClipRect(nil)
}

// paintDrawCursor outlines the brush where it would land, black and white so
// it shows on any picture, with cross arms when it is too small to find.
func paintDrawCursor(renderer *sdl.Renderer, dst sdl.Rect) {
	z := paint.zoom
	size := paintBrush()
	if paint.tool == paintFill || paint.tool == paintPicker {
		size = 1
	}
	x0 := paint.curX - (size-1)/2
	y0 := paint.curY - (size-1)/2
	r := sdl.Rect{
		X: dst.X + int32(math.Floor(float64(x0)*z)),
		Y: dst.Y + int32(math.Floor(float64(y0)*z)),
		W: max32(int32(math.Ceil(float64(size)*z)), 3),
		H: max32(int32(math.Ceil(float64(size)*z)), 3),
	}
	outer := sdl.Rect{X: r.X - 1, Y: r.Y - 1, W: r.W + 2, H: r.H + 2}
	_ = renderer.SetDrawColor(0, 0, 0, 220)
	_ = renderer.DrawRect(&outer)
	_ = renderer.SetDrawColor(255, 255, 255, 235)
	_ = renderer.DrawRect(&r)
	if r.W < 14 {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		arm, gap := int32(9), r.W/2+3
		for _, s := range [...][4]int32{{-1, 0, 0, 0}, {1, 0, 0, 0}, {0, -1, 0, 0}, {0, 1, 0, 0}} {
			x1, y1 := cx+s[0]*gap, cy+s[1]*gap
			x2, y2 := cx+s[0]*(gap+arm), cy+s[1]*(gap+arm)
			_ = renderer.SetDrawColor(0, 0, 0, 200)
			_ = renderer.DrawLine(x1+s[1], y1+s[0], x2+s[1], y2+s[0])
			_ = renderer.SetDrawColor(255, 255, 255, 230)
			_ = renderer.DrawLine(x1, y1, x2, y2)
		}
	}
	if paint.shapeOn {
		// The first corner of the shape being placed.
		a := sdl.Rect{
			X: dst.X + int32(math.Floor(float64(paint.anchorX)*z)) - 2,
			Y: dst.Y + int32(math.Floor(float64(paint.anchorY)*z)) - 2,
			W: max32(int32(z), 1) + 4,
			H: max32(int32(z), 1) + 4,
		}
		setDrawColor(renderer, glassAccent())
		_ = renderer.DrawRect(&a)
	}
}

// paintDrawStrip draws the bar above the canvas: tool, size and colour on
// the left; where the cursor is; zoom, undo, redo and File on the right.
// Each part clicks.
func paintDrawStrip(renderer *sdl.Renderer, config *Config) {
	small, _ := paintFonts(config)
	if small == nil {
		return
	}
	d := paint.doc
	h := paintStripHeight() - 6
	y := headerBarHeight() + 4
	x := int32(12)
	chip := func(label, id string, swatch *color.NRGBA) int32 {
		lw, lh := measureText(small, label)
		w := lw + 24
		if swatch != nil {
			w += h - 10 + 6
		}
		rect := sdl.Rect{X: x, Y: y, W: w, H: h}
		drawGlassSurface(renderer, rect, h/2, glassState{})
		tx := rect.X + 12
		if swatch != nil {
			sw := h - 10
			box := sdl.Rect{X: tx, Y: rect.Y + 5, W: sw, H: sw}
			fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 4, sdl.Color{R: swatch.R, G: swatch.G, B: swatch.B, A: 255})
			strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, 4, 1, glassA(90))
			tx += sw + 6
		}
		drawText(renderer, small, label, tx, rect.Y+(h-lh)/2, ColorTextPrimary(), textAlignLeft)
		paint.hits = append(paint.hits, paintHit{r: rect, id: id})
		x += w + 6
		return w
	}
	toolLabel := paintToolNames[paint.tool]
	if paint.shapeOn {
		toolLabel += " · place the end"
	}
	chip(toolLabel, "tool", nil)
	if paint.tool != paintFill && paint.tool != paintPicker {
		chip(fmt.Sprintf("%d px", paintBrush()), "size", nil)
	}
	c := paint.color
	chip(paintColorName(c), "color", &c)

	// The right-hand group, laid out from the right edge.
	right := screenWidth - 12
	rchip := func(label, id string) {
		lw, lh := measureText(small, label)
		w := lw + 24
		right -= w
		rect := sdl.Rect{X: right, Y: y, W: w, H: h}
		drawGlassSurface(renderer, rect, h/2, glassState{})
		drawText(renderer, small, label, rect.X+12, rect.Y+(h-lh)/2, ColorTextPrimary(), textAlignLeft)
		paint.hits = append(paint.hits, paintHit{r: rect, id: id})
		right -= 6
	}
	rchip("File", "file")
	if screenWidth >= 900 {
		rchip("Redo", "redo")
		rchip("Undo", "undo")
	}
	rchip("+", "zoomin")
	zoom := fmt.Sprintf("%.0f%%", paint.zoom*100)
	zw, zh := measureText(small, zoom)
	right -= zw
	drawText(renderer, small, zoom, right, y+(h-zh)/2, ColorTextSecondary(), textAlignLeft)
	right -= 6
	rchip("−", "zoomout")

	// Where the cursor is, the canvas size and the file, in what is left.
	name := "not saved"
	if d.savedPath != "" {
		name = filepath.Base(d.savedPath)
		if d.changed {
			name += " •"
		}
	} else if d.source != "" {
		name = "markup of " + filepath.Base(d.source)
	}
	info := fmt.Sprintf("%d, %d · %d×%d · %s", paint.curX, paint.curY, d.w, d.h, name)
	if room := right - x - 12; room > 60 {
		info = ellipsize(small, info, room)
		_, ih := measureText(small, info)
		drawText(renderer, small, info, x+6, y+(h-ih)/2, ColorTextSecondary(), textAlignLeft)
	}
}

// paintColorName names a colour: the palette's name, else its hex.
func paintColorName(c color.NRGBA) string {
	for _, s := range paintPalette {
		if s.c == c {
			return s.name
		}
	}
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// paintDrawLoading is the card shown while a picture opens.
func paintDrawLoading(renderer *sdl.Renderer, config *Config) {
	body := paintBody()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassScrimColor(1))
	_ = renderer.FillRect(&body)
	w := min32(420, screenWidth-40)
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: body.Y + body.H/2 - 60, W: w, H: 120}
	UIEmptyState(renderer, config, card, "@image", "Opening the picture…", "B cancels")
}

// ── Sheets ────────────────────────────────────────────────────────────────

// paintRow is one row of a list sheet.
type paintRow struct {
	id, title, sub string
	on             bool
}

// paintOpenSheet opens a sheet; whatever was half-placed on the canvas goes.
func paintOpenSheet(k paintSheetKind) {
	paintEndStroke()
	paint.sheet = k
	paint.sheetScroll = 0
	switch k {
	case paintSheetTools:
		paint.sheetCur = int(paint.tool)
	case paintSheetColors:
		paint.sheetCur = 0
		for i, s := range paintSwatches() {
			if s.c == paint.color {
				paint.sheetCur = i
			}
		}
	default:
		paint.sheetCur = 0
	}
	PlayActivateSound()
}

// paintCloseSheet closes the open sheet.
func paintCloseSheet() {
	if paint.sheet == paintSheetOpen {
		paintPreviewRelease()
	}
	if paint.sheet == paintSheetGame {
		paintGames.gen++
		paintGames.loading = false
	}
	paint.sheet = paintSheetNone
	PlayBackSound()
}

// paintSheetRows are the rows of a list sheet.
func paintSheetRows() []paintRow {
	switch paint.sheet {
	case paintSheetTools:
		rows := make([]paintRow, 0, int(paintToolCount)+1)
		for t := paintTool(0); t < paintToolCount; t++ {
			rows = append(rows, paintRow{id: "tool", title: fmt.Sprintf("%d  %s", int(t)+1, paintToolNames[t]),
				sub: paintToolHelp(t), on: t == paint.tool})
		}
		return append(rows, paintRow{id: "size", title: fmt.Sprintf("Brush size: %d px", paintBrush()),
			sub: "Left and Right change it ([ and ] on a keyboard)"})
	case paintSheetFile:
		d := paint.doc
		if d == nil {
			return nil
		}
		save := "Pictures/Paint"
		if d.savedPath != "" {
			save = "Pictures/Paint/" + filepath.Base(d.savedPath)
		}
		grid := "Off"
		if paint.grid {
			grid = "On"
		}
		return []paintRow{
			{id: "save", title: "Save", sub: save},
			{id: "saveas", title: "Save as…", sub: "A new name; the original picture is never written over"},
			{id: "wall", title: "Use as wallpaper", sub: "Saved screen-sized next to the drawing, then set"},
			{id: "cover", title: "Use as game cover", sub: "Pick a game; pixel art is enlarged without blur"},
			{id: "grid", title: "Grid: " + grid, sub: "Pixel lines from 6× zoom (G)"},
			{id: "fit", title: "Fit to screen", sub: "The whole picture in view (0)"},
			{id: "undo", title: "Undo", sub: fmt.Sprintf("%d steps to take back", len(d.undo))},
			{id: "redo", title: "Redo", sub: fmt.Sprintf("%d steps to do again", len(d.redo))},
			{id: "home", title: "New or open…", sub: "Back to the start; this drawing stays under Continue"},
		}
	case paintSheetOpen:
		rows := make([]paintRow, 0, len(paintPics.list))
		for _, p := range paintPics.list {
			rows = append(rows, paintRow{id: "pic", title: p.name, sub: p.where + " · " + p.mod.Format("2 Jan 15:04")})
		}
		return rows
	case paintSheetGame:
		rows := make([]paintRow, 0, len(paintGames.shown))
		for _, i := range paintGames.shown {
			g := paintGames.all[i]
			rows = append(rows, paintRow{id: "game", title: g.title, sub: g.rom.System})
		}
		return rows
	}
	return nil
}

// paintToolHelp is a tool's one-line help.
func paintToolHelp(t paintTool) string {
	switch t {
	case paintPencil:
		return "Hold A and move to draw"
	case paintEraser:
		return "Clears pixels; on a screenshot it brings the picture back"
	case paintLine:
		return "A at one end, move, A at the other"
	case paintRect:
		return "A at one corner, move, A at the opposite one"
	case paintEllipse:
		return "Circle something: A at one corner, move, A again"
	case paintArrow:
		return "A at the tail, move, A at the point"
	case paintFill:
		return "Fills the area of one colour under the cursor"
	case paintPicker:
		return "Takes the colour under the cursor"
	}
	return ""
}

// paintSheetTitle is a sheet's heading.
func paintSheetTitle() string {
	switch paint.sheet {
	case paintSheetTools:
		return "Tools"
	case paintSheetColors:
		return "Colours"
	case paintSheetFile:
		return "File"
	case paintSheetOpen:
		return "Open a screenshot or drawing"
	case paintSheetGame:
		if paintGames.query != "" {
			return "Use as cover for… “" + paintGames.query + "”"
		}
		return "Use as cover for…"
	}
	return ""
}

// paintSheetLen is how many items the open sheet has.
func paintSheetLen() int {
	if paint.sheet == paintSheetColors {
		return len(paintSwatches())
	}
	return len(paintSheetRows())
}

// paintDrawSheet draws the open sheet over a dimmed body.
func paintDrawSheet(renderer *sdl.Renderer, config *Config) {
	small, medium := paintFonts(config)
	body := paintBody()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassScrimColor(1))
	_ = renderer.FillRect(&body)
	wide := paint.sheet == paintSheetOpen || paint.sheet == paintSheetGame
	w := min32(620, screenWidth-32)
	if wide {
		w = min32(1040, screenWidth-32)
	}
	maxH := body.H - 24
	rh := int32(54)
	if screenHeight < 640 {
		rh = 46
	}
	titleH := int32(52)
	var h int32
	if paint.sheet == paintSheetColors {
		rows := int32((len(paintSwatches()) + paintPaletteCols - 1) / paintPaletteCols)
		cell := min32((w-40)/paintPaletteCols, (maxH-titleH-60)/max32(rows, 1))
		cell = max32(cell, 24)
		h = titleH + rows*cell + 56
	} else {
		n := int32(max(len(paintSheetRows()), 1))
		h = min32(titleH+n*rh+24, maxH)
		if wide {
			h = maxH
		}
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: body.Y + (body.H-h)/2, W: w, H: h}
	drawGlassSheet(renderer, card, glassSheetFill())
	if medium != nil {
		title := ellipsize(medium, paintSheetTitle(), card.W-40)
		_, th := measureText(medium, title)
		drawText(renderer, medium, title, card.X+20, card.Y+(titleH-th)/2+4, ColorTextPrimary(), textAlignLeft)
	}
	inner := sdl.Rect{X: card.X + 12, Y: card.Y + titleH, W: card.W - 24, H: card.H - titleH - 12}
	if paint.sheet == paintSheetColors {
		paintDrawSwatches(renderer, small, inner)
		return
	}
	list := inner
	if paint.sheet == paintSheetOpen && card.W >= 720 {
		// The highlighted picture, beside the list.
		pw := card.W * 2 / 5
		list.W -= pw + 12
		paintDrawPreview(renderer, small, sdl.Rect{X: list.X + list.W + 12, Y: inner.Y, W: pw, H: inner.H})
	}
	paintDrawRows(renderer, small, medium, list, rh)
}

// paintDrawRows draws a list sheet's rows, scrolled to keep the highlight
// in view, or what the list is waiting for.
func paintDrawRows(renderer *sdl.Renderer, small, medium *ttf.Font, list sdl.Rect, rh int32) {
	rows := paintSheetRows()
	if len(rows) == 0 {
		msg, sub := "Nothing here yet", ""
		switch {
		case paint.sheet == paintSheetOpen && paintPics.loading,
			paint.sheet == paintSheetGame && paintGames.loading:
			msg = "Looking…"
		case paint.sheet == paintSheetOpen:
			sub = "Screenshots (F12, Menu, Select + R1 in a game) and saved drawings show here"
		case paint.sheet == paintSheetGame && paintGames.query != "":
			msg, sub = "No game matches", "Y searches again"
		case paint.sheet == paintSheetGame:
			sub = "No games with a cover folder were found on the card"
		}
		UIEmptyState(renderer, paint.config, list, "@image", msg, sub)
		return
	}
	paint.sheetCur = clampInt(paint.sheetCur, 0, len(rows)-1)
	visible := max(int(list.H/rh), 1)
	if paint.sheetCur < paint.sheetScroll {
		paint.sheetScroll = paint.sheetCur
	}
	if paint.sheetCur >= paint.sheetScroll+visible {
		paint.sheetScroll = paint.sheetCur - visible + 1
	}
	paint.sheetScroll = clampInt(paint.sheetScroll, 0, max(len(rows)-visible, 0))
	for i := paint.sheetScroll; i < len(rows) && i < paint.sheetScroll+visible; i++ {
		r := rows[i]
		rect := sdl.Rect{X: list.X, Y: list.Y + int32(i-paint.sheetScroll)*rh, W: list.W, H: rh - 4}
		focused := i == paint.sheetCur
		UIListRow(renderer, rect, 12, focused, false)
		if focused {
			drawGlassFocusRing(renderer, rect, 12)
		}
		tx := rect.X + 14
		tw := rect.W - 28
		if r.on {
			// The tool in use.
			fillCircle(renderer, rect.X+rect.W-20, rect.Y+rect.H/2, 5, glassAccent())
			tw -= 16
		}
		if medium != nil {
			_, th := measureText(medium, r.title)
			ty := rect.Y + (rect.H-th)/2
			if small != nil && r.sub != "" {
				ty = rect.Y + rect.H/2 - th + 2
			}
			drawText(renderer, medium, ellipsize(medium, r.title, tw), tx, ty, ColorTextPrimary(), textAlignLeft)
		}
		if small != nil && r.sub != "" {
			drawText(renderer, small, ellipsize(small, r.sub, tw), tx, rect.Y+rect.H/2+2, ColorTextSecondary(), textAlignLeft)
		}
		paint.hits = append(paint.hits, paintHit{r: rect, id: "row", index: i})
	}
	if len(rows) > visible {
		// A thin scroll mark at the list's right edge.
		trackH := list.H - 8
		thumbH := max32(trackH*int32(visible)/int32(len(rows)), 16)
		thumbY := list.Y + 4 + (trackH-thumbH)*int32(paint.sheetScroll)/int32(max(len(rows)-visible, 1))
		fillRoundedRect(renderer, list.X+list.W-4, thumbY, 3, thumbH, 1, glassA(80))
	}
}

// paintDrawSwatches draws the colour sheet's grid.
func paintDrawSwatches(renderer *sdl.Renderer, small *ttf.Font, inner sdl.Rect) {
	sw := paintSwatches()
	rows := int32((len(sw) + paintPaletteCols - 1) / paintPaletteCols)
	cell := min32((inner.W-16)/paintPaletteCols, (inner.H-36)/max32(rows, 1))
	gridW := cell * paintPaletteCols
	x0 := inner.X + (inner.W-gridW)/2
	paint.sheetCur = clampInt(paint.sheetCur, 0, len(sw)-1)
	for i, s := range sw {
		col, row := int32(i%paintPaletteCols), int32(i/paintPaletteCols)
		r := sdl.Rect{X: x0 + col*cell + 4, Y: inner.Y + row*cell + 4, W: cell - 8, H: cell - 8}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 8, sdl.Color{R: s.c.R, G: s.c.G, B: s.c.B, A: 255})
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 8, 1, glassA(70))
		if s.c == paint.color {
			// The colour in use: a dot in its contrasting ink.
			ink := contrastText(sdl.Color{R: s.c.R, G: s.c.G, B: s.c.B, A: 255})
			fillCircle(renderer, r.X+r.W/2, r.Y+r.H/2, max32(r.W/8, 3), ink)
		}
		if i == paint.sheetCur {
			drawGlassFocusRing(renderer, sdl.Rect{X: r.X - 3, Y: r.Y - 3, W: r.W + 6, H: r.H + 6}, 10)
		}
		paint.hits = append(paint.hits, paintHit{r: r, id: "row", index: i})
	}
	if small != nil {
		name := sw[paint.sheetCur].name + " · " + paintHex(sw[paint.sheetCur].c)[:7]
		nw, _ := measureText(small, name)
		drawText(renderer, small, name, inner.X+(inner.W-nw)/2, inner.Y+rows*cell+10, ColorTextSecondary(), textAlignLeft)
	}
}

// paintSheetButton answers the pad (and the keys standing for it) in a sheet.
func paintSheetButton(b uint8, config *Config) {
	n := paintSheetLen()
	step := func(delta int) {
		if n == 0 {
			return
		}
		next := clampInt(paint.sheetCur+delta, 0, n-1)
		if next != paint.sheetCur {
			paint.sheetCur = next
			PlayNavSound()
		}
	}
	page := 6
	switch b {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if paint.sheet == paintSheetColors {
			step(-paintPaletteCols)
		} else {
			step(-1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if paint.sheet == paintSheetColors {
			step(paintPaletteCols)
		} else {
			step(1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		delta := -1
		if b == sdl.CONTROLLER_BUTTON_DPAD_RIGHT {
			delta = 1
		}
		switch paint.sheet {
		case paintSheetColors:
			step(delta)
		case paintSheetTools:
			paintStepSize(delta)
		case paintSheetOpen, paintSheetGame:
			step(delta * page)
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		delta := -1
		if b == sdl.CONTROLLER_BUTTON_RIGHTSHOULDER {
			delta = 1
		}
		if paint.sheet == paintSheetGame {
			paintGameLetterJump(delta)
		} else {
			step(delta * page)
		}
	case sdl.CONTROLLER_BUTTON_A:
		paintSheetActivate(config)
	case sdl.CONTROLLER_BUTTON_B:
		paintCloseSheet()
	case sdl.CONTROLLER_BUTTON_X:
		if paint.sheet == paintSheetTools {
			paintCloseSheet()
		}
	case sdl.CONTROLLER_BUTTON_Y:
		switch paint.sheet {
		case paintSheetColors:
			paintCloseSheet()
		case paintSheetGame:
			paintGameSearch()
		}
	case sdl.CONTROLLER_BUTTON_BACK:
		if paint.sheet == paintSheetFile {
			paintCloseSheet()
		}
	}
}

// paintSheetActivate runs the highlighted row.
func paintSheetActivate(config *Config) {
	i := paint.sheetCur
	switch paint.sheet {
	case paintSheetTools:
		if i == int(paintToolCount) {
			paint.sizeIdx = (paint.sizeIdx + 1) % len(paintBrushSizes)
			PlayNavSound()
			return
		}
		paintSetTool(paintTool(i))
		paint.sheet = paintSheetNone
		PlayActivateSound()
	case paintSheetColors:
		sw := paintSwatches()
		if i < 0 || i >= len(sw) {
			return
		}
		paint.color = sw[i].c
		if paint.tool == paintEraser || paint.tool == paintPicker {
			// Choosing a colour means drawing with it.
			paint.tool = paint.lastDrawTool
			if paint.tool == paintEraser {
				paint.tool, paint.lastDrawTool = paintPencil, paintPencil
			}
		}
		paint.sheet = paintSheetNone
		PlayActivateSound()
	case paintSheetFile:
		rows := paintSheetRows()
		if i < 0 || i >= len(rows) {
			return
		}
		paint.sheet = paintSheetNone
		switch rows[i].id {
		case "save":
			paintSave()
		case "saveas":
			paintSaveAs()
		case "wall":
			paintUseAsWallpaper(config)
		case "cover":
			paintOpenGamePicker()
		case "grid":
			paintToggleGrid()
		case "fit":
			paintZoomFit()
		case "undo":
			paintUndo()
		case "redo":
			paintRedo()
		case "home":
			paintAutosave(false)
			paint.view = paintViewStart
			paint.startSel = ""
			PlayBackSound()
		}
	case paintSheetOpen:
		if i < 0 || i >= len(paintPics.list) {
			return
		}
		path := paintPics.list[i].path
		if !paintConfirmDiscard("open:" + path) {
			return
		}
		paintPreviewRelease()
		paintOpenPicture(path)
	case paintSheetGame:
		if i < 0 || i >= len(paintGames.shown) {
			return
		}
		g := paintGames.all[paintGames.shown[i]]
		paint.sheet = paintSheetNone
		paintUseAsCover(g)
	}
}

// paintGameSearch asks for part of a game's name.
func paintGameSearch() {
	osPromptOpen("Find a game", "Part of the name", "Find", paintGames.query, 40, func(q string) {
		if paint.sheet != paintSheetGame {
			return
		}
		paintGames.query = q
		paint.sheetCur, paint.sheetScroll = 0, 0
		paintGamesFilter()
	})
}

// paintGameLetterJump moves the game list to the next (or previous) first
// letter.
func paintGameLetterJump(delta int) {
	shown := paintGames.shown
	if len(shown) == 0 {
		return
	}
	first := func(row int) byte {
		k := paintGames.all[shown[row]].key
		if k == "" {
			return 0
		}
		return k[0]
	}
	cur := clampInt(paint.sheetCur, 0, len(shown)-1)
	letter := first(cur)
	i := cur
	for {
		i += delta
		if i < 0 || i >= len(shown) {
			PlayErrorSound()
			return
		}
		if first(i) != letter {
			break
		}
	}
	if delta < 0 {
		// To the first game of that letter.
		l := first(i)
		for i > 0 && first(i-1) == l {
			i--
		}
	}
	paint.sheetCur = i
	PlayNavSound()
}

// ── The Open list's preview ───────────────────────────────────────────────

// paintPreview is the highlighted picture's thumbnail in the Open list.
var paintPreview struct {
	path    string // what the texture shows
	want    string // what is highlighted
	wantAt  uint64 // since when; a decode starts once the cursor rests
	gen     int
	tex     *sdl.Texture
	w, h    int32
	ready   *image.NRGBA
	readyOf string
	failed  bool
}

// paintPreviewRelease frees the thumbnail.
func paintPreviewRelease() {
	if paintPreview.tex != nil {
		paintPreview.tex.Destroy()
	}
	paintPreview.tex = nil
	paintPreview.path, paintPreview.want, paintPreview.readyOf = "", "", ""
	paintPreview.ready = nil
	paintPreview.gen++
}

// paintDrawPreview draws the highlighted picture, decoding it in the
// background once the highlight has rested a moment.
func paintDrawPreview(renderer *sdl.Renderer, small *ttf.Font, box sdl.Rect) {
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 12, glassA(10))
	if paint.sheetCur < 0 || paint.sheetCur >= len(paintPics.list) {
		return
	}
	want := paintPics.list[paint.sheetCur].path
	now := sdl.GetTicks64()
	if want != paintPreview.want {
		paintPreview.want, paintPreview.wantAt = want, now
		paintPreview.failed = false
	}
	if paintPreview.ready != nil && paintPreview.readyOf == want {
		if paintPreview.tex != nil {
			paintPreview.tex.Destroy()
			paintPreview.tex = nil
		}
		pic := paintPreview.ready
		paintPreview.ready = nil
		if tex := imgvTexture(renderer, pic); tex != nil {
			paintPreview.tex = tex
			paintPreview.w, paintPreview.h = int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
			paintPreview.path = want
		}
	}
	if paintPreview.path != want && !paintPreview.failed && now-paintPreview.wantAt > 150 && paintPreview.readyOf != want {
		paintPreview.gen++
		gen := paintPreview.gen
		paintPreview.readyOf = want
		edge := int(max32(box.W, box.H))
		go func() {
			pic, err := paintDecodeNRGBA(want, edge)
			tbxPost(func() {
				if gen != paintPreview.gen {
					return
				}
				if err != nil {
					paintPreview.failed = true
					return
				}
				paintPreview.ready = pic
			})
		}()
	}
	if paintPreview.tex != nil && paintPreview.path == want {
		fw, fh := imgvFitInside(int(paintPreview.w), int(paintPreview.h), int(box.W-16), int(box.H-16))
		dst := sdl.Rect{X: box.X + (box.W-int32(fw))/2, Y: box.Y + (box.H-int32(fh))/2, W: int32(fw), H: int32(fh)}
		_ = renderer.Copy(paintPreview.tex, nil, &dst)
		return
	}
	if small != nil {
		msg := "…"
		if paintPreview.failed {
			msg = "No preview"
		}
		mw, mh := measureText(small, msg)
		drawText(renderer, small, msg, box.X+(box.W-mw)/2, box.Y+(box.H-mh)/2, ColorTextTertiary(), textAlignLeft)
	}
}

// ── Footer ────────────────────────────────────────────────────────────────

// paintFooterHints are the footer's badges for what is on screen. The
// keyboard's halves follow handleKey: T tools, C colours, Z / Ctrl+Y undo
// and redo, - and + zoom, M the File menu, F finds a game, PgUp/PgDn jump
// by letter.
func paintFooterHints() []FooterHint {
	if paint.loading {
		return []FooterHint{{Button: "B", Label: "Cancel"}}
	}
	switch paint.sheet {
	case paintSheetTools:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "← →", Label: "Brush size"},
			{Button: "A", Label: "Use"},
			{Button: "B", Label: "Close"},
		}
	case paintSheetColors:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Use"},
			{Button: "B", Label: "Close"},
		}
	case paintSheetFile:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Run"},
			{Button: "B", Label: "Close"},
		}
	case paintSheetOpen:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Open"},
			{Button: "L1/R1", Label: "Page"},
			{Button: "B", Label: "Close"},
		}
	case paintSheetGame:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Use as cover"},
			{Button: "Y / F", Label: "Find"},
			{Button: "L1/R1 / PgUp/PgDn", Label: "Letter"},
			{Button: "B", Label: "Cancel"},
		}
	}
	if paint.view != paintViewCanvas || paint.doc == nil {
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Open"},
			{Button: "B", Label: "Back"},
		}
	}
	a, b := "Draw", "Back"
	switch paint.tool {
	case paintEraser:
		a = "Erase"
	case paintLine, paintRect, paintEllipse, paintArrow:
		a = "Start"
		if paint.shapeOn {
			a, b = "Place", "Cancel"
		}
	case paintFill:
		a = "Fill"
	case paintPicker:
		a = "Pick colour"
	}
	return []FooterHint{
		{Button: "A", Label: a},
		{Button: "B", Label: b},
		{Button: "X / T", Label: "Tools"},
		{Button: "Y / C", Label: "Colours"},
		{Button: "L1/R1 / Z / Ctrl+Y", Label: "Undo / Redo"},
		{Button: "L2/R2 / - / +", Label: "Zoom"},
		{Button: "Select / M", Label: "File"},
	}
}
