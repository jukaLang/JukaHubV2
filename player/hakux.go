package main

// hakux.go — the original Xbox on the TrimUI Smart Pro, through hakuX.
//
// hakuX is an original-Xbox emulator that began as an Android fork of xemu; its
// own releases since 1.0 run games on a JIT and a reimplemented Xbox kernel.
// ToastedFrog2 publishes an experimental port of it for the original Smart Pro
// (TG5040) at github.com/ToastedFrog2/hakux-trimui-smart-pro: one release asset,
// xbox-trimui-smart-pro-v<version>.zip, whose Data/ and Emus/ folders are
// merged into the SD-card root. The package's Emus/XBOX/launch.sh is a
// firmware-style launcher (it pauses the music service, sets up swap and
// restores the stock OSD afterwards), games stay in Roms/XBOX, Menu opens
// hakuX's settings in a game and holding Start+Select for about 1.2 s exits.
//
// This file installs and updates that package the way the Windows page installs
// an emulator (emuwininstall.go), with the same pieces:
//
//   - the release comes from the GitHub API through fetchUpdateBytes, so every
//     URL — the API, the asset, each redirect — is held to the download
//     allowlist;
//   - the archive is checked against GitHub's own SHA-256 digest for the asset
//     and, when the release attaches one, its checksum file (the toolbox's
//     parser); the SHA-256 of what was downloaded is recorded either way;
//   - emuExtractZip unpacks it through an os.Root into JukaHub's data folder on
//     the same card, and only a complete, verified tree is merged — file by file,
//     through an os.Root opened on the SD root, so no path can leave the card;
//   - a file no install of the package laid down is never touched. One it
//     ships that is already there with the same bytes stays as it is; one it
//     replaces is moved into a kept copy first, so an update can be rolled back
//     (R1) and a merge that fails half way is put back exactly as it was. One
//     the previous version laid down and this one no longer ships joins the
//     kept copy too, if it is still byte for byte what was installed (a changed
//     one is hakuX's or the user's by now and stays). A replaced file the
//     user had changed since JukaHub laid it down is also copied to
//     data/hakux/edited, which no later update rotates away, and originals a
//     merge could not put back are set aside in data/hakux/recovered-<time>,
//     never deleted;
//   - a provenance file (.jukahub-hakux.json) in the launcher folder records the
//     release, the digest, how the download was verified and every file laid
//     down.
//
// What it does not do: build hakuX (the port's repository carries the source
// snapshots, patches and build instructions; JukaHub installs the published
// package), touch Roms/XBOX beyond creating it, or run anything from the package.
// Games start through the package's own launcher like every Emus/ folder; the
// page side (the Xbox tile, its actions and the setup sheet) is hakuxpage.go.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// hakuxRepo is the TG5040 port's GitHub project.
	hakuxRepo = "ToastedFrog2/hakux-trimui-smart-pro"
	// hakuxName is how the page and its toasts name the emulator.
	hakuxName = "hakuX"
	// hakuxMaxBytes caps the package download. It only stops a runaway
	// transfer; the free-space check before the download protects the card.
	hakuxMaxBytes = int64(512 << 20)
	// hakuxManifestName is the provenance file in the launcher folder.
	hakuxManifestName = ".jukahub-hakux.json"
	// hakuxBackupInfoName describes the kept copy an update made.
	hakuxBackupInfoName = ".jukahub-backup.json"
	// hakuxKeptManifestName is the previous provenance file inside that copy.
	hakuxKeptManifestName = "previous-hakux.json"
	// hakuxRomsFolder is the Roms/ folder the package's launcher plays from.
	hakuxRomsFolder = "XBOX"
	// hakuxStateFallback is JukaHub's hakuX folder at the card's root, used
	// only when JukaHub's own data folder is not on the card.
	hakuxStateFallback = ".jukahub-hakux"
)

// ── Devices ─────────────────────────────────────────────────────────────────

// hakuxSupport is what the Xbox tile can offer on one handheld.
type hakuxSupport struct {
	// Offer means the install is offered at all.
	Offer bool
	// Untested is the caveat for a model the port was not made for but whose
	// chip, GPU and sticks match the Smart Pro's ("" for the Smart Pro itself).
	Untested string
	// Reason says, in a sentence, why there is no install.
	Reason string
}

// hakuxSupportFor decides per model. The package is built for the original
// Smart Pro (TG5040: Allwinner A133P, PowerVR GE8300, 1 GB). The Brick Pro has
// the same chip and GPU and two analog sticks, so it is offered and labelled
// untested; the Brick has the same chip but no sticks, which Xbox games are
// played with; the Smart Pro S is a different chip and GPU (A523, Mali) that
// no build exists for.
func hakuxSupportFor(device DeviceModel) hakuxSupport {
	switch device {
	case DeviceTrimuiSmartPro:
		return hakuxSupport{Offer: true}
	case DeviceTrimuiBrickPro:
		return hakuxSupport{Offer: true, Untested: "Built for the Smart Pro and untested on the Brick Pro (same A133P chip, GPU and sticks, but a 4:3 screen)."}
	case DeviceTrimuiBrick:
		return hakuxSupport{Reason: "The hakuX port is built for the Smart Pro, and Xbox games need two analog sticks — the Brick has none."}
	case DeviceTrimuiHammer:
		return hakuxSupport{Reason: "The hakuX port is built for the Smart Pro, and Xbox games need two analog sticks — the Hammer (the Brick's board) has none."}
	case DeviceTrimuiSmartProS:
		return hakuxSupport{Reason: "The hakuX port is built only for the original Smart Pro (TG5040: A133P, PowerVR GPU). The Smart Pro S has a different chip, GPU and firmware (A523, Mali), and no build for it has been published yet."}
	}
	return hakuxSupport{Reason: "JukaHub sets up Xbox games on the TrimUI Smart Pro (hakuX) and on Windows (xemu); this device has neither."}
}

// hakuxOffered reports whether this device is offered the hakuX install.
func hakuxOffered() bool {
	return !IsWindows() && hakuxSupportFor(hakuxDevice()).Offer
}

// hakuxDevice is DetectDevice with one correction for this package. Detection
// falls back to a family's base model, so a Smart Pro S named only by its panel
// can read as a Smart Pro and would be offered an A133P/PowerVR build. The
// A133P's four cores are all Cortex-A53 (CPU part 0xd03); the Smart Pro S's
// A523 reports other parts, which tells them apart without trusting a name.
func hakuxDevice() DeviceModel {
	d := DetectDevice()
	if d == DeviceTrimuiSmartPro && !hakuxCPUIsA133P() {
		return DeviceTrimuiSmartProS
	}
	return d
}

// hakuxCPUIsA133P reads /proc/cpuinfo: every listed core must be a Cortex-A53.
// Without the file (not Linux) it falls back to the core count.
func hakuxCPUIsA133P() bool {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return runtime.NumCPU() <= 4
	}
	parts := 0
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "CPU part" {
			continue
		}
		parts++
		if strings.TrimSpace(value) != "0xd03" {
			return false
		}
	}
	return parts > 0 || runtime.NumCPU() <= 4
}

// ── Release ─────────────────────────────────────────────────────────────────

// hakuxAssetPattern is the package's release asset name:
// xbox-trimui-smart-pro-v0.1.3.zip. "-v" must follow "smart-pro" directly, so
// a later Smart Pro S package (xbox-trimui-smart-pro-s-…) or a checksum side-car
// (….zip.sha256) can never be taken for it.
var hakuxAssetPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^xbox-trimui-smart-pro-v([0-9]+(?:\.[0-9]+)*(?:[-+.][0-9a-z.]+)?)\.zip$`)
})

// hakuxAssetVersion is the version a package asset name carries, or "" when
// the name is not the TG5040 package.
func hakuxAssetVersion(name string) string {
	m := hakuxAssetPattern().FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return ""
	}
	return m[1]
}

// hakuxPickAsset returns a release's TG5040 package: the asset whose version is
// the release tag's when there are several, else the newest version, where a
// final release beats its release candidates (0.1.3 over 0.1.3-rc1,
// emuCompareReleaseVersions). An asset without a download URL never qualifies.
func hakuxPickAsset(assets []emuGitHubAsset, tag string) (emuGitHubAsset, bool) {
	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tag)), "v")
	var pick emuGitHubAsset
	pickVersion := ""
	for _, a := range assets {
		version := hakuxAssetVersion(a.Name)
		if version == "" || strings.TrimSpace(a.BrowserDownloadURL) == "" {
			continue
		}
		if want != "" && strings.EqualFold(version, want) {
			return a, true
		}
		if pickVersion == "" || emuCompareReleaseVersions(version, pickVersion) > 0 {
			pick, pickVersion = a, version
		}
	}
	return pick, pickVersion != ""
}

// hakuxRelease is one installable release of the package.
type hakuxRelease struct {
	Release    emuWinRelease
	Prerelease bool
	// Sums is a checksum file attached to the release (Name "" when none);
	// SumsSingle marks a per-file side-car that may hold the hash alone.
	Sums       releaseAsset
	SumsSingle bool
}

// hakuxReleaseFrom picks the package out of one release's metadata.
func hakuxReleaseFrom(rel emuGitHubRelease) (hakuxRelease, bool) {
	if rel.Draft {
		return hakuxRelease{}, false
	}
	a, ok := hakuxPickAsset(rel.Assets, rel.TagName)
	if !ok {
		return hakuxRelease{}, false
	}
	out := hakuxRelease{Release: emuReleaseFrom(rel, a), Prerelease: rel.Prerelease}
	assets := make([]releaseAsset, 0, len(rel.Assets))
	for _, x := range rel.Assets {
		assets = append(assets, releaseAsset{Name: x.Name, URL: x.BrowserDownloadURL, Size: x.Size})
	}
	if sums, single, found := toolboxChecksumAsset(assets, a.Name); found {
		out.Sums, out.SumsSingle = sums, single
	}
	return out, true
}

// hakuxFetchRelease resolves the newest package: GitHub's latest release, then
// the recent release list, because an experimental port may publish
// pre-releases only and "latest" never returns one of those.
func hakuxFetchRelease(ctx context.Context, client *http.Client) (hakuxRelease, error) {
	var lastErr error
	latest := fmt.Sprintf("%s/repos/%s/releases/latest", standaloneAPIBase, hakuxRepo)
	if data, err := fetchUpdateBytes(ctx, client, latest, patchMaxMetadata); err != nil {
		lastErr = err
	} else {
		var rel emuGitHubRelease
		if err := json.Unmarshal(data, &rel); err != nil {
			lastErr = errors.New("unreadable hakuX release metadata")
		} else if out, ok := hakuxReleaseFrom(rel); ok {
			return out, nil
		} else {
			lastErr = errors.New("the newest hakuX release has no xbox-trimui-smart-pro-v<version>.zip package")
		}
	}
	if err := ctx.Err(); err != nil {
		return hakuxRelease{}, err
	}
	list := fmt.Sprintf("%s/repos/%s/releases?per_page=10", standaloneAPIBase, hakuxRepo)
	data, err := fetchUpdateBytes(ctx, client, list, patchMaxMetadata)
	if err != nil {
		if lastErr == nil {
			lastErr = err
		}
		return hakuxRelease{}, lastErr
	}
	var releases []emuGitHubRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return hakuxRelease{}, errors.New("unreadable hakuX release list")
	}
	for _, rel := range releases {
		if out, ok := hakuxReleaseFrom(rel); ok {
			return out, nil
		}
	}
	// The list answered: whatever "latest" said (a 404 when every release is a
	// pre-release), the real answer is that no release carries the package.
	return hakuxRelease{}, errors.New("no hakuX release with a Smart Pro package was found")
}

// hakuxVerify checks a downloaded package against what the project publishes:
// GitHub's SHA-256 digest for the asset, then a checksum file attached to the
// release. It returns how the file was verified, "" when nothing was published
// (the caller then records the hash it computed). A mismatch is an error, and so
// is a published checksum that cannot be read when nothing else vouches for the
// file.
func hakuxVerify(ctx context.Context, client *http.Client, rel hakuxRelease, sum string) (string, error) {
	var how []string
	if strings.TrimSpace(rel.Release.Digest) != "" {
		if err := emuDigestMatches(sum, rel.Release.Digest); err != nil {
			return "", err
		}
		how = append(how, "GitHub asset digest")
	}
	if strings.TrimSpace(rel.Sums.URL) != "" {
		data, err := fetchUpdateBytes(ctx, client, rel.Sums.URL, 1<<20)
		switch {
		case err != nil && len(how) == 0:
			return "", fmt.Errorf("could not read the published checksum %s: %w", rel.Sums.Name, err)
		case err != nil:
			logWarn("emulators", "hakuX: %s unreadable (%v); the GitHub digest matched", rel.Sums.Name, err)
		default:
			verified, err := toolboxMatchChecksum(sum, string(data), rel.Release.Asset, rel.SumsSingle)
			if err != nil {
				return "", err
			}
			switch {
			case verified:
				how = append(how, rel.Sums.Name)
			case len(how) == 0:
				// A published checksum file that does not name this download
				// is not "nothing published": refuse rather than trust it.
				return "", fmt.Errorf("%s does not list %s, and GitHub published no digest for it", rel.Sums.Name, rel.Release.Asset)
			default:
				logWarn("emulators", "hakuX: %s does not list %s; the GitHub digest matched", rel.Sums.Name, rel.Release.Asset)
			}
		}
	}
	return strings.Join(how, " + "), nil
}

// ── Provenance ──────────────────────────────────────────────────────────────

// hakuxFileRecord is one file the package laid down, as a slash path relative
// to the SD root.
type hakuxFileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// hakuxManifest is the provenance file: which release is installed, where it
// came from, how the download was verified, and every file it put on the card.
type hakuxManifest struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Tag    string `json:"tag"`
	Asset  string `json:"asset"`
	URL    string `json:"url"`
	// Digest is GitHub's "sha256:<hex>" for the asset ("" when it published
	// none); SHA256 is the hash of the bytes that were downloaded.
	Digest string `json:"digest,omitempty"`
	SHA256 string `json:"sha256"`
	// Verified says what the download was checked against; "" means nothing
	// was published and only TLS from the allowlisted host vouches for it.
	Verified    string            `json:"verified"`
	AssetDate   string            `json:"asset_updated_at,omitempty"`
	Prerelease  bool              `json:"prerelease,omitempty"`
	Device      string            `json:"device"`
	Launcher    string            `json:"launcher"`
	InstalledAt string            `json:"installed_at"`
	Files       []hakuxFileRecord `json:"files"`
}

// hakuxReadManifest reads a launcher folder's provenance; ok is false for a
// folder JukaHub did not install.
func hakuxReadManifest(folder string) (hakuxManifest, bool) {
	raw, err := os.ReadFile(filepath.Join(folder, hakuxManifestName))
	if err != nil {
		return hakuxManifest{}, false
	}
	var m hakuxManifest
	if json.Unmarshal(raw, &m) != nil || strings.TrimSpace(m.Tag) == "" {
		return hakuxManifest{}, false
	}
	return m, true
}

// hakuxWriteManifest writes the provenance file through a temporary file and a
// rename, without AtomicWrite's .bak: an update keeps the previous provenance
// in the kept copy instead, and a rollback must not find a stale one beside it.
func hakuxWriteManifest(folder string, m hakuxManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(folder, hakuxManifestName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// hakuxAsWinManifest lets emuWinUpdateAvailable compare an installed package
// with a release, by the same rules the Windows emulators use.
func hakuxAsWinManifest(m hakuxManifest) emuWinManifest {
	return emuWinManifest{Name: hakuxName, Tag: m.Tag, Digest: m.Digest, AssetDate: m.AssetDate}
}

// hakuxNewer reports whether a release should replace the installed package,
// by emuWinUpdateAvailable's rules: version tags compare as releases, so the
// final "v0.1.4" replaces an installed "v0.1.4-rc1" and a "v0.1.4-rc2" never
// replaces the final "v0.1.4"; a re-published asset under the same tag is
// told by its digest.
func hakuxNewer(m hakuxManifest, release emuWinRelease) bool {
	return emuWinUpdateAvailable(hakuxAsWinManifest(m), release)
}

// hakuxCardRel checks a card path read back from JukaHub's own records (the
// provenance file, the kept copy's description): it must be a safe path inside
// the card's Data/ or Emus/ folder, the only places an install writes.
func hakuxCardRel(rel string) (string, error) {
	clean, err := hakuxSafeRel(rel)
	if err != nil {
		return "", err
	}
	top, rest, _ := strings.Cut(clean, "/")
	if rest == "" || (top != "Data" && top != "Emus") {
		return "", fmt.Errorf("%q is outside the card's Data and Emus folders", rel)
	}
	return clean, nil
}

// hakuxIntact reports whether every file an install recorded is still on the
// card. Sizes are not compared: hakuX and its launcher rewrite some of their
// own files (settings, first-run markers), and a changed size there is use,
// not damage. A missing file makes X put the missing files back.
func hakuxIntact(sdRoot string, m hakuxManifest) bool {
	if len(m.Files) == 0 {
		return false
	}
	for _, f := range m.Files {
		rel, err := hakuxCardRel(f.Path)
		if err != nil {
			return false
		}
		info, err := os.Stat(filepath.Join(sdRoot, hakuxOS(rel)))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// hakuxKnownHashes maps each file an install recorded to its SHA-256, so the
// next merge can tell a file the user changed from the one JukaHub laid down.
func hakuxKnownHashes(m hakuxManifest) map[string]string {
	known := make(map[string]string, len(m.Files))
	for _, f := range m.Files {
		if rel, err := hakuxCardRel(f.Path); err == nil && f.SHA256 != "" {
			known[rel] = strings.ToLower(f.SHA256)
		}
	}
	return known
}

// ── Where it lives ──────────────────────────────────────────────────────────

// hakuxPaths are the folders one install works with. SDRoot is where the
// package's Data/ and Emus/ land and Roms the card's Roms folder; State is
// JukaHub's own hakuX folder (staging and the kept copy) as a slash path
// relative to SDRoot, so every move of a file is a rename inside one os.Root on
// one file system.
type hakuxPaths struct {
	SDRoot string
	State  string
	Roms   string
}

func (p hakuxPaths) stage() string   { return path.Join(p.State, "stage") }
func (p hakuxPaths) backup() string  { return path.Join(p.State, "backup") }
func (p hakuxPaths) pending() string { return path.Join(p.State, "backup.new") }

// hakuxOS turns a slash path relative to a root into the form os.Root takes.
func hakuxOS(rel string) string { return filepath.FromSlash(rel) }

// hakuxDevicePaths is the handheld's layout: the card the Emus/ and Roms/
// folders are on, and JukaHub's data folder on it.
func hakuxDevicePaths() hakuxPaths {
	sd := filepath.Dir(deviceRomsRoot)
	p := hakuxPaths{SDRoot: sd, State: hakuxStateFallback, Roms: deviceRomsRoot}
	if data, err := P().DataDir(); err == nil && strings.TrimSpace(data) != "" {
		if rel, err := filepath.Rel(sd, filepath.Join(data, "hakux")); err == nil && !filepath.IsAbs(rel) &&
			rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			p.State = filepath.ToSlash(rel)
		}
	}
	return p
}

// hakuxIsXboxFolder reports whether an Emus/ folder is the Xbox launcher: the
// package's XBOX folder (or a hand-made one under a common Xbox name), or any
// folder JukaHub's provenance file sits in.
func hakuxIsXboxFolder(name, folder string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "xbox", "xbox1", "ogxbox", "hakux":
		return true
	}
	return regularFile(filepath.Join(folder, hakuxManifestName))
}

// hakuxLauncherIn is a launcher folder's entry point, or "" when it has none:
// the script MainUI would start, by the rules every Emus/ folder follows
// (emuFolderLauncher) — the .sh its config.json names in "launch", which on
// CrossMix may be another script than launch.sh, then launch.sh and launch.
func hakuxLauncherIn(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return ""
	}
	return emuFolderLauncher(folder, emuFolderConfig(folder))
}

// hakuxInstalled finds the Xbox launcher folder under the Emus roots (the SD
// card first) and, when JukaHub installed it, its provenance. A folder with a
// launcher wins over one without in the same root.
func hakuxInstalled(emuRoots []string) (folder string, m hakuxManifest, managed bool) {
	for _, root := range emuRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range entries {
			if !de.IsDir() {
				continue
			}
			candidate := filepath.Join(root, de.Name())
			if !hakuxIsXboxFolder(de.Name(), candidate) {
				continue
			}
			if folder == "" || (hakuxLauncherIn(folder) == "" && hakuxLauncherIn(candidate) != "") {
				folder = candidate
			}
		}
		if folder != "" {
			break
		}
	}
	if folder == "" {
		return "", hakuxManifest{}, false
	}
	m, managed = hakuxReadManifest(folder)
	return folder, m, managed
}

// ── Merge plan ──────────────────────────────────────────────────────────────

// hakuxTopFolders maps the package folders that are merged to the spelling the
// TrimUI card uses, so a package that says "emus" cannot create a second
// folder next to "Emus" on a case-sensitive card.
var hakuxTopFolders = map[string]string{"data": "Data", "emus": "Emus"}

// hakuxPlanFile is one file of a merge: where it sits in the unpacked package
// and where it goes on the card — the same path, with the top folder spelled
// the card's way. Both are slash paths.
type hakuxPlanFile struct {
	From string
	To   string
}

// hakuxMergePlan is what one package lays onto the card: its files and the
// folders under Data/ and Emus/ (card paths, relative to the SD root), the
// launcher it carries, and the top-level names it carries besides, which are
// not merged.
type hakuxMergePlan struct {
	Files    []hakuxPlanFile
	Dirs     []string
	Launcher string
	Skipped  []string
}

// hakuxSafeRel cleans one slash path from the package and refuses anything
// that could name a place outside the folder it is joined to.
func hakuxSafeRel(rel string) (string, error) {
	r := strings.TrimSpace(rel)
	if r == "" || strings.ContainsAny(r, "\\\x00") || unsafeArchiveName(r) {
		return "", fmt.Errorf("unsafe path %q in the hakuX package", rel)
	}
	clean := path.Clean(r)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("unsafe path %q in the hakuX package", rel)
	}
	return clean, nil
}

// hakuxPlanMerge decides what an unpacked package merges: everything under its
// Data/ and Emus/ folders, nothing else. It refuses an unsafe path, a file
// where one of those folders belongs, two entries one FAT card would see as the
// same file, and a package without an Emus/<system>/launch.sh.
func hakuxPlanMerge(files, dirs []string) (hakuxMergePlan, error) {
	var plan hakuxMergePlan
	skipped := map[string]bool{}
	canon := func(rel string) (string, bool, error) {
		clean, err := hakuxSafeRel(rel)
		if err != nil {
			return "", false, err
		}
		top, rest, _ := strings.Cut(clean, "/")
		name, ok := hakuxTopFolders[strings.ToLower(top)]
		if !ok {
			skipped[top] = true
			return "", false, nil
		}
		if rest == "" {
			return name, true, nil
		}
		return name + "/" + rest, true, nil
	}
	seen := map[string]bool{}
	for _, f := range files {
		rel, ok, err := canon(f)
		if err != nil {
			return hakuxMergePlan{}, err
		}
		if !ok {
			continue
		}
		if !strings.Contains(rel, "/") {
			return hakuxMergePlan{}, fmt.Errorf("the hakuX package has a file where its %s folder belongs", rel)
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return hakuxMergePlan{}, fmt.Errorf("the hakuX package names %s twice", rel)
		}
		seen[key] = true
		from, _ := hakuxSafeRel(f)
		plan.Files = append(plan.Files, hakuxPlanFile{From: from, To: rel})
	}
	for _, d := range dirs {
		rel, ok, err := canon(d)
		if err != nil {
			return hakuxMergePlan{}, err
		}
		if ok && strings.Contains(rel, "/") {
			plan.Dirs = append(plan.Dirs, rel)
		}
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].To < plan.Files[j].To })
	sort.Strings(plan.Dirs)
	for _, f := range plan.Files {
		parts := strings.Split(f.To, "/")
		if len(parts) != 3 || parts[0] != "Emus" || !strings.EqualFold(parts[2], "launch.sh") {
			continue
		}
		// Emus/XBOX is the package's own folder; another one only counts when
		// the package has no XBOX folder at all.
		if plan.Launcher == "" || strings.EqualFold(parts[1], hakuxRomsFolder) {
			plan.Launcher = f.To
		}
		if strings.EqualFold(parts[1], hakuxRomsFolder) {
			break
		}
	}
	if plan.Launcher == "" {
		return hakuxMergePlan{}, errors.New("the download has no Emus/<system>/launch.sh — it is not the hakuX package")
	}
	for name := range skipped {
		plan.Skipped = append(plan.Skipped, name)
	}
	sort.Strings(plan.Skipped)
	return plan, nil
}

// hakuxContentRoot is the folder of an unpacked package that holds its Emus/
// folder: the archive's top, or a single wrapper folder up to three levels
// down (a zip made on a Mac carries a __MACOSX folder beside it, which does not
// count). A tree without one is returned as it is, and its plan then fails.
func hakuxContentRoot(dir string) string {
	current := dir
	for depth := 0; depth < 3; depth++ {
		entries, err := os.ReadDir(current)
		if err != nil {
			return dir
		}
		only, dirs := "", 0
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.EqualFold(e.Name(), "__MACOSX") {
				continue
			}
			if strings.EqualFold(e.Name(), "emus") {
				return current
			}
			dirs++
			only = e.Name()
		}
		if dirs != 1 {
			return dir
		}
		current = filepath.Join(current, only)
	}
	return dir
}

// hakuxListTree lists an unpacked package's regular files and folders as slash
// paths relative to dir. emuExtractZip never writes anything else, so a link or
// device here means the folder is not what was unpacked, and it is refused.
func hakuxListTree(dir string) (files, dirs []string, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			dirs = append(dirs, rel)
		case d.Type().IsRegular():
			files = append(files, rel)
		default:
			return fmt.Errorf("%s in the hakuX package is not a regular file", rel)
		}
		return nil
	})
	return files, dirs, err
}

// ── Merge ───────────────────────────────────────────────────────────────────

// hakuxMergeResult is what a merge did, as slash paths relative to the SD root.
type hakuxMergeResult struct {
	Added     []string
	Replaced  []string
	Unchanged []string
	Records   []hakuxFileRecord
	// Edited are the replaced files whose bytes differed from what the
	// previous install recorded: the user changed them.
	Edited []string
	// Created are the folders under Data/ and Emus/ the merge made.
	Created []string
}

// hakuxHashIn is the SHA-256 and size of a file inside root.
func hakuxHashIn(root *os.Root, name string) (string, int64, error) {
	f, err := root.Open(hakuxOS(name))
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// hakuxMove renames a file inside root, and copies it when the rename is
// refused (two file systems under one root). The copy never overwrites: the
// merge only moves a file onto a name it has just cleared.
func hakuxMove(root *os.Root, from, to string) error {
	if err := root.Rename(hakuxOS(from), hakuxOS(to)); err == nil {
		return nil
	}
	in, err := root.Open(hakuxOS(from))
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := in.Stat(); err == nil {
		mode = info.Mode().Perm()
	}
	out, err := root.OpenFile(hakuxOS(to), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		in.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	in.Close()
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = root.Remove(hakuxOS(to))
		return copyErr
	}
	// The file is at its destination now, which is what the caller records; a
	// source that will not go away is only a leftover.
	if err := root.Remove(hakuxOS(from)); err != nil {
		logWarn("emulators", "hakuX: %s was copied to %s but could not be removed: %v", from, to, err)
	}
	return nil
}

// hakuxApplyMerge moves the planned files from src (the unpacked package, a
// slash path relative to root) onto the card, which root is opened on; keep is
// where the files it replaces go, also relative to root, and known maps the
// files the previous install recorded to their hashes (nil when JukaHub did not
// install it). A file already there with the same bytes stays; a different one
// is moved under keep first. If any step fails, every file moved so far goes
// back and the folders the merge created are removed again, so the card is
// left as it was; a file that cannot go back stays under keep, which the
// caller then sets aside rather than deletes (hakuxRescuePending).
func hakuxApplyMerge(root *os.Root, src string, plan hakuxMergePlan, keep string, known map[string]string) (hakuxMergeResult, error) {
	var res hakuxMergeResult
	var created []string // folders this merge made, parents before children
	var placed []string  // files on the card that this merge put there
	var saved []string   // files moved under keep
	undo := func() {
		for i := len(placed) - 1; i >= 0; i-- {
			_ = root.Remove(hakuxOS(placed[i]))
		}
		for i := len(saved) - 1; i >= 0; i-- {
			if err := hakuxMove(root, path.Join(keep, saved[i]), saved[i]); err != nil {
				logWarn("emulators", "hakuX: %s could not be put back (it stays in %s): %v", saved[i], keep, err)
			}
		}
		for i := len(created) - 1; i >= 0; i-- {
			_ = root.Remove(hakuxOS(created[i]))
		}
	}
	fail := func(err error) (hakuxMergeResult, error) {
		undo()
		return hakuxMergeResult{}, err
	}
	// ensureDir makes the folders dir needs one level at a time, so each one
	// made is recorded even when a deeper one then fails.
	ensureDir := func(dir string) error {
		var missing []string
		for d := dir; d != "." && d != "" && d != "/"; d = path.Dir(d) {
			info, err := root.Lstat(hakuxOS(d))
			if err == nil {
				if !info.IsDir() {
					return fmt.Errorf("%s is in the way of the hakuX package (not a folder)", d)
				}
				break
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			missing = append(missing, d)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			if err := root.Mkdir(hakuxOS(missing[i]), 0o755); err != nil {
				if errors.Is(err, fs.ErrExist) {
					continue
				}
				return err
			}
			created = append(created, missing[i])
		}
		return nil
	}
	for _, dir := range plan.Dirs {
		if err := ensureDir(dir); err != nil {
			return fail(err)
		}
	}
	for _, f := range plan.Files {
		rel := f.To
		from := path.Join(src, f.From)
		sum, size, err := hakuxHashIn(root, from)
		if err != nil {
			return fail(err)
		}
		res.Records = append(res.Records, hakuxFileRecord{Path: rel, Size: size, SHA256: sum})
		info, err := root.Lstat(hakuxOS(rel))
		switch {
		case err == nil && !info.Mode().IsRegular():
			return fail(fmt.Errorf("%s is in the way of the hakuX package (not a file)", rel))
		case err == nil:
			have := ""
			if info.Size() == size || known[rel] != "" {
				if h, _, herr := hakuxHashIn(root, rel); herr == nil {
					have = h
				}
			}
			if have != "" && have == sum {
				res.Unchanged = append(res.Unchanged, rel)
				continue
			}
			if want := known[rel]; want != "" && have != "" && have != want {
				res.Edited = append(res.Edited, rel)
			}
			kept := path.Join(keep, rel)
			if err := ensureDir(path.Dir(kept)); err != nil {
				return fail(err)
			}
			if err := hakuxMove(root, rel, kept); err != nil {
				return fail(fmt.Errorf("%s is in use or read-only — close any game and try again (%v)", rel, err))
			}
			saved = append(saved, rel)
			if err := hakuxMove(root, from, rel); err != nil {
				return fail(err)
			}
			placed = append(placed, rel)
			res.Replaced = append(res.Replaced, rel)
		case errors.Is(err, fs.ErrNotExist):
			if err := ensureDir(path.Dir(rel)); err != nil {
				return fail(err)
			}
			if err := hakuxMove(root, from, rel); err != nil {
				return fail(err)
			}
			placed = append(placed, rel)
			res.Added = append(res.Added, rel)
		default:
			return fail(err)
		}
	}
	for _, dir := range created {
		if _, err := hakuxCardRel(dir); err == nil {
			res.Created = append(res.Created, dir)
		}
	}
	return res, nil
}

// hakuxSetAsideDropped moves the files the previous install laid down that the
// new package no longer ships into keep (the pending kept copy, relative to
// root), so they stop lingering in Data/ and Emus/ and a rollback can put them
// back. Only a file whose bytes are still the ones that install recorded is
// moved: a changed one now belongs to hakuX or the user (settings it rewrote,
// an edited config) and stays where it is, as does one that cannot be read or
// moved. It returns the files it moved, as card paths.
func hakuxSetAsideDropped(root *os.Root, previous hakuxManifest, plan hakuxMergePlan, keep string) []string {
	shipped := make(map[string]bool, len(plan.Files))
	for _, f := range plan.Files {
		// FAT does not tell case apart, so neither does this.
		shipped[strings.ToLower(f.To)] = true
	}
	var moved []string
	for _, rec := range previous.Files {
		rel, err := hakuxCardRel(rec.Path)
		if err != nil || shipped[strings.ToLower(rel)] || strings.TrimSpace(rec.SHA256) == "" {
			continue
		}
		info, err := root.Lstat(hakuxOS(rel))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sum, _, err := hakuxHashIn(root, rel)
		if err != nil {
			continue
		}
		if !strings.EqualFold(sum, strings.TrimSpace(rec.SHA256)) {
			logInfo("emulators", "hakuX: %s left in place — the new package drops it, but it changed since it was installed", rel)
			continue
		}
		kept := path.Join(keep, rel)
		if err := root.MkdirAll(hakuxOS(path.Dir(kept)), 0o755); err != nil {
			logWarn("emulators", "hakuX: could not set %s aside: %v", rel, err)
			continue
		}
		if err := hakuxMove(root, rel, kept); err != nil {
			logWarn("emulators", "hakuX: could not set %s aside: %v", rel, err)
			continue
		}
		moved = append(moved, rel)
	}
	sort.Strings(moved)
	return moved
}

// hakuxRescuePending deals with a pending copy left on the card before a merge
// or after a failed one. Empty, it is removed. Holding files — originals a merge
// stopped by a power cut moved there, or ones an undo could not put back — it is
// moved to a recovered-<time> folder beside it instead of being deleted, and
// that folder is returned ("" when there was nothing to keep).
func hakuxRescuePending(root *os.Root, p hakuxPaths) string {
	pending := filepath.Join(p.SDRoot, hakuxOS(p.pending()))
	if _, err := os.Lstat(pending); err != nil {
		return ""
	}
	if files, _, err := hakuxListTree(pending); err == nil && len(files) == 0 {
		_ = root.RemoveAll(hakuxOS(p.pending()))
		return ""
	}
	rescued := path.Join(p.State, "recovered-"+time.Now().UTC().Format("20060102-150405"))
	if err := root.Rename(hakuxOS(p.pending()), hakuxOS(rescued)); err != nil {
		logWarn("emulators", "hakuX: could not set %s aside: %v", pending, err)
		return pending
	}
	out := filepath.Join(p.SDRoot, hakuxOS(rescued))
	logWarn("emulators", "hakuX: files a merge could not put back are kept in %s", out)
	return out
}

// ── Kept copy and rollback ──────────────────────────────────────────────────

// hakuxBackupInfo describes the kept copy an update made: the version it holds
// ("" when that version was not installed by JukaHub), the files it took off the
// card, the files the update added and the folders it made, which a rollback
// removes again.
type hakuxBackupInfo struct {
	Tag      string   `json:"tag"`
	SavedAt  string   `json:"saved_at"`
	Launcher string   `json:"launcher"`
	Replaced []string `json:"replaced"`
	Added    []string `json:"added"`
	Created  []string `json:"created,omitempty"`
	// Dropped are files the previous version shipped and the update's package
	// no longer does, moved here unchanged (hakuxSetAsideDropped); a rollback
	// puts them back with the replaced ones.
	Dropped []string `json:"dropped,omitempty"`
	// Manifest says the previous provenance file is kept too.
	Manifest bool `json:"manifest"`
}

// hakuxKeepBackup writes info into the pending copy and makes it the kept one,
// replacing the copy an earlier update kept: one version is kept, as on Windows.
func hakuxKeepBackup(root *os.Root, p hakuxPaths, info hakuxBackupInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := root.MkdirAll(hakuxOS(p.pending()), 0o755); err != nil {
		return err
	}
	infoPath := filepath.Join(p.SDRoot, hakuxOS(p.pending()), hakuxBackupInfoName)
	if err := os.WriteFile(infoPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	_ = root.RemoveAll(hakuxOS(p.backup()))
	return root.Rename(hakuxOS(p.pending()), hakuxOS(p.backup()))
}

// hakuxReadBackupInfo reads the kept copy's description.
func hakuxReadBackupInfo(p hakuxPaths) (hakuxBackupInfo, bool) {
	raw, err := os.ReadFile(filepath.Join(p.SDRoot, hakuxOS(p.backup()), hakuxBackupInfoName))
	if err != nil {
		return hakuxBackupInfo{}, false
	}
	var info hakuxBackupInfo
	if json.Unmarshal(raw, &info) != nil {
		return hakuxBackupInfo{}, false
	}
	return info, true
}

// hakuxKeptTag is what a rollback would restore, for the footer: the kept
// version, "previous files" for a kept copy of a version JukaHub did not
// install, and "" when nothing is kept.
func hakuxKeptTag(p hakuxPaths) string {
	info, ok := hakuxReadBackupInfo(p)
	if !ok {
		return ""
	}
	if tag := strings.TrimSpace(info.Tag); tag != "" {
		return tag
	}
	return "previous files"
}

// hakuxRollback puts back what the last update replaced: the kept files return
// (the replaced ones, and the ones it set aside because its package dropped
// them), the files and folders that update added are removed, and the
// provenance file goes back to the kept version's (or away, when that version
// was not installed by JukaHub). Every path comes from JukaHub's own record but
// is still held to the card's Data/ and Emus/ folders. The kept copy is used
// up; Update installs the newer release again.
func hakuxRollback(root *os.Root, p hakuxPaths) (hakuxBackupInfo, error) {
	info, ok := hakuxReadBackupInfo(p)
	if !ok {
		return hakuxBackupInfo{}, errors.New("no previous hakuX version is kept")
	}
	launcher, err := hakuxCardRel(info.Launcher)
	if err != nil || !strings.HasPrefix(launcher, "Emus/") {
		return info, errors.New("the kept hakuX version is unreadable")
	}
	missing := 0
	// The replaced files and the ones the update set aside come back alike:
	// both sit in the kept copy under their card paths.
	restore := append(append([]string(nil), info.Replaced...), info.Dropped...)
	for _, rel := range restore {
		clean, err := hakuxCardRel(rel)
		if err != nil {
			return info, err
		}
		from := path.Join(p.backup(), clean)
		if _, err := root.Lstat(hakuxOS(from)); err != nil {
			missing++
			continue
		}
		if err := root.MkdirAll(hakuxOS(path.Dir(clean)), 0o755); err != nil {
			return info, err
		}
		if err := root.Rename(hakuxOS(from), hakuxOS(clean)); err == nil {
			continue
		}
		if err := root.Remove(hakuxOS(clean)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return info, fmt.Errorf("%s is in use — close any game and try again (%v)", clean, err)
		}
		if err := hakuxMove(root, from, clean); err != nil {
			return info, err
		}
	}
	for _, rel := range info.Added {
		clean, err := hakuxCardRel(rel)
		if err != nil {
			continue
		}
		if fi, err := root.Lstat(hakuxOS(clean)); err == nil && fi.Mode().IsRegular() {
			_ = root.Remove(hakuxOS(clean))
		}
	}
	// Deepest first; a folder that holds anything else stays.
	for i := len(info.Created) - 1; i >= 0; i-- {
		if clean, err := hakuxCardRel(info.Created[i]); err == nil {
			_ = root.Remove(hakuxOS(clean))
		}
	}
	manifest := path.Join(path.Dir(launcher), hakuxManifestName)
	_ = root.Remove(hakuxOS(manifest))
	if info.Manifest {
		if err := hakuxMove(root, path.Join(p.backup(), hakuxKeptManifestName), manifest); err != nil {
			logWarn("emulators", "hakuX rollback: the previous provenance file was not restored: %v", err)
		}
	}
	_ = root.RemoveAll(hakuxOS(p.backup()))
	if missing > 0 {
		logWarn("emulators", "hakuX rollback: %d kept file(s) were no longer in %s", missing, p.backup())
	}
	logInfo("emulators", "rolled hakuX back to %q (%d file(s) restored, %d removed)", info.Tag, len(restore)-missing, len(info.Added))
	return info, nil
}

// hakuxRollbackCard runs hakuxRollback on the card p describes.
func hakuxRollbackCard(p hakuxPaths) (hakuxBackupInfo, error) {
	root, err := os.OpenRoot(p.SDRoot)
	if err != nil {
		return hakuxBackupInfo{}, err
	}
	defer root.Close()
	return hakuxRollback(root, p)
}

// ── Install ─────────────────────────────────────────────────────────────────

// hakuxInstallResult is what one install or update did.
type hakuxInstallResult struct {
	Tag string
	// Launcher is the installed launch.sh.
	Launcher string
	// Updated means an Xbox launcher was there before; Kept that the previous
	// version was kept for a rollback; Current that the newest release was
	// already installed, complete, and nothing changed.
	Updated bool
	Kept    bool
	Current bool
	// Restored counts the missing files a repair put back: the newest release
	// was installed but some of its files were gone. Nothing else is touched.
	Restored int
	// Verified says what the download was checked against ("" = nothing was
	// published; its SHA-256 is recorded in the provenance file).
	Verified string
	// Edited counts files the user had changed that the update replaced; a
	// copy of each is kept under EditedDir, outside the one-version kept copy.
	Edited    int
	EditedDir string
	// Skipped lists the package's top-level names that are not merged.
	Skipped []string
}

// hakuxInspectZip reads the package's directory before anything is unpacked:
// how much it unpacks to, and whether two entries name one file on a FAT card
// ("Emus/XBOX/a" and "emus/xbox/A"), which unpacking would silently collapse
// into one.
func hakuxInspectZip(archive string) (int64, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		if zr != nil {
			_ = zr.Close()
		}
		return 0, fmt.Errorf("%w (open %s: %v)", errBadToolArchive, filepath.Base(archive), err)
	}
	defer zr.Close()
	seen := map[string]string{}
	var total int64
	for _, f := range zr.File {
		if !f.Mode().IsRegular() {
			continue
		}
		rel, err := emuArchiveRel(f.Name)
		if err != nil {
			return 0, err
		}
		if rel == "" {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(rel))
		if other, dup := seen[key]; dup {
			return 0, fmt.Errorf("the hakuX package names one file twice (%s, %s)", other, f.Name)
		}
		seen[key] = f.Name
		total += int64(f.UncompressedSize64)
	}
	return total, nil
}

// hakuxInstall downloads, verifies and merges the newest package for device,
// or reports that it is already installed and complete.
func hakuxInstall(ctx context.Context, client *http.Client, p hakuxPaths, device DeviceModel, progress emuWinProgress) (hakuxInstallResult, error) {
	report := func(phase string, done, total int64) {
		if progress != nil {
			progress(phase, done, total)
		}
	}
	var result hakuxInstallResult
	if support := hakuxSupportFor(device); !support.Offer {
		return result, errors.New(support.Reason)
	}
	report(installPhaseResolving, 0, 0)
	rel, err := hakuxFetchRelease(ctx, client)
	if err != nil {
		return result, err
	}
	release := rel.Release
	result.Tag = release.Tag
	if release.Size > hakuxMaxBytes {
		return result, fmt.Errorf("the hakuX download is %s, more than the %s limit", formatInstallBytes(release.Size), formatInstallBytes(hakuxMaxBytes))
	}
	folder, installed, managed := hakuxInstalled([]string{filepath.Join(p.SDRoot, "Emus")})
	hadInstall := hakuxLauncherIn(folder) != ""
	newest := managed && hadInstall && !hakuxNewer(installed, release)
	// "Not newer" is also a release older than the installed one (a
	// pre-release installed from the list while "latest" is the stable before
	// it): that leaves the card alone, missing files or not — a repair from
	// another release would mix two versions and relabel the install.
	same := strings.EqualFold(strings.TrimSpace(installed.Tag), strings.TrimSpace(release.Tag))
	if newest && (!same || hakuxIntact(p.SDRoot, installed)) {
		result.Current = true
		result.Tag = installed.Tag
		result.Launcher = hakuxLauncherIn(folder)
		return result, nil
	}
	// The newest release is installed but files it laid down are gone: a
	// repair puts the missing files back and touches nothing else — not the
	// files hakuX rewrote, and not the kept copy of the previous version.
	repair := newest // only the same release gets here
	// The zip and the unpacked tree sit on the card together; the exact
	// unpacked size is checked again once the zip is here.
	if release.Size > 0 {
		if free, err := freeBytes(p.SDRoot); err == nil && free < 2*release.Size {
			return result, fmt.Errorf("the card needs about %s free for hakuX and has %s", formatInstallBytes(2*release.Size), formatInstallBytes(free))
		}
	}

	root, err := os.OpenRoot(p.SDRoot)
	if err != nil {
		return result, err
	}
	defer root.Close()
	stage := p.stage()
	_ = root.RemoveAll(hakuxOS(stage))
	if err := root.MkdirAll(hakuxOS(stage), 0o755); err != nil {
		return result, err
	}
	defer func() { _ = root.RemoveAll(hakuxOS(stage)) }()

	archive := filepath.Join(p.SDRoot, hakuxOS(stage), "package.zip")
	report(installPhaseDownload, 0, release.Size)
	sum, err := emuDownload(ctx, client, release.URL, archive, hakuxMaxBytes, func(done, total int64) {
		report(installPhaseDownload, done, total)
	})
	if err != nil {
		return result, err
	}
	report(installPhaseVerify, 0, 0)
	verified, err := hakuxVerify(ctx, client, rel, sum)
	if err != nil {
		return result, err
	}
	result.Verified = verified
	if verified == "" {
		logInfo("emulators", "hakuX %s publishes no checksum; recording the download's SHA-256 %s", release.Tag, sum)
	}
	unpacked, err := hakuxInspectZip(archive)
	if err != nil {
		return result, err
	}
	if free, err := freeBytes(p.SDRoot); err == nil && free < unpacked+(32<<20) {
		return result, fmt.Errorf("hakuX unpacks to %s and the card has %s free", formatInstallBytes(unpacked), formatInstallBytes(free))
	}

	report(installPhaseExtract, 0, 0)
	tree := filepath.Join(p.SDRoot, hakuxOS(stage), "tree")
	if err := emuExtractZip(ctx, archive, tree, func(done, total int64) {
		report(installPhaseExtract, done, total)
	}); err != nil {
		return result, err
	}
	_ = os.Remove(archive)
	content := hakuxContentRoot(tree)
	files, dirs, err := hakuxListTree(content)
	if err != nil {
		return result, err
	}
	plan, err := hakuxPlanMerge(files, dirs)
	if err != nil {
		return result, err
	}
	result.Skipped = plan.Skipped
	// An earlier install only counts if it is the folder this package installs
	// into: a hand-made Emus/xbox1 must not turn a fresh install into an
	// "update" whose rollback would delete it.
	if hadInstall && !strings.EqualFold(filepath.Clean(folder), filepath.Join(p.SDRoot, hakuxOS(path.Dir(plan.Launcher)))) {
		hadInstall, repair = false, false
	}
	if repair {
		missing := plan.Files[:0]
		for _, f := range plan.Files {
			if _, err := os.Lstat(filepath.Join(p.SDRoot, hakuxOS(f.To))); err != nil {
				missing = append(missing, f)
			}
		}
		plan.Files = missing
	}
	contentRel, err := filepath.Rel(p.SDRoot, content)
	if err != nil {
		return result, err
	}
	// The last point a cancel leaves the card untouched.
	if err := ctx.Err(); err != nil {
		return result, err
	}

	// A pending copy from a merge a power cut stopped holds originals: they
	// are set aside, never deleted. If it cannot be moved, merging on top of
	// it would overwrite those originals, so the install stops here.
	pendingAbs := filepath.Join(p.SDRoot, hakuxOS(p.pending()))
	if left := hakuxRescuePending(root, p); left == pendingAbs {
		return result, fmt.Errorf("files from an interrupted install are still in %s — move them somewhere else and try again", left)
	}
	var known map[string]string
	if managed {
		known = hakuxKnownHashes(installed)
	}
	pending := p.pending()
	merged, err := hakuxApplyMerge(root, filepath.ToSlash(contentRel), plan, pending, known)
	if err != nil {
		if rescued := hakuxRescuePending(root, p); rescued != "" {
			return result, fmt.Errorf("%w — the files it could not put back are in %s", err, rescued)
		}
		return result, err
	}

	// Files the user changed are copied out of the kept copy first: that copy
	// is replaced by the next update, and their edits must outlive it.
	if len(merged.Edited) > 0 {
		editedDir := path.Join(p.State, "edited")
		for _, rel := range merged.Edited {
			data, err := os.ReadFile(filepath.Join(p.SDRoot, hakuxOS(path.Join(pending, rel))))
			if err != nil {
				continue
			}
			dest := filepath.Join(p.SDRoot, hakuxOS(path.Join(editedDir, rel)))
			if os.MkdirAll(filepath.Dir(dest), 0o755) == nil && os.WriteFile(dest, data, 0o644) == nil {
				result.Edited++
			}
		}
		result.EditedDir = filepath.Join(p.SDRoot, hakuxOS(editedDir))
		logInfo("emulators", "hakuX: %d file(s) the user changed are kept in %s", result.Edited, result.EditedDir)
	}

	// Files the installed version shipped and this package no longer does go
	// into the kept copy as well, so they do not linger on the card; only
	// unchanged ones move (hakuxSetAsideDropped).
	var dropped []string
	if managed && hadInstall && !repair {
		dropped = hakuxSetAsideDropped(root, installed, plan, pending)
	}

	launcherDir := path.Dir(plan.Launcher)
	records := merged.Records
	if repair {
		// The provenance keeps every file of the install, with the restored
		// ones refreshed.
		byPath := map[string]int{}
		records = append([]hakuxFileRecord(nil), installed.Files...)
		for i, r := range records {
			byPath[r.Path] = i
		}
		for _, r := range merged.Records {
			if i, ok := byPath[r.Path]; ok {
				records[i] = r
			} else {
				records = append(records, r)
			}
		}
		result.Restored = len(merged.Added)
		hakuxRescuePending(root, p)
	} else if len(merged.Replaced) > 0 || len(dropped) > 0 || (hadInstall && len(merged.Added) > 0) {
		info := hakuxBackupInfo{
			SavedAt:  time.Now().UTC().Format(time.RFC3339),
			Launcher: plan.Launcher,
			Replaced: merged.Replaced,
			Added:    merged.Added,
			Created:  merged.Created,
			Dropped:  dropped,
		}
		if managed {
			info.Tag = installed.Tag
			previous := filepath.Join(folder, hakuxManifestName)
			if data, err := os.ReadFile(previous); err == nil && root.MkdirAll(hakuxOS(pending), 0o755) == nil {
				kept := filepath.Join(p.SDRoot, hakuxOS(pending), hakuxKeptManifestName)
				info.Manifest = os.WriteFile(kept, data, 0o644) == nil
			}
		}
		if err := hakuxKeepBackup(root, p, info); err != nil {
			logWarn("emulators", "could not keep the files the hakuX update replaced: %v", err)
			hakuxRescuePending(root, p)
		} else {
			result.Kept = true
		}
	} else {
		hakuxRescuePending(root, p)
		if !hadInstall {
			// A fresh install: a copy kept for an install that is gone would
			// "roll back" to files that have nothing to do with this one.
			_ = root.RemoveAll(hakuxOS(p.backup()))
		}
	}

	manifest := hakuxManifest{
		Name:        hakuxName,
		Source:      "github:" + hakuxRepo,
		Tag:         release.Tag,
		Asset:       release.Asset,
		URL:         release.URL,
		Digest:      release.Digest,
		SHA256:      sum,
		Verified:    verified,
		AssetDate:   release.AssetDate,
		Prerelease:  rel.Prerelease,
		Device:      string(device),
		Launcher:    plan.Launcher,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		Files:       records,
	}
	if err := hakuxWriteManifest(filepath.Join(p.SDRoot, hakuxOS(launcherDir)), manifest); err != nil {
		logWarn("emulators", "could not record the hakuX install: %v", err)
	}
	if strings.TrimSpace(p.Roms) != "" {
		_ = os.MkdirAll(hakuxRomsDir(p.Roms), 0o755)
	}
	result.Updated = hadInstall && !repair
	result.Launcher = filepath.Join(p.SDRoot, hakuxOS(plan.Launcher))
	logInfo("emulators", "hakuX %s merged into %s: %d added, %d replaced, %d unchanged, %d dropped and set aside, skipped %v, verified by %q",
		release.Tag, p.SDRoot, len(merged.Added), len(merged.Replaced), len(merged.Unchanged), len(dropped), plan.Skipped, verified)
	return result, nil
}
