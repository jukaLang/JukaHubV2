package main

// browser_anchor.go — the reader keeps its place while the page is redrawn.
//
// A page reaches the screen in steps: the markup its server sent while its
// scripts run, then the page they built; its pictures and web fonts arrive
// after it is drawn, and each batch of them lays it out again (layoutFor). The
// reader's scroll is a row number, and each of those steps moved the rows
// under it: someone reading halfway down an article found another paragraph
// in front of them when a picture above the view got its height, or when the
// finished page replaced the early one.
//
// Browsers call the cure scroll anchoring, and the reader now does it too:
// when the page on screen is laid out again, or replaced by a newer version
// of the same address, the first line with text at the top of the view is
// looked up in the new layout — the same words, nearest to where the page's
// growth would put them — and the scroll moves so that line stays where it
// was. A line the new layout no longer has (the scripts removed it) leaves the
// scroll as it was. A new page, Back, Forward and Reload start from their own
// places (browserLoadNavigation forgets the anchor; the restore of a page's
// remembered row runs after it).

import "strings"

const (
	// brwAnchorLook is how many rows below the top of the view the anchor
	// line may be: a picture or a gap at the top has no words to find.
	brwAnchorLook = 40
	// brwAnchorSig is how much of the text from the anchor line on names it.
	brwAnchorSig = 64
	// brwAnchorWords is how much of it a page set in another font, whose
	// lines broke elsewhere, is found by.
	brwAnchorWords = 24
	// brwAnchorWindow is how many rows around the expected place the new
	// layout is searched.
	brwAnchorWindow = 900
)

// brwAnchor is the layout the reader drew last and its page's address.
// Render thread.
var brwAnchor struct {
	layout *webLayout
	url    string
}

// brwAnchorForget starts over: a new page has no place to keep. Render
// thread.
func brwAnchorForget() {
	brwAnchor.layout, brwAnchor.url = nil, ""
}

// brwAnchorScroll returns the row the reader should be at in layout, the page
// about to be drawn, when it was at scroll in the layout drawn last: unchanged
// unless layout is a new layout of the same address, where the line at the top
// of the view is followed into it. Render thread.
func brwAnchorScroll(doc *webDocument, layout *webLayout, scroll int32) int32 {
	if doc == nil || layout == nil {
		return scroll
	}
	prev, prevURL := brwAnchor.layout, brwAnchor.url
	brwAnchor.layout, brwAnchor.url = layout, doc.URL
	if prev == nil || prev == layout || prevURL == "" || prevURL != doc.URL || scroll <= 0 {
		return scroll
	}
	if row, ok := brwAnchorFind(prev, layout, int(scroll)); ok {
		return int32(row)
	}
	return scroll
}

// brwAnchorFind maps the top of the view at row scroll of old to a row of
// next: the anchor line (the first with text, at most brwAnchorLook rows down)
// is found in next — by its words, nearest to where it would be had the page
// grown evenly — and the view starts as far above it as before.
func brwAnchorFind(old, next *webLayout, scroll int) (int, bool) {
	if scroll < 0 || scroll >= len(old.Rows) || len(next.Rows) == 0 {
		return 0, false
	}
	anchor := -1
	for i := scroll; i < len(old.Rows) && i < scroll+brwAnchorLook; i++ {
		if brwRowText(old.Rows[i].Runs) != "" {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return 0, false
	}
	above := anchor - scroll
	want := brwRowsText(old.Rows, anchor, brwAnchorSig)
	lead := brwRowText(old.Rows[anchor].Runs)
	words := brwCutWords(want, brwAnchorWords)
	estimate := anchor * len(next.Rows) / len(old.Rows)
	lo, hi := max(estimate-brwAnchorWindow, 0), min(estimate+brwAnchorWindow, len(next.Rows)-1)
	// The same line: same first words, same text from there on.
	best, bestD := -1, 0
	for j := lo; j <= hi; j++ {
		if brwRowText(next.Rows[j].Runs) != lead || brwRowsText(next.Rows, j, brwAnchorSig) != want {
			continue
		}
		if d := brwAbs(j - estimate); best < 0 || d < bestD {
			best, bestD = j, d
		}
	}
	if best < 0 && len(words) >= 8 {
		// The lines broke elsewhere (a web font arrived): the line that holds
		// the anchor's first words, or starts them at its end.
		for j := lo; j <= hi; j++ {
			text := brwRowText(next.Rows[j].Runs)
			if text == "" {
				continue
			}
			if j+1 < len(next.Rows) {
				text += " " + brwRowText(next.Rows[j+1].Runs)
			}
			if !strings.Contains(text, words) {
				continue
			}
			if d := brwAbs(j - estimate); best < 0 || d < bestD {
				best, bestD = j, d
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	return max(best-above, 0), true
}

// brwRowText is one row's words as one line.
func brwRowText(runs []webRunPos) string {
	var b strings.Builder
	for i := range runs {
		run := &runs[i]
		if run.Invisible || run.Image != nil || strings.TrimSpace(run.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strings.TrimSpace(run.Text))
	}
	return b.String()
}

// brwRowsText is the text from row i on, rows joined by spaces, cut to n
// bytes: what a line says does not depend on where the next line breaks.
func brwRowsText(rows []webRow, i, n int) string {
	var b strings.Builder
	for ; i < len(rows) && b.Len() < n; i++ {
		text := brwRowText(rows[i].Runs)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(text)
	}
	s := b.String()
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// brwCutWords cuts s to at most n bytes at a word boundary.
func brwCutWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if cut := strings.LastIndexByte(s[:n], ' '); cut > 0 {
		return s[:cut]
	}
	return s[:n]
}

func brwAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
