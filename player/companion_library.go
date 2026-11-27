package main

// companion_library.go — the Phone Companion's Games page: the handheld's
// library on the phone, with each game's saves.
//
//   - /games is the page (the PIN in its address, as the code carries it);
//   - GET /api/games lists the library: title, console, whether it has a
//     cover; /api/games/art serves a cover (only a library game's own);
//   - POST /api/games/play starts a game on the handheld, through the same
//     queue a phone key press takes, so it runs on the main loop exactly as a
//     press on Home would;
//   - GET /api/games/saves lists one game's save files and its Save Time
//     Machine moments; /api/games/saves.zip downloads the files; POST
//     /api/games/saves/upload puts one file the phone sends where that game's
//     saves of its kind live, after a "Before a save came from the phone"
//     moment; POST /api/games/restore restores a moment.
//
// Everything is behind the PIN (guard), and nothing that writes runs while a
// game is on the handheld's screen: its emulator holds the saves open. The
// library itself belongs to the main loop (hometabs_games.go), so the list is
// a copy it hands over through tbxPost; while a game runs the last copy is
// served.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// companionLibTTL is how long a copy of the library answers the phone.
	companionLibTTL = time.Minute
	// companionLibWait bounds the wait for the main loop's copy.
	companionLibWait = 3 * time.Second
	// companionArtMax bounds a cover sent to the phone.
	companionArtMax = 4 << 20
	// companionSaveMax bounds one save the phone uploads (a state can be big).
	companionSaveMax = saveManagerMaxFile
	// companionEventPlay is a phone's "play this" for the main loop.
	companionEventPlay = "play"
)

// companionGamesCSP lets the page load the covers it shows, and nothing else.
const companionGamesCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// companionGame is one library game as the phone sees it.
type companionGame struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Console string `json:"console"`
	Art     bool   `json:"art"`
	game    libraryGame
}

// companionLib is the copy of the library the handlers read.
var companionLib struct {
	mu    sync.Mutex
	at    time.Time
	ready bool
	games []companionGame
	byID  map[string]companionGame
	wait  chan struct{}
}

// companionGameID is a game's id on the phone: a hash of its path.
func companionGameID(p string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(p))
	return strconv.FormatUint(h.Sum64(), 36)
}

// companionLibFill copies the library for the phone. Main thread.
func companionLibFill() {
	lib := homeTabsLibrary()
	ready := homeTabsLib.ready
	games := make([]companionGame, 0, lib.Total)
	byID := make(map[string]companionGame, lib.Total)
	for _, sys := range lib.Systems {
		console := consoleDisplayName(sys.Name)
		if console == "" {
			console = sys.Name
		}
		for _, g := range sys.Games {
			if g.Hidden || strings.TrimSpace(g.Path) == "" {
				continue
			}
			cg := companionGame{ID: companionGameID(g.Path), Title: g.Label(), Console: console, Art: g.HasArt && g.Art != "", game: g}
			if _, dup := byID[cg.ID]; dup {
				continue
			}
			games = append(games, cg)
			byID[cg.ID] = cg
		}
	}
	sort.SliceStable(games, func(i, j int) bool {
		return strings.ToLower(games[i].Title) < strings.ToLower(games[j].Title)
	})
	companionLib.mu.Lock()
	companionLib.games, companionLib.byID, companionLib.ready = games, byID, ready
	companionLib.at = time.Now()
	if companionLib.wait != nil {
		close(companionLib.wait)
		companionLib.wait = nil
	}
	companionLib.mu.Unlock()
}

// companionLibSnapshot is the library for a handler: a fresh copy from the
// main loop when the last one is old (waiting companionLibWait at most), or
// the last one. ready is false while the handheld has not finished its first
// look through the card.
func companionLibSnapshot() (games []companionGame, byID map[string]companionGame, ready bool) {
	companionLib.mu.Lock()
	if time.Since(companionLib.at) >= companionLibTTL || !companionLib.ready {
		wait := companionLib.wait
		if wait == nil {
			wait = make(chan struct{})
			companionLib.wait = wait
			tbxPost(companionLibFill)
			stillWake()
		}
		companionLib.mu.Unlock()
		select {
		case <-wait:
		case <-time.After(companionLibWait):
		}
		companionLib.mu.Lock()
	}
	games, byID, ready = companionLib.games, companionLib.byID, companionLib.ready
	companionLib.mu.Unlock()
	return games, byID, ready
}

// companionLibGame finds one game by its id.
func companionLibGame(id string) (companionGame, bool) {
	_, byID, _ := companionLibSnapshot()
	g, ok := byID[strings.TrimSpace(id)]
	return g, ok
}

// registerLibraryRoutes adds the Games page's routes.
func (s *companionServer) registerLibraryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/games", s.handleGamesPage)
	mux.HandleFunc("/api/games", s.guard(s.handleGamesList))
	mux.HandleFunc("/api/games/art", s.guard(s.handleGamesArt))
	mux.HandleFunc("/api/games/play", s.guard(s.handleGamesPlay))
	mux.HandleFunc("/api/games/saves", s.guard(s.handleGamesSaves))
	mux.HandleFunc("/api/games/saves.zip", s.guard(s.handleGamesSavesZip))
	mux.HandleFunc("/api/games/saves/upload", s.guard(s.handleGamesSaveUpload))
	mux.HandleFunc("/api/games/restore", s.guard(s.handleGamesRestore))
	// The Journal fields: status, rating and note from the phone
	// (companion_journal.go).
	mux.HandleFunc("/api/games/journal", s.guard(s.handleGamesJournal))
}

// handleGamesPage serves /games.
func (s *companionServer) handleGamesPage(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Security-Policy", companionGamesCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, companionGamesPage)
}

// handleGamesList answers GET /api/games.
func (s *companionServer) handleGamesList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	games, _, ready := companionLibSnapshot()
	if games == nil {
		games = []companionGame{}
	}
	writeCompanionJSON(w, map[string]interface{}{"games": games, "ready": ready, "playing": saveTimelineGameRunning()})
}

// handleGamesArt answers GET /api/games/art?id=: the game's cover, when the
// library knows one; nothing else on the card is ever served here.
func (s *companionServer) handleGamesArt(w http.ResponseWriter, r *http.Request) {
	g, ok := companionLibGame(r.URL.Query().Get("id"))
	if !ok || !g.Art {
		http.NotFound(w, r)
		return
	}
	art := g.game.Art
	ctype := ""
	switch strings.ToLower(filepath.Ext(art)) {
	case ".png":
		ctype = "image/png"
	case ".jpg", ".jpeg":
		ctype = "image/jpeg"
	case ".webp":
		ctype = "image/webp"
	default:
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(art)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > companionArtMax {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, f)
}

// companionGameCall is a POST body naming a game (and a moment).
type companionGameCall struct {
	ID     string `json:"id"`
	Moment string `json:"moment"`
}

// readCompanionGameCall reads a small JSON body.
func readCompanionGameCall(w http.ResponseWriter, r *http.Request) (companionGameCall, bool) {
	var call companionGameCall
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return call, false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&call); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return call, false
	}
	return call, true
}

// handleGamesPlay answers POST /api/games/play {"id"}.
func (s *companionServer) handleGamesPlay(w http.ResponseWriter, r *http.Request) {
	call, ok := readCompanionGameCall(w, r)
	if !ok {
		return
	}
	g, ok := companionLibGame(call.ID)
	if !ok {
		http.Error(w, "that game is not in the library any more", http.StatusNotFound)
		return
	}
	if saveTimelineGameRunning() {
		http.Error(w, "a game is running on the handheld — quit it first", http.StatusConflict)
		return
	}
	s.sink(companionEvent{Kind: companionEventPlay, Text: g.ID})
	stillWake()
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "title": g.Title})
}

// companionPlayQueued is the main loop's half of a phone's "play this"
// (drainCompanionQueue).
func companionPlayQueued(id string) {
	companionLib.mu.Lock()
	g, ok := companionLib.byID[id]
	companionLib.mu.Unlock()
	if !ok {
		return
	}
	ShowToast("Phone: starting "+g.Title, ToastKindInfo)
	playRecentGame(recentGame{Path: g.game.Path, System: g.game.System, Title: g.game.Title})
}

// companionSaveFile is one save file as the phone lists it.
type companionSaveFile struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	Folder string `json:"folder"`
}

// companionMoment is one Time Machine moment as the phone lists it.
type companionMoment struct {
	ID         string `json:"id"`
	When       string `json:"when"`
	What       string `json:"what"`
	Size       int64  `json:"size"`
	Checkpoint bool   `json:"checkpoint"`
	Name       string `json:"name,omitempty"`
}

// handleGamesSaves answers GET /api/games/saves?id=.
func (s *companionServer) handleGamesSaves(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	g, ok := companionLibGame(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "that game is not in the library any more", http.StatusNotFound)
		return
	}
	files := []companionSaveFile{}
	for _, f := range saveTimelineGameFiles(saveManagerRoots(), shoGameOf(g.game)) {
		files = append(files, companionSaveFile{Name: path.Base(f.rel), Kind: saveLedgerKindWord(f.kind), Size: f.size, Folder: f.root.Name})
	}
	moments, checkpoints := saveTimelineList(g.game.Path)
	list := []companionMoment{}
	for _, m := range append(checkpoints, moments...) {
		what := saveMomentTriggerWords(m.M.Trigger)
		if m.Checkpoint {
			what = "Checkpoint"
		}
		list = append(list, companionMoment{ID: filepath.Base(m.Dir), When: saveMomentWhen(nil, m), What: what + " · " + saveMomentKindsWords(m.M.Kinds),
			Size: m.M.TotalBytes, Checkpoint: m.Checkpoint, Name: m.M.Name})
	}
	writeCompanionJSON(w, map[string]interface{}{"title": g.Title, "files": files, "moments": list, "applies": shoApplies(g.game)})
}

// handleGamesSavesZip answers GET /api/games/saves.zip?id=: the game's save
// files as they are now, one folder per save folder they came from.
func (s *companionServer) handleGamesSavesZip(w http.ResponseWriter, r *http.Request) {
	g, ok := companionLibGame(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "that game is not in the library any more", http.StatusNotFound)
		return
	}
	files := saveTimelineGameFiles(saveManagerRoots(), shoGameOf(g.game))
	if len(files) == 0 {
		http.Error(w, "no saves of this game were found", http.StatusNotFound)
		return
	}
	name := saveRootSlug(g.Title)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-saves.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	for _, f := range files {
		src, err := os.Open(f.path)
		if err != nil {
			continue
		}
		hdr := &zip.FileHeader{Name: saveRootSlug(f.root.Name) + "/" + f.rel, Method: zip.Deflate}
		hdr.Modified = time.Unix(0, f.mtime)
		if dst, err := zw.CreateHeader(hdr); err == nil {
			_, _ = io.Copy(dst, io.LimitReader(src, saveManagerMaxFile))
		}
		_ = src.Close()
	}
	if dst, err := zw.Create("README.txt"); err == nil {
		_, _ = fmt.Fprintf(dst, "Saves of %s from JukaHub.\n\nEach folder is one of the handheld's save folders (RetroArch saves, states…).\nPut a file in the matching folder of the emulator on the other device, or\nsend it back with the Games page's Upload a save.\n", g.Title)
	}
	_ = zw.Close()
}

// companionSaveTarget decides where an uploaded save goes: over the game's
// file of that name, else beside the game's saves of its kind, else the
// first save folder of that kind. Pure apart from the files it is given.
func companionSaveTarget(name string, g shoGame, roots []saveRoot, have []saveTimelineFile) (string, error) {
	lower := strings.ToLower(name)
	if match, _ := shoNameKind(lower, g); !match {
		return "", fmt.Errorf("%s is not named like a save of this game (%s.srm, %s.state1 …)", name, g.stem, g.stem)
	}
	kind := saveLedgerKindOf(lower, g)
	for _, f := range have {
		if strings.EqualFold(path.Base(f.rel), name) {
			return f.path, nil
		}
	}
	for _, f := range have {
		if f.kind == kind {
			return filepath.Join(filepath.Dir(f.path), name), nil
		}
	}
	want := "saves"
	if kind == saveLedgerKindState || kind == saveLedgerKindAuto {
		want = "states"
	}
	for _, root := range roots {
		if strings.HasPrefix(root.Name, "RetroArch ") && strings.HasSuffix(root.Name, want) {
			return filepath.Join(root.Path, name), nil
		}
	}
	return "", errors.New("no save folder of that kind was found on the handheld — start the game once first")
}

// handleGamesSaveUpload answers POST /api/games/saves/upload?id=&name= with
// the file as the body.
func (s *companionServer) handleGamesSaveUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	g, ok := companionLibGame(q.Get("id"))
	if !ok {
		http.Error(w, "that game is not in the library any more", http.StatusNotFound)
		return
	}
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(q.Get("name")), `\`, "/"))
	if name == "" || name == "." || name == "/" || strings.HasPrefix(name, ".") {
		http.Error(w, "the file needs a name", http.StatusBadRequest)
		return
	}
	if saveTimelineGameRunning() {
		http.Error(w, "a game is running on the handheld — quit it first", http.StatusConflict)
		return
	}
	roots := saveManagerRoots()
	sg := shoGameOf(g.game)
	have := saveTimelineGameFiles(roots, sg)
	target, err := companionSaveTarget(name, sg, roots, have)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	inside := false
	for _, root := range roots {
		if isPathWithin(target, root.Path) && parentResolvesWithin(root.Path, target) {
			inside = true
			break
		}
	}
	if !inside {
		http.Error(w, "that save would land outside the save folders", http.StatusForbidden)
		return
	}
	// The saves as they are become a moment first, so an upload can be
	// taken back in the Time Machine.
	if _, err := saveTimelineCapture(g.game, roots, saveMomentBeforeUpload, "", false, false); err != nil &&
		!errors.Is(err, errSaveTimelineUnchanged) && !errors.Is(err, errSaveTimelineNoSaves) {
		http.Error(w, "nothing was changed — the saves as they are could not be kept first: "+err.Error(), http.StatusInsufficientStorage)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionSaveMax)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		http.Error(w, "could not make the save folder", http.StatusInternalServerError)
		return
	}
	tmp := filepath.Join(filepath.Dir(target), "."+name+".jukahub-upload.tmp")
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "could not write on the card", http.StatusInternalServerError)
		return
	}
	n, copyErr := io.Copy(out, r.Body)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n == 0 {
		_ = os.Remove(tmp)
		http.Error(w, "the upload did not arrive whole — nothing was changed", http.StatusBadRequest)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		http.Error(w, "could not put the save in place", http.StatusInternalServerError)
		return
	}
	syncDirectory(filepath.Dir(target))
	saveTimelineGen.Add(1)
	logInfo("companion", "phone sent a save of %s: %s (%d bytes)", g.Title, name, n)
	s.sink(companionEvent{Kind: companionEventUpload, Text: name})
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "folder": filepath.Base(filepath.Dir(target)), "size": n})
}

// handleGamesRestore answers POST /api/games/restore {"id", "moment"}.
func (s *companionServer) handleGamesRestore(w http.ResponseWriter, r *http.Request) {
	call, ok := readCompanionGameCall(w, r)
	if !ok {
		return
	}
	g, ok := companionLibGame(call.ID)
	if !ok {
		http.Error(w, "that game is not in the library any more", http.StatusNotFound)
		return
	}
	moments, checkpoints := saveTimelineList(g.game.Path)
	var pick saveMoment
	for _, m := range append(checkpoints, moments...) {
		if filepath.Base(m.Dir) == call.Moment {
			pick = m
			break
		}
	}
	if pick.Dir == "" {
		http.Error(w, "that moment is gone", http.StatusNotFound)
		return
	}
	if _, err := saveTimelineRestore(g.game, pick, saveManagerRoots()); err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, errSaveTimelineRunning) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.sink(companionEvent{Kind: companionEventUpload, Text: "the saves of " + g.Title + " from " + saveMomentWhen(nil, pick)})
	writeCompanionJSON(w, map[string]interface{}{"ok": true})
}

// companionGamesPage is the phone's Games page.
const companionGamesPage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>JukaHub · Games</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--bad:#ff6b6b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:calc(14px + env(safe-area-inset-top)) 16px calc(40px + env(safe-area-inset-bottom));max-width:640px;margin:0 auto}
h1{font-size:20px;margin:0 0 4px}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:18px 0 8px}
.note{color:var(--dim);font-size:14px;margin:0 0 10px}
input,select,textarea{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:16px system-ui,-apple-system,sans-serif}
textarea{resize:vertical;min-height:76px;margin-top:10px}
.bar{display:flex;gap:8px;margin:12px 0}.bar>*{flex:1}
.game{display:flex;gap:12px;align-items:center;background:var(--card);border:1px solid var(--line);border-radius:14px;padding:10px;margin:8px 0;cursor:pointer}
.game img,.game .ph{width:52px;height:52px;border-radius:8px;object-fit:cover;background:#1d2330;flex:none}
.game b{display:block}.game span{color:var(--dim);font-size:14px}
button{font:600 15px system-ui,-apple-system,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
button.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
.row{display:flex;gap:10px;margin-top:12px;flex-wrap:wrap}.row>*{flex:1 1 140px}
.item{display:flex;justify-content:space-between;gap:10px;align-items:center;border-top:1px solid var(--line);padding:10px 0}
.item small{color:var(--dim);display:block}
.msg{margin:10px 0;font-size:14px;color:var(--dim)}.msg:empty{display:none}.msg.bad{color:var(--bad)}
#detail{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin:12px 0}
a{color:#a8b5ff}[hidden]{display:none!important}
</style>
<h1>Games</h1>
<p class=note>The handheld's library. Tap a game to start it there, or to take its saves with you.</p>
<div class=msg id=msg aria-live=polite></div>
<div id=detail hidden>
  <h1 id=dTitle></h1>
  <p class=note id=dConsole></p>
  <div class=row><button id=dPlay>Play on the handheld</button><button class=ghost id=dBack>All games</button></div>
  <h2>Journal</h2>
  <div class=row><select id=jStatus aria-label=Status></select><select id=jRating aria-label=Rating>
    <option value=0>No rating</option><option value=1>★</option><option value=2>★★</option>
    <option value=3>★★★</option><option value=4>★★★★</option><option value=5>★★★★★</option>
  </select></div>
  <textarea id=jNote rows=3 placeholder="What did you think of it?"></textarea>
  <div class=row><button id=jSave>Save journal</button></div>
  <h2>Saves now</h2>
  <div id=dFiles class=note>Reading…</div>
  <div class=row><a id=dZip href="#"><button class=ghost type=button>Download saves (.zip)</button></a>
  <label class=ghost style="flex:1 1 140px"><input id=dUpload type=file hidden><button class=ghost type=button id=dUploadBtn>Upload a save</button></label></div>
  <p class=note>An upload replaces the game's file of the same name (or goes beside its saves of that kind); the saves as they were are kept as a Time Machine moment first.</p>
  <h2>Time Machine</h2>
  <div id=dMoments class=note>Reading…</div>
</div>
<div id=list>
  <div class=bar><input id=q type=search placeholder="Search games" autocomplete=off><select id=con aria-label=Console><option value="">All consoles</option></select></div>
  <div id=games class=note>Reading the library…</div>
</div>
<script>
const pin = new URL(location.href).searchParams.get('p') || '';
const H = (json) => { const h = {'X-Companion-Pin': pin}; if (json) h['Content-Type'] = 'application/json'; return h; };
const $ = (id) => document.getElementById(id);
let all = [], cur = null;
function say(t, bad) { const m = $('msg'); m.textContent = t || ''; m.className = bad ? 'msg bad' : 'msg'; }
function size(n) { return n >= 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB'; }
async function call(url, body) {
  const res = await fetch(url, body === undefined ? {headers: H(false), cache: 'no-store'} : {method: 'POST', headers: H(true), body: JSON.stringify(body)});
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}
function draw() {
  const q = $('q').value.trim().toLowerCase(), con = $('con').value, box = $('games');
  box.textContent = '';
  const shown = all.filter(g => (!con || g.console === con) && (!q || g.title.toLowerCase().includes(q))).slice(0, 400);
  if (!shown.length) { box.textContent = all.length ? 'No game matches.' : 'No games on the handheld yet.'; return; }
  for (const g of shown) {
    const row = document.createElement('div'); row.className = 'game';
    let pic;
    if (g.art) { pic = document.createElement('img'); pic.loading = 'lazy'; pic.alt = ''; pic.src = '/api/games/art?id=' + encodeURIComponent(g.id) + '&p=' + encodeURIComponent(pin); }
    else { pic = document.createElement('div'); pic.className = 'ph'; }
    const text = document.createElement('div'), b = document.createElement('b'), s = document.createElement('span');
    b.textContent = g.title; s.textContent = g.console; text.append(b, s); row.append(pic, text);
    row.onclick = () => open(g); box.append(row);
  }
}
async function load() {
  try {
    const d = await call('/api/games');
    all = d.games || [];
    const cons = [...new Set(all.map(g => g.console))].sort();
    const sel = $('con'), keep = sel.value; sel.length = 1;
    for (const c of cons) { const o = document.createElement('option'); o.value = o.textContent = c; sel.append(o); }
    sel.value = keep;
    if (!d.ready) say('The handheld is still looking through its card; pull to refresh in a moment.');
    draw();
  } catch (e) { say(e.message, true); }
}
async function open(g) {
  cur = g; say('');
  $('list').hidden = true; $('detail').hidden = false; window.scrollTo(0, 0);
  $('dTitle').textContent = g.title; $('dConsole').textContent = g.console;
  $('dZip').href = '/api/games/saves.zip?id=' + encodeURIComponent(g.id) + '&p=' + encodeURIComponent(pin);
  await journal(); await saves();
}
async function journal() {
  const g = cur; if (!g) return;
  try {
    const d = await call('/api/games/journal?id=' + encodeURIComponent(g.id));
    const sel = $('jStatus'); sel.textContent = '';
    const none = document.createElement('option'); none.value = ''; none.textContent = 'No status'; sel.append(none);
    for (const s of d.statuses) { const o = document.createElement('option'); o.value = o.textContent = s; sel.append(o); }
    sel.value = d.game.status || '';
    $('jRating').value = String(d.game.rating || 0);
    $('jNote').value = d.game.note || '';
  } catch (e) { say(e.message, true); }
}
async function saves() {
  const g = cur; $('dFiles').textContent = 'Reading…'; $('dMoments').textContent = 'Reading…';
  try {
    const d = await call('/api/games/saves?id=' + encodeURIComponent(g.id));
    const f = $('dFiles'); f.textContent = '';
    if (!d.files.length) f.textContent = d.applies ? 'No saves yet.' : 'This game keeps its saves elsewhere.';
    for (const x of d.files) { const it = document.createElement('div'); it.className = 'item'; it.textContent = x.name; const sm = document.createElement('small'); sm.textContent = x.kind + ' · ' + size(x.size) + ' · ' + x.folder; it.append(sm); f.append(it); }
    const m = $('dMoments'); m.textContent = '';
    if (!d.moments.length) m.textContent = 'No moments yet: they are kept while you play.';
    for (const x of d.moments) {
      const it = document.createElement('div'); it.className = 'item';
      const t = document.createElement('div'); t.textContent = (x.checkpoint && x.name ? '★ ' + x.name + ' · ' : '') + x.when;
      const sm = document.createElement('small'); sm.textContent = x.what + ' · ' + size(x.size); t.append(sm);
      const b = document.createElement('button'); b.className = 'ghost'; b.textContent = 'Restore';
      b.onclick = async () => {
        if (!confirm('Put the saves back as they were ' + x.when + '? The saves as they are now are kept as a moment first.')) return;
        try { await call('/api/games/restore', {id: g.id, moment: x.id}); say('Restored.'); saves(); } catch (e) { say(e.message, true); }
      };
      it.append(t, b); m.append(it);
    }
  } catch (e) { say(e.message, true); }
}
$('q').oninput = draw; $('con').onchange = draw;
$('dBack').onclick = () => { $('detail').hidden = true; $('list').hidden = false; cur = null; };
$('dPlay').onclick = async () => { try { const d = await call('/api/games/play', {id: cur.id}); say('Starting ' + d.title + ' on the handheld.'); } catch (e) { say(e.message, true); } };
$('jSave').onclick = async () => {
  if (!cur) return;
  try {
    const d = await call('/api/games/journal', {id: cur.id, status: $('jStatus').value, rating: Number($('jRating').value), note: $('jNote').value});
    say('Journal saved: ' + (d.game.status || 'no status') + (d.game.rating ? ', ' + d.game.rating + '/5' : '') + '.');
  } catch (e) { say(e.message, true); }
};
$('dUploadBtn').onclick = () => $('dUpload').click();
$('dUpload').onchange = async () => {
  const file = $('dUpload').files[0]; if (!file || !cur) return;
  say('Sending ' + file.name + '…');
  try {
    const res = await fetch('/api/games/saves/upload?id=' + encodeURIComponent(cur.id) + '&name=' + encodeURIComponent(file.name), {method: 'POST', headers: H(false), body: file});
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    const d = await res.json(); say(file.name + ' is in ' + d.folder + '.'); saves();
  } catch (e) { say(e.message, true); }
  $('dUpload').value = '';
};
load();
</script>
`
