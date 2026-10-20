package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jukahub/JukaHub/internal/releasesign"
)

// TestRunSignsTheArchivesItIsGiven pins the tool the release pipeline calls: it
// hashes what it is handed, signs the manifest, and the result verifies with the
// same code the updater runs.
func TestRunSignsTheArchivesItIsGiven(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body := []byte("release archive bytes")
	zipPath := filepath.Join(dir, "JukaHub-Windows.zip")
	if err := os.WriteFile(zipPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "release.json")

	if err := run([]string{
		"-tag", "JukaHub-20261020154603",
		"-asset", "JukaHub-Windows.zip=" + zipPath,
		"-out", out,
		"-expires", "24h",
	}, hex.EncodeToString(priv)); err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var m releasesign.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("written manifest is not valid JSON: %v", err)
	}
	if m.Tag != "JukaHub-20261020154603" {
		t.Errorf("tag = %q", m.Tag)
	}
	if m.KeyID != "jukahub-patch-dev-2026" {
		t.Errorf("key id = %q, want the app's embedded key id", m.KeyID)
	}
	if err := releasesign.Verify(&m, pub, m.KeyID, time.Now()); err != nil {
		t.Fatalf("written manifest does not verify: %v", err)
	}
	target, err := releasesign.FindTarget(&m, "JukaHub-Windows.zip")
	if err != nil {
		t.Fatalf("FindTarget: %v", err)
	}
	want := sha256.Sum256(body)
	if target.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("signed digest = %s, want %x", target.SHA256, want)
	}
	if target.Size != int64(len(body)) {
		t.Errorf("signed size = %d, want %d", target.Size, len(body))
	}
}

func TestRunRejectsIncompleteInput(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(priv)
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")
	if err := os.WriteFile(archive, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		key  string
	}{
		{"missing tag", []string{"-asset", "a.zip=" + archive}, key},
		{"missing asset", []string{"-tag", "t"}, key},
		{"missing key", []string{"-tag", "t", "-asset", "a.zip=" + archive}, ""},
		{"short key", []string{"-tag", "t", "-asset", "a.zip=" + archive}, "abcd"},
		{"missing file", []string{"-tag", "t", "-asset", "a.zip=" + filepath.Join(dir, "nope")}, key},
		{"asset without path", []string{"-tag", "t", "-asset", "a.zip"}, key},
	}
	for _, tc := range cases {
		if err := run(tc.args, tc.key); err == nil {
			t.Errorf("%s: run accepted incomplete input", tc.name)
		}
	}
}
