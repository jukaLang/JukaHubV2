package main

// emulatorupdates_test.go — the update checker has three rules worth pinning
// down: only cores the index actually carries are considered, a fetched core
// must match the CRC the index promised, and nothing is ever written over an
// active core or an installed emulator.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func crcOf(data []byte) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(data))
}

// allowUpdateTestServer lets the download helpers reach a loopback httptest
// server. Everything outside base still goes through the real allowlist, so a
// test cannot accidentally widen the app's download policy.
func allowUpdateTestServer(t *testing.T, base string) {
	t.Helper()
	prev := emulatorUpdateValidateURL
	emulatorUpdateValidateURL = func(raw string) error {
		if strings.HasPrefix(raw, base) {
			return nil
		}
		return validatePatchURL(raw)
	}
	t.Cleanup(func() { emulatorUpdateValidateURL = prev })
}

func writePoolTestCore(t *testing.T, dir, name, body string) coreBuild {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write core: %v", err)
	}
	return coreBuild{Core: strings.TrimSuffix(name, "_libretro.so"), Version: "1.0.0", File: name, Path: path, Active: true}
}

func zestCoreZip(t *testing.T, binary, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(binary)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestParseCoreIndexSkipsJunkAndDedupes(t *testing.T) {
	text := strings.Join([]string{
		"2026-10-23 16647efd mednafen_supergrafx_libretro.so.zip",
		"",
		"not an index line",
		"2026-10-23 nothex mednafen_wswan_libretro.so.zip",
		"2026-10-23 9e7737e6 vecx_libretro.so.zip",
		"2026-10-23 377dd207 some_other_file.zip",
		"2026-10-24 aabbccdd vecx_libretro.so.zip",
		"2026-10-23 50d1e5ec ../evil_libretro.so.zip",
	}, "\n")
	entries := parseCoreIndex(text)
	if len(entries) != 2 {
		t.Fatalf("entries = %d (%+v), want 2", len(entries), entries)
	}
	byCore := map[string]coreIndexEntry{}
	for _, entry := range entries {
		byCore[entry.Core] = entry
	}
	if got := byCore["mednafen_supergrafx"]; got.CRC32 != "16647efd" || got.Date != "2026-10-23" {
		t.Fatalf("supergrafx entry = %+v", got)
	}
	if got := byCore["vecx"]; got.CRC32 != "aabbccdd" || got.Date != "2026-10-24" {
		t.Fatalf("last line for a core must win, got %+v", got)
	}
}

func TestPlanCoreUpdatesOnlyReportsChangedInstalledCores(t *testing.T) {
	active := map[string]coreBuild{}
	dir := t.TempDir()
	same := writePoolTestCore(t, dir, "same_libretro.so", "identical build")
	staleBody := "old build"
	stale := writePoolTestCore(t, dir, "stale_libretro.so", staleBody)
	active[same.Core] = same
	active[stale.Core] = stale

	entries := []coreIndexEntry{
		{Core: "same", Date: "2026-10-23", CRC32: crcOf([]byte("identical build")), File: "same_libretro.so.zip"},
		{Core: "stale", Date: "2026-10-23", CRC32: crcOf([]byte("new build")), File: "stale_libretro.so.zip"},
		{Core: "not_installed", Date: "2026-10-23", CRC32: "deadbeef", File: "not_installed_libretro.so.zip"},
	}
	updates := planCoreUpdates(entries, active)
	if len(updates) != 1 {
		t.Fatalf("updates = %+v, want only the stale core", updates)
	}
	if updates[0].Core != "stale" || updates[0].Current != "1.0.0" {
		t.Fatalf("update = %+v", updates[0])
	}
	if !strings.HasSuffix(updates[0].URL, "stale_libretro.so.zip") {
		t.Fatalf("update URL = %q", updates[0].URL)
	}
}

func TestFetchCoreUpdateInstallsIntoThePoolAndVerifiesCRC(t *testing.T) {
	payload := []byte("brand new core binary")
	archive := zestCoreZip(t, "mgba_libretro.so", string(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	allowUpdateTestServer(t, server.URL)
	prevBase := coreUpdateBaseURL
	coreUpdateBaseURL = server.URL + "/"
	t.Cleanup(func() { coreUpdateBaseURL = prevBase })

	pool := t.TempDir()
	build, err := fetchCoreUpdate(context.Background(), server.Client(), coreUpdate{
		Core: "mgba", Date: "2026-10-23", CRC32: crcOf(payload), URL: server.URL + "/mgba_libretro.so.zip",
	}, pool)
	if err != nil {
		t.Fatalf("fetchCoreUpdate: %v", err)
	}
	wantPath := filepath.Join(pool, "mgba", "2026-10-23", "mgba_libretro.so")
	if build.Path != wantPath || !build.Pool {
		t.Fatalf("build = %+v, want %s in the pool", build, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("installed core = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(pool, "mgba", "2026-10-23", "source.json")); err != nil {
		t.Fatalf("provenance file missing: %v", err)
	}
}

func TestFetchCoreUpdateRejectsAChecksumMismatch(t *testing.T) {
	archive := zestCoreZip(t, "mgba_libretro.so", "tampered")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	allowUpdateTestServer(t, server.URL)

	pool := t.TempDir()
	_, err := fetchCoreUpdate(context.Background(), server.Client(), coreUpdate{
		Core: "mgba", Date: "2026-10-23", CRC32: "00000000", URL: server.URL + "/mgba_libretro.so.zip",
	}, pool)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(pool, "mgba")); !os.IsNotExist(statErr) {
		t.Fatalf("a rejected core was written to the pool")
	}
}

func TestFetchCoreUpdateRejectsAnArchiveWithoutTheCore(t *testing.T) {
	archive := zestCoreZip(t, "readme.txt", "not a core")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	allowUpdateTestServer(t, server.URL)

	_, err := fetchCoreUpdate(context.Background(), server.Client(), coreUpdate{
		Core: "mgba", Date: "2026-10-23", CRC32: "0", URL: server.URL + "/x.zip",
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("err = %v, want a missing-core error", err)
	}
}

func TestFetchCoreUpdateRefusesPlainHTTP(t *testing.T) {
	_, err := fetchCoreUpdate(context.Background(), http.DefaultClient, coreUpdate{
		Core: "mgba", CRC32: "0", URL: "http://example.invalid/x.zip",
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("err = %v, want an HTTPS refusal", err)
	}
}

func TestVerifyReleaseDigest(t *testing.T) {
	body := []byte("emulator build")
	sum := sha256.Sum256(body)
	good := "sha256:" + hex.EncodeToString(sum[:])
	if err := verifyReleaseDigest(body, good); err != nil {
		t.Fatalf("good digest rejected: %v", err)
	}
	if err := verifyReleaseDigest(body, ""); err != nil {
		t.Fatalf("a release without a digest should still be accepted: %v", err)
	}
	if err := verifyReleaseDigest([]byte("other"), good); err == nil {
		t.Fatal("a mismatched digest was accepted")
	}
	if err := verifyReleaseDigest(body, "md5:abc"); err == nil {
		t.Fatal("an unsupported digest algorithm was accepted")
	}
}

func TestStandaloneArm64Asset(t *testing.T) {
	cases := map[string]bool{
		"PPSSPP_linux_arm64.tar.gz":                      true,
		"PPSSPP-v1.20.4-anylinux-aarch64.AppImage":       true,
		"mGBA-0.10.5-appimage-arm64.appimage":            true,
		"flycast-linux-aarch64.zip":                      true,
		"PPSSPP_linux_x86_64.tar.gz":                     false,
		"mGBA-0.10.5-win64.zip":                          false,
		"PPSSPP-v1.20.4-Windows-ARM64.zip":               false,
		"PPSSPP-v1.20.4-anylinux-aarch64.AppImage.zsync": false,
	}
	for name, want := range cases {
		if got := standaloneArm64Asset(name); got != want {
			t.Errorf("standaloneArm64Asset(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFetchStandaloneUpdatesPicksTheArm64Asset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/hrydgard/ppsspp/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.20.4","assets":[
			{"name":"PPSSPP_linux_x86_64.tar.gz","browser_download_url":"https://example.invalid/x86.tar.gz"},
			{"name":"PPSSPP_linux_arm64.tar.gz","browser_download_url":"https://example.invalid/arm.tar.gz","digest":"sha256:aa"}
		]}`))
	})
	mux.HandleFunc("/repos/mgba-emu/mgba/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"0.10.5","assets":[
			{"name":"mGBA-0.10.5-win64.zip","browser_download_url":"https://example.invalid/win.zip"}
		]}`))
	})
	mux.HandleFunc("/repos/flyinghead/flycast/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	allowUpdateTestServer(t, server.URL)
	prevAPI := standaloneAPIBase
	standaloneAPIBase = server.URL
	t.Cleanup(func() { standaloneAPIBase = prevAPI })

	found, errs := fetchStandaloneUpdates(context.Background(), server.Client())
	if len(found) != 2 {
		t.Fatalf("found = %+v, want PPSSPP and mGBA", found)
	}
	if found[0].Name != "PPSSPP" || !strings.HasSuffix(found[0].URL, "arm.tar.gz") || found[0].Tag != "v1.20.4" {
		t.Fatalf("PPSSPP entry = %+v", found[0])
	}
	if found[1].URL != "" || !strings.Contains(found[1].Note, "no aarch64") {
		t.Fatalf("mGBA entry = %+v, want a note instead of an offer", found[1])
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "Flycast") {
		t.Fatalf("errs = %v, want one Flycast failure", errs)
	}
}

func TestDownloadStandaloneStagesTheVerifiedArchive(t *testing.T) {
	body := []byte("emulator archive")
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	allowUpdateTestServer(t, server.URL)
	dest := t.TempDir()
	update, err := downloadStandalone(context.Background(), server.Client(), standaloneUpdate{
		Name: "mGBA", Tag: "0.10.5", URL: server.URL + "/mGBA-0.10.5-arm64.AppImage", Digest: digest,
	}, dest)
	if err != nil {
		t.Fatalf("downloadStandalone: %v", err)
	}
	want := filepath.Join(dest, "mgba", "0.10.5", "mGBA-0.10.5-arm64.AppImage")
	if update.Path != want {
		t.Fatalf("path = %q, want %q", update.Path, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != string(body) {
		t.Fatalf("staged file = %q, %v", data, err)
	}
}

func TestEmulatorUpdateReportSummary(t *testing.T) {
	if got := (emulatorUpdateReport{CoresChecked: 4}).summary(); !strings.Contains(got, "All 4 cores are up to date") {
		t.Fatalf("summary = %q", got)
	}
	if got := (emulatorUpdateReport{}).summary(); !strings.Contains(got, "No RetroArch cores") {
		t.Fatalf("summary = %q", got)
	}
	report := emulatorUpdateReport{
		CoresChecked: 4,
		Cores:        []coreBuild{{Core: "mgba"}, {Core: "flycast"}},
		Standalones:  []standaloneUpdate{{Name: "PPSSPP", Path: "/x"}},
		CoreErrors:   []string{"one failed"},
	}
	got := report.summary()
	for _, want := range []string{"2 core(s) added to the pool", "1 emulator build(s) downloaded", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
}
