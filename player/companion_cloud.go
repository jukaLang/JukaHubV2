package main

// companion_cloud.go — cloud save sync set up from the phone: the Phone
// Companion's /cloud page and its /api/cloud calls, behind the pairing PIN
// like the rest of the companion (the page through the same throttle, the
// calls through guard).
//
// Google Drive needs an OAuth client, which only a Google Cloud project can
// make, and Google wants one per app. A release built with the project's
// client (GitHub secrets JUKAHUB_GOOGLE_CLIENT_ID/SECRET) lets every player
// just sign in. A build without one — or a player who wants their own — gets
// "Set up Google sign-in": the handheld shows a code that opens this page,
// the page walks through making a free "TVs and Limited Input devices" client
// in Google Cloud Console, and the player picks the client file Google gives
// them (or pastes the ID and secret). The handheld checks the client with
// Google by asking for a sign-in code with it — a Desktop or Web client is
// refused right there — keeps it in the config as google_client_id and
// google_client_secret (encrypted), and that same code becomes the sign-in:
// the page shows it with a link to google.com/device, so the phone that is
// already in hand approves it.
//
// WebDAV (Nextcloud, ownCloud, Koofr, InfiniCLOUD, a NAS) needs no Google
// project: the address, user name and app password typed here are checked
// with the server (PROPFIND) before they are saved, the password encrypted
// (webdav_client.go).
//
// Nothing secret goes back to the page: it learns where the Google client
// comes from and its ID (not secret: it is in every sign-in address), the
// account signed in, the sign-in code while one is waiting, and the WebDAV
// address and user name.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// phoneCardGoogle and phoneCardWebDAV are the /cloud page's cards
	// ("googleCard", "webdavCard"), named in the link's fragment.
	phoneCardGoogle phoneCardKind = "google"
	phoneCardWebDAV phoneCardKind = "webdav"
	// companionCloudBodyLimit bounds one call (a client file is under 1 KB).
	companionCloudBodyLimit = 32 << 10
	// companionCloudCheckWait bounds the check with Google or the server.
	companionCloudCheckWait = 15 * time.Second
)

// ── The handheld's side ─────────────────────────────────────────────────────

// cloudPhoneURL is the companion's pairing link pointed at /cloud, with the
// card's fragment; "" when the link is not the companion's.
func cloudPhoneURL(link string, kind phoneCardKind) string {
	if !strings.Contains(link, "/?p=") || strings.Contains(link, "#") {
		return ""
	}
	out := strings.Replace(link, "/?p=", "/cloud?p=", 1)
	if kind != "" {
		out += "#" + string(kind)
	}
	return out
}

// cloudPhoneOpen starts the companion when it is not up and shows the code
// that opens the /cloud page at one card, full screen, as phoneCardOpen does
// for the main page's cards. Main thread.
func cloudPhoneOpen(kind phoneCardKind, title, detail string) bool {
	companionStart()
	link := cloudPhoneURL(companion.url, kind)
	if link == "" {
		msg := companion.err
		if msg == "" {
			msg = "Your phone sends it over Wi-Fi: connect this device first"
		}
		ShowToast(msg, ToastKindWarning)
		return false
	}
	modules := tbxQRModules(link)
	if len(modules) == 0 {
		ShowToast("Cannot draw the QR code", ToastKindWarning)
		return false
	}
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption + " · then Cloud save sync"
	}
	if !qrZoomOpenCardDetail(modules, title, detail) {
		return false
	}
	phoneCard.kind, phoneCard.title = kind, title
	return true
}

// cloudGoogleStatus is what the page may know about Google sign-in.
type cloudGoogleStatus struct {
	Source   string `json:"source"`
	ClientID string `json:"clientId"`
	SignedIn bool   `json:"signedIn"`
	Expired  bool   `json:"expired"`
	Account  string `json:"account"`
	Waiting  bool   `json:"waiting"`
	Code     string `json:"code"`
	URL      string `json:"url"`
}

// cloudGoogleCache is the last copy the main loop made, for a request that
// cannot wait for a fresh one (a game holds the screen).
var cloudGoogleCache atomic.Value // cloudGoogleStatus

// cloudGoogleStatusNow describes Google sign-in. Main thread (it reads the
// config and the sign-in on screen).
func cloudGoogleStatusNow(config *Config) cloudGoogleStatus {
	source, id := gdriveClientSourceOf(config)
	s := gdriveSettingsGet()
	st := cloudGoogleStatus{Source: source, ClientID: id, SignedIn: s.signedIn(), Expired: s.Expired}
	if s.signedIn() {
		st.Account = s.accountLabel()
	}
	if gdriveSignIn.active {
		st.Waiting, st.Code, st.URL = true, gdriveSignIn.code, gdriveSignIn.url
	}
	return st
}

// cloudGoogleStatusFor asks the main loop for a fresh copy, waiting at most
// a second and a half; the last copy answers otherwise.
func cloudGoogleStatusFor() cloudGoogleStatus {
	done := make(chan cloudGoogleStatus, 1)
	tbxPost(func() {
		st := cloudGoogleStatusNow(appConfig)
		cloudGoogleCache.Store(st)
		done <- st
	})
	select {
	case st := <-done:
		return st
	case <-time.After(1500 * time.Millisecond):
	}
	st, _ := cloudGoogleCache.Load().(cloudGoogleStatus)
	return st
}

// cloudWebdavStatus is what the page may know about the WebDAV server.
func cloudWebdavStatus() map[string]interface{} {
	d := webdavSettingsGet()
	return map[string]interface{}{
		"set":      d.connected(),
		"url":      d.URL,
		"user":     d.User,
		"password": d.Password != "",
		"refused":  d.Refused,
		"auto":     d.Auto,
		"last":     d.LastResult,
	}
}

// cloudApplyGoogleClient stores a client the phone sent, in the config keys
// gdriveClientFrom reads, and starts the sign-in with the code the phone's
// check already got (pre; nil when Google could not be asked). Main thread.
func cloudApplyGoogleClient(config *Config, id, encSecret string, pre *gdriveDeviceCode) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom["google_client_id"] = id
	config.Variables.Custom["google_client_secret"] = encSecret
	saveUserConfigDebounced(userConfigFromVariables(config))
	phoneCardArrived(phoneCardGoogle)
	ssyncScreen.model.builtAt = time.Time{}
	client := gdriveClientFrom(config)
	if pre == nil || client.ID != id {
		ShowToast("Google client saved from your phone — press Sign in when this device is online", ToastKindSuccess)
		return
	}
	ShowToast("Google client saved — approve the sign-in on your phone", ToastKindSuccess)
	gdriveBeginSignIn(client, pre)
}

// cloudRemoveGoogleClient empties the config client. A sign-in made with it
// ends too: its refresh token only works with that client. Main thread.
func cloudRemoveGoogleClient(config *Config) {
	if config == nil || config.Variables.Custom == nil {
		return
	}
	id, _ := config.Variables.Custom["google_client_id"].(string)
	id = strings.TrimSpace(id)
	// Emptied rather than deleted: an empty ID is "none" to gdriveClientFrom
	// whatever the saved file held before.
	config.Variables.Custom["google_client_id"] = ""
	config.Variables.Custom["google_client_secret"] = ""
	saveUserConfigDebounced(userConfigFromVariables(config))
	gdriveCancelSignIn()
	phoneCardArrived(phoneCardGoogle)
	ssyncScreen.model.builtAt = time.Time{}
	if s := gdriveSettingsGet(); id != "" && s.signedIn() && strings.EqualFold(s.ClientID, id) {
		saveSyncCancelFull()
		gdriveSignOut()
		ShowToast("Google client removed, and signed out of Google Drive", ToastKindInfo)
		return
	}
	ShowToast("Google client removed", ToastKindInfo)
}

// webdavPhoneArrived applies a server the phone saved: the code closes, the
// page shows it, and the first comparison runs when the page is open.
func webdavPhoneArrived() {
	phoneCardArrived(phoneCardWebDAV)
	// A comparison with the previous server or password is stale: Sync now
	// compares again.
	cloudScreen.webdav = nil
	ssyncScreen.model.builtAt = time.Time{}
	d := webdavSettingsGet()
	ShowToast("WebDAV server connected: "+d.label(), ToastKindSuccess)
	if peerCurrentScene(appConfig) == ssyncSceneName && !ssyncWorkSnapshot().busy && d.ready() {
		webdavStartCompare()
	}
}

// ── The phone's side ────────────────────────────────────────────────────────

// companionCleanCredential trims what a phone pastes around a user name or
// password (line breaks, spaces at the ends) and refuses control characters
// and an overlong value. Inner spaces stay: a password may have them.
func companionCleanCredential(raw string, limit int, what string) (string, error) {
	v := strings.TrimFunc(raw, unicode.IsSpace)
	if len(v) > limit {
		return "", errors.New("the " + what + " is too long")
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("the " + what + " has a line break or a control character in it")
		}
	}
	return v, nil
}

// webdavSameServer reports whether a stored address and a new one name the
// same scheme, host and port.
func webdavSameServer(stored string, next *url.URL) bool {
	u, err := url.Parse(stored)
	return err == nil && next != nil && strings.EqualFold(u.Scheme, next.Scheme) && strings.EqualFold(u.Host, next.Host)
}

// companionCloudSentence starts an error with a capital, as the page shows it.
func companionCloudSentence(err error) string {
	msg := err.Error()
	r, size := utf8.DecodeRuneInString(msg)
	if size == 0 {
		return msg
	}
	return string(unicode.ToUpper(r)) + msg[size:]
}

// handleCloudPage serves /cloud, the setup page (the PIN in its address, as
// the QR code carries it).
func (s *companionServer) handleCloudPage(w http.ResponseWriter, r *http.Request) {
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
	_, _ = io.WriteString(w, cloudPhonePage)
}

// cloudCall is one POST to /api/cloud.
type cloudCall struct {
	Action   string `json:"action"`
	JSON     string `json:"json"`
	ID       string `json:"id"`
	Secret   string `json:"secret"`
	URL      string `json:"url"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// handleCloud answers the page: GET is the status (never a secret), POST one
// of "google", "google-remove", "webdav", "webdav-remove".
func (s *companionServer) handleCloud(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, map[string]interface{}{"google": cloudGoogleStatusFor(), "webdav": cloudWebdavStatus()})
	case http.MethodPost:
		var call cloudCall
		if err := json.NewDecoder(io.LimitReader(r.Body, companionCloudBodyLimit)).Decode(&call); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		switch call.Action {
		case "google":
			cloudPostGoogle(w, r, call)
		case "google-remove":
			st := cloudGoogleStatusFor()
			if st.Source != "config" {
				http.Error(w, "There is no Google client set on this page to remove", http.StatusUnprocessableEntity)
				return
			}
			tbxPost(func() { cloudRemoveGoogleClient(appConfig) })
			logInfo("companion", "Google OAuth client removed from the phone")
			writeCompanionJSON(w, map[string]interface{}{"ok": true, "message": "Removed from the handheld."})
		case "webdav":
			cloudPostWebdav(w, r, call)
		case "webdav-remove":
			saveSyncCancelFull()
			webdavForget()
			tbxPost(func() {
				cloudScreen.webdav = nil
				ssyncScreen.model.builtAt = time.Time{}
				ShowToast("WebDAV server forgotten (from your phone)", ToastKindInfo)
			})
			writeCompanionJSON(w, map[string]interface{}{"ok": true, "message": "The handheld forgot the server. Your saves on it stay."})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// cloudPostGoogle checks a client the phone sent with Google and hands it to
// the main loop.
func cloudPostGoogle(w http.ResponseWriter, r *http.Request, call cloudCall) {
	if cloudGoogleStatusFor().Source == "environment" {
		http.Error(w, "This JukaHub was started with JUKAHUB_GOOGLE_CLIENT_ID set, which comes first — remove it from the environment to use a client set here", http.StatusUnprocessableEntity)
		return
	}
	var id, secret string
	var err error
	if strings.TrimSpace(call.JSON) != "" {
		id, secret, err = gdriveParseClientJSON([]byte(call.JSON))
	} else if id, err = gdriveCleanClientID(call.ID); err == nil {
		secret, err = gdriveCleanClientSecret(call.Secret)
	}
	if err != nil {
		http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
		return
	}
	client := gdriveClient{ID: id, Secret: secret, Source: "config"}
	ctx, cancel := context.WithTimeout(r.Context(), companionCloudCheckWait)
	dc, err := gdriveRequestDeviceCode(ctx, client)
	cancel()
	var oe *gdriveOAuthError
	if errors.As(err, &oe) {
		// invalid_client ("Invalid client type") for a Desktop or Web client,
		// or one deleted in the console.
		http.Error(w, companionCloudSentence(errors.New(gdriveErrorText(err))), http.StatusUnprocessableEntity)
		return
	}
	verified := err == nil
	if err != nil {
		logInfo("companion", "could not ask Google about the client from the phone: %s", gdriveErrorText(err))
	}
	enc, err := EncryptAPIToken(secret, "Google OAuth client secret")
	if err != nil {
		http.Error(w, "The handheld could not store the secret", http.StatusInternalServerError)
		return
	}
	var pre *gdriveDeviceCode
	if verified {
		d := dc
		pre = &d
	}
	tbxPost(func() { cloudApplyGoogleClient(appConfig, id, enc, pre) })
	logInfo("companion", "Google OAuth client received from the phone (checked with Google: %v)", verified)
	out := map[string]interface{}{"ok": true, "verified": verified, "clientId": id}
	if verified {
		out["message"] = "Saved on the handheld. Now sign in: open the address below, enter the code and allow access."
		out["code"], out["url"] = dc.UserCode, dc.url()
	} else {
		out["message"] = "Saved on the handheld. JukaHub could not reach Google to check it just now: press Sign in on the handheld when it is online."
	}
	writeCompanionJSON(w, out)
}

// cloudPostWebdav checks a server the phone sent and saves it.
func cloudPostWebdav(w http.ResponseWriter, r *http.Request, call cloudCall) {
	cur := webdavSettingsGet()
	base, err := webdavCleanURL(call.URL)
	if err != nil {
		http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
		return
	}
	user, err := companionCleanCredential(call.User, webdavUserMax, "user name")
	if err == nil && strings.ContainsAny(user, ":") {
		err = errors.New("a user name with ':' cannot sign in with Basic auth")
	}
	if err != nil {
		http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
		return
	}
	password, err := companionCleanCredential(call.Password, webdavPasswordMax, "password")
	if err != nil {
		http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
		return
	}
	if password == "" && cur.connected() && cur.User == user && webdavSameServer(cur.URL, base) {
		// The saved password, for another folder on the same server and
		// account. Never for another server: the page could otherwise send
		// the saved password somewhere new without anyone typing it.
		if pw, derr := DecryptString(cur.Password); derr == nil {
			password = pw
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), companionCloudCheckWait)
	defer cancel()
	// No identity: a refusal here must not mark the saved connection.
	conn, err := webdavNewConn(base, user, password, "")
	if err != nil {
		http.Error(w, "That address cannot be used", http.StatusUnprocessableEntity)
		return
	}
	verdict, reason := webdavCheck(ctx, conn)
	found := ""
	if verdict == groqKeyRejected && base.Path == "/" && user != "" {
		// A Nextcloud or ownCloud address typed without its WebDAV path.
		if guess, gerr := url.Parse(base.String() + "remote.php/dav/files/" + url.PathEscape(user) + "/"); gerr == nil {
			if c2, cerr := webdavNewConn(guess, user, password, ""); cerr == nil {
				if v2, _ := webdavCheck(ctx, c2); v2 == groqKeyAccepted {
					base, verdict, found = guess, v2, guess.String()
				}
			}
		}
	}
	if verdict == groqKeyRejected {
		http.Error(w, reason, http.StatusUnprocessableEntity)
		return
	}
	enc, err := EncryptAPIToken(password, "WebDAV password")
	if err != nil {
		http.Error(w, "The handheld could not store the password", http.StatusInternalServerError)
		return
	}
	addr := base.String()
	err = webdavSettingsUpdate(func(st *webdavSettings) {
		changed := st.URL != addr || st.User != user
		st.URL, st.User, st.Password, st.Refused = addr, user, enc, false
		if changed {
			// Another server or account: its own listing mode and history, and
			// the automatic sync on, as after a Google sign-in.
			st.NoInfinity, st.LastResult, st.LastAt, st.Auto = false, "", time.Time{}, true
		}
	})
	if err != nil {
		http.Error(w, "The handheld could not save it: "+firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	tbxPost(webdavPhoneArrived)
	logInfo("companion", "WebDAV server received from the phone (checked: %v, scheme %s)", verdict == groqKeyAccepted, base.Scheme)
	msg := "Saved on the handheld. The server accepted the sign-in."
	switch {
	case found != "":
		msg = "Saved on the handheld. JukaHub found the WebDAV folder at " + found + "."
	case verdict != groqKeyAccepted:
		msg = "Saved on the handheld. JukaHub could not reach the server to check it just now; it syncs when it can."
	}
	out := cloudWebdavStatus()
	out["ok"], out["verified"], out["message"] = true, verdict == groqKeyAccepted, msg
	writeCompanionJSON(w, out)
}

// cloudPhonePage is the /cloud page: the Google card (status, the steps, the
// client file or ID and secret, the sign-in code), the WebDAV card and the
// route through a computer.
const cloudPhonePage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub · Cloud save sync</title>
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
input{width:100%;margin-top:8px;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif}
input[type=file]{padding:10px}
button{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
button.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button:disabled{opacity:.6}
.row{display:flex;gap:10px;margin-top:12px;flex-wrap:wrap}
.row>*{flex:1 1 140px}
a{color:#a8b5ff}
ol{margin:6px 0 4px;padding-left:22px;color:var(--ink);font-size:14px}
ol li{margin:4px 0}
code{background:#0f131b;border:1px solid var(--line);border-radius:6px;padding:1px 5px;font-size:13px}
details{margin-top:8px}
summary{color:#a8b5ff;cursor:pointer;padding:6px 0}
.msg{margin-top:10px;font-size:14px;color:var(--dim)}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
.code{margin-top:12px;padding:14px;border:1px solid var(--accent);border-radius:12px;text-align:center}
.code b{display:block;font-size:30px;letter-spacing:.12em;margin:6px 0}
small{color:var(--dim);display:block;margin-top:10px;font-size:12px}
[hidden]{display:none!important}
</style>
<h1>Cloud save sync</h1>
<p class=note>Your saves, the same on every JukaHub. Pick one of these; Tools &gt; Sync on the handheld shows the result. The <a id=syncLink href="#">Sync page</a> also pairs another JukaHub, RomM and Syncthing.</p>

<div class=card id=googleCard>
  <h2>Google Drive</h2>
  <p class=note id=gStatus>Checking the handheld...</p>
  <div class=code id=gCode hidden>Open <a id=gLink target=_blank rel="noopener noreferrer" href="https://www.google.com/device">google.com/device</a>, enter<b id=gCodeText></b>choose your Google account and allow access. The handheld notices by itself.</div>
  <details id=gSteps>
    <summary>Set up your own Google client (free, about 10 minutes, once)</summary>
    <ol>
      <li>On a computer, open <a href="https://console.cloud.google.com/projectcreate" target=_blank rel="noopener noreferrer">console.cloud.google.com</a> and create a project, for example "JukaHub".</li>
      <li><b>APIs &amp; Services &gt; Library</b>: find <b>Google Drive API</b> and press <b>Enable</b>.</li>
      <li><b>Google Auth Platform &gt; Branding</b>: app name "JukaHub", your email twice. No logo.</li>
      <li><b>Audience</b>: user type <b>External</b>, then <b>Publish app</b>. (Left in Testing, only test users you add can sign in, and Google signs them out every 7 days.)</li>
      <li><b>Data Access &gt; Add or remove scopes</b>: add <code>.../auth/drive.file</code> and save.</li>
      <li><b>Clients &gt; Create client</b>: application type <b>TVs and Limited Input devices</b>, name "JukaHub", Create, then <b>Download JSON</b>.</li>
      <li>Send that file to this phone (or open this page on the computer), pick it below and tap Save.</li>
    </ol>
  </details>
  <input type=file id=gFile accept=".json,application/json">
  <input type=text id=gId placeholder="Or paste the Client ID (...apps.googleusercontent.com)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=gSecret placeholder="and the Client secret (GOCSPX-...)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=gSave>Save on handheld</button><button class=ghost id=gRemove hidden>Remove this client</button></div>
  <div class=msg id=gMsg aria-live=polite></div>
  <small>JukaHub asks only for drive.file: it sees the files it made itself ("JukaHub Saves" in My Drive) and nothing else. The handheld checks the client with Google, keeps the secret encrypted and never shows it again.</small>
</div>

<div class=card id=webdavCard>
  <h2>WebDAV: Nextcloud, ownCloud, Koofr, a NAS</h2>
  <p class=note id=dStatus>Checking the handheld...</p>
  <input type=url id=dURL placeholder="https://cloud.example.com/remote.php/dav/files/you/" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=text id=dUser placeholder="User name" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=dPass placeholder="App password" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=dSave>Save on handheld</button><button class=ghost id=dRemove hidden>Forget this server</button></div>
  <div class=msg id=dMsg aria-live=polite></div>
  <small>Nextcloud and ownCloud: https://SERVER/remote.php/dav/files/USER/ with an app password (Settings &gt; Security &gt; Devices &amp; sessions). Koofr: https://app.koofr.net/dav/Koofr/ with an app password (Preferences &gt; Password). InfiniCLOUD: the WebDAV address and Apps password on My Page. A NAS: its WebDAV address. It must be https:// unless the server is on your home network: the password goes with every request, and over plain http anyone on the way could read it. JukaHub makes one folder there, "JukaHub Saves". Leave the password empty to keep the saved one. Google Drive has no WebDAV.</small>
</div>

<div class=card>
  <h2>Google Drive through a computer</h2>
  <p class=note>No Google project at all: on a PC or Mac with Google Drive for desktop, install Syncthing (syncthing.net). On the handheld open Start &gt; Save Manager &gt; Syncthing, switch it on and pair the computer. On the computer, accept the save folders and put them inside the Google Drive folder. The computer must be on for saves to move.</p>
</div>

<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const $ = id => document.getElementById(id);
let waiting = false;
function hdr(json) { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; }
async function post(body) {
  const res = await fetch('/api/cloud', {method: 'POST', headers: hdr(true), body: JSON.stringify(body)});
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  try { return JSON.parse(text); } catch (_) { return {}; }
}
function say(id, text, bad) { const el = $(id); el.textContent = text || ''; el.className = bad ? 'msg bad' : 'msg'; }
function showCode(code, url) {
  const box = $('gCode');
  box.hidden = !code;
  if (!code) return;
  const link = url || 'https://www.google.com/device';
  $('gCodeText').textContent = code;
  $('gLink').href = link;
  $('gLink').textContent = link.replace(/^https:\/\/(www\.)?/, '');
}
function showGoogle(g) {
  let line;
  if (g.signedIn && !g.expired) line = 'Signed in to Google Drive as ' + (g.account || 'your account') + '.';
  else if (g.signedIn) line = 'The sign-in for ' + (g.account || 'your account') + ' expired: press Sign in on the handheld.';
  else if (g.source === 'build') line = 'This JukaHub has Google sign-in built in: sign in from the Sync page, or on the handheld (Tools > Sync). The steps below are only for using a client of your own.';
  else if (g.source === 'environment') line = 'A Google client is set in this JukaHub\'s environment, and it comes first.';
  else if (g.source === 'config') line = 'Your own Google client is set (' + g.clientId + '). Press Sign in on the handheld, or send another one.';
  else line = 'No Google client on this JukaHub yet: follow the steps below once.';
  $('gStatus').textContent = line;
  $('gRemove').hidden = g.source !== 'config';
  if (!g.source && !g.signedIn) $('gSteps').open = true;
  waiting = !!g.waiting;
  showCode(g.waiting ? g.code : '', g.url);
}
function showWebdav(d) {
  let line = 'Not connected. Type the server\'s WebDAV address, your user name and an app password.';
  if (d.set && d.refused) line = 'The server refused the saved password: send it again.';
  else if (d.set) line = 'Connected to ' + d.url + (d.user ? ' as ' + d.user : '') + (d.last ? '. Last sync: ' + d.last : '') + '.';
  $('dStatus').textContent = line;
  $('dRemove').hidden = !d.set;
  if (d.set) {
    if (!$('dURL').value) $('dURL').value = d.url || '';
    if (!$('dUser').value) $('dUser').value = d.user || '';
  }
}
async function load() {
  const res = await fetch('/api/cloud', {headers: hdr(false), cache: 'no-store'});
  if (!res.ok) throw new Error((await res.text()).trim() || 'Could not reach the handheld');
  const data = await res.json();
  showGoogle(data.google || {});
  showWebdav(data.webdav || {});
}
function tick() {
  const next = () => setTimeout(tick, waiting ? 3000 : 15000);
  if (document.hidden) { next(); return; }
  load().catch(e => { $('gStatus').textContent = e.message; }).finally(next);
}
async function busy(id, label, fn) {
  const b = $(id); const old = b.textContent;
  b.disabled = true; b.textContent = label;
  try { await fn(); } finally { b.disabled = false; b.textContent = old; }
}
$('gSave').onclick = () => busy('gSave', 'Checking with Google...', async () => {
  const body = {action: 'google'};
  const f = $('gFile').files[0];
  if (f) {
    if (f.size > 16384) { say('gMsg', 'That file is far larger than a Google client file', true); return; }
    body.json = await f.text();
  } else {
    body.id = $('gId').value; body.secret = $('gSecret').value;
    if (!body.id.trim()) { say('gMsg', 'Pick the client file, or paste the Client ID and secret', true); return; }
  }
  try {
    const r = await post(body);
    say('gMsg', r.message);
    $('gSecret').value = ''; $('gFile').value = '';
    if (r.code) { waiting = true; showCode(r.code, r.url); }
    setTimeout(() => load().catch(() => {}), 1500);
  } catch (e) { say('gMsg', e.message, true); }
});
$('gRemove').onclick = () => busy('gRemove', 'Removing...', async () => {
  if (!confirm('Remove your Google client from the handheld? A sign-in made with it ends too.')) return;
  try { const r = await post({action: 'google-remove'}); say('gMsg', r.message); setTimeout(() => load().catch(() => {}), 800); }
  catch (e) { say('gMsg', e.message, true); }
});
$('dSave').onclick = () => busy('dSave', 'Checking with the server...', async () => {
  const body = {action: 'webdav', url: $('dURL').value, user: $('dUser').value, password: $('dPass').value};
  if (!body.url.trim()) { say('dMsg', 'Type the server\'s WebDAV address first', true); return; }
  try {
    const r = await post(body);
    say('dMsg', r.message);
    $('dPass').value = '';
    if (r.url) $('dURL').value = r.url;
    showWebdav(r);
  } catch (e) { say('dMsg', e.message, true); }
});
$('dRemove').onclick = () => busy('dRemove', 'Forgetting...', async () => {
  if (!confirm('Forget this WebDAV server on the handheld? Your saves on it stay.')) return;
  try { const r = await post({action: 'webdav-remove'}); say('dMsg', r.message); $('dURL').value = ''; $('dUser').value = ''; await load(); }
  catch (e) { say('dMsg', e.message, true); }
});
$('syncLink').href = '/sync?p=' + encodeURIComponent(pin);
const picked = location.hash.slice(1);
if (picked === 'google' || picked === 'webdav') {
  const card = $(picked + 'Card');
  card.classList.add('picked');
  card.scrollIntoView({block: 'start'});
}
tick();
</script>
`
