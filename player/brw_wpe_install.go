package main

// brw_wpe_install.go — Get WPE Browser and Update WPE Browser.
//
// WPE Browser is released on GitHub (matweew/wpe-tsp) as one zip, WPE-<ver>.zip,
// meant to be unpacked so the app sits at Apps/WPE on the card; an update is
// the same zip unpacked over the old folder, and its settings.conf survives
// because the package ships settings.default.conf instead. JukaHub does the
// same, but the way it installs everything else:
//
//   - the release comes from GitHub's releases/latest, through the download
//     allowlist (emulatorUpdateValidateURL), and its zip must carry the sha256
//     digest GitHub publishes for every asset: no digest, no install;
//   - the card needs room for the zip and the unpacked app (three times the
//     zip, plus a margin) before anything is written;
//   - the zip — a few hundred megabytes — streams to a file on the card, never
//     into memory, with progress on the install bar, the digest computed as the
//     bytes arrive, a stall watchdog, and resume: a dropped connection
//     continues from the bytes already on the card next time;
//   - it unpacks with the patch engine's checked extractor (no path escapes,
//     no links, bounded size) into a staging folder beside Apps, then the folder
//     that holds launch.sh — whether the zip has Apps/WPE/, WPE/ or the files
//     at its root — replaces Apps/WPE. On an update settings.conf and every
//     file the user or WPE made are carried into the new folder, and the old
//     folder is kept until the new one is in place and the files are across,
//     so a failure part-way leaves the old app where it was, and a carry cut
//     short is finished by the next install;
//   - the installed tag goes into Apps/WPE/.jukahub-release, and the list of
//     files the package brought into .jukahub-files, so the next update can
//     tell a file the release dropped from one the user made.
//
// One install runs at a time (the patch slot), on its own goroutine, and the
// swap waits until no game — and no WPE — owns the screen.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	brwWpeRepo         = "matweew/wpe-tsp"
	brwWpeStageDir     = ".wpe-install"
	brwWpeFilesFile    = ".jukahub-files"
	brwWpeSettingsFile = "settings.conf"
	brwWpeMaxDownload  = int64(768 << 20)
	brwWpeMaxUnpacked  = int64(2 << 30)
	brwWpeMaxEntries   = 40000
	brwWpeMaxManifest  = int64(4 << 20)
	brwWpeSpaceMargin  = int64(64 << 20)
	brwWpeStallLimit   = 90 * time.Second
	brwWpeStallTick    = 5 * time.Second
	brwWpeCheckEvery   = 6 * time.Hour
	brwWpeDownloadTime = 60 * time.Minute
	brwWpeUnpackTime   = 20 * time.Minute
)

var (
	// brwWpeReleaseURL is GitHub's latest release of WPE Browser; a variable
	// so tests can point it at a local server.
	brwWpeReleaseURL = "https://api.github.com/repos/" + brwWpeRepo + "/releases/latest"
	// brwWpeInstalling is set while a Get or Update runs, brwWpeSwapping while
	// it unpacks and replaces the folder (brwWpeGate orders it against a
	// launch).
	brwWpeInstalling atomic.Bool
	brwWpeSwapping   atomic.Bool
	// errBrwWpeUpToDate ends an Update that found nothing newer.
	errBrwWpeUpToDate = errors.New("WPE Browser is up to date")
	// errBrwWpeParked marks a swap that could neither finish nor undo
	// itself: the old app, or the user's files of it not carried yet, are
	// still in the stage's WPE.old, so nothing there may be deleted (the next
	// install moves it back or finishes the carry first).
	errBrwWpeParked = errors.New("the old WPE Browser is parked in the install folder")
)

// brwWpeAsset is one file of a GitHub release, as the API describes it.
type brwWpeAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// brwWpeRelease is the release to install: its tag and its zip.
type brwWpeRelease struct {
	Tag   string
	Asset brwWpeAsset
}

// brwWpePickAsset chooses the package among a release's assets: a .zip, the
// one whose name says wpe (or tsp) when there are several.
func brwWpePickAsset(assets []brwWpeAsset) (brwWpeAsset, bool) {
	best, bestScore := brwWpeAsset{}, -1
	for _, a := range assets {
		name := strings.ToLower(strings.TrimSpace(a.Name))
		if !strings.HasSuffix(name, ".zip") || strings.TrimSpace(a.URL) == "" {
			continue
		}
		score := 0
		if strings.Contains(name, "wpe") {
			score += 2
		}
		if strings.Contains(name, "tsp") {
			score++
		}
		if score > bestScore {
			best, bestScore = a, score
		}
	}
	return best, bestScore >= 0
}

// brwWpeDigestHex is the hex sha256 out of GitHub's "sha256:<hex>" digest, or
// an error for anything else: an asset that cannot be verified is not
// installed.
func brwWpeDigestHex(digest string) (string, error) {
	d := strings.TrimSpace(digest)
	const prefix = "sha256:"
	if len(d) <= len(prefix) || !strings.EqualFold(d[:len(prefix)], prefix) {
		return "", fmt.Errorf("no sha256 checksum is published for it")
	}
	sum := strings.ToLower(d[len(prefix):])
	if len(sum) != sha256.Size*2 || !isHex(sum) {
		return "", fmt.Errorf("its checksum %q is not a sha256", digest)
	}
	return sum, nil
}

// brwWpeParseRelease reads GitHub's release JSON and picks what to install,
// refusing a release without a tag, a zip, a sha256 digest, a sane size or an
// allowed download host.
func brwWpeParseRelease(data []byte) (brwWpeRelease, error) {
	var raw struct {
		TagName string        `json:"tag_name"`
		Assets  []brwWpeAsset `json:"assets"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return brwWpeRelease{}, errors.New("GitHub's release information was unreadable")
	}
	tag := strings.TrimSpace(raw.TagName)
	if !brwWpeValidTag(tag) {
		return brwWpeRelease{}, errors.New("the release has no usable version tag")
	}
	asset, ok := brwWpePickAsset(raw.Assets)
	if !ok {
		return brwWpeRelease{}, fmt.Errorf("release %s has no .zip package", tag)
	}
	if _, err := brwWpeDigestHex(asset.Digest); err != nil {
		return brwWpeRelease{}, fmt.Errorf("release %s can't be verified: %v", tag, err)
	}
	if asset.Size <= 0 || asset.Size > brwWpeMaxDownload {
		return brwWpeRelease{}, fmt.Errorf("release %s has a package of unexpected size (%d bytes)", tag, asset.Size)
	}
	if err := emulatorUpdateValidateURL(asset.URL); err != nil {
		return brwWpeRelease{}, err
	}
	return brwWpeRelease{Tag: tag, Asset: asset}, nil
}

// brwWpeFetchRelease asks GitHub for the latest release.
func brwWpeFetchRelease(ctx context.Context, client *http.Client) (brwWpeRelease, error) {
	data, err := fetchUpdateBytes(ctx, client, brwWpeReleaseURL, patchMaxMetadata)
	if err != nil {
		return brwWpeRelease{}, err
	}
	return brwWpeParseRelease(data)
}

// brwWpeHTTPClient is the emulator updates' client (every redirect checked
// against the allowlist) without its overall deadline, which a slow handheld
// link would hit half-way through a few hundred megabytes: each call bounds
// itself with a context, and the download with its stall watchdog too. It
// trusts the certificate bundle JukaHub carries when the firmware has none.
func brwWpeHTTPClient() *http.Client {
	client := newEmulatorUpdateClient()
	client.Timeout = 0
	client.Transport = ytTransport()
	return client
}

// brwWpeSpaceNeeded is what the card must have free before a download starts:
// the zip, the unpacked app (about twice the zip) and a margin.
func brwWpeSpaceNeeded(size int64) int64 {
	if size <= 0 {
		return brwWpeSpaceMargin
	}
	return 3*size + brwWpeSpaceMargin
}

// ── Download ─────────────────────────────────────────────────────────────

// brwWpeFileSHA256 hashes a file already on the card.
func brwWpeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// brwWpeResumeOffset feeds the bytes a previous attempt left in part into h
// and returns how many there are. A part that cannot be a prefix of the
// asset — empty, as large as the asset or larger, unreadable — is removed and
// the download starts over.
func brwWpeResumeOffset(part string, total int64, h hash.Hash) int64 {
	info, err := os.Stat(part)
	if err != nil {
		return 0
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() >= total {
		_ = os.Remove(part)
		return 0
	}
	f, err := os.Open(part)
	if err != nil {
		_ = os.Remove(part)
		return 0
	}
	n, err := io.Copy(h, io.LimitReader(f, info.Size()))
	f.Close()
	if err != nil || n != info.Size() {
		h.Reset()
		_ = os.Remove(part)
		return 0
	}
	return n
}

// brwWpeRangeStartsAt reports whether a Content-Range header ("bytes
// 100-999/1000") continues at offset.
func brwWpeRangeStartsAt(contentRange string, offset int64) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(contentRange), "bytes ")
	if !ok {
		return false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	return err == nil && n == offset
}

// brwWpeDownload streams the asset to dest. The bytes go to dest+".part" and
// through sha256 as they arrive; only a file of the asset's exact size and
// digest is renamed to dest. A part left by an earlier attempt is resumed
// with a Range request (a server that ignores it sends the whole file, which
// starts the part over). A transport failure or a cancel keeps the part for
// next time; a wrong size or digest deletes it. A dest already on the card
// with the right digest is used as it is.
func brwWpeDownload(ctx context.Context, client *http.Client, asset brwWpeAsset, dest string, progress func(done, total int64)) error {
	want, err := brwWpeDigestHex(asset.Digest)
	if err != nil {
		return err
	}
	if err := emulatorUpdateValidateURL(asset.URL); err != nil {
		return err
	}
	total := asset.Size
	if total <= 0 || total > brwWpeMaxDownload {
		return fmt.Errorf("unexpected package size (%d bytes)", total)
	}
	if progress == nil {
		progress = func(int64, int64) {}
	}
	if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() && info.Size() == total {
		if sum, err := brwWpeFileSHA256(dest); err == nil && sum == want {
			progress(total, total)
			return nil
		}
		_ = os.Remove(dest)
	}

	part := dest + ".part"
	h := sha256.New()
	offset := brwWpeResumeOffset(part, total, h)

	// The watchdog cancels a transfer that stops moving: a Wi-Fi link that
	// drops without a reset would otherwise hold the install bar for the whole
	// download budget.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// lastMove is when bytes last arrived, as time since started: the
	// monotonic clock, so the handheld setting its clock from the network
	// mid-download is not a stall.
	started := time.Now()
	var lastMove atomic.Int64
	var stalled atomic.Bool
	go func() {
		tick := time.NewTicker(brwWpeStallTick)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if time.Since(started)-time.Duration(lastMove.Load()) > brwWpeStallLimit {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	failed := func(err error) error {
		if stalled.Load() {
			return fmt.Errorf("the download stalled (no data for %d seconds)", int(brwWpeStallLimit/time.Second))
		}
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-WPE/"+versionString(nil))
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return failed(err)
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent && brwWpeRangeStartsAt(resp.Header.Get("Content-Range"), offset):
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		// Fresh, or the server ignored the range: the whole file again.
		offset = 0
		h.Reset()
		flags |= os.O_TRUNC
		if resp.ContentLength > 0 && resp.ContentLength != total {
			_ = os.Remove(part)
			return fmt.Errorf("the server's file is %d bytes, the release says %d", resp.ContentLength, total)
		}
	default:
		// A 416 or a range that starts elsewhere would fail the same way on
		// every retry while the part is there: the next try starts over. Any
		// other answer (a 5xx, a rate limit) says nothing about the part, which
		// stays for the next try like a dropped connection's.
		if offset > 0 && (resp.StatusCode == http.StatusRequestedRangeNotSatisfiable || resp.StatusCode == http.StatusPartialContent) {
			_ = os.Remove(part)
		}
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, brwHostOf(asset.URL))
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return fmt.Errorf("can't write to the SD card: %w", err)
	}
	w := io.MultiWriter(out, h)
	buf := make([]byte, 256<<10)
	done := offset
	progress(done, total)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if done+int64(n) > total {
				out.Close()
				_ = os.Remove(part)
				return errors.New("the download is larger than the release says")
			}
			if _, err := w.Write(buf[:n]); err != nil {
				out.Close()
				return fmt.Errorf("writing to the SD card failed: %w", err)
			}
			done += int64(n)
			lastMove.Store(int64(time.Since(started)))
			progress(done, total)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return failed(readErr)
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("writing to the SD card failed: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("writing to the SD card failed: %w", err)
	}
	if done != total {
		return fmt.Errorf("the download ended early (%s of %s)", formatInstallBytes(done), formatInstallBytes(total))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(part)
		return errors.New("the download doesn't match the release's checksum (it was damaged on the way)")
	}
	return os.Rename(part, dest)
}

// ── Unpacking and the swap ───────────────────────────────────────────────

// brwWpeFindPackage is the folder under root that holds launch.sh: root
// itself, WPE/, Apps/WPE/ or any folder up to three levels down. The shallowest
// wins; of several at one depth, the one named WPE.
func brwWpeFindPackage(root string) (string, error) {
	type candidate struct {
		dir   string
		depth int
	}
	queue := []candidate{{root, 0}}
	var found []candidate
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if regularFile(filepath.Join(c.dir, brwWpeLaunchFile)) {
			found = append(found, c)
			continue
		}
		if c.depth >= 3 {
			continue
		}
		entries, err := os.ReadDir(c.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() && !strings.HasPrefix(name, ".") && name != "__MACOSX" {
				queue = append(queue, candidate{filepath.Join(c.dir, name), c.depth + 1})
			}
		}
	}
	if len(found) == 0 {
		return "", errors.New("the package has no launch.sh")
	}
	var shallowest []candidate
	for _, c := range found {
		if c.depth == found[0].depth {
			shallowest = append(shallowest, c)
		}
	}
	if len(shallowest) == 1 {
		return shallowest[0].dir, nil
	}
	for _, c := range shallowest {
		if strings.EqualFold(filepath.Base(c.dir), brwWpeDirName) {
			return c.dir, nil
		}
	}
	return "", fmt.Errorf("the package holds %d apps, not one", len(shallowest))
}

// brwWpeTreeFiles lists the regular files under root as slash-separated
// relative paths, sorted. Links and anything else that is not a plain file
// are left out: they are neither the package's nor worth carrying.
func brwWpeTreeFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(files)
	return files, err
}

// brwWpeReadManifest is the file list the last JukaHub install recorded, or
// nil when there is none (an install JukaHub did not make).
func brwWpeReadManifest(dir string) map[string]bool {
	f, err := os.Open(filepath.Join(dir, brwWpeFilesFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	set := map[string]bool{}
	scanner := bufio.NewScanner(io.LimitReader(f, brwWpeMaxManifest))
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			set[line] = true
		}
	}
	if scanner.Err() != nil {
		return nil
	}
	return set
}

// brwWpeCarryOver decides whether a file of the old install moves into the new
// one. settings.conf always does (the user's values beat a package default);
// a file the new package also has never does (the new release's copy wins);
// a file the previous JukaHub install brought and this release dropped stays
// behind; everything else — favourites, data WPE wrote, anything the user put
// there — is carried, which is what unpacking over the old folder (WPE's own
// update instructions) would keep. JukaHub's own two marker files are written
// anew.
func brwWpeCarryOver(rel string, inNew bool, oldManifest map[string]bool) bool {
	switch rel {
	case brwWpeReleaseFile, brwWpeFilesFile:
		return false
	case brwWpeSettingsFile:
		return true
	}
	if inNew {
		return false
	}
	return !oldManifest[rel]
}

// brwWpeSwap puts the unpacked package pkg in place at target. With no old
// install it is one rename. On an update the old folder moves aside to oldDir
// and pkg moves in (a failure there moves the old one back), then the files
// brwWpeCarryOver keeps move across from oldDir, and oldDir goes last. The
// user's files never sit in the stage's unpack folder, which the next install
// clears: until they are carried they are in the old folder, and a carry cut
// short (an error, a power cut, JukaHub closed mid-way) is finished by the next
// install (brwWpeRecoverParked) before anything in the stage is deleted. pkg
// and target are on one card, so every move is a rename.
func brwWpeSwap(pkg, target, oldDir string) error {
	info, err := os.Lstat(target)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Rename(pkg, target); err != nil {
			return fmt.Errorf("can't move WPE Browser into Apps: %w", err)
		}
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s is a file, not a folder", target)
	}

	_ = os.RemoveAll(oldDir)
	if err := os.Rename(target, oldDir); err != nil {
		return fmt.Errorf("can't move the old WPE Browser aside: %w", err)
	}
	if err := os.Rename(pkg, target); err != nil {
		if restoreErr := os.Rename(oldDir, target); restoreErr != nil {
			return fmt.Errorf("%w (%s): can't move the new one in (%v) or the old one back (%v)", errBrwWpeParked, oldDir, err, restoreErr)
		}
		return fmt.Errorf("can't move the new WPE Browser in: %w", err)
	}
	if err := brwWpeCarryFrom(oldDir, target); err != nil {
		// The new app is in place; what is not carried yet stays in oldDir
		// for the next install to finish.
		return fmt.Errorf("%w (%s): %v", errBrwWpeParked, oldDir, err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		logWarn("browser", "the old WPE Browser folder was left in %s: %v", oldDir, err)
	}
	return nil
}

// brwWpeCarryFrom moves the files brwWpeCarryOver keeps from the old folder
// into the app now at target. A file the new app has is the new release's
// (settings.conf excepted: the user's copy replaces the package's), so the
// carry can run again over its own half-done result: what it already moved is
// no longer in oldDir, and what it did not move is decided the same way.
func brwWpeCarryFrom(oldDir, target string) error {
	oldManifest := brwWpeReadManifest(oldDir)
	oldFiles, err := brwWpeTreeFiles(oldDir)
	if err != nil {
		return fmt.Errorf("can't read the old WPE Browser: %w", err)
	}
	for _, rel := range oldFiles {
		to := filepath.Join(target, filepath.FromSlash(rel))
		_, statErr := os.Lstat(to)
		if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("can't keep %s: %w", rel, statErr)
		}
		inNew := statErr == nil
		if !brwWpeCarryOver(rel, inNew, oldManifest) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return fmt.Errorf("can't keep %s: %w", rel, err)
		}
		if inNew {
			// settings.conf shipped by the package too: the user's copy wins.
			if err := os.Remove(to); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("can't keep %s: %w", rel, err)
			}
		}
		if err := os.Rename(filepath.Join(oldDir, filepath.FromSlash(rel)), to); err != nil {
			return fmt.Errorf("can't keep %s: %w", rel, err)
		}
	}
	return nil
}

// brwWpeWriteMarkers records the installed release and its files in the app's
// folder. Neither is needed to run WPE, so a failure only logs.
func brwWpeWriteMarkers(target, tag string, files []string) {
	if err := os.WriteFile(filepath.Join(target, brwWpeReleaseFile), []byte(tag+"\n"), 0o644); err != nil {
		logWarn("browser", "can't record the WPE Browser release: %v", err)
	}
	list := strings.Join(files, "\n")
	if list != "" {
		list += "\n"
	}
	if err := os.WriteFile(filepath.Join(target, brwWpeFilesFile), []byte(list), 0o644); err != nil {
		logWarn("browser", "can't record the WPE Browser files: %v", err)
	}
}

// brwWpeInstallArchive unpacks a verified package into stage and puts it in
// place at target, recording tag.
func brwWpeInstallArchive(ctx context.Context, archive, stage, target, tag string) error {
	unpacked := filepath.Join(stage, "unpack")
	if err := os.RemoveAll(unpacked); err != nil {
		return err
	}
	if err := extractZipSafe(ctx, archive, unpacked, brwWpeMaxEntries, brwWpeMaxUnpacked); err != nil {
		return fmt.Errorf("unpacking failed: %w", err)
	}
	pkg, err := brwWpeFindPackage(unpacked)
	if err != nil {
		return err
	}
	files, err := brwWpeTreeFiles(pkg)
	if err != nil {
		return err
	}
	if err := brwWpeSwap(pkg, target, filepath.Join(stage, "WPE.old")); err != nil {
		return err
	}
	// The card's filesystem may not keep permission bits; where it does,
	// launch.sh must be runnable (sh reads it either way).
	if err := os.Chmod(filepath.Join(target, brwWpeLaunchFile), 0o755); err != nil {
		logDebug("browser", "launch.sh permissions unchanged: %v", err)
	}
	brwWpeWriteMarkers(target, tag, files)
	return nil
}

// brwWpePrepareStage makes the staging folder and empties it of everything but
// keep (this release's zip and its part file): an earlier release's leftovers
// would only take space.
func brwWpePrepareStage(stage string, keep ...string) error {
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for _, e := range entries {
		if kept[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// brwWpeWaitForScreen returns once nothing owns the screen, with
// brwWpeSwapping set so no launch can start WPE until the swap is done. It
// waits out a game, and WPE itself.
func brwWpeWaitForScreen(ctx context.Context) error {
	for {
		brwWpeGate.Lock()
		brwWpeSwapping.Store(true)
		busy := gameScreenSuspended()
		if busy {
			brwWpeSwapping.Store(false)
		}
		brwWpeGate.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// brwWpeRunInstall is one Get or Update from start to finish on the install
// goroutine, and returns the tag now installed. ctx is the install bar's run
// token: a cancel from the card stops it at any step.
func brwWpeRunInstall(ctx context.Context, client *http.Client, sdRoot string, update bool) (string, error) {
	target := brwWpeDir(sdRoot)
	if target == "" {
		return "", errors.New("can't find the SD card")
	}
	installBarPhase(installPhaseResolving)
	metaCtx, cancelMeta := context.WithTimeout(ctx, 30*time.Second)
	rel, err := brwWpeFetchRelease(metaCtx, client)
	cancelMeta()
	if err != nil {
		return "", err
	}
	brwWpeNoteLatest(rel.Tag)
	if update && regularFile(filepath.Join(target, brwWpeLaunchFile)) && brwWpeReadTag(target) == rel.Tag {
		return rel.Tag, errBrwWpeUpToDate
	}
	apps := filepath.Dir(target)
	if err := os.MkdirAll(apps, 0o755); err != nil {
		return "", fmt.Errorf("the SD card isn't writable: %w", err)
	}
	if free, err := freeBytes(apps); err == nil {
		if need := brwWpeSpaceNeeded(rel.Asset.Size); free < need {
			return "", fmt.Errorf("not enough space on the SD card: %s needed, %s free", formatInstallBytes(need), formatInstallBytes(free))
		}
	} else {
		logDebug("browser", "free space on %s unknown: %v", apps, err)
	}
	digest, err := brwWpeDigestHex(rel.Asset.Digest)
	if err != nil {
		return "", err
	}
	stage := filepath.Join(apps, brwWpeStageDir)
	if err := brwWpeRecoverParked(target, stage); err != nil {
		return "", err
	}
	// A file where the folder belongs fails the swap (brwWpeSwap): say so
	// before a few hundred megabytes are downloaded for nothing.
	if info, err := os.Lstat(target); err == nil && !info.IsDir() {
		return "", fmt.Errorf("%s is a file, not a folder", target)
	}
	archive := filepath.Join(stage, "wpe-"+digest[:12]+".zip")
	if err := brwWpePrepareStage(stage, filepath.Base(archive), filepath.Base(archive)+".part"); err != nil {
		return "", fmt.Errorf("can't prepare the download folder: %w", err)
	}

	// No detail line while the bytes arrive: the bar shows their count.
	installBarPhase(installPhaseDownload)
	dlCtx, cancelDownload := context.WithTimeout(ctx, brwWpeDownloadTime)
	err = brwWpeDownload(dlCtx, client, rel.Asset, archive, installBarProgress)
	cancelDownload()
	if err != nil {
		return "", err
	}

	if err := brwWpeWaitForScreen(ctx); err != nil {
		return "", err
	}
	defer brwWpeSwapping.Store(false)
	installBarResetProgress()
	installBarPhase(installPhaseExtract)
	installBarDetail("Unpacking " + rel.Tag)
	unpackCtx, cancelUnpack := context.WithTimeout(ctx, brwWpeUnpackTime)
	err = brwWpeInstallArchive(unpackCtx, archive, stage, target, rel.Tag)
	stopped := unpackCtx.Err() != nil
	cancelUnpack()
	// What stays on the card: everything when the old app is parked in the
	// stage; the verified zip when the unpack was cancelled or ran out of
	// time (the next try unpacks it without downloading again); nothing after
	// an install or a failed unpack (the zip is what failed, and the card
	// needs the space back).
	cleanup := stage
	switch {
	case errors.Is(err, errBrwWpeParked):
		cleanup = ""
	case err != nil && stopped:
		cleanup = filepath.Join(stage, "unpack")
	}
	if cleanup != "" {
		if rmErr := os.RemoveAll(cleanup); rmErr != nil {
			logWarn("browser", "can't remove %s: %v", cleanup, rmErr)
		}
	}
	if err != nil {
		return "", err
	}
	return rel.Tag, nil
}

// brwWpeRecoverParked finishes what a swap left in the stage before anything
// there is cleared: an old app parked between the swap's two renames (a power
// cut, or errBrwWpeParked) goes back to Apps/WPE, and with the new app already
// in place the user's files still in the old folder are carried into it
// (brwWpeCarryFrom; after a swap that finished there are none left). The
// stage's WPE.old is only deleted after this.
func brwWpeRecoverParked(target, stage string) error {
	parked := filepath.Join(stage, "WPE.old")
	if info, err := os.Lstat(parked); err != nil || !info.IsDir() {
		return nil
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		if err := os.Rename(parked, target); err != nil {
			return fmt.Errorf("%w (%s): %v", errBrwWpeParked, parked, err)
		}
		logInfo("browser", "moved the parked WPE Browser back to %s", target)
		return nil
	}
	if !regularFile(filepath.Join(target, brwWpeLaunchFile)) {
		return fmt.Errorf("%w (%s): Apps/WPE is in the way, move one of them by hand", errBrwWpeParked, parked)
	}
	if regularFile(filepath.Join(target, brwWpeFilesFile)) {
		// An install that finished writes its file list after the carry, so
		// WPE.old is only what that swap could not delete.
		return nil
	}
	if err := brwWpeCarryFrom(parked, target); err != nil {
		return fmt.Errorf("%w (%s): %v", errBrwWpeParked, parked, err)
	}
	return nil
}

// brwWpeStartInstall gets WPE Browser, or with update checks for a newer
// release and installs it, on a background goroutine with the install bar.
// Main thread.
func brwWpeStartInstall(update bool) {
	s := brwWpeProbe()
	switch {
	case !s.Handheld:
		return
	case s.Dir == "":
		ShowToast("Can't find the SD card to put "+brwWpeName+" on", ToastKindWarning)
		return
	case !update && s.Installed:
		brwWpeInvalidate()
		ShowToast(brwWpeName+" is already installed", ToastKindInfo)
		return
	case !s.Installed && !s.Supported:
		ShowToast(brwWpeUnsupportedNote, ToastKindInfo)
		return
	case gameScreenSuspended():
		return
	case !IsOnline():
		ShowToast(brwWpeName+" downloads from the internet · connect to Wi-Fi first", ToastKindWarning)
		return
	}
	if !brwWpeInstalling.CompareAndSwap(false, true) {
		ShowToast(brwWpeName+" is already downloading", ToastKindInfo)
		return
	}
	if !tryBeginPatchOp() {
		brwWpeInstalling.Store(false)
		ShowToast("Another download is running · try again when it finishes", ToastKindInfo)
		return
	}
	brwWpeInvalidate()
	gen, runCtx := installBarBeginRun(brwWpeName)
	PlayActivateSound()
	if update {
		ShowToast("Checking for a newer "+brwWpeName+"…", ToastKindInfo)
	} else {
		ShowToast("Downloading "+brwWpeName+" · a few hundred MB", ToastKindInfo)
	}
	root := brwWpeSDRootFn()
	go func() {
		defer endPatchOp()
		defer brwWpeInstalling.Store(false)
		defer brwWpeInvalidate()
		defer func() {
			if r := recover(); r != nil {
				logError("browser", "WPE Browser install crashed: %v\n%s", r, debug.Stack())
				installBarFailRun(gen, "The install hit an internal error")
			}
		}()
		// Built here, not on the main thread: the first client may load the
		// certificate bundle from the card.
		tag, err := brwWpeRunInstall(runCtx, brwWpeHTTPClient(), root, update)
		brwWpeReportInstall(runCtx, gen, tag, err)
	}()
}

// brwWpeReportInstall says how the install went, on the bar and in a toast.
func brwWpeReportInstall(ctx context.Context, gen uint64, tag string, err error) {
	switch {
	case errors.Is(err, errBrwWpeUpToDate):
		// Nothing was installed: the card goes, the toast says why.
		installBarAbortRun(gen)
		ShowToast(brwWpeName+" is up to date ("+tag+")", ToastKindSuccess)
	case err != nil && ctx.Err() != nil:
		// Cancelled from the card: the part on the card resumes next time.
		installBarAbortRun(gen)
		ShowToast(brwWpeName+" download stopped · the next try continues where it left off", ToastKindInfo)
	case err != nil:
		reason := firstLine(err.Error())
		logWarn("browser", "WPE Browser install failed: %v", err)
		installBarFailRun(gen, installCardDetail(reason))
		ShowToast("Couldn't install "+brwWpeName+": "+reason, ToastKindError)
	default:
		// The success toast plays the chime itself.
		installBarDoneRun(gen)
		ShowToast(brwWpeName+" "+tag+" is ready · the Browser menu opens pages in it", ToastKindSuccess)
	}
}
