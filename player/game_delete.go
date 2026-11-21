package main

// game_delete.go — Game options → Delete game….
//
// Deletes a game from the card: its file, the files its sheet lists (a .cue's
// tracks, an .m3u's discs and theirs) when they are inside its folder and no
// other game lists them, and the files beside it of the same stem with one
// extension that are not saves (a CloneCD .img and .sub, a manual, a patch).
// Saves and save states stay: they are the player's progress, and Reset game
// is where they go. JukaHub's own pictures of the game go with it (the cover,
// the picture from the game, the Remove cover marker); the firmware's Imgs
// pictures stay. A folder named after the game that is left empty goes too.
//
// "Move to the trash" puts the files in <data>/reset-trash/<stamp>-deleted
// (a restore.txt names where each came from), where the newest
// resetTrashKeepDeleted deletions stay; "Delete for good" frees the space at
// once. The question opens on Cancel. Afterwards the game leaves the library,
// the recents, Home's console rows, and its game options and cheats; a
// collection keeps its entry under the title it was added with.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// gameDeleteBlocked says why a game cannot be deleted here, "" when it can.
func gameDeleteBlocked(game libraryGame) string {
	switch {
	case isBuiltInLibraryGame(game):
		return game.Title + " is built into JukaHub"
	case game.System == androidLibrarySystem:
		return "An Android game is an app · remove it in Packages"
	case game.System == html5LibrarySystem, game.System == portsLibrarySystem:
		return "This game is a folder · remove it in its own manager or in Files"
	case strings.TrimSpace(game.Path) == "":
		return "This game has no file to delete"
	case strings.EqualFold(filepath.Base(game.Path), romXexDefault):
		return "An Xbox 360 game is a folder · delete the folder in Files"
	}
	info, err := os.Lstat(game.Path)
	switch {
	case err != nil:
		return "The game's file is not on the card any more"
	case info.IsDir():
		return "This game is a folder · delete it in Files"
	case !info.Mode().IsRegular():
		return "The game's file is a link · delete it in Files"
	}
	return ""
}

// gameDeleteSheetParts lists what a sheet names, and what the sheets it names
// name in turn (an .m3u's discs and their tracks).
func gameDeleteSheetParts(sheet string) []string {
	var out []string
	seen := map[string]bool{strings.ToLower(filepath.Clean(sheet)): true}
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		for _, ref := range romSheetReferences(p) {
			key := strings.ToLower(ref)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ref)
			if depth > 0 && gameRenameSheetExt(ref) {
				walk(ref, depth-1)
			}
		}
	}
	walk(sheet, 1)
	return out
}

// gameDeleteCompanionExts are the files of the game's own name beside it that
// Delete game takes: disc images and their parts, patches, manuals and
// pictures. Anything else of that name stays — a save above all, whatever
// extension its emulator gives it.
var gameDeleteCompanionExts = map[string]bool{
	".bin": true, ".img": true, ".sub": true, ".ccd": true, ".sbi": true, ".cue": true, ".gdi": true,
	".m3u": true, ".iso": true, ".chd": true, ".cso": true, ".pbp": true, ".mds": true, ".mdf": true,
	".ecm": true, ".ape": true, ".wav": true, ".raw": true,
	".ips": true, ".bps": true, ".ups": true, ".xdelta": true,
	".txt": true, ".pdf": true, ".nfo": true,
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
}

// gameDeleteJob is what counting a game's files needs from the render
// thread: the game, every other library game's file, and the sheets among
// them in its folder and below — only those can list its parts (a sheet
// listing the game itself would have made it a part, not a library game:
// dropSheetParts).
type gameDeleteJob struct {
	game     libraryGame
	others   []string
	sheets   []string
	shareDir bool // another game in its folder has the same stem
}

// gameDeleteJobFor gathers a job. Render thread.
func gameDeleteJobFor(game libraryGame) gameDeleteJob {
	job := gameDeleteJob{game: game}
	dir := filepath.Clean(filepath.Dir(game.Path))
	stem := gameRenameStem(game.Path)
	for _, sys := range gameLib.lib.Systems {
		for _, g := range sys.Games {
			if g.Path == game.Path || strings.TrimSpace(g.Path) == "" {
				continue
			}
			job.others = append(job.others, g.Path)
			gdir := filepath.Clean(filepath.Dir(g.Path))
			if !isPathWithin(gdir, dir) {
				continue
			}
			if gameRenameSheetExt(g.Path) {
				job.sheets = append(job.sheets, g.Path)
			}
			if strings.EqualFold(gdir, dir) && strings.EqualFold(gameRenameStem(g.Path), stem) {
				job.shareDir = true
			}
		}
	}
	return job
}

// gameDeleteFiles lists the game's own files on the card and their size, the
// ROM first. Off the render thread: it reads a folder and the sheets.
func gameDeleteFiles(job gameDeleteJob) ([]string, int64) {
	rom := filepath.Clean(job.game.Path)
	dir := filepath.Dir(rom)
	base := filepath.Base(rom)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	// What the other games are and list: a part one of them needs stays.
	others := make(map[string]bool, len(job.others))
	for _, p := range job.others {
		others[strings.ToLower(filepath.Clean(p))] = true
	}
	listed := map[string]bool{}
	for _, sheet := range job.sheets {
		for _, ref := range gameDeleteSheetParts(sheet) {
			listed[strings.ToLower(ref)] = true
		}
	}
	shareDir := job.shareDir
	seen := map[string]bool{}
	var files []string
	var total int64
	add := func(p string) {
		p = filepath.Clean(p)
		key := strings.ToLower(p)
		if seen[key] {
			return
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		seen[key] = true
		files = append(files, p)
		total += info.Size()
	}
	add(rom)
	if len(files) == 0 {
		return nil, 0 // the ROM itself is gone: runGameDelete reads files[0] as it
	}
	for _, part := range gameDeleteSheetParts(rom) {
		key := strings.ToLower(part)
		if isPathWithin(part, dir) && !others[key] && !listed[key] {
			add(part)
		}
	}
	// The same stem with one extension of the kinds above; saves and states
	// stay. With another game of that stem in the folder none is surely this
	// one's.
	if !shareDir {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if !e.Type().IsRegular() {
				continue
			}
			if cut, ok := gameCompanionCut(name, base, stem); !ok || cut != len(stem) {
				continue
			}
			if _, state := gmetaStateSlot(strings.TrimSuffix(name, ".png"), stem); state ||
				!gameDeleteCompanionExts[strings.ToLower(filepath.Ext(name))] {
				continue
			}
			p := filepath.Join(dir, name)
			if key := strings.ToLower(p); !others[key] && !listed[key] {
				add(p)
			}
		}
	}
	return files, total
}

// gameDeleteArt lists JukaHub's own pictures of the game (not when another
// game of the console shares their name, gameCoverStemShared).
func gameDeleteArt(game libraryGame) []string {
	rom, ok := gameCoverROM(game)
	if !ok || gameCoverStemShared(game) {
		return nil
	}
	root := libraryArtworkRoot()
	var out []string
	// With its screenshot and title screen (artstyle.go).
	for _, p := range append([]string{romArtworkPath(rom, root), playShotCoverPath(rom, root), artworkNoCoverPath(rom, root)}, artStyleKindPaths(rom, root)...) {
		if p != "" && regularFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// gameDeleteEmptyFolder removes the game's folder when it was named after the
// game, is not a console's own folder and is empty now.
func gameDeleteEmptyFolder(romPath string) {
	dir := filepath.Clean(filepath.Dir(romPath))
	name := filepath.Base(dir)
	if !strings.EqualFold(name, gameRenameStem(romPath)) || filepath.Dir(dir) == dir {
		return
	}
	if _, console := romSystemAliases[strings.ToLower(name)]; console {
		return
	}
	if _, _, tagged := nextuiFolderTag(name); tagged {
		return
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		if err := os.Remove(dir); err != nil {
			logDebug("library", "delete: %s stays: %v", dir, err)
		}
	}
}

// gameDeleteCounting is a count of a game's files in flight (render thread).
var gameDeleteCounting bool

// gameDeleteAsk is A on Delete game…: the game's files are counted off the
// render thread (a disc folder can hold hundreds of sheets), then the
// question opens on the options sheet, on Cancel, if it is still the same
// game's.
func gameDeleteAsk(game libraryGame) {
	if why := gameDeleteBlocked(game); why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	// A scan running now would put the game back when it lands.
	if gameLib.scanning {
		ShowToast("The library is still scanning · delete when it is done", ToastKindInfo)
		return
	}
	if gameDeleteCounting {
		return
	}
	gameDeleteCounting = true
	job := gameDeleteJobFor(game)
	go func() {
		var files []string
		var bytes int64
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "delete count %s: %v", game.Path, r)
				files = nil
			}
			tbxPost(func() {
				gameDeleteCounting = false
				if !gameOptsUI.open || gameOptsUI.game.Path != game.Path || gameOptsUI.choice != gameOptionsChoiceNone {
					return
				}
				if len(files) == 0 {
					ShowToast("The game's file is not on the card any more", ToastKindInfo)
					return
				}
				gameOptsUI.deleteFiles, gameOptsUI.deleteBytes = files, bytes
				openGameOptionsChoice(gameOptionsChoiceDelete)
			})
		}()
		files, bytes = gameDeleteFiles(job)
	}()
}

// runGameDelete is the confirmed Delete game: the files move (or go) on a
// goroutine, then the library forgets the game (gameDeleteFinish).
func runGameDelete(game libraryGame, files []string, forGood bool) {
	if len(files) == 0 {
		ShowToast("The game's file is not on the card any more", ToastKindInfo)
		return
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("A reset or cleanup is still moving files", ToastKindInfo)
		return
	}
	art := gameDeleteArt(game)
	label := game.Label()
	if forGood {
		ShowToast("Deleting "+label+"…", ToastKindInfo)
	} else {
		ShowToast("Moving "+label+" to the trash…", ToastKindInfo)
	}
	go func() {
		done, failed := 0, 0
		var trashErr error
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "delete %s: %v", game.Path, r)
				trashErr = errors.New("the delete stopped unexpectedly")
			}
			_, statErr := os.Lstat(game.Path)
			gone := errors.Is(statErr, os.ErrNotExist)
			tbxPost(func() {
				defer gameResetBusy.Store(false)
				gameDeleteFinish(game, label, gone, done, failed, trashErr, forGood)
			})
		}()
		// The ROM goes first (files[0]): when it cannot, nothing else does —
		// a game left without its tracks would neither play nor be deleted.
		if forGood {
			if err := os.Remove(files[0]); err != nil && !errors.Is(err, os.ErrNotExist) {
				trashErr = err
				return
			}
			done++
			for _, p := range append(append([]string(nil), files[1:]...), art...) {
				if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
					logWarn("library", "delete: cannot remove %s: %v", p, err)
					failed++
					continue
				}
				done++
			}
		} else {
			trash, err := resetTrashNewDir("-deleted")
			if err != nil {
				trashErr = err
				return
			}
			list := make([]gameResetFile, 0, len(files)+len(art))
			for _, p := range files {
				list = append(list, gameResetFile{Path: p, Kind: "game"})
			}
			for _, p := range art {
				list = append(list, gameResetFile{Path: p, Kind: "covers"})
			}
			// Parts keep their folders below the game's ("Disc 1/track01.bin"
			// and "Disc 2/track01.bin" stay apart), so restore.txt reads right.
			gameDir := filepath.Join(resetTrashSlug(game.System), resetTrashSlug(gameRenameStem(game.Path)))
			romDir := filepath.Dir(filepath.Clean(game.Path))
			sub := func(f gameResetFile) string {
				out := filepath.Join(gameDir, f.Kind)
				if rel, err := filepath.Rel(romDir, filepath.Dir(f.Path)); err == nil && f.Kind == "game" && rel != "." && filepath.IsLocal(rel) {
					out = filepath.Join(out, rel)
				}
				return out
			}
			if moved, _ := resetTrashMoveAll(trash, list[:1], sub); moved == 0 {
				trashErr = errors.New("the game's file could not be moved")
				return
			}
			done, failed = resetTrashMoveAll(trash, list[1:], sub)
			done++
			pruneResetTrash(trash)
		}
		gameDeleteEmptyFolder(game.Path)
		logInfo("library", "delete %s: %d done, %d failed, for good=%v", game.Path, done, failed, forGood)
	}()
}

// gameDeleteFinish takes a deleted game out of JukaHub's lists and says how
// it went. A game whose file is still there stays everywhere. Render thread.
func gameDeleteFinish(game libraryGame, label string, gone bool, done, failed int, trashErr error, forGood bool) {
	switch {
	case !gone && trashErr != nil:
		ShowToast("Not deleted · "+firstLine(trashErr.Error()), ToastKindWarning)
		return
	case !gone:
		ShowToast("Not deleted · the game's file could not be removed", ToastKindWarning)
		return
	case trashErr != nil:
		// It stopped part-way after the ROM went: the game is gone all the
		// same, and the toast says something stayed.
		logWarn("library", "delete %s: %v", game.Path, trashErr)
		failed++
	}
	path := game.Path
	_ = setGameOptions(path, gameOptions{})
	cheatsSetRecord(path, cheatsGameRecord{})
	cheatsFlush()
	gameControlsForget(path) // its Buttons and Turbo (racontrols.go)
	recentGamesRepath(path, "", "")
	homeConsoleRecentsRepath(path, "")
	gamePinRepath(path, "") // its Home tile goes (gamepin.go)
	if rom, ok := gameCoverROM(game); ok {
		artworkDirForget(filepath.Dir(romArtworkPath(rom, libraryArtworkRoot())),
			filepath.Dir(playShotPath(rom, libraryArtworkRoot())), filepath.Dir(artworkNoCoverPath(rom, libraryArtworkRoot())))
	}
	if gameOptsUI.open && gameOptsUI.game.Path == path {
		closeGameOptions()
	}
	gameDeleteFromLibrary(path)
	switch {
	case failed > 0:
		ShowToast("Deleted "+label+" · "+gameResetFilesWord(failed)+" could not be removed", ToastKindWarning)
	case forGood:
		ShowToast("Deleted "+label+" · "+gameResetFilesWord(done)+" removed", ToastKindSuccess)
	default:
		ShowToast(label+" moved to the trash · reset-trash keeps the newest deletions a while", ToastKindSuccess)
	}
}

// gameDeleteFromLibrary drops the game from the library on screen, and its
// console when that was its last game. Render thread.
func gameDeleteFromLibrary(path string) {
	systems := make([]librarySystem, 0, len(gameLib.lib.Systems))
	emptied := false
	for _, sys := range gameLib.lib.Systems {
		games := make([]libraryGame, 0, len(sys.Games))
		for _, g := range sys.Games {
			if g.Path != path {
				games = append(games, g)
			}
		}
		if len(games) == 0 && len(sys.Games) > 0 {
			emptied = true
			continue
		}
		sys.Games = games
		systems = append(systems, sys)
	}
	gameLib.lib.Systems = systems
	gameLib.recents = recentGames()
	closeGameLibraryArt()
	gameLibraryEdited() // first: the tag filter's paths are rebuilt for the view
	refreshLibraryView()
	recountLibraryArtwork()
	if emptied {
		// Its console is gone from the grid: back to the grid.
		gameLib.focus = 0
		gameLib.cursor, gameLib.scroll = 0, 0
		if n := len(currentGameSystems()); gameLib.system >= n {
			gameLib.system = max(0, n-1)
		}
	}
}
