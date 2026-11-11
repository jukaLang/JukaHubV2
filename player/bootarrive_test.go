package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// bootArriveTestIn reports whether pixel (x, y) is inside r.
func bootArriveTestIn(r sdl.Rect, x, y int32) bool {
	return r.W > 0 && r.H > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Every pixel outside the holes is covered by exactly one rest rect and no
// pixel inside one is: overlapping, touching, clipped and empty holes alike.
func TestBootArriveRestCoversTheScreenOnce(t *testing.T) {
	screen := sdl.Rect{X: 0, Y: 0, W: 40, H: 30}
	holes := []sdl.Rect{
		{X: 2, Y: 3, W: 10, H: 8},
		{X: 8, Y: 6, W: 10, H: 10},
		{X: 18, Y: 6, W: 5, H: 5},
		{X: 35, Y: 25, W: 20, H: 20},
		{X: 50, Y: 50, W: 5, H: 5},
		{X: 20, Y: 20, W: 0, H: 4},
	}
	rest := bootArriveRest(screen, holes)
	for y := int32(0); y < screen.H; y++ {
		for x := int32(0); x < screen.W; x++ {
			inHole := false
			for _, h := range holes {
				if bootArriveTestIn(h, x, y) {
					inHole = true
				}
			}
			n := 0
			for _, r := range rest {
				if bootArriveTestIn(r, x, y) {
					n++
				}
			}
			if (inHole && n != 0) || (!inHole && n != 1) {
				t.Fatalf("pixel (%d,%d): in a hole %v, covered %d times by %+v", x, y, inHole, n, rest)
			}
		}
	}
	if got := bootArriveRest(screen, nil); len(got) != 1 || got[0] != screen {
		t.Fatalf("no holes: %+v, want the whole screen as one rect", got)
	}
	if got := bootArriveRest(sdl.Rect{}, holes); got != nil {
		t.Fatalf("an empty screen has no rest, got %+v", got)
	}
}

func TestBootArriveCardRectsSkips(t *testing.T) {
	items := map[HomeFocusID]HomeItem{
		HomeFocusMedia:    {Rect: sdl.Rect{X: 100, Y: 200, W: 50, H: 40}},
		HomeFocusFiles:    {Rect: sdl.Rect{X: 10, Y: 200, W: 50, H: 40}},
		HomeFocusClock:    {Rect: sdl.Rect{X: 300, Y: 300, W: 50, H: 40}},
		HomeFocusApps:     {Rect: sdl.Rect{X: 10, Y: 20, W: 50, H: 40}},
		HomeFocusChat:     {Rect: sdl.Rect{X: 10, Y: 300}},
		HomeFocusTools:    {Rect: sdl.Rect{X: 2000, Y: 300, W: 50, H: 40}},
		HomeFocusExit:     {Rect: sdl.Rect{X: 10, Y: 600, W: 50, H: 30}},
		HomeFocusSettings: {Rect: sdl.Rect{X: 630, Y: 650, W: 50, H: 40}},
	}
	order := []HomeFocusID{
		HomeFocusSettings, HomeFocusMedia, HomeFocusFiles, HomeFocusClock, HomeFocusApps,
		HomeFocusChat, HomeFocusTools, HomeFocusExit, HomeFocusMedia, HomeFocusEmulator,
	}
	// The power button is switched off; everything else is on.
	hidden := func(id HomeFocusID) bool { return id != HomeFocusExit }
	got := bootArriveCardRects(items, order, hidden, "", 56, 640, 680, 4, 8)
	want := []sdl.Rect{
		{X: 6, Y: 196, W: 58, H: 48},
		{X: 96, Y: 196, W: 58, H: 48},
		{X: 626, Y: 646, W: 14, H: 34},
	}
	if len(got) != len(want) {
		t.Fatalf("cards %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("card %d: %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	shown := func(HomeFocusID) bool { return true }
	if got := bootArriveCardRects(items, order, shown, "", 56, 640, 680, 4, 8); len(got) != 4 {
		t.Fatalf("a shown power button is a card: %+v", got)
	}
	// Without an enabled function every card counts but the power button.
	if got := bootArriveCardRects(items, order, nil, "", 56, 640, 680, 4, 8); len(got) != 3 {
		t.Fatalf("no enabled function: %+v", got)
	}
	// Any control the layout switched off is left to the rest of the screen.
	noFiles := func(id HomeFocusID) bool { return id != HomeFocusFiles }
	if got := bootArriveCardRects(items, order, noFiles, "", 56, 640, 680, 4, 8); len(got) != 3 || got[0].X != 96 {
		t.Fatalf("a switched-off card was cut: %+v", got)
	}
}

// The focused card takes its ring with it: it grows by the focus margin as far
// as its neighbours allow, a neighbour's margin gives way to it, and neither
// cut reaches into the other card.
func TestBootArriveCardRectsFocus(t *testing.T) {
	// Two cards 7 px apart side by side, one 14 px below the first.
	items := map[HomeFocusID]HomeItem{
		HomeFocusMedia:    {Rect: sdl.Rect{X: 100, Y: 200, W: 100, H: 60}},
		HomeFocusFiles:    {Rect: sdl.Rect{X: 207, Y: 200, W: 100, H: 60}},
		HomeFocusSettings: {Rect: sdl.Rect{X: 100, Y: 274, W: 100, H: 60}},
	}
	order := []HomeFocusID{HomeFocusMedia, HomeFocusFiles, HomeFocusSettings}
	cards := bootArriveCardRects(items, order, nil, HomeFocusMedia, 56, 1280, 720, 6, 10)
	if len(cards) != 3 {
		t.Fatalf("cards %+v", cards)
	}
	var media, files, settings sdl.Rect
	for _, c := range cards {
		switch {
		case c.X < 150 && c.Y < 270:
			media = c
		case c.X > 150:
			files = c
		default:
			settings = c
		}
	}
	// Media grows 10 up, left and down (Settings is 14 px away), but only the
	// 7 px to Files on the right.
	if want := (sdl.Rect{X: 90, Y: 190, W: 117, H: 80}); media != want {
		t.Fatalf("focused card cut %+v, want %+v", media, want)
	}
	// Files keeps its margin on every side but the one facing Media, which now
	// ends where Media's cut does — at its own edge.
	if want := (sdl.Rect{X: 207, Y: 194, W: 106, H: 72}); files != want {
		t.Fatalf("neighbour cut %+v, want %+v", files, want)
	}
	// Settings, below, gives way at the top: Media's cut ends at y 270, two
	// pixels into what would have been Settings' margin.
	if want := (sdl.Rect{X: 94, Y: 270, W: 112, H: 70}); settings != want {
		t.Fatalf("card below %+v, want %+v", settings, want)
	}
	for i := range cards {
		for j := i + 1; j < len(cards); j++ {
			if homeRectsOverlap(cards[i], cards[j]) {
				t.Fatalf("cuts overlap: %+v and %+v", cards[i], cards[j])
			}
		}
		for _, id := range order {
			r := items[id].Rect
			inside := r.X >= cards[i].X && r.Y >= cards[i].Y && r.X+r.W <= cards[i].X+cards[i].W && r.Y+r.H <= cards[i].Y+cards[i].H
			if homeRectsOverlap(cards[i], r) && !inside {
				t.Fatalf("cut %+v reaches into card %+v", cards[i], r)
			}
		}
	}
}

// Home's own layout, with the Continue card focused: the search pill arrives
// first, the power button last, no header chip is cut out, the focused card's
// cut carries its ring (eight pixels round it), the cuts never overlap, and
// the rest of the screen plus the cards is the screen exactly.
func TestBootArriveCardRectsHome(t *testing.T) {
	h := NewHomeLayout(1280, 720)
	all := func(HomeFocusID) bool { return true }
	cards := bootArriveCardRects(h.Items, h.Order, all, HomeFocusContinue, HomeTopBarH, 1280, 720, 6, 10)
	if len(cards) < 12 {
		t.Fatalf("only %d cards: %+v", len(cards), cards)
	}
	grow := func(r sdl.Rect, m int32) sdl.Rect { return sdl.Rect{X: r.X - m, Y: r.Y - m, W: r.W + 2*m, H: r.H + 2*m} }
	contains := func(outer, inner sdl.Rect) bool {
		return inner.X >= outer.X && inner.Y >= outer.Y && inner.X+inner.W <= outer.X+outer.W && inner.Y+inner.H <= outer.Y+outer.H
	}
	if search := h.Items[HomeFocusSearch].Rect; !contains(cards[0], search) || cards[0].Y != search.Y-6 {
		t.Fatalf("first card %+v, want the search pill %+v", cards[0], search)
	}
	if want := grow(h.Items[HomeFocusExit].Rect, 6); cards[len(cards)-1] != want {
		t.Fatalf("last card %+v, want the power button %+v", cards[len(cards)-1], want)
	}
	ring := grow(h.Items[HomeFocusContinue].Rect, 8)
	carried := false
	for _, c := range cards {
		if contains(c, ring) {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("no cut carries the focused card's ring %+v: %+v", ring, cards)
	}
	for _, id := range []HomeFocusID{HomeFocusClock, HomeFocusBell, HomeFocusQR, HomeFocusWiFi} {
		chip := h.Items[id].Rect
		for _, c := range cards {
			if contains(c, chip) {
				t.Fatalf("header chip %s was cut out as a card", id)
			}
		}
	}
	for i := 1; i < len(cards); i++ {
		a, b := cards[i-1], cards[i]
		if b.Y < a.Y || (b.Y == a.Y && b.X < a.X) {
			t.Fatalf("cards %d and %d out of reading order: %+v then %+v", i-1, i, a, b)
		}
	}
	for i := range cards {
		for j := i + 1; j < len(cards); j++ {
			if homeRectsOverlap(cards[i], cards[j]) {
				t.Fatalf("grown cards overlap: %+v and %+v", cards[i], cards[j])
			}
		}
	}
	rest := bootArriveRest(sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}, cards)
	var area int64
	for i, r := range rest {
		area += int64(r.W) * int64(r.H)
		for _, c := range cards {
			if homeRectsOverlap(r, c) {
				t.Fatalf("rest %+v covers card %+v", r, c)
			}
		}
		for j := i + 1; j < len(rest); j++ {
			if homeRectsOverlap(r, rest[j]) {
				t.Fatalf("rest rects overlap: %+v and %+v", r, rest[j])
			}
		}
	}
	for _, c := range cards {
		area += int64(c.W) * int64(c.H)
	}
	if area != 1280*720 {
		t.Fatalf("rest and cards cover %d px, want %d", area, 1280*720)
	}
}

func TestBootArriveBeats(t *testing.T) {
	if got := bootArrivePower.duration(13, false, true); got != 220+34*12+420 {
		t.Fatalf("power-on arrival of 13 cards lasts %.0f ms", got)
	}
	if got := bootArriveQuick.duration(13, false, true); got != 40+10*12+220 {
		t.Fatalf("quick arrival of 13 cards lasts %.0f ms, want 380", got)
	}
	if got := bootArrivePower.duration(0, true, false); got != 560 {
		t.Fatalf("power-on whole-screen arrival lasts %.0f ms, want 560", got)
	}
	if got := bootArriveQuick.duration(0, false, true); got != 330 {
		t.Fatalf("quick arrival with nothing to cut lasts %.0f ms, want 330", got)
	}
	if got := bootArriveQuick.duration(0, false, false); got != 300 {
		t.Fatalf("quick arrival without the flight lasts %.0f ms, want 300", got)
	}
	// A press during a launch's hello: the first screen, at once.
	if !bootArriveSkip.wholeOnly || bootArrivePower.wholeOnly || bootArriveQuick.wholeOnly {
		t.Fatal("only the skip is a whole-picture arrival")
	}
	if got := bootArriveSkip.duration(0, true, false); got != 120 {
		t.Fatalf("a skipped launch arrives in %.0f ms, want 120", got)
	}
	if alpha, dy := bootArriveSkip.whole(60, true); alpha <= 0 || alpha >= 1 || dy != 0 {
		t.Fatalf("halfway through a skip: alpha %.3f dy %.3f, want a fade that does not move", alpha, dy)
	}
	for _, b := range []*bootArriveBeats{&bootArrivePower, &bootArriveQuick} {
		end := b.duration(13, false, true)
		for i := 0; i < 13; i++ {
			prev := -1.0
			for ms := 0.0; ms <= end; ms += 5 {
				alpha, dy := b.card(i, ms, true)
				if alpha < prev || dy < 0 || dy > b.cardDY {
					t.Fatalf("card %d at %.0f ms: alpha %.3f (after %.3f), dy %.2f", i, ms, alpha, prev, dy)
				}
				prev = alpha
				if _, still := b.card(i, ms, false); still != 0 {
					t.Fatalf("card %d moves under reduced motion (%.2f)", i, still)
				}
			}
			if alpha, dy := b.card(i, end, true); alpha != 1 || dy != 0 {
				t.Fatalf("card %d not in place at the end: alpha %.3f dy %.3f", i, alpha, dy)
			}
		}
		if alpha, dy := b.whole(b.duration(0, true, false), true); alpha != 1 || dy != 0 {
			t.Fatalf("the whole screen is not in place at the end: alpha %.3f dy %.3f", alpha, dy)
		}
		if _, dy := b.whole(b.wholeFrom, false); dy != 0 {
			t.Fatalf("the whole screen moves under reduced motion (%.2f)", dy)
		}
	}
}
