package main

// mediaextract_test.go — saving offline from the player and from Media: the
// ffmpeg command that keeps a video's sound, how its answers are read (no
// sound, a copy refused, a copy out of time, a clip too short), which actions
// a card offers, and the player sheet's two pages (rows, labels, D-pad, taps,
// and a sheet that narrows its rows instead of covering the bar). Nothing here
// runs a real ffmpeg; one test runs a two-line shell script in its place.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mediaExtractToastNow is the toast on screen, for the refusal tests.
func mediaExtractToastNow() string {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	return currentToast.Message
}

func TestMediaExtractArgs(t *testing.T) {
	copyArgs := strings.Join(mediaExtractArgs("/card/v.mp4", "/card/.extract/audio.m4a", false), " ")
	for _, want := range []string{"-i /card/v.mp4", "-map 0:a:0", "-vn", "-c:a copy", "-movflags +faststart"} {
		if !strings.Contains(copyArgs, want) {
			t.Errorf("copy args lack %q: %s", want, copyArgs)
		}
	}
	if !strings.HasSuffix(copyArgs, " /card/.extract/audio.m4a") {
		t.Errorf("the output must come last: %s", copyArgs)
	}
	encode := strings.Join(mediaExtractArgs("/card/v.webm", "/card/.extract/audio.m4a", true), " ")
	if !strings.Contains(encode, "-c:a aac") || strings.Contains(encode, "-c:a copy") {
		t.Errorf("encode args = %s", encode)
	}
}

// ffmpeg's answer for "-map 0:a:0" on a file without sound, as 6.1 prints it
// and as 7.x prints it (more lines after the two that say it).
const (
	mediaFFmpegNoSound61 = "Stream map '0:a:0' matches no streams.\n" +
		"To ignore this, add a trailing '?' to the map.\n"
	mediaFFmpegNoSound7 = mediaFFmpegNoSound61 +
		"Failed to set value '0:a:0' for option 'map': Invalid argument\n" +
		"Error parsing options for output file /card/.extract/audio.m4a.\n" +
		"Error opening output files: Invalid argument\n"
)

func TestMediaExtractNoAudio(t *testing.T) {
	for _, text := range []string{mediaFFmpegNoSound61, mediaFFmpegNoSound7, "Output file #0 does not contain any stream"} {
		if !mediaExtractNoAudioText(text) {
			t.Errorf("ffmpeg's no-sound answer was not recognised:\n%s", text)
		}
	}
	if mediaExtractNoAudioText("Could not find tag for codec opus in stream #0, codec not currently supported in container") {
		t.Fatal("a codec the container refuses is worth an encode, not a no-sound answer")
	}
	// What the runner returns for it: wrapped, so errors.Is finds it even
	// though the last line ("…Invalid argument") does not say it.
	wrapped := fmt.Errorf("%w (%v)", errMediaNoAudio, errors.New("exit status 1"))
	if !mediaExtractNoAudio(wrapped) {
		t.Fatal("the runner's no-sound error was not recognised")
	}
	if mediaExtractNoAudio(errors.New("exit status 1: Error opening output files: Invalid argument")) {
		t.Fatal("the last line alone is not a no-sound answer")
	}
	if mediaExtractNoAudio(nil) {
		t.Fatal("no error is not a missing track")
	}
	if got := mediaExtractFailure(errMediaNoAudio); !strings.Contains(got, "no sound") {
		t.Fatalf("no-sound failure = %q", got)
	}
}

// The real runner, with a script standing in for ffmpeg that prints the
// two-line answer and fails: the runner must see the whole of it.
func TestMediaExtractRunnerReadsTheWholeAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for ffmpeg")
	}
	script := filepath.Join(t.TempDir(), "ffmpeg")
	body := "#!/bin/sh\n" +
		"echo \"Stream map '0:a:0' matches no streams.\" >&2\n" +
		"echo \"To ignore this, add a trailing '?' to the map.\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "audio.m4a")
	var err error
	for try := 0; try < 3; try++ {
		err = mediaExtractRunner(context.Background(), script, mediaExtractArgs("/card/v.mp4", out, false))
		// A script written a moment ago can still be "busy" for exec while
		// another test's child inherits the write handle; that passes.
		if err == nil || !strings.Contains(err.Error(), "text file busy") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !errors.Is(err, errMediaNoAudio) {
		t.Fatalf("ffmpeg's two-line no-sound answer gave %v", err)
	}
	if got := mediaExtractFailure(err); got != "This video has no sound to extract" {
		t.Fatalf("no-sound failure = %q", got)
	}
}

func TestMediaExtractCopyTimeoutGrowsWithTheFile(t *testing.T) {
	if got := mediaExtractCopyTimeout(0); got != mediaExtractCopyBase {
		t.Fatalf("unknown size: %v", got)
	}
	if got := mediaExtractCopyTimeout(400 << 20); got != mediaExtractCopyBase+100*time.Second {
		t.Fatalf("400 MB: %v", got)
	}
	if got := mediaExtractCopyTimeout(100 << 30); got != mediaExtractCopyMax {
		t.Fatalf("100 GB: %v, want the cap", got)
	}
}

// mediaExtractFakeRunner stands in for ffmpeg: it records each attempt and
// answers with the next scripted result, writing size bytes to the output when
// the answer is a success.
type mediaExtractFakeRunner struct {
	calls   []string
	answers []error
	size    int
}

func (f *mediaExtractFakeRunner) run(_ context.Context, _ string, args []string) error {
	mode := "copy"
	if strings.Contains(strings.Join(args, " "), "-c:a aac") {
		mode = "encode"
	}
	f.calls = append(f.calls, mode)
	var err error
	if len(f.answers) > 0 {
		err, f.answers = f.answers[0], f.answers[1:]
	}
	if err == nil {
		if werr := os.WriteFile(args[len(args)-1], make([]byte, f.size), 0o644); werr != nil {
			return werr
		}
	}
	return err
}

func TestMediaExtractTrackAttempts(t *testing.T) {
	prev := mediaExtractRunner
	t.Cleanup(func() { mediaExtractRunner = prev })
	dir := t.TempDir()
	video := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "audio.m4a")
	cases := []struct {
		name    string
		answers []error
		size    int
		calls   string
		want    error
	}{
		{"copy works", nil, 8 << 10, "copy", nil},
		{"copy refused, encode works", []error{errors.New("exit status 1: Could not find tag for codec opus")}, 8 << 10, "copy encode", nil},
		{"no sound: no encode", []error{fmt.Errorf("%w (exit status 1)", errMediaNoAudio)}, 0, "copy", errMediaNoAudio},
		{"copy out of time: no encode", []error{context.DeadlineExceeded}, 0, "copy", errMediaCopyTooLong},
		{"a clip too short: no encode", nil, 100, "copy", errMediaTooShort},
	}
	for _, c := range cases {
		fake := &mediaExtractFakeRunner{answers: c.answers, size: c.size}
		mediaExtractRunner = fake.run
		err := mediaExtractTrack(context.Background(), "ffmpeg", video, out)
		if got := strings.Join(fake.calls, " "); got != c.calls {
			t.Errorf("%s: attempts = %q, want %q", c.name, got, c.calls)
		}
		switch {
		case c.want == nil && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != nil && !errors.Is(err, c.want):
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if got := mediaExtractFailure(errMediaCopyTooLong); !strings.Contains(got, "took too long") {
		t.Fatalf("copy out of time = %q", got)
	}
	if got := mediaExtractFailure(errMediaTooShort); !strings.Contains(got, "too short") {
		t.Fatalf("too short = %q", got)
	}
	if got := mediaExtractFailure(fmt.Errorf("%w: not enough space on the card", errMediaCardFull)); !strings.Contains(got, "almost full") {
		t.Fatalf("card full = %q", got)
	}
}

func TestMediaExtractAudioRefusalsStartNothing(t *testing.T) {
	clearToastAfterTest(t)
	dir := t.TempDir()
	if mediaExtractAudio(nil, filepath.Join(dir, "gone.mp4"), "Gone", "") {
		t.Fatal("a missing file started an extraction")
	}
	song := filepath.Join(dir, "song.m4a")
	if err := os.WriteFile(song, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if mediaExtractAudio(nil, song, "Song", "") || !strings.Contains(mediaExtractToastNow(), "sound file already") {
		t.Fatalf("a track was offered to extract its own sound (toast %q)", mediaExtractToastNow())
	}
	video := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mediaExtracting.CompareAndSwap(false, true) {
		t.Skip("an extraction from another test is running")
	}
	t.Cleanup(func() { mediaExtracting.Store(false) })
	if mediaExtractAudio(nil, video, "Clip", "") || !strings.Contains(mediaExtractToastNow(), "one at a time") {
		t.Fatalf("a second extraction started (toast %q)", mediaExtractToastNow())
	}
}

func mediaHasAction(acts []mediaSheetAction, want mediaSheetAction) bool {
	for _, a := range acts {
		if a == want {
			return true
		}
	}
	return false
}

func TestMediaSheetOffersAudioAndExtract(t *testing.T) {
	web := mediaCard{title: "Talk", video: VideoInfo{WebpageURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}}
	acts, labels := mediaSheetActionsFor(web, true, false, false, false, false)
	if !mediaHasAction(acts, mediaActDownload) || !mediaHasAction(acts, mediaActDownloadAudio) || mediaHasAction(acts, mediaActExtractAudio) {
		t.Fatalf("online web card offers %v", labels)
	}
	if len(acts) != len(labels) {
		t.Fatalf("%d actions but %d labels", len(acts), len(labels))
	}
	if acts, labels := mediaSheetActionsFor(web, false, false, false, false, false); mediaHasAction(acts, mediaActDownloadAudio) || mediaHasAction(acts, mediaActDownload) {
		t.Fatalf("offline web card offers downloads: %v", labels)
	}

	downloaded := web
	downloaded.downloaded = true
	if acts, labels := mediaSheetActionsFor(downloaded, true, false, false, false, false); !mediaHasAction(acts, mediaActExtractAudio) || !mediaHasAction(acts, mediaActRemoveDownload) || mediaHasAction(acts, mediaActDownload) {
		t.Fatalf("downloaded web card offers %v", labels)
	}

	local := mediaCard{title: "Clip", path: "/card/downloads/video/Clip.mp4", libID: "id1", downloaded: true,
		video: VideoInfo{URL: "/card/downloads/video/Clip.mp4"}}
	if acts, labels := mediaSheetActionsFor(local, true, false, false, false, false); !mediaHasAction(acts, mediaActExtractAudio) || !mediaHasAction(acts, mediaActRemoveDownload) || mediaHasAction(acts, mediaActDownloadAudio) {
		t.Fatalf("local video offers %v", labels)
	}
	song := mediaCard{title: "Song", path: "/card/downloads/music/Song.m4a", libID: "id2", downloaded: true,
		video: VideoInfo{URL: "/card/downloads/music/Song.m4a"}}
	if acts, labels := mediaSheetActionsFor(song, true, false, false, false, false); mediaHasAction(acts, mediaActExtractAudio) {
		t.Fatalf("a track offers to extract its audio: %v", labels)
	}
	if mediaSheetIcon(mediaActDownloadAudio) != "music" || mediaSheetIcon(mediaActExtractAudio) != "music" {
		t.Fatal("the audio actions draw the music glyph")
	}
}

// A page whose sound is a saved track already offers neither way to save it
// again: both rows would only refuse.
func TestMediaSheetSkipsSoundThatIsSaved(t *testing.T) {
	web := mediaCard{title: "Talk", video: VideoInfo{WebpageURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}}
	if acts, labels := mediaSheetActionsFor(web, true, false, false, false, true); mediaHasAction(acts, mediaActDownloadAudio) || !mediaHasAction(acts, mediaActDownload) {
		t.Fatalf("online card with its sound saved offers %v", labels)
	}
	downloaded := web
	downloaded.downloaded = true
	if acts, labels := mediaSheetActionsFor(downloaded, true, false, false, false, true); mediaHasAction(acts, mediaActExtractAudio) || !mediaHasAction(acts, mediaActRemoveDownload) {
		t.Fatalf("downloaded card with its sound saved offers %v", labels)
	}
	local := mediaCard{title: "Clip", path: "/card/downloads/video/Clip.mp4", libID: "id1", downloaded: true,
		video: VideoInfo{URL: "/card/downloads/video/Clip.mp4"}}
	if acts, labels := mediaSheetActionsFor(local, true, false, false, false, true); mediaHasAction(acts, mediaActExtractAudio) {
		t.Fatalf("local video with its sound saved offers %v", labels)
	}
}

func TestVideoSheetPages(t *testing.T) {
	settings := videoSheetRowIDsFor(videoSheetMain, videoSheetContext{})
	if len(settings) != 7 || settings[5] != vsRowSave || settings[6] != vsRowClose {
		t.Fatalf("settings page = %v", settings)
	}
	if got := videoSheetRowIndex(settings, vsRowSave); got != 5 {
		t.Fatalf("Save offline sits at %d", got)
	}
	stream := videoSheetRowIDsFor(videoSheetSave, videoSheetContext{})
	if len(stream) != 3 || stream[0] != vsRowSaveVideo || stream[1] != vsRowSaveAudio || stream[2] != vsRowBack {
		t.Fatalf("stream save page = %v", stream)
	}
	// A file's page keeps its rows whatever Get subtitles can do, so the ring
	// never slides onto another action between two presses.
	for _, s := range []videoSheetSubs{vsSubsFetchable, vsSubsLoaded, vsSubsBusy, vsSubsNoSource, vsSubsNone, vsSubsBadLanguage} {
		file := videoSheetRowIDsFor(videoSheetSave, videoSheetContext{local: true, subs: s})
		if len(file) != 3 || file[0] != vsRowExtractAudio || file[1] != vsRowGetSubtitles || file[2] != vsRowBack {
			t.Fatalf("file save page (subtitles %d) = %v", s, file)
		}
		if label := videoSheetRowLabel(vsRowGetSubtitles, videoSheetContext{local: true, subs: s}); !strings.HasPrefix(label, "Get subtitles: ") || label == "Get subtitles: " {
			t.Errorf("Get subtitles (state %d) reads %q", s, label)
		}
	}
	for s, want := range map[videoSheetSubs]string{vsSubsLoaded: "Loaded", vsSubsNoSource: "Source unknown",
		vsSubsNone: "None found", vsSubsBadLanguage: "Check subtitle language", vsSubsBusy: "Looking…"} {
		if got := videoSheetSubsValue(s); got != want {
			t.Errorf("subtitles state %d reads %q, want %q", s, got, want)
		}
	}
	for _, id := range append(append(settings, stream...), vsRowExtractAudio, vsRowGetSubtitles) {
		if videoSheetRowLabel(id, videoSheetContext{}) == "" {
			t.Errorf("row %d has no label", id)
		}
	}
	live := videoSheetContext{live: true}
	for id, want := range map[videoSheetRowID]string{vsRowSave: "Not for live streams", vsRowSaveVideo: "Live", vsRowSaveAudio: "Live"} {
		if got := videoSheetRowLabel(id, live); !strings.HasSuffix(got, ": "+want) {
			t.Errorf("live row %d reads %q", id, got)
		}
	}
}

func TestVideoSheetNavigationFollowsThePage(t *testing.T) {
	prevOpen, prevItem, prevPage := videoSettingsOpen, videoSettingsItem, videoSettingsPage
	t.Cleanup(func() { videoSettingsOpen, videoSettingsItem, videoSettingsPage = prevOpen, prevItem, prevPage })
	if playbackCurrentSession() != nil {
		t.Skip("a playback session from another test is live")
	}

	videoSettingsOpen, videoSettingsPage, videoSettingsItem = true, videoSheetMain, 6
	moveVideoSettingsItem(1)
	if videoSettingsItem != 0 {
		t.Fatalf("down from the last row went to %d, want a wrap to 0", videoSettingsItem)
	}
	moveVideoSettingsItem(-1)
	if videoSettingsItem != 6 {
		t.Fatalf("up from the first row went to %d", videoSettingsItem)
	}
	videoSettingsPage, videoSettingsItem = videoSheetSave, 9
	moveVideoSettingsItem(1)
	if videoSettingsItem != 0 {
		t.Fatalf("a selection past a shorter page must clamp, then wrap: %d", videoSettingsItem)
	}
	videoSettingsBack()
	if !videoSettingsOpen || videoSettingsPage != videoSheetMain || videoSettingsItem != 5 {
		t.Fatalf("back from Save offline: open=%v page=%d item=%d", videoSettingsOpen, videoSettingsPage, videoSettingsItem)
	}
	videoSettingsBack()
	if videoSettingsOpen {
		t.Fatal("back on the settings page must close the sheet")
	}
	videoSettingsPage = videoSheetSave
	videoSettingsToggle()
	if !videoSettingsOpen || videoSettingsPage != videoSheetMain || videoSettingsItem != 0 {
		t.Fatal("opening the sheet must start on its first page and row")
	}
}

func TestVideoSheetFitsAboveTheBar(t *testing.T) {
	full := vcGeom{w: 1280, h: 720, bar: sdl.Rect{X: 16, Y: 596, W: 1248, H: 108}}
	if _, rowH := vcSheetRect(full, 0, 0, 7); rowH != videoSettingsRowH {
		t.Fatalf("full screen narrowed the rows to %d", rowH)
	}
	window := vcGeom{w: 860, h: 484, bar: sdl.Rect{X: 12, Y: 364, W: 836, H: 108}}
	rect, rowH := vcSheetRect(window, 0, 0, 7)
	if rowH >= videoSettingsRowH || rowH < videoSettingsRowMinH {
		t.Fatalf("window row height = %d", rowH)
	}
	if rect.Y < 12 || rect.Y+rect.H > window.bar.Y {
		t.Fatalf("sheet %+v covers the bar at %d", rect, window.bar.Y)
	}
}

func TestVideoSheetTapsUseTheDrawnRows(t *testing.T) {
	prevSheet, prevRows, prevH := videoSettingsSheetDrawn, videoSettingsSheetRowsDrawn, videoSettingsSheetRowH
	t.Cleanup(func() {
		videoSettingsSheetDrawn, videoSettingsSheetRowsDrawn, videoSettingsSheetRowH = prevSheet, prevRows, prevH
	})
	videoSettingsSheetDrawn = sdl.Rect{X: 100, Y: 100, W: 400, H: 3*40 + videoSettingsTopPad + 40}
	videoSettingsSheetRowsDrawn, videoSettingsSheetRowH = 3, 40
	top := int32(100) + videoSettingsTopPad
	if got := vcSheetRow(200, top+45); got != 1 {
		t.Fatalf("a tap on the second row hit %d", got)
	}
	if got := vcSheetRow(200, top+3*40-1); got != 2 {
		t.Fatalf("a tap on the last row hit %d", got)
	}
	// The band above the rows and the status line under them press nothing:
	// on the Save offline page the first and last rows start a download.
	if got := vcSheetRow(200, 100+5); got != vcSheetMargin {
		t.Fatalf("a tap above the rows hit %d, want the margin", got)
	}
	if got := vcSheetRow(200, top+3*40+10); got != vcSheetMargin {
		t.Fatalf("a tap on the status line hit %d, want the margin", got)
	}
	if got := vcSheetRow(50, top+45); got != -1 {
		t.Fatalf("a tap beside the sheet hit %d", got)
	}
	videoSettingsSheetDrawn = sdl.Rect{}
	if got := vcSheetRow(200, top+45); got != -1 {
		t.Fatalf("a sheet that is not drawn was hit at %d", got)
	}
}
