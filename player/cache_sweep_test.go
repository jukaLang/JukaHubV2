package main

import (
	"path/filepath"
	"testing"
	"time"
)

// Entries long past their TTL leave the cache; fresh ones and recently expired
// ones (the media grids still paint those while they refresh) stay. Nothing
// removed expired entries before, so jukahub.cache only ever grew on the card.
func TestCacheSweepRemovesLongExpiredEntries(t *testing.T) {
	db, err := cacheOpen(filepath.Join(t.TempDir(), "sweep.cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := cacheSet(db, "fresh", []byte("a"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := cacheSet(db, "recently-expired", []byte("b"), -time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := cacheSet(db, "long-gone", []byte("c"), -30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	n, err := cacheSweepExpired(db, cacheExpiredGrace)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("swept %d entries, want 1", n)
	}
	if _, ok := cacheGetStale(db, "long-gone"); ok {
		t.Error("an entry a month past its TTL survived the sweep")
	}
	for _, key := range []string{"fresh", "recently-expired"} {
		if _, ok := cacheGetStale(db, key); !ok {
			t.Errorf("%s was swept", key)
		}
	}
}
