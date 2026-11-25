package main

// companion_cast.go — Screen Share: the handheld's screen in any browser.
//
// The Smart Pro has no video out, so a browser is the TV cable: /watch is a
// page any phone, laptop or smart TV on the same network can open, /stream an
// MJPEG feed of the handheld's screen (games included, castcap.go), /frame.jpg
// the newest single frame for browsers that cannot show MJPEG, and /status the
// state the page shows. Sharing is off until it is switched on in Start →
// Screen Share.
//
// The link is view-only. It carries a share token of its own, not the Phone
// Companion PIN: the PIN also unlocks the keyboard, the remote and uploads, so
// a link handed to a friend or typed into a TV used to hand over the handheld's
// input too. The PIN is still accepted (the phone page can show the stream),
// and a new link (Y in the panel) retires the old token without touching it.
// The token is kept in the user config, so a TV's bookmarked link survives a
// restart until Y replaces it.
//
// SDL stays on the render thread: the handlers only wait on frames that the
// capture publishes, so a browser can never touch the renderer.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// castBoundary separates MJPEG parts. It has to be long enough that no
	// JPEG payload can contain it.
	castBoundary = "jukahub-frame-boundary"
	// castFrameWait bounds one stream iteration's wait for a fresh frame.
	castFrameWait = 400 * time.Millisecond
	// castPollWait bounds a single-frame request's wait for a frame newer than
	// the one the browser already has.
	castPollWait = 1500 * time.Millisecond
	// castMaxViewers keeps a classroom from turning the handheld into a video
	// server: each viewer needs its own socket and kernel buffer.
	castMaxViewers = 6
	// castWriteTimeout drops a viewer whose network cannot take one frame in
	// this long, so a TV on a dying Wi-Fi link does not hold a slot for ever.
	castWriteTimeout = 5 * time.Second
	// castShotToken is the stand-in token screenshot mode advertises: a QR in
	// a published picture must not open the real share.
	castShotToken = "jukahubsharedemo"
)

var (
	// castEnabled gates /stream and /frame.jpg. Off by default: broadcasting
	// the screen should be a decision, not something a background server does.
	castEnabled atomic.Bool
	castViewers atomic.Int32
)

// ── The share token ───────────────────────────────────────────────────────

// castTokenKey is where the token is kept in the user config, so a TV's
// bookmarked link survives a restart. Its name marks it a secret for config
// exports (cfgdocIsSecretKey matches "token").
const castTokenKey = "castToken"

// castToken is the view-only secret in the watch link.
var castToken struct {
	mu    sync.Mutex
	value string
}

// castTokenLoaded is set once the saved token has been looked for (the first
// frame does it, castTokenLoad). Until then a link cannot be judged: the
// server may answer a TV a moment before the token is restored, and calling
// that link dead would stop the TV's page for good.
var castTokenLoaded atomic.Bool

// castNewToken is 80 random bits in lowercase base32: 16 characters, short
// enough that the watch link's QR stays small and a TV remote can type it.
func castNewToken() string {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand does not fail on any platform JukaHub runs on; a share
		// without a token is still refused, never opened to everyone.
		return ""
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
}

// castTokenValid reports whether s has the shape castNewToken makes, so a
// hand-edited config cannot shorten the secret.
func castTokenValid(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// castTokenLoad restores the saved token, once. Main thread: it reads the
// config. Without a saved one the token is made on first use.
func castTokenLoad(config *Config) {
	if config == nil || castTokenLoaded.Load() {
		return
	}
	saved := strings.TrimSpace(customStringValue(config.Variables.Custom, castTokenKey))
	castToken.mu.Lock()
	if castToken.value == "" && castTokenValid(saved) {
		castToken.value = saved
	}
	castToken.mu.Unlock()
	castTokenLoaded.Store(true)
}

// castShareToken is the current token, made on first use.
func castShareToken() string {
	castToken.mu.Lock()
	defer castToken.mu.Unlock()
	if castToken.value == "" {
		castToken.value = castNewToken()
	}
	return castToken.value
}

// castEnsureToken is castShareToken, saved to the user config when it is not
// there yet (a new token, or a new link from Y). Main thread: it writes the
// config.
func castEnsureToken(config *Config) string {
	castTokenLoad(config)
	tok := castShareToken()
	if config != nil && tok != "" && customStringValue(config.Variables.Custom, castTokenKey) != tok {
		setStoreString(config, castTokenKey, tok)
	}
	return tok
}

// castRotateToken retires the current token and returns its replacement. Every
// browser holding the old link is refused from its next request, and open
// streams end before their next frame. The caller saves it (castEnsureToken).
func castRotateToken() string {
	castToken.mu.Lock()
	defer castToken.mu.Unlock()
	castToken.value = castNewToken()
	return castToken.value
}

// castTokenCheck compares a link's token with the current one in constant
// time. ready is false while the saved token has not been restored yet, when
// no answer about the link can be given.
func castTokenCheck(got string) (ok, ready bool) {
	castToken.mu.Lock()
	want := castToken.value
	castToken.mu.Unlock()
	if want == "" {
		return false, castTokenLoaded.Load()
	}
	if len(got) != len(want) {
		return false, true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1, true
}

// castTokenMatches reports whether got is the current token.
func castTokenMatches(got string) bool {
	ok, _ := castTokenCheck(got)
	return ok
}

// castWatchURL is the shareable address: the token is all a viewer needs.
func castWatchURL(ip string, port int, token string) string {
	if ip == "" || port <= 0 || token == "" {
		return ""
	}
	return fmt.Sprintf("http://%s/watch?c=%s", net.JoinHostPort(ip, strconv.Itoa(port)), url.QueryEscape(token))
}

// castLinkToken is the token the panel shows: the stand-in in screenshot mode.
// Main thread (castEnsureToken saves a new token).
func castLinkToken() string {
	if shotModeOn() {
		return castShotToken
	}
	return castEnsureToken(appConfig)
}

// ── On, off, viewers ──────────────────────────────────────────────────────

// castSetEnabled switches the share on or off. Turning it off disconnects the
// streams at their next frame, because the handlers re-check the flag, and
// stops the game reader. Main thread: it publishes the settings from the
// config first.
func castSetEnabled(on bool) {
	if on {
		castSyncSettings(appConfig)
		castEnsureToken(appConfig)
		castUIStart()
		castEnabled.Store(true)
		castSupervisorStart()
		castAudioRefresh()
		if castLive.sound.Load() {
			// Games started from now on can send their sound once the route
			// is proven (castaudio_alsa.go).
			castTapProbeKick()
		}
		return
	}
	castEnabled.Store(false)
	castSupervisorStop()
	castLive.source.Store(castSourceOff)
	castAudioRefresh()
	castInputRevoke("Screen Share was switched off")
	castPlayCloseAll()
}

func castIsEnabled() bool { return castEnabled.Load() }

// castViewerCount is the number of open streams.
func castViewerCount() int { return int(castViewers.Load()) }

// castAddViewer reserves a stream slot for the browser at addr. It returns
// false when sharing is off or the share is full. Streams and browsers polling
// single frames share castMaxViewers; addr's own polling entry does not count,
// so a page going back from single frames to the stream is not refused by its
// own footprint.
func castAddViewer(addr string) bool {
	if !castEnabled.Load() {
		return false
	}
	polling := castPollerCountExcept(castHost(addr))
	for {
		n := castViewers.Load()
		if int(n)+polling >= castMaxViewers {
			return false
		}
		if castViewers.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func castRemoveViewer() {
	for {
		n := castViewers.Load()
		if n <= 0 || castViewers.CompareAndSwap(n, n-1) {
			return
		}
	}
}

// castPollWindow is how long one single-frame request counts as someone
// watching. A browser that cannot show the MJPEG stream asks for frames one by
// one, and the capture has to keep running for it between requests.
const castPollWindow = 3 * time.Second

// castPollers are the browsers watching through single frames, by address.
var castPollers struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// castHost is the address part of a request's RemoteAddr: one browser keeps it
// across the connections it opens.
func castHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// castNotePoll records a single-frame request from addr. It reports false
// when the share is full: polling is not a way around castMaxViewers.
func castNotePoll(addr string) bool {
	host := castHost(addr)
	now := time.Now()
	castPollers.mu.Lock()
	defer castPollers.mu.Unlock()
	if castPollers.seen == nil {
		castPollers.seen = make(map[string]time.Time)
	}
	for k, at := range castPollers.seen {
		if now.Sub(at) >= castPollWindow {
			delete(castPollers.seen, k)
		}
	}
	if _, known := castPollers.seen[host]; !known && len(castPollers.seen)+castViewerCount() >= castMaxViewers {
		return false
	}
	castPollers.seen[host] = now
	return true
}

// castPollerCount is how many browsers polled for frames recently. The render
// loop asks every frame, so it only counts; castNotePoll does the pruning.
func castPollerCount() int {
	return castPollerCountExcept("")
}

// castPollerCountExcept is castPollerCount without the browser at host.
func castPollerCountExcept(host string) int {
	now := time.Now()
	castPollers.mu.Lock()
	defer castPollers.mu.Unlock()
	n := 0
	for k, at := range castPollers.seen {
		if k != host && now.Sub(at) < castPollWindow {
			n++
		}
	}
	return n
}

// ── Authorisation ─────────────────────────────────────────────────────────

// castCredential is how a share request proved itself: the link's token (c=)
// or the companion PIN. It is kept so a stream can notice the token being
// retired under it.
type castCredential struct {
	token string // set for a link; "" when the PIN was used
	pin   string
}

// query is the credential as the page's own requests send it.
func (c castCredential) query() string {
	if c.token != "" {
		return "c=" + url.QueryEscape(c.token)
	}
	return "p=" + url.QueryEscape(c.pin)
}

// stillValid reports whether a token credential is still the current one. A
// PIN credential lasts as long as the server that checked it.
func (c castCredential) stillValid() bool {
	return c.token == "" || castTokenMatches(c.token)
}

// castGuard admits a share request carrying the current token or the PIN.
//
// A valid token never resets the PIN's failure count: a viewer holding a watch
// link could otherwise interleave PIN guesses with token requests and never
// reach the lockout, and the PIN is what unlocks the keyboard. A wrong token
// counts as a failure like a wrong PIN.
func (s *companionServer) castGuard(next func(http.ResponseWriter, *http.Request, castCredential)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr := castHost(r.RemoteAddr)
		if token := r.URL.Query().Get("c"); token != "" {
			ok, ready := castTokenCheck(token)
			switch {
			case ok:
				next(w, r, castCredential{token: token})
			case !ready:
				// The saved token comes back with the first frame. A TV that
				// asks a moment earlier tries again instead of being told its
				// link is dead; there is no token yet that a guess could hit.
				http.Error(w, "Screen Share is starting", http.StatusServiceUnavailable)
			default:
				s.noteFailure(addr, time.Now())
				http.Error(w, "this Screen Share link no longer works", http.StatusUnauthorized)
			}
			return
		}
		if s.throttled(addr, time.Now()) {
			http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
			return
		}
		if !s.authorized(r) {
			s.noteFailure(addr, time.Now())
			http.Error(w, "bad PIN", http.StatusUnauthorized)
			return
		}
		s.noteSuccess(addr)
		pin := r.Header.Get("X-Companion-Pin")
		if pin == "" {
			pin = r.URL.Query().Get("p")
		}
		next(w, r, castCredential{pin: pin})
	}
}

// castNoStore marks a response as private to this moment.
func castNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// ── /watch ────────────────────────────────────────────────────────────────

// handleWatch serves the viewer page. It is deliberately separate from the
// phone-companion page: a viewer gets the picture, the sound when it asks for
// it, and — only after the handheld allows it, or with the Phone Companion's
// PIN — the pad (castinput.go); no keyboard text, remote or upload surface. It
// opens even while sharing is off, so a TV can be set up first and starts
// showing the handheld when sharing is switched on.
func (s *companionServer) handleWatch(w http.ResponseWriter, r *http.Request, cred castCredential) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	castNoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The /play socket is this host's too; older browsers do not count ws:
	// as 'self', so it is named.
	connect := "'self'"
	if castHostHeaderOK(r.Host) {
		connect += " ws://" + r.Host
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' blob: data:; "+
		"style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src "+connect+"; "+
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	tv := r.URL.Query().Get("tv") == "1"
	_, _ = w.Write(castWatchPage(cred.query(), tv))
}

// castHostHeaderOK reports whether a Host header can be named in the page's
// policy as it is: a name or an address and a port, nothing else.
func castHostHeaderOK(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(".-:[]", c) >= 0) {
			return false
		}
	}
	return true
}

// castWatchPage is the whole viewer: the picture, a status line, and buttons
// for sound, playing, full screen and fit, which hide after three idle
// seconds; the touch pad while playing on a touch screen. ?tv=1 hides the
// buttons for good for a TV browser driven by a remote (its first key turns
// the sound on).
func castWatchPage(cred string, tv bool) []byte {
	bodyClass := "fit"
	if tv {
		bodyClass += " tv"
	}
	// cred is "c=<base32>" or "p=<digits>" with the value query-escaped, so it
	// holds no quote, backslash or angle bracket: safe inside the JS string.
	page := strings.NewReplacer("{{CRED}}", cred, "{{BODYCLASS}}", bodyClass).Replace(castWatchHTML)
	return []byte(page)
}

const castWatchHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="theme-color" content="#05070c">
<title>JukaHub — Screen Share</title>
<style>
:root { color-scheme: dark; --ink: #e6e8ef; --dim: #8b93a7; --glass: rgba(255,255,255,.08);
        --edge: rgba(255,255,255,.16); --live: #f0788a; --on: #55d8ff; }
* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; background: #05070c; color: var(--ink); overflow: hidden;
             font: 14px/1.4 system-ui, -apple-system, "Segoe UI", sans-serif; }
#stage { position: fixed; top: 0; right: 0; bottom: 0; left: 0; display: flex;
         align-items: center; justify-content: center; touch-action: manipulation; }
#screen { display: block; width: 100%; height: 100%; object-fit: contain; }
body.fill #screen { object-fit: cover; }
body.pixel #screen { width: auto; height: auto; object-fit: fill;
                     image-rendering: crisp-edges; image-rendering: pixelated; }
.bar { position: fixed; left: 12px; right: 12px; display: flex; align-items: center; gap: 10px;
       padding: 8px 12px; background: rgba(11,15,23,.74); border: 1px solid var(--edge);
       border-radius: 14px; transition: opacity .3s; }
#top { top: 12px; flex-wrap: wrap; }
#bottom { bottom: 12px; justify-content: center; color: var(--dim); font-size: 12px; text-align: center; flex-wrap: wrap; }
body.idle .bar, body.tv .bar { opacity: 0; pointer-events: none; }
body.idle, body.tv { cursor: none; }
.dot { width: 9px; height: 9px; border-radius: 50%; background: var(--dim); flex: none; }
body.live .dot { background: var(--live); box-shadow: 0 0 0 4px rgba(240,120,138,.25); }
#state { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
button { font: 600 13px system-ui, -apple-system, "Segoe UI", sans-serif; color: var(--ink);
         background: var(--glass); border: 1px solid var(--edge); border-radius: 10px;
         padding: 8px 12px; cursor: pointer; }
button:focus-visible { outline: 2px solid var(--on); outline-offset: 2px; }
button.on { background: rgba(85,216,255,.18); border-color: rgba(85,216,255,.6); }
button[disabled] { opacity: .45; cursor: default; }
[hidden] { display: none !important; }
#bottom button { padding: 4px 9px; font-size: 12px; }
#msg { position: fixed; left: 50%; top: 50%; transform: translate(-50%, -50%); display: none;
       max-width: 86vw; width: 520px; text-align: center; padding: 18px 22px; line-height: 1.5;
       background: rgba(11,15,23,.86); border: 1px solid var(--edge); border-radius: 16px; }
#msg.show { display: block; }
#toast { position: fixed; left: 50%; bottom: 72px; transform: translateX(-50%); display: none;
         max-width: 86vw; width: max-content; text-align: center; padding: 10px 16px; line-height: 1.45;
         background: rgba(11,15,23,.9); border: 1px solid var(--edge); border-radius: 12px; }
#toast.show { display: block; }
#pad { position: fixed; top: 0; right: 0; bottom: 0; left: 0; display: none; pointer-events: none;
       user-select: none; -webkit-user-select: none; -webkit-touch-callout: none; }
body.playing.touch #pad { display: block; }
body.playing.touch.padoff #pad { display: none; }
.pb { position: absolute; pointer-events: auto; touch-action: none; display: flex; align-items: center;
      justify-content: center; width: 15vmin; height: 15vmin; border-radius: 50%;
      background: rgba(255,255,255,.10); border: 1px solid rgba(255,255,255,.28);
      color: rgba(255,255,255,.85); font: 700 4.2vmin system-ui, sans-serif; }
.pb.sq { border-radius: 3vmin; }
.pb.pill { width: 13vmin; height: 7vmin; border-radius: 4vmin; font-size: 3vmin; }
.pb.down { background: rgba(85,216,255,.42); border-color: rgba(85,216,255,.9); }
#pUp { left: calc(4vmin + 15vmin); bottom: calc(6vmin + 30vmin); }
#pDown { left: calc(4vmin + 15vmin); bottom: 6vmin; }
#pLeft { left: 4vmin; bottom: calc(6vmin + 15vmin); }
#pRight { left: calc(4vmin + 30vmin); bottom: calc(6vmin + 15vmin); }
#pX { right: calc(4vmin + 15vmin); bottom: calc(6vmin + 30vmin); }
#pB { right: calc(4vmin + 15vmin); bottom: 6vmin; }
#pY { right: calc(4vmin + 30vmin); bottom: calc(6vmin + 15vmin); }
#pA { right: 4vmin; bottom: calc(6vmin + 15vmin); }
#pL1 { left: 4vmin; top: 12vmin; }
#pL2 { left: 4vmin; top: 22vmin; }
#pR1 { right: 4vmin; top: 12vmin; }
#pR2 { right: 4vmin; top: 22vmin; }
#pSelect { left: calc(50% - 21vmin); bottom: 4vmin; }
#pMenu { left: calc(50% - 6.5vmin); bottom: 4vmin; }
#pStart { left: calc(50% + 8vmin); bottom: 4vmin; }
#pHide { right: calc(50% - 6.5vmin); top: 12vmin; }
</style></head>
<body class="{{BODYCLASS}}">
<div id="stage"><img id="screen" alt="The handheld's screen"></div>
<div class="bar" id="top">
  <span class="dot"></span><b>JukaHub</b><span id="state">Connecting…</span>
  <button id="sound" type="button" title="Hear the handheld here" hidden>Sound</button>
  <button id="play" type="button" title="Play the handheld from here" hidden>Play</button>
  <button id="fit" type="button" title="Fit, fill or pixel-perfect (M)">Fit</button>
  <button id="full" type="button" title="Full screen (F)">Full screen</button>
</div>
<div class="bar" id="bottom"><span id="hint">Double-click or F for full screen · M changes the fit</span>
  <button id="layout" type="button" hidden>Buttons by position</button>
  <button id="padbtn" type="button" hidden>Touch pad</button></div>
<div id="pad" aria-hidden="true">
  <div class="pb sq" id="pUp" data-b="up">&#9650;</div><div class="pb sq" id="pDown" data-b="down">&#9660;</div>
  <div class="pb sq" id="pLeft" data-b="left">&#9664;</div><div class="pb sq" id="pRight" data-b="right">&#9654;</div>
  <div class="pb" id="pX" data-b="x">X</div><div class="pb" id="pB" data-b="b">B</div>
  <div class="pb" id="pY" data-b="y">Y</div><div class="pb" id="pA" data-b="a">A</div>
  <div class="pb pill" id="pL1" data-b="l1">L1</div><div class="pb pill" id="pL2" data-b="l2">L2</div>
  <div class="pb pill" id="pR1" data-b="r1">R1</div><div class="pb pill" id="pR2" data-b="r2">R2</div>
  <div class="pb pill" id="pSelect" data-b="select">Select</div><div class="pb pill" id="pMenu" data-b="menu">Menu</div>
  <div class="pb pill" id="pStart" data-b="start">Start</div><div class="pb pill" id="pHide" data-act="hide">Hide</div>
</div>
<div id="msg"></div>
<div id="toast"></div>
<script>
(function () {
  'use strict';
  var CRED = '{{CRED}}';
  var body = document.body;
  var img = document.getElementById('screen');
  var stateEl = document.getElementById('state');
  var msgEl = document.getElementById('msg');
  var toastEl = document.getElementById('toast');
  var fitBtn = document.getElementById('fit');
  var fullBtn = document.getElementById('full');
  var soundBtn = document.getElementById('sound');
  var playBtn = document.getElementById('play');
  var hintEl = document.getElementById('hint');
  var layoutBtn = document.getElementById('layout');
  var padBtn = document.getElementById('padbtn');
  var padEl = document.getElementById('pad');
  var mode = 'mjpeg';
  var errors = [];
  var seq = 0;
  var retryTimer = 0, pollTimer = 0, idleTimer = 0, toastTimer = 0;
  var objectURL = '';
  var status = null;
  var zeroStreams = 0;
  var expired = false;
  var streaming = false;
  var wasSharing = false;
  // gotFrame: a picture arrived since the stream last (re)started, at startedAt.
  var gotFrame = false, startedAt = 0;
  // gen numbers the starts: a frame loop or an answer from an older start
  // stops itself, so two loops never fight over the picture.
  var gen = 0;
  var FULL_MSG = 'Screen Share is full. The picture starts here when someone else stops watching.';
  var HINT = hintEl.textContent;

  function link(path, extra) { return path + '?' + CRED + (extra ? '&' + extra : ''); }
  function now() { return new Date().getTime(); }
  function setClass(name, on) {
    if (on) { body.classList.add(name); } else { body.classList.remove(name); }
  }
  function setState(text, live) { stateEl.textContent = text; setClass('live', !!live); }
  function showMsg(text) { msgEl.textContent = text || ''; msgEl.className = text ? 'show' : ''; }
  function toast(text, ms) {
    clearTimeout(toastTimer);
    toastEl.textContent = text || '';
    toastEl.className = text ? 'show' : '';
    if (text) { toastTimer = setTimeout(function () { toastEl.className = ''; }, ms || 5000); }
  }
  function store(key, value) {
    try {
      if (value === undefined) { return window.localStorage.getItem(key); }
      window.localStorage.setItem(key, value);
    } catch (e) { /* private mode */ }
    return null;
  }

  function startStream() {
    clearTimeout(retryTimer);
    clearTimeout(pollTimer);
    if (expired) { return; }
    streaming = true;
    gotFrame = false;
    startedAt = now();
    gen++;
    if (mode === 'mjpeg') {
      img.src = link('/stream', 't=' + now());
    } else {
      pollFrame(gen);
    }
  }
  function stopStream() {
    clearTimeout(retryTimer);
    clearTimeout(pollTimer);
    streaming = false;
    gen++;
    img.removeAttribute('src');
  }

  img.addEventListener('load', function () { gotFrame = true; fitPixels(); });
  img.addEventListener('error', function () {
    if (mode !== 'mjpeg' || expired || !streaming) { return; }
    var mine = gen, hadFrame = gotFrame, lived = now() - startedAt;
    // Ask the handheld why before blaming this browser: a full share, sharing
    // switched off and a retired link refuse the stream too, and the last
    // status answer may be two seconds old.
    getJSON(link('/status'), function (code, data) {
      if (code === 401) { linkExpired(); return; }
      if (mine !== gen || mode !== 'mjpeg' || !streaming) { return; }
      if (code === 200 && data) { status = data; }
      if (code !== 200 || !data || !data.sharing || data.full) {
        if (data && data.full) { showMsg(FULL_MSG); }
        retryTimer = setTimeout(startStream, 3000);
        return;
      }
      if (hadFrame && lived >= 5000) {
        // It showed the handheld for a while, then dropped: the network, not
        // the browser.
        retryTimer = setTimeout(startStream, 1500);
        return;
      }
      var t = now();
      errors.push(t);
      errors = errors.filter(function (e) { return t - e < 15000; });
      if (errors.length >= 2) {
        // This browser cannot keep an MJPEG stream: fetch frames one by one.
        mode = 'poll';
        startStream();
        return;
      }
      retryTimer = setTimeout(startStream, 1500);
    });
  });

  function pollFrame(mine) {
    if (mine !== gen || mode !== 'poll' || expired || !streaming) { return; }
    if (!window.fetch || !window.URL || !URL.createObjectURL) {
      img.src = link('/frame.jpg', 't=' + now());
      pollTimer = setTimeout(function () { pollFrame(mine); }, 250);
      return;
    }
    fetch(link('/frame.jpg', 's=' + seq), {cache: 'no-store'}).then(function (res) {
      if (res.status === 401) { linkExpired(); throw new Error('expired'); }
      if (res.status === 503 && status && status.full) { showMsg(FULL_MSG); }
      if (!res.ok) { throw new Error(String(res.status)); }
      var s = parseInt(res.headers.get('X-Frame-Seq') || '0', 10);
      if (s) { seq = s; }
      return res.blob();
    }).then(function (blob) {
      if (mine !== gen) { return; }
      var next = URL.createObjectURL(blob);
      img.src = next;
      if (objectURL) { URL.revokeObjectURL(objectURL); }
      objectURL = next;
      pollTimer = setTimeout(function () { pollFrame(mine); }, 20);
    }).catch(function () {
      if (mine !== gen) { return; }
      pollTimer = setTimeout(function () { pollFrame(mine); }, 1000);
    });
  }

  function linkExpired() {
    expired = true;
    stopStream();
    soundStop();
    playEnd('');
    setState('Link expired', false);
    showMsg('This link no longer works. Open Screen Share on the handheld for the new one.');
  }

  function getJSON(u, done) {
    var x = new XMLHttpRequest();
    x.open('GET', u, true);
    x.timeout = 4000;
    x.onload = function () {
      var data = null;
      try { data = JSON.parse(x.responseText); } catch (e) { data = null; }
      done(x.status, data);
    };
    x.onerror = x.ontimeout = function () { done(0, null); };
    x.send();
  }

  function pollStatus() {
    if (expired) { return; }
    getJSON(link('/status'), function (code, data) {
      if (code === 401) { linkExpired(); return; }
      setTimeout(pollStatus, 2000);
      if (code !== 200 || !data) { setState('Reconnecting…', false); return; }
      status = data;
      if (!data.sharing) {
        wasSharing = false;
        if (streaming) { stopStream(); }
        setState('Paused on the handheld', false);
        showMsg(data.note || 'Screen Share is off on the handheld.');
        updateButtons();
        return;
      }
      if (!wasSharing) {
        // Sharing (re)started: the stream gets another chance, whatever made
        // this page fall back to single frames before.
        wasSharing = true;
        if (mode !== 'mjpeg' || errors.length) {
          mode = 'mjpeg';
          errors = [];
          if (streaming) { stopStream(); }
        }
        wsUpdate();
      }
      if (!streaming) { startStream(); }
      var label = data.source === 'game' ? 'Live · game' : data.source === 'paused' ? 'Paused · game on screen' : 'Live';
      if (data.fps >= 1 && data.source !== 'paused') { label += ' · ' + Math.round(data.fps) + ' fps'; }
      if (play.state === 'playing') { label += ' · you are playing'; } else if (data.playing) { label += ' · a viewer is playing'; }
      setState(label, data.source !== 'paused');
      showMsg(data.full && !gotFrame ? FULL_MSG : (data.note || ''));
      // A stream the handheld no longer counts was dropped on the way: reconnect.
      if (mode === 'mjpeg' && data.streams === 0) {
        zeroStreams++;
        if (zeroStreams >= 2) { zeroStreams = 0; startStream(); }
      } else {
        zeroStreams = 0;
      }
      updateButtons();
      fitPixels();
    });
  }

  var fits = ['fit', 'fill', 'pixel'];
  var fitLabels = {fit: 'Fit', fill: 'Fill', pixel: 'Pixel'};
  var fit = store('jukahub.share.fit') || 'fit';
  if (fits.indexOf(fit) < 0) { fit = 'fit'; }
  function applyFit() {
    for (var i = 0; i < fits.length; i++) { setClass(fits[i], fits[i] === fit); }
    fitBtn.textContent = fitLabels[fit];
    fitPixels();
  }
  function fitPixels() {
    var w = img.naturalWidth || (status && status.width) || 0;
    var h = img.naturalHeight || (status && status.height) || 0;
    if (fit !== 'pixel' || !w || !h) { img.style.width = ''; img.style.height = ''; return; }
    var s = Math.max(1, Math.floor(Math.min(window.innerWidth / w, window.innerHeight / h)));
    img.style.width = (w * s) + 'px';
    img.style.height = (h * s) + 'px';
  }
  function nextFit() {
    fit = fits[(fits.indexOf(fit) + 1) % fits.length];
    store('jukahub.share.fit', fit);
    applyFit();
  }
  fitBtn.onclick = nextFit;
  window.addEventListener('resize', fitPixels);

  function isFull() { return !!(document.fullscreenElement || document.webkitFullscreenElement); }
  function toggleFull() {
    var el = document.documentElement;
    try {
      if (isFull()) {
        (document.exitFullscreen || document.webkitExitFullscreen).call(document);
        return;
      }
      var req = el.requestFullscreen || el.webkitRequestFullscreen;
      if (req) {
        var p = req.call(el);
        if (p && p.catch) { p.catch(function () {}); }
      }
    } catch (e) { /* no full screen here */ }
  }
  function fullChanged() { fullBtn.textContent = isFull() ? 'Exit full screen' : 'Full screen'; requestWake(); }
  fullBtn.onclick = toggleFull;
  document.getElementById('stage').addEventListener('dblclick', toggleFull);
  document.addEventListener('fullscreenchange', fullChanged);
  document.addEventListener('webkitfullscreenchange', fullChanged);

  function wake() {
    setClass('idle', false);
    clearTimeout(idleTimer);
    idleTimer = setTimeout(function () { setClass('idle', true); }, 3000);
  }
  ['mousemove', 'mousedown', 'touchstart'].forEach(function (name) {
    document.addEventListener(name, wake, false);
  });

  var lock = null;
  function requestWake() {
    // The Screen Wake Lock needs a secure page; over plain http it is absent
    // and the viewer's own sleep setting applies.
    if (!navigator.wakeLock || document.visibilityState !== 'visible' || lock) { return; }
    navigator.wakeLock.request('screen').then(function (l) {
      lock = l;
      l.addEventListener('release', function () { lock = null; });
    }).catch(function () {});
  }
  document.addEventListener('visibilitychange', requestWake);

  // ── The /play socket: the handheld's sound here, this page's presses there ──

  var KEY = store('jukahub.share.key');
  if (!KEY || !/^[a-z0-9]{16,64}$/.test(KEY)) {
    KEY = '';
    while (KEY.length < 24) { KEY += Math.random().toString(36).slice(2); }
    KEY = KEY.slice(0, 24);
    store('jukahub.share.key', KEY);
  }
  var ws = null, wsOpen = false, wsRetry = 0, wsTimer = 0;
  var sound = {on: false, ctx: null, gain: null, next: 0, lat: 0.08, arrived: 0, steady: 0};
  var play = {state: 'idle', games: true};

  function wsURL() { return (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + link('/play'); }
  function wsWanted() { return !expired && wasSharing && (sound.on || play.state !== 'idle'); }
  function wsUpdate() {
    clearTimeout(wsTimer);
    if (!wsWanted()) {
      if (ws) { try { ws.close(); } catch (e) { /* closed */ } }
      return;
    }
    if (ws || !window.WebSocket) { return; }
    try { ws = new WebSocket(wsURL()); } catch (e) { ws = null; return; }
    ws.binaryType = 'arraybuffer';
    var mine = ws;
    ws.onopen = function () {
      if (mine !== ws) { return; }
      wsOpen = true;
      wsRetry = 0;
      send({t: 'hello', key: KEY});
      if (sound.on) { send({t: 'sound', on: true}); }
      if (play.state !== 'idle') { send({t: 'play'}); }
    };
    ws.onmessage = function (ev) {
      if (mine !== ws) { return; }
      if (typeof ev.data === 'string') { onText(ev.data); } else { playBlock(ev.data); }
    };
    ws.onclose = function () {
      if (mine !== ws) { return; }
      ws = null;
      wsOpen = false;
      if (wsWanted()) { wsTimer = setTimeout(wsUpdate, Math.min(8000, 500 * Math.pow(2, wsRetry++))); }
    };
    ws.onerror = function () { /* onclose follows */ };
  }
  function send(obj) {
    if (ws && wsOpen) { try { ws.send(JSON.stringify(obj)); } catch (e) { /* closing */ } }
  }
  function onText(text) {
    var m = null;
    try { m = JSON.parse(text); } catch (e) { return; }
    if (!m) { return; }
    if (m.t === 'hi') {
      if (!m.sound && sound.on) { soundStop(); toast('Sound is switched off on the handheld.'); }
      if (!m.control && play.state !== 'idle') { playEnd('Controllers are switched off on the handheld.'); }
      return;
    }
    if (m.t !== 'ctl') { return; }
    if (m.state === 'asking') {
      play.state = 'asking';
      toast('Asked the handheld. Allow it there: Start → Screen Share, then A.', 15000);
    } else if (m.state === 'playing') {
      var first = play.state !== 'playing';
      if (!first && play.games && !m.games) {
        toast('Games on this handheld cannot hear these presses; JukaHub’s menus still do.', 6000);
      }
      play.state = 'playing';
      play.games = !!m.games;
      if (first) {
        if (document.activeElement && document.activeElement.blur) { document.activeElement.blur(); }
        toast(play.games ? 'You are playing. Games on the handheld hear you too.' :
          'You are playing JukaHub’s menus. Games on this handheld cannot hear these presses.', 6000);
        pollPads();
      }
    } else if (m.state === 'denied' || m.state === 'stopped') {
      playEnd(m.note || '');
      return;
    }
    updateButtons();
  }

  // ── Sound: IMA ADPCM blocks scheduled back to back ──

  var STEPS = [7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
    50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
    253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
    1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
    3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442,
    11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767];
  var INDEX = [-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8];

  // decodeBlock turns one block into per-channel samples (-1..1).
  function decodeBlock(buf) {
    var v = new DataView(buf);
    if (buf.byteLength < 16 || v.getUint8(0) !== 65 || v.getUint8(1) !== 1) { return null; }
    var channels = v.getUint8(2), rate = v.getUint32(4, true), frames = v.getUint16(12, true);
    var per = (frames + 1) >> 1;
    if (channels < 1 || channels > 2 || rate < 8000 || buf.byteLength < 16 + 4 * channels + channels * per) { return null; }
    var out = [];
    for (var c = 0; c < channels; c++) {
      var pred = v.getInt16(16 + 4 * c, true), index = v.getUint8(16 + 4 * c + 2);
      var data = 16 + 4 * channels + c * per;
      var samples = new Float32Array(frames);
      for (var i = 0; i < frames; i++) {
        var b = v.getUint8(data + (i >> 1));
        var nib = (i & 1) ? (b >> 4) : (b & 15);
        var step = STEPS[index], vp = step >> 3;
        if (nib & 4) { vp += step; }
        if (nib & 2) { vp += step >> 1; }
        if (nib & 1) { vp += step >> 2; }
        pred += (nib & 8) ? -vp : vp;
        if (pred > 32767) { pred = 32767; } else if (pred < -32768) { pred = -32768; }
        index += INDEX[nib];
        if (index < 0) { index = 0; } else if (index > 88) { index = 88; }
        samples[i] = pred / 32768;
      }
      out.push(samples);
    }
    return {rate: rate, frames: frames, data: out};
  }

  function playBlock(buf) {
    if (!sound.on || !sound.ctx) { return; }
    var blk = decodeBlock(buf);
    if (!blk) { return; }
    var ctx = sound.ctx, t = ctx.currentTime, arrived = now();
    var gap = arrived - sound.arrived;
    sound.arrived = arrived;
    if (sound.next < t + 0.005) {
      // Behind: after a quiet spell that is expected; in mid-stream it was
      // the network, and the cushion grows a little.
      if (sound.next > 0 && gap < 150) { sound.lat = Math.min(0.3, sound.lat + 0.02); sound.steady = 0; }
      sound.next = t + sound.lat;
    } else if (sound.next > t + sound.lat + 0.25) {
      return; // too far ahead after a burst: drop a block to catch up
    }
    if (++sound.steady > 1500 && sound.lat > 0.06) { sound.lat -= 0.01; sound.steady = 0; }
    var ab = ctx.createBuffer(2, blk.frames, blk.rate);
    ab.getChannelData(0).set(blk.data[0]);
    ab.getChannelData(1).set(blk.data[blk.data.length - 1]);
    var src = ctx.createBufferSource();
    src.buffer = ab;
    src.connect(sound.gain);
    src.start(sound.next);
    sound.next += blk.frames / blk.rate;
  }

  function soundStart() {
    var AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) { toast('This browser cannot play the sound.'); return; }
    if (!sound.ctx) {
      try { sound.ctx = new AC({latencyHint: 'interactive'}); } catch (e) { sound.ctx = new AC(); }
      sound.gain = sound.ctx.createGain();
      sound.gain.connect(sound.ctx.destination);
    }
    if (sound.ctx.resume) { sound.ctx.resume(); }
    sound.on = true;
    sound.next = 0;
    store('jukahub.share.sound', '1');
    wsUpdate();
    send({t: 'sound', on: true});
    updateButtons();
  }
  function soundStop() {
    sound.on = false;
    send({t: 'sound', on: false});
    if (sound.ctx && sound.ctx.suspend) { sound.ctx.suspend(); }
    wsUpdate();
    updateButtons();
  }
  soundBtn.onclick = function () {
    if (sound.on) { store('jukahub.share.sound', '0'); soundStop(); } else { soundStart(); }
  };

  // ── Playing: the keyboard, controllers and a touch pad ──

  var BITS = {up: 0, down: 1, left: 2, right: 3, a: 4, b: 5, x: 6, y: 7, l1: 8, r1: 9, l2: 10, r2: 11,
    select: 12, start: 13, menu: 14, l3: 15, r3: 16};
  var KEYS = {ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right',
    KeyX: 'a', KeyZ: 'b', KeyS: 'x', KeyA: 'y', KeyQ: 'l1', KeyW: 'r1', KeyE: 'l2', KeyR: 'r2',
    Enter: 'start', NumpadEnter: 'start', ShiftLeft: 'select', ShiftRight: 'select', Backspace: 'select', KeyH: 'menu'};
  var keyBits = 0, touchBits = 0;
  var pad = {b: 0, lx: 0, ly: 0, rx: 0, ry: 0, lt: 0, rt: 0, seen: false};
  var letters = store('jukahub.share.letters') === '1';
  var lastSent = '', lastSentAt = 0;
  var isTouch = ('ontouchstart' in window) || (navigator.maxTouchPoints > 0);
  setClass('touch', isTouch);

  function round(v) { return Math.round(v * 50) / 50; }
  function sendState(force) {
    if (play.state !== 'playing') { return; }
    var s = {t: 'pad', b: keyBits | touchBits | pad.b, lx: round(pad.lx), ly: round(pad.ly),
      rx: round(pad.rx), ry: round(pad.ry), lt: round(pad.lt), rt: round(pad.rt)};
    var key = [s.b, s.lx, s.ly, s.rx, s.ry, s.lt, s.rt].join(',');
    if (!force && key === lastSent) { return; }
    lastSent = key;
    lastSentAt = now();
    send(s);
  }
  // While anything is held the handheld hears from this page four times a
  // second: a page that goes quiet is let go of there.
  setInterval(function () {
    if (play.state === 'playing' && lastSent !== '0,0,0,0,0,0,0' && now() - lastSentAt > 200) { sendState(true); }
  }, 250);

  function releaseAll() {
    keyBits = 0;
    touchBits = 0;
    pad = {b: 0, lx: 0, ly: 0, rx: 0, ry: 0, lt: 0, rt: 0, seen: pad.seen};
    var downs = padEl.querySelectorAll('.down');
    for (var i = 0; i < downs.length; i++) { downs[i].classList.remove('down'); }
    touches = {};
    sendState(true);
  }
  window.addEventListener('blur', releaseAll);
  document.addEventListener('visibilitychange', function () { if (document.visibilityState !== 'visible') { releaseAll(); } });

  document.addEventListener('keydown', function (e) {
    if (body.classList.contains('tv') && !sound.on && status && status.sound) {
      // A TV's remote: its first key is the gesture the sound needs.
      soundStart();
    }
    if (play.state === 'playing') {
      var name = KEYS[e.code];
      if (name && !e.ctrlKey && !e.metaKey && !e.altKey) {
        keyBits |= 1 << BITS[name];
        sendState(false);
        e.preventDefault();
      }
      return;
    }
    wake();
    var k = (e.key || '').toLowerCase();
    if (k === 'f') { toggleFull(); e.preventDefault(); }
    if (k === 'm') { nextFit(); e.preventDefault(); }
  });
  document.addEventListener('keyup', function (e) {
    var name = KEYS[e.code];
    if (name && play.state === 'playing') {
      keyBits &= ~(1 << BITS[name]);
      sendState(false);
      e.preventDefault();
    }
  });

  // A standard-mapped controller: buttons by position, like the handheld's
  // (right A, bottom B, top X, left Y), or by their letters when asked.
  function readPads() {
    var list = [];
    try { list = navigator.getGamepads ? navigator.getGamepads() : []; } catch (e) { list = []; }
    var b = 0, lx = 0, ly = 0, rx = 0, ry = 0, lt = 0, rt = 0;
    var face = letters ? {a: 0, b: 1, x: 2, y: 3} : {a: 1, b: 0, x: 3, y: 2};
    function bigger(a, v) { return Math.abs(v) > Math.abs(a) ? v : a; }
    function dz(v) { return Math.abs(v) < 0.15 ? 0 : v; }
    for (var i = 0; list && i < list.length; i++) {
      var p = list[i];
      if (!p || p.connected === false || !p.buttons) { continue; }
      if (!pad.seen) { pad.seen = true; setClass('padoff', true); toast('Controller found: ' + (p.id || 'a gamepad').split('(')[0], 4000); }
      var bt = p.buttons, ax = p.axes || [];
      var on = function (n) { return !!(bt[n] && (bt[n].pressed || bt[n].value > 0.5)); };
      for (var name in face) { if (on(face[name])) { b |= 1 << BITS[name]; } }
      var map = [[4, 'l1'], [5, 'r1'], [8, 'select'], [9, 'start'], [10, 'l3'], [11, 'r3'],
        [12, 'up'], [13, 'down'], [14, 'left'], [15, 'right'], [16, 'menu']];
      for (var j = 0; j < map.length; j++) { if (on(map[j][0])) { b |= 1 << BITS[map[j][1]]; } }
      if (bt[6]) { lt = Math.max(lt, bt[6].value || (bt[6].pressed ? 1 : 0)); }
      if (bt[7]) { rt = Math.max(rt, bt[7].value || (bt[7].pressed ? 1 : 0)); }
      lx = bigger(lx, dz(ax[0] || 0)); ly = bigger(ly, dz(ax[1] || 0));
      rx = bigger(rx, dz(ax[2] || 0)); ry = bigger(ry, dz(ax[3] || 0));
    }
    if (lt > 0.5) { b |= 1 << BITS.l2; }
    if (rt > 0.5) { b |= 1 << BITS.r2; }
    pad.b = b; pad.lx = lx; pad.ly = ly; pad.rx = rx; pad.ry = ry; pad.lt = lt; pad.rt = rt;
  }
  var polling = false;
  function pollPads() {
    if (polling || !navigator.getGamepads) { return; }
    polling = true;
    var step = function () {
      if (play.state !== 'playing') { polling = false; return; }
      readPads();
      sendState(false);
      (window.requestAnimationFrame || function (f) { return setTimeout(f, 16); })(step);
    };
    step();
  }
  window.addEventListener('gamepadconnected', function () { if (play.state === 'playing') { pollPads(); } });

  // The touch pad: every finger presses the button under it, sliding along.
  var touches = {};
  function buttonAt(x, y) {
    var el = document.elementFromPoint(x, y);
    while (el && el !== padEl) {
      if (el.getAttribute && el.getAttribute('data-b')) { return el; }
      el = el.parentNode;
    }
    return null;
  }
  function touchUpdate() {
    var bits = 0, held = {};
    for (var id in touches) {
      var el = touches[id];
      if (el) { bits |= 1 << BITS[el.getAttribute('data-b')]; held[el.id] = true; }
    }
    var all = padEl.querySelectorAll('[data-b]');
    for (var i = 0; i < all.length; i++) {
      if (held[all[i].id]) { all[i].classList.add('down'); } else { all[i].classList.remove('down'); }
    }
    if (bits !== touchBits) {
      if (bits & ~touchBits && navigator.vibrate) { try { navigator.vibrate(8); } catch (e) { /* none */ } }
      touchBits = bits;
      sendState(false);
    }
  }
  padEl.addEventListener('pointerdown', function (e) {
    if (e.target && e.target.getAttribute && e.target.getAttribute('data-act') === 'hide') {
      setClass('padoff', true);
      updateButtons();
      return;
    }
    touches[e.pointerId] = buttonAt(e.clientX, e.clientY);
    try { e.target.setPointerCapture(e.pointerId); } catch (err) { /* not capturable */ }
    touchUpdate();
    e.preventDefault();
  });
  padEl.addEventListener('pointermove', function (e) {
    if (!(e.pointerId in touches)) { return; }
    var el = buttonAt(e.clientX, e.clientY);
    if (el !== touches[e.pointerId]) { touches[e.pointerId] = el; touchUpdate(); }
    e.preventDefault();
  });
  ['pointerup', 'pointercancel', 'lostpointercapture'].forEach(function (name) {
    padEl.addEventListener(name, function (e) {
      if (e.pointerId in touches) { delete touches[e.pointerId]; touchUpdate(); }
    });
  });
  padEl.addEventListener('contextmenu', function (e) { e.preventDefault(); });

  function playEnd(note) {
    var was = play.state;
    play.state = 'idle';
    releaseAll();
    if (was !== 'idle') { send({t: 'stop'}); }
    if (note) { toast(note, 6000); }
    wsUpdate();
    updateButtons();
  }
  playBtn.onclick = function () {
    // Space and Enter are the game's while playing, not this button's.
    playBtn.blur();
    if (play.state === 'idle') {
      play.state = 'asking';
      wsUpdate();
      send({t: 'play'});
      updateButtons();
    } else {
      playEnd('');
    }
  };
  layoutBtn.onclick = function () {
    letters = !letters;
    store('jukahub.share.letters', letters ? '1' : '0');
    updateButtons();
  };
  padBtn.onclick = function () {
    setClass('padoff', !body.classList.contains('padoff'));
    updateButtons();
  };

  function updateButtons() {
    var sharing = !!(status && status.sharing);
    var canSound = sharing && !!status.sound && !!(window.AudioContext || window.webkitAudioContext) && !!window.WebSocket;
    soundBtn.hidden = !canSound && !sound.on;
    soundBtn.textContent = sound.on ? 'Sound on' : 'Sound';
    soundBtn.className = sound.on ? 'on' : '';
    var canPlay = sharing && !!status.controls && !!window.WebSocket;
    playBtn.hidden = !canPlay && play.state === 'idle';
    playBtn.textContent = play.state === 'playing' ? 'Stop playing' : play.state === 'asking' ? 'Asking…' : 'Play';
    playBtn.className = play.state === 'playing' ? 'on' : '';
    setClass('playing', play.state === 'playing');
    layoutBtn.hidden = play.state !== 'playing';
    layoutBtn.textContent = letters ? 'Buttons by letter' : 'Buttons by position';
    padBtn.hidden = play.state !== 'playing' || !isTouch;
    padBtn.textContent = body.classList.contains('padoff') ? 'Show touch pad' : 'Hide touch pad';
    if (play.state === 'playing') {
      hintEl.textContent = (isTouch ? 'Touch pad, a controller, or keys: ' : 'Keys: ') +
        'arrows · X = A · Z = B · S = X · A = Y · Q/W = L1/R1 · E/R = L2/R2 · Enter = Start · Shift = Select · H = Menu.' +
        (pad.seen ? '' : ' No controller? Press one of its buttons; Chrome and Edge show controllers only to secure (https) pages, so use Firefox or Safari for one here.');
    } else {
      hintEl.textContent = HINT;
    }
  }

  applyFit();
  wake();
  requestWake();
  updateButtons();
  // The first status answer starts the stream, so a page opened while sharing
  // is off waits quietly instead of failing its first requests.
  pollStatus();
})();
</script>
</body></html>`

// ── /stream ───────────────────────────────────────────────────────────────

// handleStream writes the MJPEG feed. Each part is a complete JPEG, so a
// browser renders it without any JavaScript.
func (s *companionServer) handleStream(w http.ResponseWriter, r *http.Request, cred castCredential) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !castEnabled.Load() {
		http.Error(w, "Screen Share is off on the handheld", http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}
	if !castAddViewer(r.RemoteAddr) {
		http.Error(w, "Screen Share is full", http.StatusServiceUnavailable)
		return
	}
	defer castRemoveViewer()

	castNoStore(w)
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+castBoundary)
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Each frame gets its own write deadline; a recorder in tests cannot set
	// one, which only means no deadline.
	rc := http.NewResponseController(w)
	var last uint64
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		if !castEnabled.Load() || !cred.stillValid() {
			return
		}
		frame, seq, ok := requestCompanionStreamFrame(last, castFrameWait)
		if !ok {
			continue
		}
		// Checked again after the wait: a new link (Y) or sharing switched off
		// while this viewer waited must not let one more frame out — with the
		// panel open, that frame would show the new link.
		if !castEnabled.Load() || !cred.stillValid() {
			return
		}
		_ = rc.SetWriteDeadline(time.Now().Add(castWriteTimeout))
		if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
			castBoundary, len(frame)); err != nil {
			return
		}
		if _, err := w.Write(frame); err != nil {
			return
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return
		}
		flusher.Flush()
		last = seq
	}
}

// ── /frame.jpg ────────────────────────────────────────────────────────────

// handleFrame answers one frame: the first newer than the s= sequence the
// browser already has, waiting a moment for it, else the newest there is.
func (s *companionServer) handleFrame(w http.ResponseWriter, r *http.Request, cred castCredential) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !castEnabled.Load() {
		http.Error(w, "Screen Share is off on the handheld", http.StatusForbidden)
		return
	}
	if !castNotePoll(r.RemoteAddr) {
		http.Error(w, "Screen Share is full", http.StatusServiceUnavailable)
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("s"), 10, 64)
	frame, seq, ok := requestCompanionStreamFrame(after, castPollWait)
	if !ok {
		frame, seq, _ = companionNewestJPEG()
	}
	// The wait may have outlived the link or the share (see handleStream).
	if !cred.stillValid() {
		http.Error(w, "this Screen Share link no longer works", http.StatusUnauthorized)
		return
	}
	if !castEnabled.Load() {
		http.Error(w, "Screen Share is off on the handheld", http.StatusForbidden)
		return
	}
	if len(frame) == 0 {
		http.Error(w, "no picture yet", http.StatusServiceUnavailable)
		return
	}
	castNoStore(w)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Frame-Seq", strconv.FormatUint(seq, 10))
	_, _ = w.Write(frame)
}

// ── /status ───────────────────────────────────────────────────────────────

// castStatusReport is what /status answers, and what the panel reads.
type castStatusReport struct {
	Sharing bool    `json:"sharing"`
	Viewers int     `json:"viewers"`
	Streams int     `json:"streams"`
	Full    bool    `json:"full"` // no browser can join until one leaves
	Source  string  `json:"source"`
	FPS     float64 `json:"fps"`
	KBps    float64 `json:"kbps"`
	Quality string  `json:"quality"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Note    string  `json:"note,omitempty"`
	// Sound and Controllers on the handheld (castaudio.go, castinput.go),
	// whether a viewer plays now, and whether a game's own sound arrives.
	Sound     bool `json:"sound"`
	Controls  bool `json:"controls"`
	Playing   bool `json:"playing"`
	GameSound bool `json:"gameSound"`
}

// castStatusNow is the share's state right now. Safe from any goroutine.
func castStatusNow() castStatusReport {
	on := castIsEnabled()
	rep := castStatusReport{
		Sharing: on,
		Viewers: castWatcherCount(),
		Streams: castViewerCount(),
		Quality: castLivePreset().Name,
	}
	src := castLive.source.Load()
	if !on {
		src = castSourceOff
	} else if src == castSourceOff {
		src = castSourceUI
	}
	rep.Source = castSourceName(src)
	if on {
		rep.FPS, rep.KBps = castRates()
		rep.Width, rep.Height = castLastFrameSize()
		rep.Full = rep.Viewers >= castMaxViewers
		rep.Sound, rep.Controls = castLive.sound.Load(), castLive.control.Load()
		rep.Playing = castInputPlaying()
		_, _, rep.GameSound = castAudioInfo()
	}
	switch {
	case !on:
		rep.Note = "Screen Share is off on the handheld."
	case src == castSourcePaused && !castGamesSupported():
		rep.Note = "A game is on screen. Games are shared from the handheld only; the picture comes back with JukaHub."
	case src == castSourcePaused && !castLive.games.Load():
		rep.Note = "A game is on screen. Sharing games is off on the handheld."
	case src == castSourcePaused && castLive.fbFailed.Load():
		rep.Note = "A game is on screen, and its picture cannot be shared on this device."
	case src == castSourcePaused:
		rep.Note = "A game is on screen. Its picture appears in a moment."
	case src == castSourceGame && castLive.fbBlack.Load():
		rep.Note = "The game's picture is black: this firmware may not let JukaHub see it."
	}
	return rep
}

// handleCastStatus answers /status. It works while sharing is off, so a page
// left open learns when sharing comes back.
func (s *companionServer) handleCastStatus(w http.ResponseWriter, r *http.Request, cred castCredential) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	castNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(castStatusNow())
}
