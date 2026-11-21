package main

// lyrics_embedded.go — lyrics stored inside the audio file, and .lrc files
// without timings.
//
// The player showed lyrics only from a timed .lrc beside the song. Many files
// carry their lyrics inside: an ID3 USLT frame (ffprobe calls it "lyrics-eng"
// or "lyrics"), a FLAC/Vorbis LYRICS or UNSYNCEDLYRICS comment, an MP4 ©lyr
// atom ("lyrics"). When the song has no timed .lrc, the track's tags are read
// once with ffprobe as it starts playing:
//
//   - text with LRC timestamps ("[01:02.50]…", which taggers write into the
//     same tag) is parsed as a synced .lrc (parseLRC);
//   - plain text — and an .lrc that has no timestamps at all, which parseLRC
//     drops — scrolls through the song: its lines are spread evenly over the
//     track's length, so the lyrics tile moves along with the music, roughly.
//
// ID3 SYLT (synchronised lyrics) frames are not exported by ffprobe, so they
// are not read. Radio, podcasts and soundtracks (chiptune.go) are skipped.

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lyricsEmbeddedMax bounds the lyrics text kept from one file.
const lyricsEmbeddedMax = 64 << 10

// lyricsEmbeddedLines bounds the lines of plain lyrics.
const lyricsEmbeddedLines = 400

// lyricsEmbeddedCache remembers what the last files carried (or that they
// carry nothing), so a replay does not run ffprobe again.
var lyricsEmbeddedCache struct {
	mu      sync.Mutex
	entries map[string]lyricsEmbeddedEntry
}

type lyricsEmbeddedEntry struct {
	mod, size int64
	text      string
	duration  float64
}

// lyricsEmbeddedProbe is what ffprobe -of json says about a file's tags.
type lyricsEmbeddedProbe struct {
	Streams []struct {
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

// lyricsTagKey reports a tag name that holds lyrics: "lyrics", "lyrics-eng",
// "UNSYNCEDLYRICS", "unsynced lyrics", "©lyr", "USLT".
func lyricsTagKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "lyrics" || k == "unsyncedlyrics" || k == "unsynced lyrics" || k == "unsynced_lyrics" ||
		k == "©lyr" || k == "uslt" || k == "syncedlyrics":
		return true
	case strings.HasPrefix(k, "lyrics-") || strings.HasPrefix(k, "lyrics_"):
		return true
	}
	return false
}

// lyricsFromProbe picks the lyrics out of ffprobe's JSON answer, with the
// duration it reports: a tag with LRC timings first, then any other. Pure:
// the tags are outside data.
func lyricsFromProbe(data []byte) (string, float64) {
	var p lyricsEmbeddedProbe
	if len(data) > 4<<20 || json.Unmarshal(data, &p) != nil {
		return "", 0
	}
	dur, _ := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64)
	if dur < 0 || dur > 7*24*3600 {
		dur = 0
	}
	var found []string
	collect := func(tags map[string]string) {
		for k, v := range tags {
			if lyricsTagKey(k) && strings.TrimSpace(v) != "" {
				found = append(found, v)
			}
		}
	}
	collect(p.Format.Tags)
	for _, s := range p.Streams {
		collect(s.Tags)
	}
	best := ""
	for _, v := range found {
		if len(parseLRC(lyricsCap(v))) > 0 {
			return lyricsCap(v), dur
		}
		if len(v) > len(best) {
			best = v
		}
	}
	return lyricsCap(best), dur
}

// lyricsCap cuts text to lyricsEmbeddedMax at a line end.
func lyricsCap(s string) string {
	if len(s) <= lyricsEmbeddedMax {
		return s
	}
	s = s[:lyricsEmbeddedMax]
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

// lyricsFromText turns lyrics text into timed lines: synced when it has LRC
// timings, else its lines spread from 4% to 92% of the track (4 s a line while
// the length is unknown). Pure.
func lyricsFromText(text string, duration float64) []lyricLine {
	if lines := parseLRC(text); len(lines) > 0 {
		return lines
	}
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var plain []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.Join(strings.FieldsFunc(raw, func(r rune) bool {
			return r == ' ' || r == '\t' || r < 0x20 || r == 0x7f
		}), " ")
		if line == "" {
			continue
		}
		// An .lrc's metadata tags ([ar:…], [ti:…]) are not lyrics.
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && strings.Contains(line, ":") {
			continue
		}
		if r := []rune(line); len(r) > 200 {
			line = string(r[:200])
		}
		plain = append(plain, line)
		if len(plain) >= lyricsEmbeddedLines {
			break
		}
	}
	if len(plain) == 0 {
		return nil
	}
	step, start := 4.0, 0.0
	if duration > 0 {
		start = duration * 0.04
		step = duration * 0.88 / float64(len(plain))
	}
	out := make([]lyricLine, len(plain))
	for i, l := range plain {
		out[i] = lyricLine{At: start + float64(i)*step, Text: l}
	}
	return out
}

// lyricsEmbeddedText reads the lyrics a file carries (cached by size and
// modification time), with the length ffprobe reports.
func lyricsEmbeddedText(path string) (string, float64) {
	// ffprobe's last argument: a relative "-name" would be read as an option.
	if !filepath.IsAbs(path) {
		return "", 0
	}
	info, err := statUserFile(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0
	}
	key := dlPathKey(path)
	lyricsEmbeddedCache.mu.Lock()
	if e, ok := lyricsEmbeddedCache.entries[key]; ok && e.mod == info.ModTime().UnixNano() && e.size == info.Size() {
		lyricsEmbeddedCache.mu.Unlock()
		return e.text, e.duration
	}
	lyricsEmbeddedCache.mu.Unlock()
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	probePath := getToolPath("ffprobe", cfg)
	if probePath == "" {
		return "", 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probePath, "-v", "error",
		"-show_entries", "format=duration:format_tags:stream_tags", "-of", "json", path)
	schedHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", 0
	}
	text, dur := lyricsFromProbe(out)
	lyricsEmbeddedCache.mu.Lock()
	if lyricsEmbeddedCache.entries == nil || len(lyricsEmbeddedCache.entries) >= 64 {
		lyricsEmbeddedCache.entries = map[string]lyricsEmbeddedEntry{}
	}
	lyricsEmbeddedCache.entries[key] = lyricsEmbeddedEntry{mod: info.ModTime().UnixNano(), size: info.Size(), text: text, duration: dur}
	lyricsEmbeddedCache.mu.Unlock()
	return text, dur
}

// lyricsSidecarText is an .lrc's text when parseLRC found no timed line in
// it, "" otherwise (loadLyricsFor has it then). Same guards as loadLyricsFor.
func lyricsSidecarText(audioPath string) (text string, exists bool) {
	lrcPath := lyricsPathFor(audioPath)
	if lrcPath == "" {
		return "", false
	}
	f, err := openUserFile(lrcPath)
	if err != nil {
		return "", true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > lyricsEmbeddedMax {
		return "", true
	}
	data, err := io.ReadAll(io.LimitReader(f, lyricsEmbeddedMax+1))
	if err != nil || len(data) > lyricsEmbeddedMax || len(parseLRC(string(data))) > 0 {
		return "", true
	}
	return string(data), true
}

// lyricsEmbeddedLoad fills in the lyrics of a track that started without
// timed .lrc lines: an untimed .lrc's text, else the lyrics inside the file.
// It runs on its own goroutine (musicPlayTrackAt) and only fills in lyrics
// for the track still playing, when it still has none.
func lyricsEmbeddedLoad(path string) {
	if path == "" || isHTTPMediaURL(path) || chiptuneHandles(path) {
		return
	}
	if !lyricsEmbeddedWanted(path) {
		return
	}
	text, sidecar := lyricsSidecarText(path)
	duration := 0.0
	if !sidecar {
		text, duration = lyricsEmbeddedText(path)
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	music.mu.Lock()
	if duration <= 0 {
		duration = music.duration
	}
	music.mu.Unlock()
	lines := lyricsFromText(text, duration)
	if len(lines) == 0 {
		return
	}
	music.mu.Lock()
	if music.current >= 0 && music.current < len(music.tracks) &&
		sameAudioPath(music.tracks[music.current].Path, path) && len(music.lyrics) == 0 {
		music.lyrics = lines
	}
	music.mu.Unlock()
}

// lyricsEmbeddedWanted reports that path is still the track playing and has
// no lyrics yet.
func lyricsEmbeddedWanted(path string) bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.current >= 0 && music.current < len(music.tracks) &&
		sameAudioPath(music.tracks[music.current].Path, path) && len(music.lyrics) == 0
}
