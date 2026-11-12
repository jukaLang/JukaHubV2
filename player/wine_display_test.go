package main

import (
	"strings"
	"testing"
)

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
