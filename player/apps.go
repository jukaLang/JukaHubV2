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

// builtinApplets are JukaHub's own apps. They live at the bottom of the Apps
// list so the launcher is complete even on a machine with no external apps.
var builtinApplets = []struct {
	Name  string
	Scene string
}{
	{"JukaLand", "JukaLand"},
	{"YouTube Shorts", "Shorts"},
	{"Live TV", "LiveTV"},
	{"Podcasts", "Podcasts"},
	{"Tickers", "Tickers"},
	{"Weather", "Weather"},
	{"Unit Converter", "UnitConverter"},
	{"Canvas Sandbox", "CanvasSandbox"},
	{"Text Browser", "TextBrowser"},
	{"IP Stream", "IPStream"},
}

// maxAppEntries caps a single source so a huge Start Menu cannot flood the
// list with thousands of rows.
const maxAppEntries = 300

// builtinAppEntries renders the applet table as launcher rows.
func builtinAppEntries() []FileEntry {
	entries := make([]FileEntry, 0, len(builtinApplets))
	for _, a := range builtinApplets {
		entries = append(entries, FileEntry{
			Name:  a.Name + " (built-in)",
			Path:  builtinScenePrefix + a.Scene,
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
	entries := collectAppEntries(appsFolder(), systemAppDirs())
	config.Variables.Custom[appsEntriesVar] = entries
	log.Printf("[apps] scanned %d app entries", len(entries))
}

// collectAppEntries merges the app sources in priority order: portable apps
// folder first, then system app folders, then built-in applets. Display names
// are de-duplicated case-insensitively so a shortcut and a portable copy of
// the same program do not appear twice.
func collectAppEntries(appsFolder string, systemDirs []string) []FileEntry {
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
	for _, e := range builtinAppEntries() {
		key := strings.ToLower(e.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, e)
	}
	return entries
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
		entries = append(entries, FileEntry{
			Name:  appDisplayName(name),
			Path:  filepath.Join(dir, name),
			IsDir: false,
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
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[apps] bad app config %s: %v", cfgPath, err)
		return FileEntry{}, false
	}
	launch := strings.TrimSpace(cfg.Launch)
	if launch == "" {
		launch = "launch.sh"
	}
	launchPath := filepath.Join(dir, launch)
	if _, err := os.Stat(launchPath); err != nil {
		return FileEntry{}, false
	}
	name := strings.TrimSpace(cfg.Label)
	if name == "" {
		name = filepath.Base(dir)
	}
	return FileEntry{Name: name, Path: launchPath, IsDir: false}, true
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
		})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
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
	scene, external := appEntryTarget(entry)
	if scene != "" {
		if idx := findSceneIndex(config, scene); idx >= 0 && config.Scenes[idx].Name == scene {
			changeSceneTo(config, idx)
			return
		}
		log.Printf("[apps] unknown built-in app scene %q", scene)
		return
	}
	launchInstalledApp(external)
}

// launchInstalledApp starts an external program detached from JukaHub. .lnk,
// .exe, .bat and .url files go through the shell so shortcuts resolve their
// targets; shell scripts run with their folder as the working directory.
func launchInstalledApp(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	log.Printf("[apps] launching %s", path)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "start", "", path)
	} else if strings.HasSuffix(strings.ToLower(path), ".sh") {
		cmd = exec.Command("sh", path)
		cmd.Dir = filepath.Dir(path)
	} else {
		cmd = exec.Command(path)
		cmd.Dir = filepath.Dir(path)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[apps] launch failed for %s: %v", path, err)
		showToast("Could not launch "+filepath.Base(path), ToastError())
		return
	}
	go func() { _ = cmd.Wait() }() // reap the child, keep JukaHub responsive
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
