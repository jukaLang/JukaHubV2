package main

// homebrew_easyrpg.go — RPG Maker 2000/2003 games in Get Homebrew
// (homebrew_store.go), played by EasyRPG Player (RetroArch's easyrpg core),
// and the Games library's rule for them (romshelf.go).
//
// An RPG Maker 2000/2003 game is a folder: RPG_RT.ldb (the database),
// RPG_RT.lmt (the map tree), maps, pictures and music, and on Windows the
// RPG_RT.exe that EasyRPG replaces. So, unlike a cartridge:
//
//   - The list comes from itch.io's "Made with EasyRPG" filter and the
//     rpg-maker-2003, rpg-maker-2000 and rm2k3 tags. "Made with RPG Maker" is
//     left out on purpose: its ten thousand games are mostly MV and MZ, which
//     EasyRPG cannot play.
//   - Install takes the game's .zip — usually flagged as a Windows download,
//     since the game ships with RPG_RT.exe, which the cartridge picker skips —
//     and keeps it only when RPG_RT.ldb and RPG_RT.lmt sit at the zip's top or
//     one folder down. A later RPG Maker's game (XP/VX .rgss archives, MV/MZ
//     rpg_core.js) or a zip around a setup .exe is refused with the reason.
//     A self-extracting .exe, a .7z or a .rar is never run or guessed at: the
//     page says so. A .easyrpg file (a renamed zip the core opens directly)
//     installs like a cartridge through the store's usual path.
//   - The zip is unpacked by pkgExtractZip (an os.Root on a hidden staging
//     folder: no name can climb out, links and special files are refused,
//     entries and unpacked size are bounded, free space is checked), and the
//     game's folder becomes Roms/EASYRPG/<Title>. A folder of that name that
//     is not a leftover of this store is never overwritten: the game takes
//     "<Title> (2)".
//   - EasyRPG writes SaveXX.lsd into the game's folder, so an update swaps the
//     folder but carries the saves (and any other small file the game wrote
//     that the update does not ship) into the new one, and Delete removes the
//     game but leaves its saves in the folder — a later install of the same
//     game picks them up.
//   - A game whose RPG_RT.ini lacks FullPackageFlag=1 uses RPG Maker's free
//     run-time package (RTP); EasyRPG looks for it in RetroArch's
//     system/rtp/2000 and system/rtp/2003 folders, and the install says so.
//
// The record's file is the game's RPG_RT.ldb: that is the file the easyrpg
// core loads (its supported extensions are ldb, zip, lzh and easyrpg), the
// one file per game the shelf lists, and what the update and delete checks
// (hbRecFileOK) look at. The shelf titles it after its folder.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// hbEasyRPGSystem is the Games library's name for these games
	// (romSystemAliases: Roms/EASYRPG, easyrpg, rpgmaker).
	hbEasyRPGSystem = "EasyRPG"
	// hbEasyRPGZipMax bounds a game's download: with its music a 2003 game is
	// rarely past 200 MB.
	hbEasyRPGZipMax = 512 << 20
	// hbEasyRPGCarryMax bounds a non-save file an update carries over.
	hbEasyRPGCarryMax = 4 << 20
	// easyrpgDBName is the file a game is listed by and the core loads.
	easyrpgDBName = "RPG_RT.ldb"
	// hbEasyRPGRTPNote is said after an install that may need the RTP.
	hbEasyRPGRTPNote = "It may need RPG Maker's free RTP: put it in RetroArch's system/rtp/2000 or system/rtp/2003 folder"
)

func init() {
	romShelfRegister(hbEasyRPGSystem, easyrpgShelfLists)
	romShelfRegisterStem(easyrpgShelfStem)
}

// ── The shelf ───────────────────────────────────────────────────────────

// easyrpgShelfLists is the EasyRPG folder's rule: a game folder's
// RPG_RT.ldb, a .easyrpg file, and a .zip straight in the console's folder
// (the core opens a zipped game itself). Maps, pictures, music and a zip
// inside a game's folder (a bundled RTP) are not games. Path only, so it
// costs nothing inside the scan's walk.
func easyrpgShelfLists(consoleDir, p, ext string) bool {
	switch {
	case strings.EqualFold(filepath.Base(p), easyrpgDBName):
		return true
	case ext == ".easyrpg":
		return true
	case ext == ".zip":
		return filepath.Clean(filepath.Dir(p)) == filepath.Clean(consoleDir)
	}
	return false
}

// easyrpgShelfStem titles a game's RPG_RT.ldb after its folder; one straight
// in the console's folder keeps its own name.
func easyrpgShelfStem(p string) (string, bool) {
	if !strings.EqualFold(filepath.Base(p), easyrpgDBName) {
		return "", false
	}
	if dir := romStemFolder(filepath.Dir(p)); dir != "" {
		return dir, true
	}
	return "", false
}

// ── Which file ──────────────────────────────────────────────────────────

// hbEasyRPGIndex is EasyRPG's hbConsoles index, or -1.
func hbEasyRPGIndex() int { return hbConsoleIndex(hbEasyRPGID) }

// hbWantsEasyRPG reports whether a console set includes EasyRPG.
func hbWantsEasyRPG(consoles uint16) bool {
	c := hbEasyRPGIndex()
	return c >= 0 && consoles&(1<<uint(c)) != 0
}

// hbEasyRPGRec reports an install of an RPG Maker game folder.
func hbEasyRPGRec(r hbInstallRec) bool {
	return r.Console == hbEasyRPGID && strings.EqualFold(filepath.Base(r.Path), easyrpgDBName)
}

// hbInstalledName is how the page names an installed game: its folder for an
// RPG Maker game, else its file.
func hbInstalledName(r hbInstallRec) string {
	if hbEasyRPGRec(r) {
		return filepath.Base(filepath.Dir(r.Path)) + string(filepath.Separator)
	}
	return filepath.Base(r.Path)
}

// hbEasyRPGUpload is the zip a game listed under EasyRPG installs as a
// folder, when no upload is a file the store installs as it is (a .easyrpg,
// or a cartridge for another of its consoles). Pure.
func hbEasyRPGUpload(ups []hbUpload, consoles uint16) (hbUpload, bool) {
	if !hbWantsEasyRPG(consoles) {
		return hbUpload{}, false
	}
	if p, ok := hbPickUpload(ups, consoles); ok && p.Console >= 0 {
		return hbUpload{}, false
	}
	return hbEasyRPGPick(ups)
}

// hbEasyRPGPick chooses the game's zip: a full game before a demo, the
// game before a zip named as the RTP, then the biggest. A Windows flag is
// expected (the game ships RPG_RT.exe); an Android-only zip is not the
// game's folder. Pure.
func hbEasyRPGPick(ups []hbUpload) (hbUpload, bool) {
	best, bestScore := hbUpload{}, 0
	for _, u := range ups {
		if u.Preorder || hbSkipUploadTypes[u.Type] || u.Storage == "external" || hbRomExt(u.Filename) != ".zip" {
			continue
		}
		if (u.Android && !u.PC) || u.Size > hbEasyRPGZipMax {
			continue
		}
		score := 10
		if !u.Demo {
			score += 5
		}
		for _, w := range launcherNameTokens(u.Filename + " " + u.Display) {
			if w == "rtp" {
				score -= 6
				break
			}
		}
		if score > bestScore || (score == bestScore && u.Size > best.Size) {
			best, bestScore = u, score
		}
	}
	return best, bestScore > 0
}

// hbEasyRPGNoZipReason says why an RPG Maker game's files hold no zip, or
// "" when nothing more useful than "no cartridge" can be said. Pure.
func hbEasyRPGNoZipReason(ups []hbUpload) string {
	exe, packed := false, false
	for _, u := range ups {
		if u.Preorder || hbSkipUploadTypes[u.Type] || u.Storage == "external" {
			continue
		}
		switch hbRomExt(u.Filename) {
		case ".exe", ".msi":
			exe = true
		case ".7z", ".rar":
			packed = true
		}
	}
	switch {
	case exe:
		return "it comes as a Windows installer (.exe) — EasyRPG needs the game's folder in a .zip"
	case packed:
		return "it comes as a .7z or .rar — Get Homebrew unpacks only .zip games"
	}
	return ""
}

// hbEasyRPGJob reports whether an install takes the folder path: an update
// of a folder it installed, or a game listed under EasyRPG with a zip and no
// file to take as it is.
func hbEasyRPGJob(consoles uint16, ups []hbUpload, prev hbInstallRec, hadPrev bool) bool {
	if hadPrev && hbEasyRPGRec(prev) {
		return true
	}
	_, ok := hbEasyRPGUpload(ups, consoles)
	return ok
}

// ── What is in the zip ──────────────────────────────────────────────────

// hbEasyRPGZipRoot finds the game in a zip's entry names: the folder (a
// slash path, "" for the zip's top) that holds RPG_RT.ldb and RPG_RT.lmt, at
// the top or one folder down, the shallowest first. The error says what the
// zip is instead. Pure.
func hbEasyRPGZipRoot(names []string) (string, error) {
	dbs := map[string]string{} // lower-case folder → its name in the zip
	maps := map[string]bool{}
	later, exe := false, false
	for _, n := range names {
		// Cleaned as the unpack cleans it (pkgArchiveRel): "./Game/RPG_RT.ldb"
		// lands in Game.
		clean := strings.Trim(path.Clean("/"+strings.ReplaceAll(n, `\`, "/")), "/")
		base := path.Base(clean)
		if clean == "" || strings.HasPrefix(base, ".") || strings.Contains(clean, "__MACOSX") {
			continue
		}
		dir := path.Dir(clean)
		if dir == "." {
			dir = ""
		}
		low := strings.ToLower(base)
		switch {
		case low == "rpg_rt.ldb":
			dbs[strings.ToLower(dir)] = dir
		case low == "rpg_rt.lmt":
			maps[strings.ToLower(dir)] = true
		case low == "rpg_core.js" || low == "rmmz_core.js" || strings.HasSuffix(low, ".rgssad") ||
			strings.HasSuffix(low, ".rgss2a") || strings.HasSuffix(low, ".rgss3a") ||
			strings.HasSuffix(low, ".rxdata") || strings.HasSuffix(low, ".rvdata") || strings.HasSuffix(low, ".rvdata2"):
			later = true
		case strings.HasSuffix(low, ".exe") || strings.HasSuffix(low, ".msi"):
			exe = true
		}
	}
	best, found := "", false
	for key := range dbs {
		if !maps[key] || strings.Contains(key, "/") {
			continue
		}
		if !found || len(key) < len(best) || (len(key) == len(best) && key < best) {
			best, found = key, true
		}
	}
	switch {
	case found:
		return dbs[best], nil
	case len(dbs) > 0:
		return "", errors.New("the game's folder is incomplete or too deep in the zip (RPG_RT.ldb needs RPG_RT.lmt beside it, at most one folder down)")
	case later:
		return "", errors.New("it was made with a later RPG Maker (XP, VX, MV or MZ) — EasyRPG plays only RPG Maker 2000 and 2003 games")
	case exe:
		return "", errors.New("the zip holds an installer (.exe), not the game's folder — EasyRPG needs the folder with RPG_RT.ldb")
	}
	return "", errors.New("the zip has no RPG Maker 2000/2003 game in it (no RPG_RT.ldb)")
}

// hbEasyRPGZipCheck reads a downloaded zip's names and finds the game.
func hbEasyRPGZipCheck(zipPath string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", errors.New("the download is not a readable zip")
	}
	defer zr.Close()
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
	}
	return hbEasyRPGZipRoot(names)
}

// hbEasyRPGFullPackage reports RPG_RT.ini's FullPackageFlag=1: the game
// carries every RTP file it uses. Pure.
func hbEasyRPGFullPackage(ini []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(ini, []byte("\xef\xbb\xbf"))))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "FullPackageFlag") {
			return strings.TrimSpace(value) == "1"
		}
	}
	return false
}

// easyrpgFindFile is dir's file named name in any case, or "".
func easyrpgFindFile(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// hbEasyRPGNeedsRTP reads the game's RPG_RT.ini (a few hundred bytes).
func hbEasyRPGNeedsRTP(gameDir string) bool {
	ini := easyrpgFindFile(gameDir, "RPG_RT.ini")
	if ini == "" {
		return true
	}
	f, err := os.Open(ini)
	if err != nil {
		return true
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
	return err != nil || !hbEasyRPGFullPackage(raw)
}

// ── The install ─────────────────────────────────────────────────────────

// hbEasyRPGRunJob installs or updates an RPG Maker game as a folder; the
// first steps (its id, the download key, the uploads list) are hbRunJob's.
func hbEasyRPGRunJob(ctx context.Context, j *hbJob, ups []hbUpload, gameID, keyID int64, cover string, prev hbInstallRec, hadPrev bool) (hbInstallRec, error) {
	c := hbEasyRPGIndex()
	if c < 0 {
		return hbInstallRec{}, errors.New("unknown console")
	}
	up, ok := hbEasyRPGPick(ups)
	if hadPrev && prev.UploadID > 0 {
		// An update keeps to the zip it installed while itch.io lists it.
		for _, u := range ups {
			if u.ID == prev.UploadID && hbRomExt(u.Filename) == ".zip" {
				up, ok = u, true
				break
			}
		}
	}
	if !ok {
		_, reason := hbNoCartridgeReason(ups, j.consoles|1<<uint(c))
		return hbInstallRec{}, errors.New(reason)
	}
	if up.Size > hbEasyRPGZipMax {
		return hbInstallRec{}, errors.New("the game is larger than Get Homebrew downloads (512 MB)")
	}
	dir, err := hbConsoleDir(c)
	if err != nil {
		return hbInstallRec{}, err
	}
	// Whether the folder the last install wrote is still ours, before the
	// new one takes its place.
	prevOurs := hadPrev && hbEasyRPGRec(prev) && hbRecFileOK(prev)

	if !hbWaitForBar() {
		return hbInstallRec{}, errors.New("another download is still running — try again when it finishes")
	}
	gen, card := installBarBeginRun(j.title)
	j.gen = gen
	rctx, rcancel := mcRunContext(gen, card)
	defer rcancel()
	fail := func(err error) (hbInstallRec, error) {
		if rctx.Err() != nil {
			installBarAbortRun(gen)
			return hbInstallRec{}, hbErrCancelled
		}
		installBarFailRun(gen, firstLine(err.Error()))
		return hbInstallRec{}, err
	}
	installBarUpdateRun(gen, installPhaseDownload, 0, up.Size)
	uuid := hbDownloadSession(rctx, j.key, gameID, keyID)

	incoming := filepath.Join(hbDataDir(), "incoming")
	_ = os.MkdirAll(incoming, 0o755)
	if err := pkgCheckFreeSpace(incoming, up.Size); err != nil {
		return fail(err)
	}
	zipPath := filepath.Join(incoming, strconv.FormatInt(gameID, 10)+".zip.part")
	defer os.Remove(zipPath)
	if err := hbDownloadUpload(rctx, gen, j.key, up.ID, keyID, uuid, zipPath, hbEasyRPGZipMax); err != nil {
		return fail(err)
	}
	installBarUpdateRun(gen, installPhaseExtract, 0, 0)
	root, err := hbEasyRPGZipCheck(zipPath)
	if err != nil {
		return fail(err)
	}
	// Unpacked beside its final place (one card, so the last step is a
	// rename), in a hidden folder the library's scan skips.
	stage := filepath.Join(dir, "."+hbHash(j.link)+".part")
	_ = os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if _, err := pkgExtractZip(rctx, zipPath, stage); err != nil {
		return fail(err)
	}
	gameDir := filepath.Join(stage, filepath.FromSlash(root))
	db := easyrpgFindFile(gameDir, easyrpgDBName)
	if db == "" || !isPathWithin(gameDir, stage) {
		return fail(errors.New("the game's RPG_RT.ldb did not unpack"))
	}
	note := ""
	if hbEasyRPGNeedsRTP(gameDir) {
		note = hbEasyRPGRTPNote
	}
	stem := strings.TrimSuffix(filepath.Base(up.Filename), filepath.Ext(up.Filename))
	target, err := hbEasyRPGTarget(dir, hbCleanFileName(j.title, stem), j.link, prev, hadPrev)
	if err != nil {
		return fail(err)
	}
	if err := hbEasyRPGSwap(gameDir, target, filepath.Join(dir, "."+hbHash(j.link)+".old")); err != nil {
		return fail(err)
	}
	if prevOurs && !strings.EqualFold(filepath.Dir(prev.Path), target) {
		// It moved (another Roms folder): the old copy goes, its saves stay.
		if derr := hbEasyRPGDelete(prev); derr != nil {
			logDebug("homebrew", "removing the old copy of %s: %v", j.title, derr)
		}
	}
	dest := filepath.Join(target, filepath.Base(db))
	var fileSize int64
	if info, serr := os.Stat(dest); serr == nil {
		fileSize = info.Size()
	}
	rec := hbInstallRec{
		Link: j.link, Title: j.title, Console: hbEasyRPGID, Path: dest,
		Cover:  hbSaveCoverFor(rctx, j.title, cover, dest, hbEasyRPGSystem, prev, hadPrev),
		GameID: gameID, UploadID: up.ID, KeyID: keyID, BuildID: up.BuildID,
		Size: up.Size, FileSize: fileSize, MD5: up.MD5, Updated: up.Updated, At: time.Now(), Note: note,
	}
	hbInstallsPut(rec, false)
	installBarDoneRun(gen)
	logInfo("homebrew", "%s installed in %s", j.title, filepath.Base(target))
	return rec, nil
}

// hbSaveCoverFor saves the game's cover where the library looks first,
// unless a cover is already there that the store did not put, and returns
// its path ("" when none is ours).
func hbSaveCoverFor(ctx context.Context, title, cover, dest, system string, prev hbInstallRec, hadPrev bool) string {
	root := libraryArtworkRoot()
	if root == "" || cover == "" {
		return ""
	}
	p := romArtworkPath(scannedROM{Path: dest, System: system}, root)
	if p == "" || (artworkCoverPresent(p) && !(hadPrev && prev.Cover == p)) {
		return ""
	}
	if err := hbSaveCover(ctx, cover, p); err != nil {
		logDebug("homebrew", "cover for %s: %v", title, err)
		if hadPrev && prev.Cover == p && artworkCoverPresent(p) {
			return p
		}
		return ""
	}
	return p
}

// hbEasyRPGTarget is the folder the game goes to: the one its last install
// wrote while that is still ours (or gone), else <name>, "<name> (2)" … — the
// first that does not exist, or that holds only the saves a Delete left.
// Another record's folder is never taken.
func hbEasyRPGTarget(dir, name, link string, prev hbInstallRec, hadPrev bool) (string, error) {
	if hadPrev && hbEasyRPGRec(prev) {
		old := filepath.Dir(prev.Path)
		_, lerr := os.Lstat(old)
		if strings.EqualFold(filepath.Dir(old), dir) && (hbRecFileOK(prev) || os.IsNotExist(lerr)) {
			return old, nil
		}
	}
	taken := map[string]bool{}
	for p := range hbInstallsTakenPaths(link) {
		taken[filepath.Dir(p)] = true // already lower case
	}
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, name)
		if i > 1 {
			cand = filepath.Join(dir, name+" ("+strconv.Itoa(i)+")")
		}
		// A console's folder name ("Saturn", "Arcade") cannot title a game
		// (romStemFolder), and Delete would refuse such a folder.
		if taken[strings.ToLower(cand)] || !isPathWithin(cand, dir) || romStemFolder(cand) == "" {
			continue
		}
		if _, err := os.Lstat(cand); os.IsNotExist(err) || hbEasyRPGOnlySaves(cand) {
			return cand, nil
		}
	}
	return "", errors.New("no free folder name for it in " + filepath.Base(dir))
}

// hbEasyRPGSwap puts the unpacked game at target. A folder already there (an
// update, or saves a Delete left) gives its saves to the new one first, steps
// aside, and is removed only once the new folder is in place; a failed rename
// puts it back. A swap cut short between its two renames (power lost) left
// the player's copy aside and no folder at target: its saves come along now,
// before the next swap would clear the aside folder.
func hbEasyRPGSwap(fresh, target, aside string) error {
	if _, err := os.Lstat(target); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		left := false
		if info, aerr := os.Lstat(aside); aerr == nil && info.IsDir() {
			if err := hbEasyRPGCarry(aside, fresh); err != nil {
				return err
			}
			left = true
		}
		if err := os.Rename(fresh, target); err != nil {
			return err
		}
		if left {
			_ = os.RemoveAll(aside)
		}
		return nil
	}
	if err := hbEasyRPGCarry(target, fresh); err != nil {
		return err
	}
	_ = os.RemoveAll(aside)
	if err := os.Rename(target, aside); err != nil {
		return err
	}
	if err := os.Rename(fresh, target); err != nil {
		_ = os.Rename(aside, target)
		return err
	}
	if err := os.RemoveAll(aside); err != nil {
		logDebug("homebrew", "removing the old game folder: %v", err)
	}
	return nil
}

// hbEasyRPGSaveName reports an EasyRPG save file (Save01.lsd …).
func hbEasyRPGSaveName(name string) bool {
	low := strings.ToLower(name)
	return strings.HasPrefix(low, "save") && strings.HasSuffix(low, ".lsd")
}

// hbEasyRPGCarry copies into fresh what the player's copy of the game has
// that the new one must keep: every save (the player's wins over one the
// author shipped), and any other small file the game wrote that the new
// version does not ship (a config, another save format).
func hbEasyRPGCarry(old, fresh string) error {
	entries, err := os.ReadDir(old)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		dst := filepath.Join(fresh, e.Name())
		if !hbEasyRPGSaveName(e.Name()) {
			if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				continue
			}
			if info, err := e.Info(); err != nil || info.Size() > hbEasyRPGCarryMax {
				continue
			}
		}
		if err := copyFile(filepath.Join(old, e.Name()), dst); err != nil {
			return err
		}
	}
	return nil
}

// hbEasyRPGOnlySaves reports a folder that holds nothing but EasyRPG saves
// (or nothing): what a Delete leaves.
func hbEasyRPGOnlySaves(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !hbEasyRPGSaveName(e.Name()) {
			return false
		}
	}
	return true
}

// hbEasyRPGDelete removes an installed game's folder but its saves (the
// folder stays while it holds any). Only a folder right inside an EasyRPG
// Roms folder is touched.
func hbEasyRPGDelete(r hbInstallRec) error {
	dir := filepath.Dir(r.Path)
	parent := filepath.Dir(dir)
	if s, _, ok := romFolderSystem(filepath.Base(parent), nil); !ok || s.Name != hbEasyRPGSystem || romStemFolder(dir) == "" {
		return errors.New("its folder is not where Get Homebrew put it")
	}
	// A link (or junction) put in the folder's place would aim the removal
	// at whatever it points to.
	if info, err := os.Lstat(dir); err == nil && info.Mode().Type() != os.ModeDir {
		return errors.New("its folder is not where Get Homebrew put it")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	kept := false
	for _, e := range entries {
		if e.Type().IsRegular() && hbEasyRPGSaveName(e.Name()) {
			kept = true
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	if kept {
		return nil
	}
	return os.Remove(dir)
}
