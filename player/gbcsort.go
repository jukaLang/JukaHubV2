package main

// gbcsort.go — Game Boy Color games on the Game Boy shelf.
//
// Homebrew and indie games for the Game Boy Color are often published as
// ".gb" files (the cartridge format is the same), so they get copied into
// Roms/GB and show on the Game Boy shelf. Started from there with the Game
// Boy's emulator setup, some play in grey. The cartridge says what it is: the
// header every Game Boy and Game Boy Color cartridge carries has the Nintendo
// logo at 0x104 (the boot ROM refuses a cart without it) and the CGB flag at
// 0x143 — 0x80 "works on both, uses the Color's features", 0xC0 "Game Boy
// Color only". Anything else is a Game Boy game. (Pan Docs, "The Cartridge
// Header": gbdev.io/pandocs/The_Cartridge_Header.html.)
//
// Three things follow from it:
//
//   - Launch: findGameLaunchersFor (game_library.go) asks gbcLaunchAsColor,
//     and a Color cartridge on the Game Boy shelf gets the Game Boy Color
//     shelf's emulators — the same launcher or core list, the same picker —
//     so it plays in colour. A card with no Game Boy Color emulator keeps the
//     Game Boy one. A game's own Emulator pin, a game-launchers.json pin for
//     Game Boy and NextUI's paks (whose Gambatte reads the flag itself) are
//     left alone: they are checked before the list.
//   - Move: the ROM manager's "Move N Color games out of Game Boy" files them
//     in the card's Game Boy Color folder (the one the library already reads
//     for the console, else the firmware's name: GBC, or "Game Boy Color
//     (GBC)" on NextUI), with what is theirs: the files beside the ROM (saves,
//     states), the pictures beside it (Imgs, .media, .res), JukaHub's cover,
//     gameplay picture, Remove-cover marker and video snap, RetroArch's saves
//     and states sorted by content folder (saves/GB/… → saves/GBC/…, also
//     under a core folder), NextUI's Saves/GB and GB-<core> states, the game
//     options, cheats, Buttons and Turbo (through gameRenameRecords), recents,
//     collections, favourites, the RomM link, the resume marker, and the play
//     time, game page and history kept by console and title. Nothing is overwritten: a name taken in the target stops that
//     game before a file moves (a cover already there is kept instead). The
//     plan and the moves are game_rename.go's, reused; every move is logged.
//     RetroArch's per-game core settings and a firmware launcher's own files
//     are never touched.
//   - Notice: the first time a library visit finds such games, one toast says
//     so and where to move them (gbcNoticeKey keeps it from repeating).
//
// Zipped games are read too (the library starts .zip files): a zip holding
// one .gb or .gbc, read through its directory and at most the header's 336
// bytes. A .7z is read the same way (sevenzip.go: its directory, then its
// block decoded only up to the header). Every header read is bounded and
// cached by size and time.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	gbLibrarySystem  = "Game Boy"
	gbcLibrarySystem = "Game Boy Color"
	// gbcHeaderEnd is where the cartridge header ends: all the check reads.
	gbcHeaderEnd = 0x150
	// gbcZipMaxEntries and gbcCartMaxBytes bound what a zip may be before it
	// is opened: a Game Boy cartridge is at most 8 MiB.
	gbcZipMaxEntries = 64
	gbcCartMaxBytes  = 8 << 20
	// gbcNoticeKey is the Custom setting that remembers the notice was shown.
	gbcNoticeKey = "gbcSortNoticed"
	// gbcShelfDepth is how many folder levels under Roms/GB are looked at.
	gbcShelfDepth = 2
)

// gbcCartColor reports a Game Boy cartridge header that asks for the Game Boy
// Color: the logo at 0x104 and the CGB flag 0x80 or 0xC0 at 0x143.
func gbcCartColor(head []byte) bool {
	if len(head) <= 0x143 || !rommHasAt(head, 0x104, rommGBLogo) {
		return false
	}
	switch head[0x143] {
	case 0x80, 0xC0:
		return true
	}
	return false
}

// gbcReadHead reads a cartridge's header: the file's first bytes, or those of
// the one cartridge in a zip. nil when it is neither.
func gbcReadHead(path string) []byte {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gb", ".gbc":
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		head := make([]byte, gbcHeaderEnd)
		n, _ := io.ReadFull(f, head)
		return head[:n]
	case ".zip":
		return gbcZipHead(path)
	case ".7z":
		return sevenZipCartHead(path, []string{".gb", ".gbc"}, gbcZipMaxEntries, gbcCartMaxBytes, gbcHeaderEnd)
	}
	return nil
}

// gbcZipHead is the header of the single .gb or .gbc in a zip; a zip holding
// anything else (two games, a save) is not looked into.
func gbcZipHead(path string) []byte {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer zr.Close()
	if len(zr.File) > gbcZipMaxEntries {
		return nil
	}
	var cart *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if rommZipIgnored[ext] {
			continue
		}
		if (ext != ".gb" && ext != ".gbc") || cart != nil {
			return nil
		}
		cart = f
	}
	if cart == nil || cart.UncompressedSize64 > gbcCartMaxBytes {
		return nil
	}
	rc, err := cart.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	head, _ := io.ReadAll(io.LimitReader(rc, gbcHeaderEnd))
	return head
}

// gbcColorCache remembers each file's answer by its size and time, so a
// launch or a rescan costs a stat.
var gbcColorCache struct {
	sync.Mutex
	m map[string]gbcColorEntry
}

type gbcColorEntry struct {
	size  int64
	mod   time.Time
	color bool
}

// gbcROMColor reports whether the file at path is a Game Boy Color cartridge
// by its header (a .gb, a .gbc, or a zip of one). Any goroutine.
func gbcROMColor(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gb", ".gbc", ".zip", ".7z":
	default:
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	key := filepath.Clean(path)
	gbcColorCache.Lock()
	if e, ok := gbcColorCache.m[key]; ok && e.size == info.Size() && e.mod.Equal(info.ModTime()) {
		gbcColorCache.Unlock()
		return e.color
	}
	gbcColorCache.Unlock()
	color := gbcCartColor(gbcReadHead(path))
	gbcColorCache.Lock()
	if gbcColorCache.m == nil || len(gbcColorCache.m) > 8192 {
		gbcColorCache.m = map[string]gbcColorEntry{}
	}
	gbcColorCache.m[key] = gbcColorEntry{size: info.Size(), mod: info.ModTime(), color: color}
	gbcColorCache.Unlock()
	return color
}

// gbcLaunchLogged keeps the launch note to one line per game and run.
var gbcLaunchLogged sync.Map

// gbcLaunchAsColor reports a game on the Game Boy shelf whose cartridge asks
// for the Color: findGameLaunchersFor gives it the Game Boy Color shelf's
// emulators.
func gbcLaunchAsColor(system, romPath string) bool {
	if system != gbLibrarySystem || strings.TrimSpace(romPath) == "" || !gbcROMColor(romPath) {
		return false
	}
	if _, seen := gbcLaunchLogged.LoadOrStore(filepath.Clean(romPath), true); !seen {
		logInfo("library", "%s is a Game Boy Color cartridge on the Game Boy shelf: it is offered the Game Boy Color emulators", filepath.Base(romPath))
	}
	return true
}

// ── Finding them ────────────────────────────────────────────────────────────

// gbcCandidate is one Color game on the Game Boy shelf and where it would go.
type gbcCandidate struct {
	Path   string
	Target string
}

// gbcShelfFolder is one Game Boy folder and the Game Boy Color folder its
// games move into.
type gbcShelfFolder struct {
	dir, target string
}

// gbcShelfFolders are the library's Game Boy folders, as the scan reads them
// (NextUI's "Game Boy (GB)" only where NextUI runs).
func gbcShelfFolders(roots []string) []gbcShelfFolder {
	onNextUI := nextuiActive()
	var out []gbcShelfFolder
	for _, root := range roots {
		names := rommListDirs(root)
		for _, name := range names {
			if skipScanEntryName(name) {
				continue
			}
			sys, tagged, ok := romFolderSystem(name, nil)
			if !ok || sys.Name != gbLibrarySystem || (tagged && !onNextUI) {
				continue
			}
			out = append(out, gbcShelfFolder{dir: filepath.Join(root, name), target: gbcTargetFolder(root, tagged, names)})
		}
	}
	return out
}

// gbcTargetFolder is the Game Boy Color folder beside a Game Boy one: the one
// the card already has (GBC first; a NextUI-named one for a NextUI-named Game
// Boy folder), else the name the firmware gives it.
func gbcTargetFolder(root string, tagged bool, existing []string) string {
	best := ""
	for _, name := range existing {
		sys, t, ok := romFolderSystem(name, nil)
		if !ok || sys.Name != gbcLibrarySystem || t != tagged {
			continue
		}
		if strings.EqualFold(name, "GBC") {
			return filepath.Join(root, name)
		}
		if best == "" {
			best = name
		}
	}
	if best != "" {
		return filepath.Join(root, best)
	}
	if tagged {
		return filepath.Join(root, "Game Boy Color (GBC)")
	}
	if s, ok := rommSystemByID("gbc"); ok {
		return filepath.Join(root, s.Folder)
	}
	return filepath.Join(root, "GBC")
}

// gbcScanShelf walks the Game Boy folders. It returns the Color games, and
// the titles of the Game Boy games that stay (their play time and page are
// kept by title, so a moved game sharing one leaves them).
func gbcScanShelf(ctx context.Context, roots []string) (cands []gbcCandidate, stay map[string]bool) {
	stay = map[string]bool{}
	for _, sf := range gbcShelfFolders(roots) {
		_ = filepath.WalkDir(sf.dir, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() {
				if path == sf.dir {
					return nil
				}
				rel, rerr := filepath.Rel(sf.dir, path)
				if rerr != nil || skipScanEntryName(d.Name()) || rommSkipDir(d.Name()) ||
					plugGameSkipDir[strings.ToLower(d.Name())] || strings.Count(rel, string(filepath.Separator)) >= gbcShelfDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || skipScanEntryName(d.Name()) {
				return nil
			}
			if !romFileExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
				return nil
			}
			if !gbcROMColor(path) {
				stay[gameDisplayTitle(path)] = true
				return nil
			}
			rel, rerr := filepath.Rel(sf.dir, path)
			if rerr != nil {
				return nil
			}
			cands = append(cands, gbcCandidate{Path: filepath.Clean(path), Target: filepath.Join(sf.target, rel)})
			return nil
		})
	}
	return cands, stay
}

// ── Moving them ─────────────────────────────────────────────────────────────

// gbcMoveJob is one game's move.
type gbcMoveJob struct {
	game     libraryGame
	newPath  string
	cores    []string
	root     string
	snapRoot string
}

// gbcMoveResult is how one game's move went.
type gbcMoveResult struct {
	job      gbcMoveJob
	moved    int
	failed   int
	err      error
	conflict string
}

// ok reports a game whose ROM is in its new place.
func (r gbcMoveResult) ok() bool { return r.err == nil && r.conflict == "" && r.moved > 0 }

// gbcStemTaken names a game already in the target folder under the same name
// ("Tetris DX.gbc" for "Tetris DX.gb"): RetroArch would mix their saves.
func gbcStemTaken(newPath string) string {
	dir := filepath.Dir(newPath)
	stem := gameRenameStem(newPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !romFileExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		if strings.EqualFold(gameRenameStem(name), stem) {
			return name
		}
	}
	return ""
}

// gbcMovePlanFor lists the files one move takes along (off the main thread).
func gbcMovePlanFor(job gbcMoveJob) (gameRenamePlan, error) {
	rom := filepath.Clean(job.game.Path)
	dir, base := filepath.Dir(rom), filepath.Base(rom)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	newRom := filepath.Clean(job.newPath)
	newDir := filepath.Dir(newRom)
	plan := gameRenamePlan{names: map[string]string{}}
	seen := map[string]bool{}
	add := func(from, to string, copied, near, picture bool) {
		key := strings.ToLower(filepath.Clean(from))
		if seen[key] || filepath.Clean(from) == filepath.Clean(to) {
			return
		}
		seen[key] = true
		plan.moves = append(plan.moves, gameRenameMove{from: from, to: to, copied: copied, near: near, picture: picture})
	}
	add(rom, newRom, false, true, false)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return plan, err
	}
	// Another game of the same stem in the folder ("Game.gb" and "Game.gbc"):
	// only what names the whole file is this one's (gameRenamePlanFor's rule).
	shareDir := false
	for _, e := range entries {
		name := e.Name()
		if strings.EqualFold(name, base) || !e.Type().IsRegular() {
			continue
		}
		if romFileExtensions[strings.ToLower(filepath.Ext(name))] && strings.EqualFold(gameRenameStem(name), stem) {
			shareDir = true
		}
	}
	// Beside the ROM: its saves and states, never another game.
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || romFileExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		cut, ok := gameCompanionCut(name, base, stem)
		if !ok || (shareDir && cut != len(base)) {
			continue
		}
		add(filepath.Join(dir, name), filepath.Join(newDir, name), false, true, false)
	}

	// Its pictures beside it.
	for _, sub := range []string{"Imgs", ".media"} {
		pics, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range pics {
			name := e.Name()
			ext := filepath.Ext(name)
			if !e.Type().IsRegular() || !artCleanupImage(name) || !strings.EqualFold(strings.TrimSuffix(name, ext), stem) {
				continue
			}
			add(filepath.Join(dir, sub, name), filepath.Join(newDir, sub, name), shareDir, false, true)
		}
	}
	res, _ := os.ReadDir(filepath.Join(dir, ".res"))
	for _, e := range res {
		name := e.Name()
		if e.Type().IsRegular() && artCleanupImage(name) && len(name) > len(base) &&
			strings.EqualFold(name[:len(base)], base) && name[len(base)] == '.' {
			add(filepath.Join(dir, ".res", name), filepath.Join(newDir, ".res", name), false, false, true)
		}
	}

	// JukaHub's game-art folder keeps pictures by console: they are copied, so
	// another Game Boy game of the same name keeps its own.
	oldROM := scannedROM{Path: rom, System: gbLibrarySystem}
	newROM := scannedROM{Path: newRom, System: gbcLibrarySystem}
	for _, pair := range append([][2]string{
		{romArtworkPath(oldROM, job.root), romArtworkPath(newROM, job.root)},
		{playShotPath(oldROM, job.root), playShotPath(newROM, job.root)},
		{artworkNoCoverPath(oldROM, job.root), artworkNoCoverPath(newROM, job.root)},
	}, artStyleKindPairs(oldROM, newROM, job.root)...) { // screenshot, title screen (artstyle.go)
		if pair[0] != "" && pair[1] != "" && regularFile(pair[0]) {
			add(pair[0], pair[1], true, false, true)
		}
	}
	// The video snap, kept the same way (snappreview.go: <console>/<stem>).
	if snap := gameSnapPath(job.game, job.snapRoot); snap != "" {
		to := filepath.Join(strings.TrimSpace(job.snapRoot), strings.ReplaceAll(gbcLibrarySystem, " ", "_"), filepath.Base(snap))
		add(snap, to, true, false, true)
	}

	// The save folders: files named after the game, moved from the old
	// folder name to the new one.
	scan := func(from, to string, accept func(name string, cut int) bool, copied bool) {
		files, err := os.ReadDir(from)
		if err != nil {
			return
		}
		for _, e := range files {
			name := e.Name()
			if !e.Type().IsRegular() {
				continue
			}
			if cut, ok := gameCompanionCut(name, base, stem); ok && accept(name, cut) {
				add(filepath.Join(from, name), filepath.Join(to, name), copied, false, false)
			}
		}
	}
	retroArch := func(name string, cut int) bool {
		if cut == len(base) {
			return true
		}
		if _, state := gmetaStateSlot(strings.TrimSuffix(name, ".png"), stem); state {
			return true
		}
		return gameRenameSaveExts[strings.ToLower(filepath.Ext(name))]
	}
	wholeName := func(_ string, cut int) bool { return cut == len(base) }
	oldDirName, newDirName := filepath.Base(dir), filepath.Base(newDir)
	oldTag, newTag := gameRenameNextUITag(rom), gameRenameNextUITag(newRom)
	for _, r := range saveManagerRoots() {
		folder := filepath.Clean(r.Path)
		if strings.EqualFold(folder, filepath.Clean(dir)) {
			continue // the ROM's own folder is done above
		}
		switch {
		case r.Name == "RetroArch saves" || r.Name == "RetroArch states":
			// Sorted by content folder (saves/GB/, or saves/Gambatte/GB/):
			// the folder's name changes with the move. At the top of the
			// folder, or in a core's folder alone, the name stays the same.
			if strings.EqualFold(oldDirName, newDirName) {
				continue
			}
			for _, rel := range gameResetStateSubdirs(rom, job.cores) {
				if !strings.EqualFold(filepath.Base(rel), oldDirName) {
					continue
				}
				newRel := newDirName
				if parent := filepath.Dir(rel); parent != "." {
					newRel = filepath.Join(parent, newDirName)
				}
				scan(filepath.Join(folder, rel), filepath.Join(folder, newRel), retroArch, shareDir)
			}
			// Both sorts on: RetroArch puts the content folder first
			// (saves/GB/Gambatte/).
			for _, core := range job.cores {
				for _, f := range resetCoreFolders(core) {
					scan(filepath.Join(folder, oldDirName, f), filepath.Join(folder, newDirName, f), retroArch, shareDir)
				}
			}
		case r.Name == "NextUI saves" && oldTag != "" && newTag != "":
			scan(filepath.Join(folder, oldTag), filepath.Join(folder, newTag), wholeName, shareDir)
		case strings.HasPrefix(r.Name, "NextUI states") && oldTag != "" && newTag != "":
			// .userdata/<platform>/GB-gambatte → GBC-gambatte.
			name := filepath.Base(folder)
			if len(name) > len(oldTag) && strings.EqualFold(name[:len(oldTag)], oldTag) && name[len(oldTag)] == '-' {
				scan(folder, filepath.Join(filepath.Dir(folder), newTag+name[len(oldTag):]), wholeName, shareDir)
			}
		}
	}
	return plan, nil
}

// gbcMoveFile does one move or copy, into a folder it creates when needed,
// and never over a file that is there.
func gbcMoveFile(m gameRenameMove) error {
	if _, err := os.Lstat(m.to); err == nil {
		return fmt.Errorf("%s already exists", filepath.Base(m.to))
	}
	if err := os.MkdirAll(filepath.Dir(m.to), 0o755); err != nil {
		return err
	}
	if m.copied {
		return gmetaCopyFile(m.from, m.to)
	}
	return os.Rename(m.from, m.to)
}

// gbcMoveExecute plans and runs one game's move (off the main thread). The
// files beside the ROM go first and are put back if one of them fails; the
// rest is best effort, counted and logged.
func gbcMoveExecute(job gbcMoveJob) gbcMoveResult {
	res := gbcMoveResult{job: job}
	if taken := gbcStemTaken(job.newPath); taken != "" {
		res.conflict = taken
		return res
	}
	plan, err := gbcMovePlanFor(job)
	if err != nil {
		res.err = err
		return res
	}
	if name := gameRenameConflict(&plan); name != "" {
		res.conflict = name
		return res
	}
	var done []gameRenameMove
	undo := func() {
		for i := len(done) - 1; i >= 0; i-- {
			m := done[i]
			if m.copied {
				_ = os.Remove(m.to)
				continue
			}
			if err := os.Rename(m.to, m.from); err != nil {
				logWarn("library", "Color move: cannot put %s back: %v", m.to, err)
			}
		}
	}
	for _, m := range plan.moves {
		if !m.near {
			continue
		}
		if err := gbcMoveFile(m); err != nil {
			undo()
			res.err = err
			return res
		}
		logInfo("library", "Color move: %s → %s", m.from, m.to)
		done = append(done, m)
	}
	res.moved = len(done)
	for _, m := range plan.moves {
		if m.near {
			continue
		}
		if err := gbcMoveFile(m); err != nil {
			logWarn("library", "Color move: %s stayed: %v", m.from, err)
			res.failed++
			continue
		}
		if m.copied {
			logInfo("library", "Color move: %s copied to %s", m.from, m.to)
		} else {
			logInfo("library", "Color move: %s → %s", m.from, m.to)
		}
		res.moved++
	}
	return res
}

// gbcMoveBusy is set while a move runs.
var gbcMoveBusy atomic.Bool

// errGBCMoveBusy is the answer while files are already moving.
var errGBCMoveBusy = errors.New("files are already moving")

// gbcMoveAll moves the games in the background and brings JukaHub's records
// along on the main thread; done runs there afterwards with how many moved.
// Main thread.
func gbcMoveAll(cands []gbcCandidate, stay map[string]bool, done func(moved int)) error {
	if len(cands) == 0 {
		return nil
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		return errGBCMoveBusy
	}
	gbcMoveBusy.Store(true)
	jobs := make([]gbcMoveJob, 0, len(cands))
	root, snapRoot := libraryArtworkRoot(), librarySnapRoot()
	for _, c := range cands {
		game := libraryGame{Path: c.Path, System: gbLibrarySystem, Title: gameDisplayTitle(c.Path)}
		jobs = append(jobs, gbcMoveJob{game: game, newPath: c.Target, root: root, snapRoot: snapRoot})
	}
	ShowToast("Moving "+pluralizeCount(len(jobs), "Color game", "Color games")+" out of Game Boy…", ToastKindInfo)
	go func() {
		results := make([]gbcMoveResult, 0, len(jobs))
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "Color move stopped: %v", r)
			}
			tbxPost(func() {
				defer gameResetBusy.Store(false)
				defer gbcMoveBusy.Store(false)
				moved := gbcMoveFinish(results, stay)
				if done != nil {
					done(moved)
				}
			})
		}()
		for _, job := range jobs {
			// The cores whose folders may hold its saves: the Game Boy
			// shelf's emulator it may have run in, and the Game Boy Color
			// one it runs in now.
			job.cores = gameResetCores(job.game)
			if l := resolveGameLauncher(gbLibrarySystem); l != "" {
				if core := normalizeCoreName(gameCoreFromLauncher(l, gbLibrarySystem)); core != "" && !containsFold(job.cores, core) {
					job.cores = append(job.cores, core)
				}
			}
			color := libraryGame{Path: job.newPath, System: gbcLibrarySystem, Title: job.game.Title}
			for _, core := range gameResetCores(color) {
				if !containsFold(job.cores, core) {
					job.cores = append(job.cores, core)
				}
			}
			res := gbcMoveExecute(job)
			switch {
			case res.conflict != "":
				logInfo("library", "Color move: %s stays in Game Boy · %s is already in %s", job.game.Path, res.conflict, filepath.Dir(job.newPath))
			case res.err != nil:
				logWarn("library", "Color move: %s stays in Game Boy: %v", job.game.Path, res.err)
			default:
				logInfo("library", "Color move: %s → %s (%d files, %d stayed)", job.game.Path, job.newPath, res.moved, res.failed)
			}
			results = append(results, res)
		}
	}()
	return nil
}

// containsFold reports whether list holds s, ignoring case.
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// gbcMoveFinish brings JukaHub's records to the moved games, says how it went
// and rescans an open library. It returns how many games moved. Main thread.
func gbcMoveFinish(results []gbcMoveResult, stay map[string]bool) int {
	moved, skipped, failedFiles := 0, 0, 0
	var forget []string
	for _, res := range results {
		if !res.ok() {
			skipped++
			continue
		}
		moved++
		failedFiles += res.failed
		game, newPath := res.job.game, res.job.newPath
		gameRenameRecords(game, newPath)
		if !stay[game.Title] {
			gbcMoveTitle(game.Title)
		}
		gbcRecentsSetSystem(newPath)
		gbcQuickResumeSystem(newPath)
		oldDir, newDir := filepath.Dir(game.Path), filepath.Dir(newPath)
		forget = append(forget,
			filepath.Join(oldDir, "Imgs"), filepath.Join(oldDir, ".media"), filepath.Join(oldDir, ".res"),
			filepath.Join(newDir, "Imgs"), filepath.Join(newDir, ".media"), filepath.Join(newDir, ".res"))
	}
	if moved > 0 {
		root := libraryArtworkRoot()
		for _, sys := range []string{gbLibrarySystem, gbcLibrarySystem} {
			d := artCleanupDirName(sys)
			forget = append(forget, filepath.Join(root, d), filepath.Join(root, playShotDirName, d), filepath.Join(root, artworkNoCoverDir, d))
		}
		artworkDirForget(forget...)
		gameLibraryEdited()
		gameLib.recents = recentGames()
		libraryRescanSoon(libraryRescanTries)
	}
	msg := "Moved " + pluralizeCount(moved, "game", "games") + " to Game Boy Color"
	switch {
	case moved == 0 && skipped > 0:
		ShowToast("No game moved · a game of the same name is already there, or a file could not move (see the log)", ToastKindWarning)
	case skipped > 0 || failedFiles > 0:
		if skipped > 0 {
			msg += " · " + pluralizeCount(skipped, "stayed", "stayed")
		}
		if failedFiles > 0 {
			msg += " · " + gameResetFilesWord(failedFiles) + " stayed behind"
		}
		ShowToast(msg, ToastKindWarning)
	case moved > 0:
		ShowToast(msg+" · saves, play time and covers went with them", ToastKindSuccess)
	}
	return moved
}

// gbcMoveTitle moves what JukaHub keeps by console and title from Game Boy to
// Game Boy Color: the play time (added to a figure already there), the game
// page (merged into one already there) and the session history (only when
// the Color has none under the title). gameRenameMoveTitle's rules, across
// consoles.
func gbcMoveTitle(title string) {
	if strings.TrimSpace(title) == "" {
		return
	}
	oldKey, newKey := playtimeGameKey(gbLibrarySystem, title), playtimeGameKey(gbcLibrarySystem, title)
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
	oldMeta, newMeta := gmetaKey(gbLibrarySystem, title), gmetaKey(gbcLibrarySystem, title)
	if rec := gmetaStore.games[oldMeta]; rec != nil && oldMeta != newMeta {
		if have := gmetaStore.games[newMeta]; have != nil {
			gmetaMerge(have, rec)
			gmetaNormalize(have)
		} else {
			rec.System = gbcLibrarySystem
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

// gbcRecentsSetSystem files a moved game's recents entry under the Color.
func gbcRecentsSetSystem(path string) {
	gameRecentsMu.Lock()
	defer gameRecentsMu.Unlock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	changed := false
	for i := range gameRecents {
		if gameRecents[i].Path == path && gameRecents[i].System != gbcLibrarySystem {
			gameRecents[i].System = gbcLibrarySystem
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := saveGameRecentsFile(gameRecentsFile(), gameRecents); err != nil {
		logWarn("library", "cannot save recents: %v", err)
	}
}

// gbcQuickResumeSystem files the resume marker of a moved game (already
// pointed at its new file by quickResumeRepath) under the Color.
func gbcQuickResumeSystem(path string) {
	file := quickResumeMarkerPath()
	if file == "" {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var m quickResumeMarker
	if json.Unmarshal(data, &m) != nil || m.Path != path || m.System == gbcLibrarySystem {
		return
	}
	m.System = gbcLibrarySystem
	quickResumeWriteMarker(m)
}

// ── The notice ──────────────────────────────────────────────────────────────

// gbcNotice is where the one-time notice stands: 0 not known yet (the flag
// lives in the main thread's config), 1 not shown, 2 shown.
var gbcNotice struct {
	state atomic.Int32
}

// gbcNoticeCheck looks for Color games on the Game Boy shelf after a library
// scan (disctools.go's automatic pass, off the main thread) and asks the main
// thread to say so, once.
func gbcNoticeCheck(roots []string) {
	switch gbcNotice.state.Load() {
	case 2:
		return
	case 0:
		tbxPost(gbcNoticeLoad)
		return
	}
	cands, _ := gbcScanShelf(context.Background(), roots)
	if n := len(cands); n > 0 {
		tbxPost(func() { gbcNoticeShow(n) })
	}
}

// gbcNoticeLoad reads the flag on the main thread and, when the notice is
// still to come and the library is open, looks at once.
func gbcNoticeLoad() {
	if appConfig == nil {
		return
	}
	if customBool(appConfig.Variables.Custom, gbcNoticeKey, false) {
		gbcNotice.state.Store(2)
		return
	}
	gbcNotice.state.Store(1)
	if gameLib.open {
		roots := append([]string(nil), romLibraryRoots()...)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logWarn("library", "looking for Color games stopped: %v", r)
				}
			}()
			gbcNoticeCheck(roots)
		}()
	}
}

// gbcNoticeShow is the notice itself: a toast while the library is open (a
// scan by another screen waits for a library visit). Main thread.
func gbcNoticeShow(n int) {
	if gbcNotice.state.Load() == 2 || appConfig == nil || !gameLib.open {
		return
	}
	gbcNotice.state.Store(2)
	if appConfig.Variables.Custom == nil {
		appConfig.Variables.Custom = map[string]interface{}{}
	}
	appConfig.Variables.Custom[gbcNoticeKey] = true
	saveUserConfigDebounced(userConfigFromVariables(appConfig))
	logInfo("library", "%d Game Boy Color cartridges are on the Game Boy shelf; the notice was shown", n)
	ShowToast(pluralizeCount(n, "Game Boy game is", "Game Boy games are")+" made for the Color · they play in color, "+
		"and the ROM manager (Settings → Games & emulators) can move them to Game Boy Color", ToastKindInfo)
}
