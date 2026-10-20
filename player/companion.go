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
	"encoding/json"
	"fmt"
	"io"
	"math/big"
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

func companionURL(ip string, port int, pin string) string {
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

	mu        sync.Mutex
	ln        net.Listener
	srv       *http.Server
	port      int
	fails     int
	failSince time.Time
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
	}
	s.mu.Lock()
	s.ln = ln
	s.srv = srv
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			// A closed listener is the normal shutdown path.
		}
	}()
	return nil
}

func (s *companionServer) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.ln = nil
	s.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
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
func (s *companionServer) throttled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails < companionFailLimit {
		return false
	}
	if time.Since(s.failSince) > companionFailWindow {
		s.fails = 0
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

func (s *companionServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/api/text", s.guard(s.handleText))
	mux.HandleFunc("/api/key", s.guard(s.handleKey))
	mux.HandleFunc("/api/open", s.guard(s.handleOpenURL))
	mux.HandleFunc("/api/upload", s.guard(s.handleUpload))
	mux.HandleFunc("/api/screen", s.guard(s.handleScreen))
	return mux
}

func (s *companionServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.throttled() {
			http.Error(w, "too many attempts, wait half a minute", http.StatusTooManyRequests)
			return
		}
		if !s.authorized(r) {
			s.noteFailure()
			http.Error(w, "bad PIN", http.StatusUnauthorized)
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
	if s.throttled() {
		http.Error(w, "too many attempts, wait half a minute", http.StatusTooManyRequests)
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
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusRequestEntityTooLarge)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	dir, err := companionDropDirFor(s.root)
	if err != nil {
		http.Error(w, "cannot open drop folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	name := sanitizeUploadName(header.Filename)
	out, saved, err := createCompanionFile(dir, name)
	if err != nil {
		http.Error(w, "cannot save file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	written, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(filepath.Join(dir, saved))
		http.Error(w, "upload interrupted", http.StatusInternalServerError)
		return
	}
	s.sink(companionEvent{Kind: companionEventUpload, Text: saved})
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
body{padding:16px 16px 40px;max-width:560px;margin:0 auto}
header{display:flex;align-items:baseline;justify-content:space-between;margin:8px 0 16px}
h1{font-size:20px;margin:0}
#state{font-size:12px;color:var(--dim)}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin-bottom:14px}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
textarea,input[type=url]{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:15px/1.4 inherit;resize:vertical}
textarea{min-height:88px}
button{font:600 15px inherit;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
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
</style>
<header><h1>JukaHub</h1><span id=state>connecting</span></header>

<div class=card>
  <h2>Type</h2>
  <textarea id=text placeholder="Type here, it appears on the handheld"></textarea>
  <label class=check><input type=checkbox id=enter checked>Press Enter after sending</label>
  <div class=row><button id=sendText>Send text</button><button class=ghost id=clearText>Clear</button></div>
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

<div class=card>
  <h2>Screen</h2>
  <img id=screen alt="Handheld screen" style="width:100%;border-radius:10px;background:#0f131b;display:block">
  <div class=row><button id=shotScreen>Refresh</button><button class=ghost id=autoScreen>Auto: off</button></div>
  <small>The image is captured on the handheld, not mirrored from a stream.</small>
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
}
document.getElementById('sendText').onclick = () => sendText().catch(e => toast(e.message, true));
document.getElementById('clearText').onclick = () => { document.getElementById('text').value = ''; document.getElementById('text').focus(); };
document.querySelectorAll('[data-key]').forEach(btn => {
  btn.onclick = () => api('/api/key', {key: btn.dataset.key}, true).catch(e => toast(e.message, true));
});
document.getElementById('sendURL').onclick = () => {
  const el = document.getElementById('url');
  const url = el.value.trim();
  if (!url) return;
  api('/api/open', {url}, true).then(() => { toast('Opening on handheld'); el.value = ''; })
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
    input.value = '';
    document.getElementById('fileName').textContent = 'Files land in the PhoneDrop folder on the SD card.';
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false; btn.textContent = 'Upload';
  }
};
const screenEl = document.getElementById('screen');
let shotURL = '';
let autoTimer = 0;
async function loadScreen() {
  const res = await fetch('/api/screen', {headers: hdr()});
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  const blob = await res.blob();
  const next = URL.createObjectURL(blob);
  screenEl.src = next;
  if (shotURL) URL.revokeObjectURL(shotURL);
  shotURL = next;
}
document.getElementById('shotScreen').onclick = () => loadScreen().catch(e => toast(e.message, true));
document.getElementById('autoScreen').onclick = e => {
  clearInterval(autoTimer);
  if (autoTimer) {
    autoTimer = 0;
    e.target.textContent = 'Auto: off';
    return;
  }
  e.target.textContent = 'Auto: on';
  autoTimer = setInterval(() => { loadScreen().catch(() => {}); }, 1200);
};
loadScreen().catch(() => {});
stateEl.textContent = pin ? 'paired' : 'no PIN';
</script>
</html>`
