package main

// corefile_damage_test.go — a core file that is not the whole program.
//
// A Smart Pro S launched Crash Bandicoot on a swanstation core its own launch
// report called "a damaged program (invalid ELF section name string table type
// 'SHT_NULL' in record at byte 0x4b1be8)" and got SIGSEGV in under a second,
// while the card said "JukaHub found no missing BIOS, core or game file": the
// load check stood aside for a file it could not parse (JH-YV9AEC). These tests
// hold the two halves of the fix: a file debug/elf refuses is a finding, and the
// card words it as a copy that stopped halfway.

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"strings"
	"testing"
)

// damageTestELF zeroes the type of the section header that names the section
// table's strings, which is exactly what Go's ELF reader refused on the card.
func damageTestELF(t *testing.T, data []byte) []byte {
	t.Helper()
	le := binary.LittleEndian
	shoff := le.Uint64(data[0x28:])
	shentsize := int(le.Uint16(data[0x3A:]))
	shstrndx := int(le.Uint16(data[0x3E:]))
	if shoff == 0 || shentsize < 64 || shstrndx == 0 {
		t.Fatalf("test ELF has no section table (shoff %#x, shentsize %d, shstrndx %d)", shoff, shentsize, shstrndx)
	}
	entry := int(shoff) + shstrndx*shentsize
	if entry+64 > len(data) {
		t.Fatalf("section name string table entry at %#x is past the file (%d bytes)", entry, len(data))
	}
	le.PutUint32(data[entry+4:], 0) // sh_type = SHT_NULL
	return data
}

func TestElfDamageProblem(t *testing.T) {
	good := buildTestELF(t, testELF{machine: elf.EM_AARCH64, soname: "core.so"})
	if got := elfDamageProblem(bytes.NewReader(good)); got != "" {
		t.Fatalf("a whole ELF reads as damaged: %q", got)
	}
	got := elfDamageProblem(bytes.NewReader(damageTestELF(t, append([]byte(nil), good...))))
	if !strings.Contains(got, "its file is damaged") {
		t.Fatalf("damaged section table = %q, want the damage problem", got)
	}
	if got := elfDamageProblem(bytes.NewReader(good[:len(good)/2])); !strings.Contains(got, "its file is damaged") {
		t.Fatalf("truncated core = %q, want the damage problem", got)
	}
	// Not an ELF: a script, a Windows DLL, a test fixture. The check does not
	// apply, as before.
	for _, other := range [][]byte{[]byte("#!/bin/sh\necho hi\n"), []byte("MZ\x90\x00"), {}, []byte("\x7fEL")} {
		if got := elfDamageProblem(bytes.NewReader(other)); got != "" {
			t.Errorf("%q = %q, want no problem", other, got)
		}
	}
}

// The card says a damaged core is a copy that stopped halfway, and keeps the
// wording corefix.go's after-game repair matches on.
func TestGameDiagCoreLoadIssueDamaged(t *testing.T) {
	path := "/mnt/SDCARD/RetroArch/.retroarch/cores/swanstation_libretro.so"
	issue := gameDiagCoreLoadIssue("swanstation", path,
		"its file is damaged (invalid ELF section name string table type 'SHT_NULL' in record at byte 0x4b1be8)")
	if issue.Level != int(gameDiagSure) {
		t.Errorf("level = %d, want gameDiagSure: %+v", issue.Level, issue)
	}
	if !strings.Contains(issue.Title, "cannot load on this device") {
		t.Errorf("title = %q, want the wording corefix.go looks for", issue.Title)
	}
	if issue.Where != path {
		t.Errorf("where = %q, want the core file", issue.Where)
	}
	for _, want := range []string{"its file is damaged", "not the whole program", "Emulators page"} {
		if !strings.Contains(issue.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, issue.Detail)
		}
	}
	// A core that is simply too new for this firmware keeps its old wording.
	other := gameDiagCoreLoadIssue("swanstation", path, "needs GLIBC_2.38, which this firmware does not have")
	if other.Level != int(gameDiagLikely) || !strings.Contains(other.Detail, "Replace it with a build for this device") {
		t.Errorf("a build for a newer glibc = %+v", other)
	}
	if strings.Contains(other.Detail, "not the whole program") {
		t.Errorf("a newer glibc is not a damaged copy: %s", other.Detail)
	}
}
