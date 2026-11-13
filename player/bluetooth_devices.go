package main

// bluetooth_devices.go — pairing Bluetooth controllers (and headphones and
// keyboards) from JukaHub, through BlueZ's bluetoothctl.
//
// Until now JukaHub could switch the radio (systemcontrol.go) and name a
// connected headset (audiooutput.go), but pairing anything meant a PC or the
// firmware's own menu. The Bluetooth page (bluetooth_devices_ui.go) drives the
// flows in this file, and the PS Remote Play panel asks it which pads are
// attached (btControllerSummary) and whether the radio is on
// (btRadioCachedState).
//
// Three facts about the handhelds shape the code:
//
//   - bluetoothctl is the only tool there is, and it can hang (a busy daemon, a
//     radio being reset). The stock BusyBox has no `timeout`, so every run is
//     bounded here by its own deadline and killed when it overruns.
//   - A search only lasts as long as the client that asked for it, and pairing
//     needs an agent registered by that same client. Both therefore run as one
//     interactive bluetoothctl session ("scan on", wait, "scan off", "devices",
//     "info …") instead of one-shot commands, which also works on BlueZ builds
//     older than the --timeout option and the `devices Paired` filter. The
//     session gets a pseudo-terminal where the system has one, so bluetoothctl
//     prints each line as it happens, exactly as it does for a person typing;
//     a plain pipe is the fallback.
//   - "Connected" is not "works". The firmware announces Bluetooth LE input, and
//     a classic-Bluetooth pad (DualShock 4, DualSense, most 8BitDo modes) can
//     pair and connect and still get no input device from the kernel. After a
//     pad connects, the flow waits for the kernel to create its controller
//     (/proc/bus/input/devices, matched by the pad's address) and the page says
//     plainly when none appeared, instead of claiming that it works.
//
// Bluetooth also shares the radio with Wi-Fi on these handhelds (TrimUI's own
// notes ask to switch it off while streaming), which is why a search is one
// ten-second sweep — as the page opens with the radio on and nothing paired,
// and again when asked for — and never a background loop.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/bits"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Timing ────────────────────────────────────────────────────────────────

const (
	// bzScanDuration is how long a search listens. Ten seconds finds a pad in
	// pairing mode; longer only costs Wi-Fi airtime.
	bzScanDuration = 10 * time.Second
	// bzPairWait, bzTrustWait and bzConnectWait bound each step's answer.
	bzPairWait    = 25 * time.Second
	bzTrustWait   = 5 * time.Second
	bzConnectWait = 15 * time.Second
	// bzQuitGrace is how long bluetoothctl gets to leave after "quit" before
	// it is killed.
	bzQuitGrace = 2 * time.Second
	// bzMaxDevices caps the unpaired devices a crowded room can add to a
	// list. Paired devices are never capped: the user's own pad must not be
	// the one left out.
	bzMaxDevices = 48
	// bzInputDevicesPath lists the kernel's input devices.
	bzInputDevicesPath = "/proc/bus/input/devices"
	// bzSentinel is the command whose answer ("Version 5.xx") marks the end of
	// the output before it: bluetoothctl prints lists and info blocks with no
	// end line of their own.
	bzSentinel = "version"
	// bzNoDaemonPrompt is the prompt bluetoothctl shows until it has reached
	// bluetoothd ("Waiting to connect to bluetoothd..."). It is drawn at every
	// start and replaced by "[bluetooth]# " a moment later, so on its own it
	// says nothing; only a step that runs out while it is still up does.
	bzNoDaemonPrompt = "Waiting to connect to bluetoothd"
)

var (
	// bzQuickWait bounds the commands that only talk to the daemon (agent,
	// scan off, devices, info, disconnect, remove). Tests shorten it.
	bzQuickWait = 4 * time.Second
	// bzFindWait is how long pairing looks for the device before it asks for
	// it anyway (a device the daemon still remembers is not announced again).
	bzFindWait = 8 * time.Second
	// bzInputWait is how long a connected pad gets to show up as a controller.
	bzInputWait = 4 * time.Second
	// bzSettleWait is how long a new session gets to learn the daemon's
	// adapters before the first command is typed (bzFlow.settle).
	bzSettleWait = 2 * time.Second
)

// ── Errors ────────────────────────────────────────────────────────────────

var (
	errBzNoTool    = errors.New("this system has no bluetoothctl, so it cannot pair devices")
	errBzNoDaemon  = errors.New("the Bluetooth service is not running — switch Bluetooth on to start it, or restart the handheld")
	errBzNoAdapter = errors.New("no Bluetooth adapter answered — switch Bluetooth on and try again")
	errBzEnded     = errors.New("bluetoothctl stopped answering — try again")
	errBzNotFound  = errors.New("the device is not visible — put it in pairing mode and search again")
	// errBzNoDaemonDesktop is errBzNoDaemon where JukaHub does not start the
	// service itself (bzErrText picks it): a computer's bluetoothd is its
	// owner's to start.
	errBzNoDaemonDesktop = errors.New("the Bluetooth service (bluetoothd) is not running on this computer — start it (systemctl start bluetooth), then try again")
)

// bzError is a failed step, in words a player can act on. noAnswer marks the
// device not answering at all — switched off or out of range — where the page
// adds a controller's wake-up hint.
type bzError struct {
	step     string // "pair", "connect", …
	code     string // BlueZ's error name, for the log
	msg      string
	noAnswer bool
}

func (e *bzError) Error() string { return e.msg }

// bzNoAnswerConnect and bzNoAnswerPair are what the page says about a device
// that never answered, by step; neither names a controller's buttons, which
// the page adds where it knows the device is one.
const (
	bzNoAnswerConnect = "it did not answer — switch it on and keep it close to the handheld"
	bzNoAnswerPair    = "it did not answer — keep it in pairing mode, close to the handheld"
)

// bzStepError turns a failure line ("Failed to pair:
// org.bluez.Error.AuthenticationFailed") into a sentence. BlueZ words the same
// failure differently from one release to the next: 5.54 and 5.55, on the
// handhelds, print only the error's name ("Failed to connect:
// org.bluez.Error.Failed"), while 5.56 and later add a reason
// ("…Error.Failed br-connection-page-timeout", "le-connection-abort-by-local",
// "Host is down"). So reasons are matched by their words where a line has any,
// and a bare Failed on a connection reads as its usual cause on the handhelds:
// the device is off or out of range.
func bzStepError(step, line string) error {
	code := line
	if i := strings.Index(line, "org.bluez.Error."); i >= 0 {
		code = line[i+len("org.bluez.Error."):]
	}
	code = strings.TrimSpace(code)
	lower := strings.ToLower(line)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	msg := ""
	noAnswer := false
	switch {
	case strings.Contains(code, "AuthenticationFailed"), strings.Contains(code, "AuthenticationRejected"),
		strings.Contains(code, "AuthenticationCanceled"), strings.Contains(code, "AuthenticationTimeout"):
		msg = "it refused the pairing — put it in pairing mode again and retry"
	case has("profile-unavailable"), strings.Contains(code, "NotAvailable"), strings.Contains(code, "NotSupported"):
		msg = "this system has no driver for that kind of device"
	case strings.Contains(code, "ConnectionAttemptFailed"),
		has("page timeout", "page-timeout", "host is down", "connection timed out", "connection-timeout",
			"abort-by-local", "aborted-by-local", "software caused connection abort"),
		code == "Failed" && (step == "connect" || step == "pair"):
		noAnswer = true
		msg = bzNoAnswerConnect
		if step == "pair" {
			msg = bzNoAnswerPair
		}
	case has("key-missing"):
		msg = "it no longer has this pairing — forget it here and pair it again"
	case has("connection refused", "-refused"):
		msg = "it refused the connection — forget it here and pair it again"
	case has("abort-by-remote", "aborted-by-remote", "connection reset by peer"):
		msg = "it dropped the connection — try again; if it keeps doing that, forget it and pair it again"
	case has("canceled", "cancelled"):
		msg = "the connection was cancelled — try again"
	case strings.Contains(code, "InProgress"), has("already in progress", "-busy"):
		msg = "Bluetooth is still busy with another device — try again in a few seconds"
	case strings.Contains(code, "NotReady"), has("not-powered"):
		msg = "Bluetooth is off — switch it on first"
	case strings.Contains(code, "DoesNotExist"), has("not available"):
		msg = "it is no longer visible — put it in pairing mode and search again"
	default:
		msg = "Bluetooth said: " + code
	}
	return &bzError{step: step, code: code, msg: msg, noAnswer: noAnswer}
}

// bzIsNoAnswer reports a device that did not answer at all (bzError.noAnswer).
func bzIsNoAnswer(err error) bool {
	var be *bzError
	return errors.As(err, &be) && be.noAnswer
}

// bzErrText is an error as the page and its toasts print it.
func bzErrText(err error) string {
	if err == nil {
		return ""
	}
	var be *bzError
	if errors.As(err, &be) {
		return be.msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Bluetooth took too long to answer — try again"
	}
	if errors.Is(err, errBzNoDaemon) && runtime.GOOS == "linux" && !IsTrimuiDevice() {
		return errBzNoDaemonDesktop.Error()
	}
	return firstLine(err.Error())
}

// bzIsProfileUnavailable reports a connection the system has no driver for —
// on a pad, the sign that this firmware cannot use classic-Bluetooth input.
func bzIsProfileUnavailable(err error) bool {
	var be *bzError
	if !errors.As(err, &be) {
		return false
	}
	lower := strings.ToLower(be.code)
	return strings.Contains(lower, "profile-unavailable") || strings.Contains(lower, "notavailable") ||
		strings.Contains(lower, "notsupported")
}

// ── Devices ───────────────────────────────────────────────────────────────

// bzKind is what a device is, as far as the page needs to know.
type bzKind int

const (
	bzKindOther bzKind = iota
	bzKindController
	bzKindAudio
	bzKindKeyboard
	bzKindMouse
)

// word is the kind as the page writes it.
func (k bzKind) word() string {
	switch k {
	case bzKindController:
		return "Controller"
	case bzKindAudio:
		return "Audio"
	case bzKindKeyboard:
		return "Keyboard"
	case bzKindMouse:
		return "Mouse"
	}
	return "Device"
}

// icon is the home icon drawn for the kind.
func (k bzKind) icon() string {
	switch k {
	case bzKindController:
		return "gamepad"
	case bzKindAudio:
		return "headphones"
	case bzKindKeyboard:
		return "keyboard"
	}
	return "bluetooth"
}

// bzDevice is one device BlueZ knows: paired, or seen by a search.
type bzDevice struct {
	// MAC is the address, upper case and colon separated.
	MAC string
	// Name is "" while the device has only announced its address.
	Name string
	// Icon and Class are BlueZ's own hints at what the device is
	// ("input-gaming", 0x002508 for a gamepad).
	Icon      string
	Class     uint32
	Paired    bool
	Trusted   bool
	Connected bool
	// HID and Audio say which services it offers, from its UUIDs.
	HID   bool
	Audio bool
	RSSI  int
	Seen  time.Time
}

// kind classifies the device.
func (d bzDevice) kind() bzKind { return bzClassify(d) }

// label is the name, or the address while it has none.
func (d bzDevice) label() string {
	if d.Name != "" {
		return d.Name
	}
	return d.MAC
}

// bzPadNameWords are the words a pad's name gives it away by when the device
// sends no icon or class (some BLE pads).
var bzPadNameWords = []string{
	"controller", "gamepad", "dualsense", "dualshock", "xbox", "8bitdo", "joy-con", "joycon",
	"gamesir", "stadia", "pro con",
}

// bzClassify decides what a device is: BlueZ's icon first, then the class of
// device, then the name, then the services it offers.
func bzClassify(d bzDevice) bzKind {
	icon := strings.ToLower(strings.TrimSpace(d.Icon))
	switch {
	case icon == "input-gaming":
		return bzKindController
	case strings.HasPrefix(icon, "audio-"):
		return bzKindAudio
	case icon == "input-keyboard":
		return bzKindKeyboard
	case icon == "input-mouse", icon == "input-tablet":
		return bzKindMouse
	}
	if d.Class != 0 {
		major := (d.Class >> 8) & 0x1f
		minor := (d.Class >> 2) & 0x3f
		switch major {
		case 0x05: // peripheral
			switch minor & 0x0f {
			case 0x01, 0x02: // joystick, gamepad
				return bzKindController
			}
			switch minor >> 4 {
			case 0x01, 0x03:
				return bzKindKeyboard
			case 0x02:
				return bzKindMouse
			}
		case 0x04: // audio and video
			return bzKindAudio
		}
	}
	name := strings.ToLower(d.Name)
	for _, word := range bzPadNameWords {
		if strings.Contains(name, word) {
			return bzKindController
		}
	}
	if d.Audio {
		return bzKindAudio
	}
	if strings.Contains(name, "keyboard") {
		return bzKindKeyboard
	}
	return bzKindOther
}

// bzLess orders a list: connected first, then pads, then named devices, then
// by name and address. Signal strength is left out on purpose: it changes
// while a search runs, and the rows must not jump under the cursor.
func bzLess(a, b bzDevice) bool {
	if a.Connected != b.Connected {
		return a.Connected
	}
	if pa, pb := a.kind() == bzKindController, b.kind() == bzKindController; pa != pb {
		return pa
	}
	if (a.Name != "") != (b.Name != "") {
		return a.Name != ""
	}
	if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
		return la < lb
	}
	return a.MAC < b.MAC
}

// bzSortedDevices flattens a device map into the page's order.
func bzSortedDevices(devs map[string]*bzDevice) []bzDevice {
	out := make([]bzDevice, 0, len(devs))
	for _, d := range devs {
		if d != nil {
			out = append(out, *d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return bzLess(out[i], out[j]) })
	return out
}

// bzMergeInfo copies what an info block says onto dst. The block is the
// daemon's current word on the paired/trusted/connected state.
func bzMergeInfo(dst *bzDevice, src bzDevice) {
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Icon != "" {
		dst.Icon = src.Icon
	}
	if src.Class != 0 {
		dst.Class = src.Class
	}
	dst.Paired, dst.Trusted, dst.Connected = src.Paired, src.Trusted, src.Connected
	dst.HID = dst.HID || src.HID
	dst.Audio = dst.Audio || src.Audio
	if src.RSSI != 0 {
		dst.RSSI = src.RSSI
	}
}

// ── Parsing ───────────────────────────────────────────────────────────────

var (
	// bzANSIPattern matches the colour and cursor sequences bluetoothctl prints
	// even into a pipe, and readline's prompt markers (\x01, \x02).
	bzANSIPattern = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][0-9A-Za-z]|[\x01\x02]`)
	})
	// bzPromptPattern matches the prompt left at the start of a line
	// ("[bluetooth]# ", "[DualSense Wireless Controller]> "). Events ("[NEW]
	// Device …") are followed by a space, never by # or >, so they are kept.
	// A connected device's name can hold brackets of its own ("[TV] Soundbar"
	// makes "[[TV] Soundbar]# "), one level of which is allowed: left on the
	// line, that prompt would hide every answer behind a leading "[".
	bzPromptPattern = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^\[(?:[^\[\]]|\[[^\[\]]*\])*\][#>]\s*`)
	})
)

// bzCleanLine strips colour codes, prompts and padding from one output line.
func bzCleanLine(s string) string {
	s = strings.TrimSpace(bzANSIPattern().ReplaceAllString(s, ""))
	for {
		loc := bzPromptPattern().FindStringIndex(s)
		if loc == nil {
			return s
		}
		s = strings.TrimSpace(s[loc[1]:])
	}
}

// bzNameOrEmpty is a device name, or "" when it is only the address (BlueZ
// falls back to "AA-BB-CC-DD-EE-FF" for a device that sent no name).
func bzNameOrEmpty(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || looksLikeBluetoothAddress(strings.ReplaceAll(name, "-", ":")) {
		return ""
	}
	return name
}

// bzEventKind is the kind of a device event.
type bzEventKind int

const (
	bzEventNew bzEventKind = iota
	bzEventChange
	bzEventGone
)

// bzEvent is one "[NEW]", "[CHG]" or "[DEL]" line about a device.
type bzEvent struct {
	kind bzEventKind
	mac  string
	// name is the name a [NEW] or [DEL] line carries ("" for an address).
	name string
	// key and value are a [CHG] line's property ("Connected", "yes").
	key   string
	value string
}

// bzParseEvent reads a device event. Adapter events ("[CHG] Controller …") and
// anything else are not device events.
func bzParseEvent(line string) (bzEvent, bool) {
	var ev bzEvent
	var rest string
	switch {
	case strings.HasPrefix(line, "[NEW] Device "):
		ev.kind, rest = bzEventNew, strings.TrimPrefix(line, "[NEW] Device ")
	case strings.HasPrefix(line, "[CHG] Device "):
		ev.kind, rest = bzEventChange, strings.TrimPrefix(line, "[CHG] Device ")
	case strings.HasPrefix(line, "[DEL] Device "):
		ev.kind, rest = bzEventGone, strings.TrimPrefix(line, "[DEL] Device ")
	default:
		return bzEvent{}, false
	}
	mac, tail, _ := strings.Cut(rest, " ")
	if !looksLikeBluetoothAddress(mac) {
		return bzEvent{}, false
	}
	ev.mac = strings.ToUpper(mac)
	tail = strings.TrimSpace(tail)
	if ev.kind != bzEventChange {
		ev.name = bzNameOrEmpty(tail)
		return ev, true
	}
	key, value, ok := strings.Cut(tail, ":")
	if !ok {
		return bzEvent{}, false
	}
	ev.key, ev.value = strings.TrimSpace(key), strings.TrimSpace(value)
	return ev, true
}

// bzUnpairedCount is how many devices in devs are not paired.
func bzUnpairedCount(devs map[string]*bzDevice) int {
	n := 0
	for _, d := range devs {
		if d != nil && !d.Paired {
			n++
		}
	}
	return n
}

// bzEntry is devs[mac], added when there is room. A paired device always gets
// one. When the unpaired devices fill the list, a newcomer that sent no name
// is turned away, and a named one takes the place of the unpaired, unnamed
// device seen longest ago — the order a crowded room's devices arrive in is
// arbitrary, and the pad in pairing mode may well be the last. nil means no
// room.
func bzEntry(devs map[string]*bzDevice, mac string, named, paired bool) *bzDevice {
	if d := devs[mac]; d != nil {
		return d
	}
	if !paired && bzUnpairedCount(devs) >= bzMaxDevices {
		if !named {
			return nil
		}
		victim := ""
		var oldest time.Time
		for m, d := range devs {
			if d == nil || d.Paired || d.Name != "" {
				continue
			}
			if victim == "" || d.Seen.Before(oldest) {
				victim, oldest = m, d.Seen
			}
		}
		if victim == "" {
			return nil
		}
		delete(devs, victim)
	}
	d := &bzDevice{MAC: mac, Paired: paired}
	devs[mac] = d
	return d
}

// bzApplyEvent folds an event into a device map. A device that left is
// dropped unless it is paired: a paired pad that powers off stays listed.
func bzApplyEvent(devs map[string]*bzDevice, ev bzEvent, now time.Time) {
	d := devs[ev.mac]
	if ev.kind == bzEventGone {
		if d != nil && !d.Paired {
			delete(devs, ev.mac)
		}
		return
	}
	if d == nil {
		named := ev.name != ""
		paired := false
		if ev.kind == bzEventChange {
			switch ev.key {
			case "Name", "Alias":
				named = bzNameOrEmpty(ev.value) != ""
			case "Paired":
				paired = ev.value == "yes"
			}
		}
		if d = bzEntry(devs, ev.mac, named, paired); d == nil {
			return
		}
	}
	d.Seen = now
	if ev.kind == bzEventNew {
		if ev.name != "" {
			d.Name = ev.name
		}
		return
	}
	bzApplyInfo(d, ev.key+": "+ev.value)
}

// bzDevicesLine parses one line of "devices": "Device AA:BB:… Name".
func bzDevicesLine(line string) (string, string, bool) {
	rest, ok := strings.CutPrefix(line, "Device ")
	if !ok {
		return "", "", false
	}
	mac, name, _ := strings.Cut(rest, " ")
	if !looksLikeBluetoothAddress(mac) {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "", name == "(public)", name == "(random)", strings.HasPrefix(name, "not available"):
		return "", "", false
	}
	return strings.ToUpper(mac), bzNameOrEmpty(name), true
}

// bzInfoHeader reports the address an info block starts with: "Device
// AA:BB:CC:DD:EE:FF (public)", or the bare address older BlueZ prints.
func bzInfoHeader(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "Device ")
	if !ok {
		return "", false
	}
	mac, tail, _ := strings.Cut(rest, " ")
	if !looksLikeBluetoothAddress(mac) {
		return "", false
	}
	switch strings.TrimSpace(tail) {
	case "", "(public)", "(random)":
		return strings.ToUpper(mac), true
	}
	return "", false
}

// bzUUIDIsHID reports a Human Interface Device service: classic (0x1124) or
// Bluetooth LE (0x1812).
func bzUUIDIsHID(lower string) bool {
	return strings.Contains(lower, "00001124-0000-1000-8000-00805f9b34fb") ||
		strings.Contains(lower, "00001812-0000-1000-8000-00805f9b34fb") ||
		strings.Contains(lower, "human interface device")
}

// bzUUIDIsAudio reports a service that plays sound here: an audio sink, a
// headset or a hands-free unit. A phone's gateway services are not.
func bzUUIDIsAudio(lower string) bool {
	for _, id := range []string{"0000110b-0000", "00001108-0000", "0000111e-0000", "audio sink"} {
		if strings.Contains(lower, id) {
			return true
		}
	}
	return false
}

// bzParseRSSI reads "-60" or "0xffffffc4 (-60)".
func bzParseRSSI(value string) int {
	if i := strings.IndexByte(value, '('); i >= 0 {
		if j := strings.IndexByte(value[i:], ')'); j > 0 {
			value = value[i+1 : i+j]
		}
	}
	value = strings.TrimSpace(value)
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	if hex, ok := strings.CutPrefix(value, "0x"); ok {
		if u, err := strconv.ParseUint(hex, 16, 32); err == nil {
			return int(int32(uint32(u)))
		}
	}
	return 0
}

// bzApplyInfo folds one "Key: value" line of an info block, or of a [CHG]
// event, into d. Keys it does not use are ignored.
func bzApplyInfo(d *bzDevice, line string) {
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case "Name", "Alias":
		// The alias is the name unless someone renamed the device, and it falls
		// back to the address, which is no name at all.
		if name := bzNameOrEmpty(value); name != "" {
			d.Name = name
		}
	case "Icon":
		d.Icon = value
	case "Class":
		if fields := strings.Fields(value); len(fields) > 0 {
			raw := strings.TrimPrefix(strings.ToLower(fields[0]), "0x")
			if c, err := strconv.ParseUint(raw, 16, 32); err == nil {
				d.Class = uint32(c)
			}
		}
	case "Paired":
		d.Paired = value == "yes"
	case "Trusted":
		d.Trusted = value == "yes"
	case "Connected":
		d.Connected = value == "yes"
	case "UUID", "UUIDs":
		lower := strings.ToLower(value)
		if bzUUIDIsHID(lower) {
			d.HID = true
		}
		if bzUUIDIsAudio(lower) {
			d.Audio = true
		}
	case "RSSI":
		if rssi := bzParseRSSI(value); rssi != 0 {
			d.RSSI = rssi
		}
	}
}

// bzInfoReader assembles "info" blocks from a session's lines: a header opens
// a block, the "Key: value" lines after it fill it, the next header (or an
// adapter's) closes it. Events interleaved with a block are not key lines and
// leave it alone.
type bzInfoReader struct {
	cur    *bzDevice
	blocks map[string]*bzDevice
}

func (r *bzInfoReader) feed(line string) {
	if mac, ok := bzInfoHeader(line); ok {
		if r.blocks == nil {
			r.blocks = map[string]*bzDevice{}
		}
		d := r.blocks[mac]
		if d == nil {
			d = &bzDevice{MAC: mac}
			r.blocks[mac] = d
		}
		r.cur = d
		return
	}
	if r.cur == nil {
		return
	}
	if strings.HasPrefix(line, "Controller ") {
		r.cur = nil
		return
	}
	if strings.HasPrefix(line, "[") {
		return
	}
	bzApplyInfo(r.cur, line)
}

// bzAgentAnswer is what a session types back when bluetoothctl's agent asks
// something. Pads and headsets pair with "Just Works", which some BlueZ builds
// still turn into a yes/no question, and a legacy device asks for a PIN that
// is 0000 on every pad that uses one. Only the agent's own lines count: a
// nearby device named "x (yes/no)" shows up in a "[NEW] Device" line and must
// not make the session type anything.
func bzAgentAnswer(line string) string {
	if !strings.HasPrefix(line, "[agent]") {
		return ""
	}
	switch {
	case strings.Contains(line, "(yes/no)"):
		return "yes"
	case strings.Contains(line, "Enter PIN code"):
		return "0000"
	}
	return ""
}

// bzFatal reports a line that ends any flow: no adapter. (No daemon is not a
// line: bluetoothctl shows its "Waiting to connect to bluetoothd..." prompt at
// every start — see bzFlow.note.)
func bzFatal(line string) error {
	if bzAnswerHas(line, "No default controller available") {
		return errBzNoAdapter
	}
	return nil
}

// bzAnswerHas reports whether line is one of bluetoothctl's own answers that
// starts with one of prefixes. Device events ("[NEW] Device … <name>") never
// count: a device's name is free text, and a neighbour called "Pairing
// successful" must not finish a pairing. Prompts are already stripped, so a
// real answer never starts with "[".
func bzAnswerHas(line string, prefixes ...string) bool {
	if strings.HasPrefix(line, "[") {
		return false
	}
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// bzAdapterSays reports an adapter event ("[CHG] Controller 00:1A:… Discovering:
// yes") that ends with suffix. A device's name cannot produce one: device events
// start with "[NEW] Device" or "[CHG] Device".
func bzAdapterSays(line, suffix string) bool {
	return strings.HasPrefix(line, "[CHG] Controller ") && strings.HasSuffix(line, suffix)
}

// The answers the agent steps end with. "No agent is registered" answers both
// "agent off" (there was none) and "default-agent" (it went away).
func bzAgentOffAnswer(line string) (bool, error) {
	return bzAnswerHas(line, "Agent unregistered", "No agent is registered", "Failed to unregister agent"), nil
}

func bzAgentOnAnswer(line string) (bool, error) {
	return bzAnswerHas(line, "Agent registered", "Agent is already registered", "Failed to register agent"), nil
}

func bzDefaultAgentAnswer(line string) (bool, error) {
	return bzAnswerHas(line, "Default agent request", "No agent is registered", "Failed to request default agent"), nil
}

// bzTrustAnswer is the answer to "trust mac": success, a failure to set it, or
// a device the daemon does not know. A trust that fails is not fatal: the pad
// works, it only does not reconnect by itself.
func bzTrustAnswer(mac string) func(string) (bool, error) {
	return func(line string) (bool, error) {
		return bzAnswerHas(line, "Changing "+mac+" trust succeeded", "Failed to set "+mac+" trust",
			"Device "+mac+" not available"), nil
	}
}

// bzValidMAC is mac in the form the commands use (upper case), or false when
// it is not a Bluetooth address: nothing but an address is ever typed into a
// session after a command word.
func bzValidMAC(mac string) (string, bool) {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	return mac, looksLikeBluetoothAddress(mac)
}

// ── Sessions ──────────────────────────────────────────────────────────────

// bzSession is one running bluetoothctl. Tests replace bzStartSession with a
// scripted fake.
type bzSession interface {
	send(cmd string) error
	lines() <-chan string
	close()
}

// bzStartSession starts bluetoothctl with args for one flow, killed at the
// latest after limit.
var bzStartSession = bzStartProcess

// bzProcess is a real bluetoothctl.
type bzProcess struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    chan string
	done   chan struct{}
	exited chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

// bzChildEnv is the environment bluetoothctl runs with: the firmware's own
// library path and PATH rather than JukaHub's (childenv.go), and a dumb
// terminal so readline keeps its cursor tricks to itself.
func bzChildEnv() []string {
	return append(screenChildEnv(os.Environ()), "TERM=dumb")
}

func bzStartProcess(ctx context.Context, limit time.Duration, args ...string) (bzSession, error) {
	// Found beyond PATH too (bluetooth_tools.go): a launcher's PATH can miss
	// the directory the firmware keeps it in.
	tool, ok := btToolPath("bluetoothctl")
	if !ok {
		return nil, errBzNoTool
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	newCmd := func() *exec.Cmd {
		cmd := exec.CommandContext(runCtx, tool, args...)
		cmd.Env = bzChildEnv()
		return cmd
	}
	cmd, in, out, err := bzStartOnPTY(newCmd())
	if err != nil && !errors.Is(err, exec.ErrNotFound) {
		// No terminal here, or the child could not take it as its own: plain
		// pipes work everywhere, only less promptly.
		logDebug("bluetooth", "no terminal for bluetoothctl (%v); using pipes", err)
		cmd, in, out, err = bzStartOnPipes(newCmd())
	}
	if err != nil {
		cancel()
		return nil, err
	}
	p := &bzProcess{
		cmd:    cmd,
		in:     in,
		out:    make(chan string, 256),
		done:   make(chan struct{}),
		exited: make(chan struct{}),
		cancel: cancel,
	}
	go func() {
		bzPumpLines(out, p.out, p.done)
		_ = out.Close()
	}()
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// bzStartOnPTY starts cmd on a fresh pseudo-terminal; the master end is both
// its input and its output.
func bzStartOnPTY(cmd *exec.Cmd) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	master, slave, err := bzOpenPTY()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = bzPTYAttr()
	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, nil, nil, err
	}
	_ = slave.Close()
	return cmd, master, master, nil
}

// bzStartOnPipes starts cmd with a pipe for its input and one for its output
// (stdout and stderr together).
func bzStartOnPipes(cmd *exec.Cmd) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, nil, nil, err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, nil, nil, err
	}
	_ = pw.Close()
	return cmd, stdin, pr, nil
}

func (p *bzProcess) send(cmd string) error {
	select {
	case <-p.exited:
		return errBzEnded
	default:
	}
	if _, err := io.WriteString(p.in, cmd+"\n"); err != nil {
		return errBzEnded
	}
	return nil
}

func (p *bzProcess) lines() <-chan string { return p.out }

// close asks bluetoothctl to quit, kills it when it does not, and releases the
// line pump.
func (p *bzProcess) close() {
	p.once.Do(func() {
		_, _ = io.WriteString(p.in, "quit\n")
		select {
		case <-p.exited:
		case <-time.After(bzQuitGrace):
			p.cancel()
			<-p.exited
		}
		_ = p.in.Close()
		p.cancel()
		close(p.done)
	})
}

// bzPumpLines reads a session's output, splits it at \n and \r, cleans each
// line and hands it on until the output ends or done closes. An agent's
// question ends without a newline and then waits for the answer, so a pending
// fragment that is one is handed on as a line of its own.
func bzPumpLines(r io.Reader, out chan<- string, done <-chan struct{}) {
	defer close(out)
	send := func(raw []byte) bool {
		line := bzCleanLine(string(raw))
		if line == "" {
			return true
		}
		select {
		case out <- line:
			return true
		case <-done:
			return false
		}
	}
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.IndexAny(pending, "\r\n")
				if i < 0 {
					break
				}
				if !send(pending[:i]) {
					return
				}
				pending = pending[i+1:]
			}
			// The "no daemon" prompt ends without a newline too, and while
			// bluetoothd is down nothing follows it: it is handed on as a line
			// so a step can tell that from a slow answer (bzFlow.note).
			if p := string(pending); bzAgentAnswer(bzCleanLine(p)) != "" || strings.Contains(p, bzNoDaemonPrompt) {
				if !send(pending) {
					return
				}
				pending = pending[:0]
			}
			if len(pending) > 16<<10 {
				pending = pending[:0]
			}
		}
		if err != nil {
			send(pending)
			return
		}
	}
}

// ── Flows ─────────────────────────────────────────────────────────────────

// bzFlow is one conversation with a session: it answers the agent, hands
// every device event to onEvent, collects info blocks and waits for the line
// each step ends with.
type bzFlow struct {
	ctx     context.Context
	s       bzSession
	onEvent func(bzEvent)
	info    bzInfoReader
	// typed is what this flow typed, so the terminal's echo of a command is
	// never taken for word from the daemon.
	typed map[string]bool
	// waiting: the last thing bluetoothctl showed is its "no daemon" prompt,
	// and nothing from the daemon came since. bluetoothctl draws that prompt
	// at every start and replaces it within milliseconds, so only a step that
	// runs out while it is still up means bluetoothd is not there.
	waiting bool
}

// send types cmd into the session and remembers it.
func (f *bzFlow) send(cmd string) error {
	if f.typed == nil {
		f.typed = map[string]bool{}
	}
	f.typed[cmd] = true
	return f.s.send(cmd)
}

// note follows bluetoothctl's "no daemon" prompt through one line and returns
// what is left of the line to read ("" for nothing). The prompt is cut out
// wherever it stands; the "[bluetooth]# " prompt redrawn right behind it is
// the daemon answering, while anything else behind it is typing on the old
// prompt. Any other line clears the wait unless it is the echo of something
// typed here.
func (f *bzFlow) note(line string) string {
	i := strings.Index(line, bzNoDaemonPrompt)
	if i < 0 {
		if !f.typed[line] {
			f.waiting = false
		}
		return line
	}
	rest := strings.TrimSpace(strings.TrimLeft(line[i+len(bzNoDaemonPrompt):], "."))
	f.waiting = !bzPromptPattern().MatchString(rest)
	return bzCleanLine(rest)
}

// await reads lines until match reports the step done (true, nil) or failed
// (_, err); the wait running out is (false, nil). A line that ends every flow
// (no adapter), a wait that runs out on the "no daemon" prompt, the context
// and a dead session end it too.
func (f *bzFlow) await(wait time.Duration, match func(string) (bool, error)) (bool, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return false, f.ctx.Err()
		case <-timer.C:
			if f.waiting {
				return false, errBzNoDaemon
			}
			return false, nil
		case line, ok := <-f.s.lines():
			if !ok {
				return false, errBzEnded
			}
			if line = f.note(line); line == "" {
				continue
			}
			if err := bzFatal(line); err != nil {
				return false, err
			}
			f.observe(line)
			if match == nil {
				continue
			}
			if done, err := match(line); done || err != nil {
				return done, err
			}
		}
	}
}

// observe handles what any line can carry, whatever step is waiting.
func (f *bzFlow) observe(line string) {
	if answer := bzAgentAnswer(line); answer != "" {
		_ = f.send(answer)
	}
	if ev, ok := bzParseEvent(line); ok && f.onEvent != nil {
		f.onEvent(ev)
	}
	f.info.feed(line)
}

// settle waits, before anything is typed, until bluetoothctl has the daemon's
// adapters. It starts reading its input as soon as it reaches bluetoothd but
// learns the adapters a moment later, and a command typed in between is
// answered "No default controller available" — which ends a flow as if there
// were no adapter at all. The adapter's own line ("[NEW] Controller …"), a
// change to it, or the automatic agent's "Agent registered" (asked for once
// the objects are in) says it is ready; when none comes the flow goes on
// after bzSettleWait, and its first step decides. Only a session that died
// or a cancelled context fail here.
func (f *bzFlow) settle() error {
	_, err := f.await(bzSettleWait, func(line string) (bool, error) {
		return strings.HasPrefix(line, "[NEW] Controller ") || strings.HasPrefix(line, "[CHG] Controller ") ||
			bzAnswerHas(line, "Agent registered"), nil
	})
	if err != nil && !errors.Is(err, errBzNoDaemon) {
		return err
	}
	return nil
}

// run sends cmd and waits for its answer.
func (f *bzFlow) run(cmd string, wait time.Duration, match func(string) (bool, error)) (bool, error) {
	if err := f.send(cmd); err != nil {
		return false, err
	}
	return f.await(wait, match)
}

// bzStep is one command and the answer that ends it.
type bzStep struct {
	cmd   string
	match func(string) (bool, error)
}

// runSteps runs quick steps in order. A step whose answer does not come in
// time is passed over (the next one still runs); a session that failed ends
// them.
func (f *bzFlow) runSteps(steps []bzStep) error {
	for _, st := range steps {
		if _, err := f.run(st.cmd, bzQuickWait, st.match); err != nil {
			return err
		}
	}
	return nil
}

// runListing sends cmds and then the sentinel, and gives each to every line
// printed before the sentinel's answer.
func (f *bzFlow) runListing(cmds []string, wait time.Duration, each func(string)) error {
	for _, c := range cmds {
		if err := f.send(c); err != nil {
			return err
		}
	}
	if err := f.send(bzSentinel); err != nil {
		return err
	}
	done, err := f.await(wait, func(line string) (bool, error) {
		if strings.HasPrefix(line, "Version ") {
			return true, nil
		}
		if each != nil {
			each(line)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if !done {
		return errBzEnded
	}
	return nil
}

// scanStarted is the answer to "scan on". InProgress means another client is
// already searching; its results arrive here as well.
func bzScanStarted(line string) (bool, error) {
	switch {
	case bzAnswerHas(line, "Discovery started"), bzAdapterSays(line, "Discovering: yes"):
		return true, nil
	case bzAnswerHas(line, "Failed to start discovery"):
		if strings.Contains(line, "InProgress") {
			return true, nil
		}
		return true, bzStepError("scan", line)
	}
	return false, nil
}

// bzScanStopped is the answer to "scan off".
func bzScanStopped(line string) (bool, error) {
	return bzAnswerHas(line, "Discovery stopped", "Failed to stop discovery") ||
		bzAdapterSays(line, "Discovering: no"), nil
}

// bzInfoPerDevice is how much longer an info listing may take per device.
const bzInfoPerDevice = 300 * time.Millisecond

// bzListingLimit bounds a session that lists the devices: the paired listing
// (and its newer spelling), the full one, and the details of as many devices
// as a list holds.
func bzListingLimit() time.Duration {
	return 4*bzQuickWait + time.Duration(bzMaxDevices)*bzInfoPerDevice
}

// collectKnown asks the daemon which devices it knows and the details of each,
// and folds them into devs. The paired ones are asked for first and always
// kept; the unpaired ones fill the rest of the list, named devices first.
func (f *bzFlow) collectKnown(devs map[string]*bzDevice) error {
	paired := map[string]bool{}
	readPaired := func(line string) {
		mac, name, ok := bzDevicesLine(line)
		if !ok {
			return
		}
		paired[mac] = true
		d := bzEntry(devs, mac, name != "", true)
		d.Paired = true
		if name != "" {
			d.Name = name
		}
	}
	// paired-devices up to BlueZ 5.64; later builds dropped it for a filter on
	// "devices" (which an older build would ignore and list everything).
	invalid := false
	err := f.runListing([]string{"paired-devices"}, bzQuickWait, func(line string) {
		if bzAnswerHas(line, "Invalid command") {
			invalid = true
			return
		}
		readPaired(line)
	})
	if err != nil {
		return err
	}
	if invalid {
		if err := f.runListing([]string{"devices Paired"}, bzQuickWait, readPaired); err != nil {
			return err
		}
	}
	var named, unnamed []string
	names := map[string]string{}
	err = f.runListing([]string{"devices"}, bzQuickWait, func(line string) {
		mac, name, ok := bzDevicesLine(line)
		if !ok {
			return
		}
		if _, dup := names[mac]; dup {
			return
		}
		names[mac] = name
		switch {
		case paired[mac]:
			if name != "" {
				devs[mac].Name = name
			}
		case name != "":
			named = append(named, mac)
		default:
			unnamed = append(unnamed, mac)
		}
	})
	if err != nil {
		return err
	}
	for _, mac := range append(named, unnamed...) {
		d := bzEntry(devs, mac, names[mac] != "", false)
		if d != nil && names[mac] != "" {
			d.Name = names[mac]
		}
	}
	if len(devs) == 0 {
		return nil
	}
	macs := make([]string, 0, len(devs))
	for mac := range devs {
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	cmds := make([]string, 0, len(macs))
	for _, mac := range macs {
		cmds = append(cmds, "info "+mac)
	}
	f.info = bzInfoReader{}
	wait := bzQuickWait + time.Duration(len(macs))*bzInfoPerDevice
	if err := f.runListing(cmds, wait, nil); err != nil {
		return err
	}
	for mac, block := range f.info.blocks {
		if d := devs[mac]; d != nil && block != nil {
			bzMergeInfo(d, *block)
		}
	}
	return nil
}

// bzList returns every device the daemon knows (paired ones, and any a
// search found recently), with details.
func bzList(ctx context.Context) ([]bzDevice, error) {
	s, err := bzStartSession(ctx, bzSettleWait+bzListingLimit()+bzQuitGrace)
	if err != nil {
		return nil, err
	}
	defer s.close()
	f := &bzFlow{ctx: ctx, s: s}
	if err := f.settle(); err != nil {
		return nil, err
	}
	devs := map[string]*bzDevice{}
	err = f.collectKnown(devs)
	return bzSortedDevices(devs), err
}

// bzScan searches for dur and returns every device the daemon knows
// afterwards, with details. Events found on the way go to onEvent as they
// arrive, so the page fills while the search runs.
func bzScan(ctx context.Context, dur time.Duration, onEvent func(bzEvent)) ([]bzDevice, error) {
	s, err := bzStartSession(ctx, bzSettleWait+dur+2*bzQuickWait+bzListingLimit()+bzQuitGrace)
	if err != nil {
		return nil, err
	}
	defer s.close()
	devs := map[string]*bzDevice{}
	f := &bzFlow{ctx: ctx, s: s, onEvent: func(ev bzEvent) {
		bzApplyEvent(devs, ev, time.Now())
		if onEvent != nil {
			onEvent(ev)
		}
	}}
	if err := f.settle(); err != nil {
		return nil, err
	}
	if _, err := f.run("scan on", bzQuickWait, bzScanStarted); err != nil {
		return nil, err
	}
	if _, err := f.await(dur, nil); err != nil {
		return nil, err
	}
	if _, err := f.run("scan off", bzQuickWait, bzScanStopped); err != nil {
		return bzSortedDevices(devs), err
	}
	err = f.collectKnown(devs)
	return bzSortedDevices(devs), err
}

// bzPairOutcome is what pairing, or connecting a paired device, achieved.
type bzPairOutcome struct {
	Device    bzDevice
	Paired    bool
	Connected bool
	// Controller is true when the kernel made a controller for the pad: the
	// only proof that it will work in games and in Remote Play.
	Controller bool
	// ConnectErr is why a paired device did not connect (nil when it did).
	ConnectErr error
}

// connectStep connects mac. stepErr is why the device itself did not connect
// (the flow goes on); err is a session that failed (the flow ends). A pad that
// connects itself right after pairing makes "connect" answer InProgress or
// AlreadyConnected, so the Connected event counts as much as the answer.
func (f *bzFlow) connectStep(mac string) (connected bool, stepErr error, err error) {
	done, err := f.run("connect "+mac, bzConnectWait, func(line string) (bool, error) {
		switch {
		case bzAnswerHas(line, "Connection successful"):
			return true, nil
		case bzAnswerHas(line, "Failed to connect"):
			switch {
			case strings.Contains(line, "AlreadyConnected"):
				return true, nil
			case strings.Contains(line, "InProgress"):
				return false, nil
			}
			stepErr = bzStepError("connect", line)
			return true, nil
		case bzAnswerHas(line, "Device "+mac+" not available"):
			stepErr = errBzNotFound
			return true, nil
		}
		if ev, ok := bzParseEvent(line); ok && ev.mac == mac && ev.key == "Connected" && ev.value == "yes" {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return false, nil, err
	}
	if stepErr != nil {
		return false, stepErr, nil
	}
	if !done {
		return false, &bzError{step: "connect", code: "timeout", noAnswer: true,
			msg: "it did not connect in time — switch it on, keep it close and choose Connect again"}, nil
	}
	return true, nil, nil
}

// detailsStep reads mac's info block into d.
func (f *bzFlow) detailsStep(mac string, d *bzDevice) {
	f.info = bzInfoReader{}
	if err := f.runListing([]string{"info " + mac}, bzQuickWait, nil); err != nil {
		return
	}
	if block := f.info.blocks[mac]; block != nil {
		bzMergeInfo(d, *block)
	}
}

// bzPairAgentArgs start a pairing session whose own agent is NoInputNoOutput
// ("Just Works", what every pad and headset pairs with), so bluetoothctl's
// keyboard-and-display agent is never the one asked: it could win a race with
// an in-session "agent off" and then hand a keyboard a passkey the page never
// shows. The "=" spelling is read both by the GOption parser of older BlueZ
// (where the value is optional) and by getopt in newer builds.
var bzPairAgentArgs = []string{"--agent=NoInputNoOutput"}

// bzPairLimit bounds one pairing session: the agent and search steps, the
// pairing, trust and connection, and the details read at the end.
func bzPairLimit() time.Duration {
	return bzSettleWait + 6*bzQuickWait + bzFindWait + bzPairWait + bzTrustWait + bzConnectWait + bzQuitGrace
}

// bzStartPairFlow opens a pairing session with its agent in place. A
// bluetoothctl that does not take the option exits at once; the session then
// starts plain and swaps its own agent for NoInputNoOutput.
func bzStartPairFlow(ctx context.Context) (*bzFlow, error) {
	// With the option the agent registered as the session started: asking
	// again is answered "already registered", or registers it here when the
	// automatic one is still on its way.
	steps := []bzStep{{"agent NoInputNoOutput", bzAgentOnAnswer}, {"default-agent", bzDefaultAgentAnswer}}
	s, err := bzStartSession(ctx, bzPairLimit(), bzPairAgentArgs...)
	if err != nil {
		return nil, err
	}
	f := &bzFlow{ctx: ctx, s: s}
	// A bluetoothctl that refused the option has already exited: the settle
	// step then fails with errBzEnded, like the steps would.
	if err = f.settle(); err == nil {
		err = f.runSteps(steps)
	}
	if err == nil {
		return f, nil
	}
	s.close()
	if !errors.Is(err, errBzEnded) || ctx.Err() != nil {
		return nil, err
	}
	logDebug("bluetooth", "bluetoothctl did not take %v; pairing in a plain session", bzPairAgentArgs)
	if s, err = bzStartSession(ctx, bzPairLimit()); err != nil {
		return nil, err
	}
	f = &bzFlow{ctx: ctx, s: s}
	if err := f.settle(); err != nil {
		s.close()
		return nil, err
	}
	if err := f.runSteps(append([]bzStep{{"agent off", bzAgentOffAnswer}}, steps...)); err != nil {
		s.close()
		return nil, err
	}
	return f, nil
}

// bzPair runs a whole pairing in one session: agent, find, pair, trust,
// connect, details — then, for a pad, waits for the kernel's controller.
// stage reports progress in words for the page ("Pairing…").
func bzPair(ctx context.Context, mac, name string, stage func(string)) (bzPairOutcome, error) {
	mac, valid := bzValidMAC(mac)
	out := bzPairOutcome{Device: bzDevice{MAC: mac, Name: name}}
	if !valid {
		return out, errBzNotFound
	}
	say := func(text string) {
		if stage != nil {
			stage(text)
		}
	}
	before := bzReadInputDevices()
	f, err := bzStartPairFlow(ctx)
	if err != nil {
		return out, err
	}
	s := f.s
	closed := false
	finish := func() {
		if !closed {
			closed = true
			s.close()
		}
	}
	defer finish()

	// A short search makes sure the daemon has the device in front of it, then
	// stops: discovery and pairing compete for the same radio.
	say("Looking for " + out.Device.label() + "…")
	if _, err := f.run("scan on", bzQuickWait, bzScanStarted); err != nil {
		return out, err
	}
	found, err := f.await(bzFindWait, func(line string) (bool, error) {
		ev, ok := bzParseEvent(line)
		return ok && ev.mac == mac && ev.kind != bzEventGone, nil
	})
	if err != nil {
		return out, err
	}
	if _, err := f.run("scan off", bzQuickWait, bzScanStopped); err != nil {
		return out, err
	}
	logDebug("bluetooth", "pairing %s (seen by the search: %v)", bzShortMAC(mac), found)

	say("Pairing…")
	paired, err := f.run("pair "+mac, bzPairWait, func(line string) (bool, error) {
		switch {
		case bzAnswerHas(line, "Pairing successful"):
			return true, nil
		case bzAnswerHas(line, "Failed to pair"):
			if strings.Contains(line, "AlreadyExists") {
				return true, nil
			}
			return true, bzStepError("pair", line)
		case bzAnswerHas(line, "Device "+mac+" not available"):
			return true, errBzNotFound
		}
		if ev, ok := bzParseEvent(line); ok && ev.mac == mac && ev.key == "Paired" && ev.value == "yes" {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return out, err
	}
	if !paired {
		return out, &bzError{step: "pair", code: "timeout",
			msg: "it did not finish pairing — keep it in pairing mode and try again"}
	}
	out.Paired = true
	out.Device.Paired = true

	// Trusted devices may reconnect by themselves: a pad's PS button then
	// brings it back without this page.
	if _, err := f.run("trust "+mac, bzTrustWait, bzTrustAnswer(mac)); err != nil {
		return out, err
	}

	say("Connecting…")
	connected, stepErr, err := f.connectStep(mac)
	if err != nil {
		return out, err
	}
	out.Connected, out.ConnectErr = connected, stepErr
	f.detailsStep(mac, &out.Device)
	if out.Device.Connected {
		out.Connected, out.ConnectErr = true, nil
	}
	out.Device.Paired = true
	finish()

	if out.Connected && out.Device.kind() == bzKindController {
		say("Waiting for the controller…")
		out.Controller = bzWaitForPad(ctx, mac, before, bzInputWait)
	}
	trophyEvent(tkBTPair)
	logInfo("bluetooth", "paired %s: connected=%v controller=%v", bzShortMAC(mac), out.Connected, out.Controller)
	return out, nil
}

// bzConnect reconnects a paired device and, for a pad, waits for its
// controller.
func bzConnect(ctx context.Context, d bzDevice, stage func(string)) (bzPairOutcome, error) {
	mac, valid := bzValidMAC(d.MAC)
	d.MAC = mac
	out := bzPairOutcome{Device: d, Paired: d.Paired}
	if !valid {
		return out, errBzNotFound
	}
	before := bzReadInputDevices()
	s, err := bzStartSession(ctx, bzSettleWait+bzConnectWait+2*bzQuickWait+bzQuitGrace)
	if err != nil {
		return out, err
	}
	f := &bzFlow{ctx: ctx, s: s}
	if err := f.settle(); err != nil {
		s.close()
		return out, err
	}
	connected, stepErr, err := f.connectStep(d.MAC)
	if err != nil {
		s.close()
		return out, err
	}
	out.Connected, out.ConnectErr = connected, stepErr
	f.detailsStep(d.MAC, &out.Device)
	if out.Device.Connected {
		out.Connected, out.ConnectErr = true, nil
	}
	out.Paired = out.Device.Paired
	s.close()
	if out.Connected && out.Device.kind() == bzKindController {
		if stage != nil {
			stage("Waiting for the controller…")
		}
		out.Controller = bzWaitForPad(ctx, d.MAC, before, bzInputWait)
	}
	logInfo("bluetooth", "connect %s: connected=%v controller=%v", bzShortMAC(d.MAC), out.Connected, out.Controller)
	return out, nil
}

// bzSimple runs "verb mac", a command answered with one success or failure
// line. ok and fail are prefixes of bluetoothctl's own answers (device events
// never count); ok is checked first, a failure line holding one of okCodes
// ("Failed to disconnect: …NotConnected") counts as done, and so does a device
// the daemon does not know: there is nothing left to disconnect or remove.
func bzSimple(ctx context.Context, step, verb, mac string, ok, fail, okCodes []string) error {
	mac, valid := bzValidMAC(mac)
	if !valid {
		return errBzNotFound
	}
	s, err := bzStartSession(ctx, bzSettleWait+3*bzQuickWait+bzQuitGrace)
	if err != nil {
		return err
	}
	defer s.close()
	f := &bzFlow{ctx: ctx, s: s}
	if err := f.settle(); err != nil {
		return err
	}
	var stepErr error
	done, err := f.run(verb+" "+mac, 2*bzQuickWait, func(line string) (bool, error) {
		switch {
		case bzAnswerHas(line, ok...), bzAnswerHas(line, "Device "+mac+" not available"):
			return true, nil
		case bzAnswerHas(line, fail...):
			for _, code := range okCodes {
				if strings.Contains(line, code) {
					return true, nil
				}
			}
			stepErr = bzStepError(step, line)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if stepErr != nil {
		return stepErr
	}
	if !done {
		return &bzError{step: step, code: "timeout", msg: "Bluetooth did not answer — try again"}
	}
	return nil
}

// bzDisconnect drops a device's connection; it stays paired. A device that was
// not connected is already disconnected.
func bzDisconnect(ctx context.Context, mac string) error {
	return bzSimple(ctx, "disconnect", "disconnect", mac,
		[]string{"Successful disconnected", "Disconnection successful"},
		[]string{"Failed to disconnect"}, []string{"NotConnected"})
}

// bzForget removes a device and its pairing keys. A device the daemon no
// longer knows ("Device … not available") is already forgotten.
func bzForget(ctx context.Context, mac string) error {
	return bzSimple(ctx, "remove", "remove", mac,
		[]string{"Device has been removed"},
		[]string{"Failed to remove"}, nil)
}

// bzShortMAC is an address as the log writes it: the last two bytes are enough
// to tell devices apart and do not identify one.
func bzShortMAC(mac string) string {
	if len(mac) == 17 {
		return "…" + mac[12:]
	}
	return "?"
}

// ── Input devices ─────────────────────────────────────────────────────────

// bzInputDev is one entry of /proc/bus/input/devices.
type bzInputDev struct {
	Bus     uint64
	Name    string
	Phys    string
	Uniq    string
	Sysfs   string
	KeyBits string
}

// bzReadInputDevices reads the kernel's input devices; tests replace it.
var bzReadInputDevices = func() []bzInputDev {
	data, err := os.ReadFile(bzInputDevicesPath)
	if err != nil {
		return nil
	}
	return bzParseInputDevices(data)
}

// bzParseInputDevices splits /proc/bus/input/devices into its entries.
func bzParseInputDevices(data []byte) []bzInputDev {
	var out []bzInputDev
	var cur bzInputDev
	started := false
	flush := func() {
		if started {
			out = append(out, cur)
		}
		cur = bzInputDev{}
		started = false
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			flush()
			continue
		}
		started = true
		switch {
		case strings.HasPrefix(line, "I:"):
			if i := strings.Index(line, "Bus="); i >= 0 && len(line) >= i+8 {
				cur.Bus, _ = strconv.ParseUint(line[i+4:i+8], 16, 16)
			}
		case strings.HasPrefix(line, "N: Name="):
			cur.Name = strings.Trim(strings.TrimPrefix(line, "N: Name="), "\"")
		case strings.HasPrefix(line, "P: Phys="):
			cur.Phys = strings.TrimPrefix(line, "P: Phys=")
		case strings.HasPrefix(line, "U: Uniq="):
			cur.Uniq = strings.TrimPrefix(line, "U: Uniq=")
		case strings.HasPrefix(line, "S: Sysfs="):
			cur.Sysfs = strings.TrimPrefix(line, "S: Sysfs=")
		case strings.HasPrefix(line, "B: KEY="):
			cur.KeyBits = strings.TrimPrefix(line, "B: KEY=")
		}
	}
	flush()
	return out
}

// virtual reports a uinput device (the firmware's virtual pad, key mappers).
func (d bzInputDev) virtual() bool { return strings.HasPrefix(d.Sysfs, "/devices/virtual/") }

// key identifies one instance of a device: a reconnected pad gets a new sysfs
// node, so it counts as new.
func (d bzInputDev) key() string { return d.Name + "|" + d.Uniq + "|" + d.Sysfs }

// gamepad reports joystick or gamepad buttons (BTN_JOYSTICK 0x120 to
// BTN_THUMBR 0x13e): a pad, not its touchpad or motion sensor, not a keyboard
// or a mouse. wordBits is the kernel's long size.
func (d bzInputDev) gamepad(wordBits int) bool {
	return bzKeyBitsAny(d.KeyBits, 0x120, 0x13f, wordBits)
}

// bzKeyBitsAny reports whether any key code in lo..hi is set in a KEY bitmap:
// hexadecimal words, most significant first, wordBits bits each.
func bzKeyBitsAny(keyBits string, lo, hi, wordBits int) bool {
	words := strings.Fields(keyBits)
	if len(words) == 0 || wordBits <= 0 {
		return false
	}
	for bit := lo; bit <= hi; bit++ {
		idx := len(words) - 1 - bit/wordBits
		if idx < 0 {
			continue
		}
		w, err := strconv.ParseUint(words[idx], 16, 64)
		if err != nil {
			continue
		}
		if w&(1<<uint(bit%wordBits)) != 0 {
			return true
		}
	}
	return false
}

// bzExternalPads lists the controllers attached besides the handheld's own:
// USB (bus 0003) or Bluetooth (0005), not virtual (the firmware's "TRIMUI
// Player1" is a uinput device) and not named TRIMUI.
func bzExternalPads(devs []bzInputDev, wordBits int) []bzInputDev {
	var out []bzInputDev
	for _, d := range devs {
		if d.Bus != 0x03 && d.Bus != 0x05 {
			continue
		}
		if d.virtual() || strings.Contains(strings.ToUpper(d.Name), "TRIMUI") {
			continue
		}
		if d.gamepad(wordBits) {
			out = append(out, d)
		}
	}
	return out
}

// bzPadMACs is the set of Bluetooth addresses that have a controller over
// Bluetooth right now (the kernel records a Bluetooth pad's address as its
// Uniq; a PlayStation pad on a USB cable carries it too, so the bus decides).
func bzPadMACs(devs []bzInputDev, wordBits int) map[string]bool {
	macs, _ := bzPadReading(devs, wordBits)
	return macs
}

// bzPadReading is bzPadMACs, plus how many Bluetooth controllers carry no
// address the kernel recorded: they cannot be matched to a device by address,
// but they are there.
func bzPadReading(devs []bzInputDev, wordBits int) (map[string]bool, int) {
	out := map[string]bool{}
	anon := 0
	for _, d := range bzExternalPads(devs, wordBits) {
		if d.Bus != 0x05 {
			continue
		}
		if looksLikeBluetoothAddress(d.Uniq) {
			out[strings.ToUpper(d.Uniq)] = true
		} else {
			anon++
		}
	}
	return out, anon
}

// bzPadPresent reports whether a controller for mac is attached over
// Bluetooth: one whose Uniq is the address, or, where the kernel did not
// record one, a Bluetooth controller that was not there before.
func bzPadPresent(now, before []bzInputDev, mac string) bool {
	old := map[string]bool{}
	for _, d := range before {
		old[d.key()] = true
	}
	for _, d := range bzExternalPads(now, bits.UintSize) {
		if d.Bus != 0x05 {
			continue
		}
		if strings.EqualFold(d.Uniq, mac) {
			return true
		}
		if !looksLikeBluetoothAddress(d.Uniq) && !old[d.key()] {
			return true
		}
	}
	return false
}

// bzWaitForPad polls the input devices until the pad's controller appears or
// wait runs out.
func bzWaitForPad(ctx context.Context, mac string, before []bzInputDev, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if bzPadPresent(bzReadInputDevices(), before, mac) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// bzShortPadName drops the maker prefix the kernel puts before a pad's name.
func bzShortPadName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"Sony Interactive Entertainment ", "Sony Computer Entertainment ", "Microsoft "} {
		name = strings.TrimPrefix(name, prefix)
	}
	return name
}

// bzPadSummary names pads for a one-line read-out.
func bzPadSummary(pads []bzInputDev) string {
	var names []string
	seen := map[string]bool{}
	for _, p := range pads {
		name := bzShortPadName(p.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) <= 2 {
		return strings.Join(names, " + ")
	}
	return names[0] + " + " + names[1] + " + " + strconv.Itoa(len(names)-2) + " more"
}

// ── What the rest of JukaHub asks ─────────────────────────────────────────

// bzPadCache is btControllerSummary's answer (every external pad), the
// Bluetooth pads alone, and when they were read.
var bzPadCache struct {
	mu     sync.Mutex
	at     time.Time
	text   string
	btText string
	busy   bool
}

// bzPadCacheRead returns the cached summaries and refreshes them off the
// caller's thread when they are more than about two seconds old.
func bzPadCacheRead() (all, bluetooth string) {
	if runtime.GOOS != "linux" {
		return "", ""
	}
	bzPadCache.mu.Lock()
	defer bzPadCache.mu.Unlock()
	if !bzPadCache.busy && time.Since(bzPadCache.at) > 2*time.Second {
		bzPadCache.busy = true
		go bzRefreshPadCache()
	}
	return bzPadCache.text, bzPadCache.btText
}

// btControllerSummary names the controllers attached besides the built-in pad
// ("DualSense Wireless Controller"), over USB or Bluetooth, or "" when there
// are none. It never blocks: the answer is at most about two seconds old,
// refreshed off the caller's thread.
func btControllerSummary() string {
	all, _ := bzPadCacheRead()
	return all
}

// btBluetoothPadSummary is btControllerSummary for Bluetooth pads alone (the
// Bluetooth devices row in Settings must not name a pad on a cable).
func btBluetoothPadSummary() string {
	_, bluetooth := bzPadCacheRead()
	return bluetooth
}

// bzRefreshPadCache reads the input devices into the cache.
func bzRefreshPadCache() {
	pads := bzExternalPads(bzReadInputDevices(), bits.UintSize)
	var overBT []bzInputDev
	for _, p := range pads {
		if p.Bus == 0x05 {
			overBT = append(overBT, p)
		}
	}
	text, btText := bzPadSummary(pads), bzPadSummary(overBT)
	bzPadCache.mu.Lock()
	bzPadCache.text, bzPadCache.btText = text, btText
	bzPadCache.at = time.Now()
	bzPadCache.busy = false
	bzPadCache.mu.Unlock()
}

// bzRadioCache is the radio's last reading.
var bzRadioCache struct {
	mu    sync.Mutex
	on    bool
	known bool
	at    time.Time
	busy  bool
}

var (
	// bzRadioBackend and bzRadioProbe read the radio (systemcontrol.go);
	// tests replace them.
	bzRadioBackend = bluetoothBackend
	bzRadioProbe   = bluetoothEnabled
)

// btRadioCachedState is the Bluetooth radio's last known state. It never
// blocks: a reading older than five seconds is refreshed in the background
// (bluetoothctl can take a second to answer), and known stays false until the
// first reading lands, or for good where there is no Bluetooth control.
func btRadioCachedState() (on bool, known bool) {
	if runtime.GOOS != "linux" {
		return false, false
	}
	bzRadioCache.mu.Lock()
	defer bzRadioCache.mu.Unlock()
	if !bzRadioCache.busy && time.Since(bzRadioCache.at) > 5*time.Second {
		bzRadioCache.busy = true
		go bzRefreshRadio()
	}
	return bzRadioCache.on, bzRadioCache.known
}

// bzRefreshRadio reads the radio into the cache.
func bzRefreshRadio() {
	on, known := btProbeRadio()
	bzRadioCache.mu.Lock()
	bzRadioCache.on, bzRadioCache.known = on, known
	bzRadioCache.at = time.Now()
	bzRadioCache.busy = false
	bzRadioCache.mu.Unlock()
}

// btProbeRadio reads the radio now. The kernel answers first, without waking
// bluetoothd: no adapter registered is off (the firmware's switch takes the
// chip away), and an adapter's HCI_UP flag is its power. Only where the kernel
// cannot be asked does `bluetoothctl show` (bzRadioProbe) decide, which costs a
// process and, while bluetoothd is down, its whole timeout.
//
// On the handhelds a radio without bluetoothd reads off too, whatever its flag
// says (a daemon that crashed leaves the adapter up): nothing pairs or connects
// then, and "off" makes the switch offer "Turn on", which starts the daemon
// (btBringUp) — the error the page shows says the same.
func btProbeRadio() (on, known bool) {
	if runtime.GOOS == "linux" {
		if !btAdapterPresent() {
			return false, true
		}
		if IsTrimuiDevice() && !btDaemonRunningCached() {
			return false, true
		}
		if up, ok := btHCIUp(); ok {
			return up, true
		}
	}
	if bzRadioBackend() != "" {
		return bzRadioProbe()
	}
	return false, false
}

// bzNoteRadio records a state the page has just set or read. The bar's button
// reads the radio again on its next frame.
func bzNoteRadio(on, known bool) {
	bzRadioCache.mu.Lock()
	bzRadioCache.on, bzRadioCache.known = on, known
	bzRadioCache.at = time.Now()
	bzRadioCache.mu.Unlock()
	btQuickKick()
}

// bzToolCache remembers whether bluetoothctl exists.
var bzToolCache struct {
	mu   sync.Mutex
	at   time.Time
	ok   bool
	busy bool
}

// bzToolLookup reports whether bluetoothctl exists — on PATH or in one of the
// system directories a launcher's PATH can miss (btToolPath); tests replace it.
var bzToolLookup = func() bool { return btToolExists("bluetoothctl") }

// btDevicesAvailable reports whether this system can pair devices: Linux with
// bluetoothctl. Settings, the Start menu and the page ask on every frame, so
// the answer is cached: the first one is looked up once (no lock held while
// PATH is walked), and later ones are refreshed off the caller's thread every
// half a minute.
func btDevicesAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	bzToolCache.mu.Lock()
	if !bzToolCache.at.IsZero() {
		if !bzToolCache.busy && time.Since(bzToolCache.at) > 30*time.Second {
			bzToolCache.busy = true
			go bzRefreshToolCache()
		}
		ok := bzToolCache.ok
		bzToolCache.mu.Unlock()
		return ok
	}
	bzToolCache.mu.Unlock()
	return bzRefreshToolCache()
}

// bzRefreshToolCache looks bluetoothctl up and stores the answer.
func bzRefreshToolCache() bool {
	ok := bzToolLookup()
	bzToolCache.mu.Lock()
	bzToolCache.ok = ok
	bzToolCache.at = time.Now()
	bzToolCache.busy = false
	bzToolCache.mu.Unlock()
	return ok
}
