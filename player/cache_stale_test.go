package main

import (
	"os"
	"testing"
	"time"
)

// cacheGetStale is what lets a media grid paint the last known page instead of a
// spinner, so it has to return exactly the values cacheGet refuses.
func TestCacheGetStaleReturnsExpiredValues(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		closeSharedCache()
		_ = os.Chdir(prev)
	})
	closeSharedCache()

	db, err := cacheOpen("stale.cache")
	if err != nil {
		t.Fatalf("cacheOpen: %v", err)
	}
	defer db.Close()

	if err := cacheSet(db, "k", []byte("value"), -time.Minute); err != nil {
		t.Fatalf("cacheSet: %v", err)
	}
	if got, err := cacheGet(db, "k"); err != nil || got != nil {
		t.Fatalf("cacheGet on an expired entry = %q, %v; want nil", got, err)
	}
	got, ok := cacheGetStale(db, "k")
	if !ok || string(got) != "value" {
		t.Fatalf("cacheGetStale = %q, %v; want the expired value", got, ok)
	}
	if _, ok := cacheGetStale(db, "missing"); ok {
		t.Fatal("cacheGetStale invented a value for a missing key")
	}
}
