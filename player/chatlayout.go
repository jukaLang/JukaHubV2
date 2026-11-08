package main

// chatlayout.go — wrapping one chat message into lines, and remembering the
// answer.
//
// The old renderer wrapped every visible message on every frame, measuring
// each candidate line through FreeType, and it ran the link regexes and the
// time formatting for each bubble as well. On the Smart Pro that was most of
// what made a long chat slow to scroll. A message is immutable once it is in
// the history, so its layout is computed once per font and width and kept in
// a bounded cache: a laid-out bubble costs the frame a map lookup.
//
// Wrapping is word based and UTF-8 safe: lines break at spaces, explicit line
// breaks are kept (a run of blank lines collapses to one), a word wider than
// the bubble is split between characters but never inside a combining
// sequence, and every link becomes a run of its own so it can be drawn in the
// accent ink with an underline, and tapped, even when a long URL had to be
// split across two lines.

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/ttf"
)

// chatSeg is one run of a wrapped line drawn in one style.
type chatSeg struct {
	text string
	x    int32  // offset from the start of the line
	w    int32  // measured width
	url  string // the link this run belongs to; "" for plain text
}

// chatLine is one wrapped line of a bubble.
type chatLine struct {
	segs []chatSeg
	w    int32
}

// chatTextLayout is a message body wrapped for one font and width.
type chatTextLayout struct {
	lines     []chatLine
	width     int32 // the widest line
	truncated bool  // the text ran past the line cap and ends with an ellipsis
}

// chatMaxLines caps one bubble. A pasted wall of text still reads in full
// through Copy; the list should not turn one message into a screen of pages.
const chatMaxLines = 60

// chatPiece is part of a word with one style: a whole plain word, or the part
// of a word that is a link ("(https://x.y)," is three pieces).
type chatPiece struct {
	text string
	url  string
	w    int32
}

// chatTextReplacer normalises line endings and tabs before wrapping.
var chatTextReplacer = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", " ")

// chatWrapper carries the state of one wrap.
type chatWrapper struct {
	font      *ttf.Font
	maxW      int32
	maxLines  int
	spaceW    int32
	lines     []chatLine
	cur       []chatSeg
	curW      int32
	width     int32
	full      bool
	truncated bool
}

// chatWrapText wraps text into lines no wider than maxW (as measured with
// font), at most maxLines of them.
func chatWrapText(font *ttf.Font, text string, maxW int32, maxLines int) chatTextLayout {
	if maxLines <= 0 {
		maxLines = chatMaxLines
	}
	if font == nil || maxW <= 0 {
		if strings.TrimSpace(text) == "" {
			return chatTextLayout{}
		}
		return chatTextLayout{lines: []chatLine{{segs: []chatSeg{{text: text}}}}}
	}
	text = chatTextReplacer.Replace(text)
	if strings.TrimSpace(text) == "" {
		return chatTextLayout{}
	}
	// What is drawn is the text the font can show (fonts.go maps the rest),
	// so that is also what gets measured.
	text = sanitizeForFont(font, text)
	w := &chatWrapper{font: font, maxW: maxW, maxLines: maxLines}
	w.spaceW, _ = measureText(font, " ")
	if w.spaceW <= 0 {
		w.spaceW = 1
	}
	blankPending := false
	for _, para := range strings.Split(text, "\n") {
		if w.full {
			break
		}
		if strings.TrimSpace(para) == "" {
			// A blank line between paragraphs is kept once; leading blank
			// lines and runs of them are not.
			blankPending = len(w.lines) > 0
			continue
		}
		if blankPending {
			w.endLine(true)
			blankPending = false
		}
		w.paragraph(para)
		w.endLine(false)
	}
	if w.truncated && len(w.lines) > 0 {
		last := &w.lines[len(w.lines)-1]
		var b strings.Builder
		for _, s := range last.segs {
			b.WriteString(s.text)
		}
		cut := ellipsizeLine(font, b.String(), maxW)
		cw, _ := measureText(font, cut)
		*last = chatLine{segs: []chatSeg{{text: cut, w: cw}}, w: cw}
		if cw > w.width {
			w.width = cw
		}
	}
	return chatTextLayout{lines: w.lines, width: w.width, truncated: w.truncated}
}

// paragraph lays out one paragraph (a line of the source text).
func (w *chatWrapper) paragraph(para string) {
	spans := chatURLSpans(para)
	i := 0
	for i < len(para) && !w.full {
		for i < len(para) && para[i] == ' ' {
			i++
		}
		if i >= len(para) {
			break
		}
		start := i
		for i < len(para) && para[i] != ' ' {
			i++
		}
		w.word(chatWordPieces(para, start, i, spans))
	}
}

// word places one word: on the current line when it fits, on the next line
// when it does not, and split between characters when it is wider than a
// whole line.
func (w *chatWrapper) word(pieces []chatPiece) {
	if len(pieces) == 0 {
		return
	}
	wordW := int32(0)
	for i := range pieces {
		pieces[i].w, _ = measureText(w.font, pieces[i].text)
		wordW += pieces[i].w
	}
	if w.curW > 0 {
		if w.curW+w.spaceW+wordW <= w.maxW {
			w.add(" ", "", w.spaceW)
			for _, p := range pieces {
				w.add(p.text, p.url, p.w)
			}
			return
		}
		w.endLine(false)
		if w.full {
			return
		}
	}
	if wordW <= w.maxW {
		for _, p := range pieces {
			w.add(p.text, p.url, p.w)
		}
		return
	}
	for _, p := range pieces {
		w.split(p)
		if w.full {
			return
		}
	}
}

// split places a piece that does not fit, a line's worth at a time.
func (w *chatWrapper) split(p chatPiece) {
	rest := p.text
	for rest != "" && !w.full {
		room := w.maxW - w.curW
		restW, _ := measureText(w.font, rest)
		if restW <= room {
			w.add(rest, p.url, restW)
			return
		}
		cut := chatFitPrefix(w.font, rest, room)
		if cut <= 0 {
			if w.curW > 0 {
				w.endLine(false)
				continue
			}
			// Not even one character fits an empty line: take one anyway so
			// the loop always moves forward.
			cut = chatFirstBreak(rest)
		}
		partW, _ := measureText(w.font, rest[:cut])
		w.add(rest[:cut], p.url, partW)
		rest = rest[cut:]
		w.endLine(false)
	}
}

// add appends text to the current line, joining the previous run when the
// style matches so a line of plain words is one texture, not one per word.
func (w *chatWrapper) add(text, url string, width int32) {
	if n := len(w.cur); n > 0 && w.cur[n-1].url == url {
		w.cur[n-1].text += text
		w.cur[n-1].w += width
	} else {
		w.cur = append(w.cur, chatSeg{text: text, url: url, w: width})
	}
	w.curW += width
}

// endLine closes the current line. An empty line is kept only for a blank
// line between paragraphs.
func (w *chatWrapper) endLine(keepEmpty bool) {
	if w.full {
		return
	}
	if len(w.cur) == 0 && !keepEmpty {
		return
	}
	if len(w.lines) >= w.maxLines {
		w.full, w.truncated = true, true
		w.cur, w.curW = w.cur[:0], 0
		return
	}
	segs := make([]chatSeg, 0, len(w.cur))
	x := int32(0)
	for i, s := range w.cur {
		if i == len(w.cur)-1 {
			s.text = strings.TrimRight(s.text, " ")
		}
		if s.text == "" {
			continue
		}
		// Re-measured as one run: the sum of word widths drifts from what
		// FreeType draws by a pixel or two per space.
		s.w, _ = measureText(w.font, s.text)
		s.x = x
		x += s.w
		segs = append(segs, s)
	}
	w.lines = append(w.lines, chatLine{segs: segs, w: x})
	if x > w.width {
		w.width = x
	}
	w.cur, w.curW = w.cur[:0], 0
}

// chatURLSpans returns the byte ranges of the links in s, without the
// punctuation that usually follows a link in prose.
func chatURLSpans(s string) [][2]int {
	if !strings.Contains(s, "://") {
		return nil
	}
	found := urlRegex.FindAllStringIndex(s, -1)
	if len(found) == 0 {
		return nil
	}
	spans := make([][2]int, 0, len(found))
	for _, m := range found {
		u := trimChatURL(s[m[0]:m[1]])
		if u == "" {
			continue
		}
		spans = append(spans, [2]int{m[0], m[0] + len(u)})
	}
	return spans
}

// chatWordPieces splits the word para[start:end] where links begin and end.
func chatWordPieces(para string, start, end int, spans [][2]int) []chatPiece {
	pieces := make([]chatPiece, 0, 1)
	pos := start
	for _, sp := range spans {
		if sp[1] <= pos || sp[0] >= end {
			continue
		}
		s, e := max(sp[0], pos), min(sp[1], end)
		if s > pos {
			pieces = append(pieces, chatPiece{text: para[pos:s]})
		}
		pieces = append(pieces, chatPiece{text: para[s:e], url: para[sp[0]:sp[1]]})
		pos = e
	}
	if pos < end {
		pieces = append(pieces, chatPiece{text: para[pos:end]})
	}
	return pieces
}

// chatZWJ is the zero-width joiner that glues emoji sequences together.
const chatZWJ = rune(0x200d)

// chatJoinsPrevious reports whether r belongs to the character before it: a
// combining mark, a joiner, a variation selector or a skin-tone modifier. A
// word is never split in front of one.
func chatJoinsPrevious(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc) ||
		r == chatZWJ ||
		(r >= 0xfe00 && r <= 0xfe0f) ||
		(r >= 0x1f3fb && r <= 0x1f3ff)
}

// chatBreakOffsets lists the byte offsets s may be split at, ending with
// len(s).
func chatBreakOffsets(s string) []int {
	offs := make([]int, 0, utf8.RuneCountInString(s))
	afterJoiner := false
	for i, r := range s {
		if i > 0 && !afterJoiner && !chatJoinsPrevious(r) {
			offs = append(offs, i)
		}
		afterJoiner = r == chatZWJ
	}
	return append(offs, len(s))
}

// chatFirstBreak is the end of the first character cluster of s.
func chatFirstBreak(s string) int {
	offs := chatBreakOffsets(s)
	if len(offs) == 0 {
		return len(s)
	}
	return offs[0]
}

// chatFitPrefix returns the longest prefix of s (in bytes, at a character
// boundary) that is at most room wide, or 0 when not even one character fits.
func chatFitPrefix(font *ttf.Font, s string, room int32) int {
	if room <= 0 || s == "" {
		return 0
	}
	offs := chatBreakOffsets(s)
	best := 0
	lo, hi := 0, len(offs)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		w, _ := measureText(font, s[:offs[mid]])
		if w <= room {
			best = offs[mid]
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// chatLineText is the text of a wrapped line, for tests and the clipped-link
// rule.
func chatLineText(line chatLine) string {
	if len(line.segs) == 1 {
		return line.segs[0].text
	}
	var b strings.Builder
	for _, s := range line.segs {
		b.WriteString(s.text)
	}
	return b.String()
}

// ── Per-message layout cache ─────────────────────────────────────────────

// chatBubbleLayout is everything about a message that depends only on the
// message, the body font and the wrap width: what the renderer would otherwise
// recompute for every bubble on every frame.
type chatBubbleLayout struct {
	body     chatTextLayout
	image    string   // the picture shown under the text, "" for none
	cards    []string // link preview cards, at most chatLinkCardLimit
	clock    string   // "14:03" (or "2:03 PM")
	initials string   // what the avatar circle shows without a picture
}

// chatLayoutKey identifies one layout. A message is immutable once it is in
// the history; n still takes part because a streamed answer grows under one id,
// and the timestamp keeps two id-less messages with the same text apart.
type chatLayoutKey struct {
	id      string
	n       int
	ts      int64
	maxW    int32
	font    *ttf.Font
	clock12 bool
}

// chatLayoutCacheMax bounds the cache. A screenful is a dozen bubbles; the rest
// is scroll-back that is cheap to rebuild, so the cache is dropped wholesale
// when it fills instead of paying for an LRU list.
const chatLayoutCacheMax = 512

var chatLayouts = make(map[chatLayoutKey]*chatBubbleLayout, 128)

// clearChatLayoutCache forgets every cached layout (font change, shutdown).
func clearChatLayoutCache() {
	chatLayouts = make(map[chatLayoutKey]*chatBubbleLayout, 128)
	chatStreamLayout.lay = nil
}

// chatClock12 reports whether times are shown on a 12-hour clock.
func chatClock12() bool {
	return appConfig != nil && appConfig.Variables.Clock12
}

// chatClockLabel formats a message time in the device's zone.
func chatClockLabel(t time.Time, twelve bool) string {
	if t.IsZero() {
		return ""
	}
	if twelve {
		return t.Local().Format("3:04 PM")
	}
	return t.Local().Format("15:04")
}

// chatLayoutFor returns the layout of msg wrapped to maxW with font, building
// it on first use.
func chatLayoutFor(font *ttf.Font, msg *ChatMessage, maxW int32) *chatBubbleLayout {
	twelve := chatClock12()
	key := chatLayoutKey{id: msg.ID, n: len(msg.Text), ts: msg.Timestamp.UnixNano(), maxW: maxW, font: font, clock12: twelve}
	if key.id == "" {
		key.id = msg.Text
	}
	if l, ok := chatLayouts[key]; ok {
		return l
	}
	if len(chatLayouts) >= chatLayoutCacheMax {
		clearChatLayoutCache()
	}
	l := chatBuildLayout(font, msg, maxW, twelve)
	chatLayouts[key] = l
	return l
}

// chatBuildLayout computes a layout without caching it (the streamed answer
// changes every few frames and is laid out on its own).
func chatBuildLayout(font *ttf.Font, msg *ChatMessage, maxW int32, twelve bool) *chatBubbleLayout {
	l := &chatBubbleLayout{
		body:     chatWrapText(font, msg.Text, maxW, chatMaxLines),
		image:    chatMessageImageURL(*msg),
		clock:    chatClockLabel(msg.Timestamp, twelve),
		initials: chatInitials(msg.Sender),
	}
	for _, link := range chatMessageLinks(msg.Text) {
		if link != l.image {
			l.cards = append(l.cards, link)
		}
	}
	return l
}

// chatInitials is what a sender's avatar circle shows: the first letters of
// the first and last words of the name ("Juka AI" -> "JA", "steppa" -> "S").
func chatInitials(name string) string {
	var first, last rune
	words := 0
	for _, field := range strings.Fields(name) {
		for _, r := range field {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				if words == 0 {
					first = r
				} else if unicode.IsLetter(r) {
					last = r
				}
				words++
				break
			}
		}
	}
	if first == 0 {
		return "?"
	}
	out := string(unicode.ToUpper(first))
	if last != 0 {
		out += string(unicode.ToUpper(last))
	}
	return out
}
