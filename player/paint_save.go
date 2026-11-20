package main

// paint_save.go — Paint's files: opening a picture, the autosave behind
// Continue, saving a PNG, and handing the picture to the wallpaper and to a
// game's cover.
//
// Drawings are saved in Pictures/Paint (osAppsUserDir), which the FTP server,
// the phone companion's Files card and a card reader all see. A screenshot is
// never written over: its markup is a new file, "<screenshot> markup.png",
// and any name already taken gets a number. Every write is a temporary file
// and a rename (gmetaWriteCache) — not AtomicWrite, whose .bak copy would sit
// beside every drawing in a folder the user browses.
//
// Decoding and encoding run on goroutines with a copy of the pixels, and post
// back through tbxPost: a 1280×720 PNG takes a noticeable moment on the
// handheld, and the canvas must not stall while it is written.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// paintWIPImage, paintWIPOrig and paintWIPMetaFile are the autosave: the
	// canvas, the screenshot under a markup, and what Continue needs besides.
	paintWIPImage    = "paint-wip.png"
	paintWIPOrig     = "paint-wip-orig.png"
	paintWIPMetaFile = "paint-wip.json"
	// paintCoverEdge is the longer side a cover is made at, the size the
	// library decodes covers at.
	paintCoverEdge = libraryArtDecodeMaxEdge
	// paintPicsMax is how many pictures the Open list shows, newest first.
	paintPicsMax = 200
)

// paintPicturesDir is where drawings are saved.
func paintPicturesDir() string {
	return filepath.Join(osAppsUserDir("Pictures"), "Paint")
}

// paintIOMu keeps an autosave being written from being read half-written.
var paintIOMu sync.Mutex

// paintWrites counts the file writes still running, so paintFlush can wait
// for them before JukaHub exits; paintWriteMu keeps two writes of the same
// drawing (Ctrl+S twice) from sharing gmetaWriteCache's temporary file.
var (
	paintWrites  sync.WaitGroup
	paintWriteMu sync.Mutex
)

// paintGo runs a file write on a goroutine that paintFlush waits for.
func paintGo(fn func()) {
	paintWrites.Add(1)
	go func() {
		defer paintWrites.Done()
		fn()
	}()
}

// paintWriteFile writes one of the user's pictures (a drawing, a wallpaper,
// a cover), one at a time.
func paintWriteFile(path string, data []byte) error {
	paintWriteMu.Lock()
	defer paintWriteMu.Unlock()
	return gmetaWriteCache(path, data)
}

// paintWaitWrites waits up to limit for the writes still running.
func paintWaitWrites(limit time.Duration) {
	done := make(chan struct{})
	go func() {
		paintWrites.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		logWarn("paint", "a drawing was still being written at exit")
	}
}

// paintWIPMeta is the autosave's description.
type paintWIPMeta struct {
	W         int       `json:"w"`
	H         int       `json:"h"`
	Source    string    `json:"source,omitempty"`
	Markup    bool      `json:"markup,omitempty"`
	PixelArt  bool      `json:"pixel_art,omitempty"`
	SavedPath string    `json:"saved_path,omitempty"`
	Changed   bool      `json:"changed,omitempty"`
	Tool      int       `json:"tool"`
	Size      int       `json:"size"`
	Color     string    `json:"color"`
	Grid      bool      `json:"grid,omitempty"`
	X         int       `json:"x"`
	Y         int       `json:"y"`
	SavedAt   time.Time `json:"saved_at"`
}

// paintWIP is what the start screen and the Tools hub know about the
// autosave without reading it each frame.
var paintWIP struct {
	known, exists, asking bool
	// unsaved is an autosave whose drawing was changed since its last Save:
	// replacing it from the start screen asks first (paintConfirmDiscard).
	unsaved bool
	desc    string
}

// paintWIPDesc names an autosave the way paintDocDesc names a canvas.
func paintWIPDesc(m paintWIPMeta) string {
	switch {
	case m.Markup && m.Source != "":
		return "markup of " + filepath.Base(m.Source)
	case m.PixelArt:
		return fmt.Sprintf("%d×%d drawing", m.W, m.H)
	}
	return fmt.Sprintf("%d×%d sketch", m.W, m.H)
}

// paintWIPRefresh reads the autosave's description in the background, once
// at a time.
func paintWIPRefresh() {
	if paintWIP.asking {
		return
	}
	paintWIP.asking = true
	metaPath := osAppsDataPath(paintWIPMetaFile)
	imgPath := osAppsDataPath(paintWIPImage)
	go func() {
		paintIOMu.Lock()
		m, err := paintReadWIPMeta(metaPath)
		_, imgErr := os.Stat(imgPath)
		paintIOMu.Unlock()
		tbxPost(func() {
			paintWIP.asking = false
			paintWIP.known = true
			paintWIP.exists = err == nil && imgErr == nil
			paintWIP.unsaved = paintWIP.exists && m.Changed
			if paintWIP.exists {
				paintWIP.desc = paintWIPDesc(m)
			}
		})
	}()
}

// paintReadWIPMeta reads the autosave's description.
func paintReadWIPMeta(path string) (paintWIPMeta, error) {
	var m paintWIPMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.W <= 0 || m.H <= 0 || m.W > paintMaxEdge || m.H > paintMaxEdge {
		return m, fmt.Errorf("the autosave is %d × %d", m.W, m.H)
	}
	return m, nil
}

// paintAutosave writes the canvas for Continue when it changed since the
// last autosave. now waits for the write (JukaHub is quitting); otherwise a
// goroutine writes a copy.
func paintAutosave(now bool) {
	d := paint.doc
	if d == nil || !d.autoDirty {
		return
	}
	meta := paintWIPMeta{
		W: d.w, H: d.h, Source: d.source, Markup: d.orig != nil, PixelArt: d.pixelArt,
		SavedPath: d.savedPath, Changed: d.changed, Tool: int(paint.tool), Size: paint.sizeIdx,
		Color: paintHex(paint.color), Grid: paint.grid, X: paint.curX, Y: paint.curY,
		SavedAt: time.Now(),
	}
	pix := paintCloneNRGBA(d.img)
	var orig *image.NRGBA
	if d.orig != nil && !d.origSaved {
		orig = d.orig // never drawn on: no copy needed
		d.origSaved = true
	}
	d.autoDirty = false
	paint.lastAutosave = time.Now()
	paintWIP.known, paintWIP.exists, paintWIP.desc = true, true, paintWIPDesc(meta)
	paintWIP.unsaved = meta.Changed
	write := func() {
		paintIOMu.Lock()
		defer paintIOMu.Unlock()
		if err := paintWriteWIP(pix, orig, meta); err != nil {
			logWarn("paint", "autosave failed: %v", err)
		}
	}
	if now {
		write()
		return
	}
	paintGo(write)
}

// paintKeepWIP is paintKeepUnsaved for a drawing that is only in the
// autosave (JukaHub was restarted since): when it has unsaved changes its
// PNG is copied into Pictures/Paint — where it was last saved, else under a
// new name — before the new canvas's autosave takes its place.
func paintKeepWIP() {
	metaPath := osAppsDataPath(paintWIPMetaFile)
	imgPath := osAppsDataPath(paintWIPImage)
	dir := paintPicturesDir()
	paintGo(func() {
		paintIOMu.Lock()
		defer paintIOMu.Unlock()
		m, err := paintReadWIPMeta(metaPath)
		if err != nil || !m.Changed {
			return
		}
		data, err := os.ReadFile(imgPath)
		if err != nil {
			return
		}
		path := m.SavedPath
		if path == "" || (m.Markup && strings.EqualFold(filepath.Clean(path), filepath.Clean(m.Source))) {
			name := "Drawing"
			if m.Source != "" {
				name = strings.TrimSuffix(filepath.Base(m.Source), filepath.Ext(m.Source)) + " markup"
			}
			path = paintUniquePath(dir, paintCleanName(name), "")
		}
		err = paintWriteFile(path, data)
		tbxPost(func() {
			if err != nil {
				logWarn("paint", "cannot keep the autosaved drawing: %v", err)
				return
			}
			ShowToast("Your unsaved drawing was kept as Pictures/Paint/"+filepath.Base(path), ToastKindInfo)
		})
	})
}

// paintWriteWIP writes the autosave's files. Callers hold paintIOMu.
func paintWriteWIP(pix, orig *image.NRGBA, meta paintWIPMeta) error {
	data, err := paintEncodePNG(pix, png.BestSpeed)
	if err != nil {
		return err
	}
	if err := gmetaWriteCache(osAppsDataPath(paintWIPImage), data); err != nil {
		return err
	}
	if orig != nil {
		od, err := paintEncodePNG(orig, png.BestSpeed)
		if err != nil {
			return err
		}
		if err := gmetaWriteCache(osAppsDataPath(paintWIPOrig), od); err != nil {
			return err
		}
	}
	js, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return gmetaWriteCache(osAppsDataPath(paintWIPMetaFile), js)
}

// paintContinueWIP opens the autosave.
func paintContinueWIP() {
	if paint.doc != nil {
		paint.view = paintViewCanvas
		PlayActivateSound()
		return
	}
	gen := paintBeginLoad()
	metaPath := osAppsDataPath(paintWIPMetaFile)
	imgPath := osAppsDataPath(paintWIPImage)
	origPath := osAppsDataPath(paintWIPOrig)
	go func() {
		paintIOMu.Lock()
		m, err := paintReadWIPMeta(metaPath)
		var img, orig *image.NRGBA
		if err == nil {
			img, err = paintDecodeNRGBA(imgPath, paintMaxEdge)
		}
		if err == nil && (img.Rect.Dx() != m.W || img.Rect.Dy() != m.H) {
			err = fmt.Errorf("the autosave does not match its description")
		}
		if err == nil && m.Markup {
			if o, oerr := paintDecodeNRGBA(origPath, paintMaxEdge); oerr == nil && o.Rect.Eq(img.Rect) {
				orig = o
			}
		}
		paintIOMu.Unlock()
		tbxPost(func() {
			if !paintLoadCurrent(gen) {
				return
			}
			paint.loading = false
			if err != nil {
				logWarn("paint", "cannot continue the autosave: %v", err)
				ShowToast("The autosaved drawing could not be opened", ToastKindWarning)
				paintWIP.known, paintWIP.exists, paintWIP.unsaved = true, false, false
				return
			}
			d := newPaintDoc(img, orig, m.Source, m.PixelArt)
			d.savedPath, d.changed = m.SavedPath, m.Changed
			d.origSaved = orig != nil
			paintSetDoc(d)
			paint.tool = paintTool(clampInt(m.Tool, 0, int(paintToolCount)-1))
			paint.lastDrawTool = paint.tool
			if paint.tool == paintPicker {
				paint.tool, paint.lastDrawTool = paintPencil, paintPencil
			}
			paint.sizeIdx = clampInt(m.Size, 0, len(paintBrushSizes)-1)
			if c, ok := paintParseHex(m.Color); ok {
				paint.color = c
			}
			paint.grid = m.Grid
			paint.curX, paint.curY = clampInt(m.X, 0, d.w-1), clampInt(m.Y, 0, d.h-1)
			paintClampPan(true)
			PlayActivateSound()
		})
	}()
}

// ── Opening a picture ─────────────────────────────────────────────────────

// paintBeginLoad shows the opening state and returns the load's number.
func paintBeginLoad() int {
	paint.loadGen++
	paint.loading = true
	paint.sheet = paintSheetNone
	return paint.loadGen
}

// paintLoadCurrent reports whether a finished load is still the one wanted.
func paintLoadCurrent(gen int) bool { return paint.loading && paint.loadGen == gen }

// paintCancelLoad gives up on a picture still opening.
func paintCancelLoad() {
	paint.loadGen++
	paint.loading = false
	PlayBackSound()
}

// paintOpenPicture opens a picture: one of Paint's own drawings is opened to
// draw on further (Save writes it again), anything else — a screenshot, a
// photo — as a markup whose eraser brings the picture back.
func paintOpenPicture(path string) {
	path = filepath.Clean(path)
	paintEndStroke()
	gen := paintBeginLoad()
	own := paintIsOwnDrawing(path)
	go func() {
		img, err := paintDecodeNRGBA(path, paintMaxEdge)
		tbxPost(func() {
			if !paintLoadCurrent(gen) {
				return
			}
			paint.loading = false
			if err != nil {
				ShowToast("Cannot open "+filepath.Base(path)+": "+err.Error(), ToastKindWarning)
				PlayErrorSound()
				return
			}
			small := img.Rect.Dx() <= 256 && img.Rect.Dy() <= 256
			var orig *image.NRGBA
			if !own {
				orig = paintCloneNRGBA(img)
			}
			d := newPaintDoc(img, orig, path, small)
			d.autoDirty = true
			if own {
				d.savedPath = path
			}
			paintSetDoc(d)
			paint.tool, paint.lastDrawTool = paintPencil, paintPencil
			if own {
				paint.color = paintPalette[0].c
				paint.sizeIdx = 0
			} else {
				// Markup: the ellipse is what "circle something" wants, in
				// the bright red that reads on any game.
				paint.tool, paint.lastDrawTool = paintEllipse, paintEllipse
				paint.color = paintMarkupRed
				paint.sizeIdx = 2
			}
			if !small {
				paint.sizeIdx = max(paint.sizeIdx, 2)
			}
			PlayActivateSound()
		})
	}()
}

// paintIsOwnDrawing reports whether a picture was saved by Paint.
func paintIsOwnDrawing(path string) bool {
	dir := filepath.Clean(paintPicturesDir())
	return strings.EqualFold(filepath.Dir(filepath.Clean(path)), dir)
}

// paintDecodeNRGBA decodes a picture into straight-alpha pixels, turned
// upright (a phone photo's EXIF turn) and shrunk to maxEdge on its longer
// side.
func paintDecodeNRGBA(path string, maxEdge int) (img *image.NRGBA, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("the picture could not be decoded")
		}
	}()
	orient := imgvFileOrientation(path)
	pic, err := imgvDecodeFile(path)
	if err != nil {
		return nil, err
	}
	b := pic.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, fmt.Errorf("the picture is empty")
	}
	w, h := imgvFitInside(b.Dx(), b.Dy(), maxEdge, maxEdge)
	// At the same size this is a straight conversion to NRGBA.
	img = imgvScale(pic, w, h)
	if orient > 1 {
		img = imgvOrient(img, orient)
	}
	return img, nil
}

// paintEncodePNG encodes a picture as PNG.
func paintEncodePNG(img *image.NRGBA, level png.CompressionLevel) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: level}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── Saving ────────────────────────────────────────────────────────────────

// paintSave saves the drawing where it was saved last, or asks for a name.
func paintSave() {
	d := paint.doc
	if d == nil {
		return
	}
	paintEndStroke()
	if d.savedPath == "" {
		paintSaveAs()
		return
	}
	paintWriteDrawing(d, d.savedPath)
}

// paintSaveAs asks for a name and saves the drawing under it.
func paintSaveAs() {
	d := paint.doc
	if d == nil {
		return
	}
	paintEndStroke()
	seed := paintSeedName(d)
	osPromptOpen("Save drawing", "Name", "Save", seed, 60, func(name string) {
		if paint.doc != d {
			return
		}
		name = paintCleanName(name)
		if name == "" {
			name = paintCleanName(seed)
		}
		dir := paintPicturesDir()
		path := filepath.Join(dir, name+".png")
		if !strings.EqualFold(path, d.savedPath) {
			path = paintUniquePath(dir, name, "")
		}
		paintWriteDrawing(d, path)
	})
}

// paintWriteDrawing writes d as a PNG at path and, once it is written,
// remembers the path for the next Save.
func paintWriteDrawing(d *paintDoc, path string) {
	if strings.EqualFold(filepath.Clean(path), filepath.Clean(d.source)) && d.orig != nil {
		// Never over the screenshot a markup was made on.
		path = paintUniquePath(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), " markup")
	}
	pix := paintCloneNRGBA(d.img)
	rev := d.rev
	ShowToast("Saving "+filepath.Base(path)+"…", ToastKindInfo)
	paintGo(func() {
		data, err := paintEncodePNG(pix, png.DefaultCompression)
		if err == nil {
			err = paintWriteFile(path, data)
		}
		tbxPost(func() {
			if err != nil {
				logWarn("paint", "save failed: %v", err)
				ShowToast("The drawing could not be saved: "+err.Error(), ToastKindError)
				PlayErrorSound()
				return
			}
			if paint.doc == d {
				d.savedPath = path
				// A stroke drawn while the PNG was written is not in it.
				if d.rev == rev {
					d.changed = false
				}
				d.autoDirty = true // Continue should save to the same file
			}
			ShowToast("Saved to Pictures/Paint/"+filepath.Base(path), ToastKindSuccess)
			PlaySuccessSound()
		})
	})
}

// paintSeedName is the name the save keyboard starts with.
func paintSeedName(d *paintDoc) string {
	if d.savedPath != "" {
		return strings.TrimSuffix(filepath.Base(d.savedPath), filepath.Ext(d.savedPath))
	}
	if d.source != "" {
		return strings.TrimSuffix(filepath.Base(d.source), filepath.Ext(d.source)) + " markup"
	}
	dir := paintPicturesDir()
	for n := 1; n < 1000; n++ {
		name := fmt.Sprintf("Drawing %d", n)
		if _, err := os.Stat(filepath.Join(dir, name+".png")); os.IsNotExist(err) {
			return name
		}
	}
	return "Drawing"
}

// paintCleanName makes a typed name a safe file name: no folder separators
// or characters Windows and FAT cards refuse, no dots at the ends, at most
// 60 characters, and no ".png" typed twice.
func paintCleanName(name string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(filepath.Ext(name), ".png") {
		name = strings.TrimSpace(name[:len(name)-4])
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n >= 60 {
			break
		}
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		b.WriteRune(r)
		n++
	}
	return strings.Trim(b.String(), ". ")
}

// paintUniquePath is dir/name+suffix.png, with " 2", " 3"… when taken.
func paintUniquePath(dir, name, suffix string) string {
	path := filepath.Join(dir, name+suffix+".png")
	for n := 2; n < 10000; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s%s %d.png", name, suffix, n))
	}
	return path
}

// ── Wallpaper ─────────────────────────────────────────────────────────────

// paintUseAsWallpaper makes a screen-sized picture of the canvas — pixel art
// enlarged in whole steps without smoothing, centred on its own backdrop
// colour — saves it beside the drawings and sets it the way the Wallpapers
// picker does (wallpaperApply), so the picker opens next to it afterwards.
func paintUseAsWallpaper(config *Config) {
	d := paint.doc
	if d == nil || config == nil {
		return
	}
	paintEndStroke()
	pix := paintCloneNRGBA(d.img)
	pixelArt := d.pixelArt
	sw, sh := int(screenWidth), int(screenHeight)
	dir := paintPicturesDir()
	path := paintUniquePath(dir, paintCleanName(paintSeedName(d)), " wallpaper")
	ShowToast("Making the wallpaper…", ToastKindInfo)
	paintGo(func() {
		wall := paintCompose(pix, sw, sh, pixelArt)
		data, err := paintEncodePNG(wall, png.DefaultCompression)
		if err == nil {
			err = paintWriteFile(path, data)
		}
		tbxPost(func() {
			if err != nil {
				logWarn("paint", "wallpaper save failed: %v", err)
				ShowToast("The wallpaper could not be saved: "+err.Error(), ToastKindError)
				PlayErrorSound()
				return
			}
			if wallpaperApply(config, path) {
				PlaySuccessSound()
			}
		})
	})
}

// paintCompose fits a picture into a w×h frame on a backdrop colour: pixel
// art and anything smaller than the frame grow by whole steps without
// smoothing, a larger picture is shrunk smoothly.
func paintCompose(src *image.NRGBA, w, h int, pixelArt bool) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := paintBackdropFor(src)
	for i := 0; i < len(out.Pix); i += 4 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = bg.R, bg.G, bg.B, 255
	}
	var scaled *image.NRGBA
	if k := min(w/sw, h/sh); k >= 1 {
		scaled = paintUpscale(src, k)
	} else {
		fw, fh := imgvFitInside(sw, sh, w, h)
		scaled = imgvScale(src, fw, fh)
	}
	ox := (w - scaled.Rect.Dx()) / 2
	oy := (h - scaled.Rect.Dy()) / 2
	paintBlendOver(out, scaled, ox, oy)
	return out
}

// paintBackdropFor is the colour around a wallpaper: the picture's own
// corner when it is opaque, so a drawing on a coloured ground reaches the
// screen's edges, else PICO-8's dark blue.
func paintBackdropFor(src *image.NRGBA) color.NRGBA {
	if len(src.Pix) >= 4 && src.Pix[3] == 255 {
		return color.NRGBA{R: src.Pix[0], G: src.Pix[1], B: src.Pix[2], A: 255}
	}
	return color.NRGBA{R: 0x1D, G: 0x2B, B: 0x53, A: 255}
}

// paintUpscale enlarges a picture k times, each pixel a k×k block.
func paintUpscale(src *image.NRGBA, k int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, sw*k, sh*k))
	for y := 0; y < sh; y++ {
		row := out.Pix[y*k*out.Stride : y*k*out.Stride+sw*k*4]
		for x := 0; x < sw; x++ {
			s := src.PixOffset(x, y)
			for i := 0; i < k; i++ {
				copy(row[(x*k+i)*4:], src.Pix[s:s+4])
			}
		}
		for i := 1; i < k; i++ {
			copy(out.Pix[(y*k+i)*out.Stride:], row)
		}
	}
	return out
}

// paintBlendOver lays a straight-alpha picture over an opaque one at (ox, oy).
func paintBlendOver(dst, src *image.NRGBA, ox, oy int) {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	for y := 0; y < sh; y++ {
		dy := oy + y
		if dy < 0 || dy >= dst.Rect.Dy() {
			continue
		}
		for x := 0; x < sw; x++ {
			dx := ox + x
			if dx < 0 || dx >= dst.Rect.Dx() {
				continue
			}
			s := src.PixOffset(x, y)
			a := int(src.Pix[s+3])
			if a == 0 {
				continue
			}
			o := dst.PixOffset(dx, dy)
			for c := 0; c < 3; c++ {
				dst.Pix[o+c] = uint8((int(src.Pix[s+c])*a + int(dst.Pix[o+c])*(255-a)) / 255)
			}
			dst.Pix[o+3] = 255
		}
	}
}

// ── Game cover ────────────────────────────────────────────────────────────

// paintGame is one game the cover can go to.
type paintGame struct {
	rom   scannedROM
	title string
	key   string // the title folded for matching
}

// paintGames is the game list behind "Use as game cover".
var paintGames struct {
	all     []paintGame
	shown   []int // indexes into all, filtered by query
	query   string
	loading bool
	gen     int
	// want is the game to preselect once the list is in (a screenshot's).
	want string
}

// paintOpenGamePicker opens the game list for the cover, preselecting the
// game a screenshot came from when its name or the play history tells.
func paintOpenGamePicker() {
	if paint.doc == nil {
		return
	}
	paintEndStroke()
	paintGames.query = ""
	paintGames.want = ""
	paintOpenSheet(paintSheetGame)
	// The library already scanned this session is the quick answer.
	if games := paintGamesFromLibrary(); len(games) > 0 {
		paintGamesSet(games)
		return
	}
	paintGames.loading = true
	paintGames.gen++
	gen := paintGames.gen
	roots := append([]string(nil), romLibraryRoots()...)
	go func() {
		var games []paintGame
		func() {
			defer func() {
				if r := recover(); r != nil {
					logWarn("paint", "game list failed: %v", r)
				}
			}()
			signature := boxArtLibrarySignature(roots)
			roms, ok := romScanCached(roots, signature, time.Now())
			if !ok {
				report := scanROMLibrary(roots)
				romScanPublish(roots, signature, report.Games)
				roms = report.Games
			}
			for _, rom := range roms {
				if rom.Collection == "" || rom.Collection == "Plugins" {
					continue
				}
				title := gameDisplayTitle(rom.Path)
				if title == "" {
					continue
				}
				games = append(games, paintGame{rom: rom, title: title, key: paintFoldTitle(title)})
			}
		}()
		tbxPost(func() {
			if paintGames.gen != gen {
				return
			}
			paintGames.loading = false
			paintGamesSet(games)
		})
	}()
}

// paintGamesFromLibrary is the Games library's last scan, as cover targets.
func paintGamesFromLibrary() []paintGame {
	var games []paintGame
	for _, sys := range gameLib.lib.Systems {
		for _, g := range sys.Games {
			rom, ok := artChooseROM(g)
			if !ok {
				continue
			}
			title := g.Label()
			games = append(games, paintGame{rom: rom, title: title, key: paintFoldTitle(title)})
		}
	}
	return games
}

// paintGamesSet takes a game list: sorted by title, the picture's game
// highlighted.
func paintGamesSet(games []paintGame) {
	sort.SliceStable(games, func(i, j int) bool {
		if games[i].key != games[j].key {
			return games[i].key < games[j].key
		}
		return games[i].rom.System < games[j].rom.System
	})
	paintGames.all = games
	paintGamesFilter()
	paint.sheetCur, paint.sheetScroll = 0, 0
	if d := paint.doc; d != nil && d.source != "" {
		if i := paintGuessGame(d.source, games); i >= 0 {
			for row, idx := range paintGames.shown {
				if idx == i {
					paint.sheetCur = row
				}
			}
		}
	}
}

// paintGamesFilter applies the search to the list.
func paintGamesFilter() {
	q := paintFoldTitle(paintGames.query)
	paintGames.shown = paintGames.shown[:0]
	for i, g := range paintGames.all {
		if q == "" || strings.Contains(g.key, q) {
			paintGames.shown = append(paintGames.shown, i)
		}
	}
	paint.sheetCur = clampInt(paint.sheetCur, 0, max(len(paintGames.shown)-1, 0))
}

// paintFoldTitle folds a title for matching: lower case, letters and digits,
// single spaces.
func paintFoldTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}

// paintGuessGame finds the game a picture shows, or -1. A JukaHub game
// screenshot (game_<time>.png) is matched to the game played when it was
// taken; any other name (RetroArch's "<game>-<date>.png") or its folder is
// matched to the longest game title it starts with.
func paintGuessGame(path string, games []paintGame) int {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.HasPrefix(strings.ToLower(stem), "game_") {
		var shot time.Time
		if info, err := os.Stat(path); err == nil {
			shot = info.ModTime()
		}
		best, bestAt := "", time.Time{}
		for _, r := range recentGames() {
			if !shot.IsZero() && r.PlayedAt.After(shot.Add(time.Minute)) {
				continue
			}
			if best == "" || r.PlayedAt.After(bestAt) {
				best, bestAt = r.Path, r.PlayedAt
			}
		}
		if best != "" {
			for i, g := range games {
				if g.rom.Path == best {
					return i
				}
			}
		}
	}
	for _, name := range []string{stem, filepath.Base(filepath.Dir(path))} {
		key := paintFoldTitle(name)
		bestI, bestLen := -1, 0
		for i, g := range games {
			if len(g.key) > bestLen && len(g.key) >= 3 && strings.HasPrefix(key, g.key) {
				bestI, bestLen = i, len(g.key)
			}
		}
		if bestI >= 0 {
			return bestI
		}
	}
	return -1
}

// paintUseAsCover writes the canvas as a game's cover the way the cover
// chooser does (artChooseApply): into the game-art folder at the game's
// cover path, the "no cover" mark cleared, the library told.
func paintUseAsCover(g paintGame) {
	d := paint.doc
	if d == nil {
		return
	}
	rom := g.rom
	dest := romArtworkPath(rom, libraryArtworkRoot())
	if dest == "" {
		ShowToast("The artwork folder is unavailable.", ToastKindWarning)
		PlayErrorSound()
		return
	}
	pix := paintCloneNRGBA(d.img)
	pixelArt := d.pixelArt
	label := g.title
	ShowToast("Saving the cover for "+label+"…", ToastKindInfo)
	paintGo(func() {
		cover := paintCoverImage(pix, pixelArt)
		data, err := paintEncodePNG(cover, png.DefaultCompression)
		if err == nil {
			err = paintWriteFile(dest, data)
		}
		tbxPost(func() {
			if err != nil {
				logWarn("art", "cannot save the Paint cover for %s: %v", rom.Path, err)
				ShowToast("The cover could not be saved: "+err.Error(), ToastKindWarning)
				PlayErrorSound()
				return
			}
			logInfo("art", "%s: cover drawn in Paint", rom.Path)
			artworkClearNoCover(rom)
			libraryCoverChanged(rom)
			if nextuiActive() && romLocalCoverPath(rom) != "" {
				ShowToast("Cover saved · NextUI shows the card's own cover for "+label+" first", ToastKindInfo)
				return
			}
			ShowToast("Cover saved for "+label, ToastKindSuccess)
			PlaySuccessSound()
		})
	})
}

// paintCoverImage is the canvas at cover size: pixel art enlarged in whole
// steps to about paintCoverEdge without smoothing, a large picture shrunk to
// it.
func paintCoverImage(src *image.NRGBA, pixelArt bool) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	long := max(sw, sh)
	if long <= 0 {
		return src
	}
	if long < paintCoverEdge && (pixelArt || long*2 <= paintCoverEdge) {
		return paintUpscale(src, max(1, paintCoverEdge/long))
	}
	if long > paintCoverEdge {
		w, h := imgvFitInside(sw, sh, paintCoverEdge, paintCoverEdge)
		return imgvScale(src, w, h)
	}
	return src
}

// ── The Open list ─────────────────────────────────────────────────────────

// paintPic is one picture the Open list offers.
type paintPic struct {
	path, name, where string
	mod               time.Time
}

// paintPics is the Open list.
var paintPics struct {
	list    []paintPic
	loading bool
	gen     int
}

// paintPictureDirs are the folders the Open list reads, with the word each
// is shown under: JukaHub's screenshots (where Select + R1 and F12 save),
// RetroArch's on the card, and Paint's own drawings.
func paintPictureDirs() [][2]string {
	dirs := [][2]string{
		{screenshotDir(), "Screenshots"},
		{screenshotAppDir(), "Screenshots"},
		{paintPicturesDir(), "Paint"},
	}
	if IsTrimuiDevice() {
		dirs = append(dirs, [2]string{"/mnt/SDCARD/Screenshots", "Card screenshots"})
	} else {
		dirs = append(dirs, [2]string{screenshotPicturesDir(), "Screenshots"})
	}
	return dirs
}

// paintOpenPicker lists the newest pictures in the background.
func paintOpenPicker() {
	paintOpenSheet(paintSheetOpen)
	paintPics.loading = true
	paintPics.gen++
	gen := paintPics.gen
	dirs := paintPictureDirs()
	go func() {
		seen := map[string]bool{}
		var list []paintPic
		for _, d := range dirs {
			dir := filepath.Clean(d[0])
			if dir == "" || dir == "." || seen[dir] {
				continue
			}
			seen[dir] = true
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || strings.HasPrefix(name, ".") {
					continue
				}
				switch strings.ToLower(filepath.Ext(name)) {
				case ".png", ".jpg", ".jpeg", ".bmp":
				default:
					continue
				}
				info, err := e.Info()
				if err != nil || info.Size() == 0 {
					continue
				}
				list = append(list, paintPic{path: filepath.Join(dir, name), name: name, where: d[1], mod: info.ModTime()})
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].mod.After(list[j].mod) })
		if len(list) > paintPicsMax {
			list = list[:paintPicsMax]
		}
		tbxPost(func() {
			if paintPics.gen != gen {
				return
			}
			paintPics.loading = false
			paintPics.list = list
			if paint.sheet == paintSheetOpen {
				paint.sheetCur = clampInt(paint.sheetCur, 0, max(len(list)-1, 0))
			}
		})
	}()
}

// ── Colours ───────────────────────────────────────────────────────────────

// paintHex is a colour as #RRGGBBAA.
func paintHex(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X%02X", c.R, c.G, c.B, c.A)
}

// paintParseHex reads paintHex's form.
func paintParseHex(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 8 {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}, true
}
