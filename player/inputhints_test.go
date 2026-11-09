package main

import "testing"

// A TrimUI's own devices: the gpio power/volume keys and the firmware's
// virtual X360 pad. Neither is a keyboard.
const hintTestHandheldDevices = `I: Bus=0019 Vendor=0001 Product=0001 Version=0100
N: Name="gpio-keys"
P: Phys=gpio-keys/input0
S: Sysfs=/devices/platform/gpio-keys/input/input0
U: Uniq=
H: Handlers=kbd event0
B: PROP=0
B: EV=3
B: KEY=1c000000000000 0

I: Bus=0003 Vendor=045e Product=028e Version=0114
N: Name="TRIMUI Player1"
P: Phys=
S: Sysfs=/devices/virtual/input/input3
U: Uniq=
H: Handlers=event3 js0
B: PROP=0
B: EV=b
B: KEY=7cdb000000000000 0 0 0 0
B: ABS=3003f

`

const hintTestUSBKeyboard = `I: Bus=0003 Vendor=046d Product=c31c Version=0110
N: Name="Logitech USB Keyboard"
P: Phys=usb-sunxi-ehci-1/input0
S: Sysfs=/devices/platform/soc/5200000.ehci1-controller/usb1/1-1/1-1:1.0/0003:046D:C31C.0001/input/input5
U: Uniq=
H: Handlers=sysrq kbd leds event5
B: PROP=0
B: EV=120013
B: KEY=1000000000007 ff9f207ac14057ff febeffdfffefffff fffffffffffffffe
B: MSC=10
B: LED=7

`

const hintTestBluetoothKeyboard = `I: Bus=0005 Vendor=05ac Product=0255 Version=0050
N: Name="Magic Keyboard"
P: Phys=a4:5e:60:00:00:01
S: Sysfs=/devices/virtual/misc/uhid/0005:05AC:0255.0002/input/input7
U: Uniq=a4:5e:60:00:00:02
H: Handlers=sysrq kbd leds event7
B: PROP=0
B: EV=12001f
B: KEY=1000000000007 ff9f207ac14057ff febeffdfffefffff fffffffffffffffe

`

// A key mapper's uinput keyboard (all keys on) and a pad that also exposes
// the keyboard bits: neither should flip the handheld's hints.
const hintTestNotKeyboards = `I: Bus=0003 Vendor=0000 Product=0000 Version=0000
N: Name="Fake Keyboard"
P: Phys=
S: Sysfs=/devices/virtual/input/input9
U: Uniq=
H: Handlers=sysrq kbd event9
B: PROP=0
B: EV=3
B: KEY=ffffffffffffffff ffffffffffffffff ffffffffffffffff fffffffffffffffe

I: Bus=0003 Vendor=2dc8 Product=3106 Version=0111
N: Name="8BitDo Pro 2"
P: Phys=usb-sunxi-ehci-1/input0
S: Sysfs=/devices/platform/soc/5200000.ehci1-controller/usb1/1-1/1-1:1.0/0003:2DC8:3106.0003/input/input11
U: Uniq=
H: Handlers=kbd event11 js1
B: PROP=0
B: EV=1b
B: KEY=7fdb000000000000 0 0 0 fffffffffffffffe
`

func TestHintParseInputDevices(t *testing.T) {
	cases := []struct {
		name  string
		data  string
		found bool
		dev   string
	}{
		{"handheld only", hintTestHandheldDevices, false, ""},
		{"usb keyboard", hintTestHandheldDevices + hintTestUSBKeyboard, true, "Logitech USB Keyboard"},
		{"bluetooth keyboard", hintTestHandheldDevices + hintTestBluetoothKeyboard, true, "Magic Keyboard"},
		{"mapper and combo pad", hintTestHandheldDevices + hintTestNotKeyboards, false, ""},
		{"no trailing blank line", hintTestUSBKeyboard[:len(hintTestUSBKeyboard)-2], true, "Logitech USB Keyboard"},
		{"empty", "", false, ""},
	}
	for _, tc := range cases {
		found, dev := hintParseInputDevices([]byte(tc.data))
		if found != tc.found || dev != tc.dev {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.name, found, dev, tc.found, tc.dev)
		}
	}
}

func TestHintsAutoKeyboard(t *testing.T) {
	cases := []struct {
		why               string
		pad, kb, handheld bool
		last              hintUsed
		want              bool
	}{
		{"a PC without a pad", false, true, false, hintUsedNone, true},
		{"a phone's press is not a pad", false, true, false, hintUsedPad, true},
		{"a pad plugged into a PC", true, true, false, hintUsedNone, false},
		{"then the PC's keyboard is used", true, true, false, hintUsedKeyboard, true},
		{"the handheld alone", true, false, true, hintUsedKeyboard, false},
		{"a keyboard plugged into the handheld", true, true, true, hintUsedNone, true},
		{"then the handheld's D-pad is used", true, true, true, hintUsedPad, false},
		{"then the keyboard again", true, true, true, hintUsedKeyboard, true},
	}
	for _, tc := range cases {
		if got := hintsAutoKeyboard(tc.pad, tc.kb, tc.last, tc.handheld); got != tc.want {
			t.Errorf("%s: hintsAutoKeyboard = %v, want %v", tc.why, got, tc.want)
		}
	}
}

func TestHintTranslate(t *testing.T) {
	const kb, pad = hintVocabKeyboard, hintVocabPad
	cases := []struct {
		button  string
		v       hintVocab
		ctx     hintCtx
		desktop bool
		brick   bool
		want    string
	}{
		{"A", kb, hintCtxNone, true, false, "Enter"},
		{"B", kb, hintCtxNone, true, false, "Esc"},
		{"D-Pad", kb, hintCtxNone, true, false, "Arrows"},
		{"X", kb, hintCtxNone, true, false, "X"},
		{"A", pad, hintCtxNone, true, false, "A"},
		{"A / Enter", pad, hintCtxNone, false, false, "A"},
		{"A / Enter", kb, hintCtxNone, false, false, "Enter"},
		{"B / Esc", kb, hintCtxNone, false, false, "Esc"},
		{"Start / Del", kb, hintCtxNone, false, false, "Del"},
		{"Start / Del", pad, hintCtxNone, false, false, "Start"},
		{"B / Start", kb, hintCtxNone, false, false, "Esc"},
		{"A / B", kb, hintCtxNone, false, false, "Enter / Esc"},
		{"U/L1", kb, hintCtxNone, false, false, "U"},
		{"U/L1", pad, hintCtxNone, false, false, "L1"},
		{"Y / R", pad, hintCtxNone, false, false, "Y"},
		{"Y / R", kb, hintCtxNone, false, false, "R"},
		{"D-Pad / Mouse", kb, hintCtxNone, true, false, "Arrows / Mouse"},
		{"L1/R1", kb, hintCtxPage, true, false, "PgUp/PgDn"},
		{"L1/R1", kb, hintCtxTabs, true, false, "Tab"},
		{"L1/R1", kb, hintCtxNone, true, false, "L1/R1"},
		{"L2/R2", kb, hintCtxEnds, true, false, "Home/End"},
		{"L2/R2", kb, hintCtxNone, true, false, "L2/R2"},
		{"R2", kb, hintCtxMore, true, false, "PgDn"},
		{"L1", kb, hintCtxNone, true, false, "L1"},
		{"Start", kb, hintCtxMenu, true, false, "F10"},
		{"Start", kb, hintCtxNone, true, false, "Start"},
		{"Select+Start", kb, hintCtxNone, true, false, "Alt+F4"},
		{"Select + Start", kb, hintCtxNone, false, false, "Select + Start"},
		{"Select+Start", pad, hintCtxNone, true, false, "Select+Start"},
		{"Select + R1", kb, hintCtxNone, true, false, "Select + R1"},
		{"X / C", kb, hintCtxNone, true, false, "C"},
		{"X / C", pad, hintCtxNone, true, false, "X"},
		{"F / X", kb, hintCtxNone, true, false, "F"},
		{"L1/R1 / Shift+←→", kb, hintCtxNone, true, false, "Shift+←→"},
		{"L1/R1 / Shift+←→", pad, hintCtxNone, true, false, "L1/R1"},
		{"L2/R2 / P/N", kb, hintCtxNone, true, false, "P/N"},
		{"Ctrl+V", pad, hintCtxNone, true, false, "Ctrl+V"},
		{"A/D", kb, hintCtxNone, true, false, "A/D"},
		{"A/D", pad, hintCtxNone, true, false, "A/D"},
		{"- / +", kb, hintCtxNone, true, false, "- / +"},
		{"- / +", pad, hintCtxNone, true, false, "- / +"},
		{"Select / I", kb, hintCtxNone, true, false, "I"},
		{"Select / I", pad, hintCtxNone, true, false, "Select"},
		{"Select", kb, hintCtxNone, true, false, "Select"},
		{"L1/R1 / F", kb, hintCtxNone, true, false, "F"},
		{"L1/R1 / F", pad, hintCtxNone, true, false, "L1/R1"},
		{"B / F1", pad, hintCtxNone, false, true, "B / F1"},
		{"B / F1", pad, hintCtxNone, false, false, "B"},
		{"Enter", pad, hintCtxNone, false, false, "Enter"},
		{"PgUp/PgDn", kb, hintCtxNone, true, false, "PgUp/PgDn"},
		{"Toolbox", kb, hintCtxNone, true, false, "Toolbox"},
		{"Hold A", kb, hintCtxNone, true, false, "Hold Enter"},
		{"Hold A", pad, hintCtxNone, true, false, "Hold A"},
		{"Hold SELECT", kb, hintCtxNone, true, false, "Hold SELECT"},
		{"Y / /", kb, hintCtxNone, true, false, "/"},
		{"Y / /", pad, hintCtxNone, true, false, "Y"},
		{"L2/R2 / - / +", kb, hintCtxNone, true, false, "- / +"},
		{"L2/R2 / - / +", pad, hintCtxNone, true, false, "L2/R2"},
		{"L1/R1 / [ ]", kb, hintCtxNone, true, false, "[ ]"},
		{"L1 / Del", kb, hintCtxNone, true, false, "Del"},
		{"L1 / Del", pad, hintCtxNone, true, false, "L1"},
		{"", kb, hintCtxNone, true, false, ""},
	}
	for _, tc := range cases {
		if got := hintTranslate(tc.button, tc.v, tc.ctx, tc.desktop, tc.brick); got != tc.want {
			t.Errorf("hintTranslate(%q, vocab %d, ctx %d, desktop %v, brick %v) = %q, want %q", tc.button, tc.v, tc.ctx, tc.desktop, tc.brick, got, tc.want)
		}
	}
}

func TestHintLabelContext(t *testing.T) {
	cases := map[string]hintCtx{
		"Tabs":       hintCtxTabs,
		"Start menu": hintCtxMenu,
		"Menu":       hintCtxMenu,
		"Page":       hintCtxPage,
		"Scroll":     hintCtxPage,
		"Top / End":  hintCtxEnds,
		"More":       hintCtxMore,
		"Clear":      hintCtxNone,
		"Seek":       hintCtxNone,
		"Zoom":       hintCtxNone,
		"Send":       hintCtxNone,
		"Stop":       hintCtxNone,
		"Page 1-10":  hintCtxPage,
		"Section":    hintCtxPage,
		"Sections":   hintCtxPage,
	}
	for label, want := range cases {
		if got := hintLabelContext(label); got != want {
			t.Errorf("hintLabelContext(%q) = %d, want %d", label, got, want)
		}
	}
	if !hintTypingScancode(4) || !hintTypingScancode(101) || hintTypingScancode(102) || hintTypingScancode(128) {
		t.Error("hintTypingScancode must take A..Menu and leave power and volume out")
	}
}

// INP-4: under an Accessibility remap a badge names the physical button that
// sends the action, through the same translate-then-remap steps hintKeyFor
// takes.
func TestHintRemapFollowsAccessibility(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	const kb, pad = hintVocabKeyboard, hintVocabPad
	type badge struct {
		button string
		v      hintVocab
		want   string
	}
	cases := []struct {
		name           string
		swapAB, swapXY bool
		keymap         string
		badges         []badge
	}{
		{"no remap", false, false, "", []badge{
			{"A", pad, "A"}, {"B", pad, "B"}, {"X / C", pad, "X"}, {"L1/R1", pad, "L1/R1"}, {"A", kb, "Enter"},
		}},
		{"Nintendo layout", true, true, "", []badge{
			{"A", pad, "B"}, {"B", pad, "A"}, {"X", pad, "Y"}, {"Y", pad, "X"},
			{"A / B", pad, "B / A"}, {"A / Enter", pad, "B"}, {"X / C", pad, "Y"}, {"L1+X", pad, "L1+Y"},
			{"Hold A", pad, "Hold B"}, {"D-Pad", pad, "D-Pad"}, {"L1/R1", pad, "L1/R1"},
			// Keys are never remapped: Enter, the letter X, a key pair.
			{"A", kb, "Enter"}, {"X", kb, "X"}, {"X / C", kb, "C"}, {"A/D", pad, "A/D"},
			// The quit chord reads the physical buttons.
			{"Select + Start", pad, "Select + Start"},
		}},
		{"A/B swap only", true, false, "", []badge{
			{"A", pad, "B"}, {"B", pad, "A"}, {"X", pad, "X"}, {"Y", pad, "Y"}, {"B / Start", pad, "A / Start"},
		}},
		{"one custom remap", false, false, "a:x", []badge{
			// X still sends X. A sends X too and nothing sends A any more, so
			// A's job has no button: a dash, not "A", which now does X's job.
			{"X", pad, "X"}, {"A", pad, "—"}, {"B", pad, "B"}, {"Hold A", pad, "Hold —"},
			// An alternative with no button goes while another is left; the
			// keyboard's Enter is never remapped.
			{"A / B", pad, "B"}, {"A / Enter", pad, "—"}, {"A", kb, "Enter"},
		}},
		{"Start switched off", false, false, "start:none", []badge{
			{"Start", pad, "—"}, {"B / Start", pad, "B"}, {"Select + R1", pad, "Select + R1"},
			// The quit chord reads the physical buttons.
			{"Select + Start", pad, "Select + Start"},
		}},
		{"shoulders swapped", false, false, "l1:r1,r1:l1", []badge{
			{"L1/R1", pad, "R1/L1"}, {"L1", pad, "R1"}, {"L1", kb, "R1"}, {"L2/R2", pad, "L2/R2"},
			{"L1+R1", pad, "R1+L1"}, {"Ctrl+V", pad, "Ctrl+V"},
		}},
		{"Select row swapped", false, false, "select:start,start:select", []badge{
			{"Start", pad, "Select"}, {"Select", pad, "Start"}, {"Select + R1", pad, "Start + R1"},
			// The quit chord reads the physical buttons. Names match as hints
			// spell them (the controller test passes no remap at all).
			{"Select + Start", pad, "Select + Start"}, {"Hold SELECT", pad, "Hold SELECT"},
		}},
	}
	for _, tc := range cases {
		a11y = a11ySettings{fontScale: 1.0, swapAB: tc.swapAB, swapXY: tc.swapXY, keys: a11yParseKeymap(tc.keymap)}
		pairs := hintRemapInverse(a11yResolveButton)
		for _, b := range tc.badges {
			got := hintRemapPads(hintTranslate(b.button, b.v, hintCtxNone, true, false), b.v, false, pairs)
			if got != b.want {
				t.Errorf("%s: %q in vocab %d = %q, want %q", tc.name, b.button, b.v, got, b.want)
			}
		}
	}
}

// The published inverse reaches hintKeyFor and hintSentence, and a change of
// the remap retires what was cached under the old one.
func TestHintKeyForFollowsRemapChanges(t *testing.T) {
	prevKeyboard := hintKeyboardNow.Load()
	hintKeyboardNow.Store(false)
	resetA11yForTest()
	t.Cleanup(func() {
		resetA11yForTest()
		hintRemapRefresh()
		hintKeyboardNow.Store(prevKeyboard)
	})
	hintRemapRefresh()
	if got := hintKeyFor("A", "Open"); got != "A" {
		t.Fatalf("no remap: hintKeyFor(A) = %q, want A", got)
	}
	if got := hintSentence("Press %s to type", "A", "Type"); got != "Press A to type" {
		t.Fatalf("no remap: hintSentence = %q", got)
	}
	a11y.swapAB, a11y.swapXY = true, true
	hintRemapRefresh()
	if got := hintKeyFor("A", "Open"); got != "B" {
		t.Errorf("Nintendo layout: hintKeyFor(A) = %q, want B", got)
	}
	if got := hintSentence2("%s confirms, %s goes back", "A", "Confirm", "B", "Back"); got != "B confirms, A goes back" {
		t.Errorf("Nintendo layout: hintSentence2 = %q", got)
	}
	hintKeyboardNow.Store(true)
	if got := hintSentence("Press %s to type", "A", "Type"); got != "Press Enter to type" {
		t.Errorf("keyboard: hintSentence = %q, want the Enter key", got)
	}
}
