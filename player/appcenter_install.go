package main

// appcenter_install.go — Get, Update, Roll back and Remove for App Center
// apps, on the Packages store's staging and swap (pkgstage.go).
//
// The card's Apps folder is the "packages folder" of pkgstage.go here, and
// an app's folder name is its key:
//
//	Apps/.jukahub-staging/.download-ac-<id>   the verified download
//	Apps/.jukahub-staging/.unpack-<id>/       the archive, unpacked
//	Apps/.jukahub-staging/<Folder>/           the new version's app folder
//	Apps/.jukahub-staging/.swap-<Folder>      the swap journal
//	Apps/<Folder>/                            the installed version
//	Apps/.jukahub-rollback/<Folder>/          the version before it (Roll back)
//	Apps/.jukahub-trash/                      copies being deleted
//
// Every bookkeeping name starts with a dot. MainUI reads only
// Apps/<folder>/config.json, and none of the dot-folders holds one at that
// level; JukaHub's Apps scan (apps.go scanSystemApps) skips dot-folders, so a
// rollback copy is never listed as a second app.
//
// An install: GitHub's releases API names the newest release (of the tag
// family the entry names) and the asset matching the entry's pattern; the
// download is checked against the SHA-256 GitHub publishes for the asset when
// it publishes one (pkgDownloadTo), unpacked through os.Root (pkgExtractZip:
// no links, no paths that climb out, bounded size), and the archive must hold
// exactly one app folder (config.json and a launch script inside it,
// scanAppSubdir). Only then is anything on the card touched: the new folder
// is swapped in by rename, the old one becomes the rollback copy, and the
// files the user or the app wrote into the old folder (settings, saves) are
// moved into the new one (pkgCarryOver). A swap that fails is undone; one cut
// off by a power loss is finished or undone by the recovery pass, which the
// first scan of the Apps folder starts (acRecoverOnce).
//
// An app installed by hand is never changed unless Update or Remove is
// pressed on it, and the sheet says so before it is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// acInstallTimeout bounds one install on handheld Wi-Fi.
	acInstallTimeout = 20 * time.Minute
	// acMaxAppBytes bounds one unpacked app.
	acMaxAppBytes = int64(768 << 20)
	// acLatestAge is how long a looked-up latest release is reused: GitHub
	// allows 60 unsigned API calls an hour.
	acLatestAge = 30 * time.Minute
)

// acBusy is the app an install, rollback or removal is working on ("" when
// idle). One runs at a time.
var acBusy atomic.Value

func acBusyID() string {
	s, _ := acBusy.Load().(string)
	return s
}

var acRecover struct {
	once sync.Once
	// done closes when the pass is over: a change waits for it, since the
	// pass deletes whatever it finds in staging, a running download too.
	done chan struct{}
}

func init() { acRecover.done = make(chan struct{}) }

// acRecoverOnce finishes or undoes an install, rollback or removal that a
// power cut or a crash stopped, once per run, on a goroutine (the trash can
// be large), then scans again.
func acRecoverOnce(root string) {
	if root == "" {
		return
	}
	acRecover.once.Do(func() {
		go func() {
			defer close(acRecover.done)
			if !pkgIsDir(filepath.Join(root, pkgStagingDir)) && !pkgIsDir(filepath.Join(root, pkgRollbackDir)) && !pkgIsDir(filepath.Join(root, pkgTrashDir)) {
				return
			}
			pkgRecoverInterrupted(root)
			acRescanNow()
		}()
	})
}

// acRecoverWait starts the pass if nothing has and waits for it (a change's
// goroutine).
func acRecoverWait(root string) {
	acRecoverOnce(root)
	if root != "" {
		<-acRecover.done
	}
}

// ── Releases ─────────────────────────────────────────────────────────────

// acLatest is the newest release found for an entry.
type acLatest struct {
	Tag   string
	Asset githubAsset
	Err   string
	At    time.Time
}

var acLatestCache struct {
	mu   sync.Mutex
	byID map[string]acLatest
	busy map[string]bool
}

// acLatestFor is the cached answer for an entry; ok is false when there is
// none yet (acLookupLatest fills it).
func acLatestFor(id string) (acLatest, bool) {
	acLatestCache.mu.Lock()
	defer acLatestCache.mu.Unlock()
	l, ok := acLatestCache.byID[id]
	return l, ok && l.Err == ""
}

// acLookupLatest asks GitHub for every downloadable entry's newest release
// whose answer is missing or old, on goroutines, and calls done on the main
// loop when each lands.
func acLookupLatest(done func()) {
	if !IsOnline() {
		return
	}
	for i := range acCatalogue {
		a := &acCatalogue[i]
		if !a.Source.downloadable() {
			continue
		}
		acLatestCache.mu.Lock()
		if acLatestCache.byID == nil {
			acLatestCache.byID = map[string]acLatest{}
			acLatestCache.busy = map[string]bool{}
		}
		cur, have := acLatestCache.byID[a.ID]
		if acLatestCache.busy[a.ID] || (have && time.Since(cur.At) < acLatestAge) {
			acLatestCache.mu.Unlock()
			continue
		}
		acLatestCache.busy[a.ID] = true
		acLatestCache.mu.Unlock()
		go func(a *acApp) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rel, asset, err := acResolveRelease(ctx, a)
			l := acLatest{Tag: rel.TagName, Asset: asset, At: time.Now()}
			if err != nil {
				l.Err = err.Error()
				logDebug("appcenter", "%s: latest release: %v", a.ID, err)
			}
			acLatestCache.mu.Lock()
			acLatestCache.byID[a.ID] = l
			acLatestCache.busy[a.ID] = false
			acLatestCache.mu.Unlock()
			if done != nil {
				tbxPost(done)
			}
		}(a)
	}
}

// acResolveRelease finds the release an install takes: GitHub's latest, or
// the newest of the entry's tag family, and in it the asset matching the
// entry's pattern.
func acResolveRelease(ctx context.Context, a *acApp) (githubRelease, githubAsset, error) {
	src := a.Source
	if !src.downloadable() {
		return githubRelease{}, githubAsset{}, errors.New("the App Center has no download for it")
	}
	client := emuWinClient()
	base := standaloneAPIBase + "/repos/" + src.Owner + "/" + src.Repo
	var releases []githubRelease
	if src.TagPrefix == "" {
		data, err := fetchUpdateBytes(ctx, client, base+"/releases/latest", patchMaxMetadata)
		if err == nil {
			var rel githubRelease
			if jerr := json.Unmarshal(data, &rel); jerr != nil {
				return githubRelease{}, githubAsset{}, errors.New("GitHub's answer could not be read")
			}
			releases = append(releases, rel)
		} else if ctx.Err() != nil {
			return githubRelease{}, githubAsset{}, ctx.Err()
		}
	}
	if len(releases) == 0 {
		data, err := fetchUpdateBytes(ctx, client, base+"/releases?per_page=30", patchMaxMetadata)
		if err != nil {
			return githubRelease{}, githubAsset{}, err
		}
		if jerr := json.Unmarshal(data, &releases); jerr != nil {
			return githubRelease{}, githubAsset{}, errors.New("GitHub's release list could not be read")
		}
	}
	for _, rel := range releases {
		if rel.Prerelease {
			continue
		}
		if src.TagPrefix != "" && !strings.HasPrefix(strings.ToLower(rel.TagName), strings.ToLower(src.TagPrefix)) {
			continue
		}
		switch asset, n := acPickAsset(rel.Assets, src.Asset); {
		case n > 1:
			// "Its one zip": a release with two candidates is not guessed at.
			return rel, githubAsset{}, fmt.Errorf("release %s has %d downloads matching %s; the App Center does not guess which one is the app", rel.TagName, n, src.Asset)
		case n == 1:
			return rel, asset, nil
		}
		if src.TagPrefix == "" {
			return rel, githubAsset{}, fmt.Errorf("release %s has no %s", rel.TagName, src.Asset)
		}
	}
	return githubRelease{}, githubAsset{}, fmt.Errorf("no release of %s/%s carries %s", src.Owner, src.Repo, src.Asset)
}

// acPickAsset is the zip asset whose lower-case name matches pattern, and
// how many match: the caller refuses more than one.
func acPickAsset(assets []githubAsset, pattern string) (githubAsset, int) {
	pattern = strings.ToLower(pattern)
	var found githubAsset
	n := 0
	for _, as := range assets {
		name := strings.ToLower(as.Name)
		if !strings.HasSuffix(name, ".zip") {
			continue
		}
		if ok, err := path.Match(pattern, name); err == nil && ok {
			if n == 0 {
				found = as
			}
			n++
		}
	}
	return found, n
}

// acAssetFromRepo reports whether an asset's download URL is the catalogue
// repository's own release download, https://github.com/<Owner>/<Repo>/
// releases/download/…, so an answer from the API cannot point the installer
// anywhere else. GitHub then redirects to its file host; only this first URL
// is checked.
func acAssetFromRepo(src acSource, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || !strings.EqualFold(u.Host, "github.com") {
		return false
	}
	if u.Path == "" || path.Clean(u.Path) != u.Path {
		return false
	}
	prefix := "/" + strings.ToLower(src.Owner) + "/" + strings.ToLower(src.Repo) + "/releases/download/"
	return strings.HasPrefix(strings.ToLower(u.Path), prefix)
}

// ── Install ──────────────────────────────────────────────────────────────

// acInstallResult is what one install did.
type acInstallResult struct {
	Folder   string
	Tag      string
	Verified bool
	Kept     bool
	Carried  int
	Aside    string
	// Renamed is the archive's own folder name when the app went in under the
	// entry's folder instead.
	Renamed string
}

// acInstall fetches the newest release of a and swaps it in. progress may be
// nil. Off the main thread.
func acInstall(ctx context.Context, a *acApp, phase func(string), progress func(done, total int64)) (acInstallResult, error) {
	var res acInstallResult
	root := acAppsRoot()
	if root == "" {
		return res, errors.New("this handheld has no Apps folder")
	}
	if phase != nil {
		phase(installPhaseResolving)
	}
	rel, asset, err := acResolveRelease(ctx, a)
	if err != nil {
		return res, err
	}
	if err := validatePatchURL(asset.BrowserDownloadURL); err != nil {
		return res, err
	}
	if !acAssetFromRepo(a.Source, asset.BrowserDownloadURL) {
		return res, fmt.Errorf("the download is not a release file of %s/%s; not installed", a.Source.Owner, a.Source.Repo)
	}
	res.Tag = rel.TagName
	stageRoot := filepath.Join(root, pkgStagingDir)
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return res, err
	}
	dl := filepath.Join(stageRoot, pkgDownloadPrefix+"ac-"+a.ID)
	unpack := filepath.Join(stageRoot, ".unpack-"+a.ID)
	_ = os.RemoveAll(unpack)
	defer os.Remove(dl)
	defer os.RemoveAll(unpack)

	if phase != nil {
		phase(installPhaseDownload)
	}
	want := Package{Name: a.Name, Version: rel.TagName}
	// The size GitHub publishes bounds the download (and must match it), with
	// or without a checksum.
	if asset.Size > 0 {
		want.DownloadSize = asset.Size
	}
	if d := strings.ToLower(strings.TrimSpace(asset.Digest)); d != "" {
		// GitHub publishes "sha256:<hex>"; any other algorithm is not checked
		// here, and a sha256 value that is not one refuses the install.
		if sum, isSHA := strings.CutPrefix(d, "sha256:"); isSHA {
			if len(sum) != 64 || !isHex(sum) {
				return res, errors.New("GitHub publishes a checksum for the download that is not a SHA-256")
			}
			want.SHA256 = sum
		}
	}
	got, err := pkgDownloadTo(ctx, asset.BrowserDownloadURL, dl, want, progress)
	if err != nil {
		return res, err
	}
	if !got.Zip {
		return res, errors.New("the download is not a zip archive")
	}
	res.Verified = got.Verified
	if phase != nil {
		phase(installPhaseExtract)
	}
	files, err := pkgExtractZip(ctx, dl, unpack)
	if err != nil {
		return res, err
	}
	appRoot, err := acFindAppRoot(unpack)
	if err != nil {
		return res, err
	}
	if err := acValidateApp(appRoot); err != nil {
		return res, err
	}

	// The folder it goes into: the installed copy's when there is one (an
	// update keeps the folder MainUI and the user know); for a fresh install
	// the archive's own folder name, kept exactly, unless it is a versioned
	// archive name ("trimui-drop-1.1.0"), a name the installer cannot keep
	// safe, or there is none: then the entry's folder, and the toast says so.
	// A name is never quietly altered (a launch script may name its own
	// folder): without an entry folder to use, such a download is refused.
	key := ""
	if in, ok := acInstalledFor(a.ID); ok {
		key = in.Folder
		if key != packageDirName(key) {
			return res, fmt.Errorf("its folder %q has characters the installer cannot keep safe — update it by hand", key)
		}
	} else {
		orig := ""
		if appRoot != unpack {
			orig = filepath.Base(appRoot)
		}
		switch {
		case orig != "" && acFolderKnown(a, orig):
			key = orig
		case orig != "" && !acVersionedRE.MatchString(orig) && orig == packageDirName(orig):
			key = orig
		case len(a.Folders) > 0:
			key = a.Folders[0]
		case orig != "":
			key = orig
		default:
			key = a.Name
		}
		if key != packageDirName(key) {
			return res, fmt.Errorf("the app's folder name %q has characters the installer cannot keep safe; install it by hand", key)
		}
		if orig != "" && !strings.EqualFold(orig, key) {
			res.Renamed = orig
			logInfo("appcenter", "%s: the download's folder %q goes in as Apps/%s, the folder its source names", a.ID, orig, key)
		}
		// A fresh install never takes a folder that holds JukaHub or another
		// app: only a copy of this same app may be replaced.
		live := filepath.Join(root, key)
		if isSelfAppFolder(live) {
			return res, fmt.Errorf("the download would replace JukaHub's own folder (Apps/%s); not installed", key)
		}
		if pkgExists(live) {
			entry, ok := scanAppSubdir(live)
			if !ok || acMatchApp(key, entry.Name) != a.ID {
				return res, fmt.Errorf("Apps/%s already holds something else — move or rename it first", key)
			}
		}
	}
	paths := pkgPathsFor(root, key)
	res.Folder = key

	// This app's own earlier swap, if one stopped halfway, is put right first.
	pkgOpMu.Lock()
	rerr := pkgRecoverKeyLocked(root, key)
	if rerr == nil {
		_ = os.RemoveAll(paths.stage)
	}
	pkgOpMu.Unlock()
	if rerr != nil {
		return res, rerr
	}
	if err := pkgRename(appRoot, paths.stage); err != nil {
		return res, fmt.Errorf("prepare the new version: %w", err)
	}
	own := acFilesUnder(files, unpack, appRoot)
	own = append(own, acMarkerFile)
	// A zip made on Windows carries no execute bits; a card that keeps them
	// (ext4) would refuse the launch script.
	godotMarkRunnable(paths.stage)
	bootSyncFilesystems()

	pkgOpMu.Lock()
	defer pkgOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		pkgDiscardStage(paths.stage)
		return res, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := acWriteMarker(paths.stage, acMarker{
		AppID: a.ID, Tag: rel.TagName, Asset: asset.Name, SHA256: got.SHA256, Verified: got.Verified,
		Source: a.Source.Owner + "/" + a.Source.Repo, InstalledAt: now,
	}); err != nil {
		pkgDiscardStage(paths.stage)
		return res, err
	}
	marker := pkgMarker{
		Name: a.Name, Version: rel.TagName, SHA256: got.SHA256, Verified: got.Verified,
		InstalledAt: now, Origin: pkgOriginInstall, Main: pkgDefaultMain, Listed: true, Files: own,
		CarryPending: pkgIsDir(paths.live) || (pkgIsDir(paths.back) && pkgExists(paths.journal)),
	}
	if err := pkgWriteMarker(paths.stage, marker); err != nil {
		pkgDiscardStage(paths.stage)
		return res, err
	}
	swap, err := pkgSwapIn(paths)
	if err != nil {
		if !swap.Stranded {
			pkgDiscardStage(paths.stage)
		}
		res.Aside = swap.Aside
		return res, err
	}
	res.Kept, res.Aside = swap.Kept, swap.Aside
	if swap.Kept {
		moved, cerr := pkgCarryOver(paths)
		res.Carried = moved
		if cerr != nil {
			logWarn("appcenter", "%s: carrying your files into the new version: %v", a.ID, cerr)
		}
	}
	// The new folder must still read as an app now that it is in place; if
	// it does not, the old version goes back at once.
	if _, ok := scanAppSubdir(paths.live); !ok && swap.Kept {
		logWarn("appcenter", "%s: the new version does not read as an app in place; restoring the previous one", a.ID)
		if rerr := acRollBackLocked(root, key); rerr != nil {
			return res, fmt.Errorf("the new version does not start, and the previous one could not be restored: %w", rerr)
		}
		// The broken version is now the rollback copy: once your files are
		// back in the restored one, it goes, so Roll back never offers it.
		if lm, ok := pkgReadMarker(paths.live); ok && !lm.CarryPending && pkgIsDir(paths.back) {
			if _, terr := pkgTrash(root, paths.back); terr != nil {
				logWarn("appcenter", "%s: deleting the version that failed: %v", a.ID, terr)
			}
		}
		return res, errors.New("the new version does not read as an app; the previous version is back")
	}
	return res, nil
}

// acVersionedRE finds a version in a folder name ("drop-1.1.0", "v2.0").
var acVersionedRE = regexp.MustCompile(`\d+\.\d+`)

// acFolderKnown reports whether folder is one of the entry's own names.
func acFolderKnown(a *acApp, folder string) bool {
	for _, f := range a.Folders {
		if strings.EqualFold(f, folder) {
			return true
		}
	}
	return false
}

// acFilesUnder turns the archive's file list (slash paths below unpack) into
// the app folder's own list (slash paths below appRoot).
func acFilesUnder(files []string, unpack, appRoot string) []string {
	rel, err := filepath.Rel(unpack, appRoot)
	if err != nil {
		return nil
	}
	prefix := ""
	if rel != "." {
		prefix = filepath.ToSlash(rel) + "/"
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if prefix == "" {
			out = append(out, f)
			continue
		}
		if strings.HasPrefix(f, prefix) {
			out = append(out, strings.TrimPrefix(f, prefix))
		}
	}
	return out
}

// errAcLayout is an archive that does not hold one app folder.
var errAcLayout = errors.New("the download does not hold a TrimUI app folder (config.json and a launch script)")

// acFindAppRoot is the one app folder in an unpacked archive: its top, a
// folder below it, or Apps/<Folder> (an archive laid out like the card). Two
// app folders are refused: the installer would have to guess.
func acFindAppRoot(unpack string) (string, error) {
	if _, ok := scanAppSubdir(unpack); ok {
		return unpack, nil
	}
	var found []string
	look := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || skipScanEntryName(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if _, ok := scanAppSubdir(p); ok {
				found = append(found, p)
			}
		}
	}
	look(unpack)
	if len(found) == 0 {
		// One wrapper folder, or the card's own Apps folder.
		if entries, err := os.ReadDir(unpack); err == nil {
			for _, e := range entries {
				if e.IsDir() && !skipScanEntryName(e.Name()) {
					look(filepath.Join(unpack, e.Name()))
					if apps := filepath.Join(unpack, e.Name(), "Apps"); pkgIsDir(apps) {
						look(apps)
					}
				}
			}
		}
	}
	switch len(found) {
	case 0:
		return "", errAcLayout
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("the download holds %d apps; the App Center installs one at a time", len(found))
}

// acValidateApp checks an unpacked app folder before anything on the card
// changes: config.json and its launch script inside the folder (scanAppSubdir
// refuses a launch path that climbs out), only regular files and folders, and
// no more than acMaxAppBytes.
func acValidateApp(dir string) error {
	if _, ok := scanAppSubdir(dir); !ok {
		return errAcLayout
	}
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("the app holds a special file (%s)", d.Name())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > acMaxAppBytes {
			return fmt.Errorf("the app unpacks to more than %d MiB", acMaxAppBytes>>20)
		}
		return nil
	})
	return err
}

// acWriteMarker writes the App Center's record into an app folder.
func acWriteMarker(dir string, m acMarker) error {
	m.Schema = 1
	m.InstalledBy = "JukaHub App Center"
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileSynced(filepath.Join(dir, acMarkerFile), data, 0o644)
}

// ── Roll back and remove ─────────────────────────────────────────────────

// acRollBack swaps the kept version of the app in folder back in; the
// version it replaces becomes the kept one.
func acRollBack(folder string) error {
	root := acAppsRoot()
	if root == "" {
		return errors.New("this handheld has no Apps folder")
	}
	if folder != packageDirName(folder) {
		return errors.New("its folder name has characters the installer cannot keep safe")
	}
	// Not pkgRollBack: that one first runs the Packages store's recovery
	// pass on the Packages folder. The Apps folder's own pass is
	// acRecoverOnce (acRunChange waits for it); here only this app's earlier
	// swap is put right, as an install does.
	pkgOpMu.Lock()
	err := pkgRecoverKeyLocked(root, folder)
	if err == nil {
		err = os.MkdirAll(filepath.Join(root, pkgStagingDir), 0o755)
	}
	if err == nil {
		err = acRollBackLocked(root, folder)
	}
	pkgOpMu.Unlock()
	pkgEmptyTrash(root)
	return err
}

// acRollBackLocked swaps the rollback copy in for a caller that holds pkgOpMu
// (acRollBack, and a failed install putting the old version back).
func acRollBackLocked(root, key string) error {
	paths := pkgPathsFor(root, key)
	if !pkgIsDir(paths.back) {
		return errPkgNoRollback
	}
	m, ok := pkgReadMarker(paths.back)
	if !ok {
		m = pkgMarker{Name: key}
	}
	m.Origin = pkgOriginRollback
	m.CarryPending, m.CarryFailed = pkgIsDir(paths.live), 0
	if err := pkgWriteMarker(paths.back, m); err != nil {
		return err
	}
	if err := pkgRename(paths.back, paths.stage); err != nil {
		return fmt.Errorf("prepare the previous version: %w", err)
	}
	swap, err := pkgSwapIn(paths)
	if err != nil {
		// Unless the swap is stranded (then recovery finishes it), the copy
		// goes back to its slot.
		if !swap.Stranded && pkgIsDir(paths.stage) && !pkgIsDir(paths.back) {
			if rerr := pkgRename(paths.stage, paths.back); rerr != nil {
				logError("appcenter", "%s: the previous version could not be put back (%v); it is in %s", key, rerr, paths.stage)
			}
		}
		return err
	}
	if swap.Kept {
		if _, cerr := pkgCarryOver(paths); cerr != nil {
			logWarn("appcenter", "%s: carrying your files into the restored version: %v", key, cerr)
		}
	}
	return nil
}

// acRemove moves the app folder, its kept version and any copy kept aside
// into the trash, then deletes the trash.
func acRemove(folder string) error {
	root := acAppsRoot()
	if root == "" {
		return errors.New("this handheld has no Apps folder")
	}
	if folder == "" || folder != filepath.Base(folder) || skipScanEntryName(folder) {
		return errors.New("not an app folder")
	}
	live := filepath.Join(root, folder)
	if isSelfAppFolder(live) {
		return errors.New("JukaHub does not remove itself")
	}
	pkgOpMu.Lock()
	var first error
	targets := []string{live}
	if folder == packageDirName(folder) {
		paths := pkgPathsFor(root, folder)
		targets = append(targets, paths.back, paths.stage)
		targets = append(targets, pkgKeptCopies(root, folder)...)
		_ = os.Remove(paths.journal)
	}
	for _, t := range targets {
		if !pkgExists(t) {
			continue
		}
		if _, err := pkgTrash(root, t); err != nil && first == nil {
			first = err
		}
	}
	pkgOpMu.Unlock()
	pkgEmptyTrash(root)
	return first
}

// ── Running it from the page ─────────────────────────────────────────────

// acRunChange runs one change (install, update, rollback, removal) off the
// main thread with the install bar, one at a time, and reports through a
// toast. name is the app's; label is the card's title for anything but a
// first install ("Updating Ritder", "Removing Ritder"), "" for Get, whose
// card reads "Installing <name>" / "<name> installed" by itself.
func acRunChange(id, name, label string, needNet bool, fn func(ctx context.Context, phase func(string), progress func(done, total int64)) (string, error)) {
	if !acHandheld() {
		ShowToast(acBrowseOnlyText(), ToastKindInfo)
		return
	}
	if needNet && !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	if acBusyID() != "" {
		ShowToast("The App Center is busy — one moment", ToastKindInfo)
		return
	}
	acBusy.Store(id)
	title := label
	var run uint64
	var ctx context.Context
	if label == "" {
		title = "Installing " + name
		run, ctx = installBarBeginRun(name)
	} else {
		run, ctx = installBarBeginRunLabelled(name, label)
	}
	cur := installPhaseResolving
	if !needNet {
		// A removal or a rollback downloads nothing: no "Resolving download".
		cur = ""
		installBarUpdateRun(run, cur, 0, 0)
	}
	go func() {
		ctx, cancel := context.WithTimeout(ctx, acInstallTimeout)
		defer cancel()
		root := acAppsRoot()
		acRecoverWait(root)
		var mu sync.Mutex
		phase := func(p string) {
			mu.Lock()
			cur = p
			mu.Unlock()
			installBarUpdateRun(run, p, 0, 0)
		}
		progress := func(done, total int64) {
			mu.Lock()
			p := cur
			mu.Unlock()
			installBarUpdateRun(run, p, done, total)
		}
		msg, err := fn(ctx, phase, progress)
		// The copies a swap replaced (an old rollback copy, a failed new
		// version) are deleted now, not at the next start.
		pkgEmptyTrash(root)
		acRescanNow()
		tbxPost(func() {
			acBusy.Store("")
			acScreen.builtAt = time.Time{}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					installBarAbortRun(run)
					ShowToast(title+" cancelled", ToastKindInfo)
					return
				}
				installBarFailRun(run, installCardDetail(err.Error()))
				ShowToast(title+": "+plainNetworkError(err), ToastKindError)
				logWarn("appcenter", "%s: %v", title, err)
				return
			}
			installBarDoneRun(run)
			PlaySuccessSound()
			ShowToast(msg, ToastKindSuccess)
		})
	}()
}

// acStartInstall is Get and Update.
func acStartInstall(a *acApp) {
	if !a.Source.downloadable() {
		ShowToast(a.Name+" has no download here — "+acWhereToGet(a), ToastKindInfo)
		return
	}
	label := ""
	if _, ok := acInstalledFor(a.ID); ok {
		label = "Updating " + a.Name
	}
	acRunChange(a.ID, a.Name, label, true, func(ctx context.Context, phase func(string), progress func(done, total int64)) (string, error) {
		res, err := acInstall(ctx, a, phase, progress)
		if err != nil {
			return "", err
		}
		logInfo("appcenter", "%s %s installed in Apps/%s (verified %v, kept previous %v, %d of your files carried)",
			a.ID, res.Tag, res.Folder, res.Verified, res.Kept, res.Carried)
		msg := a.Name + " " + res.Tag + " is installed"
		if res.Renamed != "" {
			msg += " in Apps/" + res.Folder + " (the download calls its folder " + res.Renamed + ")"
		}
		if res.Kept {
			msg += " · the previous version is kept for Roll back"
		}
		if res.Aside != "" {
			msg += " · older files kept in Apps/" + pkgRollbackDir
		}
		if !res.Verified {
			msg += " · GitHub publishes no checksum for it"
		}
		return msg, nil
	})
}

// acWhereToGet is the sentence for an entry the App Center cannot download.
func acWhereToGet(a *acApp) string {
	if a.Source.Bundled != "" {
		return "it comes with " + a.Source.Bundled
	}
	return "install it by hand from " + a.Source.URL
}
