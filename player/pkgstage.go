package main

// pkgstage.go — installing a package without ever leaving a mix of versions.
//
// pkgInstall used to unzip over the folder it was updating: a failure halfway
// left half the new files on top of the old ones and nothing to go back to.
// Now an install happens beside the package, and the package folder only ever
// changes by rename:
//
//	packages/.jukahub-staging/.download-<key>  the verified download
//	packages/.jukahub-staging/<key>/           the new version, unpacked
//	packages/.jukahub-staging/.swap-<key>      the swap journal (see 3.)
//	packages/<key>/                            the installed version
//	packages/.jukahub-rollback/<key>/          the version before it (one)
//	packages/.jukahub-trash/                   copies being deleted
//
// Every name under packages/ that starts with a dot is the store's own
// bookkeeping (pkgScanInstalled skips them), so none of these shows up as a
// package. A package folder name never starts with a dot (packageDirName), so
// the dot-named files in staging can never be mistaken for a package's
// staging folder, whatever the package is called.
//
// Durability: a rename or a write the next step relies on is on the card
// before that step runs. The unpacked files are flushed before the marker
// that calls them complete is written (bootSyncFilesystems on the handheld,
// one flush per file on Windows, which has no whole-system sync); the marker
// and the journal are fsynced with their folders; the first swap rename is
// flushed before the second starts, and both before the journal is deleted;
// and moved user files are flushed before the marker says the carry-over is
// done. On FAT/exFAT, which orders
// nothing, a power cut can otherwise leave a "complete" version full of empty
// files, or a package folder without the marker that keeps its user's files.
//
//  1. The download is verified (pkgverify.go) and unpacked into staging
//     through an os.Root opened on the staging folder. An entry that is
//     absolute, climbs out with "..", is a link, a device or a pipe, or names
//     a file this system cannot store safely (a reserved Windows name) stops
//     the install; os.Root refuses whatever would still resolve outside.
//  2. A marker (.jukahub-package.json) is written into the unpacked folder
//     last. It names the version and lists the files the package itself
//     installed. A staging folder without one is an unpack that never
//     finished, and is deleted.
//  3. The swap: the installed folder is renamed into the rollback slot (the
//     previous rollback copy goes to the trash first, also by rename), then
//     the staging folder is renamed into place. If the second rename fails the
//     first is undone. Until the new version is complete the old one is never
//     touched.
//  4. Carry-over: the files in the old version that its marker does not list
//     — what the user or the app itself wrote into the folder — are moved into
//     the new one. A file the new version also ships stays in the rollback
//     copy. A folder installed before markers existed has no list, so every
//     old file the new version lacks is kept: exactly what unzipping over the
//     folder used to keep. A rollback copy whose user files could not all be
//     moved is never deleted: the next update refuses until they move, and
//     after pkgCarryGiveUp failed attempts keeps the whole copy aside
//     (.jukahub-rollback/.kept-<key>-<stamp>) rather than block every update.
//     When the installed version's marker cannot be read, nothing says its
//     user files are out of the rollback copy, so that copy is kept aside
//     whole as well: a rollback copy is trashed only on a readable marker's
//     word.
//
// A swap that stops between its renames and cannot be undone ("stranded")
// leaves its journal, and is finished before anything else touches that
// package: by the next install or rollback of it (pkgRecoverKeyLocked), or by
// the next run's recovery pass, which the first store visit, install or
// rollback of that run starts (pkgEnsureRecovered). A removal takes every
// copy, by rename into the trash.
//
// "Roll back" is the same swap with the rollback copy as the incoming version,
// so the version it replaces becomes the rollback copy and a second press goes
// forward again. pkgRecoverInterrupted finishes or undoes a swap that a power
// cut stopped between its renames.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The store's folders and files inside the packages folder.
const (
	pkgStagingDir  = ".jukahub-staging"
	pkgRollbackDir = ".jukahub-rollback"
	pkgTrashDir    = ".jukahub-trash"
	pkgMarkerFile  = ".jukahub-package.json"
)

// The names staging gives a package's download and swap journal, and the
// prefix of a rollback copy kept aside whole (pkgKeepAside). Each starts with
// a dot, which no package folder name does (packageDirName), so a package
// called "x.swap" can never meet the journal of "x".
const (
	pkgDownloadPrefix = ".download-"
	pkgJournalPrefix  = ".swap-"
	pkgKeptPrefix     = ".kept-"
)

// Bounds on one package archive.
const (
	pkgMaxEntries     = 20000
	pkgMaxExpandBytes = int64(1 << 30)
	// pkgFreeSpaceMargin stays free on top of an unpack: saves are written to
	// the same card, and an install must not take its last bytes.
	pkgFreeSpaceMargin = int64(64 << 20)
	// pkgCarryGiveUp is how many failed attempts to move the user's files out
	// of a rollback copy an update refuses through before it keeps that copy
	// aside whole instead (pkgSwapIn): never deleted, never blocking forever.
	pkgCarryGiveUp = 3
)

// pkgSyncEachFile flushes every unpacked file as it is closed. Windows has no
// whole-system sync (bootSyncFilesystems does nothing there), so this is how
// the files are on the disk before the marker calls the copy complete; the
// handheld flushes them all at once instead.
var pkgSyncEachFile = runtime.GOOS == "windows"

// pkgRenameDelays are the pauses between attempts of a rename Windows refuses
// for a moment because something has a file inside open: the store's own card
// scan, an icon being loaded, a virus scanner or the search indexer.
var pkgRenameDelays = []time.Duration{50 * time.Millisecond, 150 * time.Millisecond, 300 * time.Millisecond, 500 * time.Millisecond}

// pkgRename is os.Rename, tried again a few times when Windows reports access
// denied or a sharing or lock violation. Elsewhere it is one attempt.
func pkgRename(from, to string) error {
	err := os.Rename(from, to)
	for _, d := range pkgRenameDelays {
		if err == nil || !pkgRenameRetryable(err) {
			return err
		}
		time.Sleep(d)
		err = os.Rename(from, to)
	}
	return err
}

// pkgRenameRetryable reports a Windows refusal that usually passes:
// ERROR_ACCESS_DENIED (5), ERROR_SHARING_VIOLATION (32) or
// ERROR_LOCK_VIOLATION (33).
func pkgRenameRetryable(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case 5, 32, 33:
		return true
	}
	return false
}

// pkgCarryMove moves one of the user's files inside the os.Root opened on the
// packages folder (pkgCarryOver). A variable so a test can make a move fail.
var pkgCarryMove = func(root *os.Root, from, to string) error {
	return root.Rename(from, to)
}

// pkgExists reports whether anything is at p.
func pkgExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// The marker's origin says what a folder in staging is on its way in from.
const (
	pkgOriginInstall  = "install"
	pkgOriginRollback = "rollback"
)

// pkgOpMu serialises everything that renames or deletes package folders: an
// install's swap, a rollback, a removal and the recovery pass. The store's
// card scan reads inside the folders under it too (pkgLoadLocal,
// pkgScanRollbacks), because Windows refuses to rename a folder while a file
// in it is open. Downloads, unpacking and the trash's slow deletes happen
// outside it.
var pkgOpMu sync.Mutex

// pkgRecoverDone runs the recovery pass once per process, before the first
// install, rollback or scan that needs it.
var pkgRecoverDone sync.Once

// pkgEnsureRecovered runs pkgRecoverInterrupted on the packages folder the
// first time it is called. Later calls return at once.
func pkgEnsureRecovered() {
	pkgRecoverDone.Do(func() { pkgRecoverInterrupted(packageInstallDir()) })
}

// pkgMarker describes one version folder: which package and version it is and
// which files the package installed. It lives inside the folder, so it moves
// with it on every swap.
type pkgMarker struct {
	Schema      int    `json:"schema"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Date        string `json:"date,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
	InstalledAt string `json:"installed_at,omitempty"`
	Origin      string `json:"origin,omitempty"`
	// Listed says Files is the complete list of the package's own files
	// (slash paths). A marker written for a folder installed before markers
	// existed is not listed.
	Listed bool     `json:"listed,omitempty"`
	Files  []string `json:"files,omitempty"`
	// CarryPending is set while the user's files still have to be moved in
	// from the rollback copy; a carry-over that could not finish leaves it on,
	// and the next install or start tries again. CarryFailed counts the
	// attempts that failed (pkgCarryGiveUp).
	CarryPending bool `json:"carry_pending,omitempty"`
	CarryFailed  int  `json:"carry_failed,omitempty"`
}

// pkgPaths are the folders one package key uses. journal exists only while a
// swap is between its two renames.
type pkgPaths struct {
	root     string
	key      string
	live     string
	stage    string
	download string
	journal  string
	back     string
}

func pkgPathsFor(root, key string) pkgPaths {
	return pkgPaths{
		root:     root,
		key:      key,
		live:     filepath.Join(root, key),
		stage:    filepath.Join(root, pkgStagingDir, key),
		download: filepath.Join(root, pkgStagingDir, pkgDownloadPrefix+key),
		journal:  filepath.Join(root, pkgStagingDir, pkgJournalPrefix+key),
		back:     filepath.Join(root, pkgRollbackDir, key),
	}
}

// pkgJournalKey is the package key a staging file name is the swap journal
// of. (Builds before the review used "<key>.swap"; none of them shipped, so
// only the current name is read.)
func pkgJournalKey(name string) (string, bool) {
	key := strings.TrimPrefix(name, pkgJournalPrefix)
	if key == name || key == "" || key != packageDirName(key) {
		return "", false
	}
	return key, true
}

// pkgIsDir reports whether p is a directory (not a link to one).
func pkgIsDir(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}

// pkgReadMarker reads a version folder's marker. ok is false when there is
// none or it cannot be read.
func pkgReadMarker(dir string) (pkgMarker, bool) {
	var m pkgMarker
	data, err := os.ReadFile(filepath.Join(dir, pkgMarkerFile))
	if err != nil || json.Unmarshal(data, &m) != nil || m.Schema < 1 {
		return pkgMarker{}, false
	}
	return m, true
}

// pkgWriteMarker writes a version folder's marker through a synced temporary
// file and a rename, and syncs the folder, so a power cut leaves the old
// marker or the new one, and the next step never runs ahead of it.
func pkgWriteMarker(dir string, m pkgMarker) error {
	m.Schema = 1
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(dir, pkgMarkerFile)
	tmp := target + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := pkgRename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(dir)
	return nil
}

// pkgStepError names the step of an install that failed, so the card and the
// toast can say "Download failed" or "Could not unpack" from one error.
type pkgStepError struct {
	Step string
	Err  error
	// Installed reports that a version of the package is in its folder after
	// the failure, unchanged. Stranded reports a swap that stopped between its
	// renames and could not be undone: the new version waits in staging and
	// the next install of it, or the next run's recovery pass, finishes the
	// swap. Aside is where the swap kept an old rollback copy whole before it
	// failed (pkgKeepAside; "" normally): it stays there.
	Installed bool
	Stranded  bool
	Aside     string
}

func (e *pkgStepError) Error() string { return e.Err.Error() }
func (e *pkgStepError) Unwrap() error { return e.Err }

// The steps a pkgStepError names. pkgStepRecover is an earlier install or
// rollback of the package that is still between its renames and could not be
// finished before this one.
const (
	pkgStepCatalogue = "catalogue"
	pkgStepDownload  = "download"
	pkgStepUnpack    = "unpack"
	pkgStepSwap      = "swap"
	pkgStepRecover   = "recover"
)

// pkgInstallResult is what pkgInstallFrom did.
type pkgInstallResult struct {
	Verified bool
	SHA256   string
	// Kept reports that the version this one replaced is the rollback copy.
	Kept bool
	// Carried counts the user's files moved into the new version.
	Carried int
	// Aside is where an old rollback copy whose user files would not move was
	// kept whole instead of being deleted ("" normally; pkgKeepAside).
	Aside string
}

// pkgInstallFrom downloads p into the staging area under root, verifies and
// unpacks it, and swaps it in for the installed version. phase reports the
// installer card's step and progress the downloaded bytes; both may be nil.
// Whatever fails, the installed version is left as it was — except a swap that
// stopped between its renames and could not be undone (pkgStepError.Stranded),
// which the next install of the package, or the next run's recovery pass,
// finishes.
func pkgInstallFrom(ctx context.Context, root string, p Package, phase func(string), progress func(done, total int64)) (pkgInstallResult, error) {
	var res pkgInstallResult
	if p.HashProblem != "" {
		return res, &pkgStepError{Step: pkgStepCatalogue, Err: errors.New(p.HashProblem)}
	}
	if strings.TrimSpace(p.Download) == "" {
		return res, &pkgStepError{Step: pkgStepCatalogue, Err: errors.New("the catalogue has no download for it")}
	}
	key := packageDirName(p.Name)
	paths := pkgPathsFor(root, key)
	if err := os.MkdirAll(filepath.Join(root, pkgStagingDir), 0o755); err != nil {
		return res, &pkgStepError{Step: pkgStepDownload, Err: err}
	}
	// An earlier swap of this package that stopped between its renames (its
	// journal is still there) is finished or undone first, and its user files
	// carried over: installing on top of it would mark the new version as
	// owing nothing to the rollback copy that holds them. Then any leftover
	// copy in staging is deleted.
	pkgOpMu.Lock()
	rerr := pkgRecoverKeyLocked(root, key)
	if rerr == nil {
		_ = os.RemoveAll(paths.stage)
	}
	pkgOpMu.Unlock()
	if rerr != nil {
		return res, &pkgStepError{Step: pkgStepRecover, Err: rerr, Installed: pkgIsDir(paths.live)}
	}

	if phase != nil {
		phase(installPhaseDownload)
	}
	dl, err := pkgDownloadTo(ctx, p.Download, paths.download, p, progress)
	if err != nil {
		return res, &pkgStepError{Step: pkgStepDownload, Err: err}
	}
	defer os.Remove(paths.download)
	res.Verified, res.SHA256 = dl.Verified, dl.SHA256

	if phase != nil {
		phase(installPhaseExtract)
	}
	var files []string
	if dl.Zip || strings.HasSuffix(strings.ToLower(p.Download), ".zip") {
		files, err = pkgExtractZip(ctx, paths.download, paths.stage)
	} else {
		files, err = pkgStageSingleFile(paths.download, paths.stage, pkgDownloadFileName(p))
	}
	if err != nil {
		_ = os.RemoveAll(paths.stage)
		return res, &pkgStepError{Step: pkgStepUnpack, Err: err}
	}
	// Every unpacked file is on the card before the marker says the copy is
	// complete: recovery finishes a swap with a complete copy, and one of
	// empty files must never be what it finishes.
	bootSyncFilesystems()

	pkgOpMu.Lock()
	defer pkgOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		// The last moment a cancel can stop the install.
		_ = os.RemoveAll(paths.stage)
		return res, &pkgStepError{Step: pkgStepDownload, Err: err, Installed: pkgIsDir(paths.live)}
	}
	marker := pkgMarker{
		Name:        p.Name,
		Version:     strings.TrimSpace(p.Version),
		Date:        strings.TrimSpace(p.Date),
		SHA256:      dl.SHA256,
		Verified:    dl.Verified,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		Origin:      pkgOriginInstall,
		Listed:      true,
		Files:       files,
		// A rollback copy whose swap is still journalled holds the user's
		// files even when the package folder is missing.
		CarryPending: pkgIsDir(paths.live) || (pkgIsDir(paths.back) && pkgExists(paths.journal)),
	}
	if err := pkgWriteMarker(paths.stage, marker); err != nil {
		pkgDiscardStage(paths.stage)
		return res, &pkgStepError{Step: pkgStepUnpack, Err: err, Installed: pkgIsDir(paths.live)}
	}
	swap, err := pkgSwapIn(paths)
	if err != nil {
		if !swap.Stranded {
			// The swap was undone, or never moved anything: the staging copy
			// goes, so no later recovery can "finish" an install that failed.
			pkgDiscardStage(paths.stage)
		}
		// Stranded, the copy stays: the old version is in the rollback slot
		// with the journal, and the next install of this package (or the next
		// run's recovery pass) puts the new one in place (pkgRecoverKeyLocked).
		// A copy the swap kept aside before it failed stays aside, and the
		// toast says so.
		res.Aside = swap.Aside
		return res, &pkgStepError{Step: pkgStepSwap, Err: err, Installed: pkgIsDir(paths.live), Stranded: swap.Stranded, Aside: swap.Aside}
	}
	res.Kept, res.Aside = swap.Kept, swap.Aside
	if swap.Kept {
		moved, cerr := pkgCarryOver(paths)
		res.Carried = moved
		if cerr != nil {
			// The install stands; the files that did not move are still in the
			// rollback copy, and the next install or start moves them.
			logWarn("packages", "%s: carrying your files into the new version: %v", p.Name, cerr)
		}
	}
	return res, nil
}

// pkgSwapResult is what a swap did. Kept: the version it replaced is the
// rollback copy now. Stranded: it stopped between its renames and could not
// be undone — the old version is in the rollback slot, the new one still in
// staging, and the journal says so. Aside: where an old rollback copy was kept
// whole instead of being replaced (pkgKeepAside).
type pkgSwapResult struct {
	Kept     bool
	Stranded bool
	Aside    string
}

// pkgSwapIn puts the complete staging folder in place of the installed
// version, which becomes the rollback copy. On an error the installed version
// is where it was, unless the result says Stranded (logged; the next install
// of the package, or the next run's recovery pass, finishes it). pkgOpMu must
// be held.
func pkgSwapIn(paths pkgPaths) (pkgSwapResult, error) {
	var res pkgSwapResult
	trashed := ""
	untrash := func() {
		if trashed != "" {
			_ = pkgRename(trashed, paths.back)
		}
	}
	if _, lerr := os.Lstat(paths.live); lerr == nil {
		if pkgIsDir(paths.back) {
			// The rollback copy is about to be replaced. If the installed version
			// still waits for user files from it, they move first; files that
			// cannot move stop the install instead of going out with the copy —
			// until pkgCarryGiveUp attempts have failed: then the copy is kept
			// aside whole, so the user's files are neither lost nor in the way of
			// every later update. An installed version whose marker cannot be
			// read cannot say whether it still waits for any: its rollback copy
			// is kept aside whole too, never deleted unchecked.
			m, ok := pkgReadMarker(paths.live)
			if !ok {
				aside, kerr := pkgKeepAside(paths)
				if kerr != nil {
					return res, fmt.Errorf("the previous version may still hold your files and could not be moved aside: %w", kerr)
				}
				res.Aside = aside
				logWarn("packages", "%s: the installed version's marker cannot be read, so the previous version may still hold your files; it is kept whole in %s", paths.key, aside)
			} else if m.CarryPending {
				if _, cerr := pkgCarryOver(paths); cerr != nil {
					// Each update this stops counts; the attempts recovery
					// makes on its own do not. A marker that cannot be read
					// back is never rewritten from nothing.
					cur, readable := pkgReadMarker(paths.live)
					if !readable {
						return res, fmt.Errorf("some of your files in the previous version could not be moved (%v) — close whatever uses them and try again", cerr)
					}
					m = cur
					m.CarryFailed++
					if m.CarryFailed < pkgCarryGiveUp {
						if werr := pkgWriteMarker(paths.live, m); werr != nil {
							logWarn("packages", "%s: counting a failed carry-over: %v", paths.key, werr)
						}
						return res, fmt.Errorf("some of your files in the previous version could not be moved (%v) — close whatever uses them and try again", cerr)
					}
					aside, kerr := pkgKeepAside(paths)
					if kerr != nil {
						return res, fmt.Errorf("some of your files in the previous version could not be moved (%v), and the copy could not be kept aside: %w", cerr, kerr)
					}
					res.Aside = aside
					logWarn("packages", "%s: after %d attempts some of your files still would not move out of the previous version (%v); that copy is kept whole in %s", paths.key, m.CarryFailed, cerr, aside)
					m.CarryPending, m.CarryFailed = false, 0
					if werr := pkgWriteMarker(paths.live, m); werr != nil {
						logWarn("packages", "%s: updating the installed version's marker: %v", paths.key, werr)
					}
				}
			}
			if pkgIsDir(paths.back) {
				t, terr := pkgTrash(paths.root, paths.back)
				if terr != nil {
					return res, fmt.Errorf("clear the old rollback copy: %w", terr)
				}
				trashed = t
			}
		}
		if err := os.MkdirAll(filepath.Dir(paths.back), 0o755); err != nil {
			untrash()
			return res, err
		}
		// The journal tells the next start that a missing package folder is a
		// swap cut short, not a folder the user deleted. It is on the card
		// before the rename it describes can be.
		if err := writeFileSynced(paths.journal, nil, 0o644); err != nil {
			_ = os.Remove(paths.journal)
			untrash()
			return res, err
		}
		syncDirectory(filepath.Dir(paths.journal))
		pkgEnsureMarker(paths)
		if err := pkgRename(paths.live, paths.back); err != nil {
			_ = os.Remove(paths.journal)
			untrash()
			return res, fmt.Errorf("move the installed version aside: %w", err)
		}
		res.Kept = true
		// The installed version is in the rollback slot on the card before the
		// new one takes its name: FAT/exFAT orders nothing, and the second
		// rename reaching the card first could leave a cut-off card with the
		// old version in neither place.
		bootSyncFilesystems()
	} else if !errors.Is(lerr, fs.ErrNotExist) {
		return res, lerr
	}
	if err := pkgRename(paths.stage, paths.live); err != nil {
		if res.Kept {
			if uerr := pkgRename(paths.back, paths.live); uerr != nil {
				// The journal stays: the next install of the package, or the
				// next run's recovery pass, puts the new version in place.
				logError("packages", "%s: the old version could not be put back (%v); it is in %s", paths.key, uerr, paths.back)
				return pkgSwapResult{Stranded: true, Aside: res.Aside}, fmt.Errorf("put the new version in place: %w", err)
			}
			// The version is back in place on the card before the journal
			// that would restore it goes.
			bootSyncFilesystems()
			_ = os.Remove(paths.journal)
			untrash()
		}
		return pkgSwapResult{Aside: res.Aside}, fmt.Errorf("put the new version in place: %w", err)
	}
	// Both renames are on the card before the journal goes, so a power cut
	// cannot leave a card where the journal is gone but a rename is not done.
	bootSyncFilesystems()
	_ = os.Remove(paths.journal)
	return res, nil
}

// pkgKeepAside moves the rollback copy out of its slot, whole, to
// .jukahub-rollback/.kept-<key>-<stamp>: what an update does with a copy whose
// user files would not move after pkgCarryGiveUp attempts. Nothing in it is
// deleted; removing the package removes it with the rest (pkgKeptCopies).
func pkgKeepAside(paths pkgPaths) (string, error) {
	dest := filepath.Join(paths.root, pkgRollbackDir, pkgKeptPrefix+paths.key+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := pkgRename(paths.back, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// pkgKeptCopies lists the copies of key that pkgKeepAside kept. The stamp
// never holds a "-", so the copies of "a" are never confused with those of
// "a-b".
func pkgKeptCopies(root, key string) []string {
	var out []string
	entries, _ := os.ReadDir(filepath.Join(root, pkgRollbackDir))
	prefix := pkgKeptPrefix + key + "-"
	for _, e := range entries {
		stamp := strings.TrimPrefix(e.Name(), prefix)
		if stamp == e.Name() || stamp == "" || strings.Contains(stamp, "-") {
			continue
		}
		out = append(out, filepath.Join(root, pkgRollbackDir, e.Name()))
	}
	return out
}

// pkgEnsureMarker gives an installed folder from before markers existed a
// marker describing it (from the installed-version record), so the rollback
// copy it is about to become can say which version it is.
func pkgEnsureMarker(paths pkgPaths) {
	if _, ok := pkgReadMarker(paths.live); ok {
		return
	}
	meta := pkgLoadInstalledMeta()[paths.key]
	m := pkgMarker{
		Name:        meta.Name,
		Version:     meta.Version,
		Date:        meta.Date,
		SHA256:      meta.SHA256,
		Verified:    meta.Verified,
		InstalledAt: meta.InstalledAt,
		Origin:      pkgOriginInstall,
	}
	if m.Name == "" {
		m.Name = strings.ReplaceAll(paths.key, "_", " ")
	}
	if err := pkgWriteMarker(paths.live, m); err != nil {
		logWarn("packages", "%s: describing the installed version: %v", paths.key, err)
	}
}

// pkgTrashSeq numbers the copies this run moves into the trash. A removal
// trashes the rollback copy, the package folder and the staging copy back to
// back, all under one base name, and Windows' clock moves in steps of up to
// 15.6 ms: with the time alone the second name was the first one again, its
// rename was refused, and the removal failed. The number keeps the names of
// one run apart, the time keeps them apart from what an earlier run left.
var pkgTrashSeq atomic.Uint64

// pkgTrash moves dir into the trash folder by rename and returns where it
// went. Deleting happens later (pkgEmptyTrash), so a power cut mid-delete
// can never leave half a rollback copy in the rollback slot. pkgOpMu must be
// held.
func pkgTrash(root, dir string) (string, error) {
	trash := filepath.Join(root, pkgTrashDir)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	stem := filepath.Base(dir) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-"
	dest := filepath.Join(trash, stem+strconv.FormatUint(pkgTrashSeq.Add(1), 36))
	for pkgExists(dest) {
		// An earlier run's leftover whose clock read the same.
		dest = filepath.Join(trash, stem+strconv.FormatUint(pkgTrashSeq.Add(1), 36))
	}
	if err := pkgRename(dir, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// pkgEmptyTrash deletes the copies the swaps and removals replaced. It runs
// off the render thread, after the install it belongs to. Only the listing
// happens under pkgOpMu: a swap takes something back out of the trash only
// while it holds the lock, so whatever is in the trash when the lock is free
// is never touched again, and the long delete runs without blocking an
// install or a removal.
func pkgEmptyTrash(root string) {
	trash := filepath.Join(root, pkgTrashDir)
	pkgOpMu.Lock()
	entries, err := os.ReadDir(trash)
	pkgOpMu.Unlock()
	if err != nil {
		return
	}
	for _, e := range entries {
		if rerr := os.RemoveAll(filepath.Join(trash, e.Name())); rerr != nil {
			logWarn("packages", "emptying the trash (%s): %v; it is deleted at the next start", e.Name(), rerr)
		}
	}
	// The folder itself goes only while nothing can be renaming into it.
	pkgOpMu.Lock()
	_ = os.Remove(trash)
	pkgOpMu.Unlock()
}

// pkgCarryOver moves the user's files from the rollback copy into the
// installed version: every file the rollback copy's marker does not list as
// the package's own (every file, when it has no list) that the installed
// version does not have. It reports how many moved. When everything that had
// to move did, the installed version's CarryPending is cleared — after the
// moves are flushed to the card, so the marker never says "done" ahead of
// them; when some could not move, the error names the first one. The moves
// are renames inside one os.Root on the packages folder (pkgCarryMove), so a
// link inside either folder cannot send a file elsewhere.
func pkgCarryOver(paths pkgPaths) (int, error) {
	live, ok := pkgReadMarker(paths.live)
	if !ok || !live.CarryPending {
		return 0, nil
	}
	if !pkgIsDir(paths.back) {
		live.CarryPending, live.CarryFailed = false, 0
		return 0, pkgWriteMarker(paths.live, live)
	}
	own := map[string]bool{}
	listed := false
	if m, ok := pkgReadMarker(paths.back); ok && m.Listed {
		listed = true
		for _, f := range m.Files {
			own[f] = true
		}
	}
	root, err := os.OpenRoot(paths.root)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	backRel := filepath.Join(pkgRollbackDir, paths.key)
	moved, failed, conflicts := 0, 0, 0
	var firstErr error
	firstRel := ""
	fail := func(rel string, err error) {
		failed++
		if firstErr == nil {
			firstErr, firstRel = err, filepath.ToSlash(rel)
		}
	}
	walkErr := filepath.WalkDir(paths.back, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			rel, _ := filepath.Rel(paths.back, p)
			fail(rel, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(paths.back, p)
		if rerr != nil || !filepath.IsLocal(rel) {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, pkgMarkerFile) || (listed && own[slash]) {
			// The marker (and a temporary copy a power cut left) stays with
			// its version.
			return nil
		}
		dest := filepath.Join(paths.key, rel)
		if _, err := root.Lstat(dest); err == nil {
			// The new version ships this file: it wins, and the user's copy
			// stays in the rollback copy.
			conflicts++
			return nil
		}
		if pkgParentBlocked(root, dest) {
			// A folder of the user's where the new version has a file.
			conflicts++
			return nil
		}
		if parent := filepath.Dir(dest); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				fail(rel, err)
				return nil
			}
		}
		if err := pkgCarryMove(root, filepath.Join(backRel, rel), dest); err != nil {
			fail(rel, err)
			return nil
		}
		moved++
		return nil
	})
	if conflicts > 0 {
		logWarn("packages", "%s: %d of your file(s) clash with the new version and stay in the rollback copy", paths.key, conflicts)
	}
	if walkErr != nil {
		fail("", walkErr)
	}
	if moved > 0 {
		// The moves are on the card before the marker below can be: a marker
		// that says "nothing pending" ahead of them would let the next update
		// send the user's files to the trash with the rollback copy.
		bootSyncFilesystems()
	}
	if failed > 0 {
		reason := firstErr
		var le *os.LinkError
		var pe *fs.PathError
		switch {
		case errors.As(reason, &le):
			reason = le.Err
		case errors.As(reason, &pe):
			reason = pe.Err
		}
		what := firstRel
		if what == "" || what == "." {
			what = "the previous version's folder"
		}
		if failed > 1 {
			return moved, fmt.Errorf("%s and %d more could not be moved: %w", what, failed-1, reason)
		}
		return moved, fmt.Errorf("%s could not be moved: %w", what, reason)
	}
	live.CarryPending, live.CarryFailed = false, 0
	if err := pkgWriteMarker(paths.live, live); err != nil {
		return moved, err
	}
	return moved, nil
}

// pkgParentBlocked reports whether a folder on the way to rel (relative to
// root) exists as something other than a folder.
func pkgParentBlocked(root *os.Root, rel string) bool {
	dir := filepath.Dir(rel)
	for dir != "." && dir != string(filepath.Separator) && dir != "" {
		if info, err := root.Lstat(dir); err == nil && !info.IsDir() {
			return true
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return false
}

// errPkgNoRollback is a rollback asked for a package with no rollback copy.
var errPkgNoRollback = errors.New("no previous version is kept")

// pkgRollBack swaps the rollback copy of key back in; the version it replaces
// becomes the rollback copy, so the same action goes forward again. It returns
// the restored version's marker.
func pkgRollBack(root, key string) (pkgMarker, error) {
	pkgEnsureRecovered()
	pkgOpMu.Lock()
	defer pkgOpMu.Unlock()
	paths := pkgPathsFor(root, key)
	// A swap of this package still between its renames is finished first, as
	// an install does, so the rollback starts from one version in place and
	// one in the slot.
	if err := pkgRecoverKeyLocked(root, key); err != nil {
		return pkgMarker{}, err
	}
	if !pkgIsDir(paths.back) {
		return pkgMarker{}, errPkgNoRollback
	}
	if err := os.MkdirAll(filepath.Join(root, pkgStagingDir), 0o755); err != nil {
		return pkgMarker{}, err
	}
	m, ok := pkgReadMarker(paths.back)
	if !ok {
		m = pkgMarker{Name: strings.ReplaceAll(key, "_", " ")}
	}
	m.Origin = pkgOriginRollback
	m.CarryPending, m.CarryFailed = pkgIsDir(paths.live), 0
	if err := pkgWriteMarker(paths.back, m); err != nil {
		return m, err
	}
	if err := pkgRename(paths.back, paths.stage); err != nil {
		return m, fmt.Errorf("prepare the previous version: %w", err)
	}
	swap, err := pkgSwapIn(paths)
	if err != nil {
		// Unless the swap is stranded (then recovery finishes it), the copy
		// goes back to its slot.
		if !swap.Stranded && pkgIsDir(paths.stage) && !pkgIsDir(paths.back) {
			if rerr := pkgRename(paths.stage, paths.back); rerr != nil {
				logError("packages", "%s: the previous version could not be put back (%v); it is in %s", key, rerr, paths.stage)
			}
		}
		return m, err
	}
	if swap.Kept {
		if _, cerr := pkgCarryOver(paths); cerr != nil {
			logWarn("packages", "%s: carrying your files into the restored version: %v", key, cerr)
		}
	}
	return m, nil
}

// pkgRecoverInterrupted puts every package back into a consistent state after
// a run that stopped mid-install, mid-rollback or mid-removal:
//
//   - a download or temporary file in staging is deleted;
//   - a staging folder without a marker is an unpack that never finished, and
//     is deleted;
//   - a complete staging folder whose package folder is missing is a swap that
//     stopped between its renames: it is finished;
//   - a rollback copy on its way back in whose slot is empty goes back to its
//     slot; any other complete staging folder never started its swap, and is
//     deleted (the installed version was never touched);
//   - a swap journal whose package folder is missing (and nothing in staging
//     took its place) puts the rollback copy back. Without a journal a missing
//     package folder is one the user deleted, and nothing is restored;
//   - a carry-over that did not finish is finished, and the trash is emptied.
//
// pkgRecoverKeyLocked applies the rules to one package; an install or a
// rollback runs it for its own package first, so a swap a failure stranded
// in this run is put right before anything else touches that package.
func pkgRecoverInterrupted(root string) {
	pkgOpMu.Lock()
	keys := map[string]bool{}
	stageRoot := filepath.Join(root, pkgStagingDir)
	entries, _ := os.ReadDir(stageRoot)
	for _, e := range entries {
		name := e.Name()
		if key, ok := pkgJournalKey(name); ok && !e.IsDir() {
			keys[key] = true
			continue
		}
		if e.IsDir() && name == packageDirName(name) {
			keys[name] = true
			continue
		}
		// A download, a temporary file or anything else that is neither a
		// package's copy nor its journal.
		_ = os.RemoveAll(filepath.Join(stageRoot, name))
	}
	backs, _ := os.ReadDir(filepath.Join(root, pkgRollbackDir))
	for _, e := range backs {
		if e.IsDir() && e.Name() == packageDirName(e.Name()) {
			keys[e.Name()] = true
		}
	}
	for key := range keys {
		if err := pkgRecoverKeyLocked(root, key); err != nil {
			logWarn("packages", "%s: %v", key, err)
		}
	}
	pkgOpMu.Unlock()
	pkgEmptyTrash(root)
}

// pkgRecoverKeyLocked puts one package back into a consistent state, by
// pkgRecoverInterrupted's rules. pkgOpMu must be held. An error means a swap
// of the package is still between its renames, or a copy in staging could
// not be dealt with; nothing that holds the user's files is deleted.
func pkgRecoverKeyLocked(root, key string) error {
	paths := pkgPathsFor(root, key)
	_ = os.Remove(paths.download)
	journal := pkgExists(paths.journal)
	moved := false
	m, complete := pkgReadMarker(paths.stage)
	if !complete && pkgExists(paths.stage) {
		// An unpack that never finished.
		_ = os.RemoveAll(paths.stage)
	}
	if complete && !pkgIsDir(paths.live) {
		// A swap cut short after the installed version moved aside, or a
		// first install cut short before its one rename: it is finished.
		if err := pkgRename(paths.stage, paths.live); err != nil {
			logWarn("packages", "%s: finishing an interrupted install: %v", key, err)
		} else {
			moved = true
			logInfo("packages", "%s: finished an install that was interrupted", key)
			pkgRecordInstalledMarker(key, m)
		}
	}
	if journal && !pkgIsDir(paths.live) && pkgIsDir(paths.back) {
		// The swap moved the installed version aside and nothing could take
		// its place: it goes back.
		mb, _ := pkgReadMarker(paths.back)
		if err := pkgRename(paths.back, paths.live); err != nil {
			logWarn("packages", "%s: restoring the installed version: %v", key, err)
		} else {
			moved = true
			logInfo("packages", "%s: put the installed version back after an interrupted swap", key)
			if mb.Schema > 0 {
				mb.CarryPending, mb.CarryFailed = false, 0
				_ = pkgWriteMarker(paths.live, mb)
				pkgRecordInstalledMarker(key, mb)
			}
		}
	}
	if complete && pkgIsDir(paths.stage) && pkgIsDir(paths.live) {
		if m.Origin == pkgOriginRollback && !pkgIsDir(paths.back) {
			// A rollback copy on its way in whose slot is empty goes back to
			// its slot.
			m.Origin, m.CarryPending, m.CarryFailed = "", false, 0
			if err := pkgWriteMarker(paths.stage, m); err != nil {
				logWarn("packages", "%s: putting the rollback copy back: %v", key, err)
			} else {
				_ = os.MkdirAll(filepath.Dir(paths.back), 0o755)
				if err := pkgRename(paths.stage, paths.back); err != nil {
					logWarn("packages", "%s: putting the rollback copy back: %v", key, err)
				}
			}
		} else {
			// A complete copy whose swap never started: the installed
			// version was never touched.
			pkgDiscardStage(paths.stage)
		}
	}
	if journal && (pkgIsDir(paths.live) || !pkgIsDir(paths.back)) {
		if moved {
			// What recovery renamed is on the card before the journal that
			// would redo it goes.
			bootSyncFilesystems()
		}
		_ = os.Remove(paths.journal)
	}
	if lm, ok := pkgReadMarker(paths.live); ok && lm.CarryPending && pkgIsDir(paths.back) {
		if _, err := pkgCarryOver(paths); err != nil {
			logWarn("packages", "%s: carrying your files into the installed version: %v", key, err)
		}
	}
	switch {
	case pkgExists(paths.journal):
		return errors.New("an earlier install or rollback of it stopped halfway and its folder could not be put back; close whatever uses its files and try again")
	case pkgExists(paths.stage):
		return errors.New("an earlier install or rollback of it left a copy that could not be put in place; close whatever uses its files and try again")
	}
	return nil
}

// pkgDiscardStage deletes a staging copy. Its marker goes first, so a delete
// cut short leaves an unfinished unpack, which recovery deletes, and never a
// "complete" copy with files missing, which recovery would put in place.
func pkgDiscardStage(stage string) {
	_ = os.Remove(filepath.Join(stage, pkgMarkerFile))
	_ = os.RemoveAll(stage)
}

// pkgRecordInstalledMarker updates the installed-version record from a
// version folder's marker (after a rollback or a recovered swap).
func pkgRecordInstalledMarker(key string, m pkgMarker) {
	name := m.Name
	if name == "" {
		name = strings.ReplaceAll(key, "_", " ")
	}
	pkgRecordInstalledMeta(key, pkgInstalledMeta{
		Name:        name,
		Version:     m.Version,
		Date:        m.Date,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		SHA256:      m.SHA256,
		Verified:    m.Verified,
	})
}

// pkgScanRollbacks lists the packages with a rollback copy and the version
// each copy is ("" when it does not say). It reads the card: background only.
// It holds pkgOpMu: on Windows a marker open for reading makes a swap's
// rename of its folder fail.
func pkgScanRollbacks(root string) map[string]string {
	out := map[string]string{}
	pkgOpMu.Lock()
	defer pkgOpMu.Unlock()
	entries, err := os.ReadDir(filepath.Join(root, pkgRollbackDir))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || skipScanEntryName(e.Name()) {
			continue
		}
		m, _ := pkgReadMarker(filepath.Join(root, pkgRollbackDir, e.Name()))
		out[e.Name()] = strings.TrimSpace(m.Version)
	}
	return out
}

// ── Unpacking ────────────────────────────────────────────────────────────

// pkgCtxReader stops a copy when the install is cancelled.
type pkgCtxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c pkgCtxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// pkgArchiveRel turns an archive entry name into a relative path inside the
// package (native and slash forms), or "" for the archive's own top. A name
// that is absolute, climbs out with "..", holds a NUL or is not a local name
// on this system (filepath.IsLocal: a reserved Windows name, a drive or a
// stream) is an error.
func pkgArchiveRel(name string) (rel, slash string, err error) {
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.ContainsRune(n, 0) {
		return "", "", errors.New("the archive holds a name with a NUL byte")
	}
	if strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':') {
		return "", "", fmt.Errorf("the archive holds an absolute path (%q)", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", "", fmt.Errorf("the archive holds a path that climbs out of the package (%q)", name)
		}
	}
	clean := path.Clean(n)
	if clean == "." || clean == "" {
		return "", "", nil
	}
	rel = filepath.FromSlash(clean)
	if !filepath.IsLocal(rel) {
		return "", "", fmt.Errorf("the archive holds a name this system cannot store safely (%q)", name)
	}
	return rel, clean, nil
}

// pkgExtractZip unpacks archive into dest through an os.Root opened on dest
// and returns the files it wrote (slash paths). Links, devices, pipes and
// unsafe names stop it; so does an entry that appears twice, an archive with
// more than pkgMaxEntries entries or that unpacks past pkgMaxExpandBytes, and
// a card without room for it. The caller deletes dest on an error.
func pkgExtractZip(ctx context.Context, archive, dest string) ([]string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		if zr != nil {
			_ = zr.Close()
		}
		return nil, fmt.Errorf("the download is not a zip archive: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > pkgMaxEntries {
		return nil, fmt.Errorf("the archive has %d entries (the limit is %d)", len(zr.File), pkgMaxEntries)
	}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		sum, ok := addDeclaredBytes(total, declaredEntryBytes(f.UncompressedSize64), pkgMaxExpandBytes)
		if !ok {
			return nil, fmt.Errorf("the archive unpacks to more than %d MiB", pkgMaxExpandBytes>>20)
		}
		total = sum
	}
	// filesFreeSpace answers on Windows too (GetDiskFreeSpaceEx); statfs alone
	// left the check out there.
	if free, ferr := filesFreeSpace(filepath.Dir(dest)); ferr == nil && free < total+pkgFreeSpaceMargin {
		return nil, fmt.Errorf("not enough free space: it needs %s and %s is free", formatInstallBytes(total+pkgFreeSpaceMargin), formatInstallBytes(free))
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, slash, err := pkgArchiveRel(f.Name)
		if err != nil {
			return nil, err
		}
		if rel == "" || slash == pkgMarkerFile {
			// The archive's top, or a marker of its own: the install writes the
			// marker, never the package.
			continue
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("the archive holds a link (%q); links are refused", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if !mode.IsRegular() {
			return nil, fmt.Errorf("the archive holds a special file (%q)", f.Name)
		}
		if parent := filepath.Dir(rel); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return nil, err
			}
		}
		perm := fs.FileMode(0o644)
		if mode&0o111 != 0 {
			perm = 0o755
		}
		if err := pkgExtractEntry(ctx, root, f, rel, perm); err != nil {
			return nil, err
		}
		files = append(files, slash)
	}
	return files, nil
}

// pkgExtractEntry writes one regular file of the archive. O_EXCL refuses an
// entry that names a file already written (two entries for one path, or two
// that differ only in case on a card that does not tell them apart).
func pkgExtractEntry(ctx context.Context, root *os.Root, f *zip.File, rel string, perm fs.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("read %q: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("the archive holds %q twice", f.Name)
		}
		return err
	}
	want := declaredEntryBytes(f.UncompressedSize64)
	n, copyErr := io.Copy(out, pkgCtxReader{ctx: ctx, r: io.LimitReader(rc, want+1)})
	var syncErr error
	if copyErr == nil && pkgSyncEachFile {
		syncErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("unpack %q: %w", f.Name, copyErr)
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != want {
		return fmt.Errorf("%q is damaged (%d of %d bytes)", f.Name, n, want)
	}
	return nil
}

// pkgStageSingleFile makes a download that is not an archive the one file of
// the staging folder.
func pkgStageSingleFile(download, stage, name string) ([]string, error) {
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, err
	}
	if err := pkgRename(download, filepath.Join(stage, name)); err != nil {
		return nil, err
	}
	return []string{name}, nil
}

// pkgDownloadFileName is the name a download that is not an archive is kept
// under: the last part of its URL when that is a plain file name, else the
// package's folder name. (It used to be saved as "<name>.zip" whatever it
// was.)
func pkgDownloadFileName(p Package) string {
	key := packageDirName(p.Name)
	u, err := url.Parse(strings.TrimSpace(p.Download))
	if err != nil {
		return key
	}
	base := path.Base(u.Path)
	if unescaped, uerr := url.PathUnescape(base); uerr == nil {
		base = unescaped
	}
	if strings.Trim(base, "./") == "" {
		return key
	}
	name := packageDirName(base)
	if name == pkgMarkerFile || !filepath.IsLocal(name) {
		return key
	}
	return name
}
