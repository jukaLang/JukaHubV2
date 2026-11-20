package main

// termpty_ui.go — the screen and the controls of an interactive session
// (termpty.go). The emulator's grid (termvt.go) fills the screen under a slim
// header; rows are composed on the CPU into one surface — backgrounds, then
// each glyph from a per-character cache, centred in its cell so even a
// proportional fallback font keeps columns straight — and only the rows that
// changed are uploaded to one streaming texture. Box drawing, block elements
// and braille are drawn as shapes, so htop's meters and mc's frames join up
// whatever the font has.
//
// The text face is a monospace font when one is on the system (DejaVu Sans
// Mono, Liberation Mono, Noto Sans Mono, Consolas, Cascadia Mono …, or any
// *Mono*.ttf copied into JukaHub's fonts folder), else the UI face.
//
// Pad (no keyboard on the handheld): D-pad arrows · A Enter · B Esc (hold B
// to leave; the session keeps running) · X Backspace · Y on-screen keyboard,
// each character sent as it is typed · L1/R1 PgUp/PgDn in a full-screen
// program, scrollback on the shell's screen · L2 Tab · R2 Ctrl for the next
// key · Select the special-keys strip · Start the session menu.
// Keyboard: every key goes to the program, F-keys and Ctrl+Q included;
// Ctrl+Shift+Q leaves (and resumes from the line terminal), Ctrl+Shift+M the
// menu, Ctrl+Shift+V pastes, Shift+PgUp/PgDn scroll back, Ctrl+Shift+Plus /
// Minus change the text size. The wheel scrolls back.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	termPtyHoldLeave  = 800 * time.Millisecond
	termPtyVKPad      = "    " // spaces the on-screen keyboard can delete into
	termPtyVKMaxRunes = 48
	termPtyGlyphMax   = 1500
	termPtyPasteMax   = 64 << 10
)

// termPtyStripKeys is the special-keys strip (Select): what a pad cannot type.
var termPtyStripKeys = []struct {
	label string
	bytes string
	key   termPtyKey // used when bytes is ""
}{
	{"Esc", "\x1b", 0}, {"Tab", "\t", 0}, {"Ctrl+C", "\x03", 0}, {"Ctrl+D", "\x04", 0},
	{"Ctrl+Z", "\x1a", 0}, {"Ctrl+X", "\x18", 0}, {"Ctrl+O", "\x0f", 0}, {"Ctrl+L", "\x0c", 0},
	{"Ctrl+R", "\x12", 0}, {"Home", "", tpkHome}, {"End", "", tpkEnd}, {"PgUp", "", tpkPgUp},
	{"PgDn", "", tpkPgDn}, {"F1", "", tpkF1}, {"F2", "", tpkF1 + 1}, {"F3", "", tpkF1 + 2},
	{"F4", "", tpkF1 + 3}, {"F5", "", tpkF1 + 4}, {"F6", "", tpkF1 + 5}, {"F7", "", tpkF1 + 6},
	{"F8", "", tpkF1 + 7}, {"F9", "", tpkF1 + 8}, {"F10", "", tpkF1 + 9}, {"F11", "", tpkF1 + 10},
	{"F12", "", tpkF1 + 11}, {"q", "q", 0}, {":wq ⏎", "\x1b:wq\r", 0},
}

// Session menu rows (Start).
const (
	tpmLeave = iota
	tpmEnd
	tpmPaste
	tpmSmaller
	tpmLarger
	tpmHelp
	tpmCount
)

// termPtyUI is the screen's state. Main thread only.
var termPtyUI struct {
	// Overlays.
	menuOpen  bool
	menuSel   int
	stripOpen bool
	stripSel  int
	helpOpen  bool
	// Pad state.
	stickyCtrl bool
	bDown      time.Time
	// The on-screen keyboard typing into the session.
	vkOpen        bool
	vkSeen        string
	vkSavedInput  string
	vkSavedCursor int
	// A Ctrl chord sent from the key event: SDL may still send its text.
	swallowText bool
	swallowAt   uint64
	// Left Alt with a key: the ESC prefix waits for the key's text (so
	// Alt+Shift+1 is ESC !), or sends metaFallback when no text comes.
	metaPending  bool
	metaAt       uint64
	metaFallback [2]byte
	// The program reads a password (echo off), checked a few times a second.
	secret   bool
	secretAt uint64
	// Scrollback view: lines back from the live screen.
	sbOffset   int
	lastPushed uint64
	// Text size (0 = default for this screen).
	fontSize int
	// Hit boxes of the last frame.
	menuRects  []sdl.Rect
	stripRects []sdl.Rect
	resumeRect sdl.Rect
	headerRect sdl.Rect
	gridRect   sdl.Rect
}

// termPtyRender holds the drawing resources.
var termPtyRender struct {
	size       int
	font, bold *ttf.Font
	ownFonts   bool
	mono       bool
	cw, ch     int32
	glyphs     map[termPtyGlyphKey]*sdl.Surface
	surf       *sdl.Surface
	tex        *sdl.Texture
	texW, texH int32
	view       vtView
	needFull   bool
	lastFull   time.Time
	monoPath   string
	monoProbed bool
}

type termPtyGlyphKey struct {
	r    rune
	bold bool
}

// ── Fonts and metrics ────────────────────────────────────────────────────

func termPtyDefaultFontSize() int {
	if screenWidth >= 1200 {
		return 18
	}
	return 16
}

func termPtyFontSize() int {
	if termPtyUI.fontSize <= 0 {
		return termPtyDefaultFontSize()
	}
	return termPtyUI.fontSize
}

// termPtyMonoFontPath finds a monospace face once: JukaHub's fonts folder
// first (a *Mono*.ttf the user copied or downloaded), then the system's.
func termPtyMonoFontPath() string {
	r := &termPtyRender
	if r.monoProbed {
		return r.monoPath
	}
	r.monoProbed = true
	var cands []string
	if dir := uiFontDir(); dir != "" {
		for _, pat := range []string{"*Mono*.ttf", "*mono*.ttf"} {
			if m, err := filepath.Glob(filepath.Join(dir, pat)); err == nil {
				cands = append(cands, m...)
			}
		}
	}
	if runtime.GOOS == "windows" {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		for _, f := range []string{"CascadiaMono.ttf", "consola.ttf", "lucon.ttf", "cour.ttf"} {
			cands = append(cands, filepath.Join(win, "Fonts", f))
		}
	} else {
		rels := []string{
			"usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
			"usr/share/fonts/dejavu/DejaVuSansMono.ttf",
			"usr/share/fonts/TTF/DejaVuSansMono.ttf",
			"usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
			"usr/share/fonts/truetype/liberation2/LiberationMono-Regular.ttf",
			"usr/share/fonts/truetype/noto/NotoSansMono-Regular.ttf",
			"usr/share/fonts/truetype/ubuntu/UbuntuMono-R.ttf",
		}
		roots := []string{"/"}
		if linuxPkgSupported() {
			if lay, err := linuxPkgDirs(); err == nil {
				roots = append(roots, lay.root)
			}
		}
		for _, root := range roots {
			for _, rel := range rels {
				cands = append(cands, filepath.Join(root, filepath.FromSlash(rel)))
			}
		}
	}
	for _, p := range cands {
		if regularFile(p) {
			r.monoPath = p
			break
		}
	}
	return r.monoPath
}

// termPtyEnsureFonts opens the faces for the current size and measures the
// cell. False when no font could be opened at all.
func termPtyEnsureFonts(config *Config) bool {
	r := &termPtyRender
	size := termPtyFontSize()
	if r.font != nil && r.size == size {
		return true
	}
	termPtyFreeFonts()
	r.size = size
	if p := termPtyMonoFontPath(); p != "" {
		if f, err := ttf.OpenFont(p, size); err == nil {
			r.font, r.ownFonts, r.mono = f, true, true
			if b, err := ttf.OpenFont(p, size); err == nil {
				b.SetStyle(ttf.STYLE_BOLD)
				r.bold = b
			} else {
				r.bold = f
			}
		} else {
			logWarn("terminal", "monospace font %s: %v", p, err)
		}
	}
	if r.font == nil {
		f := getFontSized(config, "small", size)
		if f == nil {
			return false
		}
		r.font, r.bold, r.ownFonts, r.mono = f, f, false, false
	}
	adv := func(ch rune) int32 {
		if m, err := r.font.GlyphMetrics(ch); err == nil && m != nil {
			return int32(m.Advance)
		}
		return 0
	}
	r.cw = adv('M')
	if w := adv('0'); w > r.cw {
		r.cw = w
	}
	if r.cw < 4 {
		r.cw = int32(size)*6/10 + 1
	}
	r.ch = int32(r.font.Height())
	if r.ch < 8 {
		r.ch = int32(size) + 4
	}
	r.needFull = true
	return true
}

func termPtyFreeFonts() {
	r := &termPtyRender
	for k, s := range r.glyphs {
		s.Free()
		delete(r.glyphs, k)
	}
	if r.ownFonts {
		if r.bold != nil && r.bold != r.font {
			r.bold.Close()
		}
		if r.font != nil {
			r.font.Close()
		}
	}
	r.font, r.bold, r.ownFonts = nil, nil, false
}

// termPtyFreeSurfaces drops the grid's surface and texture.
func termPtyFreeSurfaces() {
	r := &termPtyRender
	if r.tex != nil {
		_ = r.tex.Destroy()
		r.tex = nil
	}
	if r.surf != nil {
		r.surf.Free()
		r.surf = nil
	}
	r.texW, r.texH = 0, 0
	r.view = vtView{}
}

// termPtyLayout is the header band and the grid's area.
func termPtyLayout() (sdl.Rect, sdl.Rect) {
	margin := int32(10)
	top := headerBarHeight() + 4
	bottom := screenHeight - bottomBarHeight("Terminal") - 6
	header := sdl.Rect{X: margin, Y: top, W: screenWidth - 2*margin, H: 30}
	gy := header.Y + header.H + 4
	grid := sdl.Rect{X: margin, Y: gy, W: screenWidth - 2*margin, H: bottom - gy}
	if grid.H < 40 {
		grid.H = 40
	}
	return header, grid
}

// termPtyGridSize is how many cells fit the screen now.
func termPtyGridSize(config *Config) (int, int) {
	if config == nil || !termPtyEnsureFonts(config) {
		return 80, 24
	}
	_, grid := termPtyLayout()
	pad := int32(8)
	cols := int((grid.W - 2*pad) / termPtyRender.cw)
	rows := int((grid.H - 2*pad) / termPtyRender.ch)
	return vtClampSize(cols, rows)
}

// ── Colours ──────────────────────────────────────────────────────────────

var (
	termPtyDefaultFG = sdl.Color{R: 0xdf, G: 0xe3, B: 0xe8, A: 255}
	termPtyDefaultBG = sdl.Color{R: 0x0f, G: 0x11, B: 0x17, A: 255}
	termPtyPalette16 = [16]sdl.Color{
		{R: 0x23, G: 0x26, B: 0x2e, A: 255}, {R: 0xe0, G: 0x52, B: 0x5a, A: 255},
		{R: 0x4f, G: 0xbf, B: 0x6f, A: 255}, {R: 0xd8, G: 0xb0, B: 0x4a, A: 255},
		{R: 0x4f, G: 0x8f, B: 0xe0, A: 255}, {R: 0xb4, G: 0x6a, B: 0xd8, A: 255},
		{R: 0x3f, G: 0xb6, B: 0xc4, A: 255}, {R: 0xc8, G: 0xcc, B: 0xd2, A: 255},
		{R: 0x6b, G: 0x72, B: 0x80, A: 255}, {R: 0xff, G: 0x6b, B: 0x72, A: 255},
		{R: 0x6e, G: 0xe0, B: 0x8a, A: 255}, {R: 0xf2, G: 0xd0, B: 0x67, A: 255},
		{R: 0x74, G: 0xa9, B: 0xff, A: 255}, {R: 0xd0, G: 0x8c, B: 0xf2, A: 255},
		{R: 0x63, G: 0xd3, B: 0xdf, A: 255}, {R: 0xf2, G: 0xf4, B: 0xf7, A: 255},
	}
)

// termPtyColor turns a cell colour into RGB; def is the terminal default.
func termPtyColor(c vtColor, def sdl.Color) sdl.Color {
	switch {
	case c == vtColorDefault:
		return def
	case c&vtColorRGB != 0:
		v := uint32(c &^ vtColorRGB)
		return sdl.Color{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
	}
	i := int(c) - 1
	switch {
	case i < 16:
		return termPtyPalette16[i]
	case i < 232:
		i -= 16
		level := func(v int) uint8 {
			if v == 0 {
				return 0
			}
			return uint8(55 + v*40)
		}
		return sdl.Color{R: level(i / 36), G: level(i / 6 % 6), B: level(i % 6), A: 255}
	default:
		g := uint8(8 + (i-232)*10)
		return sdl.Color{R: g, G: g, B: g, A: 255}
	}
}

// termPtyCellColors is a cell's ink and paper after bold, dim, reverse and
// invisible.
func termPtyCellColors(c vtCell, reverseScreen bool) (sdl.Color, sdl.Color) {
	fgc := c.fg
	if c.attr&vtAttrBold != 0 && fgc >= 1 && fgc <= 8 {
		fgc += 8 // bold as bright, the way xterm shows the first eight
	}
	fg := termPtyColor(fgc, termPtyDefaultFG)
	bg := termPtyColor(c.bg, termPtyDefaultBG)
	if (c.attr&vtAttrReverse != 0) != reverseScreen {
		fg, bg = bg, fg
	}
	if c.attr&vtAttrDim != 0 {
		fg = termPtyMix(bg, fg, 0.55)
	}
	if c.attr&vtAttrInvisible != 0 {
		fg = bg
	}
	return fg, bg
}

func termPtyMix(a, b sdl.Color, t float64) sdl.Color {
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return sdl.Color{R: m(a.R, b.R), G: m(a.G, b.G), B: m(a.B, b.B), A: 255}
}

func termPtyARGB(c sdl.Color) uint32 {
	return 0xff000000 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}

// ── Composing rows ───────────────────────────────────────────────────────

func termPtyGlyph(ch rune, bold bool) *sdl.Surface {
	r := &termPtyRender
	if r.glyphs == nil {
		r.glyphs = make(map[termPtyGlyphKey]*sdl.Surface)
	}
	key := termPtyGlyphKey{ch, bold && r.bold != r.font}
	if s, ok := r.glyphs[key]; ok {
		return s
	}
	if len(r.glyphs) >= termPtyGlyphMax {
		for k, s := range r.glyphs {
			s.Free()
			delete(r.glyphs, k)
		}
	}
	font := r.font
	if key.bold {
		font = r.bold
	}
	text := string(ch)
	if !r.ownFonts {
		text = sanitizeForFont(font, text)
	}
	s, err := font.RenderUTF8Blended(text, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if err != nil {
		s = nil
	}
	r.glyphs[key] = s
	return s
}

// termPtyFillRect, termPtyBlitRect and termPtyClipRect are the rectangles
// the row composer hands to SDL. A Rect passed to cgo escapes to the heap,
// and a full redraw is thousands of fills and blits: reusing three
// package-level ones keeps that off the A53's allocator. Main thread only.
var termPtyFillRect, termPtyBlitRect, termPtyClipRect sdl.Rect

// termPtyFill fills a rectangle of the grid surface.
func termPtyFill(surf *sdl.Surface, x, y, w, h int32, col uint32) {
	if w <= 0 || h <= 0 {
		return
	}
	termPtyFillRect = sdl.Rect{X: x, Y: y, W: w, H: h}
	_ = surf.FillRect(&termPtyFillRect, col)
}

// termPtyComposeRow draws one row of cells into the grid surface: the
// backgrounds as one fill per run of equal colour, then the glyphs, clipped
// to the row so a tall glyph never smears into its neighbours.
func termPtyComposeRow(y int, cells []vtCell, reverseScreen bool) {
	r := &termPtyRender
	surf := r.surf
	cw, ch := r.cw, r.ch
	rowY := int32(y) * ch
	cols := len(cells)
	runStart, runCol := 0, uint32(0)
	for x := 0; x < cols; {
		c := cells[x]
		w := 1
		if c.w == 2 && x+1 < cols {
			w = 2
		}
		_, bg := termPtyCellColors(c, reverseScreen)
		col := termPtyARGB(bg)
		if x > 0 && col != runCol {
			termPtyFill(surf, int32(runStart)*cw, rowY, int32(x-runStart)*cw, ch, runCol)
			runStart = x
		}
		runCol = col
		x += w
	}
	if cols > 0 {
		termPtyFill(surf, int32(runStart)*cw, rowY, int32(cols-runStart)*cw, ch, runCol)
	}
	termPtyClipRect = sdl.Rect{X: 0, Y: rowY, W: surf.W, H: ch}
	surf.SetClipRect(&termPtyClipRect)
	defer surf.SetClipRect(nil)
	for x := 0; x < cols; x++ {
		c := cells[x]
		if c.w == 0 {
			continue
		}
		w := int32(1)
		if c.w == 2 && x+1 < cols {
			w = 2
		}
		fg, bg := termPtyCellColors(c, reverseScreen)
		cell := sdl.Rect{X: int32(x) * cw, Y: rowY, W: w * cw, H: ch}
		if c.r != ' ' && c.r != 0 && c.attr&vtAttrInvisible == 0 {
			if !termPtyDrawShape(surf, c.r, cell, fg, bg) {
				if g := termPtyGlyph(c.r, c.attr&vtAttrBold != 0); g != nil {
					_ = g.SetColorMod(fg.R, fg.G, fg.B)
					// Centred in its cell(s): a proportional face keeps its
					// columns, and a wide character sits across both cells.
					termPtyBlitRect = sdl.Rect{X: cell.X + (cell.W-g.W)/2, Y: rowY + (ch-g.H)/2, W: g.W, H: g.H}
					_ = g.Blit(nil, surf, &termPtyBlitRect)
				}
			}
		}
		if c.attr&vtAttrUnderline != 0 {
			termPtyFill(surf, cell.X, rowY+ch-2, cell.W, 1, termPtyARGB(fg))
		}
		if c.attr&vtAttrStrike != 0 {
			termPtyFill(surf, cell.X, rowY+ch/2, cell.W, 1, termPtyARGB(fg))
		}
	}
}

// Box-drawing arms: 2 bits each for up, down, left, right — 1 light,
// 2 heavy, 3 double.
var termPtyBoxArms = map[rune]uint8{}

func init() {
	arm := func(u, d, l, r uint8) uint8 { return u | d<<2 | l<<4 | r<<6 }
	set := func(ch rune, u, d, l, r uint8) { termPtyBoxArms[ch] = arm(u, d, l, r) }
	set('─', 0, 0, 1, 1)
	set('━', 0, 0, 2, 2)
	set('│', 1, 1, 0, 0)
	set('┃', 2, 2, 0, 0)
	for _, ch := range "┄┈╌" {
		set(ch, 0, 0, 1, 1)
	}
	for _, ch := range "┅┉╍" {
		set(ch, 0, 0, 2, 2)
	}
	for _, ch := range "┆┊╎" {
		set(ch, 1, 1, 0, 0)
	}
	for _, ch := range "┇┋╏" {
		set(ch, 2, 2, 0, 0)
	}
	set('┌', 0, 1, 0, 1)
	set('┍', 0, 1, 0, 2)
	set('┎', 0, 2, 0, 1)
	set('┏', 0, 2, 0, 2)
	set('┐', 0, 1, 1, 0)
	set('┑', 0, 1, 2, 0)
	set('┒', 0, 2, 1, 0)
	set('┓', 0, 2, 2, 0)
	set('└', 1, 0, 0, 1)
	set('┕', 1, 0, 0, 2)
	set('┖', 2, 0, 0, 1)
	set('┗', 2, 0, 0, 2)
	set('┘', 1, 0, 1, 0)
	set('┙', 1, 0, 2, 0)
	set('┚', 2, 0, 1, 0)
	set('┛', 2, 0, 2, 0)
	for ch := rune(0x251c); ch <= 0x2523; ch++ {
		set(ch, 1, 1, 0, 1)
	}
	set('┣', 2, 2, 0, 2)
	for ch := rune(0x2524); ch <= 0x252b; ch++ {
		set(ch, 1, 1, 1, 0)
	}
	set('┫', 2, 2, 2, 0)
	for ch := rune(0x252c); ch <= 0x2533; ch++ {
		set(ch, 0, 1, 1, 1)
	}
	set('┳', 0, 2, 2, 2)
	for ch := rune(0x2534); ch <= 0x253b; ch++ {
		set(ch, 1, 0, 1, 1)
	}
	set('┻', 2, 0, 2, 2)
	for ch := rune(0x253c); ch <= 0x254b; ch++ {
		set(ch, 1, 1, 1, 1)
	}
	set('╋', 2, 2, 2, 2)
	set('═', 0, 0, 3, 3)
	set('║', 3, 3, 0, 0)
	set('╒', 0, 1, 0, 3)
	set('╓', 0, 3, 0, 1)
	set('╔', 0, 3, 0, 3)
	set('╕', 0, 1, 3, 0)
	set('╖', 0, 3, 1, 0)
	set('╗', 0, 3, 3, 0)
	set('╘', 1, 0, 0, 3)
	set('╙', 3, 0, 0, 1)
	set('╚', 3, 0, 0, 3)
	set('╛', 1, 0, 3, 0)
	set('╜', 3, 0, 1, 0)
	set('╝', 3, 0, 3, 0)
	set('╞', 1, 1, 0, 3)
	set('╟', 3, 3, 0, 1)
	set('╠', 3, 3, 0, 3)
	set('╡', 1, 1, 3, 0)
	set('╢', 3, 3, 1, 0)
	set('╣', 3, 3, 3, 0)
	set('╤', 0, 1, 3, 3)
	set('╥', 0, 3, 1, 1)
	set('╦', 0, 3, 3, 3)
	set('╧', 1, 0, 3, 3)
	set('╨', 3, 0, 1, 1)
	set('╩', 3, 0, 3, 3)
	set('╪', 1, 1, 3, 3)
	set('╫', 3, 3, 1, 1)
	set('╬', 3, 3, 3, 3)
	set('╭', 0, 1, 0, 1)
	set('╮', 0, 1, 1, 0)
	set('╯', 1, 0, 1, 0)
	set('╰', 1, 0, 0, 1)
	set('╴', 0, 0, 1, 0)
	set('╵', 1, 0, 0, 0)
	set('╶', 0, 0, 0, 1)
	set('╷', 0, 1, 0, 0)
	set('╸', 0, 0, 2, 0)
	set('╹', 2, 0, 0, 0)
	set('╺', 0, 0, 0, 2)
	set('╻', 0, 2, 0, 0)
	set('╼', 0, 0, 1, 2)
	set('╽', 1, 2, 0, 0)
	set('╾', 0, 0, 2, 1)
	set('╿', 2, 1, 0, 0)
}

// termPtyQuadrants are U+2596..U+259F (▖▗▘▙▚▛▜▝▞▟) as quarters: 1 top
// left, 2 top right, 4 bottom left, 8 bottom right.
var termPtyQuadrants = [10]uint8{4, 8, 1, 1 | 4 | 8, 1 | 8, 1 | 2 | 4, 1 | 2 | 8, 2, 2 | 4, 2 | 4 | 8}

// termPtyDrawShape draws box drawing, block elements and braille as shapes;
// false for any other character.
func termPtyDrawShape(surf *sdl.Surface, ch rune, cell sdl.Rect, fg, bg sdl.Color) bool {
	fill := func(x, y, w, h int32, c sdl.Color) {
		termPtyFill(surf, x, y, w, h, termPtyARGB(c))
	}
	if arms, ok := termPtyBoxArms[ch]; ok {
		t := cell.W / 9
		if t < 1 {
			t = 1
		}
		cx := cell.X + cell.W/2
		cy := cell.Y + cell.H/2
		d := t + 1 // half the gap between the two strokes of a double line
		vert := func(weight uint8, y0, y1 int32) {
			switch weight {
			case 1:
				fill(cx-t/2, y0, t, y1-y0, fg)
			case 2:
				fill(cx-t, y0, 2*t, y1-y0, fg)
			case 3:
				fill(cx-d-t/2, y0, t, y1-y0, fg)
				fill(cx+d-t/2, y0, t, y1-y0, fg)
			}
		}
		horiz := func(weight uint8, x0, x1 int32) {
			switch weight {
			case 1:
				fill(x0, cy-t/2, x1-x0, t, fg)
			case 2:
				fill(x0, cy-t, x1-x0, 2*t, fg)
			case 3:
				fill(x0, cy-d-t/2, x1-x0, t, fg)
				fill(x0, cy+d-t/2, x1-x0, t, fg)
			}
		}
		u, dn, l, r := arms&3, arms>>2&3, arms>>4&3, arms>>6&3
		ext := t // arms overlap the centre so corners close
		if u != 0 {
			vert(u, cell.Y, cy+ext)
		}
		if dn != 0 {
			vert(dn, cy-ext, cell.Y+cell.H)
		}
		if l != 0 {
			horiz(l, cell.X, cx+ext)
		}
		if r != 0 {
			horiz(r, cx-ext, cell.X+cell.W)
		}
		return true
	}
	switch {
	case ch == '█':
		fill(cell.X, cell.Y, cell.W, cell.H, fg)
	case ch == '▀':
		fill(cell.X, cell.Y, cell.W, cell.H/2, fg)
	case ch == '▔':
		fill(cell.X, cell.Y, cell.W, cell.H/8+1, fg)
	case ch >= '▁' && ch <= '▇': // lower eighths
		h := cell.H * int32(ch-'▁'+1) / 8
		fill(cell.X, cell.Y+cell.H-h, cell.W, h, fg)
	case ch >= '▉' && ch <= '▏': // left eighths, 7/8 down to 1/8
		w := cell.W * int32(8-(ch-'▉')-1) / 8
		if w < 1 {
			w = 1
		}
		fill(cell.X, cell.Y, w, cell.H, fg)
	case ch == '▐':
		fill(cell.X+cell.W/2, cell.Y, cell.W-cell.W/2, cell.H, fg)
	case ch == '▕':
		fill(cell.X+cell.W-cell.W/8-1, cell.Y, cell.W/8+1, cell.H, fg)
	case ch == '░' || ch == '▒' || ch == '▓':
		fill(cell.X, cell.Y, cell.W, cell.H, termPtyMix(bg, fg, float64(ch-'░'+1)*0.25))
	case ch >= '▖' && ch <= '▟': // quadrants
		q := termPtyQuadrants[ch-'▖']
		hw, hh := cell.W/2, cell.H/2
		if q&1 != 0 {
			fill(cell.X, cell.Y, hw, hh, fg)
		}
		if q&2 != 0 {
			fill(cell.X+hw, cell.Y, cell.W-hw, hh, fg)
		}
		if q&4 != 0 {
			fill(cell.X, cell.Y+hh, hw, cell.H-hh, fg)
		}
		if q&8 != 0 {
			fill(cell.X+hw, cell.Y+hh, cell.W-hw, cell.H-hh, fg)
		}
	case ch >= 0x2800 && ch <= 0x28ff: // braille: btop's and htop's graphs
		bits := uint8(ch - 0x2800)
		dot := cell.W / 4
		if dot < 1 {
			dot = 1
		}
		pos := [8][2]int32{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
		for i, p := range pos {
			if bits&(1<<uint(i)) == 0 {
				continue
			}
			x := cell.X + cell.W/4 + p[0]*cell.W/2 - dot/2
			y := cell.Y + cell.H/8 + p[1]*cell.H/4 - dot/2
			fill(x, y, dot, dot, fg)
		}
	default:
		return false
	}
	return true
}

// ── Per frame ────────────────────────────────────────────────────────────

// termPtyTick is the session's per-frame work, whether or not it is on
// screen: a session that ended hands its last screen to the line terminal,
// the on-screen keyboard's typing is sent, a held B is timed.
func termPtyTick(config *Config) {
	s := termPty.sess
	if s == nil {
		termPtyVKEnd()
		return
	}
	if s.finished() {
		termPtyFinish()
		return
	}
	termPtyVKSync()
	// Left Alt with a key whose text never came (Windows sends none with
	// Alt): ESC and the key from the key event, as before the text existed.
	if ui := &termPtyUI; ui.metaPending && sdl.GetTicks64()-ui.metaAt >= 40 {
		ui.metaPending = false
		ui.swallowText, ui.swallowAt = true, ui.metaAt // a late text is dropped
		termPtyUI.sbOffset = 0
		s.send(ui.metaFallback[:])
	}
	if !termPtyUI.bDown.IsZero() {
		held := vkPadHeld(sdl.CONTROLLER_BUTTON_B)
		switch {
		case held && time.Since(termPtyUI.bDown) >= termPtyHoldLeave:
			termPtyUI.bDown = time.Time{}
			PlayBackSound()
			termPtyLeave()
		case !held:
			termPtyUI.bDown = time.Time{}
			if termPtyShowing() {
				termPtySendKey(tpkEsc)
			}
		}
	}
}

// termPtyFinish closes an ended session: its main screen's text joins the
// line terminal's output, with how it ended.
func termPtyFinish() {
	s := termPty.sess
	termPty.sess = nil
	termPtyLive.CompareAndSwap(s, nil)
	termPty.view = false
	termPtyCloseOverlays()
	termPtyVKEnd()
	termPtyFreeSurfaces()
	termPtyUI.sbOffset = 0
	lines := s.vt.textLines()
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	termFollow()
	for _, l := range lines {
		termAppend(termCleanOutput(l), termKindOutput)
	}
	s.mu.Lock()
	code, err, killed := s.exitCode, s.exitErr, s.killed
	s.mu.Unlock()
	switch {
	case killed:
		termAppend(s.label+" ended.", termKindSystem)
	case err != nil:
		termAppend(s.label+" ended: "+firstLine(err.Error()), termKindError)
	case code != 0:
		termAppend(fmt.Sprintf("%s exited with code %d.", s.label, code), termKindError)
	default:
		termAppend(s.label+" ended.", termKindSystem)
	}
	logInfo("terminal", "interactive session ended: %s (code %d)", s.label, code)
}

// termPtyEntered runs when the session comes on screen. The line terminal's
// own sheets close: they would stay open, unseen, behind the session.
func termPtyEntered() {
	termPtyCloseOverlays()
	if cmdDeck.open {
		cmdDeckClose()
	}
	if termHist.open {
		termHistoryClose()
	}
	termPtyUI.sbOffset = 0
	termPtyRender.needFull = true
	if chromeTier(focusEngine.ElementIndex()) != 0 {
		focusEngine.ClearFocus()
		selectedButtonIndex = -1
	}
	if frameRateCtrl != nil {
		frameRateCtrl.NotifyInput()
	}
}

// termPtyCloseOverlays closes the session's menu, strip and help.
func termPtyCloseOverlays() {
	termPtyUI.menuOpen, termPtyUI.stripOpen, termPtyUI.helpOpen = false, false, false
	termPtyUI.stickyCtrl = false
	termPtyUI.bDown = time.Time{}
	termPtyUI.metaPending = false
}

// ── Sending ──────────────────────────────────────────────────────────────

// termPtySendBytes sends to the program; a sticky Ctrl applies to a single
// character. Typing brings the view back from the scrollback.
func termPtySendBytes(b []byte) {
	s := termPty.sess
	if s == nil || len(b) == 0 {
		return
	}
	if termPtyUI.stickyCtrl {
		termPtyUI.stickyCtrl = false
		if r, size := utf8.DecodeRune(b); size == len(b) {
			if c, ok := termPtyCtrlByte(r); ok {
				b = []byte{c}
			}
		}
	}
	termPtyUI.sbOffset = 0
	if !s.send(b) {
		showToast("The program is not reading what you type", ToastWarn())
	}
}

// termPtySendKey sends a special key, with Ctrl when the sticky Ctrl is on.
func termPtySendKey(k termPtyKey) {
	s := termPty.sess
	if s == nil {
		return
	}
	mods := 1
	if termPtyUI.stickyCtrl {
		mods = termPtyMods(false, false, true)
		termPtyUI.stickyCtrl = false
	}
	appCursor, _, _, newline, _ := s.vt.modes()
	b := termPtyKeyBytes(k, mods, appCursor)
	if k == tpkEnter && newline {
		b = []byte("\r\n")
	}
	termPtyUI.sbOffset = 0
	if !s.send(b) {
		showToast("The program is not reading what you type", ToastWarn())
	}
}

// termPtyPaste sends the clipboard.
func termPtyPaste() {
	s := termPty.sess
	if s == nil {
		return
	}
	text, err := pasteTextFromClipboard()
	if err != nil {
		showToast("Clipboard is unavailable: "+err.Error(), ToastWarn())
		return
	}
	if text == "" {
		showToast("Nothing to paste", ToastInfo())
		return
	}
	if len(text) > termPtyPasteMax {
		text = text[:termPtyPasteMax]
		showToast("Pasted the first 64 KB", ToastInfo())
	}
	_, _, bracketed, _, _ := s.vt.modes()
	termPtyUI.stickyCtrl = false
	termPtySendBytes(termPtyPasteBytes(text, bracketed))
}

// termPtyScrollBy moves the scrollback view (positive = older lines).
func termPtyScrollBy(n int) {
	s := termPty.sess
	if s == nil {
		return
	}
	_, sbLen := s.vt.sbPushedCount()
	termPtyUI.sbOffset = vtClampInt(termPtyUI.sbOffset+n, 0, sbLen)
}

// termPtyPageLines is one page of the grid.
func termPtyPageLines() int {
	if s := termPty.sess; s != nil {
		if _, rows := s.vt.size(); rows > 2 {
			return rows - 1
		}
	}
	return 10
}

// termPtySetFontSize changes the text size by step points.
func termPtySetFontSize(step int) {
	size := termPtyFontSize() + step
	if size < 10 || size > 36 {
		return
	}
	termPtyUI.fontSize = size
	termPtyRender.needFull = true
	showToast(fmt.Sprintf("Text size %d", size), ToastInfo())
}

// ── The on-screen keyboard ───────────────────────────────────────────────

// termPtyOpenKeyboard opens the keyboard (Y). It edits the line terminal's
// prompt field, which is set aside meanwhile; what changes in it is sent
// each frame, so every character reaches the program as it is typed.
func termPtyOpenKeyboard() {
	if termPtyUI.vkOpen {
		return
	}
	termPtyUI.vkSavedInput, termPtyUI.vkSavedCursor = term.input, term.cursor
	term.input = termPtyVKPad
	term.cursor = len(term.input)
	termPtyUI.vkSeen = term.input
	termPtyUI.vkOpen = true
	termPtyUI.secretAt = 0 // ask the program afresh
	termPtyUI.bDown = time.Time{}
	termPtyUI.metaPending = false
	vkOpenTerminal()
}

// termPtyVKActive reports the keyboard typing into the session.
func termPtyVKActive() bool { return termPtyUI.vkOpen }

// termPtySecretInput reports the keyboard typing into a session whose
// program reads a password (sudo, su, ssh, the built-in client's sign-in):
// the keyboard then shows dots, copies and learns nothing and hides its
// strip (vkSecretTarget, vkStripModeNow). Main thread; the program is asked
// a few times a second, not every frame.
func termPtySecretInput() bool {
	ui := &termPtyUI
	s := termPty.sess
	if !ui.vkOpen || s == nil || s.proc == nil || s.finished() {
		ui.secret, ui.secretAt = false, 0
		return false
	}
	if now := sdl.GetTicks64(); ui.secretAt == 0 || now-ui.secretAt >= 200 {
		ui.secretAt = now
		ui.secret = s.proc.EchoOff()
	}
	return ui.secret
}

// termPtyVKDiff sends what turned from before into after: Backspaces for the
// characters removed, then what was added.
func termPtyVKDiff(before, after string) {
	if before == after {
		return
	}
	b, a := []rune(before), []rune(after)
	p := 0
	for p < len(b) && p < len(a) && b[p] == a[p] {
		p++
	}
	if dels := len(b) - p; dels > 0 {
		termPtySendBytes([]byte(strings.Repeat("\x7f", dels)))
	}
	if p < len(a) {
		termPtySendBytes([]byte(string(a[p:])))
	}
}

// termPtyVKSync sends the keyboard's typing since the last frame, and ends
// the keyboard mode once it closed.
func termPtyVKSync() {
	if !termPtyUI.vkOpen {
		return
	}
	if virtualKeyboardActive && vkTarget == vkTargetTerm {
		cur := term.input
		termPtyVKDiff(termPtyUI.vkSeen, cur)
		termPtyUI.vkSeen = cur
		if n := utf8.RuneCountInString(cur); n < len(termPtyVKPad) || n > termPtyVKMaxRunes {
			// Room to delete into, and a field that never fills up.
			term.input = termPtyVKPad
			term.cursor = len(term.input)
			vkCaret = -1
			termPtyUI.vkSeen = term.input
		}
		return
	}
	if vkTarget == vkTargetTerm {
		termPtyVKDiff(termPtyUI.vkSeen, term.input)
	}
	termPtyVKEnd()
}

// termPtyVKEnd puts the prompt back.
func termPtyVKEnd() {
	if !termPtyUI.vkOpen {
		return
	}
	termPtyUI.vkOpen = false
	term.input, term.cursor = termPtyUI.vkSavedInput, termPtyUI.vkSavedCursor
	termPtyUI.vkSavedInput, termPtyUI.vkSeen = "", ""
}

// termPtyVKSubmit is the keyboard's Enter (it arrives as termExecute): the
// rest of the typing, then Enter. False when the keyboard was not typing
// into a session.
func termPtyVKSubmit(text string) bool {
	if !termPtyUI.vkOpen {
		return false
	}
	termPtyVKDiff(termPtyUI.vkSeen, text)
	termPtyUI.vkSeen = text
	termPtyVKEnd()
	termPtySendKey(tpkEnter)
	return true
}

// ── Input routing (hooks in main.go and terminal.go) ─────────────────────

// termPtyTakesKeys reports whether the keyboard belongs to the session now:
// it is on screen and nothing JukaHub draws over it is open.
func termPtyTakesKeys() bool {
	return termPtyShowing() && !virtualKeyboardActive && !alarmFiringActive() && !ss.active &&
		!textInputBlockedByOverlay() && !qs.open && !controlsLegend.open && !globalSearchOpen &&
		!clipSearchOpen && !onboarding.active && !launchFailure.open && !voiceOrbActive() && !companion.open
}

// termPtyChord reports the key as want: by what it types, or — on a layout
// whose keys are not Latin (Cyrillic, Greek …) — by its place on the
// keyboard, so Ctrl+Shift+Q still leaves there. A Latin layout that moves
// letters (AZERTY) goes by the letter.
func termPtyChord(ks sdl.Keysym, want sdl.Keycode, place sdl.Scancode) bool {
	if ks.Sym == want {
		return true
	}
	return ks.Sym >= 0x80 && ks.Scancode == place
}

// termPtyKeyDown takes a key press before JukaHub's global keys (main.go):
// in a session every key is the program's except the session's own chords;
// in the line terminal Ctrl+Shift+Q resumes a running session.
func termPtyKeyDown(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	mod := e.Keysym.Mod
	ctrl := mod&sdl.KMOD_CTRL != 0
	shift := mod&sdl.KMOD_SHIFT != 0
	sym := e.Keysym.Sym
	if !termPtyTakesKeys() {
		if ctrl && shift && termPtyChord(e.Keysym, sdl.K_q, sdl.SCANCODE_Q) && e.Repeat == 0 && isTerminalScene() && termPtyAlive() &&
			!termPty.view && !virtualKeyboardActive && !textInputBlockedByOverlay() && !qs.open {
			termPtyResume()
			return true
		}
		return false
	}
	ui := &termPtyUI
	if ui.helpOpen {
		if e.Repeat == 0 && (sym == sdl.K_ESCAPE || sym == sdl.K_RETURN || sym == sdl.K_F1 ||
			(ctrl && shift && termPtyChord(e.Keysym, sdl.K_m, sdl.SCANCODE_M))) {
			ui.helpOpen = false
		}
		return true
	}
	if ui.menuOpen {
		termPtyMenuKey(sym, e.Repeat != 0)
		return true
	}
	if ui.stripOpen {
		termPtyStripKey(sym)
		return true
	}
	s := termPty.sess
	_, _, _, _, alt := s.vt.modes()
	// AltGr types characters (Windows reports it as Ctrl+Right Alt).
	altGr := mod&sdl.KMOD_RALT != 0
	lalt := mod&sdl.KMOD_LALT != 0
	if ctrl && shift && !altGr {
		ks := e.Keysym
		switch {
		case termPtyChord(ks, sdl.K_q, sdl.SCANCODE_Q):
			if e.Repeat == 0 {
				termPtyLeave()
			}
			return true
		case termPtyChord(ks, sdl.K_m, sdl.SCANCODE_M):
			if e.Repeat == 0 {
				termPtyMenuOpen()
			}
			return true
		case termPtyChord(ks, sdl.K_v, sdl.SCANCODE_V):
			if e.Repeat == 0 {
				termPtyPaste()
			}
			return true
		case sym == sdl.K_PLUS || sym == sdl.K_KP_PLUS || termPtyChord(ks, sdl.K_EQUALS, sdl.SCANCODE_EQUALS):
			termPtySetFontSize(2)
			return true
		case sym == sdl.K_KP_MINUS || sym == sdl.K_UNDERSCORE || termPtyChord(ks, sdl.K_MINUS, sdl.SCANCODE_MINUS):
			termPtySetFontSize(-2)
			return true
		}
	}
	if shift && !ctrl && !alt && (sym == sdl.K_PAGEUP || sym == sdl.K_PAGEDOWN) {
		if sym == sdl.K_PAGEUP {
			termPtyScrollBy(termPtyPageLines())
		} else {
			termPtyScrollBy(-termPtyPageLines())
		}
		return true
	}
	mods := termPtyMods(shift, lalt, ctrl && !altGr)
	if ui.stickyCtrl {
		mods = termPtyMods(shift, lalt, true)
	}
	key := termPtyKey(-1)
	switch sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		key = tpkEnter
	case sdl.K_BACKSPACE:
		key = tpkBackspace
	case sdl.K_TAB:
		key = tpkTab
		if shift {
			key = tpkBackTab
		}
	case sdl.K_ESCAPE:
		key = tpkEsc
	case sdl.K_UP:
		key = tpkUp
	case sdl.K_DOWN:
		key = tpkDown
	case sdl.K_LEFT:
		key = tpkLeft
	case sdl.K_RIGHT:
		key = tpkRight
	case sdl.K_HOME:
		key = tpkHome
	case sdl.K_END:
		key = tpkEnd
	case sdl.K_PAGEUP:
		key = tpkPgUp
	case sdl.K_PAGEDOWN:
		key = tpkPgDn
	case sdl.K_INSERT:
		key = tpkInsert
	case sdl.K_DELETE:
		key = tpkDelete
	}
	if sym >= sdl.K_F1 && sym <= sdl.K_F12 {
		key = tpkF1 + termPtyKey(sym-sdl.K_F1)
	}
	if key >= 0 {
		appCursor, _, _, newline, _ := s.vt.modes()
		var b []byte
		switch {
		case key == tpkEnter && newline:
			b = []byte("\r\n")
		case key == tpkTab || key == tpkEnter || key == tpkBackspace:
			// These take only Alt (ESC prefix) and Ctrl+Backspace.
			m := termPtyMods(false, lalt, (ctrl && !altGr) || ui.stickyCtrl)
			b = termPtyKeyBytes(key, m, appCursor)
		default:
			b = termPtyKeyBytes(key, mods, appCursor)
		}
		ui.stickyCtrl = false
		ui.sbOffset = 0
		if !s.send(b) {
			showToast("The program is not reading what you type", ToastWarn())
		}
		return true
	}
	// Ctrl (or the sticky Ctrl) with a character: the control byte. The
	// scancode stands in on a layout whose letters are not Latin.
	if (ctrl && !altGr) || ui.stickyCtrl {
		r := rune(-1)
		if sym >= 0x20 && sym < 0x7f {
			r = rune(sym)
		} else if sc := e.Keysym.Scancode; sc >= sdl.SCANCODE_A && sc <= sdl.SCANCODE_Z {
			r = 'a' + rune(sc-sdl.SCANCODE_A)
		}
		if c, ok := termPtyCtrlByte(r); ok {
			b := []byte{c}
			if lalt {
				b = append([]byte{0x1b}, c)
			}
			ui.stickyCtrl = false
			ui.swallowText, ui.swallowAt = true, sdl.GetTicks64()
			ui.sbOffset = 0
			s.send(b)
			return true
		}
	}
	// Left Alt with a character: ESC and the character (Meta). The character
	// is the key's text, which follows this event (Alt+Shift+1 is ESC !, not
	// ESC 1); termPtyTick sends this guess when no text comes.
	if lalt && !ctrl && sym >= 0x20 && sym < 0x7f {
		r := rune(sym)
		if shift && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		ui.metaPending, ui.metaAt = true, sdl.GetTicks64()
		ui.metaFallback = [2]byte{0x1b, byte(r)}
		ui.swallowText = false
		ui.sbOffset = 0
		requestFrameEvery(16)
		return true
	}
	// Everything else types through the text event (handleTermTextInput).
	return true
}

// termPtyText takes typed text while the session is on screen.
func termPtyText(text string) bool {
	if !termPtyShowing() {
		return false
	}
	ui := &termPtyUI
	if ui.menuOpen || ui.stripOpen || ui.helpOpen {
		return true
	}
	if ui.metaPending {
		// Left Alt's ESC prefix goes with the key's real character.
		ui.metaPending = false
		if text != "" && sdl.GetTicks64()-ui.metaAt < 150 {
			ui.stickyCtrl = false
			ui.sbOffset = 0
			if termPty.sess != nil && !termPty.sess.send([]byte("\x1b"+strings.ReplaceAll(text, "\n", "\r"))) {
				showToast("The program is not reading what you type", ToastWarn())
			}
			return true
		}
	}
	if ui.swallowText {
		ui.swallowText = false
		if sdl.GetTicks64()-ui.swallowAt < 150 {
			return true
		}
	}
	text = strings.ReplaceAll(text, "\n", "\r")
	termPtySendBytes([]byte(text))
	return true
}

// termPtyWheel scrolls back on the shell's screen; in a full-screen program
// the wheel is the arrows, as in xterm.
func termPtyWheel(dy int32) bool {
	if !termPtyShowing() {
		return false
	}
	ui := &termPtyUI
	switch {
	case ui.menuOpen:
		ui.menuSel = vtClampInt(ui.menuSel-int(dy), 0, tpmCount-1)
	case ui.stripOpen:
		ui.stripSel = vtClampInt(ui.stripSel-int(dy), 0, len(termPtyStripKeys)-1)
	default:
		_, _, _, _, alt := termPty.sess.vt.modes()
		if alt {
			k := tpkUp
			if dy < 0 {
				k = tpkDown
			}
			for i := 0; i < 3; i++ {
				termPtySendKey(k)
			}
		} else {
			termPtyScrollBy(int(dy) * 3)
		}
	}
	return true
}

// termPtyClick: menu and strip rows answer a click, a click outside them
// closes them; the header (title and its "Menu" chip) opens the session
// menu, so a mouse alone can Leave or End; the grid takes clicks and does
// nothing with them. Elsewhere (the footer) the click is not the session's.
// On the line terminal the "resumes" pill resumes.
func termPtyClick(mx, my int32) bool {
	ui := &termPtyUI
	if !termPtyShowing() {
		if ui.resumeRect.W > 0 && pointInRect(mx, my, ui.resumeRect) && termPtyAlive() {
			PlayActivateSound()
			termPtyResume()
			return true
		}
		return false
	}
	switch {
	case ui.helpOpen:
		ui.helpOpen = false
	case ui.menuOpen:
		for i, r := range ui.menuRects {
			if pointInRect(mx, my, r) {
				ui.menuSel = i
				termPtyMenuActivate(i)
				return true
			}
		}
		ui.menuOpen = false
	case ui.stripOpen:
		for i, r := range ui.stripRects {
			if pointInRect(mx, my, r) {
				ui.stripSel = i
				termPtyStripSend(i)
				return true
			}
		}
		ui.stripOpen = false
	case ui.headerRect.W > 0 && pointInRect(mx, my, ui.headerRect):
		PlayActivateSound()
		termPtyMenuOpen()
	case ui.gridRect.W > 0 && pointInRect(mx, my, ui.gridRect):
	default:
		return false
	}
	return true
}

// termPtyPad is the pad while the session is on screen (ctrlTerminal).
func termPtyPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	ui := &termPtyUI
	if ui.helpOpen {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_BACK:
			ui.helpOpen = false
			PlayBackSound()
		}
		return true
	}
	if ui.menuOpen {
		termPtyMenuPad(e)
		return true
	}
	if ui.stripOpen {
		termPtyStripPad(e)
		return true
	}
	s := termPty.sess
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		termPtySendKey(tpkUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		termPtySendKey(tpkDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		termPtySendKey(tpkLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		termPtySendKey(tpkRight)
	case sdl.CONTROLLER_BUTTON_A:
		termPtySendKey(tpkEnter)
	case sdl.CONTROLLER_BUTTON_B:
		// Esc on release, leave when held (termPtyTick).
		if ui.bDown.IsZero() {
			ui.bDown = time.Now()
		}
	case sdl.CONTROLLER_BUTTON_X:
		termPtySendKey(tpkBackspace)
	case sdl.CONTROLLER_BUTTON_Y:
		PlayActivateSound()
		termPtyOpenKeyboard()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		up := e.Button == sdl.CONTROLLER_BUTTON_LEFTSHOULDER
		if _, _, _, _, alt := s.vt.modes(); alt {
			if up {
				termPtySendKey(tpkPgUp)
			} else {
				termPtySendKey(tpkPgDn)
			}
		} else if up {
			termPtyScrollBy(termPtyPageLines())
		} else {
			termPtyScrollBy(-termPtyPageLines())
		}
	case sdl.CONTROLLER_BUTTON_BACK:
		PlayActivateSound()
		termPtyStripOpen()
	case sdl.CONTROLLER_BUTTON_START:
		PlayActivateSound()
		termPtyMenuOpen()
	default:
		return false
	}
	return true
}

// termPtyAxis takes L2 (Tab) and R2 (sticky Ctrl) while the session has the
// pad (main.go, before the shared trigger handling).
func termPtyAxis(e *sdl.ControllerAxisEvent, config *Config) bool {
	if e == nil || (e.Axis != sdl.CONTROLLER_AXIS_TRIGGERLEFT && e.Axis != sdl.CONTROLLER_AXIS_TRIGGERRIGHT) {
		return false
	}
	if !termPtyTakesKeys() {
		return false
	}
	side := 0
	if e.Axis == sdl.CONTROLLER_AXIS_TRIGGERRIGHT {
		side = 1
	}
	if !triggerPressedEdge(side, e.Value) {
		return true
	}
	ui := &termPtyUI
	if ui.menuOpen || ui.stripOpen || ui.helpOpen {
		return true
	}
	if side == 0 {
		termPtySendKey(tpkTab)
	} else {
		ui.stickyCtrl = !ui.stickyCtrl
		PlayNavSound()
	}
	return true
}

// ── Menu, strip, help ────────────────────────────────────────────────────

// termPtyMenuOpen opens the session menu. A B still held from before is
// forgotten, so its release cannot send Esc behind the menu.
func termPtyMenuOpen() {
	termPtyUI.menuOpen = true
	termPtyUI.menuSel = 0
	termPtyUI.stripOpen = false
	termPtyUI.bDown = time.Time{}
	termPtyUI.metaPending = false
}

// termPtyStripOpen opens the special-keys strip, the same way.
func termPtyStripOpen() {
	termPtyUI.stripOpen = true
	termPtyUI.bDown = time.Time{}
	termPtyUI.metaPending = false
}

func termPtyMenuLabel(i int) string {
	switch i {
	case tpmLeave:
		return "Leave — keep it running"
	case tpmEnd:
		return "End session"
	case tpmPaste:
		return "Paste from the clipboard"
	case tpmSmaller:
		return fmt.Sprintf("Smaller text (%d)", termPtyFontSize())
	case tpmLarger:
		return fmt.Sprintf("Larger text (%d)", termPtyFontSize())
	case tpmHelp:
		return "Keys help"
	}
	return ""
}

func termPtyMenuActivate(i int) {
	ui := &termPtyUI
	switch i {
	case tpmLeave:
		ui.menuOpen = false
		PlayBackSound()
		termPtyLeave()
	case tpmEnd:
		ui.menuOpen = false
		PlayBackSound()
		if s := termPty.sess; s != nil {
			s.end()
		}
	case tpmPaste:
		ui.menuOpen = false
		PlayActivateSound()
		termPtyPaste()
	case tpmSmaller:
		PlayNavSound()
		termPtySetFontSize(-2)
	case tpmLarger:
		PlayNavSound()
		termPtySetFontSize(2)
	case tpmHelp:
		ui.menuOpen = false
		ui.helpOpen = true
		PlayActivateSound()
	}
}

func termPtyMenuMove(d int) {
	termPtyUI.menuSel = (termPtyUI.menuSel + d + tpmCount) % tpmCount
	PlayNavSound()
}

func termPtyMenuPad(e *sdl.ControllerButtonEvent) {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		termPtyMenuMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		termPtyMenuMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		termPtyMenuActivate(termPtyUI.menuSel)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_START:
		termPtyUI.menuOpen = false
		PlayBackSound()
	}
}

func termPtyMenuKey(sym sdl.Keycode, repeat bool) {
	switch sym {
	case sdl.K_UP:
		termPtyMenuMove(-1)
	case sdl.K_DOWN:
		termPtyMenuMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if !repeat {
			termPtyMenuActivate(termPtyUI.menuSel)
		}
	case sdl.K_ESCAPE, sdl.K_m:
		if !repeat {
			termPtyUI.menuOpen = false
		}
	}
}

func termPtyStripSend(i int) {
	if i < 0 || i >= len(termPtyStripKeys) {
		return
	}
	k := termPtyStripKeys[i]
	termPtyUI.stripOpen = false
	if k.bytes != "" {
		termPtySendBytes([]byte(k.bytes))
	} else {
		termPtySendKey(k.key)
	}
}

func termPtyStripMove(d int) {
	n := len(termPtyStripKeys)
	termPtyUI.stripSel = (termPtyUI.stripSel + d + n) % n
	PlayNavSound()
}

func termPtyStripPad(e *sdl.ControllerButtonEvent) {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_UP:
		termPtyStripMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		termPtyStripMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		termPtyStripMove(-5)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		termPtyStripMove(5)
	case sdl.CONTROLLER_BUTTON_A:
		PlayActivateSound()
		termPtyStripSend(termPtyUI.stripSel)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		termPtyUI.stripOpen = false
		PlayBackSound()
	}
}

func termPtyStripKey(sym sdl.Keycode) {
	switch sym {
	case sdl.K_LEFT, sdl.K_UP:
		termPtyStripMove(-1)
	case sdl.K_RIGHT, sdl.K_DOWN:
		termPtyStripMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		termPtyStripSend(termPtyUI.stripSel)
	case sdl.K_ESCAPE:
		termPtyUI.stripOpen = false
	}
}

// termPtyLeaveWords is how to leave a session, for the Terminal's help.
func termPtyLeaveWords() string {
	if hintsUseKeyboard() {
		return "Ctrl+Shift+Q leaves"
	}
	return hintSentence("hold %s leaves", "B", "Leave")
}

// termPtyHelpLines is the Keys help card: the keyboard's keys on a
// keyboard, the pad's (named as the pad and any remap name them) on a pad.
func termPtyHelpLines() []string {
	if hintsUseKeyboard() {
		return []string{
			"Keyboard",
			"  Every key goes to the program — F1–F12, Ctrl+Q and Ctrl+L included.",
			"  Ctrl+Shift+Q  leave (on the line terminal: resume)   Ctrl+Shift+M  menu",
			"  Ctrl+Shift+V  paste   Ctrl+Shift+ + / −  text size",
			"  Shift+PgUp / PgDn, mouse wheel  scroll back (the wheel is the arrows in a full-screen program)",
			"  Click the title bar for this menu.",
			"Leaving keeps the program running; End session in the menu stops it.",
		}
	}
	k := func(button, label string) string { return hintKeyFor(button, label) }
	return []string{
		"Pad",
		"  " + k("D-Pad", "Arrows") + "  arrows      " + k("A", "Enter") + "  Enter      " + k("B", "Esc") + "  Esc (hold: leave)",
		"  " + k("X", "Backspace") + "  Backspace      " + k("Y", "Keyboard") + "  keyboard (each key is sent as typed)",
		"  " + k("L2", "Tab") + "  Tab      " + k("R2", "Ctrl") + "  Ctrl for the next key",
		"  " + k("L1", "PgUp") + " / " + k("R1", "PgDn") + "  PgUp / PgDn in a full-screen program, else scroll back",
		"  " + k("Select", "Keys") + "  special keys (Esc, Ctrl+C, F1–F12, :wq …)   " + k("Start", "Menu") + "  this menu",
		"Leaving keeps the program running; End session in the menu stops it.",
	}
}

// ── Footer ───────────────────────────────────────────────────────────────

// termPtyFooterHints is the footer while the session is on screen.
func termPtyFooterHints() []FooterHint {
	ui := &termPtyUI
	if hintsUseKeyboard() {
		switch {
		case ui.menuOpen:
			return []FooterHint{{Button: "Up/Down", Label: "Choose"}, {Button: "Enter", Label: "Select"}, {Button: "Esc", Label: "Close"}}
		case ui.stripOpen:
			return []FooterHint{{Button: "Left/Right", Label: "Choose"}, {Button: "Enter", Label: "Send"}, {Button: "Esc", Label: "Close"}}
		case ui.helpOpen:
			return []FooterHint{{Button: "Esc", Label: "Close"}}
		}
		return []FooterHint{
			{Button: "Ctrl+Shift+Q", Label: "Leave"},
			{Button: "Ctrl+Shift+M", Label: "Menu"},
			{Button: "Ctrl+Shift+V", Label: "Paste"},
			{Button: "Shift+PgUp/PgDn", Label: "Scroll back"},
		}
	}
	switch {
	case ui.menuOpen:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	case ui.stripOpen:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Send"}, {Button: "L1/R1", Label: "Jump"}, {Button: "B", Label: "Close"}}
	case ui.helpOpen:
		return []FooterHint{{Button: "B", Label: "Close"}}
	}
	// The way out and the menu come first: a narrow canvas (the Brick's
	// 960 px) drops hints from the end (planFooterHints).
	return []FooterHint{
		{Button: "Hold B", Label: "Leave"},
		{Button: "Start", Label: "Menu"},
		{Button: "A", Label: "Enter"},
		{Button: "B", Label: "Esc"},
		{Button: "Y", Label: "Keyboard"},
		{Button: "X", Label: "Backspace"},
		{Button: "Select", Label: "Keys"},
		{Button: "L2", Label: "Tab"},
	}
}

// termPtyResumeHint is the line terminal's footer entry while a session runs
// in the background.
func termPtyResumeHint() (FooterHint, bool) {
	if !termPtyAlive() || termPty.view {
		return FooterHint{}, false
	}
	if hintsUseKeyboard() {
		return FooterHint{Button: "Ctrl+Shift+Q", Label: "Resume " + termPty.sess.label}, true
	}
	return FooterHint{Button: "Start", Label: "Resume " + termPty.sess.label}, true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// renderTermPtyResume draws the "Resume" pill on the line terminal's
// console while a session runs in the background.
func renderTermPtyResume(renderer *sdl.Renderer, font *ttf.Font, console sdl.Rect) {
	termPtyUI.resumeRect = sdl.Rect{}
	if !termPtyAlive() || termPty.view || font == nil {
		return
	}
	// hintSentence replaces only the first %s (no fmt), so the label needs no
	// escaping.
	label := hintSentence("%s resumes "+termPty.sess.label, "Start", "Resume")
	switch {
	case hintsUseKeyboard():
		label = "Ctrl+Shift+Q resumes " + termPty.sess.label
	case termTakesInput():
		// Start answers the program waiting here (ctrlTerminal).
		label = "Resume " + termPty.sess.label
	}
	tw, th := measureText(font, label)
	h := th + 12
	pill := sdl.Rect{X: console.X + console.W - tw - 32 - 14, Y: console.Y + 10, W: tw + 32, H: h}
	fill := glassSheetFill()
	if pointInRect(mouseX, mouseY, pill) {
		fill = glassRaisedFill()
	}
	fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, h/2, fill)
	strokeRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, h/2, 1, WithAlpha(glassAccent(), 200))
	drawText(renderer, font, label, pill.X+16, pill.Y+(h-th)/2, ColorTextPrimary(), textAlignLeft)
	termPtyUI.resumeRect = pill
}

// renderTermPty draws the session screen.
func renderTermPty(renderer *sdl.Renderer, config *Config) {
	s := termPty.sess
	r := &termPtyRender
	ui := &termPtyUI
	uiFont := termUIFont(config)
	if s == nil || !termPtyEnsureFonts(config) || uiFont == nil {
		renderStatusBar(renderer, config)
		renderFooter(renderer, config)
		return
	}
	header, grid := termPtyLayout()
	pad := int32(8)
	cols, rows := vtClampSize(int((grid.W-2*pad)/r.cw), int((grid.H-2*pad)/r.ch))
	s.resize(cols, rows)

	// A view in the scrollback stays on the same lines while new ones arrive.
	pushed, sbLen := s.vt.sbPushedCount()
	if ui.sbOffset > 0 && pushed > ui.lastPushed {
		ui.sbOffset += int(pushed - ui.lastPushed)
	}
	ui.lastPushed = pushed
	ui.sbOffset = vtClampInt(ui.sbOffset, 0, sbLen)

	// A Direct3D device reset can blank a texture: redraw everything now and
	// then on Windows.
	full := r.needFull || (runtime.GOOS == "windows" && time.Since(r.lastFull) > 3*time.Second)
	s.vt.takeView(&r.view, ui.sbOffset, full)
	v := &r.view
	// The view may refuse the offset (a full-screen program's alternate
	// screen has no scrollback): the cursor and the chip follow the view.
	ui.sbOffset = v.Offset
	w, h := int32(v.Cols)*r.cw, int32(v.Rows)*r.ch
	if r.surf == nil || r.tex == nil || r.texW != w || r.texH != h {
		termPtyFreeSurfaces()
		surf, err := sdl.CreateRGBSurfaceWithFormat(0, w, h, 32, sdl.PIXELFORMAT_ARGB8888)
		if err != nil {
			return
		}
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_STREAMING, w, h)
		if err != nil {
			surf.Free()
			return
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_NONE)
		r.surf, r.tex, r.texW, r.texH = surf, tex, w, h
		s.vt.takeView(&r.view, ui.sbOffset, true)
		v = &r.view
		ui.sbOffset = v.Offset
	}
	if v.Full {
		r.needFull = false
		r.lastFull = time.Now()
	}
	if len(v.Changed) > 0 {
		lo, hi := v.Rows, -1
		for _, y := range v.Changed {
			if y < 0 || y >= v.Rows || y >= len(v.Cells) {
				continue
			}
			termPtyComposeRow(y, v.Cells[y], v.ReverseScreen)
			lo, hi = min(lo, y), max(hi, y)
		}
		if hi >= lo {
			pix := r.surf.Pixels()
			off := int(int32(lo)*r.ch) * int(r.surf.Pitch)
			if off >= 0 && off < len(pix) {
				rect := sdl.Rect{X: 0, Y: int32(lo) * r.ch, W: w, H: int32(hi-lo+1) * r.ch}
				_ = r.tex.Update(&rect, unsafe.Pointer(&pix[off]), int(r.surf.Pitch))
			}
		}
	}
	if s.lastOut.Load() > time.Now().Add(-time.Second).UnixNano() || !ui.bDown.IsZero() {
		requestFrameEvery(33)
	}
	if ui.metaPending {
		requestFrameEvery(16) // termPtyTick's fallback for Alt+key
	}

	// The on-screen keyboard covers the bottom of the screen, where a
	// shell's prompt sits: while it types into the session the grid moves up
	// so the cursor's row stays above it (the header gives way).
	shift := int32(0)
	if ui.vkOpen && virtualKeyboardActive && vkTarget == vkTargetTerm {
		vkEnsureLayout()
		if limit := vkPanelRect.Y - 6; vkPanelRect.H > 0 && v.CursorY >= 0 && v.CursorY < v.Rows {
			if bottom := grid.Y + pad + int32(v.CursorY+1)*r.ch; bottom > limit {
				shift = bottom - limit
			}
		}
	}
	grid.Y -= shift

	// The grid on its terminal-black card.
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, grid.X, grid.Y, grid.W, grid.H, 10, termPtyDefaultBG)
	gx := grid.X + (grid.W-w)/2
	gy := grid.Y + pad
	_ = renderer.Copy(r.tex, nil, &sdl.Rect{X: gx, Y: gy, W: w, H: h})
	ui.gridRect = grid

	// The cursor: a block, blinking like the line terminal's caret.
	overlay := ui.menuOpen || ui.stripOpen || ui.helpOpen || virtualKeyboardActive
	if v.CursorVisible && v.Offset == 0 && v.CursorX >= 0 && v.CursorX < v.Cols && v.CursorY >= 0 && v.CursorY < v.Rows {
		cr := sdl.Rect{X: gx + int32(v.CursorX)*r.cw, Y: gy + int32(v.CursorY)*r.ch, W: r.cw, H: r.ch}
		accent := glassAccent()
		blinkOn := (time.Since(s.started)/termCaretBlink)%2 == 0 || s.lastOut.Load() > time.Now().Add(-termCaretBlink).UnixNano()
		if overlay {
			_ = renderer.SetDrawColor(accent.R, accent.G, accent.B, 200)
			_ = renderer.DrawRect(&cr)
		} else if blinkOn {
			_ = renderer.SetDrawColor(accent.R, accent.G, accent.B, 150)
			_ = renderer.FillRect(&cr)
		}
		requestFrameEvery(uint32(termCaretBlink / time.Millisecond / 2))
	}

	ui.headerRect = sdl.Rect{}
	if shift == 0 {
		renderTermPtyHeader(renderer, uiFont, header, v)
		ui.headerRect = header
	}
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if ui.stripOpen {
		renderTermPtyStrip(renderer, uiFont, grid)
	}
	if ui.menuOpen {
		renderTermPtyMenu(renderer, config, uiFont)
	}
	if ui.helpOpen {
		renderTermPtyHelp(renderer, config, uiFont)
	}
}

// renderTermPtyHeader is the slim band above the grid: the program's title
// (or what was started) and the session's state.
func renderTermPtyHeader(renderer *sdl.Renderer, font *ttf.Font, band sdl.Rect, v *vtView) {
	s := termPty.sess
	ui := &termPtyUI
	title := v.Title
	if title == "" {
		title = s.label
	}
	icon := band.H - 8
	DrawHomeIcon(renderer, "terminal", sdl.Rect{X: band.X + 2, Y: band.Y + 4, W: icon, H: icon}, false)
	_, th := measureText(font, "Ag")
	ty := band.Y + (band.H-th)/2

	// Right side: chips for the state, newest information first.
	x := band.X + band.W
	chip := func(text string, col sdl.Color, filled bool) {
		tw, _ := measureText(font, text)
		cw := tw + 20
		x -= cw
		h := th + 6
		y := band.Y + (band.H-h)/2
		if filled {
			fillRoundedRect(renderer, x, y, cw, h, h/2, WithAlpha(col, 220))
			drawText(renderer, font, text, x+10, y+3, ColorTextInverse(), textAlignLeft)
		} else {
			strokeRoundedRect(renderer, x, y, cw, h, h/2, 1, WithAlpha(col, 160))
			drawText(renderer, font, text, x+10, y+3, col, textAlignLeft)
		}
		x -= 8
	}
	// A click anywhere on this band opens the session menu (termPtyClick);
	// the chip says so to a mouse.
	menuCol := ColorTextSecondary()
	if pointInRect(mouseX, mouseY, band) {
		menuCol = glassAccent()
	}
	chip("Menu", menuCol, false)
	secs := int(time.Since(s.started) / time.Second)
	chip(fmt.Sprintf("Running · %d:%02d", secs/60, secs%60), ColorTextTertiary(), false)
	if ui.sbOffset > 0 {
		chip(fmt.Sprintf("Scrolled back %d", ui.sbOffset), glassAccent(), false)
	}
	if ui.stickyCtrl {
		chip("Ctrl", glassAccent(), true)
	}
	if !ui.bDown.IsZero() {
		if held := time.Since(ui.bDown); held > 200*time.Millisecond {
			chip(hintSentence("Keep holding %s to leave", "B", "Leave"), glassAccent(), true)
		}
	}
	maxW := x - (band.X + icon + 12)
	drawText(renderer, font, ellipsize(font, title, maxW), band.X+icon+10, ty, ColorTextPrimary(), textAlignLeft)
}

// renderTermPtyStrip draws the special-keys strip along the grid's bottom.
func renderTermPtyStrip(renderer *sdl.Renderer, font *ttf.Font, grid sdl.Rect) {
	ui := &termPtyUI
	_, th := measureText(font, "Ag")
	h := th + 20
	bar := sdl.Rect{X: grid.X + 6, Y: grid.Y + grid.H - h - 12, W: grid.W - 12, H: h + 6}
	fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, 12, glassSheetFill())
	strokeRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, 12, 1, glassEdge())
	widths := make([]int32, len(termPtyStripKeys))
	total := int32(0)
	for i, k := range termPtyStripKeys {
		tw, _ := measureText(font, k.label)
		widths[i] = tw + 24
		total += widths[i] + 6
	}
	// Scroll so the chosen key is in view.
	start := int32(0)
	for i := 0; i < ui.stripSel; i++ {
		start += widths[i] + 6
	}
	offset := int32(0)
	inner := bar.W - 16
	if total > inner {
		offset = start - inner/2 + widths[ui.stripSel]/2
		offset = max(0, min(offset, total-inner))
	}
	ui.stripRects = ui.stripRects[:0]
	x := bar.X + 8 - offset
	renderWithClip(renderer, bar.X+4, bar.Y, bar.W-8, bar.H, func(rr *sdl.Renderer) {
		for i, k := range termPtyStripKeys {
			rect := sdl.Rect{X: x, Y: bar.Y + 3, W: widths[i], H: h}
			sel := i == ui.stripSel
			if sel {
				fillRoundedRect(rr, rect.X, rect.Y, rect.W, rect.H, h/2, WithAlpha(glassAccent(), 230))
				drawText(rr, font, k.label, rect.X+12, rect.Y+10, ColorTextInverse(), textAlignLeft)
			} else {
				fillRoundedRect(rr, rect.X, rect.Y, rect.W, rect.H, h/2, glassA(18))
				drawText(rr, font, k.label, rect.X+12, rect.Y+10, ColorTextPrimary(), textAlignLeft)
			}
			ui.stripRects = append(ui.stripRects, rect)
			x += widths[i] + 6
		}
	})
}

// renderTermPtyMenu draws the session menu (Start).
func renderTermPtyMenu(renderer *sdl.Renderer, config *Config, font *ttf.Font) {
	ui := &termPtyUI
	drawOverlayScrim(renderer)
	rowH := int32(font.Height()) + 22
	w := int32(520)
	if w > screenWidth-40 {
		w = screenWidth - 40
	}
	h := int32(110) + int32(tpmCount)*(rowH+6)
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	drawOverlayCard(renderer, card)
	label := "Session"
	if s := termPty.sess; s != nil {
		label = s.label
	}
	y := UIPageHeaderDraw(renderer, config, overlayPageHeader(card, label, "Leaving keeps it running", "terminal")) + 10
	ui.menuRects = ui.menuRects[:0]
	for i := 0; i < tpmCount; i++ {
		row := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: rowH}
		UIListRow(renderer, row, glassRowRadius, i == ui.menuSel, pointInRect(mouseX, mouseY, row))
		col := ColorTextPrimary()
		if i == tpmEnd {
			col = DangerInk()
		}
		_, th := measureText(font, "Ag")
		drawText(renderer, font, termPtyMenuLabel(i), row.X+16, row.Y+(row.H-th)/2, col, textAlignLeft)
		ui.menuRects = append(ui.menuRects, row)
		y += rowH + 6
	}
}

// renderTermPtyHelp draws the Keys help card.
func renderTermPtyHelp(renderer *sdl.Renderer, config *Config, font *ttf.Font) {
	drawOverlayScrim(renderer)
	lines := termPtyHelpLines()
	lh := int32(font.Height()) + 6
	w := screenWidth - 80
	if w > 980 {
		w = 980
	}
	h := int32(100) + int32(len(lines))*lh
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	drawOverlayCard(renderer, card)
	closes := hintSentence("%s closes", "B", "Close")
	if hintsUseKeyboard() {
		closes = "Esc closes"
	}
	y := UIPageHeaderDraw(renderer, config, overlayPageHeader(card, "Keys", closes, "terminal")) + 8
	for _, l := range lines {
		col := ColorTextSecondary()
		if !strings.HasPrefix(l, " ") {
			col = glassAccent()
		}
		drawText(renderer, font, ellipsize(font, l, card.W-2*OverlayPadX), card.X+OverlayPadX, y, col, textAlignLeft)
		y += lh
	}
}
