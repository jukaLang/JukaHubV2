package main

// corebuilds_ui_test.go — the kept-builds panel's promises: it lists every kept
// build with the date and size the row is supposed to carry, it clamps its
// cursor to what is on the card, and deleting takes two presses and then really
// removes the file.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// coreBuildsFixture points the cores folders at a temp tree and writes the
// given kept builds into it, named exactly the way installCoreFile names the
// copies it keeps.
func coreBuildsFixture(t *testing.T, names ...string) string {
	t.Helper()
	cores := t.TempDir()
	for _, name := range names {
		writeCompatTestFile(t, filepath.Join(cores, name), "core")
	}
	setCompatTestDirs(t, t.TempDir(), nil, []string{cores}, "")
	t.Cleanup(func() {
		kbPanel = coreBuildsPanelState{}
		coreBuildsRowHits = nil
		coreBuildsPanelRect = sdl.Rect{}
		// These tests open the page the way the Emulators list does, so closing
		// it restores that list. Left open, the restored page belongs to
		// whichever test runs next and the idle screen refuses to appear
		// because a panel is up — a failure that only shows under a shuffle.
		emuPanel = emuPanelState{}
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	})
	return cores
}

func TestCoreBuildRowsCarryDateAndSize(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 12, 0, 0, 0, time.Local)
	backup := coreBackup{
		Core:   "snes9x",
		Path:   filepath.Join("/mnt/SDCARD/RetroArch/.retroarch/cores", "snes9x_libretro.so.jukahub-20260102-120000.bak"),
		Target: filepath.Join("/mnt/SDCARD/RetroArch/.retroarch/cores", "snes9x_libretro.so"),
		Stamp:  stamp,
		Size:   3 << 20,
	}
	if got := coreBuildsFileLabel(backup); got != "snes9x_libretro.so" {
		t.Fatalf("row label = %q, want the core file without the backup stamp", got)
	}
	meta := coreBuildsMeta(backup, stamp.Add(3*24*time.Hour))
	if !strings.Contains(meta, "2026-01-02 12:00") {
		t.Fatalf("row meta %q is missing the build date", meta)
	}
	if !strings.Contains(meta, "3 days ago") {
		t.Fatalf("row meta %q is missing the age next to the date", meta)
	}
	if !strings.Contains(meta, "cores") {
		t.Fatalf("row meta %q is missing the folder the build restores into", meta)
	}
}

func TestCoreBuildAgePhrases(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.Local)
	cases := []struct {
		stamp time.Time
		want  string
	}{
		{now.Add(-2 * time.Hour), "today"},
		{now.Add(-30 * time.Hour), "yesterday"},
		{now.Add(-5 * 24 * time.Hour), "5 days ago"},
		{now.Add(-21 * 24 * time.Hour), "3 weeks ago"},
		{now.Add(-95 * 24 * time.Hour), "3 months ago"},
		{now.Add(-3 * 365 * 24 * time.Hour), "3 years ago"},
		{now.Add(time.Hour), "stamped in the future"},
	}
	for _, c := range cases {
		if got := coreBuildAge(c.stamp, now); got != c.want {
			t.Errorf("coreBuildAge(%v) = %q, want %q", c.stamp, got, c.want)
		}
	}
}

func TestCoreBuildsSubtitleSaysNothingHeldWhenEmpty(t *testing.T) {
	if got := coreBuildsSubtitle(nil, 0); !strings.Contains(got, "No kept builds") {
		t.Fatalf("empty subtitle = %q, want it to say nothing is held", got)
	}
	rows := []coreBackup{{Size: 1 << 20}, {Size: 2 << 20}}
	got := coreBuildsSubtitle(rows, 3<<20)
	for _, want := range []string{"2 kept", "3.0 MB", "5"} {
		if !strings.Contains(got, want) {
			t.Errorf("subtitle %q is missing %q", got, want)
		}
	}
}

func TestCoreBuildsPanelListsEveryBuildNewestFirst(t *testing.T) {
	coreBuildsFixture(t,
		"snes9x_libretro.so.jukahub-20260102-120000.bak",
		"snes9x_libretro.so.jukahub-20260401-090000.bak",
		"mgba_libretro.so.jukahub-20260201-080000.bak",
	)
	openCoreBuildsPanel(true)
	if len(kbPanel.rows) != 3 {
		t.Fatalf("rows = %d, want all three kept builds across the card", len(kbPanel.rows))
	}
	if !kbPanel.rows[0].Stamp.After(kbPanel.rows[1].Stamp) {
		t.Fatalf("rows are not newest-first: %v then %v", kbPanel.rows[0].Stamp, kbPanel.rows[1].Stamp)
	}
	// The newest build is April's, across both cores.
	if got := kbPanel.rows[0].Stamp.Month(); got != time.April {
		t.Fatalf("first row is %v, want the April build", kbPanel.rows[0].Stamp)
	}
	if kbPanel.total != 12 {
		t.Fatalf("total = %d bytes, want every kept build's size summed", kbPanel.total)
	}
}

// Two presses, and the arm expires: one press must never delete a build.
func TestCoreBuildsDeleteNeedsTwoPresses(t *testing.T) {
	cores := coreBuildsFixture(t, "snes9x_libretro.so.jukahub-20260102-120000.bak")
	openCoreBuildsPanel(true)
	path := filepath.Join(cores, "snes9x_libretro.so.jukahub-20260102-120000.bak")

	coreBuildsDelete()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the first X press deleted the build: %v", err)
	}
	if kbPanel.confirm != path {
		t.Fatalf("the first press did not arm the row: confirm=%q", kbPanel.confirm)
	}

	// An arm that aged past the window is not a confirm.
	kbPanel.confirmAt = time.Now().Add(-coreBuildsConfirmWindow - time.Second)
	coreBuildsDelete()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("an expired arm deleted the build: %v", err)
	}
	if kbPanel.confirm != path {
		t.Fatalf("the re-arm did not reset the window: confirm=%q", kbPanel.confirm)
	}

	coreBuildsDelete()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the second press did not delete the build: %v", err)
	}
	if len(kbPanel.rows) != 0 {
		t.Fatalf("the list still shows %d build(s) after deleting the only one", len(kbPanel.rows))
	}
}

func TestCoreBuildsDeleteRefusesAPathOutsideTheCoresFolder(t *testing.T) {
	dir := t.TempDir()
	coreBuildsFixture(t)
	outside := filepath.Join(dir, "snes9x_libretro.so.jukahub-20260102-120000.bak")
	if err := os.WriteFile(outside, []byte("core"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := deleteCoreBackup(coreBackup{Path: outside, Target: filepath.Join(dir, "snes9x_libretro.so")})
	if err == nil {
		t.Fatal("deleteCoreBackup accepted a file outside the cores folder")
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("the refused delete removed the file anyway: %v", statErr)
	}
}

// A cursor that outlives its row is the empty-list crash: moving down past the
// last kept build must stop at it, and a rescan must pull the cursor back.
func TestCoreBuildsCursorStaysOnARow(t *testing.T) {
	coreBuildsFixture(t,
		"snes9x_libretro.so.jukahub-20260102-120000.bak",
		"snes9x_libretro.so.jukahub-20260401-090000.bak",
	)
	openCoreBuildsPanel(true)
	coreBuildsMove(5)
	if kbPanel.cursor != 1 {
		t.Fatalf("cursor = %d, want it clamped to the last row", kbPanel.cursor)
	}
	coreBuildsMove(-9)
	if kbPanel.cursor != 0 {
		t.Fatalf("cursor = %d, want it clamped to the first row", kbPanel.cursor)
	}
	if _, ok := coreBuildsSelected(); !ok {
		t.Fatal("no row selected while the list is not empty")
	}
}

// The panel draws one row per kept build inside its own card, with the size at
// the right edge: a row list that draws nothing is the bug this pins.
func TestCoreBuildsPanelRendersEveryRow(t *testing.T) {
	coreBuildsFixture(t,
		"snes9x_libretro.so.jukahub-20260102-120000.bak",
		"mgba_libretro.so.jukahub-20260201-080000.bak",
	)
	renderer := probeRenderer(t)
	openCoreBuildsPanel(true)
	t.Cleanup(func() { kbPanel = coreBuildsPanelState{} })

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderCoreBuildsPanel(renderer, layoutTestConfig(t))

	if len(coreBuildsRowHits) != 2 {
		t.Fatalf("row hits = %d, want one per kept build", len(coreBuildsRowHits))
	}
	if coreBuildsPanelRect.W <= 0 || coreBuildsPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", coreBuildsPanelRect)
	}
	if coreBuildsPanelRect.X < 0 || coreBuildsPanelRect.Y < 0 ||
		coreBuildsPanelRect.X+coreBuildsPanelRect.W > probeW ||
		coreBuildsPanelRect.Y+coreBuildsPanelRect.H > probeH {
		t.Fatalf("panel %+v does not fit the %dx%d canvas", coreBuildsPanelRect, probeW, probeH)
	}
	for i, hit := range coreBuildsRowHits {
		if hit.rect.W <= 0 || hit.rect.H <= 0 {
			t.Errorf("row %d has no rect: %+v", i, hit.rect)
		}
		if !pointInRect(hit.rect.X+hit.rect.W/2, hit.rect.Y+hit.rect.H/2, coreBuildsPanelRect) {
			t.Errorf("row %d %+v sits outside the panel", i, hit.rect)
		}
	}
}

// The empty state has to say where it looked, so an empty list on a card that
// does have a cores folder is a different answer from a card with none.
func TestCoreBuildsPanelRendersEmptyState(t *testing.T) {
	coreBuildsFixture(t)
	renderer := probeRenderer(t)
	openCoreBuildsPanel(true)
	t.Cleanup(func() { kbPanel = coreBuildsPanelState{} })

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderCoreBuildsPanel(renderer, layoutTestConfig(t))

	if len(kbPanel.rows) != 0 {
		t.Fatalf("rows = %d, want none on an empty card", len(kbPanel.rows))
	}
	if coreBuildsPanelRect.W <= 0 {
		t.Fatalf("the empty panel did not lay out: %+v", coreBuildsPanelRect)
	}
	if got := coresRootLabel(); got == "" || got == "not found" {
		t.Fatalf("coresRootLabel() = %q, want the temp cores folder", got)
	}
}

// Rows are what a click hits; a click on a row restores it, a click outside
// closes the panel.
func TestCoreBuildsMouseActivatesTheClickedRow(t *testing.T) {
	coreBuildsFixture(t,
		"snes9x_libretro.so.jukahub-20260102-120000.bak",
		"snes9x_libretro.so.jukahub-20260401-090000.bak",
	)
	renderer := probeRenderer(t)
	openCoreBuildsPanel(true)
	t.Cleanup(func() { kbPanel = coreBuildsPanelState{} })

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderCoreBuildsPanel(renderer, layoutTestConfig(t))

	// A click on a row restores that build: the core appears and the copy it
	// was restored from leaves the list.
	hit := coreBuildsRowHits[1]
	target := kbPanel.rows[1].Target
	handleCoreBuildsMouse(hit.rect.X+4, hit.rect.Y+4)
	if !kbPanel.open {
		t.Fatal("a click on a row closed the panel")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("clicking a row did not restore its build: %v", err)
	}
	if len(kbPanel.rows) != 1 {
		t.Fatalf("rows = %d after a restore, want the one build still kept", len(kbPanel.rows))
	}

	handleCoreBuildsMouse(2, 2)
	if kbPanel.open {
		t.Fatal("a click outside the panel did not close it")
	}
}

// TestCoreBuildsInstalledListShowsCoresAndFilters is the page's new first
// list: every installed core with its system, narrowed by family, with Y
// jumping to a core's own kept builds and B coming back.
func TestCoreBuildsInstalledListShowsCoresAndFilters(t *testing.T) {
	coreBuildsFixture(t,
		"mgba_libretro.so",
		"pcsx_rearmed_libretro.so",
		"snes9x_libretro.so",
		"mgba_libretro.so.jukahub-20260102-120000.bak",
	)
	openCoreBuildsPanel(true)
	if kbPanel.tab != coreBuildsTabCores {
		t.Fatalf("tab = %d, want the Installed list when cores are installed", kbPanel.tab)
	}
	if len(kbPanel.cores) != 3 {
		t.Fatalf("cores = %+v, want three", kbPanel.cores)
	}
	first := kbPanel.cores[0]
	if first.Core != "mgba" || first.System != "Game Boy Advance" || first.Family != emuFamilyNintendo || first.Kept != 1 {
		t.Fatalf("first core = %+v, want mGBA (GBA, Nintendo, 1 kept)", first)
	}
	if got := coreBuildsFamilies(); len(got) != 2 || got[0] != emuFamilyNintendo || got[1] != emuFamilyPlayStation {
		t.Fatalf("families = %v, want Nintendo and PlayStation", got)
	}
	coreBuildsShiftFilter(1)
	if kbPanel.filter != emuFamilyNintendo || len(coreBuildsVisibleCores()) != 2 {
		t.Fatalf("filter %q shows %d cores, want the two Nintendo ones", kbPanel.filter, len(coreBuildsVisibleCores()))
	}
	coreBuildsShiftFilter(1)
	if kbPanel.filter != emuFamilyPlayStation || len(coreBuildsVisibleCores()) != 1 {
		t.Fatalf("filter %q shows %d cores, want PCSX-ReARMed alone", kbPanel.filter, len(coreBuildsVisibleCores()))
	}
	coreBuildsShiftFilter(1)
	if kbPanel.filter != "" || len(coreBuildsVisibleCores()) != 3 {
		t.Fatalf("filter %q after a full turn, want All", kbPanel.filter)
	}

	// Y on mGBA opens its kept builds; B returns to the Installed list.
	kbPanel.cursor = 0
	coreBuildsAlt()
	if backup, ok := coreBuildsSelected(); kbPanel.tab != coreBuildsTabKept || !ok || backup.Core != "mgba" {
		t.Fatalf("Y on mGBA: tab %d, selected %+v (%v); want mGBA's kept build", kbPanel.tab, backup, ok)
	}
	coreBuildsBack()
	if !kbPanel.open || kbPanel.tab != coreBuildsTabCores {
		t.Fatalf("B on the Kept list: open=%v tab=%d, want the Installed list again", kbPanel.open, kbPanel.tab)
	}

	renderer := probeRenderer(t)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderCoreBuildsPanel(renderer, layoutTestConfig(t))
	if len(coreBuildsRowHits) != 3 {
		t.Fatalf("row hits = %d, want one per installed core", len(coreBuildsRowHits))
	}
	if len(coreBuildsChipHits) < 2 {
		t.Fatalf("chip hits = %d, want the two list tabs at least", len(coreBuildsChipHits))
	}
}

// TestCoreBuildsDateComesFromThePool: a core activated from the pool is dated
// by the nightly it came from, not by the day it was copied.
func TestCoreBuildsDateComesFromThePool(t *testing.T) {
	root := t.TempDir()
	cores := filepath.Join(root, "cores")
	pool := filepath.Join(root, "pool")
	writeCompatTestFile(t, filepath.Join(cores, "mgba_libretro.so"), "nightly build bytes")
	writeCompatTestFile(t, filepath.Join(pool, "mgba", "2026-10-24", "mgba_libretro.so"), "nightly build bytes")
	writeCompatTestFile(t, filepath.Join(pool, "mgba", "2026-10-30", "mgba_libretro.so"), "a different, longer build")
	writeCompatTestFile(t, filepath.Join(cores, "snes9x_libretro.so"), "hand-copied core")
	setCompatTestDirs(t, filepath.Join(root, "perf"), nil, []string{cores}, pool)

	got, total := coreBuildsLoadCores(nil)
	if len(got) != 2 || total <= 0 {
		t.Fatalf("cores = %+v (total %d)", got, total)
	}
	for _, c := range got {
		switch c.Core {
		case "mgba":
			if !c.Built || c.Date != "2026-10-24" {
				t.Errorf("mGBA dated %q (built=%v), want the 2026-10-24 nightly", c.Date, c.Built)
			}
		case "snes9x":
			if c.Built || c.Date == "" {
				t.Errorf("Snes9x dated %q (built=%v), want its install day", c.Date, c.Built)
			}
		}
	}
	if meta := coreBuildsCoreMeta(got[0]); !strings.Contains(meta, "build 2026-10-24") || !strings.Contains(meta, "Game Boy Advance") {
		t.Errorf("mGBA detail = %q", meta)
	}
}

// TestCoreBuildsStatusReadsTheLastCheck: the pill says what the background
// check found, and why there is no answer when there is none.
func TestCoreBuildsStatusReadsTheLastCheck(t *testing.T) {
	emuUpdateState.mu.Lock()
	prevCores, prevCurrent, prevIndexed, prevAt := emuUpdateState.cores, emuUpdateState.current, emuUpdateState.indexed, emuUpdateState.checkedAt
	emuUpdateState.cores = map[string]coreIndexEntry{"mgba": {Core: "mgba", Date: "2026-11-07"}}
	emuUpdateState.current = map[string]bool{"snes9x": true}
	emuUpdateState.indexed = map[string]coreIndexEntry{"mgba": {Core: "mgba"}, "snes9x": {Core: "snes9x"}}
	emuUpdateState.checkedAt = time.Now()
	emuUpdateState.mu.Unlock()
	t.Cleanup(func() {
		emuUpdateState.mu.Lock()
		emuUpdateState.cores, emuUpdateState.current, emuUpdateState.indexed, emuUpdateState.checkedAt = prevCores, prevCurrent, prevIndexed, prevAt
		emuUpdateState.mu.Unlock()
	})
	cases := []struct {
		core string
		want string
	}{
		{"mgba", "Update · 2026-11-07"},
		{"snes9x", "Up to date"},
		{"custom_core", "Not published"},
	}
	for _, tc := range cases {
		if got, _ := coreBuildsCoreStatus(coreBuildsCore{Core: tc.core, Name: tc.core}, false, false); got != tc.want {
			t.Errorf("%s status = %q, want %q", tc.core, got, tc.want)
		}
	}
	if got, _ := coreBuildsCoreStatus(coreBuildsCore{Core: "custom_core"}, true, false); got != "Checking…" {
		t.Errorf("status while checking = %q", got)
	}
	if !emuTileUpdateKnown(emuEntry{Name: "GBA", Status: emuAvailable, Launch: "/emus/GBA/launch.sh"}) {
		t.Error("the GBA tile does not show mGBA's known update")
	}
}
