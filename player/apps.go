// apps.go backs the "Apps" scene: a real application launcher.
//
// The Apps screen lists launchable applications discovered from the platform:
// on Windows the Start Menu plus an "apps" folder next to the executable, on
// TrimUI the SD-card Apps folder (/mnt/SDCARD/Apps), and on every platform the
// built-in JukaHub applets (JukaLand, Weather, ...). Entries are published to
// the "app_entries" list variable so the generic dynamiclist element renders
// them; activating an entry either launches a binary or opens a built-in scene.

package main

import (
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// appsEntriesVar is the list variable the Apps scene's dynamiclist renders.
const appsEntriesVar = "app_entries"

// Trigger names for the Apps scene action buttons. The scene config and the
// trigger switch share these constants so they cannot drift apart.
const (
	triggerAppRefresh    = "app_refresh"
	triggerAppOpenFolder = "app_open_folder"
)

// builtinScenePrefix marks list entries that open an in-app scene instead of
// launching an external process. It never reaches the filesystem.
const builtinScenePrefix = "scene:"

// builtinActionPrefix marks a built-in applet that opens a panel directly
// rather than navigating to a config scene. It is resolved by
// launchBuiltinAppAction and never reaches the filesystem either.
const builtinActionPrefix = "action:"

// builtinApplets are JukaHub's own apps. They live at the bottom of the Apps
// list so the launcher is complete even on a machine with no external apps.
// Only entries that really open something belong here: the grid is presented as
// launchable apps, so a row that cannot start is noise.
var builtinApplets = []struct {
	Name   string
	Scene  string
	Action string
}{
	{"JukaLand", "JukaLand", ""},
	{"Live TV", "LiveTV", ""},
	{"Podcasts", "Podcasts", ""},
	{"Tickers", "Tickers", ""},
	{"Weather", "Weather", ""},
	{"Unit Converter", "UnitConverter", ""},
	{"Canvas Sandbox", "CanvasSandbox", ""},
	{"Browser", "Browser", ""},
	{"IP Stream", "IPStream", ""},
	{"Audio Player", "MusicPlayer", ""},
	{"PS Remote Play", "", "chiaki"},
}

// maxAppEntries caps a single source so a huge Start Menu cannot flood the
// list with thousands of rows.
const maxAppEntries = 300

// deadAppPaths remembers programs that failed to start in this session, so the
// Refresh button cannot bring a row back that the user already saw fail.
var deadAppPaths = map[string]bool{}

// builtinAppEntries renders the applet table as launcher rows. A nil config
// keeps every applet (the merge-order tests use it); scanInstalledApps always
// passes the live config, so a pruned config never lists an applet whose scene
// it does not have.
func builtinAppEntries(config *Config) []FileEntry {
	entries := make([]FileEntry, 0, len(builtinApplets))
	for _, a := range builtinApplets {
		path := builtinScenePrefix + a.Scene
		if a.Action != "" {
			path = builtinActionPrefix + a.Action
			if !builtinAppActionSupported(a.Action) {
				continue
			}
		} else if config != nil && sceneIndexExact(config, a.Scene) < 0 {
			// The row would open nothing on this config: leave it out of the
			// launcher instead of logging a dead launch after a press.
			continue
		}
		entries = append(entries, FileEntry{
			Name:  a.Name + " (built-in)",
			Path:  path,
			IsDir: false,
		})
	}
	return entries
}

// appsFolder returns the portable "apps" folder next to the executable. Users
// can drop portable apps there without touching the Start Menu.
func appsFolder() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "apps"
	}
	return filepath.Join(filepath.Dir(exe), "apps")
}

// systemAppDirs returns the platform folders that hold installed
// applications: the Windows Start Menu roots, or the TrimUI SD-card Apps
// folder. Other platforms contribute only the portable folder.
func systemAppDirs() []string {
	if runtime.GOOS == "windows" {
		var dirs []string
		for _, env := range []string{"APPDATA", "PROGRAMDATA"} {
			base := strings.TrimSpace(os.Getenv(env))
			if base == "" {
				continue
			}
			dirs = append(dirs, filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs"))
		}
		return dirs
	}
	const sdApps = "/mnt/SDCARD/Apps"
	if st, err := os.Stat(sdApps); err == nil && st.IsDir() {
		return []string{sdApps}
	}
	return nil
}

// scanInstalledApps rebuilds the Apps list and publishes it for the renderer.
// It runs on the main thread from scene entry and from the app_refresh trigger.
func scanInstalledApps(config *Config) {
	entries := collectAppEntries(config, appsFolder(), systemAppDirs())
	config.Variables.Custom[appsEntriesVar] = entries
	log.Printf("[apps] scanned %d app entries", len(entries))
	// Icons for desktop shortcuts are extracted by the shell in the background;
	// the call is a cheap no-op when every tile already has a picture.
	requestAppIconExtraction(appProgramPaths(entries))
}

// collectAppEntries merges the app sources in priority order: portable apps
// folder first, then system app folders, then built-in applets. Display names
// are de-duplicated case-insensitively so a shortcut and a portable copy of
// the same program do not appear twice.
func collectAppEntries(config *Config, appsFolder string, systemDirs []string) []FileEntry {
	entries := scanAppsFolder(appsFolder)
	seen := make(map[string]bool, len(entries)+len(builtinApplets))
	for _, e := range entries {
		seen[strings.ToLower(e.Name)] = true
	}
	for _, dir := range systemDirs {
		for _, e := range scanSystemApps(dir) {
			if len(entries) >= maxAppEntries {
				break
			}
			key := strings.ToLower(e.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			entries = append(entries, e)
		}
	}
	for _, e := range builtinAppEntries(config) {
		key := strings.ToLower(e.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, e)
	}
	if len(deadAppPaths) > 0 {
		alive := entries[:0]
		for _, e := range entries {
			if deadAppPaths[e.Path] {
				continue
			}
			alive = append(alive, e)
		}
		entries = alive
	}
	return entries
}

// appIconImageExts are the image formats a launcher icon may be stored in,
// best first. A TrimUI app folder normally ships icon.png; portable apps on a
// desktop can drop a matching .png/.ico next to the program.
var appIconImageExts = []string{".png", ".webp", ".jpg", ".jpeg", ".bmp", ".ico"}

// isAppIconFile reports whether path names an image the launcher can draw.
func isAppIconFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, want := range appIconImageExts {
		if ext == want {
			return true
		}
	}
	return false
}

// regularIconFile returns path when it is an existing regular image file.
func regularIconFile(path string) string {
	if !isAppIconFile(path) {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// iconInFolder finds the icon inside an application folder. "icon" is the
// TrimUI name (config.json points at it and the launcher itself uses it);
// "logo" and "app" are common alternatives in hand-made app folders.
func iconInFolder(dir string) string {
	for _, base := range []string{"icon", "logo", "app"} {
		for _, ext := range appIconImageExts {
			if icon := regularIconFile(filepath.Join(dir, base+ext)); icon != "" {
				return icon
			}
		}
	}
	return ""
}

// sidecarIcon finds an image stored next to a program under the same name, so
// "Notepad.lnk" can carry "Notepad.png" without a config file.
func sidecarIcon(programPath string) string {
	dir, file := filepath.Split(programPath)
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	if stem == "" {
		return ""
	}
	for _, ext := range appIconImageExts {
		if icon := regularIconFile(filepath.Join(dir, stem+ext)); icon != "" {
			return icon
		}
	}
	return ""
}

// isLauncherFile reports whether a file is something we can offer to launch.
func isLauncherFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".exe", ".lnk", ".bat", ".cmd", ".url", ".sh":
		return true
	}
	return false
}

// skipAppName filters Start Menu noise such as uninstaller shortcuts.
func skipAppName(name string) bool {
	lower := strings.ToLower(name)
	for _, noise := range []string{"uninstall", "readme", "license"} {
		if strings.Contains(lower, noise) {
			return true
		}
	}
	return false
}

// appDisplayName strips the launcher extension from a file name.
func appDisplayName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// scanAppsFolder lists launchable entries in a flat "apps" folder next to the
// executable (portable .exe/.lnk/.bat/.sh files) and in TrimUI-style app
// folders, where each subdirectory holds config.json + launch.sh.
func scanAppsFolder(dir string) []FileEntry {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var entries []FileEntry
	for _, item := range items {
		name := item.Name()
		if item.IsDir() {
			if e, ok := scanAppSubdir(filepath.Join(dir, name)); ok {
				entries = append(entries, e)
			}
			continue
		}
		if !isLauncherFile(name) || skipAppName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		entries = append(entries, FileEntry{
			Name:  appDisplayName(name),
			Path:  path,
			IsDir: false,
			Icon:  sidecarIcon(path),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

// scanAppSubdir reads a TrimUI-style application folder (config.json with a
// "label" and a "launch" script). Returns false when the folder is not an app.
func scanAppSubdir(dir string) (FileEntry, bool) {
	cfgPath := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return FileEntry{}, false
	}
	var cfg struct {
		Label  string `json:"label"`
		Launch string `json:"launch"`
		Icon   string `json:"icon"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[apps] bad app config %s: %v", cfgPath, err)
		return FileEntry{}, false
	}
	launch := strings.TrimSpace(cfg.Launch)
	if launch == "" {
		launch = "launch.sh"
	}
	// The launch value comes from the app's own config.json; it must name a
	// regular file inside the app folder, never "../.." or an absolute path.
	launchPath, err := resolveWithinRoot(dir, launch)
	if err != nil {
		log.Printf("[SECURITY] [apps] %s has an unsafe launch path: %v", cfgPath, err)
		return FileEntry{}, false
	}
	info, err := os.Stat(launchPath)
	if err != nil || !info.Mode().IsRegular() {
		return FileEntry{}, false
	}
	name := strings.TrimSpace(cfg.Label)
	if name == "" {
		name = filepath.Base(dir)
	}
	// The icon lives in the app's own folder: honour config.json's "icon" when
	// it is a safe path, otherwise fall back to the conventional file names.
	icon := ""
	if declared := strings.TrimSpace(cfg.Icon); declared != "" {
		if resolved, err := resolveWithinRoot(dir, declared); err == nil {
			icon = regularIconFile(resolved)
		}
	}
	if icon == "" {
		icon = iconInFolder(dir)
	}
	return FileEntry{Name: name, Path: launchPath, IsDir: false, Icon: icon}, true
}

// scanSystemApps walks a system app folder (Windows Start Menu programs, or
// the SD-card Apps root) and collects launchable shortcuts. The walk is
// bounded in depth so a deeply nested Start Menu stays cheap to scan.
func scanSystemApps(dir string) []FileEntry {
	var entries []FileEntry
	rootDepth := strings.Count(filepath.Clean(dir), string(os.PathSeparator))
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if path == dir {
			return nil
		}
		depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
		if info.IsDir() {
			if depth > 3 {
				return filepath.SkipDir
			}
			// A TrimUI application folder (config.json + launch script) is one
			// app, not a tree of scripts: take its label and icon and skip the
			// rest, so the launcher never lists the same app as a bare
			// "launch" row once per installed app.
			if app, ok := scanAppSubdir(path); ok && !isSelfAppFolder(path) {
				entries = append(entries, app)
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if !isLauncherFile(name) || skipAppName(name) {
			return nil
		}
		entries = append(entries, FileEntry{
			Name:  appDisplayName(name),
			Path:  path,
			IsDir: false,
			Icon:  sidecarIcon(path),
		})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

// isSelfAppFolder reports whether dir is the folder JukaHub itself runs from.
// Listing the running app in its own launcher only restarts it, so it is left
// out of the list.
func isSelfAppFolder(dir string) bool {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return false
	}
	exeDir, err := filepath.Abs(filepath.Dir(exe))
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(exeDir), filepath.Clean(absDir))
}

// appEntryTarget splits an Apps row into its action: either a built-in scene
// name or an external program path (exactly one of the two is set).
func appEntryTarget(entry FileEntry) (scene string, external string) {
	if strings.HasPrefix(entry.Path, builtinScenePrefix) {
		return strings.TrimPrefix(entry.Path, builtinScenePrefix), ""
	}
	return "", entry.Path
}

// launchAppEntry activates a row from the Apps list: built-in entries open the
// matching JukaHub scene, everything else launches the external program.
func launchAppEntry(config *Config, entry FileEntry) {
	if action := strings.TrimPrefix(entry.Path, builtinActionPrefix); action != entry.Path {
		launchBuiltinAppAction(action)
		return
	}
	scene, external := appEntryTarget(entry)
	if scene != "" {
		// The audio player is created on demand: older jukaconfig.json files do
		// not list a MusicPlayer scene.
		if scene == "MusicPlayer" {
			openAudioPlayerBrowser(config, musicDefaultDir(config))
			return
		}
		if idx := findSceneIndex(config, scene); idx >= 0 && config.Scenes[idx].Name == scene {
			changeSceneTo(config, idx)
			return
		}
		log.Printf("[apps] unknown built-in app scene %q", scene)
		return
	}
	if !launchInstalledApp(external) {
		// A shortcut whose target vanished (uninstalled program, deleted
		// script) would fail again on every press: take the row out of the
		// launcher so what is listed really opens.
		dropAppEntry(config, external)
	}
}

// dropAppEntry removes a launcher row after its program failed to start.
func dropAppEntry(config *Config, path string) {
	if config == nil {
		return
	}
	entries, ok := config.Variables.Custom[appsEntriesVar].([]FileEntry)
	if !ok || len(entries) == 0 {
		return
	}
	kept := make([]FileEntry, 0, len(entries))
	removed := ""
	for _, e := range entries {
		if e.Path == path {
			removed = appGridDisplayName(e.Name)
			continue
		}
		kept = append(kept, e)
	}
	if removed == "" {
		return
	}
	config.Variables.Custom[appsEntriesVar] = kept
	deadAppPaths[path] = true
	if focusedFileIndex >= len(kept) {
		focusedFileIndex = len(kept) - 1
		if focusedFileIndex < 0 {
			focusedFileIndex = 0
		}
	}
	log.Printf("[apps] removed unlaunchable row %q (%s)", removed, path)
	ShowToast("Removed "+removed+" — it no longer opens", ToastKindWarning)
}

// builtinAppActionSupported reports whether an applet action has a handler on
// this device. A panel that needs a helper the build cannot run is left out of
// the launcher instead of opening a screen that only says it is unavailable.
func builtinAppActionSupported(action string) bool {
	switch action {
	case "chiaki":
		return psRemoteSupported()
	}
	return false
}

// launchBuiltinAppAction opens a panel-backed built-in applet. Keeping the
// action names in one switch means a malformed app_entries row cannot run an
// arbitrary function.
func launchBuiltinAppAction(action string) {
	switch action {
	case "chiaki":
		openChiakiPanel()
	default:
		log.Printf("[apps] unknown built-in app action %q", action)
	}
}

// isLaunchableAppPath reports whether path lives below one of the folders the
// Apps list is scanned from. Every launcher row is produced from those folders,
// so a tampered or stale row cannot start an arbitrary executable.
func isLaunchableAppPath(path string) bool {
	for _, root := range append([]string{appsFolder()}, systemAppDirs()...) {
		if isPathWithin(path, root) {
			return true
		}
	}
	return false
}

// launchInstalledApp starts an external program detached from JukaHub. .lnk,
// .exe, .bat and .url files go through the shell so shortcuts resolve their
// targets; shell scripts run with their folder as the working directory. It
// reports whether the program actually started.
func launchInstalledApp(path string) bool {
	// Only an existing regular file with a launchable extension is started.
	// The launcher deliberately runs user-installed programs, but a malformed
	// list entry must never point at an arbitrary command or a directory.
	clean, err := cleanUserPath(path)
	if err != nil {
		return false
	}
	if !isLaunchableAppPath(clean) {
		log.Printf("[SECURITY] [apps] refusing to launch %q: outside the app folders", clean)
		showToast("Refusing to launch an app outside the app folders", ToastError())
		return false
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() {
		showToast("Could not launch "+filepath.Base(path), ToastError())
		return false
	}
	ext := strings.ToLower(filepath.Ext(clean))
	switch ext {
	case ".exe", ".bat", ".cmd", ".lnk", ".url", ".sh":
	default:
		log.Printf("[apps] refusing to launch unsupported file type %q", ext)
		showToast("Unsupported app type "+ext, ToastError())
		return false
	}
	log.Printf("[apps] launching %s", clean)
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows" && (ext == ".lnk" || ext == ".url"):
		// Shortcuts are resolved by the shell handler, not executed directly.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", clean)
	case runtime.GOOS == "windows" && (ext == ".bat" || ext == ".cmd"):
		cmd = exec.Command("cmd", "/c", clean)
	case ext == ".sh":
		cmd = exec.Command("sh", clean)
	default:
		cmd = exec.Command(clean)
	}
	cmd.Dir = filepath.Dir(clean)
	if err := cmd.Start(); err != nil {
		log.Printf("[apps] launch failed for %s: %v", clean, err)
		showToast("Could not launch "+filepath.Base(clean), ToastError())
		return false
	}
	go func() { _ = cmd.Wait() }() // reap the child, keep JukaHub responsive
	return true
}

// openAppsFolder makes sure the portable apps folder exists and opens it in
// the platform file manager so users can drop new apps in.
func openAppsFolder() {
	dir := appsFolder()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[apps] cannot create %s: %v", dir, err)
	}
	// openInExplorer reveals the parent folder of its argument, so pass a
	// placeholder file inside the apps folder.
	openInExplorer(filepath.Join(dir, "apps"))
}
