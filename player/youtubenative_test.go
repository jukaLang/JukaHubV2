package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ytPlayerFixture decodes a recorded player answer. The shapes below are copied
// from real answers: the muxed itag 18 entry the ANDROID_VR client returns, the
// adaptive (video-only) entries it also returns, and a ciphered entry with no
// usable URL.
func ytPlayerFixture(t *testing.T, body string) *ytPlayerResponse {
	t.Helper()
	var out ytPlayerResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("fixture does not decode: %v", err)
	}
	return &out
}

const ytPlayableAnswer = `{
  "playabilityStatus": {"status": "OK"},
  "streamingData": {
    "formats": [
      {"itag": 18, "url": "https://rr1.googlevideo.com/videoplayback?itag=18",
       "mimeType": "video/mp4", "qualityLabel": "360p", "height": 360,
       "vcodec": "avc1.42001E", "acodec": "mp4a.40.2"},
      {"itag": 22, "url": "https://rr1.googlevideo.com/videoplayback?itag=22",
       "mimeType": "video/mp4", "qualityLabel": "720p", "height": 720,
       "vcodec": "avc1.64001F", "acodec": "mp4a.40.2"},
      {"itag": 37, "url": "https://rr1.googlevideo.com/videoplayback?itag=37",
       "mimeType": "video/mp4", "qualityLabel": "1080p", "height": 1080,
       "vcodec": "avc1.640028", "acodec": "mp4a.40.2"},
      {"itag": 137, "mimeType": "video/mp4", "qualityLabel": "1080p", "height": 1080,
       "vcodec": "avc1.640028", "acodec": "none"}
    ],
    "adaptiveFormats": [
      {"itag": 137, "url": "https://rr1.googlevideo.com/videoplayback?itag=137",
       "mimeType": "video/mp4", "height": 1080, "vcodec": "avc1.640028", "acodec": "none"}
    ]
  }
}`

func TestYouTubeVideoIDReadsEveryLinkShape(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=-KPeZqUch08":       "-KPeZqUch08",
		"https://youtube.com/watch?v=-KPeZqUch08&t=30s":     "-KPeZqUch08",
		"https://music.youtube.com/watch?v=-KPeZqUch08":     "-KPeZqUch08",
		"https://youtu.be/-KPeZqUch08":                      "-KPeZqUch08",
		"https://youtu.be/-KPeZqUch08?si=abc":               "-KPeZqUch08",
		"https://www.youtube.com/shorts/-KPeZqUch08":        "-KPeZqUch08",
		"https://www.youtube.com/embed/-KPeZqUch08?rel=0":   "-KPeZqUch08",
		"https://www.youtube.com/live/-KPeZqUch08":          "-KPeZqUch08",
		"  https://www.youtube.com/watch?v=-KPeZqUch08  ":   "-KPeZqUch08",
		"https://example.com/watch?v=-KPeZqUch08":           "",
		"https://www.youtube.com/@somechannel/videos":       "",
		"https://www.youtube.com/watch?v=tooshort":          "",
		"https://rr1.googlevideo.com/videoplayback?itag=18": "",
		"C:\\Videos\\clip.mp4":                              "",
		"":                                                  "",
	}
	for link, want := range cases {
		if got := youtubeVideoID(link); got != want {
			t.Errorf("youtubeVideoID(%q) = %q, want %q", link, got, want)
		}
	}
}

// The player opens the resolved URL twice, once for pictures and once for sound,
// so a video-only or audio-only entry is useless even when it is the only one.
func TestYouTubeNativePicksMuxedFormatUnderCeiling(t *testing.T) {
	player := ytPlayerFixture(t, ytPlayableAnswer)
	direct, ok := ytStreamFromPlayer(player, "-KPeZqUch08", 720)
	if !ok {
		t.Fatal("a playable answer was refused")
	}
	if !strings.Contains(direct, "itag=22") {
		t.Fatalf("picked %q, want the 720p muxed format", direct)
	}

	direct, ok = ytStreamFromPlayer(player, "-KPeZqUch08", 360)
	if !ok {
		t.Fatal("a playable answer was refused at 360p")
	}
	if !strings.Contains(direct, "itag=18") {
		t.Fatalf("picked %q, want the 360p muxed format", direct)
	}
}

// A ciphered entry has no URL the player can open; guessing at one produces a
// broken stream, so the answer is "ask the helper" instead.
func TestYouTubeNativeRefusesWhenNothingIsDirect(t *testing.T) {
	player := ytPlayerFixture(t, `{
	  "playabilityStatus": {"status": "OK"},
	  "streamingData": {
	    "formats": [
	      {"itag": 18, "signatureCipher": "s=abc&url=https%3A%2F%2Frr1.googlevideo.com"}
	    ],
	    "adaptiveFormats": [
	      {"itag": 137, "url": "https://rr1.googlevideo.com/videoplayback?itag=137",
	       "height": 1080, "vcodec": "avc1.640028", "acodec": "none"}
	    ]
	  }
	}`)
	if direct, ok := ytStreamFromPlayer(player, "-KPeZqUch08", 720); ok {
		t.Fatalf("resolved %q from an answer with no muxed stream", direct)
	}
}

// The bot check says nothing about this video, so the fast path stands down for
// a while rather than paying a round trip on every play.
func TestYouTubeNativeBotCheckStartsCooldown(t *testing.T) {
	t.Cleanup(resetYTNativeState)
	resetYTNativeState()

	player := ytPlayerFixture(t, `{
	  "playabilityStatus": {"status": "LOGIN_REQUIRED",
	    "reason": "Sign in to confirm you're not a bot"}
	}`)
	if _, ok := ytStreamFromPlayer(player, "-KPeZqUch08", 720); ok {
		t.Fatal("resolved a stream from a refused answer")
	}
	if ytNativeAllowed() {
		t.Fatal("the fast path is still allowed after a bot check")
	}

	// The cooldown expires on its own.
	ytSessionMu.Lock()
	ytBlockedUntil = time.Now().Add(-time.Second)
	ytSessionMu.Unlock()
	if !ytNativeAllowed() {
		t.Fatal("the fast path stayed blocked after the cooldown expired")
	}
}

// A private or removed video is about that video: the next one should still get
// the fast path.
func TestYouTubeNativeUnplayableVideoDoesNotStartCooldown(t *testing.T) {
	t.Cleanup(resetYTNativeState)
	resetYTNativeState()

	player := ytPlayerFixture(t, `{
	  "playabilityStatus": {"status": "ERROR",
	    "reason": "This video is not available"}
	}`)
	if _, ok := ytStreamFromPlayer(player, "-KPeZqUch08", 720); ok {
		t.Fatal("resolved a stream from an unavailable video")
	}
	if !ytNativeAllowed() {
		t.Fatal("one unavailable video blocked the fast path for every video")
	}
}

func TestYTIsBotCheckRecognisesTheGate(t *testing.T) {
	cases := []struct {
		status, reason string
		want           bool
	}{
		{"LOGIN_REQUIRED", "", true},
		{"LOGIN_REQUIRED", "Sign in to confirm you're not a bot", true},
		{"ERROR", "Sign in to confirm you're not a bot", true},
		{"ERROR", "This video is private", false},
		{"UNPLAYABLE", "The uploader has not made this video available in your country", false},
	}
	for _, c := range cases {
		if got := ytIsBotCheck(c.status, c.reason); got != c.want {
			t.Errorf("ytIsBotCheck(%q, %q) = %v, want %v", c.status, c.reason, got, c.want)
		}
	}
}

// The bootstrap page is the only source of visitor data, and the player call is
// answered with the bot check without it.
func TestYouTubeSessionReadsBootstrapPage(t *testing.T) {
	page := `{"responseContext":{"serviceTrackingParams":[]},
	  "INNERTUBE_API_KEY":"AIzaSyExampleKey123",
	  "visitorData":"CgtWQUR1LWpxUFkxayjt4KfXBjIKCgJVUxIEGgAg",
	  "INNERTUBE_CLIENT_VERSION":"2.20261030.02.00"}`
	if m := ytVisitorPattern.FindStringSubmatch(page); len(m) != 2 ||
		m[1] != "CgtWQUR1LWpxUFkxayjt4KfXBjIKCgJVUxIEGgAg" {
		t.Fatalf("visitor data not found: %v", m)
	}
	if m := ytAPIKeyPattern.FindStringSubmatch(page); len(m) != 2 || m[1] != "AIzaSyExampleKey123" {
		t.Fatalf("api key not found: %v", m)
	}
	if m := ytVisitorPattern.FindStringSubmatch("no visitor here"); m != nil {
		t.Fatalf("found visitor data in a page without one: %v", m)
	}
}

// streamableSource must use the native answer when it has one and never launch
// the helper for a video it already resolved.
func TestStreamableSourceUsesNativeStream(t *testing.T) {
	t.Cleanup(resetYTNativeState)
	resetYTNativeState()
	ytNativeOff = false
	resetDirectStreamCache()
	t.Cleanup(resetDirectStreamCache)

	original := ytNativeStreamFor
	t.Cleanup(func() { ytNativeStreamFor = original })

	calls := 0
	ytNativeStreamFor = func(ctx context.Context, videoID string, maxHeight int) (string, bool) {
		calls++
		if videoID != "-KPeZqUch08" {
			t.Errorf("native client asked for %q", videoID)
		}
		return "https://rr1.googlevideo.com/videoplayback?itag=22", true
	}

	// An empty helper path means any fallback would fail, so a resolved stream
	// proves the native answer was used.
	source := "https://www.youtube.com/watch?v=-KPeZqUch08"
	direct, ok := streamableSource(context.Background(), &Config{}, "", source)
	if !ok || !strings.Contains(direct, "itag=22") {
		t.Fatalf("streamableSource = %q, %v; want the native URL", direct, ok)
	}
	if calls != 1 {
		t.Fatalf("native client called %d times, want once", calls)
	}
	// The second request is served from the cache rather than asked again.
	if _, ok := streamableSource(context.Background(), &Config{}, "", source); !ok {
		t.Fatal("cached stream was not reused")
	}
	if calls != 1 {
		t.Fatalf("native client called %d times after caching, want once", calls)
	}
}

func TestStreamableSourceFallsBackWhenNativeDeclines(t *testing.T) {
	t.Cleanup(resetYTNativeState)
	resetYTNativeState()
	ytNativeOff = false
	resetDirectStreamCache()
	t.Cleanup(resetDirectStreamCache)

	original := ytNativeStreamFor
	t.Cleanup(func() { ytNativeStreamFor = original })
	ytNativeStreamFor = func(ctx context.Context, videoID string, maxHeight int) (string, bool) {
		return "", false
	}

	// With no helper configured the fallback cannot resolve either. What matters
	// is that a refusal does not become a false "playing" state with no URL.
	direct, ok := streamableSource(context.Background(), &Config{}, "", "https://youtu.be/-KPeZqUch08")
	if ok || direct != "" {
		t.Fatalf("streamableSource = %q, %v; want no stream", direct, ok)
	}
}

func TestYouTubeNativeStateIsNotLeftBlockedAfterReset(t *testing.T) {
	t.Cleanup(resetYTNativeState)
	resetYTNativeState()
	if !ytNativeAllowed() {
		t.Fatal("a fresh state starts blocked")
	}
	ytSessionMu.Lock()
	ytBlockedUntil = time.Now().Add(time.Minute)
	ytSessionMu.Unlock()
	if ytNativeAllowed() {
		t.Fatal("the cooldown was ignored")
	}
	resetYTNativeState()
	if !ytNativeAllowed() {
		t.Fatal("reset did not clear the cooldown")
	}
}

func TestYouTubeNativeDisabledHookSkipsTheNetwork(t *testing.T) {
	t.Cleanup(func() {
		ytNativeOff = false
		resetYTNativeState()
	})
	resetYTNativeState()
	ytNativeOff = true
	if err := ytEnsureSession(context.Background()); err == nil {
		t.Fatal("a disabled client still bootstrapped a session")
	}
	if ytNativeAllowed() {
		t.Fatal("a disabled client is still allowed")
	}
}

// resetDirectStreamCache forgets resolved URLs so one test cannot answer another.
func resetDirectStreamCache() {
	resolvedStreamsMu.Lock()
	clear(resolvedStreams)
	resolvedStreamsMu.Unlock()
}
