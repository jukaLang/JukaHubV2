package main

// gamereset.go — "Reset game…" in the library's options sheet, and the trash
// both it and the cover cleanup (artcleanup.go) move files into.
//
// A game collects settings in more places than JukaHub's own record: the
// options sheet's game-options.json entry, RetroArch's per-game files (the
// core override config/<Core>/<rom>.cfg, the core options <rom>.opt and the
// input remap remaps/<Core>/<rom>.rmp) and, for arcade sets, the DIP switches
// and high scores the arcade cores keep next to their saves. Reset game puts
// all of that back, so the next start is the game as it ships.
//
// What stays: the ROM, battery saves (.srm — the game's own progress), the
// playtime ledger and the game page's tags. Save states (and MAME 2003-Plus's
// NVRAM, an arcade board's battery save) only go with the second choice, and
// only the states of this ROM (gmeta_states.go's naming, thumbnails and the
// ".jukahub-prev" copy included).
//
// Every file is matched by the ROM's exact stem in a folder known to belong
// to a core: no wildcard and no "looks similar". Nothing is deleted outright:
// it is moved to <data>/reset-trash/<date>/<system>/<game>/, with a
// restore.txt naming where each file came from, and only the newest few
// trash folders are kept.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// resetTrashKeep is how many reset folders stay, and, counted apart, how many
// cover-cleanup folders.
const resetTrashKeep = 6

// gameResetBusy keeps one reset or cleanup moving files at a time.
var gameResetBusy atomic.Bool

// gameResetStatesDone is raised by a reset that moved save states, so the
// game page re-reads its strip on the render thread (gmetaTick).
var gameResetStatesDone atomic.Bool

// gameResetFile is one file a reset moves, with the folder in the trash it
// goes to ("settings", "states", "arcade").
type gameResetFile struct {
	Path string
	Kind string
}

// resetCoreFolders is the folder RetroArch names after a core ("library_name"
// in the core's info file) for the per-core config, options and remap files.
// coreConfigDisplayName (compat.go) covers the cores Boost writes for; the
// rest of the TrimUI inventory is listed here so a reset finds their files.
func resetCoreFolders(core string) []string {
	core = normalizeCoreName(core)
	if core == "" {
		return nil
	}
	names := map[string]string{
		"fceumm":            "FCEUmm",
		"nestopia":          "Nestopia",
		"snes9x":            "Snes9x",
		"snes9x2005":        "Snes9x 2005",
		"snes9x2005_plus":   "Snes9x 2005 Plus",
		"gambatte":          "Gambatte",
		"mgba":              "mGBA",
		"gpsp":              "gpSP",
		"genesis_plus_gx":   "Genesis Plus GX",
		"picodrive":         "PicoDrive",
		"pcsx_rearmed":      "PCSX-ReARMed",
		"mupen64plus_next":  "Mupen64Plus-Next",
		"flycast":           "Flycast",
		"ppsspp":            "PPSSPP",
		"melonds":           "melonDS",
		"fbneo":             "FinalBurn Neo",
		"mame2003_plus":     "MAME 2003-Plus",
		"stella":            "Stella",
		"prosystem":         "ProSystem",
		"handy":             "Handy",
		"mednafen_wswan":    "Beetle WonderSwan",
		"mednafen_ngp":      "Beetle NeoPop",
		"mednafen_pce_fast": "Beetle PCE Fast",
		"neocd":             "NeoCD",
		"beetle_saturn":     "Beetle Saturn",
		"virtualjaguar":     "Virtual Jaguar",
		"a5200":             "a5200",
	}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for _, have := range out {
			if have == name {
				return
			}
		}
		out = append(out, name)
	}
	add(names[core])
	add(coreConfigDisplayName(core))
	return out
}

// gameResetCores lists the cores whose files may hold this game's settings:
// the one its launcher starts and, for arcade sets, both arcade cores — a set
// name ("sf2") is the same file name under either, and nothing else uses it.
func gameResetCores(game libraryGame) []string {
	var cores []string
	add := func(core string) {
		core = normalizeCoreName(core)
		if core == "" {
			return
		}
		for _, have := range cores {
			if have == core {
				return
			}
		}
		cores = append(cores, core)
	}
	if launcher := resolveGameLauncher(game.System); launcher != "" {
		add(gameCoreFromLauncher(launcher, game.System))
	}
	add(defaultCoreForSystem(game.System))
	if gameResetArcade(game.System) {
		add("fbneo")
		add("mame2003_plus")
	}
	return cores
}

// gameResetArcade reports a system whose ROMs are arcade sets.
func gameResetArcade(system string) bool {
	switch canonicalCompatSystem(system) {
	case "Arcade", "Neo Geo":
		return true
	}
	return false
}

// gameResetDirs is where RetroArch keeps per-game files on this device: its
// config folders (with the remap folder inside), the saves folders and the
// system folder the arcade cores may use instead.
type gameResetDirs struct {
	Config []string
	Remap  []string
	Saves  []string
}

func gameResetFindDirs() gameResetDirs {
	var d gameResetDirs
	add := func(list *[]string, dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || !dirExists(dir) {
			return
		}
		dir = filepath.Clean(dir)
		for _, have := range *list {
			if strings.EqualFold(have, dir) {
				return
			}
		}
		*list = append(*list, dir)
	}
	text, cfgPath, _ := retroArchConfig()
	if text != "" {
		if v, ok := retroArchConfigValue(text, "rgui_config_directory"); ok {
			add(&d.Config, resolveRetroArchSaveDir(v, cfgPath))
		}
		if v, ok := retroArchConfigValue(text, "input_remapping_directory"); ok {
			add(&d.Remap, resolveRetroArchSaveDir(v, cfgPath))
		}
		if v, ok := retroArchConfigValue(text, "system_directory"); ok {
			add(&d.Saves, resolveRetroArchSaveDir(v, cfgPath))
		}
	}
	if cfgPath != "" {
		add(&d.Config, filepath.Join(filepath.Dir(cfgPath), "config"))
	}
	for _, dir := range retroArchConfigDirs() {
		add(&d.Config, dir)
	}
	for _, dir := range d.Config {
		add(&d.Remap, filepath.Join(dir, "remaps"))
	}
	for _, r := range saveManagerRoots() {
		if r.Name == "RetroArch saves" {
			add(&d.Saves, r.Path)
		}
	}
	if sd := saveManagerSDRoot(); sd != "" {
		add(&d.Saves, filepath.Join(sd, "Bios"))
		add(&d.Saves, filepath.Join(sd, "RetroArch", ".retroarch", "system"))
	}
	return d
}

// gameResetStem is the ROM's file name without its extension: the name every
// RetroArch per-game file is made of.
func gameResetStem(romPath string) string {
	base := filepath.Base(strings.TrimSpace(romPath))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" || stem == "." || stem == string(filepath.Separator) {
		return ""
	}
	return stem
}

// gameResetSettingsCandidates lists every path a game's settings may live at.
// Pure: it only joins names, so the matching is the same on every machine;
// gameResetPlan keeps the ones that exist.
func gameResetSettingsCandidates(stem string, cores []string, dirs gameResetDirs) []gameResetFile {
	if stem == "" {
		return nil
	}
	var out []gameResetFile
	for _, core := range cores {
		for _, folder := range resetCoreFolders(core) {
			// config/<Core>/<Core>.cfg is the override for every game on the
			// core, not this game's: a ROM named like the core leaves it be.
			if strings.EqualFold(stem, folder) {
				continue
			}
			for _, dir := range dirs.Config {
				out = append(out,
					gameResetFile{filepath.Join(dir, folder, stem+".cfg"), "settings"},
					gameResetFile{filepath.Join(dir, folder, stem+".opt"), "settings"},
					// Boost's copy of the user's own options (compat.go).
					gameResetFile{filepath.Join(dir, folder, stem+".opt"+compatOptBackupSuffix), "settings"})
			}
			for _, dir := range dirs.Remap {
				out = append(out, gameResetFile{filepath.Join(dir, folder, stem+".rmp"), "settings"})
			}
		}
		// The arcade cores keep DIP switches and high scores in a folder of
		// their own under the saves (or system) folder, also when RetroArch
		// sorts saves into per-core sub-folders.
		var arcade []string
		switch core {
		case "mame2003_plus":
			arcade = []string{
				filepath.Join("mame2003-plus", "cfg", stem+".cfg"),
				filepath.Join("mame2003-plus", "hi", stem+".hi"),
			}
		case "fbneo":
			arcade = []string{
				filepath.Join("fbneo", stem+".hi"),
				filepath.Join("fbneo", "hiscores", stem+".hi"),
			}
		}
		out = append(out, gameResetArcadeFiles(core, arcade, dirs)...)
	}
	return out
}

// gameResetArcadeFiles places arcade files (paths relative to the arcade
// core's own folder) under every saves folder, also inside RetroArch's
// per-core sub-folders.
func gameResetArcadeFiles(core string, rels []string, dirs gameResetDirs) []gameResetFile {
	var out []gameResetFile
	for _, rel := range rels {
		for _, dir := range dirs.Saves {
			out = append(out, gameResetFile{filepath.Join(dir, rel), "arcade"})
			for _, folder := range resetCoreFolders(core) {
				out = append(out, gameResetFile{filepath.Join(dir, folder, rel), "arcade"})
			}
		}
	}
	return out
}

// gameResetNVRAMCandidates is MAME 2003-Plus's NVRAM file for the set: the
// battery-backed RAM a board keeps its progress in, so it is a save and only
// goes with "and delete save states", never with the settings.
func gameResetNVRAMCandidates(stem string, cores []string, dirs gameResetDirs) []gameResetFile {
	if stem == "" {
		return nil
	}
	for _, core := range cores {
		if core == "mame2003_plus" {
			return gameResetArcadeFiles(core, []string{filepath.Join("mame2003-plus", "nvram", stem+".nv")}, dirs)
		}
	}
	return nil
}

// gameResetStateFile reports whether name is one of the stem's save states or
// a file that belongs to one: "<stem>.state", ".stateN", ".state.auto", each
// with an optional ".png" screenshot, and the ".jukahub-prev" copy
// gmetaStageState keeps of a replaced auto state. The stem must match exactly.
func gameResetStateFile(name, stem string) bool {
	if stem == "" || !strings.HasPrefix(name, stem+".state") {
		return false
	}
	rest := name[len(stem)+len(".state"):]
	rest = strings.TrimSuffix(rest, ".png")
	rest = strings.TrimSuffix(rest, ".jukahub-prev")
	switch {
	case rest == "", rest == ".auto":
		return true
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n >= 0 && strconv.Itoa(n) == rest
}

// gameResetStateSubdirs is where under a states folder this game's states may
// be sorted: RetroArch's "sort by core" puts them in <Core>/, "sort by content
// folder" in <rom folder>/, and both together in <Core>/<rom folder>/. Only
// these are entered — another core's folder can hold a different console's
// game with the same file name ("Snes9x/Mario.state" is not NES Mario's).
func gameResetStateSubdirs(romPath string, cores []string) []string {
	romDirName := filepath.Base(filepath.Clean(filepath.Dir(romPath)))
	if romDirName == "." || romDirName == string(filepath.Separator) {
		romDirName = ""
	}
	var out []string
	add := func(rel string) {
		for _, have := range out {
			if strings.EqualFold(have, rel) {
				return
			}
		}
		out = append(out, rel)
	}
	for _, core := range cores {
		for _, folder := range resetCoreFolders(core) {
			add(folder)
			if romDirName != "" {
				add(filepath.Join(folder, romDirName))
			}
		}
	}
	if romDirName != "" {
		add(romDirName)
	}
	return out
}

// gameResetStateFiles finds every file of the game's save states (no cap: a
// reset takes them all): at the top of each states folder and of the ROM's own
// folder, and inside a states folder only in this game's core and content
// sub-folders (gameResetStateSubdirs).
func gameResetStateFiles(romPath string, cores []string) []gameResetFile {
	stem := gameResetStem(romPath)
	if stem == "" {
		return nil
	}
	romDir := filepath.Clean(filepath.Dir(romPath))
	var out []gameResetFile
	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !e.Type().IsRegular() || !gameResetStateFile(e.Name(), stem) {
				continue
			}
			out = append(out, gameResetFile{filepath.Join(dir, e.Name()), "states"})
		}
	}
	subdirs := gameResetStateSubdirs(romPath, cores)
	for _, dir := range gmetaStateDirs(romPath) {
		clean := filepath.Clean(dir)
		scan(clean)
		if strings.EqualFold(clean, romDir) {
			continue
		}
		for _, rel := range subdirs {
			scan(filepath.Join(clean, rel))
		}
	}
	return out
}

// gameResetPlan is the list of files a reset moves: the settings files that
// exist and, with states, the save states. Duplicates (one folder reached
// under two spellings) are dropped; the ROM itself can never be on it.
func gameResetPlan(game libraryGame, cores []string, states bool) []gameResetFile {
	stem := gameResetStem(game.Path)
	if stem == "" {
		return nil
	}
	var files []gameResetFile
	// RetroArch names a content-folder override after the ROM's folder: a ROM
	// named like its console folder ("GBA/GBA.gba") would share that file
	// with every game beside it, so its settings files are left alone.
	_, consoleDir := romSystemAliases[strings.ToLower(filepath.Base(filepath.Dir(game.Path)))]
	dirs := gameResetFindDirs()
	if !consoleDir || !strings.EqualFold(stem, filepath.Base(filepath.Dir(game.Path))) {
		files = gameResetSettingsCandidates(stem, cores, dirs)
	}
	if states {
		files = append(files, gameResetStateFiles(game.Path, cores)...)
		// An arcade board's NVRAM is its save: it goes with the states only.
		files = append(files, gameResetNVRAMCandidates(stem, cores, dirs)...)
	}
	rom := filepath.Clean(game.Path)
	seen := map[string]bool{}
	var out []gameResetFile
	for _, f := range files {
		clean := filepath.Clean(f.Path)
		key := strings.ToLower(clean)
		if seen[key] || strings.EqualFold(clean, rom) {
			continue
		}
		seen[key] = true
		if info, err := os.Lstat(clean); err != nil || !info.Mode().IsRegular() {
			continue
		}
		f.Path = clean
		out = append(out, f)
	}
	return out
}

// resetTrashRoot is <data>/reset-trash, "" with no data folder.
func resetTrashRoot() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "reset-trash")
}

// resetTrashSlug makes a system or game name one safe folder name.
func resetTrashSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		case r < 0x20:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		return "game"
	}
	if runes := []rune(out); len(runes) > 80 {
		out = string(runes[:80])
	}
	return out
}

// resetTrashNewDir makes a fresh dated trash folder: <stamp><suffix>, with a
// counter when two land in the same second.
func resetTrashNewDir(suffix string) (string, error) {
	root := resetTrashRoot()
	if root == "" {
		return "", fmt.Errorf("JukaHub has no data folder")
	}
	stamp := time.Now().Format("2006-01-02_150405")
	dir := filepath.Join(root, stamp+suffix)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(root, stamp+suffix+"-"+strconv.Itoa(i))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// resetTrashMove moves src to dst: a rename on the same card, otherwise a
// copy (through a temporary file) and then the original's removal.
func resetTrashMove(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Two states folders can both hold "<rom>.state1": the second is kept
	// beside the first as "<rom>~2.state1" rather than over it.
	ext := filepath.Ext(dst)
	base := strings.TrimSuffix(dst, ext)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = base + "~" + strconv.Itoa(i) + ext
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := gmetaCopyFile(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// resetTrashMoveAll moves files into dir/<sub>/<kind>/ and writes restore.txt.
// It returns how many moved and how many could not be.
func resetTrashMoveAll(dir string, files []gameResetFile, sub func(gameResetFile) string) (moved, failed int) {
	var manifest strings.Builder
	manifest.WriteString("# Moved by JukaHub on " + time.Now().Format(time.RFC1123) + ".\n")
	manifest.WriteString("# To undo, move a file back to the path on its left.\n")
	for _, f := range files {
		dst := filepath.Join(dir, sub(f), filepath.Base(f.Path))
		if err := resetTrashMove(f.Path, dst); err != nil {
			logWarn("library", "cannot move %s to the trash: %v", f.Path, err)
			failed++
			continue
		}
		moved++
		manifest.WriteString(f.Path + "  ->  " + dst + "\n")
	}
	if moved > 0 {
		_ = os.WriteFile(filepath.Join(dir, "restore.txt"), []byte(manifest.String()), 0o644)
	} else {
		// Nothing arrived: the fresh folder holds at most empty sub-folders.
		_ = os.RemoveAll(dir)
	}
	return moved, failed
}

// resetTrashCovers reports a cover cleanup's trash folder ("<stamp>-covers",
// "<stamp>-covers-2"), which is pruned apart from the resets' folders.
func resetTrashCovers(name string) bool {
	return strings.Contains(name, "-covers")
}

// pruneResetTrash keeps the newest resetTrashKeep reset folders and, apart
// from them, the newest resetTrashKeep cover-cleanup folders, so cleanups never
// push a reset's save states out. keep is the folder just filled: it is never
// removed, whatever its age looks like. Age is the folder's modification time,
// not its date-stamped name — a TrimUI has no clock battery, so a stamp can be
// years off and a fresh folder can sort oldest by name.
func pruneResetTrash(keep string) {
	root := resetTrashRoot()
	if root == "" {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	keepName := ""
	if strings.TrimSpace(keep) != "" {
		keepName = filepath.Base(filepath.Clean(keep))
	}
	type trashDir struct {
		name string
		mod  time.Time
	}
	var resets, covers []trashDir
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keepName {
			continue
		}
		var mod time.Time
		if info, err := e.Info(); err == nil {
			mod = info.ModTime()
		}
		d := trashDir{e.Name(), mod}
		if resetTrashCovers(e.Name()) {
			covers = append(covers, d)
		} else {
			resets = append(resets, d)
		}
	}
	keepCovers := resetTrashCovers(keepName)
	for _, group := range []struct {
		dirs []trashDir
		mine bool
	}{{resets, keepName != "" && !keepCovers}, {covers, keepName != "" && keepCovers}} {
		dirs := group.dirs
		room := resetTrashKeep
		if group.mine {
			room-- // the folder just filled takes one of the places
		}
		if len(dirs) <= room {
			continue
		}
		// Newest first; the name breaks a tie so the order is stable.
		sort.Slice(dirs, func(i, j int) bool {
			if !dirs[i].mod.Equal(dirs[j].mod) {
				return dirs[i].mod.After(dirs[j].mod)
			}
			return dirs[i].name > dirs[j].name
		})
		for _, d := range dirs[room:] {
			if err := os.RemoveAll(filepath.Join(root, d.name)); err != nil {
				logWarn("library", "cannot prune %s: %v", d.name, err)
			}
		}
	}
}

// gameResetFilesWord is "1 file" / "N files".
func gameResetFilesWord(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// runGameReset is the confirmed reset. The options record is dropped on the
// render thread (the sheet shows it); the files move on a goroutine, since a
// copy to another card can take a moment, and the result is a toast.
func runGameReset(game libraryGame, states bool) {
	if strings.TrimSpace(game.Path) == "" || game.System == androidLibrarySystem {
		ShowToast("This game has nothing to reset here", ToastKindInfo)
		return
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("A reset or cleanup is still moving files", ToastKindInfo)
		return
	}
	hadOptions := !gameOptionsFor(game.Path).empty()
	if hadOptions {
		if err := setGameOptions(game.Path, gameOptions{}); err != nil {
			gameResetBusy.Store(false)
			ShowToast("Cannot save game options: "+err.Error(), ToastKindWarning)
			return
		}
	}
	if gameOptsUI.open && gameOptsUI.game.Path == game.Path {
		gameOptsUI.opts = gameOptions{}
	}
	label := game.Label()
	// The launcher is resolved here, on the render thread, the way a launch
	// resolves it; only file work goes to the goroutine.
	cores := gameResetCores(game)
	go func() {
		defer gameResetBusy.Store(false)
		files := gameResetPlan(game, cores, states)
		moved, failed := 0, 0
		movedStates := false
		if len(files) > 0 {
			dir, err := resetTrashNewDir("")
			if err != nil {
				ShowToast("Cannot make the reset folder: "+firstLine(err.Error()), ToastKindError)
				return
			}
			gameDir := filepath.Join(resetTrashSlug(game.System), resetTrashSlug(gameResetStem(game.Path)))
			moved, failed = resetTrashMoveAll(dir, files, func(f gameResetFile) string {
				return filepath.Join(gameDir, f.Kind)
			})
			// A state counts as moved only when it left its folder: a failed
			// move keeps it where the game page already lists it.
			for _, f := range files {
				if f.Kind != "states" {
					continue
				}
				if _, err := os.Lstat(f.Path); os.IsNotExist(err) {
					movedStates = true
					break
				}
			}
			pruneResetTrash(dir)
		}
		if movedStates {
			gameResetStatesDone.Store(true)
		}
		logInfo("library", "reset %s: %d moved, %d failed, states=%v", game.Path, moved, failed, states)
		switch {
		case failed > 0:
			ShowToast(label+" reset · "+gameResetFilesWord(failed)+" could not be removed", ToastKindWarning)
		case moved == 0 && !hadOptions:
			if states {
				ShowToast(label+" had no settings or save states to reset", ToastKindInfo)
			} else {
				ShowToast(label+" already uses the defaults", ToastKindInfo)
			}
		case moved == 0:
			ShowToast("Defaults restored for "+label, ToastKindSuccess)
		default:
			ShowToast(label+" reset · "+gameResetFilesWord(moved)+" moved to the reset trash", ToastKindSuccess)
		}
	}()
}
