package main

// fonts.go handles characters the UI font cannot draw.
//
// Inter (the bundled UI font) covers Latin, Greek and Cyrillic, but it has no
// glyphs for CJK, Thai, Arabic, Devanagari, Hebrew or emoji. SDL_ttf draws
// those as .notdef boxes ("tofu"), which is what made some YouTube titles in
// the Media gallery unreadable. We deliberately do not bundle extra fonts for
// them: instead, every character the font lacks is mapped to the closest
// supported character (full-width forms, typographic punctuation, CJK brackets,
// roman numerals, …) and, when there is no sensible match, collapsed into a
// single ellipsis so the title still reads as "something was here" instead of
// a row of boxes.
//
// The coverage check reads the font's own cmap table, so it is exact rather
// than a guess. This file also provides rune-safe truncation: slicing titles by
// bytes (the old behaviour) split multi-byte characters in half and produced
// mojibake even for scripts Inter supports.
//
// Everything here is standard library plus SDL_ttf; no extra assets, no new
// module dependencies.

import (
	"encoding/binary"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/veandco/go-sdl2/ttf"
)

// --- Glyph coverage (cmap) -------------------------------------------------

type runeRange struct{ lo, hi rune }

var (
	coverageMu    sync.Mutex
	coverageCache = map[string][]runeRange{} // nil = could not parse (assume covered)
)

// fontPaths remembers where each opened font lives on disk. renderText only
// receives the *ttf.Font, so this is how sanitizeForFont finds the font's cmap.
var (
	fontPathMu sync.RWMutex
	fontPaths  = map[*ttf.Font]string{}
)

func registerFontPath(f *ttf.Font, path string) {
	if f == nil || path == "" {
		return
	}
	fontPathMu.Lock()
	fontPaths[f] = path
	fontPathMu.Unlock()
}

func fontPathFor(f *ttf.Font) (string, bool) {
	fontPathMu.RLock()
	p, ok := fontPaths[f]
	fontPathMu.RUnlock()
	return p, ok
}

// fontCoversRune reports whether the font at path has a glyph for r. An
// unparsable font is treated as covering everything: refusing to draw text is
// worse than drawing a .notdef box for one character.
func fontCoversRune(path string, r rune) bool {
	ranges, ok := coverageFor(path)
	if !ok {
		return true
	}
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi >= r })
	return i < len(ranges) && ranges[i].lo <= r
}

func coverageFor(path string) ([]runeRange, bool) {
	if path == "" {
		return nil, false
	}
	coverageMu.Lock()
	cached, seen := coverageCache[path]
	coverageMu.Unlock()
	if seen {
		return cached, cached != nil
	}

	ranges, err := parseCMAP(resolvePath(path))
	if err != nil {
		// Remember the failure so a broken/missing font is not re-read per rune.
		coverageMu.Lock()
		coverageCache[path] = nil
		coverageMu.Unlock()
		return nil, false
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo < ranges[j].lo })
	ranges = mergeRanges(ranges)
	coverageMu.Lock()
	coverageCache[path] = ranges
	coverageMu.Unlock()
	return ranges, true
}

func mergeRanges(in []runeRange) []runeRange {
	out := in[:0]
	for _, r := range in {
		if r.lo > r.hi {
			continue
		}
		if n := len(out); n > 0 && r.lo <= out[n-1].hi+1 {
			if r.hi > out[n-1].hi {
				out[n-1].hi = r.hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// parseCMAP reads the character map of an sfnt font (TTF or OTF/CFF) and
// returns the covered code point ranges. Formats 0, 4, 6 and 12 are handled;
// those cover virtually every desktop and Noto font.
func parseCMAP(path string) ([]runeRange, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 {
		return nil, errShortFont
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	cmapOff := -1
	for i := 0; i < numTables; i++ {
		rec := 12 + i*16
		if rec+16 > len(data) {
			break
		}
		if string(data[rec:rec+4]) == "cmap" {
			cmapOff = int(binary.BigEndian.Uint32(data[rec+8 : rec+12]))
			break
		}
	}
	if cmapOff < 0 || cmapOff+4 > len(data) {
		return nil, errShortFont
	}
	n := int(binary.BigEndian.Uint16(data[cmapOff+2 : cmapOff+4]))
	// Preference order: Unicode full (format 12) first, then BMP (format 4).
	best := -1
	bestRank := 99
	for i := 0; i < n; i++ {
		rec := cmapOff + 4 + i*8
		if rec+8 > len(data) {
			break
		}
		platform := binary.BigEndian.Uint16(data[rec : rec+2])
		encoding := binary.BigEndian.Uint16(data[rec+2 : rec+4])
		off := cmapOff + int(binary.BigEndian.Uint32(data[rec+4:rec+8]))
		if off+2 > len(data) {
			continue
		}
		format := binary.BigEndian.Uint16(data[off : off+2])
		rank := 99
		switch {
		case format == 12 && platform == 3 && encoding == 10:
			rank = 0
		case format == 12:
			rank = 1
		case format == 4 && platform == 3 && encoding == 1:
			rank = 2
		case format == 4:
			rank = 3
		case format == 6:
			rank = 4
		case format == 0:
			rank = 5
		}
		if rank < bestRank {
			best, bestRank = off, rank
		}
	}
	if best < 0 {
		return nil, errShortFont
	}
	switch binary.BigEndian.Uint16(data[best : best+2]) {
	case 0:
		return parseCMAP0(data, best)
	case 4:
		return parseCMAP4(data, best)
	case 6:
		return parseCMAP6(data, best)
	case 12:
		return parseCMAP12(data, best)
	}
	return nil, errShortFont
}

func parseCMAP0(data []byte, off int) ([]runeRange, error) {
	if off+262 > len(data) {
		return nil, errShortFont
	}
	var out []runeRange
	start := -1
	for code := 0; code < 256; code++ {
		if data[off+6+code] != 0 {
			if start < 0 {
				start = code
			}
		} else if start >= 0 {
			out = append(out, runeRange{rune(start), rune(code - 1)})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, runeRange{rune(start), 255})
	}
	return out, nil
}

func parseCMAP4(data []byte, off int) ([]runeRange, error) {
	if off+14 > len(data) {
		return nil, errShortFont
	}
	segCount := int(binary.BigEndian.Uint16(data[off+6:off+8])) / 2
	if segCount == 0 {
		return nil, errShortFont
	}
	endBase := off + 14
	startBase := endBase + segCount*2 + 2
	deltaBase := startBase + segCount*2
	rangeBase := deltaBase + segCount*2
	if rangeBase+segCount*2 > len(data) {
		return nil, errShortFont
	}
	deltaAt := func(i int) int16 { return int16(binary.BigEndian.Uint16(data[deltaBase+i*2 : deltaBase+i*2+2])) }
	rangeAt := func(i int) uint16 { return binary.BigEndian.Uint16(data[rangeBase+i*2 : rangeBase+i*2+2]) }

	var out []runeRange
	for i := 0; i < segCount; i++ {
		end := rune(binary.BigEndian.Uint16(data[endBase+i*2 : endBase+i*2+2]))
		start := rune(binary.BigEndian.Uint16(data[startBase+i*2 : startBase+i*2+2]))
		if start > end || start == 0xFFFF {
			continue
		}
		// Map the segment's first code point; a zero glyph there means the
		// whole segment is a hole (common for unassigned blocks).
		glyph := 0
		if ro := rangeAt(i); ro != 0 {
			idx := rangeBase + i*2 + int(ro) + int(start)*2
			if idx+2 <= len(data) {
				glyph = int(binary.BigEndian.Uint16(data[idx : idx+2]))
				if glyph != 0 {
					glyph = (glyph + int(deltaAt(i))) % 65536
				}
			}
		} else {
			glyph = (int(start) + int(deltaAt(i))) % 65536
		}
		if glyph == 0 {
			continue
		}
		out = append(out, runeRange{start, end})
	}
	return out, nil
}

func parseCMAP6(data []byte, off int) ([]runeRange, error) {
	if off+10 > len(data) {
		return nil, errShortFont
	}
	first := rune(binary.BigEndian.Uint16(data[off+6 : off+8]))
	count := int(binary.BigEndian.Uint16(data[off+8 : off+10]))
	if off+10+count*2 > len(data) {
		return nil, errShortFont
	}
	var out []runeRange
	start := -1
	for i := 0; i < count; i++ {
		g := binary.BigEndian.Uint16(data[off+10+i*2 : off+10+i*2+2])
		if g != 0 {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			out = append(out, runeRange{first + rune(start), first + rune(i-1)})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, runeRange{first + rune(start), first + rune(count-1)})
	}
	return out, nil
}

func parseCMAP12(data []byte, off int) ([]runeRange, error) {
	if off+16 > len(data) {
		return nil, errShortFont
	}
	nGroups := int(binary.BigEndian.Uint32(data[off+12 : off+16]))
	if nGroups < 0 {
		return nil, errShortFont
	}
	var out []runeRange
	for i := 0; i < nGroups; i++ {
		rec := off + 16 + i*12
		if rec+12 > len(data) {
			break
		}
		lo := binary.BigEndian.Uint32(data[rec : rec+4])
		hi := binary.BigEndian.Uint32(data[rec+4 : rec+8])
		glyph := binary.BigEndian.Uint32(data[rec+8 : rec+12])
		if glyph == 0 || lo > hi || hi > 0x10FFFF {
			continue
		}
		out = append(out, runeRange{rune(lo), rune(hi)})
	}
	return out, nil
}

type fontParseError string

func (e fontParseError) Error() string { return string(e) }

const errShortFont = fontParseError("font table missing or truncated")

// --- Substitution ----------------------------------------------------------

// omittedMark stands in for a run of characters the font cannot draw. One mark
// per run (not per character) keeps titles tidy: "【MV】残酷な天使" reads as
// "[MV]…" instead of a wall of boxes.
const omittedMark = "…"

// sanitizeForFont rewrites text so the given font can actually draw it:
// unsupported characters become their closest supported form, and runs with no
// sensible substitute collapse to a single ellipsis. Supported text (Latin,
// Greek, Cyrillic) is returned untouched.
func sanitizeForFont(font *ttf.Font, text string) string {
	if font == nil || text == "" {
		return text
	}
	path, ok := fontPathFor(font)
	if !ok {
		return text
	}

	// Fast path: nothing to rewrite. This is every Latin/Greek/Cyrillic string,
	// so normal UI text never pays for the scan beyond the coverage lookup.
	needsWork := false
	for _, r := range text {
		if !fontCoversRune(path, r) {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	omitted := false
	skipMarks := false
	var last rune

	// writeOmission emits one ellipsis for the run of dropped characters and
	// separates it from the surrounding words, so text never runs together.
	// After punctuation ("[MV]…") no space is added, which keeps tags tidy.
	writeOmission := func(next rune) {
		if b.Len() > 0 && (unicode.IsLetter(last) || unicode.IsDigit(last) || unicode.IsSymbol(last)) {
			b.WriteByte(' ')
		}
		b.WriteString(omittedMark)
		if next != ' ' {
			b.WriteByte(' ')
		}
		last = '…'
	}

	for _, r := range text {
		if skipMarks && (unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r)) {
			continue
		}
		skipMarks = false

		sub, mapped := r, true
		if !fontCoversRune(path, r) {
			sub, mapped = lookalikeRune(r)
			if mapped && !fontCoversRune(path, sub) {
				mapped = false
			}
		}
		if !mapped {
			// Drop the character, but remember that something was omitted.
			if !unicode.IsSpace(r) {
				omitted = true
			}
			skipMarks = true
			continue
		}

		if omitted && !unicode.IsSpace(sub) {
			writeOmission(sub)
			omitted = false
		}
		b.WriteRune(sub)
		last = sub
	}
	if omitted {
		writeOmission(' ')
	}

	out := b.String()
	// Tidy up the spacing the substitution can leave behind, without touching
	// intentional indentation of supported text.
	for strings.Contains(out, "  ") {
		out = strings.ReplaceAll(out, "  ", " ")
	}
	return strings.TrimSpace(out)
}

// --- Lookalike substitution ------------------------------------------------

// lookalikeRune maps a character the UI font lacks to a visually similar
// supported character. It returns false when there is no sensible substitute
// (CJK ideographs, emoji), in which case the character is treated as omitted.
func lookalikeRune(r rune) (rune, bool) {
	switch {
	case r == 0x00A0, r == 0x3000, r == 0x2007, r == 0x202F:
		return ' ', true
	case r >= 0xFF01 && r <= 0xFF5E:
		// Full-width ASCII forms are exact lookalikes.
		return r - 0xFEE0, true
	case r == 0x3001, r == 0xFF64:
		return ',', true
	case r == 0x3002, r == 0xFF61:
		return '.', true
	case r == 0x30FB:
		return '·', true
	case r >= 0x2010 && r <= 0x2015, r == 0x2212:
		return '-', true
	case r == 0x301C, r == 0xFF5E, r == 0x223C:
		return '~', true
	case r == 0x2018, r == 0x2019, r == 0x201B, r == 0xFF07:
		return '\'', true
	case r == 0x201C, r == 0x201D, r == 0x201F:
		return '"', true
	case r == 0x2026:
		return '.', true
	case r == 0x3010: // 【
		return '[', true
	case r == 0x3011: // 】
		return ']', true
	case r == 0x300C, r == 0x300E: // 「 『
		return '"', true
	case r == 0x300D, r == 0x300F: // 」 』
		return '"', true
	case r == 0x3008, r == 0x300A: // 〈 《
		return '<', true
	case r == 0x3009, r == 0x300B: // 〉 》
		return '>', true
	case r >= 0x2160 && r <= 0x216F: // Roman numerals I..XII
		return rune('I' + (r - 0x2160)), true
	case r >= 0x2170 && r <= 0x217F:
		return rune('i' + (r - 0x2170)), true
	case r >= 0x2460 && r <= 0x2473: // Circled digits 1..20
		return rune('1' + (r - 0x2460)), true
	case r == 0x00D7:
		return 'x', true
	case r == 0x00F7:
		return '/', true
	case r == 0x2192, r == 0x21D2: // → ⇒
		return '>', true
	case r == 0x2190, r == 0x21D0: // ← ⇐
		return '<', true
	case r == 0x2191:
		return '^', true
	case r == 0x2193:
		return 'v', true
	case r == 0x2605, r == 0x2606: // ★ ☆
		return '*', true
	case r == 0x266A, r == 0x266B: // ♪ ♫
		return '~', true
	case r == 0x2665, r == 0x2661: // ♥ ♡
		return '<', true
	case r == 0x2713, r == 0x2714: // ✓ ✔
		return '+', true
	case r == 0x2717, r == 0x2718: // ✗ ✘
		return 'x', true
	case unicode.Is(unicode.Zs, r):
		return ' ', true
	}
	return 0, false
}

// --- Rune-safe truncation --------------------------------------------------

// truncateRunes shortens s to at most maxRunes characters, adding an ellipsis
// when it actually had to cut. Slicing by bytes (the old behaviour) split
// multi-byte characters in half and produced mojibake; this never does.
func truncateRunes(s string, maxRunes int) string {
	return truncateRunesSuffix(s, maxRunes, "…")
}

// truncateRunesSuffix is truncateRunes with a caller-chosen suffix.
func truncateRunesSuffix(s string, maxRunes int, suffix string) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + suffix
		}
		count++
	}
	return s
}

// fitTextToWidth clips text so it fits maxW pixels, measuring the substituted
// text (what will actually be drawn) so trimming matches rendering.
func fitTextToWidth(font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return text
	}
	display := sanitizeForFont(font, text)
	widthOf := func(s string) int32 {
		w, _, err := font.SizeUTF8(s)
		if err != nil {
			return 0
		}
		return int32(w)
	}
	if widthOf(display) <= maxW {
		return display
	}
	runes := []rune(display)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if widthOf(string(runes[:mid])+"…") <= maxW {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return "…"
	}
	return string(runes[:lo]) + "…"
}
