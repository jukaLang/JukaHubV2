package main

// fileeditor.go — the built-in text file editor.
//
// Until now "Edit packages.json" in Patch moved the user to the Files screen
// with the right folder open, which is not an editor: on the handheld there is
// nothing to open the file with, and Files answered "No handler" for any text
// file that was not an image, a video or a script. This overlay is that
// handler.
//
// Editing on a D-pad and an on-screen keyboard is line based on purpose. The
// keyboard types into a single string, so a caret moving through a document
// would need a key map of its own; instead the list is the document, A opens
// the line under the cursor in the keyboard, and submitting writes it back.
// That covers the files people actually change here — packages.json, a config
// value, a shell script — without pretending to be a code editor.
//
// Saves are atomic (temp file + rename in the same directory) and keep the
// original permissions. A JSON file is validated before it is written: a
// packages.json with a syntax error would leave Patch unable to read its own
// index, and the buffer is still there to fix.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// fileEditorMaxBytes bounds what the editor will load. Handheld RAM is shared
// with the decoders, and a line list of a multi-megabyte log would cost more
// than the file itself.
const fileEditorMaxBytes = 256 << 10

// fileEditorState is the overlay's whole state. It is touched on the UI thread
// only, like the other panels.
type fileEditorState struct {
	open  bool
	path  string
	title string
	lines []string
	// original is the text as it was loaded, so dirty is a comparison rather
	// than a flag every edit has to remember to clear.
	original string
	// endsWithNewline records the file's final newline, which splitting and
	// joining would otherwise invent or drop — and that alone would make a
	// freshly opened file look edited.
	endsWithNewline bool
	cursor          int
	scroll          int

	// pending says what the open keyboard will do with its result: replace the
	// line under the cursor or insert a new one after it.
	pending  fileEditorPending
	err      string
	confirm  bool // close requested while dirty
	readOnly bool // the file cannot be written, so edits are refused up front

	rect     sdl.Rect
	rowRects []sdl.Rect
}

type fileEditorPending int

const (
	fileEditorIdle fileEditorPending = iota
	fileEditorReplaceLine
	fileEditorInsertLine
)

var fileEditor fileEditorState

// fileEditorAutoExts are the text formats Files opens in the editor when A is
// pressed on the row. Everything here used to answer "No handler for …": the
// app had no way to look inside a config or a JSON index at all.
var fileEditorAutoExts = map[string]bool{
	".json": true, ".jsonc": true, ".txt": true, ".md": true, ".cfg": true,
	".conf": true, ".ini": true, ".yaml": true, ".yml": true, ".toml": true,
	".xml": true, ".csv": true, ".log": true, ".env": true, ".properties": true,
}

// fileEditorSheetExts are the formats the Files action sheet offers "Edit" for
// even though A already does something else with them: a script's A runs it,
// and a page's A opens it in the sandbox, so editing needs its own entry.
var fileEditorSheetExts = map[string]bool{".sh": true, ".js": true, ".html": true, ".css": true}

// fileEditorCanEdit reports whether the action sheet may open a path here.
func fileEditorCanEdit(path string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	if fileEditorAutoExts[ext] {
		return true
	}
	return fileEditorSheetExts[ext]
}

// fileEditorAutoOpens reports whether pressing A on a row should open the
// editor. Only formats no other handler claims go through here.
func fileEditorAutoOpens(path string) bool {
	return fileEditorAutoExts[strings.ToLower(filepath.Ext(strings.TrimSpace(path)))]
}

// fileEditorTooLargeText explains why an oversized file was refused.
func fileEditorTooLargeText(size int64) string {
	return fmt.Sprintf("This file is %s; the editor stops at %s",
		formatBytes(uint64(size)), formatBytes(fileEditorMaxBytes))
}

// fileEditorLooksBinary reports whether data is not text. A NUL byte in the
// first block is the same rule git uses, and it matters here: writing a line
// list back would destroy the bytes.
func fileEditorLooksBinary(data []byte) bool {
	if len(data) > 8192 {
		data = data[:8192]
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}

// fileEditorSplit turns file bytes into editable lines. The final newline is
// dropped here and remembered by the caller, so splitting a file that ends
// with one does not add a phantom empty line that would be written back as a
// second newline.
func fileEditorSplit(text string) []string {
	body := strings.TrimSuffix(text, "\n")
	if body == "" {
		return []string{""}
	}
	return strings.Split(body, "\n")
}

// fileEditorJoin is the inverse of fileEditorSplit and restores the final
// newline only when the file had one. An empty file therefore stays empty
// instead of becoming a one-line document on the first save.
func fileEditorJoin(lines []string, trailingNewline bool) string {
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	text := strings.Join(lines, "\n")
	if trailingNewline {
		text += "\n"
	}
	return text
}

// fileEditorOpen loads path into the overlay. Every refusal is explained in a
// toast: "nothing happened" was the old behaviour and it read like a bug.
func fileEditorOpen(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		showToast("No file to edit", ToastWarn())
		return false
	}
	resolved, err := filepath.Abs(path)
	if err == nil {
		path = resolved
	}
	// Edit the target of a symlink, not the link: saving replaces the path, and
	// replacing the link would silently detach it.
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	info, err := os.Stat(path)
	if err != nil {
		showToast("Cannot edit "+filepath.Base(path)+": "+err.Error(), ToastError())
		return false
	}
	if info.IsDir() {
		showToast("That is a folder, not a file", ToastInfo())
		return false
	}
	if info.Size() > fileEditorMaxBytes {
		showToast(fileEditorTooLargeText(info.Size()), ToastWarn())
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		showToast("Cannot read "+filepath.Base(path)+": "+err.Error(), ToastError())
		return false
	}
	if fileEditorLooksBinary(data) {
		showToast(filepath.Base(path)+" is not a text file", ToastWarn())
		return false
	}

	text := string(data)
	fileEditor = fileEditorState{
		open:            true,
		path:            path,
		title:           filepath.Base(path),
		lines:           fileEditorSplit(text),
		original:        text,
		endsWithNewline: strings.HasSuffix(text, "\n"),
		// A file the app cannot write opens read-only, so the header says so and
		// the edit keys refuse instead of accepting work that Save can only throw
		// away. The check is an open for writing: the same thing the save does.
		readOnly: !fileEditorWritable(path),
	}
	fileEditorRowRects = nil
	fileEditor.rect = sdl.Rect{}
	// One more modal at a time: the editor sits over the screen that opened it.
	feSheetForget()
	PlayActivateSound()
	return true
}

// fileEditorWritable reports whether the file can be opened for writing. It
// does not truncate: O_WRONLY alone keeps the bytes, and the failure is the
// same permission error the save would hit after the edit.
func fileEditorWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// fileEditorText is the buffer as it would be written.
func fileEditorText() string {
	return fileEditorJoin(fileEditor.lines, fileEditor.endsWithNewline)
}

// fileEditorDirty reports whether the buffer differs from what was loaded.
func fileEditorDirty() bool {
	return fileEditor.open && fileEditorText() != fileEditor.original
}

// fileEditorClamp keeps the cursor and the scroll window inside the buffer.
func fileEditorClamp() {
	n := len(fileEditor.lines)
	if n == 0 {
		fileEditor.lines = []string{""}
		n = 1
	}
	if fileEditor.cursor < 0 {
		fileEditor.cursor = 0
	}
	if fileEditor.cursor >= n {
		fileEditor.cursor = n - 1
	}
	visible := fileEditorVisible()
	if fileEditor.scroll > fileEditor.cursor {
		fileEditor.scroll = fileEditor.cursor
	}
	if fileEditor.scroll < 0 {
		fileEditor.scroll = 0
	}
	if fileEditor.cursor >= fileEditor.scroll+visible {
		fileEditor.scroll = fileEditor.cursor - visible + 1
	}
	maxScroll := n - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if fileEditor.scroll > maxScroll {
		fileEditor.scroll = maxScroll
	}
}

// fileEditorMove walks the cursor by delta lines.
func fileEditorMove(delta int) {
	fileEditor.cursor += delta
	fileEditorClamp()
	PlayNavSound()
}

// fileEditorStartLine opens the keyboard for the focused line.
func fileEditorStartLine() {
	if !fileEditor.open || fileEditor.cursor < 0 || fileEditor.cursor >= len(fileEditor.lines) {
		return
	}
	if fileEditorReadOnlyRefuse() {
		return
	}
	fileEditor.pending = fileEditorReplaceLine
	vkOpenFileLine(fileEditor.lines[fileEditor.cursor])
}

// fileEditorStartInsert opens the keyboard for a new line after the cursor.
func fileEditorStartInsert() {
	if fileEditorReadOnlyRefuse() {
		return
	}
	fileEditor.pending = fileEditorInsertLine
	vkOpenFileLine("")
}

// fileEditorReadOnlyRefuse answers the edit keys on a read-only file with the
// reason, so an edit cannot be typed and then discarded by a save that was
// never possible.
func fileEditorReadOnlyRefuse() bool {
	if !fileEditor.readOnly {
		return false
	}
	showToast("This file is read-only", ToastWarn())
	return true
}

// fileEditorApplyLine writes the submitted line back into the buffer. It is
// called from the keyboard's submit path, after the keyboard has closed.
func fileEditorApplyLine(text string) {
	if !fileEditor.open {
		return
	}
	if fileEditorReadOnlyRefuse() {
		fileEditor.pending = fileEditorIdle
		return
	}
	switch fileEditor.pending {
	case fileEditorReplaceLine:
		if fileEditor.cursor >= 0 && fileEditor.cursor < len(fileEditor.lines) {
			fileEditor.lines[fileEditor.cursor] = text
		}
	case fileEditorInsertLine:
		at := fileEditor.cursor + 1
		if at > len(fileEditor.lines) {
			at = len(fileEditor.lines)
		}
		fileEditor.lines = append(fileEditor.lines, "")
		copy(fileEditor.lines[at+1:], fileEditor.lines[at:])
		fileEditor.lines[at] = text
		fileEditor.cursor = at
	}
	fileEditor.pending = fileEditorIdle
	fileEditorClamp()
}

// fileEditorDeleteLine removes the focused line. A file always keeps one empty
// line so the cursor has somewhere to be.
func fileEditorDeleteLine() {
	if !fileEditor.open {
		return
	}
	if fileEditorReadOnlyRefuse() {
		return
	}
	if len(fileEditor.lines) <= 1 {
		fileEditor.lines = []string{""}
		fileEditor.cursor = 0
	} else {
		fileEditor.lines = append(fileEditor.lines[:fileEditor.cursor], fileEditor.lines[fileEditor.cursor+1:]...)
	}
	fileEditorClamp()
	PlayBackSound()
}

// fileEditorValidate checks the buffer before it replaces a file the app reads.
// Today that is JSON only: a broken packages.json or jukaconfig.json would
// break the feature the user came to fix.
func fileEditorValidate(path, text string) error {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var probe interface{}
		if err := json.Unmarshal([]byte(text), &probe); err != nil {
			return fmt.Errorf("not valid JSON: %w", err)
		}
	}
	return nil
}

// fileEditorSave writes the buffer atomically, keeping the file's mode. The
// JSON check runs first, so a refused save leaves both the buffer and the file
// untouched.
func fileEditorSave() error {
	if !fileEditor.open {
		return errors.New("no file open")
	}
	text := fileEditorText()
	if err := fileEditorValidate(fileEditor.path, text); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(fileEditor.path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(fileEditor.path)
	tmp, err := os.CreateTemp(dir, ".jukahub-edit-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.WriteString(text); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, fileEditor.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	fileEditor.original = text
	fileEditor.err = ""
	return nil
}

// fileEditorRequestClose closes the editor, asking first when the buffer has
// unsaved changes.
func fileEditorRequestClose() {
	if !fileEditor.open {
		return
	}
	if fileEditorDirty() && !fileEditor.confirm {
		fileEditor.confirm = true
		PlayNavSound()
		showToast("Unsaved changes — press B again to discard", ToastWarn())
		return
	}
	fileEditorForget()
	PlayBackSound()
}

// fileEditorForget drops the buffer without asking. Scene changes and test
// teardown use it, so an editor left open somewhere cannot answer a later key.
func fileEditorForget() {
	fileEditor = fileEditorState{}
	fileEditorRowRects = nil
}

// fileEditorVisible is how many lines fit at the current screen size.
func fileEditorVisible() int {
	rows := int((screenHeight - 200) / 24)
	if rows < 4 {
		rows = 4
	}
	if rows > 22 {
		rows = 22
	}
	return rows
}

// fileEditorSaveAndReport saves and reports the outcome; shared by the pad and
// mouse paths so both behave identically.
func fileEditorSaveAndReport() bool {
	if fileEditor.readOnly {
		showToast("This file is open read-only", ToastWarn())
		return false
	}
	if err := fileEditorSave(); err != nil {
		fileEditor.err = err.Error()
		showToast("Not saved: "+err.Error(), ToastError())
		return false
	}
	fileEditor.confirm = false
	showToast("Saved "+fileEditor.title, ToastSuccess())
	return true
}

// --- Input ------------------------------------------------------------------

// handleFileEditorKey consumes every key while the editor is open, so the
// scene underneath never reacts to typing behind the overlay. The line
// keyboard owns the keys while it is up.
func handleFileEditorKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !fileEditor.open || virtualKeyboardActive {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		fileEditorMove(-1)
	case sdl.K_DOWN:
		fileEditorMove(1)
	case sdl.K_PAGEUP:
		fileEditorMove(-fileEditorVisible())
	case sdl.K_PAGEDOWN:
		fileEditorMove(fileEditorVisible())
	case sdl.K_RETURN, sdl.K_SPACE:
		fileEditorStartLine()
	case sdl.K_x, sdl.K_i:
		fileEditorStartInsert()
	case sdl.K_DELETE, sdl.K_d:
		fileEditorDeleteLine()
	case sdl.K_y, sdl.K_s:
		fileEditorSaveAndReport()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		fileEditorRequestClose()
	default:
		return true
	}
	return true
}

// ctrlFileEditor is the pad twin of handleFileEditorKey.
func ctrlFileEditor(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !fileEditor.open {
		return false
	}
	if virtualKeyboardActive {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		fileEditorMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		fileEditorMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		fileEditorMove(-fileEditorVisible())
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		fileEditorMove(fileEditorVisible())
	case sdl.CONTROLLER_BUTTON_A:
		fileEditorStartLine()
	case sdl.CONTROLLER_BUTTON_X:
		fileEditorStartInsert()
	case sdl.CONTROLLER_BUTTON_Y:
		fileEditorSaveAndReport()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		fileEditorRequestClose()
	}
	return true
}

// handleFileEditorMouse selects the clicked line and opens it for editing; a
// tap outside the card closes the editor.
func handleFileEditorMouse(mx, my int32, config *Config) bool {
	if !fileEditor.open || virtualKeyboardActive {
		return false
	}
	for i, r := range fileEditorRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			fileEditor.cursor = fileEditor.scroll + i
			fileEditorClamp()
			fileEditorStartLine()
			return true
		}
	}
	if fileEditor.rect.W > 0 && pointInRect(mx, my, fileEditor.rect) {
		return true
	}
	fileEditorRequestClose()
	return true
}

// --- Render -----------------------------------------------------------------

var fileEditorRowRects []sdl.Rect

func renderFileEditor(renderer *sdl.Renderer, config *Config) {
	if !fileEditor.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	lineH := int32(font.Height()) + 10
	visible := fileEditorVisible()
	if visible > len(fileEditor.lines) {
		visible = len(fileEditor.lines)
	}
	if visible < 1 {
		visible = 1
	}
	fileEditorClamp()

	panelW := int32(900)
	if panelW > screenWidth-40 {
		panelW = screenWidth - 40
	}
	header := UIPanelHeader(fileEditorTitle(), fileEditorSubtitle(font, panelW-2*OverlayPadX), "files")
	headerH := UIPanelHeaderBand(config, header, 10)
	footerH := 20 + int32(font.Height())
	bodyH := int32(visible) * lineH
	panelH := headerH + bodyH + footerH
	if panelH > screenHeight-32 {
		panelH = screenHeight - 32
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	fileEditor.rect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, fileEditor.rect)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, fileEditor.rect))

	const padX = OverlayPadX
	gutterW := int32(52)
	textW := panelW - 2*padX - gutterW
	bodyY := panelY + headerH
	rowW := panelW - 2*padX
	fileEditorRowRects = fileEditorRowRects[:0]

	first := fileEditor.scroll
	for i := 0; i < visible; i++ {
		idx := first + i
		if idx >= len(fileEditor.lines) {
			break
		}
		rowY := bodyY + int32(i)*lineH
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		fileEditorRowRects = append(fileEditorRowRects, rect)
		focused := idx == fileEditor.cursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(installHighlight(), 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, installHighlight())
		}
		numColor := ColorTextTertiary()
		if focused {
			numColor = ColorTextSecondary()
		}
		num := fmt.Sprintf("%d", idx+1)
		renderText(renderer, config, font, num, numColor, rect.X+8, rowY+4)
		text := fileEditor.lines[idx]
		if strings.TrimSpace(text) == "" {
			// An empty line is invisible in a list; a faint placeholder says
			// the row exists and is editable.
			renderText(renderer, config, font, "(empty line)", WithAlpha(ColorTextTertiary(), 150), rect.X+gutterW, rowY+4)
			continue
		}
		ink := ColorTextPrimary()
		if focused {
			ink = ColorTextPrimary()
		}
		renderText(renderer, config, font, ellipsize(font, text, textW), ink, rect.X+gutterW, rowY+4)
	}

	// Scroll position: a document that continues past the card has to say so.
	if n := len(fileEditor.lines); n > visible {
		status := fmt.Sprintf("%d–%d of %d", first+1, first+visible, n)
		sw, _, _ := font.SizeUTF8(status)
		renderText(renderer, config, font, status, ColorTextTertiary(), panelX+panelW-padX-int32(sw), bodyY+bodyH+4)
	}

	footer := fileEditorFooter()
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}

// fileEditorTitle is the card heading.
func fileEditorTitle() string {
	if fileEditor.confirm {
		return "Discard changes?"
	}
	return "Editing · " + fileEditor.title
}

// fileEditorSubtitle is the line under the heading: the folder, the cursor
// position and whether there are unsaved changes. The folder is dropped to fit
// before the position and the state: those are the two facts the reader needs,
// and a long SD-card path used to push them off the card.
func fileEditorSubtitle(font *ttf.Font, maxW int32) string {
	if fileEditor.confirm {
		return "B again discards the edits · A keeps editing"
	}
	state := "no changes"
	if fileEditorDirty() {
		state = "unsaved changes"
	}
	if fileEditor.readOnly {
		state = "read-only"
	}
	if fileEditor.err != "" {
		return ellipsize(font, fileEditor.err+" · line "+fmt.Sprintf("%d", fileEditor.cursor+1), maxW)
	}
	rest := fmt.Sprintf("line %d of %d · %s", fileEditor.cursor+1, len(fileEditor.lines), state)
	folder := filepath.Base(filepath.Dir(fileEditor.path))
	if folder == "" || folder == "." || folder == string(filepath.Separator) {
		folder = fileEditor.path
	}
	left := folder + " · "
	if font == nil || maxW <= 0 {
		return left + rest
	}
	if w, _, _ := font.SizeUTF8(left + rest); int32(w) <= maxW {
		return left + rest
	}
	restW, _, _ := font.SizeUTF8(rest)
	if int32(restW) >= maxW {
		return ellipsize(font, rest, maxW)
	}
	sepW, _, _ := font.SizeUTF8(" · ")
	return ellipsize(font, folder, maxW-int32(restW)-int32(sepW)-4) + " · " + rest
}

// fileEditorFooter is the hint line.
func fileEditorFooter() string {
	if fileEditor.confirm {
		return hintLine(FooterHint{"B", "Discard"}, FooterHint{"A", "Keep editing"})
	}
	// A read-only file cannot be edited or saved, so the footer must not offer
	// keys that only answer with a warning.
	if fileEditor.readOnly {
		return hintLine(FooterHint{"B", "Close"})
	}
	return hintLine(FooterHint{"A", "Edit line"}, FooterHint{"X", "Insert"},
		FooterHint{"R2", "Delete"}, FooterHint{"Y", "Save"}, FooterHint{"B", "Close"})
}
