package main

// gamediag_crash_test.go — the finding a launch that ended on a signal gets.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gameDiagCrashTestLog is the end of the RetroArch log of the two N64 reports
// (JH-N8NGPX, JH-RW63TU): the core starts its recompiler and the CPU stops it.
const gameDiagCrashTestLog = `[INFO] [Environ]: SYSTEM_DIRECTORY: "./.retroarch/system".
core loading dynamic libretro core from: "/mnt/SDCARD/RetroArch/.retroarch/cores/mupen64plus_libretro.so"
[libretro INFO] mupen64plus: Memory initialized
[libretro INFO] mupen64plus: Starting R4300 emulator: Dynamic Recompiler
[libretro INFO] mupen64plus: Init new dynarec
[WARN] [SDL]: Couldn't open haptic device of the joypad #0: Haptic: There are 0 haptic devices available
`

func gameDiagCrashTestWrite(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func gameDiagCrashTestLaunch(dir, logPath string) *gameDiagLaunch {
	return &gameDiagLaunch{
		game:     libraryGame{Path: filepath.Join(dir, "塞爾達傳說 時之笛.z64"), System: "Nintendo 64", Title: "塞爾達傳說 時之笛"},
		launcher: filepath.Join(dir, "launch.sh"),
		logPath:  logPath,
	}
}

func TestGameDiagFatalSignal(t *testing.T) {
	for _, sig := range []int{4, 6, 7, 8, 11} {
		if !gameDiagFatalSignal(sig) {
			t.Errorf("signal %d is a crash", sig)
		}
	}
	for _, sig := range []int{0, 1, 2, 9, 15} {
		if gameDiagFatalSignal(sig) {
			t.Errorf("signal %d is not a crash of the program's own", sig)
		}
	}
	if got := gameDiagCrashSignalName(4); got != "SIGILL" {
		t.Errorf("signal 4 = %q, want SIGILL", got)
	}
	if got := gameDiagCrashSignalName(12); got != "signal 12" {
		t.Errorf("signal 12 = %q, want signal 12", got)
	}
}

// A game the emulator's recompiler stopped: the card says so, names the core
// and the signal, and says the build cannot run here.
func TestGameDiagCrashIssueRecompiler(t *testing.T) {
	dir := t.TempDir()
	logPath := gameDiagCrashTestWrite(t, dir, "retroarch.log", gameDiagCrashTestLog)
	d := gameDiagCrashTestLaunch(dir, logPath)
	issue, ok := d.gameDiagCrashIssue(4)
	if !ok {
		t.Fatal("no finding for a SIGILL launch")
	}
	if issue.Level != int(gameDiagSure) {
		t.Errorf("level = %d, want gameDiagSure: %+v", issue.Level, issue)
	}
	if !strings.Contains(issue.Title, "crashed (signal 4, SIGILL)") {
		t.Errorf("title = %q, want the signal named", issue.Title)
	}
	if !strings.Contains(strings.ToLower(issue.Title), "mupen64plus") {
		t.Errorf("title = %q, want the core that crashed", issue.Title)
	}
	for _, want := range []string{"dynamic recompiler", "Game options", "Emulators → Core builds"} {
		if !strings.Contains(issue.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, issue.Detail)
		}
	}
	if strings.Contains(issue.Detail, "nothing JukaHub checks") {
		t.Errorf("the recompiler's finding is not the generic one: %s", issue.Detail)
	}
	// A build JukaHub just installed that ends this way is still put back by
	// its probation: the finding has to read as one about the core.
	if !gameDiagCoreIssueTitle(issue.Title) {
		t.Errorf("title %q does not count as a core issue (corebuilds_probation.go)", issue.Title)
	}
	if outcome, _ := gameDiagCoreOutcome(gameDiagReport{Issues: []launchIssue{issue}}, false, 132, 4); outcome != coreRunFailed {
		t.Errorf("outcome = %v, want coreRunFailed: the build must still be rolled back", outcome)
	}
}

// A standalone emulator's words land in the launcher's own output, with no
// RetroArch log at all (JH-N8NGPX).
func TestGameDiagCrashIssueLauncherOutput(t *testing.T) {
	dir := t.TempDir()
	outPath := gameDiagCrashTestWrite(t, dir, "launcher.log", "[libretro INFO] mupen64plus: Init new dynarec\nIllegal instruction\n")
	d := gameDiagCrashTestLaunch(dir, "")
	d.outPath = outPath
	issue, ok := d.gameDiagCrashIssue(4)
	if !ok {
		t.Fatal("no finding for a SIGILL launch")
	}
	if !strings.Contains(issue.Detail, "dynamic recompiler") {
		t.Errorf("detail lacks the recompiler: %s", issue.Detail)
	}
}

// A crash that is not a recompiler trapping gets the emulator's own end, not
// the build's: the player is pointed at another emulator.
func TestGameDiagCrashIssueOtherCrash(t *testing.T) {
	dir := t.TempDir()
	logPath := gameDiagCrashTestWrite(t, dir, "retroarch.log", "[INFO] [Core]: Content ran for a while.\n")
	d := gameDiagCrashTestLaunch(dir, logPath)
	issue, ok := d.gameDiagCrashIssue(11)
	if !ok {
		t.Fatal("no finding for a SIGSEGV launch")
	}
	if !strings.Contains(issue.Title, "signal 11, SIGSEGV") {
		t.Errorf("title = %q, want the signal named", issue.Title)
	}
	if !strings.Contains(issue.Detail, "nothing JukaHub checks") {
		t.Errorf("detail = %s, want the emulator's own end", issue.Detail)
	}
	if strings.Contains(issue.Detail, "dynamic recompiler") {
		t.Errorf("detail = %s, want no recompiler for a plain crash", issue.Detail)
	}
}

// A stop is not a crash: nothing is added for a signal that is not the
// program's own.
func TestGameDiagCrashAddIgnoresStops(t *testing.T) {
	dir := t.TempDir()
	logPath := gameDiagCrashTestWrite(t, dir, "retroarch.log", gameDiagCrashTestLog)
	d := gameDiagCrashTestLaunch(dir, logPath)
	for _, sig := range []int{0, 9, 15} {
		report := gameDiagReport{Issues: []launchIssue{{Level: int(gameDiagSure), Title: "The game file is gone"}}}
		if d.gameDiagCrashAdd(&report, sig) {
			t.Errorf("signal %d added a crash finding", sig)
		}
		if len(report.Issues) != 1 {
			t.Errorf("signal %d changed the findings: %+v", sig, report.Issues)
		}
	}
}

// The finding goes in beside what JukaHub already found, after a certain one.
func TestGameDiagCrashAddKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	logPath := gameDiagCrashTestWrite(t, dir, "retroarch.log", gameDiagCrashTestLog)
	d := gameDiagCrashTestLaunch(dir, logPath)
	report := gameDiagReport{Issues: []launchIssue{
		{Level: int(gameDiagSure), Title: "The game file is gone"},
		{Level: int(gameDiagMaybe), Title: "An .n64 file may not load"},
	}}
	if !d.gameDiagCrashAdd(&report, 4) {
		t.Fatal("no finding for a SIGILL launch")
	}
	if len(report.Issues) != 3 {
		t.Fatalf("findings = %+v, want three", report.Issues)
	}
	if report.Issues[len(report.Issues)-1].Level != int(gameDiagMaybe) {
		t.Errorf("order = %+v, want the crash before the maybe", report.Issues)
	}
}
