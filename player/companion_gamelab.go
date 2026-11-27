package main

// companion_gamelab.go — Game Lab setups on the phone. Share writes a profile
// to data/game-lab/export and offers a QR that opens this page; a profile
// picked on the phone is checked by the same parser the Lab uses and written
// to data/game-lab/import, where the game's "Shared setups" list finds it.
//
// The page itself carries no data: the JSON calls behind it are PIN guarded
// like every other companion action, and the download link carries the PIN in
// the query only because a plain <a> click cannot set a header. Uploads are
// bounded by gameLabImportMax, must parse as a Game Lab profile, and are
// re-marshalled before they are stored, so a file is JSON and nothing else.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const companionGameLabMaxFiles = 100

type companionGameLabProfile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Title  string `json:"title,omitempty"`
	System string `json:"system,omitempty"`
}

type companionGameLabList struct {
	Exports  []companionGameLabProfile `json:"exports"`
	Imports  int                       `json:"imports"`
	MaxBytes int64                     `json:"maxBytes"`
}

func (s *companionServer) registerGameLabRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/gamelab", s.handleGameLabPage)
	mux.HandleFunc("/api/game-lab", s.guard(s.handleGameLab))
	mux.HandleFunc("/api/game-lab/download", s.guard(s.handleGameLabDownload))
}

// companionGameLabURL is the pairing link aimed at the Game Lab page: the
// same address and PIN, with the path changed and the query kept. A link that
// cannot be parsed is returned unchanged.
func companionGameLabURL(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return link
	}
	u.Path = "/gamelab"
	u.RawPath = ""
	return u.String()
}

func gameLabExportDir() string {
	dir := gameLabDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "export")
}

func gameLabImportDir() string {
	dir := gameLabDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "import")
}

func (s *companionServer) handleGameLabPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = io.WriteString(w, companionGameLabHTML)
}

func (s *companionServer) handleGameLab(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGameLabList(w)
	case http.MethodPost:
		s.handleGameLabUpload(w, r)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (s *companionServer) handleGameLabList(w http.ResponseWriter) {
	out := companionGameLabList{Exports: []companionGameLabProfile{}, MaxBytes: gameLabImportMax}
	if dir := gameLabExportDir(); dir != "" {
		items, _ := os.ReadDir(dir)
		for _, item := range items {
			if len(out.Exports) >= companionGameLabMaxFiles {
				break
			}
			if item.IsDir() || !strings.EqualFold(filepath.Ext(item.Name()), ".json") {
				continue
			}
			info, err := item.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > gameLabImportMax {
				continue
			}
			entry := companionGameLabProfile{Name: item.Name(), Size: info.Size()}
			if raw, err := os.ReadFile(filepath.Join(dir, item.Name())); err == nil {
				if p, err := gameLabParseProfile(raw); err == nil {
					entry.Title, entry.System = p.Game.Title, p.Game.System
				}
			}
			out.Exports = append(out.Exports, entry)
		}
		sort.Slice(out.Exports, func(i, j int) bool { return out.Exports[i].Name < out.Exports[j].Name })
	}
	if dir := gameLabImportDir(); dir != "" {
		if items, err := os.ReadDir(dir); err == nil {
			for _, item := range items {
				if !item.IsDir() && strings.EqualFold(filepath.Ext(item.Name()), ".json") {
					out.Imports++
				}
			}
		}
	}
	writeCompanionJSON(w, out)
}

func (s *companionServer) handleGameLabUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, gameLabImportMax+64<<10)
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "The profile is larger than 64 KB.", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Send the profile as a file.", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("profile")
	if err != nil {
		file, header, err = r.FormFile("file")
	}
	if err != nil {
		http.Error(w, "Choose a JSON profile to upload.", http.StatusBadRequest)
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, gameLabImportMax+1))
	if err != nil {
		http.Error(w, "The upload could not be read.", http.StatusBadRequest)
		return
	}
	if len(raw) > gameLabImportMax {
		http.Error(w, "The profile is larger than 64 KB.", http.StatusRequestEntityTooLarge)
		return
	}
	p, err := gameLabParseProfile(raw)
	if err != nil {
		http.Error(w, "That is not a Game Lab profile.", http.StatusBadRequest)
		return
	}
	dir := gameLabImportDir()
	if dir == "" {
		http.Error(w, "JukaHub has no data folder.", http.StatusServiceUnavailable)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "The profile could not be saved.", http.StatusInternalServerError)
		return
	}
	name := ""
	if header != nil {
		name = sanitizeUploadName(header.Filename)
	}
	if !strings.EqualFold(filepath.Ext(name), ".json") {
		name = gameLabExportName(p.Game.System, p.Game.Title)
	}
	name = gameLabUniqueName(dir, name)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		http.Error(w, "The profile could not be saved.", http.StatusInternalServerError)
		return
	}
	if err := AtomicWrite(filepath.Join(dir, name), append(data, '\n'), 0o644); err != nil {
		http.Error(w, "The profile could not be saved.", http.StatusInternalServerError)
		return
	}
	logInfo("gamelab", "phone imported shared setup %s", name)
	writeCompanionJSON(w, map[string]string{"name": name})
}

// gameLabUniqueName returns name, or "stem-2.json" and so on when an import
// of that name is already there. name is a single base name.
func gameLabUniqueName(dir, name string) string {
	name = filepath.Base(name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 1; ; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}

func (s *companionServer) handleGameLabDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("file")))
	if name == "" || name != r.URL.Query().Get("file") || !strings.EqualFold(filepath.Ext(name), ".json") {
		http.Error(w, "Choose a shared setup.", http.StatusBadRequest)
		return
	}
	dir := gameLabExportDir()
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > gameLabImportMax {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeUploadName(name)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

const companionGameLabHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<title>Game Lab · JukaHub</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--bad:#ff6b6b}
*{box-sizing:border-box}body{margin:0;padding:16px 16px 40px;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif;max-width:560px;margin:0 auto}
h1{font-size:22px;margin:0 0 6px}p{color:var(--dim);margin:0 0 16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin-bottom:14px}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
a{color:#a8b5ff}button{font:600 15px system-ui;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:12px 16px;min-height:44px}
input[type=file]{width:100%;color:var(--dim);margin:8px 0}
.row{display:flex;gap:10px;align-items:center;justify-content:space-between;padding:10px 0;border-top:1px solid var(--line)}
.row:first-of-type{border-top:0}.row b{display:block;font-size:15px}.row small{color:var(--dim)}
.msg{min-height:20px;color:var(--dim);margin-top:8px}.msg.bad{color:var(--bad)}
</style>
<h1>Game Lab</h1>
<p>Download a setup shared from JukaHub, or send one back. Profiles are JSON and at most 64 KB.</p>
<div class=card><h2>Shared from this device</h2><div id=list><p>Loading…</p></div></div>
<div class=card><h2>Send a setup</h2>
<form id=up><input id=file type=file name=profile accept="application/json,.json" required><button type=submit>Upload profile</button></form>
<div id=msg class=msg></div></div>
<script>
const pin=new URLSearchParams(location.search).get('p')||'';
const list=document.getElementById('list'),msg=document.getElementById('msg'),form=document.getElementById('up');
function show(t,bad){msg.textContent=t;msg.className='msg'+(bad?' bad':'')}
async function load(){
 if(!pin){list.innerHTML='<p>Open this page from the QR code in Game Lab.</p>';return}
 try{
  const r=await fetch('/api/game-lab',{headers:{'X-Companion-Pin':pin}});
  if(!r.ok)throw new Error('HTTP '+r.status);
  const d=await r.json();
  list.innerHTML='';
  if(!d.exports||!d.exports.length){list.innerHTML='<p>No setups shared yet. Choose Share in Game Lab first.</p>';return}
  d.exports.forEach(function(p){
   const row=document.createElement('div');row.className='row';
   const text=document.createElement('div');
   const b=document.createElement('b');b.textContent=p.title||p.name;
   const s=document.createElement('small');s.textContent=(p.system?p.system+' · ':'')+Math.ceil(p.size/1024)+' KB';
   text.append(b,s);
   const a=document.createElement('a');
   a.href='/api/game-lab/download?p='+encodeURIComponent(pin)+'&file='+encodeURIComponent(p.name);
   a.textContent='Download';a.setAttribute('download','');
   row.append(text,a);list.append(row);
  });
 }catch(e){list.innerHTML='<p>Could not read the setups.</p>';show('Could not read the setups: '+e.message,true)}
}
form.addEventListener('submit',async function(ev){
 ev.preventDefault();
 const f=document.getElementById('file').files[0];
 if(!f){show('Choose a JSON profile first.',true);return}
 const body=new FormData();body.append('profile',f);
 show('Uploading…',false);
 try{
  const r=await fetch('/api/game-lab',{method:'POST',headers:{'X-Companion-Pin':pin},body:body});
  const text=await r.text();
  if(!r.ok)throw new Error(text||('HTTP '+r.status));
  show('Uploaded. Open the game in Game Lab to try it.',false);form.reset();load();
 }catch(e){show('Upload failed: '+e.message,true)}
});
load();
</script>
</html>`
