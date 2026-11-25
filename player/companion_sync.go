package main

// companion_sync.go — the phone's side of Sync (synchub.go): the /sync page
// the code at the top of Sync opens, and its /api/sync calls, behind the
// companion's PIN like everything else on the phone.
//
// One page connects all five ways saves follow the player, a card each:
//
//   - Another JukaHub: the JukaHubs this handheld finds on the Wi-Fi, each
//     with Pair. The handheld starts the pairing (peerpair.go) and the phone
//     stands in for its screen: it shows the six-digit code and Codes match,
//     while the other JukaHub shows the same code to confirm there. The phone
//     holds the PIN from this handheld's own screen, so its answer is this
//     user's answer; the code on the other screen keeps a device in the middle
//     out, exactly as when both screens are handhelds. A request another
//     JukaHub sent can be answered here too.
//   - Google Drive: Sign in starts the device sign-in on the handheld, and
//     the page shows Google's code with a link to google.com/device, so the
//     phone in hand approves it; with no Google client yet it links to the
//     /cloud page's steps (companion_cloud.go).
//   - WebDAV: the /cloud page's form, posting to /api/cloud.
//   - RomM: the server's address and the code RomM shows under Client API
//     Tokens → Pair device (RomM 4.8 and later). The handheld trades the code
//     for a token itself (POST /api/client-tokens/exchange, which needs no
//     other credential), checks it as it checks a pasted token and keeps it;
//     the token never reaches the phone. A token can still be pasted
//     (/api/romm, companion_accounts.go).
//   - Syncthing: switch it on (the first time downloads it), this device's ID
//     to copy into Syncthing on a computer or on Android, the requests to
//     accept, and an ID pasted here to add a device from this side.
//
// The page polls GET /api/sync; each call also tells the handheld a phone is
// on the page (syncHubNotePhone), which keeps the peer layer up and closes the
// full-screen code. Nothing secret goes back: no token, password or key, only
// names, addresses and states.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	// phoneCardSync is the Sync page's own code (shown full screen).
	phoneCardSync phoneCardKind = "sync"
	// syncPhoneBodyLimit bounds one call.
	syncPhoneBodyLimit = 8 << 10
	// rommPairCodeMin and rommPairCodeMax bound a RomM pairing code once its
	// dashes and spaces are gone (RomM shows eight characters).
	rommPairCodeMin = 6
	rommPairCodeMax = 16
)

// syncPhoneURL is the companion's pairing link pointed at /sync, with a card's
// fragment; "" when the link is not the companion's.
func syncPhoneURL(link string, kind phoneCardKind) string {
	if !strings.Contains(link, "/?p=") || strings.Contains(link, "#") {
		return ""
	}
	out := strings.Replace(link, "/?p=", "/sync?p=", 1)
	if kind != "" {
		out += "#" + string(kind)
	}
	return out
}

// ── Status ───────────────────────────────────────────────────────────────

// syncPhonePeer is a JukaHub as the page lists it.
type syncPhonePeer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Online bool   `json:"online"`
	Last   string `json:"last,omitempty"`
}

// syncPhonePairing is the pairing the page shows: stage is "asking",
// "compare", "waiting" (this side said yes), "done" or "failed".
type syncPhonePairing struct {
	Token     int64  `json:"token"`
	Initiator bool   `json:"initiator"`
	Name      string `json:"name"`
	Stage     string `json:"stage"`
	Code      string `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// syncPhoneSTDevice is a Syncthing device or request.
type syncPhoneSTDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected,omitempty"`
}

// syncPhoneSyncthing is Syncthing as the page shows it.
type syncPhoneSyncthing struct {
	On        bool                `json:"on"`
	Installed bool                `json:"installed"`
	Running   bool                `json:"running"`
	Status    string              `json:"status"`
	Detail    string              `json:"detail,omitempty"`
	ID        string              `json:"id,omitempty"`
	Devices   []syncPhoneSTDevice `json:"devices"`
	Pending   []syncPhoneSTDevice `json:"pending"`
}

// syncPhoneStatus is GET /api/sync.
type syncPhoneStatus struct {
	Device    string                 `json:"device"`
	Model     string                 `json:"model"`
	Auto      bool                   `json:"auto"`
	Online    bool                   `json:"online"`
	LAN       bool                   `json:"lan"`
	Busy      string                 `json:"busy,omitempty"`
	Paired    []syncPhonePeer        `json:"paired"`
	Nearby    []syncPhonePeer        `json:"nearby"`
	Pairing   *syncPhonePairing      `json:"pairing,omitempty"`
	Google    cloudGoogleStatus      `json:"google"`
	WebDAV    map[string]interface{} `json:"webdav"`
	RomM      map[string]interface{} `json:"romm"`
	Syncthing syncPhoneSyncthing     `json:"syncthing"`
}

// syncPhoneStatusNow describes everything for the page. Any goroutine: every
// part is read under its own lock or from an atomic mirror, and Google's
// state comes from the main loop (cloudGoogleStatusFor).
func syncPhoneStatusNow() syncPhoneStatus {
	now := time.Now()
	st := syncPhoneStatus{
		Device: peerDeviceName(),
		Model:  peerModelName(),
		Auto:   syncAutoFlag.Load(),
		Online: IsOnline(),
		LAN:    partyLANAddress() != "",
		Paired: []syncPhonePeer{},
		Nearby: []syncPhonePeer{},
	}
	if work := ssyncWorkSnapshot(); work.busy {
		st.Busy = work.label
	}
	paired := map[string]bool{}
	for _, p := range peerPairedList() {
		paired[p.ID] = true
		pp := syncPhonePeer{ID: p.ID, Name: p.Name, Model: setOr(p.Model, "JukaHub"), Online: peerOnline(p.ID)}
		if last := ssyncLastSync(p.ID); !last.IsZero() {
			pp.Last = peerAgo(now, last)
		}
		st.Paired = append(st.Paired, pp)
	}
	if peerEnabledFlag.Load() {
		for _, d := range peerSeenList() {
			if !paired[d.ID] {
				st.Nearby = append(st.Nearby, syncPhonePeer{ID: d.ID, Name: d.Name, Model: setOr(d.Model, "JukaHub"), Online: true})
			}
		}
	}
	st.Pairing = syncPhonePairingNow()
	st.Google = cloudGoogleStatusFor()
	st.WebDAV = cloudWebdavStatus()
	st.RomM = companionRomMStatus()
	r := rommServerSettingsGet()
	st.RomM["auto"] = r.AutoSync == nil || *r.AutoSync
	if busy, line := rommSyncAllStatus(); busy {
		st.RomM["busy"] = setOr(line, "Syncing every save…")
	}
	st.Syncthing = syncPhoneSyncthingNow()
	return st
}

// syncPhonePairingNow is the pairing to show: a live one first, else the
// latest that ended (the page shows how it ended only if it saw it run).
func syncPhonePairingNow() *syncPhonePairing {
	in, out := peerPairViews()
	v := out
	switch {
	case out.live:
	case in.live, in.token > out.token:
		v = in
	}
	if v.token == 0 {
		return nil
	}
	p := &syncPhonePairing{Token: v.token, Initiator: v.initiator, Name: v.name, Reason: v.reason}
	switch {
	case v.stage == peerPairDone:
		p.Stage = "done"
	case v.stage == peerPairFailed || !v.live:
		p.Stage = "failed"
		if p.Reason == "" {
			p.Reason = "Pairing timed out — try again"
		}
	case v.stage == peerPairCompare && v.localOK:
		p.Stage, p.Code = "waiting", peerFormatCode(v.code)
	case v.stage == peerPairCompare:
		p.Stage, p.Code = "compare", peerFormatCode(v.code)
	case v.initiator:
		p.Stage = "asking"
	default:
		// Another device's request shows once its code is up.
		return nil
	}
	return p
}

// syncPhoneSyncthingNow is Syncthing's state for the page (any goroutine).
func syncPhoneSyncthingNow() syncPhoneSyncthing {
	s := syncthingSnapshotNow()
	want := syncthingWantOn.Load()
	out := syncPhoneSyncthing{
		On:        want,
		Installed: s.Installed,
		Running:   s.Phase == syncthingPhaseRunning,
		Status:    syncthingStatusText(s, want),
		Detail:    s.Detail,
		Devices:   []syncPhoneSTDevice{},
		Pending:   []syncPhoneSTDevice{},
	}
	if out.Running {
		out.ID = s.MyID
	}
	for _, d := range s.Devices {
		out.Devices = append(out.Devices, syncPhoneSTDevice{ID: d.ID, Name: setOr(d.Name, syncthingShortID(d.ID)), Connected: d.Connected})
	}
	if out.Running {
		for _, p := range s.Pending {
			out.Pending = append(out.Pending, syncPhoneSTDevice{ID: p.ID, Name: setOr(p.Name, syncthingShortID(p.ID))})
		}
	}
	return out
}

// ── Calls ────────────────────────────────────────────────────────────────

// registerSyncRoutes adds the page and its calls (companion.go's handler).
func (s *companionServer) registerSyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/sync", s.handleSyncPage)
	mux.HandleFunc("/api/sync", s.guard(s.handleSyncAPI))
}

// handleSyncPage serves /sync (the PIN in its address, as the code carries
// it).
func (s *companionServer) handleSyncPage(w http.ResponseWriter, r *http.Request) {
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
	_, _ = io.WriteString(w, syncPhonePage)
}

// syncPhoneCall is one POST to /api/sync.
type syncPhoneCall struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	On     bool   `json:"on"`
	Accept bool   `json:"accept"`
	URL    string `json:"url"`
	Code   string `json:"code"`
}

// handleSyncAPI answers the page: GET is the status (scan=1 also looks for
// JukaHubs on the Wi-Fi), POST one action.
func (s *companionServer) handleSyncAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	syncHubNotePhone()
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("scan") == "1" {
			syncHubPhoneScan()
		}
		writeCompanionJSON(w, syncPhoneStatusNow())
	case http.MethodPost:
		var call syncPhoneCall
		if err := json.NewDecoder(io.LimitReader(r.Body, syncPhoneBodyLimit)).Decode(&call); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		syncPhoneAct(w, r, call)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// syncPhoneOK answers a call that the main loop carries out.
func syncPhoneOK(w http.ResponseWriter, msg string) {
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "message": msg})
}

// syncPhoneAct runs one action. What changes the handheld's screens is
// posted to the main loop; the page sees the result on its next poll.
func syncPhoneAct(w http.ResponseWriter, r *http.Request, call syncPhoneCall) {
	switch call.Action {
	case "pair":
		id := strings.ToLower(strings.TrimSpace(call.ID))
		dev, ok := peerSeenDeviceByID(id)
		if !ok {
			http.Error(w, "That JukaHub stopped answering: check that it is on this Wi-Fi with Sync open", http.StatusConflict)
			return
		}
		if _, paired := peerPairedRecord(id); paired {
			http.Error(w, dev.Name+" is paired already", http.StatusConflict)
			return
		}
		tbxPost(func() {
			if safeModeAllows(SafeFeatureNetwork) {
				peerApplyEnabled(true)
			}
			peerPairStart(dev)
		})
		logInfo("companion", "pairing with another JukaHub started from the phone")
		syncPhoneOK(w, "Asking "+dev.Name+"… the same code appears here and on "+dev.Name+".")
	case "pair-answer":
		accept := call.Accept
		tbxPost(func() { syncPhonePairAnswer(accept) })
		if accept {
			syncPhoneOK(w, "Confirmed here.")
			return
		}
		syncPhoneOK(w, "Pairing cancelled.")
	case "auto":
		on := call.On
		tbxPost(func() { syncAutoSet(appConfig, on) })
		if on {
			syncPhoneOK(w, "Automatic sync is on.")
			return
		}
		syncPhoneOK(w, "Automatic sync is off: saves move only when you press Sync now.")
	case "sync-now":
		tbxPost(func() { syncHubStartAll(appConfig) })
		syncPhoneOK(w, "Syncing: the handheld shows how it goes.")
	case "google-signin":
		syncPhoneGoogleSignIn(w)
	case "google-signout":
		tbxPost(func() {
			saveSyncCancelFull()
			gdriveSignOut()
			syncHub.builtAt = time.Time{}
			ssyncScreen.model.builtAt = time.Time{}
			ShowToast("Signed out of Google Drive (from your phone)", ToastKindInfo)
		})
		logInfo("companion", "Google Drive sign-out from the phone")
		syncPhoneOK(w, "Signed out. Your saves on Drive stay there.")
	case "romm-pair":
		syncPhoneRommPair(w, r, call)
	case "romm-forget":
		tbxPost(func() { syncHubForgetRomm(true) })
		syncPhoneOK(w, "The handheld forgot the RomM server. Your saves and games on it stay.")
	case "syncthing-on":
		tbxPost(func() { syncHubSyncthingOn(appConfig) })
		if !syncthingInstalled() {
			syncPhoneOK(w, "Downloading Syncthing (about 12 MB): it starts by itself.")
			return
		}
		syncPhoneOK(w, "Starting Syncthing…")
	case "syncthing-off":
		tbxPost(func() { syncHubSyncthingOff(appConfig) })
		syncPhoneOK(w, "Syncthing is off. Its folders and devices stay for next time.")
	case "syncthing-add":
		id, ok := syncthingCleanID(call.ID)
		if !ok {
			http.Error(w, "That is not a Syncthing device ID: it is 8 groups of 7 letters and digits", http.StatusUnprocessableEntity)
			return
		}
		if syncthingSnapshotNow().Phase != syncthingPhaseRunning {
			http.Error(w, "Syncthing is not running on the handheld yet", http.StatusConflict)
			return
		}
		name := peerCleanName(call.Name)
		tbxPost(func() { syncHubSyncthingAdd(id, name, false) })
		syncPhoneOK(w, "Added. On that device, accept JukaHub and the save folders it offers.")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// syncPhonePairAnswer is Codes match or Cancel from the phone, for the
// pairing this handheld started or the request it is answering. Main thread.
func syncPhonePairAnswer(accept bool) {
	in, out := peerPairViews()
	switch {
	case out.live:
		peerPairAnswerOutgoing(accept)
	case in.live && in.stage == peerPairCompare:
		peerPairAnswerIncoming(accept)
	}
}

// syncPhoneGoogleSignIn starts Google's device sign-in on the handheld; the
// page shows the code from the next status.
func syncPhoneGoogleSignIn(w http.ResponseWriter) {
	st := cloudGoogleStatusFor()
	switch {
	case st.Source == "":
		http.Error(w, "This JukaHub has no Google sign-in yet: set up a Google client first", http.StatusUnprocessableEntity)
		return
	case st.SignedIn && !st.Expired:
		http.Error(w, "Signed in already as "+st.Account, http.StatusConflict)
		return
	case !IsOnline():
		http.Error(w, "The handheld is not on the internet right now", http.StatusServiceUnavailable)
		return
	}
	tbxPost(func() { gdriveStartSignIn(appConfig) })
	logInfo("companion", "Google sign-in started from the phone")
	syncPhoneOK(w, "Asking Google for a code…")
}

// ── RomM pairing code ────────────────────────────────────────────────────

// rommPairInput splits what the phone sent into a server address and a
// pairing code: the two fields, or RomM's pairing link pasted into either
// ("https://romm.example/pair?code=ABCD-1234"). A bare host gets http://, as
// the token card assumes for a server on the home network. Pure.
func rommPairInput(rawURL, rawCode string) (server, code string) {
	server, code = strings.TrimSpace(rawURL), strings.TrimSpace(rawCode)
	for _, s := range []string{code, server} {
		if !strings.Contains(s, "://") || !strings.Contains(strings.ToLower(s), "code=") {
			continue
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			continue
		}
		c := u.Query().Get("code")
		if c == "" {
			if i := strings.Index(u.Fragment, "?"); i >= 0 {
				if q, qerr := url.ParseQuery(u.Fragment[i+1:]); qerr == nil {
					c = q.Get("code")
				}
			}
		}
		if c == "" {
			continue
		}
		p := u.Path
		if i := strings.Index(strings.ToLower(p), "/pair"); i >= 0 {
			p = p[:i]
		}
		return u.Scheme + "://" + u.Host + strings.TrimSuffix(p, "/"), c
	}
	if server != "" && !strings.Contains(server, "://") {
		server = "http://" + server
	}
	return server, code
}

// rommCleanPairCode drops the dashes and spaces RomM shows a code with and
// upper-cases it (RomM ignores both). Pure.
func rommCleanPairCode(raw string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || unicode.IsSpace(r):
		default:
			return "", false
		}
	}
	c := b.String()
	return c, len(c) >= rommPairCodeMin && len(c) <= rommPairCodeMax
}

// syncthingCleanID turns a pasted Syncthing device ID — with or without its
// dashes, in any case — into the dashed form. Pure.
func syncthingCleanID(raw string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '2' && r <= '7':
			b.WriteRune(r)
		case r == '-' || unicode.IsSpace(r):
		default:
			return "", false
		}
	}
	plain := b.String()
	if len(plain) != 56 {
		return "", false
	}
	var out strings.Builder
	for i := 0; i < len(plain); i += 7 {
		if i > 0 {
			out.WriteByte('-')
		}
		out.WriteString(plain[i : i+7])
	}
	id := out.String()
	return id, syncthingLooksLikeID(id)
}

// rommExchangePairCode trades a pairing code from RomM's Client API Tokens →
// Pair device for the token itself (RomM 4.8 and later). The call carries no
// other credential: the code is the proof, works once and lasts minutes.
// Redirects are not followed (a POST would arrive as a GET).
func rommExchangePairCode(ctx context.Context, base *url.URL, code string) (string, error) {
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + "/api/client-tokens/exchange"
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	body, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("no RomM server answered at that address: check it and the port")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch status := resp.StatusCode; {
	case status >= 300 && status < 400:
		return "", errors.New("the server sends that address elsewhere (" + firstLine(resp.Header.Get("Location")) + "): type that address instead")
	case status == http.StatusTooManyRequests:
		return "", errors.New("RomM asks to wait a minute before the next try")
	case status == http.StatusNotFound, status == http.StatusGone:
		return "", errors.New("RomM does not know this code: a code works once, for 5 minutes, so press Pair device again (RomM older than 4.8 has no codes: paste a token instead)")
	case status == http.StatusMethodNotAllowed:
		return "", errors.New("this RomM has no pairing codes (it needs 4.8 or later): paste a client API token instead")
	case status < 200 || status >= 300:
		return "", fmt.Errorf("RomM refused the code (%d): press Pair device again", status)
	}
	var out struct {
		RawToken string `json:"raw_token"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", errors.New("RomM answered with something that is not a token: update RomM to 4.8 or later, or paste a token")
	}
	tok := strings.TrimSpace(out.RawToken)
	if tok == "" {
		tok = strings.TrimSpace(out.Token)
	}
	if tok == "" || len(tok) > companionRomMTokenMax || strings.ContainsAny(tok, " \t\r\n") {
		return "", errors.New("RomM answered without a usable token: paste a token instead")
	}
	return tok, nil
}

// syncPhoneRommPair connects RomM with a pairing code from the phone: the
// code becomes a token, the token is checked like a pasted one, and the
// connection is kept.
func syncPhoneRommPair(w http.ResponseWriter, r *http.Request, call syncPhoneCall) {
	server, rawCode := rommPairInput(call.URL, call.Code)
	if server == "" {
		http.Error(w, "Type your RomM server's address, like http://192.168.1.20:8080", http.StatusUnprocessableEntity)
		return
	}
	base, err := rommServerBase(server)
	if err != nil {
		http.Error(w, "Enter the server's full address, like http://192.168.1.20:8080", http.StatusUnprocessableEntity)
		return
	}
	code, ok := rommCleanPairCode(rawCode)
	if !ok {
		http.Error(w, "Type the code RomM shows under Pair device (8 letters and digits)", http.StatusUnprocessableEntity)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), companionAccountCheckWait)
	token, err := rommExchangePairCode(ctx, base, code)
	cancel()
	if err != nil {
		http.Error(w, companionCloudSentence(err), http.StatusUnprocessableEntity)
		return
	}
	next := rommServerSettingsGet()
	next.URL, next.Token, next.DeviceID = base.String(), token, ""
	ctx, cancel = context.WithTimeout(r.Context(), companionAccountCheckWait)
	verdict, reason := companionCheckRomM(ctx, next)
	cancel()
	if verdict == groqKeyRejected {
		http.Error(w, reason, http.StatusUnprocessableEntity)
		return
	}
	// Read again after the network calls: what the handheld changed
	// meanwhile (its automatic switch) stays.
	latest := rommServerSettingsGet()
	latest.URL, latest.Token, latest.DeviceID = next.URL, next.Token, ""
	if err := rommServerSettingsSave(latest); err != nil {
		http.Error(w, "The handheld could not save it: "+firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	tbxPost(rommPhoneArrived)
	logInfo("companion", "RomM connected from the phone with a pairing code (checked: %v)", verdict == groqKeyAccepted)
	msg := "Connected: RomM keeps this handheld's saves from now on."
	if verdict != groqKeyAccepted {
		msg = "Connected. The handheld could not check the token just now; it syncs when it reaches RomM."
	}
	out := companionRomMStatus()
	out["ok"], out["verified"], out["message"] = true, verdict == groqKeyAccepted, msg
	writeCompanionJSON(w, out)
}

// ── The page ─────────────────────────────────────────────────────────────

// syncPhonePage is /sync. Names from other devices are put on the page with
// textContent only, never as HTML.
const syncPhonePage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub · Sync</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--good:#3ecf8e;--bad:#ff6b6b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:calc(14px + env(safe-area-inset-top)) 16px calc(40px + env(safe-area-inset-bottom));max-width:560px;margin:0 auto}
h1{font-size:21px;margin:0 0 4px}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin:14px 0}
.card.picked{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
.note{color:var(--dim);font-size:14px;margin:0 0 10px}
input{width:100%;margin-top:8px;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.4 system-ui,-apple-system,sans-serif}
input[readonly]{font:13px/1.4 ui-monospace,Menlo,monospace}
button{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
button.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button:disabled{opacity:.6}
.row{display:flex;gap:10px;margin-top:12px;flex-wrap:wrap;align-items:center}
.row>*{flex:1 1 140px}
a{color:#a8b5ff}
ul{list-style:none;margin:0;padding:0}
li{display:flex;gap:10px;align-items:center;justify-content:space-between;padding:10px 0;border-top:1px solid var(--line)}
li:first-child{border-top:0}
li .what{flex:1 1 auto;min-width:0}
li .what b{display:block;font-weight:600;overflow:hidden;text-overflow:ellipsis}
li .what span{color:var(--dim);font-size:13px}
li button{flex:0 0 auto;min-height:40px;padding:9px 14px}
.dot{display:inline-block;width:8px;height:8px;border-radius:4px;background:var(--dim);margin-right:6px;vertical-align:middle}
.dot.on{background:var(--good)}
.msg{margin-top:10px;font-size:14px;color:var(--dim)}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
.code{margin-top:12px;padding:14px;border:1px solid var(--accent);border-radius:12px;text-align:center}
.code b{display:block;font-size:30px;letter-spacing:.12em;margin:6px 0}
.switch{display:flex;align-items:center;gap:10px;font-weight:600}
.switch input{width:22px;height:22px;margin:0}
small{color:var(--dim);display:block;margin-top:10px;font-size:12px}
details{margin-top:8px}
summary{color:#a8b5ff;cursor:pointer;padding:6px 0}
[hidden]{display:none!important}
</style>
<h1 id=title>Sync</h1>
<p class=note>Connect this JukaHub once and your saves follow you by themselves: before a game they come over, after it they go back.</p>

<div class=card id=topCard>
  <h2>Connected</h2>
  <ul id=places></ul>
  <p class=note id=nothing hidden>Nothing yet: pick one or more below.</p>
  <div class=row><label class=switch><input type=checkbox id=auto> Automatic sync</label><button id=syncNow>Sync everything now</button></div>
  <div class=msg id=topMsg aria-live=polite></div>
</div>

<div class=card id=deviceCard>
  <h2>Another JukaHub</h2>
  <div id=pairBox hidden>
    <p class=note id=pairText></p>
    <div class=code id=pairCodeBox hidden><span id=pairCodeLabel>Code</span><b id=pairCode></b></div>
    <div class=row id=pairButtons></div>
  </div>
  <p class=note id=nearNote>Open Sync on your other JukaHub (Tools, then Sync), on the same Wi-Fi. It shows up here.</p>
  <ul id=nearby></ul>
  <div id=pairedBox hidden><h2 style="margin-top:14px">Paired</h2><ul id=paired></ul></div>
  <div class=msg id=dMsg2 aria-live=polite></div>
</div>

<div class=card id=googleCard>
  <h2>Google Drive</h2>
  <p class=note id=gStatus>Checking the handheld...</p>
  <div class=code id=gCode hidden>Open <a id=gLink target=_blank rel="noopener noreferrer" href="https://www.google.com/device">google.com/device</a>, enter<b id=gCodeText></b>choose your Google account and allow access. The handheld notices by itself.</div>
  <div class=row><button id=gSignIn hidden>Sign in with Google</button><button class=ghost id=gSignOut hidden>Sign out</button><a id=gSetup hidden href="#">Set up Google sign-in (about 10 minutes, once)</a></div>
  <div class=msg id=gMsg aria-live=polite></div>
</div>

<div class=card id=webdavCard>
  <h2>Nextcloud, ownCloud, Koofr or a NAS (WebDAV)</h2>
  <p class=note id=dStatus>Checking the handheld...</p>
  <input type=url id=dURL placeholder="https://cloud.example.com/remote.php/dav/files/you/" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=text id=dUser placeholder="User name" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=password id=dPass placeholder="App password" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
  <div class=row><button id=dSave>Save on handheld</button><button class=ghost id=dRemove hidden>Forget this server</button></div>
  <div class=msg id=dMsg aria-live=polite></div>
  <small>Nextcloud and ownCloud: https://SERVER/remote.php/dav/files/USER/ with an app password. Koofr: https://app.koofr.net/dav/Koofr/. A NAS: its WebDAV address. It must be https:// unless the server is on your home network. JukaHub makes one folder there, "JukaHub Saves". Leave the password empty to keep the saved one.</small>
</div>

<div class=card id=rommCard>
  <h2>RomM server</h2>
  <p class=note id=rStatus>Checking the handheld...</p>
  <input type=url id=rURL placeholder="Server address, like http://192.168.1.20:8080" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <input type=text id=rCode placeholder="Pairing code, like ABCD-1234 (or paste RomM's pairing link)" autocomplete=off autocapitalize=characters autocorrect=off spellcheck=false>
  <div class=row><button id=rPair>Connect</button><button class=ghost id=rForget hidden>Forget this server</button></div>
  <div class=msg id=rMsg aria-live=polite></div>
  <small>In RomM: your avatar, then Client API Tokens. Make a token for this handheld with read and write access, press Pair device and type the code here within 5 minutes. Give each device its own token: pairing a token again moves it to the new device.</small>
  <details><summary>Paste a token instead</summary>
    <input type=password id=rToken placeholder="Client API token (rmm_...)" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false data-lpignore=true data-1p-ignore data-bwignore>
    <div class=row><button class=ghost id=rTokenSave>Save the token</button></div>
  </details>
</div>

<div class=card id=syncthingCard>
  <h2>Syncthing: a PC, a Mac or Android</h2>
  <p class=note id=sStatus>Checking the handheld...</p>
  <div class=row id=sOffRow hidden><button id=sOn>Turn on Syncthing</button></div>
  <div id=sIdBox hidden>
    <p class=note>This handheld's ID. In Syncthing on your computer, or Syncthing-Fork on Android: Add Remote Device, paste it, save. Then accept the request below.</p>
    <input type=text id=sID readonly>
    <div class=row><button class=ghost id=sCopy>Copy the ID</button></div>
  </div>
  <ul id=sPending></ul>
  <ul id=sDevices></ul>
  <details id=sAddBox hidden><summary>Add a device by its ID instead</summary>
    <input type=text id=sAddID placeholder="XXXXXXX-XXXXXXX-... (Syncthing: Actions, Show ID)" autocomplete=off autocapitalize=characters autocorrect=off spellcheck=false>
    <input type=text id=sAddName placeholder="Its name, like My PC" autocomplete=off>
    <div class=row><button class=ghost id=sAdd>Add</button></div>
  </details>
  <div class=row><button class=ghost id=sOff hidden>Turn off Syncthing</button></div>
  <div class=msg id=sMsg aria-live=polite></div>
  <small>Syncthing keeps the save folders in step whenever both are on, even over the internet. It pauses while a game runs. A computer with Google Drive for desktop can carry them on to Drive: put the folders it offers inside the Google Drive folder.</small>
</div>

<p class=note><a id=mainLink href="#">The rest of the phone companion</a></p>

<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const $ = id => document.getElementById(id);
let state = null, fast = false, seenPair = 0, firstLoad = true;
function hdr(json) { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; }
async function post(path, body) {
  const res = await fetch(path, {method: 'POST', headers: hdr(true), body: JSON.stringify(body)});
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  try { return JSON.parse(text); } catch (_) { return {}; }
}
const act = body => post('/api/sync', body);
function say(id, text, bad) { const el = $(id); el.textContent = text || ''; el.className = bad ? 'msg bad' : 'msg'; }
function node(tag, cls, text) { const el = document.createElement(tag); if (cls) el.className = cls; if (text !== undefined) el.textContent = text; return el; }
function item(title, sub, online, button, onClick, ghost) {
  const li = node('li');
  const what = node('div', 'what');
  const b = node('b');
  if (online !== null) b.appendChild(node('i', online ? 'dot on' : 'dot'));
  b.appendChild(document.createTextNode(title));
  what.appendChild(b);
  if (sub) what.appendChild(node('span', '', sub));
  li.appendChild(what);
  if (button) {
    const btn = node('button', ghost ? 'ghost' : '', button);
    btn.onclick = () => onClick(btn);
    li.appendChild(btn);
  }
  return li;
}
async function busy(btn, label, fn) {
  const old = btn.textContent; btn.disabled = true; btn.textContent = label;
  try { await fn(); } finally { btn.disabled = false; btn.textContent = old; }
}
function fill(list, items) { list.textContent = ''; items.forEach(i => list.appendChild(i)); }

function showTop(d) {
  $('title').textContent = 'Sync · ' + (d.device || 'JukaHub');
  const rows = [];
  (d.paired || []).forEach(p => rows.push(item(p.name, (p.online ? 'On this Wi-Fi' : 'Away') + (p.last ? ' · synced ' + p.last : ''), p.online)));
  const g = d.google || {};
  if (g.signedIn) rows.push(item('Google Drive', g.expired ? 'Sign in again below' : (g.account || ''), !g.expired));
  const w = d.webdav || {};
  if (w.set) rows.push(item('WebDAV', (w.refused ? 'Password refused · ' : '') + (w.url || ''), !w.refused));
  const r = d.romm || {};
  if (r.url) rows.push(item('RomM', r.busy || (r.token ? r.url : 'Needs a code or a token'), !!r.token));
  const s = d.syncthing || {};
  if (s.on) rows.push(item('Syncthing', s.status || '', !!s.running));
  fill($('places'), rows);
  $('nothing').hidden = rows.length > 0;
  $('auto').checked = !!d.auto;
  $('syncNow').disabled = rows.length === 0 || !!d.busy;
  if (d.busy) say('topMsg', d.busy);
  else if ($('topMsg').textContent.indexOf('…') >= 0) say('topMsg', '');
}

function showPairing(p) {
  const box = $('pairBox');
  const live = p && (p.stage === 'asking' || p.stage === 'compare' || p.stage === 'waiting');
  if (live) seenPair = p.token;
  if (!p || (!live && p.token !== seenPair)) { box.hidden = true; return false; }
  box.hidden = false;
  const buttons = $('pairButtons');
  buttons.textContent = '';
  const addButton = (label, ghost, accept) => {
    const b = node('button', ghost ? 'ghost' : '', label);
    b.onclick = () => busy(b, '...', async () => {
      try { await act({action: 'pair-answer', accept: accept}); load(false); } catch (e) { say('dMsg2', e.message, true); }
    });
    buttons.appendChild(b);
  };
  $('pairCodeBox').hidden = !p.code;
  $('pairCode').textContent = p.code || '';
  let text = '';
  switch (p.stage) {
  case 'asking':
    text = 'Asking ' + p.name + '... keep Sync open on it. A code appears here and on ' + p.name + '.';
    addButton('Cancel', true, false);
    break;
  case 'compare':
    text = p.initiator
      ? 'Check that ' + p.name + ' shows this same code and choose Codes match there. Then tap Codes match here.'
      : p.name + ' wants to pair with this JukaHub. Only confirm if you started it on ' + p.name + ' and it shows this same code.';
    addButton('Cancel', true, false);
    addButton('Codes match', false, true);
    break;
  case 'waiting':
    text = 'Confirmed here. Waiting for ' + p.name + ': choose Codes match there.';
    addButton('Cancel', true, false);
    break;
  case 'done':
    text = 'Paired with ' + p.name + '. Your saves now follow you between the two by themselves.';
    break;
  default:
    text = p.reason || 'Pairing did not finish. Try again.';
  }
  $('pairText').textContent = text;
  return live;
}

function showDevices(d) {
  const pairing = showPairing(d.pairing);
  const near = (d.nearby || []).map(n => item(n.name, n.model + ' · on this Wi-Fi', true, 'Pair', btn => busy(btn, 'Asking...', async () => {
    try { const r = await act({action: 'pair', id: n.id}); say('dMsg2', r.message); load(false); } catch (e) { say('dMsg2', e.message, true); }
  })));
  fill($('nearby'), pairing ? [] : near);
  $('nearNote').hidden = pairing;
  $('nearNote').textContent = d.lan
    ? (near.length ? 'Found on this Wi-Fi. Pick one: a code then shows here and on it.' : 'Open Sync on your other JukaHub (Tools, then Sync), on the same Wi-Fi. It shows up here.')
    : 'The handheld is not on Wi-Fi.';
  const paired = (d.paired || []).map(p => item(p.name, (p.online ? 'On this Wi-Fi' : 'Away') + (p.last ? ' · synced ' + p.last : ''), p.online));
  fill($('paired'), paired);
  $('pairedBox').hidden = paired.length === 0;
  return pairing;
}

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
  $('gSignIn').hidden = true; $('gSignOut').hidden = true; $('gSetup').hidden = true;
  if (g.signedIn && !g.expired) {
    line = 'Signed in as ' + (g.account || 'your account') + '. JukaHub sees only the files it makes ("JukaHub Saves").';
    $('gSignOut').hidden = false;
  } else if (g.waiting) {
    line = 'Approve the sign-in: open the link, enter the code, choose your account and allow access.';
  } else if (g.source) {
    line = g.signedIn ? 'The sign-in for ' + (g.account || 'your account') + ' expired: sign in again.' : 'Sign in once. JukaHub asks only to see the files it makes itself.';
    $('gSignIn').hidden = false;
  } else {
    line = 'This JukaHub has no Google sign-in yet. Making a free Google client takes about 10 minutes, once.';
    $('gSetup').hidden = false;
  }
  $('gStatus').textContent = line;
  showCode(g.waiting ? g.code : '', g.url);
  return !!g.waiting;
}

function showWebdav(w) {
  let line = 'Not connected. Type the server\'s WebDAV address, your user name and an app password.';
  if (w.set && w.refused) line = 'The server refused the saved password: send it again.';
  else if (w.set) line = 'Connected to ' + w.url + (w.user ? ' as ' + w.user : '') + (w.last ? '. Last sync: ' + w.last : '') + '.';
  $('dStatus').textContent = line;
  $('dRemove').hidden = !w.set;
  if (w.set) {
    if (!$('dURL').value) $('dURL').value = w.url || '';
    if (!$('dUser').value) $('dUser').value = w.user || '';
  }
}

function showRomm(r) {
  let line = 'Not connected.';
  if (r.url && r.token) line = 'Connected to ' + r.url + (r.tokenTail ? ' (token ...' + r.tokenTail + ')' : '') + (r.auto === false ? '. Automatic sync is off for it.' : '.') + (r.busy ? ' ' + r.busy : '');
  else if (r.url) line = 'Address saved: add a pairing code or a token.';
  $('rStatus').textContent = line;
  $('rForget').hidden = !r.url;
  if (r.url && !$('rURL').value) $('rURL').value = r.url;
}

function showSyncthing(s) {
  let line = 'Off. Turning it on downloads Syncthing the first time (about 12 MB).';
  if (s.on) line = 'Syncthing: ' + (s.status || '') + (s.detail ? ' (' + s.detail + ')' : '') + '.';
  $('sStatus').textContent = line;
  $('sOffRow').hidden = !!s.on;
  $('sOff').hidden = !s.on;
  $('sIdBox').hidden = !s.id;
  $('sAddBox').hidden = !s.running;
  if ($('sID').value !== (s.id || '')) $('sID').value = s.id || '';
  fill($('sPending'), (s.pending || []).map(p => item(p.name + ' asks to sync', p.id.slice(0, 7), null, 'Accept', btn => busy(btn, '...', async () => {
    try { const r = await act({action: 'syncthing-add', id: p.id, name: p.name}); say('sMsg', r.message); } catch (e) { say('sMsg', e.message, true); }
  }))));
  fill($('sDevices'), (s.devices || []).map(dv => item(dv.name, dv.connected ? 'Connected' : 'Away', !!dv.connected)));
  return !!s.on && !s.running;
}

async function load(scan) {
  const res = await fetch('/api/sync' + (scan ? '?scan=1' : ''), {headers: hdr(false), cache: 'no-store'});
  if (!res.ok) throw new Error((await res.text()).trim() || 'Could not reach the handheld');
  const d = await res.json();
  state = d;
  showTop(d);
  const pairing = showDevices(d);
  const waiting = showGoogle(d.google || {});
  showWebdav(d.webdav || {});
  showRomm(d.romm || {});
  const starting = showSyncthing(d.syncthing || {});
  fast = pairing || waiting || starting || !!d.busy;
  if (firstLoad) { firstLoad = false; pick(); }
}
function tick() {
  const next = () => setTimeout(tick, document.hidden ? 15000 : (fast ? 2000 : 5000));
  if (document.hidden) { next(); return; }
  load(true).catch(e => say('topMsg', e.message, true)).finally(next);
}
function pick() {
  const picked = location.hash.slice(1);
  const card = picked && $(picked + 'Card');
  if (card) { card.classList.add('picked'); card.scrollIntoView({block: 'start'}); }
}

$('auto').onchange = () => {
  const on = $('auto').checked;
  act({action: 'auto', on: on}).then(r => say('topMsg', r.message)).catch(e => { say('topMsg', e.message, true); $('auto').checked = !on; });
};
$('syncNow').onclick = () => busy($('syncNow'), 'Starting...', async () => {
  try { const r = await act({action: 'sync-now'}); say('topMsg', r.message); setTimeout(() => load(false).catch(() => {}), 1500); } catch (e) { say('topMsg', e.message, true); }
});
$('gSignIn').onclick = () => busy($('gSignIn'), 'Asking Google...', async () => {
  try { const r = await act({action: 'google-signin'}); say('gMsg', r.message); fast = true; setTimeout(() => load(false).catch(() => {}), 1500); } catch (e) { say('gMsg', e.message, true); }
});
$('gSignOut').onclick = () => busy($('gSignOut'), 'Signing out...', async () => {
  if (!confirm('Sign out of Google Drive on the handheld? Your saves on Drive stay there.')) return;
  try { const r = await act({action: 'google-signout'}); say('gMsg', r.message); setTimeout(() => load(false).catch(() => {}), 1200); } catch (e) { say('gMsg', e.message, true); }
});
$('gSetup').onclick = ev => { ev.preventDefault(); location.href = '/cloud?p=' + encodeURIComponent(pin) + '#google'; };
$('dSave').onclick = () => busy($('dSave'), 'Checking with the server...', async () => {
  const body = {action: 'webdav', url: $('dURL').value, user: $('dUser').value, password: $('dPass').value};
  if (!body.url.trim()) { say('dMsg', 'Type the server\'s WebDAV address first', true); return; }
  try { const r = await post('/api/cloud', body); say('dMsg', r.message); $('dPass').value = ''; if (r.url) $('dURL').value = r.url; showWebdav(r); }
  catch (e) { say('dMsg', e.message, true); }
});
$('dRemove').onclick = () => busy($('dRemove'), 'Forgetting...', async () => {
  if (!confirm('Forget this WebDAV server on the handheld? Your saves on it stay.')) return;
  try { const r = await post('/api/cloud', {action: 'webdav-remove'}); say('dMsg', r.message); $('dURL').value = ''; $('dUser').value = ''; await load(false); }
  catch (e) { say('dMsg', e.message, true); }
});
$('rPair').onclick = () => busy($('rPair'), 'Asking RomM...', async () => {
  const body = {action: 'romm-pair', url: $('rURL').value, code: $('rCode').value};
  if (!body.code.trim()) { say('rMsg', 'Type the code RomM shows under Pair device', true); return; }
  try { const r = await act(body); say('rMsg', r.message); $('rCode').value = ''; showRomm(r); }
  catch (e) { say('rMsg', e.message, true); }
});
$('rTokenSave').onclick = () => busy($('rTokenSave'), 'Checking with RomM...', async () => {
  try { const r = await post('/api/romm', {url: $('rURL').value, token: $('rToken').value}); say('rMsg', r.message); $('rToken').value = ''; await load(false); }
  catch (e) { say('rMsg', e.message, true); }
});
$('rForget').onclick = () => busy($('rForget'), 'Forgetting...', async () => {
  if (!confirm('Forget this RomM server on the handheld? Your saves and games on it stay.')) return;
  try { const r = await act({action: 'romm-forget'}); say('rMsg', r.message); $('rURL').value = ''; setTimeout(() => load(false).catch(() => {}), 800); }
  catch (e) { say('rMsg', e.message, true); }
});
$('sOn').onclick = () => busy($('sOn'), 'Starting...', async () => {
  try { const r = await act({action: 'syncthing-on'}); say('sMsg', r.message); fast = true; setTimeout(() => load(false).catch(() => {}), 1500); } catch (e) { say('sMsg', e.message, true); }
});
$('sOff').onclick = () => busy($('sOff'), 'Stopping...', async () => {
  if (!confirm('Turn Syncthing off on the handheld? Its folders and devices stay for next time.')) return;
  try { const r = await act({action: 'syncthing-off'}); say('sMsg', r.message); setTimeout(() => load(false).catch(() => {}), 1200); } catch (e) { say('sMsg', e.message, true); }
});
$('sAdd').onclick = () => busy($('sAdd'), 'Adding...', async () => {
  try { const r = await act({action: 'syncthing-add', id: $('sAddID').value, name: $('sAddName').value}); say('sMsg', r.message); $('sAddID').value = ''; }
  catch (e) { say('sMsg', e.message, true); }
});
$('sCopy').onclick = async () => {
  const v = $('sID').value;
  if (!v) return;
  try { await navigator.clipboard.writeText(v); say('sMsg', 'Copied.'); return; } catch (_) {}
  $('sID').focus(); $('sID').select();
  try { if (document.execCommand('copy')) { say('sMsg', 'Copied.'); return; } } catch (_) {}
  say('sMsg', 'Selected: copy it from the box.');
};
$('mainLink').href = '/?p=' + encodeURIComponent(pin);
tick();
</script>
`
