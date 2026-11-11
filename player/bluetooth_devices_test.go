package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Fakes ─────────────────────────────────────────────────────────────────

// bzFakeSession answers each command with scripted lines, the way
// bluetoothctl would, so the flows run without a radio. Like a real session on
// a terminal it echoes every command first ("[bluetooth]# pair …", which the
// cleaner turns into "pair …"), and lines go through bzCleanLine like the real
// pump's output does.
type bzFakeSession struct {
	mu      sync.Mutex
	out     chan string
	replies map[string][]string
	sent    []string
	closed  bool
	// dead is a bluetoothctl that exited as it started: nothing can be typed,
	// and its output is already over.
	dead bool
}

// bzOpening is how a healthy interactive bluetoothctl starts: its "no daemon"
// prompt, redrawn as soon as it reaches bluetoothd, then its own agent.
var bzOpening = []string{"Waiting to connect to bluetoothd...", "[bluetooth]# Agent registered"}

func newBzFake(replies map[string][]string, opening ...string) *bzFakeSession {
	f := &bzFakeSession{out: make(chan string, 4096), replies: replies}
	for _, line := range opening {
		f.push(line)
	}
	return f
}

// newBzDeadFake is a session whose bluetoothctl did not start properly.
func newBzDeadFake() *bzFakeSession {
	f := &bzFakeSession{out: make(chan string), dead: true}
	close(f.out)
	return f
}

func (f *bzFakeSession) push(line string) {
	if clean := bzCleanLine(line); clean != "" {
		f.out <- clean
	}
}

func (f *bzFakeSession) send(cmd string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.dead {
		return errBzEnded
	}
	f.sent = append(f.sent, cmd)
	f.push("[bluetooth]# " + cmd)
	for _, line := range f.replies[cmd] {
		f.push(line)
	}
	return nil
}

func (f *bzFakeSession) lines() <-chan string { return f.out }

func (f *bzFakeSession) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *bzFakeSession) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// withBzFakes makes the flows' sessions the fakes, in order (the last one
// repeats), and returns the arguments each session was started with.
func withBzFakes(t *testing.T, fakes ...*bzFakeSession) func() [][]string {
	t.Helper()
	prev := bzStartSession
	var mu sync.Mutex
	var starts [][]string
	bzStartSession = func(_ context.Context, _ time.Duration, args ...string) (bzSession, error) {
		mu.Lock()
		defer mu.Unlock()
		i := len(starts)
		if i >= len(fakes) {
			i = len(fakes) - 1
		}
		starts = append(starts, append([]string{}, args...))
		return fakes[i], nil
	}
	t.Cleanup(func() { bzStartSession = prev })
	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), starts...)
	}
}

// withBzFake makes every flow talk to fake.
func withBzFake(t *testing.T, fake *bzFakeSession) {
	t.Helper()
	withBzFakes(t, fake)
}

// withBzInput replaces the input-device reader with successive snapshots; the
// last one repeats.
func withBzInput(t *testing.T, snapshots ...[]bzInputDev) {
	t.Helper()
	prev := bzReadInputDevices
	var mu sync.Mutex
	calls := 0
	bzReadInputDevices = func() []bzInputDev {
		mu.Lock()
		defer mu.Unlock()
		if len(snapshots) == 0 {
			return nil
		}
		i := calls
		if i >= len(snapshots) {
			i = len(snapshots) - 1
		}
		calls++
		return snapshots[i]
	}
	t.Cleanup(func() { bzReadInputDevices = prev })
}

// withShortBzWaits keeps the waits that a scripted session cannot end short.
func withShortBzWaits(t *testing.T) {
	t.Helper()
	prevFind, prevInput := bzFindWait, bzInputWait
	bzFindWait, bzInputWait = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { bzFindWait, bzInputWait = prevFind, prevInput })
}

// withShortQuickWait shortens the wait for daemon-only answers.
func withShortQuickWait(t *testing.T) {
	t.Helper()
	prev := bzQuickWait
	bzQuickWait = 150 * time.Millisecond
	t.Cleanup(func() { bzQuickWait = prev })
}

// withBtScreen gives a test a fresh page state and puts the old one back.
func withBtScreen(t *testing.T) {
	t.Helper()
	saved := btScreen
	btScreen = btScreenState{
		devices:     map[string]*bzDevice{},
		padOK:       map[string]bool{},
		noPad:       map[string]bool{},
		connectedAt: map[string]time.Time{},
	}
	t.Cleanup(func() { btScreen = saved })
}

const bzTestPad = "4C:B9:9B:12:34:56"

// bzTestPadKeys is a pad's KEY bitmap on a 64-bit kernel: BTN_SOUTH and the
// buttons after it (0x130-0x13e) in the fifth word from the end.
const bzTestPadKeys = "7fdb000000000000 0 0 0 0"

const bzTestInfoPad = `Device 4C:B9:9B:12:34:56 (public)
	Name: Wireless Controller
	Alias: Wireless Controller
	Class: 0x00002508
	Icon: input-gaming
	Paired: yes
	Bonded: yes
	Trusted: yes
	Blocked: no
	Connected: yes
	LegacyPairing: no
	UUID: Human Interface Device... (00001124-0000-1000-8000-00805f9b34fb)
	UUID: PnP Information           (00001200-0000-1000-8000-00805f9b34fb)
	Modalias: usb:v054Cp05C4d0100`

const bzTestInfoHeadset = `Device 11:22:33:44:55:66
	Name: WH-1000XM4
	Alias: WH-1000XM4
	Class: 0x00240404
	Icon: audio-headset
	Paired: no
	Trusted: no
	Blocked: no
	Connected: no
	LegacyPairing: no
	UUID: Audio Sink                (0000110b-0000-1000-8000-00805f9b34fb)
	RSSI: -71`

const bzTestInputDevices = `I: Bus=0019 Vendor=0001 Product=0001 Version=0100
N: Name="axp2202-pek"
P: Phys=m1kbd/input2
S: Sysfs=/devices/platform/soc/axp2202-pek/input/input0
U: Uniq=
H: Handlers=kbd event0
B: PROP=0
B: EV=3
B: KEY=100000 0 0 0

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

I: Bus=0005 Vendor=054c Product=0ce6 Version=8100
N: Name="Sony Interactive Entertainment DualSense Wireless Controller"
P: Phys=00:1a:7d:da:71:13
S: Sysfs=/devices/platform/soc/soc:bt/hci0/hci0:11/0005:054C:0CE6.0001/input/input7
U: Uniq=4c:b9:9b:12:34:56
H: Handlers=event4 js1
B: PROP=0
B: EV=1b
B: KEY=7fdb000000000000 0 0 0 0
B: ABS=3003f
B: MSC=10

I: Bus=0005 Vendor=054c Product=0ce6 Version=8100
N: Name="Sony Interactive Entertainment DualSense Wireless Controller Touchpad"
P: Phys=00:1a:7d:da:71:13
S: Sysfs=/devices/platform/soc/soc:bt/hci0/hci0:11/0005:054C:0CE6.0001/input/input9
U: Uniq=4c:b9:9b:12:34:56
H: Handlers=event6 mouse0
B: PROP=5
B: EV=b
B: KEY=2420 10000 0 0 0 0
B: ABS=260800000000003

I: Bus=0005 Vendor=054c Product=0ce6 Version=8100
N: Name="Sony Interactive Entertainment DualSense Wireless Controller Motion Sensors"
P: Phys=00:1a:7d:da:71:13
S: Sysfs=/devices/platform/soc/soc:bt/hci0/hci0:11/0005:054C:0CE6.0001/input/input8
U: Uniq=4c:b9:9b:12:34:56
H: Handlers=event5
B: PROP=40
B: EV=19
B: ABS=3f

I: Bus=0005 Vendor=05ac Product=0255 Version=0050
N: Name="Magic Keyboard"
P: Phys=00:1a:7d:da:71:13
S: Sysfs=/devices/platform/soc/soc:bt/hci0/hci0:12/0005:05AC:0255.0002/input/input10
U: Uniq=a8:51:ab:00:00:01
H: Handlers=sysrq kbd event7 leds
B: PROP=0
B: EV=12001f
B: KEY=1000000000007 ff9f207ac14057ff febeffdfffefffff fffffffffffffffe
`

// bzPairReplies is a session that pairs the test pad cleanly. Started with
// --agent=NoInputNoOutput, its agent is already there when it is asked for.
func bzPairReplies(pair, connect []string) map[string][]string {
	mac := bzTestPad
	return map[string][]string{
		"agent off":             {"Agent unregistered"},
		"agent NoInputNoOutput": {"Agent is already registered"},
		"default-agent":         {"Default agent request successful"},
		"scan on": {"Discovery started", "[CHG] Controller 00:1A:7D:DA:71:13 Discovering: yes",
			"[NEW] Device " + mac + " Wireless Controller"},
		"scan off":       {"Discovery stopped"},
		"pair " + mac:    pair,
		"trust " + mac:   {"[CHG] Device " + mac + " Trusted: yes", "Changing " + mac + " trust succeeded"},
		"connect " + mac: connect,
		"info " + mac:    strings.Split(bzTestInfoPad, "\n"),
		"version":        {"Version 5.50"},
	}
}

// ── Lines ─────────────────────────────────────────────────────────────────

func TestBzCleanLineStripsColourAndPrompts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[0;94m[bluetooth]\x1b[0m# ", ""},
		{"\r\x1b[K[\x1b[0;92mNEW\x1b[0m] Device 4C:B9:9B:12:34:56 DualSense Wireless Controller",
			"[NEW] Device 4C:B9:9B:12:34:56 DualSense Wireless Controller"},
		{"\x01\x1b[0;94m\x02[DualSense Wireless Controller]\x01\x1b[0m\x02# Connection successful", "Connection successful"},
		{"[bluetooth]# [CHG] Device 4C:B9:9B:12:34:56 Connected: yes", "[CHG] Device 4C:B9:9B:12:34:56 Connected: yes"},
		{"\tName: Wireless Controller", "Name: Wireless Controller"},
		{"[agent] Confirm passkey 123456 (yes/no): ", "[agent] Confirm passkey 123456 (yes/no):"},
		{"\x1b[?2004h[bluetooth]# scan on", "scan on"},
		{"\x01Waiting to connect to bluetoothd...\x02", "Waiting to connect to bluetoothd..."},
	}
	for _, c := range cases {
		if got := bzCleanLine(c.in); got != c.want {
			t.Errorf("bzCleanLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBzPumpLinesSplitsAndHandsOverQuestions(t *testing.T) {
	r, w := io.Pipe()
	out := make(chan string, 16)
	done := make(chan struct{})
	defer close(done)
	go bzPumpLines(r, out, done)
	go func() {
		// The start: the "no daemon" prompt, redrawn with \r as the daemon answers.
		_, _ = io.WriteString(w, "\x01Waiting to connect to bluetoothd...\x02\r\x1b[K\x01\x1b[0;94m\x02[bluetooth]\x01\x1b[0m\x02# ")
		_, _ = io.WriteString(w, "\x1b[0;94m[bluetooth]\x1b[0m# scan on\r\nDiscovery started\n[NEW] Device 4C:B9:9B:12:34:56 Wireless Controller\r")
		// The agent's question has no newline and waits for an answer.
		_, _ = io.WriteString(w, "[agent] Confirm passkey 123456 (yes/no): ")
	}()
	want := []string{
		"Waiting to connect to bluetoothd...",
		"scan on",
		"Discovery started",
		"[NEW] Device 4C:B9:9B:12:34:56 Wireless Controller",
		"[agent] Confirm passkey 123456 (yes/no):",
	}
	for _, line := range want {
		select {
		case got := <-out:
			if got != line {
				t.Fatalf("line = %q, want %q", got, line)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no line %q", line)
		}
	}
	_ = w.Close()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("a line after the output ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pump did not finish when the output ended")
	}
}

func TestBzAgentAnswer(t *testing.T) {
	cases := []struct{ line, want string }{
		{"[agent] Confirm passkey 123456 (yes/no):", "yes"},
		{"[agent] Authorize service 0000110d (yes/no):", "yes"},
		{"[agent] Enter PIN code:", "0000"},
		{"Pairing successful", ""},
		// A neighbour's name is free text: only the agent's own lines are asked.
		{"[NEW] Device 11:22:33:44:55:66 Say (yes/no)", ""},
		{"Enter PIN code for nothing", ""},
	}
	for _, c := range cases {
		if got := bzAgentAnswer(c.line); got != c.want {
			t.Errorf("bzAgentAnswer(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// While bluetoothd is down bluetoothctl prints its "no daemon" prompt with no
// newline and then nothing: the pump must still hand it on, or no step could
// ever tell that from a slow answer.
func TestBzPumpLinesHandsOverTheNoDaemonPrompt(t *testing.T) {
	r, w := io.Pipe()
	out := make(chan string, 4)
	done := make(chan struct{})
	defer close(done)
	go bzPumpLines(r, out, done)
	go func() {
		_, _ = io.WriteString(w, "\x01Waiting to connect to bluetoothd...\x02")
	}()
	select {
	case got := <-out:
		if got != "Waiting to connect to bluetoothd..." {
			t.Fatalf("line = %q, want the no-daemon prompt", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the no-daemon prompt was never handed on")
	}
	_ = w.Close()
}

// ── The "no daemon" prompt ────────────────────────────────────────────────

func TestBzFlowNoteFollowsTheNoDaemonPrompt(t *testing.T) {
	f := &bzFlow{typed: map[string]bool{"devices": true}}
	if got := f.note("Waiting to connect to bluetoothd..."); got != "" || !f.waiting {
		t.Fatalf("the prompt alone: %q, waiting %v", got, f.waiting)
	}
	if got := f.note("devices"); got != "devices" || !f.waiting {
		t.Fatalf("the echo of a command cleared the wait: %q, waiting %v", got, f.waiting)
	}
	if got := f.note("Agent registered"); got != "Agent registered" || f.waiting {
		t.Fatalf("word from the daemon: %q, waiting %v", got, f.waiting)
	}
	if got := f.note("Waiting to connect to bluetoothd...[bluetooth]#"); got != "" || f.waiting {
		t.Fatalf("the prompt redrawn behind it: %q, waiting %v", got, f.waiting)
	}
	if got := f.note("Waiting to connect to bluetoothd...devices"); got != "devices" || !f.waiting {
		t.Fatalf("typing on the old prompt: %q, waiting %v", got, f.waiting)
	}
}

func TestBzNoDaemonPromptAtStartIsNotFatal(t *testing.T) {
	for _, opening := range [][]string{
		bzOpening,
		{"Waiting to connect to bluetoothd...[bluetooth]# "},
		{"\x01Waiting to connect to bluetoothd...\x02"},
	} {
		fake := newBzFake(map[string][]string{
			"paired-devices":    {"Device " + bzTestPad + " Wireless Controller"},
			"devices":           {"Device " + bzTestPad + " Wireless Controller"},
			"info " + bzTestPad: strings.Split(bzTestInfoPad, "\n"),
			"version":           {"Version 5.50"},
		}, opening...)
		withBzFake(t, fake)
		devs, err := bzList(context.Background())
		if err != nil || len(devs) != 1 || !devs[0].Paired {
			t.Fatalf("opening %q: devices %+v, err %v", opening, devs, err)
		}
	}
}

func TestBzNoDaemonOnlyWhenThePromptStays(t *testing.T) {
	withShortQuickWait(t)
	// A bluetoothctl that never reaches bluetoothd shows its prompt and
	// nothing else; the echo of what is typed is no answer.
	withBzFake(t, newBzFake(nil, "Waiting to connect to bluetoothd..."))
	if _, err := bzList(context.Background()); !errors.Is(err, errBzNoDaemon) {
		t.Fatalf("no daemon: err = %v", err)
	}
	// After a healthy start, a step that is not answered is only a step that
	// was not answered.
	withBzFake(t, newBzFake(nil, bzOpening...))
	err := bzDisconnect(context.Background(), bzTestPad)
	var be *bzError
	if errors.Is(err, errBzNoDaemon) || !errors.As(err, &be) || be.code != "timeout" {
		t.Fatalf("an unanswered step after a healthy start: err = %v", err)
	}
}

func TestBzNoAdapterStopsAFlow(t *testing.T) {
	withBzFake(t, newBzFake(map[string][]string{"scan on": {"No default controller available"}}, bzOpening...))
	if _, err := bzScan(context.Background(), 50*time.Millisecond, nil); !errors.Is(err, errBzNoAdapter) {
		t.Fatalf("no adapter: err = %v", err)
	}
}

// ── Parsing ───────────────────────────────────────────────────────────────

func TestBzParseEvent(t *testing.T) {
	ev, ok := bzParseEvent("[NEW] Device 4c:b9:9b:12:34:56 DualSense Wireless Controller")
	if !ok || ev.kind != bzEventNew || ev.mac != bzTestPad || ev.name != "DualSense Wireless Controller" {
		t.Fatalf("NEW = %+v, %v", ev, ok)
	}
	if ev, ok = bzParseEvent("[NEW] Device 5A:11:22:33:44:55 5A-11-22-33-44-55"); !ok || ev.name != "" {
		t.Fatalf("an address is not a name: %+v, %v", ev, ok)
	}
	ev, ok = bzParseEvent("[CHG] Device 4C:B9:9B:12:34:56 Connected: yes")
	if !ok || ev.kind != bzEventChange || ev.key != "Connected" || ev.value != "yes" {
		t.Fatalf("CHG = %+v, %v", ev, ok)
	}
	if ev, ok = bzParseEvent("[DEL] Device 4C:B9:9B:12:34:56 DualSense Wireless Controller"); !ok || ev.kind != bzEventGone {
		t.Fatalf("DEL = %+v, %v", ev, ok)
	}
	for _, line := range []string{
		"[NEW] Controller 00:1A:7D:DA:71:13 TrimUI [default]",
		"[CHG] Controller 00:1A:7D:DA:71:13 Discovering: yes",
		"[CHG] Device not-an-address Name: x",
		"Device 4C:B9:9B:12:34:56 Wireless Controller",
	} {
		if _, ok := bzParseEvent(line); ok {
			t.Errorf("%q parsed as a device event", line)
		}
	}
}

func TestBzApplyEventsBuildTheList(t *testing.T) {
	devs := map[string]*bzDevice{}
	now := time.Now()
	for _, line := range []string{
		"[NEW] Device 4C:B9:9B:12:34:56 4C-B9-9B-12-34-56",
		"[CHG] Device 4C:B9:9B:12:34:56 Name: DualSense Wireless Controller",
		"[CHG] Device 4C:B9:9B:12:34:56 Icon: input-gaming",
		"[CHG] Device 4C:B9:9B:12:34:56 RSSI: 0xffffffc4 (-60)",
		"[NEW] Device 11:22:33:44:55:66 WH-1000XM4",
		"[CHG] Device 11:22:33:44:55:66 UUIDs: 0000110b-0000-1000-8000-00805f9b34fb",
		"[NEW] Device 77:88:99:AA:BB:CC Phone",
		"[DEL] Device 77:88:99:AA:BB:CC Phone",
	} {
		if ev, ok := bzParseEvent(line); ok {
			bzApplyEvent(devs, ev, now)
		}
	}
	pad := devs[bzTestPad]
	if pad == nil || pad.Name != "DualSense Wireless Controller" || pad.kind() != bzKindController || pad.RSSI != -60 {
		t.Fatalf("pad = %+v", pad)
	}
	if hp := devs["11:22:33:44:55:66"]; hp == nil || hp.kind() != bzKindAudio {
		t.Fatalf("headphones = %+v", hp)
	}
	if _, ok := devs["77:88:99:AA:BB:CC"]; ok {
		t.Fatal("a device that left is still listed")
	}
	// A paired device that leaves stays listed.
	devs[bzTestPad].Paired = true
	if ev, ok := bzParseEvent("[DEL] Device 4C:B9:9B:12:34:56 DualSense Wireless Controller"); ok {
		bzApplyEvent(devs, ev, now)
	}
	if devs[bzTestPad] == nil {
		t.Fatal("a paired pad was dropped when it went out of range")
	}
}

func TestBzEntryKeepsPairedAndNamedDevices(t *testing.T) {
	devs := map[string]*bzDevice{}
	base := time.Now()
	for i := 0; i < bzMaxDevices; i++ {
		mac := fmt.Sprintf("02:00:00:00:00:%02X", i)
		devs[mac] = &bzDevice{MAC: mac, Seen: base.Add(time.Duration(i) * time.Second)}
	}
	// Full: a newcomer that sent no name is turned away…
	if d := bzEntry(devs, "02:00:00:00:01:00", false, false); d != nil {
		t.Fatal("an unnamed newcomer pushed a device out")
	}
	// …a named one takes the place of the unnamed device seen longest ago…
	if ev, ok := bzParseEvent("[NEW] Device " + bzTestPad + " Wireless Controller"); ok {
		bzApplyEvent(devs, ev, base.Add(time.Hour))
	}
	if d := devs[bzTestPad]; d == nil || d.Name != "Wireless Controller" {
		t.Fatal("the named pad was refused in a full list")
	}
	if _, ok := devs["02:00:00:00:00:00"]; ok {
		t.Fatal("the oldest unnamed device was not the one to go")
	}
	// …and a paired device always gets in, beyond the cap.
	if d := bzEntry(devs, "11:22:33:44:55:66", false, true); d == nil || !d.Paired {
		t.Fatalf("a paired device was refused: %+v", d)
	}
	if n := bzUnpairedCount(devs); n != bzMaxDevices {
		t.Fatalf("unpaired devices = %d, want %d", n, bzMaxDevices)
	}
}

func TestBzDevicesLine(t *testing.T) {
	mac, name, ok := bzDevicesLine("Device 4c:b9:9b:12:34:56 Wireless Controller")
	if !ok || mac != bzTestPad || name != "Wireless Controller" {
		t.Fatalf("got %q %q %v", mac, name, ok)
	}
	if _, name, ok = bzDevicesLine("Device 5A:11:22:33:44:55 5A-11-22-33-44-55"); !ok || name != "" {
		t.Fatalf("unnamed device: %q %v", name, ok)
	}
	for _, line := range []string{
		"Device 4C:B9:9B:12:34:56 (public)",
		"Device 4C:B9:9B:12:34:56",
		"Device 4C:B9:9B:12:34:56 not available",
		"Controller 00:1A:7D:DA:71:13 TrimUI [default]",
		"Version 5.50",
	} {
		if _, _, ok := bzDevicesLine(line); ok {
			t.Errorf("%q read as a list line", line)
		}
	}
}

func TestBzInfoReaderReadsBlocks(t *testing.T) {
	var r bzInfoReader
	text := bzTestInfoPad + "\n[CHG] Device 11:22:33:44:55:66 RSSI: -70\n" + bzTestInfoHeadset + "\nVersion 5.50"
	for _, line := range strings.Split(text, "\n") {
		r.feed(bzCleanLine(line))
	}
	pad := r.blocks[bzTestPad]
	if pad == nil || !pad.Paired || !pad.Trusted || !pad.Connected || !pad.HID || pad.Class != 0x2508 ||
		pad.kind() != bzKindController || pad.Name != "Wireless Controller" {
		t.Fatalf("pad block = %+v", pad)
	}
	hs := r.blocks["11:22:33:44:55:66"]
	if hs == nil || hs.Paired || hs.Connected || !hs.Audio || hs.kind() != bzKindAudio || hs.RSSI != -71 {
		t.Fatalf("headset block = %+v", hs)
	}
}

func TestBzClassify(t *testing.T) {
	cases := []struct {
		name string
		d    bzDevice
		want bzKind
	}{
		{"icon", bzDevice{Icon: "input-gaming"}, bzKindController},
		{"gamepad class", bzDevice{Class: 0x002508}, bzKindController},
		{"joystick class", bzDevice{Class: 0x002504}, bzKindController},
		{"8BitDo by name", bzDevice{Name: "8BitDo Pro 2"}, bzKindController},
		{"Xbox by name", bzDevice{Name: "Xbox Wireless Controller"}, bzKindController},
		{"headset icon", bzDevice{Icon: "audio-headset"}, bzKindAudio},
		{"audio class", bzDevice{Class: 0x240404}, bzKindAudio},
		{"audio service", bzDevice{Name: "Buds", Audio: true}, bzKindAudio},
		{"keyboard icon", bzDevice{Icon: "input-keyboard"}, bzKindKeyboard},
		{"keyboard class", bzDevice{Class: 0x002540}, bzKindKeyboard},
		{"mouse class", bzDevice{Class: 0x002580}, bzKindMouse},
		{"phone", bzDevice{Name: "Pixel 8", Icon: "phone"}, bzKindOther},
	}
	for _, c := range cases {
		if got := bzClassify(c.d); got != c.want {
			t.Errorf("%s: kind %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBzStepErrorsSpeakPlainly(t *testing.T) {
	cases := []struct {
		line, want string
	}{
		{"Failed to pair: org.bluez.Error.AuthenticationFailed", "refused the pairing"},
		{"Failed to pair: org.bluez.Error.ConnectionAttemptFailed", "did not answer"},
		{"Failed to pair: org.bluez.Error.InProgress", "busy"},
		{"Failed to start discovery: org.bluez.Error.NotReady", "off"},
		{"Failed to connect: org.bluez.Error.Failed br-connection-profile-unavailable", "no driver"},
		{"Failed to pair: org.bluez.Error.Weird", "Bluetooth said: Weird"},
	}
	for _, c := range cases {
		if got := bzErrText(bzStepError("pair", c.line)); !strings.Contains(got, c.want) {
			t.Errorf("%q -> %q, want it to say %q", c.line, got, c.want)
		}
	}
	if !bzIsProfileUnavailable(bzStepError("connect", "Failed to connect: org.bluez.Error.Failed br-connection-profile-unavailable")) {
		t.Error("profile-unavailable not recognised")
	}
	if bzIsProfileUnavailable(bzStepError("connect", "Failed to connect: org.bluez.Error.Failed")) {
		t.Error("a plain failure read as profile-unavailable")
	}
}

func TestBzStepAnswersEndTheirSteps(t *testing.T) {
	mac := bzTestPad
	trust := bzTrustAnswer(mac)
	for _, line := range []string{
		"Changing " + mac + " trust succeeded",
		"Failed to set " + mac + " trust: org.bluez.Error.Failed",
		"Device " + mac + " not available",
	} {
		if done, _ := trust(line); !done {
			t.Errorf("the trust step did not end on %q", line)
		}
	}
	if done, _ := trust("trust " + mac); done {
		t.Error("the echo of trust ended its step")
	}
	for _, line := range []string{
		"Default agent request successful",
		"No agent is registered",
		"Failed to request default agent: org.bluez.Error.DoesNotExist",
	} {
		if done, _ := bzDefaultAgentAnswer(line); !done {
			t.Errorf("default-agent did not end on %q", line)
		}
	}
	for _, line := range []string{"Agent registered", "Agent is already registered", "Failed to register agent object"} {
		if done, _ := bzAgentOnAnswer(line); !done {
			t.Errorf("agent did not end on %q", line)
		}
	}
	if done, _ := bzAgentOnAnswer("agent NoInputNoOutput"); done {
		t.Error("the echo of agent ended its step")
	}
	if done, _ := bzScanStarted("[CHG] Controller 00:1A:7D:DA:71:13 Discovering: yes"); !done {
		t.Error("the adapter's Discovering event did not start the search")
	}
}

func TestBzDeviceNamesNeverAnswerAStep(t *testing.T) {
	neighbour := "[NEW] Device 11:22:33:44:55:66 "
	for _, line := range []string{
		neighbour + "Pairing successful",
		neighbour + "Connection successful",
		neighbour + "Device has been removed",
		neighbour + "Discovery started",
	} {
		if bzAnswerHas(line, "Pairing successful", "Connection successful", "Device has been removed", "Discovery started") {
			t.Errorf("a device's name answered a step: %q", line)
		}
		if done, _ := bzScanStarted(line); done {
			t.Errorf("a device's name started a search: %q", line)
		}
	}
	withShortBzWaits(t)
	mac := bzTestPad
	fake := newBzFake(bzPairReplies(
		[]string{neighbour + "Pairing successful", "Failed to pair: org.bluez.Error.AuthenticationFailed"},
		[]string{"Connection successful"},
	), bzOpening...)
	withBzFake(t, fake)
	withBzInput(t, nil)
	out, err := bzPair(context.Background(), mac, "Wireless Controller", nil)
	if err == nil || out.Paired || !strings.Contains(bzErrText(err), "refused the pairing") {
		t.Fatalf("a neighbour's name finished the pairing: %+v, %v", out, err)
	}
}

// ── Input devices ─────────────────────────────────────────────────────────

func TestBzExternalPadsFromInputDevices(t *testing.T) {
	devs := bzParseInputDevices([]byte(bzTestInputDevices))
	if len(devs) != 6 {
		t.Fatalf("entries = %d, want 6", len(devs))
	}
	pads := bzExternalPads(devs, 64)
	if len(pads) != 1 || !strings.HasSuffix(pads[0].Name, "DualSense Wireless Controller") {
		t.Fatalf("pads = %+v; want only the DualSense (not the built-in pad, its touchpad, its motion sensor or the keyboard)", pads)
	}
	if got := bzPadSummary(pads); got != "DualSense Wireless Controller" {
		t.Fatalf("summary = %q", got)
	}
	macs := bzPadMACs(devs, 64)
	if !macs[bzTestPad] || len(macs) != 1 {
		t.Fatalf("pad addresses = %v", macs)
	}
	usb := bzInputDev{Bus: 0x03, Name: "Microsoft X-Box 360 pad", Sysfs: "/devices/platform/soc/usbc0/input/input11", KeyBits: "7cdb000000000000 0 0 0 0"}
	if got := bzPadSummary(bzExternalPads(append(devs, usb), 64)); got != "DualSense Wireless Controller + X-Box 360 pad" {
		t.Fatalf("summary with a USB pad = %q", got)
	}
	// A generic HID pad reports joystick buttons (BTN_TRIGGER 0x120 on).
	generic := bzInputDev{Bus: 0x05, Name: "Wireless Controller", KeyBits: "3fff00000000 0 0 0 0"}
	if !generic.gamepad(64) {
		t.Fatal("joystick buttons not read as a pad")
	}
	// A 32-bit kernel prints 32-bit words.
	if !bzKeyBitsAny("ffff0000 0 0 0 0 0 0 0 0 0", 0x120, 0x13f, 32) {
		t.Fatal("32-bit bitmap not read")
	}
	// A Bluetooth pad whose address the kernel did not record is counted apart.
	_, anon := bzPadReading(append(devs, generic), 64)
	if anon != 1 {
		t.Fatalf("pads without an address = %d, want 1", anon)
	}
}

func TestBzPadPresentMatchesTheAddress(t *testing.T) {
	devs := bzParseInputDevices([]byte(bzTestInputDevices))
	if !bzPadPresent(devs, devs, strings.ToLower(bzTestPad)) {
		t.Fatal("the pad's own address did not match")
	}
	if bzPadPresent(devs, devs, "00:11:22:33:44:55") {
		t.Fatal("another address matched an old pad")
	}
	fresh := bzInputDev{Bus: 0x05, Name: "Wireless Controller", Sysfs: "/devices/platform/soc/bt/input/input12", KeyBits: bzTestPadKeys}
	if !bzPadPresent(append(devs, fresh), devs, "00:11:22:33:44:55") {
		t.Fatal("a new Bluetooth pad without an address was not counted")
	}
}

func TestBtControllerSummaryUsesTheCache(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pads are read from /proc on Linux")
	}
	usb := bzInputDev{Bus: 0x03, Name: "Microsoft X-Box 360 pad", Sysfs: "/devices/platform/soc/usbc0/input/input11", KeyBits: "7cdb000000000000 0 0 0 0"}
	withBzInput(t, append(bzParseInputDevices([]byte(bzTestInputDevices)), usb))
	t.Cleanup(func() {
		bzPadCache.mu.Lock()
		bzPadCache.at, bzPadCache.text, bzPadCache.btText, bzPadCache.busy = time.Time{}, "", "", false
		bzPadCache.mu.Unlock()
	})
	bzRefreshPadCache()
	if got := btControllerSummary(); got != "DualSense Wireless Controller + X-Box 360 pad" {
		t.Fatalf("btControllerSummary() = %q", got)
	}
	// The Bluetooth devices row names the Bluetooth pads only.
	if got := btSettingsValue(nil); got != "DualSense Wireless Controller" {
		t.Fatalf("btSettingsValue() = %q", got)
	}
}

func TestBtRadioCachedStateNeverBlocks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the radio is read on Linux only")
	}
	release := make(chan struct{})
	prevBackend, prevProbe := bzRadioBackend, bzRadioProbe
	bzRadioBackend = func() string { return "bluetoothctl" }
	bzRadioProbe = func() (bool, bool) {
		<-release
		return true, true
	}
	reset := func() {
		bzRadioCache.mu.Lock()
		bzRadioCache.on, bzRadioCache.known, bzRadioCache.at, bzRadioCache.busy = false, false, time.Time{}, false
		bzRadioCache.mu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		bzRadioBackend, bzRadioProbe = prevBackend, prevProbe
		reset()
	})
	// The probe blocks until release, which comes only after the call has
	// returned: a call that waited for the radio would never return here.
	returned := make(chan bool, 1)
	go func() {
		_, known := btRadioCachedState()
		returned <- known
	}()
	select {
	case known := <-returned:
		if known {
			t.Fatal("known before the first reading")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("btRadioCachedState waited for the radio")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		on, known := btRadioCachedState()
		if known {
			if !on {
				t.Fatal("reading says off")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the reading never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBtDevicesAvailableCachesTheLookup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pairing is a Linux feature")
	}
	prev := bzToolLookup
	looked := make(chan struct{}, 4)
	bzToolLookup = func() bool {
		looked <- struct{}{}
		return true
	}
	reset := func() {
		bzToolCache.mu.Lock()
		bzToolCache.at, bzToolCache.ok, bzToolCache.busy = time.Time{}, false, false
		bzToolCache.mu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		bzToolLookup = prev
		reset()
	})
	if !btDevicesAvailable() || !btDevicesAvailable() {
		t.Fatal("bluetoothctl not found")
	}
	if n := len(looked); n != 1 {
		t.Fatalf("looked up %d times, want once", n)
	}
	<-looked
	// A stale answer is still given at once; the lookup runs off the caller's
	// thread.
	bzToolCache.mu.Lock()
	bzToolCache.at = time.Now().Add(-time.Minute)
	bzToolCache.mu.Unlock()
	if !btDevicesAvailable() {
		t.Fatal("the cached answer was lost")
	}
	select {
	case <-looked:
	case <-time.After(5 * time.Second):
		t.Fatal("a stale answer was not looked up again")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		bzToolCache.mu.Lock()
		busy := bzToolCache.busy
		bzToolCache.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ── Flows ─────────────────────────────────────────────────────────────────

func TestBzPairWaitsForTheController(t *testing.T) {
	withShortBzWaits(t)
	mac := bzTestPad
	fake := newBzFake(bzPairReplies(
		[]string{"Attempting to pair with " + mac, "[CHG] Device " + mac + " Connected: yes",
			"[CHG] Device " + mac + " Paired: yes", "Pairing successful"},
		[]string{"Attempting to connect to " + mac, "Failed to connect: org.bluez.Error.AlreadyConnected"},
	), bzOpening...)
	starts := withBzFakes(t, fake)
	pad := bzInputDev{Bus: 0x05, Name: "Wireless Controller", Uniq: strings.ToLower(mac),
		Sysfs: "/devices/platform/soc/bt/input/input7", KeyBits: bzTestPadKeys}
	withBzInput(t, nil, []bzInputDev{pad})
	var stages []string
	out, err := bzPair(context.Background(), mac, "Wireless Controller", func(s string) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !out.Paired || !out.Connected || !out.Controller || out.ConnectErr != nil {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Device.kind() != bzKindController || !out.Device.Trusted {
		t.Fatalf("device = %+v", out.Device)
	}
	if got := starts(); len(got) != 1 || strings.Join(got[0], " ") != "--agent=NoInputNoOutput" {
		t.Fatalf("session started with %q, want its own NoInputNoOutput agent", got)
	}
	want := []string{"agent NoInputNoOutput", "default-agent", "scan on", "scan off", "pair " + mac,
		"trust " + mac, "connect " + mac, "info " + mac, "version"}
	if got := strings.Join(fake.commands(), " | "); got != strings.Join(want, " | ") {
		t.Fatalf("commands:\n%s\nwant:\n%s", got, strings.Join(want, " | "))
	}
	if len(stages) == 0 || stages[len(stages)-1] != "Waiting for the controller…" {
		t.Fatalf("stages = %q", stages)
	}
}

func TestBzPairFallsBackWithoutTheAgentOption(t *testing.T) {
	withShortBzWaits(t)
	mac := bzTestPad
	plain := newBzFake(bzPairReplies([]string{"Pairing successful"}, []string{"Connection successful"}), bzOpening...)
	starts := withBzFakes(t, newBzDeadFake(), plain)
	withBzInput(t, nil)
	out, err := bzPair(context.Background(), mac, "Wireless Controller", nil)
	if err != nil || !out.Paired || !out.Connected {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	got := starts()
	if len(got) != 2 || strings.Join(got[0], " ") != "--agent=NoInputNoOutput" || len(got[1]) != 0 {
		t.Fatalf("sessions started with %q, want the option, then a plain session", got)
	}
	cmds := plain.commands()
	if len(cmds) < 3 || cmds[0] != "agent off" || cmds[1] != "agent NoInputNoOutput" || cmds[2] != "default-agent" {
		t.Fatalf("the plain session did not swap its agent first: %q", cmds)
	}
}

func TestBzPairFailures(t *testing.T) {
	mac := bzTestPad
	cases := []struct {
		name  string
		reply []string
		want  string
	}{
		{"refused", []string{"Failed to pair: org.bluez.Error.AuthenticationFailed"}, "refused the pairing"},
		{"no answer", []string{"Failed to pair: org.bluez.Error.ConnectionAttemptFailed"}, "did not answer"},
		{"busy", []string{"Failed to pair: org.bluez.Error.InProgress"}, "busy"},
		{"gone", []string{"Device " + mac + " not available"}, "pairing mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withShortBzWaits(t)
			fake := newBzFake(bzPairReplies(c.reply, []string{"Connection successful"}), bzOpening...)
			withBzFake(t, fake)
			withBzInput(t, nil)
			out, err := bzPair(context.Background(), mac, "Wireless Controller", nil)
			if err == nil || out.Paired {
				t.Fatalf("paired anyway: %+v", out)
			}
			if got := bzErrText(err); !strings.Contains(got, c.want) {
				t.Fatalf("error %q does not say %q", got, c.want)
			}
			for _, cmd := range fake.commands() {
				if strings.HasPrefix(cmd, "connect ") {
					t.Fatal("tried to connect after the pairing failed")
				}
			}
		})
	}
}

func TestBzPairAlreadyPairedCountsAsPaired(t *testing.T) {
	withShortBzWaits(t)
	mac := bzTestPad
	fake := newBzFake(bzPairReplies(
		[]string{"Failed to pair: org.bluez.Error.AlreadyExists"},
		[]string{"Connection successful"},
	), bzOpening...)
	withBzFake(t, fake)
	withBzInput(t, nil)
	out, err := bzPair(context.Background(), mac, "Wireless Controller", nil)
	if err != nil || !out.Paired || !out.Connected {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	// No controller appeared: the outcome must say so, not claim it works.
	if out.Controller {
		t.Fatal("a controller was reported without one in the input devices")
	}
	msg, kind := btOutcomeMessage(bzDevice{MAC: mac, Name: "Wireless Controller"}, out, nil, true)
	if kind != ToastKindWarning || !strings.Contains(msg, "did not create a controller") || strings.Contains(msg, "ready") {
		t.Fatalf("message = %q (%v)", msg, kind)
	}
}

func TestBzPairConnectWithoutDriver(t *testing.T) {
	withShortBzWaits(t)
	mac := bzTestPad
	replies := bzPairReplies(
		[]string{"Pairing successful"},
		[]string{"Failed to connect: org.bluez.Error.Failed br-connection-profile-unavailable"},
	)
	replies["info "+mac] = []string{"Device " + mac + " (public)", "Name: Wireless Controller", "Icon: input-gaming",
		"Paired: yes", "Trusted: yes", "Connected: no"}
	fake := newBzFake(replies, bzOpening...)
	withBzFake(t, fake)
	withBzInput(t, nil)
	out, err := bzPair(context.Background(), mac, "Wireless Controller", nil)
	if err != nil || !out.Paired || out.Connected || out.ConnectErr == nil {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	msg, kind := btOutcomeMessage(bzDevice{MAC: mac, Name: "Wireless Controller"}, out, nil, true)
	if kind != ToastKindWarning || !strings.Contains(msg, "Classic-Bluetooth controllers may not work") {
		t.Fatalf("message = %q (%v)", msg, kind)
	}
}

func TestBzPairAnswersTheAgent(t *testing.T) {
	withShortBzWaits(t)
	mac := bzTestPad
	replies := bzPairReplies(
		[]string{"Attempting to pair with " + mac, "[agent] Confirm passkey 123456 (yes/no): "},
		[]string{"Connection successful"},
	)
	replies["yes"] = []string{"Pairing successful"}
	fake := newBzFake(replies, bzOpening...)
	withBzFake(t, fake)
	withBzInput(t, nil)
	if out, err := bzPair(context.Background(), mac, "Wireless Controller", nil); err != nil || !out.Paired {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	answered := false
	for _, cmd := range fake.commands() {
		if cmd == "yes" {
			answered = true
		}
	}
	if !answered {
		t.Fatal("the agent's question was not answered")
	}
}

func TestBzScanCollectsEventsAndDetails(t *testing.T) {
	pad, hp, anon := bzTestPad, "11:22:33:44:55:66", "5A:11:22:33:44:55"
	fake := newBzFake(map[string][]string{
		"scan on": {"Discovery started", "[NEW] Device " + pad + " Wireless Controller",
			"[NEW] Device " + hp + " WH-1000XM4", "[NEW] Device " + anon + " 5A-11-22-33-44-55"},
		"scan off":       {"Discovery stopped"},
		"paired-devices": {"Device " + pad + " Wireless Controller"},
		"devices": {"Device " + pad + " Wireless Controller", "Device " + hp + " WH-1000XM4",
			"Device " + anon + " 5A-11-22-33-44-55"},
		"info " + pad:  strings.Split(bzTestInfoPad, "\n"),
		"info " + hp:   strings.Split(bzTestInfoHeadset, "\n"),
		"info " + anon: {"Device " + anon + " (random)", "Alias: 5A-11-22-33-44-55", "Paired: no", "Connected: no"},
		"version":      {"Version 5.50"},
	}, bzOpening...)
	withBzFake(t, fake)
	events := 0
	devs, err := bzScan(context.Background(), 50*time.Millisecond, func(bzEvent) { events++ })
	if err != nil {
		t.Fatal(err)
	}
	if events < 3 {
		t.Fatalf("events = %d", events)
	}
	if len(devs) != 3 {
		t.Fatalf("devices = %+v", devs)
	}
	if devs[0].MAC != pad || !devs[0].Connected || !devs[0].Paired {
		t.Fatalf("the connected pad is not first: %+v", devs)
	}
	for _, d := range devs {
		if d.MAC == anon && d.Name != "" {
			t.Fatalf("an address became a name: %+v", d)
		}
		if d.MAC == hp && d.kind() != bzKindAudio {
			t.Fatalf("headphones = %+v", d)
		}
	}
}

func TestBzCollectKnownListsPairedFirst(t *testing.T) {
	pad := bzTestPad
	// A newer BlueZ: no paired-devices, a filter on devices instead. The
	// paired pad comes last in a room full of other devices.
	var everything []string
	for i := 0; i < bzMaxDevices+12; i++ {
		everything = append(everything, fmt.Sprintf("Device 02:00:00:00:%02X:%02X 02-00-00-00-%02X-%02X", i/256, i%256, i/256, i%256))
	}
	everything = append(everything, "Device "+pad+" Wireless Controller")
	fake := newBzFake(map[string][]string{
		"paired-devices": {"Invalid command in menu main: paired-devices", "",
			"Use \"help\" for a list of available commands in a menu."},
		"devices Paired": {"Device " + pad + " Wireless Controller"},
		"devices":        everything,
		"version":        {"Version 5.72"},
	}, bzOpening...)
	withBzFake(t, fake)
	devs, err := bzList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found, unpaired := false, 0
	for _, d := range devs {
		if d.MAC == pad {
			found = d.Paired && d.Name == "Wireless Controller"
		} else if !d.Paired {
			unpaired++
		}
	}
	if !found {
		t.Fatal("the paired pad was left out of a crowded list")
	}
	if unpaired > bzMaxDevices {
		t.Fatalf("unpaired devices = %d, more than the cap of %d", unpaired, bzMaxDevices)
	}
	asked := false
	for _, cmd := range fake.commands() {
		if cmd == "info "+pad {
			asked = true
		}
	}
	if !asked {
		t.Fatal("the paired pad's details were not asked for")
	}
}

func TestBzDisconnectAndForget(t *testing.T) {
	mac := bzTestPad
	withBzFake(t, newBzFake(map[string][]string{"disconnect " + mac: {"Attempting to disconnect from " + mac, "Successful disconnected"}}, bzOpening...))
	if err := bzDisconnect(context.Background(), mac); err != nil {
		t.Fatal(err)
	}
	withBzFake(t, newBzFake(map[string][]string{"disconnect " + mac: {"Failed to disconnect: org.bluez.Error.NotConnected"}}, bzOpening...))
	if err := bzDisconnect(context.Background(), mac); err != nil {
		t.Fatalf("a device that was not connected: %v", err)
	}
	withBzFake(t, newBzFake(map[string][]string{"remove " + mac: {"[DEL] Device " + mac + " Wireless Controller", "Device has been removed"}}, bzOpening...))
	if err := bzForget(context.Background(), mac); err != nil {
		t.Fatal(err)
	}
	withBzFake(t, newBzFake(map[string][]string{"remove " + mac: {"Device " + mac + " not available"}}, bzOpening...))
	if err := bzForget(context.Background(), mac); err != nil {
		t.Fatalf("a device that is already gone: %v", err)
	}
	withBzFake(t, newBzFake(map[string][]string{"remove " + mac: {"Failed to remove device: org.bluez.Error.Failed"}}, bzOpening...))
	if err := bzForget(context.Background(), mac); err == nil {
		t.Fatal("a failed remove reported success")
	}
}

func TestBzCommandsRefuseABadAddress(t *testing.T) {
	fake := newBzFake(nil, bzOpening...)
	starts := withBzFakes(t, fake)
	withBzInput(t, nil)
	ctx := context.Background()
	bad := bzTestPad + "\nremove 11:22:33:44:55:66"
	if err := bzDisconnect(ctx, bad); !errors.Is(err, errBzNotFound) {
		t.Fatalf("disconnect: %v", err)
	}
	if err := bzForget(ctx, bad); !errors.Is(err, errBzNotFound) {
		t.Fatalf("forget: %v", err)
	}
	if _, err := bzConnect(ctx, bzDevice{MAC: bad}, nil); !errors.Is(err, errBzNotFound) {
		t.Fatalf("connect: %v", err)
	}
	if _, err := bzPair(ctx, bad, "x", nil); !errors.Is(err, errBzNotFound) {
		t.Fatalf("pair: %v", err)
	}
	if n := len(starts()); n != 0 || len(fake.commands()) != 0 {
		t.Fatalf("%d sessions started and %q typed for a bad address", n, fake.commands())
	}
}

// ── The page ──────────────────────────────────────────────────────────────

func TestBtOutcomeMessage(t *testing.T) {
	pad := bzDevice{MAC: bzTestPad, Name: "Wireless Controller", Icon: "input-gaming"}
	msg, kind := btOutcomeMessage(pad, bzPairOutcome{Device: pad, Paired: true, Connected: true, Controller: true}, nil, true)
	if kind != ToastKindSuccess || !strings.Contains(msg, "ready for games and Remote Play") {
		t.Fatalf("ready pad: %q (%v)", msg, kind)
	}
	msg, kind = btOutcomeMessage(pad, bzPairOutcome{Device: pad, Paired: true}, nil, true)
	if kind != ToastKindInfo || !strings.Contains(msg, "press its PS or Home button") {
		t.Fatalf("paired, not connected: %q (%v)", msg, kind)
	}
	msg, kind = btOutcomeMessage(pad, bzPairOutcome{}, bzStepError("pair", "Failed to pair: org.bluez.Error.AuthenticationFailed"), true)
	if kind != ToastKindError || !strings.HasPrefix(msg, "Could not pair Wireless Controller") {
		t.Fatalf("failed pairing: %q (%v)", msg, kind)
	}
	headset := bzDevice{MAC: "11:22:33:44:55:66", Name: "WH-1000XM4", Icon: "audio-headset"}
	msg, kind = btOutcomeMessage(headset, bzPairOutcome{Device: headset, Paired: true, Connected: true}, nil, true)
	if kind != ToastKindSuccess || msg != "WH-1000XM4 is connected" {
		t.Fatalf("headset: %q (%v)", msg, kind)
	}
}

func TestBtMergeListKeepsThePairing(t *testing.T) {
	withBtScreen(t)
	btScreen.devices[bzTestPad] = &bzDevice{MAC: bzTestPad, Name: "Wireless Controller", Paired: true, Trusted: true, Connected: true}
	// A search stopped part way: its devices carry no pairing details.
	btMergeList([]bzDevice{
		{MAC: bzTestPad, Name: "DualSense Wireless Controller", Icon: "input-gaming"},
		{MAC: "11:22:33:44:55:66", Name: "WH-1000XM4"},
	})
	d := btScreen.devices[bzTestPad]
	if !d.Paired || !d.Trusted || !d.Connected || d.Name != "DualSense Wireless Controller" || d.Icon != "input-gaming" {
		t.Fatalf("the paired pad after a merge = %+v", d)
	}
	if hp := btScreen.devices["11:22:33:44:55:66"]; hp == nil || hp.Paired {
		t.Fatalf("the new device = %+v", hp)
	}
	if paired := btPairedList(); len(paired) != 1 || paired[0].MAC != bzTestPad {
		t.Fatalf("Paired section = %+v", paired)
	}
}

func TestBtPadReadingWaitsBeforeNoInput(t *testing.T) {
	withBtScreen(t)
	pad := &bzDevice{MAC: bzTestPad, Name: "Wireless Controller", Icon: "input-gaming", Paired: true, Connected: true}
	btScreen.devices[bzTestPad] = pad
	// Seen connecting a moment ago: no controller yet is "Checking…".
	btScreen.connectedAt[bzTestPad] = time.Now()
	btApplyPadReading(map[string]bool{}, 0)
	if btScreen.noPad[bzTestPad] {
		t.Fatal("No input before the pad had its time to get a controller")
	}
	if spec := btPairedRow(*pad); spec.pill != "Checking…" {
		t.Fatalf("pill = %q, want Checking…", spec.pill)
	}
	// After bzInputWait, no controller is evidence.
	btScreen.connectedAt[bzTestPad] = time.Now().Add(-bzInputWait - time.Second)
	btApplyPadReading(map[string]bool{}, 0)
	if spec := btPairedRow(*pad); !btScreen.noPad[bzTestPad] || spec.pill != "No input" {
		t.Fatalf("pill = %q, noPad %v; want No input", spec.pill, btScreen.noPad[bzTestPad])
	}
	// The controller turns up: ready.
	btApplyPadReading(map[string]bool{bzTestPad: true}, 0)
	if spec := btPairedRow(*pad); btScreen.noPad[bzTestPad] || spec.pill != "Connected" {
		t.Fatalf("pill = %q, want Connected", spec.pill)
	}
	// A Bluetooth controller the kernel recorded no address for counts for a
	// connected pad that has none.
	other := &bzDevice{MAC: "11:22:33:44:55:77", Name: "8BitDo Pro 2", Paired: true, Connected: true}
	btScreen.devices[other.MAC] = other
	btApplyPadReading(map[string]bool{bzTestPad: true}, 1)
	if !btScreen.padOK[other.MAC] || btScreen.noPad[other.MAC] {
		t.Fatalf("a pad without a recorded address: padOK %v, noPad %v", btScreen.padOK[other.MAC], btScreen.noPad[other.MAC])
	}
	// Its controller goes away: it disconnected.
	btApplyPadReading(map[string]bool{bzTestPad: true}, 0)
	if other.Connected {
		t.Fatal("a pad whose controller went away still reads connected")
	}
}

func TestBtRadioAfterSwitch(t *testing.T) {
	if on, known := btRadioAfterSwitch(false, nil, false, false); on || !known {
		t.Fatalf("switched off, adapter gone: %v %v, want off", on, known)
	}
	if _, known := btRadioAfterSwitch(true, nil, false, false); known {
		t.Fatal("on claimed without a reading")
	}
	if on, known := btRadioAfterSwitch(true, nil, true, true); !on || !known {
		t.Fatalf("switched on and read on: %v %v", on, known)
	}
	if _, known := btRadioAfterSwitch(false, errors.New("rfkill failed"), false, false); known {
		t.Fatal("a failed switch claimed a state")
	}
}

func TestBtBusyTextNamesTheOperation(t *testing.T) {
	withBtScreen(t)
	btScreen.devices[bzTestPad] = &bzDevice{MAC: bzTestPad, Name: "Wireless Controller"}
	if !btClaimBusy(bzTestPad, "Pairing…", "pairing") {
		t.Fatal("the first operation was refused")
	}
	if got := btBusyText(); got != "Still pairing Wireless Controller — try again when that finishes" {
		t.Fatalf("busy text = %q", got)
	}
	btClearBusy()
	if btScreen.busyMAC != "" || btScreen.busyVerb != "" {
		t.Fatal("the operation was not cleared")
	}
}
