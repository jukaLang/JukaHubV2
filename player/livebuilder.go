package main

// livebuilder.go — LAN receiver for the browser-based JukaHub GUI builder.
//
// The builder is a static page: it cannot write to the SD card, and copying
// jukaconfig.json across by hand defeats the point of designing on a laptop.
// This server gives the page two endpoints on the handheld:
//
//	GET  /api/health   what device this is (no code required)
//	GET  /api/design   the current design, secrets left out (code required)
//	POST /api/design   replace the design and hot-reload it (code required)
//
// It is plain HTTP, guarded by a six-digit pairing code stored next to the
// config, so it is built to be safe to run rather than trusted (SEC-3):
//
//   - It is off until asked for on the handheld (Settings → Network → Live
//     Builder) and turns itself off after 30 minutes without a paired request.
//     On a PC it is on by default but listens on this computer only (loopback)
//     unless JUKAHUB_LIVE_LAN=1 or the liveBuilderLAN setting opens it to the
//     network.
//   - The code travels in the X-Live-Builder-Code header only. It is shown on
//     the device's own screen and nowhere else: not on the status page, not in
//     a log, not in the notification history. Five wrong codes in a minute
//     lock the sender (an IPv6 sender by its /64) out for five minutes; twenty
//     from anyone pause pairing for a minute and replace the code. A request
//     without a code is not a guess and is not counted.
//   - Browsers may call it only from the builder's origins (the published
//     site, a copy on localhost, JUKAHUB_LIVE_ORIGINS), and only under a host
//     name that is an IP address, localhost, a .local name or the device's own
//     name, so neither another web page nor DNS rebinding can drive it.
//   - A design whose object keys differ only in letter case is refused: Go's
//     decoder would read one spelling and the saved file keep both. A design
//     that can do something the device's design cannot (run a program or a
//     command, a plugin action, bind or set a protected setting, load from a
//     network share, change where tools run or the chat channel) is not
//     applied: it waits in jukaconfig.json.pending until the owner accepts it
//     on the device, and while it waits every other push is refused.
//   - A pull leaves secrets on the device ("<kept on device>"), and a push puts
//     the device's own values back wherever the placeholder comes back. What
//     an answer tells the sender about its design is worked out with the
//     placeholders still in place, so no answer quotes a value kept back.
//
// The pushed bytes are written back as the builder produced them (after
// validation, the placeholder restore and a schemaVersion stamp), not
// re-marshalled through Config: fields this build does not know about must
// survive a push, or opening an older layout in the builder and pushing it
// would silently strip features.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	liveBuilderDefaultPort = 8422
	liveBuilderMaxBody     = 4 << 20 // 4 MiB: a full 33-scene config is ~120 KiB
	liveBuilderCodeFile    = ".livebuilder-code"
	liveBuilderVersion     = "2"
	liveBuilderSettingKey  = "liveBuilder"
	liveBuilderLANKey      = "liveBuilderLAN"
	liveBuilderCodeHeader  = "X-Live-Builder-Code"
	liveBuilderSiteOrigin  = "https://generate.jukalang.com"
	liveBuilderPendingExt  = ".pending"
	liveBuilderKeptValue   = "<kept on device>"
)

// The abuse limits. A pairing code has a million values; these keep guessing
// it far slower than the idle switch-off ends the chance to.
const (
	liveBuilderIdleLimit     = 30 * time.Minute
	liveBuilderFailWindow    = time.Minute
	liveBuilderFailLimit     = 5 // wrong codes per sender per window
	liveBuilderLockout       = 5 * time.Minute
	liveBuilderGlobalLimit   = 20 // wrong codes from every sender together per window
	liveBuilderGlobalLockout = time.Minute
	liveBuilderMaxSenders    = 256
)

// The approval card: how long after it appears a press is ignored (so a card
// that changes under a finger cannot take the tap), and how much of each
// reason it shows. The recovery card drops the body lines that do not fit, so
// a card takes reasons only while their text stays within a width (counted
// by liveBuilderTextWidth; about 85 to a line of the card).
const (
	liveBuilderApprovalGuard = time.Second
	liveBuilderCardReasons   = 2   // reasons on the decision card itself…
	liveBuilderCardWidth     = 300 // …while they fit about four lines
	liveBuilderPageReasons   = 3   // reasons per page of "Everything it can do"…
	liveBuilderPageWidth     = 720 // …while they fit about ten (one always goes)
	liveBuilderReasonRunes   = 200 // longest value shown whole
	liveBuilderReasonHead    = 130 // a longer one keeps its start…
	liveBuilderReasonTail    = 60  // …and its end, where an appended command sits
)

// liveBuilderServer is the HTTP listener plus the last-push metadata the status
// page and health response report.
type liveBuilderServer struct {
	configPath string
	codePath   string // where a replaced code is saved ("" keeps it in memory)
	onReload   func()
	// onPendingChange runs (on the HTTP goroutine) when a design starts or
	// stops waiting for the owner, onIdle when the server switched itself off
	// and onCodeChange when too many wrong codes replaced the code. Any may
	// be nil.
	onPendingChange func()
	onIdle          func()
	onCodeChange    func()
	// now is the clock; tests replace it.
	now func() time.Time

	mu         sync.Mutex
	code       string
	lan        bool // listen on every interface, not on loopback only
	lns        []net.Listener
	srv        *http.Server
	port       int
	idleStop   chan struct{}
	lastActive time.Time
	appName    string
	deviceName string
	lastPush   time.Time
	lastScenes int
	lastElems  int
	pushCount  int
	senders    map[string]*liveBuilderSender
	recentFail []time.Time
	pausedTill time.Time
	pending    *liveBuilderPending
	pendingSeq int
	urlCache   string
	urlAt      time.Time

	// writeMu orders every write of the design and of the waiting copy: a
	// push, an accept and a reject never interleave. codeMu orders the writes
	// of the code file.
	writeMu sync.Mutex
	codeMu  sync.Mutex
}

// liveBuilderSender is one address's recent wrong codes.
type liveBuilderSender struct {
	fails  []time.Time
	until  time.Time // locked out until
	seenAt time.Time
}

// liveBuilderPending is a pushed design held for the owner's decision. Seq
// names it, so a card never applies a newer push the owner has not seen, and
// Sum is the SHA-256 of the bytes that were checked, so the file the card
// applies is the one that was checked. A pending value is never changed after
// it is made; a new one replaces it.
//
// Reasons quote the design as it will be saved, with the device's own values
// back in place of "<kept on device>": they are for the owner's card only.
// PublicReasons are the same capabilities worked out from the design as it
// was sent, placeholders and all, against the device's design as a pull
// shows it: they are all an HTTP answer ever carries, so a push cannot read
// back a value a pull keeps on the device. PublicSum is the SHA-256 of the
// design as it was sent: only a push of those same bytes hears "still
// waiting", or spelling out a guessed secret where the placeholder was would
// tell a right guess (same saved bytes) from a wrong one.
type liveBuilderPending struct {
	Seq           int
	From          string
	At            time.Time
	Reasons       []string
	PublicReasons []string
	Levels        [3]int // how many reasons run something, change a protected setting, reach out
	Scenes        int
	Elements      int
	Sum           [sha256.Size]byte
	PublicSum     [sha256.Size]byte
}

func newLiveBuilderServer(configPath, code string, onReload func()) *liveBuilderServer {
	if strings.TrimSpace(configPath) == "" {
		configPath = "jukaconfig.json"
	}
	return &liveBuilderServer{
		configPath: configPath,
		code:       code,
		onReload:   onReload,
		now:        time.Now,
		senders:    map[string]*liveBuilderSender{},
	}
}

// liveBuilderEnvSetting reads JUKAHUB_LIVE_BUILDER: set reports whether the
// variable decides at all, on what it decides.
func liveBuilderEnvSetting() (on, set bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_LIVE_BUILDER"))) {
	case "0", "false", "off", "no", "disabled":
		return false, true
	case "1", "true", "on", "yes", "enabled":
		return true, true
	}
	return false, false
}

// liveBuilderWanted reports whether the server starts with JukaHub: the
// environment wins, then the Settings switch (and the older
// liveBuilderDisabled key), then the platform default — off on the handheld,
// which would otherwise listen on every network it joins, and on on a PC,
// where it listens on that computer only.
func liveBuilderWanted(c *Config) bool {
	if on, set := liveBuilderEnvSetting(); set {
		return on
	}
	if c != nil {
		if customBool(c.Variables.Custom, "liveBuilderDisabled", false) {
			return false
		}
		if v, ok := customValue(c.Variables.Custom, liveBuilderSettingKey); ok {
			return coerceBool(v)
		}
	}
	return !P().IsHandheld()
}

// liveBuilderLANWanted reports whether the receiver listens on the network
// rather than on this computer only: always on the handheld (the builder runs
// on another machine), on a PC only when JUKAHUB_LIVE_LAN=1 or the
// liveBuilderLAN setting asks for it.
func liveBuilderLANWanted(c *Config) bool {
	if P().IsHandheld() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_LIVE_LAN"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return c != nil && customBool(c.Variables.Custom, liveBuilderLANKey, false)
}

func liveBuilderPort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_LIVE_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return liveBuilderDefaultPort
}

func liveBuilderCodePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), liveBuilderCodeFile)
}

// loadOrCreateLiveBuilderCode reads the pairing code, creating a random one on
// first use. Six digits is short enough to copy from the device; the lockout,
// the replacement after a burst of guesses and the idle switch-off are what
// make it long enough.
func loadOrCreateLiveBuilderCode(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		code := strings.TrimSpace(string(data))
		// A code anyone could read in the repository's history is no code:
		// that file is replaced as if it were missing.
		if len(code) >= 4 && len(code) <= 12 && !liveBuilderLeakedCodes[code] {
			return code, nil
		}
	}
	code, err := randomLiveBuilderCode()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := liveBuilderWriteFile(path, []byte(code+"\n")); err != nil {
		return "", err
	}
	return code, nil
}

// liveBuilderLeakedCodes were published: the tracked .livebuilder-code of
// early builds is in the git history. No install may use one.
var liveBuilderLeakedCodes = map[string]bool{"456059": true}

func randomLiveBuilderCode() (string, error) {
	max := big.NewInt(1000000)
	for {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		if code := fmt.Sprintf("%06d", n.Int64()); !liveBuilderLeakedCodes[code] {
			return code, nil
		}
	}
}

// freshLiveBuilderCode is a random code other than old: a replacement that
// happened to draw the code it replaces would keep a leaked code working.
func freshLiveBuilderCode(old string) (string, error) {
	for {
		code, err := randomLiveBuilderCode()
		if err != nil || code != old {
			return code, err
		}
	}
}

// Code is the pairing code in force.
func (s *liveBuilderServer) Code() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// RotateCode replaces the pairing code with a fresh one (never the one it
// replaces) and saves it, so a code that leaked or was being guessed stops
// working at once. It returns the code in force afterwards: when another
// rotation raced this one, that is the newer code, the one that works. It
// writes a file: call it off the render thread.
func (s *liveBuilderServer) RotateCode() (string, error) {
	s.mu.Lock()
	code, err := freshLiveBuilderCode(s.code)
	if err == nil {
		s.code = code
	}
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	s.persistCode()
	return s.Code(), nil
}

// persistCode saves the code in force (the newest, whichever rotation asked
// first). It writes no .bak: an old code must not linger next to the new one.
func (s *liveBuilderServer) persistCode() {
	if s.codePath == "" {
		return
	}
	s.codeMu.Lock()
	defer s.codeMu.Unlock()
	if err := liveBuilderWriteFile(s.codePath, []byte(s.Code()+"\n")); err != nil {
		logWarn("livebuilder", "could not save the new pairing code: %v", err)
	}
}

// Start listens on the builder port (or any free one when it is taken) and
// arms the idle switch-off. Starting a running server does nothing.
func (s *liveBuilderServer) Start() error {
	s.mu.Lock()
	running, lan := s.srv != nil, s.lan
	s.mu.Unlock()
	if running {
		return nil
	}
	lns, err := liveBuilderListen(lan, liveBuilderPort())
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	stop := make(chan struct{})
	s.mu.Lock()
	s.lns, s.srv = lns, srv
	s.port = lns[0].Addr().(*net.TCPAddr).Port
	s.idleStop = stop
	s.lastActive = s.now()
	s.urlCache = ""
	s.mu.Unlock()
	for _, ln := range lns {
		go func(ln net.Listener) {
			// A listener closed by detach ends Serve with net.ErrClosed;
			// that is a switch-off, not a failure.
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				logWarn("livebuilder", "server stopped: %v", err)
			}
		}(ln)
	}
	go s.watchIdle(stop)
	return nil
}

// liveBuilderListen opens the listeners: every interface on the handheld (or
// when a PC asks for the network), loopback only otherwise — 127.0.0.1 and,
// when the computer has it, [::1] on the same port. A taken port falls back to
// any free one; the Settings row and the toasts carry the real port.
func liveBuilderListen(lan bool, port int) ([]net.Listener, error) {
	if lan {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			// A second instance, or another app on the default port.
			ln, err = net.Listen("tcp", ":0")
			if err != nil {
				return nil, err
			}
		}
		return []net.Listener{ln}, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
	}
	lns := []net.Listener{ln}
	got := ln.Addr().(*net.TCPAddr).Port
	if ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(got))); err == nil {
		lns = append(lns, ln6)
	}
	return lns, nil
}

// detach takes the running server out of the struct and closes its listeners
// at once, so a quick off and on gets the same port back; what is left to
// finish is the wait for requests in flight.
func (s *liveBuilderServer) detach() *http.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detachLocked()
}

func (s *liveBuilderServer) detachLocked() *http.Server {
	srv, stop, lns := s.srv, s.idleStop, s.lns
	s.srv, s.lns, s.idleStop = nil, nil, nil
	if stop != nil {
		close(stop)
	}
	for _, ln := range lns {
		_ = ln.Close()
	}
	return srv
}

// Stop closes the listeners and waits (up to two seconds) for requests in
// flight.
func (s *liveBuilderServer) Stop() {
	liveBuilderShutdown(s.detach())
}

// StopSoon is Stop for the render thread: the server is off at once, and the
// wait for requests in flight happens in the background.
func (s *liveBuilderServer) StopSoon() {
	if srv := s.detach(); srv != nil {
		go liveBuilderShutdown(srv)
	}
}

func liveBuilderShutdown(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (s *liveBuilderServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

func (s *liveBuilderServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// LANURL is what the user types into the builder: the device's network
// address, or localhost when the receiver listens on this computer only.
// Looking up the address walks the network interfaces, so the answer is kept
// for a few seconds: the Settings row asks again and again.
func (s *liveBuilderServer) LANURL() string {
	now := s.now()
	s.mu.Lock()
	if s.urlCache != "" && now.Sub(s.urlAt) < 5*time.Second {
		u := s.urlCache
		s.mu.Unlock()
		return u
	}
	port, lan := s.port, s.lan
	s.mu.Unlock()
	host := "localhost"
	if lan {
		host = localIPv4()
		if host == "" {
			host = "127.0.0.1"
		}
	}
	u := fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
	s.mu.Lock()
	s.urlCache, s.urlAt = u, now
	s.mu.Unlock()
	return u
}

// ── Idle switch-off ─────────────────────────────────────────────────────────

// watchIdle switches the server off after liveBuilderIdleLimit without a
// paired request. The durations come from the monotonic clock, so a clock set
// over the network neither ends nor extends a session.
func (s *liveBuilderServer) watchIdle(stop chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			srv := s.detachIfIdle(stop, s.now())
			if srv == nil {
				continue
			}
			logInfo("livebuilder", "turned off after %s without a paired request", liveBuilderIdleLimit)
			liveBuilderShutdown(srv)
			if s.onIdle != nil {
				s.onIdle()
			}
			return
		}
	}
}

// detachIfIdle detaches the server when it is still the one stop belongs to
// and has gone unused for the limit, in one step: a server switched off and
// on again in between is never stopped by the old watcher.
func (s *liveBuilderServer) detachIfIdle(stop chan struct{}, now time.Time) *http.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil || s.idleStop != stop || now.Sub(s.lastActive) < liveBuilderIdleLimit {
		return nil
	}
	return s.detachLocked()
}

// idleExpired reports whether a running server has gone unused for the limit.
func (s *liveBuilderServer) idleExpired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil && now.Sub(s.lastActive) >= liveBuilderIdleLimit
}

// ── HTTP ────────────────────────────────────────────────────────────────────

type liveBuilderHealth struct {
	OK            bool   `json:"ok"`
	Service       string `json:"service"`
	Version       string `json:"version"`
	App           string `json:"app,omitempty"`
	Device        string `json:"device,omitempty"`
	Scenes        int    `json:"scenes"`
	Elements      int    `json:"elements"`
	CodeRequired  bool   `json:"codeRequired"`
	SchemaVersion int    `json:"schemaVersion"`
	LastPush      string `json:"lastPush,omitempty"`
	Pushes        int    `json:"pushes"`
	BuilderURL    string `json:"builderUrl,omitempty"`
}

type liveBuilderResponse struct {
	OK            bool     `json:"ok"`
	Quarantined   bool     `json:"quarantined,omitempty"`
	Scenes        int      `json:"scenes,omitempty"`
	Elements      int      `json:"elements,omitempty"`
	Bytes         int      `json:"bytes,omitempty"`
	Reloaded      bool     `json:"reloaded,omitempty"`
	SavedAt       string   `json:"savedAt,omitempty"`
	SchemaVersion int      `json:"schemaVersion,omitempty"`
	Message       string   `json:"message,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	Error         string   `json:"error,omitempty"`
	Details       []string `json:"details,omitempty"`
}

func (s *liveBuilderServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.guard(s.handleStatus))
	mux.HandleFunc("/api/health", s.guard(s.handleHealth))
	mux.HandleFunc("/api/design", s.guard(s.handleDesign))
	return mux
}

// guard is every request's front door: the CORS answer for the builder's
// origins (first, so the builder can read any refusal), the origin check and
// the host-name check against DNS rebinding. The pairing code is a header
// rather than a cookie, so no browser holds an ambient credential, and only
// the builder's origins may read an answer.
func (s *liveBuilderServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		h.Set("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !liveBuilderOriginAllowed(origin) {
				writeLiveBuilderJSON(w, http.StatusForbidden, liveBuilderResponse{
					Error: "only the JukaHub web builder may talk to this device",
				})
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, "+liveBuilderCodeHeader)
			h.Set("Access-Control-Max-Age", "600")
			// Chrome's local-network checks ask before a page reaches a LAN
			// address; the allowed origins get a yes.
			if strings.EqualFold(r.Header.Get("Access-Control-Request-Private-Network"), "true") {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
		}
		if !liveBuilderHostAllowed(r.Host) {
			writeLiveBuilderJSON(w, http.StatusMisdirectedRequest, liveBuilderResponse{
				Error: "open the device by its address (like 192.168.1.50:8422), not by a host name",
			})
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// liveBuilderHostAllowed accepts the host names a real client uses: an IP
// address, localhost, a .local mDNS name or this device's own host name. A
// web page that points a name it owns at this device (DNS rebinding) arrives
// under that name and is refused.
func liveBuilderHostAllowed(hostport string) bool {
	host := strings.TrimSpace(hostport)
	if host == "" {
		return true // HTTP/1.0 without Host: no browser sends that
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // an IPv6 zone
	}
	if net.ParseIP(host) != nil {
		return true
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") {
		return true
	}
	if name, err := os.Hostname(); err == nil && name != "" && strings.EqualFold(lower, name) {
		return true
	}
	return false
}

// liveBuilderOriginAllowed is the CORS allowlist: the published builder, a copy
// served from this computer on any port, and the origins JUKAHUB_LIVE_ORIGINS
// lists (comma-separated) for a self-hosted builder.
func liveBuilderOriginAllowed(origin string) bool {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if origin == "" || strings.EqualFold(origin, "null") {
		return false
	}
	if strings.EqualFold(origin, liveBuilderSiteOrigin) {
		return true
	}
	for _, extra := range strings.Split(os.Getenv("JUKAHUB_LIVE_ORIGINS"), ",") {
		if e := strings.TrimSuffix(strings.TrimSpace(extra), "/"); e != "" && strings.EqualFold(origin, e) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// liveBuilderClient is who a request counts against: the sender's address
// without the port, and an IPv6 sender's whole /64, which one machine can
// rotate through freely.
func liveBuilderClient(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.Trim(host, "[]")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return host
	case ip.To4() != nil:
		return ip.To4().String()
	default:
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
}

// codeMatchesLocked compares a code with the one in force in constant time.
// s.mu is held.
func (s *liveBuilderServer) codeMatchesLocked(got string) bool {
	return got != "" && s.code != "" && len(got) == len(s.code) &&
		subtle.ConstantTimeCompare([]byte(got), []byte(s.code)) == 1
}

// checkCode compares a pairing code with the lockout in front of it, in one
// critical section, so parallel requests cannot all slip past a check that has
// not yet seen each other's failures. wait > 0: locked out, nothing compared.
// A request without a code is not a guess and is not counted. rotated is true
// when this failure paused pairing for everyone and replaced the code.
func (s *liveBuilderServer) checkCode(client, got string, now time.Time) (wait time.Duration, ok, rotated bool) {
	got = strings.TrimSpace(got)
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.pausedTill) {
		wait = s.pausedTill.Sub(now)
	}
	if snd := s.senders[client]; snd != nil && now.Before(snd.until) {
		if d := snd.until.Sub(now); d > wait {
			wait = d
		}
	}
	if wait > 0 || got == "" {
		return wait, false, false
	}
	if s.codeMatchesLocked(got) {
		delete(s.senders, client)
		s.lastActive = now
		return 0, true, false
	}
	return 0, false, s.noteFailureLocked(client, now)
}

// authorize checks the pairing code with the lockout in front of it. It writes
// the refusal itself and reports whether the request may go on.
func (s *liveBuilderServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	wait, ok, rotated := s.checkCode(liveBuilderClient(r), r.Header.Get(liveBuilderCodeHeader), s.now())
	if rotated {
		s.persistCode()
		if s.onCodeChange != nil {
			s.onCodeChange()
		}
	}
	switch {
	case wait > 0:
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		writeLiveBuilderJSON(w, http.StatusTooManyRequests, liveBuilderResponse{
			Error: "too many wrong pairing codes — try again in " + liveBuilderWaitText(wait),
		})
	case !ok:
		writeLiveBuilderJSON(w, http.StatusUnauthorized, liveBuilderResponse{
			Error: "pairing code is missing or wrong",
		})
	}
	return ok
}

// noteFailureLocked records a wrong code and starts a lockout at the limits.
// When everyone's failures pause pairing, the code is replaced as well: a
// guess that was getting close starts again from nothing. s.mu is held.
func (s *liveBuilderServer) noteFailureLocked(client string, now time.Time) (rotated bool) {
	snd := s.senders[client]
	if snd == nil {
		if len(s.senders) >= liveBuilderMaxSenders {
			s.pruneSendersLocked(now)
		}
		snd = &liveBuilderSender{}
		s.senders[client] = snd
	}
	snd.seenAt = now
	snd.fails = append(liveBuilderRecent(snd.fails, now), now)
	if len(snd.fails) >= liveBuilderFailLimit {
		snd.until = now.Add(liveBuilderLockout)
		snd.fails = snd.fails[:0]
		logWarn("livebuilder", "%d wrong pairing codes from %s: locked out for %s", liveBuilderFailLimit, client, liveBuilderLockout)
	}
	s.recentFail = append(liveBuilderRecent(s.recentFail, now), now)
	if len(s.recentFail) < liveBuilderGlobalLimit {
		return false
	}
	s.pausedTill = now.Add(liveBuilderGlobalLockout)
	s.recentFail = s.recentFail[:0]
	if code, err := freshLiveBuilderCode(s.code); err == nil {
		s.code = code
		rotated = true
	}
	logWarn("livebuilder", "%d wrong pairing codes within a minute: pairing paused for %s and the code replaced", liveBuilderGlobalLimit, liveBuilderGlobalLockout)
	return rotated
}

// touchIfPaired keeps the server on for a builder that knows the code. The
// health answer is the same either way, so this is no way to test codes, and
// a wrong code here is not counted.
func (s *liveBuilderServer) touchIfPaired(client, got string, now time.Time) {
	got = strings.TrimSpace(got)
	if got == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.pausedTill) {
		return
	}
	if snd := s.senders[client]; snd != nil && now.Before(snd.until) {
		return
	}
	if s.codeMatchesLocked(got) {
		s.lastActive = now
	}
}

// liveBuilderRecent drops the times older than the failure window. It filters
// in place: callers assign the result back.
func liveBuilderRecent(times []time.Time, now time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if now.Sub(t) < liveBuilderFailWindow {
			kept = append(kept, t)
		}
	}
	return kept
}

// pruneSendersLocked keeps the sender table bounded: senders with nothing
// recent and no lockout go first, then the one seen longest ago.
func (s *liveBuilderServer) pruneSendersLocked(now time.Time) {
	oldestKey, oldest := "", time.Time{}
	for k, snd := range s.senders {
		recent := 0
		for _, t := range snd.fails {
			if now.Sub(t) < liveBuilderFailWindow {
				recent++
			}
		}
		if recent == 0 && !now.Before(snd.until) {
			delete(s.senders, k)
			continue
		}
		if oldestKey == "" || snd.seenAt.Before(oldest) {
			oldestKey, oldest = k, snd.seenAt
		}
	}
	if len(s.senders) >= liveBuilderMaxSenders && oldestKey != "" {
		delete(s.senders, oldestKey)
	}
}

// liveBuilderWaitText says a lockout's length the way a person would.
func liveBuilderWaitText(d time.Duration) string {
	if d > time.Minute {
		m := int((d + time.Minute - 1) / time.Minute)
		return strconv.Itoa(m) + " minutes"
	}
	secs := int((d + time.Second - 1) / time.Second)
	if secs <= 1 {
		return "a second"
	}
	return strconv.Itoa(secs) + " seconds"
}

func (s *liveBuilderServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeLiveBuilderJSON(w, http.StatusMethodNotAllowed, liveBuilderResponse{Error: "use GET"})
		return
	}
	s.touchIfPaired(liveBuilderClient(r), r.Header.Get(liveBuilderCodeHeader), s.now())
	builderURL := s.LANURL()
	s.mu.Lock()
	lastPush := ""
	if !s.lastPush.IsZero() {
		lastPush = s.lastPush.Format(time.RFC3339)
	}
	app := s.appName
	if app == "" {
		app = "JukaHub"
	}
	health := liveBuilderHealth{
		OK:            true,
		Service:       "jukahub-live-builder",
		Version:       liveBuilderVersion,
		App:           app,
		Device:        s.deviceName,
		Scenes:        s.lastScenes,
		Elements:      s.lastElems,
		CodeRequired:  true,
		SchemaVersion: ConfigSchemaVersion,
		LastPush:      lastPush,
		Pushes:        s.pushCount,
		BuilderURL:    builderURL,
	}
	s.mu.Unlock()
	writeLiveBuilderJSON(w, http.StatusOK, health)
}

func (s *liveBuilderServer) handleDesign(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPost:
	default:
		writeLiveBuilderJSON(w, http.StatusMethodNotAllowed, liveBuilderResponse{Error: "use GET or POST"})
		return
	}
	if !s.authorize(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		s.handleDesignPull(w)
		return
	}
	s.handleDesignPush(w, r)
}

// handleDesignPull sends the design with its secrets left on the device: the
// values of protected settings, anything stored encrypted ("ENC:") and the
// chat token read "<kept on device>", which a push turns back into the
// device's own values.
func (s *liveBuilderServer) handleDesignPull(w http.ResponseWriter) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "cannot read the device config: " + err.Error()})
		return
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "the device config is not valid JSON: " + err.Error()})
		return
	}
	if liveBuilderRedact(doc) {
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "could not prepare the design"})
			return
		}
		data = out
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *liveBuilderServer) handleDesignPush(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		writeLiveBuilderJSON(w, http.StatusUnsupportedMediaType, liveBuilderResponse{Error: "send the design as application/json"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, liveBuilderMaxBody+1))
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusBadRequest, liveBuilderResponse{Error: "could not read the request body"})
		return
	}
	if len(body) > liveBuilderMaxBody {
		writeLiveBuilderJSON(w, http.StatusRequestEntityTooLarge, liveBuilderResponse{Error: "design is larger than 4 MiB"})
		return
	}

	// The browser's own field set is what gets saved (fields this build does
	// not know about must survive), so the checks below run on exactly the
	// bytes that will be written.
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		details := []string(nil)
		if err != nil {
			details = []string{err.Error()}
		}
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{Error: "this is not a JukaHub config", Details: details})
		return
	}
	// Two spellings of one field ("trigger" and "Trigger") would show one
	// design to these checks and another to the player, which reads either.
	if clash := liveBuilderCaseClash(doc, ""); clash != "" {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "the design has fields whose names differ only in letter case",
			Details: []string{clash},
		})
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// A pulled design carries "<kept on device>" where a secret was: put the
	// device's own values back, so pulling and pushing never wipes them. What
	// the sender is told is worked out from a copy that keeps the
	// placeholders (and drops the same fields), so no answer ever quotes a
	// value the pull left on the device.
	currentData, _ := os.ReadFile(s.configPath)
	currentDoc := liveBuilderParseDoc(currentData)
	publicDoc := liveBuilderCopyDoc(doc)
	liveBuilderRestoreKept(publicDoc, currentDoc, true)
	liveBuilderRestoreKept(doc, currentDoc, false)

	restored, err := json.Marshal(doc)
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{Error: "could not read the design"})
		return
	}
	validator := NewConfigValidator()
	if _, status, resp := liveBuilderCheckDesign(restored, validator); status != 0 {
		writeLiveBuilderJSON(w, status, resp)
		return
	}
	for k := range doc {
		if strings.EqualFold(k, "schemaVersion") {
			delete(doc, k)
		}
	}
	doc["schemaVersion"] = ConfigSchemaVersion
	stamped, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{Error: "could not serialise the design"})
		return
	}
	// Everything from here on concerns the design exactly as it will be saved
	// and read back by the player.
	cfg, status, resp := liveBuilderCheckDesign(stamped, validator)
	if status != 0 {
		writeLiveBuilderJSON(w, status, resp)
		return
	}
	sum := sha256.Sum256(stamped)
	sent, err := json.Marshal(publicDoc)
	if err != nil {
		writeLiveBuilderJSON(w, http.StatusUnprocessableEntity, liveBuilderResponse{Error: "could not read the design"})
		return
	}
	publicSum := sha256.Sum256(sent)

	// While a design waits for the owner, nothing else lands: a second push
	// could otherwise swap what the card shows just as it is pressed. The
	// same design sent again (auto-push) is simply still waiting.
	s.mu.Lock()
	waiting := s.pending
	s.mu.Unlock()
	if waiting != nil {
		if waiting.Sum == sum && waiting.PublicSum == publicSum {
			writeLiveBuilderJSON(w, http.StatusAccepted, liveBuilderHeldResponse(waiting, len(stamped)))
			return
		}
		writeLiveBuilderJSON(w, http.StatusConflict, liveBuilderResponse{
			Error: "a design is already waiting for approval on the device — accept or reject it there first",
		})
		return
	}

	// A design that can do something the device's design cannot waits for
	// the owner instead. The decision is made on the design as it will be
	// saved; the sender hears about the design it sent, compared with the
	// device's design as a pull shows it.
	publicCfg := liveBuilderDecodeDesign(publicDoc)
	if risks := liveBuilderDesignRisks(cfg, liveBuilderParseDesign(currentData)); len(risks) > 0 {
		reasons, levels := liveBuilderRiskTexts(risks)
		publicReasons, _ := liveBuilderRiskTexts(liveBuilderDesignRisks(publicCfg, liveBuilderShownDesign(currentData)))
		s.holdForOwner(w, r, stamped, liveBuilderPending{
			Reasons:       reasons,
			PublicReasons: publicReasons,
			Levels:        levels,
			Scenes:        len(cfg.Scenes),
			Elements:      countLiveBuilderElements(cfg),
			Sum:           sum,
			PublicSum:     publicSum,
		})
		return
	}

	if err := backupLiveBuilderConfig(s.configPath); err != nil {
		// A missing/unreadable old file is fine on first push; a failed backup
		// with a readable old file is not, because a push would be unrecoverable.
		if !os.IsNotExist(err) {
			writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "could not back up the current config: " + err.Error()})
			return
		}
	}
	if err := AtomicWrite(s.configPath, stamped, 0o644); err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "could not save the design: " + err.Error()})
		return
	}

	// Make the change live now instead of on the watcher's next two-second poll.
	// The callback records the new mtime first, so the poller does not reload a
	// second time and double the "Config reloaded" toast.
	if s.onReload != nil {
		s.onReload()
	}

	warnings := liveBuilderPublicWarnings(validator, publicCfg)
	now := s.now()
	s.mu.Lock()
	s.lastPush = now
	s.lastScenes = len(cfg.Scenes)
	s.lastElems = countLiveBuilderElements(cfg)
	s.pushCount++
	scenes, elems := s.lastScenes, s.lastElems
	s.mu.Unlock()

	writeLiveBuilderJSON(w, http.StatusOK, liveBuilderResponse{
		OK:            true,
		Scenes:        scenes,
		Elements:      elems,
		Bytes:         len(stamped),
		Reloaded:      s.onReload != nil,
		SavedAt:       now.Format(time.RFC3339),
		SchemaVersion: ConfigSchemaVersion,
		Warnings:      warnings,
	})
	logDebug("livebuilder", "design pushed: %d scenes, %d elements, %d bytes", scenes, elems, len(stamped))
}

// liveBuilderCheckDesign reads design bytes the way the player loads a config
// and validates them. status is 0 when the design passes; otherwise it is the
// HTTP status and resp the answer.
func liveBuilderCheckDesign(data []byte, validator *ConfigValidator) (*Config, int, liveBuilderResponse) {
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "this is not a JukaHub config",
			Details: []string{err.Error()},
		}
	}
	if _, err := MigrateConfig(cfg); err != nil {
		return nil, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "config schema is not supported",
			Details: []string{err.Error()},
		}
	}
	if err := validator.Validate(cfg); err != nil {
		return nil, http.StatusUnprocessableEntity, liveBuilderResponse{
			Error:   "the design did not pass the player's validation",
			Details: []string{err.Error()},
		}
	}
	return cfg, 0, liveBuilderResponse{}
}

func countLiveBuilderElements(cfg *Config) int {
	total := 0
	for i := range cfg.Scenes {
		total += len(cfg.Scenes[i].Elements)
	}
	return total
}

// backupLiveBuilderConfig keeps the previous design as .bak, the same file the
// config health path already knows how to restore from. AtomicWrite makes that
// copy too, but only as a best effort; here a backup that fails stops the
// push. It is written in place, as AtomicWrite writes its own: through
// AtomicWrite it would leave a .bak.bak of the backup before.
func backupLiveBuilderConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return writeFileSynced(path+".bak", data, 0o644)
}

func writeLiveBuilderJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *liveBuilderServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, s.statusPageHTML())
}

// statusPageHTML is the page the builder's "Open device page" shows. Anyone
// who can reach the port can load it, so it carries nothing private: not the
// pairing code, and not whether a design waits for approval.
func (s *liveBuilderServer) statusPageHTML() string {
	lanURL := s.LANURL()
	s.mu.Lock()
	lastPush := "never"
	if !s.lastPush.IsZero() {
		lastPush = s.lastPush.Format("15:04:05")
	}
	pushes := s.pushCount
	scenes, elems := s.lastScenes, s.lastElems
	s.mu.Unlock()

	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>JukaHub Live Builder</title>
<style>
:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(1200px 600px at 20% -10%,#1b2340,#080a10 60%);color:#eef2ff;font:16px/1.55 Inter,system-ui,sans-serif}
main{width:min(680px,92vw);padding:32px;border:1px solid #2b3550;border-radius:20px;background:#111624cc;box-shadow:0 30px 80px -30px #000}
h1{margin:0 0 4px;font-size:1.6rem}
.sub{color:#9aa7c7;margin:0 0 24px}
.row{display:flex;justify-content:space-between;gap:16px;padding:10px 0;border-bottom:1px solid #232c42}
.row:last-of-type{border-bottom:0}
.k{color:#9aa7c7}
.hint{color:#8d99b8;font-size:.88rem}
.ok{color:#3ddc97}
strong{font-weight:600}
</style></head><body><main>
<h1>JukaHub <span style="color:#8b7bff">Live Builder</span></h1>
<p class="sub">This device accepts designs from the browser builder.</p>
<div class="row"><span class="k">Address for the builder</span><strong>` + html.EscapeString(lanURL) + `</strong></div>
<div class="row"><span class="k">Server</span><span class="ok">running</span></div>
<div class="row"><span class="k">Last push</span><span>` + html.EscapeString(lastPush) + ` · ` + strconv.Itoa(pushes) + ` total</span></div>
<div class="row"><span class="k">Last design</span><span>` + strconv.Itoa(scenes) + ` scenes · ` + strconv.Itoa(elems) + ` elements</span></div>
<p class="hint">The pairing code is shown on the device only, in <strong>Settings → Network → Live Builder</strong>. It is never shown on this page.</p>
<p class="hint">In the builder open <strong>Device</strong>, paste the address above, enter the pairing code, then press <strong>Push to device</strong>. The device validates the design, saves it atomically and reloads it in place. A design that can run programs or change protected settings waits for approval on the device.</p>
</main></body></html>`
}

// liveBuilderAppName / liveBuilderDeviceName are cheap metadata for the health
// response, read on the main thread when the server starts (the HTTP
// goroutines never touch appConfig). They tolerate the early-startup nil
// config.
func liveBuilderAppName() string {
	if appConfig != nil && strings.TrimSpace(appConfig.AppName) != "" {
		return appConfig.AppName
	}
	return "JukaHub"
}

func liveBuilderDeviceName() string {
	return deviceDisplayName(DetectDevice())
}

// ── JSON helpers: case clashes, secrets left on the device ─────────────────

// liveBuilderFoldKey is a name as the player compares it: strings.EqualFold
// and Go's JSON decoder treat the long s "ſ" as "s" and the Kelvin sign as
// "k", and so must every check here.
func liveBuilderFoldKey(name string) string {
	return strings.ToLower(strings.ToUpper(strings.TrimSpace(name)))
}

// liveBuilderCaseClash finds an object with two keys that differ only in case
// (at any depth) and names the first one, "" when there is none.
func liveBuilderCaseClash(v interface{}, path string) string {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		seen := make(map[string]string, len(keys))
		for _, k := range keys {
			fold := liveBuilderFoldKey(k)
			if other, ok := seen[fold]; ok {
				return path + "." + other + " and " + path + "." + k
			}
			seen[fold] = k
		}
		for _, k := range keys {
			if clash := liveBuilderCaseClash(t[k], path+"."+k); clash != "" {
				return clash
			}
		}
	case []interface{}:
		for i, e := range t {
			if clash := liveBuilderCaseClash(e, path+"["+strconv.Itoa(i)+"]"); clash != "" {
				return clash
			}
		}
	}
	return ""
}

// liveBuilderParseDoc is a design as plain JSON values (nil when the bytes are
// not a JSON object).
func liveBuilderParseDoc(data []byte) map[string]interface{} {
	var doc map[string]interface{}
	if len(data) == 0 || json.Unmarshal(data, &doc) != nil {
		return nil
	}
	return doc
}

// liveBuilderCopyDoc is a deep copy of a design, sharing nothing with it.
func liveBuilderCopyDoc(doc map[string]interface{}) map[string]interface{} {
	data, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return liveBuilderParseDoc(data)
}

// liveBuilderHasValue reports a value worth keeping secret: anything but null,
// an empty string, an empty list or an empty object.
func liveBuilderHasValue(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case []interface{}:
		return len(t) > 0
	case map[string]interface{}:
		return len(t) > 0
	}
	return true
}

// liveBuilderRedact replaces, in a pulled design, the values of protected
// settings under "variables" and "channel_profile" and every encrypted
// ("ENC:") string with "<kept on device>". It reports whether anything was
// replaced.
func liveBuilderRedact(doc map[string]interface{}) bool {
	changed := false
	var walk func(v interface{}, protectedScope bool) interface{}
	walk = func(v interface{}, protectedScope bool) interface{} {
		switch t := v.(type) {
		case map[string]interface{}:
			for k, child := range t {
				if protectedScope && liveBuilderSensitiveKey(k) && liveBuilderHasValue(child) {
					t[k] = liveBuilderKeptValue
					changed = true
					continue
				}
				t[k] = walk(child, protectedScope)
			}
		case []interface{}:
			for i := range t {
				t[i] = walk(t[i], protectedScope)
			}
		case string:
			if strings.HasPrefix(strings.TrimSpace(t), "ENC:") {
				changed = true
				return liveBuilderKeptValue
			}
		}
		return v
	}
	for k, v := range doc {
		scope := strings.EqualFold(k, "variables") || strings.EqualFold(k, "channel_profile")
		doc[k] = walk(v, scope)
	}
	return changed
}

// liveBuilderLookup finds a key in an object, exactly or else by the player's
// case-insensitive rule.
func liveBuilderLookup(m map[string]interface{}, key string) (interface{}, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m[key]; ok {
		return v, true
	}
	fold := liveBuilderFoldKey(key)
	for k, v := range m {
		if liveBuilderFoldKey(k) == fold {
			return v, true
		}
	}
	return nil, false
}

// liveBuilderRestoreKept replaces every "<kept on device>" in a pushed design
// with the device's value at the same place, or drops the field when the
// device has none: the placeholder can only ever stand for the device's own
// value, never for something new. With mark set it drops the same fields
// and blanks the same list items but leaves the placeholder where the device
// has a value: the design as its sender may be told about it. Either way the
// device's values are only ever read (with mark unset, the design shares
// them).
func liveBuilderRestoreKept(next, current interface{}, mark bool) interface{} {
	switch t := next.(type) {
	case map[string]interface{}:
		cur, _ := current.(map[string]interface{})
		for k, child := range t {
			old, found := liveBuilderLookup(cur, k)
			if s, ok := child.(string); ok && s == liveBuilderKeptValue {
				switch {
				case !found:
					delete(t, k)
				case !mark:
					t[k] = old
				}
				continue
			}
			t[k] = liveBuilderRestoreKept(child, old, mark)
		}
	case []interface{}:
		cur, _ := current.([]interface{})
		for i := range t {
			var old interface{}
			if i < len(cur) {
				old = cur[i]
			}
			if s, ok := t[i].(string); ok && s == liveBuilderKeptValue {
				switch {
				case old == nil:
					t[i] = ""
				case !mark:
					t[i] = old
				}
				continue
			}
			t[i] = liveBuilderRestoreKept(t[i], old, mark)
		}
	}
	return next
}

// ── Designs that wait for the owner ─────────────────────────────────────────

// liveBuilderShellTriggers run the command a design variable holds as a shell
// command line: whoever writes the variable decides what the press runs.
var liveBuilderShellTriggers = map[string]string{
	"terminal_run": "terminal_cmd",
	"emulator_run": "emulator_cmd",
}

// liveBuilderServerTriggers start a file server on the network from a press.
var liveBuilderServerTriggers = map[string]string{
	"ftp_start":  "FTP",
	"ftp_toggle": "FTP",
	"ssh_start":  "SFTP",
	"ssh_toggle": "SFTP",
}

// liveBuilderProtectedWords and liveBuilderProtectedPhrases mark a setting a
// pushed design must not bind or change unseen: commands, credentials and
// cookies, the chat account, where tools are run from, how far the file
// servers reach, start-up behaviour and this receiver itself. Words match a
// whole word of the name; phrases match the name with its separators removed.
// Every name the settings export treats as a secret (cfgdocIsSecretKey) is
// protected as well.
var liveBuilderProtectedWords = map[string]bool{
	"cmd": true, "cmds": true, "command": true, "commands": true, "exec": true,
	"script": true, "scripts": true, "shell": true, "token": true, "tokens": true,
	"password": true, "passwords": true, "passwd": true, "pass": true, "passphrase": true,
	"secret": true, "secrets": true, "credential": true, "credentials": true,
	"private": true, "auth": true, "bearer": true,
	"cookie": true, "cookies": true, "crypto": true, "sideload": true, "trust": true,
	"telemetry": true, "discord": true,
}

var liveBuilderProtectedPhrases = []string{"apikey", "fullroot", "toolspath", "livebuilder", "autostart"}

// liveBuilderProtectedNames say where a stored credential is sent, or whether
// the host it goes to is checked: the Home Assistant, MQTT and Pi-hole tokens
// and passwords and the router probe (smart_home.go). Changing one hands the
// credential to another host. They are compared as lower-case words joined by
// "_", so HA_URL and haUrl count too.
var liveBuilderProtectedNames = map[string]bool{
	"ha_url": true, "ha_insecure": true, "mqtt_host": true, "mqtt_port": true,
	"pihole_url": true, "router_host": true,
}

// liveBuilderSensitiveKey reports whether a variable name is protected. A name
// with any non-ASCII letter is protected outright: the player's lookups fold
// look-alikes ("toolſ_path" finds tools_path), so no word list can be trusted
// with them.
func liveBuilderSensitiveKey(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, r := range name {
		if r > unicode.MaxASCII {
			return true
		}
	}
	if cfgdocIsSecretKey(name) {
		return true
	}
	words := liveBuilderKeyWords(name)
	for _, w := range words {
		if liveBuilderProtectedWords[w] {
			return true
		}
	}
	if liveBuilderProtectedNames[strings.Join(words, "_")] {
		return true
	}
	joined := strings.Join(words, "")
	for _, p := range liveBuilderProtectedPhrases {
		if strings.Contains(joined, p) {
			return true
		}
	}
	return false
}

// liveBuilderKeyWords splits an ASCII name into lower-case words at
// separators, camelCase humps, the end of an acronym ("APIKey": api, key) and
// between letters and digits.
func liveBuilderKeyWords(name string) []string {
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isLower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var words []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !isUpper(c) && !isLower(c) && !isDigit(c) {
			flush()
			continue
		}
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			switch {
			case isDigit(c) != isDigit(prev):
				flush()
			case isUpper(c) && isLower(prev):
				flush()
			case isUpper(c) && isUpper(prev) && i+1 < len(name) && isLower(name[i+1]):
				flush()
			}
		}
		cur = append(cur, c)
	}
	flush()
	return words
}

// liveBuilderReadDesign is the design on the device, or nil when there is none
// that parses (then everything a push can do counts as new).
func liveBuilderReadDesign(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return liveBuilderParseDesign(data)
}

// liveBuilderParseDesign reads design bytes as the player does, nil when they
// do not parse.
func liveBuilderParseDesign(data []byte) *Config {
	if len(data) == 0 {
		return nil
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil
	}
	return cfg
}

// liveBuilderDecodeDesign reads a design that may hold "<kept on device>"
// where a value of another type belongs, so it reads as much as it can: such
// a field keeps its zero value instead of failing the whole design. It is for
// telling the sender what a design can do, never for applying one.
func liveBuilderDecodeDesign(doc map[string]interface{}) *Config {
	if doc == nil {
		return nil
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	cfg := &Config{}
	_ = json.Unmarshal(data, cfg)
	return cfg
}

// liveBuilderShownDesign is the device's design as a pull shows it, secrets
// replaced by the placeholder: what a sender's design is compared with when
// the sender is told what it can do. Nil when there is none that parses.
func liveBuilderShownDesign(data []byte) *Config {
	doc := liveBuilderParseDoc(data)
	if doc == nil {
		return nil
	}
	liveBuilderRedact(doc)
	return liveBuilderDecodeDesign(doc)
}

// liveBuilderPublicWarnings is the validator's advice about the design as its
// sender wrote it. A value kept on the device is left out: whether the device
// stores it encrypted, or what it is, is not the sender's to learn. public is
// a throwaway decode and is changed.
func liveBuilderPublicWarnings(validator *ConfigValidator, public *Config) []string {
	if public == nil {
		return nil
	}
	custom := make(map[string]interface{}, len(public.Variables.Custom))
	for k, v := range public.Variables.Custom {
		if s, ok := v.(string); ok && s == liveBuilderKeptValue {
			continue
		}
		custom[k] = v
	}
	public.Variables.Custom = custom
	return validator.SecurityCheck(public)
}

// The risk levels, in the order the approval card lists them.
const (
	liveBuilderRunsSomething = iota // a program, a command, a plugin action
	liveBuilderProtectedSetting
	liveBuilderReachesOut // a network share, the chat channel
)

// liveBuilderRisk is one capability of a design, keyed so the same capability
// in the device's current design can be recognised.
type liveBuilderRisk struct {
	key   string
	text  string
	level int
}

// liveBuilderDesignRisks lists what a pushed design could do that the design
// on the device cannot already do, most serious first. Something the device's
// design already does is not asked about again, so re-pushing an approved
// design (auto-push) goes straight through.
func liveBuilderDesignRisks(next, current *Config) []liveBuilderRisk {
	if next == nil {
		return nil
	}
	known := map[string]bool{}
	if current != nil {
		for _, r := range liveBuilderCapabilities(current) {
			known[r.key] = true
		}
	}
	var out []liveBuilderRisk
	seen := map[string]bool{}
	for _, r := range liveBuilderCapabilities(next) {
		if known[r.key] || seen[r.key] {
			continue
		}
		seen[r.key] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].level < out[j].level })
	return out
}

// liveBuilderCapabilities is every program, command, protected setting and
// outside reach a design has.
func liveBuilderCapabilities(cfg *Config) []liveBuilderRisk {
	var out []liveBuilderRisk
	add := func(level int, key, text string) {
		out = append(out, liveBuilderRisk{key: key, text: text, level: level})
	}
	custom := cfg.Variables.Custom
	for _, scene := range cfg.Scenes {
		for _, e := range scene.Elements {
			name := liveBuilderElementName(e, scene.Name)
			trigger := strings.TrimSpace(e.Trigger)
			target, value := strings.TrimSpace(e.TriggerTarget), strings.TrimSpace(e.TriggerValue)
			if trigger == "external_app" || strings.TrimSpace(e.ExternalAppPath) != "" {
				cmd := strings.TrimSpace(e.ExternalAppPath)
				add(liveBuilderRunsSomething, "external_app\x00"+cmd,
					name+" runs the program “"+liveBuilderShow(cmd, liveBuilderReasonRunes)+"”")
			}
			if variable, ok := liveBuilderShellTriggers[trigger]; ok {
				preset := strings.TrimSpace(customStringValue(custom, variable))
				text := name + " runs whatever " + variable + " holds as a shell command"
				if preset != "" {
					text = name + " runs the shell command “" + liveBuilderShow(preset, liveBuilderReasonRunes) + "”"
				}
				add(liveBuilderRunsSomething, trigger+"\x00"+preset, text)
			}
			// yt_search hands its target to yt-dlp as the command line, and its
			// value names the variable the results land in.
			if trigger == "yt_search" && (target != "" || value != "") {
				cmd := target
				if cmd == "" {
					cmd = value
				}
				add(liveBuilderRunsSomething, "yt_search\x00"+target+"\x00"+value,
					name+" runs the yt-dlp command “"+liveBuilderShow(cmd, liveBuilderReasonRunes)+"”")
				if liveBuilderSensitiveKey(value) {
					add(liveBuilderProtectedSetting, "bind\x00result\x00"+liveBuilderFoldKey(value),
						name+" writes search results into the protected setting "+liveBuilderShow(value, 60))
				}
			}
			if server, ok := liveBuilderServerTriggers[trigger]; ok {
				add(liveBuilderReachesOut, "server\x00"+server,
					name+" starts the "+server+" file server on the network")
			}
			if trigger == "plugin_action" {
				add(liveBuilderRunsSomething, "plugin\x00"+target+"\x00"+value,
					name+" runs the plugin action “"+liveBuilderShow(strings.TrimSpace(target+" "+value), liveBuilderReasonRunes)+"”")
			}
			if trigger == "set_variable" && liveBuilderSensitiveKey(e.VariableChange) {
				add(liveBuilderProtectedSetting, "set\x00"+liveBuilderFoldKey(e.VariableChange)+"\x00"+e.TriggerValue,
					name+" sets the protected setting "+liveBuilderShow(e.VariableChange, 60)+" to “"+liveBuilderShow(e.TriggerValue, liveBuilderReasonRunes)+"”")
			}
			// A text field, a switch or a text browser bound to a protected
			// setting writes it (or shows it); textbrowser_json publishes into
			// the scene's text browser, which is checked as the element it is.
			bound := []struct{ field, name string }{
				{"variable", e.Variable},
				{"list", e.ListVariable},
				{"media", e.MediaVariable},
				{"video", e.VideoVariable},
			}
			for _, b := range bound {
				if b.field == "variable" && trigger == "textbrowser_json" {
					continue // there it holds the JSON or its URL, not a name
				}
				if liveBuilderSensitiveKey(b.name) {
					add(liveBuilderProtectedSetting, "bind\x00"+b.field+"\x00"+liveBuilderFoldKey(b.name),
						name+" is bound to the protected setting "+liveBuilderShow(b.name, 60))
				}
			}
		}
	}
	keys := make([]string, 0, len(custom))
	for k := range custom {
		if liveBuilderSensitiveKey(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		value := fmt.Sprint(custom[k])
		add(liveBuilderProtectedSetting, "custom\x00"+liveBuilderFoldKey(k)+"\x00"+value,
			"Sets the protected setting "+liveBuilderShow(k, 60)+" to “"+liveBuilderShow(value, liveBuilderReasonRunes)+"”")
	}
	if tp := strings.TrimSpace(cfg.Variables.ToolsPath); tp != "" {
		add(liveBuilderRunsSomething, "tools_path\x00"+tp,
			"Runs JukaHub's tools from “"+liveBuilderShow(tp, liveBuilderReasonRunes)+"”")
	}
	if root := strings.TrimSpace(cfg.Variables.FileExplorerRoot); root != "" {
		add(liveBuilderProtectedSetting, "files_root\x00"+root,
			"Opens the file browser at “"+liveBuilderShow(root, liveBuilderReasonRunes)+"”")
	}
	if cp := cfg.ChannelProfile; strings.TrimSpace(cp.ChannelID) != "" || strings.TrimSpace(cp.Token) != "" {
		add(liveBuilderReachesOut, "channel\x00"+cp.ChannelID+"\x00"+cp.Token,
			"Sets the chat channel to “"+liveBuilderShow(cp.ChannelID, 60)+"” with its own account token")
	}
	// Any path that reaches another computer: on Windows, opening a share
	// sends the user's login hash to that host, with no press at all when it is
	// a background or an image.
	var shares []string
	liveBuilderEachString(reflect.ValueOf(cfg), func(s string) {
		if liveBuilderNetworkPath(s) {
			shares = append(shares, strings.TrimSpace(s))
		}
	})
	sort.Strings(shares)
	for _, s := range shares {
		add(liveBuilderReachesOut, "share\x00"+s,
			"Loads “"+liveBuilderShow(s, liveBuilderReasonRunes)+"” from another computer")
	}
	return out
}

// liveBuilderEachString calls fn with every string a value holds: struct
// fields, list items, map values and what interfaces carry.
func liveBuilderEachString(v reflect.Value, fn func(string)) {
	switch v.Kind() {
	case reflect.String:
		fn(v.String())
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			liveBuilderEachString(v.Elem(), fn)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).IsExported() {
				liveBuilderEachString(v.Field(i), fn)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			liveBuilderEachString(v.Index(i), fn)
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			liveBuilderEachString(it.Value(), fn)
		}
	}
}

// liveBuilderNetworkPath reports a path that reaches another computer: a
// Windows share (\\host\share, //host/share and their mixes, \\?\UNC\…), an
// NT object path (\??\UNC\host\share; a local \??\C:\ one is asked about
// too) or a file: URL.
func liveBuilderNetworkPath(s string) bool {
	s = strings.TrimSpace(s)
	sep := func(c byte) bool { return c == '\\' || c == '/' }
	if len(s) >= 2 && sep(s[0]) && sep(s[1]) {
		return true
	}
	if len(s) >= 4 && sep(s[0]) && s[1] == '?' && s[2] == '?' && sep(s[3]) {
		return true
	}
	return len(s) >= 5 && strings.EqualFold(s[:5], "file:")
}

// liveBuilderElementName names an element for the approval card.
func liveBuilderElementName(e Element, scene string) string {
	label := strings.TrimSpace(e.Text)
	if label == "" {
		label = strings.TrimSpace(e.Type)
	}
	if label == "" {
		label = "an element"
	}
	name := "“" + liveBuilderShow(label, 40) + "”"
	if scene = strings.TrimSpace(scene); scene != "" {
		name += " on " + liveBuilderShow(scene, 40)
	}
	return name
}

// liveBuilderShow prepares a value for the approval card: control, format and
// bidirectional characters become visible "[U+202E]" marks (so nothing can
// hide in a command or turn its order around), runs of spaces collapse, and a
// long value keeps its start and its end, where an appended command would sit.
func liveBuilderShow(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			fmt.Fprintf(&b, "[U+%04X]", r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	shown := strings.Join(strings.Fields(b.String()), " ")
	runes := []rune(shown)
	if len(runes) <= limit {
		return shown
	}
	if limit >= liveBuilderReasonRunes {
		return string(runes[:liveBuilderReasonHead]) + " … " + string(runes[len(runes)-liveBuilderReasonTail:])
	}
	return string(runes[:limit-1]) + "…"
}

// liveBuilderRiskTexts turns risks into the reasons the builder and the card
// show, with the count per level.
func liveBuilderRiskTexts(risks []liveBuilderRisk) ([]string, [3]int) {
	texts := make([]string, 0, len(risks))
	var levels [3]int
	for _, r := range risks {
		texts = append(texts, r.text)
		if r.level >= 0 && r.level < len(levels) {
			levels[r.level]++
		}
	}
	return texts, levels
}

// pendingPath is where a held design waits: next to the config, never read as
// one.
func (s *liveBuilderServer) pendingPath() string {
	return s.configPath + liveBuilderPendingExt
}

// liveBuilderHeldResponse is the 202 answer for a design that waits. It
// carries the public reasons only: the owner's card quotes values a pull
// keeps on the device.
func liveBuilderHeldResponse(p *liveBuilderPending, size int) liveBuilderResponse {
	return liveBuilderResponse{
		OK:          true,
		Quarantined: true,
		Scenes:      p.Scenes,
		Elements:    p.Elements,
		Bytes:       size,
		Message:     "This design can run programs or change protected settings, so it waits for approval on the device.",
		Details:     append([]string(nil), p.PublicReasons...),
	}
}

// holdForOwner keeps a design that can run programs or change protected
// settings out of the live config until the owner accepts it on the device.
// held carries everything but who sent it, when and its number. writeMu is
// held.
func (s *liveBuilderServer) holdForOwner(w http.ResponseWriter, r *http.Request, stamped []byte, held liveBuilderPending) {
	if err := liveBuilderWriteFile(s.pendingPath(), stamped); err != nil {
		writeLiveBuilderJSON(w, http.StatusInternalServerError, liveBuilderResponse{Error: "could not keep the design for approval: " + err.Error()})
		return
	}
	from, now := liveBuilderClient(r), s.now()
	s.mu.Lock()
	s.pendingSeq++
	p := &held
	p.Seq, p.From, p.At = s.pendingSeq, from, now
	s.pending = p
	s.mu.Unlock()
	logInfo("livebuilder", "a design from %s waits for approval on the device (%d reasons)", from, len(p.Reasons))
	if s.onPendingChange != nil {
		s.onPendingChange()
	}
	writeLiveBuilderJSON(w, http.StatusAccepted, liveBuilderHeldResponse(p, len(stamped)))
}

// liveBuilderWriteFile saves a file through a temporary file and a rename, so a
// power cut never leaves half of it, with no .bak beside it. 0600: the waiting
// design and the pairing code are not for other users.
func liveBuilderWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// dropPendingLocked deletes the held design. writeMu is held.
func (s *liveBuilderServer) dropPendingLocked(why string) {
	s.mu.Lock()
	had := s.pending != nil
	s.pending = nil
	s.mu.Unlock()
	if err := os.Remove(s.pendingPath()); err != nil && !os.IsNotExist(err) {
		logWarn("livebuilder", "could not remove the waiting design: %v", err)
	}
	if had {
		logInfo("livebuilder", "the waiting design was dropped: %s", why)
		if s.onPendingChange != nil {
			s.onPendingChange()
		}
	}
}

// Pending returns the design waiting for the owner, if any (a copy the caller
// may keep).
func (s *liveBuilderServer) Pending() (liveBuilderPending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return liveBuilderPending{}, false
	}
	p := *s.pending
	p.Reasons = append([]string(nil), p.Reasons...)
	p.PublicReasons = append([]string(nil), p.PublicReasons...)
	return p, true
}

// PendingSummary is what the Settings row needs about the waiting design,
// without copying its reasons.
func (s *liveBuilderServer) PendingSummary() (from string, reasons int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return "", 0, false
	}
	return s.pending.From, len(s.pending.Reasons), true
}

// AcceptPending applies the held design the owner approved. seq is the design
// the card showed: a newer push is never applied unseen, and the file must
// still be the bytes that were checked.
func (s *liveBuilderServer) AcceptPending(seq int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	p := s.pending
	s.mu.Unlock()
	if p == nil {
		return errors.New("no design is waiting")
	}
	if p.Seq != seq {
		return errors.New("a newer design arrived; review that one first")
	}
	data, err := os.ReadFile(s.pendingPath())
	if err != nil {
		return fmt.Errorf("the waiting design is gone: %w", err)
	}
	if sha256.Sum256(data) != p.Sum {
		s.dropPendingLocked("its file changed after it was checked")
		return errors.New("the waiting design changed after it was checked, so it was deleted")
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("the waiting design is damaged: %w", err)
	}
	if err := backupLiveBuilderConfig(s.configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not back up the current design: %w", err)
	}
	if err := AtomicWrite(s.configPath, data, 0o644); err != nil {
		return fmt.Errorf("could not save the design: %w", err)
	}
	if err := os.Remove(s.pendingPath()); err != nil && !os.IsNotExist(err) {
		logWarn("livebuilder", "could not remove the applied waiting design: %v", err)
	}
	now := s.now()
	s.mu.Lock()
	s.pending = nil
	s.lastPush = now
	s.lastScenes = len(cfg.Scenes)
	s.lastElems = countLiveBuilderElements(cfg)
	s.pushCount++
	s.mu.Unlock()
	if s.onReload != nil {
		s.onReload()
	}
	logInfo("livebuilder", "the waiting design was accepted on the device: %d scenes", len(cfg.Scenes))
	return nil
}

// RejectPending deletes the held design the owner turned down. It reports
// false when the card's design was no longer the one waiting.
func (s *liveBuilderServer) RejectPending(seq int) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	p := s.pending
	s.mu.Unlock()
	if p == nil || p.Seq != seq {
		return false
	}
	s.dropPendingLocked("rejected on the device")
	return true
}

// ── The session's server and its Settings switch ────────────────────────────

// liveBuilderState is the one server of this session. main, the Settings
// switch and the approval card reach it through the helpers below.
var liveBuilderState struct {
	mu  sync.Mutex
	srv *liveBuilderServer
}

// liveBuilderCurrent is the session's server (nil before its first start).
func liveBuilderCurrent() *liveBuilderServer {
	liveBuilderState.mu.Lock()
	defer liveBuilderState.mu.Unlock()
	return liveBuilderState.srv
}

// liveBuilderRunning reports whether the server listens now.
func liveBuilderRunning() bool {
	srv := liveBuilderCurrent()
	return srv != nil && srv.Running()
}

// liveBuilderEnsure returns the session's server, creating it (and the pairing
// code) on first use. A design still waiting from an earlier run is deleted:
// nothing is ever applied without a decision made in this session.
func liveBuilderEnsure() (*liveBuilderServer, error) {
	liveBuilderState.mu.Lock()
	defer liveBuilderState.mu.Unlock()
	if liveBuilderState.srv != nil {
		return liveBuilderState.srv, nil
	}
	codePath := liveBuilderCodePath("jukaconfig.json")
	code, err := loadOrCreateLiveBuilderCode(codePath)
	if err != nil {
		return nil, err
	}
	srv := newLiveBuilderServer("jukaconfig.json", code, func() {
		if globalConfigWatcher != nil {
			globalConfigWatcher.NotifyWritten("jukaconfig.json", XMLSidecarPath("jukaconfig.json"))
		}
	})
	srv.codePath = codePath
	srv.onPendingChange = func() { tbxPost(liveBuilderPendingChanged) }
	srv.onIdle = func() {
		tbxPost(func() {
			ShowToast("Live Builder turned off after 30 minutes without use", ToastKindInfo)
		})
	}
	srv.onCodeChange = func() {
		tbxPost(func() {
			ShowToast("Live Builder: too many wrong codes, so a new pairing code was made (Settings → Network)", ToastKindWarning)
		})
	}
	if err := os.Remove(srv.pendingPath()); err == nil {
		logInfo("livebuilder", "deleted a design that was still waiting for approval from the last run")
	}
	liveBuilderState.srv = srv
	return srv, nil
}

// liveBuilderStart starts the session's server unless it already listens. It
// runs on the main thread, which owns appConfig: what the HTTP goroutines need
// from it is copied here.
func liveBuilderStart() (*liveBuilderServer, error) {
	srv, err := liveBuilderEnsure()
	if err != nil {
		return nil, err
	}
	if srv.Running() {
		return srv, nil
	}
	lan, app, device := liveBuilderLANWanted(appConfig), liveBuilderAppName(), liveBuilderDeviceName()
	srv.mu.Lock()
	srv.lan, srv.appName, srv.deviceName = lan, app, device
	srv.mu.Unlock()
	if err := srv.Start(); err != nil {
		return nil, err
	}
	logInfo("livebuilder", "listening on %s", srv.LANURL())
	return srv, nil
}

// liveBuilderAddress is the address without its scheme, as the builder's
// field takes it.
func liveBuilderAddress(srv *liveBuilderServer) string {
	return strings.TrimPrefix(srv.LANURL(), "http://")
}

// startLiveBuilderIfEnabled is called once from main after the config watcher
// exists, so a push can request an immediate reload. Its toast says where the
// code is, not what it is: a screen being shared or streamed at start-up
// should not show it.
func startLiveBuilderIfEnabled() {
	if !liveBuilderWanted(appConfig) {
		logDebug("livebuilder", "off until switched on in Settings → Network → Live Builder")
		return
	}
	if !safeModeAllows(SafeFeatureNetwork) {
		logInfo("livebuilder", "not started: safe mode keeps the network off")
		return
	}
	srv, err := liveBuilderStart()
	if err != nil {
		logWarn("livebuilder", "could not start: %v", err)
		return
	}
	showToastPrimitive("Live Builder on: "+liveBuilderAddress(srv)+" · pairing code in Settings → Network", ToastKindInfo)
}

// stopLiveBuilder stops the server on the way out (main defers it).
func stopLiveBuilder() {
	if srv := liveBuilderCurrent(); srv != nil {
		srv.Stop()
	}
}

// liveBuilderSettingOn is the Settings switch: on while the server listens,
// so a switch-off after 30 idle minutes shows.
func liveBuilderSettingOn(*Config) bool { return liveBuilderRunning() }

// liveBuilderSettingSet is the Settings switch's press. The choice is kept for
// the next launch; starting and stopping happen now. Turning it on shows the
// code: the owner just asked for it.
func liveBuilderSettingSet(c *Config, on bool) {
	setStoreBool(c, liveBuilderSettingKey, on)
	if !on {
		if srv := liveBuilderCurrent(); srv != nil {
			srv.StopSoon()
		}
		ShowToast("Live Builder off — the web builder cannot reach this device", ToastKindInfo)
		return
	}
	if !safeModeAllows(SafeFeatureNetwork) {
		ShowToast("Live Builder stays off: safe mode keeps the network off for this launch", ToastKindWarning)
		return
	}
	srv, err := liveBuilderStart()
	if err != nil {
		logWarn("livebuilder", "could not start: %v", err)
		ShowToast("Live Builder could not start: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	// Not filed in the notification history, and never logged.
	showToastPrimitive("Live Builder on: "+liveBuilderAddress(srv)+" · pairing code "+srv.Code(), ToastKindSuccess)
}

// liveBuilderSettingDetail is the line under the switch: where to point the
// builder and the code to type, on this screen and nowhere else.
func liveBuilderSettingDetail(*Config) string {
	if srv := liveBuilderCurrent(); srv != nil && srv.Running() {
		reach := " · turns off after 30 minutes unused"
		srv.mu.Lock()
		lan := srv.lan
		srv.mu.Unlock()
		if !lan {
			reach = " · this computer only · turns off after 30 minutes unused"
		}
		return "Builder address " + liveBuilderAddress(srv) + " · pairing code " + srv.Code() + reach
	}
	if !safeModeAllows(SafeFeatureNetwork) {
		return "Off: safe mode keeps the network off"
	}
	return "Push designs from the web builder over Wi-Fi. Off until you turn it on"
}

// liveBuilderNewCode is the "New pairing code" row: the old code stops working
// at once, and the builder needs the new one.
func liveBuilderNewCode(*Config) {
	srv := liveBuilderCurrent()
	if srv == nil {
		return
	}
	go func() {
		code, err := srv.RotateCode()
		tbxPost(func() {
			if err != nil {
				ShowToast("A new pairing code could not be made: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			showToastPrimitive("New pairing code "+code+" · the old one no longer works", ToastKindSuccess)
		})
	}()
}

// ── The approval card ───────────────────────────────────────────────────────

// liveBuilderApproval is the design the open approval card shows. Main thread
// only.
var liveBuilderApproval struct {
	seq     int       // 0: no card
	gen     int       // recoveryCard.gen when it was last shown
	shownAt time.Time // presses within liveBuilderApprovalGuard of this are ignored
}

// liveBuilderPendingNow is the design waiting for the owner, if any.
func liveBuilderPendingNow() (liveBuilderPending, bool) {
	srv := liveBuilderCurrent()
	if srv == nil {
		return liveBuilderPending{}, false
	}
	return srv.Pending()
}

// liveBuilderApprovalShown reports whether one of the approval cards is on
// screen.
func liveBuilderApprovalShown() bool {
	return liveBuilderApproval.seq != 0 && recoveryCard.open && recoveryCard.gen == liveBuilderApproval.gen
}

// liveBuilderApprovalShow puts a card up and starts the press guard.
func liveBuilderApprovalShow(seq int, spec recoveryCardSpec) {
	recoveryCardShow(spec)
	liveBuilderApproval.seq, liveBuilderApproval.gen = seq, recoveryCard.gen
	liveBuilderApproval.shownAt = time.Now()
}

// liveBuilderGuarded ignores a press that comes within liveBuilderApprovalGuard
// of the card appearing or changing: a card swapped under a finger must not
// take the press meant for the one before.
func liveBuilderGuarded(fn func()) func(*Config) {
	return func(*Config) {
		if time.Since(liveBuilderApproval.shownAt) < liveBuilderApprovalGuard {
			return
		}
		fn()
	}
}

// liveBuilderPendingChanged runs on the main loop when a design starts or
// stops waiting: it opens the approval card, or closes one whose design is
// gone.
func liveBuilderPendingChanged() {
	p, ok := liveBuilderPendingNow()
	if !ok {
		if liveBuilderApprovalShown() {
			recoveryCardClose()
		}
		liveBuilderApproval.seq = 0
		return
	}
	if liveBuilderApprovalShown() && liveBuilderApproval.seq == p.Seq {
		return
	}
	liveBuilderShowApproval(appConfig)
}

// liveBuilderSummary says in one line what kinds of things the design can do.
func liveBuilderSummary(levels [3]int) string {
	var parts []string
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return strconv.Itoa(n) + " " + many
	}
	if n := levels[liveBuilderRunsSomething]; n > 0 {
		parts = append(parts, "run "+plural(n, "a program or command", "programs or commands"))
	}
	if n := levels[liveBuilderProtectedSetting]; n > 0 {
		parts = append(parts, "change "+plural(n, "a protected setting", "protected settings"))
	}
	if n := levels[liveBuilderReachesOut]; n > 0 {
		parts = append(parts, "reach "+plural(n, "another computer or account", "other computers or accounts"))
	}
	if len(parts) == 0 {
		return "It can do things the current design cannot."
	}
	return "It can " + strings.Join(parts, ", ") + "."
}

// liveBuilderShowApproval asks the owner about the waiting design: keep the
// current design (focused, the safe answer), apply this one, see everything it
// can do, or decide later (Settings → Network → Live Builder brings the card
// back). It never replaces another card that is up, such as a crash report.
func liveBuilderShowApproval(*Config) {
	p, ok := liveBuilderPendingNow()
	if !ok {
		ShowToast("No design is waiting for approval", ToastKindInfo)
		return
	}
	if recoveryCard.open && !liveBuilderApprovalShown() {
		ShowToast("A design from the web builder waits for your approval: Settings → Network → Live Builder", ToastKindWarning)
		return
	}
	seq := p.Seq
	// The advice comes before the reasons: the card drops the lines that do
	// not fit from the end, and "Everything it can do" is always there to
	// show every reason whole.
	var body strings.Builder
	body.WriteString("The design pushed from " + p.From + " can do things the current design cannot. " + liveBuilderSummary(p.Levels) +
		" Apply it only if you made it yourself. Until you choose, the current design stays.\n")
	shown := p.Reasons[:liveBuilderFitReasons(p.Reasons, liveBuilderCardReasons, liveBuilderCardWidth)]
	for _, reason := range shown {
		body.WriteString("\n• " + reason)
	}
	switch extra := len(p.Reasons) - len(shown); {
	case extra > 0 && len(shown) == 0:
		body.WriteString("\n• " + liveBuilderCount(extra, "thing", "things") + " to check: see Everything it can do")
	case extra > 0:
		body.WriteString("\n• and " + strconv.Itoa(extra) + " more: see Everything it can do")
	}
	options := []recoveryOption{
		{icon: "close", title: "Keep the current design", detail: "The pushed design is deleted",
			run: liveBuilderGuarded(func() { liveBuilderDecide(seq, false) })},
		{icon: "check", title: "Apply this design", detail: "Backs up the current design, then applies this one",
			run: liveBuilderGuarded(func() { liveBuilderDecide(seq, true) })},
	}
	if len(p.Reasons) > 0 {
		pages := len(liveBuilderReasonPages(p.Reasons))
		options = append(options, recoveryOption{icon: "info", title: "Everything it can do",
			detail: liveBuilderCount(len(p.Reasons), "thing", "things") + " to check, " + liveBuilderCount(pages, "page", "pages"),
			run:    func(*Config) { liveBuilderShowReasons(seq, 0) }})
	}
	liveBuilderApprovalShow(seq, recoveryCardSpec{
		title:    "Apply a design that runs programs?",
		subtitle: fmt.Sprintf("From %s · %d scenes · %d elements", p.From, p.Scenes, p.Elements),
		icon:     "warning",
		body:     body.String(),
		options:  options,
		back: liveBuilderGuarded(func() {
			recoveryCardClose()
			liveBuilderApproval.seq = 0
		}),
		backText: "Decide later",
	})
}

// liveBuilderFitReasons is how many of the reasons, from the first, one card
// shows whole: at most most, and only while their text stays within width,
// so the card never has to drop a line. It may be none.
func liveBuilderFitReasons(reasons []string, most, width int) int {
	n, total := 0, 0
	for _, r := range reasons {
		total += liveBuilderTextWidth(r)
		if n >= most || total > width {
			break
		}
		n++
	}
	return n
}

// liveBuilderTextWidth is a text's length in narrow letters: a rune from
// U+1100 up counts as two (CJK, Hangul, fullwidth forms and emoji are drawn
// about twice as wide; the narrow ones up there only make a card hold less).
func liveBuilderTextWidth(s string) int {
	w := 0
	for _, r := range s {
		w++
		if r >= 0x1100 {
			w++
		}
	}
	return w
}

// liveBuilderReasonPages is where each page of "Everything it can do" starts.
// A page holds at least one reason: each quotes at most a few hundred runes
// (liveBuilderShow bounds every value in it), which a page shows whole.
func liveBuilderReasonPages(reasons []string) []int {
	var starts []int
	for i := 0; i < len(reasons); {
		starts = append(starts, i)
		n := liveBuilderFitReasons(reasons[i:], liveBuilderPageReasons, liveBuilderPageWidth)
		if n < 1 {
			n = 1
		}
		i += n
	}
	return starts
}

// liveBuilderCount is "1 thing" or "3 things".
func liveBuilderCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// liveBuilderShowReasons pages through every reason, as many to a page as fit
// whole, so none falls off a card that cannot scroll. B or "Back" returns to
// the decision.
func liveBuilderShowReasons(seq, page int) {
	p, ok := liveBuilderPendingNow()
	if !ok || p.Seq != seq || len(p.Reasons) == 0 {
		liveBuilderPendingChanged()
		return
	}
	starts := liveBuilderReasonPages(p.Reasons)
	pages := len(starts)
	if page >= pages {
		page = pages - 1
	}
	if page < 0 {
		page = 0
	}
	start, end := starts[page], len(p.Reasons)
	if page+1 < pages {
		end = starts[page+1]
	}
	var body strings.Builder
	for i, reason := range p.Reasons[start:end] {
		if i > 0 {
			body.WriteString("\n")
		}
		body.WriteString("• " + reason)
	}
	back := func(*Config) { liveBuilderShowApproval(appConfig) }
	var options []recoveryOption
	if page+1 < pages {
		next := page + 1
		options = append(options, recoveryOption{icon: "skip-forward", title: "Next page",
			detail: fmt.Sprintf("Page %d of %d", next+1, pages),
			run:    func(*Config) { liveBuilderShowReasons(seq, next) }})
	}
	options = append(options, recoveryOption{icon: "back", title: "Back to the decision",
		detail: "Keep the current design or apply this one", run: back})
	liveBuilderApprovalShow(seq, recoveryCardSpec{
		title:    "Everything this design can do",
		subtitle: fmt.Sprintf("Page %d of %d · from %s", page+1, pages, p.From),
		icon:     "info",
		body:     body.String(),
		options:  options,
		back:     back,
		backText: "Back",
	})
}

// liveBuilderDecide carries out the owner's choice off the render thread.
func liveBuilderDecide(seq int, accept bool) {
	recoveryCardClose()
	liveBuilderApproval.seq = 0
	srv := liveBuilderCurrent()
	if srv == nil {
		return
	}
	if !accept {
		go func() {
			if srv.RejectPending(seq) {
				tbxPost(func() {
					ShowToast("Kept the current design · the pushed one was deleted", ToastKindInfo)
				})
			}
		}()
		return
	}
	go func() {
		err := srv.AcceptPending(seq)
		tbxPost(func() {
			if err != nil {
				ShowToast("The design was not applied: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			ShowToast("Design applied · the previous one is kept as jukaconfig.json.bak", ToastKindSuccess)
		})
	}()
}

// liveBuilderPendingDetail is the Settings row under the switch while a design
// waits.
func liveBuilderPendingDetail(*Config) string {
	srv := liveBuilderCurrent()
	if srv == nil {
		return ""
	}
	from, n, ok := srv.PendingSummary()
	if !ok {
		return ""
	}
	what := "1 thing to check"
	if n != 1 {
		what = strconv.Itoa(n) + " things to check"
	}
	return "From " + from + " · " + what + " · press to review"
}

// liveBuilderPendingVisible shows the review row only while a design waits.
func liveBuilderPendingVisible(*Config) bool {
	srv := liveBuilderCurrent()
	if srv == nil {
		return false
	}
	_, _, ok := srv.PendingSummary()
	return ok
}
