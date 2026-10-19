package main

// lyrics.go — .lrc lyrics for the audio player.
//
// The format is a list of "[mm:ss.xx]line" entries; a line may carry several
// timestamps (repeated chorus) and the file may start with metadata tags such
// as [ar:Artist]. Enhanced LRC adds per-word <mm:ss.xx> tags, which are
// stripped because the player highlights whole lines.
//
// Parsing and lookup are SDL-free so they can be tested headless.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// lyricLine is one timed line of a lyrics file.
type lyricLine struct {
	At   float64 // seconds from the start of the track
	Text string
}

// parseLRC turns .lrc content into sorted, timed lines. Unknown tags and
// malformed timestamps are skipped rather than failing the whole file.
func parseLRC(data string) []lyricLine {
	data = strings.TrimPrefix(data, "\ufeff")
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")

	offset := 0.0
	var lines []lyricLine

	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// Collect every leading [..] tag, then the lyrics text after them.
		var stamps []float64
		for strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				break
			}
			tag := line[1:end]
			line = strings.TrimSpace(line[end+1:])
			if sec, ok := parseLRCTime(tag); ok {
				stamps = append(stamps, sec)
				continue
			}
			// Metadata tags: only the offset changes the timing.
			if strings.HasPrefix(strings.ToLower(tag), "offset:") {
				if ms, err := strconv.Atoi(strings.TrimSpace(tag[len("offset:"):])); err == nil {
					offset = float64(ms) / 1000
				}
			}
		}
		text := stripLRCTags(line)
		if text == "" {
			continue
		}
		for _, at := range stamps {
			t := at + offset
			if t < 0 {
				t = 0
			}
			lines = append(lines, lyricLine{At: t, Text: text})
		}
	}

	sort.SliceStable(lines, func(i, j int) bool { return lines[i].At < lines[j].At })
	return lines
}

// parseLRCTime parses "mm:ss", "mm:ss.xx" or "mm:ss.xxx" into seconds.
func parseLRCTime(tag string) (float64, bool) {
	colon := strings.IndexByte(tag, ':')
	if colon <= 0 {
		return 0, false
	}
	mins, err := strconv.Atoi(strings.TrimSpace(tag[:colon]))
	if err != nil || mins < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(tag[colon+1:])
	secPart := rest
	fracPart := ""
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		secPart, fracPart = rest[:dot], rest[dot+1:]
	}
	if secPart == "" || len(secPart) > 2 {
		return 0, false
	}
	secs, err := strconv.Atoi(secPart)
	if err != nil || secs < 0 || secs > 59 {
		return 0, false
	}
	frac := 0.0
	if fracPart != "" {
		if len(fracPart) > 3 {
			fracPart = fracPart[:3]
		}
		digits, err := strconv.Atoi(fracPart)
		if err != nil {
			// A trailing non-numeric fragment means this was not a timestamp.
			return 0, false
		}
		div := 1.0
		for range fracPart {
			div *= 10
		}
		frac = float64(digits) / div
	}
	return float64(mins)*60 + float64(secs) + frac, true
}

// stripLRCTags removes enhanced-LRC word timings ("<00:12.34>") and stray
// surrounding whitespace.
func stripLRCTags(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '<' {
			b.WriteByte(text[i])
			continue
		}
		end := strings.IndexByte(text[i:], '>')
		if end < 0 {
			b.WriteByte(text[i])
			continue
		}
		if _, ok := parseLRCTime(text[i+1 : i+end]); ok {
			i += end
			continue
		}
		b.WriteByte(text[i])
	}
	return strings.TrimSpace(b.String())
}

// lyricIndexAt returns the index of the line that should be highlighted at
// pos, or -1 before the first line. Binary search keeps this cheap enough for
// the render loop.
func lyricIndexAt(lines []lyricLine, pos float64) int {
	lo, hi := 0, len(lines)-1
	idx := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if lines[mid].At <= pos {
			idx = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return idx
}

// lyricCache keeps the last loaded file so the render loop never re-reads the
// SD card. The modification time is part of the key, so editing a .lrc while
// the player is open shows up on the next track start.
var lyricCache struct {
	mu      sync.Mutex
	path    string
	modTime int64
	size    int64
	lines   []lyricLine
	loaded  bool
}

// lyricsPathFor returns the .lrc file next to an audio file, if one exists.
func lyricsPathFor(audioPath string) string {
	base := strings.TrimSuffix(audioPath, filepath.Ext(audioPath))
	for _, ext := range []string{".lrc", ".LRC", ".Lrc"} {
		p := base + ext
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// loadLyricsFor parses the lyrics file next to audioPath. It returns nil when
// the track has none.
func loadLyricsFor(audioPath string) []lyricLine {
	lrcPath := lyricsPathFor(audioPath)
	if lrcPath == "" {
		return nil
	}
	info, err := os.Stat(lrcPath)
	if err != nil {
		return nil
	}

	lyricCache.mu.Lock()
	if lyricCache.loaded && lyricCache.path == lrcPath &&
		lyricCache.modTime == info.ModTime().UnixNano() && lyricCache.size == info.Size() {
		lines := lyricCache.lines
		lyricCache.mu.Unlock()
		return lines
	}
	lyricCache.mu.Unlock()

	data, err := os.ReadFile(lrcPath)
	if err != nil {
		return nil
	}
	lines := parseLRC(string(data))

	lyricCache.mu.Lock()
	lyricCache.path = lrcPath
	lyricCache.modTime = info.ModTime().UnixNano()
	lyricCache.size = info.Size()
	lyricCache.lines = lines
	lyricCache.loaded = true
	lyricCache.mu.Unlock()
	return lines
}
