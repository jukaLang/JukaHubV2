package main

// videotracks_subs.go — subtitle choice for a video on the card, and the
// subtitle file formats besides SubRip and WebVTT.
//
// Beside a video the player now also reads Advanced SubStation (.ass/.ssa:
// the Dialogue lines' start, end and text; styles, positions and karaoke are
// dropped, \N is a line break) and MicroDVD (.sub: "{start}{end}text" in
// frames, turned into seconds with the file's own frame rate line or the
// video's; "|" is a line break). A language suffix works as for .srt
// ("movie.en.ass"). A VobSub .sub (pictures, with an .idx) has no text lines
// and is passed over.
//
// The sheet's Subtitles row steps through Off, each file beside the video and
// each text track inside it when there is more than one (videotracks.go);
// with one or none it is the plain CC switch it always was.

import (
	"context"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// subtitleSidecarExt reports a subtitle file extension the player reads
// (ytcaptions.go's sidecar search).
func subtitleSidecarExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".srt", ".vtt", ".ass", ".ssa", ".sub":
		return true
	}
	return false
}

// subtitleFPSHint is the frame rate of the video on screen, for a MicroDVD
// file without its own (float64 bits; 0 = unknown).
var subtitleFPSHint atomic.Uint64

// parseSubtitleFile parses a subtitle file's text by its extension
// (readSubtitleCues): SubStation, MicroDVD, else SubRip/WebVTT.
func parseSubtitleFile(path, data string) []subtitleCue {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ass", ".ssa":
		return parseASSSubtitles(data)
	case ".sub":
		return parseMicroDVDSubtitles(data, math.Float64frombits(subtitleFPSHint.Load()))
	}
	return parseSubtitles(data)
}

// parseASSSubtitles reads the Dialogue lines of an .ass/.ssa file. The
// [Events] Format line says where Start, End and Text are; without one the
// standard order is assumed. Override blocks ({\i1}, {\pos(…)}) are stripped,
// \N and \n break the line, \h is a space, and a drawing ({\p1}…) is skipped.
// Cues are sorted by start.
func parseASSSubtitles(data string) []subtitleCue {
	data = strings.TrimPrefix(data, "\ufeff")
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	startIdx, endIdx, textIdx, fields := 1, 2, 9, 10
	inEvents := false
	var cues []subtitleCue
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inEvents = strings.EqualFold(line, "[Events]")
			continue
		}
		if !inEvents {
			continue
		}
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "format":
			names := strings.Split(rest, ",")
			s, e, t := -1, -1, -1
			for i, n := range names {
				switch strings.ToLower(strings.TrimSpace(n)) {
				case "start":
					s = i
				case "end":
					e = i
				case "text":
					t = i
				}
			}
			// Text is the last field (it may hold commas); a Format line that
			// says otherwise is not trusted.
			if s >= 0 && e >= 0 && t == len(names)-1 {
				startIdx, endIdx, textIdx, fields = s, e, t, len(names)
			}
		case "dialogue":
			parts := strings.SplitN(rest, ",", fields)
			if len(parts) != fields {
				continue
			}
			start, okS := parseSubtitleTimestamp(parts[startIdx])
			end, okE := parseSubtitleTimestamp(parts[endIdx])
			if !okS || !okE || end <= start {
				continue
			}
			text, drawing := assPlainText(parts[textIdx])
			if drawing || text == "" {
				continue
			}
			cues = append(cues, subtitleCue{Start: start, End: end, Text: text})
		}
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
	return cues
}

// assPlainText turns one Dialogue text into plain lines, and reports a vector
// drawing (\p1 or more), which is not text at all.
func assPlainText(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '{' {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				break
			}
			block := s[i+1 : i+end]
			if assDrawingOn(block) {
				return "", true
			}
			i += end
			continue
		}
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'N', 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'h':
				b.WriteByte(' ')
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		if l = cleanSubtitleText(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), false
}

// assDrawingOn reports an override block that turns drawing mode on ("\p1").
func assDrawingOn(block string) bool {
	for {
		j := strings.Index(block, `\p`)
		if j < 0 {
			return false
		}
		block = block[j+2:]
		if block != "" && block[0] >= '1' && block[0] <= '9' {
			return true
		}
	}
}

// microDVDDefaultFPS is the rate assumed when neither the file nor the video
// says: most MicroDVD files were made for 23.976 fps releases.
const microDVDDefaultFPS = 23.976

// parseMicroDVDSubtitles reads a MicroDVD file: "{start}{end}text" with frame
// numbers, "|" between lines and {y:i}-style codes stripped. A first line
// "{1}{1}23.976" gives the file's own frame rate, which wins over fps (the
// video's; 0 when unknown). An empty end ("{100}{}") lasts three seconds.
func parseMicroDVDSubtitles(data string, fps float64) []subtitleCue {
	data = strings.TrimPrefix(data, "\ufeff")
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	if fps <= 1 || fps > 240 || math.IsNaN(fps) {
		fps = microDVDDefaultFPS
	}
	var cues []subtitleCue
	first := true
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		start, end, text, ok := microDVDLine(line)
		if !ok {
			first = false
			continue
		}
		if first {
			first = false
			if start <= 1 && end <= 1 {
				if v, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil && v > 1 && v <= 240 {
					fps = v
					continue
				}
			}
		}
		startSec := float64(start) / fps
		endSec := startSec + 3
		if end > start {
			endSec = float64(end) / fps
		}
		var lines []string
		for _, l := range strings.Split(text, "|") {
			if l = cleanSubtitleText(l); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) == 0 {
			continue
		}
		cues = append(cues, subtitleCue{Start: startSec, End: endSec, Text: strings.Join(lines, "\n")})
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
	return cues
}

// microDVDLine splits "{start}{end}text"; end is -1 when empty.
func microDVDLine(line string) (start, end int, text string, ok bool) {
	num := func(s string) (int, string, bool) {
		if !strings.HasPrefix(s, "{") {
			return 0, s, false
		}
		closeAt := strings.IndexByte(s, '}')
		if closeAt < 0 || closeAt > 12 {
			return 0, s, false
		}
		field := strings.TrimSpace(s[1:closeAt])
		if field == "" {
			return -1, s[closeAt+1:], true
		}
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return 0, s, false
		}
		return n, s[closeAt+1:], true
	}
	start, rest, ok1 := num(line)
	if !ok1 || start < 0 {
		return 0, 0, "", false
	}
	end, rest, ok2 := num(rest)
	if !ok2 {
		return 0, 0, "", false
	}
	return start, end, rest, true
}

// ── Embedded tracks ──────────────────────────────────────────────────────

// extractEmbeddedSubtitleTrack pulls subtitle stream n (0:s:n) out of a local
// file as cues, like extractEmbeddedSubtitles does for the first one.
func extractEmbeddedSubtitleTrack(ctx context.Context, ffmpegPath, videoPath string, n int) []subtitleCue {
	if ffmpegPath == "" || n < 0 || !filepath.IsAbs(videoPath) {
		return nil
	}
	if st, err := os.Stat(videoPath); err != nil || st.IsDir() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-v", "error", "-i", videoPath, "-map", "0:s:"+strconv.Itoa(n), "-f", "srt", "-")
	schedHideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, int64(subtitleFileMax)+1))
	tooLarge := len(out) > subtitleFileMax
	if tooLarge && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || tooLarge || waitErr != nil || len(out) == 0 {
		return nil
	}
	return parseSubtitles(string(out))
}

// ── Choices ──────────────────────────────────────────────────────────────

// videoSubsOption is one stop of the Subtitles row.
type videoSubsOption struct {
	id    string // "off", "file:<name>", "emb:N"
	label string
	file  string // the sidecar's path
	track int    // the embedded stream
}

// videoSubsOptions lists the row's stops for a file: Off, the files beside
// it (the wished language first), then its text tracks.
func videoSubsOptions(list videoTrackList, files []subtitleSidecar) []videoSubsOption {
	opts := []videoSubsOption{{id: "off", label: "Off"}}
	lang := ""
	if l, err := subtitleLanguage(appConfig); err == nil {
		lang = l
	}
	files = append([]subtitleSidecar(nil), files...)
	sort.SliceStable(files, func(i, j int) bool {
		return subtitleSidecarRank(files[i].lang, lang) < subtitleSidecarRank(files[j].lang, lang)
	})
	for _, f := range files {
		name := filepath.Base(f.path)
		label := strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), ".")) + " file"
		if f.lang != "" {
			label = videoTracksLangName(f.lang) + " · " + label
		}
		opts = append(opts, videoSubsOption{id: "file:" + name, label: label, file: f.path})
	}
	for _, t := range list.subs {
		if t.Bitmap {
			continue
		}
		parts := []string{"Track " + strconv.Itoa(t.Index+1)}
		if l := videoTracksLangName(t.Lang); l != "" {
			parts = append(parts, l)
		}
		if t.Title != "" {
			parts = append(parts, t.Title)
		} else if t.Forced {
			parts = append(parts, "Forced")
		}
		opts = append(opts, videoSubsOption{id: "emb:" + strconv.Itoa(t.Index), label: strings.Join(parts, " · "), track: t.Index})
	}
	return opts
}

// videoTracksBitmapCount is how many picture tracks a file has.
func videoTracksBitmapCount(list videoTrackList) int {
	n := 0
	for _, t := range list.subs {
		if t.Bitmap {
			n++
		}
	}
	return n
}

// videoTracksSubsChoice is the subtitle choice in force for the video on
// screen: the one loaded (or remembered), "" when only the start's own pick is
// loaded.
func videoTracksSubsChoice(path string) string {
	key := dlPathKey(path)
	videoTracksMem.mu.Lock()
	id := ""
	if videoTracksMem.key == key {
		id = videoTracksMem.subsID
	}
	videoTracksMem.mu.Unlock()
	return id
}

// videoTracksSetSubsChoice records the loaded choice for path.
func videoTracksSetSubsChoice(path, id string) {
	key := dlPathKey(path)
	videoTracksMem.mu.Lock()
	if videoTracksMem.key == key {
		videoTracksMem.subsID = id
	}
	videoTracksMem.mu.Unlock()
}

// videoTracksSubsRowLabel is the Subtitles row for a file with more than one
// subtitle source; ok is false otherwise (the plain On/Off row stays).
func videoTracksSubsRowLabel() (string, bool) {
	path := videoTracksLocalSource(playbackCurrentSession())
	if path == "" {
		return "", false
	}
	list, files, state := videoTracksFor(path)
	if state == videoTracksLoading {
		return "", false
	}
	opts := videoSubsOptions(list, files)
	if len(opts) < 3 {
		return "", false
	}
	if !subtitlesEnabled() {
		return "Subtitles: Off", true
	}
	id := videoTracksSubsChoice(path)
	for _, o := range opts[1:] {
		if o.id == id {
			return "Subtitles: " + o.label, true
		}
	}
	// The start's own pick (videoTracksStartSubs did not record one): the
	// loaded track's source, as the status line says it.
	return "Subtitles: On", true
}

// videoTracksSubsRowPress is a press on the Subtitles row. It steps to the
// next subtitle source and reports true; false leaves the press to the CC
// switch (a stream, or a file with one source or none). Render thread.
func videoTracksSubsRowPress() bool {
	sess := playbackCurrentSession()
	path := videoTracksLocalSource(sess)
	if path == "" {
		return false
	}
	list, files, state := videoTracksFor(path)
	if state == videoTracksLoading {
		ShowToast("Reading the video's tracks…", ToastKindInfo)
		return true
	}
	opts := videoSubsOptions(list, files)
	if len(opts) < 3 {
		if len(opts) == 1 && videoTracksBitmapCount(list) > 0 {
			ShowToast("This video's subtitles are pictures (Blu-ray or DVD), which the player cannot draw", ToastKindInfo)
			return true
		}
		return false
	}
	videoSubs.mu.Lock()
	owner, busy := videoSubs.owner, videoSubs.preparing
	videoSubs.mu.Unlock()
	if owner == nil {
		return false
	}
	if busy {
		ShowToast("Still reading this video's subtitles…", ToastKindInfo)
		return true
	}
	cur := 0
	if subtitlesEnabled() {
		cur = -1
		id := videoTracksSubsChoice(path)
		for i, o := range opts {
			if i > 0 && o.id == id {
				cur = i
				break
			}
		}
	}
	next := opts[(cur+1+len(opts))%len(opts)]
	if cur < 0 {
		// Unknown pick on screen: the first source after it is the start's
		// own pick in most files, so step to the second one.
		next = opts[min(2, len(opts)-1)]
	}
	videoTracksApplySubs(sess, owner, path, next, true)
	return true
}

// videoTracksApplySubs loads one subtitle stop for the video on screen and
// remembers it when remember is set. Off is instant; a file or a track loads
// on a goroutine (a track means ffmpeg reading the whole file).
func videoTracksApplySubs(sess, owner *playbackSession, path string, opt videoSubsOption, remember bool) {
	if remember && sess != nil && sess.local {
		videoTrackRemember(path, func(c *videoTrackChoice) { c.Subs = opt.id })
	}
	if opt.id == "off" {
		videoSubs.mu.Lock()
		if videoSubs.owner == owner {
			videoSubs.enabled = false
		}
		videoSubs.mu.Unlock()
		videoTracksSetSubsChoice(path, "off")
		ShowToast("Subtitles off", ToastKindInfo)
		return
	}
	if opt.file == "" {
		ShowToast("Loading "+opt.label+"…", ToastKindInfo)
	}
	videoSubs.mu.Lock()
	ctx := videoSubs.videoCtx
	videoSubs.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	ffmpegPath := getToolPath("ffmpeg", cfg)
	go func() {
		cues := videoTracksLoadOption(ctx, ffmpegPath, path, opt)
		if len(cues) == 0 {
			if ctx.Err() == nil {
				ShowToast("Could not read "+opt.label, ToastKindWarning)
			}
			return
		}
		if !videoTracksInstallCues(owner, cues, opt.label) {
			return
		}
		videoTracksSetSubsChoice(path, opt.id)
		ShowToast("Subtitles: "+opt.label, ToastKindInfo)
	}()
}

// videoTracksLoadOption reads one stop's cues.
func videoTracksLoadOption(ctx context.Context, ffmpegPath, path string, opt videoSubsOption) []subtitleCue {
	if opt.file != "" {
		cues, err := readSubtitleCues(opt.file)
		if err != nil {
			return nil
		}
		return cues
	}
	return extractEmbeddedSubtitleTrack(ctx, ffmpegPath, path, opt.track)
}

// videoTracksInstallCues puts cues on screen for the video owner started,
// with CC on; a running fetch for the file's web page stands down. It reports
// false when another video took over meanwhile.
func videoTracksInstallCues(owner *playbackSession, cues []subtitleCue, label string) bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if owner == nil || videoSubs.owner != owner {
		return false
	}
	if videoSubs.cancel != nil {
		videoSubs.cancel()
		videoSubs.cancel = nil
	}
	if videoSubs.fetching {
		videoSubs.fetching = false
		videoSubs.fetchNotify = false
		videoSubs.request++
	}
	videoSubs.cues = cues
	videoSubs.status = label
	videoSubs.language = ""
	videoSubs.enabled = true
	videoSubs.pressed = true
	return true
}

// videoTracksStartSubs loads a new local video's subtitles at its start
// (prepareVideoSubtitles): the choice remembered for the file when it is still
// there, else the best file beside it, else its first text track (which may
// not be stream 0: a Blu-ray rip often has picture tracks first). searched
// reports that the file's own tracks were looked at, so the caller does not
// read the whole file again for stream 0. A remembered Off turns CC off for
// this video; the default pick still loads, so CC can bring it back.
func videoTracksStartSubs(ctx context.Context, sess *playbackSession, config *Config, source, lang string) (cues []subtitleCue, label string, searched bool) {
	embeddedPlayer.mu.Lock()
	fps := embeddedPlayer.frameRate
	embeddedPlayer.mu.Unlock()
	subtitleFPSHint.Store(math.Float64bits(fps))
	path := ""
	if sess != nil && !isNetworkSource(sess.source) && filepath.IsAbs(source) {
		path = source
	}
	if path == "" {
		cues, label = loadSidecarSubtitles(source, lang)
		return cues, label, false
	}
	// The probe starts now either way (the sheet asks for it later); only a
	// remembered choice, or a file with no subtitle file beside it, waits.
	videoTracksFor(path)
	ffmpegPath := ""
	if config != nil {
		ffmpegPath = getToolPath("ffmpeg", config)
	}
	if want := videoTrackChoiceFor(path).Subs; sess.local && want != "" {
		list, files, _ := videoTracksWait(path, 10*time.Second)
		for _, o := range videoSubsOptions(list, files) {
			if o.id != want {
				continue
			}
			if o.id == "off" {
				// Off for this video; the default pick below still loads.
				videoSubs.mu.Lock()
				if videoSubs.owner == sess {
					videoSubs.enabled = false
				}
				videoSubs.mu.Unlock()
				break
			}
			if c := videoTracksLoadOption(ctx, ffmpegPath, path, o); len(c) > 0 {
				videoTracksSetSubsChoice(path, o.id)
				return c, o.label, true
			}
			break
		}
	}
	cues, label = loadSidecarSubtitles(source, lang)
	if len(cues) > 0 {
		videoTracksSetSubsChoice(path, "file:"+label)
		return cues, label, false
	}
	list, _, ok := videoTracksWait(path, 10*time.Second)
	if !ok {
		return nil, "", false
	}
	for _, t := range list.subs {
		if t.Bitmap {
			continue
		}
		if c := extractEmbeddedSubtitleTrack(ctx, ffmpegPath, path, t.Index); len(c) > 0 {
			videoTracksSetSubsChoice(path, "emb:"+strconv.Itoa(t.Index))
			return c, "inside the video", true
		}
		break
	}
	return nil, "", true
}
