package main

// trophy_render_test.go — the celebration and the trophy room on pixels: the
// card lands in the top-right corner under the bar with the cup in its metal,
// the confetti flies only when motion is allowed, nothing starts over a game,
// and the room keeps every pixel inside the compact canvas.

import (
	"os"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// withTrophyClock pins the animation clock and returns its setter.
func withTrophyClock(t *testing.T) func(uint64) {
	t.Helper()
	prev := uiClockMS
	now := uint64(0)
	uiClockMS = func() uint64 { return now }
	t.Cleanup(func() { uiClockMS = prev })
	return func(ms uint64) { now = ms }
}

// renderTrophyOverlayAt draws the overlay on one renderer at each clock time in
// turn, over a flat background, and returns the last frame.
func renderTrophyOverlayAt(t *testing.T, config *Config, setClock func(uint64), times ...uint64) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)
	for _, ms := range times {
		setClock(ms)
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		trophyRenderOverlay(renderer, config)
	}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	return f
}

// goldPixelsIn counts warm yellow metal pixels.
func goldPixelsIn(f *probeFrame, r sdl.Rect) int {
	n := 0
	for y := r.Y; y < r.Y+r.H && y < probeH; y++ {
		for x := r.X; x < r.X+r.W && x < probeW; x++ {
			if x < 0 || y < 0 {
				continue
			}
			c := f.at(x, y)
			if c.R > 170 && c.G > 120 && c.R >= c.G && c.G > c.B && int(c.R)-int(c.B) > 80 {
				n++
			}
		}
	}
	return n
}

// celebrationConfig is a config with the given celebration style.
func celebrationConfig(style string, reduced bool) *Config {
	c := &Config{Variables: Variables{Custom: map[string]interface{}{trophyCelebrationVar: style}}}
	c.Variables.ReducedMotion = reduced
	return c
}

func TestTrophyCardDrawsAtTheTopRight(t *testing.T) {
	withScreen(t, probeW, probeH)
	resetTrophyQueue(t)
	withScreenChild(t, false)
	setClock := withTrophyClock(t)
	if !trophyOverlayReady() {
		t.Skip("another test left the alarm or the screensaver up")
	}

	trophyCelebrateTest(trophyGold)
	frame := renderTrophyOverlayAt(t, celebrationConfig("full", false), setClock, 10000, 10300, 11500)
	card := trophyShow.card
	if !trophyShow.active || card.W <= 0 {
		t.Fatal("no card on screen 1.5 s into a celebration")
	}
	if card.X < probeW/2 || card.X+card.W < probeW-80 {
		t.Fatalf("card %+v is not in the top-right corner", card)
	}
	if card.Y < headerBarHeight() || card.Y > headerBarHeight()+40 {
		t.Fatalf("card %+v is not just under the top bar", card)
	}
	if got := paintedPixelsIn(frame, card); got < 800 {
		t.Fatalf("the card painted only %d bright pixels (text, cup, rim)", got)
	}
	medal := sdl.Rect{X: card.X + 8, Y: card.Y + 4, W: 84, H: card.H - 8}
	if got := goldPixelsIn(frame, medal); got < 60 {
		t.Fatalf("the gold trophy painted only %d gold pixels", got)
	}
	if trophyConfettiAlive() == 0 {
		t.Fatal("no confetti in the air 1.5 s into a full celebration")
	}

	// The card leaves on its own and the overlay goes idle.
	renderTrophyOverlayAt(t, celebrationConfig("full", false), setClock, 10000+trophyCardMS+50, 10000+trophyCardMS+6000)
	if trophyShow.active {
		t.Fatal("the card outstayed its time")
	}
}

func TestTrophyCardIsCalmUnderReducedMotion(t *testing.T) {
	withScreen(t, probeW, probeH)
	resetTrophyQueue(t)
	withScreenChild(t, false)
	setClock := withTrophyClock(t)
	if !trophyOverlayReady() {
		t.Skip("another test left the alarm or the screensaver up")
	}

	config := celebrationConfig("auto", true)
	trophyCelebrateTest(trophyGold)
	renderTrophyOverlayAt(t, config, setClock, 20000, 20100, 20600)
	if !trophyShow.active || trophyShow.full {
		t.Fatal("reduced motion did not give the calm card")
	}
	if trophyConfettiAlive() != 0 {
		t.Fatal("confetti flew although reduced motion is on")
	}
	rest := trophyCardLayoutFor(config, trophyShow.toast).card
	if trophyShow.card.X != rest.X || trophyShow.card.Y != rest.Y {
		t.Fatalf("the calm card moved: %+v, its place is %+v", trophyShow.card, rest)
	}
}

func TestTrophyOverlayHoldsWhileAGameOwnsTheScreen(t *testing.T) {
	withScreen(t, probeW, probeH)
	resetTrophyQueue(t)
	setClock := withTrophyClock(t)
	withScreenChild(t, true)

	trophyCelebrateTest(trophySilver)
	renderTrophyOverlayAt(t, celebrationConfig("full", false), setClock, 30000)
	if trophyShow.active {
		t.Fatal("a card started while a game owned the screen")
	}
	if trophyQueued() != 1 {
		t.Fatalf("the queue holds %d cards, want the waiting one", trophyQueued())
	}

	noteGameChildExited(nil)
	if !trophyOverlayReady() {
		t.Skip("another test left the alarm or the screensaver up")
	}
	renderTrophyOverlayAt(t, celebrationConfig("full", false), setClock, 31000)
	if !trophyShow.active || trophyShow.toast.tier != trophySilver {
		t.Fatal("the waiting trophy did not celebrate once JukaHub was back on screen")
	}
}

// TestTrophyGlyphShowsEachMetal: each tier's cup is drawn in its own metal.
func TestTrophyGlyphShowsEachMetal(t *testing.T) {
	withScreen(t, probeW, probeH)
	f := renderFunProbe(t, func(r *sdl.Renderer) {
		for tier := trophyBronze; tier <= trophyPlatinum; tier++ {
			trophyDrawTrophy(r, sdl.Rect{X: 100 + int32(tier)*200, Y: 200, W: 120, H: 120}, tier, true, false)
		}
		trophyDrawTrophy(r, sdl.Rect{X: 900, Y: 200, W: 120, H: 120}, trophyGold, false, false)
	})
	near := func(c, want sdl.Color) bool {
		d := func(a, b uint8) int {
			if a > b {
				return int(a - b)
			}
			return int(b - a)
		}
		return d(c.R, want.R)+d(c.G, want.G)+d(c.B, want.B) < 90
	}
	for tier := trophyBronze; tier <= trophyPlatinum; tier++ {
		base := trophyMetalOf(tier).base
		hits := 0
		for y := int32(220); y < 300; y++ {
			for x := 100 + int32(tier)*200 + 20; x < 100+int32(tier)*200+100; x++ {
				if near(f.at(x, y), base) {
					hits++
				}
			}
		}
		if hits < 80 {
			t.Errorf("the %s cup has only %d pixels of its metal", trophyTierName(tier), hits)
		}
	}
	// A locked trophy has no metal at all.
	if got := goldPixelsIn(f, sdl.Rect{X: 900, Y: 200, W: 120, H: 120}); got > 0 {
		t.Fatalf("a locked gold trophy painted %d gold pixels", got)
	}
}

// TestConfettiDrawsWithoutAllocating: the paper reaches the screen in the tier's
// colours, and a frame of it allocates (next to) nothing.
func TestConfettiDrawsWithoutAllocating(t *testing.T) {
	withScreen(t, probeW, probeH)
	trophyConfettiClear()
	t.Cleanup(trophyConfettiClear)
	trophyConfetti.seed = 99
	trophyConfettiBurst(640, 300, trophyConfettiMax, trophyGold)
	for i := 0; i < 6; i++ {
		trophyConfettiUpdate(1.0 / 30)
	}
	var allocs float64
	f := renderFunProbe(t, func(r *sdl.Renderer) {
		trophyConfettiDraw(r)
		allocs = testing.AllocsPerRun(10, func() { trophyConfettiDraw(r) })
	})
	painted := 0
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			if !f.isBG(x, y) {
				painted++
			}
		}
	}
	if painted < 300 {
		t.Fatalf("150 pieces of confetti painted only %d pixels", painted)
	}
	if allocs > 2 {
		t.Fatalf("a confetti frame allocated %.1f times; the pool must not allocate per piece", allocs)
	}
}

// renderRoomAt draws the trophy room on a w×h canvas inside the probe window.
func renderRoomAt(t *testing.T, w, h int32, data playtimeData, tab int) *probeFrame {
	t.Helper()
	if _, err := os.Stat("jukaconfig.json"); err != nil {
		t.Fatalf("jukaconfig.json ships next to the tests: %v", err)
	}
	config, _, err := LoadXMLConfigWithFallback("jukaconfig.json")
	cancelPendingUserConfigSave(t)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	withScreen(t, w, h)
	ClearTextCache()
	t.Cleanup(ClearTextCache)
	playtime.data = data
	playtime.loaded = true
	playtime.rev++
	trophyRoomSyncVisit()
	trophyRoom.tab = tab

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderPlaytime(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	return f
}

// TestTrophyRoomFitsTheCompactCanvas: on the Brick's 960-wide canvas both tabs
// stay inside it — no text or accent is drawn past its right edge (the dim
// backdrop blooms may spill into the wider probe window; they are not the
// room's) — and the grid has two columns of cards inside the page margins.
func TestTrophyRoomFitsTheCompactCanvas(t *testing.T) {
	data := newPlaytimeData()
	data.Total = 5 * 3600
	data.Scenes["Games"] = 3 * 3600
	data.Scenes["MusicPlayer"] = 2 * 3600
	data.Days[nowMinusDays(0)] = 3600
	data.Unlocked["first_steps"] = "2026-03-02T20:11:00Z"
	data.Games["Super Metroid"] = 5400
	withTrophyRoom(t, data)

	const w, h = int32(960), int32(720)
	for _, tab := range []int{trophyTabTrophies, trophyTabStats} {
		f := renderRoomAt(t, w, h, data, tab)
		for y := int32(0); y < h; y++ {
			for x := w + 2; x < probeW; x++ {
				if c := f.at(x, y); int(c.R)+int(c.G)+int(c.B) > 600 {
					t.Fatalf("tab %d drew %+v at (%d,%d), past the 960px canvas", tab, c, x, y)
				}
			}
		}
		for _, r := range []sdl.Rect{trophyRoom.tabRects[0], trophyRoom.tabRects[1], trophyRoom.raRect, trophyRoom.heatCard, trophyRoom.barsRect} {
			if r.W > 0 && (r.X < 0 || r.X+r.W > w) {
				t.Fatalf("tab %d laid out %+v past the canvas", tab, r)
			}
		}
		if tab == trophyTabTrophies {
			if trophyRoom.cols != 2 {
				t.Fatalf("the compact grid has %d columns, want 2", trophyRoom.cols)
			}
			for k := 0; k < trophyRoom.cardN; k++ {
				c := trophyRoom.cardRects[k]
				if c.X < 0 || c.X+c.W > w {
					t.Fatalf("card %+v leaves the canvas", c)
				}
			}
			for _, c := range trophyRoom.chipRects {
				if c.W > 0 && (c.X < 0 || c.X+c.W > w) {
					t.Fatalf("chip %+v leaves the canvas", c)
				}
			}
		}
	}
}

// TestTrophyRoomSheetAndFooterHints: the details sheet draws inside the screen
// and the footer describes it while it is up.
func TestTrophyRoomSheetAndFooterHints(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())
	// The sheet fades in; the frame is taken once the entrance is over.
	setClock := withTrophyClock(t)
	setClock(1000)
	hintFor := func(button string) string {
		for _, h := range footerHints(&Config{}, SceneConfig{Name: playtimeSceneName}) {
			if h.Button == button {
				return h.Label
			}
		}
		return ""
	}
	if hintFor("B") != "Back" || hintFor("L1/R1") == "" {
		t.Fatal("the room's footer must offer Back and the tab switch")
	}

	roomKey(sdl.K_DOWN)
	roomKey(sdl.K_RETURN)
	if hintFor("B") != "Close" {
		t.Fatalf("with the sheet up B says %q, want Close", hintFor("B"))
	}
	setClock(5000)
	f := renderRoomAt(t, probeW, probeH, playtime.data, trophyTabTrophies)
	sheet := trophyRoom.sheetRect
	if !trophyRoom.sheet || sheet.W <= 0 {
		t.Fatal("the sheet was not drawn")
	}
	if sheet.X < 0 || sheet.Y < headerBarHeight() || sheet.X+sheet.W > probeW || sheet.Y+sheet.H > sceneFooterBand().Y {
		t.Fatalf("the sheet %+v leaves the page between the bars", sheet)
	}
	if got := paintedPixelsIn(f, sheet); got < 1000 {
		t.Fatalf("the sheet painted only %d bright pixels", got)
	}
	// A click outside the sheet closes it.
	if !handlePlaytimeMouse(sheet.X/2, sheet.Y+sheet.H/2) || trophyRoom.sheet {
		t.Fatal("a click outside the sheet did not close it")
	}
}
