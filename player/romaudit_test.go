package main

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestROMAuditParseDAT(t *testing.T) {
	dat := `clrmamepro (
	name "Nintendo - Game Boy Advance"
	description "Nintendo - Game Boy Advance"
)
game (
	name "Advance Wars (USA)"
	description "Advance Wars (USA)"
	rom ( name "Advance Wars (USA).gba" size 8388608 crc 5AD0E571 md5 0123456789ABCDEF0123456789ABCDEF sha1 0123456789ABCDEF0123456789ABCDEF01234567 )
)
game (
	name "Multi Track (Europe)"
	rom ( name "Multi Track (Europe) (Track 1).bin" size 100 crc AAAAAAAA )
	rom ( name "Multi Track (Europe) (Track 2).bin" size 200 crc BBBBBBBB )
)`
	parsed := romAuditParseDAT([]byte(dat), "gba.dat")
	if parsed.Name != "Nintendo - Game Boy Advance" {
		t.Fatalf("DAT name = %q", parsed.Name)
	}
	if len(parsed.Games) != 2 {
		t.Fatalf("games = %d, want 2", len(parsed.Games))
	}
	first := parsed.Games[0]
	if first.Name != "Advance Wars (USA)" || len(first.ROMs) != 1 {
		t.Fatalf("first game = %+v", first)
	}
	rom := first.ROMs[0]
	if rom.CRC != "5AD0E571" || rom.Size != 8388608 || rom.SHA1 == "" || rom.MD5 == "" {
		t.Fatalf("rom = %+v", rom)
	}
	if got := len(parsed.Games[1].ROMs); got != 2 {
		t.Fatalf("multi-track roms = %d, want 2", got)
	}
}

func TestROMAuditBuildVerdicts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	verified := write("Advance Wars (USA).gba", "advance")
	renamed := write("Wrong Name.gba", "canonical")
	bad := write("Bad Dump.gba", "bad")
	unknown := write("Unknown.gba", "unknown")
	duplicate := write("Advance Wars (USA) copy.gba", "advance")

	crc := func(body string) string { return fmt.Sprintf("%08X", crc32.ChecksumIEEE([]byte(body))) }
	dat := `clrmamepro ( name "Nintendo - Game Boy Advance" )
game ( name "Advance Wars (USA)" rom ( name "Advance Wars (USA).gba" size 8 crc ` + crc("advance") + ` ) )
game ( name "Canonical (USA)" rom ( name "Canonical (USA).gba" size 9 crc ` + crc("canonical") + ` ) )
game ( name "Bad Dump (USA)" rom ( name "Bad Dump (USA).gba" size 3 crc ` + crc("different") + ` ) )
game ( name "Missing Game (USA)" rom ( name "Missing Game (USA).gba" size 7 crc ` + crc("missing") + ` ) )`
	datPath := filepath.Join(dir, "gba.dat")
	if err := os.WriteFile(datPath, []byte(dat), 0o644); err != nil {
		t.Fatal(err)
	}

	scan := romScanReport{Games: []scannedROM{
		{Path: verified, System: "Game Boy Advance"},
		{Path: renamed, System: "Game Boy Advance"},
		{Path: bad, System: "Game Boy Advance"},
		{Path: unknown, System: "Game Boy Advance"},
		{Path: duplicate, System: "Game Boy Advance"},
	}}
	report := romAuditBuild(scan, []string{datPath})
	if len(report.Systems) != 1 {
		t.Fatalf("systems = %d, want 1 (%+v)", len(report.Systems), report)
	}
	sys := report.Systems[0]
	if sys.Listed != 4 || sys.Verified != 1 || sys.Renamed != 1 || sys.Bad != 1 ||
		sys.Unknown != 1 || sys.Duplicates != 1 || sys.Missing != 1 {
		t.Fatalf("counts = %+v", sys)
	}
	if len(sys.MissingNames) != 1 || sys.MissingNames[0] != "Missing Game (USA)" {
		t.Fatalf("missing = %v", sys.MissingNames)
	}
	if len(sys.RenameFiles) != 1 || !strings.Contains(sys.RenameFiles[0], "Wrong Name.gba -> Canonical (USA).gba") {
		t.Fatalf("renames = %v", sys.RenameFiles)
	}
	if len(sys.BadFiles) != 1 || !strings.Contains(sys.BadFiles[0], "Bad Dump.gba") {
		t.Fatalf("bad = %v", sys.BadFiles)
	}
	if len(sys.UnknownFiles) != 1 || !strings.Contains(sys.UnknownFiles[0], "Unknown.gba") {
		t.Fatalf("unknown = %v", sys.UnknownFiles)
	}
	if len(sys.DuplicateFiles) != 1 || !strings.Contains(sys.DuplicateFiles[0], "copy.gba") {
		t.Fatalf("duplicates = %v", sys.DuplicateFiles)
	}

	lines := strings.Join(romAuditLines(report, false, 0, 0, true), "\n")
	for _, want := range []string{"Game Boy Advance", "Missing", "Bad Dump.gba", "Rename suggestions", "Duplicates"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("report has no %q:\n%s", want, lines)
		}
	}
}

func TestROMAuditSecondDATAndUnknownConsole(t *testing.T) {
	dir := t.TempDir()
	crc := func(body string) string { return fmt.Sprintf("%08X", crc32.ChecksumIEEE([]byte(body))) }
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	alpha := write("Alpha.gba", "alpha")
	first := filepath.Join(dir, "a-first.dat")
	second := filepath.Join(dir, "b-second.dat")
	unknown := filepath.Join(dir, "c-unknown.dat")
	if err := os.WriteFile(first, []byte(`clrmamepro ( name "Nintendo - Game Boy Advance" )
game ( name "Alpha (USA)" rom ( name "Alpha.gba" crc `+crc("alpha")+` ) )`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`clrmamepro ( name "Nintendo - Game Boy Advance" )
game ( name "Beta (USA)" rom ( name "Beta.gba" crc `+crc("beta")+` ) )`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A DAT for a console the library does not name is still audited when a
	// CRC proves it belongs on this card.
	if err := os.WriteFile(unknown, []byte(`clrmamepro ( name "Mystery Console" )
game ( name "Alpha (USA)" rom ( name "Alpha.gba" crc `+crc("alpha")+` ) )`), 0o644); err != nil {
		t.Fatal(err)
	}
	report := romAuditBuild(romScanReport{Games: []scannedROM{{Path: alpha, System: "Game Boy Advance"}}},
		[]string{first, second, unknown})
	if len(report.Systems) != 2 {
		t.Fatalf("systems = %d, want 2: %+v", len(report.Systems), report.Systems)
	}
	if len(report.SkippedDats) != 1 || !strings.Contains(report.SkippedDats[0], "second DAT") {
		t.Fatalf("skipped = %v", report.SkippedDats)
	}
	found := false
	for _, sys := range report.Systems {
		if sys.Collection == "Mystery Console" && sys.Name == "Mystery Console" && len(sys.Files) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown-console DAT was not audited: %+v", report.Systems)
	}
}

func TestROMAuditNoDats(t *testing.T) {
	report := romAuditReport{}
	lines := strings.Join(romAuditLines(report, false, 0, 0, false), "\n")
	if !strings.Contains(lines, "No DAT files found") {
		t.Fatalf("lines = %s", lines)
	}
	if !strings.Contains(lines, "rom-audit") {
		t.Fatalf("the folder to use is not named: %s", lines)
	}
}

func TestROMAuditHelpers(t *testing.T) {
	if got := romAuditSystemForCollection("Nintendo - Game Boy Advance"); got != "Game Boy Advance" {
		t.Fatalf("system = %q", got)
	}
	for _, ext := range []string{".cue", ".chd", ".m3u"} {
		if !romAuditNameOnlyExt("game" + ext) {
			t.Fatalf("%s should be name-only", ext)
		}
	}
	if romAuditNameOnlyExt("game.gba") {
		t.Fatal("a cartridge should be hashed")
	}
	if got := romAuditRenameTarget("Advance Wars (USA)", "x/y.gba"); got != "Advance Wars (USA).gba" {
		t.Fatalf("target = %q", got)
	}
}
