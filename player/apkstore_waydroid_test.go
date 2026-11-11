package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApkStoreProbeWaydroid(t *testing.T) {
	if IsWindows() {
		if w := apkStoreProbeWaydroid(apkStoreWaydroidHost{}); w.usable() || w.reason == "" {
			t.Errorf("Windows: %+v", w)
		}
		return
	}
	none := apkStoreWaydroidHost{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		exists:   func(string) bool { return true },
	}
	if w := apkStoreProbeWaydroid(none); w.usable() || w.reason == "" {
		t.Errorf("no binary: %+v", w)
	}
	noBinder := apkStoreWaydroidHost{
		lookPath: func(string) (string, error) { return "/usr/bin/waydroid", nil },
		exists:   func(string) bool { return false },
	}
	if w := apkStoreProbeWaydroid(noBinder); w.usable() || w.binder {
		t.Errorf("no binder: %+v", w)
	}
	ready := apkStoreWaydroidHost{
		lookPath: func(string) (string, error) { return "/usr/bin/waydroid", nil },
		exists:   func(p string) bool { return p == "/dev/binderfs/binder" },
		run: func(_ context.Context, _ string, args ...string) (string, error) {
			return "Session:\tRUNNING\nContainer:\tRUNNING\n", nil
		},
	}
	if w := apkStoreProbeWaydroid(ready); !w.usable() || !w.running || !w.canInstall() || w.reason != "" {
		t.Errorf("ready: %+v", w)
	}
	stopped := ready
	stopped.run = func(context.Context, string, ...string) (string, error) { return "Session:\tSTOPPED\n", nil }
	if w := apkStoreProbeWaydroid(stopped); !w.usable() || w.running || w.canInstall() {
		t.Errorf("stopped session: install must not be offered: %+v", w)
	}
}

// Waydroid exits 0 when it could not install (session stopped) and installs
// asynchronously, so success is only what its app list confirms.
func TestApkStoreWaydroidInstallOutcomes(t *testing.T) {
	if IsWindows() {
		t.Skip("Waydroid is Linux-only")
	}
	apk := filepath.Join(t.TempDir(), "game.apk")
	if err := os.WriteFile(apk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPause := apkStoreWaydroidListPause
	apkStoreWaydroidListPause = time.Millisecond
	t.Cleanup(func() { apkStoreWaydroidListPause = oldPause })
	host := func(status, install string, installErr error, list string) apkStoreWaydroidHost {
		return apkStoreWaydroidHost{run: func(_ context.Context, _ string, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "status":
				return status, nil
			case "app list":
				return list, nil
			}
			return install, installErr
		}}
	}
	running := "Session:\tRUNNING\n"
	listed := "Name: Game\npackageName: com.example.game\n"
	cases := []struct {
		name    string
		host    apkStoreWaydroidHost
		pkg     string
		want    apkStoreWaydroidResult
		wantErr bool
	}{
		{"stopped before install", host("Session:\tSTOPPED\n", "", nil, listed), "com.example.game", 0, true},
		{"exit 0 but stopped", host(running, "[11:02:03] WayDroid session is stopped\n", nil, ""), "com.example.game", 0, true},
		{"exit status", host(running, "boom", errors.New("exit status 1"), ""), "com.example.game", 0, true},
		{"listed", host(running, "", nil, listed), "com.example.game", apkWaydroidInstalled, false},
		{"not listed", host(running, "", nil, "packageName: com.other\n"), "com.example.game", apkWaydroidHandedOver, false},
		{"no package known", host(running, "", nil, listed), "", apkWaydroidHandedOver, false},
	}
	for _, c := range cases {
		got, err := apkStoreWaydroidInstall(context.Background(), c.host, "/usr/bin/waydroid", apk, c.pkg)
		if (err != nil) != c.wantErr || err == nil && got != c.want {
			t.Errorf("%s: result %v err %v", c.name, got, err)
		}
	}
	long := "first\nsecond\n" + strings.Repeat("noise ", 200) + "ERROR: final words"
	if msg := apkStoreWaydroidLastLines(long); len(msg) > apkStoreWaydroidMessageMax+len("…") ||
		!strings.HasSuffix(msg, "final words") || strings.Contains(msg, "first") {
		t.Errorf("toast text = %q", msg)
	}
}

func TestApkStoreWaydroidArgs(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "game.apk")
	if err := os.WriteFile(good, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := apkStoreWaydroidArgs(good)
	if err != nil || len(args) != 3 || args[0] != "app" || args[1] != "install" || args[2] != filepath.Clean(good) {
		t.Errorf("args %v err %v", args, err)
	}
	dash := filepath.Join(dir, "-rf.apk")
	if err := os.WriteFile(dash, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"game.apk", dash, filepath.Join(dir, "missing.apk"), dir, filepath.Join(dir, "notes.txt")} {
		if _, err := apkStoreWaydroidArgs(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if !apkStoreWaydroidRunning("Session: RUNNING") || apkStoreWaydroidRunning("Session: STOPPED") || apkStoreWaydroidRunning("") {
		t.Error("session parsing")
	}
}
