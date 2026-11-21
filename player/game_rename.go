package main

// game_rename.go — Game options → Rename file.
//
// Rename title (game_library_view.go) only changes what the library shows.
// Rename file renames the ROM on the card, and with it everything named after
// it, so the game keeps its saves, states, settings and cover:
//
//   - beside the ROM: files of the same stem with one extension ("Game.bin",
//     "Game.srm", "Game.sub"), its save states ("Game.state1.png") and files
//     named after the whole file name ("Game.gba.sav"), but never one that is
//     a library game itself or a part another game's sheet lists. A .cue,
//     .gdi or .m3u among them has the lines that name a renamed file
//     rewritten (gameRenameSheetText);
//   - its pictures: Imgs/<stem> and .media/<stem> beside it, MinUI's
//     .res/<file>.png, and in JukaHub's game-art folder the cover, the picture
//     from the game and the Remove cover marker;
//   - RetroArch's saves and states (the top of its folders and the game's core
//     and content sub-folders, gameResetStateSubdirs), its per-game core
//     override, options and remap (gameResetSettingsCandidates), and the other
//     emulators' save folders that name a save after the whole file
//     (NextUI/MinUI, PPSSPP), plus DraStic's for a DS game;
//   - JukaHub's own records: game options, cheats, recents, collections and
//     favourites, Home's console rows, the RomM link, the resume marker, the
//     name for covers and, when the title the file gives changes, the
//     playtime, the game page and the session history.
//
// Nothing is overwritten: a name already taken anywhere in the plan stops it
// before a file moves, except a picture's — a cover already there under the
// new name is kept, and the old one stays. The files beside the ROM move
// first, and a failure among them puts back what moved; the rest is best
// effort and counted in the toast. A file a game of the same stem elsewhere
// shares (RetroArch keeps one save for USA/Game.sfc and Hacks/Game.sfc) is
// copied, not moved, and a save at the top of RetroArch's folders that a game
// of another console could own is left alone. Arcade sets keep their names
// (the emulator finds the set by it), and an Xbox 360 game named after its
// folder is renamed in Files.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// gameFileNameVKTarget aims the on-screen keyboard at Rename file.
const gameFileNameVKTarget vkTargetKind = 224

// gameRenameMaxName bounds a typed name in bytes: FAT allows 255 characters
// for the whole name, and the extension and a state's suffix come after it.
const gameRenameMaxName = 200

// gameRenameBuffer is the name being typed; read only on the keyboard's
// Rename. gameRenameEdit is the game it is for, kept from the opening.
var (
	gameRenameBuffer string
	gameRenameEdit   libraryGame
)

// gameRenameSaveExts are the battery saves RetroArch and its cores keep in a
// saves folder shared by every console (Beetle Saturn's backup RAM, cartridge
// and SMPC files included).
var gameRenameSaveExts = map[string]bool{
	".srm": true, ".rtc": true, ".sav": true, ".mcd": true, ".eep": true,
	".fla": true, ".mpk": true, ".sra": true, ".ldci": true, ".dsv": true,
	".bkr": true, ".bcr": true, ".smpc": true,
}

// gameRenameExt is the part of a file name a rename keeps: its extension, or
// both of a PICO-8 cart picture's ("celeste.p8.png").
func gameRenameExt(base string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.EqualFold(ext, ".png") && strings.EqualFold(filepath.Ext(stem), ".p8") {
		return filepath.Ext(stem) + ext
	}
	return ext
}

// gameRenameCleanName checks a typed name for the file (without ext, which is
// dropped when typed too) and trims what FAT cannot keep at its end.
func gameRenameCleanName(text, ext string) (string, error) {
	name := strings.TrimSpace(text)
	if ext != "" && len(name) > len(ext) && strings.EqualFold(name[len(name)-len(ext):], ext) {
		name = strings.TrimSpace(name[:len(name)-len(ext)])
	}
	name = strings.TrimRight(name, ". ")
	switch {
	case name == "":
		return "", errors.New("the name is empty")
	case len(name) > gameRenameMaxName:
		return "", errors.New("the name is too long")
	case strings.HasPrefix(name, "."):
		return "", errors.New("a name starting with a dot is hidden")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError || strings.ContainsRune(`/\:*?"<>|`, r) {
			return "", fmt.Errorf("a file name cannot hold %q", r)
		}
	}
	// Names Windows keeps for devices, with or without an extension.
	device := strings.ToUpper(name)
	if i := strings.IndexByte(device, '.'); i >= 0 {
		device = device[:i]
	}
	switch strings.TrimSpace(device) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "", errors.New("that name is reserved")
	}
	return name, nil
}

// gameCompanionCut reports whether name belongs to the game whose file is
// base (stem is base without its extension), and how much of it is the
// game's part: len(base) for the whole file name and more ("Game.gba.sav"),
// len(stem) for the stem with one extension ("Game.srm", "Game.bin") or a
// save state of it ("Game.state1", "Game.state.auto.png"). Letter case is
// ignored, as FAT ignores it. The file itself is not its own companion.
func gameCompanionCut(name, base, stem string) (int, bool) {
	if stem == "" || strings.EqualFold(name, base) {
		return 0, false
	}
	if len(name) > len(base) && strings.EqualFold(name[:len(base)], base) && name[len(base)] == '.' {
		return len(base), true
	}
	if _, state := gmetaStateSlot(strings.TrimSuffix(name, ".png"), stem); state {
		return len(stem), true
	}
	ext := filepath.Ext(name)
	if ext != "" && len(name)-len(ext) == len(stem) && strings.EqualFold(name[:len(stem)], stem) {
		return len(stem), true
	}
	return 0, false
}

// gameRenameSheetText rewrites the lines of a .cue, .gdi or .m3u (ext) that
// name a file in the sheet's own folder found in names (lower-case old name ->
// new name). Everything else on the line, and every other line, stays as it
// was. It reports whether anything changed.
func gameRenameSheetText(text, ext string, names map[string]string) (string, bool) {
	ext = strings.ToLower(ext)
	lines := strings.SplitAfter(text, "\n")
	changed := false
	for i, line := range lines {
		start, end, ok := gameRenameSheetRef(line, ext)
		if !ok {
			continue
		}
		ref := line[start:end]
		if strings.ContainsAny(ref, `/\`) {
			continue // a file in another folder is not renamed
		}
		next, found := names[strings.ToLower(ref)]
		if !found || next == ref {
			continue
		}
		lines[i] = line[:start] + next + line[end:]
		changed = true
	}
	return strings.Join(lines, ""), changed
}

// gameRenameSheetRef finds the file name a sheet line names: [start, end) in
// line. The rules are romSheetReferences': an .m3u's every line that is not a
// comment (up to a "|label"), a .cue's FILE lines (quoted or not), a .gdi's
// track lines (the quoted name, else the fifth field); a leading byte-order
// mark is skipped, and so is "./" (the sheet's own folder).
func gameRenameSheetRef(line, ext string) (int, int, bool) {
	start, end, ok := gameRenameSheetSpan(line, ext)
	if !ok {
		return 0, 0, false
	}
	if ref := line[start:end]; len(ref) > 2 && (strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, `.\`)) {
		start += 2
	}
	return start, end, true
}

// gameRenameSheetSpan is gameRenameSheetRef before "./" is skipped.
func gameRenameSheetSpan(line, ext string) (int, int, bool) {
	body := strings.TrimRight(line, "\r\n")
	// A sheet saved by a Windows editor can start with a byte-order mark.
	from := 0
	if strings.HasPrefix(body, "\ufeff") {
		from = len("\ufeff")
	}
	quoted := func(at int) (int, int, bool) {
		open := strings.IndexByte(body[at:], '"')
		if open < 0 {
			return 0, 0, false
		}
		open += at + 1
		closing := strings.IndexByte(body[open:], '"')
		if closing < 0 {
			return 0, 0, false
		}
		return open, open + closing, open+closing > open
	}
	switch ext {
	case ".m3u":
		rest := body[from:]
		trimmed := strings.TrimSpace(rest)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			return 0, 0, false
		}
		start := from + strings.Index(rest, trimmed)
		end := start + len(trimmed)
		// RetroArch's "disc.cue|Label" gives a disc a label.
		if bar := strings.IndexByte(body[start:end], '|'); bar >= 0 {
			end = start + bar
		}
		end = start + len(strings.TrimRight(body[start:end], " \t"))
		return start, end, end > start
	case ".cue":
		lead := len(body) - len(strings.TrimLeft(body[from:], " \t"))
		if len(body)-lead < 5 || !strings.EqualFold(body[lead:lead+5], "FILE ") {
			return 0, 0, false
		}
		if s, e, ok := quoted(lead + 5); ok {
			return s, e, true
		}
		return gameRenameField(body[from:], 1, from)
	case ".gdi":
		if s, e, ok := quoted(from); ok {
			return s, e, true
		}
		if len(strings.Fields(body[from:])) < 6 {
			return 0, 0, false
		}
		return gameRenameField(body[from:], 4, from)
	}
	return 0, 0, false
}

// gameRenameField is the span of the n-th (from 0) space-separated field of
// s, shifted by offset (where s starts in the line).
func gameRenameField(s string, n, offset int) (int, int, bool) {
	i := 0
	for field := 0; i < len(s); field++ {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		start := i
		for i < len(s) && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		if start == i {
			return 0, 0, false
		}
		if field == n {
			return offset + start, offset + i, true
		}
	}
	return 0, 0, false
}

// gameRenameBlocked says why a game's file cannot be renamed here, "" when
// it can.
func gameRenameBlocked(game libraryGame) string {
	switch {
	case isBuiltInLibraryGame(game):
		return game.Title + " is built into JukaHub"
	case game.System == androidLibrarySystem:
		return "An Android game is an app · Rename title changes what the library shows"
	case game.System == html5LibrarySystem, game.System == portsLibrarySystem:
		return "This game's files belong to its folder · Rename title changes what the library shows"
	case strings.TrimSpace(game.Path) == "":
		return "This game has no file to rename"
	case strings.EqualFold(filepath.Base(game.Path), romXexDefault):
		return "An Xbox 360 game is named after its folder · rename the folder in Files"
	case gameResetArcade(game.System):
		return "Arcade games keep their set name (" + filepath.Base(game.Path) + ") · Rename title changes what the library shows"
	}
	info, err := os.Lstat(game.Path)
	switch {
	case err != nil:
		return "The game's file is not on the card any more"
	case info.IsDir():
		return "This game is a folder · rename it in Files"
	case !info.Mode().IsRegular():
		return "The game's file is a link · rename it in Files"
	}
	return ""
}

// gameRenameRowValue is the Rename file row's value: the file's name.
func gameRenameRowValue(game libraryGame) string {
	if game.System == androidLibrarySystem || isBuiltInLibraryGame(game) || strings.TrimSpace(game.Path) == "" {
		return "—"
	}
	return filepath.Base(game.Path)
}

// vkOpenGameFileName opens the keyboard on the options sheet's game's file
// name, without its extension, which the rename keeps.
func vkOpenGameFileName() {
	if !gameOptsUI.open {
		return
	}
	game := gameOptsUI.game
	if why := gameRenameBlocked(game); why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	base := filepath.Base(game.Path)
	gameRenameEdit = game
	chatInputActive = false
	unitInputActive = false
	vkTarget = gameFileNameVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	gameRenameBuffer = strings.TrimSuffix(base, gameRenameExt(base))
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// gameRenameJob is what the rename needs from the render thread: the game,
// its new file name, its cores (gameResetCores resolves the launcher) and
// what the library says about the other games.
type gameRenameJob struct {
	game    libraryGame
	newBase string
	cores   []string
	root    string
	// others are every other library game's file; sheets the .cue, .gdi and
	// .m3u among them in the ROM's own folder.
	others []string
	sheets []string
	// Another library game has the same stem: on the same console (shareSys),
	// on any (shareAny), in the same folder (shareDir). shareBase: another
	// game's stem is this game's whole file name ("Game.gb.zip" beside
	// "Game.gb"), so "Game.gb.srm" may be that game's save.
	shareSys, shareAny, shareDir, shareBase bool
	// snapRoot is the video snaps folder (snappreview.go); nextuiTag the
	// game's NextUI console tag ("GB"), "" when it is not in a tagged folder.
	snapRoot  string
	nextuiTag string
}

// gameRenameNextUITag is the console tag of the NextUI folder a ROM is in
// ("Roms/Game Boy (GB)/Tetris.gb" → "GB"), looking up a few folders for a
// game kept in a folder of its own; "" off NextUI's layout.
func gameRenameNextUITag(romPath string) string {
	dir := filepath.Dir(filepath.Clean(romPath))
	for i := 0; i < 3; i++ {
		if _, tag, ok := nextuiFolderTag(filepath.Base(dir)); ok && strings.EqualFold(filepath.Base(filepath.Dir(dir)), "Roms") {
			return tag
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// gameRenameStem is a file's name without its extension.
func gameRenameStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// gameRenameSheetExt reports a sheet that names other files.
func gameRenameSheetExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue", ".gdi", ".m3u":
		return true
	}
	return false
}

// gameRenameJobFor gathers a job on the render thread.
func gameRenameJobFor(game libraryGame, newBase string) gameRenameJob {
	job := gameRenameJob{game: game, newBase: newBase, cores: gameResetCores(game), root: libraryArtworkRoot(),
		snapRoot: librarySnapRoot(), nextuiTag: gameRenameNextUITag(game.Path)}
	stem := strings.ToLower(gameRenameStem(game.Path))
	base := strings.ToLower(filepath.Base(game.Path))
	dir := filepath.Clean(filepath.Dir(game.Path))
	for _, sys := range gameLib.lib.Systems {
		for _, g := range sys.Games {
			if g.Path == game.Path || strings.TrimSpace(g.Path) == "" {
				continue
			}
			job.others = append(job.others, g.Path)
			sameDir := strings.EqualFold(filepath.Clean(filepath.Dir(g.Path)), dir)
			if sameDir && gameRenameSheetExt(g.Path) {
				job.sheets = append(job.sheets, g.Path)
			}
			other := strings.ToLower(gameRenameStem(g.Path))
			job.shareBase = job.shareBase || other == base
			if other != stem {
				continue
			}
			job.shareAny = true
			job.shareSys = job.shareSys || g.System == game.System
			job.shareDir = job.shareDir || sameDir
		}
	}
	return job
}

// gameRenameMove is one file of the plan.
type gameRenameMove struct {
	from, to string
	// copied: another game shares the file, so it is copied and stays.
	copied bool
	// near: beside the ROM, where a failure puts every near move back.
	near bool
	// picture: a cover or the Remove cover marker; when the new name already
	// has one, that one is kept and this stays as it is.
	picture bool
}

// gameRenamePlan is every file a rename touches.
type gameRenamePlan struct {
	moves []gameRenameMove
	// sheets are the renamed sheets (new paths) whose lines may name a
	// renamed file; names maps the old names beside the ROM (lower case) to
	// the new.
	sheets []string
	names  map[string]string
}

// gameRenamePlanFor lists the files (off the render thread).
func gameRenamePlanFor(job gameRenameJob) (gameRenamePlan, error) {
	rom := filepath.Clean(job.game.Path)
	dir := filepath.Dir(rom)
	base := filepath.Base(rom)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	newBase := job.newBase
	newStem := strings.TrimSuffix(newBase, filepath.Ext(newBase))
	plan := gameRenamePlan{names: map[string]string{}}
	seen := map[string]bool{}
	add := func(from, to string, copied, near bool) {
		key := strings.ToLower(filepath.Clean(from))
		if seen[key] || from == to {
			return
		}
		seen[key] = true
		plan.moves = append(plan.moves, gameRenameMove{from: from, to: to, copied: copied, near: near})
	}
	addPicture := func(from, to string, copied bool) {
		add(from, to, copied, false)
		if n := len(plan.moves); n > 0 && plan.moves[n-1].from == from {
			plan.moves[n-1].picture = true
		}
	}
	renamed := func(name string, cut int) string {
		if cut == len(base) {
			return newBase + name[cut:]
		}
		return newStem + name[cut:]
	}
	// owned reports a companion this game surely owns: not one named after
	// the whole file when another game's stem is that name (shareBase).
	owned := func(cut int) bool { return cut != len(base) || !job.shareBase }
	add(rom, filepath.Join(dir, newBase), false, true)
	plan.names[strings.ToLower(base)] = newBase

	others := make(map[string]bool, len(job.others))
	for _, p := range job.others {
		others[strings.ToLower(filepath.Clean(p))] = true
	}
	listed := map[string]bool{}
	for _, sheet := range job.sheets {
		for _, ref := range romSheetReferences(sheet) {
			listed[strings.ToLower(ref)] = true
		}
	}

	// Beside the ROM. With another game of the same stem in the folder
	// ("Game.gb" and "Game.gbc"), only what names the whole file is its own.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return plan, err
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		cut, ok := gameCompanionCut(name, base, stem)
		if !ok || !owned(cut) || (job.shareDir && cut != len(base)) {
			continue
		}
		p := filepath.Join(dir, name)
		if others[strings.ToLower(p)] || listed[strings.ToLower(p)] {
			continue
		}
		next := renamed(name, cut)
		add(p, filepath.Join(dir, next), false, true)
		plan.names[strings.ToLower(name)] = next
	}
	for _, m := range plan.moves {
		if m.near && gameRenameSheetExt(m.to) {
			plan.sheets = append(plan.sheets, m.to)
		}
	}

	// Its pictures on the card.
	for _, sub := range []string{"Imgs", ".media"} {
		pics, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range pics {
			name := e.Name()
			ext := filepath.Ext(name)
			if !e.Type().IsRegular() || !artCleanupImage(name) || !strings.EqualFold(strings.TrimSuffix(name, ext), stem) {
				continue
			}
			addPicture(filepath.Join(dir, sub, name), filepath.Join(dir, sub, newStem+ext), job.shareDir)
		}
	}
	res, _ := os.ReadDir(filepath.Join(dir, ".res"))
	for _, e := range res {
		name := e.Name()
		if e.Type().IsRegular() && artCleanupImage(name) && len(name) > len(base) &&
			strings.EqualFold(name[:len(base)], base) && name[len(base)] == '.' && filepath.Ext(name) == name[len(base):] {
			addPicture(filepath.Join(dir, ".res", name), filepath.Join(dir, ".res", newBase+name[len(base):]), false)
		}
	}

	// In JukaHub's game-art folder.
	oldROM := scannedROM{Path: rom, System: job.game.System}
	newROM := scannedROM{Path: filepath.Join(dir, newBase), System: job.game.System}
	for _, pair := range append([][2]string{
		{romArtworkPath(oldROM, job.root), romArtworkPath(newROM, job.root)},
		{playShotPath(oldROM, job.root), playShotPath(newROM, job.root)},
		{artworkNoCoverPath(oldROM, job.root), artworkNoCoverPath(newROM, job.root)},
	}, artStyleKindPairs(oldROM, newROM, job.root)...) { // screenshot, title screen (artstyle.go)
		if pair[0] != "" && pair[1] != "" && regularFile(pair[0]) {
			addPicture(pair[0], pair[1], job.shareSys)
		}
	}
	// Its video snap (snappreview.go), kept under the same name.
	if snap := gameSnapPath(job.game, job.snapRoot); snap != "" {
		addPicture(snap, filepath.Join(filepath.Dir(snap), newStem+filepath.Ext(snap)), job.shareSys)
	}

	// RetroArch's per-game override, options and remap. A ROM named like its
	// console folder shares that folder's override name, so it is left alone
	// (gameResetPlan's rule).
	_, consoleDir := romSystemAliases[strings.ToLower(filepath.Base(dir))]
	if !consoleDir || !strings.EqualFold(stem, filepath.Base(dir)) {
		for _, f := range gameResetSettingsCandidates(stem, job.cores, gameResetFindDirs()) {
			name := filepath.Base(f.Path)
			if !regularFile(f.Path) || len(name) < len(stem) {
				continue
			}
			// One core can play two consoles (mGBA: Game.gb and Game.gba share
			// mGBA/Game.cfg), so any game of the same stem keeps its copy.
			add(f.Path, filepath.Join(filepath.Dir(f.Path), newStem+name[len(stem):]), job.shareAny, false)
		}
	}

	// The save folders.
	scan := func(folder string, accept func(name string, cut int) bool, copied bool) {
		files, err := os.ReadDir(folder)
		if err != nil {
			return
		}
		for _, e := range files {
			name := e.Name()
			if !e.Type().IsRegular() {
				continue
			}
			if cut, ok := gameCompanionCut(name, base, stem); ok && accept(name, cut) {
				add(filepath.Join(folder, name), filepath.Join(folder, renamed(name, cut)), copied, false)
			}
		}
	}
	retroArch := func(name string, cut int) bool {
		if cut == len(base) {
			return owned(cut)
		}
		if _, state := gmetaStateSlot(strings.TrimSuffix(name, ".png"), stem); state {
			return true
		}
		return gameRenameSaveExts[strings.ToLower(filepath.Ext(name))]
	}
	wholeName := func(_ string, cut int) bool { return cut == len(base) && owned(cut) }
	anyCut := func(_ string, cut int) bool { return owned(cut) }
	console := canonicalCompatSystem(job.game.System)
	tagged := func(name string) bool {
		return job.nextuiTag != "" && len(name) > len(job.nextuiTag) &&
			strings.EqualFold(name[:len(job.nextuiTag)], job.nextuiTag) && name[len(job.nextuiTag)] == '-'
	}
	for _, r := range saveManagerRoots() {
		folder := filepath.Clean(r.Path)
		if strings.EqualFold(folder, filepath.Clean(dir)) {
			continue // the ROM's own folder is done above
		}
		switch {
		case r.Name == "RetroArch saves" || r.Name == "RetroArch states":
			// A save at the top is anyone's of that stem, so with another game
			// of the stem it stays; in the game's core or content sub-folder it
			// is copied for the other game instead (one core can play two
			// consoles).
			if !job.shareAny {
				scan(folder, retroArch, false)
			}
			for _, rel := range gameResetStateSubdirs(rom, job.cores) {
				scan(filepath.Join(folder, rel), retroArch, job.shareAny)
			}
		case r.Name == "NextUI saves":
			// Saves/<TAG>/<file>.sav: only the game's own console's folder.
			if job.nextuiTag != "" {
				scan(filepath.Join(folder, job.nextuiTag), wholeName, job.shareSys)
			}
		case strings.HasPrefix(r.Name, "NextUI states"):
			// .userdata/…/<TAG>-<core>/<file>.st0: only the game's console.
			if tagged(filepath.Base(folder)) {
				scan(folder, wholeName, job.shareSys)
			}
		case console == "Nintendo DS" && strings.Contains(r.Name, "DraStic"):
			scan(folder, anyCut, job.shareSys)
		case console == "Dreamcast" && strings.Contains(r.Name, "Flycast"):
			scan(folder, wholeName, job.shareSys)
		}
		// Any other folder (PPSSPP's, kept by game ID) names nothing after
		// the file.
	}
	return plan, nil
}

// gameRenameConflict names a file the plan would overwrite, "" when there is
// none, and drops the pictures whose new name already has one (that one is
// kept). A file that is the source itself under other letter case (FAT, NTFS)
// is no conflict: the rename only changes its letters.
func gameRenameConflict(plan *gameRenamePlan) string {
	targets := make(map[string]bool, len(plan.moves))
	kept := plan.moves[:0]
	for _, m := range plan.moves {
		key := strings.ToLower(m.to)
		taken := targets[key]
		if info, err := os.Lstat(m.to); err == nil && !taken {
			src, serr := os.Lstat(m.from)
			taken = serr != nil || !os.SameFile(src, info)
		}
		if taken {
			if m.picture {
				continue
			}
			return filepath.Base(m.to)
		}
		targets[key] = true
		kept = append(kept, m)
	}
	plan.moves = kept
	return ""
}

// gameRenameMoveFile does one move (or copy). A change of letter case alone
// goes through a temporary name, which a case-insensitive card needs.
func gameRenameMoveFile(m gameRenameMove) error {
	if m.copied {
		return gmetaCopyFile(m.from, m.to)
	}
	if m.from != m.to && strings.EqualFold(m.from, m.to) {
		tmp := m.from + ".jukahub-rename"
		if err := os.Rename(m.from, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, m.to); err != nil {
			_ = os.Rename(tmp, m.from)
			return err
		}
		return nil
	}
	return os.Rename(m.from, m.to)
}

// gameRenameRewriteSheet rewrites one sheet in place through a temporary
// file. It returns the old content for an undo, nil when nothing changed.
func gameRenameRewriteSheet(path string, names map[string]string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > romSheetMaxBytes {
		return nil, err
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	next, changed := gameRenameSheetText(string(old), filepath.Ext(path), names)
	if !changed {
		return nil, nil
	}
	if err := gameRenameWriteInPlace(path, []byte(next), info.Mode().Perm()); err != nil {
		return nil, err
	}
	return old, nil
}

// gameRenameWriteInPlace replaces a file's content: a synced temporary file
// renamed over it, so a power cut leaves the old sheet or the new one.
func gameRenameWriteInPlace(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	tmp := path + ".jukahub-tmp"
	if err := writeFileSynced(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// gameRenameResult is what the goroutine hands back.
type gameRenameResult struct {
	err      error
	conflict string
	moved    int // files renamed or copied, the ROM included
	failed   int // files away from the ROM that kept their old name
}

// gameRenameExecute plans and runs a rename (off the render thread).
func gameRenameExecute(job gameRenameJob) gameRenameResult {
	plan, err := gameRenamePlanFor(job)
	if err != nil {
		return gameRenameResult{err: err}
	}
	if name := gameRenameConflict(&plan); name != "" {
		return gameRenameResult{conflict: name}
	}
	var done []gameRenameMove
	undo := func() {
		for i := len(done) - 1; i >= 0; i-- {
			m := done[i]
			if m.copied {
				_ = os.Remove(m.to)
				continue
			}
			if err := gameRenameMoveFile(gameRenameMove{from: m.to, to: m.from}); err != nil {
				logWarn("library", "rename: cannot put %s back: %v", m.to, err)
			}
		}
	}
	for _, m := range plan.moves {
		if !m.near {
			continue
		}
		if err := gameRenameMoveFile(m); err != nil {
			undo()
			return gameRenameResult{err: err}
		}
		done = append(done, m)
	}
	type sheetCopy struct {
		path string
		data []byte
	}
	var rewritten []sheetCopy
	for _, sheet := range plan.sheets {
		old, err := gameRenameRewriteSheet(sheet, plan.names)
		if err != nil {
			for _, s := range rewritten {
				_ = gameRenameWriteInPlace(s.path, s.data, 0)
			}
			undo()
			return gameRenameResult{err: err}
		}
		if old != nil {
			rewritten = append(rewritten, sheetCopy{sheet, old})
		}
	}
	// Read back: a sheet that still names a file the rename moved (a line
	// written in a way the rewrite does not know) would not start, so
	// everything goes back.
	for _, sheet := range plan.sheets {
		for _, ref := range romSheetReferences(sheet) {
			_, moved := plan.names[strings.ToLower(filepath.Base(ref))]
			if moved && strings.EqualFold(filepath.Dir(ref), filepath.Dir(sheet)) && !regularFile(ref) {
				for _, s := range rewritten {
					_ = gameRenameWriteInPlace(s.path, s.data, 0)
				}
				undo()
				return gameRenameResult{err: fmt.Errorf("%s names %s in a way JukaHub cannot rewrite",
					filepath.Base(sheet), filepath.Base(ref))}
			}
		}
	}
	res := gameRenameResult{moved: len(done)}
	for _, m := range plan.moves {
		if m.near {
			continue
		}
		if err := gameRenameMoveFile(m); err != nil {
			logWarn("library", "rename: %s kept its name: %v", m.from, err)
			res.failed++
			continue
		}
		res.moved++
	}
	return res
}

// gameRenameSubmit is the keyboard's Rename: the files move on a goroutine
// and the records follow on the render thread (gameRenameFinish).
func gameRenameSubmit(text string) {
	game := gameRenameEdit
	gameRenameEdit = libraryGame{}
	if strings.TrimSpace(game.Path) == "" {
		return
	}
	if why := gameRenameBlocked(game); why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	base := filepath.Base(game.Path)
	ext := gameRenameExt(base)
	name, err := gameRenameCleanName(text, ext)
	if err != nil {
		ShowToast("Not renamed: "+err.Error(), ToastKindWarning)
		return
	}
	newBase := name + ext
	if newBase == base {
		ShowToast("The file already has that name", ToastKindInfo)
		return
	}
	// A scan running now would put the old name back when it lands.
	if gameLib.scanning {
		ShowToast("The library is still scanning · rename when it is done", ToastKindInfo)
		return
	}
	if other := gameRenameNameTaken(game, newBase); other != "" {
		ShowToast("Not renamed · "+other+" goes by that name, and RetroArch would mix their saves", ToastKindWarning)
		return
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("A reset or cleanup is still moving files", ToastKindInfo)
		return
	}
	job := gameRenameJobFor(game, newBase)
	ShowToast("Renaming "+base+"…", ToastKindInfo)
	go func() {
		var res gameRenameResult
		// The busy flag is released by the render thread, once the records
		// have followed the files.
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "rename %s: %v", game.Path, r)
				res = gameRenameResult{err: errors.New("the rename stopped unexpectedly")}
			}
			tbxPost(func() {
				defer gameResetBusy.Store(false)
				gameRenameFinish(job, res)
			})
		}()
		res = gameRenameExecute(job)
	}()
}

// gameRenameNameTaken names another library game whose file has the new
// stem, on any console: RetroArch keeps saves by that stem, so the two games
// would share one save. "" when there is none.
func gameRenameNameTaken(game libraryGame, newBase string) string {
	stem := strings.ToLower(gameRenameStem(newBase))
	if stem == strings.ToLower(gameRenameStem(game.Path)) {
		return "" // only the letters' case changes: nothing new is shared
	}
	for _, sys := range gameLib.lib.Systems {
		for _, g := range sys.Games {
			if g.Path != game.Path && strings.TrimSpace(g.Path) != "" && strings.ToLower(gameRenameStem(g.Path)) == stem {
				return filepath.Base(g.Path)
			}
		}
	}
	return ""
}

// gameRenameFinish moves JukaHub's records and the library entry to the new
// file and says how it went. Render thread.
func gameRenameFinish(job gameRenameJob, res gameRenameResult) {
	oldPath := job.game.Path
	dir := filepath.Dir(oldPath)
	newPath := filepath.Join(dir, job.newBase)
	switch {
	case res.conflict != "":
		ShowToast("Not renamed · "+res.conflict+" already exists", ToastKindWarning)
		return
	case res.err != nil:
		logWarn("library", "rename %s to %s: %v", oldPath, job.newBase, res.err)
		if _, err := os.Lstat(oldPath); !errors.Is(err, os.ErrNotExist) || !regularFile(newPath) {
			ShowToast("Not renamed: "+firstLine(res.err.Error()), ToastKindWarning)
			return
		}
		// It stopped part-way after the ROM moved: the records follow it.
		res.failed++
	}
	logInfo("library", "renamed %s to %s (%d files, %d kept their name)", oldPath, job.newBase, res.moved, res.failed)
	// The picture folders are read through a cache: forget what moved.
	sysDir := artCleanupDirName(job.game.System)
	artworkDirForget(filepath.Join(dir, "Imgs"), filepath.Join(dir, ".media"), filepath.Join(dir, ".res"),
		filepath.Join(job.root, sysDir), filepath.Join(job.root, playShotDirName, sysDir),
		filepath.Join(job.root, artworkNoCoverDir, sysDir))
	gameRenameRecords(job.game, newPath)
	updated := gameRenameLibrary(oldPath, newPath)
	if gameOptsUI.open && gameOptsUI.game.Path == oldPath {
		if updated.Path == "" {
			updated = gameOptsUI.game
			updated.Path, updated.Title = newPath, gameDisplayTitle(newPath)
		}
		gameOptsUI.game = updated
		gameOptsUI.opts = gameOptionsFor(newPath)
	}
	msg := "Renamed to " + job.newBase
	if others := res.moved - 1; others > 0 {
		msg += " · " + gameResetFilesWord(others) + " followed"
	}
	if res.failed > 0 {
		ShowToast(msg+" · "+gameResetFilesWord(res.failed)+" kept the old name", ToastKindWarning)
		return
	}
	ShowToast(msg, ToastKindSuccess)
}

// gameRenameLibrary points the library's entries at the new file and keeps
// the highlight on it. It returns the renamed game. Render thread.
func gameRenameLibrary(oldPath, newPath string) libraryGame {
	root := libraryArtworkRoot()
	title := gameDisplayTitle(newPath)
	var updated libraryGame
	for i := range gameLib.lib.Systems {
		games := gameLib.lib.Systems[i].Games
		for j := range games {
			if games[j].Path != oldPath {
				continue
			}
			g := &games[j]
			g.Path, g.Title = newPath, title
			g.Art = romCoverPath(scannedROM{Path: newPath, System: g.System}, root)
			g.HasArt = artworkCoverPresent(g.Art)
			libraryDecorateGame(g)
			updated = *g
		}
	}
	gameLib.recents = recentGames()
	closeGameLibraryArt()
	gameLibraryEdited() // first: the tag filter's paths are rebuilt for the view
	refreshLibraryView()
	recountLibraryArtwork()
	if sys, ok := selectedLibrarySystem(); ok {
		for i, g := range sys.Games {
			if g.Path == newPath {
				gameLib.cursor = i
				break
			}
		}
	}
	return updated
}

// gameLibraryEdited tells the views built from the library that a game
// changed in place (renamed, deleted, its cover changed): Home takes the
// library over again — on its own it only does when the first console or the
// count changes (homeTabsLibrary) — and the tag filter rebuilds its paths.
// Render thread.
func gameLibraryEdited() {
	homeTabsLib.adopted = nil
	gmetaFilter.paths = nil
	invalidateGameLibrarySystemsCache()
	homeTabsTouch()
}

// gameRenameTitleShared reports another library game of the console with the
// same file-derived title ("Kart (USA)" and "Kart (Europe)" are both Kart):
// the playtime, page and history under that title are theirs too, so they
// stay.
func gameRenameTitleShared(game libraryGame) bool {
	for _, sys := range gameLib.lib.Systems {
		for _, g := range sys.Games {
			if g.Path != game.Path && g.System == game.System && g.Title == game.Title {
				return true
			}
		}
	}
	return false
}

// gameRenameRecords moves JukaHub's records of a game to its new file.
// Render thread.
func gameRenameRecords(game libraryGame, newPath string) {
	oldPath := game.Path
	if opts := gameOptionsFor(oldPath); !opts.empty() {
		if err := setGameOptions(newPath, opts); err == nil {
			_ = setGameOptions(oldPath, gameOptions{})
		}
	}
	if rec, ok := cheatsRecordFor(oldPath); ok && !rec.empty() {
		cheatsSetRecord(newPath, rec)
		cheatsSetRecord(oldPath, cheatsGameRecord{})
		cheatsFlush()
	}
	// Its own Buttons and Turbo (racontrols.go).
	gameControlsMove(oldPath, newPath)
	newTitle := gameDisplayTitle(newPath)
	recentGamesRepath(oldPath, newPath, newTitle)
	homeConsoleRecentsRepath(oldPath, newPath)
	homeTabsColRepathGame(oldPath, newPath)
	gamePinRepath(oldPath, newPath) // a game pinned to Home (gamepin.go)
	rommAutoSyncRepath(oldPath, newPath)
	quickResumeRepath(oldPath, newPath, newTitle)
	// Name for covers is kept by the stem's title (artwork_choose.go), which
	// "Kart (USA)" and "Kart (Europe)" share: the old entry stays while
	// another game still goes by it.
	oldStem, newStem := romGameStem(oldPath), romGameStem(newPath)
	oldKey := artworkNormalizeTitle(oldStem)
	if alias, ok := artworkUserAliasFor(oldStem); ok && oldKey != artworkNormalizeTitle(newStem) {
		if _, taken := artworkUserAliasFor(newStem); !taken && artworkSetUserAlias(newStem, alias) {
			shared := false
			for _, sys := range gameLib.lib.Systems {
				for _, g := range sys.Games {
					shared = shared || (g.Path != oldPath && artworkNormalizeTitle(romGameStem(g.Path)) == oldKey)
				}
			}
			if !shared {
				artworkSetUserAlias(oldStem, "")
			}
		}
	}
	oldTitle := strings.TrimSpace(game.Title)
	if oldTitle == "" {
		oldTitle = gameDisplayTitle(oldPath)
	}
	if newTitle != "" && oldTitle != "" && newTitle != oldTitle && !gameRenameTitleShared(game) {
		gameRenameMoveTitle(game.System, oldTitle, newTitle)
	}
}

// gameRenameMoveTitle moves what is kept by console and title: the playtime
// (added to a figure already under the new title), the game page (merged into
// a page already there) and the session history (only when the new title has
// none, so two games' sessions never mix).
func gameRenameMoveTitle(system, oldTitle, newTitle string) {
	oldKey, newKey := playtimeGameKey(system, oldTitle), playtimeGameKey(system, newTitle)
	if oldKey == newKey {
		return
	}
	playtimePeriodsMoveKey(oldKey, newKey) // the days by game (playtime_periods.go)
	if playtime.loaded && playtime.data.Games != nil {
		if secs, ok := playtime.data.Games[oldKey]; ok {
			delete(playtime.data.Games, oldKey)
			playtime.data.Games[newKey] += secs
			playtimePersist(appConfig)
		}
	}

	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	oldMeta, newMeta := gmetaKey(system, oldTitle), gmetaKey(system, newTitle)
	if rec := gmetaStore.games[oldMeta]; rec != nil && oldMeta != newMeta {
		if have := gmetaStore.games[newMeta]; have != nil {
			gmetaMerge(have, rec)
			gmetaNormalize(have)
		} else {
			rec.Title = newTitle
			gmetaStore.games[newMeta] = rec
		}
		delete(gmetaStore.games, oldMeta)
		snapshot, seq := gmetaSnapshotLocked()
		gmetaStore.mu.Unlock()
		gmetaPersist(snapshot, seq)
	} else {
		gmetaStore.mu.Unlock()
	}

	gameHistoryStore.mu.Lock()
	defer gameHistoryStore.mu.Unlock()
	gameHistoryLoadLocked()
	if rec, ok := gameHistoryStore.data.Games[oldKey]; ok {
		if _, taken := gameHistoryStore.data.Games[newKey]; !taken {
			delete(gameHistoryStore.data.Games, oldKey)
			gameHistoryStore.data.Games[newKey] = rec
			gameHistorySaveLocked()
		}
	}
}

// recentGamesRepath points the recents at a renamed file, or with newPath ""
// drops a deleted one.
func recentGamesRepath(oldPath, newPath, newTitle string) {
	gameRecentsMu.Lock()
	defer gameRecentsMu.Unlock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	next := make([]recentGame, 0, len(gameRecents))
	changed := false
	for _, it := range gameRecents {
		if it.Path == oldPath {
			changed = true
			if newPath == "" {
				continue
			}
			it.Path = newPath
			if newTitle != "" {
				it.Title = newTitle
			}
		}
		next = append(next, it)
	}
	if !changed {
		return
	}
	gameRecents = next
	if err := saveGameRecentsFile(gameRecentsFile(), next); err != nil {
		logWarn("library", "cannot save recents: %v", err)
	}
}

// homeConsoleRecentsRepath does the same for the console Home rows' order
// (home_console_recent.go).
func homeConsoleRecentsRepath(oldPath, newPath string) {
	homeConsoleRecents.Lock()
	defer homeConsoleRecents.Unlock()
	homeConsoleRecentLoadLocked()
	next := make([]homeConsoleUse, 0, len(homeConsoleRecents.items))
	changed := false
	for _, item := range homeConsoleRecents.items {
		if item.GamePath != "" && item.GamePath == oldPath {
			changed = true
			if newPath == "" {
				continue
			}
			item.GamePath = newPath
		}
		next = append(next, item)
	}
	if !changed {
		return
	}
	homeConsoleRecents.items = next
	homeConsoleRecents.gen++
	if file := homeConsoleRecentsFile(); file != "" {
		if data, err := json.MarshalIndent(next, "", "  "); err == nil {
			if err := AtomicWrite(file, append(data, '\n'), 0o644); err != nil {
				logWarn("home", "could not save console app history: %v", err)
			}
		}
	}
}

// homeTabsColRepathGame points collections and favourites at a renamed file
// (hometabs_collections.go). A deleted game is not touched there: an entry
// that no longer resolves shows the title it was added with.
func homeTabsColRepathGame(oldPath, newPath string) {
	oldRef, newRef := homeTabsGameRef(oldPath), homeTabsGameRef(newPath)
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	f := &homeTabsColStore.file
	changed := false
	repoint := func(items []homeTabsColItem) bool {
		hit := false
		for i := range items {
			if items[i].Ref == oldRef {
				items[i].Ref = newRef
				hit = true
			}
		}
		return hit
	}
	for i := range f.Collections {
		if repoint(f.Collections[i].Items) {
			f.Collections[i].Items = homeTabsColCleanItems(f.Collections[i].Items, true, homeTabsColItemsMax)
			changed = true
		}
	}
	if repoint(f.Favourites) {
		f.Favourites = homeTabsColCleanItems(f.Favourites, false, homeTabsColFavMax)
		changed = true
	}
	if changed {
		homeTabsColChangedLocked()
	}
}

// rommAutoSyncRepath moves a renamed file's RomM link (romm_autosync.go).
func rommAutoSyncRepath(oldPath, newPath string) {
	oldKey, newKey := rommAutoSyncKey(oldPath), rommAutoSyncKey(newPath)
	if oldKey == newKey {
		return
	}
	rommAutoSyncStore.Lock()
	rommAutoSyncLoadLocked()
	idx := &rommAutoSyncStore.index
	changed := false
	if link, ok := idx.Games[oldKey]; ok && idx.Games != nil {
		delete(idx.Games, oldKey)
		idx.Games[newKey] = link
		changed = true
	}
	if miss, ok := idx.Misses[oldKey]; ok && idx.Misses != nil {
		delete(idx.Misses, oldKey)
		idx.Misses[newKey] = miss
		changed = true
	}
	if changed {
		rommAutoSyncStore.dirty = true
	}
	rommAutoSyncStore.Unlock()
	if changed {
		go rommAutoSyncFlush()
	}
}

// quickResumeRepath points the resume marker (quickresume.go) at a renamed
// file, so the next power-on still starts the game. It is read here rather
// than through quickResumeReadMarker, which queues the marker for removal.
func quickResumeRepath(oldPath, newPath, newTitle string) {
	path := quickResumeMarkerPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m quickResumeMarker
	if json.Unmarshal(data, &m) != nil || m.Path != oldPath {
		return
	}
	m.Path = newPath
	if newTitle != "" {
		m.Title = newTitle
	}
	quickResumeWriteMarker(m)
}
