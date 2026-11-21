package main

// companion_update.go — the Phone Companion's update card.
//
// When the update check (autoupdate.go) has found a release on the user's
// channel, the page shows "JukaHub <version> is out" with Install. Install
// starts the very path the handheld's update prompt runs (startHeaderUpdate:
// download, verify, staged swap, restart), handed to the main loop through
// tbxPost, and the handheld says in a toast — filed in the notification
// centre too — that the phone started it.
//
// There is no second checker: GET /api/update reads the advertisement the
// startup check keeps in patch state, and only when that check is due
// (autoUpdateCheckDue: never checked, or more than half an hour ago) starts
// the same one-shot check (checkForUpdateOnce), at most once per
// companionUpdateRetry, so a page polling an offline handheld does not
// hammer GitHub.
//
// Install is refused while a game runs (the swap restarts JukaHub under it)
// and on a battery below the handheld's own update gate that is not charging
// (updateBatteryGate, 30 %). It needs the PIN in the X-Companion-Pin header —
// the "p" address parameter, which a plain link or a form can carry, is not
// enough for it — and names the release the page showed, so a release that
// changed in between is not installed unseen.
//
// Routes (behind companionServer.guard):
//
//	GET  /api/update          {current, available, tag, channel, state, progress, blocked, notes}
//	POST /api/update/install  {"tag": "<release tag>"}
//	GET  /companion-extras.js the card's script (static, nothing secret; also the logs link)

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// companionUpdateRetry spaces the checks the page may start.
	companionUpdateRetry = 5 * time.Minute
	// companionUpdateWait is how long Install waits for the main loop's answer
	// before saying the request was handed over.
	companionUpdateWait = 4 * time.Second
	// companionUpdateTool is the install bar's name for the JukaHub update
	// (startHeaderUpdate's installBarBegin).
	companionUpdateTool = "JukaHub update"
)

// companionUpdateCheck is the page-started check's state.
var companionUpdateCheck struct {
	mu       sync.Mutex
	running  bool
	lastTry  time.Time
	lastFail string
}

// companionUpdateProgress is the install bar's numbers.
type companionUpdateProgress struct {
	Phase string `json:"phase"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// companionUpdateStatus is GET /api/update's answer. State is "available",
// "installing", "restarting", "failed", "checking", "current" or "unknown"
// (never checked: offline since the handheld started).
type companionUpdateStatus struct {
	Current   string                   `json:"current"`
	Available string                   `json:"available,omitempty"`
	Tag       string                   `json:"tag,omitempty"`
	Channel   string                   `json:"channel"`
	State     string                   `json:"state"`
	Progress  *companionUpdateProgress `json:"progress,omitempty"`
	Message   string                   `json:"message,omitempty"`
	Blocked   string                   `json:"blocked,omitempty"`
	Notes     []string                 `json:"notes,omitempty"`
}

// registerUpdateRoutes adds the update card's routes (companion.go's handler).
func (s *companionServer) registerUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/update", s.guard(s.handleUpdateStatus))
	mux.HandleFunc("/api/update/install", s.guard(s.handleUpdateInstall))
	mux.HandleFunc("/companion-extras.js", handleCompanionExtrasScript)
}

// companionUpdateGameRunning reports a game (or another program) on the
// screen. Any goroutine.
func companionUpdateGameRunning() bool {
	return runningGameChildren.Load() > 0 || foreignScreenSuspended() || retroArchRunningCached()
}

// companionUpdateBlocked is why Install would be refused now, "" when it
// would not. Any goroutine.
func companionUpdateBlocked() string {
	if companionUpdateGameRunning() {
		return "A game is running on the handheld. Quit it first, then install."
	}
	if err := updateBatteryGate(); err != nil {
		return err.Error()
	}
	return ""
}

// companionUpdateCurrent is the running build's release date, or the
// version the server was started with.
func (s *companionServer) companionUpdateCurrent() string {
	if d := installedBuildDate(); d != "" {
		return d
	}
	if v := strings.TrimSpace(s.reportVersion); v != "" {
		return v
	}
	return "unknown"
}

// companionUpdateMaybeCheck starts the release check when it is due and the
// page has not started one recently. Any goroutine.
func companionUpdateMaybeCheck() {
	if strings.TrimSpace(os.Getenv("JUKAHUB_NO_AUTOUPDATE")) != "" || shotModeOn() {
		return
	}
	patchStateMu.Lock()
	last := patchState.AutoUpdateCheckedAt
	patchStateMu.Unlock()
	now := time.Now()
	if !autoUpdateCheckDue(last, now) {
		return
	}
	companionUpdateCheck.mu.Lock()
	if companionUpdateCheck.running || (!companionUpdateCheck.lastTry.IsZero() && now.Sub(companionUpdateCheck.lastTry) < companionUpdateRetry) {
		companionUpdateCheck.mu.Unlock()
		return
	}
	companionUpdateCheck.running = true
	companionUpdateCheck.lastTry = now
	companionUpdateCheck.mu.Unlock()
	go func() {
		err := checkForUpdateOnce(appConfig)
		companionUpdateCheck.mu.Lock()
		companionUpdateCheck.running = false
		companionUpdateCheck.lastFail = ""
		if err != nil && !errors.Is(err, errAutoUpdateInProgress) {
			companionUpdateCheck.lastFail = "The handheld could not reach the update server."
		}
		companionUpdateCheck.mu.Unlock()
		if err != nil {
			logPatch("auto-update: check from the phone page failed: %v", err)
		}
	}()
}

// companionUpdateStatusNow is the card's state. Any goroutine.
func (s *companionServer) companionUpdateStatusNow() companionUpdateStatus {
	st := companionUpdateStatus{
		Current: s.companionUpdateCurrent(),
		Channel: updateChannelLabel(updateChannelNow()),
		State:   "unknown",
	}
	if updateAvailable() {
		st.Available = availableUpdateLabel()
		st.Tag = availableUpdateTag()
		st.State = "available"
		st.Notes = updatePromptHighlights(3)
		st.Blocked = companionUpdateBlocked()
	} else {
		patchStateMu.Lock()
		checked := patchState.AutoUpdateCheckedAt != ""
		patchStateMu.Unlock()
		if checked {
			st.State = "current"
		}
	}
	companionUpdateCheck.mu.Lock()
	checking, fail := companionUpdateCheck.running, companionUpdateCheck.lastFail
	companionUpdateCheck.mu.Unlock()
	if checking && st.State != "available" {
		st.State = "checking"
	} else if fail != "" && st.State == "unknown" {
		st.Message = fail
	}
	// An update running now, from the phone or the handheld, wins.
	visible, tool, phase, detail, done, total, _, _, ending := installBarSnapshot()
	if visible && tool == companionUpdateTool {
		st.Blocked = ""
		switch {
		case phase == installPhaseError:
			st.State = "failed"
			st.Message = detail
		case ending:
			st.State = "restarting"
		default:
			st.State = "installing"
			st.Progress = &companionUpdateProgress{Phase: phase, Done: done, Total: total}
		}
	}
	return st
}

// handleUpdateStatus answers GET /api/update.
func (s *companionServer) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	companionUpdateMaybeCheck()
	w.Header().Set("Cache-Control", "no-store")
	writeCompanionJSON(w, s.companionUpdateStatusNow())
}

// companionUpdateRefuse answers Install with a sentence for the page.
func companionUpdateRefuse(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
}

// handleUpdateInstall answers POST /api/update/install.
func (s *companionServer) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// The page's fetch sends the header; a link or a form with the PIN in its
	// address cannot, and does not install anything.
	if r.Header.Get("X-Companion-Pin") == "" {
		companionUpdateRefuse(w, http.StatusForbidden, "Open the companion page to install updates.")
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !updateAvailable() {
		companionUpdateRefuse(w, http.StatusConflict, "JukaHub is up to date.")
		return
	}
	if tag := strings.TrimSpace(body.Tag); tag == "" || !strings.EqualFold(tag, availableUpdateTag()) {
		companionUpdateRefuse(w, http.StatusConflict, "A different release is on offer now. Reload the page to see it.")
		return
	}
	if reason := companionUpdateBlocked(); reason != "" {
		companionUpdateRefuse(w, http.StatusConflict, reason)
		return
	}
	answer := make(chan string, 1)
	tbxPost(func() { answer <- companionUpdateStartOnMain() })
	select {
	case msg := <-answer:
		if msg != "" {
			companionUpdateRefuse(w, http.StatusConflict, msg)
			return
		}
		writeCompanionJSON(w, map[string]interface{}{"ok": true, "state": "installing"})
	case <-time.After(companionUpdateWait):
		// The main loop is busy (a long frame); the request is queued and
		// runs on its next frame with the same checks.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "queued": true})
	}
}

// companionUpdateStartOnMain starts the install the handheld's prompt runs and
// says so on the handheld. Main thread (tbxPost); "" when it started.
func companionUpdateStartOnMain() string {
	if reason := companionUpdateBlocked(); reason != "" {
		return reason
	}
	if !updateAvailable() {
		return "JukaHub is up to date."
	}
	label := availableUpdateLabel()
	if !startHeaderUpdate() {
		// startHeaderUpdate said why on the handheld (already running, battery).
		return "The handheld did not start the update. Its screen says why."
	}
	logInfo("companion", "update: %s started from the phone", label)
	ShowToast("Your phone started the JukaHub "+label+" update — JukaHub restarts when it is ready", ToastKindInfo)
	return ""
}

// handleCompanionExtrasScript serves the update card's and the logs link's
// script: static, nothing secret in it.
func handleCompanionExtrasScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, companionExtrasScript)
}

// companionExtrasScript runs deferred, after the page's own script, whose
// pin, hdr() and toast() it uses. It fills #updCard (shown only while a
// release is on offer or installing) and makes #logsLink download the logs.
const companionExtrasScript = `(function () {
  const card = document.getElementById('updCard');
  const note = document.getElementById('updNote');
  const list = document.getElementById('updNotes');
  const btn = document.getElementById('updInstall');
  const msg = document.getElementById('updMsg');
  const bar = document.getElementById('updBar');
  const fill = bar ? bar.firstElementChild : null;
  const logs = document.getElementById('logsLink');
  let timer = 0, tag = '', label = '', busy = false;
  function say(text, bad) {
    if (!msg) return;
    msg.textContent = text || '';
    msg.className = bad ? 'msg bad' : 'msg';
  }
  function show(st) {
    tag = st.tag || '';
    label = st.available || '';
    const installing = st.state === 'installing' || st.state === 'restarting';
    const visible = st.state === 'available' || installing || st.state === 'failed';
    card.hidden = !visible;
    if (!visible) return;
    if (st.state === 'restarting') {
      note.textContent = 'The update is installed. JukaHub restarts on the handheld now.';
    } else if (installing) {
      note.textContent = 'Updating JukaHub on the handheld' + (label ? ' to ' + label : '') + '...';
    } else if (st.state === 'failed') {
      note.textContent = 'The update did not finish.';
    } else {
      note.textContent = 'JukaHub ' + label + ' is out (' + st.channel + '). This handheld has ' + st.current + '.';
    }
    list.textContent = '';
    if (!installing && Array.isArray(st.notes)) {
      for (const line of st.notes) {
        const li = document.createElement('li');
        li.textContent = line;
        list.append(li);
      }
    }
    list.hidden = !list.childElementCount;
    btn.hidden = installing || st.state === 'failed' && !tag;
    btn.disabled = busy || !!st.blocked;
    if (st.state === 'failed') say(st.message || 'The update failed. Try again later.', true);
    else if (st.blocked && !installing) say(st.blocked, true);
    else if (!busy) say('');
    if (bar && fill) {
      const p = st.progress;
      bar.className = installing ? (p && p.total > 0 ? 'bar' : 'bar pulse') : 'bar idle';
      fill.style.width = p && p.total > 0 ? Math.min(100, Math.round(p.done * 100 / p.total)) + '%' : (installing ? '100%' : '0');
    }
  }
  async function poll() {
    clearTimeout(timer);
    let next = 60000;
    try {
      const res = await fetch('/api/update', {headers: hdr(), cache: 'no-store'});
      if (res.ok) {
        const st = await res.json();
        show(st);
        if (st.state === 'installing' || st.state === 'restarting' || st.state === 'checking') next = 2000;
      }
    } catch (e) {
      next = 15000;
    }
    timer = setTimeout(poll, next);
  }
  if (card && btn) {
    btn.onclick = async () => {
      if (!tag || busy) return;
      if (!confirm('Install JukaHub ' + label + ' on the handheld now? It downloads the update and restarts JukaHub when it is ready.')) return;
      busy = true;
      btn.disabled = true;
      say('Starting the update...');
      try {
        const res = await fetch('/api/update/install', {method: 'POST', headers: hdr({'Content-Type': 'application/json'}), body: JSON.stringify({tag: tag})});
        const text = await res.text();
        let j = {};
        try { j = JSON.parse(text); } catch (_) { j = {}; }
        if (!res.ok) {
          say(j.error || text.trim() || res.statusText, true);
        } else {
          say(j.queued ? 'Sent: the handheld starts the update in a moment.' : 'Started: JukaHub restarts on the handheld when the update is ready.');
          toast('Update started');
        }
      } catch (e) {
        say(e.message, true);
      }
      busy = false;
      poll();
    };
    poll();
  }
  if (logs) {
    logs.onclick = (ev) => {
      ev.preventDefault();
      const a = document.createElement('a');
      a.href = '/api/logs.zip?p=' + encodeURIComponent(pin);
      a.download = 'jukahub-logs.zip';
      a.rel = 'noopener';
      document.body.append(a);
      a.click();
      a.remove();
    };
  }
})();
`
