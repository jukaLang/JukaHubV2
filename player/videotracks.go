package main

// videotracks.go — audio and subtitle tracks of a video on the card.
//
// A film ripped with two languages, a commentary or a few subtitle tracks used
// to play only what ffmpeg picked by itself: its "best" audio stream and the
// first subtitle stream. The player's sheet (videosheet.go) now has an Audio
// track row and its Subtitles row steps through Off, the subtitle files beside
// the video and the text tracks inside it.
//
//   - The streams are read once per video with ffprobe (one header read, not
//     the whole file) when the sheet first asks, and kept while it plays.
//   - A new audio track restarts both decoders at the current position, the
//     way a speed change does (videoRestartAt): the sound decoder maps the
//     chosen stream (-map 0:a:N, runFFmpegAudio) and the picture decoder is
//     unchanged, so the A/V clock (avsync.go) works exactly as for a seek.
//   - A subtitle track needs no restart: its cues replace the loaded ones
//     (videotracks_subs.go). Picture subtitles (Blu-ray PGS, DVD, DVB) are
//     listed as not drawable and skipped: the player draws text.
//   - The choice is remembered per file in video-tracks.json in the data folder
//     (next to the other playback memory), so the film comes back with the
//     same language. The next video starts with ffmpeg's own pick again.
//
// Only a video on the card has tracks to pick: a stream's alternatives live in
// its page (the Quality row), and probing a link would cost a request.
//
// Up/Down are the volume during playback, so the tracks are switched from the
// sheet only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// videoAudioTrack is one audio stream of a file. Index is its place among the
// audio streams, which is ffmpeg's 0:a:Index.
type videoAudioTrack struct {
	Index    int
	Lang     string
	Title    string
	Codec    string
	Channels int
	Layout   string
	Default  bool
}

// videoSubTrack is one subtitle stream inside a file; Index is 0:s:Index.
// Bitmap marks a picture track the player cannot draw.
type videoSubTrack struct {
	Index   int
	Lang    string
	Title   string
	Codec   string
	Bitmap  bool
	Forced  bool
	Default bool
}

// videoTrackList is what ffprobe found in a file.
type videoTrackList struct {
	audio []videoAudioTrack
	subs  []videoSubTrack
}

// videoTracksProbeMax bounds ffprobe's answer: a stream list is a few KB.
const videoTracksProbeMax = 1 << 20

// videoTracksBitmapCodecs are the picture subtitle formats.
var videoTracksBitmapCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true, "pgssub": true, "dvd_subtitle": true, "dvdsub": true,
	"dvb_subtitle": true, "dvbsub": true, "xsub": true, "dvb_teletext": true,
}

// videoTracksProbeJSON is the part of ffprobe's JSON this file reads.
type videoTracksProbeJSON struct {
	Streams []struct {
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		Disposition   map[string]int    `json:"disposition"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
}

// videoTracksClean makes a tag from the file fit for one line of the sheet.
func videoTracksClean(s string, maxRunes int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f
	}), " ")
	if r := []rune(s); len(r) > maxRunes {
		s = strings.TrimSpace(string(r[:maxRunes]))
	}
	return s
}

// videoTracksTag reads a tag whatever its case (Matroska writes "language",
// some muxers "LANGUAGE").
func videoTracksTag(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// videoTracksParse reads ffprobe's -of json stream list. Audio and subtitle
// streams are numbered in file order within their kind, as ffmpeg's 0:a:N and
// 0:s:N count them. Pure: the file's tags are outside data.
func videoTracksParse(data []byte) (videoTrackList, error) {
	var list videoTrackList
	if len(data) > videoTracksProbeMax {
		return list, errors.New("ffprobe answer too large")
	}
	var probe videoTracksProbeJSON
	if err := json.Unmarshal(data, &probe); err != nil {
		return list, err
	}
	for _, s := range probe.Streams {
		lang := strings.ToLower(videoTracksClean(videoTracksTag(s.Tags, "language"), 12))
		if lang == "und" {
			lang = ""
		}
		title := videoTracksClean(videoTracksTag(s.Tags, "title"), 48)
		codec := strings.ToLower(videoTracksClean(s.CodecName, 24))
		switch s.CodecType {
		case "audio":
			if len(list.audio) >= 64 {
				continue
			}
			ch := s.Channels
			if ch < 0 || ch > 64 {
				ch = 0
			}
			list.audio = append(list.audio, videoAudioTrack{
				Index: len(list.audio), Lang: lang, Title: title, Codec: codec,
				Channels: ch, Layout: videoTracksClean(s.ChannelLayout, 24),
				Default: s.Disposition["default"] != 0,
			})
		case "subtitle":
			if len(list.subs) >= 64 {
				continue
			}
			list.subs = append(list.subs, videoSubTrack{
				Index: len(list.subs), Lang: lang, Title: title, Codec: codec,
				Bitmap: videoTracksBitmapCodecs[codec],
				Forced: s.Disposition["forced"] != 0, Default: s.Disposition["default"] != 0,
			})
		}
	}
	return list, nil
}

// videoTracksProbe asks ffprobe for a local file's streams.
func videoTracksProbe(ctx context.Context, ffprobePath, path string) (videoTrackList, error) {
	if ffprobePath == "" {
		return videoTrackList{}, errors.New("ffprobe is not available")
	}
	if !filepath.IsAbs(path) {
		return videoTrackList{}, errors.New("not a file on the card")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name,channels,channel_layout:stream_tags=language,title:stream_disposition=default,forced",
		"-of", "json", path)
	schedHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return videoTrackList{}, err
	}
	return videoTracksParse(out)
}

// ── The video on screen ──────────────────────────────────────────────────

// videoTracksState is what is known about a file's tracks.
type videoTracksState int

const (
	videoTracksLoading videoTracksState = iota
	videoTracksReady
	videoTracksFailed
)

// videoTracksMem keeps the probe of the video on screen (one file at a time)
// and the subtitle choice loaded for it.
var videoTracksMem struct {
	mu      sync.Mutex
	key     string // dlPathKey of the file
	state   videoTracksState
	list    videoTrackList
	sidecar []subtitleSidecar // the subtitle files beside it, read once
	done    chan struct{}     // closed when the probe ends
	subsID  string            // the subtitle choice loaded: "off", "file:<name>", "emb:N"; "" unknown
	// tempAudio is the audio choice (1-based) of a temporary file, which is
	// not remembered on disk.
	tempAudio int
}

// videoTracksLocalSource is the session's file when it is a video on the card
// (a file opened from Files or Downloads, or a stream downloaded first), "".
func videoTracksLocalSource(sess *playbackSession) string {
	if sess == nil || isNetworkSource(sess.source) || !filepath.IsAbs(sess.source) {
		return ""
	}
	return sess.source
}

// videoTracksFor returns what is known about path's tracks, starting the probe
// the first time it is asked. It never blocks: the sheet calls it every frame.
func videoTracksFor(path string) (videoTrackList, []subtitleSidecar, videoTracksState) {
	key := dlPathKey(path)
	videoTracksMem.mu.Lock()
	defer videoTracksMem.mu.Unlock()
	if videoTracksMem.key == key && videoTracksMem.done != nil {
		return videoTracksMem.list, videoTracksMem.sidecar, videoTracksMem.state
	}
	videoTracksStartLocked(path, key)
	return videoTrackList{}, nil, videoTracksLoading
}

// videoTracksStartLocked starts the probe of a new file. The caller holds
// videoTracksMem.mu.
func videoTracksStartLocked(path, key string) {
	done := make(chan struct{})
	videoTracksMem.key = key
	videoTracksMem.state = videoTracksLoading
	videoTracksMem.list = videoTrackList{}
	videoTracksMem.sidecar = nil
	videoTracksMem.done = done
	videoTracksMem.subsID = ""
	videoTracksMem.tempAudio = 0
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	ffprobePath := getToolPath("ffprobe", cfg)
	go func() {
		defer close(done)
		list, err := videoTracksProbe(context.Background(), ffprobePath, path)
		files := subtitleSidecarFiles(path)
		videoTracksMem.mu.Lock()
		defer videoTracksMem.mu.Unlock()
		if videoTracksMem.key != key || videoTracksMem.done != done {
			return // another video since
		}
		videoTracksMem.sidecar = files
		if err != nil {
			logDebug("video", "track probe of %s: %v", filepath.Base(path), err)
			videoTracksMem.state = videoTracksFailed
			return
		}
		videoTracksMem.list = list
		videoTracksMem.state = videoTracksReady
	}()
}

// videoTracksWait returns path's tracks, probing and waiting up to limit when
// they are not known yet. Decoder and loader goroutines only.
func videoTracksWait(path string, limit time.Duration) (videoTrackList, []subtitleSidecar, bool) {
	key := dlPathKey(path)
	videoTracksMem.mu.Lock()
	if videoTracksMem.key != key || videoTracksMem.done == nil {
		videoTracksStartLocked(path, key)
	}
	done := videoTracksMem.done
	videoTracksMem.mu.Unlock()
	select {
	case <-done:
	case <-time.After(limit):
		return videoTrackList{}, nil, false
	}
	videoTracksMem.mu.Lock()
	defer videoTracksMem.mu.Unlock()
	if videoTracksMem.key != key || videoTracksMem.state != videoTracksReady {
		return videoTrackList{}, videoTracksMem.sidecar, false
	}
	return videoTracksMem.list, videoTracksMem.sidecar, true
}

// ── Remembered choices ───────────────────────────────────────────────────

// videoTrackChoice is what was picked for one file. Audio is 1-based (0 is
// ffmpeg's own pick); Subs is "", "off", "file:<name>" or "emb:N".
type videoTrackChoice struct {
	Audio int    `json:"a,omitempty"`
	Subs  string `json:"s,omitempty"`
	Seq   uint64 `json:"q,omitempty"` // a running count: the oldest go first
}

// videoTrackStoreFile is the per-file memory, in the data folder.
const videoTrackStoreFile = "video-tracks.json"

// videoTrackStoreMax bounds the memory.
const videoTrackStoreMax = 400

type videoTrackStoreData struct {
	Seq    uint64                      `json:"seq"`
	Videos map[string]videoTrackChoice `json:"videos"`
}

var videoTrackStore struct {
	mu     sync.Mutex
	loaded bool
	data   videoTrackStoreData
}

// videoTrackStorePath is overridden by tests.
var videoTrackStorePath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, videoTrackStoreFile)
}

func videoTrackStoreLoadLocked() {
	if videoTrackStore.loaded {
		return
	}
	videoTrackStore.loaded = true
	var d videoTrackStoreData
	if _, err := readJSONWithBackup(videoTrackStorePath(), &d); err != nil {
		if !os.IsNotExist(err) {
			logWarn("video", "ignoring unreadable %s: %v", videoTrackStoreFile, err)
		}
	}
	if d.Videos == nil {
		d.Videos = map[string]videoTrackChoice{}
	}
	for k, c := range d.Videos {
		if c.Audio < 0 || c.Audio > 64 || !videoTrackSubsIDValid(c.Subs) || k == "" {
			delete(d.Videos, k)
		}
	}
	videoTrackStore.data = d
}

// videoTrackSubsIDValid accepts the subtitle choice forms, and nothing a
// hand-edited file could turn into a path outside the video's folder.
func videoTrackSubsIDValid(id string) bool {
	switch {
	case id == "" || id == "off":
		return true
	case strings.HasPrefix(id, "emb:"):
		n, err := strconv.Atoi(id[4:])
		return err == nil && n >= 0 && n < 64
	case strings.HasPrefix(id, "file:"):
		name := id[5:]
		return name != "" && len(name) <= 255 && !strings.ContainsAny(name, `/\`) && name != "." && name != ".."
	}
	return false
}

// videoTrackChoiceFor is the choice remembered for path.
func videoTrackChoiceFor(path string) videoTrackChoice {
	if path == "" {
		return videoTrackChoice{}
	}
	videoTrackStore.mu.Lock()
	defer videoTrackStore.mu.Unlock()
	videoTrackStoreLoadLocked()
	return videoTrackStore.data.Videos[dlPathKey(path)]
}

// videoTrackRemember stores a change to path's choice and writes the file in
// the background.
func videoTrackRemember(path string, change func(c *videoTrackChoice)) {
	if path == "" || change == nil {
		return
	}
	key := dlPathKey(path)
	videoTrackStore.mu.Lock()
	videoTrackStoreLoadLocked()
	c := videoTrackStore.data.Videos[key]
	change(&c)
	videoTrackStore.data.Seq++
	c.Seq = videoTrackStore.data.Seq
	if c.Audio == 0 && c.Subs == "" {
		delete(videoTrackStore.data.Videos, key)
	} else {
		videoTrackStore.data.Videos[key] = c
	}
	if len(videoTrackStore.data.Videos) > videoTrackStoreMax {
		type aged struct {
			key string
			seq uint64
		}
		all := make([]aged, 0, len(videoTrackStore.data.Videos))
		for k, v := range videoTrackStore.data.Videos {
			all = append(all, aged{k, v.Seq})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
		for _, a := range all[:len(all)-videoTrackStoreMax] {
			delete(videoTrackStore.data.Videos, a.key)
		}
	}
	data, err := json.Marshal(videoTrackStore.data)
	seq := videoTrackStore.data.Seq
	videoTrackStore.mu.Unlock()
	if err != nil {
		return
	}
	go func() {
		videoTrackStoreWriteMu.Lock()
		defer videoTrackStoreWriteMu.Unlock()
		// Two quick presses start two writers in either order: the older
		// snapshot never lands over the newer one.
		if seq <= videoTrackStoreWritten {
			return
		}
		if err := AtomicWrite(videoTrackStorePath(), data, 0o644); err != nil {
			logWarn("video", "saving %s: %v", videoTrackStoreFile, err)
			return
		}
		videoTrackStoreWritten = seq
	}()
}

// videoTrackStoreWriteMu serialises the background writes, and
// videoTrackStoreWritten (under it) is the Seq of the snapshot on disk.
var (
	videoTrackStoreWriteMu sync.Mutex
	videoTrackStoreWritten uint64
)

// ── Audio ────────────────────────────────────────────────────────────────

// videoTracksAudioMapArgs is the sound decoder's stream choice for a session:
// "-map 0:a:N" when an audio track was picked for this file and the file has
// it, else nothing (ffmpeg's own pick). Decoder goroutine (runFFmpegAudio).
func videoTracksAudioMapArgs(sess *playbackSession) []string {
	path := videoTracksLocalSource(sess)
	if path == "" {
		return nil
	}
	audio := videoTracksAudioChoice(path)
	if audio <= 0 {
		return nil
	}
	// The probe is normally done (the sheet asked it); a remembered choice at
	// the start of a film waits for it here, briefly — the picture waits for
	// the sound's first buffer, so the two still start together.
	list, _, ok := videoTracksWait(path, 6*time.Second)
	if !ok || audio > len(list.audio) {
		return nil
	}
	return []string{"-map", "0:a:" + strconv.Itoa(audio-1)}
}

// videoTracksAutoAudio is the stream ffmpeg picks by itself: the default one,
// else the most channels, the first among equals.
func videoTracksAutoAudio(list videoTrackList) int {
	best, bestScore := 0, -1
	for i, a := range list.audio {
		score := a.Channels
		if a.Default {
			score += 1000
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// videoTracksCurrentAudio is the audio stream playing for path (0-based).
func videoTracksCurrentAudio(path string, list videoTrackList) int {
	if a := videoTracksAudioChoice(path); a > 0 && a <= len(list.audio) {
		return a - 1
	}
	return videoTracksAutoAudio(list)
}

// videoTracksLangName names a stream's language code ("eng", "de", "pt-BR").
func videoTracksLangName(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if name := subtitleLanguageName(videoTracksLang2(code)); name != "" && !strings.EqualFold(name, code) {
		return name
	}
	return strings.ToUpper(code)
}

// videoTracksLang2 maps the common three-letter codes Matroska writes to the
// two-letter ones the subtitle language names know.
func videoTracksLang2(code string) string {
	switch strings.ToLower(code) {
	case "eng":
		return "en"
	case "ger", "deu":
		return "de"
	case "fre", "fra":
		return "fr"
	case "spa":
		return "es"
	case "ita":
		return "it"
	case "por":
		return "pt"
	case "rus":
		return "ru"
	case "jpn":
		return "ja"
	case "chi", "zho":
		return "zh"
	case "kor":
		return "ko"
	case "dut", "nld":
		return "nl"
	case "pol":
		return "pl"
	case "swe":
		return "sv"
	case "nor":
		return "no"
	case "dan":
		return "da"
	case "fin":
		return "fi"
	case "tur":
		return "tr"
	case "ara":
		return "ar"
	case "hin":
		return "hi"
	case "ukr":
		return "uk"
	case "cze", "ces":
		return "cs"
	case "gre", "ell":
		return "el"
	case "heb":
		return "he"
	case "hun":
		return "hu"
	}
	return code
}

// videoTracksChannelsLabel is "stereo", "5.1", "mono" or "6 ch".
func videoTracksChannelsLabel(a videoAudioTrack) string {
	layout := strings.ToLower(a.Layout)
	switch {
	case strings.HasPrefix(layout, "5.1"):
		return "5.1"
	case strings.HasPrefix(layout, "7.1"):
		return "7.1"
	case layout == "stereo" || a.Channels == 2:
		return "stereo"
	case layout == "mono" || a.Channels == 1:
		return "mono"
	case a.Channels > 0:
		return strconv.Itoa(a.Channels) + " ch"
	}
	return ""
}

// videoTracksAudioLabel is one audio track for the sheet: "2/3 · English ·
// Commentary · AAC 5.1".
func videoTracksAudioLabel(a videoAudioTrack, total int) string {
	parts := []string{fmt.Sprintf("%d/%d", a.Index+1, total)}
	if l := videoTracksLangName(a.Lang); l != "" {
		parts = append(parts, l)
	}
	if a.Title != "" {
		parts = append(parts, a.Title)
	}
	tech := strings.ToUpper(a.Codec)
	if ch := videoTracksChannelsLabel(a); ch != "" {
		tech = strings.TrimSpace(tech + " " + ch)
	}
	if tech != "" {
		parts = append(parts, tech)
	}
	return strings.Join(parts, " · ")
}

// videoTracksAudioRowLabel is the sheet's Audio track row.
func videoTracksAudioRowLabel() string {
	path := videoTracksLocalSource(playbackCurrentSession())
	if path == "" {
		return "Audio track: Default"
	}
	list, _, state := videoTracksFor(path)
	switch {
	case state == videoTracksLoading:
		return "Audio track: Looking…"
	case state == videoTracksFailed:
		return "Audio track: Default"
	case len(list.audio) == 0:
		return "Audio track: None"
	}
	return "Audio track: " + videoTracksAudioLabel(list.audio[videoTracksCurrentAudio(path, list)], len(list.audio))
}

// videoTracksCycleAudio is a press on the Audio track row: the next audio
// stream of the file, from the current position. Render thread.
func videoTracksCycleAudio() {
	sess := playbackCurrentSession()
	path := videoTracksLocalSource(sess)
	if path == "" {
		ShowToast("Audio tracks can be picked for videos on the card", ToastKindInfo)
		return
	}
	list, _, state := videoTracksFor(path)
	switch {
	case state == videoTracksLoading:
		ShowToast("Reading the video's tracks…", ToastKindInfo)
		return
	case state == videoTracksFailed:
		ShowToast("Could not read this video's tracks", ToastKindWarning)
		return
	case len(list.audio) == 0:
		ShowToast("This video has no sound", ToastKindInfo)
		return
	case len(list.audio) == 1:
		ShowToast("This video has one audio track", ToastKindInfo)
		return
	}
	next := (videoTracksCurrentAudio(path, list) + 1) % len(list.audio)
	if sess.local {
		videoTrackRemember(path, func(c *videoTrackChoice) { c.Audio = next + 1 })
	} else {
		// A stream downloaded first plays from a temporary file: the choice
		// holds for this play only (a session entry, not the store).
		videoTracksSessionAudio(path, next+1)
	}
	state2 := snapshotVideoState()
	if !videoRestartAt(sess, state2.progress, 0, true) {
		ShowToast("The audio track changes when the video plays again", ToastKindInfo)
		return
	}
	ShowToast("Audio: "+videoTracksAudioLabel(list.audio[next], len(list.audio)), ToastKindInfo)
}

// videoTracksSessionAudio remembers a temporary file's audio choice in memory
// only: such a file is deleted or moved when it closes.
func videoTracksSessionAudio(path string, audio int) {
	key := dlPathKey(path)
	videoTracksMem.mu.Lock()
	defer videoTracksMem.mu.Unlock()
	if videoTracksMem.key == key {
		videoTracksMem.tempAudio = audio
	}
}

// videoTracksAudioChoice is the 1-based audio stream picked for path: the
// remembered one, else this play's (a temporary file), else 0.
func videoTracksAudioChoice(path string) int {
	if c := videoTrackChoiceFor(path); c.Audio > 0 {
		return c.Audio
	}
	key := dlPathKey(path)
	videoTracksMem.mu.Lock()
	defer videoTracksMem.mu.Unlock()
	if videoTracksMem.key == key {
		return videoTracksMem.tempAudio
	}
	return 0
}
