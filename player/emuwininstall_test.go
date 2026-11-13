package main

// emuwininstall_test.go — the Windows installer's promises: it never unpacks
// outside the emulator's folder, refuses a download whose digest does not
// match, an update keeps the user's files and the version it replaced, and a
// rollback puts that version back.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// emuTestZip builds a zip archive from entry name → contents.
func emuTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// emuTestDigest is GitHub's digest form of data's SHA-256.
func emuTestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// emuTestRoot points the Windows install folders at a fresh temp folder.
func emuTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := emuWinRootsOverride
	emuWinRootsOverride = []string{root}
	t.Cleanup(func() { emuWinRootsOverride = prev })
	return root
}

func emuTestRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// emuReleaseServer plays GitHub for one repository: a "latest release" with a
// single asset, which a test can republish between installs.
type emuReleaseServer struct {
	mu     sync.Mutex
	tag    string
	name   string
	body   []byte
	digest string
}

func (rs *emuReleaseServer) publish(tag, name string, body []byte, digest string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.tag, rs.name, rs.body, rs.digest = tag, name, body, digest
}

func emuNewReleaseServer(t *testing.T, repo string) (*emuReleaseServer, *http.Client) {
	t.Helper()
	rs := &emuReleaseServer{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		switch {
		case r.URL.Path == "/repos/"+repo+"/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q,"size":%d,"digest":%q}]}`,
				rs.tag, rs.name, server.URL+"/download/"+rs.name, len(rs.body), rs.digest)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			_, _ = w.Write(rs.body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	allowUpdateTestServer(t, server.URL)
	prevAPI := standaloneAPIBase
	standaloneAPIBase = server.URL
	t.Cleanup(func() { standaloneAPIBase = prevAPI })
	return rs, server.Client()
}

// TestEmuWinInstallUpdateAndRollBack is the whole path against a local
// "GitHub": a first install lands in its own folder with a manifest, the same
// release again changes nothing, an update keeps the user's files and the
// version it replaced, the catalog sees the managed install, and a rollback
// puts the old version back.
func TestEmuWinInstallUpdateAndRollBack(t *testing.T) {
	root := emuTestRoot(t)
	spec, ok := emuWinSpecFor("xemu")
	if !ok {
		t.Fatal("no xemu install rules")
	}
	rs, client := emuNewReleaseServer(t, spec.Repo)
	v1 := emuTestZip(t, map[string]string{
		"xemu-win/xemu.exe":    "xemu v1",
		"xemu-win/license.txt": "license",
	})
	rs.publish("v0.8.138", "xemu-win-x86_64-release.zip", v1, emuTestDigest(v1))

	result, err := emuWinInstall(context.Background(), client, spec, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	folder := filepath.Join(root, "xemu")
	exe := filepath.Join(folder, "xemu.exe")
	if result.Exe != exe || result.Updated || result.Current {
		t.Fatalf("first install = %+v, want a fresh install at %s", result, exe)
	}
	if got := emuTestRead(t, exe); got != "xemu v1" {
		t.Fatalf("xemu.exe = %q", got)
	}
	if m, ok := emuWinReadManifest(folder); !ok || m.Tag != "v0.8.138" || m.Exe != "xemu.exe" {
		t.Fatalf("manifest = %+v (%v), want v0.8.138 with xemu.exe", m, ok)
	}

	again, err := emuWinInstall(context.Background(), client, spec, nil)
	if err != nil || !again.Current {
		t.Fatalf("same release again = %+v, %v; want nothing to do", again, err)
	}

	if err := os.WriteFile(filepath.Join(folder, "xemu.toml"), []byte("user settings"), 0o644); err != nil {
		t.Fatal(err)
	}
	v2 := emuTestZip(t, map[string]string{
		"xemu-win/xemu.exe":    "xemu v2",
		"xemu-win/license.txt": "license",
		"xemu-win/new.dll":     "added by v2",
	})
	rs.publish("v0.8.139", "xemu-win-x86_64-release.zip", v2, emuTestDigest(v2))
	updated, err := emuWinInstall(context.Background(), client, spec, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.Updated || !updated.Kept {
		t.Fatalf("update = %+v, want an update that kept the old version", updated)
	}
	if got := emuTestRead(t, exe); got != "xemu v2" {
		t.Fatalf("xemu.exe after update = %q", got)
	}
	if got := emuTestRead(t, filepath.Join(folder, "xemu.toml")); got != "user settings" {
		t.Fatalf("the update touched the user's settings: %q", got)
	}

	var xemu emuEntry
	for _, e := range windowsEmulatorEntries([]string{root}, noLookPath) {
		if e.Name == "xemu" {
			xemu = e
		}
	}
	if xemu.Status != emuAvailable || !xemu.Managed || xemu.Version != "v0.8.139" || xemu.KeptVersion != "v0.8.138" {
		t.Fatalf("catalog entry = %+v, want the managed v0.8.139 with v0.8.138 kept", xemu)
	}

	info, err := emuWinRollback("xemu")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if info.Tag != "v0.8.138" {
		t.Fatalf("rolled back to %q, want v0.8.138", info.Tag)
	}
	if got := emuTestRead(t, exe); got != "xemu v1" {
		t.Fatalf("xemu.exe after rollback = %q", got)
	}
	if _, err := os.Stat(filepath.Join(folder, "new.dll")); !os.IsNotExist(err) {
		t.Fatalf("a file the update added survived the rollback (%v)", err)
	}
	if got := emuTestRead(t, filepath.Join(folder, "xemu.toml")); got != "user settings" {
		t.Fatalf("the rollback touched the user's settings: %q", got)
	}
	if m, _ := emuWinReadManifest(folder); m.Tag != "v0.8.138" {
		t.Fatalf("manifest after rollback = %+v, want v0.8.138", m)
	}
	if _, err := os.Stat(emuWinBackupDir(folder)); !os.IsNotExist(err) {
		t.Fatalf("the restored backup is still on disk (%v)", err)
	}
}

// TestEmuWinInstallRefusesAMismatchedDigest: a download whose SHA-256 is not
// the one GitHub published never becomes an install.
func TestEmuWinInstallRefusesAMismatchedDigest(t *testing.T) {
	root := emuTestRoot(t)
	spec, _ := emuWinSpecFor("xemu")
	rs, client := emuNewReleaseServer(t, spec.Repo)
	body := emuTestZip(t, map[string]string{"xemu.exe": "tampered"})
	rs.publish("v0.8.139", "xemu-win-x86_64-release.zip", body, emuTestDigest([]byte("something else")))
	if _, err := emuWinInstall(context.Background(), client, spec, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(root, "xemu")); !os.IsNotExist(err) {
		t.Fatalf("a rejected download left an install behind (%v)", err)
	}
}

// TestEmuExtractZipStaysInsideItsFolder: entries keep their folders, and an
// entry that climbs out of the archive is refused.
func TestEmuExtractZipStaysInsideItsFolder(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.zip")
	if err := os.WriteFile(good, emuTestZip(t, map[string]string{
		"pkg/app.exe":         "binary",
		"pkg/assets/font.ttf": "font",
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := emuExtractZip(context.Background(), good, dest, nil); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got := emuTestRead(t, filepath.Join(dest, "pkg", "assets", "font.ttf")); got != "font" {
		t.Fatalf("nested file = %q", got)
	}
	if got := emuArchiveContentRoot(dest); got != filepath.Join(dest, "pkg") {
		t.Fatalf("content root = %q, want the wrapper folder", got)
	}
	if rel, ok := emuFindLauncher(dest, []string{"app.exe"}); !ok || rel != filepath.Join("pkg", "app.exe") {
		t.Fatalf("launcher = %q (%v), want one folder down", rel, ok)
	}

	evil := filepath.Join(dir, "evil.zip")
	if err := os.WriteFile(evil, emuTestZip(t, map[string]string{"../escaped.txt": "x"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := emuExtractZip(context.Background(), evil, filepath.Join(dir, "evil-out"), nil); err == nil {
		t.Fatal("an entry climbing out of the archive was extracted")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("the escaping entry was written (%v)", err)
	}
}

// TestEmuPickWinAssetXbox: the asset lists xemu and Xenia Canary publish. xemu
// ships a debug build, symbols, AppImages and a macOS zip beside the Windows
// build, sometimes its older name too, and an ARM64 build that only Windows on
// ARM takes; Xenia Canary ships a 7z beside a Linux AppImage (older releases
// a zip).
func TestEmuPickWinAssetXbox(t *testing.T) {
	asset := func(name string) emuGitHubAsset {
		return emuGitHubAsset{Name: name, BrowserDownloadURL: "https://example.invalid/" + name}
	}
	release := []emuGitHubAsset{
		asset("xemu-0.8.139-aarch64.AppImage"),
		asset("xemu-0.8.139-dbg-windows-arm64-pdb.zip"),
		asset("xemu-0.8.139-dbg-windows-arm64.zip"),
		asset("xemu-0.8.139-dbg-windows-x86_64.zip"),
		asset("xemu-0.8.139-macos-universal.zip"),
		asset("xemu-0.8.139-windows-arm64.zip"),
		asset("xemu-0.8.139-windows-x86_64.zip"),
		asset("xemu-0.8.139-x86_64.AppImage"),
	}
	legacy := append([]emuGitHubAsset{asset("xemu-win-aarch64-release.zip"), asset("xemu-win-x86_64-release.zip")}, release...)
	x64Only := []emuGitHubAsset{asset("xemu-0.8.139-dbg-windows-x86_64.zip"), asset("xemu-0.8.139-windows-x86_64.zip")}
	xemu, _ := emuWinSpecFor("xemu")
	for _, tc := range []struct {
		assets []emuGitHubAsset
		goarch string
		want   string
	}{
		{release, "amd64", "xemu-0.8.139-windows-x86_64.zip"},
		{release, "arm64", "xemu-0.8.139-windows-arm64.zip"},
		{legacy, "amd64", "xemu-win-x86_64-release.zip"},
		{legacy, "arm64", "xemu-win-aarch64-release.zip"},
		// No ARM64 build: Windows on ARM runs the x64 one.
		{x64Only, "arm64", "xemu-0.8.139-windows-x86_64.zip"},
	} {
		if got, ok := emuPickWinAssetFor(xemu, tc.assets, tc.goarch); !ok || got.Name != tc.want {
			t.Errorf("xemu on %s picked %q (%v), want %q", tc.goarch, got.Name, ok, tc.want)
		}
	}
	debugOnly := []emuGitHubAsset{asset("xemu-0.8.139-dbg-windows-x86_64.zip"), asset("xemu-0.8.139-x86_64.AppImage")}
	if got, ok := emuPickWinAssetFor(xemu, debugOnly, "amd64"); ok {
		t.Errorf("picked %q: a debug build or an AppImage is not the Windows release", got.Name)
	}

	xenia, ok := emuWinSpecFor(emuXeniaName)
	if !ok {
		t.Fatal("no Xenia Canary install rules")
	}
	for _, tc := range []struct {
		assets []emuGitHubAsset
		want   string
	}{
		{[]emuGitHubAsset{asset("README.md"), asset("xenia_canary_linux.AppImage"), asset("xenia_canary_windows.7z")}, "xenia_canary_windows.7z"},
		{[]emuGitHubAsset{asset("xenia_canary.zip")}, "xenia_canary.zip"},
	} {
		if got, ok := emuPickWinAssetFor(xenia, tc.assets, "amd64"); !ok || got.Name != tc.want {
			t.Errorf("Xenia Canary picked %q (%v), want %q", got.Name, ok, tc.want)
		}
	}
}

// TestEmuCompareReleaseVersions: release tags order as releases — a final
// release after its candidates — so an update never offers an rc over the
// final release it led up to.
func TestEmuCompareReleaseVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v0.1.3", "v0.1.3-rc1", 1},
		{"0.1.3-rc2", "0.1.3-rc1", 1},
		{"v0.1.4-rc1", "v0.1.3", 1},
		{"v0.8.139", "v0.8.138", 1},
		{"v2.6", "2.6", 0},
		{"1.0-beta2", "1.0-rc1", -1},
		{"v0.1-11800", "v0.1-11752", 1},
		{"v0.1-11752", "v0.1", 1},
	} {
		if got := emuCompareReleaseVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := emuCompareReleaseVersions(tc.b, tc.a); got != -tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.b, tc.a, got, -tc.want)
		}
	}
	if !emuWinUpdateAvailable(emuWinManifest{Tag: "v0.1.4-rc1"}, emuWinRelease{Tag: "v0.1.4"}) {
		t.Error("the final release must replace its release candidate")
	}
	if emuWinUpdateAvailable(emuWinManifest{Tag: "v0.1.4"}, emuWinRelease{Tag: "v0.1.4-rc2"}) {
		t.Error("a release candidate must never replace its final release")
	}
}

func TestEmuDigestMatches(t *testing.T) {
	sum := sha256.Sum256([]byte("payload"))
	hexSum := hex.EncodeToString(sum[:])
	if err := emuDigestMatches(hexSum, ""); err != nil {
		t.Fatalf("no digest must pass: %v", err)
	}
	if err := emuDigestMatches(hexSum, "sha256:"+strings.ToUpper(hexSum)); err != nil {
		t.Fatalf("matching digest refused: %v", err)
	}
	if err := emuDigestMatches(hexSum, "sha256:00"); err == nil {
		t.Fatal("a wrong digest passed")
	}
	if err := emuDigestMatches(hexSum, "md5:abc"); err == nil {
		t.Fatal("an unsupported digest passed")
	}
}

// RetroArch unzipped (or installed) as RetroArch-Win64 inside a searched
// folder — Downloads, a Steam library — is the catalog's RetroArch.
func TestEmuWinDetectFindsRetroArchWin64Folder(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "RetroArch-Win64", "retroarch.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("ra"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, _, _ := emuWinDetect(winEmuSpec{"RetroArch", []string{"retroarch.exe"}}, []string{root}, noLookPath)
	if found != exe {
		t.Fatalf("found %q, want %q", found, exe)
	}
}
