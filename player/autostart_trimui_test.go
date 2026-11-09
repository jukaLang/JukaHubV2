package main

// autostart_trimui_test.go — the SD-card boot hook used on the handheld.
//
// These tests pin the platform explicitly and never write outside a temporary
// directory: the hook must not block the firmware's boot and must survive an
// install path with spaces and quotes in it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTrimuiAutostart points the boot hook at a temporary SD card for one test.
func useTrimuiAutostart(t *testing.T, root, exe string) {
	t.Helper()
	prevTrimui, prevRoot, prevExe := autostartIsTrimui, trimuiSDRootFn, autostartExeFn
	autostartIsTrimui = func() bool { return true }
	trimuiSDRootFn = func() string { return root }
	autostartExeFn = func() (string, error) { return exe, nil }
	t.Cleanup(func() {
		autostartIsTrimui, trimuiSDRootFn, autostartExeFn = prevTrimui, prevRoot, prevExe
	})
}

func TestTrimuiAutostartLifecycle(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "Apps", "JukaHub")
	exe := filepath.Join(appDir, "JukaHub")
	useTrimuiAutostart(t, root, exe)

	if autoStartInstalled() {
		t.Fatal("a fresh card already reports the hook")
	}
	if err := setAutoStart(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	path := filepath.Join(root, "System", "starts", trimuiAutostartFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hook not written: %v", err)
	}
	script := string(data)
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Errorf("hook does not start with a sh shebang:\n%s", script)
	}
	if !strings.Contains(script, "launch.sh") {
		t.Errorf("hook does not run launch.sh:\n%s", script)
	}
	if !strings.Contains(script, appDir) {
		t.Errorf("hook does not point at %q:\n%s", appDir, script)
	}
	// It must not block the firmware's boot sequence, and it must wait for the
	// stock launcher instead of racing it — but only for a bounded time.
	if strings.Contains(script, "sleep 10") {
		t.Errorf("hook waits a fixed ten seconds:\n%s", script)
	}
	if !strings.Contains(script, ") >/dev/null 2>&1 &") || !strings.HasSuffix(script, "exit 0\n") {
		t.Errorf("hook does not return immediately:\n%s", script)
	}
	if !strings.Contains(script, "pidof MainUI") {
		t.Errorf("hook does not wait for the stock launcher:\n%s", script)
	}
	if !strings.Contains(script, "-lt 30") {
		t.Errorf("the launcher wait is not bounded:\n%s", script)
	}
	if !autoStartInstalled() {
		t.Error("hook written but autoStartInstalled() is false")
	}

	// Only our file is touched: a neighbouring boot script must survive.
	other := filepath.Join(root, "System", "starts", "custom.sh")
	if err := os.WriteFile(other, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := setAutoStart(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("hook still present after disabling")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another boot script was removed: %v", err)
	}
	if err := setAutoStart(false); err != nil {
		t.Errorf("second disable: %v", err)
	}
}

func TestTrimuiHookQuotesAwkwardPaths(t *testing.T) {
	script := trimuiAutostartScript(`/mnt/SD CARD/Apps/Juka's Hub`)
	// The single quote in the folder name has to survive shell parsing.
	if !strings.Contains(script, `'/mnt/SD CARD/Apps/Juka'\''s Hub'`) {
		t.Errorf("path was not quoted for sh:\n%s", script)
	}
	// A newline stays inside the quoted value: it can never start a command of
	// its own, because the quote closes after it rather than before.
	danger := trimuiAutostartScript("/tmp/a\nexec rm -rf /")
	if !strings.Contains(danger, "APP_DIR='/tmp/a\nexec rm -rf /'") {
		t.Errorf("the newline did not stay inside the quoted value:\n%s", danger)
	}
	payload := strings.Index(danger, "exec rm -rf /")
	if payload < 0 {
		t.Fatalf("the payload vanished from the script:\n%s", danger)
	}
	// From the assignment to the payload the quote count must be odd: the
	// payload is then inside the quoted value, so the shell reads it as data
	// rather than as a command. (Measured from APP_DIR because the comments
	// above it contain apostrophes of their own.)
	assign := strings.Index(danger, "APP_DIR=")
	if assign < 0 || strings.Count(danger[assign:payload], "'")%2 == 0 {
		t.Errorf("the payload sits outside the quoted value:\n%s", danger)
	}
}
