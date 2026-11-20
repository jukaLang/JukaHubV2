package main

// libindex.go — the library index (LIB-1): a cache in front of the ROM scan
// (scanROMLibrary, romsetup.go) so a card with thousands of games opens at
// once and is not walked again on every open.
//
// The index is a bbolt file in JukaHub's data folder (library-index.db). It
// keeps, for every folder the scan has read, the folder's modification time
// and size and the names (and kinds) of what is in it; for every game, what
// the scan and the library build derive at a cost — the content key the view
// state is filed under (libraryContentKey reads up to 512 KiB of the file),
// the files a .cue/.gdi/.m3u names, and the CRC32 a game's details are looked
// up by — each stamped with the file's size and time so a changed file is read
// again; and the last library the Games library built, whole.
//
// It is only ever a cache. The scan reads the card through it
// (scanROMLibraryFrom with a libIndexSource): a folder whose time and size
// match the index, that nothing marked as changed and that was not listed in
// the same two seconds as it last changed (FAT keeps times to two seconds),
// is answered from the index; any other folder is listed again. So the result
// is the scan's own, in its own format, and the rest of the code does not
// know the index exists. When the index cannot be opened, the scan reads the
// card as it always did.
//
// What makes a folder's time trustworthy, and what does not:
//   - Linux (the handheld) moves a folder's time when a file is created,
//     deleted or renamed in it (vfat_add_entry, fat_remove_entries), on FAT32
//     and exFAT, but not its parent's: every folder is checked on its own.
//   - Windows (and macOS) do not reliably move a FAT or exFAT folder's time
//     when a file is added: a card edited in a PC looks unchanged. So the first
//     look in each run of JukaHub, the library's Y (Rescan) and every "games
//     were added" call (refreshGameLibrary) list every folder again — names
//     only, nothing is opened — and later looks trust the times. JukaHub on
//     Windows lists every folder on every look (libIndexAlwaysList).
//   - While JukaHub runs, inotify (libindex_watch_linux.go) marks folders as
//     they change (an MTP or FTP copy, the Files app, an emulator's save), and
//     an open library refreshes itself a few seconds after the last change,
//     keeping the selected game. Past libIndexMaxWatches folders, or where
//     there is no inotify, a check of the folders' times every minute stands
//     in for it.
//
// The Games library and Home show the last library from the index first
// (libIndexShowEarly, libIndexHomeEarly) while the check runs, then the
// checked one. A damaged index, or one written by another schema, is deleted
// and built again without a word; Settings → Games & emulators → Rebuild
// library index does the same on request.

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// libIndexSchema is the index's format; another one is dropped and rebuilt.
	libIndexSchema = 1
	// libIndexFileName is the index file in the data folder.
	libIndexFileName = "library-index.db"
	// libIndexRacyWindow is how close to its last change a folder may have
	// been listed and still be trusted: FAT keeps a time to two seconds, so a
	// file added in the same two seconds as the listing leaves the time as it
	// was.
	libIndexRacyWindow = 3 * time.Second
	// libIndexMaxWatches bounds the inotify watches; the rest of the folders
	// are checked by time every libIndexCheckEvery.
	libIndexMaxWatches = 1024
	// libIndexCheckEvery is how often the folders' times are checked when
	// inotify does not cover them all.
	libIndexCheckEvery = time.Minute
	// libIndexSettle is how long a burst of changes must go quiet before an
	// open library refreshes, and libIndexSettleMax how long a steady copy can
	// hold the refresh back (each refresh is a whole library build, so a long
	// copy or install into Roms does not get one every few seconds).
	libIndexSettle    = 3 * time.Second
	libIndexSettleMax = 30 * time.Second
)

// libIndexAlwaysList lists every folder on every look, trusting no folder
// time: on Windows, where a FAT or exFAT drive's folder time does not move
// when Explorer adds a file. The content keys, disc sheets, CRCs and the
// instant library still come from the index. Tests set it.
var libIndexAlwaysList = runtime.GOOS == "windows"

var (
	libIndexBucketMeta  = []byte("meta")
	libIndexBucketDirs  = []byte("dirs")
	libIndexBucketFiles = []byte("files")
	libIndexBucketLib   = []byte("lib")
	libIndexKeySchema   = []byte("schema")
	libIndexKeySnapshot = []byte("snapshot")
)

// libIndexEnt is one name in a folder and its kind (fs.FileMode type bits:
// a folder, a link, or 0 for a plain file).
type libIndexEnt struct {
	N string `json:"n"`
	T uint32 `json:"t,omitempty"`
}

// libIndexDirRec is one folder: its time and size when it was listed, when
// that was, and what it held.
type libIndexDirRec struct {
	M int64         `json:"m"`
	S int64         `json:"s"`
	L int64         `json:"l"`
	E []libIndexEnt `json:"e"`
}

// racy reports a folder listed so soon after its last change that a file
// added in the same FAT time step would not have moved the time.
func (r *libIndexDirRec) racy() bool {
	d := r.L - r.M
	return d >= 0 && d < int64(libIndexRacyWindow)
}

// libIndexFileRec is what the index keeps for one file, each part with the
// size and time of the file it was read from: the content key
// (libraryContentKey), the files a disc sheet names, and the CRC32s
// gmetaROMCRCs gives.
type libIndexFileRec struct {
	K  string   `json:"k,omitempty"`
	KS int64    `json:"ks,omitempty"`
	KM int64    `json:"km,omitempty"`
	R  []string `json:"r,omitempty"`
	RS int64    `json:"rs,omitempty"`
	RM int64    `json:"rm,omitempty"`
	RD bool     `json:"rd,omitempty"`
	C  []string `json:"c,omitempty"`
	CS int64    `json:"cs,omitempty"`
	CM int64    `json:"cm,omitempty"`
	CD bool     `json:"cd,omitempty"`
}

// libIndexSnap is the last library built, as the Games library showed it.
type libIndexSnap struct {
	V     int               `json:"v"`
	Roots []string          `json:"roots"`
	Lib   gameLibraryReport `json:"lib"`
}

// libIndexStore is the index in memory. Every field is guarded by mu; the
// database handle is safe to use outside it.
type libIndexStore struct {
	mu     sync.Mutex
	loaded bool
	broken bool // no index this run: the scan reads the card
	path   string
	db     *bolt.DB
	dirs   map[string]*libIndexDirRec
	files  map[string]*libIndexFileRec
	// What changed in memory since the last flush.
	dirtyDirs  map[string]bool
	goneDirs   map[string]bool
	dirtyFiles map[string]bool
	goneFiles  map[string]bool
	// marked holds the folders the watcher or a file operation said changed;
	// deep asks the next look to list every folder again.
	marked     map[string]bool
	deep       bool
	reconciled bool
	// roots are the folders the last look covered; games the paths it found.
	roots []string
	games []string
	// snapHash is the hash of the library snapshot on disk, so an unchanged
	// library is not written again.
	snapHash uint64
}

var libIdx libIndexStore

// libIndexScanMu keeps two looks at the card from running at once.
var libIndexScanMu sync.Mutex

// libIndexFlushMu keeps two writes of the index in order.
var libIndexFlushMu sync.Mutex

// libIndexPathFn is where the index lives; tests point it at a temp folder.
var libIndexPathFn = libIndexDefaultPath

func libIndexDefaultPath() string {
	// A test that does not point the index at a folder of its own scans the
	// card the plain way rather than write into the data folder.
	if testing.Testing() {
		return ""
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, libIndexFileName)
}

// initMapsLocked makes the maps a note or a load writes into.
func (s *libIndexStore) initMapsLocked() {
	if s.dirs == nil {
		s.dirs = make(map[string]*libIndexDirRec)
	}
	if s.files == nil {
		s.files = make(map[string]*libIndexFileRec)
	}
	if s.dirtyDirs == nil {
		s.dirtyDirs = make(map[string]bool)
	}
	if s.goneDirs == nil {
		s.goneDirs = make(map[string]bool)
	}
	if s.dirtyFiles == nil {
		s.dirtyFiles = make(map[string]bool)
	}
	if s.goneFiles == nil {
		s.goneFiles = make(map[string]bool)
	}
	if s.marked == nil {
		s.marked = make(map[string]bool)
	}
}

// libIndexOpenDB opens the bbolt file. A damaged file can make bbolt panic
// rather than fail; that is an error here.
func libIndexOpenDB(path string) (db *bolt.DB, err error) {
	defer func() {
		if r := recover(); r != nil {
			db, err = nil, fmt.Errorf("library index unreadable: %v", r)
		}
	}()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
}

// ensureLocked loads the index once. It reports false when there is no index
// this run.
func (s *libIndexStore) ensureLocked() bool {
	s.initMapsLocked()
	if s.loaded {
		return !s.broken
	}
	s.loaded = true
	s.path = libIndexPathFn()
	if s.path == "" {
		s.broken = true
		return false
	}
	db, err := libIndexOpenDB(s.path)
	if errors.Is(err, bolt.ErrTimeout) {
		// Another JukaHub holds it: run without, never delete its file.
		logWarn("library", "library index busy (%v): scanning the card instead", err)
		s.broken = true
		return false
	}
	if err == nil && !s.readLocked(db) {
		_ = db.Close()
		db, err = nil, errors.New("old or damaged contents")
	}
	if err != nil {
		// A cache: a damaged or old one is built again from the card.
		logInfo("library", "library index %s dropped (%v); it is built again", s.path, err)
		_ = os.Remove(s.path)
		s.resetMemoryLocked()
		db, err = libIndexOpenDB(s.path)
		if err == nil && !s.readLocked(db) {
			_ = db.Close()
			db, err = nil, errors.New("cannot start a new index")
		}
		if err != nil {
			logWarn("library", "no library index this run: %v", err)
			s.broken = true
			return false
		}
	}
	s.db = db
	libIndexSeedContentKeys(s.files)
	return true
}

// resetMemoryLocked forgets everything read from the file.
func (s *libIndexStore) resetMemoryLocked() {
	s.dirs = make(map[string]*libIndexDirRec)
	s.files = make(map[string]*libIndexFileRec)
	s.dirtyDirs = make(map[string]bool)
	s.goneDirs = make(map[string]bool)
	s.dirtyFiles = make(map[string]bool)
	s.goneFiles = make(map[string]bool)
	s.snapHash = 0
}

// readLocked reads the whole index into memory: a new file gets the schema
// stamp, one with another stamp or a record that does not decode is refused.
func (s *libIndexStore) readLocked(db *bolt.DB) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	fresh := false
	err := db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(libIndexBucketMeta)
		if meta == nil {
			empty := true
			_ = tx.ForEach(func([]byte, *bolt.Bucket) error {
				empty = false
				return nil
			})
			if !empty {
				return errors.New("no schema")
			}
			fresh = true
			return nil
		}
		if string(meta.Get(libIndexKeySchema)) != strconv.Itoa(libIndexSchema) {
			return errors.New("another schema")
		}
		if b := tx.Bucket(libIndexBucketDirs); b != nil {
			if err := b.ForEach(func(k, v []byte) error {
				rec := &libIndexDirRec{}
				if err := json.Unmarshal(v, rec); err != nil {
					return err
				}
				s.dirs[string(k)] = rec
				return nil
			}); err != nil {
				return err
			}
		}
		if b := tx.Bucket(libIndexBucketFiles); b != nil {
			if err := b.ForEach(func(k, v []byte) error {
				rec := &libIndexFileRec{}
				if err := json.Unmarshal(v, rec); err != nil {
					return err
				}
				s.files[string(k)] = rec
				return nil
			}); err != nil {
				return err
			}
		}
		if b := tx.Bucket(libIndexBucketLib); b != nil {
			if v := b.Get(libIndexKeySnapshot); len(v) > 0 {
				s.snapHash = libIndexHash(v)
			}
		}
		return nil
	})
	if err != nil {
		s.resetMemoryLocked()
		return false
	}
	if fresh {
		err = db.Update(func(tx *bolt.Tx) error {
			meta, err := tx.CreateBucketIfNotExists(libIndexBucketMeta)
			if err != nil {
				return err
			}
			return meta.Put(libIndexKeySchema, []byte(strconv.Itoa(libIndexSchema)))
		})
		if err != nil {
			return false
		}
	}
	return true
}

func libIndexHash(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// libIndexClose closes the file and forgets the index in memory; the next use
// loads it again. Rebuild and the tests use it. The file is closed under both
// locks (flush, then index — their usual order): a load in between would open
// it a second time and wait on bbolt's lock.
func libIndexClose() {
	libIndexFlushMu.Lock()
	defer libIndexFlushMu.Unlock()
	libIdx.mu.Lock()
	defer libIdx.mu.Unlock()
	if libIdx.db != nil {
		_ = libIdx.db.Close()
		libIdx.db = nil
	}
	libIdx.loaded, libIdx.broken = false, false
	libIdx.reconciled, libIdx.deep = false, false
	libIdx.roots, libIdx.games = nil, nil
	libIdx.marked = nil
	libIdx.resetMemoryLocked()
}

// libIndexGuard runs one bbolt transaction, turning a panic — a file damaged
// after it opened: a bad page or free list, typical after a power cut — into
// an error. The index is then given up for this run and its file deleted, so
// the next start builds it again instead of failing the same way.
func libIndexGuard(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("library index damaged (%s): %v", what, r)
			logWarn("library", "%v; scanning the card instead, the index is built again on the next start", err)
			libIndexDiscard()
		}
	}()
	return fn()
}

// libIndexDiscard gives the index up for this run: the scan reads the card,
// and the file is closed and deleted on a goroutine of its own (the caller
// may hold the flush lock; Close waits for the transactions still open).
func libIndexDiscard() {
	libIdx.mu.Lock()
	db, path := libIdx.db, libIdx.path
	libIdx.db = nil
	libIdx.loaded, libIdx.broken = true, true
	libIdx.mu.Unlock()
	if db == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		_ = db.Close()
		if path != "" {
			_ = os.Remove(path)
		}
	}()
}

// fileLocked is path's record, made when there is none.
func (s *libIndexStore) fileLocked(path string) *libIndexFileRec {
	rec := s.files[path]
	if rec == nil {
		rec = &libIndexFileRec{}
		s.files[path] = rec
	}
	s.dirtyFiles[path] = true
	delete(s.goneFiles, path)
	return rec
}

// dropFileLocked forgets one file.
func (s *libIndexStore) dropFileLocked(path string) {
	if _, ok := s.files[path]; ok {
		delete(s.files, path)
		delete(s.dirtyFiles, path)
		s.goneFiles[path] = true
	}
}

// dropDirLocked forgets a folder and the files it held.
func (s *libIndexStore) dropDirLocked(dir string) {
	rec := s.dirs[dir]
	if rec == nil {
		return
	}
	for _, e := range rec.E {
		if !fs.FileMode(e.T).IsDir() {
			s.dropFileLocked(filepath.Join(dir, e.N))
		}
	}
	delete(s.dirs, dir)
	delete(s.dirtyDirs, dir)
	s.goneDirs[dir] = true
}

// libIndexFlush writes what changed. The records are copied under the lock
// and written outside it, so a note from the render thread never waits on
// the card.
func libIndexFlush() {
	libIndexFlushMu.Lock()
	defer libIndexFlushMu.Unlock()
	s := &libIdx
	s.mu.Lock()
	db := s.db
	if db == nil || (len(s.dirtyDirs) == 0 && len(s.goneDirs) == 0 && len(s.dirtyFiles) == 0 && len(s.goneFiles) == 0) {
		s.mu.Unlock()
		return
	}
	puts := make(map[string][]byte, len(s.dirtyDirs))
	for dir := range s.dirtyDirs {
		if rec := s.dirs[dir]; rec != nil {
			if b, err := json.Marshal(rec); err == nil {
				puts[dir] = b
			}
		}
	}
	filePuts := make(map[string][]byte, len(s.dirtyFiles))
	for path := range s.dirtyFiles {
		if rec := s.files[path]; rec != nil {
			if b, err := json.Marshal(rec); err == nil {
				filePuts[path] = b
			}
		}
	}
	gone := s.goneDirs
	goneFiles := s.goneFiles
	s.dirtyDirs, s.goneDirs = make(map[string]bool), make(map[string]bool)
	s.dirtyFiles, s.goneFiles = make(map[string]bool), make(map[string]bool)
	s.mu.Unlock()
	err := libIndexGuard("write", func() error { return libIndexWrite(db, gone, puts, goneFiles, filePuts) })
	if err != nil {
		logWarn("library", "cannot write the library index: %v", err)
	}
}

// libIndexWrite is libIndexFlush's one write transaction.
func libIndexWrite(db *bolt.DB, gone map[string]bool, puts map[string][]byte, goneFiles map[string]bool, filePuts map[string][]byte) error {
	return db.Update(func(tx *bolt.Tx) error {
		dirs, err := tx.CreateBucketIfNotExists(libIndexBucketDirs)
		if err != nil {
			return err
		}
		files, err := tx.CreateBucketIfNotExists(libIndexBucketFiles)
		if err != nil {
			return err
		}
		for k := range gone {
			if err := dirs.Delete([]byte(k)); err != nil {
				return err
			}
		}
		for k, v := range puts {
			if err := dirs.Put([]byte(k), v); err != nil {
				return err
			}
		}
		for k := range goneFiles {
			if err := files.Delete([]byte(k)); err != nil {
				return err
			}
		}
		for k, v := range filePuts {
			if err := files.Put([]byte(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// ── The scan through the index ──────────────────────────────────────────────

// romScanSource is where scanROMLibraryFrom (romsetup.go) reads the folders
// and the disc sheets: nil is the card itself.
type romScanSource interface {
	readDir(dir string) ([]os.DirEntry, error)
	walkDir(root string, fn fs.WalkDirFunc) error
	sheetRefs(sheet string) []string
}

// libIndexSource answers the scan from the index for the folders that did
// not change and lists the others, recording them.
type libIndexSource struct {
	deep    bool
	visited map[string]bool
	listed  int
}

// libIndexDirEntry is a folder entry the index remembers.
type libIndexDirEntry struct {
	dir  string
	name string
	typ  fs.FileMode
}

func (e libIndexDirEntry) Name() string      { return e.name }
func (e libIndexDirEntry) IsDir() bool       { return e.typ.IsDir() }
func (e libIndexDirEntry) Type() fs.FileMode { return e.typ }
func (e libIndexDirEntry) Info() (fs.FileInfo, error) {
	return os.Lstat(filepath.Join(e.dir, e.name))
}

// readDir is os.ReadDir for the scan: from the index when the folder's time
// and size are the ones recorded, nothing marked it and the listing was not
// racy; else from the card, recorded.
func (src *libIndexSource) readDir(dir string) ([]os.DirEntry, error) {
	key := filepath.Clean(dir)
	info, err := os.Stat(key)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return os.ReadDir(key)
	}
	src.visited[key] = true
	mod, size := info.ModTime().UnixNano(), info.Size()
	libIdx.mu.Lock()
	rec := libIdx.dirs[key]
	// Taken before the listing: a change noted while it runs marks the
	// folder again for the next look.
	marked := libIdx.marked[key]
	delete(libIdx.marked, key)
	if rec != nil && !src.deep && !marked && rec.M == mod && rec.S == size && !rec.racy() {
		out := make([]os.DirEntry, len(rec.E))
		for i, e := range rec.E {
			out[i] = libIndexDirEntry{dir: key, name: e.N, typ: fs.FileMode(e.T)}
		}
		libIdx.mu.Unlock()
		return out, nil
	}
	libIdx.mu.Unlock()
	entries, err := os.ReadDir(key)
	if err != nil {
		// What was read before the error is walked, as filepath.WalkDir
		// does; nothing is recorded and the folder is read again next time.
		libIdx.mu.Lock()
		libIdx.marked[key] = true
		libIdx.mu.Unlock()
		return entries, err
	}
	src.listed++
	fresh := &libIndexDirRec{M: mod, S: size, L: time.Now().UnixNano(), E: make([]libIndexEnt, 0, len(entries))}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		fresh.E = append(fresh.E, libIndexEnt{N: e.Name(), T: uint32(e.Type())})
		if !e.IsDir() {
			names[e.Name()] = true
		}
	}
	libIdx.mu.Lock()
	if rec != nil && rec.M == mod && rec.S == size && !rec.racy() && slices.Equal(rec.E, fresh.E) {
		// Listed again (the first look in a run) and found as recorded: the
		// record stands, and nothing is written.
		libIdx.mu.Unlock()
		return entries, nil
	}
	if rec != nil {
		for _, e := range rec.E {
			if !fs.FileMode(e.T).IsDir() && !names[e.N] {
				libIdx.dropFileLocked(filepath.Join(key, e.N))
			}
		}
	}
	libIdx.dirs[key] = fresh
	libIdx.dirtyDirs[key] = true
	delete(libIdx.goneDirs, key)
	libIdx.mu.Unlock()
	return entries, nil
}

// walkDir is filepath.WalkDir with every folder read through readDir: the
// same order (names sorted), the same calls and the same SkipDir rules.
func (src *libIndexSource) walkDir(root string, fn fs.WalkDirFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		err = fn(root, nil, err)
	} else {
		err = src.walk(root, fs.FileInfoToDirEntry(info), fn)
	}
	if err == filepath.SkipDir || err == filepath.SkipAll {
		return nil
	}
	return err
}

func (src *libIndexSource) walk(path string, d fs.DirEntry, fn fs.WalkDirFunc) error {
	if err := fn(path, d, nil); err != nil || !d.IsDir() {
		if err == filepath.SkipDir && d.IsDir() {
			err = nil
		}
		return err
	}
	entries, err := src.readDir(path)
	if err != nil {
		err = fn(path, d, err)
		if err != nil {
			if err == filepath.SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
	}
	for _, e := range entries {
		if err := src.walk(filepath.Join(path, e.Name()), e, fn); err != nil {
			if err == filepath.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

// sheetRefs is romSheetReferences, answered from the index while the sheet's
// size and time are the ones it was read at.
func (src *libIndexSource) sheetRefs(sheet string) []string {
	ext := strings.ToLower(filepath.Ext(sheet))
	if ext != ".cue" && ext != ".gdi" && ext != ".m3u" {
		return nil
	}
	info, err := os.Stat(sheet)
	if err != nil {
		return romSheetReferences(sheet)
	}
	key := filepath.Clean(sheet)
	size, mod := info.Size(), info.ModTime().UnixNano()
	libIdx.mu.Lock()
	if rec := libIdx.files[key]; rec != nil && rec.RD && rec.RS == size && rec.RM == mod {
		refs := append([]string(nil), rec.R...)
		libIdx.mu.Unlock()
		return refs
	}
	libIdx.mu.Unlock()
	refs := romSheetReferences(sheet)
	// A sheet read in the same time step as its last write may be written
	// again without its time moving (the folders' racy rule): not kept.
	if d := time.Now().UnixNano() - mod; d >= 0 && d < int64(libIndexRacyWindow) {
		return refs
	}
	libIdx.mu.Lock()
	rec := libIdx.fileLocked(key)
	rec.R, rec.RS, rec.RM, rec.RD = append([]string(nil), refs...), size, mod, true
	libIdx.mu.Unlock()
	return refs
}

// libIndexUnder reports whether path is root or inside it.
func libIndexUnder(path, root string) bool {
	if path == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(path, root)
}

func libIndexUnderAny(path string, roots []string) bool {
	for _, root := range roots {
		if libIndexUnder(path, filepath.Clean(root)) {
			return true
		}
	}
	return false
}

// libIndexReconcile is one look at the card through the index: the scan's
// own result, the number of folders read from the card, and whether the
// index was there. deep lists every folder again.
func libIndexReconcile(roots []string, deep bool) (romScanReport, int, bool) {
	libIndexScanMu.Lock()
	defer libIndexScanMu.Unlock()
	libIdx.mu.Lock()
	ok := libIdx.ensureLocked()
	deep = deep || libIdx.deep || !libIdx.reconciled || libIndexAlwaysList
	libIdx.deep = false
	libIdx.mu.Unlock()
	if !ok {
		return scanROMLibrary(roots), -1, false
	}
	src := &libIndexSource{deep: deep, visited: make(map[string]bool)}
	report := scanROMLibraryFrom(roots, src)
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		clean = append(clean, filepath.Clean(root))
	}
	games := make([]string, 0, len(report.Games))
	for _, g := range report.Games {
		games = append(games, g.Path)
	}
	libIdx.mu.Lock()
	// Folders the walk no longer reached under these roots: deleted, or a
	// console folder that is not one any more.
	for dir := range libIdx.dirs {
		if !src.visited[dir] && libIndexUnderAny(dir, clean) {
			libIdx.dropDirLocked(dir)
		}
	}
	libIdx.reconciled = true
	libIdx.roots, libIdx.games = clean, games
	libIdx.mu.Unlock()
	libIndexFlush()
	return report, src.listed, true
}

// libIndexScan is scanROMLibrary through the index (LIB-1): the same report,
// with only the folders that changed read from the card. Off the render
// thread: the first look in a run lists every folder. It also keeps the
// watcher on the folders it found and lets the background CRC pass go on.
func libIndexScan(roots []string) romScanReport {
	started := time.Now()
	report, listed, ok := libIndexReconcile(roots, false)
	if !ok {
		return report
	}
	libIdx.mu.Lock()
	dirs := make([]string, 0, len(libIdx.dirs))
	for dir := range libIdx.dirs {
		if libIndexUnderAny(dir, libIdx.roots) {
			dirs = append(dirs, dir)
		}
	}
	total := len(libIdx.dirs)
	libIdx.mu.Unlock()
	// Shallow folders first, so the console folders are the ones watched when
	// there are more folders than watches.
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := strings.Count(dirs[i], string(filepath.Separator)), strings.Count(dirs[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return dirs[i] < dirs[j]
	})
	complete := libIndexWatch(dirs)
	libIndexWatchState.Lock()
	libIndexWatchState.complete = complete
	libIndexWatchState.Unlock()
	libIndexCheckStart()
	// In the log when the card was read; a look answered wholly from the
	// index (Home's every few minutes) only at debug level.
	logAt := logDebug
	if listed > 0 {
		logAt = logInfo
	}
	logAt("library", "library index: %d games, %d of %d folders read from the card in %s",
		len(report.Games), listed, total, time.Since(started).Round(time.Millisecond))
	libIndexCRCKick()
	return report
}

// ── Content keys ────────────────────────────────────────────────────────────

// libIndexContentStamp is libraryContentKey's cache key for a file.
func libIndexContentStamp(path string, size, mod int64) string {
	return fmt.Sprintf("%s|%d|%d", path, size, mod)
}

// libIndexSeedContentKeys puts the content keys the index kept into
// libraryContentKey's cache, under the very stamps they were made for: a file
// whose size or time moved since misses and is hashed again.
func libIndexSeedContentKeys(files map[string]*libIndexFileRec) {
	libraryContentCache.mu.Lock()
	defer libraryContentCache.mu.Unlock()
	if libraryContentCache.keys == nil {
		libraryContentCache.keys = map[string]string{}
	}
	for path, rec := range files {
		if rec.K != "" {
			libraryContentCache.keys[libIndexContentStamp(path, rec.KS, rec.KM)] = rec.K
		}
	}
}

// libIndexParseStamp splits a libraryContentKey stamp ("path|size|time").
func libIndexParseStamp(stamp string) (path string, size, mod int64, ok bool) {
	last := strings.LastIndexByte(stamp, '|')
	if last <= 0 {
		return "", 0, 0, false
	}
	mid := strings.LastIndexByte(stamp[:last], '|')
	if mid <= 0 {
		return "", 0, 0, false
	}
	var err error
	if size, err = strconv.ParseInt(stamp[mid+1:last], 10, 64); err != nil {
		return "", 0, 0, false
	}
	if mod, err = strconv.ParseInt(stamp[last+1:], 10, 64); err != nil {
		return "", 0, 0, false
	}
	return stamp[:mid], size, mod, true
}

// libIndexKeepContentKeys copies the content keys made for the games of lib
// from libraryContentKey's cache into the index, so the next run does not
// read the files again. Of two stamps for one file, the newer one stands.
func libIndexKeepContentKeys(lib gameLibraryReport) {
	libIdx.mu.Lock()
	roots := libIdx.roots
	libIdx.mu.Unlock()
	// Only the card's games: the Android, HTML5 and built-in rows are not
	// files the index keeps.
	want := make(map[string]bool, lib.Total)
	for _, system := range lib.Systems {
		for _, g := range system.Games {
			if path := filepath.Clean(g.Path); libIndexUnderAny(path, roots) {
				want[path] = true
			}
		}
	}
	type keyed struct {
		key       string
		size, mod int64
	}
	found := make(map[string]keyed, len(want))
	libraryContentCache.mu.Lock()
	for stamp, key := range libraryContentCache.keys {
		path, size, mod, ok := libIndexParseStamp(stamp)
		if !ok || !want[path] {
			continue
		}
		if have, seen := found[path]; !seen || mod > have.mod {
			found[path] = keyed{key: key, size: size, mod: mod}
		}
	}
	libraryContentCache.mu.Unlock()
	libIdx.mu.Lock()
	if libIdx.db == nil {
		libIdx.mu.Unlock()
		return
	}
	for path, k := range found {
		if rec := libIdx.files[path]; rec != nil && rec.K == k.key && rec.KS == k.size && rec.KM == k.mod {
			continue
		}
		rec := libIdx.fileLocked(path)
		rec.K, rec.KS, rec.KM = k.key, k.size, k.mod
	}
	libIdx.mu.Unlock()
}

// ── The last library ────────────────────────────────────────────────────────

func libIndexSameRoots(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if filepath.Clean(a[i]) != filepath.Clean(b[i]) {
			return false
		}
	}
	return true
}

// libIndexSaveLibrary keeps lib as the last library (when it changed) and the
// content keys its build made. Off the render thread, after a build.
func libIndexSaveLibrary(roots []string, lib gameLibraryReport) {
	libIndexKeepContentKeys(lib)
	data, err := json.Marshal(libIndexSnap{V: libIndexSchema, Roots: roots, Lib: lib})
	if err != nil {
		logDebug("library", "library index: the library cannot be kept: %v", err)
		libIndexFlush()
		return
	}
	h := libIndexHash(data)
	libIndexFlush()
	// The handle is read under the flush lock, which a close also takes.
	libIndexFlushMu.Lock()
	defer libIndexFlushMu.Unlock()
	libIdx.mu.Lock()
	db := libIdx.db
	same := libIdx.snapHash == h
	libIdx.mu.Unlock()
	if db == nil || same {
		return
	}
	err = libIndexGuard("write", func() error {
		return db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists(libIndexBucketLib)
			if err != nil {
				return err
			}
			return b.Put(libIndexKeySnapshot, data)
		})
	})
	if err != nil {
		logWarn("library", "cannot keep the library in the index: %v", err)
		return
	}
	libIdx.mu.Lock()
	libIdx.snapHash = h
	libIdx.mu.Unlock()
}

// libIndexLoadLibrary is the last library kept for these roots.
func libIndexLoadLibrary(roots []string) (gameLibraryReport, bool) {
	libIdx.mu.Lock()
	ok := libIdx.ensureLocked()
	db := libIdx.db
	libIdx.mu.Unlock()
	if !ok || db == nil {
		return gameLibraryReport{}, false
	}
	var data []byte
	_ = libIndexGuard("read", func() error {
		return db.View(func(tx *bolt.Tx) error {
			if b := tx.Bucket(libIndexBucketLib); b != nil {
				data = append([]byte(nil), b.Get(libIndexKeySnapshot)...)
			}
			return nil
		})
	})
	if len(data) == 0 {
		return gameLibraryReport{}, false
	}
	var snap libIndexSnap
	if err := json.Unmarshal(data, &snap); err != nil || snap.V != libIndexSchema || !libIndexSameRoots(snap.Roots, roots) {
		return gameLibraryReport{}, false
	}
	return snap.Lib, snap.Lib.Total > 0
}

// libIndexShowEarly puts the last library on the Games library's screen
// while scan gen checks the card, when the screen has none yet (the first
// open in a run). Called first thing on the scan's goroutine.
func libIndexShowEarly(gen uint64, roots []string, early bool) {
	if !early {
		return
	}
	lib, ok := libIndexLoadLibrary(roots)
	if !ok {
		return
	}
	tbxPost(func() {
		if !gameLib.open || gen != gameLib.scanGen || !gameLib.scanning || gameLib.lib.Total > 0 {
			return
		}
		gameLib.lib = lib
		if len(gameLib.lib.Systems) > 0 {
			libIndexEarlyFirst = &gameLib.lib.Systems[0]
		}
		gameLib.recents = recentGames()
		invalidateGameLibrarySystemsCache()
		clampGameLibrarySelection()
		// A console the open asked for (openGameLibraryOnSystem) opens now
		// when the last library had it; else it waits for the scan.
		if want := gameLibraryShelfWant; want.system != "" && want.gen == gen && gameLibrarySelectSystem(want.system) {
			gameLibraryShelfWant.system = ""
		}
	})
}

// libIndexEarlyFirst is the first console of the last library
// libIndexShowEarly put on the Games library's screen (render thread): while
// gameLib.lib still starts with it, the screen shows a library nothing has
// checked against the card yet.
var libIndexEarlyFirst *librarySystem

// libIndexForgetEarly drops that library when the Games library closes before
// its scan landed, so nothing takes it for a scan of the card (Home's tabs
// adopt the library's scans). The next open shows it again from the index.
// Render thread (closeGameLibraryPanel).
func libIndexForgetEarly() {
	first := libIndexEarlyFirst
	libIndexEarlyFirst = nil
	if first != nil && len(gameLib.lib.Systems) > 0 && &gameLib.lib.Systems[0] == first {
		gameLib.lib = gameLibraryReport{}
	}
}

// libIndexHomeEarly is libIndexShowEarly for Home's Games and Collections
// tabs (hometabs_games.go): the last library while their first scan runs.
func libIndexHomeEarly(roots []string, early bool) {
	if !early {
		return
	}
	lib, ok := libIndexLoadLibrary(roots)
	if !ok {
		return
	}
	playable, runnable := homeTabsLaunchers(lib.Systems, nil)
	tbxPost(func() {
		s := &homeTabsLib
		if s.ready {
			return
		}
		homeTabsInstallLibrary(lib)
		s.playable, s.runnable = playable, runnable
		s.launchAt = time.Now()
	})
}

// ── Keeping the selected game ───────────────────────────────────────────────

// libIndexSelection is the game the Games library has selected, so a list
// refreshed under it (a copy finished, a game came back) keeps it.
type libIndexSelection struct {
	ok     bool
	system string
	path   string
	cursor int
	scroll int
}

// libIndexSelectionNow is the selection before a new list goes in. Render
// thread.
func libIndexSelectionNow() libIndexSelection {
	system, ok := selectedLibrarySystem()
	if !ok {
		return libIndexSelection{}
	}
	sel := libIndexSelection{ok: true, system: system.Name, cursor: gameLib.cursor, scroll: gameLib.scroll}
	if gameLib.cursor >= 0 && gameLib.cursor < len(system.Games) {
		sel.path = system.Games[gameLib.cursor].Path
	}
	return sel
}

// restore finds the same console and game in the new list. A game that went
// leaves the highlight on the row it was on.
func (sel libIndexSelection) restore() {
	// A checked library is in: the early one is no longer held on to.
	if first := libIndexEarlyFirst; first != nil && (len(gameLib.lib.Systems) == 0 || &gameLib.lib.Systems[0] != first) {
		libIndexEarlyFirst = nil
	}
	if !sel.ok {
		return
	}
	for i, system := range currentGameSystems() {
		if system.Name != sel.system {
			continue
		}
		gameLib.system = i
		if sel.path != "" {
			for j, g := range system.Games {
				if g.Path == sel.path {
					gameLib.cursor = j
					if j == sel.cursor {
						gameLib.scroll = sel.scroll
					}
					return
				}
			}
		}
		gameLib.cursor = min(sel.cursor, len(system.Games)-1)
		if gameLib.cursor < 0 {
			gameLib.cursor = 0
		}
		return
	}
}

// ── Changes ─────────────────────────────────────────────────────────────────

// libIndexNoteChange tells the index that JukaHub changed something at path
// (a file or folder written, moved or deleted): its folder is read again on
// the next look, and an open library refreshes once changes settle. "" means
// "games may have changed anywhere": the next look lists every folder (the
// caller starts that look itself). Any goroutine; cheap.
func libIndexNoteChange(path string) {
	libIdx.mu.Lock()
	libIdx.initMapsLocked()
	deep := strings.TrimSpace(path) == ""
	if deep {
		libIdx.deep = true
	} else {
		clean := filepath.Clean(path)
		libIdx.marked[filepath.Dir(clean)] = true
		if _, isDir := libIdx.dirs[clean]; isDir {
			libIdx.marked[clean] = true
		}
	}
	libIdx.mu.Unlock()
	if !deep {
		libIndexSettleChanges()
	}
}

// libIndexMarkDirs marks folders the watcher saw change (overflow: anything
// may have) and refreshes what is on screen once the changes settle.
func libIndexMarkDirs(dirs []string, overflow bool) {
	if len(dirs) == 0 && !overflow {
		return
	}
	libIdx.mu.Lock()
	libIdx.initMapsLocked()
	for _, d := range dirs {
		libIdx.marked[filepath.Clean(d)] = true
	}
	if overflow {
		libIdx.deep = true
	}
	libIdx.mu.Unlock()
	libIndexSettleChanges()
}

// libIndexSettleState batches a burst of changes into one refresh.
var libIndexSettleState struct {
	sync.Mutex
	armed bool
	first time.Time
	last  time.Time
}

// libIndexSettleChanges refreshes libIndexSettle after the last change of a
// burst, or libIndexSettleMax after its first during a long copy.
func libIndexSettleChanges() {
	st := &libIndexSettleState
	st.Lock()
	defer st.Unlock()
	now := time.Now()
	st.last = now
	if st.armed {
		return
	}
	st.armed, st.first = true, now
	var fire func()
	fire = func() {
		st.Lock()
		quiet := time.Since(st.last)
		held := time.Since(st.first)
		if quiet < libIndexSettle && held < libIndexSettleMax {
			wait := min(libIndexSettle-quiet, libIndexSettleMax-held)
			st.Unlock()
			time.AfterFunc(wait, fire)
			return
		}
		st.armed = false
		st.Unlock()
		libIndexChanged()
	}
	time.AfterFunc(libIndexSettle, fire)
}

// libIndexChanged refreshes the views built from the library: Home's tabs on
// their next look, and an open Games library now (or after the scan it is
// running), keeping its selection. Any goroutine.
func libIndexChanged() {
	// One refresh queued at a time: while a game holds the screen the main
	// loop runs nothing, and its saves must not queue one refresh each.
	if !libIndexChangedPending.CompareAndSwap(false, true) {
		return
	}
	tbxPost(func() {
		libIndexChangedPending.Store(false)
		homeTabsLib.at = time.Time{}
		libIndexRescanOpen(libraryRescanTries)
	})
}

var (
	libIndexChangedPending atomic.Bool
	// libIndexRescanWaiting is true while libIndexRescanOpen waits for the
	// scan running now (render thread).
	libIndexRescanWaiting bool
)

// libIndexRescanOpen rescans an open Games library through the index (no
// "list everything", unlike refreshGameLibrary) once the scan running now,
// if any, is done. Render thread.
func libIndexRescanOpen(tries int) {
	if !gameLib.open || tries <= 0 {
		libIndexRescanWaiting = false
		return
	}
	if gameLib.scanning {
		if libIndexRescanWaiting && tries == libraryRescanTries {
			return // already waiting for this scan
		}
		libIndexRescanWaiting = true
		time.AfterFunc(500*time.Millisecond, func() {
			tbxPost(func() { libIndexRescanOpen(tries - 1) })
		})
		return
	}
	libIndexRescanWaiting = false
	startGameLibraryScan()
}

// libIndexWatchState says whether inotify covers every folder; when it does
// not, libIndexCheck looks at the folders' times instead.
var libIndexWatchState struct {
	sync.Mutex
	complete bool
	started  bool
}

// libIndexCheckStart starts the time check once.
func libIndexCheckStart() {
	libIndexWatchState.Lock()
	defer libIndexWatchState.Unlock()
	if libIndexWatchState.started {
		return
	}
	libIndexWatchState.started = true
	go func() {
		for {
			time.Sleep(libIndexCheckEvery)
			libIndexWatchState.Lock()
			complete := libIndexWatchState.complete
			libIndexWatchState.Unlock()
			if complete || libIndexGameRunning() {
				continue
			}
			libIndexCheck()
		}
	}()
}

// libIndexCheck compares every indexed folder's time and size with the card
// and marks the ones that moved. Folders already marked are not told twice.
func libIndexCheck() {
	type stamp struct {
		dir       string
		mod, size int64
	}
	libIdx.mu.Lock()
	if libIdx.db == nil {
		libIdx.mu.Unlock()
		return
	}
	todo := make([]stamp, 0, len(libIdx.dirs))
	for dir, rec := range libIdx.dirs {
		if !libIdx.marked[dir] && libIndexUnderAny(dir, libIdx.roots) {
			todo = append(todo, stamp{dir: dir, mod: rec.M, size: rec.S})
		}
	}
	libIdx.mu.Unlock()
	var moved []string
	for _, st := range todo {
		info, err := os.Stat(st.dir)
		if err != nil || info.ModTime().UnixNano() != st.mod || info.Size() != st.size {
			moved = append(moved, st.dir)
		}
	}
	libIndexMarkDirs(moved, false)
}

// libIndexGameRunning reports a game (or another program) on the screen.
func libIndexGameRunning() bool {
	return runningGameChildren.Load() > 0 || foreignScreenSuspended()
}

// libIndexGameExited marks the folder of a game that just quit (an emulator
// may have written saves or a playlist beside it) and refreshes the library.
func libIndexGameExited(game libraryGame, _ string, _ time.Duration, _ error) {
	if strings.TrimSpace(game.Path) == "" {
		return
	}
	libIndexNoteChange(game.Path)
}

func init() {
	gameLaunchRegisterExited(libIndexGameExited)
}

// ── Rebuild ─────────────────────────────────────────────────────────────────

// libIndexRebuild deletes the index and builds it again from the card, off
// the render thread, then refreshes the library and Home.
func libIndexRebuild() {
	roots := append([]string(nil), romLibraryRoots()...)
	go func() {
		libIndexScanMu.Lock()
		libIdx.mu.Lock()
		path := libIdx.path
		if path == "" {
			path = libIndexPathFn()
		}
		libIdx.mu.Unlock()
		libIndexClose()
		if path != "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				logWarn("library", "cannot delete the library index: %v", err)
			}
		}
		libIndexScanMu.Unlock()
		report := libIndexScan(roots)
		logInfo("library", "library index rebuilt: %d games", len(report.Games))
		n := len(report.Games)
		tbxPost(func() {
			ShowToast("Library index rebuilt: "+pluralizeCount(n, "game", "games"), ToastKindSuccess)
			homeTabsLib.at = time.Time{}
			libIndexRescanOpen(libraryRescanTries)
		})
	}()
}

// libIndexStatsLast is libIndexStats's last answer and when it was made
// (render thread).
var libIndexStatsLast struct {
	at                   time.Time
	games, folders, crcs int
	ok                   bool
}

// libIndexStats is what the Settings row reads, every frame: the games and
// folders the index knows and how many games have their CRC. Counted at most
// once a second, and never waiting for the index (it is held while the file
// loads or a write is prepared): the last count stands meanwhile.
func libIndexStats() (games, folders, crcs int, ok bool) {
	last := &libIndexStatsLast
	if time.Since(last.at) < time.Second || !libIdx.mu.TryLock() {
		return last.games, last.folders, last.crcs, last.ok
	}
	defer libIdx.mu.Unlock()
	last.at = time.Now()
	if !libIdx.loaded || libIdx.broken {
		last.games, last.folders, last.crcs, last.ok = 0, 0, 0, false
		return 0, 0, 0, false
	}
	for _, path := range libIdx.games {
		if rec := libIdx.files[path]; rec != nil && rec.CD && len(rec.C) > 0 {
			crcs++
		}
	}
	last.games, last.folders, last.crcs, last.ok = len(libIdx.games), len(libIdx.dirs), crcs, true
	return last.games, last.folders, last.crcs, true
}

// libIndexRebuildRow is Settings → Games & emulators → Rebuild library index.
func libIndexRebuildRow() setRow {
	return setRow{
		ID: "libraryIndexRebuild", Label: "Rebuild library index", Kind: setKindAction,
		Detail: func(*Config) string {
			games, folders, crcs, ok := libIndexStats()
			if !ok {
				return "The game list JukaHub keeps so a big card opens at once; rebuilt from the card"
			}
			line := pluralizeCount(games, "game", "games") + " in " + pluralizeCount(folders, "folder", "folders")
			if crcs > 0 {
				line += " · " + itoa(crcs) + " fingerprinted"
			}
			return line + " · A reads the card again"
		},
		Open: func(*Config) {
			setToast("Rebuilding the library index…")
			libIndexRebuild()
		},
	}
}
