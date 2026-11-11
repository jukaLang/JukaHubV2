package main

// files_locations.go — where the Files screen can go.
//
// Files used to be fenced into one tree: the configured root (the SD card on
// the handheld, the app folder on Windows). Nothing above it could be reached,
// so a config under /etc, a log under /var or a second drive on Windows was out
// of sight, and the root had no ".." row, so it read as a folder with no way up.
//
// The top of Files is now a Locations list: the card, the app folder, Home,
// Downloads, the filesystem root on Linux and every drive on Windows. Entering a
// location makes it the active root for the containment checks the rest of the
// screen already does (feRoot), B at a location's root returns to the list, and
// a ".." row leads to the real parent of every folder except a filesystem root.
// Going above a location's root moves the active root to the filesystem root
// that holds the folder, so the guards keep working on the tree on screen.
//
// The same file keeps the per-folder memory of the focused row (coming back up
// lands on the folder just left), the hidden-files switch, the friendly words
// for a folder that cannot be read, and the guard that asks twice before a
// destructive action in a system folder.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// filesLocationKind says what a Locations row is: it picks the row's icon and
// the line under its name.
type filesLocationKind int

const (
	filesLocCard filesLocationKind = iota
	filesLocRoot
	filesLocApp
	filesLocHome
	filesLocDownloads
	filesLocSystem
	filesLocDrive
)

// filesLocation is one row of the Locations list.
type filesLocation struct {
	Label  string
	Path   string
	Kind   filesLocationKind
	Detail string
}

// filesLocationsKey is the focus-memory key of the Locations list itself. It
// cannot collide with a folder: no path starts with a NUL byte.
const filesLocationsKey = "\x00locations"

// filesFocusMemoryCap bounds the per-folder cursor memory. A session that walks
// a few hundred folders keeps the most recent ones.
const filesFocusMemoryCap = 256

// filesNavState is the Files screen's navigation beyond fe_path. Like fe_path it
// lives for the session only.
type filesNavState struct {
	// location is the root of the location the user entered, "" for the
	// default root (the configured root, the card on a TrimUI).
	location string
	// atLocations is true while the Locations list is on screen; fe_entries
	// then holds one row per location, in the order of locations.
	atLocations bool
	locations   []filesLocation

	// listedPath, listErr, hiddenCount and systemFolder describe the last
	// listing. The renderer and the footer read them instead of stat-ing the
	// card on every frame.
	listedPath   string
	listedRoot   string
	listErr      error
	hiddenCount  int
	systemFolder bool

	// restore asks the next listing to put the cursor back on the row the user
	// left that folder on (or the folder just come up from).
	restore     bool
	focusMemory map[string]string
	focusOrder  []string
	noteDir     string
	noteIndex   int

	// userAreas are the trees that are the user's own (the card, the app
	// folder, Home, Downloads, temp, removable media). Everything else is a
	// system folder, where a destructive action asks twice.
	userAreas []string
}

var filesNav filesNavState

// filesDefaultRoot is where Files opens: the configured root, the SD card on a
// TrimUI, otherwise the working folder. It is what feRoot answered before
// Locations existed, and it is still the first location in the list.
func filesDefaultRoot(config *Config) string {
	root := ""
	if config != nil {
		root = strings.TrimSpace(config.Variables.FileExplorerRoot)
	}
	if root == "" && IsTrimuiDevice() {
		root = defaultTrimuiSDRoot()
		if root == "" {
			root = "/mnt/SDCARD"
		}
	}
	if root == "" {
		root = "."
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		root = "."
	}
	if absolute, err := filepath.Abs(root); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(root)
}

// filesActiveLocation is the root of the location being browsed, or "" for the
// default root. A location that disappeared (a card pulled, a drive ejected)
// falls back to the default root instead of listing a dead path.
func filesActiveLocation() string {
	loc := filesNav.location
	if loc == "" {
		return ""
	}
	if info, err := os.Stat(loc); err != nil || !info.IsDir() {
		filesNav.location = ""
		return ""
	}
	return loc
}

// filesSamePath compares two paths the way the filesystem does: cleaned, and
// case-insensitively on Windows.
func filesSamePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// filesVolumeRoot is the filesystem root that holds path: "/" on Linux, the
// drive root ("C:\") or the share root on Windows.
func filesVolumeRoot(path string) string {
	clean := filepath.Clean(path)
	if vol := filepath.VolumeName(clean); vol != "" {
		return vol + string(filepath.Separator)
	}
	return string(filepath.Separator)
}

// filesIsFilesystemRoot reports whether path has no parent folder.
func filesIsFilesystemRoot(path string) bool {
	clean := filepath.Clean(path)
	return filepath.Dir(clean) == clean
}

// filesShowHidden reports whether dotfiles and hidden entries are listed.
func filesShowHidden(config *Config) bool {
	if config == nil || config.Variables.Custom == nil {
		return false
	}
	v, ok := config.Variables.Custom["fe_show_hidden"]
	if !ok {
		return false
	}
	return coerceBool(v)
}

// filesSetShowHidden remembers the switch with the user's settings, so the next
// visit lists the same entries, and re-reads the folder on screen.
func filesSetShowHidden(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom["fe_show_hidden"] = on
	saveUserConfigDebounced(userConfigFromVariables(config))
	feListDirectory(config)
}

// filesToggleHidden flips the switch and says what changed.
func filesToggleHidden(config *Config) {
	on := !filesShowHidden(config)
	filesSetShowHidden(config, on)
	PlayNavSound()
	if on {
		showToast("Showing hidden files", ToastInfo())
	} else {
		showToast("Hidden files are hidden", ToastInfo())
	}
}

// --- Locations --------------------------------------------------------------

// filesLooksLikeCard reports whether a root is the handheld's SD card mount.
func filesLooksLikeCard(root string) bool {
	if strings.EqualFold(filepath.Base(root), "SDCARD") {
		return true
	}
	card := strings.TrimSpace(defaultTrimuiSDRoot())
	return card != "" && filesSamePath(root, card)
}

// filesBuildLocations lists the places Files can start from. A path is listed
// once (the app folder is often the configured root on Windows) and only while
// it exists.
func filesBuildLocations(config *Config) []filesLocation {
	var out []filesLocation
	seen := map[string]bool{}
	add := func(loc filesLocation) {
		path := strings.TrimSpace(loc.Path)
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		key := abs
		if runtime.GOOS == "windows" {
			key = strings.ToLower(abs)
		}
		if seen[key] {
			return
		}
		if loc.Kind != filesLocDrive {
			// Drives are listed from the system's own table; asking an empty
			// card reader for its root would only add a delay.
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				return
			}
		}
		seen[key] = true
		loc.Path = abs
		out = append(out, loc)
	}

	root := filesDefaultRoot(config)
	app := exeDir()
	switch {
	case IsTrimuiDevice() && filesLooksLikeCard(root):
		add(filesLocation{Label: "SD card", Path: root, Kind: filesLocCard, Detail: "Games, music, videos and apps"})
	case filesSamePath(root, app):
		add(filesLocation{Label: "JukaHub folder", Path: root, Kind: filesLocApp, Detail: "The app and its data"})
	default:
		label := filepath.Base(root)
		if label == "" || label == "." || label == string(filepath.Separator) {
			label = root
		}
		add(filesLocation{Label: label, Path: root, Kind: filesLocRoot, Detail: "Files home"})
	}
	add(filesLocation{Label: "JukaHub folder", Path: app, Kind: filesLocApp, Detail: "The app and its data"})
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add(filesLocation{Label: "Home", Path: home, Kind: filesLocHome, Detail: "Your user folder"})
		add(filesLocation{Label: "Downloads", Path: filepath.Join(home, "Downloads"), Kind: filesLocDownloads, Detail: "Downloaded files"})
	}
	for _, drive := range filesDrives() {
		add(drive)
	}
	if runtime.GOOS != "windows" {
		for _, mount := range filesRemovableMounts() {
			add(mount)
		}
		add(filesLocation{Label: "System", Path: "/", Kind: filesLocSystem, Detail: "The whole filesystem"})
	}
	return out
}

// filesRemovableMounts lists the volumes a Linux desktop mounts for USB sticks
// and cards (/media/<user>/<label>, /run/media/<user>/<label>). The handheld
// has none; its card is the first location already.
func filesRemovableMounts() []filesLocation {
	user := strings.TrimSpace(os.Getenv("USER"))
	if user == "" {
		return nil
	}
	var out []filesLocation
	for _, base := range []string{filepath.Join("/media", user), filepath.Join("/run/media", user)} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || len(out) >= 8 {
				continue
			}
			out = append(out, filesLocation{
				Label:  e.Name(),
				Path:   filepath.Join(base, e.Name()),
				Kind:   filesLocDrive,
				Detail: "Removable drive",
			})
		}
	}
	return out
}

// filesLocationIndex returns the Locations row holding path's location: the
// row whose root is path itself, else the deepest root that contains it.
func filesLocationIndex(locs []filesLocation, path string) int {
	best, bestLen := -1, -1
	for i, loc := range locs {
		if filesSamePath(loc.Path, path) {
			return i
		}
		if isPathWithin(path, loc.Path) && len(loc.Path) > bestLen {
			best, bestLen = i, len(loc.Path)
		}
	}
	return best
}

// filesLocationAt returns the Locations row under the cursor while the list is
// on screen.
func filesLocationAt(index int) (filesLocation, bool) {
	if !filesNav.atLocations || index < 0 || index >= len(filesNav.locations) {
		return filesLocation{}, false
	}
	return filesNav.locations[index], true
}

// filesLocationOf finds the Locations row a list entry was built from.
func filesLocationOf(entry FileEntry) (filesLocation, bool) {
	if !filesNav.atLocations {
		return filesLocation{}, false
	}
	for _, loc := range filesNav.locations {
		if loc.Path == entry.Path {
			return loc, true
		}
	}
	return filesLocation{}, false
}

// filesListLocations fills fe_entries with the Locations list.
func filesListLocations(config *Config) {
	filesNav.locations = filesBuildLocations(config)
	list := make([]FileEntry, 0, len(filesNav.locations))
	for _, loc := range filesNav.locations {
		list = append(list, FileEntry{Name: loc.Label, Path: loc.Path, IsDir: true})
	}
	config.Variables.Custom["fe_entries"] = list
	filesNav.listedPath = ""
	filesNav.listErr = nil
	filesNav.hiddenCount = 0
	filesNav.systemFolder = false
	filesRefreshLocationSpace(filesNav.locations)
	if filesNav.restore {
		filesNav.restore = false
		if want := filesNav.focusMemory[filesLocationsKey]; want != "" {
			for i, entry := range list {
				if entry.Path == want {
					focusedFileIndex = i
					break
				}
			}
		}
	}
	if focusedFileIndex >= len(list) {
		focusedFileIndex = len(list) - 1
	}
	if focusedFileIndex < 0 && len(list) > 0 {
		focusedFileIndex = 0
	}
}

// filesShowLocations steps up from a location's root to the Locations list,
// with the cursor on the location just left.
func filesShowLocations(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	filesRememberFocus(config)
	from := feCurrentPath(config)
	filesNav.atLocations = true
	filesNav.restore = false
	feSelectionClear()
	focusedFileIndex = 0
	feListDirectory(config)
	if i := filesLocationIndex(filesNav.locations, from); i >= 0 {
		focusedFileIndex = i
	}
	PlayBackSound()
}

// filesEnterLocation opens a Locations row: its root becomes the active root
// and its folder the one on screen.
func filesEnterLocation(config *Config, loc filesLocation) bool {
	if config == nil {
		return false
	}
	info, err := os.Stat(loc.Path)
	if err != nil || !info.IsDir() {
		showToast(loc.Label+" is not available right now", ToastWarn())
		return false
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	filesRememberFocus(config)
	filesNav.location = loc.Path
	filesNav.atLocations = false
	config.Variables.Custom["fe_path"] = loc.Path
	filesNav.restore = true
	focusedFileIndex = 0
	feSelectionClear()
	feListDirectory(config)
	PlayActivateSound()
	return true
}

// filesBackLabel is what B does on the Files screen right now. It reads the last
// listing, so the footer, drawn every frame, costs no disk access.
func filesBackLabel(config *Config) string {
	switch {
	case filesNav.atLocations, filesNav.listedPath == "":
		return "Back"
	case filesSamePath(filesNav.listedPath, filesNav.listedRoot), filesIsFilesystemRoot(filesNav.listedPath):
		return "Locations"
	}
	return "Up"
}

// filesAtLocationRoot reports whether the folder on screen is the root of the
// active location, where B steps up to the Locations list.
func filesAtLocationRoot(config *Config) bool {
	if filesNav.atLocations {
		return false
	}
	path := filesNav.listedPath
	if path == "" {
		path = feCurrentPath(config)
	}
	return filesSamePath(path, feRoot(config)) || filesIsFilesystemRoot(path)
}

// --- Moving between folders -------------------------------------------------

// filesNavigate shows dir. focus names the entry the cursor should land on (the
// folder just come up from); empty means the row the user last left dir on.
func filesNavigate(config *Config, dir, focus string) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	filesRememberFocus(config)
	filesNav.atLocations = false
	config.Variables.Custom["fe_path"] = dir
	if focus != "" {
		filesSetFocusMemory(dir, focus)
	}
	filesNav.restore = true
	focusedFileIndex = 0
	feSelectionClear()
	feListDirectory(config)
}

// filesEnterDir opens a folder row. A folder that cannot be read still opens:
// the listing says why instead of the press doing nothing.
func filesEnterDir(config *Config, entry FileEntry) {
	if entry.Name == ".." {
		filesGoParent(config)
		return
	}
	dir, err := existingDirWithin(feRoot(config), entry.Path)
	if err != nil {
		showToast("Cannot open "+entry.Name+": "+filesFriendlyError(err), ToastError())
		return
	}
	filesNavigate(config, dir, "")
	PlayNavSound()
}

// filesGoParent climbs one folder. Above the active location's root the
// filesystem root holding the parent becomes the active root, so the system
// folders around the card are reachable; a filesystem root has no parent, and
// the Locations list is the way up from it.
func filesGoParent(config *Config) {
	if config == nil {
		return
	}
	if filesNav.atLocations {
		return
	}
	cur := feCurrentPath(config)
	parent := filepath.Dir(cur)
	if parent == cur || filesIsFilesystemRoot(cur) {
		filesShowLocations(config)
		return
	}
	if !isPathWithin(parent, feRoot(config)) {
		filesNav.location = filesVolumeRoot(parent)
	}
	filesNavigate(config, parent, cur)
	PlayBackSound()
}

// --- Focus memory -------------------------------------------------------------

// filesSetFocusMemory remembers which entry of dir the cursor was on.
func filesSetFocusMemory(dir, entryPath string) {
	if dir == "" || entryPath == "" {
		return
	}
	if filesNav.focusMemory == nil {
		filesNav.focusMemory = make(map[string]string, 32)
	}
	if _, known := filesNav.focusMemory[dir]; !known {
		filesNav.focusOrder = append(filesNav.focusOrder, dir)
		if len(filesNav.focusOrder) > filesFocusMemoryCap {
			drop := filesNav.focusOrder[0]
			filesNav.focusOrder = filesNav.focusOrder[1:]
			delete(filesNav.focusMemory, drop)
		}
	}
	filesNav.focusMemory[dir] = entryPath
}

// filesRememberFocus stores the row under the cursor for the listing on screen.
func filesRememberFocus(config *Config) {
	entries := feEntriesOf(config)
	if focusedFileIndex < 0 || focusedFileIndex >= len(entries) {
		return
	}
	key := filesNav.listedPath
	if filesNav.atLocations {
		key = filesLocationsKey
	}
	if key == "" {
		return
	}
	filesSetFocusMemory(key, entries[focusedFileIndex].Path)
}

// filesNoteFocus is called once per drawn frame of the list. It writes the
// memory only when the cursor or the folder changed, so leaving Files for
// another screen (which resets the list cursor) still finds the row again.
func filesNoteFocus(config *Config) {
	dir := filesNav.listedPath
	if filesNav.atLocations {
		dir = filesLocationsKey
	}
	if dir == filesNav.noteDir && focusedFileIndex == filesNav.noteIndex {
		return
	}
	filesNav.noteDir, filesNav.noteIndex = dir, focusedFileIndex
	filesRememberFocus(config)
}

// filesSceneEntered runs when the Files scene comes on screen: the cursor goes
// back to where the user left the folder instead of the first row.
func filesSceneEntered(config *Config) {
	filesNav.restore = true
	feListDirectory(config)
}

// filesRestoreFocus moves the cursor onto the remembered entry of dir, if it is
// still listed.
func filesRestoreFocus(dir string, list []FileEntry) {
	want := filesNav.focusMemory[dir]
	if want == "" {
		return
	}
	for i, entry := range list {
		if entry.Path == want {
			focusedFileIndex = i
			return
		}
	}
}

// --- Reading a folder ---------------------------------------------------------

// filesReadFolder lists path: a ".." row first unless path is a filesystem
// root, then the folders and files, without hidden entries unless they are
// switched on. A folder that cannot be read lists just its ".." row and returns
// the error, so the screen can explain it while the way back stays on screen.
func filesReadFolder(config *Config, path string) ([]FileEntry, int, error) {
	var list []FileEntry
	if parent := filepath.Dir(path); parent != path {
		list = append(list, FileEntry{Name: "..", Path: parent, IsDir: true})
	}
	entries, err := os.ReadDir(path)
	showHidden := filesShowHidden(config)
	hidden := 0
	for _, e := range entries {
		if !showHidden && filesEntryHidden(e) {
			hidden++
			continue
		}
		full := filepath.Join(path, e.Name())
		isDir := e.IsDir()
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			// A link to a folder is browsed like the folder it points at.
			if info, statErr := os.Stat(full); statErr == nil && info.IsDir() {
				isDir = true
			}
		}
		entry := FileEntry{Name: e.Name(), Path: full, IsDir: isDir}
		if !isDir {
			// Size and date are what the rows show on the right. Info() costs
			// one lstat per entry, paid once when the folder is read rather
			// than on every frame.
			if info, infoErr := e.Info(); infoErr == nil {
				entry.Size = info.Size()
				entry.Mod = info.ModTime()
			}
		}
		list = append(list, entry)
	}
	if err != nil && len(entries) > 0 {
		// Partial listings (one unreadable entry in /proc) are still useful.
		err = nil
	}
	return list, hidden, err
}

// filesFriendlyError turns a filesystem error into the sentence a user needs.
func filesFriendlyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "it no longer exists"
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not a directory"):
		return "it is not a folder"
	case strings.Contains(lower, "escapes"):
		return "it is outside this location"
	case strings.Contains(lower, "input/output error"), strings.Contains(lower, "device not ready"):
		return "the drive did not answer"
	}
	if len(msg) > 80 {
		msg = msg[:80] + "…"
	}
	return msg
}

// filesListErrorText is the block the list shows for a folder it could not read.
func filesListErrorText(err error) (string, string) {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "No permission to open this folder", "JukaHub cannot read it. Press B to go back."
	case errors.Is(err, fs.ErrNotExist):
		return "This folder is gone", "It was removed or its drive was ejected. Press B to go back."
	}
	return "This folder cannot be read", filesFriendlyError(err)
}

// --- System folders -----------------------------------------------------------

// filesComputeUserAreas lists the trees that are the user's own. It runs when a
// folder is listed, never per frame: the executable path is a syscall.
func filesComputeUserAreas(config *Config) []string {
	areas := []string{filesDefaultRoot(config), exeDir(), os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		areas = append(areas, home)
	}
	if runtime.GOOS == "windows" {
		// Any drive but the system drive is the user's data.
		system := strings.TrimSpace(os.Getenv("SystemDrive"))
		for _, drive := range filesDrives() {
			if system != "" && strings.EqualFold(filepath.VolumeName(drive.Path), system) {
				continue
			}
			areas = append(areas, drive.Path)
		}
		return areas
	}
	areas = append(areas, "/media", "/run/media", "/mnt/SDCARD")
	return areas
}

// filesIsSystemPath reports whether path is outside every user area: the
// operating system's own folders, where Delete, Rename, Move, Paste and New
// folder ask for a second press.
func filesIsSystemPath(path string, areas []string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, area := range areas {
		if strings.TrimSpace(area) == "" {
			continue
		}
		if isPathWithin(path, area) {
			return false
		}
	}
	return true
}

// filesSystemConfirm guards a destructive action in a system folder: the first
// press arms it and says why, the second (within the arm window) runs it. In
// the user's own folders it runs at once.
func filesSystemConfirm(label string) bool {
	if !filesNav.systemFolder || filesNav.atLocations {
		return true
	}
	key := "sys:" + label
	if feSheetArmed(key) {
		feSheet.armed = ""
		return true
	}
	feSheet.armed = key
	feSheet.armedAt = time.Now()
	PlayNavSound()
	showToast(hintSentence("System folder — press %s again to "+strings.ToLower(label), "A", "Confirm"), ToastWarn())
	return false
}

// --- Free space on the Locations rows ----------------------------------------

type filesFreeValue struct {
	bytes int64
	ok    bool
}

// filesFreeCache holds each location's free space, filled by background
// lookups: statfs on a slow card, or GetDiskFreeSpaceEx on a sleeping drive,
// must not hold up the frame that draws the list.
var filesFreeCache struct {
	mu      sync.Mutex
	vals    map[string]filesFreeValue
	pending map[string]bool
	asked   time.Time
}

// filesRefreshLocationSpace starts the lookups, at most once every 30 seconds.
func filesRefreshLocationSpace(locs []filesLocation) {
	filesFreeCache.mu.Lock()
	if !filesFreeCache.asked.IsZero() && time.Since(filesFreeCache.asked) < 30*time.Second {
		filesFreeCache.mu.Unlock()
		return
	}
	filesFreeCache.asked = time.Now()
	if filesFreeCache.vals == nil {
		filesFreeCache.vals = map[string]filesFreeValue{}
	}
	if filesFreeCache.pending == nil {
		filesFreeCache.pending = map[string]bool{}
	}
	var todo []string
	for _, loc := range locs {
		if filesSkipFreeSpace(loc) || filesFreeCache.pending[loc.Path] {
			continue
		}
		filesFreeCache.pending[loc.Path] = true
		todo = append(todo, loc.Path)
	}
	filesFreeCache.mu.Unlock()
	for _, path := range todo {
		go func(path string) {
			free, err := filesFreeSpace(path)
			filesFreeCache.mu.Lock()
			filesFreeCache.vals[path] = filesFreeValue{bytes: free, ok: err == nil}
			delete(filesFreeCache.pending, path)
			filesFreeCache.mu.Unlock()
		}(path)
	}
}

// filesLocationFree returns a location's free space once a lookup has answered.
func filesLocationFree(path string) (int64, bool) {
	filesFreeCache.mu.Lock()
	defer filesFreeCache.mu.Unlock()
	v, ok := filesFreeCache.vals[path]
	if !ok || !v.ok {
		return 0, false
	}
	return v.bytes, true
}

// filesLocationMeta is the right-hand reading of a Locations row: where it is
// and how much room it has.
func filesLocationMeta(loc filesLocation) string {
	meta := loc.Path
	if loc.Kind == filesLocDrive && runtime.GOOS == "windows" {
		meta = loc.Detail
	}
	if free, ok := filesLocationFree(loc.Path); ok {
		meta += " · " + humanBytes(free) + " free"
	}
	return meta
}

// filesLocationIcon names the glyph a Locations row carries.
func filesLocationIcon(loc filesLocation) string {
	switch loc.Kind {
	case filesLocHome:
		return "home"
	case filesLocDownloads:
		return "download"
	case filesLocApp:
		return "juka"
	case filesLocSystem:
		return "cpu"
	case filesLocCard, filesLocDrive:
		return "disk"
	}
	return "folder"
}
