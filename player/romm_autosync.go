package main

// RomM auto-sync: save sync that just happens. Before a library game starts
// (pull) and after it ends (push), savesync_auto.go calls rommAutoSyncGame,
// which finds the game on the user's RomM server and runs the device sync
// negotiation for its RetroArch saves and states. "Sync all saves now" on the
// RomM page walks the whole library the same way.
//
// Matching a card file to a RomM ROM:
//   - games fetched through the RomM page record their RomM id at download;
//   - others are searched once inside the matching RomM platform by file name
//     (folder name for a multi-file game) and accepted only when the size
//     matches and, where RomM exposes one and the file is not an archive,
//     the CRC32/MD5/SHA-1 matches too;
//   - a game that is not on the server is remembered as a miss for 12 hours
//     so every launch does not search again.
//
// The links live in romm-autosync.json in the data folder: bounded, written
// atomically, and a damaged file just starts empty.

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	rommAutoSyncMaxGames  = 5000
	rommAutoSyncMaxMisses = 3000
	rommAutoSyncMissTTL   = 12 * time.Hour
	// rommAutoSyncHashLimit: bigger files are matched on name and size alone
	// (hashing a 4 GB ISO on the handheld would take minutes).
	rommAutoSyncHashLimit = 256 << 20
	// rommAutoSyncPlatformTTL keeps the server's platform list between games.
	rommAutoSyncPlatformTTL = 10 * time.Minute
	// rommAutoSyncNoSyncTTL: a server without the sync API is not asked again
	// at every launch.
	rommAutoSyncNoSyncTTL = time.Hour
)

var errRommNoSyncAPI = errors.New("this RomM server has no save sync API (it needs a RomM version with device sync, 5.2 or newer)")

// rommAutoSyncLink ties one card file to one RomM ROM on one server.
type rommAutoSyncLink struct {
	RomID  int    `json:"rom_id"`
	Size   int64  `json:"size"`
	Server string `json:"server"`
	Seen   int64  `json:"seen"`
}

// rommAutoSyncMiss is "searched, not on this server".
type rommAutoSyncMiss struct {
	Server string `json:"server"`
	Size   int64  `json:"size"`
	At     int64  `json:"at"`
}

type rommAutoSyncIndex struct {
	Version int                         `json:"version"`
	Games   map[string]rommAutoSyncLink `json:"games"`
	Misses  map[string]rommAutoSyncMiss `json:"misses"`
}

type rommAutoSyncPlatformCache struct {
	rows []rommServerPlatform
	at   time.Time
}

var rommAutoSyncStore struct {
	sync.Mutex
	loaded    bool
	dirty     bool
	index     rommAutoSyncIndex
	inflight  map[string]bool
	platforms map[string]rommAutoSyncPlatformCache
	noSync    map[string]time.Time
}

var rommAutoSyncPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "romm-autosync.json")
}

// rommAutoSyncParseIndex reads the links file. Anything unreadable gives an
// empty index; entries with no id or server are dropped.
func rommAutoSyncParseIndex(data []byte) (rommAutoSyncIndex, error) {
	var idx rommAutoSyncIndex
	err := json.Unmarshal(data, &idx)
	if err != nil {
		idx = rommAutoSyncIndex{}
	}
	clean := rommAutoSyncIndex{Version: 1, Games: map[string]rommAutoSyncLink{}, Misses: map[string]rommAutoSyncMiss{}}
	for key, link := range idx.Games {
		if key != "" && link.RomID > 0 && link.Server != "" && len(clean.Games) < rommAutoSyncMaxGames {
			clean.Games[key] = link
		}
	}
	for key, miss := range idx.Misses {
		if key != "" && miss.Server != "" && len(clean.Misses) < rommAutoSyncMaxMisses {
			clean.Misses[key] = miss
		}
	}
	return clean, err
}

// rommAutoSyncLoadLocked loads the index once. Caller holds the store lock.
func rommAutoSyncLoadLocked() {
	if rommAutoSyncStore.loaded {
		return
	}
	rommAutoSyncStore.loaded = true
	var data []byte
	if path := rommAutoSyncPath(); path != "" {
		data, _ = os.ReadFile(path)
	}
	if len(data) == 0 {
		data = []byte("{}")
	}
	idx, err := rommAutoSyncParseIndex(data)
	if err != nil {
		logWarn("romm", "romm-autosync.json is unreadable, starting a fresh game list: %s", firstLine(err.Error()))
	}
	rommAutoSyncStore.index = idx
}

// rommAutoSyncFlush writes the index when it changed, oldest entries pruned
// past the caps.
func rommAutoSyncFlush() {
	rommAutoSyncStore.Lock()
	defer rommAutoSyncStore.Unlock()
	if !rommAutoSyncStore.dirty {
		return
	}
	path := rommAutoSyncPath()
	if path == "" {
		return
	}
	idx := &rommAutoSyncStore.index
	if extra := len(idx.Games) - rommAutoSyncMaxGames; extra > 0 {
		keys := make([]string, 0, len(idx.Games))
		for key := range idx.Games {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return idx.Games[keys[i]].Seen < idx.Games[keys[j]].Seen })
		for _, key := range keys[:extra] {
			delete(idx.Games, key)
		}
	}
	if extra := len(idx.Misses) - rommAutoSyncMaxMisses; extra > 0 {
		keys := make([]string, 0, len(idx.Misses))
		for key := range idx.Misses {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return idx.Misses[keys[i]].At < idx.Misses[keys[j]].At })
		for _, key := range keys[:extra] {
			delete(idx.Misses, key)
		}
	}
	data, err := json.Marshal(idx)
	if err != nil {
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o600); err != nil {
		logWarn("romm", "could not save the RomM game list: %s", firstLine(err.Error()))
		return
	}
	rommAutoSyncStore.dirty = false
}

// rommAutoSyncKey is the index key of a card file.
func rommAutoSyncKey(romPath string) string {
	key := filepath.Clean(strings.TrimSpace(romPath))
	if IsWindows() {
		key = strings.ToLower(key)
	}
	return key
}

// rommAutoSyncServerKey names the server a link belongs to ("" when the
// address is invalid), so a link never follows the user to another server.
func rommAutoSyncServerKey(settings rommServerSettings) string {
	base, err := rommServerBase(settings.URL)
	if err != nil {
		return ""
	}
	return strings.ToLower(base.String())
}

func rommAutoSyncFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return -1
	}
	return info.Size()
}

// rommAutoSyncRemember links a card file to a RomM ROM. The RomM page calls
// it when it downloads a game; flush=false batches writes for Sync all.
func rommAutoSyncRemember(romPath string, romID int, server string, flush bool) {
	size := rommAutoSyncFileSize(romPath)
	if romID <= 0 || server == "" || size < 0 {
		return
	}
	key := rommAutoSyncKey(romPath)
	rommAutoSyncStore.Lock()
	rommAutoSyncLoadLocked()
	rommAutoSyncStore.index.Games[key] = rommAutoSyncLink{RomID: romID, Size: size, Server: server, Seen: time.Now().Unix()}
	delete(rommAutoSyncStore.index.Misses, key)
	rommAutoSyncStore.dirty = true
	rommAutoSyncStore.Unlock()
	if flush {
		rommAutoSyncFlush()
	}
}

func rommAutoSyncRememberMiss(key, server string, size int64, flush bool) {
	rommAutoSyncStore.Lock()
	rommAutoSyncLoadLocked()
	rommAutoSyncStore.index.Misses[key] = rommAutoSyncMiss{Server: server, Size: size, At: time.Now().Unix()}
	rommAutoSyncStore.dirty = true
	rommAutoSyncStore.Unlock()
	if flush {
		rommAutoSyncFlush()
	}
}

func rommAutoSyncForget(key string) {
	rommAutoSyncStore.Lock()
	rommAutoSyncLoadLocked()
	delete(rommAutoSyncStore.index.Games, key)
	rommAutoSyncStore.dirty = true
	rommAutoSyncStore.Unlock()
	rommAutoSyncFlush()
}

// rommAutoSyncLookup answers from the index: a linked id, or miss=true for a
// game searched recently and not found. A file whose size changed (replaced
// or patched) is matched again.
func rommAutoSyncLookup(key, server string, size int64) (romID int, miss bool) {
	rommAutoSyncStore.Lock()
	defer rommAutoSyncStore.Unlock()
	rommAutoSyncLoadLocked()
	if link, ok := rommAutoSyncStore.index.Games[key]; ok && link.Server == server && link.Size == size {
		return link.RomID, false
	}
	if m, ok := rommAutoSyncStore.index.Misses[key]; ok && m.Server == server && m.Size == size && time.Since(time.Unix(m.At, 0)) < rommAutoSyncMissTTL {
		return 0, true
	}
	return 0, false
}

// rommAutoSyncClaim keeps one sync per game at a time (a launch pull and a
// Sync all could otherwise race on the same save file).
func rommAutoSyncClaim(key string) bool {
	rommAutoSyncStore.Lock()
	defer rommAutoSyncStore.Unlock()
	if rommAutoSyncStore.inflight == nil {
		rommAutoSyncStore.inflight = map[string]bool{}
	}
	if rommAutoSyncStore.inflight[key] {
		return false
	}
	rommAutoSyncStore.inflight[key] = true
	return true
}

func rommAutoSyncRelease(key string) {
	rommAutoSyncStore.Lock()
	delete(rommAutoSyncStore.inflight, key)
	rommAutoSyncStore.Unlock()
}

func rommAutoSyncNoSyncKnown(server string) bool {
	rommAutoSyncStore.Lock()
	defer rommAutoSyncStore.Unlock()
	at, ok := rommAutoSyncStore.noSync[server]
	return ok && time.Since(at) < rommAutoSyncNoSyncTTL
}

func rommAutoSyncMarkNoSync(server string) {
	rommAutoSyncStore.Lock()
	if rommAutoSyncStore.noSync == nil {
		rommAutoSyncStore.noSync = map[string]time.Time{}
	}
	rommAutoSyncStore.noSync[server] = time.Now()
	rommAutoSyncStore.Unlock()
}

// rommAutoSyncPlatforms is the server's platform list, cached for a while.
func rommAutoSyncPlatforms(ctx context.Context, client *rommServerClient, server string) ([]rommServerPlatform, error) {
	rommAutoSyncStore.Lock()
	if cached, ok := rommAutoSyncStore.platforms[server]; ok && time.Since(cached.at) < rommAutoSyncPlatformTTL {
		rommAutoSyncStore.Unlock()
		return cached.rows, nil
	}
	rommAutoSyncStore.Unlock()
	rows, err := client.platforms(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) > 500 {
		rows = rows[:500]
	}
	rommAutoSyncStore.Lock()
	if rommAutoSyncStore.platforms == nil {
		rommAutoSyncStore.platforms = map[string]rommAutoSyncPlatformCache{}
	}
	if len(rommAutoSyncStore.platforms) > 4 {
		rommAutoSyncStore.platforms = map[string]rommAutoSyncPlatformCache{}
	}
	rommAutoSyncStore.platforms[server] = rommAutoSyncPlatformCache{rows: rows, at: time.Now()}
	rommAutoSyncStore.Unlock()
	return rows, nil
}

// rommAutoSyncSystemOf resolves one name (a library system, a console id, a
// card folder) to a console the RomM client knows. libName is the library's
// own name for it, used to match RomM platforms by name when the console has
// no rommSystems entry (Atari, WonderSwan, …).
func rommAutoSyncSystemOf(name string) (system rommSystem, ok bool, libName string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return rommSystem{}, false, ""
	}
	lower := strings.ToLower(name)
	if s, found := rommSystemByID(lower); found {
		return s, true, s.Name
	}
	for _, s := range rommSystems {
		if strings.EqualFold(s.Name, name) || strings.EqualFold(s.Folder, name) {
			return s, true, s.Name
		}
	}
	if alias, found := romSystemAliases[lower]; found {
		for _, s := range rommSystems {
			if s.Name == alias.Name {
				return s, true, s.Name
			}
		}
		return rommSystem{}, false, alias.Name
	}
	for _, alias := range romSystemAliases {
		if strings.EqualFold(alias.Name, name) || strings.EqualFold(alias.Collection, name) {
			return rommSystem{}, false, alias.Name
		}
	}
	return rommSystem{}, false, ""
}

// rommAutoSyncSystem tries the caller's system first, then the folders the
// file sits in (Roms/GBA/Game.gba, Roms/PS/Game/Game.cue).
func rommAutoSyncSystem(system, romPath string) (rommSystem, bool, string) {
	s, ok, libName := rommAutoSyncSystemOf(system)
	if ok {
		return s, true, libName
	}
	dir := filepath.Dir(romPath)
	for i := 0; i < 2; i++ {
		if fs, fok, fname := rommAutoSyncSystemOf(filepath.Base(dir)); fok {
			return fs, true, fname
		} else if libName == "" {
			libName = fname
		}
		dir = filepath.Dir(dir)
	}
	return rommSystem{}, false, libName
}

func rommAutoSyncNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rommAutoSyncPlatformsFor picks the server platforms a card console maps to
// (a server can have both "genesis" and "megadrive"); at most three.
func rommAutoSyncPlatformsFor(rows []rommServerPlatform, system rommSystem, haveSystem bool, libName string) []rommServerPlatform {
	var out []rommServerPlatform
	want := rommAutoSyncNorm(libName)
	for _, p := range rows {
		if p.ID <= 0 {
			continue
		}
		match := false
		if haveSystem {
			s, ok := rommServerSystem(rommServerGame{PlatformSlug: p.Slug, PlatformFS: p.FSSlug})
			match = ok && s.ID == system.ID
		} else if want != "" {
			match = rommAutoSyncNorm(p.Name) == want || rommAutoSyncNorm(p.DisplayName) == want || rommAutoSyncNorm(p.FSSlug) == want
		}
		if match {
			out = append(out, p)
			if len(out) == 3 {
				break
			}
		}
	}
	return out
}

func rommAutoSyncIsArchive(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".7z", ".rar", ".chd", ".rvz", ".gcz", ".wbfs", ".cso", ".pbp", ".ciso":
		return true
	}
	return false
}

// rommAutoSyncFolderSize sums a multi-file game's folder (bounded walk);
// -1 when it is too big to be one game or unreadable.
func rommAutoSyncFolderSize(dir string) int64 {
	var total int64
	files := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		files++
		if files > 256 {
			return errors.New("too many files")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return -1
	}
	return total
}

// rommAutoSyncHashCheck compares a file with the hash RomM published for the
// ROM. checked=false when RomM has none (or the file is too big to hash).
func rommAutoSyncHashCheck(ctx context.Context, path string, size int64, g rommServerGame) (checked, match bool, err error) {
	if size > rommAutoSyncHashLimit {
		return false, false, nil
	}
	var h hash.Hash
	want := ""
	switch {
	case strings.TrimSpace(g.CRC) != "":
		h, want = crc32.NewIEEE(), strings.ToLower(strings.TrimSpace(g.CRC))
		for len(want) < 8 {
			want = "0" + want
		}
	case strings.TrimSpace(g.MD5) != "":
		h, want = md5.New(), strings.ToLower(strings.TrimSpace(g.MD5))
	case strings.TrimSpace(g.SHA1) != "":
		h, want = sha1.New(), strings.ToLower(strings.TrimSpace(g.SHA1))
	default:
		return false, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, false, err
	}
	defer file.Close()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		n, rerr := file.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return false, false, rerr
		}
	}
	return true, hex.EncodeToString(h.Sum(nil)) == want, nil
}

// rommAutoSyncFind searches the server for the card file. found=false with no
// error means the server answered and the game is not there.
func rommAutoSyncFind(ctx context.Context, client *rommServerClient, platforms []rommServerPlatform, romPath string, size int64, folderGame bool) (rommServerGame, bool, error) {
	base := filepath.Base(romPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	folder := filepath.Base(filepath.Dir(romPath))
	term, wantSize := stem, size
	if folderGame {
		term, wantSize = folder, rommAutoSyncFolderSize(filepath.Dir(romPath))
	}
	if strings.TrimSpace(term) == "" {
		return rommServerGame{}, false, nil
	}
	for _, p := range platforms {
		items, err := client.searchGames(ctx, p.ID, term)
		if err != nil {
			return rommServerGame{}, false, err
		}
		for _, g := range items {
			if g.ID <= 0 || g.Missing || g.Physical || (g.PlatformID != 0 && g.PlatformID != p.ID) {
				continue
			}
			fsStem := strings.TrimSuffix(g.FSName, filepath.Ext(g.FSName))
			if folderGame {
				if !strings.EqualFold(g.FSName, folder) && !strings.EqualFold(fsStem, folder) {
					continue
				}
				if g.Size <= 0 || g.Size != wantSize {
					continue
				}
				return g, true, nil
			}
			if !strings.EqualFold(g.FSName, base) || g.HasMultipleFiles {
				continue
			}
			if g.Size > 0 && g.Size != size {
				continue
			}
			if rommAutoSyncIsArchive(base) {
				if g.Size == size {
					return g, true, nil
				}
				continue
			}
			checked, match, err := rommAutoSyncHashCheck(ctx, romPath, size, g)
			if err != nil {
				return rommServerGame{}, false, err
			}
			if (checked && match) || (!checked && g.Size == size) {
				return g, true, nil
			}
		}
	}
	return rommServerGame{}, false, nil
}

// rommAutoSyncIsFolderGame: a file inside its own game folder
// (Roms/PS/Game/Game.cue) rather than directly in a console folder.
func rommAutoSyncIsFolderGame(romPath string) bool {
	parent := filepath.Base(filepath.Dir(romPath))
	if _, ok, libName := rommAutoSyncSystemOf(parent); ok || libName != "" {
		return false
	}
	switch strings.ToLower(filepath.Ext(romPath)) {
	case ".m3u", ".cue", ".gdi", ".ccd", ".bin", ".img":
		return true
	}
	return false
}

// rommAutoSyncResolve finds the RomM id of a card file: the index first, then
// a search. 0 with no error = not on this server (or not a console RomM has).
func rommAutoSyncResolve(ctx context.Context, client *rommServerClient, server, romPath, system string, flush bool) (int, error) {
	size := rommAutoSyncFileSize(romPath)
	if size < 0 {
		return 0, nil
	}
	key := rommAutoSyncKey(romPath)
	if id, miss := rommAutoSyncLookup(key, server, size); id > 0 || miss {
		return id, nil
	}
	sys, haveSys, libName := rommAutoSyncSystem(system, romPath)
	if !haveSys && libName == "" {
		return 0, nil
	}
	rows, err := rommAutoSyncPlatforms(ctx, client, server)
	if err != nil {
		return 0, err
	}
	platforms := rommAutoSyncPlatformsFor(rows, sys, haveSys, libName)
	if len(platforms) == 0 {
		rommAutoSyncRememberMiss(key, server, size, flush)
		return 0, nil
	}
	g, found, err := rommAutoSyncFind(ctx, client, platforms, romPath, size, rommAutoSyncIsFolderGame(romPath))
	if err != nil {
		return 0, err
	}
	if !found {
		rommAutoSyncRememberMiss(key, server, size, flush)
		return 0, nil
	}
	rommAutoSyncRemember(romPath, g.ID, server, flush)
	return g.ID, nil
}

// rommAutoSyncRun syncs one linked game. A 404 tells apart a game deleted
// from the server (link dropped) from a server without the sync API.
func rommAutoSyncRun(ctx context.Context, client *rommServerClient, settings rommServerSettings, server, romPath string, romID int, mode rommSyncMode) (rommServerSyncResult, error) {
	result, err := client.syncSavesMode(ctx, settings, rommServerGame{ID: romID}, romPath, rommCloudSaveRoots(), mode)
	if err == nil || rommServerStatus(err) != http.StatusNotFound {
		return result, err
	}
	var probe rommServerGame
	perr := client.json(ctx, http.MethodGet, fmt.Sprintf("roms/%d", romID), nil, nil, &probe)
	switch {
	case rommServerStatus(perr) == http.StatusNotFound:
		rommAutoSyncForget(rommAutoSyncKey(romPath))
		return result, nil // the game left the server: nothing to sync
	case perr == nil:
		rommAutoSyncMarkNoSync(server)
		return result, errRommNoSyncAPI
	}
	return result, err
}

// rommAutoSyncNote is the short line shown after an automatic sync.
func rommAutoSyncNote(r rommServerSyncResult, pull bool) string {
	var parts []string
	if r.Downloaded > 0 {
		parts = append(parts, "Save pulled from RomM")
	}
	if r.Uploaded > 0 {
		parts = append(parts, "Save sent to RomM")
	}
	if r.Conflicts > 0 {
		if pull {
			parts = append(parts, "RomM has a different save: yours is kept, the server copy is beside it (.romm-conflict)")
		} else {
			parts = append(parts, "the server's other copy is kept beside your save (.romm-conflict)")
		}
	}
	return strings.Join(parts, " · ")
}

// rommAutoSyncGame syncs one library game with the RomM server. pull=true runs
// before the emulator starts (downloads only, bounded by ctx); pull=false
// after it exits (uploads, and downloads the server says are newer). It
// returns "", nil when no server is set, auto-sync is off or the game is not
// on the server; note is a short line when something changed.
func rommAutoSyncGame(ctx context.Context, romPath, system string, pull bool) (note string, err error) {
	if pull {
		// "Sync all" must not write saves under a game that is starting
		// (like saveSyncCancelFull for Google Drive).
		rommSyncAllCancel()
	}
	settings := rommServerSettingsGet()
	if !settings.autoSyncOn() || strings.TrimSpace(romPath) == "" {
		return "", nil
	}
	client, cerr := newRommServerClient(settings)
	server := rommAutoSyncServerKey(settings)
	if cerr != nil || server == "" || rommAutoSyncNoSyncKnown(server) {
		return "", nil
	}
	key := rommAutoSyncKey(romPath)
	if !rommAutoSyncClaim(key) {
		return "", nil
	}
	defer rommAutoSyncRelease(key)
	when := "end"
	mode := rommSyncPush
	if pull {
		when, mode = "start", rommSyncPull
	}
	// Matching an unlinked game can take a search and a hash. It runs on its
	// own budget: when the caller's ctx ends first the launch goes ahead and
	// the link is ready for the next sync.
	type resolved struct {
		id  int
		err error
	}
	found := make(chan resolved, 1)
	go func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer rcancel()
		id, err := rommAutoSyncResolve(rctx, client, server, romPath, system, true)
		found <- resolved{id: id, err: err}
	}()
	var r resolved
	select {
	case r = <-found:
	case <-ctx.Done():
		return "", nil
	}
	if r.err != nil {
		if rommAutoSyncUnreachable(ctx, r.err) {
			logInfo("romm", "auto-sync at game %s skipped: server unreachable", when)
			return "", nil
		}
		logWarn("romm", "auto-sync at game %s: could not look up %s: %s", when, filepath.Base(romPath), firstLine(r.err.Error()))
		return "", r.err
	}
	romID := r.id
	if romID == 0 {
		return "", nil
	}
	result, err := rommAutoSyncRun(ctx, client, settings, server, romPath, romID, mode)
	if err != nil && rommAutoSyncUnreachable(ctx, err) {
		// Offline, or the launch budget ran out: not worth a warning at
		// every launch. Finished transfers are complete files either way.
		logInfo("romm", "auto-sync at game %s for %s stopped: server unreachable or slow", when, filepath.Base(romPath))
		return rommAutoSyncNote(result, pull), nil
	}
	if err != nil {
		logWarn("romm", "auto-sync at game %s for %s: %s", when, filepath.Base(romPath), firstLine(err.Error()))
	}
	return rommAutoSyncNote(result, pull), err
}

// rommAutoSyncUnreachable: no network, DNS failure, timeout or the caller's
// budget ending, as opposed to the server answering with an error.
func rommAutoSyncUnreachable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout() // a TLS or certificate error still warns
}

// ── Sync all saves now ───────────────────────────────────────────────────

// rommSyncAll is the background "Sync all saves now" job. It keeps running
// when the RomM page closes; the page shows line while it runs.
var rommSyncAll struct {
	mu     sync.Mutex
	busy   bool
	line   string
	cancel context.CancelFunc
}

func init() { bootOnShutdown(rommSyncAllCancel) }

// rommSyncAllStatus is the job's progress line ("" when idle). It is composed
// by the worker, so the page reads it without formatting every frame.
func rommSyncAllStatus() (busy bool, line string) {
	rommSyncAll.mu.Lock()
	defer rommSyncAll.mu.Unlock()
	return rommSyncAll.busy, rommSyncAll.line
}

func rommSyncAllCancel() {
	rommSyncAll.mu.Lock()
	cancel := rommSyncAll.cancel
	rommSyncAll.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func rommSyncAllSetLine(line string) {
	rommSyncAll.mu.Lock()
	rommSyncAll.line = line
	rommSyncAll.mu.Unlock()
}

// rommSyncAllTotals is what the summary toast reports.
type rommSyncAllTotals struct {
	games, matched, uploaded, downloaded, conflicts, failed int
}

// rommSyncAllStart walks every library game, two at a time, and syncs the ones
// on the server. Main thread; the work runs on goroutines.
func rommSyncAllStart(settings rommServerSettings) {
	client, err := newRommServerClient(settings)
	server := rommAutoSyncServerKey(settings)
	if err != nil || server == "" {
		ShowToast("RomM: add the server address and API token first", ToastKindWarning)
		return
	}
	rommSyncAll.mu.Lock()
	if rommSyncAll.busy {
		rommSyncAll.mu.Unlock()
		ShowToast("RomM is already syncing every save", ToastKindInfo)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	rommSyncAll.busy, rommSyncAll.cancel, rommSyncAll.line = true, cancel, "Looking for games on the card…"
	rommSyncAll.mu.Unlock()
	ShowToast("RomM: syncing every save in the background", ToastKindInfo)
	go func() {
		defer cancel()
		totals, err := rommSyncAllRun(ctx, client, settings, server)
		rommAutoSyncFlush()
		stopped := ctx.Err() != nil
		rommSyncAll.mu.Lock()
		rommSyncAll.busy, rommSyncAll.cancel, rommSyncAll.line = false, nil, ""
		rommSyncAll.mu.Unlock()
		msg, kind := rommSyncAllSummary(totals, err, stopped)
		tbxPost(func() { ShowToast(msg, kind) })
	}()
}

func rommSyncAllSummary(t rommSyncAllTotals, err error, stopped bool) (string, ToastKind) {
	if err != nil && !stopped {
		return "RomM sync: " + firstLine(err.Error()), ToastKindWarning
	}
	head := "RomM sync done"
	if stopped {
		head = "RomM sync stopped"
	}
	msg := fmt.Sprintf("%s: %d of %d games on your server · %d sent, %d received", head, t.matched, t.games, t.uploaded, t.downloaded)
	if t.conflicts > 0 {
		msg += fmt.Sprintf(" · %d kept both copies", t.conflicts)
	}
	kind := ToastKindSuccess
	if t.failed > 0 {
		msg += fmt.Sprintf(" · %d failed (see Logs)", t.failed)
		kind = ToastKindWarning
	}
	return msg, kind
}

func rommSyncAllRun(ctx context.Context, client *rommServerClient, settings rommServerSettings, server string) (rommSyncAllTotals, error) {
	var totals rommSyncAllTotals
	if rommAutoSyncNoSyncKnown(server) {
		return totals, errRommNoSyncAPI
	}
	// Register once up front so the two workers do not both register.
	{
		rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
		err := client.registerDevice(rctx, &settings)
		rcancel()
		if st := rommServerStatus(err); st == http.StatusNotFound || st == http.StatusMethodNotAllowed {
			// RomM 3.x/4.x: no /api/devices, so no device sync either.
			rommAutoSyncMarkNoSync(server)
			return totals, errRommNoSyncAPI
		}
		if err != nil {
			return totals, err
		}
	}
	games := scanROMLibrary(romLibraryRoots()).Games
	if len(games) > rommAutoSyncMaxGames {
		games = games[:rommAutoSyncMaxGames]
	}
	totals.games = len(games)
	var mu sync.Mutex
	var fatal error
	done, logged := 0, 0
	jobs := make(chan scannedROM)
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for game := range jobs {
			if ctx.Err() != nil {
				continue
			}
			key := rommAutoSyncKey(game.Path)
			var result rommServerSyncResult
			matched := false
			var err error
			if rommAutoSyncClaim(key) {
				gctx, gcancel := context.WithTimeout(ctx, 2*time.Minute)
				var romID int
				romID, err = rommAutoSyncResolve(gctx, client, server, game.Path, game.System, false)
				if err == nil && romID > 0 {
					matched = true
					result, err = rommAutoSyncRun(gctx, client, settings, server, game.Path, romID, rommSyncBoth)
				}
				gcancel()
				rommAutoSyncRelease(key)
			}
			mu.Lock()
			done++
			if matched {
				totals.matched++
			}
			totals.uploaded += result.Uploaded
			totals.downloaded += result.Downloaded
			totals.conflicts += result.Conflicts
			if errors.Is(err, errRommNoSyncAPI) && fatal == nil {
				fatal = err
			}
			if err != nil && ctx.Err() == nil {
				totals.failed++
				if logged < 10 {
					logged++
					logWarn("romm", "sync all: %s: %s", filepath.Base(game.Path), firstLine(err.Error()))
				}
			}
			line := fmt.Sprintf("%d of %d games · %s", done, totals.games, gameDisplayTitle(game.Path))
			mu.Unlock()
			rommSyncAllSetLine(line)
		}
	}
	wg.Add(2)
	go worker()
	go worker()
	flushed := 0
feed:
	for i, game := range games {
		if gameScreenSuspended() {
			// A game holds the screen: nothing syncs under it.
			logInfo("romm", "sync all stopped: a game started")
			rommSyncAllCancel()
			break
		}
		select {
		case <-ctx.Done():
			break feed
		case jobs <- game:
		}
		mu.Lock()
		stop := fatal != nil
		mu.Unlock()
		if stop {
			break
		}
		if i-flushed >= 50 {
			flushed = i
			rommAutoSyncFlush()
		}
	}
	close(jobs)
	wg.Wait()
	return totals, fatal
}
