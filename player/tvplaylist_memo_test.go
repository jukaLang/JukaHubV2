package main

// The Live TV scene re-ran loadIPTV on every entry, and that re-parsed the
// public playlist (about 11,400 channels, 1.1 s measured) even when the body in
// the on-disk cache had not changed. These tests pin the memo that makes the
// second and later entries cheap, and the explicit reload that bypasses it.
//
// They drive the memo directly rather than through the bbolt cache: the parse
// is the expensive part, and reaching it through the database would need the
// real playlist file open in the same process.

import (
	"os"
	"testing"
	"time"
)

const tvPlaylistCacheKey = "iptv:" + tvPublicPlaylistURL

const tvPlaylistFixture = `#EXTM3U
#EXTINF:-1 tvg-id="one" group-title="News",One
https://example.com/one.m3u8
#EXTINF:-1 tvg-id="two" group-title="Sport",Two
https://example.com/two.m3u8
`

// The same text must hand back the same parsed slice: an equal backing array is
// what proves the playlist was not parsed again.
func TestPlaylistParseIsReusedWhileTheTextIsUnchanged(t *testing.T) {
	forgetTVPlaylistParse()
	t.Cleanup(forgetTVPlaylistParse)

	first := parseM3URemembered(tvPlaylistFixture)
	if len(first) != 2 {
		t.Fatalf("parsed %d channels, want the two in the fixture", len(first))
	}
	second := parseM3URemembered(tvPlaylistFixture)
	if len(second) != 2 {
		t.Fatalf("second parse returned %d channels, want 2", len(second))
	}
	if &first[0] != &second[0] {
		t.Fatal("the playlist was parsed again although the text was unchanged")
	}

	// A changed body must be parsed, not served from the memo.
	third := parseM3URemembered(tvPlaylistFixture + "#EXTINF:-1,Three\nhttps://example.com/three.m3u8\n")
	if len(third) != 3 {
		t.Fatalf("after a changed playlist: %d channels, want 3", len(third))
	}
	if &third[0] == &first[0] {
		t.Fatal("a changed playlist reused the previous parse")
	}
}

// The explicit reload must not see the memo: pressing refresh expects a parse of
// the bytes, not the channels the last scene entry left behind.
func TestForgetPlaylistParseDropsTheMemo(t *testing.T) {
	forgetTVPlaylistParse()
	t.Cleanup(forgetTVPlaylistParse)

	first := parseM3URemembered(tvPlaylistFixture)
	if len(first) != 2 {
		t.Fatalf("parsed %d channels, want 2", len(first))
	}
	forgetTVPlaylistParse()
	second := parseM3URemembered(tvPlaylistFixture)
	if len(second) != 2 {
		t.Fatalf("after forgetting: %d channels, want 2", len(second))
	}
	if &first[0] == &second[0] {
		t.Fatal("the parse survived forgetTVPlaylistParse")
	}
}

// An empty body must not stick: a playlist that could not be fetched once would
// otherwise leave the scene empty until restart.
func TestEmptyPlaylistDoesNotStick(t *testing.T) {
	forgetTVPlaylistParse()
	t.Cleanup(forgetTVPlaylistParse)

	if list := parseM3URemembered(""); len(list) != 0 {
		t.Fatalf("empty text produced %d channels", len(list))
	}
	list := parseM3URemembered(tvPlaylistFixture)
	if len(list) != 2 {
		t.Fatalf("a real playlist after an empty one produced %d channels, want 2", len(list))
	}
	// And an empty body arriving after real channels clears the list instead of
	// leaving the previous parse in place.
	if got := parseM3URemembered(""); len(got) != 0 {
		t.Fatalf("empty text after a real playlist produced %d channels, want 0", len(got))
	}
}

// The playlist read must go through the shared cache handle. A second bbolt open
// of the same file waits out its one-second timeout and then fails, which used
// to send this function to the network on every Live TV entry — a second of the
// scene switch plus the whole playlist downloaded again.
func TestPublicPlaylistReadsThroughTheSharedCacheHandle(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previous)
		closeSharedCache()
		forgetTVPlaylistParse()
	})
	closeSharedCache()
	forgetTVPlaylistParse()

	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		t.Fatalf("open shared cache: %v", err)
	}
	if err := cacheSet(db, tvPlaylistCacheKey, []byte(tvPlaylistFixture), 6*time.Hour); err != nil {
		t.Fatalf("seed playlist: %v", err)
	}

	started := time.Now()
	first := loadTVPublicPlaylist()
	firstCost := time.Since(started)
	if len(first) != 2 {
		t.Fatalf("parsed %d channels, want the two in the cache", len(first))
	}
	// One second here is the exclusive-open timeout, i.e. the bug: the read fell
	// out of the cache and onto the network. Parsing the fixture is microseconds.
	if firstCost > 500*time.Millisecond {
		t.Fatalf("cached playlist took %v: the read did not use the shared handle", firstCost)
	}

	started = time.Now()
	second := loadTVPublicPlaylist()
	secondCost := time.Since(started)
	if len(second) != 2 {
		t.Fatalf("second read: %d channels, want 2", len(second))
	}
	if &first[0] != &second[0] {
		t.Fatal("the second read re-parsed an unchanged playlist")
	}
	if secondCost > 50*time.Millisecond {
		t.Fatalf("second read took %v, want a memo hit", secondCost)
	}
}
