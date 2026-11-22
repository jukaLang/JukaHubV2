package main

// companion_music.go — the music servers and accounts set up from the phone:
// the Phone Companion's /music page and its /api/music calls, behind the
// pairing PIN like the rest of the companion (the page through the same
// throttle, the calls through guard).
//
// Navidrome: the address, user name and password, checked with the server
// (ping) before they are saved; the password is kept encrypted, since the
// Subsonic API's token is made from it. Jellyfin: the address, user name and
// password, checked by signing in; only the access token Jellyfin returns is
// kept (the handheld can also sign in with Quick Connect, no password at all).
// ListenBrainz: the user token, checked with validate-token. Last.fm: "Sign
// in" asks Last.fm for a token on the handheld and opens Last.fm's approval
// page on the phone; a build without a Last.fm API account takes the
// player's own key and secret here first.
//
// Nothing secret goes back to the page: it learns the addresses, the user
// names and whether a password or token is saved.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	companionMusicBodyLimit = 16 << 10
	companionMusicCheckWait = 15 * time.Second
)

// musicPhoneURL is the companion's pairing link pointed at /music with the
// card's fragment; "" when the link is not the companion's.
func musicPhoneURL(link, card string) string {
	if !strings.Contains(link, "/?p=") || strings.Contains(link, "#") {
		return ""
	}
	out := strings.Replace(link, "/?p=", "/music?p=", 1)
	if card != "" {
		out += "#" + card
	}
	return out
}

// musicServersPhoneOpen shows the code that opens the /music page (at one
// card when card is set). Main thread.
func musicServersPhoneOpen(card string) bool {
	companionStart()
	link := musicPhoneURL(companion.url, card)
	if link == "" {
		msg := companion.err
		if msg == "" {
			msg = "Your phone sends it over Wi-Fi: connect this device first"
		}
		ShowToast(msg, ToastKindWarning)
		return false
	}
	title := "Music servers · set up on your phone"
	detail := "Navidrome, Jellyfin, Last.fm and ListenBrainz"
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption + " · then Music servers"
	}
	if !qrZoomOpenCardDetail(tbxQRModules(link), title, detail) {
		return false
	}
	phoneCard.kind, phoneCard.title = phoneCardKind("music"), title
	return true
}

// companionMusicStatus is what the page may know.
func companionMusicStatus() map[string]interface{} {
	s := musicServersGet()
	_, _, lfmSource := lastfmCredentials()
	lastfmSignIn.mu.Lock()
	waiting, link := lastfmSignIn.waiting, lastfmSignIn.url
	lastfmSignIn.mu.Unlock()
	return map[string]interface{}{
		"navidrome": map[string]interface{}{"url": s.Navidrome.URL, "user": s.Navidrome.User,
			"set": s.Navidrome.Password != "", "scrobble": s.Navidrome.Scrobble},
		"jellyfin": map[string]interface{}{"url": s.Jellyfin.URL, "user": s.Jellyfin.User,
			"server": s.Jellyfin.ServerName, "set": s.Jellyfin.Token != ""},
		"listenbrainz": map[string]interface{}{"user": s.ListenBrainz.User, "set": s.ListenBrainz.Token != ""},
		"lastfm": map[string]interface{}{"user": s.LastFM.User, "set": s.LastFM.Session != "",
			"keySource": lfmSource, "waiting": waiting, "url": link},
		"pending": scrobblePending(),
	}
}

// handleMusicPage serves /music (the PIN in its address, as the code carries
// it).
func (s *companionServer) handleMusicPage(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, companionLockedPage)
		return
	}
	s.noteSuccess(addr)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", voicePageCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, musicPhonePage)
}

// musicCall is one POST to /api/music.
type musicCall struct {
	Action   string `json:"action"`
	URL      string `json:"url"`
	User     string `json:"user"`
	Password string `json:"password"`
	Token    string `json:"token"`
	Key      string `json:"key"`
	Secret   string `json:"secret"`
	Scrobble bool   `json:"scrobble"`
}

// handleMusicAPI answers the page: GET is the status, POST one action.
func (s *companionServer) handleMusicAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, companionMusicStatus())
	case http.MethodPost:
		var call musicCall
		if err := json.NewDecoder(io.LimitReader(r.Body, companionMusicBodyLimit)).Decode(&call); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		msg, err := companionMusicAction(r.Context(), call)
		if err != nil {
			http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
			return
		}
		out := companionMusicStatus()
		out["ok"], out["message"] = true, msg
		writeCompanionJSON(w, out)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// companionMusicAction runs one action and says what happened.
func companionMusicAction(parent context.Context, call musicCall) (string, error) {
	ctx, cancel := context.WithTimeout(parent, companionMusicCheckWait)
	defer cancel()
	switch call.Action {
	case "navidrome":
		addr, err := musicCleanServerURL(call.URL)
		if err != nil {
			return "", err
		}
		user, err := companionCleanCredential(call.User, 120, "user name")
		if err != nil {
			return "", err
		}
		pass, err := companionCleanCredential(call.Password, 400, "password")
		if err != nil {
			return "", err
		}
		cur := musicServersGet().Navidrome
		if pass == "" && cur.URL == addr && cur.User == user {
			pass = musicDecryptField(cur.Password) // the same account: keep the saved one
		}
		if user == "" || pass == "" {
			return "", errors.New("type the user name and password")
		}
		conf := musicNavidromeConf{URL: addr, User: user, Scrobble: call.Scrobble, Quality: cur.Quality}
		if err := ssCheck(ctx, conf, pass); err != nil {
			return "", err
		}
		enc, err := EncryptAPIToken(pass, "Navidrome password")
		if err != nil {
			return "", errors.New("the handheld could not store the password")
		}
		conf.Password = enc
		if err := musicServersUpdate(func(s *musicServersFile) { s.Navidrome = conf }); err != nil {
			return "", err
		}
		tbxPost(func() {
			phoneCardArrived(phoneCardKind("music"))
			showToast("Navidrome connected as "+user, ToastSuccess())
		})
		return "Saved on the handheld. Navidrome accepted the sign-in.", nil
	case "navidrome-remove":
		if err := musicServersUpdate(func(s *musicServersFile) { s.Navidrome = musicNavidromeConf{} }); err != nil {
			return "", err
		}
		return "The handheld forgot the Navidrome server. Songs you downloaded stay.", nil
	case "jellyfin":
		user, err := companionCleanCredential(call.User, 120, "user name")
		if err != nil {
			return "", err
		}
		pass, err := companionCleanCredential(call.Password, 400, "password")
		if err != nil {
			return "", err
		}
		name, err := jfSignIn(ctx, call.URL, user, pass)
		if err != nil {
			return "", err
		}
		tbxPost(func() {
			phoneCardArrived(phoneCardKind("music"))
			showToast("Jellyfin connected: "+dlFirstNonEmpty(name, "server"), ToastSuccess())
		})
		return "Saved on the handheld. Signed in to " + dlFirstNonEmpty(name, "Jellyfin") + " (only the access token is kept).", nil
	case "jellyfin-remove":
		if err := musicServersUpdate(func(s *musicServersFile) { s.Jellyfin = musicJellyfinConf{} }); err != nil {
			return "", err
		}
		return "The handheld forgot the Jellyfin server.", nil
	case "listenbrainz":
		token, err := companionCleanCredential(call.Token, 200, "token")
		if err != nil || token == "" {
			return "", errors.New("paste your ListenBrainz user token")
		}
		user, err := listenBrainzValidate(ctx, token)
		if err != nil {
			return "", err
		}
		enc, err := EncryptAPIToken(token, "ListenBrainz token")
		if err != nil {
			return "", errors.New("the handheld could not store the token")
		}
		if err := musicServersUpdate(func(s *musicServersFile) { s.ListenBrainz = musicListenBrainzConf{User: user, Token: enc} }); err != nil {
			return "", err
		}
		scrobbleKick()
		return "Saved on the handheld. Listens go to ListenBrainz as " + user + ".", nil
	case "listenbrainz-remove":
		if err := musicServersUpdate(func(s *musicServersFile) { s.ListenBrainz = musicListenBrainzConf{} }); err != nil {
			return "", err
		}
		return "ListenBrainz forgotten.", nil
	case "lastfm-key":
		key, err := companionCleanCredential(call.Key, 64, "API key")
		if err != nil || len(key) != 32 {
			return "", errors.New("a Last.fm API key is 32 letters and digits")
		}
		secret, err := companionCleanCredential(call.Secret, 64, "shared secret")
		if err != nil || len(secret) != 32 {
			return "", errors.New("a Last.fm shared secret is 32 letters and digits")
		}
		enc, err := EncryptAPIToken(secret, "Last.fm shared secret")
		if err != nil {
			return "", errors.New("the handheld could not store the secret")
		}
		if err := musicServersUpdate(func(s *musicServersFile) { s.LastFM.APIKey, s.LastFM.Secret = key, enc }); err != nil {
			return "", err
		}
		return "Saved. Now tap Sign in to Last.fm.", nil
	case "lastfm-start":
		link, err := lastfmStartSignIn(ctx)
		if err != nil {
			return "", err
		}
		return link, nil
	case "lastfm-remove":
		if err := musicServersUpdate(func(s *musicServersFile) { s.LastFM.User, s.LastFM.Session = "", "" }); err != nil {
			return "", err
		}
		return "Signed out of Last.fm on the handheld.", nil
	}
	return "", errors.New("unknown action")
}

// musicPhonePage is the /music page.
const musicPhonePage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub · Music servers</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--bad:#ff6b6b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:calc(14px + env(safe-area-inset-top)) 16px calc(40px + env(safe-area-inset-bottom));max-width:560px;margin:0 auto}
h1{font-size:20px;margin:0 0 4px}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin:14px 0}
.card.picked{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
.note{color:var(--dim);font-size:14px;margin:0 0 10px}
input[type=text],input[type=url],input[type=password]{width:100%;margin-top:8px;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif}
label{display:flex;gap:8px;align-items:center;margin-top:10px;font-size:14px;color:var(--dim)}
button{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
button.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button:disabled{opacity:.6}
.row{display:flex;gap:10px;margin-top:12px;flex-wrap:wrap}
.row>*{flex:1 1 140px}
a{color:#a8b5ff}
.msg{margin-top:10px;font-size:14px;color:var(--dim)}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
small{color:var(--dim);display:block;margin-top:10px;font-size:12px}
details{margin-top:8px}
summary{color:#a8b5ff;cursor:pointer;padding:6px 0}
[hidden]{display:none!important}
</style>
<h1>Music servers</h1>
<p class=note>The iPod screen's Settings › Source picks which of these it browses (or all of them, with duplicates folded together).</p>

<div class=card id=navidromeCard>
  <h2>Navidrome (or any Subsonic server)</h2>
  <p class=note id=nStatus>Checking the handheld...</p>
  <input type=url id=nURL placeholder="http://192.168.1.20:4533" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=text id=nUser placeholder="User name" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=nPass placeholder="Password" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label><input type=checkbox id=nScrobble checked> Count plays on the server (scrobble)</label>
  <div class=row><button id=nSave>Save on handheld</button><button class=ghost id=nRemove hidden>Forget</button></div>
  <div class=msg id=nMsg aria-live=polite></div>
  <small>The handheld signs every request with a token made from the password and keeps the password encrypted. Plain http:// only works for a server on your home network (or Tailscale); use https:// for one on the internet. Leave the password empty to keep the saved one.</small>
</div>

<div class=card id=jellyfinCard>
  <h2>Jellyfin</h2>
  <p class=note id=jStatus>Checking the handheld...</p>
  <input type=url id=jURL placeholder="http://192.168.1.20:8096" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=text id=jUser placeholder="User name" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=jPass placeholder="Password" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=jSave>Sign in</button><button class=ghost id=jRemove hidden>Forget</button></div>
  <div class=msg id=jMsg aria-live=polite></div>
  <small>Only the access token Jellyfin gives back is kept, not the password. No password at all: on the handheld use Settings › Music servers › Jellyfin Quick Connect and approve the code in any signed-in Jellyfin app.</small>
</div>

<div class=card id=lastfmCard>
  <h2>Last.fm</h2>
  <p class=note id=lStatus>Checking the handheld...</p>
  <div class=row><button id=lStart>Sign in to Last.fm</button><button class=ghost id=lRemove hidden>Sign out</button></div>
  <details id=lOwn>
    <summary>Use your own Last.fm API account</summary>
    <p class=note>Needed only when this JukaHub was built without one. Make a free API account at <a href="https://www.last.fm/api/account/create" target=_blank rel="noopener noreferrer">last.fm/api/account/create</a> (any name, no callback), then paste its key and secret.</p>
    <input type=text id=lKey placeholder="API key" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
    <input type=password id=lSecret placeholder="Shared secret" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
    <div class=row><button class=ghost id=lKeySave>Save the key</button></div>
  </details>
  <div class=msg id=lMsg aria-live=polite></div>
</div>

<div class=card id=listenbrainzCard>
  <h2>ListenBrainz</h2>
  <p class=note id=bStatus>Checking the handheld...</p>
  <input type=password id=bToken placeholder="User token (listenbrainz.org/settings)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=bSave>Save on handheld</button><button class=ghost id=bRemove hidden>Forget</button></div>
  <div class=msg id=bMsg aria-live=polite></div>
  <small>Listens made offline wait on the handheld and are sent when it is next online.</small>
</div>

<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const $ = id => document.getElementById(id);
function hdr(json) { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; }
async function post(body) {
  const res = await fetch('/api/music', {method: 'POST', headers: hdr(true), body: JSON.stringify(body)});
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  try { return JSON.parse(text); } catch (_) { return {}; }
}
function say(id, text, bad) { const el = $(id); el.textContent = text || ''; el.className = bad ? 'msg bad' : 'msg'; }
function show(d) {
  const n = d.navidrome || {}, j = d.jellyfin || {}, l = d.lastfm || {}, b = d.listenbrainz || {};
  $('nStatus').textContent = n.set ? 'Connected to ' + n.url + ' as ' + n.user + '.' : 'Not connected.';
  $('nRemove').hidden = !n.set;
  if (n.set) { if (!$('nURL').value) $('nURL').value = n.url || ''; if (!$('nUser').value) $('nUser').value = n.user || ''; $('nScrobble').checked = !!n.scrobble; }
  $('jStatus').textContent = j.set ? 'Signed in to ' + (j.server || j.url) + ' as ' + j.user + '.' : 'Not signed in.';
  $('jRemove').hidden = !j.set;
  if (j.set && !$('jURL').value) $('jURL').value = j.url || '';
  let line = l.set ? 'Scrobbling to Last.fm as ' + l.user + '.' : 'Not signed in.';
  if (!l.keySource) line += ' This build has no Last.fm API account: add yours below first.';
  if (d.pending) line += ' ' + d.pending + ' listen(s) waiting to be sent.';
  $('lStatus').textContent = line;
  $('lRemove').hidden = !l.set;
  if (!l.keySource) $('lOwn').open = true;
  $('bStatus').textContent = b.set ? 'Sending listens as ' + b.user + '.' : 'Not connected.';
  $('bRemove').hidden = !b.set;
}
async function load() {
  const res = await fetch('/api/music', {headers: hdr(false), cache: 'no-store'});
  if (!res.ok) throw new Error((await res.text()).trim() || 'Could not reach the handheld');
  show(await res.json());
}
async function busy(id, label, fn) {
  const b = $(id); const old = b.textContent;
  b.disabled = true; b.textContent = label;
  try { await fn(); } finally { b.disabled = false; b.textContent = old; }
}
function act(btn, label, msg, body, after) {
  $(btn).onclick = () => busy(btn, label, async () => {
    const b = typeof body === 'function' ? body() : body;
    if (!b) return;
    try { const r = await post(b); say(msg, r.message); show(r); if (after) after(r); }
    catch (e) { say(msg, e.message, true); }
  });
}
act('nSave', 'Checking with Navidrome...', 'nMsg', () => ({action: 'navidrome', url: $('nURL').value, user: $('nUser').value, password: $('nPass').value, scrobble: $('nScrobble').checked}), () => { $('nPass').value = ''; });
act('nRemove', 'Forgetting...', 'nMsg', () => confirm('Forget the Navidrome server on the handheld?') ? {action: 'navidrome-remove'} : null);
act('jSave', 'Signing in...', 'jMsg', () => ({action: 'jellyfin', url: $('jURL').value, user: $('jUser').value, password: $('jPass').value}), () => { $('jPass').value = ''; });
act('jRemove', 'Forgetting...', 'jMsg', () => confirm('Forget the Jellyfin server on the handheld?') ? {action: 'jellyfin-remove'} : null);
act('bSave', 'Checking with ListenBrainz...', 'bMsg', () => ({action: 'listenbrainz', token: $('bToken').value}), () => { $('bToken').value = ''; });
act('bRemove', 'Forgetting...', 'bMsg', {action: 'listenbrainz-remove'});
act('lKeySave', 'Saving...', 'lMsg', () => ({action: 'lastfm-key', key: $('lKey').value, secret: $('lSecret').value}), () => { $('lSecret').value = ''; });
act('lRemove', 'Signing out...', 'lMsg', {action: 'lastfm-remove'});
$('lStart').onclick = () => busy('lStart', 'Asking Last.fm...', async () => {
  try {
    const r = await post({action: 'lastfm-start'});
    if (r.message && r.message.indexOf('https://') === 0) {
      const box = $('lMsg'); box.className = 'msg'; box.textContent = '';
      const a = document.createElement('a');
      a.href = r.message; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = 'Open Last.fm and allow JukaHub';
      box.appendChild(a);
      box.appendChild(document.createTextNode(' — the handheld notices by itself.'));
    } else { say('lMsg', r.message); }
  } catch (e) { say('lMsg', e.message, true); }
});
const picked = location.hash.slice(1);
if (['navidrome', 'jellyfin', 'lastfm', 'listenbrainz'].indexOf(picked) >= 0) {
  const card = $(picked + 'Card');
  card.classList.add('picked');
  card.scrollIntoView({block: 'start'});
}
function tick() { load().catch(e => { $('nStatus').textContent = e.message; }).finally(() => setTimeout(tick, document.hidden ? 30000 : 10000)); }
tick();
</script>
`
