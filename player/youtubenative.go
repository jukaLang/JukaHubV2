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
// client version YouTube has started gating, a client whose stream URLs the
// CDN refuses, or a network where the bot check fires. A refusal is remembered
// for a while so a gated network pays for the attempt once instead of on every
// play.

import (
	"bytes"
	"context"
	"crypto/tls"
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

// ytNativeClient is one Innertube identity the player call can be made as. The
// versions are load-bearing: YouTube bot-gates stale ones with LOGIN_REQUIRED
// ("Sign in to confirm you're not a bot"). Measured on this network,
// ANDROID_VR answered at 1.64 and above and was gated at 1.61-1.63, and
// ANDROID answered at 20.05 and above.
//
// Answering is not the whole story, though. Since 2026-08-17 YouTube's video
// CDN refuses every android_vr format with HTTP 403 even though the player call
// still answers (yt-dlp issue 17456; yt-dlp 2026.08.19 dropped the client), so
// a URL from this path could look fine and then fail the moment the player
// opened it — and the play skipped the helper and went straight to
// downloading the whole file. The table is therefore tried in order, every URL
// is checked against the CDN before it is used (ytNativeStreamAccepted), and a
// client the CDN refuses stands down for hours while the next one, then yt-dlp,
// answers. ANDROID is first because it is the client that still serves the
// muxed 360p format without a PO token or a JavaScript runtime (yt-dlp issues
// 17586 and 18005, Aug-Nov 2026); its version is the one yt-dlp master
// announced in October 2026. When native resolution starts failing while the
// helper still works, bump these strings — nothing else changes.
type ytNativeClient struct {
	name      string // clientName in the request body
	version   string // clientVersion; also in the header and the user agent
	id        string // X-YouTube-Client-Name
	userAgent string
	sdk       int    // androidSdkVersion
	osVersion string // osVersion
}

var ytNativeClients = []ytNativeClient{
	{
		name:      "ANDROID",
		version:   "21.26.364",
		id:        "3",
		userAgent: "com.google.android.youtube/21.26.364 (Linux; U; Android 11) gzip",
		sdk:       30,
		osVersion: "11",
	},
	{
		name:      "ANDROID_VR",
		version:   "1.65.4",
		id:        "28",
		userAgent: "com.google.android.apps.youtube.vr.oculus/1.65.4 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
		sdk:       32,
		osVersion: "12L",
	},
}

// ytSessionRefresh is how long a bootstrapped session (visitor id, cookies, API
// key) is reused before it is fetched again.
const ytSessionRefresh = 30 * time.Minute

// ytNativeBlockedFor is the cooldown after a refusal. A network that fails the
// bot check fails it for every video, so re-asking on each play would only add
// latency to a path that is about to fall back to the helper anyway.
const ytNativeBlockedFor = 10 * time.Minute

// ytNativeCDNBlockedFor is the cooldown for a client whose stream URLs the CDN
// refuses. That refusal is YouTube retiring the client, not a bad minute on
// this network, so the client stays down for most of a session.
const ytNativeCDNBlockedFor = 6 * time.Hour

// ytNativeVerifyTimeout bounds the CDN check of one resolved URL.
const ytNativeVerifyTimeout = 6 * time.Second

var (
	ytSessionMu      sync.Mutex
	ytHTTPClient     *http.Client
	ytVisitor        string
	ytAPIKey         string
	ytWebVersion     string // the web client build the homepage announced
	ytSessionFetched time.Time
	// ytClientBlocked is each client's cooldown, by clientName.
	ytClientBlocked = map[string]time.Time{}
	// ytBootstrapBlockedUntil cools down a homepage fetch that failed: an
	// unreachable or untrusted host must not cost an 850 KB attempt per play.
	ytBootstrapBlockedUntil time.Time
	ytNativeOff             bool // test hook: skip the network entirely

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
		ytHTTPClient = &http.Client{Timeout: 20 * time.Second, Jar: jar, Transport: ytTransport()}
	}
	return ytHTTPClient
}

// ytTransport is the transport the native YouTube clients share. On a firmware
// with an empty certificate store every request here failed with an x509 error
// while the bundled yt-dlp (which carries its own CA list) kept working, which
// made the fast paths silently useless; the bug reporter's fallback bundle
// (bugrepRootCAs: a cacert.pem next to the app or a firmware copy) is used
// then. With a normal store this is the default transport.
func ytTransport() http.RoundTripper {
	pool := bugrepRootCAs()
	if pool == nil {
		return http.DefaultTransport
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	tr := base.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr
}

var (
	ytVisitorPattern    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"visitorData":"([^"]+)"`) })
	ytAPIKeyPattern     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"INNERTUBE_API_KEY":"([^"]+)"`) })
	ytWebVersionPattern = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`"INNERTUBE_CLIENT_VERSION":"(2\.[0-9]{8}\.[0-9]{2}\.[0-9]{2})"`)
	})
)

// ytLiveWebClientVersion returns the web client build the last homepage fetch
// announced, or "" before one has run. The native search announces it instead
// of its built-in string, so the search ages with the site rather than with
// this binary.
func ytLiveWebClientVersion() string {
	ytSessionMu.Lock()
	defer ytSessionMu.Unlock()
	return ytWebVersion
}

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
	if time.Now().Before(ytBootstrapBlockedUntil) {
		ytSessionMu.Unlock()
		return errYtBootstrap
	}
	ytSessionMu.Unlock()

	err := ytFetchSession(ctx)
	if err != nil && ctx.Err() == nil {
		// A cancelled play says nothing about the host; anything else is
		// remembered so the next plays go straight to the helper.
		ytSessionMu.Lock()
		ytBootstrapBlockedUntil = time.Now().Add(ytNativeBlockedFor)
		ytSessionMu.Unlock()
	}
	return err
}

// errYtBootstrap marks a failed (or cooling) session fetch: no client can be
// asked without one, so the native path stands down as a whole.
var errYtBootstrap = errors.New("youtube session bootstrap failed")

// ytFetchSession loads the homepage and keeps what the player call needs.
func ytFetchSession(ctx context.Context) error {
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
		return fmt.Errorf("%w: %w", errYtBootstrap, err)
	}
	defer resp.Body.Close()
	// The page is ~850 KB and the strings are near the top; a bounded read
	// keeps a misbehaving response from becoming a memory spike on the card.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("%w: %w", errYtBootstrap, err)
	}

	visitor := ""
	if m := ytVisitorPattern().FindSubmatch(body); len(m) == 2 {
		visitor = string(m[1])
	}
	key := youtubeWebAPIKey // shared with the native search path
	if m := ytAPIKeyPattern().FindSubmatch(body); len(m) == 2 {
		key = string(m[1])
	}
	webVersion := ""
	if m := ytWebVersionPattern().FindSubmatch(body); len(m) == 2 {
		webVersion = string(m[1])
	}
	if visitor == "" {
		// Without visitor data the player call is answered with the bot check,
		// so this is a real failure rather than something to retry blindly.
		return fmt.Errorf("%w: the page carried no visitor data (HTTP %d)", errYtBootstrap, resp.StatusCode)
	}
	ytSessionMu.Lock()
	ytVisitor, ytAPIKey, ytSessionFetched = visitor, key, time.Now()
	if webVersion != "" {
		ytWebVersion = webVersion
	}
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
	// Captions is the video's subtitle list, kept raw: ytcaptions.go decodes
	// it (ytCaptionTracksOf), so a change in its shape cannot make this
	// answer unreadable for the stream path, which ignores it.
	Captions json.RawMessage `json:"captions"`
}

// ytPlayerHTTPError is a player call the endpoint answered with an error status.
type ytPlayerHTTPError struct {
	status int
}

func (e *ytPlayerHTTPError) Error() string {
	return fmt.Sprintf("player call failed: HTTP %d", e.status)
}

// ytPlayerCall performs one player request for a video id as client.
func ytPlayerCall(ctx context.Context, client ytNativeClient, videoID string) (*ytPlayerResponse, error) {
	if err := ytEnsureSession(ctx); err != nil {
		return nil, err
	}
	ytSessionMu.Lock()
	visitor, key := ytVisitor, ytAPIKey
	ytSessionMu.Unlock()

	body := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName":        client.name,
			"clientVersion":     client.version,
			"androidSdkVersion": client.sdk,
			"osName":            "Android",
			"osVersion":         client.osVersion,
			"hl":                "en",
			"gl":                "US",
			"userAgent":         client.userAgent,
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
	req.Header.Set("User-Agent", client.userAgent)
	req.Header.Set("X-YouTube-Client-Name", client.id)
	req.Header.Set("X-YouTube-Client-Version", client.version)
	req.Header.Set("X-Goog-Visitor-Id", visitor)
	resp, err := ytClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("player call failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &ytPlayerHTTPError{status: resp.StatusCode}
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

// youtubeNativeStream resolves a video id to a direct stream URL, asking each
// client in ytNativeClients that is not cooling down. The second return value
// is false whenever the helper should be asked instead.
func youtubeNativeStream(ctx context.Context, videoID string, maxHeight int) (string, bool) {
	for _, client := range ytNativeClients {
		if ctx.Err() != nil {
			return "", false
		}
		if !ytNativeClientAllowed(client.name, time.Now()) {
			continue
		}
		player, err := ytPlayerCall(ctx, client, videoID)
		if err != nil {
			logNativeStreamFailure(client.name, videoID, err)
			if errors.Is(err, errYtBootstrap) {
				// No session, no client: the helper is the only way left.
				return "", false
			}
			var refused *ytPlayerHTTPError
			if errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 {
				// 400 "Precondition check failed" is how the endpoint turns
				// away a client version it no longer accepts: it says the same
				// for every video, so the client sits out a while.
				ytBlockNativeClient(client.name, ytNativeBlockedFor, err.Error())
			}
			continue
		}
		direct, botCheck, ok := ytStreamFromPlayer(player, client.name, videoID, maxHeight)
		if botCheck {
			// The bot check is about this network and this client, not this
			// video: cool it down so every play does not pay a round trip to
			// be refused again.
			ytBlockNativeClient(client.name, ytNativeBlockedFor, "bot check")
		}
		if !ok {
			if !botCheck && ytVideoRefused(player) {
				// The refusal is about this video — private, removed,
				// age-restricted, blocked in this country — and every client
				// gets the same answer, so asking the next one only added a
				// round trip to every such play. The helper is asked once
				// instead: its error names the reason the message shows.
				return "", false
			}
			continue
		}
		if !ytNativeStreamAcceptedFor(ctx, client.name, direct) {
			continue
		}
		return direct, true
	}
	return "", false
}

// ytStreamFromPlayer is the decision made once the player answer is in, split
// out so it can be tested against recorded answers instead of the network.
// botCheck reports a refusal that is about the client rather than the video.
func ytStreamFromPlayer(player *ytPlayerResponse, clientName, videoID string, maxHeight int) (direct string, botCheck, ok bool) {
	if player == nil {
		logNativeStreamFailure(clientName, videoID, errors.New("empty player answer"))
		return "", false, false
	}
	status := player.PlayabilityStatus.Status
	if status != "OK" {
		reason := strings.TrimSpace(player.PlayabilityStatus.Reason)
		if reason == "" {
			reason = status
		}
		logNativeStreamFailure(clientName, videoID, errors.New(reason))
		return "", ytIsBotCheck(status, reason), false
	}
	direct = ytMuxedURL(player.StreamingData.Formats, maxHeight)
	if direct == "" {
		logNativeStreamFailure(clientName, videoID, errors.New("no muxed format in the player answer"))
		return "", false, false
	}
	return direct, false, true
}

// ytVideoRefused reports whether the player answered with a refusal about the
// video itself rather than about the client: any status other than OK that is
// not the bot check. "No muxed format" is an OK answer and is not a refusal —
// another client may still carry one.
func ytVideoRefused(player *ytPlayerResponse) bool {
	if player == nil {
		return false
	}
	status := player.PlayabilityStatus.Status
	if status == "" || status == "OK" {
		return false
	}
	return !ytIsBotCheck(status, player.PlayabilityStatus.Reason)
}

// ytNativeStreamAcceptedFor is the CDN check; a variable so tests can stand it
// down without reaching YouTube.
var ytNativeStreamAcceptedFor = ytNativeStreamAccepted

// ytNativeStreamAccepted asks the video CDN for the first kilobyte of a URL
// clientName resolved. A refusal (401, 403, 410) means YouTube has retired the
// client for streaming even though its player call still answers — the August
// 2026 android_vr failure — so the client stands down for
// ytNativeCDNBlockedFor and the next one is asked.
//
// The deciding request is dressed as the player: the browser identity that
// ffprobe and probePlayableStream send (the decoder sends ffmpeg's own, which
// has played every URL this identity was accepted for so far). A check sent as
// the client app could vouch for a URL the player is then refused. Whether the
// CDN ties a client's URLs to that app's own user agent is not known for
// ANDROID, so a refusal is asked once more as the client, purely to say which
// it is in the log: if that passes, the stream needs a header the player does
// not send yet, and the fix is in the player (ffprobe, decoder and the https
// relay would send the client's user agent), not in this table.
//
// A URL that passes is not marked reachable: probePlayableStream still runs
// its ffprobe, which is what fills the probe cache a preloaded card starts
// from instantly.
func ytNativeStreamAccepted(ctx context.Context, clientName, direct string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, ytNativeVerifyTimeout)
	defer cancel()
	status, mediaType, err := ytFirstBytes(checkCtx, direct, streamUserAgent)
	if err != nil {
		if ctx.Err() == nil {
			logDebug("stream", "native %s stream check failed: %v", clientName, err)
		}
		return false
	}
	if (status == http.StatusOK || status == http.StatusPartialContent) && mediaType {
		return true
	}
	if status == http.StatusForbidden || status == http.StatusUnauthorized || status == http.StatusGone {
		why := fmt.Sprintf("the video CDN answered HTTP %d", status)
		if ua := ytNativeClientUserAgent(clientName); ua != "" {
			appStatus, appMedia, appErr := ytFirstBytes(checkCtx, direct, ua)
			if appErr == nil && (appStatus == http.StatusOK || appStatus == http.StatusPartialContent) && appMedia {
				why += fmt.Sprintf(" to the player's identity but HTTP %d to the %s app's own user agent — "+
					"the player would need to send that header to stream this client", appStatus, clientName)
			}
		}
		ytBlockNativeClient(clientName, ytNativeCDNBlockedFor, why)
		return false
	}
	logDebug("stream", "native %s stream check: HTTP %d (media type %v)", clientName, status, mediaType)
	return false
}

// ytNativeClientUserAgent returns the user agent a native client announces, or
// "" for a name the table does not know.
func ytNativeClientUserAgent(clientName string) string {
	for _, c := range ytNativeClients {
		if c.name == clientName {
			return c.userAgent
		}
	}
	return ""
}

// ytFirstBytes fetches the first kilobyte of a stream URL with the given user
// agent and reports the status and whether a media type came back.
func ytFirstBytes(ctx context.Context, direct, userAgent string) (int, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, direct, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := ytClient().Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	ctype := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	media := strings.HasPrefix(ctype, "video/") || strings.HasPrefix(ctype, "audio/") ||
		strings.HasPrefix(ctype, "application/mp4") || strings.Contains(ctype, "octet-stream")
	return resp.StatusCode, media, nil
}

// ytNativeCooldownKey includes the client version so a new version does not
// inherit a refusal earned by the retired one.
func ytNativeCooldownKey(clientName string) string {
	for _, client := range ytNativeClients {
		if client.name == clientName {
			return "ytnative:" + client.name + "@" + client.version
		}
	}
	return ""
}

// ytLoadNativeCooldown restores one unexpired client cooldown from the shared
// cache. Cache failures leave the fast path usable; they only lose persistence.
func ytLoadNativeCooldown(clientName string, now time.Time) time.Time {
	key := ytNativeCooldownKey(clientName)
	if key == "" {
		return time.Time{}
	}
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		logDebug("stream", "native YouTube cooldown cache unavailable: %v", err)
		return time.Time{}
	}
	data, err := cacheGet(db, key)
	if err != nil || len(data) == 0 {
		if err != nil {
			logDebug("stream", "native YouTube cooldown read failed: %v", err)
		}
		return time.Time{}
	}
	until, err := time.Parse(time.RFC3339Nano, string(data))
	if err != nil || !until.After(now) {
		return time.Time{}
	}
	return until
}

// ytSaveNativeCooldown persists only the remaining lifetime of the refusal.
func ytSaveNativeCooldown(clientName string, until time.Time) {
	key := ytNativeCooldownKey(clientName)
	remaining := time.Until(until)
	if key == "" || remaining <= 0 {
		return
	}
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		logDebug("stream", "native YouTube cooldown cache unavailable: %v", err)
		return
	}
	if err := cacheSet(db, key, []byte(until.UTC().Format(time.RFC3339Nano)), remaining); err != nil {
		logDebug("stream", "native YouTube cooldown save failed: %v", err)
	}
}

// ytBlockNativeClient cools one client down and says so once in the log.
func ytBlockNativeClient(clientName string, d time.Duration, why string) {
	until := time.Now().Add(d)
	ytSessionMu.Lock()
	already := ytClientBlocked[clientName].After(until.Add(-time.Minute))
	if !already {
		ytClientBlocked[clientName] = until
	}
	storedUntil := ytClientBlocked[clientName]
	ytSessionMu.Unlock()
	ytSaveNativeCooldown(clientName, storedUntil)
	if !already {
		logWarn("stream", "native YouTube client %s stands down for %v: %s; the next client or yt-dlp answers instead",
			clientName, d, why)
	}
}

// ytNativeClientAllowed reports whether a client is out of its cooldown.
func ytNativeClientAllowed(clientName string, now time.Time) bool {
	ytSessionMu.Lock()
	until, loaded := ytClientBlocked[clientName]
	ytSessionMu.Unlock()
	if !loaded {
		until = ytLoadNativeCooldown(clientName, now)
		ytSessionMu.Lock()
		if current, ok := ytClientBlocked[clientName]; ok && current.After(until) {
			until = current
		}
		ytClientBlocked[clientName] = until
		ytSessionMu.Unlock()
	}
	return !now.Before(until)
}

// ytIsBotCheck reports whether a refusal is YouTube asking this client to prove
// it is not a robot (as opposed to the video being private, age-restricted,
// removed, or blocked in this country).
func ytIsBotCheck(status, reason string) bool {
	lower := strings.ToLower(strings.ReplaceAll(reason, "’", "'"))
	// "Sign in to confirm your age" arrives as LOGIN_REQUIRED as well, but it
	// is about the video: read as the bot check, one age-restricted video
	// benched a working client for ten minutes.
	// A private video comes back as LOGIN_REQUIRED too, worded "This video is
	// private" by this endpoint ("Private video" is yt-dlp's wording).
	if ytContainsAny(lower, "confirm your age", "age-restricted", "age restricted",
		"inappropriate for some users", "private video", "video is private") {
		return false
	}
	if status == "LOGIN_REQUIRED" {
		return true
	}
	return strings.Contains(lower, "not a bot")
}

// logNativeStreamFailure records why the fast path stood down. The message is
// short on purpose: it precedes the helper's own log line, which explains what
// went right.
func logNativeStreamFailure(clientName, videoID string, err error) {
	reason := firstLine(err.Error())
	if len(reason) > 160 {
		reason = reason[:160] + "…"
	}
	logDebug("stream", "native client %s stood down for %s: %s", clientName, videoID, reason)
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

// ytNativeAllowed reports whether the fast path may be tried at all: the
// session fetch is not cooling down and at least one client is not either.
func ytNativeAllowed() bool {
	if ytNativeOff {
		return false
	}
	now := time.Now()
	ytSessionMu.Lock()
	bootstrapBlocked := now.Before(ytBootstrapBlockedUntil)
	ytSessionMu.Unlock()
	if bootstrapBlocked {
		return false
	}
	for _, client := range ytNativeClients {
		if ytNativeClientAllowed(client.name, now) {
			return true
		}
	}
	return false
}
