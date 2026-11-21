package main

// artwork_nocover.go — Game options → Remove cover / Restore cover.
//
// A game whose cover the user removed shows no picture, and none comes back
// on its own: the box-art fetch (romsMissingArtwork, fetchROMArtworkTracked),
// the gameplay-cover taker (playshot_cover.go) and a port's own picture
// (portEnsureCover) all leave it alone, and romCoverPath answers "" for it
// whatever is on the card. The choice is a marker file,
// <data>/game-art/.nocover/<System>/<stem>.off, named like the cover it stands
// for (romArtworkPath), so it follows the game through Rename file and costs a
// cached folder lookup per game in a library scan (artworkDirNames). The
// cover cleanup never enters dot folders (artcleanup.go).
//
// Removing also moves JukaHub's own copies (the downloaded cover and a picture
// from the game) to the reset trash, and — only when asked — the picture the
// firmware or another front end keeps beside the ROM. Choose cover, Name for
// covers and Restore cover take the marker away again.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// artworkNoCoverDir holds the markers, inside the game-art folder.
	artworkNoCoverDir = ".nocover"
	// artworkNoCoverExt is a marker's extension: not a picture type, so no
	// cover lookup ever mistakes one for a cover.
	artworkNoCoverExt = ".off"
)

// artworkNoCoverPath is the marker of a game's "no cover" choice, "" without
// an artwork folder.
func artworkNoCoverPath(rom scannedROM, root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	p := romArtworkPath(rom, filepath.Join(root, artworkNoCoverDir))
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(p, ".png") + artworkNoCoverExt
}

// artworkNoCover reports a game whose cover the user removed. The folder is
// read through artworkDirNames' cache: a library scan asks once per game.
func artworkNoCover(rom scannedROM, root string) bool {
	p := artworkNoCoverPath(rom, root)
	if p == "" {
		return false
	}
	_, ok := artworkDirNames(filepath.Dir(p))[strings.ToLower(filepath.Base(p))]
	return ok
}

// artworkSetNoCover writes (on) or removes the marker, and drops the folder's
// cached listing so the next lookup sees the change at once.
func artworkSetNoCover(rom scannedROM, root string, on bool) error {
	p := artworkNoCoverPath(rom, root)
	if p == "" {
		return errors.New("the artwork folder is unavailable")
	}
	defer artworkDirForget(filepath.Dir(p))
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o644)
}

// artworkClearNoCover takes a game's marker away when a cover is saved for it
// by hand (Choose cover, Name for covers). It reports whether there was one.
func artworkClearNoCover(rom scannedROM) bool {
	root := libraryArtworkRoot()
	if !artworkNoCover(rom, root) {
		return false
	}
	if err := artworkSetNoCover(rom, root, false); err != nil {
		logWarn("art", "cannot clear the no-cover choice for %s: %v", rom.Path, err)
		return false
	}
	return true
}

// artworkClearNoCoverFor takes the marker away for a cover file a download
// asked for by hand just wrote (<root>/<System>/<stem>.png, RomM's Cover
// download). Any goroutine.
func artworkClearNoCoverFor(root, cover string) {
	rel, err := filepath.Rel(root, cover)
	if err != nil || !filepath.IsLocal(rel) {
		return
	}
	marker := filepath.Join(root, artworkNoCoverDir, strings.TrimSuffix(rel, filepath.Ext(rel))+artworkNoCoverExt)
	if err := os.Remove(marker); err == nil {
		artworkDirForget(filepath.Dir(marker))
	}
}

// artworkDirForget drops folders from artworkDirNames' cache: a file this
// process just wrote, moved or removed shows up (or goes) at the next lookup
// instead of after the cache's half minute.
func artworkDirForget(dirs ...string) {
	artworkDirCache.Lock()
	defer artworkDirCache.Unlock()
	for _, dir := range dirs {
		if dir != "" {
			delete(artworkDirCache.seen, dir)
		}
	}
}

// gameCoverROM is the scanned ROM behind a library game whose cover the
// library resolves (romCoverPath): any console's game and a port, but not an
// Android app, a browser game or a built-in game, whose pictures come from
// elsewhere.
func gameCoverROM(game libraryGame) (scannedROM, bool) {
	switch game.System {
	case androidLibrarySystem, html5LibrarySystem:
		return scannedROM{}, false
	}
	if isBuiltInLibraryGame(game) || strings.TrimSpace(game.Path) == "" {
		return scannedROM{}, false
	}
	return scannedROM{Path: game.Path, System: game.System, Collection: romCollectionForSystem(game.System)}, true
}

// gameCoverCardPicture is the picture beside the ROM the library would show
// for it (romLocalCoverPath) when that is this game's own file on the card:
// named after the ROM's file, or after a folder that is the game's
// ("Game/Game.cue" → "Imgs/Game.png"). Never a converted copy in JukaHub's
// own folder, nor the picture of a folder of many games ("Hacks"); "" then.
func gameCoverCardPicture(rom scannedROM) string {
	local := romLocalCoverPath(rom)
	if local == "" {
		return ""
	}
	dir := filepath.Dir(rom.Path)
	if !isPathWithin(local, dir) && !isPathWithin(local, filepath.Dir(dir)) {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(local), filepath.Ext(local))
	base := filepath.Base(rom.Path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	folder := filepath.Base(dir)
	if strings.EqualFold(name, stem) || strings.EqualFold(name, base) ||
		(strings.EqualFold(name, folder) && strings.EqualFold(folder, stem)) {
		return local
	}
	return ""
}

// gameCoverStemShared reports another library game of the same console with
// the same file stem: it shares JukaHub's cover file (romArtworkPath), so
// that file stays where it is.
func gameCoverStemShared(game libraryGame) bool {
	stem := strings.ToLower(romGameStem(game.Path))
	for _, sys := range gameLib.lib.Systems {
		if sys.Name != game.System {
			continue
		}
		for _, g := range sys.Games {
			if g.Path != game.Path && strings.ToLower(romGameStem(g.Path)) == stem {
				return true
			}
		}
	}
	return false
}

// runGameCoverRemove is the confirmed Remove cover: the marker first (the
// library shows no picture from this frame on), then JukaHub's copies — and
// the card's picture when card is set — go to the reset trash on a goroutine.
func runGameCoverRemove(game libraryGame, card string) {
	rom, ok := gameCoverROM(game)
	if !ok {
		ShowToast("This game's picture comes from its app, not from a cover", ToastKindInfo)
		return
	}
	root := libraryArtworkRoot()
	if err := artworkSetNoCover(rom, root, true); err != nil {
		ShowToast("Cannot remove the cover: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	logInfo("art", "%s: cover removed by hand", rom.Path)
	libraryCoverChanged(rom)
	label := game.Label()

	var files []gameResetFile
	if !gameCoverStemShared(game) {
		// With its screenshot and title screen (artstyle.go).
		for _, p := range append([]string{romArtworkPath(rom, root), playShotCoverPath(rom, root)}, artStyleKindPaths(rom, root)...) {
			if p != "" && regularFile(p) {
				files = append(files, gameResetFile{Path: p, Kind: "jukahub"})
			}
		}
	}
	if card != "" && regularFile(card) {
		files = append(files, gameResetFile{Path: card, Kind: "card"})
	}
	if len(files) == 0 {
		ShowToast("Cover removed for "+label+" · Restore cover brings it back", ToastKindSuccess)
		return
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("Cover hidden for "+label+" · a cleanup is moving files, so its pictures stay on the card", ToastKindInfo)
		return
	}
	go func() {
		defer gameResetBusy.Store(false)
		moved, failed := 0, 0
		dir, err := resetTrashNewDir("-covers")
		if err == nil {
			moved, failed = resetTrashMoveAll(dir, files, func(f gameResetFile) string {
				return filepath.Join(resetTrashSlug(game.System), resetTrashSlug(romGameStem(rom.Path)), f.Kind)
			})
			pruneResetTrash(dir)
		}
		logInfo("art", "remove cover %s: %d moved, %d failed", rom.Path, moved, failed)
		tbxPost(func() {
			for _, f := range files {
				artworkDirForget(filepath.Dir(f.Path))
			}
			libraryCoverChanged(rom)
			switch {
			case err != nil:
				ShowToast("Cover hidden · cannot make the trash folder: "+firstLine(err.Error()), ToastKindWarning)
			case failed > 0:
				ShowToast("Cover removed for "+label+" · "+gameResetFilesWord(failed)+" could not be moved", ToastKindWarning)
			case card != "":
				ShowToast("Cover removed for "+label+", from the card too · the trash keeps it a while", ToastKindSuccess)
			default:
				ShowToast("Cover removed for "+label+" · Restore cover brings one back", ToastKindSuccess)
			}
		})
	}()
}

// runGameCoverRestore is A on Restore cover: the marker goes, and the game
// shows what there is — the card's picture, one from the game — or, with
// none, its cover is looked up once now.
func runGameCoverRestore(game libraryGame) {
	rom, ok := gameCoverROM(game)
	if !ok {
		return
	}
	root := libraryArtworkRoot()
	if err := artworkSetNoCover(rom, root, false); err != nil {
		ShowToast("Cannot restore the cover: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	PlayActivateSound()
	libraryCoverChanged(rom)
	label := game.Label()
	if artworkCoverPresent(romCoverPath(rom, root)) {
		ShowToast("Cover restored for "+label, ToastKindSuccess)
		return
	}
	if game.System == portsLibrarySystem {
		// A port's picture comes from its own folder (ports_library.go).
		go func() {
			portEnsureCover(rom.Path)
			tbxPost(func() {
				libraryCoverChanged(rom)
				if artworkCoverPresent(romCoverPath(rom, root)) {
					ShowToast("Cover restored for "+label, ToastKindSuccess)
					return
				}
				ShowToast("Covers are back on for "+label+" · its folder has no picture", ToastKindInfo)
			})
		}()
		return
	}
	if rom.Collection == "" || rom.Collection == "Plugins" {
		ShowToast("Covers are back on for "+label+" · Choose cover picks one", ToastKindInfo)
		return
	}
	if artworkFetchRunning() || !artChooseBusy.CompareAndSwap(false, true) {
		ShowToast("Covers are back on for "+label+" · the next cover fetch looks for one", ToastKindInfo)
		return
	}
	ShowToast("Covers are back on · looking for one for "+label+"…", ToastKindInfo)
	go func() {
		defer artChooseBusy.Store(false)
		dest := romArtworkPath(rom, root)
		if dest == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), artChooseTimeout)
		defer cancel()
		data, err := fetchROMCoverAllSources(ctx, nil, artworkBaseURL, rom, romGameStem(rom.Path))
		if err == nil {
			err = gmetaWriteCache(dest, data)
		}
		tbxPost(func() {
			switch {
			case err == nil:
				libraryCoverChanged(rom)
				ShowToast("Cover found for "+label, ToastKindSuccess)
			case errors.Is(err, os.ErrNotExist):
				ShowToast("No cover named like "+label+" · Choose cover lists the closest", ToastKindInfo)
			default:
				logWarn("art", "restore cover %s: %v", rom.Path, err)
				ShowToast("The thumbnail server could not be reached · the next fetch tries again", ToastKindWarning)
			}
		})
	}()
}
