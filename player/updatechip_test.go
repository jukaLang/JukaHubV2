package main

// updatechip_test.go covers the home-header update chip: the download icon that
// sits at the left end of the status cluster (immediately left of the weather
// text) whenever a newer release is advertised. It has to be reachable with the
// D-pad, pressable with A and clickable, and each of those has to start the
// install without opening any menu.

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// homeUpdateProbe renders the real home layout at the probe size with a full
// handheld-style status cluster and returns the layout.
func homeUpdateProbe(t *testing.T) *HomeLayout {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	companion.qr = make([][]bool, 21)
	for y := range companion.qr {
		companion.qr[y] = make([]bool, 21)
		for x := range companion.qr[y] {
			companion.qr[y][x] = (x*2+y)%3 == 0
		}
	}

	layout := NewHomeLayout(probeW, probeH)
	data := HomeViewData{
		Greeting:    "Good afternoon",
		Subtitle:    "What do you want to do?",
		Version:     "v0.4.0",
		StatusParts: []string{"192.168.1.42", "12C / 24C", "12:40", "87%"},
	}
	if err := layout.Render(renderer, loadHomeFonts(), data); err != nil {
		t.Fatalf("render: %v", err)
	}
	return layout
}

func TestHomeUpdateChipSitsBesideWeather(t *testing.T) {
	snapshotHomeGlobals(t)
	prevChip := homeUpdateRect
	t.Cleanup(func() { homeUpdateRect = prevChip })
	withAvailableUpdate(t, "JukaHub-20261019120000")

	layout := homeUpdateProbe(t)
	if homeUpdateRect.W == 0 {
		t.Fatal("no download chip was drawn while a release is offered")
	}
	weather := layout.Items[HomeFocusWeather].Rect
	if weather.W == 0 {
		t.Fatal("probe header has no weather chip")
	}
	if gap := weather.X - (homeUpdateRect.X + homeUpdateRect.W); gap < 0 || gap > 12 {
		t.Fatalf("chip %+v is not immediately left of the weather text %+v (gap %d)", homeUpdateRect, weather, gap)
	}
	if homeUpdateRect.Y < 0 || homeUpdateRect.Y+homeUpdateRect.H > scaleY(56, layout.Height) {
		t.Fatalf("chip %+v escapes the header band", homeUpdateRect)
	}
	if !layout.Focus.IsEnabled(HomeFocusUpdate) {
		t.Fatal("the chip is drawn but its focus entry is disabled")
	}
	if got := layout.Items[HomeFocusUpdate].Rect; got != homeUpdateRect {
		t.Fatalf("focus rect %+v is not the drawn chip %+v", got, homeUpdateRect)
	}
}

func TestHomeUpdateChipHiddenWithoutRelease(t *testing.T) {
	snapshotHomeGlobals(t)
	withAvailableUpdate(t, "")

	layout := homeUpdateProbe(t)
	if homeUpdateRect.W != 0 {
		t.Fatalf("chip was drawn without a release: %+v", homeUpdateRect)
	}
	if layout.Focus.IsEnabled(HomeFocusUpdate) {
		t.Fatal("the chip is off screen but still focusable")
	}

	// The old weather path must survive: with the chip disabled, Left from the
	// weather text falls through to Media.
	homeLayout = layout
	homeLayoutActive = true
	if !layout.Focus.Set(HomeFocusClock) {
		t.Fatal("cannot focus the clock")
	}
	homeKey(t, &Config{}, sdl.K_LEFT)
	if got := layout.Focus.Current(); got != HomeFocusWeather {
		t.Fatalf("Left from the clock focused %q, want weather", got)
	}
	homeKey(t, &Config{}, sdl.K_LEFT)
	if got := layout.Focus.Current(); got != HomeFocusMedia {
		t.Fatalf("Left from weather without a release focused %q, want media", got)
	}
}

// TestHomeUpdateChipStaysInsideANarrowHeader keeps the offer from pushing the
// status cluster into the greeting on the smallest handheld canvas: the chip is
// dropped when it would not fit, and its focus entry follows it.
func TestHomeUpdateChipStaysInsideANarrowHeader(t *testing.T) {
	snapshotHomeGlobals(t)
	prevChip := homeUpdateRect
	t.Cleanup(func() { homeUpdateRect = prevChip })
	withAvailableUpdate(t, "JukaHub-20261019120000")

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	const w, h = 800, 480
	layout := NewHomeLayout(w, h)
	data := HomeViewData{
		Greeting:    "Good afternoon",
		Subtitle:    "What do you want to do?",
		StatusParts: []string{"192.168.1.42", "12C / 24C", "12:40", "87%"},
	}
	if err := layout.Render(renderer, loadHomeFonts(), data); err != nil {
		t.Fatalf("render: %v", err)
	}

	drawn := homeUpdateRect.W > 0
	if drawn {
		if homeUpdateRect.X < scaleX(480, w) || homeUpdateRect.X+homeUpdateRect.W > w {
			t.Fatalf("chip %+v crowds the greeting or leaves the panel at %dx%d", homeUpdateRect, w, h)
		}
	}
	if got := layout.Focus.IsEnabled(HomeFocusUpdate); got != drawn {
		t.Fatalf("chip focus enabled = %v, drawn = %v", got, drawn)
	}
}

func TestHomeUpdateChipSelectableWithArrows(t *testing.T) {
	useHomeAsCurrent(t)
	withAvailableUpdate(t, "JukaHub-20261019120000")

	if !homeLayout.Focus.Set(HomeFocusClock) {
		t.Fatal("cannot focus the clock")
	}
	homeKey(t, &Config{}, sdl.K_LEFT)
	if got := homeLayout.Focus.Current(); got != HomeFocusWeather {
		t.Fatalf("Left from the clock focused %q, want weather", got)
	}
	// The Wi-Fi meter sits between the weather text and the update offer, so
	// Left crosses it before reaching the chip.
	homeKey(t, &Config{}, sdl.K_LEFT)
	if got := homeLayout.Focus.Current(); got != HomeFocusWiFi {
		t.Fatalf("Left from weather focused %q, want the Wi-Fi meter", got)
	}
	homeKey(t, &Config{}, sdl.K_LEFT)
	if got := homeLayout.Focus.Current(); got != HomeFocusUpdate {
		t.Fatalf("Left from the Wi-Fi meter focused %q, want the update chip", got)
	}
	// Down must not strand the user in the header.
	homeKey(t, &Config{}, sdl.K_DOWN)
	if got := homeLayout.Focus.Current(); got != HomeFocusSettings {
		t.Fatalf("Down from the update chip focused %q, want settings", got)
	}
	if !homeLayout.Focus.Set(HomeFocusUpdate) {
		t.Fatal("cannot focus the update chip")
	}
	homeKey(t, &Config{}, sdl.K_RIGHT)
	if got := homeLayout.Focus.Current(); got != HomeFocusWiFi {
		t.Fatalf("Right from the update chip focused %q, want the Wi-Fi meter", got)
	}
}

// waitForInstall waits for the stubbed install runner to be called.
func waitForInstall(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pressing the update chip did not start the install")
	}
}

// waitPatchIdle blocks until the background update op has released the Patch
// busy flag, so a test can press again.
func waitPatchIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tryBeginPatchOp() {
			endPatchOp()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the update operation never finished")
}

// TestHomeUpdateChipClearsAStaleOffer: if the release turns out not to be newer
// than the running build, the chip must disappear instead of failing forever.
func TestHomeUpdateChipClearsAStaleOffer(t *testing.T) {
	snapshotHomeGlobals(t)
	withAvailableUpdate(t, "JukaHub-20261019120000")
	clearToastAfterTest(t)

	prevRunner := updateInstallRunner
	t.Cleanup(func() {
		updateInstallRunner = prevRunner
		endPatchOp()
		clearPatchBusy()
	})
	done := make(chan struct{}, 1)
	updateInstallRunner = func(ctx context.Context, cfg *Config) error {
		done <- struct{}{}
		return errAlreadyUpToDate
	}

	homeLayout = NewHomeLayout(1280, 720)
	homeLayoutActive = true
	if !homeLayout.Focus.Set(HomeFocusUpdate) {
		t.Fatal("cannot focus the update chip")
	}
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	handleHomeController(a, &Config{})
	waitForInstall(t, done)
	waitPatchIdle(t)

	deadline := time.Now().Add(2 * time.Second)
	for updateAvailable() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if updateAvailable() {
		t.Fatal("a stale offer was not cleared after the install reported up to date")
	}
	// The success toast is raised on the same goroutine a moment later; wait for
	// it so clearToastAfterTest's cleanup runs after it exists.
	for !strings.Contains(currentToast.Message, "already up to date") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// homeUpdateShot renders the real home scene once, optionally with the update
// chip focused, and returns its pixels.
func homeUpdateShot(t *testing.T, focused bool) *probeFrame {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	screenWidth, screenHeight = probeW, probeH
	homeLayout = nil
	homeCalendarOpen = false
	homePressedID = ""
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
	currentSceneIndex = 0
	renderHomeModern(renderer, config)
	if focused {
		if homeLayout == nil || !homeLayout.Focus.Set(HomeFocusUpdate) {
			t.Fatalf("cannot focus the update chip for the focused shot")
		}
		renderHomeModern(renderer, config)
	}

	f := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

func writeProbePNG(t *testing.T, name string, f *probeFrame) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, int(probeW), int(probeH)))
	for i := 0; i < len(f.buf); i += 4 {
		img.Pix[i+0] = f.buf[i+2]
		img.Pix[i+1] = f.buf[i+1]
		img.Pix[i+2] = f.buf[i+0]
		img.Pix[i+3] = f.buf[i+3]
	}
	path := filepath.Join("..", "tmp", name)
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(out, img); err != nil {
		out.Close()
		t.Fatal(err)
	}
	out.Close()
}

// TestHomeUpdateChipProbeRender dumps the offer to tmp/ for visual review,
// both on the real home scene and in the full status cluster a handheld shows.
func TestHomeUpdateChipProbeRender(t *testing.T) {
	snapshotHomeGlobals(t)
	withAvailableUpdate(t, "JukaHub-20261019120000")

	writeProbePNG(t, "home_update_offer.png", homeUpdateShot(t, false))
	writeProbePNG(t, "home_update_focus.png", homeUpdateShot(t, true))

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	layout := NewHomeLayout(probeW, probeH)
	data := HomeViewData{
		Greeting:    "Good afternoon",
		Subtitle:    "What do you want to do?",
		Version:     "v0.4.0",
		StatusParts: []string{"192.168.1.42", "12C / 24C", "12:40", "87%"},
	}
	if err := layout.Render(renderer, loadHomeFonts(), data); err != nil {
		t.Fatalf("render: %v", err)
	}
	f := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	writeProbePNG(t, "home_update_weather.png", f)
}

func TestHomeUpdateChipInstallsOnPressAndClick(t *testing.T) {
	snapshotHomeGlobals(t)
	withAvailableUpdate(t, "JukaHub-20261019120000")
	clearToastAfterTest(t)

	prevRunner := updateInstallRunner
	t.Cleanup(func() {
		updateInstallRunner = prevRunner
		endPatchOp()
		clearPatchBusy()
	})
	started := make(chan struct{}, 2)
	updateInstallRunner = func(ctx context.Context, cfg *Config) error {
		started <- struct{}{}
		return nil
	}

	homeLayout = NewHomeLayout(1280, 720)
	homeLayoutActive = true
	homeCalendarOpen = false
	dismissWeatherPopover()
	companionHeaderRect = sdl.Rect{}
	ps5TileRect = sdl.Rect{}

	// A on the focused chip.
	if !homeLayout.Focus.Set(HomeFocusUpdate) {
		t.Fatal("cannot focus the update chip")
	}
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	if !handleHomeController(a, &Config{}) {
		t.Fatal("A was not handled on home")
	}
	waitForInstall(t, started)
	waitPatchIdle(t)

	// A click on the drawn chip does the same and is consumed.
	homeUpdateRect = sdl.Rect{X: 900, Y: 14, W: 30, H: 24}
	item := homeLayout.Items[HomeFocusUpdate]
	item.Rect = homeUpdateRect
	homeLayout.Items[HomeFocusUpdate] = item
	if !handleHomeMouseClick(915, 20, &Config{}) {
		t.Fatal("a click on the update chip was not consumed")
	}
	waitForInstall(t, started)
	waitPatchIdle(t)

	// Focus follows the click, so the D-pad continues from the chip.
	if got := homeLayout.Focus.Current(); got != HomeFocusUpdate {
		t.Fatalf("click focused %q, want the update chip", got)
	}
}
