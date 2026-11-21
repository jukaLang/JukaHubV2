package main

// imageviewer_edit.go — the picture viewer's options sheet (X, the "More"
// chip, Tab on a keyboard), the image viewer's edits:
//
//   - Rotate right turns the picture a quarter clockwise and saves it: decoded
//     with the viewer's own decoders, turned, and written back as the format it
//     is (PNG stays PNG, JPEG stays JPEG at quality 92). A JPEG is first stood
//     upright by its EXIF orientation and written without EXIF, so the turn is
//     never applied twice. WebP, GIF, BMP and the rest cannot be written here:
//     they turn on screen only, and the viewer says so.
//   - Rename asks for a new name on the on-screen keyboard; same folder, the
//     extension kept, and a name with a separator, an empty one or one already
//     taken is refused.
//   - Delete removes the picture on the second press and shows the next one.
//
// The sheet also carries what X and Start did before (Set as lock screen,
// Mark up), so the viewer's X keeps reaching the lock screen. Every write is
// atomic (a hidden temp file beside the picture, fsync, rename over it), and
// afterwards the Files list under the viewer is read again: the thumbnail
// cache (filethumb.go) keys a picture by its size and time, so the new
// picture gets a new thumbnail.
//
// A chat image has no file of its own: the sheet does not open for it.
//
//	keyboard  Tab / X options · R rotate right · F2 rename · Delete delete

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// imgvEditKind is one row of the sheet.
type imgvEditKind int

const (
	imgvEditRotate imgvEditKind = iota
	imgvEditRename
	imgvEditDelete
	imgvEditLock
	imgvEditMarkUp
)

// imgvEditRow is a row as the sheet draws it.
type imgvEditRow struct {
	kind    imgvEditKind
	label   string
	detail  string
	enabled bool
	danger  bool
}

// imgvJPEGQuality is the quality a turned JPEG is written at: high enough that
// one turn does not show, low enough that the file does not double in size.
const imgvJPEGQuality = 92

// imgvEdit is the sheet and the save in flight. Render thread.
var imgvEdit struct {
	open    bool
	cursor  int
	armed   bool
	armedAt time.Time
	hits    []sdl.Rect
	card    sdl.Rect
	// saving is the picture a Rotate right is writing; the sheet waits for it.
	saving string
	// turnPath and turns are the on-screen-only turn of a picture that cannot
	// be saved, so a second press turns it further.
	turnPath string
	turns    int
	// turnGen is the load that shows that turn: a picture shown again since
	// (browsed away and back) is unturned, and starts from no turn.
	turnGen int
}

var errImgvCannotSave = errors.New("this picture format cannot be saved here")

// imgvEditAvailable reports whether the picture on screen is a file the sheet
// can act on.
func imgvEditAvailable() bool {
	return !imageViewerRemote && strings.TrimSpace(imageViewerPath) != ""
}

// imgvEditSavable reports whether path's format is one Rotate right writes.
func imgvEditSavable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

// imgvEditRows builds the sheet for the picture on screen.
func imgvEditRows(config *Config) []imgvEditRow {
	path := imageViewerPath
	loaded := imageViewerTexture != nil
	rotate := imgvEditRow{kind: imgvEditRotate, label: "Rotate right", detail: "saves the picture", enabled: loaded}
	switch {
	case imgvEdit.saving != "":
		rotate.detail, rotate.enabled = "saving…", false
	case !loaded && imgv.errText != "":
		rotate.detail = "the picture did not open"
	case !loaded:
		rotate.detail = "still opening…"
	case !imgvEditSavable(path):
		rotate.detail = "on screen only · " + strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), ".")) + " can't be saved"
	}
	del := imgvEditRow{kind: imgvEditDelete, label: "Delete", detail: "asks again", enabled: true, danger: true}
	if imgvEditArmed() {
		del.detail = hintSentence("press %s again", "A", "Delete")
	}
	rename := imgvEditRow{kind: imgvEditRename, label: "Rename", detail: filepath.Base(path), enabled: true}
	if imgvEdit.saving != "" {
		// The turned picture is renamed over its old name when written: a
		// rename or a delete now would race it.
		rename.detail, rename.enabled = "wait for the save", false
		del.detail, del.enabled = "wait for the save", false
	}
	lock := imgvEditRow{kind: imgvEditLock, label: "Set as lock screen", detail: "shows when the screen sleeps", enabled: true}
	if screenlockImageIsCurrent(config, path) {
		lock.label, lock.detail = "Remove from lock screen", "the default idle screen comes back"
	}
	return []imgvEditRow{
		rotate,
		rename,
		del,
		lock,
		{kind: imgvEditMarkUp, label: "Mark up in Paint", detail: "saves a marked-up copy", enabled: loaded},
	}
}

// imgvEditOpen opens the sheet (X).
func imgvEditOpen(config *Config) bool {
	if !imgvEditAvailable() {
		return false
	}
	imgvEdit.open, imgvEdit.cursor, imgvEdit.armed = true, 0, false
	imgvEdit.hits = imgvEdit.hits[:0]
	imgvEdit.card = sdl.Rect{}
	PlayActivateSound()
	return true
}

// imgvEditClose closes the sheet.
func imgvEditClose(sound bool) {
	if !imgvEdit.open {
		return
	}
	imgvEdit.open, imgvEdit.armed = false, false
	imgvEdit.hits = imgvEdit.hits[:0]
	imgvEdit.card = sdl.Rect{}
	if sound {
		PlayBackSound()
	}
}

// imgvEditForget drops the sheet without a sound: the viewer closed.
func imgvEditForget() {
	imgvEdit.open, imgvEdit.armed = false, false
	imgvEdit.turnPath, imgvEdit.turns, imgvEdit.turnGen = "", 0, 0
}

func imgvEditArmed() bool {
	return imgvEdit.armed && time.Since(imgvEdit.armedAt) <= feArmWindow
}

func imgvEditMove(delta int, config *Config) {
	rows := imgvEditRows(config)
	imgvEdit.cursor = (imgvEdit.cursor + delta + len(rows)) % len(rows)
	imgvEdit.armed = false
	PlayNavSound()
}

// imgvEditActivate runs the highlighted row.
func imgvEditActivate(config *Config) {
	rows := imgvEditRows(config)
	if imgvEdit.cursor < 0 || imgvEdit.cursor >= len(rows) {
		return
	}
	row := rows[imgvEdit.cursor]
	if !row.enabled {
		PlayErrorSound()
		showToast(row.label+" — "+row.detail, ToastInfo())
		return
	}
	switch row.kind {
	case imgvEditRotate:
		imgvEditClose(false)
		imgvRotateRight(config)
	case imgvEditRename:
		imgvEditClose(false)
		imgvRenameStart()
	case imgvEditDelete:
		if !imgvEditArmed() {
			imgvEdit.armed, imgvEdit.armedAt = true, time.Now()
			PlayNavSound()
			showToast("Press again to delete "+filepath.Base(imageViewerPath), ToastWarn())
			return
		}
		imgvEditClose(false)
		imgvDeleteCurrent(config)
	case imgvEditLock:
		imgvEditClose(false)
		PlayActivateSound()
		toggleScreenlockFromViewer(config)
	case imgvEditMarkUp:
		imgvEditClose(false)
		imgvMarkUp(config)
	}
}

// ── Input ────────────────────────────────────────────────────────────────────

// imgvEditPad answers the pad while the sheet is open: Up/Down choose, A runs,
// B or X closes. The viewer behind it gets nothing.
func imgvEditPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !imgvEdit.open || e == nil {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		imgvEditMove(-1, config)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		imgvEditMove(1, config)
	case sdl.CONTROLLER_BUTTON_A:
		imgvEditActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_X:
		imgvEditClose(true)
	}
	return true
}

// imgvEditKey answers the keyboard: the sheet's keys while it is open, and
// the viewer's editing shortcuts while it is not. It reports whether it used
// the key.
func imgvEditKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if imgvEdit.open {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			imgvEditMove(-1, config)
		case sdl.K_DOWN:
			imgvEditMove(1, config)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
			// A held key repeats; a repeat is never the second press that
			// confirms Delete.
			if e.Repeat == 0 {
				imgvEditActivate(config)
			}
		case sdl.K_ESCAPE, sdl.K_b, sdl.K_x, sdl.K_TAB, sdl.K_BACKSPACE:
			imgvEditClose(true)
		}
		return true
	}
	if !imgvEditAvailable() {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_x, sdl.K_TAB:
		return imgvEditOpen(config)
	case sdl.K_r:
		if imageViewerTexture == nil || imgvEdit.saving != "" {
			PlayErrorSound()
			return true
		}
		imgvRotateRight(config)
		return true
	case sdl.K_F2:
		imgvRenameStart()
		return true
	case sdl.K_DELETE:
		// The sheet opens on its Delete row: Enter twice deletes.
		if imgvEditOpen(config) {
			imgvEdit.cursor = 2
		}
		return true
	}
	return false
}

// imgvEditMouse answers a press: the sheet's rows (a press outside closes it),
// and the toolbar's More chip.
func imgvEditMouse(mx, my int32, config *Config) bool {
	if imgvEdit.open {
		for i, r := range imgvEdit.hits {
			if pointInRect(mx, my, r) {
				imgvEdit.cursor = i
				imgvEditActivate(config)
				return true
			}
		}
		if !pointInRect(mx, my, imgvEdit.card) {
			imgvEditClose(true)
		}
		return true
	}
	if !imgv.hideUI && filesChipAt(imgv.chips, mx, my) == "options" {
		imgvEditOpen(config)
		return true
	}
	return false
}

// ── Rotate right ─────────────────────────────────────────────────────────────

// imgvRotateMaxPixels bounds a picture turned and saved: the turn holds two
// full-size copies, which on the handheld's RAM is the limit long before the
// viewer's own (a shrunk texture) is.
func imgvRotateMaxPixels() int64 {
	if IsTrimuiDevice() {
		return 20 << 20
	}
	return imgvMaxPixels
}

// imgvRotateRight turns the picture on screen a quarter clockwise: saved for
// PNG and JPEG (on a goroutine; the viewer reloads it when written), on screen
// only for anything else.
func imgvRotateRight(config *Config) {
	path := imageViewerPath
	if !imgvEditAvailable() || imageViewerTexture == nil {
		PlayErrorSound()
		return
	}
	if imgvEdit.saving != "" {
		showToast("Still saving the last turn…", ToastInfo())
		return
	}
	if !imgvEditSavable(path) {
		if imgvEdit.turnPath != path || !imgvCurrent(imgvEdit.turnGen) {
			imgvEdit.turnPath, imgvEdit.turns = path, 0
		}
		imgvEdit.turns = (imgvEdit.turns + 1) % 4
		imgvEdit.turnGen = imgvShowTurned(path, imgvEdit.turns)
		PlayNavSound()
		showToast("Can't save this format — turned on screen only", ToastInfo())
		return
	}
	imgvEdit.saving = path
	PlayActivateSound()
	showToast("Turning and saving "+filepath.Base(path)+"…", ToastInfo())
	go func() {
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = errors.New("the picture could not be turned")
				}
			}()
			err = imgvRotateSave(path)
		}()
		if err != nil {
			logWarn("imageviewer", "rotate %s: %v", filepath.Base(path), err)
		}
		tbxPost(func() { imgvRotateFinished(appConfig, path, err) })
	}()
}

// imgvRotateSave decodes path, stands it upright, turns it a quarter clockwise
// and writes it back in its own format. Off the render thread.
func imgvRotateSave(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("this is not a picture file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return errors.New("this picture cannot be read")
	}
	if format != "jpeg" && format != "png" {
		return errImgvCannotSave
	}
	if int64(cfg.Width)*int64(cfg.Height) > imgvRotateMaxPixels() {
		return fmt.Errorf("it is %d × %d, too large to turn here", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	pic, _, err := image.Decode(f)
	if err != nil {
		return errors.New("this picture cannot be read")
	}
	_ = f.Close()
	turned := imgvTurnUpright(pic, format, path)
	var buf bytes.Buffer
	switch format {
	case "jpeg":
		// Go's encoder writes no EXIF: the orientation applied above is not
		// applied again by the next reader.
		err = jpeg.Encode(&buf, turned, &jpeg.Options{Quality: imgvJPEGQuality})
	default:
		err = png.Encode(&buf, turned)
	}
	if err != nil {
		return err
	}
	return imgvReplaceFile(path, buf.Bytes(), info.Mode().Perm())
}

// imgvTurnUpright is the picture as the viewer showed it (a JPEG's EXIF turn
// applied) turned a quarter clockwise, at full size. imgvScale at the
// picture's own size is a straight copy into NRGBA, read from the decoder's
// planes.
func imgvTurnUpright(pic image.Image, format, path string) *image.NRGBA {
	b := pic.Bounds()
	flat := imgvScale(pic, b.Dx(), b.Dy())
	if format == "jpeg" {
		flat = imgvOrient(flat, imgvFileOrientation(path))
	}
	// EXIF orientation 6 is "needs a quarter turn clockwise" (saverOrientFor).
	return imgvOrient(flat, 6)
}

// imgvReplaceFile writes data over path atomically: a hidden temp file in the
// same folder (a rename is only atomic within one filesystem), synced, then
// renamed over the picture. No .bak is left beside a photo.
func imgvReplaceFile(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".jukahub-turn.tmp")
	if err := writeFileSynced(tmp, data, perm|0o200); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(dir)
	return nil
}

// imgvRotateFinished reports a finished save on the render thread: the
// picture is read again from the card, the lock screen redraws it if it is
// that picture, and the Files list shows its new size and thumbnail.
func imgvRotateFinished(config *Config, path string, err error) {
	imgvEdit.saving = ""
	if err != nil {
		showToast("Cannot turn the picture: "+err.Error(), ToastError())
		return
	}
	if screenlockImageIsCurrent(config, path) {
		_ = setScreenlockImage(config, path)
	}
	imgvEditRefreshFiles(config, path)
	if imageViewerPath == path && !imageViewerRemote {
		imageViewerShow(path)
	}
	showToast("Turned right and saved", ToastSuccess())
}

// imgvShowTurned shows path turned by turns quarters, on screen only: the
// viewer's own load (imageViewerShow) with a turn added to the decode. It
// returns the load's generation.
func imgvShowTurned(path string, turns int) int {
	imgvReleaseTextures()
	hide := imgv.hideUI
	imgv = imgvState{hideUI: hide, loading: true}
	imageViewerPath = path
	imageViewerZoom = 1.0
	imageViewerPanX, imageViewerPanY = 0, 0
	imgvLoad.mu.Lock()
	imgvLoad.gen++
	gen := imgvLoad.gen
	imgvLoad.done = nil
	imgvLoad.mu.Unlock()
	go imgvDecodeTurned(gen, path, turns, imgvMaxTextureDim(), int(screenWidth), int(screenHeight))
	return gen
}

// imgvDecodeTurned is imgvDecodeWorker with the extra turn: the picture is
// shrunk to the texture limit first, then stood upright and turned, which
// costs a fraction of turning it at full size.
func imgvDecodeTurned(gen int, path string, turns, maxDim, fitW, fitH int) {
	res := imgvDecoded{gen: gen}
	defer func() {
		if r := recover(); r != nil {
			res.err = errors.New("the picture could not be decoded")
			res.full, res.fit = nil, nil
		}
		imgvLoad.mu.Lock()
		if imgvLoad.gen == gen {
			imgvLoad.done = &res
		}
		imgvLoad.mu.Unlock()
	}()
	if info, err := os.Stat(path); err == nil {
		res.size = info.Size()
	}
	pic, err := imgvDecodeFile(path)
	if err != nil {
		res.err = err
		return
	}
	b := pic.Bounds()
	w, h := imgvFitInside(b.Dx(), b.Dy(), maxDim, maxDim)
	full := imgvOrient(imgvScale(pic, w, h), imgvFileOrientation(path))
	for i := 0; i < turns%4; i++ {
		full = imgvOrient(full, 6)
	}
	res.full = full
	// The size as seen: the shrunk copy's shape at the picture's own scale.
	res.origW, res.origH = b.Dx(), b.Dy()
	if (full.Rect.Dx() > full.Rect.Dy()) != (b.Dx() > b.Dy()) {
		res.origW, res.origH = b.Dy(), b.Dx()
	}
	fw, fh := imgvFitInside(full.Rect.Dx(), full.Rect.Dy(), fitW, fitH)
	if fw*3 < full.Rect.Dx()*2 {
		res.fit = imgvScale(full, fw, fh)
	}
}

// ── Rename ───────────────────────────────────────────────────────────────────

// imgvRenameStart asks for the new name, seeded with the current one without
// its extension (which is kept).
func imgvRenameStart() {
	if !imgvEditAvailable() {
		return
	}
	if imgvEdit.saving != "" {
		showToast("Still saving the turned picture…", ToastInfo())
		return
	}
	path := imageViewerPath
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	osPromptOpen("Rename picture", "New name", "Rename", stem, 120, func(typed string) {
		imgvRenameSubmit(appConfig, path, typed)
	})
}

// imgvRenameName turns what was typed into the new file name: the extension
// is the picture's own (typed again, it is not doubled), and the name must be
// one plain component (feValidateName).
func imgvRenameName(typed, ext string) (string, error) {
	name := strings.TrimSpace(typed)
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("a name cannot contain / or \\")
	}
	if ext != "" && len(name) > len(ext) && strings.EqualFold(filepath.Ext(name), ext) {
		name = strings.TrimSpace(name[:len(name)-len(ext)])
	}
	if name == "" {
		return "", errors.New("the name is empty")
	}
	return feValidateName(name + ext)
}

// imgvRenameSubmit renames the picture in its own folder and follows it: the
// viewer's list, the lock screen setting and the Files cursor.
func imgvRenameSubmit(config *Config, path, typed string) {
	name, err := imgvRenameName(typed, filepath.Ext(path))
	if err != nil {
		showToast("Cannot rename: "+err.Error(), ToastError())
		return
	}
	dest, err := safeJoinFile(filepath.Dir(path), name)
	if err != nil {
		showToast("Cannot rename: "+filesFriendlyError(err), ToastError())
		return
	}
	if dest == path {
		return
	}
	srcInfo, err := os.Lstat(path)
	if err != nil {
		showToast("Cannot rename: "+filesFriendlyError(err), ToastError())
		return
	}
	if !srcInfo.Mode().IsRegular() {
		showToast("Cannot rename: this is not a picture file", ToastError())
		return
	}
	// On a case-insensitive card the "existing" file is the picture itself
	// when only the letter case changes (feRenameEntry's rule).
	if destInfo, statErr := os.Lstat(dest); statErr == nil && !os.SameFile(srcInfo, destInfo) {
		showToast("Cannot rename: \""+name+"\" already exists here", ToastError())
		return
	}
	wasLock := screenlockImageIsCurrent(config, path)
	if err := os.Rename(path, dest); err != nil {
		showToast("Cannot rename: "+filesFriendlyError(err), ToastError())
		return
	}
	trophyEvent(tkFileRename)
	for i, p := range imageViewerFiles {
		if p == path {
			imageViewerFiles[i] = dest
		}
	}
	if imageViewerPath == path {
		// The same picture under a new name: the textures stay.
		imageViewerPath = dest
	}
	if imgvEdit.turnPath == path {
		imgvEdit.turnPath = dest
	}
	if wasLock {
		_ = setScreenlockImage(config, dest)
	}
	imgvForgetMark(path)
	imgvEditRefreshFiles(config, dest)
	PlaySuccessSound()
	showToast("Renamed to "+name, ToastSuccess())
}

// ── Delete ───────────────────────────────────────────────────────────────────

// imgvDeleteCurrent deletes the picture on screen (the sheet's second press)
// and shows the next one in the folder, or closes the viewer after the last.
func imgvDeleteCurrent(config *Config) {
	path := imageViewerPath
	if !imgvEditAvailable() {
		return
	}
	if imgvEdit.saving != "" {
		showToast("Still saving the turned picture…", ToastInfo())
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		showToast("Cannot delete: "+filesFriendlyError(err), ToastError())
		return
	}
	if info.IsDir() {
		showToast("Cannot delete: this is a folder", ToastError())
		return
	}
	wasLock := screenlockImageIsCurrent(config, path)
	if err := os.Remove(path); err != nil {
		showToast("Cannot delete: "+filesFriendlyError(err), ToastError())
		return
	}
	trophyEventN(tkFileDelete, 1)
	if wasLock {
		_ = clearScreenlockImage(config)
	}
	imgvForgetMark(path)
	name := filepath.Base(path)
	idx := -1
	for i, p := range imageViewerFiles {
		if p == path {
			idx = i
			break
		}
	}
	if idx >= 0 {
		imageViewerFiles = append(imageViewerFiles[:idx:idx], imageViewerFiles[idx+1:]...)
	}
	if len(imageViewerFiles) == 0 {
		imgvEditRefreshFiles(config, "")
		closeImageViewer()
		PlaySuccessSound()
		showToast("Deleted "+name, ToastSuccess())
		return
	}
	if idx < 0 {
		idx = imageViewerIndex
	}
	// The picture after it takes its place; after the last, the first.
	if idx < 0 || idx >= len(imageViewerFiles) {
		idx = 0
	}
	imageViewerIndex = idx
	imageViewerShow(imageViewerFiles[idx])
	imgvEditRefreshFiles(config, imageViewerFiles[idx])
	PlaySuccessSound()
	showToast("Deleted "+name, ToastSuccess())
}

// imgvForgetMark drops a Files mark on a picture that was renamed or deleted,
// so the folder's "N selected" does not count a file that is gone.
func imgvForgetMark(path string) {
	for marked := range feSelectedPaths {
		if filesSamePath(marked, path) {
			delete(feSelectedPaths, marked)
		}
	}
}

// imgvEditRefreshFiles reads the Files folder under the viewer again and puts
// its cursor on focus (when not ""): the list shows the new name, size and
// thumbnail, and the viewer and the list stay on the same picture.
func imgvEditRefreshFiles(config *Config, focus string) {
	if config == nil || !feSceneActive(config) || filesNav.atLocations {
		return
	}
	feListDirectory(config)
	if focus != "" {
		feFocusPath(config, focus)
	}
	// In two panes the other one may show the same folder (files_dualpane.go).
	filesDual.other.stale = true
}

// ── Drawing ──────────────────────────────────────────────────────────────────

// imgvEditRender draws the sheet over the viewer, and a "Saving…" pill while
// a turned picture is being written.
func imgvEditRender(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || imageViewerPath == "" {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	lineH := int32(font.Height())
	if imgvEdit.saving != "" && !imgvEdit.open {
		label := "Saving the turned picture…"
		w, _ := measureText(font, label)
		pill := sdl.Rect{X: (screenWidth - w - 32) / 2, Y: screenHeight - lineH - 84, W: w + 32, H: lineH + 16}
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		shade := ColorBackground
		shade.A = 220
		fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, shade)
		renderText(renderer, config, font, label, ColorTextPrimary(), pill.X+16, pill.Y+8)
	}
	if !imgvEdit.open {
		return
	}
	rows := imgvEditRows(config)
	if imgvEdit.cursor >= len(rows) {
		imgvEdit.cursor = len(rows) - 1
	}
	if imgvEdit.cursor < 0 {
		imgvEdit.cursor = 0
	}
	rowH := lineH + 14
	subtitle := filepath.Base(imageViewerPath)
	header := UIPanelHeader("Picture", subtitle, "image")
	headerH := UIPanelHeaderBand(config, header, 9)
	footerH := 12 + lineH
	panelW := int32(640)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + int32(len(rows))*rowH + footerH
	card := sdl.Rect{X: (screenWidth - panelW) / 2, Y: (screenHeight - panelH) / 2, W: panelW, H: panelH}
	imgvEdit.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	imgvEdit.hits = imgvEdit.hits[:0]
	y := card.Y + headerH
	for i, row := range rows {
		rect := sdl.Rect{X: card.X + 12, Y: y, W: card.W - 24, H: rowH - 4}
		if i == imgvEdit.cursor {
			UIListRow(renderer, rect, 10, true, false)
		}
		ink, detailInk := ColorTextPrimary(), ColorTextSecondary()
		switch {
		case !row.enabled:
			ink, detailInk = ColorTextTertiary(), ColorTextTertiary()
		case row.danger && imgvEditArmed():
			ink, detailInk = WarningInk(), WarningInk()
		case row.danger:
			ink = ToastErrorInk()
		}
		labelW, _ := measureText(font, row.label)
		renderText(renderer, config, font, row.label, ink, rect.X+12, rect.Y+7)
		if row.detail != "" {
			room := rect.W - labelW - 52
			if room < 60 {
				room = 60
			}
			// A file name is the user's text, drawn as it is.
			detail := ellipsize(font, row.detail, room)
			dw, _ := measureText(font, detail)
			filesDrawPlainText(renderer, config, font, detail, detailInk, rect.X+rect.W-12-dw, rect.Y+7)
		}
		imgvEdit.hits = append(imgvEdit.hits, rect)
		y += rowH
	}
	hint := hintLine(FooterHint{Button: "D-Pad", Label: "Choose"}, FooterHint{Button: "A", Label: "Run"},
		FooterHint{Button: "B", Label: "Close"})
	renderText(renderer, config, font, hint, ColorTextSecondary(), card.X+22, card.Y+card.H-footerH+8)
}
