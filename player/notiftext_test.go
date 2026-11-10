package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// notifTestMeasure is a fixed advance per rune: 12 px is the bundled Inter's
// average at the 24 px "small" role the toast and the centre use.
func notifTestMeasure(perRune int32) func(string) int32 {
	return func(s string) int32 {
		return perRune * int32(utf8.RuneCountInString(s))
	}
}

// The message a Smart Pro user saw cut: the toast split it by pixel count into
// two 31-character lines ("...shares every fil" / "e on the device, system
// files i…"). At the 1280 canvas's toast width it now fits whole, at word
// boundaries.
func TestToastLongMessageIsNotCut(t *testing.T) {
	msg := "Advanced: this shares every file on the device, system files included — press again to confirm"
	measure := notifTestMeasure(12)
	maxTextW := toastTextWidth(toastMaxWidth(1280))
	lines, cut := wrapWordsMeasured(msg, maxTextW, toastMaxLines, measure)
	if cut {
		t.Fatalf("a %d-character message was cut: %q", utf8.RuneCountInString(msg), lines)
	}
	if got := strings.Join(lines, " "); got != msg {
		t.Errorf("the lines do not add up to the message:\n got %q\nwant %q", got, msg)
	}
	words := map[string]bool{}
	for _, w := range strings.Fields(msg) {
		words[w] = true
	}
	for _, l := range lines {
		if measure(l) > maxTextW {
			t.Errorf("line %q is %d px, wider than the %d px text column", l, measure(l), maxTextW)
		}
		for _, w := range strings.Fields(l) {
			if !words[w] {
				t.Errorf("line %q splits a word (%q is not one of the message's)", l, w)
			}
		}
	}
	if len(lines) < 2 || len(lines) > 3 {
		t.Errorf("got %d lines, want 2-3 at the wide toast: %q", len(lines), lines)
	}
}

// The Brick's compact canvas (960 wide, type scaled to ~18 px) holds about the
// same text per line as the Smart Pro.
func TestToastWidthFollowsTheCanvas(t *testing.T) {
	cases := []struct {
		screenW, want int32
	}{
		{1280, 576}, // Smart Pro, Windows
		{960, 432},  // Brick compact canvas
		{640, 420},  // never narrower than the old fixed card
		{3840, 600}, // capped
		{300, 260},  // inside the margins on a tiny panel
	}
	for _, c := range cases {
		if got := toastMaxWidth(c.screenW); got != c.want {
			t.Errorf("toastMaxWidth(%d) = %d, want %d", c.screenW, got, c.want)
		}
	}
	if got := toastTextWidth(576); got != 576-2*toastPad-toastStripW {
		t.Errorf("toastTextWidth(576) = %d", got)
	}
}

// The card hugs short messages as before, grows with its lines, and has no
// height ceiling: the old 96 px cap clipped the second line at the largest
// text size (43 px lines).
func TestToastCardSize(t *testing.T) {
	if w, h := toastCardSize(40, 1, 30, 576); w != toastMinW || h != 62 {
		t.Errorf("short message: %dx%d, want %dx62", w, h, toastMinW)
	}
	if w, h := toastCardSize(534, 4, 30, 576); w != 576 || h != 4*30+2*toastPad {
		t.Errorf("four full lines: %dx%d, want 576x%d", w, h, 4*30+2*toastPad)
	}
	if _, h := toastCardSize(300, 2, 43, 576); h != 2*43+2*toastPad {
		t.Errorf("two lines at the largest text size: height %d, want %d (no ceiling)", h, 2*43+2*toastPad)
	}
	if w, _ := toastCardSize(900, 1, 30, 576); w != 576 {
		t.Errorf("an over-wide line: width %d, want the 576 cap", w)
	}
}

func TestToastDurationGrowsWithTheMessage(t *testing.T) {
	long := strings.Repeat("word ", 60)
	cases := []struct {
		name string
		kind ToastKind
		msg  string
		want uint64
	}{
		{"short info keeps its slot", ToastKindInfo, "Saved", 2500},
		{"short error keeps its slot", ToastKindError, "Couldn't connect", 5000},
		{"a long confirmation gets reading time", ToastKindInfo,
			"Advanced: this shares every file on the device, system files included — press again to confirm",
			toastReadBaseMS + toastReadPerRuneMS*94},
		{"capped", ToastKindWarning, long, toastReadMaxMS},
		{"scene-switch labels stay short", ToastKindSwitch, long, 1500},
	}
	for _, c := range cases {
		if got := toastDurationForMessage(c.kind, c.msg); got != c.want {
			t.Errorf("%s: %d ms, want %d", c.name, got, c.want)
		}
	}
}

func TestWrapWordsMeasured(t *testing.T) {
	m := notifTestMeasure(10)
	t.Run("fits", func(t *testing.T) {
		lines, cut := wrapWordsMeasured("  Saved  ", 100, 4, m)
		if cut || len(lines) != 1 || lines[0] != "Saved" {
			t.Errorf("got %q cut=%v", lines, cut)
		}
	})
	t.Run("blank", func(t *testing.T) {
		if lines, cut := wrapWordsMeasured(" \n ", 100, 4, m); lines != nil || cut {
			t.Errorf("got %q cut=%v, want nothing", lines, cut)
		}
	})
	t.Run("newlines break lines", func(t *testing.T) {
		lines, _ := wrapWordsMeasured("Line one\r\nLine two\n\nthree", 200, 0, m)
		want := []string{"Line one", "Line two", "three"}
		if strings.Join(lines, "|") != strings.Join(want, "|") {
			t.Errorf("got %q, want %q", lines, want)
		}
	})
	t.Run("exact fit is not cut", func(t *testing.T) {
		lines, cut := wrapWordsMeasured("aaaa bbbb cccc dddd", 90, 2, m)
		if cut || len(lines) != 2 {
			t.Errorf("got %q cut=%v, want two lines, not cut", lines, cut)
		}
	})
	t.Run("cut ends in an ellipsis inside the width", func(t *testing.T) {
		text := strings.Repeat("lorem ipsum dolor sit amet ", 15)
		lines, cut := wrapWordsMeasured(text, 200, 4, m)
		if !cut || len(lines) != 4 {
			t.Fatalf("got %d lines cut=%v, want 4 lines, cut", len(lines), cut)
		}
		last := lines[3]
		if !strings.HasSuffix(last, notifEllipsis) || strings.HasSuffix(last, " "+notifEllipsis) {
			t.Errorf("last line %q should end in a bare ellipsis", last)
		}
		for _, l := range lines {
			if m(l) > 200 {
				t.Errorf("line %q is wider than the box", l)
			}
		}
	})
	t.Run("a path shares the open line and breaks after a slash", func(t *testing.T) {
		lines, cut := wrapWordsMeasured("Saved to /storage/roms/psp/game.iso", 200, 0, m)
		want := []string{"Saved to /storage/", "roms/psp/game.iso"}
		if cut || strings.Join(lines, "|") != strings.Join(want, "|") {
			t.Errorf("got %q, want %q", lines, want)
		}
	})
	t.Run("a long URL is cut into pieces that fit", func(t *testing.T) {
		url := "https://github.com/jukaLang/JukaHubV2/releases/download/v2.1/JukaHub-linux-arm64.tar.gz"
		lines, cut := wrapWordsMeasured("Download failed: "+url+" timed out", 240, 0, m)
		if cut {
			t.Fatalf("cut with no line limit: %q", lines)
		}
		for _, l := range lines {
			if m(l) > 240 {
				t.Errorf("line %q is wider than the box", l)
			}
		}
		joined := strings.Join(lines, "")
		if !strings.Contains(joined, url) {
			t.Errorf("the URL did not survive the cut: %q", lines)
		}
		slashEnds := 0
		for _, l := range lines {
			if strings.HasSuffix(l, "/") {
				slashEnds++
			}
		}
		if slashEnds == 0 {
			t.Errorf("no piece ends at a path separator: %q", lines)
		}
		if got := lines[len(lines)-1]; !strings.HasSuffix(got, "timed out") {
			t.Errorf("the words after the URL should follow its last piece, got last line %q", got)
		}
	})
	t.Run("text without spaces", func(t *testing.T) {
		cjk := "日本語のテキストはスペースがありません"
		lines, _ := wrapWordsMeasured(cjk, 50, 0, m)
		if strings.Join(lines, "") != cjk {
			t.Errorf("pieces %q do not add up to the text", lines)
		}
		for _, l := range lines {
			if m(l) > 50 {
				t.Errorf("piece %q is wider than the box", l)
			}
		}
	})
	t.Run("a glyph wider than the box goes out alone", func(t *testing.T) {
		lines, _ := wrapWordsMeasured("abc", 5, 0, m)
		if strings.Join(lines, "|") != "a|b|c" {
			t.Errorf("got %q", lines)
		}
	})
	t.Run("the line budget stops a long token too", func(t *testing.T) {
		lines, cut := wrapWordsMeasured(strings.Repeat("x", 500), 100, 3, m)
		if !cut || len(lines) != 3 || !strings.HasSuffix(lines[2], notifEllipsis) {
			t.Errorf("got %q cut=%v", lines, cut)
		}
	})
	t.Run("no measure", func(t *testing.T) {
		lines, cut := wrapWordsMeasured(" hello ", 100, 2, nil)
		if cut || len(lines) != 1 || lines[0] != "hello" {
			t.Errorf("got %q cut=%v", lines, cut)
		}
	})
}

func TestFitEllipsisMeasured(t *testing.T) {
	m := notifTestMeasure(10)
	if got := fitEllipsisMeasured("abcdefghij", 50, m); got != "abcd…" {
		t.Errorf("got %q, want %q", got, "abcd…")
	}
	if got := fitEllipsisMeasured("abc defgh", 50, m); got != "abc…" {
		t.Errorf("got %q, want %q (no space before the ellipsis)", got, "abc…")
	}
	if got := fitEllipsisMeasured("short", 200, m); got != "short…" {
		t.Errorf("got %q, want %q", got, "short…")
	}
}

func TestNotifRowGeometry(t *testing.T) {
	if got := notifRowHeight(2, 30); got != notifRowPad*2+2*30+2+30 {
		t.Errorf("two-line row: %d", got)
	}
	if got := notifRowHeight(1, 10); got != notifRowMinH {
		t.Errorf("tiny row: %d, want the %d floor", got, notifRowMinH)
	}
	// The Smart Pro's card under the bell has ~596 px for rows: a highlighted
	// message may use 18 lines of 30 px, some 650 characters.
	if got := notifExpandedLines(596, 30); got != 18 {
		t.Errorf("notifExpandedLines(596, 30) = %d, want 18", got)
	}
	if got := notifExpandedLines(50, 30); got != notifMsgLines {
		t.Errorf("a cramped card still gives %d lines, got %d", notifMsgLines, got)
	}
	if got := notifExpandedLines(596, 0); got != notifMsgLines {
		t.Errorf("no line height: %d", got)
	}
}

func TestNotifListWindow(t *testing.T) {
	rows := func(n int, h int32) []int32 {
		out := make([]int32, n)
		for i := range out {
			out[i] = h
		}
		return out
	}
	cases := []struct {
		name                    string
		heights                 []int32
		avail                   int32
		maxRows, cursor, scroll int
		wantFirst, wantCount    int
	}{
		{"empty", nil, 596, 8, 0, 0, 0, 0},
		{"all fit", rows(3, 78), 596, 8, 0, 0, 0, 3},
		// Twenty rows, seven fit: the cursor paged to row 15 used to sit on a
		// row that was never drawn.
		{"cursor past the first card-full", rows(20, 78), 596, 8, 15, 0, 9, 7},
		{"end of the list", rows(20, 78), 596, 8, 19, 0, 13, 7},
		{"moving inside the window keeps it still", rows(20, 78), 596, 8, 18, 13, 13, 7},
		{"moving above the window scrolls up", rows(20, 78), 596, 8, 12, 13, 12, 7},
		{"an expanded row pushes the rest down", []int32{78, 300, 78, 78, 78, 78}, 400, 8, 1, 0, 0, 2},
		{"a row taller than the card is still shown", []int32{78, 500}, 400, 8, 1, 0, 1, 1},
		{"the row cap", rows(20, 52), 2000, 8, 10, 0, 3, 8},
		{"the end fills upwards", []int32{52, 52, 52, 52, 52, 52, 52, 52, 200, 52}, 400, 8, 9, 9, 6, 4},
		{"out-of-range cursor and scroll", rows(5, 78), 596, 8, 99, -4, 0, 5},
	}
	for _, c := range cases {
		first, count := notifListWindow(c.heights, c.avail, c.maxRows, c.cursor, c.scroll)
		if first != c.wantFirst || count != c.wantCount {
			t.Errorf("%s: window (%d, %d), want (%d, %d)", c.name, first, count, c.wantFirst, c.wantCount)
		}
	}
}

// Whatever the heights, cursor and previous scroll, the cursor's row is on the
// card and the rows fit (a single over-tall row excepted).
func TestNotifListWindowInvariants(t *testing.T) {
	seed := uint32(12345)
	next := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for iter := 0; iter < 2000; iter++ {
		n := 1 + next(50)
		heights := make([]int32, n)
		for i := range heights {
			heights[i] = int32(52 + next(400))
		}
		avail := int32(100 + next(600))
		maxRows := 1 + next(10)
		cursor := next(n + 3)
		scroll := next(n + 3)
		first, count := notifListWindow(heights, avail, maxRows, cursor, scroll)
		c := clampIndex(cursor, n)
		if count < 1 || count > maxRows || first < 0 || first+count > n {
			t.Fatalf("bad window (%d, %d) for n=%d maxRows=%d", first, count, n, maxRows)
		}
		if c < first || c >= first+count {
			t.Fatalf("cursor %d is off the window (%d, %d)", c, first, count)
		}
		if count > 1 && notifSumHeights(heights[first:first+count]) > avail {
			t.Fatalf("window (%d, %d) is %d px tall, more than %d", first, count,
				notifSumHeights(heights[first:first+count]), avail)
		}
	}
}

// notifTestKeepState saves the centre's state and restores it after the test.
func notifTestKeepState(t *testing.T) {
	t.Helper()
	notifHistoryMu.Lock()
	saved, savedAdded := nc, notifAdded
	nc.history = nil
	notifHistoryMu.Unlock()
	t.Cleanup(func() {
		notifHistoryMu.Lock()
		nc, notifAdded = saved, savedAdded
		notifHistoryMu.Unlock()
	})
}

// The D-pad, End and the wheel reach every row now that the list scrolls; the
// cursor used to stop at the last row the first card-full drew.
func TestNotifCursorReachesTheWholeHistory(t *testing.T) {
	notifTestKeepState(t)
	for i := 0; i < 20; i++ {
		notifRecordAs(strings.Repeat("m", i+1), notifInfo, true)
	}
	nc.open = true
	nc.cursor = 0
	nc.visibleRows = 6
	for i := 0; i < 15; i++ {
		notifNavigate(1)
	}
	if nc.cursor != 15 {
		t.Errorf("15 presses down: cursor %d, want 15", nc.cursor)
	}
	for i := 0; i < 40; i++ {
		notifNavigate(1)
	}
	if nc.cursor != 19 {
		t.Errorf("past the end: cursor %d, want 19", nc.cursor)
	}
	if !notifWheel(1) || nc.cursor != 18 {
		t.Errorf("wheel up: cursor %d, want 18", nc.cursor)
	}
	if !notifWheel(-1) || nc.cursor != 19 {
		t.Errorf("wheel down: cursor %d, want 19", nc.cursor)
	}
	nc.open = false
	if notifWheel(1) {
		t.Error("the closed centre took the wheel")
	}
}

// A row arriving while the centre is open does not move the highlight off the
// message being read, the newest one included.
func TestNotifHighlightFollowsItsRow(t *testing.T) {
	notifTestKeepState(t)
	notifRecordAs("first", notifInfo, true)
	notifRecordAs("second", notifWarning, false)
	notifRecordAs("third", notifError, false)
	notifHistoryMu.Lock()
	nc.seenAdded = notifAdded
	notifHistoryMu.Unlock()

	nc.cursor, nc.scroll = 2, 1 // reading "first"
	notifRecordAs("fourth", notifInfo, true)
	notifRecordAs("fourth", notifInfo, true) // folded into the row above: not a new row
	notifRecordAs("fifth", notifInfo, true)
	history, added := notifHistoryState()
	notifFollowArrivals(added, len(history))
	if nc.cursor != 4 || history[nc.cursor].Message != "first" {
		t.Errorf("highlight on row %d, want 4 (still %q)", nc.cursor, "first")
	}
	if nc.scroll != 3 {
		t.Errorf("scroll %d, want 3", nc.scroll)
	}
	if history[1].Repeat != 2 {
		t.Errorf("the repeat was not folded: %+v", history[1])
	}

	// A message opened from its toast sits on the newest row; the next one
	// filed must not collapse it. The list at its top stays at the top.
	nc.cursor, nc.scroll = 0, 0
	notifRecordAs("sixth", notifInfo, true)
	history, added = notifHistoryState()
	notifFollowArrivals(added, len(history))
	if nc.cursor != 1 || history[1].Message != "fifth" || nc.scroll != 0 {
		t.Errorf("after an arrival: highlight %d (want 1, %q), scroll %d (want 0)", nc.cursor, "fifth", nc.scroll)
	}
	if got := notifRowIndex(" second "); got != 4 {
		t.Errorf("notifRowIndex(second) = %d, want 4", got)
	}
	if got := notifRowIndex("missing"); got != -1 {
		t.Errorf("notifRowIndex(missing) = %d, want -1", got)
	}

	// After Clear All a burst has no row to follow: the newest is highlighted.
	notifClearAll()
	notifRecordAs("a", notifInfo, true)
	notifRecordAs("b", notifInfo, true)
	notifRecordAs("  c  ", notifInfo, true) // stored as the toast shows it
	history, added = notifHistoryState()
	notifFollowArrivals(added, len(history))
	if nc.cursor != 0 || nc.scroll != 0 || history[0].Message != "c" {
		t.Errorf("after a burst into an empty centre: highlight %d scroll %d, newest %q", nc.cursor, nc.scroll, history[0].Message)
	}
	if got := notifRowIndex("c"); got != 0 {
		t.Errorf("a padded message was not filed trimmed: notifRowIndex(c) = %d", got)
	}
}
