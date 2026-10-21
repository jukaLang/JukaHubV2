package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanBIOSRootsReportsSystemsAndFingerprints(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"ScPh5501.BIN":  "ps1 firmware",
		"dc_boot.bin":   "dreamcast boot",
		"dc_flash.bin":  "dreamcast flash",
		"mpr-17933.bin": "saturn firmware",
		"other.bin":     "other firmware",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	report := scanBIOSRoots([]string{root})
	if got, want := biosDoctorSummary(report), "PS1 ready, Dreamcast ready, Saturn ready"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if len(report.Files) != len(files) {
		t.Fatalf("hashed files = %d, want %d", len(report.Files), len(files))
	}
	want := sha256.Sum256([]byte(files["ScPh5501.BIN"]))
	found := false
	for _, item := range report.Files {
		if item.Name == "ScPh5501.BIN" {
			found = true
			if item.SHA256 != hex.EncodeToString(want[:]) {
				t.Fatalf("SHA-256 = %s, want %x", item.SHA256, want)
			}
		}
	}
	if !found {
		t.Fatal("PS1 BIOS fingerprint is missing")
	}
}

func TestScanBIOSRootsNamesMissingFirmware(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "scph5502.bin"), []byte("bios"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := scanBIOSRoots([]string{root})
	if got, want := biosDoctorSummary(report), "PS1 ready, Dreamcast missing dc_boot.bin, dc_flash.bin, Saturn missing sega_101.bin or mpr-17933.bin or saturn_bios.bin"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if len(report.Systems) == 0 {
		t.Fatal("BIOS report has no systems")
	}
	if len(report.Systems[0].Missing) > 0 && strings.Contains(report.Systems[0].Missing[0], "scph5502") {
		t.Fatalf("recognized regional BIOS was reported missing: %+v", report.Systems[0])
	}
}
