package main

// gamemanual_add.go — the player's own manuals and guides: a file copied into
// the game's folder in JukaHub's data, or a link to a page on the web.
//
// JukaHub has no file picker a screen can borrow, so a file is chosen where
// files are: "Add a manual or guide" in the game's list (gamemanual_ui.go)
// parks the library and opens Files on the game's folder, and Files' Y menu
// then has "Use as this game's manual" for a PDF, EPUB, text or Markdown file
// or for pictures (several marked pictures are the pages of a scan). The
// files are copied into <data>/game-manuals/<system>/<stem>/ — copied, not
// moved, so a manual kept next to the ROMs for another frontend stays there —
// and Files goes back to the list, which shows them.
//
// The same row is in Files' Y menu without that start ("Use as a game's
// manual…") for a PDF, EPUB, text or Markdown file found anywhere: it asks
// the game's name (seeded with the file's) and picks the library game whose
// title matches best, from the last library scan (boxart_auto.go's shared
// copy) or a fresh one. Pictures are only offered after the list asked for
// them: a photo is not a manual unless the player says so.
//
// A link is typed with the on-screen keyboard; words that are not an address
// become a search, the way the Browser's address bar treats them. Links live
// in game-manuals.json (gamemanual.go).
//
// X twice on something the player added removes it: the copied file (only
// ever inside the game's own data folder) or the link. Documents found next
// to the game or in a frontend's folders are never deleted from here.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// manAddPendingFor is how long the game a list asked Files for stays the
	// one Files' row adds to.
	manAddPendingFor = 15 * time.Minute
	// manCopyMax refuses a file too big to be a manual.
	manCopyMax = 1 << 30
)

// manAdd is the game whose list opened Files to add a document. Main thread
// only.
var manAdd struct {
	game libraryGame
	at   time.Time
}

// manAddPending is the game Files' row adds to, if a list asked for one.
func manAddPending() (libraryGame, bool) {
	if manAdd.game.Path == "" || time.Since(manAdd.at) > manAddPendingFor {
		return libraryGame{}, false
	}
	return manAdd.game, true
}

// manAddFromFiles parks the library and opens Files on the game's folder,
// with the row in its Y menu aimed at this game.
func manAddFromFiles(game libraryGame) {
	if sceneIndexExact(appConfig, "FileExplorer") < 0 {
		PlayErrorSound()
		ShowToast("Files is not available, so a manual cannot be chosen here", ToastKindWarning)
		return
	}
	manAdd.game, manAdd.at = game, time.Now()
	// Files opens on the game's own folder with the game's file focused; the
	// library waits behind it (or closes when Files is already under it).
	chatShowInFiles(appConfig, game.Path)
	manParkScene(sceneIndexExact(appConfig, "FileExplorer"))
	ShowToast("Pick the manual, press "+hintKeyFor("Y", "Actions")+", then Use as this game's manual", ToastKindInfo)
}

// manFilesKind sorts a Files row: a document, a picture, or neither.
func manFilesKind(name string) (doc, picture bool) {
	ext := strings.ToLower(filepath.Ext(name))
	return manReaderExts[ext] || manTextExts[ext], manPictureExts[ext]
}

// manFilesAction is Files' Y-menu row for the marked files (or the focused
// one), when they can be a manual. Called by feActions (files_manager.go).
func manFilesAction(targets []FileEntry) (feAction, bool) {
	docs, pictures := 0, 0
	for _, t := range targets {
		if t.IsDir || t.Name == ".." {
			return feAction{}, false
		}
		doc, picture := manFilesKind(t.Name)
		switch {
		case doc:
			docs++
		case picture:
			pictures++
		default:
			return feAction{}, false
		}
	}
	if docs+pictures == 0 {
		return feAction{}, false
	}
	game, pending := manAddPending()
	if pictures > 0 && !pending {
		return feAction{}, false
	}
	if pending {
		return feAction{Kind: feActGameManual, Label: "Use as this game's manual", Detail: game.Label(), Enabled: true}, true
	}
	return feAction{Kind: feActGameManual, Label: "Use as a game's manual…", Detail: feItemCountLabel(len(targets)), Enabled: true}, true
}

// manFilesActivate is the row chosen (feSheetActivate).
func manFilesActivate(config *Config, targets []FileEntry) {
	var paths []string
	for _, t := range targets {
		if p, err := resolveExplorerEntry(config, t.Path); err == nil {
			paths = append(paths, p)
		}
	}
	feSheetForget()
	if len(paths) == 0 {
		PlayErrorSound()
		ShowToast("That file cannot be read", ToastKindWarning)
		return
	}
	if game, ok := manAddPending(); ok {
		manAdd.game = libraryGame{}
		PlayActivateSound()
		manCopyFiles(game, paths)
		// Back to the game's list the add started from.
		if manAway.active && manAway.kind == manAwayScene && feSceneActive(config) {
			goBackScene(config)
		}
		return
	}
	seed := gameDisplayTitle(paths[0])
	if key, tagged := manDocKey(seed); tagged && key != "" {
		// "Super Metroid (Manual)" asks for "Super Metroid".
		seed = strings.TrimSpace(stripArtworkTagGroups(seed))
	}
	roots := append([]string(nil), romLibraryRoots()...)
	tbxOpenPrompt("Which game is it for?", "Name of the game", seed, func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		ShowToast("Looking for "+text+" in your games…", ToastKindInfo)
		go manFindGameAndCopy(text, roots, paths)
	})
}

// manFindGameAndCopy finds the library game named text and adds paths to it.
// Its own goroutine.
func manFindGameAndCopy(text string, roots, paths []string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("manual", "finding the game for a manual failed: %v", r)
		}
	}()
	roms, ok := romScanCached(roots, boxArtLibrarySignature(roots), time.Now())
	if !ok {
		roms = scanROMLibrary(roots).Games
	}
	game, found := manBestGame(text, roms)
	tbxPost(func() {
		if !found {
			PlayErrorSound()
			ShowToast("No game called “"+text+"” in your library", ToastKindWarning)
			return
		}
		manCopyFiles(game, paths)
	})
}

// manBestGame is the game whose name matches query best: the same name, else
// the closest name that contains it or that it contains. Pure.
func manBestGame(query string, roms []scannedROM) (libraryGame, bool) {
	want := manCompact(query)
	if want == "" {
		return libraryGame{}, false
	}
	best, bestGap := -1, 1<<30
	for i, r := range roms {
		for _, name := range []string{romGameStem(r.Path), gameDisplayTitle(r.Path)} {
			key := manCompact(name)
			if key == "" {
				continue
			}
			gap := -1
			switch {
			case key == want:
				gap = 0
			case len(want) >= 4 && strings.Contains(key, want):
				gap = len(key) - len(want)
			case len(key) >= 4 && strings.Contains(want, key):
				gap = len(want) - len(key)
			}
			if gap >= 0 && gap < bestGap {
				best, bestGap = i, gap
			}
		}
	}
	if best < 0 {
		return libraryGame{}, false
	}
	r := roms[best]
	return libraryGame{Path: r.Path, System: r.System, Title: gameDisplayTitle(r.Path)}, true
}

// manCopyFiles copies paths into game's own folder on a goroutine, then says
// how it went and lists the game's documents again.
func manCopyFiles(game libraryGame, paths []string) {
	dir := manGameDir(game)
	if dir == "" {
		PlayErrorSound()
		ShowToast("JukaHub has no data folder to keep the manual in", ToastKindError)
		return
	}
	name := game.Label()
	ShowToast("Adding to "+name+"…", ToastKindInfo)
	go func() {
		added, failed := 0, 0
		var lastErr error
		defer func() {
			if r := recover(); r != nil {
				logWarn("manual", "adding a manual failed: %v", r)
				lastErr = fmt.Errorf("%v", r)
			}
			manInvalidate(game.Path)
			tbxPost(func() {
				switch {
				case added == 0:
					PlayErrorSound()
					msg := "Could not add the manual"
					if lastErr != nil {
						msg += ": " + filesFriendlyError(lastErr)
					}
					ShowToast(msg, ToastKindError)
				case lastErr != nil:
					ShowToast(fmt.Sprintf("Added %d to %s; %d could not be copied", added, name, max(failed, 1)), ToastKindWarning)
				default:
					PlaySuccessSound()
					ShowToast("Added to "+name+" · More → Manual & guide", ToastKindSuccess)
				}
				manRefresh(game, true, func([]manDoc) { manDocsLanded(game.Path) })
			})
		}()
		for _, src := range paths {
			if _, err := manCopyInto(dir, src); err != nil {
				logWarn("manual", "could not copy %s for %s: %v", filepath.Base(src), name, err)
				lastErr = err
				failed++
				continue
			}
			added++
		}
		if added > 0 {
			logInfo("manual", "%d document(s) added for %s in %s", added, name, dir)
		}
	}()
}

// manCopyInto copies src into dir under its own name (a different file of
// the same name gets " (2)"), through a temporary file renamed into place.
// A file already there with the same size counts as added.
func manCopyInto(dir, src string) (string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	if info.Size() > manCopyMax {
		return "", errors.New("the file is too big for a manual")
	}
	if isPathWithin(src, dir) {
		return src, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := manSafeName(filepath.Base(src))
	if srcExt := filepath.Ext(src); srcExt != "" && !strings.EqualFold(filepath.Ext(name), srcExt) {
		// A name that was only its extension (".pdf") lost it to the trim:
		// without it the copy would never be listed.
		name += strings.ToLower(srcExt)
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	dst := filepath.Join(dir, name)
	for n := 2; ; n++ {
		have, err := os.Stat(dst)
		if err != nil {
			break
		}
		if have.Size() == info.Size() {
			return dst, nil
		}
		if n > 99 {
			return "", errors.New("too many files of that name")
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	// A temporary file of its own ("….part" is never listed): two adds of the
	// same name at once must not write into one file.
	out, err := os.CreateTemp(dir, ".adding-*.part")
	if err != nil {
		return "", err
	}
	tmp := out.Name()
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

// manAddLinkPrompt asks for a guide's address and keeps it for game.
func manAddLinkPrompt(game libraryGame) {
	tbxOpenPrompt("Guide link", "https://… or words to search for", "", func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		address, err := normalizeTextBrowserURL(text)
		if err != nil {
			PlayErrorSound()
			ShowToast("That link cannot be opened: "+err.Error(), ToastKindWarning)
			return
		}
		title := ""
		if !strings.Contains(text, "://") && !textBrowserLooksLikeAddress(text) {
			title = "Search: " + text
		}
		go func() {
			err := manStoreUpdate(game.Path, func(g *manStoreGame) {
				for _, l := range g.Links {
					if l.URL == address {
						return
					}
				}
				if len(g.Links) >= manMaxLinks {
					g.Links = g.Links[1:]
				}
				g.Links = append(g.Links, manLink{Title: title, URL: address})
			})
			manInvalidate(game.Path)
			tbxPost(func() {
				if err != nil {
					PlayErrorSound()
					ShowToast("Could not keep the link: "+filesFriendlyError(err), ToastKindError)
					return
				}
				PlaySuccessSound()
				ShowToast("Guide link added", ToastKindSuccess)
				manRefresh(game, true, func([]manDoc) { manDocsLanded(game.Path) })
			})
		}()
	})
}

// manRemoveFocused is X on the list: on something the player added, the
// first press arms and the second removes it.
func manRemoveFocused() {
	ui := &manUI
	d, ok := manFocusedDoc()
	if !ok || !d.User {
		return
	}
	if ui.armed != ui.cursor || time.Since(ui.armedAt) > feArmWindow {
		ui.armed, ui.armedAt = ui.cursor, time.Now()
		PlayNavSound()
		return
	}
	ui.armed = -1
	game := ui.game
	dir := manGameDir(game)
	PlayActivateSound()
	go func() {
		var err error
		switch d.Kind {
		case manKindLink:
			err = manStoreUpdate(game.Path, func(g *manStoreGame) {
				kept := g.Links[:0]
				for _, l := range g.Links {
					if l.URL != d.URL {
						kept = append(kept, l)
					}
				}
				g.Links = kept
			})
		case manKindPictures:
			for _, p := range d.Pages {
				if dir != "" && isPathWithin(p, dir) {
					if rerr := os.Remove(p); rerr != nil && !os.IsNotExist(rerr) {
						err = rerr
					}
				}
			}
		default:
			if dir == "" || !isPathWithin(d.Path, dir) {
				err = errors.New("only what you added can be removed here")
			} else if rerr := os.Remove(d.Path); rerr != nil && !os.IsNotExist(rerr) {
				err = rerr
			}
		}
		manInvalidate(game.Path)
		tbxPost(func() {
			if err != nil {
				PlayErrorSound()
				ShowToast("Could not remove it: "+filesFriendlyError(err), ToastKindError)
			} else {
				ShowToast("Removed "+d.Title, ToastKindSuccess)
			}
			manRefresh(game, true, func([]manDoc) { manDocsLanded(game.Path) })
		})
	}()
}
