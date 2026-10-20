package main

// chiaki.go — PS4 Remote Play through the TrimUI Chiaki-ng port.
//
// The upstream chiaki-ng project is a desktop client that requires Vulkan on
// the current release. This integration targets the separate
// nlkcodenew/trimui-chiaki-ng port instead: it ships Python wrappers plus
// native aarch64 helpers and the SDL2/FFmpeg/OpenSSL libraries the Brick Pro
// needs, so it can run from an SD card on TrimUI firmware without a desktop.
//
// The port currently supports PS4 Remote Play only. PS5 pairing is explicitly
// disabled by its README and PS5_STATUS.md, so the panel says so rather than
// pretending the button enables PS5 streaming. The installer keeps the
// package's own settings and pairing data when it updates.
//
// JukaHub only downloads the official release ZIP from the project's GitHub
// releases, verifies the SHA-256 digest published by GitHub, extracts it in a
// staging folder on the SD card, and then swaps the app folder into place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
	chiakiAppName       = "Chiaki-ng"
	chiakiPanelTitle    = "PS Remote Play"
	chiakiRepo          = "nlkcodenew/trimui-chiaki-ng"
	chiakiInstallSubdir = "Apps/Chiaki"
	chiakiVersionFile   = ".jukahub-chiaki-version"
	chiakiZipMaxBytes   = 64 << 20
	chiakiZipMaxEntries = 8192
	chiakiZipMaxExpand  = 256 << 20
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
	launcher := chiakiLauncherPath()
	return launcher != "" && regularFile(launcher) && regularFile(filepath.Join(filepath.Dir(launcher), "app.py"))
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
	req.Python = findPython3()
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
	if req.Python == "" {
		req.Detail = "Python 3.8+ not found — update the firmware (1.0.4+)"
		return req
	}
	if req.SDL2 == "" {
		req.Detail = "SDL2 not found in the firmware or package"
		return req
	}
	if req.Libs == 0 {
		req.Detail = "Package runtime libraries are missing"
		return req
	}
	for _, rel := range []string{"bin/chiaki-stream", "bin/chiaki-regist"} {
		if !regularFile(filepath.Join(appDir, filepath.FromSlash(rel))) {
			req.Detail = "Package helper " + rel + " is missing"
			return req
		}
	}
	req.OK = true
	req.Detail = fmt.Sprintf("Python %s · SDL2 %s · %d bundled libs", filepath.Base(req.Python), filepath.Base(req.SDL2), req.Libs)
	return req
}

func findPython3() string {
	for _, name := range []string{"python3", "python3.12", "python3.11", "python3.10"} {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return p
		}
	}
	for _, cand := range []string{
		"/usr/bin/python3",
		"/usr/trimui/bin/python3",
		"/mnt/SDCARD/Apps/Python/bin/python3",
		"/mnt/SDCARD/Apps/python/bin/python3",
	} {
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
	appDir := chiakiAppDir()
	launcher := chiakiLauncherPath()
	if !chiakiPackageSupported() {
		ShowToast("Chiaki-ng runs on the TrimUI Linux build", ToastKindWarning)
		return
	}
	if req := chiakiCheckRequirements(appDir); !req.OK {
		ShowToast(req.Detail, ToastKindWarning)
		return
	}

	logFile, err := os.OpenFile(filepath.Join(appDir, "Chiaki-jukahub.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logFile = nil
	}
	if logFile != nil {
		fmt.Fprintf(logFile, "\n=== JukaHub launch %s ===\n", time.Now().Format(time.RFC3339))
		defer logFile.Close()
	}

	cmd := exec.Command("sh", launcher)
	cmd.Dir = appDir
	cmd.Env = chiakiLaunchEnv(appDir, os.Environ())
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	log.Printf("[chiaki] launching %s", launcher)
	if mainWindow != nil {
		mainWindow.Hide()
	}
	runErr := cmd.Run()
	if mainWindow != nil {
		mainWindow.Show()
		mainWindow.Raise()
	}
	if runErr != nil {
		log.Printf("[chiaki] launcher exited: %v", runErr)
		ShowToast("Chiaki-ng exited — check Chiaki-jukahub.log", ToastKindError)
		return
	}
	ShowToast("Chiaki-ng closed", ToastKindInfo)
}

// ── Panel ────────────────────────────────────────────────────────────────

type chiakiAction int

const (
	chiakiActionInstall chiakiAction = iota
	chiakiActionLaunch
	chiakiActionFolder
	chiakiActionClose
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
	rows := make([]chiakiRow, 0, 5)
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
		detail := "Pair a PS4 inside Chiaki-ng, then select it to stream."
		if !req.OK {
			status = "Needs setup"
		}
		detail = req.Detail
		rows = append(rows, chiakiRow{
			label:  "Launch Remote Play",
			detail: detail,
			status: status,
			action: chiakiActionLaunch,
		})
	} else {
		rows = append(rows, chiakiRow{
			label:  "Install Chiaki-ng",
			detail: "Download the TrimUI port and its bundled SDL2 / FFmpeg libraries (~13 MB).",
			status: "Install",
			action: chiakiActionInstall,
		})
	}
	rows = append(rows, chiakiRow{
		label:  "Open install folder",
		detail: chiakiAppDir(),
		status: "Files",
		action: chiakiActionFolder,
	})
	rows = append(rows, chiakiRow{
		label:  "PS5 status",
		detail: "This TrimUI build supports PS4 Remote Play. PS5 pairing is not supported yet.",
		status: "PS4 only",
		info:   true,
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
	switch rows[chiakiPanel.cursor].action {
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
	subtitle := "TrimUI Chiaki-ng port · PS4 Remote Play"
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
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
