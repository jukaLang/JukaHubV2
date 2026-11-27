package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// WestonPack 0.2.7 evals its app words. A direct WINEDLLOVERRIDES value with
// a semicolon made it run winemenubuilder.exe=d as a command and return home.
// The private script must carry that value, the app's spaced folder and every
// argument literally, and preserve the app's exit status after Weston exits 0.
func TestWestonLaunchScriptKeepsAppWordsAndStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	root := t.TempDir()
	appDir := filepath.Join(root, "Wine Test's app")
	if err := os.Mkdir(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "received")
	program := filepath.Join(root, "capture's app")
	programText := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$WINEDLLOVERRIDES\" \"$DISPLAY\" \"$LD_LIBRARY_PATH\" \"$1\" \"$2\" > " + shellQuote(marker) + "\nexit 37\n"
	if err := os.WriteFile(program, []byte(programText), 0o700); err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(root, "status")
	nativeLibDir := filepath.Join(root, "native lib's")
	command, err := westonLaunchScriptText(appDir, program, []string{"one;two", "a word's value"},
		map[string]string{"WINEDLLOVERRIDES": "mscoree,mshtml=;winemenubuilder.exe=d"}, status, nativeLibDir)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "launch.sh")
	if err := os.WriteFile(script, []byte(command), 0o600); err != nil {
		t.Fatal(err)
	}
	run := hiddenCommand("/bin/sh", script)
	run.Env = append(os.Environ(), "DISPLAY=:42", "LD_LIBRARY_PATH=/weston/lib")
	if err := run.Run(); err == nil || !strings.Contains(err.Error(), "37") {
		t.Fatalf("launch exit = %v, want status 37", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{appDir, "mscoree,mshtml=;winemenubuilder.exe=d", ":42", nativeLibDir + ":/weston/lib", "one;two", "a word's value", ""}, "\n")
	if string(got) != want {
		t.Errorf("program received %q, want %q", got, want)
	}
	d := wineDisplayPlan{appStatus: status}
	if err := d.appExit(nil); err == nil || !strings.Contains(err.Error(), "37") {
		t.Errorf("Weston exit 0 hid app status 37: %v", err)
	}
}

func TestWestonMissingAppStatusIsFailure(t *testing.T) {
	d := wineDisplayPlan{appStatus: filepath.Join(t.TempDir(), "never-written")}
	if err := d.appExit(nil); err == nil || !strings.Contains(err.Error(), "before") {
		t.Errorf("Weston exit 0 hid an app that never ran: %v", err)
	}
}

func TestWinebootUsesFirmwareLibraries(t *testing.T) {
	t.Setenv("JUKAHUB_ORIG_ENV", "HOME PATH LD_LIBRARY_PATH")
	t.Setenv("JUKAHUB_ORIG_HOME", "/firmware/home")
	t.Setenv("JUKAHUB_ORIG_PATH", "/firmware/bin")
	t.Setenv("JUKAHUB_ORIG_LD_LIBRARY_PATH", "/firmware/lib")
	t.Setenv("HOME", "/jukahub")
	t.Setenv("PATH", "/jukahub/required")
	t.Setenv("LD_LIBRARY_PATH", "/jukahub/libs")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")

	env := wineCmdEnv(map[string]string{"WINEPREFIX": "/wine/prefix"}, true)
	for key, want := range map[string]string{
		"HOME": "/firmware/home", "PATH": "/firmware/bin",
		"LD_LIBRARY_PATH": "/firmware/lib", "WINEPREFIX": "/wine/prefix",
	} {
		if got, ok := childEnvLookup(env, key); !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", key, got, ok, want)
		}
	}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "JUKAHUB_ORIG_ENV"} {
		if _, ok := childEnvLookup(env, key); ok {
			t.Errorf("%s leaked into headless wineboot", key)
		}
	}
	if strings.Contains(strings.Join(env, "\n"), "/jukahub/libs") {
		t.Fatal("JukaHub's SDL libraries leaked into wineboot")
	}
}

// weston.env is written by hand on a device: the lines that count, and the
// ones that are ignored.
func TestParseWestonEnv(t *testing.T) {
	env, modes, bad := parseWestonEnv("# comment\n\nCFW_NAME=TrimUI\nexport CRUSTY_RESOLUTION=\"640x360\"\r\n" +
		"JUKAHUB_WESTON_MODE=drm gl kiosk gl4es\nJUKAHUB_WESTON_GL_MODE=too few\n1BAD=x\nnovalue\n")
	if len(env) != 2 || env["CFW_NAME"] != "TrimUI" || env["CRUSTY_RESOLUTION"] != "640x360" {
		t.Fatalf("env = %v", env)
	}
	if got := modes[windowsDisplayWeston]; len(got) != 4 || got[0] != "drm" || got[3] != "gl4es" {
		t.Fatalf("weston mode = %v", got)
	}
	if _, ok := modes[windowsDisplayWestonGL]; ok || len(bad) != 3 {
		t.Fatalf("modes = %v, bad = %v", modes, bad)
	}
}

// The WestonPack build that re-announces every device (and so unmounts the
// SD card on a TrimUI) is refused; a trigger limited to input devices is not.
func TestWestonTextResetsDevices(t *testing.T) {
	for text, want := range map[string]bool{
		"udevadm trigger\n":                                         true,
		"  $ESUDO udevadm trigger --action=change\n":                true,
		"udevadm trigger --subsystem-match=input --action=change\n": false,
		"# udevadm trigger\n":                                       false,
		"weston --xwayland\n":                                       false,
	} {
		if got := westonTextResetsDevices(text); got != want {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

// /proc/mounts escapes spaces in a field as \040.
func TestParseProcMounts(t *testing.T) {
	got := parseProcMounts("/dev/loop0 /tmp/jukahub-weston squashfs ro,relatime 0 0\n/dev/mmcblk1p1 /mnt/SDCARD\\040card exfat rw 0 0\n")
	if len(got) != 2 || got[0] != (procMount{"/dev/loop0", "/tmp/jukahub-weston", "squashfs"}) || got[1].dir != "/mnt/SDCARD card" {
		t.Fatalf("got %+v", got)
	}
}

