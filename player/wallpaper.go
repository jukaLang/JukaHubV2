package main

import (
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // the preview decodes the pictures the picker lists
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Wallpaper Picker — browse and set custom wallpapers
//
// The picker draws the live backdrop behind itself, so the wallpaper in use —
// or an animated one just picked (gifwall.go) — is what it sits on. A pick is
// remembered in the user config (wallpaperApply); the old picker only set the
// in-memory value, and the shipped config's "background.jpg" took over again
// at the next start.
// ──────────────────────────────────────────────────────────────────────

type wallpaperEntry struct {
	name   string
	path   string
	folder bool // a folder to open
	parent bool // the "up a folder" row
	clear  bool // the "None" row: back to the glass backdrop
	gif    bool // an animated wallpaper (gifwall.go)
}

var (
	wpEntries []wallpaperEntry
	wpCursor  int
	wpDir     string
	wpOpen    bool
	wpPreview string // the wallpaper last set from the picker
	wpScrollY int
)

// wallpaperNoneLabel is the first row: no picture, Home's own backdrop.
const wallpaperNoneLabel = "None — the glass backdrop"

// wallpaperPage is how many rows L1/R1 (PgUp/PgDn on a keyboard) move.
const wallpaperPage = 5

// wallpaperExts are the image types the wallpaper picker lists. A .gif plays
// (gifwall.go).
var wallpaperExts = []string{".jpg", ".jpeg", ".png", ".bmp", ".gif"}

func initWallpaperPicker() {
	dir := wallpaperStartDir(appConfig)
	// An absolute folder always has a way up, even when the picker starts in
	// the working directory.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	wpDir = dir
	scanWallpaperDir()
	wallpaperSelectCurrent(appConfig)
}

// wallpaperStartDirs are the folders the picker may open in, best first: the
// folder of the wallpaper in use (changing it starts next to it), then the
// usual wallpaper and picture folders. On the handheld the card's own
// Wallpapers, PhoneDrop, Pictures and Screenshots folders come before the home
// folder, which is the app's own folder there, and the card's root is the last
// stop. (The card's Imgs folder holds box art, not wallpapers, so it is not
// one.)
func wallpaperStartDirs(config *Config) []string {
	var dirs []string
	if config != nil && !appWallpaperIsShippedDefault(config.Variables.BackgroundImage) {
		dirs = append(dirs, filepath.Dir(resolvePath(config.Variables.BackgroundImage)))
	}
	// A picture sent from a phone (Phone Companion → Send a file) lands in
	// PhoneDrop, so the picker opens there once something has arrived — the
	// easiest way to get a GIF onto the handheld.
	phoneDrop := ""
	if root := ftpRootDir(); root != "" {
		phoneDrop = filepath.Join(root, companionDropDir)
	}
	if P().IsHandheld() {
		dirs = append(dirs, "/mnt/SDCARD/Wallpapers")
		if data, err := P().DataDir(); err == nil && data != "" {
			dirs = append(dirs, filepath.Join(data, "wallpapers"))
		}
		if phoneDrop != "" {
			dirs = append(dirs, phoneDrop)
			phoneDrop = ""
		}
		dirs = append(dirs, "/mnt/SDCARD/Pictures", "/mnt/SDCARD/Screenshots")
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, "Pictures", "Wallpapers"),
			filepath.Join(home, "Pictures", "Backgrounds"),
			filepath.Join(home, "Pictures"),
			filepath.Join(home, "Downloads"))
	}
	// On a PC the user's own picture folders come first.
	if phoneDrop != "" {
		dirs = append(dirs, phoneDrop)
	}
	if P().IsHandheld() {
		dirs = append(dirs, "/mnt/SDCARD")
	}
	return append(dirs, ".")
}

// wallpaperStartDir is the first of wallpaperStartDirs that exists.
func wallpaperStartDir(config *Config) string {
	for _, d := range wallpaperStartDirs(config) {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
	}
	return "."
}

// wallpaperNameLess orders names the way a person reads a folder: letters
// before case, so "apple" sits with "Apple" instead of after "Zebra".
func wallpaperNameLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// listPickerEntries returns the visible subfolders of dir followed by the
// files whose extension is in exts, each group in reading order. Hidden
// folders and files are skipped. What a row needs to know about itself (a
// folder, a GIF) is settled here, once, not on every frame it is drawn.
func listPickerEntries(dir string, exts []string) ([]wallpaperEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var folders, files []wallpaperEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			folders = append(folders, wallpaperEntry{name: e.Name(), path: filepath.Join(dir, e.Name()), folder: true})
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		for _, want := range exts {
			if ext == want {
				files = append(files, wallpaperEntry{name: e.Name(), path: filepath.Join(dir, e.Name()), gif: ext == ".gif"})
				break
			}
		}
	}
	sort.SliceStable(folders, func(i, j int) bool { return wallpaperNameLess(folders[i].name, folders[j].name) })
	sort.SliceStable(files, func(i, j int) bool { return wallpaperNameLess(files[i].name, files[j].name) })
	return append(folders, files...), nil
}

// scanWallpaperDir lists wpDir. The None row and the way up are always there,
// so a folder that cannot be read is never a dead end.
func scanWallpaperDir() {
	wpEntries = []wallpaperEntry{{name: wallpaperNoneLabel, clear: true}}
	if parent := filepath.Dir(wpDir); parent != wpDir {
		wpEntries = append(wpEntries, wallpaperEntry{name: "Up a folder", path: parent, parent: true})
	}
	if listed, err := listPickerEntries(wpDir, wallpaperExts); err == nil {
		wpEntries = append(wpEntries, listed...)
	}
	wpCursor = 0
	wpScrollY = 0
}

// wallpaperOpenDir lists dir and puts the cursor on the row for selectPath
// (the folder just left, when going up), or on the first row.
func wallpaperOpenDir(dir, selectPath string) {
	wpDir = dir
	scanWallpaperDir()
	if selectPath == "" {
		return
	}
	want := filepath.Clean(selectPath)
	for i, e := range wpEntries {
		if !e.parent && !e.clear && filepath.Clean(e.path) == want {
			wpCursor = i
			return
		}
	}
}

// wallpaperSelectCurrent puts the cursor on the wallpaper in use when it is in
// the listed folder — or on the None row when there is no wallpaper.
func wallpaperSelectCurrent(config *Config) {
	cur := wallpaperCurrent(config)
	if cur == "" {
		return
	}
	for i, e := range wpEntries {
		if !e.folder && !e.parent && !e.clear && filepath.Clean(e.path) == cur {
			wpCursor = i
			return
		}
	}
}

// wpCur caches the wallpaper in use as a clean absolute path, keyed by the
// configured value: a relative path costs file checks to resolve, and the
// list compares every visible row against it on every frame.
var wpCur struct {
	key   string
	clean string
}

// wallpaperCurrent is the wallpaper in use as a clean path, or "" for none.
func wallpaperCurrent(config *Config) string {
	if config == nil || appWallpaperIsShippedDefault(config.Variables.BackgroundImage) {
		return ""
	}
	bg := config.Variables.BackgroundImage
	if bg != wpCur.key || wpCur.clean == "" {
		wpCur.key, wpCur.clean = bg, filepath.Clean(resolvePath(bg))
	}
	return wpCur.clean
}

// wallpaperApply sets — or, with an empty path, clears — the wallpaper and
// remembers the choice in the user config. The keyboard, the pad and the mouse
// all land here, so a pick is kept the same way however it was made. It
// reports whether the wallpaper changed: a GIF too large to animate on this
// screen is refused here, while the picker is still open to pick another,
// rather than warned about after it closed.
func wallpaperApply(config *Config, path string) bool {
	if config == nil {
		return false
	}
	path = strings.TrimSpace(path)
	if path != "" && gifWallpaperPath(path) {
		if reason := gifWallTooLarge(path, int(screenWidth), int(screenHeight)); reason != "" {
			ShowToast(reason, ToastKindWarning)
			return false
		}
		// Picking a GIF that could not be played (or that memory pressure
		// left at its first frame) tries it again at once.
		gifWallRetryPick(path)
	}
	value := path
	if path == "" {
		config.Variables.BackgroundImage = xpackDefaultWallpaper
		value = wallpaperNoneValue
	} else {
		config.Variables.BackgroundImage = path
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	// The pick owns the wallpaper now. An experience pack's remembered
	// wallpaper would otherwise come back over it at the next start
	// (xpackEnsure), and the restore yields to a pack that has one.
	delete(config.Variables.Custom, xpackWallpaperKey)
	setStoreString(config, wallpaperSavedKey, value)
	wallpaperRestoreRemember(config)
	wpPreview = path
	xpackDropDynamicWallpaper()
	switch {
	case path == "":
		ShowToast("Wallpaper off — the glass backdrop is back", ToastKindSuccess)
	case gifWallpaperPath(path) && gifWallHoldStill(config):
		// Say why the picture does not move, and where that is changed.
		ShowToast("Wallpaper set: "+filepath.Base(path)+" — "+gifWallStillReason(config), ToastKindSuccess)
	case gifWallpaperPath(path):
		ShowToast("Animated wallpaper set: "+filepath.Base(path), ToastKindSuccess)
	default:
		ShowToast("Wallpaper set: "+filepath.Base(path), ToastKindSuccess)
	}
	return true
}

// wallpaperClose puts the picker away and frees its preview: the texture is
// a picture's worth of memory nothing else will draw.
func wallpaperClose() {
	wpOpen = false
	if wpThumb.tex != nil && wpThumb.owner != nil {
		_ = wpThumb.tex.Destroy()
	}
	wpThumb.tex, wpThumb.path, wpThumb.info = nil, "", ""
	wpThumb.w, wpThumb.h = 0, 0
	wpRows = wpRows[:0]
}

// wallpaperActivate runs the highlighted row: a folder opens, the way up goes
// up, the None row clears the wallpaper, a picture becomes it.
func wallpaperActivate(config *Config) {
	if wpCursor < 0 || wpCursor >= len(wpEntries) {
		return
	}
	e := wpEntries[wpCursor]
	switch {
	case e.parent:
		wallpaperUp()
		return
	case e.folder:
		wallpaperOpenDir(e.path, "")
	case e.clear:
		wallpaperApply(config, "")
		wallpaperClose()
	default:
		if !wallpaperApply(config, e.path) {
			PlayErrorSound()
			return
		}
		wallpaperClose()
	}
	PlayActivateSound()
}

// wallpaperRight is Right on the list: it opens the highlighted folder (and
// goes up from the way-up row); on a picture or the None row it does nothing.
func wallpaperRight(config *Config) {
	if wpCursor < 0 || wpCursor >= len(wpEntries) {
		return
	}
	if e := wpEntries[wpCursor]; e.folder || e.parent {
		wallpaperActivate(config)
	}
}

// wallpaperMove steps the highlight by delta rows, stopping at either end.
func wallpaperMove(delta int) {
	if len(wpEntries) == 0 {
		return
	}
	next := min(max(wpCursor+delta, 0), len(wpEntries)-1)
	if next != wpCursor {
		wpCursor = next
		PlayNavSound()
	}
}

// wallpaperUp opens the parent folder with the folder just left highlighted,
// so going down and back up lands where it started.
func wallpaperUp() {
	if parent := filepath.Dir(wpDir); parent != wpDir {
		wallpaperOpenDir(parent, wpDir)
		PlayBackSound()
	}
}

// ── The pointer ───────────────────────────────────────────────────────────

// wpRows are the list rows drawn this frame and the entry each one shows, so
// a click lands on what is on screen.
var wpRows []wallpaperRowHit

type wallpaperRowHit struct {
	rect  sdl.Rect
	index int
}

// wallpaperOwnsPointer reports whether the picker takes the mouse: it covers
// the screen, so nothing behind it may hear a press — unless a card drawn over
// the picker is up, which then gets the pointer as it would anywhere.
func wallpaperOwnsPointer() bool {
	return wpOpen && !companion.open && !updatePrompt.open && !shortcutsOpen &&
		!controlsLegend.open && !clipSearchOpen && !globalSearchOpen && !virtualKeyboardActive &&
		!onboarding.active && !launchFailure.open && !recoveryCard.open && !qrZoomActive()
}

// wallpaperRowAt is the entry under the pointer, or -1.
func wallpaperRowAt(mx, my int32) int {
	for _, r := range wpRows {
		if pointInRect(mx, my, r.rect) {
			return r.index
		}
	}
	return -1
}

// wallpaperMouseDown is a left press on the picker: a row is highlighted by
// the first click and run by the second (or by a double click); anything else
// on the picker does nothing.
func wallpaperMouseDown(mx, my int32, clicks uint8, config *Config) {
	i := wallpaperRowAt(mx, my)
	if i < 0 || i >= len(wpEntries) {
		return
	}
	if i == wpCursor || clicks >= 2 {
		wpCursor = i
		wallpaperActivate(config)
		return
	}
	wpCursor = i
	PlayNavSound()
}

// wallpaperMouseMove highlights the row under a moving pointer, the way a
// pointer hovers Home's tiles.
func wallpaperMouseMove(mx, my int32) {
	if i := wallpaperRowAt(mx, my); i >= 0 && i < len(wpEntries) && i != wpCursor {
		wpCursor = i
	}
}

// wallpaperWheel scrolls the list: three rows a notch.
func wallpaperWheel(dy int32) {
	switch {
	case dy > 0:
		wallpaperMove(-3)
	case dy < 0:
		wallpaperMove(3)
	}
}

// ── The preview ───────────────────────────────────────────────────────────

// wpThumb is the preview of the highlighted picture. The decode runs on a
// goroutine — a phone photo is tens of megabytes of pixels — and only for the
// newest highlight, one decode at a time. busySince lets a new decode start
// when the last one is stuck (a picture on a network drive that stopped
// answering): its late answer is for another path and is dropped.
var wpThumb struct {
	path      string // the picture the texture shows
	tex       *sdl.Texture
	owner     *sdl.Renderer
	w, h      int32
	info      string
	busy      bool // a decode is running
	busySince time.Time

	mu     sync.Mutex
	result *wpThumbResult
}

// wpThumbStuck is how long a preview decode may run before the next highlight
// gets one of its own.
const wpThumbStuck = 10 * time.Second

// wpThumbScanBytes / wpThumbScanFrames bound the walk the preview makes to
// count a GIF's frames: enough to tell a loop from a film without reading a
// 48 MB file to draw one frame.
const (
	wpThumbScanBytes  = 8 << 20
	wpThumbScanFrames = 240
)

// wpThumbResult is a finished preview decode.
type wpThumbResult struct {
	path string
	pix  []byte
	w, h int
	info string
}

// wpThumbMaxPixels bounds the pictures the preview decodes.
func wpThumbMaxPixels() int {
	if P().IsHandheld() {
		return gifWallMaxCanvasHandheld
	}
	return gifWallMaxCanvasDesktop
}

// wpThumbFit is the largest w×h with the source's aspect inside maxW×maxH,
// never larger than the source.
func wpThumbFit(sw, sh, maxW, maxH int) (int, int) {
	if sw <= 0 || sh <= 0 || maxW <= 0 || maxH <= 0 {
		return 0, 0
	}
	w, h := sw, sh
	if w > maxW {
		w, h = maxW, max(1, sh*maxW/sw)
	}
	if h > maxH {
		w, h = max(1, sw*maxH/sh), maxH
	}
	return w, h
}

// wpThumbFrames is the frame count a capped walk can vouch for: "N frames",
// or "N+ frames" when the walk stopped before the end of the file.
func wpThumbFrames(scan *gifScan) string {
	if scan.truncated {
		return fmt.Sprintf("%d+ frames", len(scan.frames))
	}
	return gifWallFrameWord(len(scan.frames))
}

// wpThumbDecode builds a preview of path at most maxW×maxH, with the line of
// facts shown under it. canvasW×canvasH is the screen a GIF would play on
// (read on the render thread and handed in). It runs on its own goroutine.
func wpThumbDecode(path string, maxW, maxH, canvasW, canvasH int) *wpThumbResult {
	res := &wpThumbResult{path: path}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		// Not a plain file (a pipe would block the decode for good).
		res.info = "Cannot be opened"
		return res
	}
	size := fmt.Sprintf("%.1f MB", float64(info.Size())/(1<<20))
	file, err := os.Open(path)
	if err != nil {
		res.info = "Cannot be opened"
		return res
	}
	defer file.Close()
	var rgba []byte
	var sw, sh int
	if gifWallpaperPath(path) {
		scan, err := gifWallScan(file, wpThumbScanBytes, wpThumbScanFrames)
		if err != nil {
			res.info = "Not a GIF this player can read"
			return res
		}
		sw, sh = gifWallSourceSize(scan)
		kind := "Animated GIF · " + wpThumbFrames(scan)
		if len(scan.frames) == 1 && !scan.truncated {
			kind = "GIF · still"
		}
		res.info = fmt.Sprintf("%s · %d × %d · %s", kind, sw, sh, size)
		lim := gifWallLimitsNow()
		if plan := gifWallMakePlan(sw, sh, canvasW, canvasH, 1, lim.budget); lim.canvas > 0 && plan.crop.Dx()*plan.crop.Dy() > lim.canvas {
			res.info += " · too large to animate here"
		}
		if sw <= 0 || sh <= 0 || sw*sh > wpThumbMaxPixels() {
			return res
		}
		// The frame is bounded on its own as well: its descriptor may name a
		// size the screen never had.
		p, _, err := gifWallDecodeFrame(file, gifWallFrameHeader(scan), scan.frames[0], nil, 2*wpThumbMaxPixels())
		if err != nil {
			return res
		}
		var pal [256][4]byte
		gifWallPalette(p.Palette, &pal)
		canvas := newGifWallCanvas(image.Rect(0, 0, sw, sh), [4]byte{0, 0, 0, 255})
		canvas.draw(p, &pal)
		rgba = canvas.pix
	} else {
		cfg, _, err := image.DecodeConfig(file)
		if err != nil {
			res.info = "No preview for this file · " + size
			return res
		}
		sw, sh = cfg.Width, cfg.Height
		res.info = fmt.Sprintf("%d × %d · %s", sw, sh, size)
		if sw <= 0 || sh <= 0 || sw*sh > wpThumbMaxPixels() {
			return res
		}
		if _, err := file.Seek(0, 0); err != nil {
			return res
		}
		img, _, err := image.Decode(file)
		if err != nil {
			return res
		}
		b := img.Bounds()
		dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(dst, dst.Rect, img, b.Min, draw.Src)
		rgba, sw, sh = dst.Pix, b.Dx(), b.Dy()
	}
	w, h := wpThumbFit(sw, sh, maxW, maxH)
	if w == 0 || len(rgba) < sw*sh*4 {
		return res
	}
	res.pix = make([]byte, w*h*4)
	gifWallScale(rgba, sw, sh, res.pix, w, h)
	res.w, res.h = w, h
	return res
}

// gifWallFrameWord is "1 frame" / "N frames".
func gifWallFrameWord(n int) string {
	if n == 1 {
		return "1 frame"
	}
	return fmt.Sprintf("%d frames", n)
}

// wpThumbUpdate keeps the preview on the highlighted picture: it collects a
// finished decode, uploads it when it is still the one highlighted, and asks
// for the next one when the highlight moved on.
func wpThumbUpdate(renderer *sdl.Renderer, want string, maxW, maxH int) {
	if wpThumb.owner != renderer {
		// The previous renderer freed its textures itself.
		wpThumb.tex, wpThumb.path, wpThumb.owner = nil, "", renderer
	}
	wpThumb.mu.Lock()
	res := wpThumb.result
	wpThumb.result = nil
	wpThumb.mu.Unlock()
	if res != nil {
		wpThumb.busy = false
		if res.path == want {
			if wpThumb.tex != nil {
				_ = wpThumb.tex.Destroy()
				wpThumb.tex = nil
			}
			wpThumb.path, wpThumb.info = res.path, res.info
			wpThumb.w, wpThumb.h = int32(res.w), int32(res.h)
			if len(res.pix) > 0 {
				wpThumb.tex = gifWallTexture(renderer, res.pix, res.w, res.h, false)
			}
		}
	}
	if want == "" || want == wpThumb.path {
		return
	}
	if wpThumb.busy && time.Since(wpThumb.busySince) < wpThumbStuck {
		return
	}
	wpThumb.busy, wpThumb.busySince = true, time.Now()
	canvasW, canvasH := int(screenWidth), int(screenHeight)
	go func(path string) {
		r := wpThumbDecode(path, maxW, maxH, canvasW, canvasH)
		wpThumb.mu.Lock()
		wpThumb.result = r
		wpThumb.mu.Unlock()
	}(want)
}

// ── Drawing ───────────────────────────────────────────────────────────────

// wallpaperFitTail fits text into maxW pixels by cutting its start: the end of
// a path is the part that says where you are.
func wallpaperFitTail(font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return text
	}
	if w, _ := measureText(font, text); w <= maxW {
		return text
	}
	runes := []rune(text)
	lo, hi := 0, len(runes) // the fewest leading runes to drop
	for lo < hi {
		mid := (lo + hi) / 2
		if w, _ := measureText(font, "…"+string(runes[mid:])); w <= maxW {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return "…" + string(runes[lo:])
}

// wpText caches the picker's measured strings by what they depend on, so an
// open picker lays out the same words once rather than on every frame.
var wpText struct {
	dir       string
	dirFont   *ttf.Font
	dirW      int32
	dirFitted string

	noteFont  *ttf.Font
	noteW     int32
	noteLines []string
}

// wallpaperHeaderDir is the folder as the header shows it.
func wallpaperHeaderDir(font *ttf.Font, maxW int32) string {
	if wpText.dir != wpDir || wpText.dirFont != font || wpText.dirW != maxW || wpText.dirFitted == "" {
		wpText.dir, wpText.dirFont, wpText.dirW = wpDir, font, maxW
		wpText.dirFitted = wallpaperFitTail(font, wpDir, maxW)
	}
	return wpText.dirFitted
}

// wallpaperGifNote is the line under a GIF's preview, wrapped once per width.
func wallpaperGifNote(font *ttf.Font, maxW int32) []string {
	if wpText.noteFont != font || wpText.noteW != maxW || wpText.noteLines == nil {
		wpText.noteFont, wpText.noteW = font, maxW
		wpText.noteLines = wrapTextLines(font, "Plays on every screen; Animate wallpaper off or Battery Saver holds its first frame", maxW, 2)
	}
	return wpText.noteLines
}

// wallpaperRowLabel is what a row reads.
func wallpaperRowLabel(e wallpaperEntry) string {
	return e.name
}

// wallpaperInUse reports whether entry e is the wallpaper on screen; cur is
// wallpaperCurrent's answer for this frame.
func wallpaperInUse(e wallpaperEntry, cur string) bool {
	if e.clear {
		return cur == ""
	}
	if e.folder || e.parent || cur == "" {
		return false
	}
	return filepath.Clean(e.path) == cur
}

// wallpaperActionLabel is the A hint for the highlighted row.
func wallpaperActionLabel() string {
	if wpCursor < 0 || wpCursor >= len(wpEntries) {
		return "Select"
	}
	e := wpEntries[wpCursor]
	switch {
	case e.folder:
		return "Open"
	case e.parent:
		return "Up"
	case e.clear:
		return "Use none"
	}
	return "Set wallpaper"
}

func renderWallpaperPicker(renderer *sdl.Renderer, config *Config) {
	if !wpOpen || renderer == nil || config == nil {
		return
	}
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	wpRows = wpRows[:0]

	bodyFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if bodyFont == nil {
		bodyFont = smallFont
	}

	margin := scaleX(homeSideMargin, screenWidth)
	// The folder is the subtitle; a deep path keeps its end, the part that
	// says where you are.
	where := wallpaperHeaderDir(smallFont, screenWidth-2*margin-96)
	header := UIPanelHeader("Wallpaper", where, "image")
	header.X, header.Y, header.W = margin, scaleY(18, screenHeight), screenWidth-2*margin
	contentY := UIPageHeaderDraw(renderer, config, header) + 6
	band := sceneFooterBand()
	bottom := band.Y - 14
	if bottom-contentY < 120 {
		return
	}
	gap := scaleX(16, screenWidth)
	listW := (screenWidth - 2*margin - gap) * 55 / 100
	listRect := sdl.Rect{X: margin, Y: contentY, W: listW, H: bottom - contentY}
	previewRect := sdl.Rect{X: margin + listW + gap, Y: contentY, W: screenWidth - 2*margin - listW - gap, H: bottom - contentY}
	cur := wallpaperCurrent(config)

	// ── The list ──
	drawOverlayCard(renderer, listRect)
	const pad = int32(12)
	rowH := int32(44)
	if bodyFont != nil {
		rowH = max(rowH, int32(bodyFont.Height())+18)
	}
	inner := sdl.Rect{X: listRect.X + pad, Y: listRect.Y + pad, W: listRect.W - 2*pad, H: listRect.H - 2*pad}
	maxVisible := max(int(inner.H/(rowH+4)), 1)
	if len(wpEntries) == 0 {
		wpCursor = 0
	} else if wpCursor >= len(wpEntries) {
		wpCursor = len(wpEntries) - 1
	}
	if wpCursor < wpScrollY {
		wpScrollY = wpCursor
	}
	if wpCursor >= wpScrollY+maxVisible {
		wpScrollY = wpCursor - maxVisible + 1
	}
	for i := wpScrollY; i < len(wpEntries) && i < wpScrollY+maxVisible; i++ {
		e := wpEntries[i]
		row := sdl.Rect{X: inner.X, Y: inner.Y + int32(i-wpScrollY)*(rowH+4), W: inner.W, H: rowH}
		wpRows = append(wpRows, wallpaperRowHit{rect: row, index: i})
		focused := i == wpCursor
		UIListRow(renderer, row, glassRowRadius, focused, false)
		icon := "image"
		switch {
		case e.folder:
			icon = "folder"
		case e.parent:
			icon = "folder-up"
		case e.clear:
			icon = "palette"
		}
		iconBox := sdl.Rect{X: row.X + 10, Y: row.Y + (row.H-22)/2, W: 22, H: 22}
		DrawHomeIcon(renderer, icon, iconBox, false)
		textX := iconBox.X + iconBox.W + 12
		tagW := int32(0)
		if smallFont != nil {
			tag := ""
			switch {
			case wallpaperInUse(e, cur):
				tag = "In use"
			case e.gif:
				tag = "GIF"
			}
			if tag != "" {
				tw, th := measureText(smallFont, tag)
				chip := sdl.Rect{X: row.X + row.W - tw - 30, Y: row.Y + (row.H-th-6)/2, W: tw + 16, H: th + 6}
				_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
				fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, WithAlpha(glassAccent(), 60))
				drawText(renderer, smallFont, tag, chip.X+8, chip.Y+3, ColorTextPrimary(), textAlignLeft)
				tagW = chip.W + 12
			}
		}
		if bodyFont != nil {
			ink := ColorTextPrimary()
			if focused {
				ink = glassAccent()
			}
			label := fitTextCached(bodyFont, wallpaperRowLabel(e), row.X+row.W-textX-tagW-8)
			drawText(renderer, bodyFont, label, textX, row.Y+(row.H-int32(bodyFont.Height()))/2, ink, textAlignLeft)
		}
	}

	// ── The preview ──
	drawOverlayCard(renderer, previewRect)
	pw := previewRect.W - 2*20
	ph := previewRect.H * 58 / 100
	want := ""
	var sel wallpaperEntry
	if wpCursor >= 0 && wpCursor < len(wpEntries) {
		sel = wpEntries[wpCursor]
		if !sel.folder && !sel.parent && !sel.clear {
			want = sel.path
		}
	}
	wpThumbUpdate(renderer, want, int(pw), int(ph))
	box := sdl.Rect{X: previewRect.X + 20, Y: previewRect.Y + 20, W: pw, H: ph}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 12, glassA(10))
	if want != "" && wpThumb.path == want && wpThumb.tex != nil && wpThumb.w > 0 && wpThumb.h > 0 {
		dw, dh := wpThumbFit(int(wpThumb.w), int(wpThumb.h), int(box.W), int(box.H))
		_ = renderer.Copy(wpThumb.tex, nil, &sdl.Rect{
			X: box.X + (box.W-int32(dw))/2, Y: box.Y + (box.H-int32(dh))/2, W: int32(dw), H: int32(dh),
		})
	} else if smallFont != nil {
		hint := ""
		switch {
		case sel.clear:
			hint = "Home's colour blooms, no picture"
		case sel.folder:
			hint = "A folder"
		case sel.parent:
			hint = "The folder above"
		case want != "" && wpThumb.path == want:
			hint = "No preview for this picture"
		case want != "":
			hint = "Loading the preview…"
		}
		if hint != "" {
			hw, _ := measureText(smallFont, hint)
			drawText(renderer, smallFont, hint, box.X+(box.W-hw)/2, box.Y+box.H/2-int32(smallFont.Height())/2, ColorTextTertiary(), textAlignLeft)
		}
	}
	y := box.Y + box.H + 14
	textW := previewRect.W - 40
	if bodyFont != nil && wpCursor >= 0 && wpCursor < len(wpEntries) {
		title := fitTextCached(bodyFont, wallpaperRowLabel(sel), textW)
		if sel.parent {
			title = fitTextCached(bodyFont, filepath.Base(sel.path), textW)
		}
		drawText(renderer, bodyFont, title, previewRect.X+20, y, ColorTextPrimary(), textAlignLeft)
		y += int32(bodyFont.Height()) + 6
	}
	if smallFont != nil {
		line := ""
		switch {
		case want != "" && wpThumb.path == want:
			line = wpThumb.info
		case sel.clear:
			line = "The wallpaper Home ships with"
		case sel.folder || sel.parent:
			line = "Open it to see its pictures"
		}
		if line != "" {
			drawText(renderer, smallFont, fitTextCached(smallFont, line, textW), previewRect.X+20, y, ColorTextSecondary(), textAlignLeft)
			y += int32(smallFont.Height()) + 4
		}
		if want != "" && sel.gif {
			for _, l := range wallpaperGifNote(smallFont, textW) {
				drawText(renderer, smallFont, l, previewRect.X+20, y, ColorTextTertiary(), textAlignLeft)
				y += int32(smallFont.Height()) + 2
			}
		}
	}

	hints := wpFooterHints[:0]
	hints = append(hints,
		FooterHint{Button: "D-Pad", Label: "Move"},
		FooterHint{Button: "A", Label: wallpaperActionLabel()})
	if filepath.Dir(wpDir) != wpDir {
		hints = append(hints, FooterHint{Button: "Left", Label: "Up a folder"})
	}
	hints = append(hints,
		FooterHint{Button: "L / R", Label: "Page"},
		FooterHint{Button: "B", Label: "Back"})
	wpFooterHints = hints
	wallpaperDrawFooter(renderer, config, band, hints)
}

// wpFooterHints is the footer's hint list, reused frame to frame.
var wpFooterHints []FooterHint

// wallpaperDrawFooter paints Home's footer band with the picker's hints, laid
// out by the planner every scene footer uses.
func wallpaperDrawFooter(renderer *sdl.Renderer, config *Config, band sdl.Rect, hints []FooterHint) {
	drawGlassFooterBand(renderer, band)
	pad := scaleX(16, screenWidth)
	plan := planFooterHints(config, hints, band.W-2*pad)
	if plan.font == nil {
		return
	}
	x := band.X + band.W - pad - plan.width
	textY := band.Y + (band.H-plan.fontHeight)/2
	badgeY := band.Y + (band.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
}

func handleWallpaperInput(event *sdl.KeyboardEvent, config *Config) bool {
	if !wpOpen {
		return false
	}
	if event == nil || event.Type != sdl.KEYDOWN {
		return true
	}
	switch event.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		wallpaperClose()
		PlayBackSound()
	case sdl.K_UP:
		wallpaperMove(-1)
	case sdl.K_DOWN:
		wallpaperMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_a:
		wallpaperActivate(config)
	case sdl.K_RIGHT:
		wallpaperRight(config)
	case sdl.K_BACKSPACE, sdl.K_LEFT:
		wallpaperUp()
	case sdl.K_PAGEUP:
		wallpaperMove(-wallpaperPage)
	case sdl.K_PAGEDOWN:
		wallpaperMove(wallpaperPage)
	case sdl.K_HOME:
		wallpaperMove(-len(wpEntries))
	case sdl.K_END:
		wallpaperMove(len(wpEntries))
	}
	return true
}

// wallpaperAnimateRow is Settings → Display & theme → Animate wallpaper. On
// (the default) a GIF wallpaper plays; off holds its first frame, for anyone
// who wants the picture without the movement. Battery Saver holds it too, and
// the row says so rather than showing a switch that seems to do nothing.
func wallpaperAnimateRow() setRow {
	return setRow{
		ID: "wallpaperAnimate", Label: "Animate wallpaper", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if currentPowerMode(c) == PowerModeBatterySaver {
				return "Battery Saver holds a GIF wallpaper on its first frame"
			}
			return "Plays a GIF wallpaper; off holds its first frame"
		},
		Get: func(c *Config) bool {
			return c != nil && customBool(c.Variables.Custom, wallpaperAnimateKey, true)
		},
		Set: func(c *Config, on bool) {
			setStoreBool(c, wallpaperAnimateKey, on)
			setToast("Animate wallpaper: " + setOnOff(on))
		},
	}
}
