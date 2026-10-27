package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The reader's <video> and video-embed support (webpage_video.go): every
// resolvable video becomes a dedicated block, the layout reserves a 16:9 play
// card for it on the same row grid images use, and a play card is also a link
// hit the browser activates through playSmartURL — streaming media directly
// and handing YouTube/Vimeo/... to yt-dlp.

func TestParseWebVideoBlocks(t *testing.T) {
	doc := parseWebPage(`<body>
		<p>Intro text</p>
		<video src="https://cdn.example.com/clip.mp4" title="The clip"></video>
		<video><source src="https://cdn.example.com/b.webm"></video>
		<iframe src="https://www.youtube.com/embed/dEADbEEF" data-src-x="y" title="Embed"></iframe>
		<video data-src="/lazy.mov"></video>
	</body>`, "", "https://example.com/page")

	var clips []string
	for _, b := range doc.Blocks {
		if !b.Video {
			continue
		}
		clips = append(clips, b.VideoSrc)
	}
	if len(clips) != 4 {
		t.Fatalf("video blocks = %v, want 4", clips)
	}
	want := []string{
		"https://cdn.example.com/clip.mp4",
		"https://cdn.example.com/b.webm",
		"https://www.youtube.com/embed/dEADbEEF",
		"https://example.com/lazy.mov",
	}
	for i := range want {
		if clips[i] != want[i] {
			t.Errorf("clip %d = %q, want %q", i, clips[i], want[i])
		}
	}
	// The title attribute rides along for the card label.
	for _, b := range doc.Blocks {
		if b.Video && b.VideoSrc == "https://cdn.example.com/clip.mp4" && b.VideoTitle != "The clip" {
			t.Errorf("clip title = %q, want \"The clip\"", b.VideoTitle)
		}
	}
}

func TestWebVideoIframeFiltering(t *testing.T) {
	doc := parseWebPage(`<body>
		<iframe src="https://player.vimeo.com/video/123"></iframe>
		<iframe src="https://www.youtube-nocookie.com/embed/xy"></iframe>
		<iframe src="https://dailymotion.com/video/z"></iframe>
		<iframe src="https://clips.twitch.tv/embed"></iframe>
		<iframe src="https://media.example.org/clip.mp4"></iframe>
		<iframe src="https://example.com/not-a-video"></iframe>
		<iframe src="https://evil-example.com/youtube.com"></iframe>
	</body>`, "", "")

	var found []string
	for _, b := range doc.Blocks {
		if b.Video {
			found = append(found, b.VideoSrc)
		}
	}
	if len(found) != 5 {
		t.Fatalf("video blocks = %v, want 5 (host/page suffix matches, lookalike host excluded)", found)
	}
	for _, src := range found {
		if strings.Contains(src, "evil-example.com") || strings.Contains(src, "not-a-video") {
			t.Errorf("structurally non-video iframe %q rendered as a play card", src)
		}
	}
}

func TestWebVideoUnescapesEntityEncodedSrc(t *testing.T) {
	doc := parseWebPage(`<body>
		<video src="https://cdn.example.com/w?start=1&amp;end=2"></video>
	</body>`, "", "https://example.com/")

	for _, b := range doc.Blocks {
		if b.Video {
			if b.VideoSrc != "https://cdn.example.com/w?start=1&end=2" {
				t.Fatalf("entity-encoded src survived: %q", b.VideoSrc)
			}
			return
		}
	}
	t.Fatal("no video block parsed")
}

func TestWebVideoUnresolvableSrcSkipped(t *testing.T) {
	doc := parseWebPage(`<body>
		<video></video>
		<iframe src="https://example.com/plain-site"></iframe>
	</body>`, "", "")
	for _, b := range doc.Blocks {
		if b.Video {
			t.Fatalf("unresolvable element produced a play card: %+v", b)
		}
	}
}

func TestWebVideoLayoutReservesRows(t *testing.T) {
	doc := parseWebPage(`<body>
		<p>Before</p>
		<video src="https://cdn.example.com/reel.mp4" width="640" height="360"></video>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 900)
	if layout == nil {
		t.Fatal("no layout")
	}
	if len(layout.Videos) != 1 {
		t.Fatalf("video boxes = %d, want 1", len(layout.Videos))
	}
	v := layout.Videos[0]
	if v.Src != "https://cdn.example.com/reel.mp4" {
		t.Errorf("video box src = %q", v.Src)
	}
	if v.Rect.W <= 0 || v.Rect.H <= 0 {
		t.Fatalf("video box = %dx%d", v.Rect.W, v.Rect.H)
	}
	// 16:9 on the page's own grid; small row rounding keeps it near that.
	ratio := float64(v.Rect.W) / float64(v.Rect.H)
	if ratio < 1.4 || ratio > 2.0 {
		t.Errorf("video box aspect = %.2f, want ~16:9", ratio)
	}
	// Rows reserved: the card owns ceil(H/RowH) grid rows in reading order.
	if layout.RowH > 0 {
		need := (v.Rect.H + layout.RowH - 1) / layout.RowH
		if int32(len(layout.Rows)) < need {
			t.Errorf("rows = %d for a %dpx card at RowH %d, want >= %d",
				len(layout.Rows), v.Rect.H, layout.RowH, need)
		}
	}
}

func TestWebVideoCardIsAnActivatableLink(t *testing.T) {
	doc := parseWebPage(`<body>
		<p>Read <a href="/docs/start">this</a>.</p>
		<video src="https://cdn.example.com/reel.mp4" title="Big reveal"></video>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 900)
	if layout == nil {
		t.Fatal("no layout")
	}
	var (
		anchor, card bool
		cardRow      int
	)
	for _, hit := range layout.Links {
		switch {
		case hit.Href == "/docs/start":
			anchor = true
		case hit.Href == "https://cdn.example.com/reel.mp4":
			card = true
			cardRow = hit.Row
			if !hit.Video {
				t.Errorf("play-card link not flagged Video: %+v", hit)
			}
			if !strings.HasPrefix(hit.Text, "▶") {
				t.Errorf("play-card label = %q, want a ▶ glyph", hit.Text)
			}
			if hit.Row < 0 || hit.Row >= len(layout.Rows) {
				t.Errorf("play card sits on row %d of %d", hit.Row, len(layout.Rows))
			}
		}
	}
	if !anchor || !card {
		t.Fatalf("links = %v, missing the text anchor (%v) or the play card (%v)", layout.Links, anchor, card)
	}
	if cardRow < 1 {
		t.Errorf("play card row = %d; it should come after the intro text row", cardRow)
	}
	// Cards that draw nothing still need no link.
	empty := parseWebPage(`<body><video></video></body>`, "", "")
	emptyLayout := empty.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 900)
	if emptyLayout != nil {
		for _, hit := range emptyLayout.Links {
			if hit.Video {
				t.Errorf("dead element produced link %+v", hit)
			}
		}
	}
}

// YouTube's real thumbnails come from the URL alone, so a page with a YouTube
// embed gets a play card carrying the video's actual artwork. The id must
// come from every embed form a real page uses, and never from playlist,	// channel or lookalike URLs.
func TestWebYouTubeVideoID(t *testing.T) {
	cases := [][2]string{
		{"https://www.youtube.com/embed/dEADbEEF999", "dEADbEEF999"},
		{"https://youtube.com/embed/dEADbEEF999", "dEADbEEF999"},
		{"https://www.youtube-nocookie.com/embed/dEADbEEF999", "dEADbEEF999"},
		{"https://www.youtube.com/shorts/dEADbEEF999", "dEADbEEF999"},
		{"https://www.youtube.com/watch?v=dEADbEEF999", "dEADbEEF999"},
		{"https://youtu.be/dEADbEEF999", "dEADbEEF999"},
		{"https://www.youtube.com/live/dEADbEEF999?feature=share", "dEADbEEF999"},
		{"https://evil-example.com/youtube.com/embed/dEADbEEF999", ""},
		{"https://www.youtube.com/playlist?list=dEADbEEF999", ""},
		{"https://www.youtube.com/embed/dEADbEEF9", ""},
		{"https://www.youtube.com/", ""},
	}
	for i, tc := range cases {
		if got := webYouTubeVideoID(tc[0]); got != tc[1] {
			t.Errorf("case %d %s: id = %q, want %q", i, tc[0], got, tc[1])
		}
	}
}

func TestWebVideoBlockCarriesThumbnail(t *testing.T) {
	doc := parseWebPage(`<body>
		<iframe src="https://www.youtube.com/embed/dEADbEEF999" title="Embed"></iframe>
		<video src="https://player.vimeo.com/video/123" title="Vimeo clip"></video>
		<video src="https://cdn.example.com/clip.mp4"></video>
	</body>`, "", "https://example.com/page")

	var yt, plain bool
	for _, b := range doc.Blocks {
		switch {
		case b.Video && b.VideoSrc == "https://www.youtube.com/embed/dEADbEEF999":
			yt = true
			want := webYouTubeThumbBase + "dEADbEEF999/mqdefault.jpg"
			if b.VideoThumb != want {
				t.Errorf("YouTube thumb = %q, want %q", b.VideoThumb, want)
			}
		case b.Video && strings.Contains(b.VideoSrc, "vimeo.com"):
			plain = true
			if b.VideoThumb != "" {
				t.Errorf("non-YouTube video gained a thumbnail: %q", b.VideoThumb)
			}
		}
	}
	if !yt {
		t.Fatal("the YouTube embed never got a play card")
	}
	if !plain {
		t.Fatal("the Vimeo video never got a play card")
	}
}

// The thumbnail joins the page's image pass: same limits, same bytes-in-doc
// contract, and the layout hands the URL to the painter.
func TestWebVideoThumbnailFetch(t *testing.T) {
	pngBytes := tinyTestPNG(t, 32, 18)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	oldBase := webYouTubeThumbBase
	webYouTubeThumbBase = server.URL + "/vi/"
	defer func() { webYouTubeThumbBase = oldBase }()

	doc := parseWebPage(`<body>
		<p>Story</p>
		<iframe src="https://www.youtube.com/embed/dEADbEEF999" title="Embed"></iframe>
	</body>`, "", "https://example.com/page")
	var thumb string
	for _, b := range doc.Blocks {
		if b.Video && b.VideoThumb != "" {
			thumb = b.VideoThumb
		}
	}
	if thumb == "" {
		t.Fatal("the YouTube block parsed without a thumbnail URL")
	}
	fetchWebImages(doc)
	if got := doc.imageBytes(thumb); !bytes.Equal(got, pngBytes) {
		t.Fatalf("thumbnail fetch got %d bytes, want the served PNG", len(got))
	}
	if meta, _ := doc.imageMeta(thumb); !meta.HasSize || meta.W != 32 || meta.H != 18 {
		t.Errorf("thumbnail meta = %+v, want 32x18", meta)
	}
}

// A card whose thumbnail never arrived falls back to the chrome-only card:
// the painter must not crash on an empty bytes map, and the layout still
// records the thumb URL so a repainted page keeps its chance to draw it.
func TestWebVideoThumbnailFallback(t *testing.T) {
	doc := parseWebPage(`<body><iframe src="https://www.youtube.com/embed/dEADbEEF999"></iframe></body>`, "", "")
	fetchWebImages(doc) // no server: every thumbnail fetch fails
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 640)
	if layout == nil || len(layout.Videos) != 1 {
		t.Fatalf("layout videos = %+v, want one card", layout)
	}
	if layout.Videos[0].Thumb == "" {
		t.Error("the card lost its thumbnail URL after a failed fetch")
	}
	// And with no URL at all, the plain card is what draws.
	if drawWebVideoThumb(nil, doc, "", sdl.Rect{}) {
		t.Error("a card without a thumbnail URL reported artwork")
	}
}

// The yt-dlp-free thumbnail derivation is also the media browser's fallback.
// A Shorts feed row or a search result that ships no artwork (flat-playlist
// entries are common for both) maps its video URL onto the image CDN and paints
// the video's real frame through the disk-backed loader, exactly like a reader
// play card. Real metadata always wins; non-YouTube and lookalike-host entries
// keep the metadata-only behaviour.
func TestRealThumbnailCandidatesForMediaCards(t *testing.T) {
	id := "dEADbEEF999"
	want := webYouTubeThumbBase + id + "/mqdefault.jpg"

	// Flat-playlist Shorts row: no artwork metadata at all, artwork still
	// comes out of the URL.
	if got := realThumbnailCandidates(VideoInfo{WebpageURL: "https://www.youtube.com/shorts/" + id, Title: "short"}); len(got) != 1 || got[0] != want {
		t.Fatalf("URL-only candidates = %v, want [%s]", got, want)
	}
	// A bare yt-dlp ID maps through the watch URL.
	if got := realThumbnailCandidates(VideoInfo{ID: id}); len(got) != 1 || got[0] != want {
		t.Fatalf("ID-only candidates = %v, want [%s]", got, want)
	}
	// Real metadata wins and the derivation only trails as a fallback.
	got := realThumbnailCandidates(VideoInfo{WebpageURL: "https://youtu.be/" + id, Thumbnail: "https://example.com/own.jpg"})
	if len(got) != 2 || got[0] != "https://example.com/own.jpg" || got[1] != want {
		t.Fatalf("metadata candidates = %v, want own first and derived second", got)
	}
	// Non-YouTube hosts have nothing to derive.
	if got := realThumbnailCandidates(VideoInfo{WebpageURL: "https://vimeo.com/123"}); len(got) != 0 {
		t.Fatalf("vimeo candidates = %v, want none", got)
	}
	// A lookalike host never derives artwork.
	if got := realThumbnailCandidates(VideoInfo{WebpageURL: "https://youtube.evil.example/watch?v=" + id}); len(got) != 0 {
		t.Fatalf("lookalike candidates = %v, want none", got)
	}
}

// A search-results list row whose video has no artwork metadata paints the
// derived URL's bytes: the loader must actually reach the derived link and hand
// back a texture, and drawing the list must not panic anywhere around that
// fallback. The disk cache entry is cleared for the key so the fetch really
// happens in this run instead of a previous one leaving the answer behind.
func TestSearchResultCardLoadsDerivedThumbnail(t *testing.T) {
	pngBytes := tinyTestPNG(t, 32, 18)
	hits := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hits <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	oldBase := webYouTubeThumbBase
	webYouTubeThumbBase = server.URL + "/vi/"
	defer func() { webYouTubeThumbBase = oldBase }()

	vid := VideoInfo{ID: "dEADbEEF999", Title: "No art row"}
	url := normalizeThumbnailURL(webYouTubeThumbURL(vid.GetURL()))

	// Start cold: the texture cache, the memory cache and the disk entry all
	// drop the key, so a hit proves the derived URL is still reachable.
	getThumbnailTextures().Clear()
	thumbnailCacheMutex.Lock()
	thumbnailDataCache.drop(url)
	thumbnailCacheMutex.Unlock()
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		_ = cacheDelete(db, thumbnailDiskPrefix+url)
	}

	newProbeFrame(t)
	withScreen(t, probeW, probeH)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	// A fresh frame opens a fresh decode budget, whatever earlier tests spent
	// on the previous frame number.
	frameCount++
	tex := loadThumbnailFromURLs(renderer, realThumbnailCandidates(vid))
	if tex == nil {
		t.Fatal("no texture for the derived thumbnail URL")
	}
	select {
	case <-hits:
	default:
		t.Fatal("the derived URL was never fetched; the row would paint a generic tile")
	}

	// The real render path: one metadata-less row above the fold, drawn with
	// the shared loader, placeholder and all.
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		"search_results": []VideoInfo{vid},
	}}}
	frameCount++
	renderSearchResultsList(renderer, config, Element{Type: "searchresults", Variable: "search_results", X: 24, Y: 136}, config.Variables.Custom["search_results"].([]VideoInfo), 1232, 456)
}

// The Shorts grid rides the same loader: a feed row without thumbnails loads
// its derived artwork through drawThumbnail, and rendering the grid with no
// placeholder and no network still falls back to the flat tile instead of a
// blank cell.
func TestShortsGridDrawsWithoutArtworkMetadata(t *testing.T) {
	config := layoutTestConfig(t)
	config.Variables.Custom = map[string]interface{}{
		"shorts_list": []VideoInfo{
			{WebpageURL: "https://www.youtube.com/shorts/dEADbEEF999", Title: "No art"},
		},
	}
	prevIdx := currentShortIdx
	currentShortIdx = -1
	t.Cleanup(func() { currentShortIdx = prevIdx })

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	frameCount++
	// No server behind the derivation this time: the fetch fails, so the grid
	// must still render and leave a filled tile rather than a hole.
	renderShortsGrid(renderer, config, Element{Type: "shortslist", X: 24, Y: 72})
}

// Regression from the image layout deadlock: a video card with no recorded
// dimensions must not block layoutFor either.
func TestWebVideoLayoutWithoutSizeDoesNotDeadlock(t *testing.T) {
	newProbeFrame(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	doc := parseWebPage(`<body><p>x</p><video src="https://cdn.example.com/a.mp4"></video></body>`, "", "")
	if doc == nil {
		t.Fatal("page did not parse")
	}
	elem := Element{Type: "textbrowser", Font: "small"}
	done := make(chan *webLayout, 1)
	go func() { done <- doc.layoutFor(config, elem, 900) }()
	select {
	case layout := <-done:
		if layout == nil {
			t.Fatal("no layout")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("layoutFor deadlocked on a video card without dimensions")
	}
}
