package main

// raget.go — RetroArch for a handheld whose card has none.
//
// Juka, 2026-11-22: "Download latest version of working RetroArch (if it's not
// installed) … make sure GLIBC is private library that we use and that it's
// compatible with Trimui Smart Pro and other Trimui devices so that we do not
// get any error problems." Every console but a few plays in RetroArch, and on
// the TrimUI handhelds RetroArch is not part of the firmware: it comes on the
// card, in TrimUI's SD base package (RetroArch/ra64.trimui). A card set up
// without that package — formatted by hand, another firmware's, a JukaHub OS
// card made from one — had no RetroArch, and every game, core install and
// core update failed with "RetroArch not found".
//
// Which RetroArch. The one that works on these handhelds is the one TrimUI
// builds for them: its ra64.trimui drives the screen through the firmware's
// own SDL2 and GPU libraries and carries TrimUI's in-game menu. TrimUI
// publishes it in each model's SD base package on GitHub (trimui/assets_*), and
// the newest of them is the newest RetroArch there is for the hardware
// (1.22.2 on the Smart Pro S in December 2025; upstream has released nothing
// newer since). The A133P handhelds — Smart Pro, Brick (and Hammer), Brick Pro
// — share one chip, GPU and SDK, so each of their packages is a source for all
// of them; the Smart Pro S (A523, Mali) has its own. raGetCollect reads the
// newest releases of the model's family, and in every .zip asset finds
// RetroArch's folder without downloading the package (raget_remote.go reads
// only the zip's directory). Most of TrimUI's packages — all of the Smart
// Pro's — are .7z archives: their directories are read the same way, and
// their files in one pass over the blocks that hold them (raget_7z.go).
//
// Is it working here. A first install tries the handheld's own model's builds
// first, then the rest of the family's; within each, and in the update pass,
// newest first (by the binary's own time in the package; the handheld's own
// model wins a near tie) — at most raGetMaxTries of them, each downloaded and
// checked before anything is written to the card's RetroArch folder
// (raGetCheck): a 64-bit ARM program, every library it links found on this
// handheld, every symbol version defined — by
// the firmware's own loader listing and binding its libraries the way
// `ldd -r` does, and again by JukaHub's private loader (glibc 2.39). A build
// is taken when it starts on the firmware's glibc, or on JukaHub's where
// JukaHub can start it so (ra_glibc.go: the setting allows it, the system
// gives a game a mount namespace).
//
// What is written. Only into the card's RetroArch folder, and only files the
// card does not have yet — a folder left behind keeps every setting and core
// it holds: the RetroArch folder of the handheld's own model's newest package
// (TrimUI's retroarch.cfg, cores, autoconfig profiles and assets for exactly
// this screen and pad), else the chosen build's package's — when the card is
// short of space without the menu pictures, shaders, overlays and databases
// RetroArch runs without — then the binary last, so an install cut short
// leaves no RetroArch to start and the next start finishes it. A folder from
// another model's package has its full-screen size and custom viewport put
// back to "the screen's own" (raGetFitConfig). .jukahub-retroarch.json beside
// the binary records what was installed.
//
// JukaHub's glibc. The private runtime (glibc_runtime.go) is installed first,
// with its C++ library: the builds are checked against it, and the newest
// cores RetroArch loads need it. A RetroArch JukaHub installed starts on it at
// every launch JukaHub makes, whatever the Automatic setting would decide for
// the core (ra_glibc.go, raGetOwnsLaunch), as long as the probe passes; the
// firmware's glibc is what is left when it cannot.
//
// When. A minute after start, on a card with no RetroArch, online and between
// games (raget_ui.go), unless Emulator updates is Off; A on the Emulators
// page's RetroArch tile; a core install or "Get more cores" on such a card;
// a game whose launch script found no RetroArch. Afterwards the Emulator
// updates pass keeps the RetroArch JukaHub installed current with TrimUI's
// newest build (the binary alone, the old one kept as ra64.trimui.bak; never
// from another model's 7z package); a RetroArch that came with the card, or
// one changed by hand, is never touched.

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// raGetBinaryName is TrimUI's RetroArch binary, the name every launch
	// script on the card calls.
	raGetBinaryName = "ra64.trimui"
	// raGetManifestName records what JukaHub installed, beside the binary.
	raGetManifestName = ".jukahub-retroarch.json"
	// raGetReleasesPerPage and raGetReleasesLooked bound one repository's
	// look: the newest releases, a few of them (TrimUI's newest base package
	// carries its newest RetroArch).
	raGetReleasesPerPage = 8
	raGetReleasesLooked  = 2
	// raGetMaxTries bounds the binaries downloaded to find one that starts
	// here.
	raGetMaxTries = 3
	// raGetDirBudget bounds the transfer while a package's directory is read
	// (in raGetDirBlock blocks, raget_remote.go).
	raGetDirBudget = 48 << 20
	// raGetMaxEntries bounds the files one package's directory may list.
	raGetMaxEntries = 200000
	// raGetMaxBinary bounds ra64.trimui (tens of megabytes).
	raGetMaxBinary = 192 << 20
	// raGetMaxFolder and raGetMaxFolderFiles bound RetroArch's folder.
	raGetMaxFolder      = int64(4 << 30)
	raGetMaxFolderFiles = 60000
	// raGetSpaceMargin is left free on the card after the install.
	raGetSpaceMargin = 96 << 20
	// raGetOwnBonus lets the handheld's own model win builds this close.
	raGetOwnBonus = 48 * time.Hour
	// raGetPartSuffix names a file still being written.
	raGetPartSuffix = ".jhpart"
	// raGetSeenMax bounds the package names the manifest remembers.
	raGetSeenMax = 64
)

// raGetSource is a TrimUI repository of SD base packages and the model it is
// for.
type raGetSource struct {
	Repo  string
	Model DeviceModel
}

// The families: the A133P handhelds share one chip, GPU and SDK; the A523
// Smart Pro S has its own.
var (
	raGetA133PSources = []raGetSource{
		{Repo: "trimui/assets_smartpro", Model: DeviceTrimuiSmartPro},
		{Repo: "trimui/assets_brickpro", Model: DeviceTrimuiBrickPro},
		{Repo: "trimui/assets_brick", Model: DeviceTrimuiBrick},
	}
	raGetA523Sources = []raGetSource{
		{Repo: "trimui/assets_smartpro_s", Model: DeviceTrimuiSmartProS},
	}
)

// raGetSourcesFor is model's family, its own repository first; nil for a model
// TrimUI publishes no RetroArch for.
func raGetSourcesFor(model DeviceModel) []raGetSource {
	var family []raGetSource
	switch model {
	case DeviceTrimuiSmartProS:
		family = raGetA523Sources
	case DeviceTrimuiSmartPro, DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		family = raGetA133PSources
	default:
		return nil
	}
	own := raGetPackageModel(model)
	out := make([]raGetSource, 0, len(family))
	for _, s := range family {
		if s.Model == own {
			out = append(out, s)
		}
	}
	for _, s := range family {
		if s.Model != own {
			out = append(out, s)
		}
	}
	return out
}

// raGetPackageModel is the model whose SD package a handheld uses: the Hammer
// is a Brick.
func raGetPackageModel(model DeviceModel) DeviceModel {
	if model == DeviceTrimuiHammer {
		return DeviceTrimuiBrick
	}
	return model
}

// raGetModelName names a model in messages ("Brick Pro").
func raGetModelName(model DeviceModel) string {
	name := deviceDisplayName(model)
	name = strings.TrimPrefix(name, "TrimUI ")
	if i := strings.Index(name, " ("); i > 0 {
		name = name[:i]
	}
	return name
}

var (
	raGetModelOnce  sync.Once
	raGetModelValue DeviceModel
)

// raGetModel is the handheld's model, read once: hakuxDevice tells the Smart
// Pro S (A523) from a Smart Pro when detection named it by its panel, and an
// A133P build would not start on its Mali GPU.
func raGetModel() DeviceModel {
	raGetModelOnce.Do(func() { raGetModelValue = hakuxDevice() })
	return raGetModelValue
}

// raGetSupported reports a handheld JukaHub installs RetroArch on.
func raGetSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && IsTrimuiDevice() && len(raGetSourcesFor(raGetModel())) > 0
}

// raGetCardFn is the card RetroArch goes on (a variable for the tests).
var raGetCardFn = childEnvFirmwareCard

// raGetTargetDir is the card's RetroArch folder, where TrimUI's launch scripts
// look for it.
func raGetTargetDir() string {
	card := strings.TrimSpace(raGetCardFn())
	if card == "" {
		return ""
	}
	return filepath.Join(card, "RetroArch")
}

// raGetMissingNow reports a handheld with no RetroArch anywhere JukaHub looks:
// the card's RetroArch folder, then everything netplayRetroArchPath searches
// (JUKAHUB_RETROARCH, the card's and the firmware's RetroArch folders, a
// running RetroArch, the launch scripts, a walk of the card). It may walk the
// card: never on the main thread.
func raGetMissingNow() bool {
	if !raGetSupported() {
		return false
	}
	if dir := raGetTargetDir(); dir != "" && regularFile(filepath.Join(dir, raGetBinaryName)) {
		return false
	}
	return netplayRetroArchPath() == ""
}

// ── What JukaHub installed ───────────────────────────────────────────────────

// raGetManifest is .jukahub-retroarch.json: where the binary came from and
// what it is.
type raGetManifest struct {
	Repo    string `json:"repo"`
	Release string `json:"release"`
	Asset   string `json:"asset"`
	Model   string `json:"model"`
	Version string `json:"version,omitempty"`
	// Built is the binary's time in TrimUI's zip (RFC 3339), CRC32 its
	// checksum there and Size its length on the card.
	Built string `json:"built"`
	CRC32 string `json:"crc32"`
	Size  int64  `json:"size"`
	// Folder names the package the rest of the folder came from (first
	// install), Files how many files it wrote and Lean whether it left out
	// what RetroArch runs without.
	Folder string `json:"folder,omitempty"`
	Files  int    `json:"files,omitempty"`
	Lean   bool   `json:"lean,omitempty"`
	// Glibc is where the build was found to start: "runtime" (JukaHub's) or
	// "firmware".
	Glibc     string `json:"glibc"`
	Installed string `json:"installed"`
	Updated   string `json:"updated,omitempty"`
	// Seen names every package already looked at (repo/tag/asset@time), so
	// the daily check opens only new ones.
	Seen []string `json:"seen,omitempty"`
}

// raGetReadManifest reads dir's manifest.
func raGetReadManifest(dir string) (raGetManifest, bool) {
	var m raGetManifest
	data, err := os.ReadFile(filepath.Join(dir, raGetManifestName))
	if err != nil || len(data) > 256<<10 || json.Unmarshal(data, &m) != nil {
		return raGetManifest{}, false
	}
	return m, m.CRC32 != "" && m.Size > 0
}

// raGetWriteManifest writes dir's manifest through a temporary file.
func raGetWriteManifest(dir string, m raGetManifest) error {
	if len(m.Seen) > raGetSeenMax {
		m.Seen = m.Seen[len(m.Seen)-raGetSeenMax:]
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	err = linuxPkgWriteFile(filepath.Join(dir, raGetManifestName), append(data, '\n'))
	raGetForgetManaged()
	return err
}

var raGetManagedMemo struct {
	mu    sync.Mutex
	stamp string
	bin   string
	man   raGetManifest
}

// raGetManaged is the RetroArch JukaHub installed and its manifest, while the
// binary on the card is still the size JukaHub wrote (a RetroArch replaced by
// hand is the user's again). Two stats and, when either file changed, a small
// read: cheap enough for every launch.
func raGetManaged() (string, raGetManifest, bool) {
	if !raGetSupported() {
		return "", raGetManifest{}, false
	}
	dir := raGetTargetDir()
	if dir == "" {
		return "", raGetManifest{}, false
	}
	bin := filepath.Join(dir, raGetBinaryName)
	bst, err := os.Stat(bin)
	if err != nil || !bst.Mode().IsRegular() {
		return "", raGetManifest{}, false
	}
	mst, err := os.Stat(filepath.Join(dir, raGetManifestName))
	if err != nil {
		return "", raGetManifest{}, false
	}
	stamp := coreFixFileStamp(bst) + "|" + coreFixFileStamp(mst)
	m := &raGetManagedMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stamp != stamp {
		man, ok := raGetReadManifest(dir)
		m.stamp, m.man, m.bin = stamp, man, ""
		if ok && man.Size == bst.Size() {
			m.bin = bin
		}
	}
	return m.bin, m.man, m.bin != ""
}

// raGetManagedBinary is raGetManaged's binary, "" when JukaHub installed none.
func raGetManagedBinary() string {
	bin, _, _ := raGetManaged()
	return bin
}

// raGetForgetManaged drops the memo: the manifest or the binary was written.
func raGetForgetManaged() {
	raGetManagedMemo.mu.Lock()
	raGetManagedMemo.stamp = ""
	raGetManagedMemo.mu.Unlock()
}

// raGetFileCRC is a file's CRC-32 as "%08x", "" when it cannot be read.
func raGetFileCRC(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// ── TrimUI's packages ────────────────────────────────────────────────────────

// raGetGHRelease is the part of a GitHub release the look reads.
type raGetGHRelease struct {
	TagName     string           `json:"tag_name"`
	Name        string           `json:"name"`
	Body        string           `json:"body"`
	Draft       bool             `json:"draft"`
	Prerelease  bool             `json:"prerelease"`
	PublishedAt string           `json:"published_at"`
	Assets      []emuGitHubAsset `json:"assets"`
}

// raGetCandidate is one RetroArch binary in one of TrimUI's packages.
type raGetCandidate struct {
	Repo  string
	Tag   string
	Asset string
	Model DeviceModel
	// Own: the package is the handheld's own model's.
	Own bool
	// Notes is the RetroArch version the release notes name ("" when none).
	Notes string
	// Prefix is RetroArch's folder inside the zip ("…/RetroArch/"); Bin the
	// binary's entry and When its time.
	Prefix string
	Bin    *zip.File
	When   time.Time
	// Full: the folder has its retroarch.cfg, so it can be the card's whole
	// RetroArch folder; Files and Bytes are its files and their size.
	Full  bool
	Files int
	Bytes int64
	// Version is read from the binary once it is downloaded.
	Version string
	zr      *zip.Reader
	remote  *raGetRemote
	// A 7z package's (raget_7z.go; Bin and zr are nil): its directory and the
	// binary's entry. staged is the folder its last pass unpacked into ("" until
	// one ran); stagedFolder says that pass took RetroArch's folder, whose
	// files and folders it wrote there are stagedFiles and stagedDirs (the
	// binary not among them), stagedLean whether it left out what RetroArch
	// runs without. szFailed says why the package is no use this time: a pass
	// of it failed, or it failed for good before (raGetPassOver).
	sz           *sevenZipArchive
	szBin        int
	staged       string
	stagedFolder bool
	stagedFiles  []raGet7zWanted
	stagedDirs   []string
	stagedLean   bool
	szFailed     string
}

// binCRC and binSize are the binary's CRC-32 and size as its package states
// them.
func (c *raGetCandidate) binCRC() uint32 {
	if c.sz != nil {
		return c.sz.Entries[c.szBin].CRC
	}
	return c.Bin.CRC32
}

func (c *raGetCandidate) binSize() uint64 {
	if c.sz != nil {
		return c.sz.Entries[c.szBin].Size
	}
	return c.Bin.UncompressedSize64
}

// crc is the binary's CRC-32 as its package states it.
func (c *raGetCandidate) crc() string { return fmt.Sprintf("%08x", c.binCRC()) }

// label names the build in logs and messages.
func (c *raGetCandidate) label() string {
	v := c.Version
	if v == "" {
		v = c.Notes
	}
	what := "RetroArch"
	if v != "" {
		what += " " + v
	}
	return fmt.Sprintf("%s (TrimUI's %s package %s, built %s)", what, raGetModelName(c.Model), c.Tag, c.When.UTC().Format("2006-01-02"))
}

// raGetNotesRe finds a RetroArch version in release notes ("Update RetroArch
// to version 1.20.0", "RetroArch 1.22.2 (ra64.trimui)").
var raGetNotesRe = regexp.MustCompile(`(?i)retro\s?arch[^0-9\n]{0,24}?v?(1\.\d{1,2}\.\d{1,2})`)

// raGetNotesVersion is the highest RetroArch version texts name.
func raGetNotesVersion(texts ...string) string {
	best := ""
	for _, text := range texts {
		for _, m := range raGetNotesRe.FindAllStringSubmatch(text, -1) {
			if best == "" || compareCoreVersions(m[1], best) > 0 {
				best = m[1]
			}
		}
	}
	return best
}

// raGetAssetKey names one package as it is now: a re-upload under the same
// name has a new time, and counts as new.
func raGetAssetKey(repo, tag string, a emuGitHubAsset) string {
	return repo + "/" + tag + "/" + strings.TrimSpace(a.Name) + "@" + strings.TrimSpace(a.UpdatedAt)
}

// raGetListing is one look at TrimUI's packages.
type raGetListing struct {
	cands []raGetCandidate
	// keys names every zip package looked at; notes says why a repository or
	// a package gave nothing.
	keys  []string
	notes []string
}

// raGetCollect reads the newest releases of model's family and opens each
// .zip or .7z package not in skip, keeping those whose directory holds a
// RetroArch binary. It fails only when no repository could be asked at all.
func raGetCollect(ctx context.Context, client *http.Client, model DeviceModel, skip map[string]bool) (raGetListing, error) {
	var out raGetListing
	own := raGetPackageModel(model)
	asked := 0
	var firstErr error
	for _, src := range raGetSourcesFor(model) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		api := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", standaloneAPIBase, src.Repo, raGetReleasesPerPage)
		data, err := fetchUpdateBytes(ctx, client, api, patchMaxMetadata)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", src.Repo, err)
			}
			out.notes = append(out.notes, src.Repo+": "+firstLine(err.Error()))
			if emuRateLimited(err) || ctx.Err() != nil {
				break // GitHub's hour-long hold: the other repositories wait too
			}
			continue
		}
		asked++
		var rels []raGetGHRelease
		if err := json.Unmarshal(data, &rels); err != nil {
			out.notes = append(out.notes, src.Repo+": unreadable release list")
			continue
		}
		looked := 0
		for _, rel := range rels {
			tag := strings.TrimSpace(rel.TagName)
			if rel.Draft || tag == "" {
				continue
			}
			if looked >= raGetReleasesLooked {
				break
			}
			looked++
			notes := raGetNotesVersion(rel.Name, rel.Body)
			full := false
			for _, a := range rel.Assets {
				name := strings.TrimSpace(a.Name)
				lower := strings.ToLower(name)
				// TrimUI's packages are zips and 7z archives (raget_7z.go).
				sevenZip := strings.HasSuffix(lower, ".7z")
				switch {
				case !sevenZip && strings.Contains(lower, ".7z."):
					out.notes = append(out.notes, fmt.Sprintf("%s %s: %s is one part of a split 7-Zip archive, which JukaHub does not join", src.Repo, tag, name))
					continue
				case !sevenZip && !strings.HasSuffix(lower, ".zip"), a.Size <= 0, strings.TrimSpace(a.BrowserDownloadURL) == "":
					continue
				}
				key := raGetAssetKey(src.Repo, tag, a)
				out.keys = append(out.keys, key)
				if skip[key] {
					continue
				}
				open := raGetOpenAsset
				if sevenZip {
					open = raGet7zOpenAsset
				}
				c, ok, err := open(ctx, client, src, tag, a)
				switch {
				case err != nil:
					if ctx.Err() != nil {
						return out, ctx.Err()
					}
					out.notes = append(out.notes, fmt.Sprintf("%s %s: %v", src.Repo, tag, err))
					logInfo("emulators", "RetroArch packages: %s %s: %v", src.Repo, tag, err)
				case !ok:
					logDebug("emulators", "RetroArch packages: %s %s %s holds no RetroArch/%s", src.Repo, tag, name, raGetBinaryName)
				default:
					c.Own, c.Notes = src.Model == own, notes
					if c.When.IsZero() {
						c.When, _ = time.Parse(time.RFC3339, strings.TrimSpace(rel.PublishedAt))
					}
					logInfo("emulators", "RetroArch packages: %s in %s (%d files, %s)", c.label(), name, c.Files, formatInstallBytes(c.Bytes))
					out.cands = append(out.cands, c)
					full = full || c.Full
				}
			}
			if full && !rel.Prerelease {
				break // older releases carry older builds
			}
		}
	}
	if asked == 0 && firstErr != nil {
		return out, firstErr
	}
	return out, nil
}

// raGetOpenAsset reads one package's directory (raget_remote.go) and finds
// RetroArch's folder in it: the shallowest ra64.trimui in a folder named
// RetroArch, else the shallowest ra64.trimui anywhere (a fix that ships the
// binary alone). ok is false when the package has none.
func raGetOpenAsset(ctx context.Context, client *http.Client, src raGetSource, tag string, a emuGitHubAsset) (raGetCandidate, bool, error) {
	remote := newRaGetRemote(ctx, client, strings.TrimSpace(a.BrowserDownloadURL), a.Size, raGetDirBudget)
	remote.setBlockSize(raGetDirBlock)
	zr, err := zip.NewReader(remote, a.Size)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		return raGetCandidate{}, false, fmt.Errorf("%s: %w", a.Name, err)
	}
	if len(zr.File) > raGetMaxEntries {
		return raGetCandidate{}, false, fmt.Errorf("%s lists %d files", a.Name, len(zr.File))
	}
	var bin, loose *zip.File
	binDir, looseDir := "", ""
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if !strings.EqualFold(path.Base(name), raGetBinaryName) || f.FileInfo().IsDir() {
			continue
		}
		dir := path.Dir(name)
		depth := strings.Count(dir, "/")
		if dir == "." {
			depth = -1
		}
		if strings.EqualFold(path.Base(dir), "RetroArch") {
			if bin == nil || depth < strings.Count(binDir, "/") {
				bin, binDir = f, dir
			}
			continue
		}
		if loose == nil || depth < strings.Count(looseDir, "/") {
			loose, looseDir = f, dir
		}
	}
	if bin == nil && loose != nil {
		bin, binDir = loose, looseDir
	}
	if bin == nil {
		return raGetCandidate{}, false, nil
	}
	c := raGetCandidate{Repo: src.Repo, Tag: tag, Asset: strings.TrimSpace(a.Name), Model: src.Model, Bin: bin, When: bin.Modified, zr: zr, remote: remote}
	if binDir != "." {
		c.Prefix = binDir + "/"
	}
	folder := strings.EqualFold(path.Base(binDir), "RetroArch")
	for _, f := range zr.File {
		rel, ok := raGetUnder(f.Name, c.Prefix)
		if !ok || rel == "" || f.FileInfo().IsDir() {
			continue
		}
		c.Files++
		c.Bytes += int64(f.UncompressedSize64)
		if folder && strings.EqualFold(rel, "retroarch.cfg") {
			c.Full = true
		}
	}
	return c, true, nil
}

// raGetUnder is name (a zip entry, either slash) relative to prefix, matched
// without regard to case as a FAT card would; ok is false outside it.
func raGetUnder(name, prefix string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if len(name) < len(prefix) || !strings.EqualFold(name[:len(prefix)], prefix) {
		return "", false
	}
	return name[len(prefix):], true
}

// raGetRank sorts candidates best first: the newest build (the handheld's own
// model's within raGetOwnBonus), then the version the notes name, then a whole
// folder over a lone binary. ownFirst (a first install) puts every build of
// the handheld's own model's packages before the others: TrimUI's build for
// exactly this screen, pad and firmware, with its folder in the same package
// — since 7z packages became readable (raget_7z.go), the one a Smart Pro gets
// rather than a Brick's. The update pass (newest first) moves on from it when
// TrimUI publishes a newer build that starts here.
func raGetRank(cands []raGetCandidate, ownFirst bool) {
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := &cands[i], &cands[j]
		if ownFirst && a.Own != b.Own {
			return a.Own
		}
		ta, tb := a.When, b.When
		if a.Own {
			ta = ta.Add(raGetOwnBonus)
		}
		if b.Own {
			tb = tb.Add(raGetOwnBonus)
		}
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
		if a.Notes != "" && b.Notes != "" {
			if c := compareCoreVersions(a.Notes, b.Notes); c != 0 {
				return c > 0
			}
		}
		return a.Full && !b.Full
	})
}

// raGetFolderSource is the candidate whose folder becomes the card's (its
// retroarch.cfg, autoconfig profiles, cores…), whichever package the binary
// came from: the handheld's own model's newest whole folder — TrimUI's set for
// exactly this screen and pad — else pick's own when it is whole, else the
// whole one closest to it (same release, same repository, the newest); -1 when
// there is none.
func raGetFolderSource(cands []raGetCandidate, pick int) int {
	if pick < 0 || pick >= len(cands) {
		return -1
	}
	// A 7z package whose pass failed (now, or for good before) is no source.
	whole := func(c *raGetCandidate) bool { return c.Full && c.szFailed == "" }
	own := -1
	for i := range cands {
		if cands[i].Own && whole(&cands[i]) && (own < 0 || cands[i].When.After(cands[own].When)) {
			own = i
		}
	}
	switch {
	case own >= 0:
		return own
	case whole(&cands[pick]):
		return pick
	}
	score := func(c *raGetCandidate) int {
		s := 0
		if c.Repo == cands[pick].Repo {
			s += 4
			if c.Tag == cands[pick].Tag {
				s += 2
			}
		}
		return s
	}
	best := -1
	for i := range cands {
		c := &cands[i]
		if !whole(c) {
			continue
		}
		if best < 0 || score(c) > score(&cands[best]) || (score(c) == score(&cands[best]) && c.When.After(cands[best].When)) {
			best = i
		}
	}
	return best
}

// ── Downloading and checking a build ─────────────────────────────────────────

// raGetCounting reports the bytes read through it, a few times a second.
type raGetCounting struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
	last   time.Time
}

func (c *raGetCounting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	if c.report != nil && (err != nil || time.Since(c.last) >= 250*time.Millisecond) {
		c.last = time.Now()
		c.report(c.done, c.total)
	}
	return n, err
}

// raGetFetch writes entry f of c's package to dest (through dest+.jhpart),
// checked against its size and CRC-32.
func raGetFetch(ctx context.Context, c *raGetCandidate, f *zip.File, dest string, report func(done, total int64)) error {
	if f.UncompressedSize64 > raGetMaxBinary {
		return fmt.Errorf("%s is %s, more than a RetroArch can be", path.Base(f.Name), formatInstallBytes(int64(f.UncompressedSize64)))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.remote.setBlockSize(raGetBlock)
	c.remote.setBudget(c.remote.transferred() + int64(f.CompressedSize64) + 3*raGetBlock)
	rc, err := raGetOpen(f)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + raGetPartSuffix
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, &raGetCounting{r: rc, total: int64(f.UncompressedSize64), report: report})
	cerr := out.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}

// raGetVerdict is what raGetCheck found.
type raGetVerdict struct {
	firmware bool // its libraries load with the firmware's glibc
	runtime  bool // and with JukaHub's
	usable   bool // JukaHub can start it on its glibc here
	why      string
	note     string
}

// accepted reports a build that starts here one way or the other.
func (v raGetVerdict) accepted() bool { return v.firmware || v.usable }

// glibc is where JukaHub will start it.
func (v raGetVerdict) glibc() string {
	if v.usable {
		return "runtime"
	}
	return "firmware"
}

// raGetCheck decides whether the binary at bin starts on this handheld (see
// the top of the file).
func raGetCheck(bin string) raGetVerdict {
	var v raGetVerdict
	f, err := elf.Open(bin)
	if err != nil {
		v.why = "it is not a program (no ELF header)"
		return v
	}
	class, machine := f.Class, f.Machine
	dynamic := false
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			dynamic = true
		}
	}
	_ = f.Close()
	if class != elf.ELFCLASS64 || machine != elf.EM_AARCH64 {
		v.why = "it is built for " + elfArchName(class, machine) + ", not this handheld's 64-bit ARM"
		return v
	}
	if !dynamic {
		v.why = "it is not a dynamically linked program"
		return v
	}
	firmwareDirs := raGetFirmwareDirs()
	fw := raGetStaticProblem(bin, firmwareDirs)
	if p, ran := raGetTrace(bin, "", ""); ran {
		fw = p
	}
	v.firmware = fw == ""
	loader, libDir, ready := raGlibcRuntime()
	rt := "JukaHub's glibc " + privateGlibcVersion + " is not installed"
	if ready {
		rt = raGetStaticProblem(bin, append([]string{libDir}, firmwareDirs...))
		if p, ran := raGetTrace(bin, loader, libDir); ran {
			rt = p
		}
	}
	v.runtime = rt == ""
	block := ""
	switch {
	case !raGlibcWanted():
		block = "RetroArch's C library is set to the firmware's"
	case !raGlibcNamespaceOK():
		block = "this system gives a game no mount namespace of its own"
	}
	v.usable = v.runtime && block == ""
	switch {
	case v.usable && v.firmware:
		v.note = "starts on the firmware's glibc and on JukaHub's " + privateGlibcVersion
	case v.usable:
		v.note = "starts on JukaHub's glibc " + privateGlibcVersion + " only (the firmware's: " + fw + ")"
	case v.firmware && v.runtime:
		v.note = "starts on the firmware's glibc (JukaHub's cannot start it here: " + block + ")"
	case v.firmware:
		v.note = "starts on the firmware's glibc only (JukaHub's: " + rt + ")"
	default:
		v.why = "with the firmware's glibc: " + fw
		switch {
		case v.runtime:
			v.why += "; JukaHub's glibc would start it, but " + block
		case ready:
			v.why += "; with JukaHub's: " + rt
		}
	}
	return v
}

// raGetFirmwareDirs is where the firmware's loader finds a program's
// libraries: the LD_LIBRARY_PATH games get, the folders of /etc/ld.so.conf and
// the system folders.
func raGetFirmwareDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(list string) {
		for _, d := range strings.Split(list, ":") {
			if d = strings.TrimSpace(d); d != "" && !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	add(raGlibcFirmwareLD())
	add(raGlibcLibTail())
	add(strings.Join(elfHostEnv(nil).libDirs, ":"))
	return dirs
}

// raGetStaticProblem is the check a loader would make, read from the files:
// every library bin links found in dirs (its own run path first) and every
// symbol version it needs of each defined there. "" when nothing is missing.
// Unlike a core's check (elfcompat.go), a library found nowhere counts: a
// program has no RetroArch to bring it.
func raGetStaticProblem(bin string, dirs []string) string {
	f, err := elf.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	needed, _ := f.ImportedLibraries()
	needs := elfVersionNeeds(f)
	search := append(elfRunPaths(f, filepath.Dir(bin)), dirs...)
	var problems []string
	for _, lib := range elfNeededOrder(needed, needs) {
		info, state := elfFindLibrary(lib, f.Class, f.Machine, search)
		switch state {
		case elfLibMissing:
			problems = append(problems, "needs "+filepath.Base(lib)+", which this handheld does not have")
		case elfLibFound:
			if gap := elfVersionGap(lib, needs[lib], info.defs); gap != "" {
				problems = append(problems, gap)
			}
		}
	}
	return elfJoinProblems(problems)
}

// raGetTrace has a loader list and bind bin's libraries, as `ldd -r` does,
// without starting it: the firmware's (loader "") from bin's own PT_INTERP, or
// JukaHub's private one with its runtime first. ran is false when the listing
// could not be made (no loader); problem is "" when it found nothing missing.
func raGetTrace(bin, loader, libDir string) (problem string, ran bool) {
	env := []string{"LD_TRACE_LOADED_OBJECTS=1", "LD_WARN=yes", "LD_BIND_NOW=yes",
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	var name string
	var args []string
	who := "the firmware's loader"
	if loader == "" {
		name = raGlibcInterp(bin)
		if name == "" {
			return "", false
		}
		args = []string{bin}
		env = append(env, "LD_LIBRARY_PATH="+raGlibcFirmwareLD())
	} else {
		if !regularFile(loader) || strings.TrimSpace(libDir) == "" {
			return "", false
		}
		libs := libDir
		if ld := strings.Trim(raGlibcFirmwareLD(), ":"); ld != "" {
			libs += ":" + ld
		}
		if tail := raGlibcLibTail(); tail != "" {
			libs += ":" + tail
		}
		name, args = loader, []string{"--inhibit-cache", "--library-path", libs, bin}
		who = "JukaHub's loader"
	}
	out, err := raGlibcRunProbe(name, args, env, filepath.Dir(bin), false)
	problems, listed := raGlibcTraceProblems(out)
	code, clean := raGlibcExitCode(err)
	switch {
	case len(problems) > 0:
		return elfJoinProblems(problems), true
	case !clean:
		return who + " did not finish: " + firstLine(fmt.Sprint(err)), true
	case code != 0:
		return fmt.Sprintf("%s ended with status %d: %s", who, code, firstLine(strings.TrimSpace(out))), true
	case listed == 0:
		return who + " listed no libraries: " + firstLine(strings.TrimSpace(out)), true
	}
	return "", true
}

// raGetVersionIn returns the highest RetroArch version in data above best: a
// string "1.22.2" (RetroArch's PACKAGE_VERSION) standing alone or after
// "RetroArch " or "/". Library versions compiled into the same binary (zlib's
// 1.3.1, libpng's 1.6.43) never have a two-digit minor
// (ppssppPlausibleVersion).
func raGetVersionIn(data []byte, best string) string {
	marker := []byte("1.")
	for i := 0; i < len(data); {
		j := bytes.Index(data[i:], marker)
		if j < 0 {
			break
		}
		at := i + j
		i = at + 2
		if at > 0 {
			switch data[at-1] {
			case 0, ' ', '/', 'v':
			default:
				continue
			}
		}
		k := at
		for k < len(data) && k-at < 10 && ((data[k] >= '0' && data[k] <= '9') || data[k] == '.') {
			k++
		}
		if k >= len(data) {
			break // cut at the end of the window; the next one sees it whole
		}
		switch data[k] {
		case 0, ' ', '\n', '"', ')', '-':
		default:
			continue
		}
		v := string(data[at:k])
		if strings.Count(v, ".") != 2 {
			continue
		}
		if v = ppssppPlausibleVersion(v); v != "" && (best == "" || compareCoreVersions(v, best) > 0) {
			best = v
		}
	}
	return best
}

// raGetBinaryVersion reads a RetroArch binary for its version, "" when none is
// found.
func raGetBinaryVersion(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	const chunk = 1 << 20
	const carry = 16
	buf := make([]byte, chunk)
	work := make([]byte, 0, chunk+carry)
	best := ""
	for {
		n, err := f.Read(buf)
		if n > 0 {
			work = append(work, buf[:n]...)
			best = raGetVersionIn(work, best)
			if len(work) > carry {
				copy(work, work[len(work)-carry:])
				work = work[:carry]
			}
		}
		if err != nil {
			return best
		}
	}
}

// raGetPick tries the candidates best first (raGetRank), at most raGetMaxTries
// binaries, and returns the first that starts here: its index, the staged
// file in stage and the verdict. target is the card's RetroArch folder for a
// first install (its own model's builds first; a 7z package whose folder the
// card will get unpacks that folder in the same pass), "" for an update.
func raGetPick(ctx context.Context, cands []raGetCandidate, stage, target string, report func(phase string, done, total int64)) (int, string, raGetVerdict, error) {
	raGetRank(cands, target != "")
	var why []string
	refused := map[uint32]bool{}
	tries := 0
	for i := range cands {
		c := &cands[i]
		if refused[c.binCRC()] {
			continue // the same binary in another package
		}
		if c.szFailed != "" {
			// A 7z package that failed for good here before (raGetPassOver).
			why = append(why, c.label()+": "+c.szFailed)
			continue
		}
		if tries >= raGetMaxTries {
			break
		}
		tries++
		download := func(done, total int64) { report(installPhaseDownload, done, total) }
		var staged string
		var err error
		if c.sz != nil {
			folderFor := ""
			if target != "" && raGetFolderSource(cands, i) == i {
				folderFor = target
			}
			staged, err = raGet7zStage(ctx, c, stage, i, folderFor, true, 0, download)
		} else {
			staged = filepath.Join(stage, fmt.Sprintf("ra64-%d.trimui", i))
			total := int64(c.binSize())
			err = raGetFetch(ctx, c, c.Bin, staged, func(done, _ int64) { download(done, total) })
		}
		if err != nil {
			if ctx.Err() != nil {
				return -1, "", raGetVerdict{}, ctx.Err()
			}
			if c.sz != nil {
				c.szFailed = "its pass failed: " + firstLine(err.Error())
				raGetRefuseIfFinal(c, err)
			}
			logWarn("emulators", "RetroArch install: %s did not download: %v", c.label(), err)
			why = append(why, c.label()+" did not download: "+firstLine(err.Error()))
			continue
		}
		report(installPhaseVerify, 0, 0)
		c.Version = raGetBinaryVersion(staged)
		v := raGetCheck(staged)
		if !v.accepted() {
			refused[c.binCRC()] = true
			// The binary only: a 7z package's folder unpacked beside it can
			// still be the card's (raGetFolderSource). A later start passes
			// such a package over (raGetPassOver).
			_ = os.Remove(staged)
			if c.sz != nil {
				raGetRefuse(c, "its build does not start here: "+v.why)
			}
			logWarn("emulators", "RetroArch install: %s does not start here: %s", c.label(), v.why)
			why = append(why, c.label()+": "+v.why)
			continue
		}
		logInfo("emulators", "RetroArch install: %s %s", c.label(), v.note)
		return i, staged, v, nil
	}
	if len(why) == 0 {
		return -1, "", raGetVerdict{}, errors.New("TrimUI's packages for this model carry no RetroArch JukaHub can read")
	}
	return -1, "", raGetVerdict{}, fmt.Errorf("none of TrimUI's RetroArch builds for this model starts here: %s", elfJoinProblems(why))
}

// ── Writing to the card ──────────────────────────────────────────────────────

// raGetLeanSkip are the parts of RetroArch's folder it runs without, left out
// when the card is short of space: menu pictures and thumbnails, shaders,
// overlays, the game databases and cheats.
var raGetLeanSkip = []string{".retroarch/assets/", ".retroarch/thumbnails/", ".retroarch/shaders/",
	".retroarch/overlays/", ".retroarch/overlay/", ".retroarch/database/", ".retroarch/cheats/", ".retroarch/downloads/"}

// raGetPlanned is one file of the folder to write: its entry and its slash
// path inside the folder.
type raGetPlanned struct {
	f   *zip.File
	rel string
}

// raGetFolderPlan lists the files of c's RetroArch folder the card's folder
// target does not have yet — never one it has — leaving out the binary (it
// comes from the chosen build, last) and JukaHub's manifest, and with lean the
// parts in raGetLeanSkip. total and packed are their unpacked and packed sizes.
func raGetFolderPlan(c *raGetCandidate, target string, lean bool) (plan []raGetPlanned, dirs []string, total, packed int64, err error) {
	for _, f := range c.zr.File {
		under, ok := raGetUnder(f.Name, c.Prefix)
		if !ok {
			continue
		}
		rel, err := emuArchiveRel(under)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		if rel == "" {
			continue
		}
		slash := filepath.ToSlash(rel)
		lowSlash := strings.ToLower(slash)
		if lowSlash == raGetBinaryName || strings.EqualFold(path.Base(slash), raGetManifestName) ||
			strings.HasSuffix(lowSlash, raGetPartSuffix) {
			continue
		}
		if lean && slices.ContainsFunc(raGetLeanSkip, func(p string) bool { return strings.HasPrefix(lowSlash+"/", p) }) {
			continue
		}
		if f.FileInfo().IsDir() {
			dirs = append(dirs, slash)
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(target, rel)); err == nil {
			continue // the card's own file stays
		}
		plan = append(plan, raGetPlanned{f: f, rel: slash})
		total += int64(f.UncompressedSize64)
		packed += int64(f.CompressedSize64)
	}
	if len(plan) > raGetMaxFolderFiles || total > raGetMaxFolder {
		return nil, nil, 0, 0, fmt.Errorf("RetroArch's folder in %s is %d files, %s: more than JukaHub unpacks", c.Asset, len(plan), formatInstallBytes(total))
	}
	return plan, dirs, total, packed, nil
}

// raGetWriteFolder writes plan into target through an os.Root (no entry can
// land outside it), each file beside its name and renamed into place, and
// returns the files it wrote, for raGetUndo.
func raGetWriteFolder(ctx context.Context, c *raGetCandidate, target string, plan []raGetPlanned, dirs []string, total, packed int64, report func(done, total int64)) ([]string, error) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, d := range dirs {
		_ = root.MkdirAll(filepath.FromSlash(d), 0o755)
	}
	c.remote.setBlockSize(raGetBlock)
	// The folder is one run of the zip when the package was made folder by
	// folder, and its files are read in the directory's order, so no block is
	// fetched twice: at most the package once, whatever lies between them.
	c.remote.setBudget(c.remote.transferred() + c.remote.size + 2*raGetBlock)
	var created []string
	var done int64
	for _, p := range plan {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		rel := filepath.FromSlash(p.rel)
		if parent := filepath.Dir(rel); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return created, err
			}
		}
		part := rel + raGetPartSuffix
		base := done
		if err := raGetWriteEntry(root, p.f, part, func(n int64) { report(base+n, total) }); err != nil {
			_ = root.Remove(part)
			return created, fmt.Errorf("%s: %w", p.rel, err)
		}
		if err := root.Rename(part, rel); err != nil {
			_ = root.Remove(part)
			return created, err
		}
		created = append(created, p.rel)
		done += int64(p.f.UncompressedSize64)
		report(done, total)
	}
	logInfo("emulators", "RetroArch install: %d files written (%s, %s packed); %s downloaded from %s's package in all",
		len(created), formatInstallBytes(total), formatInstallBytes(packed), formatInstallBytes(c.remote.transferred()), c.Repo)
	return created, nil
}

// raGetWriteEntry writes entry f to name inside root.
func raGetWriteEntry(root *os.Root, f *zip.File, name string, progress func(n int64)) error {
	rc, err := raGetOpen(f)
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := os.FileMode(0o644)
	if f.Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	out, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, &raGetCounting{r: rc, report: func(done, _ int64) { progress(done) }})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// raGetUndo removes what an install that failed wrote: the files in created,
// or the whole folder when the install made it.
func raGetUndo(target string, created []string, madeTarget bool) {
	if madeTarget {
		if err := os.RemoveAll(target); err != nil {
			logWarn("emulators", "RetroArch install: could not remove the half-written %s: %v", target, err)
		}
		return
	}
	for i := len(created) - 1; i >= 0; i-- {
		_ = os.Remove(filepath.Join(target, filepath.FromSlash(created[i])))
	}
}

// raGetMoveFile moves src to dst: a rename on one card, a copy across two.
func raGetMoveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	_ = os.Remove(src)
	return nil
}

// raGetPlaceBinary puts the staged binary in target as ra64.trimui: beside its
// name first, then renamed over it. keepOld keeps the binary it replaces as
// ra64.trimui.bak (an update); a first install never replaces one.
func raGetPlaceBinary(staged, target string, keepOld bool) (string, error) {
	final := filepath.Join(target, raGetBinaryName)
	if !keepOld && regularFile(final) {
		return "", errRaGetPresent
	}
	part := final + raGetPartSuffix
	_ = os.Remove(part)
	if err := raGetMoveFile(staged, part); err != nil {
		return "", err
	}
	_ = os.Chmod(part, 0o755)
	bak := final + ".bak"
	kept := false
	if keepOld && regularFile(final) {
		_ = os.Remove(bak)
		if err := os.Rename(final, bak); err != nil {
			_ = os.Remove(part)
			return "", fmt.Errorf("could not keep the RetroArch it replaces: %w", err)
		}
		kept = true
	}
	if err := os.Rename(part, final); err != nil {
		if kept {
			_ = os.Rename(bak, final)
		}
		_ = os.Remove(part)
		return "", err
	}
	return final, nil
}

// raGetScreenKeys are the retroarch.cfg settings that hold one model's screen.
var raGetScreenKeys = []string{"video_fullscreen_x", "video_fullscreen_y",
	"custom_viewport_width", "custom_viewport_height", "custom_viewport_x", "custom_viewport_y"}

// raGetFitConfig makes a retroarch.cfg that came from another model's package
// fit this screen: its full-screen size and custom viewport become "0", which
// RetroArch reads as the screen's own. It reports how many lines changed.
func raGetFitConfig(cfg string) (int, error) {
	data, err := os.ReadFile(cfg)
	if err != nil {
		return 0, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	changed := 0
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !slices.Contains(raGetScreenKeys, key) {
			continue
		}
		eol := ""
		switch {
		case strings.HasSuffix(line, "\r\n"):
			eol = "\r\n"
		case strings.HasSuffix(line, "\n"):
			eol = "\n"
		}
		if want := key + ` = "0"` + eol; line != want {
			lines[i] = want
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, linuxPkgWriteFile(cfg, []byte(strings.Join(lines, "")))
}

// raGetStageDir is an empty folder in JukaHub's data folder for the builds
// being tried.
func raGetStageDir() (string, error) {
	data, err := P().DataDir()
	if err != nil || strings.TrimSpace(data) == "" {
		return "", errors.New("JukaHub has no data folder to download RetroArch into")
	}
	dir := filepath.Join(data, "retroarch-get")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	return dir, os.MkdirAll(dir, 0o755)
}

// ── Installing ───────────────────────────────────────────────────────────────

// errRaGetPresent is a card that has a RetroArch (it came, or appeared, while
// JukaHub looked).
var errRaGetPresent = errors.New("RetroArch is already on this card")

// raGetResult is what an install put on the card.
type raGetResult struct {
	Bin     string
	Version string
	From    DeviceModel // whose package the binary came from
	Release string
	Glibc   string
	Files   int
	Lean    bool
}

// raGetDoneMessage is the toast for res.
func raGetDoneMessage(res raGetResult, update bool) string {
	what := "RetroArch"
	if res.Version != "" {
		what += " " + res.Version
	}
	verb := " is installed"
	if update {
		verb = " is updated"
	}
	msg := what + verb + " (TrimUI's " + raGetModelName(res.From) + " build)"
	if res.Glibc == "runtime" {
		msg += " and starts on JukaHub's glibc " + privateGlibcVersion
	} else {
		msg += " — it starts on the firmware's glibc here"
	}
	if res.Lean {
		msg += "; the card was short of space, so its menu pictures and shaders were left out"
	}
	return msg
}

// raGetInstallFirst puts TrimUI's newest RetroArch for this handheld on a card
// that has none (see the top of the file). progress may be nil.
func raGetInstallFirst(ctx context.Context, client *http.Client, progress emuWinProgress) (raGetResult, error) {
	var res raGetResult
	report := func(phase string, done, total int64) {
		if progress != nil {
			progress(phase, done, total)
		}
	}
	if !raGetSupported() {
		return res, errors.New("JukaHub installs RetroArch only on a TrimUI handheld")
	}
	if !raGetMissingNow() {
		return res, errRaGetPresent
	}
	target := raGetTargetDir()
	if target == "" {
		return res, errors.New("no SD card to put RetroArch on")
	}
	model := raGetModel()
	report(installPhaseResolving, 0, 0)
	// JukaHub's glibc first, with its C++ library: the builds are checked
	// against it, and the cores RetroArch loads need it.
	if err := privateGlibcInstallFor(ctx, true); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		logWarn("emulators", "RetroArch install: JukaHub's glibc %s: %v — builds are checked against the firmware's glibc alone", privateGlibcVersion, err)
	}
	raGlibcRuntimeForget()
	list, err := raGetCollect(ctx, client, model, nil)
	if err != nil {
		return res, fmt.Errorf("could not ask GitHub for TrimUI's packages: %w", err)
	}
	if len(list.cands) == 0 {
		why := "none of them holds RetroArch/" + raGetBinaryName
		if len(list.notes) > 0 {
			why = elfJoinProblems(list.notes)
		}
		return res, fmt.Errorf("TrimUI publishes no RetroArch for the %s that JukaHub can read (%s)", raGetModelName(model), why)
	}
	stage, err := raGetStageDir()
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(stage)
	// A 7z package that failed for good at an earlier start is not unpacked
	// again (raget_7z.go).
	raGetPassOver(list.cands)
	pick, staged, verdict, err := raGetPick(ctx, list.cands, stage, target, report)
	if err != nil {
		return res, err
	}
	chosen := &list.cands[pick]
	binSize := int64(chosen.binSize())
	var folder *raGetCandidate
	var created []string
	var lean, madeTarget bool
	for {
		src := raGetFolderSource(list.cands, pick)
		if src < 0 {
			return res, fmt.Errorf("%s comes without RetroArch's folder (retroarch.cfg, cores), and no other package of TrimUI's for this model has one JukaHub could unpack", chosen.label())
		}
		folder = &list.cands[src]
		if folder.sz == nil {
			created, lean, madeTarget, err = raGetZipFolderInto(ctx, folder, target, binSize, report)
		} else {
			created, lean, madeTarget, err = raGet7zFolderInto(ctx, folder, stage, src, target, binSize, report)
			if err != nil && folder.szFailed != "" && ctx.Err() == nil {
				// That package's folder could not be unpacked: the next
				// package with a whole folder.
				logWarn("emulators", "RetroArch install: %v", err)
				continue
			}
		}
		break
	}
	if err != nil {
		return res, err
	}
	// Another model's settings: its screen size and viewport go.
	if folder.Model != raGetPackageModel(model) && slices.Contains(created, "retroarch.cfg") {
		if n, err := raGetFitConfig(filepath.Join(target, "retroarch.cfg")); err != nil {
			logWarn("emulators", "RetroArch install: could not fit the %s's retroarch.cfg to this screen: %v", raGetModelName(folder.Model), err)
		} else if n > 0 {
			logInfo("emulators", "RetroArch install: retroarch.cfg came from the %s's package; %d screen settings now follow this screen", raGetModelName(folder.Model), n)
		}
	}
	_ = os.MkdirAll(filepath.Join(target, ".retroarch", "cores"), 0o755)
	// The binary last (raGetPlaceBinary refuses when a RetroArch appeared there
	// meanwhile: that one stays, and so does nothing of this install).
	bin, err := raGetPlaceBinary(staged, target, false)
	if err != nil {
		raGetUndo(target, created, madeTarget)
		if errors.Is(err, errRaGetPresent) {
			return res, err
		}
		return res, fmt.Errorf("putting %s in place: %w", raGetBinaryName, err)
	}
	st, err := os.Stat(bin)
	if err != nil {
		return res, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	man := raGetManifest{
		Repo: chosen.Repo, Release: chosen.Tag, Asset: chosen.Asset, Model: string(chosen.Model), Version: chosen.Version,
		Built: chosen.When.UTC().Format(time.RFC3339), CRC32: chosen.crc(), Size: st.Size(),
		Folder: folder.Repo + "/" + folder.Tag + "/" + folder.Asset, Files: len(created), Lean: lean,
		Glibc: verdict.glibc(), Installed: now, Seen: list.keys,
	}
	if err := raGetWriteManifest(target, man); err != nil {
		logWarn("emulators", "RetroArch install: could not write %s: %v", raGetManifestName, err)
	}
	res = raGetResult{Bin: bin, Version: chosen.Version, From: chosen.Model, Release: chosen.Tag, Glibc: verdict.glibc(), Files: len(created), Lean: lean}
	glibcNote := "the firmware's glibc"
	if res.Glibc == "runtime" {
		glibcNote = "JukaHub's glibc " + privateGlibcVersion
	}
	logInfo("emulators", "RetroArch installed: %s at %s, %d files of its folder from %s; it starts on %s",
		chosen.label(), bin, len(created), man.Folder, glibcNote)
	raGetAfterInstall(bin)
	return res, nil
}

// raGetZipFolderInto writes the files of a zip package's RetroArch folder the
// card's target does not have — all of them unless the card is short of space
// (binSize: the binary still to come) — and returns what it wrote, whether it
// left out what RetroArch runs without, and whether it made target. What it
// wrote is removed again when it fails.
func raGetZipFolderInto(ctx context.Context, folder *raGetCandidate, target string, binSize int64, report func(phase string, done, total int64)) ([]string, bool, bool, error) {
	plan, dirs, total, packed, err := raGetFolderPlan(folder, target, false)
	if err != nil {
		return nil, false, false, err
	}
	free, ferr := freeBytes(filepath.Dir(target))
	lean := false
	if ferr == nil && free > 0 && free < total+binSize+raGetSpaceMargin {
		lean = true
		if plan, dirs, total, packed, err = raGetFolderPlan(folder, target, true); err != nil {
			return nil, false, false, err
		}
		if free < total+binSize+raGetSpaceMargin {
			return nil, false, false, fmt.Errorf("RetroArch needs about %s on the card, which has %s free", formatInstallBytes(total+binSize+raGetSpaceMargin), formatInstallBytes(free))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, false, err
	}
	madeTarget := !dirExists(target)
	leanNote := ""
	if lean {
		leanNote = " (without what it runs without: the card is short of space)"
	}
	logInfo("emulators", "RetroArch install: writing %d files (%s) of %s %s's RetroArch folder into %s%s",
		len(plan), formatInstallBytes(total), folder.Repo, folder.Tag, target, leanNote)
	created, err := raGetWriteFolder(ctx, folder, target, plan, dirs, total, packed, func(done, all int64) { report(installPhaseExtract, done, all) })
	if err != nil {
		raGetUndo(target, created, madeTarget)
		return nil, false, false, fmt.Errorf("writing RetroArch's folder: %w", err)
	}
	return created, lean, madeTarget, nil
}

// raGetAfterInstall tells everything that looks for RetroArch, and has the
// probe that starts it on JukaHub's glibc (ra_glibc.go) run now rather than
// at the first game.
func raGetAfterInstall(bin string) {
	raGetForgetManaged()
	invalidateEmulatorCache()
	raGlibcRuntimeForget()
	go raGlibcProbePending(bin)
}

// raGetUpdateCandidates is the update look: the packages not seen before
// whose binary is newer than the installed one (another checksum, a later
// time). keys names every package looked at.
func raGetUpdateCandidates(ctx context.Context, client *http.Client, man raGetManifest) ([]raGetCandidate, []string, error) {
	skip := make(map[string]bool, len(man.Seen))
	for _, k := range man.Seen {
		skip[k] = true
	}
	list, err := raGetCollect(ctx, client, raGetModel(), skip)
	if err != nil {
		return nil, nil, err
	}
	built, _ := time.Parse(time.RFC3339, man.Built)
	var newer []raGetCandidate
	for _, c := range list.cands {
		if c.sz != nil && !c.Own {
			// Another model's 7z package: its binary costs most of the
			// package to unpack (raget_7z.go), too much for a pass in the
			// background. A first install still falls back to it.
			continue
		}
		if c.crc() != man.CRC32 && c.When.After(built.Add(time.Minute)) {
			newer = append(newer, c)
		}
	}
	return newer, list.keys, nil
}

// raGetRememberSeen adds keys to the manifest's packages seen.
func raGetRememberSeen(dir string, keys []string) {
	man, ok := raGetReadManifest(dir)
	if !ok {
		return
	}
	changed := false
	for _, k := range keys {
		if !slices.Contains(man.Seen, k) {
			man.Seen = append(man.Seen, k)
			changed = true
		}
	}
	if changed {
		if err := raGetWriteManifest(dir, man); err != nil {
			logWarn("emulators", "RetroArch: could not update %s: %v", raGetManifestName, err)
		}
	}
}

// raGetUpdate brings the RetroArch JukaHub installed forward to TrimUI's newest
// build: the binary alone, checked like a first install's, the old one kept as
// ra64.trimui.bak. errEmuUpdaterCurrent when there is nothing newer.
func raGetUpdate(ctx context.Context, client *http.Client, report func(phase string, done, total int64)) (raGetResult, error) {
	var res raGetResult
	bin, man, ok := raGetManaged()
	if !ok {
		return res, errEmuUpdaterCurrent
	}
	dir := filepath.Dir(bin)
	if crc := raGetFileCRC(bin); crc != man.CRC32 {
		// Changed by hand (same size): the user's RetroArch now.
		logInfo("emulators", "RetroArch at %s is not the build JukaHub installed (%s, not %s): it is left alone from now on", bin, crc, man.CRC32)
		_ = os.Remove(filepath.Join(dir, raGetManifestName))
		raGetForgetManaged()
		return res, errEmuUpdaterCurrent
	}
	newer, keys, err := raGetUpdateCandidates(ctx, client, man)
	if err != nil {
		return res, err
	}
	if len(newer) == 0 {
		raGetRememberSeen(dir, keys)
		return res, errEmuUpdaterCurrent
	}
	stage, err := raGetStageDir()
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(stage)
	pick, staged, verdict, err := raGetPick(ctx, newer, stage, "", report)
	if err != nil {
		if ctx.Err() == nil {
			raGetRememberSeen(dir, keys) // refused builds are not fetched again daily
		}
		return res, err
	}
	if gameScreenSuspended() {
		return res, errors.New("a game started: RetroArch is updated at the next quiet moment")
	}
	chosen := &newer[pick]
	placed, err := raGetPlaceBinary(staged, dir, true)
	if err != nil {
		return res, err
	}
	st, err := os.Stat(placed)
	if err != nil {
		return res, err
	}
	old := man.Version
	man.Repo, man.Release, man.Asset, man.Model, man.Version = chosen.Repo, chosen.Tag, chosen.Asset, string(chosen.Model), chosen.Version
	man.Built, man.CRC32, man.Size, man.Glibc = chosen.When.UTC().Format(time.RFC3339), chosen.crc(), st.Size(), verdict.glibc()
	man.Updated = time.Now().UTC().Format(time.RFC3339)
	for _, k := range keys {
		if !slices.Contains(man.Seen, k) {
			man.Seen = append(man.Seen, k)
		}
	}
	if err := raGetWriteManifest(dir, man); err != nil {
		logWarn("emulators", "RetroArch update: could not write %s: %v", raGetManifestName, err)
	}
	logInfo("emulators", "RetroArch updated: %s (was %s); the old binary is %s.bak", chosen.label(), old, raGetBinaryName)
	res = raGetResult{Bin: placed, Version: chosen.Version, From: chosen.Model, Release: chosen.Tag, Glibc: verdict.glibc()}
	raGetAfterInstall(placed)
	return res, nil
}
