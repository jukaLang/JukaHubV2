package main

// Tests for dock & ambient mode: the auto-entry rule, the photo folder, the
// sleep-timer cycle, the screen-off state machine, and an offscreen render that
// proves the clock, photo and chips really reach the panel.

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

func TestAmbientAutoTickEntersWhenIdle(t *testing.T) {
	resetAmbientState(t)
	prevTimeout := screensaverIdleTimeout
	screensaverIdleTimeout = time.Minute
	ambientLastAutoCheck = time.Time{}
	t.Cleanup(func() {
		screensaverIdleTimeout = prevTimeout
		ambientLastAutoCheck = time.Time{}
	})
	config := &Config{}
	if ambientEntryBlocked(config) {
		t.Skip("another UI surface is open in this test binary")
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
	prevTimeout := screensaverIdleTimeout
	screensaverIdleTimeout = time.Minute
	ambientLastAutoCheck = time.Time{}
	t.Cleanup(func() {
		screensaverIdleTimeout = prevTimeout
		ambientLastAutoCheck = time.Time{}
	})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientMode": false}
	if ambientEntryBlocked(config) {
		t.Skip("another UI surface is open in this test binary")
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
	weatherDaily = []WeatherDay{{TMax: 20, TMin: 10, Code: 0}}
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

func renderAmbientProbe(t *testing.T, config *Config) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		t.Skipf("SDL_image unavailable: %v", err)
	}
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	// A previous test's scene renderer has been destroyed, so the shared
	// gradient/scrim cache may hold handles SDL has already freed. Drop the
	// references without calling Destroy on them (that would be a use-after-free
	// and corrupts later SDL allocations); renderAmbient rebuilds what it needs
	// on this renderer.
	gradientTexture = nil
	gradientSceneKey = ""
	gradientOverlayKey = sdl.Color{}
	ambientScrim.texture = nil
	ambientScrim.w, ambientScrim.h = 0, 0

	// Cleanups run last-registered-first: drop textures created for this probe
	// before its renderer is destroyed, so no later test sees a stale handle.
	t.Cleanup(func() { destroyTestRenderer(renderer) })
	t.Cleanup(func() {
		ambientFreePhoto()
		if ambientScrim.texture != nil {
			ambientScrim.texture.Destroy()
			ambientScrim.texture = nil
			ambientScrim.w, ambientScrim.h = 0, 0
		}
		if gradientTexture != nil {
			gradientTexture.Destroy()
			gradientTexture = nil
			gradientSceneKey = ""
			gradientOverlayKey = sdl.Color{}
		}
	})

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderAmbient(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

// TestRenderAmbientFrame draws one real frame with a photo behind it and
// checks the three things a broken screen would lose: the clock text, the
// action chips, and the slideshow (including skipping a corrupt file).
func TestRenderAmbientFrame(t *testing.T) {
	resetAmbientState(t)
	// Initialise SDL video and SDL_ttf before asking for a font; getCachedFont
	// returns nil otherwise, which would look like a missing font file.
	newProbeFrame(t)
	if font, _ := getCachedFont(&Config{}, "big"); font == nil {
		t.Skip("no usable font in this environment")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a-broken.png"), []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAmbientPhoto(t, dir, "b-dock.png", color.RGBA{R: 20, G: 40, B: 220, A: 255})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientPhotos": dir}

	ambientEnter(config)
	now := time.Now()
	ambientRefreshPhotos(config, now)
	if len(ambientPhotos.files) != 2 {
		t.Fatalf("slideshow found %d photos, want 2", len(ambientPhotos.files))
	}
	// Skip the fade so this frame is fully opaque.
	ambientPhotos.changedAt = now.Add(-2 * ambientPhotoFade)

	f := renderAmbientProbe(t, config)

	// The corrupt first file must be skipped for the good one.
	if filepath.Base(ambientPhotos.texturePath) != "b-dock.png" {
		t.Fatalf("did not fall through the corrupt photo: %q", ambientPhotos.texturePath)
	}

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

	// The blue photo must dominate the dark scrim away from the cards.
	if c := f.at(probeW/2, probeH/3); c.B <= c.R+40 || c.B <= c.G+20 {
		t.Errorf("photo not drawn: pixel %+v", c)
	}

	if len(ambientButtons) != 6 {
		t.Fatalf("got %d chips, want 6", len(ambientButtons))
	}
	for _, b := range ambientButtons {
		if b.rect.W <= 0 || b.rect.H <= 0 {
			t.Errorf("chip %v has an empty hit rect", b.action)
		}
		if b.rect.X < 0 || b.rect.X+b.rect.W > probeW {
			t.Errorf("chip %v at x=%d w=%d is off screen", b.action, b.rect.X, b.rect.W)
		}
	}
}
