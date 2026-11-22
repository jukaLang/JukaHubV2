package main

// companion.go — the Phone Companion: a PIN-guarded web app on the LAN that
// turns a phone browser into a keyboard, a remote and a file drop for the
// handheld.
//
// This file is deliberately free of SDL so it can be unit-tested headless.
// HTTP handlers never touch UI state: they append to companionQueue and the
// main loop drains it (see drainCompanionQueue in companionui.go), so a phone
// keystroke travels the same route as a real one.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	companionDefaultPort = 8317
	// companionMaxUpload bounds a streamed browser transfer while allowing
	// disc images and other games larger than half a gigabyte.
	companionMaxUpload = 4 << 30
	// companionTextLimit bounds one "send text" request, not the phone's
	// whole textarea: the page batches long text into several requests.
	companionTextLimit = 4000
	// A wrong PIN is cheap to guess if nothing throttles it. After this many
	// failures in the window the API answers 429 instead of comparing again.
	companionFailLimit  = 20
	companionFailWindow = 30 * time.Second
	companionDropDir    = "PhoneDrop"
)

// ── Queued actions ──

const (
	companionEventText   = "text"
	companionEventKey    = "key"
	companionEventURL    = "url"
	companionEventUpload = "upload"
)

// companionEvent is one action waiting for the main loop.
type companionEvent struct {
	Kind string
	Text string
}

// companionQueueSize caps the backlog. A page that retries in a loop cannot
// grow the slice without bound; the oldest actions are dropped first because
// stale keystrokes are worse than missing ones.
const companionQueueSize = 256

var companionQueue struct {
	mu    sync.Mutex
	items []companionEvent
}

func enqueueCompanion(ev companionEvent) {
	companionQueue.mu.Lock()
	defer companionQueue.mu.Unlock()
	if len(companionQueue.items) >= companionQueueSize {
		companionQueue.items = append([]companionEvent(nil), companionQueue.items[1:]...)
	}
	companionQueue.items = append(companionQueue.items, ev)
}

func drainCompanionEvents() []companionEvent {
	companionQueue.mu.Lock()
	defer companionQueue.mu.Unlock()
	if len(companionQueue.items) == 0 {
		return nil
	}
	items := companionQueue.items
	companionQueue.items = nil
	return items
}

func resetCompanionQueue() {
	companionQueue.mu.Lock()
	companionQueue.items = nil
	companionQueue.mu.Unlock()
}

// ── PIN and address ──

// companionNewPIN returns a random six-digit PIN. Showing it on screen is the
// only way it is ever handed out, so the QR code is the whole pairing flow.
func companionNewPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// companionURL is the pairing link the QR encodes. In screenshot mode it names
// the stand-in address and PIN, so a published QR pairs with nothing
// (screenshotmode.go).
func companionURL(ip string, port int, pin string) string {
	if shotModeOn() {
		ip, pin = shotLANAddress, shotPairPIN
	}
	return fmt.Sprintf("%s/?p=%s", companionBaseURL(ip, port), pin)
}

func companionBaseURL(host string, port int) string {
	if port == 80 {
		return "http://" + host
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func companionPort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_COMPANION_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return companionDefaultPort
}

// companionDropDirFor returns the folder phone uploads land in, creating it.
// It lives inside the shared folder the FTP server publishes, so a file sent
// from the phone is already where the file manager looks.
func companionDropDirFor(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	dir := filepath.Join(root, companionDropDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ── Input sanitising ──

// sanitizeUploadName makes a browser-supplied filename safe to join onto a
// directory: no path separators, no traversal, no control characters, and a
// length that still fits a FAT32 entry.
func sanitizeUploadName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		// The drop folder is on the FAT/exFAT card, which refuses these in a
		// name: "notes 10:15.txt" from a desktop failed as "cannot save".
		if strings.ContainsRune(`:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	if name == "" {
		name = "upload"
	}
	const maxRunes = 80
	if utf8.RuneCountInString(name) > maxRunes {
		runes := []rune(name)
		ext := filepath.Ext(name)
		keep := maxRunes - utf8.RuneCountInString(ext)
		if keep < 1 {
			keep = maxRunes
			ext = ""
		}
		name = string(runes[:keep]) + ext
	}
	return name
}

// createCompanionFile opens dir/name without ever overwriting an existing
// file, appending " (2)" style suffixes on collision. O_EXCL is what makes
// two phones uploading "video.mp4" at once safe.
func createCompanionFile(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		full := filepath.Join(dir, candidate)
		f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return f, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("no free filename for %q", name)
}

// companionTextChunks splits typed text into pieces that fit one SDL
// TextInputEvent (31 bytes plus the NUL). Splitting mid-rune would inject
// mojibake, so the boundary moves back to a rune start.
func companionTextChunks(text string, max int) []string {
	if max < 1 {
		max = 1
	}
	var chunks []string
	for len(text) > 0 {
		if len(text) <= max {
			chunks = append(chunks, text)
			break
		}
		cut := max
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			_, size := utf8.DecodeRuneInString(text)
			cut = size
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}

// companionKeyNames is the whitelist of remote buttons the phone may press.
// The UI translates these names to SDL values; anything else is rejected so a
// crafted request cannot fabricate arbitrary keycodes.
var companionKeyNames = map[string]bool{
	"up": true, "down": true, "left": true, "right": true,
	"a": true, "b": true, "x": true, "y": true,
	"start": true, "back": true, "lb": true, "rb": true,
	"enter": true, "escape": true, "space": true, "tab": true, "backspace": true,
}

// ── Server ──

// companionServer is the HTTP listener. One instance is live for the session:
// the home-header QR needs it running even while the pairing card is closed.
type companionServer struct {
	pin  string
	root string
	sink func(companionEvent)
	// Captured on the main thread before the listener starts. HTTP handlers
	// never read the live config map while it is being edited on screen.
	reportEndpoint string
	reportVersion  string
	bugMu          sync.Mutex

	mu   sync.Mutex
	ln   net.Listener
	srv  *http.Server
	port int
	// Port 80 is an optional second listener so the browser can open
	// http://<IP> without a port. The configured port stays live for
	// existing companion and screen-share links. There is deliberately no
	// mDNS name: many routers and phones answer jukahub.local with NXDOMAIN,
	// so the numeric address is the only one shown.
	browserPort int
	webSrv      *http.Server
	// throttles is the PIN failure budget and lockout escalation per remote
	// address (SEC-28): a paired phone polling on one address must not keep
	// clearing a guesser's count on another.
	throttles       map[string]*companionThrottle
	diagnosticToken string
	diagnosticUntil time.Time
	diagnosticText  string
}

func newCompanionServer(pin, root string, sink func(companionEvent)) *companionServer {
	if sink == nil {
		sink = enqueueCompanion
	}
	return &companionServer{pin: pin, root: root, sink: sink}
}

func (s *companionServer) Start() error {
	port := companionPort()
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		// The preferred port is taken (a second JukaHub, or a stray process).
		// Any free port works: the QR code carries the real one.
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
	}
	handler := s.handler()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Keep-alive connections a client stopped using are dropped instead of
		// being held for good: any phone on the network could otherwise pile
		// them up until the handheld ran out of file descriptors.
		IdleTimeout: 2 * time.Minute,
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	browserPort := actualPort
	var webLn net.Listener
	var webSrv *http.Server
	if actualPort != 80 {
		// A handheld running as root can offer the short address. If port 80
		// is unavailable, the configured port remains the working address.
		if extra, listenErr := net.Listen("tcp", ":80"); listenErr == nil {
			webLn = extra
			webSrv = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
			browserPort = 80
		}
	}
	s.mu.Lock()
	s.ln = ln
	s.srv = srv
	s.port = actualPort
	s.browserPort = browserPort
	s.webSrv = webSrv
	s.mu.Unlock()
	go func() {
		// A closed listener is the normal shutdown path; anything else is worth
		// a line in the log, since the companion silently stopped answering
		// phones before.
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logWarn("companion", "server stopped: %v", err)
		}
	}()
	if webSrv != nil {
		go func() {
			if err := webSrv.Serve(webLn); err != nil && err != http.ErrServerClosed {
				logWarn("companion", "short-address listener stopped: %v", err)
			}
		}()
	}
	return nil
}

func (s *companionServer) Stop() {
	s.mu.Lock()
	srv := s.srv
	webSrv := s.webSrv
	s.srv = nil
	s.webSrv = nil
	s.ln = nil
	s.diagnosticToken = ""
	s.diagnosticText = ""
	s.diagnosticUntil = time.Time{}
	s.mu.Unlock()
	if srv == nil {
		return
	}
	// A short grace for a request finishing, then every connection is closed.
	// Shutdown alone never ends an open /stream viewer — it waits for it, so a
	// new PIN froze the UI for the whole timeout and the old viewer, paired
	// with the PIN just retired, kept watching.
	shutdown := func(server *http.Server) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}
	if webSrv != nil {
		done := make(chan struct{})
		go func() { shutdown(webSrv); close(done) }()
		shutdown(srv)
		<-done
		return
	}
	shutdown(srv)
}

func (s *companionServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

func (s *companionServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

func (s *companionServer) BrowserPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browserPort
}

func (s *companionServer) URL() string {
	return companionURL(localIPv4(), s.BrowserPort(), s.pin)
}

// authorized compares the request PIN in constant time. The header is what
// the page uses after load; the query string only exists so the QR link opens
// the page already paired.
func (s *companionServer) authorized(r *http.Request) bool {
	got := r.Header.Get("X-Companion-Pin")
	if got == "" {
		got = r.URL.Query().Get("p")
	}
	if len(got) != len(s.pin) || s.pin == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.pin)) == 1
}

// throttled reports whether the failure budget is spent. Twenty wrong PINs in
// thirty seconds cannot be explained by a mis-scanned QR code.
//
// Each spent budget doubles the next lockout (30 s, 1 min, 2 min … about an
// hour). A fixed 30 s window let a script on the network try 2,400 PINs an
// hour — a few percent of the whole six-digit space every day, for a PIN that
// unlocks the keyboard and with it the Terminal.
func (s *companionServer) throttled(addr string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.throttleLocked(addr, now)
	if t.fails < companionFailLimit {
		return false
	}
	window := companionFailWindow << min(t.lockouts, 7)
	if now.Sub(t.since) > window {
		t.fails = 0
		t.lockouts++
		return false
	}
	return true
}

func (s *companionServer) noteFailure(addr string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.throttleLocked(addr, now)
	if t.fails == 0 {
		t.since = now
	}
	t.fails++
}

// noteSuccess clears the lockout escalation for addr only once the right PIN
// arrives, so a phone that mistyped a few times is not slowed down for the
// session — while a guesser on another address keeps its own budget.
func (s *companionServer) noteSuccess(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.throttles[addr]
	if t == nil || (t.fails == 0 && t.lockouts == 0) {
		return
	}
	t.fails = 0
	t.lockouts = 0
	t.since = time.Time{}
}

// companionTrackedAddrs caps the per-address budgets: a network of pinned
// devices must not grow the map without bound. The least recently seen address
// is dropped for a new one.
const companionTrackedAddrs = 64

type companionThrottle struct {
	fails    int
	since    time.Time
	lockouts int
	last     time.Time
}

// throttleLocked returns addr's budget, creating it (and pruning the oldest
// entry at the cap) as needed. The caller holds s.mu.
func (s *companionServer) throttleLocked(addr string, now time.Time) *companionThrottle {
	if s.throttles == nil {
		s.throttles = make(map[string]*companionThrottle)
	}
	t := s.throttles[addr]
	if t == nil {
		if len(s.throttles) >= companionTrackedAddrs {
			oldest, at := "", time.Time{}
			for k, v := range s.throttles {
				if oldest == "" || v.last.Before(at) {
					oldest, at = k, v.last
				}
			}
			delete(s.throttles, oldest)
		}
		t = &companionThrottle{}
		s.throttles[addr] = t
	}
	t.last = now
	return t
}

func (s *companionServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/text", s.guard(s.handleText))
	mux.HandleFunc("/api/key", s.guard(s.handleKey))
	mux.HandleFunc("/api/open", s.guard(s.handleOpenURL))
	mux.HandleFunc("/api/upload", s.guard(s.handleUpload))
	mux.HandleFunc("/api/screen", s.guard(s.handleScreen))
	mux.HandleFunc("/api/bug-report", s.guard(s.handleBugReport))
	// Screen Share (companion_cast.go): the viewer page, its MJPEG feed, single
	// frames for browsers without MJPEG, and the state the page shows. They
	// take the view-only share token or this PIN; the feed and the frames
	// refuse while sharing is off.
	mux.HandleFunc("/watch", s.castGuard(s.handleWatch))
	mux.HandleFunc("/stream", s.castGuard(s.handleStream))
	mux.HandleFunc("/frame.jpg", s.castGuard(s.handleFrame))
	mux.HandleFunc("/status", s.castGuard(s.handleCastStatus))
	// PlayStation sign-in: the page that walks a phone through Sony's login,
	// and the call that hands the PSN Account-ID back (psn_signin.go).
	mux.HandleFunc("/psn", s.handlePSNPage)
	mux.HandleFunc("/api/psn", s.guard(s.handlePSN))
	// The main page's PlayStation card: the saved Account-ID, and Remote
	// Play's own download on the handheld (companion_psremote.go).
	mux.HandleFunc("/api/remote-play", s.guard(s.handleRemotePlay))
	// Voice commands from the phone's keyboard microphone: the page, and the
	// call that runs one command and answers with what it did (voicecmd_phone.go).
	mux.HandleFunc("/voice", s.handleVoicePage)
	mux.HandleFunc("/api/voice", s.guard(s.handleVoice))
	// The Groq API key card: whether a key is saved, and a new one from the
	// phone, checked with Groq before it is saved (companion_groq.go).
	mux.HandleFunc("/api/groq-key", s.guard(s.handleGroqKey))
	// The VPN card: whether a WireGuard configuration is saved (its name,
	// never its keys), and a new one pasted or picked on the phone (vpn.go).
	mux.HandleFunc("/api/vpn", s.guard(s.handleVPN))
	// The RetroAchievements and RomM cards: the account and the server
	// connection typed on the phone, checked with the service before they are
	// saved; keys and tokens are never sent back (companion_accounts.go).
	mux.HandleFunc("/api/retroachievements", s.guard(s.handleRetroAchievements))
	mux.HandleFunc("/api/romm", s.guard(s.handleRomM))
	// The itch.io card: Get Homebrew's API key, checked with itch.io before
	// it is saved and never sent back (homebrew_store_key.go).
	mux.HandleFunc("/api/itch-key", s.guard(s.handleItchKey))
	// The Email and Calendars cards: accounts and calendar links checked with
	// their servers before they are saved; passwords and links are never sent
	// back (companion_mailcal.go).
	mux.HandleFunc("/api/mailcal", s.guard(s.handleMailCal))
	// Cloud save sync: the page that sets up Google sign-in (a client file
	// from Google Cloud Console) or a WebDAV server, checked before they are
	// saved; secrets are never sent back (companion_cloud.go).
	mux.HandleFunc("/cloud", s.handleCloudPage)
	mux.HandleFunc("/api/cloud", s.guard(s.handleCloud))
	// Music servers and scrobbling: Navidrome, Jellyfin, Last.fm and
	// ListenBrainz, checked before they are saved (companion_music.go).
	mux.HandleFunc("/music", s.handleMusicPage)
	mux.HandleFunc("/api/music", s.guard(s.handleMusicAPI))
	// Steam: the sign-in through Steam's own page (a one-use token, never the
	// PIN, goes to Steam) and the card's profile link, Web API key and Sign
	// out (steam_signin.go).
	mux.HandleFunc("/steam", s.handleSteamStart)
	mux.HandleFunc("/steam/return", s.handleSteamReturn)
	mux.HandleFunc("/api/steam", s.guard(s.handleSteamAPI))
	// The Files card: the FTP server's shares, browsed and changed from the
	// page, every route behind the PIN (companion_files.go).
	s.registerFileRoutes(mux)
	// The Tasks card: lists and tasks typed, ticked and deleted from the
	// phone, behind the PIN (companion_tasks.go).
	s.registerTaskRoutes(mux)
	// The Game Boy Printer scrapbook card: the prints, read-only, behind the
	// PIN (gbprinter_companion.go).
	s.registerGBPrinterRoutes(mux)
	// The update card: a release on the user's channel, installed from the
	// phone the way the handheld's prompt installs it (companion_update.go).
	s.registerUpdateRoutes(mux)
	// Download logs, personal details masked (companion_logs.go).
	s.registerLogRoutes(mux)
	return mux
}

func (s *companionServer) issueDiagnosticsLink(text string) (string, error) {
	host := localIPv4()
	if host == "" {
		return "", fmt.Errorf("connect to Wi-Fi to create a diagnostics link")
	}
	if !s.Running() {
		return "", fmt.Errorf("the phone companion server is not running")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	s.mu.Lock()
	s.diagnosticToken = token
	s.diagnosticText = text
	s.diagnosticUntil = time.Now().Add(10 * time.Minute)
	port := s.port
	s.mu.Unlock()
	return fmt.Sprintf("http://%s/diagnostics?token=%s", net.JoinHostPort(host, strconv.Itoa(port)), token), nil
}

func (s *companionServer) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	got := r.URL.Query().Get("token")
	s.mu.Lock()
	want, body, until := s.diagnosticToken, s.diagnosticText, s.diagnosticUntil
	s.mu.Unlock()
	if len(got) != len(want) || want == "" || time.Now().After(until) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "diagnostics link expired or invalid", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="jukahub-diagnostics.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, body)
}

func (s *companionServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr := castHost(r.RemoteAddr)
		now := time.Now()
		if s.throttled(addr, now) {
			http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
			return
		}
		if !s.authorized(r) {
			s.noteFailure(addr, now)
			http.Error(w, "bad PIN", http.StatusUnauthorized)
			return
		}
		s.noteSuccess(addr)
		// The startup lock (bootlock.go): nothing acts from the phone until
		// the handheld is unlocked. The page itself still loads.
		if startupLocked() {
			http.Error(w, "JukaHub is locked", http.StatusLocked)
			return
		}
		// Kiosk / Kid mode: a phone must not go round the lock (uimode.go).
		if uiModeCompanionLocked() {
			http.Error(w, "JukaHub is in "+uiModeLabel(uiModeNow())+" mode", http.StatusLocked)
			return
		}
		next(w, r)
	}
}

func (s *companionServer) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	addr := castHost(r.RemoteAddr)
	now := time.Now()
	if s.throttled(addr, now) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	if !s.authorized(r) {
		s.noteFailure(addr, now)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, companionLockedPage)
		return
	}
	io.WriteString(w, companionPageHTML)
}

func writeCompanionJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *companionServer) handleText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text  string `json:"text"`
		Enter bool   `json:"enter"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if len(body.Text) > companionTextLimit {
		http.Error(w, "text too long", http.StatusRequestEntityTooLarge)
		return
	}
	// A textarea newline is an Enter press: scenes like the terminal expect
	// lines, and SDL text input cannot carry a newline meaningfully.
	lines := strings.Split(strings.ReplaceAll(body.Text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			s.sink(companionEvent{Kind: companionEventText, Text: line})
		}
		if i < len(lines)-1 || (body.Enter && i == len(lines)-1) {
			s.sink(companionEvent{Kind: companionEventKey, Text: "enter"})
		}
	}
	writeCompanionJSON(w, map[string]interface{}{"ok": true})
}

func (s *companionServer) handleKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	name := strings.ToLower(strings.TrimSpace(body.Key))
	if !companionKeyNames[name] {
		http.Error(w, "unknown key", http.StatusBadRequest)
		return
	}
	s.sink(companionEvent{Kind: companionEventKey, Text: name})
	writeCompanionJSON(w, map[string]interface{}{"ok": true})
}

func (s *companionServer) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(body.URL)
	if !looksLikeStreamURL(url) {
		http.Error(w, "only http(s) and stream links can be sent", http.StatusBadRequest)
		return
	}
	s.sink(companionEvent{Kind: companionEventURL, Text: url})
	writeCompanionJSON(w, map[string]interface{}{"ok": true})
}

func (s *companionServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionMaxUpload)
	// The file part is streamed straight onto the card. ParseMultipartForm
	// spooled any part over 8 MiB to os.TempDir(), which on the handheld is a
	// small RAM-backed tmpfs: a video upload filled it (and failed as "too
	// large") or pushed the device into the out-of-memory killer.
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	var file *multipart.Part
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "upload too large or malformed", http.StatusRequestEntityTooLarge)
			return
		}
		if p.FormName() == "file" {
			file = p
			break
		}
		p.Close()
	}
	if file == nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	dir, err := companionDropDirFor(s.root)
	if err != nil {
		http.Error(w, "cannot open drop folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	name := sanitizeUploadName(file.FileName())
	out, saved, err := createCompanionFile(dir, name)
	if err != nil {
		http.Error(w, "cannot save file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	written, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(filepath.Join(dir, saved))
		var tooBig *http.MaxBytesError
		if errors.As(copyErr, &tooBig) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "upload interrupted", http.StatusInternalServerError)
		return
	}
	s.sink(companionEvent{Kind: companionEventUpload, Text: saved})
	trophyEvent(tkFileTransfer)
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "name": saved, "size": written})
}

// ── Pages ──

const companionLockedPage = `<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>JukaHub Companion</title>
<style>html{background:#0b0d12;color:#e6e8ef;font:16px system-ui,sans-serif}
body{margin:0;display:grid;place-items:center;min-height:100vh;padding:24px}
.card{max-width:360px;background:#151923;border:1px solid #232a38;border-radius:16px;padding:24px}
h1{font-size:20px;margin:0 0 8px}p{color:#8b93a7;line-height:1.5;margin:0 0 16px}
label{display:block;margin-bottom:6px}input{box-sizing:border-box;width:100%;padding:12px;font:20px system-ui;border:1px solid #6f86ff;border-radius:10px;background:#0b0d12;color:#e6e8ef}
button{width:100%;margin-top:12px;padding:12px;border:0;border-radius:10px;background:#6f86ff;color:#fff;font:600 16px system-ui}</style>
<div class=card><h1>Enter the pairing PIN</h1>
<p>Use the six-digit PIN shown on JukaHub, or scan its QR code to open this page directly.</p>
<form method=get action="/"><label for=pin>PIN</label><input id=pin name=p inputmode=numeric pattern="[0-9]{6}" maxlength=6 autocomplete=one-time-code required><button type=submit>Open JukaHub</button></form></div>`

const companionPageHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub Companion</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--ok:#3ecf8e;--bad:#ff6b6b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.4 system-ui,-apple-system,sans-serif}
body{padding:0 16px calc(40px + env(safe-area-inset-bottom));max-width:560px;margin:0 auto}
/* The handheld's screen stays pinned at the top while the cards scroll under
   it, so the picture is in view while the remote or the keyboard is used. */
.top{position:-webkit-sticky;position:sticky;top:0;z-index:10;background:var(--bg);margin:0 -16px 14px;padding:calc(10px + env(safe-area-inset-top)) 16px 10px;border-bottom:1px solid var(--line);box-shadow:0 10px 18px -14px #000}
header{display:flex;align-items:baseline;justify-content:space-between;margin:0 0 8px}
h1{font-size:20px;margin:0}
#state{font-size:12px;color:var(--dim)}
.shot{position:relative;min-height:56px;background:#0f131b;border:1px solid var(--line);border-radius:10px;overflow:hidden}
/* At most 30% of the height (16:9 and the Brick's 4:3 alike), and less while
   the phone's keyboard is up, so the cards keep room to type in. */
.shot img{display:block;margin:0 auto;width:auto;height:auto;max-width:100%;max-height:30vh}
body.typing .shot img{max-height:16vh}
.shot img:not([src]){display:none}
.shotmsg{padding:18px 12px;text-align:center;color:var(--dim);font-size:13px}
.top .row{margin-top:8px;flex-wrap:nowrap}
.top .row>*{flex:1 1 0;padding:8px 6px;min-height:38px;font-size:14px}
body.noshot .shot,body.noshot .shotonly{display:none}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin-bottom:14px}
/* The card a handheld's code asked for ("#groq", "#vpn"): moved to the top and
   ringed, so the phone opens where the paste goes. */
.card.picked{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
/* A shorthand cannot take "inherit" for the family, so the fonts are named;
   16 px in the fields keeps iOS from zooming in on focus. */
textarea,input[type=url],input[type=password],input[type=text],select{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif;resize:vertical}
textarea{min-height:88px}
a{color:#a8b5ff}
button{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
button.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button:active{transform:translateY(1px)}
.row{display:flex;gap:10px;margin-top:10px;flex-wrap:wrap}
.row>*{flex:1 1 120px}
.pad{display:grid;grid-template-columns:repeat(3,1fr);gap:8px}
.pad button{background:#1d2330;border:1px solid var(--line);font-size:17px;padding:14px 0}
.pad .wide{grid-column:span 3}
label.check{display:flex;align-items:center;gap:8px;color:var(--dim);font-size:14px;margin-top:10px}
label.check input{width:20px;height:20px;accent-color:var(--accent)}
#toast{position:fixed;left:50%;bottom:24px;transform:translateX(-50%);background:#1d2330;border:1px solid var(--line);border-radius:999px;padding:10px 18px;font-size:14px;opacity:0;transition:opacity .2s;pointer-events:none}
#toast.show{opacity:1}
#toast.bad{border-color:var(--bad)}
small{color:var(--dim);display:block;margin-top:8px;font-size:12px}
.note{color:var(--dim);font-size:14px;margin:0 0 10px}
/* An element's own display (the .row flex) beats the browser's rule for the
   hidden attribute, so the attribute gets its own. */
[hidden]{display:none!important}
/* The PlayStation card: its answer under the field, its line about Remote
   Play on the handheld, and the progress bar (the download's share, or a slow
   pulse while the size is not known yet or the download is being checked and
   installed). */
.msg{margin-top:8px;font-size:14px;color:var(--dim)}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
.split{border-top:1px solid var(--line);margin-top:14px;padding-top:12px}
.split .note{margin:0}
/* The Email and Calendars cards: fields a little apart, the server rows as
   host and port side by side, and the saved accounts above the form. */
.mc input,.mc select{margin-top:8px}
.mc details{margin-top:10px}
.mc summary{color:var(--dim);font-size:14px;padding:6px 0;cursor:pointer}
.mc .pair{display:flex;gap:8px}
.mc .pair input{flex:1 1 auto;min-width:0}
.mc .pair .port{flex:0 0 92px}
.mc .pair select{flex:0 0 120px}
.mclist{margin:0 0 6px}
.bar{height:6px;margin-top:10px;background:#0f131b;border:1px solid var(--line);border-radius:99px;overflow:hidden}
.bar>span{display:block;height:100%;width:0;background:var(--accent);transition:width .4s}
.bar.idle{display:none}
.bar.pulse>span{animation:pulse 1.2s ease-in-out infinite alternate}
@keyframes pulse{from{opacity:.25}to{opacity:.8}}
/* The Files card: a breadcrumb, then one row per file with its actions; on a
   narrow phone the actions drop under the name. */
.crumbs{display:flex;flex-wrap:wrap;align-items:center;gap:2px;margin:0 0 8px;font-size:14px;color:var(--dim)}
.crumbs button{background:none;border:0;color:#a8b5ff;padding:4px 4px;min-height:32px;font:600 14px system-ui,-apple-system,sans-serif;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.flist{border:1px solid var(--line);border-radius:10px;max-height:60vh;overflow-y:auto;background:#0f131b}
.frow{display:flex;align-items:center;gap:8px;padding:6px 10px;border-top:1px solid var(--line)}
.frow:first-child{border-top:0}
.fname{flex:1 1 auto;min-width:0;background:none;border:0;border-radius:0;padding:6px 0;min-height:40px;text-align:left;color:var(--ink);font:15px/1.3 system-ui,-apple-system,sans-serif}
.fname b,.fname span{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.fname b{font-weight:600}
.fname span{color:var(--dim);font-size:12px}
.fname.dir b{color:#a8b5ff}
.facts{display:flex;gap:4px;flex:0 0 auto}
.facts button{padding:6px 9px;min-height:36px;font-size:13px}
.fempty{padding:14px;color:var(--dim);font-size:14px;text-align:center}
.filedrop{border:2px dashed var(--line);border-radius:10px;padding:12px;margin:8px 0;color:var(--dim);text-align:center;font-size:14px}
.filedrop.active{border-color:var(--accent);background:rgba(111,134,255,.12);color:var(--ink)}
@media (max-width:420px){.frow{flex-wrap:wrap}.facts{width:100%;justify-content:flex-end}}
/* The drop card (companion_drop.go): a big target that is also a button,
   then one row per file with where it goes and its progress. */
.filedrop.big{padding:24px 12px;margin:0;font-size:16px;color:var(--ink);cursor:pointer;-webkit-user-select:none;user-select:none}
.filedrop.big small{margin-top:4px}
.dlist{margin-top:6px}
.drow{border-top:1px solid var(--line);padding:8px 0}
.drow:first-child{border-top:0}
.drow>b{display:block;font-weight:600;font-size:15px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.drow>span{display:block;color:var(--dim);font-size:13px;overflow-wrap:anywhere}
.drow.bad>span{color:var(--bad)}
.drow.ok>span{color:var(--ok)}
.drow .bar{margin-top:6px}
.drow .row{margin-top:6px}
.drow select{flex:1 1 100%}
/* The Tasks card (companion_tasks.go): the list, then day and time side by
   side, then one row per task with its checkbox and Delete. */
.tk select{margin-top:8px}
.tk input[type=date],.tk input[type=time]{width:100%;min-width:0;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif;color-scheme:dark}
.tk .pair{display:flex;gap:8px;margin-top:8px}
.tk .pair>*{flex:1 1 0}
.tk .flist{margin-top:10px}
.tkrow{display:flex;align-items:center;gap:10px;padding:6px 10px;border-top:1px solid var(--line)}
.tkrow:first-child{border-top:0}
.tkrow input{width:22px;height:22px;flex:0 0 auto;accent-color:var(--accent)}
.tkrow .fname{cursor:pointer}
.tkrow.done b{text-decoration:line-through;color:var(--dim)}
.tkrow .late{color:var(--bad)}
/* A phone on its side has little height: the screen moves to a column of its
   own on the left and the cards scroll on the right. */
@media (orientation:landscape) and (max-height:540px){
  body:not(.noshot){max-width:none;padding-left:calc(46vw + 16px)}
  body:not(.noshot) .top{position:fixed;left:0;top:0;bottom:0;width:46vw;margin:0;overflow-y:auto;border-bottom:0;border-right:1px solid var(--line);box-shadow:none;padding:calc(8px + env(safe-area-inset-top)) 12px 10px max(12px,env(safe-area-inset-left))}
  body:not(.noshot) .shot img{max-height:calc(100vh - 120px)}
}
</style>
<div class=top id=top>
  <header><h1>JukaHub</h1><span id=state>connecting</span></header>
  <div class=shot><img id=screen alt="Handheld screen"><div class=shotmsg id=shotMsg>Loading the handheld's screen...</div></div>
  <div class=row><button class="ghost shotonly" id=shotScreen>Refresh</button><button class="ghost shotonly" id=autoScreen>Live: off</button><button class=ghost id=pinScreen>Hide screen</button></div>
</div>

<div class=card id=updCard hidden>
  <h2>JukaHub update</h2>
  <p class=note id=updNote></p>
  <ul class=note id=updNotes hidden></ul>
  <div class=row><button id=updInstall>Install</button></div>
  <div class="bar idle" id=updBar><span></span></div>
  <div class=msg id=updMsg aria-live=polite></div>
  <small>The handheld downloads the update and restarts JukaHub when it is ready. Not while a game is running, and on a low battery only on the charger.</small>
  <script defer src="/companion-extras.js"></script>
</div>

<div class=card id=dropCard>
  <h2>Send to the handheld</h2>
  <div class="filedrop big" id=dropZone role=button tabindex=0>Drop games, covers, music, folders…<small>or tap to choose files</small></div>
  <input type=file id=dropPick multiple hidden>
  <input type=file id=dropPickDir webkitdirectory multiple hidden>
  <div class=row><button class=ghost id=dropDirBtn type=button>Send a folder</button></div>
  <label class=check><input type=checkbox id=dropSort checked>Put each file where it belongs</label>
  <div class=msg id=dropMsg aria-live=polite></div>
  <div class=dlist id=dropList></div>
  <small>Games go to their console's folder in Roms, covers beside their game, songs to Music, BIOS files to BIOS and anything else to PhoneDrop. A folder keeps its structure: books to Books, albums to Music, pictures to Pictures, disc, DOS and ScummVM games to Roms. Works over Wi-Fi or a USB network connection.</small>
</div>

<div class=card>
  <h2>Type</h2>
  <textarea id=text placeholder="Type here, it appears on the handheld"></textarea>
  <label class=check><input type=checkbox id=enter checked>Press Enter after sending</label>
  <div class=row><button id=sendText>Send text</button><button class=ghost id=clearText>Clear</button></div>
</div>

<div class="card tk" id=tasksCard>
  <h2>Tasks</h2>
  <p class=note id=tkStatus>Checking the handheld...</p>
  <input type=text id=tkText placeholder="New task, like Buy milk" maxlength=300 autocomplete=off enterkeyhint=done>
  <select id=tkList aria-label="List"></select>
  <div class=pair><input type=date id=tkDate aria-label="Due day"><input type=time id=tkTime aria-label="Due time" step=300></div>
  <div class=row><button id=tkAdd>Add on handheld</button><button class=ghost id=tkNewList>New list</button></div>
  <div class=msg id=tkMsg aria-live=polite></div>
  <div class=flist id=tkItems hidden></div>
  <small>Leave the day and time empty for a plain checklist item; a task with a time reminds you on the handheld. Ring as alarm, repeats and edits are in Tasks on the handheld.</small>
</div>

<div class=card>
  <h2>Voice</h2>
  <div class=row><button id=voicePage>Say what to open</button></div>
  <small>Your keyboard's microphone sends commands like "open YouTube" or "volume 50".</small>
</div>

<div class=card id=cloudCard>
  <h2>Cloud save sync</h2>
  <div class=row><button id=cloudPage>Google Drive or WebDAV</button></div>
  <small>Set up Google sign-in with a client of your own, or connect Nextcloud, Koofr or a NAS, so saves follow you to every JukaHub.</small>
</div>

<div class=card id=bugCard>
  <h2>Report a bug</h2>
  <p class=note>Describe the problem here. The handheld sends it to the JukaHub report service, which files a public GitHub issue. Your device model and a reference are included; logs are not.</p>
  <select id=bugCategory aria-label="Problem category">
    <option value=other>Other problem</option><option value=crash>Crash</option><option value=ui>UI</option>
    <option value=controls>Controls</option><option value=media>Media</option><option value=network>Network</option>
  </select>
  <input type=text id=bugTitle placeholder="Short title" maxlength=120 autocomplete=off>
  <textarea id=bugDescription placeholder="What happened? What did you expect?" maxlength=4000></textarea>
  <div class=row><button id=submitBug>Submit report</button></div>
  <div class=msg id=bugMsg aria-live=polite></div>
  <div class=row><a id=bugLink target=_blank rel="noopener noreferrer" hidden></a></div>
  <div class=split id=bugQueued hidden><p class=note>Saved reports on the handheld</p><div id=bugQueuedList></div></div>
</div>

<div class=card>
  <h2>Remote</h2>
  <div class=pad>
    <button data-key=lb>&#8676;</button><button data-key=up>&#9650;</button><button data-key=rb>&#8677;</button>
    <button data-key=left>&#9664;</button><button data-key=a>A</button><button data-key=right>&#9654;</button>
    <button data-key=x>X</button><button data-key=down>&#9660;</button><button data-key=y>Y</button>
    <button data-key=start>Start</button><button data-key=b>B</button><button data-key=back>Back</button>
    <button class="wide ghost" data-key=escape>Leave current screen</button>
  </div>
</div>

<div class=card>
  <h2>Send a link</h2>
  <input type=url id=url placeholder="https://youtu.be/...">
  <div class=row><button id=sendURL>Play on handheld</button></div>
  <small>YouTube, direct video links and live streams open in the player.</small>
</div>

<div class=card id=filesCard>
  <h2>Files</h2>
  <div class=crumbs id=filesCrumbs></div>
  <div class=filedrop id=filesDrop>Drop games, covers or music here, or choose Upload here</div>
  <div class=row><button id=filesUpload>Upload here</button><button class=ghost id=filesMkdir>New folder</button></div>
  <input type=file id=filesPick multiple hidden>
  <div class="bar idle" id=filesBar><span id=filesBarFill></span></div>
  <div class=msg id=filesMsg aria-live=polite></div>
  <div class=flist id=filesList><div class=fempty>Loading...</div></div>
  <small>Open a destination folder first. Use this page over Wi-Fi or an active USB network connection. Roms/Ports is read-only here. Delete removes a file or an empty folder only. Renaming or deleting a folder JukaHub needs, like a console's folder in Roms, asks first.</small>
</div>

<div class=card id=gbprintCard>
  <h2>Game Boy Printer scrapbook</h2>
  <style>.gbprow{display:flex;gap:12px;align-items:center;padding:8px 0;border-top:1px solid var(--line)}.gbprow:first-child{border-top:0}.gbprow img{width:96px;height:auto;image-rendering:pixelated;background:#fff;border-radius:4px;flex:0 0 auto}.gbpinfo{flex:1 1 auto;min-width:0}.gbpinfo b,.gbpinfo span{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.gbpinfo span{color:var(--dim);font-size:12px}.gbpbtn{display:inline-block;padding:6px 12px;margin:6px 6px 0 0;border:1px solid var(--line);border-radius:8px;background:#1d2330;color:var(--ink);text-decoration:none;font-size:13px}</style>
  <div id=gbprintList><div class=fempty>Loading...</div></div>
  <div class=msg id=gbprintMsg aria-live=polite></div>
  <div class=row><button class=ghost id=gbprintRefresh>Refresh</button></div>
  <small>What your Game Boy games printed on the handheld, newest first. Save keeps a print as it is; 2× and 4× are bigger copies with sharp pixels, for printing on paper.</small>
  <script defer src="/gbprint.js"></script>
</div>

<div class=card id=psCard>
  <h2>PlayStation Remote Play</h2>
  <p class=note id=psStatus>Checking the handheld...</p>
  <input type=text id=psAccount placeholder="PSN Account-ID, like AbCdEfGhIjk=" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <div class=row><button id=sendPS>Save on handheld</button><button class=ghost id=psSignIn>Sign in to get it</button></div>
  <div class=msg id=psMsg></div>
  <small>Pairing a PS5, or a PS4 on firmware 7 or newer, needs your PSN Account-ID once. It is 12 characters, not your sign-in name or the PIN, and no console menu shows it. Don't know it? Tap Sign in to get it: you sign in to Sony on this phone, and the handheld keeps only the Account-ID, never your password. The long account number some tools print works in the field too.</small>
  <div class=split id=psRemote hidden>
    <p class=note id=psRemoteText></p>
    <div class="bar idle" id=psBar><span id=psBarFill></span></div>
    <div class=row id=psStartRow hidden><button class=ghost id=psStart>Download now</button></div>
  </div>
</div>

<div class=card id=groqCard>
  <h2>Groq API key</h2>
  <p class=note id=groqStatus>Checking the handheld...</p>
  <input type=password id=groqKey placeholder="gsk_..." autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label class=check><input type=checkbox id=groqShow>Show the key</label>
  <div class=row><button id=sendGroq>Save on handheld</button></div>
  <small id=groqNote>Voice typing, voice commands and the Juka chat use it. Get a free key at <a href="https://console.groq.com/keys" target=_blank rel="noopener noreferrer">console.groq.com/keys</a>, copy it and paste it here. The handheld checks it with Groq and never shows it again.</small>
</div>

<div class=card id=raCard>
  <h2>RetroAchievements</h2>
  <p class=note id=raStatus>Checking the handheld...</p>
  <input type=text id=raUser placeholder="Username" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=raKey placeholder="Web API key (32 letters or digits)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label class=check><input type=checkbox id=raShow>Show the key</label>
  <div class=row><button id=sendRA>Save on handheld</button></div>
  <div class=msg id=raMsg></div>
  <small>Your Web API key is on <a href="https://retroachievements.org/settings" target=_blank rel="noopener noreferrer">retroachievements.org/settings</a> under Keys: copy it and paste it here. The handheld checks the account with RetroAchievements and never shows the key again. Leave a field empty to keep what the handheld has.</small>
</div>

<div class=card id=rommCard>
  <h2>RomM server</h2>
  <p class=note id=rommStatus>Checking the handheld...</p>
  <input type=url id=rommURL placeholder="http://192.168.1.20:8080" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=rommToken placeholder="Client API token" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label class=check><input type=checkbox id=rommShow>Show the token</label>
  <div class=row><button id=sendRomM>Save on handheld</button></div>
  <div class=msg id=rommMsg></div>
  <small>Your own RomM server's address, and a client API token with read and write access (made in RomM under your profile). The handheld checks them with the server and never shows the token again. Leave a field empty to keep what the handheld has.</small>
</div>

<div class=card id=itchCard>
  <h2>itch.io</h2>
  <p class=note id=itchStatus>Checking the handheld...</p>
  <input type=password id=itchKey placeholder="API key" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label class=check><input type=checkbox id=itchShow>Show the key</label>
  <div class=row><button id=sendItch>Save on handheld</button></div>
  <div class=msg id=itchMsg></div>
  <small>Get Homebrew downloads free games and the ones you bought with your own itch.io account. Make a key on <a href="https://itch.io/user/settings/api-keys" target=_blank rel="noopener noreferrer">itch.io/user/settings/api-keys</a> (Generate new API key), copy it and paste it here. The handheld checks it with itch.io and never shows it again.</small>
</div>

<div class="card mc" id=mailCard>
  <h2>Email</h2>
  <p class=note id=mailStatus>Checking the handheld...</p>
  <div class="flist mclist" id=mailList hidden></div>
  <input type=text inputmode=email id=mailEmail placeholder="you@example.com" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=mailPass placeholder="App password" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label class=check><input type=checkbox id=mailShow>Show the password</label>
  <input type=text id=mailName placeholder="Your name on sent mail (optional)" autocomplete=off maxlength=100>
  <p class=msg id=mailHelp></p>
  <details id=mailAdv>
    <summary>Advanced: servers and calendar</summary>
    <div class=pair><input type=text id=mailImapHost placeholder="IMAP server" autocomplete=off autocapitalize=off spellcheck=false><input type=text class=port id=mailImapPort inputmode=numeric placeholder="993" autocomplete=off><select id=mailImapSec aria-label="IMAP security"><option value=tls>TLS</option><option value=starttls>STARTTLS</option></select></div>
    <div class=pair><input type=text id=mailSmtpHost placeholder="SMTP server" autocomplete=off autocapitalize=off spellcheck=false><input type=text class=port id=mailSmtpPort inputmode=numeric placeholder="465" autocomplete=off><select id=mailSmtpSec aria-label="SMTP security"><option value=tls>TLS</option><option value=starttls>STARTTLS</option></select></div>
    <input type=text id=mailUser placeholder="User name, if it is not the address" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
    <input type=url id=mailCalDAV placeholder="CalDAV address (optional), like https://caldav.example.com/" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  </details>
  <div class=row><button id=sendMail>Add on handheld</button></div>
  <div class=msg id=mailMsg></div>
  <small>Most providers want an app password made for this, not your normal password: the handheld says where to make one when you type your address. It checks the login with your mail server and never shows the password again. Add the same address again to change its password or servers.</small>
</div>

<div class="card mc" id=calCard>
  <h2>Calendars</h2>
  <p class=note id=calStatus>Checking the handheld...</p>
  <div class="flist mclist" id=calList hidden></div>
  <input type=url id=calURL placeholder="https://... .ics, webcal://... or a CalDAV address" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=text id=calName placeholder="Name (optional)" autocomplete=off maxlength=80>
  <input type=text id=calUser placeholder="CalDAV user name (empty for a link)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=calPass placeholder="CalDAV password (empty for a link)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=sendCal>Add on handheld</button></div>
  <div class=msg id=calMsg></div>
  <small>Google Calendar: on a computer, Settings &gt; your calendar &gt; Secret address in iCal format. Outlook: Settings &gt; Calendar &gt; Shared calendars &gt; Publish a calendar &gt; ICS link. The handheld shows calendars read-only and keeps the link to itself.</small>
</div>

<div class=card id=steamCard>
  <h2>Steam</h2>
  <p class=note id=steamStatus>Checking the handheld...</p>
  <div class=row><button id=steamSignIn>Sign in through Steam</button><button class=ghost id=steamSignOut hidden>Sign out</button></div>
  <input type=text id=steamProfile placeholder="Or your profile link: steamcommunity.com/id/..." autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=steamKey placeholder="Web API key (optional)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=sendSteam>Save on handheld</button></div>
  <div class=msg id=steamMsg></div>
  <small>Steam's website sign-in gives the handheld your Steam ID, never your password. For your full library, use Sign in with the Steam app on the handheld, or add a Web API key from <a href="https://steamcommunity.com/dev/apikey" target=_blank rel="noopener noreferrer">steamcommunity.com/dev/apikey</a>. Steam may ask for a login even when Game details are Public. Games start on a PC with Steam; on the handheld they start on your paired PC.</small>
</div>

<div class=card id=vpnCard>
  <h2>VPN (WireGuard)</h2>
  <p class=note id=vpnStatus>Checking the handheld...</p>
  <textarea id=vpnConf placeholder="[Interface]&#10;PrivateKey = ...&#10;Address = ...&#10;&#10;[Peer]&#10;PublicKey = ...&#10;Endpoint = ..." autocomplete=off autocapitalize=off autocorrect=off spellcheck=false></textarea>
  <input type=file id=vpnFile accept=".conf,text/plain">
  <div class=row><button id=sendVPN>Save on handheld</button></div>
  <div class=msg id=vpnMsg></div>
  <small>Paste the WireGuard configuration your VPN provider or your own server gives you, or pick its .conf file. Got a QR code? Scan it with the phone's camera and paste the text. The handheld keeps it to itself and never sends it back; send it over your own Wi-Fi, then switch the VPN on in Settings &gt; Network.</small>
</div>

<p class=note style="text-align:center"><a href="#" id=logsLink>Download logs</a><br><small>For a bug report: Wi-Fi names, account names, e-mail, Bluetooth and LAN addresses are masked.</small></p>

<div id=toast></div>
<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const stateEl = document.getElementById('state');
const toastEl = document.getElementById('toast');
let toastTimer = 0;

function toast(msg, bad) {
  toastEl.textContent = msg;
  toastEl.className = bad ? 'bad show' : 'show';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastEl.className = ''; }, 2200);
}
function hdr(extra) {
  const h = Object.assign({'X-Companion-Pin': pin}, extra || {});
  return h;
}
async function api(path, body, json) {
  const opts = {method: 'POST', headers: hdr(json ? {'Content-Type': 'application/json'} : null)};
  if (body !== undefined) opts.body = json ? JSON.stringify(body) : body;
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json().catch(() => ({}));
}
async function sendText() {
  const el = document.getElementById('text');
  const value = el.value;
  if (!value.trim()) return;
  const enter = document.getElementById('enter').checked;
  // Keep each request small so a pasted essay cannot trip the 4 KB limit.
  const lines = value.replace(/\r\n/g, '\n').split('\n');
  let batch = [];
  let size = 0;
  const flush = async () => {
    if (!batch.length) return;
    await api('/api/text', {text: batch.join('\n'), enter: false}, true);
    batch = []; size = 0;
  };
  for (const line of lines) {
    if (size + line.length > 1200) await flush();
    batch.push(line); size += line.length + 1;
  }
  await flush();
  if (enter) await api('/api/key', {key: 'enter'}, true);
  toast('Sent');
  el.value = '';
  refreshSoon();
}
document.getElementById('sendText').onclick = () => sendText().catch(e => toast(e.message, true));
document.getElementById('clearText').onclick = () => { document.getElementById('text').value = ''; document.getElementById('text').focus(); };
document.getElementById('voicePage').onclick = () => { location.href = '/voice?p=' + encodeURIComponent(pin); };
document.getElementById('cloudPage').onclick = () => { location.href = '/cloud?p=' + encodeURIComponent(pin); };

// A report typed on the phone keeps its reference across a retry, so a lost
// response cannot create a second issue. Saved device reports have the same
// GitHub option as the handheld's queued sheet.
let bugDraft = null;
const bugMsgEl = document.getElementById('bugMsg');
const bugLinkEl = document.getElementById('bugLink');
function bugSay(text, bad) { bugMsgEl.textContent = text || ''; bugMsgEl.className = bad ? 'msg bad' : 'msg'; }
function bugShowLink(url, label) {
  bugLinkEl.hidden = !url;
  if (url) { bugLinkEl.href = url; bugLinkEl.textContent = label; }
}
function bugShowQueued(items) {
  const section = document.getElementById('bugQueued');
  const list = document.getElementById('bugQueuedList');
  list.replaceChildren();
  section.hidden = !items || !items.length;
  for (const item of items || []) {
    const row = document.createElement('p');
    row.className = 'note';
    row.textContent = (item.title || 'Bug report') + ' · ' + item.id + ' ';
    const link = document.createElement('a');
    link.href = item.github_url;
    link.target = '_blank'; link.rel = 'noopener noreferrer';
    link.textContent = 'Open on GitHub';
    row.append(link);
    list.append(row);
  }
}
async function bugLoadDraft() {
  const res = await fetch('/api/bug-report', {headers: hdr(), cache: 'no-store'});
  if (!res.ok) throw new Error('Could not reach the handheld');
  const data = await res.json();
  bugDraft = {id: data.id, created: data.created};
  bugShowQueued(data.queued);
}
bugLoadDraft().catch(e => bugSay(e.message, true));
document.getElementById('submitBug').onclick = async () => {
  const title = document.getElementById('bugTitle').value.trim();
  const description = document.getElementById('bugDescription').value.trim();
  if (!title && !description) { bugSay('Add a title or description first', true); return; }
  const button = document.getElementById('submitBug');
  button.disabled = true; button.textContent = 'Sending...';
  try {
    if (!bugDraft) await bugLoadDraft();
    const result = await api('/api/bug-report', {
      ...bugDraft, category: document.getElementById('bugCategory').value, title, description,
    }, true);
    if (result.status === 'sent') {
      bugSay(result.issue ? 'Filed as issue #' + result.issue + ' · reference ' + result.reference :
        'Report sent · reference ' + result.reference);
      bugShowLink(result.issue_url, 'View GitHub issue');
    } else if (result.status === 'queued') {
      bugSay('Saved on the handheld · reference ' + result.reference + '. It will retry automatically.');
      bugShowLink(result.github_url, 'Submit on GitHub instead');
    } else {
      bugSay(result.message || 'Open the prefilled issue on GitHub to submit it.', true);
      bugShowLink(result.github_url, 'Submit on GitHub');
    }
    if (result.status === 'sent' || result.status === 'queued') {
      document.getElementById('bugTitle').value = '';
      document.getElementById('bugDescription').value = '';
      bugDraft = null;
      try { await bugLoadDraft(); } catch (_) { /* the confirmation above still stands */ }
    }
  } catch (e) {
    bugSay(e.message, true);
  } finally {
    button.disabled = false; button.textContent = 'Submit report';
  }
};
document.querySelectorAll('[data-key]').forEach(btn => {
  btn.onclick = () => api('/api/key', {key: btn.dataset.key}, true).then(refreshSoon).catch(e => toast(e.message, true));
});
document.getElementById('sendURL').onclick = () => {
  const el = document.getElementById('url');
  const url = el.value.trim();
  if (!url) return;
  api('/api/open', {url}, true).then(() => { toast('Opening on handheld'); el.value = ''; refreshSoon(); })
    .catch(e => toast(e.message, true));
};
// The drop card (companion_drop.go). Each file is asked about first: its name
// and size, then the start of a disc image or the end of a zip when the
// handheld asks for them. A console or a game to pick shows under the file's
// row. Then the file is sent once, to the place the handheld chose. The files
// of one disc game (tracks, cue sheet, playlist) follow the first answer.
// After the last file the handheld shows one line for the batch.
const dropListEl = document.getElementById('dropList');
const dropMsgEl = document.getElementById('dropMsg');
const dropZoneEl = document.getElementById('dropZone');
const dropPickEl = document.getElementById('dropPick');
const dropQueue = [];
let dropBusy = false;
let dropBatch = '';
function dropSay(text, bad) { dropMsgEl.textContent = text || ''; dropMsgEl.className = bad ? 'msg bad' : 'msg'; }
function dropRow(file) {
  const row = document.createElement('div');
  row.className = 'drow';
  const title = document.createElement('b');
  title.textContent = file.name;
  const line = document.createElement('span');
  line.textContent = 'Waiting...';
  const bar = document.createElement('div');
  bar.className = 'bar idle';
  const fill = document.createElement('span');
  bar.append(fill);
  row.append(title, line, bar);
  dropListEl.append(row);
  return {
    row: row,
    say(text, cls) { line.textContent = text; row.className = cls ? 'drow ' + cls : 'drow'; },
    pct(p) { bar.classList.toggle('idle', p < 0); fill.style.width = (p < 0 ? 0 : p) + '%'; }
  };
}
// A disc game's files share a name once "(Track 2)" or "(Disc 1)" is set
// aside: the console found or chosen for one goes for the rest.
function dropGroup(name) {
  const m = /^(.*)\.(bin|cue|img|ccd|sub|m3u|chd|iso|gdi|raw|cdi)$/i.exec(name);
  return m ? m[1].replace(/\s*\((track|disc|cd)\s*\d+\)/gi, '').trim().toLowerCase() : '';
}
// Pictures go last, so a cover finds a game sent with it; sheets after the
// tracks they name, so a track's header answers for them.
function dropOrder(name) {
  if (/\.(png|jpe?g|webp)$/i.test(name) && !/\.p8\.png$/i.test(name)) return 2;
  if (/\.(cue|m3u|ccd|sub)$/i.test(name)) return 1;
  return 0;
}
async function dropPlan(file, extra, body) {
  const res = await fetch('/api/drop/plan?name=' + encodeURIComponent(file.name) + '&size=' + file.size +
    '&batch=' + dropBatch + (extra || ''),
    {method: 'POST', headers: hdr(body ? {'Content-Type': 'application/octet-stream'} : null), body: body || null});
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}
async function dropDecide(file, extra) {
  const plan = await dropPlan(file, extra);
  if (plan.kind !== 'need') return plan;
  const n = Math.min(plan.bytes || 0, file.size);
  const part = plan.need === 'tail' ? file.slice(file.size - n) : file.slice(0, n);
  return dropPlan(file, (extra || '') + '&part=' + encodeURIComponent(plan.need), part);
}
function dropAsk(r, plan) {
  return new Promise(resolve => {
    const box = document.createElement('div');
    box.className = 'row';
    const sel = document.createElement('select');
    sel.setAttribute('aria-label', plan.pick === 'game' ? 'Game' : plan.pick === 'as' ? 'Where it goes' : 'Console');
    for (const c of plan.choices || []) {
      const o = document.createElement('option');
      o.value = c.id;
      o.textContent = c.label;
      sel.append(o);
    }
    const done = value => { box.remove(); resolve(value); };
    if (sel.options.length) {
      box.append(sel, filesButton(plan.pick === 'game' ? 'Use as its cover' : 'Send it there', '',
        () => done('&' + plan.pick + '=' + encodeURIComponent(sel.value))));
    }
    box.append(filesButton('PhoneDrop', 'ghost', () => done('&to=drop')), filesButton('Skip', 'ghost', () => done(null)));
    r.row.append(box);
    r.say(plan.note || 'Where does it go?');
  });
}
function dropSend(file, plan, r, where) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/drop?plan=' + encodeURIComponent(plan.token) + (plan.exists ? '&replace=1' : ''));
    xhr.setRequestHeader('X-Companion-Pin', pin);
    xhr.upload.onprogress = ev => {
      if (!ev.lengthComputable) return;
      const p = Math.floor(ev.loaded * 100 / ev.total);
      r.pct(p);
      r.say('Sending to ' + where + ': ' + p + '%');
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) { resolve(); return; }
      reject(new Error((xhr.responseText || '').trim() || ('Error ' + xhr.status)));
    };
    xhr.onerror = () => reject(new Error('The connection to the handheld was lost.'));
    const fd = new FormData();
    fd.append('file', file);
    xhr.send(fd);
  });
}
async function dropOne(item, groups) {
  const f = item.file;
  const r = item.r;
  const key = dropGroup(f.name);
  let extra = '';
  if (!document.getElementById('dropSort').checked) extra = '&to=drop';
  else if (key && groups[key]) extra = '&system=' + encodeURIComponent(groups[key]);
  r.say('Checking where it goes...');
  let plan = await dropDecide(f, extra);
  while (plan.kind === 'ask') {
    const pick = await dropAsk(r, plan);
    if (pick === null) { r.say('Skipped'); return; }
    r.say('Checking where it goes...');
    plan = await dropDecide(f, pick);
  }
  if (key && plan.kind === 'game' && plan.system) groups[key] = plan.system;
  const where = (plan.dest || '').replace(/^\//, '');
  if (plan.exists && !confirm(plan.confirm || (f.name + ' is already there. Replace it?'))) {
    r.say('Kept the copy on the handheld');
    return;
  }
  r.say('Sending to ' + where + '...');
  r.pct(0);
  await dropSend(f, plan, r, where);
  r.pct(-1);
  r.say([plan.label, where, plan.note].filter(Boolean).join(' · '), 'ok');
}
// Whole folders (companion_dropdir.go): "Send a folder", or folders dropped
// on the page. The folder's list of files goes first and the handheld answers
// one place for all of it ("Books/Asimov", "Roms/PS/Final Fantasy VII") or
// asks; then each file goes to its own path under that place, named by its
// number in the list, with one progress line and a Cancel for the folder.
// Hidden and system files (.DS_Store, Thumbs.db, __MACOSX) stay behind.
const dropDirPickEl = document.getElementById('dropPickDir');
const dropDirMax = 5000;
const dropDirDepth = 12;
function dropJunk(rel) {
  return rel.split('/').some(p => p.charAt(0) === '.' || /^(__macosx|thumbs\.db|desktop\.ini)$/i.test(p));
}
// A folder picker's files by their top folder; a file with none goes the
// single-file way.
function dropPickedFolders(list) {
  const groups = new Map();
  const loose = [];
  for (const f of list) {
    const rel = f.webkitRelativePath || '';
    const cut = rel.indexOf('/');
    if (cut <= 0) { loose.push(f); continue; }
    const top = rel.slice(0, cut);
    if (!groups.has(top)) groups.set(top, []);
    groups.get(top).push({file: f, path: rel.slice(cut + 1)});
  }
  return {loose: loose, folders: Array.from(groups, g => ({name: g[0], files: g[1]}))};
}
function dropEntryFile(entry) { return new Promise((resolve, reject) => entry.file(resolve, reject)); }
// One folder's entries: readEntries answers in batches until an empty one.
// A folder with more than dropDirMax entries is cut short and marked (cut),
// so the walk refuses it instead of sending part of it.
function dropEntryList(dir) {
  const reader = dir.createReader();
  const all = [];
  return new Promise((resolve, reject) => {
    const next = () => reader.readEntries(batch => {
      if (!batch.length) { resolve(all); return; }
      all.push(...batch);
      if (all.length > dropDirMax) { all.cut = true; resolve(all); return; }
      next();
    }, reject);
    next();
  });
}
// A dropped folder's files with their paths, bounded: more than dropDirMax
// files stops the walk (the folder is refused); folders deeper than
// dropDirDepth are left out.
async function dropWalk(dir) {
  const files = [];
  let over = false;
  const walk = async (d, prefix, depth) => {
    const list = await dropEntryList(d);
    if (list.cut) { over = true; return; }
    for (const e of list) {
      if (over) return;
      if (dropJunk(e.name)) continue;
      if (e.isDirectory) {
        if (depth < dropDirDepth) await walk(e, prefix + e.name + '/', depth + 1);
        continue;
      }
      if (files.length >= dropDirMax) { over = true; return; }
      try { files.push({file: await dropEntryFile(e), path: prefix + e.name}); } catch (_) { /* unreadable: left out */ }
    }
  };
  await walk(dir, '', 1);
  return {files: files, over: over};
}
// A drop with folders in it: each folder's walk starts now, while the browser
// still allows it, and the folder is queued; loose files go the single-file
// way. A drop with no folder is the plain file list.
function dropTake(dt) {
  const items = Array.from((dt && dt.items) || []);
  const entries = items.map(it => (it.kind === 'file' && it.webkitGetAsEntry) ? it.webkitGetAsEntry() : null);
  if (!entries.some(e => e && e.isDirectory)) { dropFiles(dt && dt.files); return; }
  const folders = [];
  const loose = [];
  for (const e of entries) {
    if (e && e.isDirectory) folders.push({name: e.name, walk: dropWalk(e).catch(err => ({files: [], error: err}))});
    else if (e && e.isFile) loose.push(dropEntryFile(e).catch(() => null));
  }
  Promise.all(loose).then(files => dropFiles(files.filter(Boolean), folders));
}
async function dropFolderPlan(query, body, list) {
  const type = list ? 'application/json' : (body ? 'application/octet-stream' : '');
  const res = await fetch('/api/drop/folder' + query, {method: 'POST', headers: hdr(type ? {'Content-Type': type} : null),
    body: list ? JSON.stringify(list) : (body || null)});
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}
function dropFolderSend(item, id, i, file, replace, progress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    item.xhr = xhr;
    xhr.open('POST', '/api/drop/folder/file?id=' + encodeURIComponent(id) + '&i=' + i + (replace ? '&replace=1' : ''));
    xhr.setRequestHeader('X-Companion-Pin', pin);
    xhr.upload.onprogress = ev => { if (ev.lengthComputable) progress(Math.min(ev.loaded, file.size)); };
    const fail = (text, fatal) => { const e = new Error(text); e.fatal = fatal; reject(e); };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) { resolve(); return; }
      // A full card, a forgotten folder, a refused PIN or a locked handheld
      // end the folder; anything else costs this file only.
      fail((xhr.responseText || '').trim() || ('Error ' + xhr.status), [401, 403, 410, 423, 429, 507].indexOf(xhr.status) >= 0);
    };
    xhr.onerror = () => fail('The connection to the handheld was lost.', true);
    xhr.onabort = () => fail('Cancelled', true);
    const fd = new FormData();
    fd.append('file', file);
    xhr.send(fd);
  });
}
// dropFolderOne sends one folder and returns how many of its files were not
// sent (0 when the player skipped or cancelled it).
async function dropFolderOne(item) {
  const r = item.r;
  const d = item.folder;
  const tooMany = 'More than ' + dropDirMax + ' files: send its folders one at a time.';
  let files = d.files || [];
  if (d.walk) {
    r.say('Reading the folder...');
    const got = await d.walk;
    if (got.error) throw got.error;
    if (got.over) throw new Error(tooMany);
    files = got.files;
  }
  files = files.filter(x => !dropJunk(x.path));
  if (!files.length) { r.say('Nothing to send in this folder', 'bad'); return 0; }
  if (files.length > dropDirMax) throw new Error(tooMany);
  r.say('Checking where it goes...');
  let plan = await dropFolderPlan('', null, {name: d.name, batch: dropBatch,
    to: document.getElementById('dropSort').checked ? '' : 'drop',
    files: files.map(x => ({path: x.path, size: x.file.size}))});
  const id = plan.id;
  const again = extra => '?id=' + encodeURIComponent(id) + (extra || '');
  for (let round = 0; plan.kind === 'need' || plan.kind === 'ask'; round++) {
    if (round > 8) throw new Error('The handheld could not tell where this folder goes.');
    if (plan.kind === 'need') {
      const f = files[plan.file] && files[plan.file].file;
      if (!f) throw new Error('The handheld asked for a file that is not in the folder.');
      const n = Math.min(plan.bytes || 0, f.size);
      plan = await dropFolderPlan(again('&part=' + encodeURIComponent(plan.need)), plan.need === 'tail' ? f.slice(f.size - n) : f.slice(0, n));
      continue;
    }
    const pick = await dropAsk(r, plan);
    if (pick === null) { r.say('Skipped'); return 0; }
    r.say('Checking where it goes...');
    plan = await dropFolderPlan(again(pick));
  }
  const where = (plan.dest || '').replace(/^\//, '');
  const skip = new Set((plan.skip || []).map(s => s.i));
  const told = (plan.skip || []).filter(s => !s.quiet);
  const exists = new Set(plan.exists || []);
  let replace = false;
  if (exists.size) {
    replace = confirm(exists.size === 1 ? '1 of these files is already in ' + where + '. Replace it?' :
      exists.size + ' of these files are already in ' + where + '. Replace them?');
  }
  const todo = [];
  files.forEach((x, i) => { if (!skip.has(i) && (replace || !exists.has(i))) todo.push(i); });
  const kept = replace ? 0 : exists.size;
  const leftOut = !told.length ? '' : told.length === 1 ? '1 file left out (' + told[0].why + ')' :
    told.length + ' files left out (' + told[0].why + ', …)';
  if (!todo.length) {
    r.say([kept ? 'Kept the copies on the handheld' : 'Nothing to send', where, leftOut].filter(Boolean).join(' · '), kept ? '' : 'bad');
    return 0;
  }
  const total = todo.reduce((n, i) => n + files[i].file.size, 0) || 1;
  const box = document.createElement('div');
  box.className = 'row';
  item.cancel = false;
  box.append(filesButton('Cancel', 'ghost', () => { item.cancel = true; if (item.xhr) item.xhr.abort(); }));
  r.row.append(box);
  let sent = 0, done = 0, failed = 0, stopped = '', firstErr = '';
  r.pct(0);
  r.say('Sending to ' + where + '...');
  try {
    for (const i of todo) {
      if (item.cancel) break;
      const f = files[i].file;
      try {
        await dropFolderSend(item, id, i, f, replace && exists.has(i), loaded => {
          const p = Math.min(100, Math.floor((sent + loaded) * 100 / total));
          r.pct(p);
          r.say('Sending to ' + where + ': ' + (done + failed + 1) + ' of ' + todo.length + ' files, ' + p + '%');
        });
        done++;
      } catch (e) {
        if (item.cancel) break;
        failed++;
        if (!firstErr) firstErr = files[i].path + ': ' + e.message;
        if (e.fatal) { stopped = e.message; break; }
      }
      sent += f.size;
    }
  } finally {
    box.remove();
    item.xhr = null;
  }
  r.pct(-1);
  if (item.cancel) {
    r.say('Cancelled: ' + done + ' of ' + todo.length + ' files sent to ' + where);
    return 0;
  }
  const unsent = todo.length - done;
  if (unsent) {
    r.say(done + ' of ' + todo.length + ' files sent to ' + where + ' · ' + (stopped || firstErr), 'bad');
    return unsent;
  }
  r.say([plan.label, where, done === 1 ? '1 file' : done + ' files', plan.note, kept ? kept + ' kept' : '', leftOut].filter(Boolean).join(' · '), 'ok');
  return 0;
}
async function dropFiles(selection, folders) {
  const files = Array.from(selection || []);
  if (!files.length && !(folders && folders.length)) return;
  files.map((f, i) => ({f: f, i: i}))
    .sort((a, b) => (dropOrder(a.f.name) - dropOrder(b.f.name)) || (a.i - b.i))
    .forEach(x => dropQueue.push({file: x.f, r: dropRow(x.f)}));
  for (const d of folders || []) dropQueue.push({folder: d, r: dropRow({name: d.name + '/'})});
  if (dropBusy) return;
  dropBusy = true;
  dropBatch = Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
  dropSay('');
  const groups = {};
  let failed = 0;
  try {
    while (dropQueue.length) {
      const item = dropQueue.shift();
      try {
        if (item.folder) failed += await dropFolderOne(item);
        else await dropOne(item, groups);
      } catch (e) {
        item.r.pct(-1);
        item.r.say(e.message, 'bad');
        failed++;
      }
    }
  } finally {
    dropBusy = false;
  }
  let text = '';
  try { text = (await api('/api/drop/done?batch=' + dropBatch)).text || ''; } catch (e) { /* each row already says what arrived */ }
  const missed = failed ? (failed === 1 ? '1 file was not sent.' : failed + ' files were not sent.') : '';
  dropSay([text, missed].filter(Boolean).join(' '), failed > 0);
  if (text) toast(text);
  refreshSoon();
  if (filesPath !== '/' && !filesBusy) filesLoad(filesPath).catch(() => {});
}
dropZoneEl.onclick = () => dropPickEl.click();
dropZoneEl.onkeydown = ev => {
  if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); dropPickEl.click(); }
};
dropPickEl.onchange = () => {
  const picked = Array.from(dropPickEl.files || []);
  dropPickEl.value = '';
  dropFiles(picked);
};
document.getElementById('dropDirBtn').onclick = () => dropDirPickEl.click();
dropDirPickEl.onchange = () => {
  const picked = dropPickedFolders(Array.from(dropDirPickEl.files || []));
  dropDirPickEl.value = '';
  dropFiles(picked.loose, picked.folders);
};
for (const eventName of ['dragenter', 'dragover']) {
  dropZoneEl.addEventListener(eventName, ev => {
    ev.preventDefault();
    ev.dataTransfer.dropEffect = 'copy';
    dropZoneEl.classList.add('active');
  });
}
dropZoneEl.addEventListener('dragleave', () => dropZoneEl.classList.remove('active'));
dropZoneEl.addEventListener('drop', ev => {
  ev.preventDefault();
  dropZoneEl.classList.remove('active');
  dropTake(ev.dataTransfer);
});
// Files dropped anywhere else on the page go the same way, instead of the
// browser leaving the page to open them. The Files card's own target keeps
// its folder: its handler has taken the drop first.
function dropHasFiles(ev) { return !!(ev.dataTransfer && Array.from(ev.dataTransfer.types || []).indexOf('Files') >= 0); }
document.addEventListener('dragover', ev => {
  if (dropHasFiles(ev)) { ev.preventDefault(); ev.dataTransfer.dropEffect = 'copy'; }
});
document.addEventListener('drop', ev => {
  if (ev.defaultPrevented || !dropHasFiles(ev)) return;
  ev.preventDefault();
  dropTake(ev.dataTransfer);
});
// The screen pinned at the top. Each picture is a capture on the handheld, so
// it is fetched on demand: Refresh, Live (every 1.2 s while this page is in
// view), and a moment after every remote press, text, link or upload, so the
// picture follows what the phone just did without polling all the time.
const screenEl = document.getElementById('screen');
const shotMsg = document.getElementById('shotMsg');
let shotURL = '';
let autoTimer = 0;
let soonTimer = 0;
let shotBusy = false;
let shotAgain = false;
function screenShown() { return !document.body.classList.contains('noshot'); }
async function loadScreen() {
  if (shotBusy) { shotAgain = true; return; }
  shotBusy = true;
  try {
    const res = await fetch('/api/screen', {headers: hdr(), cache: 'no-store'});
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    const blob = await res.blob();
    const next = URL.createObjectURL(blob);
    screenEl.src = next;
    if (shotURL) URL.revokeObjectURL(shotURL);
    shotURL = next;
    shotMsg.hidden = true;
  } finally {
    shotBusy = false;
    if (shotAgain) { shotAgain = false; setTimeout(() => { loadScreen().catch(() => {}); }, 0); }
  }
}
function shotFailed(e) {
  // Keep the last picture; only an empty frame gets the reason.
  if (screenEl.getAttribute('src')) return;
  shotMsg.textContent = 'The screen is not available right now (' + e.message + '). Tap Refresh to try again.';
  shotMsg.hidden = false;
}
function refreshSoon() {
  if (!screenShown() || autoTimer) return;
  clearTimeout(soonTimer);
  soonTimer = setTimeout(() => { loadScreen().catch(shotFailed); }, 450);
}
function setLive(on) {
  clearInterval(autoTimer);
  autoTimer = 0;
  if (on) {
    autoTimer = setInterval(() => { if (!document.hidden) loadScreen().catch(() => {}); }, 1200);
    loadScreen().catch(shotFailed);
  }
  document.getElementById('autoScreen').textContent = on ? 'Live: on' : 'Live: off';
}
function showScreen(shown) {
  document.body.classList.toggle('noshot', !shown);
  document.getElementById('pinScreen').textContent = shown ? 'Hide screen' : 'Show screen';
  try { localStorage.setItem('jukahubCompanionNoScreen', shown ? '' : '1'); } catch (e) {}
  if (!shown) { setLive(false); return; }
  loadScreen().catch(shotFailed);
}
document.getElementById('shotScreen').onclick = () => loadScreen().catch(e => { shotFailed(e); toast(e.message, true); });
document.getElementById('autoScreen').onclick = () => setLive(!autoTimer);
document.getElementById('pinScreen').onclick = () => showScreen(!screenShown());
// While a text field has the phone's keyboard up, the picture shrinks (CSS
// body.typing) so the field and the keyboard both fit under the bar.
document.addEventListener('focusin', e => {
  if (e.target.matches && e.target.matches('textarea, input[type=url], input[type=password], input[type=text]')) document.body.classList.add('typing');
});
document.addEventListener('focusout', () => { document.body.classList.remove('typing'); });
// Coming back to the page (another app, a locked phone) shows the screen as
// it is now, not as it was when the page was left.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && screenShown()) loadScreen().catch(() => {});
});
let startShown = true;
try { startShown = localStorage.getItem('jukahubCompanionNoScreen') !== '1'; } catch (e) {}
showScreen(startShown);

// The Groq API key card. The handheld never sends the key back: only whether
// one is saved and its last four characters.
const groqStatusEl = document.getElementById('groqStatus');
function showGroqStatus(set, tail) {
  if (!set) { groqStatusEl.textContent = 'No key is saved on the handheld yet.'; return; }
  groqStatusEl.textContent = tail ? 'A key is saved on the handheld (it ends in ' + tail + ').' : 'A key is saved on the handheld.';
}
fetch('/api/groq-key', {headers: hdr(), cache: 'no-store'})
  .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
  .then(s => showGroqStatus(!!s.set, s.tail || ''))
  .catch(() => { groqStatusEl.textContent = 'Paste a key to save it on the handheld.'; });
document.getElementById('groqShow').onchange = e => {
  document.getElementById('groqKey').type = e.target.checked ? 'text' : 'password';
};
document.getElementById('sendGroq').onclick = async () => {
  const input = document.getElementById('groqKey');
  const key = input.value.trim();
  if (!key) { toast('Paste your Groq key first', true); input.focus(); return; }
  const btn = document.getElementById('sendGroq');
  btn.disabled = true; btn.textContent = 'Checking with Groq...';
  try {
    const r = await api('/api/groq-key', {key}, true);
    input.value = '';
    showGroqStatus(true, r.tail || '');
    document.getElementById('groqNote').textContent = r.message || 'Saved on the handheld.';
    toast(r.verified ? 'Groq key saved' : 'Groq key saved, not checked');
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Save on handheld';
  }
};

// The RetroAchievements and RomM cards. The handheld sends back the username
// and the server address, never the key or the token: only whether one is
// saved and its last four characters.
function accountSay(id, text, bad) { const el = document.getElementById(id); el.textContent = text || ''; el.className = bad ? 'msg bad' : 'msg'; }
const raStatusEl = document.getElementById('raStatus');
function showRA(s) {
  if (!s || (!s.username && !s.key)) { raStatusEl.textContent = 'No RetroAchievements account is set up on the handheld yet.'; return; }
  let line = s.username ? 'Account: ' + s.username + '.' : 'No username yet.';
  if (s.key) line += s.keyTail ? ' A Web API key is saved (it ends in ' + s.keyTail + ').' : ' A Web API key is saved.';
  else line += ' No Web API key yet.';
  if (s.keyFrom && s.keyFrom !== 'JukaHub') line += ' The key comes from ' + s.keyFrom + '.';
  raStatusEl.textContent = line;
  const user = document.getElementById('raUser');
  if (s.username && !user.value) user.placeholder = 'Username (now ' + s.username + ')';
}
fetch('/api/retroachievements', {headers: hdr(), cache: 'no-store'})
  .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
  .then(showRA)
  .catch(() => { raStatusEl.textContent = 'Enter your account to save it on the handheld.'; });
document.getElementById('raShow').onchange = e => {
  document.getElementById('raKey').type = e.target.checked ? 'text' : 'password';
};
document.getElementById('sendRA').onclick = async () => {
  const user = document.getElementById('raUser');
  const key = document.getElementById('raKey');
  if (!user.value.trim() && !key.value.trim()) { toast('Type your username and paste your key first', true); user.focus(); return; }
  const btn = document.getElementById('sendRA');
  btn.disabled = true; btn.textContent = 'Checking with RetroAchievements...';
  try {
    const r = await api('/api/retroachievements', {username: user.value.trim(), key: key.value.trim()}, true);
    user.value = ''; key.value = '';
    showRA(r);
    accountSay('raMsg', r.message || 'Saved on the handheld.');
    toast('Saved on the handheld');
  } catch (err) {
    accountSay('raMsg', err.message, true);
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Save on handheld';
  }
};
const rommStatusEl = document.getElementById('rommStatus');
function showRomM(s) {
  if (!s || (!s.url && !s.token)) { rommStatusEl.textContent = 'No RomM server is set up on the handheld yet.'; return; }
  let line = s.url ? 'Server: ' + s.url + '.' : 'No server address yet.';
  if (s.token) line += s.tokenTail ? ' A token is saved (it ends in ' + s.tokenTail + ').' : ' A token is saved.';
  else line += ' No token yet.';
  rommStatusEl.textContent = line;
  const url = document.getElementById('rommURL');
  if (s.url && !url.value) url.placeholder = s.url;
}
fetch('/api/romm', {headers: hdr(), cache: 'no-store'})
  .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
  .then(showRomM)
  .catch(() => { rommStatusEl.textContent = 'Enter your server to save it on the handheld.'; });
document.getElementById('rommShow').onchange = e => {
  document.getElementById('rommToken').type = e.target.checked ? 'text' : 'password';
};
document.getElementById('sendRomM').onclick = async () => {
  const url = document.getElementById('rommURL');
  const token = document.getElementById('rommToken');
  if (!url.value.trim() && !token.value.trim()) { toast('Type the address or paste a token first', true); url.focus(); return; }
  const btn = document.getElementById('sendRomM');
  btn.disabled = true; btn.textContent = 'Checking with the server...';
  try {
    const r = await api('/api/romm', {url: url.value.trim(), token: token.value.trim()}, true);
    url.value = ''; token.value = '';
    showRomM(r);
    accountSay('rommMsg', r.message || 'Saved on the handheld.');
    toast('Saved on the handheld');
  } catch (err) {
    accountSay('rommMsg', err.message, true);
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Save on handheld';
  }
};
// The itch.io card (homebrew_store_key.go): whether a key is saved, its last
// four characters and the account it belongs to, never the key.
const itchStatusEl = document.getElementById('itchStatus');
function showItch(s) {
  if (!s || !s.set) { itchStatusEl.textContent = 'No itch.io key is saved on the handheld yet.'; return; }
  let line = s.account ? 'Signed in as ' + s.account + '.' : 'A key is saved.';
  if (s.tail) line += ' The key ends in ' + s.tail + '.';
  itchStatusEl.textContent = line;
}
fetch('/api/itch-key', {headers: hdr(), cache: 'no-store'})
  .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
  .then(showItch)
  .catch(() => { itchStatusEl.textContent = 'Paste your key to save it on the handheld.'; });
document.getElementById('itchShow').onchange = e => {
  document.getElementById('itchKey').type = e.target.checked ? 'text' : 'password';
};
document.getElementById('sendItch').onclick = async () => {
  const key = document.getElementById('itchKey');
  if (!key.value.trim()) { toast('Paste your API key first', true); key.focus(); return; }
  const btn = document.getElementById('sendItch');
  btn.disabled = true; btn.textContent = 'Checking with itch.io...';
  try {
    const r = await api('/api/itch-key', {key: key.value.trim()}, true);
    key.value = '';
    showItch(r);
    accountSay('itchMsg', r.message || 'Saved on the handheld.');
    toast('Saved on the handheld');
  } catch (err) {
    accountSay('itchMsg', err.message, true);
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Save on handheld';
  }
};

// The Email and Calendars cards (companion_mailcal.go). The handheld lists
// what it has without a password or a full link, and sends its provider
// presets, so an address fills the servers here and says where the app
// password is made. A field the user typed in is never overwritten.
let mcPresets = [];
let mcMicrosoft = false;
const mcAuto = ['mailImapHost', 'mailImapPort', 'mailImapSec', 'mailSmtpHost', 'mailSmtpPort', 'mailSmtpSec', 'mailCalDAV'];
function mcEl(id) { return document.getElementById(id); }
function mcPresetFor(email) {
  const at = email.lastIndexOf('@');
  if (at <= 0 || at === email.length - 1) return null;
  const domain = email.slice(at + 1).toLowerCase();
  for (const p of mcPresets) if ((p.domains || []).indexOf(domain) >= 0) return p;
  return {key: '', name: domain, imapHost: 'imap.' + domain, imapPort: 993, imapSec: 'tls',
    smtpHost: 'smtp.' + domain, smtpPort: 465, smtpSec: 'tls', caldav: '', oauth: '', passwordHelp: '', calendarHelp: '', guess: true};
}
function mcFill(p) {
  const values = {mailImapHost: p.imapHost, mailImapPort: String(p.imapPort || ''), mailImapSec: p.imapSec || 'tls',
    mailSmtpHost: p.smtpHost, mailSmtpPort: String(p.smtpPort || ''), mailSmtpSec: p.smtpSec || 'tls', mailCalDAV: p.caldav || ''};
  for (const id of mcAuto) {
    const el = mcEl(id);
    if (el.dataset.typed === '1') continue;
    el.value = values[id] || (el.tagName === 'SELECT' ? 'tls' : '');
  }
}
function mcHelp(p) {
  const el = mcEl('mailHelp');
  el.replaceChildren();
  el.className = 'msg';
  if (!p) return;
  const lines = [];
  if (p.oauth === 'microsoft') {
    lines.push(mcMicrosoft
      ? 'Outlook addresses sign in with Microsoft: on the handheld, open Email & calendar and choose Sign in with Microsoft. A password only works for a custom setup.'
      : 'Outlook.com does not take passwords from mail apps, and this build has no Microsoft sign-in. Add your Outlook calendar\'s ICS link in the Calendars card instead.');
  } else if (p.passwordHelp) {
    lines.push(p.name + ' needs an app password: ');
  } else if (p.guess) {
    lines.push('JukaHub does not know ' + p.name + ': it guessed the servers below (Advanced). Check them with your provider.');
  }
  for (const line of lines) el.append(document.createTextNode(line));
  if (p.oauth !== 'microsoft' && p.passwordHelp) {
    // "myaccount.google.com/apppasswords (needs 2-Step Verification)": the
    // first word is the page, the rest stays text.
    const cut = p.passwordHelp.indexOf(' ');
    const page = cut > 0 ? p.passwordHelp.slice(0, cut) : p.passwordHelp;
    if (/^[a-z0-9.-]+\.[a-z]{2,}(\/[A-Za-z0-9\/._-]*)?$/.test(page)) {
      const a = document.createElement('a');
      a.href = 'https://' + page; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = page;
      el.append(a, document.createTextNode(cut > 0 ? p.passwordHelp.slice(cut) : ''));
    } else {
      el.append(document.createTextNode(p.passwordHelp));
    }
  }
  if (p.calendarHelp) el.append(document.createTextNode(' Calendar: ' + p.calendarHelp + ', then add it in the Calendars card.'));
  else if (p.caldav) el.append(document.createTextNode(' The calendar comes with it (CalDAV).'));
}
for (const id of mcAuto) {
  const el = mcEl(id);
  el.addEventListener(el.tagName === 'SELECT' ? 'change' : 'input', () => { el.dataset.typed = el.value ? '1' : ''; });
}
function mcEmailChanged() {
  const p = mcPresetFor(mcEl('mailEmail').value.trim());
  mcHelp(p);
  if (p) mcFill(p);
}
mcEl('mailEmail').addEventListener('blur', mcEmailChanged);
mcEl('mailEmail').addEventListener('change', mcEmailChanged);
mcEl('mailShow').onchange = e => { mcEl('mailPass').type = e.target.checked ? 'text' : 'password'; };
function mcRow(title, sub, bad, onRemove) {
  const row = document.createElement('div');
  row.className = 'frow';
  const name = document.createElement('div');
  name.className = 'fname';
  const b = document.createElement('b');
  b.textContent = title;
  const s = document.createElement('span');
  s.textContent = sub;
  if (bad) s.style.color = 'var(--bad)';
  name.append(b, s);
  const acts = document.createElement('div');
  acts.className = 'facts';
  const rm = document.createElement('button');
  rm.type = 'button'; rm.className = 'ghost'; rm.textContent = 'Remove';
  rm.onclick = onRemove;
  acts.append(rm);
  row.append(name, acts);
  return row;
}
function showMailCal(s) {
  if (!s) return;
  mcPresets = s.presets || mcPresets;
  mcMicrosoft = !!s.microsoft;
  const accounts = s.accounts || [];
  const calendars = s.calendars || [];
  mcEl('mailStatus').textContent = accounts.length
    ? accounts.length + (accounts.length === 1 ? ' account' : ' accounts') + ' on the handheld (up to ' + s.maxAccounts + ').'
    : 'No email account on the handheld yet.';
  const ml = mcEl('mailList');
  ml.replaceChildren();
  ml.hidden = !accounts.length;
  for (const a of accounts) {
    const parts = [a.provider];
    if (a.microsoft) parts.push('Microsoft sign-in');
    if (!a.mail) parts.push('mail off');
    else if (a.mailError) parts.push(a.mailError);
    if (a.calendar) parts.push(a.calError ? 'calendar: ' + a.calError : 'calendar ' + (a.calHost || 'on'));
    ml.append(mcRow(a.email, parts.join(' · '), !!(a.mailError || a.calError), () => mcRemove('removeAccount', a.id, a.email)));
  }
  mcEl('calStatus').textContent = calendars.length
    ? calendars.length + (calendars.length === 1 ? ' calendar' : ' calendars') + ' on the handheld, besides the ones that come with an account.'
    : 'No calendar link on the handheld yet.';
  const cl = mcEl('calList');
  cl.replaceChildren();
  cl.hidden = !calendars.length;
  for (const c of calendars) {
    const parts = [c.kind === 'caldav' ? 'CalDAV' : 'Link'];
    if (c.host) parts.push(c.host + (c.tail ? ' (ends in ' + c.tail + ')' : ''));
    if (!c.on) parts.push('off');
    if (c.error) parts.push(c.error);
    cl.append(mcRow(c.name || c.host || 'Calendar', parts.join(' · '), !!c.error, () => mcRemove('removeCalendar', c.id, c.name || 'this calendar')));
  }
  mcEmailChanged();
}
function loadMailCal() {
  return fetch('/api/mailcal', {headers: hdr(), cache: 'no-store'})
    .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
    .then(showMailCal);
}
loadMailCal().catch(() => {
  mcEl('mailStatus').textContent = 'Type your address to add it on the handheld.';
  mcEl('calStatus').textContent = 'Paste a calendar link to add it on the handheld.';
});
async function mcRemove(action, id, label) {
  if (!confirm('Remove ' + label + ' from the handheld? Mail and events it synced are forgotten there; nothing changes on the server.')) return;
  try {
    const r = await api('/api/mailcal', {action, id}, true);
    showMailCal(r);
    toast(r.message || 'Removed');
  } catch (err) { toast(err.message, true); }
}
// An Advanced field goes to the handheld only when the user typed in it: the
// filled-in values are the handheld's own preset or guess, and sending them
// would replace the servers an account was saved with.
function mcTyped(id) {
  const el = mcEl(id);
  return el.dataset.typed === '1' ? el.value.trim() : '';
}
mcEl('sendMail').onclick = async () => {
  const email = mcEl('mailEmail');
  const pass = mcEl('mailPass');
  if (!email.value.trim()) { accountSay('mailMsg', 'Type your email address first', true); email.focus(); return; }
  const btn = mcEl('sendMail');
  btn.disabled = true; btn.textContent = 'Checking with your mail server...';
  accountSay('mailMsg', '');
  try {
    const r = await api('/api/mailcal', {
      action: 'addAccount', email: email.value.trim(), password: pass.value, name: mcEl('mailName').value.trim(),
      imapHost: mcTyped('mailImapHost'), imapPort: mcTyped('mailImapPort'), imapSec: mcTyped('mailImapSec'),
      smtpHost: mcTyped('mailSmtpHost'), smtpPort: mcTyped('mailSmtpPort'), smtpSec: mcTyped('mailSmtpSec'),
      username: mcEl('mailUser').value.trim(), caldav: mcTyped('mailCalDAV'),
    }, true);
    pass.value = '';
    showMailCal(r);
    accountSay('mailMsg', r.message || 'Saved on the handheld.');
    toast('Added on the handheld');
  } catch (err) {
    accountSay('mailMsg', err.message, true);
    toast('Not added', true);
  } finally {
    btn.disabled = false; btn.textContent = 'Add on handheld';
  }
};
mcEl('sendCal').onclick = async () => {
  const link = mcEl('calURL');
  if (!link.value.trim()) { accountSay('calMsg', 'Paste the calendar\'s link first', true); link.focus(); return; }
  const btn = mcEl('sendCal');
  btn.disabled = true; btn.textContent = 'Checking the calendar...';
  accountSay('calMsg', '');
  try {
    const r = await api('/api/mailcal', {
      action: 'addCalendar', url: link.value.trim(), name: mcEl('calName').value.trim(),
      username: mcEl('calUser').value.trim(), password: mcEl('calPass').value,
    }, true);
    link.value = ''; mcEl('calName').value = ''; mcEl('calUser').value = ''; mcEl('calPass').value = '';
    showMailCal(r);
    accountSay('calMsg', r.message || 'Saved on the handheld.');
    toast('Calendar added');
  } catch (err) {
    accountSay('calMsg', err.message, true);
    toast('Not added', true);
  } finally {
    btn.disabled = false; btn.textContent = 'Add on handheld';
  }
};

// The Steam card. Sign in goes to Steam's own page through /steam (the
// handheld's one-use link, never the PIN, goes to Steam); a profile link, the
// Web API key and Sign out go to /api/steam. The key is never sent back.
const steamStatusEl = document.getElementById('steamStatus');
function showSteamStatus(s) {
  const out = document.getElementById('steamSignOut');
  out.hidden = !(s.signedIn && s.via !== 'app');
  let text = 'Not signed in yet.';
  if (s.signedIn) {
    const who = s.persona || s.steamId;
    text = s.via === 'app' ? 'Using the Steam app\'s account: ' + who + '.' : 'Signed in as ' + who + '.';
  }
  if (s.key) text += s.keyTail ? ' A Web API key is saved (it ends in ' + s.keyTail + ').' : ' A Web API key is saved.';
  steamStatusEl.textContent = text;
}
function loadSteamStatus() {
  fetch('/api/steam', {headers: hdr(), cache: 'no-store'})
    .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
    .then(showSteamStatus)
    .catch(() => { steamStatusEl.textContent = 'Sign in to show your Steam library on the handheld.'; });
}
loadSteamStatus();
document.getElementById('steamSignIn').onclick = () => { location.href = '/steam?p=' + encodeURIComponent(pin); };
document.getElementById('steamSignOut').onclick = async () => {
  try {
    const r = await api('/api/steam', {signOut: true}, true);
    document.getElementById('steamMsg').textContent = r.message || 'Signed out.';
    toast('Signed out of Steam');
    loadSteamStatus();
  } catch (err) { toast(err.message, true); }
};
document.getElementById('sendSteam').onclick = async () => {
  const profile = document.getElementById('steamProfile');
  const key = document.getElementById('steamKey');
  if (!profile.value.trim() && !key.value.trim()) { toast('Paste a profile link or a key first', true); profile.focus(); return; }
  const btn = document.getElementById('sendSteam');
  btn.disabled = true; btn.textContent = 'Checking with Steam...';
  try {
    const r = await api('/api/steam', {profile: profile.value.trim(), key: key.value.trim()}, true);
    profile.value = ''; key.value = '';
    document.getElementById('steamMsg').textContent = r.message || 'Saved on the handheld.';
    toast('Saved on the handheld');
    loadSteamStatus();
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Save on handheld';
  }
};

// The VPN card. The configuration holds a private key, so the handheld only
// ever says whether one is saved, its name and whether the VPN is on.
const vpnStatusEl = document.getElementById('vpnStatus');
const vpnMsgEl = document.getElementById('vpnMsg');
let vpnName = '';
function vpnSay(text, bad) { vpnMsgEl.textContent = text || ''; vpnMsgEl.className = bad ? 'msg bad' : 'msg'; }
function showVPN(s) {
  if (!s || !s.set) { vpnStatusEl.textContent = 'No VPN configuration is saved on the handheld yet.'; return; }
  let line = 'Saved on the handheld: ' + s.name + '. ';
  line += s.enabled ? 'VPN on' + (s.state ? ': ' + s.state : '') + '.' : 'The VPN is off.';
  vpnStatusEl.textContent = line;
}
fetch('/api/vpn', {headers: hdr(), cache: 'no-store'})
  .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
  .then(showVPN)
  .catch(() => { vpnStatusEl.textContent = 'Paste a WireGuard configuration to save it on the handheld.'; });
document.getElementById('vpnFile').onchange = e => {
  const f = e.target.files && e.target.files[0];
  if (!f) return;
  if (f.size > 65536) { vpnSay('That file is too big to be a WireGuard configuration', true); return; }
  const reader = new FileReader();
  reader.onload = () => {
    document.getElementById('vpnConf').value = String(reader.result || '');
    vpnName = f.name.replace(/\.conf$/i, '');
    vpnSay('Loaded ' + f.name + ': tap Save on handheld');
  };
  reader.onerror = () => vpnSay('Could not read that file', true);
  reader.readAsText(f);
};
// Text typed or pasted over a picked file is not that file any more.
document.getElementById('vpnConf').oninput = () => { vpnName = ''; };
document.getElementById('sendVPN').onclick = async () => {
  const input = document.getElementById('vpnConf');
  const config = input.value.trim();
  if (!config) { toast('Paste the configuration or pick the file first', true); input.focus(); return; }
  const btn = document.getElementById('sendVPN');
  btn.disabled = true;
  try {
    const r = await api('/api/vpn', {name: vpnName, config}, true);
    input.value = ''; vpnName = ''; document.getElementById('vpnFile').value = '';
    showVPN(r);
    vpnSay(r.message || 'Saved on the handheld.');
    toast('VPN configuration saved');
  } catch (err) {
    vpnSay(err.message, true);
  } finally {
    btn.disabled = false;
  }
};

// The PlayStation card. The Account-ID goes to /api/psn, as the sign-in page
// sends it; /api/remote-play says what the handheld has saved (shortened) and
// how Remote Play's own download there is going. That is asked again every
// 1.5 s while the download runs (5 s after an ask that failed), every 10 s
// while a sent Account-ID waits to be saved, every 20 s while the download may
// still start by itself, and whenever the page comes back into view. Answers are numbered, so an older one never replaces a newer one.
const psStatusEl = document.getElementById('psStatus');
const psMsgEl = document.getElementById('psMsg');
let psTimer = 0;
let psSeq = 0;
let psBusy = false;
// psSent is the shortened Account-ID this page sent, until the handheld shows
// it saved: while a game has its screen, the handheld saves it afterwards.
let psSent = '';
function psShort(id) { return id.length > 8 ? id.slice(0, 4) + '…' + id.slice(-2) : id; }
function psSay(text, bad) { psMsgEl.textContent = text || ''; psMsgEl.className = bad ? 'msg bad' : 'msg'; }
function showPSAccount(set, shown) {
  if (psSent && set && shown === psSent) psSent = '';
  if (psSent) {
    psStatusEl.textContent = 'Sent ' + psSent + ': the handheld saves it when JukaHub is back on its screen.';
  } else {
    psStatusEl.textContent = set ? 'Saved on the handheld: ' + shown + '. Send another to replace it.' : 'No PSN Account-ID on the handheld yet.';
  }
  // Without one, signing in is the usual way to get it: that button leads.
  const have = set || !!psSent;
  document.getElementById('psSignIn').classList.toggle('ghost', have);
  document.getElementById('sendPS').classList.toggle('ghost', !have);
}
function psPoll(ms) {
  clearTimeout(psTimer);
  psTimer = setTimeout(() => {
    psTimer = 0;
    loadPS().catch(() => { if ((psBusy || psSent) && !document.hidden) psPoll(5000); });
  }, ms);
}
function showPS(s) {
  const acct = s.account || {};
  showPSAccount(!!acct.set, acct.shown || '');
  const rp = s.remotePlay || {};
  psBusy = !!rp.busy;
  document.getElementById('psRemote').hidden = !rp.text;
  document.getElementById('psRemoteText').textContent = rp.text || '';
  const pct = typeof rp.percent === 'number' ? rp.percent : -1;
  const bar = document.getElementById('psBar');
  bar.classList.toggle('idle', !rp.busy);
  bar.classList.toggle('pulse', !!rp.busy && pct < 0);
  document.getElementById('psBarFill').style.width = (pct >= 0 ? pct : 100) + '%';
  document.getElementById('psStartRow').hidden = !rp.canStart;
  document.getElementById('psStart').textContent = rp.startLabel || 'Download now';
  clearTimeout(psTimer);
  psTimer = 0;
  if (document.hidden) return;
  if (rp.busy) psPoll(1500);
  else if (psSent) psPoll(10000);
  else if (rp.state === 'missing') psPoll(20000);
}
async function loadPS() {
  const seq = ++psSeq;
  const res = await fetch('/api/remote-play', {headers: hdr(), cache: 'no-store'});
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  const s = await res.json();
  if (seq === psSeq) showPS(s);
}
loadPS().catch(() => { psStatusEl.textContent = 'Paste your Account-ID to save it on the handheld.'; });
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) loadPS().catch(() => {});
});
document.getElementById('psSignIn').onclick = () => { location.href = '/psn?p=' + encodeURIComponent(pin); };
document.getElementById('psAccount').addEventListener('input', () => psSay(''));
document.getElementById('sendPS').onclick = async () => {
  const input = document.getElementById('psAccount');
  const value = input.value.trim();
  if (!value) { psSay('Paste your Account-ID first, or tap Sign in to get it.', true); input.focus(); return; }
  const btn = document.getElementById('sendPS');
  btn.disabled = true; btn.textContent = 'Checking...';
  psSay('');
  try {
    const r = await api('/api/psn', {input: value}, true);
    input.value = '';
    psSent = psShort(r.account || '');
    // An answer asked for before this send is out of date now.
    psSeq++;
    psStatusEl.textContent = 'Sent ' + psSent + ' to the handheld.';
    toast('Account-ID sent');
    // The handheld saves it on its next frame and, when Remote Play is
    // missing and its background download is on, starts downloading it.
    setTimeout(() => { loadPS().catch(() => { if (!document.hidden) psPoll(5000); }); }, 900);
  } catch (err) {
    // The handheld's sentences are long: they stay under the field.
    psSay(err.message, true);
    toast('Not saved', true);
  } finally {
    btn.disabled = false; btn.textContent = 'Save on handheld';
  }
};
document.getElementById('psStart').onclick = async () => {
  const btn = document.getElementById('psStart');
  btn.disabled = true;
  psSay('');
  const seq = ++psSeq;
  try {
    const r = await api('/api/remote-play', {action: 'install'}, true);
    if (seq === psSeq) showPS(r);
    toast(r.message || 'Downloading on the handheld');
  } catch (err) {
    psSay(err.message, true);
    toast('Not started', true);
  } finally {
    btn.disabled = false;
  }
};
// The Files card (companion_files.go). Paths are the handheld's share paths
// ("/Roms/GBA"); "/" lists the shares. Uploads go one at a time through
// XMLHttpRequest, whose upload events drive the bar.
const filesMsgEl = document.getElementById('filesMsg');
const filesListEl = document.getElementById('filesList');
let filesPath = '/';
let filesData = null;
let filesBusy = false;
function filesSay(text, bad) { filesMsgEl.textContent = text || ''; filesMsgEl.className = bad ? 'msg bad' : 'msg'; }
function filesJoin(dir, name) { return dir === '/' ? '/' + name : dir + '/' + name; }
function filesSize(n) {
  if (n < 1024) return n + ' B';
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MB';
  return (n / 1073741824).toFixed(2) + ' GB';
}
function filesWhen(t) {
  if (!t) return '';
  try { return new Date(t * 1000).toLocaleString([], {year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit'}); } catch (e) { return ''; }
}
function filesBar(pct) {
  const bar = document.getElementById('filesBar');
  bar.classList.toggle('idle', pct < 0);
  document.getElementById('filesBarFill').style.width = (pct < 0 ? 0 : pct) + '%';
}
async function filesLoad(p) {
  const res = await fetch('/api/files?path=' + encodeURIComponent(p), {headers: hdr(), cache: 'no-store'});
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  filesData = await res.json();
  filesPath = filesData.path || '/';
  filesRender();
}
function filesOpen(p) {
  filesSay('');
  filesLoad(p).catch(e => filesSay(e.message, true));
}
function filesButton(label, cls, fn) {
  const b = document.createElement('button');
  b.type = 'button';
  b.textContent = label;
  if (cls) b.className = cls;
  b.onclick = fn;
  return b;
}
function filesRender() {
  filesSay('');
  const d = filesData || {entries: []};
  const crumbs = document.getElementById('filesCrumbs');
  crumbs.replaceChildren(filesButton('Shared folders', '', () => filesOpen('/')));
  let acc = '';
  for (const part of filesPath.split('/').filter(Boolean)) {
    acc += '/' + part;
    const target = acc;
    crumbs.append(document.createTextNode('/'), filesButton(part, '', () => filesOpen(target)));
  }
  for (const b of crumbs.querySelectorAll('button')) b.disabled = filesBusy;
  const canWrite = filesPath !== '/' && !d.readOnly;
  document.getElementById('filesUpload').disabled = !canWrite || filesBusy;
  document.getElementById('filesMkdir').disabled = !canWrite || filesBusy;
  if (filesPath === '/') filesSay('Open a folder to upload or make folders in it.');
  else if (d.readOnly) filesSay('This folder is read-only from the phone.');
  else if (d.truncated) filesSay('This folder holds more files than can be shown; the first ' + d.entries.length + ' are listed.');
  filesListEl.replaceChildren();
  if (!d.entries.length) {
    const empty = document.createElement('div');
    empty.className = 'fempty';
    empty.textContent = filesPath === '/' ? 'Nothing is shared: no user folders were found.' : 'This folder is empty.';
    filesListEl.append(empty);
    return;
  }
  for (const e of d.entries) {
    const full = filesJoin(filesPath, e.name);
    const row = document.createElement('div');
    row.className = 'frow';
    const name = document.createElement(e.isDir ? 'button' : 'div');
    name.className = e.isDir ? 'fname dir' : 'fname';
    const title = document.createElement('b');
    title.textContent = e.name;
    const meta = document.createElement('span');
    meta.textContent = [e.isDir ? 'Folder' : filesSize(e.size || 0), filesWhen(e.mtime), e.locked ? 'read-only' : ''].filter(Boolean).join(' · ');
    name.append(title, meta);
    if (e.isDir) { name.type = 'button'; name.disabled = filesBusy; name.onclick = () => filesOpen(full); }
    row.append(name);
    const acts = document.createElement('div');
    acts.className = 'facts';
    if (!e.isDir) acts.append(filesButton('Download', 'ghost', () => filesDownload(full, e.name)));
    if (filesPath !== '/' && !d.readOnly && !e.locked && !filesBusy) {
      acts.append(filesButton('Rename', 'ghost', () => filesRename(full, e.name)));
      acts.append(filesButton('Delete', 'ghost', () => filesDelete(full, e)));
    }
    if (acts.childNodes.length) row.append(acts);
    filesListEl.append(row);
  }
}
function filesDownload(full, name) {
  const a = document.createElement('a');
  a.href = '/api/files/download?p=' + encodeURIComponent(pin) + '&path=' + encodeURIComponent(full);
  a.download = name;
  a.rel = 'noopener';
  document.body.append(a);
  a.click();
  a.remove();
}
// A folder JukaHub depends on (a console's folder, BIOS, PortMaster...) is
// answered 409 with the reason; asked about, the request goes again with
// confirm (companion_guard.go). False when the user said no.
async function filesGuarded(path, body) {
  const send = (b) => fetch(path, {method: 'POST', headers: hdr({'Content-Type': 'application/json'}), body: JSON.stringify(b)});
  let res = await send(body);
  if (res.status === 409) {
    const text = await res.text();
    let j = null;
    try { j = JSON.parse(text); } catch (_) { j = null; }
    if (!j || !j.needsConfirm) throw new Error(text.trim() || res.statusText);
    if (!confirm(j.reason + '. ' + (j.ask || 'Go ahead?'))) return false;
    res = await send(Object.assign({}, body, {confirm: true}));
  }
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return true;
}
async function filesRename(full, old) {
  const next = prompt('New name for ' + old, old);
  if (next === null || next.trim() === '' || next.trim() === old) return;
  try {
    if (!await filesGuarded('/api/files/rename', {path: full, name: next.trim()})) return;
    toast('Renamed');
    await filesLoad(filesPath);
  } catch (e) { filesSay(e.message, true); }
}
async function filesDelete(full, e) {
  const ask = e.isDir
    ? 'Delete the folder "' + e.name + '"? Only an empty folder can be deleted.'
    : 'Delete "' + e.name + '" from the handheld? This cannot be undone.';
  if (!confirm(ask)) return;
  try {
    if (!await filesGuarded('/api/files/delete', {path: full})) return;
    toast('Deleted');
    await filesLoad(filesPath);
  } catch (err) { filesSay(err.message, true); }
}
document.getElementById('filesMkdir').onclick = async () => {
  const name = prompt('Name of the new folder');
  if (name === null || !name.trim()) return;
  try {
    await api('/api/files/mkdir', {path: filesPath, name: name.trim()}, true);
    toast('Folder created');
    await filesLoad(filesPath);
  } catch (e) { filesSay(e.message, true); }
};
function filesUploadOne(file, dir, replace, label) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/files/upload?path=' + encodeURIComponent(dir) + (replace ? '&replace=1' : ''));
    xhr.setRequestHeader('X-Companion-Pin', pin);
    xhr.upload.onprogress = ev => {
      if (!ev.lengthComputable) return;
      const pct = Math.floor(ev.loaded * 100 / ev.total);
      filesBar(pct);
      filesSay(label + ': ' + pct + '%');
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) { resolve(); return; }
      const err = new Error((xhr.responseText || '').trim() || ('Error ' + xhr.status));
      err.status = xhr.status;
      reject(err);
    };
    xhr.onerror = () => reject(new Error('The connection to the handheld was lost.'));
    const fd = new FormData();
    fd.append('file', file);
    xhr.send(fd);
  });
}
document.getElementById('filesUpload').onclick = () => document.getElementById('filesPick').click();
async function filesUploadSelection(selection) {
  const files = Array.from(selection || []);
  if (!files.length) return;
  if (filesPath === '/' || !filesData || filesData.readOnly) {
    filesSay('Open a writable folder before dropping files.', true);
    return;
  }
  const dir = filesPath;
  const max = (filesData && filesData.maxUpload) || 536870912;
  const have = new Map(((filesData && filesData.entries) || []).map(x => [x.name.toLowerCase(), x]));
  filesBusy = true;
  filesRender();
  let done = 0;
  let failed = '';
  try {
    for (let i = 0; i < files.length; i++) {
      const f = files[i];
      const label = 'Uploading ' + f.name + (files.length > 1 ? ' (' + (i + 1) + ' of ' + files.length + ')' : '');
      // The multipart wrapping counts against the server's limit too.
      if (f.size > max - 4096) { failed = f.name + ' is larger than ' + filesSize(max) + '.'; continue; }
      const same = have.get(f.name.toLowerCase());
      if (same && same.isDir) { failed = 'A folder named ' + f.name + ' is already here.'; continue; }
      if (same && !confirm(f.name + ' is already here. Replace it?')) continue;
      filesSay(label + '...');
      filesBar(0);
      try {
        try {
          await filesUploadOne(f, dir, !!same, label);
        } catch (e) {
          // The handheld may store the name differently (cleaned of
          // characters the card cannot hold) and find that one already there.
          const msg = e.message || '';
          const tail = ' is already here.';
          if (same || e.status !== 409 || !msg.endsWith(tail) || msg.startsWith('A folder named ')) throw e;
          if (!confirm('Replace ' + msg.slice(0, -tail.length) + '?')) continue;
          filesBar(0);
          await filesUploadOne(f, dir, true, label);
        }
        done++;
      } catch (e) { failed = f.name + ': ' + e.message; }
    }
  } finally {
    filesBusy = false;
    filesBar(-1);
  }
  if (done) toast(done === 1 ? 'Uploaded' : 'Uploaded ' + done + ' files');
  try { await filesLoad(filesPath); } catch (e) { failed = failed || e.message; }
  if (failed) filesSay(failed, true);
  else if (done) filesSay('Uploaded ' + done + (done === 1 ? ' file.' : ' files.'));
}
document.getElementById('filesPick').onchange = async ev => {
  await filesUploadSelection(ev.target.files);
  ev.target.value = '';
};
const filesDropEl = document.getElementById('filesDrop');
for (const eventName of ['dragenter', 'dragover']) {
  filesDropEl.addEventListener(eventName, ev => {
    ev.preventDefault();
    ev.dataTransfer.dropEffect = 'copy';
    filesDropEl.classList.add('active');
  });
}
filesDropEl.addEventListener('dragleave', () => filesDropEl.classList.remove('active'));
filesDropEl.addEventListener('drop', ev => {
  ev.preventDefault();
  filesDropEl.classList.remove('active');
  filesUploadSelection(ev.dataTransfer.files);
});
filesOpen('/');

// The Tasks card (companion_tasks.go): the handheld's lists and the chosen
// list's tasks. A task typed here is added there at once; the tick and
// Delete change it there too. Dates come back written the handheld's way.
let tkData = null;
function tkEl(id) { return document.getElementById(id); }
function tkSay(msg, bad) {
  const el = tkEl('tkMsg');
  el.textContent = msg || '';
  el.className = bad ? 'msg bad' : 'msg';
}
function tkShow(s) {
  if (!s || !Array.isArray(s.lists)) return;
  tkData = s;
  const sel = tkEl('tkList');
  const keep = Number(sel.value) || (s.lists[0] ? s.lists[0].id : 0);
  sel.replaceChildren();
  for (const l of s.lists) {
    const o = document.createElement('option');
    o.value = String(l.id);
    o.textContent = l.name + (l.open ? ' (' + l.open + ')' : '');
    sel.append(o);
  }
  if (s.lists.some(l => l.id === keep)) sel.value = String(keep);
  tkRender();
}
function tkRender() {
  const s = tkData;
  if (!s) return;
  const list = Number(tkEl('tkList').value);
  const items = (s.items || []).filter(t => t.list === list);
  const total = s.lists.reduce((n, l) => n + (l.open || 0), 0);
  tkEl('tkStatus').textContent = total
    ? total + (total === 1 ? ' open task' : ' open tasks') + ' on the handheld.'
    : 'Nothing open on the handheld.';
  const box = tkEl('tkItems');
  box.replaceChildren();
  box.hidden = !items.length;
  for (const t of items) {
    const row = document.createElement('div');
    row.className = t.done ? 'tkrow done' : 'tkrow';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!t.done;
    cb.setAttribute('aria-label', t.done ? 'Not done' : 'Done');
    cb.onchange = () => tkPost({action: 'toggle', id: t.id});
    const name = document.createElement('div');
    name.className = 'fname';
    const b = document.createElement('b');
    b.textContent = t.text;
    name.append(b);
    const parts = [];
    if (t.label) parts.push((t.overdue ? 'Overdue · ' : '') + t.label);
    if (t.repeat === 'daily') parts.push('Every day');
    if (t.repeat === 'weekly') parts.push('Every week');
    if (t.alarm) parts.push('Alarm');
    else if (t.remind) parts.push('Reminder');
    if (parts.length) {
      const sub = document.createElement('span');
      sub.textContent = parts.join(' · ');
      if (t.overdue) sub.className = 'late';
      name.append(sub);
    }
    name.onclick = () => { cb.checked = !cb.checked; tkPost({action: 'toggle', id: t.id}); };
    const acts = document.createElement('div');
    acts.className = 'facts';
    const rm = document.createElement('button');
    rm.type = 'button';
    rm.className = 'ghost';
    rm.textContent = 'Delete';
    rm.onclick = () => { if (confirm('Delete "' + t.text + '" from the handheld?')) tkPost({action: 'delete', id: t.id}); };
    acts.append(rm);
    row.append(cb, name, acts);
    box.append(row);
  }
}
function tkLoad() {
  return fetch('/api/tasks', {headers: hdr(), cache: 'no-store'})
    .then(res => res.ok ? res.json() : Promise.reject(new Error(res.statusText)))
    .then(tkShow);
}
async function tkPost(body) {
  try {
    const r = await api('/api/tasks', body, true);
    tkShow(r);
    tkSay('');
    if (r.message) toast(r.message);
    return r;
  } catch (err) {
    tkSay(err.message, true);
    toast('Not changed', true);
    tkLoad().catch(() => {});
    return null;
  }
}
tkEl('tkList').onchange = tkRender;
tkEl('tkAdd').onclick = async () => {
  const text = tkEl('tkText');
  if (!text.value.trim()) { tkSay('Type the task first', true); text.focus(); return; }
  const btn = tkEl('tkAdd');
  btn.disabled = true;
  const r = await tkPost({action: 'add', text: text.value.trim(), list: Number(tkEl('tkList').value) || 0,
    date: tkEl('tkDate').value, time: tkEl('tkTime').value});
  btn.disabled = false;
  if (r) { text.value = ''; tkEl('tkTime').value = ''; text.focus(); }
};
tkEl('tkText').addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); tkEl('tkAdd').click(); }
});
tkEl('tkNewList').onclick = async () => {
  const name = prompt('Name of the new list');
  if (!name || !name.trim()) return;
  const r = await tkPost({action: 'addList', name: name.trim()});
  if (r && r.added) { tkEl('tkList').value = String(r.added); tkRender(); }
};
tkLoad().catch(() => { tkEl('tkStatus').textContent = 'Type a task to add it on the handheld.'; });
document.addEventListener('visibilitychange', () => { if (!document.hidden) tkLoad().catch(() => {}); });

stateEl.textContent = pin ? 'paired' : 'no PIN';
// A code on the handheld that asks for one card ("#groq", "#vpn") opens the
// page there: the card moves to the top under the screen bar, ringed, with the
// cursor in its field, so the paste goes straight in (companion_phonecard.go).
// A second code scanned into the same tab only changes the part after "#", so
// the page listens for that too.
function pickCard() {
  const want = (location.hash || '').slice(1).toLowerCase();
  if (!/^[a-z]{1,16}$/.test(want)) return;
  const card = document.getElementById(want + 'Card');
  const bar = document.getElementById('top');
  if (!card || !bar || !card.classList.contains('card')) return;
  document.querySelectorAll('.card.picked').forEach(c => { if (c !== card) c.classList.remove('picked'); });
  bar.after(card);
  card.classList.add('picked');
  window.scrollTo(0, 0);
  const field = card.querySelector('input[type=password], input[type=text], input[type=url], textarea');
  if (field) {
    try { field.focus({preventScroll: true}); } catch (e) { field.focus(); }
  }
}
pickCard();
window.addEventListener('hashchange', pickCard);
</script>
</html>`
