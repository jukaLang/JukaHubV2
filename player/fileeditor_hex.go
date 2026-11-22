package main

// fileeditor_hex.go — the hex editor: Files → Y → Open with… → Hex editor for
// any file, the text page's View → Hex editor, and the page a binary file
// opens in (fileeditor.go is the page around it; fileeditor_hex_ui.go draws
// it and reads the pad).
//
// It shows a file as offsets, bytes and their printable characters, and it
// changes bytes where they are: the file never grows or shrinks, because a
// byte inserted into a save file or a ROM header moves everything after it and
// breaks what it was meant to fix. Editing is built for the D-pad. A on a
// byte edits its high digit, Up/Down count that digit up or down, Left/Right
// move between digits and on into the next byte, A keeps the change and B
// puts the bytes back as they were. X types a run of bytes on the on-screen
// keyboard — hex pairs, or text in quotes — and a real keyboard types hex
// digits straight in.
//
// Nothing is written until Y. The page keeps the changed bytes as patches
// over the file, never the whole file, so a 4 GB disc image costs a 64 KB
// window plus one entry per changed byte. A save first checks that the file is
// still the one that was opened (same size and time: every patch is an offset
// into that file). Then it writes a copy with the patches applied and renames
// it over the original — atomic, like the text editor's saves — or, above
// hexAtomicMax, writes just the changed bytes in place, since copying a disc
// image to change four bytes would take minutes and need its size again in
// free space.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// hexAtomicMax is the largest file a save rewrites as a copy.
	hexAtomicMax = int64(64 << 20)
	// hexUndoMax bounds the undo list; past it the oldest half is dropped
	// (those bytes stay changed, they just cannot be undone one by one).
	hexUndoMax = 20000
	// hexTypeMax bounds one run of bytes typed on the keyboard.
	hexTypeMax = 4096
)

// The line keyboard's two hex prompts; fileEditorPending's other values are
// in fileeditor.go.
const (
	fileEditorHexType fileEditorPending = 10 + iota
	fileEditorHexGoto
)

// fileEditorHex is the hex editor's part of the page's state.
type fileEditorHex struct {
	// binary: the file is not text, so "Text" shows its bytes as text
	// rather than reopening it in the text editor.
	binary bool
	// modTime is the file's time when it was opened or last saved.
	modTime time.Time
	// data is the window of the file being shown, from dataStart on.
	dataStart int64
	data      []byte
	readErr   string
	// patches are the changed bytes by offset; undo lists every change and
	// group numbers the presses that made them.
	patches map[int64]byte
	undo    []hexUndo
	group   int
	// cursor is the selected byte. editing is on between A and A (or B),
	// with nibble the digit being changed (0 high, 1 low) and editFrom the
	// length of the undo list when the edit began: B undoes back to it.
	cursor   int64
	editing  bool
	nibble   uint8
	editFrom int
	// top is the first row on screen. perRow, rows, lay, body and rowH are
	// the last frame's layout, which the pad and the mouse move through.
	top    int64
	perRow int
	rows   int
	lay    hexLayout
	body   sdl.Rect
	rowH   int32
}

// hexUndo is one byte's change: the byte before it and whether that byte
// was itself a change.
type hexUndo struct {
	off     int64
	before  byte
	patched bool
	group   int
}

// fileEditorHexSetup makes state the hex editor for its file (info is the
// file's stat). binary is whether the bytes are not text.
func fileEditorHexSetup(state *fileEditorState, info os.FileInfo, binary bool) bool {
	state.mode = filesViewHex
	state.forcedText = false
	state.readOnly = !fileEditorWritable(state.path)
	state.hex = fileEditorHex{binary: binary, modTime: info.ModTime(), perRow: 16}
	fileEditorHexNotice(state)
	if state.size > 0 {
		data, err := hexReadRange(state.path, 0, min(state.size, filesHexWindowBytes))
		if err != nil {
			showToast("Cannot read "+filepath.Base(state.path)+": "+filesFriendlyError(err), ToastError())
			return false
		}
		state.hex.data = data
	}
	return true
}

// fileEditorHexNotice is the line over the hex editor's bytes: why it cannot
// write, or, for a binary file (which A in Files opened here unasked), that
// nothing changes on the card until a save. The keys are in the footer, which
// names them for a pad or a keyboard as the case may be.
func fileEditorHexNotice(state *fileEditorState) {
	state.notice, state.noticeWarn = "", false
	switch {
	case state.readOnly:
		state.notice, state.noticeWarn = "Read-only — JukaHub cannot write this file", true
	case state.hex.binary:
		state.notice = "Binary file · changed bytes stay unsaved until you save"
	}
}

// fileEditorOpenHex opens any file in the hex editor.
func fileEditorOpenHex(path string) bool {
	path, info, ok := fileEditorResolve(path)
	if !ok {
		return false
	}
	binary, err := filesLooksBinary(path)
	if err != nil {
		showToast("Cannot read "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
		return false
	}
	state := fileEditorState{open: true, path: path, title: filepath.Base(path), size: info.Size()}
	filesLineIndexStopIfOther(path)
	if !fileEditorHexSetup(&state, info, binary) {
		return false
	}
	fileEditorShow(state)
	return true
}

// fileEditorHexMode reports whether the page is the hex editor (and not a
// binary file shown as text).
func fileEditorHexMode() bool {
	return fileEditor.open && fileEditor.mode == filesViewHex && !fileEditor.forcedText
}

// fileEditorSwitchToHex reopens the page's file in the hex editor (View →
// Hex editor). Unsaved text is saved or discarded first: the hex editor
// reads the file, not the buffer.
func fileEditorSwitchToHex() {
	if fileEditorDirty() {
		PlayErrorSound()
		showToast("Save or discard the changes first, then open the hex editor", ToastWarn())
		return
	}
	fileEditorOpenHex(fileEditor.path)
}

// fileEditorSwitchToText reopens a text file shown in the hex editor in the
// text editor; changed bytes are saved or undone first.
func fileEditorSwitchToText() {
	if fileEditorDirty() {
		PlayErrorSound()
		showToast("Save or undo the byte changes first", ToastWarn())
		return
	}
	filesViewerOpen(fileEditor.path)
}

// hexReadRange reads bytes [start, end) of path; a file that ended sooner
// gives what there is.
func hexReadRange(path string, start, end int64) ([]byte, error) {
	if end <= start {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, end-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// hexEnsureWindow makes the window cover [from, from+n) of the file (as
// much of it as exists), reading a new 64 KB window around it when it does
// not. False when the file cannot be read.
func hexEnsureWindow(from, n int64) bool {
	h := &fileEditor.hex
	size := fileEditor.size
	if from < 0 {
		from = 0
	}
	to := from + n
	if to > size {
		to = size
	}
	if from >= to {
		return true
	}
	if h.data != nil && from >= h.dataStart && to <= h.dataStart+int64(len(h.data)) {
		return true
	}
	start := from - filesHexWindowBytes/4
	if start < 0 {
		start = 0
	}
	start -= start % filesHexRowBytes
	end := start + filesHexWindowBytes
	if end < to {
		end = to
	}
	if end > size {
		end = size
	}
	data, err := hexReadRange(fileEditor.path, start, end)
	if err != nil {
		if h.readErr == "" {
			h.readErr = filesFriendlyError(err)
			showToast("Cannot read "+fileEditor.title+": "+h.readErr, ToastError())
		}
		return false
	}
	h.readErr = ""
	h.dataStart, h.data = start, data
	if got := start + int64(len(data)); got < end {
		// The file got shorter since it was opened: show what is there
		// (a save refuses it, its time has changed too).
		fileEditor.size = got
		if h.cursor >= got {
			h.cursor = max(0, got-1)
		}
	}
	return true
}

// hexDiskByte is the byte at off as the file holds it.
func hexDiskByte(off int64) (byte, bool) {
	h := &fileEditor.hex
	if off < h.dataStart || off >= h.dataStart+int64(len(h.data)) {
		if !hexEnsureWindow(off, 1) {
			return 0, false
		}
	}
	if off < h.dataStart || off >= h.dataStart+int64(len(h.data)) {
		return 0, false
	}
	return h.data[off-h.dataStart], true
}

// hexByteAt is the byte at off as the page shows it, and whether it is a
// change.
func hexByteAt(off int64) (byte, bool, bool) {
	if off < 0 || off >= fileEditor.size {
		return 0, false, false
	}
	if v, ok := fileEditor.hex.patches[off]; ok {
		return v, true, true
	}
	v, ok := hexDiskByte(off)
	return v, false, ok
}

// hexNewGroup numbers the changes of one press, which Undo takes back
// together (a typed run of bytes is one press).
func hexNewGroup() int {
	fileEditor.hex.group++
	return fileEditor.hex.group
}

// hexSetByte changes the byte at off to v and records it for undo. Setting a
// byte back to what the file holds drops its patch.
func hexSetByte(off int64, v byte, group int) bool {
	h := &fileEditor.hex
	cur, patched, ok := hexByteAt(off)
	if !ok {
		return false
	}
	if cur == v {
		return true
	}
	disk, ok := hexDiskByte(off)
	if !ok {
		return false
	}
	h.undo = append(h.undo, hexUndo{off: off, before: cur, patched: patched, group: group})
	if len(h.undo) > hexUndoMax {
		drop := len(h.undo) - hexUndoMax/2
		h.undo = append(h.undo[:0], h.undo[drop:]...)
		h.editFrom = max(0, h.editFrom-drop)
	}
	if v == disk {
		delete(h.patches, off)
	} else {
		if h.patches == nil {
			h.patches = map[int64]byte{}
		}
		h.patches[off] = v
	}
	fileEditorChanged()
	return true
}

// hexUndoPop takes back the newest change and puts the cursor on its byte.
func hexUndoPop() {
	h := &fileEditor.hex
	if len(h.undo) == 0 {
		return
	}
	e := h.undo[len(h.undo)-1]
	h.undo = h.undo[:len(h.undo)-1]
	if e.patched {
		if h.patches == nil {
			h.patches = map[int64]byte{}
		}
		h.patches[e.off] = e.before
	} else {
		delete(h.patches, e.off)
	}
	h.cursor = e.off
	if h.editFrom > len(h.undo) {
		h.editFrom = len(h.undo)
	}
}

// hexUndoLast takes back the last press's changes (Undo in the View menu,
// Ctrl+Z).
func hexUndoLast() bool {
	h := &fileEditor.hex
	if len(h.undo) == 0 {
		showToast("Nothing to undo", ToastInfo())
		return false
	}
	g := h.undo[len(h.undo)-1].group
	for len(h.undo) > 0 && h.undo[len(h.undo)-1].group == g {
		hexUndoPop()
	}
	h.editing = false
	fileEditorChanged()
	hexFollowCursor()
	PlayBackSound()
	return true
}

// hexRevertAll drops every change since the file was opened or saved.
func hexRevertAll() {
	h := &fileEditor.hex
	if len(h.patches) == 0 {
		showToast("No changes to undo", ToastInfo())
		return
	}
	h.patches, h.undo, h.editing, h.editFrom = nil, nil, false, 0
	fileEditorChanged()
	PlayBackSound()
	showToast("Every change undone", ToastInfo())
}

// hexPerRow is the bytes in one row of the last frame.
func hexPerRow() int64 {
	if fileEditor.hex.perRow == 8 {
		return 8
	}
	return 16
}

// hexFollowCursor scrolls so the cursor's row is on screen.
func hexFollowCursor() {
	h := &fileEditor.hex
	row := h.cursor / hexPerRow()
	rows := int64(max(1, h.rows))
	if row < h.top {
		h.top = row
	}
	if row >= h.top+rows {
		h.top = row - rows + 1
	}
	if h.top < 0 {
		h.top = 0
	}
}

// hexMoveBy moves the cursor by delta bytes, stopping at either end. A move
// by more than a digit keeps the byte being edited as it is and stops the
// edit.
func hexMoveBy(delta int64) {
	h := &fileEditor.hex
	if fileEditor.size <= 0 {
		return
	}
	c := min(max(h.cursor+delta, 0), fileEditor.size-1)
	if c == h.cursor {
		return
	}
	h.cursor = c
	h.editing = false
	hexFollowCursor()
	PlayNavSound()
}

// hexMoveRows is fileEditorMove in the hex editor: rows of bytes.
func hexMoveRows(rows int) {
	hexMoveBy(int64(rows) * hexPerRow())
}

// hexJump goes to the first or the last byte.
func hexJump(toEnd bool) {
	if toEnd {
		hexMoveBy(fileEditor.size)
		return
	}
	hexMoveBy(-fileEditor.size)
}

// hexGoto puts the cursor on off (Go to offset).
func hexGoto(off int64) {
	if fileEditor.size <= 0 {
		return
	}
	hexPlace(off)
	PlayActivateSound()
}

// hexPlace puts the cursor on off without a sound. Its row lands a third of
// the way down, so what comes before it shows too.
func hexPlace(off int64) {
	h := &fileEditor.hex
	h.cursor = min(max(off, 0), max(0, fileEditor.size-1))
	h.editing = false
	h.top = max(0, h.cursor/hexPerRow()-int64(max(1, h.rows))/3)
	hexFollowCursor()
}

// hexBeginEdit is A on a byte: its high digit becomes the one Up/Down
// count.
func hexBeginEdit() {
	h := &fileEditor.hex
	if fileEditorReadOnlyRefuse() {
		return
	}
	if fileEditor.size <= 0 {
		showToast("The file is empty — the hex editor changes bytes, it does not add them", ToastInfo())
		return
	}
	if _, _, ok := hexByteAt(h.cursor); !ok {
		return
	}
	h.editing, h.nibble, h.editFrom = true, 0, len(h.undo)
	PlayActivateSound()
}

// hexEndEdit keeps what the edit changed (A, Enter).
func hexEndEdit() {
	fileEditor.hex.editing = false
	PlayNavSound()
}

// hexCancelEdit puts back every byte the edit changed (B, Esc).
func hexCancelEdit() {
	h := &fileEditor.hex
	changed := len(h.undo) > h.editFrom
	for len(h.undo) > h.editFrom {
		hexUndoPop()
	}
	h.editing = false
	if changed {
		fileEditorChanged()
	}
	hexFollowCursor()
	PlayBackSound()
}

// hexStepNibble counts the digit being edited up (+1) or down (-1), F
// wrapping round to 0.
func hexStepNibble(delta int) {
	h := &fileEditor.hex
	cur, _, ok := hexByteAt(h.cursor)
	if !ok {
		return
	}
	shift := uint(4)
	if h.nibble == 1 {
		shift = 0
	}
	d := byte((int(cur>>shift&0x0F) + delta) & 0x0F)
	mask := byte(0x0F) << shift
	hexSetByte(h.cursor, cur&^mask|d<<shift, hexNewGroup())
	PlayNavSound()
}

// hexNibbleMove moves the edit to the next (+1) or previous (-1) digit, on
// into the neighbouring byte.
func hexNibbleMove(dir int) {
	h := &fileEditor.hex
	pos := h.cursor*2 + int64(h.nibble) + int64(dir)
	if pos < 0 || pos >= fileEditor.size*2 {
		return
	}
	h.cursor, h.nibble = pos/2, uint8(pos%2)
	hexFollowCursor()
	PlayNavSound()
}

// hexTypeDigit writes digit d (0–15) into the digit under the cursor and
// moves on, into the next byte after the low digit: a keyboard types bytes
// straight in, as in any hex editor.
func hexTypeDigit(d byte) {
	h := &fileEditor.hex
	if !h.editing {
		hexBeginEdit()
		if !h.editing {
			return
		}
	}
	cur, _, ok := hexByteAt(h.cursor)
	if !ok {
		return
	}
	v := cur&0xF0 | d&0x0F
	if h.nibble == 0 {
		v = d<<4 | cur&0x0F
	}
	hexSetByte(h.cursor, v, hexNewGroup())
	if h.nibble == 0 || h.cursor+1 < fileEditor.size {
		hexNibbleMove(1)
	}
}

// hexStartTyping opens the keyboard for a run of bytes written from the
// cursor on (X).
func hexStartTyping() {
	if fileEditorReadOnlyRefuse() {
		return
	}
	if fileEditor.size <= 0 {
		showToast("The file is empty — the hex editor changes bytes, it does not add them", ToastInfo())
		return
	}
	fileEditor.hex.editing = false
	fileEditor.pending = fileEditorHexType
	vkOpenFileLine("")
}

// hexStartGoto opens the keyboard for an offset to go to.
func hexStartGoto() {
	if fileEditor.size <= 0 {
		return
	}
	fileEditor.hex.editing = false
	fileEditor.pending = fileEditorHexGoto
	vkOpenFileLine("")
}

// fileEditorApplyHex takes the keyboard's text for one of the hex prompts.
// False when the keyboard was not open for one.
func fileEditorApplyHex(text string) bool {
	switch fileEditor.pending {
	case fileEditorHexType:
		fileEditor.pending = fileEditorIdle
		hexApplyTyped(text)
		return true
	case fileEditorHexGoto:
		fileEditor.pending = fileEditorIdle
		off, err := hexParseOffset(text, fileEditor.size)
		if err != nil {
			showToast(err.Error(), ToastWarn())
			return true
		}
		hexGoto(off)
		return true
	}
	return false
}

// hexApplyTyped writes the bytes X's keyboard typed from the cursor on, as
// one change for Undo, and leaves the cursor after them.
func hexApplyTyped(text string) {
	if fileEditorReadOnlyRefuse() {
		return
	}
	data, err := hexParseBytes(text)
	if err != nil {
		showToast(err.Error(), ToastWarn())
		return
	}
	h := &fileEditor.hex
	g := hexNewGroup()
	off, wrote := h.cursor, 0
	for _, b := range data {
		if off >= fileEditor.size || !hexSetByte(off, b, g) {
			break
		}
		off++
		wrote++
	}
	h.cursor = min(off, fileEditor.size-1)
	h.editing = false
	hexFollowCursor()
	if wrote < len(data) {
		showToast(fmt.Sprintf("Changed %d of %d bytes: the file ends there (the hex editor does not add bytes)", wrote, len(data)), ToastWarn())
		return
	}
	PlayActivateSound()
}

// hexDigitValue is a hex digit's value.
func hexDigitValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// hexParseBytes reads what X's keyboard typed: hex pairs ("4A 75 6B 61",
// "4a756b61", "0x4A,0x75") or text in quotes ("Juka" or 'Juka', the closing
// quote optional), which is written as its UTF-8 bytes.
func hexParseBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("Type hex bytes like 4A 75 6B 61, or text in quotes")
	}
	if q := s[0]; q == '"' || q == '\'' {
		body := s[1:]
		if len(body) > 0 && body[len(body)-1] == q {
			body = body[:len(body)-1]
		}
		switch {
		case body == "":
			return nil, errors.New("Nothing between the quotes")
		case len(body) > hexTypeMax:
			return nil, fmt.Errorf("At most %d bytes at a time", hexTypeMax)
		}
		return []byte(body), nil
	}
	digits := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == ',' || c == ':' || c == '-':
			continue
		case c == '0' && i+1 < len(s) && (s[i+1] == 'x' || s[i+1] == 'X'):
			i++
			continue
		}
		v, ok := hexDigitValue(c)
		if !ok {
			r, _ := utf8.DecodeRuneInString(s[i:])
			return nil, fmt.Errorf("%q is not a hex digit — type pairs like 4A 75, or text in quotes", r)
		}
		digits = append(digits, v)
	}
	switch {
	case len(digits) == 0:
		return nil, errors.New("Type hex bytes like 4A 75 6B 61, or text in quotes")
	case len(digits)%2 != 0:
		return nil, errors.New("An odd number of hex digits — every byte is two (0A, not A)")
	case len(digits)/2 > hexTypeMax:
		return nil, fmt.Errorf("At most %d bytes at a time", hexTypeMax)
	}
	out := make([]byte, len(digits)/2)
	for i := range out {
		out[i] = digits[2*i]<<4 | digits[2*i+1]
	}
	return out, nil
}

// hexParseOffset reads "Go to offset": hex with or without 0x (1F00,
// 0x1f00) or a share of the file ("50%"). The answer is a byte of the file.
func hexParseOffset(s string, size int64) (int64, error) {
	s = strings.TrimSpace(s)
	if size <= 0 {
		return 0, errors.New("The file is empty")
	}
	var off int64
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		v, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil || v < 0 || v > 100 {
			return 0, errors.New("A share of the file is 0% to 100%")
		}
		off = int64(float64(size-1) * v / 100)
	} else {
		t := strings.ReplaceAll(s, "_", "")
		if rest, ok := strings.CutPrefix(t, "0x"); ok {
			t = rest
		} else if rest, ok := strings.CutPrefix(t, "0X"); ok {
			t = rest
		}
		v, err := strconv.ParseUint(t, 16, 63)
		if err != nil {
			return 0, errors.New("Type the offset in hex (1F00 or 0x1F00) or a share like 50%")
		}
		off = int64(v)
	}
	return min(off, size-1), nil
}

// hexSave writes the changes (Y). It refuses a file that is no longer the
// one that was opened, since every patch is an offset into that file.
func hexSave() error {
	h := &fileEditor.hex
	if len(h.patches) == 0 {
		return nil
	}
	info, err := os.Stat(fileEditor.path)
	if err != nil {
		return err
	}
	if info.Size() != fileEditor.size || !info.ModTime().Equal(h.modTime) {
		return errors.New("the file changed on disk after it was opened — close it and open it again")
	}
	if err := hexWritePatches(fileEditor.path, h.patches, info.Size() <= hexAtomicMax); err != nil {
		return err
	}
	h.patches, h.undo, h.editing, h.editFrom = nil, nil, false, 0
	// The window is read again from the saved file.
	h.data = nil
	if info, err := os.Stat(fileEditor.path); err == nil {
		h.modTime = info.ModTime()
		fileEditor.size = info.Size()
	}
	fileEditorChanged()
	return nil
}

// hexWritePatches writes patches (offset → byte) into the file at path. With
// atomic it copies the file beside itself with the patches applied and
// renames the copy over it, keeping the file's permissions; otherwise it
// writes the bytes in place and syncs. Every patch must lie inside the file.
func hexWritePatches(path string, patches map[int64]byte, atomic bool) error {
	offs := make([]int64, 0, len(patches))
	for off := range patches {
		offs = append(offs, off)
	}
	slices.Sort(offs)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if len(offs) > 0 && (offs[0] < 0 || offs[len(offs)-1] >= info.Size()) {
		return errors.New("a changed byte lies past the end of the file")
	}
	if !atomic {
		return hexWriteInPlace(path, patches, offs)
	}
	return hexWriteCopy(path, info.Mode().Perm(), patches, offs)
}

// hexWriteInPlace writes each run of consecutive changed bytes at its offset.
func hexWriteInPlace(path string, patches map[int64]byte, offs []int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	run := make([]byte, 0, 256)
	for i := 0; i < len(offs); {
		j := i
		run = run[:0]
		for j < len(offs) && offs[j] == offs[i]+int64(j-i) {
			run = append(run, patches[offs[j]])
			j++
		}
		if _, err := f.WriteAt(run, offs[i]); err != nil {
			f.Close()
			return err
		}
		i = j
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// hexWriteCopy writes the file with the patches applied into a temporary
// file in the same folder, then renames it over the original. The original
// is closed before the rename: Windows will not replace an open file.
func hexWriteCopy(path string, mode os.FileMode, patches map[int64]byte, offs []int64) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jukahub-hex-*.tmp")
	if err != nil {
		src.Close()
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		src.Close()
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	buf := make([]byte, 256<<10)
	var pos int64
	k := 0
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			for k < len(offs) && offs[k] < pos+int64(n) {
				chunk[offs[k]-pos] = patches[offs[k]]
				k++
			}
			if _, err := tmp.Write(chunk); err != nil {
				return fail(err)
			}
			pos += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	if k < len(offs) {
		return fail(errors.New("the file got shorter while it was being saved"))
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	src.Close()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	err = root.Rename(filepath.Base(tmpName), filepath.Base(path))
	root.Close()
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// hexOffsetText is an offset as the page writes it: hex, at least eight
// digits.
func hexOffsetText(off int64, digits int) string {
	return fmt.Sprintf("%0*X", digits, off)
}

// --- The line keyboard's words for the editor's prompts (vkeyboard.go) ---

// fileEditorVKTitle is the keyboard's title while it types for the editor.
func fileEditorVKTitle() string {
	switch fileEditor.pending {
	case fileEditorInsertLine:
		return "INSERT LINE"
	case fileEditorHexType:
		return "BYTES AT " + hexOffsetText(fileEditor.hex.cursor, 8)
	case fileEditorHexGoto:
		return "GO TO OFFSET"
	}
	return fmt.Sprintf("LINE %d", fileEditor.cursor+1)
}

// fileEditorVKPlaceholder is the hint in the keyboard's empty field.
func fileEditorVKPlaceholder() string {
	switch fileEditor.pending {
	case fileEditorInsertLine:
		return "New line"
	case fileEditorHexType:
		return `Hex like 4A 75 6B 61, or "text"`
	case fileEditorHexGoto:
		return "Hex like 1F00, or 50%"
	}
	return "The line's text"
}

// fileEditorVKAction is the keyboard's submit key.
func fileEditorVKAction() string {
	switch fileEditor.pending {
	case fileEditorHexType:
		return "Apply"
	case fileEditorHexGoto:
		return "Go"
	}
	return "Save"
}

// fileEditorVKHexInput reports a hex prompt on the keyboard: no word
// suggestions (and nothing learned) for "DE AD BE EF".
func fileEditorVKHexInput() bool {
	return fileEditor.pending == fileEditorHexType || fileEditor.pending == fileEditorHexGoto
}
