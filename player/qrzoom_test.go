package main

// qrzoom_test.go — the enlarged QR card and the three surfaces that open it.
//
// The code inside a panel is small and sits on a dim card, which is exactly the
// combination a phone camera struggles with. These tests pin the card the user
// gets instead: full screen, the backlight at full while it is up and back
// where it was afterwards, one press to dismiss from either button, and a real
// code drawn larger than the panel it came from.

import (
	"testing"
	"unsafe"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

// qrZoomFixture isolates the card and swaps the backlight for a recorder, so a
// test never writes to a real device.
func qrZoomFixture(t *testing.T, pct int, readable bool) *[]int {
	t.Helper()
	var writes []int
	prevOpen := qrZoom
	prevRead, prevWrite := qrZoomReadBrightness, qrZoomWriteBrightness
	qrZoomReadBrightness = func() (int, bool) { return pct, readable }
	qrZoomWriteBrightness = func(v int) error {
		writes = append(writes, v)
		return nil
	}
	t.Cleanup(func() {
		qrZoom = prevOpen
		qrZoomReadBrightness, qrZoomWriteBrightness = prevRead, prevWrite
	})
	qrZoom = qrZoomState{}
	return &writes
}

// qrZoomModules builds a real code for a test URL: a hand-made bitmap could be
// symmetric enough to pass a pixel count without being a code.
func qrZoomModules(t *testing.T, link string) [][]bool {
	t.Helper()
	code, err := qrcode.New(link, qrcode.Low)
	if err != nil {
		t.Fatalf("building the QR code failed: %v", err)
	}
	modules := code.Bitmap()
	dark := 0
	for _, row := range modules {
		for _, on := range row {
			if on {
				dark++
			}
		}
	}
	if dark < 50 {
		t.Fatalf("test QR code has only %d dark modules", dark)
	}
	return modules
}

// Opening the card pushes the backlight to full, and closing puts back exactly
// the level the device had — a scan must not leave the handheld at full
// brightness afterwards.
func TestQRZoomRaisesBrightnessAndRestoresIt(t *testing.T) {
	writes := qrZoomFixture(t, 35, true)
	modules := qrZoomModules(t, "http://192.168.1.42:8317/?p=123456")

	if !qrZoomOpenCard(modules, "Remote Cast") {
		t.Fatal("opening the card was refused for a real code")
	}
	if !qrZoomActive() {
		t.Fatal("the card did not become active")
	}
	if len(*writes) != 1 || (*writes)[0] != qrZoomBrightness {
		t.Fatalf("brightness writes = %v, want one write of %d", *writes, qrZoomBrightness)
	}

	qrZoomCloseCard()
	if qrZoomActive() {
		t.Fatal("the card stayed active after closing")
	}
	if len(*writes) != 2 || (*writes)[1] != 35 {
		t.Fatalf("brightness writes = %v, want the original 35 restored", *writes)
	}

	// Closing twice is a no-op, so every dismissal path can call it.
	qrZoomCloseCard()
	if len(*writes) != 2 {
		t.Fatalf("a second close wrote brightness again: %v", *writes)
	}
}

// A device with no readable backlight (the desktop builds) still gets the
// card; it just must not write a guessed level back to it.
func TestQRZoomOnADeviceWithoutBacklight(t *testing.T) {
	writes := qrZoomFixture(t, 0, false)
	if !qrZoomOpenCard(qrZoomModules(t, "http://192.168.1.42:8317/?p=123456"), "Phone Companion") {
		t.Fatal("the card was refused on a device without a backlight")
	}
	qrZoomCloseCard()
	if len(*writes) != 0 {
		t.Fatalf("brightness writes = %v, want none without a readable backlight", *writes)
	}
}

// A surface whose code is not ready yet (Wi-Fi down, no server) must say so and
// leave the screen as it was rather than show an empty white square.
func TestQRZoomRefusesACodeItCannotDraw(t *testing.T) {
	writes := qrZoomFixture(t, 60, true)
	clearToastAfterTest(t)
	if qrZoomOpenCard(nil, "Remote Cast") {
		t.Fatal("an empty code was accepted")
	}
	if qrZoomActive() {
		t.Fatal("the card opened without a code")
	}
	if len(*writes) != 0 {
		t.Fatalf("brightness writes = %v, want none for a refused code", *writes)
	}
}

// The card is read-only, so either button gets out of it — keyboard, pad and a
// tap — and nothing behind it answers the press.
func TestQRZoomDismissedByEitherButton(t *testing.T) {
	qrZoomFixture(t, 50, true)
	modules := qrZoomModules(t, "http://192.168.1.42:8317/?p=123456")

	for _, key := range []sdl.Keycode{sdl.K_ESCAPE, sdl.K_b, sdl.K_RETURN, sdl.K_a, sdl.K_SPACE} {
		qrZoomOpenCard(modules, "Remote Cast")
		ev := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: key}}
		if !handleQRZoomKey(ev) {
			t.Fatalf("key %v was not consumed by the enlarged card", key)
		}
		if qrZoomActive() {
			t.Fatalf("key %v did not close the card", key)
		}
	}

	for _, button := range []uint8{sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK} {
		qrZoomOpenCard(modules, "Remote Cast")
		ev := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}
		if !ctrlQRZoom(ev) {
			t.Fatalf("pad button %d was not consumed by the enlarged card", button)
		}
		if qrZoomActive() {
			t.Fatalf("pad button %d did not close the card", button)
		}
	}

	// A closed card consumes nothing, so the panel underneath keeps working.
	if handleQRZoomKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}) {
		t.Fatal("a closed card still consumed keys")
	}
}

// renderQRZoomProbe draws the card over a known background on the Brick's
// 960x544 canvas and returns the captured frame.
func renderQRZoomProbe(t *testing.T, modules [][]bool) (*probeFrame, sdl.Rect, sdl.Rect) {
	t.Helper()
	f := newProbeFrame(t)
	withScreen(t, 960, 544)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (the frame assertions below would otherwise never run)", err)
	}
	t.Cleanup(func() { destroyTestRenderer(renderer) })
	ClearTextCache()

	prevOpen := qrZoom
	qrZoom = qrZoomState{open: true, title: "Remote Cast", modules: modules}
	t.Cleanup(func() { qrZoom = prevOpen })

	card, grid := qrZoomLayout(screenWidth, screenHeight, len(modules))
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderQRZoom(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	return f, card, grid
}

// The card really is full screen and really is bigger than the panel's code:
// the whole point is a code a phone can read from across a room.
func TestQRZoomDrawsARealCodeFullScreen(t *testing.T) {
	// The panel draws the same link at 248px (diagnostics) / 300px (cast), so
	// anything near that is the bug this feature fixes.
	const panelCode = int32(248)
	modules := qrZoomModules(t, "http://192.168.1.42:8317/?p=123456")

	side := int32(0)
	{
		card, grid := qrZoomLayout(960, 544, len(modules))
		side = card.W
		if card.W != card.H {
			t.Fatalf("card = %dx%d, want a square", card.W, card.H)
		}
		if grid.X <= card.X || grid.Y <= card.Y || grid.X+grid.W >= card.X+card.W {
			t.Fatalf("module grid %+v is not inside the card %+v", grid, card)
		}
	}
	if side < panelCode*3/2 {
		t.Fatalf("card side = %d, want at least %d (the panel code is %d)", side, panelCode*3/2, panelCode)
	}
	if side > 544 {
		t.Fatalf("card side = %d, taller than the 544px screen", side)
	}
	// It fills the short side: the bands are text lines, not margins.
	if side < 544*4/5 {
		t.Fatalf("card side = %d, want it to fill the 544px screen", side)
	}

	f, card, grid := renderQRZoomProbe(t, modules)

	// The backdrop is opaque, so the panel that made the code unscannable is
	// gone rather than showing through the white card.
	if edge := f.at(8, 8); edge.R > 30 || edge.G > 30 || edge.B > 30 {
		t.Fatalf("corner pixel = %+v, want the dark backdrop", edge)
	}
	// The card is white paper under the modules.
	if white := f.at(card.X+3, card.Y+card.H/2); white.R < 230 || white.G < 230 || white.B < 230 {
		t.Fatalf("card edge pixel = %+v, want the white card", white)
	}
	// And the code is drawn: dark modules on that white card, clustered rather
	// than one stray line, and not so dense that the light modules vanished.
	black, total := 0, 0
	for y := grid.Y; y < grid.Y+grid.H; y++ {
		for x := grid.X; x < grid.X+grid.W; x++ {
			total++
			if c := f.at(x, y); c.R < 40 && c.G < 40 && c.B < 40 {
				black++
			}
		}
	}
	if total == 0 {
		t.Fatal("the module grid has no area, so nothing could be drawn")
	}
	if black < 600 {
		t.Fatalf("code area has only %d black pixels; the code is not drawn", black)
	}
	if black > total/2 {
		t.Fatalf("code area is %d/%d black; the light modules are missing", black, total)
	}
}

// The Remote Cast card selects between its two controls: the switch keeps the
// selection when it opens, Up reaches the code, and A on the code enlarges it
// instead of toggling the cast.
func TestCastPanelSelectsTheQRAndEnlargesIt(t *testing.T) {
	qrZoomFixture(t, 80, true)
	prevPanel := castPanel
	prevToggle, prevQR := castToggleRect, castQRRect
	prevEnabled := castIsEnabled()
	t.Cleanup(func() {
		castPanel = prevPanel
		castToggleRect, castQRRect = prevToggle, prevQR
		castSetEnabled(prevEnabled)
	})

	// The panel is installed directly: opening it for real also starts the
	// companion server, which is not what this check is about.
	castPanel = castPanelState{
		open:  true,
		focus: castFocusSwitch,
		url:   "http://192.168.1.42:8317/cast?p=123456",
		qr:    qrZoomModules(t, "http://192.168.1.42:8317/cast?p=123456"),
	}
	if castPanel.focus != castFocusSwitch {
		t.Fatalf("the panel opened on focus %d, want the switch", castPanel.focus)
	}

	// A on the switch is still the toggle it always was.
	castSetEnabled(false)
	castPanelActivate()
	if !castIsEnabled() {
		t.Fatal("A on the switch did not start the cast")
	}
	castSetEnabled(false)

	// Up selects the code, and A there opens the enlarged card without
	// starting a cast.
	if !castPanelMoveFocus(-1) {
		t.Fatal("Up did not move the selection")
	}
	if castPanel.focus != castFocusQR {
		t.Fatalf("selection = %d, want the code", castPanel.focus)
	}
	castPanelActivate()
	if !qrZoomActive() {
		t.Fatal("A on the selected code did not enlarge it")
	}
	if castIsEnabled() {
		t.Fatal("enlarging the code also started the cast")
	}
	if qrZoom.title != "Remote Cast" {
		t.Fatalf("card title = %q, want the surface's name", qrZoom.title)
	}
	qrZoomCloseCard()

	// The same selection from the pad, and a tap on the drawn code.
	if !ctrlCastPanel(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_RIGHT}, &Config{}) {
		t.Fatal("the pad did not handle a direction")
	}
	if castPanel.focus != castFocusSwitch {
		t.Fatalf("selection after Right = %d, want the switch", castPanel.focus)
	}
	if !ctrlCastPanel(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_LEFT}, &Config{}) {
		t.Fatal("the pad did not handle Left")
	}
	if castPanel.focus != castFocusQR {
		t.Fatalf("selection after Left = %d, want the code", castPanel.focus)
	}

	// A tap on the drawn code enlarges, a tap on the switch toggles.
	castQRRect = sdl.Rect{X: 40, Y: 120, W: 240, H: 240}
	if !handleCastMouse(castQRRect.X+10, castQRRect.Y+10, &Config{}) {
		t.Fatal("a tap on the code was not handled")
	}
	if !qrZoomActive() {
		t.Fatal("a tap on the code did not enlarge it")
	}
	qrZoomCloseCard()

	castPanel.focus = castFocusSwitch
	castToggleRect = sdl.Rect{X: 400, Y: 300, W: 200, H: 44}
	if !handleCastMouse(castToggleRect.X+10, castToggleRect.Y+10, &Config{}) {
		t.Fatal("a tap on the switch was not handled")
	}
	if !castIsEnabled() || castPanel.focus != castFocusSwitch {
		t.Fatal("a tap on the switch did not toggle the cast")
	}
}

// The diagnostics page has one control, so A or Enter enlarges its code; B is
// what returns to the list.
func TestCommunityDiagnosticsPageEnlargesItsQR(t *testing.T) {
	qrZoomFixture(t, 70, true)
	prevPanel := communityPanel
	prevQR := communityDiagnosticQR
	prevRect := communityQRRect
	t.Cleanup(func() {
		communityPanel = prevPanel
		communityDiagnosticQR = prevQR
		communityQRRect = prevRect
	})

	communityDiagnosticQR.qr = qrZoomModules(t, "http://192.168.1.42:8317/diag?t=abc")
	communityPanel = communityPanelState{open: true, page: communityPageDiagnosticsQR}

	key := func(sym sdl.Keycode) bool {
		return handleCommunityKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, &Config{})
	}
	if !key(sdl.K_RETURN) {
		t.Fatal("Enter was not consumed on the diagnostics page")
	}
	if !qrZoomActive() {
		t.Fatal("Enter did not enlarge the code")
	}
	if communityPanel.page != communityPageDiagnosticsQR {
		t.Fatal("enlarging the code also left the page")
	}
	if qrZoom.title != "Diagnostics export" {
		t.Fatalf("card title = %q", qrZoom.title)
	}
	qrZoomCloseCard()

	// The pad's A is the same action.
	if !ctrlCommunity(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}, &Config{}) {
		t.Fatal("pad A was not handled on the diagnostics page")
	}
	if !qrZoomActive() {
		t.Fatal("pad A did not enlarge the code")
	}
	qrZoomCloseCard()

	// B still backs out of the page, not out of the panel.
	if !ctrlCommunity(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}, &Config{}) {
		t.Fatal("pad B was not handled on the diagnostics page")
	}
	if communityPanel.page != communityPageHome || !communityPanel.open {
		t.Fatalf("B left page=%v open=%v, want the list still open", communityPanel.page, communityPanel.open)
	}
}

// The page's code is drawn as a control: the render records where it landed, a
// tap there enlarges it, and the code is really on screen.
func TestCommunityDiagnosticsQRDrawsAndTakesATap(t *testing.T) {
	qrZoomFixture(t, 70, true)
	prevPanel := communityPanel
	prevQR := communityDiagnosticQR
	prevRect := communityQRRect
	t.Cleanup(func() {
		communityPanel = prevPanel
		communityDiagnosticQR = prevQR
		communityQRRect = prevRect
	})

	communityDiagnosticQR.qr = qrZoomModules(t, "http://192.168.1.42:8317/diag?t=abc")
	communityPanel = communityPanelState{open: true, page: communityPageDiagnosticsQR}

	f := componentFrame(t, func(r *sdl.Renderer) {
		renderCommunityPanel(r, &Config{})
	})
	rect := communityQRRect
	if rect.W < 200 || rect.H < 200 {
		t.Fatalf("diagnostics QR rect = %+v, want a visible code", rect)
	}
	black := 0
	for y := rect.Y + 10; y < rect.Y+rect.H-10; y++ {
		for x := rect.X + 10; x < rect.X+rect.W-10; x++ {
			if c := f.at(x, y); c.R < 40 && c.G < 40 && c.B < 40 {
				black++
			}
		}
	}
	if black < 400 {
		t.Fatalf("diagnostics QR has only %d black pixels; the code was not drawn", black)
	}

	if !handleCommunityMouse(rect.X+rect.W/2, rect.Y+rect.H/2, &Config{}) {
		t.Fatal("a tap on the code was not handled")
	}
	if !qrZoomActive() {
		t.Fatal("a tap on the code did not enlarge it")
	}
}

// The pairing card is the same affordance with a different surface: A or a tap
// on the code enlarges it, B closes the card.
func TestCompanionCardEnlargesThePairingCode(t *testing.T) {
	qrZoomFixture(t, 40, true)
	prev := companion
	prevRect := companionQRRect
	t.Cleanup(func() {
		companion = prev
		companionQRRect = prevRect
	})

	companion = companionOverlay{open: true, pin: "123456", qr: qrZoomModules(t, "http://192.168.1.42:8317/?p=123456")}
	companionQRRect = sdl.Rect{X: 40, Y: 120, W: 280, H: 280}

	if !handleControllerEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}, &Config{}) {
		t.Fatal("pad A was not handled by the pairing card")
	}
	if !qrZoomActive() {
		t.Fatal("pad A did not enlarge the pairing code")
	}
	if qrZoom.title != "Phone Companion" {
		t.Fatalf("card title = %q", qrZoom.title)
	}
	qrZoomCloseCard()

	if !handleCompanionMouse(companionQRRect.X+10, companionQRRect.Y+10) {
		t.Fatal("a tap on the pairing code was not handled")
	}
	if !qrZoomActive() {
		t.Fatal("a tap on the pairing code did not enlarge it")
	}
	qrZoomCloseCard()

	// A tap elsewhere on the card still dismisses it, as before.
	if !handleCompanionMouse(5, 5) {
		t.Fatal("a tap outside the code was not handled")
	}
	if companion.open {
		t.Fatal("a tap outside the code did not close the card")
	}
}
