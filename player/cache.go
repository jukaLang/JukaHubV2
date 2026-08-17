package main

import (
	"encoding/json"
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
