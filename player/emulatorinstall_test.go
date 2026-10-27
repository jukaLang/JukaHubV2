package main

// emulatorinstall_test.go — the Emulators page installs what it used to only
// report. Four rules matter and are pinned here: a tile resolves to the core
// that runs its system, an absent core is an install while a different build is
// an update, the binary that was running is kept when it is replaced, and a
// download that does not match the index's CRC never reaches the card.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withCoreInstallsAllowed drives the handheld install path on any host.
func withCoreInstallsAllowed(t *testing.T, allowed bool) {
	t.Helper()
	prev := emuCoreInstallPlatform
	emuCoreInstallPlatform = func() bool { return allowed }
	t.Cleanup(func() {
		emuCoreInstallPlatform = prev
		invalidateEmulatorCache()
	})
	invalidateEmulatorCache()
}

// installHistoryFixture builds a temp card: a RetroArch cores folder with the
// named cores in it, an empty pool, and the data folder the pool lives under.
func installHistoryFixture(t *testing.T, cores map[string]string) (coresDir, poolDir string) {
	t.Helper()
	root := t.TempDir()
	coresDir = filepath.Join(root, "retroarch", "cores")
	poolDir = filepath.Join(root, "data", "performance", "cores")
	if err := os.MkdirAll(coresDir, 0o755); err != nil {
		t.Fatalf("mkdir cores: %v", err)
	}
	for name, body := range cores {
		if err := os.WriteFile(filepath.Join(coresDir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	setCompatTestDirs(t, filepath.Join(root, "data", "performance"), nil, []string{coresDir}, poolDir)
	return coresDir, poolDir
}

func TestEmuCoreForEntryResolvesSystemsAndCores(t *testing.T) {
	cases := []struct {
		entry emuEntry
		want  string
	}{
		{emuEntry{Name: "PSP"}, "ppsspp"},
		{emuEntry{Name: "GBA"}, "mgba"},
		{emuEntry{Name: "PS"}, "pcsx_rearmed"},
		{emuEntry{Name: "N64"}, "mupen64plus_next"},
		{emuEntry{Name: "DC"}, "flycast"},
		// An installed core row names its own core, whatever it is called.
		{emuEntry{Name: "mgba", Core: "mgba"}, "mgba"},
		{emuEntry{Name: "Custom Build", Core: "mame2003_plus"}, "mame2003_plus"},
		// Pinned rows are JukaHub's tools, not systems.
		{emuEntry{Name: "Smart Pro Compatibility", Pinned: true}, ""},
		// A folder nobody knows a core for offers no install.
		{emuEntry{Name: "Banana"}, ""},
	}
	for _, tc := range cases {
		if got := emuCoreForEntry(tc.entry); got != tc.want {
			t.Errorf("emuCoreForEntry(%+v) = %q, want %q", tc.entry, got, tc.want)
		}
	}
}

func TestPlanEmuInstallReportsNewUpdateAndCurrent(t *testing.T) {
	coresDir, _ := installHistoryFixture(t, map[string]string{
		"mgba_libretro.so":     "old mgba build",
		"snes9x_libretro.so":   "current snes9x build",
		"gambatte_libretro.so": "orphan build the index dropped",
	})
	_ = coresDir
	inv := scanCoreInventory()
	if len(inv.Active) != 3 {
		t.Fatalf("inventory has %d active cores, want 3 (%+v)", len(inv.Active), inv.Active)
	}

	index := []coreIndexEntry{
		{Core: "mgba", Date: "2026-10-24", CRC32: crcOf([]byte("new mgba build")), File: "mgba_libretro.so.zip"},
		{Core: "snes9x", Date: "2026-10-24", CRC32: crcOf([]byte("current snes9x build")), File: "snes9x_libretro.so.zip"},
		{Core: "ppsspp", Date: "2026-10-24", CRC32: crcOf([]byte("ppsspp build")), File: "ppsspp_libretro.so.zip"},
	}

	cases := []struct {
		name   string
		entry  emuEntry
		action emuInstallAction
	}{
		{"stale install is an update", emuEntry{Name: "GBA"}, emuInstallUpdate},
		{"matching build is current", emuEntry{Name: "SNES"}, emuInstallCurrent},
		{"absent core is an install", emuEntry{Name: "PSP"}, emuInstallNew},
		{"core the index dropped is not offered", emuEntry{Name: "GB"}, emuInstallNone},
		{"system with no known core is not offered", emuEntry{Name: "Banana"}, emuInstallNone},
		{"pinned tool row is not offered", emuEntry{Name: "Smart Pro Compatibility", Pinned: true}, emuInstallNone},
	}
	for _, tc := range cases {
		plan := planEmuInstall(tc.entry, inv, index)
		if plan.Action != tc.action {
			t.Errorf("%s: action = %v (%q), want %v", tc.name, plan.Action, plan.Reason, tc.action)
		}
		if plan.Reason == "" {
			t.Errorf("%s: plan has no reason to show", tc.name)
		}
	}
}

func TestEmuInstallPlanNamesTheBuildAndTheCore(t *testing.T) {
	installHistoryFixture(t, map[string]string{"mgba_libretro.so": "old build"})
	inv := scanCoreInventory()
	index := []coreIndexEntry{
		{Core: "mgba", Date: "2026-10-24", CRC32: crcOf([]byte("new build")), File: "mgba_libretro.so.zip"},
	}
	plan := planEmuInstall(emuEntry{Name: "GBA"}, inv, index)
	if plan.Core != "mgba" || plan.Action != emuInstallUpdate {
		t.Fatalf("plan = %+v, want an update for mgba", plan)
	}
	if plan.Index.File != "mgba_libretro.so.zip" || plan.Index.CRC32 != crcOf([]byte("new build")) {
		t.Fatalf("plan does not carry the index line: %+v", plan.Index)
	}
	if !strings.Contains(plan.Reason, "2026-10-24") || !strings.Contains(plan.Reason, "mGBA") {
		t.Fatalf("reason %q does not name the build or the emulator", plan.Reason)
	}
}

// coreInstallServer serves one core archive and returns the client plus a
// cleanup that restores the update base URL.
func coreInstallServer(t *testing.T, core string, payload []byte) *http.Client {
	t.Helper()
	archive := zestCoreZip(t, core+"_libretro.so", string(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	allowUpdateTestServer(t, server.URL)
	prevBase := coreUpdateBaseURL
	coreUpdateBaseURL = server.URL + "/"
	t.Cleanup(func() {
		coreUpdateBaseURL = prevBase
		server.Close()
	})
	return server.Client()
}

func TestInstallEmuCoreAddsACoreThatWasNotThere(t *testing.T) {
	coresDir, poolDir := installHistoryFixture(t, nil)
	payload := []byte("freshly built ppsspp")
	client := coreInstallServer(t, "ppsspp", payload)
	index := []coreIndexEntry{
		{Core: "ppsspp", Date: "2026-10-24", CRC32: crcOf(payload), File: "ppsspp_libretro.so.zip"},
	}

	plan := planEmuInstall(emuEntry{Name: "PSP"}, scanCoreInventory(), index)
	if plan.Action != emuInstallNew {
		t.Fatalf("plan = %v (%q), want an install", plan.Action, plan.Reason)
	}
	build, backup, err := applyEmuInstallPlan(context.Background(), client, plan, poolDir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if backup != "" {
		t.Fatalf("a first install kept a backup %q; nothing was replaced", backup)
	}
	active := filepath.Join(coresDir, "ppsspp_libretro.so")
	data, err := os.ReadFile(active)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("installed core = %q, %v", data, err)
	}
	if !build.Pool || build.Path == "" {
		t.Fatalf("build = %+v, want the pool copy", build)
	}
	// The pool keeps where the build came from, so a card can be diffed later.
	if _, err := os.Stat(filepath.Join(poolDir, "ppsspp", "2026-10-24", "source.json")); err != nil {
		t.Fatalf("provenance file missing: %v", err)
	}
}

func TestInstallEmuCoreKeepsTheBuildItReplaced(t *testing.T) {
	oldBody := "the build that was running"
	coresDir, poolDir := installHistoryFixture(t, map[string]string{"mgba_libretro.so": oldBody})
	payload := []byte("the newer mgba build")
	client := coreInstallServer(t, "mgba", payload)
	index := []coreIndexEntry{
		{Core: "mgba", Date: "2026-10-24", CRC32: crcOf(payload), File: "mgba_libretro.so.zip"},
	}

	plan := planEmuInstall(emuEntry{Name: "GBA"}, scanCoreInventory(), index)
	if plan.Action != emuInstallUpdate {
		t.Fatalf("plan = %v (%q), want an update", plan.Action, plan.Reason)
	}
	_, backup, err := applyEmuInstallPlan(context.Background(), client, plan, poolDir)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if backup == "" || !strings.HasSuffix(backup, ".bak") {
		t.Fatalf("backup = %q, want a .bak beside the core", backup)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != oldBody {
		t.Fatalf("backup holds %q, %v; want the replaced build", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "mgba_libretro.so")); err != nil || string(data) != string(payload) {
		t.Fatalf("active core = %q, %v; want the new build", data, err)
	}
}

func TestInstallEmuCoreRefusesAMismatchedBuild(t *testing.T) {
	oldBody := "the build that was running"
	coresDir, poolDir := installHistoryFixture(t, map[string]string{"mgba_libretro.so": oldBody})
	client := coreInstallServer(t, "mgba", []byte("tampered payload"))
	index := []coreIndexEntry{
		{Core: "mgba", Date: "2026-10-24", CRC32: "00000000", File: "mgba_libretro.so.zip"},
	}
	plan := planEmuInstall(emuEntry{Name: "GBA"}, scanCoreInventory(), index)
	if plan.Action != emuInstallUpdate {
		t.Fatalf("plan = %v, want an update", plan.Action)
	}
	if _, _, err := applyEmuInstallPlan(context.Background(), client, plan, poolDir); err == nil {
		t.Fatal("a core whose bytes do not match the index was installed")
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "mgba_libretro.so")); err != nil || string(data) != oldBody {
		t.Fatalf("active core changed to %q, %v; a rejected download must not touch it", data, err)
	}
	backups, _ := filepath.Glob(filepath.Join(coresDir, "*.bak"))
	if len(backups) != 0 {
		t.Fatalf("a rejected download left backups: %v", backups)
	}
}

func TestApplyEmuInstallRefusesACurrentPlan(t *testing.T) {
	body := "already current"
	coresDir, poolDir := installHistoryFixture(t, map[string]string{"snes9x_libretro.so": body})
	client := coreInstallServer(t, "snes9x", []byte(body))
	index := []coreIndexEntry{
		{Core: "snes9x", Date: "2026-10-24", CRC32: crcOf([]byte(body)), File: "snes9x_libretro.so.zip"},
	}
	plan := planEmuInstall(emuEntry{Name: "SNES"}, scanCoreInventory(), index)
	if plan.Action != emuInstallCurrent {
		t.Fatalf("plan = %v (%q), want current", plan.Action, plan.Reason)
	}
	if _, _, err := applyEmuInstallPlan(context.Background(), client, plan, poolDir); err == nil {
		t.Fatal("a current core was downloaded again")
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "snes9x_libretro.so")); err != nil || string(data) != body {
		t.Fatalf("current core changed to %q, %v", data, err)
	}
}

// TestACoreCoversItsRomFolder is the wiring the install depended on: once the
// core is on the card, the Roms/ folder for that system must stop being
// reported as having no emulator, or installing would look like nothing
// happened.
func TestACoreCoversItsRomFolder(t *testing.T) {
	root := t.TempDir()
	emus := filepath.Join(root, "Emus")
	roms := filepath.Join(root, "Roms")
	cores := filepath.Join(root, "cores")
	for _, dir := range []string{emus, filepath.Join(roms, "PSP"), cores} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	without := deviceEmulatorEntries([]string{emus}, roms, cores)
	if !hasEmuEntry(without, "PSP") {
		t.Fatalf("Roms/PSP with no emulator was not reported: %+v", without)
	}

	if err := os.WriteFile(filepath.Join(cores, "ppsspp_libretro.so"), []byte("core"), 0o755); err != nil {
		t.Fatalf("write core: %v", err)
	}
	with := deviceEmulatorEntries([]string{emus}, roms, cores)
	if hasEmuEntry(with, "PSP") {
		t.Fatalf("Roms/PSP is still called unplayable next to the ppsspp core: %+v", with)
	}
	if !hasEmuEntry(with, "ppsspp") {
		t.Fatalf("the installed core is not listed: %+v", with)
	}
}

func hasEmuEntry(entries []emuEntry, name string) bool {
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return true
		}
	}
	return false
}

// TestEmulatorFooterHintsMatchTheHandlers keeps the page honest: A installs on a
// tile the page can fix, A launches the one that is there, and X only appears
// where a core exists to update. A hint for a press that does nothing is worse
// than no hint.
func TestEmulatorFooterHintsMatchTheHandlers(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	coresDir, _ := installHistoryFixture(t, nil)

	labels := func(e emuEntry) map[string]string {
		out := map[string]string{}
		for _, hint := range emuFooterHints(e, true) {
			out[hint.Button] = hint.Label
		}
		return out
	}

	// An empty card: nothing to update, so no X and no L1 anywhere.
	if got := labels(emuEntry{Name: "GBA", Status: emuAvailable, Launch: "/emus/GBA/launch.sh"})["X"]; got != "" {
		t.Fatalf("GBA tile offers X %q with no core on the card", got)
	}
	if got := labels(emuEntry{Name: "Banana", Status: emuMissing})["L1"]; got != "" {
		t.Fatalf("L1 Update all is offered with no installed core: %q", got)
	}

	// Installing one core must change both lines: the tile that owns it gains
	// Update, and the page gains the bulk update.
	if err := os.WriteFile(filepath.Join(coresDir, "mgba_libretro.so"), []byte("installed build"), 0o755); err != nil {
		t.Fatalf("write core: %v", err)
	}
	invalidateEmulatorCache()

	missing := labels(emuEntry{Name: "PSP", Status: emuMissing})
	if missing["A"] != "Install" {
		t.Fatalf("missing PSP tile offers A %q, want Install", missing["A"])
	}
	if got, ok := missing["X"]; ok {
		t.Fatalf("missing PSP tile offers X %q, but there is no ppsspp core to update", got)
	}

	installed := labels(emuEntry{Name: "GBA", Status: emuAvailable, Launch: "/emus/GBA/launch.sh"})
	if installed["A"] != "Launch" {
		t.Fatalf("installed GBA tile offers A %q, want Launch", installed["A"])
	}
	if installed["X"] != "Update" {
		t.Fatalf("installed GBA tile offers X %q, want Update", installed["X"])
	}

	tool := labels(emuEntry{Name: "Smart Pro Compatibility", Status: emuAvailable, Pinned: true})
	if tool["A"] != "Open" {
		t.Fatalf("tool row offers A %q, want Open", tool["A"])
	}

	// A core row is already the install: it offers the update, not a second
	// install of the same file.
	coreRow := labels(emuEntry{Name: "mgba", Status: emuAvailable, Core: "mgba"})
	if got, ok := coreRow["A"]; ok {
		t.Fatalf("core row offers A %q, but A only explains where a core runs", got)
	}
	if coreRow["X"] != "Update" {
		t.Fatalf("core row offers X %q, want Update", coreRow["X"])
	}

	unknown := labels(emuEntry{Name: "Banana", Status: emuMissing})
	if got, ok := unknown["A"]; ok {
		t.Fatalf("a system with no known core offers A %q", got)
	}
	if unknown["B"] != "Back" || unknown["D-Pad"] != "Navigate" {
		t.Fatalf("every tile needs Back and D-Pad: %+v", unknown)
	}
	// L1 is a page-level key, so it is advertised on every tile once a core is
	// installed, not only on the tile that owns it.
	if unknown["L1"] != "Update all" {
		t.Fatalf("L1 hint = %q, want Update all when a core is installed", unknown["L1"])
	}
}

// TestEmulatorHintLineFitsBothCanvases keeps the page from drawing a key it
// cannot show. The hint line carries up to six groups once a core has a kept
// build, and the Brick's body is 320px narrower than the Smart Pro's: a line
// that does not fit would be ellipsized on screen, cutting the B hint that every
// page has to offer. Measured with the widest fixture, the line leaves 84px of
// headroom on the Brick.
func TestEmulatorHintLineFitsBothCanvases(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "installed build"})
	// A kept build adds R1, so this fixture is the widest line the page draws.
	if err := os.WriteFile(filepath.Join(coresDir, "mgba_libretro.so.jukahub-20261020-101010.bak"),
		[]byte("previous build"), 0o755); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	invalidateEmulatorCache()
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// Render tests need SDL and TTF up before a font can be measured.
	newProbeFrame(t)

	// The longest line the page can produce: A, X, R1, L1, Y and B are all
	// offered.
	line := hintLine(emuFooterHints(emuEntry{
		Name: "GBA", Status: emuAvailable, Launch: "/emus/GBA/launch.sh",
	}, true)...)
	if !strings.Contains(line, "L1 Update all") || !strings.Contains(line, "B Back") ||
		!strings.Contains(line, "R1 ") {
		t.Fatalf("the fixture does not produce the longest hint line: %q", line)
	}

	for _, canvas := range []struct {
		name string
		w, h int32
	}{
		{"Smart Pro", 1280, 720},
		{"Brick", uiCanvasCompactW, uiCanvasCompactH},
	} {
		withScreen(t, canvas.w, canvas.h)
		font, _ := getCachedFont(config, "small")
		if font == nil {
			t.Fatalf("%s: no small font", canvas.name)
		}
		body := emuGridView().body
		if width, _, _ := font.SizeUTF8(line); int32(width) > body.W {
			t.Errorf("%s: the hint line is %dpx wide in a %dpx body (%q)", canvas.name, width, body.W, line)
		}
	}
}

// TestCoreInstallsAreHandheldOnly pins the shipped policy: the buildbot serves
// aarch64 shared objects, so a Windows build must not write them into a
// RetroArch folder that loads .dll files.
func TestCoreInstallsAreHandheldOnly(t *testing.T) {
	if emuCoreInstallPlatform() == IsWindows() {
		t.Fatalf("emuCoreInstallPlatform() = %v on windows=%v", emuCoreInstallPlatform(), IsWindows())
	}
	withCoreInstallsAllowed(t, false)
	if got := emuInstallHintLabel(emuEntry{Name: "PSP", Status: emuMissing}); got != "" {
		t.Fatalf("install hint = %q while core installs are unsupported", got)
	}
	if got := emuUpdateHintLabel(emuEntry{Name: "GBA", Status: emuAvailable}); got != "" {
		t.Fatalf("update hint = %q while core installs are unsupported", got)
	}
}

// ── Rolling a core back ─────────────────────────────────────────────────────

// TestParseCoreBackupName is the guard on what a rollback may pick up. The
// cores folder also holds `.part` downloads, `.jukahub.tmp` staging files and
// backups other tools wrote; restoring any of those over the active core would
// brick the emulator, so only this build's own naming parses.
func TestParseCoreBackupName(t *testing.T) {
	cases := []struct {
		name   string
		core   string
		target string
		stamp  string
		ok     bool
	}{
		{"mgba_libretro.so.jukahub-20261024-101010.bak", "mgba", "mgba_libretro.so", "2026-10-24 10:10:10", true},
		// A second replacement inside the same second gets a counter, and it
		// still has to parse or the build it keeps is never offered.
		{"mgba_libretro.so.jukahub-20261024-101010-2.bak", "mgba", "mgba_libretro.so", "2026-10-24 10:10:10", true},
		{"mgba_libretro.dll.jukahub-20261024-101010.bak", "mgba", "mgba_libretro.dll", "2026-10-24 10:10:10", true},
		// Not this build's backups.
		{"mgba_libretro.so", "", "", "", false},
		{"mgba_libretro.so.jukahub.tmp", "", "", "", false},
		{"mgba_libretro.so.part", "", "", "", false},
		{"mgba_libretro.so.bak", "", "", "", false},
		{"mgba_libretro.so.jukahub-notastamp.bak", "", "", "", false},
		{"backup.bak", "", "", "", false},
		{".jukahub-20261024-101010.bak", "", "", "", false},
	}
	for _, tc := range cases {
		core, target, stamp, ok := parseCoreBackupName(tc.name)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if core != tc.core || target != tc.target {
			t.Errorf("%s: parsed as core %q target %q, want %q / %q", tc.name, core, target, tc.core, tc.target)
		}
		if got := stamp.Format("2006-01-02 15:04:05"); got != tc.stamp {
			t.Errorf("%s: stamp = %q, want %q", tc.name, got, tc.stamp)
		}
	}
}

func readRollbackFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestRollbackRestoresTheKeptBuildAndKeepsTheOneItReplaced is the whole point of
// the press: an update that runs worse is undone on the device, and undoing the
// undo puts the newer build back rather than throwing it away.
func TestRollbackRestoresTheKeptBuildAndKeepsTheOneItReplaced(t *testing.T) {
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "nightly build"})
	old := filepath.Join(coresDir, "mgba_libretro.so.jukahub-20261020-101010.bak")
	if err := os.WriteFile(old, []byte("stable build"), 0o755); err != nil {
		t.Fatalf("write old build: %v", err)
	}
	invalidateEmulatorCache()
	t.Cleanup(invalidateEmulatorCache)

	backup, ok := emuNewestCoreBackup("mgba")
	if !ok {
		t.Fatal("the kept build was not found beside the core")
	}
	if got := backup.Stamp.Format("2006-01-02"); got != "2026-10-20" {
		t.Fatalf("newest backup stamp = %q, want 2026-10-20", got)
	}

	build, err := rollbackCoreBuild(backup)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !build.Active || build.Core != "mgba" {
		t.Fatalf("rollback reported %+v, want the active mgba core", build)
	}
	active := filepath.Join(coresDir, "mgba_libretro.so")
	if got := readRollbackFile(t, active); got != "stable build" {
		t.Fatalf("active core after rollback = %q, want the kept build", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the restored backup is still on the card (%v): it would offer the same build again", err)
	}

	// The build that was just replaced is kept, so a second press reverses it.
	invalidateEmulatorCache()
	undone, ok := emuNewestCoreBackup("mgba")
	if !ok {
		t.Fatal("the replaced build was not kept: the rollback cannot be undone")
	}
	if got := readRollbackFile(t, undone.Path); got != "nightly build" {
		t.Fatalf("kept build after rollback = %q, want the nightly build that was replaced", got)
	}
	if _, err := rollbackCoreBuild(undone); err != nil {
		t.Fatalf("rollback back: %v", err)
	}
	if got := readRollbackFile(t, active); got != "nightly build" {
		t.Fatalf("active core after the second press = %q, want the nightly build", got)
	}
}

// TestRollbackRefusesAFileOutsideTheCoresFolder covers the scan going stale: the
// path is checked again at the write, and a backup that is not in a RetroArch
// cores folder is never installed over a core.
func TestRollbackRefusesAFileOutsideTheCoresFolder(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	installHistoryFixture(t, map[string]string{"mgba_libretro.so": "nightly build"})
	outside := t.TempDir()
	stray := filepath.Join(outside, "mgba_libretro.so.jukahub-20261020-101010.bak")
	if err := os.WriteFile(stray, []byte("unrelated file"), 0o755); err != nil {
		t.Fatalf("write stray: %v", err)
	}
	_, err := rollbackCoreBuild(coreBackup{
		Core:   "mgba",
		Path:   stray,
		Target: filepath.Join(outside, "mgba_libretro.so"),
	})
	if err == nil {
		t.Fatal("a backup outside RetroArch's cores folder was installed")
	}
	if !strings.Contains(err.Error(), "cores folder") {
		t.Fatalf("error = %v, want it to name the cores folder", err)
	}
}

// TestEmuRollbackHintOnlyAppearsWithAKeptBuild keeps the footer honest, and
// pins the platform rule: a rollback is a file the card already has, so it is
// offered even where an install from the buildbot is not.
func TestEmuRollbackHintOnlyAppearsWithAKeptBuild(t *testing.T) {
	withCoreInstallsAllowed(t, false)
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "nightly build"})
	tile := emuEntry{Name: "GBA", Status: emuAvailable, Launch: "/emus/GBA/launch.sh"}

	labels := func() map[string]string {
		out := map[string]string{}
		for _, hint := range emuFooterHints(tile, true) {
			out[hint.Button] = hint.Label
		}
		return out
	}

	if got := emuRollbackHintLabel(tile); got != "" {
		t.Fatalf("rollback hint = %q with no kept build", got)
	}
	if _, ok := labels()["R1"]; ok {
		t.Fatalf("the footer offers R1 with no kept build: %v", labels())
	}

	if err := os.WriteFile(filepath.Join(coresDir, "mgba_libretro.so.jukahub-20261020-101010.bak"),
		[]byte("stable build"), 0o755); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	invalidateEmulatorCache()
	t.Cleanup(invalidateEmulatorCache)

	if got := emuRollbackHintLabel(tile); got != "Roll back" {
		t.Fatalf("rollback hint = %q with a kept build", got)
	}
	if got := labels()["R1"]; got != "Roll back" {
		t.Fatalf("footer R1 = %q, want Roll back", got)
	}
	// Only the core that has a backup offers it.
	if got := emuRollbackHintLabel(emuEntry{Name: "PSP", Status: emuMissing}); got != "" {
		t.Fatalf("PSP offers a rollback with no ppsspp backup: %q", got)
	}
	// A tool row is not an emulator core.
	if got := emuRollbackHintLabel(emuEntry{Name: "Smart Pro Compatibility", Pinned: true}); got != "" {
		t.Fatalf("a pinned tool row offers a rollback: %q", got)
	}
}
