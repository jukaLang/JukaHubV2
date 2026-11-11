package main

import (
	"bytes"
	"testing"
	"time"
)

// TestEvdevIoctlNumbers: the asm-generic encoding gives the numbers the
// kernel headers define (as strace prints them on arm64 and x86).
func TestEvdevIoctlNumbers(t *testing.T) {
	cases := []struct {
		name      string
		got, want uint32
	}{
		{"EVIOCGRAB", evdevGrab, 0x40044590},
		{"UI_DEV_CREATE", evdevUIDevCreate, 0x5501},
		{"UI_DEV_DESTROY", evdevUIDevDestroy, 0x5502},
		{"UI_DEV_SETUP", evdevUIDevSetup, 0x405c5503},
		{"UI_SET_EVBIT", evdevUISetEvBit, 0x40045564},
		{"UI_SET_KEYBIT", evdevUISetKeyBit, 0x40045565},
		{"UI_SET_RELBIT", evdevUISetRelBit, 0x40045566},
		{"EVIOCGABS(ABS_X)", evdevGetAbs(evdevAbsX), 0x80184540},
		{"EVIOCGABS(ABS_HAT0Y)", evdevGetAbs(evdevAbsHat0Y), 0x80184551},
		{"EVIOCGKEY(96)", evdevGetKey(evdevKeyBitmapSize), 0x80604518},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
	if evdevSetupSize != 92 || evdevUserDevSize != 1116 || evdevAbsInfoSize != 24 || evdevKeyBitmapSize != 96 {
		t.Errorf("struct sizes: setup %d, user_dev %d, absinfo %d, key bitmap %d",
			evdevSetupSize, evdevUserDevSize, evdevAbsInfoSize, evdevKeyBitmapSize)
	}
}

// TestEvdevEncodeEvent: struct input_event is a zero time stamp, then type,
// code and value, little-endian; decoding gives the events back and ignores
// a short tail.
func TestEvdevEncodeEvent(t *testing.T) {
	b := evdevEncode(nil, evdevEvent{Type: evdevEvKey, Code: evdevBtnLeft, Value: 1})
	if len(b) != evdevEventSize {
		t.Fatalf("one event is %d bytes, want %d", len(b), evdevEventSize)
	}
	stamp := evdevEventSize - 8
	if !bytes.Equal(b[:stamp], make([]byte, stamp)) {
		t.Errorf("time stamp not zero: % x", b[:stamp])
	}
	if want := []byte{0x01, 0x00, 0x10, 0x01, 0x01, 0x00, 0x00, 0x00}; !bytes.Equal(b[stamp:], want) {
		t.Errorf("type/code/value = % x, want % x", b[stamp:], want)
	}
	b = evdevEncode(b, evdevEvent{Type: evdevEvRel, Code: evdevRelWheel, Value: -1})
	if want := []byte{0x02, 0x00, 0x08, 0x00, 0xff, 0xff, 0xff, 0xff}; !bytes.Equal(b[2*evdevEventSize-8:], want) {
		t.Errorf("negative value = % x, want % x", b[2*evdevEventSize-8:], want)
	}
	if n := len(b); n != 2*evdevEventSize {
		t.Fatalf("two events are %d bytes", n)
	}
	evs := evdevDecode(append(b, 1, 2, 3), nil)
	if len(evs) != 2 || evs[0] != (evdevEvent{Type: evdevEvKey, Code: evdevBtnLeft, Value: 1}) ||
		evs[1] != (evdevEvent{Type: evdevEvRel, Code: evdevRelWheel, Value: -1}) {
		t.Errorf("decoded %+v", evs)
	}
}

// TestEvdevSetupBytes: struct uinput_setup and the legacy uinput_user_dev
// carry the ids and a NUL-terminated name where the kernel reads them.
func TestEvdevSetupBytes(t *testing.T) {
	b := evdevSetupBytes(mbridgeMouseName, evdevBusVirtual, 0x1234, 0x5678, 1)
	if len(b) != 92 {
		t.Fatalf("uinput_setup is %d bytes, want 92", len(b))
	}
	if want := []byte{0x06, 0x00, 0x34, 0x12, 0x78, 0x56, 0x01, 0x00}; !bytes.Equal(b[:8], want) {
		t.Errorf("input_id = % x, want % x", b[:8], want)
	}
	if got := string(b[8 : 8+len(mbridgeMouseName)]); got != mbridgeMouseName || b[8+len(mbridgeMouseName)] != 0 {
		t.Errorf("name = %q", got)
	}
	if !bytes.Equal(b[88:], []byte{0, 0, 0, 0}) {
		t.Errorf("ff_effects_max = % x, want 0", b[88:])
	}
	long := evdevSetupBytes(string(bytes.Repeat([]byte("x"), 200)), 0, 0, 0, 0)
	if long[8+78] != 'x' || long[8+79] != 0 || len(long) != 92 {
		t.Error("a long name was not cut to 79 bytes and a NUL")
	}

	u := evdevUserDevBytes(mbridgeMouseName, evdevBusVirtual, 0x1234, 0x5678, 1)
	if len(u) != 1116 {
		t.Fatalf("uinput_user_dev is %d bytes, want 1116", len(u))
	}
	if string(u[:len(mbridgeMouseName)]) != mbridgeMouseName || u[len(mbridgeMouseName)] != 0 {
		t.Errorf("user_dev name = %q", u[:len(mbridgeMouseName)])
	}
	if want := []byte{0x06, 0x00, 0x34, 0x12, 0x78, 0x56, 0x01, 0x00}; !bytes.Equal(u[80:88], want) {
		t.Errorf("user_dev input_id = % x, want % x", u[80:88], want)
	}
}

// TestEvdevAbsInfo: an EVIOCGABS answer gives the value and range, and a
// range reads sticks around its middle and triggers from its bottom.
func TestEvdevAbsInfo(t *testing.T) {
	b := []byte{
		0x10, 0x00, 0x00, 0x00, // value 16
		0x00, 0x80, 0xff, 0xff, // minimum -32768
		0xff, 0x7f, 0x00, 0x00, // maximum 32767
		0x10, 0x00, 0x00, 0x00, // fuzz
		0x80, 0x00, 0x00, 0x00, // flat
		0x00, 0x00, 0x00, 0x00, // resolution
	}
	v, r, ok := evdevParseAbsInfo(b)
	if !ok || v != 16 || r.min != -32768 || r.max != 32767 {
		t.Fatalf("absinfo = %d %+v %v", v, r, ok)
	}
	if _, _, ok := evdevParseAbsInfo(b[:20]); ok {
		t.Error("a short answer was accepted")
	}
	if got := r.stick(32767); got < 0.999 {
		t.Errorf("full right = %v", got)
	}
	if got := r.stick(-32768); got > -0.999 {
		t.Errorf("full left = %v", got)
	}
	if got := r.stick(0); got > 0.001 || got < -0.001 {
		t.Errorf("centre = %v", got)
	}
	trig := evdevAbsRange{min: 0, max: 255}
	if trig.trigger(0) != 0 || trig.trigger(255) != 1 || trig.trigger(300) != 1 {
		t.Error("trigger range read wrongly")
	}
	if (evdevAbsRange{}).stick(1000) != 0 || (evdevAbsRange{}).trigger(1000) != 0 {
		t.Error("an axis the pad does not have moved")
	}
}

// TestEvdevParseDevices: the TrimUI's device list gives the firmware pad on
// event3 with its X360 buttons and axes.
func TestEvdevParseDevices(t *testing.T) {
	devs := evdevParseDevices(hintTestHandheldDevices)
	if len(devs) != 2 {
		t.Fatalf("parsed %d devices, want 2", len(devs))
	}
	pad := devs[1]
	if pad.Name != evdevPadName || pad.Handler != "event3" {
		t.Fatalf("pad = %q on %q", pad.Name, pad.Handler)
	}
	for _, code := range []int{evdevBtnSouth, evdevBtnEast, evdevBtnNorth, evdevBtnWest, evdevBtnTL, evdevBtnSelect, evdevBtnMode} {
		if !evdevHas(pad.Key, code) {
			t.Errorf("pad lacks key %#x", code)
		}
	}
	if evdevHas(pad.Key, 0x132) || evdevHas(pad.Key, evdevBtnLeft) {
		t.Error("pad has keys it does not have (BTN_C, BTN_LEFT)")
	}
	for _, axis := range []int{evdevAbsX, evdevAbsY, evdevAbsZ, evdevAbsRX, evdevAbsRY, evdevAbsRZ, evdevAbsHat0X, evdevAbsHat0Y} {
		if !evdevHas(pad.Abs, axis) {
			t.Errorf("pad lacks axis %#x", axis)
		}
	}
	if !evdevHas(pad.EV, evdevEvKey) || !evdevHas(pad.EV, evdevEvAbs) || evdevHas(pad.EV, evdevEvRel) {
		t.Errorf("pad event types wrong: %x", pad.EV)
	}
	if devs[0].Handler != "event0" || devs[0].Name != "gpio-keys" {
		t.Errorf("first device = %q on %q", devs[0].Name, devs[0].Handler)
	}
	got, ok := evdevPickPad(devs)
	if !ok || got.Name != evdevPadName {
		t.Errorf("picked %q, %v", got.Name, ok)
	}
}

// TestEvdevPickPad: without the firmware pad the first gamepad-shaped device
// is lent; a keyboard and the bridge's own mouse never are.
func TestEvdevPickPad(t *testing.T) {
	got, ok := evdevPickPad(evdevParseDevices(hintTestNotKeyboards))
	if !ok || got.Name != "8BitDo Pro 2" || got.Handler != "event11" {
		t.Errorf("picked %q on %q, %v", got.Name, got.Handler, ok)
	}
	if _, ok := evdevPickPad(evdevParseDevices(hintTestUSBKeyboard)); ok {
		t.Error("a keyboard was taken for a pad")
	}
	own := "N: Name=\"" + mbridgeMouseName + "\"\nH: Handlers=event9\nB: EV=b\nB: KEY=7cdb000000000000 0 0 0 0\nB: ABS=3003f\n"
	if _, ok := evdevPickPad(evdevParseDevices(own)); ok {
		t.Error("the bridge's own mouse was taken for a pad")
	}
	for h, want := range map[string]bool{"event3": true, "event12": true, "event": false, "eventX": false,
		"js0": false, "event12345": false, "../event1": false, "event1/": false} {
		if evdevHandlerOK(h) != want {
			t.Errorf("handler %q accepted = %v", h, !want)
		}
	}
}

// TestEvdevKeyBit reads an EVIOCGKEY bitmap.
func TestEvdevKeyBit(t *testing.T) {
	bitmap := make([]byte, evdevKeyBitmapSize)
	bitmap[evdevBtnSelect/8] |= 1 << (evdevBtnSelect % 8)
	if !evdevKeyBit(bitmap, evdevBtnSelect) || evdevKeyBit(bitmap, evdevBtnTL) || evdevKeyBit(bitmap, 0x7fff) {
		t.Error("key bitmap read wrongly")
	}
}

// mbridgeTestEngine is a bridge engine for the TrimUI pad: the printed A is
// BTN_EAST (the firmware swap), sticks span the int16 range, triggers 0..255.
func mbridgeTestEngine() *mbridgeEngine {
	g := &mbridgeEngine{p: mbridgeParams{
		codeA: evdevBtnEast, codeB: evdevBtnSouth, codeX: evdevBtnNorth,
		calL: stickCalDefault(), calR: stickCalDefault(),
	}}
	for _, axis := range []uint16{evdevAbsX, evdevAbsY, evdevAbsRX, evdevAbsRY} {
		g.setRange(axis, evdevAbsRange{min: -32768, max: 32767}, 0)
	}
	g.setRange(evdevAbsZ, evdevAbsRange{min: 0, max: 255}, 0)
	g.setRange(evdevAbsRZ, evdevAbsRange{min: 0, max: 255}, 0)
	g.setRange(evdevAbsHat0X, evdevAbsRange{min: -1, max: 1}, 0)
	g.setRange(evdevAbsHat0Y, evdevAbsRange{min: -1, max: 1}, 0)
	return g
}

func mbridgeKey(code uint16, down bool) evdevEvent {
	return evdevEvent{Type: evdevEvKey, Code: code, Value: evdevBool(down)}
}

func mbridgeAbs(code uint16, v int32) evdevEvent {
	return evdevEvent{Type: evdevEvAbs, Code: code, Value: v}
}

// TestMbridgeChordTurnsMouseOn: the held chord asks for the mouse; the
// releases the bridge writes before grabbing cover the chord's buttons; a
// press the firmware re-sends right after does not count; a fresh hold later
// turns the mouse off.
func TestMbridgeChordTurnsMouseOn(t *testing.T) {
	g := mbridgeTestEngine()
	t0 := time.Unix(500, 0)
	g.last = t0
	g.feed(mbridgeKey(evdevBtnSelect, true), t0, nil)
	g.feed(mbridgeKey(evdevBtnTL, true), t0, nil)
	if _, toggle := g.tick(t0.Add(500*time.Millisecond), nil); toggle {
		t.Fatal("the chord fired early")
	}
	_, toggle := g.tick(t0.Add(vcursorChordHold), nil)
	if !toggle {
		t.Fatal("the held chord did not ask for the mouse")
	}
	rel := g.releaseEvents(nil)
	want := []evdevEvent{mbridgeKey(evdevBtnTL, false), mbridgeKey(evdevBtnSelect, false)}
	if len(rel) != len(want) || rel[0] != want[0] || rel[1] != want[1] {
		t.Fatalf("releases before the grab = %+v, want %+v", rel, want)
	}
	if g.key(evdevBtnSelect) || g.key(evdevBtnTL) {
		t.Error("the engine still thinks the chord is held")
	}
	on := t0.Add(vcursorChordHold)
	g.setOn(true, on)

	// The firmware re-sends the still-held buttons: no new hold.
	g.feed(mbridgeKey(evdevBtnSelect, true), on.Add(5*time.Millisecond), nil)
	g.feed(mbridgeKey(evdevBtnTL, true), on.Add(5*time.Millisecond), nil)
	if _, toggle := g.tick(on.Add(2*time.Second), nil); toggle {
		t.Fatal("a re-sent chord turned the mouse straight off")
	}
	g.feed(mbridgeKey(evdevBtnTL, false), on.Add(3*time.Second), nil)
	g.feed(mbridgeKey(evdevBtnSelect, false), on.Add(3*time.Second), nil)

	// A real hold later turns it off.
	later := on.Add(4 * time.Second)
	g.feed(mbridgeKey(evdevBtnSelect, true), later, nil)
	g.feed(mbridgeKey(evdevBtnTL, true), later, nil)
	if _, toggle := g.tick(later.Add(vcursorChordHold), nil); !toggle {
		t.Error("the chord did not ask for the mouse to go")
	}
}

// TestMbridgeButtons: while the pad is the mouse, A, B and X are its left,
// right and middle buttons; other buttons and key repeats make nothing;
// Menu asks for the pad back. While it is not the mouse nothing comes out.
func TestMbridgeButtons(t *testing.T) {
	g := mbridgeTestEngine()
	now := time.Unix(600, 0)
	if out, _ := g.feed(mbridgeKey(evdevBtnEast, true), now, nil); len(out) != 0 {
		t.Fatalf("a button made mouse events with the mouse off: %+v", out)
	}
	if _, off := g.feed(mbridgeKey(evdevBtnMode, true), now, nil); off {
		t.Error("Menu asked to turn off a mouse that is not on")
	}
	g.feed(mbridgeKey(evdevBtnMode, false), now, nil)
	g.feed(mbridgeKey(evdevBtnEast, false), now, nil)
	g.setOn(true, now)

	cases := []struct {
		in   evdevEvent
		want []evdevEvent
	}{
		{mbridgeKey(evdevBtnEast, true), []evdevEvent{mbridgeKey(evdevBtnLeft, true)}},
		{evdevEvent{Type: evdevEvKey, Code: evdevBtnEast, Value: 2}, nil},
		{mbridgeKey(evdevBtnEast, false), []evdevEvent{mbridgeKey(evdevBtnLeft, false)}},
		{mbridgeKey(evdevBtnSouth, true), []evdevEvent{mbridgeKey(evdevBtnRight, true)}},
		{mbridgeKey(evdevBtnNorth, true), []evdevEvent{mbridgeKey(evdevBtnMiddle, true)}},
		{mbridgeKey(evdevBtnWest, true), nil},
		{mbridgeKey(0x2c0, true), nil},
	}
	for _, c := range cases {
		out, _ := g.feed(c.in, now, nil)
		if len(out) != len(c.want) || (len(out) == 1 && out[0] != c.want[0]) {
			t.Errorf("%+v made %+v, want %+v", c.in, out, c.want)
		}
	}
	if _, off := g.feed(mbridgeKey(evdevBtnMode, true), now, nil); !off {
		t.Error("Menu did not ask for the pad back")
	}
}

// TestMbridgeMotion: a full stick moves the mouse at the in-app pointer's top
// speed in whole counts; the D-pad starts slow; nothing moves with the mouse
// off.
func TestMbridgeMotion(t *testing.T) {
	g := mbridgeTestEngine()
	now := time.Unix(700, 0)
	g.feed(mbridgeAbs(evdevAbsX, 32767), now, nil)
	if out, _ := g.tick(now.Add(20*time.Millisecond), nil); len(out) != 0 {
		t.Fatalf("the stick moved a mouse that is off: %+v", out)
	}
	g.setOn(true, now)
	sum := int32(0)
	for i := 1; i <= 50; i++ {
		out, _ := g.tick(now.Add(time.Duration(i)*20*time.Millisecond), nil)
		for _, ev := range out {
			if ev.Type != evdevEvRel || ev.Code != evdevRelX {
				t.Fatalf("unexpected event %+v", ev)
			}
			sum += ev.Value
		}
	}
	if sum < int32(vcursorMaxSpeed)-2 || sum > int32(vcursorMaxSpeed) {
		t.Errorf("a second of full tilt moved %d counts, want about %v", sum, vcursorMaxSpeed)
	}

	g.feed(mbridgeAbs(evdevAbsX, 0), now, nil)
	start := now.Add(2 * time.Second)
	g.last = start
	g.feed(mbridgeAbs(evdevAbsHat0Y, -1), start, nil)
	up := int32(0)
	for i := 1; i <= 10; i++ {
		out, _ := g.tick(start.Add(time.Duration(i)*20*time.Millisecond), nil)
		for _, ev := range out {
			if ev.Code == evdevRelY {
				up += ev.Value
			}
		}
	}
	if up >= 0 || up < -60 {
		t.Errorf("a fresh D-pad up for 0.2 s moved %d counts: want a slow start upward", up)
	}
}

// TestMbridgeWheel: L2 / R2 scroll a notch on the press and repeat while
// held, at the in-app pace.
func TestMbridgeWheel(t *testing.T) {
	g := mbridgeTestEngine()
	now := time.Unix(800, 0)
	g.setOn(true, now)
	wheel := func(out []evdevEvent) (n int, last int32) {
		for _, ev := range out {
			if ev.Code == evdevRelWheel {
				n++
				last = ev.Value
			}
		}
		return n, last
	}
	g.feed(mbridgeAbs(evdevAbsRZ, 255), now, nil)
	out, _ := g.tick(now.Add(time.Millisecond), nil)
	if n, v := wheel(out); n != 1 || v != -1 {
		t.Fatalf("R2 press: %d notches, last %d; want one down", n, v)
	}
	out, _ = g.tick(now.Add(200*time.Millisecond), nil)
	if n, _ := wheel(out); n != 0 {
		t.Error("R2 repeated before the delay")
	}
	out, _ = g.tick(now.Add(vcursorScrollDelay+10*time.Millisecond), nil)
	if n, _ := wheel(out); n != 1 {
		t.Error("a held R2 did not repeat")
	}
	g.feed(mbridgeAbs(evdevAbsRZ, 0), now, nil)
	out, _ = g.tick(now.Add(2*time.Second), nil)
	if n, _ := wheel(out); n != 0 {
		t.Error("a released R2 kept scrolling")
	}
	g.feed(mbridgeAbs(evdevAbsZ, 200), now, nil)
	out, _ = g.tick(now.Add(3*time.Second), nil)
	if n, v := wheel(out); n != 1 || v != 1 {
		t.Errorf("L2 press: %d notches, last %d; want one up", n, v)
	}
}

// TestMbridgeReleaseEvents: everything held is let go and every axis put at
// rest before the grab; axes already at rest and axes the pad lacks are left
// alone.
func TestMbridgeReleaseEvents(t *testing.T) {
	g := mbridgeTestEngine()
	now := time.Unix(900, 0)
	g.feed(mbridgeKey(evdevBtnEast, true), now, nil)
	g.feed(mbridgeAbs(evdevAbsX, 20000), now, nil)
	g.feed(mbridgeAbs(evdevAbsRZ, 180), now, nil)
	g.feed(mbridgeAbs(evdevAbsHat0X, 1), now, nil)
	got := g.releaseEvents(nil)
	want := []evdevEvent{
		mbridgeKey(evdevBtnEast, false),
		mbridgeAbs(evdevAbsX, 0),
		mbridgeAbs(evdevAbsRZ, 0),
		mbridgeAbs(evdevAbsHat0X, 0),
	}
	if len(got) != len(want) {
		t.Fatalf("releases = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("release %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if again := g.releaseEvents(nil); len(again) != 0 {
		t.Errorf("a second pass released %+v", again)
	}
}

// TestMbridgeReconcileButtons: after the kernel dropped events, a mouse
// button whose pad button the fresh read shows released is let go.
func TestMbridgeReconcileButtons(t *testing.T) {
	g := mbridgeTestEngine()
	now := time.Unix(950, 0)
	g.setOn(true, now)
	g.feed(mbridgeKey(evdevBtnEast, true), now, nil)
	g.feed(mbridgeKey(evdevBtnSouth, true), now, nil)
	bitmap := make([]byte, evdevKeyBitmapSize)
	bitmap[evdevBtnSouth/8] |= 1 << (evdevBtnSouth % 8)
	g.setKeys(bitmap)
	out := g.reconcileButtons(nil)
	if len(out) != 1 || out[0] != mbridgeKey(evdevBtnLeft, false) {
		t.Fatalf("reconcile = %+v, want only the left button let go", out)
	}
	if g.btn[0] || !g.btn[1] {
		t.Errorf("mouse buttons after reconcile: %v", g.btn)
	}
}

// TestMbridgeSetKeysHeldChord: a chord already held when the pad is opened
// (or after dropped events) does not fire until it is pressed again.
func TestMbridgeSetKeysHeldChord(t *testing.T) {
	g := mbridgeTestEngine()
	now := time.Unix(1000, 0)
	bitmap := make([]byte, evdevKeyBitmapSize)
	bitmap[evdevBtnSelect/8] |= 1 << (evdevBtnSelect % 8)
	bitmap[evdevBtnTL/8] |= 1 << (evdevBtnTL % 8)
	g.setKeys(bitmap)
	if !g.key(evdevBtnSelect) || !g.key(evdevBtnTL) {
		t.Fatal("held keys not loaded")
	}
	if _, toggle := g.tick(now.Add(5*time.Second), nil); toggle {
		t.Fatal("a chord held at open fired")
	}
	g.feed(mbridgeKey(evdevBtnTL, false), now.Add(6*time.Second), nil)
	g.feed(mbridgeKey(evdevBtnSelect, false), now.Add(6*time.Second), nil)
	g.feed(mbridgeKey(evdevBtnSelect, true), now.Add(7*time.Second), nil)
	g.feed(mbridgeKey(evdevBtnTL, true), now.Add(7*time.Second), nil)
	if _, toggle := g.tick(now.Add(7*time.Second+vcursorChordHold), nil); !toggle {
		t.Error("a fresh chord after letting go did not fire")
	}
}
