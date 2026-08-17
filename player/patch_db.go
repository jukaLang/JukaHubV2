package main

// patch_db.go is the local package database for the Patch manager. It is a
// single atomic JSON file (db.json) under the Patch state directory; every
// write goes through a temp-file + rename so a crash never leaves a partially
// written database.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
)

// patchDBPath returns the database file path.
func patchDBPath() (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "db", "db.json"), nil
}

// loadPatchDB loads the local database (best effort; a missing file starts
// empty, a corrupt file is quarantined).
func loadPatchDB() {
	path, err := patchDBPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var db PatchDB
	if json.Unmarshal(data, &db) != nil {
		_ = os.Rename(path, path+".corrupt-"+fmt.Sprintf("%d", time.Now().Unix()))
		return
	}
	if db.Schema == 1 {
		patchDBMu.Lock()
		patchDB = &db
		patchDBMu.Unlock()
	}
}

// savePatchDB writes the database atomically (temp + fsync + rename).
func savePatchDB() error {
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
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

// dbIsHeld reports whether a package is held against safe upgrades.
func dbIsHeld(name string) bool {
	p := dbFindInstalled(name)
	return p != nil && p.Held
}

// dbRecordInstall upserts an installed package record.
func dbRecordInstall(pkg DBInstalledPkg) error {
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

// dbSetHeld toggles the hold flag on an installed package.
func dbSetHeld(name string, held bool) error {
	patchDBMu.Lock()
	for i := range patchDB.Installed {
		if patchDB.Installed[i].Name == name {
			patchDB.Installed[i].Held = held
			patchDBMu.Unlock()
			return savePatchDB()
		}
	}
	patchDBMu.Unlock()
	return fmt.Errorf("package %q is not installed", name)
}

// dbInstalledFiles returns the file records owned by a package.
func dbInstalledFiles(name string) []DBInstalledFile {
	p := dbFindInstalled(name)
	if p == nil {
		return nil
	}
	out := make([]DBInstalledFile, len(p.Files))
	copy(out, p.Files)
	return out
}

// dbPackageByFile finds the package that owns a managed path.
func dbPackageByFile(path string) (string, bool) {
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
	for _, p := range patchDB.Installed {
		for _, f := range p.Files {
			if filepath.Clean(f.Path) == filepath.Clean(path) {
				return p.Name, true
			}
		}
	}
	return "", false
}

// dbValidate checks database integrity; broken entries are reported.
func dbValidate() []string {
	var problems []string
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
	names := map[string]bool{}
	for _, p := range patchDB.Installed {
		if names[p.Name] {
			problems = append(problems, "duplicate installed package: "+p.Name)
		}
		names[p.Name] = true
		for _, f := range p.Files {
			if strings.TrimSpace(f.Path) == "" {
				problems = append(problems, p.Name+": empty managed path")
			}
		}
	}
	return problems
}

// dbSummary returns package counts for the Overview view.
type dbSummary struct {
	Installed   int
	Upgradeable int
	Held        int
	Broken      int
}

// buildDBSummary computes installed/upgradeable/held/broken counts.
func buildDBSummary() dbSummary {
	var s dbSummary
	patchDBMu.Lock()
	defer patchDBMu.Unlock()
	for _, p := range patchDB.Installed {
		s.Installed++
		if p.Held {
			s.Held++
		}
	}
	return s
}

var errNotInstalled = errors.New("package is not installed")
