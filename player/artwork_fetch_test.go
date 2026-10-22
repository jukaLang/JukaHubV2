package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func artworkTestROM(root, name, system, collection string) scannedROM {
	return scannedROM{
		Path:       filepath.Join(root, filepath.FromSlash("Roms/"+strings.ReplaceAll(system, " ", "_")+"/"+name)),
		System:     system,
		Collection: collection,
	}
}

func TestFetchROMArtworkRecordsMissingAndSkipsRecentMisses(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/Found.png") {
			_, _ = w.Write(png)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	dest := t.TempDir()
	roms := []scannedROM{
		artworkTestROM(dest, "Found.nes", "NES", "Nintendo - NES"),
		artworkTestROM(dest, "Missing.nes", "NES", "Nintendo - NES"),
	}
	misses := map[string]time.Time{}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	fetched, skipped, missing := fetchROMArtworkWithMisses(context.Background(), roms, server.Client(),
		server.URL, dest, misses, now, artworkMissTTL)
	if fetched != 1 || skipped != 0 || missing != 1 {
		t.Fatalf("first pass = fetched %d, skipped %d, missing %d; want 1/0/1", fetched, skipped, missing)
	}
	if _, err := os.Stat(filepath.Join(dest, "NES", "Found.png")); err != nil {
		t.Fatalf("downloaded cover missing: %v", err)
	}
	key := artworkMissKey("Nintendo - NES", "Missing")
	if _, ok := misses[key]; !ok {
		t.Fatalf("404 was not recorded in the miss cache: %v", misses)
	}

	// A day later: the existing cover is skipped and the recent miss is not
	// requested again.
	mu.Lock()
	before := requests["/Nintendo - NES/Named_Boxarts/Missing.png"]
	mu.Unlock()
	fetched, skipped, missing = fetchROMArtworkWithMisses(context.Background(), roms, server.Client(),
		server.URL, dest, misses, now.Add(24*time.Hour), artworkMissTTL)
	if fetched != 0 || skipped != 1 || missing != 1 {
		t.Fatalf("second pass = fetched %d, skipped %d, missing %d; want 0/1/1", fetched, skipped, missing)
	}
	mu.Lock()
	after := requests["/Nintendo - NES/Named_Boxarts/Missing.png"]
	mu.Unlock()
	if after != before {
		t.Errorf("cached miss was requested again: %d -> %d requests", before, after)
	}

	// After the TTL the cover is worth asking about again.
	_, _, _ = fetchROMArtworkWithMisses(context.Background(), roms, server.Client(),
		server.URL, dest, misses, now.Add(artworkMissTTL+time.Hour), artworkMissTTL)
	mu.Lock()
	retried := requests["/Nintendo - NES/Named_Boxarts/Missing.png"]
	mu.Unlock()
	if retried <= after {
		t.Errorf("expired miss was not retried: %d requests", retried)
	}
}

func TestFetchROMArtworkDoesNotCacheServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "throttled", http.StatusTooManyRequests)
	}))
	defer server.Close()

	dest := t.TempDir()
	misses := map[string]time.Time{}
	rom := artworkTestROM(dest, "Game.nes", "NES", "Nintendo - NES")
	_, _, missing := fetchROMArtworkWithMisses(context.Background(), []scannedROM{rom}, server.Client(),
		server.URL, dest, misses, time.Now(), artworkMissTTL)
	if missing != 1 {
		t.Fatalf("missing = %d, want 1", missing)
	}
	if len(misses) != 0 {
		t.Errorf("a temporary server error was cached as a missing cover: %v", misses)
	}
}

func TestArtworkMissesRoundTripAndPruneExpiredEntries(t *testing.T) {
	dest := t.TempDir()
	now := time.Now()
	misses := map[string]time.Time{
		"NES/Old": now.Add(-5 * artworkMissTTL),
		"NES/New": now,
	}
	if err := saveArtworkMisses(dest, misses); err != nil {
		t.Fatal(err)
	}
	loaded := loadArtworkMisses(dest)
	if _, ok := loaded["NES/New"]; !ok {
		t.Errorf("recent miss did not survive the round trip: %v", loaded)
	}
	if _, ok := loaded["NES/Old"]; ok {
		t.Errorf("long-expired miss was kept: %v", loaded)
	}

	if err := os.WriteFile(artworkMissPath(dest), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadArtworkMisses(dest); len(got) != 0 {
		t.Errorf("corrupt cache parsed as %v, want empty", got)
	}
}

func TestRomsMissingArtworkFiltersLocalCovers(t *testing.T) {
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, "Game_Boy_Advance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "Game_Boy_Advance", "Have.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	roms := []scannedROM{
		{Path: "/roms/GBA/Have.gba", System: "Game Boy Advance", Collection: "Nintendo - Game Boy Advance"},
		{Path: "/roms/GBA/Want.gba", System: "Game Boy Advance", Collection: "Nintendo - Game Boy Advance"},
	}
	missing := romsMissingArtwork(roms, dest)
	if len(missing) != 1 || filepath.Base(missing[0].Path) != "Want.gba" {
		t.Fatalf("missing = %+v, want only Want.gba", missing)
	}
}
