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
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// TestParseCoreIndexRequiresRealDates pins the date field, which is not just
// display text: a fetched core is filed under <pool>/<core>/<date>/. The line
// with "../../.." (ten characters, the old length test's only requirement)
// used to survive parsing and become a directory name.
func TestParseCoreIndexRequiresRealDates(t *testing.T) {
	text := strings.Join([]string{
		"../../.. 16647efd vecx_libretro.so.zip",
		"2026-13-45 16647efd fceumm_libretro.so.zip",
		"2026-1-2 1234abcd gambatte_libretro.so.zip",
		"2026-10-23 16647efd mednafen_supergrafx_libretro.so.zip",
	}, "\n")
	entries := parseCoreIndex(text)
	if len(entries) != 1 {
		t.Fatalf("entries = %d (%+v), want only the real date", len(entries), entries)
	}
	if entries[0].Core != "mednafen_supergrafx" || entries[0].Date != "2026-10-23" {
		t.Fatalf("kept %+v, want the mednafen_supergrafx line", entries[0])
	}
	for _, bad := range []string{"../../..", "2026-13-45", "2026-1-2", "", "2026-10-23T00"} {
		if validCoreIndexDate(bad) {
			t.Errorf("validCoreIndexDate(%q) = true, want false", bad)
		}
	}
	if !validCoreIndexDate("2026-10-23") {
		t.Error("a real date was refused")
	}
}

// TestJ2MEIndexFilesAndPair pins the release index's companion-file lines
// (FreeJ2ME-Plus's jar, j2me_install.go): the core reader still skips them,
// a jar name is a plain file name, and the jar pairs with the core of its date.
func TestJ2MEIndexFilesAndPair(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	text := strings.Join([]string{
		"2026-11-23 1234abcd freej2me_plus_libretro.so.zip",
		"2026-11-16 " + sum + " freej2me_plus-lr.jar freej2me_plus",
		"2026-11-23 " + strings.ToUpper(sum) + " freej2me_plus-lr.jar freej2me_plus",
		"2026-11-23 " + sum + " ../evil-lr.jar freej2me_plus",
		"2026-11-23 " + sum + " sub/x-lr.jar freej2me_plus",
		"2026-11-23 " + sum[:63] + " short-lr.jar freej2me_plus",
		"2026-11-23 " + sum + " notajar.zip freej2me_plus",
		"2026-13-01 " + sum + " baddate-lr.jar freej2me_plus",
	}, "\n")
	if cores := parseCoreIndex(text); len(cores) != 1 || cores[0].Core != "freej2me_plus" || cores[0].CRC32 != "1234abcd" {
		t.Fatalf("parseCoreIndex = %+v, want only the core line", cores)
	}
	files := j2meParseIndexFiles(text)
	if len(files) != 1 || files[0].File != "freej2me_plus-lr.jar" || files[0].Date != "2026-11-23" || files[0].SHA256 != sum {
		t.Fatalf("j2meParseIndexFiles = %+v, want the last good jar line, lower-cased", files)
	}
	entry, jar, err := j2meIndexPair(text, "freej2me_plus")
	if err != nil || entry.Date != "2026-11-23" || jar.File != "freej2me_plus-lr.jar" {
		t.Fatalf("j2meIndexPair = %+v, %+v, %v", entry, jar, err)
	}
	stale := "2026-11-30 1234abcd freej2me_plus_libretro.so.zip\n2026-11-23 " + sum + " freej2me_plus-lr.jar freej2me_plus\n"
	if _, _, err := j2meIndexPair(stale, "freej2me_plus"); err == nil {
		t.Fatal("a jar of another date was paired with the core")
	}
	if _, _, err := j2meIndexPair(text, "mgba"); err == nil {
		t.Fatal("a core the index does not list was paired")
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
	if leftovers, err := os.ReadDir(pool); err != nil || len(leftovers) != 0 {
		t.Fatalf("a rejected download left temporary files in the pool: %v, %v", leftovers, err)
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

// TestRetroArchPackageReadInPart pins raget.go's reading of a TrimUI SD
// package: only range requests (in small blocks here, so they cross and
// evict), RetroArch's folder found under the package's top folder, the binary
// and its version, and the folder written without overwriting a file the
// card kept, without the lean parts and without anything outside the folder.
func TestRetroArchPackageReadInPart(t *testing.T) {
	prevBlock, prevDir := raGetBlock, raGetDirBlock
	raGetBlock, raGetDirBlock = 1000, 700
	t.Cleanup(func() { raGetBlock, raGetDirBlock = prevBlock, prevDir })

	// Bytes that do not compress, so the package spans many blocks.
	noise := func(n int, seed uint32) string {
		b := make([]byte, n)
		for i := range b {
			seed = seed*1664525 + 1013904223
			b[i] = byte(seed >> 24)
		}
		return string(b)
	}
	binary := "\x7fELF fake\x00zlib\x001.3.1\x00libpng 1.6.43\x00RetroArch 1.22.2\x00v1.21.0\x00" + noise(6000, 1)
	files := []struct{ name, body string }{
		{"pkg/Emus/GBA/launch.sh", "#!/bin/sh\n" + noise(3000, 4)},
		{"pkg/RetroArch/ra64.trimui", binary},
		{"pkg/RetroArch/retroarch.cfg", "video_fullscreen_x = \"1024\"\nvideo_driver = \"gl\"\n"},
		{"pkg/RetroArch/.retroarch/cores/a_libretro.so", noise(9000, 2)},
		{"pkg/RetroArch/.retroarch/assets/big.png", noise(5000, 3)},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("zip: %v", err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatalf("zip: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip: %v", err)
	}
	dir := t.TempDir()
	pkg := filepath.Join(dir, "base.zip")
	if err := os.WriteFile(pkg, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ranged atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Errorf("a request without a range for %s", r.URL.Path)
		}
		ranged.Add(1)
		http.ServeFile(w, r, pkg)
	}))
	defer server.Close()
	allowUpdateTestServer(t, server.URL)

	ctx := context.Background()
	asset := emuGitHubAsset{Name: "base.zip", BrowserDownloadURL: server.URL + "/base.zip", Size: int64(buf.Len())}
	c, ok, err := raGetOpenAsset(ctx, server.Client(), raGetSource{Repo: "trimui/assets_brick", Model: DeviceTrimuiBrick}, "20241105", asset)
	if err != nil || !ok {
		t.Fatalf("raGetOpenAsset = %v, %v", ok, err)
	}
	if c.Prefix != "pkg/RetroArch/" || !c.Full || c.Files != 4 {
		t.Fatalf("candidate = prefix %q, full %v, %d files", c.Prefix, c.Full, c.Files)
	}

	staged := filepath.Join(dir, "stage", "ra64.trimui")
	if err := raGetFetch(ctx, &c, c.Bin, staged, nil); err != nil {
		t.Fatalf("raGetFetch: %v", err)
	}
	if got, _ := os.ReadFile(staged); string(got) != binary {
		t.Fatalf("the binary came back as %d bytes, want %d", len(got), len(binary))
	}
	if v := raGetBinaryVersion(staged); v != "1.22.2" {
		t.Fatalf("version = %q, want 1.22.2", v)
	}

	target := filepath.Join(dir, "card", "RetroArch")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "retroarch.cfg"), []byte("mine\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	plan, dirs, total, packed, err := raGetFolderPlan(&c, target, true)
	if err != nil {
		t.Fatalf("raGetFolderPlan: %v", err)
	}
	created, err := raGetWriteFolder(ctx, &c, target, plan, dirs, total, packed, func(int64, int64) {})
	if err != nil {
		t.Fatalf("raGetWriteFolder: %v", err)
	}
	if len(created) != 1 || created[0] != ".retroarch/cores/a_libretro.so" {
		t.Fatalf("written = %v, want only the core", created)
	}
	if got, _ := os.ReadFile(filepath.Join(target, ".retroarch", "cores", "a_libretro.so")); string(got) != files[3].body {
		t.Fatalf("the core came back as %d bytes", len(got))
	}
	if got, _ := os.ReadFile(filepath.Join(target, "retroarch.cfg")); string(got) != "mine\n" {
		t.Fatalf("the card's own retroarch.cfg was replaced: %q", got)
	}
	for _, absent := range []string{filepath.Join(target, "ra64.trimui"), filepath.Join(target, ".retroarch", "assets", "big.png"),
		filepath.Join(dir, "card", "Emus")} {
		if _, err := os.Stat(absent); err == nil {
			t.Fatalf("%s was written", absent)
		}
	}
	if n := ranged.Load(); n < 10 {
		t.Fatalf("only %d range requests: the small blocks were not used", n)
	}

	for _, tc := range []struct {
		in       string
		a, b, n  int64
		accepted bool
	}{
		{"bytes 0-0/1", 0, 0, 1, true},
		{"bytes 0-99/1000", 0, 99, 1000, true},
		{"bytes 5-5/*", 5, 5, -1, true},
		{"bytes 9-3/1000", 0, 0, 0, false},
		{"bytes 0-999/999", 0, 0, 0, false},
		{"items 0-9/10", 0, 0, 0, false},
	} {
		a, b, n, ok := raGetContentRange(tc.in)
		if ok != tc.accepted || (ok && (a != tc.a || b != tc.b || n != tc.n)) {
			t.Fatalf("raGetContentRange(%q) = %d, %d, %d, %v", tc.in, a, b, n, ok)
		}
	}
}

// raGet7zFixture is a TrimUI SD package as a 7z, 1,319 bytes, built byte by
// byte with Python's lzma module and read back whole by libarchive: two LZMA2
// blocks — Emus/GBA/launch.sh, RetroArch/.retroarch/assets/big.png,
// …/cores/a_libretro.so, RetroArch/ra64.trimui (RetroArch 1.22.2, dated
// 2025-04-30 12:00 UTC, CRC 4746ead6) and RetroArch/retroarch.cfg in the
// first; Roms/readme.txt, …/cores/b_libretro.so (CRC b62b23e9) and
// Roms/zz_after.bin in the second — the folders, an empty
// …/config/empty.txt, file times, a plain header.
const raGet7zFixture = `N3q8ryccAATi5J/FIQEAAAAAAADmAwAAAAAAAM8y8KvgWekAx10AEYhCRj30FjRzCg2kNp3mcg11
G5RdfzABgEVJkdcnHTSxM7FVbApIX1U80R6j3/bu67IpiC3LinRvRFUIDqVVRPr7B1ZBtCEFpxzG
tZFq7+tUMMfr0z1aMjXkXf2vky1Qi95Dm7lUP+7sG37bMQePL5jVhiPplWGJftZBI4FogZexx8F0
dcMPvFMHjlnij8AxA1vIns4fj6gjgUY2KII8oyp40A2Zp5JUuBGlyWKdPTMmQ6aHBEOKvXaWiQ+G
a0gDpyxQWyAl4ADgJX8ASl0AOB1Kz/aDM8rzlUTKxVqQPyVX2ArM9y+Avngg4zn5QQD31rl/5lw7
0nhV6pM4VlF3PESmA4LpiEqxQRO316dYmovQ4IfLDN/OQAAAAQQGAAIJgM9SAAcLAgABISEBEAEh
IQEQDMDqWaWAAAgNBQMJipaT7KOMl6yJYJBoCgFadix1I6nrqyE+6VTW6kZHe8O/1GG1k2LpIyu2
f4VeowAABRAOAv4BDwEBEYL/AEUAbQB1AHMAAABFAG0AdQBzAC8ARwBCAEEAAABSAGUAdAByAG8A
QQByAGMAaAAAAFIAZQB0AHIAbwBBAHIAYwBoAC8ALgByAGUAdAByAG8AYQByAGMAaAAAAFIAZQB0
AHIAbwBBAHIAYwBoAC8ALgByAGUAdAByAG8AYQByAGMAaAAvAGEAcwBzAGUAdABzAAAAUgBlAHQA
cgBvAEEAcgBjAGgALwAuAHIAZQB0AHIAbwBhAHIAYwBoAC8AYwBvAHIAZQBzAAAAUgBlAHQAcgBv
AEEAcgBjAGgALwAuAHIAZQB0AHIAbwBhAHIAYwBoAC8AYwBvAG4AZgBpAGcAAABFAG0AdQBzAC8A
RwBCAEEALwBsAGEAdQBuAGMAaAAuAHMAaAAAAFIAZQB0AHIAbwBBAHIAYwBoAC8ALgByAGUAdABy
AG8AYQByAGMAaAAvAGEAcwBzAGUAdABzAC8AYgBpAGcALgBwAG4AZwAAAFIAZQB0AHIAbwBBAHIA
YwBoAC8ALgByAGUAdAByAG8AYQByAGMAaAAvAGMAbwByAGUAcwAvAGEAXwBsAGkAYgByAGUAdABy
AG8ALgBzAG8AAABSAGUAdAByAG8AQQByAGMAaAAvAHIAYQA2ADQALgB0AHIAaQBtAHUAaQAAAFIA
ZQB0AHIAbwBBAHIAYwBoAC8AcgBlAHQAcgBvAGEAcgBjAGgALgBjAGYAZwAAAFIAbwBtAHMALwBy
AGUAYQBkAG0AZQAuAHQAeAB0AAAAUgBlAHQAcgBvAEEAcgBjAGgALwAuAHIAZQB0AHIAbwBhAHIA
YwBoAC8AYwBvAHIAZQBzAC8AYgBfAGwAaQBiAHIAZQB0AHIAbwAuAHMAbwAAAFIAbwBtAHMALwB6
AHoAXwBhAGYAdABlAHIALgBiAGkAbgAAAFIAZQB0AHIAbwBBAHIAYwBoAC8ALgByAGUAdAByAG8A
YQByAGMAaAAvAGMAbwBuAGYAaQBnAC8AZQBtAHAAdAB5AC4AdAB4AHQAAAAUgIIBAABgKDZzPdoB
AGAoNnM92gEAYCg2cz3aAQBgKDZzPdoBAGAoNnM92gEAYCg2cz3aAQBgKDZzPdoBAGAoNnM92gEA
YCg2cz3aAQBgKDZzPdoBAGAXZse52wEAYCg2cz3aAQBgKDZzPdoBAGAoNnM92gEAYCg2cz3aAQBg
KDZzPdoBAAA=`

// TestRetroArch7zPackage pins raget_7z.go: a 7z package's directory read in
// range requests, RetroArch's folder and the binary's time found in it, one
// pass over both blocks (in small range requests) that writes only
// RetroArch's files the card does not have, each checked against its CRC, the
// lean plan, a binary-only pass, and the move into the card's folder.
func TestRetroArch7zPackage(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(raGet7zFixture, "\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	prevDir, prev7z := raGetDirBlock, raGet7zBlock
	raGetDirBlock, raGet7zBlock = 300, 200
	t.Cleanup(func() { raGetDirBlock, raGet7zBlock = prevDir, prev7z })
	dir := t.TempDir()
	pkg := filepath.Join(dir, "sd_base.7z")
	if err := os.WriteFile(pkg, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ranged atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Errorf("a request without a range for %s", r.URL.Path)
		}
		ranged.Add(1)
		http.ServeFile(w, r, pkg)
	}))
	defer server.Close()
	allowUpdateTestServer(t, server.URL)

	ctx := context.Background()
	asset := emuGitHubAsset{Name: "sd_base.7z", BrowserDownloadURL: server.URL + "/sd_base.7z", Size: int64(len(data))}
	src := raGetSource{Repo: "trimui/assets_smartpro", Model: DeviceTrimuiSmartPro}
	c, ok, err := raGet7zOpenAsset(ctx, server.Client(), src, "20250430", asset)
	if err != nil || !ok {
		t.Fatalf("raGet7zOpenAsset = %v, %v", ok, err)
	}
	if c.Prefix != "RetroArch/" || !c.Full || c.Files != 6 {
		t.Fatalf("candidate = prefix %q, full %v, %d files", c.Prefix, c.Full, c.Files)
	}
	if want := time.Date(2025, 4, 30, 12, 0, 0, 0, time.UTC); !c.When.Equal(want) {
		t.Fatalf("binary time = %v, want %v", c.When, want)
	}
	if c.crc() != "4746ead6" || c.binSize() != 6060 {
		t.Fatalf("binary = crc %s, %d bytes", c.crc(), c.binSize())
	}

	target := filepath.Join(dir, "card", "RetroArch")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "retroarch.cfg"), []byte("mine\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rels := func(ws []raGet7zWanted) string {
		var out []string
		for _, w := range ws {
			out = append(out, w.rel)
		}
		return strings.Join(out, ",")
	}
	lean, _, _, err := raGet7zPlan(&c, target, true, true)
	if err != nil {
		t.Fatalf("raGet7zPlan: %v", err)
	}
	if got := rels(lean); got != "ra64.trimui,.retroarch/cores/a_libretro.so,.retroarch/cores/b_libretro.so,.retroarch/config/empty.txt" {
		t.Fatalf("lean plan = %s", got)
	}

	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bin, err := raGet7zStage(ctx, &c, stage, 0, target, true, 0, nil)
	if err != nil {
		t.Fatalf("raGet7zStage: %v", err)
	}
	if got, _ := os.ReadFile(bin); crcOf(got) != "4746ead6" {
		t.Fatalf("the binary came back as %d bytes", len(got))
	}
	if v := raGetBinaryVersion(bin); v != "1.22.2" {
		t.Fatalf("version = %q, want 1.22.2", v)
	}
	if !c.stagedFolder || rels(c.stagedFiles) != ".retroarch/assets/big.png,.retroarch/cores/a_libretro.so,.retroarch/cores/b_libretro.so,.retroarch/config/empty.txt" {
		t.Fatalf("staged = folder %v, files %s", c.stagedFolder, rels(c.stagedFiles))
	}
	created, err := raGet7zPlaceFolder(ctx, &c, target, func(int64, int64) {})
	if err != nil {
		t.Fatalf("raGet7zPlaceFolder: %v", err)
	}
	if len(created) != 4 {
		t.Fatalf("written = %v", created)
	}
	for rel, crc := range map[string]string{
		".retroarch/assets/big.png":      "abeba923",
		".retroarch/cores/a_libretro.so": "54e93e21",
		".retroarch/cores/b_libretro.so": "b62b23e9",
		".retroarch/config/empty.txt":    "00000000",
	} {
		got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(rel)))
		if err != nil || crcOf(got) != crc {
			t.Fatalf("%s came back wrong (%v)", rel, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(target, "retroarch.cfg")); string(got) != "mine\n" {
		t.Fatalf("the card's own retroarch.cfg was replaced: %q", got)
	}
	for _, absent := range []string{filepath.Join(target, "ra64.trimui"), filepath.Join(dir, "card", "Emus"), filepath.Join(dir, "card", "Roms")} {
		if _, err := os.Stat(absent); err == nil {
			t.Fatalf("%s was written", absent)
		}
	}

	// An update's pass: the binary alone.
	c2, ok, err := raGet7zOpenAsset(ctx, server.Client(), src, "20250430", asset)
	if err != nil || !ok {
		t.Fatalf("raGet7zOpenAsset again = %v, %v", ok, err)
	}
	bin2, err := raGet7zStage(ctx, &c2, stage, 1, "", true, 0, nil)
	if err != nil || c2.stagedFolder || len(c2.stagedFiles) != 0 {
		t.Fatalf("binary-only pass = %v, folder %v, files %s", err, c2.stagedFolder, rels(c2.stagedFiles))
	}
	if got, _ := os.ReadFile(bin2); crcOf(got) != "4746ead6" {
		t.Fatalf("the binary-only pass gave %d bytes", len(got))
	}
	if n := ranged.Load(); n < 8 {
		t.Fatalf("only %d range requests: the small blocks were not used", n)
	}
}

// TestCoreSurveyJudge pins how corecompat.go reads the cores workflow's
// survey of a buildbot build: GLIBC_2.35 and GLIBCXX_3.4.30 load on JukaHub's
// glibc where RetroArch may use it and only there; a version above the
// runtime's, a library nowhere and another architecture are final; JukaHub's
// stand-ins count once RetroArch may use the runtime.
func TestCoreSurveyJudge(t *testing.T) {
	defs := func(vs ...string) map[string]bool {
		m := map[string]bool{}
		for _, v := range vs {
			m[v] = true
		}
		return m
	}
	firmware := map[string]coreSurveyLib{
		"libc.so.6":      {found: true, defs: defs("GLIBC_2.17", "GLIBC_2.28", "GLIBC_2.33")},
		"libm.so.6":      {found: true, defs: defs("GLIBC_2.17", "GLIBC_2.29")},
		"libstdc++.so.6": {found: true, defs: defs("GLIBCXX_3.4", "GLIBCXX_3.4.24", "CXXABI_1.3")},
		"libGLESv2.so.2": {found: true},
	}
	env := func(open bool) coreSurveyEnv {
		return coreSurveyEnv{
			firmware: func(s string) coreSurveyLib { return firmware[s] },
			runtime:  func(s string) coreSurveyLib { return coreSurveyLib{found: coreSurveyRuntimeSonames[s]} },
			raHas:    func(s string) bool { return s == "libSDL2-2.0.so.0" },
			standIn: func(s string) bool {
				_, ok := devStubLibs[s]
				return ok && open
			},
			runtimeOpen: open,
			runtimeWhy:  "the setting says so",
			ceilings:    raGlibcCeilings,
		}
	}
	ppsspp := coreSurveyEntry{Arch: "aarch64", CRC32: "abcd0123",
		Needed: []string{"libGLESv2.so.2", "libstdc++.so.6", "libm.so.6", "libc.so.6", "libSDL2-2.0.so.0"},
		Versions: map[string][]string{"libc.so.6": {"GLIBC_2.17", "GLIBC_2.34"}, "libm.so.6": {"GLIBC_2.35"},
			"libstdc++.so.6": {"GLIBCXX_3.4.30", "CXXABI_1.3.13"}}}
	if fit := coreSurveyJudge(ppsspp, env(true)); fit.firmware || !fit.runtime || fit.problem != "" {
		t.Fatalf("GLIBCXX_3.4.30, runtime open: %+v", fit)
	}
	fit := coreSurveyJudge(ppsspp, env(false))
	if fit.firmware || fit.runtime || fit.final || !strings.Contains(fit.problem, "the setting says so") {
		t.Fatalf("GLIBCXX_3.4.30, runtime closed: %+v", fit)
	}
	if note := coreGapPlain(fit.problem); !strings.Contains(note, "GCC 12") || !strings.Contains(note, "not of glibc") {
		t.Fatalf("coreGapPlain = %q", note)
	}
	newer := coreSurveyEntry{Arch: "aarch64", Needed: []string{"libc.so.6"}, Versions: map[string][]string{"libc.so.6": {"GLIBC_2.41"}}}
	if fit := coreSurveyJudge(newer, env(true)); fit.runtime || !fit.final || !strings.Contains(fit.problem, "GLIBC_2.41") {
		t.Fatalf("GLIBC_2.41: %+v", fit)
	}
	gl := coreSurveyEntry{Arch: "aarch64", Needed: []string{"libGL.so.1", "libc.so.6"}}
	if fit := coreSurveyJudge(gl, env(true)); fit.runtime || !fit.final || !strings.Contains(fit.problem, "desktop OpenGL") {
		t.Fatalf("libGL: %+v", fit)
	}
	usb := coreSurveyEntry{Arch: "aarch64", Needed: []string{"libusb-1.0.so.0", "libc.so.6"}}
	if fit := coreSurveyJudge(usb, env(true)); !fit.runtime || fit.firmware {
		t.Fatalf("libusb beside JukaHub's stand-ins: %+v", fit)
	}
	if fit := coreSurveyJudge(coreSurveyEntry{Arch: "x86_64"}, env(true)); !fit.final || fit.runtime {
		t.Fatalf("x86_64: %+v", fit)
	}
	doc, err := coreSurveyParse([]byte(`{"format":1,"generated":"2026-11-23T05:41:00Z","cores":{"PPSSPP":{"crc32":"ABCD0123","arch":"aarch64"}}}`))
	if err != nil || doc.Cores["ppsspp"].CRC32 != "abcd0123" {
		t.Fatalf("coreSurveyParse = %+v, %v", doc, err)
	}
	if _, err := coreSurveyParse([]byte(`{"format":2,"generated":"2026-11-23T05:41:00Z","cores":{"x":{}}}`)); err == nil {
		t.Fatal("a survey of another format was read")
	}
}

// raCardTestELF is a minimal 64-bit little-endian ELF header for machine,
// with a PT_INTERP naming interp when it is not "".
func raCardTestELF(machine uint16, interp string) []byte {
	b := make([]byte, 64)
	copy(b, "\x7fELF\x02\x01\x01")
	binary.LittleEndian.PutUint16(b[16:], 2) // ET_EXEC
	binary.LittleEndian.PutUint16(b[18:], machine)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64) // e_ehsize
	binary.LittleEndian.PutUint16(b[54:], 56) // e_phentsize
	binary.LittleEndian.PutUint16(b[58:], 64) // e_shentsize
	if interp == "" {
		return b
	}
	name := append([]byte(interp), 0)
	binary.LittleEndian.PutUint64(b[32:], 64) // e_phoff
	binary.LittleEndian.PutUint16(b[56:], 1)  // e_phnum
	ph := make([]byte, 56)
	binary.LittleEndian.PutUint32(ph[0:], 3) // PT_INTERP
	binary.LittleEndian.PutUint32(ph[4:], 4)
	binary.LittleEndian.PutUint64(ph[8:], 120) // p_offset
	binary.LittleEndian.PutUint64(ph[32:], uint64(len(name)))
	binary.LittleEndian.PutUint64(ph[40:], uint64(len(name)))
	binary.LittleEndian.PutUint64(ph[48:], 1)
	return append(append(b, ph...), name...)
}

// TestRetroArchTheGamesCall pins racard.go (JH-X6HRVE): the RetroArch the
// stock firmware's and CrossMix's launch scripts call is read out of them —
// $RA_DIR, a cd before a relative ./ra64.trimui, bare names and longer names
// left out — and the card misses it, whatever else the search finds, when
// that binary is not there; it is broken when it is there but cannot start
// (another processor's, its loader absent), and a script counts only where
// the launch scripts call it.
func TestRetroArchTheGamesCall(t *testing.T) {
	stock := "#!/bin/sh\necho $0 $*\nRA_DIR=/mnt/SDCARD/RetroArch\nEMU_DIR=/mnt/SDCARD/Emus/SFC\ncd $RA_DIR/\n\n" +
		"HOME=$RA_DIR/ $RA_DIR/ra64.trimui -v -L $RA_DIR/.retroarch/cores/snes9x_libretro.so \"$@\"\n"
	crossmix := "RA_DIR=/mnt/SDCARD/RetroArch\ncd $RA_DIR\n./ra64.trimui -v $NET_PARAM -L .retroarch/cores/x.so \"$@\"\n" +
		"killall ra64.trimui\ncp $RA_DIR/ra64.trimui.bak /tmp\n$UNSET/ra32.trimui\ncat $RA_DIR/retroarch.cfg\n"
	want := filepath.FromSlash("/mnt/SDCARD/RetroArch/ra64.trimui")
	for name, text := range map[string]string{"stock": stock, "crossmix": crossmix} {
		if got := raScriptCallsText(text, "/mnt/SDCARD/Emus/SFC"); len(got) != 1 || got[0] != want {
			t.Errorf("%s calls %q, want only %s", name, got, want)
		}
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "RetroArch", raGetBinaryName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "Apps", "RetroArch", "retroarch")
	none := func() string { return "" }
	found := func() string { return other }
	put := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(target, data, 0o755); err != nil {
			t.Fatal(err)
		}
		// A file system with coarse times could keep the last answer.
		raBinaryMemo.mu.Lock()
		raBinaryMemo.m = nil
		raBinaryMemo.mu.Unlock()
	}
	check := func(what string, st raCardState, missing, broken bool, problem string) {
		t.Helper()
		if st.Missing != missing || st.Broken != broken || !strings.Contains(st.Problem, problem) || (problem == "" && st.Problem != "") {
			t.Errorf("%s: %+v, want missing %v, broken %v, problem %q", what, st, missing, broken, problem)
		}
	}
	check("called, not there", raCardJudge(target, target, 41, found), true, false, "missing")
	check("not named, not there, another found", raCardJudge(target, "", 0, found), false, false, "missing")
	if st := raCardJudge(target, "", 0, found); st.Found != other {
		t.Errorf("the other RetroArch was not taken: %+v", st)
	}
	put(raCardTestELF(62, ""))
	check("an x86-64 build", raCardJudge(target, target, 41, found), true, true, "x86-64")
	loader := filepath.Join(dir, "ld-linux-aarch64.so.1")
	if err := os.WriteFile(loader, []byte("ld"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(raCardTestELF(183, loader))
	check("an aarch64 build", raCardJudge(target, target, 41, none), false, false, "")
	if filepath.IsAbs("/nonexistent-jukahub") {
		put(raCardTestELF(183, "/nonexistent-jukahub/ld-linux-aarch64.so.1"))
		check("its loader absent", raCardJudge(target, target, 41, none), true, true, "loader")
	}
	put([]byte("#!/bin/sh\nexec ./real \"$@\"\n"))
	check("a script the scripts call", raCardJudge(target, target, 41, none), false, false, "")
	check("a script no script names", raCardJudge(target, "", 0, none), true, true, "script")

	if filepath.Separator == '/' {
		// The scripts on a card: the two that start RetroArch outvote the
		// standalone emulator's.
		card := filepath.Join(dir, "card")
		ra := filepath.ToSlash(filepath.Join(card, "RetroArch"))
		for emu, text := range map[string]string{
			"GBA": strings.ReplaceAll(stock, "/mnt/SDCARD/RetroArch", ra),
			"SFC": strings.ReplaceAll(crossmix, "/mnt/SDCARD/RetroArch", ra),
			"PSP": "#!/bin/sh\ncd /mnt/SDCARD/Emus/PSP\n./PPSSPPSDL \"$@\"\n",
		} {
			if err := os.MkdirAll(filepath.Join(card, "Emus", emu), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(card, "Emus", emu, "launch.sh"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if calls, n := raScriptCalls([]string{card}); calls != ra+"/"+raGetBinaryName || n != 2 {
			t.Errorf("raScriptCalls = %q, %d", calls, n)
		}
	}
}
