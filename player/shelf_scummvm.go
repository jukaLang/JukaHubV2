package main

// shelf_scummvm.go — the ScummVM shelf: each adventure is a folder, listed
// once by its .scummvm file, titled after the folder, with a .scummvm written
// for the folders that have none and a pointer layout that works on a D-pad.
//
// Where the .scummvm file lives differs by firmware, and the shelf takes all
// three without listing a game twice:
//
//   - inside the game's folder (the libretro docs, Batocera, Knulli, RetroBat:
//     Roms/SCUMMVM/Sky/sky.scummvm). The libretro core plays the game in the
//     folder the file is in, so this is the layout the shelf writes;
//   - beside the folder, named like it (muOS, spruce: Roms/SCUMMVM/Sky.scummvm
//     next to Roms/SCUMMVM/Sky/), and CrossMix's importer, which writes those
//     shortcuts straight in Roms/SCUMMVM for data kept in Roms/SCUMMVM/GAMES.
//     A file straight in the console folder is always listed, under its own
//     name, and a folder that has such a shortcut is never listed again or
//     written into.
//
// A folder with no .scummvm is not listed (the core cannot start it) until the
// detector has written one: a goroutine started from the shelf rule itself —
// the rule runs inside every library scan and is the first to see a new
// folder — which names the game from its files (shelf_scummvm_ids.go), writes
// <folder name>.scummvm with the ID with O_EXCL (never over a file), logs one
// line per file and asks for a rescan. It never writes outside a ScummVM
// folder right under one of the library's own Roms roots, nor into CrossMix's
// GAMES folder (its importer's shortcuts are those games), tries each folder
// once per change of the folder, and keeps the folders it could not name for
// the shelf's More row.
//
// The rule runs for every file the scan meets under the console folder, so it
// answers from a per-folder cache: the folder's modification time decides
// whether its listing is read again, and within a scan the cache is trusted
// for a few seconds without a stat (a game folder holds hundreds of files).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// shelfTrust is how long a folder's cached answer is used without a stat.
	shelfTrust = 3 * time.Second
	// shelfSVReadMax bounds the names read from one game folder.
	shelfSVReadMax = 4000
)

// shelfSVFolder is what the rule knows about one game folder.
type shelfSVFolder struct {
	mod     time.Time
	checked time.Time
	// listed is the base name of the folder's .scummvm the shelf lists ("").
	listed string
}

// shelfSVConsole is what the rule knows about a console folder: the lower-case
// stems of the shortcuts straight in it.
type shelfSVConsole struct {
	mod     time.Time
	checked time.Time
	stems   map[string]bool
}

var shelfSV struct {
	mu       sync.Mutex
	folders  map[string]*shelfSVFolder
	consoles map[string]*shelfSVConsole
	// pending are the folders waiting for the detector (game → console).
	pending map[string]string
	// tried is each folder's modification time at its last detection.
	tried   map[string]time.Time
	running bool
	// unknown are the folders the detector could not name, by game folder.
	unknown map[string]bool
}

func init() {
	romShelfRegister("ScummVM", shelfSVListsFile)
	gameLaunchRegisterPrepare(shelfSVPrepare)
	gamePageRegisterExtra(gamePageExtra{
		ID:    "scummvm-unrecognised",
		Order: 80,
		Icon:  "scummvm",
		Row:   shelfSVUnknownRow,
		Open:  shelfSVUnknownOpen,
	})
	ctrlLayoutRegister(ctrlLayout{
		ID:          "scummvm-pointer",
		Name:        "ScummVM point and click",
		Description: "The pointer on the D-pad and the stick, clicks on A and B.",
		Systems:     []string{"ScummVM"},
		DefaultFor:  []string{"ScummVM"},
		// The core's own mapper (scummvm_mapper_*, ScummVM 2.8 and later):
		// every button set, so a mapping changed in RetroArch's menu for
		// another game does not leak into this chart.
		CoreOptions: map[string]string{
			"scummvm_mapper_up":     "RETROKE_UP",
			"scummvm_mapper_down":   "RETROKE_DOWN",
			"scummvm_mapper_left":   "RETROKE_LEFT",
			"scummvm_mapper_right":  "RETROKE_RIGHT",
			"scummvm_mapper_a":      "RETROKE_LEFT_BUTTON",
			"scummvm_mapper_b":      "RETROKE_RIGHT_BUTTON",
			"scummvm_mapper_x":      "RETROK_ESCAPE",
			"scummvm_mapper_y":      "RETROK_PERIOD",
			"scummvm_mapper_l":      "RETROKE_FINE_CONTROL",
			"scummvm_mapper_r":      "RETROK_F5",
			"scummvm_mapper_start":  "RETROKE_SCUMMVM_GUI",
			"scummvm_mapper_select": "RETROKE_VKBD",
		},
		Chart: []ctrlLayoutLine{
			{Button: "D-pad / stick", Action: "Move the pointer"},
			{Button: "A", Action: "Left click"},
			{Button: "B", Action: "Right click"},
			{Button: "X", Action: "Escape · skip a scene"},
			{Button: "Y", Action: "Skip a line of talk"},
			{Button: "L1 (hold)", Action: "Slow, exact pointer"},
			{Button: "R1", Action: "Save and load (most games)"},
			{Button: "Start", Action: "ScummVM menu"},
			{Button: "Select", Action: "On-screen keyboard"},
		},
		Source: "scummvm-shelf",
	})
}

// shelfSVListsFile is the ScummVM shelf rule (romshelf.go).
func shelfSVListsFile(consoleDir, path, ext string) bool {
	gameDir, top := shelfGameDir(consoleDir, path, true)
	if top {
		return ext == ".scummvm"
	}
	if gameDir == "" || filepath.Dir(path) != gameDir {
		return false
	}
	// CrossMix's importer parks the shortcuts of a game's other languages in
	// a "duplicates" folder: that is no game.
	if strings.EqualFold(filepath.Base(gameDir), "duplicates") {
		return false
	}
	if shelfSVCovered(consoleDir, gameDir) {
		return false
	}
	listed := shelfSVListed(consoleDir, gameDir)
	return listed != "" && filepath.Base(path) == listed
}

// shelfSVCovered reports whether a shortcut straight in the console folder is
// named like the game folder: that shortcut is the game.
func shelfSVCovered(consoleDir, gameDir string) bool {
	return shelfSVTopStems(consoleDir)[strings.ToLower(filepath.Base(gameDir))]
}

// shelfSVTopStems are the console folder's shortcuts, cached by the folder's
// modification time. The map is never changed once made.
func shelfSVTopStems(consoleDir string) map[string]bool {
	now := time.Now()
	shelfSV.mu.Lock()
	c := shelfSV.consoles[consoleDir]
	if c != nil && now.Sub(c.checked) < shelfTrust {
		stems := c.stems
		shelfSV.mu.Unlock()
		return stems
	}
	shelfSV.mu.Unlock()
	info, err := os.Stat(consoleDir)
	if err != nil {
		return nil
	}
	shelfSV.mu.Lock()
	if c != nil && c.mod.Equal(info.ModTime()) {
		c.checked = now
		stems := c.stems
		shelfSV.mu.Unlock()
		return stems
	}
	shelfSV.mu.Unlock()
	stems := map[string]bool{}
	entries, _ := os.ReadDir(consoleDir)
	for _, e := range entries {
		if e.IsDir() || skipScanEntryName(e.Name()) || !strings.EqualFold(filepath.Ext(e.Name()), ".scummvm") {
			continue
		}
		stems[strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))] = true
	}
	shelfSV.mu.Lock()
	if shelfSV.consoles == nil {
		shelfSV.consoles = map[string]*shelfSVConsole{}
	}
	shelfSV.consoles[consoleDir] = &shelfSVConsole{mod: info.ModTime(), checked: now, stems: stems}
	shelfSV.mu.Unlock()
	return stems
}

// shelfSVListed is the .scummvm a game folder is listed by ("" none yet, and
// then the folder goes to the detector).
func shelfSVListed(consoleDir, gameDir string) string {
	now := time.Now()
	shelfSV.mu.Lock()
	f := shelfSV.folders[gameDir]
	if f != nil && now.Sub(f.checked) < shelfTrust {
		listed := f.listed
		shelfSV.mu.Unlock()
		return listed
	}
	shelfSV.mu.Unlock()
	info, err := os.Stat(gameDir)
	if err != nil {
		return ""
	}
	shelfSV.mu.Lock()
	if f != nil && f.mod.Equal(info.ModTime()) {
		f.checked = now
		listed := f.listed
		shelfSV.mu.Unlock()
		return listed
	}
	shelfSV.mu.Unlock()
	listed := shelfSVPick(gameDir)
	shelfSV.mu.Lock()
	if shelfSV.folders == nil {
		shelfSV.folders = map[string]*shelfSVFolder{}
	}
	shelfSV.folders[gameDir] = &shelfSVFolder{mod: info.ModTime(), checked: now, listed: listed}
	if listed != "" {
		// The player put a .scummvm in a folder the detector could not name.
		delete(shelfSV.unknown, gameDir)
	}
	shelfSV.mu.Unlock()
	if listed == "" && !shelfSVContainer(consoleDir, gameDir) {
		shelfSVQueue(consoleDir, gameDir, info.ModTime())
	}
	return listed
}

// shelfSVContainer reports a game folder in CrossMix's GAMES folder. Its
// importer makes the shortcuts for those, named after ScummVM's own detection
// rather than the folder, so a file written there would list the game twice:
// the detector leaves them alone.
func shelfSVContainer(consoleDir, gameDir string) bool {
	rel, err := filepath.Rel(consoleDir, gameDir)
	if err != nil {
		return true
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	return strings.EqualFold(first, "games")
}

// shelfSVPick chooses the folder's .scummvm: the one named like the folder,
// else the first by name (a folder may hold one per language or version).
func shelfSVPick(gameDir string) string {
	entries, err := os.ReadDir(gameDir)
	if err != nil {
		return ""
	}
	want := strings.ToLower(filepath.Base(gameDir))
	first := ""
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || skipScanEntryName(name) || !strings.EqualFold(filepath.Ext(name), ".scummvm") {
			continue
		}
		if strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) == want {
			return name
		}
		if first == "" {
			first = name
		}
	}
	return first
}

// ── The detector ────────────────────────────────────────────────────────────

// shelfSVQueue hands a folder with no .scummvm to the detector, once per
// change of the folder; one goroutine at a time.
func shelfSVQueue(consoleDir, gameDir string, mod time.Time) {
	shelfSV.mu.Lock()
	defer shelfSV.mu.Unlock()
	if t, ok := shelfSV.tried[gameDir]; ok && t.Equal(mod) {
		return
	}
	if shelfSV.pending == nil {
		shelfSV.pending = map[string]string{}
	}
	shelfSV.pending[gameDir] = consoleDir
	if shelfSV.running {
		return
	}
	shelfSV.running = true
	go shelfSVDetectRun()
}

// shelfSVDetectRun is the detector's goroutine: it drains the queue, then asks
// the open library to rescan when it wrote anything.
func shelfSVDetectRun() {
	written := 0
	defer func() {
		if r := recover(); r != nil {
			logWarn("library", "ScummVM game detection stopped: %v", r)
			shelfSV.mu.Lock()
			shelfSV.running = false
			shelfSV.pending = nil
			shelfSV.mu.Unlock()
		}
		if written > 0 {
			tbxPost(func() { libraryRescanSoon(libraryRescanTries) })
		}
	}()
	roots := romLibraryRoots()
	for {
		shelfSV.mu.Lock()
		var gameDir, consoleDir string
		for g, c := range shelfSV.pending {
			gameDir, consoleDir = g, c
			break
		}
		if gameDir == "" {
			shelfSV.running = false
			shelfSV.mu.Unlock()
			return
		}
		delete(shelfSV.pending, gameDir)
		shelfSV.mu.Unlock()
		if shelfSVDetectOne(roots, consoleDir, gameDir) {
			written++
		}
	}
}

// shelfSVDetectOne names one folder's game and writes its .scummvm; true when
// it wrote one.
func shelfSVDetectOne(roots []string, consoleDir, gameDir string) bool {
	inRoot := false
	for _, root := range roots {
		if shelfSamePath(filepath.Dir(consoleDir), root) {
			inRoot = true
			break
		}
	}
	if !inRoot {
		return false
	}
	if rel, err := filepath.Rel(consoleDir, gameDir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	if shelfSVContainer(consoleDir, gameDir) {
		return false
	}
	info, err := os.Stat(gameDir)
	if err != nil || !info.IsDir() {
		return false
	}
	shelfSV.mu.Lock()
	if shelfSV.tried == nil {
		shelfSV.tried = map[string]time.Time{}
	}
	shelfSV.tried[gameDir] = info.ModTime()
	shelfSV.mu.Unlock()
	if shelfSVPick(gameDir) != "" || shelfSVCovered(consoleDir, gameDir) {
		return false
	}
	entries, err := os.ReadDir(gameDir)
	if err != nil {
		return false
	}
	names := make(map[string]bool, len(entries))
	for i, e := range entries {
		if i >= shelfSVReadMax {
			break
		}
		if !e.IsDir() && !skipScanEntryName(e.Name()) {
			names[strings.ToLower(e.Name())] = true
		}
	}
	folder := filepath.Base(gameDir)
	id, why := shelfSVDetect(names, folder)
	if id == "" {
		if shelfSVDataLike(names) {
			shelfSV.mu.Lock()
			if shelfSV.unknown == nil {
				shelfSV.unknown = map[string]bool{}
			}
			shelfSV.unknown[gameDir] = true
			shelfSV.mu.Unlock()
			logInfo("library", "ScummVM: %q not recognised — put a .scummvm file with the game's ScummVM ID in it", gameDir)
		}
		return false
	}
	target := filepath.Join(gameDir, folder+".scummvm")
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if !os.IsExist(err) {
			logWarn("library", "ScummVM: cannot write %s: %v", target, err)
		}
		return false
	}
	_, werr := f.WriteString(id)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(target)
		logWarn("library", "ScummVM: cannot write %s: %v", target, werr)
		return false
	}
	shelfSV.mu.Lock()
	delete(shelfSV.unknown, gameDir)
	delete(shelfSV.folders, gameDir)
	shelfSV.mu.Unlock()
	logInfo("library", "ScummVM: wrote %s (%s, from %s)", target, id, why)
	return true
}

// ── The More row for folders it could not name ──────────────────────────────

// shelfSVUnknownNames are the unrecognised folders' names, sorted.
func shelfSVUnknownNames() []string {
	shelfSV.mu.Lock()
	names := make([]string, 0, len(shelfSV.unknown))
	for dir := range shelfSV.unknown {
		names = append(names, filepath.Base(dir))
	}
	shelfSV.mu.Unlock()
	sort.Strings(names)
	return names
}

func shelfSVUnknownRow(game libraryGame) (string, string, bool) {
	if game.System != "ScummVM" {
		return "", "", false
	}
	names := shelfSVUnknownNames()
	if len(names) == 0 {
		return "", "", false
	}
	detail := strings.Join(names, ", ")
	if len(names) > 3 {
		detail = strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
	}
	label := "1 folder not recognised"
	if len(names) > 1 {
		label = fmt.Sprintf("%d folders not recognised", len(names))
	}
	return label, detail, true
}

func shelfSVUnknownOpen(game libraryGame) {
	names := shelfSVUnknownNames()
	if len(names) == 0 {
		return
	}
	ShowToast("Not on the shelf yet: "+names[0]+" — put a .scummvm file with the game's ScummVM ID (scummvm.org/compatibility) in its folder", ToastKindInfo)
}

// ── Launch ──────────────────────────────────────────────────────────────────

// shelfSVReadID is the ID a .scummvm file holds ("" when empty or unreadable).
func shelfSVReadID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	line := string(buf[:n])
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.Trim(strings.TrimPrefix(line, "\ufeff"), " \t\"")
}

// shelfSVPrepare gives a high-resolution game a faster pointer for this launch
// (gamelaunch_hooks.go), unless the player set a speed of their own.
func shelfSVPrepare(launcher string, game libraryGame) {
	if game.System != "ScummVM" || appConfig == nil || !strings.EqualFold(filepath.Ext(game.Path), ".scummvm") {
		return
	}
	if !retroArchLauncher(launcher) || gameCoreFromLauncher(launcher, game.System) != "scummvm" {
		return
	}
	if !shelfSVHiRes[shelfSVBareID(shelfSVReadID(game.Path))] {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	opt := raTuneCoreOptionFile(targets[0], "scummvm", game.Path)
	if opt == "" {
		logInfo("library", "scummvm has no options file yet (start it once from RetroArch); pointer speed left alone")
		return
	}
	raw, err := os.ReadFile(opt)
	if err != nil {
		return
	}
	const key = "scummvm_gamepad_cursor_speed"
	if v, ok := raConfigGet(string(raw), key); ok && !raValueEqual(v, "1.0") {
		return // the player's own speed
	}
	if _, err := raScopeApply(opt, []raScopeValue{{Key: key, Value: "2.0"}}); err != nil {
		logWarn("library", "cannot set the ScummVM pointer speed in %s: %v", opt, err)
	}
}
