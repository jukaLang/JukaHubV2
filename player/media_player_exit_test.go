package main

// media_player_exit_test.go — leaving the player never leaves a video playing
// out of sight (the "video from Files keeps playing after Back" bug): a file
// from the card is closed by Back and by every other way out, returning to
// the screen that opened it; a stream keeps playing in the floating window;
// and a file that reaches its end is handed to the render thread instead of
// being torn down under it.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// mediaLocalSession starts a session for a file on the card, the way Files
// opens one, and restores the player state when the test ends.
func mediaLocalSession(t *testing.T) *playbackSession {
	t.Helper()
	restorePlaybackGlobals(t)
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(clip, []byte("not really a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := beginPlaybackSession(clip, clip, false)
	if !sess.local {
		t.Fatal("a file on the card did not start a local session")
	}
	return sess
}

// mediaClearUpNext empties Up Next for one test, so the end of a video has no
// queue to hand over to.
func mediaClearUpNext(t *testing.T) {
	t.Helper()
	prev := upNextSnapshot()
	prevArmed := shortsAutoplay
	upNextClear()
	shortsAutoplay = false
	t.Cleanup(func() {
		upNextClear()
		for _, v := range prev {
			upNextAdd(v)
		}
		shortsAutoplay = prevArmed
		upNextAdvancePending = false
	})
}

// Back on a file opened from Files closes it and returns to Files, from full
// screen too: there is nothing to keep watching behind a local clip.
func TestBackOnALocalFileClosesItAndReturnsToTheOpener(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	mediaLocalSession(t)
	videoFullscreen = true

	if !HandleVideoControllerInput(backButton()) {
		t.Fatal("B on the player was not consumed")
	}
	if got := playerPhase(); got != "idle" {
		t.Fatalf("phase after Back on a local file = %q, want idle", got)
	}
	if videoMiniActive() || videoFullscreen {
		t.Fatal("a closed file left the mini window or full screen armed")
	}
	if pendingSceneIndex != miniTestHome {
		t.Fatalf("Back did not return to the screen that opened the file: pending=%d", pendingSceneIndex)
	}
}

// The handheld's Menu/Select button is the same Back: it used to fall through
// to the scene underneath and leave the video playing with nothing drawing it.
func TestMenuButtonOnThePlayerIsBack(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	mediaLocalSession(t)

	menu := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_BACK}
	if !HandleVideoControllerInput(menu) {
		t.Fatal("the Menu button on the player was not consumed")
	}
	if got := playerPhase(); got != "idle" {
		t.Fatalf("phase after Menu on a local file = %q, want idle", got)
	}
}

// A file Media itself played closes on Back but stays on Media.
func TestBackOnMediasOwnFileStaysOnMedia(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	mediaLocalSession(t)
	mediaNotePlayOrigin(appConfig)

	HandleVideoControllerInput(backButton())
	if got := playerPhase(); got != "idle" {
		t.Fatalf("phase after Back = %q, want idle", got)
	}
	if pendingSceneIndex != -1 {
		t.Fatalf("Back left Media for scene %d; the file was played from Media itself", pendingSceneIndex)
	}
}

// Leaving without Back (Home, the Start menu, the header's back pill) left the
// session playing with nothing drawing it. The main loop now closes a file
// the user has seen once nothing shows it.
func TestAnOrphanedLocalFileIsClosed(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	sess := mediaLocalSession(t)
	sess.shown.Store(true)
	currentSceneIndex = miniTestHome

	videoSyncPlaybackEnd()
	if got := playerPhase(); got != "idle" {
		t.Fatalf("an orphaned local file kept playing: phase = %q", got)
	}
}

// An orphaned stream keeps playing where the user can see it: in the floating
// window over the screen they went to.
func TestAnOrphanedStreamFloatsIntoTheMiniWindow(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	restorePlaybackGlobals(t)
	sess := beginPlaybackSession("https://cdn.example.com/v.mp4", "https://www.youtube.com/watch?v=abcdefghijk", false)
	if sess.local {
		t.Fatal("a stream was taken for a local file")
	}
	currentSceneIndex = miniTestHome

	videoSyncPlaybackEnd()
	if got := playerPhase(); got != "playing" {
		t.Fatalf("an orphaned stream was stopped: phase = %q", got)
	}
	if !videoMiniActive() || !videoMiniVisible() {
		t.Fatal("an orphaned stream is not shown in the floating window")
	}
}

// The end of a file is the render thread's: finish() on the decoder goroutine
// leaves it alone, and the main loop's check returns to the opener.
func TestTheEndOfAFileFromFilesReturnsThere(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	sess := mediaLocalSession(t)
	mediaClearUpNext(t)
	t.Cleanup(func() { playbackEnded.Store(false) })

	sess.ended.Store(true)
	playbackEnded.Store(true)
	sess.finish()
	if playbackCurrentSession() != sess || playerPhase() != "playing" {
		t.Fatal("the decoder goroutine tore down a session that reached its end before the main loop saw it")
	}

	videoSyncPlaybackEnd()
	if got := playerPhase(); got != "idle" {
		t.Fatalf("phase after the end of the file = %q, want idle", got)
	}
	if pendingSceneIndex != miniTestHome {
		t.Fatalf("the end of a file from Files did not return there: pending=%d", pendingSceneIndex)
	}
}

// A decoder that stops without reaching the end (an error, no frames) still
// closes its session itself.
func TestFinishWithoutTheEndStopsTheSession(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	sess := mediaLocalSession(t)
	playbackEnded.Store(false)

	sess.finish()
	if got := playerPhase(); got != "idle" || playbackCurrentSession() != nil {
		t.Fatalf("finish() left phase %q and a current session", got)
	}
}

// A game or an app taking the screen ends playback: the parked main loop no
// longer draws the player, and the audio would go on over the game.
func TestAGameTakingTheScreenStopsPlayback(t *testing.T) {
	setupVideoMiniTest(t, miniTestTube)
	mediaLocalSession(t)

	mediaStopForScreenChild()
	if got := playerPhase(); got != "idle" {
		t.Fatalf("playback survived a game launch: phase = %q", got)
	}
}

func TestMediaFitRectKeepsThePicturesShape(t *testing.T) {
	cases := []struct {
		name   string
		box    sdl.Rect
		vw, vh int32
		want   sdl.Rect
	}{
		{"4:3 in 16:9", sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}, 640, 480, sdl.Rect{X: 160, Y: 0, W: 960, H: 720}},
		{"cinema in 16:9", sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}, 1920, 800, sdl.Rect{X: 0, Y: 93, W: 1280, H: 533}},
		{"same shape", sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}, 1280, 720, sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}},
		{"rounding only", sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}, 1281, 720, sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}},
		{"unknown size", sdl.Rect{X: 5, Y: 6, W: 100, H: 50}, 0, 0, sdl.Rect{X: 5, Y: 6, W: 100, H: 50}},
		{"a Short", sdl.Rect{X: 100, Y: 50, W: 800, H: 450}, 1080, 1920, sdl.Rect{X: 373, Y: 50, W: 253, H: 450}},
	}
	for _, c := range cases {
		if got := mediaFitRect(c.box, c.vw, c.vh); got != c.want {
			t.Errorf("%s: mediaFitRect = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestMediaPageIsLocalFileOnlyForFiles(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(clip, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		page string
		want bool
	}{
		{clip, true},
		{"https://www.youtube.com/watch?v=abcdefghijk", false},
		{filepath.Join(dir, "missing.mp4"), false},
		{dir, false},
		{"", false},
	} {
		if got := mediaPageIsLocalFile(c.page); got != c.want {
			t.Errorf("mediaPageIsLocalFile(%q) = %v, want %v", c.page, got, c.want)
		}
	}
}
