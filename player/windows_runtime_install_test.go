package main

// Tests for the on-device Windows runtime installer. They run against a local
// HTTP server and a synthetic Hangover-shaped tarball, so the real download,
// extraction and wrapper path is exercised without network access or 500 MB
// download.

import (
	"archive/tar"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func makeHangoverTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range entries {
		mode := int64(0o644)
		if strings.HasSuffix(name, "/wine") || strings.HasSuffix(name, "wine") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeDeb builds a minimal Debian package whose data.tar.xz holds files. The
// real Hangover release uses exactly this layout, so the test exercises the
// same unpack path without a 280 MB download.
func makeDeb(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	for name, body := range files {
		mode := int64(0o644)
		if strings.HasSuffix(name, "wine") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: "./" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xw.Write(data.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}

	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	writeMember := func(name string, body []byte) {
		header := fmt.Sprintf("%-16s%-12s%-6s%-6s%-8s%-10s`\n", name, "0", "0", "0", "100644", strconv.Itoa(len(body)))
		if len(header) != 60 {
			t.Fatalf("ar header %q is %d bytes, want 60", name, len(header))
		}
		ar.WriteString(header)
		ar.Write(body)
		if len(body)%2 != 0 {
			ar.WriteByte('\n')
		}
	}
	writeMember("debian-binary", []byte("2.0\n"))
	writeMember("control.tar.xz", nil)
	writeMember("data.tar.xz", compressed.Bytes())
	return ar.Bytes()
}

func withWindowsRuntimeRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevDir, prevVerify := windowsRuntimeDirOverride, windowsRuntimeVerify
	windowsRuntimeDirOverride = dir
	// Do not spawn a real wine --version in unit tests.
	windowsRuntimeVerify = func(string) error { return nil }
	setWindowsRuntimeStatus("", "", false)
	t.Cleanup(func() {
		windowsRuntimeDirOverride, windowsRuntimeVerify = prevDir, prevVerify
		setWindowsRuntimeStatus("", "", false)
	})
	return dir
}

// The Hangover release publishes several distro builds and a separate DLL
// archive; the installer must pick an ARM64 runtime archive.
func TestSelectHangoverAssetPrefersArm64Runtime(t *testing.T) {
	assets := []hangoverAsset{
		{Name: "hangover_11.16_dlls.tar", BrowserDownloadURL: "https://example.com/dlls.tar"},
		{Name: "hangover_11.16_x86_64.tar", BrowserDownloadURL: "https://example.com/x86.tar"},
		{Name: "hangover_11.16_ubuntu2404_noble_arm64.tar", BrowserDownloadURL: "https://example.com/noble.tar"},
		{Name: "hangover_11.16_debian12_bookworm_arm64.tar", BrowserDownloadURL: "https://example.com/bookworm.tar"},
	}
	name, url, err := selectHangoverAsset(assets)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://example.com/bookworm.tar" {
		t.Errorf("picked %q (%s), want the Debian 12 ARM64 archive", name, url)
	}
	if _, _, err := selectHangoverAsset([]hangoverAsset{{Name: "hangover_dlls.tar"}}); err == nil {
		t.Error("an archive set with no ARM64 runtime did not fail")
	}
}

// installWineRuntime must download, extract, and leave a wrapper that
// resolveWine finds at data/windows-runtime/bin/wine.
func TestInstallWineRuntimeFromArchive(t *testing.T) {
	root := withWindowsRuntimeRoot(t)
	archive := makeHangoverTar(t, map[string]string{
		"hangover/bin/wine":            "#!/bin/sh\nexit 0\n",
		"hangover/lib/wine/libwine.so": "payload",
		"hangover/usr/bin/other":       "x",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	if err := installWineRuntime(srv.URL + "/hangover_11.16_debian12_bookworm_arm64.tar"); err != nil {
		t.Fatal(err)
	}

	wrapper := filepath.Join(root, "bin", "wine")
	info, err := os.Stat(wrapper)
	if err != nil {
		t.Fatalf("wrapper was not created: %v", err)
	}
	// Windows does not track Unix permission bits, so the executable check
	// only applies to the handheld/Linux build where the wrapper must run.
	if !IsWindows() && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("wrapper is not executable: %v", info.Mode())
	}
	script, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("hangover", "bin", "wine"); !strings.Contains(string(script), want) {
		t.Errorf("wrapper does not run the extracted wine binary %q:\n%s", want, script)
	}
	if !strings.Contains(string(script), "LD_LIBRARY_PATH") {
		t.Errorf("wrapper does not export LD_LIBRARY_PATH:\n%s", script)
	}
	if _, err := os.Stat(filepath.Join(root, "hangover", "lib", "wine", "libwine.so")); err != nil {
		t.Errorf("runtime payload was not extracted: %v", err)
	}

	// The wrapper is the launcher's resolution point.
	t.Setenv("JUKAHUB_WINE", "")
	if got, err := resolveWine(nil); err != nil || got != wrapper {
		t.Errorf("resolveWine = %q, %v; want %q", got, err, wrapper)
	}
}

// The published Hangover tarball contains .deb packages, not an installed Wine
// tree. The installer must unpack every package's data.tar and merge them, or
// the runtime would never be found.
func TestInstallWineRuntimeUnpacksDebPackages(t *testing.T) {
	root := withWindowsRuntimeRoot(t)
	wine := makeDeb(t, map[string]string{
		"usr/bin/wine":                           "#!/bin/sh\nexit 0\n",
		"usr/lib/wine/libwine.so":                "payload",
		"usr/lib/aarch64-linux-gnu/libwine.so.1": "lib",
	})
	tools := makeDeb(t, map[string]string{
		"usr/lib/wine/wow64.dll": "dll",
	})
	archive := makeHangoverTar(t, map[string]string{
		"hangover-wine_11.16~bookworm_arm64.deb": string(wine),
		"hangover-wowbox64_11.16_arm64.deb":      string(tools),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	if err := installWineRuntime(srv.URL + "/hangover_11.16_debian12_bookworm_arm64.tar"); err != nil {
		t.Fatal(err)
	}

	wrapper := filepath.Join(root, "bin", "wine")
	script, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatalf("wrapper was not created: %v", err)
	}
	if want := filepath.Join("hangover", "usr", "bin", "wine"); !strings.Contains(string(script), want) {
		t.Errorf("wrapper does not run %q:\n%s", want, script)
	}
	for _, file := range []string{
		filepath.Join("hangover", "usr", "lib", "wine", "libwine.so"),
		filepath.Join("hangover", "usr", "lib", "wine", "wow64.dll"),
	} {
		if _, err := os.Stat(filepath.Join(root, file)); err != nil {
			t.Errorf("package payload %s was not extracted: %v", file, err)
		}
	}
	t.Setenv("JUKAHUB_WINE", "")
	if got, err := resolveWine(nil); err != nil || got != wrapper {
		t.Errorf("resolveWine = %q, %v; want %q", got, err, wrapper)
	}
}

// Wine's package aliases winecfg/notepad to wine with symlinks. SD cards are
// FAT/exFAT and reject symlinks, so extraction must fall back to copying.
func TestExtractRuntimeArchiveMaterializesLinks(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("#!/bin/sh\nexit 0\n")
	if err := tw.WriteHeader(&tar.Header{Name: "usr/bin/wine", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "usr/bin/winecfg", Linkname: "wine", Mode: 0o777, Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "tree.tar")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeArchive(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "usr", "bin", "winecfg"))
	if err != nil {
		t.Fatalf("winecfg alias was not created: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("winecfg alias = %q, want the wine launcher", got)
	}

	// A link that escapes the extraction root is still rejected.
	escape := t.TempDir()
	var bad bytes.Buffer
	bw := tar.NewWriter(&bad)
	if err := bw.WriteHeader(&tar.Header{Name: "usr/bin/evil", Linkname: "../../../outside", Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	badArchive := filepath.Join(t.TempDir(), "evil.tar")
	if err := os.WriteFile(badArchive, bad.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeArchive(badArchive, escape); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error = %v, want an escape rejection", err)
	}
}

// A hostile archive must not be able to write outside the runtime folder.
func TestInstallWineRuntimeRejectsTraversal(t *testing.T) {
	root := withWindowsRuntimeRoot(t)
	archive := makeHangoverTar(t, map[string]string{
		"../../escaped.txt": "nope",
		"bin/wine":          "#!/bin/sh\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	err := installWineRuntime(srv.URL + "/hangover.tar")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error = %v, want an escape rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); statErr == nil {
		t.Fatal("traversal entry was written outside the runtime folder")
	}
}

func TestInstallWineRuntimeRejectsUnsupportedArchive(t *testing.T) {
	withWindowsRuntimeRoot(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PK\x03\x04"))
	}))
	defer srv.Close()
	err := installWineRuntime(srv.URL + "/hangover.deb")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v, want an unsupported-archive error", err)
	}
}

// The setup page must expose the two install actions above the status text.
func TestWindowsSetupRowsExposeInstaller(t *testing.T) {
	setWindowsRuntimeStatus("", "", false)
	t.Cleanup(func() { setWindowsRuntimeStatus("", "", false) })
	rows := windowsAppsSetupRows(windowsRuntimeStatus{})
	if len(rows) != windowsSetupRowCount {
		t.Fatalf("setup rows = %d, want %d", len(rows), windowsSetupRowCount)
	}
	if !strings.Contains(rows[windowsSetupRowWine].Title, "Wine") {
		t.Errorf("first row %q is not the Wine install", rows[windowsSetupRowWine].Title)
	}
	if !strings.Contains(rows[windowsSetupRowDisplay].Title, "WestonPack") {
		t.Errorf("second row %q is not the display install", rows[windowsSetupRowDisplay].Title)
	}
	if !rows[windowsSetupRowWine].Bad {
		t.Error("a missing Wine runtime is not flagged")
	}
	ready := windowsAppsSetupRows(windowsRuntimeStatus{Wine: "/x/wine", Weston: "/x/westonwrap.sh"})
	if ready[windowsSetupRowWine].Bad || ready[windowsSetupRowDisplay].Bad {
		t.Error("an installed runtime is still reported as missing")
	}
}

// Only the row the user activated may say "installing"; otherwise starting the
// display download paints the Wine row as busy and hides the Wine action.
func TestWindowsSetupRowsTrackInstallTarget(t *testing.T) {
	endWindowsRuntimeInstall()
	t.Cleanup(func() {
		endWindowsRuntimeInstall()
		setWindowsRuntimeStatus("", "", false)
	})

	if !beginWindowsRuntimeInstall("wine", "downloading") {
		t.Fatal("could not begin a Wine install")
	}
	rows := windowsAppsSetupRows(windowsRuntimeStatus{})
	if rows[windowsSetupRowWine].Status != "installing" {
		t.Errorf("Wine row = %q, want installing", rows[windowsSetupRowWine].Status)
	}
	if rows[windowsSetupRowDisplay].Status != "missing" {
		t.Errorf("display row = %q, want missing while Wine installs", rows[windowsSetupRowDisplay].Status)
	}
	endWindowsRuntimeInstall()

	if !beginWindowsRuntimeInstall("weston", "downloading") {
		t.Fatal("could not begin a WestonPack install")
	}
	rows = windowsAppsSetupRows(windowsRuntimeStatus{})
	if rows[windowsSetupRowDisplay].Status != "installing" {
		t.Errorf("display row = %q, want installing", rows[windowsSetupRowDisplay].Status)
	}
	if rows[windowsSetupRowWine].Status != "missing" {
		t.Errorf("Wine row = %q, want missing while WestonPack installs", rows[windowsSetupRowWine].Status)
	}
	endWindowsRuntimeInstall()

	setWindowsRuntimeStatus("Wine install failed", "no network", false)
	rows = windowsAppsSetupRows(windowsRuntimeStatus{})
	if rows[windowsSetupRowWine].Status != "failed" {
		t.Errorf("Wine row = %q, want failed after an error", rows[windowsSetupRowWine].Status)
	}
	setWindowsRuntimeStatus("WestonPack not installed", "no unsquashfs", false)
	rows = windowsAppsSetupRows(windowsRuntimeStatus{})
	if rows[windowsSetupRowDisplay].Status != "failed" {
		t.Errorf("display row = %q, want failed after an error", rows[windowsSetupRowDisplay].Status)
	}
}
