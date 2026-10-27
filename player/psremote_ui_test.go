package main

// psremote_ui_test.go — the panel's decisions without a console on the LAN:
// which row a console gets, what pairing asks for first, and where the quality
// and volume rows write.

import (
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func openPSPanelForTest(t *testing.T) {
	t.Helper()
	closePSPanelAfterTest(t)
	chiakiPanel = chiakiPanelState{open: true}
}

func psRowByLabel(rows []chiakiRow, label string) (chiakiRow, bool) {
	for _, row := range rows {
		if row.label == label {
			return row, true
		}
	}
	return chiakiRow{}, false
}

func TestPSPanelListsPairedConsoleAsPlay(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	upsertPSConsole(testConsole())
	psPanelReload()

	rows := chiakiRows()
	row, ok := psRowByLabel(rows, testConsole().label())
	if !ok {
		t.Fatalf("paired console missing from rows: %+v", rows)
	}
	if row.action != psActionPlay || row.status != "Play" {
		t.Fatalf("paired console row = %+v, want Play", row)
	}
	if !strings.Contains(row.detail, "720p30") {
		t.Fatalf("play row should name the quality: %q", row.detail)
	}
}

func TestPSPanelListsUnpairedConsoleAsPair(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	psPanelMu.Lock()
	chiakiPanel.consoles = []psConsole{{Host: "192.168.1.77", Target: psTargetPS4FW10}}
	psPanelMu.Unlock()

	row, ok := psRowByLabel(chiakiRows(), "192.168.1.77 · PS4")
	if !ok {
		t.Fatalf("discovered console missing from rows: %+v", chiakiRows())
	}
	if row.action != psActionPair || row.status != "Pair" {
		t.Fatalf("unpaired row = %+v, want Pair", row)
	}
}

func TestPSPanelEmptyListExplainsWhatToDo(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	rows := chiakiRows()
	if _, ok := psRowByLabel(rows, "No consoles yet"); !ok {
		t.Fatalf("empty list should say what to do: %+v", rows)
	}
	for _, want := range []string{"Scan for consoles", "Add console by address", "PSN Account-ID", "Quality", "Volume", "Close"} {
		if _, ok := psRowByLabel(rows, want); !ok {
			t.Fatalf("row %q missing from %+v", want, rows)
		}
	}
}

func TestPSPanelPairAsksForAccountFirstOnPS5(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	t.Cleanup(func() { vkCloseKeyboard(); psPanelEditField = psEditNone })
	chiakiPanel.consoles = []psConsole{{Host: "192.168.1.50", Target: psTargetPS5_1, PS5: true}}

	psPanelBeginPair("192.168.1.50")
	if !virtualKeyboardActive || vkTarget != vkTargetPSRemote {
		t.Fatal("pairing a PS5 without an Account-ID should open the keyboard")
	}
	if psPanelEditField != psEditAccount {
		t.Fatalf("field = %v, want the Account-ID", psPanelEditField)
	}
	if chiakiPanel.pairHost != "192.168.1.50" || chiakiPanel.pairTarget != psTargetPS5_1 {
		t.Fatalf("pair target not remembered: %+v", chiakiPanel)
	}
}

func TestPSPanelPairAsksForPinWhenAccountIsKnown(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	t.Cleanup(func() { vkCloseKeyboard(); psPanelEditField = psEditNone })
	chiakiPanel.account = testPSNAccountID
	chiakiPanel.consoles = []psConsole{{Host: "192.168.1.50", Target: psTargetPS5_1, PS5: true}}

	psPanelBeginPair("192.168.1.50")
	if psPanelEditField != psEditPIN {
		t.Fatalf("field = %v, want the PIN", psPanelEditField)
	}
}

// TestPSPanelAccountSubmitContinuesToPin covers the PS5 flow: the Account-ID
// prompt opens first, and submitting it hands over to the PIN instead of making
// the user press the console row again.
func TestPSPanelAccountSubmitContinuesToPin(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	t.Cleanup(func() { vkCloseKeyboard(); psPanelEditField = psEditNone })
	chiakiPanel.consoles = []psConsole{{Host: "192.168.1.50", Target: psTargetPS5_1, PS5: true}}

	psPanelBeginPair("192.168.1.50")
	if psPanelEditField != psEditAccount {
		t.Fatalf("field = %v, want the Account-ID first", psPanelEditField)
	}
	psPanelApplyEdit(testPSNAccountID)
	if psPanelEditField != psEditPIN {
		t.Fatalf("field = %v, want the PIN after the Account-ID", psPanelEditField)
	}
	if !virtualKeyboardActive || vkTarget != vkTargetPSRemote {
		t.Fatal("the keyboard should stay open for the PIN")
	}
	if chiakiPanel.pairHost != "192.168.1.50" {
		t.Fatalf("pair host = %q", chiakiPanel.pairHost)
	}
}

func TestPSPanelPairPS4GoesStraightToPin(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	t.Cleanup(func() { vkCloseKeyboard(); psPanelEditField = psEditNone })
	chiakiPanel.consoles = []psConsole{{Host: "192.168.1.40", Target: psTargetPS4FW10}}

	psPanelBeginPair("192.168.1.40")
	if psPanelEditField != psEditPIN {
		t.Fatalf("field = %v, want the PIN", psPanelEditField)
	}
	if chiakiPanel.pairTarget != psTargetPS4FW10 {
		t.Fatalf("pair target = %d, want the discovered firmware", chiakiPanel.pairTarget)
	}
}

func TestPSPanelAccountSubmitPersists(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	psPanelEditField = psEditAccount

	psPanelApplyEdit(" " + testPSNAccountID + " ")
	if chiakiPanel.account != testPSNAccountID {
		t.Fatalf("account = %q", chiakiPanel.account)
	}
	if got := loadPSHosts().Settings.Account; got != testPSNAccountID {
		t.Fatalf("stored account = %q", got)
	}
	if psPanelEditField != psEditNone {
		t.Fatal("the edit field should be cleared after submit")
	}
}

func TestPSPanelQualityAndVolumeRowsWriteThrough(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	rows := chiakiRows()
	for i, row := range rows {
		if row.action == psActionQuality {
			chiakiPanel.cursor = i
		}
	}
	before := chiakiPanel.quality
	if !chiakiPanelAdjust(1) {
		t.Fatal("Right on the quality row should adjust it")
	}
	if want := (before + 1) % len(psQualityPresets); chiakiPanel.quality != want {
		t.Fatalf("quality = %d, want %d", chiakiPanel.quality, want)
	}
	if got := loadPSHosts().Settings.Quality; got != chiakiPanel.quality {
		t.Fatalf("stored quality = %d", got)
	}

	rows = chiakiRows()
	for i, row := range rows {
		if row.action == psActionVolume {
			chiakiPanel.cursor = i
		}
	}
	chiakiPanel.volume = 50
	chiakiPanelAdjust(1)
	if chiakiPanel.volume != 55 {
		t.Fatalf("volume = %d, want 55", chiakiPanel.volume)
	}
	if got := loadPSHosts().Settings.Volume; got != 55 {
		t.Fatalf("stored volume = %d", got)
	}
	chiakiPanel.volume = 100
	chiakiPanelAdjust(1)
	if chiakiPanel.volume != 100 {
		t.Fatalf("volume should clamp at 100, got %d", chiakiPanel.volume)
	}
}

func TestPSPanelForgetRemovesTheConsole(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	upsertPSConsole(testConsole())
	psPanelReload()
	chiakiPanel.cursor = 0

	psPanelForget()
	if hosts := loadPSHosts().Hosts; len(hosts) != 0 {
		t.Fatalf("forget left %+v", hosts)
	}
	if len(chiakiRows()) == 0 {
		t.Fatal("the panel should still render after forgetting")
	}
}

// TestPSPanelPlayRefusesUnpairedConsole keeps the stream from being started
// with credentials that cannot work.
func TestPSPanelPlayRefusesUnpairedConsole(t *testing.T) {
	clearToastAfterTest(t)
	psPanelPlay(psConsole{Host: "192.168.1.40", Target: psTargetPS4FW10})
	if currentToast.Message == "" {
		t.Fatal("an unpaired play press should explain itself")
	}
}

func TestPSPanelSubtitleNamesTheMissingHelper(t *testing.T) {
	t.Setenv("JUKAHUB_CHIAKI_STREAM", "")
	psEngineStreamOverride = ""
	t.Cleanup(func() { psEngineStreamOverride = "" })
	subtitle := psPanelSubtitle()
	if !strings.Contains(subtitle, "Helper missing") {
		t.Fatalf("subtitle = %q", subtitle)
	}
	if !strings.Contains(subtitle, "chiaki-stream") {
		t.Fatalf("subtitle should name chiaki-stream: %q", subtitle)
	}
}

func TestPSPanelMergesDiscoveryWithoutCredentials(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	upsertPSConsole(testConsole())
	psPanelMergeDiscovered([]psDiscovered{{
		Host:   "192.168.1.40",
		Name:   "Living Room",
		Target: psTargetPS5_1,
		PS5:    true,
	}, {
		Host:   "192.168.1.90",
		Name:   "Bedroom",
		Target: psTargetPS4FW10,
	}})

	rows := chiakiRows()
	bedroom, ok := psRowByLabel(rows, "Bedroom · PS4")
	if !ok || bedroom.action != psActionPair {
		t.Fatalf("discovered console row = %+v", bedroom)
	}
	var living chiakiRow
	for _, row := range rows {
		if strings.HasPrefix(row.label, "Living Room") {
			living = row
		}
	}
	if living.action != psActionPlay {
		t.Fatalf("the stored pairing was lost in the merge: %+v", living)
	}
	if got := loadPSHosts().Hosts; len(got) != 1 {
		t.Fatalf("discovery persisted an unpaired console: %+v", got)
	}
}

func TestPSPanelScanIsSingleFlight(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	t.Cleanup(func() {
		psPanelMu.Lock()
		chiakiPanel.scanning = false
		psPanelMu.Unlock()
	})
	psPanelMu.Lock()
	chiakiPanel.scanning = true
	psPanelMu.Unlock()

	psPanelScan()
	psPanelMu.Lock()
	running := chiakiPanel.scanning
	psPanelMu.Unlock()
	if !running {
		t.Fatal("a second scan cleared the running flag")
	}
}

// TestPSPanelPairRefusesEmptyHost guards the goroutine launch.
func TestPSPanelPairRefusesEmptyHost(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	psPanelPair("", "12345678", psTargetPS4FW10)
	psPanelMu.Lock()
	busy := chiakiPanel.busy
	psPanelMu.Unlock()
	if busy != "" {
		t.Fatalf("pairing an empty host set busy=%q", busy)
	}
}

// TestPSPanelPS4TargetsFallBackThroughFirmware keeps a manually added address
// working: discovery is what normally says which generation answered, so a
// typed address has to try the current firmware and then the older protocols.
func TestPSPanelPS4TargetsFallBackThroughFirmware(t *testing.T) {
	cases := []struct {
		target int
		want   []int
	}{
		{psTargetPS4FW10, []int{psTargetPS4FW10, psTargetPS4FW9, psTargetPS4FW8}},
		{0, []int{psTargetPS4FW10, psTargetPS4FW9, psTargetPS4FW8}},
		{psTargetPS4FW9, []int{psTargetPS4FW9, psTargetPS4FW8}},
		{psTargetPS4FW8, []int{psTargetPS4FW8}},
	}
	for _, tc := range cases {
		got := psPanelPS4Targets(tc.target)
		if len(got) != len(tc.want) {
			t.Fatalf("target %d: got %v, want %v", tc.target, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("target %d: got %v, want %v", tc.target, got, tc.want)
			}
		}
	}
}

// TestPSPanelRendersItsRows draws the real panel offscreen: every row the draw
// loop records must sit inside the panel, and the panel must be on screen.
func TestPSPanelRendersItsRows(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	upsertPSConsole(testConsole())
	psPanelMergeDiscovered([]psDiscovered{{Host: "192.168.1.90", Name: "Bedroom", Target: psTargetPS4FW10}})

	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })
	screenWidth, screenHeight = probeW, probeH

	renderer.SetDrawColor(frame.bg.R, frame.bg.G, frame.bg.B, 255)
	renderer.Clear()
	renderChiakiPanel(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	panel := chiakiPanelRect
	if panel.W <= 0 || panel.H <= 0 {
		t.Fatalf("panel rect = %+v", panel)
	}
	if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > probeW || panel.Y+panel.H > probeH {
		t.Fatalf("panel %+v does not fit the %dx%d screen", panel, probeW, probeH)
	}
	if len(chiakiRowHits) == 0 {
		t.Fatal("no row was recorded, so nothing is clickable")
	}
	for _, hit := range chiakiRowHits {
		if hit.rect.W <= 0 || hit.rect.X < panel.X || hit.rect.X+hit.rect.W > panel.X+panel.W {
			t.Fatalf("row %d %+v is outside the panel %+v", hit.index, hit.rect, panel)
		}
	}

	// The panel paints over the dimmed background.
	probe := frame.at(panel.X+panel.W/2, panel.Y+8)
	if nearColor(probe, frame.bg, 6) {
		t.Fatalf("panel centre is still the background colour %v", probe)
	}
}

func TestPSPanelRowsSurviveReload(t *testing.T) {
	useTempPSHosts(t)
	openPSPanelForTest(t)
	upsertPSConsole(testConsole())
	psPanelReload()
	for i := 0; i < 3; i++ {
		if len(chiakiRows()) < 2 {
			t.Fatal("row list collapsed after a reload")
		}
	}
	if chiakiPanel.consoles[0].PairedAt.IsZero() || time.Since(chiakiPanel.consoles[0].PairedAt) > time.Hour {
		t.Fatalf("paired timestamp not carried: %+v", chiakiPanel.consoles[0])
	}
}
