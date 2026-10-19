package main

// Offscreen rendering and focus checks for the home screen. These run with
// SDL's dummy video driver and the software renderer and pin the behaviours
// that used to break silently: the header clock's calendar popover must draw
// above the layout, the Exit chip must sit visibly on the footer, and the
// clock must be reachable with the D-pad and clickable.

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// snapshotHomeGlobals saves the home-related globals the probe touches and
// restores them when the test ends, so later tests (which may run with a nil
// appConfig) never see this test's scene index or layout.
func snapshotHomeGlobals(t *testing.T) {
	t.Helper()
	prevIndex, prevLayout := currentSceneIndex, homeLayout
	prevCal, prevPressed := homeCalendarOpen, homePressedID
	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() {
		currentSceneIndex = prevIndex
		homeLayout = prevLayout
		homeCalendarOpen = prevCal
		homePressedID = prevPressed
		screenWidth, screenHeight = prevW, prevH
	})
}

// renderHomeFrame draws the home scene once and captures the pixels. It reuses
// the shared probe window from installbar_render_test.go so SDL is initialised
// exactly once per test binary.
func renderHomeFrame(t *testing.T, calendarOpen bool) *probeFrame {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()

	screenWidth, screenHeight = probeW, probeH
	homeLayout = nil
	homeCalendarOpen = calendarOpen
	homePressedID = ""
	// The home layout only builds when the active scene uses it, so the probe
	// config needs a home-layout scene like the real jukaconfig does.
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}}
	currentSceneIndex = 0
	renderHomeModern(renderer, config)

	f := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

func nearColor(p, c sdl.Color, tol uint8) bool {
	return absDiffU8(p.R, c.R) <= tol && absDiffU8(p.G, c.G) <= tol && absDiffU8(p.B, c.B) <= tol
}

func absDiffU8(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

// borderPixels counts the columns around row y whose pixel looks like the
// calendar popover's border colour. The 3-row band tolerates stroke rounding.
func borderPixels(f *probeFrame, y, x0, x1 int32) int {
	n := 0
	for x := x0; x <= x1; x++ {
		for dy := int32(-1); dy <= 1; dy++ {
			if nearColor(f.at(x, y+dy), ColorBorder, 10) {
				n++
				break
			}
		}
	}
	return n
}

// TestHomeCalendarPopoverRendersAboveLayout pins the "clicking the clock does
// nothing" bug: the mini calendar must render on top of the layout (and of
// the cards it overlaps), not underneath the layout backdrop.
func TestHomeCalendarPopoverRendersAboveLayout(t *testing.T) {
	snapshotHomeGlobals(t)
	open := renderHomeFrame(t, true)
	closed := renderHomeFrame(t, false)
	r := homeCalendarRect()
	if r.W <= 0 || r.H <= 0 {
		t.Fatalf("calendar popover rect is empty: %+v", r)
	}

	// The popover's top edge sits on the empty strip below the header; its
	// bottom edge overlaps the top-right cards. Both must show the panel
	// border in the open frame and never in the closed frame.
	for _, edge := range []int32{r.Y, r.Y + r.H - 1} {
		if got := borderPixels(open, edge, r.X+14, r.X+r.W-14); got < 20 {
			t.Errorf("calendar popover edge y=%d not drawn above the layout: only %d border pixels", edge, got)
		}
		if got := borderPixels(closed, edge, r.X+14, r.X+r.W-14); got > 5 {
			t.Errorf("calendar popover edge y=%d visible while closed: %d border pixels", edge, got)
		}
	}
}

// TestHomeHasNoSeparateClockButton pins the "not a separate button" request:
// the old round button spot must be plain background again.
func TestHomeHasNoSeparateClockButton(t *testing.T) {
	snapshotHomeGlobals(t)
	frame := renderHomeFrame(t, false)
	// Centre of where the round Clock button (1204,58,60x60) used to be.
	if nearColor(frame.at(1234, 88), ColorIconSurface, 6) {
		t.Error("the old round clock button is still drawn at (1234,88)")
	}
}

// TestHomeExitChipVisibleOnFooter pins the "there is no exit button" bug: the
// Exit chip used to be drawn before the footer and vanished behind its band.
func TestHomeExitChipVisibleOnFooter(t *testing.T) {
	snapshotHomeGlobals(t)
	frame := renderHomeFrame(t, false)
	// Blank area inside the Exit chip pill (rect 8,678,116x36), right of its
	// glyph and label, on top of the footer band (y 672..720).
	c := frame.at(115, 696)
	want := ColorIconSurface
	if c.R != want.R || c.G != want.G || c.B != want.B {
		t.Errorf("Exit chip not visible on the footer: pixel=(%d,%d,%d) want ColorIconSurface=(%d,%d,%d)",
			c.R, c.G, c.B, want.R, want.G, want.B)
	}
}

// TestHomeClockReachableAndTogglesCalendar pins the two keyboard/mouse
// complaints: the clock must be reachable with the D-pad, and clicking or
// activating it must toggle the calendar popover.
func TestHomeClockReachableAndTogglesCalendar(t *testing.T) {
	snapshotHomeGlobals(t)
	homeLayout = NewHomeLayout(probeW, probeH)
	homeLayout.Focus.Set(HomeFocusContinue)
	if got := homeLayout.Move(FocusRight); got != HomeFocusMedia {
		t.Fatalf("Right from Continue = %s, want media", got)
	}
	if got := homeLayout.Move(FocusUp); got != HomeFocusClock {
		t.Fatalf("Up from Media = %s, want clock (the clock must be reachable by keys)", got)
	}

	homeCalendarOpen = false
	homeActivate(&Config{})
	if !homeCalendarOpen {
		t.Error("activating the focused clock did not open the calendar")
	}
	homeActivate(&Config{})
	if homeCalendarOpen {
		t.Error("activating the clock again did not close the calendar")
	}

	// Clicking the clock text (its rect is tracked during rendering) toggles
	// the popover; a click anywhere else dismisses it.
	renderHomeFrame(t, false)
	r := homeLayout.Items[HomeFocusClock].Rect
	if r.W < 40 || r.Y+r.H > 64 {
		t.Fatalf("clock rect is not tracked in the header: %+v", r)
	}
	if !handleHomeMouseClick(r.X+r.W/2, r.Y+r.H/2, &Config{}) {
		t.Fatal("click on the clock was not handled")
	}
	if !homeCalendarOpen {
		t.Error("clicking the clock did not open the calendar")
	}
	// A click on empty space below the cards dismisses the popover only.
	if !handleHomeMouseClick(5, 640, &Config{}) {
		t.Error("outside click was not handled while the calendar is open")
	}
	if homeCalendarOpen {
		t.Error("clicking outside did not close the calendar")
	}
}

// TestHomeLayoutGeometry pins the requested card layout: Continue takes over
// the old Settings slot, Files halves its width and Settings fills the same
// width right of Files.
func TestHomeLayoutGeometry(t *testing.T) {
	snapshotHomeGlobals(t)
	homeLayout = NewHomeLayout(probeW, probeH)
	homeLayout.Resize(probeW, probeH)

	cont := homeLayout.Items[HomeFocusContinue].Rect
	if cont.H != 258 {
		t.Errorf("Continue height = %d, want 258 (its own row plus the old Settings slot)", cont.H)
	}
	files := homeLayout.Items[HomeFocusFiles].Rect
	settings := homeLayout.Items[HomeFocusSettings].Rect
	if files.W < 150 || settings.W < 150 {
		t.Errorf("Files/Settings widths = %d/%d, want two readable narrow tiles", files.W, settings.W)
	}
	if gap := settings.X - (files.X + files.W); gap < 16 {
		t.Errorf("Files and Settings are %dpx apart, want at least 16px of breathing room", gap)
	}
	if settings.X+settings.W != 1248 {
		t.Errorf("Settings right edge = %d, want 1248 (keeps the grid's right margin)", settings.X+settings.W)
	}

	// Exit must sit inside the footer band so it renders on top of it.
	exit := homeLayout.Items[HomeFocusExit].Rect
	if exit.Y < 672 || exit.Y+exit.H > 720 {
		t.Errorf("Exit chip rect %+v is not within the footer band (672..720)", exit)
	}
}

// TestNarrowHomeCardsUseStackedLayout pins the fix for the "Files and Settings
// look squished" complaint: the half-width tiles must stack their icon above
// the label instead of squeezing the text into a ~60px column, while the wider
// rows keep the icon-beside-label composition.
func TestNarrowHomeCardsUseStackedLayout(t *testing.T) {
	snapshotHomeGlobals(t)
	homeLayout = NewHomeLayout(probeW, probeH)
	homeLayout.Resize(probeW, probeH)
	iconSize := scaleX(56, probeW)
	pad := scaleX(20, probeW)

	for _, id := range []HomeFocusID{HomeFocusFiles, HomeFocusSettings} {
		r := homeLayout.Items[id].Rect
		if !homeCardStacksIcon(r, iconSize, pad, probeW) {
			t.Errorf("%s card %+v is narrow but still uses the horizontal layout", id, r)
		}
	}
	for _, id := range []HomeFocusID{
		HomeFocusMedia, HomeFocusPackages, HomeFocusApps,
		HomeFocusChat, HomeFocusTools, HomeFocusEmulator,
	} {
		r := homeLayout.Items[id].Rect
		if homeCardStacksIcon(r, iconSize, pad, probeW) {
			t.Errorf("%s card %+v should keep the icon beside its label", id, r)
		}
	}
}

// TestHomeProbeRender dumps the two home states to tmp/ for visual review.
func TestHomeProbeRender(t *testing.T) {
	snapshotHomeGlobals(t)
	for _, tc := range []struct {
		name string
		open bool
	}{
		{"home_probe_clock.png", false},
		{"home_probe_calendar.png", true},
	} {
		f := renderHomeFrame(t, tc.open)
		img := image.NewNRGBA(image.Rect(0, 0, int(probeW), int(probeH)))
		for i := 0; i < len(f.buf); i += 4 {
			img.Pix[i+0] = f.buf[i+2] // R
			img.Pix[i+1] = f.buf[i+1] // G
			img.Pix[i+2] = f.buf[i+0] // B
			img.Pix[i+3] = f.buf[i+3] // A
		}
		path := filepath.Join("..", "tmp", tc.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
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
}
