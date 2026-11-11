package main

// windowsapps.go — Windows Apps: finds Windows programs and starts them, on
// the handheld through Wine and a display, on Windows directly.
//
// A launch has two halves. The runtime half — Box64 and Wine, the prefix — is
// planWineRuntime's (wine_runtime.go). The display half — WestonPack on a
// handheld with no X11 or Wayland — is prepareWineDisplay's (wine_display.go).
// This file runs the two together for Windows Apps and for Wine game profiles
// (wineStart), so both get the same launch:
//
//   - the slow part (a first run creates the prefix for minutes; later ones
//     still take tens of seconds) runs on a goroutine, its progress on the
//     app's row, and a second A on a starting app does nothing;
//   - on the handheld the pad is the program's mouse, with a few keys, from
//     before its compositor starts until the session ends (wineInputStart);
//   - the screen is handed over and back like any game's (gamesuspend.go):
//     parked while anything the launch started still runs;
//   - each app writes its own log: the runtime, the display, the exact command
//     line (secret values hidden) and the program's output;
//   - when the program exits, `westonwrap.sh cleanup` runs, the runtime puts
//     its things away (plan.After), leftovers are stopped, the WestonPack image
//     is unmounted — each step bounded, so nothing keeps the screen parked.
//
// On Windows the same launcher starts .exe files directly, and .bat / .cmd
// files through cmd.exe, in the program's own folder.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	windowsAppsEntryName = "Windows Apps"

	windowsAppMaxScanDepth = 4
	windowsAppMaxEntries   = 200

	// wineQuickExit: a session that ends with an error sooner than this is
	// reported like a launch that failed (the program, or its display, did not
	// come up), with its log.
	wineQuickExit = 30 * time.Second
	// wineGroupGrace is how long what a program left in its process group
	// (Wine's helper processes, a compositor the clean-up missed) gets to exit
	// after the clean-up before it is stopped.
	wineGroupGrace = 6 * time.Second
	// wineAfterLimit bounds the runtime's clean-up (plan.After).
	wineAfterLimit = 45 * time.Second
)

// windowsAppEntry is one program found by the scanner. Path is the real file;
// Rel keeps the row readable when several apps share a title.
type windowsAppEntry struct {
	Title string
	Path  string
	Rel   string
}

// windowsRuntimeStatus is the single "can this device launch an exe?" answer
// used by the panel, the Emulators row and the setup page. The runtime part
// comes from currentWineRuntimeFacts, the display part from
// resolveWineDisplay.
type windowsRuntimeStatus struct {
	Wine    string
	Box64   string
	Display string
	Weston  string
	DXVK    string
	Vulkan  bool
	Problem string
	// Flavor names the runtime ("Box64 + Wine 10.0", "Windows"); Prefix is
	// where the shared prefix is or would be.
	Flavor string
	Prefix string
}

func (s windowsRuntimeStatus) ready() bool { return s.Problem == "" }

func (s windowsRuntimeStatus) displayLabel() string {
	return windowsDisplayLabel(s.Display)
}

// windowsDisplayLabel names a display mode for people.
func windowsDisplayLabel(mode string) string {
	if label, ok := windowsAppDisplayNames[mode]; ok {
		return label
	}
	return mode
}

// currentWindowsRuntimeStatus resolves the runtime and the display without
// installing, mounting or starting anything. It reads files, so the panel
// keeps a copy (windowsRuntimeStatusCached) instead of calling it per frame.
func currentWindowsRuntimeStatus() windowsRuntimeStatus {
	facts := currentWineRuntimeFacts()
	status := windowsRuntimeStatus{
		Wine:   facts.Wine,
		Box64:  facts.Box64,
		Flavor: facts.Flavor,
		Prefix: facts.Prefix,
	}
	if IsWindows() {
		// Windows runs the programs itself: nothing to install, nothing missing.
		status.Display = windowsDisplayDesktop
		return status
	}
	host := currentWineHost()
	status.Weston = host.westonPath()
	if dxvk, err := resolveDXVK(); err == nil {
		status.DXVK = dxvk
	}
	status.Vulkan = resolveVulkan() != ""
	display, displayErr := resolveWineDisplay("", host)
	if displayErr == nil {
		status.Display = display
	}
	switch {
	case facts.Problem != "":
		status.Problem = facts.Problem
	case displayErr != nil:
		status.Problem = displayErr.Error()
	}
	return status
}

// executableFile is the stricter check used for wrapper scripts. A regular
// but non-executable script would otherwise be reported as ready and then
// fail at exec time.
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
	dirs := []string{windowsAppsDir()}
	if !IsWindows() {
		dirs = append(dirs, filepath.Join(windowsRuntimeDir(), "bin"))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logWarn("windowsapps", "setup failed: %v", err)
		}
	}
}

// windowsAppRoots lists where programs are discovered. The app folder is
// always there; on the handheld the usual PC/Windows game folders are included
// so a user does not have to copy a game twice.
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

// invalidateWindowsAppsCache drops the scan and the panel's runtime status, so
// both are read again (after an install, on rescan, when the panel opens).
func invalidateWindowsAppsCache() {
	windowsAppsCache.mu.Lock()
	windowsAppsCache.built = false
	windowsAppsCache.entries = nil
	windowsAppsCache.issues = nil
	windowsAppsCache.roots = nil
	windowsAppsCache.mu.Unlock()
	windowsRuntimeStatusForget()
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
			logDebug("windowsapps", "%s", issue)
		}
	}
	out := make([]windowsAppEntry, len(windowsAppsCache.entries))
	copy(out, windowsAppsCache.entries)
	return out
}

// windowsAppKnown reports whether path is a program the scanner found: the
// only files the launcher starts.
func windowsAppKnown(path string) bool {
	for _, entry := range windowsAppEntries() {
		if entry.Path == path {
			return true
		}
	}
	return false
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
			if !windowsAppLaunchable(d.Name()) || skipWindowsExecutable(d.Name()) {
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
			issues = append(issues, fmt.Sprintf("more than %d programs found; only the first %d are listed", windowsAppMaxEntries, windowsAppMaxEntries))
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

// windowsAppLaunchable reports a file the launcher lists: an .exe everywhere,
// and on Windows also a batch file, which runs through cmd.exe.
func windowsAppLaunchable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe":
		return true
	case ".bat", ".cmd":
		return IsWindows()
	}
	return false
}

// windowsBatchFile reports a Windows batch file (.bat, .cmd).
func windowsBatchFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".bat" || ext == ".cmd"
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
	return strings.HasPrefix(lower, "unins") // uninstallers
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

// ── Per-app display ──────────────────────────────────────────────────────

// windowsAppPrefs remembers the apps that use WestonPack's OpenGL mode instead
// of the Windows (GDI) one (Y on a row, windowsapps_ui.go). It is kept in
// data/windows-apps/display.json as the list of those apps' paths; every
// other app uses the default.
var windowsAppPrefs struct {
	mu     sync.Mutex
	loaded bool
	openGL map[string]bool
}

// windowsAppPrefsFile is display.json.
type windowsAppPrefsFile struct {
	SchemaVersion int      `json:"schemaVersion"`
	OpenGL        []string `json:"opengl,omitempty"`
}

func windowsAppPrefsPath() string { return filepath.Join(windowsAppsDir(), "display.json") }

// windowsAppPrefsLoadLocked reads display.json once. A missing file is the
// default; one that cannot be read is logged and taken as empty, and the next
// change replaces it.
func windowsAppPrefsLoadLocked() {
	if windowsAppPrefs.loaded {
		return
	}
	windowsAppPrefs.loaded = true
	windowsAppPrefs.openGL = map[string]bool{}
	data, err := os.ReadFile(windowsAppPrefsPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logWarn("windowsapps", "display choices not read: %v", err)
		}
		return
	}
	var file windowsAppPrefsFile
	if err := json.Unmarshal(stripUTF8BOM(data), &file); err != nil {
		logWarn("windowsapps", "display choices not read (%s): %v", windowsAppPrefsPath(), err)
		return
	}
	for _, path := range file.OpenGL {
		if path = strings.TrimSpace(path); path != "" {
			windowsAppPrefs.openGL[path] = true
		}
	}
}

// windowsAppOpenGL reports whether the app at path uses the OpenGL mode.
func windowsAppOpenGL(path string) bool {
	windowsAppPrefs.mu.Lock()
	defer windowsAppPrefs.mu.Unlock()
	windowsAppPrefsLoadLocked()
	return windowsAppPrefs.openGL[path]
}

// windowsAppSetOpenGL stores the app's display choice. The file is written
// whole and atomically; when that fails, the choice is not changed.
func windowsAppSetOpenGL(path string, on bool) error {
	windowsAppPrefs.mu.Lock()
	defer windowsAppPrefs.mu.Unlock()
	windowsAppPrefsLoadLocked()
	was := windowsAppPrefs.openGL[path]
	if on {
		windowsAppPrefs.openGL[path] = true
	} else {
		delete(windowsAppPrefs.openGL, path)
	}
	file := windowsAppPrefsFile{SchemaVersion: 1}
	for p := range windowsAppPrefs.openGL {
		file.OpenGL = append(file.OpenGL, p)
	}
	sort.Strings(file.OpenGL)
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = AtomicWrite(windowsAppPrefsPath(), append(data, '\n'), 0o644)
	}
	if err != nil {
		if was {
			windowsAppPrefs.openGL[path] = true
		} else {
			delete(windowsAppPrefs.openGL, path)
		}
		return err
	}
	return nil
}

// ── Launching ────────────────────────────────────────────────────────────

// windowsAppRunning holds the pid of each Windows app that runs, by path.
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

// wineLaunchSettings is what a launch needs from the settings. The launch runs
// on a goroutine, which must not read the config, so the main thread takes a
// copy whenever a launch may follow (wineLaunchSettingsTake): when Windows Apps
// opens, and when an app or a Wine profile is started.
type wineLaunchSettings struct {
	bridge mbridgeParams
	power  powerProfileConfig
}

var wineLaunchSettingsCopy atomic.Pointer[wineLaunchSettings]

// wineLaunchSettingsTake copies the settings a launch needs. Main thread only.
func wineLaunchSettingsTake(config *Config) {
	// The pad session buzzes from its goroutine: find the motor here, on the
	// main thread, as vcursorBridgeTick does.
	hapticsAvailable()
	bridge := mbridgeDefaultParams()
	if config != nil {
		bridge = mbridgeParamsFor(config)
	}
	wineLaunchSettingsCopy.Store(&wineLaunchSettings{
		bridge: bridge,
		power:  loadPowerProfileConfig(config),
	})
}

// wineLaunchSettingsNow is the last copy, or the defaults when no screen took
// one (a launch from somewhere that never opened Windows Apps).
func wineLaunchSettingsNow() wineLaunchSettings {
	if s := wineLaunchSettingsCopy.Load(); s != nil {
		return *s
	}
	return wineLaunchSettings{bridge: mbridgeDefaultParams(), power: loadPowerProfileConfig(nil)}
}

// wineLaunch is one Windows program to start, from Windows Apps or a Wine game
// profile.
type wineLaunch struct {
	Title string
	// Key is what the starting and running state is kept under: the app's
	// path, or "profile:" and the profile's id.
	Key  string
	Exe  string
	Args []string
	Dir  string
	// Env is the caller's own environment (a profile's), which wins over the
	// runtime's defaults.
	Env map[string]string
	// Display is the requested display ("" = automatic); OpenGL picks
	// WestonPack's OpenGL mode where WestonPack is the display.
	Display string
	OpenGL  bool
	LogPath string
	// Started runs once the program is up, Ended once its whole session is
	// over; Failed reports a launch that did not start, or ended with an error
	// right away (wineQuickExit). All three run on the launch's goroutine:
	// none may touch config or SDL.
	Started func(pid int)
	Ended   func(err error)
	Failed  func(err error, logPath string)
}

// wineLaunches is the state of Windows programs from A to the end of their
// session: what a starting one is doing (phase, by key; gone once it runs),
// the one session that owns the screen and the pad (busy, named busyTitle),
// and the last Windows Apps launch that failed after A returned, for the panel
// to show (windowsAppsTakeFailure).
var wineLaunches struct {
	mu        sync.Mutex
	phase     map[string]string
	busy      string
	busyTitle string
	failed    *windowsAppFailure
}

// windowsAppFailure is a Windows Apps launch that failed on its goroutine.
type windowsAppFailure struct {
	entry windowsAppEntry
	err   error
	log   string
}

// wineLaunchPhase is what a starting program is doing, "" when it is not
// starting.
func wineLaunchPhase(key string) string {
	wineLaunches.mu.Lock()
	defer wineLaunches.mu.Unlock()
	return wineLaunches.phase[key]
}

func wineSetPhase(key, phase string) {
	wineLaunches.mu.Lock()
	defer wineLaunches.mu.Unlock()
	if wineLaunches.busy != key {
		return
	}
	if phase == "" {
		delete(wineLaunches.phase, key)
		return
	}
	if wineLaunches.phase == nil {
		wineLaunches.phase = map[string]string{}
	}
	wineLaunches.phase[key] = phase
}

// wineLaunchEnded frees the launcher for the next program.
func wineLaunchEnded(key string) {
	wineLaunches.mu.Lock()
	defer wineLaunches.mu.Unlock()
	delete(wineLaunches.phase, key)
	if wineLaunches.busy == key {
		wineLaunches.busy, wineLaunches.busyTitle = "", ""
	}
}

// wineStart checks a launch and runs it on a goroutine (wineRun). An error
// here means nothing started; later failures go to l.Failed. One Windows
// program runs at a time: there is one screen and one pad to lend.
func wineStart(l wineLaunch) error {
	if !regularFile(l.Exe) {
		return withLaunchFailureKind(launchFailureProfileExecutableMissing, fmt.Errorf("%s is no longer there", l.Exe))
	}
	if gameScreenSuspended() {
		return errors.New("another game or app is on screen; close it first")
	}
	wineLaunches.mu.Lock()
	defer wineLaunches.mu.Unlock()
	if wineLaunches.busy != "" {
		if wineLaunches.busy == l.Key {
			return fmt.Errorf("%s is already starting", l.Title)
		}
		return fmt.Errorf("%s is still running; one Windows program runs at a time", wineLaunches.busyTitle)
	}
	wineLaunches.busy, wineLaunches.busyTitle = l.Key, l.Title
	if wineLaunches.phase == nil {
		wineLaunches.phase = map[string]string{}
	}
	wineLaunches.phase[l.Key] = "Preparing Windows…"
	if IsWindows() {
		wineLaunches.phase[l.Key] = "Starting…"
	}
	go wineRun(l, wineLaunchSettingsNow())
	return nil
}

// wineRun is one launch, from planning to the screen handed back.
func wineRun(l wineLaunch, settings wineLaunchSettings) {
	log := wineOpenLog(l.LogPath, l.Title)
	defer log.close()
	restorePower := beginGamePowerProfileWith(settings.power)
	failed := func(err error) {
		restorePower()
		log.printf("launch failed: %v\n", err)
		logWarn("windowsapps", "%s did not start: %v", l.Title, err)
		wineLaunchEnded(l.Key)
		if l.Failed != nil {
			l.Failed(err, log.path)
		}
	}
	progress := func(line string) {
		if line = strings.TrimSpace(line); line != "" {
			wineSetPhase(l.Key, line)
			log.printf("runtime: %s\n", line)
		}
	}
	plan, err := planWineRuntime(context.Background(), l.Exe, l.Args, l.Env, progress)
	if err != nil {
		failed(err)
		return
	}
	after := plan.After
	if after == nil {
		after = func() {}
	}
	wineSetPhase(l.Key, "Starting…")
	disp, err := prepareWineDisplay(l.Display, l.OpenGL, plan, log)
	if err != nil {
		wineBounded(log, "the runtime's clean-up", wineAfterLimit, after)
		failed(err)
		return
	}

	// The pad is the mouse from before the compositor starts, so it finds the
	// device; quit is Select + Start held (wineQuit).
	var running atomic.Pointer[exec.Cmd]
	stopInput := func() {}
	if disp.lendsPad() {
		stop, inputErr := wineInputStart(settings.bridge, func() { wineQuit(running.Load(), log) })
		if inputErr != nil {
			log.printf("input: the pad stays a pad: %v\n", inputErr)
			ShowToast("The pad could not become a mouse: "+inputErr.Error(), ToastKindWarning)
		} else {
			stopInput = stop
			log.printf("input: the pad is the mouse; Select + L1 hands it back\n")
		}
		wineSettleInput(log)
	}

	cmd, err := disp.command(l.Dir)
	if err == nil {
		log.printf("runtime: %s\n", plan.Flavor)
		if plan.Prefix != "" {
			log.printf("prefix: %s\n", plan.Prefix)
		}
		if disp.Detail != "" {
			log.printf("display: %s (%s)\n", windowsDisplayLabel(disp.Mode), disp.Detail)
		} else {
			log.printf("display: %s\n", windowsDisplayLabel(disp.Mode))
		}
		// A profile's own variables are named but their values stay out of
		// the log (a token in "env" is not copied there); the runtime's are
		// shown, and secret-looking names are hidden either way.
		hide := map[string]bool{}
		for key := range l.Env {
			if !strings.EqualFold(key, "WINEPREFIX") {
				hide[key] = true
			}
		}
		if len(plan.Env) > 0 {
			log.printf("environment: %s\n", wineLogEnv(plan.Env, hide))
		}
		log.printf("command: %s\nworking dir: %s\n", wineLogCommand(disp.Command, disp.Args, hide), l.Dir)
		if log.f != nil {
			cmd.Stdout, cmd.Stderr = log.f, log.f
		}
		configureGameChild(cmd)
		// The program leads its own process group, so whatever the wrapper
		// starts in the background keeps the screen parked until it is gone.
		screenChildGroupConfigure(cmd)
		err = cmd.Start()
	}
	if err != nil {
		stopInput()
		disp.stop(log)
		wineBounded(log, "the runtime's clean-up", wineAfterLimit, after)
		disp.release(log)
		failed(fmt.Errorf("start %s: %w", filepath.Base(disp.Command), err))
		return
	}
	running.Store(cmd)
	started := time.Now()
	noteGameChildStarted(cmd)
	wineSetPhase(l.Key, "")
	if l.Started != nil {
		l.Started(cmd.Process.Pid)
	}
	logInfo("windowsapps", "started %s (pid %d, %s, %s)", l.Title, cmd.Process.Pid, plan.Flavor, windowsDisplayLabel(disp.Mode))

	waitErr := cmd.Wait()
	ran := time.Since(started).Round(time.Second)
	if waitErr != nil {
		log.printf("exit after %s: %v\n", ran, waitErr)
	} else {
		log.printf("exit after %s: 0\n", ran)
	}
	// Put everything away before the launcher is free again: the next launch
	// could otherwise reuse the WestonPack mount this one is about to take
	// down. The screen comes back last, with the failure (if any) waiting.
	stopInput()
	disp.stop(log)
	wineDrainGroup(cmd, log)
	wineBounded(log, "the runtime's clean-up", wineAfterLimit, after)
	disp.release(log)
	if waitErr != nil {
		logWarn("windowsapps", "%s exited after %s: %v", l.Title, ran, waitErr)
		if ran < wineQuickExit && l.Failed != nil {
			l.Failed(fmt.Errorf("%s closed after %s (%v); the log says why", l.Title, ran, waitErr), log.path)
		}
	}
	if l.Ended != nil {
		l.Ended(waitErr)
	}
	wineLaunchEnded(l.Key)
	screenChildReaped(cmd)
	restorePower()
}

// wineOpenLog opens a launch's log (appending, rotated past its cap like a
// game profile's) and writes the start line. A log that cannot be opened is
// skipped, not a reason to refuse the launch.
func wineOpenLog(path, title string) wineLog {
	if path == "" {
		return wineLog{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return wineLog{}
	}
	rotateGameProfileLog(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return wineLog{}
	}
	fmt.Fprintf(f, "\n=== %s start %s ===\n", title, time.Now().Format(time.RFC3339))
	return wineLog{f: f, path: path}
}

// wineBounded runs one clean-up step and waits for it at most limit: a step
// that hangs keeps running on its own, but cannot keep the screen parked.
func wineBounded(log wineLog, what string, limit time.Duration, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				log.printf("%s failed: %v\n", what, r)
			}
		}()
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		log.printf("%s took longer than %s; going on without it\n", what, limit)
	}
}

// wineDrainGroup gives what the program left in its process group — Wine's
// helper processes, a compositor the clean-up missed — wineGroupGrace to exit,
// then stops it: an interrupt, and a kill for what ignores that. Until the
// group is empty the screen stays parked (screenChildReaped).
func wineDrainGroup(cmd *exec.Cmd, log wineLog) {
	deadline := time.Now().Add(wineGroupGrace)
	for screenChildGroupAlive(cmd) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !screenChildGroupAlive(cmd) {
		return
	}
	log.printf("processes were still running %s after the program; stopping them\n", wineGroupGrace)
	_ = screenChildSignal(cmd, screenSignalInterrupt)
	for i := 0; i < 20 && screenChildGroupAlive(cmd); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if screenChildGroupAlive(cmd) {
		_ = screenChildKill(cmd)
	}
}

// wineQuit ends a session's program from the pad (Select + Start held, after
// Alt+F4 did not close it): an interrupt to its process group, then a kill.
// The session's own goroutine does the rest of the clean-up.
func wineQuit(cmd *exec.Cmd, log wineLog) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	log.printf("Select + Start held: ending the program\n")
	_ = screenChildSignal(cmd, screenSignalInterrupt)
	for i := 0; i < 30 && screenChildGroupAlive(cmd); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if screenChildGroupAlive(cmd) || IsWindows() {
		_ = screenChildKill(cmd)
	}
}

// launchWindowsApp starts one app found by the scanner. It returns once the
// launch is under way; the slow part and its failures happen on a goroutine
// (wineStart). The provisioning chain (windows_runtime_install.go) calls it
// too, from its own goroutine, once the runtime is installed.
func launchWindowsApp(entry windowsAppEntry) error {
	if !regularFile(entry.Path) {
		return fmt.Errorf("%s is no longer present", entry.Path)
	}
	if !windowsAppKnown(entry.Path) {
		return fmt.Errorf("%s is not in the Windows Apps folders", entry.Path)
	}
	if pid := windowsAppRunningPID(entry.Path); pid > 0 {
		return fmt.Errorf("%s is already running (pid %d)", entry.Title, pid)
	}
	return wineStart(wineLaunch{
		Title:   entry.Title,
		Key:     entry.Path,
		Exe:     entry.Path,
		Dir:     filepath.Dir(entry.Path),
		OpenGL:  windowsAppOpenGL(entry.Path),
		LogPath: windowsAppLogPath(entry),
		Started: func(pid int) {
			windowsAppSetRunning(entry.Path, pid)
			trophyEvent(tkAppLaunch)
		},
		Ended:  func(error) { windowsAppSetRunning(entry.Path, 0) },
		Failed: func(err error, logPath string) { windowsAppsLaunchFailed(entry, err, logPath) },
	})
}

// windowsAppsLaunchFailed reports a Windows Apps launch that failed on its
// goroutine: a toast now, and the launch-failure dialog — or the setup page,
// for a missing runtime — when the panel is next drawn (windowsAppsTakeFailure;
// both are main-thread state).
func windowsAppsLaunchFailed(entry windowsAppEntry, err error, logPath string) {
	wineLaunches.mu.Lock()
	wineLaunches.failed = &windowsAppFailure{entry: entry, err: err, log: logPath}
	wineLaunches.mu.Unlock()
	ShowToast(entry.Title+": "+firstLine(err.Error()), ToastKindError)
}

// windowsAppsTakeFailure hands the panel the last failure, once.
func windowsAppsTakeFailure() (windowsAppFailure, bool) {
	wineLaunches.mu.Lock()
	defer wineLaunches.mu.Unlock()
	f := wineLaunches.failed
	wineLaunches.failed = nil
	if f == nil {
		return windowsAppFailure{}, false
	}
	return *f, true
}

func windowsAppStatus(entry windowsAppEntry, runtime windowsRuntimeStatus) string {
	if windowsAppRunningPID(entry.Path) > 0 {
		return "Running"
	}
	if wineLaunchPhase(entry.Path) != "" {
		return "Starting…"
	}
	if !runtime.ready() {
		return "Unavailable"
	}
	return "Ready"
}
