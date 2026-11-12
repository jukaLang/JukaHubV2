package main

// tts_text.go — what Juka says out loud, made from what Juka wrote.
//
// A model's answer is written for the screen: markdown, emoji, code, links and
// lists. Read out as written, a voice says "asterisk asterisk" and spells out
// addresses, so the text is made speakable first: a code block becomes "the
// code is on the screen", a link its words (a bare address "a link"), and the
// emphasis marks, emoji, bullets, tags and square brackets go — Groq's voice
// reads a word in brackets as a direction ("[whisper]"), not as text. Every
// line ends as a sentence, so a list is read item by item instead of as one
// breathless run.
//
// Only the first ttsSpokenMax characters of an answer are spoken; the rest
// stays on screen and the voice says so. A long answer would otherwise spend
// most of the day's free voice allowance on one reply.
//
// Groq's voice takes at most 200 characters a request, so the speakable text
// is cut into chunks at sentence ends — a sentence longer than a chunk at a
// comma, semicolon, colon or dash, and only then at a space. The first chunk
// is kept short: it is synthesised first and starts playing while the rest is
// fetched, and a short request comes back sooner. Everything here is pure.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// ttsSpokenMax is how much of one answer is spoken, in characters.
	ttsSpokenMax = 600
	// ttsChunkMax is the longest chunk sent to Groq, in bytes. Its limit is
	// 200 characters; counting bytes stays under it however the server counts.
	ttsChunkMax = 195
	// ttsFirstChunk is roughly how long the first chunk is: the first
	// sentence, joined by the next ones while it is shorter than this.
	ttsFirstChunk = 60
	// ttsInlineCodeMax is the longest inline code that is still read out.
	ttsInlineCodeMax = 40
	// ttsRestOnScreen is said when an answer was cut short.
	ttsRestOnScreen = "The rest is on the screen."
)

var (
	ttsFenceRe      = regexp.MustCompile("(?s)```.*?(?:```|$)")
	ttsInlineCodeRe = regexp.MustCompile("`([^`\n]*)`")
	ttsLinkRe       = regexp.MustCompile(`!?\[([^\]\n]*)\]\([^)\n]*\)`)
	ttsURLRe        = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>()\[\]]+`)
	ttsTagRe        = regexp.MustCompile(`</?[A-Za-z][^<>\n]*>`)
	ttsBulletRe     = regexp.MustCompile(`^(?:[-*+•>]+|\d{1,3}[.)])\s+`)
	ttsHeadingRe    = regexp.MustCompile(`^#{1,6}\s*`)
	ttsTableRuleRe  = regexp.MustCompile(`^[\s|:\-]+$`)
)

// ttsSymbols are the symbols a voice should say as words, or skip. Longer
// marks come before the single characters they start with: a replacer tries
// its pairs in this order at each position.
var ttsSymbols = strings.NewReplacer(
	"**", "", "__", "", "~~", "", "*", "", "_", " ",
	"->", " to ", "=>", " to ", "<-", " from ",
	"→", " to ", "⇒", " to ", "×", " times ", "&", " and ", "°", " degrees ",
	"[", " ", "]", " ", "{", " ", "}", " ", "<", " ", ">", " ", "|", ", ",
	"\\", " ", "^", " ", "=", " equals ", "#", " ",
)

// ttsPrepare turns an answer into the chunks Juka speaks, in order: the
// speakable text, capped at ttsSpokenMax characters, cut into chunks of at
// most ttsChunkMax bytes. An answer with nothing to say gives none.
func ttsPrepare(text string) []string {
	sentences := ttsCap(ttsSentences(ttsSpeakable(text)), ttsSpokenMax)
	return ttsChunks(sentences, ttsChunkMax)
}

// ttsSpeakable is text as a voice should read it: one line of plain sentences
// with no markdown, code, links, tags or emoji.
func ttsSpeakable(text string) string {
	text = chatStripThink(text)
	text = ttsFenceRe.ReplaceAllString(text, "\nThe code is on the screen.\n")
	text = ttsInlineCodeRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := strings.TrimSpace(strings.Trim(m, "`"))
		if utf8.RuneCountInString(inner) > ttsInlineCodeMax {
			return "code"
		}
		return inner
	})
	text = ttsLinkRe.ReplaceAllString(text, "$1")
	text = ttsURLRe.ReplaceAllString(text, "a link")
	text = ttsTagRe.ReplaceAllString(text, " ")
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || ttsTableRuleRe.MatchString(line) {
			continue
		}
		line = ttsHeadingRe.ReplaceAllString(line, "")
		line = ttsBulletRe.ReplaceAllString(line, "")
		line = strings.Trim(line, "| ")
		line = strings.Join(strings.Fields(ttsDropSymbols(ttsSymbols.Replace(line))), " ")
		if line == "" {
			continue
		}
		// A line that ends without punctuation (a heading, a list item) ends
		// as a sentence, so the voice pauses there.
		if last, _ := utf8.DecodeLastRuneInString(line); !strings.ContainsRune(".!?:;,…", last) {
			line += "."
		}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(line)
	}
	// The text starts with a word, a number or a quote, never with a stray
	// mark a bullet left behind ("- 5" keeps its minus).
	s := out.String()
	for s != "" {
		r, size := utf8.DecodeRuneInString(s)
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(`"“‘'($£€¿¡`, r) ||
			(r == '-' && len(s) > size && s[size] >= '0' && s[size] <= '9') {
			break
		}
		s = s[size:]
	}
	return s
}

// ttsDropSymbols removes what a voice cannot say: emoji (with their joiners,
// skin tones and variation selectors), pictographs, dingbats, private-use
// characters and control characters.
func ttsDropSymbols(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r == 0x200D, r == 0xFE0E, r == 0xFE0F, r == 0x20E3,
			r >= 0x1F000 && r <= 0x1FAFF, r >= 0x2600 && r <= 0x27BF,
			r >= 0x2B00 && r <= 0x2BFF, r >= 0xE0000 && r <= 0xE007F,
			unicode.Is(unicode.Co, r), unicode.Is(unicode.So, r), unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// ttsSentences splits speakable text after the marks that end a sentence: a
// full stop, question or exclamation mark or an ellipsis, followed by a space
// and then a capital, a digit or an opening quote — so "e.g. the", "3.5" and
// "a.m. today" stay whole.
func ttsSentences(s string) []string {
	rs := []rune(s)
	var out []string
	start := 0
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '.', '!', '?', '…':
		default:
			continue
		}
		end := i + 1
		for end < len(rs) && strings.ContainsRune(`"')”’»`, rs[end]) {
			end++
		}
		if end < len(rs) && rs[end] != ' ' {
			continue
		}
		next := end
		for next < len(rs) && rs[next] == ' ' {
			next++
		}
		if next < len(rs) && !unicode.IsUpper(rs[next]) && !unicode.IsDigit(rs[next]) &&
			!strings.ContainsRune(`"'“‘(«`, rs[next]) {
			continue
		}
		if t := strings.TrimSpace(string(rs[start:end])); t != "" {
			out = append(out, t)
		}
		start = next
		i = next - 1
	}
	if start < len(rs) {
		if t := strings.TrimSpace(string(rs[start:])); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ttsCap keeps the sentences that fit in limit characters, and says that the
// rest is on the screen when any were left out. A first sentence longer than
// limit is cut at a pause or a space.
func ttsCap(sentences []string, limit int) []string {
	var out []string
	total := 0
	for i, s := range sentences {
		n := utf8.RuneCountInString(s)
		if total+n <= limit {
			out = append(out, s)
			total += n + 1
			continue
		}
		if i == 0 {
			for _, piece := range ttsSplitLong(s, ttsChunkMax) {
				n := utf8.RuneCountInString(piece)
				if total+n > limit {
					break
				}
				out = append(out, piece)
				total += n + 1
			}
		}
		return append(out, ttsRestOnScreen)
	}
	return out
}

// ttsChunks packs sentences into chunks of at most max bytes: the first chunk
// short (ttsFirstChunk), the others as full as they go, so a reply costs as
// few requests as it can.
func ttsChunks(sentences []string, max int) []string {
	var out []string
	cur := ""
	for _, s := range sentences {
		for _, piece := range ttsSplitLong(s, max) {
			switch {
			case cur == "":
				cur = piece
			case len(cur)+1+len(piece) <= max && (len(out) > 0 || len(cur) < ttsFirstChunk):
				cur += " " + piece
			default:
				out = append(out, cur)
				cur = piece
			}
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ttsSplitLong cuts a sentence longer than max bytes into pieces that fit:
// after the last comma, semicolon, colon or dash that leaves a piece of at
// least a third of max, else at the last space, else at max itself (on a
// character boundary).
func ttsSplitLong(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := -1
		for _, sep := range []string{", ", "; ", ": ", " — ", " – ", " - "} {
			if i := strings.LastIndex(s[:max], sep); i >= max/3 && i+len(sep)-1 > cut {
				cut = i + len(sep) - 1
			}
		}
		if cut < 0 {
			cut = strings.LastIndexByte(s[:max], ' ')
		}
		if cut <= 0 {
			cut = max
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			if cut == 0 {
				cut = max
			}
		}
		if piece := strings.TrimSpace(s[:cut]); piece != "" {
			out = append(out, piece)
		}
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
