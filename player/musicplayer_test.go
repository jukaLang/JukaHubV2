package main

// Regression tests for the audio player that Files opens on an audio file:
// the playlist it builds, the scene it needs, its transport hitboxes, and the
// settings scenes that used to draw labels on top of their fields.

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// audioTestConfig is a minimal config without a MusicPlayer scene, so the
// tests exercise the on-demand scene creation older configs rely on.
func audioTestConfig() *Config {
	return &Config{Scenes: []SceneConfig{
		{Name: "Main"},
		{Name: "FileExplorer", Elements: []Element{{Type: "dynamiclist", Variable: "fe_entries"}}},
	}}
}

// musicTestFiles writes a small music folder and returns its path.
func musicTestFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"b-song.mp3", "a-song.flac", "clip.mp4", "notes.txt", "c-song.ogg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

func resetMusicState(t *testing.T) {
	t.Helper()
	music.mu.Lock()
	prevTracks := music.tracks
	prevDir := music.dir
	prevVolume := music.volume
	music.mu.Unlock()
	t.Cleanup(func() {
		music.StopForTest()
		music.mu.Lock()
		music.tracks = prevTracks
		music.dir = prevDir
		music.volume = prevVolume
		music.current = -1
		music.cursor = 0
		music.ready = false
		music.mu.Unlock()
	})
	music.mu.Lock()
	music.tracks = nil
	music.current = -1
	music.cursor = 0
	music.ready = false
	music.mu.Unlock()
}

// StopForTest halts a decoder a test may have started.
func (m *musicState) StopForTest() {
	m.mu.Lock()
	m.gen++
	proc := m.proc
	m.proc = nil
	m.playing = false
	m.paused = false
	m.mu.Unlock()
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
	}
}

func TestIsAudioFileRecognisesPlayerFormats(t *testing.T) {
	for name, want := range map[string]bool{
		"song.mp3":       true,
		"SONG.FLAC":      true,
		"track.ogg":      true,
		"voice.m4a":      true,
		"movie.mp4":      false,
		"clip.mkv":       false,
		"notes.txt":      false,
		"no-extension":   false,
		"stream.m3u8":    false,
		"podcast.opus":   true,
		"broken.mp3.exe": false,
	} {
		if got := isAudioFile(name); got != want {
			t.Errorf("isAudioFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAudioFilesInFiltersAndSorts(t *testing.T) {
	dir := musicTestFiles(t)
	files := audioFilesIn(dir)
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	want := []string{"a-song.flac", "b-song.mp3", "c-song.ogg"}
	if len(names) != len(want) {
		t.Fatalf("audioFilesIn = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("audioFilesIn = %v, want %v (sorted, audio only)", names, want)
		}
	}
	for _, f := range files {
		if !filepath.IsAbs(f.Path) && dir != "." {
			t.Errorf("%s: path %q should be absolute for the decoder", f.Name, f.Path)
		}
	}
}

func TestMusicLoadPlaylistSelectsRequestedTrack(t *testing.T) {
	resetMusicState(t)
	dir := musicTestFiles(t)

	idx := musicLoadPlaylist(dir, filepath.Join(dir, "b-song.mp3"))
	if len(music.tracks) != 3 {
		t.Fatalf("playlist has %d tracks, want 3", len(music.tracks))
	}
	if idx != 1 {
		t.Fatalf("start index = %d, want 1 (b-song.mp3)", idx)
	}
	if music.dir != dir {
		t.Errorf("playlist dir = %q, want %q", music.dir, dir)
	}
	if music.current != -1 {
		t.Errorf("current = %d, want -1 until playback starts", music.current)
	}
}

func TestEnsureMusicSceneAddsMissingScene(t *testing.T) {
	cfg := audioTestConfig()
	idx := ensureMusicScene(cfg)
	if idx < 0 || idx >= len(cfg.Scenes) {
		t.Fatalf("ensureMusicScene returned %d", idx)
	}
	if cfg.Scenes[idx].Name != "MusicPlayer" {
		t.Fatalf("scene %d is %q, want MusicPlayer", idx, cfg.Scenes[idx].Name)
	}
	if again := ensureMusicScene(cfg); again != idx {
		t.Errorf("second call returned %d, want the same scene %d", again, idx)
	}
	if count := len(cfg.Scenes); count != 3 {
		t.Errorf("scene count = %d, want 3 (no duplicate MusicPlayer)", count)
	}
}

// The player must not start decoding when a test (or the user) just wants the
// scene: openAudioPlayerBrowser only loads a playlist when there is one.
func TestOpenAudioPlayerBrowserLoadsFolder(t *testing.T) {
	resetMusicState(t)
	cfg := audioTestConfig()
	dir := musicTestFiles(t)

	prevIndex := currentSceneIndex
	currentSceneIndex = 0
	t.Cleanup(func() { currentSceneIndex = prevIndex })

	// Stub the decoder so the test never launches ffmpeg.
	prevPlay := musicPlayTrackHook
	musicPlayTrackHook = func(index int) {
		music.mu.Lock()
		music.current = index
		music.cursor = index
		music.mu.Unlock()
	}
	t.Cleanup(func() { musicPlayTrackHook = prevPlay })

	openAudioPlayerBrowser(cfg, dir)
	if len(music.tracks) != 3 {
		t.Fatalf("playlist has %d tracks, want 3", len(music.tracks))
	}
	if music.current != 0 {
		t.Errorf("current = %d, want the first track to start", music.current)
	}
	if findSceneIndex(cfg, "MusicPlayer") < 0 {
		t.Fatal("player scene was not created")
	}
}

func TestOpenAudioPlayerFindsTrackInFolder(t *testing.T) {
	resetMusicState(t)
	cfg := audioTestConfig()
	dir := musicTestFiles(t)

	prevPlay := musicPlayTrackHook
	musicPlayTrackHook = func(index int) {
		music.mu.Lock()
		music.current = index
		music.mu.Unlock()
	}
	t.Cleanup(func() { musicPlayTrackHook = prevPlay })

	openAudioPlayer(cfg, filepath.Join(dir, "c-song.ogg"))
	if music.current != 2 {
		t.Fatalf("current = %d, want 2 (c-song.ogg)", music.current)
	}
	if music.tracks[music.current].Name != "c-song.ogg" {
		t.Errorf("playing %q, want c-song.ogg", music.tracks[music.current].Name)
	}
}

// The transport buttons are drawn by the player itself; their rects have to be
// inside the window and separated, otherwise a tap hits two of them.
func TestMusicTransportHitboxesAreSeparate(t *testing.T) {
	withScreen(t, 1280, 720)
	resetMusicState(t)
	musicLoadPlaylist(musicTestFiles(t), "")

	newProbeFrame(t) // initialises SDL and renderWin
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	renderMusicPlayer(renderer, &Config{})

	if !musicRects.valid {
		t.Fatal("render did not publish the transport hitboxes")
	}
	rects := map[string]sdl.Rect{
		"prev":      musicRects.prev,
		"playPause": musicRects.playPause,
		"next":      musicRects.next,
		"stop":      musicRects.stop,
		"volDown":   musicRects.volDown,
		"volUp":     musicRects.volUp,
	}
	for name, r := range rects {
		if r.W < 30 || r.H < 30 {
			t.Errorf("%s: hitbox %v is smaller than a fingertip", name, r)
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > screenWidth || r.Y+r.H > screenHeight {
			t.Errorf("%s: hitbox %v is outside the screen", name, r)
		}
	}
	names := []string{"prev", "playPause", "next", "stop", "volDown", "volUp"}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a, b := rects[names[i]], rects[names[j]]
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Errorf("%s %v overlaps %s %v", names[i], a, names[j], b)
			}
		}
	}
}

// Tapping a playlist row starts that row, not the top of the list, even when
// the list is scrolled.
func TestMusicPlaylistClickPlaysVisibleRow(t *testing.T) {
	withScreen(t, 1280, 720)
	resetMusicState(t)
	dir := musicTestFiles(t)
	musicLoadPlaylist(dir, "")

	newProbeFrame(t) // initialises SDL and renderWin
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	renderMusicPlayer(renderer, &Config{})

	if len(musicRects.rows) == 0 {
		t.Fatal("no playlist rows were drawn")
	}
	prevPlay := musicPlayTrackHook
	played := -1
	musicPlayTrackHook = func(index int) { played = index }
	t.Cleanup(func() { musicPlayTrackHook = prevPlay })

	row := musicRects.rows[len(musicRects.rows)-1]
	if !HandleMusicClick(row.X+10, row.Y+10) {
		t.Fatal("click on a playlist row was not consumed")
	}
	want := musicRects.rowStart + len(musicRects.rows) - 1
	if played != want {
		t.Fatalf("clicked row played %d, want %d", played, want)
	}
}

// Tapping the artwork shows the synced lyrics and tapping again hides them.
// The lyrics view occupies the artwork slot, so the rest of the layout (seek
// bar, transport, playlist) must not move when it is toggled.
func TestMusicArtworkTapTogglesLyrics(t *testing.T) {
	withScreen(t, 1280, 720)
	resetMusicState(t)
	musicLoadPlaylist(musicTestFiles(t), "")

	music.mu.Lock()
	music.current = 0
	music.lyrics = parseLRC("[00:00.00]one\n[00:02.00]two\n[00:04.00]three")
	music.showLyrics = false
	music.position = 3
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.lyrics = nil
		music.showLyrics = false
		music.position = 0
		music.mu.Unlock()
	})

	renderMusicForTest(t)
	art := musicRects.art
	if art.W < 100 || art.H < 100 {
		t.Fatalf("artwork hitbox %v is implausibly small", art)
	}
	seekBefore := musicRects.seek

	if !HandleMusicClick(art.X+12, art.Y+12) {
		t.Fatal("tap on the artwork was not consumed")
	}
	music.mu.Lock()
	shown := music.showLyrics
	music.mu.Unlock()
	if !shown {
		t.Fatal("artwork tap did not enable lyrics")
	}

	renderMusicForTest(t) // draws the lyric lines instead of the bars
	if musicRects.seek != seekBefore {
		t.Errorf("seek bar moved when lyrics were shown: %v -> %v", seekBefore, musicRects.seek)
	}
	if musicRects.art != art {
		t.Errorf("artwork rect changed when lyrics were shown: %v -> %v", art, musicRects.art)
	}

	if !HandleMusicClick(art.X+12, art.Y+12) {
		t.Fatal("second tap on the artwork was not consumed")
	}
	music.mu.Lock()
	shown = music.showLyrics
	music.mu.Unlock()
	if shown {
		t.Error("second artwork tap did not hide lyrics")
	}
}

// renderMusicForTest draws the player into an offscreen renderer so the
// hitboxes are published exactly as they are on screen.
func renderMusicForTest(t *testing.T) {
	t.Helper()
	withScreen(t, 1280, 720)
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	renderMusicPlayer(renderer, &Config{})
}

// renderMusicFrame draws the player and returns the captured pixels.
func renderMusicFrame(t *testing.T) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	prevAccent := accentColor
	SetAccentColor(sdl.Color{R: 85, G: 216, B: 255, A: 255})
	t.Cleanup(func() { SetAccentColor(prevAccent) })
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMusicPlayer(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

// The player has to paint, not just publish hitboxes: the title, the primary
// transport button and the playlist rows are all visible.
func TestMusicPlayerRendersPanels(t *testing.T) {
	resetMusicState(t)
	musicLoadPlaylist(musicTestFiles(t), "")
	renderMusicForTest(t) // publishes musicRects
	f := renderMusicFrame(t)

	// Track title area (right of the artwork tile) holds bright text.
	if n := overlayCountNear(f, ColorTextPrimary(), 316, 96, 1240, 170, 70); n == 0 {
		t.Error("no title text drawn next to the artwork")
	}
	// The primary transport button is filled with the accent color.
	pp := musicRects.playPause
	if c := f.at(pp.X+4, pp.Y+4); overlayAbs(int32(c.R)-int32(accentColor.R)) > 40 ||
		overlayAbs(int32(c.G)-int32(accentColor.G)) > 40 ||
		overlayAbs(int32(c.B)-int32(accentColor.B)) > 40 {
		t.Errorf("play button is %+v, want accent %+v", c, accentColor)
	}
	// Playlist rows draw their track names.
	if len(musicRects.rows) == 0 {
		t.Fatal("playlist has no rows")
	}
	row := musicRects.rows[0]
	if n := overlayCountNear(f, ColorTextPrimary(), row.X+50, row.Y, row.X+row.W-20, row.Y+row.H, 70); n == 0 {
		t.Error("no track name drawn in the first playlist row")
	}
}

// TestMusicPlayerPreview writes a PNG of the player for design review. It only
// runs when JUKA_PREVIEW=1 so the normal suite stays headless-only.
func TestMusicPlayerPreview(t *testing.T) {
	if os.Getenv("JUKA_PREVIEW") == "" {
		t.Skip("set JUKA_PREVIEW=1 to write tmp/audioplayer-preview.png")
	}
	resetMusicState(t)
	// Show the new playback modes in their on state so the preview covers the
	// buttons, the status line and the favorite row marker.
	withTempFavorites(t)
	dir := musicTestFiles(t)
	musicLoadPlaylist(dir, "")
	musicToggleShuffle()
	musicCycleRepeatMode()
	musicToggleFavorite(filepath.Join(dir, "a-song.flac"))
	f := renderMusicFrame(t)

	img := image.NewRGBA(image.Rect(0, 0, int(probeW), int(probeH)))
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			i := (y*probeW + x) * 4
			c := f.buf[i : i+4]
			img.SetRGBA(int(x), int(y), color.RGBA{R: c[2], G: c[1], B: c[0], A: 255})
		}
	}
	out := filepath.Join("..", "tmp", "audioplayer-preview.png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", out)
}

// Transport taps must drive the player: pause toggles, next/prev change track
// and a seek tap restarts decoding at the tapped offset.
func TestMusicTransportClicks(t *testing.T) {
	resetMusicState(t)
	dir := musicTestFiles(t)
	musicLoadPlaylist(dir, "")

	prevPlay := musicPlayTrackHook
	var played []int
	musicPlayTrackHook = func(index int) {
		played = append(played, index)
		// Mirror what the real starter does, so next/prev step from the new track.
		music.mu.Lock()
		music.current = index
		music.mu.Unlock()
	}
	t.Cleanup(func() { musicPlayTrackHook = prevPlay })

	type seekCall struct {
		path  string
		start float64
	}
	prevDecode := musicDecodeHook
	var seeks []seekCall
	musicDecodeHook = func(path string, startSec float64, gen int) {
		seeks = append(seeks, seekCall{path: path, start: startSec})
	}
	t.Cleanup(func() { musicDecodeHook = prevDecode })

	music.mu.Lock()
	music.current = 1
	music.playing = true
	music.paused = false
	music.duration = 100
	music.position = 50
	music.mu.Unlock()

	renderMusicForTest(t)

	pp := musicRects.playPause
	if !HandleMusicClick(pp.X+pp.W/2, pp.Y+pp.H/2) {
		t.Fatal("pause tap was not consumed")
	}
	music.mu.Lock()
	paused := music.paused
	music.mu.Unlock()
	if !paused {
		t.Error("pause tap did not pause playback")
	}

	n := musicRects.next
	HandleMusicClick(n.X+n.W/2, n.Y+n.H/2)
	p := musicRects.prev
	HandleMusicClick(p.X+p.W/2, p.Y+p.H/2)
	if len(played) != 2 {
		t.Fatalf("next/prev triggered %d plays, want 2", len(played))
	}
	if played[0] != 2 {
		t.Errorf("next played %d, want 2", played[0])
	}
	if played[1] != 1 {
		t.Errorf("prev played %d, want 1", played[1])
	}

	seek := musicRects.seek
	HandleMusicClick(seek.X+seek.W/2, seek.Y+seek.H/2)
	if len(seeks) != 1 {
		t.Fatalf("seek tap started %d decoders, want 1", len(seeks))
	}
	if seeks[0].start < 45 || seeks[0].start > 55 {
		t.Errorf("seek to the middle started at %.1fs, want ~50s", seeks[0].start)
	}
}

// settingsScenes loads the shipped configs and returns the layout scenes.
func settingsScenes(t *testing.T, path string) []SceneConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s not available: %v", path, err)
	}
	var cfg struct {
		Scenes []SceneConfig `json:"scenes"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return cfg.Scenes
}

// Labels used to be drawn on top of the field below them (132 + 24px text vs a
// field starting at 154). Every settings field must start below its label and
// leave a readable gap to the next block.
func TestSettingsScenesHaveReadableSpacing(t *testing.T) {
	const labelHeight = int32(30) // default font is 24px; line box is taller
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			for _, scene := range settingsScenes(t, path) {
				if scene.Name != "SettingsGeneral" && scene.Name != "SettingsAppearance" {
					continue
				}
				var labels []sdl.Rect
				var fields []sdl.Rect
				for _, e := range scene.Elements {
					w, h := resolveElementDimensions(e, 200, 40)
					switch e.Type {
					case "label":
						labelW := w
						if labelW <= 0 {
							labelW = 320
						}
						labels = append(labels, sdl.Rect{X: e.X, Y: e.Y, W: labelW, H: labelHeight})
					case "input", "toggle", "button":
						fields = append(fields, sdl.Rect{X: e.X, Y: e.Y, W: w, H: h})
					}
				}
				for _, l := range labels {
					for _, f := range fields {
						if l.X < f.X+f.W && f.X < l.X+l.W && l.Y < f.Y+f.H && f.Y < l.Y+l.H {
							t.Errorf("%s: label %v overlaps field %v", scene.Name, l, f)
						}
					}
					// A label needs a little breathing room under it.
					for _, f := range fields {
						if l.X < f.X+f.W && f.X < l.X+l.W && f.Y >= l.Y && f.Y-l.Y < 24 {
							t.Errorf("%s: label %v is only %dpx above its field %v", scene.Name, l, f.Y-l.Y, f)
						}
					}
				}
				// Two fields must never be stacked flush against each other.
				for i := 0; i < len(fields); i++ {
					for j := i + 1; j < len(fields); j++ {
						a, b := fields[i], fields[j]
						overlapX := a.X < b.X+b.W && b.X < a.X+a.W
						if overlapX && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
							t.Errorf("%s: fields %v and %v overlap", scene.Name, a, b)
						}
					}
				}
			}
		})
	}
}

// writeTestWAV writes a short mono sine WAV the decoder can read.
func writeTestWAV(t *testing.T, path string, seconds float64) {
	t.Helper()
	const rate = 44100
	frames := int(seconds * rate)
	data := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/rate) * 12000)
		data[i*2] = byte(uint16(v))
		data[i*2+1] = byte(uint16(v) >> 8)
	}
	buf := make([]byte, 0, 44+len(data))
	le := func(v uint32, n int) {
		for i := 0; i < n; i++ {
			buf = append(buf, byte(v>>(8*uint(i))))
		}
	}
	buf = append(buf, []byte("RIFF")...)
	le(uint32(36+len(data)), 4)
	buf = append(buf, []byte("WAVEfmt ")...)
	le(16, 4)
	le(1, 2)      // PCM
	le(1, 2)      // mono
	le(rate, 4)   // sample rate
	le(rate*2, 4) // byte rate
	le(2, 2)      // block align
	le(16, 2)     // bits
	buf = append(buf, []byte("data")...)
	le(uint32(len(data)), 4)
	buf = append(buf, data...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write wav: %v", err)
	}
}

// TestMusicDecodeProducesPCM exercises the real playback pipeline: ffmpeg
// decodes the file and its PCM is paced through the SDL queue. The position
// only advances when the decoder actually produced audio, so this fails if the
// ffmpeg arguments or the queue wiring break.
func TestMusicDecodeProducesPCM(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns ffmpeg")
	}
	tool := "ffmpeg"
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	if _, err := os.Stat(filepath.Join("required", tool)); err != nil {
		t.Skipf("bundled ffmpeg not available: %v", err)
	}

	resetMusicState(t)
	newProbeFrame(t) // SDL video (and font) init used by the cache helpers
	if err := sdl.InitSubSystem(sdl.INIT_AUDIO); err == nil {
		defer sdl.QuitSubSystem(sdl.INIT_AUDIO)
	}
	prevConfig := appConfig
	appConfig = &Config{}
	appConfig.Variables.ToolsPath = "required"
	t.Cleanup(func() { appConfig = prevConfig })

	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	writeTestWAV(t, wav, 2)

	musicLoadPlaylist(dir, wav)
	musicPlayTrack(0)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		music.mu.Lock()
		pos := music.position
		music.mu.Unlock()
		if pos > 0.25 {
			return // PCM reached the queue
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("decoder produced no audio: position never advanced")
}

// The video settings sheet keeps its caption inside the panel, so it can no
// longer be drawn over the control bar.
func TestVideoSettingsSheetFitsCaption(t *testing.T) {
	withScreen(t, 1280, 720)
	sheet := videoSettingsSheetRect(0, 0, screenWidth, screenHeight)
	if sheet.H <= 0 || sheet.W <= 0 {
		t.Fatalf("invalid sheet rect %v", sheet)
	}
	lastRowBottom := sheet.Y + videoSettingsTopPad + int32(videoSettingsSheetRows-1)*videoSettingsRowH + (videoSettingsRowH - 6)
	if lastRowBottom > sheet.Y+sheet.H {
		t.Errorf("last settings row ends at %d, past the sheet bottom %d", lastRowBottom, sheet.Y+sheet.H)
	}
	if sheet.Y+sheet.H > screenHeight-72 {
		t.Errorf("sheet %v runs into the control bar", sheet)
	}
	// The hit test uses the same metrics, so every row centre must select its
	// own row.
	for i := 0; i < videoSettingsSheetRows; i++ {
		cy := sheet.Y + videoSettingsTopPad + int32(i)*videoSettingsRowH + videoSettingsRowH/2
		idx := int((cy - (sheet.Y + videoSettingsTopPad)) / videoSettingsRowH)
		if idx != i {
			t.Errorf("row %d centre %d maps to row %d", i, cy, idx)
		}
	}
}
