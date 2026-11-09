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
	"os"
	"path/filepath"
	"strings"
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
