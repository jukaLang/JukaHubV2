package main

// ctlmode_bt.go — Controller Mode's Bluetooth link (beta): the handheld
// becomes a Bluetooth LE gamepad, "HID over GATT", that a phone or a PC
// pairs with in its own Bluetooth settings like any wireless controller. No
// app and no receiver on the other side: Android and Linux read a generic HID
// gamepad as it is; Windows sees a DirectInput "HID-compliant game
// controller" (games that only read XInput need Steam Input or a wrapper);
// iPhone and iPad do not accept generic gamepads at all.
//
// Everything goes through BlueZ's D-Bus API (dbusmini.go), the way BlueZ's
// own example peripherals do it:
//
//   - the stack is brought up the way the Bluetooth page does it
//     (btBringUp: the firmware's script, bluetoothd, rfkill, power);
//   - the adapter is renamed "JukaHub Pad" and made pairable; both are put
//     back when the link stops (the original name is also written to
//     controller-mode/bt-hosts.json first, so a JukaHub that died mid-session
//     still restores it next time);
//   - an Agent1 with DisplayYesNo answers pairing: the six-digit code goes on
//     the handheld's screen (ctlPrompt) and the answer waits for A or B;
//   - a GATT application (GattManager1.RegisterApplication) serves the HID
//     service 0x1812 (HID Information, Report Map, Protocol Mode, HID Control
//     Point, one input Report with its Report Reference), Battery 0x180F and
//     Device Information 0x180A (PnP ID);
//   - an LEAdvertisement1 advertises the HID service with the gamepad
//     appearance, general discoverable, LE only (the adapter's own classic
//     discoverability is left alone, so a PC cannot pair the wrong transport);
//   - a report goes out as PropertiesChanged on the Report characteristic's
//     Value, which BlueZ turns into a notification for every subscribed host:
//     only when it changed, at most one every 8 ms.
//
// Which host is "the" host: BlueZ's StartNotify carries no device, so the
// host is the device that last read or wrote one of the pad's
// characteristics (BlueZ passes it in the options), else the device that
// connected last. A host that has paired with the pad is remembered
// (bt-hosts.json) and counts as connected whenever it reconnects: a bonded
// host keeps its subscription, and BlueZ keeps a paired device's
// notification state across reconnections without asking again.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// ctlBleName is the adapter's name while the pad is on, the one to look
	// for on the phone or PC.
	ctlBleName = "JukaHub Pad"
	// ctlBleAppearance is the GAP appearance "HID Gamepad".
	ctlBleAppearance = 0x03C4
	// ctlBleReportLen is the input report's size (without its report ID).
	ctlBleReportLen = 13
	// ctlBleHatNull is the hat switch's centred value (outside 0..7).
	ctlBleHatNull = 8
	// ctlBleMinGap spaces reports: at most 125 a second.
	ctlBleMinGap = 8 * time.Millisecond
	// ctlBleBatteryEvery is how often the battery level is read again.
	ctlBleBatteryEvery = time.Minute
	// ctlBlePromptLife drops a pairing prompt BlueZ never cancelled.
	ctlBlePromptLife = 65 * time.Second
	// ctlBleAppTimeout bounds RegisterApplication (BlueZ reads every object
	// back before it answers).
	ctlBleAppTimeout = 25 * time.Second
	// ctlBleQuickTimeout bounds the calls made while stopping.
	ctlBleQuickTimeout = 1500 * time.Millisecond
	// ctlBleQuitWait is how long JukaHub's shutdown waits for the pad to put
	// the adapter back.
	ctlBleQuitWait = time.Second
	// ctlBleStoreFile remembers the hosts and the adapter's own name.
	ctlBleStoreFile = "bt-hosts.json"
	// ctlBleMaxHosts bounds the remembered hosts.
	ctlBleMaxHosts = 16
)

// BlueZ's names.
const (
	ctlBleBluez         = "org.bluez"
	ctlBleAdapterIface  = "org.bluez.Adapter1"
	ctlBleDeviceIface   = "org.bluez.Device1"
	ctlBleAgentMgrIface = "org.bluez.AgentManager1"
	ctlBleAgentIface    = "org.bluez.Agent1"
	ctlBleGattMgrIface  = "org.bluez.GattManager1"
	ctlBleAdvMgrIface   = "org.bluez.LEAdvertisingManager1"
	ctlBleAdvIface      = "org.bluez.LEAdvertisement1"
	ctlBleServiceIface  = "org.bluez.GattService1"
	ctlBleCharIface     = "org.bluez.GattCharacteristic1"
	ctlBleDescIface     = "org.bluez.GattDescriptor1"
	ctlBleRejected      = "org.bluez.Error.Rejected"
	ctlBleBluezFailed   = "org.bluez.Error.Failed"
)

// The pad's objects. The GATT application's root holds only GATT objects:
// BlueZ reads everything below it as part of the application.
const (
	ctlBleAgentPath  dbusObjectPath = "/jukahub/ctlpad/agent"
	ctlBleAdvPath    dbusObjectPath = "/jukahub/ctlpad/adv0"
	ctlBleAppPath    dbusObjectPath = "/jukahub/ctlpad/gatt"
	ctlBleHIDPath    dbusObjectPath = "/jukahub/ctlpad/gatt/hid"
	ctlBleInfoPath   dbusObjectPath = "/jukahub/ctlpad/gatt/hid/info"
	ctlBleMapPath    dbusObjectPath = "/jukahub/ctlpad/gatt/hid/map"
	ctlBleProtoPath  dbusObjectPath = "/jukahub/ctlpad/gatt/hid/proto"
	ctlBleCtrlPath   dbusObjectPath = "/jukahub/ctlpad/gatt/hid/ctrl"
	ctlBleReportPath dbusObjectPath = "/jukahub/ctlpad/gatt/hid/report"
	ctlBleRefPath    dbusObjectPath = "/jukahub/ctlpad/gatt/hid/report/ref"
	ctlBleBatPath    dbusObjectPath = "/jukahub/ctlpad/gatt/bat"
	ctlBleLevelPath  dbusObjectPath = "/jukahub/ctlpad/gatt/bat/level"
	ctlBleDisPath    dbusObjectPath = "/jukahub/ctlpad/gatt/dis"
	ctlBlePnpPath    dbusObjectPath = "/jukahub/ctlpad/gatt/dis/pnp"
)

// The page's actions.
const (
	ctlBleActYes        = "bt:yes"
	ctlBleActNo         = "bt:no"
	ctlBleActHide       = "bt:hide"
	ctlBleActCancelPair = "bt:cancelpair"
	ctlBleActRemove     = "bt:remove:"
)

// ctlBleReportMap is the HID report descriptor: one input report, ID 1.
// Buttons 1..15 in the order Android's Generic.kl names them (1 A, 2 B, 3
// unused, 4 X, 5 Y, 6 unused, 7 LB, 8 RB, 9 LT, 10 RT, 11 Back, 12 Start,
// 13 Guide, 14 LS, 15 RS) and a pad bit; the D-pad as a hat switch with a
// null state; the sticks as X/Y and Z/Rz, 16-bit signed, +Y down; the
// triggers as Brake (LT) and Accelerator (RT), 0..255.
var ctlBleReportMap = []byte{
	// Usage Page (Generic Desktop), Usage (Game Pad), Collection (Application), Report ID (1)
	0x05, 0x01, 0x09, 0x05, 0xA1, 0x01, 0x85, 0x01,
	// Usage Page (Button), Usage Minimum (1), Usage Maximum (15), Logical 0..1,
	// Report Size (1), Report Count (15), Input (Data, Variable, Absolute)
	0x05, 0x09, 0x19, 0x01, 0x29, 0x0F, 0x15, 0x00, 0x25, 0x01, 0x75, 0x01, 0x95, 0x0F, 0x81, 0x02,
	// Report Size (1), Report Count (1), Input (Constant): the 16th bit
	0x75, 0x01, 0x95, 0x01, 0x81, 0x03,
	// Usage Page (Generic Desktop), Usage (Hat switch), Logical 0..7, Physical 0..315,
	// Unit (degrees), Report Size (4), Report Count (1), Input (Data, Variable, Absolute, Null State)
	0x05, 0x01, 0x09, 0x39, 0x15, 0x00, 0x25, 0x07, 0x35, 0x00, 0x46, 0x3B, 0x01,
	0x65, 0x14, 0x75, 0x04, 0x95, 0x01, 0x81, 0x42,
	// Unit (none), Physical Maximum (0): physical follows logical again;
	// Report Size (4), Report Count (1), Input (Constant): the rest of the byte
	0x65, 0x00, 0x45, 0x00, 0x75, 0x04, 0x95, 0x01, 0x81, 0x03,
	// Usage (X), Usage (Y), Usage (Z), Usage (Rz), Logical -32768..32767,
	// Report Size (16), Report Count (4), Input (Data, Variable, Absolute)
	0x09, 0x30, 0x09, 0x31, 0x09, 0x32, 0x09, 0x35, 0x16, 0x00, 0x80, 0x26, 0xFF, 0x7F,
	0x75, 0x10, 0x95, 0x04, 0x81, 0x02,
	// Usage Page (Simulation Controls), Usage (Brake), Usage (Accelerator),
	// Logical 0..255, Report Size (8), Report Count (2), Input (Data, Variable, Absolute)
	0x05, 0x02, 0x09, 0xC5, 0x09, 0xC4, 0x15, 0x00, 0x26, 0xFF, 0x00,
	0x75, 0x08, 0x95, 0x02, 0x81, 0x02,
	// End Collection
	0xC0,
}

// ctlBleButtons maps the controller's bits onto HID button numbers (minus
// one, so the bit in the report).
var ctlBleButtons = [...]struct {
	bit uint32
	idx uint
}{
	{ctlBtnA, 0}, {ctlBtnB, 1}, {ctlBtnX, 3}, {ctlBtnY, 4}, {ctlBtnLB, 6}, {ctlBtnRB, 7},
	{ctlBtnLT, 8}, {ctlBtnRT, 9}, {ctlBtnBack, 10}, {ctlBtnStart, 11}, {ctlBtnGuide, 12},
	{ctlBtnLS, 13}, {ctlBtnRS, 14},
}

// ctlBlePnpID is the PnP ID: USB-IF vendor source, pid.codes' VID 0x1209 and
// its test PID 0x0001, version 1.00 (all little-endian).
var ctlBlePnpID = []byte{0x02, 0x09, 0x12, 0x01, 0x00, 0x00, 0x01}

// ctlBleEncodeReport is the input report's value for a pad state.
func ctlBleEncodeReport(st ctlPadState) [ctlBleReportLen]byte {
	var r [ctlBleReportLen]byte
	var b uint16
	for _, m := range ctlBleButtons {
		if st.Buttons&m.bit != 0 {
			b |= 1 << m.idx
		}
	}
	binary.LittleEndian.PutUint16(r[0:], b)
	r[2] = ctlBleHat(st.Buttons)
	binary.LittleEndian.PutUint16(r[3:], uint16(st.LX))
	binary.LittleEndian.PutUint16(r[5:], uint16(st.LY))
	binary.LittleEndian.PutUint16(r[7:], uint16(st.RX))
	binary.LittleEndian.PutUint16(r[9:], uint16(st.RY))
	r[11], r[12] = st.LT, st.RT
	return r
}

// ctlBleHat is the hat switch for the D-pad bits: 0 up, clockwise in eighths,
// ctlBleHatNull when centred (opposite directions cancel).
func ctlBleHat(b uint32) byte {
	up, down := b&ctlBtnUp != 0, b&ctlBtnDown != 0
	left, right := b&ctlBtnLeft != 0, b&ctlBtnRight != 0
	if up && down {
		up, down = false, false
	}
	if left && right {
		left, right = false, false
	}
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
	return ctlBleHatNull
}

// ctlBleUUID is the full 128-bit form of a Bluetooth SIG 16-bit UUID.
func ctlBleUUID(short uint16) string {
	return fmt.Sprintf("%08x-0000-1000-8000-00805f9b34fb", short)
}

// ctlBleCode prints a passkey as "482 913".
func ctlBleCode(passkey uint32) string {
	s := fmt.Sprintf("%06d", passkey%1000000)
	return s[:3] + " " + s[3:]
}

// ctlBleBattery is the battery level for the Battery service (100 when the
// handheld cannot tell).
func ctlBleBattery() byte {
	p := getBatteryPercent()
	if p < 0 || p > 100 {
		return 100
	}
	return byte(p)
}

// ctlBleFail words a failed step with BlueZ's own sentence.
func ctlBleFail(what string, err error) error {
	var de *dbusError
	if errors.As(err, &de) && de.Text != "" {
		return ctlError(what + ": " + de.Text)
	}
	return ctlError(what + ": " + err.Error())
}

// ── Transport ────────────────────────────────────────────────────────────

// ctlBleTransport is the "bt" link. Each Start runs a session; the transport
// only holds the current one.
type ctlBleTransport struct {
	mu sync.Mutex
	// cur is the session Start began and Stop has not ended.
	cur *ctlBleSession
	// ending is the session Stop ended last, maybe still putting the adapter
	// back (a new session and JukaHub's shutdown wait for it).
	ending *ctlBleSession
	// unsupported is set once the stack proved it cannot be a gamepad.
	unsupported string
	// Available's last probe.
	probeAt  time.Time
	probeOK  bool
	probeWhy string
}

// ctlBle is the registered transport.
var ctlBle = &ctlBleTransport{}

func init() {
	ctlModeRegisterTransport(30, ctlBle)
	bootOnShutdown(ctlBleShutdown)
}

// ID is the saved settings' name for the link.
func (t *ctlBleTransport) ID() string { return "bt" }

// Title is the page's name for the link.
func (t *ctlBleTransport) Title() string { return "Bluetooth gamepad (beta)" }

// Subtitle says what it is for.
func (t *ctlBleTransport) Subtitle() string {
	return "Pairs with an Android phone or a PC like a wireless controller — no app needed"
}

// Available answers from a probe at most a few seconds old: a socket and a
// sysfs directory looked at, never a program started.
func (t *ctlBleTransport) Available() (bool, string) {
	if ok, why := ctlBlePlatform(); !ok {
		return false, why
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.unsupported != "" {
		return false, t.unsupported
	}
	if t.probeAt.IsZero() || time.Since(t.probeAt) > 3*time.Second {
		t.probeOK, t.probeWhy = ctlBleProbe()
		t.probeAt = time.Now()
	}
	return t.probeOK, t.probeWhy
}

func ctlBleProbe() (bool, string) {
	if !dbusSystemBusReachable() {
		return false, "The system message bus (D-Bus) is not running, so Bluetooth cannot be reached"
	}
	if IsTrimuiDevice() {
		// Start brings the adapter and bluetoothd up the firmware's way.
		return true, ""
	}
	if !btAdapterPresent() {
		return false, "This computer has no Bluetooth adapter"
	}
	if !btDaemonRunningCached() {
		return false, "The Bluetooth service (bluetoothd) is not running"
	}
	return true, ""
}

func (t *ctlBleTransport) markUnsupported(why string) {
	t.mu.Lock()
	t.unsupported = why
	t.mu.Unlock()
}

func (t *ctlBleTransport) session() *ctlBleSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur
}

// Start begins a session on its own goroutine and returns at once.
func (t *ctlBleTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur != nil && !t.cur.finished() {
		return nil
	}
	s := &ctlBleSession{
		t:         t,
		prev:      t.ending,
		done:      make(chan struct{}),
		kick:      make(chan struct{}, 1),
		devices:   map[dbusObjectPath]*ctlBleDevice{},
		hosts:     map[string]string{},
		protoMode: 1,
		battLevel: 100,
		detail:    "Starting Bluetooth…",
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	// All zeros would read as the D-pad held up (hat 0): a host reading the
	// report before the first state sees every control at rest instead.
	s.report = ctlBleEncodeReport(ctlPadState{})
	if dir := ctlModeDir(); dir != "" {
		s.storeFile = filepath.Join(dir, ctlBleStoreFile)
	}
	t.cur = s
	go s.run()
	return nil
}

// Stop ends the session; its goroutine puts the adapter back.
func (t *ctlBleTransport) Stop() {
	t.mu.Lock()
	s := t.cur
	t.cur = nil
	if s != nil {
		t.ending = s
	}
	t.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

// Send keeps the newest state; the session's goroutine sends it.
func (t *ctlBleTransport) Send(st ctlPadState) {
	s := t.session()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.latest = st
	s.mu.Unlock()
	s.wake()
}

// Status is the session's state, Idle when none runs.
func (t *ctlBleTransport) Status() ctlLinkStatus {
	if s := t.session(); s != nil {
		return s.status()
	}
	return ctlLinkStatus{State: ctlLinkIdle}
}

// Rows: how to pair, a "Remove pairing" line per paired host, and who can use
// the pad.
func (t *ctlBleTransport) Rows() []ctlRow {
	rows := []ctlRow{{
		Label:  "Pair from your phone/PC: look for " + ctlBleName,
		Detail: "Add a new device in its Bluetooth settings, then confirm the code here",
	}}
	if s := t.session(); s != nil {
		rows = append(rows, s.hostRows()...)
	}
	return append(rows, ctlRow{
		Label:  "Works with Android, Windows and Linux",
		Detail: "Windows games that only read Xbox pads need Steam Input; iPhone and iPad do not accept it",
	})
}

// Prompt is the pairing question waiting, or nil.
func (t *ctlBleTransport) Prompt() *ctlPrompt {
	if s := t.session(); s != nil {
		return s.promptCopy()
	}
	return nil
}

// Do runs a prompt's or a row's action.
func (t *ctlBleTransport) Do(action string) {
	s := t.session()
	if s == nil {
		return
	}
	switch {
	case action == ctlBleActYes:
		s.answer(true)
	case action == ctlBleActNo:
		s.answer(false)
	case action == ctlBleActHide:
		s.hidePrompt()
	case action == ctlBleActCancelPair:
		s.cancelPairing()
	case strings.HasPrefix(action, ctlBleActRemove):
		s.removeHost(strings.TrimPrefix(action, ctlBleActRemove))
	}
}

// ctlBleShutdown is JukaHub's exit: the session ends and the adapter gets its
// name back before the process goes (bounded by ctlBleQuitWait).
func ctlBleShutdown() {
	t := ctlBle
	t.mu.Lock()
	s := t.cur
	if s == nil {
		s = t.ending
	}
	t.cur = nil
	t.mu.Unlock()
	if s == nil {
		return
	}
	s.cancel()
	wait := time.NewTimer(ctlBleQuitWait)
	defer wait.Stop()
	select {
	case <-s.done:
	case <-wait.C:
		logWarn("ctlmode", "bluetooth gamepad: still stopping at exit")
	}
}

// ── Session ──────────────────────────────────────────────────────────────

// ctlBleDevice is what the pad knows of one BlueZ device.
type ctlBleDevice struct {
	addr      string
	alias     string
	adapter   dbusObjectPath
	paired    bool
	connected bool
	// since is when it was last seen connecting.
	since time.Time
}

// ctlBleStore is ctlBleStoreFile.
type ctlBleStore struct {
	// Hosts are the hosts that paired with the pad: address → name.
	Hosts map[string]string `json:"hosts"`
	// Alias is the adapter's own name while the pad has renamed it.
	Alias string `json:"alias,omitempty"`
}

// ctlBleSession is one run of the link, from Start to Stop.
type ctlBleSession struct {
	t      *ctlBleTransport
	prev   *ctlBleSession
	ctx    context.Context
	cancel context.CancelFunc
	// done is closed once the adapter is put back and the bus closed.
	done chan struct{}
	// kick wakes the sender.
	kick      chan struct{}
	storeFile string
	// fileMu orders the store's writes.
	fileMu sync.Mutex

	mu      sync.Mutex
	conn    *dbusConn
	adapter dbusObjectPath
	// ready: advertising, the GATT application and the agent are up.
	ready   bool
	detail  string
	errText string
	// The pairing question on screen, the call waiting for its answer, and
	// the device asking.
	prompt     *ctlPrompt
	promptEnd  time.Time
	confirm    *dbusCall
	confirmDev dbusObjectPath
	// latest is the newest pad state; report its value as last encoded.
	latest ctlPadState
	report [ctlBleReportLen]byte
	// resend makes the sender send even an unchanged report (a host just
	// subscribed or came back).
	resend bool
	wasUp  bool
	// The devices BlueZ knows, the remembered hosts, the host, and the device
	// that last read or wrote the pad's characteristics.
	devices map[dbusObjectPath]*ctlBleDevice
	hosts   map[string]string
	peer    dbusObjectPath
	touched dbusObjectPath
	// notifying: the Report's notifications are on (StartNotify).
	notifying  bool
	battNotify bool
	battLevel  byte
	protoMode  byte
	// strictNotify uses encrypt-notify (BlueZ releases that know it).
	strictNotify bool
	// What the session changed on the adapter, to put back.
	origAlias   string
	setAlias    bool
	origPair    bool
	setPairable bool
	// What it registered with BlueZ.
	regAgent bool
	regApp   bool
	regAdv   bool
}

func (s *ctlBleSession) finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *ctlBleSession) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *ctlBleSession) setDetail(text string) {
	s.mu.Lock()
	s.detail = text
	s.mu.Unlock()
}

// lost ends the session with a reason (bluetoothd gone, the radio off).
func (s *ctlBleSession) lost(why string) {
	s.mu.Lock()
	if s.errText == "" {
		s.errText = why
	}
	s.mu.Unlock()
	logWarn("ctlmode", "bluetooth gamepad: %s", why)
	s.cancel()
}

// run is the session's goroutine.
func (s *ctlBleSession) run() {
	defer close(s.done)
	defer s.cancel()
	if p := s.prev; p != nil {
		wait := time.NewTimer(3 * time.Second)
		select {
		case <-p.done:
		case <-wait.C:
		case <-s.ctx.Done():
		}
		wait.Stop()
	}
	if s.ctx.Err() != nil {
		return
	}
	s.loadStore()
	err := s.setup()
	if err == nil {
		err = s.serve()
	}
	if err != nil && s.ctx.Err() == nil {
		logWarn("ctlmode", "bluetooth gamepad: %v", err)
		s.mu.Lock()
		if s.errText == "" {
			s.errText = err.Error()
		}
		s.mu.Unlock()
	}
	s.teardown()
}

// setup brings Bluetooth up and puts the pad on the air.
func (s *ctlBleSession) setup() error {
	if err := btBringUp(s.ctx, s.setDetail); err != nil {
		if s.ctx.Err() != nil {
			return err
		}
		return ctlError("Bluetooth did not start: " + bzErrText(err))
	}
	s.setDetail("Connecting to the Bluetooth service…")
	conn, err := dbusDialSystem(s.onSignal)
	if err != nil {
		return ctlBleFail("Cannot reach the system message bus", err)
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	for _, rule := range []string{
		"type='signal',sender='org.bluez',interface='org.freedesktop.DBus.ObjectManager'",
		"type='signal',sender='org.bluez',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged'",
		"type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='org.bluez'",
	} {
		if err := conn.AddMatch(rule); err != nil {
			return ctlBleFail("The system message bus refused a subscription", err)
		}
	}
	if err := s.findAdapter(conn); err != nil {
		return err
	}
	s.mu.Lock()
	adapter := s.adapter
	s.mu.Unlock()
	if err := s.prepareAdapter(conn, adapter); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}

	s.setDetail("Setting up the gamepad…")
	s.exportObjects(conn)
	if _, err := conn.Call(ctlBleBluez, "/org/bluez", ctlBleAgentMgrIface, "RegisterAgent", "os",
		ctlBleAgentPath, "DisplayYesNo"); err != nil {
		// Pairing then goes to whichever agent is the default.
		logWarn("ctlmode", "bluetooth gamepad: no pairing agent: %v", err)
	} else {
		s.mu.Lock()
		s.regAgent = true
		s.mu.Unlock()
		if _, err := conn.Call(ctlBleBluez, "/org/bluez", ctlBleAgentMgrIface, "RequestDefaultAgent", "o",
			ctlBleAgentPath); err != nil {
			logWarn("ctlmode", "bluetooth gamepad: not the default agent: %v", err)
		}
	}
	if err := s.registerApp(conn, adapter); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if _, err := conn.Call(ctlBleBluez, adapter, ctlBleAdvMgrIface, "RegisterAdvertisement", "oa{sv}",
		ctlBleAdvPath, map[string]dbusVariant{}); err != nil {
		return ctlBleFail("Bluetooth would not advertise the gamepad", err)
	}
	s.mu.Lock()
	s.regAdv = true
	s.ready = true
	s.detail = "Waiting for a phone or PC to pair with " + ctlBleName
	s.updateLinkLocked()
	s.mu.Unlock()
	logInfo("ctlmode", "bluetooth gamepad: advertising as %s on %s", ctlBleName, adapter)
	return nil
}

// findAdapter picks the adapter (hci0 first), checks it can be a GATT server
// that advertises, and reads the devices BlueZ already knows.
func (s *ctlBleSession) findAdapter(conn *dbusConn) error {
	reply, err := conn.Call(ctlBleBluez, "/", dbusObjMgrIface, "GetManagedObjects", "")
	if err != nil {
		return ctlBleFail("The Bluetooth service did not answer", err)
	}
	objs := dbusDict(reply.arg(0))
	var adapters []string
	s.mu.Lock()
	for path, v := range objs {
		ifaces := dbusDict(v)
		if _, ok := ifaces[ctlBleAdapterIface]; ok {
			adapters = append(adapters, path)
		}
		if props, ok := ifaces[ctlBleDeviceIface].(map[string]any); ok {
			if _, seen := s.devices[dbusObjectPath(path)]; !seen {
				s.noteDeviceLocked(dbusObjectPath(path), props)
			}
		}
	}
	s.mu.Unlock()
	if len(adapters) == 0 {
		return ctlError("No Bluetooth adapter is registered with the Bluetooth service")
	}
	sort.Strings(adapters)
	path := adapters[0]
	for _, a := range adapters {
		if strings.HasSuffix(a, "/hci0") {
			path = a
			break
		}
	}
	ifaces := dbusDict(objs[path])
	if _, ok := ifaces[ctlBleGattMgrIface]; !ok {
		why := "This handheld's Bluetooth service cannot host a gamepad (BlueZ 5.42 or newer is needed)"
		s.t.markUnsupported(why)
		return ctlError(why)
	}
	if _, ok := ifaces[ctlBleAdvMgrIface]; !ok {
		why := "This handheld's Bluetooth service cannot advertise over Bluetooth LE (BlueZ too old, or LE switched off)"
		s.t.markUnsupported(why)
		return ctlError(why)
	}
	props := dbusDict(ifaces[ctlBleAdapterIface])
	s.mu.Lock()
	s.adapter = dbusObjectPath(path)
	alias := dbusPropString(props, "Alias")
	if alias == ctlBleName && s.origAlias != "" {
		// An earlier run ended without putting the name back: the stored one
		// is the adapter's own.
		s.setAlias = true
	} else {
		s.origAlias = alias
	}
	s.origPair, _ = dbusPropBool(props, "Pairable")
	s.mu.Unlock()
	if powered, ok := dbusPropBool(props, "Powered"); ok && !powered {
		if err := s.setAdapter(conn, dbusObjectPath(path), "Powered", dbusVariant{Sig: "b", Value: true}); err != nil {
			return ctlBleFail("Bluetooth did not switch on", err)
		}
	}
	return nil
}

// prepareAdapter renames the adapter and makes it pairable, noting what to
// put back.
func (s *ctlBleSession) prepareAdapter(conn *dbusConn, adapter dbusObjectPath) error {
	s.mu.Lock()
	alias, renamed, pairable := s.origAlias, s.setAlias, s.origPair
	s.mu.Unlock()
	if alias != ctlBleName && !renamed {
		// The adapter's own name goes to the store first: a JukaHub that
		// dies now still puts it back next time.
		s.mu.Lock()
		s.setAlias = true
		s.mu.Unlock()
		s.saveStore()
		if err := s.setAdapter(conn, adapter, "Alias", dbusVariant{Sig: "s", Value: ctlBleName}); err != nil {
			logWarn("ctlmode", "bluetooth gamepad: cannot rename the adapter: %v", err)
			s.mu.Lock()
			s.setAlias = false
			s.mu.Unlock()
			s.saveStore()
		}
	}
	if !pairable {
		if err := s.setAdapter(conn, adapter, "Pairable", dbusVariant{Sig: "b", Value: true}); err != nil {
			return ctlBleFail("Bluetooth would not accept pairing", err)
		}
		s.mu.Lock()
		s.setPairable = true
		s.mu.Unlock()
	}
	return nil
}

func (s *ctlBleSession) setAdapter(conn *dbusConn, adapter dbusObjectPath, name string, v dbusVariant) error {
	_, err := conn.CallTimeout(ctlBleQuickTimeout*4, ctlBleBluez, adapter, dbusPropsIface, "Set", "ssv",
		ctlBleAdapterIface, name, v)
	return err
}

// registerApp registers the GATT application. A BlueZ that does not know the
// encrypt-notify flag refuses the whole application, so it is tried once
// more with plain notify.
func (s *ctlBleSession) registerApp(conn *dbusConn, adapter dbusObjectPath) error {
	var err error
	for _, strict := range []bool{true, false} {
		s.mu.Lock()
		s.strictNotify = strict
		s.mu.Unlock()
		_, err = conn.CallTimeout(ctlBleAppTimeout, ctlBleBluez, adapter, ctlBleGattMgrIface, "RegisterApplication",
			"oa{sv}", ctlBleAppPath, map[string]dbusVariant{})
		var de *dbusError
		if err == nil || !errors.As(err, &de) || de.Name != ctlBleBluezFailed || s.ctx.Err() != nil {
			break
		}
		logInfo("ctlmode", "bluetooth gamepad: application refused with encrypt-notify (%v), trying notify", err)
	}
	if err != nil {
		return ctlBleFail("Bluetooth would not take the gamepad's services", err)
	}
	s.mu.Lock()
	s.regApp = true
	s.mu.Unlock()
	return nil
}

// serve sends reports until the session ends.
func (s *ctlBleSession) serve() error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	battery := time.NewTicker(ctlBleBatteryEvery)
	defer battery.Stop()
	var last [ctlBleReportLen]byte
	var lastAt time.Time
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case <-conn.Done():
			return ctlError("The connection to the Bluetooth service closed")
		case <-battery.C:
			s.batteryTick(conn)
			continue
		case <-s.kick:
		}
		if wait := ctlBleMinGap - time.Since(lastAt); wait > 0 {
			pause := time.NewTimer(wait)
			select {
			case <-s.ctx.Done():
				pause.Stop()
				return nil
			case <-pause.C:
			}
		}
		s.mu.Lock()
		rep := ctlBleEncodeReport(s.latest)
		s.report = rep
		up := s.linkUpLocked()
		force := s.resend
		s.resend = false
		s.mu.Unlock()
		if !up || (!force && rep == last) {
			continue
		}
		if err := conn.EmitPropsChanged(ctlBleReportPath, ctlBleCharIface,
			map[string]dbusVariant{"Value": {Sig: "ay", Value: rep[:]}}); err != nil {
			return ctlBleFail("Sending to the Bluetooth service failed", err)
		}
		last, lastAt = rep, time.Now()
	}
}

// batteryTick reads the battery and notifies a subscribed host of a change.
func (s *ctlBleSession) batteryTick(conn *dbusConn) {
	level := ctlBleBattery()
	s.mu.Lock()
	changed := level != s.battLevel
	s.battLevel = level
	notify := s.battNotify && s.linkUpLocked()
	s.mu.Unlock()
	if changed && notify {
		_ = conn.EmitPropsChanged(ctlBleLevelPath, ctlBleCharIface,
			map[string]dbusVariant{"Value": {Sig: "ay", Value: []byte{level}}})
	}
}

// teardown releases the host (every button up, then disconnected), puts the
// adapter's name and pairability back and unregisters; closing the bus also
// makes BlueZ drop whatever is left.
func (s *ctlBleSession) teardown() {
	s.mu.Lock()
	conn, adapter, peer := s.conn, s.adapter, s.peer
	wasUp := s.linkUpLocked()
	confirm := s.confirm
	s.confirm, s.prompt = nil, nil
	s.ready = false
	alias, setAlias := s.origAlias, s.setAlias
	pairable, setPairable := s.origPair, s.setPairable
	regAgent, regApp, regAdv := s.regAgent, s.regApp, s.regAdv
	s.mu.Unlock()
	if conn == nil {
		return
	}
	defer conn.Close()
	if confirm != nil {
		_ = confirm.Fail(ctlBleRejected, "Controller Mode stopped")
	}
	if wasUp {
		neutral := ctlBleEncodeReport(ctlPadState{})
		_ = conn.EmitPropsChanged(ctlBleReportPath, ctlBleCharIface,
			map[string]dbusVariant{"Value": {Sig: "ay", Value: neutral[:]}})
		// A moment for the release to leave before the link drops.
		time.Sleep(60 * time.Millisecond)
	}
	if setAlias && adapter != "" {
		if _, err := conn.CallTimeout(ctlBleQuickTimeout, ctlBleBluez, adapter, dbusPropsIface, "Set", "ssv",
			ctlBleAdapterIface, "Alias", dbusVariant{Sig: "s", Value: alias}); err != nil {
			logWarn("ctlmode", "bluetooth gamepad: adapter name not restored: %v", err)
		} else {
			s.mu.Lock()
			s.setAlias = false
			s.mu.Unlock()
			s.saveStore()
		}
	}
	if setPairable && adapter != "" {
		_, _ = conn.CallTimeout(ctlBleQuickTimeout, ctlBleBluez, adapter, dbusPropsIface, "Set", "ssv",
			ctlBleAdapterIface, "Pairable", dbusVariant{Sig: "b", Value: pairable})
	}
	if wasUp && peer != "" {
		_ = conn.CallNoReply(ctlBleBluez, peer, ctlBleDeviceIface, "Disconnect", "")
	}
	if regAdv {
		_ = conn.CallNoReply(ctlBleBluez, adapter, ctlBleAdvMgrIface, "UnregisterAdvertisement", "o", ctlBleAdvPath)
	}
	if regApp {
		_ = conn.CallNoReply(ctlBleBluez, adapter, ctlBleGattMgrIface, "UnregisterApplication", "o", ctlBleAppPath)
	}
	if regAgent {
		_ = conn.CallNoReply(ctlBleBluez, "/org/bluez", ctlBleAgentMgrIface, "UnregisterAgent", "o", ctlBleAgentPath)
	}
}

// ── Hosts and the link ───────────────────────────────────────────────────

// noteDeviceLocked folds a Device1 property set (all or changed) in.
func (s *ctlBleSession) noteDeviceLocked(path dbusObjectPath, props map[string]any) {
	d := s.devices[path]
	if d == nil {
		d = &ctlBleDevice{}
		s.devices[path] = d
	}
	if v := dbusPropString(props, "Address"); v != "" {
		d.addr = strings.ToUpper(v)
	}
	if v := dbusPropString(props, "Alias"); v != "" {
		d.alias = v
	}
	if v := dbusPropString(props, "Adapter"); v != "" {
		d.adapter = dbusObjectPath(v)
	}
	if v, ok := dbusPropBool(props, "Paired"); ok {
		d.paired = v
	}
	if v, ok := dbusPropBool(props, "Connected"); ok {
		if v && !d.connected {
			d.since = time.Now()
		}
		d.connected = v
	}
}

func (s *ctlBleSession) ourDeviceLocked(d *ctlBleDevice) bool {
	return d != nil && (s.adapter == "" || d.adapter == "" || d.adapter == s.adapter)
}

// linkUpLocked reports a host receiving the pad: connected, and subscribed
// (StartNotify) or a remembered paired host, whose subscription lasts.
func (s *ctlBleSession) linkUpLocked() bool {
	if !s.ready {
		return false
	}
	d := s.devices[s.peer]
	if d == nil || !d.connected {
		return false
	}
	if s.notifying {
		return true
	}
	_, known := s.hosts[d.addr]
	return d.paired && known
}

// lastConnectedLocked is the device of this adapter that connected last,
// limited to remembered hosts when hostsOnly.
func (s *ctlBleSession) lastConnectedLocked(hostsOnly bool) dbusObjectPath {
	var best dbusObjectPath
	var bestAt time.Time
	for path, d := range s.devices {
		if !d.connected || !s.ourDeviceLocked(d) {
			continue
		}
		if _, known := s.hosts[d.addr]; hostsOnly && (!known || !d.paired) {
			continue
		}
		if best == "" || d.since.After(bestAt) {
			best, bestAt = path, d.since
		}
	}
	return best
}

// updateLinkLocked re-reads who the host is after anything changed: a
// remembered host back, a new host paired (remembered from now on), the link
// going up (the current report goes out again).
func (s *ctlBleSession) updateLinkLocked() {
	if d := s.devices[s.peer]; d == nil || !d.connected {
		if p := s.lastConnectedLocked(true); p != "" {
			s.peer = p
		}
	}
	dirty := false
	for _, path := range []dbusObjectPath{s.peer, s.touched} {
		d := s.devices[path]
		if d == nil || !d.paired || d.addr == "" || !s.ourDeviceLocked(d) {
			continue
		}
		if name, ok := s.hosts[d.addr]; ok && name == d.alias {
			continue
		}
		if _, ok := s.hosts[d.addr]; !ok && len(s.hosts) >= ctlBleMaxHosts {
			continue
		}
		s.hosts[d.addr] = d.alias
		dirty = true
	}
	if dirty {
		go s.saveStore()
	}
	up := s.linkUpLocked()
	if up != s.wasUp {
		s.wasUp = up
		s.resend = true
		s.wake()
		if up {
			d := s.devices[s.peer]
			logInfo("ctlmode", "bluetooth gamepad: %s (%s) receives the pad", d.alias, d.addr)
		}
	}
}

// touch notes the device that read or wrote a characteristic.
func (s *ctlBleSession) touch(dev dbusObjectPath) {
	if dev == "" {
		return
	}
	s.mu.Lock()
	s.touched = dev
	if p := s.devices[s.peer]; p == nil || !p.connected {
		if d := s.devices[dev]; d != nil && d.connected {
			s.peer = dev
		}
	}
	s.updateLinkLocked()
	s.mu.Unlock()
}

// hostRows are the "Remove pairing" lines for the remembered hosts BlueZ
// still has paired.
func (s *ctlBleSession) hostRows() []ctlRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hosts) == 0 {
		return nil
	}
	addrs := make([]string, 0, len(s.hosts))
	for a := range s.hosts {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	var rows []ctlRow
	for _, addr := range addrs {
		for _, d := range s.devices {
			if d.addr != addr || !d.paired || !s.ourDeviceLocked(d) {
				continue
			}
			name := d.alias
			if name == "" {
				name = s.hosts[addr]
			}
			if name == "" {
				name = addr
			}
			detail := "Paired · forget it here and on " + name + " to pair again"
			if d.connected {
				detail = "Connected now"
			}
			rows = append(rows, ctlRow{Label: "Remove pairing with " + name, Detail: detail, Action: ctlBleActRemove + addr})
			break
		}
	}
	return rows
}

// status is the page's view of the session.
func (s *ctlBleSession) status() ctlLinkStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expirePromptLocked()
	switch {
	case s.errText != "":
		return ctlLinkStatus{State: ctlLinkError, Detail: s.errText}
	case s.linkUpLocked():
		d := s.devices[s.peer]
		name := d.alias
		if name == "" {
			name = d.addr
		}
		return ctlLinkStatus{State: ctlLinkConnected, Peer: name, PeerID: "bt:" + d.addr, Detail: "Connected over Bluetooth"}
	case s.prompt != nil:
		return ctlLinkStatus{State: ctlLinkPairing, Detail: "Pairing…"}
	}
	return ctlLinkStatus{State: ctlLinkWaiting, Detail: s.detail}
}

// ── Pairing ──────────────────────────────────────────────────────────────

func (s *ctlBleSession) deviceNameLocked(dev dbusObjectPath) string {
	if d := s.devices[dev]; d != nil {
		if d.alias != "" {
			return d.alias
		}
		if d.addr != "" {
			return d.addr
		}
	}
	return "your phone or PC"
}

// expirePromptLocked drops a prompt BlueZ forgot to cancel.
func (s *ctlBleSession) expirePromptLocked() {
	if s.prompt == nil || time.Now().Before(s.promptEnd) {
		return
	}
	s.prompt = nil
	if call := s.confirm; call != nil {
		s.confirm = nil
		go func() { _ = call.Fail(ctlBleRejected, "Not answered on the handheld") }()
	}
}

func (s *ctlBleSession) promptCopy() *ctlPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expirePromptLocked()
	if s.prompt == nil {
		return nil
	}
	p := *s.prompt
	return &p
}

// answer is A or B on the pairing question.
func (s *ctlBleSession) answer(yes bool) {
	s.mu.Lock()
	call, dev := s.confirm, s.confirmDev
	s.confirm, s.prompt = nil, nil
	if yes && dev != "" {
		// The device paired from the handheld counts as the host once it
		// has paired, even before it reads anything.
		s.touched = dev
	}
	s.mu.Unlock()
	if call == nil {
		return
	}
	go func() {
		if yes {
			_ = call.Reply("")
		} else {
			_ = call.Fail(ctlBleRejected, "Refused on the handheld")
		}
	}()
}

func (s *ctlBleSession) hidePrompt() {
	s.mu.Lock()
	s.prompt = nil
	s.mu.Unlock()
}

// cancelPairing is B on a code shown for typing on the other side.
func (s *ctlBleSession) cancelPairing() {
	s.mu.Lock()
	s.prompt = nil
	conn, dev := s.conn, s.confirmDev
	s.mu.Unlock()
	if conn == nil || dev == "" {
		return
	}
	go func() {
		_, _ = conn.CallTimeout(ctlBleQuickTimeout*2, ctlBleBluez, dev, ctlBleDeviceIface, "CancelPairing", "")
	}()
}

// removeHost unpairs a host (Adapter1.RemoveDevice) and forgets it.
func (s *ctlBleSession) removeHost(addr string) {
	s.mu.Lock()
	conn, adapter := s.conn, s.adapter
	var path dbusObjectPath
	name := s.hosts[addr]
	for p, d := range s.devices {
		if d.addr == addr && s.ourDeviceLocked(d) {
			path = p
			if d.alias != "" {
				name = d.alias
			}
			break
		}
	}
	delete(s.hosts, addr)
	if path == s.peer {
		s.peer = ""
	}
	s.mu.Unlock()
	if name == "" {
		name = addr
	}
	go func() {
		s.saveStore()
		if conn == nil || path == "" || adapter == "" {
			return
		}
		if _, err := conn.Call(ctlBleBluez, adapter, ctlBleAdapterIface, "RemoveDevice", "o", path); err != nil {
			logWarn("ctlmode", "bluetooth gamepad: removing %s: %v", addr, err)
			msg := ctlBleFail("Could not remove the pairing", err).Error()
			tbxPost(func() { ShowToast(msg, ToastKindError) })
			return
		}
		tbxPost(func() { ShowToast("Pairing with "+name+" removed", ToastKindSuccess) })
	}()
}

// ── Store ────────────────────────────────────────────────────────────────

func (s *ctlBleSession) loadStore() {
	if s.storeFile == "" {
		return
	}
	raw, err := os.ReadFile(s.storeFile)
	if err != nil {
		return
	}
	var st ctlBleStore
	if err := json.Unmarshal(raw, &st); err != nil {
		logWarn("ctlmode", "bluetooth gamepad: %s unreadable: %v", ctlBleStoreFile, err)
		return
	}
	s.mu.Lock()
	for addr, name := range st.Hosts {
		if len(s.hosts) < ctlBleMaxHosts {
			s.hosts[strings.ToUpper(addr)] = name
		}
	}
	s.origAlias = st.Alias
	s.mu.Unlock()
}

// saveStore writes the hosts and, while the adapter is renamed, its own name.
func (s *ctlBleSession) saveStore() {
	if s.storeFile == "" {
		return
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	s.mu.Lock()
	st := ctlBleStore{Hosts: make(map[string]string, len(s.hosts))}
	for k, v := range s.hosts {
		st.Hosts[k] = v
	}
	if s.setAlias {
		st.Alias = s.origAlias
	}
	s.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.storeFile), 0o700); err != nil {
		logWarn("ctlmode", "bluetooth gamepad: cannot create %s: %v", filepath.Dir(s.storeFile), err)
		return
	}
	if err := AtomicWrite(s.storeFile, data, 0o600); err != nil {
		logWarn("ctlmode", "bluetooth gamepad: cannot save %s: %v", ctlBleStoreFile, err)
	}
}

// ── BlueZ's signals ──────────────────────────────────────────────────────

// onSignal follows devices coming, going, connecting and pairing, and the
// stack going away (on the bus's reading goroutine).
func (s *ctlBleSession) onSignal(m *dbusMsg) {
	switch m.Member {
	case "InterfacesAdded":
		path, _ := m.arg(0).(dbusObjectPath)
		props, ok := dbusDict(m.arg(1))[ctlBleDeviceIface].(map[string]any)
		if !ok {
			return
		}
		s.mu.Lock()
		s.noteDeviceLocked(path, props)
		s.updateLinkLocked()
		s.mu.Unlock()
	case "InterfacesRemoved":
		path, _ := m.arg(0).(dbusObjectPath)
		for _, name := range dbusStrings(m.arg(1)) {
			switch name {
			case ctlBleDeviceIface:
				s.mu.Lock()
				delete(s.devices, path)
				s.updateLinkLocked()
				s.mu.Unlock()
			case ctlBleAdapterIface:
				s.mu.Lock()
				ours := path == s.adapter
				s.mu.Unlock()
				if ours {
					s.lost("The Bluetooth adapter went away")
				}
			}
		}
	case "PropertiesChanged":
		changed := dbusDict(m.arg(1))
		switch m.argString(0) {
		case ctlBleDeviceIface:
			s.mu.Lock()
			s.noteDeviceLocked(m.Path, changed)
			if paired, ok := dbusPropBool(changed, "Paired"); ok && paired && m.Path == s.confirmDev && s.confirm == nil {
				// A code shown for typing (agentDisplay) was typed: BlueZ
				// sends no Cancel after a pairing that worked.
				s.prompt = nil
			}
			s.updateLinkLocked()
			s.mu.Unlock()
		case ctlBleAdapterIface:
			s.mu.Lock()
			ours := m.Path == s.adapter
			s.mu.Unlock()
			if on, ok := dbusPropBool(changed, "Powered"); ours && ok && !on {
				s.lost("Bluetooth was switched off")
			}
		}
	case "NameOwnerChanged":
		if m.argString(0) == ctlBleBluez && m.argString(2) == "" {
			s.lost("The Bluetooth service stopped")
		}
	}
}

// ── Exported objects ─────────────────────────────────────────────────────

// exportObjects puts the agent, the advertisement and the GATT application
// on the bus.
func (s *ctlBleSession) exportObjects(conn *dbusConn) {
	conn.Export(ctlBleAgentPath, s.agentIface())

	adv := &dbusIface{Name: ctlBleAdvIface, Props: ctlBleAdvProps, Methods: map[string]func(*dbusCall){}}
	adv.Methods["Release"] = func(call *dbusCall) {
		s.mu.Lock()
		s.regAdv = false
		s.mu.Unlock()
		_ = call.Reply("")
	}
	conn.Export(ctlBleAdvPath, adv)

	conn.ExportManager(ctlBleAppPath)
	conn.Export(ctlBleHIDPath, ctlBleService(0x1812))
	conn.Export(ctlBleBatPath, ctlBleService(0x180F))
	conn.Export(ctlBleDisPath, ctlBleService(0x180A))

	info := []byte{0x11, 0x01, 0x00, 0x02} // bcdHID 1.11, no country, normally connectable
	conn.Export(ctlBleInfoPath, s.char(ctlBleHIDPath, 0x2A4A, ctlBleFlags("encrypt-read"),
		func() []byte { return info }, nil))
	conn.Export(ctlBleMapPath, s.char(ctlBleHIDPath, 0x2A4B, ctlBleFlags("encrypt-read"),
		func() []byte { return ctlBleReportMap }, nil))
	conn.Export(ctlBleProtoPath, s.char(ctlBleHIDPath, 0x2A4E, ctlBleFlags("encrypt-read", "write-without-response"),
		s.protoValue, s.protoWrite))
	conn.Export(ctlBleCtrlPath, s.char(ctlBleHIDPath, 0x2A4C, ctlBleFlags("write-without-response"),
		nil, func([]byte) {}))

	report := s.char(ctlBleHIDPath, 0x2A4D, s.reportFlags, s.reportValue, nil)
	report.Methods["StartNotify"] = func(call *dbusCall) { s.setNotify(call, true) }
	report.Methods["StopNotify"] = func(call *dbusCall) { s.setNotify(call, false) }
	conn.Export(ctlBleReportPath, report)
	conn.Export(ctlBleRefPath, s.desc(ctlBleReportPath, 0x2908, []byte{0x01, 0x01})) // report ID 1, input

	level := s.char(ctlBleBatPath, 0x2A19, ctlBleFlags("read", "notify"), s.levelValue, nil)
	level.Methods["StartNotify"] = func(call *dbusCall) { s.setBattNotify(call, true) }
	level.Methods["StopNotify"] = func(call *dbusCall) { s.setBattNotify(call, false) }
	conn.Export(ctlBleLevelPath, level)

	conn.Export(ctlBlePnpPath, s.char(ctlBleDisPath, 0x2A50, ctlBleFlags("read"),
		func() []byte { return ctlBlePnpID }, nil))
}

// ctlBleAdvProps are the advertisement's properties.
func ctlBleAdvProps() map[string]dbusVariant {
	return map[string]dbusVariant{
		"Type":         {Sig: "s", Value: "peripheral"},
		"ServiceUUIDs": {Sig: "as", Value: []string{"1812"}},
		"Appearance":   {Sig: "q", Value: uint16(ctlBleAppearance)},
		"LocalName":    {Sig: "s", Value: ctlBleName},
		"Discoverable": {Sig: "b", Value: true},
	}
}

// ctlBleFlags is a fixed flag list.
func ctlBleFlags(flags ...string) func() []string {
	return func() []string { return flags }
}

// reportFlags are the input report's: encrypted reads, and encrypted
// notifications where BlueZ knows the flag.
func (s *ctlBleSession) reportFlags() []string {
	s.mu.Lock()
	strict := s.strictNotify
	s.mu.Unlock()
	if strict {
		return []string{"encrypt-read", "encrypt-notify"}
	}
	return []string{"encrypt-read", "notify"}
}

func ctlBleService(uuid uint16) *dbusIface {
	props := map[string]dbusVariant{
		"UUID":    {Sig: "s", Value: ctlBleUUID(uuid)},
		"Primary": {Sig: "b", Value: true},
	}
	return &dbusIface{Name: ctlBleServiceIface, Props: func() map[string]dbusVariant { return props }}
}

// char is a GattCharacteristic1: read answers ReadValue (nil: none), write
// takes WriteValue (nil: none).
func (s *ctlBleSession) char(service dbusObjectPath, uuid uint16, flags func() []string, read func() []byte, write func([]byte)) *dbusIface {
	ifc := &dbusIface{Name: ctlBleCharIface, Methods: map[string]func(*dbusCall){}}
	ifc.Props = func() map[string]dbusVariant {
		return map[string]dbusVariant{
			"UUID":    {Sig: "s", Value: ctlBleUUID(uuid)},
			"Service": {Sig: "o", Value: service},
			"Flags":   {Sig: "as", Value: flags()},
		}
	}
	if read != nil {
		ifc.Methods["ReadValue"] = func(call *dbusCall) { s.serveRead(call, read()) }
	}
	if write != nil {
		ifc.Methods["WriteValue"] = func(call *dbusCall) { s.serveWrite(call, write) }
	}
	return ifc
}

// desc is a readable GattDescriptor1 with a fixed value.
func (s *ctlBleSession) desc(chrc dbusObjectPath, uuid uint16, value []byte) *dbusIface {
	ifc := &dbusIface{Name: ctlBleDescIface, Methods: map[string]func(*dbusCall){}}
	ifc.Props = func() map[string]dbusVariant {
		return map[string]dbusVariant{
			"UUID":           {Sig: "s", Value: ctlBleUUID(uuid)},
			"Characteristic": {Sig: "o", Value: chrc},
			"Flags":          {Sig: "as", Value: []string{"read"}},
		}
	}
	ifc.Methods["ReadValue"] = func(call *dbusCall) { s.serveRead(call, value) }
	return ifc
}

// ctlBleOptions reads ReadValue's and WriteValue's options: the offset of a
// long read and the device asking.
func ctlBleOptions(v any) (offset int, dev dbusObjectPath) {
	opts := dbusDict(v)
	if o, ok := opts["offset"].(dbusVariant); ok {
		if n, ok := o.Value.(uint16); ok {
			offset = int(n)
		}
	}
	if d, ok := opts["device"].(dbusVariant); ok {
		dev, _ = d.Value.(dbusObjectPath)
	}
	return offset, dev
}

func (s *ctlBleSession) serveRead(call *dbusCall, value []byte) {
	offset, dev := ctlBleOptions(call.Msg.arg(0))
	s.touch(dev)
	if offset > len(value) {
		_ = call.Fail("org.bluez.Error.InvalidOffset", "The offset is past the value's end")
		return
	}
	_ = call.Reply("ay", value[offset:])
}

func (s *ctlBleSession) serveWrite(call *dbusCall, write func([]byte)) {
	value, _ := call.Msg.arg(0).([]byte)
	_, dev := ctlBleOptions(call.Msg.arg(1))
	s.touch(dev)
	write(value)
	_ = call.Reply("")
}

func (s *ctlBleSession) protoValue() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []byte{s.protoMode}
}

// protoWrite takes the host's protocol mode (0 boot, 1 report). The pad has
// no boot protocol, so it is only remembered for reading back.
func (s *ctlBleSession) protoWrite(v []byte) {
	if len(v) == 1 && v[0] <= 1 {
		s.mu.Lock()
		s.protoMode = v[0]
		s.mu.Unlock()
	}
}

func (s *ctlBleSession) reportValue() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.report
	return r[:]
}

func (s *ctlBleSession) levelValue() []byte {
	level := ctlBleBattery()
	s.mu.Lock()
	s.battLevel = level
	s.mu.Unlock()
	return []byte{level}
}

// setNotify is StartNotify/StopNotify on the input report: a host
// subscribed, so it is the host (the device that touched the pad last, else
// the one that connected last).
func (s *ctlBleSession) setNotify(call *dbusCall, on bool) {
	s.mu.Lock()
	s.notifying = on
	if on {
		if d := s.devices[s.touched]; d != nil && d.connected {
			s.peer = s.touched
		} else if p := s.devices[s.peer]; p == nil || !p.connected {
			s.peer = s.lastConnectedLocked(false)
		}
		s.resend = true
	}
	s.updateLinkLocked()
	s.mu.Unlock()
	s.wake()
	_ = call.Reply("")
}

func (s *ctlBleSession) setBattNotify(call *dbusCall, on bool) {
	s.mu.Lock()
	s.battNotify = on
	s.mu.Unlock()
	_ = call.Reply("")
}

// agentIface is the pairing agent (DisplayYesNo).
func (s *ctlBleSession) agentIface() *dbusIface {
	ifc := &dbusIface{Name: ctlBleAgentIface, Methods: map[string]func(*dbusCall){}}
	reject := func(call *dbusCall) { _ = call.Fail(ctlBleRejected, "The JukaHub pad does not pair that way") }
	// AuthorizeService is a profile connection from a paired but untrusted
	// device. The pad needs none, and as the default agent it would answer
	// for every device: only the pad's own hosts get one, while it runs.
	authorize := func(call *dbusCall) {
		dev, _ := call.Msg.arg(0).(dbusObjectPath)
		s.mu.Lock()
		d := s.devices[dev]
		host := false
		if d != nil && d.paired && d.addr != "" {
			_, host = s.hosts[d.addr]
		}
		s.mu.Unlock()
		if s.ctx.Err() != nil || !host {
			_ = call.Fail(ctlBleRejected, "Not a host of the JukaHub pad")
			return
		}
		_ = call.Reply("")
	}
	ifc.Methods["Release"] = func(call *dbusCall) {
		s.mu.Lock()
		s.regAgent = false
		s.mu.Unlock()
		_ = call.Reply("")
	}
	ifc.Methods["RequestPinCode"] = reject
	ifc.Methods["RequestPasskey"] = reject
	// A pairing without a code ("Just Works") is asked on the handheld too:
	// accepting it unseen would let any device in range bond while the pad
	// is pairable.
	ifc.Methods["RequestAuthorization"] = s.agentConfirm
	ifc.Methods["AuthorizeService"] = authorize
	ifc.Methods["RequestConfirmation"] = s.agentConfirm
	ifc.Methods["DisplayPasskey"] = s.agentDisplay
	ifc.Methods["DisplayPinCode"] = s.agentDisplay
	ifc.Methods["Cancel"] = func(call *dbusCall) {
		s.mu.Lock()
		s.prompt, s.confirm = nil, nil
		s.mu.Unlock()
		_ = call.Reply("")
	}
	return ifc
}

// agentConfirm is RequestConfirmation (o, u) and RequestAuthorization (o):
// the code, if there is one, goes on the handheld's screen and the answer
// waits for A or B (answer).
func (s *ctlBleSession) agentConfirm(call *dbusCall) {
	if s.ctx.Err() != nil {
		_ = call.Fail(ctlBleRejected, "Controller Mode is not running")
		return
	}
	dev, _ := call.Msg.arg(0).(dbusObjectPath)
	passkey, withCode := call.Msg.arg(1).(uint32)
	s.mu.Lock()
	old := s.confirm
	name := s.deviceNameLocked(dev)
	s.confirm, s.confirmDev = call, dev
	s.prompt = &ctlPrompt{
		Title:     "Pair with " + name + "?",
		Body:      "No code to compare: pair only if you chose " + ctlBleName + " on " + name + ".",
		Yes:       "Pair",
		No:        "Cancel",
		YesAction: ctlBleActYes,
		NoAction:  ctlBleActNo,
	}
	if withCode {
		s.prompt.Code = ctlBleCode(passkey)
		s.prompt.Body = "Check that " + name + " shows the same number, then pair."
	}
	s.promptEnd = time.Now().Add(ctlBlePromptLife)
	s.mu.Unlock()
	if old != nil && old != call {
		_ = old.Fail(ctlBleRejected, "A newer pairing request replaced it")
	}
}

// agentDisplay is DisplayPasskey/DisplayPinCode: a code to type on the
// other side, shown until it pairs or B cancels.
func (s *ctlBleSession) agentDisplay(call *dbusCall) {
	dev, _ := call.Msg.arg(0).(dbusObjectPath)
	var code string
	switch v := call.Msg.arg(1).(type) {
	case uint32:
		code = ctlBleCode(v)
	case string:
		code = v
	}
	s.mu.Lock()
	name := s.deviceNameLocked(dev)
	s.confirmDev = dev
	s.prompt = &ctlPrompt{
		Title:     "Pairing code for " + name,
		Code:      code,
		Body:      "Type this code on " + name + ".",
		Yes:       "OK",
		No:        "Cancel",
		YesAction: ctlBleActHide,
		NoAction:  ctlBleActCancelPair,
	}
	s.promptEnd = time.Now().Add(ctlBlePromptLife)
	s.mu.Unlock()
	_ = call.Reply("")
}
