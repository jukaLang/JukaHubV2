package main

// Tests for dock & ambient mode: the auto-entry rule, the photo folder, the
// sleep-timer cycle, the screen-off state machine, the Play button, and an
// offscreen render that proves the clock, photo and buttons really reach the
// panel.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// resetAmbientState tears the screen down between tests so a leaked texture or
// an active flag cannot affect the next one.
func resetAmbientState(t *testing.T) {
	t.Helper()
	ambientLeave()
	ambientResetState()
	ambientFreePhoto()
	ambientPhotos = ambientPhotoState{}
	ambientButtons = nil
	ambientStatusCache.mu.Lock()
	ambientStatusCache.at = time.Time{}
	ambientStatusCache.mu.Unlock()
	t.Cleanup(func() {
		ambientLeave()
		ambientResetState()
		ambientFreePhoto()
		ambientButtons = nil
	})
}

func TestAmbientShouldEnter(t *testing.T) {
	dock := ambientDockIdleTimeout
	idle := 5 * time.Minute
	cases := []struct {
		name     string
		charging bool
		idle     time.Duration
		want     bool
	}{
		{"charging but recently used", true, 10 * time.Second, false},
		{"charging and quiet", true, dock, true},
		{"charging just before dock timeout", true, dock - time.Second, false},
		{"battery only, quiet", false, 10 * time.Second, false},
		{"battery at idle timeout", false, idle, true},
		{"battery past idle timeout", false, idle + time.Minute, true},
	}
	for _, c := range cases {
		if got := ambientShouldEnter(c.charging, c.idle, dock, idle); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestAmbientShouldEnterIgnoresTimeoutOrdering(t *testing.T) {
	// A user who sets a very short idle timeout still gets the dock screen, and
	// a zero timeout must not mean "enter immediately on battery".
	if !ambientShouldEnter(false, 90*time.Second, ambientDockIdleTimeout, 60*time.Second) {
		t.Error("idle timeout shorter than dock timeout should still enter on battery")
	}
	if ambientShouldEnter(false, time.Hour, ambientDockIdleTimeout, 0) {
		t.Error("zero idle timeout must disable the battery path, not make it immediate")
	}
	if !ambientShouldEnter(true, ambientDockIdleTimeout, ambientDockIdleTimeout, 0) {
		t.Error("charging should still enter with a zero idle timeout")
	}
}

// clearArmedSceneFade finishes a scene fade the way the render loop would.
// The fade machine is frame state: in the app the next frame completes it, and
// the main loop refuses new scene changes while one is armed. A UI test that
// switches scenes has no loop, so a fade it left armed says nothing about the
// panels this audit is for — it only says the suite has no frames. Healing it
// here keeps the audit about surfaces the user can actually be looking at.
func clearArmedSceneFade() {
	transitionPhase, pendingSceneIndex = "none", -1
}

func TestAmbientAutoTickEntersWhenIdle(t *testing.T) {
	resetAmbientState(t)
	clearArmedSceneFade()
	prevTimeout := screensaverIdleTimeout
	screensaverIdleTimeout = time.Minute
	ambientLastAutoCheck = time.Time{}
	t.Cleanup(func() {
		screensaverIdleTimeout = prevTimeout
		ambientLastAutoCheck = time.Time{}
	})
	config := &Config{}
	if reason := ambientEntryBlockReason(config); reason != "" {
		t.Fatalf("an earlier test left the screen owned by %s", reason)
	}

	// Recently used: no dock screen yet.
	noteUserInputTime(time.Now())
	ambientAutoTick(config, time.Now())
	if ambientIsActive() {
		t.Fatal("entered while the device was just touched")
	}

	// Quiet for longer than the idle timeout: the dock screen appears.
	noteUserInputTime(time.Now().Add(-90 * time.Second))
	ambientLastAutoCheck = time.Time{}
	ambientAutoTick(config, time.Now())
	if !ambientIsActive() {
		t.Fatal("did not enter after the idle timeout")
	}
	ambientLeave()
}

func TestAmbientAutoTickRespectsSetting(t *testing.T) {
	resetAmbientState(t)
	clearArmedSceneFade()
	prevTimeout := screensaverIdleTimeout
	screensaverIdleTimeout = time.Minute
	ambientLastAutoCheck = time.Time{}
	t.Cleanup(func() {
		screensaverIdleTimeout = prevTimeout
		ambientLastAutoCheck = time.Time{}
	})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientMode": false}
	if reason := ambientEntryBlockReason(config); reason != "" {
		t.Fatalf("an earlier test left the screen owned by %s", reason)
	}
	noteUserInputTime(time.Now().Add(-90 * time.Second))
	ambientAutoTick(config, time.Now())
	if ambientIsActive() {
		t.Fatal("ambientMode=false still entered the dock screen")
	}
}

func TestAmbientModeEnabled(t *testing.T) {
	if !ambientModeEnabled(nil) {
		t.Error("nil config should default to enabled")
	}
	empty := &Config{}
	empty.Variables.Custom = map[string]interface{}{}
	if !ambientModeEnabled(empty) {
		t.Error("missing key should default to enabled")
	}
	for _, v := range []interface{}{false, "false", "0", "off", "no", float64(0)} {
		c := &Config{}
		c.Variables.Custom = map[string]interface{}{"ambientMode": v}
		if ambientModeEnabled(c) {
			t.Errorf("%v should disable ambient mode", v)
		}
	}
	for _, v := range []interface{}{true, "true", "on", float64(1)} {
		c := &Config{}
		c.Variables.Custom = map[string]interface{}{"ambientMode": v}
		if !ambientModeEnabled(c) {
			t.Errorf("%v should enable ambient mode", v)
		}
	}
}

func TestAmbientPhotoFiles(t *testing.T) {
	resetAmbientState(t)
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.JPG")
	write("a.png")
	write("notes.txt")
	write(".hidden.jpg")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A subdirectory with an image extension must still be skipped.
	if err := os.Mkdir(filepath.Join(dir, "folder.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := ambientPhotoFiles(dir)
	if len(got) != 2 {
		t.Fatalf("got %d files (%v), want 2", len(got), got)
	}
	if filepath.Base(got[0]) != "a.png" || filepath.Base(got[1]) != "b.JPG" {
		t.Errorf("unexpected order: %v", got)
	}
	if files := ambientPhotoFiles(dir + "/missing"); files != nil {
		t.Errorf("missing dir should list nothing, got %v", files)
	}
	if files := ambientPhotoFiles(""); files != nil {
		t.Errorf("empty dir should list nothing, got %v", files)
	}
}

func TestAmbientPhotoDirFromConfig(t *testing.T) {
	resetAmbientState(t)
	dir := t.TempDir()
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientPhotos": dir}
	if got := ambientPhotosDir(config); got != dir {
		t.Errorf("ambientPhotos setting ignored: got %q want %q", got, dir)
	}
}

func TestAmbientSleepCycle(t *testing.T) {
	options := ambientSleepOptions
	cases := []struct {
		remaining, want time.Duration
	}{
		{0, 15 * time.Minute},
		{14 * time.Minute, 30 * time.Minute},
		{15 * time.Minute, 30 * time.Minute},
		{16 * time.Minute, 60 * time.Minute},
		{29 * time.Minute, 60 * time.Minute},
		{30 * time.Minute, 60 * time.Minute},
		{59 * time.Minute, 90 * time.Minute},
		{60 * time.Minute, 90 * time.Minute},
		{90 * time.Minute, 0},
	}
	for _, c := range cases {
		if got := ambientSleepCycle(c.remaining, options); got != c.want {
			t.Errorf("remaining %v: got %v want %v", c.remaining, got, c.want)
		}
	}
	if got := ambientSleepCycle(0, nil); got != 0 {
		t.Errorf("no options should cycle to 0, got %v", got)
	}
}

func TestAmbientSleepArmsAndCancels(t *testing.T) {
	resetAmbientState(t)
	ambientEnter(&Config{})

	if got := ambientSleepRemaining(); got != 0 {
		t.Fatalf("fresh screen should have no timer, got %v", got)
	}
	if got := ambientCycleSleep(); got != 15*time.Minute {
		t.Fatalf("first press should arm 15m, got %v", got)
	}
	if remaining := ambientSleepRemaining(); remaining <= 14*time.Minute || remaining > 15*time.Minute {
		t.Errorf("armed timer reports %v, want close to 15m", remaining)
	}
	if got := ambientCycleSleep(); got != 30*time.Minute {
		t.Errorf("second press should advance to 30m, got %v", got)
	}
	ambientArmSleep(0)
	if remaining := ambientSleepRemaining(); remaining != 0 {
		t.Errorf("cancel left %v armed", remaining)
	}
}

func TestAmbientScreenOffWake(t *testing.T) {
	resetAmbientState(t)
	config := &Config{}
	ambientEnter(config)

	if ambientIsScreenOff() {
		t.Fatal("entered with the screen off")
	}
	ambientDarken()
	if !ambientIsScreenOff() {
		t.Fatal("darken did not blank the screen")
	}
	if !ambientNeedsBlackFrame() {
		t.Fatal("darken must owe the panel a black frame")
	}
	ambientMarkBlackFrameDrawn()
	if ambientNeedsBlackFrame() {
		t.Fatal("black frame still owed after it was drawn")
	}

	// The first key only wakes; it must not also leave the screen.
	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_b}}
	if !ambientHandleKey(key, config) {
		t.Fatal("ambient did not consume the waking key")
	}
	if ambientIsScreenOff() {
		t.Fatal("key did not wake the screen")
	}
	if !ambientIsActive() {
		t.Fatal("waking key should not leave ambient mode")
	}

	// The next B leaves.
	if !ambientHandleKey(key, config) {
		t.Fatal("ambient did not consume the exit key")
	}
	if ambientIsActive() {
		t.Fatal("B did not leave ambient mode")
	}
}

func TestAmbientHandlesInactiveInput(t *testing.T) {
	resetAmbientState(t)
	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_b}}
	if ambientHandleKey(key, &Config{}) {
		t.Error("inactive ambient must not consume keys")
	}
	if ambientHandleClick(10, 10) {
		t.Error("inactive ambient must not consume clicks")
	}
	if ambientHandleController(&sdl.ControllerButtonEvent{}) {
		t.Error("inactive ambient must not consume controller input")
	}
}

func TestAmbientProgressAndTime(t *testing.T) {
	cases := []struct{ pos, dur, want float64 }{
		{0, 0, 0},
		{0, 100, 0},
		{50, 100, 0.5},
		{150, 100, 1},
		{-10, 100, 0},
	}
	for _, c := range cases {
		if got := ambientProgressFraction(c.pos, c.dur); got != c.want {
			t.Errorf("fraction(%v,%v) = %v want %v", c.pos, c.dur, got, c.want)
		}
	}
	if got := ambientTimeText(3725); got != "1:02:05" {
		t.Errorf("time text = %q", got)
	}
	if got := ambientTimeText(-4); got != "0:00" {
		t.Errorf("negative time = %q", got)
	}
	if got := ambientDurationText(90 * time.Second); got != "2m" {
		t.Errorf("duration text = %q", got)
	}
}

func TestFormatAmbientTemps(t *testing.T) {
	day := WeatherDay{TMax: 20, TMin: 10}
	if got := formatAmbientTemps(day, "C"); got != "20° / 10°C" {
		t.Errorf("celsius = %q", got)
	}
	if got := formatAmbientTemps(day, "F"); got != "68° / 50°F" {
		t.Errorf("fahrenheit = %q", got)
	}
}

func TestAmbientWeatherOff(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientWeather": false}
	if desc, temps := ambientWeather(config); desc != "" || temps != "" {
		t.Errorf("weather off should render nothing, got %q %q", desc, temps)
	}
}

func TestAmbientWeatherDefaultsOn(t *testing.T) {
	// The header's weather chip can be off while the dock still shows the
	// forecast; only ambientWeather=false hides it here.
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	wxMutex.Lock()
	prevDaily, prevReady := weatherDaily, weatherReady
	weatherDaily = []WeatherDay{{Date: time.Now().Format("2006-01-02"), TMax: 20, TMin: 10, Code: 0}}
	weatherReady = true
	wxMutex.Unlock()
	t.Cleanup(func() {
		wxMutex.Lock()
		weatherDaily, weatherReady = prevDaily, prevReady
		wxMutex.Unlock()
	})
	desc, temps := ambientWeather(config)
	if desc != "Clear" || !strings.Contains(temps, "20") {
		t.Errorf("dock weather = %q %q, want today's forecast", desc, temps)
	}
	// A clear sky gets the sun by day and the moon at night; anything else a
	// cloud.
	noon := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	midnight := time.Date(2026, 11, 8, 23, 30, 0, 0, time.Local)
	wxMutex.Lock()
	weatherDaily = []WeatherDay{{Date: noon.Format("2006-01-02"), TMax: 20, TMin: 10, Code: 0}}
	wxMutex.Unlock()
	if got := saverDockWeatherIcon(noon); got != "sun" {
		t.Errorf("clear day icon = %q, want sun", got)
	}
	if got := saverDockWeatherIcon(midnight); got != "moon" {
		t.Errorf("clear night icon = %q, want moon", got)
	}
	wxMutex.Lock()
	weatherDaily = []WeatherDay{{Date: noon.Format("2006-01-02"), TMax: 20, TMin: 10, Code: 63}}
	wxMutex.Unlock()
	if got := saverDockWeatherIcon(noon); got != "cloud" {
		t.Errorf("rain icon = %q, want cloud", got)
	}
}

// ──────────────────────────────────────────────────────────────────────
// The Play button
// ──────────────────────────────────────────────────────────────────────

// saverStubMusic replaces the playlist and the track starter for one test, so
// no decoder process is ever spawned, and reports which track was started.
func saverStubMusic(t *testing.T) *[]int {
	t.Helper()
	music.mu.Lock()
	prevTracks, prevCurrent, prevCursor := music.tracks, music.current, music.cursor
	prevPlaying, prevPaused, prevDir := music.playing, music.paused, music.dir
	music.tracks, music.current, music.cursor = nil, -1, 0
	music.playing, music.paused, music.dir = false, false, ""
	music.mu.Unlock()
	prevHook := musicPlayTrackHook
	started := &[]int{}
	musicPlayTrackHook = func(i int) { *started = append(*started, i) }
	prevConfig := appConfig
	t.Cleanup(func() {
		musicPlayTrackHook = prevHook
		appConfig = prevConfig
		music.mu.Lock()
		music.tracks, music.current, music.cursor = prevTracks, prevCurrent, prevCursor
		music.playing, music.paused, music.dir = prevPlaying, prevPaused, prevDir
		music.mu.Unlock()
	})
	return started
}

// The dock's Play button used to start only the Files root's own audio files,
// and silently did nothing on a device whose music lives one folder down — the
// "Play doesn't work" report. It now finds the music folder.
func TestAmbientPlayFindsTheMusicFolder(t *testing.T) {
	started := saverStubMusic(t)
	root := t.TempDir()
	album := filepath.Join(root, "Music", "Album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"02 two.mp3", "01 one.mp3", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(album, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := &Config{}
	config.Variables.FileExplorerRoot = root
	appConfig = config

	if got := saverFindMusicDir(config); got != album {
		t.Fatalf("music folder = %q, want the album folder %q", got, album)
	}
	ambientTogglePlayback()
	if len(*started) != 1 || (*started)[0] != 0 {
		t.Fatalf("Play started %v, want the first track", *started)
	}
	music.mu.Lock()
	n := len(music.tracks)
	music.mu.Unlock()
	if n != 2 {
		t.Fatalf("playlist has %d tracks, want the two audio files", n)
	}
}

// With a playlist loaded but stopped, Play starts the highlighted track rather
// than reloading a folder from the top.
func TestAmbientPlayResumesTheLoadedList(t *testing.T) {
	started := saverStubMusic(t)
	music.mu.Lock()
	music.tracks = []musicTrack{{Path: "/m/a.mp3", Name: "a.mp3"}, {Path: "/m/b.mp3", Name: "b.mp3"}}
	music.cursor = 1
	music.mu.Unlock()
	ambientTogglePlayback()
	if len(*started) != 1 || (*started)[0] != 1 {
		t.Fatalf("Play started %v, want the highlighted track 1", *started)
	}

	// A stopped current track is played again.
	*started = (*started)[:0]
	music.mu.Lock()
	music.current = 0
	music.mu.Unlock()
	ambientTogglePlayback()
	if len(*started) != 1 || (*started)[0] != 0 {
		t.Fatalf("Play started %v, want the current track 0", *started)
	}
}

// Nothing to play anywhere is an answer, not a crash or a silent no-op.
func TestAmbientPlayWithNoMusicSaysSo(t *testing.T) {
	started := saverStubMusic(t)
	config := &Config{}
	config.Variables.FileExplorerRoot = t.TempDir()
	appConfig = config
	// The home folder's Music is a candidate too; point it at an empty one.
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	t.Setenv("USERPROFILE", empty)
	if _, err := os.Stat("/mnt/SDCARD/Music"); err == nil {
		t.Skip("this machine has a real SD-card music folder")
	}
	if got := saverFindMusicDir(config); got != "" {
		t.Fatalf("found music in %q on an empty card", got)
	}
	ambientTogglePlayback()
	if len(*started) != 0 {
		t.Fatalf("Play started %v with no music anywhere", *started)
	}
}

// Every route — pad, keyboard, mouse — reaches the same dock action.
func TestAmbientButtonsAnswerEveryInput(t *testing.T) {
	resetAmbientState(t)
	started := saverStubMusic(t)
	music.mu.Lock()
	music.tracks = []musicTrack{{Path: "/m/a.mp3", Name: "a.mp3"}, {Path: "/m/b.mp3", Name: "b.mp3"}}
	music.mu.Unlock()
	config := &Config{}
	ambientEnter(config)

	if !ambientHandleController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}) {
		t.Fatal("the dock did not take A")
	}
	if len(*started) != 1 {
		t.Fatalf("pad A started %v, want one track", *started)
	}
	if saverDockFlash.action != ambientActionPlayPause || saverDockFlash.at.IsZero() {
		t.Error("pad A did not show the Play button pressed")
	}

	*started = (*started)[:0]
	if !ambientHandleKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}}, config) {
		t.Fatal("the dock did not take Enter")
	}
	if len(*started) != 1 {
		t.Fatalf("Enter started %v, want one track", *started)
	}

	// A tap on the Play button's rect is the same press.
	*started = (*started)[:0]
	ambientButtons = []ambientButton{{action: ambientActionPlayPause, rect: sdl.Rect{X: 100, Y: 600, W: 120, H: 46}}}
	if !ambientHandleClick(130, 620) {
		t.Fatal("the dock did not take the tap")
	}
	if len(*started) != 1 {
		t.Fatalf("a tap on Play started %v, want one track", *started)
	}

	// And B, from the pad, leaves.
	ambientHandleController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B})
	if ambientIsActive() {
		t.Fatal("B did not leave the dock")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Offscreen render
// ──────────────────────────────────────────────────────────────────────

func writeAmbientPhoto(t *testing.T, dir, name string, c color.RGBA) string {
	t.Helper()
	file, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

// ambientProbeRenderer creates one offscreen renderer for a whole test, so the
// textures the dock uploads on one frame are still its own on the next.
func ambientProbeRenderer(t *testing.T) (*sdl.Renderer, *probeFrame) {
	t.Helper()
	f := newProbeFrame(t)
	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		t.Skipf("SDL_image unavailable: %v", err)
	}
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	// Cleanups run last-registered-first: drop textures created for this probe
	// before its renderer is destroyed, so no later test sees a stale handle.
	t.Cleanup(func() { destroyTestRenderer(renderer) })
	t.Cleanup(func() {
		ambientFreePhoto()
		releaseScreenlockTexture()
	})
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })
	return renderer, f
}

// renderAmbientOn draws one dock frame on renderer and captures it into f.
func renderAmbientOn(t *testing.T, renderer *sdl.Renderer, f *probeFrame, config *Config) *probeFrame {
	t.Helper()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderAmbient(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f
}

// TestRenderAmbientFrame draws real frames with a photo behind them and checks
// the three things a broken screen would lose: the clock text, the action
// buttons, and the slideshow (including skipping a corrupt file).
func TestRenderAmbientFrame(t *testing.T) {
	resetAmbientState(t)
	// Initialise SDL video and SDL_ttf before asking for a font; getCachedFont
	// returns nil otherwise, which would look like a missing font file.
	newProbeFrame(t)
	if font, _ := getCachedFont(&Config{}, "big"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	saverPinWifiForTest(t)
	saverPicReset()
	t.Cleanup(saverPicReset)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a-broken.png"), []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAmbientPhoto(t, dir, "b-dock.png", color.RGBA{R: 20, G: 40, B: 220, A: 255})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientPhotos": dir}

	ambientEnter(config)
	ambientRefreshPhotos(config, time.Now())
	if len(ambientPhotos.files) != 2 {
		t.Fatalf("slideshow found %d photos, want 2", len(ambientPhotos.files))
	}

	renderer, f := ambientProbeRenderer(t)
	// Photos are decoded off the render thread: draw frames until the good
	// photo is up. The corrupt first file must be skipped on the way.
	deadline := time.Now().Add(10 * time.Second)
	for filepath.Base(ambientPhotos.texturePath) != "b-dock.png" && time.Now().Before(deadline) {
		renderAmbientOn(t, renderer, f, config)
		time.Sleep(10 * time.Millisecond)
	}
	if filepath.Base(ambientPhotos.texturePath) != "b-dock.png" {
		t.Fatalf("did not fall through the corrupt photo: %q", ambientPhotos.texturePath)
	}
	if !ambientPhotos.failed[filepath.Join(dir, "a-broken.png")] {
		t.Error("the corrupt photo was not remembered as broken")
	}
	// Skip the cross-fade so this frame is fully opaque.
	ambientPhotos.changedAt = time.Now().Add(-2 * ambientPhotoFade)
	renderAmbientOn(t, renderer, f, config)

	// The clock lives in the top-left. Its antialiased glyphs must produce
	// bright pixels there; a blank or crashed frame would not.
	bright := 0
	for y := int32(20); y < 190; y++ {
		for x := int32(56); x < 420; x++ {
			c := f.at(x, y)
			if c.R > 200 && c.G > 200 && c.B > 200 {
				bright++
			}
		}
	}
	if bright < 200 {
		t.Errorf("clock area has only %d bright pixels, want the time text drawn", bright)
	}

	// The blue photo must dominate the scrim away from the cards.
	if c := f.at(probeW/2, probeH/3); c.B <= c.R+40 || c.B <= c.G+20 {
		t.Errorf("photo not drawn: pixel %+v", c)
	}

	if len(ambientButtons) != 6 {
		t.Fatalf("got %d buttons, want 6", len(ambientButtons))
	}
	seen := map[ambientAction]bool{}
	for _, b := range ambientButtons {
		if b.rect.W < 44 || b.rect.H < 30 {
			t.Errorf("button %v has a %dx%d hit rect, too small to tap", b.action, b.rect.W, b.rect.H)
		}
		if b.rect.X < 0 || b.rect.X+b.rect.W > probeW || b.rect.Y+b.rect.H > probeH {
			t.Errorf("button %v at %+v is off screen", b.action, b.rect)
		}
		seen[b.action] = true
	}
	for _, a := range []ambientAction{ambientActionPlayPause, ambientActionPrev, ambientActionNext, ambientActionSleep, ambientActionScreenOff, ambientActionExit} {
		if !seen[a] {
			t.Errorf("no button for action %v", a)
		}
	}
	for i := 1; i < len(ambientButtons); i++ {
		a, b := ambientButtons[i-1].rect, ambientButtons[i].rect
		if a.X+a.W > b.X {
			t.Errorf("buttons %d and %d overlap: %+v %+v", i-1, i, a, b)
		}
	}
}

// The lock-screen picture chosen in Files is the dock's backdrop too: the dock
// is the default idle screen, and it used to ignore the setting, which is why
// "set as lock screen" seemed to do nothing.
func TestAmbientDrawsTheLockScreenPicture(t *testing.T) {
	resetAmbientState(t)
	resetScreenlockCache(t)
	newProbeFrame(t)
	saverPinWifiForTest(t)
	saverPicReset()
	t.Cleanup(saverPicReset)

	picture := writeTestPNG(t, "lock.png", color.RGBA{R: 230, G: 30, B: 30, A: 255})
	config := screenlockTestConfig("")
	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	ambientEnter(config)
	renderer, f := ambientProbeRenderer(t)
	deadline := time.Now().Add(10 * time.Second)
	for ssImage.texture == nil && time.Now().Before(deadline) {
		renderAmbientOn(t, renderer, f, config)
		time.Sleep(10 * time.Millisecond)
	}
	if ssImage.texture == nil {
		t.Fatal("the dock never drew the lock-screen picture")
	}
	ssImage.shownAt = time.Time{} // skip the fade-in
	renderAmbientOn(t, renderer, f, config)
	if c := f.at(probeW/2, probeH/2); c.R <= c.B+60 || c.R <= c.G+60 {
		t.Fatalf("the middle of the dock is %+v, want the red lock picture", c)
	}
	if len(ambientPhotos.files) != 0 {
		t.Error("the slideshow was scanned although a lock picture is set")
	}
}

// The dock used to advertise its own empty states: a card reading "Nothing
// playing" with a 0:00 clock, and "Battery unknown" in the corner. Both read as
// a broken panel on a screen whose whole job is to look calm, so neither is
// drawn; the column stacks only the lines that have something to say.
func TestAmbientEmptyStatesStayOffTheDock(t *testing.T) {
	if ambientShowNowPlaying("", false, false) {
		t.Fatal("the dock would draw a now-playing card with no track")
	}
	for _, c := range []struct {
		title           string
		playing, paused bool
	}{
		{"Track", false, false},
		{"", true, false},
		{"", false, true},
	} {
		if !ambientShowNowPlaying(c.title, c.playing, c.paused) {
			t.Fatalf("the dock hid a real state: %+v", c)
		}
	}

	if lines := ambientStatusLines(0, 0, 0); len(lines) != 0 {
		t.Fatalf("an idle dock drew %d status line(s): %+v", len(lines), lines)
	}
	lines := ambientStatusLines(90*time.Second, 2, 5)
	want := []string{"Sleep in 1:30", "Photo 3/5"}
	if len(lines) != len(want) {
		t.Fatalf("column = %+v, want %d lines", lines, len(want))
	}
	for i, text := range want {
		if lines[i].text != text {
			t.Fatalf("line %d = %q, want %q", i, lines[i].text, text)
		}
	}

	// The battery moved to its chip: no reading means no chip at all.
	if got := saverBatteryLabel(-1, false); got != "" {
		t.Fatalf("unknown battery label = %q, want no chip", got)
	}
	if got := saverBatteryLabel(-1, true); got != "Charging" {
		t.Fatalf("charging without a level = %q", got)
	}
	if got := saverBatteryLabel(82, true); got != "82% · Charging" {
		t.Fatalf("charging label = %q", got)
	}
	if got := saverBatteryLabel(82, false); got != "82%" {
		t.Fatalf("battery label = %q", got)
	}
}

// The press that wakes a darkened dock (or the screensaver) only wakes it, its
// release is swallowed with it, and a release never wakes — the release of X,
// which darkens the dock, used to light the panel straight back up.
func TestWakePressIsSwallowedAndReleasesNeverWake(t *testing.T) {
	resetAmbientState(t)
	prev := wakeSwallowed
	wakeSwallowed = nil
	t.Cleanup(func() { wakeSwallowed = prev })
	config := &Config{}
	ambientEnter(config)

	// X darkens on its press...
	if !ambientHandleController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X}) {
		t.Fatal("the dock did not take X")
	}
	if !ambientIsScreenOff() {
		t.Fatal("X did not darken the dock")
	}
	// ...and its release must leave it dark.
	if wakeSwallowRelease(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_X)) {
		t.Fatal("the release of a press that did not wake was swallowed")
	}
	if !ambientIsScreenOff() {
		t.Fatal("a release woke the dock")
	}

	// The next press wakes and is consumed, not passed on as B (leave).
	if !wakeSwallowPress(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_B), false) {
		t.Fatal("the waking press was not consumed")
	}
	if ambientIsScreenOff() || !ambientIsActive() {
		t.Fatalf("after the waking press: screenOff=%v active=%v, want awake and still docked",
			ambientIsScreenOff(), ambientIsActive())
	}
	if !wakeSwallowRelease(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_B)) {
		t.Fatal("the release of the waking press was not swallowed")
	}
	// Awake now: the next press is an ordinary press.
	if wakeSwallowPress(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_B), false) {
		t.Fatal("a press on an awake screen was swallowed")
	}
	if wakeSwallowRelease(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_B)) {
		t.Fatal("an ordinary release was swallowed")
	}

	// A wake press whose release was taken by another loop must not swallow
	// that button for good: the next fresh press is judged on its own.
	ambientDarken()
	if !wakeSwallowPress(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_A), false) {
		t.Fatal("the waking A was not consumed")
	}
	if wakeSwallowPress(wakeInputPad, int32(sdl.CONTROLLER_BUTTON_A), false) {
		t.Fatal("a fresh A after a lost release was swallowed")
	}
	// Keyboard auto-repeat of the key that woke the screen stays swallowed.
	ambientDarken()
	if !wakeSwallowPress(wakeInputKey, 4, false) || !wakeSwallowPress(wakeInputKey, 4, true) {
		t.Fatal("the waking key, or its auto-repeat, was not swallowed")
	}
	wakeSwallowRelease(wakeInputKey, 4)
}

func TestAxisDeflectedIgnoresDrift(t *testing.T) {
	for _, v := range []int16{0, 900, -2500, axisWakeThreshold} {
		if axisDeflected(v) {
			t.Errorf("axisDeflected(%d) = true; drift must not count as input", v)
		}
	}
	for _, v := range []int16{axisWakeThreshold + 1, -axisWakeThreshold - 1, 32767, -32768} {
		if !axisDeflected(v) {
			t.Errorf("axisDeflected(%d) = false; a deliberate push is input", v)
		}
	}
}
