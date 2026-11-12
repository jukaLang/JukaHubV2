package main

// emulatorautoupdate_test.go — the handheld brings its emulator cores forward
// without a press. Three things have to hold for that to be safe enough to run
// unattended: it only runs where cores can be installed at all, a failed check
// is retried rather than standing down for a day, and the builds it replaces
// are kept to a fixed number so the card cannot fill with them. The other
// updaters of the same pass (emuautoupdate_runtimes.go) keep their records
// across a restart, back off and respect GitHub's refusals, and never install
// a build again that the user rolled back from.

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoreAutoUpdateDue(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		last string
		want bool
	}{
		{"never checked", "", true},
		{"garbled stamp", "not a time", true},
		{"just checked", now.Add(-time.Hour).Format(time.RFC3339), false},
		{"checked yesterday", now.Add(-25 * time.Hour).Format(time.RFC3339), true},
		{"future stamp (clock rolled back)", now.Add(48 * time.Hour).Format(time.RFC3339), true},
		{"pre-2020 stamp (no RTC battery)", "2019-01-01T00:00:00Z", true},
	}
	for _, tc := range cases {
		if got := coreAutoUpdateDue(tc.last, now); got != tc.want {
			t.Errorf("%s: due(%q) = %v, want %v", tc.name, tc.last, got, tc.want)
		}
	}
}

func TestCoreAutoUpdateAllowed(t *testing.T) {
	if !coreAutoUpdateAllowed(false, true, true, true) {
		t.Fatal("the handheld, online, with core installs enabled was refused")
	}
	for _, tc := range []struct {
		name                          string
		disabled, supported, hh, onln bool
	}{
		{"opted out by the environment", true, true, true, true},
		{"desktop build", false, false, true, true},
		{"not a handheld", false, true, false, true},
		{"offline", false, true, true, false},
	} {
		if coreAutoUpdateAllowed(tc.disabled, tc.supported, tc.hh, tc.onln) {
			t.Errorf("%s: the automatic pass was allowed", tc.name)
		}
	}
}

// coreIndexServer serves the build index and one core archive from one loopback
// server, the way the buildbot does.
func coreIndexServer(t *testing.T, index string, archives map[string][]byte) *http.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".index-extended") {
			_, _ = w.Write([]byte(index))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if body, ok := archives[name]; ok {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	allowUpdateTestServer(t, server.URL)
	prevIndex, prevBase := coreUpdateIndexURL, coreUpdateBaseURL
	coreUpdateIndexURL = server.URL + "/.index-extended"
	coreUpdateBaseURL = server.URL + "/"
	t.Cleanup(func() {
		coreUpdateIndexURL, coreUpdateBaseURL = prevIndex, prevBase
		server.Close()
	})
	return server.Client()
}

// TestRunInstalledCoreUpdatesUpdatesWithoutAPress is the whole point: an
// installed core whose bytes differ from the index is downloaded and activated,
// with the build it replaced kept beside it.
func TestRunInstalledCoreUpdatesUpdatesWithoutAPress(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	oldBody := "the build that was running"
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": oldBody})
	payload := []byte("the newer mgba build")
	archive := zestCoreZip(t, "mgba_libretro.so", string(payload))
	index := fmt.Sprintf("2026-10-24 %s mgba_libretro.so.zip\n", crcOf(payload))
	client := coreIndexServer(t, index, map[string][]byte{"mgba_libretro.so.zip": archive})

	var phases []string
	report := runInstalledCoreUpdates(context.Background(), client, func(phase, detail string) {
		phases = append(phases, phase)
	})

	if !report.Checked {
		t.Fatalf("report says the index was never read: %+v", report)
	}
	if len(report.Updated) != 1 || report.Updated[0] != "mGBA" {
		t.Fatalf("updated = %v, want [mGBA]", report.Updated)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("errors = %v", report.Errors)
	}
	active := filepath.Join(coresDir, "mgba_libretro.so")
	if data, err := os.ReadFile(active); err != nil || string(data) != string(payload) {
		t.Fatalf("active core = %q, %v; want the new build", data, err)
	}
	backups, _ := filepath.Glob(filepath.Join(coresDir, "mgba_libretro.so.jukahub-*.bak"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want the replaced build kept", backups)
	}
	if data, err := os.ReadFile(backups[0]); err != nil || string(data) != oldBody {
		t.Fatalf("backup holds %q, %v; want the replaced build", data, err)
	}
	// The bar has to be driven, or a background download would be invisible.
	sawDownload := false
	for _, phase := range phases {
		if phase == installPhaseDownload {
			sawDownload = true
		}
	}
	if !sawDownload {
		t.Fatalf("phases = %v, want a download phase for the bar", phases)
	}
}

// TestRunInstalledCoreUpdatesLeavesACurrentCardAlone: no download, no backup, no
// churn when every installed core is the build the index lists.
func TestRunInstalledCoreUpdatesLeavesACurrentCardAlone(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	body := "already the nightly"
	coresDir, _ := installHistoryFixture(t, map[string]string{"snes9x_libretro.so": body})
	index := fmt.Sprintf("2026-10-24 %s snes9x_libretro.so.zip\n", crcOf([]byte(body)))
	client := coreIndexServer(t, index, nil)

	report := runInstalledCoreUpdates(context.Background(), client, nil)
	if len(report.Updated) != 0 || len(report.Errors) != 0 {
		t.Fatalf("report = %+v, want an up-to-date card", report)
	}
	if report.Current != 1 {
		t.Fatalf("current = %d, want 1", report.Current)
	}
	backups, _ := filepath.Glob(filepath.Join(coresDir, "*.bak"))
	if len(backups) != 0 {
		t.Fatalf("a current core left backups: %v", backups)
	}
}

// TestRunInstalledCoreUpdatesReportsAnUnreachableIndex: Checked stays false so
// the caller does not stand down until tomorrow after a failed check.
func TestRunInstalledCoreUpdatesReportsAnUnreachableIndex(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "the build that was running"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	allowUpdateTestServer(t, server.URL)
	prevIndex := coreUpdateIndexURL
	coreUpdateIndexURL = server.URL + "/.index-extended"
	t.Cleanup(func() {
		coreUpdateIndexURL = prevIndex
		server.Close()
	})

	report := runInstalledCoreUpdates(context.Background(), server.Client(), nil)
	if report.Checked {
		t.Fatal("a check that never read the index reported Checked")
	}
	if len(report.Errors) == 0 {
		t.Fatal("a failed index fetch was not reported")
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "mgba_libretro.so")); err != nil || string(data) != "the build that was running" {
		t.Fatalf("the active core changed to %q, %v; a failed check must not touch it", data, err)
	}
}

// TestCoreBackupsStayAtFive drives seven updates of the same core and counts
// what is left beside it: the newest five builds, and nothing else.
func TestCoreBackupsStayAtFive(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	coresDir, poolDir := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "build 0"})
	for i := 1; i <= 7; i++ {
		body := fmt.Sprintf("build %d", i)
		archive := zestCoreZip(t, "mgba_libretro.so", body)
		client := coreIndexServer(t, fmt.Sprintf("2026-10-%02d %s mgba_libretro.so.zip\n", 20+i, crcOf([]byte(body))),
			map[string][]byte{"mgba_libretro.so.zip": archive})
		if report := runInstalledCoreUpdates(context.Background(), client, nil); len(report.Errors) != 0 {
			t.Fatalf("update %d: %v", i, report.Errors)
		}
		// The loop re-points the index/base URLs at a fresh server; the fixture
		// directories are what matter here.
	}

	backups, err := filepath.Glob(filepath.Join(coresDir, "mgba_libretro.so.jukahub-*.bak"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(backups) != maxCoreBackups {
		t.Fatalf("kept %d backups (%v), want %d", len(backups), backups, maxCoreBackups)
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "mgba_libretro.so")); err != nil || string(data) != "build 7" {
		t.Fatalf("active core = %q, %v; want the last update", data, err)
	}
	// The pool is the other half of the same growth: one dated folder per fetch.
	pooled, err := os.ReadDir(filepath.Join(poolDir, "mgba"))
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	if len(pooled) != maxCorePoolBuilds {
		t.Fatalf("the pool kept %d builds (%v), want %d", len(pooled), pooled, maxCorePoolBuilds)
	}
}

// TestPruneCorePoolNeverDropsTheBuildJustFetched: a build published with an
// older date than what the pool already holds still survives the prune.
func TestPruneCorePoolNeverDropsTheBuildJustFetched(t *testing.T) {
	dir := t.TempDir()
	for _, date := range []string{"2026-09-30", "2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04", "2026-10-05", "2026-10-06"} {
		if err := os.MkdirAll(filepath.Join(dir, "mgba", date), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	removed := pruneCorePool(dir, "mgba", "2026-09-30", maxCorePoolBuilds)
	if _, err := os.Stat(filepath.Join(dir, "mgba", "2026-09-30")); err != nil {
		t.Fatalf("the build just fetched was pruned: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %v, want only the oldest dated build", removed)
	}
}

// TestPruneCoreBackupsKeepsTheNewestAndOthersAlone covers the pruning rule on
// its own: only this core's backups are candidates, and only the newest five
// survive.
func TestPruneCoreBackupsKeepsTheNewestAndOthersAlone(t *testing.T) {
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "active"})
	target := filepath.Join(coresDir, "mgba_libretro.so")
	writeBackup := func(core, stamp string) string {
		path := filepath.Join(coresDir, core+"_libretro.so.jukahub-"+stamp+".bak")
		if err := os.WriteFile(path, []byte(stamp), 0o755); err != nil {
			t.Fatalf("write backup: %v", err)
		}
		return path
	}
	stamps := []string{
		"20260101-000001", "20260102-000002", "20260103-000003", "20260104-000004",
		"20260105-000005", "20260106-000006", "20260107-000007",
	}
	var paths []string
	for _, stamp := range stamps {
		paths = append(paths, writeBackup("mgba", stamp))
	}
	other := writeBackup("snes9x", "20250101-000001")
	foreign := filepath.Join(coresDir, "notes.bak")
	if err := os.WriteFile(foreign, []byte("not a core"), 0o644); err != nil {
		t.Fatalf("write foreign: %v", err)
	}

	removed := pruneCoreBackups(target, maxCoreBackups)
	if len(removed) != 2 {
		t.Fatalf("removed %v, want the two oldest", removed)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if i < 2 && err == nil {
			t.Errorf("backup %d (%s) survived pruning; it is older than the five kept", i, filepath.Base(path))
		}
		if i >= 2 && err != nil {
			t.Errorf("backup %d (%s) was pruned; it is one of the five kept", i, filepath.Base(path))
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("trimming mGBA's backups removed %s, another core's build", filepath.Base(other))
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("trimming removed %s, which is not a core backup", filepath.Base(foreign))
	}
}

// TestCoreAutoUpdateOverdueWaitsOnePollOnAFreshCard: a card that has never had
// a pass waits for a quiet moment, but only a poll's worth — the rule used to
// return false forever, so a device that was always in use on battery never got
// its first update.
func TestCoreAutoUpdateOverdueWaitsOnePollOnAFreshCard(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		last   string
		waited time.Duration
		want   bool
	}{
		{"fresh card, just started waiting", "", 0, false},
		{"fresh card, waited a poll", "", coreAutoUpdatePoll, true},
		{"clock before 2020, waited a poll", "2019-01-01T00:00:00Z", coreAutoUpdatePoll, true},
		{"recent stamp: waiting does not hurry it", now.Add(-25 * time.Hour).Format(time.RFC3339), 10 * time.Hour, false},
		{"three days without a pass", now.Add(-73 * time.Hour).Format(time.RFC3339), 0, true},
	}
	for _, c := range cases {
		if got := coreAutoUpdateOverdueFor(c.last, now, c.waited); got != c.want {
			t.Errorf("%s: overdue = %v, want %v", c.name, got, c.want)
		}
	}
	if coreAutoUpdateMoment(false, true, true, time.Hour, true) {
		t.Error("a busy device (an install, a playing video) was a good moment")
	}
	if !coreAutoUpdateMoment(false, false, false, 0, true) {
		t.Error("an overdue pass did not take a quiet moment")
	}
}

// Only the user's own pick carries the marker; JukaHub's start-up store and the
// migration do not, so the migration can tell the two apart.
func TestEmuStoreCoreUpdateModeMarksOnlyChoices(t *testing.T) {
	prev := emuCoreUpdateMode.Load()
	t.Cleanup(func() { emuCoreUpdateMode.Store(prev) })
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{coreUpdateModeSetting: "notify"}}}

	emuStoreCoreUpdateMode(cfg, coreUpdateInstall, false)
	if _, marked := cfg.Variables.Custom[coreUpdateModeChosenSetting]; marked {
		t.Fatal("an automatic store wrote the chosen marker")
	}
	if cfg.Variables.Custom[coreUpdateModeSetting] != "install" || cfg.Variables.Custom[emuAutoUpdateSetting] != true {
		t.Fatalf("custom = %v, want install with the old switch on", cfg.Variables.Custom)
	}
	if emuCoreUpdateModeNow() != coreUpdateInstall {
		t.Fatalf("the scheduler's mirror = %s", emuCoreUpdateModeNow().key())
	}

	if msg := coreUpdateModeChoose(cfg, "notify"); !strings.HasPrefix(msg, "Emulator updates: ") {
		t.Errorf("toast = %q", msg)
	}
	if cfg.Variables.Custom[coreUpdateModeChosenSetting] != true || cfg.Variables.Custom[emuAutoUpdateSetting] != false {
		t.Fatalf("custom = %v, want the marker and the old switch off", cfg.Variables.Custom)
	}
	if got := coreUpdateModeFor(cfg); got != coreUpdateNotify || coreUpdateModeNeedsMigration(cfg.Variables.Custom) {
		t.Fatalf("a chosen notify reads as %s (migrate %v)", got.key(), coreUpdateModeNeedsMigration(cfg.Variables.Custom))
	}
	if got := coreUpdateModeFor(nil); got != coreUpdateInstall {
		t.Errorf("no config = %s, want the default", got.key())
	}
}

// resetCoreRefusalsForTest drops the in-memory refusals so the next read
// comes from the file, and does so again when the test ends.
func resetCoreRefusalsForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		coreRefusals.mu.Lock()
		coreRefusals.m = nil
		coreRefusals.path = ""
		coreRefusals.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestCoreRefusalsAreRememberedByBuild(t *testing.T) {
	installHistoryFixture(t, nil)
	resetCoreRefusalsForTest(t)
	coreRefusalRecord("mgba", "AABBCCDD", "2026-11-09", coreCannotLoadReason+"needs GLIBC_2.34")
	if reason, ok := coreBuildRefused("mgba", "aabbccdd"); !ok || !strings.Contains(reason, "GLIBC_2.34") {
		t.Fatalf("refused = %q %v", reason, ok)
	}
	if _, ok := coreBuildRefused("mgba", "11111111"); ok {
		t.Fatal("another build of the same core was treated as refused")
	}
	// The file carries it across a restart.
	resetCoreRefusalsForTest(t)
	if _, ok := coreBuildRefused("mgba", "aabbccdd"); !ok {
		t.Fatal("the refusal did not survive a reload from core-refused.json")
	}
	coreRefusalForget("mgba", "11111111")
	if _, ok := coreBuildRefused("mgba", "aabbccdd"); !ok {
		t.Fatal("forgetting another build dropped this one")
	}
	coreRefusalForget("mgba", "aabbccdd")
	if _, ok := coreBuildRefused("mgba", "aabbccdd"); ok {
		t.Fatal("a build that installed after all is still refused")
	}
	err := &coreRefusedError{Core: "mgba", Build: "2026-11-09", Reason: coreCannotLoadReason + "needs GLIBC_2.34"}
	if got := err.Error(); got != "the 2026-11-09 build cannot load on this firmware: needs GLIBC_2.34" {
		t.Errorf("error = %q", got)
	}
}

// A build refused before is not downloaded again: a firmware older than the
// buildbot's image would otherwise fetch every stale core every day.
func TestRunInstalledCoreUpdatesSkipsARefusedBuild(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	coresDir, _ := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "the build that was running"})
	resetCoreRefusalsForTest(t)
	payload := []byte("a nightly this firmware cannot load")
	coreRefusalRecord("mgba", crcOf(payload), "2026-11-09", coreCannotLoadReason+"needs GLIBC_2.34")
	client := coreIndexServer(t, fmt.Sprintf("2026-11-09 %s mgba_libretro.so.zip\n", crcOf(payload)), nil)

	report := runInstalledCoreUpdates(context.Background(), client, nil)
	if report.KnownRefused != 1 || len(report.Updated) != 0 || len(report.Errors) != 0 || len(report.Refused) != 0 {
		t.Fatalf("report = %+v, want the refused build skipped quietly", report)
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "mgba_libretro.so")); err != nil || string(data) != "the build that was running" {
		t.Fatalf("active core = %q, %v", data, err)
	}
	if !strings.Contains(report.summary(), "kept") {
		t.Errorf("summary = %q, want the kept core counted", report.summary())
	}
}

// The regression the activation target fixes: a core that lives in the second
// cores folder is replaced there, under its own name, not copied into the
// first folder beside nothing that loads it.
func TestRunInstalledCoreUpdatesUpdatesTheCoreWhereItLives(t *testing.T) {
	withCoreInstallsAllowed(t, true)
	root := t.TempDir()
	first := filepath.Join(root, "RetroArch", ".retroarch", "cores")
	second := filepath.Join(root, "RetroArch", "cores")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(second, "MGBA_libretro.so"), []byte("the build that was running"), 0o755); err != nil {
		t.Fatal(err)
	}
	setCompatTestDirs(t, filepath.Join(root, "data", "performance"), nil, []string{first, second}, filepath.Join(root, "data", "performance", "cores"))
	resetCoreRefusalsForTest(t)

	if got, err := coreActivationTarget(coreBuild{Core: "mgba", File: "mgba_libretro.so", Pool: true}); err != nil || got != filepath.Join(second, "MGBA_libretro.so") {
		t.Fatalf("target = %q, %v; want the active file where it is", got, err)
	}
	if got, err := coreActivationTarget(coreBuild{Core: "snes9x", File: "snes9x_libretro.so", Pool: true}); err != nil || got != filepath.Join(first, "snes9x_libretro.so") {
		t.Fatalf("a new core's target = %q, %v; want the first cores folder", got, err)
	}

	payload := []byte("the newer mgba build")
	client := coreIndexServer(t, fmt.Sprintf("2026-11-09 %s mgba_libretro.so.zip\n", crcOf(payload)),
		map[string][]byte{"mgba_libretro.so.zip": zestCoreZip(t, "mgba_libretro.so", string(payload))})
	report := runInstalledCoreUpdates(context.Background(), client, nil)
	if len(report.Updated) != 1 || len(report.Errors) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if data, err := os.ReadFile(filepath.Join(second, "MGBA_libretro.so")); err != nil || string(data) != string(payload) {
		t.Fatalf("the core in the second folder = %q, %v; want the new build", data, err)
	}
	if entries, _ := os.ReadDir(first); len(entries) != 0 {
		t.Fatalf("the first folder gained %d file(s); nothing loads a core from there", len(entries))
	}
}

// A build that cannot load on this device is refused before the pool, the core
// that works stays, and the next pass does not fetch the same build again.
func TestRunInstalledCoreUpdatesRefusesABuildThatCannotLoad(t *testing.T) {
	if _, hostMachine := elfHostArch(runtime.GOARCH); runtime.GOOS != "linux" || hostMachine == elf.EM_NONE {
		t.Skip("the load check applies to Linux builds on a known architecture")
	}
	withCoreInstallsAllowed(t, true)
	coresDir, poolDir := installHistoryFixture(t, map[string]string{"mgba_libretro.so": "the build that was running"})
	resetCoreRefusalsForTest(t)
	payload := buildTestELF(t, testELF{machine: elf.EM_PPC64})
	archive := zestCoreZip(t, "mgba_libretro.so", string(payload))
	index := fmt.Sprintf("2026-11-09 %s mgba_libretro.so.zip\n", crcOf(payload))
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".index-extended") {
			_, _ = w.Write([]byte(index))
			return
		}
		fetches.Add(1)
		_, _ = w.Write(archive)
	}))
	allowUpdateTestServer(t, server.URL)
	prevIndex, prevBase := coreUpdateIndexURL, coreUpdateBaseURL
	coreUpdateIndexURL, coreUpdateBaseURL = server.URL+"/.index-extended", server.URL+"/"
	t.Cleanup(func() {
		coreUpdateIndexURL, coreUpdateBaseURL = prevIndex, prevBase
		server.Close()
	})

	report := runInstalledCoreUpdates(context.Background(), server.Client(), nil)
	if len(report.Refused) != 1 || len(report.Errors) != 0 || len(report.Updated) != 0 {
		t.Fatalf("report = %+v, want one refusal and no failure", report)
	}
	if !strings.Contains(report.RefusedNote, "cannot load on this firmware") {
		t.Errorf("note = %q", report.RefusedNote)
	}
	if data, err := os.ReadFile(filepath.Join(coresDir, "mgba_libretro.so")); err != nil || string(data) != "the build that was running" {
		t.Fatalf("active core = %q, %v; a refused build must not touch it", data, err)
	}
	if _, err := os.Stat(filepath.Join(poolDir, "mgba")); !os.IsNotExist(err) {
		t.Fatalf("the refused build reached the pool (%v)", err)
	}
	if msg := emuAutoUpdateSummary(report); !strings.HasPrefix(msg, "mGBA: the 2026-11-09 build cannot load") || !strings.Contains(msg, "kept") {
		t.Errorf("toast = %q", msg)
	}

	again := runInstalledCoreUpdates(context.Background(), server.Client(), nil)
	if again.KnownRefused != 1 || fetches.Load() != 1 {
		t.Fatalf("second pass: %+v after %d fetch(es); the refused build was downloaded again", again, fetches.Load())
	}
}

func TestCoreDirIsSystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		if coreDirIsSystem(`C:\usr\cores`) {
			t.Error("Windows has no firmware partition")
		}
		return
	}
	for dir, want := range map[string]bool{
		"/usr/trimui/RetroArch/.retroarch/cores": true,
		"/usr":                                   true,
		"/mnt/SDCARD/RetroArch/.retroarch/cores": false,
		"/usrlocal/cores":                        false,
		"":                                       false,
	} {
		if got := coreDirIsSystem(dir); got != want {
			t.Errorf("coreDirIsSystem(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestCoreRefusedSummaryNamesTheCores(t *testing.T) {
	one := emuInstallReport{Checked: true, Refused: []string{"mGBA"}, RefusedNote: "the 2026-11-09 build cannot load on this firmware: needs GLIBC_2.34"}
	if got := emuAutoUpdateSummary(one); got != "mGBA: the 2026-11-09 build cannot load on this firmware: needs GLIBC_2.34 · the installed build is kept" {
		t.Errorf("one refusal = %q", got)
	}
	many := emuInstallReport{Checked: true, Refused: []string{"mGBA", "Snes9x", "fceumm", "gpsp"}}
	if got := emuAutoUpdateSummary(many); !strings.HasPrefix(got, "New builds of mGBA, Snes9x, fceumm +1 more cannot be used") {
		t.Errorf("many refusals = %q", got)
	}
	mixed := emuInstallReport{Checked: true, Updated: []string{"mGBA"}, Refused: []string{"Snes9x"}}
	if got := emuAutoUpdateSummary(mixed); !strings.Contains(got, "Updated 1 emulator core") || !strings.Contains(got, "1 kept") {
		t.Errorf("mixed = %q", got)
	}
}

// A run that stopped for a nearly full card says so — in the press's summary
// and in the daily pass's toast, whatever else it did — and the room it asks
// for counts the core it would replace, at least 16 MB of it.
func TestCoreUpdatesSayTheyStoppedForANearlyFullCard(t *testing.T) {
	if got := coreUpdateFreeFloor(filepath.Join(t.TempDir(), "missing_libretro.so")); got != bootLowSpaceBytes+4*(16<<20) {
		t.Errorf("floor for an unknown core = %d", got)
	}
	reports := []emuInstallReport{
		{Checked: true, Current: 2, LowSpace: "180 MB"},
		{Checked: true, Updated: []string{"mGBA"}, LowSpace: "180 MB"},
		{Checked: true, Refused: []string{"Snes9x"}, RefusedNote: "the 2026-11-09 build cannot load on this firmware: needs GLIBC_2.34", LowSpace: "180 MB"},
	}
	for i, r := range reports {
		if got := emuAutoUpdateSummary(r); !strings.Contains(got, "almost full (180 MB free)") {
			t.Errorf("report %d: toast = %q, want the full card named", i, got)
		}
	}
	if got := reports[0].summary(); !strings.Contains(got, "almost full") || strings.Contains(got, "stopped early") {
		t.Errorf("summary = %q", got)
	}
}

// useTempEmuRuntimeState points the updaters' records at a temporary file and
// starts from a clean session, as a fresh start would.
func useTempEmuRuntimeState(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), emuRuntimeStateName)
	prev := emuRuntimeStatePath
	emuRuntimeStatePath = func() string { return path }
	emuRuntimeResetForTest()
	t.Cleanup(func() {
		emuRuntimeStatePath = prev
		emuRuntimeResetForTest()
	})
	return path
}

// emuRuntimeResetForTest forgets the records and the session, as a restart
// does; only the file stays.
func emuRuntimeResetForTest() {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeState.loaded = false
	emuRuntimeState.path = ""
	emuRuntimeState.file = emuRuntimeFile{}
	emuRuntimeState.checked = nil
	emuRuntimeState.attempt = nil
	emuRuntimeState.retryAt = nil
	emuRuntimeState.checkFails = nil
	emuRuntimeState.installFails = nil
	emuRuntimeState.hold = time.Time{}
	emuRuntimeState.notes = nil
}

// TestEmuRuntimeUpdaterRules pins the rules the pass applies to the updaters:
// a stamp the clock cannot vouch for is due, a failure waits an hour and
// doubles up to a day, only GitHub's refusals start the hour's hold, and a
// build's label tells a rebuild under the same tag from the installed one.
func TestEmuRuntimeUpdaterRules(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		last string
		want bool
	}{
		{"", true},
		{"garbled", true},
		{"2019-06-01T00:00:00Z", true},
		{now.Add(time.Hour).Format(time.RFC3339), true},
		{now.Add(-time.Hour).Format(time.RFC3339), false},
		{now.Add(-25 * time.Hour).Format(time.RFC3339), true},
	} {
		if got := emuRuntimeStampDue(tc.last, now, coreAutoUpdateInterval); got != tc.want {
			t.Errorf("due(%q) = %v, want %v", tc.last, got, tc.want)
		}
	}
	for n, want := range map[int]time.Duration{1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour, 5: 16 * time.Hour, 6: 24 * time.Hour, 40: 24 * time.Hour} {
		if got := emuRuntimeRetryAfter(n); got != want {
			t.Errorf("wait after failure %d = %v, want %v", n, got, want)
		}
	}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("%w (listing JukaHub releases: HTTP 403)", errRelPackRateLimited), true},
		{errors.New("HTTP 429 from https://api.github.com/repos/a/b/releases/latest"), true},
		{errors.New("HTTP 403 from https://buildbot.libretro.com/stable/"), false},
		{errors.New("HTTP 404 from https://api.github.com/repos/a/b/releases/latest"), false},
	} {
		if got := emuRateLimited(tc.err); got != tc.want {
			t.Errorf("rate limited(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	at := "2026-11-10T14:03:05Z"
	for _, tc := range []struct {
		installed string
		rel       emuWinRelease
		want      string
	}{
		{"v0.1.4", emuWinRelease{Tag: "v0.1.5", AssetDate: at}, "v0.1.5"},
		{"v0.1.5", emuWinRelease{Tag: "v0.1.5", AssetDate: at}, "v0.1.5 (2026-11-10 14:03)"},
		{"continuous", emuWinRelease{Tag: "continuous", AssetDate: at}, "continuous (2026-11-10 14:03)"},
		{"v1.0", emuWinRelease{}, "new build"},
	} {
		if got := emuReleaseLabel(tc.installed, tc.rel); got != tc.want {
			t.Errorf("label(%q, %+v) = %q, want %q", tc.installed, tc.rel, got, tc.want)
		}
	}
	// Checks and installs fail in a row apart: the first failed install after
	// a failed check is a first failure (the toast names it).
	useTempEmuRuntimeState(t)
	if n := emuRuntimeFailed("test:a", false); n != 1 {
		t.Errorf("first failed check = %d in a row, want 1", n)
	}
	emuRuntimeAnswered("test:a")
	if n := emuRuntimeFailed("test:a", true); n != 1 {
		t.Errorf("first failed install after a failed check = %d in a row, want 1", n)
	}
	if n := emuRuntimeFailed("test:a", true); n != 2 {
		t.Errorf("second failed install = %d in a row, want 2", n)
	}
	emuRuntimeChecked("test:a", now)
	if n := emuRuntimeFailed("test:a", true); n != 1 {
		t.Errorf("failed install after a check that answered = %d in a row, want 1", n)
	}
	// A PC: no stamp is overdue only after three days of waiting, and overdue
	// spares the battery rule, never the idle minutes.
	if emuDesktopOverdueFor("", now, coreAutoUpdatePoll) || !emuDesktopOverdueFor("", now, coreAutoUpdateOverdue) {
		t.Error("a PC's first pass: overdue after one poll, or not after three days")
	}
	if !emuDesktopOverdueFor(now.Add(-73*time.Hour).Format(time.RFC3339), now, 0) {
		t.Error("a stamp three days old is not overdue on a PC")
	}
	if emuDesktopMoment(false, false, false, time.Minute, true) {
		t.Error("an overdue pass ran on a PC in use")
	}
	if !emuDesktopMoment(false, false, true, emuDesktopIdle, true) || emuDesktopMoment(false, false, true, emuDesktopIdle, false) {
		t.Error("on battery a PC pass should run only when overdue")
	}
}

// TestEmuRuntimeRecordsSurviveARestart: an updater checked an hour before a
// restart is not asked again after it, and is once its day is over.
func TestEmuRuntimeRecordsSurviveARestart(t *testing.T) {
	path := useTempEmuRuntimeState(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	emuRuntimeChecked("hakux", now)
	emuRuntimeUpdateRecord("hakux", func(r *emuRuntimeRecord) { r.Installed = "v0.1.5" })
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no record file: %v", err)
	}
	emuRuntimeResetForTest()
	rec := emuRuntimeRecordFor("hakux")
	if rec.Installed != "v0.1.5" || rec.CheckedAt != "2026-11-11T12:00:00Z" {
		t.Fatalf("record after a restart = %+v", rec)
	}
	if emuRuntimeDue("hakux", now.Add(time.Hour), coreAutoUpdateInterval) {
		t.Error("checked an hour before the restart, but due again")
	}
	if !emuRuntimeDue("hakux", now.Add(25*time.Hour), coreAutoUpdateInterval) {
		t.Error("checked a day before, but not due")
	}
}

// TestEmuRuntimeNotifyPassSaysEachBuildOnce runs the updaters' share of a
// Notify pass: a new build is announced once, and a build the pass installed
// that is offered again (the user rolled back from it) is left alone.
func TestEmuRuntimeNotifyPassSaysEachBuildOnce(t *testing.T) {
	useTempEmuRuntimeState(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	offer := func(label string) func(context.Context, *http.Client) (bool, string, error) {
		return func(context.Context, *http.Client) (bool, string, error) { return true, label, nil }
	}
	never := func(context.Context, *http.Client, func(string)) error {
		t.Error("Notify mode installed something")
		return nil
	}
	installed := func() bool { return true }
	emuRuntimeUpdateRecord("test:rolledback", func(r *emuRuntimeRecord) { r.Installed = "v2" })
	list := []emuRuntimeUpdater{
		{Key: "test:new", Title: "Newemu", Installed: installed, Check: offer("v3"), Install: never},
		{Key: "test:rolledback", Title: "Oldemu", Installed: installed, Check: offer("v2"), Install: never},
	}
	report := emuRunRuntimeUpdaters(context.Background(), coreUpdateNotify, list, nil, now)
	if len(report.Available) != 1 || report.Available[0] != "Newemu v3" {
		t.Fatalf("first pass announced %q, want only Newemu v3", report.Available)
	}
	if emuRuntimeRecordFor("test:new").CheckedAt == "" {
		t.Error("the check that answered was not stamped")
	}
	report = emuRunRuntimeUpdaters(context.Background(), coreUpdateNotify, list, nil, now.Add(25*time.Hour))
	if len(report.Available) != 0 {
		t.Errorf("second pass announced %q again", report.Available)
	}
}
