package main

// emuwininstall.go — installing and updating emulators on Windows.
//
// The Emulators page used to stop at "Not installed" on Windows: the core
// installer was handheld-only and the standalone emulators had to be found,
// downloaded and unpacked by hand. Every emulator in the catalog publishes its
// Windows build somewhere JukaHub already trusts, so the page installs them:
//
//   - RetroArch comes from the libretro buildbot (the newest stable build that
//     has a Windows x86_64 RetroArch.7z, the nightly tree as a fallback). Its
//     cores then come from the buildbot's windows/x86_64 tree through the same
//     core installer the handheld uses.
//   - The standalone emulators come from their official GitHub releases: the
//     latest release (or the project's rolling tag), the one asset that is the
//     Windows x64 portable archive by the rules in emuWinSpecs, its size checked
//     against a cap and its SHA-256 against the digest GitHub publishes.
//   - Dolphin and the older desktop extras publish nothing on an allowlisted
//     host; their tiles open the official download page instead.
//
// Everything lands in one folder per emulator under emuWinInstallRoot (a
// portable "Emulators" folder next to JukaHub.exe, or %LOCALAPPDATA% when that
// is read-only), which is also where the catalog looks first, so the tile turns
// "Available" the moment the install finishes.
//
// An update never wipes a folder: the new release is laid over the old one, so
// portable settings, saves and RetroArch's own cores survive, and every file it
// replaces is moved aside into .backup/<Name>/ first. That one kept version is
// what "Roll back" restores. Each managed folder carries a small manifest
// (.jukahub-emu.json) saying which release it holds; a folder without one was
// installed by someone else and is launched but never updated.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// emuWinSpec says where one Windows emulator comes from and which release
// asset is its portable Windows x64 build.
type emuWinSpec struct {
	// Name matches the winEmuCatalog entry the install becomes.
	Name string
	// Repo is the GitHub project ("owner/name"). Empty for RetroArch, which
	// comes from the buildbot, and for emulators with nothing to install here.
	Repo string
	// Tag pins a rolling release ("latest", "continuous"); empty asks GitHub
	// for the newest stable release.
	Tag string
	// Prefer names assets to take first, compared without case.
	Prefer []string
	// Match lists token groups: an asset qualifies when its name contains every
	// token of one group. Groups are tried in order.
	Match [][]string
	// Exclude disqualifies an asset whose name contains any of these.
	Exclude []string
	// MaxBytes caps the download.
	MaxBytes int64
	// Site is the official download page, opened when nothing can be installed.
	Site string
	// Buildbot marks RetroArch, which comes from the libretro buildbot.
	Buildbot bool
}

// emuWinSpecs is every Windows emulator the page knows how to obtain. The
// asset rules match each project's real release names (checked November 2026):
// PPSSPP-v1.20.4-Windows-x64.zip, duckstation-windows-x64-release.zip,
// pcsx2-v2.8.2-windows-x64-Qt.7z, melonDS-1.1-windows-x86_64.zip,
// mGBA-0.10.5-win64.7z, cemu-2.6-windows-x64.zip, windows-latest.zip (Vita3K),
// rpcs3-v0.0.43-…_win64_msvc.7z and xemu-win-x86_64-release.zip.
var emuWinSpecs = []emuWinSpec{
	{
		Name:     "RetroArch",
		Buildbot: true,
		MaxBytes: 768 << 20,
		Site:     "https://www.retroarch.com/?page=platforms",
	},
	{
		Name:     "PPSSPP",
		Repo:     "hrydgard/ppsspp",
		Match:    [][]string{{"windows", "x64", ".zip"}},
		MaxBytes: 256 << 20,
		Site:     "https://www.ppsspp.org/download/",
	},
	{
		Name:     "DuckStation",
		Repo:     "stenzek/duckstation",
		Tag:      "latest",
		Prefer:   []string{"duckstation-windows-x64-release.zip"},
		Match:    [][]string{{"windows", "x64", "release", ".zip"}},
		Exclude:  []string{"sse2"},
		MaxBytes: 256 << 20,
		Site:     "https://www.duckstation.org/",
	},
	{
		Name:     "PCSX2",
		Repo:     "PCSX2/pcsx2",
		Match:    [][]string{{"windows", "x64", "qt", ".7z"}},
		MaxBytes: 256 << 20,
		Site:     "https://pcsx2.net/downloads/",
	},
	{
		Name: "Dolphin",
		Site: "https://dolphin-emu.org/download/",
	},
	{
		Name:     "melonDS",
		Repo:     "melonDS-emu/melonDS",
		Match:    [][]string{{"windows", "x86_64", ".zip"}, {"win", "x64", ".zip"}},
		MaxBytes: 128 << 20,
		Site:     "https://melonds.kuribo64.net/downloads.php",
	},
	{
		Name:     "mGBA",
		Repo:     "mgba-emu/mgba",
		Match:    [][]string{{"win64", ".7z"}, {"win64", ".zip"}},
		MaxBytes: 128 << 20,
		Site:     "https://mgba.io/downloads.html",
	},
	{
		Name:     "Cemu",
		Repo:     "cemu-project/Cemu",
		Match:    [][]string{{"windows", "x64", ".zip"}},
		MaxBytes: 256 << 20,
		Site:     "https://cemu.info/",
	},
	{
		Name:     "Vita3K",
		Repo:     "Vita3K/Vita3K",
		Tag:      "continuous",
		Prefer:   []string{"windows-latest.zip"},
		Match:    [][]string{{"windows", ".zip"}},
		MaxBytes: 256 << 20,
		Site:     "https://vita3k.org/",
	},
	{
		Name:     "RPCS3",
		Repo:     "RPCS3/rpcs3-binaries-win",
		Match:    [][]string{{"_win64_msvc", ".7z"}},
		Exclude:  []string{"clang"},
		MaxBytes: 512 << 20,
		Site:     "https://rpcs3.net/download",
	},
	{
		Name:     "xemu",
		Repo:     "xemu-project/xemu",
		Prefer:   []string{"xemu-win-x86_64-release.zip"},
		Match:    [][]string{{"win", "x86_64", ".zip"}},
		MaxBytes: 128 << 20,
		Site:     "https://xemu.app/",
	},
	{Name: "FCEUX", Site: "https://fceux.com/web/download.html"},
	{Name: "Snes9x", Site: "https://www.snes9x.com/"},
	{Name: "Mednafen", Site: "https://mednafen.github.io/"},
	{Name: "MAME", Site: "https://www.mamedev.org/release.html"},
	{Name: "bsnes", Site: "https://github.com/bsnes-emu/bsnes/releases"},
}

// emuWinAssetExclude disqualifies the assets no rule may pick: debug symbols,
// installers, other architectures and source archives.
var emuWinAssetExclude = []string{
	"pdb", "debug", "symbols", "installer", "setup", ".msi",
	"arm64", "aarch64", "source", "-src",
}

const (
	// emuWinManifestName records which release a managed folder holds.
	emuWinManifestName = ".jukahub-emu.json"
	// emuWinBackupManifestName records what an update added, so a rollback can
	// remove it again.
	emuWinBackupManifestName = ".jukahub-backup.json"
	// emuMaxArchiveEntries and emuMaxUnpackedBytes bound what one archive may
	// unpack to; the largest emulator (RetroArch with its assets) is well
	// under both.
	emuMaxArchiveEntries = 60000
	emuMaxUnpackedBytes  = int64(4 << 30)
	// emuWinInstallTimeout bounds one install. RetroArch is a quarter of a
	// gigabyte, and a slow line needs most of an hour for it.
	emuWinInstallTimeout = 90 * time.Minute
)

// emuWinSpecFor finds the install rules for a catalog name.
func emuWinSpecFor(name string) (emuWinSpec, bool) {
	name = strings.TrimSpace(name)
	for _, spec := range emuWinSpecs {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return emuWinSpec{}, false
}

// installable reports whether the page can download the emulator itself
// (rather than only opening its download page).
func (s emuWinSpec) installable() bool {
	return s.Buildbot || strings.TrimSpace(s.Repo) != ""
}

// emuWinLauncherNames is the executable names an install of name may carry.
func emuWinLauncherNames(name string) []string {
	for _, spec := range winEmuCatalog {
		if strings.EqualFold(spec.name, name) {
			return spec.files
		}
	}
	return nil
}

// ── Install folder ──────────────────────────────────────────────────────────

// emuWinRootsOverride replaces the candidate folders when it is not nil. The
// test binary points it at a temporary folder, so neither a test nor a scan in
// one ever sees (or writes) the developer's real emulator installs.
var emuWinRootsOverride []string

// emuWinInstallRootCandidates lists the folders an install may go to, in order
// of preference: an explicit JUKAHUB_EMULATORS_DIR, the portable "Emulators"
// folder next to JukaHub.exe, then the user's local application data.
func emuWinInstallRootCandidates() []string {
	if emuWinRootsOverride != nil {
		return append([]string(nil), emuWinRootsOverride...)
	}
	var out []string
	if v := strings.TrimSpace(os.Getenv("JUKAHUB_EMULATORS_DIR")); v != "" {
		out = append(out, filepath.Clean(v))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "Emulators"))
	}
	if v := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); v != "" {
		out = append(out, filepath.Join(v, "JukaHub", "Emulators"))
	}
	return out
}

// emuWinExistingRoots is the candidates that exist, for the catalog scan: a
// lookup must never create folders.
func emuWinExistingRoots() []string {
	var out []string
	for _, dir := range emuWinInstallRootCandidates() {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// emuDirWritable creates dir if needed and proves a file can be written in it.
func emuDirWritable(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

// emuWinWritableRoot is the first candidate folder that can be written.
func emuWinWritableRoot() (string, error) {
	for _, dir := range emuWinInstallRootCandidates() {
		if emuDirWritable(dir) {
			return dir, nil
		}
	}
	return "", errors.New("no folder JukaHub can write emulators to — set JUKAHUB_EMULATORS_DIR to one")
}

// emuWinInstallRoot is the folder an install of name goes to: the root that
// already holds JukaHub's copy of it, else the first one that can be written.
func emuWinInstallRoot(name string) (string, error) {
	for _, dir := range emuWinExistingRoots() {
		if _, ok := emuWinReadManifest(filepath.Join(dir, name)); ok {
			return dir, nil
		}
	}
	return emuWinWritableRoot()
}

// emuWinToolsDir holds helpers the installer needs (7-Zip's 7zr.exe).
func emuWinToolsDir() (string, error) {
	root, err := emuWinWritableRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, ".tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ── Manifest ────────────────────────────────────────────────────────────────

// emuWinManifest is what a managed install folder says about itself.
type emuWinManifest struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Tag    string `json:"tag"`
	Asset  string `json:"asset"`
	URL    string `json:"url"`
	Digest string `json:"digest,omitempty"`
	// AssetDate is GitHub's updated_at for the asset: the only thing that
	// changes when a rolling tag ("latest", "continuous") is rebuilt.
	AssetDate string `json:"asset_updated_at,omitempty"`
	// Exe is the launcher's path relative to the folder.
	Exe         string `json:"exe"`
	InstalledAt string `json:"installed_at"`
}

// emuWinReadManifest reads dir's manifest; ok is false for a folder JukaHub
// did not install.
func emuWinReadManifest(dir string) (emuWinManifest, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, emuWinManifestName))
	if err != nil {
		return emuWinManifest{}, false
	}
	var m emuWinManifest
	if json.Unmarshal(raw, &m) != nil || strings.TrimSpace(m.Name) == "" {
		return emuWinManifest{}, false
	}
	return m, true
}

func emuWinWriteManifest(dir string, m emuWinManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, emuWinManifestName), append(data, '\n'), 0o644)
}

// emuWinManaged finds the managed folder of name under the existing roots.
func emuWinManaged(name string) (dir string, m emuWinManifest, ok bool) {
	for _, root := range emuWinExistingRoots() {
		candidate := filepath.Join(root, name)
		if m, ok := emuWinReadManifest(candidate); ok {
			return candidate, m, true
		}
	}
	return "", emuWinManifest{}, false
}

// emuWinBackupDir is where the version an update replaced is kept.
func emuWinBackupDir(managedDir string) string {
	return filepath.Join(filepath.Dir(managedDir), ".backup", filepath.Base(managedDir))
}

// emuWinBackupInfo is the kept version's description.
type emuWinBackupInfo struct {
	Name    string   `json:"name"`
	Tag     string   `json:"tag"`
	SavedAt string   `json:"saved_at"`
	Added   []string `json:"added"`
}

// emuWinKeptTag is the version a rollback of the managed folder dir would
// restore, or "" when no previous version is kept.
func emuWinKeptTag(dir string) string {
	raw, err := os.ReadFile(filepath.Join(emuWinBackupDir(dir), emuWinBackupManifestName))
	if err != nil {
		return ""
	}
	var info emuWinBackupInfo
	if json.Unmarshal(raw, &info) != nil {
		return ""
	}
	if tag := strings.TrimSpace(info.Tag); tag != "" {
		return tag
	}
	return "previous version"
}

// ── Releases ────────────────────────────────────────────────────────────────

// emuWinRelease is one downloadable build of an emulator.
type emuWinRelease struct {
	Tag       string
	Asset     string
	URL       string
	Size      int64
	Digest    string
	AssetDate string
}

type emuGitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	UpdatedAt          string `json:"updated_at"`
}

type emuGitHubRelease struct {
	TagName    string           `json:"tag_name"`
	Draft      bool             `json:"draft"`
	Prerelease bool             `json:"prerelease"`
	Assets     []emuGitHubAsset `json:"assets"`
}

// emuPickWinAsset returns the asset of a release that spec installs: an
// exact preferred name first, then the first asset matching a token group.
// Side-cars (checksums, signatures), installers, debug symbols and other
// architectures never qualify.
func emuPickWinAsset(spec emuWinSpec, assets []emuGitHubAsset) (emuGitHubAsset, bool) {
	usable := func(a emuGitHubAsset) bool {
		lower := strings.ToLower(strings.TrimSpace(a.Name))
		if lower == "" || strings.TrimSpace(a.BrowserDownloadURL) == "" || isToolAssetSidecar(lower) {
			return false
		}
		if !strings.HasSuffix(lower, ".zip") && !strings.HasSuffix(lower, ".7z") {
			return false
		}
		for _, bad := range emuWinAssetExclude {
			if strings.Contains(lower, bad) {
				return false
			}
		}
		for _, bad := range spec.Exclude {
			if bad != "" && strings.Contains(lower, strings.ToLower(bad)) {
				return false
			}
		}
		return true
	}
	for _, want := range spec.Prefer {
		for _, a := range assets {
			if strings.EqualFold(strings.TrimSpace(a.Name), want) && usable(a) {
				return a, true
			}
		}
	}
	for _, tokens := range spec.Match {
		if len(tokens) == 0 {
			continue
		}
		for _, a := range assets {
			if usable(a) && assetNameMatches(a.Name, tokens) {
				return a, true
			}
		}
	}
	return emuGitHubAsset{}, false
}

func emuReleaseFrom(rel emuGitHubRelease, a emuGitHubAsset) emuWinRelease {
	return emuWinRelease{
		Tag:       strings.TrimSpace(rel.TagName),
		Asset:     a.Name,
		URL:       a.BrowserDownloadURL,
		Size:      a.Size,
		Digest:    strings.TrimSpace(a.Digest),
		AssetDate: strings.TrimSpace(a.UpdatedAt),
	}
}

// emuFetchGitHubRelease resolves spec's newest Windows build: the pinned tag
// or the latest stable release, then the recent release list (pre-releases
// included) when that release has no Windows x64 archive.
func emuFetchGitHubRelease(ctx context.Context, client *http.Client, spec emuWinSpec) (emuWinRelease, error) {
	primary := fmt.Sprintf("%s/repos/%s/releases/latest", standaloneAPIBase, spec.Repo)
	if spec.Tag != "" {
		primary = fmt.Sprintf("%s/repos/%s/releases/tags/%s", standaloneAPIBase, spec.Repo, spec.Tag)
	}
	var lastErr error
	if data, err := fetchUpdateBytes(ctx, client, primary, patchMaxMetadata); err != nil {
		lastErr = err
	} else {
		var rel emuGitHubRelease
		if err := json.Unmarshal(data, &rel); err != nil {
			lastErr = fmt.Errorf("%s: unreadable release metadata", spec.Name)
		} else if a, ok := emuPickWinAsset(spec, rel.Assets); ok {
			return emuReleaseFrom(rel, a), nil
		} else {
			lastErr = fmt.Errorf("the newest %s release has no Windows x64 build", spec.Name)
		}
	}
	if err := ctx.Err(); err != nil {
		return emuWinRelease{}, err
	}
	list := fmt.Sprintf("%s/repos/%s/releases?per_page=8", standaloneAPIBase, spec.Repo)
	if data, err := fetchUpdateBytes(ctx, client, list, patchMaxMetadata); err == nil {
		var releases []emuGitHubRelease
		if json.Unmarshal(data, &releases) == nil {
			for _, rel := range releases {
				if rel.Draft {
					continue
				}
				if a, ok := emuPickWinAsset(spec, rel.Assets); ok {
					return emuReleaseFrom(rel, a), nil
				}
			}
		}
	} else if lastErr == nil {
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no %s release with a Windows x64 build was found", spec.Name)
	}
	return emuWinRelease{}, lastErr
}

// The buildbot URLs are variables so tests can serve a local listing.
var (
	emuRetroArchStableURL  = "https://buildbot.libretro.com/stable/"
	emuRetroArchNightlyURL = "https://buildbot.libretro.com/nightly/windows/x86_64/RetroArch.7z"
)

// emuStableVersionPattern finds the version folders a buildbot listing links
// to ("1.22.2/"). The guard before the number keeps a dated folder
// ("2026.10.25/") from being read as a version.
var emuStableVersionPattern = regexp.MustCompile(`(?:^|[^0-9.])([0-9]{1,2}\.[0-9]{1,3}\.[0-9]{1,3})/`)

// emuRetroArchStableVersions lists the stable versions a buildbot listing
// names, newest first.
func emuRetroArchStableVersions(listing string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range emuStableVersionPattern.FindAllStringSubmatch(listing, -1) {
		if len(m) < 2 || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	sort.SliceStable(out, func(i, j int) bool { return compareCoreVersions(out[i], out[j]) > 0 })
	return out
}

// emuRemoteExists asks the server whether url is there without downloading
// it. A server that refuses HEAD is taken at its word that the file may be
// there: the download itself will say.
func emuRemoteExists(ctx context.Context, client *http.Client, raw string) (bool, int64, error) {
	if err := emulatorUpdateValidateURL(raw); err != nil {
		return false, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, raw, nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, resp.ContentLength, nil
	case http.StatusNotFound, http.StatusForbidden:
		return false, 0, nil
	case http.StatusMethodNotAllowed:
		return true, 0, nil
	}
	return false, 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
}

// emuResolveRetroArch finds the RetroArch build to install: the newest stable
// version whose Windows x86_64 RetroArch.7z exists (a fresh version folder can
// appear before its Windows build does), else today's nightly.
func emuResolveRetroArch(ctx context.Context, client *http.Client) (emuWinRelease, error) {
	listing, err := fetchUpdateBytes(ctx, client, emuRetroArchStableURL, patchMaxMetadata)
	if err == nil {
		for i, version := range emuRetroArchStableVersions(string(listing)) {
			if i >= 4 {
				break
			}
			candidate := emuRetroArchStableURL + version + "/windows/x86_64/RetroArch.7z"
			found, size, probeErr := emuRemoteExists(ctx, client, candidate)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return emuWinRelease{}, ctxErr
			}
			if probeErr == nil && found {
				return emuWinRelease{Tag: version, Asset: "RetroArch.7z", URL: candidate, Size: size}, nil
			}
		}
	}
	found, size, nightlyErr := emuRemoteExists(ctx, client, emuRetroArchNightlyURL)
	if nightlyErr == nil && found {
		return emuWinRelease{
			Tag:   "nightly-" + time.Now().UTC().Format("2006-01-02"),
			Asset: "RetroArch.7z",
			URL:   emuRetroArchNightlyURL,
			Size:  size,
		}, nil
	}
	if err == nil {
		err = nightlyErr
	}
	if err == nil {
		err = errors.New("RetroArch for Windows is not on the buildbot right now")
	}
	return emuWinRelease{}, err
}

// emuWinResolve finds the build spec would install.
func emuWinResolve(ctx context.Context, client *http.Client, spec emuWinSpec) (emuWinRelease, error) {
	if spec.Buildbot {
		return emuResolveRetroArch(ctx, client)
	}
	if strings.TrimSpace(spec.Repo) == "" {
		return emuWinRelease{}, fmt.Errorf("%s has no download JukaHub can install — get it from %s", spec.Name, spec.Site)
	}
	return emuFetchGitHubRelease(ctx, client, spec)
}

// emuVersionTag matches a release tag that is a version number ("v1.20.4",
// "2.6", "v0.1-11752"), as opposed to a rolling name or a commit
// ("build-e8979d31…"), whose digits mean nothing in order.
var emuVersionTag = regexp.MustCompile(`^[vV]?[0-9]+(\.[0-9]+)+([-.][0-9A-Za-z]+)*$`)

// emuWinUpdateAvailable reports whether latest is newer than what the
// manifest says is installed. Two version numbers compare numerically; any
// other change of tag (a new commit build, a nightly date) is an update; a
// rolling tag is compared by the asset's digest, or by its upload time when
// GitHub publishes no digest.
func emuWinUpdateAvailable(installed emuWinManifest, latest emuWinRelease) bool {
	have, want := strings.TrimSpace(installed.Tag), strings.TrimSpace(latest.Tag)
	if have == "" || want == "" {
		return false
	}
	if have != want {
		if emuVersionTag.MatchString(have) && emuVersionTag.MatchString(want) {
			return compareCoreVersions(want, have) > 0
		}
		return true
	}
	if installed.Digest != "" && latest.Digest != "" {
		return !strings.EqualFold(installed.Digest, latest.Digest)
	}
	if installed.AssetDate != "" && latest.AssetDate != "" {
		return installed.AssetDate != latest.AssetDate
	}
	return false
}

// ── Transfer ────────────────────────────────────────────────────────────────

// emuWinClient is the HTTP client for emulator archives: long enough for a
// quarter-gigabyte RetroArch on a slow line (the run's context still cancels
// it), with every redirect held to the download allowlist.
func emuWinClient() *http.Client {
	return &http.Client{
		Timeout: emuWinInstallTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			return emulatorUpdateValidateURL(req.URL.String())
		},
	}
}

// emuDownload streams raw to dest, reporting progress, and returns the SHA-256
// of the bytes written. The file only appears under its name once complete.
func emuDownload(ctx context.Context, client *http.Client, raw, dest string, maxBytes int64, progress func(done, total int64)) (string, error) {
	if err := emulatorUpdateValidateURL(raw); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return "", fmt.Errorf("the download is %s, more than the %s limit", formatInstallBytes(resp.ContentLength), formatInstallBytes(maxBytes))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return "", err
	}
	limit := maxBytes
	if limit <= 0 {
		limit = emuMaxUnpackedBytes
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, hash), &progressReader{r: io.LimitReader(resp.Body, limit+1), total: resp.ContentLength, fn: progress})
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		err = copyErr
	case closeErr != nil:
		err = closeErr
	case n > limit:
		err = fmt.Errorf("the download is larger than the %s limit", formatInstallBytes(limit))
	case n == 0:
		err = errors.New("the download was empty")
	case resp.ContentLength > 0 && n != resp.ContentLength:
		err = fmt.Errorf("the download stopped after %s of %s", formatInstallBytes(n), formatInstallBytes(resp.ContentLength))
	}
	if err != nil {
		_ = os.Remove(part)
		return "", err
	}
	_ = os.Remove(dest)
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// emuDigestMatches checks a download's SHA-256 against GitHub's
// "sha256:<hex>" digest. A release without one passes: older releases predate
// GitHub publishing digests, and TLS from the allowlisted host is the check.
func emuDigestMatches(sum, digest string) error {
	d := strings.TrimSpace(digest)
	if d == "" {
		return nil
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(strings.ToLower(d), prefix) {
		return fmt.Errorf("unsupported release digest %q", digest)
	}
	want := strings.ToLower(strings.TrimSpace(d[len(prefix):]))
	if !strings.EqualFold(want, sum) {
		return fmt.Errorf("checksum mismatch (release %s, download %s)", want, sum)
	}
	return nil
}

// ── Unpacking ───────────────────────────────────────────────────────────────

// emuArchiveRel turns an archive entry name into a safe relative path, or ""
// for an entry that names the archive's own top.
func emuArchiveRel(name string) (string, error) {
	n := strings.TrimSpace(strings.ReplaceAll(name, `\`, "/"))
	if strings.ContainsRune(n, '\x00') || unsafeArchiveName(n) {
		return "", fmt.Errorf("%w (unsafe archive path %q)", errBadToolArchive, name)
	}
	clean := strings.TrimPrefix(path.Clean("/"+n), "/")
	if clean == "" || clean == "." {
		return "", nil
	}
	return filepath.FromSlash(clean), nil
}

// emuExtractZip unpacks every directory and regular file of a zip archive into
// dest through an os.Root, so no entry can land outside it; links and special
// entries are skipped. progress receives bytes written against the archive's
// declared total.
func emuExtractZip(ctx context.Context, archive, dest string, progress func(done, total int64)) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		// zip.ErrInsecurePath comes back with an open reader; closing it keeps
		// the archive deletable on Windows.
		if zr != nil {
			_ = zr.Close()
		}
		return fmt.Errorf("%w (open %s: %v)", errBadToolArchive, filepath.Base(archive), err)
	}
	defer zr.Close()
	if len(zr.File) > emuMaxArchiveEntries {
		return fmt.Errorf("%w (%d entries)", errBadToolArchive, len(zr.File))
	}
	var total int64
	for _, f := range zr.File {
		if f.Mode().IsRegular() {
			total += int64(f.UncompressedSize64)
		}
	}
	if total > emuMaxUnpackedBytes {
		return fmt.Errorf("%w (unpacks to %s)", errBadToolArchive, formatInstallBytes(total))
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	var done int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := emuArchiveRel(f.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		if parent := filepath.Dir(rel); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w (%s: %v)", errBadToolArchive, f.Name, err)
		}
		out, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		n, copyErr := io.Copy(out, &archiveReader{ctx: ctx, r: io.LimitReader(rc, int64(f.UncompressedSize64)+1)})
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != int64(f.UncompressedSize64) {
			return fmt.Errorf("%w (%s ended after %d of %d bytes)", errBadToolArchive, f.Name, n, f.UncompressedSize64)
		}
		done += n
		if progress != nil {
			progress(done, total)
		}
	}
	return nil
}

// emuArchiveContentRoot is the folder inside an unpacked archive that holds
// the emulator: the archive's top when it has files there, or its single
// wrapper folder ("Cemu_2.6", "mGBA-0.10.5-win64"), up to two levels down.
func emuArchiveContentRoot(dir string) string {
	for depth := 0; depth < 2; depth++ {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return dir
		}
		var only os.DirEntry
		dirs, files := 0, 0
		for _, e := range entries {
			if e.IsDir() {
				dirs++
				only = e
			} else {
				files++
			}
		}
		if files > 0 || dirs != 1 || only == nil {
			return dir
		}
		dir = filepath.Join(dir, only.Name())
	}
	return dir
}

// emuFindLauncher returns the path, relative to dir, of the first of names
// found at the top of dir or one folder down.
func emuFindLauncher(dir string, names []string) (string, bool) {
	for _, name := range names {
		if regularFile(filepath.Join(dir, name)) {
			return name, true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		for _, name := range names {
			rel := filepath.Join(e.Name(), name)
			if regularFile(filepath.Join(dir, rel)) {
				return rel, true
			}
		}
	}
	return "", false
}

// emuOverlay moves every file of the unpacked tree src into dest. A file dest
// already has is moved into backup first, so the version being replaced can be
// put back; files that are new are returned so a rollback can remove them. On
// failure everything moved so far is put back and dest is as it was.
func emuOverlay(src, dest, backup string) ([]string, error) {
	var rels []string
	walkErr := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rels = append(rels, rel)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	var added, saved []string
	undo := func() {
		for _, rel := range added {
			_ = os.Remove(filepath.Join(dest, rel))
		}
		for _, rel := range saved {
			_ = os.Rename(filepath.Join(backup, rel), filepath.Join(dest, rel))
		}
	}
	for _, rel := range rels {
		target := filepath.Join(dest, rel)
		existed := false
		if info, err := os.Lstat(target); err == nil {
			if !info.Mode().IsRegular() {
				undo()
				return nil, fmt.Errorf("%s is in the way of the update", rel)
			}
			keep := filepath.Join(backup, rel)
			if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
				undo()
				return nil, err
			}
			if err := os.Rename(target, keep); err != nil {
				undo()
				return nil, fmt.Errorf("%s is in use — close the emulator and try again (%v)", rel, err)
			}
			saved = append(saved, rel)
			existed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			undo()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			undo()
			return nil, err
		}
		if err := os.Rename(filepath.Join(src, rel), target); err != nil {
			if copyErr := copyArchiveFile(filepath.Join(src, rel), target, 0o644); copyErr != nil {
				if !existed {
					added = append(added, rel)
				}
				undo()
				return nil, copyErr
			}
		}
		if !existed {
			added = append(added, rel)
		}
	}
	return added, nil
}

// emuMoveTree moves a finished tree into place: a rename when it can, a copy
// when the two folders are on different volumes.
func emuMoveTree(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := copyTree(src, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	return os.RemoveAll(src)
}

// ── Install ─────────────────────────────────────────────────────────────────

// emuWinInstallResult is what one install or update did.
type emuWinInstallResult struct {
	Name string
	Tag  string
	Dir  string
	Exe  string
	// Updated is an existing install brought forward; Kept says the version it
	// replaced was kept for a rollback.
	Updated bool
	Kept    bool
	// Current means the newest release was already installed: nothing changed.
	Current bool
}

// emuWinProgress reports one install's phase and byte progress; any of it may
// be empty.
type emuWinProgress func(phase string, done, total int64)

// emuWinInstall downloads, verifies and unpacks spec's newest Windows build
// into its folder, or updates the folder when JukaHub installed it before.
func emuWinInstall(ctx context.Context, client *http.Client, spec emuWinSpec, progress emuWinProgress) (emuWinInstallResult, error) {
	report := func(phase string, done, total int64) {
		if progress != nil {
			progress(phase, done, total)
		}
	}
	result := emuWinInstallResult{Name: spec.Name}
	if !spec.installable() {
		return result, fmt.Errorf("%s has no download JukaHub can install — get it from %s", spec.Name, spec.Site)
	}
	report(installPhaseResolving, 0, 0)
	release, err := emuWinResolve(ctx, client, spec)
	if err != nil {
		return result, err
	}
	result.Tag = release.Tag
	maxBytes := spec.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	if release.Size > maxBytes {
		return result, fmt.Errorf("the %s download is %s, more than the %s limit", spec.Name, formatInstallBytes(release.Size), formatInstallBytes(maxBytes))
	}
	root, err := emuWinInstallRoot(spec.Name)
	if err != nil {
		return result, err
	}
	dest := filepath.Join(root, spec.Name)
	result.Dir = dest
	installed, managed := emuWinReadManifest(dest)
	if managed && !emuWinUpdateAvailable(installed, release) && regularFile(filepath.Join(dest, installed.Exe)) {
		result.Current = true
		result.Exe = filepath.Join(dest, installed.Exe)
		return result, nil
	}

	archive := filepath.Join(root, ".downloads", sanitizeFileName(release.Asset))
	report(installPhaseDownload, 0, release.Size)
	sum, err := emuDownload(ctx, client, release.URL, archive, maxBytes, func(done, total int64) {
		report(installPhaseDownload, done, total)
	})
	if err != nil {
		return result, err
	}
	defer os.Remove(archive)
	report(installPhaseVerify, 0, 0)
	if err := emuDigestMatches(sum, release.Digest); err != nil {
		return result, err
	}
	if release.Digest == "" {
		log.Printf("[emulators] %s %s has no published digest; verified by TLS from the allowlisted host only", spec.Name, release.Tag)
	}

	stage, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	report(installPhaseExtract, 0, 0)
	if strings.HasSuffix(strings.ToLower(release.Asset), ".7z") {
		err = emu7zExtract(ctx, client, archive, stage)
	} else {
		err = emuExtractZip(ctx, archive, stage, func(done, total int64) {
			report(installPhaseExtract, done, total)
		})
	}
	if err != nil {
		return result, err
	}
	content := emuArchiveContentRoot(stage)
	exeRel, ok := emuFindLauncher(content, emuWinLauncherNames(spec.Name))
	if !ok {
		return result, fmt.Errorf("the %s download does not contain %s", spec.Name, strings.Join(emuWinLauncherNames(spec.Name), " or "))
	}

	manifest := emuWinManifest{
		Name:        spec.Name,
		Source:      "github:" + spec.Repo,
		Tag:         release.Tag,
		Asset:       release.Asset,
		URL:         release.URL,
		Digest:      release.Digest,
		AssetDate:   release.AssetDate,
		Exe:         exeRel,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	if spec.Buildbot {
		manifest.Source = "buildbot.libretro.com"
	}

	if destInfo, statErr := os.Stat(dest); statErr == nil && destInfo.IsDir() {
		// An update: lay the release over the folder, keeping what it replaces.
		backup := emuWinBackupDir(dest)
		pending := backup + ".new"
		_ = os.RemoveAll(pending)
		added, overlayErr := emuOverlay(content, dest, pending)
		if overlayErr != nil {
			_ = os.RemoveAll(pending)
			return result, overlayErr
		}
		if managed {
			_ = os.MkdirAll(pending, 0o755)
			_ = os.Rename(filepath.Join(dest, emuWinManifestName), filepath.Join(pending, emuWinManifestName))
		}
		kept := emuWinBackupInfo{Name: spec.Name, Tag: installed.Tag, SavedAt: time.Now().UTC().Format(time.RFC3339), Added: added}
		if data, marshalErr := json.MarshalIndent(kept, "", "  "); marshalErr == nil {
			_ = os.MkdirAll(pending, 0o755)
			_ = os.WriteFile(filepath.Join(pending, emuWinBackupManifestName), append(data, '\n'), 0o644)
		}
		_ = os.RemoveAll(backup)
		if renameErr := os.Rename(pending, backup); renameErr != nil {
			log.Printf("[emulators] could not keep the previous %s: %v", spec.Name, renameErr)
			_ = os.RemoveAll(pending)
		} else {
			result.Kept = true
		}
		result.Updated = true
	} else if moveErr := emuMoveTree(content, dest); moveErr != nil {
		return result, moveErr
	}
	if err := emuWinWriteManifest(dest, manifest); err != nil {
		log.Printf("[emulators] could not record the %s install: %v", spec.Name, err)
	}
	if spec.Buildbot {
		// A portable RetroArch loads cores from the folder beside it; creating
		// it now is what lets the core installer find this install.
		_ = os.MkdirAll(filepath.Join(dest, "cores"), 0o755)
	}
	result.Exe = filepath.Join(dest, exeRel)
	log.Printf("[emulators] installed %s %s into %s", spec.Name, release.Tag, dest)
	return result, nil
}

// emuWinRollback puts back the version the last update of name replaced: the
// kept files return, the files that update added are removed, and the old
// manifest comes back with them. The newer release can be installed again with
// Update.
func emuWinRollback(name string) (emuWinBackupInfo, error) {
	dir, _, ok := emuWinManaged(name)
	if !ok {
		return emuWinBackupInfo{}, fmt.Errorf("%s was not installed by JukaHub", name)
	}
	backup := emuWinBackupDir(dir)
	raw, err := os.ReadFile(filepath.Join(backup, emuWinBackupManifestName))
	if err != nil {
		return emuWinBackupInfo{}, fmt.Errorf("no previous %s version is kept", name)
	}
	var info emuWinBackupInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return emuWinBackupInfo{}, fmt.Errorf("the kept %s version is unreadable", name)
	}
	var rels []string
	walkErr := filepath.WalkDir(backup, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(backup, p)
		if err != nil {
			return err
		}
		if rel != emuWinBackupManifestName {
			rels = append(rels, rel)
		}
		return nil
	})
	if walkErr != nil {
		return info, walkErr
	}
	// The version being restored may predate JukaHub managing the folder, so
	// its manifest is not in the kept set. The current one then stays, retagged
	// so the folder no longer claims the release it just gave up.
	if !regularFile(filepath.Join(backup, emuWinManifestName)) {
		if current, ok := emuWinReadManifest(dir); ok {
			current.Tag = info.Tag
			if current.Tag == "" {
				current.Tag = "rolled-back"
			}
			current.Digest, current.AssetDate = "", ""
			_ = emuWinWriteManifest(dir, current)
		}
	}
	for _, rel := range rels {
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return info, err
		}
		if err := os.Rename(filepath.Join(backup, rel), target); err != nil {
			return info, fmt.Errorf("%s is in use — close the emulator and try again (%v)", rel, err)
		}
	}
	for _, rel := range info.Added {
		clean, err := emuArchiveRel(filepath.ToSlash(rel))
		if err != nil || clean == "" {
			continue
		}
		_ = os.Remove(filepath.Join(dir, clean))
	}
	_ = os.RemoveAll(backup)
	log.Printf("[emulators] rolled %s back to %s", name, info.Tag)
	return info, nil
}

// ── Running an install from the page ────────────────────────────────────────

// emuInstalling names the emulator whose install is running, so its tile can
// say so. Written by the install goroutine, read by the render thread.
var emuInstalling struct {
	mu   sync.Mutex
	name string
}

func emuSetInstalling(name string) {
	emuInstalling.mu.Lock()
	emuInstalling.name = name
	emuInstalling.mu.Unlock()
}

// emuInstallingName is the tile name being installed right now, or "".
func emuInstallingName() string {
	emuInstalling.mu.Lock()
	defer emuInstalling.mu.Unlock()
	return emuInstalling.name
}

// emuInstallBarReport drives the install bar from an emuWinProgress callback.
func emuInstallBarReport(phase string, done, total int64) {
	if phase != "" {
		installBarPhase(phase)
	}
	if done > 0 || total > 0 {
		installBarProgress(done, total)
	}
}

// emuFriendlyError is the sentence a failed install shows: the offline notice
// for network failures, a plain word for the server's refusals, and the error's
// first line otherwise.
func emuFriendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "Cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The download took too long — try again on a faster connection"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "HTTP 403"), strings.Contains(msg, "HTTP 429"):
		return "GitHub is limiting downloads right now — try again in a while"
	case strings.Contains(msg, "HTTP 404"):
		return "The download is not on the server any more — try again later"
	case strings.Contains(msg, "HTTP 5"):
		return "The download server is having trouble — try again later"
	}
	return plainNetworkError(err)
}

// emuOpenSite opens an emulator's official download page.
func emuOpenSite(spec emuWinSpec) {
	site := strings.TrimSpace(spec.Site)
	if site == "" {
		ShowToast(spec.Name+" has no download JukaHub can install", ToastKindInfo)
		return
	}
	if err := P().OpenURL(site); err != nil {
		ShowToast("Could not open "+site, ToastKindError)
		return
	}
	PlayActivateSound()
	ShowToast("Opened the "+spec.Name+" download page in your browser", ToastKindInfo)
}

// emuWinStartInstall installs or updates one Windows emulator off the event
// thread, reporting through the install bar and a toast.
func emuWinStartInstall(spec emuWinSpec) {
	if !spec.installable() {
		emuOpenSite(spec)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to install "+spec.Name, ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	_, _, updating := emuWinManaged(spec.Name)
	if updating {
		installBarBeginLabelled(spec.Name, "Updating "+spec.Name)
	} else {
		installBarBegin(spec.Name)
	}
	installBarPhase(installPhaseResolving)
	emuSetInstalling(spec.Name)
	go func() {
		defer endPatchOp()
		defer emuSetInstalling("")
		ctx, cancel := context.WithTimeout(installBarRunContext(), emuWinInstallTimeout)
		defer cancel()
		client := emuWinClient()
		result, err := emuWinInstall(ctx, client, spec, emuInstallBarReport)
		if err == nil && strings.EqualFold(spec.Name, "xemu") {
			// xemu also needs a hard-disk image, and the xemu project publishes
			// a free one: fetch it with the emulator so only the firmware is
			// left for the user to supply.
			emuXboxAfterInstall(ctx, client)
		}
		emuWinFinish(spec, result, err)
	}()
}

// emuWinFinish reports one install's outcome.
func emuWinFinish(spec emuWinSpec, result emuWinInstallResult, err error) {
	if err != nil {
		msg := emuFriendlyError(err)
		if errors.Is(err, context.Canceled) {
			installBarAbort()
			ShowToast(spec.Name+" install cancelled", ToastKindInfo)
			return
		}
		installBarFail(installCardDetail(msg))
		ShowToast(spec.Name+": "+msg, ToastKindError)
		log.Printf("[emulators] %s install failed: %v", spec.Name, err)
		return
	}
	installBarPhase(installPhaseDone)
	installBarDone()
	invalidateEmulatorCache()
	emuForgetUpdate(spec.Name)
	if result.Exe != "" {
		requestAppIconExtraction([]string{result.Exe})
	}
	switch {
	case result.Current:
		ShowToast(spec.Name+" is already the newest version ("+result.Tag+")", ToastKindInfo)
	case result.Updated:
		PlaySuccessSound()
		note := spec.Name + " updated to " + result.Tag
		if result.Kept {
			note += " · previous version kept"
		}
		ShowToast(note, ToastKindSuccess)
	case strings.EqualFold(spec.Name, "xemu"):
		PlaySuccessSound()
		ShowToast("xemu "+result.Tag+" installed · press Y on its tile for the Xbox BIOS setup", ToastKindSuccess)
	default:
		PlaySuccessSound()
		ShowToast(spec.Name+" "+result.Tag+" installed", ToastKindSuccess)
	}
}

// emuWinStartRollback restores the kept version of a managed emulator.
func emuWinStartRollback(name string) {
	info, err := emuWinRollback(name)
	if err != nil {
		ShowToast(name+": "+err.Error(), ToastKindError)
		return
	}
	invalidateEmulatorCache()
	emuForgetUpdate(name)
	PlaySuccessSound()
	label := info.Tag
	if label == "" {
		label = "the previous version"
	}
	ShowToast(name+" rolled back to "+label, ToastKindSuccess)
}
