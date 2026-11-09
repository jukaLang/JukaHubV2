package main

// youtubenative.go — a native YouTube client for the call the player makes on
// every play: turn a watch URL into a stream URL.
//
// Why it exists: resolving through the bundled helper costs a process launch
// before YouTube is even asked (measured: `yt-dlp --version` alone is 2.8 s on
// the desktop build, because the one-file binary unpacks itself on every run),
// and two round trips after that. The player call below is one POST over a
// session the app keeps warm: measured through streamableSource on the desktop
// build, the helper needs 4961 ms for one video, this needs 623 ms for the first
// play of a session (which fetches the bootstrap page) and 147 ms after that.
//
// It is a fast path, never the only path: streamableSource falls back to the
// helper whenever this refuses, which is the case for age-restricted videos, a
// client version YouTube has started gating, or a network where the bot check
// fires. A refusal is remembered for a while so a gated network pays for the
// attempt once instead of on every play.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The Innertube clients. The versions are load-bearing: YouTube bot-gates stale
// ones with LOGIN_REQUIRED ("Sign in to confirm you're not a bot"). Measured on
// this network, ANDROID_VR answered at 1.64 and above and was gated at 1.61-1.63,
// and ANDROID answered at 20.05 and above. When native resolution starts failing
// while the helper still works, bump these two strings — nothing else changes.
const (
	ytPlayerClientName    = "ANDROID_VR"
	ytPlayerClientVersion = "1.65.4"
	ytPlayerClientID      = "28" // X-YouTube-Client-Name for ANDROID_VR
	ytPlayerUserAgent     = "com.google.android.apps.youtube.vr.oculus/1.65.4 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip"
)

// ytSessionRefresh is how long a bootstrapped session (visitor id, cookies, API
// key) is reused before it is fetched again.
const ytSessionRefresh = 30 * time.Minute

// ytNativeBlockedFor is the cooldown after a refusal. A network that fails the
// bot check fails it for every video, so re-asking on each play would only add
// latency to a path that is about to fall back to the helper anyway.
const ytNativeBlockedFor = 10 * time.Minute

var (
	ytSessionMu      sync.Mutex
	ytHTTPClient     *http.Client
	ytVisitor        string
	ytAPIKey         string
	ytSessionFetched time.Time
	ytBlockedUntil   time.Time
	ytNativeOff      bool // test hook: skip the network entirely

	// ytBootstrapMu serialises the first fetch so a burst of prefetch jobs does
	// not download the same 850 KB page several times over.
	ytBootstrapMu sync.Mutex
)

// ytClient returns the shared client. The jar matters: the visitor cookie the
// homepage sets is part of what the player call is allowed to see.
func ytClient() *http.Client {
	ytSessionMu.Lock()
	defer ytSessionMu.Unlock()
	if ytHTTPClient == nil {
		jar, _ := cookiejar.New(nil)
		ytHTTPClient = &http.Client{Timeout: 20 * time.Second, Jar: jar}
	}
	return ytHTTPClient
}

var (
	ytVisitorPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"visitorData":"([^"]+)"`) })
	ytAPIKeyPattern  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"INNERTUBE_API_KEY":"([^"]+)"`) })
)

// ytEnsureSession bootstraps the visitor data, cookies and API key the player
// call needs. Measured at ~460 ms for the homepage; it is cached for
// ytSessionRefresh, so the cost is paid once per session, not once per video.
func ytEnsureSession(ctx context.Context) error {
	if ytNativeOff {
		return errors.New("native YouTube client disabled")
	}
	ytBootstrapMu.Lock()
	defer ytBootstrapMu.Unlock()
	ytSessionMu.Lock()
	if ytVisitor != "" && ytAPIKey != "" && time.Since(ytSessionFetched) < ytSessionRefresh {
		ytSessionMu.Unlock()
		return nil
	}
	if time.Now().Before(ytBlockedUntil) {
		ytSessionMu.Unlock()
		return errors.New("native YouTube client is cooling down")
	}
	ytSessionMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.youtube.com/", nil)
	if err != nil {
		return err
	}
	// The bootstrap is a page load in a browser, and is dressed as one for the
	// same reason the search request is.
	setWebBrowserHeaders(req, "none", "navigate", "document")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	resp, err := ytClient().Do(req)
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	defer resp.Body.Close()
	// The page is ~850 KB and the two strings are near the top; a bounded read
	// keeps a misbehaving response from becoming a memory spike on the card.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	visitor := ""
	if m := ytVisitorPattern().FindSubmatch(body); len(m) == 2 {
		visitor = string(m[1])
	}
	key := youtubeWebAPIKey // shared with the native search path
	if m := ytAPIKeyPattern().FindSubmatch(body); len(m) == 2 {
		key = string(m[1])
	}
	if visitor == "" {
		// Without visitor data the player call is answered with the bot check,
		// so this is a real failure rather than something to retry blindly.
		return errors.New("bootstrap page carried no visitor data")
	}
	ytSessionMu.Lock()
	ytVisitor, ytAPIKey, ytSessionFetched = visitor, key, time.Now()
	ytSessionMu.Unlock()
	return nil
}

// ytPlayerFormat is one entry of streamingData. Only muxed formats (video and
// audio in one file) are useful: the player opens the URL twice, once for
// pictures and once for sound.
type ytPlayerFormat struct {
	Itag         int    `json:"itag"`
	URL          string `json:"url"`
	MimeType     string `json:"mimeType"`
	QualityLabel string `json:"qualityLabel"`
	Height       int    `json:"height"`
	ACodec       string `json:"acodec"`
	VCodec       string `json:"vcodec"`
}

// ytPlayerResponse is the subset of the player answer this file reads.
type ytPlayerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	StreamingData struct {
		Formats         []ytPlayerFormat `json:"formats"`
		AdaptiveFormats []ytPlayerFormat `json:"adaptiveFormats"`
		HLSManifestURL  string           `json:"hlsManifestUrl"`
	} `json:"streamingData"`
	VideoDetails struct {
		Title         string `json:"title"`
		LengthSeconds string `json:"lengthSeconds"`
	} `json:"videoDetails"`
}

// ytPlayerCall performs one player request for a video id.
func ytPlayerCall(ctx context.Context, videoID string) (*ytPlayerResponse, error) {
	if err := ytEnsureSession(ctx); err != nil {
		return nil, err
	}
	ytSessionMu.Lock()
	visitor, key := ytVisitor, ytAPIKey
	ytSessionMu.Unlock()

	body := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName":        ytPlayerClientName,
			"clientVersion":     ytPlayerClientVersion,
			"androidSdkVersion": 32,
			"osName":            "Android",
			"osVersion":         "12L",
			"hl":                "en",
			"gl":                "US",
			"userAgent":         ytPlayerUserAgent,
			"visitorData":       visitor,
		}},
		"videoId":        videoID,
		"contentCheckOk": true,
		"racyCheckOk":    true,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := "https://www.youtube.com/youtubei/v1/player?key=" + url.QueryEscape(key) + "&prettyPrint=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ytPlayerUserAgent)
	req.Header.Set("X-YouTube-Client-Name", ytPlayerClientID)
	req.Header.Set("X-YouTube-Client-Version", ytPlayerClientVersion)
	req.Header.Set("X-Goog-Visitor-Id", visitor)
	resp, err := ytClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("player call failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("player call failed: HTTP %d", resp.StatusCode)
	}
	var out ytPlayerResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("player answer unreadable: %w", err)
	}
	return &out, nil
}

// ytMuxedURL picks the best muxed format at or below the ceiling. A format with
// no direct URL (a ciphered one) is skipped rather than guessed at: the helper
// is the fallback for those.
func ytMuxedURL(formats []ytPlayerFormat, maxHeight int) string {
	if maxHeight <= 0 {
		maxHeight = 720
	}
	bestURL, bestHeight := "", -1
	for _, f := range formats {
		if f.URL == "" || f.VCodec == "none" || f.ACodec == "none" {
			continue
		}
		height := f.Height
		if height == 0 {
			height = 1 // an unlabelled muxed format is still better than nothing
		}
		if height > maxHeight || height <= bestHeight {
			continue
		}
		bestURL, bestHeight = f.URL, height
	}
	return bestURL
}

// youtubeNativeStream resolves a video id to a direct stream URL. The second
// return value is false whenever the helper should be asked instead.
func youtubeNativeStream(ctx context.Context, videoID string, maxHeight int) (string, bool) {
	player, err := ytPlayerCall(ctx, videoID)
	if err != nil {
		logNativeStreamFailure(videoID, err)
		return "", false
	}
	return ytStreamFromPlayer(player, videoID, maxHeight)
}

// ytStreamFromPlayer is the decision made once the player answer is in, split
// out so it can be tested against recorded answers instead of the network.
func ytStreamFromPlayer(player *ytPlayerResponse, videoID string, maxHeight int) (string, bool) {
	if player == nil {
		logNativeStreamFailure(videoID, errors.New("empty player answer"))
		return "", false
	}
	status := player.PlayabilityStatus.Status
	if status != "OK" {
		reason := strings.TrimSpace(player.PlayabilityStatus.Reason)
		if reason == "" {
			reason = status
		}
		if ytIsBotCheck(status, reason) {
			// The bot check is about this network, not this video: cool down so
			// every play does not pay a round trip to be refused again.
			ytSessionMu.Lock()
			ytBlockedUntil = time.Now().Add(ytNativeBlockedFor)
			ytSessionMu.Unlock()
		}
		logNativeStreamFailure(videoID, errors.New(reason))
		return "", false
	}
	direct := ytMuxedURL(player.StreamingData.Formats, maxHeight)
	if direct == "" {
		logNativeStreamFailure(videoID, errors.New("no muxed format in the player answer"))
		return "", false
	}
	return direct, true
}

// ytIsBotCheck reports whether a refusal is YouTube asking this client to prove
// it is not a robot (as opposed to the video being private, age-restricted,
// removed, or blocked in this country).
func ytIsBotCheck(status, reason string) bool {
	if status == "LOGIN_REQUIRED" {
		return true
	}
	return strings.Contains(strings.ToLower(reason), "not a bot")
}

// logNativeStreamFailure records why the fast path stood down. The message is
// short on purpose: it precedes the helper's own log line, which explains what
// went right.
func logNativeStreamFailure(videoID string, err error) {
	reason := firstLine(err.Error())
	if len(reason) > 160 {
		reason = reason[:160] + "…"
	}
	logDebug("stream", "native client stood down for %s: %s", videoID, reason)
}

// ytVideoIDPatterns cover the shapes a video link arrives in: a watch page, a
// short link, a Shorts page and an embed.
var ytVideoIDPatterns = sync.OnceValue(func() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`(?i)[?&]v=([A-Za-z0-9_-]{11})`),
		regexp.MustCompile(`(?i)youtu\.be/([A-Za-z0-9_-]{11})`),
		regexp.MustCompile(`(?i)/shorts/([A-Za-z0-9_-]{11})`),
		regexp.MustCompile(`(?i)/embed/([A-Za-z0-9_-]{11})`),
		regexp.MustCompile(`(?i)/live/([A-Za-z0-9_-]{11})`),
	}
})

// youtubeVideoID extracts the 11-character id from a YouTube URL, or "" for
// anything else (a direct media link, a local file, another site).
func youtubeVideoID(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "youtube.com") && !strings.Contains(lower, "youtu.be") {
		return ""
	}
	for _, pattern := range ytVideoIDPatterns() {
		if m := pattern.FindStringSubmatch(text); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// ytNativeAllowed reports whether the fast path may be tried at all.
func ytNativeAllowed() bool {
	if ytNativeOff {
		return false
	}
	ytSessionMu.Lock()
	defer ytSessionMu.Unlock()
	return !time.Now().Before(ytBlockedUntil)
}
