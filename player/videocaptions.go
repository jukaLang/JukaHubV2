package main

// videocaptions.go — captions that come up on time and read neatly (Juka,
// 2026-11-26: "Make sure that captions download appropriately when playing
// selected youtube video and captions are enabled...make sure they show up
// appropriately and neatly").
//
// On time:
//   - With CC on (or Accessibility → Captions always on), Play on a YouTube
//     video starts its captions at once, beside the stream's resolve
//     (subtitlePrefetchStart, called by resolveAndStartPlayback): through
//     YouTube's own caption list only — no helper run beside the loading card
//     — into the subtitle cache. The fetch the player starts with the picture
//     joins it (subtitleNativeCaptions) instead of asking again, so the first
//     line is there with the first frame. Before, the fetch began only once the
//     stream had opened, and made a second player call of its own.
//   - The stream resolver's player answer carries the caption list too: it is
//     kept for a few minutes (ytCaptionStashPut) and read before any player
//     call is made for the captions.
//   - A caption address marked exp=xpe (or xpv) is one YouTube answers with an
//     empty body unless a proof-of-origin token comes with it — rolled out to
//     more and more videos through 2025-26. It is no longer fetched for
//     nothing: the next client, then the helper, is asked straight away.
//
// Neatly:
//   - A track becomes a timeline once (capBuildTimeline): whitespace tidied,
//     cues that overlap shown together, a pause under capBridgeGap closed so
//     the box does not blink off between two lines, repeats merged. Each frame
//     finds its line by binary search instead of walking every cue.
//   - Lines are set in a column of about 42 characters inside the picture and
//     wrapped so they balance — two lines of similar length, not a long one and
//     a stub — at most capMaxLines, each on its own rounded box (with Caption
//     background off, an outline all round instead of four offsets).
//   - Music notes, which Inter has no glyph for ("♪ la la ♪" drew as
//     "… la la …"), are drawn as the icon set's note. A Japanese, Chinese,
//     Korean, Arabic or Hebrew word is set in a face that has its letters
//     (webScriptWord, the Browser's fallback faces); Chinese and Japanese break
//     between characters, and a right-to-left line reads right to left.
//   - While captions are on their way the CC button pulses, and for the first
//     seconds a small note says so where they will appear.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// capBridgeGap: a pause between two cues shorter than this (seconds)
	// keeps the earlier line up until the next one starts.
	capBridgeGap = 0.3
	// capMaxActive is how many overlapping cues show at once, the newest
	// last.
	capMaxActive = 2
	// capMaxLines is the most lines a caption shows; the newest stay.
	capMaxLines = 3
	// capPendingMax is how long the "Getting … subtitles" note stays up while
	// a fetch runs; the CC button keeps pulsing after it.
	capPendingMax = 10 * time.Second
	// subtitlePrefetchTimeout bounds a prefetch; subtitleStashWait is how
	// long it waits for the stream resolver's player answer before asking for
	// the caption list itself.
	subtitlePrefetchTimeout = 20 * time.Second
	subtitleStashWait       = 1200 * time.Millisecond
	// subtitlePrefetchKeep is how long a finished prefetch's answer is kept
	// for a fetch that comes to join it.
	subtitlePrefetchKeep = 2 * time.Minute
)

// ── Timeline ─────────────────────────────────────────────────────────────────

// capPiece is one stretch of the timeline and the text shown over it.
type capPiece struct {
	start float64
	end   float64
	text  string
}

// capTimeline is the timeline built for the cue slice on screen; first, n
// and sig name that slice (a new track is a new slice). last is the piece the
// previous lookup found, where the next one starts. Guarded by videoSubs.mu.
var capTimeline struct {
	first  *subtitleCue
	n      int
	sig    float64
	pieces []capPiece
	last   int
}

// capTidyText collapses runs of white space inside each line and drops empty
// lines.
func capTidyText(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// capHasLine reports line among lines.
func capHasLine(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

// capBuildTimeline turns cues into the stretches the caption area shows. The
// time line is cut at every cue's start and end; each stretch shows the cues
// running over it (the newest capMaxActive, a line two of them share once),
// stretches with the same text that touch become one, and a pause under
// capBridgeGap is closed by keeping the line before it up.
func capBuildTimeline(cues []subtitleCue) []capPiece {
	type span struct {
		start, end float64
		text       string
	}
	spans := make([]span, 0, len(cues))
	for _, c := range cues {
		if !(c.End > c.Start) || math.IsNaN(c.Start) || math.IsInf(c.End, 0) {
			continue
		}
		if text := capTidyText(c.Text); text != "" {
			spans = append(spans, span{start: c.Start, end: c.End, text: text})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	cuts := make([]float64, 0, 2*len(spans))
	for _, s := range spans {
		cuts = append(cuts, s.start, s.end)
	}
	sort.Float64s(cuts)

	var pieces []capPiece
	var active []int
	next := 0
	for k := 0; k+1 < len(cuts); k++ {
		a, b := cuts[k], cuts[k+1]
		if b-a < 1e-6 {
			continue
		}
		for next < len(spans) && spans[next].start <= a {
			active = append(active, next)
			next++
		}
		kept := active[:0]
		for _, i := range active {
			if spans[i].end > a {
				kept = append(kept, i)
			}
		}
		active = kept
		if len(active) == 0 {
			continue
		}
		from := 0
		if len(active) > capMaxActive {
			from = len(active) - capMaxActive
		}
		var lines []string
		for _, i := range active[from:] {
			for _, line := range strings.Split(spans[i].text, "\n") {
				if !capHasLine(lines, line) {
					lines = append(lines, line)
				}
			}
		}
		text := strings.Join(lines, "\n")
		if n := len(pieces); n > 0 && pieces[n-1].text == text && a-pieces[n-1].end < 1e-6 {
			pieces[n-1].end = b
			continue
		}
		pieces = append(pieces, capPiece{start: a, end: b, text: text})
	}
	for i := 0; i+1 < len(pieces); i++ {
		if gap := pieces[i+1].start - pieces[i].end; gap > 0 && gap < capBridgeGap {
			pieces[i].end = pieces[i+1].start
		}
	}
	out := pieces[:0]
	for _, p := range pieces {
		if n := len(out); n > 0 && out[n-1].text == p.text && p.start-out[n-1].end < 1e-6 {
			out[n-1].end = p.end
			continue
		}
		out = append(out, p)
	}
	return out
}

// capTimelineText is the caption at seconds for cues. The timeline is built
// the first time a track is asked for and kept until the slice changes. The
// caller holds videoSubs.mu.
func capTimelineText(cues []subtitleCue, seconds float64) string {
	n := len(cues)
	if n == 0 {
		return ""
	}
	sig := cues[0].Start + 3*cues[n-1].End + float64(len(cues[0].Text)) + 7*float64(len(cues[n-1].Text))
	tl := &capTimeline
	if tl.first != &cues[0] || tl.n != n || tl.sig != sig {
		tl.first, tl.n, tl.sig = &cues[0], n, sig
		tl.pieces = capBuildTimeline(cues)
		tl.last = 0
	}
	p := tl.pieces
	// A frame almost always wants the piece the last one did.
	if i := tl.last; i < len(p) && p[i].start <= seconds && seconds < p[i].end {
		return p[i].text
	}
	i := sort.Search(len(p), func(i int) bool { return p[i].end > seconds })
	if i < len(p) && p[i].start <= seconds {
		tl.last = i
		return p[i].text
	}
	return ""
}

// subtitleDisplayAt is what the caption area shows at seconds: the caption,
// or — while CC is on, nothing is loaded yet and a fetch has been running
// under capPendingMax — what the fetch is doing.
func subtitleDisplayAt(seconds float64) (string, string) {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if !videoSubs.enabled {
		return "", ""
	}
	if len(videoSubs.cues) > 0 {
		return capTimelineText(videoSubs.cues, seconds), ""
	}
	if videoSubs.fetching && time.Since(videoSubs.fetchAt) < capPendingMax {
		return "", videoSubs.status
	}
	return "", ""
}

// subtitleFetchingNow reports captions on their way for the video on screen
// with CC on: the CC button pulses meanwhile.
func subtitleFetchingNow() bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	return videoSubs.enabled && videoSubs.fetching
}

// ── Laying a caption out ─────────────────────────────────────────────────────

// capTok is one piece a caption line is never broken inside: a word, part of
// one (a Chinese or Japanese character, a slice of a link wider than the
// column), or a run of music notes (note > 0), drawn as icons. gap is a space
// before it; face is the face its letters are set in.
type capTok struct {
	text string
	note int
	gap  bool
	face *ttf.Font
	w    int32
}

// capRun is a stretch of a laid-out line drawn in one go: text in one face,
// or a run of notes; lead is the room before it.
type capRun struct {
	text string
	note int
	face *ttf.Font
	w    int32
	lead int32
}

// capLine is one line of a caption, ready to draw.
type capLine struct {
	runs []capRun
	w    int32
}

// capLayoutCache is the caption laid out last: a cue stays up for seconds,
// and laying one out measures every word. Render thread.
var capLayoutCache struct {
	text  string
	font  *ttf.Font
	maxW  int32
	lines []capLine
}

// capIsNote reports a music note.
func capIsNote(r rune) bool {
	switch r {
	case '♩', '♪', '♫', '♬', '\U0001F3B5', '\U0001F3B6':
		return true
	}
	return false
}

// capIsCJK reports a Chinese character, kana, or full-width CJK punctuation:
// those lines carry no spaces and may break between any two characters.
func capIsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF01 && r <= 0xFF60)
}

// capNoBreakBefore is punctuation a line never starts with; capNoBreakAfter
// is what a line never ends with.
func capNoBreakBefore(r rune) bool {
	return strings.ContainsRune("、。，．・：；？！ー）」』】〕〉》｝］〜…‥ぁぃぅぇぉっゃゅょゎァィゥェォッャュョヮヵヶ.,!?;:)]}%'\"", r)
}

func capNoBreakAfter(r rune) bool {
	return strings.ContainsRune("（「『【〔〈《｛［([{", r)
}

// capBreakBetween reports a place a word may be broken: between notes and
// letters, and around a Chinese or Japanese character.
func capBreakBetween(prev, r rune) bool {
	pn, rn := capIsNote(prev), capIsNote(r)
	if pn != rn {
		return true
	}
	if rn || capNoBreakBefore(r) || capNoBreakAfter(prev) {
		return false
	}
	return capIsCJK(prev) || capIsCJK(r)
}

// capMeasure is text's width in face.
func capMeasure(face *ttf.Font, text string) int32 {
	if face == nil || text == "" {
		return 0
	}
	w, _, err := face.SizeUTF8(text)
	if err != nil {
		return 0
	}
	return int32(w)
}

// capNoteSize is the side of a note icon, and the room between two notes,
// for a face.
func capNoteSize(font *ttf.Font) (int32, int32) {
	side := int32(font.Height()) * 7 / 10
	return side, max32(1, side/6)
}

// capMakeToks turns one unbreakable stretch of a word into its piece — or,
// for a link wider than the column, into pieces that each fit.
func capMakeToks(font *ttf.Font, s string, gap bool, maxW int32) []capTok {
	notes := 0
	for _, r := range s {
		if capIsNote(r) {
			notes++
		}
	}
	if notes > 0 && notes == utf8.RuneCountInString(s) {
		side, room := capNoteSize(font)
		return []capTok{{note: notes, gap: gap, w: int32(notes)*side + int32(notes-1)*room}}
	}
	face, visual := webScriptWord(font, s)
	if w := capMeasure(face, visual); w <= maxW || maxW <= 0 {
		return []capTok{{text: visual, gap: gap, face: face, w: w}}
	}
	var out []capTok
	var cur []rune
	cut := func() {
		out = append(out, capTok{text: string(cur), gap: gap && len(out) == 0, face: face, w: capMeasure(face, string(cur))})
	}
	for _, r := range []rune(visual) {
		if len(cur) > 0 && capMeasure(face, string(cur)+string(r)) > maxW {
			cut()
			cur = cur[:0]
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		cut()
	}
	return out
}

// capTokens splits one line of a caption into the pieces it may be broken
// between.
func capTokens(font *ttf.Font, para string, maxW int32) []capTok {
	var toks []capTok
	for wi, word := range strings.Fields(para) {
		gap := wi > 0
		var run []rune
		emit := func() {
			if len(run) > 0 {
				toks = append(toks, capMakeToks(font, string(run), gap, maxW)...)
				gap = false
				run = run[:0]
			}
		}
		var prev rune
		for i, r := range []rune(word) {
			if i > 0 && capBreakBetween(prev, r) {
				emit()
			}
			run = append(run, r)
			prev = r
		}
		emit()
	}
	return toks
}

// capLead is the room before toks[i] on a line: a space for a new word, a
// hair between a note and the letters it touches, nothing inside a word.
func capLead(toks []capTok, i int, spaceW, noteRoom int32) int32 {
	switch {
	case toks[i].gap:
		return spaceW
	case toks[i].note > 0 || toks[i-1].note > 0:
		return noteRoom
	}
	return 0
}

// capWrap breaks toks into lines no wider than limit, greedily; it returns
// each line's token range.
func capWrap(toks []capTok, limit, spaceW, noteRoom int32) [][2]int {
	var out [][2]int
	start, cur := 0, int32(0)
	for i, t := range toks {
		add := t.w
		if i > start {
			add += capLead(toks, i, spaceW, noteRoom)
		}
		if i > start && cur+add > limit {
			out = append(out, [2]int{start, i})
			start, cur = i, t.w
			continue
		}
		cur += add
	}
	if start < len(toks) {
		out = append(out, [2]int{start, len(toks)})
	}
	return out
}

// capWrapBalanced is capWrap at the narrowest width that still needs no more
// lines than the column does: the same number of lines, of similar lengths.
func capWrapBalanced(toks []capTok, maxW, spaceW, noteRoom int32) [][2]int {
	ranges := capWrap(toks, maxW, spaceW, noteRoom)
	n := len(ranges)
	if n <= 1 {
		return ranges
	}
	lo, hi := int32(1), maxW
	for lo < hi {
		mid := lo + (hi-lo)/2
		if len(capWrap(toks, mid, spaceW, noteRoom)) <= n {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return capWrap(toks, lo, spaceW, noteRoom)
}

// capParaRTL reports a line whose first letter belongs to a right-to-left
// script.
func capParaRTL(para string) bool {
	for _, r := range para {
		if webRTLLetter(r) {
			return true
		}
		if unicode.IsLetter(r) {
			return false
		}
	}
	return false
}

// capBuildLine makes a line of toks ready to draw: words in one face join
// into one run, measured whole; a right-to-left line has its pieces in
// reverse, each word already turned by webScriptWord.
func capBuildLine(toks []capTok, rtl bool, spaceW, noteRoom int32) capLine {
	var runs []capRun
	for i, t := range toks {
		lead := int32(0)
		if i > 0 {
			lead = capLead(toks, i, spaceW, noteRoom)
		}
		if !rtl && t.note == 0 && len(runs) > 0 {
			if last := &runs[len(runs)-1]; last.note == 0 && last.face == t.face {
				if lead > 0 {
					last.text += " "
				}
				last.text += t.text
				continue
			}
		}
		runs = append(runs, capRun{text: t.text, note: t.note, face: t.face, w: t.w, lead: lead})
	}
	for i := range runs {
		if runs[i].note == 0 {
			runs[i].w = capMeasure(runs[i].face, runs[i].text)
		}
	}
	if rtl && len(runs) > 1 {
		n := len(runs)
		leads := make([]int32, n)
		for i, r := range runs {
			leads[i] = r.lead
		}
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			runs[i], runs[j] = runs[j], runs[i]
		}
		runs[0].lead = 0
		for j := 1; j < n; j++ {
			runs[j].lead = leads[n-j]
		}
	}
	var w int32
	for _, r := range runs {
		w += r.lead + r.w
	}
	return capLine{runs: runs, w: w}
}

// capLayout lays text out for font in a column maxW wide: each of its lines
// balanced on its own, the newest capMaxLines kept.
func capLayout(font *ttf.Font, text string, maxW int32) []capLine {
	c := &capLayoutCache
	if c.lines != nil && c.font == font && c.maxW == maxW && c.text == text {
		return c.lines
	}
	spaceW := capMeasure(font, " ")
	_, noteRoom := capNoteSize(font)
	var lines []capLine
	for _, para := range strings.Split(text, "\n") {
		toks := capTokens(font, para, maxW)
		if len(toks) == 0 {
			continue
		}
		rtl := capParaRTL(para)
		for _, r := range capWrapBalanced(toks, maxW, spaceW, noteRoom) {
			lines = append(lines, capBuildLine(toks[r[0]:r[1]], rtl, spaceW, noteRoom))
		}
	}
	if len(lines) > capMaxLines {
		lines = lines[len(lines)-capMaxLines:]
	}
	c.text, c.font, c.maxW, c.lines = text, font, maxW, lines
	return lines
}

// ── Drawing ──────────────────────────────────────────────────────────────────

// capPictureRect is where the picture sits inside the player at (x, y, w, h):
// captions are centred on it and set to its width.
func capPictureRect(x, y, w, h int32) sdl.Rect {
	embeddedPlayer.mu.Lock()
	vw, vh := embeddedPlayer.width, embeddedPlayer.height
	embeddedPlayer.mu.Unlock()
	return mediaFitRect(sdl.Rect{X: x, Y: y, W: w, H: h}, vw, vh)
}

// capColumnWidth is how wide a caption line may run: most of the picture's
// width, but no more than about 42 characters (the length subtitles are
// written to) — and, for a tall picture (a Short) in a wide player, a little
// past its sides rather than five lines deep.
func capColumnWidth(pic sdl.Rect, w int32, font *ttf.Font) int32 {
	col := pic.W * 88 / 100
	if wide := w * 6 / 10; col < wide {
		col = wide
	}
	if most := int32(font.Height()) * 18; col > most {
		col = most
	}
	if col > w-24 {
		col = w - 24
	}
	return max32(col, 40)
}

// capFontPx is the caption's size for a player h tall: a twenty-sixth,
// twentieth or fifteenth of it for Small, Medium and Large (Accessibility →
// Caption size), as players size it, so a mini player's captions shrink with
// it and a full-screen film's grow.
func capFontPx(h int32) int {
	div := int32(20)
	switch a11y.captionSize {
	case 0:
		div = 26
	case 2:
		div = 15
	}
	return int(min(max(h/div, 14), 72))
}

// capRender draws the caption at seconds over the player at (x, y, w, h), its
// last line bottomInset above the player's foot (renderVideoSubtitles).
func capRender(renderer *sdl.Renderer, config *Config, x, y, w, h int32, seconds float64, bottomInset int32) {
	if renderer == nil || config == nil || w <= 32 || h <= 0 {
		return
	}
	text, pending := subtitleDisplayAt(seconds)
	if text == "" && pending == "" {
		return
	}
	px := capFontPx(h)
	pic := capPictureRect(x, y, w, h)
	cx := pic.X + pic.W/2
	bottom := y + h - bottomInset
	if text == "" {
		capDrawPending(renderer, config, pending, cx, bottom, px)
		return
	}
	font := getFontSized(config, "medium", px)
	if font == nil {
		font, _ = getCachedFont(config, "medium")
	}
	if font == nil {
		return
	}
	lines := capLayout(font, text, capColumnWidth(pic, w, font))
	if len(lines) == 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fh := int32(font.Height())
	asc := int32(font.Ascent())
	boxed := a11y.captionBackground
	padX, padY := max32(6, fh/3), max32(1, fh/12)
	gap := max32(2, fh/14)
	lineH := fh + 2*padY
	step := lineH + gap
	if !boxed {
		padY = 0
		lineH = fh
		step = fh + max32(2, fh/10)
	}
	n := int32(len(lines))
	top := bottom - (n*step - (step - lineH))
	if top < y+6 {
		top = y + 6
	}
	radius := min32(lineH/2, max32(4, fh/5))
	edge := max32(1, fh/24)
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	shade := sdl.Color{R: 0, G: 0, B: 0, A: 210}
	noteSide, _ := capNoteSize(font)
	for i, line := range lines {
		ly := top + int32(i)*step
		lx := cx - line.w/2
		if lx < x+padX+2 {
			lx = x + padX + 2
		}
		if boxed {
			fillRoundedRect(renderer, lx-padX, ly, line.w+2*padX, lineH, radius, sdl.Color{R: 8, G: 8, B: 8, A: 190})
		}
		tx, ty := lx, ly+padY
		for _, r := range line.runs {
			tx += r.lead
			if r.note > 0 {
				capDrawNotes(renderer, r.note, tx, ty+(fh-noteSide)/2, noteSide, boxed, edge)
				tx += r.w
				continue
			}
			ry := ty + asc - int32(r.face.Ascent())
			if !boxed {
				// An outline all round keeps white letters readable over snow
				// or a white wall: eight offsets, not four.
				for _, d := range [8][2]int32{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
					drawTextRaw(renderer, r.face, r.text, tx+d[0]*edge, ry+d[1]*edge, shade, textAlignLeft)
				}
			}
			drawTextRaw(renderer, r.face, r.text, tx, ry, white, textAlignLeft)
			tx += r.w
		}
	}
}

// capDrawNotes draws count music notes from x, the icon set's note side px
// square: Inter has no ♪.
func capDrawNotes(renderer *sdl.Renderer, count int, x, y, side int32, boxed bool, edge int32) {
	room := max32(1, side/6)
	for i := 0; i < count; i++ {
		r := sdl.Rect{X: x + int32(i)*(side+room), Y: y, W: side, H: side}
		if !boxed {
			drawHomeGlyphTinted(renderer, "music", sdl.Rect{X: r.X + edge, Y: r.Y + edge, W: side, H: side}, sdl.Color{A: 210})
		}
		drawHomeGlyphTinted(renderer, "music", r, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	}
}

// capDrawPending is the note where captions will appear while they are on
// their way: a small dim pill with the CC glyph and what the fetch is doing.
func capDrawPending(renderer *sdl.Renderer, config *Config, msg string, cx, bottom int32, px int) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	font := getFontSized(config, "small", max(13, px*55/100))
	if font == nil {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	tw, th := measureText(font, msg)
	glyph := th
	pw, ph := 14+glyph+8+tw+14, th+12
	px0, py0 := cx-pw/2, bottom-ph
	fillRoundedRect(renderer, px0, py0, pw, ph, ph/2, sdl.Color{R: 8, G: 8, B: 8, A: 150})
	ink := sdl.Color{R: 210, G: 220, B: 228, A: 230}
	drawHomeGlyphTinted(renderer, "subtitles", sdl.Rect{X: px0 + 14, Y: py0 + (ph-glyph)/2, W: glyph, H: glyph}, ink)
	drawText(renderer, font, msg, px0+14+glyph+8, py0+(ph-th)/2, ink, textAlignLeft)
	requestFrameEvery(250)
}

// capDrawFetchDot is the CC button's pulse while captions are on their way:
// an accent dot in its corner, breathing.
func capDrawFetchDot(renderer *sdl.Renderer, r sdl.Rect) {
	if renderer == nil || r.W <= 0 {
		return
	}
	beat := float64(sdl.GetTicks64()%1000) / 1000
	a := uint8(120 + 135*(0.5+0.5*math.Sin(beat*2*math.Pi)))
	fillCircle(renderer, r.X+r.W-9, r.Y+9, 4, WithAlpha(vcAccent(), a))
	requestFrameEvery(50)
}

// ── Captions ready with the first frame ─────────────────────────────────────

// subtitlePrefetchRun is one prefetch: done closes once cues or err hold its
// answer, at doneAt.
type subtitlePrefetchRun struct {
	done   chan struct{}
	cues   []subtitleCue
	err    error
	doneAt time.Time
}

// subtitlePrefetches are the prefetches running or answered lately, by
// subtitleNoTrackKey.
var subtitlePrefetches struct {
	mu   sync.Mutex
	runs map[string]*subtitlePrefetchRun
}

// subtitlePrefetchStart starts a YouTube video's captions as its Play is
// pressed (resolveAndStartPlayback, a loader goroutine): when CC is on or
// Captions always on, the language is a code, nothing is cached or known
// about the page yet, and it is not the Shorts feed. Only the caption list is
// asked (no helper run beside the loading card); the answer goes to the cache
// and to the fetch that joins it (subtitleNativeCaptions).
func subtitlePrefetchStart(config *Config, page string) {
	id := youtubeVideoID(page)
	if id == "" || shortsAutoplay || !IsOnline() {
		return
	}
	lang, err := subtitleLanguage(config)
	if err != nil {
		return
	}
	videoSubs.mu.Lock()
	on := videoSubs.enabled || a11y.captionsAlways
	_, known := subtitleNoTrackLocked(page, lang)
	videoSubs.mu.Unlock()
	if !on || known || subtitleCacheHas(page, lang) {
		return
	}
	key := subtitleNoTrackKey(page, lang)
	if key == "" {
		return
	}
	run := &subtitlePrefetchRun{done: make(chan struct{})}
	now := time.Now()
	subtitlePrefetches.mu.Lock()
	if subtitlePrefetches.runs == nil {
		subtitlePrefetches.runs = make(map[string]*subtitlePrefetchRun)
	}
	for k, r := range subtitlePrefetches.runs {
		if !r.doneAt.IsZero() && now.Sub(r.doneAt) > subtitlePrefetchKeep {
			delete(subtitlePrefetches.runs, k)
		}
	}
	if old, ok := subtitlePrefetches.runs[key]; ok && old.doneAt.IsZero() {
		subtitlePrefetches.mu.Unlock()
		return // one is running already
	}
	subtitlePrefetches.runs[key] = run
	subtitlePrefetches.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), subtitlePrefetchTimeout)
		defer cancel()
		// The stream resolver's player answer, asked at the same moment, lists
		// the captions too: a moment's wait saves a second player call.
		ytCaptionStashWait(ctx, id, subtitleStashWait)
		cues, err := ytNativeCaptionsFor(ctx, id, lang)
		if len(cues) > 0 {
			subtitleCacheStore(page, lang, cues)
		}
		subtitlePrefetches.mu.Lock()
		run.cues, run.err, run.doneAt = cues, err, time.Now()
		subtitlePrefetches.mu.Unlock()
		close(run.done)
		if err != nil {
			logDebug("subs", "caption prefetch for %s: %v", id, err)
		} else {
			logDebug("subs", "caption prefetch for %s: %d cues", id, len(cues))
		}
	}()
}

// subtitleNativeCaptions is a fetch's caption-list step: the answer of a
// prefetch for the same page and language (waiting for one still running),
// else YouTube's caption list asked now. A prefetch cut short by its own time
// limit says nothing, and is asked again.
func subtitleNativeCaptions(ctx context.Context, page, id, lang string) ([]subtitleCue, error) {
	key := subtitleNoTrackKey(page, lang)
	subtitlePrefetches.mu.Lock()
	run := subtitlePrefetches.runs[key]
	subtitlePrefetches.mu.Unlock()
	if run != nil {
		select {
		case <-run.done:
			subtitlePrefetches.mu.Lock()
			cues, err := run.cues, run.err
			subtitlePrefetches.mu.Unlock()
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				return cues, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return ytNativeCaptionsFor(ctx, id, lang)
}

// ── The caption list the stream resolver saw ────────────────────────────────

// ytCaptionStashEntry is a video's caption list from a player answer, and
// when it came.
type ytCaptionStashEntry struct {
	tracks []ytCaptionTrack
	at     time.Time
}

// ytCaptionStash keeps the caption lists of the player answers the stream
// resolver got (youtubeNativeStream), by video id.
var ytCaptionStash struct {
	mu      sync.Mutex
	entries map[string]ytCaptionStashEntry
}

const (
	// ytCaptionStashFresh is how long a kept list is used: well inside the
	// hours a caption address stays signed for.
	ytCaptionStashFresh = 15 * time.Minute
	// ytCaptionStashMax bounds the lists kept (the Shorts feed resolves
	// four ahead).
	ytCaptionStashMax = 24
)

// ytCaptionStashPut keeps the caption list of a player answer that played.
func ytCaptionStashPut(videoID string, player *ytPlayerResponse) {
	if videoID == "" || player == nil || player.PlayabilityStatus.Status != "OK" {
		return
	}
	tracks, err := ytCaptionTracksOf(player)
	if err != nil {
		return
	}
	ytCaptionStash.mu.Lock()
	defer ytCaptionStash.mu.Unlock()
	if ytCaptionStash.entries == nil || len(ytCaptionStash.entries) >= ytCaptionStashMax {
		ytCaptionStash.entries = make(map[string]ytCaptionStashEntry)
	}
	ytCaptionStash.entries[videoID] = ytCaptionStashEntry{tracks: tracks, at: time.Now()}
}

// ytCaptionStashGet is the kept caption list of a video, while fresh.
func ytCaptionStashGet(videoID string) ([]ytCaptionTrack, bool) {
	ytCaptionStash.mu.Lock()
	defer ytCaptionStash.mu.Unlock()
	e, ok := ytCaptionStash.entries[videoID]
	if !ok || time.Since(e.at) > ytCaptionStashFresh {
		return nil, false
	}
	return e.tracks, true
}

// ytCaptionStashWait waits up to wait for a video's caption list to be kept,
// and reports whether it is.
func ytCaptionStashWait(ctx context.Context, videoID string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if _, ok := ytCaptionStashGet(videoID); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ytCaptionsFromStash fetches lang captions from a kept list. answered is
// false when there is none, or it could not answer (a fetch that failed: an
// address that expired or wants a token), and the clients are asked afresh.
func ytCaptionsFromStash(ctx context.Context, videoID, lang string) ([]subtitleCue, bool, error) {
	tracks, ok := ytCaptionStashGet(videoID)
	if !ok {
		return nil, false, nil
	}
	if len(tracks) == 0 {
		return nil, true, fmt.Errorf("the stream's player answer lists no caption tracks: %w", errNoCaptions)
	}
	choice, ok := ytChooseCaptionTrack(tracks, lang)
	if !ok {
		return nil, true, fmt.Errorf("no track in the stream's player answer serves %q: %w", lang, errNoCaptions)
	}
	cues, err := ytFetchCaptionTrack(ctx, choice)
	if err != nil {
		logDebug("subs", "kept caption list for %s did not answer, asking the clients: %v", videoID, err)
		return nil, false, nil
	}
	return cues, true, nil
}

// errCaptionNeedsToken is a caption address YouTube answers with an empty body
// unless a proof-of-origin token comes with it (exp=xpe or xpv in it).
var errCaptionNeedsToken = errors.New("the caption address needs a proof-of-origin token (exp=xpe)")

// ytCaptionNeedsToken reports such an address.
func ytCaptionNeedsToken(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	exp := strings.ToLower(u.Query().Get("exp"))
	return strings.Contains(exp, "xpe") || strings.Contains(exp, "xpv")
}
