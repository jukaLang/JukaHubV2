package main

// wg_device.go — WireGuard's transport for the VPN: sessions, the replay
// window, the timers of whitepaper §6, and the loops that move packets
// between the tunnel interface and the UDP socket. The handshake itself is
// wg_noise.go; the Linux daemon (vpnd_linux.go) supplies the TUN device and
// the socket.
//
// The timer rules follow the kernel and wireguard-go: a session is rekeyed by
// its initiator after 2 minutes, unusable after 3; an unanswered handshake is
// retried every 5 s for 90 s; a side that received data and has nothing to
// send answers with a keepalive after 10 s; data sent with no reply for 15 s
// starts a new handshake. All time is the monotonic clock, so the handheld's
// wall clock jumping when it first reaches a time server changes nothing.

import (
	"crypto/cipher"
	"crypto/ecdh"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// wgMaxQueued is how many packets wait for a handshake (wireguard-go's
	// limit); the oldest goes first when more arrive.
	wgMaxQueued = 128
	// wgBufSize holds the largest UDP datagram or tunnel packet.
	wgBufSize = 1 << 16
	// wgTick is how often the timers are looked at.
	wgTick = 200 * time.Millisecond
)

// wgTUN is the tunnel side: one whole IP packet per Read and per Write.
type wgTUN interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
}

// wgConn is the UDP side (*net.UDPConn).
type wgConn interface {
	ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
}

// wgDeviceConfig is what a device needs besides its two ends.
type wgDeviceConfig struct {
	Identity *wgIdentity
	Endpoint netip.AddrPort
	// Allowed are the inner addresses the peer may send from and that we send
	// to it: the configuration's AllowedIPs, or everything when the VPN
	// carries all traffic.
	Allowed []netip.Prefix
	// Keepalive is the persistent keepalive (0: off).
	Keepalive time.Duration
	MTU       int
	// LocalPort is our UDP port: a packet from it to the server that was
	// routed back into the tunnel is our own traffic looping, never sent on.
	LocalPort uint16
	// Timestamp makes handshake timestamps (wgTAI64N of the clock, kept
	// increasing across restarts by the daemon).
	Timestamp func() [12]byte
	Logf      func(format string, args ...any)
}

// wgKeypair is one session's keys and counters.
type wgKeypair struct {
	send, recv  cipher.AEAD
	sendNonce   uint64
	replay      wgReplay
	created     time.Time
	initiator   bool
	localIndex  uint32
	remoteIndex uint32
}

func newWGKeypair(send, recv [32]byte, local, remote uint32, initiator bool, now time.Time) (*wgKeypair, error) {
	s, err := chacha20poly1305.New(send[:])
	if err != nil {
		return nil, err
	}
	r, err := chacha20poly1305.New(recv[:])
	if err != nil {
		return nil, err
	}
	return &wgKeypair{send: s, recv: r, created: now, initiator: initiator, localIndex: local, remoteIndex: remote}, nil
}

// wgReplay is the sliding window of received counters (RFC 6479): 2^13 - 64
// counters behind the newest are remembered, older ones are refused.
type wgReplay struct {
	last uint64
	ring [wgReplayBlocks]uint64
}

const (
	wgReplayBlocks = 128
	wgReplayWindow = (wgReplayBlocks - 1) * 64
)

// accept reports whether counter is new and inside the window, and records
// it. Only a packet that has already authenticated may be shown to it.
func (r *wgReplay) accept(counter uint64) bool {
	if counter >= wgRejectAfterMessages {
		return false
	}
	block := counter / 64
	if counter > r.last {
		cur := r.last / 64
		diff := block - cur
		if diff > wgReplayBlocks {
			diff = wgReplayBlocks
		}
		for i := cur + 1; i <= cur+diff; i++ {
			r.ring[i%wgReplayBlocks] = 0
		}
		r.last = counter
	} else if r.last-counter > wgReplayWindow {
		return false
	}
	bit := uint64(1) << (counter % 64)
	slot := &r.ring[block%wgReplayBlocks]
	if *slot&bit != 0 {
		return false
	}
	*slot |= bit
	return true
}

// wgStats is the device's state for the status line.
type wgStats struct {
	LastHandshake   time.Time // zero: no handshake yet
	UnansweredSince time.Time // first initiation still waiting for an answer
	GaveUp          bool      // the last series of attempts went unanswered
	Endpoint        netip.AddrPort
	Rx, Tx          uint64
}

// wgDevice is one tunnel to one peer.
type wgDevice struct {
	cfg  wgDeviceConfig
	tun  wgTUN
	conn wgConn

	mu       sync.Mutex
	noise    wgNoise
	current  *wgKeypair
	previous *wgKeypair
	next     *wgKeypair // a session we answered, until the peer uses it
	endpoint netip.AddrPort
	queue    [][]byte

	// Timers: a zero time is a timer that is not running.
	retransmitAt   time.Time
	attemptsSince  time.Time // start of the current series of attempts
	lastInitiation time.Time
	keepaliveAt    time.Time
	newHandshakeAt time.Time
	persistentAt   time.Time
	zeroKeysAt     time.Time

	lastHandshake   time.Time
	unansweredSince time.Time
	gaveUp          bool
	lastWriteErr    time.Time

	rx, tx atomic.Uint64
	closed atomic.Bool
	done   chan struct{}

	now  func() time.Time
	rand io.Reader
}

func newWGDevice(cfg wgDeviceConfig, tun wgTUN, conn wgConn, random io.Reader) *wgDevice {
	return &wgDevice{
		cfg:      cfg,
		tun:      tun,
		conn:     conn,
		noise:    wgNoise{id: cfg.Identity},
		endpoint: cfg.Endpoint,
		done:     make(chan struct{}),
		now:      time.Now,
		rand:     random,
	}
}

func (d *wgDevice) logf(format string, args ...any) {
	if d.cfg.Logf != nil {
		d.cfg.Logf(format, args...)
	}
}

// Start runs the loops and starts a handshake at once, so a wrong key or an
// unreachable server shows up when the VPN is switched on, not at the first
// page load.
func (d *wgDevice) Start() {
	go d.tunLoop()
	go d.udpLoop()
	go d.timerLoop()
	d.sendPacket(nil, nil)
}

// Kick sends a keepalive (a handshake when there is no session): after a
// network change it refreshes the NAT mapping and proves the path again.
func (d *wgDevice) Kick() { d.sendPacket(nil, nil) }

// Close stops the loops; the owner then closes the TUN and the socket, which
// ends the reads they are blocked in.
func (d *wgDevice) Close() {
	if d.closed.Swap(true) {
		return
	}
	close(d.done)
}

// Stats is a snapshot for the status line.
func (d *wgDevice) Stats() wgStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return wgStats{
		LastHandshake:   d.lastHandshake,
		UnansweredSince: d.unansweredSince,
		GaveUp:          d.gaveUp,
		Endpoint:        d.endpoint,
		Rx:              d.rx.Load(),
		Tx:              d.tx.Load(),
	}
}

// ── Randomness ───────────────────────────────────────────────────────────

func (d *wgDevice) newEphemeral() (*ecdh.PrivateKey, error) {
	var b [32]byte
	if _, err := io.ReadFull(d.rand, b[:]); err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPrivateKey(b[:])
}

// freshIndexLocked draws a sender index no live session uses.
func (d *wgDevice) freshIndexLocked() uint32 {
	var idx uint32
	for try := 0; try < 8; try++ {
		var b [4]byte
		if _, err := io.ReadFull(d.rand, b[:]); err != nil {
			idx = rand.Uint32()
		} else {
			idx = binary.LittleEndian.Uint32(b[:])
		}
		if d.keypairLocked(idx) == nil {
			break
		}
	}
	return idx
}

func (d *wgDevice) keypairLocked(idx uint32) *wgKeypair {
	for _, kp := range [...]*wgKeypair{d.current, d.previous, d.next} {
		if kp != nil && kp.localIndex == idx {
			return kp
		}
	}
	return nil
}

// wgJitter spreads retries the way the protocol asks (up to a third of a second).
func wgJitter() time.Duration { return rand.N(334 * time.Millisecond) }

// ── Handshakes ───────────────────────────────────────────────────────────

// initiateLocked builds a handshake initiation unless one went out less than
// wgRekeyTimeout ago; the caller sends it. retry marks the timer's resend,
// which continues a series of attempts instead of starting a new one.
func (d *wgDevice) initiateLocked(now time.Time, retry bool) []byte {
	if !retry {
		d.attemptsSince = now
	}
	if !d.lastInitiation.IsZero() && now.Sub(d.lastInitiation) < wgRekeyTimeout {
		return nil
	}
	eph, err := d.newEphemeral()
	if err != nil {
		d.logf("no randomness for a handshake: %v", err)
		return nil
	}
	msg, err := d.noise.initiation(eph, d.freshIndexLocked(), d.cfg.Timestamp(), now)
	if err != nil {
		d.logf("handshake: %v", err)
		return nil
	}
	if d.attemptsSince.IsZero() {
		d.attemptsSince = now
	}
	if d.unansweredSince.IsZero() {
		d.unansweredSince = now
	}
	d.lastInitiation = now
	d.retransmitAt = now.Add(wgRekeyTimeout + wgJitter())
	d.gaveUp = false
	d.anySentLocked(now)
	return msg
}

// handshakeDoneLocked notes a session both sides now hold.
func (d *wgDevice) handshakeDoneLocked(now time.Time) {
	if d.lastHandshake.IsZero() || d.gaveUp || now.Sub(d.lastHandshake) > wgRejectAfterTime {
		d.logf("connected to %s", d.endpoint)
	}
	d.retransmitAt = time.Time{}
	d.attemptsSince = time.Time{}
	d.unansweredSince = time.Time{}
	d.gaveUp = false
	d.lastHandshake = now
	d.zeroKeysAt = now.Add(3 * wgRejectAfterTime)
}

func (d *wgDevice) receiveInitiation(msg []byte, src netip.AddrPort) {
	now := d.now()
	d.mu.Lock()
	if !d.noise.mac1OK(msg) || d.noise.takeInitiation(msg, now) != nil {
		d.mu.Unlock()
		return
	}
	eph, err := d.newEphemeral()
	if err != nil {
		d.noise.zero()
		d.mu.Unlock()
		return
	}
	index := d.freshIndexLocked()
	resp, send, recv, remote, err := d.noise.response(eph, index, now)
	if err != nil {
		d.mu.Unlock()
		return
	}
	kp, err := newWGKeypair(send, recv, index, remote, false, now)
	if err != nil {
		d.mu.Unlock()
		return
	}
	// The answered session waits in next until the peer sends with it; the
	// one before the current one is no longer needed. A response counts as a
	// handshake sent (the kernel's rule): a packet of ours meanwhile waits for
	// the session instead of crossing it with an initiation of our own.
	d.next = kp
	d.previous = nil
	d.endpoint = src
	d.lastInitiation = now
	d.zeroKeysAt = now.Add(3 * wgRejectAfterTime)
	d.anyReceivedLocked(now)
	d.anySentLocked(now)
	d.mu.Unlock()
	d.write(resp, src)
}

func (d *wgDevice) receiveResponse(msg []byte, src netip.AddrPort) {
	now := d.now()
	d.mu.Lock()
	if !d.noise.mac1OK(msg) {
		d.mu.Unlock()
		return
	}
	send, recv, local, remote, err := d.noise.takeResponse(msg)
	if err != nil {
		d.mu.Unlock()
		return
	}
	kp, err := newWGKeypair(send, recv, local, remote, true, now)
	if err != nil {
		d.mu.Unlock()
		return
	}
	if d.next != nil {
		d.previous = d.next
		d.next = nil
	} else {
		d.previous = d.current
	}
	d.current = kp
	d.endpoint = src
	d.handshakeDoneLocked(now)
	d.anyReceivedLocked(now)
	queued := d.queue
	d.queue = nil
	d.mu.Unlock()
	// The responder only starts sending once it hears from us under the new
	// keys, so something always goes out now: what waited, or a keepalive.
	if len(queued) == 0 {
		d.sendPacket(nil, nil)
	}
	for _, p := range queued {
		d.sendPacket(p, nil)
	}
}

func (d *wgDevice) receiveCookie(msg []byte) {
	now := d.now()
	d.mu.Lock()
	err := d.noise.takeCookie(msg, now)
	d.mu.Unlock()
	if err == nil {
		d.logf("the server is busy and asked for a cookie; the next handshake carries it")
	}
}

// ── Data ─────────────────────────────────────────────────────────────────

// wgPadding is how much zero padding a packet of n bytes gets: up to a
// multiple of 16, but never past the MTU (the kernel's rule).
func wgPadding(n, mtu int) int {
	last := n
	if mtu > 0 && last > mtu {
		last %= mtu
	}
	padded := (last + 15) &^ 15
	if mtu > 0 && padded > mtu {
		padded = mtu
	}
	return padded - last
}

// sealData frames pkt as a transport message for kp with the given counter,
// encrypting it in place inside buf (grown when too small).
func sealData(kp *wgKeypair, counter uint64, pkt []byte, mtu int, buf []byte) []byte {
	pad := wgPadding(len(pkt), mtu)
	body := len(pkt) + pad
	if cap(buf) < wgDataHeaderSize+body+wgTagSize {
		buf = make([]byte, 0, wgDataHeaderSize+body+wgTagSize)
	}
	buf = buf[:wgDataHeaderSize+body]
	binary.LittleEndian.PutUint32(buf[0:4], wgMsgData)
	binary.LittleEndian.PutUint32(buf[4:8], kp.remoteIndex)
	binary.LittleEndian.PutUint64(buf[8:16], counter)
	copy(buf[wgDataHeaderSize:], pkt)
	clear(buf[wgDataHeaderSize+len(pkt):])
	nonce := wgNonce(counter)
	return kp.send.Seal(buf[:wgDataHeaderSize], nonce[:], buf[wgDataHeaderSize:], nil)
}

// sendPacket sends one packet from the tunnel to the peer — or a keepalive
// when pkt is empty — queueing it and starting a handshake when there is no
// usable session. buf is scratch space for the encrypted message (nil
// allocates).
func (d *wgDevice) sendPacket(pkt, buf []byte) {
	if d.closed.Load() {
		return
	}
	now := d.now()
	d.mu.Lock()
	kp := d.current
	if kp == nil || now.Sub(kp.created) >= wgRejectAfterTime || kp.sendNonce >= wgRejectAfterMessages {
		d.queueLocked(pkt)
		init := d.initiateLocked(now, false)
		ep := d.endpoint
		d.mu.Unlock()
		d.write(init, ep)
		return
	}
	counter := kp.sendNonce
	kp.sendNonce++
	if len(pkt) > 0 {
		d.dataSentLocked(now)
	}
	d.anySentLocked(now)
	var init []byte
	if counter >= wgRekeyAfterMessages || (kp.initiator && now.Sub(kp.created) >= wgRekeyAfterTime) {
		init = d.initiateLocked(now, false)
	}
	ep := d.endpoint
	d.mu.Unlock()
	d.write(sealData(kp, counter, pkt, d.cfg.MTU, buf), ep)
	d.write(init, ep)
}

// queueLocked keeps pkt for when the session is up. A keepalive is only
// queued when nothing else is: any packet confirms the session.
func (d *wgDevice) queueLocked(pkt []byte) {
	if len(pkt) == 0 && len(d.queue) > 0 {
		return
	}
	if len(d.queue) >= wgMaxQueued {
		copy(d.queue, d.queue[1:])
		d.queue = d.queue[:len(d.queue)-1]
	}
	d.queue = append(d.queue, append([]byte{}, pkt...))
}

func (d *wgDevice) receiveData(msg []byte, src netip.AddrPort, buf []byte) {
	if len(msg) < wgKeepaliveSize {
		return
	}
	index := binary.LittleEndian.Uint32(msg[4:8])
	counter := binary.LittleEndian.Uint64(msg[8:16])
	now := d.now()
	d.mu.Lock()
	kp := d.keypairLocked(index)
	d.mu.Unlock()
	if kp == nil || now.Sub(kp.created) >= wgRejectAfterTime || counter >= wgRejectAfterMessages {
		return
	}
	nonce := wgNonce(counter)
	plain, err := kp.recv.Open(buf[:0], nonce[:], msg[wgDataHeaderSize:], nil)
	if err != nil {
		return
	}

	d.mu.Lock()
	if !kp.replay.accept(counter) {
		d.mu.Unlock()
		return
	}
	var queued [][]byte
	if kp == d.next {
		// The initiator's first packet under the session we answered: it is
		// confirmed and becomes the one we send with.
		d.previous, d.current, d.next = d.current, kp, nil
		d.handshakeDoneLocked(now)
		queued = d.queue
		d.queue = nil
	}
	d.endpoint = src
	d.anyReceivedLocked(now)
	if len(plain) > 0 {
		d.dataReceivedLocked(now)
	}
	var init []byte
	if kp == d.current && kp.initiator &&
		now.Sub(kp.created) >= wgRejectAfterTime-wgKeepaliveTimeout-wgRekeyTimeout {
		init = d.initiateLocked(now, false)
	}
	d.mu.Unlock()
	d.rx.Add(uint64(len(msg)))
	d.write(init, src)
	for _, p := range queued {
		d.sendPacket(p, nil)
	}
	if len(plain) == 0 {
		return
	}
	// Only a whole IP packet from an address the peer may use reaches the
	// system; the padding is cut off by the packet's own length.
	from, _, n, ok := wgPacketAddrs(plain)
	if !ok || !wgPrefixesContain(d.cfg.Allowed, from) {
		return
	}
	_, _ = d.tun.Write(plain[:n])
}

// ── Timer bookkeeping (whitepaper §6.2–§6.5) ─────────────────────────────

func (d *wgDevice) anySentLocked(now time.Time) {
	d.keepaliveAt = time.Time{}
	d.traversalLocked(now)
}

func (d *wgDevice) anyReceivedLocked(now time.Time) {
	d.newHandshakeAt = time.Time{}
	d.traversalLocked(now)
}

func (d *wgDevice) traversalLocked(now time.Time) {
	if d.cfg.Keepalive > 0 {
		d.persistentAt = now.Add(d.cfg.Keepalive)
	}
}

func (d *wgDevice) dataSentLocked(now time.Time) {
	if d.newHandshakeAt.IsZero() {
		d.newHandshakeAt = now.Add(wgKeepaliveTimeout + wgRekeyTimeout + wgJitter())
	}
}

// dataReceivedLocked arms the passive keepalive: data came in, and if nothing
// goes back within wgKeepaliveTimeout a keepalive tells the peer we are here.
func (d *wgDevice) dataReceivedLocked(now time.Time) {
	if d.keepaliveAt.IsZero() {
		d.keepaliveAt = now.Add(wgKeepaliveTimeout)
	}
}

// tick runs the timers that are due.
func (d *wgDevice) tick() {
	now := d.now()
	var init []byte
	keepalive := false
	d.mu.Lock()
	if !d.retransmitAt.IsZero() && !now.Before(d.retransmitAt) {
		d.retransmitAt = time.Time{}
		if now.Sub(d.attemptsSince) >= wgRekeyAttemptTime {
			d.logf("no answer from %s for %d seconds; trying again when there is something to send",
				d.endpoint, int(wgRekeyAttemptTime/time.Second))
			d.attemptsSince = time.Time{}
			d.queue = nil
			d.gaveUp = true
			if d.zeroKeysAt.IsZero() {
				d.zeroKeysAt = now.Add(3 * wgRejectAfterTime)
			}
		} else {
			d.lastInitiation = time.Time{}
			init = d.initiateLocked(now, true)
		}
	}
	if !d.keepaliveAt.IsZero() && !now.Before(d.keepaliveAt) {
		d.keepaliveAt = time.Time{}
		keepalive = true
	}
	if !d.newHandshakeAt.IsZero() && !now.Before(d.newHandshakeAt) {
		d.newHandshakeAt = time.Time{}
		if init == nil {
			init = d.initiateLocked(now, false)
		}
	}
	if !d.persistentAt.IsZero() && !now.Before(d.persistentAt) {
		d.persistentAt = time.Time{}
		keepalive = true
	}
	if !d.zeroKeysAt.IsZero() && !now.Before(d.zeroKeysAt) {
		d.zeroKeysAt = time.Time{}
		d.current, d.previous, d.next = nil, nil, nil
		d.noise.zero()
	}
	ep := d.endpoint
	d.mu.Unlock()
	d.write(init, ep)
	if keepalive {
		d.sendPacket(nil, nil)
	}
}

// ── Loops ────────────────────────────────────────────────────────────────

func (d *wgDevice) timerLoop() {
	t := time.NewTicker(wgTick)
	defer t.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-t.C:
			d.tick()
		}
	}
}

func (d *wgDevice) tunLoop() {
	buf := make([]byte, wgBufSize)
	out := make([]byte, 0, wgBufSize+wgDataHeaderSize+wgTagSize+16)
	for {
		n, err := d.tun.Read(buf)
		if d.closed.Load() {
			return
		}
		if err != nil {
			d.logf("tunnel read failed: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		pkt := buf[:n]
		if d.outboundOK(pkt) {
			d.sendPacket(pkt, out)
		}
	}
}

func (d *wgDevice) udpLoop() {
	buf := make([]byte, wgBufSize)
	plain := make([]byte, 0, wgBufSize)
	for {
		n, src, err := d.conn.ReadFromUDPAddrPort(buf)
		if d.closed.Load() {
			return
		}
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		d.receive(buf[:n], netip.AddrPortFrom(src.Addr().Unmap(), src.Port()), plain)
	}
}

// receive dispatches one datagram from the network.
func (d *wgDevice) receive(msg []byte, src netip.AddrPort, buf []byte) {
	if len(msg) < 4 {
		return
	}
	switch binary.LittleEndian.Uint32(msg[:4]) {
	case wgMsgInitiation:
		if len(msg) == wgInitiationSize {
			d.receiveInitiation(msg, src)
		}
	case wgMsgResponse:
		if len(msg) == wgResponseSize {
			d.receiveResponse(msg, src)
		}
	case wgMsgCookie:
		if len(msg) == wgCookieSize {
			d.receiveCookie(msg)
		}
	case wgMsgData:
		d.receiveData(msg, src, buf)
	}
}

// write sends one message, logging a failing network at most once a minute.
func (d *wgDevice) write(msg []byte, to netip.AddrPort) {
	if len(msg) == 0 || !to.IsValid() || d.closed.Load() {
		return
	}
	n, err := d.conn.WriteToUDPAddrPort(msg, to)
	if err != nil {
		now := d.now()
		d.mu.Lock()
		quiet := !d.lastWriteErr.IsZero() && now.Sub(d.lastWriteErr) < time.Minute
		if !quiet {
			d.lastWriteErr = now
		}
		d.mu.Unlock()
		if !quiet {
			d.logf("cannot reach %s: %v", to, err)
		}
		return
	}
	d.tx.Add(uint64(n))
}

// ── Packets ──────────────────────────────────────────────────────────────

// wgPacketAddrs reads an IPv4 or IPv6 header: the source and destination and
// the packet's length as its header gives it (which must fit p).
func wgPacketAddrs(p []byte) (src, dst netip.Addr, length int, ok bool) {
	if len(p) == 0 {
		return
	}
	switch p[0] >> 4 {
	case 4:
		if len(p) < 20 {
			return
		}
		total := int(binary.BigEndian.Uint16(p[2:4]))
		if total < 20 || total > len(p) {
			return
		}
		return netip.AddrFrom4([4]byte(p[12:16])), netip.AddrFrom4([4]byte(p[16:20])), total, true
	case 6:
		if len(p) < 40 {
			return
		}
		total := 40 + int(binary.BigEndian.Uint16(p[4:6]))
		if total > len(p) {
			return
		}
		return netip.AddrFrom16([16]byte(p[8:24])), netip.AddrFrom16([16]byte(p[24:40])), total, true
	}
	return
}

func wgPrefixesContain(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// outboundOK reports whether a packet the system routed into the tunnel goes
// to the peer: an IP packet for an address the peer takes, that is not our
// own UDP traffic to the server finding its way back in. (A family the tunnel
// has no address for never gets here: the daemon routes it to "unreachable".)
func (d *wgDevice) outboundOK(pkt []byte) bool {
	_, dst, _, ok := wgPacketAddrs(pkt)
	if !ok || !wgPrefixesContain(d.cfg.Allowed, dst) {
		return false
	}
	d.mu.Lock()
	ep := d.endpoint
	d.mu.Unlock()
	return !wgOwnTraffic(pkt, ep, d.cfg.LocalPort)
}

// wgOwnTraffic reports whether pkt is a UDP datagram from our WireGuard port to
// the server — the tunnel's own traffic routed back into it, which would loop.
func wgOwnTraffic(pkt []byte, ep netip.AddrPort, localPort uint16) bool {
	_, dst, _, ok := wgPacketAddrs(pkt)
	if !ok || localPort == 0 || dst != ep.Addr() {
		return false
	}
	var udp []byte
	switch pkt[0] >> 4 {
	case 4:
		ihl := int(pkt[0]&0x0f) * 4
		if pkt[9] != 17 || ihl < 20 || len(pkt) < ihl+8 {
			return false
		}
		udp = pkt[ihl:]
	case 6:
		if pkt[6] != 17 || len(pkt) < 48 {
			return false
		}
		udp = pkt[40:]
	default:
		return false
	}
	return binary.BigEndian.Uint16(udp[0:2]) == localPort && binary.BigEndian.Uint16(udp[2:4]) == ep.Port()
}
