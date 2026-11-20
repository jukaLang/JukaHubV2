package main

// ctlmode_dsu.go — Controller Mode's DSU link: the handheld as a cemuhook
// ("DSU") server on UDP 26760, the protocol Dolphin, Cemu and the Switch
// emulators (yuzu's forks) read controllers with. Nothing is installed on the
// PC: the emulator is pointed at "<handheld IP>:26760".
//
// The protocol (v1993.github.io/cemuhook-protocol): every packet has a
// 16-byte header — "DSUC" from clients, "DSUS" from the server, protocol
// 1001 (u16), the length after the header (u16), a CRC-32 of the whole
// packet computed with its own field zeroed (u32) and the sender's id (u32) —
// then the message type (u32) and its body, all little-endian. This server
// answers 0x100000 (protocol version), 0x100001 (controller info: slot 0
// connected, the others not) and 0x100002 (subscribe to pad data). A client
// subscribes to every slot, a slot or a MAC address and must repeat the
// request: it is dropped 5 s after its last one. Every live subscriber gets
// the 100-byte data packet on each change and at least every 100 ms.
//
// The data packet carries buttons in the DS4 layout (positional, so Xbox A is
// Cross), sticks as 0..255 with +Y up, analogue pressures, no touch, and a
// motion timestamp with zero accelerometer and gyro: the handheld has no
// motion sensors, so emulators that read only motion over DSU (Citra, Azahar,
// Ryujinx) get nothing useful from it.
//
// DSU has no pairing and no password: anyone on the network can read the
// pad, so the link runs only while the user keeps it started on the page.

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	dsuPort       = 26760
	dsuProtocol   = 1001
	dsuMsgVersion = 0x100000
	dsuMsgInfo    = 0x100001
	dsuMsgData    = 0x100002
	dsuDataLen    = 100
	dsuSubTTL     = 5 * time.Second
	dsuRepeat     = 100 * time.Millisecond
	dsuGap        = 4 * time.Millisecond
	dsuMaxSubs    = 8
)

// dsuMAC is the controller's MAC address in the protocol (a locally
// administered one: "JUKA" and slot 1).
var dsuMAC = [6]byte{0x02, 0x4A, 0x55, 0x4B, 0x41, 0x01}

type ctlDSUSub struct {
	addr   *net.UDPAddr
	since  time.Time
	until  time.Time
	number uint32
}

type ctlDSU struct {
	mu       sync.Mutex
	running  bool
	gen      int
	conn     *net.UDPConn
	stop     chan struct{}
	wake     chan struct{}
	serverID uint32
	battery  byte
	subs     map[string]*ctlDSUSub
	started  time.Time
	ip       string

	pad      ctlPadState
	padDirty bool
	lastSent time.Time

	availAt time.Time
	availOK bool
}

func init() {
	ctlModeRegisterTransport(20, &ctlDSU{})
}

func (d *ctlDSU) ID() string    { return "dsu" }
func (d *ctlDSU) Title() string { return "DSU for PC emulators" }
func (d *ctlDSU) Subtitle() string {
	return "Dolphin, Cemu and Switch emulators read the handheld directly — no password, so only while this is open"
}

func (d *ctlDSU) Available() (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if d.availAt.IsZero() || now.Sub(d.availAt) > 2*time.Second {
		d.availAt = now
		d.availOK = len(ctlLANv4()) > 0
	}
	if !d.availOK {
		return false, "Connect to Wi-Fi first"
	}
	return true, ""
}

func (d *ctlDSU) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: dsuPort})
	if err != nil {
		return ctlError("UDP port 26760 is taken — is another DSU server running?")
	}
	d.gen++
	gen := d.gen
	d.running = true
	d.conn = conn
	d.stop = make(chan struct{})
	d.wake = make(chan struct{}, 1)
	d.serverID = binary.LittleEndian.Uint32(ctlWifiRandom(4))
	d.subs = map[string]*ctlDSUSub{}
	d.started = time.Now()
	d.pad, d.padDirty = ctlPadState{}, false
	d.battery = 0
	d.ip = ""
	go d.reader(gen, conn)
	go d.worker(gen, conn, d.stop, d.wake)
	return nil
}

// Stop sends every subscriber a released pad, then closes the socket.
func (d *ctlDSU) Stop() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.running = false
	d.gen++
	conn := d.conn
	out := d.packetsLocked(ctlPadState{}, time.Now())
	close(d.stop)
	d.conn = nil
	d.subs = nil
	d.mu.Unlock()
	ctlWifiFlush(conn, out)
	_ = conn.Close()
}

func (d *ctlDSU) Send(st ctlPadState) {
	d.mu.Lock()
	d.pad = st
	d.padDirty = true
	wake := d.wake
	live := d.running && len(d.subs) > 0
	d.mu.Unlock()
	if live {
		ctlWifiSignal(wake)
	}
}

// firstSubLocked is the subscriber that came first among the live ones.
func (d *ctlDSU) firstSubLocked(now time.Time) *ctlDSUSub {
	var first *ctlDSUSub
	for _, s := range d.subs {
		if now.Before(s.until) && (first == nil || s.since.Before(first.since)) {
			first = s
		}
	}
	return first
}

func (d *ctlDSU) Status() ctlLinkStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return ctlLinkStatus{State: ctlLinkIdle}
	}
	if s := d.firstSubLocked(time.Now()); s != nil {
		ip := s.addr.IP.String()
		return ctlLinkStatus{State: ctlLinkConnected, Peer: "PC emulator at " + ip, PeerID: "dsu:" + ip,
			Detail: "An emulator at " + ip + " is reading the pad"}
	}
	detail := "Starting…"
	if d.ip != "" {
		detail = "Waiting for an emulator — use " + d.ip + ":" + strconv.Itoa(dsuPort)
	}
	return ctlLinkStatus{State: ctlLinkWaiting, Detail: detail}
}

func (d *ctlDSU) Rows() []ctlRow {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return nil
	}
	addr, ip := "…", "the handheld's IP"
	if d.ip != "" {
		addr, ip = d.ip+":"+strconv.Itoa(dsuPort), d.ip
	}
	return []ctlRow{
		{Label: "Server address", Detail: addr},
		{Label: "Dolphin", Detail: "Controllers → Alternate Input Sources → DSU Client: add " + addr + ", then pick DSUClient as the device"},
		{Label: "Cemu", Detail: "Input settings → API DSUController, IP " + ip + ", port 26760"},
		{Label: "Eden, Citron and other yuzu forks", Detail: "Controls → Advanced → enable UDP controllers, add " + addr},
		{Label: "Citra, Azahar, Ryujinx", Detail: "They read only motion over DSU, which this handheld lacks: use Wi-Fi to a PC"},
		{Label: "No password", Detail: "Anyone on this network can read the pad while this runs — stop it when you are done"},
	}
}

func (d *ctlDSU) Prompt() *ctlPrompt { return nil }

func (d *ctlDSU) Do(string) {}

// ── Goroutines ───────────────────────────────────────────────────────────

func (d *ctlDSU) live(gen int) bool {
	return d.running && d.gen == gen
}

func (d *ctlDSU) reader(gen int, conn *net.UDPConn) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			d.mu.Lock()
			alive := d.live(gen)
			d.mu.Unlock()
			if !alive {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if from != nil {
			d.handle(gen, conn, buf[:n], from)
		}
	}
}

func (d *ctlDSU) worker(gen int, conn *net.UDPConn, stop, wake chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var infoAt time.Time
	for {
		select {
		case <-stop:
			return
		case <-wake:
		case <-timer.C:
		}
		now := time.Now()
		// The address and the battery change rarely: read them every 10 s
		// (the IP on the page is the screenshot-safe one).
		if infoAt.IsZero() || now.Sub(infoAt) >= 10*time.Second {
			infoAt = now
			ip := localIPv4()
			bat := dsuBattery()
			d.mu.Lock()
			d.ip, d.battery = ip, bat
			d.mu.Unlock()
		}
		d.mu.Lock()
		if !d.live(gen) {
			d.mu.Unlock()
			return
		}
		for k, s := range d.subs {
			if !now.Before(s.until) {
				delete(d.subs, k)
			}
		}
		wait := 10 * time.Second
		var out []ctlWifiOut
		if len(d.subs) > 0 {
			due := now.Sub(d.lastSent) >= dsuRepeat || (d.padDirty && now.Sub(d.lastSent) >= dsuGap)
			if due {
				out = d.packetsLocked(d.pad, now)
				d.padDirty = false
				d.lastSent = now
			}
			wait = d.lastSent.Add(dsuRepeat).Sub(now)
			if d.padDirty {
				wait = d.lastSent.Add(dsuGap).Sub(now)
			}
			// Wake for the next expiry too, so the page drops to Waiting on time.
			for _, s := range d.subs {
				if w := s.until.Sub(now); w < wait {
					wait = w
				}
			}
		}
		d.mu.Unlock()
		ctlWifiFlush(conn, out)
		if wait < 2*time.Millisecond {
			wait = 2 * time.Millisecond
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
}

// packetsLocked builds one data packet per live subscriber.
func (d *ctlDSU) packetsLocked(st ctlPadState, now time.Time) []ctlWifiOut {
	ts := uint64(now.Sub(d.started).Microseconds())
	var out []ctlWifiOut
	for _, s := range d.subs {
		if !now.Before(s.until) {
			continue
		}
		s.number++
		out = append(out, ctlWifiOut{dsuDataPacket(d.serverID, dsuMAC, d.battery, s.number, st, ts), s.addr})
	}
	return out
}

// handle answers one client request.
func (d *ctlDSU) handle(gen int, conn *net.UDPConn, pkt []byte, from *net.UDPAddr) {
	msg, body, ok := dsuParseRequest(pkt)
	if !ok {
		return
	}
	d.mu.Lock()
	if !d.live(gen) {
		d.mu.Unlock()
		return
	}
	id, battery := d.serverID, d.battery
	var out [][]byte
	wakeWorker := false
	switch msg {
	case dsuMsgVersion:
		out = append(out, dsuFinish(id, dsuMsgVersion, binary.LittleEndian.AppendUint16(nil, dsuProtocol)))
	case dsuMsgInfo:
		if len(body) < 4 {
			break
		}
		count := int(int32(binary.LittleEndian.Uint32(body)))
		for i := 0; i < count && i < 4 && 4+i < len(body); i++ {
			slot := body[4+i]
			if slot > 3 {
				continue
			}
			p := dsuSlotHeader(nil, slot, slot == 0, battery)
			out = append(out, dsuFinish(id, dsuMsgInfo, append(p, 0)))
		}
	case dsuMsgData:
		if len(body) < 8 {
			break
		}
		flags, slot := body[0], body[1]
		want := flags == 0 || (flags&1 != 0 && slot == 0) || (flags&2 != 0 && [6]byte(body[2:8]) == dsuMAC)
		if !want {
			break
		}
		now := time.Now()
		key := from.String()
		s := d.subs[key]
		if s == nil {
			if len(d.subs) >= dsuMaxSubs {
				break
			}
			s = &ctlDSUSub{addr: from, since: now}
			d.subs[key] = s
			logInfo("ctlmode", "dsu: %s subscribed", from.IP)
			wakeWorker = true
		}
		s.until = now.Add(dsuSubTTL)
	}
	wake := d.wake
	d.mu.Unlock()
	for _, p := range out {
		_, _ = conn.WriteToUDP(p, from)
	}
	if wakeWorker {
		ctlWifiSignal(wake)
	}
}

// dsuBattery is the battery in the protocol's terms (0 when unknown).
func dsuBattery() byte {
	dir := batterySysfsDir()
	pct := batteryPercentIn(dir)
	if pct < 0 {
		return 0
	}
	pct = batteryCalCorrect(pct)
	if batteryChargingIn(powerSupplyRoot) {
		if pct >= 100 {
			return 0xEF
		}
		return 0xEE
	}
	switch {
	case pct >= 90:
		return 5
	case pct >= 60:
		return 4
	case pct >= 30:
		return 3
	case pct >= 10:
		return 2
	}
	return 1
}

// ── Packets (pure; ctlmode_wifi_test.go checks them) ─────────────────────

// dsuParseRequest checks a client packet's header, length and CRC and
// returns its message type and body.
func dsuParseRequest(pkt []byte) (uint32, []byte, bool) {
	if len(pkt) < 20 || string(pkt[:4]) != "DSUC" {
		return 0, nil, false
	}
	le := binary.LittleEndian
	if le.Uint16(pkt[4:]) > dsuProtocol {
		return 0, nil, false
	}
	n := 16 + int(le.Uint16(pkt[6:]))
	if n < 20 || n > len(pkt) {
		return 0, nil, false
	}
	pkt = pkt[:n]
	want := le.Uint32(pkt[8:])
	c := crc32.NewIEEE()
	c.Write(pkt[:8])
	c.Write([]byte{0, 0, 0, 0})
	c.Write(pkt[12:])
	if c.Sum32() != want {
		return 0, nil, false
	}
	return le.Uint32(pkt[16:]), pkt[20:], true
}

// dsuFinish wraps a message: header, type, body, CRC.
func dsuFinish(serverID, msg uint32, body []byte) []byte {
	le := binary.LittleEndian
	p := make([]byte, 0, 20+len(body))
	p = append(p, 'D', 'S', 'U', 'S')
	p = le.AppendUint16(p, dsuProtocol)
	p = le.AppendUint16(p, uint16(4+len(body)))
	p = le.AppendUint32(p, 0)
	p = le.AppendUint32(p, serverID)
	p = le.AppendUint32(p, msg)
	p = append(p, body...)
	le.PutUint32(p[8:], crc32.ChecksumIEEE(p))
	return p
}

// dsuSlotHeader appends the 11 bytes every controller message starts with:
// slot, state (2 connected), model (1: no gyro), connection (0: not
// applicable), MAC, battery.
func dsuSlotHeader(p []byte, slot byte, connected bool, battery byte) []byte {
	if !connected {
		return append(p, slot, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	p = append(p, slot, 2, 1, 0)
	p = append(p, dsuMAC[:]...)
	return append(p, battery)
}

// dsuAxis maps -32768..32767 (+ right / up) onto 0..255 with 128 at rest.
func dsuAxis(v int32) byte {
	if v < -32767 {
		v = -32767
	}
	if v >= 0 {
		return byte(128 + v*127/32767)
	}
	return byte(128 - (-v)*128/32767)
}

// dsuDataPacket is the 100-byte pad data message for slot 0. ts is the
// motion timestamp in microseconds.
func dsuDataPacket(serverID uint32, mac [6]byte, battery byte, number uint32, st ctlPadState, ts uint64) []byte {
	le := binary.LittleEndian
	b := st.Buttons
	bit := func(mask uint32, v byte) byte {
		if b&mask != 0 {
			return v
		}
		return 0
	}
	pressure := func(mask uint32) byte { return bit(mask, 255) }
	p := make([]byte, 0, dsuDataLen-16)
	p = append(p, 0, 2, 1, 0)
	p = append(p, mac[:]...)
	p = append(p, battery, 1)
	p = le.AppendUint32(p, number)
	// The face buttons are named the Nintendo way and are positional: the
	// spec's Y, B, A, X are left, bottom, right, top (Square, Cross, Circle,
	// Triangle in Dolphin's and DS4Windows' terms) — Xbox X, A, B, Y here.
	p = append(p,
		bit(ctlBtnLeft, 0x80)|bit(ctlBtnDown, 0x40)|bit(ctlBtnRight, 0x20)|bit(ctlBtnUp, 0x10)|
			bit(ctlBtnStart, 0x08)|bit(ctlBtnRS, 0x04)|bit(ctlBtnLS, 0x02)|bit(ctlBtnBack, 0x01),
		bit(ctlBtnX, 0x80)|bit(ctlBtnA, 0x40)|bit(ctlBtnB, 0x20)|bit(ctlBtnY, 0x10)|
			bit(ctlBtnRB, 0x08)|bit(ctlBtnLB, 0x04)|bit(ctlBtnRT, 0x02)|bit(ctlBtnLT, 0x01),
		bit(ctlBtnGuide, 1), 0)
	// DSU's sticks have +Y up; SDL's have +Y down.
	p = append(p, dsuAxis(int32(st.LX)), dsuAxis(-int32(st.LY)), dsuAxis(int32(st.RX)), dsuAxis(-int32(st.RY)))
	p = append(p, pressure(ctlBtnLeft), pressure(ctlBtnDown), pressure(ctlBtnRight), pressure(ctlBtnUp),
		pressure(ctlBtnX), pressure(ctlBtnA), pressure(ctlBtnB), pressure(ctlBtnY),
		pressure(ctlBtnRB), pressure(ctlBtnLB), st.RT, st.LT)
	p = append(p, make([]byte, 12)...) // two touches, inactive
	p = le.AppendUint64(p, ts)
	p = append(p, make([]byte, 24)...) // accelerometer and gyro: none
	return dsuFinish(serverID, dsuMsgData, p)
}
