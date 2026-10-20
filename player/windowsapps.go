package main

// windowsapps.go — a small Windows-app launcher for the ARM64 handheld build.
//
// Wine is only half of the problem. It still needs a display server, and a
// stock TrimUI Smart Pro has neither X11 nor Wayland. This module detects the
// pieces that can bridge that gap:
//
//   * a native ARM64 Wine (Hangover, or a PortMaster build), optionally with
//     Box64 when the Wine binary is x86-64;
//   * an existing X11 or Wayland session; or
//   * WestonPack's westonwrap.sh, which starts a minimal Weston/Xwayland
//     session on DRM/KMS so basic Win32 windows can be shown on the handheld.
//
// Nothing large is downloaded here. Wine, Box64 and WestonPack are
// glibc-sensitive binaries and must match the firmware, so the panel explains
// what is missing and launches only when the user has installed them.
//
// "Basic" is deliberate: console programs and simple Win32 windows are the
// target. Direct3D games still need DXVK/VKD3D and a capable Vulkan driver.

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	windowsAppsEntryName = "Windows Apps"

	windowsDisplayAuto     = "auto"
	windowsDisplayX11      = "x11"
	windowsDisplayWayland  = "wayland"
	windowsDisplayWeston   = "weston"
	windowsDisplayHeadless = "headless"

	windowsAppMaxScanDepth = 4
	windowsAppMaxEntries   = 200
)

var windowsAppDisplayNames = map[string]string{
	windowsDisplayX11:      "X11",
	windowsDisplayWayland:  "Wayland",
	windowsDisplayWeston:   "WestonPack (DRM/KMS)",
	windowsDisplayHeadless: "Headless",
}

// windowsAppEntry is one executable found by the scanner. Path is the real
// file; Rel keeps the row readable when several apps share a title.
type windowsAppEntry struct {
	Title string
	Path  string
	Rel   string
}

// wineHost abstracts the environment so display selection is testable without
// touching the process environment or a real WestonPack install.
type wineHost struct {
	getenv    func(string) string
	weston    string
	westonErr string
}

func currentWineHost() wineHost {
	weston, err := resolveWestonPack()
	host := wineHost{getenv: os.Getenv, weston: weston}
	if err != nil {
		host.westonErr = err.Error()
	}
	return host
}

// windowsRuntimeStatus is the single "can this device launch an exe?" answer
// used by the panel, the Emulators row and the start menu detail.
type windowsRuntimeStatus struct {
	Wine    string
	Box64   string
	Display string
	Weston  string
	DXVK    string
	Vulkan  bool
	Problem string
}

func (s windowsRuntimeStatus) ready() bool { return s.Problem == "" }

func (s windowsRuntimeStatus) displayLabel() string {
	if label, ok := windowsAppDisplayNames[s.Display]; ok {
		return label
	}
	return s.Display
}

// currentWindowsRuntimeStatus resolves Wine and the display backend. It is
// cheap enough to call when a panel opens or a row is built, but not per row
// per frame.
func currentWindowsRuntimeStatus() windowsRuntimeStatus {
	var status windowsRuntimeStatus
	if IsWindows() {
		status.Problem = "this launcher is for the handheld/Linux build"
		return status
	}
	wine, err := resolveWine(exec.LookPath)
	if err != nil {
		status.Problem = err.Error()
		return status
	}
	status.Wine = wine
	if needsBox64(wine) {
		box, boxErr := resolveBox64(exec.LookPath)
		if boxErr != nil {
			status.Problem = "Wine is x86 and needs Box64: " + boxErr.Error()
			return status
		}
		status.Box64 = box
	}
	host := currentWineHost()
	display, displayErr := resolveWineDisplay("", host)
	status.Weston = host.weston
	if dxvk, dxvkErr := resolveDXVK(); dxvkErr == nil {
		status.DXVK = dxvk
	}
	status.Vulkan = resolveVulkan() != ""
	if displayErr != nil {
		status.Problem = displayErr.Error()
		return status
	}
	status.Display = display
	return status
}

// resolveWineDisplay picks a display backend. "auto" prefers a real desktop
// session, then falls back to WestonPack on DRM/KMS. It intentionally refuses
// to pretend that a bare framebuffer is enough for a Win32 window.
func resolveWineDisplay(requested string, host wineHost) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		if host.getenv != nil {
			requested = strings.ToLower(strings.TrimSpace(host.getenv("JUKAHUB_WINE_DISPLAY")))
		}
	}
	if requested == "" {
		requested = windowsDisplayAuto
	}

	hasWayland := host.getenv != nil && strings.TrimSpace(host.getenv("WAYLAND_DISPLAY")) != ""
	hasX11 := host.getenv != nil && strings.TrimSpace(host.getenv("DISPLAY")) != ""

	switch requested {
	case windowsDisplayAuto:
		// Prefer X11 when both are present: Wine and Hangover default to
		// winex11, and XWayland usually exports DISPLAY alongside Wayland.
		switch {
		case hasX11:
			return windowsDisplayX11, nil
		case hasWayland:
			return windowsDisplayWayland, nil
		case host.weston != "":
			return windowsDisplayWeston, nil
		default:
			if host.westonErr != "" {
				return "", errors.New(host.westonErr)
			}
			return "", errors.New("no display server: Wine needs X11 or Wayland, or WestonPack on DRM/KMS (install WestonPack or set JUKAHUB_WESTON)")
		}
	case windowsDisplayX11:
		if !hasX11 {
			return "", errors.New("display=x11, but DISPLAY is not set")
		}
		return windowsDisplayX11, nil
	case windowsDisplayWayland:
		if !hasWayland {
			return "", errors.New("display=wayland, but WAYLAND_DISPLAY is not set")
		}
		return windowsDisplayWayland, nil
	case windowsDisplayWeston:
		if host.weston == "" {
			if host.westonErr != "" {
				return "", errors.New(host.westonErr)
			}
			return "", errors.New("display=weston, but westonwrap.sh was not found (install WestonPack or set JUKAHUB_WESTON)")
		}
		return windowsDisplayWeston, nil
	case windowsDisplayHeadless:
		return windowsDisplayHeadless, nil
	default:
		return "", fmt.Errorf("unknown display %q (use auto, x11, wayland, weston or headless)", requested)
	}
}

// wrapWineCommand adapts an already-built Wine command for the selected
// display. On WestonPack, the wrapper owns the compositor and takes the app
// command as its final arguments; environment variables are passed explicitly
// because westonwrap.sh does not inherit them for the wrapped app.
func wrapWineCommand(displayRequest, command string, args []string, env map[string]string, host wineHost) (string, []string, error) {
	mode, err := resolveWineDisplay(displayRequest, host)
	if err != nil {
		return "", nil, err
	}
	if env == nil {
		env = map[string]string{}
	}
	switch mode {
	case windowsDisplayWeston:
		if host.weston == "" {
			return "", nil, errors.New("WestonPack is not available")
		}
		wrapped := []string{"drm", "gl", "kiosk", "system"}
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			wrapped = append(wrapped, key+"="+env[key])
		}
		wrapped = append(wrapped, command)
		wrapped = append(wrapped, args...)
		return host.weston, wrapped, nil
	case windowsDisplayHeadless:
		// An explicitly headless profile must not accidentally pick up the
		// parent session's display connection.
		env["DISPLAY"] = ""
		env["WAYLAND_DISPLAY"] = ""
		return command, args, nil
	default:
		return command, args, nil
	}
}

// windowsWineCommand builds the Wine (or Box64 + Wine) command for an exe.
func windowsWineCommand(exe string, args []string, lookPath func(string) (string, error)) (string, []string, error) {
	wine, err := resolveWine(lookPath)
	if err != nil {
		return "", nil, err
	}
	if needsBox64(wine) {
		box, boxErr := resolveBox64(lookPath)
		if boxErr != nil {
			return "", nil, fmt.Errorf("wine is x86 and needs box64: %w", boxErr)
		}
		return box, append([]string{wine, exe}, args...), nil
	}
	return wine, append([]string{exe}, args...), nil
}

// ── DXVK / Vulkan ────────────────────────────────────────────────────────

// resolveDXVK finds a DXVK build the user installed beside Wine or PortMaster.
// JukaHub never downloads it: the DLLs must match the Wine prefix's
// architecture. A detected build is only used when a Vulkan loader is also
// present, because DXVK cannot start without one.
func resolveDXVK() (string, error) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_DXVK")); override != "" {
		if !dxvkBuild(override) {
			return "", fmt.Errorf("JUKAHUB_DXVK does not point at a DXVK directory or its d3d11.dll: %s", override)
		}
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	candidates := []string{
		filepath.Join(windowsRuntimeDir(), "dxvk"),
		filepath.Join(windowsRuntimeDir(), "bin", "dxvk"),
		"/mnt/SDCARD/Apps/DXVK",
		"/mnt/SDCARD/PortMaster/runtimes/dxvk",
		"/mnt/SDCARD/PortMaster/dxvk",
		"/mnt/SDCARD/Apps/Wine/dxvk",
		"/mnt/SDCARD/PortMaster/wine/dxvk",
	}
	for _, candidate := range candidates {
		if dxvkBuild(candidate) {
			return candidate, nil
		}
	}
	return "", nil
}

func dxvkBuild(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return strings.EqualFold(filepath.Base(path), "d3d11.dll") || strings.EqualFold(filepath.Base(path), "d3d9.dll")
	}
	for _, rel := range []string{"x64/d3d11.dll", "d3d11.dll", "x64/d3d9.dll", "d3d9.dll"} {
		if info, err := os.Stat(filepath.Join(path, rel)); err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

// resolveVulkan returns the first Vulkan loader found. DXVK is a Vulkan
// consumer; Wine's built-in D3D path does not require this. The lookup is
// intentionally filesystem-only so a Linux device without vulkaninfo still
// gets a useful diagnostic.
func resolveVulkan() string {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_VULKAN")); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override
		}
		return ""
	}
	candidates := []string{
		"/usr/lib/libvulkan.so.1",
		"/usr/lib64/libvulkan.so.1",
		"/lib/libvulkan.so.1",
		"/usr/trimui/lib/libvulkan.so.1",
		"/usr/lib/aarch64-linux-gnu/libvulkan.so.1",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	for _, pattern := range []string{"/usr/lib/*/libvulkan.so.1", "/lib/*/libvulkan.so.1"} {
		if matches, err := filepath.Glob(pattern); err == nil {
			for _, match := range matches {
				if info, err := os.Stat(match); err == nil && !info.IsDir() {
					return match
				}
			}
		}
	}
	return ""
}

// applyWineCompatibilityEnv enables DXVK through Wine's DLL overrides only when
// both the DLLs and a Vulkan loader are present. Existing user overrides win;
// silently replacing them would make a working setup stop working.
func applyWineCompatibilityEnv(env map[string]string) (dxvk string, vulkan bool) {
	if env == nil {
		return "", false
	}
	dxvk, _ = resolveDXVK()
	vulkan = resolveVulkan() != ""
	if dxvk == "" || !vulkan {
		return dxvk, vulkan
	}
	if !hasEnvKey(env, "WINEDLLOVERRIDES") {
		env["WINEDLLOVERRIDES"] = "dxgi,d3d9,d3d10core,d3d11,d3d12=n,b"
	}
	return dxvk, vulkan
}

func resolveWestonPack() (string, error) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WESTON")); override != "" {
		if !executableFile(override) {
			return "", fmt.Errorf("JUKAHUB_WESTON does not point at an executable file: %s", override)
		}
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	candidates := []string{
		filepath.Join(windowsRuntimeDir(), "westonwrap.sh"),
		filepath.Join(windowsRuntimeDir(), "weston", "westonwrap.sh"),
		"/mnt/SDCARD/PortMaster/runtimes/westonpack/westonwrap.sh",
		"/mnt/SDCARD/PortMaster/runtimes/weston/westonwrap.sh",
		"/mnt/SDCARD/PortMaster/weston/westonwrap.sh",
		"/mnt/SDCARD/Apps/WestonPack/westonwrap.sh",
		"/tmp/weston/westonwrap.sh",
	}
	for _, candidate := range candidates {
		if executableFile(candidate) {
			return candidate, nil
		}
	}
	return "", nil
}

// executableFile is the stricter check used for the WestonPack wrapper. A
// regular but non-executable script would otherwise be reported as ready and
// then fail at exec time.
func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return IsWindows() || info.Mode().Perm()&0o111 != 0
}

// ── Windows-app discovery ────────────────────────────────────────────────

var (
	windowsRuntimeDirOverride string
	windowsAppsRootsOverride  []string
)

func windowsRuntimeDir() string {
	if windowsRuntimeDirOverride != "" {
		return windowsRuntimeDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "windows-runtime")
	}
	return filepath.Join("data", "windows-runtime")
}

func windowsAppsDir() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "windows-apps")
	}
	return filepath.Join("data", "windows-apps")
}

// InitWindowsApps creates the two folders users are expected to drop files
// into. It is best-effort, like InitGameProfiles: a read-only data folder must
// not stop JukaHub from booting.
func InitWindowsApps() {
	for _, dir := range []string{windowsAppsDir(), filepath.Join(windowsRuntimeDir(), "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("[windowsapps] setup failed: %v", err)
		}
	}
}

// windowsAppRoots lists where .exe files are discovered. The app-folder copy
// is always available; on the handheld the usual PC/Windows game folders are
// included so a user does not have to copy a game twice.
func windowsAppRoots() []string {
	if len(windowsAppsRootsOverride) > 0 {
		return dedupeCleanPaths(windowsAppsRootsOverride)
	}
	roots := []string{windowsAppsDir()}
	if !IsWindows() {
		roots = append(roots,
			"/mnt/SDCARD/Roms/PC",
			"/mnt/SDCARD/Apps/Windows",
			"/mnt/SDCARD/Games/Windows",
		)
	}
	return dedupeCleanPaths(roots)
}

var windowsAppsCache struct {
	mu      sync.Mutex
	built   bool
	entries []windowsAppEntry
	issues  []string
	roots   []string
}

func invalidateWindowsAppsCache() {
	windowsAppsCache.mu.Lock()
	windowsAppsCache.built = false
	windowsAppsCache.entries = nil
	windowsAppsCache.issues = nil
	windowsAppsCache.roots = nil
	windowsAppsCache.mu.Unlock()
}

func windowsAppEntries() []windowsAppEntry {
	windowsAppsCache.mu.Lock()
	defer windowsAppsCache.mu.Unlock()
	if !windowsAppsCache.built {
		roots := windowsAppRoots()
		entries, issues := scanWindowsApps(roots)
		windowsAppsCache.entries = entries
		windowsAppsCache.issues = issues
		windowsAppsCache.roots = roots
		windowsAppsCache.built = true
		for _, issue := range issues {
			log.Printf("[windowsapps] %s", issue)
		}
	}
	out := make([]windowsAppEntry, len(windowsAppsCache.entries))
	copy(out, windowsAppsCache.entries)
	return out
}

func windowsAppIssues() []string {
	windowsAppEntries()
	windowsAppsCache.mu.Lock()
	defer windowsAppsCache.mu.Unlock()
	out := make([]string, len(windowsAppsCache.issues))
	copy(out, windowsAppsCache.issues)
	return out
}

// windowsAppRootsCached returns the roots used by the current scan, so the
// empty-state renderer does not call os.MkdirAll every frame.
func windowsAppRootsCached() []string {
	windowsAppEntries()
	windowsAppsCache.mu.Lock()
	defer windowsAppsCache.mu.Unlock()
	out := make([]string, len(windowsAppsCache.roots))
	copy(out, windowsAppsCache.roots)
	return out
}

// scanWindowsApps walks the roots without following symlinks or descending
// into Wine system folders. It stops at a bounded number of entries so a huge
// SD-card tree cannot stall the UI thread.
func scanWindowsApps(roots []string) ([]windowsAppEntry, []string) {
	var entries []windowsAppEntry
	var issues []string
	seen := map[string]bool{}
	for _, root := range dedupeCleanPaths(roots) {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				issues = append(issues, path+": "+err.Error())
				return nil
			}
			if len(entries) >= windowsAppMaxEntries {
				return fs.SkipAll
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil || rel == "." {
				return nil
			}
			depth := strings.Count(filepath.ToSlash(rel), "/") + 1
			if d.IsDir() {
				if depth >= windowsAppMaxScanDepth || skipWindowsScanDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(d.Name()), ".exe") || skipWindowsExecutable(d.Name()) {
				return nil
			}
			real, evalErr := filepath.EvalSymlinks(path)
			if evalErr != nil {
				real = path
			}
			if seen[real] {
				return nil
			}
			seen[real] = true
			entries = append(entries, windowsAppEntry{
				Title: windowsAppTitle(d.Name()),
				Path:  real,
				Rel:   filepath.ToSlash(rel),
			})
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
			issues = append(issues, root+": "+walkErr.Error())
		}
		if len(entries) >= windowsAppMaxEntries {
			issues = append(issues, fmt.Sprintf("more than %d executables found; only the first %d are listed", windowsAppMaxEntries, windowsAppMaxEntries))
			break
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left := strings.ToLower(entries[i].Title)
		right := strings.ToLower(entries[j].Title)
		if left != right {
			return left < right
		}
		return entries[i].Rel < entries[j].Rel
	})
	return entries, issues
}

func skipWindowsScanDir(name string) bool {
	switch strings.ToLower(name) {
	case "windows", "system32", "syswow64", "winsxs", "$recycle.bin",
		"system volume information", ".git", "node_modules", "__macosx":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func skipWindowsExecutable(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "unins") // Wine's uninstaller stubs
}

func windowsAppTitle(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = strings.ReplaceAll(base, "_", " ")
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.Join(strings.Fields(base), " ")
	if strings.TrimSpace(base) == "" {
		return name
	}
	return base
}

func windowsAppDetail(entry windowsAppEntry) string {
	if entry.Rel == "" {
		return entry.Path
	}
	return entry.Rel
}

// ── Launching ────────────────────────────────────────────────────────────

var windowsAppRunning struct {
	mu   sync.Mutex
	pids map[string]int
}

func windowsAppRunningPID(path string) int {
	windowsAppRunning.mu.Lock()
	defer windowsAppRunning.mu.Unlock()
	return windowsAppRunning.pids[path]
}

func windowsAppSetRunning(path string, pid int) {
	windowsAppRunning.mu.Lock()
	defer windowsAppRunning.mu.Unlock()
	if windowsAppRunning.pids == nil {
		windowsAppRunning.pids = make(map[string]int)
	}
	if pid <= 0 {
		delete(windowsAppRunning.pids, path)
		return
	}
	windowsAppRunning.pids[path] = pid
}

// ensureWinePrefix creates one shared prefix unless the caller supplied a
// different WINEPREFIX. Sharing keeps the first launch (wineboot) cost out of
// every subsequent app.
func ensureWinePrefix(env map[string]string) error {
	if hasEnvKey(env, "WINEPREFIX") {
		return nil
	}
	prefix := filepath.Join(windowsRuntimeDir(), "prefix")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return fmt.Errorf("create wine prefix: %w", err)
	}
	env["WINEPREFIX"] = prefix
	return nil
}

func windowsAppLogPath(entry windowsAppEntry) string {
	base := strings.ToLower(windowsAppTitle(filepath.Base(entry.Path)))
	var cleaned strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cleaned.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			cleaned.WriteByte('-')
		}
	}
	name := strings.Trim(cleaned.String(), "-")
	if name == "" {
		name = "app"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(entry.Path))
	return filepath.Join(windowsRuntimeDir(), "logs", fmt.Sprintf("%s-%08x.log", name, h.Sum32()))
}

// launchWindowsApp starts one exe detached from the UI thread and reaps it in
// the background. The command is planned before anything is created, so a
// missing runtime or display never leaves a half-launched process behind.
func launchWindowsApp(entry windowsAppEntry) error {
	if IsWindows() {
		return errors.New("Windows apps are launched directly by Windows")
	}
	if !regularFile(entry.Path) {
		return fmt.Errorf("%s is no longer present", entry.Path)
	}
	if pid := windowsAppRunningPID(entry.Path); pid > 0 {
		return fmt.Errorf("%s is already running (pid %d)", entry.Title, pid)
	}

	command, args, err := windowsWineCommand(entry.Path, nil, exec.LookPath)
	if err != nil {
		return err
	}
	env := map[string]string{}
	if err := ensureWinePrefix(env); err != nil {
		return err
	}
	applyWineCompatibilityEnv(env)
	command, args, err = wrapWineCommand("", command, args, env, currentWineHost())
	if err != nil {
		return err
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = filepath.Dir(entry.Path)
	cmd.Env = mergeGameProfileEnv(os.Environ(), env)
	configureGameChild(cmd)

	var logFile *os.File
	logPath := windowsAppLogPath(entry)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
		if opened, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); logErr == nil {
			logFile = opened
			fmt.Fprintf(logFile, "\n=== %s start %s ===\n", entry.Title, time.Now().Format(time.RFC3339))
			fmt.Fprintf(logFile, "command: %s (args: %d)\nworking dir: %s\n", command, len(args), cmd.Dir)
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			fmt.Fprintf(logFile, "start failed: %v\n", err)
			logFile.Close()
		}
		return fmt.Errorf("start %s: %w", filepath.Base(command), err)
	}
	windowsAppSetRunning(entry.Path, cmd.Process.Pid)
	log.Printf("[windowsapps] launched %s (pid %d)", entry.Title, cmd.Process.Pid)

	go func() {
		waitErr := cmd.Wait()
		windowsAppSetRunning(entry.Path, 0)
		if logFile != nil {
			if waitErr != nil {
				fmt.Fprintf(logFile, "exit: %v\n", waitErr)
			} else {
				fmt.Fprint(logFile, "exit: 0\n")
			}
			logFile.Close()
		}
		if waitErr != nil {
			log.Printf("[windowsapps] %s exited: %v", entry.Title, waitErr)
		}
	}()
	return nil
}

func windowsAppStatus(entry windowsAppEntry, runtime windowsRuntimeStatus) string {
	if windowsAppRunningPID(entry.Path) > 0 {
		return "Running"
	}
	if !runtime.ready() {
		return "Unavailable"
	}
	return "Ready"
}
