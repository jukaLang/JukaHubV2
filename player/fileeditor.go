package main

// fileeditor.go — the built-in text viewer and editor.
//
// Files opens every file without a viewer of its own here (files_open.go), and
// Patch's "Edit packages.json" opens its index here too. It is a full-screen
// page in Home's glass: a header with the file's name and position and a
// toolbar, the text in a fixed viewport between it and the footer band, and the
// controller hints in that footer. It used to be a card whose rows were sized
// from one height and drawn at another, so a long file ran over its own footer
// and the controls could not be seen; the viewport is clipped now, and the
// header and footer are always on screen.
//
// Three modes (files_textview.go): a text file up to fileEditorMaxBytes is held
// as editable lines; a larger one is shown read-only one window at a time, so a
// 200 MB log costs a quarter of a megabyte; binary data is shown as a read-only
// hex dump with a warning instead of as garbage text. Word wrap, line numbers
// and the text size are view options (Start opens them; the toolbar chips do
// the same with a click) and are remembered with the user's settings.
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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// fileEditorMaxBytes bounds what the editor holds as editable lines. A larger
// file opens read-only and paged (filesViewWindow): handheld RAM is shared with
// the decoders, and the line list of a multi-megabyte log would cost more than
// the file itself.
const fileEditorMaxBytes = 1 << 20

// fileEditorState is the page's whole state. It is touched on the UI thread
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

	// mode is how the file is held; size is its size on disk.
	mode filesViewMode
	size int64
	// window is the part of a large or binary file on screen.
	window filesTextWindow
	// lineBase is the 1-based number of the window's first line, 0 while the
	// background count has not reached it.
	lineBase int64
	// forcedText shows a binary file as text (X in the hex view).
	forcedText bool
	notice     string
	noticeWarn bool
	hscroll    int
	// edits bumps on every change to the buffer; it keys the cached dirty flag
	// the page draws from, so a frame never joins a megabyte to compare it.
	edits      int
	dirtyAt    int
	dirty      bool
	dirtyKnown bool
	// viewLines is how many lines the last frame fitted: one page for L1/R1.
	viewLines int
	menu      filesMenu
	chips     []filesChip
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
// app had no way to look inside a config or a JSON index at all. (Any other
// file without a viewer of its own now opens here too; see files_open.go.)
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

// fileEditorTooLargeText explains why a large file opened read-only.
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
		// The cut can land inside a multi-byte character, and the partial
		// character is not binary data: about half of all Cyrillic or CJK
		// files over 8 KiB were refused as "not a text file". Up to three
		// trailing bytes of an unfinished character are dropped first.
		for i := 0; i < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); i++ {
			data = []byte(trimLastRune(string(data)))
		}
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

// fileEditorOpen opens a text file for editing (Patch's index, the Files
// sheet's Edit text). Binary data is refused here — writing a line list back
// would destroy it; A on a binary file goes through filesViewerOpen, which
// shows it as hex instead. Every refusal is explained in a toast.
func fileEditorOpen(path string) bool {
	return fileEditorOpenAs(path, false)
}

// filesViewerOpen opens any file in the page: text for editing (read-only and
// paged when large), binary data as a hex dump.
func filesViewerOpen(path string) bool {
	return fileEditorOpenAs(path, true)
}

func fileEditorOpenAs(path string, allowHex bool) bool {
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
		showToast("Cannot open "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
		return false
	}
	if info.IsDir() {
		showToast("That is a folder, not a file", ToastInfo())
		return false
	}
	binary, err := filesLooksBinary(path)
	if err != nil {
		showToast("Cannot read "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
		return false
	}
	if binary && !allowHex {
		showToast(filepath.Base(path)+" is not a text file", ToastWarn())
		return false
	}

	state := fileEditorState{
		open:  true,
		path:  path,
		title: filepath.Base(path),
		size:  info.Size(),
	}
	// A line count still running for a file opened before this one stops.
	filesLineIndexStopIfOther(path)
	switch {
	case binary:
		state.mode = filesViewHex
		state.readOnly = true
		state.notice = "Binary file — shown as hex, read-only"
		state.noticeWarn = true
		if !filesViewerLoadEdge(&state, false) {
			return false
		}
	case info.Size() > fileEditorMaxBytes:
		state.mode = filesViewWindow
		state.readOnly = true
		state.notice = fileEditorTooLargeText(info.Size()) + " · read-only, " +
			humanBytes(filesWindowBytes) + " at a time"
		// A log is opened for its newest lines, which are at the end.
		atEnd := strings.EqualFold(filepath.Ext(path), ".log")
		if !filesViewerLoadEdge(&state, atEnd) {
			return false
		}
		if atEnd {
			state.cursor = len(state.window.lines) - 1
		}
		filesLineIndexStart(path, info.Size())
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			showToast("Cannot read "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
			return false
		}
		text := string(data)
		state.mode = filesViewEdit
		state.lines = fileEditorSplit(text)
		state.original = text
		state.endsWithNewline = strings.HasSuffix(text, "\n")
		// A file the app cannot write opens read-only, so the header says so
		// and the edit keys refuse instead of accepting work that Save can
		// only throw away. The check is an open for writing: the same thing
		// the save does.
		state.readOnly = !fileEditorWritable(path)
		if state.readOnly {
			state.notice = "Read-only — JukaHub cannot write this file"
		}
	}
	fileEditor = state
	fileEditorRowRects = nil
	fileEditor.rect = sdl.Rect{}
	filesWrapCacheReset()
	// One modal at a time: the page covers the screen that opened it.
	feSheetForget()
	PlayActivateSound()
	return true
}

// filesLineIndexStopIfOther stops a background line count that belongs to a
// different file than the one opening (an edit-mode file needs none).
func filesLineIndexStopIfOther(path string) {
	filesLines.mu.Lock()
	other := filesLines.path != "" && filesLines.path != path
	filesLines.mu.Unlock()
	if other {
		filesLineIndexStop()
	}
}

// filesViewerLoadEdge loads the first (or last) window of a large or binary
// file into state.
func filesViewerLoadEdge(state *fileEditorState, atEnd bool) bool {
	hex := state.mode == filesViewHex
	start, end, err := filesEdgeWindowRange(state.path, atEnd, state.size, hex)
	if err == nil {
		var w filesTextWindow
		w, err = filesLoadWindow(state.path, start, end, hex)
		if err == nil {
			state.window = w
			state.lineBase = 0
			state.cursor, state.scroll = 0, 0
			return true
		}
	}
	showToast("Cannot read "+filepath.Base(state.path)+": "+filesFriendlyError(err), ToastError())
	return false
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
	return fileEditor.open && fileEditor.mode == filesViewEdit && fileEditorText() != fileEditor.original
}

// fileEditorDirtyCached is fileEditorDirty for the frame: it compares the
// buffer once per change instead of joining the whole file on every frame.
func fileEditorDirtyCached() bool {
	if !fileEditor.dirtyKnown || fileEditor.dirtyAt != fileEditor.edits {
		fileEditor.dirty = fileEditorDirty()
		fileEditor.dirtyAt = fileEditor.edits
		fileEditor.dirtyKnown = true
	}
	return fileEditor.dirty
}

// fileEditorChanged records an edit: the cached dirty flag and the wrapped
// rows of the page are stale.
func fileEditorChanged() {
	fileEditor.edits++
	filesWrapCacheReset()
}

// fileEditorRowCount is how many rows the page shows: lines, the rows of the
// window on screen, or hex rows.
func fileEditorRowCount() int {
	switch fileEditor.mode {
	case filesViewHex:
		if fileEditor.forcedText {
			return len(fileEditor.window.lines)
		}
		return len(fileEditor.window.hex)
	case filesViewWindow:
		return len(fileEditor.window.lines)
	}
	return len(fileEditor.lines)
}

// fileEditorRowText is the text of one row (not used for hex rows).
func fileEditorRowText(i int) string {
	if fileEditor.mode == filesViewEdit {
		if i >= 0 && i < len(fileEditor.lines) {
			return fileEditor.lines[i]
		}
		return ""
	}
	if i >= 0 && i < len(fileEditor.window.lines) {
		return fileEditor.window.lines[i]
	}
	return ""
}

// fileEditorClamp keeps the cursor and the scroll window inside the buffer.
func fileEditorClamp() {
	if fileEditor.mode == filesViewEdit && len(fileEditor.lines) == 0 {
		fileEditor.lines = []string{""}
	}
	n := fileEditorRowCount()
	if n < 1 {
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

// fileEditorMove walks the cursor by delta lines. A large or binary file's
// window follows the cursor: moving past either end loads the next window.
func fileEditorMove(delta int) {
	if fileEditor.mode != filesViewEdit {
		filesViewerMove(delta)
		return
	}
	fileEditor.cursor += delta
	fileEditorClamp()
	PlayNavSound()
}

// filesViewerMove moves through a windowed file.
func filesViewerMove(delta int) {
	target := fileEditor.cursor + delta
	n := fileEditorRowCount()
	hex := fileEditor.mode == filesViewHex && !fileEditor.forcedText
	switch {
	case target >= n && fileEditor.window.end < fileEditor.size:
		over := target - n
		if filesViewerLoad(fileEditor.window.end, true, hex) {
			fileEditor.cursor = clampIndex(over, fileEditorRowCount())
			fileEditor.scroll = fileEditor.cursor
		}
	case target < 0 && fileEditor.window.start > 0:
		if filesViewerLoad(fileEditor.window.start, false, hex) {
			fileEditor.cursor = clampIndex(fileEditorRowCount()+target, fileEditorRowCount())
			fileEditor.scroll = fileEditor.cursor
		}
	default:
		fileEditor.cursor = target
	}
	fileEditorClamp()
	PlayNavSound()
}

// filesViewerLoad loads the window after (forward) or before the offset.
func filesViewerLoad(offset int64, forward, hex bool) bool {
	var start, end int64
	var err error
	if forward {
		start, end, err = filesNextWindowRange(fileEditor.path, offset, fileEditor.size, hex)
	} else {
		start, end, err = filesPrevWindowRange(fileEditor.path, offset, hex)
	}
	if err != nil {
		return false
	}
	w, err := filesLoadWindow(fileEditor.path, start, end, hex)
	if err != nil {
		showToast("Cannot read "+fileEditor.title+": "+filesFriendlyError(err), ToastError())
		return false
	}
	fileEditor.window = w
	fileEditor.lineBase = 0
	filesWrapCacheReset()
	return true
}

// filesViewerJump goes to the top or the end of the file.
func filesViewerJump(toEnd bool) {
	if fileEditor.mode == filesViewEdit {
		if toEnd {
			fileEditor.cursor = len(fileEditor.lines) - 1
		} else {
			fileEditor.cursor = 0
		}
		fileEditorClamp()
		PlayNavSound()
		return
	}
	hex := fileEditor.mode == filesViewHex && !fileEditor.forcedText
	start, end, err := filesEdgeWindowRange(fileEditor.path, toEnd, fileEditor.size, hex)
	if err != nil {
		return
	}
	w, err := filesLoadWindow(fileEditor.path, start, end, hex)
	if err != nil {
		return
	}
	fileEditor.window = w
	fileEditor.lineBase = 0
	filesWrapCacheReset()
	fileEditor.cursor, fileEditor.scroll = 0, 0
	if toEnd {
		fileEditor.cursor = fileEditorRowCount() - 1
	}
	fileEditorClamp()
	PlayNavSound()
}

// filesViewerToggleHexText shows a binary file as text or back as hex. Some
// "binary" files are logs with a stray NUL; X is the way to read them anyway.
func filesViewerToggleHexText() {
	if fileEditor.mode != filesViewHex {
		return
	}
	fileEditor.forcedText = !fileEditor.forcedText
	hex := !fileEditor.forcedText
	start := fileEditor.window.start
	end := start + filesWindowBytes
	if hex {
		start -= start % filesHexRowBytes
		end = start + filesHexWindowBytes
	}
	if end > fileEditor.size {
		end = fileEditor.size
	}
	w, err := filesLoadWindow(fileEditor.path, start, end, hex)
	if err != nil {
		return
	}
	fileEditor.window = w
	fileEditor.cursor, fileEditor.scroll = 0, 0
	if fileEditor.forcedText {
		fileEditor.notice = "Binary file shown as text — some characters will look wrong"
	} else {
		fileEditor.notice = "Binary file — shown as hex, read-only"
	}
	filesWrapCacheReset()
	PlayNavSound()
}

// fileEditorStartLine opens the keyboard for the focused line.
func fileEditorStartLine() {
	if !fileEditor.open || fileEditor.cursor < 0 || fileEditor.cursor >= len(fileEditor.lines) {
		if fileEditor.open && fileEditor.mode != filesViewEdit {
			fileEditorReadOnlyRefuse()
		}
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
	if !fileEditor.readOnly && fileEditor.mode == filesViewEdit {
		return false
	}
	switch fileEditor.mode {
	case filesViewHex:
		showToast("Binary files are shown read-only", ToastWarn())
	case filesViewWindow:
		showToast("Large files open read-only", ToastWarn())
	default:
		showToast("This file is read-only", ToastWarn())
	}
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
	fileEditorChanged()
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
	fileEditorChanged()
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
	if fileEditor.mode != filesViewEdit {
		return errors.New("this file is shown read-only")
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
	fileEditorChanged()
	return nil
}

// fileEditorRequestClose closes the page, asking first when the buffer has
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
// teardown use it, so a page left open somewhere cannot answer a later key.
func fileEditorForget() {
	if fileEditor.mode != filesViewEdit && fileEditor.open {
		filesLineIndexStop()
	}
	fileEditor = fileEditorState{}
	fileEditorRowRects = nil
	filesWrapCacheReset()
}

// fileEditorVisible is how many lines one page holds: what the last frame
// fitted, or an estimate before the first frame.
func fileEditorVisible() int {
	if fileEditor.viewLines > 0 {
		return fileEditor.viewLines
	}
	rows := int((screenHeight - 200) / 28)
	if rows < 4 {
		rows = 4
	}
	return rows
}

// fileEditorSaveAndReport saves and reports the outcome; shared by the pad and
// mouse paths so both behave identically.
func fileEditorSaveAndReport() bool {
	if fileEditor.readOnly || fileEditor.mode != filesViewEdit {
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

// --- View options -------------------------------------------------------------

// filesTextSizes are the body text sizes the page steps through, in design px.
var filesTextSizes = []int{16, 18, 20, 22, 24, 28}

const filesTextSizeDefault = 20

// filesViewPrefBool reads a remembered view switch.
func filesViewPrefBool(key string, def bool) bool {
	if appConfig == nil || appConfig.Variables.Custom == nil {
		return def
	}
	v, ok := appConfig.Variables.Custom[key]
	if !ok {
		return def
	}
	return coerceBool(v)
}

// filesViewWrap reports whether long lines wrap (on unless switched off).
func filesViewWrap() bool { return filesViewPrefBool("fe_text_wrap", true) }

// filesViewNumbers reports whether the line-number gutter is shown.
func filesViewNumbers() bool { return filesViewPrefBool("fe_text_numbers", true) }

// filesViewTextSize is the remembered body text size.
func filesViewTextSize() int {
	size := filesTextSizeDefault
	if appConfig != nil && appConfig.Variables.Custom != nil {
		switch v := appConfig.Variables.Custom["fe_text_size"].(type) {
		case float64:
			size = int(v)
		case int:
			size = v
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				size = n
			}
		}
	}
	if size < filesTextSizes[0] || size > filesTextSizes[len(filesTextSizes)-1] {
		return filesTextSizeDefault
	}
	return size
}

// filesSetViewPref remembers a view option with the user's settings.
func filesSetViewPref(key string, value interface{}) {
	if appConfig == nil {
		return
	}
	if appConfig.Variables.Custom == nil {
		appConfig.Variables.Custom = map[string]interface{}{}
	}
	appConfig.Variables.Custom[key] = value
	saveUserConfigDebounced(userConfigFromVariables(appConfig))
	filesWrapCacheReset()
}

func filesToggleWrap() {
	on := !filesViewWrap()
	filesSetViewPref("fe_text_wrap", on)
	fileEditor.hscroll = 0
	PlayNavSound()
	if on {
		showToast("Word wrap on", ToastInfo())
	} else {
		showToast("Word wrap off — Left/Right scroll sideways", ToastInfo())
	}
}

func filesToggleNumbers() {
	filesSetViewPref("fe_text_numbers", !filesViewNumbers())
	PlayNavSound()
}

// filesStepTextSize makes the body text one step larger (+1) or smaller (-1).
func filesStepTextSize(delta int) {
	cur := filesViewTextSize()
	idx := 0
	for i, s := range filesTextSizes {
		if s == cur {
			idx = i
		}
	}
	idx = clampIndex(idx+delta, len(filesTextSizes))
	if filesTextSizes[idx] == cur {
		return
	}
	filesSetViewPref("fe_text_size", filesTextSizes[idx])
	PlayNavSound()
}

// fileEditorOpenMenu shows the View options.
func fileEditorOpenMenu() {
	fileEditor.menu = filesMenu{open: true, title: "View"}
	fileEditorMenuRows()
	PlayActivateSound()
}

// fileEditorMenuRows rebuilds the option rows from the current state.
func fileEditorMenuRows() {
	onOff := func(on bool) string {
		if on {
			return "On"
		}
		return "Off"
	}
	hex := fileEditor.mode == filesViewHex && !fileEditor.forcedText
	rows := []filesMenuRow{
		{ID: "wrap", Label: "Word wrap", Value: onOff(filesViewWrap()), Enabled: !hex},
		{ID: "numbers", Label: "Line numbers", Value: onOff(filesViewNumbers()), Enabled: !hex},
		{ID: "size", Label: "Text size", Value: strconv.Itoa(filesViewTextSize()) + " px", Adjust: true, Enabled: true},
		{ID: "top", Label: "Go to the top", Enabled: true},
		{ID: "end", Label: "Go to the end", Enabled: true},
	}
	if fileEditor.mode == filesViewHex {
		label := "Show as text"
		if fileEditor.forcedText {
			label = "Show as hex"
		}
		rows = append(rows, filesMenuRow{ID: "hex", Label: label, Enabled: true})
	}
	if filesSystemOpenAvailable() {
		rows = append(rows, filesMenuRow{ID: "system", Label: "Open with Windows", Enabled: true})
	}
	rows = append(rows, filesMenuRow{ID: "close", Label: "Close the file", Enabled: true})
	fileEditor.menu.rows = rows
}

// fileEditorMenuApply runs what a menu press asked for.
func fileEditorMenuApply(in filesMenuInput) {
	switch in.run {
	case "wrap":
		filesToggleWrap()
	case "numbers":
		filesToggleNumbers()
	case "size":
		step := in.adjust
		if step == 0 {
			step = 1
			if filesViewTextSize() >= filesTextSizes[len(filesTextSizes)-1] {
				// A press cycles back to the smallest size.
				filesSetViewPref("fe_text_size", filesTextSizes[0])
				step = 0
			}
		}
		if step != 0 {
			filesStepTextSize(step)
		}
	case "top":
		fileEditor.menu.open = false
		filesViewerJump(false)
	case "end":
		fileEditor.menu.open = false
		filesViewerJump(true)
	case "hex":
		fileEditor.menu.open = false
		filesViewerToggleHexText()
	case "system":
		fileEditor.menu.open = false
		filesOpenWithSystem(fileEditor.path)
	case "close":
		fileEditor.menu.open = false
		fileEditorRequestClose()
		return
	}
	if fileEditor.menu.open {
		fileEditorMenuRows()
	}
}

// fileEditorShoulder answers the triggers (and L1/R1 through the pad handler):
// R2 deletes the focused line of an editable file — A/X/Y/B are already
// editing keys and DELETE exists only on a keyboard the handheld does not
// have — and otherwise jumps to the end; L2 jumps to the top.
func fileEditorShoulder(s padShoulder) {
	switch s {
	case padL1:
		fileEditorMove(-fileEditorVisible())
	case padR1:
		fileEditorMove(fileEditorVisible())
	case padL2:
		filesViewerJump(false)
	case padR2:
		if fileEditor.mode == filesViewEdit {
			fileEditorDeleteLine()
		} else {
			filesViewerJump(true)
		}
	}
}

// --- Input ------------------------------------------------------------------

// handleFileEditorKey consumes every key while the page is open, so the scene
// underneath never reacts to typing behind it. The line keyboard owns the keys
// while it is up.
func handleFileEditorKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !fileEditor.open || virtualKeyboardActive {
		return false
	}
	if fileEditor.menu.open {
		fileEditorMenuApply(filesMenuKey(&fileEditor.menu, e.Keysym.Sym))
		return true
	}
	ctrl := e.Keysym.Mod&uint16(sdl.KMOD_CTRL) != 0
	switch e.Keysym.Sym {
	case sdl.K_UP:
		fileEditorMove(-1)
	case sdl.K_DOWN:
		fileEditorMove(1)
	case sdl.K_PAGEUP:
		fileEditorMove(-fileEditorVisible())
	case sdl.K_PAGEDOWN:
		fileEditorMove(fileEditorVisible())
	case sdl.K_HOME:
		filesViewerJump(false)
	case sdl.K_END:
		filesViewerJump(true)
	case sdl.K_LEFT:
		filesEditorSideways(-1)
	case sdl.K_RIGHT:
		filesEditorSideways(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		if fileEditor.confirm {
			fileEditor.confirm = false
			PlayNavSound()
			break
		}
		fileEditorStartLine()
	case sdl.K_x, sdl.K_i, sdl.K_INSERT:
		if fileEditor.mode == filesViewHex {
			filesViewerToggleHexText()
			break
		}
		fileEditorStartInsert()
	case sdl.K_DELETE, sdl.K_d:
		fileEditorDeleteLine()
	case sdl.K_y, sdl.K_s:
		if e.Keysym.Sym == sdl.K_s && !ctrl && fileEditor.mode != filesViewEdit {
			break
		}
		fileEditorSaveAndReport()
	case sdl.K_w:
		filesToggleWrap()
	case sdl.K_n, sdl.K_l:
		filesToggleNumbers()
	case sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS:
		filesStepTextSize(1)
	case sdl.K_MINUS, sdl.K_KP_MINUS:
		filesStepTextSize(-1)
	case sdl.K_m, sdl.K_o, sdl.K_TAB:
		fileEditorOpenMenu()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		fileEditorRequestClose()
	default:
		return true
	}
	return true
}

// filesEditorSideways is Left/Right: with wrap off a long line scrolls
// sideways; with wrap on there is nothing to the side, so it pages.
func filesEditorSideways(dir int) {
	if !filesViewWrap() && !(fileEditor.mode == filesViewHex && !fileEditor.forcedText) {
		fileEditor.hscroll += dir * 16
		if fileEditor.hscroll < 0 {
			fileEditor.hscroll = 0
		}
		filesWrapCacheReset()
		PlayNavSound()
		return
	}
	fileEditorMove(dir * fileEditorVisible())
}

// ctrlFileEditor is the pad twin of handleFileEditorKey.
func ctrlFileEditor(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !fileEditor.open {
		return false
	}
	if virtualKeyboardActive {
		return false
	}
	if fileEditor.menu.open {
		fileEditorMenuApply(filesMenuController(&fileEditor.menu, e.Button))
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		fileEditorMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		fileEditorMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		filesEditorSideways(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		filesEditorSideways(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		fileEditorShoulder(padL1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		fileEditorShoulder(padR1)
	case sdl.CONTROLLER_BUTTON_A:
		if fileEditor.confirm {
			// "A keeps editing", as the discard question says.
			fileEditor.confirm = false
			PlayNavSound()
			break
		}
		fileEditorStartLine()
	case sdl.CONTROLLER_BUTTON_X:
		if fileEditor.mode == filesViewHex {
			filesViewerToggleHexText()
			break
		}
		fileEditorStartInsert()
	case sdl.CONTROLLER_BUTTON_Y:
		fileEditorSaveAndReport()
	case sdl.CONTROLLER_BUTTON_START:
		fileEditorOpenMenu()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		fileEditorRequestClose()
	}
	return true
}

// handleFileEditorMouse answers a click on the page: a toolbar chip runs its
// action, a line is selected and a click on the selected line edits it.
func handleFileEditorMouse(mx, my int32, config *Config) bool {
	if !fileEditor.open || virtualKeyboardActive {
		return false
	}
	if fileEditor.menu.open {
		fileEditorMenuApply(filesMenuMouse(&fileEditor.menu, mx, my))
		return true
	}
	switch filesChipAt(fileEditor.chips, mx, my) {
	case "wrap":
		filesToggleWrap()
		return true
	case "numbers":
		filesToggleNumbers()
		return true
	case "smaller":
		filesStepTextSize(-1)
		return true
	case "larger":
		filesStepTextSize(1)
		return true
	case "hex":
		filesViewerToggleHexText()
		return true
	case "save":
		fileEditorSaveAndReport()
		return true
	case "view":
		fileEditorOpenMenu()
		return true
	case "close":
		fileEditorRequestClose()
		return true
	}
	for i, r := range fileEditorRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			line := fileEditor.scroll + i
			if line == fileEditor.cursor {
				fileEditorStartLine()
			} else {
				fileEditor.cursor = line
				fileEditorClamp()
				PlayNavSound()
			}
			return true
		}
	}
	return true
}

// fileEditorWheel scrolls the page by wheel notches (positive is away from the
// user, which scrolls up).
func fileEditorWheel(notches int32) bool {
	if !fileEditor.open || fileEditor.menu.open || virtualKeyboardActive || notches == 0 {
		return fileEditor.open
	}
	fileEditorMove(int(-notches) * 3)
	return true
}

// --- Render -------------------------------------------------------------------

var fileEditorRowRects []sdl.Rect

// filesWrapKey is everything the wrapped rows of a line depend on.
type filesWrapKey struct {
	path    string
	edits   int
	start   int64
	width   int32
	wrap    bool
	hscroll int
	text    bool
	font    *ttf.Font
}

// filesWrapCache keeps the display rows of the lines on screen, so a frame only
// copies textures: wrapping measures text, a FreeType call per try.
var filesWrapCache struct {
	key   filesWrapKey
	valid bool
	rows  map[int][]string
}

func filesWrapCacheReset() {
	filesWrapCache.valid = false
	filesWrapCache.rows = nil
}

// filesMaxDisplayRunes bounds a line drawn without wrap: past it a long line
// cannot become a texture wider than the GPU allows.
const filesMaxDisplayRunes = 4000

// filesDisplayText is a line as the page draws it: tabs as four spaces, a
// stray carriage return dropped, other control bytes as a middle dot.
func filesDisplayText(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7F {
			clean = false
			break
		}
	}
	if clean && len(s) <= filesMaxDisplayRunes {
		return s
	}
	var sb strings.Builder
	n := 0
	for _, r := range s {
		if n >= filesMaxDisplayRunes {
			sb.WriteString(" …")
			break
		}
		switch {
		case r == '\t':
			sb.WriteString("    ")
		case r == '\r':
		case r < 0x20 || r == 0x7F:
			sb.WriteRune('·')
		default:
			sb.WriteRune(r)
		}
		n++
	}
	return sb.String()
}

// filesWrapLine splits text into rows no wider than width, breaking after a
// space when one is near the cut.
func filesWrapLine(font *ttf.Font, text string, width int32, maxRows int) []string {
	if font == nil || width <= 0 || text == "" {
		return []string{text}
	}
	runes := []rune(text)
	var rows []string
	start := 0
	for start < len(runes) && len(rows) < maxRows {
		if w, _, err := font.SizeUTF8(string(runes[start:])); err == nil && int32(w) <= width {
			rows = append(rows, string(runes[start:]))
			return rows
		}
		lo, hi := start+1, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if w, _, err := font.SizeUTF8(string(runes[start:mid])); err == nil && int32(w) <= width {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		end := lo
		if end < len(runes) {
			for b := end; b > start+(end-start)/2; b-- {
				if runes[b-1] == ' ' {
					end = b
					break
				}
			}
		}
		rows = append(rows, string(runes[start:end]))
		start = end
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	return rows
}

// filesSliceRunes is up to n runes of s from rune from on.
func filesSliceRunes(s string, from, n int) string {
	if from <= 0 && len(s) <= n {
		return s
	}
	i := 0
	start := len(s)
	for pos := range s {
		if i == from {
			start = pos
			break
		}
		i++
	}
	if start >= len(s) {
		return ""
	}
	rest := s[start:]
	count := 0
	for pos := range rest {
		if count == n {
			return rest[:pos]
		}
		count++
	}
	return rest
}

// filesRowsFor returns the display rows of line idx for the current frame.
func filesRowsFor(idx int, key filesWrapKey) []string {
	if !filesWrapCache.valid || filesWrapCache.key != key {
		filesWrapCache.key = key
		filesWrapCache.valid = true
		filesWrapCache.rows = make(map[int][]string, 64)
	}
	if rows, ok := filesWrapCache.rows[idx]; ok {
		return rows
	}
	text := filesDisplayText(fileEditorRowText(idx))
	var rows []string
	if key.wrap {
		rows = filesWrapLine(key.font, text, key.width, 64)
	} else {
		rows = []string{filesSliceRunes(text, key.hscroll, 320)}
	}
	if len(filesWrapCache.rows) > 512 {
		filesWrapCache.rows = make(map[int][]string, 64)
	}
	filesWrapCache.rows[idx] = rows
	return rows
}

// filesNumberText caches the gutter's numbers, which repeat every frame.
var filesNumberText = map[int64]string{}

func filesNumberString(n int64) string {
	if s, ok := filesNumberText[n]; ok {
		return s
	}
	if len(filesNumberText) > 1024 {
		filesNumberText = map[int64]string{}
	}
	s := strconv.FormatInt(n, 10)
	filesNumberText[n] = s
	return s
}

// fileEditorBodyFont is the font the text is drawn in.
func fileEditorBodyFont(config *Config) *ttf.Font {
	if f := getFontSized(config, "small", filesViewTextSize()); f != nil {
		return f
	}
	f, _ := getCachedFont(config, "small")
	return f
}

// fileEditorIcon is the header badge's glyph.
func fileEditorIcon() string {
	switch {
	case fileEditor.mode == filesViewHex:
		return "code"
	case fileEditor.readOnly:
		return "text"
	}
	return "edit"
}

func renderFileEditor(renderer *sdl.Renderer, config *Config) {
	if !fileEditor.open || renderer == nil {
		return
	}
	font := fileEditorBodyFont(config)
	if font == nil {
		return
	}
	fileEditor.rect = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

	margin := scaleX(homeSideMargin, screenWidth)
	top := int32(14)

	// Toolbar chips first, so the header knows how much room is left.
	hexView := fileEditor.mode == filesViewHex && !fileEditor.forcedText
	chips := fileEditor.chips[:0]
	if hexView {
		chips = append(chips, filesChip{ID: "hex", Label: "Text"})
	} else {
		chips = append(chips,
			filesChip{ID: "wrap", Label: "Wrap", On: filesViewWrap()},
			filesChip{ID: "numbers", Label: "Lines", On: filesViewNumbers()},
		)
		if fileEditor.mode == filesViewHex {
			chips = append(chips, filesChip{ID: "hex", Label: "Hex"})
		}
	}
	chips = append(chips, filesChip{ID: "smaller", Label: "A−"}, filesChip{ID: "larger", Label: "A+"})
	if fileEditor.mode == filesViewEdit && !fileEditor.readOnly {
		chips = append(chips, filesChip{ID: "save", Label: "Save", On: fileEditorDirtyCached()})
	}
	chips = append(chips, filesChip{ID: "view", Label: "View"}, filesChip{ID: "close", Label: "Close"})
	chips, toolbarX := filesDrawToolbar(renderer, config, chips, screenWidth-margin, top+4, 34)
	fileEditor.chips = chips

	subFont, _ := getCachedFont(config, "small")
	subtitleInk := sdl.Color{}
	if fileEditor.confirm || fileEditor.err != "" {
		subtitleInk = WarningInk()
	}
	headerW := toolbarX - margin - 24
	y := filesDrawHeader(renderer, config, fileEditorIcon(), fileEditorTitle(),
		fileEditorSubtitle(subFont, headerW), margin, top, headerW, subtitleInk)
	y += 10
	if h := filesDrawNotice(renderer, config, fileEditor.notice, margin, y, screenWidth-2*margin, fileEditor.noticeWarn); h > 0 {
		y += h + 8
	}

	footerTop := filesDrawFooter(renderer, config, fileEditorFooterHints())
	body := sdl.Rect{X: margin, Y: y, W: screenWidth - 2*margin, H: footerTop - 12 - y}
	if body.H < 60 {
		body.H = 60
	}
	drawGlassSurface(renderer, body, glassRowRadius, glassState{})
	fileEditorClamp()
	if hexView {
		renderFilesHexBody(renderer, config, font, body)
	} else {
		renderFilesTextBody(renderer, config, font, body)
	}
	filesMenuRender(renderer, config, &fileEditor.menu)
}

// renderFilesTextBody draws the lines inside the viewport: the gutter of line
// numbers, the cursor's band, the (wrapped) text and the scrollbar.
func renderFilesTextBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, body sdl.Rect) {
	rowH := int32(font.Height()) + 4
	padX := int32(14)
	count := fileEditorRowCount()
	numbers := filesViewNumbers()
	wrap := filesViewWrap()

	// Line numbers of a window come from the background count; until it has
	// reached the window, the gutter shows the window's offset instead.
	if fileEditor.mode != filesViewEdit && fileEditor.lineBase == 0 {
		if n, ok := filesLineNumberAt(fileEditor.path, fileEditor.window.start); ok {
			fileEditor.lineBase = n
		}
	}
	gutterW := int32(0)
	if numbers {
		widest := int64(count)
		if fileEditor.mode != filesViewEdit {
			widest = fileEditor.lineBase + int64(count)
		}
		w, _ := measureText(font, filesNumberString(widest))
		gutterW = w + 24
	}
	textX := body.X + padX + gutterW
	textW := body.W - 2*padX - gutterW - 10
	key := filesWrapKey{
		path:    fileEditor.path,
		edits:   fileEditor.edits,
		start:   fileEditor.window.start,
		width:   textW,
		wrap:    wrap,
		hscroll: fileEditor.hscroll,
		text:    fileEditor.forcedText,
		font:    font,
	}

	avail := body.H - 16
	// Keep the cursor's whole line on screen.
	if fileEditor.cursor < fileEditor.scroll {
		fileEditor.scroll = fileEditor.cursor
	}
	for fileEditor.scroll < fileEditor.cursor {
		used := int32(0)
		for i := fileEditor.scroll; i <= fileEditor.cursor; i++ {
			used += int32(len(filesRowsFor(i, key))) * rowH
		}
		if used <= avail {
			break
		}
		fileEditor.scroll++
	}

	fileEditorRowRects = fileEditorRowRects[:0]
	visibleLines := 0
	y := body.Y + 8
	bottom := body.Y + body.H - 8
	renderWithClip(renderer, body.X+2, body.Y+2, body.W-4, body.H-4, func(r *sdl.Renderer) {
		for idx := fileEditor.scroll; idx < count && y < bottom; idx++ {
			rows := filesRowsFor(idx, key)
			lineH := int32(len(rows)) * rowH
			rect := sdl.Rect{X: body.X + 6, Y: y, W: body.W - 12, H: lineH}
			focused := idx == fileEditor.cursor
			if focused {
				fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(glassAccent(), 34))
				strokeRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 8, 1, WithAlpha(glassAccent(), 150))
			}
			if numbers {
				if num := fileEditorLineLabel(idx); num != "" {
					ink := ColorTextTertiary()
					if focused {
						ink = ColorTextSecondary()
					}
					nw, _ := measureText(font, num)
					filesDrawPlainText(r, config, font, num, ink, textX-16-nw, y+2)
				}
				setDrawColor(r, glassHairline())
				r.FillRect(&sdl.Rect{X: textX - 9, Y: y, W: 1, H: lineH})
			}
			rowY := y
			for i, row := range rows {
				if row == "" {
					if i == 0 && len(rows) == 1 && fileEditor.mode == filesViewEdit && focused {
						// An empty line is invisible; the focused one says it is
						// there and editable.
						filesDrawPlainText(r, config, font, "(empty line)", WithAlpha(ColorTextTertiary(), 150), textX, rowY+2)
					}
				} else {
					filesDrawPlainText(r, config, font, row, ColorTextPrimary(), textX, rowY+2)
				}
				rowY += rowH
			}
			fileEditorRowRects = append(fileEditorRowRects, rect)
			if y+lineH <= bottom {
				visibleLines++
			}
			y += lineH
		}
	})
	if visibleLines < 1 {
		visibleLines = 1
	}
	fileEditor.viewLines = visibleLines
	filesDrawBodyScrollbar(renderer, body, count, visibleLines)
}

// fileEditorLineLabel is the gutter text of row idx.
func fileEditorLineLabel(idx int) string {
	if fileEditor.mode == filesViewEdit {
		return filesNumberString(int64(idx + 1))
	}
	if idx < 0 || idx >= len(fileEditor.window.lineNo) {
		return ""
	}
	rel := fileEditor.window.lineNo[idx]
	if rel < 0 {
		return "↳"
	}
	if fileEditor.lineBase <= 0 {
		return ""
	}
	return filesNumberString(fileEditor.lineBase + int64(rel))
}

// renderFilesHexBody draws hex rows: the offset, four groups of four bytes at
// fixed columns (Inter is not monospaced, so each group gets its own column),
// and the printable characters.
func renderFilesHexBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, body sdl.Rect) {
	rowH := int32(font.Height()) + 4
	rows := fileEditor.window.hex
	count := len(rows)
	avail := body.H - 16
	visible := int(avail / rowH)
	if visible < 1 {
		visible = 1
	}
	if fileEditor.cursor < fileEditor.scroll {
		fileEditor.scroll = fileEditor.cursor
	}
	if fileEditor.cursor >= fileEditor.scroll+visible {
		fileEditor.scroll = fileEditor.cursor - visible + 1
	}
	offW, _ := measureText(font, "00000000")
	groupW, _ := measureText(font, "WW WW WW WW")
	x0 := body.X + 16
	groupX := x0 + offW + 28
	asciiX := groupX + 4*(groupW+18) + 12
	fileEditorRowRects = fileEditorRowRects[:0]
	y := body.Y + 8
	renderWithClip(renderer, body.X+2, body.Y+2, body.W-4, body.H-4, func(r *sdl.Renderer) {
		for idx := fileEditor.scroll; idx < count && idx < fileEditor.scroll+visible; idx++ {
			row := rows[idx]
			rect := sdl.Rect{X: body.X + 6, Y: y, W: body.W - 12, H: rowH}
			if idx == fileEditor.cursor {
				fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(glassAccent(), 34))
			}
			filesDrawPlainText(r, config, font, row.offset, ColorTextTertiary(), x0, y+2)
			for g := 0; g < 4; g++ {
				filesDrawPlainText(r, config, font, row.groups[g], ColorTextPrimary(), groupX+int32(g)*(groupW+18), y+2)
			}
			filesDrawPlainText(r, config, font, row.ascii, ColorTextSecondary(), asciiX, y+2)
			fileEditorRowRects = append(fileEditorRowRects, rect)
			y += rowH
		}
	})
	fileEditor.viewLines = visible
	filesDrawBodyScrollbar(renderer, body, count, visible)
}

// filesDrawBodyScrollbar marks where the view is: within the file for a
// windowed file, within the buffer otherwise.
func filesDrawBodyScrollbar(renderer *sdl.Renderer, body sdl.Rect, count, visible int) {
	if count <= 0 {
		return
	}
	thumb := float64(visible) / float64(count)
	pos := 0.0
	if count > visible {
		pos = float64(fileEditor.scroll) / float64(count-visible)
	}
	if fileEditor.mode != filesViewEdit && fileEditor.size > 0 {
		// The window is a slice of the file: place it in the whole file.
		span := float64(fileEditor.window.end - fileEditor.window.start)
		thumb = (span / float64(fileEditor.size)) * thumb
		pos = (float64(fileEditor.window.start) + pos*span) / float64(fileEditor.size)
	}
	if thumb >= 1 {
		return
	}
	drawScrollbar(renderer, body.X+body.W-8, body.Y+10, 3, body.H-20, thumb, pos)
}

// fileEditorTitle is the page heading.
func fileEditorTitle() string {
	if fileEditor.confirm {
		return "Discard changes?"
	}
	if fileEditor.mode != filesViewEdit || fileEditor.readOnly {
		return fileEditor.title
	}
	return "Editing · " + fileEditor.title
}

// fileEditorSubtitle is the line under the heading: the folder, the position
// and whether there are unsaved changes. The folder is dropped to fit before
// the position and the state: those are the two facts the reader needs, and a
// long SD-card path used to push them off the line.
func fileEditorSubtitle(font *ttf.Font, maxW int32) string {
	if fileEditor.confirm {
		return "B again discards the edits · A keeps editing"
	}
	state := "no changes"
	// The cached flag: this line is drawn every frame, and comparing a
	// megabyte buffer per frame is what the edit counter is there to avoid.
	if fileEditor.mode == filesViewEdit && fileEditorDirtyCached() {
		state = "unsaved changes"
	}
	if fileEditor.readOnly {
		state = "read-only"
	}
	if fileEditor.err != "" {
		return ellipsize(font, fileEditor.err+" · line "+fmt.Sprintf("%d", fileEditor.cursor+1), maxW)
	}
	rest := fileEditorPosition() + " · " + state
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

// fileEditorPosition says where the cursor is in the file.
func fileEditorPosition() string {
	switch fileEditor.mode {
	case filesViewEdit:
		return fmt.Sprintf("line %d of %d", fileEditor.cursor+1, len(fileEditor.lines))
	case filesViewHex:
		offset := fileEditor.window.start + int64(fileEditor.cursor)*filesHexRowBytes
		return fmt.Sprintf("offset %s of %s", humanBytes(offset), humanBytes(fileEditor.size))
	}
	pct := 0
	if fileEditor.size > 0 {
		pct = int(fileEditor.window.start * 100 / fileEditor.size)
	}
	where := fmt.Sprintf("%d%% of %s", pct, humanBytes(fileEditor.size))
	if num := fileEditorLineLabel(fileEditor.cursor); num != "" && num != "↳" {
		where = "line " + num + " · " + where
		if progress, total, done := filesLineIndexProgress(fileEditor.path); done {
			where = "line " + num + " of " + filesNumberString(total) + " · " + humanBytes(fileEditor.size)
		} else if progress > 0 {
			where += " · counting lines"
		}
	}
	return where
}

// fileEditorFooterHints are the keys the page answers in its current state.
func fileEditorFooterHints() []FooterHint {
	if fileEditor.confirm {
		return []FooterHint{{Button: "B", Label: "Discard"}, {Button: "A", Label: "Keep editing"}}
	}
	if fileEditor.mode == filesViewHex {
		toggle := "Text"
		if fileEditor.forcedText {
			toggle = "Hex"
		}
		return []FooterHint{
			{Button: "D-Pad", Label: "Scroll"},
			{Button: "L1/R1", Label: "Page"},
			{Button: "X", Label: toggle},
			{Button: "Start / Tab", Label: "View"},
			{Button: "B", Label: "Close"},
		}
	}
	// A read-only file cannot be edited or saved, so the footer must not offer
	// keys that only answer with a warning.
	if fileEditor.readOnly || fileEditor.mode != filesViewEdit {
		return []FooterHint{
			{Button: "D-Pad", Label: "Scroll"},
			{Button: "L1/R1", Label: "Page"},
			{Button: "L2/R2", Label: "Top / End"},
			{Button: "Start / Tab", Label: "View"},
			{Button: "B", Label: "Close"},
		}
	}
	return []FooterHint{
		{Button: "A", Label: "Edit line"},
		{Button: "X", Label: "Insert"},
		{Button: "R2 / Del", Label: "Delete"},
		{Button: "Y", Label: "Save"},
		{Button: "L1/R1", Label: "Page"},
		{Button: "Start / Tab", Label: "View"},
		{Button: "B", Label: "Close"},
	}
}
