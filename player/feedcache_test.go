package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// feedCacheTestEnv points the shared cache at a temp database so the fallback
// copy is isolated per test and cleaned up on exit.
func feedCacheTestEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		closeSharedCache()
		_ = os.Chdir(prev)
	})
}

// TestFeedFallbackServesLastGoodWhenOffline is the whole point: a feed fetched
// while online is remembered, and an offline boot serves that copy instead of
// an empty screen.
func TestFeedFallbackServesLastGoodWhenOffline(t *testing.T) {
	feedCacheTestEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<item><title>Ep 1</title></item>"))
	}))
	defer srv.Close()

	// Online: the fetch succeeds and stores a fallback copy.
	if got := httpGetTextFallbackContext(context.Background(), srv.URL, 2*time.Second); got == "" {
		t.Fatal("online fetch returned nothing")
	}

	// Simulate offline by flipping the connectivity monitor.
	netMon.mu.Lock()
	netMon.online, netMon.checked = false, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked = true, true
		netMon.mu.Unlock()
	})

	// Point the fetch at a dead server to force a transport error, but the URL
	// key must match the one we stored, so reuse the same server URL by closing
	// it and expecting a connection error.
	srv.Close()
	if got := httpGetTextFallbackContext(context.Background(), srv.URL, time.Second); got == "" {
		t.Fatal("offline fetch served no cached feed; fallback did not engage")
	}
}

// TestFeedFallbackNotUsedWhileOnline: a fresh URL with no cached copy and a
// working server returns the live body, not a fallback.
func TestFeedFallbackNotUsedWhileOnline(t *testing.T) {
	feedCacheTestEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("live body"))
	}))
	defer srv.Close()
	if got := httpGetTextFallbackContext(context.Background(), srv.URL, 2*time.Second); got != "live body" {
		t.Fatalf("got %q, want live body", got)
	}
}

// TestStoreFeedFallbackIgnoresOversize: a body past the cap is not stored, so
// an over-large response cannot fill the card through the fallback path.
func TestStoreFeedFallbackIgnoresOversize(t *testing.T) {
	feedCacheTestEnv(t)
	big := make([]byte, feedFallbackMaxBody+1)
	storeFeedFallback("http://example/big", big)
	if _, ok := loadFeedFallback("http://example/big"); ok {
		t.Fatal("oversize body was stored as a feed fallback")
	}
}

// TestLoadFeedFallbackMissingKey: an unknown URL reports no copy.
func TestLoadFeedFallbackMissingKey(t *testing.T) {
	feedCacheTestEnv(t)
	if _, ok := loadFeedFallback("http://example/never-seen"); ok {
		t.Fatal("unknown URL reported a cached fallback")
	}
}

// TestStoreThenLoadFeedFallbackRoundTrip: the basic persistence contract.
func TestStoreThenLoadFeedFallbackRoundTrip(t *testing.T) {
	feedCacheTestEnv(t)
	storeFeedFallback("http://example/feed", []byte("<item>x</item>"))
	body, ok := loadFeedFallback("http://example/feed")
	if !ok || body != "<item>x</item>" {
		t.Fatalf("round-trip = %q,%v want the stored body", body, ok)
	}
	// The cache file must actually exist on disk (state, not memory).
	if _, err := os.Stat(filepath.Join(".", "jukahub.cache")); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}
