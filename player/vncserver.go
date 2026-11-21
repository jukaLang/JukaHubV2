package main

// vncserver.go — VNC server: the handheld's screen in any VNC app on the
// same network, and control of it (RealVNC Viewer, TigerVNC, macOS Screen
// Sharing, bVNC or AVNC on a phone).
//
//   - Off by default. Tools → Network tools → VNC starts it
//     (vncserver_ui.go). It stops itself after fileServerIdleLimit with nobody
//     connected and when the device joins another network (the file
//     servers' rules, fileserver_guard.go), at the startup lock, and with
//     JukaHub.
//   - Port 5900 (vnc_port / JUKAHUB_VNC_PORT) on every interface, but only
//     LAN addresses get past accept: private, link-local and loopback, and
//     Tailscale's 100.64.0.0/10 (webdavLANIP; Tailscale's userspace daemon
//     forwards to loopback). At most vncMaxClients viewers; a third is told
//     why.
//   - VNC Authentication only, never "None", with an 8-character password
//     made on first use and kept in the user config (vncPassword, a secret by
//     its name, like Screen Share's token) so a viewer's saved entry keeps
//     working; New password replaces it and disconnects every viewer. A wrong
//     password costs vncFailDelay, and vncFailLimit of them in a minute lock
//     that address out for a minute. VNC's DES challenge is weak by today's
//     standards — the other reason it stays on the LAN.
//   - The picture is JukaHub or the game on screen (vncserver_frames.go),
//     sent as the 64×64 tiles that changed since the viewer's last update,
//     Raw or ZRLE (vncserver_proto.go). Keys, mouse, clipboard:
//     vncserver_input.go.
//   - Windows runs it too, for JukaHub's own screens: a game there plays in a
//     window of its own that JukaHub can neither read nor type into.
//
// Each viewer has two goroutines: the reader, which owns the parser and the
// input, and the writer, which owns every byte sent after the handshake.
// Neither touches SDL or the config.

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	vncDefaultPort = 5900
	vncMaxClients  = 2
	// vncHandshakeLimit bounds a connection's sign-in; vncMessageLimit the
	// rest of a message once its first byte came; vncWriteLimit one update.
	vncHandshakeLimit = 15 * time.Second
	vncMessageLimit   = 15 * time.Second
	vncWriteLimit     = 15 * time.Second
	// A wrong password costs vncFailDelay; vncFailLimit of them inside
	// vncFailWindow lock the address out until the window has passed.
	vncFailDelay  = 2 * time.Second
	vncFailLimit  = 5
	vncFailWindow = time.Minute
	// vncPasswordKey keeps the password in the user config. Its name marks it
	// a secret for config exports (cfgdocIsSecretKey matches "password").
	vncPasswordKey = "vncPassword"
	vncPasswordLen = 8
	// vncShotPassword is shown instead in screenshot mode.
	vncShotPassword = "jukademo"
	// vncPasswordAlphabet leaves out what a small screen makes look alike
	// (0/O, 1/l/I), as the SSH password does.
	vncPasswordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
)

// vncSheetSeq changes whenever the VNC or Telnet sheet must be rebuilt: a
// start, a stop, a new password.
var vncSheetSeq atomic.Uint64

// vncService is the running server. Use vncSrv.
type vncService struct {
	mu       sync.Mutex
	ln       net.Listener
	port     int
	gen      uint64
	lastErr  string
	password string // for the connection goroutines; the config holds the saved copy
	netBase  fsNetIdentity
	conns    map[*vncClient]struct{}
	fbStop   chan struct{}
	clients  fsClients
	activity fsActivity
	fails    vncFailGate
}

var vncSrv = &vncService{}

// Running reports whether the listener is open.
func (s *vncService) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln != nil
}

// Port is the port the server listens on (or will).
func (s *vncService) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port > 0 {
		return s.port
	}
	return vncDefaultPort
}

// ViewerAddress is what a viewer types: the address alone on the standard
// port (display 0), "address::port" otherwise — "address:5901" would mean
// display 5901 to most viewers. "" while stopped or offline.
func (s *vncService) ViewerAddress() string {
	if !s.Running() {
		return ""
	}
	host := toolsIPAddress()
	if host == "" {
		return ""
	}
	if p := s.Port(); p != vncDefaultPort {
		return host + "::" + strconv.Itoa(p)
	}
	return host
}

// Clients lists the viewers for the Network tools page.
func (s *vncService) Clients() []fsClient { return s.clients.list() }

// Start opens the server. Main thread: it reads the config (the password,
// the port, the pad's button layout, the framebuffer turn).
func (s *vncService) Start(config *Config) error {
	if s.Running() {
		return nil
	}
	pass, err := vncPasswordFor(config)
	if err != nil {
		return fmt.Errorf("make a VNC password: %w", err)
	}
	vncPadSetCodes(mbridgeParamsFor(config))
	vncLive.canvasW.Store(screenWidth)
	vncLive.canvasH.Store(screenHeight)
	vncLive.rotate.Store(int32(castRotateFor(config)))
	addr := fmt.Sprintf("0.0.0.0:%d", vncPort(config))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("start the VNC server on %s: %w", addr, err)
	}
	stop := make(chan struct{})
	s.mu.Lock()
	s.ln, s.password, s.lastErr, s.fbStop = ln, pass, "", stop
	s.gen++
	gen := s.gen
	s.netBase = fsNetIdentity{ip: localIPv4()}
	s.conns = make(map[*vncClient]struct{})
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	vncLive.on.Store(true)
	s.mu.Unlock()
	s.activity.touch()
	vncSheetSeq.Add(1)
	go vncFBLoop(stop)
	go s.acceptLoop(ln)
	vncWatchRun("VNC", gen, s.watchTarget, s.stopRun)
	logInfo("vnc", "serving on %s (at most %d viewers)", ln.Addr(), vncMaxClients)
	return nil
}

// Stop closes the listener and every viewer.
func (s *vncService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopLocked()
}

// stopRun stops the server if it is still run gen (the watchdog's stop).
func (s *vncService) stopRun(gen uint64) bool {
	s.mu.Lock()
	if s.ln == nil || s.gen != gen {
		s.mu.Unlock()
		return false
	}
	s.stopLocked()
	return true
}

// stopLocked is Stop for a caller holding s.mu, which it releases. The
// viewers are closed after the lock, since their goroutines take it to
// leave.
func (s *vncService) stopLocked() {
	ln, stop := s.ln, s.fbStop
	s.ln, s.fbStop = nil, nil
	conns := make([]*vncClient, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[*vncClient]struct{})
	s.clients.clear()
	vncLive.on.Store(false)
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if stop != nil {
		close(stop)
	}
	for _, c := range conns {
		c.close()
	}
	vncPadClose()
	vncQueueInput(vncInput{kind: vncInRelease})
	vncHubReset()
	if ln != nil {
		logInfo("vnc", "stopped")
		vncSheetSeq.Add(1)
	}
}

// watchTarget is the server as the watchdog sees it: a connected viewer
// counts as use, so the idle stop waits for everyone to leave.
func (s *vncService) watchTarget() fsWatchTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.activity.lastUsed()
	if len(s.conns) > 0 {
		last = time.Now()
	}
	return fsWatchTarget{running: s.ln != nil, gen: s.gen, base: s.netBase, lastUsed: last}
}

// acceptLoop admits LAN viewers, up to vncMaxClients.
func (s *vncService) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		host := fsClientHost(conn.RemoteAddr().String())
		if !webdavLANIP(net.ParseIP(host)) {
			logWarn("vnc", "refused %s: not on this network", host)
			_ = conn.Close()
			continue
		}
		if s.fails.lockedOut(host, time.Now()) {
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.ln != ln {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		if len(s.conns) >= vncMaxClients {
			s.mu.Unlock()
			go func(conn net.Conn) {
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_ = vncRefuse(conn, "Two viewers are already connected to this handheld")
				_ = conn.Close()
			}(conn)
			continue
		}
		c := newVncClient(s, conn)
		s.conns[c] = struct{}{}
		pass := s.password
		s.mu.Unlock()
		go s.serve(c, pass)
	}
}

// serve signs a viewer in, sends ServerInit and runs its two goroutines.
func (s *vncService) serve(c *vncClient, password string) {
	defer s.drop(c)
	conn := c.conn
	host := fsClientHost(conn.RemoteAddr().String())
	_ = conn.SetDeadline(time.Now().Add(vncHandshakeLimit))
	challenge := make([]byte, 16)
	if _, err := rand.Read(challenge); err != nil {
		return
	}
	_, ok, err := vncHandshake(conn, password, challenge, func() {
		s.fails.note(host, time.Now())
		logWarn("vnc", "wrong password from %s", host)
		time.Sleep(vncFailDelay)
	})
	if err != nil || !ok {
		if err != nil {
			logDebug("vnc", "%s did not sign in: %v", host, err)
		}
		return
	}
	// ClientInit's shared flag is not needed: every viewer shares, and there
	// are at most two.
	var shared [1]byte
	if _, err := io.ReadFull(conn, shared[:]); err != nil {
		return
	}
	w, h := vncFrameSize()
	name := "JukaHub"
	hello := binary.BigEndian.AppendUint16(nil, uint16(w))
	hello = binary.BigEndian.AppendUint16(hello, uint16(h))
	hello = vncNativeFormat.appendTo(hello)
	hello = binary.BigEndian.AppendUint32(hello, uint32(len(name)))
	hello = append(hello, name...)
	if _, err := conn.Write(hello); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	c.w, c.h = w, h
	c.id = s.clients.add(conn.RemoteAddr().String())
	s.clients.setUser(c.id, "viewer")
	s.activity.touch()
	logInfo("vnc", "viewer signed in from %s", host)
	go c.writeLoop()
	c.readLoop()
}

// drop forgets a viewer that left: its keys are let go, and the frames go
// with the last viewer.
func (s *vncService) drop(c *vncClient) {
	c.close()
	s.mu.Lock()
	delete(s.conns, c)
	left := len(s.conns)
	s.mu.Unlock()
	if c.id != 0 {
		s.clients.remove(c.id)
		vncPadReleaseAll()
		vncQueueInput(vncInput{kind: vncInRelease})
		logInfo("vnc", "viewer %s left", fsClientHost(c.conn.RemoteAddr().String()))
	}
	if left == 0 {
		vncHubReset()
	}
}

// vncFrameSize is the framebuffer size a viewer is told: JukaHub's canvas.
func vncFrameSize() (int, int) {
	w, h := int(vncLive.canvasW.Load()), int(vncLive.canvasH.Load())
	if w <= 0 || h <= 0 {
		return 1280, 720
	}
	return w, h
}

// ── One viewer ────────────────────────────────────────────────────────────

// vncClient is one signed-in viewer (or one signing in).
type vncClient struct {
	srv  *vncService
	conn net.Conn
	id   uint64
	done chan struct{}
	once sync.Once
	wake chan struct{}
	w, h int // the framebuffer size the viewer knows (writer goroutine)

	mu      sync.Mutex
	conv    *vncConv
	zrle    bool
	desktop bool
	// The update request waiting for an answer: req merges every request
	// since the last answer, latest is the newest alone, reqSeq counts them.
	hasReq     bool
	req        vncRect
	reqIncr    bool
	reqSeq     uint64
	latest     vncRect
	latestIncr bool
}

func newVncClient(s *vncService, conn net.Conn) *vncClient {
	return &vncClient{
		srv: s, conn: conn, done: make(chan struct{}), wake: make(chan struct{}, 1),
		conv: newVncConv(vncNativeFormat),
	}
}

// close ends the connection once; both goroutines notice.
func (c *vncClient) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// readLoop parses the viewer's messages until the connection ends.
func (c *vncClient) readLoop() {
	br := bufio.NewReaderSize(c.conn, 8<<10)
	for {
		_ = c.conn.SetReadDeadline(time.Time{})
		m, err := vncReadClientMessage(br, func() {
			_ = c.conn.SetReadDeadline(time.Now().Add(vncMessageLimit))
		})
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrDeadlineExceeded) {
				logDebug("vnc", "viewer disconnected: %v", err)
			}
			return
		}
		c.srv.activity.touch()
		switch m.kind {
		case vncMsgSetPixelFormat:
			if !m.pf.usable() {
				logInfo("vnc", "a viewer asked for a colour-map pixel format (%d bits): keeping true colour", m.pf.bpp)
				continue
			}
			conv := newVncConv(m.pf)
			c.mu.Lock()
			c.conv = conv
			c.mu.Unlock()
		case vncMsgSetEncodings:
			zrle, desktop := false, false
			for _, e := range m.encodings {
				switch e {
				case vncEncZRLE:
					zrle = true
				case vncEncDesktopSize:
					desktop = true
				}
			}
			c.mu.Lock()
			c.zrle, c.desktop = zrle, desktop
			c.mu.Unlock()
		case vncMsgUpdateRequest:
			c.post(m.incr, m.rect)
		case vncMsgKeyEvent:
			vncViewerKey(m.down, m.key)
		case vncMsgPointerEvent:
			if !gameScreenSuspended() {
				vncQueueInput(vncInput{kind: vncInPointer, buttons: m.buttons, x: m.x, y: m.y})
			}
		case vncMsgCutText:
			if m.skipped {
				logInfo("vnc", "clipboard text over %d KB from a viewer was not taken", vncMaxCutText>>10)
				continue
			}
			if strings.TrimSpace(m.text) != "" {
				vncQueueInput(vncInput{kind: vncInClip, text: m.text})
			}
		}
	}
}

// post records an update request and wakes the writer.
func (c *vncClient) post(incr bool, r vncRect) {
	c.mu.Lock()
	if c.hasReq {
		c.req, c.reqIncr = c.req.union(r), c.reqIncr && incr
	} else {
		c.req, c.reqIncr, c.hasReq = r, incr, true
	}
	c.reqSeq++
	c.latest, c.latestIncr = r, incr
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// consume marks request seq answered. A request that arrived meanwhile stays
// pending, as itself rather than merged with the one answered.
func (c *vncClient) consume(seq uint64) {
	c.mu.Lock()
	if c.reqSeq == seq {
		c.hasReq = false
	} else {
		c.req, c.reqIncr = c.latest, c.latestIncr
	}
	c.mu.Unlock()
}

// send writes one message with a deadline: a viewer whose network cannot
// take an update in vncWriteLimit is dropped.
func (c *vncClient) send(b []byte) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(vncWriteLimit))
	_, err := c.conn.Write(b)
	return err
}

// writeLoop answers update requests: for an incremental one it waits for a
// frame newer than the last it sent and sends the tiles that changed inside
// the requested area; a full one gets the whole area of the current frame.
func (c *vncClient) writeLoop() {
	defer c.close()
	var prev *vncFrame
	defer func() { vncHubRelease(prev) }()
	var dirty []bool
	var runs []vncRect
	var out []byte
	var z vncZRLE
	for {
		c.mu.Lock()
		has, req, incr, seq := c.hasReq, c.req, c.reqIncr, c.reqSeq
		conv, zrle, desktop := c.conv, c.zrle, c.desktop
		c.mu.Unlock()
		if !has {
			select {
			case <-c.done:
				return
			case <-c.wake:
			}
			continue
		}
		after := uint64(0)
		if prev != nil {
			after = prev.seq
			if !incr {
				after = prev.seq - 1
			}
		}
		cur := vncHubAcquire(after, time.Second, c.done)
		select {
		case <-c.done:
			vncHubRelease(cur)
			return
		default:
		}
		if cur == nil {
			continue
		}
		if cur.w != c.w || cur.h != c.h {
			// TV mode changed the canvas. A viewer that understands
			// DesktopSize resizes and gets everything again; one that does
			// not would draw the new picture into the old frame.
			if !desktop {
				logInfo("vnc", "a viewer that cannot follow the new screen size (%dx%d) was disconnected", cur.w, cur.h)
				vncHubRelease(cur)
				return
			}
			out = append(out[:0], 0, 0, 0, 1)
			out = vncAppendRectHeader(out, vncRect{w: cur.w, h: cur.h}, vncEncDesktopSize)
			c.w, c.h = cur.w, cur.h
			vncHubRelease(cur)
			vncHubRelease(prev)
			prev, dirty = nil, nil
			if err := c.send(out); err != nil {
				return
			}
			c.consume(seq)
			continue
		}
		if n := vncTilesAcross(c.w) * vncTilesAcross(c.h); len(dirty) != n || prev == nil {
			dirty = make([]bool, n)
			for i := range dirty {
				dirty[i] = true
			}
		} else if prev != cur {
			vncMarkChanged(dirty, prev.pix, cur.pix, c.w, c.h)
		}
		area := req.intersect(vncRect{w: c.w, h: c.h})
		if !incr {
			vncMarkArea(dirty, c.w, area)
		}
		runs = vncDirtyRuns(dirty, c.w, c.h, area, runs[:0])
		vncHubRelease(prev)
		prev = cur
		if len(runs) == 0 {
			if !incr {
				// A full request for an empty area still gets its answer.
				if err := c.send([]byte{0, 0, 0, 0}); err != nil {
					return
				}
				c.consume(seq)
			}
			continue
		}
		var err error
		if out, err = vncEncodeUpdate(out[:0], cur, runs, conv, zrle, &z); err != nil {
			logDebug("vnc", "update not encoded: %v", err)
			return
		}
		if err := c.send(out); err != nil {
			return
		}
		c.consume(seq)
		c.srv.activity.touch()
	}
}

// vncMarkArea marks every tile that area touches dirty (a full request).
func vncMarkArea(dirty []bool, w int, area vncRect) {
	if area.w <= 0 || area.h <= 0 {
		return
	}
	cols := vncTilesAcross(w)
	for ty := area.y / vncTile; ty <= (area.y+area.h-1)/vncTile; ty++ {
		for tx := area.x / vncTile; tx <= (area.x+area.w-1)/vncTile; tx++ {
			if i := ty*cols + tx; i >= 0 && i < len(dirty) {
				dirty[i] = true
			}
		}
	}
}

// vncEncodeUpdate appends a FramebufferUpdate carrying runs of frame f,
// ZRLE when the viewer listed it, else Raw.
func vncEncodeUpdate(out []byte, f *vncFrame, runs []vncRect, conv *vncConv, zrle bool, z *vncZRLE) ([]byte, error) {
	out = append(out, 0, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(len(runs)))
	stride := f.w * 4
	for _, r := range runs {
		if !zrle {
			out = vncAppendRectHeader(out, r, vncEncRaw)
			out = conv.appendRaw(out, f.pix, stride, r)
			continue
		}
		out = vncAppendRectHeader(out, r, vncEncZRLE)
		var err error
		if out, err = z.appendRect(out, conv, f.pix, stride, r); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ── Password, port, lockout, watchdog ─────────────────────────────────────

// vncGeneratePassword makes n characters of vncPasswordAlphabet, a dash
// between groups of group (0 for none). Bytes past the alphabet's last whole
// multiple are drawn again, so every character is equally likely.
func vncGeneratePassword(n, group int) (string, error) {
	const alpha = vncPasswordAlphabet
	limit := byte(256 / len(alpha) * len(alpha))
	var b strings.Builder
	buf := make([]byte, 32)
	for got := 0; got < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, v := range buf {
			if v >= limit || got >= n {
				continue
			}
			if group > 0 && got > 0 && got%group == 0 {
				b.WriteByte('-')
			}
			b.WriteByte(alpha[int(v)%len(alpha)])
			got++
		}
	}
	return b.String(), nil
}

// vncPasswordValid reports a saved password of the shape the server makes,
// so a hand-edited config cannot shorten it.
func vncPasswordValid(s string) bool {
	if len(s) != vncPasswordLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(vncPasswordAlphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// vncPasswordFor is the saved password, made and saved on first use. Main
// thread: it reads and writes the config.
func vncPasswordFor(config *Config) (string, error) {
	if config != nil {
		if saved := strings.TrimSpace(customStringValue(config.Variables.Custom, vncPasswordKey)); vncPasswordValid(saved) {
			return saved, nil
		}
	}
	pass, err := vncGeneratePassword(vncPasswordLen, 0)
	if err != nil {
		return "", err
	}
	if config != nil {
		setStoreString(config, vncPasswordKey, pass)
	}
	return pass, nil
}

// vncNewPassword replaces the password: saved, in force for the next viewer,
// and every viewer connected now is disconnected. Main thread.
func vncNewPassword(config *Config) error {
	pass, err := vncGeneratePassword(vncPasswordLen, 0)
	if err != nil {
		return err
	}
	if config != nil {
		setStoreString(config, vncPasswordKey, pass)
	}
	s := vncSrv
	s.mu.Lock()
	s.password = pass
	conns := make([]*vncClient, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
	vncSheetSeq.Add(1)
	logInfo("vnc", "new password; %d viewer(s) disconnected", len(conns))
	return nil
}

// vncPort is the configured port: a numeric vnc_port, else JUKAHUB_VNC_PORT,
// else 5900. Main thread.
func vncPort(config *Config) int {
	return vncPortSetting(config, "vnc_port", "JUKAHUB_VNC_PORT", vncDefaultPort)
}

// vncPortSetting reads a port from the config key, then the environment
// variable, then falls back to def. Main thread.
func vncPortSetting(config *Config, key, env string, def int) int {
	if config != nil {
		if raw, ok := customString(config.Variables.Custom, key); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && p > 0 && p < 65536 {
				return p
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv(env)); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return def
}

// vncFailGate counts wrong passwords by address (VNC and Telnet each keep
// one).
type vncFailGate struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

// note records a wrong password from host.
func (g *vncFailGate) note(host string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = make(map[string][]time.Time)
	}
	g.m[host] = append(g.pruneLocked(host, now), now)
}

// lockedOut reports whether host had vncFailLimit wrong passwords inside
// vncFailWindow.
func (g *vncFailGate) lockedOut(host string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pruneLocked(host, now)) >= vncFailLimit
}

// pruneLocked drops host's failures older than the window (and the host
// itself once it has none, so the map cannot grow without bound).
func (g *vncFailGate) pruneLocked(host string, now time.Time) []time.Time {
	list := g.m[host]
	keep := list[:0]
	for _, at := range list {
		if now.Sub(at) < vncFailWindow {
			keep = append(keep, at)
		}
	}
	if len(keep) == 0 {
		delete(g.m, host)
		return nil
	}
	g.m[host] = keep
	return keep
}

// vncWatchRun watches one run of a server the way fileserver_guard.go
// watches FTP and SFTP: it stops the run after fileServerIdleLimit unused or
// when the device leaves the network it started on, and says why in a
// toast. It ends with the run. A fault stops the server: one nobody watches
// would outlive a move to a café's network.
func vncWatchRun(name string, gen uint64, target func() fsWatchTarget, stopRun func(uint64) bool) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logError("vnc", "%s watchdog stopped: %v", name, r)
				if stopRun(gen) {
					ShowToast(fsStopMessage([]string{name}, fsStopUnwatched), ToastKindWarning)
				}
			}
		}()
		w := &fsWatcher{idleLimit: fileServerIdleLimit}
		ticker := time.NewTicker(fileServerWatchInterval)
		defer ticker.Stop()
		for range ticker.C {
			t := target()
			if !t.running || t.gen != gen {
				return
			}
			reason := w.decide(time.Now(), fsReadNetIdentity(), []fsWatchTarget{t})[0]
			if reason == "" || !stopRun(gen) {
				continue
			}
			logInfo("vnc", "%s server stopped: %s", name, reason)
			kind := ToastKindWarning
			if reason == fsStopIdle {
				kind = ToastKindInfo
			}
			ShowToast(fsStopMessage([]string{name}, reason), kind)
			return
		}
	}()
}
