package main

// The idle screen mirrors what is still running: wifi beside the battery, and a
// card in the middle with the live video frame or the track's cover art. These
// tests pin the decision (what shows and when), the artwork lookup, and the
// layout maths — the drawing itself is covered by the render check at the end.

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestSaverMediaPrefersTheLiveVideo(t *testing.T) {
	got := saverMediaDecide("playing", "Deep Focus", saverMusicNow{title: "Track", playing: true})
	if got.kind != saverMediaVideo {
		t.Fatalf("kind = %v, want video", got.kind)
	}
	if got.title != "Deep Focus" || !got.playing {
		t.Fatalf("video state = %+v", got)
	}
	if got.subtitle != "Now playing" {
		t.Fatalf("subtitle = %q", got.subtitle)
	}

	paused := saverMediaDecide("paused", "Deep Focus", saverMusicNow{})
	if paused.kind != saverMediaVideo || paused.subtitle != "Paused" {
		t.Fatalf("paused video = %+v", paused)
	}
}

// With nothing on screen the saver stays the quiet starfield: no card, no bars.
func TestSaverMediaIsEmptyWhenNothingPlays(t *testing.T) {
	got := saverMediaDecide("idle", "", saverMusicNow{})
	if got.kind != saverMediaNone {
		t.Fatalf("kind = %v, want none", got.kind)
	}
	if got.title != "" || got.coverPath != "" {
		t.Fatalf("idle media carries content: %+v", got)
	}
}

func TestSaverMediaShowsMusicAndItsArtwork(t *testing.T) {
	dir := t.TempDir()
	forgetCoverArtLookups()
	t.Cleanup(forgetCoverArtLookups)

	track := filepath.Join(dir, "05 Night Drive.flac")
	if err := os.WriteFile(track, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(cover, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := saverMediaDecide("idle", "", saverMusicNow{
		title: "Night Drive", trackPath: track, playing: true, position: 65, duration: 245,
	})
	if got.kind != saverMediaMusic {
		t.Fatalf("kind = %v, want music", got.kind)
	}
	if got.coverPath != cover {
		t.Fatalf("cover = %q, want %q", got.coverPath, cover)
	}
	if got.subtitle != "Now playing · 01:05 / 04:05" {
		t.Fatalf("subtitle = %q", got.subtitle)
	}
	if !got.playing || got.paused {
		t.Fatalf("playing/paused = %v/%v", got.playing, got.paused)
	}
}

// A paused track still shows its art, so the screen answers "what was I on".
func TestSaverMediaKeepsArtworkWhenPaused(t *testing.T) {
	dir := t.TempDir()
	forgetCoverArtLookups()
	t.Cleanup(forgetCoverArtLookups)

	track := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(track, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "folder.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := saverMediaDecide("idle", "", saverMusicNow{title: "Song", trackPath: track, paused: true})
	if got.kind != saverMediaMusic || got.coverPath == "" {
		t.Fatalf("paused music = %+v", got)
	}
	if got.subtitle != "Paused" {
		t.Fatalf("subtitle = %q, want the duration-free paused label", got.subtitle)
	}
}

// No artwork means no path, which is what makes the equaliser stand in for it.
func TestSaverMediaWithoutArtwork(t *testing.T) {
	dir := t.TempDir()
	forgetCoverArtLookups()
	t.Cleanup(forgetCoverArtLookups)

	track := filepath.Join(dir, "bare.wav")
	if err := os.WriteFile(track, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := saverMediaDecide("idle", "", saverMusicNow{title: "Bare", trackPath: track, playing: true})
	if got.kind != saverMediaMusic {
		t.Fatalf("kind = %v, want music", got.kind)
	}
	if got.coverPath != "" {
		t.Fatalf("cover = %q, want none", got.coverPath)
	}
}

// The track's own image wins over the folder-default names, and an empty or
// zero-byte file is not artwork.
func TestScanCoverArtOrderAndValidity(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "intro.mp3")
	if err := os.WriteFile(track, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := scanCoverArt(dir, track); got != filepath.Join(dir, "cover.jpg") {
		t.Fatalf("folder cover = %q", got)
	}

	own := filepath.Join(dir, "intro.png")
	if err := os.WriteFile(own, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := scanCoverArt(dir, track); got != own {
		t.Fatalf("own art = %q, want %q", got, own)
	}

	// An empty file is a placeholder, not a picture: SDL would fail to load it
	// and the card would come out blank.
	other := t.TempDir()
	emptyTrack := filepath.Join(other, "x.mp3")
	if err := os.WriteFile(emptyTrack, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "cover.jpg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := scanCoverArt(other, emptyTrack); got != "" {
		t.Fatalf("zero-byte cover = %q, want none", got)
	}
}

// The card has to fit a 480p handheld as well as a 720p panel, and keep 16:9 for
// video, 1:1 for artwork.
func TestSaverMediaCardGeometry(t *testing.T) {
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {480, 272}} {
		video := saverMediaCard(saverMediaVideo, size.w, size.h)
		if video.W <= 0 || video.H <= 0 {
			t.Fatalf("%dx%d: video card %+v", size.w, size.h, video)
		}
		if video.X < 0 || video.Y < 0 || video.X+video.W > size.w || video.Y+video.H > size.h {
			t.Fatalf("%dx%d: video card leaves the screen: %+v", size.w, size.h, video)
		}
		if ratio := float64(video.W) / float64(video.H); ratio < 1.7 || ratio > 1.85 {
			t.Fatalf("%dx%d: video ratio %.2f, want 16:9", size.w, size.h, ratio)
		}
		if absInt32(video.X*2+video.W-size.w) > 2 {
			t.Fatalf("%dx%d: video card is not centred: %+v", size.w, size.h, video)
		}

		art := saverMediaCard(saverMediaMusic, size.w, size.h)
		if art.W != art.H {
			t.Fatalf("%dx%d: artwork card is not square: %+v", size.w, size.h, art)
		}
		if art.X < 0 || art.Y < 0 || art.X+art.W > size.w || art.Y+art.H > size.h {
			t.Fatalf("%dx%d: artwork card leaves the screen: %+v", size.w, size.h, art)
		}
	}

	if got := saverMediaCard(saverMediaNone, 1280, 720); got != (sdl.Rect{}) {
		t.Fatalf("no media produced a card: %+v", got)
	}
}

func TestSaverFitAndCoverRects(t *testing.T) {
	card := sdl.Rect{X: 100, Y: 50, W: 400, H: 200}

	// A square source inside a wide card letterboxes and stays centred.
	fit := saverFitRect(100, 100, card)
	if fit.W != fit.H || fit.W != 200 {
		t.Fatalf("fit = %+v, want a 200x200 square", fit)
	}
	if fit.X != card.X+(card.W-fit.W)/2 || fit.Y != card.Y+(card.H-fit.H)/2 {
		t.Fatalf("fit is not centred: %+v in %+v", fit, card)
	}

	// Cover fills the card and crops the overflow, never letterboxes.
	cover := saverCoverRect(100, 100, card)
	if cover.W < card.W || cover.H < card.H {
		t.Fatalf("cover = %+v, want it to fill %+v", cover, card)
	}
	if cover.X > card.X || cover.Y > card.Y {
		t.Fatalf("cover is not centred: %+v", cover)
	}

	// Unknown sizes pass through rather than producing a zero rect.
	if got := saverFitRect(0, 0, card); got != card {
		t.Fatalf("unknown source = %+v, want the card", got)
	}
}

// The fade scales every alpha it is given and never brightens past the request.
func TestSaverFade(t *testing.T) {
	if got := saverFade(0, 200); got != 0 {
		t.Fatalf("fade 0 = %d, want 0", got)
	}
	if got := saverFade(255, 200); got != 200 {
		t.Fatalf("fade 255 = %d, want 200", got)
	}
	if got := saverFade(128, 200); got == 0 || got > 200 {
		t.Fatalf("fade 128 = %d, want a partial alpha", got)
	}
}

func absInt32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// The card must actually reach the panel: cyan equaliser bars over the starfield
// when a track has no artwork, and nothing at all when no session is running.
func TestDrawScreensaverMediaPaintsBarsWithoutArtwork(t *testing.T) {
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)
	// The probe frame also brings SDL video and the hidden test window up; the
	// offscreen renderer is created against that window.
	probe := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	music.mu.Lock()
	previous := append([]float64(nil), music.visualizer[:]...)
	for i := range music.visualizer {
		music.visualizer[i] = float64(visualMaxH) * 0.8
	}
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		copy(music.visualizer[:], previous)
		music.mu.Unlock()
	})

	config, _, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cancelPendingUserConfigSave(t)

	// The bars are drawn in the theme accent, which the app sets when it applies
	// a theme; a bare test process has none.
	previousAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = previousAccent })

	countAccent := func() int {
		f := probe
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels: %v", err)
		}
		card := saverMediaCard(saverMediaMusic, screenWidth, screenHeight)
		n := 0
		for y := card.Y; y < card.Y+card.H; y += 2 {
			for x := card.X; x < card.X+card.W; x += 2 {
				c := f.at(x, y)
				if int(c.B) > 120 && int(c.G) > int(c.R)+40 {
					n++
				}
			}
		}
		return n
	}

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	idle := countAccent()

	renderer.Clear()
	drawScreensaverMedia(renderer, config, screensaverMedia{
		kind: saverMediaMusic, title: "Bare Track", subtitle: "Now playing", playing: true,
	}, 255)
	playing := countAccent()

	if playing <= idle+40 {
		t.Fatalf("accent pixels in the card: idle=%d playing=%d — the equaliser did not draw", idle, playing)
	}

	// Nothing playing must leave the starfield alone.
	renderer.Clear()
	drawScreensaverMedia(renderer, config, screensaverMedia{kind: saverMediaNone}, 255)
	if got := countAccent(); got > idle+40 {
		t.Fatalf("an idle saver painted %d accent pixels, want ~%d", got, idle)
	}
}
