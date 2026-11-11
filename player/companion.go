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
	// companionMaxUpload bounds a single phone upload. The SD card is small;
	// half a gigabyte is already a feature film.
	companionMaxUpload = 512 << 20
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
	return fmt.Sprintf("http://%s/?p=%s", net.JoinHostPort(ip, strconv.Itoa(port)), pin)
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

	mu              sync.Mutex
	ln              net.Listener
	srv             *http.Server
	port            int
	fails           int
	failSince       time.Time
	lockouts        int
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
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Keep-alive connections a client stopped using are dropped instead of
		// being held for good: any phone on the network could otherwise pile
		// them up until the handheld ran out of file descriptors.
		IdleTimeout: 2 * time.Minute,
	}
	s.mu.Lock()
	s.ln = ln
	s.srv = srv
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.mu.Unlock()
	go func() {
		// A closed listener is the normal shutdown path; anything else is worth
		// a line in the log, since the companion silently stopped answering
		// phones before.
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logWarn("companion", "server stopped: %v", err)
		}
	}()
	return nil
}

func (s *companionServer) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
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

func (s *companionServer) URL() string {
	return companionURL(localIPv4(), s.Port(), s.pin)
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
func (s *companionServer) throttled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails < companionFailLimit {
		return false
	}
	window := companionFailWindow << min(s.lockouts, 7)
	if time.Since(s.failSince) > window {
		s.fails = 0
		s.lockouts++
		return false
	}
	return true
}

func (s *companionServer) noteFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails == 0 {
		s.failSince = time.Now()
	}
	s.fails++
}

// noteSuccess clears the lockout escalation once the right PIN arrives, so a
// phone that mistyped a few times is not slowed down for the session.
func (s *companionServer) noteSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails == 0 && s.lockouts == 0 {
		return
	}
	s.fails = 0
	s.lockouts = 0
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
		if s.throttled() {
			http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
			return
		}
		if !s.authorized(r) {
			s.noteFailure()
			http.Error(w, "bad PIN", http.StatusUnauthorized)
			return
		}
		s.noteSuccess()
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
	if s.throttled() {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	if !s.authorized(r) {
		s.noteFailure()
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
h1{font-size:20px;margin:0 0 8px}p{color:#8b93a7;line-height:1.5;margin:0}</style>
<div class=card><h1>Waiting for the PIN</h1>
<p>Scan the QR code shown on the handheld. The link carries the pairing PIN; this page cannot be opened without it.</p></div>`

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
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
/* A shorthand cannot take "inherit" for the family, so the fonts are named;
   16 px in the fields keeps iOS from zooming in on focus. */
textarea,input[type=url],input[type=password],input[type=text]{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif;resize:vertical}
textarea{min-height:88px}
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
.bar{height:6px;margin-top:10px;background:#0f131b;border:1px solid var(--line);border-radius:99px;overflow:hidden}
.bar>span{display:block;height:100%;width:0;background:var(--accent);transition:width .4s}
.bar.idle{display:none}
.bar.pulse>span{animation:pulse 1.2s ease-in-out infinite alternate}
@keyframes pulse{from{opacity:.25}to{opacity:.8}}
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

<div class=card>
  <h2>Type</h2>
  <textarea id=text placeholder="Type here, it appears on the handheld"></textarea>
  <label class=check><input type=checkbox id=enter checked>Press Enter after sending</label>
  <div class=row><button id=sendText>Send text</button><button class=ghost id=clearText>Clear</button></div>
</div>

<div class=card>
  <h2>Voice</h2>
  <div class=row><button id=voicePage>Say what to open</button></div>
  <small>Your keyboard's microphone sends commands like "open YouTube" or "volume 50".</small>
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

<div class=card>
  <h2>Send a file</h2>
  <input type=file id=file>
  <div class=row><button id=sendFile>Upload</button></div>
  <small id=fileName>Files land in the PhoneDrop folder on the SD card.</small>
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

<div class=card>
  <h2>Groq API key</h2>
  <p class=note id=groqStatus>Checking the handheld...</p>
  <input type=password id=groqKey placeholder="gsk_..." autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label class=check><input type=checkbox id=groqShow>Show the key</label>
  <div class=row><button id=sendGroq>Save on handheld</button></div>
  <small id=groqNote>Voice typing, voice commands and the Juka chat use it. Get a free key at console.groq.com (API Keys), copy it and paste it here. The handheld checks it with Groq and never shows it again.</small>
</div>

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
document.getElementById('file').onchange = e => {
  const f = e.target.files[0];
  document.getElementById('fileName').textContent = f ? f.name + ' - ' + (f.size / 1048576).toFixed(1) + ' MB' : '';
};
document.getElementById('sendFile').onclick = async () => {
  const input = document.getElementById('file');
  if (!input.files.length) { toast('Pick a file first', true); return; }
  const fd = new FormData();
  fd.append('file', input.files[0]);
  const btn = document.getElementById('sendFile');
  btn.disabled = true; btn.textContent = 'Uploading...';
  try {
    await api('/api/upload', fd, false);
    toast('Uploaded');
    refreshSoon();
    input.value = '';
    document.getElementById('fileName').textContent = 'Files land in the PhoneDrop folder on the SD card.';
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Upload';
  }
};
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
stateEl.textContent = pin ? 'paired' : 'no PIN';
</script>
</html>`
