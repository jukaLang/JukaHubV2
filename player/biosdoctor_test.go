package main

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func biosDoctorDreamcastCheck(t *testing.T, report biosDoctorReport) biosDoctorCheck {
	t.Helper()
	for _, system := range report.Systems {
		if !strings.HasPrefix(system.Name, "Dreamcast") {
			continue
		}
		if len(system.Checks) != 1 {
			t.Fatalf("Dreamcast checks = %d, want 1", len(system.Checks))
		}
		return system.Checks[0]
	}
	t.Fatal("Dreamcast group not found")
	return biosDoctorCheck{}
}

func TestBIOSDoctorVerifiesHashAndFolder(t *testing.T) {
	good := []byte("a known-good firmware test image")
	bad := []byte("a known-badd firmware test image") // same size as good
	sum := sha1.Sum(good)
	catalog := map[string]biosKnownHash{
		"dc/dc_boot.bin": {Size: int64(len(good)), SHA1: hex.EncodeToString(sum[:])},
	}
	for _, tc := range []struct {
		name       string
		location   string
		data       []byte
		want       biosDoctorCheckKind
		wantNote   string
		otherValid bool
	}{
		{name: "verified", location: "target", data: good, want: biosCheckVerified},
		{name: "wrong hash", location: "target", data: bad, want: biosCheckWrongHash, wantNote: "SHA-1 does not match"},
		{name: "wrong folder", location: "elsewhere", data: good, want: biosCheckWrongPlace},
		{name: "missing", want: biosCheckMissing},
		{name: "prefer verified misplaced copy", location: "target", data: bad, otherValid: true, want: biosCheckWrongPlace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "BIOS")
			elsewhere := filepath.Join(base, "Other")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.location != "" {
				root := target
				if tc.location == "elsewhere" {
					root = elsewhere
				}
				file := filepath.Join(root, "dc", "dc_boot.bin")
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, tc.data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.otherValid {
				file := filepath.Join(elsewhere, "dc", "dc_boot.bin")
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, good, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			report := scanBIOSRootsWithCatalog([]string{target, elsewhere}, catalog)
			check := biosDoctorDreamcastCheck(t, report)
			if check.Kind != tc.want {
				t.Fatalf("kind = %q, want %q; check = %+v", check.Kind, tc.want, check)
			}
			if want := filepath.Join(target, "dc", "dc_boot.bin"); check.Expected != want {
				t.Errorf("expected path = %q, want %q", check.Expected, want)
			}
			if tc.wantNote != "" && !strings.Contains(check.Note, tc.wantNote) {
				t.Errorf("note = %q, want %q", check.Note, tc.wantNote)
			}
			if tc.want == biosCheckWrongPlace {
				want := filepath.Join(elsewhere, "dc", "dc_boot.bin")
				if check.Found != want {
					t.Errorf("found path = %q, want %q", check.Found, want)
				}
				lines := strings.Join(biosDoctorLines(report, false), "\n")
				if !strings.Contains(lines, "Wrong folder: "+want) || !strings.Contains(lines, "Put this verified file at "+check.Expected) {
					t.Errorf("screen text does not show both paths: %s", lines)
				}
			}
		})
	}
}

func TestBIOSDoctorUnknownHashIsNotVerified(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "dc", "dc_boot.bin")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("unlisted firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := biosDoctorDreamcastCheck(t, scanBIOSRootsWithCatalog([]string{root}, nil))
	if check.Kind != biosCheckUnverified {
		t.Fatalf("kind = %q, want unverified", check.Kind)
	}
}

func TestBIOSDoctorCatalogCoversFileGroups(t *testing.T) {
	for _, group := range biosFilenameGroups {
		for _, names := range group.accepted {
			for _, name := range names {
				if filepath.Ext(name) == "" { // blueMSX's folders
					continue
				}
				if _, ok := biosKnownHashes[strings.ToLower(filepath.ToSlash(name))]; !ok {
					t.Errorf("%s: %s has no known-good hash", group.name, name)
				}
			}
		}
	}
}
