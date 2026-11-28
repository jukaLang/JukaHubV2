package main

// MED-19 — the pinned QuickJS-NG runtime for yt-dlp on Windows.
//
// The tests pin the release values, prove that an install is refused unless
// the download matches both the published SHA-256 and the exact size, prove
// that a failed check leaves a working runtime alone, and prove that the
// stream attempts put yt-dlp's default clients first only when a runtime is
// beside the helper.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestYtDlpJSRuntimePin(t *testing.T) {
	asset, ok := ytdlpJSRuntimePinnedAsset("windows", "amd64")
	if !ok {
		t.Fatal("no pinned QuickJS-NG asset for windows/amd64")
	}
	want := ytdlpJSRuntimeAsset{
		Name:   "qjs-windows-x86_64.exe",
		SHA256: "ff03398d74ea6e6bd3a8e58058f7003225abcac229d7d57775b7aaae812a49c7",
		Size:   1873152,
		URL:    "https://github.com/quickjs-ng/quickjs/releases/download/v0.18.0/qjs-windows-x86_64.exe",
	}
	if asset != want {
		t.Fatalf("windows/amd64 asset = %+v, want %+v", asset, want)
	}
	if _, ok := ytdlpJSRuntimePinnedAsset("windows", "386"); !ok {
		t.Error("no pinned QuickJS-NG asset for windows/386")
	}
	if _, ok := ytdlpJSRuntimePinnedAsset("linux", "amd64"); ok {
		t.Error("a platform this installer does not cover has a pin")
	}
	if _, ok := ytdlpJSRuntimePinnedAsset("windows", "arm64"); ok {
		t.Error("windows/arm64 has no release asset, but a pin was returned")
	}
}

func TestParseQuickJSVersion(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0.18.0\r\n", "0.18.0"},
		{"QuickJS-NG version 0.18.0\n", "0.18.0"},
		{"v0.18.0", "0.18.0"},
		{"QuickJS-NG, version 0.18.0 (x86_64)", "0.18.0"},
		{"not a version", ""},
		{"", ""},
	} {
		if got := parseQuickJSVersion(tc.in); got != tc.want {
			t.Errorf("parseQuickJSVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// serveRuntime returns a test server that answers with body.
func serveRuntime(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestYtDlpJSRuntimeInstallVerifies(t *testing.T) {
	// The tool downloader refuses anything under 4 KiB, like a proxy's error
	// page; a test payload has to clear that floor.
	good := bytes.Repeat([]byte("pinned quickjs-ng runtime bytes\n"), 200)
	sum := sha256.Sum256(good)
	dir := t.TempDir()
	target := filepath.Join(dir, ytdlpJSRuntimeName())
	asset := ytdlpJSRuntimeAsset{
		Name:   "qjs-test.exe",
		SHA256: hex.EncodeToString(sum[:]),
		Size:   int64(len(good)),
		URL:    serveRuntime(t, good).URL + "/qjs.exe",
	}
	got, err := ytdlpInstallJSRuntime(context.Background(), dir, asset)
	if err != nil {
		t.Fatalf("a verified install failed: %v", err)
	}
	if got != target {
		t.Fatalf("installed to %q, want %q", got, target)
	}
	data, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(data, good) {
		t.Fatalf("installed bytes = %q, %v", data, err)
	}

	// A payload that does not match the pin must not replace the good copy.
	tampered := bytes.Repeat([]byte("tampered quickjs-ng bytes\n"), 200)
	asset.URL = serveRuntime(t, tampered).URL + "/qjs.exe"
	if _, err := ytdlpInstallJSRuntime(context.Background(), dir, asset); err == nil {
		t.Fatal("an install with a wrong SHA-256 succeeded")
	}
	data, err = os.ReadFile(target)
	if err != nil || !bytes.Equal(data, good) {
		t.Fatalf("a failed check replaced the runtime: %q, %v", data, err)
	}

	// A download of the wrong length is refused even when the hash matches
	// what was expected of the shorter file.
	asset.URL = serveRuntime(t, good).URL + "/qjs.exe"
	asset.Size = int64(len(good)) + 1
	if _, err := ytdlpInstallJSRuntime(context.Background(), dir, asset); err == nil {
		t.Fatal("an install with a wrong size succeeded")
	}
}

// TestYtDlpJSRuntimeOffer covers the Patch row: it appears when the runtime is
// missing or not the pinned file, and disappears when the pinned size is there.
func TestYtDlpJSRuntimeOffer(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the pinned runtime is installed on Windows only")
	}
	asset, ok := ytdlpJSRuntimePinnedAsset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Skipf("no pin for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	dir := t.TempDir()
	ytDlpPath := filepath.Join(dir, "yt-dlp.exe")
	if err := os.WriteFile(ytDlpPath, []byte("helper"), 0o644); err != nil {
		t.Fatal(err)
	}
	label, _, ok := ytdlpJSRuntimeOffer(ytDlpPath)
	if !ok || !strings.Contains(label, "Install") {
		t.Fatalf("missing runtime offer = %q, %v", label, ok)
	}

	// The pinned size (the installer's own file) means nothing to install.
	qjs := filepath.Join(dir, ytdlpJSRuntimeName())
	f, err := os.Create(qjs)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(asset.Size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if label, _, ok := ytdlpJSRuntimeOffer(ytDlpPath); ok {
		t.Fatalf("a pinned-size runtime still offers %q", label)
	}

	// An older, smaller copy is offered an update.
	if err := os.WriteFile(qjs, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	label, _, ok = ytdlpJSRuntimeOffer(ytDlpPath)
	if !ok || !strings.Contains(label, "Update") {
		t.Fatalf("old runtime offer = %q, %v", label, ok)
	}
}

func TestYtDlpJSRuntimePathBesideHelper(t *testing.T) {
	dir := t.TempDir()
	ytDlpPath := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(ytDlpPath, []byte("helper"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ytDlpJSRuntimePath(ytDlpPath); got != "" {
		t.Fatalf("found a runtime that is not there: %q", got)
	}
	qjs := filepath.Join(dir, ytdlpJSRuntimeName())
	if err := os.WriteFile(qjs, []byte("qjs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ytDlpJSRuntimePath(ytDlpPath); got != qjs {
		t.Fatalf("runtime path = %q, want %q", got, qjs)
	}
	args := ytDlpJSRuntimeArgs(ytDlpPath)
	if len(args) != 2 || args[0] != "--js-runtimes" || args[1] != "quickjs:"+qjs {
		t.Fatalf("runtime args = %q", args)
	}
}

// TestStreamAttemptsPreferDefaultsWithRuntime is the behaviour MED-19 exists
// for: with a runtime the upstream default clients lead, because they serve the
// best formats; without one android is the only client that still hands back a
// muxed file.
func TestStreamAttemptsPreferDefaultsWithRuntime(t *testing.T) {
	with := streamAttempts(720, true)
	if len(with) == 0 {
		t.Fatal("with a runtime, no attempts")
	}
	if with[0].name != "default clients" {
		t.Fatalf("with a runtime, first attempt = %q", with[0].name)
	}
	if len(with) < 2 || with[1].name != "android client" {
		t.Fatalf("with a runtime, second attempt = %q", with[1].name)
	}
	without := streamAttempts(720, false)
	if without[0].name != "android client" {
		t.Fatalf("without a runtime, first attempt = %q", without[0].name)
	}
	if without[1].name != "default clients" {
		t.Fatalf("without a runtime, second attempt = %q", without[1].name)
	}
	for _, attempt := range with {
		if strings.Contains(strings.Join(attempt.args, " "), "android_vr") {
			t.Fatalf("attempt %q still asks for android_vr", attempt.name)
		}
	}
}
