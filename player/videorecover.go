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
	"sync/atomic"
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
	_, _ = os.Stderr.WriteString(redactLogSecrets(string(p))) // an error names its URL: no ?token= on the card (SEC-2)
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
//
// Every http(s) input — a direct link and a live channel too — also gets a read
// timeout (ffmpegReadTimeout) and the reconnect. Without the timeout a stream
// that went quiet without closing (a CDN that stopped sending, Wi-Fi that
// dropped behind a NAT that never sent a reset) left ffmpeg waiting in recv for
// good: a frozen picture, or the loading picture, that nothing recovered from.
// With it, the socket read gives up after that long without a byte; on a file
// whose length the server announced, -reconnect then asks again from where it
// broke off (backing off 0, 1 and 3 s, then giving up past
// -reconnect_delay_max), so a short drop plays on, and a stall that outlasts
// all of that ends the decoder with ffmpeg's I/O error, which
// mediaDecoderStalled turns into a failure for mediaRecoverStream. A live
// stream (no length) is not reconnected — ffmpeg would restart it from its live
// edge — it just ends there. The user agent stays for page streams: a direct
// link keeps ffmpeg's own name, as before.
//
// All of these are options of ffmpeg's http protocol (-rw_timeout of every
// protocol), which every one of these inputs is opened with (the relay's
// loopback address included; the HLS demuxer hands -rw_timeout on to each
// segment it opens); an rtsp/udp input (whose protocol would not take them, so
// ffmpeg would refuse to start) never gets them.
func ffmpegStreamInputArgs(source, pageURL string) []string {
	if !isHTTPMediaURL(source) {
		return nil
	}
	args := make([]string, 0, 8)
	if mediaPageStream(source, pageURL) {
		args = append(args, "-user_agent", streamUserAgent)
	}
	return append(args, "-rw_timeout", ffmpegReadTimeoutArg(), "-reconnect", "1", "-reconnect_delay_max", "4")
}

// ffmpegReadTimeout is how long a decoder (and ffprobe) reading an http(s)
// input waits for the next byte before it gives the connection up. 15 s, the
// bound the probe already had: well past what handheld Wi-Fi goes quiet for
// while it is healthy — a background scan or a roam pauses traffic for 2-8 s,
// a weak link retransmits for a few seconds, a CDN that throttles to playback
// speed still sends something every second or two — and short enough that a
// dead stream says so instead of freezing. It counts time without a single
// byte, not time to fill a buffer, so a slow link that still trickles never
// trips it; and nothing reads while the video is paused (ffmpeg is blocked
// writing its pipe), so a long pause does not either.
const ffmpegReadTimeout = 15 * time.Second

// ffmpegReadTimeoutArg is ffmpegReadTimeout as -rw_timeout takes it
// (microseconds).
func ffmpegReadTimeoutArg() string {
	return fmt.Sprint(ffmpegReadTimeout.Microseconds())
}

// mediaStallWait is how long the picture decoder's last read must have waited
// for a pipe that then closed for the close to count as a stall: the read
// timeout, less the frames ffmpeg still had queued when its input went quiet.
// A file that simply ends delivers its last frames at once; ffmpeg closing the
// pipe after this long without a frame gave up on its input.
const mediaStallWait = ffmpegReadTimeout - 3*time.Second

// mediaDecoderStalled decides whether a picture decoder of an http(s) input
// that had delivered frames stopped because its input went quiet (the read
// timeout above, then the reconnects, ran out) rather than at its end, when
// mediaDecoderFailed did not already count it. lastWait is how long the read
// that found the pipe closed had waited.
//
// A file (a known length) that stalled anywhere but at its end failed; a web
// video stalled in its last half minute still ends there, as one that broke
// off there always has (the download that takes it over is not worth it). A
// live stream (no length) can also end by itself after a wait — a playlist
// that closes when the broadcast stops — so it needs ffmpeg to say its input
// broke off as well, unless watchdog is set: mediaReadWatch stopped the
// decoder, whose read had hung, so it did not end by itself.
func mediaDecoderStalled(frames int, httpInput, pageStream bool, position, duration float64, lastWait time.Duration, stderr string, watchdog bool) bool {
	if frames <= 0 || !httpInput {
		return false // no frame at all is mediaDecoderFailed's
	}
	stalled := watchdog || lastWait >= mediaStallWait || strings.Contains(strings.ToLower(stderr), "timed out")
	if !stalled {
		return false
	}
	if duration <= 0 {
		return watchdog || (lastWait >= mediaStallWait && ffmpegStreamBroke(stderr))
	}
	if pageStream {
		return duration-position > 30 && position < duration*0.9
	}
	return position < duration-2
}

// mediaStallReason is a stalled decoder's reason: ffmpeg's own when it said
// the read timed out, else what happened in words mediaReasonForPeople reads
// as a time-out ("the connection timed out").
func mediaStallReason(stderr string) string {
	if reason := ffmpegFailureReason(stderr); strings.Contains(strings.ToLower(reason), "timed out") {
		return reason
	}
	return fmt.Sprintf("no data for %d s, the read timed out", int(ffmpegReadTimeout/time.Second))
}

// mediaStallGiveUp is how long one read of the picture decoder of an http(s)
// input may wait — the player not paused, and no sound arriving either —
// before mediaReadWatch stops the decoder as stalled. HLS hides a dead network
// from ffmpeg's own timeouts: a live playlist's failed reloads and segment
// opens are only warnings, so ffmpeg neither ends nor says why; a VOD playlist
// skips each segment that times out and waits on the next, for minutes; and
// -reconnect never reaches the segments. 90 s is past the about 80 s the read
// timeout and the reconnects of a progressive stream can take, so those still
// end by themselves, with ffmpeg's own reason.
const mediaStallGiveUp = 90 * time.Second

// mediaReadWatchTick is how often the watch looks.
const mediaReadWatchTick = 5 * time.Second

// mediaReadWatch stops a picture decoder whose pipe read has hung. The decoder
// brackets each read with begin and end and stops the watch when its loop is
// over. A read still pending while the player is paused, while the sound
// decoder still feeds the device (the input is alive, only the picture is
// quiet), or across a sleep of the PC or handheld, counts from then on. A nil
// watch does nothing.
type mediaReadWatch struct {
	base time.Time
	// pending is the pending read's start, as nanoseconds since base plus
	// one; 0 when no read is pending.
	pending atomic.Int64
	fired   atomic.Bool
	stop    chan struct{}
	stopped bool // stop is closed; the decoder's goroutine only
}

// startMediaReadWatch starts the watch of one decoder: done is its session's
// end, av its clock (whose sound shows the input alive), paused reports the
// player paused and kill stops the decoder.
func startMediaReadWatch(done <-chan struct{}, av *avSync, paused func() bool, kill func()) *mediaReadWatch {
	w := &mediaReadWatch{base: time.Now(), stop: make(chan struct{})}
	go w.run(done, av, paused, kill)
	return w
}

func (w *mediaReadWatch) now() int64 { return int64(time.Since(w.base)) + 1 }

// begin notes a read starting. Decoder goroutine.
func (w *mediaReadWatch) begin() {
	if w != nil {
		w.pending.Store(w.now())
	}
}

// end notes the read over. Decoder goroutine.
func (w *mediaReadWatch) end() {
	if w != nil {
		w.pending.Store(0)
	}
}

// Stop ends the watch; more calls do nothing. Decoder goroutine.
func (w *mediaReadWatch) Stop() {
	if w != nil && !w.stopped {
		w.stopped = true
		close(w.stop)
	}
}

// Fired reports whether the watch stopped the decoder.
func (w *mediaReadWatch) Fired() bool {
	return w != nil && w.fired.Load()
}

// run is the watch's goroutine. It ends with the session, with Stop, or once
// it has stopped the decoder; its ticker goes with it.
func (w *mediaReadWatch) run(done <-chan struct{}, av *avSync, paused func() bool, kill func()) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("video", "the stall watch failed: %v", r)
		}
	}()
	t := time.NewTicker(mediaReadWatchTick)
	defer t.Stop()
	lastFed := int64(-1)
	last, seenWakes := time.Now(), wakeCount()
	for {
		select {
		case <-w.stop:
			return
		case <-done:
			return
		case <-t.C:
		}
		// A tick far behind its time (on Windows Go's monotonic clock counts
		// a sleep, so the first tick after a wake comes that late; a stopped
		// process too) or a wake the watcher noticed (wake_resume.go): the
		// time away is not the read's, so a pending read counts from now and
		// the sound is looked at afresh.
		away := time.Since(last) > 3*mediaReadWatchTick
		last = time.Now()
		if n := wakeCount(); away || n != seenWakes {
			seenWakes = n
			if at := w.pending.Load(); at != 0 {
				w.pending.CompareAndSwap(at, w.now())
			}
			lastFed = -1
			continue
		}
		fed := mediaSoundFed(av)
		soundMoved := lastFed >= 0 && fed >= 0 && fed != lastFed
		lastFed = fed
		at := w.pending.Load()
		if at == 0 {
			continue
		}
		if soundMoved || paused() {
			// Not a stall: this read counts from now — if it is still the
			// same read (the decoder may have finished it meanwhile).
			w.pending.CompareAndSwap(at, w.now())
			continue
		}
		if time.Duration(w.now()-at) > mediaStallGiveUp {
			w.fired.Store(true)
			kill()
			return
		}
	}
}

// mediaSoundFed is how much sound this session's sound decoder has given the
// device so far (taken plus still queued), or -1 before its first buffer or
// with no device.
func mediaSoundFed(av *avSync) int64 {
	if av == nil || !av.audioStarted.Load() {
		return -1
	}
	consumed, _, queued, _, _, ok := videoAudioClock()
	if !ok {
		return -1
	}
	return consumed + queued
}

// mediaWatchdogReason is the reason of a decoder mediaReadWatch stopped, in
// words mediaReasonForPeople reads as a time-out.
func mediaWatchdogReason() string {
	return fmt.Sprintf("no data for %d s, the read timed out", int(mediaStallGiveUp/time.Second))
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
