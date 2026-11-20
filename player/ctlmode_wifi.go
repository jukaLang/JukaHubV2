package main

// ctlmode_wifi.go — Controller Mode's "Wi-Fi to a PC" link: the JukaPad
// protocol (ctlmode_wifi_proto.go) to the receiver on a Windows or Linux PC
// (ctlmode/jukapad_receiver.py, which the handheld serves on a small download
// page while this link runs).
//
// While it runs there are three goroutines, all ended by Stop: a reader on
// the UDP socket; a worker that sends probes, pairing retries and the pad
// state when their timers fall due (it sleeps in between — nothing polls);
// and the download page's HTTP server. Everything the page reads is kept
// under one mutex and answered from memory.
//
// The handheld finds receivers by broadcast once a second while nothing is
// connected, pairs with one the first time (a 6-digit code on both screens),
// and from then on connects to the PC it used last whenever it answers.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The receiver and its Windows launcher, served by the download page.
//
//go:embed ctlmode/jukapad_receiver.py
var jpadReceiverPy []byte

//go:embed ctlmode/jukapad-receiver.bat
var jpadReceiverBat []byte

const (
	// ctlWifiStoreFile (in ctlModeDir) holds this handheld's id and the
	// paired PCs with their keys (0600).
	ctlWifiStoreFile = "jukapad.json"

	ctlWifiProbeEvery  = time.Second
	ctlWifiRetryEvery  = 500 * time.Millisecond
	ctlWifiConnectWait = 5 * time.Second
	ctlWifiLinkTimeout = 3 * time.Second
	ctlWifiPairTimeout = 2 * time.Minute
	ctlWifiStateFast   = 50 * time.Millisecond
	ctlWifiStateIdle   = 250 * time.Millisecond
	ctlWifiStateGap    = 4 * time.Millisecond
	// ctlWifiSeenFor is how long a PC counts as on the network after its
	// last answer.
	ctlWifiSeenFor = 5 * time.Second
	// ctlWifiBackoff keeps automatic connection off a PC that just failed
	// or closed.
	ctlWifiBackoff = 3 * time.Second
	ctlWifiNoteFor = 10 * time.Second
	ctlWifiMaxPCs  = 16
)

type ctlWifiPhase int

const (
	ctlWifiSearching ctlWifiPhase = iota
	ctlWifiPairing
	ctlWifiConnecting
	ctlWifiConnected
)

// ctlWifiPC is a receiver found on the network, paired, or both.
type ctlWifiPC struct {
	rid    []byte
	ridHex string
	name   string
	addr   *net.UDPAddr
	seen   time.Time
	// key is the paired key, nil when not paired.
	key      []byte
	pairedAt string
	// forgotten: the PC says it no longer knows this handheld.
	forgotten bool
	// retryAt: no automatic connection before then.
	retryAt time.Time
}

// ctlWifiPair is a pairing in progress. stage: 1 PAIR_HELLO sent, 2
// PAIR_NONCE sent, 3 the code is on screen, 4 confirmed here (waiting for
// the PC's user).
type ctlWifiPair struct {
	pc      *ctlWifiPC
	pid     []byte
	priv    *ecdh.PrivateKey
	pka     []byte
	pkb     []byte
	commit  []byte
	na      []byte
	ltk     []byte
	stage   int
	started time.Time
	nextTx  time.Time
}

// ctlWifiSession is a connection to a paired PC, from HELLO on.
type ctlWifiSession struct {
	pc  *ctlWifiPC
	na  []byte
	sk  []byte
	sid uint32
	// proof is the PROOF packet, resent until the first FEEDBACK.
	proof     []byte
	txSeq     uint32
	rxSeq     uint32
	started   time.Time
	lastRx    time.Time
	nextTx    time.Time
	lastState time.Time
	nextState time.Time
}

type ctlWifiOut struct {
	pkt []byte
	to  *net.UDPAddr
}

// ctlWifiStoreBody is ctlWifiStoreFile on disk.
type ctlWifiStoreBody struct {
	ID   string           `json:"id"`
	Last string           `json:"last,omitempty"`
	PCs  []ctlWifiStorePC `json:"pcs"`
}

type ctlWifiStorePC struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Key    string `json:"key"`
	Addr   string `json:"addr,omitempty"`
	Paired string `json:"paired,omitempty"`
}

type ctlWifi struct {
	mu       sync.Mutex
	running  bool
	gen      int
	conn     *net.UDPConn
	stop     chan struct{}
	wake     chan struct{}
	httpSrv  *http.Server
	httpPort int
	url      string

	loaded  bool
	hid     []byte
	name    string
	last    string
	pcs     []*ctlWifiPC
	actions []string

	phase     ctlWifiPhase
	pair      *ctlWifiPair
	sess      *ctlWifiSession
	prompt    *ctlPrompt
	note      string
	noteUntil time.Time
	started   time.Time
	nextProbe time.Time
	probedAt  map[string]time.Time

	pad      ctlPadState
	padDirty bool
	padAt    time.Time

	availAt time.Time
	availOK bool

	// saveMu orders the store's writes (the reader and the worker both save).
	saveMu sync.Mutex
}

func init() {
	ctlModeRegisterTransport(10, &ctlWifi{})
}

func (w *ctlWifi) ID() string    { return "wifi" }
func (w *ctlWifi) Title() string { return "Wi-Fi to a PC" }
func (w *ctlWifi) Subtitle() string {
	return "A controller for Windows and Linux games, with the free JukaPad receiver on the PC"
}

// Available needs a network with an IPv4 address (checked at most every 2 s).
func (w *ctlWifi) Available() (bool, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if w.availAt.IsZero() || now.Sub(w.availAt) > 2*time.Second {
		w.availAt = now
		w.availOK = len(ctlLANv4()) > 0
	}
	if !w.availOK {
		return false, "Connect to Wi-Fi first"
	}
	return true, ""
}

func (w *ctlWifi) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: jpadHandheldPort})
	if err != nil {
		if conn, err = net.ListenUDP("udp4", &net.UDPAddr{}); err != nil {
			return ctlError("The network is not ready for the controller link (" + err.Error() + ")")
		}
	}
	ln, err := net.Listen("tcp4", ":"+strconv.Itoa(jpadHTTPPort))
	if err != nil {
		ln, err = net.Listen("tcp4", ":0")
		if err != nil {
			ln = nil
			logWarn("ctlmode", "wifi: no port for the receiver download page: %v", err)
		}
	}
	w.gen++
	gen := w.gen
	w.running = true
	w.conn = conn
	w.stop = make(chan struct{})
	w.wake = make(chan struct{}, 1)
	w.phase = ctlWifiSearching
	w.pair, w.sess, w.prompt = nil, nil, nil
	w.note, w.noteUntil = "", time.Time{}
	w.started = time.Now()
	w.nextProbe = time.Time{}
	w.actions = nil
	w.pad, w.padDirty = ctlPadState{}, false
	w.probedAt = map[string]time.Time{}
	for _, pc := range w.pcs {
		pc.seen, pc.retryAt, pc.forgotten = time.Time{}, time.Time{}, false
	}
	w.httpSrv, w.httpPort, w.url = nil, 0, ""
	if ln != nil {
		srv := &http.Server{
			Handler:           http.HandlerFunc(ctlWifiServeHTTP),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    8 << 10,
		}
		w.httpSrv = srv
		if a, ok := ln.Addr().(*net.TCPAddr); ok {
			w.httpPort = a.Port
		}
		go func() { _ = srv.Serve(ln) }()
	}
	go w.reader(gen, conn)
	go w.worker(gen, conn, w.stop, w.wake)
	return nil
}

// Stop says goodbye to the PC (it releases every button at once) or cancels
// a pairing, then closes the socket and the download page.
func (w *ctlWifi) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	w.gen++
	conn, srv := w.conn, w.httpSrv
	var out []ctlWifiOut
	if s := w.sess; s != nil && s.sk != nil && s.pc.addr != nil {
		s.txSeq++
		bye := jpadPackBye(s.sk, jpadByeH, s.sid, s.txSeq)
		out = append(out, ctlWifiOut{bye, s.pc.addr}, ctlWifiOut{bye, s.pc.addr})
	}
	if p := w.pair; p != nil && p.pc.addr != nil {
		out = append(out, ctlWifiOut{jpadPackPairFail(p.pid, jpadFailRefused), p.pc.addr})
	}
	close(w.stop)
	w.conn, w.httpSrv = nil, nil
	w.sess, w.pair, w.prompt = nil, nil, nil
	w.phase = ctlWifiSearching
	w.actions = nil
	w.mu.Unlock()
	ctlWifiFlush(conn, out)
	_ = conn.Close()
	if srv != nil {
		_ = srv.Close()
	}
}

// Send keeps the newest state; the worker sends it at once and repeats it.
func (w *ctlWifi) Send(st ctlPadState) {
	w.mu.Lock()
	w.pad = st
	w.padDirty = true
	w.padAt = time.Now()
	wake := w.wake
	live := w.running && w.phase == ctlWifiConnected
	w.mu.Unlock()
	if live {
		ctlWifiSignal(wake)
	}
}

func (w *ctlWifi) Status() ctlLinkStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return ctlLinkStatus{State: ctlLinkIdle}
	}
	now := time.Now()
	switch w.phase {
	case ctlWifiConnected:
		pc := w.sess.pc
		return ctlLinkStatus{State: ctlLinkConnected, Peer: pc.name, PeerID: "jukapad:" + pc.ridHex,
			Detail: "Connected to " + pc.name + " over Wi-Fi"}
	case ctlWifiPairing:
		name := w.pair.pc.name
		switch {
		case w.prompt != nil:
			return ctlLinkStatus{State: ctlLinkPairing, Detail: "Compare the code with " + name + "'s screen"}
		case w.pair.stage == 4:
			return ctlLinkStatus{State: ctlLinkWaiting, Detail: "Press Enter on " + name + " to finish pairing"}
		}
		return ctlLinkStatus{State: ctlLinkWaiting, Detail: "Pairing with " + name + "…"}
	case ctlWifiConnecting:
		return ctlLinkStatus{State: ctlLinkWaiting, Detail: "Connecting to " + w.sess.pc.name + "…"}
	}
	detail := "Looking for the JukaPad receiver on this Wi-Fi…"
	switch {
	case w.note != "" && now.Before(w.noteUntil):
		detail = w.note
	case w.anySeenLocked(now):
		detail = "Choose your PC below"
	case now.Sub(w.started) > 8*time.Second:
		detail = "Run the receiver on your PC — see Get the PC receiver"
	}
	return ctlLinkStatus{State: ctlLinkWaiting, Detail: detail}
}

func (w *ctlWifi) anySeenLocked(now time.Time) bool {
	for _, pc := range w.pcs {
		if ctlWifiSeen(pc, now) {
			return true
		}
	}
	return false
}

func ctlWifiSeen(pc *ctlWifiPC, now time.Time) bool {
	return !pc.seen.IsZero() && now.Sub(pc.seen) < ctlWifiSeenFor
}

// Rows: the PCs (A pairs or connects), Forget for the paired ones, and where
// to get the receiver.
func (w *ctlWifi) Rows() []ctlRow {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return nil
	}
	now := time.Now()
	var rows, forget []ctlRow
	for _, pc := range w.pcs {
		var detail string
		switch {
		case w.sess != nil && w.sess.pc == pc && w.phase == ctlWifiConnected:
			detail = "Connected"
		case w.sess != nil && w.sess.pc == pc:
			detail = "Connecting…"
		case w.pair != nil && w.pair.pc == pc:
			detail = "Pairing…"
		case pc.key != nil && pc.forgotten:
			detail = "It forgot this handheld — A pairs again"
		case pc.key != nil && ctlWifiSeen(pc, now):
			detail = "Paired — A connects"
		case pc.key != nil:
			detail = "Paired · not seen on this network"
		case ctlWifiSeen(pc, now):
			detail = "Found — A pairs"
		default:
			continue
		}
		rows = append(rows, ctlRow{Label: pc.name, Detail: detail, Action: "pc:" + pc.ridHex})
		if pc.key != nil {
			forget = append(forget, ctlRow{Label: "Forget " + pc.name, Detail: "It will need pairing again",
				Action: "forget:" + pc.ridHex})
		}
	}
	rows = append(rows, forget...)
	get := ctlRow{Label: "Get the PC receiver", Detail: "The download page is not available"}
	if w.url != "" {
		get.Detail, get.Action = w.url, "receiver"
	}
	return append(rows, get)
}

// Prompt is the pairing question (immutable once published).
func (w *ctlWifi) Prompt() *ctlPrompt {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prompt
}

// Do queues an action for the worker ("pc:<id>", "forget:<id>", "pair-yes",
// "pair-no"); "receiver" shows where to download the receiver.
func (w *ctlWifi) Do(action string) {
	if action == "receiver" {
		w.mu.Lock()
		url := w.url
		w.mu.Unlock()
		if url != "" {
			ShowToast("On the PC, open "+url+" in a web browser", ToastKindInfo)
		}
		return
	}
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	if action == "pair-yes" || action == "pair-no" {
		w.prompt = nil
	}
	if len(w.actions) < 8 {
		w.actions = append(w.actions, action)
	}
	wake := w.wake
	w.mu.Unlock()
	ctlWifiSignal(wake)
}

// ── Goroutines ───────────────────────────────────────────────────────────

func ctlWifiSignal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func ctlWifiFlush(conn *net.UDPConn, out []ctlWifiOut) {
	if conn == nil {
		return
	}
	for _, o := range out {
		if o.to != nil {
			_, _ = conn.WriteToUDP(o.pkt, o.to)
		}
	}
}

func ctlWifiRandom(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func (w *ctlWifi) live(gen int) bool {
	return w.running && w.gen == gen
}

func (w *ctlWifi) reader(gen int, conn *net.UDPConn) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			w.mu.Lock()
			alive := w.live(gen)
			w.mu.Unlock()
			if !alive {
				return
			}
			// Windows reports an ICMP "port unreachable" from an earlier send
			// as a read error: keep reading.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if from != nil && n >= 4 {
			w.handle(gen, conn, buf[:n], from)
		}
	}
}

func (w *ctlWifi) worker(gen int, conn *net.UDPConn, stop, wake chan struct{}) {
	w.load(gen)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-wake:
		case <-timer.C:
		}
		wait, ok := w.tick(gen, conn, time.Now())
		if !ok {
			return
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

// load reads the store once (the first start) and the handheld's name.
func (w *ctlWifi) load(gen int) {
	name := peerDeviceName()
	ip := localIPv4()
	var body ctlWifiStoreBody
	have := false
	if dir := ctlModeDir(); dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, ctlWifiStoreFile)); err == nil {
			if err := json.Unmarshal(raw, &body); err != nil {
				logWarn("ctlmode", "wifi: %s unreadable, starting fresh: %v", ctlWifiStoreFile, err)
			} else {
				have = true
			}
		}
	}
	w.mu.Lock()
	if !w.live(gen) {
		w.mu.Unlock()
		return
	}
	w.name = name
	w.setURLLocked(ip)
	var snap []byte
	if !w.loaded {
		w.loaded = true
		hid, err := hex.DecodeString(body.ID)
		fresh := !have || err != nil || len(hid) != jpadIDLen
		if fresh {
			hid = ctlWifiRandom(jpadIDLen)
		}
		w.hid = hid
		w.last = strings.ToLower(body.Last)
		for _, e := range body.PCs {
			rid, err1 := hex.DecodeString(e.ID)
			key, err2 := hex.DecodeString(e.Key)
			if err1 != nil || err2 != nil || len(rid) != jpadIDLen || len(key) != jpadKeyLen || len(w.pcs) >= ctlWifiMaxPCs {
				continue
			}
			pc := &ctlWifiPC{rid: rid, ridHex: hex.EncodeToString(rid), name: ctlWifiCleanName(e.Name), key: key, pairedAt: e.Paired}
			if a4 := net.ParseIP(e.Addr).To4(); a4 != nil {
				pc.addr = &net.UDPAddr{IP: a4, Port: jpadReceiverPort}
			}
			w.pcs = append(w.pcs, pc)
		}
		if fresh {
			snap = w.storeJSONLocked()
		}
	}
	wake := w.wake
	w.mu.Unlock()
	if snap != nil {
		w.save(snap)
	}
	ctlWifiSignal(wake)
}

func ctlWifiCleanName(s string) string {
	s = jpadReadName(jpadPackName(s), 0)
	if s == "" {
		s = "PC"
	}
	return s
}

func (w *ctlWifi) setURLLocked(ip string) {
	if ip == "" || w.httpPort == 0 {
		w.url = ""
		return
	}
	w.url = "http://" + net.JoinHostPort(ip, strconv.Itoa(w.httpPort))
}

// storeJSONLocked is the store's file body.
func (w *ctlWifi) storeJSONLocked() []byte {
	body := ctlWifiStoreBody{ID: hex.EncodeToString(w.hid), Last: w.last, PCs: []ctlWifiStorePC{}}
	for _, pc := range w.pcs {
		if pc.key == nil {
			continue
		}
		e := ctlWifiStorePC{ID: pc.ridHex, Name: pc.name, Key: hex.EncodeToString(pc.key), Paired: pc.pairedAt}
		if pc.addr != nil {
			e.Addr = pc.addr.IP.String()
		}
		body.PCs = append(body.PCs, e)
	}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil
	}
	return data
}

func (w *ctlWifi) save(data []byte) {
	dir := ctlModeDir()
	if dir == "" || data == nil {
		return
	}
	w.saveMu.Lock()
	defer w.saveMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logWarn("ctlmode", "wifi: cannot create %s: %v", dir, err)
		return
	}
	if err := AtomicWrite(filepath.Join(dir, ctlWifiStoreFile), data, 0o600); err != nil {
		logWarn("ctlmode", "wifi: cannot save the paired PCs: %v", err)
	}
}

// tick runs the queued actions and whatever timer fell due; it returns how
// long the worker may sleep.
func (w *ctlWifi) tick(gen int, conn *net.UDPConn, now time.Time) (time.Duration, bool) {
	var out []ctlWifiOut
	save := false
	w.mu.Lock()
	if !w.live(gen) {
		w.mu.Unlock()
		return 0, false
	}
	if w.hid == nil {
		w.mu.Unlock()
		return time.Second, true
	}
	actions := w.actions
	w.actions = nil
	for _, a := range actions {
		if w.doLocked(a, now, &out) {
			save = true
		}
	}
	if w.phase == ctlWifiSearching {
		w.autoConnectLocked(now)
	}
	wait := w.stepLocked(now, &out)
	var probePkt []byte
	var unicast []*net.UDPAddr
	if w.phase == ctlWifiSearching || w.phase == ctlWifiPairing {
		if !now.Before(w.nextProbe) {
			probePkt = jpadPackProbe(w.hid, w.name)
			w.nextProbe = now.Add(ctlWifiProbeEvery)
			for _, pc := range w.pcs {
				if pc.key != nil && pc.addr != nil {
					unicast = append(unicast, pc.addr)
				}
			}
			if len(w.probedAt) > 64 {
				w.probedAt = map[string]time.Time{}
			}
		}
		if d := w.nextProbe.Sub(now); d < wait {
			wait = d
		}
	}
	var snap []byte
	if save {
		snap = w.storeJSONLocked()
	}
	w.mu.Unlock()
	ctlWifiFlush(conn, out)
	if snap != nil {
		w.save(snap)
	}
	if probePkt != nil {
		for _, to := range ctlWifiProbeTargets(unicast) {
			_, _ = conn.WriteToUDP(probePkt, to)
		}
		ip := localIPv4()
		w.mu.Lock()
		if w.live(gen) {
			w.setURLLocked(ip)
		}
		w.mu.Unlock()
	}
	if wait < 2*time.Millisecond {
		wait = 2 * time.Millisecond
	}
	return wait, true
}

// stepLocked sends what is due for the current phase.
func (w *ctlWifi) stepLocked(now time.Time, out *[]ctlWifiOut) time.Duration {
	wait := time.Second
	switch w.phase {
	case ctlWifiPairing:
		p := w.pair
		if now.Sub(p.started) > ctlWifiPairTimeout {
			*out = append(*out, ctlWifiOut{jpadPackPairFail(p.pid, jpadFailTimeout), p.pc.addr})
			w.endPairLocked("Pairing timed out — choose the PC to try again", now)
			break
		}
		if p.stage == 1 && now.Sub(p.started) > 2*ctlWifiConnectWait {
			w.endPairLocked(p.pc.name+" did not answer — is the receiver still running?", now)
			break
		}
		if p.stage == 3 {
			break
		}
		if !now.Before(p.nextTx) {
			*out = append(*out, ctlWifiOut{w.pairPacketLocked(p), p.pc.addr})
			p.nextTx = now.Add(ctlWifiRetryEvery)
		}
		wait = p.nextTx.Sub(now)
	case ctlWifiConnecting:
		s := w.sess
		if now.Sub(s.started) > ctlWifiConnectWait {
			s.pc.retryAt = now.Add(ctlWifiBackoff)
			w.sess = nil
			w.phase = ctlWifiSearching
			w.noteLocked(s.pc.name+" did not answer — is the receiver still running?", now)
			break
		}
		if !now.Before(s.nextTx) {
			pkt := s.proof
			if pkt == nil {
				pkt = jpadPackHello(w.hid, s.pc.rid, s.na)
			}
			*out = append(*out, ctlWifiOut{pkt, s.pc.addr})
			s.nextTx = now.Add(ctlWifiRetryEvery)
		}
		wait = s.nextTx.Sub(now)
	case ctlWifiConnected:
		s := w.sess
		if now.Sub(s.lastRx) > ctlWifiLinkTimeout {
			w.sess = nil
			w.phase = ctlWifiSearching
			w.noteLocked("Lost "+s.pc.name+" — looking for it again…", now)
			logInfo("ctlmode", "wifi: link to %s lost", s.pc.name)
			break
		}
		// A stick streams hundreds of events a second: changes closer than
		// ctlWifiStateGap are merged into one packet.
		due := !now.Before(s.nextState)
		if w.padDirty && !due && now.Sub(s.lastState) >= ctlWifiStateGap {
			due = true
		}
		if due {
			w.padDirty = false
			s.txSeq++
			*out = append(*out, ctlWifiOut{jpadPackState(s.sk, s.sid, s.txSeq, w.pad), s.pc.addr})
			every := ctlWifiStateIdle
			if now.Sub(w.padAt) < time.Second || w.pad != (ctlPadState{}) {
				every = ctlWifiStateFast
			}
			s.lastState = now
			s.nextState = now.Add(every)
		}
		wait = s.nextState.Sub(now)
		if w.padDirty {
			wait = s.lastState.Add(ctlWifiStateGap).Sub(now)
		}
		if d := s.lastRx.Add(ctlWifiLinkTimeout).Sub(now) + 10*time.Millisecond; d < wait {
			wait = d
		}
	}
	return wait
}

func (w *ctlWifi) pairPacketLocked(p *ctlWifiPair) []byte {
	switch p.stage {
	case 1:
		return jpadPackPairHello(p.pid, w.hid, p.pka, w.name)
	case 2:
		return jpadPackPairNonce(p.pid, p.na)
	}
	return jpadPackPairConfirm(p.pid, jpadConfirmTag(p.ltk, "h", p.pid))
}

func (w *ctlWifi) noteLocked(msg string, now time.Time) {
	w.note, w.noteUntil = msg, now.Add(ctlWifiNoteFor)
}

func (w *ctlWifi) endPairLocked(msg string, now time.Time) {
	w.pair, w.prompt = nil, nil
	w.phase = ctlWifiSearching
	w.noteLocked(msg, now)
}

func (w *ctlWifi) pcLocked(ridHex string) *ctlWifiPC {
	for _, pc := range w.pcs {
		if pc.ridHex == ridHex {
			return pc
		}
	}
	return nil
}

// leaveLocked ends the current pairing or connection (the PC hears of it).
func (w *ctlWifi) leaveLocked(out *[]ctlWifiOut) {
	if p := w.pair; p != nil {
		*out = append(*out, ctlWifiOut{jpadPackPairFail(p.pid, jpadFailRefused), p.pc.addr})
	}
	if s := w.sess; s != nil && s.sk != nil {
		s.txSeq++
		*out = append(*out, ctlWifiOut{jpadPackBye(s.sk, jpadByeH, s.sid, s.txSeq), s.pc.addr})
	}
	w.pair, w.sess, w.prompt = nil, nil, nil
	w.phase = ctlWifiSearching
}

// doLocked runs one queued action; true when the store changed.
func (w *ctlWifi) doLocked(action string, now time.Time, out *[]ctlWifiOut) bool {
	switch {
	case strings.HasPrefix(action, "pc:"):
		pc := w.pcLocked(strings.TrimPrefix(action, "pc:"))
		if pc == nil {
			return false
		}
		if (w.sess != nil && w.sess.pc == pc) || (w.pair != nil && w.pair.pc == pc) {
			return false
		}
		if pc.addr == nil {
			w.noteLocked(pc.name+" is not on this network — start the receiver on it", now)
			return false
		}
		w.leaveLocked(out)
		if pc.key != nil && !pc.forgotten {
			w.startConnectLocked(pc, now)
		} else {
			w.startPairLocked(pc, now)
		}
	case strings.HasPrefix(action, "forget:"):
		pc := w.pcLocked(strings.TrimPrefix(action, "forget:"))
		if pc == nil || pc.key == nil {
			return false
		}
		if (w.sess != nil && w.sess.pc == pc) || (w.pair != nil && w.pair.pc == pc) {
			w.leaveLocked(out)
		}
		pc.key, pc.forgotten, pc.pairedAt = nil, false, ""
		if w.last == pc.ridHex {
			w.last = ""
		}
		if !ctlWifiSeen(pc, now) {
			for i, p := range w.pcs {
				if p == pc {
					w.pcs = append(w.pcs[:i], w.pcs[i+1:]...)
					break
				}
			}
		}
		w.noteLocked("Forgot "+pc.name, now)
		logInfo("ctlmode", "wifi: forgot %s", pc.name)
		return true
	case action == "pair-yes":
		if p := w.pair; p != nil && p.stage == 3 {
			p.stage = 4
			p.nextTx = now
		}
	case action == "pair-no":
		if p := w.pair; p != nil {
			*out = append(*out, ctlWifiOut{jpadPackPairFail(p.pid, jpadFailRefused), p.pc.addr})
			w.endPairLocked("Pairing cancelled", now)
		}
	}
	return false
}

func (w *ctlWifi) startPairLocked(pc *ctlWifiPC, now time.Time) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		w.noteLocked("Pairing could not start: "+err.Error(), now)
		return
	}
	w.pair = &ctlWifiPair{pc: pc, pid: ctlWifiRandom(jpadPIDLen), priv: priv, pka: priv.PublicKey().Bytes(),
		stage: 1, started: now, nextTx: now}
	w.sess = nil
	w.phase = ctlWifiPairing
	logInfo("ctlmode", "wifi: pairing with %s", pc.name)
}

func (w *ctlWifi) startConnectLocked(pc *ctlWifiPC, now time.Time) {
	w.sess = &ctlWifiSession{pc: pc, na: ctlWifiRandom(jpadNonceLen), started: now, nextTx: now}
	w.pair = nil
	w.phase = ctlWifiConnecting
}

// autoConnectLocked connects to a paired PC that is on the network: the one
// used last first.
func (w *ctlWifi) autoConnectLocked(now time.Time) {
	var pick *ctlWifiPC
	for _, pc := range w.pcs {
		if pc.key == nil || pc.forgotten || pc.addr == nil || !ctlWifiSeen(pc, now) || now.Before(pc.retryAt) {
			continue
		}
		if pick == nil || pc.ridHex == w.last {
			pick = pc
		}
	}
	if pick != nil {
		w.startConnectLocked(pick, now)
	}
}

// ── Incoming packets ─────────────────────────────────────────────────────

func (w *ctlWifi) handle(gen int, conn *net.UDPConn, pkt []byte, from *net.UDPAddr) {
	t := jpadType(pkt)
	if t == 0 {
		return
	}
	var out []ctlWifiOut
	var rumble float32
	var rumbleMS uint32
	save := false
	w.mu.Lock()
	if !w.live(gen) || w.hid == nil {
		w.mu.Unlock()
		return
	}
	now := time.Now()
	switch t {
	case jpadAnnounce:
		w.onAnnounceLocked(pkt, from, now, &out)
	case jpadPairCommit, jpadPairReveal, jpadPairDone, jpadPairFail:
		save = w.onPairLocked(t, pkt, now, &out)
	case jpadChallenge:
		w.onChallengeLocked(pkt, from, now, &out)
	case jpadNotPaired:
		w.onNotPairedLocked(pkt, now)
	case jpadFeedback:
		rumble, rumbleMS, save = w.onFeedbackLocked(pkt, from, now)
	case jpadByeR:
		w.onByeLocked(pkt, now)
	}
	wake := w.wake
	var snap []byte
	if save {
		snap = w.storeJSONLocked()
	}
	w.mu.Unlock()
	ctlWifiFlush(conn, out)
	if snap != nil {
		w.save(snap)
	}
	if rumble > 0 {
		ctlModeRumble(rumble, rumbleMS)
	}
	// A keepalive changes nothing the worker waits for; the first one of a
	// session (save) starts the state stream.
	if t != jpadFeedback || save {
		ctlWifiSignal(wake)
	}
}

func ctlWifiZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func (w *ctlWifi) onAnnounceLocked(pkt []byte, from *net.UDPAddr, now time.Time, out *[]ctlWifiOut) {
	m, ok := jpadParseAnnounce(pkt)
	if !ok {
		return
	}
	if ctlWifiZero(m.HID) {
		// A receiver started with --tsp: answer with a unicast probe, which
		// gets a proper reply through networks that drop broadcasts.
		key := from.String()
		if now.Sub(w.probedAt[key]) >= time.Second {
			w.probedAt[key] = now
			*out = append(*out, ctlWifiOut{jpadPackProbe(w.hid, w.name), from})
		}
		return
	}
	if !bytes.Equal(m.HID, w.hid) {
		return
	}
	ridHex := hex.EncodeToString(m.RID)
	pc := w.pcLocked(ridHex)
	if pc == nil {
		if len(w.pcs) >= ctlWifiMaxPCs && !w.dropStaleLocked(now) {
			return
		}
		pc = &ctlWifiPC{rid: m.RID, ridHex: ridHex}
		w.pcs = append(w.pcs, pc)
	}
	switch {
	case m.Name != "":
		pc.name = m.Name
	case pc.name == "":
		pc.name = "PC at " + from.IP.String()
	}
	// ANNOUNCE is not authenticated: it must not move a live session (the
	// session follows its authenticated CHALLENGE and FEEDBACK instead), or
	// one forged packet would send every STATE elsewhere for good.
	if w.sess == nil || w.sess.pc != pc {
		pc.addr = from
	}
	pc.seen = now
	if pc.key != nil {
		pc.forgotten = !m.Paired
	}
}

// dropStaleLocked removes one unpaired PC no longer on the network.
func (w *ctlWifi) dropStaleLocked(now time.Time) bool {
	for i, pc := range w.pcs {
		if pc.key == nil && !ctlWifiSeen(pc, now) && (w.pair == nil || w.pair.pc != pc) {
			w.pcs = append(w.pcs[:i], w.pcs[i+1:]...)
			return true
		}
	}
	return false
}

// onPairLocked runs the handheld's side of pairing; true when a PC was
// paired (the store changed).
func (w *ctlWifi) onPairLocked(t byte, pkt []byte, now time.Time, out *[]ctlWifiOut) bool {
	p := w.pair
	if p == nil || w.phase != ctlWifiPairing {
		return false
	}
	switch t {
	case jpadPairCommit:
		m, ok := jpadParseCommit(pkt)
		if !ok || p.stage != 1 || !bytes.Equal(m.PID, p.pid) || !bytes.Equal(m.RID, p.pc.rid) {
			return false
		}
		// Na goes out only now, after the PC is bound to its Nb.
		p.pkb, p.commit = m.PKB, m.C
		p.na = ctlWifiRandom(jpadPairNonceLen)
		p.stage = 2
		*out = append(*out, ctlWifiOut{w.pairPacketLocked(p), p.pc.addr})
		p.nextTx = now.Add(ctlWifiRetryEvery)
	case jpadPairReveal:
		pid, nb, ok := jpadParsePID(pkt, jpadPairReveal, jpadPairNonceLen)
		if !ok || p.stage != 2 || !bytes.Equal(pid, p.pid) {
			return false
		}
		if subtle.ConstantTimeCompare(jpadCommit(p.pkb, p.pka, nb), p.commit) != 1 {
			*out = append(*out, ctlWifiOut{jpadPackPairFail(p.pid, jpadFailTamper), p.pc.addr})
			w.endPairLocked("Pairing stopped: the PC's answer did not match — try again", now)
			logWarn("ctlmode", "wifi: pairing commitment mismatch from %s", p.pc.name)
			return false
		}
		var z []byte
		pub, err := ecdh.X25519().NewPublicKey(p.pkb)
		if err == nil {
			z, err = p.priv.ECDH(pub)
		}
		var ltk []byte
		if err == nil {
			ltk, err = jpadDeriveLTK(z, p.na, nb, w.hid, p.pc.rid, p.pka, p.pkb)
		}
		if err != nil {
			*out = append(*out, ctlWifiOut{jpadPackPairFail(p.pid, jpadFailTamper), p.pc.addr})
			w.endPairLocked("Pairing failed: the PC sent a bad key", now)
			return false
		}
		p.ltk = ltk
		p.stage = 3
		w.prompt = &ctlPrompt{
			Title:     "Pair with " + p.pc.name + "?",
			Code:      jpadFormatCode(jpadPairCode(p.pka, p.pkb, p.na, nb)),
			Body:      "The PC shows a code too. If they match, choose Codes match, then press Enter on the PC.",
			Yes:       "Codes match",
			No:        "Cancel",
			YesAction: "pair-yes",
			NoAction:  "pair-no",
		}
	case jpadPairDone:
		pid, tag, ok := jpadParsePID(pkt, jpadPairDone, jpadTagLen)
		if !ok || p.stage != 4 || !bytes.Equal(pid, p.pid) || !hmac.Equal(tag, jpadConfirmTag(p.ltk, "r", p.pid)) {
			return false
		}
		pc := p.pc
		pc.key, pc.forgotten = p.ltk, false
		pc.pairedAt = now.Format("2006-01-02")
		w.last = pc.ridHex
		w.pair, w.prompt = nil, nil
		w.noteLocked("Paired with "+pc.name, now)
		logInfo("ctlmode", "wifi: paired with %s", pc.name)
		w.startConnectLocked(pc, now)
		return true
	case jpadPairFail:
		pid, reason, ok := jpadParsePID(pkt, jpadPairFail, 1)
		if !ok || !bytes.Equal(pid, p.pid) {
			return false
		}
		name := p.pc.name
		msg := "Pairing with " + name + " stopped"
		switch reason[0] {
		case jpadFailRefused:
			msg = name + " refused the pairing"
		case jpadFailBusy:
			msg = name + " is pairing another handheld — try again in a minute"
		case jpadFailTamper:
			msg = "Pairing stopped: " + name + " saw a changed message — try again"
		case jpadFailTimeout:
			msg = "Pairing timed out — choose the PC to try again"
		}
		w.endPairLocked(msg, now)
	}
	return false
}

func (w *ctlWifi) onChallengeLocked(pkt []byte, from *net.UDPAddr, now time.Time, out *[]ctlWifiOut) {
	s := w.sess
	if s == nil || w.phase != ctlWifiConnecting || s.proof != nil {
		return
	}
	m, ok := jpadParseChallenge(pkt)
	if !ok || !bytes.Equal(m.RID, s.pc.rid) || !bytes.Equal(m.HID, w.hid) || !bytes.Equal(m.NA, s.na) {
		return
	}
	key := s.pc.key
	if !hmac.Equal(m.Proof, jpadProofTag(key, "r", w.hid, s.pc.rid, s.na, m.NB)) {
		return
	}
	s.sk = jpadSessionKey(key, w.hid, s.pc.rid, s.na, m.NB)
	s.sid = jpadSessionID(s.sk)
	s.proof = jpadPackProof(w.hid, m.NB, jpadProofTag(key, "h", w.hid, s.pc.rid, s.na, m.NB))
	s.pc.addr = from
	*out = append(*out, ctlWifiOut{s.proof, from})
	s.nextTx = now.Add(ctlWifiRetryEvery)
}

func (w *ctlWifi) onNotPairedLocked(pkt []byte, now time.Time) {
	rid, hid, ok := jpadParseNotPaired(pkt)
	s := w.sess
	if !ok || s == nil || w.phase != ctlWifiConnecting || !bytes.Equal(rid, s.pc.rid) || !bytes.Equal(hid, w.hid) {
		return
	}
	s.pc.forgotten = true
	w.sess = nil
	w.phase = ctlWifiSearching
	w.noteLocked(s.pc.name+" no longer knows this handheld — choose it to pair again", now)
}

// onFeedbackLocked takes the receiver's keepalive and rumble. The first one
// of a session is the PC's word that it accepted the proof: the link is up.
func (w *ctlWifi) onFeedbackLocked(pkt []byte, from *net.UDPAddr, now time.Time) (float32, uint32, bool) {
	s := w.sess
	if s == nil || s.sk == nil || (w.phase != ctlWifiConnecting && w.phase != ctlWifiConnected) {
		return 0, 0, false
	}
	m, ok := jpadUnpackFeedback(s.sk, pkt)
	if !ok || m.SID != s.sid || m.Seq <= s.rxSeq {
		return 0, 0, false
	}
	s.rxSeq = m.Seq
	s.lastRx = now
	// The states go where the authenticated keepalives come from.
	s.pc.addr = from
	save := false
	if w.phase == ctlWifiConnecting {
		w.phase = ctlWifiConnected
		s.proof = nil
		s.nextState = now
		w.padDirty = true
		w.note = ""
		w.last = s.pc.ridHex
		save = true
		logInfo("ctlmode", "wifi: connected to %s", s.pc.name)
	}
	if m.MS == 0 || (m.Large == 0 && m.Small == 0) {
		return 0, 0, save
	}
	return float32(max(m.Large, m.Small)) / 255, uint32(m.MS), save
}

func (w *ctlWifi) onByeLocked(pkt []byte, now time.Time) {
	s := w.sess
	if s == nil || s.sk == nil {
		return
	}
	sid, seq, ok := jpadUnpackBye(s.sk, jpadByeR, pkt)
	if !ok || sid != s.sid || seq <= s.rxSeq {
		return
	}
	s.pc.retryAt = now.Add(ctlWifiBackoff)
	w.sess = nil
	w.phase = ctlWifiSearching
	w.noteLocked(s.pc.name+" closed the receiver", now)
}

// ── Network helpers (the DSU link uses them too) ─────────────────────────

// ctlLANAddr is one IPv4 address of this device and its subnet's broadcast
// address (nil on a /32 or /31).
type ctlLANAddr struct {
	IP, Bcast net.IP
}

// ctlLANv4 lists the device's usable IPv4 addresses: up, not loopback, not
// point-to-point (a VPN tunnel), not link-local.
func ctlLANv4() []ctlLANAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []ctlLANAddr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			la := ctlLANAddr{IP: ip}
			mask := ipNet.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			if ones, bits := mask.Size(); len(mask) == net.IPv4len && bits == 32 && ones < 31 {
				b := make(net.IP, net.IPv4len)
				for i := range b {
					b[i] = ip[i] | ^mask[i]
				}
				la.Bcast = b
			}
			out = append(out, la)
		}
	}
	return out
}

// ctlWifiProbeTargets: the limited broadcast, every directed broadcast and
// the paired PCs' last addresses.
func ctlWifiProbeTargets(unicast []*net.UDPAddr) []*net.UDPAddr {
	out := []*net.UDPAddr{{IP: net.IPv4bcast, Port: jpadReceiverPort}}
	seen := map[string]bool{net.IPv4bcast.String(): true}
	add := func(ip net.IP) {
		if ip == nil || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		out = append(out, &net.UDPAddr{IP: ip, Port: jpadReceiverPort})
	}
	for _, la := range ctlLANv4() {
		add(la.Bcast)
	}
	for _, u := range unicast {
		add(u.IP)
	}
	return out
}

// ── The receiver's download page ─────────────────────────────────────────

// ctlWifiServeHTTP serves the how-to page and the two receiver files to the
// local network only.
func ctlWifiServeHTTP(rw http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		http.Error(rw, "Only devices on the local network can download the receiver.", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/", "/index.html":
		self, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			self = r.Host
		}
		if net.ParseIP(self) == nil {
			self = "HANDHELD_IP"
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(rw, ctlWifiPage(self))
	case "/jukapad_receiver.py":
		rw.Header().Set("Content-Type", "text/x-python; charset=utf-8")
		rw.Header().Set("Content-Disposition", `attachment; filename="jukapad_receiver.py"`)
		_, _ = rw.Write(jpadReceiverPy)
	case "/jukapad-receiver.bat":
		rw.Header().Set("Content-Type", "application/octet-stream")
		rw.Header().Set("Content-Disposition", `attachment; filename="jukapad-receiver.bat"`)
		_, _ = rw.Write(jpadReceiverBat)
	default:
		http.NotFound(rw, r)
	}
}

// ctlWifiPage is the one-page how-to; ip is the handheld's address as the
// browser reached it.
func ctlWifiPage(ip string) string {
	ip = html.EscapeString(ip)
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>JukaPad receiver</title>
<style>:root{color-scheme:light}body{font:16px/1.5 system-ui,sans-serif;max-width:44em;margin:2em auto;padding:0 1em;color:#1d1d1f;background:#fafafa}
h1{font-size:1.5em}a.b{display:inline-block;margin:.3em .5em .3em 0;padding:.5em 1em;border-radius:.5em;background:#2563eb;color:#fff;text-decoration:none}
code{background:#eee;padding:.1em .3em;border-radius:.3em}li{margin:.4em 0}.n{color:#555;font-size:.92em}</style></head><body>
<h1>JukaPad receiver</h1>
<p>Use your JukaHub handheld as a game controller on this PC, over Wi-Fi.</p>
<p><a class="b" href="/jukapad-receiver.bat">Windows launcher (.bat)</a><a class="b" href="/jukapad_receiver.py">Receiver (.py)</a></p>
<ol>
<li><b>Windows:</b> save both files in the same folder and double-click <code>jukapad-receiver.bat</code>.
It needs Python 3.8 or newer from <a href="https://www.python.org/downloads/">python.org</a>; the first run installs
<code>vgamepad</code> and its ViGEmBus driver (accept the driver installer). Allow Python on private networks when Windows asks.
Add <code>--pad ds4</code> for a DualShock 4 instead of an Xbox 360 controller.</li>
<li><b>Linux:</b> run <code>python3 jukapad_receiver.py</code>. It needs write access to <code>/dev/uinput</code>; if it is missing, the receiver prints the two commands that allow it.</li>
<li><b>macOS</b> is not supported (no virtual controllers).</li>
<li>On the handheld, open <b>Controller Mode → Wi-Fi to a PC</b> and choose this PC. The first time, both screens show a
6-digit code: if they match, choose <b>Codes match</b> on the handheld and press <b>Enter</b> on the PC. After that the handheld connects by itself.</li>
</ol>
<p class="n">PC not found? Guest Wi-Fi and "AP isolation" block the search. Start the receiver with
<code>--tsp ` + ip + `</code> (for example <code>jukapad-receiver.bat --tsp ` + ip + `</code>), and allow UDP port 8320 in the PC's firewall.</p>
<p class="n">Emulators such as Dolphin, Cemu or Eden can read the handheld with nothing installed: choose <b>DSU</b> in Controller Mode instead.</p>
</body></html>
`
}
