package main

// Offscreen checks for the Phone Companion card: the QR code has to be really
// drawn (a blank white square would scan as nothing), the error state must not
// panic, and the card must swallow every key while it is open.

import (
	"testing"
	"unsafe"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

func probeCompanionCard(t *testing.T, state companionOverlay) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	config := &Config{}
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}

	prevW, prevH := screenWidth, screenHeight
	prev := companion
	screenWidth, screenHeight = probeW, probeH
	companion = state
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		companion = prev
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderCompanionOverlay(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f
}

func TestCompanionCardDrawsScannableQR(t *testing.T) {
	const pin = "123456"
	const urlish = "http://192.168.1.42:8317/?p=123456"
	code, err := qrcode.New(urlish, qrcode.Low)
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

	f := probeCompanionCard(t, companionOverlay{open: true, pin: pin, url: urlish, qr: modules})

	// The card is 760x430 centred in the probe frame; the QR sits at the
	// left edge of its padding. Count black pixels inside that square and
	// make sure they cluster like a code rather than one stray line.
	qrX := (probeW-760)/2 + 24
	qrY := (probeH-430)/2 + 24 + 40
	box := sdl.Rect{X: qrX, Y: qrY, W: 286, H: 286}
	black := 0
	for y := box.Y + 4; y < box.Y+box.H-4; y++ {
		for x := box.X + 4; x < box.X+box.W-4; x++ {
			c := f.at(x, y)
			if c.R < 40 && c.G < 40 && c.B < 40 {
				black++
			}
		}
	}
	if black < 500 {
		t.Fatalf("QR area has only %d black pixels; the code is not drawn", black)
	}
	if limit := int(box.W * box.H / 2); black > limit {
		t.Fatalf("QR area is %d/%d black; the white modules are missing", black, limit*2)
	}

	// The PIN is drawn to the right of the code, in the accent colour, so the
	// right column must contain pixels the card background does not have.
	bg := f.at(qrX+box.W+60, qrY+4)
	coloured := 0
	for y := qrY; y < qrY+140; y++ {
		for x := qrX + box.W + 20; x < qrX+box.W+300; x++ {
			c := f.at(x, y)
			if c.R != bg.R || c.G != bg.G || c.B != bg.B {
				coloured++
			}
		}
	}
	if coloured < 200 {
		t.Fatalf("right column has only %d drawn pixels; the PIN/status text is missing", coloured)
	}
}

// The home header tile is the always-visible pairing affordance. It must draw
// a real code on a white tile and record the exact rect the click handler uses.
func TestCompanionHeaderQRDrawsScannableTile(t *testing.T) {
	code, err := qrcode.New("http://192.168.1.42:8317/?p=123456", qrcode.Low)
	if err != nil {
		t.Fatalf("building the QR code failed: %v", err)
	}
	prev := companion
	prevRect := companionHeaderRect
	t.Cleanup(func() {
		companion = prev
		companionHeaderRect = prevRect
	})
	companion = companionOverlay{pin: "123456", qr: code.Bitmap()}

	rect := sdl.Rect{X: 100, Y: 100, W: 76, H: 76}
	f := componentFrame(t, func(r *sdl.Renderer) {
		renderCompanionHeaderQR(r, HomeFonts{}, rect)
	})
	if companionHeaderRect != rect {
		t.Fatalf("header QR rect = %+v, want %+v", companionHeaderRect, rect)
	}
	if got := f.at(rect.X+rect.W/2, rect.Y+3); got.R < 230 || got.G < 230 || got.B < 230 {
		t.Errorf("header QR tile top edge = %+v, want the white tile", got)
	}
	black := 0
	for y := rect.Y + 4; y < rect.Y+rect.H-4; y++ {
		for x := rect.X + 4; x < rect.X+rect.W-4; x++ {
			if c := f.at(x, y); c.R < 40 && c.G < 40 && c.B < 40 {
				black++
			}
		}
	}
	if black < 120 {
		t.Fatalf("header QR has only %d black pixels; the code was not drawn", black)
	}
}

// The home screen must show the same scannable code without opening the full
// card, in the empty band above the action cards and left of the status text.
func TestHomeHeaderQRTileRenders(t *testing.T) {
	code, err := qrcode.New("http://192.168.1.42:8317/?p=123456", qrcode.Low)
	if err != nil {
		t.Fatalf("building the QR code failed: %v", err)
	}
	snapshotHomeGlobals(t)
	prev := companion
	prevRect := companionHeaderRect
	t.Cleanup(func() {
		companion = prev
		companionHeaderRect = prevRect
	})
	companion = companionOverlay{pin: "123456", qr: code.Bitmap()}

	f := renderHomeFrame(t, false)
	rect := companionHeaderRect
	if rect.W < 72 || rect.H < 72 {
		t.Fatalf("home header QR rect = %+v, want a visible tile", rect)
	}
	if rect.Y < scaleY(56, probeH) {
		t.Errorf("header QR top = %d, want it below the header band", rect.Y)
	}
	if rect.Y+rect.H > scaleY(164, probeH) {
		t.Errorf("header QR bottom = %d, want it above the action cards", rect.Y+rect.H)
	}
	if got := f.at(rect.X+rect.W/2, rect.Y+3); got.R < 230 || got.G < 230 || got.B < 230 {
		t.Errorf("header QR tile top edge = %+v, want the white tile", got)
	}
	black := 0
	for y := rect.Y + 4; y < rect.Y+rect.H-20; y++ {
		for x := rect.X + 4; x < rect.X+rect.W-4; x++ {
			if c := f.at(x, y); c.R < 40 && c.G < 40 && c.B < 40 {
				black++
			}
		}
	}
	if black < 200 {
		t.Fatalf("home header QR has only %d black pixels; the code was not drawn", black)
	}
}

func TestCompanionCardErrorStateRenders(t *testing.T) {
	// No QR and no server: this is what a failed bind looks like. It must
	// draw the message instead of panicking on a nil bitmap.
	_ = probeCompanionCard(t, companionOverlay{open: true, err: "listen tcp :8317: bind: address already in use"})
}

func TestCompanionKeysAreModal(t *testing.T) {
	prev := companion
	t.Cleanup(func() { companion = prev })

	companion = companionOverlay{open: true, pin: "123456"}
	up := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_UP}}
	if !handleCompanionKey(up, &Config{}) {
		t.Fatal("card did not consume a D-pad key")
	}
	if !companion.open {
		t.Fatal("card closed on an unrelated key")
	}

	esc := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}
	if !handleCompanionKey(esc, &Config{}) {
		t.Fatal("card did not consume Escape")
	}
	if companion.open {
		t.Fatal("Escape did not close the card")
	}
	if handleCompanionKey(up, &Config{}) {
		t.Fatal("a closed card still consumed keys")
	}
}

func TestCompanionControllerClosesCard(t *testing.T) {
	prev := companion
	t.Cleanup(func() { companion = prev })

	companion = companionOverlay{open: true, pin: "123456"}
	ev := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}
	if !handleControllerEvent(ev, &Config{}) {
		t.Fatal("controller B was not handled by the card")
	}
	if companion.open {
		t.Fatal("controller B did not close the card")
	}
}
