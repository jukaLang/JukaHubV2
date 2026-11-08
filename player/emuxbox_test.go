package main

// emuxbox_test.go — the Xbox pieces: xemu's settings are edited without
// disturbing anything else in them, the checklist recognises the firmware files
// by what they are (and a PS1 BIOS is not mistaken for one), xemu boots a game
// through -dvd_path, Roms/XBOX is a library system, and the handheld says
// plainly that it cannot run Xbox games.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmuXemuTomlSetFilesAddsTheSection(t *testing.T) {
	files := map[string]string{
		"bootrom_path":  `C:\Games\BIOS\xbox\mcpx_1.0.bin`,
		"flashrom_path": `C:\Games\BIOS\xbox\Complex_4627.bin`,
		"hdd_path":      `C:\Games\BIOS\xbox\xbox_hdd.qcow2`,
	}
	got := emuXemuTomlSetFiles("", files)
	if !strings.HasPrefix(got, "[sys.files]\n") {
		t.Fatalf("new config = %q, want a [sys.files] section", got)
	}
	parsed := emuXemuTomlFiles(got)
	for key, want := range files {
		if parsed[key] != want {
			t.Errorf("%s = %q, want %q (Windows backslashes must survive)", key, parsed[key], want)
		}
	}
	if _, ok := parsed["eeprom_path"]; ok {
		t.Error("an EEPROM that was not found was written")
	}
}

func TestEmuXemuTomlSetFilesKeepsTheRestOfTheConfig(t *testing.T) {
	existing := strings.Join([]string{
		"[general]",
		"show_welcome = false",
		"",
		"[sys.files]",
		"bootrom_path = 'D:\\old\\mcpx.bin'",
		"dvd_path = 'D:\\games\\halo2.iso'",
		"",
		"[display]",
		"renderer = 'VULKAN'",
	}, "\n")
	got := emuXemuTomlSetFiles(existing, map[string]string{
		"bootrom_path":  `C:\BIOS\mcpx_1.0.bin`,
		"flashrom_path": `C:\BIOS\Complex_4627.bin`,
	})
	parsed := emuXemuTomlFiles(got)
	if parsed["bootrom_path"] != `C:\BIOS\mcpx_1.0.bin` {
		t.Errorf("bootrom_path = %q, want it replaced in place", parsed["bootrom_path"])
	}
	if parsed["flashrom_path"] != `C:\BIOS\Complex_4627.bin` {
		t.Errorf("flashrom_path = %q, want it added to the section", parsed["flashrom_path"])
	}
	if parsed["dvd_path"] != `D:\games\halo2.iso` {
		t.Errorf("dvd_path = %q, want the user's own key untouched", parsed["dvd_path"])
	}
	for _, keep := range []string{"[general]", "show_welcome = false", "[display]", "renderer = 'VULKAN'"} {
		if !strings.Contains(got, keep) {
			t.Errorf("config lost %q:\n%s", keep, got)
		}
	}
	if strings.Count(got, "[sys.files]") != 1 {
		t.Errorf("config has %d [sys.files] sections:\n%s", strings.Count(got, "[sys.files]"), got)
	}
	// The added key belongs inside the section, before [display].
	if strings.Index(got, "flashrom_path") > strings.Index(got, "[display]") {
		t.Errorf("flashrom_path landed outside [sys.files]:\n%s", got)
	}
}

func TestEmuTomlStringRoundTrips(t *testing.T) {
	for _, path := range []string{`C:\Users\Ana\BIOS\mcpx_1.0.bin`, `C:\Users\O'Brien\bios.bin`, "/home/user/xbox_hdd.qcow2"} {
		line := "[sys.files]\nhdd_path = " + emuTomlString(path) + "\n"
		if got := emuXemuTomlFiles(line)["hdd_path"]; got != path {
			t.Errorf("round trip of %q gave %q", path, got)
		}
	}
}

// TestEmuXboxClassifyRecognisesTheFirmware: the boot ROM by name or by its
// first and last bytes, the flash ROM only by a name that says Xbox (a 512 KB
// PS1 BIOS is the same size), the HDD image and the EEPROM.
func TestEmuXboxClassifyRecognisesTheFirmware(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	mcpxBody := make([]byte, 512)
	mcpxBody[0], mcpxBody[1], mcpxBody[510], mcpxBody[511] = 0x33, 0xC0, 0x02, 0xEE
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"mcpx_1.0.bin", make([]byte, 512), "bootrom"},
		{"boot.bin", mcpxBody, "bootrom"},
		{"random512.bin", make([]byte, 512), ""},
		{"Complex_4627.bin", make([]byte, 1<<20), "flashrom"},
		{"scph5501.bin", make([]byte, 512<<10), ""},
		{"xbox_hdd.qcow2", []byte("QFI"), "hdd"},
		{"eeprom.bin", make([]byte, 256), "eeprom"},
		{"readme.txt", []byte("hello"), ""},
	}
	for _, tc := range cases {
		path := write(tc.name, tc.data)
		if got := emuXboxClassify(path, int64(len(tc.data))); got != tc.want {
			t.Errorf("%s classified as %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEmuXboxCheckFileJudgesTheBootROM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpx_1.0.bin")
	if err := os.WriteFile(path, make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	f := emuXboxFile{Key: "bootrom", Path: path, Required: true}
	emuXboxCheckFile(&f)
	if !f.ok() || f.Note != "unverified dump" {
		t.Fatalf("an unknown 512-byte dump = %+v, want usable but unverified", f)
	}
	short := filepath.Join(dir, "mcpx_short.bin")
	if err := os.WriteFile(short, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	f = emuXboxFile{Key: "bootrom", Path: short, Required: true}
	emuXboxCheckFile(&f)
	if f.ok() || f.Problem == "" {
		t.Fatalf("a 100-byte boot ROM = %+v, want a problem", f)
	}
	gone := emuXboxFile{Key: "flashrom", Path: filepath.Join(dir, "missing.bin"), Required: true}
	emuXboxCheckFile(&gone)
	if gone.ok() {
		t.Fatal("a listed file that does not exist counts as found")
	}
}

func TestEmuXboxReportReadiness(t *testing.T) {
	report := emuXboxReport{Files: []emuXboxFile{
		{Key: "bootrom", Label: "MCPX boot ROM", Path: "/x/mcpx.bin", Required: true},
		{Key: "flashrom", Label: "Flash ROM (BIOS)", Required: true},
		{Key: "hdd", Label: "Hard disk image", Path: "/x/hdd.qcow2", Problem: "bad", Required: true},
		{Key: "eeprom", Label: "EEPROM (optional)"},
	}}
	if report.ready() {
		t.Fatal("a checklist with a missing flash ROM is ready")
	}
	if got := strings.Join(report.missing(), ", "); got != "Flash ROM (BIOS), Hard disk image" {
		t.Fatalf("missing = %q", got)
	}
	if _, err := emuXboxApply(report); err == nil {
		t.Fatal("xemu was configured with files missing")
	}
}

func TestEmuIsXemuLauncher(t *testing.T) {
	cases := map[string]bool{
		`C:\Emulators\xemu\xemu.exe`: true,
		"/opt/xemu/xemu":             true,
		`C:\Emulators\XEMU.EXE`:      true,
		`C:\Emulators\cemu\Cemu.exe`: false,
		"/emus/XBOX/launch.sh":       false,
	}
	for launcher, want := range cases {
		if got := emuIsXemuLauncher(launcher); got != want {
			t.Errorf("emuIsXemuLauncher(%q) = %v, want %v", launcher, got, want)
		}
	}
}

// TestXemuBootsAGameWithDvdPath: xemu's documented command line is
// "-dvd_path <iso>"; a bare path would be handed to QEMU and ignored.
func TestXemuBootsAGameWithDvdPath(t *testing.T) {
	name, args := gameLaunchCommand(`C:\Emulators\xemu\xemu.exe`, `D:\Roms\XBOX\Halo 2.iso`)
	if name != `C:\Emulators\xemu\xemu.exe` {
		t.Fatalf("command = %q, want xemu itself", name)
	}
	if len(args) != 2 || args[0] != "-dvd_path" || args[1] != `D:\Roms\XBOX\Halo 2.iso` {
		t.Fatalf("args = %v, want -dvd_path and the image", args)
	}
	if launcher := findGameLauncher("Xbox", []emuEntry{{Name: "xemu", Status: emuAvailable, Launch: `C:\Emulators\xemu\xemu.exe`}}); launcher == "" {
		t.Fatal("the library does not find xemu for Xbox games")
	}
}

func TestXboxRomFolderIsALibrarySystem(t *testing.T) {
	root := t.TempDir()
	for _, game := range []string{"Halo 2.iso", "Jet Set Radio Future.xiso"} {
		writeFakeEmuFile(t, filepath.Join(root, "XBOX", game))
	}
	report := scanROMLibrary([]string{root})
	if report.Counts["Xbox"] != 2 {
		t.Fatalf("Xbox games = %d (%v), want both images", report.Counts["Xbox"], report.Counts)
	}
}

// TestHandheldSaysXboxIsNotSupported: the Xbox tile is always there on the
// handheld, marked unsupported with the reason, and a Roms/PS2 folder is the
// same honest answer instead of "no emulator found".
func TestHandheldSaysXboxIsNotSupported(t *testing.T) {
	root := t.TempDir()
	roms := filepath.Join(root, "Roms")
	writeFakeEmuFile(t, filepath.Join(roms, "PS2", "game.iso"))
	writeFakeEmuFile(t, filepath.Join(roms, "XBOX", "halo2.iso"))
	entries := deviceEmulatorEntries([]string{filepath.Join(root, "Emus")}, roms, filepath.Join(root, "cores"))
	byName := map[string]emuEntry{}
	for _, e := range entries {
		byName[strings.ToLower(e.Name)] = e
	}
	xbox, ok := byName["xbox"]
	if !ok || xbox.Status != emuUnsupported {
		t.Fatalf("Xbox = %+v (%v), want an unsupported tile", xbox, ok)
	}
	if !strings.Contains(xbox.Note, "A133P") || !strings.Contains(xbox.Note, "desktop-class") {
		t.Fatalf("Xbox reason = %q, want the honest hardware reason", xbox.Note)
	}
	if ps2 := byName["ps2"]; ps2.Status != emuUnsupported || ps2.Note == "" {
		t.Fatalf("PS2 = %+v, want unsupported with a reason", ps2)
	}
	if label := emuInstallHintLabel(xbox); label != "Details" {
		t.Fatalf("the Xbox tile offers A %q, want Details", label)
	}
	// Unsupported tiles sort after everything a user can act on.
	if last := entries[len(entries)-1]; last.Status != emuUnsupported {
		t.Fatalf("last tile = %+v, want an unsupported one", last)
	}
}
