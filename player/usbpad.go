package main

// usbpad.go — "Controller mode": plug the handheld into a computer with a
// USB cable and its buttons and sticks become a controller there.
//
// The handheld's USB-C port turns into a USB device through the kernel's
// gadget framework, the same way USB file transfer does (usbmtp*.go). While
// controller mode holds the port, JukaHub builds a gadget of its own with one
// function in it, in one of two shapes chosen on the screen:
//
//   - PC (Xbox 360 compatible): the wired Xbox 360 controller's protocol —
//     Microsoft's IDs (045E:028E), a vendor interface of class FF/5D/01 with
//     an interrupt pipe in (the 20-byte input report) and one out (rumble and
//     the ring light). Windows' own driver (xusb22) and Linux's xpad take it,
//     so XInput games and Steam see an ordinary Xbox 360 pad. It is served
//     through FunctionFS (usbpad_linux.go), the way MTP is.
//   - Generic HID gamepad: 16 buttons, a hat and four axes in a standard HID
//     report, which macOS, Android, Linux and DirectInput games read without
//     a driver. The kernel's own HID function (/dev/hidgN) carries it when
//     the kernel has one; otherwise JukaHub answers the HID requests itself
//     over FunctionFS.
//
// Which of those the kernel offers is checked when the screen opens (a
// scratch gadget gets a HID and a FunctionFS function, which are removed
// again at once) and said on the screen and in the log in plain words.
//
// The pad is read straight from its evdev node beside JukaHub, not through
// SDL: once a computer is connected the node is grabbed (EVIOCGRAB), so none
// of JukaHub's own bindings — the quit chord, the voice orb's Menu, mouse
// mode — fire while the buttons belong to a game on the computer. Holding
// Select + Start for two seconds is the one way back; the computer never sees
// that chord (both buttons are held back from the report once both are
// down). With no computer connected the node is not grabbed and the screen
// works like any other: B stops controller mode.
//
// The pure parts — the evdev-to-controller state, the reports, the USB and
// HID descriptors — are here, so both builds compile them; the port itself
// is usbpad_linux.go (usbpad_other.go elsewhere: the screen is handheld
// only). USB file transfer is paused while controller mode holds the port and
// started again afterwards; host mode (OTG) and controller mode exclude each
// other. A part of JukaHub that reads the pad itself can run the same USB
// side without the evdev reader: usbPadTurnOnWith with external set, then
// usbPadSubmit with each state, usbPadStopAsync to end.

import (
	"encoding/binary"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// The two device shapes.
const (
	usbPadModeXbox = "xbox"
	usbPadModeHID  = "hid"
)

// usbPadLeaveHold is how long Select + Start are held to leave.
const usbPadLeaveHold = 2 * time.Second

// usbPadNoHostLimit ends controller mode when no computer has been connected
// for this long: the screen keeps the handheld awake while it is up.
const usbPadNoHostLimit = 10 * time.Minute

// Controller buttons, named by their place on an Xbox pad: A is the bottom
// face button, X the left one.
const (
	usbPadUp uint32 = 1 << iota
	usbPadDown
	usbPadLeft
	usbPadRight
	usbPadStart
	usbPadBack
	usbPadLS
	usbPadRS
	usbPadLB
	usbPadRB
	usbPadGuide
	usbPadA
	usbPadB
	usbPadX
	usbPadY
)

// usbPadState is the controller at one moment: buttons, sticks with up and
// right positive (XInput's convention) and the triggers.
type usbPadState struct {
	buttons        uint32
	lx, ly, rx, ry int16
	lt, rt         uint8
}

// ── From the pad's evdev events ───────────────────────────────────────────

// Keys a pad may send besides the X360-shaped set vcursor_evdev.go names.
const (
	usbPadEvTL2       = 0x138
	usbPadEvTR2       = 0x139
	usbPadEvThumbL    = 0x13d
	usbPadEvThumbR    = 0x13e
	usbPadEvDpadUp    = 0x220
	usbPadEvDpadDown  = 0x221
	usbPadEvDpadLeft  = 0x222
	usbPadEvDpadRight = 0x223
)

// usbPadEngine turns the pad's evdev events into controller states. The
// TrimUI pad is X360-shaped (vcursor_evdev.go): BTN_SOUTH is the bottom face
// button, BTN_EAST the right one, BTN_NORTH (0x133, xpad's BTN_X) the left
// one and BTN_WEST the top one; the sticks are X/Y and RX/RY with Y growing
// downwards, the triggers Z and RZ, the D-pad the hat.
type usbPadEngine struct {
	ranges     [evdevAbsHat0Y + 1]evdevAbsRange
	abs        [evdevAbsHat0Y + 1]int32
	keys       uint32
	ltKey      bool
	rtKey      bool
	printed    bool // A/B and X/Y by the printed letters (A on the right)
	chordLatch bool // Select + Start went down together: both held back until both are up
}

// keyBit is the controller button behind an evdev key, 0 for none.
func (e *usbPadEngine) keyBit(code uint16) uint32 {
	switch code {
	case evdevBtnSouth:
		if e.printed {
			return usbPadB
		}
		return usbPadA
	case evdevBtnEast:
		if e.printed {
			return usbPadA
		}
		return usbPadB
	case evdevBtnNorth:
		if e.printed {
			return usbPadY
		}
		return usbPadX
	case evdevBtnWest:
		if e.printed {
			return usbPadX
		}
		return usbPadY
	case evdevBtnTL:
		return usbPadLB
	case evdevBtnTR:
		return usbPadRB
	case evdevBtnSelect:
		return usbPadBack
	case evdevBtnStart:
		return usbPadStart
	case evdevBtnMode:
		return usbPadGuide
	case usbPadEvThumbL:
		return usbPadLS
	case usbPadEvThumbR:
		return usbPadRS
	case usbPadEvDpadUp:
		return usbPadUp
	case usbPadEvDpadDown:
		return usbPadDown
	case usbPadEvDpadLeft:
		return usbPadLeft
	case usbPadEvDpadRight:
		return usbPadRight
	}
	return 0
}

// setKeys loads the keys held now from an EVIOCGKEY bitmap.
func (e *usbPadEngine) setKeys(bitmap []byte) {
	e.keys = 0
	for code := uint16(evdevBtnSouth); code <= usbPadEvDpadRight; code++ {
		if !evdevKeyBit(bitmap, code) {
			continue
		}
		e.keys |= e.keyBit(code)
	}
	e.ltKey = evdevKeyBit(bitmap, usbPadEvTL2)
	e.rtKey = evdevKeyBit(bitmap, usbPadEvTR2)
}

// setAxis loads an axis's range and value.
func (e *usbPadEngine) setAxis(axis uint16, r evdevAbsRange, value int32) {
	if int(axis) < len(e.ranges) {
		e.ranges[axis], e.abs[axis] = r, value
	}
}

// feed takes one event and reports whether a whole report (SYN_REPORT) is
// complete, which is when the state is worth sending.
func (e *usbPadEngine) feed(ev evdevEvent) bool {
	switch ev.Type {
	case evdevEvKey:
		down := ev.Value != 0
		switch ev.Code {
		case usbPadEvTL2:
			e.ltKey = down
			return false
		case usbPadEvTR2:
			e.rtKey = down
			return false
		}
		if bit := e.keyBit(ev.Code); bit != 0 {
			if down {
				e.keys |= bit
			} else {
				e.keys &^= bit
			}
		}
	case evdevEvAbs:
		if int(ev.Code) < len(e.abs) {
			e.abs[ev.Code] = ev.Value
		}
	case evdevEvSyn:
		return ev.Code == evdevSynReport
	}
	return false
}

// releaseEvents appends what is written into the pad just before it is
// grabbed: a release for every key held and every axis back at rest (a
// trigger at the bottom, the hat at 0, a stick in the middle), so JukaHub,
// which stops seeing the pad, is not left with a button, the D-pad or a stick
// held. The engine's own view follows at once, as the games bridge's does
// (mbridgeEngine.releaseEvents).
func (e *usbPadEngine) releaseEvents(out []evdevEvent) []evdevEvent {
	for code := uint16(evdevBtnSouth); code <= usbPadEvDpadRight; code++ {
		if bit := e.keyBit(code); bit != 0 && e.keys&bit != 0 {
			out = append(out, evdevEvent{Type: evdevEvKey, Code: code, Value: 0})
		}
	}
	if e.ltKey {
		out = append(out, evdevEvent{Type: evdevEvKey, Code: usbPadEvTL2, Value: 0})
	}
	if e.rtKey {
		out = append(out, evdevEvent{Type: evdevEvKey, Code: usbPadEvTR2, Value: 0})
	}
	e.keys, e.ltKey, e.rtKey = 0, false, false
	for i, r := range e.ranges {
		if !r.valid() {
			continue
		}
		axis := uint16(i)
		rest := int32((int64(r.min) + int64(r.max)) / 2)
		switch axis {
		case evdevAbsZ, evdevAbsRZ:
			rest = r.min
		case evdevAbsHat0X, evdevAbsHat0Y:
			rest = 0
		}
		if e.abs[axis] != rest {
			out = append(out, evdevEvent{Type: evdevEvAbs, Code: axis, Value: rest})
			e.abs[axis] = rest
		}
	}
	return out
}

// usbPadStick turns -1..1 into a signed 16-bit stick value.
func usbPadStick(v float64) int16 {
	return int16(math.Round(clampFloat(v, -1, 1) * 32767))
}

// snapshot is the pad now: phys is what is held (the screen's tester), sent
// what the computer gets (Select and Start held back while the leave chord
// is down), and both whether Select and Start are both held right now.
func (e *usbPadEngine) snapshot() (phys, sent usbPadState, both bool) {
	s := usbPadState{buttons: e.keys}
	switch evdevHatDir(e.abs[evdevAbsHat0X]) {
	case -1:
		s.buttons |= usbPadLeft
	case 1:
		s.buttons |= usbPadRight
	}
	switch evdevHatDir(e.abs[evdevAbsHat0Y]) {
	case -1:
		s.buttons |= usbPadUp
	case 1:
		s.buttons |= usbPadDown
	}
	s.lx = usbPadStick(e.ranges[evdevAbsX].stick(e.abs[evdevAbsX]))
	s.ly = usbPadStick(-e.ranges[evdevAbsY].stick(e.abs[evdevAbsY]))
	s.rx = usbPadStick(e.ranges[evdevAbsRX].stick(e.abs[evdevAbsRX]))
	s.ry = usbPadStick(-e.ranges[evdevAbsRY].stick(e.abs[evdevAbsRY]))
	s.lt = uint8(math.Round(e.ranges[evdevAbsZ].trigger(e.abs[evdevAbsZ]) * 255))
	s.rt = uint8(math.Round(e.ranges[evdevAbsRZ].trigger(e.abs[evdevAbsRZ]) * 255))
	if e.ltKey {
		s.lt = 255
	}
	if e.rtKey {
		s.rt = 255
	}
	const chord = usbPadBack | usbPadStart
	both = s.buttons&chord == chord
	if both {
		e.chordLatch = true
	} else if s.buttons&chord == 0 {
		e.chordLatch = false
	}
	sent = s
	if e.chordLatch {
		sent.buttons &^= chord
	}
	return s, sent, both
}

// ── Xbox 360 (XInput) ─────────────────────────────────────────────────────

// usbPadXInputLen is the wired Xbox 360 pad's input report: type 0, length
// 20, two button bytes, the triggers, the four stick axes, six zero bytes.
const usbPadXInputLen = 20

// usbPadXInputBits places each button in the report's two button bytes.
var usbPadXInputBits = [...]struct {
	mask uint32
	at   int
	bit  uint
}{
	{usbPadUp, 2, 0}, {usbPadDown, 2, 1}, {usbPadLeft, 2, 2}, {usbPadRight, 2, 3},
	{usbPadStart, 2, 4}, {usbPadBack, 2, 5}, {usbPadLS, 2, 6}, {usbPadRS, 2, 7},
	{usbPadLB, 3, 0}, {usbPadRB, 3, 1}, {usbPadGuide, 3, 2},
	{usbPadA, 3, 4}, {usbPadB, 3, 5}, {usbPadX, 3, 6}, {usbPadY, 3, 7},
}

// usbPadXInputReport packs s the way the wired pad sends it.
func usbPadXInputReport(s usbPadState) []byte {
	r := make([]byte, usbPadXInputLen)
	r[0], r[1] = 0x00, usbPadXInputLen
	for _, b := range usbPadXInputBits {
		if s.buttons&b.mask != 0 {
			r[b.at] |= 1 << b.bit
		}
	}
	r[4], r[5] = s.lt, s.rt
	binary.LittleEndian.PutUint16(r[6:], uint16(s.lx))
	binary.LittleEndian.PutUint16(r[8:], uint16(s.ly))
	binary.LittleEndian.PutUint16(r[10:], uint16(s.rx))
	binary.LittleEndian.PutUint16(r[12:], uint16(s.ry))
	return r
}

// usbPadParseRumble reads an output report from the computer: 00 08 00
// <big motor> <small motor> 00 00 00 is rumble; anything else (the ring
// light's 01 03 xx) is not.
func usbPadParseRumble(b []byte) (big, small uint8, ok bool) {
	if len(b) < 5 || b[0] != 0x00 || b[1] < 5 {
		return 0, 0, false
	}
	return b[3], b[4], true
}

// usbPadXUSBDescriptor is the vendor descriptor (type 0x21) the wired pad
// carries after interface 0 ([MS-XUSBI] "XUSB Interface Device Descriptor":
// XUSB 1.00, wired controller, the report sizes per endpoint).
var usbPadXUSBDescriptor = []byte{0x11, 0x21, 0x00, 0x01, 0x01, 0x25, 0x81, 0x14, 0x00, 0x00, 0x00, 0x00, 0x13, 0x01, 0x08, 0x00, 0x00}

// usbPadEndpoint is one endpoint descriptor.
func usbPadEndpoint(addr, attr byte, maxp uint16, interval byte) []byte {
	return []byte{7, 5, addr, attr, byte(maxp), byte(maxp >> 8), interval}
}

// usbPadFFSBlob wraps full- and high-speed descriptor sets in FunctionFS's
// v2 header (as usbmtp_linux.go does), count descriptors in each.
func usbPadFFSBlob(fs, hs []byte, count uint32) []byte {
	const magicV2, hasFS, hasHS = 3, 1, 2
	head := make([]byte, 20)
	binary.LittleEndian.PutUint32(head[0:], magicV2)
	binary.LittleEndian.PutUint32(head[4:], uint32(len(head)+len(fs)+len(hs)))
	binary.LittleEndian.PutUint32(head[8:], hasFS|hasHS)
	binary.LittleEndian.PutUint32(head[12:], count)
	binary.LittleEndian.PutUint32(head[16:], count)
	out := append(head, fs...)
	return append(out, hs...)
}

// usbPadXboxFFSDescriptors is the Xbox 360 interface: class FF/5D/01, the
// XUSB descriptor when withXUSB (FunctionFS refuses class descriptors after
// a vendor interface on most kernels, so a second try goes without it), an
// interrupt pipe in (every 1 ms) and out (every 8 ms). FunctionFS names the
// endpoint files ep1 and ep2 in descriptor order; the addresses the computer
// sees are the ones the port's controller hands out, whatever is written
// here (so the XUSB descriptor's 0x81/0x01 may not match them exactly).
func usbPadXboxFFSDescriptors(withXUSB bool) []byte {
	iface := []byte{9, 4, 0, 0, 2, 0xFF, 0x5D, 0x01, 0}
	build := func(inEvery, outEvery byte) []byte {
		var d []byte
		d = append(d, iface...)
		if withXUSB {
			d = append(d, usbPadXUSBDescriptor...)
		}
		d = append(d, usbPadEndpoint(0x81, 3, 32, inEvery)...)
		return append(d, usbPadEndpoint(0x02, 3, 32, outEvery)...)
	}
	count := uint32(3)
	if withXUSB {
		count = 4
	}
	// High speed counts in 125 µs microframes, 2^(n-1) of them: 4 is 1 ms,
	// 7 is 8 ms.
	return usbPadFFSBlob(build(1, 8), build(4, 7), count)
}

// ── Generic HID ───────────────────────────────────────────────────────────

// usbPadHIDReportLen is the generic report: 16 button bits, the hat in a
// nibble (8 = centred) and padding, then X, Y, Z and Rz as 0..255.
const usbPadHIDReportLen = 7

// usbPadHIDButtons is the order of the 16 buttons: the Linux gamepad order
// (hid-input maps button n to BTN_GAMEPAD + n - 1), which Android's generic
// layout follows too — 1 A, 2 B, 4 X, 5 Y, 7/8 the shoulders, 9/10 the
// triggers, 11 Select, 12 Start, 13 Home, 14/15 the stick clicks. 3, 6 and
// 16 are never pressed.
var usbPadHIDButtons = [16]uint32{
	usbPadA, usbPadB, 0, usbPadX, usbPadY, 0, usbPadLB, usbPadRB,
	0, 0, usbPadBack, usbPadStart, usbPadGuide, usbPadLS, usbPadRS, 0,
}

// usbPadHIDReportDescriptor describes usbPadHIDReport.
func usbPadHIDReportDescriptor() []byte {
	return []byte{
		0x05, 0x01, // Usage Page (Generic Desktop)
		0x09, 0x05, // Usage (Game Pad)
		0xA1, 0x01, // Collection (Application)
		0x05, 0x09, //   Usage Page (Button)
		0x19, 0x01, //   Usage Minimum (1)
		0x29, 0x10, //   Usage Maximum (16)
		0x15, 0x00, //   Logical Minimum (0)
		0x25, 0x01, //   Logical Maximum (1)
		0x75, 0x01, //   Report Size (1)
		0x95, 0x10, //   Report Count (16)
		0x81, 0x02, //   Input (Data, Variable, Absolute)
		0x05, 0x01, //   Usage Page (Generic Desktop)
		0x09, 0x39, //   Usage (Hat switch)
		0x15, 0x00, //   Logical Minimum (0)
		0x25, 0x07, //   Logical Maximum (7)
		0x35, 0x00, //   Physical Minimum (0)
		0x46, 0x3B, 0x01, //   Physical Maximum (315)
		0x65, 0x14, //   Unit (degrees)
		0x75, 0x04, //   Report Size (4)
		0x95, 0x01, //   Report Count (1)
		0x81, 0x42, //   Input (Data, Variable, Absolute, Null state)
		0x65, 0x00, //   Unit (none)
		0x45, 0x00, //   Physical Maximum (0: the axes use their logical range)
		0x75, 0x04, //   Report Size (4)
		0x95, 0x01, //   Report Count (1)
		0x81, 0x03, //   Input (Constant): padding
		0x09, 0x30, //   Usage (X)
		0x09, 0x31, //   Usage (Y)
		0x09, 0x32, //   Usage (Z)
		0x09, 0x35, //   Usage (Rz)
		0x15, 0x00, //   Logical Minimum (0)
		0x26, 0xFF, 0x00, //   Logical Maximum (255)
		0x75, 0x08, //   Report Size (8)
		0x95, 0x04, //   Report Count (4)
		0x81, 0x02, //   Input (Data, Variable, Absolute)
		0xC0, // End Collection
	}
}

// usbPadHIDDescriptor is the HID class descriptor (type 0x21) that names the
// report descriptor and its length.
func usbPadHIDDescriptor() []byte {
	n := len(usbPadHIDReportDescriptor())
	return []byte{9, 0x21, 0x11, 0x01, 0x00, 0x01, 0x22, byte(n), byte(n >> 8)}
}

// usbPadHIDFFSDescriptors is the HID interface for FunctionFS: class 3, no
// boot protocol, the HID descriptor, one interrupt pipe in every 1 ms.
func usbPadHIDFFSDescriptors() []byte {
	build := func(every byte) []byte {
		d := []byte{9, 4, 0, 0, 1, 3, 0, 0, 0}
		d = append(d, usbPadHIDDescriptor()...)
		return append(d, usbPadEndpoint(0x81, 3, 8, every)...)
	}
	return usbPadFFSBlob(build(1), build(4), 3)
}

// usbPadFFSNoStrings is an empty FunctionFS string table: the interfaces
// name no strings.
func usbPadFFSNoStrings() []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], 2) // FUNCTIONFS_STRINGS_MAGIC
	binary.LittleEndian.PutUint32(b[4:], 16)
	return b
}

// usbPadAxis8 maps a signed 16-bit axis to 0..255 with 128 at rest.
func usbPadAxis8(v int) byte {
	v = (v + 32768) >> 8
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(v)
}

// usbPadHat is the hat value for the D-pad bits: 0 up, clockwise to 7 up-left,
// 8 (outside the logical range) centred.
func usbPadHat(b uint32) byte {
	up, down := b&usbPadUp != 0, b&usbPadDown != 0
	left, right := b&usbPadLeft != 0, b&usbPadRight != 0
	switch {
	case up && right:
		return 1
	case down && right:
		return 3
	case down && left:
		return 5
	case up && left:
		return 7
	case up:
		return 0
	case right:
		return 2
	case down:
		return 4
	case left:
		return 6
	}
	return 8
}

// usbPadHIDReport packs s as the generic report (HID's Y grows downwards).
func usbPadHIDReport(s usbPadState) []byte {
	var btn uint16
	for i, m := range usbPadHIDButtons {
		if m != 0 && s.buttons&m != 0 {
			btn |= 1 << uint(i)
		}
	}
	if s.lt > 127 {
		btn |= 1 << 8
	}
	if s.rt > 127 {
		btn |= 1 << 9
	}
	return []byte{
		byte(btn), byte(btn >> 8), usbPadHat(s.buttons),
		usbPadAxis8(int(s.lx)), usbPadAxis8(-int(s.ly)), usbPadAxis8(int(s.rx)), usbPadAxis8(-int(s.ry)),
	}
}

// ── What the kernel offers ────────────────────────────────────────────────

// usbPadCaps is the capability check's answer.
type usbPadCaps struct {
	checked   bool
	configfs  bool   // the gadget framework is there
	udc       string // the port's device controller, "" for none
	hid       bool   // the kernel's HID gadget function can be made
	ffs       bool   // FunctionFS functions can be made
	kernel    string // the kernel release
	hidConfig string // CONFIG_USB_CONFIGFS_F_HID from /proc/config.gz: y, m, n or "" (unknown)
	ffsConfig string // CONFIG_USB_CONFIGFS_F_FS, the same way
	problem   string // why the check stopped early
}

// canXbox / canHID say whether a mode can start at all.
func (c usbPadCaps) canXbox() bool { return c.configfs && c.udc != "" && c.ffs }
func (c usbPadCaps) canHID() bool  { return c.configfs && c.udc != "" && (c.hid || c.ffs) }

// summary is the check in one plain log line.
func (c usbPadCaps) summary() string {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	s := "kernel " + c.kernel + ": gadget framework " + yes(c.configfs) + ", device controller " + yes(c.udc != "") +
		", HID function " + yes(c.hid) + ", FunctionFS " + yes(c.ffs)
	if c.hidConfig != "" || c.ffsConfig != "" {
		s += " (config: F_HID=" + c.hidConfig + ", F_FS=" + c.ffsConfig + ")"
	}
	if c.problem != "" {
		s += "; " + c.problem
	}
	return s
}

// usbPadProbeState caches the check: it is redone on each visit to the
// screen, on a goroutine, while controller mode is off.
var usbPadProbeState struct {
	mu      sync.Mutex
	running bool
	caps    usbPadCaps
	logged  string
}

// usbPadProbeAsync starts the check unless one is running or controller
// mode holds the port (its gadget would be in the way of the scratch one).
func usbPadProbeAsync() {
	if !usbPadSupported() || usbPadHolds.Load() {
		return
	}
	usbPadProbeState.mu.Lock()
	if usbPadProbeState.running {
		usbPadProbeState.mu.Unlock()
		return
	}
	usbPadProbeState.running = true
	usbPadProbeState.mu.Unlock()
	go func() {
		caps := usbPadProbe()
		caps.checked = true
		usbPadProbeState.mu.Lock()
		usbPadProbeState.running = false
		usbPadProbeState.caps = caps
		first := usbPadProbeState.logged != caps.summary()
		usbPadProbeState.logged = caps.summary()
		usbPadProbeState.mu.Unlock()
		if first {
			logInfo("usbpad", "controller mode check: %s", caps.summary())
		}
	}()
}

// usbPadCapsNow is the last check's answer.
func usbPadCapsNow() usbPadCaps {
	usbPadProbeState.mu.Lock()
	defer usbPadProbeState.mu.Unlock()
	return usbPadProbeState.caps
}

// ── Starting and stopping ─────────────────────────────────────────────────

// usbPadStartConfig is what the platform side needs to start.
type usbPadStartConfig struct {
	mode    string
	printed bool
	name    string // what the computer calls the controller
	serial  string
	// external: another part of JukaHub reads the pad and hands its state
	// to usbPadSubmit; the session then neither opens nor grabs the pad's
	// evdev node, and the leave chord is that reader's business.
	external bool
}

// usbPadRunInfo is how a start went: the way the reports travel and anything
// worth saying about it.
type usbPadRunInfo struct {
	transport string
	note      string
}

// Phases of controller mode.
const (
	usbPadIdle = iota
	usbPadStarting
	usbPadActive
	usbPadStopping
)

// usbPad is controller mode's state; mu guards it. usbPadOpMu serialises the
// slow start and stop.
var usbPad struct {
	mu         sync.Mutex
	phase      int
	gen        int
	mode       string
	printed    bool
	err        string
	transport  string
	note       string
	stop       func()
	restoreMTP bool
}

var usbPadOpMu sync.Mutex

// usbPadHolds is set while controller mode has (or is taking) the port: USB
// file transfer does not start then (usbmtp.go), and the stale-gadget
// cleanup leaves the port alone.
var usbPadHolds atomic.Bool

// usbPadQuitting is set by the exit hook: nothing is started again then.
var usbPadQuitting atomic.Bool

// usbPadLive is what the session reports, for the screen: any goroutine
// writes, the main thread reads.
var usbPadLive struct {
	mu        sync.Mutex
	state     usbPadState // as held (the tester)
	connected bool
	rumble    uint8
	chord     bool
	chordGen  uint64
	gen       uint64
	lastInput time.Time
	waitSince time.Time // when the last computer left (or the start)
}

func init() {
	// A previous run that died with controller mode on left the port to its
	// own gadget and the firmware's (ADB) unbound; give it back at startup,
	// on a goroutine — the gadget calls can take seconds.
	deferStartupTask("usb-gamepad-recover", func() {
		if usbPadSupported() {
			go usbPadClearStale()
		}
	})
	Cleanup = append(Cleanup, usbPadShutdown)
}

// usbPadSupported: the handheld only, like USB file transfer.
func usbPadSupported() bool { return runtime.GOOS == "linux" && IsTrimuiDevice() }

// usbPadTurnOn starts controller mode (main thread); the work runs on a
// goroutine and the screen follows usbPadSnapshot.
func usbPadTurnOn(config *Config, mode string, printed bool) {
	usbPadTurnOnWith(config, usbPadStartConfig{mode: mode, printed: printed})
}

// usbPadTurnOnWith is the start for any caller (main thread): the screen's,
// or one that reads the pad itself (cfg.external) and feeds usbPadSubmit.
// usbPadStopAsync ends either; usbPadSnapshot follows either.
func usbPadTurnOnWith(config *Config, cfg usbPadStartConfig) {
	usbPad.mu.Lock()
	if usbPad.phase != usbPadIdle {
		usbPad.mu.Unlock()
		return
	}
	usbPad.phase = usbPadStarting
	usbPad.gen++
	gen := usbPad.gen
	usbPad.mode, usbPad.printed, usbPad.err, usbPad.note, usbPad.transport = cfg.mode, cfg.printed, "", "", ""
	restore := usbMtpSupported() && usbMtpEnabled(config)
	usbPad.restoreMTP = restore
	usbPad.mu.Unlock()
	if restore {
		usbMtpNoteNetSwitch(config)
	}
	// The rumble goroutine drives the motor; find it here, on the main
	// thread, as the games mouse does.
	hapticsAvailable()
	if cfg.name == "" {
		cfg.name = usbMtpDeviceName()
	}
	usbPadLiveReset()
	go usbPadStartWork(gen, cfg, restore)
}

// usbPadSink is where usbPadSubmit's states go: the running session when it
// was started with external set (usbpad_linux.go sets and clears it).
var usbPadSink struct {
	mu sync.Mutex
	fn func(usbPadState)
}

func usbPadSetSink(fn func(usbPadState)) {
	usbPadSink.mu.Lock()
	usbPadSink.fn = fn
	usbPadSink.mu.Unlock()
}

// usbPadSubmit hands an external session the pad's state (any goroutine;
// never blocks: the newest state is packed and the sender writes it when the
// computer polls). Sticks are up and right positive (XInput's convention),
// triggers 0..255. A no-op when no external session runs.
func usbPadSubmit(st usbPadState) {
	usbPadSink.mu.Lock()
	fn := usbPadSink.fn
	usbPadSink.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

// usbPadStartWork takes the port: USB file transfer is stopped first (it is
// started again when controller mode ends), then the gadget is made.
func usbPadStartWork(gen int, cfg usbPadStartConfig, restore bool) {
	usbPadOpMu.Lock()
	defer usbPadOpMu.Unlock()
	usbPadHolds.Store(true)
	giveBack := func() {
		usbPadHolds.Store(false)
		if restore && !usbPadQuitting.Load() {
			usbMtpApply(true)
		}
	}
	fail := func(msg string) {
		giveBack()
		usbPad.mu.Lock()
		if usbPad.gen == gen {
			usbPad.phase = usbPadIdle
			usbPad.err = msg
		}
		usbPad.mu.Unlock()
		logWarn("usbpad", "controller mode not started: %s", msg)
	}
	if port := usbDetectPort(); port.Mode == "host" {
		fail("the USB-C port is in Host mode — set it to Device or Auto in Tools → USB & OTG first")
		return
	}
	usbMtp.mu.Lock()
	usbMtp.want = false
	usbMtp.mu.Unlock()
	usbMtpSettle()
	cfg.serial = usbMtpSerial()
	info, stop, err := usbPadPlatformStart(cfg)
	if err != nil {
		fail(firstLine(err.Error()))
		return
	}
	usbPad.mu.Lock()
	if usbPad.gen != gen || usbPad.phase != usbPadStarting {
		// Left the screen (or quitting) while the port was being set up.
		usbPad.mu.Unlock()
		stop()
		giveBack()
		return
	}
	usbPad.phase = usbPadActive
	usbPad.stop = stop
	usbPad.transport, usbPad.note = info.transport, info.note
	usbPad.mu.Unlock()
	logInfo("usbpad", "controller mode on: %s over %s", usbPadModeLabel(cfg.mode), info.transport)
}

// usbPadBeginStop moves controller mode to Stopping and hands back what is
// left to do; ok is false when it is off or already stopping.
func usbPadBeginStop() (stop func(), restore, wasActive, ok bool) {
	usbPad.mu.Lock()
	defer usbPad.mu.Unlock()
	if usbPad.phase == usbPadIdle || usbPad.phase == usbPadStopping {
		return nil, false, false, false
	}
	wasActive = usbPad.phase == usbPadActive
	// A start still under way sees the new generation and undoes itself.
	usbPad.gen++
	stop = usbPad.stop
	usbPad.stop = nil
	restore = usbPad.restoreMTP && wasActive
	usbPad.phase = usbPadStopping
	return stop, restore, wasActive, true
}

// usbPadFinishStop ends the session and gives the port back: the firmware's
// gadget is bound again and USB file transfer restarted if it was on.
func usbPadFinishStop(stop func(), restore, wasActive bool) {
	usbPadOpMu.Lock()
	if stop != nil {
		stop()
		usbPadHolds.Store(false)
	}
	usbPad.mu.Lock()
	usbPad.phase = usbPadIdle
	usbPad.mu.Unlock()
	usbPadOpMu.Unlock()
	usbPadLiveReset()
	if wasActive {
		logInfo("usbpad", "controller mode off; the USB port is given back")
	}
	if restore && !usbPadQuitting.Load() {
		usbMtpApply(true)
	}
}

// usbPadStopAsync stops controller mode on a goroutine (the screen's way)
// and reports whether there was anything to stop.
func usbPadStopAsync() bool {
	stop, restore, wasActive, ok := usbPadBeginStop()
	if ok {
		go usbPadFinishStop(stop, restore, wasActive)
	}
	return ok
}

// usbPadShutdown is the exit hook: off, within a bound, including a stop
// that was already under way.
func usbPadShutdown() {
	usbPadQuitting.Store(true)
	usbPadStopAsync()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		usbPad.mu.Lock()
		idle := usbPad.phase == usbPadIdle
		usbPad.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	logWarn("usbpad", "controller mode did not stop in time")
}

// usbPadSnap is the screen's copy of everything.
type usbPadSnap struct {
	phase      int
	mode       string
	printed    bool
	err        string
	transport  string
	note       string
	state      usbPadState
	connected  bool
	rumble     uint8
	chord      bool
	gen        uint64
	lastInput  time.Time
	waitSince  time.Time
	restoreMTP bool
}

func usbPadSnapshot() usbPadSnap {
	var s usbPadSnap
	usbPad.mu.Lock()
	s.phase, s.mode, s.printed, s.err = usbPad.phase, usbPad.mode, usbPad.printed, usbPad.err
	s.transport, s.note, s.restoreMTP = usbPad.transport, usbPad.note, usbPad.restoreMTP
	usbPad.mu.Unlock()
	usbPadLive.mu.Lock()
	s.state, s.connected, s.rumble, s.chord = usbPadLive.state, usbPadLive.connected, usbPadLive.rumble, usbPadLive.chord
	s.gen, s.lastInput, s.waitSince = usbPadLive.gen, usbPadLive.lastInput, usbPadLive.waitSince
	usbPadLive.mu.Unlock()
	return s
}

func usbPadLiveReset() {
	now := time.Now()
	usbPadLive.mu.Lock()
	usbPadLive.state = usbPadState{}
	usbPadLive.connected, usbPadLive.rumble, usbPadLive.chord = false, 0, false
	usbPadLive.chordGen++
	usbPadLive.gen++
	usbPadLive.lastInput, usbPadLive.waitSince = now, now
	usbPadLive.mu.Unlock()
}

// usbPadPublish records the pad's state for the screen (any goroutine) and
// times the leave chord: Select + Start held for usbPadLeaveHold ends
// controller mode.
func usbPadPublish(phys usbPadState, both bool) {
	now := time.Now()
	usbPadLive.mu.Lock()
	if phys != usbPadLive.state {
		usbPadLive.state = phys
		usbPadLive.gen++
		usbPadLive.lastInput = now
	}
	if both && !usbPadLive.chord {
		usbPadLive.chordGen++
		gen := usbPadLive.chordGen
		time.AfterFunc(usbPadLeaveHold, func() { usbPadChordCheck(gen) })
	}
	usbPadLive.chord = both
	usbPadLive.mu.Unlock()
}

// usbPadChordCheck fires when the chord has been held long enough.
func usbPadChordCheck(gen uint64) {
	usbPadLive.mu.Lock()
	fire := usbPadLive.chord && usbPadLive.chordGen == gen
	usbPadLive.mu.Unlock()
	if !fire {
		return
	}
	playHaptic(hapticBackPulse)
	logInfo("usbpad", "Select + Start held: leaving controller mode")
	tbxPost(func() {
		if usbPadStopAsync() {
			ShowToast("USB controller off — the USB port is the handheld's again", ToastKindInfo)
		}
	})
}

// usbPadSetConnected follows the computer (any goroutine).
func usbPadSetConnected(on bool) {
	usbPadLive.mu.Lock()
	if usbPadLive.connected != on {
		usbPadLive.connected = on
		usbPadLive.gen++
		if !on {
			usbPadLive.waitSince = time.Now()
			usbPadLive.rumble = 0
		}
	}
	usbPadLive.mu.Unlock()
}

// usbPadSetRumble records the rumble level the computer asked for.
func usbPadSetRumble(level uint8) {
	usbPadLive.mu.Lock()
	if usbPadLive.rumble != level {
		usbPadLive.rumble = level
		usbPadLive.gen++
	}
	usbPadLive.mu.Unlock()
}

// usbPadMotor drives the handheld's vibration motor for the computer's rumble
// (0 stops it). Only the TrimUI's GPIO motor is driven here — it is the one
// a goroutine may touch — and the Vibration switch in Settings silences it
// too. A pulse lasts 300 ms; the caller renews it while rumble goes on.
func usbPadMotor(level uint8) {
	// Under hapticsMu, as playHaptic drives it from the main thread: the
	// motor's own fields are not guarded against two callers otherwise.
	hapticsMu.Lock()
	defer hapticsMu.Unlock()
	m, _ := hapticDev.(*sysfsMotor)
	if m == nil {
		return
	}
	if level == 0 || !hapticOn.Load() {
		_ = m.stop()
		return
	}
	// A coin motor does not spin at the bottom of its range.
	strength := 0.35 + 0.65*float32(level)/255
	_ = m.play(strength, 300)
}

// usbPadModeLabel names a mode the way the screen does.
func usbPadModeLabel(mode string) string {
	if mode == usbPadModeHID {
		return "Generic HID gamepad"
	}
	return "PC (Xbox 360 compatible)"
}

// ── Screen brightness ─────────────────────────────────────────────────────

// usbPadDimmed is set while controller mode has the screen at its floor.
// Main thread.
var usbPadDimmed bool

// usbPadDim takes the screen to its dimmest level for a moment, the way the
// QR card borrows full brightness (brightnessBorrow): nothing is saved, and
// brightnessGiveBack (or quitting) puts the user's level back.
func usbPadDim() {
	if usbPadDimmed || brightnessKnownBackend() == "" || brightnessBorrowed.Load() {
		return
	}
	floor := brightnessFloor()
	brightnessLevel.mu.Lock()
	cur, known := brightnessLevel.pct, brightnessLevel.known
	if !known || cur <= floor {
		brightnessLevel.mu.Unlock()
		return
	}
	brightnessLevel.editGen++
	gen := brightnessLevel.editGen
	brightnessLevel.mu.Unlock()
	brightnessBorrowed.Store(true)
	brightnessRequestLevel(floor, gen)
	usbPadDimmed = true
}

// usbPadUndim gives the screen its level back.
func usbPadUndim() {
	if !usbPadDimmed {
		return
	}
	usbPadDimmed = false
	brightnessGiveBack()
}

// ── Settings ──────────────────────────────────────────────────────────────

// usbPadSettingRow opens the screen from Settings → Network, under USB file
// transfer.
func usbPadSettingRow() setRow {
	return setRow{
		ID: "usbGamepad", Label: "USB controller", Kind: setKindAction,
		Visible: func(*Config) bool { return usbPadSupported() },
		Detail:  func(*Config) string { return usbPadTileValue() },
		Open:    usbPadOpenScreen,
	}
}

// usbPadTileValue is the one-line state for the Tools hub and Settings.
func usbPadTileValue() string {
	s := usbPadSnapshot()
	switch {
	case s.phase == usbPadActive && s.connected:
		return "On · connected to a computer"
	case s.phase == usbPadActive:
		return "On · waiting for a computer"
	case s.phase == usbPadStarting:
		return "Starting…"
	}
	return "Use the handheld as a controller on a computer"
}
