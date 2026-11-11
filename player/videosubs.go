package main

// videosubs.go — subtitles for the embedded player.
//
// The player draws the cue under the playback position over the picture; the
// CC button (X on the pad, C or X on a keyboard) turns them on and off. Where
// the cues come from depends on what plays:
//
//   - a file on the card: a subtitle file beside it (<name>.srt, or
//     <name>.<lang>.srt / .vtt with the configured language first), else the
//     first text track inside it. A download whose page the Downloads library
//     knows fetches its subtitles from that page and saves them beside the
//     file, so the next play is offline;
//   - a web video: fetched in the configured language (Settings → subtitle
//     language) from the cache, the native caption list (ytcaptions.go) or
//     yt-dlp, in that order.
//
// CC is a choice for the session: once it is on, the next video comes up with
// its own subtitles until CC is turned off (it is not saved across launches).
// Only a YouTube video fetches by itself, though: CC carried over to a live
// channel or another site waits for a press, so zapping through channels does
// not start a helper run per channel. A fetch nobody pressed for stays quiet
// about a video that simply has no subtitles; a press always hears how it went
// — the toast used to say "Subtitles on" over a download that then failed
// without a word.
//
// A video's subtitles belong to the video, not to one decode session: a seek
// replaces the session, and used to cancel the fetch running for it.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// subtitleCue is one timed subtitle entry.
type subtitleCue struct {
	Start float64 // seconds
	End   float64 // seconds
	Text  string  // newline-separated lines
}

// subtitleNoTrack is what the session learned about a page that has no
// subtitles in a language. final says the helper was asked too; without it
// only YouTube's own caption list said so, which a press still double-checks.
type subtitleNoTrack struct {
	reason string
	final  bool
}

// subtitleNoTrackMax bounds the session's memory of pages without subtitles.
const subtitleNoTrackMax = 512

var videoSubs struct {
	mu      sync.Mutex
	cues    []subtitleCue
	enabled bool   // CC on; kept from one video to the next for the session
	status  string // the loaded track's source, or the fetch's progress or failure
	owner   *playbackSession
	// sourcePage is the web page a local file was downloaded from, when the
	// Downloads library knows it; "" for streams and any other file.
	sourcePage string
	language   string
	fetching   bool
	request    uint64
	cancel     context.CancelFunc // the running fetch
	// preparing is set while a local file is searched for subtitles of its
	// own (prepareVideoSubtitles); notifyPrepare remembers a CC press made
	// meanwhile, so the search answers it when it is done. fetchNotify is the
	// same for a running fetch: a press announces the result, success too.
	preparing     bool
	notifyPrepare bool
	fetchNotify   bool
	// pressed is set once CC (or Get subtitles) was pressed for the video on
	// screen. With CC carried over, the first press asks for the subtitles
	// that did not come up by themselves; the next one turns CC off.
	pressed bool
	// videoCtx lives as long as the video does — across seeks, which replace
	// the decode session — and is cancelled when another video starts or
	// playback stops.
	videoCtx    context.Context
	videoCancel context.CancelFunc
	// noTrack remembers for the session the pages that have no subtitles in a
	// language, keyed by subtitleNoTrackKey, so a replay does not ask again;
	// autoSaid holds the reasons a fetch nobody pressed for has toasted once.
	noTrack  map[string]subtitleNoTrack
	autoSaid map[string]bool
}

// parseSubtitleTimestamp parses "00:01:02,500" / "00:01:02.500" / "01:02.500".
func parseSubtitleTimestamp(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	s = strings.Replace(s, ",", ".", 1)
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

// cleanSubtitleText strips SRT/VTT styling that the renderer cannot draw.
func cleanSubtitleText(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	// Drop HTML-ish tags (<i>, <font ...>) and ASS overrides ({\an8}).
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	for {
		start := strings.Index(s, "{")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	return strings.TrimSpace(html.UnescapeString(s))
}

// parseSubtitles parses SubRip and WebVTT data into timed cues. Unparseable
// blocks are skipped rather than aborting the whole file. YouTube's
// auto-generated captions are tidied (cleanRollingCaptions); every other file
// parses exactly as written.
func parseSubtitles(data string) []subtitleCue {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	var cues []subtitleCue
	for _, block := range strings.Split(data, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) == 0 {
			continue
		}
		// Find the timing line ("start --> end", optional settings after end).
		timingIdx := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timingIdx = i
				break
			}
		}
		if timingIdx < 0 {
			continue
		}
		halves := strings.SplitN(lines[timingIdx], "-->", 2)
		if len(halves) != 2 {
			continue
		}
		start, okStart := parseSubtitleTimestamp(halves[0])
		endField := strings.TrimSpace(halves[1])
		if f := strings.Fields(endField); len(f) > 0 {
			endField = f[0]
		}
		end, okEnd := parseSubtitleTimestamp(endField)
		if !okStart || !okEnd || end <= start {
			continue
		}
		var textLines []string
		for _, line := range lines[timingIdx+1:] {
			cleaned := cleanSubtitleText(line)
			if cleaned == "" {
				continue
			}
			textLines = append(textLines, cleaned)
		}
		if len(textLines) == 0 {
			continue
		}
		cues = append(cues, subtitleCue{Start: start, End: end, Text: strings.Join(textLines, "\n")})
	}
	if subtitleLooksRolling(data) {
		cues = cleanRollingCaptions(cues)
	}
	return cues
}

// subtitleWordTiming matches the per-word timestamps YouTube puts inside its
// auto-generated WebVTT ("<00:00:01.280>").
var subtitleWordTiming = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`<\d{1,2}:\d{2}(?::\d{2})?[.,]\d{3}>`)
})

// subtitleLooksRolling reports YouTube's auto-generated WebVTT by its word
// timings. A <c> span alone is not enough: hand-made and converted WebVTT uses
// class spans (<c.yellow>) for colour, and such a file must parse as written.
func subtitleLooksRolling(data string) bool {
	return subtitleWordTiming().MatchString(data)
}

// subtitleBlipSeconds is the longest cue cleanRollingCaptions treats as one
// of YouTube's 10 ms hand-over cues.
const subtitleBlipSeconds = 0.05

// cleanRollingCaptions turns YouTube's rolling captions into plain ones. The
// auto-generated track scrolls: every cue repeats the line before it above the
// line being spoken, and a 10 ms cue sits between the two. Drawn as written,
// each line showed twice and the text jumped at every hand-over. Here the
// hand-over cues go (the line before them stays up over their 10 ms instead,
// so the box does not blink empty for a frame), the carried-over line goes,
// and a line held across two cues becomes one cue.
func cleanRollingCaptions(cues []subtitleCue) []subtitleCue {
	out := make([]subtitleCue, 0, len(cues))
	for _, c := range cues {
		if c.End-c.Start < subtitleBlipSeconds {
			if n := len(out); n > 0 && c.Start-out[n-1].End <= subtitleBlipSeconds && c.End > out[n-1].End {
				out[n-1].End = c.End
			}
			continue
		}
		lines := strings.Split(c.Text, "\n")
		if n := len(out); n > 0 {
			prev := strings.Split(out[n-1].Text, "\n")
			last := prev[len(prev)-1]
			for len(lines) > 1 && lines[0] == last {
				lines = lines[1:]
			}
		}
		text := strings.Join(lines, "\n")
		if n := len(out); n > 0 && out[n-1].Text == text && c.Start-out[n-1].End < 0.5 {
			if c.End > out[n-1].End {
				out[n-1].End = c.End
			}
			continue
		}
		out = append(out, subtitleCue{Start: c.Start, End: c.End, Text: text})
	}
	return out
}

// readSubtitleCues reads and parses one subtitle file, refusing anything over
// subtitleFileMax: the auto-generated track of a long livestream runs to tens
// of megabytes, all of it held in the handheld's RAM at once.
func readSubtitleCues(path string) ([]subtitleCue, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, errors.New("not a subtitle file")
	}
	if info.Size() > subtitleFileMax {
		return nil, fmt.Errorf("the subtitle file is too large (%d bytes)", info.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, subtitleFileMax))
	if err != nil {
		return nil, err
	}
	return parseSubtitles(string(data)), nil
}

// loadSidecarSubtitles reads the best subtitle file beside a local video (see
// subtitleSidecarRank for the order).
func loadSidecarSubtitles(videoPath, lang string) ([]subtitleCue, string) {
	files := subtitleSidecarFiles(videoPath)
	sort.SliceStable(files, func(i, j int) bool {
		return subtitleSidecarRank(files[i].lang, lang) < subtitleSidecarRank(files[j].lang, lang)
	})
	for _, f := range files {
		if cues, err := readSubtitleCues(f.path); err == nil && len(cues) > 0 {
			return cues, filepath.Base(f.path)
		}
	}
	return nil, ""
}

// extractEmbeddedSubtitles pulls the first subtitle track out of a local file
// with ffmpeg. Containers without a text track simply return none.
//
// Its output is capped at subtitleFileMax; a video left before the scan
// finished no longer keeps its ffmpeg running because it is tied to ctx.
func extractEmbeddedSubtitles(ctx context.Context, ffmpegPath, videoPath string) ([]subtitleCue, string) {
	if ffmpegPath == "" {
		return nil, ""
	}
	if st, err := os.Stat(videoPath); err != nil || st.IsDir() {
		return nil, ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-v", "error", "-i", videoPath, "-map", "0:s:0", "-f", "srt", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, ""
	}
	if err := cmd.Start(); err != nil {
		return nil, ""
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, int64(subtitleFileMax)+1))
	tooLarge := len(out) > subtitleFileMax
	if tooLarge && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || tooLarge || waitErr != nil || len(out) == 0 {
		return nil, ""
	}
	cues := parseSubtitles(string(out))
	if len(cues) == 0 {
		return nil, ""
	}
	return cues, "inside the video"
}

// prepareVideoSubtitles loads subtitles for a new local source: a file beside
// it first, then a track inside it. Network streams have neither; their cues
// come from a fetch (startEnabledNetworkSubtitles, toggleSubtitlesFor). A
// download with none of its own learns its web page from the Downloads
// library: the subtitle cache may hold them already (saved beside the file
// then, for the next play), and otherwise they are fetched while CC is on.
func prepareVideoSubtitles(sess *playbackSession, config *Config, source string) {
	if sess == nil {
		return
	}
	if isNetworkSource(sess.pageURL) || isNetworkSource(source) {
		// Nothing of its own to search. A session whose page looked local
		// while its source is a link (a stale page URL) was marked as being
		// searched, which would answer every CC press with "Looking for
		// subtitles in this file…" for the whole video.
		videoSubs.mu.Lock()
		if videoSubs.owner == sess {
			videoSubs.preparing = false
			videoSubs.notifyPrepare = false
		}
		videoSubs.mu.Unlock()
		return
	}
	videoSubs.mu.Lock()
	if videoSubs.owner != sess {
		videoSubs.mu.Unlock()
		return
	}
	ctx := videoSubs.videoCtx
	if ctx == nil {
		// Nothing can search without the video's context; the search must
		// not stay "in progress" for the whole video either.
		videoSubs.preparing, videoSubs.notifyPrepare = false, false
		videoSubs.mu.Unlock()
		return
	}
	videoSubs.mu.Unlock()
	lang, err := subtitleLanguage(config)
	if err != nil {
		lang = ""
	}
	cues, label := loadSidecarSubtitles(source, dlFirstNonEmpty(lang, "en"))
	if len(cues) == 0 {
		ffmpegPath := ""
		if config != nil {
			ffmpegPath = getToolPath("ffmpeg", config)
		}
		cues, label = extractEmbeddedSubtitles(ctx, ffmpegPath, source)
	}
	page := ""
	cachedLang := ""
	if len(cues) == 0 {
		page = subtitleLibrarySource(source)
		// The page's subtitles may be in the cache from a play before the
		// download: they come up at once, offline too, and are saved beside
		// the file so the next play does not need the cache either.
		if page != "" && lang != "" {
			if cached := subtitleCacheLoad(page, lang); len(cached) > 0 {
				cues, label, cachedLang = cached, subtitleLanguageName(lang)+" · saved", lang
				if _, err := writeSubtitleSidecar(source, lang, cached); err != nil {
					logDebug("subs", "could not save subtitles beside %s: %v", filepath.Base(source), err)
				}
			}
		}
	}
	online := IsOnline()
	videoSubs.mu.Lock()
	if videoSubs.owner != sess {
		videoSubs.mu.Unlock()
		return
	}
	notify := videoSubs.notifyPrepare && videoSubs.enabled
	videoSubs.preparing = false
	videoSubs.notifyPrepare = false
	videoSubs.sourcePage = page
	message := ""
	switch {
	case len(cues) > 0:
		videoSubs.cues = cues
		videoSubs.status = label
		videoSubs.language = cachedLang
		if notify {
			message = "Subtitles on"
		}
	case videoSubs.fetching:
		// A fetch is on its way already and answers for itself.
		videoSubs.fetchNotify = videoSubs.fetchNotify || notify
	case !videoSubs.enabled || page == "":
		if notify {
			message = "This file has no subtitles"
		}
	case lang == "":
		videoSubs.status = "The subtitle language in Settings is not a language code"
		if notify {
			message = videoSubs.status
		}
	case notify:
		// A page already known to have none answers at once, without another
		// helper run beside the decoder.
		if known, ok := subtitleNoTrackLocked(page, lang); ok && known.final {
			videoSubs.status = known.reason
			message = known.reason
			break
		}
		startSubtitleFetchLocked(config, subtitleFetchSpec{page: page, lang: lang, sidecarFor: source, notify: true})
	default:
		// CC carried over from the last video. Only a YouTube page fetches by
		// itself, only online, and never one already known to have none; the
		// Shorts feed moves on in seconds and is left alone. (shortsAutoplay
		// is the render thread's; this goroutine only reads it, and a stale
		// value at worst starts or skips one fetch.)
		known, isKnown := subtitleNoTrackLocked(page, lang)
		switch {
		case isKnown:
			videoSubs.status = known.reason
		case youtubeVideoID(page) == "":
			videoSubs.status = "Press CC to look for subtitles"
		case !online:
			videoSubs.status = "Subtitles need an internet connection"
		case !shortsAutoplay:
			startSubtitleFetchLocked(config, subtitleFetchSpec{page: page, lang: lang, sidecarFor: source})
		}
	}
	videoSubs.mu.Unlock()
	if message != "" {
		ShowToast(message, ToastKindInfo)
	}
}

// subtitleLibrarySource is the web page a downloaded file came from, "" when
// the Downloads library does not list the file or knows no page for it. Only
// the app's own download folder is looked up.
func subtitleLibrarySource(path string) string {
	if strings.TrimSpace(path) == "" || !isPathWithin(path, downloadsRoot()) {
		return ""
	}
	dlLibraryLoadNow()
	if e, ok := dlLibraryFindPath(path); ok && isNetworkSource(e.Source) {
		return strings.TrimSpace(e.Source)
	}
	return ""
}

// isNetworkSource reports whether a playback source is a remote URL.
func isNetworkSource(source string) bool {
	lower := strings.ToLower(source)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "rtsp://") || strings.HasPrefix(lower, "rtmp://")
}

// subtitleLanguage defaults to English. Only literal language codes are
// accepted: yt-dlp reads its argument as a list of regular expressions (and a
// leading "-" as "not this one", "all" as every language), so configuration
// must not inject a selector.
func subtitleLanguage(config *Config) (string, error) {
	lang := ""
	if config != nil {
		lang = strings.TrimSpace(config.Variables.SubtitleLanguage)
	}
	if lang == "" {
		return "en", nil
	}
	if !subtitleLangCodeValid(lang) {
		return "", fmt.Errorf("invalid subtitle language code")
	}
	return lang, nil
}

// resetVideoSubtitles hands the subtitles to a new video (or to nothing, when
// playback stops): the old video's fetch and cues go, CC stays as it was.
func resetVideoSubtitles(sess *playbackSession) {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if videoSubs.cancel != nil {
		videoSubs.cancel()
		videoSubs.cancel = nil
	}
	if videoSubs.videoCancel != nil {
		videoSubs.videoCancel()
		videoSubs.videoCancel = nil
	}
	videoSubs.videoCtx = nil
	videoSubs.request++
	videoSubs.owner = sess
	videoSubs.cues = nil
	videoSubs.status = ""
	videoSubs.language = ""
	videoSubs.sourcePage = ""
	videoSubs.fetching = false
	videoSubs.fetchNotify = false
	videoSubs.pressed = false
	// A file on the card is searched for subtitles of its own as it starts
	// (prepareVideoSubtitles); a stream is not, and neither is a session whose
	// source is a link.
	videoSubs.preparing = sess != nil && !isNetworkSource(sess.pageURL) && !isNetworkSource(sess.source)
	videoSubs.notifyPrepare = false
	if sess != nil {
		videoSubs.videoCtx, videoSubs.videoCancel = context.WithCancel(context.Background())
	}
}

// ── What the session knows ───────────────────────────────────────────────────

// subtitleNoTrackKey names a page and a language in the session's memory of
// pages without subtitles; "" when the page cannot be named.
func subtitleNoTrackKey(page, lang string) string {
	key := subtitleCacheKey(page)
	if key == "" || strings.TrimSpace(lang) == "" {
		return ""
	}
	return key + "|" + strings.ToLower(strings.TrimSpace(lang))
}

// subtitleNoTrackLocked looks a page up; the caller holds videoSubs.mu.
func subtitleNoTrackLocked(page, lang string) (subtitleNoTrack, bool) {
	key := subtitleNoTrackKey(page, lang)
	if key == "" || videoSubs.noTrack == nil {
		return subtitleNoTrack{}, false
	}
	known, ok := videoSubs.noTrack[key]
	return known, ok
}

// subtitleNoteNoTrackLocked remembers a page without subtitles in lang. A
// final answer is never weakened by a later partial one. The caller holds
// videoSubs.mu.
func subtitleNoteNoTrackLocked(page, lang, reason string, final bool) {
	key := subtitleNoTrackKey(page, lang)
	if key == "" {
		return
	}
	if videoSubs.noTrack == nil || len(videoSubs.noTrack) >= subtitleNoTrackMax {
		videoSubs.noTrack = make(map[string]subtitleNoTrack)
	}
	if prev, ok := videoSubs.noTrack[key]; ok && prev.final && !final {
		return
	}
	videoSubs.noTrack[key] = subtitleNoTrack{reason: reason, final: final}
}

// subtitleArgs keeps the user's own yt-dlp arguments (the cookies file) as the
// last CC press read them. They come from the config's custom map, which the
// settings screens write on the render thread: the press reads it there, and a
// fetch goroutine — or the download worker — only ever reads this copy. A read
// of the map beside a write is a fatal error in Go. CC is off at every launch,
// so a fetch that runs at all follows a press made in this session.
var subtitleArgs struct {
	mu    sync.Mutex
	extra []string
}

// subtitleNoteExtraArgs reads the user's yt-dlp arguments for the fetches to
// come. Render thread (a press) only.
func subtitleNoteExtraArgs(config *Config) {
	extra := ytDlpExtraArgsSlice(config)
	subtitleArgs.mu.Lock()
	subtitleArgs.extra = append([]string(nil), extra...)
	subtitleArgs.mu.Unlock()
}

// subtitleExtraArgs is a copy of the arguments the last press read.
func subtitleExtraArgs() []string {
	subtitleArgs.mu.Lock()
	defer subtitleArgs.mu.Unlock()
	return append([]string(nil), subtitleArgs.extra...)
}

// ── Fetching ─────────────────────────────────────────────────────────────────

// subtitleFetchSpec is one fetch: the page, the language, the downloaded file
// to save the result beside (if any), and whether the user asked for it — then
// success is announced too, not only failure.
type subtitleFetchSpec struct {
	page       string
	lang       string
	sidecarFor string
	notify     bool
}

// subtitleRequest is everything the fetch chain needs. extra is the user's
// yt-dlp arguments (the cookies file) as the caller read them on the render
// thread; nil uses the copy the last CC press made (subtitleExtraArgs), so no
// goroutine reads the config for them. pressed marks a fetch the user asked
// for: when YouTube's caption list says the video has none, a pressed fetch
// still asks the helper, while a fetch nobody pressed for (CC carried over,
// the download worker) takes the list's word for it.
type subtitleRequest struct {
	config  *Config
	page    string
	lang    string
	extra   []string
	pressed bool
}

// subtitleResult is what a fetch brought back: the cues and where they came
// from, or why there are none, in words. none marks a page that has no track
// in the language (not a failure to reach it); final says the helper agreed.
type subtitleResult struct {
	cues   []subtitleCue
	ok     bool
	status string
	reason string
	none   bool
	final  bool
}

// subtitleFetcher runs the fetch chain; tests swap it.
var subtitleFetcher = fetchPageSubtitles

// subtitleYtdlpTimeout bounds the helper's run for one page.
const subtitleYtdlpTimeout = 2 * time.Minute

// subtitleSourceLabel is the status of cues fetched from page.
func subtitleSourceLabel(page, lang string) string {
	if youtubeVideoID(page) != "" {
		return subtitleLanguageName(lang) + " · from YouTube"
	}
	return subtitleLanguageName(lang) + " · downloaded"
}

// fetchPageSubtitles finds subtitles for a web page in one language: the
// cache, then YouTube's caption list (ytcaptions.go), then yt-dlp. A result
// from the network is cached for the next play.
func fetchPageSubtitles(ctx context.Context, r subtitleRequest) subtitleResult {
	name := subtitleLanguageName(r.lang)
	noTrack := "No " + name + " subtitles for this video"
	if cues := subtitleCacheLoad(r.page, r.lang); len(cues) > 0 {
		return subtitleResult{cues: cues, ok: true, status: name + " · saved"}
	}
	if isDirectStreamURL(r.page) {
		// A bare media link carries no subtitle list to ask for.
		return subtitleResult{reason: "This stream has no subtitles", none: true, final: true}
	}
	if !IsOnline() {
		return subtitleResult{reason: "Subtitles need an internet connection"}
	}
	if id := youtubeVideoID(r.page); id != "" {
		cues, err := ytNativeCaptionsFor(ctx, id, r.lang)
		if len(cues) > 0 {
			subtitleCacheStore(r.page, r.lang, cues)
			return subtitleResult{cues: cues, ok: true, status: subtitleSourceLabel(r.page, r.lang)}
		}
		if errors.Is(err, errNoCaptions) && !r.pressed {
			// YouTube answered and listed nothing for the language: a fetch
			// nobody asked for does not spend a helper run (seconds of CPU on
			// the handheld, beside a video decoding) on its second opinion.
			return subtitleResult{reason: noTrack, none: true}
		}
		if err != nil && ctx.Err() == nil {
			logDebug("subs", "native captions for %s stood down, asking yt-dlp: %v", id, err)
		}
	}
	if ctx.Err() != nil {
		return subtitleResult{reason: "Subtitle download cancelled"}
	}
	cues, reason := subtitleYtdlpRunner(ctx, r)
	if len(cues) > 0 {
		subtitleCacheStore(r.page, r.lang, cues)
		return subtitleResult{cues: cues, ok: true, status: subtitleSourceLabel(r.page, r.lang)}
	}
	if reason == "" {
		return subtitleResult{reason: noTrack, none: true, final: true}
	}
	return subtitleResult{reason: reason}
}

// subtitleYtdlpRunner is the yt-dlp step; tests swap it.
var subtitleYtdlpRunner = ytdlpSubtitles

// ytdlpSubtitles asks yt-dlp for a page's subtitles. It returns the cues, or
// why there are none — "" when the run worked and found no track, which the
// caller words for the language.
func ytdlpSubtitles(ctx context.Context, r subtitleRequest) ([]subtitleCue, string) {
	dir, err := os.MkdirTemp("", "juka-subs-")
	if err != nil {
		return nil, "Could not create a folder for the subtitles"
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, subtitleYtdlpTimeout)
	defer cancel()
	// Only a press may install the helper (tens of MB on a slow card); a fetch
	// nobody pressed for — CC carried over, a download's subtitle step — uses
	// the helper when it is there and otherwise says so.
	var ytDlpPath string
	if r.pressed {
		ytDlpPath, err = helperToolPathCtx(ctx, "yt-dlp", r.config)
	} else if p := getToolPath("yt-dlp", r.config); toolPathRunnable(p) {
		ytDlpPath = p
	} else {
		err = errors.New("yt-dlp is not installed")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, "Subtitle download cancelled"
		}
		return nil, "The YouTube helper is not installed yet, so subtitles cannot be fetched"
	}
	extra := r.extra
	if extra == nil {
		extra = subtitleExtraArgs()
	}
	cmd := exec.CommandContext(ctx, ytDlpPath, subtitleDownloadArgs(dir, r.page, r.lang, extra)...)
	cmd.Env = helperToolEnv(ytDlpPath)
	// yt-dlp on the handheld is a one-file bundle: the process started here
	// unpacks the real one and runs it as its child. Cancelling used to kill
	// only the first, and the second went on fetching into a folder being
	// deleted. The run now leads its own process group, and a cancel or the
	// timeout stops all of it (as Power Tools snippets are stopped).
	processGroupConfigure(cmd)
	cmd.Cancel = func() error { return screenChildKill(cmd) }
	cmd.WaitDelay = 3 * time.Second
	out, runErr := cmd.CombinedOutput()
	if runErr != nil && ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, "Subtitle download cancelled"
	}
	// A run that failed may still have saved the track asked for first:
	// YouTube refuses the fifth or sixth subtitle request with HTTP 429, after
	// the wanted one is written.
	if path := pickSubtitleFileFor(dir, r.lang); path != "" {
		cues, readErr := readSubtitleCues(path)
		if readErr == nil && len(cues) > 0 {
			if runErr != nil {
				logDebug("subs", "yt-dlp stopped with %v after saving %s; using it", runErr, filepath.Base(path))
			}
			return cues, ""
		}
		if runErr == nil {
			if readErr != nil {
				return nil, "Could not read the downloaded subtitles"
			}
			return nil, "The downloaded subtitles could not be read"
		}
	}
	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "The subtitle download took too long — try again"
		}
		tail := string(out)
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		logWarn("subs", "yt-dlp subtitle fetch failed: %v: %s", runErr, redactDiagnosticText(strings.TrimSpace(tail)))
		return nil, subtitleFailureReason(string(out))
	}
	return nil, ""
}

// subtitleDownloadArgs is the yt-dlp command line for one page's subtitles.
//
// --sub-langs takes anchored regular expressions, so the bare code ("en")
// matched only a track named exactly that and missed "en-US", "en-GB", the
// auto-generated "en-orig" and named tracks; the selector asks for the code
// and its subtags (and the plain language for a regional wish). On YouTube
// those subtags also matched a translation of every other written track
// ("en-de", "en-es": one request each, and YouTube answers the sixth with HTTP
// 429), so YouTube is told to skip the translations — the caption list's own
// translation (ytcaptions.go) covers that case. Format selection still runs
// under --skip-download and fails the whole run when no format suits, hence
// --ignore-no-formats-error. The user's own arguments come last, before the
// page, so a --extractor-args of theirs wins.
func subtitleDownloadArgs(dir, pageURL, lang string, extra []string) []string {
	args := []string{"--skip-download", "--no-playlist", "--ignore-no-formats-error",
		"--write-subs", "--write-auto-subs",
		"--sub-langs", subtitleLangSelector(lang), "--sub-format", "vtt/srt",
		"-o", filepath.Join(dir, "subs")}
	if youtubeVideoID(pageURL) != "" {
		args = append(args, "--extractor-args", "youtube:skip=translated_subs")
	}
	args = append(args, extra...)
	return append(args, "--", pageURL)
}

// subtitleLangSelector is the --sub-langs value for a literal code: the code,
// any subtags of it, and the plain language when the code is regional
// ("pt-BR,pt-BR-.*,pt").
func subtitleLangSelector(lang string) string {
	q := regexp.QuoteMeta(lang)
	items := []string{q, q + "-.*"}
	if base := subtitleBaseLang(lang); base != "" && !strings.EqualFold(base, lang) {
		items = append(items, regexp.QuoteMeta(base))
	}
	return strings.Join(items, ",")
}

// pickSubtitleFileFor returns the subtitle file yt-dlp wrote into dir that
// serves lang best (subtitleFileRank), WebVTT before SRT, or "" when none was
// written.
func pickSubtitleFileFor(dir, lang string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type written struct {
		name, code string
		srt        bool
	}
	var files []written
	origWritten := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".vtt" && ext != ".srt" {
			continue
		}
		code := strings.TrimPrefix(name[:len(name)-len(ext)], "subs.")
		if strings.EqualFold(code, lang+"-orig") {
			origWritten = true
		}
		files = append(files, written{name: name, code: code, srt: ext == ".srt"})
	}
	best, bestName, bestRank := "", "", 1<<30
	for _, f := range files {
		rank := subtitleFileRank(f.code, lang, origWritten) * 2
		if f.srt {
			rank++
		}
		if rank < bestRank || (rank == bestRank && f.name < bestName) {
			best, bestName, bestRank = filepath.Join(dir, f.name), f.name, rank
		}
	}
	return best
}

// subtitleCodeIsTranslation reports yt-dlp's name for a translation of
// another language's written track into lang: "en-de" is English made from the
// German track. A region is upper case ("en-US"), a named track mixes case and
// digits, and "-orig" is the auto-generated original; a translation's subtag
// is a lower-case language code.
func subtitleCodeIsTranslation(code, lang string) bool {
	if len(code) <= len(lang)+1 || !strings.EqualFold(code[:len(lang)+1], lang+"-") {
		return false
	}
	first := code[len(lang)+1:]
	if i := strings.IndexByte(first, '-'); i >= 0 {
		first = first[:i]
	}
	if first == "orig" || len(first) < 2 || len(first) > 3 {
		return false
	}
	for _, r := range first {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// subtitleFileRank orders yt-dlp's files for a wish (lower is better): the
// exact code, the auto-generated original ("en-orig"), another track of the
// code ("en-US", a named track), a translation into it ("en-de"), the plain
// language of a regional wish ("pt" for "pt-BR"), the language's other
// regions, anything else. When "<code>-orig" was written, the plain "<code>"
// file may well be the auto-generated track (yt-dlp fills a language without a
// written track from the automatic ones), so a written regional or named track
// comes first then — as the caption list prefers it (ytChooseCaptionTrack).
func subtitleFileRank(code, lang string, origWritten bool) int {
	c, l := strings.ToLower(code), strings.ToLower(lang)
	base := subtitleBaseLang(l)
	regional := strings.HasPrefix(c, l+"-") && c != l+"-orig" && !subtitleCodeIsTranslation(code, lang)
	switch {
	case origWritten && regional:
		return 0
	case c == l:
		return 1
	case c == l+"-orig":
		return 2
	case regional:
		return 3
	case strings.HasPrefix(c, l+"-"):
		return 4
	case c == base || c == base+"-orig":
		return 5
	case subtitleBaseLang(c) == base:
		return 6
	}
	return 7
}

// subtitleFailureReason words a failed yt-dlp run.
func subtitleFailureReason(out string) string {
	lower := strings.ToLower(strings.ReplaceAll(out, "’", "'"))
	switch {
	case strings.Contains(lower, "http error 429") || strings.Contains(lower, "too many requests"):
		return "YouTube is limiting subtitle downloads — try again in a few minutes"
	// "Sign in to confirm your age" shares its first words with the bot check,
	// so the age gate is told apart first (as youtubenative.go does).
	case strings.Contains(lower, "confirm your age") || strings.Contains(lower, "age-restricted") ||
		strings.Contains(lower, "age restricted") || strings.Contains(lower, "inappropriate for some users"):
		return "This video is age-restricted, so its subtitles could not be fetched"
	case strings.Contains(lower, "not a bot") || strings.Contains(lower, "sign in to confirm"):
		return "YouTube asked to confirm you're not a bot, so the subtitles could not be fetched"
	case strings.Contains(lower, "private video") || strings.Contains(lower, "video unavailable") ||
		strings.Contains(lower, "this video is not available"):
		return "This video is not available, so there are no subtitles to fetch"
	case strings.Contains(lower, "unable to download video subtitles"):
		return "The subtitles would not download — check the connection and try again"
	}
	return "Subtitle download failed"
}

// startSubtitleFetchLocked starts a fetch for the current video. The caller
// holds videoSubs.mu.
func startSubtitleFetchLocked(config *Config, spec subtitleFetchSpec) {
	sess := videoSubs.owner
	if sess == nil || videoSubs.videoCtx == nil || spec.page == "" || spec.lang == "" {
		return
	}
	if videoSubs.cancel != nil {
		videoSubs.cancel()
	}
	videoSubs.request++
	request := videoSubs.request
	ctx, cancel := context.WithCancel(videoSubs.videoCtx)
	videoSubs.cancel = cancel
	videoSubs.fetching = true
	videoSubs.fetchNotify = spec.notify
	videoSubs.status = "Getting " + subtitleLanguageName(spec.lang) + " subtitles…"
	r := subtitleRequest{config: config, page: spec.page, lang: spec.lang, pressed: spec.notify}
	go runSubtitleFetch(ctx, cancel, sess, request, spec, r)
}

// subtitleFetchLanded is called when a fetch's goroutine is done, toast
// included; tests wait on it.
var subtitleFetchLanded = func() {}

// runSubtitleFetch is one fetch's goroutine: run the chain, save a sidecar for
// a download, install the cues if the video and the press are still current,
// and say how it went. A press hears every outcome. A fetch nobody pressed for
// (CC carried over to the next video) never toasts that a video has no
// subtitles — the sheet's status line says it — and toasts any other reason
// once a session, as a notice: offline, YouTube's limit or a missing helper
// would otherwise sound an error on every video.
func runSubtitleFetch(ctx context.Context, cancel context.CancelFunc, sess *playbackSession, request uint64, spec subtitleFetchSpec, r subtitleRequest) {
	defer subtitleFetchLanded()
	defer cancel()
	res := subtitleFetcher(ctx, r)
	if res.ok && spec.sidecarFor != "" {
		if _, err := writeSubtitleSidecar(spec.sidecarFor, spec.lang, res.cues); err != nil {
			logDebug("subs", "could not save subtitles beside %s: %v", filepath.Base(spec.sidecarFor), err)
		}
	}
	videoSubs.mu.Lock()
	cancelled := ctx.Err() != nil
	if !res.ok && res.none && !cancelled {
		subtitleNoteNoTrackLocked(spec.page, spec.lang, res.reason, res.final)
	}
	current := videoSubs.owner == sess && videoSubs.request == request && !cancelled
	notify := false
	if current {
		videoSubs.cancel = nil
		videoSubs.fetching = false
		notify = videoSubs.fetchNotify
		videoSubs.fetchNotify = false
		if res.ok {
			videoSubs.cues = res.cues
			videoSubs.status = res.status
			videoSubs.language = spec.lang
		} else {
			videoSubs.status = res.reason
		}
	}
	enabled := videoSubs.enabled
	autoToast := false
	if current && enabled && !notify && !res.ok && !res.none && res.reason != "" {
		if videoSubs.autoSaid == nil {
			videoSubs.autoSaid = make(map[string]bool)
		}
		if !videoSubs.autoSaid[res.reason] {
			videoSubs.autoSaid[res.reason] = true
			autoToast = true
		}
	}
	videoSubs.mu.Unlock()
	if !current || !enabled {
		return
	}
	if res.ok && trophyFirstFor(tkSubtitles, spec.page) {
		// Fetched for the video on screen, pressed or carried over; a restart
		// of the same video (a quality switch) fetches them again, uncounted.
		trophyEvent(tkSubtitles)
	}
	switch {
	case res.ok && notify:
		ShowToast(subtitleLanguageName(spec.lang)+" subtitles on", ToastKindSuccess)
	case !res.ok && notify && res.none:
		ShowToast(res.reason, ToastKindInfo)
	case !res.ok && notify:
		ShowToast(res.reason, ToastKindWarning)
	case autoToast:
		ShowToast(res.reason, ToastKindInfo)
	}
}

// startEnabledNetworkSubtitles fetches a new stream's subtitles when CC is on:
// the choice carries over from the last video. Only a YouTube video fetches by
// itself; anything else (a live channel, another site) waits for a press, and
// so does the Shorts feed.
func startEnabledNetworkSubtitles(config *Config) {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	sess := videoSubs.owner
	if !videoSubs.enabled || sess == nil || !isNetworkSource(sess.pageURL) {
		return
	}
	if isDirectStreamURL(sess.pageURL) {
		// A bare media link (a live channel) has no subtitle list to fetch.
		videoSubs.status = "This stream has no subtitles"
		return
	}
	lang, err := subtitleLanguage(config)
	if err != nil {
		videoSubs.status = "The subtitle language in Settings is not a language code"
		return
	}
	// shortsAutoplay is the render thread's; a stream may start on a loader
	// goroutine, which only reads it — a stale value starts or skips one fetch.
	if shortsAutoplay || youtubeVideoID(sess.pageURL) == "" {
		// A page whose subtitles were fetched before — a quality switch or a
		// replay restarts the stream — gets them back from the cache, with no
		// helper run; anything else waits for a press.
		if !shortsAutoplay && subtitleCacheHas(sess.pageURL, lang) {
			startSubtitleFetchLocked(config, subtitleFetchSpec{page: sess.pageURL, lang: lang})
			return
		}
		videoSubs.status = "Press CC to look for subtitles"
		return
	}
	if known, ok := subtitleNoTrackLocked(sess.pageURL, lang); ok {
		videoSubs.status = known.reason
		return
	}
	startSubtitleFetchLocked(config, subtitleFetchSpec{page: sess.pageURL, lang: lang})
}

// subtitleSaveSidecar fetches a downloaded video's subtitles from its page and
// saves them beside the file. The download worker calls it once the video is in
// place; nothing here can fail the download. Like a fetch nobody pressed for,
// it takes YouTube's caption list at its word and skips a page the session
// already knows has none.
func subtitleSaveSidecar(ctx context.Context, r subtitleRequest, videoPath string) (string, error) {
	if !regularFile(videoPath) {
		return "", errors.New("the video is not on the card")
	}
	for _, f := range subtitleSidecarFiles(videoPath) {
		if subtitleLangScore(f.lang, r.lang) == 3 {
			return f.path, nil
		}
	}
	if !isNetworkSource(r.page) {
		return "", errors.New("no web page to fetch subtitles from")
	}
	videoSubs.mu.Lock()
	known, isKnown := subtitleNoTrackLocked(r.page, r.lang)
	videoSubs.mu.Unlock()
	if isKnown {
		return "", errors.New(known.reason)
	}
	res := subtitleFetcher(ctx, r)
	if !res.ok {
		if res.none && ctx.Err() == nil {
			videoSubs.mu.Lock()
			subtitleNoteNoTrackLocked(r.page, r.lang, res.reason, res.final)
			videoSubs.mu.Unlock()
		}
		return "", errors.New(res.reason)
	}
	return writeSubtitleSidecar(videoPath, r.lang, res.cues)
}

// ── CC ───────────────────────────────────────────────────────────────────────

// subtitleToggle is what a press of CC (or "Get subtitles") did.
type subtitleToggle int

const (
	// subtitleToggleOff: CC was turned off.
	subtitleToggleOff subtitleToggle = iota
	// subtitleToggleOn: CC is on and cues are showing.
	subtitleToggleOn
	// subtitleToggleFetching: CC is on and the cues are on their way; the
	// fetch announces itself when it lands.
	subtitleToggleFetching
	// subtitleToggleNone: CC is on, but this video has none; the message says
	// why. The next video still comes up with subtitles.
	subtitleToggleNone
)

// subtitlesEnabled reports the CC toggle state.
func subtitlesEnabled() bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	return videoSubs.enabled
}

// toggleSubtitlesFor is a press of CC; it reports what happened, with the
// message to show for a fetch or for a video without subtitles. Render thread.
//
// With CC on, a press turns it off — unless CC was carried over from the last
// video and nothing is showing yet: that first press asks for this video's
// subtitles (or makes the fetch already running announce itself), and the
// next press turns CC off.
func toggleSubtitlesFor(config *Config) (subtitleToggle, string) {
	subtitleNoteExtraArgs(config)
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if videoSubs.enabled {
		if videoSubs.owner != nil && !videoSubs.pressed && len(videoSubs.cues) == 0 {
			return subtitleEnableLocked(config, true)
		}
		videoSubs.enabled = false
		if videoSubs.cancel != nil {
			videoSubs.cancel()
			videoSubs.cancel = nil
		}
		if videoSubs.fetching {
			videoSubs.fetching = false
			videoSubs.fetchNotify = false
			videoSubs.status = ""
		}
		videoSubs.notifyPrepare = false
		videoSubs.request++
		return subtitleToggleOff, ""
	}
	videoSubs.enabled = true
	return subtitleEnableLocked(config, true)
}

// subtitleEnableLocked decides what CC turning on (or a press on a carried-over
// CC) means for the video on screen: show what is loaded, fetch, or explain.
// The caller holds videoSubs.mu and has set enabled.
func subtitleEnableLocked(config *Config, notify bool) (subtitleToggle, string) {
	if notify {
		videoSubs.pressed = true
	}
	sess := videoSubs.owner
	if sess == nil {
		return subtitleToggleNone, "Subtitles on — they come up with the next video"
	}
	if videoSubs.fetching {
		// A fetch CC carried over is running already: the press asks it to
		// announce its result.
		videoSubs.fetchNotify = videoSubs.fetchNotify || notify
		return subtitleToggleFetching, videoSubs.status
	}
	network := isNetworkSource(sess.pageURL)
	if !network && len(videoSubs.cues) > 0 {
		return subtitleToggleOn, ""
	}
	if !network && videoSubs.preparing {
		// The file is still being searched (a large MKV is read whole for its
		// track): the search answers this press when it is done.
		videoSubs.notifyPrepare = notify
		return subtitleToggleFetching, "Looking for subtitles in this file…"
	}
	if !network && videoSubs.sourcePage == "" {
		if videoSubs.status != "" && videoSubs.status != "Press CC to look for subtitles" {
			return subtitleToggleNone, videoSubs.status
		}
		return subtitleToggleNone, "This file has no subtitles"
	}
	if network && isDirectStreamURL(sess.pageURL) {
		videoSubs.status = "This stream has no subtitles"
		return subtitleToggleNone, videoSubs.status
	}
	lang, err := subtitleLanguage(config)
	if err != nil {
		videoSubs.status = "The subtitle language in Settings is not a language code"
		if len(videoSubs.cues) > 0 {
			return subtitleToggleOn, ""
		}
		return subtitleToggleNone, videoSubs.status
	}
	if network && len(videoSubs.cues) > 0 && lang == videoSubs.language {
		return subtitleToggleOn, ""
	}
	spec := subtitleFetchSpec{page: sess.pageURL, lang: lang, notify: notify}
	if !network {
		spec.page, spec.sidecarFor = videoSubs.sourcePage, sess.source
	}
	// What the session knows for sure is answered at once; a "none" only
	// YouTube's list gave is asked again, with the helper's second opinion.
	if known, ok := subtitleNoTrackLocked(spec.page, lang); ok && (known.final || !notify) {
		videoSubs.status = known.reason
		return subtitleToggleNone, known.reason
	}
	// The cache answers on the spot, before any "Getting…" toast could cover
	// the result — when the file is small, as nearly every track is: the
	// render loop waits on this lock every frame, so a big one is left to the
	// fetch, which reads the cache first and answers from it.
	if cached := subtitleCacheLoadSmall(spec.page, lang); len(cached) > 0 {
		videoSubs.cues = cached
		videoSubs.status = subtitleLanguageName(lang) + " · saved"
		videoSubs.language = lang
		if spec.sidecarFor != "" {
			go func(path string) {
				if _, err := writeSubtitleSidecar(path, lang, cached); err != nil {
					logDebug("subs", "could not save subtitles beside %s: %v", filepath.Base(path), err)
				}
			}(spec.sidecarFor)
		}
		return subtitleToggleOn, ""
	}
	videoSubs.cues = nil
	startSubtitleFetchLocked(config, spec)
	if !videoSubs.fetching {
		return subtitleToggleNone, "Subtitles could not be fetched for this video"
	}
	return subtitleToggleFetching, videoSubs.status
}

// subtitlesSourceFetchable reports whether the local file on screen can fetch
// its subtitles from the page it was downloaded from: it has none of its own,
// the library knows the page, nothing is on its way, and the session has not
// learned for sure that the page has none.
func subtitlesSourceFetchable() bool {
	lang, err := subtitleLanguage(appConfig)
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	sess := videoSubs.owner
	if sess == nil || isNetworkSource(sess.pageURL) || videoSubs.sourcePage == "" ||
		len(videoSubs.cues) > 0 || videoSubs.fetching {
		return false
	}
	if err == nil {
		if known, ok := subtitleNoTrackLocked(videoSubs.sourcePage, lang); ok && known.final {
			return false
		}
	}
	return true
}

// subtitlesFetchFromSource turns CC on and fetches the local file's subtitles
// from its page; they are saved beside the file for the next play. Render
// thread (the sheet's Get subtitles row).
func subtitlesFetchFromSource(config *Config) (subtitleToggle, string) {
	subtitleNoteExtraArgs(config)
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	videoSubs.enabled = true
	return subtitleEnableLocked(config, true)
}

// subtitleTextAt returns the cue text covering the given playback position.
func subtitleTextAt(seconds float64) string {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if !videoSubs.enabled || len(videoSubs.cues) == 0 {
		return ""
	}
	for _, cue := range videoSubs.cues {
		if seconds >= cue.Start && seconds < cue.End {
			return cue.Text
		}
	}
	return ""
}

// renderVideoSubtitles draws the active cue centred above the bottom edge.
// bottomInset is the distance from the element's bottom to the text baseline.
func renderVideoSubtitles(renderer *sdl.Renderer, config *Config, x, y, w, h int32, seconds float64, bottomInset int32) {
	text := subtitleTextAt(seconds)
	if text == "" || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font == nil {
		return
	}
	if w <= 32 || h <= 0 {
		return
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, wrapTextLines(font, line, w-32, 20)...)
	}
	lineH := int32(font.Height()) + 4
	maxLines := int((h - 16) / lineH)
	if maxLines < 1 || len(lines) == 0 {
		return
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	top := y + h - bottomInset - int32(len(lines))*lineH
	if top < y+8 {
		top = y + 8
	}
	var maxWidth int32
	for _, line := range lines {
		lineW, _, _ := font.SizeUTF8(line)
		maxWidth = max32(maxWidth, int32(lineW))
	}
	fillRoundedRect(renderer, x+(w-maxWidth)/2-8, top-2, maxWidth+16, int32(len(lines))*lineH+4, 6, sdl.Color{R: 0, G: 0, B: 0, A: 170})
	for i, line := range lines {
		lineW, _, _ := font.SizeUTF8(line)
		lx := x + (w-int32(lineW))/2
		ly := top + int32(i)*lineH
		renderText(renderer, config, font, line, sdl.Color{R: 255, G: 255, B: 255, A: 255}, lx, ly)
	}
}

// subtitleStatusLine is the settings sheet's description of the loaded track:
// "Subtitles: On (English · from YouTube)", or what is happening instead.
func subtitleStatusLine() string {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if len(videoSubs.cues) == 0 {
		if videoSubs.status != "" {
			return videoSubs.status
		}
		return "No subtitles loaded"
	}
	state := "Off"
	if videoSubs.enabled {
		state = "On"
	}
	if videoSubs.status == "" {
		return "Subtitles: " + state
	}
	return "Subtitles: " + state + " (" + videoSubs.status + ")"
}
