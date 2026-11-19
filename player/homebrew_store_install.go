package main

// homebrew_store_install.go — Get Homebrew's installs, updates and deletes
// (homebrew_store.go), and what they remember.
//
// One worker takes the queue in order, like the Ports Store's
// (ports_store_install.go), and shows each download on the shared status
// strip, whose X stops it:
//
//  1. The game's id comes from its data.json (cached in games.json).
//  2. A paid game needs a download key: the owned-keys list (asked for this
//     game alone) says whether the player bought it. Without one the install
//     stops and the page says "Buy on itch.io" — nothing is ever downloaded
//     around a payment. A free game needs only the key.
//  3. /games/ID/uploads lists the files; hbPickUpload picks the cartridge.
//  4. A download session is opened (itch.io asks for one per install, so its
//     counts are right), then /uploads/ID/download redirects to the file.
//     A ROM goes straight to a hidden ".part" file in the console's Roms
//     folder; a .zip goes to data/homebrew-store/incoming and only the ROM
//     inside it is extracted (by its extension; the archive's paths are never
//     used, so a "../" entry cannot write anywhere). Sizes are bounded and
//     free space is checked first.
//  5. The finished file is flushed and renamed to its clean name ("Tobu Tobu
//     Girl.gbc"), atomically. An update writes the same name, so saves (keyed
//     by the ROM's name) keep working.
//  6. The cover is saved as a PNG where the library looks first
//     (romArtworkPath under the game-art folder), unless a cover is already
//     there that this store did not put.
//  7. installed.json records the game page, game id, upload id, the upload's
//     updated_at, md5 and build id, the file (and its size, so a player's own
//     file that later takes the name is never updated or deleted as ours)
//     and the cover.
//
// Update checks compare those with the uploads list: when the store opens (at
// most every half hour) and, in the background, at most once a day while
// online — never before the first frame (deferStartupTask) and never on the
// main thread.
//
// The key travels only in the Authorization header and is never logged;
// errors are unwrapped from url.Error so no address (with a download key or a
// signed CDN link) reaches a toast or the log.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// hbRomMax bounds a cartridge (a big GBA game is 32 MB).
	hbRomMax = 64 << 20
	// hbZipMax bounds a downloaded archive (manuals and art ride along).
	hbZipMax = 256 << 20
	// hbAPIMax bounds one API answer.
	hbAPIMax = 4 << 20
	// hbCoverMax bounds a cover download; hbCoverW is the saved width.
	hbCoverMax = 8 << 20
	hbCoverW   = 512
	// hbOwnedPages bounds the owned-keys pages read (50 games a page).
	hbOwnedPages = 20
	// hbGameCacheMax bounds games.json.
	hbGameCacheMax = 3000
	// hbGameDataTTL is how long a game's data.json is trusted.
	hbGameDataTTL = 7 * 24 * time.Hour
	// hbUpdateEvery is the background check's spacing.
	hbUpdateEvery = 24 * time.Hour
	// hbWaitOtherDownload bounds the wait for another tool's download.
	hbWaitOtherDownload = 10 * time.Minute
	// hbKeepFinished is how many settled jobs stay listed for the cards.
	hbKeepFinished = 20
)

// ── HTTP ─────────────────────────────────────────────────────────────────

// hbClient is the store's HTTP client: https redirects only, five at most.
func hbClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: ytTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("the file is on an insecure address")
			}
			return nil
		}}
}

// hbNetError drops the address from a transport error.
func hbNetError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		if errors.Is(ue.Err, context.Canceled) {
			return context.Canceled
		}
		return ue.Err
	}
	return err
}

// errHbKeyRefused is a 401 or 403 from itch.io.
var errHbKeyRefused = errors.New("itch.io refused the API key — add it again")

// hbStatusError says what an HTTP status means here.
func hbStatusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errHbKeyRefused
	case http.StatusNotFound:
		return errors.New("itch.io no longer has it")
	case http.StatusTooManyRequests:
		return errors.New("itch.io asks to slow down — try again in a minute")
	}
	return fmt.Errorf("itch.io answered %d", status)
}

// hbGet reads rawURL with a size bound; key, when set, goes in the
// Authorization header. A status other than 200 is returned with the start of
// the body (the API puts its errors there), not as an error.
func hbGet(ctx context.Context, rawURL, key string, max int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", homebrewUserAgent)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hbClient(45 * time.Second).Do(req)
	if err != nil {
		return nil, 0, hbNetError(err)
	}
	defer resp.Body.Close()
	limit := max
	if resp.StatusCode != http.StatusOK {
		limit = 64 << 10
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, hbNetError(err)
	}
	if int64(len(data)) > limit {
		if resp.StatusCode == http.StatusOK {
			return nil, 0, errors.New("itch.io sent more than expected")
		}
		data = data[:limit]
	}
	return data, resp.StatusCode, nil
}

// hbAPI calls api.itch.io with the key; non-200 answers become errors (the
// API's own message when it sent one).
func hbAPI(ctx context.Context, key, method, path string, q url.Values) ([]byte, error) {
	u := "https://api.itch.io" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	if method == http.MethodGet {
		raw, status, err := hbGet(ctx, u, key, hbAPIMax)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, hbAPIStatusError(raw, status)
		}
		return raw, nil
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", homebrewUserAgent)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := hbClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, hbNetError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, hbNetError(err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, hbAPIStatusError(raw, resp.StatusCode)
	}
	return raw, nil
}

// hbAPIStatusError is the API's message for a failed call, else the status.
func hbAPIStatusError(raw []byte, status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return hbStatusError(status)
	}
	var doc map[string]interface{}
	if json.Unmarshal(raw, &doc) == nil {
		if msg := hbAPIErrors(doc); msg != "" {
			return errors.New(msg)
		}
	}
	return hbStatusError(status)
}

// hbFetchProfile is the name of the account the key belongs to.
func hbFetchProfile(ctx context.Context, key string) (string, error) {
	raw, err := hbAPI(ctx, key, http.MethodGet, "/profile", nil)
	if err != nil {
		return "", err
	}
	return hbParseProfile(raw)
}

// hbFetchOwned is every game the key's account owns.
func hbFetchOwned(ctx context.Context, key string) ([]hbOwnedGame, error) {
	var all []hbOwnedGame
	seen := map[int64]bool{}
	for page := 1; page <= hbOwnedPages; page++ {
		raw, err := hbAPI(ctx, key, http.MethodGet, "/profile/owned-keys", url.Values{"page": {strconv.Itoa(page)}})
		if err != nil {
			return all, err
		}
		got, more, err := hbParseOwnedKeys(raw)
		if err != nil {
			return all, err
		}
		added := 0
		for _, o := range got {
			if !seen[o.KeyID] {
				seen[o.KeyID] = true
				all = append(all, o)
				added++
			}
		}
		// An empty page ends the list; so does one that repeats what came
		// (a server that ignored "page").
		if !more || (added == 0 && len(got) > 0) {
			break
		}
	}
	return all, nil
}

// hbFetchOwnedKey is the player's download key for one game, 0 when they do
// not own it.
func hbFetchOwnedKey(ctx context.Context, key string, gameID int64) (int64, error) {
	raw, err := hbAPI(ctx, key, http.MethodGet, "/profile/owned-keys", url.Values{"game_id": {strconv.FormatInt(gameID, 10)}})
	if err != nil {
		return 0, err
	}
	got, _, err := hbParseOwnedKeys(raw)
	if err != nil {
		return 0, err
	}
	for _, o := range got {
		if o.GameID == gameID {
			return o.KeyID, nil
		}
	}
	// The game_id filter is not documented: when it was ignored, the game
	// may be on a later page of the whole list.
	all, err := hbFetchOwned(ctx, key)
	for _, o := range all {
		if o.GameID == gameID {
			return o.KeyID, nil
		}
	}
	return 0, err
}

// hbFetchUploads is a game's files as the key (and download key) sees them.
func hbFetchUploads(ctx context.Context, key string, gameID, keyID int64) ([]hbUpload, error) {
	q := url.Values{}
	if keyID > 0 {
		q.Set("download_key_id", strconv.FormatInt(keyID, 10))
	}
	raw, err := hbAPI(ctx, key, http.MethodGet, "/games/"+strconv.FormatInt(gameID, 10)+"/uploads", q)
	if err != nil {
		return nil, err
	}
	return hbParseUploads(raw)
}

// hbDownloadSession opens a download session; "" when itch.io would not
// (the download works without one).
func hbDownloadSession(ctx context.Context, key string, gameID, keyID int64) string {
	q := url.Values{}
	if keyID > 0 {
		q.Set("download_key_id", strconv.FormatInt(keyID, 10))
	}
	raw, err := hbAPI(ctx, key, http.MethodPost, "/games/"+strconv.FormatInt(gameID, 10)+"/download-sessions", q)
	if err != nil {
		logDebug("homebrew", "download session: %v", err)
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	uuid := hbJSONString(m, "uuid")
	if len(uuid) > 80 || !hbSlugOK(uuid) {
		return ""
	}
	return uuid
}

// ── data.json cache ──────────────────────────────────────────────────────

type hbGameCacheEntry struct {
	Data hbGameData `json:"data"`
	At   time.Time  `json:"at"`
}

var hbGames struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]hbGameCacheEntry
	// write keeps two savers from sharing games.json's temporary file.
	write sync.Mutex
}

func hbGameCachePath() string { return filepath.Join(hbDataDir(), "games.json") }

// hbGameCacheLoadLocked reads games.json once.
func hbGameCacheLoadLocked() {
	if hbGames.loaded {
		return
	}
	hbGames.loaded = true
	hbGames.m = map[string]hbGameCacheEntry{}
	if raw, err := os.ReadFile(hbGameCachePath()); err == nil {
		var m map[string]hbGameCacheEntry
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				if hbNormalizeLink(k) == k && v.Data.ID > 0 {
					hbGames.m[k] = v
				}
			}
		}
	}
}

// hbGameCacheSnapshot is every cached game's data, by game page.
func hbGameCacheSnapshot() map[string]hbGameData {
	hbGames.mu.Lock()
	defer hbGames.mu.Unlock()
	hbGameCacheLoadLocked()
	out := make(map[string]hbGameData, len(hbGames.m))
	for k, v := range hbGames.m {
		out[k] = v.Data
	}
	return out
}

// hbGameDataFor is a game's data.json: cached, else fetched and cached.
func hbGameDataFor(ctx context.Context, link string) (hbGameData, error) {
	hbGames.mu.Lock()
	hbGameCacheLoadLocked()
	e, ok := hbGames.m[link]
	hbGames.mu.Unlock()
	if ok && time.Since(e.At) < hbGameDataTTL {
		return e.Data, nil
	}
	raw, status, err := hbGet(ctx, link+"/data.json", "", 1<<20)
	if err == nil && status != http.StatusOK {
		err = hbStatusError(status)
	}
	if err != nil {
		if ok {
			return e.Data, nil
		}
		return hbGameData{}, err
	}
	d, err := hbParseGameData(raw)
	if err != nil {
		return hbGameData{}, err
	}
	hbGames.mu.Lock()
	hbGames.m[link] = hbGameCacheEntry{Data: d, At: time.Now()}
	if len(hbGames.m) > hbGameCacheMax {
		type kv struct {
			k  string
			at time.Time
		}
		list := make([]kv, 0, len(hbGames.m))
		for k, v := range hbGames.m {
			list = append(list, kv{k, v.At})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
		for _, x := range list[:len(list)-hbGameCacheMax] {
			delete(hbGames.m, x.k)
		}
	}
	out, merr := json.Marshal(hbGames.m)
	hbGames.mu.Unlock()
	if merr == nil {
		hbGames.write.Lock()
		_ = pstoreWriteFile(hbGameCachePath(), out)
		hbGames.write.Unlock()
	}
	return d, nil
}

// ── What is installed ────────────────────────────────────────────────────

// hbInstallRec is one installed game.
type hbInstallRec struct {
	Link     string    `json:"link"`
	Title    string    `json:"title"`
	Console  string    `json:"console"` // hbConsoles id
	Path     string    `json:"path"`
	Cover    string    `json:"cover,omitempty"`
	GameID   int64     `json:"gameId"`
	UploadID int64     `json:"uploadId"`
	KeyID    int64     `json:"downloadKeyId,omitempty"`
	BuildID  int64     `json:"buildId,omitempty"`
	Size     int64     `json:"size,omitempty"`
	FileSize int64     `json:"fileSize,omitempty"` // the cartridge as written, see hbRecFileOK
	MD5      string    `json:"md5,omitempty"`
	Updated  string    `json:"uploadUpdatedAt,omitempty"`
	At       time.Time `json:"installedAt"`
	Update   bool      `json:"update,omitempty"`
}

// hbRecFileOK reports whether r's file is on the card and still the one the
// store wrote (the same size, when the record knows it). A game deleted in
// the library and replaced by the player's own file of that name is not
// ours any more: Delete, Update and the Installed pill leave it alone.
func hbRecFileOK(r hbInstallRec) bool {
	info, err := os.Stat(r.Path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return r.FileSize <= 0 || info.Size() == r.FileSize
}

type hbInstallsFile struct {
	Version   int            `json:"version"`
	CheckedAt time.Time      `json:"checkedAt"`
	Games     []hbInstallRec `json:"games"`
}

var hbInst struct {
	mu        sync.Mutex
	loaded    bool
	recs      map[string]hbInstallRec
	checkedAt time.Time
	// seq numbers each copy encoded for the card (under mu). write is held
	// across the disk write, never together with mu, so the main thread's
	// reads never wait on the card; written keeps an older copy from landing
	// after a newer one.
	seq     uint64
	write   sync.Mutex
	written uint64
}

func hbInstallsPath() string { return filepath.Join(hbDataDir(), "installed.json") }

// hbParseInstalls reads installed.json, keeping well-formed records. Pure.
func hbParseInstalls(raw []byte) (map[string]hbInstallRec, time.Time, error) {
	var f hbInstallsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, time.Time{}, err
	}
	out := make(map[string]hbInstallRec, len(f.Games))
	for _, r := range f.Games {
		if r.Link = hbNormalizeLink(r.Link); r.Link == "" || r.Path == "" || !filepath.IsAbs(r.Path) || hbConsoleIndex(r.Console) < 0 {
			continue
		}
		out[r.Link] = r
	}
	return out, f.CheckedAt, nil
}

// hbInstallsLoadLocked reads installed.json once (its .bak when the file is
// damaged).
func hbInstallsLoadLocked() {
	if hbInst.loaded {
		return
	}
	hbInst.loaded = true
	hbInst.recs = map[string]hbInstallRec{}
	var f hbInstallsFile
	if _, err := readJSONWithBackup(hbInstallsPath(), &f); err == nil {
		raw, _ := json.Marshal(f)
		if recs, at, perr := hbParseInstalls(raw); perr == nil {
			hbInst.recs, hbInst.checkedAt = recs, at
		}
	}
}

// hbInstallsEncodeLocked is installed.json's bytes and their place in the
// order of changes. Under mu; hbInstallsWrite puts them on the card after mu
// is let go.
func hbInstallsEncodeLocked() ([]byte, uint64, error) {
	f := hbInstallsFile{Version: 1, CheckedAt: hbInst.checkedAt}
	for _, r := range hbInst.recs {
		f.Games = append(f.Games, r)
	}
	sort.Slice(f.Games, func(i, j int) bool { return f.Games[i].Link < f.Games[j].Link })
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, 0, err
	}
	hbInst.seq++
	return append(raw, '\n'), hbInst.seq, nil
}

// hbInstallsWrite writes installed.json from a copy hbInstallsEncodeLocked
// made, unless a newer copy is already written. Never under mu.
func hbInstallsWrite(raw []byte, seq uint64) {
	hbInst.write.Lock()
	defer hbInst.write.Unlock()
	if seq <= hbInst.written {
		return
	}
	hbInst.written = seq
	if err := AtomicWrite(hbInstallsPath(), raw, 0o644); err != nil {
		logWarn("homebrew", "saving installed.json: %v", err)
	}
}

// hbInstallsSnapshot is the records whose file is still on the card.
func hbInstallsSnapshot() map[string]hbInstallRec {
	hbInst.mu.Lock()
	hbInstallsLoadLocked()
	recs := make([]hbInstallRec, 0, len(hbInst.recs))
	for _, r := range hbInst.recs {
		recs = append(recs, r)
	}
	hbInst.mu.Unlock()
	out := make(map[string]hbInstallRec, len(recs))
	for _, r := range recs {
		if hbRecFileOK(r) {
			out[r.Link] = r
		}
	}
	return out
}

// hbInstallsGet is one record.
func hbInstallsGet(link string) (hbInstallRec, bool) {
	hbInst.mu.Lock()
	defer hbInst.mu.Unlock()
	hbInstallsLoadLocked()
	r, ok := hbInst.recs[link]
	return r, ok
}

// hbInstallsTakenPaths is every file another game's record names, in
// rommFreeName's lower-case form. A game deleted outside the store keeps its
// record (a reinstall reuses its name), so a new game with the same title
// must not take that name: the old record would call the new file its own
// and its update or Delete would replace or remove the other game.
func hbInstallsTakenPaths(except string) map[string]bool {
	hbInst.mu.Lock()
	defer hbInst.mu.Unlock()
	hbInstallsLoadLocked()
	out := make(map[string]bool, len(hbInst.recs))
	for link, r := range hbInst.recs {
		if link != except {
			out[strings.ToLower(r.Path)] = true
		}
	}
	return out
}

// hbInstallsPut stores (or with del, removes) one record and saves.
func hbInstallsPut(r hbInstallRec, del bool) {
	hbInst.mu.Lock()
	hbInstallsLoadLocked()
	if del {
		delete(hbInst.recs, r.Link)
	} else {
		hbInst.recs[r.Link] = r
	}
	raw, seq, err := hbInstallsEncodeLocked()
	hbInst.mu.Unlock()
	if err != nil {
		logWarn("homebrew", "saving installed.json: %v", err)
		return
	}
	hbInstallsWrite(raw, seq)
}

// ── The queue ────────────────────────────────────────────────────────────

type hbJobState int

const (
	hbJobQueued hbJobState = iota
	hbJobWorking
	hbJobDone
	hbJobFailed
)

// hbJob is one install or update. The item's fields are copied on the main
// thread when it is queued.
type hbJob struct {
	link     string
	title    string
	consoles uint16
	gameID   int64
	keyID    int64
	owned    bool
	image    string
	price    string
	update   bool
	key      string
	state    hbJobState
	note     string
	gen      uint64 // the status strip's run, worker only
}

var hbQueue struct {
	mu      sync.Mutex
	jobs    []*hbJob
	running bool
}

// hbErrCancelled is a job the player stopped from the status strip.
var hbErrCancelled = errors.New("cancelled")

// hbJobLookup is the newest job for a game page.
func hbJobLookup(link string) (hbJobState, string, bool) {
	hbQueue.mu.Lock()
	defer hbQueue.mu.Unlock()
	for i := len(hbQueue.jobs) - 1; i >= 0; i-- {
		if j := hbQueue.jobs[i]; j.link == link {
			return j.state, j.note, true
		}
	}
	return 0, "", false
}

// hbJobActive reports whether a state still has work ahead.
func hbJobActive(s hbJobState) bool { return s == hbJobQueued || s == hbJobWorking }

// hbQueueCounts is (waiting or running jobs, the running job's title).
func hbQueueCounts() (int, string) {
	hbQueue.mu.Lock()
	defer hbQueue.mu.Unlock()
	n, now := 0, ""
	for _, j := range hbQueue.jobs {
		if hbJobActive(j.state) {
			n++
			if j.state == hbJobWorking {
				now = j.title
			}
		}
	}
	return n, now
}

// hbEnqueue queues an install (or an update) of it. Main thread.
func hbEnqueue(it *hbItem, update bool) error {
	key := hbKeyGet()
	if key == "" {
		return errors.New("add your itch.io API key first")
	}
	if !IsOnline() {
		return errors.New("connect to Wi-Fi first")
	}
	if st, _, ok := hbJobLookup(it.key); ok && hbJobActive(st) {
		return errors.New(it.title + " is already in the queue")
	}
	j := &hbJob{link: it.key, title: it.title, consoles: it.consoles, gameID: it.gameID, keyID: it.keyID,
		owned: it.owned, image: it.image, price: it.price, update: update, key: key}
	hbQueue.mu.Lock()
	kept := hbQueue.jobs[:0:0]
	finished := 0
	for i := len(hbQueue.jobs) - 1; i >= 0; i-- {
		old := hbQueue.jobs[i]
		if !hbJobActive(old.state) && (old.link == j.link || finished >= hbKeepFinished) {
			continue
		}
		if !hbJobActive(old.state) {
			finished++
		}
		kept = append(kept, old)
	}
	for a, b := 0, len(kept)-1; a < b; a, b = a+1, b-1 {
		kept[a], kept[b] = kept[b], kept[a]
	}
	hbQueue.jobs = append(kept, j)
	start := !hbQueue.running
	hbQueue.running = true
	hbQueue.mu.Unlock()
	if start {
		go hbWorker()
	}
	return nil
}

// hbCancel drops a job that has not started. Main thread.
func hbCancel(link string) bool {
	hbQueue.mu.Lock()
	defer hbQueue.mu.Unlock()
	for i, j := range hbQueue.jobs {
		if j.link == link && j.state == hbJobQueued {
			hbQueue.jobs = append(hbQueue.jobs[:i], hbQueue.jobs[i+1:]...)
			return true
		}
	}
	return false
}

// hbSetState records a job's state under the lock.
func hbSetState(j *hbJob, s hbJobState, note string) {
	hbQueue.mu.Lock()
	j.state, j.note = s, note
	hbQueue.mu.Unlock()
}

// hbNextJob takes the next queued job, or marks the worker idle.
func hbNextJob() *hbJob {
	hbQueue.mu.Lock()
	defer hbQueue.mu.Unlock()
	for _, j := range hbQueue.jobs {
		if j.state == hbJobQueued {
			j.state = hbJobWorking
			return j
		}
	}
	hbQueue.running = false
	return nil
}

// hbWorker runs the queue until it is empty.
func hbWorker() {
	for {
		j := hbNextJob()
		if j == nil {
			return
		}
		rec, err := hbRunGuarded(j)
		if err != nil {
			note := firstLine(err.Error())
			if errors.Is(err, hbErrCancelled) {
				note = "Stopped"
			}
			hbSetState(j, hbJobFailed, note)
		} else {
			hbSetState(j, hbJobDone, "")
		}
		title, update := j.title, j.update
		tbxPost(func() {
			hbAfterChange()
			switch {
			case err == nil && update:
				PlaySuccessSound()
				ShowToast(title+" is up to date — same file name, so its saves still match", ToastKindSuccess)
			case err == nil:
				PlaySuccessSound()
				con := rec.Console
				if c := hbConsoleIndex(rec.Console); c >= 0 {
					con = hbConsoles[c].name
				}
				ShowToast(title+" is on your "+con+" shelf", ToastKindSuccess)
			case !errors.Is(err, hbErrCancelled):
				ShowToast("Could not install "+title+": "+firstLine(err.Error()), ToastKindError)
			}
		})
	}
}

// hbRunGuarded runs one job; a panic fails the job, not JukaHub.
func hbRunGuarded(j *hbJob) (rec hbInstallRec, err error) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("homebrew", "installing %s failed: %v", j.title, r)
			err = fmt.Errorf("internal error: %v", r)
			if j.gen != 0 {
				installBarFailRun(j.gen, "internal error")
			}
		}
	}()
	return hbRunJob(j)
}

// hbWaitForBar lets another tool's download finish first: the status strip
// shows one run at a time.
func hbWaitForBar() bool {
	deadline := time.Now().Add(hbWaitOtherDownload)
	for installBarActive() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
	return true
}

// hbRunJob does one install or update.
func hbRunJob(j *hbJob) (hbInstallRec, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	prev, hadPrev := hbInstallsGet(j.link)
	gameID, keyID, cover := j.gameID, j.keyID, j.image
	price := j.price
	priceKnown := price != ""
	dataRead := false
	if d, err := hbGameDataFor(ctx, j.link); err == nil {
		dataRead = true
		if gameID == 0 {
			gameID = d.ID
		}
		if d.Cover != "" {
			cover = d.Cover
		}
		if d.Price != "" {
			price, priceKnown = d.Price, true
		}
	} else if gameID == 0 {
		return hbInstallRec{}, fmt.Errorf("its itch.io page could not be read: %w", err)
	}
	if keyID == 0 && hadPrev {
		keyID = prev.KeyID
	}
	// A paid game is downloaded only with the player's own download key; a
	// game whose price is not known looks for one first.
	if keyID == 0 && (!priceKnown || !hbPriceFree(price) || j.owned) {
		k, err := hbFetchOwnedKey(ctx, j.key, gameID)
		if err != nil {
			return hbInstallRec{}, err
		}
		keyID = k
	}
	// Not owned and still no price: data.json names a price only when there
	// is one, and a game installed here before without a download key was
	// free then. Both count as free — itch.io itself refuses a paid file to
	// anyone without the buyer's download key, so this never works around a
	// payment. An update of a game no list shows any more (Update all) takes
	// this path.
	if keyID == 0 && !priceKnown && (dataRead || (hadPrev && prev.KeyID == 0)) {
		price, priceKnown = "", true
	}
	if keyID == 0 && !priceKnown {
		return hbInstallRec{}, errors.New("its itch.io page could not be read — try again in a moment")
	}
	if keyID == 0 && !hbPriceFree(price) {
		return hbInstallRec{}, errors.New("it is a paid game — buy it on itch.io first, then install it here")
	}
	ups, err := hbFetchUploads(ctx, j.key, gameID, keyID)
	if err != nil {
		return hbInstallRec{}, err
	}
	pick, ok := hbPickUpload(ups, j.consoles)
	if hadPrev && prev.UploadID > 0 {
		// An update keeps to the file it installed while itch.io lists it.
		for _, u := range ups {
			if u.ID != prev.UploadID {
				continue
			}
			c := hbConsoleForExt(hbRomExt(u.Filename), j.consoles|1<<uint(hbConsoleIndex(prev.Console)))
			if c >= 0 || hbRomExt(u.Filename) == ".zip" {
				pick, ok = hbPick{Upload: u, Console: c, Zip: c < 0}, true
			}
			break
		}
	}
	if !ok {
		return hbInstallRec{}, errors.New("its page has no cartridge for these consoles — it may be a PC or web game")
	}
	if pick.Upload.Size > hbZipMax {
		return hbInstallRec{}, errors.New("the file is larger than a cartridge download can be")
	}

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
	installBarUpdateRun(gen, installPhaseDownload, 0, pick.Upload.Size)
	uuid := hbDownloadSession(rctx, j.key, gameID, keyID)

	// Where the cartridge goes: known from its name, or once the zip is open.
	console := pick.Console
	var dir string
	if console >= 0 {
		if dir, err = hbConsoleDir(console); err != nil {
			return fail(err)
		}
		if err := pkgCheckFreeSpace(dir, pick.Upload.Size); err != nil {
			return fail(err)
		}
	}
	var part, ext string
	if pick.Zip {
		incoming := filepath.Join(hbDataDir(), "incoming")
		_ = os.MkdirAll(incoming, 0o755)
		if err := pkgCheckFreeSpace(incoming, pick.Upload.Size); err != nil {
			return fail(err)
		}
		zipPath := filepath.Join(incoming, strconv.FormatInt(gameID, 10)+".zip.part")
		defer os.Remove(zipPath)
		if err := hbDownloadUpload(rctx, gen, j.key, pick.Upload.ID, keyID, uuid, zipPath, hbZipMax); err != nil {
			return fail(err)
		}
		installBarUpdateRun(gen, installPhaseExtract, 0, 0)
		want := j.consoles
		if hadPrev {
			if c := hbConsoleIndex(prev.Console); c >= 0 {
				want |= 1 << uint(c)
			}
		}
		idx, c, entryExt, err := hbZipRom(zipPath, want)
		if err != nil {
			return fail(err)
		}
		console, ext = c, entryExt
		if dir, err = hbConsoleDir(console); err != nil {
			return fail(err)
		}
		part = filepath.Join(dir, "."+hbHash(j.link)+".part")
		if err := hbZipExtract(zipPath, idx, part); err != nil {
			_ = os.Remove(part)
			return fail(err)
		}
	} else {
		ext = hbRomExt(pick.Upload.Filename)
		part = filepath.Join(dir, "."+hbHash(j.link)+".part")
		if err := hbDownloadUpload(rctx, gen, j.key, pick.Upload.ID, keyID, uuid, part, hbRomMax); err != nil {
			_ = os.Remove(part)
			return fail(err)
		}
	}
	defer os.Remove(part)
	if err := mcSyncFile(part); err != nil {
		return fail(err)
	}
	installBarUpdateRun(gen, installPhaseExtract, 0, 0)

	// The name: an update keeps the file it replaces (saves follow the name)
	// while that file is still the one this store wrote, or is gone. A file
	// of the player's own under that name is never replaced.
	prevOurs := hadPrev && hbRecFileOK(prev)
	prevGone := false
	if hadPrev {
		_, lerr := os.Lstat(prev.Path)
		prevGone = os.IsNotExist(lerr)
	}
	dest := ""
	if hadPrev && (prevOurs || prevGone) && strings.EqualFold(filepath.Dir(prev.Path), dir) && strings.EqualFold(hbRomExt(prev.Path), ext) {
		dest = prev.Path
	} else {
		stem := strings.TrimSuffix(filepath.Base(pick.Upload.Filename), filepath.Ext(pick.Upload.Filename))
		dest = rommFreeName(filepath.Join(dir, hbCleanFileName(j.title, stem)+ext), hbInstallsTakenPaths(j.link))
	}
	if !isPathWithin(dest, dir) {
		return fail(errors.New("the file name is not safe"))
	}
	if err := os.Rename(part, dest); err != nil {
		return fail(err)
	}
	if prevOurs && prev.Path != dest {
		// Another console or format: the old file goes (its saves keep
		// their old name and stay where they are).
		_ = os.Remove(prev.Path)
	}
	var fileSize int64
	if info, serr := os.Stat(dest); serr == nil {
		fileSize = info.Size()
	}

	// The cover, where the library looks first, unless one is already there
	// that this store did not put.
	con := hbConsoles[console]
	coverPath := ""
	if root := libraryArtworkRoot(); root != "" && cover != "" {
		p := romArtworkPath(scannedROM{Path: dest, System: con.name}, root)
		if p != "" && (!artworkCoverPresent(p) || (hadPrev && prev.Cover == p)) {
			if err := hbSaveCover(rctx, cover, p); err != nil {
				logDebug("homebrew", "cover for %s: %v", j.title, err)
				if hadPrev && prev.Cover == p && artworkCoverPresent(p) {
					// The cover saved last time is still ours to delete.
					coverPath = p
				}
			} else {
				coverPath = p
			}
		}
	}
	rec := hbInstallRec{
		Link: j.link, Title: j.title, Console: con.id, Path: dest, Cover: coverPath,
		GameID: gameID, UploadID: pick.Upload.ID, KeyID: keyID, BuildID: pick.Upload.BuildID,
		Size: pick.Upload.Size, FileSize: fileSize, MD5: pick.Upload.MD5, Updated: pick.Upload.Updated, At: time.Now(),
	}
	hbInstallsPut(rec, false)
	installBarDoneRun(gen)
	logInfo("homebrew", "%s installed as %s", j.title, filepath.Base(dest))
	return rec, nil
}

// hbDownloadUpload fetches an upload into dest (created or truncated),
// reporting on the status strip, refusing more than max bytes.
func hbDownloadUpload(ctx context.Context, gen uint64, key string, uploadID, keyID int64, uuid, dest string, max int64) error {
	q := url.Values{}
	if keyID > 0 {
		q.Set("download_key_id", strconv.FormatInt(keyID, 10))
	}
	if uuid != "" {
		q.Set("uuid", uuid)
	}
	u := "https://api.itch.io/uploads/" + strconv.FormatInt(uploadID, 10) + "/download"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := hbOpenURL(ctx, u, key)
	if err != nil {
		return err
	}
	// Older answers name the file instead of redirecting to it.
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if rerr != nil {
			return hbNetError(rerr)
		}
		next := hbParseDownloadURL(raw)
		if next == "" {
			return errors.New("itch.io did not say where the file is")
		}
		if resp, err = hbOpenURL(ctx, next, ""); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.ContentLength > max {
		return errors.New("the file is larger than a cartridge can be")
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	buf := make([]byte, 64<<10)
	var done int64
	last := time.Time{}
	for {
		if ctx.Err() != nil {
			out.Close()
			return hbErrCancelled
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > max {
				out.Close()
				return errors.New("the file is larger than a cartridge can be")
			}
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			if time.Since(last) > 150*time.Millisecond {
				installBarUpdateRun(gen, installPhaseDownload, done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			if ctx.Err() != nil {
				return hbErrCancelled
			}
			return hbNetError(rerr)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if total > 0 && done != total {
		return errors.New("the download stopped early — try again")
	}
	if done == 0 {
		return errors.New("itch.io sent an empty file")
	}
	installBarUpdateRun(gen, installPhaseDownload, done, total)
	return nil
}

// hbOpenURL starts a GET and returns the response when it is a 200.
func hbOpenURL(ctx context.Context, u, key string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", homebrewUserAgent)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hbClient(20 * time.Minute).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, hbErrCancelled
		}
		return nil, hbNetError(err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, hbAPIStatusError(raw, resp.StatusCode)
	}
	return resp, nil
}

// hbZipRom finds the cartridge in a downloaded zip: the entry's index, its
// console and its extension. Only names are read here.
func hbZipRom(zipPath string, want uint16) (int, int, string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return -1, -1, "", errors.New("the download is not a readable zip")
	}
	defer zr.Close()
	names := make([]string, len(zr.File))
	sizes := make([]int64, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
		sizes[i] = int64(f.UncompressedSize64)
	}
	idx, console := hbZipRomPick(names, sizes, want)
	if idx < 0 {
		return -1, -1, "", errors.New("the download has no cartridge for these consoles")
	}
	if zr.File[idx].UncompressedSize64 > hbRomMax {
		return -1, -1, "", errors.New("the cartridge in the zip is larger than one can be")
	}
	return idx, console, hbRomExt(zr.File[idx].Name), nil
}

// hbZipExtract copies entry idx of a zip to dest. Only the entry's bytes are
// used, never its path, so no entry name can write outside dest.
func hbZipExtract(zipPath string, idx int, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return errors.New("the download is not a readable zip")
	}
	defer zr.Close()
	if idx < 0 || idx >= len(zr.File) {
		return errors.New("the download changed while it was read")
	}
	in, err := zr.File[idx].Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, hbRomMax+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return err
	case n > hbRomMax:
		return errors.New("the cartridge in the zip is larger than one can be")
	case n == 0:
		return errors.New("the cartridge in the zip is empty")
	}
	return nil
}

// hbConsoleDir is console c's Roms folder as an absolute path. A PC finds its
// "Roms" relative to the working folder (resolvePath), and installed.json
// keeps only absolute paths (hbParseInstalls): a relative one would forget
// the game at the next start.
func hbConsoleDir(c int) (string, error) {
	dir, err := hbConsoleDirFind(c)
	if err != nil {
		return "", err
	}
	return filepath.Abs(dir)
}

// hbConsoleDirFind is console c's Roms folder: one the card already has for
// it (the firmware's own name first, NextUI's tagged folders too), else the
// firmware's name in the first Roms folder there is.
func hbConsoleDirFind(c int) (string, error) {
	if c < 0 || c >= len(hbConsoles) {
		return "", errors.New("unknown console")
	}
	con := hbConsoles[c]
	if con.romm == "" {
		if d := pico8NativeRomsDir(true); d != "" {
			return d, nil
		}
		return "", errors.New("there is no Roms folder on the card")
	}
	sys, ok := rommSystemByID(con.romm)
	if !ok {
		return "", errors.New("unknown console")
	}
	roots := romLibraryRoots()
	for _, root := range roots {
		dirs := rommListDirs(root)
		for _, name := range dirs {
			s, _, found := romFolderSystem(name, nil)
			if !found || s.Name != sys.Name {
				continue
			}
			if _, alias := romSystemAliases[strings.ToLower(name)]; alias {
				return rommTargetFolder(root, sys, dirs), nil
			}
			return filepath.Join(root, name), nil
		}
	}
	for _, root := range roots {
		if filepath.IsAbs(root) && dirExists(root) {
			dir := filepath.Join(root, sys.Folder)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			return dir, nil
		}
	}
	for _, root := range roots {
		if filepath.IsAbs(root) {
			dir := filepath.Join(root, sys.Folder)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			return dir, nil
		}
	}
	return "", errors.New("there is no Roms folder on the card")
}

// hbSaveCover downloads a cover and saves it as a PNG at dest.
func hbSaveCover(ctx context.Context, coverURL, dest string) error {
	raw, status, err := hbGet(ctx, coverURL, "", hbCoverMax)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return hbStatusError(status)
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rgba, err := decodeImageRGBA(dctx, raw)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pstoreDownscale(rgba, hbCoverW)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return pstoreWriteFile(dest, buf.Bytes())
}

// hbDelete removes an installed game's file and the cover this store put,
// never its saves, on a goroutine. Main thread.
func hbDelete(link, title string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("homebrew", "deleting %s failed: %v", title, r)
			}
		}()
		rec, ok := hbInstallsGet(link)
		var err error
		if ok {
			// Only the file this store wrote: a player's own file that took
			// its name since stays (the record just goes).
			if hbRecFileOK(rec) {
				if rerr := os.Remove(rec.Path); rerr != nil && !os.IsNotExist(rerr) {
					err = rerr
				}
			}
			if err == nil && rec.Cover != "" {
				if root := libraryArtworkRoot(); root != "" && isPathWithin(rec.Cover, root) {
					_ = os.Remove(rec.Cover)
				}
			}
			if err == nil {
				hbInstallsPut(rec, true)
			}
		}
		tbxPost(func() {
			hbAfterChange()
			if err != nil {
				ShowToast("Could not delete "+title+": "+firstLine(err.Error()), ToastKindError)
				return
			}
			ShowToast(title+" is deleted — its saves stay", ToastKindInfo)
		})
	}()
}

// ── Update checks ────────────────────────────────────────────────────────

// hbUpdateCheck is one check's lock; at most one runs.
var hbUpdateCheck sync.Mutex

// hbCheckUpdatesNow compares every install with itch.io's uploads and marks
// the ones with a newer file. It returns how many have one. Goroutine.
func hbCheckUpdatesNow(ctx context.Context, key string) (int, error) {
	if !hbUpdateCheck.TryLock() {
		return 0, errors.New("a check is already running")
	}
	defer hbUpdateCheck.Unlock()
	hbInst.mu.Lock()
	hbInstallsLoadLocked()
	recs := make([]hbInstallRec, 0, len(hbInst.recs))
	for _, r := range hbInst.recs {
		recs = append(recs, r)
	}
	hbInst.mu.Unlock()
	found := map[string]bool{}
	var firstErr error
	for _, r := range recs {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if r.GameID <= 0 || !hbRecFileOK(r) {
			continue
		}
		ups, err := hbFetchUploads(ctx, key, r.GameID, r.KeyID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		found[r.Link] = hbUploadChanged(r, ups, uint16(1)<<uint(hbConsoleIndex(r.Console)))
	}
	n := 0
	seen := make(map[string]hbInstallRec, len(recs))
	for _, r := range recs {
		seen[r.Link] = r
	}
	hbInst.mu.Lock()
	for link, upd := range found {
		// A record an install replaced during the check keeps its own state.
		if r, ok := hbInst.recs[link]; ok && r.UploadID == seen[link].UploadID && r.At.Equal(seen[link].At) {
			r.Update = upd
			hbInst.recs[link] = r
		}
	}
	for _, r := range hbInst.recs {
		if r.Update {
			n++
		}
	}
	if firstErr == nil || len(found) > 0 {
		hbInst.checkedAt = time.Now()
	}
	raw, seq, encErr := hbInstallsEncodeLocked()
	hbInst.mu.Unlock()
	if encErr != nil {
		logWarn("homebrew", "saving installed.json: %v", encErr)
	} else {
		hbInstallsWrite(raw, seq)
	}
	if len(found) == 0 && firstErr != nil {
		return n, firstErr
	}
	return n, nil
}

// hbUploadChanged reports whether itch.io has a newer file than r: the same
// upload with another date, md5 or build, or (that upload gone) another
// cartridge for the console. Pure.
func hbUploadChanged(r hbInstallRec, ups []hbUpload, want uint16) bool {
	for _, u := range ups {
		if u.ID != r.UploadID {
			continue
		}
		switch {
		case u.MD5 != "" && r.MD5 != "" && !strings.EqualFold(u.MD5, r.MD5):
			return true
		case u.BuildID > 0 && r.BuildID > 0 && u.BuildID != r.BuildID:
			return true
		case u.Updated != "" && r.Updated != "" && u.Updated != r.Updated:
			return true
		}
		return false
	}
	_, ok := hbPickUpload(ups, want)
	return ok
}

// hbCheckUpdatesSoon starts a check from the main thread when the last one
// is older than minGap.
func hbCheckUpdatesSoon(minGap time.Duration) {
	key := hbKeyGet()
	if hb.checking || key == "" || !IsOnline() {
		return
	}
	hbInst.mu.Lock()
	hbInstallsLoadLocked()
	due := len(hbInst.recs) > 0 && time.Since(hbInst.checkedAt) >= minGap
	hbInst.mu.Unlock()
	if !due {
		return
	}
	hb.checking = true
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("homebrew", "update check failed: %v", r)
			}
			tbxPost(func() { hb.checking = false })
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := hbCheckUpdatesNow(ctx, key); err != nil {
			logDebug("homebrew", "update check: %v", err)
		}
		tbxPost(hbInstallsRefresh)
	}()
}

// hbUpdateLoop checks for updates at most once a day while online, in the
// background. Started after the first frame.
func hbUpdateLoop() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("homebrew", "background update check stopped: %v", r)
		}
	}()
	time.Sleep(3 * time.Minute)
	for {
		key := hbKeyGet()
		hbInst.mu.Lock()
		hbInstallsLoadLocked()
		due := len(hbInst.recs) > 0 && time.Since(hbInst.checkedAt) >= hbUpdateEvery
		hbInst.mu.Unlock()
		if due && key != "" && IsOnline() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			n, err := hbCheckUpdatesNow(ctx, key)
			cancel()
			if err != nil {
				logDebug("homebrew", "background update check: %v", err)
			} else if n > 0 {
				logInfo("homebrew", "%d homebrew update(s) on itch.io", n)
			}
			tbxPost(hbInstallsRefresh)
		}
		time.Sleep(time.Hour)
	}
}

func init() {
	// After the first frame, off the main thread.
	deferStartupTask("homebrew-updates", func() { go hbUpdateLoop() })
}
