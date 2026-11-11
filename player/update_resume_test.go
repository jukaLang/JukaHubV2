package main

// update_resume_test.go pins the fix for the endless "update available" loop:
// a release that ships files in folders the installation does not have yet
// (bin/, lib/) installs completely, and a swap whose helper stopped after it
// had replaced the binary is finished at the next start instead of being
// half undone.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newFolderRelease is a release with files in folders an older installation
// lacks, like the first one that shipped the Remote Play helpers and the
// game-audio plugin.
func newFolderRelease(tag string) map[string]string {
	return map[string]string{
		releaseExecutableName(): "new-binary",
		"Inter-Regular.ttf":     "new-font",
		"bin/chiaki-regist":     "regist",
		"bin/chiaki-stream":     "stream",
		"lib/libjukaspatial.so": "plugin",
		releaseInfoFileName:     `{"tag":"` + tag + `"}`,
		"jukaconfig.json":       "new-config",
		"background.jpg":        "new-background",
		"SECURITY.md":           "new-security",
		"JukaPlayer.png":        "new-icon",
		"config.json":           "new-app-config",
		"launch.sh":             "new-launcher",
		"required/yt-dlp":       "archive-tool",
		"apply-update.log":      "not part of the install",
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), body)
	}
}

// TestSwapHelperCreatesNewReleaseFolders runs the real helper against an
// installation without bin/ and lib/. The old helper replaced the binary, then
// failed at bin/chiaki-regist and never reached release-info.json.
func TestSwapHelperCreatesNewReleaseFolders(t *testing.T) {
	exe := releaseExecutableName()
	tag := "JukaHub-20261111002100"
	staged := filepath.Join(t.TempDir(), "JukaHub-Trimui")
	writeTree(t, staged, newFolderRelease(tag))
	install := t.TempDir()
	writeTree(t, install, map[string]string{
		exe:                 "old-binary",
		"Inter-Regular.ttf": "old-font",
		releaseInfoFileName: `{"tag":"JukaHub-20261110054938"}`,
	})

	files, err := stagedInstallFiles(staged, install)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	journal := filepath.Join(t.TempDir(), "journal.json")
	mustWrite(t, journal, "{}")
	script := filepath.Join(t.TempDir(), "apply-update")
	if runtime.GOOS == "windows" {
		script += ".bat"
	}
	// No ensureInstallDirs here: the script itself must make the folders.
	if err := writeSwapScript(script, filepath.Join(install, exe), files, journal, 0); err != nil {
		t.Fatalf("writeSwapScript: %v", err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	sep := "\n"
	if runtime.GOOS == "windows" {
		sep = "\r\n"
	}
	var kept []string
	for _, line := range strings.Split(string(data), sep) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "start ") || trimmed == "sleep 2" || strings.HasPrefix(trimmed, "ping -n 3 ") {
			continue
		}
		kept = append(kept, strings.ReplaceAll(line, "nohup ", "true "))
	}
	if err := os.WriteFile(script, []byte(strings.Join(kept, sep)), 0o700); err != nil {
		t.Fatal(err)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", script)
	} else if sh, lookErr := exec.LookPath("sh"); lookErr == nil {
		cmd = exec.Command(sh, script)
	} else {
		cmd = exec.Command(script)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		log, _ := os.ReadFile(filepath.Join(filepath.Dir(script), swapHelperLogName))
		t.Fatalf("swap helper failed: %v\n%s\n%s", err, out, log)
	}
	for rel, want := range map[string]string{
		exe:                     "new-binary",
		"bin/chiaki-regist":     "regist",
		"bin/chiaki-stream":     "stream",
		"lib/libjukaspatial.so": "plugin",
		"launch.sh":             "new-launcher",
	} {
		if got := readFile(t, filepath.Join(install, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if got := readReleaseInfoFromDir(install).Tag; got != tag {
		t.Errorf("installed stamp = %q, want %q: the update would be offered again", got, tag)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Error("the helper must commit (delete) the journal after a complete swap")
	}
	if _, err := os.Stat(filepath.Join(install, "required", "yt-dlp")); !os.IsNotExist(err) {
		t.Error("required/ belongs to the device: the update must not write into it")
	}
}

// TestSwapScriptMakesEachNewFolderOnce checks both helpers' text on any host:
// one mkdir per folder below the executable's, none for the executable's own.
func TestSwapScriptMakesEachNewFolderOnce(t *testing.T) {
	dir := t.TempDir()
	posix := []stagedInstallFile{
		{Src: "/s/JukaHub", Name: "JukaHub", Dest: "/mnt/SDCARD/Apps/JukaHub/JukaHub"},
		{Src: "/s/bin/a", Name: "bin/a", Dest: "/mnt/SDCARD/Apps/JukaHub/bin/a"},
		{Src: "/s/bin/b", Name: "bin/b", Dest: "/mnt/SDCARD/Apps/JukaHub/bin/b"},
		{Src: "/s/lib/c.so", Name: "lib/c.so", Dest: "/mnt/SDCARD/Apps/JukaHub/lib/c.so"},
	}
	script := filepath.Join(dir, "apply-update.sh")
	if err := writeSwapScriptFor("linux", script, "/mnt/SDCARD/Apps/JukaHub/JukaHub", posix, "/j.json", 0); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, script)
	for _, want := range []string{"mkdir -p '/mnt/SDCARD/Apps/JukaHub/bin'", "mkdir -p '/mnt/SDCARD/Apps/JukaHub/lib'"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("POSIX helper has %q %d times, want once:\n%s", want, n, body)
		}
	}
	if n := strings.Count(body, "mkdir -p "); n != 2 {
		t.Errorf("POSIX helper makes %d folders, want bin and lib only (not the executable's own):\n%s", n, body)
	}
	if mk, cp := strings.Index(body, "mkdir -p '/mnt/SDCARD/Apps/JukaHub/bin'"), strings.Index(body, "/mnt/SDCARD/Apps/JukaHub/bin/a.new"); mk < 0 || cp < 0 || mk > cp {
		t.Errorf("the folder must be made before the first copy into it:\n%s", body)
	}
	if !strings.Contains(body, swapHelperLogName) {
		t.Errorf("POSIX helper keeps no log of its errors:\n%s", body)
	}

	win := []stagedInstallFile{
		{Src: `C:\s\JukaHub.exe`, Name: "JukaHub.exe", Dest: `C:\Games\JukaHub\JukaHub.exe`},
		{Src: `C:\s\bin\a.dll`, Name: `bin\a.dll`, Dest: `C:\Games\JukaHub\bin\a.dll`},
		{Src: `C:\s\bin\b.dll`, Name: `bin\b.dll`, Dest: `C:\Games\JukaHub\BIN\b.dll`},
	}
	bat := filepath.Join(dir, "apply-update.bat")
	if err := writeSwapScriptFor("windows", bat, `C:\Games\JukaHub\JukaHub.exe`, win, `C:\j.json`, 0); err != nil {
		t.Fatal(err)
	}
	body = readFile(t, bat)
	want := `if not exist "C:\Games\JukaHub\bin\" mkdir "C:\Games\JukaHub\bin"`
	if n := strings.Count(body, want); n != 1 {
		t.Errorf("Windows helper has %q %d times, want once:\n%s", want, n, body)
	}
	if strings.Count(body, "mkdir ") != 1 {
		t.Errorf("Windows helper makes a folder it does not need (the exe's own, or bin twice by case):\n%s", body)
	}
}

func TestEnsureInstallDirsMakesMissingFolders(t *testing.T) {
	install := t.TempDir()
	files := []stagedInstallFile{
		{Name: "JukaHub", Dest: filepath.Join(install, "JukaHub")},
		{Name: "bin/x", Dest: filepath.Join(install, "bin", "x")},
		{Name: "lib/deep/y", Dest: filepath.Join(install, "lib", "deep", "y")},
	}
	if err := ensureInstallDirs(files, install); err != nil {
		t.Fatalf("ensureInstallDirs: %v", err)
	}
	for _, d := range []string{"bin", filepath.Join("lib", "deep")} {
		if info, err := os.Stat(filepath.Join(install, d)); err != nil || !info.IsDir() {
			t.Errorf("%s was not created: %v", d, err)
		}
	}
	// A file where a folder must go is an error the user sees, not a helper
	// that fails in the background.
	mustWrite(t, filepath.Join(install, "blocked"), "file")
	bad := []stagedInstallFile{{Name: "blocked/z", Dest: filepath.Join(install, "blocked", "z")}}
	if err := ensureInstallDirs(bad, install); err == nil {
		t.Error("a folder that cannot be made must stop the swap before it starts")
	}
}

// halfDoneSwap lays out what the old helper left behind: the release staged
// and marked ready, the installation with the NEW binary but the old stamp and
// no bin/ or lib/, and the journal it never committed.
func halfDoneSwap(t *testing.T, binary string) (tag, install, exe, journalPath string, j *journal) {
	t.Helper()
	base := withTempPatchState(t)
	t.Cleanup(func() { updateResumeReplacedConfig.Store(false) })
	tag = "JukaHub-20261111002100"
	staging, err := stagingDirFor(tag)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(staging, "JukaHub-Trimui"), newFolderRelease(tag))
	mustWrite(t, stagedReadyMarker(staging), tag+"\n")
	mustWrite(t, filepath.Join(staging, swapHelperLogName), "cp: can't create 'bin/chiaki-regist.new': No such file or directory\n")

	install = t.TempDir()
	exe = filepath.Join(install, releaseExecutableName())
	writeTree(t, install, map[string]string{
		releaseExecutableName(): binary,
		"Inter-Regular.ttf":     "old-font",
		"launch.sh":             "old-launcher",
		releaseInfoFileName:     `{"tag":"JukaHub-20261110054938"}`,
	})

	journalPath = filepath.Join(base, "journals", "juka-update-1.json")
	backups := filepath.Join(base, "journals", "files", "juka-update-1")
	j = &journal{ID: "juka-update-1", Kind: "juka-update", Target: tag, Entries: []journalEntry{
		{Kind: "replace", Path: filepath.Join(install, "Inter-Regular.ttf"), Backup: filepath.Join(backups, "0000-Inter-Regular.ttf")},
		{Kind: "replace", Path: exe, Backup: filepath.Join(backups, "0001-"+releaseExecutableName())},
		{Kind: "remove", Path: filepath.Join(install, "bin", "chiaki-regist")},
	}}
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, journalPath, string(data))
	j.path = journalPath
	return tag, install, exe, journalPath, j
}

func TestResumeInterruptedUpdateFinishesHalfDoneSwap(t *testing.T) {
	tag, install, exe, journalPath, j := halfDoneSwap(t, "new-binary")
	if !resumeInterruptedUpdate(j, exe) {
		t.Fatal("a swap that already replaced the running binary must be finished, not undone")
	}
	for rel, want := range map[string]string{
		releaseExecutableName(): "new-binary",
		"Inter-Regular.ttf":     "new-font",
		"bin/chiaki-regist":     "regist",
		"bin/chiaki-stream":     "stream",
		"lib/libjukaspatial.so": "plugin",
		"launch.sh":             "new-launcher",
		"jukaconfig.json":       "new-config",
	} {
		if got := readFile(t, filepath.Join(install, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if got := readReleaseInfoFromDir(install).Tag; got != tag {
		t.Errorf("installed stamp = %q, want %q: the same update would be offered again", got, tag)
	}
	for _, rel := range []string{"required/yt-dlp", "apply-update.log", "launch.sh" + resumeTempSuffix} {
		if _, err := os.Stat(filepath.Join(install, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s must not be in the installation", rel)
		}
	}
	if !updateResumeReplacedConfig.Load() {
		t.Error("a replaced jukaconfig.json must be reloaded once the config watcher starts")
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Error("a finished swap must commit its journal, or the next start undoes it")
	}
	// Nothing is left for a later start to replay.
	if left := pendingJournals(); len(left) != 0 {
		t.Errorf("pending journals after finishing: %v", left)
	}
}

func TestResumeInterruptedUpdateLeavesTheOldBuildToRollback(t *testing.T) {
	_, install, exe, journalPath, j := halfDoneSwap(t, "old-binary")
	if resumeInterruptedUpdate(j, exe) {
		t.Fatal("the helper never reached the binary: the journal must be rolled back, not finished")
	}
	if got := readFile(t, filepath.Join(install, "Inter-Regular.ttf")); got != "old-font" {
		t.Errorf("a refused resume copied files anyway: Inter-Regular.ttf = %q", got)
	}
	if _, err := os.Stat(filepath.Join(install, "bin")); !os.IsNotExist(err) {
		t.Error("a refused resume created bin/")
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Errorf("the journal must stay for the rollback: %v", err)
	}
}

func TestResumeInterruptedUpdateNeedsACompleteStagingTree(t *testing.T) {
	tag, _, exe, _, j := halfDoneSwap(t, "new-binary")
	staging, err := stagingDirFor(tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stagedReadyMarker(staging)); err != nil {
		t.Fatal(err)
	}
	if resumeInterruptedUpdate(j, exe) {
		t.Fatal("a staging tree without its ready marker may be cut short and must not finish an install")
	}
	other := &journal{Kind: "sd-merge", Target: tag}
	if resumeInterruptedUpdate(other, exe) {
		t.Fatal("only an app swap journal can be finished")
	}
}

// On Windows the Patch folder is per user, shared by every copy of JukaHub: a
// copy must not finish another copy's swap into its own folder.
func TestResumeInterruptedUpdateOnlyFinishesItsOwnInstall(t *testing.T) {
	_, _, _, journalPath, j := halfDoneSwap(t, "new-binary")
	other := t.TempDir()
	otherExe := filepath.Join(other, releaseExecutableName())
	mustWrite(t, otherExe, "new-binary")
	if resumeInterruptedUpdate(j, otherExe) {
		t.Fatal("a swap journaled for another installation was finished into this one")
	}
	if _, err := os.Stat(filepath.Join(other, "bin")); !os.IsNotExist(err) {
		t.Error("the refused resume wrote into the other installation")
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Errorf("the other installation's journal must stay: %v", err)
	}
}

// TestResumedSwapIsKeptInVersionHistory runs the startup order of
// InitPatchModule: the resume pass, the version history's adoption, the
// journal check. The finished swap's backups become a saved version, as a
// swap its helper committed does, and nothing is left to undo.
func TestResumedSwapIsKeptInVersionHistory(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	uhistKeep.Store(2)
	t.Cleanup(func() { updateResumeReplacedConfig.Store(false) })
	tag := "JukaHub-20261111002100"
	exeName := releaseExecutableName()
	install := t.TempDir()
	exe := filepath.Join(install, exeName)
	writeTree(t, install, map[string]string{
		exeName:             "old-binary",
		"Inter-Regular.ttf": "old-font",
		releaseInfoFileName: `{"tag":"JukaHub-20261110054938"}`,
	})
	staging, err := stagingDirFor(tag)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(staging, "JukaHub-Trimui")
	writeTree(t, root, newFolderRelease(tag))
	mustWrite(t, stagedReadyMarker(staging), tag+"\n")

	// The swap journals and notes what it replaces, as swapInstallFiles does,
	// and the old helper gets as far as the binary.
	files, err := stagedInstallFiles(root, install)
	if err != nil {
		t.Fatal(err)
	}
	j := newJournal("juka-update", tag)
	for _, f := range files {
		if err := j.recordReplace(f.Dest); err != nil {
			t.Fatal(err)
		}
	}
	uhistNoteSwap(j, install, tag, uhistReasonUpdate)
	mustWrite(t, exe, "new-binary")

	if n := resumeInterruptedUpdatesFor(exe); n != 1 {
		t.Fatalf("resumed %d swaps, want 1", n)
	}
	uhistAdoptCommittedSwaps()
	checkInterruptedJournal()

	entries, _ := uhistEntriesSnapshot()
	if len(entries) != 1 || entries[0].ReplacedBy != tag {
		t.Fatalf("saved versions = %+v, want the one the finished swap replaced", entries)
	}
	saved := ""
	for _, f := range entries[0].Files {
		if f.Name == exeName {
			saved = readFile(t, filepath.Join(dir, "versions", entries[0].ID, f.Stored))
		}
	}
	if saved != "old-binary" {
		t.Errorf("the saved version's binary = %q, want the build that was replaced", saved)
	}
	if got := readFile(t, filepath.Join(install, "Inter-Regular.ttf")); got != "new-font" {
		t.Errorf("the journal check undid the finished swap: Inter-Regular.ttf = %q", got)
	}
	if got := readReleaseInfoFromDir(install).Tag; got != tag {
		t.Errorf("installed stamp = %q, want %q", got, tag)
	}
}

func TestPruneStaleStagingTreesKeepsOtherWork(t *testing.T) {
	base := t.TempDir()
	for _, rel := range []string{
		"staging/app-JukaHub-1/JukaHub-Trimui/JukaHub",
		"staging/app-JukaHub-1.ready",
		"staging/app-JukaHub-2/JukaHub",
		"staging/pkg-cores/x",
		"staging/work-delta/y",
		"staging/rollback/apply-update.sh",
	} {
		mustWrite(t, filepath.Join(base, filepath.FromSlash(rel)), "x")
	}
	pruneStaleStagingTrees(base)
	for _, gone := range []string{"app-JukaHub-1", "app-JukaHub-1.ready", "app-JukaHub-2"} {
		if _, err := os.Stat(filepath.Join(base, "staging", gone)); !os.IsNotExist(err) {
			t.Errorf("stale %s survived", gone)
		}
	}
	for _, kept := range []string{"pkg-cores", "work-delta", "rollback"} {
		if _, err := os.Stat(filepath.Join(base, "staging", kept)); err != nil {
			t.Errorf("%s is not a stale release tree and must stay: %v", kept, err)
		}
	}
}

func TestSwapHelperLogTailAndParentDir(t *testing.T) {
	dir := t.TempDir()
	if got := swapHelperLogTail(dir); got != "" {
		t.Errorf("no log = %q, want empty", got)
	}
	mustWrite(t, filepath.Join(dir, swapHelperLogName), "one\n\ntwo\nthree\nfour\n")
	if got := swapHelperLogTail(dir); got != "two | three | four" {
		t.Errorf("tail = %q", got)
	}
	for in, want := range map[string]string{
		`C:\Games\JukaHub\bin\a.dll`:   `C:\Games\JukaHub\bin`,
		"/mnt/SDCARD/Apps/JukaHub/lib": "/mnt/SDCARD/Apps/JukaHub",
		"JukaHub":                      "",
	} {
		if got := swapParentDir(in); got != want {
			t.Errorf("swapParentDir(%q) = %q, want %q", in, got, want)
		}
	}
}
