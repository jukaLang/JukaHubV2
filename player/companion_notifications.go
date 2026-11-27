package main

// companion_notifications.go — the Phone Companion's Notifications page
// (/notifications): the handheld's notification centre on a phone, with the
// actions that are safe to take from there.
//
//	GET  /api/notifications      every row, newest first, with its source,
//	                             priority, age, unread state and — when the
//	                             action is safe from a phone — its action
//	POST /api/notifications      {"id": N, "action": "kind"} runs one row's
//	                             action and removes the row, like A in the
//	                             handheld's centre
//
// The page and both routes sit behind the existing PIN (s.guard). Actions that
// install an update or start a game stay on the handheld: a phone may open a
// screen, the Downloads page, a file or retry a download, but not start an
// install. The action itself comes from the stored row, never from the request
// body: the body only names the row and the kind the page drew.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// companionNotifBodyMax bounds a POST body.
	companionNotifBodyMax = 4 << 10
)

var (
	errNotifPhoneNoRow    = errors.New("That notification is not on the handheld any more")
	errNotifPhoneAction   = errors.New("That action is not safe from your phone")
	errNotifPhoneMismatch = errors.New("That notification changed — pull down to refresh")
)

// companionNotifAction is an action as the page may offer it. Arg stays on the
// handheld: the phone names the row, and the handheld supplies the argument.
type companionNotifAction struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// companionNotifRow is one notification as the page gets it.
type companionNotifRow struct {
	ID       uint64                `json:"id"`
	Message  string                `json:"message"`
	Priority string                `json:"priority"`
	Source   string                `json:"source"`
	When     string                `json:"when"`
	Read     bool                  `json:"read"`
	Repeat   int                   `json:"repeat,omitempty"`
	Action   *companionNotifAction `json:"action,omitempty"`
}

// companionNotifState is the answer to the list and to every action.
type companionNotifState struct {
	Rows   []companionNotifRow `json:"rows"`
	Unread int                 `json:"unread"`
}

// companionNotifRequest is a POST body: which row, and the kind the page drew.
type companionNotifRequest struct {
	ID     uint64 `json:"id"`
	Action string `json:"action"`
}

// registerNotificationRoutes adds the page and its endpoints.
func (s *companionServer) registerNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/notifications", s.handleNotificationsPage)
	mux.HandleFunc("/api/notifications", s.guard(s.handleNotificationsAPI))
}

// notifPhoneSafeAction reports whether a phone may run one action kind. An
// update offer is deliberately left out: opening its install prompt and
// confirming it belong on the handheld.
func notifPhoneSafeAction(kind string) bool {
	switch kind {
	case notifActScene, notifActDownloads, notifActDownloadRetry, notifActOpenFile:
		return true
	}
	return false
}

// notifPhonePriority is the word the page uses for a priority.
func notifPhonePriority(p notifPriority) string {
	switch p {
	case notifSuccess:
		return "success"
	case notifWarning:
		return "warning"
	case notifError:
		return "error"
	}
	return "info"
}

// notifPhoneSnapshot copies the history for the page. The action's argument
// and any action unsafe from a phone stay here.
func notifPhoneSnapshot() companionNotifState {
	notifEnsureLoaded()
	notifHistoryMu.Lock()
	all := append([]notification(nil), nc.history...)
	notifHistoryMu.Unlock()
	now := time.Now()
	st := companionNotifState{Rows: make([]companionNotifRow, 0, len(all))}
	for _, n := range all {
		row := companionNotifRow{
			ID: n.ID, Message: n.Message, Priority: notifPhonePriority(n.Priority),
			Source: notifSourceLabel(n.Source), When: formatDateTimeNear(nil, n.Timestamp, now),
			Read: n.Read, Repeat: n.Repeat,
		}
		if n.Action.Kind != "" && n.Action.Label != "" && notifPhoneSafeAction(n.Action.Kind) && notifActionKnown(n.Action.Kind) {
			row.Action = &companionNotifAction{Label: n.Action.Label, Kind: n.Action.Kind}
		}
		if !n.Read {
			st.Unread++
		}
		st.Rows = append(st.Rows, row)
	}
	return st
}

// notifPhoneRunAction is the handoff from the HTTP goroutine to the main
// thread, where every action runs. A test replaces it to observe the dispatch
// without a window.
var notifPhoneRunAction = func(kind, arg string) {
	if fn := notifActions[kind]; fn != nil {
		fn(appConfig, arg)
	}
}

// handleNotificationsAPI answers GET with the list and POST with one action.
func (s *companionServer) handleNotificationsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, notifPhoneSnapshot())
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionNotifBodyMax)
	var req companionNotifRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.ID == 0 {
		http.Error(w, errNotifPhoneNoRow.Error(), http.StatusNotFound)
		return
	}
	notifHistoryMu.Lock()
	var found notification
	ok := false
	for _, n := range nc.history {
		if n.ID == req.ID {
			found, ok = n, true
			break
		}
	}
	notifHistoryMu.Unlock()
	if !ok {
		http.Error(w, errNotifPhoneNoRow.Error(), http.StatusNotFound)
		return
	}
	if found.Action.Kind == "" || !notifPhoneSafeAction(found.Action.Kind) {
		http.Error(w, errNotifPhoneAction.Error(), http.StatusForbidden)
		return
	}
	if strings.TrimSpace(req.Action) != "" && req.Action != found.Action.Kind {
		http.Error(w, errNotifPhoneMismatch.Error(), http.StatusConflict)
		return
	}
	if !notifActionKnown(found.Action.Kind) {
		http.Error(w, "That action is not part of this build", http.StatusConflict)
		return
	}
	if !notifRemoveID(found.ID) {
		http.Error(w, errNotifPhoneNoRow.Error(), http.StatusNotFound)
		return
	}
	kind, arg := found.Action.Kind, found.Action.Arg
	tbxPost(func() { notifPhoneRunAction(kind, arg) })
	writeCompanionJSON(w, map[string]bool{"ok": true})
}

func (s *companionServer) handleNotificationsPage(w http.ResponseWriter, r *http.Request) {
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
	_, _ = io.WriteString(w, companionNotificationsPage)
}

// companionNotificationsPage is /notifications. It mirrors the handheld's
// rows and posts an action only for the buttons the API offered.
const companionNotificationsPage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<meta name="referrer" content="no-referrer">
<title>JukaHub · Notifications</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--bad:#ff6b6b;--ok:#3ecf8e;--warn:#f0b35b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:calc(14px + env(safe-area-inset-top)) 16px calc(40px + env(safe-area-inset-bottom));max-width:720px;margin:0 auto}
h1{font-size:22px;margin:0}
.top{display:flex;align-items:center;gap:10px;justify-content:space-between;flex-wrap:wrap}
.note{color:var(--dim);font-size:14px;margin:6px 0 10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px 16px;margin:10px 0}
.card.error{border-color:#5a2a34}
.card.warning{border-color:#5a4a2a}
.card h3{margin:0 0 5px;font-size:16px;line-height:1.3;overflow-wrap:anywhere}
.meta{color:var(--dim);font-size:12px;display:flex;gap:7px;flex-wrap:wrap}
.meta .unread{color:var(--accent);font-weight:600}
.msg{margin:10px 0 0;color:var(--dim);font-size:14px}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
button,.btn{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:10px 14px;min-height:42px;text-decoration:none;display:inline-flex;align-items:center;justify-content:center}
button.ghost,.btn.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button:disabled{opacity:.55}
.row{display:flex;gap:10px;margin-top:10px;flex-wrap:wrap}
.row>*{flex:1 1 120px}
[hidden]{display:none!important}
small{color:var(--dim);display:block;margin-top:10px;font-size:12px}
</style>
<div class=top><h1>Notifications</h1><a class="btn ghost" id=back href="#">‹ Companion</a></div>
<p class=note id=status>Asking the handheld…</p>
<div class=row><button class=ghost id=refresh type=button>Refresh</button></div>
<div class=msg id=msg aria-live=polite></div>
<div id=items></div>
<small>Actions here only do what is safe from a phone. Opening an update offer or starting a game stays on the handheld.</small>
<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const $ = id => document.getElementById(id);
function hdr(json) { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; }
$('back').href = '/?p=' + encodeURIComponent(pin);
function say(text, bad) { $('msg').textContent = text || ''; $('msg').className = bad ? 'msg bad' : 'msg'; }
function when(row) { return [row.when, row.source, row.repeat > 1 ? '×' + row.repeat : ''].filter(Boolean).join(' · '); }
function draw(rows) {
  const box = $('items'); box.textContent = '';
  if (!rows.length) {
    const p = document.createElement('p'); p.className = 'note';
    p.textContent = 'Nothing here: notifications from the handheld appear on this page.';
    box.appendChild(p);
    return;
  }
  rows.forEach(row => {
    const card = document.createElement('div');
    card.className = 'card ' + (row.priority === 'error' ? 'error' : row.priority === 'warning' ? 'warning' : '');
    const h = document.createElement('h3'); h.textContent = row.message;
    const meta = document.createElement('div'); meta.className = 'meta';
    if (!row.read) { const u = document.createElement('span'); u.className = 'unread'; u.textContent = 'Unread'; meta.appendChild(u); }
    const m = document.createElement('span'); m.textContent = when(row); meta.appendChild(m);
    card.append(h, meta);
    if (row.action) {
      const r = document.createElement('div'); r.className = 'row';
      const b = document.createElement('button'); b.type = 'button'; b.textContent = row.action.label;
      b.onclick = () => run(row, b);
      r.appendChild(b); card.appendChild(r);
    }
    box.appendChild(card);
  });
}
async function load() {
  $('refresh').disabled = true;
  try {
    const res = await fetch('/api/notifications', {headers: hdr(false), cache: 'no-store'});
    const text = await res.text();
    if (!res.ok) throw new Error(text.trim() || res.statusText);
    const data = JSON.parse(text);
    draw(data.rows || []);
    const n = data.rows || [];
    $('status').textContent = n.length === 1 ? '1 notification on the handheld.' : n.length + ' notifications on the handheld.';
  } catch (e) { $('status').textContent = e.message; }
  finally { $('refresh').disabled = false; }
}
async function run(row, button) {
  if (button.disabled) return;
  button.disabled = true; say('Sending to the handheld…');
  try {
    const res = await fetch('/api/notifications', {method: 'POST', headers: hdr(true), body: JSON.stringify({id: row.id, action: row.action.kind})});
    const text = await res.text();
    if (!res.ok) throw new Error(text.trim() || res.statusText);
    say(row.action.label + ' — done on the handheld.');
    await load();
  } catch (e) { say(e.message, true); button.disabled = false; }
}
$('refresh').onclick = load;
window.addEventListener('focus', load);
load();
function tick() { if (!document.hidden) load(); setTimeout(tick, 10000); }
setTimeout(tick, 10000);
</script>
`
