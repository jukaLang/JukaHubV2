package main

// downloads_test.go — the offline download queue's file safety: which links
// it accepts, how a saved file is named, that nothing is overwritten or
// deleted outside the queue's own files, that partials are resumed and never
// adopted, and that a config-supplied yt-dlp command cannot carry an option
// that runs another program. Everything here runs without a network, a yt-dlp
// binary or a real SD card.

import (
	"context"
	"errors"
	"fmt"
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
	downloads.sidecars = nil
	downloads.sidecarID = 0
	downloads.sidecarPath = ""
	downloads.sidecarDropped = false
	downloads.mu.Unlock()

	prevRoot, prevRunner := downloadsRootOverride, downloadRunner
	downloadsRootOverride = t.TempDir()
	downloadRunner = func(context.Context, downloadJob, func(downloadProgress)) (downloadResult, error) {
		return downloadResult{}, errors.New("this test did not install a runner")
	}
	// A finished row would be filed in the Downloads library on a goroutine
	// that outlives the test and writes into its temporary folder
	// (dl_library.go); the queue's tests leave the library alone.
	prevFilingOff := dlLibraryFilingOff.Load()
	dlLibraryFilingOff.Store(true)
	t.Cleanup(func() {
		dlLibraryFilingOff.Store(prevFilingOff)
	})
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
		downloads.sidecars = nil
		downloads.sidecarID = 0
		downloads.sidecarPath = ""
		downloads.sidecarDropped = false
		downloads.mu.Unlock()
		downloadsRootOverride = prevRoot
		downloadRunner = prevRunner
	})
}

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

func TestDownloadResumeKeepsThePartialFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, "Song.webm.part")
	if err := os.WriteFile(partial, []byte("half a song"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dropped link or a pause must leave the staged bytes on the card, or
	// Resume re-downloads the whole track on the handheld's slow storage.
	downloadsCleanStaging(dir, errors.New("no data for 60 s"))
	if !regularFile(partial) {
		t.Fatal("a failed transfer deleted its partial file")
	}
	downloadsCleanStaging(dir, nil)
	if regularFile(partial) {
		t.Fatal("a finished transfer left its staging folder behind")
	}
}

func TestDownloadRejectsTinySavedFile(t *testing.T) {
	dir := t.TempDir()
	tiny := filepath.Join(dir, "error-page.m4a")
	if err := os.WriteFile(tiny, []byte("not media"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := downloadsValidateSavedFile(tiny, downloadKindAudio)
	if err == nil || !strings.Contains(err.Error(), "not playable media") {
		t.Fatalf("tiny file gave %v, want a rejection explaining the size", err)
	}
	if regularFile(tiny) {
		t.Fatal("the rejected file was left in the music folder")
	}

	good := filepath.Join(dir, "song.m4a")
	if err := os.WriteFile(good, make([]byte, downloadMinimumBytes(downloadKindAudio)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadsValidateSavedFile(good, downloadKindAudio); err != nil {
		t.Fatalf("a real file was rejected: %v", err)
	}
}

// A search command is config-supplied, so it must not be able to hand yt-dlp
// an option that runs another program.
func TestRejectYtDlpExecFlags(t *testing.T) {
	allowed := []string{
		"--flat-playlist",
		"--dump-single-json",
		"--default-search", "ytsearch",
		"--playlist-end", "20",
		"ytsearch20:funny cats",
		"--cookies", "/tmp/cookies.txt",
		"--extractor-args", "youtube:api_key=abc=",
	}
	if err := rejectYtDlpExecFlags(allowed); err != nil {
		t.Fatalf("benign search command rejected: %v", err)
	}

	for _, args := range [][]string{
		{"--exec", "rm -rf /"},
		{"--exec-before-download=sh -c id"},
		{"--downloader", "curl"},
		{"--external-downloader=wget"},
		{"--postprocessor-args", "-i"},
		{"--ppa=ffmpeg:-i"},
		{"--update"},
	} {
		if err := rejectYtDlpExecFlags(args); err == nil {
			t.Errorf("execution-capable args %v were accepted", args)
		}
	}
}

func TestDownloadEnqueueRefusesASecondCopy(t *testing.T) {
	downloadsTestReset(t)
	downloadSetPaused(true) // the rows stay queued; nothing runs
	page := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	first, err := downloadEnqueueKind(page, downloadKindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if !downloadsHas("https://youtu.be/dQw4w9WgXcQ", downloadKindVideo) {
		t.Fatal("the queued video was not found under its short link")
	}
	if _, err := downloadEnqueueKind("https://youtu.be/dQw4w9WgXcQ", downloadKindVideo); !errors.Is(err, errDownloadAlreadyQueued) {
		t.Fatalf("a second copy of a queued video = %v, want errDownloadAlreadyQueued", err)
	}
	if _, err := downloadEnqueueKind(page, downloadKindAudio); err != nil {
		t.Fatalf("the sound of a queued video is a different file: %v", err)
	}
	downloads.mu.Lock()
	downloads.findLocked(first.ID).Status = downloadFailed
	downloads.mu.Unlock()
	if downloadsHas(page, downloadKindVideo) {
		t.Fatal("a failed row still counts as downloading")
	}
	if _, err := downloadEnqueueKind(page, downloadKindVideo); err != nil {
		t.Fatalf("a new try after a failure was refused: %v", err)
	}
	// The worker does not read the custom settings map: each row carries its
	// own copy of the extra yt-dlp arguments, empty but not nil when there are
	// none.
	if it, ok := downloadFocused(first.ID); !ok || it.ytExtra == nil {
		t.Fatal("a queued row must carry its own copy of the yt-dlp arguments")
	}
}

// downloadsSidecarFixture records what the queue did: transfers (a stub that
// writes the video into the download folder) and subtitle steps (each test's
// own stub, set on downloadSubtitleSaver after the fixture is made).
type downloadsSidecarFixture struct {
	mu     sync.Mutex
	events []string
}

func (f *downloadsSidecarFixture) note(event string) {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
}

func (f *downloadsSidecarFixture) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// newDownloadsSidecarFixture gives a test the queue with saved videos getting
// the subtitle step. The step's globals are restored by a cleanup registered
// before downloadsTestReset's, so it runs after the worker has stopped
// (cleanups run last in, first out) and no step can reach the real fetch.
func newDownloadsSidecarFixture(t *testing.T) *downloadsSidecarFixture {
	t.Helper()
	clearToastAfterTest(t) // the worker toasts "Saved …"
	prevSaver, prevOn := downloadSubtitleSaver, downloadSidecarsOn
	t.Cleanup(func() { downloadSubtitleSaver, downloadSidecarsOn = prevSaver, prevOn })
	downloadsTestReset(t)
	f := &downloadsSidecarFixture{}
	videos := downloadsVideoDir()
	if err := os.MkdirAll(videos, 0o755); err != nil {
		t.Fatal(err)
	}
	downloadRunner = func(_ context.Context, job downloadJob, _ func(downloadProgress)) (downloadResult, error) {
		path := filepath.Join(videos, fmt.Sprintf("clip%d.mp4", job.ID))
		if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
			return downloadResult{}, err
		}
		f.note(fmt.Sprintf("transfer %d", job.ID))
		return downloadResult{Path: path, Title: fmt.Sprintf("Clip %d", job.ID)}, nil
	}
	downloadSubtitleSaver = func(_ context.Context, _ subtitleRequest, videoPath string) (string, error) {
		return videoPath + ".en.srt", nil
	}
	downloadSidecarsOn = func() bool { return true }
	return f
}

// A queued download never waits behind a subtitle fetch: the subtitles of the
// videos that landed are fetched once nothing else is queued.
func TestDownloadSubtitleStepsWaitForAnIdleQueue(t *testing.T) {
	f := newDownloadsSidecarFixture(t)
	downloadSubtitleSaver = func(_ context.Context, r subtitleRequest, videoPath string) (string, error) {
		event := "subtitles " + filepath.Base(videoPath)
		if r.extra == nil {
			event += " (arguments read on the worker)"
		}
		f.note(event)
		return videoPath + ".en.srt", nil
	}
	downloadSetPaused(true)
	for _, link := range []string{"https://example.com/a", "https://example.com/b"} {
		if _, err := downloadEnqueueKind(link, downloadKindVideo); err != nil {
			t.Fatal(err)
		}
	}
	downloadSetPaused(false)
	waitForDownload(t, "both subtitle steps", func() bool { return len(f.snapshot()) >= 4 })
	got := strings.Join(f.snapshot(), ", ")
	want := "transfer 1, transfer 2, subtitles clip1.mp4, subtitles clip2.mp4"
	if got != want {
		t.Fatalf("order = %s\nwant    %s", got, want)
	}
}

// A row queued while a subtitle step runs starts at once; the step is put
// back and runs again when the queue is idle.
func TestDownloadSubtitleStepYieldsToANewRow(t *testing.T) {
	f := newDownloadsSidecarFixture(t)
	started := make(chan struct{}, 1)
	var once sync.Once
	downloadSubtitleSaver = func(ctx context.Context, _ subtitleRequest, videoPath string) (string, error) {
		block := false
		once.Do(func() { block = true })
		if block {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			f.note("subtitles " + filepath.Base(videoPath) + " cut short")
			return "", ctx.Err()
		}
		f.note("subtitles " + filepath.Base(videoPath))
		return videoPath + ".en.srt", nil
	}
	if _, err := downloadEnqueueKind("https://example.com/a", downloadKindVideo); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the subtitle step never started")
	}
	if _, err := downloadEnqueueKind("https://example.com/b", downloadKindVideo); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, "the transfers and the subtitle steps", func() bool { return len(f.snapshot()) >= 5 })
	got := strings.Join(f.snapshot(), ", ")
	want := "transfer 1, subtitles clip1.mp4 cut short, transfer 2, subtitles clip1.mp4, subtitles clip2.mp4"
	if got != want {
		t.Fatalf("order = %s\nwant    %s", got, want)
	}
}

// Removing a video that just landed stops the fetch of its subtitles, which
// is not tried again.
func TestDownloadRemoveStopsItsSubtitleStep(t *testing.T) {
	f := newDownloadsSidecarFixture(t)
	started := make(chan struct{}, 1)
	downloadSubtitleSaver = func(ctx context.Context, _ subtitleRequest, videoPath string) (string, error) {
		f.note("subtitles " + filepath.Base(videoPath))
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	row, err := downloadEnqueueKind("https://example.com/a", downloadKindVideo)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the subtitle step never started")
	}
	if !downloadRemove(row.ID) {
		t.Fatal("remove refused the saved video")
	}
	waitForDownload(t, "the worker to stop", func() bool {
		downloads.mu.Lock()
		defer downloads.mu.Unlock()
		return !downloads.running
	})
	if got := f.snapshot(); len(got) != 2 {
		t.Fatalf("events = %v; a removed video's subtitles must not be fetched again", got)
	}
	if regularFile(filepath.Join(downloadsVideoDir(), "clip1.mp4")) {
		t.Fatal("the removed video stayed on the card")
	}
}

func TestPlaceDownloadedFileNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Song.m4a"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(stage, "audio.m4a")
	if err := os.WriteFile(src, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest, err := placeDownloadedFile(src, dir, "Song", ".m4a")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dest) != "Song (2).m4a" {
		t.Fatalf("placed as %q, want Song (2).m4a", filepath.Base(dest))
	}
	if data, err := os.ReadFile(filepath.Join(dir, "Song.m4a")); err != nil || string(data) != "first" {
		t.Fatalf("the file that was there became %q (%v)", data, err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "second" {
		t.Fatalf("the placed file holds %q (%v)", data, err)
	}
	if regularFile(src) {
		t.Fatal("the staged file was left behind")
	}
	// A file that cannot be moved or copied leaves no empty placeholder.
	if _, err := placeDownloadedFile(filepath.Join(stage, "missing.m4a"), dir, "Ghost", ".m4a"); err == nil {
		t.Fatal("a missing file was placed")
	}
	if regularFile(filepath.Join(dir, "Ghost.m4a")) {
		t.Fatal("a failed placement left a file behind")
	}
}

func TestDownloadTrimmedRowKeepsItsSubtitleStep(t *testing.T) {
	downloadsTestReset(t)
	video := filepath.Join(downloadsVideoDir(), "kept.mp4")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	downloads.mu.Lock()
	// Rows trimmed from the list (more than downloadQueueLimit saved in one
	// go): one whose file is still on the card, one whose file is gone.
	downloads.sidecars = []downloadItem{
		{ID: 41, Path: filepath.Join(downloadsVideoDir(), "gone.mp4")},
		{ID: 42, Path: video},
	}
	sub, ok := downloads.nextSidecarLocked()
	rest := len(downloads.sidecars)
	downloads.mu.Unlock()
	if !ok || sub.ID != 42 || rest != 0 {
		t.Fatalf("next step = %+v %v (%d left); want the trimmed row whose file is there", sub, ok, rest)
	}
}

func TestDownloadRetryDoesNotFetchAPageTwice(t *testing.T) {
	downloadsTestReset(t)
	downloadSetPaused(true)
	const page = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	downloads.mu.Lock()
	downloads.nextID = 2
	downloads.items = []*downloadItem{
		{ID: 1, URL: page, Kind: downloadKindVideo, Status: downloadFailed},
		{ID: 2, URL: "https://youtu.be/dQw4w9WgXcQ", Kind: downloadKindVideo, Status: downloadQueued},
	}
	downloads.mu.Unlock()
	if downloadRetry(1) {
		t.Fatal("a retry queued a page that is already waiting")
	}
	if it, ok := downloadFocused(1); !ok || it.Status != downloadFailed {
		t.Fatalf("the refused row = %+v", it)
	}
	if !downloadsHas(page, downloadKindVideo) {
		t.Fatal("the waiting row stopped counting")
	}
}

func TestForgetQueuePathDropsAWaitingStepOfATrimmedRow(t *testing.T) {
	downloadsTestReset(t)
	video := filepath.Join(downloadsVideoDir(), "trimmed.mp4")
	downloads.mu.Lock()
	downloads.sidecars = []downloadItem{{ID: 7, Path: video}, {ID: 8, Path: filepath.Join(downloadsVideoDir(), "other.mp4")}}
	downloads.mu.Unlock()
	dlForgetQueuePath(video)
	downloads.mu.Lock()
	left := append([]downloadItem(nil), downloads.sidecars...)
	downloads.mu.Unlock()
	if len(left) != 1 || left[0].ID != 8 {
		t.Fatalf("waiting steps after the delete = %+v, want only the other video's", left)
	}
}
