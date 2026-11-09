package main

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const cacheBucket = "cache"

// cacheEntry represents a cached value with an expiration timestamp.
type cacheEntry struct {
	Value     []byte
	ExpiresAt int64
}

// cacheOpen opens (or creates) the bbolt cache database.
//
// bbolt holds an exclusive lock on the file, so opening the database that
// openSharedCache already holds waits out the one-second timeout and then fails
// — silently turning a cache read into a network fetch. In-process callers that
// touch jukahub.cache must use openSharedCache; cacheOpen is for a path nothing
// else in the process has open (a copy, a test, a second database).
func cacheOpen(path string) (*bolt.DB, error) {
	return bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
}

var (
	sharedCacheMu   sync.Mutex
	sharedCacheDB   *bolt.DB
	sharedCachePath string
)

// openSharedCache returns a process-wide handle for the cache database at
// path. Opening bbolt costs roughly 12 ms (file open plus mmap), which used to
// dominate every thumbnail fetch; reusing one handle makes cache reads cheap.
// A changed path (tests chdir between runs) closes the previous handle first.
func openSharedCache(path string) (*bolt.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return cacheOpen(path)
	}
	sharedCacheMu.Lock()
	defer sharedCacheMu.Unlock()
	if sharedCacheDB != nil && sharedCachePath == abs {
		return sharedCacheDB, nil
	}
	db, err := cacheOpen(path)
	if err != nil {
		return nil, err
	}
	if sharedCacheDB != nil {
		_ = sharedCacheDB.Close()
	}
	sharedCacheDB, sharedCachePath = db, abs
	return db, nil
}

// closeSharedCache releases the shared handle. Tests that switch working
// directories call it so a temp database file is not kept open across the
// cleanup; the app itself can leave it open for the whole run.
func closeSharedCache() {
	sharedCacheMu.Lock()
	defer sharedCacheMu.Unlock()
	if sharedCacheDB != nil {
		_ = sharedCacheDB.Close()
		sharedCacheDB = nil
		sharedCachePath = ""
	}
}

// cacheGet retrieves a cached value by key. Returns nil if missing or expired.
func cacheGet(db *bolt.DB, key string) ([]byte, error) {
	var val []byte
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v == nil {
			return nil
		}
		var entry cacheEntry
		if err := json.Unmarshal(v, &entry); err != nil {
			return nil
		}
		if time.Now().Unix() > entry.ExpiresAt {
			return nil
		}
		val = entry.Value
		return nil
	})
	return val, err
}

// cacheGetStale returns a stored value whether or not its TTL has passed. A
// reader that can paint the value and refresh behind it (the media grids) uses
// this to show the last known list instead of an empty screen and a spinner;
// anything that needs current data keeps using cacheGet.
func cacheGetStale(db *bolt.DB, key string) ([]byte, bool) {
	var val []byte
	_ = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v == nil {
			return nil
		}
		var entry cacheEntry
		if err := json.Unmarshal(v, &entry); err != nil {
			return nil
		}
		val = entry.Value
		return nil
	})
	return val, val != nil
}

// cacheSet stores a value with a TTL duration.
func cacheSet(db *bolt.DB, key string, value []byte, ttl time.Duration) error {
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(cacheBucket))
		if err != nil {
			return err
		}
		entry := cacheEntry{
			Value:     value,
			ExpiresAt: time.Now().Add(ttl).Unix(),
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		return b.Put([]byte(key), data)
	})
}

// cacheExpiredGrace is how long past its TTL an entry is still kept. The media
// grids paint a stale list and refresh behind it (cacheGetStale), so an entry
// is not dropped the moment it expires, only once nothing would show it.
const cacheExpiredGrace = 14 * 24 * time.Hour

// cacheSweepExpired deletes the entries more than grace past their expiry, and
// unreadable ones, and reports how many went. Nothing removed them before:
// every thumbnail and feed answer stayed in jukahub.cache on the card for good.
func cacheSweepExpired(db *bolt.DB, grace time.Duration) (int, error) {
	cutoff := time.Now().Add(-grace).Unix()
	removed := 0
	err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return nil
		}
		var stale [][]byte
		if err := b.ForEach(func(k, v []byte) error {
			// Only the stamp is decoded; the value (often a whole thumbnail)
			// is skipped over, not copied.
			var meta struct{ ExpiresAt int64 }
			if json.Unmarshal(v, &meta) != nil || meta.ExpiresAt < cutoff {
				stale = append(stale, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range stale {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		removed = len(stale)
		return nil
	})
	return removed, err
}

// sweepSharedCacheLater runs the expiry sweep on the shared cache a while after
// boot, off the startup path and away from the first thumbnail reads.
func sweepSharedCacheLater(delay time.Duration) {
	time.AfterFunc(delay, func() {
		db, err := openSharedCache("jukahub.cache")
		if err != nil {
			return
		}
		if n, err := cacheSweepExpired(db, cacheExpiredGrace); err != nil {
			logWarn("cache", "expiry sweep failed: %v", err)
		} else if n > 0 {
			logDebug("cache", "expiry sweep removed %d entries", n)
		}
	})
}

// cacheClear removes all cached entries.
func cacheClear(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return nil
		}
		return tx.DeleteBucket([]byte(cacheBucket))
	})
}
