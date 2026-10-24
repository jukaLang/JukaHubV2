package main

// downloads.go — offline music downloads for the audio player.
//
// JukaHub streams YouTube, which needs the network the whole time. This file is
// the other half: a small, deliberate queue that saves audio onto the SD card so
// the audio player keeps working on a train, on a plane, or with Wi-Fi off.
//
// One track downloads at a time. The device has 1 GB of shared RAM and a
// standalone yt-dlp is already a heavy child; running two would starve the UI
// long before it made anything faster. Pausing cancels the running yt-dlp and
// puts its row back in the queue, and the partial file is kept so resuming
// continues instead of starting over.
//
// Everything a test needs is a plain function of plain values: the queue walk,
// the progress parser, the storage split and the file naming. Only the runner
// touches the network, and it is swappable.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// downloadQueueLimit is how many finished tracks the panel keeps. The files stay
// on the card; only the rows are forgotten, so a week of listening cannot turn
// the list into a second file manager.
const downloadQueueLimit = 20

// downloadStatus is one row's lifecycle. Queued covers both "waiting" and
// "paused" because the queue is the same list either way.
type downloadStatus int

const (
	downloadQueued downloadStatus = iota
	downloadActive
	downloadDone
	downloadFailed
)

// downloadKind is what a row saves. Audio is the long-standing m4a path; video
// is an MP4 merged by the bundled ffmpeg so the embedded player can decode it.
// The zero value is audio, so rows written before the split stay valid.
type downloadKind int

const (
	downloadKindAudio downloadKind = iota
	downloadKindVideo
)

// label is what the panel and the toasts call the row.
func (k downloadKind) label() string {
	if k == downloadKindVideo {
		return "Video"
	}
	return "Audio"
}

// downloadItem is one track or video the user asked to keep. Fields are only
// written under downloads.mu; the panel reads copies (see downloadsSnapshot).
type downloadItem struct {
	ID     int64
	URL    string
	Kind   downloadKind
	Title  string // empty until yt-dlp reports the real title
	Source string // uploader / channel, may stay empty
	Status downloadStatus

	Done  int64   // bytes written so far
	Total int64   // bytes expected, 0 while unknown
	Speed float64 // bytes per second, 0 when unknown

	Err  string // failure detail for a failed row
	Path string // file on the card once the row is done

	added    time.Time
	finished time.Time
}

// displayTitle is what a row shows before metadata arrives.
func (it downloadItem) displayTitle() string {
	if title := strings.TrimSpace(it.Title); title != "" {
		return title
	}
	if it.Status == downloadActive || it.Status == downloadQueued {
		return "Resolving link…"
	}
	// A failed row can fail before any title arrived, so name the host instead
	// of leaving the row anonymous.
	if u, err := url.Parse(it.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return "Track"
}

// percent is the row's progress as 0..1. A row with no Content-Length reports 0
// so the UI draws an indeterminate bar instead of a fake one.
func (it downloadItem) percent() float64 {
	if it.Status == downloadDone {
		return 1
	}
	if it.Total <= 0 || it.Done <= 0 {
		return 0
	}
	p := float64(it.Done) / float64(it.Total)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// downloadJob is everything the runner needs, copied out of the item so the
// runner never reaches into the queue.
type downloadJob struct {
	ID   int64
	URL  string
	Kind downloadKind
	Dir  string
}

// downloadResult is what a finished job hands back.
type downloadResult struct {
	Path   string
	Title  string
	Source string
}

// downloadProgress is one progress callback. Title/Source arrive once, before
// the transfer starts; the counters arrive repeatedly.
type downloadProgress struct {
	Title   string
	Source  string
	Percent float64
	Done    int64
	Total   int64
	Speed   float64
}

// downloadRunner performs one job. Tests swap it so the queue can be exercised
// without a network, a binary or a card.
var downloadRunner = runQueuedDownload

// downloadQueue is the whole queue. Every field is read and written under mu.
type downloadQueue struct {
	mu      sync.Mutex
	items   []*downloadItem
	nextID  int64
	paused  bool
	running bool
	cancel  context.CancelFunc
}

var downloads downloadQueue

// ── Locations ────────────────────────────────────────────────────────────────

// downloadsRootOverride redirects the download folder for tests, so a queue
// test never touches the real card.
var downloadsRootOverride string

// downloadsRoot is the app-owned folder for saved media. It lives next to the
// config instead of in /tmp: on the handheld that is the SD card, and the app
// owns the folder so removing a row can never reach the user's own music.
func downloadsRoot() string {
	if dir := strings.TrimSpace(downloadsRootOverride); dir != "" {
		return dir
	}
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "downloads")
	}
	return "downloads"
}

// downloadsMusicDir is the one folder every saved track lands in, so the audio
// player can play the whole queue as a playlist.
func downloadsMusicDir() string { return filepath.Join(downloadsRoot(), "music") }

// downloadsVideoDir is the matching folder for saved videos. Keeping the two
// apart matters: the audio player treats "music" as a playlist, and a video
// beside the tracks would show up there as an unplayable row.
func downloadsVideoDir() string { return filepath.Join(downloadsRoot(), "video") }

// downloadsDirForKind picks the folder a row lands in.
func downloadsDirForKind(k downloadKind) string {
	if k == downloadKindVideo {
		return downloadsVideoDir()
	}
	return downloadsMusicDir()
}

func downloadsPartDir(id int64) string {
	return filepath.Join(downloadsRoot(), ".partial", strconv.FormatInt(id, 10))
}

// ── Queue ────────────────────────────────────────────────────────────────────

// validateDownloadURL accepts only a plain http(s) link. The URL is pasted into
// a yt-dlp argument, so whitespace, control bytes and other schemes are refused
// rather than escaped.
func validateDownloadURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("paste a link first")
	}
	if len(raw) > 2000 {
		return errors.New("that link is too long")
	}
	if strings.IndexFunc(raw, func(r rune) bool { return r < 0x21 || r == 0x7f }) >= 0 {
		return errors.New("that link has spaces or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("that is not a valid link")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("only http(s) links can be downloaded")
	}
	if u.Host == "" {
		return errors.New("that link has no address")
	}
	return nil
}

// downloadEnqueue adds an audio link to the queue and starts the worker. It is
// the path the Downloads panel's "Add a link" row uses.
func downloadEnqueue(raw string) (downloadItem, error) {
	return downloadEnqueueKind(raw, downloadKindAudio)
}

// downloadEnqueueKind adds a link to the queue with the row's media kind and
// starts the worker.
func downloadEnqueueKind(raw string, kind downloadKind) (downloadItem, error) {
	clean := strings.TrimSpace(raw)
	if err := validateDownloadURL(clean); err != nil {
		return downloadItem{}, err
	}
	downloads.mu.Lock()
	downloads.nextID++
	it := &downloadItem{ID: downloads.nextID, URL: clean, Kind: kind, Status: downloadQueued, added: time.Now()}
	downloads.items = append(downloads.items, it)
	downloads.trimLocked()
	snapshot := *it
	downloads.mu.Unlock()
	downloadsKick()
	return snapshot, nil
}

// downloadsSnapshot copies the queue for the render thread, newest section
// first: active, queued, failed, then saved with the most recent on top.
func downloadsSnapshot() []downloadItem {
	downloads.mu.Lock()
	items := make([]downloadItem, 0, len(downloads.items))
	for _, it := range downloads.items {
		items = append(items, *it)
	}
	downloads.mu.Unlock()
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := downloadSortRank(items[i].Status), downloadSortRank(items[j].Status)
		if ri != rj {
			return ri < rj
		}
		if items[i].Status == downloadDone && !items[i].finished.Equal(items[j].finished) {
			return items[i].finished.After(items[j].finished)
		}
		return items[i].ID < items[j].ID
	})
	return items
}

// downloadSortRank orders the sections the way the panel lists them.
func downloadSortRank(s downloadStatus) int {
	switch s {
	case downloadActive:
		return 0
	case downloadQueued:
		return 1
	case downloadFailed:
		return 2
	}
	return 3
}

// downloadCounts is the header summary: how many rows are in each section.
func downloadCounts(items []downloadItem) (active, queued, failed, done int) {
	for _, it := range items {
		switch it.Status {
		case downloadActive:
			active++
		case downloadQueued:
			queued++
		case downloadFailed:
			failed++
		case downloadDone:
			done++
		}
	}
	return
}

// downloadsPaused reports whether the user paused the whole queue.
func downloadsPaused() bool {
	downloads.mu.Lock()
	defer downloads.mu.Unlock()
	return downloads.paused
}

// downloadSetPaused pauses or resumes the queue. Pausing cancels the running
// yt-dlp; its row goes back to queued and keeps its partial file.
func downloadSetPaused(paused bool) {
	downloads.mu.Lock()
	downloads.paused = paused
	cancel := downloads.cancel
	downloads.mu.Unlock()
	if paused && cancel != nil {
		cancel()
	}
	if !paused {
		downloadsKick()
	}
}

// downloadRetry puts a failed row back at the end of the queue.
func downloadRetry(id int64) bool {
	downloads.mu.Lock()
	it := downloads.findLocked(id)
	if it == nil || it.Status != downloadFailed {
		downloads.mu.Unlock()
		return false
	}
	it.Status = downloadQueued
	it.Err = ""
	it.Done = 0
	it.Total = 0
	it.Speed = 0
	downloads.mu.Unlock()
	downloadsKick()
	return true
}

// downloadRemove drops a row, cancels it when it is the running one, and
// deletes the saved file. Only files inside the app's own download folder are
// ever deleted: a row always ends up there, but a renamed or moved download
// must not turn X into an arbitrary file delete.
func downloadRemove(id int64) bool {
	downloads.mu.Lock()
	idx := -1
	for i, it := range downloads.items {
		if it.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		downloads.mu.Unlock()
		return false
	}
	it := downloads.items[idx]
	cancel := downloads.cancel
	active := it.Status == downloadActive
	path := it.Path
	downloads.items = append(downloads.items[:idx], downloads.items[idx+1:]...)
	downloads.mu.Unlock()

	if active && cancel != nil {
		cancel()
	}
	os.RemoveAll(downloadsPartDir(id))
	// Both saved folders live under the app's own root; nothing outside it is
	// ever deleted.
	if path != "" && isPathWithin(path, downloadsRoot()) {
		os.Remove(path)
	}
	return true
}

// downloadFocused returns one row's copy, for the UI's Play/Retry paths.
func downloadFocused(id int64) (downloadItem, bool) {
	downloads.mu.Lock()
	defer downloads.mu.Unlock()
	if it := downloads.findLocked(id); it != nil {
		return *it, true
	}
	return downloadItem{}, false
}

func (d *downloadQueue) findLocked(id int64) *downloadItem {
	for _, it := range d.items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// trimLocked keeps the list bounded: finished rows are dropped oldest-first, so
// the songs saved most recently are the ones still listed.
func (d *downloadQueue) trimLocked() {
	done := 0
	for _, it := range d.items {
		if it.Status == downloadDone {
			done++
		}
	}
	drop := done - downloadQueueLimit
	kept := d.items[:0]
	for _, it := range d.items {
		if it.Status == downloadDone && drop > 0 {
			drop--
			continue
		}
		kept = append(kept, it)
	}
	d.items = kept
}

// downloadsKick starts the worker when it is not already running.
func downloadsKick() {
	downloads.mu.Lock()
	if downloads.running || downloads.paused {
		downloads.mu.Unlock()
		return
	}
	downloads.running = true
	downloads.mu.Unlock()
	go downloadsWorker()
}

func downloadsWorker() {
	for {
		downloads.mu.Lock()
		if downloads.paused {
			downloads.running = false
			downloads.mu.Unlock()
			return
		}
		item := downloads.nextQueuedLocked()
		if item == nil {
			downloads.running = false
			downloads.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		downloads.cancel = cancel
		item.Status = downloadActive
		item.Err = ""
		item.Speed = 0
		job := downloadJob{
			ID:   item.ID,
			URL:  item.URL,
			Kind: item.Kind,
			Dir:  downloadsDirForKind(item.Kind),
		}
		downloads.mu.Unlock()

		res, err := downloadRunner(ctx, job, func(p downloadProgress) {
			downloadsApplyProgress(job.ID, p)
		})
		cancel()

		downloads.mu.Lock()
		downloads.cancel = nil
		cur := downloads.findLocked(job.ID)
		if cur == nil {
			// Removed while it ran: the runner cleaned its own staging folder.
			downloads.mu.Unlock()
			continue
		}
		justSaved := false
		switch {
		case err != nil && (errors.Is(err, context.Canceled) || downloads.paused):
			// A pause is not a failure: the row waits for Resume.
			cur.Status = downloadQueued
			cur.Speed = 0
		case err != nil:
			cur.Status = downloadFailed
			cur.Speed = 0
			cur.Err = downloadFailureLine(err)
		default:
			cur.Status = downloadDone
			cur.Path = res.Path
			cur.Speed = 0
			cur.finished = time.Now()
			if cur.Total > 0 {
				cur.Done = cur.Total
			}
			if res.Title != "" {
				cur.Title = res.Title
			}
			if res.Source != "" {
				cur.Source = res.Source
			}
			justSaved = true
		}
		title := cur.displayTitle()
		downloads.mu.Unlock()

		if justSaved {
			ShowToast("Saved "+title, ToastKindSuccess)
		}
	}
}

// nextQueuedLocked returns the first waiting row, or nil when the queue is dry.
func (d *downloadQueue) nextQueuedLocked() *downloadItem {
	for _, it := range d.items {
		if it.Status == downloadQueued {
			return it
		}
	}
	return nil
}

func downloadsApplyProgress(id int64, p downloadProgress) {
	downloads.mu.Lock()
	defer downloads.mu.Unlock()
	it := downloads.findLocked(id)
	if it == nil || it.Status != downloadActive {
		return
	}
	if title := strings.TrimSpace(p.Title); title != "" {
		it.Title = title
	}
	if source := strings.TrimSpace(p.Source); source != "" && source != "NA" {
		it.Source = source
	}
	if p.Total > 0 {
		it.Total = p.Total
	}
	if p.Done > 0 {
		it.Done = p.Done
	} else if p.Percent > 0 && it.Total > 0 {
		it.Done = int64(p.Percent * float64(it.Total))
	}
	if p.Speed > 0 {
		it.Speed = p.Speed
	}
}

// ── yt-dlp ───────────────────────────────────────────────────────────────────

// downloadUserAgent is the identity yt-dlp asks for. It matches the streaming
// path so a link that streams also downloads.
const downloadUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// runQueuedDownload picks the command line for the row's kind. Both kinds share
// the same staging, progress parsing and adoption; only yt-dlp's flags differ.
func runQueuedDownload(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
	if job.Kind == downloadKindVideo {
		return runVideoDownload(ctx, job, onProgress)
	}
	return runAudioDownload(ctx, job, onProgress)
}

// runAudioDownload saves one track as m4a.
func runAudioDownload(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
	return runYTDLPDownload(ctx, job, onProgress, downloadArgs(downloadsPartDir(job.ID), job, appConfig))
}

// runVideoDownload saves one video as an MP4 the embedded player can decode.
func runVideoDownload(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
	return runYTDLPDownload(ctx, job, onProgress, videoDownloadArgs(downloadsPartDir(job.ID), job, appConfig))
}

// runYTDLPDownload is the transfer both kinds share. The download goes into a
// private staging folder first: yt-dlp names its output after the video title
// and picks the extension itself, so the finished file is moved into place once
// its name and size are known.
func runYTDLPDownload(ctx context.Context, job downloadJob, onProgress func(downloadProgress), args []string) (downloadResult, error) {
	ytDlp, err := helperToolPath("yt-dlp", appConfig)
	if err != nil || strings.TrimSpace(ytDlp) == "" {
		return downloadResult{}, errors.New("yt-dlp is not installed yet — open Media tools and retry")
	}
	partDir := downloadsPartDir(job.ID)
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		return downloadResult{}, fmt.Errorf("cannot write to the SD card: %w", err)
	}
	defer os.RemoveAll(partDir)
	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		return downloadResult{}, fmt.Errorf("cannot create the download folder: %w", err)
	}

	cmd := exec.CommandContext(ctx, ytDlp, args...)
	cmd.Env = helperToolEnv(ytDlp)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return downloadResult{}, err
	}
	var stderr textTail
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return downloadResult{}, err
	}

	var res downloadResult
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				if p, ok := parseDownloadLine(line); ok {
					onProgress(p)
				}
				continue
			}
			text := strings.TrimSpace(line)
			if text == "" || text == "NA" {
				continue
			}
			// Two --print calls: the title arrives first, the uploader second.
			if res.Title == "" {
				res.Title = text
				onProgress(downloadProgress{Title: text})
				continue
			}
			if res.Source == "" {
				res.Source = text
				onProgress(downloadProgress{Source: text})
			}
		}
	}()
	<-scanned
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if waitErr != nil {
		if line := stderr.lastLine(); line != "" {
			return res, errors.New(line)
		}
		return res, waitErr
	}

	path, err := adoptDownloadedFile(partDir, job.Dir, res.Title)
	if err != nil {
		return res, err
	}
	res.Path = path
	return res, nil
}

// downloadArgs is the yt-dlp command line for one saved track. It is a pure
// function so the flags can be asserted without a binary.
//
// Two flags are load-bearing: --print implies --quiet and --simulate, so
// --no-simulate is what actually downloads the file, and --progress is what
// keeps the progress bar coming in quiet mode. Without both, yt-dlp prints the
// title, exits, and the row fails with "no file".
func downloadArgs(partDir string, job downloadJob, config *Config) []string {
	args := []string{
		"-o", filepath.Join(partDir, "%(title).80s.%(ext)s"),
		"--no-playlist",
		"--no-check-certificate",
		"--geo-bypass",
		// One line per progress tick instead of carriage-return updates, so the
		// parser sees complete lines.
		"--newline",
		"--progress",
		"--no-simulate",
		"--print", "before_dl:%(title)s",
		"--print", "before_dl:%(uploader)s",
		"--user-agent", downloadUserAgent,
		"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
		"-f", "bestaudio/best",
		"--extract-audio", "--audio-format", "m4a", "--audio-quality", "0",
	}
	args = append(args, ytDlpExtraArgsSlice(config)...)
	return append(args, job.URL)
}

// videoDownloadArgs is the yt-dlp command line for one saved video. It asks
// for the best video plus its best audio and merges them into an MP4 with the
// bundled ffmpeg (the player already requires it); a source that only offers a
// pre-muxed file falls back to that instead.
func videoDownloadArgs(partDir string, job downloadJob, config *Config) []string {
	args := []string{
		"-o", filepath.Join(partDir, "%(title).80s.%(ext)s"),
		"--no-playlist",
		"--no-check-certificate",
		"--geo-bypass",
		"--newline",
		"--progress",
		"--no-simulate",
		"--print", "before_dl:%(title)s",
		"--print", "before_dl:%(uploader)s",
		"--user-agent", downloadUserAgent,
		"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
		// Video and audio arrive as separate streams on YouTube, so the two
		// halves are merged rather than downloaded as video-only.
		"-f", "bv*+ba/b",
		"--merge-output-format", "mp4",
	}
	args = append(args, ytDlpExtraArgsSlice(config)...)
	return append(args, job.URL)
}

// parseDownloadLine reads one yt-dlp progress line:
//
//	[download]  12.3% of   4.10MiB at    1.20MiB/s ETA 00:03
//
// It reports false for lines that carry no progress so the caller can ignore
// extractor chatter.
func parseDownloadLine(line string) (downloadProgress, bool) {
	if !strings.Contains(line, "[download]") {
		return downloadProgress{}, false
	}
	fields := strings.Fields(line)
	p := downloadProgress{}
	found := false
	for i, f := range fields {
		switch {
		case strings.HasSuffix(f, "%"):
			if v, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64); err == nil {
				p.Percent = v / 100
				found = true
			}
		case f == "of" && i+1 < len(fields):
			if n, ok := parseDownloadSize(fields[i+1]); ok {
				p.Total = n
				found = true
			}
		case f == "at" && i+1 < len(fields):
			if n, ok := parseDownloadSize(fields[i+1]); ok {
				p.Speed = float64(n)
				found = true
			}
		}
	}
	if p.Percent > 0 && p.Total > 0 {
		p.Done = int64(p.Percent * float64(p.Total))
	}
	return p, found
}

// parseDownloadSize turns "4.10MiB", "850.5KiB/s" or "1.2GB" into bytes.
func parseDownloadSize(tok string) (int64, bool) {
	tok = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(tok), "/s"))
	if tok == "" {
		return 0, false
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if !strings.HasSuffix(tok, u.suffix) {
			continue
		}
		num := strings.TrimSpace(strings.TrimSuffix(tok, u.suffix))
		f, err := strconv.ParseFloat(num, 64)
		if err != nil || f < 0 {
			return 0, false
		}
		return int64(f * u.mult), true
	}
	return 0, false
}

// adoptDownloadedFile moves the biggest finished file out of the staging folder
// and names it after the track. yt-dlp may leave a .part file behind when it
// was cancelled, so only complete files are considered.
func adoptDownloadedFile(partDir, dir, title string) (string, error) {
	entries, err := os.ReadDir(partDir)
	if err != nil {
		return "", err
	}
	var best string
	var bestSize int64 = -1
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(strings.ToLower(e.Name()), ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() <= 0 {
			continue
		}
		if info.Size() > bestSize {
			best, bestSize = e.Name(), info.Size()
		}
	}
	if best == "" {
		return "", errors.New("the download finished without a file")
	}
	ext := filepath.Ext(best)
	name := safeDownloadName(title)
	if name == "" {
		name = safeDownloadName(strings.TrimSuffix(best, ext))
	}
	if name == "" {
		name = "track"
	}
	dest := uniqueDownloadPath(dir, name, ext)
	src := filepath.Join(partDir, best)
	if err := os.Rename(src, dest); err != nil {
		// The staging folder can sit on a different mount from the music
		// folder, where a rename is not allowed; copy instead.
		if err := copyFile(src, dest); err != nil {
			return "", err
		}
		os.Remove(src)
	}
	return dest, nil
}

// safeDownloadName reduces a title to a file name. It never returns a path: a
// title like "../../etc/passwd" becomes "etc_passwd", and the caller joins it
// to the music folder.
func safeDownloadName(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.TrimSpace(b.String())
	name = strings.Trim(name, ". ")
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return ""
	}
	runes := []rune(name)
	if len(runes) > 100 {
		name = strings.TrimSpace(string(runes[:100]))
	}
	return name
}

// uniqueDownloadPath keeps a second copy of the same track from overwriting the
// first: "Song.m4a", "Song (2).m4a", …
func uniqueDownloadPath(dir, name, ext string) string {
	base := filepath.Join(dir, name+ext)
	if _, err := os.Stat(base); err != nil {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, i, ext))
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", name, time.Now().Unix(), ext))
}

// downloadFailureLine turns a yt-dlp error into one short line for the row.
func downloadFailureLine(err error) string {
	if err == nil {
		return ""
	}
	line := ""
	for _, candidate := range strings.Split(err.Error(), "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.HasPrefix(candidate, "WARNING") {
			continue
		}
		line = candidate
		break
	}
	line = strings.TrimPrefix(line, "ERROR: ")
	if line == "" {
		line = "Download failed"
	}
	if runes := []rune(line); len(runes) > 110 {
		line = string(runes[:110]) + "…"
	}
	return line
}

// textTail keeps the last few kilobytes of a child's stderr. yt-dlp can be
// chatty, and a failed row only needs the final line.
type textTail struct {
	buf strings.Builder
}

func (t *textTail) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if s := t.buf.String(); len(s) > 8*1024 {
		t.buf.Reset()
		t.buf.WriteString(s[len(s)-4*1024:])
	}
	return len(p), nil
}

func (t *textTail) lastLine() string {
	lines := strings.Split(t.buf.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return strings.TrimPrefix(line, "ERROR: ")
		}
	}
	return ""
}

// ── Storage ──────────────────────────────────────────────────────────────────

// downloadStorage is the card as the panel shows it: total capacity, what is
// free, and how much of the used space is JukaHub's own saved music.
type downloadStorage struct {
	Total     int64
	Free      int64
	Used      int64
	Music     int64
	Other     int64
	Available bool
}

var downloadStorageCache struct {
	mu    sync.Mutex
	at    time.Time
	stats downloadStorage
}

// downloadStorageRefresh is how long a reading is reused. Walking the download
// folder is cheap, but the panel redraws every frame.
const downloadStorageRefresh = 10 * time.Second

// downloadSpace reads the card. A filesystem without statfs (the development
// builds on Windows) reports Available=false and the panel says so instead of
// drawing a bar full of guesses.
func downloadSpace() downloadStorage {
	downloadStorageCache.mu.Lock()
	if !downloadStorageCache.at.IsZero() && time.Since(downloadStorageCache.at) < downloadStorageRefresh {
		stats := downloadStorageCache.stats
		downloadStorageCache.mu.Unlock()
		return stats
	}
	downloadStorageCache.mu.Unlock()

	stats := downloadStorage{}
	root, err := P().DataDir()
	if err == nil && strings.TrimSpace(root) != "" {
		if total, free, err := diskUsage(root); err == nil && total > 0 {
			stats.Total = total
			stats.Free = free
			stats.Used = total - free
			if stats.Used < 0 {
				stats.Used = 0
			}
			stats.Available = true
		}
	}
	stats.Music = dirBytes(downloadsRoot())
	stats.Other = stats.Used - stats.Music
	if stats.Other < 0 {
		stats.Other = 0
	}

	downloadStorageCache.mu.Lock()
	downloadStorageCache.at = time.Now()
	downloadStorageCache.stats = stats
	downloadStorageCache.mu.Unlock()
	return stats
}

// downloadSpaceShares splits the card into music / other / free fractions of
// the total, in that order. The three always add up to 1 so the bar ends flush
// with the card.
func downloadSpaceShares(s downloadStorage) [3]float64 {
	if s.Total <= 0 {
		return [3]float64{}
	}
	music := float64(s.Music) / float64(s.Total)
	other := float64(s.Other) / float64(s.Total)
	free := float64(s.Free) / float64(s.Total)
	for i, v := range []float64{music, other, free} {
		if v < 0 || v != v {
			if i == 0 {
				music = 0
			} else if i == 1 {
				other = 0
			} else {
				free = 0
			}
		}
	}
	sum := music + other + free
	if sum <= 0 {
		return [3]float64{}
	}
	if sum > 1 {
		music, other, free = music/sum, other/sum, free/sum
	}
	return [3]float64{music, other, free}
}

// downloadSpaceSummary is the card's one-line reading.
func downloadSpaceSummary(s downloadStorage) string {
	if !s.Available {
		return "Storage reading unavailable"
	}
	return humanBytes(s.Used) + " used · " + humanBytes(s.Free) + " free of " + humanBytes(s.Total)
}

// dirBytes sums the regular files under root. Missing folders are zero bytes,
// not an error, and symlinks are never followed out of the tree.
func dirBytes(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
