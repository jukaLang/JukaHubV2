package main

// patch_db.go is the local package database for the Patch manager. It is a
// single atomic JSON file (db.json) under the Patch state directory; every
// write goes through a temp-file + rename so a crash never leaves a partially
// written database.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// DBInstalledFile records one managed file owned by a package.
type DBInstalledFile struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	BackupRef string `json:"backup_ref,omitempty"`
	Origin    string `json:"origin"` // "package" or "user"
}

// DBInstalledPkg records an installed package.
type DBInstalledPkg struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Title       string            `json:"title"`
	InstalledAt string            `json:"installed_at"`
	KeyID       string            `json:"key_id"`
	Transaction string            `json:"transaction"`
	Files       []DBInstalledFile `json:"files"`
	Risk        string            `json:"risk"`
	Restart     string            `json:"restart"`
	Held        bool              `json:"held,omitempty"`
}

// DBTransaction is one completed (or failed) transaction.
type DBTransaction struct {
	ID         string   `json:"id"`
	At         string   `json:"at"`
	Op         string   `json:"op"` // install / upgrade / remove / rollback / repair / profile
	Packages   []string `json:"packages"`
	Result     string   `json:"result"` // committed / rolled-back / failed
	BackupSet  string   `json:"backup_set,omitempty"`
	RollbackOf string   `json:"rollback_of,omitempty"`
}

// PatchDB is the on-disk database.
type PatchDB struct {
	Schema       int              `json:"schema"`
	Installed    []DBInstalledPkg `json:"installed"`
	Transactions []DBTransaction  `json:"transactions"`
}

var (
	patchDBMu sync.Mutex
	patchDB   = &PatchDB{Schema: 1}
	// patchDBUnread is why the last load could not read a db.json that may
	// hold packages (nil when it read one, or there was none). savePatchDB
	// refuses while it is set: this session's database is (nearly) empty,
	// and writing it would lose every earlier package and the history.
	// Guarded by patchDBMu.
	patchDBUnread error
)

// patchDBPath returns the database file path.
func patchDBPath() (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "db", "db.json"), nil
}

// patchDBCorruptKeep bounds the damaged database copies kept for inspection.
const patchDBCorruptKeep = 5

// patchDBLoadOnce reads db.json the first time anything touches the database.
// Nothing loaded it before ARCH-9 (2026-11-17): every start began with an
// empty database, so the first install after a restart rewrote db.json without
// the earlier packages and history, and Remove did not know them.
var patchDBLoadOnce sync.Once

// patchDBEnsureLoaded loads db.json once per process. Call it before taking
// patchDBMu (loadPatchDB takes the lock itself).
func patchDBEnsureLoaded() { patchDBLoadOnce.Do(loadPatchDB) }

// loadPatchDB loads the local database (best effort; a missing file starts
// empty, a corrupt file is quarantined). A db.json that may hold packages but
// could not be read (the state folder not found, an I/O error, a schema from a
// newer JukaHub) leaves the database unsaved for the session (patchDBUnread),
// so the file is never overwritten without its packages and history; logged
// once, as production loads once. Production reaches it through
// patchDBEnsureLoaded; tests call it directly to re-read the file.
func loadPatchDB() {
	unread := patchDBRead()
	patchDBMu.Lock()
	patchDBUnread = unread
	patchDBMu.Unlock()
	if unread != nil {
		logWarn("patch", "package database not read, so it is not saved this session: %v", unread)
	}
}

// patchDBRead is loadPatchDB's read: nil when db.json was loaded, was not
// there, or was damaged and moved aside.
func patchDBRead() error {
	path, err := patchDBPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing installed yet
	}
	if err != nil {
		return err
	}
	var db PatchDB
	if json.Unmarshal(data, &db) != nil || db.Schema < 1 {
		// Keep the damaged copy for inspection, but bounded like the config's
		// (FIX-17): a database that fails every start must not fill the card.
		// A file that cannot even be moved aside is not overwritten either.
		if err := os.Rename(path, corruptAsidePath(path)); err != nil {
			return fmt.Errorf("%s is damaged and could not be moved aside: %w", path, err)
		}
		pruneCorruptCopies(path, patchDBCorruptKeep)
		return nil
	}
	if db.Schema != 1 {
		return fmt.Errorf("%s has schema %d; this JukaHub reads schema 1", path, db.Schema)
	}
	patchDBMu.Lock()
	patchDB = &db
	patchDBMu.Unlock()
	return nil
}

// savePatchDB writes the database atomically (temp + fsync + rename). It
// refuses after a load that could not read db.json (patchDBUnread).
func savePatchDB() error {
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
	if patchDBUnread != nil {
		return fmt.Errorf("the package database could not be read, so it is not overwritten: %w", patchDBUnread)
	}
	path, err := patchDBPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(patchDB, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// dbFindInstalled returns the installed record for a package.
func dbFindInstalled(name string) *DBInstalledPkg {
	patchDBEnsureLoaded()
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
	for i := range patchDB.Installed {
		if patchDB.Installed[i].Name == name {
			return &patchDB.Installed[i]
		}
	}
	return nil
}

// dbIsInstalled reports whether a package is installed.
func dbIsInstalled(name string) bool { return dbFindInstalled(name) != nil }

// dbRecordInstall upserts an installed package record.
func dbRecordInstall(pkg DBInstalledPkg) error {
	patchDBEnsureLoaded()
	patchDBMu.Lock()
	replaced := false
	for i := range patchDB.Installed {
		if patchDB.Installed[i].Name == pkg.Name {
			patchDB.Installed[i] = pkg
			replaced = true
			break
		}
	}
	if !replaced {
		patchDB.Installed = append(patchDB.Installed, pkg)
	}
	patchDBMu.Unlock()
	return savePatchDB()
}

// dbRemoveInstalled removes an installed package record (and its files).
func dbRemoveInstalled(name string) error {
	patchDBEnsureLoaded()
	patchDBMu.Lock()
	out := patchDB.Installed[:0]
	for _, p := range patchDB.Installed {
		if p.Name != name {
			out = append(out, p)
		}
	}
	patchDB.Installed = out
	patchDBMu.Unlock()
	return savePatchDB()
}

// dbAppendTransaction records a transaction outcome.
func dbAppendTransaction(tx DBTransaction) error {
	patchDBEnsureLoaded()
	patchDBMu.Lock()
	patchDB.Transactions = append(patchDB.Transactions, tx)
	if len(patchDB.Transactions) > 200 {
		patchDB.Transactions = patchDB.Transactions[len(patchDB.Transactions)-200:]
	}
	patchDBMu.Unlock()
	return savePatchDB()
}

// dbTransactions returns a copy of the transaction history (newest first).
func dbTransactions() []DBTransaction {
	patchDBEnsureLoaded()
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
	out := make([]DBTransaction, len(patchDB.Transactions))
	copy(out, patchDB.Transactions)
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
