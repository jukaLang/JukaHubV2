package main

// mlib.go — the Media Library: the films, TV shows and music already on the
// card (or in the PC's Videos and Music folders), found by a background scan
// and grouped into a library that needs no network at all. The Media screen's
// Library tab draws it (media_hub_library.go).
//
// The scan is incremental. The index (medialibrary/index.json beside the app's
// other data) keeps every file with its size and modification time, and every
// folder with its own modification time and subfolders: a folder whose time
// has not moved is not listed again, its files come from the index. Copying a
// film into a folder changes that folder's time, so only that folder is read.
// The index also keeps what the slow pass learnt — the length and the tags
// ffprobe read, the poster made for each title — and where each video was
// left, so reopening the library is instant and Continue watching outlives
// the player's twenty-entry recent list.
//
// Grouping is by name: "Show.S01E02…" or "Show 1x02" and the Season folders
// make a TV show, every other video is a film, and songs group by artist and
// album (their tags when ffprobe is installed, else the Artist/Album folders).
//
// A scan is one goroutine, cancelled when Media is left or a game takes the
// screen; every root it finished stays published. The slow pass waits while a
// video plays. The render thread only ever reads a finished, immutable model.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// mlibIndexVersion is the index format; another version is scanned afresh.
	mlibIndexVersion = 1
	// mlibMaxDepth bounds how deep a root is walked: Show/Season/episode and
	// Artist/Album/CD1/song fit with room to spare.
	mlibMaxDepth = 6
	// mlibMaxFiles bounds one library on the 1 GB handheld.
	mlibMaxFiles = 20000
	// mlibProbeTimeout bounds one ffprobe run on a slow card.
	mlibProbeTimeout = 10 * time.Second
	// mlibArtWidth and mlibArtMaxH are the size posters are stored at: a card
	// is at most about 300 px wide, and a 2000 px album cover would cost 16 MB
	// as a texture.
	mlibArtWidth = 320
	mlibArtMaxH  = 480
	// mlibArtMaxFiles bounds the poster cache on disk.
	mlibArtMaxFiles = 1500
	// mlibArtMaxPixels and mlibArtMaxBytes refuse pictures too big to decode
	// on the handheld for a 320 px poster.
	mlibArtMaxPixels = 24 << 20
	mlibArtMaxBytes  = 16 << 20
	// mlibPublishEvery and mlibSaveEvery pace the slow pass: the screen gets
	// new posters every few seconds, the card is written rarely.
	mlibPublishEvery = 4 * time.Second
	mlibSaveEvery    = 30 * time.Second
	// mlibRescanAfter is how soon opening the Library looks for changes again.
	mlibRescanAfter = 45 * time.Second
	// mlibRecentMax is the length of the Recently added row.
	mlibRecentMax = 12
	// mlibRootsKey names extra folders to scan: a list, or one string with the
	// folders separated by semicolons.
	mlibRootsKey = "media_library_roots"
)

// mlibVideoExts and mlibAudioExts are the files the library lists.
var (
	mlibVideoExts = map[string]bool{".mp4": true, ".mkv": true, ".webm": true, ".avi": true, ".mov": true, ".m4v": true, ".ts": true}
	mlibAudioExts = map[string]bool{".mp3": true, ".flac": true, ".ogg": true, ".opus": true, ".m4a": true, ".aac": true, ".wav": true}
)

// mlibMediaKind reports whether a file name is a video or a song the library
// lists.
func mlibMediaKind(name string) (video, ok bool) {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case mlibVideoExts[ext]:
		return true, true
	case mlibAudioExts[ext]:
		return false, true
	}
	return false, false
}

// ── The index ────────────────────────────────────────────────────────────────

// mlibItem is one file of the library.
type mlibItem struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mod   int64  `json:"mod"`
	Video bool   `json:"video"`
	// Added is when the file arrived (its modification time, Unix seconds):
	// Recently added sorts by it.
	Added    int64   `json:"added"`
	Title    string  `json:"title"`
	Year     int     `json:"year,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	// A TV episode.
	Show    string `json:"show,omitempty"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
	// A song.
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	Track  int    `json:"track,omitempty"`
	// Art is the item's own picture in the poster cache; Poster is its show's
	// (an episode's show folder), drawn on the show's card.
	Art    string `json:"art,omitempty"`
	Poster string `json:"poster,omitempty"`
	// Probed and ArtDone record the slow pass, so a rescan does not repeat it
	// for a file that did not change.
	Probed  bool `json:"probed,omitempty"`
	ArtDone bool `json:"artDone,omitempty"`
	// Position and Played come from the resume map when the model is built.
	Position float64 `json:"-"`
	Played   int64   `json:"-"`
}

// mlibDirState is one folder as the last scan saw it.
type mlibDirState struct {
	Mod     int64    `json:"mod"`
	Subdirs []string `json:"subdirs,omitempty"`
}

// mlibResume is where a video was left.
type mlibResume struct {
	Position float64 `json:"position"`
	Duration float64 `json:"duration,omitempty"`
	Played   int64   `json:"played"`
}

// mlibIndexFile is the index on disk.
type mlibIndexFile struct {
	Version int                     `json:"version"`
	Items   []mlibItem              `json:"items"`
	Dirs    map[string]mlibDirState `json:"dirs,omitempty"`
	Resume  map[string]mlibResume   `json:"resume,omitempty"`
}

// mlibState is the live library. Every field is guarded by mu; the screen
// only reads model, which is never changed once published.
var mlibState struct {
	mu      sync.Mutex
	loaded  bool
	items   []mlibItem
	dirs    map[string]mlibDirState
	resume  map[string]mlibResume
	model   *mlibModel
	gen     uint64
	cancel  context.CancelFunc
	ticket  uint64
	found   int
	scanned time.Time
	saving  bool
	again   bool
}

var (
	// mlibScanning is raised while a scan runs. mediaHubTick polls it every
	// frame, so it is an atomic rather than a field behind the lock.
	mlibScanning atomic.Bool
	// mlibBuildMu keeps model builds in order, so an older build is never
	// published over a newer one.
	mlibBuildMu sync.Mutex
	// mlibDirOverride moves the index and the poster cache for tests.
	mlibDirOverride string
	// mlibProbe reads a file's length and tags; tests replace it so the suite
	// never runs ffprobe.
	mlibProbe = mlibProbeFile
	// mlibBaseRoots lists the folders scanned without being configured; tests
	// point it at a temporary folder so the suite never walks the real ones.
	mlibBaseRoots = mlibDefaultRoots
)

// mlibDir is the folder the index and the poster cache live in.
func mlibDir() string {
	if dir := strings.TrimSpace(mlibDirOverride); dir != "" {
		return dir
	}
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "medialibrary")
	}
	return "medialibrary"
}

func mlibIndexPath() string { return filepath.Join(mlibDir(), "index.json") }

func mlibArtDir() string { return filepath.Join(mlibDir(), "art") }

// ── Names ────────────────────────────────────────────────────────────────────

var (
	// mlibEpisodeSE finds "S01E02" in any case, with or without a separator
	// between the numbers; what precedes it is the show.
	mlibEpisodeSE = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)^(.*?)(?:^|[\s._\-\[(]+)s(\d{1,2})[\s._\-]?e(\d{1,3})(\D.*)?$`)
	})
	// mlibEpisodeX finds "1x02". A separator (or the start) must come before
	// it and no digit after it, so 1920x1080 is not an episode.
	mlibEpisodeX = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)^(.*?)(?:^|[\s._\-\[(]+)(\d{1,2})x(\d{1,3})(\D.*)?$`)
	})
	// mlibSeasonDir is a season folder: "Season 2", "S02", "Staffel 2".
	mlibSeasonDir = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)^(?:season|series|staffel|saison|temporada|s)[\s._\-]*(\d{1,2})$`)
	})
	// mlibDiscDir is a disc folder inside an album: "CD1", "Disc 2".
	mlibDiscDir = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)^(?:cd|disc|disk)[\s._\-]*\d{1,2}$`) })
	// mlibYearToken is a release year between separators.
	mlibYearToken = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?:^|[\s._\-(\[])((?:19|20)\d\d)(?:[\s._\-)\]]|$)`) })
	// mlibTrackPrefix is a song's leading track number: "01 Song", "01 - Song",
	// "1. Song", "1-03 Song". A bare number before a space ("7 Years") is
	// part of the title.
	mlibTrackPrefix = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^(?:\d-)?(?:(0\d{1,2})[\s._\-]+|(\d{1,3})\s*[.\-][\s._\-]*)(\S.*)$`)
	})
	// mlibSeparators turns the dots and underscores of a file name into spaces.
	mlibSeparators = strings.NewReplacer(".", " ", "_", " ")
)

// mlibReleaseTags are the words that start a release's technical tail; they
// and everything after them are not part of a title. Words that also occur
// in titles ("web", "internal", "proper") are left out on purpose.
var mlibReleaseTags = map[string]bool{
	"4k": true, "uhd": true, "hdr10": true, "x264": true, "x265": true, "h264": true, "h265": true,
	"hevc": true, "xvid": true, "divx": true, "webrip": true, "webdl": true, "web-dl": true, "hdtv": true,
	"hdrip": true, "dvdrip": true, "bdrip": true, "brrip": true, "bluray": true, "blu-ray": true,
	"remux": true, "10bit": true, "8bit": true, "aac": true, "ac3": true, "ddp5": true,
}

// mlibReleaseTag reports a word of a release's technical tail: one of the
// tags, a tag with a group name attached ("x264-GROUP"), or a resolution
// ("720p", "1080i").
func mlibReleaseTag(word string) bool {
	w := strings.ToLower(strings.Trim(word, "[](){}-"))
	if w == "" {
		return false
	}
	if mlibReleaseTags[w] {
		return true
	}
	if i := strings.IndexByte(w, '-'); i > 0 && mlibReleaseTags[w[:i]] {
		return true
	}
	if strings.HasSuffix(w, "p") || strings.HasSuffix(w, "i") {
		if n, err := strconv.Atoi(w[:len(w)-1]); err == nil && n >= 240 && n <= 4320 {
			return true
		}
	}
	return false
}

// mlibCleanName turns a file name into the words a person would write: dots
// and underscores become spaces, the release tail goes, and stray separators
// are trimmed.
func mlibCleanName(s string) string {
	words := strings.Fields(mlibSeparators.Replace(s))
	n := 0
	for _, w := range words {
		if mlibReleaseTag(w) {
			break
		}
		words[n] = w
		n++
	}
	return strings.Trim(strings.Join(words[:n], " "), " -–—[](){}")
}

// mlibExtraEpisode reports the "E02" a double episode ("S01E01E02") leaves in
// front of the title.
func mlibExtraEpisode(word string) bool {
	w := strings.ToLower(word)
	if len(w) < 2 || w[0] != 'e' {
		return false
	}
	_, err := strconv.Atoi(w[1:])
	return err == nil
}

// mlibParseEpisode reads a TV episode out of a file name without its
// extension: the show as written before the episode number (may be empty),
// the season, the episode, and the episode's own title after it. ok is false
// for a name with no episode number.
func mlibParseEpisode(name string) (show string, season, episode int, title string, ok bool) {
	m := mlibEpisodeSE().FindStringSubmatch(name)
	if m == nil {
		m = mlibEpisodeX().FindStringSubmatch(name)
	}
	if m == nil {
		return "", 0, 0, "", false
	}
	season, _ = strconv.Atoi(m[2])
	episode, _ = strconv.Atoi(m[3])
	show = mlibCleanName(m[1])
	title = mlibCleanName(m[4])
	if f := strings.Fields(title); len(f) > 0 && mlibExtraEpisode(f[0]) {
		title = strings.Trim(strings.Join(f[1:], " "), " -")
	}
	return show, season, episode, title, true
}

// mlibMovieTitle reads a film's title and year out of its file name:
// "Heat.1995.1080p" is Heat (1995). A year at the very start belongs to the
// title ("2001.A.Space.Odyssey.1968" is 2001 A Space Odyssey, 1968).
func mlibMovieTitle(name string) (string, int) {
	for _, loc := range mlibYearToken().FindAllStringSubmatchIndex(name, -1) {
		if loc[0] <= 0 {
			continue
		}
		title := mlibCleanName(name[:loc[0]])
		if title == "" {
			continue
		}
		year, _ := strconv.Atoi(name[loc[2]:loc[3]])
		return title, year
	}
	return mlibCleanName(name), 0
}

// mlibSongTitle reads a song's track number and title from its file name:
// "03 - Song" is track 3, Song.
func mlibSongTitle(name string) (int, string) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	m := mlibTrackPrefix().FindStringSubmatch(name)
	if m == nil {
		return 0, name
	}
	num := m[1]
	if num == "" {
		num = m[2]
	}
	n, _ := strconv.Atoi(num)
	return n, strings.TrimSpace(m[3])
}

// mlibNameItem fills an entry's title and grouping from its file name and the
// folders between it and the root it was found under.
func mlibNameItem(it *mlibItem, root string) {
	base := filepath.Base(it.Path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	dir := filepath.Dir(it.Path)
	upper := filepath.Dir(dir)
	rootKey := dlPathKey(root)
	// folder is the folder holding the file ("" directly in the root), above
	// the one above it ("" when that is the root).
	folder, above := "", ""
	if dlPathKey(dir) != rootKey {
		folder = filepath.Base(dir)
		if dlPathKey(upper) != rootKey && isPathWithin(upper, root) {
			above = filepath.Base(upper)
		}
	}
	if !it.Video {
		it.Track, it.Title = mlibSongTitle(name)
		album, artist := folder, above
		if above != "" && mlibDiscDir().MatchString(folder) {
			// Artist/Album/CD1/song: the disc folder is part of the album.
			album, artist = above, ""
			if top := filepath.Dir(upper); dlPathKey(top) != rootKey && isPathWithin(top, root) {
				artist = filepath.Base(top)
			}
		}
		if album == "" {
			// A song directly in the root belongs to the root's own name.
			album = filepath.Base(dir)
		}
		it.Album, it.Artist = album, artist
		if artist != "" && strings.HasPrefix(it.Title, artist+" - ") {
			if rest := strings.TrimSpace(it.Title[len(artist)+3:]); rest != "" {
				it.Title = rest
			}
		}
		if it.Title == "" {
			it.Title = name
		}
		return
	}
	if show, season, episode, title, ok := mlibParseEpisode(name); ok {
		it.Season, it.Episode, it.Title = season, episode, title
		if show == "" {
			switch {
			case folder != "" && mlibSeasonDir().MatchString(folder):
				show = mlibCleanName(above)
			case folder != "":
				show = mlibCleanName(folder)
			}
		}
		if show == "" {
			show = "Unknown show"
		}
		it.Show = show
		return
	}
	it.Title, it.Year = mlibMovieTitle(name)
	if it.Title == "" {
		it.Title = name
	}
}

// ── Walking ──────────────────────────────────────────────────────────────────

// mlibFile is one media file a walk found.
type mlibFile struct {
	path  string
	root  string
	size  int64
	mod   int64
	video bool
}

// mlibNewItem starts the index entry for a file the scan found, from what its
// name and folders say. The slow pass adds the tags, the length and the
// poster.
func mlibNewItem(f mlibFile) mlibItem {
	it := mlibItem{Path: f.path, Size: f.size, Mod: f.mod, Video: f.video}
	if f.mod > 0 {
		it.Added = f.mod / int64(time.Second)
	}
	mlibNameItem(&it, f.root)
	return it
}

// mlibSkipDir reports the folders a walk never enters: hidden folders, the
// trash and system folders, NAS thumbnail stores.
func mlibSkipDir(name string) bool {
	if skipScanEntryName(name) {
		return true
	}
	switch strings.ToLower(name) {
	case "$recycle.bin", "system volume information", "@eadir", "lost+found", "#recycle":
		return true
	}
	return false
}

// mlibWalkRoot lists one root's media files. A folder whose modification time
// is the one the index recorded is not listed again: its files and subfolders
// come from the index (known, knownFiles). Every folder visited is recorded in
// dirs for the next scan. It stops at limit files, and reports false when ctx
// was cancelled first.
func mlibWalkRoot(ctx context.Context, root string, known map[string]mlibDirState, knownFiles map[string][]mlibItem, dirs map[string]mlibDirState, limit int) ([]mlibFile, bool) {
	type visit struct {
		dir   string
		depth int
	}
	var out []mlibFile
	stack := []visit{{dir: root}}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return out, false
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		info, err := os.Stat(v.dir)
		if err != nil || !info.IsDir() {
			continue
		}
		key := dlPathKey(v.dir)
		mod := info.ModTime().UnixNano()
		prev, seen := known[key]
		var subdirs []string
		listed := false
		if !seen || prev.Mod != mod {
			if entries, rerr := os.ReadDir(v.dir); rerr == nil {
				listed = true
				for _, e := range entries {
					name := e.Name()
					if e.IsDir() {
						if !mlibSkipDir(name) {
							subdirs = append(subdirs, name)
						}
						continue
					}
					if skipScanEntryName(name) || !e.Type().IsRegular() {
						continue
					}
					video, ok := mlibMediaKind(name)
					if !ok {
						continue
					}
					fi, ierr := e.Info()
					if ierr != nil || fi.Size() <= 0 {
						continue
					}
					out = append(out, mlibFile{path: filepath.Join(v.dir, name), root: root,
						size: fi.Size(), mod: fi.ModTime().UnixNano(), video: video})
				}
				sort.Strings(subdirs)
			}
		}
		if !listed {
			if !seen {
				continue
			}
			// Unchanged, or unreadable this time: what the index knew stands,
			// and keeping its time makes the next scan look again.
			subdirs = prev.Subdirs
			mod = prev.Mod
			for _, it := range knownFiles[key] {
				out = append(out, mlibFile{path: it.Path, root: root, size: it.Size, mod: it.Mod, video: it.Video})
			}
		}
		dirs[key] = mlibDirState{Mod: mod, Subdirs: subdirs}
		if len(out) >= limit {
			return out[:limit], true
		}
		if v.depth < mlibMaxDepth {
			for i := len(subdirs) - 1; i >= 0; i-- {
				stack = append(stack, visit{dir: filepath.Join(v.dir, subdirs[i]), depth: v.depth + 1})
			}
		}
	}
	return out, true
}

// mlibFilesByDir groups index entries by the folder holding them, for the
// walk's unchanged folders.
func mlibFilesByDir(items []mlibItem) map[string][]mlibItem {
	out := make(map[string][]mlibItem, len(items)/8+1)
	for _, it := range items {
		key := dlPathKey(filepath.Dir(it.Path))
		out[key] = append(out[key], it)
	}
	return out
}

// mlibReconcile keeps the index entry of every file still there with the same
// size and time, and starts a fresh one for a new or changed file (keeping
// when it arrived). Entries of files that are gone are dropped.
func mlibReconcile(old []mlibItem, files []mlibFile) []mlibItem {
	byPath := make(map[string]int, len(old))
	for i := range old {
		byPath[dlPathKey(old[i].Path)] = i
	}
	out := make([]mlibItem, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		key := dlPathKey(f.path)
		if seen[key] {
			continue
		}
		seen[key] = true
		i, known := byPath[key]
		if known && old[i].Size == f.size && old[i].Mod == f.mod {
			out = append(out, old[i])
			continue
		}
		it := mlibNewItem(f)
		if known && old[i].Added > 0 {
			it.Added = old[i].Added
		}
		out = append(out, it)
	}
	return out
}

// mlibReplaceRoot swaps one root's entries for a fresh walk of it.
func mlibReplaceRoot(items []mlibItem, root string, fresh []mlibItem) []mlibItem {
	out := make([]mlibItem, 0, len(items)+len(fresh))
	for _, it := range items {
		if !isPathWithin(it.Path, root) {
			out = append(out, it)
		}
	}
	return append(out, fresh...)
}

// mlibKeepUnder drops the entries no root reaches any more (a folder removed
// from media_library_roots).
func mlibKeepUnder(items []mlibItem, roots []string) []mlibItem {
	out := make([]mlibItem, 0, len(items))
	for _, it := range items {
		for _, r := range roots {
			if isPathWithin(it.Path, r) {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// ── Roots ────────────────────────────────────────────────────────────────────

// mlibDefaultRoots are the folders the library looks in without being told:
// the card's Videos, Movies, TV, Music and Media folders on the handheld, the
// user's Videos and Music folders on a PC. It runs on the scan's goroutine.
func mlibDefaultRoots() []string {
	if IsTrimuiDevice() {
		base := strings.TrimSpace(defaultTrimuiSDRoot())
		if base == "" {
			base = "/mnt/SDCARD"
		}
		roots := make([]string, 0, 5)
		for _, sub := range []string{"Videos", "Movies", "TV", "Music", "Media"} {
			roots = append(roots, filepath.Join(base, sub))
		}
		return roots
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	return []string{filepath.Join(home, "Videos"), filepath.Join(home, "Music")}
}

// mlibConfiguredRoots reads media_library_roots: a JSON list of folders, or
// one string with the folders separated by semicolons or new lines.
func mlibConfiguredRoots(v interface{}) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	switch t := v.(type) {
	case string:
		for _, part := range strings.FieldsFunc(t, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' }) {
			add(part)
		}
	case []string:
		for _, s := range t {
			add(s)
		}
	case []interface{}:
		for _, item := range t {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// mlibCleanRoots tidies the folder list: cleaned, each once, and a folder
// inside another one left out (the outer walk already reaches it).
func mlibCleanRoots(roots []string) []string {
	unique := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		key := dlPathKey(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, r)
	}
	out := make([]string, 0, len(unique))
	for i, r := range unique {
		nested := false
		for j, other := range unique {
			if i != j && isPathWithin(r, other) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, r)
		}
	}
	return out
}

// ── Scanning ─────────────────────────────────────────────────────────────────

// mlibStartScan looks for changes in the library's folders in the background,
// unless a scan is running or one finished a moment ago (force skips that
// wait: the empty state's Scan again). It reads the config on the calling
// (main) thread and touches no disk: the folders are resolved on the scan's
// own goroutine.
func mlibStartScan(config *Config, force bool) {
	var extra []string
	toolsPath := ""
	if config != nil {
		extra = mlibConfiguredRoots(config.Variables.Custom[mlibRootsKey])
		toolsPath = strings.TrimSpace(config.Variables.ToolsPath)
	}
	mlibState.mu.Lock()
	if mlibScanning.Load() || (!force && !mlibState.scanned.IsZero() && time.Since(mlibState.scanned) < mlibRescanAfter) {
		mlibState.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	mlibState.ticket++
	ticket := mlibState.ticket
	mlibState.cancel = cancel
	mlibState.found = 0
	mlibScanning.Store(true)
	mlibState.mu.Unlock()
	go mlibRunScan(ctx, ticket, extra, toolsPath)
}

// mlibCancelScan stops a running scan; what it finished is kept, and the next
// scan carries on from there.
func mlibCancelScan() {
	if !mlibScanning.Load() {
		return
	}
	mlibState.mu.Lock()
	cancel := mlibState.cancel
	mlibState.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// mlibScanFinished clears the running scan. Only a scan that ran to the end
// starts the wait before the next one: a cancelled one is resumed the next
// time the Library opens.
func mlibScanFinished(ticket uint64, completed bool) {
	mlibState.mu.Lock()
	if mlibState.ticket == ticket {
		if mlibState.cancel != nil {
			mlibState.cancel()
		}
		mlibState.cancel = nil
		if completed {
			mlibState.scanned = time.Now()
		}
		mlibScanning.Store(false)
	}
	mlibState.mu.Unlock()
}

// mlibRunScan is one scan: the index first (instant when it is on disk), then
// each root, published as soon as it is walked, then the slow pass.
func mlibRunScan(ctx context.Context, ticket uint64, extra []string, toolsPath string) {
	completed := false
	defer func() { mlibScanFinished(ticket, completed) }()
	mlibLoad()
	roots := mlibCleanRoots(append(mlibBaseRoots(), extra...))
	mlibState.mu.Lock()
	known := make(map[string]mlibDirState, len(mlibState.dirs))
	for k, v := range mlibState.dirs {
		known[k] = v
	}
	mlibState.mu.Unlock()
	dirs := make(map[string]mlibDirState, len(known))
	budget := mlibMaxFiles
	whole := true
	for _, root := range roots {
		if ctx.Err() != nil {
			return
		}
		mlibState.mu.Lock()
		old := append([]mlibItem(nil), mlibState.items...)
		mlibState.mu.Unlock()
		files, done := mlibWalkRoot(ctx, root, known, mlibFilesByDir(old), dirs, budget)
		if !done {
			return
		}
		fresh := mlibReconcile(old, files)
		budget -= len(files)
		mlibState.mu.Lock()
		mlibState.items = mlibReplaceRoot(mlibState.items, root, fresh)
		for k, v := range dirs {
			mlibState.dirs[k] = v
		}
		mlibState.found += len(files)
		mlibState.mu.Unlock()
		mlibRebuildModel()
		if budget <= 0 {
			whole = false
			break
		}
	}
	mlibState.mu.Lock()
	mlibState.items = mlibKeepUnder(mlibState.items, roots)
	if whole {
		mlibState.dirs = dirs
	}
	mlibState.mu.Unlock()
	mlibRebuildModel()
	mlibSave()
	if !mlibEnrich(ctx, toolsPath) {
		return
	}
	mlibPruneArt()
	completed = true
}

// mlibLoad reads the index once, so the library is on screen before the first
// scan has looked at a single folder.
func mlibLoad() {
	mlibState.mu.Lock()
	loaded := mlibState.loaded
	mlibState.mu.Unlock()
	if loaded {
		return
	}
	var file mlibIndexFile
	if _, err := readJSONWithBackup(mlibIndexPath(), &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("library", "the index could not be read; scanning afresh: %v", err)
		}
		file = mlibIndexFile{}
	}
	if file.Version != mlibIndexVersion {
		file = mlibIndexFile{}
	}
	mlibState.mu.Lock()
	if !mlibState.loaded {
		mlibState.items = file.Items
		mlibState.dirs = file.Dirs
		mlibState.resume = file.Resume
		if mlibState.dirs == nil {
			mlibState.dirs = map[string]mlibDirState{}
		}
		if mlibState.resume == nil {
			mlibState.resume = map[string]mlibResume{}
		}
		mlibState.loaded = true
	}
	mlibState.mu.Unlock()
	mlibRebuildModel()
}

// mlibStatus is what the Library tab shows before it has anything to list:
// whether a scan runs, how many files it found, and whether the index was read.
func mlibStatus() (scanning bool, found int, loaded bool) {
	mlibState.mu.Lock()
	defer mlibState.mu.Unlock()
	return mlibScanning.Load(), mlibState.found, mlibState.loaded
}

// mlibCurrentModel is the library as the screen draws it (nil before the
// index was read).
func mlibCurrentModel() *mlibModel {
	mlibState.mu.Lock()
	defer mlibState.mu.Unlock()
	return mlibState.model
}

// mlibGen moves whenever the model is replaced; cheap enough every frame.
func mlibGen() uint64 {
	mlibState.mu.Lock()
	defer mlibState.mu.Unlock()
	return mlibState.gen
}

// ── The slow pass ────────────────────────────────────────────────────────────

// mlibTags is what ffprobe says about a file.
type mlibTags struct {
	duration    float64
	title       string
	artist      string
	albumArtist string
	album       string
	track       int
	year        int
}

// mlibProbeFile reads a file's length and tags with ffprobe.
func mlibProbeFile(ctx context.Context, ffprobe, path string) (mlibTags, error) {
	ctx, cancel := context.WithTimeout(ctx, mlibProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error",
		"-show_entries", "format=duration:format_tags",
		"-of", "default=noprint_wrappers=1", path)
	schedHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return mlibTags{}, err
	}
	return mlibParseProbe(string(out)), nil
}

// mlibParseProbe reads ffprobe's key=value lines. Tag names are matched
// without regard to case: FLAC files write them in capitals.
func mlibParseProbe(out string) mlibTags {
	var t mlibTags
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		key = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(key)), "tag:")
		switch key {
		case "duration":
			if d, err := strconv.ParseFloat(value, 64); err == nil && d > 0 {
				t.duration = d
			}
		case "title":
			t.title = value
		case "artist":
			t.artist = value
		case "album_artist", "albumartist", "album artist":
			t.albumArtist = value
		case "album":
			t.album = value
		case "track", "tracknumber":
			t.track = mlibLeadingInt(value)
		case "date", "year":
			if y := mlibLeadingInt(value); y >= 1900 && y <= 2100 {
				t.year = y
			}
		}
	}
	return t
}

// mlibLeadingInt is the number a tag starts with: "3/12" is 3, "2019-05-01"
// is 2019.
func mlibLeadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// mlibApplyTags folds what ffprobe read into an entry. A song takes its tags;
// a video only its length, because a video's title tag is usually the
// release's file name.
func mlibApplyTags(it *mlibItem, t mlibTags) {
	if t.duration > 0 {
		it.Duration = t.duration
	}
	if it.Video {
		return
	}
	if title := strings.TrimSpace(t.title); title != "" {
		it.Title = title
	}
	if artist := dlFirstNonEmpty(t.albumArtist, t.artist); artist != "" {
		it.Artist = artist
	}
	if album := strings.TrimSpace(t.album); album != "" {
		it.Album = album
	}
	if t.track > 0 {
		it.Track = t.track
	}
	if t.year > 0 {
		it.Year = t.year
	}
}

// mlibTool resolves a bundled tool without installing it: the library never
// waits on a download.
func mlibTool(name, toolsPath string) string {
	path := strings.TrimSpace(getToolPath(name, &Config{Variables: Variables{ToolsPath: toolsPath}}))
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}
	return path
}

// mlibPauseForPlayback holds the slow pass while a video plays: ffmpeg and
// ffprobe runs would take the handheld's cores from the decoder. It reports
// false once the scan was cancelled.
func mlibPauseForPlayback(ctx context.Context) bool {
	for playerPhase() != "idle" {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return ctx.Err() == nil
}

// mlibEnrich is the slow pass: the length and tags of every file not read yet
// (when ffprobe is installed) and its poster. What it finished is published
// every few seconds and saved every half minute. It reports false when the
// scan was cancelled before the pass ended.
func mlibEnrich(ctx context.Context, toolsPath string) bool {
	ffprobe := mlibTool("ffprobe", toolsPath)
	mlibState.mu.Lock()
	pos := make(map[string]int, len(mlibState.items))
	videosInDir := make(map[string]int)
	var todo []mlibItem
	for i, it := range mlibState.items {
		pos[dlPathKey(it.Path)] = i
		if it.Video {
			videosInDir[dlPathKey(filepath.Dir(it.Path))]++
		}
		if (!it.Probed && ffprobe != "") || !it.ArtDone {
			todo = append(todo, it)
		}
	}
	mlibState.mu.Unlock()
	if len(todo) == 0 {
		return ctx.Err() == nil
	}
	// Folder by folder, so an album's songs share one look for its cover.
	sort.SliceStable(todo, func(a, b int) bool { return todo[a].Path < todo[b].Path })
	art := &mlibArtJob{
		dirArt:      map[string]string{},
		dirTried:    map[string]bool{},
		videosInDir: videosInDir,
		budget:      mlibArtBudget(),
	}
	published, saved := time.Now(), time.Now()
	changed := false
	for _, it := range todo {
		if !mlibPauseForPlayback(ctx) {
			break
		}
		upd := it
		if !upd.Probed && ffprobe != "" {
			tags, err := mlibProbe(ctx, ffprobe, upd.Path)
			if ctx.Err() != nil {
				break
			}
			if err == nil {
				mlibApplyTags(&upd, tags)
			}
			upd.Probed = true
		}
		if !upd.ArtDone {
			upd.Art, upd.Poster = art.find(ctx, upd)
			if ctx.Err() != nil {
				break
			}
			upd.ArtDone = true
		}
		mlibState.mu.Lock()
		if i, ok := pos[dlPathKey(upd.Path)]; ok && i < len(mlibState.items) && mlibState.items[i].Path == upd.Path {
			mlibState.items[i] = upd
			changed = true
		}
		mlibState.mu.Unlock()
		if changed && time.Since(published) >= mlibPublishEvery {
			mlibRebuildModel()
			published = time.Now()
		}
		if changed && time.Since(saved) >= mlibSaveEvery {
			mlibSave()
			saved = time.Now()
		}
	}
	if changed {
		mlibRebuildModel()
		mlibSave()
	}
	return ctx.Err() == nil
}

// ── Posters ──────────────────────────────────────────────────────────────────

var (
	// mlibPictureExts are the picture files a poster is made from.
	mlibPictureExts = []string{".jpg", ".jpeg", ".png"}
	// mlibPosterNames are a film's or a show's folder pictures, best first.
	mlibPosterNames = []string{"poster", "folder", "cover", "fanart"}
	// mlibCoverNames are an album folder's pictures, best first.
	mlibCoverNames = []string{"cover", "folder", "front", "album", "albumart", "artwork"}
)

// mlibArtJob finds and makes the posters of one slow pass, remembering each
// folder's picture so an album's twelve songs cost one look.
type mlibArtJob struct {
	dirArt      map[string]string
	dirTried    map[string]bool
	videosInDir map[string]int
	budget      int
}

// find returns an entry's own picture and, for an episode, its show's poster:
//
//   - a film: a picture named after the file, else the folder's poster when
//     the folder holds that one film, else a frame of the film;
//   - an episode: a picture named after the file, else a frame of it; the
//     show folder's poster is kept beside it for the show's card;
//   - a song: the album folder's cover, else the picture embedded in the
//     folder's first song.
func (j *mlibArtJob) find(ctx context.Context, it mlibItem) (art, poster string) {
	dir := filepath.Dir(it.Path)
	dirKey := dlPathKey(dir)
	if !it.Video {
		if a, seen := j.dirArt[dirKey]; seen {
			return a, ""
		}
		a := ""
		if src := mlibFindPicture(dir, mlibCoverNames); src != "" {
			a = j.fromImage(src)
		}
		if a == "" && !j.dirTried[dirKey] {
			j.dirTried[dirKey] = true
			a = j.fromFrame(ctx, it)
		}
		j.dirArt[dirKey] = a
		return a, ""
	}
	if src := mlibStemArt(it.Path); src != "" {
		art = j.fromImage(src)
	}
	if it.Show != "" {
		poster = j.folderPoster(mlibShowDir(it.Path))
	} else if art == "" && j.videosInDir[dirKey] == 1 {
		art = j.folderPoster(dir)
	}
	if art == "" {
		art = j.fromFrame(ctx, it)
	}
	return art, poster
}

// folderPoster is a folder's own picture, looked for once per folder.
func (j *mlibArtJob) folderPoster(dir string) string {
	key := "poster:" + dlPathKey(dir)
	if a, seen := j.dirArt[key]; seen {
		return a
	}
	a := ""
	if src := mlibFindPicture(dir, mlibPosterNames); src != "" {
		a = j.fromImage(src)
	}
	j.dirArt[key] = a
	return a
}

// fromImage makes the cached poster for a picture file, once per picture: the
// cache name comes from its path, size and time.
func (j *mlibArtJob) fromImage(src string) string {
	info, err := os.Stat(src)
	if err != nil || info.Size() <= 0 || info.Size() > mlibArtMaxBytes {
		return ""
	}
	dst := mlibArtPath(src, info.Size(), info.ModTime().UnixNano())
	if mlibFileExists(dst) {
		return dst
	}
	if j.budget <= 0 {
		return ""
	}
	data, err := mlibScalePicture(src)
	if err != nil || len(data) == 0 {
		return ""
	}
	if err := mlibWriteArt(dst, data); err != nil {
		return ""
	}
	j.budget--
	return dst
}

// fromFrame makes the cached poster from the file itself through the bundled
// ffmpeg: a frame of a video, or the cover a song carries inside it.
func (j *mlibArtJob) fromFrame(ctx context.Context, it mlibItem) string {
	dst := mlibArtPath(it.Path+"#frame", it.Size, it.Mod)
	if mlibFileExists(dst) {
		return dst
	}
	if j.budget <= 0 || ctx.Err() != nil {
		return ""
	}
	data := dlPosterGrabber(it.Path)
	if len(data) == 0 {
		return ""
	}
	if err := mlibWriteArt(dst, data); err != nil {
		return ""
	}
	j.budget--
	return dst
}

// mlibShowDir is the folder of an episode's show: the folder above a Season
// folder, else the episode's own folder.
func mlibShowDir(path string) string {
	dir := filepath.Dir(path)
	if mlibSeasonDir().MatchString(filepath.Base(dir)) {
		return filepath.Dir(dir)
	}
	return dir
}

// mlibStemArt is a picture saved beside a video under the video's own name
// (Kodi and Plex write "Film.jpg", "Film-poster.jpg", "Film-thumb.jpg").
func mlibStemArt(path string) string {
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	for _, suffix := range []string{"", "-poster", "-thumb"} {
		for _, ext := range mlibPictureExts {
			if candidate := stem + suffix + ext; mlibFileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// mlibFindPicture looks for one of names (with a picture extension) in dir,
// without regard to case: Windows Media Player writes "Folder.jpg".
func mlibFindPicture(dir string, names []string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	have := make(map[string]string, 8)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		switch filepath.Ext(lower) {
		case ".jpg", ".jpeg", ".png":
			have[lower] = filepath.Join(dir, e.Name())
		}
	}
	for _, name := range names {
		for _, ext := range mlibPictureExts {
			if p, ok := have[name+ext]; ok {
				return p
			}
		}
	}
	return ""
}

// mlibFileExists reports a regular, non-empty file.
func mlibFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// mlibArtPath names a poster in the cache after what it was made from.
func mlibArtPath(source string, size, mod int64) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(dlPathKey(source)))
	_, _ = fmt.Fprintf(h, "|%d|%d", size, mod)
	return filepath.Join(mlibArtDir(), fmt.Sprintf("%016x.jpg", h.Sum64()))
}

// mlibWriteArt stores a poster through a temporary name, so a power cut never
// leaves half a picture under the real one.
func mlibWriteArt(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// mlibArtBudget is how many more posters the cache may take.
func mlibArtBudget() int {
	entries, err := os.ReadDir(mlibArtDir())
	if err != nil {
		return mlibArtMaxFiles
	}
	return mlibArtMaxFiles - len(entries)
}

// mlibScalePicture decodes a picture and stores it at poster size as a JPEG.
// The header is read first: a 48-megapixel scan is refused while it is still
// two integers.
func mlibScalePicture(src string) ([]byte, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > mlibArtMaxPixels {
		return nil, fmt.Errorf("%s is %dx%d", filepath.Base(src), cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	pic, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, mlibShrink(pic, mlibArtWidth, mlibArtMaxH), &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mlibShrink box-averages a picture down to fit maxW×maxH, keeping its shape.
// A picture that already fits is returned as it is.
func mlibShrink(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || (w <= maxW && h <= maxH) {
		return src
	}
	scale := float64(maxW) / float64(w)
	if s := float64(maxH) / float64(h); s < scale {
		scale = s
	}
	dw, dh := int(float64(w)*scale), int(float64(h)*scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0 := b.Min.Y + y*h/dh
		y1 := b.Min.Y + (y+1)*h/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		stepY := (y1 - y0) / 4
		if stepY < 1 {
			stepY = 1
		}
		for x := 0; x < dw; x++ {
			x0 := b.Min.X + x*w/dw
			x1 := b.Min.X + (x+1)*w/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			stepX := (x1 - x0) / 4
			if stepX < 1 {
				stepX = 1
			}
			var r, g, bl, n uint32
			for sy := y0; sy < y1; sy += stepY {
				for sx := x0; sx < x1; sx += stepX {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r += cr >> 8
					g += cg >> 8
					bl += cb >> 8
					n++
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n)
			dst.Pix[i+1] = uint8(g / n)
			dst.Pix[i+2] = uint8(bl / n)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

// mlibPruneArt deletes the posters no entry uses any more (the films and songs
// they were made for are gone), so the cache never outgrows the library.
func mlibPruneArt() {
	mlibState.mu.Lock()
	used := make(map[string]bool, len(mlibState.items))
	for _, it := range mlibState.items {
		if it.Art != "" {
			used[dlPathKey(it.Art)] = true
		}
		if it.Poster != "" {
			used[dlPathKey(it.Poster)] = true
		}
	}
	mlibState.mu.Unlock()
	dir := mlibArtDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !used[dlPathKey(path)] {
			_ = os.Remove(path)
		}
	}
}

// ── Saving and resume ────────────────────────────────────────────────────────

// mlibSave writes the index in the background; saves asked for while one is
// running are folded into one more write after it.
func mlibSave() {
	mlibState.mu.Lock()
	if mlibState.saving {
		mlibState.again = true
		mlibState.mu.Unlock()
		return
	}
	mlibState.saving = true
	mlibState.mu.Unlock()
	go func() {
		for {
			mlibState.mu.Lock()
			file := mlibIndexFile{
				Version: mlibIndexVersion,
				Items:   append([]mlibItem(nil), mlibState.items...),
				Dirs:    make(map[string]mlibDirState, len(mlibState.dirs)),
				Resume:  make(map[string]mlibResume, len(mlibState.resume)),
			}
			for k, v := range mlibState.dirs {
				file.Dirs[k] = v
			}
			for k, v := range mlibState.resume {
				file.Resume[k] = v
			}
			mlibState.again = false
			mlibState.mu.Unlock()
			mlibWriteIndex(file)
			mlibState.mu.Lock()
			if !mlibState.again {
				mlibState.saving = false
				mlibState.mu.Unlock()
				return
			}
			mlibState.mu.Unlock()
		}
	}()
}

// mlibWriteIndex writes one index snapshot through the crash-safe path the
// config and the Downloads library use.
func mlibWriteIndex(file mlibIndexFile) {
	if file.Items == nil {
		file.Items = []mlibItem{}
	}
	data, err := json.Marshal(file)
	if err != nil {
		logWarn("library", "index: %v", err)
		return
	}
	if err := AtomicWrite(mlibIndexPath(), data, 0o644); err != nil {
		logWarn("library", "could not save the index: %v", err)
	}
}

// mlibSyncResume copies where each library video was left from the player's
// recent list (which keeps twenty entries) into the library, which keeps them
// for good. It runs on its own goroutine when playback ends and when the
// Library opens.
func mlibSyncResume() {
	snap := favoritesSnapshot()
	changed := false
	mlibState.mu.Lock()
	if m := mlibState.model; m != nil {
		for _, it := range snap.Recent {
			v, ok := it.Data.(VideoInfo)
			if !ok || it.Type != "video" {
				continue
			}
			page := strings.TrimSpace(v.GetURL())
			if page == "" || strings.Contains(page, "://") {
				continue
			}
			key := dlPathKey(page)
			if _, listed := m.byPath[key]; !listed {
				continue
			}
			r := mlibResume{Position: v.Position, Duration: v.Duration, Played: it.Timestamp.Unix()}
			if cur, known := mlibState.resume[key]; known && cur == r {
				continue
			}
			if mlibState.resume == nil {
				mlibState.resume = map[string]mlibResume{}
			}
			mlibState.resume[key] = r
			changed = true
		}
	}
	mlibState.mu.Unlock()
	if changed {
		mlibRebuildModel()
		mlibSave()
	}
}

// mlibResumeAt is where the library last saw a video left, 0 when unknown or
// finished.
func mlibResumeAt(path string) float64 {
	m := mlibCurrentModel()
	if m == nil {
		return 0
	}
	i, ok := m.byPath[dlPathKey(path)]
	if !ok {
		return 0
	}
	it := m.items[i]
	if it.Duration > 0 && it.Position >= it.Duration*0.95 {
		return 0
	}
	return it.Position
}

// ── The model ────────────────────────────────────────────────────────────────

// mlibGroup is a TV show or an album.
type mlibGroup struct {
	key     string
	title   string
	sub     string
	art     string
	items   []int
	newest  int64
	seasons int
}

// mlibRef is one card of the Recently added row: a video (item), or an album
// (album); the other is -1.
type mlibRef struct {
	item  int
	album int
}

// mlibModel is the library grouped for the screen. It is built off the render
// thread and never changed once published.
type mlibModel struct {
	gen      uint64
	items    []mlibItem
	movies   []int
	shows    []mlibGroup
	albums   []mlibGroup
	recent   []mlibRef
	cont     []int
	byPath   map[string]int
	showIdx  map[string]int
	albumIdx map[string]int
}

// mlibAlbumKey groups songs: the artist and the album, without regard to case.
func mlibAlbumKey(artist, album string) string {
	return strings.ToLower(strings.TrimSpace(artist)) + "\x00" + strings.ToLower(strings.TrimSpace(album))
}

// mlibRebuildModel builds the grouped library from the live list and publishes
// it. It runs on the goroutine that changed the list, never on the render
// thread.
func mlibRebuildModel() {
	mlibBuildMu.Lock()
	defer mlibBuildMu.Unlock()
	mlibState.mu.Lock()
	items := append([]mlibItem(nil), mlibState.items...)
	resume := make(map[string]mlibResume, len(mlibState.resume))
	for k, v := range mlibState.resume {
		resume[k] = v
	}
	mlibState.mu.Unlock()
	m := mlibBuildModel(items, resume)
	mlibState.mu.Lock()
	mlibState.gen++
	m.gen = mlibState.gen
	mlibState.model = m
	mlibState.mu.Unlock()
}

// mlibBuildModel groups the library: films by title, shows with their episodes
// in order, albums with their songs in order, what is in progress, and what
// arrived last. resume carries where each video was left. It owns items.
func mlibBuildModel(items []mlibItem, resume map[string]mlibResume) *mlibModel {
	m := &mlibModel{
		items:    items,
		byPath:   make(map[string]int, len(items)),
		showIdx:  map[string]int{},
		albumIdx: map[string]int{},
	}
	titleKey := make([]string, len(m.items))
	for i := range m.items {
		it := &m.items[i]
		key := dlPathKey(it.Path)
		m.byPath[key] = i
		titleKey[i] = strings.ToLower(it.Title)
		if r, ok := resume[key]; ok {
			it.Position, it.Played = r.Position, r.Played
			if it.Duration <= 0 && r.Duration > 0 {
				it.Duration = r.Duration
			}
		}
		switch {
		case !it.Video:
			k := mlibAlbumKey(it.Artist, it.Album)
			gi, ok := m.albumIdx[k]
			if !ok {
				gi = len(m.albums)
				m.albumIdx[k] = gi
				m.albums = append(m.albums, mlibGroup{key: k, title: dlFirstNonEmpty(it.Album, "Unknown album"),
					sub: dlFirstNonEmpty(it.Artist, "Unknown artist")})
			}
			g := &m.albums[gi]
			g.items = append(g.items, i)
			if it.Added > g.newest {
				g.newest = it.Added
			}
		case it.Show != "":
			k := strings.ToLower(strings.TrimSpace(it.Show))
			gi, ok := m.showIdx[k]
			if !ok {
				gi = len(m.shows)
				m.showIdx[k] = gi
				m.shows = append(m.shows, mlibGroup{key: k, title: it.Show})
			}
			g := &m.shows[gi]
			g.items = append(g.items, i)
			if it.Added > g.newest {
				g.newest = it.Added
			}
		default:
			m.movies = append(m.movies, i)
		}
	}

	sort.SliceStable(m.movies, func(a, b int) bool {
		x, y := m.movies[a], m.movies[b]
		if titleKey[x] != titleKey[y] {
			return titleKey[x] < titleKey[y]
		}
		return m.items[x].Path < m.items[y].Path
	})
	sort.SliceStable(m.shows, func(a, b int) bool { return m.shows[a].key < m.shows[b].key })
	sort.SliceStable(m.albums, func(a, b int) bool { return m.albums[a].key < m.albums[b].key })
	for gi := range m.shows {
		g := &m.shows[gi]
		m.showIdx[g.key] = gi
		sort.SliceStable(g.items, func(a, b int) bool {
			x, y := &m.items[g.items[a]], &m.items[g.items[b]]
			if x.Season != y.Season {
				return x.Season < y.Season
			}
			if x.Episode != y.Episode {
				return x.Episode < y.Episode
			}
			return x.Path < y.Path
		})
		seasons := map[int]bool{}
		for _, i := range g.items {
			seasons[m.items[i].Season] = true
			if g.art == "" && m.items[i].Poster != "" {
				g.art = m.items[i].Poster
			}
		}
		g.seasons = len(seasons)
		for _, i := range g.items {
			if g.art != "" {
				break
			}
			g.art = m.items[i].Art
		}
	}
	for gi := range m.albums {
		g := &m.albums[gi]
		m.albumIdx[g.key] = gi
		sort.SliceStable(g.items, func(a, b int) bool {
			x, y := g.items[a], g.items[b]
			tx, ty := m.items[x].Track, m.items[y].Track
			switch {
			case tx > 0 && ty > 0 && tx != ty:
				return tx < ty
			case tx > 0 && ty == 0:
				return true
			case tx == 0 && ty > 0:
				return false
			}
			if titleKey[x] != titleKey[y] {
				return titleKey[x] < titleKey[y]
			}
			return m.items[x].Path < m.items[y].Path
		})
		for _, i := range g.items {
			if g.art != "" {
				break
			}
			g.art = m.items[i].Art
		}
	}

	// Continue watching: videos started and not finished, last played first.
	for i := range m.items {
		it := &m.items[i]
		if it.Video && it.Position > 0 && (it.Duration <= 0 || it.Position < it.Duration*0.95) {
			m.cont = append(m.cont, i)
		}
	}
	sort.SliceStable(m.cont, func(a, b int) bool { return m.items[m.cont[a]].Played > m.items[m.cont[b]].Played })

	// Recently added: the newest files, a song standing for its album.
	order := make([]int, len(m.items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := &m.items[order[a]], &m.items[order[b]]
		if x.Added != y.Added {
			return x.Added > y.Added
		}
		return x.Path < y.Path
	})
	seenAlbum := map[int]bool{}
	for _, i := range order {
		if len(m.recent) >= mlibRecentMax {
			break
		}
		it := &m.items[i]
		if it.Video {
			m.recent = append(m.recent, mlibRef{item: i, album: -1})
			continue
		}
		gi, ok := m.albumIdx[mlibAlbumKey(it.Artist, it.Album)]
		if !ok || seenAlbum[gi] {
			continue
		}
		seenAlbum[gi] = true
		m.recent = append(m.recent, mlibRef{item: -1, album: gi})
	}
	return m
}

// mlibNextEpisode is the episode a show's Play starts: the one in progress
// played last, else the one after the last one finished, else the first. It
// returns an index into m.items, -1 for an empty show.
func mlibNextEpisode(m *mlibModel, g mlibGroup) int {
	if len(g.items) == 0 {
		return -1
	}
	best, bestPlayed := -1, int64(0)
	lastDone, lastDonePlayed := -1, int64(0)
	for j, i := range g.items {
		it := m.items[i]
		if it.Played <= 0 {
			continue
		}
		if mediaProgressFraction(it.Position, it.Duration) > 0 {
			if it.Played > bestPlayed {
				best, bestPlayed = i, it.Played
			}
			continue
		}
		if it.Played > lastDonePlayed {
			lastDone, lastDonePlayed = j, it.Played
		}
	}
	if best >= 0 {
		return best
	}
	if lastDone >= 0 && lastDone+1 < len(g.items) {
		return g.items[lastDone+1]
	}
	return g.items[0]
}
