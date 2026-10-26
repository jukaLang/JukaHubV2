package main

// browserfind.go — searching the page that is already on screen.
//
// A reading browser without find-in-page sends the reader back to the top of a
// long issue or release page and lets them scroll: on a 5-inch screen with a
// D-pad, "find the paragraph that mentions the fan curve" is otherwise a
// minute of paging. This searches the laid-out rows the renderer already
// holds — no second parse, no DOM — and jumps the scroll to each match in
// turn.
//
// Matching is per row, which is per visual line: the layout has already
// wrapped the text, so a phrase that spans a wrap is not found. That is the
// honest limit of a row-grid reader and it is one line of text rather than a
// paragraph, so the misses are rare and never silent-looking.
//
// The query is typed into its own keyboard buffer (browserFindVariable), never
// into the address bar: cancelling the keyboard leaves the page on screen
// exactly where it was.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// browserFindVariable holds the query typed for an in-page search. It is
	// separate from the address and from the web search, so cancelling any
	// keyboard leaves both untouched.
	browserFindVariable = "browser_find_query"
	browserFindPrompt   = "Find on this page"
	// browserFindMaxMatches bounds the match list. A page that matches a
	// one-letter query thousands of times still steps through the first
	// hundred rather than building a list the D-pad can never walk.
	browserFindMaxMatches = 200
)

// browserFindState is one in-page search. The key is the page's identity, so
// navigating away resets the list the way the link cursor resets: a match
// index must never point at a different page's row.
type browserFindState struct {
	key    string
	query  string
	rows   []int
	index  int
	active bool
}

var browserFind browserFindState

// browserFindReset clears an in-page search. It runs on navigation and on a
// test reset, so a stale match list cannot be drawn over a new page.
func browserFindReset() {
	browserFind = browserFindState{}
}

// browserFindPageKey identifies the page a match list belongs to: the document
// URL when there is one, otherwise the element's variable. Both matter — a
// variable can hold a different page after a reload, and two variables can
// hold the same page.
func browserFindPageKey(elem Element) string {
	if doc := browserDocumentFor(elem.Variable); doc != nil && strings.TrimSpace(doc.URL) != "" {
		return elem.Variable + "|" + doc.URL
	}
	return elem.Variable + "|"
}

// browserFindRowText is one laid-out row as a single string: the text of every
// run on that line, concatenated in reading order. Runs are already in x order,
// so this is the line as the reader sees it.
func browserFindRowText(row webRow) string {
	if len(row.Runs) == 0 {
		return ""
	}
	if len(row.Runs) == 1 {
		return row.Runs[0].Text
	}
	var sb strings.Builder
	for i := range row.Runs {
		sb.WriteString(row.Runs[i].Text)
	}
	return sb.String()
}

// browserFindScan collects the rows matching a query, case-insensitively. It
// returns at most browserFindMaxMatches rows in ascending order, so stepping
// is always down the page and wraps cleanly.
func browserFindScan(layout *webLayout, query string) []int {
	query = strings.TrimSpace(query)
	if layout == nil || query == "" {
		return nil
	}
	needle := strings.ToLower(query)
	rows := make([]int, 0, 8)
	for i := range layout.Rows {
		if strings.Contains(strings.ToLower(browserFindRowText(layout.Rows[i])), needle) {
			rows = append(rows, i)
			if len(rows) >= browserFindMaxMatches {
				break
			}
		}
	}
	return rows
}

// browserFindMatches returns the match list for the page on screen, rebuilding
// it only when the page or the query changed. One scan of the rows is cheap
// (the text is already in memory), but doing it on every D-pad press would
// still be waste on a 1 GB handheld.
func browserFindMatches(elem Element, layout *webLayout) []int {
	key := browserFindPageKey(elem) + "\x00" + strings.ToLower(strings.TrimSpace(browserFind.query))
	if browserFind.key == key {
		return browserFind.rows
	}
	browserFind.rows = browserFindScan(layout, browserFind.query)
	browserFind.key = key
	if browserFind.index >= len(browserFind.rows) {
		browserFind.index = 0
	}
	return browserFind.rows
}

// browserFindReveal scrolls the page so a matching row is visible, using the
// same rule as the link cursor: only move when the row would otherwise be off
// screen, so reading position survives a step that lands in view.
func browserFindReveal(elem Element, layout *webLayout, row int) {
	max := browserScrollMax(elem, layout)
	if max <= 0 {
		textBrowserScrollY = 0
		return
	}
	visible := browserVisibleRows(elem, layout)
	scroll := int(textBrowserScrollY)
	if row < scroll {
		scroll = row
	} else if row >= scroll+visible {
		scroll = row - visible + 1
	}
	if scroll < 0 {
		scroll = 0
	}
	if int32(scroll) > max {
		scroll = int(max)
	}
	textBrowserScrollY = int32(scroll)
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
}

// browserFindStep moves to the next (dir > 0) or previous match and reveals it.
// It wraps at both ends so the pad is never stuck, and it reports whether a
// search was actually in progress — a caller that gets false has pressed a key
// that belongs to someone else.
func browserFindStep(config *Config, elem Element, dir int) bool {
	if !browserFind.active || strings.TrimSpace(browserFind.query) == "" {
		return false
	}
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return false
	}
	rows := browserFindMatches(elem, layout)
	if len(rows) == 0 {
		// The query stopped matching (a re-render changed the wrap). Say so
		// rather than pretending the key did nothing.
		ShowToast("No matches for \u201c"+truncate(browserFind.query, 24)+"\u201d", ToastKindInfo)
		return true
	}
	browserFind.index = (browserFind.index + dir + len(rows)) % len(rows)
	browserFindReveal(elem, layout, rows[browserFind.index])
	PlayNavSound()
	return true
}

// browserFindClear ends the search and drops the highlight, so the reader gets
// the page back whole. The query is kept in the variable: opening Find again
// starts from the last search rather than an empty field, which is what a
// reader who scrolled away and wants to look again expects.
func browserFindClear() {
	if !browserFind.active {
		return
	}
	browserFind.active = false
	browserFind.rows = nil
	browserFind.key = ""
	browserFind.index = 0
	PlayBackSound()
}

// browserFindApply runs a search that was just typed. It is what the keyboard
// submit calls: the query is taken from its own variable, the match list is
// rebuilt, and the page jumps to the first hit. A query with no matches says
// so and leaves the page where it is, because silently clearing the search is
// how a reader concludes the feature is broken.
func browserFindApply(config *Config, elem Element) {
	query, _ := config.Variables.Custom[browserFindVariable].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		browserFindClear()
		return
	}
	browserFind.query = query
	browserFind.active = true
	browserFind.index = 0
	browserFind.key = ""

	layout := browserPageLayout(config, elem)
	if layout == nil {
		// A page with no styled document (plain-text fallback) has no rows to
		// search; say so rather than arming a search that can never match.
		browserFind.active = false
		ShowToast("This page cannot be searched", ToastKindInfo)
		return
	}
	// The same query again means "go to the next one", not "start over": a
	// controller has no N key, and pressing Find a second time is the press a
	// user reaches for when they want to keep stepping. This is the only way
	// a pad can walk the matches, so it has to do the useful thing.
	if browserFind.active && strings.EqualFold(strings.TrimSpace(browserFind.query), query) {
		if rows := browserFindMatches(elem, layout); len(rows) > 0 {
			browserFind.index = (browserFind.index + 1) % len(rows)
			browserFindReveal(elem, layout, rows[browserFind.index])
			PlayNavSound()
			return
		}
	}
	rows := browserFindMatches(elem, layout)
	if len(rows) == 0 {
		ShowToast("No matches for \u201c"+truncate(query, 24)+"\u201d", ToastKindInfo)
		return
	}
	browserFindReveal(elem, layout, rows[0])
	PlayActivateSound()
}

// openBrowserFind is the Find shortcut: it opens the keyboard for a query and
// searches the page on screen. The address bar and the web search are both
// untouched, so a cancelled keyboard costs nothing.
func openBrowserFind(renderer *sdl.Renderer, config *Config, elem Element) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	for i, current := range config.Scenes[currentSceneIndex].Elements {
		if current.Type != "textbrowser" || current.Variable != elem.Variable {
			continue
		}
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		// Pre-fill the last query so a reader can adjust one word instead of
		// retyping the whole phrase on a D-pad.
		config.Variables.Custom[browserFindVariable] = browserFind.query
		textBrowserFindEditing = true
		textBrowserFindSubmitted = false
		vkOpenSceneInput(config, currentSceneIndex, i)
		runInputModal(renderer, config)
		textBrowserFindEditing = false
		activeSceneIndex = -1
		activeElementIndex = -1
		if textBrowserFindSubmitted {
			browserFindApply(config, current)
		}
		textBrowserFindSubmitted = false
		return
	}
}

// browserFindChipLabel is the toolbar chip's text while a search is running:
// the query and where the reader is in it. The count is the whole point —
// "3/12" is what tells a reader whether to keep stepping or give up. Nothing
// is shown when no search is running, so the toolbar stays as it was.
func browserFindChipLabel() string {
	if !browserFind.active || strings.TrimSpace(browserFind.query) == "" {
		return ""
	}
	if len(browserFind.rows) == 0 {
		return "Find: " + truncate(browserFind.query, 18)
	}
	return truncate(browserFind.query, 18) + "  " + itoa(browserFind.index+1) + "/" + itoa(len(browserFind.rows))
}

// browserDrawPageFind paints the match highlights over the page: a band on
// every matching row, brighter on the one the reader is currently on. It runs
// after the page and after the link focus, so the current match is never
// covered by a link outline. Rows scrolled out of the viewport are skipped.
func browserDrawPageFind(renderer *sdl.Renderer, elem Element, layout *webLayout, scroll int, contentTop, contentH int32) {
	if renderer == nil || !browserFind.active || layout == nil || layout.RowH <= 0 || contentH <= 0 {
		return
	}
	rows := browserFindMatches(elem, layout)
	if len(rows) == 0 {
		return
	}
	left := elem.X + 7
	right := left + getElementWidth(elem, 1100) - 14
	width := right - left
	current := rows[browserFind.index]
	for _, row := range rows {
		rel := row - scroll
		if rel < 0 || int32(rel)*layout.RowH >= contentH {
			continue
		}
		y := contentTop + int32(rel)*layout.RowH - 2
		if row == current {
			// The current match gets a filled band and a rail on the left, so
			// it is findable at a glance on a page with many hits.
			fillRoundedRect(renderer, left, y, width, layout.RowH, 4, WithAlpha(ColorWarning, 60))
			fillRoundedRect(renderer, left, y, 4, layout.RowH, 2, ColorWarning)
			continue
		}
		fillRoundedRect(renderer, left, y, width, layout.RowH, 4, WithAlpha(ColorWarning, 28))
	}
}

// browserFindHintLine is the footer hint shown while a search is running, so
// the keys that step it are discoverable without the docs. An empty result
// means "no search", and the caller then draws nothing extra.
func browserFindHintLine() string {
	if !browserFind.active {
		return ""
	}
	return hintLine(FooterHint{"N", "Next"}, FooterHint{"P", "Prev"}, FooterHint{"Esc", "End find"})
}
