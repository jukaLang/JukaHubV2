package main

// fileeditor_syntax_draw.go — the text editor's syntax colours on screen
// (fileeditor_syntax.go is the lexer): which language the open file is
// coloured as, the state each of its lines starts in, each display row cut
// into coloured pieces, and the theme's colour for each kind of token.
//
// A row is drawn as a few pieces instead of one string. Each piece is placed
// at the measured width of the row before it, and the cut is put just after
// a visible character — the blanks between two tokens ride at the front of
// the piece after them — because the width of text that ends in spaces is
// where fonts and SDL_ttf versions disagree, and a piece placed by it would
// drift. A row with a character the font lacks is drawn in one colour: the
// font substitutes such characters when it draws them, so measured widths
// and drawn widths would not match.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// filesViewSyntax reports whether code is coloured: on unless switched off
// (View → Syntax colours, or H on a keyboard), remembered with the settings.
func filesViewSyntax() bool { return filesViewPrefBool("fe_text_syntax", true) }

// filesToggleSyntax switches the colours on or off.
func filesToggleSyntax() {
	on := !filesViewSyntax()
	filesSetViewPref("fe_text_syntax", on)
	PlayNavSound()
	if on {
		showToast("Syntax colours on", ToastInfo())
	} else {
		showToast("Syntax colours off", ToastInfo())
	}
}

// fileEditorDetectSyntax is the language a file opens with: its name, else
// its shebang (a script called "run" or "start").
func fileEditorDetectSyntax(path, firstLine string) synLangID {
	if id := synLangByName(path); id != synNone {
		return id
	}
	return synLangByShebang(firstLine)
}

// fileEditorSyntaxNow is the language the page colours the text as now: none
// in the hex editor, for a binary file shown as text, and with the colours
// switched off.
func fileEditorSyntaxNow() synLangID {
	if !fileEditor.open || fileEditor.mode == filesViewHex || !filesViewSyntax() {
		return synNone
	}
	return fileEditor.syntax
}

// fileEditorStepSyntax picks the next (+1) or the previous language for the
// open file (View → Highlight as); the choice lasts while it is open.
func fileEditorStepSyntax(delta int) {
	n := int(synLangCount)
	fileEditor.syntax = synLangID(((int(fileEditor.syntax)+delta)%n + n) % n)
	fileEditor.syntaxSet = true
	filesWrapCacheReset()
	PlayNavSound()
}

// fileEditorSyntaxLabel is the Highlight as row's value: the language, and
// whether the file's name or first line chose it.
func fileEditorSyntaxLabel() string {
	name := synLangName(fileEditor.syntax)
	if !fileEditor.syntaxSet {
		return name + " · auto"
	}
	return name
}

// fileEditorLineIsProse reports whether the line keyboard capitalises
// sentences: in plain text yes, in code and configs no ("import os", not
// "Import os"), and never in the hex editor's prompts.
func fileEditorLineIsProse() bool {
	return fileEditor.open && fileEditor.syntax == synNone && !fileEditorVKHexInput()
}

// --- Line states --------------------------------------------------------------

// fileEditorSyn holds the state each row of the open file starts in, so
// drawing line 900 does not rescan lines 0–899 on every frame. It fills up
// to the furthest row drawn. An edit cuts it back to the edited line
// (fileEditorSynEdited); another file, another window of a large one or
// another language empties it.
var fileEditorSyn struct {
	path   string
	mode   filesViewMode
	start  int64
	lang   synLangID
	edits  int
	states []synState
}

// fileEditorSynStateAt is the state row idx of the page starts in.
func fileEditorSynStateAt(idx int, lang synLangID) synState {
	c := &fileEditorSyn
	if c.path != fileEditor.path || c.mode != fileEditor.mode || c.start != fileEditor.window.start ||
		c.lang != lang || c.edits != fileEditor.edits {
		c.path, c.mode, c.start, c.lang, c.edits = fileEditor.path, fileEditor.mode, fileEditor.window.start, lang, fileEditor.edits
		c.states = c.states[:0]
	}
	if n := fileEditorRowCount(); idx >= n {
		idx = n - 1
	}
	if idx <= 0 {
		return synState{}
	}
	if len(c.states) == 0 {
		c.states = append(c.states, synState{})
	}
	for len(c.states) <= idx {
		k := len(c.states) - 1
		next := synScan(lang, c.states[k], filesDisplayText(fileEditorRowText(k)), fileEditorRowEndsLine(k), nil)
		c.states = append(c.states, next)
	}
	return c.states[idx]
}

// fileEditorSynEdited keeps the states of the lines down to an edit at line,
// which the edit cannot have changed. Called right after fileEditorChanged.
func fileEditorSynEdited(line int) {
	c := &fileEditorSyn
	if c.path != fileEditor.path || c.mode != filesViewEdit || c.edits != fileEditor.edits-1 {
		return
	}
	if line < 0 {
		line = 0
	}
	if line+1 < len(c.states) {
		c.states = c.states[:line+1]
	}
	c.edits = fileEditor.edits
}

// fileEditorSynForget drops the states when the page closes.
func fileEditorSynForget() {
	fileEditorSyn.path = ""
	fileEditorSyn.states = nil
}

// fileEditorRowEndsLine reports whether row k of the page ends its line: a
// large file's viewer continues a long line on the next row.
func fileEditorRowEndsLine(k int) bool {
	if fileEditor.mode == filesViewEdit {
		return true
	}
	no := fileEditor.window.lineNo
	return k+1 >= len(no) || no[k+1] >= 0
}

// --- Pieces -------------------------------------------------------------------

// filesSeg is one coloured piece of a display row: its text, where it starts
// (pixels from the row's left edge) and its kind.
type filesSeg struct {
	text string
	x    int32
	kind synKind
}

// filesRowSegs is display row `row` of line idx cut into coloured pieces, or
// nil when the row is drawn in one colour. key is the frame's wrap key: the
// pieces are cached beside the wrapped rows and go with them.
func filesRowSegs(idx, row int, key filesWrapKey) []filesSeg {
	if key.syntax == synNone || key.font == nil {
		return nil
	}
	if filesWrapCache.segs == nil {
		filesWrapCache.segs = make(map[int][][]filesSeg, 64)
	}
	all, ok := filesWrapCache.segs[idx]
	if !ok {
		all = filesLineSegs(idx, key)
		if len(filesWrapCache.segs) > 512 {
			filesWrapCache.segs = make(map[int][][]filesSeg, 64)
		}
		filesWrapCache.segs[idx] = all
	}
	if row < 0 || row >= len(all) {
		return nil
	}
	return all[row]
}

// filesLineSegs colours line idx and cuts it the way filesRowsFor wrapped
// (or scrolled) it.
func filesLineSegs(idx int, key filesWrapKey) [][]filesSeg {
	rows := filesRowsFor(idx, key)
	text := filesDisplayText(fileEditorRowText(idx))
	var spans []synSpan
	synScan(key.syntax, fileEditorSynStateAt(idx, key.syntax), text, fileEditorRowEndsLine(idx), &spans)
	if len(spans) == 0 {
		return nil
	}
	out := make([][]filesSeg, len(rows))
	off := 0
	if !key.wrap {
		off = filesRuneByteOffset(text, key.hscroll)
	}
	for r, row := range rows {
		if off > len(text) || !strings.HasPrefix(text[off:], row) {
			break
		}
		out[r] = filesRowPieces(key.font, row, off, spans)
		off += len(row)
	}
	return out
}

// filesRuneByteOffset is the byte offset of rune n of s (len(s) past its
// end).
func filesRuneByteOffset(s string, n int) int {
	if n <= 0 {
		return 0
	}
	i := 0
	for pos := range s {
		if i == n {
			return pos
		}
		i++
	}
	return len(s)
}

// filesRowPieces cuts one display row — bytes [base, base+len(row)) of its
// line — into coloured pieces; nil draws it in one colour.
func filesRowPieces(font *ttf.Font, row string, base int, spans []synSpan) []filesSeg {
	if row == "" || sanitizeCached(font, row) != row {
		return nil
	}
	end := base + len(row)
	type piece struct {
		from, to int
		kind     synKind
	}
	ps := make([]piece, 0, 8)
	add := func(from, to int, kind synKind) {
		if from >= to {
			return
		}
		if n := len(ps); n > 0 && ps[n-1].kind == kind && ps[n-1].to == from {
			ps[n-1].to = to
			return
		}
		ps = append(ps, piece{from: from, to: to, kind: kind})
	}
	pos := base
	for _, sp := range spans {
		if sp.end <= pos {
			continue
		}
		if sp.start >= end {
			break
		}
		from, to := max(sp.start, pos), min(sp.end, end)
		add(pos, from, synPlain)
		add(from, to, sp.kind)
		pos = to
	}
	add(pos, end, synPlain)
	if len(ps) == 1 && ps[0].kind == synPlain {
		return nil
	}
	// The blanks after a piece move to the front of the next one.
	for k := 0; k+1 < len(ps); k++ {
		t := ps[k].to
		for t > ps[k].from && row[t-1-base] == ' ' {
			t--
		}
		ps[k].to, ps[k+1].from = t, t
	}
	segs := make([]filesSeg, 0, len(ps))
	for _, p := range ps {
		if p.from >= p.to {
			continue
		}
		x := int32(0)
		if p.from > base {
			x, _ = measureTextRaw(font, row[:p.from-base])
		}
		segs = append(segs, filesSeg{text: row[p.from-base : p.to-base], x: x, kind: p.kind})
	}
	return segs
}

// filesDrawSegs draws a row's pieces from x.
func filesDrawSegs(r *sdl.Renderer, config *Config, font *ttf.Font, segs []filesSeg, x, y int32) {
	pal := synPalette()
	for _, sg := range segs {
		filesDrawPlainText(r, config, font, sg.text, pal[sg.kind], x+sg.x, y)
	}
}

// --- Colours ------------------------------------------------------------------

// synDarkHues and synLightHues are the colours each kind starts from: One
// Dark's on a dark theme, One Light's on a light one. Comments are the
// quietest, keywords and strings the loudest.
var synDarkHues = [synKindCount]sdl.Color{
	synKeyword: {R: 198, G: 120, B: 221, A: 255},
	synType:    {R: 86, G: 182, B: 194, A: 255},
	synString:  {R: 152, G: 195, B: 121, A: 255},
	synNumber:  {R: 209, G: 154, B: 102, A: 255},
	synComment: {R: 127, G: 132, B: 142, A: 255},
	synFunc:    {R: 97, G: 175, B: 239, A: 255},
	synVar:     {R: 224, G: 108, B: 117, A: 255},
	synMeta:    {R: 229, G: 192, B: 123, A: 255},
	synConst:   {R: 209, G: 154, B: 102, A: 255},
	synKey:     {R: 224, G: 108, B: 117, A: 255},
}

var synLightHues = [synKindCount]sdl.Color{
	synKeyword: {R: 166, G: 38, B: 164, A: 255},
	synType:    {R: 1, G: 132, B: 188, A: 255},
	synString:  {R: 80, G: 161, B: 79, A: 255},
	synNumber:  {R: 152, G: 104, B: 1, A: 255},
	synComment: {R: 160, G: 161, B: 167, A: 255},
	synFunc:    {R: 64, G: 120, B: 242, A: 255},
	synVar:     {R: 228, G: 86, B: 73, A: 255},
	synMeta:    {R: 193, G: 132, B: 1, A: 255},
	synConst:   {R: 152, G: 104, B: 1, A: 255},
	synKey:     {R: 228, G: 86, B: 73, A: 255},
}

// synPaletteCache is the palette for the theme it was made for: TextInk
// does a contrast search per colour, too much for every piece of every row.
var synPaletteCache struct {
	key    [5]sdl.Color
	hc     bool
	valid  bool
	colors [synKindCount]sdl.Color
}

// synPalette is each kind's colour on the current theme, every hue moved by
// TextInk until it reads on all the surfaces text sits on (7:1 under
// Increase Contrast). Plain text is the body colour.
func synPalette() *[synKindCount]sdl.Color {
	p := &synPaletteCache
	key := [5]sdl.Color{ColorBackground, ColorSurface, ColorSurfaceAlt, ColorSurfaceRaised, textPrimaryColor}
	hc := a11y.increaseContrast
	if p.valid && p.key == key && p.hc == hc {
		return &p.colors
	}
	hues := &synDarkHues
	if glassLightTheme() {
		hues = &synLightHues
	}
	for k := range p.colors {
		p.colors[k] = TextInk(hues[k])
	}
	p.colors[synPlain] = ColorTextPrimary()
	p.key, p.hc, p.valid = key, hc, true
	return &p.colors
}
