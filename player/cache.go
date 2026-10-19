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

// cacheDelete removes a cached key.
func cacheDelete(db *bolt.DB, key string) error {
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
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
