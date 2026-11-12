package main

// statusbar_bluetooth.go — the Bluetooth button in the top bar's system corner.
//
// The corner reads, left to right: the sun, the speaker, this button, the
// Wi-Fi meter, then the forecast, the clock and the battery. The rune is the
// button that opens the Bluetooth page (bluetooth_devices_ui.go), where a
// paired controller, headset or keyboard connects or disconnects with one
// press — so getting a pad onto the handheld no longer means a trip through
// Settings → Network first. Both headers draw it through the shared cluster
// (headerStatusParts), so it sits in the same place on Home and on every
// scene.
//
// The rune also says what the radio is doing: struck through while it is off,
// the accent with a dot either side while a device is connected, the plain
// rune otherwise. btQuickState (bluetooth_quick.go) keeps that reading fresh
// off the render thread, from the kernel rather than from bluetoothctl, so the
// bar never waits on Bluetooth.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// statusBluetoothPart is the Bluetooth button's part, a marker like the
// speaker's "vol|" and never drawn as text. headerStatusParts puts it after
// the volume part and before the network slot, so the rune sits immediately
// left of the Wi-Fi meter.
const statusBluetoothPart = "bt|"

// statusBluetoothRect is the button's hit area, measured on every frame by
// whichever header is on screen and grown to a finger-sized target with the
// speaker's and the meter's (growStatusButtonRects). A click, a tap or A on it
// opens the Bluetooth page.
var statusBluetoothRect sdl.Rect

// statusBluetoothState supplies the button's state. A variable, like the
// meter's reading, so a render test can pin it instead of depending on the
// host's radio.
var statusBluetoothState = btQuickState

// isBluetoothStatusPart reports whether the part is the Bluetooth button. The
// marker cannot collide with a reading: no clock, forecast, battery level,
// SSID or media title is spelt "bt|".
func isBluetoothStatusPart(part string) bool {
	return part == statusBluetoothPart
}

// statusBluetoothAvailable reports whether the bar carries the button: where
// the Bluetooth page can do something — pair, connect and disconnect on Linux
// with bluetoothctl, or open the system's own Bluetooth settings on Windows
// and macOS.
func statusBluetoothAvailable() bool {
	return statusBluetoothState().Available
}

// statusBluetoothOnPage reports whether the Bluetooth page is the screen on
// display: the button then wears the accent, the way the speaker does while
// its popover is open, so the bar says what is showing.
func statusBluetoothOnPage() bool {
	return tbxCurrentScene(appConfig) == btSceneName
}

// statusBluetoothLook is the glyph and the ink for a state: struck through and
// muted while the radio is off, a dot either side in the accent while a device
// is connected, and the plain rune in the speaker's ink otherwise — on and
// idle, or a radio nobody has read yet (claiming "off" before it answered
// would be a lie). onPage lights the rune in the accent whatever it shows.
func statusBluetoothLook(q btQuick, onPage bool) (string, sdl.Color) {
	glyph, ink := "bluetooth", ColorTextSecondary()
	switch {
	case q.Known && !q.On:
		glyph, ink = "bluetooth-off", ColorTextTertiary()
	case q.Connected > 0:
		glyph, ink = "bluetooth-connected", glassAccent()
	}
	if onPage {
		ink = glassAccent()
	}
	return glyph, ink
}

// drawStatusBluetoothChip paints the button in the speaker's box, so the
// three buttons read as a row, and returns the width it used.
func drawStatusBluetoothChip(renderer *sdl.Renderer, font *ttf.Font, x, textTop int32) int32 {
	iconH := statusIconHeight(font)
	side := statusVolumeIconWidth(iconH)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	glyph, ink := statusBluetoothLook(statusBluetoothState(), statusBluetoothOnPage())
	iconDraw(renderer, glyph, sdl.Rect{X: x, Y: textTop + (textH-side)/2, W: side, H: side}, ink)
	return side
}
