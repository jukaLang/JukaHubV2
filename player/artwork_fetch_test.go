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

func TestArtworkFetchReportsProgressPerGame(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(png)
	}))
	defer server.Close()

	dest := t.TempDir()
	roms := []scannedROM{
		artworkTestROM(dest, "One.nes", "NES", "Nintendo - NES"),
		artworkTestROM(dest, "Two.nes", "NES", "Nintendo - NES"),
		artworkTestROM(dest, "Three.nes", "NES", "Nintendo - NES"),
	}
	var seen []int
	fetched, _, missing, stopped := fetchROMArtworkTracked(context.Background(), roms, server.Client(),
		server.URL, dest, nil, time.Now(), artworkMissTTL, func(done int) { seen = append(seen, done) })
	if stopped {
		t.Error("a complete fetch reported itself as stopped")
	}
	if fetched != 3 || missing != 0 {
		t.Fatalf("fetched %d, missing %d; want 3 and 0", fetched, missing)
	}
	if len(seen) != len(roms) || seen[0] != 1 || seen[len(seen)-1] != len(roms) {
		t.Fatalf("progress = %v, want one tick per game ending at %d", seen, len(roms))
	}
}

// TestArtworkFetchStopsBetweenGames is what makes the action interruptible: a
// cancelled context ends the loop, and the covers already written stay put.
func TestArtworkFetchStopsBetweenGames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := t.TempDir()
	roms := []scannedROM{
		artworkTestROM(dest, "One.nes", "NES", "Nintendo - NES"),
		artworkTestROM(dest, "Two.nes", "NES", "Nintendo - NES"),
	}
	var seen []int
	fetched, _, _, stopped := fetchROMArtworkTracked(ctx, roms, nil, "http://127.0.0.1:1", dest, nil,
		time.Now(), artworkMissTTL, func(done int) { seen = append(seen, done) })
	if !stopped {
		t.Error("cancelled fetch did not report stopped")
	}
	if fetched != 0 || len(seen) != 0 {
		t.Fatalf("cancelled fetch did %d covers, %d ticks; want none", fetched, len(seen))
	}
}

func TestArtworkFetchCancelsDuringARequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		http.Error(w, "gone", http.StatusInternalServerError)
	}))
	defer server.Close()

	dest := t.TempDir()
	roms := []scannedROM{
		artworkTestROM(dest, "One.nes", "NES", "Nintendo - NES"),
		artworkTestROM(dest, "Two.nes", "NES", "Nintendo - NES"),
	}
	var seen []int
	_, _, _, stopped := fetchROMArtworkTracked(ctx, roms, server.Client(), server.URL, dest, nil,
		time.Now(), artworkMissTTL, func(done int) { seen = append(seen, done) })
	if !stopped {
		t.Error("stopped flag was not set after the request was cancelled")
	}
	if len(seen) != 1 {
		t.Fatalf("progress ticks = %v, want one game handled before the stop", seen)
	}
}

func TestStopArtworkFetchWithoutARunIsANoOp(t *testing.T) {
	artworkFetchState.Lock()
	running, prevCancel := artworkFetchState.running, artworkFetchState.cancel
	artworkFetchState.running, artworkFetchState.cancel, artworkFetchState.stopping = false, nil, false
	artworkFetchState.Unlock()
	t.Cleanup(func() {
		artworkFetchState.Lock()
		artworkFetchState.running, artworkFetchState.cancel, artworkFetchState.stopping = running, prevCancel, false
		artworkFetchState.Unlock()
	})
	if stopArtworkFetch() {
		t.Error("stopArtworkFetch reported a stop with nothing running")
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
