package main

// relpack.go — installing a package that JukaHub's own CI builds and attaches
// to its GitHub releases.
//
// Some pieces JukaHub needs cannot come from anywhere else in a form the
// handheld can run: Box64 has to be built against a glibc no newer than the
// firmware's (the Windows-app runtime, JukaHub-Wine-linux-arm64.zip), and
// AndroidRT has to be cross-built at all (JukaHub-AndroidRT-linux-arm64.zip).
// CI builds them next to the app archives and attaches each as one zip. This
// file finds, checks and installs such a zip into one folder. It is the generic
// half of what psremote_helpers_install.go does for the Remote Play helpers,
// with the same trust model:
//
//   - Which release: the newest JukaHub release, stable before beta, within the
//     first relPackMaxPages pages of the release list, that carries the asset.
//     A release whose build of the package failed has none, so an older one is
//     used: the packages talk to JukaHub through files and environment
//     variables both sides keep compatible.
//   - Trust: HTTPS to the hosts on the Patch allowlist only (newPatchClient and
//     downloadToFile check every URL, redirects included). The archive must
//     match the signed release manifest when the release carries one, else the
//     digest GitHub publishes for the asset; a release whose signed manifest
//     does not list the package is passed over for an older one. Inside, the
//     package's own SHA256SUMS must list every file, and every file must match.
//   - Will it run: spec.Probe runs on the unpacked copy before anything is
//     replaced, so a package this device refuses never displaces one that
//     works.
//   - Install: the unpacked tree is renamed into place as a whole (the old one
//     kept aside until the new one is in), and .jukahub-pack.json records the
//     release, the archive's SHA-256 and when. A power cut between the two
//     renames is repaired the next time the package is read or installed.
//
// Nothing here has UI: callers show relPackProgress.line() and
// relPackFriendly(err), and decide when to install (on a press, or by the
// emulator auto-update pass).

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// relPackPerPage and relPackMaxPages bound the release search: every push
	// to main publishes a release, so a package build broken for a few days
	// pushes the last good package off the first page.
	relPackPerPage  = 20
	relPackMaxPages = 5
	// relPackMaxCandidates is how many releases carrying the package are
	// tried when a signed manifest does not list it.
	relPackMaxCandidates = 3
	// relPackDownloadTries is how often one install starts the download: a
	// download cut off after it made progress is resumed at once.
	relPackDownloadTries = 3
	// relPackInfoName is the provenance file inside an installed package.
	relPackInfoName = ".jukahub-pack.json"
	// relPackSumsName is the package's own checksum list.
	relPackSumsName = "SHA256SUMS"
	// The work folders sit beside the install folder, on the same file
	// system, so the final step is a rename.
	relPackWorkSuffix  = ".jukahub-download"
	relPackStageSuffix = ".jukahub-new"
	relPackOldSuffix   = ".jukahub-old"
	// relPackSpare is the free space kept on top of the archive and what it
	// unpacks to.
	relPackSpare = int64(32 << 20)
)

// relPackReleasesAPI lists JukaHub's releases, newest first, assets included.
var relPackReleasesAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/releases"

// Test seams.
var relPackClientFn = newPatchClient

var (
	errRelPackNotPublished = errors.New("no recent JukaHub release carries this package")
	errRelPackOffline      = errors.New("could not reach GitHub")
	errRelPackCutOff       = errors.New("the download was cut off")
	errRelPackRateLimited  = errors.New("GitHub is refusing requests for now")
	errRelPackClock        = errors.New("a certificate or manifest does not fit this device's date")
	errRelPackTrust        = errors.New("this device cannot check GitHub's certificate")
	errRelPackNoSpace      = errors.New("not enough free space")
	errRelPackDiskWrite    = errors.New("the card refused a write")
	errRelPackUnverified   = errors.New("the download did not verify")
	errRelPackUnlisted     = errors.New("the release's signed manifest does not list the package")
	errRelPackWontRun      = errors.New("it won't run on this device")
	errRelPackBusy         = errors.New("already installing")
	errRelPackUnsupported  = errors.New("not available on this device")
)

// relPackSpec describes one package.
type relPackSpec struct {
	// Key names the package in logs ("wine", "androidrt").
	Key string
	// Asset is the release asset, e.g. "JukaHub-Wine-linux-arm64.zip".
	Asset string
	// Title is how messages name it ("the Windows app runtime").
	Title string
	// MaxAsset bounds the download; MaxUnpacked what it may unpack to.
	MaxAsset    int64
	MaxUnpacked int64
	// Root is the folder inside the zip whose contents become the install
	// folder ("" for the zip's root). SHA256SUMS sits at that root.
	Root string
	// Probe says why the unpacked tree in dir would not run here, "" when it
	// would (or when that cannot be told). nil skips the check.
	Probe func(dir string) string
	// Supported reports whether the package can be used on this device at
	// all; nil means everywhere.
	Supported func() bool
}

// relPackRelease is a release as GitHub's list returns it.
type relPackRelease struct {
	TagName     string        `json:"tag_name"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

// relPackCandidate is a release that carries the package.
type relPackCandidate struct {
	Rel   relPackRelease
	Asset githubAsset
}

// relPackInfo is what .jukahub-pack.json records about an installed package.
type relPackInfo struct {
	Key         string `json:"key"`
	Asset       string `json:"asset"`
	Tag         string `json:"tag"`
	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Published   string `json:"published,omitempty"`
	InstalledAt string `json:"installed_at"`
	Files       int    `json:"files"`
}

// relPackPhase is where an install is.
type relPackPhase int

const (
	relPackLooking relPackPhase = iota
	relPackDownloading
	relPackUnpacking
	relPackChecking
	relPackInstalling
	relPackDone
)

// relPackProgress is one progress report.
type relPackProgress struct {
	Phase relPackPhase
	Got   int64
	Total int64
	Tag   string
}

// line renders a progress report for a status row: "Downloading the Windows
// app runtime · 12 of 40 MB".
func (p relPackProgress) line(title string) string {
	if title == "" {
		title = "the package"
	}
	switch p.Phase {
	case relPackLooking:
		return "Looking for " + title + " on GitHub…"
	case relPackDownloading:
		if p.Total > 0 {
			return fmt.Sprintf("Downloading %s · %s of %s", title, formatInstallBytes(p.Got), formatInstallBytes(p.Total))
		}
		return "Downloading " + title + "…"
	case relPackUnpacking:
		return "Unpacking " + title + "…"
	case relPackChecking:
		return "Checking " + title + "…"
	case relPackInstalling:
		return "Installing " + title + "…"
	}
	return capitalizeFirst(title) + " is installed"
}

// relPackBusy holds the keys of the installs that are running.
var relPackBusy struct {
	mu   sync.Mutex
	keys map[string]bool
}

// relPackBegin claims key for one install; false when one is running.
func relPackBegin(key string) bool {
	relPackBusy.mu.Lock()
	defer relPackBusy.mu.Unlock()
	if relPackBusy.keys == nil {
		relPackBusy.keys = map[string]bool{}
	}
	if relPackBusy.keys[key] {
		return false
	}
	relPackBusy.keys[key] = true
	return true
}

// relPackEnd releases key.
func relPackEnd(key string) {
	relPackBusy.mu.Lock()
	delete(relPackBusy.keys, key)
	relPackBusy.mu.Unlock()
}

// relPackRunning reports whether an install of key is running.
func relPackRunning(key string) bool {
	relPackBusy.mu.Lock()
	defer relPackBusy.mu.Unlock()
	return relPackBusy.keys[key]
}

// ── Reading what is installed ────────────────────────────────────────────

// relPackRead returns the provenance of the package installed in dir. A
// folder left half-swapped by a power cut is put back first.
func relPackRead(dir string) (relPackInfo, bool) {
	if dir == "" {
		return relPackInfo{}, false
	}
	relPackRecover(dir)
	data, err := os.ReadFile(filepath.Join(dir, relPackInfoName))
	if err != nil {
		return relPackInfo{}, false
	}
	var info relPackInfo
	if json.Unmarshal(data, &info) != nil || strings.TrimSpace(info.Tag) == "" {
		return relPackInfo{}, false
	}
	return info, true
}

// relPackRecover repairs an install interrupted between its two renames: the
// install folder is gone but the copy set aside is there, so it goes back.
func relPackRecover(dir string) {
	old := dir + relPackOldSuffix
	if _, err := os.Stat(dir); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if info, err := os.Stat(old); err != nil || !info.IsDir() {
		return
	}
	if err := os.Rename(old, dir); err != nil {
		logWarn("relpack", "could not put %s back after an interrupted install: %v", dir, err)
		return
	}
	logInfo("relpack", "restored %s after an interrupted install", dir)
}

// relPackWriteInfo records info in dir.
func relPackWriteInfo(dir string, info relPackInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, relPackInfoName), append(data, '\n'), 0o644)
}

// ── Finding a release ────────────────────────────────────────────────────

// relPackCandidates lists the releases carrying asset in the order they are
// tried: never a draft, stable before beta, then newest first.
func relPackCandidates(releases []relPackRelease, asset string, maxAsset int64) []relPackCandidate {
	type ranked struct {
		relPackCandidate
		at    time.Time
		dated bool
		order int
	}
	var found []ranked
	for i, r := range releases {
		if r.Draft || strings.TrimSpace(r.TagName) == "" {
			continue
		}
		for _, a := range r.Assets {
			if !strings.EqualFold(strings.TrimSpace(a.Name), asset) {
				continue
			}
			if strings.TrimSpace(a.BrowserDownloadURL) != "" && a.Size >= 0 && (maxAsset <= 0 || a.Size <= maxAsset) {
				at, dated := releasePublishedTime(&githubRelease{TagName: r.TagName, PublishedAt: r.PublishedAt})
				found = append(found, ranked{relPackCandidate: relPackCandidate{Rel: r, Asset: a}, at: at, dated: dated, order: i})
			}
			break
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.Rel.Prerelease != b.Rel.Prerelease {
			return !a.Rel.Prerelease
		}
		if a.dated && b.dated && !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		if a.dated != b.dated {
			return a.dated
		}
		return a.order < b.order
	})
	out := make([]relPackCandidate, len(found))
	for i, f := range found {
		out[i] = f.relPackCandidate
	}
	return out
}

// relPackFind asks GitHub for the releases to install spec from, in the order
// to try them: page by page until one carries the package.
func relPackFind(ctx context.Context, client *http.Client, spec relPackSpec) ([]relPackCandidate, error) {
	sep := "?"
	if strings.Contains(relPackReleasesAPI, "?") {
		sep = "&"
	}
	for page := 1; page <= relPackMaxPages; page++ {
		pageURL := relPackReleasesAPI + sep + "per_page=" + strconv.Itoa(relPackPerPage) + "&page=" + strconv.Itoa(page)
		data, err := fetchBounded(ctx, client, pageURL, patchMaxMetadata)
		if err != nil {
			return nil, relPackNetErr("listing JukaHub releases", err)
		}
		var releases []relPackRelease
		if err := json.Unmarshal(data, &releases); err != nil {
			return nil, fmt.Errorf("reading JukaHub's release list: %w", err)
		}
		if cands := relPackCandidates(releases, spec.Asset, spec.MaxAsset); len(cands) > 0 {
			return cands, nil
		}
		if len(releases) < relPackPerPage {
			break
		}
	}
	return nil, fmt.Errorf("%w (%s)", errRelPackNotPublished, spec.Asset)
}

// relPackLatest is the release an install would try first, for "is there a
// newer one" checks. It does not download anything.
func relPackLatest(ctx context.Context, client *http.Client, spec relPackSpec) (relPackCandidate, error) {
	cands, err := relPackFind(ctx, client, spec)
	if err != nil {
		return relPackCandidate{}, err
	}
	return cands[0], nil
}

// relPackNewer reports whether c is a different build from the installed one:
// by the archive's SHA-256 when both are known, else by tag.
func relPackNewer(installed relPackInfo, c relPackCandidate) bool {
	if strings.TrimSpace(installed.Tag) == "" {
		return true
	}
	if digest := releaseAssetDigest(c.Asset.Digest); digest != "" && installed.SHA256 != "" {
		return !strings.EqualFold(digest, installed.SHA256)
	}
	return strings.TrimSpace(c.Rel.TagName) != strings.TrimSpace(installed.Tag)
}

// relPackExpectedSHA is the SHA-256 the archive must have: the signed
// manifest's when the release carries one, else GitHub's digest ("" when
// GitHub published none; the package's SHA256SUMS is still checked then).
func relPackExpectedSHA(ctx context.Context, client *http.Client, c relPackCandidate) (string, error) {
	digest := releaseAssetDigest(c.Asset.Digest)
	signed, _, err := signedReleaseTarget(ctx, client, &githubRelease{TagName: c.Rel.TagName, PublishedAt: c.Rel.PublishedAt, Assets: c.Rel.Assets}, c.Asset.Name)
	switch {
	case err == nil:
		if digest != "" && !strings.EqualFold(digest, signed) {
			return "", fmt.Errorf("%w: GitHub's digest for %s disagrees with the signed release manifest", errRelPackUnverified, c.Asset.Name)
		}
		return strings.ToLower(signed), nil
	case errors.Is(err, errNoReleaseManifest):
		if digest == "" {
			logWarn("relpack", "%s in %s has no published sha256; relying on HTTPS and its SHA256SUMS", c.Asset.Name, c.Rel.TagName)
		}
		return digest, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", err
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "does not list"):
		return "", fmt.Errorf("%w (%s: %v)", errRelPackUnlisted, c.Rel.TagName, err)
	case strings.Contains(text, "issued in the future"), strings.Contains(text, "manifest expired"):
		return "", fmt.Errorf("%w (%v)", errRelPackClock, err)
	}
	wrapped := relPackNetErr("fetching the signed release manifest", err)
	for _, pass := range []error{errRelPackOffline, errRelPackCutOff, errRelPackRateLimited, errRelPackClock, errRelPackTrust, errRelPackNoSpace, errRelPackDiskWrite} {
		if errors.Is(wrapped, pass) {
			return "", wrapped
		}
	}
	return "", fmt.Errorf("%w: %v", errRelPackUnverified, err)
}

// ── Errors ───────────────────────────────────────────────────────────────

// relPackOutOfSpace reports whether err is a full disk.
func relPackOutOfSpace(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no space left on device") ||
		strings.Contains(text, "not enough space on the disk") ||
		strings.Contains(text, "disk quota exceeded")
}

// relPackDiskErr maps a full or refusing disk to its sentinel; nil for any
// other error.
func relPackDiskErr(err error) error {
	if err == nil {
		return nil
	}
	if relPackOutOfSpace(err) {
		return fmt.Errorf("%w · free some space, then try again", errRelPackNoSpace)
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "read-only file system") || strings.Contains(text, "input/output error") {
		return fmt.Errorf("%w (%v)", errRelPackDiskWrite, err)
	}
	return nil
}

// relPackNetErr classifies a failed request the way the Remote Play helper
// installer does (psHelperNetErr), with sentinels that do not name a feature.
func relPackNetErr(what string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if disk := relPackDiskErr(err); disk != nil {
		return disk
	}
	text := err.Error()
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var netErr net.Error
	switch {
	case strings.Contains(text, "HTTP 403") || strings.Contains(text, "HTTP 429"):
		return fmt.Errorf("%w (%s: %v)", errRelPackRateLimited, what, err)
	case strings.Contains(text, "certificate has expired or is not yet valid"):
		// No RTC battery: after a power cycle the clock can sit years back.
		return fmt.Errorf("%w (%s: %v)", errRelPackClock, what, err)
	case strings.Contains(text, "certificate is valid for"):
		// A Wi-Fi sign-in page answering for GitHub.
		return fmt.Errorf("%w (%s: %v)", errRelPackOffline, what, err)
	case strings.Contains(text, "certificate signed by unknown authority"),
		strings.Contains(text, "failed to load system roots"):
		return fmt.Errorf("%w (%s: %v)", errRelPackTrust, what, err)
	case errors.Is(err, io.ErrUnexpectedEOF),
		strings.Contains(text, "unexpected EOF"),
		strings.Contains(text, "stream error"),
		strings.Contains(text, "connection reset"),
		strings.Contains(text, "partial download does not match"):
		return fmt.Errorf("%w (%s: %v)", errRelPackCutOff, what, err)
	case errors.As(err, &dnsErr), errors.As(err, &opErr),
		errors.As(err, &netErr) && netErr.Timeout(),
		strings.Contains(text, "no such host"),
		strings.Contains(text, "network is unreachable"),
		strings.Contains(text, "no route to host"),
		strings.Contains(text, "connection refused"),
		strings.Contains(text, "i/o timeout"),
		strings.Contains(text, "TLS handshake timeout"):
		return fmt.Errorf("%w (%s: %v)", errRelPackOffline, what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// relPackFriendly is a one-line, user-facing reason for err.
func relPackFriendly(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "Stopped"
	case errors.Is(err, context.DeadlineExceeded):
		return "Took too long · try again on a faster connection"
	case errors.Is(err, errRelPackOffline):
		return "Could not reach GitHub · check the Wi-Fi, then try again"
	case errors.Is(err, errRelPackCutOff):
		return "The download was cut off · try again to resume it"
	case errors.Is(err, errRelPackRateLimited):
		return "GitHub is refusing requests for now · try again in an hour"
	case errors.Is(err, errRelPackClock):
		return "This device's date is wrong · set the clock (or connect to Wi-Fi), then try again"
	case errors.Is(err, errRelPackTrust):
		return "This device cannot check GitHub's certificate"
	case errors.Is(err, errRelPackNoSpace):
		return "Not enough free space · " + strings.TrimPrefix(err.Error(), errRelPackNoSpace.Error()+" · ")
	case errors.Is(err, errRelPackDiskWrite):
		return "The card refused a write · check that it is not full or read-only"
	case errors.Is(err, errRelPackNotPublished):
		return "No recent JukaHub release carries it yet"
	case errors.Is(err, errRelPackUnverified), errors.Is(err, errRelPackUnlisted):
		return "The download did not verify, so nothing was installed"
	case errors.Is(err, errRelPackWontRun):
		return err.Error()
	case errors.Is(err, errRelPackBusy):
		return "Already installing"
	case errors.Is(err, errRelPackUnsupported):
		return "Not available on this device"
	}
	return err.Error()
}

// ── Download, unpack, check, install ─────────────────────────────────────

// relPackFileSize is a file's size, 0 when it is not there.
func relPackFileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}

// relPackDownload fetches the archive to path and checks it against expect
// (when known) and the size GitHub states. A download cut off after it made
// progress is resumed, up to relPackDownloadTries starts in all.
func relPackDownload(ctx context.Context, client *http.Client, spec relPackSpec, c relPackCandidate, path, expect string, progress func(relPackProgress)) error {
	total := c.Asset.Size
	matches := func(p string) bool {
		if total > 0 && relPackFileSize(p) != total {
			return false
		}
		if expect == "" {
			return false
		}
		got, err := SHA256File(p)
		return err == nil && strings.EqualFold(got, expect)
	}
	// A complete archive from an attempt that stopped after its download.
	if matches(path) {
		return nil
	}
	_ = os.Remove(path)
	limit := spec.MaxAsset
	if limit <= 0 {
		limit = 1 << 30
	}
	report := func() {
		progress(relPackProgress{Phase: relPackDownloading, Got: relPackFileSize(path + ".part"), Total: total, Tag: c.Rel.TagName})
	}
	report()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				report()
			}
		}
	}()
	var err error
	for try := 1; try <= relPackDownloadTries; try++ {
		before := relPackFileSize(path + ".part")
		err = downloadToFile(ctx, client, c.Asset.BrowserDownloadURL, path, expect, limit)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "sha256 mismatch") {
			err = fmt.Errorf("%w: %v", errRelPackUnverified, err)
			break
		}
		if relPackOutOfSpace(err) {
			_ = os.Remove(path + ".part")
			err = fmt.Errorf("%w · free some space, then try again", errRelPackNoSpace)
			break
		}
		err = relPackNetErr("downloading "+c.Asset.Name, err)
		lost := errors.Is(err, errRelPackCutOff) || errors.Is(err, errRelPackOffline)
		if !lost || try == relPackDownloadTries || relPackFileSize(path+".part") <= before {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(2 * time.Second):
			continue
		}
		break
	}
	close(stop)
	wg.Wait()
	if err != nil {
		return err
	}
	if got := relPackFileSize(path); total > 0 && got != total {
		_ = os.Remove(path)
		return fmt.Errorf("%w: %s is %d bytes, GitHub lists %d", errRelPackUnverified, c.Asset.Name, got, total)
	}
	got := relPackFileSize(path)
	progress(relPackProgress{Phase: relPackDownloading, Got: got, Total: got, Tag: c.Rel.TagName})
	return nil
}

// relPackReadSums reads a SHA256SUMS file ("<hex>  <path>" or "<hex> *<path>"
// per line) into hashes by slash-separated relative path.
func relPackReadSums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 || releaseAssetDigest("sha256:"+fields[0]) == "" {
			return nil, fmt.Errorf("%w: unreadable %s line %q", errRelPackUnverified, relPackSumsName, line)
		}
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		name = strings.ReplaceAll(name, `\`, "/")
		if name == "" || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return nil, fmt.Errorf("%w: unsafe path %q in %s", errRelPackUnverified, fields[1], relPackSumsName)
		}
		sums[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

// relPackVerifyTree checks the unpacked package in dir against its own
// SHA256SUMS: every regular file must be listed (the list itself and the
// provenance file aside) and match, and every listed file must be there. It
// returns how many files were checked.
func relPackVerifyTree(ctx context.Context, dir string) (int, error) {
	sums, err := relPackReadSums(filepath.Join(dir, relPackSumsName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("%w: the package has no %s", errRelPackUnverified, relPackSumsName)
		}
		return 0, err
	}
	if len(sums) == 0 {
		return 0, fmt.Errorf("%w: %s lists no files", errRelPackUnverified, relPackSumsName)
	}
	seen := map[string]bool{}
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == relPackSumsName || rel == relPackInfoName {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", errRelPackUnverified, rel)
		}
		want, listed := sums[rel]
		if !listed {
			return fmt.Errorf("%w: %s is not in %s", errRelPackUnverified, rel, relPackSumsName)
		}
		got, err := SHA256File(p)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("%w: %s does not match %s", errRelPackUnverified, rel, relPackSumsName)
		}
		seen[rel] = true
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}
	for name := range sums {
		if !seen[name] {
			return 0, fmt.Errorf("%w: %s lists %s, which the package does not have", errRelPackUnverified, relPackSumsName, name)
		}
	}
	return len(seen), nil
}

// relPackSwap moves the checked tree into dir: the current dir aside, the new
// one in, the old one removed. If the second rename fails the old one goes
// back.
func relPackSwap(tree, dir string) error {
	old := dir + relPackOldSuffix
	_ = os.RemoveAll(old)
	hadOld := false
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		if err := os.Rename(dir, old); err != nil {
			return fmt.Errorf("could not set the installed copy aside: %w", err)
		}
		hadOld = true
	}
	if err := os.Rename(tree, dir); err != nil {
		if hadOld {
			if back := os.Rename(old, dir); back != nil {
				logWarn("relpack", "could not put %s back: %v", dir, back)
			}
		}
		return fmt.Errorf("could not move the new copy into place: %w", err)
	}
	if hadOld {
		if err := os.RemoveAll(old); err != nil {
			logWarn("relpack", "could not remove the replaced copy %s: %v", old, err)
		}
	}
	return nil
}

// relPackInstall installs spec's newest published package into dir. It
// returns what it installed; progress (may be nil) receives every step. An
// install that is already running for spec.Key returns errRelPackBusy.
func relPackInstall(ctx context.Context, client *http.Client, spec relPackSpec, dir string, progress func(relPackProgress)) (relPackInfo, error) {
	if progress == nil {
		progress = func(relPackProgress) {}
	}
	if spec.Supported != nil && !spec.Supported() {
		return relPackInfo{}, errRelPackUnsupported
	}
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(spec.Asset) == "" {
		return relPackInfo{}, errors.New("relpack: no install folder or asset")
	}
	if !relPackBegin(spec.Key) {
		return relPackInfo{}, errRelPackBusy
	}
	defer relPackEnd(spec.Key)
	if client == nil {
		client = relPackClientFn()
	}
	dir = filepath.Clean(dir)
	relPackRecover(dir)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return relPackInfo{}, disk
		}
		return relPackInfo{}, fmt.Errorf("could not create %s: %w", parent, err)
	}

	progress(relPackProgress{Phase: relPackLooking})
	cands, err := relPackFind(ctx, client, spec)
	if err != nil {
		return relPackInfo{}, err
	}
	var pick relPackCandidate
	expect := ""
	for i, c := range cands {
		if i >= relPackMaxCandidates {
			break
		}
		sum, err := relPackExpectedSHA(ctx, client, c)
		if errors.Is(err, errRelPackUnlisted) {
			logWarn("relpack", "%v; trying an older release", err)
			continue
		}
		if err != nil {
			return relPackInfo{}, err
		}
		pick, expect = c, sum
		break
	}
	if pick.Rel.TagName == "" {
		return relPackInfo{}, fmt.Errorf("%w: the signed manifests of the newest releases do not list %s", errRelPackUnverified, spec.Asset)
	}

	// Room for the archive and what it unpacks to (the declared size is
	// checked against MaxUnpacked once the archive is here).
	need := pick.Asset.Size*3 + relPackSpare
	if free, err := freeBytes(parent); err == nil && free < need {
		return relPackInfo{}, fmt.Errorf("%w · needs %s, has %s", errRelPackNoSpace, formatInstallBytes(need), formatInstallBytes(max64(free, 0)))
	}

	work := dir + relPackWorkSuffix
	if err := os.MkdirAll(work, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return relPackInfo{}, disk
		}
		return relPackInfo{}, fmt.Errorf("could not create %s: %w", work, err)
	}
	archive := filepath.Join(work, sanitizeFileName(pick.Rel.TagName)+"-"+sanitizeFileName(spec.Asset))
	// A download of another release must not be resumed into this one.
	if entries, err := os.ReadDir(work); err == nil {
		for _, e := range entries {
			if e.Name() != filepath.Base(archive) && e.Name() != filepath.Base(archive)+".part" {
				_ = os.RemoveAll(filepath.Join(work, e.Name()))
			}
		}
	}
	if err := relPackDownload(ctx, client, spec, pick, archive, expect, progress); err != nil {
		return relPackInfo{}, err
	}
	sum := expect
	if sum == "" {
		if got, err := SHA256File(archive); err == nil {
			sum = got
		}
	}

	progress(relPackProgress{Phase: relPackUnpacking, Tag: pick.Rel.TagName})
	stage := dir + relPackStageSuffix
	_ = os.RemoveAll(stage)
	if spec.MaxUnpacked > 0 {
		if size, err := relPackZipSize(archive); err != nil {
			_ = os.Remove(archive)
			return relPackInfo{}, fmt.Errorf("%w: %v", errRelPackUnverified, err)
		} else if size > spec.MaxUnpacked {
			_ = os.Remove(archive)
			return relPackInfo{}, fmt.Errorf("%w: it unpacks to %s, more than %s allows", errRelPackUnverified, formatInstallBytes(size), spec.Title)
		} else if free, err := freeBytes(parent); err == nil && free < size+relPackSpare {
			return relPackInfo{}, fmt.Errorf("%w · needs %s, has %s", errRelPackNoSpace, formatInstallBytes(size+relPackSpare), formatInstallBytes(max64(free, 0)))
		}
	}
	if err := emuExtractZip(ctx, archive, stage, nil); err != nil {
		_ = os.RemoveAll(stage)
		if disk := relPackDiskErr(err); disk != nil {
			return relPackInfo{}, disk
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return relPackInfo{}, err
		}
		_ = os.Remove(archive)
		return relPackInfo{}, fmt.Errorf("%w: %v", errRelPackUnverified, err)
	}
	tree := stage
	if root := strings.Trim(filepath.ToSlash(spec.Root), "/"); root != "" {
		tree = filepath.Join(stage, filepath.FromSlash(root))
		if info, err := os.Stat(tree); err != nil || !info.IsDir() {
			_ = os.RemoveAll(stage)
			return relPackInfo{}, fmt.Errorf("%w: the package has no %s/ folder", errRelPackUnverified, root)
		}
	}

	progress(relPackProgress{Phase: relPackChecking, Tag: pick.Rel.TagName})
	files, err := relPackVerifyTree(ctx, tree)
	if err != nil {
		_ = os.RemoveAll(stage)
		return relPackInfo{}, err
	}
	if spec.Probe != nil {
		if problem := strings.TrimSpace(spec.Probe(tree)); problem != "" {
			_ = os.RemoveAll(stage)
			return relPackInfo{}, fmt.Errorf("%w · %s", errRelPackWontRun, problem)
		}
	}

	progress(relPackProgress{Phase: relPackInstalling, Tag: pick.Rel.TagName})
	info := relPackInfo{
		Key:         spec.Key,
		Asset:       spec.Asset,
		Tag:         pick.Rel.TagName,
		SHA256:      strings.ToLower(sum),
		Size:        pick.Asset.Size,
		Published:   strings.TrimSpace(pick.Rel.PublishedAt),
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		Files:       files,
	}
	if err := relPackWriteInfo(tree, info); err != nil {
		_ = os.RemoveAll(stage)
		if disk := relPackDiskErr(err); disk != nil {
			return relPackInfo{}, disk
		}
		return relPackInfo{}, err
	}
	if err := relPackSwap(tree, dir); err != nil {
		_ = os.RemoveAll(stage)
		if disk := relPackDiskErr(err); disk != nil {
			return relPackInfo{}, disk
		}
		return relPackInfo{}, err
	}
	_ = os.RemoveAll(stage)
	_ = os.RemoveAll(work)
	progress(relPackProgress{Phase: relPackDone, Tag: pick.Rel.TagName})
	logInfo("relpack", "%s %s installed in %s (%d files)", spec.Key, pick.Rel.TagName, dir, files)
	return info, nil
}

// relPackZipSize is what the archive's regular files add up to.
func relPackZipSize(archive string) (int64, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		// zip.ErrInsecurePath comes back with an open reader.
		if zr != nil {
			_ = zr.Close()
		}
		return 0, err
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		if f.Mode().IsRegular() {
			total += int64(f.UncompressedSize64)
		}
	}
	return total, nil
}
