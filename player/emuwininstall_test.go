package main

// emuwininstall_test.go — the Windows installer's promises: it picks the one
// asset that is each emulator's portable Windows x64 build, it never unpacks
// outside the emulator's folder, an update keeps the user's files and the
// version it replaced, and a rollback puts that version back.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// TestEmuPickWinAssetMatchesRealReleaseNames runs each rule against the asset
// lists the projects really publish: the portable Windows x64 archive wins over
// installers, ARM builds, debug symbols, checksum side-cars and other systems.
func TestEmuPickWinAssetMatchesRealReleaseNames(t *testing.T) {
	cases := []struct {
		emulator string
		assets   []string
		want     string
	}{
		{"PPSSPP", []string{"PPSSPP-v1.20.4-Windows-ARM64.zip", "PPSSPP-v1.20.4-Windows-x64.zip", "PPSSPP-v1.20.4-anylinux-aarch64.AppImage", "PPSSPP_linux_x86_64.tar.gz"}, "PPSSPP-v1.20.4-Windows-x64.zip"},
		{"DuckStation", []string{"duckstation-windows-arm64-release.zip", "duckstation-windows-x64-installer.exe", "duckstation-windows-x64-sse2-release.zip", "duckstation-windows-x64-release.zip", "DuckStation-x64.AppImage"}, "duckstation-windows-x64-release.zip"},
		{"PCSX2", []string{"PCSX2-v2.8.2-windows-x64-installer.exe", "pcsx2-v2.8.2-windows-x64-Qt.7z", "pcsx2-v2.8.2-linux-appimage-x64-Qt.AppImage", "pcsx2-v2.8.2-macos-Qt.tar.xz"}, "pcsx2-v2.8.2-windows-x64-Qt.7z"},
		{"melonDS", []string{"melonDS-1.1-appimage-x86_64.zip", "melonDS-1.1-windows-x86_64.zip", "melonDS-1.1-macOS-universal.zip"}, "melonDS-1.1-windows-x86_64.zip"},
		{"mGBA", []string{"mGBA-0.10.5-win32.7z", "mGBA-0.10.5-win64-installer.exe", "mGBA-0.10.5-win64.7z", "mGBA-0.10.5-switch.7z", "mGBA-0.10.5.tar.xz"}, "mGBA-0.10.5-win64.7z"},
		{"Cemu", []string{"Cemu-2.6-x86_64.AppImage", "cemu-2.6-macos-12-x64.dmg", "cemu-2.6-windows-x64.zip"}, "cemu-2.6-windows-x64.zip"},
		{"Vita3K", []string{"ubuntu-latest.zip", "macos-latest.dmg", "windows-latest.zip", "Vita3K-x86_64.AppImage"}, "windows-latest.zip"},
		{"RPCS3", []string{"rpcs3-v0.0.43-20404-e8979d31_win64_aarch64_clang.7z", "rpcs3-v0.0.43-20404-e8979d31_win64_msvc.7z.sha256", "rpcs3-v0.0.43-20404-e8979d31_win64_x86_64_clang.7z", "rpcs3-v0.0.43-20404-e8979d31_win64_msvc.7z"}, "rpcs3-v0.0.43-20404-e8979d31_win64_msvc.7z"},
		{"xemu", []string{"xemu-0.8.139-windows-x86_64.zip", "xemu-win-aarch64-release.zip", "xemu-win-x86_64-release-pdb.zip", "xemu-win-x86_64-release.zip", "xemu-v0.8.139-x86_64.AppImage"}, "xemu-win-x86_64-release.zip"},
		{"PPSSPP", []string{"PPSSPP_linux_x86_64.tar.gz", "PPSSPP-v1.20.4-Windows-ARM64.zip"}, ""},
	}
	for _, tc := range cases {
		spec, ok := emuWinSpecFor(tc.emulator)
		if !ok {
			t.Fatalf("no install rules for %s", tc.emulator)
		}
		assets := make([]emuGitHubAsset, 0, len(tc.assets))
		for _, name := range tc.assets {
			assets = append(assets, emuGitHubAsset{Name: name, BrowserDownloadURL: "https://github.com/x/" + name})
		}
		got, found := emuPickWinAsset(spec, assets)
		if tc.want == "" {
			if found {
				t.Errorf("%s picked %q from a release with no Windows x64 build", tc.emulator, got.Name)
			}
			continue
		}
		if !found || got.Name != tc.want {
			t.Errorf("%s picked %q (found=%v), want %q", tc.emulator, got.Name, found, tc.want)
		}
	}
}

// TestEmuWinSpecsCoverTheCatalog keeps the two tables in step: every cataloged
// Windows emulator has install rules, and every installable one names a launcher
// the catalog can find.
func TestEmuWinSpecsCoverTheCatalog(t *testing.T) {
	for _, entry := range winEmuCatalog {
		spec, ok := emuWinSpecFor(entry.name)
		if !ok {
			t.Errorf("%s has no install rules", entry.name)
			continue
		}
		if !spec.installable() && spec.Site == "" {
			t.Errorf("%s can neither be installed nor sent to its download page", entry.name)
		}
		if len(emuWinLauncherNames(entry.name)) == 0 {
			t.Errorf("%s has no launcher names", entry.name)
		}
	}
	if spec, ok := emuWinSpecFor("Dolphin"); !ok || spec.installable() {
		t.Error("Dolphin publishes no GitHub build; its tile must open the download page")
	}
}

func TestEmuWinUpdateAvailable(t *testing.T) {
	cases := []struct {
		name      string
		installed emuWinManifest
		latest    emuWinRelease
		want      bool
	}{
		{"newer version", emuWinManifest{Tag: "v1.20.3"}, emuWinRelease{Tag: "v1.20.4"}, true},
		{"same version", emuWinManifest{Tag: "v1.20.4"}, emuWinRelease{Tag: "v1.20.4"}, false},
		{"installed is newer", emuWinManifest{Tag: "2.7"}, emuWinRelease{Tag: "2.6"}, false},
		{"rolling tag rebuilt", emuWinManifest{Tag: "latest", Digest: "sha256:aa"}, emuWinRelease{Tag: "latest", Digest: "sha256:bb"}, true},
		{"rolling tag unchanged", emuWinManifest{Tag: "latest", Digest: "sha256:aa"}, emuWinRelease{Tag: "latest", Digest: "SHA256:AA"}, false},
		{"rolling tag without digest", emuWinManifest{Tag: "continuous", AssetDate: "2026-10-01T00:00:00Z"}, emuWinRelease{Tag: "continuous", AssetDate: "2026-11-01T00:00:00Z"}, true},
		{"commit builds are not numbers", emuWinManifest{Tag: "build-e8979d31b3a2"}, emuWinRelease{Tag: "build-1f857121aaaa"}, true},
		{"nightly date moved", emuWinManifest{Tag: "nightly-2026-11-01"}, emuWinRelease{Tag: "nightly-2026-11-08"}, true},
		{"rolled back install", emuWinManifest{Tag: "rolled-back"}, emuWinRelease{Tag: "v0.8.139"}, true},
		{"unknown installed tag", emuWinManifest{}, emuWinRelease{Tag: "v1"}, false},
	}
	for _, tc := range cases {
		if got := emuWinUpdateAvailable(tc.installed, tc.latest); got != tc.want {
			t.Errorf("%s: update = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEmuRetroArchStableVersions(t *testing.T) {
	listing := `<a href="/stable/1.9.0/">1.9.0</a>
<a href="/stable/1.22.2/">1.22.2</a> <a href="/stable/1.21.0/">1.21.0</a>
<a href="/stable/2026.10.25/">dated folder</a> <a href="1.22.2/">again</a>`
	got := emuRetroArchStableVersions(listing)
	want := []string{"1.22.2", "1.21.0", "1.9.0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("versions = %v, want %v", got, want)
	}
}

// TestEmuResolveRetroArchSkipsAVersionWithoutAWindowsBuild: a fresh stable
// folder can exist before its Windows archive does; the newest one that has it
// is the one to install.
func TestEmuResolveRetroArchSkipsAVersionWithoutAWindowsBuild(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stable/":
			_, _ = w.Write([]byte(`<a href="/stable/1.22.1/">1.22.1</a><a href="/stable/1.22.2/">1.22.2</a>`))
		case "/stable/1.22.1/windows/x86_64/RetroArch.7z":
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	allowUpdateTestServer(t, server.URL)
	prevStable, prevNightly := emuRetroArchStableURL, emuRetroArchNightlyURL
	emuRetroArchStableURL = server.URL + "/stable/"
	emuRetroArchNightlyURL = server.URL + "/nightly/RetroArch.7z"
	t.Cleanup(func() { emuRetroArchStableURL, emuRetroArchNightlyURL = prevStable, prevNightly })

	release, err := emuResolveRetroArch(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if release.Tag != "1.22.1" || !strings.HasSuffix(release.URL, "/stable/1.22.1/windows/x86_64/RetroArch.7z") {
		t.Fatalf("release = %+v, want the 1.22.1 Windows build", release)
	}
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

func TestEmuFriendlyError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.Canceled, "Cancelled"},
		{errors.New("HTTP 403 from https://api.github.com/x"), "GitHub is limiting"},
		{errors.New("HTTP 404 from https://github.com/x"), "not on the server"},
		{errors.New("HTTP 502 from https://github.com/x"), "having trouble"},
	}
	for _, tc := range cases {
		if got := emuFriendlyError(tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("emuFriendlyError(%v) = %q, want it to mention %q", tc.err, got, tc.want)
		}
	}
}
