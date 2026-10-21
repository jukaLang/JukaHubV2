package main

// launch_handover_test.go covers the restart handshake between JukaHub, the
// swap helper and the device launcher.
//
// The bug it pins: after a self-update the launcher that started the old build
// restored MainUI as that build exited, while the helper started the new build
// through a second launcher. Both UIs then drew to the same framebuffer and the
// screen blinked between them. The helper now either hands the restart to a
// protocol-aware launcher or waits for the old one to finish, and the launcher
// runs the new build in place.

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeLauncherScript is the launcher stub: it records that it ran, so a test
// can tell an in-place restart from a relaunch.
const fakeLauncherScript = "#!/bin/sh\necho launched >> launch.log\n"

// fakeLauncherPID starts a shell that reports its own pid and then does body.
// The pid comes from the shell (`$$`), not from Go, so `kill -0` works from the
// helper on every host — a Windows pid is invisible to Git Bash's sh.
func fakeLauncherPID(t *testing.T, dir, body string) int {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX sh on this host")
	}
	cmd := exec.Command(sh, "-c", "echo $$; "+body)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("launcher did not report its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || pid <= 0 {
		t.Fatalf("launcher reported pid %q", line)
	}
	return pid
}

// swapHelperFixture stages one file for replacement. Paths are relative and
// slash-separated so the generated POSIX script can use them on any host.
func swapHelperFixture(t *testing.T) (tmp, script string, files []stagedInstallFile) {
	t.Helper()
	tmp = t.TempDir()
	for _, d := range []string{"staged", "install", "state"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tmp, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("staged/JukaHub", "new-binary")
	write("install/JukaHub", "old-binary")
	write("install/launch.sh", fakeLauncherScript)
	write("state/journal.json", "{}")

	files = []stagedInstallFile{{
		Src:  "staged/JukaHub",
		Name: "JukaHub",
		Dest: "install/JukaHub",
	}}
	return tmp, "apply-update.sh", files
}

// runSwapHelper executes the generated helper with its startup sleep replaced
// by a no-op. filepath separators are normalised because the host that
// generated the script may be Windows while the script targets /bin/sh.
func runSwapHelper(t *testing.T, tmp, script string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX sh on this host")
	}
	raw, err := os.ReadFile(filepath.Join(tmp, script))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "sleep 2\n", ":\n", 1)
	body = strings.ReplaceAll(body, `\`, "/")
	if err := os.WriteFile(filepath.Join(tmp, script), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, script)
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("swap helper failed: %v\n%s", err, out)
	}
}

// waitForLog waits for the fake launcher to record its run.
func waitForLog(t *testing.T, path string, lines int) []string {
	t.Helper()
	for i := 0; i < 100; i++ {
		raw, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) >= lines {
				return fields
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, _ := os.ReadFile(path)
	t.Fatalf("launcher log %s never reached %d lines (has %q)", path, lines, raw)
	return nil
}

// TestSwapHelperHandsRestartToLauncher is the blink fix: with the pending
// marker in place and a live launcher, the helper swaps the files, reports the
// verdict and leaves the restart to the launcher instead of starting a second
// one next to the still-running MainUI.
func TestSwapHelperHandsRestartToLauncher(t *testing.T) {
	tmp, script, files := swapHelperFixture(t)
	// A launcher that stays alive for the duration of the swap.
	pid := fakeLauncherPID(t, tmp, "exec sleep 20")
	if err := writeSwapScriptFor("linux", filepath.Join(tmp, script), "install/JukaHub", files, "state/journal.json", pid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "install", restartPendingName), []byte("restart\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runSwapHelper(t, tmp, script)

	if got, err := os.ReadFile(filepath.Join(tmp, "install", "JukaHub")); err != nil || string(got) != "new-binary" {
		t.Fatalf("binary = %q, %v; want the staged build", got, err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "state", "journal.json")); !os.IsNotExist(err) {
		t.Error("journal must be committed (removed) after a successful swap")
	}
	if _, err := os.Stat(filepath.Join(tmp, "install", restartSwappedName)); err != nil {
		t.Errorf("helper did not report a completed swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "install", restartFailedName)); err == nil {
		t.Error("helper reported a failure for a successful swap")
	}
	if _, err := os.Stat(filepath.Join(tmp, "install", restartPendingName)); err != nil {
		t.Error("the pending marker belongs to the launcher; the helper must leave it")
	}
	if _, err := os.Stat(filepath.Join(tmp, "launch.log")); err == nil {
		t.Error("helper started a second launcher while the first one was handling the restart")
	}
}

// TestSwapHelperLaunchesAfterTheOldOneExits covers a device whose launcher is
// older than the protocol: nothing will restart the app in place, so the helper
// must wait for the previous launcher to finish restoring MainUI and only then
// start a replacement.
func TestSwapHelperLaunchesAfterTheOldOneExits(t *testing.T) {
	tmp, script, files := swapHelperFixture(t)
	pid := fakeLauncherPID(t, tmp, "sleep 1; echo old-exited >> launch.log")
	if err := writeSwapScriptFor("linux", filepath.Join(tmp, script), "install/JukaHub", files, "state/journal.json", pid); err != nil {
		t.Fatal(err)
	}

	runSwapHelper(t, tmp, script)

	fields := waitForLog(t, filepath.Join(tmp, "launch.log"), 2)
	if len(fields) < 2 || fields[0] != "old-exited" || fields[1] != "launched" {
		t.Fatalf("launch order = %v; want the old launcher to exit before the new one starts", fields)
	}
	if _, err := os.Stat(filepath.Join(tmp, "install", restartPendingName)); err == nil {
		t.Error("helper left the pending marker behind")
	}
}

// TestLauncherScriptsStayInSync keeps the release archive and the SD-card
// package shipping the same launcher. They diverged before: a release shipped
// the old script that never stopped MainUI, and updating replaced the device's
// launcher with it.
func TestLauncherScriptsStayInSync(t *testing.T) {
	releasePath := "launch.sh"
	devicePath := filepath.Join("trimui", "Apps", "JukaHub", "launch.sh")
	release, err := os.ReadFile(releasePath)
	if err != nil {
		t.Fatalf("read %s: %v", releasePath, err)
	}
	device, err := os.ReadFile(devicePath)
	if err != nil {
		t.Fatalf("read %s: %v", devicePath, err)
	}
	if string(release) != string(device) {
		t.Fatalf("%s and %s differ; the release archive and the SD-card package must ship the same launcher", releasePath, devicePath)
	}
	body := string(release)
	if strings.Contains(body, "\r") {
		t.Error("launcher has CRLF line endings; /bin/sh fails on the device")
	}
	for _, want := range []string{
		launcherRestartProtocolToken,
		restartPendingName,
		restartSwappedName,
		restartFailedName,
		// The restart loop and the display verification are the two halves of
		// the blink fix; losing either one brings it back.
		"while :; do",
		"update swap in progress",
		"FATAL: MainUI still holds the display",
		"JUKAHUB_LAUNCHED_BY=launch.sh",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("launcher is missing %q", want)
		}
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		if out, err := exec.Command(sh, "-n", releasePath).CombinedOutput(); err != nil {
			t.Errorf("launcher does not parse: %v\n%s", err, out)
		}
	}
}

// TestDeviceLauncherProtocolDetection pins the on-disk check: only a launcher
// that advertises the protocol may receive the marker, because writing one for
// an old launcher would leave the device without a restart.
func TestDeviceLauncherProtocolDetection(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.sh")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n./JukaHub &> errors.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if deviceLauncherSupportsInPlaceRestart(plain) {
		t.Error("a launcher without the protocol was treated as protocol-aware")
	}
	aware := filepath.Join(dir, "aware.sh")
	if err := os.WriteFile(aware, []byte("#!/bin/sh\n# "+launcherRestartProtocolToken+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !deviceLauncherSupportsInPlaceRestart(aware) {
		t.Error("a launcher with the protocol token was not recognised")
	}
	if deviceLauncherSupportsInPlaceRestart(filepath.Join(dir, "missing.sh")) {
		t.Error("a missing launcher must not be treated as protocol-aware")
	}
}

// TestPrepareLauncherRestart covers the marker write: only a protocol-aware
// launcher started by launch.sh gets one.
func TestPrepareLauncherRestart(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "launch.sh"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write("#!/bin/sh\n./JukaHub\n")
	t.Setenv("JUKAHUB_LAUNCHED_BY", "launch.sh")
	if marker := prepareLauncherRestart(dir); marker != "" {
		t.Errorf("old launcher got a marker (%s); it would never read it", marker)
	}

	write("#!/bin/sh\n# " + launcherRestartProtocolToken + "\n")
	t.Setenv("JUKAHUB_LAUNCHED_BY", "")
	if marker := prepareLauncherRestart(dir); marker != "" {
		t.Errorf("marker written for a process that launch.sh did not start (%s)", marker)
	}

	t.Setenv("JUKAHUB_LAUNCHED_BY", "launch.sh")
	marker := prepareLauncherRestart(dir)
	if marker == "" {
		t.Fatal("no marker for a protocol-aware launcher started by launch.sh")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker path %s does not exist: %v", marker, err)
	}
}

// TestClearStaleRestartMarkers covers the cleanup at startup: a marker from a
// run killed mid-update must not make the next helper wait for a launcher loop
// that is not running.
func TestClearStaleRestartMarkers(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{restartPendingName, restartSwappedName, restartFailedName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "jukaHub")
	if err := os.WriteFile(keep, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	clearStaleRestartMarkersIn(dir)
	for _, name := range []string{restartPendingName, restartSwappedName, restartFailedName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s survived the startup cleanup", name)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("cleanup removed an unrelated file: %v", err)
	}
}
