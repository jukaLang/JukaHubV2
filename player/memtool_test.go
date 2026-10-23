package main

// Offscreen checks for the Memory panel. The scan/narrow/write flow runs
// against a fake process image, so the arithmetic and the confirm-before-write
// rule are verified without ptrace or a real game.

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// memTestSession points the panel at a fake image (the ramAccess from
// ramsearch_test.go) and restores the global state afterwards.
func memTestSession(t *testing.T, data []byte) *fakeMem {
	t.Helper()
	prev := memPanel
	t.Cleanup(func() { memPanel = prev })

	mem := newFakeMem(len(data))
	copy(mem.data, data)
	memPanel = memPanelState{
		open:         true,
		phase:        memPhaseActions,
		pid:          4242,
		pidName:      "test-game",
		access:       mem,
		width:        4,
		search:       nil,
		actionCursor: int(memActionValue),
	}
	return mem
}

func TestRamParseValue(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"100", 100, true},
		{"  42  ", 42, true},
		{"0x1F", 31, true},
		{"0Xff", 255, true},
		{"0", 0, true},
		{"-1", ^uint64(0), true},
		{"-2", ^uint64(1), true},
		{"", 0, false},
		{"   ", 0, false},
		{"abc", 0, false},
		{"0x", 0, false},
		{"-", 0, false},
		{"99999999999999999999999", 0, false},
	}
	for _, c := range cases {
		got, ok := ramParseValue(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("ramParseValue(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestMemScanNarrowWrite(t *testing.T) {
	// Two 4-byte slots hold 5000 and one holds 77.
	data := make([]byte, 64)
	ramEncode(4, 5000, data[8:12])
	ramEncode(4, 5000, data[32:36])
	ramEncode(4, 77, data[16:20])
	mem := memTestSession(t, data)

	memPanel.valueText = "5000"
	memStartScan()
	if memPanel.search == nil {
		t.Fatal("scan produced no search")
	}
	if got := len(memPanel.search.Candidates); got != 2 {
		t.Fatalf("first scan found %d addresses, want 2: %+v", got, memPanel.search.Candidates)
	}

	// One of the two addresses changes in the "game".
	ramEncode(4, 12, mem.data[8:12])
	memPanel.valueText = "12"
	memNarrowScan()
	if got := len(memPanel.search.Candidates); got != 1 {
		t.Fatalf("narrow left %d addresses, want 1: %+v", got, memPanel.search.Candidates)
	}
	addr := memPanel.search.Candidates[0].Addr
	if addr != mem.base+8 {
		t.Fatalf("narrow kept %s, want %s", ramFormatAddr(addr), ramFormatAddr(mem.base+8))
	}

	// Writing needs the second press.
	memPanel.phase = memPhaseResults
	memPanel.resultCursor = 0
	memPanel.valueText = "999999"
	memWriteSelected()
	if got := ramDecode(4, mem.data[8:12]); got != 12 {
		t.Fatalf("one press already wrote %d", got)
	}
	memWriteSelected()
	if got := ramDecode(4, mem.data[8:12]); got != 999999 {
		t.Fatalf("write stored %d, want 999999", got)
	}
	if got := memPanel.search.Candidates[0].Value; got != 999999 {
		t.Fatalf("candidate value not updated: %d", got)
	}
}

func TestMemWriteTruncatesToWidth(t *testing.T) {
	// 70000 is 0x11170: only its low two bytes (0x1170) fit the field width.
	data := make([]byte, 32)
	ramEncode(2, 70000, data[4:6])
	mem := memTestSession(t, data)
	memPanel.width = 2
	memPanel.valueText = "70000"
	memStartScan()
	if memPanel.search == nil || len(memPanel.search.Candidates) != 1 {
		t.Fatalf("scan found %+v, want the one truncated slot", memPanel.search)
	}
	memPanel.phase = memPhaseResults
	memPanel.resultCursor = 0
	memPanel.valueText = "0x11173"
	memWriteSelected()
	memWriteSelected()
	off := memPanel.search.Candidates[0].Addr - mem.base
	if got := ramDecode(2, mem.data[off:off+2]); got != 0x1173 {
		t.Fatalf("wrote %#x, want 0x1173", got)
	}
}

// A narrow that leaves nothing must still render the results page: the empty
// list used to be read for its first address.
func TestMemEmptyResultsRender(t *testing.T) {
	data := make([]byte, 16)
	ramEncode(4, 10, data[4:8])
	memTestSession(t, data)
	memPanel.valueText = "10"
	memStartScan()
	if memPanel.search == nil || len(memPanel.search.Candidates) != 1 {
		t.Fatalf("setup scan = %+v", memPanel.search)
	}

	// Nothing matches now, so the list empties.
	memPanel.valueText = "11"
	memNarrowScan()
	if got := len(memPanel.search.Candidates); got != 0 {
		t.Fatalf("narrow left %d addresses, want 0", got)
	}
	memPanel.phase = memPhaseResults
	memPanel.resultCursor = 0
	if sub := memSubtitle(); !strings.Contains(sub, "0 addresses") {
		t.Fatalf("subtitle = %q, want the empty state", sub)
	}
	found := false
	for _, row := range memRows() {
		if row.action != memActionResults {
			continue
		}
		found = true
		if !strings.Contains(row.value, "0 address") {
			t.Fatalf("results row = %q, want 0 addresses", row.value)
		}
	}
	if !found {
		t.Fatal("no Results row")
	}

	// Writing with nothing selected does nothing rather than panicking.
	memWriteSelected()
}

func TestMemWidthChangeDropsStaleSearch(t *testing.T) {
	data := make([]byte, 32)
	ramEncode(4, 55, data[4:8])
	memTestSession(t, data)
	memPanel.valueText = "55"
	memStartScan()
	if memPanel.search == nil {
		t.Fatal("scan produced no search")
	}
	memCycleWidth(1)
	if memPanel.search != nil {
		t.Fatal("a different width kept the old addresses")
	}
	if memPanel.width != 8 {
		t.Fatalf("width = %d, want 8", memPanel.width)
	}
}

func TestMemCloseDetachesAndKeepsTarget(t *testing.T) {
	mem := memTestSession(t, make([]byte, 16))
	closeMemPanel()
	if !mem.closed {
		t.Fatal("closing the panel did not release the process")
	}
	if memPanel.access != nil {
		t.Fatal("access survived close")
	}
	if memPanel.pid != 4242 || memPanel.pidName != "test-game" {
		t.Fatalf("target forgotten: %d %q", memPanel.pid, memPanel.pidName)
	}
	if memPanel.open {
		t.Fatal("panel still open")
	}
}

func TestMemKeysYieldToValueKeyboard(t *testing.T) {
	memTestSession(t, make([]byte, 16))
	prevVK := virtualKeyboardActive
	t.Cleanup(func() { virtualKeyboardActive = prevVK })

	virtualKeyboardActive = true
	if handleMemKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}, &Config{}) {
		t.Fatal("the panel consumed a key while its value keyboard was open")
	}

	virtualKeyboardActive = false
	before := memPanel.actionCursor
	if !handleMemKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}, &Config{}) {
		t.Fatal("the panel did not consume a key while it was the active modal")
	}
	if memPanel.actionCursor != before+1 {
		t.Fatalf("cursor = %d, want %d", memPanel.actionCursor, before+1)
	}
}

func TestMemChooseTargetRefusesSelf(t *testing.T) {
	prev := memPanel
	t.Cleanup(func() { memPanel = prev })

	memPanel = memPanelState{
		open:  true,
		phase: memPhaseTarget,
		procs: []procRow{{PID: memSelfPID(), Name: "JukaHub"}},
	}
	memChooseTarget()
	if memPanel.pid != 0 || memPanel.access != nil {
		t.Fatalf("attached to itself: pid=%d", memPanel.pid)
	}
	if memPanel.phase != memPhaseTarget {
		t.Fatal("panel left the picker after a refused target")
	}
}

// The panel is reached from the Power tools menu, next to the hex inspector.
func TestMemoryMenuRowOpensPanelFromPowerTools(t *testing.T) {
	prevOpen, prevPage, prevCursor := powerTools.open, powerTools.page, powerTools.cursor
	prevMem := memPanel
	t.Cleanup(func() {
		powerTools.open, powerTools.page, powerTools.cursor = prevOpen, prevPage, prevCursor
		memPanel = prevMem
	})

	rows := powerToolRows(&Config{})
	idx := -1
	for i, row := range rows {
		if row.action == powerActionOpenMemory {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no Memory scanner row in the Power tools menu: %+v", rows)
	}

	powerTools.open = true
	powerTools.page = powerPageMenu
	powerTools.cursor = idx
	memPanel = memPanelState{width: 4}
	powerActivate(&Config{})

	if !memPanel.open {
		t.Fatal("activating the row did not open the Memory panel")
	}
	if memPanel.phase != memPhaseTarget {
		t.Fatalf("phase = %v, want the process picker", memPanel.phase)
	}
	if powerTools.open {
		t.Fatal("Power tools stayed open behind the panel")
	}
}

// renderMemProbe draws the actions phase against a known background.
func renderMemProbe(t *testing.T, config *Config) (*probeFrame, sdl.Rect, []sdl.Rect) {
	t.Helper()
	f := newProbeFrame(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}

	prev := memPanel
	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() {
		memPanel = prev
		screenWidth, screenHeight = prevW, prevH
		memActionRects = nil
		memPanelRect = sdl.Rect{}
	})
	screenWidth, screenHeight = probeW, probeH
	memPanel = memPanelState{
		open:         true,
		phase:        memPhaseActions,
		pid:          4242,
		pidName:      "test-game",
		width:        4,
		valueText:    "5000",
		actionCursor: 0,
	}

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMemPanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f, memPanelRect, append([]sdl.Rect(nil), memActionRects...)
}

func TestMemPanelRenderLayout(t *testing.T) {
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	config := &Config{}
	f, panel, rects := renderMemProbe(t, config)

	if panel.W <= 0 || panel.H <= 0 {
		t.Fatalf("panel not laid out: %+v", panel)
	}
	if panel.X < 0 || panel.X+panel.W > probeW || panel.Y < 0 || panel.Y+panel.H > probeH {
		t.Fatalf("panel out of bounds: %+v", panel)
	}

	rows := memRows()
	if len(rects) != len(rows) {
		t.Fatalf("drew %d row rects for %d rows", len(rects), len(rows))
	}
	for i, r := range rects {
		if r.X < panel.X || r.X+r.W > panel.X+panel.W ||
			r.Y < panel.Y || r.Y+r.H > panel.Y+panel.H {
			t.Fatalf("row %d %+v escapes panel %+v", i, r, panel)
		}
		if i > 0 && r.Y < rects[i-1].Y+rects[i-1].H {
			t.Fatalf("row %d at y=%d overlaps the previous row ending at %d", i, r.Y, rects[i-1].Y+rects[i-1].H)
		}
	}

	// The focused row must be visibly highlighted.
	if lit, plain := bandLuma(f, rects[0]), bandLuma(f, rects[1]); lit <= plain {
		t.Fatalf("focused row is not highlighted: focused luma %d <= unfocused %d", lit, plain)
	}

	body := f.at(panel.X+panel.W/2, panel.Y+panel.H-4)
	if body.R == f.bg.R && body.G == f.bg.G && body.B == f.bg.B {
		t.Fatal("panel body is transparent")
	}
}
