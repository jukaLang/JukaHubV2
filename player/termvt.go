package main

// termvt.go — the screen of the Terminal's interactive sessions: a small VT100
// / xterm emulator in pure Go. A program's output (htop, vi, nano, less, a
// shell, an SSH session) is fed in as bytes; the result is a grid of cells
// with colours and attributes, a cursor and a bounded scrollback, which
// termpty_ui.go draws.
//
// What it understands is what programs that read TERM=xterm-256color (or
// vt100) send: C0 controls, ESC 7/8/D/E/M/c/=/>/H, the (0 line-drawing set,
// CSI cursor movement, erase, insert/delete of lines and characters, scroll
// regions, SGR with 16/256/24-bit colour, the private modes for the cursor
// keys, the alternate screen, origin mode, autowrap, cursor visibility and
// bracketed paste, device status and attribute queries (answered through
// reply), and OSC 0/2 window titles. Everything else — other OSC strings,
// DCS, APC, PM, SOS — is swallowed with a length cap, so a stray binary file
// cannot make the parser hold megabytes or index outside the grid: every
// index is clamped, and garbage only ever produces garbage on screen.
//
// Concurrency: the session's read goroutine calls Feed under mu; the render
// thread takes mu to snapshot dirty rows (termpty_ui.go). reply is called with
// mu held and must not block (it queues bytes for the program).

import (
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// vtColor is a cell colour: 0 is the terminal's default, 1..256 a palette
// index plus one, and vtColorRGB|0xRRGGBB a 24-bit colour.
type vtColor uint32

const (
	vtColorDefault vtColor = 0
	vtColorRGB     vtColor = 1 << 24
)

func vtPaletteColor(i int) vtColor {
	if i < 0 || i > 255 {
		return vtColorDefault
	}
	return vtColor(i + 1)
}

func vtTrueColor(r, g, b int) vtColor {
	clamp := func(v int) uint32 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint32(v)
	}
	return vtColorRGB | vtColor(clamp(r)<<16|clamp(g)<<8|clamp(b))
}

// Cell attributes.
const (
	vtAttrBold uint16 = 1 << iota
	vtAttrDim
	vtAttrItalic
	vtAttrUnderline
	vtAttrBlink
	vtAttrReverse
	vtAttrInvisible
	vtAttrStrike
)

// vtCell is one character cell. w is 1 for an ordinary character, 2 for the
// left half of a wide one and 0 for the right half (which draws nothing).
type vtCell struct {
	r    rune
	w    int8
	attr uint16
	fg   vtColor
	bg   vtColor
}

// vtPen is what the next printed character is drawn with.
type vtPen struct {
	attr uint16
	fg   vtColor
	bg   vtColor
}

// vtSaved is DECSC's saved state.
type vtSaved struct {
	x, y   int
	pen    vtPen
	origin bool
	g      [2]byte
	gl     int
	valid  bool
}

// Parser states.
const (
	vtGround = iota
	vtEscape
	vtEscInter
	vtCSI
	vtCSIIgnore
	vtOSC
	vtString // DCS, APC, PM, SOS: swallowed until ST
)

const (
	// vtMaxParams bounds the CSI parameter list; vtMaxParam each value.
	vtMaxParams = 32
	vtMaxParam  = 65535
	// vtMaxOSC bounds a title (or any OSC string) kept while it arrives.
	vtMaxOSC = 4096
	// vtMaxCols / vtMaxRows bound the grid whatever size is asked for.
	vtMaxCols = 400
	vtMaxRows = 200
	// vtScrollbackMax is how many lines scrolled off the main screen are kept.
	vtScrollbackMax = 1000
	// vtFeedChunk and vtFeedWork bound how long Feed holds the lock in one go:
	// at most this many bytes, or this many cell writes (a screen erase or a
	// DECALN is a whole grid), before the render thread gets a turn.
	vtFeedChunk = 4 << 10
	vtFeedWork  = 64 << 10
)

// vtTerm is one emulated terminal. Its exported behaviour is Feed, Resize and
// the snapshot helpers; all fields are guarded by mu.
type vtTerm struct {
	mu sync.Mutex

	cols, rows int
	main, alt  [][]vtCell
	altActive  bool

	x, y        int
	pendingWrap bool
	pen         vtPen
	saved       vtSaved
	savedAlt    vtSaved

	top, bot int // scroll region, inclusive

	originMode     bool
	autowrap       bool
	insertMode     bool
	newlineMode    bool
	appCursor      bool
	appKeypad      bool
	bracketedPaste bool
	cursorVisible  bool
	reverseScreen  bool
	mouseMode      int // 0 off; reported so the UI can say so, never acted on

	tabs []bool
	g    [2]byte // designated sets: 'B' ASCII, '0' DEC line drawing
	gl   int

	title string
	bell  bool

	// scrollback is a ring of lines scrolled off the main screen's top.
	scrollback [][]vtCell
	sbHead     int // index of the oldest line
	sbLen      int
	// sbPushed counts every line ever pushed, so a view anchored in the
	// scrollback can stay on the same text while new lines arrive.
	sbPushed uint64

	dirty    []bool
	dirtyAll bool
	// gen goes up with every change, so a renderer can tell nothing happened.
	gen uint64
	// work counts the cells written since Feed last let go of the lock.
	work int

	// Parser.
	state      int
	params     []int
	subParam   []bool // params[i] came after a ':' (an SGR sub-parameter)
	curParam   int
	hasParam   bool
	pendingSub bool
	private    byte
	inter      byte
	osc        []byte
	strEsc     bool // an ESC inside a string, waiting for '\'
	u8         [4]byte
	u8n        int
	u8need     int
	lastRune   rune

	// reply sends an answer (DA, DSR) back to the program. It is called with
	// mu held and must not block.
	reply func([]byte)
}

// newVTTerm makes a terminal of cols x rows.
func newVTTerm(cols, rows int, reply func([]byte)) *vtTerm {
	t := &vtTerm{reply: reply}
	t.resetLocked(cols, rows)
	return t
}

func vtClampSize(cols, rows int) (int, int) {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	if cols > vtMaxCols {
		cols = vtMaxCols
	}
	if rows > vtMaxRows {
		rows = vtMaxRows
	}
	return cols, rows
}

func vtBlankLine(cols int, pen vtPen) []vtCell {
	line := make([]vtCell, cols)
	for i := range line {
		line[i] = vtCell{r: ' ', w: 1, bg: pen.bg}
	}
	return line
}

func vtBlankScreen(cols, rows int) [][]vtCell {
	s := make([][]vtCell, rows)
	for i := range s {
		s[i] = vtBlankLine(cols, vtPen{})
	}
	return s
}

// resetLocked is RIS: everything back to power-on, scrollback kept.
func (t *vtTerm) resetLocked(cols, rows int) {
	cols, rows = vtClampSize(cols, rows)
	t.cols, t.rows = cols, rows
	t.main = vtBlankScreen(cols, rows)
	t.alt = vtBlankScreen(cols, rows)
	t.altActive = false
	t.x, t.y, t.pendingWrap = 0, 0, false
	t.pen = vtPen{}
	t.saved, t.savedAlt = vtSaved{}, vtSaved{}
	t.top, t.bot = 0, rows-1
	t.originMode, t.insertMode, t.newlineMode = false, false, false
	t.autowrap, t.cursorVisible = true, true
	t.appCursor, t.appKeypad, t.bracketedPaste, t.reverseScreen = false, false, false, false
	t.mouseMode = 0
	t.g = [2]byte{'B', 'B'}
	t.gl = 0
	t.resetTabs()
	t.dirty = make([]bool, rows)
	t.dirtyAll = true
	t.state = vtGround
	t.gen++
}

func (t *vtTerm) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for i := 8; i < t.cols; i += 8 {
		t.tabs[i] = true
	}
}

// screen is the buffer on display.
func (t *vtTerm) screen() [][]vtCell {
	if t.altActive {
		return t.alt
	}
	return t.main
}

func (t *vtTerm) markDirty(y int) {
	if y >= 0 && y < len(t.dirty) {
		t.dirty[y] = true
	}
	t.gen++
}

func (t *vtTerm) markAll() {
	t.dirtyAll = true
	t.gen++
}

// ── Feeding ─────────────────────────────────────────────────────────────

// Feed parses program output. Safe for concurrent use with the snapshots:
// the lock is let go every few kilobytes, or sooner after a lot of cell
// writes, so a flood of output never stalls the render thread for long.
func (t *vtTerm) Feed(b []byte) {
	for len(b) > 0 {
		n := min(len(b), vtFeedChunk)
		t.mu.Lock()
		t.work = 0
		i := 0
		for i < n {
			t.feedByte(b[i])
			i++
			if t.work >= vtFeedWork {
				break
			}
		}
		t.mu.Unlock()
		b = b[i:]
	}
}

func (t *vtTerm) feedByte(c byte) {
	// CAN and SUB abort any sequence; ESC starts a new one (inside a string
	// it may be the first half of ST).
	switch {
	case c == 0x18 || c == 0x1a:
		t.u8n, t.u8need = 0, 0
		t.state = vtGround
		return
	case c == 0x1b:
		if t.state == vtOSC || t.state == vtString {
			t.strEsc = true
			return
		}
		t.u8n, t.u8need = 0, 0
		t.enterEscape()
		return
	}
	switch t.state {
	case vtGround:
		t.ground(c)
	case vtEscape:
		t.escape(c)
	case vtEscInter:
		t.escInter(c)
	case vtCSI:
		t.csi(c)
	case vtCSIIgnore:
		if c < 0x20 {
			t.control(c)
		} else if c >= 0x40 && c <= 0x7e {
			t.state = vtGround
		}
	case vtOSC:
		t.oscByte(c)
	case vtString:
		if t.strEsc {
			// ESC \ ends the string; ESC and anything else abandons it and
			// starts a new sequence.
			t.strEsc = false
			t.state = vtGround
			if c != '\\' {
				t.enterEscape()
				t.feedByte(c)
			}
			return
		}
		if c == 0x07 {
			t.state = vtGround
		}
	}
}

func (t *vtTerm) enterEscape() {
	t.state = vtEscape
	t.inter = 0
	t.strEsc = false
}

func (t *vtTerm) ground(c byte) {
	if c < 0x20 || c == 0x7f {
		t.u8n, t.u8need = 0, 0
		if c != 0x7f {
			t.control(c)
		}
		return
	}
	if c < 0x80 {
		if t.u8need > 0 {
			// A broken sequence: show it as one replacement character.
			t.u8n, t.u8need = 0, 0
			t.put(utf8.RuneError)
		}
		t.put(rune(c))
		return
	}
	// UTF-8, which may arrive split across reads.
	if t.u8need == 0 {
		switch {
		case c&0xe0 == 0xc0:
			t.u8need = 2
		case c&0xf0 == 0xe0:
			t.u8need = 3
		case c&0xf8 == 0xf0:
			t.u8need = 4
		default:
			t.put(utf8.RuneError)
			return
		}
		t.u8[0] = c
		t.u8n = 1
		return
	}
	if c&0xc0 != 0x80 {
		t.u8n, t.u8need = 0, 0
		t.put(utf8.RuneError)
		t.ground(c)
		return
	}
	t.u8[t.u8n] = c
	t.u8n++
	if t.u8n < t.u8need {
		return
	}
	r, _ := utf8.DecodeRune(t.u8[:t.u8n])
	t.u8n, t.u8need = 0, 0
	t.put(r)
}

// control runs a C0 control character.
func (t *vtTerm) control(c byte) {
	switch c {
	case 0x07:
		t.bell = true
	case 0x08:
		t.pendingWrap = false
		if t.x > 0 {
			t.x--
		}
	case 0x09:
		t.tabForward(1)
	case 0x0a, 0x0b, 0x0c:
		t.lineFeed()
		if t.newlineMode {
			t.x = 0
		}
	case 0x0d:
		t.x = 0
		t.pendingWrap = false
	case 0x0e:
		t.gl = 1
	case 0x0f:
		t.gl = 0
	}
}

func (t *vtTerm) escape(c byte) {
	t.state = vtGround
	switch {
	case c >= 0x20 && c <= 0x2f:
		t.inter = c
		t.state = vtEscInter
	case c == '[':
		t.state = vtCSI
		t.params = t.params[:0]
		t.subParam = t.subParam[:0]
		t.curParam, t.hasParam, t.pendingSub = 0, false, false
		t.private, t.inter = 0, 0
	case c == ']':
		t.state = vtOSC
		t.osc = t.osc[:0]
		t.strEsc = false
	case c == 'P' || c == '_' || c == '^' || c == 'X':
		t.state = vtString
		t.strEsc = false
	case c == '7':
		t.saveCursor()
	case c == '8':
		t.restoreCursor()
	case c == 'D':
		t.lineFeed()
	case c == 'E':
		t.lineFeed()
		t.x = 0
	case c == 'M':
		t.reverseIndex()
	case c == 'H':
		if t.x >= 0 && t.x < len(t.tabs) {
			t.tabs[t.x] = true
		}
	case c == 'c':
		cols, rows := t.cols, t.rows
		t.resetLocked(cols, rows)
		t.work += 2 * cols * rows
	case c == '=':
		t.appKeypad = true
	case c == '>':
		t.appKeypad = false
	case c == 'Z':
		t.sendReply("\x1b[?1;2c")
	case c < 0x20:
		t.control(c)
		t.state = vtEscape
	}
}

func (t *vtTerm) escInter(c byte) {
	if c >= 0x20 && c <= 0x2f {
		return // more intermediates: ignored
	}
	if c < 0x20 {
		t.control(c)
		return
	}
	t.state = vtGround
	switch t.inter {
	case '(':
		t.g[0] = c
	case ')':
		t.g[1] = c
	case '#':
		if c == '8' { // DECALN: fill the screen with E
			s := t.screen()
			for y := range s {
				for x := range s[y] {
					s[y][x] = vtCell{r: 'E', w: 1}
				}
			}
			t.work += t.cols * t.rows
			t.markAll()
		}
	}
}

// ── CSI ─────────────────────────────────────────────────────────────────

func (t *vtTerm) csi(c byte) {
	switch {
	case c >= '0' && c <= '9':
		if t.curParam < vtMaxParam {
			t.curParam = t.curParam*10 + int(c-'0')
			if t.curParam > vtMaxParam {
				t.curParam = vtMaxParam
			}
		}
		t.hasParam = true
	case c == ';' || c == ':':
		t.pushParam()
		t.pendingSub = c == ':'
	case c >= '<' && c <= '?':
		if len(t.params) == 0 && !t.hasParam && t.private == 0 {
			t.private = c
		} else {
			t.state = vtCSIIgnore
		}
	case c >= 0x20 && c <= 0x2f:
		t.inter = c
	case c >= 0x40 && c <= 0x7e:
		t.pushParam()
		t.state = vtGround
		t.dispatchCSI(c)
	case c < 0x20:
		t.control(c)
	case c == 0x7f:
		// DEL is ignored inside a sequence.
	default:
		t.state = vtCSIIgnore
	}
}

// pushParam ends the parameter being read. subParam records whether it came
// after a ':' (38:2::r:g:b), which SGR needs to tell the forms apart.
func (t *vtTerm) pushParam() {
	if len(t.params) < vtMaxParams {
		t.params = append(t.params, t.curParam)
		t.subParam = append(t.subParam, t.pendingSub)
	}
	t.curParam, t.hasParam = 0, false
}

// param is parameter i, or def when it is missing or zero.
func (t *vtTerm) param(i, def int) int {
	if i < len(t.params) && t.params[i] != 0 {
		return t.params[i]
	}
	return def
}

func (t *vtTerm) dispatchCSI(c byte) {
	if t.private == '?' {
		switch c {
		case 'h':
			t.setPrivateModes(true)
		case 'l':
			t.setPrivateModes(false)
		}
		return
	}
	if t.private == '>' {
		if c == 'c' {
			// Secondary attributes: a plain VT100-class terminal, so programs
			// do not try xterm-only queries on it.
			t.sendReply("\x1b[>0;10;1c")
		}
		return
	}
	if t.private != 0 {
		return
	}
	if t.inter != 0 {
		if t.inter == '!' && c == 'p' { // DECSTR soft reset
			t.originMode, t.insertMode, t.appCursor, t.appKeypad = false, false, false, false
			t.autowrap, t.cursorVisible = true, true
			t.top, t.bot = 0, t.rows-1
			t.pen = vtPen{}
			t.g, t.gl = [2]byte{'B', 'B'}, 0
		}
		return // DECSCUSR (cursor shape) and friends: nothing to do
	}
	n := t.param(0, 1)
	switch c {
	case 'A':
		t.cursorUp(n)
	case 'B', 'e':
		t.cursorDown(n)
	case 'C', 'a':
		t.setX(t.x + n)
	case 'D':
		t.setX(t.x - n)
	case 'E':
		t.cursorDown(n)
		t.x = 0
	case 'F':
		t.cursorUp(n)
		t.x = 0
	case 'G', '`':
		t.setX(n - 1)
	case 'H', 'f':
		t.moveTo(t.param(1, 1)-1, t.param(0, 1)-1)
	case 'd':
		t.moveTo(t.x, n-1)
	case 'J':
		t.eraseDisplay(t.param(0, 0))
	case 'K':
		t.eraseLine(t.param(0, 0))
	case 'L':
		t.insertLines(n)
	case 'M':
		t.deleteLines(n)
	case 'P':
		t.deleteChars(n)
	case 'X':
		t.eraseChars(n)
	case '@':
		t.insertChars(n)
	case 'S':
		t.scrollUp(t.top, t.bot, n)
	case 'T':
		if len(t.params) <= 1 {
			t.scrollDown(t.top, t.bot, n)
		}
	case 'r':
		top, bot := t.param(0, 1)-1, t.param(1, t.rows)-1
		if bot >= t.rows {
			bot = t.rows - 1
		}
		if top >= 0 && top < bot {
			t.top, t.bot = top, bot
			t.moveTo(0, 0)
		}
	case 's':
		if len(t.params) <= 1 && t.params[0] == 0 {
			t.saveCursor()
		}
	case 'u':
		t.restoreCursor()
	case 'm':
		t.sgr()
	case 'h', 'l':
		on := c == 'h'
		for _, p := range t.params {
			switch p {
			case 4:
				t.insertMode = on
			case 20:
				t.newlineMode = on
			}
		}
	case 'n':
		switch t.param(0, 0) {
		case 5:
			t.sendReply("\x1b[0n")
		case 6:
			y := t.y
			if t.originMode {
				y -= t.top
			}
			t.sendReply("\x1b[" + strconv.Itoa(y+1) + ";" + strconv.Itoa(t.x+1) + "R")
		}
	case 'c':
		if t.param(0, 0) == 0 {
			t.sendReply("\x1b[?1;2c")
		}
	case 'g':
		switch t.param(0, 0) {
		case 0:
			if t.x >= 0 && t.x < len(t.tabs) {
				t.tabs[t.x] = false
			}
		case 3:
			for i := range t.tabs {
				t.tabs[i] = false
			}
		}
	case 'I':
		t.tabForward(n)
	case 'Z':
		t.tabBackward(n)
	case 'b':
		// REP: the last character again, at most to the end of the line
		// (curses only uses it within one).
		if t.lastRune != 0 {
			limit := t.cols - t.x
			if t.pendingWrap {
				limit = t.cols
			}
			if n > limit {
				n = limit
			}
			for i := 0; i < n; i++ {
				t.put(t.lastRune)
			}
		}
	}
}

func (t *vtTerm) setPrivateModes(on bool) {
	for _, p := range t.params {
		switch p {
		case 1:
			t.appCursor = on
		case 5:
			if t.reverseScreen != on {
				t.reverseScreen = on
				t.markAll()
			}
		case 6:
			t.originMode = on
			t.moveTo(0, 0)
		case 7:
			t.autowrap = on
			if !on {
				t.pendingWrap = false
			}
		case 25:
			t.cursorVisible = on
			t.gen++
		case 47:
			t.setAltScreen(on, false)
		case 1047:
			// Like 47, but the alternate screen is cleared on the way out.
			if !on && t.altActive {
				t.alt = vtBlankScreen(t.cols, t.rows)
				t.work += t.cols * t.rows
			}
			t.setAltScreen(on, false)
		case 1048:
			if on {
				t.saveCursor()
			} else {
				t.restoreCursor()
			}
		case 1049:
			if on {
				t.saveCursor()
				t.setAltScreen(true, true)
			} else {
				t.setAltScreen(false, false)
				t.restoreCursor()
			}
		case 2004:
			t.bracketedPaste = on
		case 9, 1000, 1002, 1003:
			if on {
				t.mouseMode = p
			} else {
				t.mouseMode = 0
			}
		}
	}
}

func (t *vtTerm) setAltScreen(on, clear bool) {
	if t.altActive == on {
		return
	}
	if on {
		t.altActive = true
		if clear {
			t.alt = vtBlankScreen(t.cols, t.rows)
		}
	} else {
		t.altActive = false
	}
	t.pendingWrap = false
	t.markAll()
}

func (t *vtTerm) sgr() {
	p := t.params
	if len(p) == 0 {
		t.pen = vtPen{}
		return
	}
	for i := 0; i < len(p); i++ {
		v := p[i]
		switch {
		case v == 0:
			t.pen = vtPen{}
		case v == 1:
			t.pen.attr |= vtAttrBold
		case v == 2:
			t.pen.attr |= vtAttrDim
		case v == 3:
			t.pen.attr |= vtAttrItalic
		case v == 4 || v == 21:
			t.pen.attr |= vtAttrUnderline
			// 4:0 is "no underline" in the colon form.
			if v == 4 && i+1 < len(p) && i+1 < len(t.subParam) && t.subParam[i+1] {
				if p[i+1] == 0 {
					t.pen.attr &^= vtAttrUnderline
				}
				i++
			}
		case v == 5 || v == 6:
			t.pen.attr |= vtAttrBlink
		case v == 7:
			t.pen.attr |= vtAttrReverse
		case v == 8:
			t.pen.attr |= vtAttrInvisible
		case v == 9:
			t.pen.attr |= vtAttrStrike
		case v == 22:
			t.pen.attr &^= vtAttrBold | vtAttrDim
		case v == 23:
			t.pen.attr &^= vtAttrItalic
		case v == 24:
			t.pen.attr &^= vtAttrUnderline
		case v == 25:
			t.pen.attr &^= vtAttrBlink
		case v == 27:
			t.pen.attr &^= vtAttrReverse
		case v == 28:
			t.pen.attr &^= vtAttrInvisible
		case v == 29:
			t.pen.attr &^= vtAttrStrike
		case v >= 30 && v <= 37:
			t.pen.fg = vtPaletteColor(v - 30)
		case v == 39:
			t.pen.fg = vtColorDefault
		case v >= 40 && v <= 47:
			t.pen.bg = vtPaletteColor(v - 40)
		case v == 49:
			t.pen.bg = vtColorDefault
		case v >= 90 && v <= 97:
			t.pen.fg = vtPaletteColor(v - 90 + 8)
		case v >= 100 && v <= 107:
			t.pen.bg = vtPaletteColor(v - 100 + 8)
		case v == 38 || v == 48 || v == 58:
			col, used := t.extendedColor(i)
			if v == 38 {
				t.pen.fg = col
			} else if v == 48 {
				t.pen.bg = col
			}
			i += used
		}
	}
}

// extendedColor reads the colour after a 38/48/58 at index i — 5;n, 2;r;g;b,
// or their colon forms (2:cs:r:g:b with a colour-space id) — and how many
// parameters it took.
func (t *vtTerm) extendedColor(i int) (vtColor, int) {
	p := t.params
	if i+1 >= len(p) {
		return vtColorDefault, len(p) - 1 - i
	}
	switch p[i+1] {
	case 5:
		if i+2 < len(p) {
			return vtPaletteColor(p[i+2]), 2
		}
		return vtColorDefault, 1
	case 2:
		j := i + 2
		// 38:2:cs:r:g:b — four sub-parameters after the 2 mean the first is
		// a colour space.
		colon := i+1 < len(t.subParam) && t.subParam[i+1]
		if colon {
			k := j
			for k < len(p) && k < len(t.subParam) && t.subParam[k] {
				k++
			}
			if k-j >= 4 {
				j++
			}
		}
		if j+2 < len(p) {
			return vtTrueColor(p[j], p[j+1], p[j+2]), j + 2 - i
		}
		return vtColorDefault, len(p) - 1 - i
	}
	return vtColorDefault, 1
}

// ── OSC ─────────────────────────────────────────────────────────────────

func (t *vtTerm) oscByte(c byte) {
	if t.strEsc {
		t.strEsc = false
		if c == '\\' {
			t.endOSC()
			return
		}
		// ESC and anything else abandons the string.
		t.osc = t.osc[:0]
		t.state = vtGround
		t.enterEscape()
		t.feedByte(c)
		return
	}
	if c == 0x07 {
		t.endOSC()
		return
	}
	if c < 0x20 {
		return
	}
	if len(t.osc) < vtMaxOSC {
		t.osc = append(t.osc, c)
	}
}

func (t *vtTerm) endOSC() {
	t.state = vtGround
	s := string(t.osc)
	t.osc = t.osc[:0]
	num, text, ok := strings.Cut(s, ";")
	if !ok || (num != "0" && num != "2") {
		return
	}
	text = strings.ToValidUTF8(text, "?")
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	if utf8.RuneCountInString(text) > 120 {
		text = string([]rune(text)[:120])
	}
	t.title = strings.TrimSpace(text)
	t.gen++
}

// ── Printing ────────────────────────────────────────────────────────────

// vtDECGraphics maps the DEC special graphics set ('(0') onto Unicode, for
// the borders of dialog, mc and older curses programs.
var vtDECGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'f': '°', 'g': '±', 'j': '┘', 'k': '┐', 'l': '┌',
	'm': '└', 'n': '┼', 'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│', 'y': '≤', 'z': '≥',
	'{': 'π', '|': '≠', '}': '£', '~': '·',
}

// vtRuneWidth is how many cells r takes: 0 for combining and zero-width
// marks (they are dropped), 2 for East Asian wide and emoji, else 1.
func vtRuneWidth(r rune) int {
	switch {
	case r < 0x300:
		return 1
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff,
		r >= 0xfe00 && r <= 0xfe0f:
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return 0
	}
	if vtWide(r) {
		return 2
	}
	return 1
}

func vtWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f,
		r == 0x2329 || r == 0x232a,
		r >= 0x2e80 && r <= 0x303e,
		r >= 0x3041 && r <= 0x33ff,
		r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff,
		r >= 0xa000 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f680 && r <= 0x1f6ff,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x1fa70 && r <= 0x1faff,
		r >= 0x20000 && r <= 0x3fffd:
		return true
	}
	return false
}

func (t *vtTerm) put(r rune) {
	if t.g[t.gl] == '0' && r >= '`' && r <= '~' {
		if m, ok := vtDECGraphics[r]; ok {
			r = m
		}
	}
	w := vtRuneWidth(r)
	if w == 0 {
		return
	}
	if w == 2 && t.cols < 2 {
		w = 1
	}
	s := t.screen()
	if t.pendingWrap && t.autowrap {
		t.x = 0
		t.lineFeed()
	}
	t.pendingWrap = false
	if w == 2 && t.x >= t.cols-1 {
		if t.autowrap {
			t.clearCell(s, t.x, t.y)
			t.x = 0
			t.lineFeed()
		} else {
			t.x = t.cols - 2
		}
	}
	t.x = vtClampInt(t.x, 0, t.cols-1)
	t.y = vtClampInt(t.y, 0, t.rows-1)
	line := s[t.y]
	if t.insertMode {
		t.shiftRight(line, t.x, w)
	}
	t.clearCell(s, t.x, t.y)
	if w == 2 {
		t.clearCell(s, t.x+1, t.y)
	}
	line[t.x] = vtCell{r: r, w: int8(w), attr: t.pen.attr, fg: t.pen.fg, bg: t.pen.bg}
	if w == 2 {
		line[t.x+1] = vtCell{r: 0, w: 0, attr: t.pen.attr, fg: t.pen.fg, bg: t.pen.bg}
	}
	t.lastRune = r
	t.work++
	t.markDirty(t.y)
	t.x += w
	if t.x >= t.cols {
		if t.autowrap {
			t.pendingWrap = true
		}
		t.x = t.cols - 1
	}
}

// clearCell blanks the cell at x and the other half of a wide character it
// belongs to, so no half of one is left behind.
func (t *vtTerm) clearCell(s [][]vtCell, x, y int) {
	if y < 0 || y >= len(s) || x < 0 || x >= len(s[y]) {
		return
	}
	line := s[y]
	switch line[x].w {
	case 0:
		if x > 0 && line[x-1].w == 2 {
			line[x-1] = vtCell{r: ' ', w: 1, bg: line[x-1].bg}
		}
	case 2:
		if x+1 < len(line) && line[x+1].w == 0 {
			line[x+1] = vtCell{r: ' ', w: 1, bg: line[x+1].bg}
		}
	}
	line[x] = vtCell{r: ' ', w: 1, bg: t.pen.bg}
}

func (t *vtTerm) shiftRight(line []vtCell, x, n int) {
	if x < 0 || x >= len(line) || n <= 0 {
		return
	}
	if n > len(line)-x {
		n = len(line) - x
	}
	copy(line[x+n:], line[x:len(line)-n])
	for i := x; i < x+n; i++ {
		line[i] = vtCell{r: ' ', w: 1, bg: t.pen.bg}
	}
	vtFixWideEdge(line)
	t.work += len(line)
}

// vtFixWideEdge blanks every half of a wide character that lost its other
// half (after a shift, a resize or a copy that cut one).
func vtFixWideEdge(line []vtCell) {
	n := len(line)
	for i := 0; i < n; i++ {
		switch line[i].w {
		case 2:
			if i+1 >= n || line[i+1].w != 0 {
				line[i] = vtCell{r: ' ', w: 1, bg: line[i].bg}
			}
		case 0:
			if i == 0 || line[i-1].w != 2 {
				line[i] = vtCell{r: ' ', w: 1, bg: line[i].bg}
			}
		}
	}
}

func vtClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ── Cursor ──────────────────────────────────────────────────────────────

func (t *vtTerm) setX(x int) {
	t.x = vtClampInt(x, 0, t.cols-1)
	t.pendingWrap = false
}

// moveTo is CUP: x, y are relative to the scroll region in origin mode.
func (t *vtTerm) moveTo(x, y int) {
	if t.originMode {
		y = vtClampInt(y+t.top, t.top, t.bot)
	} else {
		y = vtClampInt(y, 0, t.rows-1)
	}
	t.x = vtClampInt(x, 0, t.cols-1)
	t.y = y
	t.pendingWrap = false
}

func (t *vtTerm) cursorUp(n int) {
	lo := 0
	if t.y >= t.top {
		lo = t.top
	}
	t.y = vtClampInt(t.y-n, lo, t.rows-1)
	t.pendingWrap = false
}

func (t *vtTerm) cursorDown(n int) {
	hi := t.rows - 1
	if t.y <= t.bot {
		hi = t.bot
	}
	t.y = vtClampInt(t.y+n, 0, hi)
	t.pendingWrap = false
}

func (t *vtTerm) lineFeed() {
	t.pendingWrap = false
	switch {
	case t.y == t.bot:
		t.scrollUp(t.top, t.bot, 1)
	case t.y < t.rows-1:
		t.y++
	}
}

func (t *vtTerm) reverseIndex() {
	t.pendingWrap = false
	switch {
	case t.y == t.top:
		t.scrollDown(t.top, t.bot, 1)
	case t.y > 0:
		t.y--
	}
}

func (t *vtTerm) tabForward(n int) {
	t.pendingWrap = false
	for ; n > 0 && t.x < t.cols-1; n-- {
		t.x++
		for t.x < t.cols-1 && !t.tabs[t.x] {
			t.x++
		}
	}
}

func (t *vtTerm) tabBackward(n int) {
	t.pendingWrap = false
	for ; n > 0 && t.x > 0; n-- {
		t.x--
		for t.x > 0 && !t.tabs[t.x] {
			t.x--
		}
	}
}

func (t *vtTerm) saveCursor() {
	sv := vtSaved{x: t.x, y: t.y, pen: t.pen, origin: t.originMode, g: t.g, gl: t.gl, valid: true}
	if t.altActive {
		t.savedAlt = sv
	} else {
		t.saved = sv
	}
}

func (t *vtTerm) restoreCursor() {
	sv := t.saved
	if t.altActive {
		sv = t.savedAlt
	}
	if !sv.valid {
		t.moveTo(0, 0)
		t.originMode = false
		return
	}
	t.pen, t.originMode, t.g, t.gl = sv.pen, sv.origin, sv.g, sv.gl
	t.x = vtClampInt(sv.x, 0, t.cols-1)
	t.y = vtClampInt(sv.y, 0, t.rows-1)
	t.pendingWrap = false
}

// ── Scrolling and erasing ───────────────────────────────────────────────

// scrollUp moves rows top..bot up by n; lines leaving the top of the whole
// main screen go to the scrollback.
func (t *vtTerm) scrollUp(top, bot, n int) { t.scrollUpKeep(top, bot, n, true) }

// scrollUpKeep is scrollUp; keep false (DL) never feeds the scrollback.
func (t *vtTerm) scrollUpKeep(top, bot, n int, keep bool) {
	top, bot = vtClampInt(top, 0, t.rows-1), vtClampInt(bot, 0, t.rows-1)
	if top > bot || n <= 0 {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	s := t.screen()
	if keep && !t.altActive && top == 0 {
		for i := 0; i < n; i++ {
			t.pushScrollback(s[i])
		}
	}
	// The lines leaving the region are blanked and reused at its other end:
	// `cat` of a large file scrolls thousands of times a second.
	var spare [8][]vtCell
	gone := append(spare[:0:len(spare)], s[top:top+min(n, len(spare))]...)
	copy(s[top:bot+1-n], s[top+n:bot+1])
	t.refill(s, bot+1-n, bot, gone)
	for i := top; i <= bot; i++ {
		t.markDirty(i)
	}
}

// refill puts blank lines in rows lo..hi, reusing the arrays in spare.
func (t *vtTerm) refill(s [][]vtCell, lo, hi int, spare [][]vtCell) {
	for i := lo; i <= hi; i++ {
		t.work += t.cols
		k := i - lo
		if k < len(spare) && len(spare[k]) == t.cols {
			line := spare[k]
			for x := range line {
				line[x] = vtCell{r: ' ', w: 1, bg: t.pen.bg}
			}
			s[i] = line
			continue
		}
		s[i] = vtBlankLine(t.cols, t.pen)
	}
}

func (t *vtTerm) scrollDown(top, bot, n int) {
	top, bot = vtClampInt(top, 0, t.rows-1), vtClampInt(bot, 0, t.rows-1)
	if top > bot || n <= 0 {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	s := t.screen()
	var spare [8][]vtCell
	gone := append(spare[:0:len(spare)], s[bot+1-min(n, len(spare)):bot+1]...)
	copy(s[top+n:bot+1], s[top:bot+1-n])
	t.refill(s, top, top+n-1, gone)
	for i := top; i <= bot; i++ {
		t.markDirty(i)
	}
}

// pushScrollback keeps a copy of a line leaving the top of the main screen,
// without its trailing blank cells.
func (t *vtTerm) pushScrollback(line []vtCell) {
	end := len(line)
	for end > 0 && line[end-1].r == ' ' && line[end-1].bg == vtColorDefault && line[end-1].attr == 0 {
		end--
	}
	if t.scrollback == nil {
		t.scrollback = make([][]vtCell, vtScrollbackMax)
	}
	// Once the ring is full the line it drops lends its array to the new one,
	// so a long `cat` stops allocating (takeView copies, nothing else keeps
	// a scrollback line).
	slot := (t.sbHead + t.sbLen) % vtScrollbackMax
	full := t.sbLen >= vtScrollbackMax
	if full {
		slot = t.sbHead
	}
	cp := t.scrollback[slot]
	switch {
	case cap(cp) >= end:
		cp = cp[:end]
	case full:
		// A whole row's room, so the next reuse of this slot fits too.
		cp = make([]vtCell, end, max(end, t.cols))
	default:
		cp = make([]vtCell, end)
	}
	copy(cp, line[:end])
	t.scrollback[slot] = cp
	if t.sbLen < vtScrollbackMax {
		t.sbLen++
	} else {
		t.sbHead = (t.sbHead + 1) % vtScrollbackMax
	}
	t.sbPushed++
}

// scrollbackLine is line i of the scrollback, 0 the oldest.
func (t *vtTerm) scrollbackLine(i int) []vtCell {
	if i < 0 || i >= t.sbLen {
		return nil
	}
	return t.scrollback[(t.sbHead+i)%vtScrollbackMax]
}

func (t *vtTerm) clearScrollback() {
	t.scrollback = nil
	t.sbHead, t.sbLen = 0, 0
	t.markAll()
}

func (t *vtTerm) eraseRange(y, x0, x1 int) {
	s := t.screen()
	if y < 0 || y >= len(s) {
		return
	}
	x0, x1 = vtClampInt(x0, 0, t.cols), vtClampInt(x1, 0, t.cols)
	for x := x0; x < x1; x++ {
		t.clearCell(s, x, y)
	}
	t.work += x1 - x0
	t.markDirty(y)
}

func (t *vtTerm) eraseDisplay(mode int) {
	t.pendingWrap = false
	switch mode {
	case 0:
		t.eraseRange(t.y, t.x, t.cols)
		for y := t.y + 1; y < t.rows; y++ {
			t.eraseRange(y, 0, t.cols)
		}
	case 1:
		for y := 0; y < t.y; y++ {
			t.eraseRange(y, 0, t.cols)
		}
		t.eraseRange(t.y, 0, t.x+1)
	case 2:
		for y := 0; y < t.rows; y++ {
			t.eraseRange(y, 0, t.cols)
		}
	case 3:
		// xterm's "erase saved lines": the scrollback only, the screen stays
		// (clear sends 2J and 3J together).
		t.clearScrollback()
	}
}

func (t *vtTerm) eraseLine(mode int) {
	t.pendingWrap = false
	switch mode {
	case 0:
		t.eraseRange(t.y, t.x, t.cols)
	case 1:
		t.eraseRange(t.y, 0, t.x+1)
	case 2:
		t.eraseRange(t.y, 0, t.cols)
	}
}

func (t *vtTerm) eraseChars(n int) {
	t.pendingWrap = false
	t.eraseRange(t.y, t.x, t.x+n)
}

func (t *vtTerm) insertLines(n int) {
	if t.y < t.top || t.y > t.bot {
		return
	}
	t.scrollDown(t.y, t.bot, n)
	t.x, t.pendingWrap = 0, false
}

func (t *vtTerm) deleteLines(n int) {
	if t.y < t.top || t.y > t.bot {
		return
	}
	// Lines deleted inside the region never go to the scrollback.
	t.scrollUpKeep(t.y, t.bot, n, false)
	t.x, t.pendingWrap = 0, false
}

func (t *vtTerm) deleteChars(n int) {
	t.pendingWrap = false
	s := t.screen()
	if t.y < 0 || t.y >= len(s) {
		return
	}
	line := s[t.y]
	x := vtClampInt(t.x, 0, t.cols-1)
	if n > t.cols-x {
		n = t.cols - x
	}
	if n <= 0 {
		return
	}
	copy(line[x:], line[x+n:])
	for i := t.cols - n; i < t.cols; i++ {
		line[i] = vtCell{r: ' ', w: 1, bg: t.pen.bg}
	}
	vtFixWideEdge(line)
	t.work += t.cols
	t.markDirty(t.y)
}

func (t *vtTerm) insertChars(n int) {
	t.pendingWrap = false
	s := t.screen()
	if t.y < 0 || t.y >= len(s) {
		return
	}
	t.shiftRight(s[t.y], vtClampInt(t.x, 0, t.cols-1), n)
	t.markDirty(t.y)
}

func (t *vtTerm) sendReply(s string) {
	if t.reply != nil {
		t.reply([]byte(s))
	}
}

// ── Size ────────────────────────────────────────────────────────────────

// Resize changes the grid. Lines are cut or padded, not re-wrapped; when the
// main screen loses rows the top ones go to the scrollback so the cursor's
// line stays on screen.
func (t *vtTerm) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cols, rows = vtClampSize(cols, rows)
	if cols == t.cols && rows == t.rows {
		return
	}
	fit := func(s [][]vtCell, keepBottom bool, cursorY int) ([][]vtCell, int) {
		shift := 0
		if keepBottom && rows < len(s) && cursorY >= rows {
			shift = cursorY - rows + 1
			for i := 0; i < shift; i++ {
				t.pushScrollback(s[i])
			}
			s = s[shift:]
		}
		out := make([][]vtCell, rows)
		for y := 0; y < rows; y++ {
			line := vtBlankLine(cols, vtPen{})
			if y < len(s) {
				copy(line, s[y])
				vtFixWideEdge(line)
			}
			out[y] = line
		}
		return out, shift
	}
	var shift int
	if t.altActive {
		t.main, _ = fit(t.main, false, 0)
		t.alt, shift = fit(t.alt, false, 0)
	} else {
		t.main, shift = fit(t.main, true, t.y)
		t.alt, _ = fit(t.alt, false, 0)
	}
	t.y -= shift
	t.cols, t.rows = cols, rows
	t.top, t.bot = 0, rows-1
	t.x = vtClampInt(t.x, 0, cols-1)
	t.y = vtClampInt(t.y, 0, rows-1)
	t.pendingWrap = false
	t.resetTabs()
	t.dirty = make([]bool, rows)
	t.markAll()
}

// ── Reading (for the renderer and the session) ───────────────────────────

// vtView is what the renderer needs of one frame. Changed lists the rows
// whose cells changed since the last view (all of them when Full).
type vtView struct {
	Cols, Rows     int
	Full           bool
	Changed        []int
	Cells          [][]vtCell // only the changed rows are filled
	CursorX        int
	CursorY        int
	CursorVisible  bool
	AltScreen      bool
	AppCursor      bool
	BracketedPaste bool
	ReverseScreen  bool
	Title          string
	Bell           bool
	ScrollbackLen  int
	Gen            uint64
	Offset         int
}

// takeView copies the rows that changed since the last call into v (reusing
// its buffers). offset > 0 shows the screen that many lines back in the
// scrollback; then every row is copied whenever the view moved or anything
// changed, and none while nothing did.
func (t *vtTerm) takeView(v *vtView, offset int, forceFull bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if offset > t.sbLen {
		offset = t.sbLen
	}
	if t.altActive || offset < 0 {
		offset = 0
	}
	full := forceFull || t.dirtyAll || v.Cols != t.cols || v.Rows != t.rows || offset != v.Offset ||
		(offset > 0 && t.gen != v.Gen)
	v.Offset = offset
	v.Cols, v.Rows, v.Full = t.cols, t.rows, full
	if len(v.Cells) != t.rows {
		v.Cells = make([][]vtCell, t.rows)
	}
	v.Changed = v.Changed[:0]
	s := t.screen()
	for y := 0; y < t.rows; y++ {
		if !full && (offset > 0 || !t.dirty[y]) {
			continue
		}
		var src []vtCell
		if offset > 0 {
			if idx := t.sbLen - offset + y; idx < t.sbLen {
				src = t.scrollbackLine(idx)
			} else {
				src = s[idx-t.sbLen]
			}
		} else {
			src = s[y]
		}
		dst := v.Cells[y]
		if cap(dst) < t.cols {
			dst = make([]vtCell, t.cols)
		}
		dst = dst[:t.cols]
		n := copy(dst, src)
		for i := n; i < t.cols; i++ {
			dst[i] = vtCell{r: ' ', w: 1}
		}
		v.Cells[y] = dst
		v.Changed = append(v.Changed, y)
		t.dirty[y] = false
	}
	t.dirtyAll = false
	v.CursorX, v.CursorY = t.x, t.y
	v.CursorVisible = t.cursorVisible && offset == 0
	v.AltScreen, v.AppCursor, v.BracketedPaste = t.altActive, t.appCursor, t.bracketedPaste
	v.ReverseScreen = t.reverseScreen
	v.Title = t.title
	v.Bell = t.bell
	t.bell = false
	v.ScrollbackLen = t.sbLen
	v.Gen = t.gen
}

// textLines is the main screen as plain text, trailing blank lines dropped:
// what the line terminal keeps when a session ends.
func (t *vtTerm) textLines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, t.rows)
	var b strings.Builder
	for _, line := range t.main {
		b.Reset()
		for _, c := range line {
			if c.w == 0 {
				continue
			}
			if c.r == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(c.r)
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// modes reports what the input encoder needs.
func (t *vtTerm) modes() (appCursor, appKeypad, bracketedPaste, newline, alt bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.appCursor, t.appKeypad, t.bracketedPaste, t.newlineMode, t.altActive
}

// size is the grid size.
func (t *vtTerm) size() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// sbPushedCount is how many lines have ever gone to the scrollback.
func (t *vtTerm) sbPushedCount() (uint64, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sbPushed, t.sbLen
}

// feedString is Feed for a message JukaHub itself writes on the screen.
func (t *vtTerm) feedString(s string) { t.Feed([]byte(s)) }
