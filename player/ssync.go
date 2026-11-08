package main

// ssync.go — Save sync: the saves of two paired devices made the same, with no
// cloud in between.
//
// Each device describes its save folders (savemanager.go's discovery) as a
// manifest: every file with its size, modification time and SHA-256, under a
// folder key both devices agree on ("retroarch-saves", "ppsspp-savedata"). The
// device that starts a sync fetches the other's manifest, compares, and then
// pulls and pushes single files over the paired HTTPS connection. The rules:
//
//   - a file on one side only is copied to the other;
//   - identical files are left alone;
//   - when the last sync between these two devices recorded the version both
//     had (the "base"), the side that still has it takes the other's change;
//   - when both changed since then — a true conflict — both copies are kept:
//     the newer one under the original name on both devices, the other as
//     "name (from <device>).ext" beside it;
//   - with no base yet, the newer file wins; two edits too close to order, or
//     a clock that is clearly wrong, count as a conflict and both are kept;
//   - nothing is ever deleted.
//
// Before a sync overwrites anything, each device takes a Save Manager snapshot
// ("before sync with …"), so a sync can be undone from Save Manager. Every file
// lands through a temp file, is checked against its SHA-256, gets the original
// modification time and is renamed into place, so a dropped connection or a
// full card never leaves half a save behind.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
	// ssyncSceneName is the Save sync page.
	ssyncSceneName = "SaveSync"
	// ssyncMaxFiles bounds a manifest: twenty thousand save files is far past a
	// real card, and a list that long would not fit the request limits.
	ssyncMaxFiles = 20000
	// ssyncClockSlack is how close two modification times may be (seconds)
	// before they cannot be ordered: FAT keeps 2-second times.
	ssyncClockSlack = 2
	// ssyncMinTime is the earliest believable modification time
	// (2020-01-01): a handheld whose clock reset cannot say which copy is newer.
	ssyncMinTime = 1577836800
	// ssyncManifestLimit bounds a manifest received from another device.
	ssyncManifestLimit = 16 << 20
)

var (
	errSsyncTooMany  = errors.New("there are more save files than one sync can carry")
	errSsyncTooBig   = errors.New("the save folders are larger than one sync can carry")
	errSsyncChecksum = errors.New("the copy did not match its checksum")
	errSsyncChanged  = errors.New("the file changed while it was being copied — sync again")
	errSsyncBadName  = errors.New("that file name is not allowed in a save folder")
)

// ssyncDirOverride is a test seam for the data folder.
var ssyncDirOverride string

// ssyncDataDir keeps the hash cache and the per-device sync bases.
func ssyncDataDir() string {
	if ssyncDirOverride != "" {
		return ssyncDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "ssync")
	}
	return filepath.Join("data", "ssync")
}

// ssyncFile is one save file as a device describes it.
type ssyncFile struct {
	Root  string `json:"root"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	SHA   string `json:"sha256"`
}

// key is the file's name in both manifests: folder key and relative path.
func (f ssyncFile) key() string { return f.Root + "/" + f.Path }

// ssyncRoot is one save folder in a manifest.
type ssyncRoot struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// ssyncManifest is a device's saves. Now is its clock when it was built, so
// the other device can correct for a clock that is off.
type ssyncManifest struct {
	V       int         `json:"v"`
	Device  string      `json:"device"`
	Name    string      `json:"name"`
	Now     int64       `json:"now"`
	Roots   []ssyncRoot `json:"roots"`
	Files   []ssyncFile `json:"files"`
	Skipped int         `json:"skipped,omitempty"`
}

// ── Folder keys ───────────────────────────────────────────────────────────

// ssyncRootKeys names each save folder the way both devices can agree on: the
// folder's label as a slug ("retroarch-saves"), with "-2" for a second folder
// of the same label, in the stable order saveRootsFor returns.
func ssyncRootKeys(roots []saveRoot) []string {
	keys := make([]string, len(roots))
	used := map[string]int{}
	for i, root := range roots {
		slug := saveRootSlug(root.Name)
		used[slug]++
		if n := used[slug]; n > 1 {
			slug = slug + "-" + strconv.Itoa(n)
		}
		keys[i] = slug
	}
	return keys
}

// ssyncRootMap is folder key → folder on this device.
func ssyncRootMap(roots []saveRoot) map[string]string {
	keys := ssyncRootKeys(roots)
	out := make(map[string]string, len(roots))
	for i, root := range roots {
		out[keys[i]] = root.Path
	}
	return out
}

// ssyncValidKey reports whether s is a folder key (saveRootSlug's alphabet).
func ssyncValidKey(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// ssyncValidSHA reports whether s is a hex SHA-256.
func ssyncValidSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ssyncCleanRel accepts a relative save path from another device only when it
// is already clean, stays inside its folder and can exist on FAT32 and on
// Windows: no "..", no drive letters, no backslashes, no characters those
// file systems refuse, and it must be a save file at all.
func ssyncCleanRel(p string) (string, bool) {
	if p == "" || len(p) > 512 || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", false
	}
	if path.Clean(p) != p {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
		if strings.ContainsAny(part, `:*?"<>|`) || strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			return "", false
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return "", false
			}
		}
	}
	if !isSaveFile(p) {
		return "", false
	}
	return p, true
}

// ssyncValidBaseKey reports whether k is "<folder key>/<relative path>".
func ssyncValidBaseKey(k string) bool {
	i := strings.IndexByte(k, '/')
	if i <= 0 {
		return false
	}
	if !ssyncValidKey(k[:i]) {
		return false
	}
	_, ok := ssyncCleanRel(k[i+1:])
	return ok
}

// ssyncLocalPath is where a relative save path lands under a folder on this
// device, refusing anything that would escape it (including through a link).
func ssyncLocalPath(rootDir, rel string) (string, error) {
	clean, ok := ssyncCleanRel(rel)
	if !ok {
		return "", errSsyncBadName
	}
	dst := filepath.Join(rootDir, filepath.FromSlash(clean))
	if !isPathWithin(dst, rootDir) {
		return "", errSsyncBadName
	}
	if !parentResolvesWithin(rootDir, dst) {
		return "", errors.New("the save folder links outside itself")
	}
	return dst, nil
}

// ── Manifests ─────────────────────────────────────────────────────────────

// ssyncHashEntry remembers a file's hash for as long as its size and
// modification time stay the same, so a manifest does not re-read every save
// state on the card each time.
type ssyncHashEntry struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	SHA   string `json:"sha256"`
}

type ssyncHashFile struct {
	V       int                       `json:"v"`
	Entries map[string]ssyncHashEntry `json:"entries"`
}

var ssyncHashes struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]ssyncHashEntry
}

// ssyncBuildMu lets one manifest be built at a time (they share the cache).
var ssyncBuildMu sync.Mutex

func ssyncHashPath() string { return filepath.Join(ssyncDataDir(), "hashes.json") }

// ssyncHashLoadLocked reads the hash cache from disk once.
func ssyncHashLoadLocked() {
	if ssyncHashes.loaded {
		return
	}
	ssyncHashes.loaded = true
	var file ssyncHashFile
	if _, err := readJSONWithBackup(ssyncHashPath(), &file); err == nil && file.Entries != nil {
		ssyncHashes.entries = file.Entries
	}
}

// ssyncHashSnapshot returns a copy of the hash cache, loading it once.
func ssyncHashSnapshot() map[string]ssyncHashEntry {
	ssyncHashes.mu.Lock()
	defer ssyncHashes.mu.Unlock()
	ssyncHashLoadLocked()
	out := make(map[string]ssyncHashEntry, len(ssyncHashes.entries))
	for k, v := range ssyncHashes.entries {
		out[k] = v
	}
	return out
}

// ssyncHashNote records the checksum of a file a sync just wrote. The file got
// the other copy's modification time, which can equal the time of the copy it
// replaced (two-second FAT times, fixed-size saves): the cache must not answer
// for the old content then.
func ssyncHashNote(p, sum string) {
	info, err := os.Stat(p)
	ssyncHashes.mu.Lock()
	defer ssyncHashes.mu.Unlock()
	ssyncHashLoadLocked()
	if err != nil || !info.Mode().IsRegular() || !ssyncValidSHA(sum) {
		delete(ssyncHashes.entries, p)
		return
	}
	if ssyncHashes.entries == nil {
		ssyncHashes.entries = make(map[string]ssyncHashEntry)
	}
	ssyncHashes.entries[p] = ssyncHashEntry{Size: info.Size(), MTime: info.ModTime().UnixNano(), SHA: strings.ToLower(sum)}
}

// ssyncHashStore replaces the cache with the files one walk saw (so deleted
// files drop out) and writes it when it changed.
func ssyncHashStore(fresh map[string]ssyncHashEntry) {
	ssyncHashes.mu.Lock()
	same := len(fresh) == len(ssyncHashes.entries)
	if same {
		for k, v := range fresh {
			if old, ok := ssyncHashes.entries[k]; !ok || old != v {
				same = false
				break
			}
		}
	}
	ssyncHashes.entries = fresh
	ssyncHashes.loaded = true
	ssyncHashes.mu.Unlock()
	if same {
		return
	}
	data, err := json.Marshal(ssyncHashFile{V: 1, Entries: fresh})
	if err != nil {
		return
	}
	if err := AtomicWrite(ssyncHashPath(), data, 0o600); err != nil {
		log.Printf("[ssync] could not save the hash cache: %v", err)
	}
}

// ssyncBuildManifest walks the save folders and describes every save file.
// Symlinks, Syncthing's markers, partial downloads and files larger than Save
// Manager accepts are left out (the last are counted in Skipped).
func ssyncBuildManifest(ctx context.Context, roots []saveRoot) (ssyncManifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	keys := ssyncRootKeys(roots)
	m := ssyncManifest{V: 1, Now: time.Now().Unix(), Name: peerDeviceName()}
	// The id is only included once this device has one: an export must not
	// make a key for a feature that was never switched on.
	if peerIdentityExists() {
		if self, ok := peerSelf(); ok {
			m.Device, m.Name = self.id, self.name
		}
	}
	old := ssyncHashSnapshot()
	fresh := make(map[string]ssyncHashEntry, len(old))
	var total int64
	for i, root := range roots {
		if !dirExists(root.Path) {
			continue
		}
		key := keys[i]
		m.Roots = append(m.Roots, ssyncRoot{Key: key, Name: root.Name})
		rootPath := root.Path
		walkErr := filepath.WalkDir(rootPath, func(p string, d os.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
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
			if _, ok := ssyncCleanRel(rel); !ok {
				// A name Windows or FAT32 could not hold is left where it is.
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
			if len(m.Files) >= ssyncMaxFiles {
				return errSsyncTooMany
			}
			total += info.Size()
			if total > saveManagerMaxTotal {
				return errSsyncTooBig
			}
			mod := info.ModTime()
			sum := ""
			if e, ok := old[p]; ok && e.Size == info.Size() && e.MTime == mod.UnixNano() && ssyncValidSHA(e.SHA) {
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
			}
			fresh[p] = ssyncHashEntry{Size: info.Size(), MTime: mod.UnixNano(), SHA: sum}
			m.Files = append(m.Files, ssyncFile{Root: key, Path: rel, Size: info.Size(), MTime: mod.Unix(), SHA: sum})
			return nil
		})
		if walkErr != nil {
			return ssyncManifest{}, walkErr
		}
	}
	sort.Slice(m.Files, func(a, b int) bool { return m.Files[a].key() < m.Files[b].key() })
	ssyncHashStore(fresh)
	return m, nil
}

// ── Rules ─────────────────────────────────────────────────────────────────

// ssyncAction is what one file needs.
type ssyncAction int

const (
	// ssyncSame: identical on both devices.
	ssyncSame ssyncAction = iota
	// ssyncPull: copy the other device's file here.
	ssyncPull
	// ssyncPush: copy this device's file there.
	ssyncPush
	// ssyncConflict: both changed; both copies are kept.
	ssyncConflict
)

// ssyncNewer orders two modification times. ordered is false when they are
// too close to call or either clock is not believable.
func ssyncNewer(local, remote int64) (localNewer, ordered bool) {
	if local < ssyncMinTime || remote < ssyncMinTime {
		return false, false
	}
	d := local - remote
	if d > ssyncClockSlack {
		return true, true
	}
	if d < -ssyncClockSlack {
		return false, true
	}
	return false, false
}

// ssyncDecide is the rule for one file. local and remote are nil when the file
// is missing on that side; base is the SHA-256 both devices had after their
// last sync ("" when unknown); skew is how far the other device's clock is
// ahead. localWins says, for a conflict, whether this device's copy keeps the
// original name (it is the newer one, or the two cannot be ordered).
func ssyncDecide(local, remote *ssyncFile, base string, skew int64) (action ssyncAction, localWins bool, reason string) {
	switch {
	case local == nil && remote == nil:
		return ssyncSame, false, ""
	case local == nil:
		return ssyncPull, false, "only on the other device"
	case remote == nil:
		return ssyncPush, true, "only on this device"
	case strings.EqualFold(local.SHA, remote.SHA):
		return ssyncSame, false, "identical"
	}
	localNewer, ordered := ssyncNewer(local.MTime, remote.MTime-skew)
	if base != "" {
		localChanged := !strings.EqualFold(local.SHA, base)
		remoteChanged := !strings.EqualFold(remote.SHA, base)
		switch {
		case !localChanged && remoteChanged:
			return ssyncPull, false, "changed on the other device"
		case localChanged && !remoteChanged:
			return ssyncPush, true, "changed on this device"
		}
		return ssyncConflict, localNewer || !ordered, "changed on both"
	}
	if !ordered {
		return ssyncConflict, true, "changed on both"
	}
	if localNewer {
		return ssyncPush, true, "newer on this device"
	}
	return ssyncPull, false, "newer on the other device"
}

// ssyncItem is one file that needs work.
type ssyncItem struct {
	Key       string
	Root      string
	Path      string
	Local     ssyncFile
	Remote    ssyncFile
	HasLocal  bool
	HasRemote bool
	Action    ssyncAction
	LocalWins bool
	Reason    string
	CopyPath  string
}

// ssyncPlan is a comparison's result: the work, and the files already the
// same (folder/path → SHA-256), which become the next sync's base.
type ssyncPlan struct {
	Items     []ssyncItem
	Same      map[string]string
	Pull      int
	Push      int
	Conflict  int
	PullBytes int64
	PushBytes int64
	Skipped   []string
}

// ssyncConflictName is "stem (from Device).ext" beside rel; a second conflict
// from the same device gets " 2", " 3"…, so nothing is ever overwritten.
func ssyncConflictName(rel, device string, taken func(string) bool) string {
	dir, file := path.Split(rel)
	ext := path.Ext(file)
	stem := strings.TrimSuffix(file, ext)
	dev := ssyncSafeDeviceName(device)
	for n := 1; n < 100; n++ {
		label := dev
		if n > 1 {
			label = dev + " " + strconv.Itoa(n)
		}
		candidate := dir + stem + " (from " + label + ")" + ext
		if taken == nil || !taken(candidate) {
			return candidate
		}
	}
	return dir + stem + " (from " + dev + " " + strconv.FormatInt(time.Now().Unix(), 10) + ")" + ext
}

// ssyncHasKeptCopy reports whether a copy of rel that an earlier conflict kept
// ("stem (from …).ext" in the same folder) already holds sha.
func ssyncHasKeptCopy(files []ssyncFile, root, rel, sha string) bool {
	dir, file := path.Split(rel)
	ext := path.Ext(file)
	prefix := dir + strings.TrimSuffix(file, ext) + " (from "
	for _, f := range files {
		if f.Root == root && strings.HasPrefix(f.Path, prefix) && strings.HasSuffix(f.Path, ")"+ext) && strings.EqualFold(f.SHA, sha) {
			return true
		}
	}
	return false
}

// ssyncSafeDeviceName is a device name a file name can carry on FAT32 and
// Windows.
func ssyncSafeDeviceName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if runes := []rune(out); len(runes) > 24 {
		out = strings.TrimSpace(string(runes[:24]))
	}
	if out == "" {
		return "other device"
	}
	return out
}

// ssyncBuildPlan compares this device's manifest with the other device's.
// base is the version both had after the last sync (may be nil), skew how far
// the other clock is ahead, and the names label "(from …)" copies.
func ssyncBuildPlan(local, remote ssyncManifest, base map[string]string, skew int64, localName, remoteName string) ssyncPlan {
	plan := ssyncPlan{Same: map[string]string{}}
	localRoots := map[string]bool{}
	for _, r := range local.Roots {
		localRoots[r.Key] = true
	}
	remoteRoots := map[string]bool{}
	for _, r := range remote.Roots {
		remoteRoots[r.Key] = true
	}
	localFiles := make(map[string]ssyncFile, len(local.Files))
	for _, f := range local.Files {
		localFiles[f.key()] = f
	}
	remoteFiles := make(map[string]ssyncFile, len(remote.Files))
	for _, f := range remote.Files {
		_, okPath := ssyncCleanRel(f.Path)
		if !okPath || !ssyncValidKey(f.Root) || !ssyncValidSHA(f.SHA) || f.Size < 0 || f.Size > saveManagerMaxFile {
			plan.Skipped = append(plan.Skipped, f.Path+" (a name this device cannot store)")
			continue
		}
		f.SHA = strings.ToLower(f.SHA)
		remoteFiles[f.key()] = f
	}
	taken := make(map[string]bool, len(localFiles)+len(remoteFiles))
	keys := make([]string, 0, len(localFiles)+len(remoteFiles))
	for k := range localFiles {
		taken[k] = true
		keys = append(keys, k)
	}
	for k := range remoteFiles {
		taken[k] = true
		if _, ok := localFiles[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lf, hasL := localFiles[k]
		rf, hasR := remoteFiles[k]
		var lp, rp *ssyncFile
		root, rel := rf.Root, rf.Path
		if hasL {
			lp = &lf
			root, rel = lf.Root, lf.Path
		}
		if hasR {
			rp = &rf
		}
		action, localWins, reason := ssyncDecide(lp, rp, base[k], skew)
		switch action {
		case ssyncSame:
			plan.Same[k] = strings.ToLower(lf.SHA)
			continue
		case ssyncPull:
			if !localRoots[root] {
				plan.Skipped = append(plan.Skipped, rel+" (no such save folder here)")
				continue
			}
			plan.Pull++
			plan.PullBytes += rf.Size
		case ssyncPush:
			if !remoteRoots[root] {
				plan.Skipped = append(plan.Skipped, rel+" (no such save folder there)")
				continue
			}
			plan.Push++
			plan.PushBytes += lf.Size
		case ssyncConflict:
			plan.Conflict++
			plan.PullBytes += rf.Size
			plan.PushBytes += lf.Size + rf.Size
		}
		item := ssyncItem{
			Key:       k,
			Root:      root,
			Path:      rel,
			Local:     lf,
			Remote:    rf,
			HasLocal:  hasL,
			HasRemote: hasR,
			Action:    action,
			LocalWins: localWins,
			Reason:    reason,
		}
		if action == ssyncConflict {
			loser := localName
			if localWins {
				loser = remoteName
			}
			prefix := root + "/"
			item.CopyPath = ssyncConflictName(rel, loser, func(p string) bool { return taken[prefix+p] })
			taken[prefix+item.CopyPath] = true
		}
		plan.Items = append(plan.Items, item)
	}
	return plan
}

// ssyncFilterPlan keeps the items keep accepts and recounts them.
func ssyncFilterPlan(plan ssyncPlan, keep func(ssyncItem) bool) ssyncPlan {
	out := ssyncPlan{Same: plan.Same, Skipped: plan.Skipped}
	for _, it := range plan.Items {
		if !keep(it) {
			continue
		}
		out.Items = append(out.Items, it)
		switch it.Action {
		case ssyncPull:
			out.Pull++
			out.PullBytes += it.Remote.Size
		case ssyncPush:
			out.Push++
			out.PushBytes += it.Local.Size
		case ssyncConflict:
			out.Conflict++
			out.PullBytes += it.Remote.Size
			out.PushBytes += it.Local.Size + it.Remote.Size
		}
	}
	return out
}

// ssyncLocalOverwrites is the files carrying out plan replaces on this device
// (folder/path keys): they go into the safety snapshot first.
func ssyncLocalOverwrites(plan ssyncPlan) map[string]bool {
	out := map[string]bool{}
	for _, it := range plan.Items {
		if (it.Action == ssyncPull && it.HasLocal) || (it.Action == ssyncConflict && !it.LocalWins) {
			out[it.Key] = true
		}
	}
	return out
}

// ssyncRemoteOverwrites is the files carrying out plan replaces on the other
// device, which snapshots them before the first one arrives.
func ssyncRemoteOverwrites(plan ssyncPlan) []string {
	var out []string
	for _, it := range plan.Items {
		if (it.Action == ssyncPush && it.HasRemote) || (it.Action == ssyncConflict && it.LocalWins) {
			out = append(out, it.Key)
		}
	}
	sort.Strings(out)
	return out
}

// ssyncSnapshotFiles takes a Save Manager snapshot of just the files in keys
// (folder/path), so "before sync" copies stay as small as what they protect.
func ssyncSnapshotFiles(roots []saveRoot, keys map[string]bool, label string) (saveSnapshot, error) {
	rootKeys := ssyncRootKeys(roots)
	keyOf := make(map[string]string, len(roots))
	for i, root := range roots {
		keyOf[root.Path] = rootKeys[i]
	}
	return createSaveSnapshotOf(roots, label, func(rootPath, rel string) bool {
		return keys[keyOf[rootPath]+"/"+rel]
	})
}

// ── Writing files ─────────────────────────────────────────────────────────

// ssyncWriteFile streams r into dst through a temp file beside it, checks the
// size and the SHA-256, stamps the modification time and renames it into
// place. size < 0 means unknown (bounded by Save Manager's file limit).
func ssyncWriteFile(dst string, r io.Reader, size int64, wantSHA string, mtime int64) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".jukasync-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmpName)
		}
	}()
	limit := int64(saveManagerMaxFile)
	if size >= 0 && size < limit {
		limit = size
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, limit+1))
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	}
	if n > limit || (size >= 0 && n != size) {
		return fmt.Errorf("got %s, expected %s — the connection dropped?", humanBytes(n), humanBytes(size))
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), wantSHA) {
		return errSsyncChecksum
	}
	if mtime > 0 {
		t := time.Unix(mtime, 0)
		_ = os.Chtimes(tmpName, t, t)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	done = true
	ssyncHashNote(dst, wantSHA)
	return nil
}

// ssyncCopyLocal copies a file on this device, checked like a transfer.
func ssyncCopyLocal(src, dst string, f ssyncFile) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return ssyncWriteFile(dst, in, f.Size, f.SHA, f.MTime)
}

// ── Sync base ─────────────────────────────────────────────────────────────

type ssyncBaseFile struct {
	V     int               `json:"v"`
	Peer  string            `json:"peer"`
	At    time.Time         `json:"at"`
	Files map[string]string `json:"files"`
}

func ssyncBasePath(peerID string) string {
	id := peerID
	if len(id) > 16 {
		id = id[:16]
	}
	return filepath.Join(ssyncDataDir(), "base-"+id+".json")
}

// ssyncLoadBase is what both devices had after their last sync (never nil).
func ssyncLoadBase(peerID string) map[string]string {
	out := map[string]string{}
	if !peerValidID(peerID) {
		return out
	}
	var file ssyncBaseFile
	if _, err := readJSONWithBackup(ssyncBasePath(peerID), &file); err != nil || file.Peer != peerID {
		return out
	}
	for k, v := range file.Files {
		if ssyncValidBaseKey(k) && ssyncValidSHA(v) {
			out[k] = strings.ToLower(v)
		}
	}
	return out
}

// ssyncSaveBase records what both devices hold after a sync.
func ssyncSaveBase(peerID string, files map[string]string) error {
	if !peerValidID(peerID) {
		return errors.New("bad device id")
	}
	data, err := json.Marshal(ssyncBaseFile{V: 1, Peer: peerID, At: time.Now().UTC(), Files: files})
	if err != nil {
		return err
	}
	return AtomicWrite(ssyncBasePath(peerID), data, 0o600)
}

// ssyncForgetPeer drops what was kept about a device that was unpaired.
func ssyncForgetPeer(peerID string) {
	if !peerValidID(peerID) {
		return
	}
	p := ssyncBasePath(peerID)
	_ = os.Remove(p)
	_ = os.Remove(p + ".bak")
}

// ssyncLastSync is when this device last synced with peerID (zero if never).
func ssyncLastSync(peerID string) time.Time {
	if !peerValidID(peerID) {
		return time.Time{}
	}
	info, err := os.Stat(ssyncBasePath(peerID))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// ── Carrying out a plan ───────────────────────────────────────────────────

// ssyncPeerIO moves files to and from the other device. The real one is
// HTTPS (ssyncNetIO); the tests use a second folder.
type ssyncPeerIO interface {
	// prepare asks the other device to snapshot the files (folder/path keys)
	// this sync is about to overwrite there.
	prepare(ctx context.Context, keys []string) error
	// pull copies the other device's file f into dst on this device.
	pull(ctx context.Context, f ssyncFile, dst string) error
	// push copies this device's file at src to the other device, where it is
	// stored as f.Root/f.Path with f's checksum and time.
	push(ctx context.Context, f ssyncFile, src string) error
}

// ssyncResult is what a sync did.
type ssyncResult struct {
	Peer      string
	Pulled    int
	Pushed    int
	Conflicts int
	Failed    int
	Snapshot  string
	Errors    []string
	At        time.Time
}

// ssyncExecute carries out a plan against roots (this device's save folders,
// in saveManagerRoots order). Both devices snapshot the files they are about
// to overwrite before the first copy (label names this device's snapshot); it
// returns what happened plus the base entries the sync established: every
// file both devices now hold.
func ssyncExecute(ctx context.Context, plan ssyncPlan, pio ssyncPeerIO, roots []saveRoot, remoteName, label string, progress func(done, total int, current string)) (ssyncResult, map[string]string) {
	res := ssyncResult{Peer: remoteName, At: time.Now()}
	updates := make(map[string]string, len(plan.Same)+len(plan.Items))
	for k, sha := range plan.Same {
		updates[k] = sha
	}
	if over := ssyncLocalOverwrites(plan); len(over) > 0 {
		snap, err := ssyncSnapshotFiles(roots, over, label)
		if err != nil && !errors.Is(err, errSaveManagerEmpty) {
			res.Errors = append(res.Errors, "Could not take a safety snapshot, so nothing was copied: "+firstLine(err.Error()))
			res.Failed = len(plan.Items)
			return res, updates
		}
		res.Snapshot = snap.Manifest.ID
	}
	if over := ssyncRemoteOverwrites(plan); len(over) > 0 {
		if err := pio.prepare(ctx, over); err != nil {
			res.Errors = append(res.Errors, remoteName+" could not take a safety snapshot, so nothing was copied: "+firstLine(err.Error()))
			res.Failed = len(plan.Items)
			return res, updates
		}
	}
	rootPaths := ssyncRootMap(roots)
	total := len(plan.Items)
	for i, it := range plan.Items {
		if ctx.Err() != nil {
			res.Errors = append(res.Errors, "Stopped — the rest is left for the next sync")
			break
		}
		if progress != nil {
			progress(i, total, it.Path)
		}
		if err := ssyncExecuteItem(ctx, it, pio, rootPaths, updates, &res); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, path.Base(it.Path)+": "+firstLine(err.Error()))
		}
	}
	if progress != nil {
		progress(total, total, "")
	}
	return res, updates
}

// ssyncExecuteItem carries out one file.
func ssyncExecuteItem(ctx context.Context, it ssyncItem, pio ssyncPeerIO, rootPaths map[string]string, updates map[string]string, res *ssyncResult) error {
	rootDir, ok := rootPaths[it.Root]
	if !ok {
		return errors.New("no such save folder here")
	}
	dst, err := ssyncLocalPath(rootDir, it.Path)
	if err != nil {
		return err
	}
	switch it.Action {
	case ssyncPull:
		if err := pio.pull(ctx, it.Remote, dst); err != nil {
			return err
		}
		updates[it.Key] = it.Remote.SHA
		res.Pulled++
	case ssyncPush:
		if err := pio.push(ctx, it.Local, dst); err != nil {
			return err
		}
		updates[it.Key] = it.Local.SHA
		res.Pushed++
	case ssyncConflict:
		copyDst, err := ssyncLocalPath(rootDir, it.CopyPath)
		if err != nil {
			return err
		}
		copyKey := it.Root + "/" + it.CopyPath
		if it.LocalWins {
			// The other device's version is kept beside this one, on both.
			if err := pio.pull(ctx, it.Remote, copyDst); err != nil {
				return err
			}
			if err := pio.push(ctx, it.Local, dst); err != nil {
				return err
			}
			loser := it.Remote
			loser.Path = it.CopyPath
			if err := pio.push(ctx, loser, copyDst); err != nil {
				return err
			}
			updates[it.Key] = it.Local.SHA
			updates[copyKey] = it.Remote.SHA
		} else {
			// This device's version is kept beside the other device's.
			if err := ssyncCopyLocal(dst, copyDst, it.Local); err != nil {
				return err
			}
			if err := pio.pull(ctx, it.Remote, dst); err != nil {
				return err
			}
			loser := it.Local
			loser.Path = it.CopyPath
			if err := pio.push(ctx, loser, copyDst); err != nil {
				return err
			}
			updates[it.Key] = it.Remote.SHA
			updates[copyKey] = it.Local.SHA
		}
		res.Conflicts++
	}
	return nil
}

// ── Over the network ──────────────────────────────────────────────────────

// ssyncNetIO is the other device over the paired HTTPS connection.
type ssyncNetIO struct {
	peerID  string
	session string
}

// ssyncTransferTimeout allows half a minute plus four seconds per megabyte: a
// slow handheld Wi-Fi moves one to three.
func ssyncTransferTimeout(size int64) time.Duration {
	return 30*time.Second + time.Duration(size/(256<<10))*time.Second
}

// ssyncPrepareReq lists the files a sync is about to overwrite on the device
// that receives it.
type ssyncPrepareReq struct {
	Session string   `json:"session"`
	Files   []string `json:"files"`
}

func (n ssyncNetIO) prepare(ctx context.Context, keys []string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return peerDoJSON(ctx, n.peerID, http.MethodPost, "/peer/v1/saves/prepare", ssyncPrepareReq{Session: n.session, Files: keys}, nil, 0)
}

func (n ssyncNetIO) pull(ctx context.Context, f ssyncFile, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(f.Size))
	defer cancel()
	q := url.Values{}
	q.Set("root", f.Root)
	q.Set("path", f.Path)
	resp, err := peerDo(ctx, n.peerID, http.MethodGet, "/peer/v1/saves/file?"+q.Encode(), nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	return ssyncWriteFile(dst, resp.Body, f.Size, f.SHA, f.MTime)
}

func (n ssyncNetIO) push(ctx context.Context, f ssyncFile, src string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != f.Size {
		return errSsyncChanged
	}
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(f.Size))
	defer cancel()
	q := url.Values{}
	q.Set("root", f.Root)
	q.Set("path", f.Path)
	q.Set("sha256", f.SHA)
	q.Set("mtime", strconv.FormatInt(f.MTime, 10))
	q.Set("session", n.session)
	resp, err := peerDo(ctx, n.peerID, http.MethodPut, "/peer/v1/saves/file?"+q.Encode(), file, f.Size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// ssyncComparison is a finished comparison, ready to sync.
type ssyncComparison struct {
	PeerID   string
	PeerName string
	Local    ssyncManifest
	Remote   ssyncManifest
	Plan     ssyncPlan
	Roots    []saveRoot
	At       time.Time
}

// ssyncCompare fetches the other device's manifest, builds this device's and
// plans the sync. The first comparison hashes every save, which can take a
// while on a card full of save states; later ones reuse the hash cache.
func ssyncCompare(ctx context.Context, peerID, peerName string) (ssyncComparison, error) {
	var remote ssyncManifest
	mctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err := peerDoJSON(mctx, peerID, http.MethodGet, "/peer/v1/saves/manifest", nil, &remote, ssyncManifestLimit)
	cancel()
	if err != nil {
		return ssyncComparison{}, err
	}
	fetched := time.Now().Unix()
	if remote.V != 1 || len(remote.Files) > ssyncMaxFiles {
		return ssyncComparison{}, errors.New(peerName + " sent a save list this version cannot read")
	}
	roots := saveManagerRoots()
	ssyncBuildMu.Lock()
	local, err := ssyncBuildManifest(ctx, roots)
	ssyncBuildMu.Unlock()
	if err != nil {
		return ssyncComparison{}, err
	}
	// A clock within a minute is NTP-close: no correction.
	skew := remote.Now - fetched
	if skew > -60 && skew < 60 {
		skew = 0
	}
	plan := ssyncBuildPlan(local, remote, ssyncLoadBase(peerID), skew, local.Name, peerName)
	return ssyncComparison{
		PeerID:   peerID,
		PeerName: peerName,
		Local:    local,
		Remote:   remote,
		Plan:     plan,
		Roots:    roots,
		At:       time.Now(),
	}, nil
}

// ssyncDoneReq tells the other device a sync finished and what both now hold.
type ssyncDoneReq struct {
	Session string            `json:"session"`
	Base    map[string]string `json:"base"`
	Updated int               `json:"updated"`
}

// ssyncNewSession names one sync on the other device (its safety snapshot is
// taken once per session).
func ssyncNewSession() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// ssyncRunSync carries out a comparison and records the new base on both
// devices. Files that failed keep their old base, so the next sync sees them.
func ssyncRunSync(ctx context.Context, cmp ssyncComparison, progress func(done, total int, current string)) ssyncResult {
	session := ssyncNewSession()
	pio := ssyncNetIO{peerID: cmp.PeerID, session: session}
	res, updates := ssyncExecute(ctx, cmp.Plan, pio, cmp.Roots, cmp.PeerName, "before sync with "+cmp.PeerName, progress)
	base := ssyncLoadBase(cmp.PeerID)
	for k, v := range updates {
		base[k] = v
	}
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
	if err := ssyncSaveBase(cmp.PeerID, base); err != nil {
		log.Printf("[ssync] could not save the sync base: %v", err)
	}
	dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = peerDoJSON(dctx, cmp.PeerID, http.MethodPost, "/peer/v1/saves/done", ssyncDoneReq{
		Session: session,
		Base:    base,
		Updated: res.Pushed + res.Conflicts,
	}, nil, 0)
	cancel()
	return res
}

// ssyncGameStem is a ROM's file name without its extension, lower case. Both
// separators are understood: the path may come from a Windows device.
func ssyncGameStem(romPath string) string {
	base := romPath
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, path.Ext(base))
	return strings.ToLower(strings.TrimSpace(base))
}

// ssyncMatchesGame reports whether a save file belongs to the game with stem:
// "Zelda.srm", "Zelda.state1", "Zelda.state.auto", or a kept copy of one.
func ssyncMatchesGame(rel, stem string) bool {
	if stem == "" {
		return false
	}
	base := strings.ToLower(path.Base(rel))
	return base == stem || strings.HasPrefix(base, stem+".") || strings.HasPrefix(base, stem+" (from ")
}

// ssyncPullForGame brings one game's newest saves from a paired device before
// it starts here (handoff). Only copies toward this device run; a save that is
// newer here stays.
func ssyncPullForGame(ctx context.Context, peerID, peerName, romPath string) (ssyncResult, error) {
	stem := ssyncGameStem(romPath)
	if stem == "" {
		return ssyncResult{Peer: peerName}, nil
	}
	cmp, err := ssyncCompare(ctx, peerID, peerName)
	if err != nil {
		return ssyncResult{Peer: peerName}, err
	}
	cmp.Plan = ssyncFilterPlan(cmp.Plan, func(it ssyncItem) bool {
		return (it.Action == ssyncPull || it.Action == ssyncConflict) && ssyncMatchesGame(it.Path, stem)
	})
	if len(cmp.Plan.Items) == 0 {
		return ssyncResult{Peer: peerName}, nil
	}
	return ssyncRunSync(ctx, cmp, nil), nil
}

// ── Serving the other device ──────────────────────────────────────────────

// ssyncHandleManifest answers with this device's save list.
func ssyncHandleManifest(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if gameScreenSuspended() {
		http.Error(w, "a game is running there — close it first", http.StatusConflict)
		return
	}
	ssyncBuildMu.Lock()
	man, err := ssyncBuildManifest(r.Context(), saveManagerRoots())
	ssyncBuildMu.Unlock()
	if err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	peerWriteJSONResponse(w, http.StatusOK, man)
}

// ssyncHandlePrepare snapshots the files a sync is about to overwrite here,
// once, before the first one arrives.
func ssyncHandlePrepare(w http.ResponseWriter, r *http.Request, call peerCall) {
	var req ssyncPrepareReq
	if !peerDecodeBody(w, r, ssyncManifestLimit, &req) {
		return
	}
	if gameScreenSuspended() {
		http.Error(w, "a game is running there — close it first", http.StatusConflict)
		return
	}
	keys := make(map[string]bool, len(req.Files))
	for _, k := range req.Files {
		if len(keys) >= ssyncMaxFiles {
			break
		}
		if ssyncValidBaseKey(k) {
			keys[k] = true
		}
	}
	if len(keys) > 0 {
		_, err := ssyncSnapshotFiles(saveManagerRoots(), keys, "before sync from "+call.rec.Name)
		if err != nil && !errors.Is(err, errSaveManagerEmpty) {
			http.Error(w, "could not take a safety snapshot: "+firstLine(err.Error()), http.StatusInternalServerError)
			return
		}
	}
	ssyncIncoming.mu.Lock()
	ssyncIncomingLocked(call, req.Session).snapshot = true
	ssyncIncoming.mu.Unlock()
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ssyncHandleFile sends (GET) or receives (PUT) one save file.
func ssyncHandleFile(w http.ResponseWriter, r *http.Request, call peerCall) {
	q := r.URL.Query()
	roots := saveManagerRoots()
	rootDir, ok := ssyncRootMap(roots)[q.Get("root")]
	if !ok {
		http.Error(w, "no such save folder here", http.StatusNotFound)
		return
	}
	dst, err := ssyncLocalPath(rootDir, q.Get("path"))
	if err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ssyncServeFile(w, dst)
	case http.MethodPut:
		ssyncReceiveFile(w, r, call, roots, dst)
	default:
		http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
	}
}

func ssyncServeFile(w http.ResponseWriter, p string) {
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > saveManagerMaxFile {
		http.Error(w, "not a save file", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(f, info.Size()))
}

func ssyncReceiveFile(w http.ResponseWriter, r *http.Request, call peerCall, roots []saveRoot, dst string) {
	if gameScreenSuspended() {
		http.Error(w, "a game is running there — close it first", http.StatusConflict)
		return
	}
	q := r.URL.Query()
	sum := strings.ToLower(strings.TrimSpace(q.Get("sha256")))
	if !ssyncValidSHA(sum) {
		http.Error(w, "missing checksum", http.StatusBadRequest)
		return
	}
	mtime, _ := strconv.ParseInt(q.Get("mtime"), 10, 64)
	if r.ContentLength < 0 || r.ContentLength > saveManagerMaxFile {
		http.Error(w, "the file is too large for a save", http.StatusRequestEntityTooLarge)
		return
	}
	session := q.Get("session")
	if !ssyncIncomingSnapshot(call, session, roots, dst) {
		http.Error(w, "could not take a safety snapshot first", http.StatusInternalServerError)
		return
	}
	body := http.MaxBytesReader(w, r.Body, saveManagerMaxFile)
	if err := ssyncWriteFile(dst, body, r.ContentLength, sum, mtime); err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusBadRequest)
		return
	}
	ssyncNoteIncoming(call, session)
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ssyncIncoming tracks the syncs other devices run against this one: the
// safety snapshot is taken once per sync, before its first overwrite.
var ssyncIncoming struct {
	mu       sync.Mutex
	sessions map[string]*ssyncIncomingSession
}

type ssyncIncomingSession struct {
	name     string
	snapshot bool
	files    int
	seen     time.Time
}

// ssyncIncomingLocked finds (or starts) the session for a caller.
func ssyncIncomingLocked(call peerCall, session string) *ssyncIncomingSession {
	if ssyncIncoming.sessions == nil {
		ssyncIncoming.sessions = make(map[string]*ssyncIncomingSession)
	}
	now := time.Now()
	for k, s := range ssyncIncoming.sessions {
		if now.Sub(s.seen) > time.Hour {
			delete(ssyncIncoming.sessions, k)
		}
	}
	if len(session) > 64 {
		session = session[:64]
	}
	key := call.id + "|" + session
	s := ssyncIncoming.sessions[key]
	if s == nil {
		s = &ssyncIncomingSession{name: call.rec.Name}
		ssyncIncoming.sessions[key] = s
	}
	s.seen = now
	return s
}

// ssyncIncomingSnapshot takes the safety snapshot before a sync's first
// overwrite on this device. A file that does not exist yet needs none.
func ssyncIncomingSnapshot(call peerCall, session string, roots []saveRoot, dst string) bool {
	if !fileExists(dst) {
		return true
	}
	ssyncIncoming.mu.Lock()
	s := ssyncIncomingLocked(call, session)
	taken := s.snapshot
	ssyncIncoming.mu.Unlock()
	if taken {
		return true
	}
	if _, err := createSaveSnapshot(roots, "before sync from "+call.rec.Name); err != nil && !errors.Is(err, errSaveManagerEmpty) {
		log.Printf("[ssync] safety snapshot failed: %v", err)
		return false
	}
	ssyncIncoming.mu.Lock()
	s.snapshot = true
	ssyncIncoming.mu.Unlock()
	return true
}

// ssyncNoteIncoming counts a received file; the first one of a sync says so on
// screen.
func ssyncNoteIncoming(call peerCall, session string) {
	ssyncIncoming.mu.Lock()
	s := ssyncIncomingLocked(call, session)
	s.files++
	first := s.files == 1
	ssyncIncoming.mu.Unlock()
	if first {
		name := call.rec.Name
		tbxPost(func() { ShowToast("Receiving saves from "+name+"…", ToastKindInfo) })
	}
}

// ssyncHandleDone records the base the other device computed and says the
// sync finished.
func ssyncHandleDone(w http.ResponseWriter, r *http.Request, call peerCall) {
	var req ssyncDoneReq
	if !peerDecodeBody(w, r, ssyncManifestLimit, &req) {
		return
	}
	base := make(map[string]string, len(req.Base))
	for k, v := range req.Base {
		if len(base) >= ssyncMaxFiles {
			break
		}
		if ssyncValidBaseKey(k) && ssyncValidSHA(v) {
			base[k] = strings.ToLower(v)
		}
	}
	if err := ssyncSaveBase(call.id, base); err != nil {
		log.Printf("[ssync] could not save the sync base: %v", err)
	}
	ssyncIncoming.mu.Lock()
	s := ssyncIncomingLocked(call, req.Session)
	files := s.files
	ssyncIncoming.mu.Unlock()
	name := call.rec.Name
	tbxPost(func() {
		if files > 0 {
			ShowToast(fmt.Sprintf("Saves synced with %s · %d updated here", name, files), ToastKindSuccess)
			return
		}
		ShowToast("Saves synced with "+name+" · nothing changed here", ToastKindSuccess)
	})
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
