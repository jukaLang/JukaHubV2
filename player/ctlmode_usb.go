package main

// ctlmode_usb.go — Controller Mode's USB link: a cable to a computer.
//
// The USB side is the USB controller engine (usbpad*.go) in its external
// mode: Controller Mode reads the pad itself (ctlmode.go, so Menu's tap,
// the layouts and the live display work the same as over Wi-Fi), and hands
// every state to usbPadSubmit; the engine builds the gadget — an Xbox 360
// controller Windows and Steam take with no driver, or a generic HID gamepad
// for macOS, Linux and Android — follows the computer and drives the motor
// when a game asks for rumble. The engine's own screen (Tools → System →
// Controller mode, usbpad_ui.go) stays for the port's diagnostics; the two
// never run at once: whichever started first holds the port.

import (
	"strings"
	"sync"
)

// ctlUSBShapeKey remembers the device shape chosen on the page.
const ctlUSBShapeKey = "ctlModeUSBShape"

// ctlUSBTransport is the "usb" link (main thread only, apart from what the
// engine guards itself).
type ctlUSBTransport struct {
	mu      sync.Mutex
	started bool
	probed  bool
	// shape is the mode Start used (usbPadModeXbox or usbPadModeHID).
	shape string
	// fellBack is set when Xbox was asked for and the kernel could only
	// be a generic gamepad.
	fellBack bool
}

var ctlUSB = &ctlUSBTransport{}

func init() { ctlModeRegisterTransport(5, ctlUSB) }

func (t *ctlUSBTransport) ID() string    { return "usb" }
func (t *ctlUSBTransport) Title() string { return "USB cable to a computer" }
func (t *ctlUSBTransport) Subtitle() string {
	return "An Xbox 360 controller for Windows and Steam, or a generic gamepad for Mac, Linux and Android"
}

// ctlUSBShapeWanted is the shape the page chose (Xbox unless set).
func ctlUSBShapeWanted() string {
	if appConfig != nil {
		if v, ok := customValue(appConfig.Variables.Custom, ctlUSBShapeKey); ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) == usbPadModeHID {
				return usbPadModeHID
			}
		}
	}
	return usbPadModeXbox
}

func (t *ctlUSBTransport) Available() (bool, string) {
	if !usbPadSupported() {
		return false, "Needs the handheld's USB-C port"
	}
	if !t.probed && !t.started {
		// The kernel check builds and removes a scratch gadget: once per
		// visit is plenty (the engine's own screen redoes it on its visits).
		t.probed = true
		usbPadProbeAsync()
	}
	caps := usbPadCapsNow()
	if caps.checked && !caps.canHID() && !caps.canXbox() {
		why := strings.TrimSpace(caps.problem)
		if why == "" {
			why = "This firmware's kernel cannot act as a USB controller"
		}
		return false, why
	}
	return true, ""
}

func (t *ctlUSBTransport) Start() error {
	if !usbPadSupported() {
		return ctlError("Needs the handheld's USB-C port")
	}
	if usbPadSnapshot().phase != usbPadIdle {
		return ctlError("The USB controller screen is using the port — stop it there first")
	}
	shape := ctlUSBShapeWanted()
	fell := false
	if caps := usbPadCapsNow(); caps.checked && shape == usbPadModeXbox && !caps.canXbox() && caps.canHID() {
		shape, fell = usbPadModeHID, true
	}
	usbPadTurnOnWith(appConfig, usbPadStartConfig{mode: shape, external: true})
	t.mu.Lock()
	t.started, t.shape, t.fellBack = true, shape, fell
	t.mu.Unlock()
	return nil
}

func (t *ctlUSBTransport) Stop() {
	t.mu.Lock()
	was := t.started
	t.started = false
	t.probed = false
	t.mu.Unlock()
	if was {
		usbPadSubmit(usbPadState{})
		usbPadStopAsync()
	}
}

// ctlUSBButtonPairs maps Controller Mode's buttons onto the engine's; both
// name them by their place on an Xbox pad.
var ctlUSBButtonPairs = [...]struct{ from, to uint32 }{
	{ctlBtnA, usbPadA}, {ctlBtnB, usbPadB}, {ctlBtnX, usbPadX}, {ctlBtnY, usbPadY},
	{ctlBtnLB, usbPadLB}, {ctlBtnRB, usbPadRB}, {ctlBtnBack, usbPadBack}, {ctlBtnStart, usbPadStart},
	{ctlBtnGuide, usbPadGuide}, {ctlBtnLS, usbPadLS}, {ctlBtnRS, usbPadRS},
	{ctlBtnUp, usbPadUp}, {ctlBtnDown, usbPadDown}, {ctlBtnLeft, usbPadLeft}, {ctlBtnRight, usbPadRight},
}

// ctlUSBState converts a state: the engine wants +Y up (XInput's way).
func ctlUSBState(st ctlPadState) usbPadState {
	var b uint32
	for _, p := range ctlUSBButtonPairs {
		if st.Buttons&p.from != 0 {
			b |= p.to
		}
	}
	return usbPadState{
		buttons: b,
		lx:      st.LX,
		ly:      ctlNeg16(st.LY),
		rx:      st.RX,
		ry:      ctlNeg16(st.RY),
		lt:      st.LT,
		rt:      st.RT,
	}
}

// Send never blocks: the engine packs the newest state and its sender writes
// it when the computer polls.
func (t *ctlUSBTransport) Send(st ctlPadState) {
	usbPadSubmit(ctlUSBState(st))
}

func (t *ctlUSBTransport) Status() ctlLinkStatus {
	t.mu.Lock()
	started, shape := t.started, t.shape
	t.mu.Unlock()
	if !started {
		return ctlLinkStatus{State: ctlLinkIdle}
	}
	s := usbPadSnapshot()
	switch s.phase {
	case usbPadStarting:
		return ctlLinkStatus{State: ctlLinkWaiting, Detail: "Getting the USB port ready…"}
	case usbPadStopping:
		return ctlLinkStatus{State: ctlLinkWaiting, Detail: "Giving the USB port back…"}
	case usbPadActive:
		if s.connected {
			return ctlLinkStatus{State: ctlLinkConnected, Peer: "the computer (USB)", PeerID: "usb:" + shape,
				Detail: usbPadModeLabel(shape) + " over the cable"}
		}
		return ctlLinkStatus{State: ctlLinkWaiting,
			Detail: "Plug the handheld into the computer with a data USB cable"}
	}
	why := strings.TrimSpace(s.err)
	if why == "" {
		why = "The USB controller stopped"
	} else {
		why = "Could not start: " + why
	}
	return ctlLinkStatus{State: ctlLinkError, Detail: why}
}

func (t *ctlUSBTransport) Rows() []ctlRow {
	t.mu.Lock()
	started, fell := t.started, t.fellBack
	t.mu.Unlock()
	var rows []ctlRow
	if !started {
		if ctlUSBShapeWanted() == usbPadModeHID {
			rows = append(rows, ctlRow{Label: "Shape: Generic gamepad",
				Detail: "macOS, Linux, Android and DirectInput games. A switches to Xbox 360", Action: "shape"})
		} else {
			rows = append(rows, ctlRow{Label: "Shape: Xbox 360 controller",
				Detail: "Windows and Steam see an Xbox 360 pad. A switches to Generic gamepad", Action: "shape"})
		}
	}
	if fell {
		rows = append(rows, ctlRow{Label: "Running as a generic gamepad",
			Detail: "This kernel cannot be an Xbox 360 pad (no FunctionFS)"})
	}
	if s := usbPadSnapshot(); started && strings.TrimSpace(s.note) != "" {
		rows = append(rows, ctlRow{Label: "Port", Detail: s.note})
	}
	rows = append(rows, ctlRow{Label: "While it runs",
		Detail: "USB file transfer pauses and comes back afterwards; host mode (OTG) must be off"})
	return rows
}

func (t *ctlUSBTransport) Prompt() *ctlPrompt { return nil }

func (t *ctlUSBTransport) Do(action string) {
	if action != "shape" {
		return
	}
	t.mu.Lock()
	started := t.started
	t.mu.Unlock()
	if started {
		return
	}
	next := usbPadModeHID
	if ctlUSBShapeWanted() == usbPadModeHID {
		next = usbPadModeXbox
	}
	ctlStoreSetting(ctlUSBShapeKey, next)
	PlayActivateSound()
}
