package main

// vcursor_evdev.go — the device-free half of "Mouse in games and apps".
//
// While a game, an emulator or a port owns the screen, JukaHub's own event
// loop is parked (gamesuspend.go), and SDL cannot put a pointer into another
// program. The kernel can: the bridge (vcursor_bridge_linux.go) reads the
// pad's evdev node beside the game and, on the mouse-mode chord, grabs it
// (EVIOCGRAB, so the game stops seeing the buttons) and creates a mouse
// through /dev/uinput that the pad then drives — the way PortMaster's
// gptokeyb gives ports a mouse.
//
// Everything here is bytes and arithmetic, so it builds and is tested on
// every platform: the Linux input structures, the ioctl numbers, the input
// device list, and the engine that turns pad events into mouse events. The
// engine uses the in-app pointer's speed curve, D-pad ramp, scroll pacing and
// chord (vcursor_mode.go), so the pad feels the same in a game as in JukaHub.

import (
	"encoding/binary"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Linux input event types and codes (include/uapi/linux/input-event-codes.h).
const (
	evdevEvSyn = 0x00
	evdevEvKey = 0x01
	evdevEvRel = 0x02
	evdevEvAbs = 0x03

	evdevSynReport  = 0x00
	evdevSynDropped = 0x03

	evdevRelX     = 0x00
	evdevRelY     = 0x01
	evdevRelWheel = 0x08

	evdevBtnLeft   = 0x110
	evdevBtnRight  = 0x111
	evdevBtnMiddle = 0x112

	evdevBtnSouth  = 0x130
	evdevBtnEast   = 0x131
	evdevBtnNorth  = 0x133
	evdevBtnWest   = 0x134
	evdevBtnTL     = 0x136
	evdevBtnTR     = 0x137
	evdevBtnSelect = 0x13a
	evdevBtnStart  = 0x13b
	evdevBtnMode   = 0x13c

	// Keyboard keys the Windows-app layout sends.
	evdevKeyEsc     = 1
	evdevKeyTab     = 15
	evdevKeyEnter   = 28
	evdevKeyLeftAlt = 56
	evdevKeySpace   = 57
	evdevKeyF4      = 62
	evdevKeyUp      = 103
	evdevKeyLeft    = 105
	evdevKeyRight   = 106
	evdevKeyDown    = 108
	// evdevKeyLast is the last keyboard key a full keyboard declares (the
	// codes from 1 up to here; mouse and pad buttons start at 0x100).
	evdevKeyLast = 255

	evdevAbsX     = 0x00
	evdevAbsY     = 0x01
	evdevAbsZ     = 0x02
	evdevAbsRX    = 0x03
	evdevAbsRY    = 0x04
	evdevAbsRZ    = 0x05
	evdevAbsHat0X = 0x10
	evdevAbsHat0Y = 0x11

	evdevBusVirtual = 0x06
)

// Sizes of the kernel structures, for this build's word size.
const (
	// evdevEventSize is sizeof(struct input_event): a struct timeval (two C
	// longs), then __u16 type, __u16 code and __s32 value — 24 bytes on a
	// 64-bit kernel such as the Smart Pro's.
	evdevEventSize = 2*(bits.UintSize/8) + 8
	// evdevAbsInfoSize is sizeof(struct input_absinfo): value, minimum,
	// maximum, fuzz, flat and resolution, six __s32.
	evdevAbsInfoSize = 24
	// evdevNameSize is UINPUT_MAX_NAME_SIZE.
	evdevNameSize = 80
	// evdevSetupSize is sizeof(struct uinput_setup): a struct input_id
	// (bustype, vendor, product, version: four __u16), char name[80] and
	// __u32 ff_effects_max.
	evdevSetupSize = 8 + evdevNameSize + 4
	// evdevUserDevSize is sizeof(struct uinput_user_dev), the description a
	// kernel older than 4.5 reads with write(): char name[80], struct
	// input_id, __u32 ff_effects_max, then absmax, absmin, absfuzz and
	// absflat, each __s32[ABS_CNT] with ABS_CNT = 64.
	evdevUserDevSize = evdevNameSize + 8 + 4 + 4*64*4
	// evdevKeyBitmapSize is the EVIOCGKEY answer: (KEY_MAX + 1) / 8 bytes,
	// KEY_MAX being 0x2ff.
	evdevKeyBitmapSize = (0x2ff + 1) / 8
)

// ioctl directions (include/uapi/asm-generic/ioctl.h).
const (
	evdevIOCNone  = 0
	evdevIOCWrite = 1
	evdevIOCRead  = 2
)

// evdevIOC builds an ioctl request number the asm-generic way, which arm64
// and x86 both use: the direction in bits 30-31, the argument's size in bits
// 16-29, the driver's letter in bits 8-15 and the command in bits 0-7.
func evdevIOC(dir, typ, nr, size uint32) uint32 {
	return dir<<30 | size<<16 | typ<<8 | nr
}

// The requests the bridge sends.
var (
	// EVIOCGRAB = _IOW('E', 0x90, int): 1 grabs the device for this open
	// file alone (every other reader, the game included, stops getting its
	// events), 0 lets go. The int travels by value, not through a pointer.
	evdevGrab = evdevIOC(evdevIOCWrite, 'E', 0x90, 4)
	// UI_DEV_CREATE = _IO('U', 1) and UI_DEV_DESTROY = _IO('U', 2) add and
	// remove the uinput device.
	evdevUIDevCreate  = evdevIOC(evdevIOCNone, 'U', 1, 0)
	evdevUIDevDestroy = evdevIOC(evdevIOCNone, 'U', 2, 0)
	// UI_DEV_SETUP = _IOW('U', 3, struct uinput_setup): the device's name and
	// ids (Linux 4.5 and later).
	evdevUIDevSetup = evdevIOC(evdevIOCWrite, 'U', 3, evdevSetupSize)
	// UI_SET_EVBIT, UI_SET_KEYBIT, UI_SET_RELBIT = _IOW('U', 100 / 101 / 102,
	// int): the event types and codes the device sends, each by value.
	evdevUISetEvBit  = evdevIOC(evdevIOCWrite, 'U', 100, 4)
	evdevUISetKeyBit = evdevIOC(evdevIOCWrite, 'U', 101, 4)
	evdevUISetRelBit = evdevIOC(evdevIOCWrite, 'U', 102, 4)
)

// evdevGetAbs is EVIOCGABS(axis) = _IOR('E', 0x40 + axis, struct
// input_absinfo): one axis's value and range.
func evdevGetAbs(axis uint16) uint32 {
	return evdevIOC(evdevIOCRead, 'E', 0x40+uint32(axis), evdevAbsInfoSize)
}

// evdevGetKey is EVIOCGKEY(len) = _IOC(_IOC_READ, 'E', 0x18, len): the keys
// held right now, as a bitmap of len bytes.
func evdevGetKey(n uint32) uint32 {
	return evdevIOC(evdevIOCRead, 'E', 0x18, n)
}

// ── Events ────────────────────────────────────────────────────────────────

// evdevEvent is one struct input_event without its time stamp, which the
// kernel sets itself (and ignores in what is written to it).
type evdevEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

// evdevEncode appends ev the way the kernel reads it from a write(): the time
// stamp zero, then type, code and value, little-endian like every Linux
// device JukaHub runs on.
func evdevEncode(dst []byte, ev evdevEvent) []byte {
	n := len(dst)
	for i := 0; i < evdevEventSize; i++ {
		dst = append(dst, 0)
	}
	rec := dst[n+evdevEventSize-8:]
	binary.LittleEndian.PutUint16(rec[0:], ev.Type)
	binary.LittleEndian.PutUint16(rec[2:], ev.Code)
	binary.LittleEndian.PutUint32(rec[4:], uint32(ev.Value))
	return dst
}

// evdevDecode appends the whole events in b (what a read() returned) to out;
// a short tail is left out.
func evdevDecode(b []byte, out []evdevEvent) []evdevEvent {
	for len(b) >= evdevEventSize {
		rec := b[evdevEventSize-8 : evdevEventSize]
		out = append(out, evdevEvent{
			Type:  binary.LittleEndian.Uint16(rec[0:]),
			Code:  binary.LittleEndian.Uint16(rec[2:]),
			Value: int32(binary.LittleEndian.Uint32(rec[4:])),
		})
		b = b[evdevEventSize:]
	}
	return out
}

// evdevBool is a key event's value for down / up.
func evdevBool(down bool) int32 {
	if down {
		return 1
	}
	return 0
}

// evdevSetupBytes is struct uinput_setup for UI_DEV_SETUP: the input_id, the
// name (cut to fit, always NUL-terminated) and no force feedback.
func evdevSetupBytes(name string, bus, vendor, product, version uint16) []byte {
	b := make([]byte, evdevSetupSize)
	binary.LittleEndian.PutUint16(b[0:], bus)
	binary.LittleEndian.PutUint16(b[2:], vendor)
	binary.LittleEndian.PutUint16(b[4:], product)
	binary.LittleEndian.PutUint16(b[6:], version)
	copy(b[8:8+evdevNameSize-1], name)
	return b
}

// evdevUserDevBytes is the legacy struct uinput_user_dev with the same
// content; the axis arrays stay zero (the mouse has no absolute axes).
func evdevUserDevBytes(name string, bus, vendor, product, version uint16) []byte {
	b := make([]byte, evdevUserDevSize)
	copy(b[:evdevNameSize-1], name)
	binary.LittleEndian.PutUint16(b[evdevNameSize:], bus)
	binary.LittleEndian.PutUint16(b[evdevNameSize+2:], vendor)
	binary.LittleEndian.PutUint16(b[evdevNameSize+4:], product)
	binary.LittleEndian.PutUint16(b[evdevNameSize+6:], version)
	return b
}

// evdevKeyBit reports whether code is set in an EVIOCGKEY bitmap (bit code%8
// of byte code/8: the kernel's longs, little-endian, read byte by byte).
func evdevKeyBit(bitmap []byte, code uint16) bool {
	i := int(code) / 8
	return i < len(bitmap) && bitmap[i]&(1<<(code%8)) != 0
}

// ── Axes ──────────────────────────────────────────────────────────────────

// evdevAbsRange is the part of an axis's struct input_absinfo the bridge
// uses. A zero range is an axis the pad does not have (the Brick's sticks).
type evdevAbsRange struct {
	min, max int32
}

// evdevParseAbsInfo reads an EVIOCGABS answer: the axis's value now and its
// range; ok is false for a short answer or an empty range.
func evdevParseAbsInfo(b []byte) (value int32, r evdevAbsRange, ok bool) {
	if len(b) < evdevAbsInfoSize {
		return 0, evdevAbsRange{}, false
	}
	value = int32(binary.LittleEndian.Uint32(b[0:]))
	r.min = int32(binary.LittleEndian.Uint32(b[4:]))
	r.max = int32(binary.LittleEndian.Uint32(b[8:]))
	return value, r, r.valid()
}

// valid reports a usable range.
func (r evdevAbsRange) valid() bool { return r.max > r.min }

// stick reads a stick axis as -1..1 around the middle of its range.
func (r evdevAbsRange) stick(v int32) float64 {
	if !r.valid() {
		return 0
	}
	mid := (float64(r.min) + float64(r.max)) / 2
	half := (float64(r.max) - float64(r.min)) / 2
	return clampFloat((float64(v)-mid)/half, -1, 1)
}

// trigger reads a trigger axis as 0..1 from the bottom of its range.
func (r evdevAbsRange) trigger(v int32) float64 {
	if !r.valid() {
		return 0
	}
	return clampFloat((float64(v)-float64(r.min))/(float64(r.max)-float64(r.min)), 0, 1)
}

// evdevHatDir is a D-pad hat value as a step: -1, 0 or 1.
func evdevHatDir(v int32) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}

// ── The device list ───────────────────────────────────────────────────────

// evdevDevicesPath lists the kernel's input devices.
const evdevDevicesPath = "/proc/bus/input/devices"

// evdevPadName is the TrimUI firmware's virtual pad (a uinput device of its
// own, X360-shaped: inputhints_test.go has a real entry).
const evdevPadName = "TRIMUI Player1"

// mbridgeMouseName is the bridge's own mouse, which is never taken for a pad.
const mbridgeMouseName = "JukaHub Mouse"

// evdevDevice is one block of /proc/bus/input/devices. The bitmaps hold the
// least significant 64-bit word first.
type evdevDevice struct {
	Name    string
	Handler string // "event3": the node under /dev/input
	EV      []uint64
	Key     []uint64
	Abs     []uint64
}

// evdevHas reports whether bit is set in a bitmap.
func evdevHas(bitmap []uint64, bit int) bool {
	w := bit / 64
	return bit >= 0 && w < len(bitmap) && bitmap[w]&(1<<(uint(bit)%64)) != 0
}

// evdevParseBitmap reads a "B: KEY=" value: hex words, most significant first.
func evdevParseBitmap(s string) []uint64 {
	words := strings.Fields(s)
	out := make([]uint64, len(words))
	for i, w := range words {
		v, err := strconv.ParseUint(w, 16, 64)
		if err != nil {
			return nil
		}
		out[len(words)-1-i] = v
	}
	return out
}

// evdevHandlerOK accepts an event node name: "event" and a number, nothing a
// path could be made of.
func evdevHandlerOK(h string) bool {
	digits := strings.TrimPrefix(h, "event")
	if digits == h || digits == "" || len(digits) > 4 {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// evdevParseDevices reads /proc/bus/input/devices.
func evdevParseDevices(text string) []evdevDevice {
	var out []evdevDevice
	var cur evdevDevice
	flush := func() {
		if cur.Name != "" || cur.Handler != "" {
			out = append(out, cur)
		}
		cur = evdevDevice{}
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "N: Name="):
			cur.Name = strings.Trim(strings.TrimPrefix(line, "N: Name="), "\"")
		case strings.HasPrefix(line, "H: Handlers="):
			for _, h := range strings.Fields(strings.TrimPrefix(line, "H: Handlers=")) {
				if evdevHandlerOK(h) {
					cur.Handler = h
				}
			}
		case strings.HasPrefix(line, "B: EV="):
			cur.EV = evdevParseBitmap(strings.TrimPrefix(line, "B: EV="))
		case strings.HasPrefix(line, "B: KEY="):
			cur.Key = evdevParseBitmap(strings.TrimPrefix(line, "B: KEY="))
		case strings.HasPrefix(line, "B: ABS="):
			cur.Abs = evdevParseBitmap(strings.TrimPrefix(line, "B: ABS="))
		}
	}
	flush()
	return out
}

// evdevPickPad chooses the pad to lend: the firmware's own by name, else the
// first device that looks like a gamepad (keys and axes, with BTN_SOUTH and
// BTN_SELECT). The bridge's own mouse is never chosen.
func evdevPickPad(devs []evdevDevice) (evdevDevice, bool) {
	for _, d := range devs {
		if d.Name == evdevPadName && d.Handler != "" {
			return d, true
		}
	}
	for _, d := range devs {
		if d.Handler == "" || d.Name == mbridgeMouseName {
			continue
		}
		if evdevHas(d.EV, evdevEvKey) && evdevHas(d.EV, evdevEvAbs) &&
			evdevHas(d.Key, evdevBtnSouth) && evdevHas(d.Key, evdevBtnSelect) {
			return d, true
		}
	}
	return evdevDevice{}, false
}

// ── The engine ────────────────────────────────────────────────────────────

// mbridgeParams is what the games bridge takes from the settings, read on the
// main thread when it starts (mbridgeParamsFor): the evdev buttons that are
// A, B, X and Y under the face-button swap, and the sticks' calibration.
// keys turns on the Windows-app layout (a Wine session, wineInputStart): the
// pad also types the few keys a Windows program needs without a keyboard.
type mbridgeParams struct {
	codeA, codeB, codeX, codeY uint16
	calL, calR                 stickCal
	keys                       bool
}

const (
	// mbridgeKeyBase is BTN_SOUTH: the pad's buttons are 0x130 to 0x13f.
	mbridgeKeyBase = 0x130
	// mbridgeQuiet is how long after the mouse came on a press of Select or
	// L1 is taken for the firmware re-sending a button the user still holds
	// (the bridge has just told the game it was let go), not a new press.
	mbridgeQuiet = 300 * time.Millisecond
	// mbridgeTrigDown / mbridgeTrigUp latch L2 / R2 into wheel presses.
	mbridgeTrigDown = 0.5
	mbridgeTrigUp   = 0.3
	// mbridgeQuitHold is how long Select + Start are held in a Windows
	// program before JukaHub ends it (a tap only sends Alt+F4).
	mbridgeQuitHold = 2 * time.Second
)

// evdevMouseButtons are the virtual mouse's buttons: left, right, middle.
var evdevMouseButtons = [3]uint16{evdevBtnLeft, evdevBtnRight, evdevBtnMiddle}

// The keys of the Windows-app layout, as indexes into mbridgeKeyCodes and the
// engine's kbd.
const (
	mbKeyEsc = iota
	mbKeyTab
	mbKeyEnter
	mbKeySpace
	mbKeyUp
	mbKeyDown
	mbKeyLeft
	mbKeyRight
	mbKeyAlt
	mbKeyF4
	mbKeyCount
)

// mbridgeKeyCodes are the evdev codes of the layout's keys, in mbKey order.
var mbridgeKeyCodes = [mbKeyCount]uint16{
	evdevKeyEsc, evdevKeyTab, evdevKeyEnter, evdevKeySpace,
	evdevKeyUp, evdevKeyDown, evdevKeyLeft, evdevKeyRight,
	evdevKeyLeftAlt, evdevKeyF4,
}

// mbridgeEngine turns the pad's evdev events into mouse events. It does no
// I/O: the bridge feeds it what it read and writes out what it returns.
//
// ranges and abs are each axis's range and value (ABS_X to ABS_HAT0Y), keys
// the pad's buttons (BTN_SOUTH to 0x13f), chord the Select + L1 hold, quiet
// the end of the window after the mouse came on, on whether the pad is the
// mouse, btn the mouse's buttons held down (left, right, middle), acc the
// motion not sent yet (under a count), dpadAt when the D-pad started moving
// the pointer, trig and trigAt the L2 / R2 wheel latch and its next repeat,
// last the previous tick. The Windows-app layout (p.keys) adds kbd, the keys
// held down on the virtual device; combo, Start pressed while Select was held
// (Alt+F4 instead of Enter); quitAt, when that hold started; and quit, set
// once the hold lasted mbridgeQuitHold, for the bridge to end the program.
type mbridgeEngine struct {
	p      mbridgeParams
	ranges [18]evdevAbsRange
	abs    [18]int32
	keys   [16]bool
	chord  vcursorChordState
	quiet  time.Time
	on     bool
	btn    [3]bool
	acc    [2]float64
	dpadAt time.Time
	trig   [2]bool
	trigAt time.Time
	last   time.Time
	kbd    [mbKeyCount]bool
	combo  bool
	quitAt time.Time
	quit   bool
}

// setRange records an axis's range and value (EVIOCGABS when the pad opens).
func (g *mbridgeEngine) setRange(axis uint16, r evdevAbsRange, value int32) {
	if int(axis) < len(g.ranges) {
		g.ranges[axis] = r
		g.abs[axis] = value
	}
}

// key reports whether a pad button is held.
func (g *mbridgeEngine) key(code uint16) bool {
	i := int(code) - mbridgeKeyBase
	return i >= 0 && i < len(g.keys) && g.keys[i]
}

// setKeys loads the buttons held now (EVIOCGKEY when the pad opens, and after
// the kernel dropped events). A chord already held does not fire from it: it
// has to be pressed again.
func (g *mbridgeEngine) setKeys(bitmap []byte) {
	for i := range g.keys {
		g.keys[i] = evdevKeyBit(bitmap, uint16(mbridgeKeyBase+i))
	}
	sel, l1 := g.key(evdevBtnSelect), g.key(evdevBtnTL)
	g.chord = vcursorChordState{held: [2]bool{sel, l1}, fired: sel && l1}
}

// reconcileButtons lets go of every mouse button whose pad button is no
// longer held (after setKeys read the pad whole: the release was among the
// events the kernel dropped).
func (g *mbridgeEngine) reconcileButtons(out []evdevEvent) []evdevEvent {
	for b, code := range [3]uint16{g.p.codeA, g.p.codeB, g.p.codeX} {
		if g.btn[b] && !g.key(code) {
			g.btn[b] = false
			out = append(out, evdevEvent{Type: evdevEvKey, Code: evdevMouseButtons[b], Value: 0})
		}
	}
	if g.p.keys && g.on {
		out = g.reconcileKeys(out)
	}
	return out
}

// reconcileKeys is reconcileButtons for the Windows-app layout: a key whose
// button is no longer held is let go, Alt+F4 never stays down, and the arrows
// follow the D-pad as it is now.
func (g *mbridgeEngine) reconcileKeys(out []evdevEvent) []evdevEvent {
	for _, src := range [...]struct {
		key  int
		code uint16
	}{{mbKeyEsc, g.p.codeY}, {mbKeyTab, evdevBtnTL}, {mbKeyEnter, evdevBtnStart}, {mbKeySpace, evdevBtnTR}} {
		if g.kbd[src.key] && !g.key(src.code) {
			out = g.keyEvent(src.key, false, out)
		}
	}
	out = g.keyEvent(mbKeyF4, false, out)
	out = g.keyEvent(mbKeyAlt, false, out)
	if g.combo && !(g.key(evdevBtnSelect) && g.key(evdevBtnStart)) {
		g.combo, g.quitAt = false, time.Time{}
	}
	return g.syncArrows(out)
}

// keyEvent puts one of the layout's keys down or up, adding the event only
// when that changes it.
func (g *mbridgeEngine) keyEvent(k int, down bool, out []evdevEvent) []evdevEvent {
	if g.kbd[k] == down {
		return out
	}
	g.kbd[k] = down
	return append(out, evdevEvent{Type: evdevEvKey, Code: mbridgeKeyCodes[k], Value: evdevBool(down)})
}

// dpadKeys reports whether the D-pad types arrow keys rather than moving the
// pointer: the Windows-app layout on a pad with a stick to point with. The
// Brick, which has none, keeps the D-pad as its pointer.
func (g *mbridgeEngine) dpadKeys() bool {
	return g.p.keys && (g.ranges[evdevAbsX].valid() || g.ranges[evdevAbsRX].valid())
}

// syncArrows makes the arrow keys match the D-pad (all up when the D-pad is
// not typing them).
func (g *mbridgeEngine) syncArrows(out []evdevEvent) []evdevEvent {
	on := g.on && g.dpadKeys()
	x, y := evdevHatDir(g.abs[evdevAbsHat0X]), evdevHatDir(g.abs[evdevAbsHat0Y])
	out = g.keyEvent(mbKeyLeft, on && x < 0, out)
	out = g.keyEvent(mbKeyRight, on && x > 0, out)
	out = g.keyEvent(mbKeyUp, on && y < 0, out)
	return g.keyEvent(mbKeyDown, on && y > 0, out)
}

// letGo appends a release for every mouse button and key the virtual device
// holds down, for the moment the pad is given back.
func (g *mbridgeEngine) letGo(out []evdevEvent) []evdevEvent {
	for i, held := range g.btn {
		if held {
			out = append(out, evdevEvent{Type: evdevEvKey, Code: evdevMouseButtons[i], Value: 0})
		}
	}
	for i, held := range g.kbd {
		if held {
			out = append(out, evdevEvent{Type: evdevEvKey, Code: mbridgeKeyCodes[i], Value: 0})
		}
	}
	return out
}

// layoutKey is the Windows-app layout for one button while the pad is the
// mouse: Y is Escape, Start is Enter, L1 is Tab, R1 is Space. Start pressed
// while Select is held is Alt+F4 instead (close the window), and holding that
// pair for mbridgeQuitHold asks the bridge to end the program (tick). L1
// pressed while Select is held belongs to the Select + L1 chord.
func (g *mbridgeEngine) layoutKey(code uint16, down, selectHeld bool, now time.Time, out []evdevEvent) []evdevEvent {
	switch code {
	case g.p.codeY:
		return g.keyEvent(mbKeyEsc, down, out)
	case evdevBtnTR:
		return g.keyEvent(mbKeySpace, down, out)
	case evdevBtnTL:
		if down && selectHeld {
			return out
		}
		return g.keyEvent(mbKeyTab, down, out)
	case evdevBtnStart:
		if down && selectHeld {
			g.combo, g.quitAt = true, now
			out = g.keyEvent(mbKeyAlt, true, out)
			out = g.keyEvent(mbKeyF4, true, out)
			// A frame of its own for the press, so nothing downstream folds
			// the press and the release of F4 into one.
			out = append(out, evdevEvent{Type: evdevEvSyn, Code: evdevSynReport})
			out = g.keyEvent(mbKeyF4, false, out)
			return g.keyEvent(mbKeyAlt, false, out)
		}
		if !down && g.combo {
			g.combo, g.quitAt = false, time.Time{}
			return out
		}
		return g.keyEvent(mbKeyEnter, down, out)
	case evdevBtnSelect:
		if !down && g.combo {
			g.combo, g.quitAt = false, time.Time{}
		}
	}
	return out
}

// mouseButton is the mouse button a pad button is while the pad is the mouse:
// A the left one (click, hold to drag), B the right one, X the middle one; -1
// for any other.
func (g *mbridgeEngine) mouseButton(code uint16) int {
	switch code {
	case g.p.codeA:
		return 0
	case g.p.codeB:
		return 1
	case g.p.codeX:
		return 2
	}
	return -1
}

// feed takes one event read from the pad and appends to out the mouse events
// it makes (a button, while the pad is the mouse). off reports Menu pressed
// while the pad is the mouse: the way back that needs no chord.
func (g *mbridgeEngine) feed(ev evdevEvent, now time.Time, out []evdevEvent) ([]evdevEvent, bool) {
	switch ev.Type {
	case evdevEvKey:
		return g.feedKey(ev, now, out)
	case evdevEvAbs:
		if int(ev.Code) >= len(g.abs) {
			return out, false
		}
		hat := ev.Code == evdevAbsHat0X || ev.Code == evdevAbsHat0Y
		idle := g.abs[evdevAbsHat0X] == 0 && g.abs[evdevAbsHat0Y] == 0
		g.abs[ev.Code] = ev.Value
		if hat && idle && ev.Value != 0 {
			// The D-pad starts moving the pointer: its speed ramp starts.
			g.dpadAt = now
		}
		if hat && g.on && g.p.keys {
			// Or it types the arrow keys (dpadKeys).
			out = g.syncArrows(out)
		}
	}
	return out, false
}

// feedKey is feed for a button.
func (g *mbridgeEngine) feedKey(ev evdevEvent, now time.Time, out []evdevEvent) ([]evdevEvent, bool) {
	i := int(ev.Code) - mbridgeKeyBase
	if i < 0 || i >= len(g.keys) || ev.Value == 2 {
		// Not one of the pad's buttons, or a key repeat.
		return out, false
	}
	down := ev.Value != 0
	// Whether Select was already held when this button moved: L1 and Start
	// mean something else with it (layoutKey).
	selectHeld := g.key(evdevBtnSelect)
	g.keys[i] = down
	off := false
	switch ev.Code {
	case evdevBtnSelect, evdevBtnTL:
		btn := vcursorChordSelect
		if ev.Code == evdevBtnTL {
			btn = vcursorChordL1
		}
		// Right after the mouse came on, a press of a chord button is the
		// firmware re-sending one that is still held, not a new press.
		if !down || !now.Before(g.quiet) {
			g.chord.press(btn, down, now)
		}
	case evdevBtnMode:
		off = down && g.on
	}
	if g.on {
		if b := g.mouseButton(ev.Code); b >= 0 && g.btn[b] != down {
			g.btn[b] = down
			out = append(out, evdevEvent{Type: evdevEvKey, Code: evdevMouseButtons[b], Value: evdevBool(down)})
		}
		if g.p.keys {
			out = g.layoutKey(ev.Code, down, selectHeld, now, out)
		}
	}
	return out, off
}

// tick advances the engine to now: the chord's hold timer and, while the pad
// is the mouse, the pointer's motion from the sticks and the D-pad (whole
// counts; the rest waits for the next tick) and the wheel from L2 / R2.
// toggle reports the chord completing.
func (g *mbridgeEngine) tick(now time.Time, out []evdevEvent) ([]evdevEvent, bool) {
	toggle := g.chord.tick(now)
	dt := clampFloat(now.Sub(g.last).Seconds(), 0, 0.05)
	g.last = now
	if !g.on {
		return out, toggle
	}
	if g.combo && !g.quitAt.IsZero() && now.Sub(g.quitAt) >= mbridgeQuitHold &&
		g.key(evdevBtnSelect) && g.key(evdevBtnStart) {
		// Select + Start held: the window did not close, so the program ends.
		g.quit, g.quitAt = true, time.Time{}
	}
	vx, vy := g.velocity(now)
	g.acc[0] += vx * dt
	g.acc[1] += vy * dt
	if n := int32(g.acc[0]); n != 0 {
		out = append(out, evdevEvent{Type: evdevEvRel, Code: evdevRelX, Value: n})
		g.acc[0] -= float64(n)
	}
	if n := int32(g.acc[1]); n != 0 {
		out = append(out, evdevEvent{Type: evdevEvRel, Code: evdevRelY, Value: n})
		g.acc[1] -= float64(n)
	}
	return g.wheel(now, out), toggle
}

// stick reads a stick axis as -1..1 (0 for an axis the pad does not have).
func (g *mbridgeEngine) stick(axis uint16) float64 {
	return g.ranges[axis].stick(g.abs[axis])
}

// velocity is the pointer's velocity from both sticks, through their
// calibration, and the D-pad — the in-app pointer's (vcursor_mode.go).
func (g *mbridgeEngine) velocity(now time.Time) (float64, float64) {
	lx, ly := g.p.calL.shape(g.stick(evdevAbsX), g.stick(evdevAbsY))
	rx, ry := g.p.calR.shape(g.stick(evdevAbsRX), g.stick(evdevAbsRY))
	vx, vy := vcursorStickVelocity(lx, ly)
	ax, ay := vcursorStickVelocity(rx, ry)
	if g.dpadKeys() {
		// The D-pad types arrows instead (dpadKeys).
		return vcursorClampSpeed(vx+ax, vy+ay)
	}
	dx, dy := evdevHatDir(g.abs[evdevAbsHat0X]), evdevHatDir(g.abs[evdevAbsHat0Y])
	bx, by := vcursorDpadVelocity(dx, dy, now.Sub(g.dpadAt))
	return vcursorClampSpeed(vx+ax+bx, vy+ay+by)
}

// wheel turns L2 / R2 into wheel notches, up for L2 and down for R2: one on
// the press, then one every vcursorScrollRepeat after vcursorScrollDelay
// while held — the in-app pace.
func (g *mbridgeEngine) wheel(now time.Time, out []evdevEvent) []evdevEvent {
	for side, axis := range [2]uint16{evdevAbsZ, evdevAbsRZ} {
		v := g.ranges[axis].trigger(g.abs[axis])
		switch {
		case !g.trig[side] && v >= mbridgeTrigDown:
			g.trig[side] = true
			out = append(out, evdevWheelNotch(side))
			g.trigAt = now.Add(vcursorScrollDelay)
		case g.trig[side] && v < mbridgeTrigUp:
			g.trig[side] = false
		}
	}
	if (g.trig[0] || g.trig[1]) && !now.Before(g.trigAt) {
		for side, held := range g.trig {
			if held {
				out = append(out, evdevWheelNotch(side))
			}
		}
		g.trigAt = now.Add(vcursorScrollRepeat)
	}
	return out
}

// evdevWheelNotch is one wheel step: up (away from the user) for L2, down for
// R2.
func evdevWheelNotch(side int) evdevEvent {
	v := int32(1)
	if side == 1 {
		v = -1
	}
	return evdevEvent{Type: evdevEvRel, Code: evdevRelWheel, Value: v}
}

// restOf is where an axis sits untouched: a trigger at the bottom of its
// range, the D-pad hat at 0, a stick in the middle.
func (g *mbridgeEngine) restOf(axis uint16) int32 {
	r := g.ranges[axis]
	switch axis {
	case evdevAbsZ, evdevAbsRZ:
		return r.min
	case evdevAbsHat0X, evdevAbsHat0Y:
		return 0
	}
	return int32((int64(r.min) + int64(r.max)) / 2)
}

// releaseEvents appends what the bridge writes into the pad just before it
// grabs it: a release for every button held and every axis back at rest, so
// the game is not left with a button stuck down or a stick still tilted while
// it cannot see the pad. The engine's own view follows at once.
func (g *mbridgeEngine) releaseEvents(out []evdevEvent) []evdevEvent {
	for i, held := range g.keys {
		if held {
			out = append(out, evdevEvent{Type: evdevEvKey, Code: uint16(mbridgeKeyBase + i), Value: 0})
			g.keys[i] = false
		}
	}
	for i, r := range g.ranges {
		if !r.valid() {
			continue
		}
		axis := uint16(i)
		if rest := g.restOf(axis); g.abs[axis] != rest {
			out = append(out, evdevEvent{Type: evdevEvAbs, Code: axis, Value: rest})
			g.abs[axis] = rest
		}
	}
	return out
}

// setOn records the mouse coming on or going. Coming on starts from a clean
// slate — no button down, no leftover motion, the chord's quiet window open —
// because the game has just been told that everything was let go. Going
// keeps the chord as it is, so the hold that turned the mouse off has to end
// before another one counts.
func (g *mbridgeEngine) setOn(on bool, now time.Time) {
	g.on = on
	g.btn = [3]bool{}
	g.acc = [2]float64{}
	g.trig = [2]bool{}
	g.kbd = [mbKeyCount]bool{}
	g.combo, g.quitAt, g.quit = false, time.Time{}, false
	g.last = now
	if on {
		g.chord = vcursorChordState{}
		g.quiet = now.Add(mbridgeQuiet)
	}
}
