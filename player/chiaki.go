package main

// chiaki.go — PS4 Remote Play through the TrimUI Chiaki-ng port.
//
// The upstream chiaki-ng project is a desktop client that requires Vulkan on
// the current release. This integration targets the separate
// nlkcodenew/trimui-chiaki-ng port instead: it ships Python wrappers plus
// native aarch64 helpers and the SDL2/FFmpeg/OpenSSL libraries the Brick Pro
// needs, so it can run from an SD card on TrimUI firmware without a desktop.
//
// The port's published releases are PS4-only: the author shipped one PS5 build
// (v0.4.0-beta2, an in-app PSN Account-ID pairing flow) and then withdrew it,
// deleting the release ZIP and closing PS5 development in docs/PS5_STATUS.md.
// The tag itself still carries the complete, runnable tree, so JukaHub can offer
// that build as an opt-in "PS5 beta" installed beside the PS4 app. It is
// deliberately separate: the stable build and its PS4 pairing are never
// replaced by an unsupported beta.
//
// Both installers keep the package's own settings and pairing data when they
// update, and both verify what they write: the release ZIP against the digest
// GitHub publishes, the beta tree against the per-file SHA-256 values in the
// manifest.json that ships inside its tag.
//
// JukaHub only downloads the official release ZIP from the project's GitHub
// releases, verifies the SHA-256 digest published by GitHub, extracts it in a
// staging folder on the SD card, and then swaps the app folder into place.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	chiakiAppName = "Chiaki-ng"
	// chiakiPS5AppName names the opt-in beta in the install card, so a failed
	// PS5 download retries the PS5 install and not the stable one.
	chiakiPS5AppName    = "Chiaki-ng PS5"
	chiakiPanelTitle    = "PS Remote Play"
	chiakiRepo          = "nlkcodenew/trimui-chiaki-ng"
	chiakiInstallSubdir = "Apps/Chiaki"
	chiakiVersionFile   = ".jukahub-chiaki-version"
	chiakiZipMaxBytes   = 64 << 20
	chiakiZipMaxEntries = 8192
	chiakiZipMaxExpand  = 256 << 20

	// The last build with PS5 pairing. The release was withdrawn (its ZIP and
	// its GitHub release are gone), but the tag still holds the whole app tree
	// plus the manifest that lists every file's SHA-256.
	chiakiPS5Tag        = "v0.4.0-beta2"
	chiakiPS5Subdir     = "Apps/Chiaki-PS5"
	chiakiPS5MarkModule = "rh/ps5_regist.py"
	chiakiPS5MaxArchive = 96 << 20
	chiakiPS5MaxExpand  = 320 << 20
	chiakiPS5MaxEntries = 4096
	chiakiPS5MinFiles   = 8
)

// chiakiSDRoot is overridable in tests. On a device the SD card is mounted at
// /mnt/SDCARD; the executable-relative fallback in chiakiSDRootDir covers
// installs that were moved somewhere else.
var chiakiSDRoot = "/mnt/SDCARD"

// chiakiInstallState prevents two concurrent downloads from writing the same
// app folder. The UI can be tapped repeatedly while the install card is up.
var chiakiInstallState struct {
	mu      sync.Mutex
	running bool
}

func chiakiInstalling() bool {
	chiakiInstallState.mu.Lock()
	defer chiakiInstallState.mu.Unlock()
	return chiakiInstallState.running
}

func setChiakiInstalling(running bool) {
	chiakiInstallState.mu.Lock()
	chiakiInstallState.running = running
	chiakiInstallState.mu.Unlock()
}

// chiakiSDRootDir returns the SD-card root. Explicit configuration wins, then
// the real TrimUI mount, then the Apps/JukaHub layout next to the executable.
// An empty string means the platform cannot host the Linux ARM64 package.
func chiakiSDRootDir() string {
	if root := strings.TrimSpace(os.Getenv("JUKAHUB_SD_ROOT")); root != "" {
		return root
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return ""
	}
	if st, err := os.Stat(chiakiSDRoot); err == nil && st.IsDir() {
		return chiakiSDRoot
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return chiakiSDRoot
	}
	appDir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(appDir), "JukaHub") {
		appsDir := filepath.Dir(appDir)
		if strings.EqualFold(filepath.Base(appsDir), "Apps") {
			return filepath.Dir(appsDir)
		}
	}
	return chiakiSDRoot
}

// chiakiAppDir is the app folder the TrimUI launcher scans.
func chiakiAppDir() string {
	root := chiakiSDRootDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(chiakiInstallSubdir))
}

func chiakiLauncherPath() string {
	app := chiakiAppDir()
	if app == "" {
		return ""
	}
	return filepath.Join(app, "launch.sh")
}

func chiakiInstalled() bool {
	return chiakiInstalledAt(chiakiAppDir())
}

func chiakiInstalledVersion() string {
	return chiakiInstalledVersionAt(chiakiAppDir())
}

func chiakiInstalledVersionAt(app string) string {
	if app == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(app, chiakiVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ── Interface language ──────────────────────────────────────────────────────
//
// The port ships its own English catalogue (rh/i18n.py: VI and EN) and stores
// the choice as "language": "VI"|"EN" in settings.json. JukaHub flips that one
// key, preserving every other value, so the user's menus change language
// without losing settings or console pairing. The strings that never go
// through the catalogue are rewritten separately (see chiaki_translate.go).

// chiakiSettingsPath is where the port keeps its settings.
func chiakiSettingsPath(appDir string) string {
	if appDir == "" {
		return ""
	}
	return filepath.Join(appDir, "settings.json")
}

// chiakiSettingsLanguage returns "EN", "VI", or "" when the file or key is
// missing or the file cannot be parsed.
func chiakiSettingsLanguage(appDir string) string {
	path := chiakiSettingsPath(appDir)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	lang := strings.ToUpper(strings.TrimSpace(cfg.Language))
	if lang != "EN" && lang != "VI" {
		return ""
	}
	return lang
}

// setChiakiSettingsLanguage rewrites only the language key. Unknown keys are
// carried over as raw JSON, so a newer release's settings survive untouched.
// A corrupt file is never overwritten: the caller gets an error instead of the
// app silently losing its pairing data.
func setChiakiSettingsLanguage(appDir, lang string) error {
	lang = strings.ToUpper(strings.TrimSpace(lang))
	if lang != "EN" && lang != "VI" {
		return fmt.Errorf("unsupported language %q", lang)
	}
	path := chiakiSettingsPath(appDir)
	if path == "" {
		return errors.New("the Chiaki-ng install folder is unknown")
	}
	values := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &values); err != nil {
				return errors.New("settings.json is not valid JSON; fix or delete it first")
			}
		}
	case os.IsNotExist(err):
		// The port creates its defaults on first run; a file with only the
		// language key is enough to make that first run English.
	default:
		return err
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	quoted, err := json.Marshal(lang)
	if err != nil {
		return err
	}
	values["language"] = quoted
	out, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := path + ".jukahub-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// chiakiCurrentLanguage is the language the next launch will use. The port
// defaults to VI when settings.json has no language yet.
func chiakiCurrentLanguage(appDir string) string {
	if lang := chiakiSettingsLanguage(appDir); lang != "" {
		return lang
	}
	return "VI"
}

// chiakiToggleLanguage switches between the port's two catalogues and returns
// the new code. The hardcoded literals are switched in the same step, so the
// menus do not come back half-translated after the row is pressed.
func chiakiToggleLanguage(appDir string) (string, error) {
	next := "EN"
	if chiakiCurrentLanguage(appDir) == "EN" {
		next = "VI"
	}
	if err := setChiakiSettingsLanguage(appDir, next); err != nil {
		return "", err
	}
	if _, err := chiakiRepairCatalogue(appDir); err != nil {
		return next, fmt.Errorf("repair the menu catalogue: %w", err)
	}
	if _, err := chiakiSetLiteralLanguage(appDir, next); err != nil {
		return next, fmt.Errorf("switch the menu text: %w", err)
	}
	return next, nil
}

// chiakiLanguageName is the label shown in the panel and toast.
func chiakiLanguageName(lang string) string {
	if lang == "EN" {
		return "English"
	}
	return "Vietnamese"
}

// chiakiEnsureEnglish runs before a launch: it creates settings.json with
// English when the app has none, then points the port's hardcoded literals at
// the language settings.json selects. An existing file is never touched: a user
// who picked Vietnamese in the app keeps it, and the panel row switches it
// either way.
func chiakiEnsureEnglish(appDir string) {
	if appDir == "" {
		return
	}
	if !regularFile(chiakiSettingsPath(appDir)) {
		if err := setChiakiSettingsLanguage(appDir, "EN"); err != nil {
			log.Printf("[chiaki] could not default the UI to English: %v", err)
		}
	}
	chiakiPrepareUILanguage(appDir)
}

// chiakiInstallLanguageDefault points a fresh install at English. Every release
// ZIP ships its own settings.json with "language": "VI", so the file exists on
// the very first run and chiakiEnsureEnglish must leave it alone at launch —
// the installer is the only place that can tell a package default from a
// language the user picked. hadSettings records whether the app was already
// installed before this download (its settings are preserved across updates).
func chiakiInstallLanguageDefault(appDir string, hadSettings bool) {
	if appDir == "" {
		return
	}
	if !hadSettings {
		if err := setChiakiSettingsLanguage(appDir, "EN"); err != nil {
			log.Printf("[chiaki] could not default the UI to English: %v", err)
		}
	}
	chiakiEnsureEnglish(appDir)
}

func chiakiPackageSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64"
}

// chiakiRequirement summarizes the files the launcher needs. The release
// bundles the Brick Pro runtime under libs/brick-stock; Smart Pro S uses the
// firmware's SDL2 and FFmpeg. Missing system libraries are reported instead of
// being hidden, because JukaHub cannot safely replace a firmware package.
type chiakiRequirements struct {
	Python string
	SDL2   string
	Libs   int
	OK     bool
	Detail string
}

func chiakiCheckRequirements(appDir string) chiakiRequirements {
	var req chiakiRequirements
	req.Python = findPython3(appDir)
	for _, cand := range []string{
		"/usr/trimui/lib/libSDL2-2.0.so.0",
		"/usr/lib64/libSDL2-2.0.so.0",
		"/usr/lib/libSDL2-2.0.so.0",
		"/lib/libSDL2-2.0.so.0",
		filepath.Join(appDir, "libs", "brick-stock", "libSDL2-2.0.so.0"),
	} {
		if regularFile(cand) {
			req.SDL2 = cand
			break
		}
	}
	if entries, err := os.ReadDir(filepath.Join(appDir, "libs", "brick-stock")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".so") || strings.Contains(name, ".so.") {
				req.Libs++
			}
		}
	}
	// Newer releases also ship their Python SDL2 binding in vendor/sdl2 and may
	// take the native library from the firmware. Treat all three layouts as a
	// valid runtime instead of rejecting a package the stock menu starts fine.
	vendor := regularFile(filepath.Join(appDir, "vendor", "sdl2", "__init__.py"))
	if req.Python == "" {
		req.Detail = "Python 3.8+ not found — update the firmware (1.0.4+)"
		return req
	}
	var missing []string
	for _, rel := range []string{"bin/chiaki-stream", "bin/chiaki-regist"} {
		if !regularFile(filepath.Join(appDir, filepath.FromSlash(rel))) {
			missing = append(missing, filepath.Base(rel))
		}
	}
	if req.SDL2 == "" && req.Libs == 0 && !vendor {
		missing = append(missing, "SDL2")
	}
	if len(missing) > 0 {
		// These are needed to stream, not to open the app. Report them without
		// pretending the install is unusable: the stock menu runs it anyway.
		req.Detail = "Missing " + strings.Join(missing, ", ") + " — the menu may still start"
		return req
	}
	req.OK = true
	SDL := "firmware"
	if req.SDL2 != "" {
		SDL = filepath.Base(req.SDL2)
	} else if vendor {
		SDL = "vendored"
	}
	req.Detail = fmt.Sprintf("Python %s · SDL2 %s · %d bundled libs", filepath.Base(req.Python), SDL, req.Libs)
	return req
}

// findPython3 follows the same discovery as the port's own launch.sh: PATH,
// the app's own python folder, the SD card System/bin folder and PortMaster's
// bundled interpreter. A missing interpreter is the one startup failure that
// cannot be worked around, so the search is deliberately broad.
func findPython3(appDir string) string {
	for _, name := range []string{"python3", "python3.13", "python3.12", "python3.11", "python3.10", "python3.9", "python3.8"} {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return p
		}
	}
	candidates := []string{
		"/usr/bin/python3",
		"/usr/local/bin/python3",
		"/usr/trimui/bin/python3",
	}
	if appDir != "" {
		candidates = append(candidates, filepath.Join(appDir, "python", "bin", "python3"))
	}
	if root := chiakiSDRootDir(); root != "" {
		candidates = append(candidates,
			filepath.Join(root, "System", "bin", "python3"),
			filepath.Join(root, "Apps", "Python", "bin", "python3"),
			filepath.Join(root, "Apps", "python", "bin", "python3"),
			filepath.Join(root, "Apps", "PortMaster", "PortMaster", "exlibs", "python3"),
		)
	}
	for _, cand := range candidates {
		if regularFile(cand) {
			return cand
		}
	}
	return ""
}

// ── Release metadata ─────────────────────────────────────────────────────

type chiakiAsset struct {
	Name   string
	URL    string
	Size   int64
	Digest string
}

type chiakiRelease struct {
	Tag     string
	ZipURL  string
	ZipName string
	Size    int64
	SHA256  string
}

// selectChiakiRelease picks the install ZIP from a GitHub release. Source
// archives are ignored: they do not contain the packaged native helpers or the
// bundled runtime libraries.
func selectChiakiRelease(tag string, assets []chiakiAsset) (*chiakiRelease, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, errors.New("release has no tag")
	}
	var chosen *chiakiAsset
	for i := range assets {
		a := &assets[i]
		lower := strings.ToLower(a.Name)
		if !strings.HasSuffix(lower, ".zip") {
			continue
		}
		if strings.HasPrefix(lower, "trimui-chiaki-ng-v") || lower == "trimui-chiaki-ng.zip" {
			chosen = a
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("release %s has no trimui-chiaki-ng ZIP asset", tag)
	}
	sha := strings.TrimSpace(chosen.Digest)
	sha = strings.TrimPrefix(strings.ToLower(sha), "sha256:")
	if sha != "" && (len(sha) != 64 || !isHexString(sha)) {
		sha = ""
	}
	return &chiakiRelease{
		Tag:     tag,
		ZipURL:  chosen.URL,
		ZipName: chosen.Name,
		Size:    chosen.Size,
		SHA256:  sha,
	}, nil
}

func isHexString(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func fetchChiakiRelease(ctx context.Context) (*chiakiRelease, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", chiakiRepo)
	client := newPatchClient()
	data, err := fetchBounded(ctx, client, apiURL, patchMaxMetadata)
	if err != nil {
		return nil, err
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, err
	}
	assets := make([]chiakiAsset, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		assets = append(assets, chiakiAsset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size, Digest: a.Digest})
	}
	release, err := selectChiakiRelease(rel.TagName, assets)
	if err != nil {
		return nil, err
	}
	if release.SHA256 == "" {
		// Older GitHub API responses do not expose asset digests. The project
		// publishes a .sha256 sidecar next to every ZIP; read that instead of
		// installing an unverified archive.
		sha, err := fetchChiakiSidecarSHA(ctx, client, release.ZipURL+".sha256")
		if err != nil {
			return nil, err
		}
		release.SHA256 = sha
	}
	if release.SHA256 == "" {
		return nil, errors.New("release ZIP has no SHA-256 digest")
	}
	if err := validatePatchURL(release.ZipURL); err != nil {
		return nil, err
	}
	return release, nil
}

func fetchChiakiSidecarSHA(ctx context.Context, client *http.Client, url string) (string, error) {
	data, err := fetchBounded(ctx, client, url, 4096)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errors.New("empty SHA-256 sidecar")
	}
	sha := strings.ToLower(strings.TrimSpace(fields[0]))
	if len(sha) != 64 || !isHexString(sha) {
		return "", errors.New("invalid SHA-256 sidecar")
	}
	return sha, nil
}

// ── Install / update ─────────────────────────────────────────────────────

// findChiakiPackageRoot tolerates the App/ versus Apps/ naming difference
// between the project's release builder and its install guide.
func findChiakiPackageRoot(unpacked string) (string, error) {
	candidates := []string{
		filepath.Join(unpacked, "Apps", "Chiaki"),
		filepath.Join(unpacked, "App", "Chiaki"),
		filepath.Join(unpacked, "Chiaki"),
		unpacked,
	}
	for _, dir := range candidates {
		if regularFile(filepath.Join(dir, "launch.sh")) && regularFile(filepath.Join(dir, "app.py")) {
			return dir, nil
		}
	}
	return "", errors.New("archive does not contain Apps/Chiaki/launch.sh and app.py")
}

// installChiakiPackage extracts the official ZIP into a staging directory and
// installs it. The staging directory lives on the same filesystem as the app so
// the final rename does not copy across filesystems on the SD card.
func installChiakiPackage(ctx context.Context, sdRoot, archive string, release *chiakiRelease) error {
	if strings.TrimSpace(sdRoot) == "" {
		return errors.New("SD card path is unknown")
	}
	if err := os.MkdirAll(sdRoot, 0o755); err != nil {
		return fmt.Errorf("SD card is not writable: %w", err)
	}
	stage, err := os.MkdirTemp(sdRoot, ".chiaki-install-*")
	if err != nil {
		return fmt.Errorf("cannot create staging folder: %w", err)
	}
	defer os.RemoveAll(stage)

	installBarPhase(installPhaseExtract)
	unpacked := filepath.Join(stage, "unpack")
	if err := extractZipSafe(ctx, archive, unpacked, chiakiZipMaxEntries, chiakiZipMaxExpand); err != nil {
		return fmt.Errorf("unpack Chiaki-ng: %w", err)
	}
	root, err := findChiakiPackageRoot(unpacked)
	if err != nil {
		return err
	}
	return installChiakiTree(root, filepath.Join(sdRoot, filepath.FromSlash(chiakiInstallSubdir)), release)
}

// chiakiPreservedNames are written by the app while it is in use. An update
// must never replace them: they hold the user's settings and, most
// importantly, the console pairing keys.
var chiakiPreservedNames = map[string]bool{
	"settings.json":     true,
	"paired_hosts.json": true,
	"chiaki.conf":       true,
	"Chiaki-debug.log":  true,
	"Chiaki-loi.txt":    true,
}

// installChiakiTree installs a staged package tree. Existing user data is
// copied into the new tree before the swap, so a failed update can fall back to
// the previous install.
func installChiakiTree(root, target string, release *chiakiRelease) error {
	if !regularFile(filepath.Join(root, "launch.sh")) || !regularFile(filepath.Join(root, "app.py")) {
		return errors.New("staged Chiaki-ng tree is incomplete")
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create Apps folder: %w", err)
	}

	next, err := os.MkdirTemp(parent, ".chiaki-next-*")
	if err != nil {
		return fmt.Errorf("create staging folder: %w", err)
	}
	defer os.RemoveAll(next)
	if err := copyChiakiTree(root, next); err != nil {
		return err
	}

	// Carry over settings and pairing data from an existing install.
	if oldInfo, err := os.Stat(target); err == nil && oldInfo.IsDir() {
		for name := range chiakiPreservedNames {
			src := filepath.Join(target, name)
			if regularFile(src) {
				if err := copyChiakiFile(src, filepath.Join(next, name)); err != nil {
					return fmt.Errorf("preserve %s: %w", name, err)
				}
			}
		}
	}

	if release != nil && strings.TrimSpace(release.Tag) != "" {
		version := strings.TrimSpace(release.Tag) + "\n"
		if err := os.WriteFile(filepath.Join(next, chiakiVersionFile), []byte(version), 0o644); err != nil {
			return fmt.Errorf("write version file: %w", err)
		}
	}

	// Swap: move the old tree aside only after the complete new tree exists.
	backup := target + ".old"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("move old Chiaki-ng aside: %w", err)
		}
	}
	if err := os.Rename(next, target); err != nil {
		// Put the old install back so a failed update is not destructive.
		_ = os.Rename(backup, target)
		return fmt.Errorf("activate Chiaki-ng: %w", err)
	}
	_ = os.RemoveAll(backup)
	return nil
}

func copyChiakiTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %s", path)
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing special file %s", path)
		}
		return copyChiakiFile(path, out)
	})
}

func copyChiakiFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// startChiakiInstall downloads and installs the latest stable release. It is
// deliberately a background job: the download is ~13 MB and the panel must
// stay responsive.
func startChiakiInstall() error {
	if !chiakiPackageSupported() {
		return errors.New("Chiaki-ng runs on the TrimUI Linux build, not this platform")
	}
	if chiakiInstalling() {
		return errors.New("the Chiaki-ng installer is already running")
	}
	if !IsOnline() {
		return errors.New("no internet — connect WiFi, then try again")
	}
	root := chiakiSDRootDir()
	if root == "" {
		return errors.New("SD card path is unknown")
	}
	setChiakiInstalling(true)
	installBarBegin(chiakiAppName)
	// Read before the download: the shipped ZIP carries a Vietnamese
	// settings.json, so presence afterwards cannot distinguish a fresh install
	// from one whose language the user already chose.
	hadSettings := regularFile(chiakiSettingsPath(filepath.Join(root, filepath.FromSlash(chiakiInstallSubdir))))
	go func() {
		defer setChiakiInstalling(false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		release, err := fetchChiakiRelease(ctx)
		if err != nil {
			log.Printf("[chiaki] release lookup failed: %v", err)
			installBarFailRetry(chiakiAppName, firstLine(err.Error()))
			ShowToast("Could not check Chiaki-ng — "+firstLine(err.Error()), ToastKindError)
			return
		}
		stage, err := os.MkdirTemp(root, ".chiaki-download-*")
		if err != nil {
			installBarFailRetry(chiakiAppName, firstLine(err.Error()))
			return
		}
		defer os.RemoveAll(stage)
		archive := filepath.Join(stage, "chiaki-ng.zip")
		installBarPhase(installPhaseDownload)
		client := newPatchClient()
		if err := downloadToFile(ctx, client, release.ZipURL, archive, release.SHA256, chiakiZipMaxBytes); err != nil {
			log.Printf("[chiaki] download failed: %v", err)
			installBarFailRetry(chiakiAppName, firstLine(err.Error()))
			ShowToast("Chiaki-ng download failed — "+firstLine(err.Error()), ToastKindError)
			return
		}
		if err := installChiakiPackage(ctx, root, archive, release); err != nil {
			log.Printf("[chiaki] install failed: %v", err)
			installBarFailRetry(chiakiAppName, firstLine(err.Error()))
			ShowToast("Could not install Chiaki-ng — "+firstLine(err.Error()), ToastKindError)
			return
		}
		chiakiInstallLanguageDefault(filepath.Join(root, filepath.FromSlash(chiakiInstallSubdir)), hadSettings)
		installBarDone()
		log.Printf("[chiaki] installed %s into %s", release.Tag, filepath.Join(root, filepath.FromSlash(chiakiInstallSubdir)))
		ShowToast(chiakiAppName+" "+release.Tag+" installed", ToastKindSuccess)
	}()
	return nil
}

// retryChiakiInstall starts a fresh download after a failed one. The install
// card's A button calls this before the generic helper-tool retry path.
func retryChiakiInstall() {
	if chiakiInstalling() {
		return
	}
	if err := startChiakiInstall(); err != nil {
		ShowToast(err.Error(), ToastKindWarning)
	}
}

// retryChiakiPS5Install is the same retry for the beta card.
func retryChiakiPS5Install() {
	if chiakiInstalling() {
		return
	}
	if err := startChiakiPS5Install(); err != nil {
		ShowToast(err.Error(), ToastKindWarning)
	}
}

// ── PS5 beta build ────────────────────────────────────────────────────────
//
// The author's only PS5 build was published as v0.4.0-beta2 and then pulled:
// the release and its ZIP now return 404 and docs/PS5_STATUS.md says PS5 work
// stopped. The git tag is intact and carries the complete application plus a
// manifest listing a SHA-256 for every file, which is what this installer
// uses. It is installed into its own folder and never replaces the stable PS4
// app: the beta is unsupported, and a user who tries it must be able to go
// back to a working PS4 setup by simply launching the other app.

func chiakiPS5AppDir() string {
	root := chiakiSDRootDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(chiakiPS5Subdir))
}

func chiakiPS5Installed() bool {
	app := chiakiPS5AppDir()
	if app == "" {
		return false
	}
	return regularFile(filepath.Join(app, "launch.sh")) && regularFile(filepath.Join(app, "app.py"))
}

func chiakiPS5InstalledVersion() string {
	return chiakiInstalledVersionAt(chiakiPS5AppDir())
}

// chiakiTagArchiveURL is GitHub's source tarball for a tag. codeload is on the
// download allowlist, so this goes through the same URL validation as every
// other download in the app.
func chiakiTagArchiveURL(tag string) string {
	return fmt.Sprintf("https://codeload.github.com/%s/tar.gz/refs/tags/%s", chiakiRepo, url.PathEscape(tag))
}

// chiakiManifest is the file list the port publishes beside every release. The
// PS5 tag's manifest is the only integrity data left for that build, so it is
// what the installer verifies against.
type chiakiManifest struct {
	Version string `json:"version"`
	Note    struct {
		EN string `json:"en"`
		VI string `json:"vi"`
	} `json:"note"`
	Files []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"files"`
}

func (m *chiakiManifest) supportsPS5() bool {
	if m == nil {
		return false
	}
	for _, f := range m.Files {
		if filepath.ToSlash(f.Path) == chiakiPS5MarkModule {
			return true
		}
	}
	return false
}

// parseChiakiManifest validates the file list before anything is written. Every
// path has to be relative, every digest has to be a real SHA-256 and the total
// has to fit, so a damaged or hostile manifest fails before it can steer the
// install.
func parseChiakiManifest(data []byte) (*chiakiManifest, error) {
	var m chiakiManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, errors.New("manifest has no version")
	}
	if len(m.Files) < chiakiPS5MinFiles || len(m.Files) > chiakiPS5MaxEntries {
		return nil, fmt.Errorf("manifest lists %d files, outside the supported range", len(m.Files))
	}
	var total int64
	for _, f := range m.Files {
		if err := sanitizeArchiveEntry(filepath.FromSlash(f.Path), 0o644, false); err != nil {
			return nil, fmt.Errorf("manifest path %q: %w", f.Path, err)
		}
		sha := strings.ToLower(strings.TrimSpace(f.SHA256))
		if len(sha) != 64 || !isHexString(sha) {
			return nil, fmt.Errorf("manifest entry %q has no valid SHA-256", f.Path)
		}
		if f.Size <= 0 {
			return nil, fmt.Errorf("manifest entry %q has no size", f.Path)
		}
		total += f.Size
		if total > chiakiPS5MaxExpand {
			return nil, errors.New("manifest tree is larger than the install limit")
		}
	}
	return &m, nil
}

// findChiakiTagFilesRoot locates the app tree inside an extracted GitHub tag
// tarball: <repo>-<tag>/manifest.json beside <repo>-<tag>/files/.
func findChiakiTagFilesRoot(unpacked string) (filesRoot string, manifest chiakiManifest, err error) {
	entries, err := os.ReadDir(unpacked)
	if err != nil {
		return "", manifest, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		top := filepath.Join(unpacked, entry.Name())
		data, readErr := os.ReadFile(filepath.Join(top, "manifest.json"))
		if readErr != nil {
			continue
		}
		parsed, parseErr := parseChiakiManifest(data)
		if parseErr != nil {
			return "", manifest, parseErr
		}
		files := filepath.Join(top, "files")
		if !regularFile(filepath.Join(files, "app.py")) || !regularFile(filepath.Join(files, "launch.sh")) {
			continue
		}
		return files, *parsed, nil
	}
	return "", manifest, errors.New("archive does not contain a manifest.json beside files/app.py")
}

// verifyChiakiTree hashes every file the manifest lists. It catches a truncated
// download and a half-written SD card, which are the realistic failure modes on
// a handheld; it is not a defence against a compromised upstream, because the
// manifest travels in the same archive.
func verifyChiakiTree(root string, m *chiakiManifest) error {
	if m == nil {
		return errors.New("missing manifest")
	}
	for _, f := range m.Files {
		path := filepath.Join(root, filepath.FromSlash(f.Path))
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("missing file %s", f.Path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular file %s", f.Path)
		}
		if info.Size() != f.Size {
			return fmt.Errorf("%s is %d bytes, manifest says %d", f.Path, info.Size(), f.Size)
		}
		sum, err := sha256File(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", f.Path, err)
		}
		if !strings.EqualFold(sum, f.SHA256) {
			return fmt.Errorf("%s failed its SHA-256 check", f.Path)
		}
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// installChiakiFromTag downloads a tag's source tree, verifies it against the
// manifest it carries and installs the app folder into target.
func installChiakiFromTag(ctx context.Context, sdRoot, target, tag string) error {
	if strings.TrimSpace(sdRoot) == "" {
		return errors.New("SD card path is unknown")
	}
	stage, err := os.MkdirTemp(sdRoot, ".chiaki-tag-*")
	if err != nil {
		return fmt.Errorf("cannot create staging folder: %w", err)
	}
	defer os.RemoveAll(stage)

	installBarPhase(installPhaseDownload)
	archive := filepath.Join(stage, "source.tar.gz")
	if err := downloadToFile(ctx, newPatchClient(), chiakiTagArchiveURL(tag), archive, "", chiakiPS5MaxArchive); err != nil {
		return err
	}

	installBarPhase(installPhaseExtract)
	unpacked := filepath.Join(stage, "unpack")
	if err := extractTarGzSafe(ctx, archive, unpacked, chiakiPS5MaxEntries, chiakiPS5MaxExpand); err != nil {
		return fmt.Errorf("unpack %s: %w", tag, err)
	}
	return installChiakiFromTree(unpacked, target, tag)
}

// installChiakiFromTree is the local half of the tag install: find the app
// tree, check it is the build the caller asked for, verify every file, install.
// It is separate from the download so the verification path can be tested
// without a network.
func installChiakiFromTree(unpacked, target, tag string) error {
	filesRoot, manifest, err := findChiakiTagFilesRoot(unpacked)
	if err != nil {
		return err
	}
	if !manifest.supportsPS5() {
		return fmt.Errorf("%s does not contain the PS5 pairing module", tag)
	}
	if !strings.EqualFold(strings.TrimSpace(manifest.Version), strings.TrimPrefix(tag, "v")) {
		return fmt.Errorf("manifest version %q does not match %s", manifest.Version, tag)
	}
	if err := verifyChiakiTree(filesRoot, &manifest); err != nil {
		return err
	}
	return installChiakiTree(filesRoot, target, &chiakiRelease{Tag: tag})
}

// startChiakiPS5Install installs the opt-in PS5 beta beside the PS4 app.
func startChiakiPS5Install() error {
	if !chiakiPackageSupported() {
		return errors.New("the Chiaki-ng PS5 beta is a TrimUI ARM64 package")
	}
	if chiakiInstalling() {
		return errors.New("the Chiaki-ng installer is already running")
	}
	if !IsOnline() {
		return errors.New("no internet — connect WiFi, then try again")
	}
	root := chiakiSDRootDir()
	if root == "" {
		return errors.New("SD card path is unknown")
	}
	setChiakiInstalling(true)
	installBarBegin(chiakiPS5AppName)
	go func() {
		defer setChiakiInstalling(false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		target := filepath.Join(root, filepath.FromSlash(chiakiPS5Subdir))
		hadSettings := regularFile(chiakiSettingsPath(target))
		if err := installChiakiFromTag(ctx, root, target, chiakiPS5Tag); err != nil {
			log.Printf("[chiaki] PS5 beta install failed: %v", err)
			installBarFailRetry(chiakiPS5AppName, firstLine(err.Error()))
			ShowToast("PS5 beta install failed — "+firstLine(err.Error()), ToastKindError)
			return
		}
		chiakiInstallLanguageDefault(target, hadSettings)
		installBarDone()
		log.Printf("[chiaki] installed PS5 beta %s into %s", chiakiPS5Tag, target)
		ShowToast("PS5 beta installed — pair from Chiaki-ng", ToastKindSuccess)
		// A press on the home launcher asked for "download, then play".
		chiakiPS5InstallSucceeded()
	}()
	return nil
}

// ── Launch ───────────────────────────────────────────────────────────────

// chiakiLaunchEnv builds the environment for the port's launcher. The port's
// own launch.sh also exports these values; setting them here makes a manual
// launch from JukaHub behave the same even if the package's launcher is
// replaced by a future release with a different layout.
func chiakiLaunchEnv(appDir string, base []string) []string {
	env := append([]string{}, base...)
	libs := []string{
		filepath.Join(appDir, "libs"),
		appDir,
		"/usr/trimui/lib",
		"/usr/lib64",
		"/usr/lib",
		"/lib",
	}
	// The Brick Pro Stock OS is the one target whose native helpers need the
	// bundled FFmpeg/OpenSSL/SDL2 runtime. Smart Pro S uses the firmware's
	// newer libraries, and forcing the Brick copies there would be an ABI
	// mismatch. The port's own chiaki.py makes the same distinction using the
	// sun50iw10 device-tree model.
	if chiakiUsesBrickRuntime() {
		libs = append([]string{filepath.Join(appDir, "libs", "brick-stock")}, libs...)
	}
	if existing := strings.TrimSpace(os.Getenv("LD_LIBRARY_PATH")); existing != "" {
		libs = append(libs, existing)
	}
	sdRoot := filepath.Dir(filepath.Dir(appDir))
	env = append(env,
		"HOME="+appDir,
		"SDCARD_PATH="+sdRoot,
		"LD_LIBRARY_PATH="+strings.Join(libs, ":"),
		"PATH="+filepath.Join(appDir, "bin")+":"+filepath.Join(sdRoot, "System", "bin")+":"+os.Getenv("PATH"),
		"CLR_OPENSSL_VERSION_OVERRIDE=1.1",
		"PYTHONUNBUFFERED=1",
	)
	return env
}

// chiakiUsesBrickRuntime reports whether the port should use its bundled
// brick-stock runtime. The upstream port keys this off the device-tree model,
// not the marketing name, so do the same here.
func chiakiUsesBrickRuntime() bool {
	model := strings.ToLower(readDeviceModelFile("/proc/device-tree/model"))
	if strings.Contains(model, "sun50iw10") {
		return true
	}
	return DetectDevice() == DeviceTrimuiBrickPro
}

// launchChiaki runs the port's launcher synchronously with JukaHub's window
// hidden. The child owns the display while it runs; the launcher is responsible
// for returning to the stock UI. On TrimUI the app is launched from the SD card
// exactly as the firmware would.
func launchChiaki() {
	if !chiakiInstalled() {
		ShowToast("Install Chiaki-ng first", ToastKindWarning)
		return
	}
	launchChiakiAt(chiakiAppDir(), chiakiAppName)
}

// launchChiakiPS5 runs the opt-in PS5 beta build from its own folder, so a
// failed PS5 experiment cannot disturb the stable PS4 install beside it.
func launchChiakiPS5() {
	if !chiakiPS5Installed() {
		ShowToast("Install the PS5 beta build first", ToastKindWarning)
		return
	}
	launchChiakiAt(chiakiPS5AppDir(), "Chiaki-ng PS5 beta")
}

// launchChiakiAt hands the screen to the port instead of running it inside
// JukaHub. This firmware gives one app the display at a time: JukaHub's own
// launcher kills the stock UI before JukaHub starts, and a second SDL window
// cannot come up behind a running JukaHub. The old version hid JukaHub's window
// and blocked on the port's launcher, which is why "Launch Remote Play" looked
// like it did nothing.
//
// JukaHub now arms a detached handoff script and exits: the script waits for
// this process to release the display, runs the port, then gives the screen
// back to the stock launcher. The failure mode is safe - when the port cannot
// start, the log says why and the stock menu still comes back.
func launchChiakiAt(appDir, label string) {
	launcher := filepath.Join(appDir, "launch.sh")
	if !chiakiPackageSupported() {
		ShowToast("Chiaki-ng runs on the TrimUI Linux build", ToastKindWarning)
		return
	}
	if !regularFile(launcher) {
		ShowToast("Chiaki-ng is not installed", ToastKindWarning)
		return
	}

	if err := startChiakiHandoff(appDir, label); err != nil {
		log.Printf("[chiaki] handoff failed: %v", err)
		ShowToast("Could not start "+label+" — "+firstLine(err.Error()), ToastKindError)
		return
	}
	log.Printf("[chiaki] handing the display to %s (%s)", label, launcher)
	ShowToast(label+" is starting — JukaHub closes while it runs", ToastKindInfo)
	// Quit so SDL and the framebuffer are released before the port starts.
	requestAppExit()
}

// chiakiHandoffPath is where the generated handoff script lives. It is on
// tmpfs, so it never survives a reboot and cannot accumulate on the SD card.
func chiakiHandoffPath() string {
	return filepath.Join(os.TempDir(), "jukahub-chiaki-handoff.sh")
}

// chiakiHandoffEnv picks only the variables the port's launcher needs. The
// whole process environment is never written to disk: it can hold tokens.
func chiakiHandoffEnv(appDir string) []string {
	allowed := map[string]bool{
		"HOME": true, "SDCARD_PATH": true, "LD_LIBRARY_PATH": true,
		"PATH": true, "CLR_OPENSSL_VERSION_OVERRIDE": true, "PYTHONUNBUFFERED": true,
	}
	var out []string
	for _, kv := range chiakiLaunchEnv(appDir, nil) {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if allowed[name] {
			out = append(out, kv)
		}
	}
	return out
}

// chiakiShellQuote wraps a value so the generated script treats it literally.
func chiakiShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// chiakiHandoffScript builds the launcher that outlives JukaHub. The firmware
// gives one app the screen, so the script waits for JukaHub to exit, lets its
// launcher restore the stock UI, then takes the display from it the same way
// that launcher takes it from MainUI: pause the supervisors, stop the UI, and
// run the port. When the stream ends it hands the screen back.
func chiakiHandoffScript(pid int, appDir, label string) string {
	logPath := filepath.Join(appDir, "Chiaki-jukahub.log")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by JukaHub — display handoff to Chiaki-ng. Do not edit.\n")
	fmt.Fprintf(&b, "JUKAHUB_PID=%d\n", pid)
	fmt.Fprintf(&b, "APP=%s\n", chiakiShellQuote(appDir))
	fmt.Fprintf(&b, "LOG=%s\n", chiakiShellQuote(logPath))
	fmt.Fprintf(&b, "LABEL=%s\n", chiakiShellQuote(label))
	for _, kv := range chiakiHandoffEnv(appDir) {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", kv[:i], chiakiShellQuote(kv[i+1:]))
	}
	b.WriteString(`
# Wait for JukaHub to release SDL and the framebuffer (60 s cap).
i=0
while kill -0 "$JUKAHUB_PID" 2>/dev/null; do
    i=$((i+1))
    [ "$i" -ge 600 ] && break
    sleep 0.1
done

# JukaHub's own launcher restores the stock UI when JukaHub exits. Let it
# finish, then take the screen from it exactly like that launcher took it from
# MainUI: pause the firmware supervisors and stop the UI.
sleep 3
if command -v pkill >/dev/null 2>&1; then
    pkill -STOP runtrimui.sh 2>/dev/null
    pkill -STOP trimui_launcher 2>/dev/null
fi
UI_PID="$(pidof MainUI 2>/dev/null)"
if [ -n "$UI_PID" ]; then
    kill "$UI_PID" 2>/dev/null
    sleep 1
    UI_PID="$(pidof MainUI 2>/dev/null)"
    [ -n "$UI_PID" ] && kill -9 "$UI_PID" 2>/dev/null
fi
# Give the display a moment to settle before SDL takes it, as JukaHub's own
# launcher does.
sleep 1

{
    echo ""
    echo "=== JukaHub handoff $(date 2>/dev/null) ==="
} >> "$LOG" 2>/dev/null
cd "$APP" || exit 1
sh "$APP/launch.sh" >> "$LOG" 2>&1
RC=$?
echo "=== $LABEL exited with code $RC ===" >> "$LOG" 2>/dev/null

# Hand the screen back to the stock UI.
if command -v pkill >/dev/null 2>&1; then
    pkill -CONT runtrimui.sh 2>/dev/null
    pkill -CONT trimui_launcher 2>/dev/null
fi
sleep 2

# If nothing brought the stock menu back, start it here so the user is never
# left on a black screen.
if [ -z "$(pidof MainUI 2>/dev/null)" ]; then
    for UI in /usr/trimui/bin/MainUI /usr/bin/MainUI /usr/local/bin/MainUI \
        /mnt/SDCARD/System/usr/trimui/bin/MainUI
    do
        if [ -x "$UI" ]; then
            (nohup "$UI" >/dev/null 2>&1 &)
            break
        fi
    done
fi

rm -f "$0" 2>/dev/null
exit 0
`)
	return b.String()
}

// startChiakiHandoff writes and starts the detached handoff script.
func startChiakiHandoff(appDir, label string) error {
	if strings.TrimSpace(appDir) == "" {
		return errors.New("the Chiaki-ng install folder is unknown")
	}
	if !chiakiInstalledAt(appDir) {
		return errors.New("Chiaki-ng is not installed")
	}
	chiakiEnsureEnglish(appDir)
	path := chiakiHandoffPath()
	script := chiakiHandoffScript(os.Getpid(), appDir, label)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return fmt.Errorf("write handoff script: %w", err)
	}
	cmd := exec.Command("sh", path)
	cmd.Dir = appDir
	configureChiakiDetached(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("start handoff: %w", err)
	}
	// The script is detached: it keeps running after JukaHub exits. Nothing
	// waits on it, but reaping the direct child keeps the process table clean
	// if it finishes before JukaHub does.
	go func() { _ = cmd.Wait() }()
	return nil
}

// chiakiInstalledAt reports whether appDir holds a runnable port install.
func chiakiInstalledAt(appDir string) bool {
	if appDir == "" {
		return false
	}
	return regularFile(filepath.Join(appDir, "launch.sh")) && regularFile(filepath.Join(appDir, "app.py"))
}

// ── Panel ────────────────────────────────────────────────────────────────

type chiakiAction int

const (
	// chiakiActionNone is first on purpose: it used to be the zero value of
	// chiakiActionInstall, so pressing A on an informational row started an
	// install.
	chiakiActionNone chiakiAction = iota
	chiakiActionInstall
	chiakiActionLaunch
	chiakiActionFolder
	chiakiActionClose
	chiakiActionInstallPS5
	chiakiActionLaunchPS5
	chiakiActionLanguage
	chiakiActionLanguagePS5
)

type chiakiRow struct {
	label  string
	detail string
	status string
	action chiakiAction
	info   bool
}

type chiakiPanelState struct {
	open   bool
	cursor int
	scroll int
}

var chiakiPanel chiakiPanelState
var chiakiPanelRect sdl.Rect
var chiakiRowHits []struct {
	index int
	rect  sdl.Rect
}

func openChiakiPanel() {
	qs.open = false
	a11y.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{}
	raPanel.open = false
	chiakiPanel = chiakiPanelState{open: true}
	chiakiPanelRect = sdl.Rect{}
	chiakiRowHits = nil
	PlayActivateSound()
}

func closeChiakiPanel() {
	if !chiakiPanel.open {
		return
	}
	chiakiPanel = chiakiPanelState{}
	chiakiPanelRect = sdl.Rect{}
	chiakiRowHits = nil
	PlayBackSound()
}

func chiakiRows() []chiakiRow {
	rows := make([]chiakiRow, 0, 9)
	if chiakiInstalled() {
		version := chiakiInstalledVersion()
		if version == "" {
			version = "installed"
		}
		rows = append(rows, chiakiRow{
			label:  "Update Chiaki-ng",
			detail: "Install the latest release over the existing app; settings and pairing are kept.",
			status: version,
			action: chiakiActionInstall,
		})
		req := chiakiCheckRequirements(chiakiAppDir())
		status := "Ready"
		detail := "JukaHub closes while Chiaki-ng runs; START+SELECT returns to the stock menu."
		if !req.OK {
			status = "Needs setup"
			detail = req.Detail
		}
		rows = append(rows, chiakiRow{
			label:  "Launch Remote Play",
			detail: detail,
			status: status,
			action: chiakiActionLaunch,
		})
		lang := chiakiCurrentLanguage(chiakiAppDir())
		other := "English"
		if lang == "EN" {
			other = "Vietnamese"
		}
		rows = append(rows, chiakiRow{
			label:  "Interface language",
			detail: "Chiaki-ng's own menus are " + chiakiLanguageName(lang) + "; select for " + other + ".",
			status: lang,
			action: chiakiActionLanguage,
		})
	} else {
		rows = append(rows, chiakiRow{
			label:  "Install Chiaki-ng",
			detail: "Download the TrimUI port and its bundled SDL2 / FFmpeg libraries (~13 MB).",
			status: "Install",
			action: chiakiActionInstall,
		})
	}
	// PS5. The published builds are PS4-only; the one PS5 build was withdrawn,
	// so the panel offers the author's beta tag as a separate, opt-in app instead
	// of claiming PS5 works out of the box.
	switch {
	case chiakiSDRootDir() == "":
		// No SD-card root means there is nowhere to install to, which is the
		// same reason the PS4 row cannot proceed on a desktop build.
		rows = append(rows, chiakiRow{
			label:  "PS5 beta build",
			detail: "Needs a TrimUI SD card; this platform has no Apps folder to install into.",
			status: "Unavailable",
			action: chiakiActionNone,
			info:   true,
		})
	case chiakiPS5Installed():
		version := chiakiPS5InstalledVersion()
		if version == "" {
			version = "installed"
		}
		rows = append(rows, chiakiRow{
			label:  "Launch PS5 beta",
			detail: "Pair from Link Device using your PSN Account-ID; PS4 stays on the stable app.",
			status: "Beta",
			action: chiakiActionLaunchPS5,
		})
		rows = append(rows, chiakiRow{
			label:  "Reinstall PS5 beta",
			detail: "Re-download " + chiakiPS5Tag + "; its settings and pairing are kept.",
			status: version,
			action: chiakiActionInstallPS5,
		})
		ps5Lang := chiakiCurrentLanguage(chiakiPS5AppDir())
		ps5Other := "English"
		if ps5Lang == "EN" {
			ps5Other = "Vietnamese"
		}
		rows = append(rows, chiakiRow{
			label:  "PS5 interface language",
			detail: "The PS5 beta's menus are " + chiakiLanguageName(ps5Lang) + "; select for " + ps5Other + ".",
			status: ps5Lang,
			action: chiakiActionLanguagePS5,
		})
	default:
		rows = append(rows, chiakiRow{
			label:  "Install PS5 beta",
			detail: "Author-withdrawn " + chiakiPS5Tag + " build, installed beside PS4. Unsupported.",
			status: "Beta",
			action: chiakiActionInstallPS5,
		})
	}
	rows = append(rows, chiakiRow{
		label:  "PS5 setup",
		detail: "Link Device on the PS5; the app asks for a 12-char Base64 PSN Account-ID, then the 8-digit PIN.",
		status: "Guide",
		action: chiakiActionNone,
		info:   true,
	})
	rows = append(rows, chiakiRow{
		label:  "Open install folder",
		detail: chiakiAppDir(),
		status: "Files",
		action: chiakiActionFolder,
	})
	rows = append(rows, chiakiRow{
		label:  "Close",
		detail: "",
		status: "",
		action: chiakiActionClose,
	})
	return rows
}

func chiakiPanelMove(delta int) {
	rows := chiakiRows()
	if len(rows) == 0 {
		return
	}
	chiakiPanel.cursor += delta
	if chiakiPanel.cursor < 0 {
		chiakiPanel.cursor = len(rows) - 1
	}
	if chiakiPanel.cursor >= len(rows) {
		chiakiPanel.cursor = 0
	}
	PlayNavSound()
}

func chiakiPanelActivate() {
	rows := chiakiRows()
	if chiakiPanel.cursor < 0 || chiakiPanel.cursor >= len(rows) {
		return
	}
	chosen := rows[chiakiPanel.cursor]
	switch chosen.action {
	case chiakiActionInstall:
		if chiakiInstalling() {
			ShowToast("The Chiaki-ng installer is already running", ToastKindInfo)
			return
		}
		if err := startChiakiInstall(); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		installBarPhase(installPhaseResolving)
	case chiakiActionLaunch:
		closeChiakiPanel()
		launchChiaki()
	case chiakiActionInstallPS5:
		if chiakiInstalling() {
			ShowToast("The Chiaki-ng installer is already running", ToastKindInfo)
			return
		}
		if err := startChiakiPS5Install(); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		installBarPhase(installPhaseResolving)
	case chiakiActionLaunchPS5:
		closeChiakiPanel()
		launchChiakiPS5()
	case chiakiActionLanguage, chiakiActionLanguagePS5:
		appDir, label := chiakiAppDir(), chiakiAppName
		if chosen.action == chiakiActionLanguagePS5 {
			appDir, label = chiakiPS5AppDir(), chiakiPS5AppName+" beta"
		}
		if !chiakiInstalledAt(appDir) {
			ShowToast("Install "+label+" first", ToastKindWarning)
			return
		}
		next, err := chiakiToggleLanguage(appDir)
		if err != nil {
			ShowToast("Could not change the language — "+firstLine(err.Error()), ToastKindError)
			return
		}
		ShowToast(label+" menus will open in "+chiakiLanguageName(next), ToastKindSuccess)
	case chiakiActionFolder:
		dir := chiakiAppDir()
		if dir == "" {
			ShowToast("SD card path is unknown", ToastKindWarning)
			return
		}
		_ = os.MkdirAll(dir, 0o755)
		openInExplorer(dir)
	case chiakiActionClose:
		closeChiakiPanel()
	}
}

func ctrlChiakiPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		chiakiPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		chiakiPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		chiakiPanelActivate()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeChiakiPanel()
	}
	return true
}

func handleChiakiKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !chiakiPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		chiakiPanelMove(-1)
	case sdl.K_DOWN:
		chiakiPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		chiakiPanelActivate()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeChiakiPanel()
	}
	return true
}

func handleChiakiMouse(mx, my int32) bool {
	if !chiakiPanel.open {
		return false
	}
	for _, hit := range chiakiRowHits {
		if pointInRect(mx, my, hit.rect) {
			chiakiPanel.cursor = hit.index
			chiakiPanelActivate()
			return true
		}
	}
	if chiakiPanelRect.W > 0 && pointInRect(mx, my, chiakiPanelRect) {
		return true
	}
	closeChiakiPanel()
	return true
}

// renderChiakiPanel draws the install/launch card. It follows the same visual
// language as the Ports and Tools panels so it does not look bolted on.
func renderChiakiPanel(renderer *sdl.Renderer, config *Config) {
	if !chiakiPanel.open || renderer == nil {
		return
	}
	rows := chiakiRows()
	if chiakiPanel.cursor >= len(rows) {
		chiakiPanel.cursor = len(rows) - 1
	}
	if chiakiPanel.cursor < 0 {
		chiakiPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = int32(24)
	rowH := 8 + medH + 4 + smallH + 8
	headerH := 18 + medH + 4 + smallH + 14
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	chiakiPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if chiakiPanel.cursor < chiakiPanel.scroll {
		chiakiPanel.scroll = chiakiPanel.cursor
	}
	if chiakiPanel.cursor >= chiakiPanel.scroll+int(visible) {
		chiakiPanel.scroll = chiakiPanel.cursor - int(visible) + 1
	}
	if chiakiPanel.scroll > len(rows)-int(visible) {
		chiakiPanel.scroll = len(rows) - int(visible)
	}
	if chiakiPanel.scroll < 0 {
		chiakiPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, chiakiPanelTitle, ColorTextPrimary(), panelX+padX, panelY+18)
	subtitle := "TrimUI Chiaki-ng port · PS4 + PS5 beta"
	if chiakiInstalled() {
		if v := chiakiInstalledVersion(); v != "" {
			subtitle += " · " + v
		}
	}
	renderText(renderer, config, font, subtitle, ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	chiakiRowHits = chiakiRowHits[:0]
	for i := int32(0); i < visible; i++ {
		idx := chiakiPanel.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		chiakiRowHits = append(chiakiRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: idx, rect: r})

		focused := idx == chiakiPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, row.label, ColorTextPrimary(), r.X+16, r.Y+8)
		if row.status != "" {
			statusColor := ColorTextSecondary()
			if row.status == "Install" || row.status == "Ready" {
				statusColor = focusColor
			}
			if row.info {
				statusColor = ColorTextTertiary()
			}
			sw, _, _ := font.SizeUTF8(row.status)
			renderText(renderer, config, font, row.status, statusColor, r.X+r.W-16-int32(sw), r.Y+8+(medH-smallH)/2)
		}
		if row.detail != "" {
			color := ColorTextTertiary()
			if row.info {
				color = ColorTextSecondary()
			}
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), color, r.X+16, r.Y+8+medH+4)
		}
	}

	hint := "A: select   B: close"
	if !chiakiInstalled() {
		hint = "A: install Chiaki-ng   B: close"
	}
	if chiakiInstalling() {
		hint = "Installing — this can take a few minutes"
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
