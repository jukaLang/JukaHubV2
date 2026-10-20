package main

import (
	"strings"
	"testing"
)

func withPowerToolsState(t *testing.T, state powerToolsState) {
	t.Helper()
	prev := powerTools
	powerTools = state
	t.Cleanup(func() { powerTools = prev })
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
		t.Fatalf("menu rows = %d, want 6 tools + Close", len(rows))
	}
	want := []powerAction{
		powerActionOpenNetwork, powerActionOpenFiles, powerActionOpenScripts,
		powerActionOpenSync, powerActionOpenConfigs, powerActionOpenBench,
		powerActionClose,
	}
	for i, action := range want {
		if rows[i].action != action {
			t.Fatalf("row %d action = %v, want %v", i, rows[i].action, action)
		}
	}
	if !strings.Contains(rows[2].value, "1 snippet") || !strings.Contains(rows[4].value, "1 snapshot") {
		t.Fatalf("menu does not show cached counts: %+v", rows)
	}
	if rows[3].value != "nas:/backup" {
		t.Fatalf("sync value = %q", rows[3].value)
	}
}

func TestPowerToolsNetworkRows(t *testing.T) {
	withPowerToolsState(t, powerToolsState{page: powerPageNetwork})
	rows := powerToolRows(&Config{})
	if len(rows) != 7 {
		t.Fatalf("network rows = %d, want 6 probes + Back", len(rows))
	}
	actions := []powerAction{
		powerActionScan, powerActionScanClipboard, powerActionPing, powerActionTrace,
		powerActionDNS, powerActionWiFi, powerActionBack,
	}
	for i, action := range actions {
		if rows[i].action != action {
			t.Fatalf("row %d action = %v, want %v", i, rows[i].action, action)
		}
	}
}

func TestPowerToolsScriptRows(t *testing.T) {
	withPowerToolsState(t, powerToolsState{
		page:    powerPageScripts,
		scripts: []powerScript{{Name: "disk", Command: "df -h"}},
	})
	rows := powerToolRows(&Config{})
	if len(rows) != 3 {
		t.Fatalf("script rows = %d, want snippet, save and Back", len(rows))
	}
	if rows[0].action != powerActionRunScript || rows[0].arg != "0" {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[1].action != powerActionSaveClipboardScript || !rows[1].enabled {
		t.Fatalf("save row = %+v", rows[1])
	}
	if rows[2].action != powerActionBack {
		t.Fatalf("last row = %+v", rows[2])
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
	if len(rows) != 3 || rows[0].action != powerActionSnapshotNow || rows[1].action != powerActionDiffSnapshot {
		t.Fatalf("config rows = %+v", rows)
	}
	powerTools.page = powerPageBench
	rows = powerToolRows(&Config{})
	if len(rows) != 6 {
		t.Fatalf("bench rows = %d, want actions + same-model summary + result + Back", len(rows))
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
	w, h := powerPageSize(powerPageMenu, 70)
	if w != 860 || h > 696 {
		t.Fatalf("menu panel = %dx%d, want 860 wide and at most 696 tall", w, h)
	}
	powerTools.page = powerPageFiles
	powerTools.files = make([]powerFileInfo, 12)
	_, h = powerPageSize(powerPageFiles, 100)
	if h > 696 {
		t.Fatalf("long file list panel = %dpx, want at most 696", h)
	}
	_, h = powerPageSize(powerPageOutput, 70)
	if h != 640 {
		t.Fatalf("output panel = %dpx, want 640", h)
	}
}
