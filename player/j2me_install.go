package main

// j2me_install.go — the two halves FreeJ2ME-Plus needs: its libretro core and
// the jar that core starts, and JukaHub's own build of the pair for the
// handheld.
//
// FreeJ2ME-Plus's core is a small C bridge. RetroArch loads it, and it starts
// `java -jar <RetroArch's system folder>/<name>-lr.jar` and shows what that
// Java program draws, over a pipe. The two halves only work as a pair built
// from one commit, and nobody publishes an aarch64 build, so JukaHub's CI
// builds both (player/native/build-cores.sh, build_j2me_jar) and publishes them
// in the rolling release "cores-aarch64" beside the other cores
// (.github/workflows/retroarch-cores.yml). The core has the index line every
// core has, "<date> <crc32> freej2me_plus_libretro.so.zip"; the jar has one of
// its own, "<date> <sha256> <jar> <core>", which the buildbot-format reader
// (parseCoreIndex) skips and j2meParseIndexFiles reads. A pair is the core's
// line and its jar's line of the same date.
//
// The install (the handheld only, coreFixSupported: the platform the release's
// builds are for) puts:
//   - the core through the Emulators page's path: fetchCoreUpdate (the index's
//     CRC32, the size cap, the load check against this firmware) and
//     activateCoreBuild (into the cores folder RetroArch loads from; a build it
//     replaces is kept as a .bak) — skipped when the core there is that build;
//   - the jar into the system folder RetroArch's config names
//     (gameDiagSystemDir), staged as a .part, checked against the index's
//     SHA-256 and renamed into place — skipped when the file there already has
//     that checksum;
//   - and last j2me/install.json in JukaHub's data folder: the pair as
//     installed. A start checks it without the network (j2meInspect): the
//     core's CRC32 (emuCachedCRC32, cached by size and time) and the jar's
//     SHA-256 (cached the same way here). Either half changed or gone, and the
//     next start installs the pair again.
//
// A FreeJ2ME core and jar the user put there by hand (the handheld, or a PC's
// RetroArch, where JukaHub downloads nothing) are used as they are: JukaHub
// cannot tell whether such halves belong together, and does not replace them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// j2meCoreName is the core JukaHub's release builds (FreeJ2ME-Plus's
	// devel name); j2meOldCoreName is the name older FreeJ2ME trees build,
	// accepted when the user brought one.
	j2meCoreName    = "freej2me_plus"
	j2meOldCoreName = "freej2me"
	// j2meMaxJar bounds the jar's download and checksum (it is a few MB).
	j2meMaxJar = 64 << 20
	// j2meInstallTimeout bounds one install: the index, a small core, the jar.
	j2meInstallTimeout = 10 * time.Minute
)

// errJ2MENotPublished is a release with no J2ME core in it (yet).
var errJ2MENotPublished = errors.New("JukaHub has not published its J2ME core for this device yet — try again in a few days")

// j2meIndexFile is one companion-file line of the release's index:
//
//	2026-11-23 <sha256> freej2me_plus-lr.jar freej2me_plus
type j2meIndexFile struct {
	Date   string
	SHA256 string
	File   string
	Core   string
}

var (
	j2meIndexFileRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*\.jar$`)
	j2meIndexCoreRe = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// j2meParseIndexFiles reads the companion-file lines of the release's index
// and skips everything else, the core lines included. A file name is a plain
// name (no folder, no ".."), since it becomes a file in RetroArch's system
// folder. The last line for a core's file wins, as for a core.
func j2meParseIndexFiles(text string) []j2meIndexFile {
	var out []j2meIndexFile
	at := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		date, sum, file, core := f[0], strings.ToLower(f[1]), f[2], f[3]
		if !validCoreIndexDate(date) || len(sum) != 64 || !isHex(sum) ||
			!j2meIndexFileRe.MatchString(file) || strings.Contains(file, "..") || !j2meIndexCoreRe.MatchString(core) {
			continue
		}
		entry := j2meIndexFile{Date: date, SHA256: sum, File: file, Core: core}
		key := core + "/" + file
		if i, ok := at[key]; ok {
			out[i] = entry
			continue
		}
		at[key] = len(out)
		out = append(out, entry)
	}
	return out
}

// j2meIndexPair is core's build in the index and the jar of the same run.
func j2meIndexPair(text, core string) (coreIndexEntry, j2meIndexFile, error) {
	entry, ok := coreFixEntry(parseCoreIndex(text), core)
	if !ok {
		return coreIndexEntry{}, j2meIndexFile{}, errJ2MENotPublished
	}
	for _, f := range j2meParseIndexFiles(text) {
		if f.Core == core && f.Date == entry.Date {
			return entry, f, nil
		}
	}
	return coreIndexEntry{}, j2meIndexFile{}, fmt.Errorf("JukaHub's %s J2ME core has no jar of the same date in its release — try again later", entry.Date)
}

// ── What is on the device ────────────────────────────────────────────────

// j2meInstalled is j2me/install.json: the pair JukaHub installed last.
type j2meInstalled struct {
	Core     string `json:"core"`
	Date     string `json:"date"`
	CoreCRC  string `json:"core_crc32"`
	CorePath string `json:"core_path"`
	Jar      string `json:"jar"`
	JarSHA   string `json:"jar_sha256"`
	JarPath  string `json:"jar_path"`
}

func j2meInstalledPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "j2me", "install.json")
}

// j2meLoadInstalled reads the record; false when there is none or it is bad.
func j2meLoadInstalled() (j2meInstalled, bool) {
	path := j2meInstalledPath()
	if path == "" {
		return j2meInstalled{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64<<10 {
		return j2meInstalled{}, false
	}
	var rec j2meInstalled
	if json.Unmarshal(data, &rec) != nil || rec.CorePath == "" || rec.CoreCRC == "" ||
		!j2meIndexFileRe.MatchString(rec.Jar) || len(rec.JarSHA) != 64 {
		return j2meInstalled{}, false
	}
	return rec, true
}

func j2meSaveInstalled(rec j2meInstalled) error {
	path := j2meInstalledPath()
	if path == "" {
		return errors.New("JukaHub has no data folder")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// j2meSums caches a jar's SHA-256 by path, size and time.
var j2meSums struct {
	mu sync.Mutex
	m  map[string]j2meSum
}

type j2meSum struct {
	size int64
	mod  time.Time
	sum  string
}

// j2meFileSHA256 is the file's SHA-256, read again only when it changed.
func j2meFileSHA256(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > j2meMaxJar {
		return "", fmt.Errorf("%s is not a jar JukaHub installed", filepath.Base(path))
	}
	j2meSums.mu.Lock()
	if hit, ok := j2meSums.m[path]; ok && hit.size == info.Size() && hit.mod.Equal(info.ModTime()) {
		j2meSums.mu.Unlock()
		return hit.sum, nil
	}
	j2meSums.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, j2meMaxJar+1)); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	j2meSums.mu.Lock()
	if j2meSums.m == nil {
		j2meSums.m = map[string]j2meSum{}
	}
	j2meSums.m[path] = j2meSum{size: info.Size(), mod: info.ModTime(), sum: sum}
	j2meSums.mu.Unlock()
	return sum, nil
}

// j2meJarNames are the jar names a FreeJ2ME core may start, the likeliest
// first: FreeJ2ME-Plus's devel tree renamed its jar with its core.
func j2meJarNames(core string) []string {
	if core == j2meCoreName {
		return []string{"freej2me_plus-lr.jar", "freej2me-lr.jar"}
	}
	return []string{"freej2me-lr.jar"}
}

// j2meCoreHasRetroArch reports whether a RetroArch that can load the core
// file sits where the library's launch looks for it (gameLaunchCommandArgs).
func j2meCoreHasRetroArch(core string) bool {
	switch strings.ToLower(filepath.Ext(core)) {
	case ".dll":
		return IsWindows() && emuRetroArchForCore(core) != ""
	case ".so":
		return !IsWindows() && coreRetroArchBinary(filepath.Dir(core)) != ""
	}
	return false
}

// j2meFindCore is the FreeJ2ME core a J2ME game starts in: JukaHub's name
// first, then the older one, each only with a RetroArch to start it.
func j2meFindCore() (path, name string) {
	dirs := retroArchCoreDirs()
	for _, n := range []string{j2meCoreName, j2meOldCoreName} {
		if p := coreActiveBinary(n, dirs); p != "" && j2meCoreHasRetroArch(p) {
			return p, n
		}
	}
	return "", ""
}

// j2meSetup is what a J2ME game would start in here (j2meInspect).
type j2meSetup struct {
	Core   string // the core file ("" when there is none)
	Jar    string // its jar in RetroArch's system folder ("" when missing)
	SysDir string // that system folder
	Ready  bool   // both halves are there (JukaHub's pair: unchanged since)
	Fetch  bool   // JukaHub installs (or repairs) its pair here
	Why    string // neither: what is missing and what to do
}

// j2meInspect looks at the core, the jar and the record for a game at
// romPath. It reads files (the jar's checksum the first time), so never on the
// main thread.
func j2meInspect(romPath string) j2meSetup {
	var s j2meSetup
	fetch := coreFixSupported()
	core, name := j2meFindCore()
	if core == "" {
		if fetch {
			s.Fetch = true
			return s
		}
		s.Why = j2meNoCoreHelp()
		return s
	}
	s.Core = core
	s.SysDir, _, _ = gameDiagSystemDir(core, romPath)
	if IsWindows() && strings.Contains(s.SysDir, " ") {
		// Some FreeJ2ME builds pass the jar's path to java unquoted.
		logWarn("library", "J2ME: RetroArch's system folder %q has a space in its path; FreeJ2ME may not find its jar there", s.SysDir)
	}
	if rec, ok := j2meLoadInstalled(); ok && j2meSamePath(rec.CorePath, core) {
		// JukaHub's pair: both halves as it installed them, else again.
		jar := filepath.Join(s.SysDir, rec.Jar)
		crc, crcErr := emuCachedCRC32(core)
		sum, sumErr := j2meFileSHA256(jar)
		if crcErr == nil && strings.EqualFold(crc, rec.CoreCRC) && sumErr == nil && sum == rec.JarSHA {
			s.Jar, s.Ready = jar, true
			return s
		}
		if fetch {
			logInfo("library", "J2ME: the installed pair changed (core %s → %s, jar %v); installing it again", rec.CoreCRC, crc, sumErr)
			s.Fetch = true
			return s
		}
	}
	for _, jar := range j2meJarNames(name) {
		if p := filepath.Join(s.SysDir, jar); regularFile(p) {
			s.Jar, s.Ready = p, true
			return s
		}
	}
	if fetch {
		s.Fetch = true
		return s
	}
	s.Why = "The FreeJ2ME core is in RetroArch, but its jar is not: put " + j2meJarNames(name)[0] +
		" from the same FreeJ2ME download into RetroArch's system folder (" + s.SysDir + "), then start the game again"
	return s
}

// j2meSamePath compares two file paths the way the platform does.
func j2meSamePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if IsWindows() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// j2meNoCoreHelp says how a computer's RetroArch gets FreeJ2ME (JukaHub
// downloads it only on the handheld).
func j2meNoCoreHelp() string {
	return "J2ME games need RetroArch's FreeJ2ME-Plus core and its jar: download both from github.com/TASEmulators/freej2me-plus (Releases), " +
		"put the core in RetroArch's cores folder and the -lr.jar in its system folder, then start the game again (docs/J2ME.md)"
}

// ── Installing JukaHub's pair ────────────────────────────────────────────

// j2meInstallPair installs (or repairs) JukaHub's pair for a game at romPath
// and returns the core file. Off the main thread; the caller holds the patch
// slot. phase names each step; progress gets the jar's bytes.
func j2meInstallPair(ctx context.Context, romPath string, phase func(string), progress func(done, total int64)) (string, error) {
	client := newEmulatorUpdateClient()
	phase(installPhaseResolving)
	data, err := fetchUpdateBytes(ctx, client, coreFixIndexURL, patchMaxMetadata)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", errJ2MENotPublished
		}
		return "", fmt.Errorf("JukaHub's builds for this device: %w", err)
	}
	entry, jar, err := j2meIndexPair(string(data), j2meCoreName)
	if err != nil {
		return "", err
	}
	if reason, refused := coreBuildRefused(j2meCoreName, entry.CRC32); refused {
		return "", &coreRefusedError{Core: j2meCoreName, Build: entry.Date + " (JukaHub's)", Reason: "was refused here before: " + reason}
	}

	// The core, unless the one there is this build.
	core := coreActiveBinary(j2meCoreName, retroArchCoreDirs())
	same := false
	if core != "" {
		if sum, err := emuCachedCRC32(core); err == nil && strings.EqualFold(sum, entry.CRC32) {
			same = true
		}
	}
	if !same {
		phase(installPhaseDownload)
		pool := compatCorePoolDir()
		build, err := fetchCoreUpdate(ctx, client, coreUpdate{
			Core:   j2meCoreName,
			Date:   entry.Date,
			CRC32:  entry.CRC32,
			URL:    coreFixBaseURL + entry.File,
			Active: core,
		}, pool)
		if err != nil {
			return "", err
		}
		phase(installPhaseExtract)
		target, _, err := activateCoreBuild(build)
		emuCRCForget(target)
		if err != nil {
			return "", err
		}
		if !coreKeepActivatedInPool() {
			if dropErr := coreDropPoolBuild(pool, build); dropErr != nil {
				logDebug("library", "J2ME: kept the pool copy of the core: %v", dropErr)
			}
		}
		invalidateEmulatorCache()
		invalidateCompatCache()
		coreBuildsRequestReload()
		core = target
	}
	if coreRetroArchBinary(filepath.Dir(core)) == "" {
		return "", fmt.Errorf("there is no RetroArch beside %s to start the J2ME core", filepath.Dir(core))
	}

	// The jar, into the system folder that RetroArch reads.
	sysDir, _, _ := gameDiagSystemDir(core, romPath)
	if err := j2meCheckSysDir(sysDir, romPath); err != nil {
		return "", err
	}
	dest := filepath.Join(sysDir, jar.File)
	if sum, err := j2meFileSHA256(dest); err != nil || sum != jar.SHA256 {
		phase(installPhaseDownload)
		if err := j2meDownloadJar(ctx, client, coreFixBaseURL+jar.File, dest, jar.SHA256, progress); err != nil {
			return "", err
		}
	}

	// The record, last: the pair as it is now.
	phase(installPhaseVerify)
	crc, err := emuCachedCRC32(core)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(crc, entry.CRC32) {
		return "", fmt.Errorf("the J2ME core in %s is not the build that was just installed", filepath.Dir(core))
	}
	rec := j2meInstalled{
		Core:     j2meCoreName,
		Date:     entry.Date,
		CoreCRC:  crc,
		CorePath: core,
		Jar:      jar.File,
		JarSHA:   jar.SHA256,
		JarPath:  dest,
	}
	if err := j2meSaveInstalled(rec); err != nil {
		return "", fmt.Errorf("could not record the J2ME install: %w", err)
	}
	logInfo("library", "J2ME: JukaHub's %s pair is in: %s (crc %s) and %s (sha256 %s)", entry.Date, core, crc, dest, jar.SHA256)
	return core, nil
}

// j2meCheckSysDir makes sure the jar has a folder of its own to go in.
// RetroArch set to "default" reads firmware from each game's own folder; a jar
// there would be listed as a game, so the player is told to set one instead.
func j2meCheckSysDir(sysDir, romPath string) error {
	if strings.TrimSpace(sysDir) == "" {
		return errors.New("RetroArch's system folder could not be found")
	}
	if strings.TrimSpace(romPath) != "" && filepath.Clean(sysDir) == filepath.Clean(filepath.Dir(romPath)) {
		return errors.New("RetroArch reads firmware from each game's own folder: set its System/BIOS folder (RetroArch → Settings → Directory) and start the game again")
	}
	return os.MkdirAll(sysDir, 0o755)
}

// j2meDownloadJar fetches the jar to dest: into dest.part, checked against
// want, then renamed over dest.
func j2meDownloadJar(ctx context.Context, client *http.Client, raw, dest, want string, progress func(done, total int64)) error {
	if err := emulatorUpdateValidateURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
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
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	if resp.ContentLength > j2meMaxJar {
		return errors.New("the J2ME jar is larger than expected")
	}
	part := dest + ".part"
	defer os.Remove(part)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	var body io.Reader = io.LimitReader(resp.Body, j2meMaxJar+1)
	if progress != nil {
		body = &progressReader{r: body, total: resp.ContentLength, fn: progress}
	}
	n, copyErr := io.Copy(io.MultiWriter(out, hash), body)
	syncErr := out.Sync()
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	case n > j2meMaxJar:
		return errors.New("the J2ME jar is larger than expected")
	case hex.EncodeToString(hash.Sum(nil)) != want:
		return errors.New("the J2ME jar arrived damaged (checksum mismatch) — try again")
	}
	return os.Rename(part, dest)
}
