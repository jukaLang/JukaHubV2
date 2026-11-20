package main

// gbprinter_collect.go — the Game Boy Printer's prints, picked up after the
// game and kept in JukaHub's scrapbook.
//
// DoubleCherryGB writes each print as a PNG, but where it writes them is not
// documented anywhere we could read (see docs/GB_PRINTER.md), so nothing here
// trusts one folder. As a game starts in that core (a launch hook; the listing
// runs on a goroutine while the game boots), the folders a libretro core can
// write to are listed: the game's own folder, RetroArch's save and system
// folders, the folder RetroArch runs in, and its screenshot folder, each up to
// gbpScanMaxEntries names; in each, the top level and the
// sub-folders whose names say "print", "DoubleCherry", "dcgb" or the game's
// folder (RetroArch's sort-by-core and sort-by-content folders). After the game
// (the reaper goroutine) the same folders are listed again. A PNG that was not
// there before, or whose size or modification stamp changed, is a print. The
// stamps are only compared with themselves: the handheld boots before network
// time, so no file is ever judged by the clock.
//
// RetroArch's own pictures are left out: a screenshot is named after the game
// with a date ("<game>-251119-201502.png"), a save-state thumbnail carries
// ".state" in its name, and in the screenshot folder only a file whose name
// says "print" counts. A PNG must decode, be at least 160 pixels wide (the
// printer's paper) and either be a multiple of 160 wide or sit in a print
// folder or file name — so a cover that happens to be downloaded meanwhile is
// not taken for a print.
//
// Each print is copied (never moved: the core's folder is the core's) into
// <data>/gbprinter/scrapbook/<game>/<NNNN>.png, and scrapbook.json lists them:
// the game's title and file, its console, the order the scrapbook got them in
// (a counter, not a date), the picture's size and its SHA-256. A picture the
// scrapbook already has is not copied twice. The index is read and written
// under one lock: the reaper goroutine adds, the screen deletes, the phone
// companion reads.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// gbpScrapbookDirName is the scrapbook's folder in gbpRoot, and
	// gbpIndexName its list beside it.
	gbpScrapbookDirName = "scrapbook"
	gbpIndexName        = "scrapbook.json"
	// gbpIndexMax bounds the index read.
	gbpIndexMax = 4 << 20
	// gbpPrintMaxBytes bounds one print copied in: a DIN A4 upscale is a few
	// MB, a 160-pixel print a few KB.
	gbpPrintMaxBytes = 48 << 20
	// gbpPrintMaxPixels bounds a print's picture, so decoding one (the
	// scrapbook screen, the phone's copies) cannot run the handheld out of
	// memory: a DIN A4 page at 300 dpi is about 8.7 million pixels.
	gbpPrintMaxPixels = 16 << 20
	// gbpScanMaxEntries bounds one listing of every folder, so a ROM folder of
	// thousands of games costs a bounded look.
	gbpScanMaxEntries = 20000
	// gbpPaperWidth is the Game Boy Printer's paper: 160 pixels.
	gbpPaperWidth = 160
)

// gbpPrint is one print in the scrapbook.
type gbpPrint struct {
	ID string `json:"id"`
	// File is the print's path inside the scrapbook folder, with "/":
	// "<game folder>/0003.png".
	File string `json:"file"`
	// Game is the title the game had when it printed; ROM is its file, which
	// ties the print to the game's page.
	Game string `json:"game"`
	ROM  string `json:"rom"`
	// System is the library's console for the game ("GBC" or "Game Boy
	// Color", as the library has it); Console is its plain name.
	System  string `json:"system"`
	Console string `json:"console"`
	// Order counts the prints the scrapbook ever got: newest is highest.
	Order  int    `json:"order"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Source is the name the core gave the file.
	Source string `json:"source,omitempty"`
}

// gbpIndex is scrapbook.json.
type gbpIndex struct {
	Version int        `json:"version"`
	Next    int        `json:"next"`
	Prints  []gbpPrint `json:"prints"`
}

// gbpStore guards the index file and its copy in memory.
var gbpStore struct {
	mu     sync.Mutex
	loaded bool
	index  gbpIndex
	// rev counts changes, so the screen knows when to read the list again.
	// Atomic: the screen reads it every frame without the lock, which an
	// import holds while it writes to the card.
	rev atomic.Int64
}

// gbpRootDir is JukaHub's folder for the printer ("" when there is no data
// folder). A variable so the tests can point it at a folder of their own.
var gbpRootDir = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "gbprinter")
}

// gbpScrapbookDir is the folder the prints are kept in.
func gbpScrapbookDir() string {
	root := gbpRootDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, gbpScrapbookDirName)
}

var gbpIDPattern = regexp.MustCompile(`^p[0-9]{1,9}$`)

// gbpSafeRel reports whether rel names a print file inside the scrapbook: a
// clean, relative, slash-separated path of at most two plain names ending in
// .png — no "..", no leading dot, no backslash, no drive. The scrapbook is
// also opened as an os.Root, so a link inside it cannot lead out either; this
// check keeps a damaged or edited index from even asking. Pure.
func gbpSafeRel(rel string) bool {
	if rel == "" || len(rel) > 200 || strings.ContainsAny(rel, "\\:\x00") || strings.HasPrefix(rel, "/") {
		return false
	}
	if path.Clean(rel) != rel || !strings.EqualFold(path.Ext(rel), ".png") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") {
			return false
		}
		for _, r := range p {
			if !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.')) {
				return false
			}
		}
	}
	return true
}

// gbpParseIndex reads scrapbook.json, dropping any entry that does not name a
// safe file or has a malformed id. Pure.
func gbpParseIndex(raw []byte) (gbpIndex, error) {
	var idx gbpIndex
	if len(raw) == 0 {
		return gbpIndex{Version: 1, Next: 1}, nil
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return gbpIndex{Version: 1, Next: 1}, err
	}
	kept := idx.Prints[:0]
	seen := map[string]bool{}
	maxOrder := 0
	for _, p := range idx.Prints {
		if !gbpIDPattern.MatchString(p.ID) || !gbpSafeRel(p.File) || seen[p.ID] || p.Order < 0 {
			continue
		}
		seen[p.ID] = true
		kept = append(kept, p)
		if p.Order > maxOrder {
			maxOrder = p.Order
		}
	}
	idx.Prints = kept
	idx.Version = 1
	if idx.Next <= maxOrder {
		idx.Next = maxOrder + 1
	}
	return idx, nil
}

// gbpLoadLocked reads the index once; gbpStore.mu is held.
func gbpLoadLocked() {
	if gbpStore.loaded {
		return
	}
	gbpStore.loaded = true
	gbpStore.index = gbpIndex{Version: 1, Next: 1}
	root := gbpRootDir()
	if root == "" {
		return
	}
	f, err := os.Open(filepath.Join(root, gbpIndexName))
	if err != nil {
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, gbpIndexMax+1))
	if err != nil || len(raw) > gbpIndexMax {
		logWarn("gbprinter", "ignoring the scrapbook list: unreadable or too large")
		return
	}
	idx, err := gbpParseIndex(raw)
	if err != nil {
		logWarn("gbprinter", "the scrapbook list cannot be read (%v); prints stay in their folder", err)
	}
	gbpStore.index = idx
}

// gbpSaveLocked writes the index; gbpStore.mu is held.
func gbpSaveLocked() error {
	root := gbpRootDir()
	if root == "" {
		return errors.New("JukaHub has no data folder for the scrapbook")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(gbpStore.index, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(root, gbpIndexName), append(data, '\n'), 0o644); err != nil {
		return err
	}
	gbpStore.rev.Add(1)
	return nil
}

// gbpPrints is a copy of the scrapbook, newest first; romPath "" for every
// print, else that game's. Safe on any goroutine; the first call reads the
// file.
func gbpPrints(romPath string) ([]gbpPrint, int64) {
	gbpStore.mu.Lock()
	defer gbpStore.mu.Unlock()
	gbpLoadLocked()
	out := make([]gbpPrint, 0, len(gbpStore.index.Prints))
	for _, p := range gbpStore.index.Prints {
		if romPath == "" || gbpSameROM(p.ROM, romPath) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order > out[j].Order })
	return out, gbpStore.rev.Load()
}

// gbpRev is the scrapbook's change counter. Never waits (the screen reads it
// every frame).
func gbpRev() int64 {
	return gbpStore.rev.Load()
}

// gbpSameROM compares two game files the way the card's file system does.
func gbpSameROM(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if IsWindows() || strings.HasPrefix(a, "/mnt/") {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// gbpPrintByID finds one print.
func gbpPrintByID(id string) (gbpPrint, bool) {
	if !gbpIDPattern.MatchString(id) {
		return gbpPrint{}, false
	}
	gbpStore.mu.Lock()
	defer gbpStore.mu.Unlock()
	gbpLoadLocked()
	for _, p := range gbpStore.index.Prints {
		if p.ID == id {
			return p, true
		}
	}
	return gbpPrint{}, false
}

// gbpOpenPrint opens a print's file through the scrapbook folder as an
// os.Root: neither the index nor a link inside the folder can reach a file
// outside it. The caller closes the file.
func gbpOpenPrint(p gbpPrint) (*os.File, error) {
	if !gbpSafeRel(p.File) {
		return nil, errors.New("not a scrapbook file")
	}
	dir := gbpScrapbookDir()
	if dir == "" {
		return nil, errors.New("no scrapbook folder")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(p.File))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("not a file")
	}
	return f, nil
}

// gbpPrintSize reads an opened print's size from its PNG header and puts the
// file back at its start, so a picture is measured before it is decoded; an
// error for a file that is not a PNG or is over gbpPrintMaxPixels (the index
// and the folder may have been edited).
func gbpPrintSize(f *os.File) (int, int, error) {
	cfg, err := png.DecodeConfig(io.LimitReader(f, 64<<10))
	if _, serr := f.Seek(0, io.SeekStart); err == nil {
		err = serr
	}
	if err != nil {
		return 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > gbpPrintMaxPixels {
		return 0, 0, fmt.Errorf("%dx%d is too large to show", cfg.Width, cfg.Height)
	}
	return cfg.Width, cfg.Height, nil
}

// gbpDeletePrint removes one print and its file.
func gbpDeletePrint(id string) error {
	gbpStore.mu.Lock()
	defer gbpStore.mu.Unlock()
	gbpLoadLocked()
	at := -1
	for i, p := range gbpStore.index.Prints {
		if p.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return errors.New("that print is not in the scrapbook any more")
	}
	p := gbpStore.index.Prints[at]
	gbpStore.index.Prints = append(gbpStore.index.Prints[:at], gbpStore.index.Prints[at+1:]...)
	if err := gbpSaveLocked(); err != nil {
		gbpStore.loaded = false // read the file again next time
		return err
	}
	if dir := gbpScrapbookDir(); dir != "" && gbpSafeRel(p.File) {
		if root, err := os.OpenRoot(dir); err == nil {
			if err := root.Remove(filepath.FromSlash(p.File)); err != nil && !os.IsNotExist(err) {
				logWarn("gbprinter", "print %s left on the card: %v", p.File, err)
			}
			root.Close()
		}
	}
	logInfo("gbprinter", "deleted print %s (%s)", p.ID, p.File)
	return nil
}

// gbpGameFolder is a print folder name for a game: its title in plain ASCII
// and a short hash of its file, so two dumps of one title keep apart. Pure.
func gbpGameFolder(title, romPath string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "game"
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(romPath))))
	return slug + "-" + hex.EncodeToString(sum[:3])
}

// ── Before and after a printing game ─────────────────────────────────────

// gbpScanRoot is one folder looked at around a game.
type gbpScanRoot struct {
	dir   string
	depth int
	// named: only files whose name says "print" (RetroArch's screenshots).
	named bool
}

// gbpSeen is what a listing remembers of one PNG.
type gbpSeen struct {
	size int64
	mod  int64
}

// gbpRun is the printing launch being watched. The listing before the game
// runs on a goroutine (it may read thousands of names off the SD card, too
// slow for the launch's main thread); ready closes when it is done.
var gbpRun struct {
	mu      sync.Mutex
	rom     string
	game    libraryGame
	roots   []gbpScanRoot
	before  map[string]gbpSeen
	content string
	ready   chan struct{}
}

// gbpBeforeWait bounds how long the reaper waits for the listing before the
// game (a game that quit at once, on a slow card).
const gbpBeforeWait = 30 * time.Second

// gbpDirMatches reports whether a sub-folder may hold prints. Pure.
func gbpDirMatches(name, contentBase string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".") {
		return false
	}
	if strings.Contains(lower, "print") || strings.Contains(lower, "doublecherry") || strings.Contains(lower, "dcgb") {
		return true
	}
	return contentBase != "" && strings.EqualFold(name, contentBase)
}

// gbpPrintishName reports a file or folder name that says it is a print.
func gbpPrintishName(p string) bool {
	return strings.Contains(strings.ToLower(p), "print")
}

var gbpRAShotPattern = regexp.MustCompile(`-[0-9]{6}-[0-9]{6}\.png$`)

// gbpRetroArchPicture reports RetroArch's own pictures: a screenshot
// ("<game>-YYMMDD-HHMMSS.png") or a save-state thumbnail. Pure.
func gbpRetroArchPicture(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, ".state") || gbpRAShotPattern.MatchString(lower)
}

// gbpScanDir lists the PNGs of dir (and of matching sub-folders while depth
// allows) into seen.
func gbpScanDir(dir string, depth int, named bool, contentBase string, seen map[string]gbpSeen, budget *int) {
	if depth <= 0 || *budget <= 0 || dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		*budget--
		if *budget < 0 {
			return
		}
		name := e.Name()
		if e.IsDir() {
			if depth > 1 && gbpDirMatches(name, contentBase) {
				gbpScanDir(filepath.Join(dir, name), depth-1, named, contentBase, seen, budget)
			}
			continue
		}
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".png") || gbpRetroArchPicture(name) {
			continue
		}
		if named && !gbpPrintishName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		seen[filepath.Join(dir, name)] = gbpSeen{size: info.Size(), mod: info.ModTime().UnixNano()}
	}
}

// gbpScan lists every root, each with its own budget: a ROM folder of
// thousands of games must not leave the save folder unlisted.
func gbpScan(roots []gbpScanRoot, contentBase string) map[string]gbpSeen {
	seen := map[string]gbpSeen{}
	for _, r := range roots {
		budget := gbpScanMaxEntries
		gbpScanDir(r.dir, r.depth, r.named, contentBase, seen, &budget)
	}
	return seen
}

// gbpNewFiles is every PNG of after that before did not have, or had with
// another size or stamp, sorted by name (a core numbering its prints keeps
// their order). Pure.
func gbpNewFiles(before, after map[string]gbpSeen) []string {
	var out []string
	for p, a := range after {
		if b, ok := before[p]; ok && b == a {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// gbpLaunchConfig is the retroarch.cfg the launcher's RetroArch reads.
func gbpLaunchConfig(launcher string) (text, cfgPath string) {
	for _, p := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		if raw, err := os.ReadFile(p); err == nil {
			return string(raw), p
		}
	}
	return "", ""
}

// gbpPrintRoots are the folders a launch's prints may land in.
func gbpPrintRoots(launcher string, game libraryGame) []gbpScanRoot {
	contentDir := filepath.Dir(filepath.Clean(game.Path))
	roots := []gbpScanRoot{{dir: contentDir, depth: 2}}
	if text, cfgPath := gbpLaunchConfig(launcher); cfgPath != "" {
		for _, key := range []string{"savefile_directory", "system_directory"} {
			v, _ := raConfigGet(text, key)
			roots = append(roots, gbpScanRoot{dir: gameDiagResolveRADir(v, cfgPath, game.Path), depth: 3})
		}
		// The folder RetroArch runs in (a core writing a relative path).
		roots = append(roots, gbpScanRoot{dir: raInstallDir(cfgPath), depth: 2})
		if v, ok := raConfigGet(text, "screenshot_directory"); ok {
			roots = append(roots, gbpScanRoot{dir: gameDiagResolveRADir(v, cfgPath, game.Path), depth: 2, named: true})
		}
	}
	// One folder listed once: the save folder is often the content folder.
	var out []gbpScanRoot
	seen := map[string]int{}
	for _, r := range roots {
		if strings.TrimSpace(r.dir) == "" {
			continue
		}
		key := filepath.Clean(r.dir)
		if IsWindows() {
			key = strings.ToLower(key)
		}
		if i, ok := seen[key]; ok {
			if r.depth > out[i].depth {
				out[i].depth = r.depth
			}
			out[i].named = out[i].named && r.named
			continue
		}
		seen[key] = len(out)
		out = append(out, r)
	}
	return out
}

// gbpPrepareLaunch is the launch hook: for a Game Boy game starting in
// DoubleCherryGB, the printer's core options for this launch (written before
// RetroArch starts) and the listing the prints are found against afterwards
// (started on a goroutine: the folders are listed while the game boots, long
// before anyone can print). Main thread.
func gbpPrepareLaunch(launcher string, game libraryGame) {
	gbpRun.mu.Lock()
	gbpRun.rom, gbpRun.before, gbpRun.roots, gbpRun.ready = "", nil, nil, nil
	gbpRun.mu.Unlock()
	if !gbpIsPrinterLaunch(launcher, game) {
		return
	}
	gbpApplyCoreOptions(launcher, game)
	ready := make(chan struct{})
	gbpRun.mu.Lock()
	gbpRun.rom, gbpRun.game, gbpRun.ready = game.Path, game, ready
	gbpRun.mu.Unlock()
	go func() {
		defer close(ready)
		roots := gbpPrintRoots(launcher, game)
		contentBase := filepath.Base(filepath.Dir(filepath.Clean(game.Path)))
		before := gbpScan(roots, contentBase)
		gbpRun.mu.Lock()
		if gbpRun.ready == ready {
			gbpRun.roots, gbpRun.before, gbpRun.content = roots, before, contentBase
		}
		gbpRun.mu.Unlock()
		logInfo("gbprinter", "%s starts with the printer: watching %d folders (%d pictures there now)", filepath.Base(game.Path), len(roots), len(before))
	}()
}

// gbpGameExited is the launch hook after the game: the new prints go into the
// scrapbook and one toast says how many. The reaper goroutine.
func gbpGameExited(game libraryGame, launcher string, _ time.Duration, _ error) {
	gbpRun.mu.Lock()
	if gbpRun.rom == "" || !gbpSameROM(gbpRun.rom, game.Path) {
		gbpRun.mu.Unlock()
		return
	}
	ready := gbpRun.ready
	gbpRun.mu.Unlock()
	if ready != nil {
		select {
		case <-ready:
		case <-time.After(gbpBeforeWait):
			logWarn("gbprinter", "%s: the folders were still being listed when the game ended; prints not collected", filepath.Base(game.Path))
		}
	}
	gbpRun.mu.Lock()
	if gbpRun.ready != ready || gbpRun.before == nil {
		// Another launch took over, or the listing never finished.
		if gbpRun.ready == ready {
			gbpRun.rom, gbpRun.ready = "", nil
		}
		gbpRun.mu.Unlock()
		return
	}
	watched, roots, before, contentBase := gbpRun.game, gbpRun.roots, gbpRun.before, gbpRun.content
	gbpRun.rom, gbpRun.before, gbpRun.roots, gbpRun.ready = "", nil, nil, nil
	gbpRun.mu.Unlock()
	defer gbpNoteOptionKeys(launcher, game)
	after := gbpScan(roots, contentBase)
	files := gbpNewFiles(before, after)
	if len(files) == 0 {
		logInfo("gbprinter", "%s: no new prints", filepath.Base(game.Path))
		return
	}
	added, skipped := 0, 0
	for _, f := range files {
		ok, err := gbpImport(watched, f)
		switch {
		case err != nil:
			logWarn("gbprinter", "not a print, or not copied: %s: %v", f, err)
			skipped++
		case ok:
			added++
		}
	}
	logInfo("gbprinter", "%s: %d new prints in the scrapbook (%d other pictures left alone)", filepath.Base(game.Path), added, skipped)
	if added == 0 {
		return
	}
	msg := "1 print saved to the scrapbook"
	if added > 1 {
		msg = strconv.Itoa(added) + " prints saved to the scrapbook"
	}
	tbxPost(func() { ShowToast(msg, ToastKindSuccess) })
}

// gbpImport copies one new picture into the scrapbook when it is a print it
// does not have yet; ok is false for a duplicate.
func gbpImport(game libraryGame, src string) (bool, error) {
	f, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("not a file")
	}
	if info.Size() > gbpPrintMaxBytes {
		return false, fmt.Errorf("too large for a print (%s)", formatBytes(uint64(info.Size())))
	}
	data, err := io.ReadAll(io.LimitReader(f, gbpPrintMaxBytes+1))
	if err != nil {
		return false, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return false, errors.New("not a PNG picture")
	}
	if cfg.Width < gbpPaperWidth || cfg.Height < 8 || cfg.Width > 8192 || cfg.Height > 32768 {
		return false, fmt.Errorf("%dx%d is not a print", cfg.Width, cfg.Height)
	}
	if cfg.Width*cfg.Height > gbpPrintMaxPixels {
		return false, fmt.Errorf("%dx%d is too large for the scrapbook", cfg.Width, cfg.Height)
	}
	if cfg.Width%gbpPaperWidth != 0 && !gbpPrintishName(src) {
		return false, fmt.Errorf("%d pixels wide and not named as a print", cfg.Width)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dir := gbpScrapbookDir()
	if dir == "" {
		return false, errors.New("JukaHub has no data folder for the scrapbook")
	}
	gbpStore.mu.Lock()
	defer gbpStore.mu.Unlock()
	gbpLoadLocked()
	number := 1
	folder := gbpGameFolder(game.Label(), game.Path)
	for _, p := range gbpStore.index.Prints {
		if p.SHA256 == hash {
			return false, nil
		}
		if strings.HasPrefix(p.File, folder+"/") {
			if n, err := strconv.Atoi(strings.TrimSuffix(path.Base(p.File), path.Ext(p.File))); err == nil && n >= number {
				number = n + 1
			}
		}
	}
	rel := fmt.Sprintf("%s/%04d.png", folder, number)
	for regularFile(filepath.Join(dir, filepath.FromSlash(rel))) {
		number++
		rel = fmt.Sprintf("%s/%04d.png", folder, number)
	}
	dst := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := AtomicWrite(dst, data, 0o644); err != nil {
		return false, err
	}
	order := gbpStore.index.Next
	if order < 1 {
		order = 1
	}
	gbpStore.index.Next = order + 1
	gbpStore.index.Prints = append(gbpStore.index.Prints, gbpPrint{
		ID:      "p" + strconv.Itoa(order),
		File:    rel,
		Game:    game.Label(),
		ROM:     game.Path,
		System:  game.System,
		Console: canonicalCompatSystem(game.System),
		Order:   order,
		W:       cfg.Width,
		H:       cfg.Height,
		Size:    int64(len(data)),
		SHA256:  hash,
		Source:  filepath.Base(src),
	})
	if err := gbpSaveLocked(); err != nil {
		_ = os.Remove(dst)
		gbpStore.index.Prints = gbpStore.index.Prints[:len(gbpStore.index.Prints)-1]
		return false, err
	}
	logInfo("gbprinter", "print %s from %s (%dx%d) → %s", "p"+strconv.Itoa(order), src, cfg.Width, cfg.Height, rel)
	return true, nil
}

func init() {
	gameLaunchRegisterPrepare(gbpPrepareLaunch)
	gameLaunchRegisterExited(gbpGameExited)
}
