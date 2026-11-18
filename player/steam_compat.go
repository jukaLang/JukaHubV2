package main

// steam_compat.go — how well a Steam game plays on a handheld, as Valve rates
// it for the Steam Deck: Verified, Playable, Unsupported, or not tested. The
// store's own Deck widget reads the rating from
// store.steampowered.com/saleaction/ajaxgetdeckappcompatibilityreport, which
// needs no sign-in. A rating is asked for only when its game is on screen,
// one game at a time, and kept in data/steam/compat.json for a week (ratings
// do change). Steam asking to slow down pauses the lookups for ten minutes.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Valve's Deck compatibility categories, as resolved_category gives them.
const (
	steamDeckUnknown     uint8 = 0
	steamDeckUnsupported uint8 = 1
	steamDeckPlayable    uint8 = 2
	steamDeckVerified    uint8 = 3
)

const (
	steamCompatTTL         = 7 * 24 * time.Hour
	steamCompatMax         = 20000
	steamCompatFileVersion = 1
	// steamCompatGap spaces the lookups: the store answers 429 to bursts.
	steamCompatGap = 350 * time.Millisecond
)

// steamCompatRecord is one game's rating and when it was read.
type steamCompatRecord struct {
	Deck uint8 `json:"deck"`
	At   int64 `json:"at"` // Unix seconds
}

type steamCompatFile struct {
	Version int                          `json:"version"`
	Apps    map[string]steamCompatRecord `json:"apps"`
}

var steamCompat struct {
	mu         sync.Mutex
	loading    bool
	loaded     bool
	apps       map[int]steamCompatRecord
	pending    map[int]bool      // queued or being read this session
	failed     map[int]time.Time // could not be read: when it was tried
	dirty      bool
	pauseUntil time.Time
	queue      chan int
	once       sync.Once
}

func steamCompatPath() string { return filepath.Join(steamDataDir(), "compat.json") }

// steamCompatLoad reads compat.json once, on a goroutine.
func steamCompatLoad() {
	var f steamCompatFile
	apps := map[int]steamCompatRecord{}
	if _, err := readJSONWithBackup(steamCompatPath(), &f); err == nil && f.Version == steamCompatFileVersion {
		for k, rec := range f.Apps {
			id, err := strconv.Atoi(k)
			if err != nil || id <= 0 || id > 1<<31-1 || rec.Deck > steamDeckVerified {
				continue
			}
			apps[id] = rec
			if len(apps) >= steamCompatMax {
				break
			}
		}
	}
	steamCompat.mu.Lock()
	for id, rec := range steamCompat.apps {
		apps[id] = rec // read while the file loaded: newer
	}
	steamCompat.apps = apps
	steamCompat.loaded, steamCompat.loading = true, false
	steamCompat.mu.Unlock()
}

// steamCompatSave writes the ratings read so far, when any are new.
func steamCompatSave() {
	steamCompat.mu.Lock()
	if !steamCompat.dirty || !steamCompat.loaded {
		steamCompat.mu.Unlock()
		return
	}
	f := steamCompatFile{Version: steamCompatFileVersion, Apps: make(map[string]steamCompatRecord, len(steamCompat.apps))}
	for id, rec := range steamCompat.apps {
		f.Apps[strconv.Itoa(id)] = rec
	}
	steamCompat.dirty = false
	steamCompat.mu.Unlock()
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	if err := os.MkdirAll(steamDataDir(), 0o700); err != nil {
		return
	}
	if err := AtomicWrite(steamCompatPath(), append(data, '\n'), 0o600); err != nil {
		logWarn("steam", "saving compat.json failed: %v", err)
	}
}

// steamCompatFresh reports whether rec was read within the week. A clock a
// little behind counts the rating as fresh; one read more than a day "in the
// future" (a clock that was wrong then) is shown but read again.
func steamCompatFresh(rec steamCompatRecord, now time.Time) bool {
	at := time.Unix(rec.At, 0)
	return !at.After(now.Add(24*time.Hour)) && now.Sub(at) < steamCompatTTL
}

// steamCompatRetry is how long a lookup that failed waits before the next.
const steamCompatRetry = 15 * time.Minute

// steamDeckRating is appid's Deck rating, and whether one is known. Asking
// for a game with no fresh rating queues a lookup. Main thread; no I/O.
func steamDeckRating(appid int) (uint8, bool) {
	if appid <= 0 || steamIsShortcut(appid) {
		return steamDeckUnknown, false
	}
	steamCompat.mu.Lock()
	defer steamCompat.mu.Unlock()
	if !steamCompat.loaded {
		if !steamCompat.loading {
			steamCompat.loading = true
			go steamCompatLoad()
		}
		return steamDeckUnknown, false
	}
	now := time.Now()
	rec, ok := steamCompat.apps[appid]
	if ok && steamCompatFresh(rec, now) {
		return rec.Deck, true
	}
	if failedAt, failed := steamCompat.failed[appid]; failed && now.Sub(failedAt) < steamCompatRetry {
		return rec.Deck, ok
	}
	if steamCompat.pending[appid] || now.Before(steamCompat.pauseUntil) || !IsOnline() {
		return rec.Deck, ok
	}
	steamCompat.once.Do(func() {
		steamCompat.queue = make(chan int, 64)
		go steamCompatWorker()
	})
	select {
	case steamCompat.queue <- appid:
		if steamCompat.pending == nil {
			steamCompat.pending = map[int]bool{}
		}
		steamCompat.pending[appid] = true
	default:
		// The queue is full: asked again on a later frame.
	}
	return rec.Deck, ok
}

func steamCompatWorker() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("steam", "Deck rating worker stopped: %v", r)
		}
	}()
	for appid := range steamCompat.queue {
		steamCompat.mu.Lock()
		paused := time.Now().Before(steamCompat.pauseUntil)
		steamCompat.mu.Unlock()
		if !paused {
			cat, err := steamFetchDeckRating(appid)
			steamCompat.mu.Lock()
			switch {
			case err == nil:
				if len(steamCompat.apps) < steamCompatMax {
					steamCompat.apps[appid] = steamCompatRecord{Deck: cat, At: time.Now().Unix()}
					steamCompat.dirty = true
				}
			case steamIsTooMany(err):
				steamCompat.pauseUntil = time.Now().Add(10 * time.Minute)
				logDebug("steam", "the store asked to slow down; Deck ratings pause")
			default:
				if steamCompat.failed == nil {
					steamCompat.failed = map[int]time.Time{}
				}
				steamCompat.failed[appid] = time.Now()
				logDebug("steam", "Deck rating of %d: %v", appid, err)
			}
			steamCompat.mu.Unlock()
		}
		steamCompat.mu.Lock()
		delete(steamCompat.pending, appid)
		steamCompat.mu.Unlock()
		if len(steamCompat.queue) == 0 {
			steamCompatSave()
		}
		time.Sleep(steamCompatGap)
	}
}

func steamIsTooMany(err error) bool {
	var he *steamHTTPError
	return errors.As(err, &he) && he.code == http.StatusTooManyRequests
}

// steamFetchDeckRating asks the store for appid's Deck category.
func steamFetchDeckRating(appid int) (uint8, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := steamGet(ctx, "https://store.steampowered.com/saleaction/ajaxgetdeckappcompatibilityreport?nAppID="+strconv.Itoa(appid)+"&l=english", 1<<20)
	if err != nil {
		return steamDeckUnknown, err
	}
	return steamParseDeckRating(data)
}

// steamParseDeckRating reads results.resolved_category; an app Valve has not
// rated answers without results (or with an empty list), which is Unknown.
func steamParseDeckRating(data []byte) (uint8, error) {
	var body struct {
		Results json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return steamDeckUnknown, err
	}
	if len(body.Results) == 0 || body.Results[0] != '{' {
		return steamDeckUnknown, nil
	}
	var res struct {
		Category int `json:"resolved_category"`
	}
	if err := json.Unmarshal(body.Results, &res); err != nil {
		return steamDeckUnknown, err
	}
	if res.Category < 0 || res.Category > int(steamDeckVerified) {
		return steamDeckUnknown, nil
	}
	return uint8(res.Category), nil
}

// steamDeckLabel is a rating as a row says it, "" when untested.
func steamDeckLabel(c uint8) string {
	switch c {
	case steamDeckVerified:
		return "Deck Verified"
	case steamDeckPlayable:
		return "Deck Playable"
	case steamDeckUnsupported:
		return "Deck Unsupported"
	}
	return ""
}

// steamDeckSentence is a rating as the game's sheet says it.
func steamDeckSentence(c uint8, known bool) string {
	switch {
	case !known:
		return ""
	case c == steamDeckVerified:
		return "Steam Deck Verified · plays well with a controller on a small screen"
	case c == steamDeckPlayable:
		return "Steam Deck Playable · may need a tweak, a keyboard or small text"
	case c == steamDeckUnsupported:
		return "Steam Deck Unsupported · not made for a handheld"
	}
	return "Not rated for Steam Deck yet"
}
