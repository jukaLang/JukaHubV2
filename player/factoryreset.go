package main

// factoryreset.go — Settings → System → Reset (REL-6).
//
// Two choices, each asked twice (both questions open on Cancel):
//
//   - "Reset settings" is the recovery card's reset (recoverysettings.go): a
//     safety backup of the settings, then the shipped settings and Home layout.
//     Games, saves, the library, media, themes and plugins stay. It runs live,
//     off the render thread, like a restore.
//   - "Erase everything JukaHub created" first copies every file the erase
//     removes (and the design file it puts back) into one archive,
//     JukaHub-erased-<device>-<time>.jkbackup, in the export folder — the
//     card's "JukaHub Backups" on the handheld, Downloads on a computer,
//     neither of which is erased. Nothing happens when that archive cannot be
//     written whole. The VPN is turned off, then a marker is left and JukaHub
//     restarts: the next start erases, in initCrashReports, before the config,
//     the stores or anything else is read. Erasing under a running session
//     would not stick: every store keeps a copy in memory and writes it back on
//     the way out.
//   - "Undo an erase" puts such an archive back the same way: a marker, a
//     restart, and the files written before anything is read. It writes only
//     names an erase removes, so an archive changed by hand cannot place a
//     program, a script or a marker.
//
// The erase moves everything into a trash folder (.erased-trash, in JukaHub's
// folder and in its data folder) first and keeps its marker until every move
// is done: a power cut half-way runs it again at the next start, which finds
// only what is left. The trash is deleted in the background, at every start
// until it is gone.
//
// What the erase removes is a fixed list (factoryErasePlan), never a walk of
// the card: the user files in JukaHub's folder (settings and their .bak,
// alarms, notes, synth songs, the shared cache), the stores at the top of the
// data folder (*.json, *.json.bak, *.json.tmp: library, history, playtime,
// trophies, accounts and tokens, caches) except the boot record and the
// installed-release stamp, the battery calibration and the VPN's DNS record,
// and a short list of data folders that hold only JukaHub's own state or
// caches. The design file is put back to the shipped one. Every other folder
// stays — ROMs, BIOS, saves of every emulator and
// game, Minecraft worlds, downloads, wallpapers, timelapses, screenshots,
// backups, logs, Version history, installed emulators and runtimes, themes,
// plugins and experience packs — and nothing outside JukaHub's folder and its
// data folder is ever touched.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// factoryEraseMarker, in JukaHub's folder, asks the next start to erase.
	factoryEraseMarker = ".factory-erase"
	factoryEraseToken  = "jukahub-factory-erase: 1"
	// factoryUndoMarker asks the next start to put an erase's archive back.
	factoryUndoMarker = ".factory-undo"
	factoryUndoToken  = "jukahub-factory-undo: 1"
	// factoryEraseTrash is where the erase moves what it removes, in JukaHub's
	// folder and in its data folder, until the background delete.
	factoryEraseTrash = ".erased-trash"
	// factoryEraseLabel marks the archive of what an erase removes.
	factoryEraseLabel = "erased"
	// factoryUndoSafetyPrefix names what an undo saves first, in the safety
	// backups; no pruner matches it.
	factoryUndoSafetyPrefix = "JukaHub-undo-safety-"
	// factoryWaitPIDEnv names the process a desktop restart came from: the new
	// one waits for it to end before it erases (recoverySpawn sets it).
	factoryWaitPIDEnv = "JUKAHUB_RESTARTED_FROM_PID"
)

// factoryEraseAppFiles are the user files JukaHub keeps beside itself.
var factoryEraseAppFiles = []string{
	"jukauser.json", "jukauser.json.bak", "jukauser.json.tmp",
	"jukaconfig.json.bak",
	"jukaalarm.json", "jukaalarm.json.bak",
	"notes.json", "notes.json.bak",
	synthStoreFile, synthStoreFile + ".bak",
	"jukahub.cache",
}

// factoryEraseDataDirs are the data folders that hold only JukaHub's own
// state or caches: input profiles, game metadata and per-game profiles,
// curation lists, snap previews, the speech and subtitle caches, performance
// and diagnostics records, bug reports and telemetry, the media index and its
// posters, the Port store's catalogue, the nearby-devices identity and the
// Moonlight pairing identity.
var factoryEraseDataDirs = []string{
	"input-profiles", "game-meta", "game-profiles", curationDirName, "game-snaps",
	"voice-cache", "subtitles", "performance", gameDiagLogDirName, "bugreports", "telemetry",
	"medialibrary", "ports-store", "peer", mlIdentityDirName,
}

// factoryEraseDataFiles are single files inside data folders that otherwise
// stay: the Steam sign-in and the downloaded cheat database (the cheats folder
// can also hold .cht files the user put there).
var factoryEraseDataFiles = []string{
	filepath.Join("steam", "account.json"),
	filepath.Join(cheatsDBDirName, cheatsDBZipName),
}

// factoryEraseKeepData are top-level data files the erase leaves: the boot
// guard's record (it counts starts, and the erase is one), the stamp that says
// which release is installed (updates and Version history read it), the
// battery calibration (it describes the device), and the VPN's copy of the
// DNS settings it replaced (the tunnel daemon puts them back from it after a
// power cut).
var factoryEraseKeepData = map[string]bool{
	"bootguard.json": true, "bootguard.json.bak": true,
	"release-info.json": true, "release-info.json.bak": true,
	batteryCalStateFile: true, batteryCalStateFile + ".bak": true, batteryCalStateFile + ".tmp": true,
	"vpn-resolv.json": true, "vpn-resolv.json.bak": true, "vpn-resolv.json.tmp": true,
}

// factoryEraseStoreFile reports whether a top-level data file is one of
// JukaHub's stores.
func factoryEraseStoreFile(name string) bool {
	lower := strings.ToLower(name)
	if factoryEraseKeepData[lower] {
		return false
	}
	return strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".json.bak") || strings.HasSuffix(lower, ".json.tmp")
}

// factoryEraseRootUnsafe reports whether dir is no place for the data-folder
// rules: empty, the top of a file system or drive, or the card's own top.
func factoryEraseRootUnsafe(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return true
	}
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) == clean {
		return true
	}
	if card := bkupCardRoot(); card != "" && sameFilePath(clean, filepath.Clean(card)) {
		return true
	}
	return false
}

// factoryErasePlan lists what the erase removes, as absolute paths, each
// inside appDir or dataDir and never one of them. Only what exists is listed.
// A data folder that is JukaHub's folder itself, or holds it, gets only the
// named folders: the pattern would reach the program's own files. A data
// folder that is a drive, the card or empty gets nothing.
func factoryErasePlan(appDir, dataDir string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(root, rel string) {
		if root == "" || rel == "" || pathHasParentComponent(rel) || filepath.IsAbs(rel) {
			return
		}
		p := filepath.Join(root, rel)
		if seen[p] || !isPathWithin(p, root) || filepath.Clean(p) == filepath.Clean(root) {
			return
		}
		if _, err := os.Lstat(p); err != nil {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if appDir != "" {
		for _, name := range factoryEraseAppFiles {
			add(appDir, name)
		}
	}
	if factoryEraseRootUnsafe(dataDir) {
		return out
	}
	for _, name := range factoryEraseDataDirs {
		if info, err := os.Lstat(filepath.Join(dataDir, name)); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			add(dataDir, name)
		}
	}
	for _, rel := range factoryEraseDataFiles {
		add(dataDir, rel)
	}
	if appDir != "" && isPathWithin(appDir, dataDir) {
		return out
	}
	if entries, err := os.ReadDir(dataDir); err == nil {
		for _, e := range entries {
			if e.Type().IsRegular() && factoryEraseStoreFile(e.Name()) {
				add(dataDir, e.Name())
			}
		}
	}
	return out
}

// factoryEraseRootOf is the folder of the two that p lies in (the data folder
// first: on the handheld it sits inside JukaHub's folder), "" when neither.
func factoryEraseRootOf(p, appDir, dataDir string) string {
	for _, root := range []string{dataDir, appDir} {
		if root != "" && isPathWithin(p, root) && filepath.Clean(p) != filepath.Clean(root) {
			return root
		}
	}
	return ""
}

// factoryEraseTrashDir makes sure root's trash folder is a real folder (a
// link or a file of that name is removed first) and returns it.
func factoryEraseTrashDir(root string) (string, error) {
	trash := filepath.Join(root, factoryEraseTrash)
	if info, err := os.Lstat(trash); err == nil {
		if info.IsDir() {
			return trash, nil
		}
		if err := os.Remove(trash); err != nil {
			return "", err
		}
	}
	if err := os.Mkdir(trash, 0o755); err != nil {
		return "", err
	}
	return trash, nil
}

// factoryEraseMove takes the planned paths away. Each is checked again
// against its root right before it goes and is moved into that root's trash
// folder — one rename, so a power cut never leaves a folder half removed; where
// it cannot be moved (another file system) it is removed in place. A folder
// goes with what is in it, a link only as the link.
func factoryEraseMove(plan []string, appDir, dataDir string) (removed int, failed []string) {
	for _, p := range plan {
		root := factoryEraseRootOf(p, appDir, dataDir)
		if root == "" {
			failed = append(failed, filepath.Base(p)+": outside JukaHub's folders")
			continue
		}
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if trash, terr := factoryEraseTrashDir(root); terr == nil {
			if dest, ok := factoryEraseFreeName(trash, filepath.Base(p), factoryEraseTrashTries); ok && os.Rename(p, dest) == nil {
				removed++
				continue
			}
		}
		if info.IsDir() {
			err = os.RemoveAll(p)
		} else {
			err = os.Remove(p)
		}
		if err != nil {
			failed = append(failed, filepath.Base(p)+": "+err.Error())
			continue
		}
		removed++
	}
	return removed, failed
}

// factoryEraseTrashTries is how many names factoryEraseFreeName tries.
const factoryEraseTrashTries = 1000

// factoryEraseFreeName is a name in trash that nothing has yet: base, then
// base-2, base-3 … — at most tries names. It gives up on any error but "not
// there" (a failing card, no permission, a name grown too long): this runs
// before anything is shown, at every start until the erase is done, so it
// must never loop for ever. The caller then removes the item in place.
func factoryEraseFreeName(trash, base string, tries int) (string, bool) {
	dest := filepath.Join(trash, base)
	for i := 2; i <= tries+1; i++ {
		_, err := os.Lstat(dest)
		if errors.Is(err, fs.ErrNotExist) {
			return dest, true
		}
		if err != nil {
			return "", false
		}
		dest = filepath.Join(trash, base+"-"+strconv.Itoa(i))
	}
	return "", false
}

// factoryEraseEmptyTrash deletes what an erase moved aside, in the background
// once the start has done its own reading; every start tries again until the
// trash is gone. A trash folder that is a link goes only as the link.
func factoryEraseEmptyTrash(appDir, dataDir string) {
	var trashes []string
	for _, root := range []string{dataDir, appDir} {
		if root == "" {
			continue
		}
		trash := filepath.Join(root, factoryEraseTrash)
		if _, err := os.Lstat(trash); err == nil && (len(trashes) == 0 || !sameFilePath(trashes[0], trash)) {
			trashes = append(trashes, trash)
		}
	}
	if len(trashes) == 0 {
		return
	}
	go func() {
		time.Sleep(30 * time.Second)
		for _, trash := range trashes {
			if err := os.RemoveAll(trash); err != nil {
				logWarn("reset", "erased files not deleted yet (next start tries again): %v", err)
			}
		}
	}()
}

// factoryEraseSweepParts deletes, in the background, what a power cut or a
// kill left half-written: an undo file's temporary copies (.backup-*.part,
// .jukahub-cache-*.part) in the export folder and the safety backups, and an
// undo's (.undo-*.part) beside JukaHub and at the top of its data folder.
// They hold sign-ins. Only files untouched for an hour go, so a backup being
// written right now is left alone.
func factoryEraseSweepParts(appDir, dataDir string) {
	go func() {
		time.Sleep(30 * time.Second)
		for _, dir := range []string{bkupExportDir(), bkupSafetyDir(), appDir, dataDir} {
			if dir == "" {
				continue
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if !e.Type().IsRegular() || !strings.HasSuffix(name, ".part") ||
					!(strings.HasPrefix(name, ".backup-") || strings.HasPrefix(name, ".jukahub-cache-") || strings.HasPrefix(name, ".undo-")) {
					continue
				}
				if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
					if err := os.Remove(filepath.Join(dir, name)); err == nil {
						logInfo("reset", "removed a half-written %s", name)
					}
				}
			}
		}
	}()
}

// factoryEraseNote is what the start that erased says once Home is up
// (recoveryCardOpenAtBoot); "" when it did not erase.
var factoryEraseNote string

// factoryEraseAtStart carries out an erase, or an undo, the last session
// asked for, and empties the trash of an earlier erase. main reaches it
// through initCrashReports, before the config or any store is read.
func factoryEraseAtStart() {
	appDir, dataDir := bkupAppDir(), bkupDataDir()
	eraseMarker := filepath.Join(appDir, factoryEraseMarker)
	undoMarker := filepath.Join(appDir, factoryUndoMarker)
	erase, eraseErr := os.ReadFile(eraseMarker)
	undo, undoErr := os.ReadFile(undoMarker)
	if eraseErr == nil || undoErr == nil {
		factoryWaitForOldProcess()
	}
	if eraseErr == nil {
		var ready error
		ours := strings.HasPrefix(string(erase), factoryEraseToken)
		if ours {
			ready = factoryEraseArchiveReady(erase)
		}
		switch {
		case !ours:
			factoryMarkerArmed(eraseMarker, erase, factoryEraseToken) // removes and logs a marker JukaHub did not write
		case ready != nil:
			// Never erase what no undo file holds.
			logWarn("reset", "erase not carried out: %v", ready)
			factoryEraseNote = "Nothing was erased: " + firstLine(ready.Error())
			factoryMarkerDone(eraseMarker)
		case !factoryMarkerArmed(eraseMarker, erase, factoryEraseToken):
			factoryEraseNote = "Nothing was erased: its marker could not be changed (see the log)"
		default:
			factoryEraseNow(appDir, dataDir, erase)
			factoryMarkerDone(eraseMarker)
		}
	}
	if undoErr == nil {
		ours := strings.HasPrefix(string(undo), factoryUndoToken)
		if factoryMarkerArmed(undoMarker, undo, factoryUndoToken) {
			factoryUndoNow(appDir, dataDir, undo)
			factoryMarkerDone(undoMarker)
		} else if ours {
			factoryEraseNote = "The erase was not undone: its marker could not be changed (see the log)"
		}
	}
	factoryEraseEmptyTrash(appDir, dataDir)
	factoryEraseSweepParts(appDir, dataDir)
}

// factoryEraseArchiveReady checks that the undo file an erase marker names is
// there and is this platform's undo file of an erase (its manifest only: the
// whole file was hashed when it was written and read back).
func factoryEraseArchiveReady(marker []byte) error {
	path := factoryMarkerValue(marker, "archive")
	if path == "" {
		return errors.New("the request names no undo file")
	}
	m, err := bkupReadManifest(path)
	if err != nil {
		if _, serr := os.Stat(path); errors.Is(serr, fs.ErrNotExist) {
			return fmt.Errorf("its undo file %s is gone", filepath.Base(path))
		}
		return fmt.Errorf("%s %v", filepath.Base(path), err)
	}
	if m.Label != factoryEraseLabel || m.Platform != P().Name() {
		return fmt.Errorf("%s is not this device's undo file of an erase", filepath.Base(path))
	}
	return nil
}

// factoryMarkerArmed reports whether a marker is JukaHub's and can be taken
// away afterwards: it is written again in place first, so a marker that could
// not be removed when the work is done never makes every start repeat it. A
// marker JukaHub did not write is removed and ignored.
func factoryMarkerArmed(marker string, data []byte, token string) bool {
	if !strings.HasPrefix(string(data), token) {
		logWarn("reset", "ignored a %s that JukaHub did not write", filepath.Base(marker))
		_ = os.Remove(marker)
		return false
	}
	if err := AtomicWrite(marker, data, 0o644); err != nil {
		logWarn("reset", "%s not carried out: its marker cannot be changed: %v", filepath.Base(marker), err)
		return false
	}
	return true
}

// factoryMarkerDone takes a marker away once its work is done. Until then a
// power cut runs the work again at the next start, which is harmless: the
// erase finds only what is left, the undo writes the same files again. It
// reports an error only when the marker is still armed.
func factoryMarkerDone(marker string) error {
	if err := os.Remove(marker); err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// A marker without the token is ignored by the next start.
	if err := AtomicWrite(marker, []byte("done\n"), 0o644); err != nil {
		logError("reset", "%s could not be cleared and may run again: %v", filepath.Base(marker), err)
		return err
	}
	return nil
}

// factoryMarkerValue is a "key: value" line of a marker.
func factoryMarkerValue(data []byte, key string) string {
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// factoryWaitForOldProcess waits, on a desktop, up to five seconds for the
// process that restarted this one to end: a late save of its own could
// otherwise bring back a file the erase just took away. The handheld's
// launcher starts JukaHub again only after the old one has exited.
func factoryWaitForOldProcess() {
	pid, _ := strconv.Atoi(strings.TrimSpace(os.Getenv(factoryWaitPIDEnv)))
	if !IsWindows() || pid <= 0 || pid == os.Getpid() {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return // already gone
	}
	done := make(chan struct{})
	go func() {
		_, _ = proc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		logWarn("reset", "the previous JukaHub (process %d) has not ended after 5 s; going on", pid)
	}
}

// factoryEraseNow is the erase itself (before anything is read).
func factoryEraseNow(appDir, dataDir string, marker []byte) {
	plan := factoryErasePlan(appDir, dataDir)
	removed, failed := factoryEraseMove(plan, appDir, dataDir)
	logInfo("reset", "erased everything JukaHub created: %d removed from %s and %s", removed, appDir, dataDir)
	for _, f := range failed {
		logWarn("reset", "not erased: %s", f)
	}
	// The design file goes back to the shipped one; the user settings were
	// erased and start from their defaults.
	if _, shipped := cfgdocShippedBytes(); len(shipped) > 0 {
		if _, err := recoveryInstallDesign(appDir, shipped); err != nil {
			logWarn("reset", "the shipped layout could not be put back: %v", err)
		}
	}
	factoryEraseNote = "JukaHub was erased"
	if archive := filepath.Base(factoryMarkerValue(marker, "archive")); archive != "" && archive != "." {
		factoryEraseNote += " · undo file " + archive
	}
	if len(failed) > 0 {
		factoryEraseNote += fmt.Sprintf(" · %d left (see the log)", len(failed))
	}
}

// ── The undo file: everything an erase takes away ──────────────────────────

// factoryUndoAllowed reports whether an undo may write target: only a name an
// erase takes away, or the design file it puts back. An archive changed by
// hand can therefore never place a program, a script, launch.sh or a marker.
func factoryUndoAllowed(token, rel string) bool {
	switch token {
	case "app":
		if rel == "jukaconfig.json" {
			return true
		}
		for _, name := range factoryEraseAppFiles {
			if rel == name {
				return true
			}
		}
	case "data":
		first, rest, nested := strings.Cut(rel, "/")
		if !nested {
			return factoryEraseStoreFile(first)
		}
		for _, name := range factoryEraseDataFiles {
			if rel == filepath.ToSlash(name) {
				return true
			}
		}
		for _, dir := range factoryEraseDataDirs {
			if first == dir && rest != "" {
				return true
			}
		}
	}
	return false
}

// factoryEraseTarget is where a file the erase takes away goes in the
// archive: "app:<name>" beside JukaHub, "data:<path>" in the data folder.
// ok is false for a file the archive cannot name.
func factoryEraseTarget(abs, appDir, dataDir string) (string, bool) {
	if appDir != "" && sameFilePath(filepath.Dir(abs), appDir) && factoryUndoAllowed("app", filepath.Base(abs)) {
		return "app:" + filepath.Base(abs), true
	}
	if dataDir == "" || !isPathWithin(abs, dataDir) {
		return "", false
	}
	rel, err := filepath.Rel(dataDir, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if _, clean, err := bkupSplitTarget("data:" + rel); err != nil || clean != rel {
		return "", false
	}
	return "data:" + rel, true
}

// factoryEraseItems lists, as archive items, every file the plan takes away —
// each file inside a planned folder too — and the design file the erase puts
// back to the shipped one. Links are left out: the erase removes only the
// link, never what it points to. A file that cannot be read or named stops it.
func factoryEraseItems(plan []string, appDir, dataDir string) ([]bkupItem, error) {
	var items []bkupItem
	add := func(abs string, info fs.FileInfo) error {
		target, ok := factoryEraseTarget(abs, appDir, dataDir)
		if !ok {
			return fmt.Errorf("%s cannot be saved under its name", abs)
		}
		cat := bkupCatLibrary
		if strings.HasPrefix(target, "app:") {
			cat = bkupCatSettings
		}
		items = append(items, bkupItem{Abs: abs, Target: target, Category: cat, Size: info.Size(), Platform: true, ModTime: info.ModTime()})
		return nil
	}
	if appDir != "" {
		design := filepath.Join(appDir, "jukaconfig.json")
		if info, err := os.Lstat(design); err == nil && info.Mode().IsRegular() {
			if err := add(design, info); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range plan {
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() {
			if err := add(p, info); err != nil {
				return nil, err
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // gone since it was listed
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			return add(path, fi)
		})
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// factoryEraseCacheCopy copies the shared cache, which this session keeps
// open, through the database itself (a consistent copy) into tmp. It reports
// false when the cache is not open here: the file is then copied as it is.
func factoryEraseCacheCopy(src, tmp string) (bool, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return false, nil
	}
	sharedCacheMu.Lock()
	db, path := sharedCacheDB, sharedCachePath
	sharedCacheMu.Unlock()
	if db == nil || !sameFilePath(path, abs) {
		return false, nil
	}
	return true, db.View(func(tx *bolt.Tx) error { return tx.CopyFile(tmp, 0o600) })
}

// factoryEraseBackup copies everything the erase would take away now into one
// archive in dir, prefix<device>-<time>.jkbackup, labelled as an erase's undo
// file. There is no size limit but the free space: the whole archive is
// written, or an error says why not and nothing is left under its name.
func factoryEraseBackup(ctx context.Context, dir, prefix, appDir, dataDir, version string) (string, error) {
	items, err := factoryEraseItems(factoryErasePlan(appDir, dataDir), appDir, dataDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for i, it := range items {
		if it.Target != "app:jukahub.cache" {
			continue
		}
		tmp := filepath.Join(dir, ".jukahub-cache-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".part")
		defer os.Remove(tmp)
		copied, err := factoryEraseCacheCopy(it.Abs, tmp)
		if err != nil {
			return "", fmt.Errorf("the shared cache could not be copied: %w", err)
		}
		if info, err := os.Stat(tmp); copied && err == nil {
			items[i].Abs, items[i].Size = tmp, info.Size()
		}
	}
	var total, listing int64
	for _, it := range items {
		total += it.Size
		listing += 2*int64(len(it.Target)) + 280 // its line in the manifest
	}
	if listing > bkupMaxManifestBytes {
		return "", fmt.Errorf("%d files are more than one undo file can list", len(items))
	}
	need := total + 32<<20
	if free, err := freeBytes(dir); err == nil && free < need {
		return "", fmt.Errorf("%s has %s free; the undo file needs about %s", dir, humanBytes(free), humanBytes(need))
	}
	now := time.Now()
	m := bkupManifest{
		Format:     bkupFormat,
		Version:    bkupVersion,
		CreatedAt:  now.Format(time.RFC3339),
		AppVersion: version,
		Platform:   P().Name(),
		Device:     string(DetectDevice()),
		DeviceName: deviceDisplayName(DetectDevice()),
		Label:      factoryEraseLabel,
		Roots:      bkupHereRoots(),
		Categories: []string{bkupCatSettings, bkupCatLibrary},
	}
	path := bkupUniquePath(dir, prefix+bkupDeviceSlug()+"-"+now.Format("20060102-150405")+bkupExt)
	if _, err := bkupWrite(ctx, path, items, nil, m, nil); err != nil {
		return "", err
	}
	// Every file was hashed as it was written; what is left to prove is that
	// the undo file opens again.
	if _, err := bkupReadManifest(path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("the undo file does not read back: %v", err)
	}
	return path, nil
}

// factoryUndoNoLinks reports whether every folder between root and dest that
// exists is a real folder, not a link that would carry the write elsewhere.
func factoryUndoNoLinks(root, dest string) bool {
	rel, err := filepath.Rel(root, filepath.Dir(dest))
	if err != nil || rel == "." {
		return err == nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return errors.Is(err, fs.ErrNotExist)
		}
		// A Windows junction reads as a folder that is also irregular.
		if !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return false
		}
	}
	return true
}

// factoryUndoWriteFile writes one archive entry to dest through a temporary
// file beside it, checked against its size and checksum before it replaces
// dest. It streams: no file is held in memory whole.
func factoryUndoWriteFile(f *zip.File, dest string, size int64, sum string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(dir, ".undo-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(rc, size+1))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum)) {
		err = errors.New("does not match its checksum")
	}
	if err == nil {
		err = os.Rename(tmpName, dest)
	}
	if err != nil {
		_ = os.Remove(tmpName)
	}
	return err
}

// factoryUndoWriteArchive checks an erase's undo file whole, then writes back
// every file in it that an erase takes away; anything else in it is left out.
func factoryUndoWriteArchive(path, appDir, dataDir string) (written, skipped int, err error) {
	if path == "" {
		return 0, 0, errors.New("no undo file was named")
	}
	m, err := bkupVerify(path)
	if err != nil {
		return 0, 0, fmt.Errorf("%s %v", filepath.Base(path), err)
	}
	if m.Label != factoryEraseLabel {
		return 0, 0, fmt.Errorf("%s is not the undo file of an erase", filepath.Base(path))
	}
	if m.Platform != P().Name() {
		return 0, 0, fmt.Errorf("%s was made on %s", filepath.Base(path), bkupPlatformLabel(m.Platform))
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, 0, err
	}
	defer zr.Close()
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	for _, bf := range m.Files {
		token, rel, err := bkupSplitTarget(bf.Target)
		root := appDir
		if token == "data" {
			root = dataDir
			// The same guards as factoryErasePlan: nothing in an unsafe data
			// folder, no top-level store where the data folder holds JukaHub.
			if factoryEraseRootUnsafe(dataDir) ||
				(appDir != "" && isPathWithin(appDir, dataDir) && !strings.Contains(rel, "/")) {
				root = ""
			}
		}
		if err != nil || root == "" || !factoryUndoAllowed(token, rel) {
			skipped++
			logWarn("reset", "undo left out %s: not a file an erase takes away", bf.Target)
			continue
		}
		dest, err := resolveWithinRoot(root, filepath.FromSlash(rel))
		if err == nil && !factoryUndoNoLinks(root, dest) {
			err = errors.New("a folder on its way is a link")
		}
		f := entries[bf.Path]
		if err == nil && f == nil {
			err = errors.New("missing from the archive")
		}
		if err == nil {
			err = factoryUndoWriteFile(f, dest, bf.Size, bf.SHA256)
		}
		if err != nil {
			skipped++
			logWarn("reset", "undo did not write %s: %v", bf.Target, err)
			continue
		}
		written++
	}
	return written, skipped, nil
}

// factoryUndoNow puts an erase's undo file back (before anything is read).
func factoryUndoNow(appDir, dataDir string, marker []byte) {
	path := factoryMarkerValue(marker, "archive")
	written, skipped, err := factoryUndoWriteArchive(path, appDir, dataDir)
	if err != nil {
		logWarn("reset", "erase not undone: %v", err)
		factoryEraseNote = "The erase was not undone: " + firstLine(err.Error())
		return
	}
	logInfo("reset", "erase undone from %s: %d files written, %d left out", path, written, skipped)
	factoryEraseNote = fmt.Sprintf("The erase was undone · %d files back", written)
	if skipped > 0 {
		factoryEraseNote += fmt.Sprintf(" · %d left out (see the log)", skipped)
	}
}

// ── The Reset card ──────────────────────────────────────────────────────────

// factoryResetOpen is the Reset choice (Settings → About → Reset).
func factoryResetOpen(config *Config) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Reset",
		subtitle: "Nothing changes until you confirm",
		icon:     "refresh",
		body: "Reset settings puts the settings and Home layout back to how JukaHub ships. Erase everything also clears " +
			"JukaHub's library, history, accounts and caches, and saves all of it in an undo file first.",
		options: []recoveryOption{
			{icon: "refresh", title: "Reset settings", detail: "Games, saves, library, media, themes and plugins stay",
				run: factoryAskReset},
			{icon: "warning", title: "Erase everything JukaHub created", detail: "ROMs, BIOS, saves and your media stay · restarts",
				run: factoryAskErase},
			{icon: "backup", title: "Undo an erase", detail: "Puts back what an erase took away, from its undo file · restarts",
				run: factoryAskUndo},
		},
	})
}

// factoryAsk is one confirmation; its first row, the one focused, is Cancel.
func factoryAsk(title, subtitle, icon, body, goTitle, goDetail string, run func(*Config)) {
	back := func(c *Config) { factoryResetOpen(c) }
	recoveryCardShow(recoveryCardSpec{
		title:    title,
		subtitle: subtitle,
		icon:     icon,
		body:     body,
		options: []recoveryOption{
			{icon: "close", title: "Cancel", detail: "Nothing changes", run: back},
			{icon: icon, title: goTitle, detail: goDetail, run: run},
		},
		back:     back,
		backText: "Cancel",
	})
}

// factoryAskReset is the first question before resetting the settings.
func factoryAskReset(config *Config) {
	factoryAsk("Reset settings?", "Games and saves are kept", "refresh",
		"Settings and the Home layout go back to how JukaHub ships. Games, saves, favourites, play history, media, "+
			"themes and plugins stay.\n\nYour current settings are saved first as a safety backup "+
			"(Tools → Your setup → Backup & restore), so this can be undone.",
		"Continue", "One more question follows",
		func(*Config) {
			factoryAsk("Reset settings now?", "Second check", "refresh",
				"Last question: your settings and Home layout are backed up, then reset.",
				"Reset settings", "Backs up the current settings, then resets them",
				func(c *Config) {
					appDir, version := bkupAppDir(), versionString(c)
					recoveryRunSettings(c, "Resetting your settings…", true, func(ctx context.Context) bkupRestoreResult {
						return recoveryResetSettings(ctx, appDir, version)
					})
				})
		})
}

// factoryEraseSummary says what goes and what stays.
const factoryEraseSummary = "Erased: settings and Home layout, alarms, notes and synth songs, favourites, recents, play " +
	"history and playtime, trophies, collections, input and game profiles, accounts and sign-ins (Steam, Google Drive, " +
	"RetroAchievements, PlayStation, mail, VPN), paired devices and JukaHub's caches.\n\n" +
	"Kept: ROMs, BIOS, every game and emulator save, Minecraft worlds, downloads, wallpapers, screenshots, " +
	"timelapses, box art, backups, logs, Version history, installed emulators and runtimes, themes, plugins and packs."

// factoryUndoWhere says where the undo file goes and how it is used.
func factoryUndoWhere() string {
	return "First, every file the erase takes away is copied into one undo file, JukaHub-erased-….jkbackup, in " +
		bkupExportDir() + ". Settings → About → Reset → Undo an erase puts it all back. It holds your sign-ins " +
		"too: keep it private, and delete it before you give the device away."
}

// factoryAskErase is the first question before erasing.
func factoryAskErase(config *Config) {
	factoryAsk("Erase everything JukaHub created?", "Games, saves and media are kept", "warning",
		factoryEraseSummary+"\n\n"+factoryUndoWhere(),
		"Continue", "One more question follows",
		func(*Config) {
			factoryAsk("Erase now?", "Second check · JukaHub restarts", "warning",
				"Last question. JukaHub writes the undo file, turns the VPN off, restarts and erases before it "+
					"loads anything. If the undo file cannot be written whole, nothing is erased. To undo it later: "+
					"Settings → About → Reset → Undo an erase.",
				"Erase everything", "Saves the undo file, restarts and erases",
				factoryRunErase)
		})
}

// factoryRunErase writes the undo file, turns the VPN off and leaves the
// marker, off the render thread, then restarts.
func factoryRunErase(config *Config) {
	// A settings save still waiting lands first, so the undo file has it.
	flushPendingUserConfig()
	recoveryCardShow(recoveryCardSpec{
		title:    "Working",
		subtitle: "Writing the undo file…",
		icon:     "backup",
		body: "Copying everything the erase takes away before anything is erased. This can take a while; " +
			"JukaHub restarts when it is done.",
		busy:   true,
		pinned: true,
	})
	gen := recoveryCard.gen
	version := versionString(config)
	appDir, dataDir, dir := bkupAppDir(), bkupDataDir(), bkupExportDir()
	go func() {
		path, err := factoryEraseBackup(context.Background(), dir, "JukaHub-erased-", appDir, dataDir, version)
		if err != nil {
			err = fmt.Errorf("the undo file could not be written, so nothing was erased: %w", err)
		}
		vpnWas := false
		if err == nil {
			vpnWas, err = factoryEraseStopVPN()
		}
		if err == nil {
			err = AtomicWrite(filepath.Join(appDir, factoryEraseMarker),
				[]byte(factoryEraseToken+"\narchive: "+path+"\n"), 0o644)
			if err != nil {
				err = fmt.Errorf("the erase could not be scheduled: %w", err)
			}
		}
		if err != nil && path != "" {
			// Not needed after all, and it holds the sign-ins.
			_ = os.Remove(path)
		}
		tbxPost(func() { factoryEraseReady(path, err, gen, vpnWas) })
	}()
}

// factoryEraseStopVPN holds the tunnel down until factoryVPNBack, whatever
// the switch says (a settings save cannot bring it back), and makes sure it
// is down before the erase removes its profile, so the DNS settings it
// replaced go back now. On the handheld the VPN worker takes it down and this
// waits for it: the worker stays the tunnel's only owner. On Windows, where
// the worker acts only on a user's switch, this stops it, and looks again in
// case an apply the worker had already begun lands after the stop. It
// reports whether a tunnel the switch wanted was taken down, to start it
// again if the erase is called off. Off the render thread.
func factoryEraseStopVPN() (bool, error) {
	vpnMgr.mu.Lock()
	on, worker, looks := vpnMgr.enabled, vpnMgr.started, vpnMgr.looks
	vpnMgr.held = true
	vpnMgr.mu.Unlock()
	if vpnPlatformAuto() && worker {
		return on, factoryEraseWaitVPN(looks)
	}
	// Windows' probe keeps no state, so it may run beside the worker's; on
	// the handheld there is no worker to race with here.
	stopped := false
	for try := 0; ; try++ {
		if p := vpnPlatformProbe(); !p.Running && !p.Installed {
			return on && stopped, nil
		}
		if try == 3 {
			return on && stopped, errors.New("the VPN kept starting again, so nothing was erased")
		}
		if err := vpnStop(); err != nil {
			return on && stopped, fmt.Errorf("the VPN could not be turned off, so nothing was erased: %w", err)
		}
		stopped = true
		time.Sleep(2 * time.Second)
	}
}

// factoryEraseWaitVPN waits, up to 20 s, until the VPN worker has looked at
// the tunnel twice since the hold — the second look comes after any apply it
// had already begun, and after its own stop — and found it down with nothing
// pending.
func factoryEraseWaitVPN(since uint64) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		vpnWake()
		time.Sleep(250 * time.Millisecond)
		vpnMgr.mu.Lock()
		looks, p := vpnMgr.looks, vpnMgr.probe
		idle := vpnMgr.applying.IsZero() && vpnMgr.stopping.IsZero()
		vpnMgr.mu.Unlock()
		if looks >= since+2 && !p.Running && !p.Installed && idle {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the VPN could not be turned off, so nothing was erased")
		}
	}
}

// factoryVPNBack releases the hold after an erase that did not go ahead, and
// starts the tunnel the erase took down again (main thread). explicit: on
// Windows the worker starts a tunnel only on a user's action.
func factoryVPNBack(restart bool) {
	vpnMgr.mu.Lock()
	vpnMgr.held = false
	vpnMgr.mu.Unlock()
	if restart && appConfig != nil {
		vpnPublish(appConfig, true)
		return
	}
	vpnWake()
}

// factoryRestartInto restarts into the work a marker asks for and says what
// really happens; where JukaHub cannot start itself again it asks to be
// closed, with a way to call the work off (main thread). gen is the Working
// card's: that card is pinned, but should anything have replaced it, the work
// is called off rather than JukaHub restarting under the user.
func factoryRestartInto(marker, does, saved, stopTitle string, gen int, calledOff func()) {
	// The card stays up while the work is still armed.
	callOff := func(*Config) {
		if factoryCallOff(marker, calledOff) {
			recoveryCardClose()
		}
	}
	if !recoveryCard.open || recoveryCard.gen != gen {
		logWarn("reset", "the Working card was replaced before %s was ready; called off", marker)
		factoryCallOff(marker, calledOff)
		return
	}
	if recoveryRestartKind() != "" {
		recoveryCardClose()
		if recoveryRestartApp() {
			ShowToast(saved+" · JukaHub restarts and "+does, ToastKindInfo)
		} else {
			logWarn("reset", "JukaHub could not arrange its own restart; it %s when it is opened again", does)
			ShowToast(saved+" · JukaHub closes: open it again and it "+does, ToastKindWarning)
		}
		return
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Close JukaHub to finish",
		subtitle: saved,
		icon:     "warning",
		body:     "JukaHub " + does + " the next time it starts, before it loads anything. Close it now and open it again.",
		options: []recoveryOption{
			{icon: "close", title: stopTitle, detail: "Calls it off; the saved file stays", run: callOff},
			{icon: "power", title: "Close JukaHub", detail: "Open it again to finish",
				run: func(*Config) {
					recoveryCardClose()
					recoveryRestartApp()
				}},
		},
		// Leaving the card any other way must not leave the work armed: the
		// next start would take everything done since.
		back:     callOff,
		backText: stopTitle,
	})
}

// factoryCallOff takes a marker away so the next start does not act on it,
// and says whether that worked (main thread: one small file, taken away at
// once, so a late remove can never take a later request's marker). calledOff
// runs only when the work is really off; the result says whether it is.
func factoryCallOff(marker string, calledOff func()) bool {
	if err := factoryMarkerDone(filepath.Join(bkupAppDir(), marker)); err != nil {
		ShowToast("Could not call it off: "+firstLine(err.Error())+" · it still runs at the next start", ToastKindWarning)
		PlayErrorSound()
		return false
	}
	if calledOff != nil {
		calledOff()
	}
	ShowToast("Called off: nothing happens at the next start · the saved file stays", ToastKindInfo)
	return true
}

// factoryStopped says why an erase or an undo did not go ahead (main thread).
func factoryStopped(title, what string, err error, gen int) {
	logWarn("reset", "%s stopped: %v", what, err)
	if !recoveryCard.open || recoveryCard.gen != gen {
		ShowToast(title+": "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	recoveryCardShow(recoveryCardSpec{
		title: title, subtitle: "Nothing was changed", icon: "warning",
		body:    firstLine(err.Error()) + ".",
		options: []recoveryOption{{icon: "check", title: "OK", detail: "Back to JukaHub", run: func(*Config) { recoveryCardClose() }}},
	})
	PlayErrorSound()
}

// factoryEraseReady restarts into the erase, or says why it stopped (main
// thread).
func factoryEraseReady(archive string, err error, gen int, vpnWas bool) {
	if err != nil {
		factoryVPNBack(vpnWas)
		factoryStopped("Nothing was erased", "erase", err, gen)
		return
	}
	logInfo("reset", "undo file before the erase: %s; erasing at the next start", archive)
	factoryRestartInto(factoryEraseMarker, "erases", "Undo file saved as "+filepath.Base(archive), "Don't erase", gen,
		func() { factoryVPNBack(vpnWas) })
}

// factoryAskUndo looks for undo files off the render thread (the backup
// places: the export folder, the card, Downloads, the safety backups), then
// offers the three newest.
func factoryAskUndo(config *Config) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Working",
		subtitle: "Looking for undo files…",
		icon:     "backup",
		busy:     true,
		backText: "Hide",
	})
	gen := recoveryCard.gen
	go func() {
		var found []bkupListItem
		for _, it := range bkupList() {
			if it.Problem == "" && it.Manifest.Label == factoryEraseLabel {
				found = append(found, it)
				if len(found) == 3 {
					break
				}
			}
		}
		tbxPost(func() { factoryUndoChoose(found, gen) })
	}()
}

// factoryUndoChoose offers the undo files found (main thread).
func factoryUndoChoose(found []bkupListItem, gen int) {
	if !recoveryCard.open || recoveryCard.gen != gen {
		return
	}
	back := func(c *Config) { factoryResetOpen(c) }
	if len(found) == 0 {
		recoveryCardShow(recoveryCardSpec{
			title: "Nothing to undo", subtitle: "No undo file was found", icon: "backup",
			body: "An erase saves what it takes away as JukaHub-erased-….jkbackup in " + bkupExportDir() +
				". None was found there, at the top of the card, in Downloads or among the safety backups.",
			options:  []recoveryOption{{icon: "close", title: "Back", detail: "To the Reset choice", run: back}},
			back:     back,
			backText: "Back",
		})
		return
	}
	options := []recoveryOption{{icon: "close", title: "Cancel", detail: "Nothing changes", run: back}}
	for _, it := range found {
		path, name := it.Path, it.Name
		options = append(options, recoveryOption{icon: "backup", title: "Put back " + formatDateTime(nil, it.ModTime),
			detail: fmt.Sprintf("%s · %d files", name, len(it.Manifest.Files)),
			run: func(*Config) {
				factoryAsk("Undo the erase?", "JukaHub restarts", "backup",
					"JukaHub restarts and, before it loads anything, writes back every file in "+name+
						": settings, library, history, trophies, sign-ins and the rest. Files made since the erase "+
						"with the same names are replaced; what is there now is saved first in "+bkupSafetyDir()+".",
					"Undo the erase", "Saves what is there now, restarts and puts the files back",
					func(c *Config) { factoryRunUndo(c, path) })
			}})
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Undo an erase",
		subtitle: "Pick the erase to undo",
		icon:     "backup",
		body:     "Each erase left an undo file with everything it took away.",
		options:  options,
		back:     back,
		backText: "Cancel",
	})
}

// factoryRunUndo checks the undo file, saves what the undo would replace and
// leaves the marker, off the render thread, then restarts.
func factoryRunUndo(config *Config, archive string) {
	flushPendingUserConfig()
	recoveryCardShow(recoveryCardSpec{
		title:    "Working",
		subtitle: "Checking the undo file…",
		icon:     "backup",
		body: "Checking every file in it, then saving what is there now. This can take a while; " +
			"JukaHub restarts when it is done.",
		busy:   true,
		pinned: true,
	})
	gen := recoveryCard.gen
	version := versionString(config)
	appDir, dataDir, safety := bkupAppDir(), bkupDataDir(), bkupSafetyDir()
	go func() {
		m, err := bkupVerify(archive)
		switch {
		case err != nil:
			err = fmt.Errorf("%s %v", filepath.Base(archive), err)
		case m.Label != factoryEraseLabel:
			err = fmt.Errorf("%s is not the undo file of an erase", filepath.Base(archive))
		case m.Platform != P().Name():
			err = fmt.Errorf("%s was made on %s", filepath.Base(archive), bkupPlatformLabel(m.Platform))
		}
		if err == nil {
			// Never pruned: the file being put back may be an older one of
			// these. Its own prefix keeps it out of the keep-3 pruning of the
			// settings safety backups (JukaHub-safety-…), both ways.
			if _, err = factoryEraseBackup(context.Background(), safety, factoryUndoSafetyPrefix, appDir, dataDir, version); err != nil {
				err = fmt.Errorf("what is there now could not be saved first: %w", err)
			}
		}
		if err == nil {
			err = AtomicWrite(filepath.Join(appDir, factoryUndoMarker),
				[]byte(factoryUndoToken+"\narchive: "+archive+"\n"), 0o644)
		}
		tbxPost(func() {
			if err != nil {
				factoryStopped("Nothing was undone", "undo", err, gen)
				return
			}
			logInfo("reset", "undoing the erase from %s at the next start", archive)
			factoryRestartInto(factoryUndoMarker, "puts the erased files back", "Ready to undo", "Don't undo", gen, nil)
		})
	}()
}
