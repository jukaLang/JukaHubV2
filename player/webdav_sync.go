package main

// webdav_sync.go — a WebDAV server as the "other device" of Save sync, the
// way gdrive_sync.go does it for Google Drive.
//
// Layout under the address the player gave:
//
//	JukaHub Saves/
//	  .jukahub-manifest.json   every file's SHA-256, size and time
//	  retroarch-saves/Zelda.srm
//	  ppsspp-savedata/ULUS10041/…
//
// A WebDAV server knows sizes, ETags and modification times, none of which is
// a checksum, so JukaHub keeps its own manifest beside the saves: for each
// file the SHA-256 of what was uploaded, its size, its own modification time
// (it travels; it is never compared) and the ETag and Last-Modified the server
// reported for that upload. A listed file whose size or ETag no longer match
// the manifest — changed in the server's web page, or uploaded by a device
// whose manifest write lost a race — is downloaded and hashed again (within a
// budget), so the rules never act on a stale checksum. Uploads carry If-Match
// (or If-None-Match for a new file), so a file that changed on the server
// since the comparison is refused rather than overwritten.
//
// The rules are Drive's: the wall clock never decides; with a base (per
// server and account) the side that still has it takes the other's change;
// otherwise both copies are kept, the server's as "name (from WebDAV).ext".

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// webdavPeerName labels WebDAV in comparisons and kept copies.
	webdavPeerName = "WebDAV"
	// webdavRehashFiles and webdavRehashBytes bound how much one comparison
	// downloads to learn checksums the manifest lacks; the rest waits for the
	// next one.
	webdavRehashFiles = 400
	webdavRehashBytes = 128 << 20
	// webdavMaxFolders bounds a folder-by-folder listing.
	webdavMaxFolders = 4000
	// webdavMaxDepth bounds how deep a folder-by-folder listing goes.
	webdavMaxDepth = 12
)

// ── The manifest on the server ──────────────────────────────────────────────

// webdavRecordEntry is one file in the manifest.
type webdavRecordEntry struct {
	SHA   string `json:"sha256"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime,omitempty"`
	ETag  string `json:"etag,omitempty"`
	LM    string `json:"lm,omitempty"`
}

// webdavRecord is .jukahub-manifest.json: file key → entry.
type webdavRecord struct {
	V     int                          `json:"v"`
	Files map[string]webdavRecordEntry `json:"files"`
}

// webdavParseRecord reads the manifest from the server (outside data):
// entries with a bad key, checksum or size are dropped. A manifest that is not
// JSON counts as empty (every file is hashed again and a good one written); one
// from a newer JukaHub is refused rather than overwritten.
func webdavParseRecord(data []byte) (webdavRecord, error) {
	out := webdavRecord{V: 1, Files: map[string]webdavRecordEntry{}}
	if len(strings.TrimSpace(string(data))) == 0 {
		return out, nil
	}
	var rec webdavRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		logWarn("webdav", "the manifest on the server is not readable; its checksums are learned again")
		return out, nil
	}
	if rec.V > 1 {
		return out, errors.New("the saves on the WebDAV server were written by a newer JukaHub — update this one")
	}
	for k, e := range rec.Files {
		if len(out.Files) >= webdavMaxItems {
			break
		}
		if !ssyncValidBaseKey(k) || !ssyncValidSHA(e.SHA) || e.Size < 0 || e.Size > saveManagerMaxFile || e.MTime < 0 {
			continue
		}
		e.SHA = strings.ToLower(e.SHA)
		e.ETag = webdavCleanETag(e.ETag)
		if len(e.LM) > 100 {
			e.LM = ""
		}
		out.Files[k] = e
	}
	return out, nil
}

// getRecord reads the manifest and its ETag; found is false when there is
// none yet.
func (c *webdavConn) getRecord(ctx context.Context) (rec webdavRecord, etag string, found bool, err error) {
	resp, err := c.get(ctx, c.top.String()+webdavRecordName)
	if webdavStatusIs(err, http.StatusNotFound) {
		return webdavRecord{V: 1, Files: map[string]webdavRecordEntry{}}, "", false, nil
	}
	if err != nil {
		return webdavRecord{}, "", false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, webdavListLimit+1))
	if err != nil {
		return webdavRecord{}, "", false, gdriveNetError(err)
	}
	if len(data) > webdavListLimit {
		return webdavRecord{}, "", false, errors.New("the JukaHub manifest on the WebDAV server is larger than one sync can read")
	}
	rec, err = webdavParseRecord(data)
	return rec, webdavCleanETag(resp.Header.Get("ETag")), true, err
}

// ── What the server holds ───────────────────────────────────────────────────

// webdavRemoteFile is one save on the server with a known checksum.
type webdavRemoteFile struct {
	Size  int64
	SHA   string
	MTime int64
	ETag  string
}

// webdavRemote is the server's side of one comparison: folders, files, the
// same as a manifest for the rules, and what this sync learned for the
// manifest on the server.
type webdavRemote struct {
	conn     *webdavConn
	dirs     map[string]bool
	files    map[string]webdavRemoteFile
	manifest ssyncManifest
	// read is the manifest as read; learned the entries to write back
	// (uploads, checksums learned); listed the file keys a whole listing saw.
	read    webdavRecord
	learned map[string]webdavRecordEntry
	listed  map[string]bool
	full    bool
	// uploaded is set once a file went up: its manifest entry is worth a
	// few seconds more after a stop (webdavRun).
	uploaded bool
}

// webdavTrusted reports whether the manifest's entry still describes the
// listed file: same size, and the same ETag (or else Last-Modified) when both
// sides have one. Size alone vouches only on a server that reports neither:
// an entry written without them (an upload whose ETag could not be learned)
// is checked again rather than trusted for a save of the same size.
func webdavTrusted(e webdavRecordEntry, it webdavItem) bool {
	if e.Size != it.Size {
		return false
	}
	switch {
	case e.ETag != "" && it.ETag != "":
		return webdavSameETag(e.ETag, it.ETag)
	case e.LM != "" && it.LastMod != "":
		return e.LM == it.LastMod
	}
	return it.ETag == "" && it.LastMod == ""
}

// webdavBuildRemote joins a listing with the manifest. Files the manifest
// cannot vouch for come back in need, to be hashed; with stem only that
// game's files are kept at all.
func webdavBuildRemote(c *webdavConn, items []webdavItem, rec webdavRecord, full bool, stem string) (*webdavRemote, []webdavItem) {
	r := &webdavRemote{
		conn:    c,
		dirs:    map[string]bool{},
		files:   map[string]webdavRemoteFile{},
		read:    rec,
		learned: map[string]webdavRecordEntry{},
		listed:  map[string]bool{},
		full:    full,
	}
	var need []webdavItem
	skipped := 0
	for _, it := range items {
		if it.Rel == "" {
			r.dirs[gdriveTopKey] = true
			continue
		}
		if it.Dir {
			if gdriveValidDirKey(it.Rel) {
				r.dirs[it.Rel] = true
			}
			continue
		}
		if !strings.Contains(it.Rel, "/") {
			// Beside the folders: the manifest itself, or something else.
			continue
		}
		if !ssyncValidBaseKey(it.Rel) || it.Size > saveManagerMaxFile {
			skipped++
			continue
		}
		_, rel, _ := strings.Cut(it.Rel, "/")
		if !full && !ssyncMatchesGame(rel, stem) {
			continue
		}
		r.listed[it.Rel] = true
		if e, ok := rec.Files[it.Rel]; ok && webdavTrusted(e, it) {
			r.files[it.Rel] = webdavRemoteFile{Size: it.Size, SHA: e.SHA, MTime: e.MTime, ETag: it.ETag}
			continue
		}
		need = append(need, it)
	}
	if skipped > 0 {
		logInfo("webdav", "%d files on the server left out (names this device cannot use)", skipped)
	}
	sort.Slice(need, func(a, b int) bool { return need[a].Rel < need[b].Rel })
	return r, need
}

// rehash downloads the files the manifest cannot vouch for and learns their
// checksums, within the budget. It returns how many were left for next time.
func (r *webdavRemote) rehash(ctx context.Context, need []webdavItem) int {
	left, files := 0, 0
	var total int64
	for _, it := range need {
		if ctx.Err() != nil || files >= webdavRehashFiles || total+it.Size > webdavRehashBytes {
			left++
			continue
		}
		files++
		total += it.Size
		sum, n, err := r.hashOne(ctx, it)
		if err != nil || n != it.Size {
			// Changed while it was read, or unreadable: left out this time.
			left++
			continue
		}
		e := webdavRecordEntry{SHA: sum, Size: it.Size, ETag: it.ETag, LM: it.LastMod}
		if old, ok := r.read.Files[it.Rel]; ok && old.SHA == sum {
			e.MTime = old.MTime
		}
		r.learned[it.Rel] = e
		r.files[it.Rel] = webdavRemoteFile{Size: it.Size, SHA: sum, MTime: e.MTime, ETag: it.ETag}
	}
	if left > 0 {
		logInfo("webdav", "%d files on the server not checked this time (hashed %d, %s)", left, files, humanBytes(total))
	}
	return left
}

// hashOne downloads one file and hashes it.
func (r *webdavRemote) hashOne(ctx context.Context, it webdavItem) (string, int64, error) {
	hctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(it.Size))
	defer cancel()
	resp, err := r.conn.get(hctx, r.conn.keyURL(it.Rel, false))
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	return webdavHashBody(resp.Body, it.Size)
}

// buildManifest describes the known files for the rules, with no times.
func (r *webdavRemote) buildManifest() {
	m := ssyncManifest{V: 1, Name: webdavPeerName}
	roots := map[string]bool{}
	for key, e := range r.files {
		root, rel, _ := strings.Cut(key, "/")
		roots[root] = true
		// No time: the rules must not order the server's copy by a clock.
		m.Files = append(m.Files, ssyncFile{Root: root, Path: rel, Size: e.Size, SHA: e.SHA})
	}
	for key := range r.dirs {
		if key != gdriveTopKey && !strings.Contains(key, "/") {
			roots[key] = true
		}
	}
	for root := range roots {
		m.Roots = append(m.Roots, ssyncRoot{Key: root, Name: root})
	}
	sort.Slice(m.Roots, func(a, b int) bool { return m.Roots[a].Key < m.Roots[b].Key })
	sort.Slice(m.Files, func(a, b int) bool { return m.Files[a].key() < m.Files[b].key() })
	r.manifest = m
}

// webdavListAll lists the whole top folder: one Depth infinity request where
// the server allows it, else folder by folder. found is false when the top
// folder is not there yet; noInfinity comes back true when the server refused
// infinity, so later syncs go folder by folder at once.
func webdavListAll(ctx context.Context, c *webdavConn, skipInfinity bool) (items []webdavItem, found, noInfinity bool, err error) {
	if !skipInfinity {
		items, err = c.propfind(ctx, c.top.String(), c.top, "infinity")
		switch {
		case err == nil && !webdavOneLevel(items):
			return items, true, false, nil
		case err == nil:
			// SabreDAV (ownCloud, Nextcloud unless its admin allowed
			// infinity) answers it as depth 1 without saying so: the folders
			// came back but nothing inside any of them. Taken as the whole
			// tree, every save would look gone and the manifest be pruned.
			noInfinity = true
			logInfo("webdav", "the server answered a whole-tree listing with one level only; listing folder by folder")
		case webdavStatusIs(err, http.StatusNotFound):
			return nil, false, false, nil
		case errors.Is(err, errWebdavRefused), ctx.Err() != nil:
			return nil, false, false, err
		default:
			var he *webdavHTTPError
			if !errors.As(err, &he) {
				return nil, false, false, err
			}
			// 403, 400, 507… — infinity is switched off on this server.
			noInfinity = true
			logInfo("webdav", "the server refused a whole-tree listing (HTTP %d); listing folder by folder", he.Status)
		}
	}
	items = nil
	queue := []string{""}
	seen := map[string]bool{"": true}
	for folders := 0; len(queue) > 0; folders++ {
		if folders >= webdavMaxFolders {
			return nil, true, noInfinity, errors.New("the JukaHub folder on the WebDAV server has more folders than one sync can read")
		}
		rel := queue[0]
		queue = queue[1:]
		target := c.top.String()
		if rel != "" {
			target = c.keyURL(rel, true)
		}
		got, err := c.propfind(ctx, target, c.top, "1")
		if webdavStatusIs(err, http.StatusNotFound) {
			if rel == "" {
				return nil, false, noInfinity, nil
			}
			continue
		}
		if err != nil {
			return nil, true, noInfinity, err
		}
		for _, it := range got {
			if it.Rel == rel {
				if rel == "" {
					items = append(items, it)
				}
				continue
			}
			items = append(items, it)
			if len(items) > webdavMaxItems {
				return nil, true, noInfinity, errSsyncTooMany
			}
			if it.Dir && !seen[it.Rel] && strings.Count(it.Rel, "/") < webdavMaxDepth {
				seen[it.Rel] = true
				queue = append(queue, it.Rel)
			}
		}
	}
	return items, true, noInfinity, nil
}

// webdavOneLevel reports whether a Depth: infinity answer stopped one level
// down: folders below the top, yet no entry inside any of them.
func webdavOneLevel(items []webdavItem) bool {
	folders := false
	for _, it := range items {
		if strings.Contains(it.Rel, "/") {
			return false
		}
		if it.Dir && it.Rel != "" {
			folders = true
		}
	}
	return folders
}

// webdavListGame lists only the folders the manifest says hold the game's
// saves: a launch has six seconds, not time for the whole tree. A save of
// this game that no manifest knows yet comes with the next whole sync.
func webdavListGame(ctx context.Context, c *webdavConn, rec webdavRecord, stem string) ([]webdavItem, error) {
	parents := map[string]bool{}
	for key := range rec.Files {
		root, rel, _ := strings.Cut(key, "/")
		if ssyncMatchesGame(rel, stem) {
			parents[gdriveDirKeyOf(root, rel)] = true
		}
	}
	keys := make([]string, 0, len(parents))
	for k := range parents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 16 {
		keys = keys[:16]
	}
	var items []webdavItem
	for _, dir := range keys {
		got, err := c.propfind(ctx, c.keyURL(dir, true), c.top, "1")
		if webdavStatusIs(err, http.StatusNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, got...)
	}
	return items, nil
}

// webdavListRemote reads the manifest and lists the server (everything, or
// only the game with stem), hashing what the manifest cannot vouch for.
func webdavListRemote(ctx context.Context, c *webdavConn, s webdavSettings, stem string) (*webdavRemote, error) {
	rec, _, recFound, err := c.getRecord(ctx)
	if err != nil {
		return nil, err
	}
	full := stem == ""
	var items []webdavItem
	if full {
		var noInf bool
		items, _, noInf, err = webdavListAll(ctx, c, s.NoInfinity)
		if noInf && !s.NoInfinity {
			id := c.identity
			if uerr := webdavSettingsUpdate(func(st *webdavSettings) {
				if webdavIdentity(*st) == id {
					st.NoInfinity = true
				}
			}); uerr != nil {
				logWarn("webdav", "could not record the listing mode: %v", uerr)
			}
		}
	} else {
		items, err = webdavListGame(ctx, c, rec, stem)
	}
	if err != nil {
		return nil, err
	}
	remote, need := webdavBuildRemote(c, items, rec, full, stem)
	if recFound {
		// The manifest lives in the top folder: no MKCOL for it (a game's
		// listing never shows the top folder itself).
		remote.dirs[gdriveTopKey] = true
	}
	remote.rehash(ctx, need)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	remote.buildManifest()
	return remote, nil
}

// writeRecord puts the manifest back with what this sync learned, merged
// into the copy on the server as it is now (another device may have written
// it meanwhile). If-Match (If-None-Match for a first one) makes a write that
// raced another one fail, and it is merged again.
func (r *webdavRemote) writeRecord(ctx context.Context) error {
	if len(r.learned) == 0 {
		return nil
	}
	if err := (&webdavIO{remote: r}).ensureDir(ctx, gdriveTopKey); err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		fresh, etag, found, err := r.conn.getRecord(ctx)
		if err != nil {
			return err
		}
		merged := webdavRecord{V: 1, Files: make(map[string]webdavRecordEntry, len(fresh.Files)+len(r.learned))}
		for k, e := range fresh.Files {
			// A whole listing saw every file: an entry for one that is gone,
			// which nobody changed since it was read, goes.
			if r.full && !r.listed[k] && r.read.Files[k] == e {
				continue
			}
			merged.Files[k] = e
		}
		for k, e := range r.learned {
			merged.Files[k] = e
		}
		data, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		hdr := http.Header{}
		switch {
		case !found:
			hdr.Set("If-None-Match", "*")
		case webdavStrongETag(etag):
			hdr.Set("If-Match", webdavIfMatch(etag))
		}
		_, err = r.conn.put(ctx, r.conn.top.String()+webdavRecordName, strings.NewReader(string(data)), int64(len(data)), hdr)
		if webdavStatusIs(err, http.StatusPreconditionFailed) {
			continue
		}
		return err
	}
	return errors.New("the manifest kept changing on the server — the next sync writes it")
}

// ── Base ────────────────────────────────────────────────────────────────────

// webdavBasePath is the base for one server and account (identity is
// already a hash).
func webdavBasePath(identity string) string {
	return filepath.Join(ssyncDataDir(), "base-webdav-"+identity+".json")
}

// webdavLoadBase is what this device and the server both held after their
// last sync (never nil).
func webdavLoadBase(identity string) map[string]string {
	out := map[string]string{}
	if identity == "" {
		return out
	}
	var file ssyncBaseFile
	if _, err := readJSONWithBackup(webdavBasePath(identity), &file); err != nil || file.Peer != "webdav:"+identity {
		return out
	}
	for k, v := range file.Files {
		if ssyncValidBaseKey(k) && ssyncValidSHA(v) {
			out[k] = strings.ToLower(v)
		}
	}
	return out
}

// webdavSaveBase records what both hold after a sync.
func webdavSaveBase(identity string, files map[string]string) error {
	if identity == "" {
		return errors.New("no WebDAV server")
	}
	data, err := json.Marshal(ssyncBaseFile{V: 1, Peer: "webdav:" + identity, At: time.Now().UTC(), Files: files})
	if err != nil {
		return err
	}
	return AtomicWrite(webdavBasePath(identity), data, 0o600)
}

// ── Comparing and syncing ───────────────────────────────────────────────────

// webdavCompare lists the server, describes this device (every save, or only
// the game with stem) and plans the sync with ssync's rules.
func webdavCompare(ctx context.Context, stem string) (ssyncComparison, *webdavRemote, error) {
	s := webdavSettingsGet()
	switch {
	case !s.connected():
		return ssyncComparison{}, nil, errWebdavNotSet
	case s.Refused:
		return ssyncComparison{}, nil, errWebdavRefused
	}
	conn, err := webdavConnFrom(s)
	if err != nil {
		return ssyncComparison{}, nil, err
	}
	remote, err := webdavListRemote(ctx, conn, s, stem)
	if err != nil {
		return ssyncComparison{}, nil, err
	}
	roots := saveManagerRoots()
	var local ssyncManifest
	if stem == "" {
		ssyncBuildMu.Lock()
		local, err = ssyncBuildManifest(ctx, roots)
		ssyncBuildMu.Unlock()
	} else {
		local, err = gdriveGameManifest(ctx, roots, stem)
	}
	if err != nil {
		return ssyncComparison{}, nil, err
	}
	// The server can hold any folder this device has.
	theirs := remote.manifest
	have := map[string]bool{}
	for _, r := range theirs.Roots {
		have[r.Key] = true
	}
	for i, key := range ssyncRootKeys(roots) {
		if !have[key] {
			theirs.Roots = append(theirs.Roots, ssyncRoot{Key: key, Name: roots[i].Name})
			have[key] = true
		}
	}
	plan := ssyncBuildPlan(local, theirs, webdavLoadBase(conn.identity), 0, local.Name, webdavPeerName)
	for i := range plan.Items {
		if e, ok := remote.files[plan.Items[i].Key]; ok && plan.Items[i].HasRemote {
			plan.Items[i].Remote.MTime = e.MTime
		}
	}
	// A file the server listed but whose checksum this comparison could not
	// learn (the download budget ran out, or it changed while read) waits for
	// the next one: sent now with If-None-Match it would only be refused, and
	// the toast would call it "changed on the server".
	plan = ssyncFilterPlan(plan, func(it ssyncItem) bool {
		return it.HasRemote || !remote.listed[it.Key]
	})
	return ssyncComparison{
		// gdrivePeerID marks a cloud copy (no clock) on the page; the name
		// tells WebDAV from Drive (gdriveStartSync).
		PeerID:   gdrivePeerID,
		PeerName: webdavPeerName,
		Local:    local,
		Remote:   theirs,
		Plan:     plan,
		Roots:    roots,
		At:       time.Now(),
	}, remote, nil
}

// webdavIO moves files to and from the server for ssyncExecute.
type webdavIO struct {
	remote *webdavRemote
}

// prepare has nothing to do: the only server copy a sync replaces is the one
// both sides had at the last sync (the base) — a conflict keeps both — and
// Nextcloud and ownCloud keep earlier versions of a file anyway.
func (w *webdavIO) prepare(ctx context.Context, keys []string) error { return nil }

func (w *webdavIO) pull(ctx context.Context, f ssyncFile, dst string) error {
	e, ok := w.remote.files[f.key()]
	if !ok {
		return errors.New("no longer on the WebDAV server")
	}
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(f.Size))
	defer cancel()
	resp, err := w.remote.conn.get(ctx, w.remote.conn.keyURL(f.key(), false))
	if webdavStatusIs(err, http.StatusNotFound) {
		return errors.New("no longer on the WebDAV server")
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	mtime := f.MTime
	if mtime <= 0 {
		mtime = e.MTime
	}
	if err := ssyncWriteFile(dst, resp.Body, f.Size, f.SHA, mtime); err != nil {
		return gdriveNetError(err)
	}
	return nil
}

func (w *webdavIO) push(ctx context.Context, f ssyncFile, src string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.Size() != f.Size {
		return errSsyncChanged
	}
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(f.Size))
	defer cancel()
	key := f.key()
	if err := w.ensureDir(ctx, gdriveDirKeyOf(f.Root, f.Path)); err != nil {
		return err
	}
	hdr := http.Header{}
	if e, ok := w.remote.files[key]; ok {
		if webdavStrongETag(e.ETag) {
			hdr.Set("If-Match", webdavIfMatch(e.ETag))
		}
	} else {
		// Not there at the comparison: if it is now, another device put it
		// there, and the next comparison decides.
		hdr.Set("If-None-Match", "*")
	}
	target := w.remote.conn.keyURL(key, false)
	h := sha256.New()
	etag, err := w.remote.conn.put(ctx, target, io.TeeReader(io.LimitReader(file, f.Size), h), f.Size, hdr)
	if err != nil {
		return err
	}
	sent := hex.EncodeToString(h.Sum(nil))
	w.remote.uploaded = true
	lm := ""
	if etag == "" {
		// No ETag with the answer: ask for the file's own, so the next
		// comparison can tell it was not changed since.
		if it, err := w.remote.conn.stat(ctx, target); err == nil {
			etag, lm = it.ETag, it.LastMod
		}
	}
	w.remote.learned[key] = webdavRecordEntry{SHA: sent, Size: f.Size, MTime: f.MTime, ETag: etag, LM: lm}
	w.remote.files[key] = webdavRemoteFile{Size: f.Size, SHA: sent, MTime: f.MTime, ETag: etag}
	w.remote.listed[key] = true
	if !strings.EqualFold(sent, f.SHA) {
		// The file changed while it was read: the manifest says what was
		// really sent, and this file waits for the next sync.
		return errSsyncChanged
	}
	return nil
}

// ensureDir makes the folder with key (and its parents) when the listing did
// not show it.
func (w *webdavIO) ensureDir(ctx context.Context, key string) error {
	if w.remote.dirs[key] {
		return nil
	}
	if key != gdriveTopKey {
		parent := gdriveTopKey
		if i := strings.LastIndexByte(key, '/'); i >= 0 {
			parent = key[:i]
		}
		if err := w.ensureDir(ctx, parent); err != nil {
			return err
		}
	}
	if err := w.remote.conn.mkcol(ctx, w.remote.conn.keyURL(key, true)); err != nil {
		return err
	}
	w.remote.dirs[key] = true
	return nil
}

// webdavRun carries out a comparison, writes the manifest on the server and
// records the new base. full prunes the base to the files either side still
// has.
func webdavRun(ctx context.Context, cmp ssyncComparison, remote *webdavRemote, full bool, progress func(done, total int, current string)) ssyncResult {
	identity := remote.conn.identity
	pio := &webdavIO{remote: remote}
	res, updates := ssyncExecute(ctx, cmp.Plan, pio, cmp.Roots, webdavPeerName, "before sync with WebDAV", progress)
	// The manifest even after a stop: what went up must be labelled, or the
	// next comparison downloads it again to learn its checksum. It gets what
	// is left of the caller's time — a launch waits its six seconds, not
	// thirty more — and after a stop three seconds only when files went up:
	// the game that stopped a background sync waits for gdriveSyncMu.
	wait := 30 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(dl))
	}
	if ctx.Err() != nil {
		wait = 0
	}
	if remote.uploaded {
		wait = max(wait, 3*time.Second)
	}
	if wait > 0 {
		wctx, cancel := context.WithTimeout(context.Background(), wait)
		if err := remote.writeRecord(wctx); err != nil {
			logWarn("webdav", "could not update the manifest on the server: %s", webdavErrorText(err))
		}
		cancel()
	}
	if now := webdavSettingsGet(); webdavIdentity(now) != identity {
		logInfo("webdav", "the WebDAV server changed during the sync; its record is not kept")
		return res
	}
	base := webdavLoadBase(identity)
	for k, v := range updates {
		base[k] = v
	}
	if full {
		present := make(map[string]bool, len(cmp.Local.Files)+len(cmp.Remote.Files))
		for _, f := range cmp.Local.Files {
			present[f.key()] = true
		}
		for _, f := range cmp.Remote.Files {
			present[f.key()] = true
		}
		for k := range updates {
			present[k] = true
		}
		for k := range base {
			if !present[k] {
				delete(base, k)
			}
		}
	}
	if err := webdavSaveBase(identity, base); err != nil {
		logWarn("webdav", "could not save the sync base: %v", err)
	}
	return res
}

// webdavSyncAll is a whole sync with the server. One cloud sync runs at a
// time (gdriveSyncMu covers Drive and WebDAV: both write the same saves).
func webdavSyncAll(ctx context.Context) (ssyncResult, error) {
	if !gdriveLockWithin(ctx) {
		return ssyncResult{Peer: webdavPeerName}, ctx.Err()
	}
	defer gdriveSyncMu.Unlock()
	cmp, remote, err := webdavCompare(ctx, "")
	if err != nil {
		return ssyncResult{Peer: webdavPeerName}, err
	}
	return webdavRun(ctx, cmp, remote, true, nil), nil
}

// webdavSyncGame brings one game's saves down (pull) or sends them up.
func webdavSyncGame(ctx context.Context, romPath string, pull bool) (ssyncResult, error) {
	stem := ssyncGameStem(romPath)
	if stem == "" {
		return ssyncResult{Peer: webdavPeerName}, nil
	}
	if !gdriveLockWithin(ctx) {
		return ssyncResult{Peer: webdavPeerName}, ctx.Err()
	}
	defer gdriveSyncMu.Unlock()
	cmp, remote, err := webdavCompare(ctx, stem)
	if err != nil {
		return ssyncResult{Peer: webdavPeerName}, err
	}
	cmp.Plan = ssyncFilterPlan(cmp.Plan, func(it ssyncItem) bool {
		if !ssyncMatchesGame(it.Path, stem) {
			return false
		}
		if pull {
			return it.Action == ssyncPull || it.Action == ssyncConflict
		}
		return it.Action == ssyncPush || it.Action == ssyncConflict
	})
	return webdavRun(ctx, cmp, remote, false, nil), nil
}

// webdavNoteResult keeps the last outcome for the page.
func webdavNoteResult(line string) {
	if !webdavSettingsGet().connected() {
		return
	}
	if err := webdavSettingsUpdate(func(s *webdavSettings) {
		s.LastResult, s.LastAt = line, time.Now()
	}); err != nil {
		logWarn("webdav", "could not record the last sync: %v", err)
	}
}
