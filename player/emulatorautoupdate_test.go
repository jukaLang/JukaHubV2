package main

// emulatorautoupdate_test.go — the handheld brings its emulator cores forward
// without a press. Three things have to hold for that to be safe enough to run
// unattended: it only runs where cores can be installed at all, a failed check
// is retried rather than standing down for a day, and the builds it replaces
// are kept to a fixed number so the card cannot fill with them.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestInstallBarNamesACoreUpdate: the bar is what makes a background download
// visible, and "Installing Emulator cores" would be the wrong sentence for it.
func TestInstallBarNamesACoreUpdate(t *testing.T) {
	t.Cleanup(resetInstallBar)
	resetInstallBar()

	installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	if got := installBarTitle(); got != "Updating emulator cores" {
		t.Fatalf("title = %q, want the update wording", got)
	}
	if visible, tool, _, _, _, _, _, _, _ := installBarSnapshot(); !visible || tool != "Emulator cores" {
		t.Fatalf("snapshot visible=%v tool=%q, want the running card", visible, tool)
	}
	installBarAbort()

	installBarBegin("ffplay")
	if got := installBarTitle(); got != "" {
		t.Fatalf("a plain install carries the title %q; it should use the default wording", got)
	}
	installBarAbort()
}
