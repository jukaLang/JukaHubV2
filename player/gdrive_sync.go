package main

// gdrive_sync.go — Google Drive as the "other device" of Save sync.
//
// ssync.go's rules decide every file; this file only describes Drive as a
// manifest and moves files to and from it (an ssyncPeerIO). The one
// difference from a paired device: the wall clock never decides. A TrimUI
// can boot with its clock in 1970 and sync before NTP sets it, so Drive's
// files go into the comparison with no modification time. Then:
//
//   - a file on one side only is copied to the other;
//   - identical files are left alone;
//   - with a base (what this device and Drive both held after their last
//     sync, recorded per Google account), the side that still has it takes
//     the other's change;
//   - otherwise — both changed, or no base yet and the two differ — both are
//     kept: this device's copy keeps the name on both sides and Drive's lands
//     beside it as "name (from Google Drive).ext". Nothing is deleted.
//
// The files' own modification times still travel (jhmtime) so a pulled save
// keeps the time it was written; they are only never compared.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// gdrivePeerID marks a comparison with Drive on the Save sync page.
	gdrivePeerID   = "gdrive"
	gdrivePeerName = "Google Drive"
)

// gdriveSyncMu lets one Drive sync run at a time (the page's, a game's, the
// background one).
var gdriveSyncMu sync.Mutex

// gdriveLockWithin takes gdriveSyncMu, giving up when ctx ends.
func gdriveLockWithin(ctx context.Context) bool {
	for {
		if gdriveSyncMu.TryLock() {
			return true
		}
		if gdriveSleep(ctx, 100*time.Millisecond) != nil {
			return false
		}
	}
}

// gdriveEntry is one save file on Drive.
type gdriveEntry struct {
	ID     string
	Size   int64
	SHA    string
	MTime  int64
	Server time.Time
}

// gdriveRemote is what Drive holds: folder key → folder id, file key →
// entry, and the same as a manifest for the rules.
type gdriveRemote struct {
	dirs     map[string]string
	files    map[string]gdriveEntry
	manifest ssyncManifest
	// account is the Google account the list came from (its base).
	account string
}

// gdriveBuildRemote turns Drive's folder and file lists into a manifest
// (outside data: every name and label is checked like a paired device's).
// Two folders with one key (two devices made it at once) both count; the
// oldest is the one new files go into. Two files with one key: the one Drive
// changed last wins.
func gdriveBuildRemote(dirs, files []gdriveFile) gdriveRemote {
	r := gdriveRemote{dirs: map[string]string{}, files: map[string]gdriveEntry{}}
	keyOfID := map[string]string{}
	created := map[string]time.Time{}
	for _, d := range dirs {
		key := d.AppProperties["jhdir"]
		if d.ID == "" || d.MimeType != gdriveFolderMime || d.AppProperties["jhkind"] != "dir" || !gdriveValidDirKey(key) {
			continue
		}
		keyOfID[d.ID] = key
		at, _ := time.Parse(time.RFC3339, d.CreatedTime)
		if old, ok := r.dirs[key]; !ok || at.Before(created[key]) || (at.Equal(created[key]) && d.ID < old) {
			r.dirs[key] = d.ID
			created[key] = at
		}
	}
	m := ssyncManifest{V: 1, Name: gdrivePeerName}
	roots := map[string]bool{}
	for _, f := range files {
		if f.ID == "" || f.MimeType == gdriveFolderMime || f.AppProperties["jhkind"] != "save" || len(f.Parents) == 0 {
			continue
		}
		dirKey, ok := keyOfID[f.Parents[0]]
		if !ok || dirKey == gdriveTopKey {
			continue
		}
		root, sub, _ := strings.Cut(dirKey, "/")
		rel := f.Name
		if sub != "" {
			rel = sub + "/" + f.Name
		}
		if _, ok := ssyncCleanRel(rel); !ok || !ssyncValidKey(root) {
			m.Skipped++
			continue
		}
		sha := strings.ToLower(f.SHA256)
		if !ssyncValidSHA(sha) {
			sha = strings.ToLower(f.AppProperties["jhsha"])
		}
		size, err := strconv.ParseInt(f.Size, 10, 64)
		if !ssyncValidSHA(sha) || err != nil || size < 0 || size > saveManagerMaxFile {
			m.Skipped++
			continue
		}
		mtime, _ := strconv.ParseInt(f.AppProperties["jhmtime"], 10, 64)
		if mtime < 0 {
			mtime = 0
		}
		server, _ := time.Parse(time.RFC3339, f.ModifiedTime)
		key := root + "/" + rel
		if old, ok := r.files[key]; ok && !server.After(old.Server) {
			continue
		}
		r.files[key] = gdriveEntry{ID: f.ID, Size: size, SHA: sha, MTime: mtime, Server: server}
		roots[root] = true
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
	for key, e := range r.files {
		root, rel, _ := strings.Cut(key, "/")
		// No time: the rules must not order Drive's copy by a clock.
		m.Files = append(m.Files, ssyncFile{Root: root, Path: rel, Size: e.Size, SHA: e.SHA})
	}
	sort.Slice(m.Files, func(a, b int) bool { return m.Files[a].key() < m.Files[b].key() })
	r.manifest = m
	return r
}

// gdriveValidDirKey accepts "." or "<folder key>[/<sub>/<sub>]".
func gdriveValidDirKey(key string) bool {
	if key == gdriveTopKey {
		return true
	}
	root, sub, _ := strings.Cut(key, "/")
	if !ssyncValidKey(root) {
		return false
	}
	if sub == "" {
		return true
	}
	// The sub-path is checked as a folder path (a file name inside it makes
	// it a path isSaveFile can judge).
	_, ok := ssyncCleanRel(sub + "/x.srm")
	return ok
}

// gdriveListRemote reads the whole JukaHub tree on Drive (two paged lists).
func gdriveListRemote(ctx context.Context) (*gdriveRemote, error) {
	dirs, err := gdriveListAll(ctx, "mimeType = '"+gdriveFolderMime+"' and appProperties has { key='jhkind' and value='dir' } and trashed = false")
	if err != nil {
		return nil, err
	}
	files, err := gdriveListAll(ctx, "appProperties has { key='jhkind' and value='save' } and trashed = false")
	if err != nil {
		return nil, err
	}
	r := gdriveBuildRemote(dirs, files)
	if r.manifest.Skipped > 0 {
		logInfo("gdrive", "%d files on Drive left out (names or labels this device cannot use)", r.manifest.Skipped)
	}
	return &r, nil
}

// ── Base ────────────────────────────────────────────────────────────────────

// gdriveBasePath is the base for one Google account (its permission id,
// hashed so the file name says nothing about the account).
func gdriveBasePath(account string) string {
	sum := sha256.Sum256([]byte("gdrive:" + account))
	return filepath.Join(ssyncDataDir(), "base-gdrive-"+hex.EncodeToString(sum[:8])+".json")
}

// gdriveLoadBase is what this device and Drive both held after their last
// sync (never nil).
func gdriveLoadBase(account string) map[string]string {
	out := map[string]string{}
	if account == "" {
		return out
	}
	var file ssyncBaseFile
	if _, err := readJSONWithBackup(gdriveBasePath(account), &file); err != nil || file.Peer != "gdrive:"+account {
		return out
	}
	for k, v := range file.Files {
		if ssyncValidBaseKey(k) && ssyncValidSHA(v) {
			out[k] = strings.ToLower(v)
		}
	}
	return out
}

// gdriveSaveBase records what both hold after a sync.
func gdriveSaveBase(account string, files map[string]string) error {
	if account == "" {
		return errors.New("no Google account")
	}
	data, err := json.Marshal(ssyncBaseFile{V: 1, Peer: "gdrive:" + account, At: time.Now().UTC(), Files: files})
	if err != nil {
		return err
	}
	return AtomicWrite(gdriveBasePath(account), data, 0o600)
}

// ── Local side ──────────────────────────────────────────────────────────────

// gdriveGameManifest describes only the save files of the game with stem:
// the quick version of ssyncBuildManifest for a launch, which must not hash a
// whole card. Hashes come from the shared cache when the file is unchanged.
func gdriveGameManifest(ctx context.Context, roots []saveRoot, stem string) (ssyncManifest, error) {
	keys := ssyncRootKeys(roots)
	m := ssyncManifest{V: 1, Now: time.Now().Unix(), Name: peerDeviceName()}
	cache := ssyncHashSnapshot()
	for i, root := range roots {
		if !dirExists(root.Path) {
			continue
		}
		key := keys[i]
		m.Roots = append(m.Roots, ssyncRoot{Key: key, Name: root.Name})
		rootPath := root.Path
		err := filepath.WalkDir(rootPath, func(p string, d os.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// A save that cannot be read must stop the sync, as in
			// ssyncBuildManifest: left out, it would look absent here and
			// Drive's copy would replace it with no snapshot taken first.
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() || !isSaveFile(p) {
				return nil
			}
			rel, err := filepath.Rel(rootPath, p)
			if err != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if !ssyncMatchesGame(rel, stem) {
				return nil
			}
			if _, ok := ssyncCleanRel(rel); !ok {
				m.Skipped++
				return nil
			}
			info, err := d.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.Size() > saveManagerMaxFile {
				m.Skipped++
				return nil
			}
			sum := ""
			if e, ok := cache[p]; ok && e.Size == info.Size() && e.MTime == info.ModTime().UnixNano() && ssyncValidSHA(e.SHA) {
				sum = e.SHA
			} else {
				s, err := SHA256File(p)
				if err != nil {
					if os.IsNotExist(err) {
						return nil
					}
					return err
				}
				sum = s
				ssyncHashNote(p, sum)
			}
			m.Files = append(m.Files, ssyncFile{Root: key, Path: rel, Size: info.Size(), MTime: info.ModTime().Unix(), SHA: sum})
			return nil
		})
		if err != nil {
			return ssyncManifest{}, err
		}
	}
	sort.Slice(m.Files, func(a, b int) bool { return m.Files[a].key() < m.Files[b].key() })
	return m, nil
}

// ── Comparing and syncing ───────────────────────────────────────────────────

// gdriveCompare lists Drive, describes this device (every save, or only the
// game with stem) and plans the sync with ssync's rules.
func gdriveCompare(ctx context.Context, stem string) (ssyncComparison, *gdriveRemote, error) {
	s := gdriveSettingsGet()
	switch {
	case !s.signedIn():
		return ssyncComparison{}, nil, errGdriveSignedOut
	case s.Expired:
		return ssyncComparison{}, nil, errGdriveExpired
	}
	remote, err := gdriveListRemote(ctx)
	if err != nil {
		return ssyncComparison{}, nil, err
	}
	remote.account = s.Account
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
	// Drive can hold any folder this device has: every local folder counts as
	// one there too, so a save only here is sent.
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
	plan := ssyncBuildPlan(local, theirs, gdriveLoadBase(s.Account), 0, local.Name, gdrivePeerName)
	// The page and the copies want Drive's real times back (never compared).
	for i := range plan.Items {
		if e, ok := remote.files[plan.Items[i].Key]; ok && plan.Items[i].HasRemote {
			plan.Items[i].Remote.MTime = e.MTime
		}
	}
	return ssyncComparison{
		PeerID:   gdrivePeerID,
		PeerName: gdrivePeerName,
		Local:    local,
		Remote:   theirs,
		Plan:     plan,
		Roots:    roots,
		At:       time.Now(),
	}, remote, nil
}

// gdriveIO moves files to and from Drive for ssyncExecute.
type gdriveIO struct {
	remote *gdriveRemote
}

// prepare has nothing to do: a file on Drive is updated in place, and Drive
// keeps the version it replaced in the file's version history.
func (g *gdriveIO) prepare(ctx context.Context, keys []string) error { return nil }

func (g *gdriveIO) pull(ctx context.Context, f ssyncFile, dst string) error {
	e, ok := g.remote.files[f.key()]
	if !ok {
		return errors.New("no longer on Google Drive")
	}
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(f.Size))
	defer cancel()
	resp, err := gdriveDownload(ctx, e.ID)
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

func (g *gdriveIO) push(ctx context.Context, f ssyncFile, src string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.Size() != f.Size {
		return errSsyncChanged
	}
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(f.Size))
	defer cancel()
	key := f.key()
	meta := map[string]any{"appProperties": gdriveSaveProps(f.SHA, f.MTime)}
	method, target := http.MethodPatch, ""
	if e, ok := g.remote.files[key]; ok {
		target = gdriveUploadAPI + "/files/" + url.PathEscape(e.ID)
	} else {
		dirID, err := g.ensureDir(ctx, gdriveDirKeyOf(f.Root, f.Path))
		if err != nil {
			return err
		}
		method, target = http.MethodPost, gdriveUploadAPI+"/files"
		meta["name"] = filepath.Base(filepath.FromSlash(f.Path))
		meta["parents"] = []string{dirID}
	}
	var got gdriveFile
	if f.Size <= gdriveMultipartMax {
		got, err = gdriveUploadMultipart(ctx, method, target, meta, src, f)
	} else {
		got, err = gdriveUploadResumable(ctx, method, target, meta, src, f)
	}
	if got.ID != "" {
		g.remote.files[key] = gdriveEntry{ID: got.ID, Size: f.Size, SHA: strings.ToLower(f.SHA), MTime: f.MTime, Server: time.Now()}
	}
	return err
}

// ensureDir finds or makes the folder with jhdir key (and its parents).
func (g *gdriveIO) ensureDir(ctx context.Context, key string) (string, error) {
	if id, ok := g.remote.dirs[key]; ok {
		return id, nil
	}
	parentID, name := "root", gdriveTopFolder
	if key != gdriveTopKey {
		parentKey := gdriveTopKey
		name = key
		if i := strings.LastIndexByte(key, '/'); i >= 0 {
			parentKey, name = key[:i], key[i+1:]
		}
		id, err := g.ensureDir(ctx, parentKey)
		if err != nil {
			return "", err
		}
		parentID = id
	}
	id, err := gdriveCreateFolder(ctx, name, parentID, key)
	if err != nil {
		return "", err
	}
	g.remote.dirs[key] = id
	return id, nil
}

// gdriveRun carries out a comparison and records the new base. full prunes
// the base to the files either side still has (only a whole-card comparison
// sees them all).
func gdriveRun(ctx context.Context, cmp ssyncComparison, remote *gdriveRemote, full bool, progress func(done, total int, current string)) ssyncResult {
	// The base of the account that was compared: a sign-out or another
	// account signed in meanwhile must not receive it.
	account := remote.account
	pio := &gdriveIO{remote: remote}
	res, updates := ssyncExecute(ctx, cmp.Plan, pio, cmp.Roots, gdrivePeerName, "before sync with Google Drive", progress)
	if now := gdriveSettingsGet(); !now.signedIn() || now.Account != account {
		logInfo("gdrive", "the Google account changed during the sync; its record is not kept")
		return res
	}
	base := gdriveLoadBase(account)
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
	if err := gdriveSaveBase(account, base); err != nil {
		logWarn("gdrive", "could not save the sync base: %v", err)
	}
	return res
}

// gdriveSyncAll is a whole sync with Drive (the page's Sync now uses the
// comparison it showed instead).
func gdriveSyncAll(ctx context.Context) (ssyncResult, error) {
	if !gdriveLockWithin(ctx) {
		return ssyncResult{Peer: gdrivePeerName}, ctx.Err()
	}
	defer gdriveSyncMu.Unlock()
	cmp, remote, err := gdriveCompare(ctx, "")
	if err != nil {
		return ssyncResult{Peer: gdrivePeerName}, err
	}
	return gdriveRun(ctx, cmp, remote, true, nil), nil
}

// gdriveSyncGame brings one game's saves down (pull) or sends them up.
func gdriveSyncGame(ctx context.Context, romPath string, pull bool) (ssyncResult, error) {
	stem := ssyncGameStem(romPath)
	if stem == "" {
		return ssyncResult{Peer: gdrivePeerName}, nil
	}
	if !gdriveLockWithin(ctx) {
		return ssyncResult{Peer: gdrivePeerName}, ctx.Err()
	}
	defer gdriveSyncMu.Unlock()
	cmp, remote, err := gdriveCompare(ctx, stem)
	if err != nil {
		return ssyncResult{Peer: gdrivePeerName}, err
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
	return gdriveRun(ctx, cmp, remote, false, nil), nil
}

// gdriveNoteResult keeps the last outcome for the page.
func gdriveNoteResult(line string) {
	if !gdriveSettingsGet().signedIn() {
		// Signed out while the sync ran: nothing is written back.
		return
	}
	if err := gdriveSettingsUpdate(func(s *gdriveSettings) {
		s.LastResult, s.LastAt = line, time.Now()
	}); err != nil {
		logWarn("gdrive", "could not record the last sync: %v", err)
	}
}
