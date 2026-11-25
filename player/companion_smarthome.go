package main

// companion_smarthome.go — Smart Home set up from the phone: the Phone
// Companion's /smarthome page and its /api/smarthome calls, behind the
// pairing PIN like the rest of the companion (the page through the same
// throttle, the calls through guard).
//
// TP-Link: the TP-Link ID (the Tapo or Kasa app's e-mail and password),
// checked with a device that needs it when one is on the network, kept in
// smarthome.json with the password encrypted (shubSignIn). Home Assistant:
// the address and a long-lived token — far too long to type with a D-pad —
// checked with /api/states before they are saved in ha_url / ha_token (the
// token encrypted), the keys the Smart Home panel has always read. Google Home
// needs nothing: the page lists what the handheld found. Apple Home lets no
// other device in; the page shows the way that works, a Matter pairing code
// from the Home app into Home Assistant.
//
// Nothing secret goes back to the page: it learns the TP-Link ID's address,
// Home Assistant's address, whether a token is saved, and each device's name
// and state.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// phoneCardSmartHome is the code that opens /smarthome.
	phoneCardSmartHome phoneCardKind = "smarthome"
	// companionSmartHomeBodyLimit bounds one call.
	companionSmartHomeBodyLimit = 16 << 10
	// companionSmartHomeWait bounds a check with a device or Home Assistant.
	companionSmartHomeWait = 20 * time.Second
)

// registerSmartHomeRoutes adds the page and its calls (companion.go).
func (s *companionServer) registerSmartHomeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/smarthome", s.handleSmartHomePage)
	mux.HandleFunc("/api/smarthome", s.guard(s.handleSmartHomeAPI))
}

// smartHomePhoneURL is the companion's pairing link pointed at /smarthome,
// with the card's fragment; "" when the link is not the companion's.
func smartHomePhoneURL(link, card string) string {
	if !strings.Contains(link, "/?p=") || strings.Contains(link, "#") {
		return ""
	}
	out := strings.Replace(link, "/?p=", "/smarthome?p=", 1)
	if card != "" {
		out += "#" + card
	}
	return out
}

// smartHomePhoneOpen shows the code that opens /smarthome (at one card when
// card is set: "tplink", "ha", "google", "apple"). Main thread.
func smartHomePhoneOpen(card string) bool {
	companionStart()
	link := smartHomePhoneURL(companion.url, card)
	if link == "" {
		msg := companion.err
		if msg == "" {
			msg = "Your phone sets it up over Wi-Fi: connect this device first"
		}
		ShowToast(msg, ToastKindWarning)
		return false
	}
	title := "Smart Home · set up on your phone"
	detail := "TP-Link, Home Assistant, Google Home and Apple Home"
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption + " · then Smart Home"
	}
	if !qrZoomOpenCardDetail(tbxQRModules(link), title, detail) {
		return false
	}
	phoneCard.kind, phoneCard.title = phoneCardSmartHome, title
	return true
}

// companionSmartHomeSync has the main loop copy the config's Home Assistant
// settings into the hub and count the page as on screen (shubKick: the
// devices are read while it is open), waiting a moment for it (a game may
// hold the loop; the last copy answers then).
func companionSmartHomeSync() {
	done := make(chan struct{})
	tbxPost(func() {
		shubKick(appConfig)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
	}
}

// companionSmartHomeStatus is what the page may know.
func companionSmartHomeStatus() map[string]interface{} {
	v := shubSnapshot()
	need, tpap := 0, 0
	devices := make([]map[string]string, 0, len(v.Devices))
	for _, d := range v.Devices {
		switch d.Need {
		case shubNeedSignIn:
			need++
		case shubNeedTPAP:
			tpap++
		}
		state := shubNeedText(v.SignedIn, d)
		if state == "" {
			state = shubStateText(d)
		}
		devices = append(devices, map[string]string{"name": d.Name, "state": state, "source": d.Source})
	}
	return map[string]interface{}{
		"tplink":   map[string]interface{}{"user": v.User, "set": v.SignedIn, "found": v.TPLink, "needSignIn": need, "tpap": tpap},
		"ha":       map[string]interface{}{"url": v.HAURL, "set": v.HAOn, "error": v.HAErr},
		"google":   v.Google,
		"devices":  devices,
		"scanning": v.Scanning,
	}
}

// handleSmartHomePage serves /smarthome (the PIN in its address, as the code
// carries it).
func (s *companionServer) handleSmartHomePage(w http.ResponseWriter, r *http.Request) {
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
	_, _ = io.WriteString(w, smartHomePhonePage)
}

// smartHomeCall is one POST to /api/smarthome.
type smartHomeCall struct {
	Action   string `json:"action"`
	User     string `json:"user"`
	Password string `json:"password"`
	URL      string `json:"url"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure"`
}

// handleSmartHomeAPI answers the page: GET is the status, POST one action.
func (s *companionServer) handleSmartHomeAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		companionSmartHomeSync()
		writeCompanionJSON(w, companionSmartHomeStatus())
	case http.MethodPost:
		var call smartHomeCall
		if err := json.NewDecoder(io.LimitReader(r.Body, companionSmartHomeBodyLimit)).Decode(&call); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		msg, err := companionSmartHomeAction(r.Context(), call)
		if err != nil {
			http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
			return
		}
		companionSmartHomeSync()
		out := companionSmartHomeStatus()
		out["ok"], out["message"] = true, msg
		writeCompanionJSON(w, out)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// companionSmartHomeAction runs one action and says what happened.
func companionSmartHomeAction(parent context.Context, call smartHomeCall) (string, error) {
	ctx, cancel := context.WithTimeout(parent, companionSmartHomeWait)
	defer cancel()
	switch call.Action {
	case "tplink":
		user, err := companionCleanCredential(call.User, 254, "TP-Link ID")
		if err != nil {
			return "", err
		}
		pass, err := companionCleanCredential(call.Password, 256, "password")
		if err != nil {
			return "", err
		}
		msg, err := shubSignIn(ctx, user, pass)
		if err != nil {
			return "", err
		}
		tbxPost(func() {
			phoneCardArrived(phoneCardSmartHome)
			ShowToast("TP-Link ID saved from your phone", ToastKindSuccess)
		})
		return msg, nil
	case "tplink-remove":
		shubSignOut()
		return "The handheld forgot your TP-Link ID. Old Kasa plugs keep working without it.", nil
	case "ha":
		return companionSmartHomeHA(ctx, call)
	case "ha-remove":
		done := make(chan struct{})
		tbxPost(func() {
			smartHomeHAForget(appConfig)
			close(done)
		})
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		return "The handheld forgot Home Assistant.", nil
	case "scan":
		if !shubScanNow() {
			return "Already looking…", nil
		}
		return "Looking for devices on the handheld's Wi-Fi — the list fills in a few seconds.", nil
	}
	return "", errors.New("unknown action")
}

// companionSmartHomeHA checks a Home Assistant address and token and has the
// main loop save them. Plain http:// is only for a server on the home network
// (or Tailscale): the token must not cross the internet in the clear.
func companionSmartHomeHA(ctx context.Context, call smartHomeCall) (string, error) {
	base, err := smartHomeEndpoint(call.URL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("the address is not valid")
	}
	if u.Scheme == "http" && !webdavLANHost(u.Hostname()) {
		return "", errors.New("use https:// for a Home Assistant on the internet — plain http:// only on your home network or Tailscale")
	}
	token, err := companionCleanCredential(call.Token, 2048, "token")
	if err != nil {
		return "", err
	}
	if token == "" {
		// The same server again: keep the token the handheld has.
		shub.mu.Lock()
		cur := shub.ha
		shub.mu.Unlock()
		if cur.HAToken == "" || !strings.EqualFold(strings.TrimRight(cur.HAURL, "/"), base) {
			return "", errors.New("paste a long-lived access token (Home Assistant › your profile › Security)")
		}
		token = cur.HAToken
	}
	check := smartHomeSettings{HAURL: base, HAToken: token, HAVerify: !call.Insecure}
	ents, err := fetchHAEntities(ctx, check)
	if err != nil {
		return "", err
	}
	enc, err := EncryptAPIToken(token, "ha_token")
	if err != nil || enc == "" {
		return "", errors.New("the handheld could not store the token")
	}
	insecure := call.Insecure
	tbxPost(func() {
		smartHomeHASave(appConfig, base, enc, insecure)
		phoneCardArrived(phoneCardSmartHome)
		ShowToast("Home Assistant connected from your phone", ToastKindSuccess)
	})
	return "Saved on the handheld. Home Assistant has " + pluralizeCount(len(ents), "light, switch, fan or cover", "lights, switches, fans and covers") +
		" for the Smart Home tab.", nil
}

// smartHomeHASave writes the Home Assistant keys the panel reads (the token
// encrypted, as `JukaHub -set-secret ha_token` would). Main thread.
func smartHomeHASave(c *Config, base, encToken string, insecure bool) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom["ha_url"] = base
	c.Variables.Custom["ha_token"] = encToken
	c.Variables.Custom["ha_insecure"] = insecure
	saveUserConfigDebounced(userConfigFromVariables(c))
	smartHomeSettingsForget()
	shubSyncConfig(c)
	shubPollSoon()
	homeTabsTouch()
}

// smartHomeHAForget empties the Home Assistant keys (emptied rather than
// deleted, so a value under another spelling cannot come back). Main thread.
func smartHomeHAForget(c *Config) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom["ha_url"] = ""
	c.Variables.Custom["ha_token"] = ""
	c.Variables.Custom["ha_insecure"] = false
	saveUserConfigDebounced(userConfigFromVariables(c))
	smartHomeSettingsForget()
	shubSyncConfig(c)
	homeTabsTouch()
}

// smartHomePhonePage is the /smarthome page.
const smartHomePhonePage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub · Smart Home</title>
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
input[type=email],input[type=url],input[type=password]{width:100%;margin-top:8px;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif}
label{display:flex;gap:8px;align-items:center;margin-top:10px;font-size:14px;color:var(--dim)}
button{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
button.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button:disabled{opacity:.6}
.row{display:flex;gap:10px;margin-top:12px;flex-wrap:wrap}
.row>*{flex:1 1 140px}
.msg{margin-top:10px;font-size:14px;color:var(--dim)}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
small{color:var(--dim);display:block;margin-top:10px;font-size:12px}
ol{margin:0;padding-left:20px;font-size:14px}
ol li{margin:6px 0}
ul.devs{list-style:none;margin:0;padding:0}
ul.devs li{display:flex;justify-content:space-between;gap:12px;padding:8px 0;border-top:1px solid var(--line);font-size:14px}
ul.devs li:first-child{border-top:0}
ul.devs span{color:var(--dim);text-align:right}
[hidden]{display:none!important}
</style>
<h1>Smart Home</h1>
<p class=note>What the handheld's Smart Home tab controls. Everything stays on your home network: no cloud sits in between.</p>

<div class=card id=tplinkCard>
  <h2>TP-Link Kasa &amp; Tapo</h2>
  <p class=note id=tStatus>Checking the handheld...</p>
  <input type=email id=tUser placeholder="TP-Link ID (e-mail)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=tPass placeholder="Password" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=tSave>Sign in</button><button class=ghost id=tRemove hidden>Sign out</button></div>
  <div class=msg id=tMsg aria-live=polite></div>
  <small>The e-mail and password of your Tapo or Kasa app. Older Kasa plugs need none. The handheld checks them with one of your devices, keeps the password encrypted and never sends it to TP-Link. A device that still refuses: in the Tapo app open Me › Tapo Lab › Third-Party Compatibility (Kasa app: Settings › Third-Party Compatibility) and switch it on.</small>
</div>

<div class=card id=haCard>
  <h2>Home Assistant</h2>
  <p class=note id=hStatus>Checking the handheld...</p>
  <input type=url id=hURL placeholder="http://homeassistant.local:8123" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=hToken placeholder="Long-lived access token" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <label><input type=checkbox id=hInsecure> Accept a self-signed certificate</label>
  <div class=row><button id=hSave>Connect</button><button class=ghost id=hRemove hidden>Disconnect</button></div>
  <div class=msg id=hMsg aria-live=polite></div>
  <small>Make the token in Home Assistant: your profile › Security › Long-lived access tokens › Create token. Its lights, switches, fans and covers appear in Smart Home. Plain http:// only works on your home network (or Tailscale). Leave the token empty to keep the saved one.</small>
</div>

<div class=card id=googleCard>
  <h2>Google Home</h2>
  <p class=note id=gStatus>Checking the handheld...</p>
  <div class=row><button class=ghost id=gScan>Look again</button></div>
  <div class=msg id=gMsg aria-live=polite></div>
  <small>Nest speakers and displays, Chromecast and Google TV on the same Wi-Fi appear by themselves, with no sign-in: what is playing, volume, mute, pause and stop. Google lets no other device switch the rest of a Google Home: add other makers' lights and plugs through their own maker (TP-Link above), or share Matter ones with Home Assistant (Google Home app › the device › Settings › Linked Matter apps &amp; services).</small>
</div>

<div class=card id=appleCard>
  <h2>Apple Home</h2>
  <p class=note>Apple Home takes orders only from Apple devices, so the handheld reaches its accessories through Home Assistant:</p>
  <ol>
    <li>In the Home app, open a Matter accessory's settings, choose <b>Turn On Pairing Mode</b> and copy the code.</li>
    <li>In the Home Assistant phone app: Settings › Devices &amp; services › Add device › <b>Add Matter device</b>, say it is already in use, and paste the code. It stays in Apple Home too.</li>
    <li>Connect Home Assistant above: the accessory appears in the handheld's Smart Home tab.</li>
  </ol>
  <small>HomeKit-only accessories (no Matter logo) can join Home Assistant through its HomeKit Device integration, but only once they are removed from Apple Home.</small>
</div>

<div class=card id=devicesCard>
  <h2>On the handheld</h2>
  <p class=note id=dNone>No devices yet.</p>
  <ul class=devs id=dList></ul>
</div>

<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const $ = id => document.getElementById(id);
function hdr(json) { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; }
async function post(body) {
  const res = await fetch('/api/smarthome', {method: 'POST', headers: hdr(true), body: JSON.stringify(body)});
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  try { return JSON.parse(text); } catch (_) { return {}; }
}
function say(id, text, bad) { const el = $(id); el.textContent = text || ''; el.className = bad ? 'msg bad' : 'msg'; }
function show(d) {
  const t = d.tplink || {}, h = d.ha || {};
  let line = t.set ? 'Signed in as ' + t.user + '.' : 'Not signed in.';
  if (t.found) line += ' ' + t.found + ' TP-Link device(s) found.';
  if (t.needSignIn) line += ' ' + t.needSignIn + ' waiting for the TP-Link ID.';
  if (t.tpap) line += ' ' + t.tpap + ' need Third-Party Compatibility (below).';
  $('tStatus').textContent = line;
  $('tRemove').hidden = !t.set;
  if (t.set && !$('tUser').value) $('tUser').value = t.user || '';
  $('hStatus').textContent = h.set ? 'Connected to ' + h.url + (h.error ? ' — ' + h.error : '.') : 'Not connected.';
  $('hRemove').hidden = !h.set;
  if (h.set && !$('hURL').value) $('hURL').value = h.url || '';
  const g = d.google || 0;
  $('gStatus').textContent = d.scanning ? 'Looking...' : (g ? g + ' speaker(s), display(s) or TV(s) found.' : 'None found yet.');
  const list = $('dList'); list.textContent = '';
  (d.devices || []).forEach(x => {
    const li = document.createElement('li');
    const n = document.createElement('b'); n.textContent = x.name;
    const s = document.createElement('span'); s.textContent = x.state;
    li.appendChild(n); li.appendChild(s); list.appendChild(li);
  });
  $('dNone').hidden = (d.devices || []).length > 0;
}
async function load() {
  const res = await fetch('/api/smarthome', {headers: hdr(false), cache: 'no-store'});
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
act('tSave', 'Checking with your devices...', 'tMsg', () => ({action: 'tplink', user: $('tUser').value, password: $('tPass').value}), () => { $('tPass').value = ''; });
act('tRemove', 'Signing out...', 'tMsg', () => confirm('Forget your TP-Link ID on the handheld?') ? {action: 'tplink-remove'} : null);
act('hSave', 'Checking with Home Assistant...', 'hMsg', () => ({action: 'ha', url: $('hURL').value, token: $('hToken').value, insecure: $('hInsecure').checked}), () => { $('hToken').value = ''; });
act('hRemove', 'Disconnecting...', 'hMsg', () => confirm('Forget Home Assistant on the handheld?') ? {action: 'ha-remove'} : null);
act('gScan', 'Asking the handheld...', 'gMsg', {action: 'scan'});
const picked = location.hash.slice(1);
if (['tplink', 'ha', 'google', 'apple'].indexOf(picked) >= 0) {
  const card = $(picked + 'Card');
  card.classList.add('picked');
  card.scrollIntoView({block: 'start'});
}
function tick() { load().catch(e => { $('tStatus').textContent = e.message; }).finally(() => setTimeout(tick, document.hidden ? 30000 : 5000)); }
tick();
</script>
`
