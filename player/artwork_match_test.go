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

// The exact name is asked for first and every guess only after it 404s. A
// truncated title can land on another game's cover, so the order is part of the
// behaviour, not an implementation detail.
func TestArtworkTitleVariantsOrder(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{
			// Dump tags come off before the subtitle does: "Zelda II - The
			// Adventure of Link" is closer to the real name than "Zelda II".
			name: "Zelda II - The Adventure of Link (USA)",
			want: []string{
				"Zelda II - The Adventure of Link (USA)",
				"Zelda_II_-_The_Adventure_of_Link_(USA)",
				"Zelda II - The Adventure of Link",
				"Zelda_II_-_The_Adventure_of_Link",
				"Zelda II",
				"Zelda_II",
			},
		},
		{
			// A leading article is the last guess, and it keeps the tag form.
			name: "The Legend of Zelda (USA)",
			want: []string{
				"The Legend of Zelda (USA)",
				"The_Legend_of_Zelda_(USA)",
				"The Legend of Zelda",
				"The_Legend_of_Zelda",
				// The article-free twin of each name comes last, keeping its tags:
				// reference titles carry them too, so that is still a real file.
				"Legend of Zelda (USA)",
				"Legend_of_Zelda_(USA)",
				"Legend of Zelda",
				"Legend_of_Zelda",
			},
		},
		{
			// No tags, no subtitle: the underscored twin is the only extra.
			name: "Mario",
			want: []string{"Mario"},
		},
	}
	for _, tc := range cases {
		got := artworkTitleVariants(tc.name)
		if len(got) != len(tc.want) {
			t.Fatalf("%q: variants = %q, want %q", tc.name, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("%q: variant %d = %q, want %q (all: %q)", tc.name, i, got[i], tc.want[i], got)
			}
		}
	}

	if got := artworkTitleVariants("   "); len(got) != 0 {
		t.Errorf("a blank stem produced variants: %q", got)
	}
}

// One title cannot turn a library fetch into a sweep, and no name is asked for
// twice.
func TestArtworkTitleVariantsDedupedAndCapped(t *testing.T) {
	long := "The Legend of Zelda - The Adventure of Link - Collector's Edition (USA) (Rev 1) [!]"
	variants := artworkTitleVariants(long)
	if len(variants) > artworkVariantLimit {
		t.Fatalf("%d variants for one title, cap is %d: %q", len(variants), artworkVariantLimit, variants)
	}
	seen := map[string]bool{}
	for _, v := range variants {
		if v == "" {
			t.Errorf("empty variant in %q", variants)
		}
		if seen[v] {
			t.Errorf("duplicate variant %q in %q", v, variants)
		}
		seen[v] = true
	}
	// The first request is always the name the ROM actually has.
	if variants[0] != long {
		t.Errorf("first variant = %q, want the exact stem %q", variants[0], long)
	}
}

// The end-to-end case the backlog named: a ROM whose folder name is a shortened
// version of the reference title still gets its cover, and only after the exact
// names have been asked for.
func TestFetchROMArtworkMatchesShortenedTitle(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/Nintendo - NES/Named_Boxarts/Zelda II.png" {
			_, _ = w.Write(png)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	dest := t.TempDir()
	rom := artworkTestROM(dest, "Zelda II - The Adventure of Link (USA).nes", "NES", "Nintendo - NES")
	fetched, skipped, missing := fetchROMArtworkWithMisses(context.Background(), []scannedROM{rom},
		server.Client(), server.URL, dest, map[string]time.Time{}, time.Now(), artworkMissTTL)
	if fetched != 1 || skipped != 0 || missing != 0 {
		t.Fatalf("fetch = %d/%d/%d, want the cover found (1/0/0)", fetched, skipped, missing)
	}
	// The cover is still written under the ROM's own name, so the library card
	// finds it without knowing which variant matched.
	if _, err := os.Stat(filepath.Join(dest, "NES", "Zelda II - The Adventure of Link (USA).png")); err != nil {
		t.Fatalf("downloaded cover missing: %v", err)
	}

	mu.Lock()
	asked := append([]string(nil), paths...)
	mu.Unlock()
	want := []string{
		"/Nintendo - NES/Named_Boxarts/Zelda II - The Adventure of Link (USA).png",
		"/Nintendo - NES/Named_Boxarts/Zelda_II_-_The_Adventure_of_Link_(USA).png",
		"/Nintendo - NES/Named_Boxarts/Zelda II - The Adventure of Link.png",
		"/Nintendo - NES/Named_Boxarts/Zelda_II_-_The_Adventure_of_Link.png",
		"/Nintendo - NES/Named_Boxarts/Zelda II.png",
	}
	if len(asked) != len(want) {
		t.Fatalf("asked %d names, want %d: %q", len(asked), len(want), asked)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Fatalf("request %d = %q, want %q", i, asked[i], want[i])
		}
	}
}

// A name that is found straight away must not be chased with guesses: that
// would multiply a full library fetch by the variant count for no gain.
func TestFetchROMArtworkDoesNotGuessAfterAHit(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write(png)
	}))
	defer server.Close()

	dest := t.TempDir()
	rom := artworkTestROM(dest, "The Legend of Zelda (USA).nes", "NES", "Nintendo - NES")
	if fetched, _, _ := fetchROMArtworkWithMisses(context.Background(), []scannedROM{rom},
		server.Client(), server.URL, dest, map[string]time.Time{}, time.Now(), artworkMissTTL); fetched != 1 {
		t.Fatal("the exact name was not used")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("asked %d names when the first one worked: %q", len(paths), paths)
	}
}

// A miss recorded by the older matcher answers a question this build no longer
// asks, so it must not keep a newly-matchable title hidden for a month.
func TestArtworkMissCacheIgnoresOlderMatcher(t *testing.T) {
	dest := t.TempDir()
	path := artworkMissPath(dest)
	old := `{"version":1,"missing":{"NES/Zelda II - The Adventure of Link (USA)":"` +
		time.Now().Add(-time.Hour).Format(time.RFC3339) + `"}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadArtworkMisses(dest); len(got) != 0 {
		t.Fatalf("misses written by matcher v1 were kept: %v", got)
	}

	if err := saveArtworkMisses(dest, map[string]time.Time{"NES/Still missing": time.Now()}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 2`) {
		t.Errorf("rewritten cache is stamped %s", raw)
	}
	if got := loadArtworkMisses(dest); len(got) != 1 {
		t.Fatalf("recent miss did not survive the rewrite: %v", got)
	}
}
