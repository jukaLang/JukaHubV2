package main

import (
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

// closePSPanelAfterTest keeps the modal panel from leaking into the next test.
func closePSPanelAfterTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		chiakiPanel = chiakiPanelState{}
		chiakiRowHits = nil
		// The panel's transient edit state lives outside chiakiPanel, so
		// closing it properly means clearing that too: a test that leaves the
		// PS5 Account-ID hand-off armed makes the next account submit continue
		// to the PIN instead of finishing, and a keyboard left up changes what
		// every later keyboard test sees. Both only show up when the suite runs
		// in a shuffled order.
		psPanelEditField = psEditNone
		psPanelPairAfterAccount = false
		vkCloseKeyboard()
	})
}

func TestPSPillsSitLeftOfQR(t *testing.T) {
	// The pill geometry is measured, so the font stack (and with it SDL_ttf)
	// has to be up before the layout is asked for sizes.
	newProbeFrame(t)
	fonts := loadHomeFonts()
	if fonts.Small == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so the pills cannot be measured")
	}
	group, ps4, ps5 := psPillRects(fonts, 1000, 78, 44)
	if group.W <= 0 || ps4.W <= 0 || ps5.W <= 0 {
		t.Fatalf("the pill group did not lay out: group %+v ps4 %+v ps5 %+v", group, ps4, ps5)
	}
	if group.X+group.W != 1000 {
		t.Fatalf("group right edge = %d, want 1000", group.X+group.W)
	}
	if ps4.X < group.X || ps5.X+ps5.W > group.X+group.W {
		t.Fatalf("pills %+v / %+v escape their group %+v", ps4, ps5, group)
	}
	if ps4.X+ps4.W > ps5.X || ps4.Y != ps5.Y || ps4.H != ps5.H {
		t.Fatalf("pills are not a left-to-right pair: %+v / %+v", ps4, ps5)
	}
	// Both pills sit inside the header band, not under the greeting column.
	if ps4.X >= ps5.X {
		t.Fatalf("PS4 pill %+v should be left of the PS5 pill %+v", ps4, ps5)
	}
	// No font, no pills: the caller hides the group instead of drawing junk.
	if g, a, b := psPillRects(HomeFonts{}, 1000, 78, 44); g.W != 0 || a.W != 0 || b.W != 0 {
		t.Fatalf("pills were laid out without a font: %+v %+v %+v", g, a, b)
	}
}

func TestPS5TileStateFollowsThePairing(t *testing.T) {
	if !psRemoteSupported() {
		t.Skip("the native client runs on the TrimUI Linux build")
	}
	useTempPSHosts(t)
	if got := ps5TileCurrentState(); got != ps5TileInstall {
		t.Fatalf("state without a paired PS5 = %v, want pair", got)
	}

	upsertPSConsole(psConsole{
		Host:      "192.168.1.50",
		Name:      "Living Room",
		Target:    psTargetPS5_1,
		PS5:       true,
		RegistKey: "0a1b2c3d",
		RPKey:     "AAECAwQFBgcICQoLDA0ODw==",
	})
	if got := ps5TileCurrentState(); got != ps5TileLaunch {
		t.Fatalf("state with a paired PS5 = %v, want play", got)
	}

	// A paired PS4 must not turn the PS5 tile into a play button.
	useTempPSHosts(t)
	upsertPSConsole(psConsole{
		Host:      "192.168.1.40",
		Name:      "Desk",
		Target:    psTargetPS4FW10,
		RegistKey: "0a1b2c3d",
		RPKey:     "AAECAwQFBgcICQoLDA0ODw==",
	})
	if got := ps5TileCurrentState(); got != ps5TileInstall {
		t.Fatalf("a PS4-only pairing changed the PS5 tile to %v", got)
	}
}

func TestPS4TileStateFollowsThePairing(t *testing.T) {
	if !psRemoteSupported() {
		t.Skip("the native client runs on the TrimUI Linux build")
	}
	useTempPSHosts(t)
	if got := ps4TileCurrentState(); got != ps4TileInstall {
		t.Fatalf("state without a paired PS4 = %v, want pair", got)
	}
	upsertPSConsole(psConsole{
		Host:      "192.168.1.40",
		Name:      "Desk",
		Target:    psTargetPS4FW10,
		RegistKey: "0a1b2c3d",
		RPKey:     "AAECAwQFBgcICQoLDA0ODw==",
	})
	if got := ps4TileCurrentState(); got != ps4TileLaunch {
		t.Fatalf("state with a paired PS4 = %v, want play", got)
	}
}

// TestHomeHeaderDrawsPS5TileBesideQR renders the real home screen and checks the
// launcher lands in the header band to the left of the QR, painted in its own
// colour rather than on top of the code.
func TestHomeHeaderDrawsPS5TileBesideQR(t *testing.T) {
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })

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
		t.Fatal("the header QR was not drawn at this viewport; the launcher it pairs with cannot be checked")
	}
	if ps5TileRect.W == 0 {
		t.Fatalf("PS5 launcher was not drawn (QR %+v)", companionHeaderRect)
	}
	if ps5TileRect.X+ps5TileRect.W > companionHeaderRect.X {
		t.Fatalf("launcher %+v overlaps the QR %+v", ps5TileRect, companionHeaderRect)
	}
	// The pills sit vertically centred on the QR they are beside, not at its
	// top edge: the group is a 44px capsule in a 72px band.
	if ps5TileRect.Y < companionHeaderRect.Y || ps5TileRect.Y+ps5TileRect.H > companionHeaderRect.Y+companionHeaderRect.H {
		t.Fatalf("launcher %+v is not inside the QR's band %+v", ps5TileRect, companionHeaderRect)
	}
	if center := ps5TileRect.Y + ps5TileRect.H/2; center < companionHeaderRect.Y+companionHeaderRect.H/2-scaleY(4, probeH) ||
		center > companionHeaderRect.Y+companionHeaderRect.H/2+scaleY(4, probeH) {
		t.Fatalf("launcher centre y=%d is not level with the QR band %+v", center, companionHeaderRect)
	}
	// The pill draws its label, so its centre must not be the page background.
	probe := frame.at(ps5TileRect.X+ps5TileRect.W/2, ps5TileRect.Y+ps5TileRect.H/2)
	if frame.isBG(ps5TileRect.X+ps5TileRect.W/2, ps5TileRect.Y+ps5TileRect.H/2) && nearColor(probe, frame.bg, 2) {
		t.Fatalf("nothing was drawn inside the pill group at %+v", ps5TileRect)
	}
}

// TestPS5TileAppearsInCompactLayout covers the 960x720 (4:3 device) canvas: the
// launcher has to be there too, still clear of the QR and of the greeting.
func TestPS5TileAppearsInCompactLayout(t *testing.T) {
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
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
		t.Fatal("the header QR was not drawn at 960x720; the compact header lost its pairing code")
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
	closePSPanelAfterTest(t)
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	prevLayout := homeLayout
	t.Cleanup(func() {
		ps5TileRect = prevTile
		homeLayout = prevLayout
	})

	// A layout is needed for the click handler to run at all; the tile check
	// happens before card hit-testing.
	homeLayout = NewHomeLayout(1280, 720)
	ps5TileRect = sdl.Rect{X: 900, Y: 58, W: 104, H: 104}

	if !handleHomeMouseClick(950, 100, &Config{}) {
		t.Fatal("a click on the PS5 tile was not consumed")
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

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
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
		t.Fatal("the PS5 launcher was not drawn beside the header; the home layout lost the tile")
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

func TestPSPillGroupIsNotDrawnWithoutRenderer(t *testing.T) {
	prevPS4, prevPS5 := ps4TileRect, ps5TileRect
	t.Cleanup(func() { ps4TileRect, ps5TileRect = prevPS4, prevPS5 })

	// renderPSPillGroup records nothing itself; the layout owns the rects. What
	// matters here is that a nil renderer paints nothing instead of panicking on
	// the handheld's first frame.
	renderPSPillGroup(nil, HomeFonts{}, sdl.Rect{X: 900, Y: 78, W: 200, H: 44},
		sdl.Rect{X: 904, Y: 82, W: 96, H: 36}, sdl.Rect{X: 1004, Y: 82, W: 96, H: 36}, psPillVisual{})
}

// TestHomePS5TileIsTheDpadTargetAboveItsCard checks the real 1280x720 home
// screen: the launcher is drawn, its focus entry is live, the Up edge of the
// card it sits over points at it, and A presses it. This is the Smart Pro path -
// the device has a D-pad, no touchscreen.
func TestHomePS5TileIsTheDpadTargetAboveItsCard(t *testing.T) {
	clearToastAfterTest(t)
	closePSPanelAfterTest(t)
	snapshotHomeGlobals(t)
	prevTile := ps5TileRect
	t.Cleanup(func() { ps5TileRect = prevTile })

	companion.qr = make([][]bool, 21)
	for y := range companion.qr {
		companion.qr[y] = make([]bool, 21)
		for x := range companion.qr[y] {
			companion.qr[y][x] = (x+y)%2 == 0
		}
	}

	renderHomeFrame(t, false)
	if ps5TileRect.W == 0 || homeLayout == nil {
		t.Fatal("the PS5 launcher was not drawn at this viewport")
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

	// A must run the launcher. On a host without the native client the press
	// refuses (no panel), which is the observable, non-crashing proof that A is
	// wired to the tile.
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	if !handleHomeController(a, &Config{}) {
		t.Fatal("A was not handled on home")
	}
	if homePressedID != HomeFocusPS5 {
		t.Fatalf("A on the launcher gave press feedback for %q", homePressedID)
	}
	if !psRemoteSupported() && chiakiPanel.open {
		t.Fatal("the client opened on a host that cannot run it")
	}
}
