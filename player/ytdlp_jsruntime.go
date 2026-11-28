package main

// ytdlp_jsruntime.go — the JavaScript runtime yt-dlp needs, pinned.
//
// Since late 2025 yt-dlp runs YouTube's JavaScript challenges to get the full
// format list. Deno is the runtime it finds by itself on PATH; QuickJS-NG is
// one small binary, which suits the handheld better, but yt-dlp has to be told
// where it is (--js-runtimes quickjs:PATH, ytdownload.go). The helper installer
// used to leave the user to find one: a card that had yt-dlp but no runtime
// played only the clients that still serve a muxed format, and the full format
// list stayed out of reach.
//
// This file pins QuickJS-NG v0.18.0 for Windows — the asset name, its SHA-256
// and its exact size, all taken from the release's published digest. The
// download goes through the same resumable transfer as the helper tools, is
// checked against the pin before anything is renamed into place, and lands as
// qjs.exe next to JukaHub's own yt-dlp, where ytDlpJSRuntimePath finds it. The
// previous copy is moved aside first (Windows lets a running .exe be renamed,
// not overwritten) and put back if the swap fails.
//
// Only the copy JukaHub installed itself is managed: a yt-dlp the user put
// elsewhere is theirs, just as with the helper updates.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// ytdlpJSRuntimePinnedVersion is the QuickJS-NG release this build ships.
	ytdlpJSRuntimePinnedVersion = "0.18.0"

	// ytdlpJSRuntimeReleaseBase is where every pinned asset lives. The URL is
	// compiled in, so the download cannot be redirected by configuration.
	ytdlpJSRuntimeReleaseBase = "https://github.com/quickjs-ng/quickjs/releases/download/v" + ytdlpJSRuntimePinnedVersion + "/"

	// ytdlpJSRuntimeVersionTTL bounds the version probe shown in Patch; the
	// value only changes when a new runtime is installed.
	ytdlpJSRuntimeVersionTTL = 10 * time.Minute
)

// ytdlpJSRuntimeAsset is one pinned release file.
type ytdlpJSRuntimeAsset struct {
	Name   string
	SHA256 string
	Size   int64
	URL    string
}

// ytdlpJSRuntimePinnedAsset returns the pinned QuickJS-NG asset for a target.
// Windows is the platform this installer covers (the handheld builds can drop
// a qjs beside yt-dlp themselves, and ytDlpJSRuntimeArgs uses it there).
func ytdlpJSRuntimePinnedAsset(goos, goarch string) (ytdlpJSRuntimeAsset, bool) {
	if goos != "windows" {
		return ytdlpJSRuntimeAsset{}, false
	}
	var name, sum string
	var size int64
	switch goarch {
	case "amd64":
		name = "qjs-windows-x86_64.exe"
		sum = "ff03398d74ea6e6bd3a8e58058f7003225abcac229d7d57775b7aaae812a49c7"
		size = 1873152
	case "386":
		name = "qjs-windows-x86.exe"
		sum = "79aa85a8208596dc04333e99726c254d43b9e814803f2100ea593341f2c95b0e"
		size = 1576340
	default:
		return ytdlpJSRuntimeAsset{}, false
	}
	return ytdlpJSRuntimeAsset{
		Name:   name,
		SHA256: sum,
		Size:   size,
		URL:    ytdlpJSRuntimeReleaseBase + name,
	}, true
}

// ytdlpJSRuntimeName is the file name yt-dlp looks for beside the helper.
func ytdlpJSRuntimeName() string {
	if runtime.GOOS == "windows" {
		return "qjs.exe"
	}
	return "qjs"
}

// ytdlpJSRuntimeVersion is the version a QuickJS-NG binary prints, or "".
// The pinned release answers "0.18.0"; older builds spell "QuickJS-NG version
// 0.18.0", and both parse to the same short form.
func ytdlpJSRuntimeVersion(path string) string {
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return parseQuickJSVersion(string(out))
}

// parseQuickJSVersion picks the dotted version out of a --version line. Pure,
// so the probe's output can be tested without running a binary.
func parseQuickJSVersion(out string) string {
	for _, field := range strings.Fields(out) {
		field = strings.TrimPrefix(field, "v")
		parts := strings.Split(field, ".")
		if len(parts) < 2 {
			continue
		}
		ok := true
		for _, part := range parts {
			if part == "" {
				ok = false
				break
			}
			for i := 0; i < len(part); i++ {
				if part[i] < '0' || part[i] > '9' {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
		}
		if ok {
			return field
		}
	}
	return ""
}

// ytdlpJSRuntimeVersionCache keeps Patch's version read off the render thread:
// a probe starts in the background and the next frames read the result.
var ytdlpJSRuntimeVersionCache struct {
	mu      sync.Mutex
	path    string
	value   string
	at      time.Time
	probing bool
}

// ytdlpJSRuntimeInstalledVersion is the cached version of the runtime at path,
// starting a background probe when it is missing or stale. Safe from any
// goroutine; never blocks on the binary.
func ytdlpJSRuntimeInstalledVersion(path string) string {
	c := &ytdlpJSRuntimeVersionCache
	c.mu.Lock()
	if c.path != path {
		c.path, c.value, c.at, c.probing = path, "", time.Time{}, false
	}
	// A negative probe is cached briefly, not for the full TTL: a binary that
	// was locked during a swap should be retried soon, but a broken one must
	// not spawn a process on every Patch frame.
	ttl := ytdlpJSRuntimeVersionTTL
	if c.value == "" {
		ttl = 30 * time.Second
	}
	fresh := !c.at.IsZero() && time.Since(c.at) < ttl
	start := path != "" && !fresh && !c.probing
	if start {
		c.probing = true
	}
	value := c.value
	c.mu.Unlock()
	if start {
		go func() {
			got := ytdlpJSRuntimeVersion(path)
			c.mu.Lock()
			if c.path == path {
				c.value, c.at = got, time.Now()
			}
			c.probing = false
			c.mu.Unlock()
		}()
	}
	return value
}

// ytdlpJSRuntimeVersionForget drops the cached probe after an install, so the
// Patch screen shows the new runtime's version on its next frame.
func ytdlpJSRuntimeVersionForget() {
	c := &ytdlpJSRuntimeVersionCache
	c.mu.Lock()
	c.path, c.value, c.at, c.probing = "", "", time.Time{}, false
	c.mu.Unlock()
}

// ytdlpJSRuntimeNeeded reports whether dir needs the pinned runtime: no qjs at
// all, or one whose size is not the pinned file's (an older copy, or a file
// that did not come from the pin).
func ytdlpJSRuntimeNeeded(dir string) bool {
	asset, ok := ytdlpJSRuntimePinnedAsset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, ytdlpJSRuntimeName()))
	if err != nil || !info.Mode().IsRegular() {
		return true
	}
	return info.Size() != asset.Size
}

// ytdlpInstallJSRuntime downloads and verifies asset into dir, then swaps it
// into place under the name yt-dlp looks for. The pinned SHA-256 and size are
// both checked: a truncated resume, a changed release asset or a proxy's error
// page never replaces a working runtime.
func ytdlpInstallJSRuntime(ctx context.Context, dir string, asset ytdlpJSRuntimeAsset) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("the tools folder is unavailable")
	}
	if asset.Name == "" || asset.SHA256 == "" || asset.Size <= 0 || asset.URL == "" {
		return "", fmt.Errorf("the pinned QuickJS-NG asset is incomplete")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create the tools folder: %w", err)
	}
	staged := filepath.Join(dir, ".qjs-"+asset.Name)
	// A successful swap renames the staged file; on failure this removes it.
	defer os.Remove(staged)
	if err := downloadFileWithProgressCtx(ctx, asset.URL, staged, nil); err != nil {
		return "", fmt.Errorf("download QuickJS-NG %s: %w", ytdlpJSRuntimePinnedVersion, err)
	}
	info, err := os.Stat(staged)
	if err != nil {
		return "", fmt.Errorf("read the downloaded QuickJS-NG: %w", err)
	}
	if info.Size() != asset.Size {
		return "", fmt.Errorf("QuickJS-NG %s is %d bytes, want %d", ytdlpJSRuntimePinnedVersion, info.Size(), asset.Size)
	}
	got, err := hashFile(staged)
	if err != nil {
		return "", fmt.Errorf("hash the downloaded QuickJS-NG: %w", err)
	}
	if !strings.EqualFold(got, asset.SHA256) {
		return "", fmt.Errorf("QuickJS-NG %s SHA-256 is %s, want %s", ytdlpJSRuntimePinnedVersion, got, asset.SHA256)
	}
	target := filepath.Join(dir, ytdlpJSRuntimeName())
	backup := target + ".bak"
	_ = os.Remove(backup)
	hadTarget := false
	if _, err := os.Stat(target); err == nil {
		// Windows refuses to overwrite a binary a running yt-dlp has open, but
		// allows renaming it; the new copy then takes the name.
		if err := os.Rename(target, backup); err != nil {
			return "", fmt.Errorf("move the old QuickJS-NG aside: %w", err)
		}
		hadTarget = true
	}
	if err := os.Rename(staged, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return "", fmt.Errorf("install QuickJS-NG: %w", err)
	}
	_ = os.Remove(backup)
	return target, nil
}

// ytdlpJSRuntimeEnsure installs the pinned runtime into dir when it is missing
// or not the pinned file. It does nothing on a platform it has no pin for.
func ytdlpJSRuntimeEnsure(ctx context.Context, dir string) error {
	asset, ok := ytdlpJSRuntimePinnedAsset(runtime.GOOS, runtime.GOARCH)
	if !ok || !ytdlpJSRuntimeNeeded(dir) {
		return nil
	}
	if !IsOnline() {
		return errToolsOffline
	}
	if _, err := ytdlpInstallJSRuntime(ctx, dir, asset); err != nil {
		return err
	}
	ytdlpJSRuntimeVersionForget()
	return nil
}

// ytdlpJSRuntimeEnsureManaged installs the runtime next to the yt-dlp JukaHub
// manages, when there is one. Best effort: a failure is logged by the caller
// and the helper keeps working without the full format list.
func ytdlpJSRuntimeEnsureManaged(config *Config) error {
	path, managed := ytdlpManagedPath(config)
	if !managed || path == "" {
		return nil
	}
	return ytdlpJSRuntimeEnsure(context.Background(), filepath.Dir(path))
}

// ytdlpJSRuntimeOffer describes the Patch row for the runtime, when Windows
// has a managed yt-dlp whose qjs is missing or not the pinned file.
func ytdlpJSRuntimeOffer(ytDlpPath string) (label, sub string, ok bool) {
	if runtime.GOOS != "windows" || ytDlpPath == "" {
		return "", "", false
	}
	asset, pinned := ytdlpJSRuntimePinnedAsset(runtime.GOOS, runtime.GOARCH)
	if !pinned {
		return "", "", false
	}
	qjs := ytDlpJSRuntimePath(ytDlpPath)
	if qjs == "" {
		return "Install JavaScript runtime",
			"QuickJS-NG " + ytdlpJSRuntimePinnedVersion + " · pinned, SHA-256 checked", true
	}
	if info, err := os.Stat(qjs); err == nil && info.Size() == asset.Size {
		return "", "", false
	}
	return "Update JavaScript runtime",
		"QuickJS-NG " + ytdlpJSRuntimePinnedVersion + " · replaces the old copy", true
}

// patchYtDlpJSRuntime installs the runtime from Patch's row. It runs as a
// normal Patch operation: the standard progress card, then a completion toast.
func patchYtDlpJSRuntime(config *Config) {
	path, managed := ytdlpManagedPath(config)
	if !managed || path == "" {
		ShowToast("Install yt-dlp first — its runtime goes next to it", ToastKindWarning)
		return
	}
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi to install the JavaScript runtime", ToastKindWarning)
		return
	}
	dir := filepath.Dir(path)
	RunPatchOp("Installing QuickJS-NG "+ytdlpJSRuntimePinnedVersion, func(ctx context.Context) error {
		return ytdlpJSRuntimeEnsure(ctx, dir)
	})
}
