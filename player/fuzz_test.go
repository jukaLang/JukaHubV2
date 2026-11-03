package main

// Fuzz targets.
//
// These cover the two places where text from outside the app becomes a path or
// a network address: the Windows-runtime archive extractor and the SFTP address
// field. Both are pure string functions, so a target runs in microseconds and
// needs no device, card or network — that is what makes them worth fuzzing.
//
// `go test` runs every target over its seed corpus as an ordinary test, so the
// seeds below are the regression cases; the nightly job feeds them to the fuzz
// engine for a burst of generated input.

import (
	"path/filepath"
	"strings"
	"testing"
)

// fuzzArchiveDest is a fixed extraction root. The path helpers only do path
// arithmetic, so nothing is created on disk.
func fuzzArchiveDest() string {
	return filepath.Join(string(filepath.Separator), "mnt", "SDCARD", "data", "windows-runtime", "staging")
}

// archiveNameSeeds are the shapes that matter: ordinary members, traversal,
// absolute names, Windows separators and drive letters, and the empty string.
var archiveNameSeeds = []string{
	"bin/wine",
	"usr/lib/wine/x86_64-windows/ntdll.dll",
	"./bin/./wine",
	"bin//wine",
	"a/../b",
	"../escape",
	"../../../../etc/passwd",
	"/etc/passwd",
	`..\..\escape`,
	`C:\Windows\System32\drivers\etc\hosts`,
	"",
}

// FuzzRuntimeArchivePath: a member name either gets rejected or resolves to a
// path below the extraction root. Nothing else is acceptable, because the
// result is opened for writing a moment later.
func FuzzRuntimeArchivePath(f *testing.F) {
	for _, seed := range archiveNameSeeds {
		f.Add(seed)
	}
	dest := fuzzArchiveDest()
	dirPrefix := filepath.Clean(dest) + string(filepath.Separator)
	f.Fuzz(func(t *testing.T, name string) {
		target, err := runtimeArchivePath(dest, name)
		if err != nil {
			return
		}
		if !isPathWithin(target, dest) {
			t.Fatalf("entry %q resolved outside the runtime folder: %q", name, target)
		}
		if !strings.HasPrefix(target, dirPrefix) {
			t.Fatalf("entry %q resolved to %q, which is not below %q", name, target, dest)
		}
	})
}

// FuzzArchiveLinkTarget: the same rule for a symlink's text. The member name is
// itself validated first, so the fuzzer drives both halves of the check.
func FuzzArchiveLinkTarget(f *testing.F) {
	for _, seed := range archiveNameSeeds {
		for _, link := range []string{"wine", "../wine64", "/lib/ld-linux-aarch64.so.1", `..\escape`, ""} {
			f.Add(seed, link)
		}
	}
	dest := fuzzArchiveDest()
	dirPrefix := filepath.Clean(dest) + string(filepath.Separator)
	f.Fuzz(func(t *testing.T, name, link string) {
		target, err := runtimeArchivePath(dest, name)
		if err != nil {
			return
		}
		resolved, err := archiveLinkTarget(dest, target, link)
		if err != nil {
			return
		}
		if !isPathWithin(resolved, dest) {
			t.Fatalf("link %q from %q resolved outside the runtime folder: %q", link, name, resolved)
		}
		if !strings.HasPrefix(resolved, dirPrefix) {
			t.Fatalf("link %q from %q resolved to %q, which is not below %q", link, name, resolved, dest)
		}
	})
}

// FuzzParseFeRemote: whatever is typed or pasted into the transfer address
// field, the parser must either refuse it or hand back a usable address — a
// real port, a device name, a user name and an absolute folder.
func FuzzParseFeRemote(f *testing.F) {
	for _, seed := range []string{
		"192.168.1.7",
		"user@192.168.1.7:2222/roms",
		"user@host:/roms",
		"sftp://user@host:22/",
		"user@host:0/x",
		"user@host:99999/x",
		"@host",
		"host with space",
		"user@@host",
		"....",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		target, err := parseFeRemote(text)
		if err != nil {
			return
		}
		if target.Host == "" {
			t.Fatalf("%q parsed without a device name", text)
		}
		if target.User == "" || strings.ContainsAny(target.User, " \t@/") {
			t.Fatalf("%q parsed with an unusable user name %q", text, target.User)
		}
		if target.Port <= 0 || target.Port > 65535 {
			t.Fatalf("%q parsed with port %d", text, target.Port)
		}
		if !strings.HasPrefix(target.Path, "/") {
			t.Fatalf("%q parsed with a relative folder %q", text, target.Path)
		}
		// The canonical form is what gets seeded back into the field and
		// stored as the last address, so it has to round-trip.
		if again, err := parseFeRemote(target.address()); err != nil {
			t.Fatalf("%q re-parsed its own address %q: %v", text, target.address(), err)
		} else if again.key() != target.key() {
			t.Fatalf("%q round-tripped to %q, want %q", text, again.key(), target.key())
		}
	})
}
