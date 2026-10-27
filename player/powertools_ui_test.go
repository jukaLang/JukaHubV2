package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func withPowerToolsState(t *testing.T, state powerToolsState) {
	t.Helper()
	prev := powerTools
	powerTools = state
	t.Cleanup(func() { powerTools = prev })
}

// waitForPowerAsync blocks until the runner powerStartAsync started has
// finished. That goroutine reads package state — the toolbox directory
// override, the config — for as long as it runs, so a test that triggered one
// must not let its cleanups rewrite that state until it is done. The scan-row
// test used to restore its tools-dir override while the tool it had just
// launched was still resolving its path through it: a data race under -race,
// and outside it a coin flip over which install the runner would see.
func waitForPowerAsync(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&powerTools.busy) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the async power runner was still busy 5s after it started")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// renderPowerProbe draws the Power tools panel over a known background so the
// list's visual rules can be checked on real pixels.
func renderPowerProbe(t *testing.T, config *Config, page powerPage, cursor int) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}

	prevW, prevH := screenWidth, screenHeight
	prevOpen, prevPage, prevCursor := powerTools.open, powerTools.page, powerTools.cursor
	screenWidth, screenHeight = probeW, probeH
	powerTools.open = true
	powerTools.page = page
	powerTools.cursor = cursor
	powerRefreshCaches()
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		powerTools.open, powerTools.page, powerTools.cursor = prevOpen, prevPage, prevCursor
		powerRowRects = nil
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderPowerToolsPanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f
}

func TestPowerToolsMenuRows(t *testing.T) {
	withPowerToolsState(t, powerToolsState{
		page:       powerPageMenu,
		scripts:    []powerScript{{Name: "mine", Command: "echo"}},
		snapshots:  []powerSnapshot{{Name: "a.json"}},
		syncTarget: "nas:/backup",
	})
	rows := powerToolRows(&Config{})
	if len(rows) != 7 {
		t.Fatalf("menu rows = %d, want the seven tools and no Close row", len(rows))
	}
	want := []powerAction{
		powerActionOpenFiles, powerActionOpenScripts,
		powerActionOpenSync, powerActionOpenConfigs, powerActionOpenBench,
		powerActionOpenMemory, powerActionOpenCtrlDiag,
	}
	for i, action := range want {
		if rows[i].action != action {
			t.Fatalf("row %d action = %v, want %v", i, rows[i].action, action)
		}
	}
	if !strings.Contains(rows[1].value, "1 snippet") || !strings.Contains(rows[3].value, "1 snapshot") {
		t.Fatalf("menu does not show cached counts: %+v", rows)
	}
	if rows[2].value != "nas:/backup" {
		t.Fatalf("sync value = %q", rows[2].value)
	}
	// The network probes moved to Tools → FTP & IP; the menu must not offer a
	// second copy of them.
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.label), "network") {
			t.Fatalf("Power tools still lists network probes: %+v", row)
		}
	}
}

func TestPowerToolsScriptRows(t *testing.T) {
	withPowerToolsState(t, powerToolsState{
		page:    powerPageScripts,
		scripts: []powerScript{{Name: "disk", Command: "df -h"}},
	})
	rows := powerToolRows(&Config{})
	if len(rows) != 2 {
		t.Fatalf("script rows = %d, want the snippet and the save row", len(rows))
	}
	if rows[0].action != powerActionRunScript || rows[0].arg != "0" {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[1].action != powerActionSaveClipboardScript || !rows[1].enabled {
		t.Fatalf("save row = %+v", rows[1])
	}
}

// The footer carries the focused row's action plus the page's own, so no row
// repeats its action as a right-edge pill.
func TestPowerFooterNamesTheFocusedRow(t *testing.T) {
	withPowerToolsState(t, powerToolsState{page: powerPageMenu})
	rows := powerToolRows(&Config{})
	got := powerFooterFor(powerPageMenu, rows, 0)
	if !strings.Contains(got, "A Open") || !strings.Contains(got, "B Close") {
		t.Fatalf("menu footer = %q, want the focused row's action and the way out", got)
	}

	withPowerToolsState(t, powerToolsState{
		page:    powerPageScripts,
		scripts: []powerScript{{Name: "built-in", Command: "echo"}},
	})
	rows = powerToolRows(&Config{})
	got = powerFooterFor(powerPageScripts, rows, 0)
	if !strings.Contains(got, "A Run") || !strings.Contains(got, "Y Save to Clipboard") || !strings.Contains(got, "B Back") {
		t.Fatalf("script footer = %q, want run, save and back", got)
	}

	// A row with no action (a benchmark result) leaves the page actions only.
	withPowerToolsState(t, powerToolsState{
		page:  powerPageBench,
		bench: []powerBenchEntry{{Device: "fast", Write: 90, Read: 80, At: "now"}},
	})
	rows = powerToolRows(&Config{})
	last := len(rows) - 1
	if rows[last].hint != "" {
		t.Fatalf("benchmark result row carries a hint: %+v", rows[last])
	}
	if got := powerFooterFor(powerPageBench, rows, last); got != "B Back" {
		t.Fatalf("result footer = %q, want just the way back", got)
	}
}

func TestPowerToolsListScroll(t *testing.T) {
	scripts := make([]powerScript, 12)
	for i := range scripts {
		scripts[i] = powerScript{Name: "s", Command: "echo"}
	}
	withPowerToolsState(t, powerToolsState{
		page:    powerPageScripts,
		scripts: scripts,
		visible: 5,
	})
	powerMove(7)
	if powerTools.cursor != 7 {
		t.Fatalf("cursor = %d, want 7", powerTools.cursor)
	}
	if powerTools.scroll != 3 {
		t.Fatalf("scroll = %d, want cursor-visible+1 = 3", powerTools.scroll)
	}
	powerMove(-100)
	if powerTools.cursor != 0 || powerTools.scroll != 0 {
		t.Fatalf("cursor/scroll = %d/%d, want 0/0", powerTools.cursor, powerTools.scroll)
	}
}

func TestPowerToolsConfigAndBenchRows(t *testing.T) {
	withPowerToolsState(t, powerToolsState{
		page:      powerPageConfigs,
		snapshots: []powerSnapshot{{Name: "jukauser-1.json", Size: 20}},
		bench:     []powerBenchEntry{{Device: "fast", Write: 90, Read: 80, At: "now"}},
	})
	rows := powerToolRows(&Config{})
	if len(rows) != 2 || rows[0].action != powerActionSnapshotNow || rows[1].action != powerActionDiffSnapshot {
		t.Fatalf("config rows = %+v", rows)
	}
	powerTools.page = powerPageBench
	rows = powerToolRows(&Config{})
	if len(rows) != 5 {
		t.Fatalf("bench rows = %d, want actions + same-model summary + result", len(rows))
	}
	if rows[0].action != powerActionRunBenchmark || rows[3].enabled || rows[4].enabled {
		t.Fatalf("bench rows = %+v", rows)
	}
	if !strings.Contains(rows[4].value, "80.0 MB/s") {
		t.Fatalf("result value = %q, want min(read, write)", rows[4].value)
	}
}

func TestPowerWrapByMeasure(t *testing.T) {
	withPowerToolsState(t, powerToolsState{})
	measure := func(s string) int32 { return int32(len([]rune(s)) * 7) }
	text := strings.Repeat("word ", 80) + "\n\nlast line"
	lines := powerWrapByMeasure(text, 220, measure)
	if len(lines) < 3 {
		t.Fatalf("lines = %d, want wrapped output", len(lines))
	}
	for _, line := range lines {
		if measure(line) > 220 {
			t.Fatalf("line %q is %dpx, wider than 220", line, measure(line))
		}
	}
	if strings.Count(strings.Join(lines, " "), "word") != 80 {
		t.Fatalf("wrapping lost words: %q", strings.Join(lines, " "))
	}
	if lines[len(lines)-1] != "last line" {
		t.Fatalf("last line = %q", lines[len(lines)-1])
	}
	// A long unbroken token is hard-split instead of overflowing.
	long := powerWrapByMeasure(strings.Repeat("x", 100), 70, measure)
	if len(long) != 10 {
		t.Fatalf("long token split into %d lines, want 10", len(long))
	}
}

func TestPowerToolsPanelSizing(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = 1280, 720
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	withPowerToolsState(t, powerToolsState{page: powerPageMenu})
	// The header band the renderer measures for a medium/small pair on this
	// canvas; the sizing contract is about the rows below it.
	const headerH = int32(96)
	w, h := powerPageSize(powerPageMenu, 70, headerH)
	if w != 860 || h > 696 {
		t.Fatalf("menu panel = %dx%d, want 860 wide and at most 696 tall", w, h)
	}
	powerTools.page = powerPageFiles
	powerTools.files = make([]powerFileInfo, 12)
	_, h = powerPageSize(powerPageFiles, 100, headerH)
	if h > 696 {
		t.Fatalf("long file list panel = %dpx, want at most 696", h)
	}
	_, h = powerPageSize(powerPageOutput, 70, headerH)
	if h != 640 {
		t.Fatalf("output panel = %dpx, want 640", h)
	}
}

// The panel is sized to the rows it can show: no dead band under the last row,
// and never taller than the screen.
func TestPowerPanelSizedToVisibleRows(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = 1280, 720
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	withPowerToolsState(t, powerToolsState{page: powerPageMenu})
	const rowH = int32(82)
	_, h := powerPageSize(powerPageMenu, rowH, 96)
	if h > 720-24 {
		t.Fatalf("panel = %dpx, taller than the 696px a 720px screen leaves", h)
	}
	if h+rowH <= 720-24 {
		t.Fatalf("panel = %dpx leaves room for another %dpx row: dead space under the last one", h, rowH)
	}
}

// The selected row reads at a glance: a filled row with an accent spine. The
// rows themselves carry no action pill — the footer names what A does.
func TestPowerToolsPanelMarksTheFocusedRow(t *testing.T) {
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	f := renderPowerProbe(t, &Config{}, powerPageMenu, 0)
	if len(powerRowRects) < 2 {
		t.Fatalf("rows drawn = %d, want at least two", len(powerRowRects))
	}
	focus, other := powerRowRects[0], powerRowRects[1]
	if focus.W == 0 || other.W == 0 {
		t.Fatalf("row rects = %+v / %+v", focus, other)
	}

	spine := f.at(focus.X+3, focus.Y+focus.H/2)
	plain := f.at(other.X+3, other.Y+other.H/2)
	if spine == plain {
		t.Fatalf("focused row has no accent spine: both columns are %+v", spine)
	}
	if f.at(focus.X+60, focus.Y+8) == f.at(other.X+60, other.Y+8) {
		t.Fatal("focused row is not filled; the list reads as one flat surface")
	}
}
