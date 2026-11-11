package main

// voicecmd_phone.go — voice commands from the phone.
//
// The companion (companion.go) serves a /voice page behind the same PIN as the
// rest of the phone pages. It is one text field: tap it, tap the microphone on
// the phone's keyboard, say "open YouTube", and the keyboard's dictation turns
// it into words — on the phone, for free, and on a plain-HTTP page on the LAN
// where a page's own microphone access (getUserMedia, Web Speech) is refused.
// The words go to /api/voice; the handheld runs them through the same parser
// and executor as its own microphone (voicecmd_intent.go, voicecmd_exec.go)
// and answers with the sentence it would show ("Opening Media"), or with the
// choices when the name could mean several things. A tap on a choice sends its
// number back.
//
// The HTTP handler never touches UI state: it posts the command to the main
// loop (tbxPost, drained every frame by partyTick) and waits up to
// voicePhoneWait for the answer. While a game or an app has the screen the
// main loop is parked, so the phone hears "busy" instead of waiting forever.
//
// Exactly one side wins a command: the main loop, which runs it, or the
// handler, which gives up on it (claimed). A handler that loses the race the
// command is already running, so it waits a little longer for the answer
// instead of saying "busy" about a command that ran. Every send carries an
// id; the same id again (the page's Try again) never runs a command twice —
// it gets the stored answer, or "still running".

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// voicePhoneWait is how long a phone command waits for the main loop.
var voicePhoneWait = 2500 * time.Millisecond

// Test seams, nil in production. voicePhonePost hands work to the main loop
// (tbxPost when nil); voicePhoneRun answers a request there
// (voicePhoneRunOnMain when nil). They are not initialised with those
// functions on purpose: voicePhoneRunOnMain reaches most of the app — the
// Home search catalog's openers start the companion server, whose handler is
// this file's — and a package variable initialised with it would be an
// initialisation cycle.
var (
	voicePhonePost func(func())
	voicePhoneRun  func(voicePhoneRequest) voicePhoneReply
)

// voicePhoneRequest is one command from the phone page: the words, or the
// number (1-based) of a choice from the list generation gen. ID names the
// send, so a retry of it is recognised.
type voicePhoneRequest struct {
	Text string `json:"text"`
	Pick int    `json:"pick"`
	Gen  uint64 `json:"gen"`
	ID   string `json:"id"`
}

// voicePhoneJob is one command on its way through the main loop.
type voicePhoneJob struct {
	// claimed is taken by whoever gets there first: the main loop (it runs
	// the command) or the handler (it gave up, and the command never runs).
	claimed atomic.Bool
	answer  chan voicePhoneReply
	mu      sync.Mutex
	done    bool
	rep     voicePhoneReply
}

// finish stores the answer for a retry of the same id.
func (j *voicePhoneJob) finish(rep voicePhoneReply) {
	j.mu.Lock()
	j.done, j.rep = true, rep
	j.mu.Unlock()
}

// result is the stored answer, once there is one.
func (j *voicePhoneJob) result() (voicePhoneReply, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.rep, j.done
}

// voicePhoneJobsMax bounds the ids remembered for retries.
const voicePhoneJobsMax = 32

// voicePhoneJobs are the recent sends by id, oldest first.
var voicePhoneJobs struct {
	mu    sync.Mutex
	byID  map[string]*voicePhoneJob
	order []string
}

// voicePhoneCleanID keeps an id the page made (letters, digits, '-' and '_',
// at most 40) and drops anything else, which then runs as an unnamed send.
func voicePhoneCleanID(id string) string {
	if id == "" || len(id) > 40 {
		return ""
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return id
}

// voicePhoneJobFor returns the job already sent under id (true), or files job
// under it (false) — in one step, so two sends of one id at the same moment
// (a double tap) still run once. The oldest ids are forgotten past the bound.
func voicePhoneJobFor(id string, job *voicePhoneJob) (*voicePhoneJob, bool) {
	voicePhoneJobs.mu.Lock()
	defer voicePhoneJobs.mu.Unlock()
	if old, ok := voicePhoneJobs.byID[id]; ok {
		return old, true
	}
	if voicePhoneJobs.byID == nil {
		voicePhoneJobs.byID = make(map[string]*voicePhoneJob, voicePhoneJobsMax)
	}
	voicePhoneJobs.byID[id] = job
	voicePhoneJobs.order = append(voicePhoneJobs.order, id)
	for len(voicePhoneJobs.order) > voicePhoneJobsMax {
		delete(voicePhoneJobs.byID, voicePhoneJobs.order[0])
		voicePhoneJobs.order = voicePhoneJobs.order[1:]
	}
	return job, false
}

// voicePhoneJobDrop forgets a send that never ran, so its retry runs.
func voicePhoneJobDrop(id string, job *voicePhoneJob) {
	voicePhoneJobs.mu.Lock()
	defer voicePhoneJobs.mu.Unlock()
	if voicePhoneJobs.byID[id] != job {
		return
	}
	delete(voicePhoneJobs.byID, id)
	for i, x := range voicePhoneJobs.order {
		if x == id {
			voicePhoneJobs.order = append(voicePhoneJobs.order[:i], voicePhoneJobs.order[i+1:]...)
			break
		}
	}
}

// voicePhoneChoice is one row of the phone's "did you mean" list.
type voicePhoneChoice struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
}

// voicePhoneReply is the handheld's answer.
type voicePhoneReply struct {
	OK      bool               `json:"ok"`
	Say     string             `json:"say"`
	Heard   string             `json:"heard,omitempty"`
	Choices []voicePhoneChoice `json:"choices,omitempty"`
	Gen     uint64             `json:"gen,omitempty"`
}

// voicePhoneChoices is the last list sent to a phone, for its pick. Only the
// main loop writes it (voicePhoneRunOnMain); the lock keeps a test's reads
// honest.
var voicePhoneChoices struct {
	mu      sync.Mutex
	gen     uint64
	matches []voiceMatch
}

// handleVoicePage serves the phone page, PIN-guarded like the main page.
func (s *companionServer) handleVoicePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.throttled() {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	if !s.authorized(r) {
		s.noteFailure()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, companionLockedPage)
		return
	}
	s.noteSuccess()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", voicePageCSP)
	_, _ = io.WriteString(w, voicePhonePage)
}

// handleVoice runs one command (or one pick) and answers with what the
// handheld did.
func (s *companionServer) handleVoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var req voicePhoneRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if utf8.RuneCountInString(req.Text) > voiceMaxCommandRunes {
		http.Error(w, "that is too long for a command", http.StatusRequestEntityTooLarge)
		return
	}
	if req.Text == "" && req.Pick <= 0 {
		http.Error(w, "say or type a command first", http.StatusBadRequest)
		return
	}
	post, run := voicePhonePost, voicePhoneRun
	if post == nil {
		post = tbxPost
	}
	if run == nil {
		run = voicePhoneRunOnMain
	}
	id := voicePhoneCleanID(req.ID)
	job := &voicePhoneJob{answer: make(chan voicePhoneReply, 1)}
	if id != "" {
		if old, sent := voicePhoneJobFor(id, job); sent {
			// A retry of a send the handheld took: never run it twice.
			if rep, done := old.result(); done {
				writeCompanionJSON(w, rep)
				return
			}
			voicePhoneWriteStatus(w, http.StatusAccepted,
				"Still running on the handheld — check it before you send that again.")
			return
		}
	}
	post(func() {
		// A command the phone gave up on never runs: while a game has the
		// screen the main loop is parked, and "open YouTube" said during the
		// game would otherwise fire the moment it ends.
		if !job.claimed.CompareAndSwap(false, true) {
			return
		}
		rep := run(req)
		job.finish(rep)
		job.answer <- rep
	})
	timer := time.NewTimer(voicePhoneWait)
	defer timer.Stop()
	select {
	case rep := <-job.answer:
		writeCompanionJSON(w, rep)
		return
	case <-timer.C:
	case <-r.Context().Done():
		if job.claimed.CompareAndSwap(false, true) && id != "" {
			voicePhoneJobDrop(id, job)
		}
		return
	}
	if job.claimed.CompareAndSwap(false, true) {
		// The main loop never reached it, and now never will.
		if id != "" {
			voicePhoneJobDrop(id, job)
		}
		voicePhoneWriteStatus(w, http.StatusServiceUnavailable,
			"The handheld is busy — a game or an app may have the screen. Nothing ran; try again when JukaHub is back.")
		return
	}
	// It is running (a slow switch, say): its answer is worth a little more
	// waiting, and saying "busy" about a command that ran invited a retry
	// that ran it twice.
	extra := time.NewTimer(2 * voicePhoneWait)
	defer extra.Stop()
	select {
	case rep := <-job.answer:
		writeCompanionJSON(w, rep)
	case <-extra.C:
		voicePhoneWriteStatus(w, http.StatusAccepted,
			"The handheld is still working on it — it may have run already; check it before you send that again.")
	case <-r.Context().Done():
	}
}

// voicePhoneWriteStatus answers with a status other than 200 and the sentence
// the page shows.
func voicePhoneWriteStatus(w http.ResponseWriter, status int, say string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(voicePhoneReply{Say: say})
}

// voicePhoneRunOnMain runs a phone command on the main loop. On the voice page
// the page shows it too; anywhere else the command just runs, and a toast on
// the handheld says what the phone asked for.
func voicePhoneRunOnMain(req voicePhoneRequest) voicePhoneReply {
	config := appConfig
	// The ROM and file index is otherwise only built when the search sheet or
	// the voice page opens; a phone may be the first to ask. It refreshes in
	// the background and only when stale.
	homeSearchRefreshIndex(config)
	if voiceOnPage(config) {
		// The phone's command replaces whatever the page was doing: a listen
		// in progress closes its microphone (it stayed open and leaked when
		// the page moved on to the phone's answer), a request still with
		// Whisper is dropped. A pick that turns out stale or blocked does not
		// redraw the page, so a page left listening or understanding goes back
		// to ready instead of showing a state nothing will end.
		switch voicePage.phase {
		case voicePhaseListening, voicePhaseUnderstanding:
			voiceReset()
		default:
			voiceStopCapture()
		}
	}
	if req.Pick > 0 {
		return voicePhonePick(config, req)
	}
	text := voiceClip(req.Text)
	in := voiceParse(text)
	out := voiceExecute(config, in, voiceFromPhone)
	rep := voicePhoneReply{OK: true, Say: out.Say, Heard: text}
	if len(out.Choices) > 0 {
		rep.Choices, rep.Gen = voicePhoneRemember(out.Choices)
	}
	if out.Tone == tbxToneWarn || out.Tone == tbxToneBad {
		rep.OK = false
	}
	if voiceOnPage(config) {
		voicePage.heard = text
		voiceShow(config, out, voiceFromPhone)
		return rep
	}
	switch {
	case out.Cancel:
	case len(out.Choices) == 1:
		ShowToast("From your phone: "+out.Say+" — answer on the phone", ToastKindInfo)
	case len(out.Choices) > 0:
		ShowToast("Your phone found a few matches — pick one there", ToastKindInfo)
	case out.Act != nil:
		ShowToast("From your phone: "+out.Say, ToastKindInfo)
		out.Act(config)
	case out.Search != "":
		ShowToast("From your phone: "+out.Say, ToastKindWarning)
	case out.Say != "":
		kind := ToastKindInfo
		if !rep.OK {
			kind = ToastKindWarning
		}
		ShowToast(out.Say, kind)
	}
	return rep
}

// voicePhoneRemember keeps the list a phone was sent and returns it in the
// reply's form.
func voicePhoneRemember(matches []voiceMatch) ([]voicePhoneChoice, uint64) {
	voicePhoneChoices.mu.Lock()
	defer voicePhoneChoices.mu.Unlock()
	voicePhoneChoices.gen++
	voicePhoneChoices.matches = append(voicePhoneChoices.matches[:0], matches...)
	out := make([]voicePhoneChoice, 0, len(matches))
	for _, m := range matches {
		out = append(out, voicePhoneChoice{Title: m.Target.Title, Subtitle: m.Target.Where})
	}
	return out, voicePhoneChoices.gen
}

// voicePhonePick runs choice req.Pick of list req.Gen.
func voicePhonePick(config *Config, req voicePhoneRequest) voicePhoneReply {
	voicePhoneChoices.mu.Lock()
	stale := req.Gen != voicePhoneChoices.gen || req.Pick > len(voicePhoneChoices.matches)
	var m voiceMatch
	if !stale {
		m = voicePhoneChoices.matches[req.Pick-1]
	}
	voicePhoneChoices.mu.Unlock()
	if stale {
		return voicePhoneReply{Say: "That list is out of date — say the command again"}
	}
	out := voiceOutcomeFor(m)
	if out.Act == nil {
		return voicePhoneReply{Say: out.Say}
	}
	if voiceOnPage(config) {
		voicePage.act, voicePage.leave = out.Act, out.Leave
		voicePage.choices = nil
		voiceRunPending(config)
	} else {
		ShowToast("From your phone: "+out.Say, ToastKindInfo)
		out.Act(config)
	}
	return voicePhoneReply{OK: true, Say: out.Say}
}

// voicePageCSP keeps the page to itself: inline style and script, requests
// only to this handheld.
const voicePageCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// voicePhonePage is the phone's voice page.
const voicePhonePage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<meta name="referrer" content="no-referrer">
<title>JukaHub · Voice</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--ok:#3ecf8e;--bad:#ff6b6b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:16px 16px 40px;max-width:560px;margin:0 auto}
h1{font-size:21px;margin:8px 0 6px}
.lead{color:var(--dim);margin:0 0 14px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin-bottom:14px}
input{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:12px;color:var(--ink);padding:16px;font:20px/1.3 system-ui,sans-serif}
.btn{display:block;width:100%;font:600 17px system-ui,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:12px;padding:14px 16px;min-height:50px;margin-top:10px}
.btn:disabled{opacity:.6}
.chips{display:flex;flex-wrap:wrap;gap:8px;margin-top:12px}
.chip{background:#1d2330;color:var(--ink);border:1px solid var(--line);border-radius:999px;padding:9px 14px;font:15px system-ui,sans-serif}
#answer{min-height:1.5em;margin-top:12px;font-size:17px}
#answer.bad{color:var(--bad)}
#answer.ok{color:var(--ok)}
#heard{color:var(--dim);font-size:14px;margin-top:6px;min-height:1.2em}
#choices .chip{display:block;width:100%;text-align:left;margin-top:8px;border-radius:12px;padding:12px 14px}
#choices small{display:block;color:var(--dim);font-size:13px}
small.note{color:var(--dim);display:block;margin-top:10px;font-size:13px}
[hidden]{display:none!important}
</style>
<h1>Say what to open</h1>
<p class=lead>Tap the field, then the microphone on your keyboard, and speak. The handheld does it and answers here.</p>
<div class=card>
  <form id=f autocomplete=off>
    <input id=cmd enterkeyhint=send placeholder="open YouTube" autocapitalize=off spellcheck=false>
    <button class=btn id=send type=submit>Send to handheld</button>
  </form>
  <div id=heard></div>
  <div id=answer></div>
  <button class=btn id=again type=button hidden>Try again</button>
  <div id=choices></div>
  <div class=chips>
    <button class=chip data-say="open YouTube">open YouTube</button>
    <button class=chip data-say="go home">go home</button>
    <button class=chip data-say="volume 50">volume 50</button>
    <button class=chip data-say="take a screenshot">take a screenshot</button>
    <button class=chip data-say="next song">next song</button>
    <button class=chip data-say="what time is it">what time is it</button>
  </div>
  <small class=note>Works with any keyboard that has dictation (Gboard, the iPhone keyboard, Samsung). Nothing you say leaves your phone except the words.</small>
</div>
<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const cmd = document.getElementById('cmd');
const answer = document.getElementById('answer');
const heard = document.getElementById('heard');
const choices = document.getElementById('choices');
const send = document.getElementById('send');
const again = document.getElementById('again');
// Every send has an id; Try again resends the same one, so the handheld never
// runs one command twice.
let last = null;
function newId() { return Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10); }
function show(text, cls) { answer.textContent = text; answer.className = cls || ''; }
async function post(body) {
  last = body;
  again.hidden = true;
  send.disabled = true;
  try {
    const res = await fetch('/api/voice', {method: 'POST', headers: {'X-Companion-Pin': pin, 'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    const text = (await res.text()).trim();
    let data = null;
    try { data = JSON.parse(text); } catch (e) {}
    if (!data) throw new Error(text || res.statusText);
    heard.textContent = data.heard ? '“' + data.heard + '”' : '';
    show(data.say || (res.ok ? 'Done' : 'Something went wrong'), data.ok ? 'ok' : 'bad');
    // Busy (nothing ran) or still running: the same send can be asked again.
    again.hidden = !(res.status === 503 || res.status === 202);
    choices.textContent = '';
    (data.choices || []).forEach((c, i) => {
      const b = document.createElement('button');
      b.className = 'chip';
      b.textContent = c.title;
      if (c.subtitle) { const s = document.createElement('small'); s.textContent = c.subtitle; b.appendChild(s); }
      b.onclick = () => post({pick: i + 1, gen: data.gen, id: newId()});
      choices.appendChild(b);
    });
  } catch (e) {
    show(e.message, 'bad');
    again.hidden = false;
  } finally {
    send.disabled = false;
  }
}
again.onclick = () => { if (last) post(last); };
document.getElementById('f').onsubmit = ev => {
  ev.preventDefault();
  const text = cmd.value.trim();
  if (!text) { cmd.focus(); return; }
  post({text: text, id: newId()});
  cmd.value = '';
};
document.querySelectorAll('[data-say]').forEach(b => { b.onclick = () => post({text: b.dataset.say, id: newId()}); });
cmd.focus();
</script>
`
