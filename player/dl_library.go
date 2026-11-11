package main

// dl_library.go — the Downloads library: every finished video or track kept on
// the card, listed with the metadata the Media screen draws it with, so it
// plays offline and survives a relaunch.
//
// downloads.go is the transfer queue, and it forgets its rows when the app
// exits: the files stayed on the card with nothing listing them. The library is
// the list that lasts — an index beside the media (downloads/library.json),
// reconciled with the two media folders whenever it loads, so a file an older
// build saved still appears and an entry whose file was deleted in Files goes
// away.
//
// It also keeps what the player already downloaded. When a stream has to be
// fetched whole before it can play (playWithTempFile in core.go), the finished
// file used to be deleted the moment playback ended. With "Keep watched videos"
// on (the default) it moves into the library instead. Only whole files are
// kept: the temp file exists only after yt-dlp exited cleanly (a transfer in
// progress is a .part beside it and never reaches the player), and
// dlTempLooksComplete checks that again before anything is kept.
//
// A watched video costs card space the user did not explicitly ask for, so the
// kept ones carry a storage limit (4 GB by default). Over it nothing is
// deleted behind the user's back: a toast says so, and the Downloads view marks
// the oldest kept videos that put the library over the limit and offers to
// remove exactly those.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// dlLibraryVersion is the index format written to library.json.
	dlLibraryVersion = 1
	// dlKeptLimitDefaultGB is the shipped limit for videos kept after watching.
	dlKeptLimitDefaultGB = 4
	// dlKeepMinFreeBytes is the space a kept video copied from another volume
	// must leave free. A rename on the same card costs no space at all.
	dlKeepMinFreeBytes = int64(512 << 20)
	// dlPosterBatch bounds how many missing posters one load extracts: each
	// is an ffmpeg run, and on the handheld that is a second of CPU.
	dlPosterBatch = 6
	// dlPosterTimeout bounds one poster extraction.
	dlPosterTimeout = 15 * time.Second
	// dlMoveAttempts and dlMoveRetry cover the moment after Stop: the killed
	// decoders can still hold the file, and Windows refuses to rename an open
	// file.
	dlMoveAttempts = 6
	dlMoveRetry    = 300 * time.Millisecond
)

// The two settings, stored in Custom like their neighbours and saved through
// syncVariableOverrides. An absent key means the shipped default.
const (
	dlKeepWatchedKey = "media_keep_watched"
	dlKeptLimitKey   = "media_kept_limit_gb"
)

// dlKeptLimitSteps is the order the Downloads view's limit chip cycles
// through; 0 means no limit.
var dlKeptLimitSteps = []int{1, 2, 4, 8, 16, 32, 0}

// errDlCardFull marks a kept video the card had no room for.
var errDlCardFull = errors.New("not enough free space on the card")

// dlLibraryEntry is one file the library lists.
type dlLibraryEntry struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Video     bool      `json:"video"`
	Title     string    `json:"title"`
	Channel   string    `json:"channel,omitempty"`
	Duration  float64   `json:"duration,omitempty"`
	Thumbnail string    `json:"thumbnail,omitempty"`
	ThumbFile string    `json:"thumbFile,omitempty"`
	Source    string    `json:"source,omitempty"`
	Size      int64     `json:"size"`
	Added     time.Time `json:"added"`
	// Kept marks a video saved after watching rather than downloaded on
	// purpose; only kept videos count against the storage limit.
	Kept bool `json:"kept,omitempty"`
}

// dlLibraryFile is the index on disk.
type dlLibraryFile struct {
	Version int              `json:"version"`
	Entries []dlLibraryEntry `json:"entries"`
}

// dlScannedFile is one media file found in the download folders.
type dlScannedFile struct {
	path  string
	size  int64
	mod   time.Time
	video bool
}

// dlLibrary is the live list. Every field is guarded by mu; views read copies
// (dlLibrarySnapshot) and rebuild their rows when gen moves.
var dlLibrary struct {
	mu      sync.Mutex
	loaded  bool
	loading bool
	entries []dlLibraryEntry // newest first
	gen     uint64
	saving  bool
	dirty   bool

	// removedPaths are the files removed while a load runs: its scan may have
	// listed them before they were deleted (dlPublishLoad).
	removedPaths map[string]bool
}

// The settings are mirrored for goroutines: Stop runs on decoder goroutines and
// must never read the live Custom map, which the render thread writes.
var (
	dlKeepWatchedOn atomic.Bool
	dlKeptLimitGB   atomic.Int32
	dlIDSeq         atomic.Uint64
	// dlToolsPath mirrors the config's tools_path (a string) for the poster
	// worker, so a configured ffmpeg is found without reading the live config.
	dlToolsPath atomic.Value
)

// dlPendingMeta carries the card metadata Media knows when it queues a link
// (title, channel, duration, artwork) to the moment the download lands.
var dlPendingMeta = struct {
	mu    sync.Mutex
	byURL map[string]VideoInfo
}{byURL: map[string]VideoInfo{}}

func init() {
	dlKeepWatchedOn.Store(true)
	dlKeptLimitGB.Store(dlKeptLimitDefaultGB)
}

// ── Locations ────────────────────────────────────────────────────────────────

// dlLibraryPath is the index file, inside the app's own download folder.
func dlLibraryPath() string { return filepath.Join(downloadsRoot(), "library.json") }

// dlThumbDir holds the posters saved beside the library, so a card keeps its
// picture with the network off.
func dlThumbDir() string { return filepath.Join(downloadsRoot(), ".thumbs") }

// dlPathKey compares paths the way the filesystem does: case-insensitively on
// Windows.
func dlPathKey(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if IsWindows() {
		p = strings.ToLower(p)
	}
	return p
}

// dlNewID returns an id no other entry of this run or an earlier one has.
func dlNewID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(dlIDSeq.Add(1), 36)
}

// dlTitleFromFile names a file the index knows nothing about: its name without
// the extension, which for JukaHub's own downloads is the video title.
func dlTitleFromFile(path string) string {
	base := filepath.Base(path)
	title := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if title == "" {
		return base
	}
	return title
}

// dlFirstNonEmpty returns the first value that is not blank.
func dlFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// dlShortTitle cuts a title to max runes for a toast.
func dlShortTitle(title string, max int) string {
	runes := []rune(strings.TrimSpace(title))
	if max <= 1 || len(runes) <= max {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// dlLibraryKey is how a page URL is matched against the library: the YouTube
// video id when there is one, so watch?v=, youtu.be/ and shorts/ links of one
// video match, otherwise the trimmed URL.
func dlLibraryKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if id := youtubeVideoID(raw); id != "" {
		return "yt:" + id
	}
	return raw
}

// dlSamePage reports whether two page URLs are the same video.
func dlSamePage(a, b string) bool {
	ka := dlLibraryKey(a)
	return ka != "" && ka == dlLibraryKey(b)
}

// dlThumbURLFor is the artwork a library card uses: YouTube's small 16:9 frame
// when the page is a YouTube video (fast to decode on the handheld), else the
// metadata's own thumbnail.
func dlThumbURLFor(meta VideoInfo, pageURL string) string {
	if u := webYouTubeThumbURL(strings.TrimSpace(pageURL)); u != "" {
		return u
	}
	return strings.TrimSpace(meta.Thumbnail)
}

// ── Settings ─────────────────────────────────────────────────────────────────

// dlCustomBool reads a Custom value that may have been written as a bool, a
// string or a number (JSON round trips and hand edits all happen).
func dlCustomBool(v interface{}, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "on", "yes":
			return true
		case "0", "false", "off", "no":
			return false
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return def
}

// dlCustomInt reads a Custom number the same forgiving way.
func dlCustomInt(v interface{}, def int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return def
}

// dlSettingsSync copies the two settings out of the live Custom map into the
// goroutine-safe mirrors. It runs on the main thread (mediaHubTick, the
// setting chips).
func dlSettingsSync(config *Config) {
	if config == nil || config.Variables.Custom == nil {
		return
	}
	dlKeepWatchedOn.Store(dlCustomBool(config.Variables.Custom[dlKeepWatchedKey], true))
	limit := dlCustomInt(config.Variables.Custom[dlKeptLimitKey], dlKeptLimitDefaultGB)
	if limit < 0 {
		limit = dlKeptLimitDefaultGB
	}
	dlKeptLimitGB.Store(int32(limit))
	dlToolsPath.Store(strings.TrimSpace(config.Variables.ToolsPath))
}

// dlKeepWatchedEnabled reports the "Keep watched videos" setting.
func dlKeepWatchedEnabled() bool { return dlKeepWatchedOn.Load() }

// dlSetKeepWatched turns "Keep watched videos" on or off and saves it.
func dlSetKeepWatched(config *Config, on bool) {
	dlKeepWatchedOn.Store(on)
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[dlKeepWatchedKey] = on
	syncVariableOverrides(config)
}

// mediaKeepWatchedEnabled reports "Keep fully watched videos" for the Settings
// hub's Media & playback row. The config's own value wins (the mirror is only
// refreshed while Media is on screen); a config without the key is the
// shipped default, on.
func mediaKeepWatchedEnabled(config *Config) bool {
	if config != nil && config.Variables.Custom != nil {
		if v, ok := config.Variables.Custom[dlKeepWatchedKey]; ok {
			return dlCustomBool(v, true)
		}
	}
	return dlKeepWatchedEnabled()
}

// mediaSetKeepWatched is the setter the Settings row calls (the cross-area
// name); it is the same switch as the Downloads view's chip.
func mediaSetKeepWatched(config *Config, on bool) {
	dlSetKeepWatched(config, on)
}

// dlShouldKeepWatched is the keep-or-delete decision for a stream that had to
// be downloaded whole before it could play: kept only with the setting on, a
// page to file it under, and a file yt-dlp finished.
func dlShouldKeepWatched(keepOn bool, pageURL string, complete bool) bool {
	return keepOn && strings.TrimSpace(pageURL) != "" && complete
}

// dlKeptLimitGBValue is the limit in GB (0 = no limit).
func dlKeptLimitGBValue() int { return int(dlKeptLimitGB.Load()) }

// dlKeptLimitBytes is the limit in bytes (0 = no limit).
func dlKeptLimitBytes() int64 {
	gb := int64(dlKeptLimitGB.Load())
	if gb <= 0 {
		return 0
	}
	return gb << 30
}

// dlKeptLimitLabel names a limit for the chip.
func dlKeptLimitLabel(gb int) string {
	if gb <= 0 {
		return "No limit"
	}
	return fmt.Sprintf("%d GB", gb)
}

// dlNextKeptLimit is the step after current on the chip, wrapping to the first.
func dlNextKeptLimit(current int) int {
	for i, step := range dlKeptLimitSteps {
		if step == current {
			return dlKeptLimitSteps[(i+1)%len(dlKeptLimitSteps)]
		}
	}
	return dlKeptLimitSteps[0]
}

// dlCycleKeptLimit steps the limit chip, saves it and returns the new value.
func dlCycleKeptLimit(config *Config) int {
	next := dlNextKeptLimit(dlKeptLimitGBValue())
	dlKeptLimitGB.Store(int32(next))
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[dlKeptLimitKey] = next
		syncVariableOverrides(config)
	}
	return next
}

// ── Loading ──────────────────────────────────────────────────────────────────

// dlLibraryEnsureLoaded starts the first load in the background. Views draw a
// loading state until it lands; nothing reads the card on the render thread.
func dlLibraryEnsureLoaded() {
	dlLibrary.mu.Lock()
	if dlLibrary.loaded || dlLibrary.loading {
		dlLibrary.mu.Unlock()
		return
	}
	dlLibrary.loading = true
	dlLibrary.mu.Unlock()
	go dlLibraryLoad()
}

// dlLibraryRefresh re-reads the folders in the background, so a file deleted or
// copied in through Files is reflected the next time Downloads opens.
func dlLibraryRefresh() {
	dlLibrary.mu.Lock()
	if dlLibrary.loading {
		dlLibrary.mu.Unlock()
		return
	}
	dlLibrary.loading = true
	dlLibrary.mu.Unlock()
	go dlLibraryLoad()
}

// dlLibraryLoadNow loads the index on the calling goroutine when no load has
// run yet, so a background add never races the first load into duplicate rows.
func dlLibraryLoadNow() {
	dlLibrary.mu.Lock()
	if dlLibrary.loaded {
		dlLibrary.mu.Unlock()
		return
	}
	if dlLibrary.loading {
		dlLibrary.mu.Unlock()
		for i := 0; i < 200; i++ {
			time.Sleep(50 * time.Millisecond)
			dlLibrary.mu.Lock()
			done := dlLibrary.loaded
			dlLibrary.mu.Unlock()
			if done {
				return
			}
		}
		return
	}
	dlLibrary.loading = true
	dlLibrary.mu.Unlock()
	dlLibraryLoad()
}

// dlLibraryLoad reads the folders, reconciles them with what the library knew
// and publishes the result. The caller has set dlLibrary.loading.
//
// The first load reconciles the index on disk; a refresh reconciles the live
// list, which is never older than the index. Rows added or removed while the
// load ran are respected (dlPublishLoad), so a refresh can neither bring back
// a download that was just removed nor drop one that just landed.
func dlLibraryLoad() {
	dlLibrary.mu.Lock()
	first := !dlLibrary.loaded
	start := append([]dlLibraryEntry(nil), dlLibrary.entries...)
	dlLibrary.removedPaths = map[string]bool{}
	dlLibrary.mu.Unlock()

	index := start
	startIDs := make(map[string]bool, len(start))
	if first {
		var file dlLibraryFile
		if _, err := readJSONWithBackup(dlLibraryPath(), &file); err != nil && !os.IsNotExist(err) {
			logWarn("downloads", "library index unreadable, rebuilding from the folders: %v", err)
		}
		index = file.Entries
	} else {
		for _, e := range start {
			startIDs[e.ID] = true
		}
	}
	files, unreadable := dlScanMediaFolders()
	entries, removed, changed := dlReconcile(index, files, unreadable)
	for _, gone := range removed {
		if gone.ThumbFile != "" && isPathWithin(gone.ThumbFile, downloadsRoot()) {
			_ = os.Remove(gone.ThumbFile)
		}
	}

	dlLibrary.mu.Lock()
	merged := dlPublishLoad(entries, dlLibrary.entries, startIDs, dlLibrary.removedPaths)
	if len(merged) != len(entries) {
		changed = true
	}
	dlLibrary.entries = merged
	dlLibrary.removedPaths = nil
	dlLibrary.loaded = true
	dlLibrary.loading = false
	dlLibrary.gen++
	dlLibrary.mu.Unlock()

	if changed {
		dlLibrarySave()
	}
	dlLibraryFillPosters()
}

// dlScanMediaFolders lists the media files in the video and music folders. It
// also returns the folders that exist but could not be listed: their rows are
// kept as they were rather than dropped for a passing card error.
func dlScanMediaFolders() ([]dlScannedFile, []string) {
	var out []dlScannedFile
	var unreadable []string
	for _, dir := range []struct {
		path  string
		video bool
	}{
		{downloadsVideoDir(), true},
		{downloadsMusicDir(), false},
	} {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			if !os.IsNotExist(err) {
				logWarn("downloads", "cannot list %s: %v", dir.path, err)
				unreadable = append(unreadable, dir.path)
			}
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !e.Type().IsRegular() || skipScanEntryName(name) || !isMediaFile(name) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() <= 0 {
				continue
			}
			out = append(out, dlScannedFile{
				path:  filepath.Join(dir.path, name),
				size:  info.Size(),
				mod:   info.ModTime(),
				video: dir.video,
			})
		}
	}
	return out, unreadable
}

// dlInAnyDir reports whether path lies inside one of dirs.
func dlInAnyDir(path string, dirs []string) bool {
	for _, d := range dirs {
		if isPathWithin(path, d) {
			return true
		}
	}
	return false
}

// dlReconcile is the load's pure core: the index entries whose files still
// exist (sizes refreshed), plus an entry for every file the index did not
// know, newest first. An entry inside a folder that could not be listed
// (unreadable) is kept unchanged. It also returns the entries that were
// dropped, and whether anything changed against the index.
func dlReconcile(index []dlLibraryEntry, files []dlScannedFile, unreadable []string) (kept, removed []dlLibraryEntry, changed bool) {
	onDisk := make(map[string]dlScannedFile, len(files))
	for _, f := range files {
		onDisk[dlPathKey(f.path)] = f
	}
	seen := make(map[string]bool, len(index))
	kept = make([]dlLibraryEntry, 0, len(files))
	for _, e := range index {
		key := dlPathKey(e.Path)
		if seen[key] {
			removed = append(removed, e)
			changed = true
			continue
		}
		f, ok := onDisk[key]
		if !ok && dlInAnyDir(e.Path, unreadable) {
			// The folder could not be listed this time: keep the row as it
			// was rather than lose its metadata to a passing card error.
			seen[key] = true
			kept = append(kept, e)
			continue
		}
		if !ok {
			removed = append(removed, e)
			changed = true
			continue
		}
		seen[key] = true
		if e.Size != f.size {
			e.Size = f.size
			changed = true
		}
		if e.ID == "" {
			e.ID = dlNewID()
			changed = true
		}
		if strings.TrimSpace(e.Title) == "" {
			e.Title = dlTitleFromFile(e.Path)
			changed = true
		}
		kept = append(kept, e)
	}
	for _, f := range files {
		key := dlPathKey(f.path)
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, dlLibraryEntry{
			ID:    dlNewID(),
			Path:  f.path,
			Video: f.video,
			Title: dlTitleFromFile(f.path),
			Size:  f.size,
			Added: f.mod,
		})
		changed = true
	}
	dlSortNewestFirst(kept)
	return kept, removed, changed
}

// dlPublishLoad combines a finished load with the live list, one row per file.
// loaded is what the load reconciled; live is the list now; startIDs are the
// ids the list had when a refresh started (empty for the first load); removed
// holds the paths deleted while the load ran.
//
//   - A row the refresh started from that is no longer live was removed while
//     it ran, and stays removed.
//   - A live row that was not there when the load started was added while it
//     ran, and wins over a scanned row for the same file.
//   - A row in both keeps its live fields (a poster saved meanwhile) and the
//     size the scan measured.
func dlPublishLoad(loaded, live []dlLibraryEntry, startIDs, removed map[string]bool) []dlLibraryEntry {
	liveByID := make(map[string]int, len(live))
	livePaths := make(map[string]bool, len(live))
	for i, e := range live {
		liveByID[e.ID] = i
		if !startIDs[e.ID] {
			livePaths[dlPathKey(e.Path)] = true
		}
	}
	out := make([]dlLibraryEntry, 0, len(loaded)+len(live))
	seen := make(map[string]bool, len(loaded)+len(live))
	for _, e := range loaded {
		if startIDs[e.ID] {
			i, ok := liveByID[e.ID]
			if !ok {
				continue
			}
			cur := live[i]
			cur.Size = e.Size
			if strings.TrimSpace(cur.Title) == "" {
				cur.Title = e.Title
			}
			e = cur
		} else if key := dlPathKey(e.Path); removed[key] || livePaths[key] {
			continue
		}
		key := dlPathKey(e.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	for _, e := range live {
		if startIDs[e.ID] {
			continue
		}
		key := dlPathKey(e.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	dlSortNewestFirst(out)
	return out
}

// dlSortNewestFirst orders entries by the moment they were added.
func dlSortNewestFirst(entries []dlLibraryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Added.After(entries[j].Added)
	})
}

// ── Saving ───────────────────────────────────────────────────────────────────

// dlLibrarySave writes the index in the background. Changes made while a write
// is running are coalesced into one more write after it.
func dlLibrarySave() {
	dlLibrary.mu.Lock()
	if dlLibrary.saving {
		dlLibrary.dirty = true
		dlLibrary.mu.Unlock()
		return
	}
	dlLibrary.saving = true
	dlLibrary.mu.Unlock()
	go func() {
		for {
			dlLibrary.mu.Lock()
			snapshot := append([]dlLibraryEntry(nil), dlLibrary.entries...)
			dlLibrary.dirty = false
			dlLibrary.mu.Unlock()
			dlLibraryWrite(snapshot)
			dlLibrary.mu.Lock()
			if !dlLibrary.dirty {
				dlLibrary.saving = false
				dlLibrary.mu.Unlock()
				return
			}
			dlLibrary.mu.Unlock()
		}
	}()
}

// dlLibraryWrite writes one index snapshot through the crash-safe path the
// config and the favorites use.
func dlLibraryWrite(entries []dlLibraryEntry) {
	if entries == nil {
		entries = []dlLibraryEntry{}
	}
	data, err := json.MarshalIndent(dlLibraryFile{Version: dlLibraryVersion, Entries: entries}, "", "  ")
	if err != nil {
		logWarn("downloads", "library index: %v", err)
		return
	}
	if err := AtomicWrite(dlLibraryPath(), data, 0o644); err != nil {
		logWarn("downloads", "could not save the library index: %v", err)
	}
}

// ── Reading and changing ─────────────────────────────────────────────────────

// dlLibrarySnapshot copies the list for a view: the entries newest first, the
// change counter, and whether the first load has finished.
func dlLibrarySnapshot() ([]dlLibraryEntry, uint64, bool) {
	dlLibrary.mu.Lock()
	defer dlLibrary.mu.Unlock()
	return append([]dlLibraryEntry(nil), dlLibrary.entries...), dlLibrary.gen, dlLibrary.loaded
}

// dlLibraryGen is the change counter alone, cheap enough to poll every frame.
func dlLibraryGen() uint64 {
	dlLibrary.mu.Lock()
	defer dlLibrary.mu.Unlock()
	return dlLibrary.gen
}

// dlLibraryFindSource returns the video (video true) or audio-only entry
// downloaded from a page, if any. The two are different files of one page: a
// track saved for listening is not the video, and must never stand in for it
// (or be deleted in its place).
func dlLibraryFindSource(pageURL string, video bool) (dlLibraryEntry, bool) {
	key := dlLibraryKey(pageURL)
	if key == "" {
		return dlLibraryEntry{}, false
	}
	dlLibrary.mu.Lock()
	defer dlLibrary.mu.Unlock()
	for _, e := range dlLibrary.entries {
		if e.Video == video && e.Source != "" && dlLibraryKey(e.Source) == key {
			return e, true
		}
	}
	return dlLibraryEntry{}, false
}

// dlLibraryFindID returns the entry with an id, if it is still listed.
func dlLibraryFindID(id string) (dlLibraryEntry, bool) {
	if id == "" {
		return dlLibraryEntry{}, false
	}
	dlLibrary.mu.Lock()
	defer dlLibrary.mu.Unlock()
	for _, e := range dlLibrary.entries {
		if e.ID == id {
			return e, true
		}
	}
	return dlLibraryEntry{}, false
}

// dlLibraryFindPath returns the entry for a file, if the library lists it.
func dlLibraryFindPath(path string) (dlLibraryEntry, bool) {
	if strings.TrimSpace(path) == "" {
		return dlLibraryEntry{}, false
	}
	key := dlPathKey(path)
	dlLibrary.mu.Lock()
	defer dlLibrary.mu.Unlock()
	for _, e := range dlLibrary.entries {
		if dlPathKey(e.Path) == key {
			return e, true
		}
	}
	return dlLibraryEntry{}, false
}

// dlLibrarySourceSet returns the match keys of every page downloaded as a
// video, for a view that badges many video cards at once.
func dlLibrarySourceSet() map[string]bool {
	dlLibrary.mu.Lock()
	defer dlLibrary.mu.Unlock()
	set := make(map[string]bool, len(dlLibrary.entries))
	for _, e := range dlLibrary.entries {
		if !e.Video {
			continue
		}
		if key := dlLibraryKey(e.Source); key != "" {
			set[key] = true
		}
	}
	return set
}

// dlLibraryAdd records a finished file, newest first. A row for the same path
// is replaced (a download saved over an older one of the same name).
func dlLibraryAdd(e dlLibraryEntry) {
	if strings.TrimSpace(e.Path) == "" {
		return
	}
	if e.ID == "" {
		e.ID = dlNewID()
	}
	if e.Added.IsZero() {
		e.Added = time.Now()
	}
	key := dlPathKey(e.Path)
	dlLibrary.mu.Lock()
	out := make([]dlLibraryEntry, 0, len(dlLibrary.entries)+1)
	out = append(out, e)
	for _, cur := range dlLibrary.entries {
		if dlPathKey(cur.Path) != key {
			out = append(out, cur)
		}
	}
	dlLibrary.entries = out
	dlLibrary.gen++
	dlLibrary.mu.Unlock()
	dlLibrarySave()
}

// dlLibraryUpdate edits one entry in place and saves.
func dlLibraryUpdate(id string, edit func(*dlLibraryEntry)) bool {
	if edit == nil {
		return false
	}
	dlLibrary.mu.Lock()
	found := false
	for i := range dlLibrary.entries {
		if dlLibrary.entries[i].ID == id {
			edit(&dlLibrary.entries[i])
			found = true
			break
		}
	}
	if found {
		dlLibrary.gen++
	}
	dlLibrary.mu.Unlock()
	if found {
		dlLibrarySave()
	}
	return found
}

// dlLibraryRemove drops an entry and deletes its file and poster in the
// background. It returns the entry so the caller can say what was freed.
func dlLibraryRemove(id string) (dlLibraryEntry, bool) {
	dlLibrary.mu.Lock()
	idx := -1
	for i := range dlLibrary.entries {
		if dlLibrary.entries[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		dlLibrary.mu.Unlock()
		return dlLibraryEntry{}, false
	}
	e := dlLibrary.entries[idx]
	out := make([]dlLibraryEntry, 0, len(dlLibrary.entries)-1)
	out = append(out, dlLibrary.entries[:idx]...)
	out = append(out, dlLibrary.entries[idx+1:]...)
	dlLibrary.entries = out
	dlLibrary.gen++
	if dlLibrary.loading && dlLibrary.removedPaths != nil {
		dlLibrary.removedPaths[dlPathKey(e.Path)] = true
	}
	dlLibrary.mu.Unlock()

	dlForgetQueuePath(e.Path)
	dlLibrarySave()
	go dlDeleteEntryFiles(e)
	return e, true
}

// dlDeleteEntryFiles removes an entry's media and poster. Only files under the
// app's own download folder are ever deleted — the rule downloadRemove keeps —
// and a file the player still holds open is retried for a moment.
func dlDeleteEntryFiles(e dlLibraryEntry) {
	root := downloadsRoot()
	if e.Path != "" && isPathWithin(e.Path, root) {
		var err error
		for attempt := 0; attempt < dlMoveAttempts; attempt++ {
			if err = os.Remove(e.Path); err == nil || os.IsNotExist(err) {
				err = nil
				break
			}
			time.Sleep(dlMoveRetry)
		}
		if err != nil {
			logWarn("downloads", "could not delete %s: %v", filepath.Base(e.Path), err)
		} else {
			// The subtitles saved beside a video go with it.
			removeSubtitleSidecars(e.Path)
		}
	}
	if e.ThumbFile != "" && isPathWithin(e.ThumbFile, root) {
		_ = os.Remove(e.ThumbFile)
	}
	mediaNoteFreed()
}

// dlLibraryTotals sums the library: every file, and the kept ones alone.
func dlLibraryTotals(entries []dlLibraryEntry) (all, kept int64) {
	for _, e := range entries {
		all += e.Size
		if e.Kept {
			kept += e.Size
		}
	}
	return all, kept
}

// dlOverLimit returns the kept videos to remove, oldest first, to bring the
// kept total back under limit (0 = no limit), and the kept total itself.
func dlOverLimit(entries []dlLibraryEntry, limit int64) (map[string]bool, int64) {
	kept := make([]dlLibraryEntry, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.Kept {
			kept = append(kept, e)
			total += e.Size
		}
	}
	if limit <= 0 || total <= limit {
		return nil, total
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Added.Before(kept[j].Added) })
	over := make(map[string]bool, 4)
	left := total
	for _, e := range kept {
		if left <= limit {
			break
		}
		over[e.ID] = true
		left -= e.Size
	}
	return over, total
}

// dlCheckKeptLimit warns once a kept video takes the library over the limit.
func dlCheckKeptLimit() {
	entries, _, _ := dlLibrarySnapshot()
	limit := dlKeptLimitBytes()
	over, kept := dlOverLimit(entries, limit)
	if len(over) == 0 {
		return
	}
	ShowToast(fmt.Sprintf("Watched videos use %s, over your %s limit. Remove the oldest in Media's Downloads tab.",
		humanBytes(kept), humanBytes(limit)), ToastKindWarning)
}

// ── Media handing metadata over ──────────────────────────────────────────────

// dlRememberMeta keeps a card's metadata until the download of its link lands.
func dlRememberMeta(url string, v VideoInfo) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	dlPendingMeta.mu.Lock()
	if len(dlPendingMeta.byURL) > 64 {
		// A bounded map: links that never finished are not worth keeping.
		dlPendingMeta.byURL = map[string]VideoInfo{}
	}
	dlPendingMeta.byURL[url] = v
	dlPendingMeta.mu.Unlock()
}

// dlTakePendingMeta returns and forgets the metadata for a link.
func dlTakePendingMeta(url string) (VideoInfo, bool) {
	url = strings.TrimSpace(url)
	dlPendingMeta.mu.Lock()
	defer dlPendingMeta.mu.Unlock()
	v, ok := dlPendingMeta.byURL[url]
	delete(dlPendingMeta.byURL, url)
	return v, ok
}

// dlLibraryFilingOff stops dlLibraryNoteSaved from filing finished queue
// downloads. The queue's own tests set it, so no library goroutine outlives a
// test (and its temporary folder).
var dlLibraryFilingOff atomic.Bool

// dlLibraryNoteSaved files a finished queue download in the library. It is
// called by the queue worker after it released its lock.
func dlLibraryNoteSaved(it downloadItem) {
	if dlLibraryFilingOff.Load() || strings.TrimSpace(it.Path) == "" {
		return
	}
	meta, _ := dlTakePendingMeta(it.URL)
	go func() {
		size := it.Done
		if info, err := os.Stat(it.Path); err == nil {
			size = info.Size()
		}
		entry := dlLibraryEntry{
			ID:        dlNewID(),
			Path:      it.Path,
			Video:     it.Kind == downloadKindVideo,
			Title:     dlFirstNonEmpty(meta.Title, it.Title, dlTitleFromFile(it.Path)),
			Channel:   dlFirstNonEmpty(meta.Uploader, meta.Channel, it.Source),
			Duration:  meta.Duration,
			Thumbnail: dlThumbURLFor(meta, it.URL),
			Source:    strings.TrimSpace(it.URL),
			Size:      size,
			Added:     time.Now(),
		}
		dlLibraryLoadNow()
		dlLibraryAdd(entry)
		dlFetchPoster(entry)
	}()
}

// ── Keeping watched streams ──────────────────────────────────────────────────

// dlTempLooksComplete reports whether a temp download is a whole file: present,
// larger than an error page, and with no yt-dlp partial beside it.
func dlTempLooksComplete(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < downloadMinimumBytes(downloadKindVideo) {
		return false
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		return false
	}
	return true
}

// dlWatchedMeta is what the player knew about the page it played: the video
// recorded in the recent list when playback started.
func dlWatchedMeta(pageURL string) VideoInfo {
	pageURL = strings.TrimSpace(pageURL)
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for _, it := range favoritesStore.Recent {
		if v, ok := it.Data.(VideoInfo); ok && it.Type == "video" && strings.TrimSpace(v.GetURL()) == pageURL {
			return v
		}
	}
	return VideoInfo{WebpageURL: pageURL}
}

// dlReleaseTempPlayback decides what becomes of a finished temp download when
// its playback ends: kept in the library when "Keep watched videos" is on and
// the file is whole, deleted otherwise (the old behaviour). It runs wherever
// Stop runs, so the move itself happens on its own goroutine.
func dlReleaseTempPlayback(tempPath, pageURL string) {
	keepOn := dlKeepWatchedOn.Load()
	if !dlShouldKeepWatched(keepOn, pageURL, keepOn && dlTempLooksComplete(tempPath)) {
		removeTempVideo(tempPath)
		return
	}
	meta := dlWatchedMeta(pageURL)
	go dlKeepWatched(tempPath, pageURL, meta)
}

// dlKeepWatched moves a watched download into the library with its metadata.
func dlKeepWatched(tempPath, pageURL string, meta VideoInfo) {
	dlLibraryLoadNow()
	if _, ok := dlLibraryFindSource(pageURL, true); ok {
		// Already downloaded (another quality, an earlier watch): one copy.
		removeTempVideo(tempPath)
		return
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return
	}
	if err := os.MkdirAll(downloadsVideoDir(), 0o755); err != nil {
		logWarn("downloads", "cannot create the video folder: %v", err)
		removeTempVideo(tempPath)
		return
	}
	title := strings.TrimSpace(meta.Title)
	name := safeDownloadName(title)
	if name == "" {
		name = "Video " + time.Now().Format("2006-01-02 15.04")
	}
	ext := strings.ToLower(filepath.Ext(tempPath))
	if ext == "" {
		ext = ".mp4"
	}
	dest := uniqueDownloadPath(downloadsVideoDir(), name, ext)
	if err := dlMoveFile(tempPath, dest, info.Size()); err != nil {
		logWarn("downloads", "could not keep the watched video: %v", err)
		removeTempVideo(tempPath)
		if errors.Is(err, errDlCardFull) {
			ShowToast("Not kept: the card is almost full", ToastKindWarning)
		}
		return
	}
	entry := dlLibraryEntry{
		ID:        dlNewID(),
		Path:      dest,
		Video:     true,
		Title:     dlFirstNonEmpty(title, name),
		Channel:   dlFirstNonEmpty(meta.Uploader, meta.Channel),
		Duration:  meta.Duration,
		Thumbnail: dlThumbURLFor(meta, pageURL),
		Source:    strings.TrimSpace(pageURL),
		Size:      info.Size(),
		Added:     time.Now(),
		Kept:      true,
	}
	dlLibraryAdd(entry)
	ShowToast("Saved to Downloads: "+dlShortTitle(entry.Title, 48), ToastKindSuccess)
	dlCheckKeptLimit()
	dlKeepCachedSubtitles(pageURL, dest)
	dlFetchPoster(entry)
}

// dlKeepCachedSubtitles saves the subtitles a kept video was watched with
// beside its file, from the subtitle cache (ytcaptions.go): no network, so a
// kept video plays offline with the subtitles it had.
func dlKeepCachedSubtitles(pageURL, videoPath string) {
	lang, err := subtitleLanguage(appConfig)
	if err != nil {
		return
	}
	cues := subtitleCacheLoad(pageURL, lang)
	if len(cues) == 0 {
		return
	}
	if _, err := writeSubtitleSidecar(videoPath, lang, cues); err != nil {
		logDebug("downloads", "kept video without its subtitles: %v", err)
	}
}

// dlMoveFile moves src to dst: a rename on the same card (free and instant),
// retried while the stopped decoders release the file, else a copy when the
// card has room for it.
func dlMoveFile(src, dst string, size int64) error {
	var err error
	for attempt := 0; attempt < dlMoveAttempts; attempt++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		time.Sleep(dlMoveRetry)
	}
	if free, ferr := freeBytes(filepath.Dir(dst)); ferr == nil && free > 0 && free-size < dlKeepMinFreeBytes {
		return errDlCardFull
	}
	if cerr := copyFile(src, dst); cerr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("rename failed (%v), copy failed: %w", err, cerr)
	}
	_ = os.Remove(src)
	return nil
}

// ── Posters ──────────────────────────────────────────────────────────────────

// dlThumbBytes returns artwork bytes from the thumbnail caches, or from the
// network while online. Offline it never dials.
func dlThumbBytes(raw string) []byte {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	url := normalizeThumbnailURL(raw)
	if data := thumbnailDataGet(url); len(data) > 0 {
		return data
	}
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		if data, cErr := cacheGet(db, thumbnailDiskPrefix+url); cErr == nil && len(data) > 0 {
			return data
		}
	}
	if !IsOnline() {
		return nil
	}
	data, err := fetchThumbnailBytes(url)
	if err != nil {
		return nil
	}
	return data
}

// dlGrabPoster takes a frame from a video file with the bundled ffmpeg, for a
// file that came with no artwork.
func dlGrabPoster(path string) []byte {
	// A throwaway config carrying only tools_path: getToolPath looks there
	// first, then beside the binary and on PATH.
	toolsPath, _ := dlToolsPath.Load().(string)
	ffmpeg := strings.TrimSpace(getToolPath("ffmpeg", &Config{Variables: Variables{ToolsPath: toolsPath}}))
	if ffmpeg == "" {
		return nil
	}
	if info, err := os.Stat(ffmpeg); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	tmp, err := os.CreateTemp("", "jukahub-poster-*.jpg")
	if err != nil {
		return nil
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(name)
	// Three seconds in is a picture rather than the black fade most clips open
	// with; a shorter clip falls back to its first frame.
	for _, seek := range []string{"3", "0"} {
		err := runQuietCommand(ffmpeg, []string{
			"-loglevel", "error", "-ss", seek, "-i", path,
			"-frames:v", "1", "-vf", "scale=320:-2", "-y", name,
		}, dlPosterTimeout)
		if err != nil {
			continue
		}
		if data, rerr := os.ReadFile(name); rerr == nil && len(data) > 0 {
			return data
		}
	}
	return nil
}

// dlPosterGrabs remembers the files ffmpeg could not take a frame from during
// this run, so every refresh of the library does not spend another ffmpeg run
// (seconds of CPU on the handheld) on the same file.
var dlPosterGrabs = struct {
	mu     sync.Mutex
	failed map[string]bool
}{failed: map[string]bool{}}

// dlPosterGrabber takes a poster frame from a file; tests swap it so the suite
// never runs ffmpeg.
var dlPosterGrabber = dlGrabPoster

// dlFetchPoster saves an entry's artwork beside the library so its card keeps
// a picture offline: the bytes the Media grid already cached, the network when
// online, or a frame of the file itself.
func dlFetchPoster(e dlLibraryEntry) {
	if e.ThumbFile != "" {
		return
	}
	data := dlThumbBytes(e.Thumbnail)
	if len(data) == 0 && e.Video {
		dlPosterGrabs.mu.Lock()
		tried := dlPosterGrabs.failed[e.ID]
		dlPosterGrabs.mu.Unlock()
		if !tried {
			data = dlPosterGrabber(e.Path)
			if len(data) == 0 {
				dlPosterGrabs.mu.Lock()
				dlPosterGrabs.failed[e.ID] = true
				dlPosterGrabs.mu.Unlock()
			}
		}
	}
	if len(data) == 0 {
		return
	}
	if err := os.MkdirAll(dlThumbDir(), 0o755); err != nil {
		return
	}
	out := filepath.Join(dlThumbDir(), e.ID+".jpg")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		logWarn("downloads", "could not save a poster: %v", err)
		return
	}
	dlLibraryUpdate(e.ID, func(cur *dlLibraryEntry) { cur.ThumbFile = out })
}

// dlLibraryFillPosters gives a few entries without artwork a poster after a
// load, and forgets posters whose file has gone.
func dlLibraryFillPosters() {
	entries, _, _ := dlLibrarySnapshot()
	done := 0
	for _, e := range entries {
		if e.ThumbFile != "" {
			if _, err := os.Stat(e.ThumbFile); err == nil {
				continue
			}
			dlLibraryUpdate(e.ID, func(cur *dlLibraryEntry) { cur.ThumbFile = "" })
			e.ThumbFile = ""
		}
		if done >= dlPosterBatch {
			continue
		}
		if e.Thumbnail == "" && !e.Video {
			continue
		}
		done++
		dlFetchPoster(e)
	}
}
