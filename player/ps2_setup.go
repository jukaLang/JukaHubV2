package main

// ps2_setup.go — what a PlayStation 2 game needs before it starts in the
// ARMSX2 core (ps2.go): the core, ARMSX2's resources folder, and the player's
// own BIOS.
//
//   - The core is ARMSX2's libretro build for Linux arm64 from the project's
//     GitHub releases (ARMSX2/ARMSX2, the asset ending
//     "Linux-arm64-libretro-core.tar.zst": a 4K-page build, which is what the
//     TrimUI kernels use, with its own dependencies linked in since ARMSX2
//     PR #716 in September 2026). The libretro buildbot's linux/aarch64 build
//     links libjpeg.so.8, libpcap and plutosvg from the system instead, and the
//     TrimUI firmware has none of them, so it is not the one installed. The
//     newest stable release that carries the build is taken, else the newest
//     nightly. The archive must match the SHA-256 GitHub states for it; the
//     core then passes the load check every core install runs
//     (coreLoadProblemFile: RetroArch's architecture, the firmware's
//     libraries, and JukaHub's private glibc 2.39 where the build needs a newer
//     glibc than the firmware's 2.33 — ARMSX2 builds on Ubuntu 22.04) and goes
//     into RetroArch's cores folder through the core pool like any core
//     (activateCoreBuild: the old build kept as a .bak, an update watched
//     through its first game). A release whose core this firmware refused is
//     not downloaded again; the next one gets its own check.
//   - The core does not boot without ARMSX2's resources folder (GameIndex.yaml
//     with the per-game fixes, the renderer shaders, fonts) from a matching
//     build, in <system>/pcsx2/resources. The core's archive holds the .so
//     alone, so the folder comes from the same release's SDL handheld archive,
//     of which only the resources tree is unpacked. A resources folder JukaHub
//     put there is replaced along with the core; one the player put there is
//     left alone unless it is missing.
//   - The BIOS cannot be downloaded: it is a dump of the player's own console.
//     The core reads <system>/pcsx2/bios. A valid image (4 to 8 MB with the PS2
//     ROM directory, the test PCSX2 itself applies) found in the usual BIOS
//     folders, their PS2 subfolders or the game's own folder is copied there.
//
// armsx2.json in JukaHub's data folder records what this setup installed: the
// release, the core file as it was then, the resources folder, and the
// renderer the start-failure fallback chose (ps2_video.go).

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	// ps2ReleasesURL lists ARMSX2's releases, newest first.
	ps2ReleasesURL = "https://api.github.com/repos/ARMSX2/ARMSX2/releases?per_page=30"
	// ps2ReleasesTTL is how long one read of that list is used.
	ps2ReleasesTTL = time.Hour
	// ps2DownloadMax bounds one archive (the handheld archive is the larger).
	ps2DownloadMax = int64(768 << 20)
	// ps2UnpackMax and ps2UnpackFilesMax bound what one archive may unpack.
	ps2UnpackMax      = int64(2 << 30)
	ps2UnpackFilesMax = 50000
	// ps2NeedBytes is the free space a setup keeps on the card: an archive and
	// what it unpacks beside it.
	ps2NeedBytes = int64(640 << 20)
	// ps2SetupTimeout bounds one setup: two archives on handheld Wi-Fi.
	ps2SetupTimeout = 45 * time.Minute
	// ps2StateName is the record in JukaHub's data folder.
	ps2StateName = "armsx2.json"
	// ps2WorkName is the setup's scratch folder inside <system>/pcsx2 (the
	// same card as the resources folder, so it moves in with a rename).
	ps2WorkName = ".jukahub-armsx2"
	// ps2BIOSMin and ps2BIOSMax are the image sizes PCSX2 accepts.
	ps2BIOSMin = int64(4 << 20)
	ps2BIOSMax = int64(8 << 20)
	// ps2BIOSScan is how far into an image the ROM directory is looked for:
	// a real BIOS has it in the first few KB.
	ps2BIOSScan = int64(1 << 20)
)

// ps2Release is one release in GitHub's list.
type ps2Release struct {
	Tag        string     `json:"tag_name"`
	Name       string     `json:"name"`
	Draft      bool       `json:"draft"`
	Prerelease bool       `json:"prerelease"`
	Published  time.Time  `json:"published_at"`
	Assets     []ps2Asset `json:"assets"`
}

// ps2Asset is one file of a release.
type ps2Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>", when GitHub states one
}

// ps2Pick is the release a setup installs from: its core archive, and the
// archive its resources folder comes from (the core's own when that holds
// one, else the SDL handheld build's).
type ps2Pick struct {
	Release   ps2Release
	Core      ps2Asset
	Resources ps2Asset
}

// ps2Date is the release's day, the version the core pool files it under.
func (p ps2Pick) ps2Date() string {
	if p.Release.Published.IsZero() {
		return time.Now().UTC().Format("2006-01-02")
	}
	return p.Release.Published.UTC().Format("2006-01-02")
}

// ps2Record is armsx2.json.
type ps2Record struct {
	// Release is the release tag the core came from; the core file as it was
	// when this setup installed it.
	Release  string `json:"release,omitempty"`
	CorePath string `json:"corePath,omitempty"`
	CoreSize int64  `json:"coreSize,omitempty"`
	CoreTime int64  `json:"coreTime,omitempty"` // modification time, Unix seconds
	// ResRelease is the release the resources folder at ResPath came from.
	ResRelease string `json:"resourcesRelease,omitempty"`
	ResPath    string `json:"resourcesPath,omitempty"`
	// Refused lists the releases whose core cannot load on this firmware for
	// a reason JukaHub's private glibc does not change (the architecture, a
	// library the firmware lacks), the last one's reason in RefusedWhy: those
	// releases are not picked again.
	Refused    []string `json:"refused,omitempty"`
	RefusedWhy string   `json:"refusedWhy,omitempty"`
	// Renderer is the value JukaHub wrote for armsx2_renderer into the options
	// file at RendererPath; RendererFailed lists the renderers a start could
	// not draw with in the core build RendererCore names (path, size and
	// time; ps2_video.go).
	Renderer       string   `json:"renderer,omitempty"`
	RendererPath   string   `json:"rendererPath,omitempty"`
	RendererFailed []string `json:"rendererFailed,omitempty"`
	RendererCore   string   `json:"rendererCore,omitempty"`
}

var ps2State struct {
	mu     sync.Mutex
	loaded bool
	rec    ps2Record
	// The release list as last read, at listAt.
	list   []ps2Release
	listAt time.Time
	// bios caches the BIOS test by path|size|time; load the load check of a
	// core JukaHub did not install (ps2LoadProblem).
	bios map[string]bool
	load map[string]string
}

// ps2StatePath is armsx2.json's path, "" without a data folder.
func ps2StatePath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, ps2StateName)
}

// ps2LoadLocked reads the record once. Under ps2State.mu.
func ps2LoadLocked() {
	if ps2State.loaded {
		return
	}
	ps2State.loaded = true
	p := ps2StatePath()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var rec ps2Record
	if err := json.Unmarshal(data, &rec); err != nil {
		logWarn("library", "PS2: ignoring %s: %v", p, err)
		return
	}
	ps2State.rec = rec
}

// ps2RecordGet is a copy of the record.
func ps2RecordGet() ps2Record {
	ps2State.mu.Lock()
	defer ps2State.mu.Unlock()
	ps2LoadLocked()
	rec := ps2State.rec
	rec.RendererFailed = slices.Clone(rec.RendererFailed)
	rec.Refused = slices.Clone(rec.Refused)
	return rec
}

// ps2RecordUpdate changes the record and saves it.
func ps2RecordUpdate(change func(*ps2Record)) {
	ps2State.mu.Lock()
	defer ps2State.mu.Unlock()
	ps2LoadLocked()
	change(&ps2State.rec)
	p := ps2StatePath()
	if p == "" {
		return
	}
	data, err := json.MarshalIndent(ps2State.rec, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o644); err != nil {
		logWarn("library", "PS2: could not save %s: %v", p, err)
	}
}

// ps2ManagesCore reports a core the buildbot's updates must leave alone here:
// ARMSX2 on the handheld comes from ARMSX2's own releases with the resources
// folder that matches it, and the buildbot's build links libraries the
// TrimUI firmware lacks (planCoreUpdates, emuCompareCores).
func ps2ManagesCore(core string) bool {
	return normalizeCoreName(core) == ps2CoreName && ps2SoC() != ""
}

// ps2FindCore is the ARMSX2 core a game starts in: the active build with a
// RetroArch beside it, or "".
func ps2FindCore() string {
	core := coreActiveBinary(ps2CoreName, retroArchCoreDirs())
	if core == "" || coreRetroArchBinary(filepath.Dir(core)) == "" {
		return ""
	}
	return core
}

// ps2CoreIsOurs reports whether core is the file this setup installed, as it
// was then (not replaced since by RetroArch's updater or by hand).
func ps2CoreIsOurs(core string, rec ps2Record) bool {
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() || rec.Release == "" {
		return false
	}
	return rec.CorePath == filepath.Clean(core) && rec.CoreSize == info.Size() && rec.CoreTime == info.ModTime().Unix()
}

// ps2ResourcesDir and ps2BIOSDir are where the core reads its folders from
// RetroArch's system folder.
func ps2ResourcesDir(sysDir string) string { return filepath.Join(sysDir, "pcsx2", "resources") }
func ps2BIOSDir(sysDir string) string      { return filepath.Join(sysDir, "pcsx2", "bios") }

// ps2ResourcesPresent reports whether a resources folder with the game index
// (which every ARMSX2 and PCSX2 build carries) is in sysDir.
func ps2ResourcesPresent(sysDir string) bool {
	if strings.TrimSpace(sysDir) == "" {
		return false
	}
	return regularFile(filepath.Join(ps2ResourcesDir(sysDir), "GameIndex.yaml"))
}

// ps2Needs is what a start in ARMSX2 lacks (ps2Inspect).
type ps2Needs struct {
	Core        string // the core file, "" when there is none
	CoreMissing bool
	// CoreBroken is why the core there cannot load here ("" when it can, or
	// when only JukaHub's newer glibc is missing, which ra_glibc.go fetches).
	CoreBroken string
	SysDir     string // RetroArch's system folder
	ResMissing bool
	// ResStale: JukaHub's resources are from another release than JukaHub's
	// core.
	ResStale bool
	BIOS     string // a valid image the core reads, "" when there is none
	// BIOSFound is a valid image elsewhere, which the setup copies in.
	BIOSFound string
	Why       string // what stops the start that no download fixes
}

// ps2NeedsDownload reports whether what n lacks takes a download.
func (n ps2Needs) ps2NeedsDownload() bool {
	return n.CoreMissing || n.CoreBroken != "" || n.ResMissing || n.ResStale
}

// ps2NeedsAnything reports whether a setup has anything to do for n.
func (n ps2Needs) ps2NeedsAnything() bool {
	return n.ps2NeedsDownload() || (n.BIOS == "" && n.BIOSFound != "")
}

// ps2NeedLabel names what a setup fetches, for a toast.
func ps2NeedLabel(n ps2Needs) string {
	switch {
	case n.CoreMissing:
		return "the ARMSX2 core"
	case n.CoreBroken != "":
		return "an ARMSX2 core that runs here"
	case n.ResMissing || n.ResStale:
		return "ARMSX2's resources folder"
	}
	return "your BIOS in ARMSX2's folder"
}

// ps2Inspect looks at the core launcher starts (the active ARMSX2 core for a
// script or for no launcher), RetroArch's system folder and the BIOS for a
// game at romPath ("" from the Emulators page or Settings: a missing BIOS
// stops nothing then). A few stats, one config read, at most a load check of
// a core JukaHub did not install and the BIOS test of a few small files —
// cheap enough for the main thread.
func ps2Inspect(launcher, romPath string) ps2Needs {
	var n ps2Needs
	rec := ps2RecordGet()
	script := strings.TrimSpace(launcher) != "" && !ps2IsCore(launcher)
	core := launcher
	if !ps2IsCore(core) {
		core = ps2FindCore()
	}
	n.Core = core
	ours := core != "" && ps2CoreIsOurs(core, rec)
	switch {
	case script:
		// A RetroArch script brings its own core: JukaHub neither installs nor
		// replaces one for it.
	case core == "" || !regularFile(core):
		n.CoreMissing = true
	case !ours:
		// A build JukaHub did not install (RetroArch's own updater fetches the
		// buildbot's, which needs libraries this firmware lacks). A gap only
		// JukaHub's glibc closes is not one: the launch fetches that.
		if problem := ps2LoadProblem(core); problem != "" && !raGlibcCouldCover(problem) {
			n.CoreBroken = problem
		}
	}
	from := core
	if script || from == "" {
		from = launcher
	}
	n.SysDir, _, _ = gameDiagSystemDir(from, romPath)
	if err := ps2CheckSysDir(n.SysDir, romPath); err != nil {
		n.Why = err.Error()
		return n
	}
	resDir := ps2ResourcesDir(n.SysDir)
	switch {
	case !ps2ResourcesPresent(n.SysDir):
		n.ResMissing = true
	case ours && rec.ResPath == filepath.Clean(resDir) && rec.ResRelease != rec.Release:
		n.ResStale = true
	}
	n.BIOS = ps2BIOSIn(ps2BIOSDir(n.SysDir))
	if n.BIOS == "" {
		n.BIOSFound = ps2FindBIOS(n.SysDir, romPath)
		if n.BIOSFound == "" && strings.TrimSpace(romPath) != "" {
			n.Why = ps2NoBIOSHint(n.SysDir)
		}
	}
	return n
}

// ps2LoadProblem is the load check of a core file (coreLoadProblemFile),
// remembered by its path, size and time and the private glibc's state: it
// reads the ELF's tables, too much for every start on the main thread.
func ps2LoadProblem(core string) string {
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%d|%s", filepath.Clean(core), info.Size(), info.ModTime().UnixNano(), raGlibcStamp())
	ps2State.mu.Lock()
	if problem, seen := ps2State.load[key]; seen {
		ps2State.mu.Unlock()
		return problem
	}
	ps2State.mu.Unlock()
	problem := coreLoadProblemFile(core, core)
	ps2State.mu.Lock()
	if ps2State.load == nil || len(ps2State.load) > 32 {
		ps2State.load = map[string]string{}
	}
	ps2State.load[key] = problem
	ps2State.mu.Unlock()
	return problem
}

// ps2CheckSysDir says why sysDir cannot take ARMSX2's folders, or nil.
// RetroArch set to "default" reads system files from each game's own folder.
func ps2CheckSysDir(sysDir, romPath string) error {
	if s := strings.TrimSpace(sysDir); s == "" || s == "." {
		return errors.New("RetroArch's system folder could not be found, so ARMSX2's files have nowhere to go")
	}
	if strings.TrimSpace(romPath) != "" && filepath.Clean(sysDir) == filepath.Clean(filepath.Dir(romPath)) {
		return errors.New("RetroArch reads system files from each game's own folder: set its System/BIOS folder (RetroArch → Settings → Directory), then start the game again")
	}
	return nil
}

// ps2CardPath shortens a path on the SD card for a message.
func ps2CardPath(p string) string {
	root := filepath.Dir(deviceRomsRoot)
	if rel, err := filepath.Rel(root, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel) + " on the SD card"
	}
	return p
}

// ps2NoBIOSHint is what a start says when no PS2 BIOS is anywhere.
func ps2NoBIOSHint(sysDir string) string {
	return "PlayStation 2 games need a BIOS dumped from your own console (a 4 MB file such as SCPH-70012.bin): copy it to " +
		ps2CardPath(ps2BIOSDir(sysDir)) + " (or BIOS/PS2), then start the game again"
}

// ── The BIOS ─────────────────────────────────────────────────────────────

// ps2BIOSValid reports whether path is a PS2 BIOS image as PCSX2 tests one:
// 4 to 8 MB, with the ROM directory — a 16-byte entry "RESET" on a 16-byte
// boundary — near the start and a "ROMVER" entry after it. The answer is
// cached by path, size and time.
func ps2BIOSValid(p string) bool {
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() < ps2BIOSMin || info.Size() > ps2BIOSMax {
		return false
	}
	key := fmt.Sprintf("%s|%d|%d", filepath.Clean(p), info.Size(), info.ModTime().UnixNano())
	ps2State.mu.Lock()
	if ok, seen := ps2State.bios[key]; seen {
		ps2State.mu.Unlock()
		return ok
	}
	ps2State.mu.Unlock()
	ok := ps2BIOSRomdir(p)
	ps2State.mu.Lock()
	if ps2State.bios == nil || len(ps2State.bios) > 256 {
		ps2State.bios = map[string]bool{}
	}
	ps2State.bios[key] = ok
	ps2State.mu.Unlock()
	return ok
}

// ps2BIOSRomdir reads the start of the image for the ROM directory.
func ps2BIOSRomdir(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, ps2BIOSScan)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	buf = buf[:n-n%16]
	entry := func(i int, name string) bool {
		e := buf[i : i+10]
		return string(e[:len(name)]) == name && e[len(name)] == 0
	}
	for i := 0; i+16 <= len(buf); i += 16 {
		if !entry(i, "RESET") {
			continue
		}
		// ROMVER follows within the directory's first entries.
		for j := i + 16; j+16 <= len(buf) && j < i+16*64; j += 16 {
			if entry(j, "ROMVER") {
				return true
			}
		}
		return false
	}
	return false
}

// ps2NotBIOSExts are game and archive files no BIOS is named like: the game's
// own folder is skipped through without a look at each of its discs.
var ps2NotBIOSExts = map[string]bool{
	".iso": true, ".chd": true, ".cso": true, ".zso": true, ".cue": true, ".m3u": true, ".elf": true,
	".gz": true, ".zip": true, ".7z": true, ".png": true, ".jpg": true, ".txt": true, ".srm": true,
}

// ps2BIOSIn is the first valid image in dir, by name; "" when there is none.
func ps2BIOSIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || ps2NotBIOSExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		if p := filepath.Join(dir, e.Name()); ps2BIOSValid(p) {
			return p
		}
	}
	return ""
}

// ps2BIOSSubdirs are the folders under a BIOS folder a PS2 BIOS is put in by
// the other frontends (NextUI's PS2 tag, RetroArch's pcsx2/bios, AetherSX2's
// and ARMSX2's own folders).
var ps2BIOSSubdirs = []string{"", "PS2", "ps2", "pcsx2", "pcsx2/bios", "PCSX2", "PCSX2/bios", "armsx2", "ARMSX2", "aethersx2", "aethersx2/bios", "AetherSX2", "PlayStation 2"}

// ps2FindBIOS looks for a valid image outside the folder the core reads: the
// BIOS folders JukaHub knows (RetroArch's first), their PS2 subfolders, and
// the game's own folder. "" when there is none.
func ps2FindBIOS(sysDir, romPath string) string {
	roots := []string{sysDir}
	roots = append(roots, biosDoctorRoots()...)
	if !IsWindows() {
		card := filepath.Dir(deviceRomsRoot)
		roots = append(roots, filepath.Join(card, "Bios"), filepath.Join(card, "bios"))
	}
	target := filepath.Clean(ps2BIOSDir(sysDir))
	seen := map[string]bool{target: true}
	var dirs []string
	add := func(d string) {
		if strings.TrimSpace(d) == "" {
			return
		}
		d = filepath.Clean(d)
		if d == "." || seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		for _, sub := range ps2BIOSSubdirs {
			add(filepath.Join(root, filepath.FromSlash(sub)))
		}
	}
	if strings.TrimSpace(romPath) != "" {
		romDir := filepath.Dir(romPath)
		for _, sub := range []string{"", "bios", "BIOS", "Bios"} {
			add(filepath.Join(romDir, sub))
		}
	}
	// A FAT card answers PS2 and ps2 with the same folder: each is read once.
	var read []os.FileInfo
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			continue
		}
		if slices.ContainsFunc(read, func(o os.FileInfo) bool { return os.SameFile(o, info) }) {
			continue
		}
		read = append(read, info)
		if p := ps2BIOSIn(d); p != "" {
			return p
		}
	}
	return ""
}

// ps2CopyBIOS copies the image at src into the folder the core reads, under
// its own name (another name when a different file already has it), and
// returns the copy.
func ps2CopyBIOS(src, sysDir string) (string, error) {
	dir := ps2BIOSDir(sysDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(src)
	dest := filepath.Join(dir, name)
	if _, err := os.Lstat(dest); err == nil {
		ext := filepath.Ext(name)
		dest = filepath.Join(dir, strings.TrimSuffix(name, ext)+"-jukahub"+ext)
	}
	part := dest + ".part"
	if _, err := copyCompatFile(src, part); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	syncDirectory(dir)
	if !ps2BIOSValid(dest) {
		_ = os.Remove(dest)
		return "", errors.New("the copied BIOS did not read back")
	}
	logInfo("library", "PS2: copied the BIOS %s into %s", src, dir)
	return dest, nil
}

// ── ARMSX2's releases ────────────────────────────────────────────────────

// ps2Releases is ARMSX2's release list, read at most once an hour.
func ps2Releases(ctx context.Context, client *http.Client) ([]ps2Release, error) {
	ps2State.mu.Lock()
	if ps2State.list != nil && time.Since(ps2State.listAt) < ps2ReleasesTTL {
		list := ps2State.list
		ps2State.mu.Unlock()
		return list, nil
	}
	ps2State.mu.Unlock()
	data, err := fetchUpdateBytes(ctx, client, ps2ReleasesURL, patchMaxMetadata)
	if err != nil {
		return nil, fmt.Errorf("ARMSX2's releases on GitHub: %w", err)
	}
	var list []ps2Release
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("ARMSX2's releases on GitHub: %w", err)
	}
	ps2State.mu.Lock()
	ps2State.list, ps2State.listAt = list, time.Now()
	ps2State.mu.Unlock()
	return list, nil
}

// ps2ArchiveExts are the archive kinds the setup unpacks.
var ps2ArchiveExts = []string{".tar.zst", ".tzst", ".tar.gz", ".tgz", ".tar"}

// ps2AssetIs reports whether an asset is a Linux arm64 archive of kind
// ("libretro" for the core, "handheld" for the SDL handheld build). A 16K-page
// build (Apple Silicon kernels) is never one: the TrimUI kernels use 4K pages.
func ps2AssetIs(a ps2Asset, kind string) bool {
	name := strings.ToLower(a.Name)
	if !strings.Contains(name, "linux") || !(strings.Contains(name, "arm64") || strings.Contains(name, "aarch64")) {
		return false
	}
	if !strings.Contains(name, kind) || strings.Contains(name, "16k") || strings.TrimSpace(a.URL) == "" {
		return false
	}
	for _, ext := range ps2ArchiveExts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// ps2AssetOf is release's archive of kind, a 4K-page one first.
func ps2AssetOf(r ps2Release, kind string) (ps2Asset, bool) {
	var found ps2Asset
	ok := false
	for _, a := range r.Assets {
		if !ps2AssetIs(a, kind) {
			continue
		}
		if strings.Contains(strings.ToLower(a.Name), "4k") {
			return a, true
		}
		if !ok {
			found, ok = a, true
		}
	}
	return found, ok
}

// ps2PickRelease is the release a setup installs from: the newest stable one
// with a Linux arm64 libretro core, else the newest nightly with one, leaving
// out the releases this firmware refused. A release without the handheld
// archive is taken only when its core archive may hold the resources folder
// itself (checked when it is unpacked).
func ps2PickRelease(list []ps2Release, refused []string) (ps2Pick, bool) {
	sorted := slices.Clone(list)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Published.After(sorted[j].Published) })
	var stable, nightly *ps2Pick
	for _, r := range sorted {
		if r.Draft || strings.TrimSpace(r.Tag) == "" || slices.Contains(refused, r.Tag) {
			continue
		}
		core, ok := ps2AssetOf(r, "libretro")
		if !ok {
			continue
		}
		res, ok := ps2AssetOf(r, "handheld")
		if !ok {
			res = core
		}
		pick := ps2Pick{Release: r, Core: core, Resources: res}
		if r.Prerelease || strings.Contains(strings.ToLower(r.Tag), "nightly") {
			if nightly == nil {
				nightly = &pick
			}
			continue
		}
		if stable == nil {
			stable = &pick
		}
	}
	switch {
	case stable != nil:
		return *stable, true
	case nightly != nil:
		return *nightly, true
	}
	return ps2Pick{}, false
}

// ps2PickTag is the release named tag in list, with its archives; false when
// GitHub no longer lists it (old nightlies are pruned) or it has no core.
func ps2PickTag(list []ps2Release, tag string) (ps2Pick, bool) {
	if strings.TrimSpace(tag) == "" {
		return ps2Pick{}, false
	}
	for _, r := range list {
		if r.Draft || r.Tag != tag {
			continue
		}
		core, ok := ps2AssetOf(r, "libretro")
		if !ok {
			return ps2Pick{}, false
		}
		res, ok := ps2AssetOf(r, "handheld")
		if !ok {
			res = core
		}
		return ps2Pick{Release: r, Core: core, Resources: res}, true
	}
	return ps2Pick{}, false
}

// ── The setup ────────────────────────────────────────────────────────────

// ps2Setup installs what a game at romPath ("" for none: the Emulators page,
// Settings) needs to start in ARMSX2 with launcher ("" for the core JukaHub
// sets up) and returns the core file: the BIOS copied in, the core when there
// is none, it cannot load, or (update) a newer release is out, and the
// resources folder from the same release. stale lets JukaHub's resources from
// another release than its core be replaced (a start offline leaves them). A
// RetroArch script that names the core brings its own: only the folders of
// the RetroArch it starts are set up for it. Off the main thread; the caller
// holds the patch slot. phase names each step (with a detail line); progress
// gets download bytes.
func ps2Setup(ctx context.Context, launcher, romPath string, update, stale bool, phase func(phase, detail string), progress func(done, total int64)) (string, error) {
	need := ps2Inspect(launcher, romPath)
	if need.Why != "" {
		return "", errors.New(need.Why)
	}
	if !stale {
		need.ResStale = false
	}
	script := strings.TrimSpace(launcher) != "" && !ps2IsCore(launcher)
	core := need.Core
	if need.BIOS == "" && need.BIOSFound != "" {
		phase(installPhaseExtract, "Your PS2 BIOS")
		if _, err := ps2CopyBIOS(need.BIOSFound, need.SysDir); err != nil {
			return "", fmt.Errorf("copying the BIOS %s into %s: %w", need.BIOSFound, ps2BIOSDir(need.SysDir), err)
		}
	}
	rec := ps2RecordGet()
	ours := core != "" && ps2CoreIsOurs(core, rec)
	if !need.ps2NeedsDownload() && !(update && !script) {
		return core, nil
	}
	// A core that plays now (one an update or new resources would replace).
	usable := core != "" && !need.CoreMissing && need.CoreBroken == ""
	client := newEmulatorUpdateClient()
	phase(installPhaseResolving, "ARMSX2's releases on GitHub")
	list, err := ps2Releases(ctx, client)
	if err != nil {
		if usable && !update && !need.ResMissing && ctx.Err() == nil {
			// Only JukaHub's resources were out of date: the game plays with
			// the ones there.
			logWarn("library", "PS2: ARMSX2's resources not refreshed: %v", err)
			return core, nil
		}
		return "", err
	}
	pick, ok := ps2PickRelease(list, rec.Refused)
	if !ok {
		if len(rec.Refused) > 0 && !usable {
			return "", fmt.Errorf("no ARMSX2 core published now runs on this firmware (%s) — a later release gets its own check", rec.RefusedWhy)
		}
		if !usable {
			return "", errors.New("ARMSX2 publishes no Linux arm64 libretro core right now — try again later")
		}
		// The build there plays; nothing newer runs here.
		if update {
			return core, nil
		}
		need.ResStale = false
		if !need.ResMissing {
			return core, nil
		}
		return "", errors.New("ARMSX2's resources folder is missing and ARMSX2 publishes no build to take it from right now — try again later")
	}
	// The core: when there is none or it cannot load here, and on a press of
	// Update (the newest release, over JukaHub's older one or a core the
	// player installed by hand).
	wantCore := false
	switch {
	case script:
	case need.CoreMissing || need.CoreBroken != "":
		wantCore = true
	case update:
		wantCore = !ours || rec.Release != pick.Release.Tag
	}
	// The resources folder comes from the core's own release: for JukaHub's
	// core that is the release it came from while GitHub still lists it, else
	// both come from the newest. A core installed by hand gets the newest.
	resPick := pick
	if !wantCore && ours && (need.ResMissing || need.ResStale) {
		if p, ok := ps2PickTag(list, rec.Release); ok {
			resPick = p
		} else {
			wantCore = true
		}
	}
	resDir := ps2ResourcesDir(need.SysDir)
	resOurs := rec.ResPath == filepath.Clean(resDir) && rec.ResRelease != ""
	wantRes := need.ResMissing || need.ResStale || (wantCore && resOurs)
	if !wantCore && !wantRes {
		return core, nil // already the newest
	}
	work := filepath.Join(need.SysDir, "pcsx2", ps2WorkName)
	if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
		return "", err
	}
	if free, ok := emuMoreCoresLeastFree([]string{filepath.Dir(work)}); ok && free < ps2NeedBytes {
		return "", fmt.Errorf("only %s free on the card: ARMSX2 needs about %s while it unpacks", bootFormatBytes(free), bootFormatBytes(ps2NeedBytes))
	}
	// What a setup that died midway left.
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	resStage := ""
	if wantCore {
		installed, stage, err := ps2InstallCore(ctx, client, pick, core, work, phase, progress)
		switch {
		case err == nil:
			core, resStage = installed, stage
			resPick = pick
		case usable && !update && ctx.Err() == nil:
			// A start that only wanted the newest release beside new
			// resources: the build there still plays. Its resources stay
			// unless they are missing (then the newest are the closest match
			// there is), so a build that keeps failing is not followed by a
			// resources download at every start.
			logWarn("library", "PS2: ARMSX2 could not be updated, keeping %s: %v", core, err)
			ShowToast("ARMSX2 could not be updated ("+firstLine(emuFriendlyError(err))+") — the game plays in the build there", ToastKindWarning)
			wantRes = need.ResMissing
		default:
			return "", err
		}
	}
	if wantRes {
		if err := ps2PutResources(ctx, client, resPick, resStage, resDir, work, phase, progress); err != nil {
			if !ps2ResourcesPresent(need.SysDir) || ctx.Err() != nil {
				return "", err
			}
			// The folder there stays (an older release's): a game still boots
			// with it more often than not.
			logWarn("library", "PS2: ARMSX2's resources could not be replaced, keeping %s: %v", resDir, err)
			ShowToast("ARMSX2's resources could not be updated ("+firstLine(emuFriendlyError(err))+") — the ones there stay", ToastKindWarning)
		}
	}
	phase(installPhaseVerify, "")
	if !ps2ResourcesPresent(need.SysDir) {
		return "", errors.New("ARMSX2's resources folder did not land in " + resDir)
	}
	if script {
		return core, nil // the script starts its own core
	}
	if core == "" || coreRetroArchBinary(filepath.Dir(core)) == "" {
		return "", errors.New("ARMSX2 is in, but there is no RetroArch beside it to start it")
	}
	return core, nil
}

// ps2PutResources puts pick's resources folder in place of resDir: the one
// unpacked from the core's archive (stage), else the release's resources
// archive downloaded into work.
func ps2PutResources(ctx context.Context, client *http.Client, pick ps2Pick, stage, resDir, work string, phase func(phase, detail string), progress func(done, total int64)) error {
	if stage == "" {
		fetched, err := ps2FetchResources(ctx, client, pick, work, phase, progress)
		if err != nil {
			return err
		}
		stage = fetched
	}
	phase(installPhaseExtract, "ARMSX2's resources folder")
	if err := ps2SwapIn(stage, resDir); err != nil {
		return fmt.Errorf("ARMSX2's resources into %s: %w", resDir, err)
	}
	ps2RecordUpdate(func(r *ps2Record) {
		r.ResRelease, r.ResPath = pick.Release.Tag, filepath.Clean(resDir)
	})
	logInfo("library", "PS2: ARMSX2's resources folder (%s) is in %s", pick.Release.Tag, resDir)
	return nil
}

// ps2InstallCore downloads pick's core archive into work, puts the core into
// RetroArch's cores folder over current ("" for none) and returns the active
// file, with the resources folder when the archive held one (unpacked under
// work, else "").
func ps2InstallCore(ctx context.Context, client *http.Client, pick ps2Pick, current, work string, phase func(phase, detail string), progress func(done, total int64)) (string, string, error) {
	phase(installPhaseDownload, "ARMSX2 core, "+pick.Release.Tag)
	archive := filepath.Join(work, path.Base(pick.Core.Name))
	if err := ps2Download(ctx, client, pick.Core, archive, progress); err != nil {
		return "", "", fmt.Errorf("the ARMSX2 core: %w", err)
	}
	phase(installPhaseExtract, "ARMSX2 core, "+pick.Release.Tag)
	stage := filepath.Join(work, "core")
	bin, resStage, err := ps2Unpack(ctx, archive, stage, true)
	if err != nil {
		return "", "", fmt.Errorf("the ARMSX2 core: %w", err)
	}
	_ = os.Remove(archive)
	if bin == "" {
		return "", "", fmt.Errorf("%s has no %s", pick.Core.Name, ps2CoreFile)
	}
	// The file the build becomes: the core there, else a new one in the first
	// cores folder JukaHub may write. The load check asks about that place
	// (its RetroArch, its glibc).
	target := current
	if target == "" || !regularFile(target) {
		t, err := coreActivationTarget(coreBuild{Core: ps2CoreName, File: ps2CoreFile})
		if err != nil {
			return "", "", err
		}
		target = t
	}
	if coreDirIsSystem(filepath.Dir(target)) {
		return "", "", fmt.Errorf("the ARMSX2 core RetroArch loads is part of the firmware (%s) and is not replaced", filepath.Dir(target))
	}
	problem := coreLoadProblemFile(bin, target)
	if problem != "" && raGlibcEnsureForCore(ctx, problem, target) {
		problem = coreLoadProblemFile(bin, target)
	}
	if problem != "" {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if raGlibcCouldCover(problem) {
			// Only JukaHub's glibc was missing (its download failed, or its
			// setting is off): nothing about this release is final.
			return "", "", fmt.Errorf("ARMSX2 needs JukaHub's glibc %s, which could not be set up (check Wi-Fi, and that the setting \"RetroArch's C library\" is not Off): %s", privateGlibcVersion, problem)
		}
		ps2RecordUpdate(func(r *ps2Record) {
			if !slices.Contains(r.Refused, pick.Release.Tag) {
				r.Refused = append(r.Refused, pick.Release.Tag)
			}
			if len(r.Refused) > 20 {
				r.Refused = r.Refused[len(r.Refused)-20:]
			}
			r.RefusedWhy = problem
		})
		logWarn("library", "PS2: ARMSX2 %s refused: %s", pick.Release.Tag, problem)
		return "", "", &coreRefusedError{Core: ps2CoreName, Build: pick.Release.Tag, Reason: coreCannotLoadReason + problem}
	}
	// Into the pool, then active like any core build.
	pool := compatCorePoolDir()
	date := pick.ps2Date()
	dir := filepath.Join(pool, ps2CoreName, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	pooled := filepath.Join(dir, ps2CoreFile)
	if err := os.Rename(bin, pooled); err != nil {
		if _, cerr := copyCompatFile(bin, pooled); cerr != nil {
			_ = os.Remove(pooled)
			return "", "", cerr
		}
	}
	_ = os.Chmod(pooled, 0o755)
	info, err := os.Stat(pooled)
	if err != nil {
		return "", "", err
	}
	sum, _ := ps2FileSHA256(pooled)
	meta := map[string]string{
		"source":     pick.Core.URL,
		"release":    pick.Release.Tag,
		"sha256":     sum,
		"index_date": date,
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
	}
	if encoded, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "source.json"), append(encoded, '\n'), 0o644)
	}
	pruneCorePool(pool, ps2CoreName, date, maxCorePoolBuilds)
	build := coreBuild{Core: ps2CoreName, Version: date, File: ps2CoreFile, Path: pooled, Pool: true, Size: info.Size()}
	active, _, err := activateCoreBuild(build)
	emuCRCForget(active)
	if err != nil {
		return "", "", err
	}
	if !coreKeepActivatedInPool() {
		if dropErr := coreDropPoolBuild(pool, build); dropErr != nil {
			logDebug("library", "PS2: kept the pool copy of the ARMSX2 core: %v", dropErr)
		}
	}
	// The catalog (and with it the games' choices), the compatibility panel
	// and the Core builds page all read the cores folder.
	invalidateEmulatorCache()
	invalidateCompatCache()
	coreBuildsRequestReload()
	if err := emuConfirmInstalledCore(ps2CoreName, build); err != nil {
		return "", "", err
	}
	found := ps2FindCore()
	if found == "" {
		return "", "", errors.New("the ARMSX2 core is in, but there is no RetroArch beside it to start it")
	}
	st, err := os.Stat(found)
	if err != nil {
		return "", "", err
	}
	ps2RecordUpdate(func(r *ps2Record) {
		r.Release = pick.Release.Tag
		r.CorePath = filepath.Clean(found)
		r.CoreSize = st.Size()
		r.CoreTime = st.ModTime().Unix()
		r.Refused = slices.DeleteFunc(r.Refused, func(tag string) bool { return tag == pick.Release.Tag })
	})
	logInfo("library", "PS2: ARMSX2 core %s installed at %s", pick.Release.Tag, found)
	return found, resStage, nil
}

// ps2FetchResources downloads pick's resources archive into work and returns
// the resources folder unpacked from it.
func ps2FetchResources(ctx context.Context, client *http.Client, pick ps2Pick, work string, phase func(phase, detail string), progress func(done, total int64)) (string, error) {
	phase(installPhaseDownload, "ARMSX2's resources ("+pick.Release.Tag+")")
	archive := filepath.Join(work, path.Base(pick.Resources.Name))
	if err := ps2Download(ctx, client, pick.Resources, archive, progress); err != nil {
		return "", fmt.Errorf("ARMSX2's resources: %w", err)
	}
	phase(installPhaseExtract, "ARMSX2's resources ("+pick.Release.Tag+")")
	_, res, err := ps2Unpack(ctx, archive, filepath.Join(work, "resources"), false)
	_ = os.Remove(archive)
	if err != nil {
		return "", fmt.Errorf("ARMSX2's resources: %w", err)
	}
	if res == "" {
		return "", fmt.Errorf("%s has no resources folder (GameIndex.yaml)", pick.Resources.Name)
	}
	return res, nil
}

// ps2SwapIn puts the folder at src in place of dest (the same card: two
// renames), and removes what dest held. A swap that fails puts dest back.
func ps2SwapIn(src, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// What an earlier swap could not remove; this one's own name is new.
	if leftovers, err := filepath.Glob(filepath.Join(parent, ps2WorkName+"-old-*")); err == nil {
		for _, l := range leftovers {
			_ = os.RemoveAll(l)
		}
	}
	old := filepath.Join(parent, fmt.Sprintf("%s-old-%d", ps2WorkName, time.Now().UnixNano()))
	had := false
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return err
		}
		had = true
	}
	if err := os.Rename(src, dest); err != nil {
		if had {
			_ = os.Rename(old, dest)
		}
		return err
	}
	syncDirectory(parent)
	_ = os.RemoveAll(old)
	return nil
}

// ps2Download fetches asset to dest (through dest.part), checking the size
// and the SHA-256 GitHub states for it.
func ps2Download(ctx context.Context, client *http.Client, asset ps2Asset, dest string, progress func(done, total int64)) error {
	if err := emulatorUpdateValidateURL(asset.URL); err != nil {
		return err
	}
	if asset.Size > ps2DownloadMax {
		return fmt.Errorf("%s is larger than expected", asset.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, asset.URL)
	}
	if resp.ContentLength > ps2DownloadMax {
		return fmt.Errorf("%s is larger than expected", asset.Name)
	}
	part := dest + ".part"
	defer os.Remove(part)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	if total <= 0 {
		total = asset.Size
	}
	sha := sha256.New()
	var body io.Reader = io.LimitReader(resp.Body, ps2DownloadMax+1)
	if progress != nil {
		body = &progressReader{r: body, total: total, fn: progress}
	}
	n, copyErr := io.Copy(io.MultiWriter(out, sha), body)
	syncErr := out.Sync()
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	case n > ps2DownloadMax:
		return fmt.Errorf("%s is larger than expected", asset.Name)
	case asset.Size > 0 && n != asset.Size:
		return fmt.Errorf("%s ended after %d of %d bytes — try again", asset.Name, n, asset.Size)
	}
	if want, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(asset.Digest)), "sha256:"); ok && want != "" {
		if got := hex.EncodeToString(sha.Sum(nil)); got != want {
			return fmt.Errorf("%s does not match the checksum GitHub states for it — try again", asset.Name)
		}
	}
	return os.Rename(part, dest)
}

// ps2FileSHA256 is a file's SHA-256 in hex.
func ps2FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
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

// ── Unpacking ────────────────────────────────────────────────────────────

// ps2OpenTar opens a downloaded archive as a tar stream, by its name.
func ps2OpenTar(archive string) (*tar.Reader, func(), error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, nil, err
	}
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".tar.zst") || strings.HasSuffix(lower, ".tzst"):
		zr, err := zstd.NewReader(f, zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return tar.NewReader(zr), func() { zr.Close(); f.Close() }, nil
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return tar.NewReader(gz), func() { gz.Close(); f.Close() }, nil
	case strings.HasSuffix(lower, ".tar"):
		return tar.NewReader(f), func() { f.Close() }, nil
	}
	f.Close()
	return nil, nil, fmt.Errorf("%s is not an archive JukaHub unpacks", filepath.Base(archive))
}

// ps2CleanMember is an archive member's path made relative and safe ("" for
// one that leaves the archive's own tree: absolute, or through "..").
func ps2CleanMember(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") {
		return ""
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

// ps2Unpack reads archive once into stage: the core (when wantCore) as
// stage/armsx2_libretro.so, and every tree under a folder named "resources"
// as stage/res<N>. It returns the core file ("" when the archive has none) and
// the resources tree that holds GameIndex.yaml ("" when none does). Links and
// devices are skipped; sizes and counts are bounded.
func ps2Unpack(ctx context.Context, archive, stage string, wantCore bool) (string, string, error) {
	tr, closeTar, err := ps2OpenTar(archive)
	if err != nil {
		return "", "", err
	}
	defer closeTar()
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", "", err
	}
	bin := ""
	groups := map[string]string{} // the archive's resources folder → its stage folder
	var order []string
	var total int64
	files := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", err
		}
		files++
		if files > ps2UnpackFilesMax {
			return "", "", errors.New("the archive has more files than JukaHub unpacks")
		}
		rel := ps2CleanMember(h.Name)
		if rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		if wantCore && h.Typeflag == tar.TypeReg && parts[len(parts)-1] == ps2CoreFile && bin == "" {
			if h.Size <= 0 || h.Size > coreUpdateMaxBinary {
				return "", "", fmt.Errorf("%s in the archive has an unexpected size", ps2CoreFile)
			}
			total += h.Size
			dest := filepath.Join(stage, ps2CoreFile)
			if err := ps2WriteMember(tr, dest, h.Size); err != nil {
				return "", "", err
			}
			bin = dest
			continue
		}
		at := -1
		for i, p := range parts[:len(parts)-1] {
			if strings.EqualFold(p, "resources") {
				at = i
				break
			}
		}
		if at < 0 {
			if strings.EqualFold(parts[len(parts)-1], "resources") && h.Typeflag == tar.TypeDir {
				// The folder itself: its files follow.
				prefix := strings.Join(parts, "/")
				if _, ok := groups[prefix]; !ok {
					groups[prefix] = filepath.Join(stage, fmt.Sprintf("res%d", len(groups)))
					order = append(order, prefix)
				}
			}
			continue
		}
		prefix := strings.Join(parts[:at+1], "/")
		root, ok := groups[prefix]
		if !ok {
			root = filepath.Join(stage, fmt.Sprintf("res%d", len(groups)))
			groups[prefix] = root
			order = append(order, prefix)
		}
		dest := filepath.Join(root, filepath.FromSlash(strings.Join(parts[at+1:], "/")))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", "", err
			}
		case tar.TypeReg:
			if h.Size < 0 {
				return "", "", errors.New("the archive has a member of negative size")
			}
			total += h.Size
			if total > ps2UnpackMax {
				return "", "", errors.New("the archive unpacks to more than JukaHub takes")
			}
			if err := ps2WriteMember(tr, dest, h.Size); err != nil {
				return "", "", err
			}
		}
		if total > ps2UnpackMax {
			return "", "", errors.New("the archive unpacks to more than JukaHub takes")
		}
	}
	// The shallowest resources folder with the game index.
	sort.SliceStable(order, func(i, j int) bool { return strings.Count(order[i], "/") < strings.Count(order[j], "/") })
	res := ""
	for _, prefix := range order {
		if regularFile(filepath.Join(groups[prefix], "GameIndex.yaml")) {
			res = groups[prefix]
			break
		}
	}
	return bin, res, nil
}

// ps2WriteMember writes the current member (size bytes) to dest.
func ps2WriteMember(r io.Reader, dest string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(r, size))
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case closeErr != nil:
		return closeErr
	case n != size:
		return fmt.Errorf("%s ended early in the archive", filepath.Base(dest))
	}
	return nil
}
