package main

// uhist_test.go pins the version history: a committed swap's backups become a
// saved version, an interrupted one is left to the journal rollback, the
// limit keeps the newest, a rollback restores whole files only, and the
// release rolled back from is not offered again.

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// upchTestPatchDir points the Patch state folder at a temporary one and puts
// the patch state back afterwards.
func upchTestPatchDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevFn, prevDir := patchStateDirFn, patchStateDir
	patchStateMu.Lock()
	prevState := *patchState
	patchStateMu.Unlock()
	patchStateDirFn = func() (string, error) { return dir, nil }
	patchStateDir = dir
	t.Cleanup(func() {
		patchStateDirFn, patchStateDir = prevFn, prevDir
		patchStateMu.Lock()
		*patchState = prevState
		patchStateMu.Unlock()
	})
	return dir
}

// upchTestResetState empties the channel's and the history's caches for one
// test and restores them afterwards.
func upchTestResetState(t *testing.T) {
	t.Helper()
	upchState.mu.Lock()
	prevEntries, prevFrom, prevKind, prevErr := upchState.entries, upchState.fromTag, upchState.fromKind, upchState.lastErr
	upchState.entries, upchState.fromTag, upchState.fromKind, upchState.lastErr = nil, "", "", ""
	upchState.viewAt = time.Time{}
	upchState.mu.Unlock()
	uhistCache.mu.Lock()
	prevHist, prevSkip := uhistCache.entries, uhistCache.skipTag
	uhistCache.entries, uhistCache.skipTag = nil, ""
	uhistCache.version++
	uhistCache.mu.Unlock()
	prevKeep := uhistKeep.Load()
	t.Cleanup(func() {
		upchState.mu.Lock()
		upchState.entries, upchState.fromTag, upchState.fromKind, upchState.lastErr = prevEntries, prevFrom, prevKind, prevErr
		upchState.viewAt = time.Time{}
		upchState.mu.Unlock()
		uhistCache.mu.Lock()
		uhistCache.entries, uhistCache.skipTag = prevHist, prevSkip
		uhistCache.version++
		uhistCache.mu.Unlock()
		uhistKeep.Store(prevKeep)
	})
}

// upchTestExecutable returns the head of a program upchCheckHostBinary accepts
// on this host, for goarch.
func upchTestExecutable(goarch string) []byte {
	b := make([]byte, 256)
	if runtime.GOOS == "windows" {
		b[0], b[1] = 'M', 'Z'
		binary.LittleEndian.PutUint32(b[0x3c:0x40], 0x40)
		copy(b[0x40:0x44], "PE\x00\x00")
		binary.LittleEndian.PutUint16(b[0x44:0x46], upchPEMachine(goarch))
		return b
	}
	copy(b[0:4], "\x7fELF")
	b[4] = 2 // 64-bit
	b[5] = 1 // little-endian
	binary.LittleEndian.PutUint16(b[18:20], upchELFMachine(goarch))
	return b
}

func upchWriteTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// uhistNoteTestSwap journals a swap over the installation in exeDir the way
// swapInstallFiles does, and notes it for the history.
func uhistNoteTestSwap(t *testing.T, exeDir, tag string, dests ...string) *journal {
	t.Helper()
	j := newJournal("juka-update", tag)
	for _, name := range dests {
		if err := j.recordReplace(filepath.Join(exeDir, name)); err != nil {
			t.Fatalf("recordReplace %s: %v", name, err)
		}
	}
	uhistNoteSwap(j, exeDir, tag, uhistReasonUpdate)
	return j
}

func TestUhistAdoptsOnlyCommittedSwaps(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	uhistKeep.Store(2)
	exeDir := t.TempDir()
	exe := releaseExecutableName()
	upchWriteTestFile(t, filepath.Join(exeDir, exe), upchTestExecutable(runtime.GOARCH))
	upchWriteTestFile(t, filepath.Join(exeDir, "jukaconfig.json"), []byte(`{"Version":"0.4.0"}`))

	// A swap the helper committed: it deleted the journal, the backups stay.
	committed := uhistNoteTestSwap(t, exeDir, "JukaHub-20261101120000", exe, "jukaconfig.json", "new-in-this-release.txt")
	if err := os.Remove(committed.path); err != nil {
		t.Fatal(err)
	}
	// A swap that was interrupted: its journal is still there.
	interrupted := uhistNoteTestSwap(t, exeDir, "JukaHub-20261102120000", exe)

	uhistAdoptCommittedSwaps()

	entries, _ := uhistEntriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("%d saved versions, want the committed swap's only", len(entries))
	}
	e := entries[0]
	if e.ReplacedBy != "JukaHub-20261101120000" || e.Bytes <= 0 {
		t.Fatalf("saved version = %+v", e)
	}
	names := map[string]bool{}
	for _, f := range e.Files {
		names[f.Name] = true
		if _, err := os.Stat(filepath.Join(dir, "versions", e.ID, f.Stored)); err != nil {
			t.Errorf("the saved copy of %s is not in its folder: %v", f.Name, err)
		}
	}
	if !names[exe] || !names["jukaconfig.json"] {
		t.Fatalf("saved files = %v, want the executable and the config", names)
	}
	if len(e.Added) != 1 || e.Added[0] != "new-in-this-release.txt" {
		t.Errorf("files the update added = %v", e.Added)
	}
	if pathExists(filepath.Join(dir, "journals", "files", committed.ID)) {
		t.Error("the committed swap's backups were copied instead of moved")
	}
	// The interrupted swap is the journal check's: its backups stay for the
	// rollback, and nothing of it became a saved version.
	if !pathExists(filepath.Join(dir, "journals", "files", interrupted.ID)) {
		t.Error("the interrupted swap's backups were taken from the journal")
	}
	if pending, _ := filepath.Glob(filepath.Join(dir, "versions", "pending-*.json")); len(pending) != 0 {
		t.Errorf("records left behind: %v", pending)
	}
}

// Keeping nothing means the backups are left for the journal purge.
func TestUhistKeepsNothingWhenOff(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	uhistKeep.Store(0)
	exeDir := t.TempDir()
	exe := releaseExecutableName()
	upchWriteTestFile(t, filepath.Join(exeDir, exe), upchTestExecutable(runtime.GOARCH))
	j := uhistNoteTestSwap(t, exeDir, "JukaHub-20261101120000", exe)
	if err := os.Remove(j.path); err != nil {
		t.Fatal(err)
	}
	uhistAdoptCommittedSwaps()
	if entries, _ := uhistEntriesSnapshot(); len(entries) != 0 {
		t.Fatalf("kept %d versions with the setting off", len(entries))
	}
	if !pathExists(filepath.Join(dir, "journals", "files", j.ID)) {
		t.Fatal("the backups were moved although nothing is kept")
	}
}

// uhistWriteTestVersion writes a saved version straight into versions/.
func uhistWriteTestVersion(t *testing.T, dir string, e uhistEntry, files map[string][]byte) {
	t.Helper()
	folder := filepath.Join(dir, "versions", e.ID)
	for stored, data := range files {
		upchWriteTestFile(t, filepath.Join(folder, stored), data)
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	upchWriteTestFile(t, filepath.Join(folder, "version.json"), data)
}

func TestUhistPruneKeepsTheNewest(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	for i, saved := range []string{"2026-11-01T10:00:00Z", "2026-11-03T10:00:00Z", "2026-11-02T10:00:00Z"} {
		id := "build_" + strings.NewReplacer("-", "", ":", "").Replace(saved)
		uhistWriteTestVersion(t, dir, uhistEntry{
			ID:      id,
			SavedAt: saved,
			Files:   []uhistFile{{Name: "jukaconfig.json", Stored: "0000-jukaconfig.json", Size: 2}},
			Bytes:   int64(i + 1),
		}, map[string][]byte{"0000-jukaconfig.json": []byte("{}")})
	}
	versions := filepath.Join(dir, "versions")
	uhistPrune(versions, 2)
	uhistReload(versions)
	entries, _ := uhistEntriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("%d versions after keeping 2", len(entries))
	}
	if entries[0].SavedAt != "2026-11-03T10:00:00Z" || entries[1].SavedAt != "2026-11-02T10:00:00Z" {
		t.Fatalf("kept %s and %s, want the two newest", entries[0].SavedAt, entries[1].SavedAt)
	}
	uhistPrune(versions, 0)
	uhistReload(versions)
	if entries, _ := uhistEntriesSnapshot(); len(entries) != 0 {
		t.Fatalf("%d versions left after turning the history off", len(entries))
	}
}

func TestUhistRollbackFilesChecksTheSavedCopy(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	versions := filepath.Join(dir, "versions")
	exe := releaseExecutableName()
	bin := upchTestExecutable(runtime.GOARCH)
	e := uhistEntry{
		ID:      "JukaHub-20261020120000_20261108T120000Z",
		Tag:     "JukaHub-20261020120000",
		SavedAt: "2026-11-08T12:00:00Z",
		Files: []uhistFile{
			{Name: releaseInfoFileName, Stored: "0002-release-info.json", Size: 2},
			{Name: exe, Stored: "0000-" + exe, Size: int64(len(bin))},
			{Name: "fonts/Inter-Regular.ttf", Stored: "0001-Inter-Regular.ttf", Size: 4},
		},
	}
	uhistWriteTestVersion(t, dir, e, map[string][]byte{
		"0002-release-info.json": []byte("{}"),
		"0000-" + exe:            bin,
		"0001-Inter-Regular.ttf": []byte("font"),
	})
	exeDir := t.TempDir()
	files, err := uhistRollbackFiles(versions, e, exeDir)
	if err != nil {
		t.Fatalf("uhistRollbackFiles: %v", err)
	}
	if len(files) != 3 || files[len(files)-1].Name != releaseInfoFileName {
		t.Fatalf("files = %+v, want three with the release stamp last", files)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Dest, exeDir+string(filepath.Separator)) {
			t.Errorf("%s would be written outside the installation: %s", f.Name, f.Dest)
		}
	}

	damaged := e
	damaged.Files = append([]uhistFile(nil), e.Files...)
	damaged.Files[2].Size = 99
	if _, err := uhistRollbackFiles(versions, damaged, exeDir); err == nil {
		t.Error("a saved file whose size changed was accepted")
	}
	escaping := e
	escaping.Files = []uhistFile{{Name: "../outside", Stored: "0000-" + exe, Size: int64(len(bin))}}
	if _, err := uhistRollbackFiles(versions, escaping, exeDir); err == nil {
		t.Error("a saved path outside the installation was accepted")
	}
	missing := e
	missing.Files = []uhistFile{{Name: exe, Stored: "9999-gone", Size: 1}}
	if _, err := uhistRollbackFiles(versions, missing, exeDir); err == nil {
		t.Error("a missing saved file was accepted")
	}
}

// After a rollback the release rolled back from is not offered again, until
// any update is installed.
func TestUhistRollbackSkipsTheReleaseItLeft(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	clearToastAfterTest(t)
	notifHistoryMu.Lock()
	prevHistory := nc.history
	notifHistoryMu.Unlock()
	t.Cleanup(func() {
		notifHistoryMu.Lock()
		nc.history = prevHistory
		notifHistoryMu.Unlock()
	})
	versions := filepath.Join(dir, "versions")
	if err := os.MkdirAll(versions, 0o700); err != nil {
		t.Fatal(err)
	}
	uhistSwapCommitted(versions, uhistPending{
		Reason:  uhistReasonRollback,
		FromTag: "JukaHub-20261105120000",
		ToTag:   "JukaHub-20261020120000",
		ToDate:  "2026-10-20T12:00:00Z",
	})
	uhistReload(versions)
	patchStateMu.Lock()
	installed := patchState.InstalledReleaseTag
	patchStateMu.Unlock()
	if installed != "JukaHub-20261020120000" {
		t.Fatalf("installed release = %q after rolling back", installed)
	}
	left := &githubRelease{TagName: "JukaHub-20261105120000", PublishedAt: "2026-11-05T12:00:00Z"}
	if !uhistSkipped(left.TagName) || releaseIsNewerThanInstalled(left) {
		t.Fatal("the release rolled back from is offered again")
	}
	next := &githubRelease{TagName: "JukaHub-20261110120000", PublishedAt: "2026-11-10T12:00:00Z"}
	if uhistSkipped(next.TagName) {
		t.Fatal("a later release is skipped too")
	}

	uhistSwapCommitted(versions, uhistPending{Reason: uhistReasonUpdate, ToTag: next.TagName})
	uhistReload(versions)
	if uhistSkipped(left.TagName) {
		t.Fatal("installing an update did not end the skip")
	}
}

func TestUhistEntryIDIsAFolderName(t *testing.T) {
	cases := []struct {
		tag, created, want string
	}{
		{"JukaHub-20261017120000", "2026-11-08T12:00:00Z", "JukaHub-20261017120000_20261108T120000Z"},
		{"", "2026-11-08T12:00:00Z", "build_20261108T120000Z"},
		{"../x", "2026-11-08T12:00:00Z", "___x_20261108T120000Z"},
	}
	for _, tc := range cases {
		if got := uhistEntryID(tc.tag, tc.created); got != tc.want {
			t.Errorf("uhistEntryID(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// An update's probation plan (updrescue.go) lists every file the swap
// replaces with a backup launch.sh can find, drops a release stamp the update
// adds, refuses paths that leave the app folder, survives the next start when
// no versions are kept, and ends with a rollback.
func TestUhistUpdateProbationPlan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launch.sh's plan is made on the handheld only")
	}
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	uhistKeep.Store(0)
	exeDir := t.TempDir()
	prevApp := updAppDirFn
	updAppDirFn = func() (string, error) { return exeDir, nil }
	t.Cleanup(func() { updAppDirFn = prevApp })
	t.Setenv(updProbationEnv, "1")
	t.Setenv("JUKAHUB_LAUNCHED_BY", "launch.sh")
	tag := "JukaHub-20261120000000"
	upchWriteTestFile(t, filepath.Join(exeDir, "JukaHub"), []byte("old build"))
	upchWriteTestFile(t, filepath.Join(exeDir, "bin", "helper"), []byte("old helper"))
	staging, err := stagingDirFor(tag)
	if err != nil {
		t.Fatal(err)
	}
	upchWriteTestFile(t, filepath.Join(staging, "JukaHub"), []byte("the new build"))
	upchWriteTestFile(t, filepath.Join(staging, "launch.sh"), []byte("#!/bin/sh\n#  "+updProbationToken+"\n"))
	j := uhistNoteTestSwap(t, exeDir, tag, "JukaHub", "bin/helper", releaseInfoFileName)

	planPath := filepath.Join(exeDir, updProbationName)
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("no plan: %v", err)
	}
	plan, err := updPlanParse(data)
	if err != nil {
		t.Fatalf("the plan does not parse: %v\n%s", err, data)
	}
	if plan.To != tag || plan.Journal != j.ID || plan.ExeSize != int64(len("the new build")) || len(plan.Dirs) != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Files) != 2 || plan.Files[0].Name != "JukaHub" || plan.Files[1].Name != "bin/helper" || plan.Files[1].Mode != 0o755 {
		t.Fatalf("files = %+v", plan.Files)
	}
	for _, f := range plan.Files {
		info, err := os.Stat(filepath.Join(filepath.FromSlash(plan.Dirs[0]), f.Stored))
		if err != nil || info.Size() != f.Size {
			t.Errorf("the backup of %s is not where the plan says: %v", f.Name, err)
		}
	}
	if len(plan.Drop) != 1 || plan.Drop[0] != releaseInfoFileName {
		t.Errorf("drop = %v, want the release stamp the update adds", plan.Drop)
	}
	for _, bad := range []string{"file ../helper\t0001-helper", "file bin/helper\t../0001-helper", "dir ../../etc"} {
		if _, err := updPlanParse([]byte(string(data) + bad + "\n")); err == nil {
			t.Errorf("a plan with %q was accepted", bad)
		}
	}

	// The helper committed. Keeping no versions, the next start keeps the
	// backups aside for launch.sh instead of purging them.
	if err := os.Remove(j.path); err != nil {
		t.Fatal(err)
	}
	uhistAdoptCommittedSwaps()
	aside := filepath.Join(dir, updProbationDir, j.ID)
	if filepath.ToSlash(aside) != plan.Dirs[2] {
		t.Errorf("kept in %s, the plan looks in %s", aside, plan.Dirs[2])
	}
	for _, f := range plan.Files {
		if !pathExists(filepath.Join(aside, f.Stored)) {
			t.Errorf("the backup of %s was not kept for launch.sh", f.Name)
		}
	}

	// A rollback ends the plan and what it kept.
	updProbationNoteSwap(j, exeDir, uhistPending{Reason: uhistReasonRollback})
	if pathExists(planPath) || pathExists(aside) {
		t.Error("a rollback left the plan or its backups behind")
	}

	// A release from before the check never says "ready": no plan for it, or
	// every start of it would be undone.
	upchWriteTestFile(t, filepath.Join(staging, "launch.sh"), []byte("#!/bin/sh\n"))
	uhistNoteTestSwap(t, exeDir, tag, "JukaHub")
	if pathExists(planPath) {
		t.Error("a plan was made for a release that does not report its start")
	}
}
