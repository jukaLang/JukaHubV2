package main

// videorecover.go — what the player does when a stream will not play.
//
// The decoder used to treat every closed pipe the same way: frames or not, the
// session ended and the player closed. A stream ffmpeg could not open — a CDN
// that refused the request, a link the firmware's ffmpeg cannot read — showed
// the dark loading picture for about a second, and then the player was simply
// gone: no message, nothing in the log but ffmpeg's own line in errors.txt.
// A user reported exactly that on 2026-11-09: "YouTube videos open a black
// canvas for a second, but then crash."
//
// The decoder now says how it stopped (mediaStreamFailure), and this file
// decides what comes next:
//
//   - a web video whose resolved stream would not open, or broke off well short
//     of its end, is downloaded with yt-dlp and played from the card, from
//     where it stopped. That is the fallback a stream that fails its probe has
//     always taken, and it plays wherever ffmpeg can read a file;
//   - anything else (a file on the card, a finished download, a direct link, a
//     live channel) says why it could not play instead of vanishing.
//
// Streaming is skipped for a while where it has just failed — for that video,
// and for every web video once two streams in a row would not open — so the
// next play goes straight to the download instead of paying for the failed
// attempt first.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// mediaStreamFailure is a decoder that stopped without playing its source: it
// never delivered a frame, or it broke off well before the end.
type mediaStreamFailure struct {
	source     string  // what the decoder was given
	pageURL    string  // the session's page (the watch URL of a YouTube video)
	tempFile   bool    // the session played a download already
	local      bool    // the session played a file on the card
	startSec   float64 // where the decoder started
	position   float64 // how far it got: startSec plus the frames it delivered
	duration   float64 // the probed length; 0 when unknown (a live stream)
	frames     int     // frames delivered; 0 means the stream never opened
	startErr   bool    // ffmpeg itself could not be started
	unplayable bool    // nothing a download changes (a picture too large to decode)
	reason     string  // ffmpeg's own words, cleaned (ffmpegFailureReason)
	origin     string  // the scene the video was started from (mediaPlayOrigin)
	floating   bool    // the player was the mini window over another screen
}

// ffmpegStderrSink copies a decoder's stderr to JukaHub's own (errors.txt on
// the handheld) and keeps its last lines. It never fails a write: an error
// from a console that went away must not end the copy, or ffmpeg would block,
// or die, on its next line.
type ffmpegStderrSink struct {
	tail *lockedTailBuffer
}

func (s ffmpegStderrSink) Write(p []byte) (int, error) {
	if s.tail != nil {
		_, _ = s.tail.Write(p)
	}
	_, _ = os.Stderr.Write(p)
	return len(p), nil
}

// resumeAt is where a download that takes over should start: where the user
// asked to start, or a little before the point a stream broke off.
func (f mediaStreamFailure) resumeAt() float64 {
	at := f.startSec
	if f.frames > 0 && f.position > at {
		// A few seconds back, so the picture picks up the moment it lost.
		at = f.position - 3
		if at < f.startSec {
			at = f.startSec
		}
	}
	if at < 0 {
		at = 0
	}
	return at
}

// mediaPageStream reports whether source is a stream resolved from a web page:
// an http(s) media URL that yt-dlp or the native client handed over for a page
// that is not itself a media link. Such a page can be downloaded instead, and
// its URL was issued for the identity the extractor announced.
func mediaPageStream(source, pageURL string) bool {
	source, pageURL = strings.TrimSpace(source), strings.TrimSpace(pageURL)
	if source == "" || pageURL == "" || source == pageURL {
		return false
	}
	return isHTTPMediaURL(source) && isHTTPMediaURL(pageURL) && !isDirectStreamURL(pageURL)
}

// mediaIsHLS reports whether a stream URL is an HLS playlist: what a live
// video resolves to (YouTube's "hls_playlist" manifests included). A live
// stream has no file behind it, so it is never sent to the download.
func mediaIsHLS(u string) bool {
	lower := strings.ToLower(strings.TrimSpace(u))
	if cut := strings.IndexAny(lower, "?#"); cut >= 0 {
		lower = lower[:cut]
	}
	return strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".m3u") ||
		strings.Contains(lower, "/hls_playlist/") || strings.Contains(lower, "/hls_variant/")
}

// ffmpegStreamInputArgs are the input options a decoder reading source needs.
// A stream resolved from a page is asked for as the browser the extractor said
// it was: yt-dlp (--user-agent) and the native client asked for the signed URL
// as streamUserAgent, and ffprobe and the CDN check opened it as that, but the
// two decoders announced ffmpeg's own "Lavf/…" — the one request in the chain
// the CDN had not seen from that client, and the one a CDN that refuses
// ffmpeg's name answers with 403. Such a stream also reconnects where it broke
// off (a Range request, which the https relay passes through): a connection
// the CDN or the relay dropped during a pause used to end the video there.
// All of these are options of ffmpeg's http protocol, which every one of these
// inputs is opened with (the relay's loopback address included); a direct link
// keeps ffmpeg's defaults, and an rtsp/udp input (whose protocol would not take
// them, so ffmpeg would refuse to start) never gets them.
func ffmpegStreamInputArgs(source, pageURL string) []string {
	if mediaPageStream(source, pageURL) {
		return []string{"-user_agent", streamUserAgent, "-reconnect", "1", "-reconnect_delay_max", "4"}
	}
	return nil
}

// ffmpegFailureReason picks the line of a decoder's stderr that says why it
// stopped, without the log tag ("[https @ 0x…]"), the input address in front
// of it, or the "Error opening input files:" wrapper ffmpeg 6.1 puts around it.
// A line that still names an address after that is skipped: a signed media URL
// belongs neither on screen nor in the log.
func ffmpegFailureReason(stderr string) string {
	reason := ""
	lines := strings.FieldsFunc(stderr, func(r rune) bool { return r == '\n' || r == '\r' })
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			if end := strings.Index(line, "] "); end > 0 {
				line = strings.TrimSpace(line[end+2:])
			}
		}
		// "https://host/videoplayback?…: Server returned 403 Forbidden (access denied)"
		if i := strings.Index(line, ": "); i > 0 && strings.Contains(line[:i], "://") {
			line = strings.TrimSpace(line[i+2:])
		}
		for _, wrapper := range []string{"Error opening input files: ", "Error opening input: ", "Error during demuxing: "} {
			line = strings.TrimPrefix(line, wrapper)
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "://") {
			continue
		}
		reason = line
	}
	return truncateRunesSuffix(redactDiagnosticText(reason), 120, "…")
}

// ffmpegStartReason is why ffmpeg could not be started, without the tool's
// path in front of it ("fork/exec /mnt/SDCARD/…/ffmpeg: exec format error").
func ffmpegStartReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return strings.TrimSpace(redactDiagnosticText(msg))
}

// ffmpegStreamBroke reports whether a decoder's stderr shows its input breaking
// off — a refusal, a reset, a timeout, a truncated file — rather than the
// decoding hiccups a damaged frame leaves, which ffmpeg also prints at the
// error level and plays on through.
func ffmpegStreamBroke(stderr string) bool {
	return ytContainsAny(strings.ToLower(stderr),
		"http error", "server returned", "partial file", "i/o error", "input/output error",
		"connection reset", "connection refused", "connection timed out", "timed out",
		"stream ends prematurely", "error in the pull function", "network is unreachable")
}

// mediaDecoderFailed decides whether a decoder that closed its pipe failed
// rather than reached the end. frames is how many it delivered, position how
// far that got and duration the probed length (0 when unknown).
//
// No frame at all is a failure, unless the decoder started at the very end (a
// seek to the last second). After frames, only a stream resolved from a page
// counts, and only when ffmpeg said the input broke off with a good part of the
// video still to come: a download can carry such a stream on, and anything
// else keeps the end-of-video path it always had.
func mediaDecoderFailed(startSec, position, duration float64, frames int, pageStream bool, stderr string) bool {
	if frames <= 0 {
		return !(duration > 0 && startSec >= duration-2)
	}
	if !pageStream || duration <= 0 {
		return false
	}
	left := duration - position
	return left > 30 && position < duration*0.9 && ffmpegStreamBroke(stderr)
}

// mediaReasonForPeople says a decoder's reason in plain words for the toast,
// or passes ffmpeg's own line through when it is not one of the usual ones.
func mediaReasonForPeople(reason string) string {
	lower := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case lower == "":
		return ""
	case ytContainsAny(lower, "does not contain any stream", "matches no streams"):
		return "it has no picture"
	case ytContainsAny(lower, "403 forbidden", "http error 403"):
		return "the server refused it (HTTP 403)"
	case ytContainsAny(lower, "404 not found", "http error 404"):
		return "it is not there any more (HTTP 404)"
	case strings.Contains(lower, "protocol not found"):
		return "this ffmpeg cannot open that kind of link"
	case ffmpegTLSVerifyFailed(lower):
		return "ffmpeg could not check the site's certificate"
	case ytContainsAny(lower, "connection refused", "network is unreachable", "no route to host",
		"failed to resolve hostname", "name or service not known", "temporary failure in name resolution"):
		return "the network did not answer"
	case ytContainsAny(lower, "timed out", "timeout"):
		return "the connection timed out"
	case strings.Contains(lower, "invalid data found when processing input"):
		return "ffmpeg cannot read this format"
	case strings.Contains(lower, "decoder") && strings.Contains(lower, "not found"):
		return "this ffmpeg has no decoder for it"
	}
	return strings.TrimSpace(reason)
}

// mediaFailureToast is the line a failure that nothing takes over shows.
func mediaFailureToast(f mediaStreamFailure) string {
	why := mediaReasonForPeople(f.reason)
	msg := "Couldn't play this stream"
	switch {
	case f.startErr:
		msg = "The video player could not start ffmpeg"
	case f.frames > 0:
		msg = "Playback stopped"
	case f.local || f.tempFile || f.unplayable:
		msg = "Couldn't play this video"
	}
	if why == "" {
		return msg
	}
	return msg + " — " + why
}

// mediaFailureLogLine describes a failure for errors.txt, with the page (not
// the signed media URL) and ffmpeg's reason.
func mediaFailureLogLine(f mediaStreamFailure) string {
	what := strings.TrimSpace(f.pageURL)
	if what == "" {
		what = f.source
	}
	where := "before its first frame"
	if f.startErr {
		where = "before it started"
	} else if f.frames > 0 {
		where = fmt.Sprintf("after %d frames (%.0f s in)", f.frames, f.position)
	}
	reason := f.reason
	if reason == "" {
		reason = "ffmpeg gave no reason"
	}
	return fmt.Sprintf("decoder for %s stopped %s: %s", sanitizeLogURL(what), where, reason)
}

// ── Where streaming has just failed ─────────────────────────────────────

const (
	// mediaSkipStreamFor is how long a page whose stream never opened is
	// downloaded instead, and how long every YouTube video is once YouTube
	// streams keep failing.
	mediaSkipStreamFor = 30 * time.Minute
	// mediaSkipAllAfter is how many YouTube streams in a row that never opened
	// make every YouTube video download first: past that it is the device or
	// the network (the firmware's ffmpeg, the CDN refusing this client), not a
	// video. Other sites are not counted: a dead channel or a one-connection
	// stream there says nothing about YouTube.
	mediaSkipAllAfter = 2
	// mediaRecoverCooldown keeps one page from falling back to a download more
	// than once in a short while, whatever loop a strange link could make.
	mediaRecoverCooldown = 2 * time.Minute
	// mediaHealthMaxPages bounds the per-page tables.
	mediaHealthMaxPages = 64
)

var mediaStreamHealth = struct {
	mu         sync.Mutex
	failures   int                  // YouTube streams in a row that never opened
	lastFailed string               // the page of the last of them
	skipAll    time.Time            // until then every YouTube video downloads first
	skipPage   map[string]time.Time // per page: until then it downloads first
	recovered  map[string]time.Time // per page: when its last fallback began
}{skipPage: map[string]time.Time{}, recovered: map[string]time.Time{}}

// mediaStreamHealthReset forgets everything above (tests).
func mediaStreamHealthReset() {
	mediaStreamHealth.mu.Lock()
	mediaStreamHealth.failures = 0
	mediaStreamHealth.lastFailed = ""
	mediaStreamHealth.skipAll = time.Time{}
	mediaStreamHealth.skipPage = map[string]time.Time{}
	mediaStreamHealth.recovered = map[string]time.Time{}
	mediaStreamHealth.mu.Unlock()
}

// mediaSkippablePage reports whether page is one the skip applies to: a web
// page, not a file on the card and not a direct media link (those have no
// download to go to).
func mediaSkippablePage(page string) bool {
	return page != "" && isHTTPMediaURL(page) && !isDirectStreamURL(page)
}

// mediaStreamSkipped reports whether page should be downloaded rather than
// streamed, because streaming it — or, for a YouTube video, streaming YouTube
// here — failed a moment ago.
func mediaStreamSkipped(page string, now time.Time) bool {
	page = strings.TrimSpace(page)
	if !mediaSkippablePage(page) {
		return false
	}
	mediaStreamHealth.mu.Lock()
	defer mediaStreamHealth.mu.Unlock()
	if now.Before(mediaStreamHealth.skipAll) && youtubeVideoID(page) != "" {
		return true
	}
	until, ok := mediaStreamHealth.skipPage[page]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(mediaStreamHealth.skipPage, page)
		return false
	}
	return true
}

// mediaNoteStreamFailed records a resolved stream of page that never opened.
// The page downloads first for a while; a YouTube video also counts toward
// downloading every YouTube video, and the return value says whether this
// failure is the one that just turned that on. (A stream that played and then
// broke off is not recorded: it opened, and the next play may well stream.)
func mediaNoteStreamFailed(page string, now time.Time) bool {
	page = strings.TrimSpace(page)
	if !mediaSkippablePage(page) {
		return false
	}
	mediaStreamHealth.mu.Lock()
	defer mediaStreamHealth.mu.Unlock()
	if len(mediaStreamHealth.skipPage) >= mediaHealthMaxPages {
		mediaStreamHealth.skipPage = map[string]time.Time{}
	}
	mediaStreamHealth.skipPage[page] = now.Add(mediaSkipStreamFor)
	// The same video again (a retry, a seek, a quality switch) is one bad
	// video, not a second sign that YouTube will not stream here.
	if youtubeVideoID(page) == "" || page == mediaStreamHealth.lastFailed {
		return false
	}
	mediaStreamHealth.lastFailed = page
	mediaStreamHealth.failures++
	if mediaStreamHealth.failures < mediaSkipAllAfter {
		return false
	}
	mediaStreamHealth.failures = 0
	wasOn := now.Before(mediaStreamHealth.skipAll)
	mediaStreamHealth.skipAll = now.Add(mediaSkipStreamFor)
	return !wasOn
}

// mediaNoteStreamOpened records a resolved stream of page that delivered its
// first frame: for a YouTube video, YouTube streams here, so the run of
// failures starts again.
func mediaNoteStreamOpened(page string) {
	if youtubeVideoID(page) == "" {
		return
	}
	mediaStreamHealth.mu.Lock()
	mediaStreamHealth.failures = 0
	mediaStreamHealth.lastFailed = ""
	mediaStreamHealth.mu.Unlock()
}

// mediaNoteDownloadFailed records that page's download failed as well. The
// skip above sends a page (or every YouTube video) to the download because
// the stream just failed; when the download fails too it is no better than
// the stream, and keeping the skip would only send every next play into the
// same failing download. The next play streams first again.
func mediaNoteDownloadFailed(page string) {
	page = strings.TrimSpace(page)
	mediaStreamHealth.mu.Lock()
	delete(mediaStreamHealth.skipPage, page)
	if youtubeVideoID(page) != "" {
		mediaStreamHealth.skipAll = time.Time{}
		mediaStreamHealth.failures = 0
		mediaStreamHealth.lastFailed = ""
	}
	mediaStreamHealth.mu.Unlock()
}

// mediaClaimRecovery reports whether page may fall back to a download now,
// and records that it did.
func mediaClaimRecovery(page string, now time.Time) bool {
	page = strings.TrimSpace(page)
	mediaStreamHealth.mu.Lock()
	defer mediaStreamHealth.mu.Unlock()
	if at, ok := mediaStreamHealth.recovered[page]; ok && now.Sub(at) < mediaRecoverCooldown {
		return false
	}
	if len(mediaStreamHealth.recovered) >= mediaHealthMaxPages {
		mediaStreamHealth.recovered = map[string]time.Time{}
	}
	mediaStreamHealth.recovered[page] = now
	return true
}

// ── Taking over ─────────────────────────────────────────────────────────

// mediaRecoverStream runs on the decoder's goroutine after a failed session
// was closed (finish). A web video falls back to the download; anything else,
// or a web video that already fell back a moment ago, says why it stopped.
func mediaRecoverStream(config *Config, f *mediaStreamFailure) {
	if f == nil {
		return
	}
	logWarn("video", "%s", mediaFailureLogLine(*f))
	now := time.Now()
	page := strings.TrimSpace(f.pageURL)
	// A live stream has no file to download: a playlist with no length, or
	// any other site's stream with no length (an IPTV channel behind an
	// address without an extension resolves to its .ts that way). A YouTube
	// video with no length is a file whose probe failed; YouTube's live
	// streams are playlists.
	live := f.duration <= 0 && (mediaIsHLS(f.source) || youtubeVideoID(page) == "")
	webVideo := !f.startErr && !f.unplayable && !f.tempFile && !f.local && !live && mediaPageStream(f.source, page)
	skipAllStarted := false
	if webVideo {
		// The signed URL just failed; a replay resolves a fresh one.
		forgetDirectStream(page)
		if f.frames == 0 {
			skipAllStarted = mediaNoteStreamFailed(page, now)
			if skipAllStarted {
				logWarn("video", "two YouTube streams in a row would not open; YouTube videos download before they play for the next %v", mediaSkipStreamFor)
			}
		}
	}
	// The user moved on while the decoder was failing: another video is
	// loading or playing, and it owns the player and the screen now.
	if playbackCurrentSession() != nil || videoPrepareActive() {
		return
	}
	// A stream that cut out while it floated over another screen is not
	// fetched behind the user's back: the loading card would take that screen.
	// The resume point was saved as the session closed, so Play carries on.
	if f.frames > 0 && f.floating {
		showToast("The stream cut out — play it again to carry on", ToastWarn())
		return
	}
	if config == nil || !webVideo || !mediaClaimRecovery(page, now) {
		showToast(mediaFailureToast(*f), ToastError())
		return
	}
	// If the download fails too, its message reads this as well
	// (youTubePlaybackFailureMessage): yt-dlp's own stderr often says less
	// than the refusal the stream met.
	note := "the player could not open the stream"
	if f.frames > 0 {
		note = "the stream broke off"
	}
	if f.reason != "" {
		note += ": " + f.reason
	}
	noteStreamResolveFailure(page, note)
	msg := "Streaming didn't work — downloading the video instead"
	switch {
	case skipAllStarted:
		msg = "YouTube won't stream here right now — videos download before they play for a while"
	case f.frames > 0:
		msg = "The stream cut out — downloading the video to carry on"
	}
	showToast(msg, ToastInfo())
	// Closing the failed session forgot where it was started from; the
	// download is the same video, so Back from it does what Back from the
	// stream would have.
	mediaRestorePlayOrigin(f.origin)
	// The URL that failed is the native download's last resort when yt-dlp
	// finds no way (ytdownload.go): ffmpeg refused it, a ranged fetch may not.
	ytdlRememberFailedStream(page, f.source)
	mediaDownloadInstead(config, page, f.resumeAt())
}

// mediaPlayOriginName is the scene the video on screen was started from.
func mediaPlayOriginName() string {
	mediaPlayOrigin.mu.Lock()
	defer mediaPlayOrigin.mu.Unlock()
	return mediaPlayOrigin.scene
}

// mediaRestorePlayOrigin puts back the scene a video was started from.
func mediaRestorePlayOrigin(scene string) {
	mediaPlayOrigin.mu.Lock()
	mediaPlayOrigin.scene = scene
	mediaPlayOrigin.mu.Unlock()
}

// mediaDownloadInstead plays page from a download, starting at resume. It is a
// variable so tests can stand it in, and it is set in init rather than by an
// initializer: the download starts a session whose decoder can end up here
// again (playWithTempFile → startEmbeddedPlayback → mediaRecoverStream), and
// as an initializer that is an initialization cycle the compiler refuses.
var mediaDownloadInstead func(config *Config, page string, resume float64)

func init() { mediaDownloadInstead = mediaDownloadFromCard }

// mediaDownloadFromCard is the load a stream that fails its probe already ends
// on (playWithTempFile), with the same loading card and B to cancel.
func mediaDownloadFromCard(config *Config, page string, resume float64) {
	prepareSeq, prepareCtx := beginVideoPrepare(recentVideoTitle(page))
	// StopEmbeddedPlayback cleared it; the download is still this page's
	// video, and its session reads the page from here (startEmbeddedPlayback).
	setPlaybackPageURL(page)
	go func() {
		ytDlpPath, missing, err := resolvePlaybackTools(prepareCtx, config)
		if err != nil {
			finishVideoPrepare(prepareSeq)
			if prepareCtx.Err() == nil {
				showToast(toolUnavailableMessage(missing, err), ToastError())
			}
			return
		}
		if prepareCtx.Err() != nil {
			finishVideoPrepare(prepareSeq)
			return
		}
		playWithTempFile(prepareCtx, config, ytDlpPath, page, resume, prepareSeq)
	}()
}

// recentVideoTitle is the title the recently-played list keeps for page, read
// under its own lock: the decoder's goroutine must not walk the playback queue
// the main loop rewrites.
func recentVideoTitle(page string) string {
	page = strings.TrimSpace(page)
	if page == "" {
		return ""
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for _, it := range favoritesStore.Recent {
		if v, ok := it.Data.(VideoInfo); ok && strings.TrimSpace(v.GetURL()) == page {
			return strings.TrimSpace(v.Title)
		}
	}
	return ""
}
