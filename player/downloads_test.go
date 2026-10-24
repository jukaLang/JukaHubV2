package main

// downloads_test.go — the offline download queue, its progress parser and the
// storage reading behind the panel's card. Everything here runs without a
// network, a yt-dlp binary or a real SD card: the runner is swapped for a fake
// so the queue's ordering, pause/resume and cleanup rules can be pinned.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// downloadsTestReset gives one test the queue to itself and points the download
// folder at a temporary directory.
func downloadsTestReset(t *testing.T) {
	t.Helper()
	downloads.mu.Lock()
	downloads.items = nil
	downloads.nextID = 0
	downloads.paused = false
	downloads.running = false
	downloads.cancel = nil
	downloads.mu.Unlock()

	prevRoot, prevRunner := downloadsRootOverride, downloadRunner
	downloadsRootOverride = t.TempDir()
	downloadRunner = func(context.Context, downloadJob, func(downloadProgress)) (downloadResult, error) {
		return downloadResult{}, errors.New("this test did not install a runner")
	}
	t.Cleanup(func() {
		// Stop whatever the test left running before the globals are restored.
		downloadSetPaused(true)
		waitForDownload(t, "the worker to stop", func() bool {
			downloads.mu.Lock()
			defer downloads.mu.Unlock()
			return !downloads.running
		})
		downloads.mu.Lock()
		downloads.items = nil
		downloads.paused = false
		downloads.cancel = nil
		downloads.mu.Unlock()
		downloadsRootOverride = prevRoot
		downloadRunner = prevRunner
	})
}

// mib converts a fractional MiB count at run time (a constant conversion would
// have to be exact).
func mib(n float64) int64 { return int64(n * 1024 * 1024) }

func waitForDownload(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func downloadCountsNow() (active, queued, failed, done int) {
	return downloadCounts(downloadsSnapshot())
}

func TestValidateDownloadURL(t *testing.T) {
	for _, ok := range []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"http://example.com/track",
		"  https://youtu.be/abc  ",
	} {
		if err := validateDownloadURL(ok); err != nil {
			t.Errorf("validateDownloadURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"   ",
		"not a link",
		"ftp://example.com/x",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"https://exa mple.com/watch",
		"https://example.com/" + strings.Repeat("a", 2100),
	} {
		if err := validateDownloadURL(bad); err == nil {
			t.Errorf("validateDownloadURL(%q) accepted a bad link", bad)
		}
	}
}

func TestDownloadArgsKeepTheTransferReal(t *testing.T) {
	partDir := filepath.Join("stage", "7")
	args := downloadArgs(partDir, downloadJob{ID: 7, URL: "https://youtu.be/abc"}, nil)
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	// --print switches yt-dlp into simulation and quiet mode; both helpers are
	// what keep the download and its progress alive.
	for _, want := range []string{"--no-simulate", "--progress", "--newline", "--extract-audio", "--audio-format"} {
		if !has(want) {
			t.Errorf("download args are missing %s (%v)", want, args)
		}
	}
	if !has("m4a") {
		t.Error("audio is not converted to m4a")
	}
	if args[len(args)-1] != "https://youtu.be/abc" {
		t.Errorf("the URL is not the last argument: %q", args[len(args)-1])
	}
	// Output must land in the private staging folder, never straight into the
	// music library, or a cancelled row would leave a half file there.
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" {
			found = strings.HasPrefix(args[i+1], partDir)
		}
	}
	if !found {
		t.Errorf("the output template does not start in %q: %v", partDir, args)
	}
}

func TestVideoDownloadArgsKeepPictureAndSound(t *testing.T) {
	partDir := filepath.Join("stage", "9")
	args := videoDownloadArgs(partDir, downloadJob{ID: 9, URL: "https://youtu.be/abc", Kind: downloadKindVideo}, nil)
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"--no-simulate", "--progress", "--newline", "--merge-output-format"} {
		if !has(want) {
			t.Errorf("video args are missing %s (%v)", want, args)
		}
	}
	// The merge has to happen: YouTube serves the picture and the sound as
	// separate streams, so a video-only grab would play silently.
	format := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" {
			format = args[i+1]
		}
	}
	if !strings.Contains(format, "+") {
		t.Errorf("the format selector %q does not ask for video and audio", format)
	}
	if has("--extract-audio") || has("--audio-format") {
		t.Error("the video download stripped the audio track")
	}
	if args[len(args)-1] != "https://youtu.be/abc" {
		t.Errorf("the URL is not the last argument: %q", args[len(args)-1])
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && !strings.HasPrefix(args[i+1], partDir) {
			t.Errorf("the video output template is not staged in %q: %q", partDir, args[i+1])
		}
	}
}

// A video row must run the video command line and land in its own folder, so
// the audio player's playlist never picks up an unplayable MP4.
func TestVideoDownloadLandsInTheVideoFolder(t *testing.T) {
	downloadsTestReset(t)
	var jobMu sync.Mutex
	var got downloadJob
	downloadRunner = func(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
		jobMu.Lock()
		got = job
		jobMu.Unlock()
		if err := os.MkdirAll(job.Dir, 0o755); err != nil {
			return downloadResult{}, err
		}
		path := filepath.Join(job.Dir, "clip.mp4")
		if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
			return downloadResult{}, err
		}
		return downloadResult{Path: path, Title: "Clip"}, nil
	}

	item, err := downloadEnqueueKind("https://example.com/watch?v=vid", downloadKindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != downloadKindVideo {
		t.Fatalf("queued row kind = %v, want video", item.Kind)
	}
	waitForDownload(t, "the video to finish", func() bool {
		_, _, _, done := downloadCountsNow()
		return done == 1
	})

	jobMu.Lock()
	jobCopy := got
	jobMu.Unlock()
	if jobCopy.Kind != downloadKindVideo {
		t.Errorf("runner job kind = %v, want video", jobCopy.Kind)
	}
	if jobCopy.Dir != downloadsVideoDir() {
		t.Errorf("runner dir = %q, want the video folder %q", jobCopy.Dir, downloadsVideoDir())
	}
	saved := downloadsSnapshot()[0]
	if saved.Kind != downloadKindVideo {
		t.Errorf("saved row kind = %v, want video", saved.Kind)
	}
	if !strings.HasPrefix(saved.Path, downloadsVideoDir()) {
		t.Errorf("saved video path = %q, want it under %q", saved.Path, downloadsVideoDir())
	}
}

func TestParseDownloadLine(t *testing.T) {
	p, ok := parseDownloadLine("[download]  12.3% of   4.10MiB at    1.20MiB/s ETA 00:03")
	if !ok {
		t.Fatal("a progress line was not recognised")
	}
	if got := p.Percent; got < 0.122 || got > 0.124 {
		t.Errorf("percent = %v, want ~0.123", got)
	}
	wantTotal := mib(4.10)
	if p.Total != wantTotal {
		t.Errorf("total = %d, want %d", p.Total, wantTotal)
	}
	if p.Done <= 0 || p.Done > p.Total {
		t.Errorf("done = %d, want 0 < done <= %d", p.Done, p.Total)
	}
	if want := 1.2 * 1024 * 1024; p.Speed < want*0.99 || p.Speed > want*1.01 {
		t.Errorf("speed = %v, want ~%v", p.Speed, want)
	}

	// yt-dlp's finished line puts the time where ETA used to be.
	p, ok = parseDownloadLine("[download] 100% of 3.00MiB in 00:02 at 1.50MiB/s")
	if !ok || p.Total != mib(3) || p.Done != p.Total {
		t.Fatalf("finished line = %+v (ok=%v)", p, ok)
	}

	// An unknown size still reports the percentage; the row stays indeterminate.
	if _, ok := parseDownloadLine("[download]  45.0% of Unknown at Unknown"); !ok {
		t.Error("a line without a size was ignored")
	}
	// Extractor chatter is not progress.
	for _, line := range []string{
		"[youtube] dQw4w9WgXcQ: Downloading webpage",
		"[download] Destination: /tmp/track.m4a",
		"",
	} {
		if _, ok := parseDownloadLine(line); ok {
			t.Errorf("%q was parsed as progress", line)
		}
	}
}

func TestParseDownloadSize(t *testing.T) {
	cases := []struct {
		token string
		want  int64
		ok    bool
	}{
		{"4.10MiB", mib(4.10), true},
		{"850.5KiB/s", int64(850.5 * 1024), true},
		{"1.2GB", int64(1.2e9), true},
		{"512B", 512, true},
		{"Unknown", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDownloadSize(c.token)
		if ok != c.ok {
			t.Errorf("parseDownloadSize(%q) ok = %v, want %v", c.token, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseDownloadSize(%q) = %d, want %d", c.token, got, c.want)
		}
	}
}

func TestSafeDownloadName(t *testing.T) {
	cases := map[string]string{
		"Night Drive Theory":    "Night Drive Theory",
		"../../etc/passwd":      "_.._etc_passwd",
		"AC/DC: Back in Black?": "AC_DC_ Back in Black_",
		"  spaced   out  ":      "spaced out",
		"..":                    "",
		"":                      "",
		"naïve — artist ✦":      "naïve — artist ✦",
	}
	for in, want := range cases {
		if got := safeDownloadName(in); got != want {
			t.Errorf("safeDownloadName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 200)
	if got := safeDownloadName(long); len([]rune(got)) != 100 {
		t.Errorf("long title kept %d runes, want 100", len([]rune(got)))
	}
	// Whatever a title contains, the result is still one plain file name.
	if name := safeDownloadName("../../x"); strings.ContainsAny(name, `/\`) {
		t.Errorf("safeDownloadName returned a path: %q", name)
	}
}

func TestUniqueDownloadPathDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	first := uniqueDownloadPath(dir, "Song", ".m4a")
	if filepath.Base(first) != "Song.m4a" {
		t.Fatalf("first path = %q", first)
	}
	if err := os.WriteFile(first, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := uniqueDownloadPath(dir, "Song", ".m4a")
	if filepath.Base(second) != "Song (2).m4a" {
		t.Fatalf("second path = %q, want Song (2).m4a", second)
	}
}

func TestAdoptDownloadedFileIgnoresPartials(t *testing.T) {
	part := t.TempDir()
	dest := t.TempDir()
	os.WriteFile(filepath.Join(part, "Song.m4a.part"), []byte("partial"), 0o644)
	os.WriteFile(filepath.Join(part, "Song.m4a"), []byte("finished-audio"), 0o644)

	path, err := adoptDownloadedFile(part, dest, "Real Title")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "Real Title.m4a" {
		t.Fatalf("saved as %q, want Real Title.m4a", filepath.Base(path))
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "finished-audio" {
		t.Fatalf("saved content = %q (%v)", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Song.m4a")); err == nil {
		t.Fatal("the partial download was adopted")
	}

	empty := t.TempDir()
	if _, err := adoptDownloadedFile(empty, dest, "Nothing"); err == nil {
		t.Fatal("an empty staging folder produced a file")
	}
}

func TestDownloadQueueRunsOneAtATime(t *testing.T) {
	downloadsTestReset(t)
	release := make(chan struct{})
	var mu sync.Mutex
	peak, live, started := 0, 0, 0
	downloadRunner = func(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
		mu.Lock()
		started++
		live++
		if live > peak {
			peak = live
		}
		mu.Unlock()
		defer func() { mu.Lock(); live--; mu.Unlock() }()

		onProgress(downloadProgress{Title: "Track", Total: 1000, Done: 250, Speed: 1 << 20})
		select {
		case <-release:
		case <-ctx.Done():
			return downloadResult{}, ctx.Err()
		}
		path := filepath.Join(job.Dir, "track.m4a")
		if err := os.MkdirAll(job.Dir, 0o755); err != nil {
			return downloadResult{}, err
		}
		if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
			return downloadResult{}, err
		}
		return downloadResult{Path: path, Title: "Saved Track", Source: "Artist"}, nil
	}

	for i := 0; i < 3; i++ {
		if _, err := downloadEnqueue("https://example.com/watch?v=" + string(rune('a'+i))); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	waitForDownload(t, "the first track to start", func() bool {
		active, _, _, _ := downloadCountsNow()
		return active == 1
	})
	active, queued, _, _ := downloadCountsNow()
	if active != 1 || queued != 2 {
		t.Fatalf("queue = %d active, %d queued; want 1 and 2", active, queued)
	}
	// Progress reaches the row while it runs.
	items := downloadsSnapshot()
	if items[0].percent() != 0.25 || items[0].Speed == 0 || items[0].Title != "Track" {
		t.Fatalf("running row = %+v", items[0])
	}

	close(release)
	waitForDownload(t, "the queue to drain", func() bool {
		_, queued, failed, done := downloadCountsNow()
		return queued == 0 && failed == 0 && done == 3
	})
	mu.Lock()
	peakSeen, startedCount := peak, started
	mu.Unlock()
	if peakSeen != 1 {
		t.Fatalf("%d downloads ran at once, want 1", peakSeen)
	}
	if startedCount != 3 {
		t.Fatalf("%d downloads started, want 3", startedCount)
	}
	for _, it := range downloadsSnapshot() {
		if it.Status != downloadDone || it.Path == "" || it.Title != "Saved Track" {
			t.Fatalf("finished row = %+v", it)
		}
	}
}

func TestDownloadPauseKeepsTheRowQueued(t *testing.T) {
	downloadsTestReset(t)
	started := make(chan struct{}, 1)
	downloadRunner = func(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return downloadResult{}, ctx.Err()
	}
	if _, err := downloadEnqueue("https://example.com/watch?v=pause"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the download never started")
	}
	waitForDownload(t, "the row to become active", func() bool {
		active, _, _, _ := downloadCountsNow()
		return active == 1
	})

	downloadSetPaused(true)
	waitForDownload(t, "the paused row to go back to the queue", func() bool {
		active, queued, failed, _ := downloadCountsNow()
		return active == 0 && queued == 1 && failed == 0
	})
	if !downloadsPaused() {
		t.Fatal("the queue did not report itself as paused")
	}

	// Resume starts it again instead of failing it.
	downloadSetPaused(false)
	waitForDownload(t, "the track to run again", func() bool {
		active, _, _, _ := downloadCountsNow()
		return active == 1
	})
}

func TestDownloadFailureAndRetry(t *testing.T) {
	downloadsTestReset(t)
	var failMu sync.Mutex
	shouldFail := true
	downloadRunner = func(ctx context.Context, job downloadJob, onProgress func(downloadProgress)) (downloadResult, error) {
		failMu.Lock()
		fail := shouldFail
		failMu.Unlock()
		if fail {
			return downloadResult{}, errors.New("ERROR: Video unavailable")
		}
		path := filepath.Join(job.Dir, "ok.m4a")
		os.MkdirAll(job.Dir, 0o755)
		os.WriteFile(path, []byte("audio"), 0o644)
		return downloadResult{Path: path, Title: "Recovered"}, nil
	}
	if _, err := downloadEnqueue("https://example.com/watch?v=retry"); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, "the failure", func() bool {
		_, _, failed, _ := downloadCountsNow()
		return failed == 1
	})
	items := downloadsSnapshot()
	if !strings.Contains(items[0].Err, "Video unavailable") {
		t.Fatalf("failure line = %q, want the yt-dlp error", items[0].Err)
	}

	failMu.Lock()
	shouldFail = false
	failMu.Unlock()
	if !downloadRetry(items[0].ID) {
		t.Fatal("retry refused a failed row")
	}
	waitForDownload(t, "the retry to finish", func() bool {
		_, _, failed, done := downloadCountsNow()
		return failed == 0 && done == 1
	})
}

func TestDownloadRemoveDeletesOnlySavedFiles(t *testing.T) {
	downloadsTestReset(t)
	music := downloadsMusicDir()
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(music, "kept.m4a")
	if err := os.WriteFile(saved, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "user-music.m4a")
	if err := os.WriteFile(outside, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	videos := downloadsVideoDir()
	if err := os.MkdirAll(videos, 0o755); err != nil {
		t.Fatal(err)
	}
	savedVideo := filepath.Join(videos, "clip.mp4")
	if err := os.WriteFile(savedVideo, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	downloads.mu.Lock()
	downloads.nextID = 3
	downloads.items = []*downloadItem{
		{ID: 1, URL: "https://example.com/a", Status: downloadDone, Path: saved, Title: "Kept"},
		{ID: 2, URL: "https://example.com/b", Status: downloadDone, Path: outside, Title: "Outside"},
		{ID: 3, URL: "https://example.com/c", Status: downloadDone, Path: savedVideo, Title: "Clip", Kind: downloadKindVideo},
	}
	downloads.mu.Unlock()

	if !downloadRemove(1) {
		t.Fatal("remove refused a saved row")
	}
	if _, err := os.Stat(saved); err == nil {
		t.Fatal("the saved file was not deleted")
	}
	if !downloadRemove(3) {
		t.Fatal("remove refused a saved video")
	}
	if _, err := os.Stat(savedVideo); err == nil {
		t.Fatal("the saved video was not deleted")
	}
	downloadRemove(2)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a file outside the download folder was deleted: %v", err)
	}
	if len(downloadsSnapshot()) != 0 {
		t.Fatal("rows survived removal")
	}
}

func TestDownloadSpaceShares(t *testing.T) {
	stats := downloadStorage{Total: 1000, Used: 400, Free: 600, Music: 100, Other: 300, Available: true}
	shares := downloadSpaceShares(stats)
	if sum := shares[0] + shares[1] + shares[2]; sum < 0.999 || sum > 1.001 {
		t.Fatalf("shares sum to %v, want 1", sum)
	}
	if shares[0] < 0.099 || shares[0] > 0.101 {
		t.Errorf("music share = %v, want 0.1", shares[0])
	}

	// A card read that failed draws no bar rather than a full one.
	if got := downloadSpaceShares(downloadStorage{}); got != [3]float64{} {
		t.Errorf("empty storage shares = %v, want zeros", got)
	}
	if got := downloadSpaceSummary(downloadStorage{}); !strings.Contains(got, "unavailable") {
		t.Errorf("unavailable summary = %q", got)
	}
	if got := downloadSpaceSummary(stats); !strings.Contains(got, "used") || !strings.Contains(got, "free") {
		t.Errorf("summary = %q", got)
	}
}

func TestDownloadQueueKeepsTheNewestSavedRows(t *testing.T) {
	downloadsTestReset(t)
	downloads.mu.Lock()
	for i := 1; i <= downloadQueueLimit+5; i++ {
		downloads.items = append(downloads.items, &downloadItem{
			ID: int64(i), URL: "https://example.com/x", Status: downloadDone, Title: "Track" + itoa(i),
		})
	}
	downloads.nextID = int64(downloadQueueLimit + 5)
	downloads.trimLocked()
	kept := len(downloads.items)
	first, last := downloads.items[0].ID, downloads.items[kept-1].ID
	downloads.mu.Unlock()

	if kept != downloadQueueLimit {
		t.Fatalf("kept %d saved rows, want %d", kept, downloadQueueLimit)
	}
	// The oldest rows go first, so the list still shows what was just saved.
	if first != 6 || last != int64(downloadQueueLimit+5) {
		t.Fatalf("kept rows %d..%d, want 6..%d", first, last, downloadQueueLimit+5)
	}
}

func TestDownloadRowsGroupByStatus(t *testing.T) {
	prevRoot := downloadsRootOverride
	downloadsRootOverride = t.TempDir()
	prevItems := downloads.items
	t.Cleanup(func() {
		downloads.mu.Lock()
		downloads.items = prevItems
		downloads.mu.Unlock()
		downloadsRootOverride = prevRoot
	})

	downloads.mu.Lock()
	downloads.items = []*downloadItem{
		{ID: 3, URL: "https://example.com/c", Status: downloadFailed, Title: "Failed one", Err: "boom"},
		{ID: 1, URL: "https://example.com/a", Status: downloadActive, Title: "Running"},
		{ID: 4, URL: "https://example.com/d", Status: downloadDone, Title: "Saved one", Path: "/tmp/x.m4a"},
		{ID: 2, URL: "https://example.com/b", Status: downloadQueued},
	}
	downloads.mu.Unlock()

	rows := downloadRowsFor(downloadTabAll)
	if len(rows) != 5 || !rows[0].add {
		t.Fatalf("all rows = %d (first add=%v), want 5 starting with Add a link", len(rows), rows[0].add)
	}
	wantSections := []string{"", "Downloading", "Queued", "Failed", "Saved"}
	for i, want := range wantSections {
		if rows[i].section != want {
			t.Errorf("row %d section = %q, want %q", i, rows[i].section, want)
		}
	}
	if rows[2].item.displayTitle() != "Resolving link…" {
		t.Errorf("queued row title = %q", rows[2].item.displayTitle())
	}

	active := downloadRowsFor(downloadTabActive)
	if downloadRowCount(active) != 2 {
		t.Errorf("active tab has %d rows, want 2", downloadRowCount(active))
	}
	saved := downloadRowsFor(downloadTabSaved)
	if downloadRowCount(saved) != 1 || saved[1].item.Title != "Saved one" {
		t.Errorf("saved tab = %+v", saved)
	}
	if got := downloadTabLabel(downloadTabSaved); got != "Saved" {
		t.Errorf("tab label = %q", got)
	}
}

func TestDownloadRowStatusText(t *testing.T) {
	running := downloadItem{Status: downloadActive, Total: 4 << 20, Done: 2 << 20, Speed: 2 << 20}
	text, _ := downloadRowStatus(running)
	if !strings.Contains(text, "50%") || !strings.Contains(text, "MiB/s") {
		t.Errorf("running status = %q", text)
	}
	if text, _ := downloadRowStatus(downloadItem{Status: downloadQueued}); text != "Queued" {
		t.Errorf("queued status = %q", text)
	}
	if text, _ := downloadRowStatus(downloadItem{Status: downloadDone}); text != "Saved" {
		t.Errorf("done status = %q", text)
	}
	if text, _ := downloadRowStatus(downloadItem{Status: downloadFailed}); text != "Failed" {
		t.Errorf("failed status = %q", text)
	}
	if detail := downloadRowDetail(downloadItem{Status: downloadFailed, Err: "Video unavailable"}); detail != "Video unavailable" {
		t.Errorf("failed detail = %q", detail)
	}
	if detail := downloadRowDetail(downloadItem{Status: downloadDone, Done: 2 << 20}); !strings.Contains(detail, "2.0 MiB") {
		t.Errorf("saved detail = %q", detail)
	}
	// The row says which kind it saved, so a video is not mistaken for a track
	// when A opens it.
	if detail := downloadRowDetail(downloadItem{Status: downloadDone, Kind: downloadKindVideo}); !strings.Contains(detail, "Video") {
		t.Errorf("saved video detail = %q, want the kind named", detail)
	}
	if detail := downloadRowDetail(downloadItem{Status: downloadDone}); !strings.Contains(detail, "Audio") {
		t.Errorf("saved audio detail = %q, want the kind named", detail)
	}
}
