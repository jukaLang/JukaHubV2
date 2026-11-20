package main

// paint.go — Paint: pixel art drawn with the D-pad or the stick, and markup
// over a screenshot (circle something, point an arrow at it), with undo, saved
// as a PNG and usable as the wallpaper or a game's cover.
//
// The app opens on a short start screen (Continue, a new canvas of 16 to 128
// pixels or the screen's size, Open a screenshot or drawing). The canvas is
// one picture in memory (paint_canvas.go); this file holds the app's state,
// the contract the shell calls (paintEnter, paintLeave, paintFlush, the
// footer, the pointer and the Tools hub tile) and the input. Drawing lives in
// paint_ui.go, files in paint_save.go.
//
//	pad       D-pad one pixel (held: repeats, faster the longer) · left stick
//	          glides · A draws (held) / places a shape's corner · B cancels a
//	          shape, else leaves the canvas · X tools · Y colours · L1 undo ·
//	          R1 redo · L2/R2 zoom · Select the File menu
//	keyboard  arrows (Shift: 8 pixels) · Enter/Space draw · Esc · T tools ·
//	          C colours · Z or Ctrl+Z undo · Ctrl+Y redo · - and + zoom, 0 fit ·
//	          [ and ] brush size · 1–8 tools · G grid · M File menu · Ctrl+S
//	          save
//	mouse     the left button draws, the wheel zooms, the bars click
//
// Start stays the Start menu here, as on every ordinary screen, so the File
// menu is on Select and undo and redo have the shoulders; the brush size is
// in the tool sheet (Left/Right) and on [ ].

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// paintViewKind is which page of the app is on screen.
type paintViewKind int

const (
	paintViewStart paintViewKind = iota
	paintViewCanvas
)

// paintSheetKind is the sheet open over the page, if any.
type paintSheetKind int

const (
	paintSheetNone paintSheetKind = iota
	paintSheetTools
	paintSheetColors
	paintSheetFile
	paintSheetOpen
	paintSheetGame
)

// Where a held stroke came from: the pad's A (or Enter/Space), or the mouse.
const (
	paintSrcPad = iota + 1
	paintSrcMouse
)

// paintAutosaveEvery is how often a changed canvas is autosaved while drawing.
const paintAutosaveEvery = 45 * time.Second

// paintHold follows a held direction for the accelerating repeat.
type paintHold struct {
	dx, dy      int
	since, next uint64
}

// paintHit is one clickable thing drawn this frame.
type paintHit struct {
	r     sdl.Rect
	id    string
	index int
}

// paint is the app's state. Main thread only; goroutines post back through
// tbxPost.
var paint struct {
	config *Config
	view   paintViewKind
	doc    *paintDoc

	tool, lastDrawTool paintTool
	sizeIdx            int
	color              color.NRGBA
	picked             color.NRGBA // the eyedropper's last colour (A 0: none)
	grid               bool

	zoom       float64
	panX, panY float64 // the canvas point at the viewport's top-left
	viewport   sdl.Rect
	curX, curY int
	subX, subY float64 // the stick's travel short of a whole pixel

	drawing          bool // a held stroke (pencil, eraser, a dragged shape)
	drawSrc          int
	lastX, lastY     int
	shapeOn          bool // a shape's first corner is placed
	anchorX, anchorY int

	hold     paintHold
	lastTick uint64

	sheet       paintSheetKind
	sheetCur    int
	sheetScroll int
	// startSel is the start screen's highlighted row by id, so a Continue
	// row that appears once the autosave has been read does not move it;
	// "" is the default row (paintStartIndex).
	startSel string

	// loading is a picture being opened; loadGen drops a load B gave up on.
	loading bool
	loadGen int
	// pendingOpen is a picture the image viewer asked to mark up.
	pendingOpen string

	lastAutosave time.Time
	hits         []paintHit

	// confirmID is an action that throws away unsaved work and waits for
	// its second press (paintConfirm).
	confirmID string
	confirmAt time.Time
}

// paintBrush is the brush size in pixels.
func paintBrush() int {
	return paintBrushSizes[clampInt(paint.sizeIdx, 0, len(paintBrushSizes)-1)]
}

// paintIsCurrent reports whether Paint is the scene on screen.
func paintIsCurrent(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == paintSceneName
}

// paintOwnsPad reports whether nothing floats over Paint: no Start menu,
// keyboard, Quick Settings, popover or panel. The held D-pad, the stick and
// the triggers are only Paint's then. The on-screen keyboard (Save as, Find
// a game) is one of them: its stick steps its keys and R2 is voice typing.
func paintOwnsPad(config *Config) bool {
	if !paintIsCurrent(config) {
		return false
	}
	switch {
	case virtualKeyboardActive, tb.startOpen, nc.open, globalSearchOpen, shortcutsOpen, clipSearchOpen, wpOpen,
		weatherPopoverOpen, alarmFiringActive():
		return false
	}
	return !inmapOverlayOpen() && !triggerOwnedByOverlay() && !modalPanelBlocksShoulders()
}

// ── The contract the shell calls ──────────────────────────────────────────

// paintEnter runs when Paint becomes the scene on screen.
func paintEnter(config *Config) {
	paint.config = config
	paint.sheet = paintSheetNone
	paint.hold = paintHold{}
	paint.lastTick = sdl.GetTicks64()
	paintWIPRefresh()
	if paint.pendingOpen != "" {
		paintTakePendingOpen()
		return
	}
	if paint.doc == nil {
		paint.view = paintViewStart
		paint.startSel = ""
	}
}

// paintLeave runs when another scene takes the screen: the drawing is
// autosaved, its textures go, and the undo history is cut down to what is
// worth keeping while a game may need the memory.
func paintLeave() {
	paintEndStroke()
	paintAutosave(false)
	paintReleaseTexture()
	paintReleaseChecker()
	paintPreviewRelease()
	if paint.doc != nil {
		paint.doc.trimHistory(paintUndoKeepOnLeave)
	}
	paint.sheet = paintSheetNone
	paint.hold = paintHold{}
}

// paintFlush runs when JukaHub quits: the drawing is autosaved before it
// returns, and a save or autosave still running on its goroutine (B, then
// quitting at once) is waited for, a few seconds at most.
func paintFlush() {
	paintEndStroke()
	paintAutosave(true)
	paintWaitWrites(5 * time.Second)
}

// paintHubValue is the Tools hub tile's line: cached state only.
func paintHubValue() (string, bool) {
	if d := paint.doc; d != nil {
		return "Continue your " + paintDocDesc(d), true
	}
	if !paintWIP.known {
		paintWIPRefresh()
		return "", false
	}
	if paintWIP.exists {
		return "Continue your " + paintWIP.desc, true
	}
	return "", false
}

// paintDocDesc names a canvas in a few words: "64×64 drawing", "markup of
// game_2026-11-19.png".
func paintDocDesc(d *paintDoc) string {
	if d.orig != nil && d.source != "" {
		return "markup of " + filepath.Base(d.source)
	}
	if d.pixelArt {
		return fmt.Sprintf("%d×%d drawing", d.w, d.h)
	}
	return fmt.Sprintf("%d×%d sketch", d.w, d.h)
}

// paintMarkUp opens a picture in Paint as a markup canvas: the image viewer's
// Mark up. The viewer is closed first; Paint opens on the picture.
func paintMarkUp(config *Config, path string) {
	if strings.TrimSpace(path) == "" || config == nil {
		return
	}
	paint.pendingOpen = path
	closeImageViewer()
	osAppsOpen(config, paintSceneName)
	if paintIsCurrent(config) {
		// Already on screen (or the scene change is immediate): open now.
		paintTakePendingOpen()
	}
}

// paintTakePendingOpen starts opening the picture the viewer handed over.
// The viewer cannot ask first, so a drawing with unsaved changes is saved
// before the markup takes its place.
func paintTakePendingOpen() {
	path := paint.pendingOpen
	paint.pendingOpen = ""
	if path == "" {
		return
	}
	paintKeepUnsaved()
	paintOpenPicture(path)
}

// paintKeepUnsaved saves a drawing with unsaved changes into Pictures/Paint
// (under its own name, or a new one) before something replaces it without
// asking: the autosave holds one drawing only.
func paintKeepUnsaved() {
	d := paint.doc
	if d == nil {
		// Since a restart an unsaved drawing is only in the autosave, which
		// the new canvas's autosave would replace.
		paintKeepWIP()
		return
	}
	if !d.changed {
		return
	}
	paintEndStroke()
	path := d.savedPath
	if path == "" {
		path = paintUniquePath(paintPicturesDir(), paintCleanName(paintSeedName(d)), "")
	}
	paintWriteDrawing(d, path)
}

// ── Starting a canvas ─────────────────────────────────────────────────────

// paintNewCanvas starts a blank canvas: transparent pixel art, or a white
// screen-sized sketch.
func paintNewCanvas(w, h int, pixelArt bool) {
	paintEndStroke()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	var paper *image.NRGBA
	if !pixelArt {
		// A sketch is drawn on white paper, which its eraser brings back
		// the way a markup's eraser brings the screenshot back.
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		paper = paintCloneNRGBA(img)
	}
	paintSetDoc(newPaintDoc(img, paper, "", pixelArt))
	paint.color = paintPalette[0].c
	paint.tool, paint.lastDrawTool = paintPencil, paintPencil
	paint.sizeIdx = 0
	if !pixelArt {
		paint.sizeIdx = 2
	}
	paint.doc.autoDirty = true
	PlayActivateSound()
}

// paintSetDoc makes d the canvas: the cursor in the middle, the whole
// picture in view, the old texture and history gone.
func paintSetDoc(d *paintDoc) {
	paintReleaseTexture()
	paint.doc = d
	paint.view = paintViewCanvas
	paint.sheet = paintSheetNone
	paint.drawing, paint.shapeOn = false, false
	paint.curX, paint.curY = d.w/2, d.h/2
	paint.subX, paint.subY = 0, 0
	paint.grid = d.pixelArt
	paintLayout()
	paint.zoom = paintFitZoom(d)
	paintClampPan(true)
	paint.lastAutosave = time.Now()
}

// ── Zoom and pan ──────────────────────────────────────────────────────────

// paintLayout sets the canvas viewport: the body between the shared bars,
// under Paint's own tool strip.
func paintLayout() {
	top := headerBarHeight()
	bottom := screenHeight - footerBarHeight()
	strip := paintStripHeight()
	margin := int32(12)
	paint.viewport = sdl.Rect{
		X: margin,
		Y: top + strip + 8,
		W: max32(screenWidth-2*margin, 32),
		H: max32(bottom-top-strip-16, 32),
	}
}

// paintStripHeight is the tool strip's height: smaller on a small screen.
func paintStripHeight() int32 {
	if screenHeight < 640 {
		return 34
	}
	return 42
}

// paintFitZoom is the zoom that shows the whole canvas: whole pixels for
// pixel art (and anything that fits at 1:1 or more), any fraction for a
// screenshot larger than the screen.
func paintFitZoom(d *paintDoc) float64 {
	vw, vh := float64(paint.viewport.W), float64(paint.viewport.H)
	fit := math.Min(vw/float64(d.w), vh/float64(d.h))
	if fit >= 1 {
		return math.Floor(fit)
	}
	if d.pixelArt {
		return 1
	}
	return fit
}

// paintZoomStops are the whole-number zooms past fit.
var paintZoomStops = [...]float64{1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64}

// paintZoomLevels are the zooms the triggers step through: fit, then the
// whole-number stops above it, up to a sixth of the viewport per pixel.
func paintZoomLevels(d *paintDoc) []float64 {
	fit := paintFitZoom(d)
	limit := math.Max(fit, float64(min32(paint.viewport.W, paint.viewport.H))/6)
	levels := make([]float64, 0, len(paintZoomStops)+1)
	levels = append(levels, fit)
	for _, z := range paintZoomStops {
		if z > fit+1e-9 && z <= limit {
			levels = append(levels, z)
		}
	}
	return levels
}

// paintZoomBy steps the zoom, keeping the cursor where it is on screen.
func paintZoomBy(steps int) {
	d := paint.doc
	if d == nil || steps == 0 {
		return
	}
	levels := paintZoomLevels(d)
	i := 0
	for j, z := range levels {
		if z <= paint.zoom+1e-6 {
			i = j
		}
	}
	next := clampInt(i+steps, 0, len(levels)-1)
	if levels[next] == paint.zoom {
		PlayErrorSound()
		return
	}
	paintSetZoom(levels[next])
	PlayNavSound()
}

// paintSetZoom changes the zoom around the cursor.
func paintSetZoom(z float64) {
	old := paint.zoom
	if old <= 0 {
		old = z
	}
	fx := float64(paint.curX) + 0.5
	fy := float64(paint.curY) + 0.5
	sx := (fx - paint.panX) * old
	sy := (fy - paint.panY) * old
	paint.zoom = z
	paint.panX = fx - sx/z
	paint.panY = fy - sy/z
	paintClampPan(false)
	paintFollowCursor()
}

// paintZoomFit shows the whole canvas again.
func paintZoomFit() {
	if paint.doc == nil {
		return
	}
	paint.zoom = paintFitZoom(paint.doc)
	paintClampPan(true)
	PlayNavSound()
}

// paintClampPan keeps the canvas on screen: centred along an axis where it
// is smaller than the viewport, edge to edge where it is larger. centre
// re-centres a larger canvas on the cursor.
func paintClampPan(centre bool) {
	d := paint.doc
	if d == nil || paint.zoom <= 0 {
		return
	}
	vw := float64(paint.viewport.W) / paint.zoom
	vh := float64(paint.viewport.H) / paint.zoom
	clampAxis := func(pan, view float64, size int, cur int) float64 {
		if float64(size) <= view {
			return -(view - float64(size)) / 2
		}
		if centre {
			pan = float64(cur) + 0.5 - view/2
		}
		return clampFloat(pan, 0, float64(size)-view)
	}
	paint.panX = clampAxis(paint.panX, vw, d.w, paint.curX)
	paint.panY = clampAxis(paint.panY, vh, d.h, paint.curY)
}

// paintFollowCursor pans just enough to keep the cursor a little way inside
// the viewport.
func paintFollowCursor() {
	d := paint.doc
	if d == nil || paint.zoom <= 0 {
		return
	}
	vw := float64(paint.viewport.W) / paint.zoom
	vh := float64(paint.viewport.H) / paint.zoom
	margin := math.Max(1, 40/paint.zoom)
	follow := func(pan, view float64, cur int) float64 {
		c := float64(cur)
		m := math.Min(margin, view/3)
		if c < pan+m {
			pan = c - m
		}
		if c+1 > pan+view-m {
			pan = c + 1 - view + m
		}
		return pan
	}
	paint.panX = follow(paint.panX, vw, paint.curX)
	paint.panY = follow(paint.panY, vh, paint.curY)
	paintClampPan(false)
}

// paintScreenToCanvas is the canvas pixel under a screen point.
func paintScreenToCanvas(mx, my int32) (int, int) {
	x := paint.panX + float64(mx-paint.viewport.X)/paint.zoom
	y := paint.panY + float64(my-paint.viewport.Y)/paint.zoom
	return int(math.Floor(x)), int(math.Floor(y))
}

// ── Drawing ───────────────────────────────────────────────────────────────

// paintMoveCursorTo moves the cursor to a canvas point (kept on the canvas)
// and carries the stroke or the shape preview along.
func paintMoveCursorTo(x, y int) bool {
	d := paint.doc
	if d == nil {
		return false
	}
	x, y = clampInt(x, 0, d.w-1), clampInt(y, 0, d.h-1)
	if x == paint.curX && y == paint.curY {
		return false
	}
	paint.curX, paint.curY = x, y
	switch {
	case paint.drawing && !paintToolShape(paint.tool):
		d.line(paint.lastX, paint.lastY, x, y, paintBrush(), paint.color, paint.tool == paintEraser)
		paint.lastX, paint.lastY = x, y
	case paint.shapeOn:
		d.previewShape(paint.tool, paint.anchorX, paint.anchorY, x, y, paintBrush(), paint.color)
	}
	paintFollowCursor()
	return true
}

// paintPress is A (or Enter, Space, a click) on the canvas.
func paintPress(src int) {
	d := paint.doc
	if d == nil {
		return
	}
	x, y := paint.curX, paint.curY
	switch paint.tool {
	case paintPencil, paintEraser:
		if paint.drawing {
			return
		}
		paint.drawing, paint.drawSrc = true, src
		paint.lastX, paint.lastY = x, y
		d.stamp(x, y, paintBrush(), paint.color, paint.tool == paintEraser)
	case paintLine, paintRect, paintEllipse, paintArrow:
		if !paint.shapeOn {
			paint.shapeOn = true
			paint.anchorX, paint.anchorY = x, y
			paint.drawing, paint.drawSrc = src == paintSrcMouse, src
			d.previewShape(paint.tool, x, y, x, y, paintBrush(), paint.color)
			PlayNavSound()
			return
		}
		d.previewShape(paint.tool, paint.anchorX, paint.anchorY, x, y, paintBrush(), paint.color)
		paint.shapeOn, paint.drawing = false, false
		paintCommit()
		PlayActivateSound()
	case paintFill:
		paintEndStroke()
		d.fill(x, y, paint.color, false)
		paintCommit()
		PlayActivateSound()
	case paintPicker:
		c := d.at(x, y)
		if c.A == 0 {
			ShowToast("That pixel is see-through — pick a coloured one", ToastKindInfo)
			PlayErrorSound()
			return
		}
		paint.color, paint.picked = c, c
		paint.tool = paint.lastDrawTool
		ShowToast(fmt.Sprintf("Colour picked: #%02X%02X%02X — back to the %s", c.R, c.G, c.B,
			strings.ToLower(paintToolNames[paint.tool])), ToastKindInfo)
		PlayActivateSound()
	}
}

// paintCommit ends a stroke as one undo step.
func paintCommit() {
	if paint.doc != nil {
		paint.doc.commitStroke()
	}
	paint.drawing = false
}

// paintEndStroke finishes whatever is in progress: a held stroke is kept, a
// half-placed shape is dropped.
func paintEndStroke() {
	d := paint.doc
	if d == nil {
		paint.drawing, paint.shapeOn = false, false
		return
	}
	if paint.shapeOn {
		d.cancelStroke()
		paint.shapeOn = false
	}
	if paint.drawing {
		paintCommit()
	}
	paint.drawing = false
}

// paintCancel is B on the canvas: a shape in progress goes; otherwise the
// canvas is left for the start screen (the drawing stays, and is autosaved).
func paintCancel() {
	if paint.shapeOn {
		paint.doc.cancelStroke()
		paint.shapeOn, paint.drawing = false, false
		PlayBackSound()
		return
	}
	paintEndStroke()
	paintAutosave(false)
	paint.view = paintViewStart
	paint.startSel = ""
	PlayBackSound()
}

// paintUndo and paintRedo step through the history.
func paintUndo() {
	if paint.doc == nil {
		return
	}
	paintEndStroke()
	if paint.doc.undoStep() {
		PlayNavSound()
		return
	}
	ShowToast("Nothing to undo", ToastKindInfo)
	PlayErrorSound()
}

func paintRedo() {
	if paint.doc == nil {
		return
	}
	paintEndStroke()
	if paint.doc.redoStep() {
		PlayNavSound()
		return
	}
	ShowToast("Nothing to redo", ToastKindInfo)
	PlayErrorSound()
}

// paintSetTool picks a tool; a shape half placed with the old one goes.
func paintSetTool(t paintTool) {
	if t < 0 || t >= paintToolCount {
		return
	}
	paintEndStroke()
	if t != paintPicker {
		paint.lastDrawTool = t
	}
	paint.tool = t
}

// paintStepSize changes the brush size.
func paintStepSize(delta int) {
	next := clampInt(paint.sizeIdx+delta, 0, len(paintBrushSizes)-1)
	if next == paint.sizeIdx {
		PlayErrorSound()
		return
	}
	paint.sizeIdx = next
	PlayNavSound()
}

// paintToggleGrid shows or hides the pixel grid.
func paintToggleGrid() {
	paint.grid = !paint.grid
	if paint.grid {
		ShowToast("Grid on (it shows from 6× zoom)", ToastKindInfo)
	} else {
		ShowToast("Grid off", ToastKindInfo)
	}
	PlayNavSound()
}

// ── The frame tick ────────────────────────────────────────────────────────

// paintTick runs once a frame before the canvas is drawn: the held stroke's
// end, the held direction's repeat, the stick's glide and the autosave.
func paintTick(config *Config) {
	now := sdl.GetTicks64()
	dt := now - paint.lastTick
	if dt > 100 {
		dt = 100
	}
	paint.lastTick = now
	if paint.pendingOpen != "" {
		paintTakePendingOpen()
	}
	d := paint.doc
	if paint.view != paintViewCanvas || d == nil || paint.sheet != paintSheetNone || paint.loading || !paintOwnsPad(config) {
		// Something took the screen mid-stroke: the stroke is kept as it is.
		paint.hold = paintHold{}
		if paint.drawing && paint.drawSrc == paintSrcPad {
			paintCommit()
		}
		return
	}
	moved := false
	// A stroke drawn with A lasts while A (Enter, Space) is held.
	if paint.drawing && paint.drawSrc == paintSrcPad && !paintToolShape(paint.tool) && !paintDrawHeld() {
		paintCommit()
	}
	// The held direction repeats, faster the longer it is held.
	dx, dy := paintHeldDirection()
	switch {
	case dx == 0 && dy == 0:
		paint.hold = paintHold{}
	case dx != paint.hold.dx || dy != paint.hold.dy:
		paint.hold = paintHold{dx: dx, dy: dy, since: now, next: now + uint64(a11yRepeatDelayMs())}
	case now >= paint.hold.next:
		held := now - paint.hold.since
		interval := int64(90) - int64(held)/20
		if interval < 16 {
			interval = 16
		}
		step := 1
		if !d.pixelArt && held > 1500 {
			step = 2 + int(held-1500)/1000
			step = min(step, 6)
		}
		paint.hold.next = now + uint64(interval)
		if paintMoveCursorTo(paint.curX+dx*step, paint.curY+dy*step) {
			moved = true
		}
	}
	// The left stick glides: speed grows with the push past the dead zone.
	if sx, sy := stickShaped(config, stickLeft); sx != 0 || sy != 0 {
		mag := math.Min(1, math.Hypot(sx, sy))
		screenSpeed := 40 + 1000*mag*mag // screen pixels a second
		speed := math.Max(screenSpeed/paint.zoom, 4*mag)
		paint.subX += sx / mag * speed * float64(dt) / 1000
		paint.subY += sy / mag * speed * float64(dt) / 1000
		ix, iy := int(paint.subX), int(paint.subY)
		paint.subX -= float64(ix)
		paint.subY -= float64(iy)
		if (ix != 0 || iy != 0) && paintMoveCursorTo(paint.curX+ix, paint.curY+iy) {
			moved = true
		}
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
	} else {
		paint.subX, paint.subY = 0, 0
	}
	if (moved || paint.hold.dx != 0 || paint.hold.dy != 0) && frameRateCtrl != nil {
		frameRateCtrl.NotifyInput()
	}
	if d.autoDirty && !paint.drawing && !paint.shapeOn && time.Since(paint.lastAutosave) > paintAutosaveEvery {
		paintAutosave(false)
	}
}

// paintHeldDirection is the direction held on the pad (through the
// accessibility remap) and the arrow keys.
func paintHeldDirection() (int, int) {
	dx, dy := vkControllerNavDirection()
	keys := sdl.GetKeyboardState()
	held := func(sc int) bool { return sc < len(keys) && keys[sc] != 0 }
	if held(int(sdl.SCANCODE_LEFT)) {
		dx = -1
	}
	if held(int(sdl.SCANCODE_RIGHT)) {
		dx = 1
	}
	if held(int(sdl.SCANCODE_UP)) {
		dy = -1
	}
	if held(int(sdl.SCANCODE_DOWN)) {
		dy = 1
	}
	return dx, dy
}

// paintDrawHeld reports whether the draw button is still down: A on the pad
// (whichever physical button the accessibility remap makes A), or Enter or
// Space.
func paintDrawHeld() bool {
	keys := sdl.GetKeyboardState()
	for _, sc := range [...]int{int(sdl.SCANCODE_SPACE), int(sdl.SCANCODE_RETURN), int(sdl.SCANCODE_KP_ENTER)} {
		if sc < len(keys) && keys[sc] != 0 {
			return true
		}
	}
	pad := gameController
	if pad == nil {
		return false
	}
	for phys := 0; phys < int(sdl.CONTROLLER_BUTTON_MAX); phys++ {
		if pad.Button(sdl.GameControllerButton(phys)) == 0 {
			continue
		}
		if logical, dropped := a11yResolveButton(uint8(phys)); !dropped && logical == sdl.CONTROLLER_BUTTON_A {
			return true
		}
	}
	return false
}

// paintStickAxis is controller.go's hook, ahead of the stick's D-pad steps:
// while the canvas has the pad the left stick moves the cursor smoothly
// (paintTick reads it) instead of stepping like the D-pad, and the triggers
// zoom. It reports whether the axis event was Paint's.
func paintStickAxis(config *Config, axis uint8, value int16) bool {
	if !paintOwnsPad(config) {
		return false
	}
	switch axis {
	case sdl.CONTROLLER_AXIS_TRIGGERLEFT, sdl.CONTROLLER_AXIS_TRIGGERRIGHT:
		side, btn, steps := 0, inmapBtnL2, -1
		if axis == sdl.CONTROLLER_AXIS_TRIGGERRIGHT {
			side, btn, steps = 1, inmapBtnR2, 1
		}
		if !triggerPressedEdge(side, value) {
			return true
		}
		// A control profile may map the trigger (inmap_runtime.go).
		if inmapTriggerPress(btn, config) {
			return true
		}
		if paint.view == paintViewCanvas && paint.sheet == paintSheetNone && !paint.loading {
			paintZoomBy(steps)
		}
		return true
	case sdl.CONTROLLER_AXIS_LEFTX, sdl.CONTROLLER_AXIS_LEFTY:
		// In a sheet or on the start screen the stick steps like the D-pad.
		return paint.view == paintViewCanvas && paint.sheet == paintSheetNone && paint.doc != nil && !paint.loading
	}
	return false
}

// ── Pad ───────────────────────────────────────────────────────────────────

// paintController answers the pad.
func paintController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || virtualKeyboardActive {
		return false
	}
	paint.config = config
	if paint.loading {
		if e.Button == sdl.CONTROLLER_BUTTON_B {
			paintCancelLoad()
		}
		return true
	}
	if paint.sheet != paintSheetNone {
		paintSheetButton(e.Button, config)
		return true
	}
	if paint.view == paintViewStart {
		paintStartButton(e.Button, config)
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT,
		sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		// A held direction is repeated by paintTick, with its own
		// acceleration; the shell's even-paced repeat is not used here.
		if e.Which == padRepeatWhich {
			return true
		}
		dx, dy := paintButtonDirection(e.Button)
		paintMoveCursorTo(paint.curX+dx, paint.curY+dy)
	case sdl.CONTROLLER_BUTTON_A:
		paintPress(paintSrcPad)
	case sdl.CONTROLLER_BUTTON_B:
		paintCancel()
	case sdl.CONTROLLER_BUTTON_X:
		paintOpenSheet(paintSheetTools)
	case sdl.CONTROLLER_BUTTON_Y:
		paintOpenSheet(paintSheetColors)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		paintUndo()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		paintRedo()
	case sdl.CONTROLLER_BUTTON_BACK:
		paintOpenSheet(paintSheetFile)
	}
	return true
}

// paintButtonDirection is a D-pad button's direction.
func paintButtonDirection(b uint8) (int, int) {
	switch b {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return -1, 0
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return 1, 0
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return 0, -1
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return 0, 1
	}
	return 0, 0
}

// ── Keyboard ──────────────────────────────────────────────────────────────

// paintKey answers the keyboard. Every key is Paint's while it is on screen
// (the global F-keys and Ctrl+Q were read before).
func paintKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || virtualKeyboardActive {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	paint.config = config
	sym := e.Keysym.Sym
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	shift := e.Keysym.Mod&sdl.KMOD_SHIFT != 0
	alt := e.Keysym.Mod&sdl.KMOD_ALT != 0
	if paint.loading {
		if sym == sdl.K_ESCAPE || sym == sdl.K_BACKSPACE {
			paintCancelLoad()
		}
		return true
	}
	if paint.sheet != paintSheetNone || paint.view == paintViewStart {
		// Ctrl+Shift+S is main.go's Shorts feed, run after the scene's keys:
		// only a plain Ctrl+S saves here.
		if b, ok := paintKeyButton(sym, ctrl && !shift && e.Repeat == 0); ok {
			if paint.sheet != paintSheetNone {
				paintSheetButton(b, config)
			} else {
				paintStartButton(b, config)
			}
		} else if paint.sheet == paintSheetGame && sym == sdl.K_PAGEUP {
			paintGameLetterJump(-1)
		} else if paint.sheet == paintSheetGame && sym == sdl.K_PAGEDOWN {
			paintGameLetterJump(1)
		}
		return true
	}
	switch sym {
	case sdl.K_LEFT, sdl.K_RIGHT, sdl.K_UP, sdl.K_DOWN:
		// The OS's own repeat is ignored: paintTick repeats a held arrow
		// with the pad's acceleration.
		if e.Repeat != 0 {
			return true
		}
		dx, dy := 0, 0
		switch sym {
		case sdl.K_LEFT:
			dx = -1
		case sdl.K_RIGHT:
			dx = 1
		case sdl.K_UP:
			dy = -1
		case sdl.K_DOWN:
			dy = 1
		}
		if shift {
			dx, dy = dx*8, dy*8
		}
		paintMoveCursorTo(paint.curX+dx, paint.curY+dy)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		// Alt+Enter is the global fullscreen toggle (main.go).
		if e.Repeat == 0 && !alt {
			paintPress(paintSrcPad)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		paintCancel()
	case sdl.K_t, sdl.K_x:
		paintOpenSheet(paintSheetTools)
	case sdl.K_c:
		paintOpenSheet(paintSheetColors)
	case sdl.K_y:
		if ctrl {
			paintRedo()
		} else {
			paintOpenSheet(paintSheetColors)
		}
	case sdl.K_z:
		if ctrl && shift {
			paintRedo()
		} else {
			paintUndo()
		}
	case sdl.K_s:
		// Ctrl+S only: Ctrl+Shift+S opens the Shorts feed after any scene's
		// keys (main.go), so Save as stays in the File menu (M). A held
		// Ctrl+S does not save again on every repeat.
		if ctrl && !shift && e.Repeat == 0 {
			paintSave()
		}
	case sdl.K_m:
		paintOpenSheet(paintSheetFile)
	case sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS:
		paintZoomBy(1)
	case sdl.K_MINUS, sdl.K_KP_MINUS:
		paintZoomBy(-1)
	case sdl.K_0, sdl.K_KP_0:
		paintZoomFit()
	case sdl.K_LEFTBRACKET:
		paintStepSize(-1)
	case sdl.K_RIGHTBRACKET:
		paintStepSize(1)
	case sdl.K_g:
		paintToggleGrid()
	case sdl.K_1, sdl.K_2, sdl.K_3, sdl.K_4, sdl.K_5, sdl.K_6, sdl.K_7, sdl.K_8:
		paintSetTool(paintTool(sym - sdl.K_1))
		PlayNavSound()
	}
	return true
}

// paintKeyButton is a key as the pad button it stands for in the sheets and
// on the start screen.
func paintKeyButton(sym sdl.Keycode, ctrl bool) (uint8, bool) {
	switch sym {
	case sdl.K_UP:
		return sdl.CONTROLLER_BUTTON_DPAD_UP, true
	case sdl.K_DOWN:
		return sdl.CONTROLLER_BUTTON_DPAD_DOWN, true
	case sdl.K_LEFT:
		return sdl.CONTROLLER_BUTTON_DPAD_LEFT, true
	case sdl.K_RIGHT:
		return sdl.CONTROLLER_BUTTON_DPAD_RIGHT, true
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		return sdl.CONTROLLER_BUTTON_A, true
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		return sdl.CONTROLLER_BUTTON_B, true
	case sdl.K_f:
		// The game list's Find (the pad's Y there).
		if paint.sheet == paintSheetGame {
			return sdl.CONTROLLER_BUTTON_Y, true
		}
	case sdl.K_s:
		if ctrl && paint.sheet == paintSheetFile {
			paintCloseSheet()
			paintSave()
		}
	}
	return 0, false
}

// ── Mouse and touch ───────────────────────────────────────────────────────

// paintPointer draws with the mouse or a finger on the canvas: kind 0 is the
// press, 1 a move while held, 2 the release. It reports whether the canvas
// took it (a press off the canvas is left to paintClick).
func paintPointer(kind int, mx, my int32) bool {
	d := paint.doc
	if paint.view != paintViewCanvas || d == nil || paint.sheet != paintSheetNone || paint.loading {
		return false
	}
	switch kind {
	case 0:
		if !pointInRect(mx, my, paint.viewport) {
			return false
		}
		x, y := paintScreenToCanvas(mx, my)
		if !d.in(x, y) {
			return true
		}
		paintEndStroke()
		paintMoveCursorTo(x, y)
		paintPress(paintSrcMouse)
		return true
	case 1:
		if !paint.drawing || paint.drawSrc != paintSrcMouse {
			return false
		}
		x, y := paintScreenToCanvas(mx, my)
		paintMoveCursorTo(x, y)
		return true
	case 2:
		if !paint.drawing || paint.drawSrc != paintSrcMouse {
			return false
		}
		if paint.shapeOn {
			// A dragged shape ends where the button is let go.
			x, y := paintScreenToCanvas(mx, my)
			paintMoveCursorTo(x, y)
			paintPress(paintSrcMouse)
			return true
		}
		paintCommit()
		return true
	}
	return false
}

// paintWheel zooms the canvas, or scrolls the open list.
func paintWheel(y int32) bool {
	if y == 0 {
		return true
	}
	switch {
	case paint.sheet != paintSheetNone:
		b := uint8(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
		if y > 0 {
			b = sdl.CONTROLLER_BUTTON_DPAD_UP
		}
		if paint.sheet == paintSheetColors {
			return true
		}
		paintSheetButton(b, paint.config)
	case paint.view == paintViewCanvas && paint.doc != nil:
		steps := 1
		if y < 0 {
			steps = -1
		}
		paintZoomBy(steps)
	}
	return true
}

// paintClick answers a click on Paint's own controls: the start screen's
// rows, the tool strip and the sheets. The canvas itself is paintPointer's.
func paintClick(mx, my int32, config *Config) bool {
	paint.config = config
	if paint.loading {
		return true
	}
	for i := len(paint.hits) - 1; i >= 0; i-- {
		h := paint.hits[i]
		if !pointInRect(mx, my, h.r) {
			continue
		}
		paintHitActivate(h, config)
		return true
	}
	if paint.sheet != paintSheetNone {
		// A click beside the sheet closes it.
		paintCloseSheet()
		return true
	}
	return false
}

// paintHitActivate runs what a clicked target stands for.
func paintHitActivate(h paintHit, config *Config) {
	switch h.id {
	case "start":
		if rows := paintStartRows(); h.index >= 0 && h.index < len(rows) {
			paint.startSel = rows[h.index].id
			paintStartButton(sdl.CONTROLLER_BUTTON_A, config)
		}
	case "row":
		paint.sheetCur = h.index
		paintSheetButton(sdl.CONTROLLER_BUTTON_A, config)
	case "tool":
		paintOpenSheet(paintSheetTools)
	case "size":
		// The size chip cycles through the sizes.
		paint.sizeIdx = (paint.sizeIdx + 1) % len(paintBrushSizes)
		PlayNavSound()
	case "color":
		paintOpenSheet(paintSheetColors)
	case "undo":
		paintUndo()
	case "redo":
		paintRedo()
	case "zoomin":
		paintZoomBy(1)
	case "zoomout":
		paintZoomBy(-1)
	case "file":
		paintOpenSheet(paintSheetFile)
	}
}

// ── Scene registration ────────────────────────────────────────────────────

type paintScene struct{ sceneBase }

func (paintScene) render(r *sdl.Renderer, c *Config) bool {
	renderPaint(r, c)
	return true
}

func (paintScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return paintKey(e, c) }

func (paintScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return paintController(e, c)
}

func init() { registerScene(paintScene{sceneBase{name: paintSceneName}}) }
