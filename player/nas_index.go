package main

// nas_index.go — what is on the network drives, and how it joins the Games
// library.
//
// A listing walks a drive's console folders (GBA, SFC, PS … or the folders
// the player assigned) the way the card's scan walks Roms: a file is a game by
// the same rules (romListsFile), a .cue/.gdi/.m3u keeps the files it names as
// its parts (read from the drive, at most nasSheetMaxBytes each), and on a
// console whose games are folders (DOS, ScummVM, EasyRPG) each folder is one
// game. The result is cached in <data>/nas-cache.json, so the network shelves
// show at once and while the drive is away; a listing older than
// nasListingFresh is walked again in the background when the library opens.
//
// libraryAddNetworkGames runs on the library's scan goroutine after the card's
// own games are built (game_library_ui.go): a network game whose console
// already has a game of that name on the card is left out (the card's copy
// is the one shown), the rest join their console's shelf with the path they
// will have on the card once downloaded. nasLibraryRef answers "is this a
// network game, and from which drive" from an atomic snapshot, so the
// library's rows can say so every frame without a lock or a read.
//
// Reachability is a TCP connect to each drive (nasReachProbe), at most every
// nasReachEvery while the library is on screen and at every scan; a drive that
// does not answer has its network-only games dimmed and marked Offline.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// nasMaxGamesPerSource and nasMaxGamesTotal bound the cache.
	nasMaxGamesPerSource = 20000
	nasMaxGamesTotal     = 30000
	// nasMaxDirs bounds the folders one listing opens; nasMaxDepth how deep
	// below a console folder it looks.
	nasMaxDirs  = 6000
	nasMaxDepth = 4
	// nasMaxSheets bounds the .cue/.gdi/.m3u files one listing reads.
	nasMaxSheets = 4000
	// nasFolderGameMaxFiles bounds one folder game.
	nasFolderGameMaxFiles = 5000
	// nasListingFresh is how long a listing is trusted before the library
	// walks the drive again in the background.
	nasListingFresh = 15 * time.Minute
	// nasListingRetry spaces a failed listing's retries.
	nasListingRetry = 2 * time.Minute
	// nasListingTimeout bounds one whole listing.
	nasListingTimeout = 5 * time.Minute
	// nasReachEvery is how often the library re-checks the drives.
	nasReachEvery = 30 * time.Second
	// nasReachTimeout bounds one reachability probe.
	nasReachTimeout = 2 * time.Second
)

// nasFolderGameSystems are the consoles whose games are folders (romshelf.go):
// each folder in the console folder is one game, copied whole.
var nasFolderGameSystems = map[string]bool{"DOS": true, "ScummVM": true, hbEasyRPGSystem: true}

// nasGame is one game on a drive.
type nasGame struct {
	// Folder is the console folder below the drive's games folder ("." is
	// the games folder itself); Rel is the game below it: its file, or its
	// folder for a folder game.
	Folder string `json:"f"`
	Rel    string `json:"p"`
	Sys    string `json:"s"`
	Coll   string `json:"c,omitempty"`
	Size   int64  `json:"z"`
	// Parts are the other files below Folder that come with the game (a
	// .cue's tracks, an .m3u's discs and theirs).
	Parts []string `json:"x,omitempty"`
	Dir   bool     `json:"d,omitempty"`
}

// nasFolderInfo is one console folder a listing found.
type nasFolderInfo struct {
	Name   string `json:"name"`
	System string `json:"system"`
	Games  int    `json:"games"`
}

// nasListing is one drive's games.
type nasListing struct {
	Sig     string          `json:"sig"`
	At      time.Time       `json:"at"`
	Games   []nasGame       `json:"games"`
	Folders []nasFolderInfo `json:"folders,omitempty"`
	// Others are the folders of the games folder that no console claims (the
	// mapping page offers them).
	Others []string `json:"others,omitempty"`
	Note   string   `json:"note,omitempty"`
}

type nasCacheFile struct {
	V        int                    `json:"v"`
	Listings map[string]*nasListing `json:"listings"`
}

var nasCache struct {
	mu     sync.Mutex
	loaded bool
	data   nasCacheFile
}

func nasCacheLoadLocked() {
	if nasCache.loaded {
		return
	}
	nasCache.loaded = true
	nasCache.data = nasCacheFile{V: 1, Listings: map[string]*nasListing{}}
	p := nasDataPath("nas-cache.json")
	if p == "" {
		return
	}
	var f nasCacheFile
	if _, err := readJSONWithBackup(p, &f); err != nil {
		if !os.IsNotExist(err) {
			logWarn("nas", "ignoring nas-cache.json: %v", err)
		}
		return
	}
	if f.V == 1 && f.Listings != nil {
		total := 0
		for id, l := range f.Listings {
			if l == nil || total+len(l.Games) > nasMaxGamesTotal {
				continue
			}
			total += len(l.Games)
			nasCache.data.Listings[id] = l
		}
	}
}

// nasListingSig names everything a listing depends on: another address,
// share, folder or mapping makes the cached one stale.
func nasListingSig(src nasSource) string {
	var b strings.Builder
	b.WriteString(src.Kind + "\n" + src.Host + "\n" + strconv.Itoa(src.Port) + "\n" + src.Share + "\n" + src.Root + "\n" + src.User)
	keys := make([]string, 0, len(src.Map))
	for k := range src.Map {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\n" + k + "=" + src.Map[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// nasCacheListing is src's cached listing when it still fits src (nil when
// there is none). The listing is shared, never written: read only.
func nasCacheListing(src nasSource) *nasListing {
	nasCache.mu.Lock()
	defer nasCache.mu.Unlock()
	nasCacheLoadLocked()
	l := nasCache.data.Listings[src.ID]
	if l == nil || l.Sig != nasListingSig(src) {
		return nil
	}
	return l
}

// nasCachePeek is nasCacheListing that never reads the disk: nil until the
// cache is loaded (nasCachePreload). For pages drawn every frame.
func nasCachePeek(src nasSource) *nasListing {
	nasCache.mu.Lock()
	defer nasCache.mu.Unlock()
	if !nasCache.loaded {
		return nil
	}
	l := nasCache.data.Listings[src.ID]
	if l == nil || l.Sig != nasListingSig(src) {
		return nil
	}
	return l
}

// nasCacheOtherGames counts the cached games of every drive but id, so all
// drives together stay within nasMaxGamesTotal.
func nasCacheOtherGames(id string) int {
	nasCache.mu.Lock()
	defer nasCache.mu.Unlock()
	nasCacheLoadLocked()
	n := 0
	for k, l := range nasCache.data.Listings {
		if k != id && l != nil {
			n += len(l.Games)
		}
	}
	return n
}

// nasCachePreload reads the cache file off the render thread.
func nasCachePreload() {
	go func() {
		nasCache.mu.Lock()
		nasCacheLoadLocked()
		nasCache.mu.Unlock()
	}()
}

// nasCacheStore keeps a fresh listing (nil forgets the drive's) and writes the
// file. Any goroutine.
func nasCacheStore(id string, l *nasListing) {
	nasCache.mu.Lock()
	nasCacheLoadLocked()
	if l == nil {
		delete(nasCache.data.Listings, id)
	} else {
		nasCache.data.Listings[id] = l
	}
	// Drives that are gone take their listings with them.
	known := map[string]bool{}
	for _, src := range nasSettingsGet().Sources {
		known[src.ID] = true
	}
	for k := range nasCache.data.Listings {
		if !known[k] {
			delete(nasCache.data.Listings, k)
		}
	}
	data, err := json.Marshal(nasCache.data)
	nasCache.mu.Unlock()
	if err != nil {
		return
	}
	if p := nasDataPath("nas-cache.json"); p != "" {
		if err := AtomicWrite(p, data, 0o600); err != nil {
			logWarn("nas", "could not save the network game list: %v", err)
		}
	}
}

// ── Listing a drive ──────────────────────────────────────────────────────────

type nasWalkFile struct {
	rel  string
	size int64
}

// nasBuildListing walks src's games folder. Off the render thread.
func nasBuildListing(ctx context.Context, fs nasFS, src nasSource, budget int) (*nasListing, error) {
	top := src.Root
	entries, err := fs.List(ctx, top)
	if err != nil {
		return nil, err
	}
	_, pluginByFolder := plugxROMFormats()
	type consoleDir struct {
		name string
		sys  romSystem
	}
	var consoles []consoleDir
	out := &nasListing{Sig: nasListingSig(src), At: time.Now()}
	for _, e := range entries {
		if !e.IsDir || skipScanEntryName(e.Name) || !nasSafeName(e.Name) {
			continue
		}
		if sys, ok := nasFolderSystem(e.Name, src.Map, pluginByFolder); ok {
			consoles = append(consoles, consoleDir{e.Name, sys})
		} else if src.Map[e.Name] != nasSkipMapping {
			out.Others = append(out.Others, e.Name)
		}
	}
	// The games folder can be one console's folder itself (\\nas\games\GBA).
	if sys, ok := nasFolderSystem(".", src.Map, pluginByFolder); ok {
		consoles = append(consoles, consoleDir{".", sys})
	} else if len(consoles) == 0 && top != "" {
		if sys, ok := nasFolderSystem(path.Base(top), src.Map, pluginByFolder); ok && src.Map["."] != nasSkipMapping {
			consoles = append(consoles, consoleDir{".", sys})
		}
	}
	dirs := 0
	sheets := 0
	truncated := false
	for _, con := range consoles {
		if len(out.Games) >= budget {
			truncated = true
			break
		}
		base := nasJoin(top, con.name)
		if con.name == "." {
			base = top
		}
		games, more := nasListConsole(ctx, fs, base, con.name, con.sys, &dirs, &sheets, budget-len(out.Games))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		truncated = truncated || more
		out.Games = append(out.Games, games...)
		out.Folders = append(out.Folders, nasFolderInfo{Name: con.name, System: con.sys.Name, Games: len(games)})
	}
	sort.Strings(out.Others)
	if truncated {
		out.Note = "The list stops at " + strconv.Itoa(len(out.Games)) + " games"
	}
	return out, nil
}

// nasListConsole lists one console folder's games (at most budget). more is
// true when it stopped at a limit.
func nasListConsole(ctx context.Context, fs nasFS, base, folder string, sys romSystem, dirs, sheets *int, budget int) (games []nasGame, more bool) {
	folderGames := nasFolderGameSystems[sys.Name]
	pluginByExt, _ := plugxROMFormats()
	// romListsFile reads paths only: a stand-in console folder is enough.
	fakeDir := filepath.Join(string(filepath.Separator)+"nas", "console")
	var files []nasWalkFile
	queue := []string{""}
	depth := map[string]int{"": 0}
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return nil, false
		}
		d := queue[0]
		queue = queue[1:]
		if *dirs >= nasMaxDirs {
			more = true
			break
		}
		*dirs++
		list, err := fs.List(ctx, nasJoin(base, d))
		if err != nil {
			logDebug("nas", "list %s: %v", nasJoin(base, d), err)
			continue
		}
		for _, e := range list {
			if skipScanEntryName(e.Name) || !nasSafeName(e.Name) {
				continue
			}
			rel := nasJoin(d, e.Name)
			if e.IsDir {
				if folderGames && d == "" {
					if len(games) >= budget {
						more = true
						continue
					}
					size, ok := nasFolderSize(ctx, fs, nasJoin(base, rel), dirs)
					if ok {
						games = append(games, nasGame{Folder: folder, Rel: rel, Sys: sys.Name, Coll: sys.Collection, Size: size, Dir: true})
					}
					continue
				}
				if depth[d] < nasMaxDepth {
					depth[rel] = depth[d] + 1
					queue = append(queue, rel)
				}
				continue
			}
			files = append(files, nasWalkFile{rel: rel, size: e.Size})
		}
	}
	// Which files are games, by the card's own rules.
	sizes := map[string]nasWalkFile{}
	for _, f := range files {
		sizes[strings.ToLower(f.rel)] = f
	}
	var listed []nasWalkFile
	for _, f := range files {
		if folderGames && strings.Contains(f.rel, "/") {
			continue // inside a folder game
		}
		ext := strings.ToLower(path.Ext(f.rel))
		_, pluginExt := pluginByExt[ext]
		if romListsFile(sys.Name, fakeDir, filepath.Join(fakeDir, filepath.FromSlash(f.rel)), ext) || pluginExt {
			listed = append(listed, f)
		}
	}
	// A sheet's parts are not games of their own.
	refs := map[string][]string{}
	for _, f := range listed {
		ext := strings.ToLower(path.Ext(f.rel))
		if ext != ".cue" && ext != ".gdi" && ext != ".m3u" {
			continue
		}
		if *sheets >= nasMaxSheets || f.size > nasSheetMaxBytes {
			continue
		}
		*sheets++
		data, err := nasReadSmall(ctx, fs, nasJoin(base, f.rel), nasSheetMaxBytes)
		if err != nil {
			continue
		}
		dir := path.Dir(f.rel)
		for _, ref := range nasSheetRefs(f.rel, data) {
			full := path.Clean(path.Join(dir, ref))
			if full == "." || strings.HasPrefix(full, "../") || full == ".." {
				continue
			}
			if hit, ok := sizes[strings.ToLower(full)]; ok {
				refs[strings.ToLower(f.rel)] = append(refs[strings.ToLower(f.rel)], hit.rel)
			}
		}
	}
	referenced := map[string]bool{}
	for sheet, list := range refs {
		for _, r := range list {
			if !strings.EqualFold(r, sheet) {
				referenced[strings.ToLower(r)] = true
			}
		}
	}
	for _, f := range listed {
		if referenced[strings.ToLower(f.rel)] {
			continue
		}
		if len(games) >= budget {
			more = true
			break
		}
		g := nasGame{Folder: folder, Rel: f.rel, Sys: sys.Name, Coll: sys.Collection, Size: f.size}
		// The sheet's parts, and the parts of the sheets it names (an .m3u's
		// discs and their tracks).
		seen := map[string]bool{strings.ToLower(f.rel): true}
		var walk func(key string, depth int)
		walk = func(key string, depth int) {
			for _, r := range refs[key] {
				k := strings.ToLower(r)
				if seen[k] {
					continue
				}
				seen[k] = true
				g.Parts = append(g.Parts, r)
				g.Size += sizes[k].size
				if depth > 0 {
					walk(k, depth-1)
				}
			}
		}
		walk(strings.ToLower(f.rel), 2)
		games = append(games, g)
	}
	return games, more
}

// nasFolderSize adds up a folder game's files. ok is false for an empty
// folder or one past nasFolderGameMaxFiles.
func nasFolderSize(ctx context.Context, fs nasFS, dir string, dirs *int) (int64, bool) {
	files, _, err := nasWalkFolder(ctx, fs, dir, dirs)
	if err != nil || len(files) == 0 {
		return 0, false
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	return total, true
}

// nasWalkFolder lists every file below dir (a folder game), relative to it.
func nasWalkFolder(ctx context.Context, fs nasFS, dir string, dirs *int) ([]nasWalkFile, []string, error) {
	var files []nasWalkFile
	var folders []string
	queue := []string{""}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		d := queue[0]
		queue = queue[1:]
		if dirs != nil {
			if *dirs >= nasMaxDirs {
				return nil, nil, errNasTooBig
			}
			*dirs++
		}
		list, err := fs.List(ctx, nasJoin(dir, d))
		if err != nil {
			return nil, nil, err
		}
		for _, e := range list {
			if !nasSafeName(e.Name) {
				continue
			}
			rel := nasJoin(d, e.Name)
			if e.IsDir {
				if strings.Count(rel, "/") < 8 {
					queue = append(queue, rel)
					folders = append(folders, rel)
				}
				continue
			}
			files = append(files, nasWalkFile{rel: rel, size: e.Size})
			if len(files) > nasFolderGameMaxFiles {
				return nil, nil, errNasTooBig
			}
		}
	}
	return files, folders, nil
}

// ── Refreshing listings ──────────────────────────────────────────────────────

// nasRefresh is the listing work in flight and its last outcome per drive.
var nasRefresh struct {
	mu      sync.Mutex
	running map[string]bool
	tried   map[string]time.Time
	errText map[string]string
}

// nasRefreshStatus is a drive's listing state for the page: running, and the
// last error ("" after a good listing).
func nasRefreshStatus(id string) (running bool, errText string) {
	nasRefresh.mu.Lock()
	defer nasRefresh.mu.Unlock()
	return nasRefresh.running[id], nasRefresh.errText[id]
}

// nasRefreshStart walks src again in the background unless that is already
// running; force skips the freshness and retry waits. done, when not nil, runs
// on the main thread with the outcome. Any goroutine.
func nasRefreshStart(src nasSource, force bool, done func(err error)) bool {
	// Read before the lock: the first read parses the cache file, and the
	// Network drives page takes nasRefresh.mu every frame.
	var cached *nasListing
	if !force {
		cached = nasCacheListing(src)
	}
	nasRefresh.mu.Lock()
	if nasRefresh.running == nil {
		nasRefresh.running = map[string]bool{}
		nasRefresh.tried = map[string]time.Time{}
		nasRefresh.errText = map[string]string{}
	}
	if nasRefresh.running[src.ID] {
		nasRefresh.mu.Unlock()
		return false
	}
	if !force {
		if l := cached; l != nil && time.Since(l.At) < nasListingFresh && !l.At.After(time.Now()) {
			nasRefresh.mu.Unlock()
			return false
		}
		if at, ok := nasRefresh.tried[src.ID]; ok && time.Since(at) < nasListingRetry {
			nasRefresh.mu.Unlock()
			return false
		}
	}
	nasRefresh.running[src.ID] = true
	nasRefresh.tried[src.ID] = time.Now()
	nasRefresh.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), nasListingTimeout)
		defer cancel()
		var listing *nasListing
		fs, err := nasDial(ctx, src)
		if err == nil {
			stop := context.AfterFunc(ctx, func() { fs.Close() })
			budget := min(nasMaxGamesPerSource, max(nasMaxGamesTotal-nasCacheOtherGames(src.ID), 0))
			listing, err = nasBuildListing(ctx, fs, src, budget)
			stop()
			fs.Close()
		}
		if err == nil {
			// The drive may have been edited or removed meanwhile.
			if cur, ok := nasSourceByID(src.ID); ok && nasListingSig(cur) == listing.Sig {
				nasCacheStore(src.ID, listing)
				nasReachSet(src.ID, true, "")
			}
			logInfo("nas", "%s: %d games in %d console folders", nasDisplayName(src), len(listing.Games), len(listing.Folders))
		} else {
			logInfo("nas", "%s: listing failed: %v", nasDisplayName(src), err)
		}
		text := nasErrorText(src, err)
		nasRefresh.mu.Lock()
		nasRefresh.running[src.ID] = false
		nasRefresh.errText[src.ID] = text
		nasRefresh.mu.Unlock()
		tbxPost(func() {
			if err == nil {
				nasLibraryChanged()
			}
			if done != nil {
				done(err)
			}
		})
	}()
	return true
}

// nasLibraryChanged has an open library pick up new network listings: it
// scans again (the scan merges the cache). Nothing on the card changed, so
// it is the index's usual changed-folders scan, not refreshGameLibrary's
// relist of every folder. Main thread.
func nasLibraryChanged() {
	if gameLib.open && !gameLib.scanning {
		startGameLibraryScan()
	}
}

// ── Reachability ─────────────────────────────────────────────────────────────

type nasReachState struct {
	ok  bool
	err string
}

var (
	// nasReachMap is the last probe's answer per drive (copy on write).
	nasReachMap     atomic.Pointer[map[string]nasReachState]
	nasReachRunning atomic.Bool
	nasReachMu      sync.Mutex
	// nasReachLast is when the library last asked (under nasReachMu).
	nasReachLast time.Time
	// nasReachOnline is IsOnline() at the last tick: a change (Wi-Fi gone or
	// back, another network) probes again at once. Main thread.
	nasReachOnline     bool
	nasReachOnlineSeen bool
)

// nasReachable reports whether a drive answered its last probe; known is
// false before the first one.
func nasReachable(id string) (ok, known bool) {
	m := nasReachMap.Load()
	if m == nil {
		return false, false
	}
	st, known := (*m)[id]
	return st.ok, known
}

// nasReachError is the last probe's failure in words ("" when it answered).
func nasReachError(id string) string {
	if m := nasReachMap.Load(); m != nil {
		return (*m)[id].err
	}
	return ""
}

func nasReachSet(id string, ok bool, errText string) {
	nasReachMu.Lock()
	defer nasReachMu.Unlock()
	next := map[string]nasReachState{}
	if m := nasReachMap.Load(); m != nil {
		for k, v := range *m {
			next[k] = v
		}
	}
	next[id] = nasReachState{ok: ok, err: errText}
	nasReachMap.Store(&next)
}

// nasReachProbe checks that src answers: a TCP connect to its service, or
// for a folder that the operating system can see it.
func nasReachProbe(src nasSource) error {
	if src.Kind == nasKindFolder {
		done := make(chan error, 1)
		go func() {
			_, err := os.Stat(strings.TrimSpace(src.Host))
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * nasReachTimeout):
			return context.DeadlineExceeded
		}
	}
	addr := nasDialAddress(src)
	if addr == "" {
		return errNasNotFound
	}
	conn, err := net.DialTimeout("tcp", addr, nasReachTimeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

// nasReachKick probes every drive in the background unless a probe runs, or
// one ran within nasReachEvery and the device's network is the same (force
// skips the wait). Any goroutine.
func nasReachKick(force bool) {
	if !force {
		nasReachMu.Lock()
		recent := time.Since(nasReachLast) < nasReachEvery
		nasReachMu.Unlock()
		if recent {
			return
		}
	}
	if !nasReachRunning.CompareAndSwap(false, true) {
		return
	}
	nasReachMu.Lock()
	nasReachLast = time.Now()
	nasReachMu.Unlock()
	sources := nasSettingsGet().Sources
	go func() {
		defer nasReachRunning.Store(false)
		var wg sync.WaitGroup
		for _, src := range sources {
			wg.Add(1)
			go func(src nasSource) {
				defer wg.Done()
				err := nasReachProbe(src)
				nasReachSet(src.ID, err == nil, nasErrorText(src, err))
			}(src)
		}
		wg.Wait()
	}()
}

// ── The library ──────────────────────────────────────────────────────────────

// nasLibRef is what the library knows about one network game.
type nasLibRef struct {
	Source string
	Name   string
	Game   nasGame
	// Folder is the card's console folder the game goes to.
	Folder string
}

type nasLibSnapshot struct {
	byPath map[string]nasLibRef
}

var nasLibIndex atomic.Pointer[nasLibSnapshot]

// nasLibraryRef answers whether path is a network-only game in the library.
// Any goroutine; no I/O.
func nasLibraryRef(path string) (nasLibRef, bool) {
	snap := nasLibIndex.Load()
	if snap == nil || path == "" {
		return nasLibRef{}, false
	}
	ref, ok := snap.byPath[path]
	return ref, ok
}

// nasDupKey is how a game is the same game on the card and on a drive: its
// console and its name without the extension, case folded.
func nasDupKey(system, stem string) string {
	return strings.ToLower(system) + "\x00" + strings.ToLower(strings.TrimSpace(stem))
}

// libraryAddNetworkGames adds the drives' cached games to a built library.
// On the scan goroutine, after the card's games are in (game_library_ui.go):
// it reads the cache and the card's Roms folder, never the network, and asks
// for a listing refresh and a reachability probe in the background.
func libraryAddNetworkGames(lib *gameLibraryReport, seconds map[string]int64) {
	if lib == nil {
		return
	}
	sources := nasSettingsGet().Sources
	if len(sources) == 0 {
		nasLibIndex.Store(&nasLibSnapshot{})
		return
	}
	nasReachKick(true)
	for _, src := range sources {
		nasRefreshStart(src, false, nil)
	}
	romsRoot := nasRomsRoot()
	localFolders := nasLocalSystemFolders(romsRoot)
	artRoot := libraryArtworkRoot()
	have := map[string]bool{}
	for _, sys := range lib.Systems {
		for _, g := range sys.Games {
			have[nasDupKey(g.System, romGameStem(g.Path))] = true
		}
	}
	snap := &nasLibSnapshot{byPath: map[string]nasLibRef{}}
	added := map[string][]libraryGame{}
	for _, src := range sources {
		listing := nasCacheListing(src)
		if listing == nil {
			continue
		}
		name := nasDisplayName(src)
		for _, g := range listing.Games {
			folder := localFolders[g.Sys]
			if folder == "" {
				folder = nasDefaultLocalFolder(g.Sys, g.Folder)
			}
			p, err := nasLocalPath(romsRoot, folder, g.Rel)
			if err != nil {
				continue
			}
			if g.Dir && dirExists(p) {
				continue // that folder game is on the card already (under another title)
			}
			stem := romGameStem(p)
			if g.Dir {
				stem = path.Base(g.Rel)
			}
			key := nasDupKey(g.Sys, stem)
			if have[key] {
				continue
			}
			have[key] = true
			title := gameDisplayTitle(p)
			if g.Dir {
				title = gameDisplayTitle(path.Base(g.Rel) + ".dir")
			}
			if title == "" {
				continue
			}
			art := romCoverPath(scannedROM{Path: p, System: g.Sys, Collection: g.Coll}, artRoot)
			added[g.Sys] = append(added[g.Sys], libraryGame{
				Path:   p,
				System: g.Sys,
				Title:  title,
				Art:    art,
				HasArt: artworkCoverPresent(art),
			})
			snap.byPath[p] = nasLibRef{Source: src.ID, Name: name, Game: g, Folder: folder}
		}
	}
	if len(added) > 0 {
		// A copy of the consoles: the library index keeps the card-only
		// library it was handed (libIndexSaveLibrary), which must not change.
		lib.Systems = append([]librarySystem(nil), lib.Systems...)
		mode := librarySortMode()
		for sysName, games := range added {
			libraryDecorateGames(games, seconds)
			idx := -1
			for i := range lib.Systems {
				if lib.Systems[i].Name == sysName {
					idx = i
					break
				}
			}
			if idx < 0 {
				lib.Systems = append(lib.Systems, librarySystem{Name: sysName})
				idx = len(lib.Systems) - 1
			}
			sys := &lib.Systems[idx]
			merged := make([]libraryGame, 0, len(sys.Games)+len(games))
			merged = append(merged, sys.Games...)
			merged = append(merged, games...)
			sortLibraryGames(merged, mode)
			sys.Games = merged
			withArt := countCoveredGames(games)
			sys.WithArt += withArt
			lib.Total += len(games)
			lib.WithArt += withArt
		}
		sortLibrarySystems(lib.Systems)
	}
	nasLibIndex.Store(snap)
}

// nasLibraryCardOnly is lib without its network-only games, as a copy (lib
// is not changed). Home's tabs start games straight from their tiles, where
// a game that is not on the card has no download sheet (hometabs_games.go).
// Render thread; no I/O.
func nasLibraryCardOnly(lib gameLibraryReport) gameLibraryReport {
	snap := nasLibIndex.Load()
	if snap == nil || len(snap.byPath) == 0 {
		return lib
	}
	out := lib
	out.Systems = make([]librarySystem, 0, len(lib.Systems))
	removed, removedArt := 0, 0
	for _, sys := range lib.Systems {
		keep := make([]libraryGame, 0, len(sys.Games))
		for _, g := range sys.Games {
			if _, onNet := snap.byPath[g.Path]; !onNet {
				keep = append(keep, g)
			}
		}
		if n := len(sys.Games) - len(keep); n > 0 {
			removed += n
			withArt := countCoveredGames(keep)
			removedArt += sys.WithArt - withArt
			if len(keep) == 0 {
				continue // a console only the drives fill
			}
			sys.Games, sys.WithArt = keep, withArt
		}
		out.Systems = append(out.Systems, sys)
	}
	if removed == 0 {
		return lib
	}
	out.Total = max(lib.Total-removed, 0)
	out.WithArt = max(lib.WithArt-removedArt, 0)
	return out
}

// nasLibraryStatusSplit takes the network-only games out of the library's
// count for its status line: onCard games, and a note like " · 12 games on
// network drives" ("" when there are none). Main thread; no I/O.
func nasLibraryStatusSplit(total int) (onCard int, note string) {
	snap := nasLibIndex.Load()
	if snap == nil {
		return total, ""
	}
	n := len(snap.byPath)
	if n <= 0 || n > total {
		return total, ""
	}
	return total - n, " · " + pluralizeCount(n, "game on a network drive", "games on network drives")
}

// nasLibraryTick keeps the drives' reachability current while the library
// is drawn: a probe at most every nasReachEvery. Main thread, no I/O.
func nasLibraryTick() {
	if snap := nasLibIndex.Load(); snap == nil || len(snap.byPath) == 0 {
		return
	}
	online := IsOnline()
	changed := nasReachOnlineSeen && online != nasReachOnline
	nasReachOnline, nasReachOnlineSeen = online, true
	nasReachKick(changed)
}

// nasLibraryOffline reports whether ref's drive did not answer its last probe.
func nasLibraryOffline(ref nasLibRef) bool {
	ok, known := nasReachable(ref.Source)
	return known && !ok
}

// nasLibraryRowNote is what a library row's second line adds for a network
// game: where it is, or that it cannot be reached now, or its download.
// Main thread, no I/O.
func nasLibraryRowNote(path string) string {
	ref, ok := nasLibraryRef(path)
	if !ok {
		return ""
	}
	nasLibraryTick()
	if pct, ok := nasDownloadPercent(path); ok {
		return " · Downloading " + strconv.Itoa(pct) + "%"
	}
	if nasLibraryOffline(ref) {
		return " · Offline · " + ref.Name
	}
	return " · On " + ref.Name
}

// nasLibraryDimRow dims a library row whose network game cannot be reached
// now. Main thread, no I/O.
func nasLibraryDimRow(renderer *sdl.Renderer, rect sdl.Rect, path string) {
	ref, ok := nasLibraryRef(path)
	if !ok || !nasLibraryOffline(ref) {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 12, sdl.Color{R: 0, G: 0, B: 0, A: 120})
}

// nasLibraryArtBadge marks a network game's cover: "NAS", or "Offline" (and
// the cover dimmed) when its drive does not answer. Main thread, no I/O.
func nasLibraryArtBadge(renderer *sdl.Renderer, game libraryGame, rect sdl.Rect) {
	ref, ok := nasLibraryRef(game.Path)
	if !ok || rect.W < 48 || rect.H < 32 {
		return
	}
	nasLibraryTick()
	fonts, ok := tbxLoadFonts(appConfig)
	if !ok || fonts.tiny == nil {
		return
	}
	text, tone := "NAS", tbxToneAccent
	if pct, ok := nasDownloadPercent(game.Path); ok {
		text, tone = strconv.Itoa(pct)+"%", tbxToneGood
	} else if nasLibraryOffline(ref) {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, sdl.Color{R: 0, G: 0, B: 0, A: 120})
		text, tone = "Offline", tbxToneWarn
	}
	pw, ph := tbxPillSize(fonts.tiny, text)
	if pw > rect.W-8 {
		return
	}
	tbxDrawPill(renderer, appConfig, fonts.tiny, rect.X+rect.W-6, rect.Y+rect.H-6-ph/2, text, tone)
}
