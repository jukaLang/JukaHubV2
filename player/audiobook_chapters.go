package main

// audiobook_chapters.go — chapters inside one audiobook file (backlog MED-9).
//
// The Audiobooks shelf treats each file as a chapter; a single .m4b (or a long
// .m4a/.mp3 with ID3 chapters) is one long track. When such a file starts, its
// embedded chapters are read once with ffprobe -show_chapters (off the render
// thread, bounded in time, output, count and title length) and kept in a small
// cache. Then the player's list shows the chapters, Next and Previous move
// between them (past the last one, Next goes on to the next file), the
// now-playing line names the chapter, and the remembered place names it too
// (audioRememberPosition keeps the title beside the position, which the
// Audiobooks shelf shows as "Continue at …").

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// audioChapter is one chapter of a file, in seconds of that file.
type audioChapter struct {
	Title      string
	Start, End float64
}

const (
	// audioChapterMax bounds one file's list; a real book has dozens.
	audioChapterMax = 1000
	// audioChapterTitleRunes bounds one title.
	audioChapterTitleRunes = 120
	// audioChapterProbeOut bounds what ffprobe may print.
	audioChapterProbeOut = 1 << 20
	// audioChapterProbeTime bounds one ffprobe run.
	audioChapterProbeTime = 15 * time.Second
	// audioChapterCacheMax is how many files' lists are kept.
	audioChapterCacheMax = 32
	// audioChapterRestartSec is how far into a chapter Previous goes back to
	// its start rather than to the chapter before, as a CD player's does.
	audioChapterRestartSec = 3
)

// ffprobeChapterDoc is the part of `ffprobe -print_format json -show_chapters`
// read here.
type ffprobeChapterDoc struct {
	Chapters []struct {
		StartTime string            `json:"start_time"`
		EndTime   string            `json:"end_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
}

// parseFFprobeChapters reads ffprobe's chapter list: sorted by start, bad or
// empty entries dropped, titles cleaned and bounded, an untitled one named
// "Chapter N". Fewer than two chapters is no list (one chapter is the file).
func parseFFprobeChapters(data []byte) []audioChapter {
	if len(data) == 0 || len(data) > audioChapterProbeOut {
		return nil
	}
	var doc ffprobeChapterDoc
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	out := make([]audioChapter, 0, min(len(doc.Chapters), audioChapterMax))
	for _, c := range doc.Chapters {
		if len(out) >= audioChapterMax {
			break
		}
		start, err1 := strconv.ParseFloat(strings.TrimSpace(c.StartTime), 64)
		end, err2 := strconv.ParseFloat(strings.TrimSpace(c.EndTime), 64)
		if err1 != nil || err2 != nil || math.IsNaN(start) || math.IsInf(start, 0) ||
			math.IsNaN(end) || math.IsInf(end, 0) || start < 0 || end <= start {
			continue
		}
		title := ""
		for k, v := range c.Tags {
			if strings.EqualFold(k, "title") {
				title = v
				break
			}
		}
		out = append(out, audioChapter{Title: audioChapterCleanTitle(title), Start: start, End: end})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	// Two chapters starting together are one place to go to.
	kept := out[:0]
	for _, c := range out {
		if n := len(kept); n > 0 && c.Start-kept[n-1].Start < 0.5 {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) < 2 {
		return nil
	}
	for i := range kept {
		if kept[i].Title == "" {
			kept[i].Title = "Chapter " + strconv.Itoa(i+1)
		}
	}
	return kept
}

// audioChapterCleanTitle drops control characters and bounds the length: the
// title comes from the file's tags.
func audioChapterCleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunesSuffix(s, audioChapterTitleRunes-1, "…") // the "…" is the last rune
}

// audioChapterIndex is the chapter position falls in: -1 before the first.
func audioChapterIndex(chapters []audioChapter, position float64) int {
	idx := -1
	for i, c := range chapters {
		if position+0.25 < c.Start {
			break
		}
		idx = i
	}
	return idx
}

// audioChapterStep is where Next (+1) or Previous (-1) goes inside a chaptered
// file: ok is false when the step leaves the file (past the last chapter, or
// back from the first one's opening seconds), so the playlist's own step runs.
func audioChapterStep(chapters []audioChapter, position float64, delta int) (int, bool) {
	if len(chapters) < 2 || delta == 0 {
		return 0, false
	}
	cur := audioChapterIndex(chapters, position)
	if delta > 0 {
		next := cur + 1
		if next >= len(chapters) {
			return 0, false
		}
		return next, true
	}
	if cur >= 0 && position-chapters[cur].Start > audioChapterRestartSec {
		return cur, true // back to this chapter's start
	}
	if cur <= 0 {
		return 0, false
	}
	return cur - 1, true
}

// audioChapterCandidate reports whether a file is worth asking for chapters:
// an .m4b always; another long-form container only when it is long (or part
// of a book). A song gets no second ffprobe at every start.
func audioChapterCandidate(path string, longForm bool) bool {
	if strings.TrimSpace(path) == "" || isHTTPMediaURL(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4b":
		return true
	case ".m4a", ".mp3", ".ogg", ".opus", ".flac":
		return longForm
	}
	return false
}

// audioChapterCache holds the files' lists by the path the player uses. A nil
// list means "asked, none". Any goroutine, under mu; the render thread only
// looks up.
var audioChapterCache struct {
	mu     sync.Mutex
	byPath map[string][]audioChapter
	order  []string
}

// audioChaptersFor is the cached list of path (nil when none or not asked).
func audioChaptersFor(path string) []audioChapter {
	audioChapterCache.mu.Lock()
	defer audioChapterCache.mu.Unlock()
	return audioChapterCache.byPath[path]
}

func audioChaptersKnown(path string) bool {
	audioChapterCache.mu.Lock()
	defer audioChapterCache.mu.Unlock()
	_, ok := audioChapterCache.byPath[path]
	return ok
}

func audioChaptersStore(path string, list []audioChapter) {
	audioChapterCache.mu.Lock()
	defer audioChapterCache.mu.Unlock()
	if audioChapterCache.byPath == nil {
		audioChapterCache.byPath = make(map[string][]audioChapter)
	}
	if _, ok := audioChapterCache.byPath[path]; !ok {
		audioChapterCache.order = append(audioChapterCache.order, path)
	}
	audioChapterCache.byPath[path] = list
	audioChapterRevision.Add(1)
	for len(audioChapterCache.order) > audioChapterCacheMax {
		delete(audioChapterCache.byPath, audioChapterCache.order[0])
		audioChapterCache.order = audioChapterCache.order[1:]
	}
}

// audioChapterTitleAt names the chapter position is in, "" when path has no
// chapter list (yet). audioRememberPosition keeps it beside the position.
func audioChapterTitleAt(path string, position float64) string {
	chapters := audioChaptersFor(path)
	if i := audioChapterIndex(chapters, position); i >= 0 {
		return chapters[i].Title
	}
	return ""
}

// musicProbeChapters reads path's chapters once. musicPlayTrackAt starts it on
// its own goroutine, beside musicProbeDuration; longForm is a book's file or a
// long one.
func musicProbeChapters(path string, longForm bool) {
	if !audioChapterCandidate(path, longForm) || audioChaptersKnown(path) {
		return
	}
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	probe := getToolPath("ffprobe", cfg)
	if probe == "" {
		return
	}
	if info, err := os.Stat(probe); err != nil || info.Size() == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), audioChapterProbeTime)
	defer cancel()
	cmd := P().CommandContext(ctx, probe, "-v", "error", "-print_format", "json", "-show_chapters", "-i", path)
	if cmd == nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		logDebug("musicplayer", "chapters: ffprobe did not start: %s", ffmpegStartReason(err))
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, audioChapterProbeOut+1))
	if readErr != nil || len(data) > audioChapterProbeOut {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	var list []audioChapter
	if readErr == nil && waitErr == nil {
		list = parseFFprobeChapters(data)
	}
	audioChaptersStore(path, list)
	if len(list) > 0 {
		logInfo("musicplayer", "%d chapters in %s", len(list), filepath.Base(path))
	}
}

// musicChapterStep is Next (+1) or Previous (-1) inside the playing file's
// chapters: true when it seeked, false when the playlist's own step should run.
func musicChapterStep(delta int) bool {
	music.mu.Lock()
	if !music.playing || music.current < 0 || music.current >= len(music.tracks) {
		music.mu.Unlock()
		return false
	}
	path, position := music.tracks[music.current].Path, music.position
	music.mu.Unlock()
	chapters := audioChaptersFor(path)
	i, ok := audioChapterStep(chapters, position, delta)
	if !ok {
		return false
	}
	musicSeek(chapters[i].Start)
	showToast(fmt.Sprintf("Chapter %d of %d — %s", i+1, len(chapters), chapters[i].Title), ToastInfo())
	return true
}

// musicChapterMeta is the now-playing line for a chaptered file ("Chapter 3 of
// 12 · The Storm"), "" for any other.
func musicChapterMeta(path string, position float64) string {
	chapters := audioChaptersFor(path)
	if len(chapters) == 0 {
		return ""
	}
	i := max(audioChapterIndex(chapters, position), 0)
	return fmt.Sprintf("Chapter %d of %d   ·   %s", i+1, len(chapters), chapters[i].Title)
}

// audioChapterRevision bumps whenever a file's chapter list is stored, so the
// music shelf can re-count a probed file's chapters.
var audioChapterRevision atomic.Uint64

// musicChapters returns the chapter list shown for the current single-track
// book, if any.
func musicChapters() []audioChapter {
	music.mu.Lock()
	if music.current < 0 || music.current >= len(music.tracks) {
		music.mu.Unlock()
		return nil
	}
	track := music.tracks[music.current]
	tracks := len(music.tracks)
	music.mu.Unlock()
	// A book is one file; a podcast episode is one track in a show queue but
	// its chapters still belong in the list. An ordinary album track does not
	// get a chapter list.
	if tracks != 1 && !track.Podcast {
		return nil
	}
	return audioChaptersFor(track.Path)
}

// musicPlayChapter starts chapter i of the current book file.
func musicPlayChapter(i int) {
	chapters := musicChapters()
	if i < 0 || i >= len(chapters) {
		return
	}
	music.mu.Lock()
	cur := music.current
	if cur < 0 || cur >= len(music.tracks) {
		music.mu.Unlock()
		return
	}
	needPlay := !music.playing
	singleBook := len(music.tracks) == 1 && !music.tracks[cur].Podcast
	music.mu.Unlock()
	if needPlay {
		musicPlayTrack(cur)
	}
	musicSeek(chapters[i].Start)
	if singleBook {
		music.mu.Lock()
		music.cursor = i
		music.mu.Unlock()
	}
	showToast(fmt.Sprintf("Chapter %d of %d — %s", i+1, len(chapters), chapters[i].Title), ToastInfo())
}

// renderMusicChapterRows draws a chaptered file's chapters in the player's list
// card, the playing one marked and kept in view. Rows are tap targets: Up/Down
// move the cursor, A or a click/tap starts the chosen chapter.
func renderMusicChapterRows(renderer *sdl.Renderer, config *Config, font *ttf.Font, chapters []audioChapter,
	position float64, cursor int, cardX, cardW, rowsTop int32, visible int) {
	cur := max(audioChapterIndex(chapters, position), 0)
	if cursor < 0 || cursor >= len(chapters) {
		cursor = cur
	}
	start := 0
	if cursor >= visible {
		start = cursor - visible + 1
	}
	if start < 0 {
		start = 0
	}
	if start+visible > len(chapters) {
		start = len(chapters) - visible
		if start < 0 {
			start = 0
		}
	}
	end := min(start+visible, len(chapters))
	ctlFocus := musicCtlHasFocus(config)
	for i := start; i < end; i++ {
		rowY := rowsTop + int32(i-start)*musicRowHeight
		row := sdl.Rect{X: cardX + 16, Y: rowY, W: cardW - 32, H: musicRowHeight - 6}
		textY := row.Y + (row.H-int32(font.Height()))/2
		if i == cursor && !ctlFocus {
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 10, WithAlpha(ColorCardFocus, 255))
			drawGlassFocusRing(renderer, row, 10)
		}
		col := ColorTextPrimary()
		if i == cur {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 220)
			renderer.FillRect(&sdl.Rect{X: row.X + 3, Y: row.Y + 8, W: 4, H: row.H - 16})
			col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255}
		}
		renderText(renderer, config, font, strconv.Itoa(i+1)+".", ColorTextTertiary(), row.X+18, textY)
		renderText(renderer, config, font, ellipsize(font, chapters[i].Title, row.W-62-96), col, row.X+62, textY)
		at := formatTime(chapters[i].Start)
		aw, _, _ := font.SizeUTF8(at)
		renderText(renderer, config, font, at, ColorTextTertiary(), row.X+row.W-int32(aw)-18, textY)
		musicRects.rows = append(musicRects.rows, row)
	}
	musicRects.rowStart = start
}
