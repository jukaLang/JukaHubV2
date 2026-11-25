package main

// companion_notes.go — the Phone Companion's Notes page (/notes): the
// handheld's notes on a phone, to read, write with the phone's keyboard, add
// from .txt files and download as text (Juka, 2026-11-25: "save notes as a
// file and sync to telephone if needed").
//
// The page is opened from the main companion page's Notes card, or from the
// handheld (Notes → Select → Open on phone, or the editor's menu: the QR code
// opens the page at that note, "#n<id>").
//
//	GET  /api/notes           the list: every note's title, folder, a preview,
//	                          when it changed, its revision; the folders; the
//	                          note open on the handheld
//	GET  /api/notes?id=N      one note, whole
//	POST /api/notes           {"action": "save" | "delete", "id", "rev",
//	                          "title", "folder", "content"}; id 0 saves a new
//	                          note. A save or delete naming a revision the
//	                          note has moved past (it was changed on the
//	                          handheld meanwhile) is refused with 409 and the
//	                          note as it is now, so nothing is overwritten
//	                          unseen; the page offers to keep both.
//	GET  /api/notes/file?id=N the note as a .txt download
//	GET  /api/notes/all.zip   every note as .txt files, one folder per notes
//	                          folder
//
// Everything is behind the PIN (s.guard; the page itself checks it the way
// /music does). A body is at most 64 KB; a title at most notesTitleMax bytes
// and a note notesBodyMax, cleaned of control characters other than line
// breaks and tabs. The store is shared with the handheld through notesMu
// (notes.go); what changes on the screen is drawn on its next frame, and a
// toast says what arrived.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// notesPhoneBodyMax bounds a POST body.
	notesPhoneBodyMax = 64 << 10
	// notesPhonePreview is how much of a note the list shows.
	notesPhonePreview = 140
)

// The page's own failures.
var (
	errNotesPhoneAction   = errors.New("Unknown action")
	errNotesPhoneNoNote   = errors.New("That note is not on the handheld any more")
	errNotesPhoneLong     = fmt.Errorf("That note is too long: %d characters at most", notesBodyMax)
	errNotesPhoneFolder   = errors.New("That folder is not on the handheld")
	errNotesPhoneConflict = errors.New("Changed on the handheld since you opened it")
	errNotesPhoneNotReady = errors.New("Notes is not ready on the handheld yet")
)

// notesPhoneNote is a note as the page gets it. Updated is written the way
// the handheld writes dates; UpdatedAt is for sorting.
type notesPhoneNote struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Folder    string `json:"folder"`
	Preview   string `json:"preview,omitempty"`
	Content   string `json:"content,omitempty"`
	Rev       int    `json:"rev"`
	Updated   string `json:"updated"`
	UpdatedAt string `json:"updatedAt"`
	File      string `json:"file,omitempty"`
}

// notesPhoneState is the answer to the list and to every action.
type notesPhoneState struct {
	Notes    []notesPhoneNote `json:"notes"`
	Folders  []string         `json:"folders"`
	Open     int              `json:"open,omitempty"`
	MaxTitle int              `json:"maxTitle"`
	MaxBody  int              `json:"maxBody"`
	FilesIn  string           `json:"filesIn"`
	Message  string           `json:"message,omitempty"`
	Note     *notesPhoneNote  `json:"note,omitempty"`
	Conflict bool             `json:"conflict,omitempty"`
}

// notesPhoneRequest is a POST body.
type notesPhoneRequest struct {
	Action  string `json:"action"`
	ID      int    `json:"id"`
	Rev     int    `json:"rev"`
	Title   string `json:"title"`
	Folder  string `json:"folder"`
	Content string `json:"content"`
}

// registerNotesRoutes adds the page and its endpoints.
func (s *companionServer) registerNotesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/notes", s.handleNotesPage)
	mux.HandleFunc("/api/notes", s.guard(s.handleNotesAPI))
	mux.HandleFunc("/api/notes/file", s.guard(s.handleNotesFile))
	mux.HandleFunc("/api/notes/all.zip", s.guard(s.handleNotesZip))
}

// companionNotesURL is the pairing link (companionURL) turned into the Notes
// page's, at note id when it is not 0. A link that does not parse comes back
// as it is.
func companionNotesURL(link string, id int) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return link
	}
	u.Path = "/notes"
	u.Fragment = ""
	if id > 0 {
		u.Fragment = "n" + strconv.Itoa(id)
	}
	return u.String()
}

func (s *companionServer) handleNotesPage(w http.ResponseWriter, r *http.Request) {
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
	_, _ = io.WriteString(w, notesPhonePage)
}

// handleNotesAPI answers GET with the list (or one note) and POST with one
// action.
func (s *companionServer) handleNotesAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		if raw := r.URL.Query().Get("id"); raw != "" {
			id, err := strconv.Atoi(raw)
			n, ok := notesFind(id)
			if err != nil || !ok {
				http.Error(w, errNotesPhoneNoNote.Error(), http.StatusNotFound)
				return
			}
			st := notesPhoneSnapshot("")
			one := notesPhoneFull(n)
			st.Note = &one
			writeCompanionJSON(w, st)
			return
		}
		writeCompanionJSON(w, notesPhoneSnapshot(""))
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, notesPhoneBodyMax)
	var req notesPhoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	msg, saved, current, err := notesPhoneDo(req, time.Now())
	if errors.Is(err, errNotesPhoneConflict) {
		st := notesPhoneSnapshot(err.Error())
		st.Conflict = true
		if current != nil {
			one := notesPhoneFull(*current)
			st.Note = &one
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(st)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), notesPhoneStatus(err))
		return
	}
	st := notesPhoneSnapshot(msg)
	if saved != nil {
		one := notesPhoneFull(*saved)
		st.Note = &one
	}
	writeCompanionJSON(w, st)
}

// notesPhoneStatus is the HTTP status for one of the fixed failures.
func notesPhoneStatus(err error) int {
	switch {
	case errors.Is(err, errNotesPhoneNoNote):
		return http.StatusNotFound
	case errors.Is(err, errNotesPhoneLong):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, errNotesPhoneNotReady):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// notesPhoneClean drops control characters other than line breaks and tabs,
// and makes every line break a "\n". oneLine folds them into spaces (titles).
// Pure.
func notesPhoneClean(s string, oneLine bool) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\r\n", "\n"), "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			if oneLine || r == '\r' {
				return ' '
			}
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// notesPhoneDo carries out one action. It returns the page's sentence, the
// note saved, and — for a conflict — the note as it is now.
func notesPhoneDo(req notesPhoneRequest, now time.Time) (string, *noteEntry, *noteEntry, error) {
	switch req.Action {
	case "save":
		// A title is a label: cut to the handheld's limit (bytes, so a title in
		// another script is cut sooner than the page's character count).
		title := strings.TrimSpace(notesTrimTitle(strings.TrimSpace(notesPhoneClean(req.Title, true))))
		content := notesPhoneClean(req.Content, false)
		if len(content) > notesBodyMax {
			return "", nil, nil, errNotesPhoneLong
		}
		if title == "" {
			title = notesPhoneTitleFrom(content)
		}
		notesMu.Lock()
		defer notesMu.Unlock()
		if notes.storePath == "" {
			return "", nil, nil, errNotesPhoneNotReady
		}
		folder, ok := notesPhoneFolderLocked(req.Folder)
		if !ok {
			return "", nil, nil, errNotesPhoneFolder
		}
		if req.ID <= 0 {
			notes.store.NextID++
			n := noteEntry{ID: notes.store.NextID, Title: title, Content: content, Folder: folder,
				CreatedAt: now, UpdatedAt: now, Rev: 1}
			notes.store.Notes = append([]noteEntry{n}, notes.store.Notes...)
			notesSaveLocked()
			notesPhoneTell("Note “" + notesSheetTitle(title) + "” added from your phone")
			return "Saved on the handheld", &n, nil, nil
		}
		i := notesIndexLocked(req.ID)
		if i < 0 {
			return "", nil, nil, errNotesPhoneNoNote
		}
		cur := notes.store.Notes[i]
		if req.Rev != cur.Rev {
			return "", nil, &cur, errNotesPhoneConflict
		}
		if cur.Title == title && cur.Content == content && cur.Folder == folder {
			return "No changes to save", &cur, nil, nil
		}
		n := &notes.store.Notes[i]
		n.Title, n.Content, n.Folder, n.UpdatedAt = title, content, folder, now
		n.Rev++
		out := *n
		notesSaveLocked()
		notesPhoneTell("Note “" + notesSheetTitle(title) + "” changed on your phone")
		return "Saved on the handheld", &out, nil, nil
	case "delete":
		notesMu.Lock()
		defer notesMu.Unlock()
		i := notesIndexLocked(req.ID)
		if req.ID <= 0 || i < 0 {
			return "", nil, nil, errNotesPhoneNoNote
		}
		cur := notes.store.Notes[i]
		if req.Rev != cur.Rev {
			return "", nil, &cur, errNotesPhoneConflict
		}
		notes.store.Notes = append(notes.store.Notes[:i], notes.store.Notes[i+1:]...)
		notesSaveLocked()
		notesPhoneTell("Note “" + notesSheetTitle(cur.Title) + "” deleted from your phone")
		return "Deleted", nil, nil, nil
	}
	return "", nil, nil, errNotesPhoneAction
}

// notesPhoneTell shows what arrived from the phone, on the main thread.
func notesPhoneTell(msg string) {
	tbxPost(func() { ShowToast(msg, ToastKindInfo) })
}

// notesPhoneTitleFrom names an untitled note after its first words. Pure.
func notesPhoneTitleFrom(content string) string {
	line := strings.TrimSpace(strings.TrimLeft(strings.SplitN(strings.TrimSpace(content), "\n", 2)[0], "#-*> "))
	if line == "" {
		return "Note from phone"
	}
	if utf8.RuneCountInString(line) > 40 {
		line = string([]rune(line)[:39]) + "…"
	}
	return notesTrimTitle(line)
}

// notesPhoneFolderLocked is the folder a note from the page goes into: the
// one it names (any case), or the first folder when it names none. The
// caller holds notesMu.
func notesPhoneFolderLocked(name string) (string, bool) {
	name = strings.TrimSpace(name)
	var first string
	for _, f := range notes.store.Folders {
		if f == "All" {
			continue
		}
		if first == "" {
			first = f
		}
		if strings.EqualFold(f, name) {
			return f, true
		}
	}
	if name == "" || name == "All" {
		if first == "" {
			first = "Personal"
		}
		return first, true
	}
	return "", false
}

// notesPhoneSummary is a note in the list.
func notesPhoneSummary(n noteEntry, now time.Time) notesPhoneNote {
	preview := strings.Join(strings.Fields(n.Content), " ")
	if utf8.RuneCountInString(preview) > notesPhonePreview {
		preview = string([]rune(preview)[:notesPhonePreview-1]) + "…"
	}
	return notesPhoneNote{ID: n.ID, Title: n.Title, Folder: n.Folder, Preview: preview, Rev: n.Rev,
		Updated: formatDateTimeNear(nil, n.UpdatedAt, now), UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339), File: n.File}
}

// notesPhoneFull is a whole note.
func notesPhoneFull(n noteEntry) notesPhoneNote {
	out := notesPhoneSummary(n, time.Now())
	out.Preview = ""
	out.Content = n.Content
	return out
}

// notesPhoneSnapshot is the list as the page draws it, newest first.
func notesPhoneSnapshot(msg string) notesPhoneState {
	notesMu.Lock()
	all := append([]noteEntry(nil), notes.store.Notes...)
	folders := append([]string(nil), notes.store.Folders...)
	notesMu.Unlock()
	now := time.Now()
	st := notesPhoneState{
		Notes:    make([]notesPhoneNote, 0, len(all)),
		Open:     int(notesOpenNow.Load()),
		MaxTitle: notesTitleMax,
		MaxBody:  notesBodyMax,
		FilesIn:  notesShownPath(""),
		Message:  msg,
	}
	for _, f := range folders {
		if f != "All" {
			st.Folders = append(st.Folders, f)
		}
	}
	sortNotesNewestFirst(all)
	for _, n := range all {
		st.Notes = append(st.Notes, notesPhoneSummary(n, now))
	}
	return st
}

// sortNotesNewestFirst orders notes by their last change, newest first.
func sortNotesNewestFirst(list []noteEntry) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].UpdatedAt.After(list[j-1].UpdatedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// notesAttachment is a Content-Disposition for a download called name.
func notesAttachment(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return `attachment; filename="note.txt"`
}

// handleNotesFile sends one note as a text file.
func (s *companionServer) handleNotesFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	n, ok := notesFind(id)
	if err != nil || !ok {
		http.Error(w, errNotesPhoneNoNote.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", notesAttachment(notesFileName(n.Title)+".txt"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(notesFileText(n))
}

// handleNotesZip sends every note as text files in a zip, one folder per
// notes folder, under Notes/.
func (s *companionServer) handleNotesZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	notesMu.Lock()
	all := append([]noteEntry(nil), notes.store.Notes...)
	notesMu.Unlock()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", notesAttachment("JukaHub notes "+time.Now().Format("2006-01-02")+".zip"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	for _, n := range all {
		dir, base := notesFolderOnDisk(n.Folder), notesFileName(n.Title)
		name := ""
		for k := 1; k < 1000; k++ {
			cand := "Notes/" + dir + "/" + base + ".txt"
			if k > 1 {
				cand = fmt.Sprintf("Notes/%s/%s (%d).txt", dir, base, k)
			}
			if !used[strings.ToLower(cand)] {
				name = cand
				break
			}
		}
		if name == "" {
			continue
		}
		used[strings.ToLower(name)] = true
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: n.UpdatedAt}
		f, err := zw.CreateHeader(hdr)
		if err != nil {
			break
		}
		if _, err := f.Write(notesFileText(n)); err != nil {
			break
		}
	}
	_ = zw.Close()
}

// notesPhonePage is the /notes page.
const notesPhonePage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub · Notes</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--bad:#ff6b6b;--ok:#3ecf8e}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:calc(14px + env(safe-area-inset-top)) 16px calc(40px + env(safe-area-inset-bottom));max-width:720px;margin:0 auto}
h1{font-size:22px;margin:0}
.top{display:flex;align-items:center;gap:10px;justify-content:space-between;flex-wrap:wrap}
.note{color:var(--dim);font-size:14px;margin:6px 0 10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px 16px;margin:10px 0}
.item{cursor:pointer}
.item:active{border-color:var(--accent)}
.item h3{margin:0 0 4px;font-size:17px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.item p{margin:0;color:var(--dim);font-size:14px;max-height:3em;overflow:hidden}
.meta{color:var(--dim);font-size:12px;margin-top:6px}
.badge{color:var(--ok)}
input[type=text],input[type=search],select,textarea{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px/1.45 system-ui,-apple-system,sans-serif}
textarea{min-height:45vh;resize:vertical}
select{appearance:none}
button,.btn{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:11px 15px;min-height:44px;text-decoration:none;display:inline-flex;align-items:center;justify-content:center}
button.ghost,.btn.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
button.bad{background:#3a1d24;color:#ffb3b3;border:1px solid #5a2a34}
button:disabled{opacity:.6}
.row{display:flex;gap:10px;margin-top:10px;flex-wrap:wrap}
.row>*{flex:1 1 120px}
.chips{display:flex;gap:8px;flex-wrap:wrap;margin:10px 0}
.chip{background:#1d2330;border:1px solid var(--line);color:var(--ink);border-radius:999px;padding:6px 12px;font-size:14px;min-height:34px}
.chip.on{background:var(--accent);border-color:var(--accent);color:#fff}
.msg{margin-top:10px;font-size:14px;color:var(--dim)}
.msg:empty{display:none}
.msg.bad{color:var(--bad)}
.count{color:var(--dim);font-size:12px;text-align:right;margin-top:4px}
small{color:var(--dim);display:block;margin-top:10px;font-size:12px}
[hidden]{display:none!important}
</style>

<section id=listView>
  <div class=top><h1>Notes</h1><button id=newNote>New note</button></div>
  <p class=note id=status>Asking the handheld…</p>
  <input type=search id=q placeholder="Search notes" autocomplete=off>
  <div class=chips id=folders></div>
  <div id=items></div>
  <div class=row><a class="btn ghost" id=zip href="#">Download all (.zip)</a><button class=ghost id=addFiles type=button>Add .txt files</button></div>
  <input type=file id=files accept=".txt,.md,text/plain,text/markdown" multiple hidden>
  <div class=msg id=listMsg aria-live=polite></div>
  <small id=filesNote>Saved files on the handheld are in its Notes folder.</small>
</section>

<section id=editView hidden>
  <div class=top><button class=ghost id=back type=button>‹ Notes</button><span class=note id=editState></span></div>
  <div class=card>
    <input type=text id=title placeholder="Title" autocomplete=off enterkeyhint=next>
    <div class=row><select id=folder aria-label="Folder"></select></div>
    <div class=row><textarea id=body placeholder="Write your note"></textarea></div>
    <div class=count id=count></div>
    <div class=row><button id=save>Save on handheld</button><a class="btn ghost" id=download href="#">Download .txt</a></div>
    <div class=row id=conflictRow hidden><button class=ghost id=keepBoth>Keep both (mine as a copy)</button><button class=ghost id=loadTheirs>Show the handheld's</button></div>
    <div class=row><button class=bad id=del type=button>Delete note</button></div>
    <div class=msg id=editMsg aria-live=polite></div>
  </div>
  <small>Lines starting with # are headings, - bullets and &gt; quotes on the handheld. Ctrl+S saves.</small>
</section>

<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const $ = id => document.getElementById(id);
function hdr(json) { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; }
let state = {notes: [], folders: [], maxTitle: 80, maxBody: 16000};
let folder = '', editing = null, dirty = false;
function say(id, text, bad) { const el = $(id); el.textContent = text || ''; el.className = bad ? 'msg bad' : 'msg'; }
async function getJSON(path) {
  const res = await fetch(path, {headers: hdr(false), cache: 'no-store'});
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  return JSON.parse(text);
}
async function post(body) {
  const res = await fetch('/api/notes', {method: 'POST', headers: hdr(true), body: JSON.stringify(body)});
  const text = await res.text();
  let data = {};
  try { data = JSON.parse(text); } catch (_) {}
  if (res.status === 409) { const e = new Error(data.message || 'Changed on the handheld'); e.conflict = data; throw e; }
  if (!res.ok) throw new Error(text.trim() || res.statusText);
  return data;
}
function link(path, extra) { return path + '?p=' + encodeURIComponent(pin) + (extra || ''); }
function chip(name, label) {
  const b = document.createElement('button');
  b.type = 'button'; b.className = 'chip' + (folder === name ? ' on' : ''); b.textContent = label;
  b.onclick = () => { folder = name; draw(); };
  return b;
}
function draw() {
  const q = $('q').value.trim().toLowerCase();
  const box = $('folders'); box.textContent = '';
  box.appendChild(chip('', 'All'));
  state.folders.forEach(f => box.appendChild(chip(f, f)));
  const list = state.notes.filter(n => (!folder || n.folder === folder) &&
    (!q || (n.title + ' ' + (n.preview || '')).toLowerCase().indexOf(q) >= 0));
  const items = $('items'); items.textContent = '';
  if (!list.length) {
    const p = document.createElement('p'); p.className = 'note';
    p.textContent = state.notes.length ? 'No notes match.' : 'No notes yet: New note writes one with your phone’s keyboard.';
    items.appendChild(p);
  }
  list.forEach(n => {
    const c = document.createElement('div'); c.className = 'card item';
    const h = document.createElement('h3'); h.textContent = n.title || 'Untitled';
    const p = document.createElement('p'); p.textContent = n.preview || '';
    const m = document.createElement('div'); m.className = 'meta';
    m.textContent = n.folder + ' · ' + n.updated + (n.file ? ' · saved as a file' : '');
    if (state.open === n.id) { const b = document.createElement('span'); b.className = 'badge'; b.textContent = ' · open on the handheld'; m.appendChild(b); }
    c.append(h, p, m);
    c.onclick = () => openNote(n.id);
    items.appendChild(c);
  });
  const total = state.notes.length;
  $('status').textContent = total === 1 ? '1 note on the handheld.' : total + ' notes on the handheld.';
  $('filesNote').textContent = 'Notes saved as files on the handheld are in ' + (state.filesIn || 'its Notes folder') + '.';
}
async function load() {
  state = await getJSON('/api/notes');
  draw();
}
function fillFolders(selected) {
  const sel = $('folder'); sel.textContent = '';
  state.folders.forEach(f => { const o = document.createElement('option'); o.value = f; o.textContent = f; if (f === selected) o.selected = true; sel.appendChild(o); });
}
function count() {
  const n = new TextEncoder().encode($('body').value).length;
  $('count').textContent = n > state.maxBody * 0.8 ? n + ' / ' + state.maxBody : '';
}
function showEditor(note) {
  editing = note; dirty = false;
  $('title').value = note.title || '';
  $('title').maxLength = state.maxTitle;
  fillFolders(note.folder || folder || state.folders[0]);
  $('body').value = note.content || '';
  $('download').hidden = !note.id;
  $('del').hidden = !note.id;
  if (note.id) $('download').href = link('/api/notes/file', '&id=' + note.id);
  $('editState').textContent = note.id ? 'Changed ' + note.updated : 'New note';
  $('conflictRow').hidden = true;
  say('editMsg', '');
  $('listView').hidden = true; $('editView').hidden = false;
  count();
  window.scrollTo(0, 0);
  if (!note.id) $('title').focus();
}
async function openNote(id) {
  try {
    const d = await getJSON('/api/notes?id=' + id);
    state = d; draw();
    if (d.note) { showEditor(d.note); history.replaceState(null, '', '#n' + id); }
  } catch (e) { say('listMsg', e.message, true); }
}
function leave() {
  if (dirty && !confirm('Leave without saving?')) return;
  editing = null; dirty = false;
  $('editView').hidden = true; $('listView').hidden = false;
  history.replaceState(null, '', location.pathname + location.search);
  load().catch(e => say('listMsg', e.message, true));
}
async function save(asCopy) {
  if (!editing) return;
  const b = $('save'); b.disabled = true;
  try {
    const body = {action: 'save', id: asCopy ? 0 : (editing.id || 0), rev: editing.rev || 0,
      title: $('title').value, folder: $('folder').value, content: $('body').value};
    if (asCopy && body.title) body.title = (body.title + ' (copy)').slice(0, state.maxTitle);
    const d = await post(body);
    state = d; draw();
    if (d.note) { editing = d.note; $('download').hidden = false; $('del').hidden = false; $('download').href = link('/api/notes/file', '&id=' + d.note.id); history.replaceState(null, '', '#n' + d.note.id); }
    dirty = false; $('conflictRow').hidden = true;
    $('editState').textContent = 'Saved';
    say('editMsg', d.message || 'Saved on the handheld');
  } catch (e) {
    if (e.conflict) { $('conflictRow').hidden = false; say('editMsg', e.message + ': keep both, or look at the handheld’s version first.', true); editing.theirs = e.conflict.note; }
    else say('editMsg', e.message, true);
  } finally { b.disabled = false; }
}
$('newNote').onclick = () => showEditor({id: 0, rev: 0, title: '', folder: folder || state.folders[0], content: ''});
$('back').onclick = leave;
$('save').onclick = () => save(false);
$('keepBoth').onclick = () => save(true);
$('loadTheirs').onclick = () => { if (editing && editing.theirs) { if (!dirty || confirm('Replace what you typed with the handheld’s version?')) showEditor(editing.theirs); } };
$('del').onclick = async () => {
  if (!editing || !editing.id || !confirm('Delete this note from the handheld?')) return;
  try { const d = await post({action: 'delete', id: editing.id, rev: editing.rev}); state = d; dirty = false; leave(); say('listMsg', d.message); }
  catch (e) { if (e.conflict) { say('editMsg', e.message + '. Open it again to see the change.', true); } else say('editMsg', e.message, true); }
};
['title', 'body', 'folder'].forEach(id => $(id).addEventListener('input', () => { dirty = true; if (id === 'body') count(); }));
$('title').addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); $('body').focus(); } });
document.addEventListener('keydown', e => { if ((e.ctrlKey || e.metaKey) && e.key === 's' && editing) { e.preventDefault(); save(false); } });
$('q').addEventListener('input', draw);
$('zip').href = link('/api/notes/all.zip');
$('addFiles').onclick = () => $('files').click();
$('files').onchange = async () => {
  const files = Array.from($('files').files || []);
  let added = 0, failed = '';
  for (const f of files) {
    try {
      if (f.size > state.maxBody * 4) throw new Error(f.name + ' is too large');
      const text = await f.text();
      await post({action: 'save', id: 0, title: f.name.replace(/\.(txt|md|markdown)$/i, '').slice(0, state.maxTitle), folder: folder || '', content: text});
      added++;
    } catch (e) { failed = e.message; }
  }
  $('files').value = '';
  say('listMsg', added + (added === 1 ? ' note added' : ' notes added') + (failed ? ' · ' + failed : ''), !!failed && !added);
  load().catch(e => say('listMsg', e.message, true));
};
window.addEventListener('beforeunload', e => { if (dirty) { e.preventDefault(); e.returnValue = ''; } });
const m = /^#n(\d+)$/.exec(location.hash);
load().then(() => { if (m) openNote(+m[1]); }).catch(e => { $('status').textContent = e.message; });
function tick() {
  if (!editing && !document.hidden) load().catch(() => {});
  setTimeout(tick, 15000);
}
setTimeout(tick, 15000);
</script>
`
