package main

// peernet.go — the wire side of the peer layer: the HTTPS listener and its
// API, the client that pins the key of the device it talks to, and LAN
// discovery.
//
// Two ports, both only open while Settings → Network → Nearby devices is on:
//
//   - UDP 8321 answers the datagram "JUKAPEER?1" with one small JSON object:
//     the device's id, name, model and TLS port (plus what it is playing when
//     it shares that — nearby.go). The answer's source address is where the
//     scanner connects, so a device cannot point it at a third host. The same
//     broadcast targets as Watch Party's discovery are used, and replies are
//     rate limited so the handheld cannot be used as a reflector.
//   - TCP 8322 (or any free port, announced in the answer) is HTTPS with TLS
//     1.3 only. Both sides present a certificate made from their own key and
//     pin the other's: the server lets any key in for the pairing endpoints and
//     the shared Nearby summary, and refuses every other endpoint to a key that
//     is not paired. At most eight connections are served at once, headers are
//     bounded, and every handler bounds its body.
//
// The API (all under /peer/v1/):
//
//	GET  hello               who is this (any key)
//	POST pair/hello|reveal|confirm   pairing (any key, peerpair.go)
//	GET  nearby              what this device shares (any key, nearby.go)
//	POST unpair              the caller forgot this device (paired)
//	GET|POST activity        handoff (paired, hoff.go)
//	GET  saves/manifest      save list with hashes (paired, ssync.go)
//	POST saves/prepare       snapshot what a sync will overwrite (paired)
//	GET|PUT saves/file       one save file (paired, ssync.go)
//	POST saves/done          a sync finished (paired, ssync.go)

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// peerDefaultPort is the preferred TLS port; discovery is one below it.
	peerDefaultPort = 8322
	// peerDiscoveryQuery is the datagram a scanner broadcasts.
	peerDiscoveryQuery = "JUKAPEER?1"
	// peerReplyMax bounds a discovery answer; a real one is about 300 bytes.
	peerReplyMax = 1200
	// peerMaxJSONBody bounds a JSON request or answer.
	peerMaxJSONBody = 64 << 10
	// peerMaxConns caps the connections served at once.
	peerMaxConns = 8
	// peerHeaderPort carries the caller's own listening port, so a paired
	// device that calls in is reachable at that address afterwards.
	peerHeaderPort = "X-Jukahub-Port"
	// peerScanWait is how long one discovery scan listens for answers.
	peerScanWait = 1500 * time.Millisecond
	// peerBeaconRepliesPerSecond caps discovery answers.
	peerBeaconRepliesPerSecond = 20
)

var (
	errPeerWrongKey = errors.New("the device answered with a different key than the one it paired with")
	errPeerNotFound = errors.New("the device is not on this Wi-Fi right now")
	errPeerOff      = errors.New("nearby devices is off")
	errPeerNoLAN    = errors.New("not connected to Wi-Fi")
)

// peerPort is the preferred TLS port (JUKAHUB_PEER_PORT overrides it).
func peerPort() int {
	return peerEnvPort("JUKAHUB_PEER_PORT", peerDefaultPort)
}

// peerDiscoveryPort is the UDP port answers come from.
func peerDiscoveryPort() int {
	return peerEnvPort("JUKAHUB_PEER_DISCOVERY_PORT", peerDefaultPort-1)
}

func peerEnvPort(name string, fallback int) int {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return fallback
}

// ── Listener ──────────────────────────────────────────────────────────────

// peerSrv is the running listener and its discovery beacon.
var peerSrv struct {
	mu      sync.Mutex
	srv     *http.Server
	beacon  *peerBeacon
	lastErr string
}

// peerListenPortNow is the TLS port while the listener runs, else 0.
var peerListenPortNow atomic.Int32

// peerRunning reports whether the listener is up.
func peerRunning() bool { return peerListenPortNow.Load() > 0 }

// peerServerError is why the listener could not start, or "".
func peerServerError() string {
	peerSrv.mu.Lock()
	defer peerSrv.mu.Unlock()
	return peerSrv.lastErr
}

// peerReconcile makes the listener match the switch. It is the only place
// that starts or stops it, so a quick on/off/on settles on the last state.
func peerReconcile() {
	want := peerEnabledFlag.Load() && !peerShuttingDown.Load()
	peerSrv.mu.Lock()
	defer peerSrv.mu.Unlock()
	running := peerSrv.srv != nil
	if want == running {
		return
	}
	if !want {
		peerStopLocked()
		return
	}
	if err := peerStartLocked(); err != nil {
		peerSrv.lastErr = err.Error()
		log.Printf("[peer] could not start: %v", err)
	}
}

// peerRestart stops and starts the listener (a new identity needs a new
// certificate).
func peerRestart() {
	peerSrv.mu.Lock()
	defer peerSrv.mu.Unlock()
	peerStopLocked()
	if peerEnabledFlag.Load() && !peerShuttingDown.Load() {
		if err := peerStartLocked(); err != nil {
			peerSrv.lastErr = err.Error()
		}
	}
}

func peerStartLocked() error {
	self, ok := peerSelf()
	if !ok {
		return errors.New("this device has no identity yet")
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(peerPort()))
	if err != nil {
		// A second JukaHub on the same PC may hold the preferred port; any
		// free one works because discovery announces the real one.
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
	}
	port := 0
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	limited := &peerLimitListener{Listener: ln, sem: make(chan struct{}, peerMaxConns)}
	tlsLn := tls.NewListener(limited, peerServerTLS(self.cert))
	srv := &http.Server{
		Handler:           peerHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// A failed handshake from a port scanner is not worth a log line.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	peerSrv.srv = srv
	peerSrv.lastErr = ""
	peerListenPortNow.Store(int32(port))
	go func() { _ = srv.Serve(tlsLn) }()
	beacon, err := peerStartBeacon()
	if err != nil {
		// Discovery is best effort: a device that is not discoverable can still
		// be reached at the address it last used.
		log.Printf("[peer] discovery unavailable: %v", err)
		beacon = nil
	}
	peerSrv.beacon = beacon
	log.Printf("[peer] listening on port %d", port)
	return nil
}

func peerStopLocked() {
	srv, beacon := peerSrv.srv, peerSrv.beacon
	peerSrv.srv, peerSrv.beacon = nil, nil
	peerListenPortNow.Store(0)
	if beacon != nil {
		beacon.Stop()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		_ = srv.Shutdown(ctx)
		cancel()
		_ = srv.Close()
		log.Printf("[peer] listener stopped")
	}
	peerPairAbortAll("Nearby devices was turned off")
}

// peerLimitListener serves at most cap(sem) connections at once. One over the
// limit is closed at once rather than queued: a flood must not hold sockets.
type peerLimitListener struct {
	net.Listener
	sem chan struct{}
}

func (l *peerLimitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.sem <- struct{}{}:
			return &peerLimitConn{Conn: c, sem: l.sem}, nil
		default:
			_ = c.Close()
		}
	}
}

type peerLimitConn struct {
	net.Conn
	sem  chan struct{}
	once sync.Once
}

func (c *peerLimitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { <-c.sem })
	return err
}

// peerServerTLS asks every caller for a certificate (any key: the handlers
// decide what an unpaired key may do) and speaks TLS 1.3 only.
func peerServerTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("a client certificate is required")
			}
			_, err := peerCertFingerprint(raw[0])
			return err
		},
	}
}

// peerHandler is the API.
func peerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/v1/hello", peerGuard(false, peerHandleHello))
	mux.HandleFunc("/peer/v1/pair/hello", peerGuard(false, peerHandlePairHello))
	mux.HandleFunc("/peer/v1/pair/reveal", peerGuard(false, peerHandlePairReveal))
	mux.HandleFunc("/peer/v1/pair/confirm", peerGuard(false, peerHandlePairConfirm))
	mux.HandleFunc("/peer/v1/nearby", peerGuard(false, nearbyHandleInfo))
	mux.HandleFunc("/peer/v1/unpair", peerGuard(true, peerHandleUnpair))
	mux.HandleFunc("/peer/v1/activity", peerGuard(true, hoffHandleActivity))
	mux.HandleFunc("/peer/v1/saves/manifest", peerGuard(true, ssyncHandleManifest))
	mux.HandleFunc("/peer/v1/saves/prepare", peerGuard(true, ssyncHandlePrepare))
	mux.HandleFunc("/peer/v1/saves/file", peerGuard(true, ssyncHandleFile))
	mux.HandleFunc("/peer/v1/saves/done", peerGuard(true, ssyncHandleDone))
	return mux
}

// peerCall is who is calling: the key the TLS handshake proved, and whether
// it belongs to a paired device.
type peerCall struct {
	id     string
	paired bool
	rec    peerRecord
}

type peerHandlerFunc func(w http.ResponseWriter, r *http.Request, call peerCall)

// peerGuard identifies the caller and refuses a key that is not paired on the
// endpoints that need one.
func peerGuard(pairedOnly bool, next peerHandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !peerEnabledFlag.Load() {
			http.Error(w, "nearby devices is off on this device", http.StatusServiceUnavailable)
			return
		}
		id := peerRequestFingerprint(r)
		if id == "" {
			http.Error(w, "a client certificate is required", http.StatusUnauthorized)
			return
		}
		call := peerCall{id: id}
		call.rec, call.paired = peerPairedRecord(id)
		if pairedOnly && !call.paired {
			http.Error(w, "not paired", http.StatusForbidden)
			return
		}
		if call.paired {
			peerNoteCaller(r, call.rec)
		}
		next(w, r, call)
	}
}

// peerRequestFingerprint is the id of the key the caller proved in the
// handshake.
func peerRequestFingerprint(r *http.Request) string {
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	pub, ok := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return peerFingerprint(pub)
}

// peerNoteCaller remembers where a paired caller listens.
func peerNoteCaller(r *http.Request, rec peerRecord) {
	port, err := strconv.Atoi(strings.TrimSpace(r.Header.Get(peerHeaderPort)))
	if err != nil || port <= 0 || port > 65535 {
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return
	}
	peerNoteAddr(rec.ID, net.JoinHostPort(host, strconv.Itoa(port)))
}

// peerWriteJSONResponse answers with a JSON body.
func peerWriteJSONResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// peerDecodeBody reads a bounded JSON POST body.
func peerDecodeBody(w http.ResponseWriter, r *http.Request, limit int64, dst interface{}) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func peerHandleHello(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	self, ok := peerSelf()
	if !ok {
		http.Error(w, "no identity", http.StatusServiceUnavailable)
		return
	}
	peerWriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"v":      1,
		"app":    "JukaHub",
		"id":     self.id,
		"name":   self.name,
		"model":  peerModelName(),
		"paired": call.paired,
	})
}

// peerHandleUnpair is the other device saying it forgot this one: this side
// forgets it too, so neither keeps a one-sided pairing.
func peerHandleUnpair(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	name := call.rec.Name
	peerRemovePaired(call.id)
	tbxPost(func() {
		ShowToast(name+" is no longer paired with this device", ToastKindInfo)
	})
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── Client ────────────────────────────────────────────────────────────────

// peerKeySeen records the key a server presented when none was pinned yet.
type peerKeySeen struct {
	mu sync.Mutex
	id string
}

func (k *peerKeySeen) set(id string) {
	k.mu.Lock()
	k.id = id
	k.mu.Unlock()
}

func (k *peerKeySeen) get() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.id
}

// peerClientTLS presents this device's key and pins the server's: want is the
// id the server must prove, or "" to accept any key and report it in seen.
func peerClientTLS(self peerIdentity, want string, seen *peerKeySeen) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{self.cert},
		MinVersion:   tls.VersionTLS13,
		// There is no CA on a home network: the key pin below replaces the
		// chain check, and the handshake still proves the server holds the key.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the device sent no certificate")
			}
			id, err := peerCertFingerprint(raw[0])
			if err != nil {
				return err
			}
			if want != "" && !peerSameID(id, want) {
				return errPeerWrongKey
			}
			if seen != nil {
				seen.set(id)
			}
			return nil
		},
	}
}

// peerNewClient is an HTTPS client for one device. Requests carry their own
// deadlines through their context.
func peerNewClient(self peerIdentity, want string, seen *peerKeySeen) *http.Client {
	dialer := &net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:     peerClientTLS(self, want, seen),
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConnsPerHost: 2,
	}
	return &http.Client{Transport: tr}
}

// peerClients caches one pinned client per paired device, so its connection
// is kept alive across the requests of a sync.
var peerClients struct {
	mu sync.Mutex
	m  map[string]*http.Client
}

func peerClientFor(id string) (*http.Client, error) {
	self, ok := peerSelf()
	if !ok {
		return nil, errors.New("this device has no identity yet")
	}
	peerClients.mu.Lock()
	defer peerClients.mu.Unlock()
	if c := peerClients.m[id]; c != nil {
		return c, nil
	}
	if peerClients.m == nil {
		peerClients.m = make(map[string]*http.Client)
	}
	c := peerNewClient(self, id, nil)
	peerClients.m[id] = c
	return c, nil
}

// peerClientsReset drops every cached client (the identity changed).
func peerClientsReset() {
	peerClients.mu.Lock()
	m := peerClients.m
	peerClients.m = nil
	peerClients.mu.Unlock()
	for _, c := range m {
		c.CloseIdleConnections()
	}
}

// peerClientDrop drops one device's client (it was unpaired).
func peerClientDrop(id string) {
	peerClients.mu.Lock()
	c := peerClients.m[id]
	delete(peerClients.m, id)
	peerClients.mu.Unlock()
	if c != nil {
		c.CloseIdleConnections()
	}
}

// peerTagRequest names the caller's listening port, so the other device learns
// where to reach this one.
func peerTagRequest(req *http.Request) {
	req.Header.Set("User-Agent", "JukaHub-peer/1")
	if port := peerListenPortNow.Load(); port > 0 {
		req.Header.Set(peerHeaderPort, strconv.Itoa(int(port)))
	}
}

// peerStatusError is a non-200 answer.
type peerStatusError struct {
	code int
	msg  string
}

func (e *peerStatusError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return http.StatusText(e.code)
}

// peerResponseError turns a non-200 answer into an error with its first line.
func peerResponseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &peerStatusError{code: resp.StatusCode, msg: firstLine(strings.TrimSpace(string(data)))}
}

// peerRequest builds a request to a paired device and the pinned client that
// must send it. A device with no recent address is looked for first.
func peerRequest(ctx context.Context, id, method, path string, body io.Reader, size int64) (*http.Request, *http.Client, error) {
	if !peerEnabledFlag.Load() {
		return nil, nil, errPeerOff
	}
	addr := peerAddrFor(id)
	if addr == "" {
		found, err := peerLocate(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		addr = found
	}
	client, err := peerClientFor(id)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, body)
	if err != nil {
		return nil, nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	peerTagRequest(req)
	return req, client, nil
}

// peerSend sends a request built by peerRequest. A failure forgets the address
// (the device may have moved), and "not paired" is remembered for the UI.
func peerSend(id string, client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		peerForgetAddr(id)
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden {
		peerNoteUnpairedThere(id)
	}
	return resp, nil
}

// peerDo is peerRequest and peerSend in one call.
func peerDo(ctx context.Context, id, method, path string, body io.Reader, size int64) (*http.Response, error) {
	req, client, err := peerRequest(ctx, id, method, path, body, size)
	if err != nil {
		return nil, err
	}
	return peerSend(id, client, req)
}

// peerDoJSON sends in (when not nil) as JSON and decodes a 200 answer into out
// (when not nil), reading at most limit bytes.
func peerDoJSON(ctx context.Context, id, method, path string, in, out interface{}, limit int64) error {
	var body io.Reader
	size := int64(-1)
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		size = int64(len(data))
	}
	req, client, err := peerRequest(ctx, id, method, path, body, size)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := peerSend(id, client, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	if limit <= 0 {
		limit = peerMaxJSONBody
	}
	return json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(out)
}

// peerPostJSON posts to a device through a client the caller made (pairing,
// where the device is not paired yet).
func peerPostJSON(ctx context.Context, client *http.Client, addr, path string, in, out interface{}) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	peerTagRequest(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, peerMaxJSONBody)).Decode(out)
}

// peerGetJSON reads a JSON answer from a device through a caller-made client.
func peerGetJSON(ctx context.Context, client *http.Client, addr, path string, out interface{}, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+path, nil)
	if err != nil {
		return err
	}
	peerTagRequest(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(out)
}

// peerUnpairRemote tells a device this one forgot it. Best effort: a device
// that is away finds out the next time it calls.
func peerUnpairRemote(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = peerDoJSON(ctx, id, http.MethodPost, "/peer/v1/unpair", map[string]bool{"bye": true}, nil, 0)
}

// peerErrorText is a failed request as a sentence for a toast.
func peerErrorText(err error, name string) string {
	if err == nil {
		return ""
	}
	if name == "" {
		name = "The other device"
	}
	var se *peerStatusError
	switch {
	case errors.Is(err, errPeerOff):
		return "Nearby devices is off — turn it on in Settings → Network"
	case errors.Is(err, errPeerNoLAN):
		return "Not connected to Wi-Fi"
	case errors.Is(err, errPeerNotFound):
		return name + " is not on this Wi-Fi right now — is JukaHub open there, with Nearby devices on?"
	case errors.Is(err, errPeerWrongKey):
		return name + " answered with a different key — forget it and pair again"
	case errors.Is(err, context.DeadlineExceeded):
		return name + " did not answer in time"
	case errors.Is(err, context.Canceled):
		return "Stopped"
	case errors.As(err, &se):
		switch se.code {
		case http.StatusForbidden:
			return name + " no longer knows this device — pair again"
		case http.StatusServiceUnavailable:
			return "Nearby devices is off on " + name
		case http.StatusConflict:
			if se.msg != "" {
				return name + ": " + se.msg
			}
			return name + " is busy — try again in a moment"
		}
		return name + ": " + se.Error()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return name + " did not answer in time"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "refused") || strings.Contains(lower, "no route") || strings.Contains(lower, "unreachable") {
		return name + " did not answer — is JukaHub open there, with Nearby devices on?"
	}
	return firstLine(err.Error())
}

// ── Discovery ─────────────────────────────────────────────────────────────

// peerBeaconReply is the discovery answer.
type peerBeaconReply struct {
	V     int    `json:"v"`
	App   string `json:"app"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Port  int    `json:"port"`
	// The Nearby fields are only set while "Share what I'm playing" is on.
	Share bool   `json:"share,omitempty"`
	NRev  string `json:"nrev,omitempty"`
	Play  string `json:"play,omitempty"`
	Sys   string `json:"sys,omitempty"`
}

// peerBeacon answers discovery queries.
type peerBeacon struct {
	conn net.PacketConn
	done chan struct{}
}

// peerBeaconListenAddr is a test seam: tests bind 127.0.0.1:0.
var peerBeaconListenAddr string

func peerStartBeacon() (*peerBeacon, error) {
	addr := peerBeaconListenAddr
	if addr == "" {
		addr = ":" + strconv.Itoa(peerDiscoveryPort())
	}
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	b := &peerBeacon{conn: conn, done: make(chan struct{})}
	go b.serve()
	return b, nil
}

func (b *peerBeacon) serve() {
	defer close(b.done)
	buf := make([]byte, 64)
	window := time.Now()
	replies := 0
	for {
		n, from, err := b.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Windows reports an ICMP "port unreachable" from an earlier
			// answer as an error on the next read; it is not fatal.
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if strings.TrimSpace(string(buf[:n])) != peerDiscoveryQuery {
			continue
		}
		if now := time.Now(); now.Sub(window) >= time.Second {
			window, replies = now, 0
		}
		if replies >= peerBeaconRepliesPerSecond {
			continue
		}
		data, ok := peerBeaconPayload()
		if !ok {
			continue
		}
		replies++
		_, _ = b.conn.WriteTo(data, from)
	}
}

// Stop closes the socket and waits for the reader.
func (b *peerBeacon) Stop() {
	if b == nil || b.conn == nil {
		return
	}
	_ = b.conn.Close()
	<-b.done
}

// peerBeaconPayload is this device's answer, or false while it is not
// listening.
func peerBeaconPayload() ([]byte, bool) {
	port := int(peerListenPortNow.Load())
	if port <= 0 || !peerEnabledFlag.Load() {
		return nil, false
	}
	self, ok := peerSelf()
	if !ok {
		return nil, false
	}
	reply := peerBeaconReply{V: 1, App: "JukaHub", ID: self.id, Name: self.name, Model: peerModelName(), Port: port}
	nearbyBeaconFields(&reply)
	data, err := json.Marshal(reply)
	if err != nil {
		return nil, false
	}
	if len(data) > peerReplyMax {
		// Drop the optional fields rather than the answer.
		reply.Play, reply.Sys = "", ""
		if data, err = json.Marshal(reply); err != nil || len(data) > peerReplyMax {
			return nil, false
		}
	}
	return data, true
}

// peerParseBeacon reads one answer. from is the sender; ok is false for
// anything that is not a well-formed JukaHub answer.
func peerParseBeacon(payload []byte, from *net.UDPAddr) (peerSeenDevice, bool) {
	if from == nil || from.IP == nil || len(payload) == 0 || len(payload) > peerReplyMax {
		return peerSeenDevice{}, false
	}
	var info peerBeaconReply
	if err := json.Unmarshal(payload, &info); err != nil {
		return peerSeenDevice{}, false
	}
	id := strings.ToLower(strings.TrimSpace(info.ID))
	if info.V != 1 || info.App != "JukaHub" || !peerValidID(id) || info.Port <= 0 || info.Port > 65535 {
		return peerSeenDevice{}, false
	}
	name := peerCleanName(info.Name)
	if name == "" {
		name = "JukaHub " + peerShortID(id)
	}
	ip := from.IP.String()
	return peerSeenDevice{
		ID:    id,
		Name:  name,
		Model: partyCleanLabel(info.Model, 40),
		Addr:  net.JoinHostPort(ip, strconv.Itoa(info.Port)),
		IP:    ip,
		Seen:  time.Now(),
		Share: info.Share,
		NRev:  partyCleanLabel(info.NRev, 64),
		Play:  partyCleanLabel(info.Play, 120),
		Sys:   partyCleanLabel(info.Sys, 40),
	}, true
}

// peerScanTargetsOverride is a test seam: a scan sends here instead of
// broadcasting.
var peerScanTargetsOverride []string

// peerDiscoverOnce broadcasts one query and collects answers until wait ends
// or ctx is cancelled. This device's own answer is skipped; an answer with
// this device's id from another address is a copied identity.
func peerDiscoverOnce(ctx context.Context, wait time.Duration) ([]peerSeenDevice, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	query := []byte(peerDiscoveryQuery)
	targets := peerScanTargetsOverride
	if len(targets) == 0 {
		targets = partyDiscoveryTargets(peerDiscoveryPort())
	}
	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(query, addr)
	}

	selfID := peerSelfID()
	own := peerOwnIPs()
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	found := map[string]peerSeenDevice{}
	buf := make([]byte, peerReplyMax+64)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		dev, ok := peerParseBeacon(buf[:n], from)
		if !ok {
			continue
		}
		if dev.ID == selfID {
			if !own[dev.IP] {
				peerNoteClone(dev.Addr)
			}
			continue
		}
		found[dev.ID] = dev
	}
	out := make([]peerSeenDevice, 0, len(found))
	for _, d := range found {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, ctx.Err()
}

// peerOwnIPs is every address of this device, for telling its own discovery
// answer from a copied identity's.
func peerOwnIPs() map[string]bool {
	out := map[string]bool{"127.0.0.1": true}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP != nil {
			out[n.IP.String()] = true
		}
	}
	return out
}

// peerScanning keeps background scans from piling up.
var peerScanning atomic.Bool

// peerScanAndMerge runs one scan (unless one is running) and records what it
// heard.
func peerScanAndMerge(ctx context.Context) {
	if !peerEnabledFlag.Load() || partyLANAddress() == "" {
		return
	}
	if !peerScanning.CompareAndSwap(false, true) {
		return
	}
	defer peerScanning.Store(false)
	devs, _ := peerDiscoverOnce(ctx, peerScanWait)
	if ctx.Err() != nil {
		return
	}
	peerMergeSeen(devs, time.Now())
}

// peerLocate scans for one device and returns where it listens.
func peerLocate(ctx context.Context, id string) (string, error) {
	if partyLANAddress() == "" {
		return "", errPeerNoLAN
	}
	devs, err := peerDiscoverOnce(ctx, peerScanWait)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	peerMergeSeen(devs, time.Now())
	for _, d := range devs {
		if d.ID == id {
			return d.Addr, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errPeerNotFound
}
