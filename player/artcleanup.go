package main

// artcleanup.go — "Clean up covers" in the library's options sheet.
//
// The box-art fetch writes <data>/game-art/<System>/<rom stem>.png for every
// game it finds a cover for. A ROM deleted or renamed later leaves its cover
// behind, and over a few card reshuffles that is megabytes of pictures no
// game shows. The cleanup finds them and, after a confirmation, moves them to
// the reset trash (gamereset.go), where they stay for a while.
//
// It is deliberately narrow:
//   - only JukaHub's own game-art folder — never the firmware's Imgs or
//     MinUI's .res folders beside the ROMs, which other front ends own;
//   - only image files straight inside a console's folder, and only for a
//     console the fresh scan found games for: a missing or empty Roms folder
//     (a card not mounted, a scan that read nothing) leaves its covers alone,
//     and a library root that held games and now reads as missing or empty
//     stops the whole cleanup;
//   - a cover is used when any scanned ROM, the library already on screen or a
//     recent game has its stem (compared ignoring case, as FAT does);
//   - a cover written in the last few minutes is left, in case a fetch for a
//     game added since the scan is writing it.
// The walk runs on a goroutine; the answer is picked up by the sheet.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// artCleanupFresh is how recent a cover may be and still be left alone.
const artCleanupFresh = 10 * time.Minute

// artCleanupFile is one unused cover.
type artCleanupFile struct {
	Path   string
	System string // the console folder's name inside game-art
	Size   int64
}

// artCleanup is the scan's state. busy is a scan in flight; ready is a result
// waiting for the sheet to show it; files is that result.
var artCleanup struct {
	mu    sync.Mutex
	busy  bool
	ready bool
	err   string
	files []artCleanupFile
	bytes int64
}

// artCleanupImage reports the image types the art folders hold.
func artCleanupImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

// artCleanupDirName is the game-art folder of a console (romArtworkPath).
func artCleanupDirName(system string) string {
	return strings.ReplaceAll(system, " ", "_")
}

// artCleanupUsedStems folds ROM paths into, per console folder, the stems in
// use, lower-cased.
func artCleanupUsedStems(used map[string]map[string]bool, system, romPath string) {
	stem := gameResetStem(romPath)
	if stem == "" || strings.TrimSpace(system) == "" {
		return
	}
	dir := artCleanupDirName(system)
	if used[dir] == nil {
		used[dir] = map[string]bool{}
	}
	used[dir][strings.ToLower(stem)] = true
}

// artCleanupFind lists the unused covers under root. scanned holds the
// consoles the fresh scan found games for (by game-art folder name); used the
// stems in use. Pure apart from reading the folders, so the rules above are in
// one place.
func artCleanupFind(root string, scanned map[string]bool, used map[string]map[string]bool, now time.Time) []artCleanupFile {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []artCleanupFile
	for _, sys := range entries {
		name := sys.Name()
		if !sys.IsDir() || strings.HasPrefix(name, ".") || !scanned[name] ||
			strings.EqualFold(name, artCleanupDirName(androidLibrarySystem)) {
			continue
		}
		stems := used[name]
		if len(stems) == 0 {
			continue
		}
		dir := filepath.Join(root, name)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			fname := f.Name()
			if f.IsDir() || strings.HasPrefix(fname, ".") || !artCleanupImage(fname) {
				continue
			}
			stem := strings.TrimSuffix(fname, filepath.Ext(fname))
			if stems[strings.ToLower(stem)] {
				continue
			}
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < artCleanupFresh {
				continue
			}
			out = append(out, artCleanupFile{Path: filepath.Join(dir, fname), System: name, Size: info.Size()})
		}
	}
	return out
}

// artCleanupRootOf is the root a ROM path lies under, or "".
func artCleanupRootOf(roots []string, path string) string {
	path = strings.ToLower(filepath.Clean(path))
	for _, root := range roots {
		r := strings.ToLower(filepath.Clean(root))
		if strings.HasPrefix(path, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator)) {
			return root
		}
	}
	return ""
}

// artCleanupLibraryRoots lists the roots the library on screen found games
// under (render thread: it reads the library).
func artCleanupLibraryRoots(roots []string, systems []librarySystem) map[string]bool {
	out := map[string]bool{}
	for _, sys := range systems {
		for _, g := range sys.Games {
			if root := artCleanupRootOf(roots, g.Path); root != "" {
				out[root] = true
			}
		}
	}
	return out
}

// artCleanupMissingRoot names a library root the fresh scan cannot vouch for:
// one the scan did not cover, one that no longer reads (a card not mounted),
// or one the library had games under and the scan found none in. "" is a scan
// that saw every root it should have.
func artCleanupMissingRoot(roots []string, report romScanReport, expected map[string]bool) string {
	if len(report.Roots) < len(roots) {
		for _, root := range roots {
			found := false
			for _, r := range report.Roots {
				if r == root {
					found = true
					break
				}
			}
			if !found {
				return root
			}
		}
	}
	found := map[string]bool{}
	for _, rom := range report.Games {
		if root := artCleanupRootOf(roots, rom.Path); root != "" {
			found[root] = true
		}
	}
	for _, root := range roots {
		if !expected[root] {
			continue
		}
		if _, err := os.ReadDir(root); err != nil || !found[root] {
			return root
		}
	}
	return ""
}

// startArtCleanupScan looks for unused covers off the render thread. The
// library on screen and the recents are read here, on the render thread, and
// count as used next to the fresh scan's ROMs. It reports whether a scan
// started, so the caller waits for its answer (artCleanupTake).
func startArtCleanupScan() bool {
	if artworkFetchRunning() {
		ShowToast("The artwork fetch is running — let it finish, then clean up", ToastKindInfo)
		return false
	}
	root := libraryArtworkRoot()
	if strings.TrimSpace(root) == "" || !dirExists(root) {
		ShowToast("There are no downloaded covers to clean up", ToastKindInfo)
		return false
	}
	artCleanup.mu.Lock()
	if artCleanup.busy {
		artCleanup.mu.Unlock()
		ShowToast("Still looking for unused covers…", ToastKindInfo)
		return false
	}
	artCleanup.busy = true
	artCleanup.ready = false
	artCleanup.files = nil
	artCleanup.err = ""
	artCleanup.mu.Unlock()

	used := map[string]map[string]bool{}
	for _, sys := range gameLib.lib.Systems {
		for _, g := range sys.Games {
			artCleanupUsedStems(used, g.System, g.Path)
		}
	}
	for _, r := range recentGames() {
		artCleanupUsedStems(used, r.System, r.Path)
	}
	roots := romLibraryRoots()
	// The roots the library on screen has games under: each must still read
	// and still hold games, or a card that came out would make its covers
	// look unused.
	expected := artCleanupLibraryRoots(roots, gameLib.lib.Systems)
	ShowToast("Looking for unused covers…", ToastKindInfo)
	go func() {
		report := scanROMLibrary(roots)
		scanned := map[string]bool{}
		for _, rom := range report.Games {
			artCleanupUsedStems(used, rom.System, rom.Path)
			if report.Counts[rom.System] > 0 {
				scanned[artCleanupDirName(rom.System)] = true
			}
		}
		var files []artCleanupFile
		why := ""
		missing := artCleanupMissingRoot(roots, report, expected)
		if len(report.Games) == 0 {
			// No ROM found at all is a card that is not there or a scan that
			// failed, never "every cover is unused".
			why = "No games were found, so every cover stays"
		} else if missing != "" {
			logInfo("library", "cover cleanup skipped: %s not readable or empty", missing)
			why = "A games folder could not be read (" + missing + "), so every cover stays"
		} else {
			files = artCleanupFind(root, scanned, used, time.Now())
		}
		var total int64
		for _, f := range files {
			total += f.Size
		}
		artCleanup.mu.Lock()
		artCleanup.busy = false
		artCleanup.ready = true
		artCleanup.err = why
		artCleanup.files = files
		artCleanup.bytes = total
		artCleanup.mu.Unlock()
	}()
	return true
}

// artCleanupTake hands a finished scan to the render thread once.
func artCleanupTake() (files []artCleanupFile, bytes int64, why string, ok bool) {
	artCleanup.mu.Lock()
	defer artCleanup.mu.Unlock()
	if !artCleanup.ready {
		return nil, 0, "", false
	}
	artCleanup.ready = false
	files, bytes, why = artCleanup.files, artCleanup.bytes, artCleanup.err
	artCleanup.files = nil
	return files, bytes, why, true
}

// artCleanupBusy reports a scan in flight, for the sheet's row value.
func artCleanupBusy() bool {
	artCleanup.mu.Lock()
	defer artCleanup.mu.Unlock()
	return artCleanup.busy
}

// artCleanupSummary is "12 unused covers, 3.4 MB".
func artCleanupSummary(n int, bytes int64) string {
	return pluralizeCount(n, "unused cover", "unused covers") + ", " + humanBytes(bytes)
}

// runArtCleanup moves the confirmed covers to the trash on a goroutine and
// toasts the result. The library's cover cache is told on the next rescan.
func runArtCleanup(files []artCleanupFile) {
	if len(files) == 0 {
		return
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("A reset or cleanup is still moving files", ToastKindInfo)
		return
	}
	go func() {
		defer gameResetBusy.Store(false)
		dir, err := resetTrashNewDir("-covers")
		if err != nil {
			ShowToast("Cannot make the trash folder: "+firstLine(err.Error()), ToastKindError)
			return
		}
		list := make([]gameResetFile, 0, len(files))
		system := make(map[string]string, len(files))
		for _, f := range files {
			list = append(list, gameResetFile{Path: f.Path, Kind: "covers"})
			system[f.Path] = f.System
		}
		moved, failed := resetTrashMoveAll(dir, list, func(f gameResetFile) string {
			return resetTrashSlug(system[f.Path])
		})
		pruneResetTrash(dir)
		logInfo("library", "cover cleanup: %d moved, %d failed", moved, failed)
		if failed > 0 {
			ShowToast(pluralizeCount(moved, "cover", "covers")+" moved to the trash · "+
				gameResetFilesWord(failed)+" could not be removed", ToastKindWarning)
			return
		}
		ShowToast(pluralizeCount(moved, "unused cover", "unused covers")+" moved to the trash", ToastKindSuccess)
	}()
}
