package main

// webdav_snapshots.go — versioned, client-encrypted save snapshots on a
// WebDAV server (SYS-19).
//
// A snapshot is the same .jukasaves file "Export encrypted saves" writes
// (ssyncbundle.go): AES-256-GCM chunks under a PBKDF2 key, so the server only
// ever holds the sealed form and its folder listing shows sizes, not saves.
// The files live in the sync folder's snapshots/ subfolder, one file per
// version, named for the device and the minute it was taken. Nothing on the
// server is overwritten: an upload goes to "<name>.part" and is MOVE-d into
// place, so an interrupted upload leaves every earlier snapshot readable.
//
// Restoring goes through the ordinary import preview: fetch the newest
// snapshot into the JukaHub Saves folder, then choose it in "Import a saves
// file" (or call webdavSnapshotRestore, which previews and applies it). The
// import takes a Save Manager snapshot of everything it replaces first
// (ssyncImportApply), so a wrong passphrase or a wrong snapshot costs nothing.

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// webdavSnapshotFolder is the subfolder of the sync folder that holds the
	// versions. It is never touched by the file sync.
	webdavSnapshotFolder = "snapshots"
	// webdavSnapshotExt is the encrypted bundle's extension.
	webdavSnapshotExt = ssyncBundleExt
	// webdavSnapshotMaxBytes bounds one download: a bundle holds every save,
	// but not an unbounded file from a server.
	webdavSnapshotMaxBytes = 2 << 30
	// webdavSnapshotKeep bounds the versions listed on one page.
	webdavSnapshotKeep = 100
)

// webdavSnapshot is one version on the server.
type webdavSnapshot struct {
	Name string
	Size int64
	At   time.Time
}

// webdavSnapshotFolderURL is the snapshots folder's address.
func webdavSnapshotFolderURL(c *webdavConn) string {
	return c.top.String() + webdavSnapshotFolder + "/"
}

// webdavSnapshotSlug makes a device name safe for a URL path segment: letters,
// digits, dots, dashes and underscores stay, everything else becomes a dash.
func webdavSnapshotSlug(device string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(device) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		return "device"
	}
	return out
}

// webdavSnapshotName is one version's file name: readable, sortable by time,
// and free of characters a server would escape differently.
func webdavSnapshotName(device string, at time.Time) string {
	return "snapshot-" + webdavSnapshotSlug(device) + "-" + at.UTC().Format("20060102-150405") + webdavSnapshotExt
}

// webdavSnapshotValidName reports whether name is a snapshot this code wrote:
// one path segment ending in the bundle extension. The listing is outside
// data, so nothing else may reach a URL or a local file name.
func webdavSnapshotValidName(name string) bool {
	if name == "" || len(name) > 200 || !strings.HasSuffix(name, webdavSnapshotExt) {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return false
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

// webdavSnapshotAt is a version's time: the server's Last-Modified when it
// holds a valid one, else the name's own stamp, else the zero time.
func webdavSnapshotAt(name, lastMod string) time.Time {
	if t, err := http.ParseTime(strings.TrimSpace(lastMod)); err == nil {
		return t
	}
	stem := strings.TrimSuffix(name, webdavSnapshotExt)
	if i := strings.LastIndexByte(stem, '-'); i >= 0 && i+1 < len(stem) {
		if t, err := time.ParseInLocation("20060102-150405", stem[i+1:], time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

// webdavSnapshotMove is a WebDAV MOVE from one URL to another, with an
// absolute Destination. A server that does not support MOVE answers 405 or
// 501; the caller falls back to writing the final name itself.
func (c *webdavConn) move(ctx context.Context, from, to string) error {
	hdr := http.Header{}
	hdr.Set("Destination", to)
	hdr.Set("Overwrite", "T")
	resp, err := c.send(ctx, webdavReq{method: "MOVE", url: from, header: hdr})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webdavAnswerLimit))
	resp.Body.Close()
	return nil
}

// webdavSnapshotDelete removes a temporary upload. A failure is logged, never
// returned: the .part is ignored by the listing either way.
func (c *webdavConn) snapshotDelete(ctx context.Context, target string) {
	resp, err := c.send(ctx, webdavReq{method: http.MethodDelete, url: target})
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webdavAnswerLimit))
	resp.Body.Close()
}

// webdavSnapshotUpload writes every save under roots into one new encrypted
// version on the server and returns it. passphrase is the one the user chose;
// it is never stored. progress may be nil.
func webdavSnapshotUpload(ctx context.Context, c *webdavConn, roots []saveRoot, passphrase string, at time.Time, progress func(done, total int)) (webdavSnapshot, error) {
	if c == nil {
		return webdavSnapshot{}, errWebdavNotSet
	}
	if at.IsZero() {
		at = time.Now()
	}
	dir, err := os.MkdirTemp("", "jukahub-snapshot-")
	if err != nil {
		return webdavSnapshot{}, err
	}
	defer os.RemoveAll(dir)
	file, n, err := ssyncExport(ctx, roots, dir, passphrase, progress)
	if err != nil {
		return webdavSnapshot{}, err
	}
	info, err := os.Stat(file)
	if err != nil {
		return webdavSnapshot{}, err
	}
	if n == 0 {
		return webdavSnapshot{}, errSaveManagerEmpty
	}
	// The sync folder itself may not exist before the first sync.
	if err := c.mkcol(ctx, c.top.String()); err != nil && !webdavStatusIs(err, http.StatusConflict) {
		return webdavSnapshot{}, err
	}
	if err := c.mkcol(ctx, webdavSnapshotFolderURL(c)); err != nil && !webdavStatusIs(err, http.StatusConflict) {
		return webdavSnapshot{}, err
	}
	name := webdavSnapshotName(peerDeviceName(), at)
	target := webdavSnapshotFolderURL(c) + url.PathEscape(name)
	part := target + ".part"

	send := func(url string) error {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = c.put(ctx, url, io.LimitReader(f, info.Size()), info.Size(), nil)
		return err
	}
	if err := send(part); err != nil {
		c.snapshotDelete(ctx, part)
		return webdavSnapshot{}, err
	}
	if err := c.move(ctx, part, target); err != nil {
		if !webdavStatusIs(err, http.StatusMethodNotAllowed, http.StatusNotImplemented) {
			c.snapshotDelete(ctx, part)
			return webdavSnapshot{}, err
		}
		// No MOVE on this server: write the final name directly. A cut-off
		// upload is still harmless: the listing only offers files whose
		// header opens as a bundle, and every earlier version is untouched.
		if err := send(target); err != nil {
			c.snapshotDelete(ctx, target)
			return webdavSnapshot{}, err
		}
		c.snapshotDelete(ctx, part)
	}
	return webdavSnapshot{Name: name, Size: info.Size(), At: at}, nil
}

// webdavSnapshotList lists the versions on the server, newest first. A folder
// that does not exist yet is no error: nothing has been uploaded.
func webdavSnapshotList(ctx context.Context, c *webdavConn) ([]webdavSnapshot, error) {
	if c == nil {
		return nil, errWebdavNotSet
	}
	folder := webdavSnapshotFolderURL(c)
	ref, err := url.Parse(folder)
	if err != nil {
		return nil, err
	}
	items, err := c.propfind(ctx, folder, ref, "1")
	if webdavStatusIs(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]webdavSnapshot, 0, len(items))
	for _, it := range items {
		if it.Dir || strings.Contains(it.Rel, "/") || !webdavSnapshotValidName(it.Rel) {
			continue
		}
		out = append(out, webdavSnapshot{Name: it.Rel, Size: it.Size, At: webdavSnapshotAt(it.Rel, it.LastMod)})
		if len(out) >= webdavSnapshotKeep {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// webdavSnapshotFetch downloads one version into dir and returns its local
// path. The file is checked as a JukaHub bundle before it is given its final
// name, so a cut-off or foreign file is never offered to the importer.
func webdavSnapshotFetch(ctx context.Context, c *webdavConn, name, dir string) (string, error) {
	if c == nil {
		return "", errWebdavNotSet
	}
	if !webdavSnapshotValidName(name) {
		return "", errors.New("that is not a saves file JukaHub can restore")
	}
	resp, err := c.get(ctx, webdavSnapshotFolderURL(c)+url.PathEscape(name))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := ssyncUniquePath(filepath.Join(dir, name))
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, webdavSnapshotMaxBytes+1))
	closeErr := f.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tmp)
		return "", gdriveNetError(copyErr)
	}
	if n > webdavSnapshotMaxBytes {
		os.Remove(tmp)
		return "", errors.New("that snapshot is larger than JukaHub will restore")
	}
	if _, err := ssyncPeekBundle(tmp); err != nil {
		os.Remove(tmp)
		return "", errors.New("the download did not arrive whole — try again")
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

// webdavSnapshotPreview plans a restore: unlike an import, which merges two
// devices' changes and keeps both copies of a conflict, a snapshot is the
// state to go back to. Every local copy counts as the base, so a file the
// snapshot holds is pulled even when it changed here afterwards; a local file
// the snapshot does not hold stays where it is. It reads the bundle with the
// same preview as an import, so a wrong passphrase fails before anything is
// written.
func webdavSnapshotPreview(ctx context.Context, path, passphrase string, roots []saveRoot) (ssyncImportPreview, error) {
	f, hdr, or, err := ssyncOpenBundle(path, passphrase)
	if err != nil {
		return ssyncImportPreview{}, err
	}
	defer f.Close()
	man, err := ssyncReadBundleManifest(tar.NewReader(or))
	if err != nil {
		return ssyncImportPreview{}, err
	}
	ssyncBuildMu.Lock()
	local, err := ssyncBuildManifest(ctx, roots)
	ssyncBuildMu.Unlock()
	if err != nil {
		return ssyncImportPreview{}, err
	}
	from := peerCleanName(man.Name)
	if from == "" {
		from = peerCleanName(hdr.Name)
	}
	if from == "" {
		from = "the snapshot"
	}
	base := make(map[string]string, len(local.Files))
	for _, it := range local.Files {
		base[it.key()] = it.SHA
	}
	plan := ssyncBuildPlan(local, man, base, 0, local.Name, from)
	// A restore only changes this device.
	plan = ssyncFilterPlan(plan, func(it ssyncItem) bool { return it.Action != ssyncPush })
	return ssyncImportPreview{Path: path, Header: hdr, From: from, Bundle: man, Plan: plan}, nil
}

// webdavSnapshotRestore downloads one version and applies it through the
// preview above; the merge takes a Save Manager snapshot of every file it
// replaces before the first one. Tests call this seam to exercise the full
// restore against a local WebDAV server.
func webdavSnapshotRestore(ctx context.Context, c *webdavConn, snap webdavSnapshot, passphrase string, roots []saveRoot, dir string, progress func(done, total int, current string)) (ssyncResult, error) {
	path, err := webdavSnapshotFetch(ctx, c, snap.Name, dir)
	if err != nil {
		return ssyncResult{}, err
	}
	prev, err := webdavSnapshotPreview(ctx, path, passphrase, roots)
	if err != nil {
		return ssyncResult{}, err
	}
	if len(prev.Plan.Items) == 0 {
		return ssyncResult{Peer: prev.From, At: time.Now()}, nil
	}
	return ssyncImportApply(ctx, prev, passphrase, roots, progress)
}

// webdavSnapshotSummary is a row's line for one version.
func webdavSnapshotSummary(s webdavSnapshot) string {
	when := s.At.Local().Format("2 Jan 2006 15:04")
	if s.At.IsZero() {
		when = "an earlier version"
	}
	return fmt.Sprintf("%s · %s", when, humanBytes(s.Size))
}
