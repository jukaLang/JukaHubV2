package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkipScanEntryName(t *testing.T) {
	cases := []struct {
		name string
		skip bool
	}{
		{".", true},
		{"..", true},
		{"", true},
		{".hidden", true},
		{".DS_Store", true},
		{" _Zelda.gba", false},
		{"._Zelda.gba", true},
		{"__MACOSX", true},
		{"__macosx", true},
		{"desktop.ini", true},
		{"Thumbs.db", true},
		{"Zelda.gba", false},
		{"Roms", false},
		{"Super Mario World (USA).sfc", false},
	}
	for _, c := range cases {
		if got := skipScanEntryName(c.name); got != c.skip {
			t.Errorf("skipScanEntryName(%q) = %v, want %v", c.name, got, c.skip)
		}
	}
}

// The text-browser listing is one of the scanners outside the game library; a
// folder copied from a Mac must list only its real files.
func TestScanLocalFilesSkipsMetadata(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Zelda.gba", "._Zelda.gba", ".DS_Store", "desktop.ini"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "__MACOSX"), 0o755); err != nil {
		t.Fatalf("mkdir __MACOSX: %v", err)
	}

	out := scanLocalFiles(dir)
	if !strings.Contains(out, "Zelda.gba") {
		t.Fatalf("the real file is missing from the listing:\n%s", out)
	}
	for _, hidden := range []string{"._Zelda.gba", ".DS_Store", "desktop.ini", "__MACOSX"} {
		if strings.Contains(out, hidden) {
			t.Errorf("the listing still shows %q:\n%s", hidden, out)
		}
	}
}

// An Xbox game with no launcher must name the fix on a device that can install
// hakuX, and the model's own reason on one that cannot.
func TestXboxLibraryLauncherHint(t *testing.T) {
	if got := xboxLibraryLauncherHintFor(DeviceTrimuiSmartPro); !strings.Contains(got, "Install hakuX: Emulators → Xbox → X") {
		t.Errorf("Smart Pro hint = %q, want the install path", got)
	}
	if got := xboxLibraryLauncherHintFor(DeviceTrimuiBrickPro); !strings.Contains(got, "Install hakuX") {
		t.Errorf("Brick Pro hint = %q, want the install path", got)
	}
	for _, d := range []DeviceModel{DeviceTrimuiSmartProS, DeviceTrimuiBrick, DeviceUnknown} {
		got := xboxLibraryLauncherHintFor(d)
		if !strings.Contains(got, "does not support") || !strings.Contains(got, "—") {
			t.Errorf("%s hint = %q, want the device's reason", d, got)
		}
	}
}
