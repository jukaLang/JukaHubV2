package main

import (
	"strings"
	"unicode/utf8"
)

// Notification text: the word wrap the toast and the notification centre
// share, and the arithmetic that sizes them. Every function here takes a
// measure callback (font.SizeUTF8 in the app, a fixed advance in the tests), so
// the layout is pinned without loading a font.
//
// Both surfaces used to cut long messages short. The toast split its text by
// pixel count into two lines ("…every fil" / "e on the device, system files
// i…") inside a card capped at 420×96, and the centre ellipsized every row to
// two lines with no way to see the rest, so a long error could not be read
// anywhere.

// notifEllipsis marks text that was cut. The bundled Inter carries U+2026.
const notifEllipsis = "…"

// wrapWordsMeasured word-wraps text into lines no wider than maxW. An explicit
// newline starts a new line. A token wider than a whole line (a path, a URL, a
// run of CJK) is cut into pieces that fit, preferably just after a separator,
// instead of being ellipsized down to its first few characters.
//
// With maxLines > 0 at most that many lines come back; when the text needed
// more, the last one ends in an ellipsis and cut is true. The only line that
// can be wider than maxW is a single glyph that is wider on its own.
func wrapWordsMeasured(text string, maxW int32, maxLines int, measure func(string) int32) (lines []string, cut bool) {
	if measure == nil || maxW <= 0 {
		if t := strings.TrimSpace(text); t != "" {
			return []string{t}, false
		}
		return nil, false
	}
	emit := func(line string) bool {
		if maxLines > 0 && len(lines) >= maxLines {
			cut = true
			return false
		}
		lines = append(lines, line)
		return true
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
paragraphs:
	for _, para := range strings.Split(text, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			if cur == "" {
				if measure(word) <= maxW {
					cur = word
					continue
				}
			} else if measure(cur+" "+word) <= maxW {
				cur += " " + word
				continue
			} else if measure(word) <= maxW {
				if !emit(cur) {
					break paragraphs
				}
				cur = word
				continue
			}
			var ok bool
			if cur, ok = wrapLongToken(cur, word, maxW, measure, emit); !ok {
				break paragraphs
			}
		}
		if cur != "" && !emit(cur) {
			break
		}
	}
	if cut && len(lines) > 0 {
		lines[len(lines)-1] = fitEllipsisMeasured(lines[len(lines)-1], maxW, measure)
	}
	return lines, cut
}

// wrapLongToken lays out a token wider than a line after the open line cur.
// The first piece shares the open line when a third of it is still free ("Saved
// to /storage/…"); the middle pieces fill whole lines; the last piece is
// returned as the new open line, so the words after the token can follow it.
// It reports false once emit has refused a line (the line budget is spent).
func wrapLongToken(cur, word string, maxW int32, measure func(string) int32, emit func(string) bool) (string, bool) {
	prefix := ""
	if cur != "" {
		if maxW-measure(cur+" ") >= maxW/3 {
			prefix = cur + " "
		} else if !emit(cur) {
			return "", false
		}
	}
	rest := []rune(word)
	for len(rest) > 0 {
		n := fitRunesMeasured(prefix, rest, maxW, measure)
		if n == 0 {
			if prefix != "" {
				// Not one glyph fits after the open line: close it first.
				if !emit(strings.TrimRight(prefix, " ")) {
					return "", false
				}
				prefix = ""
				continue
			}
			n = 1 // a glyph wider than the whole line goes out on its own
		}
		if n < len(rest) {
			n = preferTokenCut(rest, n)
		}
		piece := prefix + string(rest[:n])
		rest = rest[n:]
		if len(rest) == 0 {
			return piece, true
		}
		if !emit(piece) {
			return "", false
		}
		prefix = ""
	}
	return strings.TrimRight(prefix, " "), true
}

// fitRunesMeasured is how many of runes fit after prefix within maxW.
func fitRunesMeasured(prefix string, runes []rune, maxW int32, measure func(string) int32) int {
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if measure(prefix+string(runes[:mid])) <= maxW {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// preferTokenCut moves a cut inside an over-long token back to just after a
// separator when one sits in the back half of the piece: "/storage/roms/psp/"
// then "Castlevania.iso" reads far better than ".../Castlev" then "ania.iso".
// Path separators win over the URL and file-name punctuation.
func preferTokenCut(runes []rune, n int) int {
	for _, seps := range []string{`/\`, "-_.?&=,;:"} {
		for i := n - 1; i >= n/2 && i > 0; i-- {
			if strings.ContainsRune(seps, runes[i]) {
				return i + 1
			}
		}
	}
	return n
}

// fitEllipsisMeasured ends line with an ellipsis, trimming it from the end until
// both fit in maxW.
func fitEllipsisMeasured(line string, maxW int32, measure func(string) int32) string {
	line = strings.TrimRight(line, " ")
	if measure(line+notifEllipsis) <= maxW {
		return line + notifEllipsis
	}
	runes := []rune(line)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if measure(string(runes[:mid])+notifEllipsis) <= maxW {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return strings.TrimRight(string(runes[:lo]), " ") + notifEllipsis
}

// ── Toast card ──────────────────────────────────────────────────────────────

const (
	// toastMaxLines is how many lines a toast shows. Four hold about 170
	// characters on either canvas; a longer message ends in "…" and is kept
	// whole in the notification centre, where a click on the card opens it.
	toastMaxLines = 4
	toastPad      = int32(16)
	toastStripW   = int32(10) // the severity strip and the gap after it
	toastMinW     = int32(120)
	toastMinH     = int32(52)
	// toastNarrowMaxW was the fixed cap; toastWideMaxW bounds the canvas share.
	toastNarrowMaxW = int32(420)
	toastWideMaxW   = int32(600)
	// The card sits toastMarginRight from the right edge and never closer than
	// toastMarginMin to any other edge.
	toastMarginRight = int32(24)
	toastMarginMin   = int32(16)

	// A toast stays up long enough to be read: about 1.5 s to notice the card
	// plus 50 ms a character (some 20 characters a second, an unhurried read),
	// never shorter than its kind's slot and never longer than 10 s. The old
	// fixed 2.5 s took a 90-character confirmation away mid-sentence.
	toastReadBaseMS    = 1500
	toastReadPerRuneMS = 50
	toastReadMaxMS     = 10000
)

// toastMaxWidth is the widest the toast card gets on a canvas screenW wide:
// 45% of it, so a long message wraps into a few readable lines instead of a
// tall column (576 px on the 1280 canvas, 432 on the Brick's 960), never
// narrower than the old fixed 420, never wider than 600, and always inside the
// screen's margins.
func toastMaxWidth(screenW int32) int32 {
	w := screenW * 45 / 100
	w = max(w, toastNarrowMaxW)
	w = min(w, toastWideMaxW)
	w = min(w, screenW-toastMarginRight-toastMarginMin)
	return max(w, toastMinW)
}

// toastTextWidth is the room a message has inside a card cardW wide.
func toastTextWidth(cardW int32) int32 {
	return max(cardW-toastPad*2-toastStripW, 1)
}

// toastCardSize sizes the card around its wrapped lines: it hugs the widest
// line (between toastMinW and maxW) and is exactly as tall as the lines plus
// padding. The old 96 px ceiling clipped the second line at the larger text
// sizes.
func toastCardSize(widest int32, lines int, lineH, maxW int32) (w, h int32) {
	w = widest + toastPad*2 + toastStripW
	w = min(max(w, toastMinW), maxW)
	h = int32(max(lines, 1))*lineH + toastPad*2
	return w, max(h, toastMinH)
}

// toastDurationForMessage is how long a toast of this kind and text stays on
// screen: toastDurationFor's slot, stretched for a long message. Screenshot
// mode's zero and the short scene-switch label are kept as they are.
func toastDurationForMessage(kind ToastKind, msg string) uint64 {
	base := toastDurationFor(kind)
	if base == 0 || kind == ToastKindSwitch {
		return base
	}
	read := uint64(toastReadBaseMS + toastReadPerRuneMS*utf8.RuneCountInString(msg))
	return max(base, min(read, toastReadMaxMS))
}

// ── Notification centre rows ────────────────────────────────────────────────

// notifRowHeight is a history row's height for a message of lines lines: the
// padding, the message, a 2 px gap and the timestamp line under it.
func notifRowHeight(lines int, lineH int32) int32 {
	return max(notifRowMinH, notifRowPad*2+int32(lines)*lineH+2+lineH)
}

// notifExpandedLines is how many lines the highlighted row may use: as many as
// fit on a card avail tall with that row alone, and never fewer than a
// collapsed row's two.
func notifExpandedLines(avail, lineH int32) int {
	if lineH <= 0 {
		return notifMsgLines
	}
	return max(int((avail-notifRowPad*2-2-lineH)/lineH), notifMsgLines)
}

// notifSumHeights is the total height of a run of rows.
func notifSumHeights(heights []int32) int32 {
	total := int32(0)
	for _, h := range heights {
		total += h
	}
	return total
}

// notifListWindow picks the history rows the card draws: count rows from
// first, never more than maxRows and never taller than avail together. The
// cursor row is always one of them (the window moves from scroll just far
// enough to bring it on), so the D-pad, L1/R1, End and the wheel reach every
// row of the history. The list used to start at the newest row every frame and
// only ever drew one card-full; the cursor could be paged onto rows that were
// never drawn. At the end of the list the window fills upwards, so the card
// does not shrink to a few rows while older ones are hidden above.
func notifListWindow(heights []int32, avail int32, maxRows, cursor, scroll int) (first, count int) {
	n := len(heights)
	if n == 0 {
		return 0, 0
	}
	maxRows = max(maxRows, 1)
	cursor = clampIndex(cursor, n)
	first = min(clampIndex(scroll, n), cursor)
	for first < cursor && (cursor-first+1 > maxRows || notifSumHeights(heights[first:cursor+1]) > avail) {
		first++
	}
	total := int32(0)
	for i := first; i < n && count < maxRows; i++ {
		if count > 0 && total+heights[i] > avail {
			break
		}
		total += heights[i]
		count++
	}
	for first+count == n && first > 0 && count < maxRows && total+heights[first-1] <= avail {
		first--
		total += heights[first]
		count++
	}
	return first, count
}
