package main

// ytcaptions.go — YouTube subtitles without the helper, and the files
// subtitles are kept in.
//
// Subtitles used to come from one place: a yt-dlp run that asked for the exact
// code in Settings ("en"). The run costs seconds of CPU on the handheld before
// YouTube is even asked, and the exact code missed most English tracks: they
// are "en-US", "en-GB" or a named track, and the auto-generated one is filed as
// a translation. So the player said "No subtitles found for en" for videos that
// plainly had subtitles.
//
// The fast path here asks the same Innertube player call the stream resolver
// makes (youtubenative.go) for the video's caption list, picks the track for
// the language — a track someone wrote in that language, then the same
// language's other regions, then the auto-generated track, then a translation
// YouTube makes on the fly — and fetches it as WebVTT. Whatever it cannot do (a
// client cooling down, the empty answer YouTube gives where it wants a
// proof-of-origin token) falls back to yt-dlp (videosubs.go). A list with
// nothing for the language (errNoCaptions) is taken at its word by a fetch
// nobody pressed for; a press still asks yt-dlp. A caption fetch never benches
// a client: those cooldowns are for streaming, and a caption-only problem says
// nothing about the stream.
//
// What a fetch brings back is kept twice: in a small cache under the app's data
// folder, so a replay, a quality switch or a second press of CC does not fetch
// again, and — for a downloaded video — as an .srt beside the file, which plays
// offline here and in any other player.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ytCaptionTrack is one entry of the player answer's caption list.
type ytCaptionTrack struct {
	BaseURL        string     `json:"baseUrl"`
	LanguageCode   string     `json:"languageCode"`
	Kind           string     `json:"kind"` // "asr" marks the auto-generated track
	IsTranslatable bool       `json:"isTranslatable"`
	VssID          string     `json:"vssId"`
	Name           nativeText `json:"name"`
}

// ytCaptionsInfo is the player answer's "captions" object.
type ytCaptionsInfo struct {
	Renderer struct {
		CaptionTracks []ytCaptionTrack `json:"captionTracks"`
	} `json:"playerCaptionsTracklistRenderer"`
}

// errNoCaptions is YouTube answering that a video has no track for the
// language (none at all, or none in it and none to translate). A fetch nobody
// pressed for takes it as the answer; a press still asks yt-dlp, in case the
// client's list was short.
var errNoCaptions = errors.New("YouTube lists no subtitles for this video in that language")

// ytCaptionTracksOf reads the caption list out of a player answer. The answer
// keeps it raw (ytPlayerResponse.Captions) and it is decoded only here, so a
// change in its shape can only cost the subtitles, never the stream the same
// answer resolves.
func ytCaptionTracksOf(player *ytPlayerResponse) ([]ytCaptionTrack, error) {
	if player == nil || len(player.Captions) == 0 {
		return nil, nil
	}
	var info ytCaptionsInfo
	if err := json.Unmarshal(player.Captions, &info); err != nil {
		return nil, fmt.Errorf("the caption list is unreadable: %w", err)
	}
	return info.Renderer.CaptionTracks, nil
}

// ytCaptionChoice is the track to fetch and, for a translation, the language
// YouTube translates it into ("" fetches the track as it is).
type ytCaptionChoice struct {
	track ytCaptionTrack
	tlang string
}

// ytCaptionTimeout bounds one caption request; the player call has its own.
const ytCaptionTimeout = 8 * time.Second

// subtitleBaseLang is a code's language without its region or script: "en"
// for "en-US", "zh" for "zh-Hans".
func subtitleBaseLang(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		return code[:i]
	}
	return code
}

// subtitleHasScript reports a code carrying a script subtag ("zh-Hans"): two
// scripts of one language are different alphabets, not two accents.
func subtitleHasScript(code string) bool {
	parts := strings.FieldsFunc(code, func(r rune) bool { return r == '-' || r == '_' })
	for i, part := range parts {
		if i > 0 && len(part) == 4 {
			return true
		}
	}
	return false
}

// subtitleLangScore rates how well a track's code serves the wanted one: 3 the
// same code; 2 the plain language for a regional wish, or a region of a plain
// wish ("pt" for "pt-BR", "en-US" for "en"); 1 another region of the same
// language ("en-GB" for "en-US"); 0 no match — which is also the answer for
// two different scripts ("zh-Hant" for "zh-Hans").
func subtitleLangScore(code, want string) int {
	c := strings.ToLower(strings.TrimSpace(code))
	w := strings.ToLower(strings.TrimSpace(want))
	if c == "" || w == "" {
		return 0
	}
	if c == w {
		return 3
	}
	cb, wb := subtitleBaseLang(c), subtitleBaseLang(w)
	if cb != wb {
		return 0
	}
	if c == wb || cb == w {
		return 2
	}
	if subtitleHasScript(c) || subtitleHasScript(w) {
		return 0
	}
	return 1
}

// ytChooseCaptionTrack picks the track for lang. A track someone wrote beats
// the auto-generated one even in a neighbouring region, the auto-generated one
// beats a translation, and a translation (written tracks first) beats nothing.
func ytChooseCaptionTrack(tracks []ytCaptionTrack, lang string) (ytCaptionChoice, bool) {
	bestManual, bestASR := -1, -1
	scoreManual, scoreASR := 0, 0
	for i, t := range tracks {
		if strings.TrimSpace(t.BaseURL) == "" {
			continue
		}
		s := subtitleLangScore(t.LanguageCode, lang)
		if s == 0 {
			continue
		}
		if t.Kind == "asr" {
			if s > scoreASR {
				bestASR, scoreASR = i, s
			}
		} else if s > scoreManual {
			bestManual, scoreManual = i, s
		}
	}
	if bestManual >= 0 {
		return ytCaptionChoice{track: tracks[bestManual]}, true
	}
	if bestASR >= 0 {
		return ytCaptionChoice{track: tracks[bestASR]}, true
	}
	for _, asr := range []bool{false, true} {
		for _, t := range tracks {
			if strings.TrimSpace(t.BaseURL) == "" || !t.IsTranslatable || (t.Kind == "asr") != asr {
				continue
			}
			return ytCaptionChoice{track: t, tlang: lang}, true
		}
	}
	return ytCaptionChoice{}, false
}

// ytCaptionURL is a track's address asked for as WebVTT, translated when tlang
// is set. Only YouTube's own hosts are fetched.
func ytCaptionURL(base, tlang string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	if !u.IsAbs() {
		u = (&url.URL{Scheme: "https", Host: "www.youtube.com"}).ResolveReference(u)
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || (host != "youtube.com" && !strings.HasSuffix(host, ".youtube.com")) {
		return "", fmt.Errorf("unexpected caption host %q", host)
	}
	q := u.Query()
	q.Set("fmt", "vtt")
	if tlang != "" {
		q.Set("tlang", tlang)
	} else {
		q.Del("tlang")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ytNativeCaptionsFor is the fast path; tests swap it so nothing dials.
var ytNativeCaptionsFor = ytNativeCaptions

// ytNativeCaptions fetches lang subtitles for a video through the player
// call's caption list. An error sends the caller on to yt-dlp (errNoCaptions
// only when the user pressed for it); it is logged by the caller, never turned
// into a client cooldown. A client that answers OK with nothing for the
// language is the answer: the next client would list the same tracks.
func ytNativeCaptions(ctx context.Context, videoID, lang string) ([]subtitleCue, error) {
	if !ytNativeAllowed() {
		return nil, errors.New("the native client is cooling down")
	}
	var lastErr error
	for _, client := range ytNativeClients {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !ytNativeClientAllowed(client.name, time.Now()) {
			continue
		}
		player, err := ytPlayerCall(ctx, client, videoID)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", client.name, err)
			if errors.Is(err, errYtBootstrap) {
				break
			}
			continue
		}
		if status := player.PlayabilityStatus.Status; status != "OK" {
			reason := strings.TrimSpace(player.PlayabilityStatus.Reason)
			if reason == "" {
				reason = status
			}
			lastErr = fmt.Errorf("%s: %s", client.name, reason)
			continue
		}
		tracks, err := ytCaptionTracksOf(player)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", client.name, err)
			continue
		}
		if len(tracks) == 0 {
			return nil, fmt.Errorf("%s: the answer lists no caption tracks: %w", client.name, errNoCaptions)
		}
		choice, ok := ytChooseCaptionTrack(tracks, lang)
		if !ok {
			return nil, fmt.Errorf("%s: no track serves %q: %w", client.name, lang, errNoCaptions)
		}
		cues, err := ytFetchCaptionTrack(ctx, choice)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", client.name, err)
			continue
		}
		return cues, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no native client could be asked")
	}
	return nil, lastErr
}

// ytFetchCaptionTrack downloads one chosen track as WebVTT and parses it. An
// empty answer is a failure: it is how YouTube refuses a track it wants a
// proof-of-origin token for.
func ytFetchCaptionTrack(ctx context.Context, choice ytCaptionChoice) ([]subtitleCue, error) {
	raw, err := ytCaptionURL(choice.track.BaseURL, choice.tlang)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, ytCaptionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", streamUserAgent)
	req.Header.Set("Accept", "text/vtt,text/plain;q=0.9,*/*;q=0.5")
	resp, err := ytClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("caption fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caption fetch: HTTP %d", resp.StatusCode)
	}
	// One byte over the cap tells a whole answer from a cut one: a track cut
	// at the cap would be cached (and kept beside a download) as if complete.
	data, err := io.ReadAll(io.LimitReader(resp.Body, subtitleFileMax+1))
	if err != nil {
		return nil, fmt.Errorf("caption answer unreadable: %w", err)
	}
	if len(data) > subtitleFileMax {
		return nil, fmt.Errorf("caption answer over %d bytes", subtitleFileMax)
	}
	cues := parseSubtitles(string(data))
	if len(cues) == 0 {
		return nil, fmt.Errorf("empty caption answer (%d bytes): YouTube may want a proof-of-origin token", len(data))
	}
	return cues, nil
}

// ── Language codes ───────────────────────────────────────────────────────────

// subtitleLangCodeValid accepts a language code as yt-dlp and YouTube spell
// them: a letter first (a leading "-" is yt-dlp's syntax for "not this one"),
// then letters, digits and hyphens — and never "all", which yt-dlp reads as
// every language there is (hundreds of requests, and YouTube's limit).
func subtitleLangCodeValid(code string) bool {
	if code == "" || len(code) > 24 || strings.EqualFold(code, "all") {
		return false
	}
	for i, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// subtitleSidecarCodeValid is stricter, for the code in a file name: two or
// three letters first ("en", "fil"), then any subtags ("-US", "-orig"). A name
// like "Song.part1.srt" is not a language file.
func subtitleSidecarCodeValid(code string) bool {
	if !subtitleLangCodeValid(code) {
		return false
	}
	base := code
	if i := strings.IndexByte(code, '-'); i >= 0 {
		base = code[:i]
	}
	if len(base) < 2 || len(base) > 3 {
		return false
	}
	for _, r := range base {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// subtitleLanguageName is a code as the toasts say it: "English", "Español".
func subtitleLanguageName(code string) string {
	if strings.TrimSpace(code) == "" {
		return "English"
	}
	return setSubtitleLabel(code)
}

// ── SRT ──────────────────────────────────────────────────────────────────────

// subtitleFileMax bounds a subtitle file read from the card or the network: a
// feature film's track is a few hundred kilobytes.
const subtitleFileMax = 8 << 20

// srtTimestamp formats seconds the SubRip way: "01:02:03,450".
func srtTimestamp(sec float64) string {
	if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		sec = 0
	}
	ms := int64(math.Round(sec * 1000))
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}

// formatSRT writes cues as a SubRip file, the format every player reads. A
// blank line ends a block, so blank lines inside a cue are dropped, and an
// arrow inside the text is written plainly so it cannot pass for a timing.
func formatSRT(cues []subtitleCue) string {
	var b strings.Builder
	n := 0
	for _, c := range cues {
		if c.End <= c.Start {
			continue
		}
		var lines []string
		for _, line := range strings.Split(strings.ReplaceAll(c.Text, "-->", "->"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", n, srtTimestamp(c.Start), srtTimestamp(c.End), strings.Join(lines, "\n"))
	}
	return b.String()
}

// writeFileReplace writes data to path through a temporary file and a rename,
// so a reader never sees half a file. Unlike AtomicWrite it keeps no .bak: a
// cache entry or a sidecar is cheap to fetch again, and a backup beside a
// video would only be one more file to clean up. The temporary file has a
// name of its own (os.CreateTemp): the download worker and the player can
// store the same page's subtitles at the same moment, and a shared ".tmp"
// let one writer truncate the other's file under it.
func writeFileReplace(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		// CreateTemp makes the file private (0600); a subtitle file is not.
		// Best effort: a card that refuses mode changes (FAT, NTFS-3G, FUSE)
		// still keeps the file.
		if err := f.Chmod(0o644); err != nil {
			logDebug("subs", "could not open up %s: %v", filepath.Base(tmp), err)
		}
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ── Sidecars ─────────────────────────────────────────────────────────────────

// subtitleSidecar is one subtitle file beside a video, with the language code
// its name carries ("" for "<name>.srt").
type subtitleSidecar struct {
	path string
	lang string
}

// subtitleSidecarPath is where a video's subtitles in lang are saved: beside
// it as "<name>.<lang>.srt", which this player and every other one looks for.
func subtitleSidecarPath(videoPath, lang string) string {
	return strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + "." + lang + ".srt"
}

// subtitleSidecarFiles lists the subtitle files beside a video. The folder is
// read rather than globbed — titles are full of brackets, which a glob takes
// for a character class — and only "<name>.srt|vtt" and "<name>.<code>.srt|vtt"
// with a language-shaped code count, so the subtitles of "Song.en.mp4" are
// never taken for those of "Song.mp4". Names compare without case: the
// handheld's card is FAT, where "song.SRT" is the same file as "Song.srt".
func subtitleSidecarFiles(videoPath string) []subtitleSidecar {
	videoPath = strings.TrimSpace(videoPath)
	if videoPath == "" {
		return nil
	}
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	if base == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []subtitleSidecar
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := filepath.Ext(name)
		if lower := strings.ToLower(ext); lower != ".srt" && lower != ".vtt" {
			continue
		}
		stem := name[:len(name)-len(ext)]
		code := ""
		switch {
		case strings.EqualFold(stem, base):
		case len(stem) > len(base)+1 && strings.EqualFold(stem[:len(base)+1], base+"."):
			code = stem[len(base)+1:]
			if !subtitleSidecarCodeValid(code) {
				continue
			}
		default:
			continue
		}
		out = append(out, subtitleSidecar{path: filepath.Join(dir, name), lang: code})
	}
	return out
}

// subtitleSidecarRank orders the files beside a video for a wish: the wished
// language, a file named exactly like the video (its own subtitles), the same
// language's other regions, then anything else.
func subtitleSidecarRank(code, want string) int {
	if code == "" {
		return 1
	}
	switch subtitleLangScore(code, want) {
	case 3:
		return 0
	case 2:
		return 2
	case 1:
		return 3
	}
	return 4
}

// writeSubtitleSidecar saves cues beside a video. Only a video in the app's own
// download folder gets one — a user's own files (opened from Files) are never
// written next to, whichever caller asks. The video is checked before and
// after the write: a download removed while its subtitles were on the way must
// not leave an orphan .srt behind.
func writeSubtitleSidecar(videoPath, lang string, cues []subtitleCue) (string, error) {
	if len(cues) == 0 {
		return "", errors.New("no subtitles to save")
	}
	if !subtitleLangCodeValid(lang) {
		return "", fmt.Errorf("invalid subtitle language %q", lang)
	}
	if strings.TrimSpace(videoPath) == "" || !isPathWithin(videoPath, downloadsRoot()) {
		return "", errors.New("subtitles are only saved beside downloads")
	}
	if !regularFile(videoPath) {
		return "", errors.New("the video is not on the card")
	}
	path := subtitleSidecarPath(videoPath, lang)
	if err := writeFileReplace(path, []byte(formatSRT(cues))); err != nil {
		return "", err
	}
	if !regularFile(videoPath) {
		_ = os.Remove(path)
		return "", errors.New("the video was removed meanwhile")
	}
	return path, nil
}

// removeSubtitleSidecars deletes the subtitle files beside a removed download.
// Like every delete in the download code it only touches the app's own folder.
func removeSubtitleSidecars(videoPath string) {
	root := downloadsRoot()
	if strings.TrimSpace(videoPath) == "" || !isPathWithin(videoPath, root) {
		return
	}
	for _, s := range subtitleSidecarFiles(videoPath) {
		if isPathWithin(s.path, root) {
			_ = os.Remove(s.path)
		}
	}
}

// ── Cache ────────────────────────────────────────────────────────────────────

const (
	// subtitleCacheMaxFiles and subtitleCacheMaxAge bound the cache: a few
	// megabytes of text at most.
	subtitleCacheMaxFiles = 300
	subtitleCacheMaxAge   = 60 * 24 * time.Hour
)

// subtitleCacheDirOverride points the cache at a test folder.
var subtitleCacheDirOverride string

// subtitleCacheDir is the cache folder in the app's data folder, "" when the
// platform has none.
func subtitleCacheDir() string {
	if dir := strings.TrimSpace(subtitleCacheDirOverride); dir != "" {
		return dir
	}
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "subtitles")
}

// subtitleCacheKey names a page in the cache. A YouTube video is named by its
// id, so every link shape of one video shares a file; the short hash of the id
// keeps two ids that differ only in case apart on a FAT card. Any other page is
// a hash of its link.
func subtitleCacheKey(pageURL string) string {
	if id := youtubeVideoID(pageURL); id != "" {
		sum := sha256.Sum256([]byte(id))
		return "yt-" + id + "-" + hex.EncodeToString(sum[:2])
	}
	page := strings.TrimSpace(pageURL)
	if page == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(page))
	return "u-" + hex.EncodeToString(sum[:8])
}

// subtitleCachePath is the cache file for a page and a language, "" when
// either cannot name one.
func subtitleCachePath(pageURL, lang string) string {
	dir, key := subtitleCacheDir(), subtitleCacheKey(pageURL)
	if dir == "" || key == "" || !subtitleLangCodeValid(lang) {
		return ""
	}
	return filepath.Join(dir, key+"."+lang+".srt")
}

// subtitleSyncLoadMax is the largest cached track a CC press reads on the
// render thread (almost every track is far smaller); a bigger one is left to
// the fetch goroutine, which reads the cache first as well.
const subtitleSyncLoadMax = 512 << 10

// subtitleCacheHas reports a cached track for a page and a language.
func subtitleCacheHas(pageURL, lang string) bool {
	path := subtitleCachePath(pageURL, lang)
	return path != "" && regularFile(path)
}

// subtitleCacheSmall reports a cached track small enough to read on the spot.
func subtitleCacheSmall(pageURL, lang string) bool {
	path := subtitleCachePath(pageURL, lang)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= subtitleSyncLoadMax
}

// subtitleCacheLoadSmall is subtitleCacheLoad for a track small enough to read
// on the render thread, nil otherwise.
func subtitleCacheLoadSmall(pageURL, lang string) []subtitleCue {
	if !subtitleCacheSmall(pageURL, lang) {
		return nil
	}
	return subtitleCacheLoad(pageURL, lang)
}

// subtitleCacheLoad returns the cached cues for a page, or nil. A hit is
// touched, so the pruner keeps what is still being watched.
func subtitleCacheLoad(pageURL, lang string) []subtitleCue {
	path := subtitleCachePath(pageURL, lang)
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > subtitleFileMax {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cues := parseSubtitles(string(data))
	if len(cues) > 0 {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	}
	return cues
}

// subtitleCacheStore keeps fetched cues for a page, then trims the cache.
func subtitleCacheStore(pageURL, lang string, cues []subtitleCue) {
	path := subtitleCachePath(pageURL, lang)
	if path == "" || len(cues) == 0 {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logDebug("subs", "subtitle cache unavailable: %v", err)
		return
	}
	if err := writeFileReplace(path, []byte(formatSRT(cues))); err != nil {
		logDebug("subs", "subtitle cache write failed: %v", err)
		return
	}
	subtitleCachePrune(dir, subtitleCacheMaxFiles, subtitleCacheMaxAge, time.Now())
}

// subtitleCacheTmpAge is how old a temporary file in the cache must be before
// the pruner takes it for one a crash or a power cut left behind.
const subtitleCacheTmpAge = time.Hour

// subtitleCachePrune removes cache files older than maxAge, then the oldest
// until at most maxFiles remain, and temporary files a crash left behind. It
// returns how many it removed.
func subtitleCachePrune(dir string, maxFiles int, maxAge time.Duration, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	type cached struct {
		path string
		mod  time.Time
	}
	var files []cached
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".srt" && ext != ".tmp" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if ext == ".tmp" {
			if now.Sub(info.ModTime()) > subtitleCacheTmpAge && os.Remove(p) == nil {
				removed++
			}
			continue
		}
		if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			if os.Remove(p) == nil {
				removed++
			}
			continue
		}
		files = append(files, cached{path: p, mod: info.ModTime()})
	}
	if maxFiles > 0 && len(files) > maxFiles {
		sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
		for _, f := range files[:len(files)-maxFiles] {
			if os.Remove(f.path) == nil {
				removed++
			}
		}
	}
	return removed
}
