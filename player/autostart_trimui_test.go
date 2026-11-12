package main

// autostart_trimui_test.go — the SD-card boot hook used on the handheld.
//
// These tests pin the platform explicitly and never write outside a temporary
// directory. The hook must not block the firmware's boot, must be safe when the
// firmware sources it instead of running it, must start JukaHub through the
// firmware's own queue (/tmp/cmd_to_run.sh) rather than by fighting the
// launcher loop, and must survive an install path with spaces and quotes.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// useTrimuiAutostart points the boot hook at a temporary SD card for one test.
func useTrimuiAutostart(t *testing.T, root, exe string) {
	t.Helper()
	prevTrimui, prevRoot, prevExe := autostartIsTrimui, trimuiSDRootFn, autostartExeFn
	prevLock, prevLauncher := trimuiAutostartLockDir, trimuiLauncherScript
	autostartIsTrimui = func() bool { return true }
	trimuiSDRootFn = func() string { return root }
	autostartExeFn = func() (string, error) { return exe, nil }
	// The boot report must read the test's card, never the host's /tmp.
	trimuiAutostartLockDir = filepath.Join(root, "tmp", "jukahub-autostart.lock")
	trimuiLauncherScript = filepath.Join(root, "usr", "trimui", "bin", "runtrimui.sh")
	t.Cleanup(func() {
		autostartIsTrimui, trimuiSDRootFn, autostartExeFn = prevTrimui, prevRoot, prevExe
		trimuiAutostartLockDir, trimuiLauncherScript = prevLock, prevLauncher
	})
}

// trimuiTestCard is a temporary card with JukaHub's folder on it.
func trimuiTestCard(t *testing.T) (root, appDir, hook string) {
	t.Helper()
	root = t.TempDir()
	appDir = filepath.Join(root, "Apps", "JukaHub")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	useTrimuiAutostart(t, root, filepath.Join(appDir, "JukaHub"))
	return root, appDir, filepath.Join(root, "System", "starts", trimuiAutostartFile)
}

// mustTrimuiScript is the hook for appDir, failing the test on an error.
func mustTrimuiScript(t *testing.T, appDir string) string {
	t.Helper()
	script, err := trimuiAutostartScript(appDir)
	if err != nil {
		t.Fatalf("trimuiAutostartScript(%q): %v", appDir, err)
	}
	return script
}

func TestTrimuiAutostartLifecycle(t *testing.T) {
	root, appDir, path := trimuiTestCard(t)

	if autoStartInstalled() {
		t.Fatal("a fresh card already reports the hook")
	}
	if err := setAutoStart(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hook not written: %v", err)
	}
	script := string(data)
	if script != mustTrimuiScript(t, appDir) {
		t.Errorf("the hook on the card is not this build's:\n%s", script)
	}
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Errorf("hook does not start with a sh shebang:\n%s", script)
	}
	if !strings.Contains(script, shellQuote(appDir+"/launch.sh")) {
		t.Errorf("hook does not queue %s/launch.sh:\n%s", appDir, script)
	}
	if !autoStartInstalled() {
		t.Error("hook written but autoStartInstalled() is false")
	}

	// Writing it again (a refresh) leaves nothing else in System/starts — the
	// firmware may run every file there — nor the staging copy in System.
	if err := setAutoStart(true); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != trimuiAutostartFile {
			t.Errorf("System/starts gained %q next to the hook", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "System", trimuiAutostartStagingFile)); !os.IsNotExist(err) {
		t.Error("the staging copy of the hook was left in System")
	}

	// Only our file is touched: a neighbouring boot script must survive. The
	// pending marker of an earlier start goes with the hook.
	other := filepath.Join(root, "System", "starts", "custom.sh")
	if err := os.WriteFile(other, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(appDir, trimuiAutostartPendingFile)
	if err := os.WriteFile(pending, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setAutoStart(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("hook still present after disabling")
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Error("the pending marker survived turning the setting off")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another boot script was removed: %v", err)
	}
	if err := setAutoStart(false); err != nil {
		t.Errorf("second disable: %v", err)
	}
}

// The firmware may source the hook instead of running it. Then a top-level
// exit would end the firmware's own launcher script (no MainUI, no apps), and a
// top-level variable would land in its shell. Everything must be inside the
// subshell, which also resets the firmware shell's errexit/nounset/IFS.
func TestTrimuiHookIsSafeToSource(t *testing.T) {
	script := mustTrimuiScript(t, "/mnt/SDCARD/Apps/JukaHub")
	lines := strings.Split(strings.TrimSuffix(script, "\n"), "\n")
	open := -1
	for i, line := range lines {
		if line == "(" {
			open = i
			break
		}
		if !strings.HasPrefix(line, "#") {
			t.Fatalf("line %d runs in the firmware's shell: %q", i+1, line)
		}
	}
	if open < 0 {
		t.Fatalf("the hook has no subshell:\n%s", script)
	}
	if got := lines[open+1]; got != "set +e +u +f" {
		t.Errorf("the subshell does not reset errexit/nounset/noglob first (got %q)", got)
	}
	if last := lines[len(lines)-1]; last != ") </dev/null >/dev/null 2>&1" {
		t.Errorf("the hook does not end with its subshell (last line %q)", last)
	}
	// The waiting runs in a background subshell, so the boot is never held.
	if !strings.Contains(script, "\n) </dev/null >/dev/null 2>&1 &\n") {
		t.Errorf("the waiting part is not backgrounded:\n%s", script)
	}
}

// The hook starts JukaHub the way the stock menu starts an app, and never with
// the moves that made the first hook fail.
func TestTrimuiHookUsesTheFirmwareQueue(t *testing.T) {
	script := mustTrimuiScript(t, "/mnt/SDCARD/Apps/JukaHub")
	for _, want := range [][2]string{
		{"Q=/tmp/cmd_to_run.sh", "the firmware's launch queue"},
		{"LOCK=/tmp/jukahub-autostart.lock", "the once-per-boot lock"},
		{`if ! mkdir "$LOCK" 2>/dev/null; then`, "taking the lock atomically"},
		{`PENDING="$APP_DIR/.autostart-pending"`, "the boot-loop guard"},
		{`[ "$st_state" = Z ] || printf`, "a zombie MainUI does not count as running"},
		{`[ "$state" = other ] && give_up`, "backing off for another queued app"},
		{`[ -n "$(pids JukaHub)" ] && give_up`, "backing off for a running JukaHub"},
		{"took the queue with it", "backing off when the menu closes with the queue"},
		{`[ "$n" -ge $((120 * PER)) ] && give_up`, "the bounded wait for the menu"},
		{`kill -9 $ui`, "closing MainUI, which ignores SIGTERM"},
		{`ln "$Q.jukahub" "$Q"`, "queueing only where nothing is queued"},
		{`mkdir "$RAN" 2>/dev/null || { resume; started; }`, "the direct start claiming the start"},
		{`[ -n "$paused" ] && kill -STOP $paused`, "the direct start pausing the loop by pid"},
		{`kill -CONT $paused`, "the direct start letting the loop go again"},
		{`trap 'resume; withdraw' TERM INT`, "never leaving the loop paused when stopped"},
		{"sh | ash | bash | dash | busybox | *.sh | trimui_launcher)", "pausing only a shell-script loop"},
		{"*/sh | */ash | */bash | */dash | */busybox) out=", "knowing a loop with a long name by its executable"},
		{`[ -n "$loose" ] && give_up`, "refusing when something it cannot pause restarts MainUI"},
		{`busy $paused && { resume; give_up`, "refusing when the loop is already starting an app"},
		{`[ "$io" = "$last_io" ] && break`, "waiting for MainUI to stop writing"},
		{`GO="$LOCK/go"`, "the go token the queued command needs"},
		{`[ -f "$Q" ] && [ ! -s "$Q" ] && rm -f "$Q"`, "treating an empty queue as none"},
		{`[ "$old" != "$BOOT" ]`, "clearing a lock left by an earlier boot"},
		{"if sleep 0.1 2>/dev/null; then STEP=0.5", "the busybox-without-fractions fallback"},
		{`say "boot hook started (run as $0)"`, "logging whether it was run or sourced"},
		{`record $paused`, "writing the paused loop down for launch.sh to resume"},
		{`PAUSED=/tmp/jukahub-paused`, "the paused record launch.sh reads"},
		{`: >"$MENU" 2>/dev/null`, "noting that the menu was on screen before it was closed"},
		{`[ -n "$same" ] && break`, "not showing the menu a third time to a loop that ignores the queue"},
	} {
		if !strings.Contains(script, want[0]) {
			t.Errorf("hook lacks %s (%q)", want[1], want[0])
		}
	}
	// The launcher loop is never looked up by name: whether pkill exists, and
	// what the loop's process is called, depends on the busybox build.
	if strings.Contains(script, "pkill") || strings.Contains(script, "killall") {
		t.Error("hook controls processes by name")
	}
	if strings.Contains(script, "sleep 10") {
		t.Error("hook waits a fixed ten seconds")
	}
	// The queued command may be sourced by the loop too: no exit, no exec.
	start := strings.Index(script, "<<'JUKAHUB_QUEUE'")
	end := strings.Index(script, "\nJUKAHUB_QUEUE\n")
	if start < 0 || end < start {
		t.Fatal("the queued command is missing")
	}
	queued := script[start:end]
	for _, bad := range []string{"exit", "exec "} {
		if strings.Contains(queued, bad) {
			t.Errorf("the queued command uses %q:\n%s", bad, queued)
		}
	}
	for _, want := range [][2]string{
		{"if [ -e /tmp/jukahub-autostart.lock/go ] && mkdir /tmp/jukahub-autostart.ran 2>/dev/null; then", "needs the go token and claims the start once per boot"},
		{"/bin/sh '/mnt/SDCARD/Apps/JukaHub/launch.sh' || :", "runs launch.sh without failing a set -e loop"},
		{"rm -f '/mnt/SDCARD/Apps/JukaHub/.autostart-pending'", "drops the marker once JukaHub exits"},
		{"dd if=/dev/zero of=/dev/fb0 bs=65536 count=512 2>/dev/null || :", "blanks the closed menu without failing a set -e loop"},
	} {
		if !strings.Contains(queued, want[0]) {
			t.Errorf("the queued command no longer %s:\n%s", want[1], queued)
		}
	}
	if strings.Contains(script, "`") {
		t.Error("the hook contains a backquote")
	}
}

func TestTrimuiHookQuotesAwkwardPaths(t *testing.T) {
	dir := `/mnt/SD CARD/Apps/Juka's Hub`
	script := mustTrimuiScript(t, dir)
	// The single quote in the folder name has to survive shell parsing, in the
	// hook and in the command it queues.
	if !strings.Contains(script, `APP_DIR='/mnt/SD CARD/Apps/Juka'\''s Hub'`+"\n") {
		t.Errorf("APP_DIR was not quoted for sh:\n%s", script)
	}
	if !strings.Contains(script, `/bin/sh '/mnt/SD CARD/Apps/Juka'\''s Hub/launch.sh' || :`+"\n") {
		t.Errorf("the queued launch.sh path was not quoted for sh:\n%s", script)
	}
	if !strings.Contains(script, `rm -f '/mnt/SD CARD/Apps/Juka'\''s Hub/.autostart-pending'`+"\n") {
		t.Errorf("the queued marker path was not quoted for sh:\n%s", script)
	}
	// A control character would cut the here-document or a log line short.
	for _, bad := range []string{"/tmp/a\nexec rm -rf /", "/tmp/a\rb", "/tmp/a\tb", ""} {
		if s, err := trimuiAutostartScript(bad); err == nil {
			t.Errorf("trimuiAutostartScript(%q) gave a hook:\n%s", bad, s)
		}
	}
}

// The hook must parse in the shells a firmware may have. A syntax error in a
// sourced file would end the firmware's launcher script.
func TestTrimuiHookParses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell")
	}
	file := filepath.Join(t.TempDir(), trimuiAutostartFile)
	if err := os.WriteFile(file, []byte(mustTrimuiScript(t, `/mnt/SDCARD/Apps/Juka's Hub`)), 0o755); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, shell := range [][]string{{"sh"}, {"dash"}, {"busybox", "sh"}, {"bash", "--posix"}} {
		bin, err := exec.LookPath(shell[0])
		if err != nil {
			continue
		}
		args := append(append([]string{}, shell[1:]...), "-n", file)
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", strings.Join(shell, " "), err, out)
		}
		ran++
	}
	if ran == 0 {
		t.Skip("no shell found")
	}
}

// A card that still carries a hook from another build gets this build's at
// the next start, and a current hook is left as it is.
func TestReconcileRefreshesAStaleTrimuiHook(t *testing.T) {
	_, appDir, path := trimuiTestCard(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "#!/bin/sh\nAPP_DIR='/mnt/SDCARD/Apps/JukaHub'\n[ -d \"$APP_DIR\" ] || exit 0\nexit 0\n"
	if err := os.WriteFile(path, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"autoStart": "true", autoStartDefaultedKey: true}

	reconcileAutoStart(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != mustTrimuiScript(t, appDir) {
		t.Fatalf("the stale hook was not replaced:\n%s", data)
	}
	if trimuiAutostartHookStale() {
		t.Fatal("the hook still counts as stale after the refresh")
	}

	// Current: not rewritten (the file stays the very same one).
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reconcileAutoStart(cfg)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Error("a current hook was rewritten")
	}
}

// JukaHub clears the hook's pending marker once it has been up for a while,
// and not before: a JukaHub that hangs at start keeps the marker, and the next
// boot stays on the stock menu.
func TestAutoStartBootSettleClearsThePendingMarker(t *testing.T) {
	_, appDir, _ := trimuiTestCard(t)
	prevSince, prevSettled, prevCleanup := autoStartSettleSince, autoStartSettled, Cleanup
	autoStartSettleSince, autoStartSettled = time.Time{}, false
	t.Cleanup(func() { autoStartSettleSince, autoStartSettled, Cleanup = prevSince, prevSettled, prevCleanup })

	marker := filepath.Join(appDir, trimuiAutostartPendingFile)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	autoStartBootSettleAt(t0)
	autoStartBootSettleAt(t0.Add(autoStartSettleDelay - time.Second))
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the marker was cleared before JukaHub had been up long enough")
	}
	autoStartBootSettleAt(t0.Add(autoStartSettleDelay))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the marker survived JukaHub being up")
	}
	if !autoStartSettled {
		t.Fatal("the settle check would keep running")
	}
}

// Leaving JukaHub before then (quit, restart, power off) clears the marker as
// well: a JukaHub that exits hands the device back, it did not lock it up.
func TestAutoStartExitClearsThePendingMarker(t *testing.T) {
	_, appDir, _ := trimuiTestCard(t)
	prevSince, prevSettled, prevCleanup := autoStartSettleSince, autoStartSettled, Cleanup
	autoStartSettleSince, autoStartSettled, Cleanup = time.Time{}, false, nil
	t.Cleanup(func() { autoStartSettleSince, autoStartSettled, Cleanup = prevSince, prevSettled, prevCleanup })

	marker := filepath.Join(appDir, trimuiAutostartPendingFile)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	autoStartBootSettleAt(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	if len(Cleanup) != 1 {
		t.Fatalf("the first tick registered %d cleanup hooks, want 1", len(Cleanup))
	}
	Cleanup[0]()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("leaving JukaHub kept the marker")
	}
}

// The boot report says when the firmware never ran the hook (and whether its
// launcher loop mentions System/starts), and otherwise quotes the hook.
func TestTrimuiAutostartBootNote(t *testing.T) {
	root, appDir, _ := trimuiTestCard(t)
	launcher := filepath.Join(root, "usr", "trimui", "bin", "runtrimui.sh")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	loop := "#!/bin/sh\nfor f in /mnt/SDCARD/System/starts/*.sh; do sh \"$f\"; done\n"
	if err := os.WriteFile(launcher, []byte(loop), 0o755); err != nil {
		t.Fatal(err)
	}
	got := trimuiAutostartBootNote()
	if !strings.Contains(got, "did not run at this boot") || !strings.Contains(got, "mentions System/starts") {
		t.Fatalf("without the hook's lock: %q", got)
	}

	if err := os.MkdirAll(filepath.Join(root, "tmp", "jukahub-autostart.lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := "[2026-11-10 08:00:00 boot+9.1s] boot hook started (run as /x)\n" +
		"[2026-11-10 08:00:03 boot+12.4s] the firmware started JukaHub (before the menu opened)\n"
	if err := os.WriteFile(filepath.Join(appDir, trimuiAutostartLogFile), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := trimuiAutostartBootNote(), "boot hook: the firmware started JukaHub (before the menu opened)"; got != want {
		t.Fatalf("with the lock and a log: %q, want %q", got, want)
	}
}

// The boot report quotes the hook's last word without its timestamp, and skips
// the firmware lines it copied into the log.
func TestLastLogLine(t *testing.T) {
	log := filepath.Join(t.TempDir(), trimuiAutostartLogFile)
	text := "[2026-11-10 08:00:00 boot+9.1s] boot hook started (run as /x)\n" +
		"[2026-11-10 08:00:00 boot+9.2s] queued JukaHub for the firmware's launcher\n" +
		"    runtrimui.sh:42:for f in /mnt/SDCARD/System/starts/*.sh; do\n" +
		"[2026-11-10 08:00:03 boot+12.4s] the firmware started JukaHub (after the menu was closed)\n" +
		"    runtrimui.sh:57:    ./MainUI\n\n"
	if err := os.WriteFile(log, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := lastLogLine(log), "the firmware started JukaHub (after the menu was closed)"; got != want {
		t.Fatalf("lastLogLine = %q, want %q", got, want)
	}
	if got := lastLogLine(filepath.Join(t.TempDir(), "missing.log")); got != "" {
		t.Fatalf("a missing log gave %q", got)
	}
}
