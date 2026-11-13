package main

// ytdownload.go — playing a video from a download when its stream will not
// play: the path every failed stream ends on (videorecover.go, streaming.go).
//
// A tester on a Smart Pro (CrossMix, a build from 2026-11-10) wrote: "It says
// 'cannot stream and will download instead', but never downloads anyways."
// Four things made that true, and this file replaces the code that had them
// (playWithTempFile in core.go is a thin call into ytDownloadAndPlay now):
//
//   - The download could not be seen. yt-dlp's progress was parsed into three
//     variables nothing ever drew, so the loading card said "Loading <title>"
//     over a spinner for minutes — which is exactly what a hang looks like. The
//     card now shows a bar, the percentage, the size, the speed and the time
//     left (ytdownload_ui.go), and B still cancels.
//   - It asked for the one kind of file YouTube no longer hands a device like
//     this one. The player wanted a single muxed file (format 18) from the
//     android client. In November 2026 yt-dlp without a JavaScript runtime —
//     the handheld has none — uses the visionos client, which carries no muxed
//     format at all (yt-dlp issues 17586 and 18005), and YouTube answers the
//     android and web_embedded copies of format 18 with HTTP 403, at once or
//     after about a minute of media, when they carry no PO token (somnilux
//     issue 32, 2026-11-03; hometube PRs 98 and 99, since 2026-10-22). The
//     first attempt now takes the separate picture and sound yt-dlp's own
//     defaults offer and has ffmpeg join them — a local remux, which the
//     firmware's TLS-less ffmpeg does as well as any. The next asks for the HLS
//     copies, which YouTube still serves to visionos where it cuts the plain
//     https ones off at 1 MiB (ytm-rs PR 90). Only then the muxed file.
//   - A flat five-minute cap ended any download that needed longer — a long
//     video, or the handheld's Wi-Fi on a bad day — and threw the file away. A
//     transfer now runs for as long as it keeps moving: it stops when no data
//     arrives for a minute, or when the measured speed says it would take more
//     than an hour (ytdlVerdict).
//   - When yt-dlp could not do it, nothing else was tried. The last attempt is
//     native: the stream URL the native client resolves (or the one that just
//     failed to play) fetched in 10 MB ranges and resumed where a range broke
//     off, which is how the client app and yt-dlp fetch YouTube's files.
//
// Every attempt that fails leaves a "youtube: download failed" line in
// errors.txt with its reason, and the toast at the end is one line that says
// why, read from all of them.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ytdlStallAfter is how long a transfer may go without a new byte before
	// it is given up. yt-dlp prints a progress line for every block it
	// receives, even at a few kilobytes a second, so a minute with no growth is
	// a dead connection rather than a slow one.
	ytdlStallAfter = 60 * time.Second
	// ytdlQuietAfter bounds the silence before the first byte: the one-file
	// helper unpacks itself from the SD card and asks YouTube for the player,
	// printing a line at each step, so this is a hang rather than the wait.
	ytdlQuietAfter = 2 * time.Minute
	// ytdlAskLimit bounds the whole time before the first byte, retries
	// included.
	ytdlAskLimit = 4 * time.Minute
	// ytdlJoinAfter bounds ffmpeg joining the picture and the sound, which
	// prints nothing until it is done: an hour of 720p is a few hundred
	// megabytes copied on the card.
	ytdlJoinAfter = 6 * time.Minute
	// ytdlUpperBound is the longest one download may take. A transfer the
	// measured speed says would need longer stops early, with that figure,
	// instead of after the hour.
	ytdlUpperBound = time.Hour
	// ytdlJudgeAfter is how long a transfer runs before its speed is trusted
	// for that projection: the first seconds of a connection are no measure.
	ytdlJudgeAfter = 30 * time.Second
	// ytdlNativeChunk is one ranged request of the native download. YouTube
	// serves 10 MB ranges at full speed where one long request is throttled,
	// which is why yt-dlp asks for its files the same way.
	ytdlNativeChunk = 10 << 20
	// ytdlNativeTries is how often one range is asked for before the native
	// download gives up on it.
	ytdlNativeTries = 4
	// ytdlNativeStall is how long one range may go without a byte.
	ytdlNativeStall = 30 * time.Second
	// ytdlNativeResolve bounds asking the native client for a fresh URL.
	ytdlNativeResolve = 20 * time.Second
	// ytdlMinFileBytes is the smallest file taken for a video: anything less is
	// an error page or an empty container.
	ytdlMinFileBytes = 64 << 10
	// ytdlFailedStreamTTL is how long the stream URL that just failed is kept
	// for the native download. Signed URLs live for hours; this needs minutes.
	ytdlFailedStreamTTL = 20 * time.Minute
)

// ── What the loading card shows ──────────────────────────────────────────────

// ytdlStage is where the download in flight is.
type ytdlStage int

const (
	ytdlStageAsking   ytdlStage = iota // the helper starts and asks the site for the file
	ytdlStageTransfer                  // bytes are arriving
	ytdlStageJoining                   // ffmpeg joins the picture and the sound
	ytdlStageOpening                   // the file is whole; the player opens it
)

// ytdlState is the download in flight, as the loading card draws it.
type ytdlState struct {
	seq      uint64 // the prepare it belongs to (beginVideoPrepare)
	stage    ytdlStage
	attempt  int  // 1-based
	attempts int  // how many ways there are in all
	youTube  bool // the page is a YouTube video (for the wording)
	done     int64
	total    int64         // expected bytes of this attempt; 0 when unknown
	estimate bool          // total is an estimate, or more parts follow
	fraction float64       // 0..1 of this attempt; below 0 when unknown
	speed    float64       // bytes per second; 0 when unknown
	eta      time.Duration // 0 when unknown
}

var ytdlNow = struct {
	mu sync.Mutex
	on bool
	st ytdlState
	// What sending the download to the background needs (ytdownload_bg.go):
	// the page, where Play was pressed, and the download's own cancel.
	page     string
	startSec float64
	cancel   context.CancelFunc
}{}

// ytdlBegin starts the card's download state for the prepare seq.
func ytdlBegin(seq uint64, youTube bool, attempts int, page string, startSec float64, cancel context.CancelFunc) {
	ytdlNow.mu.Lock()
	ytdlNow.on = true
	ytdlNow.st = ytdlState{seq: seq, youTube: youTube, attempt: 1, attempts: attempts, fraction: -1}
	ytdlNow.page, ytdlNow.startSec, ytdlNow.cancel = page, startSec, cancel
	ytdlNow.mu.Unlock()
}

// ytdlUpdate changes the state of the prepare seq; a later prepare's state is
// left alone. A download sent to the background moves its updates to its
// chip (ytdownload_bg.go).
func ytdlUpdate(seq uint64, change func(*ytdlState)) {
	ytdlNow.mu.Lock()
	if ytdlNow.on && ytdlNow.st.seq == seq {
		change(&ytdlNow.st)
		ytdlNow.mu.Unlock()
		return
	}
	ytdlNow.mu.Unlock()
	ytdlBgUpdate(seq, change)
}

// ytdlClaim ends Y's chance to move the download of prepare seq to the
// background, and reports whether it was moved already. ytdlSendToBackground
// refuses a download whose cancel is gone, and both run under ytdlNow.mu, so
// the answer cannot change after this returns.
func ytdlClaim(seq uint64) bool {
	ytdlNow.mu.Lock()
	if ytdlNow.on && ytdlNow.st.seq == seq {
		ytdlNow.cancel = nil
		ytdlNow.mu.Unlock()
		return false
	}
	ytdlNow.mu.Unlock()
	return ytdlBgHas(seq)
}

// ytdlEnd clears the state of the prepare seq.
func ytdlEnd(seq uint64) {
	ytdlNow.mu.Lock()
	if ytdlNow.st.seq == seq {
		ytdlNow.on = false
		ytdlNow.cancel = nil
	}
	ytdlNow.mu.Unlock()
}

// ytdlSnapshot returns the download the card should show: the one of the
// prepare on screen, never one a newer load replaced.
func ytdlSnapshot() (ytdlState, bool) {
	ytdlNow.mu.Lock()
	st, on := ytdlNow.st, ytdlNow.on
	ytdlNow.mu.Unlock()
	if !on {
		return ytdlState{}, false
	}
	if active, _ := videoPrepareStatus(); !active || !videoPrepareIsCurrent(st.seq) {
		return ytdlState{}, false
	}
	return st, true
}

// ── The download ─────────────────────────────────────────────────────────────

// ytdlFailure is one attempt that did not produce a file.
type ytdlFailure struct {
	method string // the attempt, for the log
	reason string // one line for the log
	text   string // what the cause is read from (the helper's error lines)
	fatal  bool   // the video itself cannot play: no other attempt will do better
	broken bool   // the helper cannot run here: its other attempts are skipped
	stall  bool   // no data arrived for too long
	slow   bool   // it would have taken longer than ytdlUpperBound
}

// ytDownloadAndPlay downloads page and plays the file from startSec. It is the
// work behind playWithTempFile: the loading card stays up for all of it (the
// caller began the prepare), B cancels it, and every way out clears the card.
// It reports whether playback started.
//
// Y on the card sends the download to the background (ytdownload_bg.go): the
// card closes, the download runs on under its own context with a chip in the
// activity strip, and a finished file is filed in Media → Downloads instead of
// taking over the screen. That is why the work below runs under ctx, which the
// prepare's cancel ends only while the download is still on the card.
func ytDownloadAndPlay(parent context.Context, config *Config, ytDlpPath, page string, startSec float64, prepareSeq uint64) bool {
	defer finishVideoPrepare(prepareSeq)
	if parent == nil {
		parent = context.Background()
	}
	page = strings.TrimSpace(page)
	if frac, running := ytdlBgRunningFor(page); running {
		// The same video is already downloading behind the user: a second copy
		// would only race it for the Wi-Fi.
		ytdlBgStillDownloadingToast(frac)
		return false
	}
	youTube := youtubeVideoID(page) != ""
	// Read now: anything that stops a session while this runs may clear them,
	// and the video that finally plays is still this page's.
	origin := mediaPlayOriginName()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-parent.Done():
			// B or a newer load ends the download — unless it was sent to the
			// background first, where only its chip's X ends it.
			if !ytdlBgHas(prepareSeq) {
				cancel()
			}
		case <-ctx.Done():
		}
	}()
	// The player's phase belongs to whatever is on screen; a background
	// download must not paint it.
	phase := func(p string, progress float64, errText string) {
		if !ytdlBgHas(prepareSeq) {
			ytdlSetPlaybackPhase(p, progress, errText)
		}
	}

	ffmpegPath := getToolPath("ffmpeg", config)
	var attempts []ytdlAttempt
	if toolPathRunnable(ytDlpPath) {
		attempts = ytdlAttempts(config, page, ffmpegPath)
	}
	native := youTube || ytdlFailedStream(page) != ""
	total := len(attempts)
	if native {
		total++
	}
	// Checked before the card offers the background: nothing has started
	// yet that Y could move.
	if err := downloadsRequireSpace(videoTempDir(), freeBytes); err != nil {
		logError("youtube", "download failed: %s: %v", sanitizeLogURL(page), err)
		showToast("Not enough space on the SD card to download this video", ToastError())
		ytdlSetPlaybackPhase("error", 0, err.Error())
		return false
	}
	ytdlBegin(prepareSeq, youTube, total, page, startSec, cancel)
	defer ytdlEnd(prepareSeq)
	phase("downloading", 0, "")
	logInfo("youtube", "downloading %s to play it (resume %.0f s, %d way(s) to try)", sanitizeLogURL(page), startSec, total)

	var fails []ytdlFailure
	path := ""
	helperBroken := false
	fatal := false
	for i, a := range attempts {
		if ctx.Err() != nil || helperBroken || fatal {
			break
		}
		ytdlUpdate(prepareSeq, func(s *ytdlState) {
			*s = ytdlState{seq: s.seq, youTube: s.youTube, attempt: i + 1, attempts: s.attempts, fraction: -1}
		})
		tempPath := tempVideoPath()
		f := ytdlRunAttempt(ctx, config, ytDlpPath, page, tempPath, a, prepareSeq)
		if f == nil {
			path = tempPath
			break
		}
		removeTempVideoArtifacts(tempPath)
		if ctx.Err() != nil {
			break
		}
		logWarn("youtube", "download failed: %s (%s, way %d of %d): %s", sanitizeLogURL(page), a.name, i+1, total, f.reason)
		fails = append(fails, *f)
		helperBroken, fatal = f.broken, f.fatal
	}
	if path == "" && native && !fatal && ctx.Err() == nil {
		ytdlUpdate(prepareSeq, func(s *ytdlState) {
			*s = ytdlState{seq: s.seq, youTube: s.youTube, attempt: s.attempts, attempts: s.attempts, fraction: -1}
		})
		tempPath := tempVideoPath()
		f := ytdlNativeAttempt(ctx, config, page, tempPath, prepareSeq)
		if f == nil {
			path = tempPath
		} else {
			removeTempVideoArtifacts(tempPath)
			if ctx.Err() == nil {
				logWarn("youtube", "download failed: %s (%s, way %d of %d): %s", sanitizeLogURL(page), f.method, total, total, f.reason)
				fails = append(fails, *f)
			}
		}
	}

	// From here Y can no longer move the download (ytdlClaim), so bg is the
	// answer for every way out below, whatever is pressed meanwhile.
	bg := ytdlClaim(prepareSeq)
	if ctx.Err() != nil {
		// B, another video started, or the background chip's X: not the
		// link's fault, so no toast, and nothing half-fetched is left on the
		// card.
		if path != "" {
			removeTempVideoArtifacts(path)
		}
		logDebug("youtube", "download of %s cancelled", sanitizeLogURL(page))
		if bg {
			ytdlBgSettle(prepareSeq, "", "", true)
		}
		return false
	}
	if path == "" {
		msg := ytdlFailureMessage(config, page, fails, ytDlpPath)
		// The stream did not play and neither did this: streaming is no worse
		// than the download here, so the next play tries it first again.
		mediaNoteDownloadFailed(page)
		if bg {
			ytdlBgSettle(prepareSeq, "", msg, false)
			return false
		}
		ytdlSetPlaybackPhase("error", 0, msg)
		showToast(msg, ToastError())
		return false
	}

	ytdlUpdate(prepareSeq, func(s *ytdlState) { s.stage = ytdlStageOpening })
	if bg {
		logInfo("youtube", "downloaded %s in the background (%s)", sanitizeLogURL(page), formatBytes(uint64(fileSize(path))))
		ytdlBgSettle(prepareSeq, path, "", false)
		return false
	}
	ytdlSetPlaybackPhase("playing", 1, "")
	// Anything that closed a session during the download may have cleared the
	// page and the screen it was opened from; the file is this page's video.
	if playbackPageURL() != page {
		setPlaybackPageURL(page)
	}
	if origin != "" && mediaPlayOriginName() == "" {
		mediaRestorePlayOrigin(origin)
	}
	logInfo("youtube", "downloaded %s (%s); playing it from %.0f s", sanitizeLogURL(page), formatBytes(uint64(fileSize(path))), startSec)
	// tempFile=true: the session owns the downloaded copy and removes it when
	// playback ends (or keeps it in Downloads, dl_library.go).
	startEmbeddedPlayback(config, path, startSec, true)
	return true
}

// ytdlFailureMessage is the one toast for a download that found no way. The
// watchdog's own verdicts are said as they are; anything else is read the way
// every YouTube failure is (ytfailure.go), from the error lines of every
// attempt together.
func ytdlFailureMessage(config *Config, page string, fails []ytdlFailure, ytDlpPath string) string {
	if len(fails) == 0 {
		logError("youtube", "download failed: %s: nothing could fetch it (yt-dlp at %q runnable: %v)",
			sanitizeLogURL(page), ytDlpPath, toolPathRunnable(ytDlpPath))
		if !toolPathRunnable(ytDlpPath) {
			return "Couldn't download this video — yt-dlp is missing"
		}
		return "Couldn't download this video"
	}
	reasons := make([]string, 0, len(fails))
	texts := make([]string, 0, len(fails))
	allStalled, anySlow, counted := true, false, 0
	for _, f := range fails {
		reasons = append(reasons, f.method+": "+f.reason)
		if f.text != "" {
			texts = append(texts, f.text)
		}
		if f.slow {
			anySlow = true
		}
		if f.text == "" && !f.stall && !f.slow {
			// It says nothing either way: a native attempt with no URL to try.
			continue
		}
		counted++
		if !f.stall && !f.slow {
			allStalled = false
		}
	}
	if counted == 0 {
		allStalled = false
	}
	logError("youtube", "download failed: %s: no way worked (%s)", sanitizeLogURL(page), strings.Join(reasons, "; "))
	switch {
	case anySlow && allStalled:
		return "This video would take over an hour to download here — pick a lower quality in Settings"
	case allStalled:
		return "The download stopped getting data — check the Wi-Fi, then try again"
	}
	return youTubePlaybackFailureMessage(config, page, strings.Join(texts, "\n"))
}

// ── yt-dlp ───────────────────────────────────────────────────────────────────

// ytDlpQueueClients is the client list of the Downloads queue (downloads.go),
// which joins the best picture and sound or takes the sound alone. It was
// "android,web;youtube:player_skip=webpage": the second "youtube:" made
// player_skip an unknown argument, web needs a JavaScript runtime and a PO
// token the handheld has neither of, and since November 2026 YouTube refuses
// the android client's files without a PO token — the override that "now
// breaks video" (somnilux issue 32), whose fix was yt-dlp's own defaults.
// Those defaults are maintained upstream and refreshed with every update
// (ytdlp_update.go); web_embedded stays as the muxed fallback ("/b").
const ytDlpQueueClients = "youtube:player_client=default,web_embedded"

// ytdlAttempt is one way of asking yt-dlp for the file.
type ytdlAttempt struct {
	name string   // for the log
	args []string // what differs between attempts: clients, formats, joining
}

// ytdlAttempts lists the yt-dlp attempts for page, best first. A joined
// download needs ffmpeg; without one the muxed file is the only way.
func ytdlAttempts(config *Config, page, ffmpegPath string) []ytdlAttempt {
	height := effectivePlaybackMaxHeight(config, page)
	single := ytdlAttempt{name: "single file", args: []string{"-f", videoFormatSelectorFor(config, page)}}
	youTube := youtubeVideoID(page) != ""
	if youTube {
		// android for format 18 where YouTube still serves it, then the
		// defaults and web_embedded (streaming.go explains the list).
		single.args = append([]string{"--extractor-args", ytDlpDownloadClients}, single.args...)
	}
	if !toolPathRunnable(ffmpegPath) {
		return []ytdlAttempt{single}
	}
	joinArgs := func(format string) []string {
		args := []string{"-f", format, "--merge-output-format", "mp4"}
		if filepath.IsAbs(ffmpegPath) {
			// yt-dlp looks for ffmpeg on PATH; the firmware's copy is there,
			// JukaHub's own is only next to yt-dlp. Named, it is the one the
			// player decodes with.
			args = append(args, "--ffmpeg-location", ffmpegPath)
		}
		return args
	}
	joined := ytdlAttempt{name: "picture and sound", args: joinArgs(ytdlJoinedFormat(height, ""))}
	if !youTube {
		return []ytdlAttempt{joined, single}
	}
	hls := ytdlAttempt{name: "HLS picture and sound", args: joinArgs(ytdlJoinedFormat(height, "m3u8"))}
	return []ytdlAttempt{joined, hls, single}
}

// ytdlJoinedFormat is the selector of a two-part download at or below height.
// H.264 comes first because the handheld decodes it in software at a fraction
// of VP9's cost, and AV1 not at all; AAC sound first because it goes into an
// MP4 as it is. proto narrows every part to one protocol ("m3u8" for the HLS
// copies); "" takes whatever yt-dlp ranks best. A muxed file ends the list,
// for a site — or a video — that has nothing else.
func ytdlJoinedFormat(height int, proto string) string {
	if height <= 0 {
		height = 720
	}
	p := ""
	if proto != "" {
		p = "[protocol^=" + proto + "]"
	}
	video := fmt.Sprintf("bv[height<=%d]", height)
	return strings.Join([]string{
		video + "[vcodec^=avc1]" + p + "+ba[ext=m4a]" + p,
		video + "[vcodec^=avc1]" + p + "+ba" + p,
		video + "[vcodec!^=av01]" + p + "+ba" + p,
		fmt.Sprintf("b[height<=%d]", height) + p,
	}, "/")
}

// ytdlArgs is the whole command line of one attempt.
func ytdlArgs(config *Config, ytDlpPath, page, tempPath string, a ytdlAttempt) []string {
	args := []string{
		"-o", tempPath,
		// One progress line per update instead of carriage returns, so every
		// line the parser reads is whole.
		"--newline", "--progress",
		"--no-playlist",
		"--no-check-certificate",
		"--geo-bypass",
		"--no-continue",
		// A live stream has no file: yt-dlp would download it until the cap.
		// It is skipped instead, and the message says why (ytdlRunAttempt).
		"--match-filter", "!is_live",
	}
	if youtubeVideoID(page) == "" {
		// Another site's CDN may refuse yt-dlp's own name. YouTube's clients
		// each announce the identity their URLs are issued for, and overriding
		// it with a desktop browser's (as the download used to) is one more
		// way to be refused, so YouTube keeps yt-dlp's.
		args = append(args, "--user-agent", streamUserAgent)
	}
	args = append(args, ytDlpJSRuntimeArgs(ytDlpPath)...)
	args = append(args, a.args...)
	args = append(args, ytDlpExtraArgsSlice(config)...)
	return append(args, page)
}

// ytDlpJSRuntimeArgs names a QuickJS runtime that sits next to the helper.
// Since 2025.11 yt-dlp needs a JavaScript runtime for YouTube's challenges and
// its full format list. Deno is the one it looks for by itself (on PATH, where
// helperToolEnv puts the tools folder); QuickJS-NG — one small binary, which
// suits a 1 GB handheld better than Deno — has to be named (EJS wiki:
// --js-runtimes quickjs:PATH). A qjs put into the tools folder is used from
// then on, by the stream attempts as well (streaming.go).
func ytDlpJSRuntimeArgs(ytDlpPath string) []string {
	if ytDlpPath == "" || !filepath.IsAbs(ytDlpPath) {
		return nil
	}
	name := "qjs"
	if runtime.GOOS == "windows" {
		name = "qjs.exe"
	}
	qjs := filepath.Join(filepath.Dir(ytDlpPath), name)
	if st, err := os.Stat(qjs); err != nil || !st.Mode().IsRegular() {
		return nil
	}
	return []string{"--js-runtimes", "quickjs:" + qjs}
}

// ytdlRunAttempt runs one yt-dlp attempt into tempPath. It returns nil when
// the file is there, or why not.
func ytdlRunAttempt(parent context.Context, config *Config, ytDlpPath, page, tempPath string, a ytdlAttempt, seq uint64) *ytdlFailure {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, ytDlpPath, ytdlArgs(config, ytDlpPath, page, tempPath, a)...)
	cmd.Env = helperToolEnv(ytDlpPath)
	// The one-file helper runs the real yt-dlp as a child of the process
	// started here. A cancel that killed only the first left the second
	// downloading behind the user's back — and beside the next attempt, on a
	// 1 GB device (the subtitle fetch had the same bug, videosubs.go). The run
	// leads its own process group, and B, a newer load or the watchdog stops
	// all of it.
	processGroupConfigure(cmd)
	cmd.Cancel = func() error { return screenChildKill(cmd) }
	cmd.WaitDelay = 3 * time.Second
	tail := &lockedTailBuffer{max: 64 << 10}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	watch := newYtdlWatch(time.Now())
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		reason := ffmpegStartReason(err)
		return &ytdlFailure{method: a.name, reason: "yt-dlp could not start: " + reason, text: err.Error(), broken: true}
	}
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		ytdlReadOutput(pr, tail, watch, seq)
	}()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watch.run(ctx, cancel)
	}()
	waitErr := cmd.Wait()
	_ = pw.Close()
	<-scanned
	cancel()
	<-watchDone

	out := tail.String()
	if parent.Err() != nil {
		return &ytdlFailure{method: a.name, reason: "cancelled"}
	}
	if stop, slow := watch.verdict(); stop != "" {
		// No text: the helper was killed mid-way, and what it printed last
		// (a fragment retry, the runtime warning) is not why it stopped.
		return &ytdlFailure{method: a.name, reason: stop, stall: !slow, slow: slow}
	}
	if waitErr != nil {
		text := ytdlErrorText(out)
		reason := cleanYtDlpError(text)
		if reason == "" {
			reason = waitErr.Error()
		}
		return &ytdlFailure{
			method: a.name,
			reason: reason,
			text:   text,
			fatal:  streamFailureFatal(text),
			// Only the helper's own failure to load counts: an ffmpeg that
			// could not join the parts says the same words, and the
			// single-file attempt needs no ffmpeg.
			broken: !ytdlHelperRan(out) && (helperUnpackFailure(out) || helperCannotRun(out)),
		}
	}
	if strings.Contains(out, "does not pass filter") {
		return &ytdlFailure{method: a.name, reason: "it is a live stream, which has no file to download",
			text: "This live event has no file to download", fatal: true}
	}
	if size := fileSize(tempPath); size < ytdlMinFileBytes {
		return &ytdlFailure{method: a.name, reason: fmt.Sprintf("yt-dlp finished but left %d bytes", size),
			text: ytdlErrorText(out)}
	}
	return nil
}

// ytdlReadOutput reads the helper's output line by line: every line goes to
// the tail kept for the error message, and the progress lines move the card
// and the watchdog. It reads to the end whatever happens, so the helper never
// blocks on a pipe nobody empties.
func ytdlReadOutput(r io.Reader, tail *lockedTailBuffer, watch *ytdlWatch, seq uint64) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	sc.Split(ytdlScanLines)
	var parts ytdlParts
	for sc.Scan() {
		line := sc.Text()
		_, _ = tail.Write([]byte(line + "\n"))
		now := time.Now()
		watch.output(now)
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[info]") && strings.Contains(trimmed, "Downloading") && strings.Contains(trimmed, "format(s):"):
			// "[info] <id>: Downloading 1 format(s): 136+140"
			formats := strings.TrimSpace(trimmed[strings.LastIndex(trimmed, ":")+1:])
			parts.count = strings.Count(formats, "+") + 1
		case strings.HasPrefix(trimmed, "[download] Destination:"):
			parts.next()
		case ytdlPostProcessing(trimmed):
			watch.joining(now)
			ytdlUpdate(seq, func(s *ytdlState) { s.stage = ytdlStageJoining })
		default:
			p, ok := ytdlParseProgress(trimmed)
			if !ok {
				continue
			}
			parts.update(p)
			done, total, estimate, fraction := parts.overall()
			watch.bytes(now, done, total, p.speed)
			ytdlUpdate(seq, func(s *ytdlState) {
				s.stage = ytdlStageTransfer
				s.done, s.total, s.estimate, s.fraction = done, total, estimate, fraction
				s.speed = p.speed
				s.eta = p.eta
			})
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// ytdlScanLines splits on either line ending: yt-dlp ends its lines with \n
// under --newline, but ffmpeg, which it runs to join the parts, may use \r.
func ytdlScanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ytdlHelperRan reports whether yt-dlp itself got as far as its extractor: a
// line tagged "[youtube]", "[info]", "[download]"… rather than only the
// one-file bootloader's "[PYI-…]" lines.
func ytdlHelperRan(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[PYI-") {
			return true
		}
	}
	return false
}

// ytdlPostProcessing reports whether a line starts yt-dlp's work after the
// transfer: joining the parts, or fixing up the container.
func ytdlPostProcessing(line string) bool {
	for _, tag := range []string{"[Merger]", "[FixupM3u8]", "[FixupM4a]", "[FixupDuplicateMoov]", "[VideoRemuxer]", "[VideoConvertor]"} {
		if strings.HasPrefix(line, tag) {
			return true
		}
	}
	return false
}

// ytdlErrorText is the part of the helper's output a cause is read from: its
// ERROR lines (and an "error:" from its option parser). Without any, the last
// few lines. Its WARNING lines alone are left out when there are errors: the
// "No supported JavaScript runtime" warning comes with every run on the
// handheld and would otherwise be read as the cause of whatever failed.
func ytdlErrorText(out string) string {
	var errs, rest []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := ytdlParseProgress(line); ok {
			continue
		}
		if strings.Contains(line, "ERROR:") || strings.HasPrefix(strings.ToLower(line), "yt-dlp: error:") {
			errs = append(errs, line)
			continue
		}
		rest = append(rest, line)
	}
	if len(errs) > 0 {
		return redactDiagnosticText(strings.Join(errs, "\n"))
	}
	if len(rest) > 4 {
		rest = rest[len(rest)-4:]
	}
	return redactDiagnosticText(strings.Join(rest, "\n"))
}

// ── Progress lines ───────────────────────────────────────────────────────────

// ytdlProgress is what one "[download]" progress line says.
type ytdlProgress struct {
	percent  float64       // 0..100; below 0 when the line has none
	done     int64         // bytes so far, when the line says them instead of a percentage
	total    int64         // bytes; 0 when unknown
	estimate bool          // the total is yt-dlp's estimate ("of ~ 35.21MiB")
	speed    float64       // bytes per second; 0 when unknown
	eta      time.Duration // 0 when unknown
}

// ytdlParseProgress reads one yt-dlp progress line:
//
//	[download]  12.3% of   35.21MiB at    1.20MiB/s ETA 00:25
//	[download]   3.3% of ~  35.21MiB at  1.20MiB/s ETA 00:25 (frag 4/120)
//	[download]  12.10MiB at    1.20MiB/s (00:00:10)
//	[download] 100% of   35.21MiB in 00:00:29 at 1.20MiB/s
//
// It reports false for every other line, "Destination:" included.
func ytdlParseProgress(line string) (ytdlProgress, bool) {
	i := strings.Index(line, "[download]")
	if i < 0 {
		return ytdlProgress{}, false
	}
	fields := strings.Fields(line[i+len("[download]"):])
	p := ytdlProgress{percent: -1}
	found := false
	if len(fields) > 1 && fields[1] == "at" {
		// "12.10MiB at 1.20MiB/s": no total, so the bytes come first.
		if n, ok := parseDownloadSize(fields[0]); ok {
			p.done = n
			found = true
		}
	}
	for k := 0; k < len(fields); k++ {
		f := fields[k]
		switch {
		case strings.HasSuffix(f, "%"):
			if v, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64); err == nil && v >= 0 && v <= 100 {
				p.percent = v
				found = true
			}
		case f == "of" && k+1 < len(fields):
			next := fields[k+1]
			if next == "~" && k+2 < len(fields) {
				k++
				next = fields[k+1]
				p.estimate = true
			} else if strings.HasPrefix(next, "~") {
				next = strings.TrimPrefix(next, "~")
				p.estimate = true
			}
			if n, ok := parseDownloadSize(next); ok {
				p.total = n
				found = true
			}
		case f == "at" && k+1 < len(fields):
			if n, ok := parseDownloadSize(fields[k+1]); ok {
				p.speed = float64(n)
				found = true
			}
		case f == "ETA" && k+1 < len(fields):
			if d, ok := ytdlParseClock(fields[k+1]); ok {
				p.eta = d
			}
		}
	}
	return p, found
}

// ytdlParseClock reads "25", "00:25" or "01:02:03" as a duration.
func ytdlParseClock(s string) (time.Duration, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	secs := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(secs) * time.Second, true
}

// ytdlParts follows a download made of several files (the picture, then the
// sound) so the card shows one bar for all of them.
type ytdlParts struct {
	count      int   // parts announced ("136+140" is two); 0 until said
	index      int   // 1-based part being fetched; 0 before the first
	doneBefore int64 // bytes of the parts already finished
	cur        ytdlProgress
	curDone    int64
}

// next starts the following part.
func (p *ytdlParts) next() {
	if p.index > 0 {
		finished := p.curDone
		if p.cur.total > finished {
			finished = p.cur.total
		}
		p.doneBefore += finished
	}
	p.index++
	p.cur, p.curDone = ytdlProgress{percent: -1}, 0
}

// update takes one progress line of the current part.
func (p *ytdlParts) update(line ytdlProgress) {
	if p.index == 0 {
		p.index = 1
	}
	if line.total > 0 {
		p.cur.total, p.cur.estimate = line.total, line.estimate
	}
	if line.percent >= 0 {
		p.cur.percent = line.percent
		if p.cur.total > 0 {
			p.curDone = int64(line.percent / 100 * float64(p.cur.total))
		}
	} else if line.done > 0 {
		p.curDone = line.done
	}
}

// overall is the progress of all parts together. The picture is most of a
// joined download, so with two parts it is nine tenths of the bar.
func (p *ytdlParts) overall() (done, total int64, estimate bool, fraction float64) {
	done = p.doneBefore + p.curDone
	if p.cur.total > 0 {
		total = p.doneBefore + p.cur.total
	}
	count := p.count
	if count < p.index {
		count = p.index
	}
	estimate = p.cur.estimate || p.index < count
	fraction = -1
	if p.cur.percent < 0 {
		return done, total, estimate, fraction
	}
	cur := p.cur.percent / 100
	switch {
	case count <= 1:
		fraction = cur
	case count == 2:
		if p.index <= 1 {
			fraction = 0.9 * cur
		} else {
			fraction = 0.9 + 0.1*cur
		}
	default:
		fraction = (float64(p.index-1) + cur) / float64(count)
	}
	return done, total, estimate, fraction
}

// ── The watchdog ─────────────────────────────────────────────────────────────

// ytdlWatch is the adaptive time cap of one attempt. It replaces the flat five
// minutes: a transfer that keeps moving runs on, one that stops is ended a
// minute later, and one the measured speed says would take more than an hour
// is ended once that speed can be trusted, with the figure.
type ytdlWatch struct {
	mu         sync.Mutex
	started    time.Time
	lastOutput time.Time
	lastGrowth time.Time // when the byte count last grew
	firstBytes time.Time // when the transfer began; zero before
	joinFrom   time.Time // when joining began; zero before
	done       int64
	total      int64
	speed      float64
	stopped    string // why the watchdog ended the attempt
	slow       bool
}

func newYtdlWatch(now time.Time) *ytdlWatch {
	return &ytdlWatch{started: now, lastOutput: now, lastGrowth: now}
}

// output notes a line from the helper.
func (w *ytdlWatch) output(now time.Time) {
	w.mu.Lock()
	w.lastOutput = now
	w.mu.Unlock()
}

// joining notes the start of the work after the transfer.
func (w *ytdlWatch) joining(now time.Time) {
	w.mu.Lock()
	if w.joinFrom.IsZero() {
		w.joinFrom = now
	}
	w.lastOutput = now
	w.mu.Unlock()
}

// bytes notes where the transfer is.
func (w *ytdlWatch) bytes(now time.Time, done, total int64, speed float64) {
	w.mu.Lock()
	if w.firstBytes.IsZero() {
		w.firstBytes = now
		w.lastGrowth = now
	}
	// A line without a byte count still means data: yt-dlp prints one per
	// block it receives, and nothing while a connection hangs.
	if done > w.done || done <= 0 {
		w.lastGrowth = now
	}
	w.done, w.total = done, total
	if speed > 0 {
		w.speed = speed
	}
	w.mu.Unlock()
}

// ytdlPausedGap is a gap between two of the watchdog's one-second ticks that
// only a paused process explains.
const ytdlPausedGap = 10 * time.Second

// shift moves every clock of the watch forward by d, so a pause is not counted
// as time without data — never past now, because the reader may already have
// noted output that arrived after the pause. Callers hold w.mu.
func (w *ytdlWatch) shift(d time.Duration, now time.Time) {
	move := func(t time.Time) time.Time {
		if t.IsZero() {
			return t
		}
		if t = t.Add(d); t.After(now) {
			return now
		}
		return t
	}
	w.started, w.lastOutput, w.lastGrowth = move(w.started), move(w.lastOutput), move(w.lastGrowth)
	w.firstBytes, w.joinFrom = move(w.firstBytes), move(w.joinFrom)
}

// verdict is why the watchdog stopped the attempt ("" when it did not), and
// whether that was the speed.
func (w *ytdlWatch) verdict() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped, w.slow
}

// run checks the attempt every second until ctx ends, and cancels it when
// ytdlVerdict says so.
func (w *ytdlWatch) run(ctx context.Context, cancel context.CancelFunc) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	prev := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// The wall clock, not the tick's own time: after a pause the
			// first tick carries the time it was due, before the pause.
			now := time.Now()
			gap := now.Sub(prev)
			prev = now
			w.mu.Lock()
			if gap > ytdlPausedGap {
				// The app was paused (a game took the handheld, launch.sh
				// stopped it) and nobody could read the helper's output
				// meanwhile: the silence is ours, not the connection's. A
				// background download lives through that now.
				w.shift(gap-time.Second, now)
			}
			why, slow := ytdlVerdict(now, w.started, w.lastOutput, w.lastGrowth, w.firstBytes, w.joinFrom, w.done, w.total, w.speed)
			if why != "" {
				w.stopped, w.slow = why, slow
			}
			w.mu.Unlock()
			if why != "" {
				cancel()
				return
			}
		}
	}
}

// ytdlVerdict decides whether an attempt should end now and why (""
// otherwise); slow says the reason is the speed rather than a stall.
func ytdlVerdict(now, started, lastOutput, lastGrowth, firstBytes, joinFrom time.Time, done, total int64, speed float64) (string, bool) {
	switch {
	case !joinFrom.IsZero():
		if now.Sub(lastOutput) > ytdlJoinAfter {
			return "ffmpeg took too long to join the picture and the sound", false
		}
		return "", false
	case firstBytes.IsZero():
		if now.Sub(lastOutput) > ytdlQuietAfter {
			return fmt.Sprintf("yt-dlp printed nothing for %d s before the transfer began", int(now.Sub(lastOutput).Seconds())), false
		}
		if now.Sub(started) > ytdlAskLimit {
			return fmt.Sprintf("no data after %d s of asking for the file", int(now.Sub(started).Seconds())), false
		}
		return "", false
	}
	if now.Sub(lastGrowth) > ytdlStallAfter {
		return fmt.Sprintf("no data arrived for %d s", int(now.Sub(lastGrowth).Seconds())), false
	}
	elapsed := now.Sub(started)
	if elapsed > ytdlUpperBound {
		return "it ran for an hour", true
	}
	if now.Sub(firstBytes) > ytdlJudgeAfter && speed > 0 && total > done {
		left := time.Duration(float64(total-done) / speed * float64(time.Second))
		if elapsed+left > ytdlUpperBound {
			return fmt.Sprintf("at %s it would take about %d minutes", formatDownloadSpeed(speed), int((elapsed+left).Minutes()+0.5)), true
		}
	}
	return "", false
}

// ── The native download ──────────────────────────────────────────────────────

// The stream URL each page failed to play with, kept for the native download
// (videorecover.go notes it as the failed session closes).
var ytdlFailedStreams = struct {
	mu sync.Mutex
	m  map[string]ytdlFailedStreamEntry
}{m: map[string]ytdlFailedStreamEntry{}}

type ytdlFailedStreamEntry struct {
	direct string
	at     time.Time
}

// ytdlRememberFailedStream keeps the stream URL page just failed to play with.
// A playlist is not kept: it is no file.
func ytdlRememberFailedStream(page, direct string) {
	page, direct = strings.TrimSpace(page), strings.TrimSpace(direct)
	if page == "" || !isHTTPMediaURL(direct) || direct == page || mediaIsHLS(direct) {
		return
	}
	ytdlFailedStreams.mu.Lock()
	if len(ytdlFailedStreams.m) >= 16 {
		ytdlFailedStreams.m = map[string]ytdlFailedStreamEntry{}
	}
	ytdlFailedStreams.m[page] = ytdlFailedStreamEntry{direct: direct, at: time.Now()}
	ytdlFailedStreams.mu.Unlock()
}

// ytdlFailedStream returns the URL ytdlRememberFailedStream kept for page, if
// it is recent enough to still be signed.
func ytdlFailedStream(page string) string {
	ytdlFailedStreams.mu.Lock()
	defer ytdlFailedStreams.mu.Unlock()
	e, ok := ytdlFailedStreams.m[strings.TrimSpace(page)]
	if !ok || time.Since(e.at) > ytdlFailedStreamTTL {
		return ""
	}
	return e.direct
}

// ytdlNativeAttempt downloads page without yt-dlp: a URL the native client
// resolves now, else the one the failed stream had, fetched in ranges.
func ytdlNativeAttempt(parent context.Context, config *Config, page, tempPath string, seq uint64) *ytdlFailure {
	const method = "native download"
	var sources []string
	if id := youtubeVideoID(page); id != "" && ytNativeAllowed() {
		ctx, cancel := context.WithTimeout(parent, ytdlNativeResolve)
		direct, ok := ytNativeStreamFor(ctx, id, effectivePlaybackMaxHeight(config, page))
		cancel()
		if ok && direct != "" {
			sources = append(sources, direct)
		}
	}
	if failed := ytdlFailedStream(page); failed != "" && (len(sources) == 0 || sources[0] != failed) {
		sources = append(sources, failed)
	}
	if len(sources) == 0 {
		return &ytdlFailure{method: method, reason: "no stream URL to fetch (the native client stood down)"}
	}
	var last *ytdlFailure
	for _, direct := range sources {
		if parent.Err() != nil {
			return &ytdlFailure{method: method, reason: "cancelled"}
		}
		last = ytdlNativeFetchAttempt(parent, direct, tempPath, seq)
		if last == nil {
			return nil
		}
		removeTempVideoArtifacts(tempPath)
	}
	return last
}

// ytdlNativeFetchAttempt fetches one direct URL under the same watchdog the
// helper runs under.
func ytdlNativeFetchAttempt(parent context.Context, direct, tempPath string, seq uint64) *ytdlFailure {
	const method = "native download"
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	watch := newYtdlWatch(time.Now())
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watch.run(ctx, cancel)
	}()
	var meter ytdlSpeedMeter
	err := ytdlNativeFetch(ctx, direct, tempPath, ytdlAgentsFor(direct), func(done, total int64) {
		now := time.Now()
		speed := meter.add(now, done)
		watch.output(now)
		watch.bytes(now, done, total, speed)
		ytdlUpdate(seq, func(s *ytdlState) {
			s.stage = ytdlStageTransfer
			s.done, s.speed, s.estimate = done, speed, false
			s.total, s.fraction, s.eta = 0, -1, 0
			if total > 0 {
				s.total = total
				s.fraction = float64(done) / float64(total)
				if speed > 0 && total > done {
					s.eta = time.Duration(float64(total-done) / speed * float64(time.Second))
				}
			}
		})
	})
	cancel()
	<-watchDone
	if parent.Err() != nil {
		return &ytdlFailure{method: method, reason: "cancelled"}
	}
	if stop, slow := watch.verdict(); stop != "" {
		return &ytdlFailure{method: method, reason: stop, stall: !slow, slow: slow}
	}
	if err != nil {
		msg := ytdlNativeError(err)
		return &ytdlFailure{method: method, reason: redactDiagnosticText(msg), text: msg}
	}
	if size := fileSize(tempPath); size < ytdlMinFileBytes {
		return &ytdlFailure{method: method, reason: fmt.Sprintf("the server sent only %d bytes", size)}
	}
	return nil
}

// ytdlNativeError is a native failure in the transport's own words, without
// the signed media URL a *url.Error puts in front of them: that URL belongs
// neither in errors.txt nor in a toast.
func ytdlNativeError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// ytdlAgentsFor is the identities a direct URL is asked for with, in order:
// the browser one every other request in the chain used, then the client
// app's own when the URL names a client the native table knows ("c=ANDROID"):
// a CDN that ties a URL to its app refuses the first and serves the second.
func ytdlAgentsFor(direct string) []string {
	agents := []string{streamUserAgent}
	if u, err := url.Parse(direct); err == nil {
		if ua := ytNativeClientUserAgent(strings.ToUpper(u.Query().Get("c"))); ua != "" {
			agents = append(agents, ua)
		}
	}
	return agents
}

// ytdlStatusError is a range the server answered with an error status.
type ytdlStatusError struct {
	status int
	at     int64 // the byte offset asked for
}

func (e *ytdlStatusError) Error() string {
	return fmt.Sprintf("the video server answered HTTP %d at byte %d", e.status, e.at)
}

// ytdlNativeFetch downloads direct into path in ranges of ytdlNativeChunk,
// asking again from where a range broke off, and reports progress as bytes
// arrive. A refusal is asked once more with the next identity in agents;
// anything else transient is retried with a growing pause.
func ytdlNativeFetch(ctx context.Context, direct, path string, agents []string, progress func(done, total int64)) error {
	if len(agents) == 0 {
		agents = []string{streamUserAgent}
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// No overall timeout on the client: each range is bounded by its own
	// stall timer, and the whole by the watchdog and ctx.
	client := &http.Client{Transport: ytTransport()}
	var done int64
	total := int64(-1)
	agent, tries := 0, 0
	for total < 0 || done < total {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		end := done + ytdlNativeChunk - 1
		if total > 0 && end > total-1 {
			end = total - 1
		}
		n, size, rangeErr := ytdlFetchRange(ctx, client, direct, agents[agent], done, end, out, func(got, size int64) {
			whole := total
			if size > 0 {
				whole = size
			}
			progress(done+got, whole)
		})
		done += n
		if size > 0 {
			total = size
		}
		if rangeErr == nil {
			tries = 0
			if size == 0 {
				// The whole body came without a length: that is the file.
				total = done
			}
			continue
		}
		var status *ytdlStatusError
		if errors.As(rangeErr, &status) {
			switch status.status {
			case http.StatusForbidden, http.StatusUnauthorized, http.StatusGone, http.StatusNotFound:
				if agent+1 < len(agents) {
					agent++
					continue
				}
				out.Close()
				return rangeErr
			case http.StatusRequestedRangeNotSatisfiable:
				if total > 0 && done >= total {
					continue
				}
			}
		}
		if ctx.Err() != nil {
			out.Close()
			return ctx.Err()
		}
		if n > 0 {
			// The range broke off after it delivered bytes: the next one
			// resumes there, so only breaks in a row with nothing between
			// them use up the tries. A server that cuts every request short
			// (YouTube does at 1 MiB on some connections) still gets there;
			// the watchdog stops one that is too slow.
			tries = 0
		}
		tries++
		if tries >= ytdlNativeTries {
			out.Close()
			return rangeErr
		}
		pause := time.Duration(1<<(tries-1)) * time.Second
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	progress(done, done)
	return nil
}

// ytdlFetchRange fetches bytes start..end of direct and appends them to w. It
// returns how many bytes it wrote, the whole file's size when the answer said
// it (0 when not), and an error for anything short of the full range. A range
// whose answer or data stops for ytdlNativeStall is ended, and the caller asks
// again from where it stopped. progress gets the bytes of this range so far
// and the whole size as the answer gave it.
func ytdlFetchRange(ctx context.Context, client *http.Client, direct, agent string, start, end int64, w io.Writer, progress func(got, size int64)) (int64, int64, error) {
	rangeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(rangeCtx, http.MethodGet, direct, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	// Bytes as stored: a compressed answer would not line up with the range.
	req.Header.Set("Accept-Encoding", "identity")
	// The timer bounds the wait for the answer as well as for the data: the
	// transport has no header timeout of its own.
	idle := time.AfterFunc(ytdlNativeStall, cancel)
	defer idle.Stop()
	stalled := func(err error) error {
		if rangeCtx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("no data for %d s", int(ytdlNativeStall.Seconds()))
		}
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, stalled(err)
	}
	defer resp.Body.Close()
	size := int64(0)
	switch resp.StatusCode {
	case http.StatusPartialContent:
		first, total, ok := ytdlContentRange(resp.Header.Get("Content-Range"))
		if !ok || first != start {
			return 0, 0, fmt.Errorf("the video server answered a different range (%q)", resp.Header.Get("Content-Range"))
		}
		size = total
	case http.StatusOK:
		// The server ignored the range. From the start that is the whole
		// file; anywhere else it would put the file's head in the middle.
		if start != 0 {
			return 0, 0, errors.New("the video server cannot resume a download")
		}
		if resp.ContentLength > 0 {
			size = resp.ContentLength
		}
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return 0, 0, &ytdlStatusError{status: resp.StatusCode, at: start}
	}
	if start == 0 {
		ctype := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
		if strings.HasPrefix(ctype, "text/") {
			return 0, 0, fmt.Errorf("the video server sent a page (%s), not the video", ctype)
		}
	}
	idle.Reset(ytdlNativeStall)
	buf := make([]byte, 64<<10)
	var got int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(ytdlNativeStall)
			if _, err := w.Write(buf[:n]); err != nil {
				return got, size, fmt.Errorf("could not write the download: %w", err)
			}
			got += int64(n)
			progress(got, size)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return got, size, stalled(readErr)
		}
	}
	if resp.StatusCode == http.StatusPartialContent && got < end-start+1 && (size <= 0 || start+got < size) {
		return got, size, io.ErrUnexpectedEOF
	}
	return got, size, nil
}

// ytdlContentRange reads "bytes 0-1023/35210000": the first byte and the
// whole size (0 when the server wrote "*").
func ytdlContentRange(header string) (first, total int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes ")
	if !found {
		return 0, 0, false
	}
	rng, size, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, false
	}
	from, _, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, false
	}
	first, err := strconv.ParseInt(strings.TrimSpace(from), 10, 64)
	if err != nil || first < 0 {
		return 0, 0, false
	}
	if strings.TrimSpace(size) != "*" {
		if total, err = strconv.ParseInt(strings.TrimSpace(size), 10, 64); err != nil || total < 0 {
			return 0, 0, false
		}
	}
	return first, total, true
}

// ytdlSpeedMeter smooths a download's speed over whole seconds.
type ytdlSpeedMeter struct {
	at    time.Time
	bytes int64
	rate  float64
}

// add notes the byte count at now and returns the smoothed speed.
func (m *ytdlSpeedMeter) add(now time.Time, done int64) float64 {
	if m.at.IsZero() {
		m.at, m.bytes = now, done
		return m.rate
	}
	dt := now.Sub(m.at).Seconds()
	if dt < 1 {
		return m.rate
	}
	inst := float64(done-m.bytes) / dt
	if m.rate == 0 {
		m.rate = inst
	} else {
		m.rate = 0.7*m.rate + 0.3*inst
	}
	m.at, m.bytes = now, done
	return m.rate
}
