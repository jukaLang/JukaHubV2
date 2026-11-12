package main

// companion_files.go — the Phone Companion's Files card: browse, download,
// upload, make folders, rename and delete on the handheld from a phone or a
// computer's browser on the same Wi-Fi, without taking the card out (the idea
// of Mosaik's "Transfer files" tool).
//
// Design notes:
//   - The card sees exactly what the FTP server shares (ftp_server.go): the
//     card's user folders (ftpShareNames) plus JukaHub's downloads, or the
//     "shared" folder next to the app on a computer. Every request opens the
//     shares afresh as os.Roots (openFTPShares) and resolves its path through
//     ftpShareSet.resolvePath, so neither ".." nor a symlink leaves a share,
//     and the same folders are read-only: Roms/Ports and the game profiles'
//     folders (ftpPath.protected). "/" itself and the shares themselves cannot
//     be changed either.
//   - Every route sits behind companionServer.guard: the pairing PIN in the
//     X-Companion-Pin header (fetch and XHR) or the "p" query parameter (the
//     download link, which a plain <a> click cannot give a header).
//   - Paths in requests and answers are the share-relative "virtual" paths
//     ("/Roms/GBA"), never the folders on the host. Error answers are fixed
//     sentences; the system's error text (which names folders) only goes to
//     the log.
//   - An upload is streamed from the multipart body onto the card under a
//     hidden temporary name in the target folder and renamed when complete,
//     so a broken upload never leaves half a file under the real name and a
//     video never sits in RAM or the handheld's small tmpfs. It replaces a
//     file of the same name only when the page asked to (after confirm()).
//   - Delete removes one file or one EMPTY folder; a folder with anything in
//     it is refused, so a mistap cannot wipe a game library.
//   - A downloaded file is always an attachment of type octet-stream with
//     nosniff and a sandbox CSP: an HTML file from the card must never run as
//     a page on the companion's origin, where its address carries the PIN.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// companionFilesMaxList bounds one folder's listing.
	companionFilesMaxList = 5000
	// companionFilesTempPrefix starts the hidden name an upload is written
	// under until it is complete; listings leave such files out.
	companionFilesTempPrefix = ".jukahub-upload-"
	// companionFilesMaxName is the longest name, in bytes, the card's file
	// systems take.
	companionFilesMaxName = 255
	// companionFilesStaleTemp is the age after which an upload's temporary
	// file (".part") is taken as left behind by a crash and removed.
	companionFilesStaleTemp = time.Hour
)

// companionFilesIsPart reports an upload's temporary ".part" file name.
func companionFilesIsPart(name string) bool {
	return strings.HasPrefix(name, companionFilesTempPrefix) && strings.HasSuffix(name, ".part")
}

// Fixed answers.
const (
	companionFilesReadOnlyMsg = "This folder is read-only from the phone: it holds programs JukaHub runs. Use SFTP to change it."
	companionFilesFixedMsg    = "The shared folders themselves cannot be changed. Open one first."
	companionFilesNotFoundMsg = "No such file or folder."
	companionFilesBadPathMsg  = "That path is outside the shared folders."
)

// companionFileShares lists the folders the Files card offers: the same ones
// ftpService.Start shares. A variable so the tests can point it at folders of
// their own.
var companionFileShares = func() []ftpShare {
	base, card := ftpShareBase()
	shares := ftpSharesFor(base, card)
	if dl := downloadsRoot(); dirExists(dl) {
		shares = ftpAddShare(shares, ftpShare{name: ftpDownloadsShareName, dir: dl})
	}
	return shares
}

// companionFileEntry is one row of a listing. Locked marks a row the page
// may not rename or delete (a share, or a read-only folder).
type companionFileEntry struct {
	Name   string `json:"name"`
	IsDir  bool   `json:"isDir"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	Locked bool   `json:"locked,omitempty"`
}

// registerFileRoutes adds the Files card's endpoints, all behind the PIN.
func (s *companionServer) registerFileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/files", s.guard(s.handleFilesList))
	mux.HandleFunc("/api/files/download", s.guard(s.handleFilesDownload))
	mux.HandleFunc("/api/files/upload", s.guard(s.handleFilesUpload))
	mux.HandleFunc("/api/files/mkdir", s.guard(s.handleFilesMkdir))
	mux.HandleFunc("/api/files/rename", s.guard(s.handleFilesRename))
	mux.HandleFunc("/api/files/delete", s.guard(s.handleFilesDelete))
}

// openCompanionFileShares opens this request's shares; the caller closes
// them. The game profiles' folders inside them are locked as FTP locks them.
func openCompanionFileShares(w http.ResponseWriter) (*ftpShareSet, bool) {
	set, err := openFTPShares(companionFileShares())
	if err != nil {
		http.Error(w, "Nothing to share: no user folders were found on the card.", http.StatusServiceUnavailable)
		return nil, false
	}
	set.locked = set.keepInside(ftpProfileDirs())
	return set, true
}

// companionFilesResolve maps a page path onto the shares, answering the
// request itself when it cannot.
func companionFilesResolve(w http.ResponseWriter, set *ftpShareSet, raw string) (ftpPath, bool) {
	p, err := set.resolvePath("/", raw)
	if err != nil {
		if errors.Is(err, errFTPNotFound) {
			http.Error(w, companionFilesNotFoundMsg, http.StatusNotFound)
		} else {
			http.Error(w, companionFilesBadPathMsg, http.StatusBadRequest)
		}
		return ftpPath{}, false
	}
	return p, true
}

// companionFilesAliasProtected catches a name that opens a protected folder
// without spelling it — a FAT short name like "PORTS~1" on the card — by
// asking whether the path's first folder is the same folder.
func companionFilesAliasProtected(p ftpPath) bool {
	if p.share == nil || p.rel == "." {
		return false
	}
	first, _, _ := strings.Cut(filepath.ToSlash(p.rel), "/")
	for _, d := range ftpProtectedDirs {
		if !strings.EqualFold(p.share.name, d.share) {
			continue
		}
		a, errA := p.share.root.Stat(first)
		b, errB := p.share.root.Stat(d.dir)
		if errA == nil && errB == nil && os.SameFile(a, b) {
			return true
		}
	}
	return false
}

// companionFilesWritable answers the request and reports false when p may
// not be created, changed or removed.
func companionFilesWritable(w http.ResponseWriter, p ftpPath) bool {
	if p.isRoot() || p.isShareTop() {
		http.Error(w, companionFilesFixedMsg, http.StatusForbidden)
		return false
	}
	if p.protected || companionFilesAliasProtected(p) {
		http.Error(w, companionFilesReadOnlyMsg, http.StatusForbidden)
		return false
	}
	return true
}

// companionFilesCheckName checks a name typed for a new folder or a rename,
// returning it trimmed, or a sentence for the page. A name is one component
// the card's FAT/exFAT can hold: no separators, no ".." or dot names, no
// control characters or :*?"<>|, no trailing dot or space (FAT drops them),
// and no leading dot (that would hide it on the handheld).
func companionFilesCheckName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", "Type a name first."
	case name == "." || name == "..":
		return "", "That name is not allowed."
	case !utf8.ValidString(name):
		return "", "That name is not valid text."
	case len(name) > companionFilesMaxName:
		return "", "That name is too long."
	case strings.ContainsAny(name, "/\\"):
		return "", "A name cannot contain / or \\."
	case strings.ContainsAny(name, `:*?"<>|`):
		return "", `A name cannot contain : * ? " < > or |.`
	case strings.HasPrefix(name, "."):
		return "", "A name cannot start with a dot."
	case strings.HasSuffix(name, "."):
		return "", "A name cannot end with a dot."
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", "A name cannot contain control characters."
		}
	}
	return name, ""
}

// companionFilesJSON decodes a small JSON body, answering when it cannot.
func companionFilesJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(v); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

// handleFilesList answers GET /api/files?path=/Share/dir with the folder's
// rows, folders first, each group by name.
func (s *companionServer) handleFilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	p, ok := companionFilesResolve(w, set, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	var entries []companionFileEntry
	truncated := false
	if p.isRoot() {
		for _, sh := range set.list {
			e := companionFileEntry{Name: sh.name, IsDir: true, Locked: true}
			if info, err := sh.root.Stat("."); err == nil {
				e.MTime = info.ModTime().Unix()
			}
			entries = append(entries, e)
		}
	} else {
		info, err := p.share.root.Stat(p.rel)
		if err != nil {
			http.Error(w, companionFilesNotFoundMsg, http.StatusNotFound)
			return
		}
		if !info.IsDir() {
			http.Error(w, "That is a file, not a folder.", http.StatusBadRequest)
			return
		}
		entries, truncated, err = companionFilesReadDir(set, p)
		if err != nil {
			logWarn("companion", "files: cannot list %s: %v", p.display(), err)
			http.Error(w, "Cannot open that folder.", http.StatusInternalServerError)
			return
		}
		sort.Slice(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			if a.IsDir != b.IsDir {
				return a.IsDir
			}
			la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
			if la != lb {
				return la < lb
			}
			return a.Name < b.Name
		})
	}
	if entries == nil {
		entries = []companionFileEntry{}
	}
	readOnly := p.isRoot() || p.protected || companionFilesAliasProtected(p)
	w.Header().Set("Cache-Control", "no-store")
	writeCompanionJSON(w, map[string]interface{}{
		"path":      p.virtual,
		"readOnly":  readOnly,
		"entries":   entries,
		"truncated": truncated,
		"maxUpload": companionMaxUpload,
	})
}

// companionFilesReadDir lists the folder p, at most companionFilesMaxList
// rows. A symlink is shown as what it points at when that is inside the
// share, and left out when it is not (os.Root refuses to follow it).
func companionFilesReadDir(set *ftpShareSet, p ftpPath) ([]companionFileEntry, bool, error) {
	f, err := p.share.root.Open(p.rel)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var out []companionFileEntry
	for {
		batch, err := f.ReadDir(256)
		for _, de := range batch {
			if len(out) >= companionFilesMaxList {
				return out, true, nil
			}
			name := de.Name()
			if strings.HasPrefix(name, companionFilesTempPrefix) {
				// A ".part" an hour old is a crashed upload's: best effort away.
				if companionFilesIsPart(name) {
					if info, ierr := de.Info(); ierr == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > companionFilesStaleTemp {
						_ = p.share.root.Remove(filepath.Join(p.rel, name))
					}
				}
				continue
			}
			if !ftpNameOK(name) {
				continue
			}
			rel := filepath.Join(p.rel, name)
			info, ierr := de.Info()
			if ierr != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if info, ierr = p.share.root.Stat(rel); ierr != nil {
					continue
				}
			}
			e := companionFileEntry{Name: name, IsDir: info.IsDir(), MTime: info.ModTime().Unix()}
			if !e.IsDir {
				e.Size = info.Size()
			}
			if child, cerr := set.resolvePath(p.virtual, name); cerr != nil || child.protected {
				e.Locked = true
			}
			out = append(out, e)
		}
		if err != nil && err != io.EOF {
			return out, false, err
		}
		if err == io.EOF || len(batch) == 0 {
			break
		}
	}
	return out, false, nil
}

// companionFilesDisposition is a Content-Disposition naming the file, safe
// for the header: control characters and quotes dropped, non-ASCII encoded
// the RFC 2231 way by mime.FormatMediaType.
func companionFilesDisposition(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if clean == "" {
		clean = "download"
	}
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": clean}); v != "" {
		return v
	}
	return `attachment; filename="download"`
}

// handleFilesDownload sends GET /api/files/download?path=… as an attachment.
// http.ServeContent answers Range requests, so a browser can resume.
func (s *companionServer) handleFilesDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	p, ok := companionFilesResolve(w, set, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if p.isRoot() || p.isShareTop() {
		http.Error(w, "Only files can be downloaded.", http.StatusBadRequest)
		return
	}
	f, err := p.share.root.Open(p.rel)
	if err != nil {
		http.Error(w, companionFilesNotFoundMsg, http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "Only files can be downloaded.", http.StatusBadRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", companionFilesDisposition(path.Base(p.virtual)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	logInfo("companion", "files: download %s (%d bytes)", p.display(), info.Size())
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// handleFilesUpload takes POST /api/files/upload?path=/Share/dir[&replace=1]
// with one multipart "file" part, streamed onto the card.
func (s *companionServer) handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength > companionMaxUpload {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionMaxUpload)
	q := r.URL.Query()
	replace := q.Get("replace") == "1"
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	dir, ok := companionFilesResolve(w, set, q.Get("path"))
	if !ok {
		return
	}
	if dir.isRoot() {
		http.Error(w, "Open one of the shared folders first: files go inside them.", http.StatusForbidden)
		return
	}
	if info, err := dir.share.root.Stat(dir.rel); err != nil || !info.IsDir() {
		http.Error(w, "The target folder does not exist.", http.StatusNotFound)
		return
	}
	// Said before the body is read; the target's own check below still runs.
	if dir.protected || companionFilesAliasProtected(dir) {
		http.Error(w, companionFilesReadOnlyMsg, http.StatusForbidden)
		return
	}
	// Streamed, never ParseMultipartForm: that spools big parts to the
	// handheld's RAM-backed tmpfs (see handleUpload).
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	var file *multipart.Part
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "upload too large or malformed", http.StatusRequestEntityTooLarge)
			return
		}
		if part.FormName() == "file" {
			file = part
			break
		}
		part.Close()
	}
	if file == nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	name := sanitizeUploadName(file.FileName())
	target, err := set.resolvePath(dir.virtual, name)
	if err != nil || target.share != dir.share {
		http.Error(w, "That file name cannot be used here.", http.StatusBadRequest)
		return
	}
	if !companionFilesWritable(w, target) {
		return
	}
	root := target.share.root
	if info, err := root.Lstat(target.rel); err == nil {
		if info.IsDir() {
			http.Error(w, "A folder named "+name+" is already here.", http.StatusConflict)
			return
		}
		if !replace {
			http.Error(w, name+" is already here.", http.StatusConflict)
			return
		}
	}
	tmpRel, out, err := companionFilesCreateTemp(root, dir.rel)
	if err != nil {
		logWarn("companion", "files: cannot write in %s: %v", dir.display(), err)
		http.Error(w, "Cannot write in this folder.", http.StatusInternalServerError)
		return
	}
	written, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = root.Remove(tmpRel)
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(copyErr, &tooBig):
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		case isDiskFullError(copyErr) || isDiskFullError(closeErr):
			http.Error(w, "The card is full.", http.StatusInsufficientStorage)
		default:
			logWarn("companion", "files: upload of %q to %s failed: %v %v", name, dir.display(), copyErr, closeErr)
			http.Error(w, "upload interrupted", http.StatusInternalServerError)
		}
		return
	}
	// A file of that name may have appeared while this one was arriving.
	if !replace {
		if _, err := root.Lstat(target.rel); err == nil {
			_ = root.Remove(tmpRel)
			http.Error(w, name+" is already here.", http.StatusConflict)
			return
		}
	}
	if err := root.Rename(tmpRel, target.rel); err != nil {
		// A system whose rename does not replace: move the old one aside,
		// never delete it, until the new one is in place.
		if replace && (errors.Is(err, os.ErrExist) || runtime.GOOS == "windows") {
			err = companionFilesReplace(root, dir.rel, tmpRel, target.rel)
		}
		if err != nil {
			_ = root.Remove(tmpRel)
			logWarn("companion", "files: cannot save %s: %v", target.display(), err)
			http.Error(w, "Cannot save the file.", http.StatusInternalServerError)
			return
		}
	}
	logInfo("companion", "files: uploaded %s (%d bytes)", target.display(), written)
	s.sink(companionEvent{Kind: companionEventUpload, Text: name})
	trophyEvent(tkFileTransfer)
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "name": name, "size": written})
}

// companionFilesCreateTemp creates a new hidden file in dir (relative to
// root) for an upload to arrive in.
func companionFilesCreateTemp(root *os.Root, dir string) (string, *os.File, error) {
	var lastErr error
	for i := 0; i < 5; i++ {
		rel, err := companionFilesTempName(dir, ".part")
		if err != nil {
			return "", nil, err
		}
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return rel, f, nil
		}
		lastErr = err
		if !os.IsExist(err) {
			break
		}
	}
	return "", nil, lastErr
}

// companionFilesTempName is a new random hidden name in dir ending in suffix.
func companionFilesTempName(dir, suffix string) (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return filepath.Join(dir, companionFilesTempPrefix+hex.EncodeToString(raw)+suffix), nil
}

// companionFilesReplace puts tmpRel in place of the existing targetRel where
// rename will not replace: the old file is moved aside to a hidden name
// first, removed once the new one is in place, and moved back if that fails,
// so a failure never loses both copies.
func companionFilesReplace(root *os.Root, dir, tmpRel, targetRel string) error {
	var aside string
	for i := 0; ; i++ {
		name, err := companionFilesTempName(dir, ".old")
		if err != nil {
			return err
		}
		if _, err := root.Lstat(name); err != nil {
			aside = name
			break
		}
		if i == 4 {
			return os.ErrExist
		}
	}
	if err := root.Rename(targetRel, aside); err != nil {
		return err
	}
	if err := root.Rename(tmpRel, targetRel); err != nil {
		if backErr := root.Rename(aside, targetRel); backErr != nil {
			logWarn("companion", "files: old copy of %s kept as %s: %v", targetRel, aside, backErr)
		}
		return err
	}
	if err := root.Remove(aside); err != nil {
		logWarn("companion", "files: cannot remove old copy %s: %v", aside, err)
	}
	return nil
}

// handleFilesMkdir takes POST /api/files/mkdir {"path": folder, "name": new}.
func (s *companionServer) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !companionFilesJSON(w, r, &body) {
		return
	}
	name, msg := companionFilesCheckName(body.Name)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	dir, ok := companionFilesResolve(w, set, body.Path)
	if !ok {
		return
	}
	if dir.isRoot() {
		http.Error(w, "Folders can only be made inside one of the shared folders.", http.StatusForbidden)
		return
	}
	target, err := set.resolvePath(dir.virtual, name)
	if err != nil || target.share != dir.share {
		http.Error(w, "That name is not allowed.", http.StatusBadRequest)
		return
	}
	if !companionFilesWritable(w, target) {
		return
	}
	if _, err := target.share.root.Lstat(target.rel); err == nil {
		http.Error(w, name+" is already here.", http.StatusConflict)
		return
	}
	if err := target.share.root.Mkdir(target.rel, 0o755); err != nil {
		logWarn("companion", "files: cannot make %s: %v", target.display(), err)
		http.Error(w, "Cannot create the folder.", http.StatusInternalServerError)
		return
	}
	logInfo("companion", "files: made folder %s", target.display())
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "name": name})
}

// handleFilesRename takes POST /api/files/rename {"path": item, "name": new};
// the item stays in its folder.
func (s *companionServer) handleFilesRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !companionFilesJSON(w, r, &body) {
		return
	}
	name, msg := companionFilesCheckName(body.Name)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	from, ok := companionFilesResolve(w, set, body.Path)
	if !ok || !companionFilesWritable(w, from) {
		return
	}
	root := from.share.root
	fromInfo, err := root.Lstat(from.rel)
	if err != nil {
		http.Error(w, companionFilesNotFoundMsg, http.StatusNotFound)
		return
	}
	to, err := set.resolvePath(path.Dir(from.virtual), name)
	if err != nil || to.share != from.share {
		http.Error(w, "That name is not allowed.", http.StatusBadRequest)
		return
	}
	if !companionFilesWritable(w, to) {
		return
	}
	if to.rel == from.rel {
		writeCompanionJSON(w, map[string]interface{}{"ok": true, "name": name})
		return
	}
	// Another item of that name is refused; the same item under another case
	// (a case-insensitive card finds itself) is a rename.
	if toInfo, err := root.Lstat(to.rel); err == nil && !os.SameFile(fromInfo, toInfo) {
		http.Error(w, name+" is already here.", http.StatusConflict)
		return
	}
	if err := root.Rename(from.rel, to.rel); err != nil {
		logWarn("companion", "files: cannot rename %s: %v", from.display(), err)
		http.Error(w, "Cannot rename it.", http.StatusInternalServerError)
		return
	}
	logInfo("companion", "files: renamed %s to %s", from.display(), name)
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "name": name})
}

// companionFilesDirEmpty reports whether the folder rel holds nothing but
// uploads' ".part" files, and lists those. When it cannot be read it says
// empty and the Remove that follows gives the answer.
func companionFilesDirEmpty(root *os.Root, rel string) (bool, []string) {
	f, err := root.Open(rel)
	if err != nil {
		return true, nil
	}
	defer f.Close()
	var parts []string
	for {
		names, err := f.Readdirnames(64)
		for _, name := range names {
			if !companionFilesIsPart(name) {
				return false, nil
			}
			parts = append(parts, name)
		}
		if err != nil || len(names) == 0 || len(parts) > companionFilesMaxList {
			return true, parts
		}
	}
}

// handleFilesDelete takes POST /api/files/delete {"path": item}: one file,
// or one empty folder. A link is removed itself, never what it points at.
func (s *companionServer) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !companionFilesJSON(w, r, &body) {
		return
	}
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	p, ok := companionFilesResolve(w, set, body.Path)
	if !ok || !companionFilesWritable(w, p) {
		return
	}
	root := p.share.root
	info, err := root.Lstat(p.rel)
	if err != nil {
		http.Error(w, companionFilesNotFoundMsg, http.StatusNotFound)
		return
	}
	if info.IsDir() {
		empty, parts := companionFilesDirEmpty(root, p.rel)
		if !empty {
			http.Error(w, "Only empty folders can be deleted. Delete what is inside first.", http.StatusConflict)
			return
		}
		// Left-behind uploads' ".part" files are not the user's: clear them.
		for _, name := range parts {
			_ = root.Remove(filepath.Join(p.rel, name))
		}
	}
	if err := root.Remove(p.rel); err != nil {
		logWarn("companion", "files: cannot delete %s: %v", p.display(), err)
		if info.IsDir() {
			http.Error(w, "Cannot delete the folder (is it empty?).", http.StatusConflict)
		} else {
			http.Error(w, "Cannot delete the file.", http.StatusInternalServerError)
		}
		return
	}
	logInfo("companion", "files: deleted %s", p.display())
	writeCompanionJSON(w, map[string]interface{}{"ok": true})
}
