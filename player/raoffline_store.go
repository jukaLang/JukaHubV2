package main

// raoffline_store.go — what Offline RetroAchievements (raoffline.go) keeps
// on the card: the server's answers RetroArch needs to start a game with no
// internet, the sign-in it needs to log in, and the unlocks waiting to be
// sent.
//
// All of it lives in ra-offline/ in JukaHub's data folder:
//
//   - index.json: which answers are cached (one body file each under cache/,
//     named by a hash of the answer's key, so no request value ever becomes
//     a path), the game ID each game hash resolved to, the ROM each hash
//     came from (for "ready offline" in Settings and the Wi-Fi wait), and the
//     unlocks the server confirmed since a game's cached session answer.
//   - login.json (0600): per account, the last login answer, the login token
//     RetroArch used and a salted hash of a password sign-in. The token is
//     needed to answer RetroArch's login offline and to send queued unlocks;
//     it is never logged. RetroArch keeps the same token in retroarch.cfg.
//   - queue.json (0600): the unlocks earned while the server could not be
//     reached, without the token (the current one is added when they go).
//
// Size: the cached answers and badge pictures together stay under
// raoCacheMaxBytes; the least recently used go first. The order is a counter,
// never the wall clock (a TrimUI boots before NTP).
//
// How old an unlock is. The server can be told how long ago an unlock
// happened (rcheevos' "o", seconds since the unlock), so a game played on a
// plane shows the right moment. The age is counted with Go's monotonic clock
// while JukaHub runs (AgeSec plus the time since anchor) and written down
// with the queue; the time the handheld was switched off is not counted, so
// the age is a lower bound, never a time in the future.

import (
	"bytes"
	"crypto/md5" // the RetroAchievements award signature is MD5 by protocol
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	raoDirName      = "ra-offline"
	raoIndexName    = "index.json"
	raoLoginName    = "login.json"
	raoQueueName    = "queue.json"
	raoCacheDirName = "cache"
	raoSchema       = 1

	// raoCacheMaxBytes and raoCacheMaxEntries bound the cached answers and
	// pictures; raoEntryMaxBytes bounds one (a big achievement set is about
	// a megabyte).
	raoCacheMaxBytes   = 64 << 20
	raoCacheMaxEntries = 6000
	raoEntryMaxBytes   = 8 << 20
	// raoFileMaxBytes bounds a read of index.json, login.json or queue.json.
	raoFileMaxBytes = 8 << 20
	// raoQueueMax bounds the unlocks waiting; raoUnlocksPerGame and
	// raoPathsMax bound the index's lists.
	raoQueueMax       = 2000
	raoUnlocksPerGame = 2000
	raoPathsMax       = 4000
	// raoLaunchLink is how long after a routed launch the first game hash
	// RetroArch resolves is taken to be that launch's ROM.
	raoLaunchLink = 10 * time.Minute
	// raoAgesEvery is how often the unlock ages are written while unlocks wait.
	raoAgesEvery = 10 * time.Minute
	// raoIndexSaveDelay gathers the index writes of a game's start (its
	// answers and dozens of badges) into one.
	raoIndexSaveDelay = 2 * time.Second
)

// raoEntry is one cached answer or picture.
type raoEntry struct {
	File string `json:"file"`
	Size int64  `json:"size"`
	Use  uint64 `json:"use"`
	Game int    `json:"game,omitempty"`
}

// raoUnlock is an unlock the server confirmed after a game's session answer
// was cached.
type raoUnlock struct {
	ID   int  `json:"id"`
	Hard bool `json:"hard,omitempty"`
}

type raoIndex struct {
	Schema  int                    `json:"schema"`
	Seq     uint64                 `json:"seq"`
	Entries map[string]*raoEntry   `json:"entries"`
	Games   map[string]int         `json:"games"`
	Paths   map[string]string      `json:"paths"`
	Unlocks map[string][]raoUnlock `json:"unlocks"`
}

// raoLogin is one account's offline sign-in.
type raoLogin struct {
	User     string          `json:"user"`
	Token    string          `json:"token"`
	PassHash string          `json:"passHash,omitempty"`
	Body     json.RawMessage `json:"body"`
	Agent    string          `json:"agent,omitempty"`
}

type raoLoginFile struct {
	Schema int                  `json:"schema"`
	Last   string               `json:"last,omitempty"`
	Logins map[string]*raoLogin `json:"logins"`
}

// raoQueued is one unlock waiting to be sent: RetroArch's own request
// without the token.
type raoQueued struct {
	ID     string            `json:"id"`
	User   string            `json:"user"`
	Game   int               `json:"game,omitempty"`
	Params map[string]string `json:"params"`
	Agent  string            `json:"agent,omitempty"`
	AgeSec float64           `json:"ageSec"`
	Tries  int               `json:"tries,omitempty"`
	// Verbatim: the server refused the request with the unlock's age added,
	// so it goes as RetroArch sent it.
	Verbatim bool `json:"verbatim,omitempty"`
	// anchor is the monotonic moment AgeSec was last brought up to date.
	anchor time.Time
}

// age is how long ago the unlock happened, as far as JukaHub saw.
func (q *raoQueued) age() time.Duration {
	d := time.Duration(q.AgeSec * float64(time.Second))
	if !q.anchor.IsZero() {
		d += time.Since(q.anchor)
	}
	return d
}

type raoQueueFile struct {
	Schema   int          `json:"schema"`
	Items    []*raoQueued `json:"items"`
	Sent     int          `json:"sent,omitempty"`
	Rejected int          `json:"rejected,omitempty"`
}

// rao is the store in memory; every field under mu. The proxy's handlers,
// the replay worker and the launch path all use it.
var rao struct {
	mu     sync.Mutex
	loaded bool
	dir    string
	index  raoIndex
	logins raoLoginFile
	queue  raoQueueFile
	// indexDirty: a change not written yet; saveTimer writes it soon.
	indexDirty bool
	saveTimer  *time.Timer
	// launchPath is the ROM of the last routed launch, launchAt when.
	launchPath string
	launchAt   time.Time
	// curGame is the game each account's session is in, so an unlock knows
	// its game.
	curGame map[string]int
	// loginGen counts the logins stored, so a replay waiting for a new token
	// knows one came.
	loginGen  uint64
	agesSaved time.Time
}

// raoCounts are the numbers Settings shows, published after each change so
// drawing never waits for rao.mu.
var raoCounts struct {
	pending  atomic.Int64
	games    atomic.Int64
	sent     atomic.Int64
	rejected atomic.Int64
}

// raoDataDir is ra-offline/ in JukaHub's data folder, "" when there is none.
func raoDataDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, raoDirName)
}

// raoReadJSON reads one of the store's files, bounded, falling back to the
// .bak AtomicWrite keeps. A missing file is not an error.
func raoReadJSON(path string, v any) error {
	if info, err := os.Stat(path); err == nil && info.Size() > raoFileMaxBytes {
		return fmt.Errorf("%s is too large", filepath.Base(path))
	}
	if _, err := readJSONWithBackup(path, v); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// raoLoadLocked reads the store the first time it is needed.
func raoLoadLocked() {
	if rao.loaded {
		return
	}
	rao.loaded = true
	rao.dir = raoDataDir()
	rao.curGame = map[string]int{}
	rao.index = raoIndex{Schema: raoSchema}
	rao.logins = raoLoginFile{Schema: raoSchema}
	rao.queue = raoQueueFile{Schema: raoSchema}
	if rao.dir != "" {
		var idx raoIndex
		if err := raoReadJSON(filepath.Join(rao.dir, raoIndexName), &idx); err != nil {
			logWarn("raoffline", "starting the offline cache again: %v", err)
		} else if idx.Schema == raoSchema {
			rao.index = idx
		}
		var logins raoLoginFile
		if err := raoReadJSON(filepath.Join(rao.dir, raoLoginName), &logins); err != nil {
			logWarn("raoffline", "ignoring the offline sign-in file: unreadable")
		} else if logins.Schema == raoSchema {
			rao.logins = logins
		}
		var queue raoQueueFile
		if err := raoReadJSON(filepath.Join(rao.dir, raoQueueName), &queue); err != nil {
			logWarn("raoffline", "the offline unlock queue could not be read: %v", err)
		} else if queue.Schema == raoSchema {
			rao.queue = queue
		}
	}
	if rao.index.Entries == nil {
		rao.index.Entries = map[string]*raoEntry{}
	}
	if rao.index.Games == nil {
		rao.index.Games = map[string]int{}
	}
	if rao.index.Paths == nil {
		rao.index.Paths = map[string]string{}
	}
	if rao.index.Unlocks == nil {
		rao.index.Unlocks = map[string][]raoUnlock{}
	}
	if rao.logins.Logins == nil {
		rao.logins.Logins = map[string]*raoLogin{}
	}
	// Entries whose file is not a plain name of ours are dropped: the index
	// is ours, but a hand-edited one must not point anywhere else.
	for key, e := range rao.index.Entries {
		if e == nil || !raoCacheFileName(e.File) {
			delete(rao.index.Entries, key)
		}
	}
	now := time.Now()
	clean := rao.queue.Items[:0]
	for _, q := range rao.queue.Items {
		if q == nil || q.Params == nil || q.User == "" {
			continue
		}
		q.anchor = now
		clean = append(clean, q)
	}
	rao.queue.Items = clean
	rao.agesSaved = now
	raoPublishLocked()
}

// raoCacheFileName accepts the names raoFileFor makes and nothing else.
func raoCacheFileName(name string) bool {
	if len(name) != 40+5 || !strings.HasSuffix(name, ".data") {
		return false
	}
	for _, r := range name[:40] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// raoFileFor is a cached answer's file name: the SHA-1 of its key.
func raoFileFor(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:]) + ".data"
}

// raoPublishLocked refreshes the numbers Settings shows.
func raoPublishLocked() {
	raoCounts.pending.Store(int64(len(rao.queue.Items)))
	raoCounts.sent.Store(int64(rao.queue.Sent))
	raoCounts.rejected.Store(int64(rao.queue.Rejected))
	raoCounts.games.Store(int64(raoGamesCachedLocked()))
}

// raoGamesCachedLocked counts the games whose achievement data is cached.
func raoGamesCachedLocked() int {
	games := map[int]bool{}
	for key, e := range rao.index.Entries {
		if e.Game > 0 && (strings.HasPrefix(key, "patch:") || strings.HasPrefix(key, "sets:")) {
			games[e.Game] = true
		}
	}
	return len(games)
}

// ── Saving ───────────────────────────────────────────────────────────────

func raoSaveIndexLocked() {
	if rao.dir == "" {
		return
	}
	data, err := json.Marshal(rao.index)
	if err != nil {
		return
	}
	if err := raWriteFileAtomic(filepath.Join(rao.dir, raoIndexName), data, 0o644); err != nil {
		logWarn("raoffline", "could not save the offline cache index: %v", err)
		return
	}
	rao.indexDirty = false
}

func raoSaveLoginsLocked() {
	if rao.dir == "" {
		return
	}
	data, err := json.Marshal(rao.logins)
	if err != nil {
		return
	}
	if err := os.MkdirAll(rao.dir, 0o700); err != nil {
		return
	}
	// Only the fact reaches the log, never an error text, for the file
	// that holds the token.
	if err := AtomicWrite(filepath.Join(rao.dir, raoLoginName), data, 0o600); err != nil {
		logWarn("raoffline", "could not save the offline sign-in")
	}
}

// raoSaveQueueLocked brings the unlock ages up to date and writes the queue.
func raoSaveQueueLocked() {
	now := time.Now()
	for _, q := range rao.queue.Items {
		q.AgeSec = q.age().Seconds()
		q.anchor = now
	}
	rao.agesSaved = now
	if rao.dir == "" {
		return
	}
	data, err := json.Marshal(rao.queue)
	if err != nil {
		return
	}
	if err := os.MkdirAll(rao.dir, 0o700); err != nil {
		return
	}
	if err := AtomicWrite(filepath.Join(rao.dir, raoQueueName), data, 0o600); err != nil {
		logWarn("raoffline", "could not save the offline unlock queue: %v", err)
	}
}

// raoPersistAges writes the ages of waiting unlocks when they are due (or
// now, at exit), and an index with unwritten use counts.
func raoPersistAges(force bool) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	if !rao.loaded {
		return
	}
	if len(rao.queue.Items) > 0 && (force || time.Since(rao.agesSaved) >= raoAgesEvery) {
		raoSaveQueueLocked()
	}
	if rao.indexDirty && force {
		raoSaveIndexLocked()
	}
}

// ── Cached answers ───────────────────────────────────────────────────────

// raoCachePut keeps one answer (or picture) under key. game is the game ID it
// belongs to, 0 for none.
func raoCachePut(key string, body []byte, game int) {
	if len(body) == 0 || len(body) > raoEntryMaxBytes {
		return
	}
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	if rao.dir == "" {
		return
	}
	dir := filepath.Join(rao.dir, raoCacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("raoffline", "could not create the offline cache: %v", err)
		return
	}
	name := raoFileFor(key)
	if err := raWriteFileAtomic(filepath.Join(dir, name), body, 0o644); err != nil {
		logWarn("raoffline", "could not cache an answer: %v", err)
		return
	}
	rao.index.Seq++
	rao.index.Entries[key] = &raoEntry{File: name, Size: int64(len(body)), Use: rao.index.Seq, Game: game}
	raoEvictLocked()
	raoSaveIndexSoonLocked()
	raoPublishLocked()
}

// raoSaveIndexSoonLocked writes the index raoIndexSaveDelay from now,
// together with whatever else changes meanwhile.
func raoSaveIndexSoonLocked() {
	rao.indexDirty = true
	if rao.saveTimer != nil {
		return
	}
	rao.saveTimer = time.AfterFunc(raoIndexSaveDelay, func() {
		rao.mu.Lock()
		defer rao.mu.Unlock()
		rao.saveTimer = nil
		if rao.indexDirty {
			raoSaveIndexLocked()
		}
	})
}

// raoSweepOrphans removes cache files the index does not know (an index
// write a power cut lost), so they cannot fill the card outside the bound.
// Start-up goroutine.
func raoSweepOrphans() {
	rao.mu.Lock()
	raoLoadLocked()
	dir := rao.dir
	known := make(map[string]bool, len(rao.index.Entries))
	for _, e := range rao.index.Entries {
		known[e.File] = true
	}
	rao.mu.Unlock()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(filepath.Join(dir, raoCacheDirName))
	if err != nil {
		return
	}
	removed := 0
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || known[name] {
			continue
		}
		// A file written after the snapshot above is in the index by now.
		rao.mu.Lock()
		inIndex := false
		for _, e := range rao.index.Entries {
			if e.File == name {
				inIndex = true
				break
			}
		}
		if !inIndex {
			if os.Remove(filepath.Join(dir, raoCacheDirName, name)) == nil {
				removed++
			}
		}
		rao.mu.Unlock()
	}
	if removed > 0 {
		logInfo("raoffline", "removed %d offline cache file(s) the index had lost", removed)
	}
}

// raoCacheGet is a cached answer, read from its file.
func raoCacheGet(key string) ([]byte, bool) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	e, ok := rao.index.Entries[key]
	if !ok || rao.dir == "" {
		return nil, false
	}
	f, err := os.Open(filepath.Join(rao.dir, raoCacheDirName, e.File))
	if err != nil {
		delete(rao.index.Entries, key)
		rao.indexDirty = true
		return nil, false
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, raoEntryMaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > raoEntryMaxBytes {
		return nil, false
	}
	rao.index.Seq++
	e.Use = rao.index.Seq
	rao.indexDirty = true
	return body, true
}

// raoCacheHas reports whether key is cached, without reading it.
func raoCacheHas(key string) bool {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	_, ok := rao.index.Entries[key]
	return ok
}

// raoEvictLocked drops the least recently used entries over the bounds.
func raoEvictLocked() {
	var total int64
	for _, e := range rao.index.Entries {
		total += e.Size
	}
	if total <= raoCacheMaxBytes && len(rao.index.Entries) <= raoCacheMaxEntries {
		return
	}
	keys := make([]string, 0, len(rao.index.Entries))
	for k := range rao.index.Entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return rao.index.Entries[keys[i]].Use < rao.index.Entries[keys[j]].Use })
	for _, k := range keys {
		if total <= raoCacheMaxBytes && len(rao.index.Entries) <= raoCacheMaxEntries {
			break
		}
		e := rao.index.Entries[k]
		_ = os.Remove(filepath.Join(rao.dir, raoCacheDirName, e.File))
		total -= e.Size
		delete(rao.index.Entries, k)
	}
}

// raoNoteGameHash records which game a hash resolved to and, right after a
// routed launch, which ROM it came from.
func raoNoteGameHash(hash string, game int) {
	if !raoValidHash(hash) {
		return
	}
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	changed := false
	if game > 0 && rao.index.Games[hash] != game {
		if len(rao.index.Games) >= raoPathsMax {
			rao.index.Games = map[string]int{}
		}
		rao.index.Games[hash] = game
		changed = true
	}
	if rao.launchPath != "" && time.Since(rao.launchAt) < raoLaunchLink {
		if len(rao.index.Paths) >= raoPathsMax {
			rao.index.Paths = map[string]string{}
		}
		rao.index.Paths[rao.launchPath] = hash
		rao.launchPath = ""
		changed = true
	}
	if changed {
		raoSaveIndexLocked()
	}
}

// raoNotePathHash records a ROM's hash computed here (Cache recently played).
func raoNotePathHash(path, hash string, game int) {
	if !raoValidHash(hash) || path == "" {
		return
	}
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	if len(rao.index.Paths) >= raoPathsMax {
		rao.index.Paths = map[string]string{}
	}
	rao.index.Paths[path] = hash
	if game > 0 {
		if len(rao.index.Games) >= raoPathsMax {
			rao.index.Games = map[string]int{}
		}
		rao.index.Games[hash] = game
	}
	raoSaveIndexLocked()
}

// raoNoteLaunch remembers the ROM a routed launch started.
func raoNoteLaunch(path string) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	rao.launchPath, rao.launchAt = path, time.Now()
}

// raoPathHash is the game hash known for a ROM, "" when none.
func raoPathHash(path string) string {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	return rao.index.Paths[path]
}

// raoHashGame is the game ID known for a hash, 0 when none.
func raoHashGame(hash string) int {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	return rao.index.Games[hash]
}

// raoGameReadyOffline reports whether the ROM at path can start its
// achievements with no internet: its hash is known and the game's data is
// cached.
func raoGameReadyOffline(path string) bool {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	hash := rao.index.Paths[path]
	if hash == "" {
		return false
	}
	if _, ok := rao.index.Entries["sets:"+hash]; ok {
		return true
	}
	game := rao.index.Games[hash]
	if game <= 0 {
		return false
	}
	_, ok := rao.index.Entries["patch:"+strconv.Itoa(game)]
	return ok
}

// raoGameCached reports whether a game's achievement data is cached.
func raoGameCached(game int) bool {
	if game <= 0 {
		return false
	}
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	for key, e := range rao.index.Entries {
		if e.Game == game && (strings.HasPrefix(key, "patch:") || strings.HasPrefix(key, "sets:")) {
			return true
		}
	}
	return false
}

// raoClearCache removes the cached answers and pictures and what the index
// learned about games. The sign-in and the waiting unlocks stay: the unlocks
// are the player's, and they need the sign-in to go.
func raoClearCache() int {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	n := raoGamesCachedLocked()
	if rao.dir != "" {
		_ = os.RemoveAll(filepath.Join(rao.dir, raoCacheDirName))
	}
	rao.index = raoIndex{Schema: raoSchema, Entries: map[string]*raoEntry{}, Games: map[string]int{},
		Paths: map[string]string{}, Unlocks: map[string][]raoUnlock{}}
	raoSaveIndexLocked()
	raoPublishLocked()
	return n
}

// ── Sign-in ──────────────────────────────────────────────────────────────

// raoUserKey is an account's key: RetroAchievements names ignore case.
func raoUserKey(user string) string { return strings.ToLower(strings.TrimSpace(user)) }

// raoPassHash is the salted hash a password sign-in is checked against
// offline; the password itself is never kept.
func raoPassHash(user, pass string) string {
	sum := sha256.Sum256([]byte("jukahub-raoffline\x00" + raoUserKey(user) + "\x00" + pass))
	return hex.EncodeToString(sum[:])
}

// raoLoginStore keeps an account's login answer after the server accepted it.
func raoLoginStore(user, token, pass string, body []byte, agent string) {
	key := raoUserKey(user)
	if key == "" || token == "" || len(body) == 0 || len(body) > raoEntryMaxBytes {
		return
	}
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	l := &raoLogin{User: strings.TrimSpace(user), Token: token, Body: append(json.RawMessage(nil), body...), Agent: agent}
	if prev := rao.logins.Logins[key]; prev != nil && pass == "" && prev.Token == token {
		l.PassHash = prev.PassHash
	}
	if pass != "" {
		l.PassHash = raoPassHash(user, pass)
	}
	rao.logins.Logins[key] = l
	rao.logins.Last = key
	rao.loginGen++
	raoSaveLoginsLocked()
}

// raoLoginFor is a copy of an account's offline sign-in.
func raoLoginFor(user string) (raoLogin, bool) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	l := rao.logins.Logins[raoUserKey(user)]
	if l == nil {
		return raoLogin{}, false
	}
	return *l, true
}

// raoLastLogin is the sign-in of the account that logged in last.
func raoLastLogin() (raoLogin, bool) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	l := rao.logins.Logins[rao.logins.Last]
	if l == nil {
		return raoLogin{}, false
	}
	return *l, true
}

// raoLoginMatches reports whether a login request's token or password is the
// one the server accepted for l.
func raoLoginMatches(l raoLogin, token, pass string) bool {
	if token != "" && l.Token != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(l.Token)) == 1
	}
	if pass != "" && l.PassHash != "" {
		return subtle.ConstantTimeCompare([]byte(raoPassHash(l.User, pass)), []byte(l.PassHash)) == 1
	}
	return false
}

// raoLoginGeneration counts the logins stored.
func raoLoginGeneration() uint64 {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	return rao.loginGen
}

// ── Sessions and unlocks ─────────────────────────────────────────────────

func raoUnlockKey(user string, game int) string {
	return raoUserKey(user) + "|" + strconv.Itoa(game)
}

// raoSetCurrentGame records the game an account's session is in.
func raoSetCurrentGame(user string, game int) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	rao.curGame[raoUserKey(user)] = game
}

// raoCurrentGame is the game an account's session is in, 0 when unknown.
func raoCurrentGame(user string) int {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	return rao.curGame[raoUserKey(user)]
}

// raoSessionCached forgets the unlocks learned for a game: a fresh session
// answer from the server lists them all.
func raoSessionCached(user string, game int) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	if _, ok := rao.index.Unlocks[raoUnlockKey(user, game)]; ok {
		delete(rao.index.Unlocks, raoUnlockKey(user, game))
		raoSaveIndexLocked()
	}
}

// raoNoteUnlockLocked remembers an unlock the server confirmed.
func raoNoteUnlockLocked(user string, game, id int, hard bool) {
	if game <= 0 || id <= 0 {
		return
	}
	key := raoUnlockKey(user, game)
	list := rao.index.Unlocks[key]
	for i, u := range list {
		if u.ID == id {
			list[i].Hard = u.Hard || hard
			return
		}
	}
	if len(list) >= raoUnlocksPerGame {
		return
	}
	rao.index.Unlocks[key] = append(list, raoUnlock{ID: id, Hard: hard})
}

// raoNoteUnlock is raoNoteUnlockLocked for the proxy's pass-through.
func raoNoteUnlock(user string, game, id int, hard bool) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	raoNoteUnlockLocked(user, game, id, hard)
	raoSaveIndexLocked()
}

// raoKnownUnlocks are the unlocks a cached session answer does not list yet:
// the ones confirmed since, and the ones waiting to be sent (casual).
func raoKnownUnlocks(user string, game int) []raoUnlock {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	out := append([]raoUnlock(nil), rao.index.Unlocks[raoUnlockKey(user, game)]...)
	key := raoUserKey(user)
	for _, q := range rao.queue.Items {
		if q.Game != game || raoUserKey(q.User) != key {
			continue
		}
		if id, _ := strconv.Atoi(q.Params["a"]); id > 0 {
			out = append(out, raoUnlock{ID: id})
		}
	}
	return out
}

// raoMergeSession adds unlocks to a session answer ("Unlocks" and
// "HardcoreUnlocks", lists of {"ID", "When"} as rcheevos reads them), so
// RetroArch does not unlock them a second time. An answer it cannot read is
// returned as it is. now is the "When" of the added ones and the new
// "ServerNow" (0 leaves the cached one).
func raoMergeSession(body []byte, unlocks []raoUnlock, now int64) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}
	when := now
	if when == 0 {
		// No clock yet: the cached answer's own "now" is the best moment.
		_ = json.Unmarshal(obj["ServerNow"], &when)
	}
	add := func(field string, hardOnly bool) {
		var list []json.RawMessage
		if raw, ok := obj[field]; ok && len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			if err := json.Unmarshal(raw, &list); err != nil {
				return
			}
		}
		have := map[int]bool{}
		for _, item := range list {
			var u struct {
				ID int `json:"ID"`
			}
			if json.Unmarshal(item, &u) == nil {
				have[u.ID] = true
			}
		}
		changed := false
		for _, u := range unlocks {
			if have[u.ID] || (hardOnly && !u.Hard) {
				continue
			}
			have[u.ID] = true
			item, _ := json.Marshal(map[string]int64{"ID": int64(u.ID), "When": when})
			list = append(list, item)
			changed = true
		}
		if changed {
			if raw, err := json.Marshal(list); err == nil {
				obj[field] = raw
			}
		}
	}
	add("Unlocks", false)
	add("HardcoreUnlocks", true)
	if now > 0 {
		obj["ServerNow"], _ = json.Marshal(now)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// raoMergeLegacyUnlocks adds unlock IDs to an older RetroArch's "unlocks"
// answer ("UserUnlocks", a list of IDs). hard is the answer's mode.
func raoMergeLegacyUnlocks(body []byte, unlocks []raoUnlock, hard bool) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}
	var ids []int
	if raw, ok := obj["UserUnlocks"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return body
		}
	}
	have := map[int]bool{}
	for _, id := range ids {
		have[id] = true
	}
	for _, u := range unlocks {
		if !have[u.ID] && (!hard || u.Hard) {
			have[u.ID] = true
			ids = append(ids, u.ID)
		}
	}
	obj["UserUnlocks"], _ = json.Marshal(ids)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// ── The queue ────────────────────────────────────────────────────────────

// raoQueueAdd keeps an unlock to send later. An unlock already waiting (the
// same account, achievement and mode: rcheevos retries) is not added twice.
// It reports whether the unlock is waiting now.
func raoQueueAdd(user string, game int, params map[string]string, agent string) bool {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	key := raoUserKey(user)
	for _, q := range rao.queue.Items {
		if raoUserKey(q.User) == key && q.Params["a"] == params["a"] && q.Params["h"] == params["h"] {
			return true
		}
	}
	if len(rao.queue.Items) >= raoQueueMax {
		logWarn("raoffline", "the offline unlock queue is full (%d); an unlock was not kept", raoQueueMax)
		return false
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return false
	}
	q := &raoQueued{
		ID:     hex.EncodeToString(id[:]),
		User:   strings.TrimSpace(user),
		Game:   game,
		Params: params,
		Agent:  agent,
		anchor: time.Now(),
	}
	rao.queue.Items = append(rao.queue.Items, q)
	raoSaveQueueLocked()
	raoPublishLocked()
	return true
}

// raoQueueSnapshot is a copy of the waiting unlocks with their ages now.
func raoQueueSnapshot() []raoQueued {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	out := make([]raoQueued, 0, len(rao.queue.Items))
	for _, q := range rao.queue.Items {
		c := *q
		c.Params = make(map[string]string, len(q.Params))
		for k, v := range q.Params {
			c.Params[k] = v
		}
		c.AgeSec = q.age().Seconds()
		c.anchor = time.Time{}
		out = append(out, c)
	}
	return out
}

// raoQueueOutcome is what became of one waiting unlock.
type raoQueueOutcome int

const (
	raoOutcomeSent     raoQueueOutcome = iota // the server has it
	raoOutcomeRejected                        // the server refused it for good
	raoOutcomeVerbatim                        // try again as RetroArch sent it
	raoOutcomeTried                           // a refusal that may pass later
)

// raoQueueResolve records what became of the waiting unlock id.
func raoQueueResolve(id string, outcome raoQueueOutcome) {
	rao.mu.Lock()
	defer rao.mu.Unlock()
	raoLoadLocked()
	for i, q := range rao.queue.Items {
		if q.ID != id {
			continue
		}
		switch outcome {
		case raoOutcomeSent, raoOutcomeRejected:
			rao.queue.Items = append(rao.queue.Items[:i], rao.queue.Items[i+1:]...)
			if outcome == raoOutcomeSent {
				rao.queue.Sent++
				a, _ := strconv.Atoi(q.Params["a"])
				raoNoteUnlockLocked(q.User, q.Game, a, q.Params["h"] == "1")
				raoSaveIndexLocked()
			} else {
				rao.queue.Rejected++
			}
		case raoOutcomeVerbatim:
			q.Verbatim = true
		case raoOutcomeTried:
			q.Tries++
		}
		raoSaveQueueLocked()
		raoPublishLocked()
		return
	}
}

// ── The award signature ──────────────────────────────────────────────────

// raoAwardSignature is rcheevos' "v" for an award request, as recalled from
// rc_api_runtime.c (UNVERIFIED against the source here): the MD5 of the
// achievement ID, the user name and the mode digit, and — when the unlock's
// age is sent — the achievement ID again and the age in seconds.
// raoAwardSignatureHolds checks the recipe against RetroArch's own request
// before it is ever used.
func raoAwardSignature(id int, user, mode string, secs int) string {
	s := strconv.Itoa(id) + user + mode
	if secs > 0 {
		s += strconv.Itoa(id) + strconv.Itoa(secs)
	}
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// raoAwardSignatureHolds reports whether raoAwardSignature gives the "v"
// RetroArch itself sent with params: only then may the age be added with a
// signature made here; otherwise the request goes as it was.
func raoAwardSignatureHolds(params map[string]string) bool {
	id, err := strconv.Atoi(params["a"])
	if err != nil || id <= 0 || params["v"] == "" {
		return false
	}
	secs := 0
	if o := params["o"]; o != "" {
		if secs, err = strconv.Atoi(o); err != nil || secs < 0 {
			return false
		}
	}
	mode := params["h"]
	if mode == "" {
		mode = "0"
	}
	return strings.EqualFold(raoAwardSignature(id, params["u"], mode, secs), params["v"])
}

// raoValidHash accepts a RetroAchievements game hash: 32 hex digits.
func raoValidHash(h string) bool {
	if len(h) != 32 {
		return false
	}
	for _, r := range h {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
