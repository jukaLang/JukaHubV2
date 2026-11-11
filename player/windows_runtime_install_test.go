package main

// Tests for the on-device Windows runtime installer. They run against a local
// HTTP server and a synthetic Hangover-shaped tarball, so the real download,
// extraction and wrapper path is exercised without network access or 500 MB
// download.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

// serveHangoverArchive serves archive over HTTPS — the installer refuses
// plain HTTP — and points the installer's client at the test certificate.
func serveHangoverArchive(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(archive)
	}))
	prev := windowsRuntimeClient
	windowsRuntimeClient = srv.Client()
	t.Cleanup(func() {
		windowsRuntimeClient = prev
		srv.Close()
	})
	return srv
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
	srv := serveHangoverArchive(t, archive)
	if err := installWineRuntime("http://127.0.0.1/hangover.tar"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("a plain-HTTP runtime URL gave %v, want a refusal", err)
	}

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

// A hostile archive must not be able to write outside the runtime folder.
func TestInstallWineRuntimeRejectsTraversal(t *testing.T) {
	root := withWindowsRuntimeRoot(t)
	archive := makeHangoverTar(t, map[string]string{
		"../../escaped.txt": "nope",
		"bin/wine":          "#!/bin/sh\n",
	})
	srv := serveHangoverArchive(t, archive)

	err := installWineRuntime(srv.URL + "/hangover.tar")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error = %v, want an escape rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); statErr == nil {
		t.Fatal("traversal entry was written outside the runtime folder")
	}
}

func TestRuntimeExtractionRejectsPreexistingSymlinkEscape(t *testing.T) {
	for _, format := range []string{"tar", "zip"} {
		t.Run(format, func(t *testing.T) {
			dest, outside := t.TempDir(), t.TempDir()
			if err := os.Symlink(outside, filepath.Join(dest, "escape")); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("creating symlinks needs extra privileges on Windows: %v", err)
				}
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "test."+format)
			if format == "tar" {
				if err := os.WriteFile(archive, makeHangoverTar(t, map[string]string{"escape/payload": "unsafe"}), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
					w, err := zw.Create("escape/payload")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write([]byte("unsafe")); err != nil {
						t.Fatal(err)
					}
				})
			}
			if err := extractRuntimeArchive(archive, dest); err == nil {
				t.Fatal("extraction through outside link succeeded")
			}
			if _, err := os.Stat(filepath.Join(outside, "payload")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("outside path exists or stat failed: %v", err)
			}
		})
	}
}

func TestRuntimeExtractionDoesNotOverwriteOutsideHardLink(t *testing.T) {
	dest, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(dest, "victim")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "test.tar")
	if err := os.WriteFile(archive, makeHangoverTar(t, map[string]string{"victim": "replacement"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeArchive(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "original" {
		t.Fatalf("outside hard link was modified: %q, %v", got, err)
	}
}

// The pack's wine.json decides what the handheld downloads and runs, so a
// description that names Wine 11 (which aborts on the 4.9 kernel), a host off
// the allowlist, an archive its URL does not end in, or a checksum that
// contradicts the published one is refused.
func TestWineTarballSpecValidate(t *testing.T) {
	t.Setenv("JUKAHUB_WINE_ALLOW_NEWER", "")
	if err := wineNativeSpec.validate(); err != nil {
		t.Fatalf("the pinned Wine does not validate: %v", err)
	}
	bad := map[string]func(*wineTarballSpec){
		"wine 11":         func(s *wineTarballSpec) { s.Version = "11.0" },
		"no version":      func(s *wineTarballSpec) { s.Version = "" },
		"path in version": func(s *wineTarballSpec) { s.Version = "10.0/../x" },
		"other host":      func(s *wineTarballSpec) { s.URL = "https://example.com/wine-10.0-amd64-wow64.tar.xz" },
		"plain http": func(s *wineTarballSpec) {
			s.URL = "http://github.com/Kron4ek/Wine-Builds/releases/download/10.0/wine-10.0-amd64-wow64.tar.xz"
		},
		"asset mismatch": func(s *wineTarballSpec) { s.Asset = "wine-10.0-amd64.tar.xz" },
		"not tar.xz":     func(s *wineTarballSpec) { s.Asset = "wine.zip" },
		"short sha":      func(s *wineTarballSpec) { s.SHA256 = "abc" },
		"other sha":      func(s *wineTarballSpec) { s.SHA256 = strings.Repeat("0", 64) },
		"negative size":  func(s *wineTarballSpec) { s.Size = -1 },
		"huge size":      func(s *wineTarballSpec) { s.Size = wineMaxTarball + 1 },
	}
	for name, change := range bad {
		spec := wineNativeSpec
		change(&spec)
		if err := spec.validate(); err == nil {
			t.Errorf("%s: validate accepted %+v", name, spec)
		}
	}
	t.Setenv("JUKAHUB_WINE_ALLOW_NEWER", "1")
	newer := wineNativeSpec
	newer.Version = "11.0"
	if err := newer.validate(); err != nil {
		t.Errorf("JUKAHUB_WINE_ALLOW_NEWER=1 still refuses Wine 11: %v", err)
	}
}

func TestReadWineTarballSpec(t *testing.T) {
	t.Setenv("JUKAHUB_WINE_ALLOW_NEWER", "")
	dir := t.TempDir()
	body := `{"version":" 10.0 ","asset":"wine-10.0-amd64-wow64.tar.xz",` +
		`"url":"https://github.com/Kron4ek/Wine-Builds/releases/download/10.0/wine-10.0-amd64-wow64.tar.xz",` +
		`"sha256":"` + strings.ToUpper(wineNativeSpec.SHA256) + `","size":65111222}`
	if err := os.WriteFile(filepath.Join(dir, wineSpecName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := readWineTarballSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "10.0" || spec.SHA256 != wineNativeSpec.SHA256 || spec.Size != 65111222 {
		t.Errorf("readWineTarballSpec = %+v", spec)
	}
	if err := os.WriteFile(filepath.Join(dir, wineSpecName), []byte(`{"version":"10.0"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readWineTarballSpec(dir); err == nil {
		t.Error("a truncated wine.json was accepted")
	}
}

// The wrappers carry their paths quoted for sh; reading them back must give
// the same paths, apostrophes included, or resolveWine would judge the wrong
// files.
func TestWineWrapperPathsRoundTrip(t *testing.T) {
	layout := wineLayout{
		Spec:   wineNativeSpec,
		Tree:   "/mnt/SDCARD/Juka's Apps/data/windows-runtime/wine-10.0",
		Box64:  "/mnt/SDCARD/Juka's Apps/data/windows-runtime/pack/box64/box64",
		X64Lib: "/mnt/SDCARD/Juka's Apps/data/windows-runtime/pack/box64/x64lib",
	}
	script := wineWrapperScript(layout, "wine")
	if got := wineScriptVar(script, "JUKAHUB_WINE_TREE"); got != layout.Tree {
		t.Errorf("JUKAHUB_WINE_TREE = %q, want %q", got, layout.Tree)
	}
	if got := wineScriptVar(script, "JUKAHUB_BOX64"); got != layout.Box64 {
		t.Errorf("JUKAHUB_BOX64 = %q, want %q", got, layout.Box64)
	}
	if !strings.Contains(script, wineWrapperTag) {
		t.Error("the wrapper does not carry its tag")
	}

	wrapper := filepath.Join(t.TempDir(), "wine")
	hangoverBin := "/data/windows-runtime/hangover/usr/bin/wine"
	if err := writeWineWrapper(wrapper, hangoverBin, "/data/windows-runtime/hangover/usr"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got := hangoverWrapperBinary(string(data)); got != hangoverBin {
		t.Errorf("hangoverWrapperBinary = %q, want %q", got, hangoverBin)
	}
}

// WestonPack comes from a repository file: what arrives must be a SquashFS 4
// image of a plausible size, never a Git LFS pointer or an HTML page.
func TestWestonImageProblem(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, head []byte, size int64) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, head, 0o644); err != nil {
			t.Fatal(err)
		}
		if size > int64(len(head)) {
			if err := os.Truncate(p, size); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	const size = int64(30 << 20)
	head := make([]byte, 96)
	copy(head, "hsqs")
	binary.LittleEndian.PutUint32(head[12:], 1<<17)
	binary.LittleEndian.PutUint16(head[20:], 1)
	binary.LittleEndian.PutUint16(head[22:], 17)
	binary.LittleEndian.PutUint16(head[28:], 4)
	binary.LittleEndian.PutUint64(head[40:], uint64(size-100))
	good := write("good.squashfs", head, size)
	if problem := westonImageProblem(good, true); problem != "" {
		t.Errorf("a sound image was refused: %s", problem)
	}

	old := append([]byte(nil), head...)
	binary.LittleEndian.PutUint16(old[28:], 3)
	if problem := westonImageProblem(write("old.squashfs", old, size), true); problem == "" {
		t.Error("a SquashFS 3 image was accepted")
	}
	long := append([]byte(nil), head...)
	binary.LittleEndian.PutUint64(long[40:], uint64(size+1))
	if problem := westonImageProblem(write("cut.squashfs", long, size), true); problem == "" {
		t.Error("an image shorter than its superblock says was accepted")
	}
	lfs := write("lfs.squashfs", []byte("version https://git-lfs.github.com/spec/v1\noid sha256:00\nsize 55000000\n"), 0)
	if problem := westonImageProblem(lfs, false); !strings.Contains(problem, "LFS") {
		t.Errorf("an LFS pointer gave %q", problem)
	}
	if problem := westonImageProblem(write("page.squashfs", []byte("<!DOCTYPE html>"), size), false); problem == "" {
		t.Error("an HTML page was accepted")
	}
	if problem := westonImageProblem(write("small.squashfs", head, 1<<20), false); problem == "" {
		t.Error("a 1 MB image was accepted")
	}
}

// The WestonPack download may follow GitHub's redirect to its raw-file hosts,
// for PortMaster-New's repository only.
func TestWestonRedirectAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://raw.githubusercontent.com/PortsMaster/PortMaster-New/main/runtimes/weston_pkg_0.2.aarch64.squashfs":         true,
		"https://media.githubusercontent.com/media/PortsMaster/PortMaster-New/main/runtimes/weston_pkg_0.2.aarch64.squashfs": true,
		"https://raw.githubusercontent.com/someone/else/main/weston_pkg_0.2.aarch64.squashfs":                                false,
		"https://raw.githubusercontent.com/PortsMaster/PortMaster-New/../../someone/else/x":                                  false,
		"https://media.githubusercontent.com/PortsMaster/PortMaster-New/main/x":                                              false,
		"http://raw.githubusercontent.com/PortsMaster/PortMaster-New/main/x":                                                 false,
		"https://example.com/PortsMaster/PortMaster-New/main/x":                                                              false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := westonRedirectAllowed(u); got != want {
			t.Errorf("westonRedirectAllowed(%s) = %v, want %v", raw, got, want)
		}
	}
}

// The prefix image is 2 GB when the card has room, else 1 GB, else refused.
func TestWineImageSize(t *testing.T) {
	t.Setenv("JUKAHUB_WINE_PREFIX_IMAGE_MB", "")
	for free, want := range map[int64]int64{
		-1:      2 << 30,
		6 << 30: 2 << 30,
		2 << 30: 1 << 30,
	} {
		if got, err := wineImageSize(free); err != nil || got != want {
			t.Errorf("wineImageSize(%d) = %d, %v; want %d", free, got, err, want)
		}
	}
	if _, err := wineImageSize(1 << 30); err == nil {
		t.Error("a card with 1 GB free got a 1 GB image")
	}
	t.Setenv("JUKAHUB_WINE_PREFIX_IMAGE_MB", "700")
	if got, err := wineImageSize(6 << 30); err != nil || got != 700<<20 {
		t.Errorf("JUKAHUB_WINE_PREFIX_IMAGE_MB=700 gave %d, %v", got, err)
	}
}

// A prefix that already exists is used where it is; JUKAHUB_WINEPREFIX wins
// over everything.
func TestChooseWinePrefixKeepsWorkingPrefix(t *testing.T) {
	t.Setenv("JUKAHUB_WINEPREFIX", "")
	prefix := t.TempDir()
	for _, name := range []string{"system.reg", "user.reg"} {
		if err := os.WriteFile(filepath.Join(prefix, name), []byte("WINE REGISTRY Version 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := chooseWinePrefix(map[string]string{"WINEPREFIX": prefix}, false); got.Dir != filepath.Clean(prefix) || got.Problem != "" {
		t.Errorf("chooseWinePrefix kept %+v, want %s", got, prefix)
	}
	override := t.TempDir()
	t.Setenv("JUKAHUB_WINEPREFIX", override)
	if got := chooseWinePrefix(map[string]string{"WINEPREFIX": prefix}, false); got.Dir != filepath.Clean(override) {
		t.Errorf("JUKAHUB_WINEPREFIX gave %+v, want %s", got, override)
	}
}

func TestDXVKNeedsVulkan(t *testing.T) {
	for version, want := range map[string][2]int{"1.10.3": {1, 1}, "2.7.1": {1, 3}, "": {1, 3}} {
		if major, minor := dxvkNeedsVulkan(version); major != want[0] || minor != want[1] {
			t.Errorf("dxvkNeedsVulkan(%q) = %d.%d, want %d.%d", version, major, minor, want[0], want[1])
		}
	}
}
