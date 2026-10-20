package main

import (
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// clearToastAfterTest drops any toast a launcher press raised. A toast lives in
// a package global and paints over the bottom of the screen for a few seconds,
// so without this a press here would leak into a later render test's probe.
func clearToastAfterTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { currentToast = ToastState{} })
}

func TestPS5TileRectSitsLeftOfQR(t *testing.T) {
	qr := sdl.Rect{X: 1000, Y: 58, W: 104, H: 104}
	got, ok := ps5TileRectFor(qr, 12, 560)
	if !ok {
		t.Fatal("tile should fit left of the QR")
	}
	if got.X+got.W > qr.X {
		t.Fatalf("tile %+v overlaps the QR %+v", got, qr)
	}
	if got.X != qr.X-12-qr.W {
		t.Fatalf("tile x = %d, want %d", got.X, qr.X-12-qr.W)
	}
	if got.Y != qr.Y || got.W != qr.W || got.H != qr.H {
		t.Fatalf("tile should share the QR's band and size: %+v vs %+v", got, qr)
	}

	// A QR that has slid left leaves no room; the tile must disappear rather
	// than crowd the brand or the greeting column.
	if _, ok := ps5TileRectFor(sdl.Rect{X: 600, Y: 58, W: 104, H: 104}, 12, 560); ok {
		t.Fatal("tile was placed too close to the left edge")
	}
	if _, ok := ps5TileRectFor(sdl.Rect{}, 12, 560); ok {
		t.Fatal("tile was placed without a QR rect")
	}
	// A negative gap must never be allowed to overlap.
	if _, ok := ps5TileRectFor(sdl.Rect{X: 1000, Y: 58, W: 104, H: 104}, -40, 0); ok {
		t.Fatal("a negative gap produced an overlapping tile")
	}
}

func TestPS5TileStateFollowsTheInstall(t *testing.T) {
	prev := ps5LaunchPending.Load()
	t.Cleanup(func() {
		setChiakiInstalling(false)
		ps5LaunchPending.Store(prev)
	})

	root := withChiakiSDRoot(t)
	if got := ps5TileCurrentState(); got != ps5TileInstall {
		t.Fatalf("state without a build = %v, want install", got)
	}

	app := filepath.Join(root, "Apps", "Chiaki-PS5")
	writeChiakiTestFile(t, filepath.Join(app, "launch.sh"), "#!/bin/sh\n")
	writeChiakiTestFile(t, filepath.Join(app, "app.py"), "print('ps5')\n")
	if got := ps5TileCurrentState(); got != ps5TileLaunch {
		t.Fatalf("state with a build = %v, want launch", got)
	}

	setChiakiInstalling(true)
	if got := ps5TileCurrentState(); got != ps5TileBusy {
		t.Fatalf("state while installing = %v, want busy", got)
	}
}

// A launcher press that cannot start a download must not leave a launch armed
// for whatever install happens later.
func TestPS5TilePressClearsLaunchWhenInstallCannotStart(t *testing.T) {
	clearToastAfterTest(t)
	t.Cleanup(func() { chiakiPS5LaunchAfterInstall.Store(false) })
	// A desktop build has no TrimUI package support, so the install refuses.
	ps5TileActivate()
	if chiakiPS5LaunchAfterInstall.Load() {
		t.Fatal("a refused install left the launch flag set")
	}
	if ps5LaunchPending.Load() {
		t.Fatal("a refused install queued a launch")
	}
}

func TestPS5InstallSuccessHandsOverToTheFrameLoop(t *testing.T) {
	clearToastAfterTest(t)
	t.Cleanup(func() {
		chiakiPS5LaunchAfterInstall.Store(false)
		ps5LaunchPending.Store(false)
	})

	// Install triggered from the panel: nothing to launch.
	chiakiPS5LaunchAfterInstall.Store(false)
	chiakiPS5InstallSucceeded()
	if ps5LaunchPending.Load() {
		t.Fatal("panel install queued a launch")
	}

	// Install triggered from the launcher: hand over once, then forget.
	chiakiPS5LaunchAfterInstall.Store(true)
	chiakiPS5InstallSucceeded()
	if !ps5LaunchPending.Load() {
		t.Fatal("launcher install did not queue a launch")
	}
	if chiakiPS5LaunchAfterInstall.Load() {
		t.Fatal("launch flag should be consumed")
	}

	// The frame loop consumes it exactly once, and clears it when there is
	// nothing to launch so it cannot loop forever.
	withChiakiSDRoot(t)
	ps5PollPendingLaunch()
	if ps5LaunchPending.Load() {
		t.Fatal("poll left the pending flag set")
	}
}

// TestHomeHeaderDrawsPS5TileBesideQR renders the real home screen and checks the
// launcher lands in the header band to the left of the QR, painted in its own
// colour rather than on top of the code.
func TestHomeHeaderDrawsPS5TileBesideQR(t *testing.T) {
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })
	withChiakiSDRoot(t)

	// A 21x21 module pattern is enough for the header code to have something to
	// draw; the launcher only needs the QR's rectangle to exist.
	companion.qr = make([][]bool, 21)
	for y := range companion.qr {
		companion.qr[y] = make([]bool, 21)
		for x := range companion.qr[y] {
			companion.qr[y][x] = (x+y)%2 == 0
		}
	}

	frame := renderHomeFrame(t, false)
	if companionHeaderRect.W == 0 {
		t.Skip("header QR was not drawn at this viewport")
	}
	if ps5TileRect.W == 0 {
		t.Fatalf("PS5 launcher was not drawn (QR %+v)", companionHeaderRect)
	}
	if ps5TileRect.X+ps5TileRect.W > companionHeaderRect.X {
		t.Fatalf("launcher %+v overlaps the QR %+v", ps5TileRect, companionHeaderRect)
	}
	if ps5TileRect.Y != companionHeaderRect.Y || ps5TileRect.H != companionHeaderRect.H {
		t.Fatalf("launcher %+v is not in the QR's band %+v", ps5TileRect, companionHeaderRect)
	}

	// The tile body is PlayStation blue; sample above the centred label.
	probe := frame.at(ps5TileRect.X+ps5TileRect.W/2, ps5TileRect.Y+8)
	if !nearColor(probe, sdl.Color{R: 0, G: 112, B: 209}, 40) {
		t.Fatalf("tile pixel %v is not the launcher fill", probe)
	}
}

// TestPS5TileAppearsInCompactLayout covers the 960x720 (4:3 device) canvas: the
// launcher has to be there too, still clear of the QR and of the greeting.
func TestPS5TileAppearsInCompactLayout(t *testing.T) {
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })
	withChiakiSDRoot(t)

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	companion.qr = make([][]bool, 21)
	for y := range companion.qr {
		companion.qr[y] = make([]bool, 21)
	}

	screenWidth, screenHeight = 960, 720
	homeLayout = nil
	homeCalendarOpen = false
	currentSceneIndex = 0
	renderHomeModern(renderer, &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}})

	if companionHeaderRect.W == 0 {
		t.Skip("header QR was not drawn at 960x720")
	}
	if ps5TileRect.W == 0 {
		t.Fatalf("PS5 launcher missing on the compact layout (QR %+v)", companionHeaderRect)
	}
	if ps5TileRect.X+ps5TileRect.W > companionHeaderRect.X {
		t.Fatalf("launcher %+v overlaps the QR %+v", ps5TileRect, companionHeaderRect)
	}
	if ps5TileRect.X < scaleX(480, 960) {
		t.Fatalf("launcher at x=%d would cover the greeting column", ps5TileRect.X)
	}
}

// TestHomeClickRoutesToPS5Tile covers the mouse path: a click inside the tile
// is consumed by the launcher and never falls through to the cards underneath.
func TestHomeClickRoutesToPS5Tile(t *testing.T) {
	clearToastAfterTest(t)
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	prevLayout := homeLayout
	t.Cleanup(func() {
		ps5TileRect = prevTile
		homeLayout = prevLayout
		chiakiPS5LaunchAfterInstall.Store(false)
	})

	// A layout is needed for the click handler to run at all; the tile check
	// happens before card hit-testing.
	homeLayout = NewHomeLayout(1280, 720)
	ps5TileRect = sdl.Rect{X: 900, Y: 58, W: 104, H: 104}

	if !handleHomeMouseClick(950, 100, &Config{}) {
		t.Fatal("a click on the PS5 tile was not consumed")
	}
	// The press tried to install (this test platform cannot), so nothing may be
	// left queued for launch.
	if ps5LaunchPending.Load() || chiakiPS5LaunchAfterInstall.Load() {
		t.Fatal("click left a launch queued")
	}

	// A click that misses the tile and every card is not consumed.
	ps5TileRect = sdl.Rect{X: 900, Y: 58, W: 104, H: 104}
	if handleHomeMouseClick(5, 470, &Config{}) {
		t.Fatal("a click outside the tile should fall through to normal handling")
	}
}

// TestHomePS5LauncherLinksTheCardBeneathItOnWideHeader is the handheld case:
// the Smart Pro shows network, weather, clock and battery in the header, which
// pushes the QR - and the launcher beside it - left of where the short desktop
// cluster puts them. The Up edge that reaches the launcher has to follow the
// tile, not a hard-coded column.
func TestHomePS5LauncherLinksTheCardBeneathItOnWideHeader(t *testing.T) {
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })
	withChiakiSDRoot(t)

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
			companion.qr[y][x] = (x*3+y)%4 == 0
		}
	}

	layout := NewHomeLayout(probeW, probeH)
	data := HomeViewData{
		Greeting:    "Good afternoon",
		Subtitle:    "What do you want to do?",
		Version:     "v0.4.0",
		StatusParts: []string{"192.168.1.42", "72F clear", "12:40", "87%"},
	}
	if err := layout.Render(renderer, loadHomeFonts(), data); err != nil {
		t.Fatalf("render: %v", err)
	}
	if ps5TileRect.W == 0 {
		t.Skip("launcher was not drawn with this header")
	}

	under := layout.CardUnderTile(ps5TileRect)
	if under == "" {
		t.Fatalf("launcher %+v has no card beneath it", ps5TileRect)
	}
	item := layout.Items[under]
	center := ps5TileRect.X + ps5TileRect.W/2
	if center < item.Rect.X || center >= item.Rect.X+item.Rect.W {
		t.Fatalf("launcher centre %d is not over the linked card %q %+v", center, under, item.Rect)
	}
	t.Logf("wide header: tile %+v sits over %q", ps5TileRect, under)
	if got := layout.Focus.TileLink(); got != under {
		t.Fatalf("launcher link = %q, want the card under it (%q)", got, under)
	}

	// A D-pad press from that card must land on the launcher, whichever card it
	// turned out to be.
	homeLayout = layout
	homeLayoutActive = true
	if !layout.Focus.Set(under) {
		t.Fatalf("cannot focus %q", under)
	}
	up := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_UP}
	if !handleHomeController(up, &Config{}) {
		t.Fatal("D-pad Up was not handled on home")
	}
	if got := layout.Focus.Current(); got != HomeFocusPS5 {
		t.Fatalf("D-pad Up from %q focused %q, want the launcher", under, got)
	}
}

func TestPS5TileHiddenWithoutRoom(t *testing.T) {
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })
	ps5TileRect = sdl.Rect{X: 10, Y: 10, W: 100, H: 100}

	// The tile is hidden at 480p; a stale rect must not keep swallowing clicks.
	got := renderPS5HeaderTile(nil, HomeFonts{}, sdl.Rect{X: 900, Y: 58, W: 104, H: 104}, ps5TileVisual{})
	if got.W != 0 || ps5TileRect.W != 0 {
		t.Fatalf("hidden tile kept a rect: %+v", ps5TileRect)
	}
}

// TestHomePS5TileIsTheDpadTargetAboveItsCard checks the real 1280x720 home
// screen: the launcher is drawn, its focus entry is live, the Up edge of the
// card it sits over points at it, and A presses it. This is the Smart Pro path -
// the device has a D-pad, no touchscreen.
func TestHomePS5TileIsTheDpadTargetAboveItsCard(t *testing.T) {
	clearToastAfterTest(t)
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() {
		ps5TileRect = prevTile
		chiakiPS5LaunchAfterInstall.Store(false)
		ps5LaunchPending.Store(false)
	})
	withChiakiSDRoot(t)

	companion.qr = make([][]bool, 21)
	for y := range companion.qr {
		companion.qr[y] = make([]bool, 21)
		for x := range companion.qr[y] {
			companion.qr[y][x] = (x+y)%2 == 0
		}
	}

	renderHomeFrame(t, false)
	if ps5TileRect.W == 0 || homeLayout == nil {
		t.Skip("launcher was not drawn at this viewport")
	}
	if !homeLayout.Focus.IsEnabled(HomeFocusPS5) {
		t.Fatal("the focused launcher is on screen but its focus entry is disabled")
	}

	under := homeLayout.CardUnderTile(ps5TileRect)
	if under == "" {
		t.Fatalf("launcher %+v has no card under it", ps5TileRect)
	}
	if got := homeLayout.Focus.TileLink(); got != under {
		t.Fatalf("launcher link = %q, want the card under it (%q)", got, under)
	}

	if !homeLayout.Focus.Set(under) {
		t.Fatalf("cannot focus %q", under)
	}
	homeKey(t, &Config{}, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusPS5 {
		t.Fatalf("Up from %q focused %q, want the PS5 launcher", under, got)
	}

	// The launcher itself must stay on screen and reachable in both directions.
	homeKey(t, &Config{}, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusClock {
		t.Fatalf("Up from the launcher focused %q, want clock", got)
	}
	if !homeLayout.Focus.Set(HomeFocusPS5) {
		t.Fatal("cannot focus the launcher")
	}
	homeKey(t, &Config{}, sdl.K_DOWN)
	if got := homeLayout.Focus.Current(); got == HomeFocusPS5 {
		t.Fatal("Down from the launcher did not move focus")
	}
	if !homeLayout.Focus.Set(HomeFocusPS5) {
		t.Fatal("cannot focus the launcher")
	}

	// A must run the launcher. This host cannot install the TrimUI package, so a
	// refused install (no queued launch) is the observable, non-crashing proof
	// that A is wired to the tile.
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	if !handleHomeController(a, &Config{}) {
		t.Fatal("A was not handled on home")
	}
	if homePressedID != HomeFocusPS5 {
		t.Fatalf("A on the launcher gave press feedback for %q", homePressedID)
	}
	if ps5LaunchPending.Load() || chiakiPS5LaunchAfterInstall.Load() {
		t.Fatal("a refused install left a launch queued")
	}
}
