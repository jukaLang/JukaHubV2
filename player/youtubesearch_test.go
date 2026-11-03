package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The native path must never answer a command it does not fully understand: a
// pasted link, a cookie file or a filter belongs to yt-dlp, where the user's own
// flags are honoured exactly. A download-format cap is not one of those: a flat
// playlist carries no formats for it to choose between.
func TestNativeSearchRequestGate(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		ok    bool
		query string
		start int
		end   int
	}{
		{
			name:  "stock search",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 5 "ytsearch5:lofi beats"`,
			ok:    true,
			query: "lofi beats",
			start: 1,
			end:   5,
		},
		{
			name:  "second page",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --skip-download --quiet --ignore-errors --playlist-start 6 --playlist-end 10 "ytsearch10:lofi"`,
			ok:    true,
			query: "lofi",
			start: 6,
			end:   10,
		},
		{
			name:  "empty query became popular",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --skip-download --quiet --playlist-start 1 --playlist-end 5 "ytsearch5:popular"`,
			ok:    true,
			query: "popular",
			start: 1,
			end:   5,
		},
		{
			// The cap names a download format, and a flat playlist carries no
			// formats, so the list this path returns is the same one. Refusing
			// it meant every search of a user who picked a resolution in
			// Settings paid yt-dlp's startup on every load.
			name:  "resolution cap does not change a flat list",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --skip-download --quiet --playlist-start 1 --playlist-end 5 -f "best[height<=720]" "ytsearch5:lofi"`,
			ok:    true,
			query: "lofi",
			start: 1,
			end:   5,
		},
		{
			name: "cookies change what is visible",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download --quiet --playlist-start 1 --playlist-end 5 --cookies "c:\yt\cookies.txt" "ytsearch5:lofi"`,
			ok:   false,
		},
		{
			name: "a pasted link is not a search",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download --quiet "https://www.youtube.com/watch?v=abc123"`,
			ok:   false,
		},
		{
			name: "a second positional argument",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download "ytsearch5:lofi" "ytsearch5:jazz"`,
			ok:   false,
		},
		{
			name: "another tool",
			cmd:  `youtube-dl --flat-playlist --dump-single-json --skip-download "ytsearch5:lofi"`,
			ok:   false,
		},
		{
			name: "not a metadata dump",
			cmd:  `yt-dlp --flat-playlist --skip-download --quiet "ytsearch5:lofi"`,
			ok:   false,
		},
		{
			name: "not flat",
			cmd:  `yt-dlp --dump-single-json --skip-download --quiet "ytsearch5:lofi"`,
			ok:   false,
		},
		{
			// Past one response but inside the window the continuation can
			// build: the endpoint's first page names about twenty results and
			// its token asks for the next twenty.
			name:  "window past one response follows the continuation",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --skip-download --playlist-start 25 --playlist-end 30 "ytsearch30:lofi"`,
			ok:    true,
			query: "lofi",
			start: 25,
			end:   30,
		},
		{
			name: "window past every page this path builds",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download --playlist-start 61 --playlist-end 70 "ytsearch70:lofi"`,
			ok:   false,
		},
		{
			name: "window ends before it starts",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download --playlist-start 9 --playlist-end 5 "ytsearch10:lofi"`,
			ok:   false,
		},
		{
			name: "search count below the window",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download --playlist-start 1 --playlist-end 10 "ytsearch5:lofi"`,
			ok:   false,
		},
		{
			name: "empty query",
			cmd:  `yt-dlp --flat-playlist --dump-single-json --skip-download "ytsearch5:"`,
			ok:   false,
		},
		{
			name:  "inline flag values",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --skip-download --playlist-start=1 --playlist-end=5 "ytsearch5:lofi"`,
			ok:    true,
			query: "lofi",
			start: 1,
			end:   5,
		},
		{
			name:  "no window flags at all",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --skip-download "ytsearch5:lofi"`,
			ok:    true,
			query: "lofi",
			start: 1,
			end:   5,
		},
		{
			// The Shorts row asks for ten results with no window; the whole
			// count is what it wants, not the default page of five.
			name:  "count with no window is the window",
			cmd:   `yt-dlp --flat-playlist --dump-single-json --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors "ytsearch10:shorts" `,
			ok:    true,
			query: "shorts",
			start: 1,
			end:   10,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := nativeSearchRequestFor(tc.cmd)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, req)
			}
			if !tc.ok {
				return
			}
			if req.query != tc.query || req.start != tc.start || req.end != tc.end {
				t.Fatalf("request = %+v, want query %q window %d..%d", req, tc.query, tc.start, tc.end)
			}
		})
	}
}

// The endpoint answers in the two-column shape for a desktop client. Every
// field the grid draws has to come out of it, and the entries that are not
// videos have to be skipped rather than drawn as empty cards.
const nativeSearchTwoColumnFixture = `{"contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{"contents":[
{"itemSectionRenderer":{"contents":[
{"videoRenderer":{"videoId":"abc123","title":{"runs":[{"text":"First "},{"text":"video"}]},"lengthText":{"simpleText":"12:34"},"viewCountText":{"simpleText":"1,234,567 views"},"ownerText":{"runs":[{"text":"Chan"}]},"publishedTimeText":{"simpleText":"2 days ago"},"thumbnail":{"thumbnails":[{"url":"https://i.ytimg.com/vi/abc123/default.jpg","width":120,"height":90},{"url":"https://i.ytimg.com/vi/abc123/hqdefault.jpg","width":480,"height":360}]}}},
{"shelfRenderer":{"title":{"simpleText":"A shelf"}}},
{"videoRenderer":{"videoId":"live01","title":{"simpleText":"Live now"},"isLiveNow":true,"shortViewCountText":{"simpleText":"1.2M views"},"ownerText":{"runs":[{"text":"Live Chan"}]}}},
{"videoRenderer":{"videoId":"hour01","title":{"simpleText":"An hour long"},"lengthText":{"simpleText":"1:02:03"},"shortViewCountText":{"simpleText":"12K views"},"ownerText":{"runs":[{"text":"Long Chan"}]},"thumbnail":{"thumbnails":[{"url":"//i.ytimg.com/vi/hour01/hqdefault.jpg","width":480,"height":360}]}}}
]}}
]}}}}}`

func TestParseNativeYouTubeSearchTwoColumn(t *testing.T) {
	videos := parseNativeYouTubeSearch([]byte(nativeSearchTwoColumnFixture))
	if len(videos) != 3 {
		t.Fatalf("parsed %d videos, want 3: %+v", len(videos), videos)
	}
	first := videos[0]
	if first.ID != "abc123" || first.Title != "First video" {
		t.Errorf("first = %q / %q, want the id and joined title runs", first.ID, first.Title)
	}
	if first.Channel != "Chan" || first.Uploader != "Chan" {
		t.Errorf("channel = %q uploader = %q, want the owner text", first.Channel, first.Uploader)
	}
	if first.Duration != 754 {
		t.Errorf("duration = %v, want 754 seconds for 12:34", first.Duration)
	}
	if first.ViewCount != 1234567 {
		t.Errorf("view count = %d, want the exact long form", first.ViewCount)
	}
	if !strings.HasSuffix(first.Thumbnail, "hqdefault.jpg") {
		t.Errorf("thumbnail = %q, want the widest image", first.Thumbnail)
	}
	if first.GetURL() != "https://www.youtube.com/watch?v=abc123" {
		t.Errorf("url = %q, want the watch page", first.GetURL())
	}

	live := videos[1]
	if live.Duration != 0 {
		t.Errorf("live duration = %v, want unknown", live.Duration)
	}
	if live.ViewCount != 1200000 {
		t.Errorf("live view count = %d, want the rounded short form", live.ViewCount)
	}

	hour := videos[2]
	if hour.Duration != 3723 {
		t.Errorf("duration = %v, want 3723 seconds for 1:02:03", hour.Duration)
	}
	if hour.ViewCount != 12000 {
		t.Errorf("view count = %d, want 12K expanded", hour.ViewCount)
	}
	if !strings.HasPrefix(hour.Thumbnail, "https://") {
		t.Errorf("thumbnail = %q, want a scheme, not a protocol-relative URL", hour.Thumbnail)
	}
}

// The same data arrives as a flat section list when the page has no sidebar.
func TestParseNativeYouTubeSearchFlatShape(t *testing.T) {
	const fixture = `{"contents":{"sectionListRenderer":{"contents":[
{"itemSectionRenderer":{"contents":[{"videoRenderer":{"videoId":"flat01","title":{"simpleText":"Flat result"},"lengthText":{"simpleText":"3:07"}}}]}}
]}}}`
	videos := parseNativeYouTubeSearch([]byte(fixture))
	if len(videos) != 1 {
		t.Fatalf("parsed %d videos, want 1", len(videos))
	}
	if videos[0].ID != "flat01" || videos[0].Title != "Flat result" || videos[0].Duration != 187 {
		t.Fatalf("video = %+v, want the flat entry's id, title and length", videos[0])
	}
}

// A body that is not the search page must read as no results, which is what
// sends the request to yt-dlp.
func TestParseNativeYouTubeSearchRejectsOtherBodies(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"contents":{"twoColumnSearchResultsRenderer":{}}}`, `not json`} {
		if videos := parseNativeYouTubeSearch([]byte(body)); len(videos) != 0 {
			t.Errorf("body %q parsed %d videos, want none", body, len(videos))
		}
	}
}

// A page is a slice of the one response: page two must not need a second
// request, and a window past the entries that arrived must not panic.
func TestNativeSearchWindowSlice(t *testing.T) {
	videos := make([]VideoInfo, 20)
	for i := range videos {
		videos[i].ID = string(rune('a' + i))
	}
	cases := []struct {
		start, end int
		want       int
		firstID    string
	}{
		{1, 5, 5, "a"},
		{6, 10, 5, "f"},
		{16, 20, 5, "p"},
		{18, 25, 3, "r"},
		{21, 25, 0, ""},
		{0, 5, 0, ""},
		{5, 5, 1, "e"},
	}
	for _, tc := range cases {
		got := sliceNativeSearchWindow(videos, tc.start, tc.end)
		if len(got) != tc.want {
			t.Errorf("window %d..%d returned %d entries, want %d", tc.start, tc.end, len(got), tc.want)
			continue
		}
		if tc.want > 0 && got[0].ID != tc.firstID {
			t.Errorf("window %d..%d starts at %q, want %q", tc.start, tc.end, got[0].ID, tc.firstID)
		}
	}
	if got := sliceNativeSearchWindow(nil, 1, 5); got != nil {
		t.Errorf("empty result returned %v, want nil", got)
	}
}

// TestNativeSearchBreakerStandsDownAfterFailures pins the cooldown that keeps a
// network which cannot reach the endpoint from paying the attempt on every
// load. A success brings the fast path straight back.
func TestNativeSearchBreakerStandsDownAfterFailures(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	breaker := nativeSearchBreaker{now: func() time.Time { return now }}
	if breaker.coolingDown() {
		t.Fatal("a fresh breaker is already standing down")
	}
	breaker.note(errors.New("TLS handshake timeout"))
	if breaker.coolingDown() {
		t.Fatal("one failure stood the native path down")
	}
	breaker.note(errors.New("TLS handshake timeout"))
	if !breaker.coolingDown() {
		t.Fatal("two failures did not stand the native path down")
	}
	now = now.Add(nativeSearchCooldown - time.Second)
	if !breaker.coolingDown() {
		t.Fatal("the cooldown ended early")
	}
	now = now.Add(2 * time.Second)
	if breaker.coolingDown() {
		t.Fatal("the cooldown never ended")
	}
	breaker.note(nil)
	if breaker.coolingDown() {
		t.Fatal("a success left the native path standing down")
	}
}

// sameMediaResults is what stops a refresh from republishing the page the grid
// already shows, so the order and the set both matter.
func TestSameMediaResults(t *testing.T) {
	a := []VideoInfo{{ID: "1"}, {ID: "2"}}
	b := []VideoInfo{{ID: "1"}, {ID: "2"}}
	if !sameMediaResults(a, b) {
		t.Fatal("identical lists compared unequal")
	}
	if sameMediaResults(a, []VideoInfo{{ID: "1"}}) {
		t.Fatal("a shorter list compared equal")
	}
	if sameMediaResults(a, []VideoInfo{{ID: "2"}, {ID: "1"}}) {
		t.Fatal("a reordered list compared equal")
	}
}

// ── Continuation paging ─────────────────────────────────────────────────────
//
// These run against a local server: the window logic is the part that decides
// how many requests a page costs, and that has to be provable without a network.

const nativeSearchFirstPageFixture = `{"contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{"contents":[
{"itemSectionRenderer":{"contents":[
{"videoRenderer":{"videoId":"aa01","title":{"simpleText":"First"},"thumbnail":{"thumbnails":[{"url":"https://i.ytimg.com/vi/aa01/hq.jpg","width":480}]}}},
{"videoRenderer":{"videoId":"aa02","title":{"simpleText":"Second"}}}
]}},
{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"TOKEN-1"}}}}
]}}}}}`

const nativeSearchContinuationFixture = `{"onResponseReceivedCommands":[{"appendContinuationItemsAction":{"continuationItems":[
{"itemSectionRenderer":{"contents":[
{"videoRenderer":{"videoId":"bb01","title":{"simpleText":"Third"}}},
{"videoRenderer":{"videoId":"bb02","title":{"simpleText":"Fourth"}}}
]}},
{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"TOKEN-2"}}}}
]}}]}`

const nativeSearchLastContinuationFixture = `{"onResponseReceivedCommands":[{"appendContinuationItemsAction":{"continuationItems":[
{"itemSectionRenderer":{"contents":[
{"videoRenderer":{"videoId":"cc01","title":{"simpleText":"Fifth"}}}
]}}
]}}]}`

// nativeSearchFakeServer answers the three fixtures above in order and counts
// the requests it served, so a test can assert what a page actually cost.
func nativeSearchFakeServer(t *testing.T, last []byte) func() int {
	t.Helper()
	var mu sync.Mutex
	served := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		served++
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Continuation string `json:"continuation"`
		}
		_ = json.Unmarshal(body, &payload)
		switch {
		case payload.Continuation == "":
			_, _ = io.WriteString(w, nativeSearchFirstPageFixture)
		case payload.Continuation == "TOKEN-1":
			_, _ = io.WriteString(w, nativeSearchContinuationFixture)
		default:
			_, _ = w.Write(last)
		}
	}))
	prevURL, prevClient := nativeSearchURL, nativeSearchHTTPClient
	nativeSearchURL, nativeSearchHTTPClient = server.URL, server.Client()
	nativeSearchWindows = nativeSearchWindowCache{}
	t.Cleanup(func() {
		nativeSearchURL, nativeSearchHTTPClient = prevURL, prevClient
		nativeSearchWindows = nativeSearchWindowCache{}
		server.Close()
	})
	return func() int { mu.Lock(); defer mu.Unlock(); return served }
}

func TestNativeSearchWindowReusesOneFetchForEveryPage(t *testing.T) {
	requests := nativeSearchFakeServer(t, []byte(nativeSearchLastContinuationFixture))

	// The first page costs the one POST.
	first, err := nativeSearchWindowFor(context.Background(), "lofi", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || first[0].Title != "First" {
		t.Fatalf("first page = %+v, want the two entries of one response", first)
	}
	if got := requests(); got != 1 {
		t.Fatalf("requests after the first page = %d, want 1", got)
	}

	// Later pages inside the first response's twenty cost nothing: they are
	// slices of work already done.
	if _, err := nativeSearchWindowFor(context.Background(), "lofi", 2); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if got := requests(); got != 1 {
		t.Fatalf("requests after a repeat page = %d, want 1 (the window is cached)", got)
	}

	// Past the first response's entries the window follows the continuation.
	window, err := nativeSearchWindowFor(context.Background(), "lofi", 4)
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if len(window) != 4 || window[3].Title != "Fourth" {
		t.Fatalf("window = %+v, want the four entries of the two responses", window)
	}
	if got := requests(); got != 2 {
		t.Fatalf("requests after the continuation = %d, want 2", got)
	}

	// A window the endpoint cannot fill returns what exists, without error and
	// without another search: that is the end of the list.
	deep, err := nativeSearchWindowFor(context.Background(), "lofi", 9)
	if err != nil {
		t.Fatalf("deep window: %v", err)
	}
	if len(deep) != 5 {
		t.Fatalf("deep window = %d entries, want the five the endpoint has", len(deep))
	}
	if got := requests(); got != 3 {
		t.Fatalf("requests after the last continuation = %d, want 3", got)
	}

	// The end is remembered, so asking again is free.
	if _, err := nativeSearchWindowFor(context.Background(), "lofi", 9); err != nil {
		t.Fatalf("repeat deep window: %v", err)
	}
	if got := requests(); got != 3 {
		t.Fatalf("requests after re-asking at the end = %d, want 3", got)
	}
}

func TestNativeSearchWindowFollowsOneQueryAtATime(t *testing.T) {
	requests := nativeSearchFakeServer(t, []byte(nativeSearchLastContinuationFixture))
	if _, err := nativeSearchWindowFor(context.Background(), "lofi", 2); err != nil {
		t.Fatalf("first query: %v", err)
	}
	// A different query is a different search, not a slice of the old one.
	if _, err := nativeSearchWindowFor(context.Background(), "jazz", 2); err != nil {
		t.Fatalf("second query: %v", err)
	}
	if got := requests(); got != 2 {
		t.Fatalf("requests after two queries = %d, want 2", got)
	}
}

func TestNativeSearchWindowCapsTheRequestedDepth(t *testing.T) {
	// A command cannot ask for more than the window limit, however it is built:
	// the request count and the memory one search may cost stay bounded.
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Continuation string `json:"continuation"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Continuation == "" {
			_, _ = io.WriteString(w, nativeSearchFirstPageFixture)
			return
		}
		// A continuation that always has more to give: the cap, not the
		// endpoint, has to stop the paging.
		_, _ = io.WriteString(w, nativeSearchContinuationFixture)
	}))
	defer server.Close()
	prevURL, prevClient := nativeSearchURL, nativeSearchHTTPClient
	nativeSearchURL, nativeSearchHTTPClient = server.URL, server.Client()
	nativeSearchWindows = nativeSearchWindowCache{}
	defer func() {
		nativeSearchURL, nativeSearchHTTPClient = prevURL, prevClient
		nativeSearchWindows = nativeSearchWindowCache{}
	}()

	window, err := nativeSearchWindowFor(context.Background(), "lofi", 10_000)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window) > nativeSearchWindowLimit {
		t.Fatalf("window = %d entries, want at most %d", len(window), nativeSearchWindowLimit)
	}
	if posts > 1+nativeSearchMaxContinuations {
		t.Fatalf("posts = %d, want at most %d (one search, %d continuations)",
			posts, 1+nativeSearchMaxContinuations, nativeSearchMaxContinuations)
	}
}

// The token lives beside the result sections, not inside one, and a
// continuation page wraps its videos in an item section of its own.
func TestParseNativeSearchPageCarriesTheContinuation(t *testing.T) {
	page := parseNativeSearchPage([]byte(nativeSearchFirstPageFixture))
	if len(page.videos) != 2 || page.continuation != "TOKEN-1" {
		t.Fatalf("page = %d videos, token %q; want 2 and TOKEN-1", len(page.videos), page.continuation)
	}
	next := parseNativeContinuationPage([]byte(nativeSearchContinuationFixture))
	if len(next.videos) != 2 || next.videos[0].Title != "Third" || next.continuation != "TOKEN-2" {
		t.Fatalf("continuation = %+v, want Third/Fourth and TOKEN-2", next)
	}
	last := parseNativeContinuationPage([]byte(nativeSearchLastContinuationFixture))
	if len(last.videos) != 1 || last.continuation != "" {
		t.Fatalf("last continuation = %+v, want one entry and no token", last)
	}
	if got := parseNativeContinuationPage([]byte(`{"nonsense":true}`)); len(got.videos) != 0 {
		t.Fatalf("an unknown body produced %d videos", len(got.videos))
	}
}
