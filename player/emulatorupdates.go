package main

// emulatorupdates.go — checking the emulators JukaHub is allowed to update.
//
// Two different things live here, because the hardware forces the distinction:
//
//   - libretro cores. The buildbot publishes one index and one zip per core for
//     each platform: aarch64 shared objects for the handheld, x86_64 DLLs for a
//     portable RetroArch on Windows (emuCoreIndexURL picks the channel). JukaHub
//     compares the CRC32 the index states with the CRC32 of the installed binary
//     and, when they differ, installs the newer build into the existing core
//     pool (data/performance/cores/<core>/<date>/). This pass only fills the
//     pool; runInstalledCoreUpdates is what makes a build active, and it keeps
//     the binary it replaces as a .bak (pruned to maxCoreBackups). The Emulators
//     page does that on a press, and startCoreAutoUpdate does it by itself once a
//     day on the handheld — see emulatorautoupdate.go.
//   - standalone emulators. Windows installs them for real, from their GitHub
//     releases (emuwininstall.go). The handheld used to look up PPSSPP, mGBA and
//     Flycast here and "stage" an aarch64 archive in data/emulator-updates/, but
//     none of the three publishes a build for this firmware — upstream PPSSPP
//     and mGBA release no aarch64 Linux build at all, and a desktop AppImage
//     could not start under the firmware's launchers anyway. The stage never
//     received a file, and the page's toast still read as though an emulator
//     had been updated, so the lookup is gone. Where a launcher runs the
//     libretro core (mgba, flycast, ppsspp) the core pass keeps it current; the
//     standalone PPSSPP the PSP launchers start is managed for real elsewhere:
//     JukaHub installs and updates its own build under the same Emulator
//     updates setting.
//
// The index is a plain listing, not a signed document: its CRC is a corruption
// check, not proof of authorship. An activation — on a press or automatic — is
// therefore always reversible: the replaced binary is kept beside the core and
// the Emulators page restores it in one press. And a build is checked before it
// is kept at all: one that cannot load on this firmware (a nightly built against
// a newer glibc than the handheld has — elfcompat.go) is refused before it
// reaches the pool, remembered by its checksum so the daily pass does not fetch
// it again, and the core that works stays.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// retroArchCoreIndexURL is the ARM64 buildbot index RetroArch's own core
	// updater reads. There is no stable aarch64 tree, so this is the nightly
	// channel; a build can be newer and still be worse than what is installed,
	// which is why every activation keeps the build it replaced.
	retroArchCoreIndexURL = "https://buildbot.libretro.com/nightly/linux/aarch64/latest/.index-extended"
	retroArchCoreBaseURL  = "https://buildbot.libretro.com/nightly/linux/aarch64/latest/"
	// retroArchWinCoreIndexURL is the same listing for the x86_64 DLLs a
	// portable Windows RetroArch loads: RetroArch's own Windows core updater
	// reads this tree.
	retroArchWinCoreIndexURL = "https://buildbot.libretro.com/nightly/windows/x86_64/latest/.index-extended"
	retroArchWinCoreBaseURL  = "https://buildbot.libretro.com/nightly/windows/x86_64/latest/"
	// Archives and extracted cores are streamed to the card. These limits
	// bound a corrupt response or zip bomb without excluding published large
	// cores such as MAME (its ARM64 archive exceeds the old 96 MB limit).
	coreUpdateMaxDownload = 512 << 20
	coreUpdateMaxBinary   = 1 << 30
	standaloneMaxDownload = 256 << 20
)

// The URLs are variables so tests can point the checker at a local server. An
// empty index or base URL means "this platform's channel" (emuCoreIndexURL).
var (
	coreUpdateIndexURL = ""
	coreUpdateBaseURL  = ""
	standaloneAPIBase  = "https://api.github.com"
	// emulatorUpdateValidateURL is the host policy for everything this file
	// downloads. It defaults to the app's single allowlist and is a variable so
	// tests can point at a loopback server.
	emulatorUpdateValidateURL = validatePatchURL
)

// emuCoreIndexURL is the build index this platform's RetroArch updates from:
// aarch64 shared objects on the handheld, x86_64 DLLs on Windows. A test
// override wins.
func emuCoreIndexURL() string {
	if coreUpdateIndexURL != "" {
		return coreUpdateIndexURL
	}
	if IsWindows() {
		return retroArchWinCoreIndexURL
	}
	return retroArchCoreIndexURL
}

// emuCoreBaseURL is the folder the index's archives are served from.
func emuCoreBaseURL() string {
	if coreUpdateBaseURL != "" {
		return coreUpdateBaseURL
	}
	if IsWindows() {
		return retroArchWinCoreBaseURL
	}
	return retroArchCoreBaseURL
}

// fetchUpdateBytes is fetchBounded's counterpart for the buildbot: same size
// limit and headers, but it validates against the emulator-update policy rather
// than assuming the URL was already checked by the caller.
func fetchUpdateBytes(ctx context.Context, client *http.Client, raw string, maxBytes int64) ([]byte, error) {
	if err := emulatorUpdateValidateURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, &updateTooLargeError{limit: maxBytes}
	}
	return data, nil
}

// updateTooLargeError is fetchUpdateBytes' refusal of a response over its
// limit. It is a type so the core pass can tell "this build will never fit"
// (remembered, not retried daily) from a download that merely failed.
type updateTooLargeError struct{ limit int64 }

func (e *updateTooLargeError) Error() string {
	return fmt.Sprintf("response exceeds %d byte limit", e.limit)
}

// coreIndexEntry is one line of the buildbot index:
//
//	2026-10-23 16647efd mednafen_supergrafx_libretro.so.zip
type coreIndexEntry struct {
	Date  string
	CRC32 string
	File  string
	Core  string
}

// coreUpdate is an installed core the index has a different build of.
type coreUpdate struct {
	Core    string
	Date    string
	CRC32   string
	URL     string
	Current string
	// Active is the binary the build would replace ("" for a first install),
	// so the download can be refused when it is not the same architecture.
	Active string
}

// parseCoreIndex reads the buildbot index. It skips anything that is not a
// "<date> <crc32> <core>_libretro.so.zip" line (or the Windows tree's
// "<core>_libretro.dll.zip") rather than failing the whole check over one odd
// entry, and the last line for a core wins.
func parseCoreIndex(text string) []coreIndexEntry {
	byCore := make(map[string]coreIndexEntry)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		// "<date> <crc32> <file>" is the format RetroArch's own core updater
		// reads. A fourth column (a size) must not empty the whole index, and
		// where it sits is not documented: the file is whichever of the last
		// two ends in ".zip", and the other must be a plain number.
		if len(fields) != 3 && len(fields) != 4 {
			continue
		}
		date, crc, file := fields[0], strings.ToLower(fields[1]), fields[2]
		if len(fields) == 4 {
			size := fields[3]
			if !strings.HasSuffix(file, ".zip") {
				file, size = fields[3], fields[2]
			}
			if !strings.HasSuffix(file, ".zip") || size == "" || strings.Trim(size, "0123456789") != "" {
				continue
			}
		}
		// The date is not decoration: a fetched core is filed under
		// <pool>/<core>/<date>/, so it has to be a real calendar date. The old
		// length check accepted any ten characters, and "../../../.." is ten.
		if !validCoreIndexDate(date) || len(crc) != 8 || !isHex(crc) {
			continue
		}
		core := coreIndexFileCore(file)
		if core == "" || strings.ContainsAny(core, "/\\") {
			continue
		}
		byCore[core] = coreIndexEntry{Date: date, CRC32: crc, File: file, Core: core}
	}
	out := make([]coreIndexEntry, 0, len(byCore))
	for _, entry := range byCore {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Core < out[j].Core })
	return out
}

// coreIndexFileCore is the core an index archive name carries, or "" when the
// name is not a core archive: "mgba_libretro.so.zip" (the handheld tree) and
// "mgba_libretro.dll.zip" (the Windows tree) are both "mgba".
func coreIndexFileCore(file string) string {
	if !strings.HasSuffix(file, ".zip") {
		return ""
	}
	binary := strings.TrimSuffix(file, ".zip")
	for _, suffix := range []string{"_libretro.so", "_libretro.dll"} {
		if strings.HasSuffix(binary, suffix) {
			return strings.TrimSuffix(binary, suffix)
		}
	}
	return ""
}

// coreUpdateBinary is the file name a core archive must hold: the name the
// index published it under (…_libretro.so on the handheld, …_libretro.dll on
// Windows), or the shared-object name when the URL does not say.
func coreUpdateBinary(upd coreUpdate) string {
	name := upd.URL
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ".zip")
	if name == upd.Core+"_libretro.so" || name == upd.Core+"_libretro.dll" {
		return name
	}
	return upd.Core + "_libretro.so"
}

// validCoreIndexDate reports whether a buildbot index date is a real
// YYYY-MM-DD day. Only a value that parses is used as a folder name.
func validCoreIndexDate(date string) bool {
	if len(date) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", date)
	return err == nil && parsed.Format("2006-01-02") == date
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// coreCRC32File is the checksum the buildbot index states for a core.
func coreCRC32File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x", h.Sum32()), nil
}

// planCoreUpdates returns the installed cores whose build differs from the one
// the index lists, in core-name order. A core the index does not carry (a
// custom build, a Windows .dll) is left alone, and so is one on the firmware's
// read-only system partition (coreDirIsSystem): its update could only fail.
//
// "Differs" is all the CRC32 can say. On CrossMix, whose image ships its own
// builds of many cores, that means a shipped core is replaced by the buildbot's
// nightly the first time the pass runs — the kept .bak and the one-press
// rollback are what make that acceptable, and a nightly that cannot load here is
// refused before it replaces anything.
func planCoreUpdates(entries []coreIndexEntry, active map[string]coreBuild) []coreUpdate {
	updates := make([]coreUpdate, 0, len(entries))
	for _, entry := range entries {
		build, ok := active[entry.Core]
		if !ok || build.Path == "" || coreDirIsSystem(filepath.Dir(build.Path)) {
			continue
		}
		// The checksum cache is keyed by size and modification time, so a core
		// read by the last check (or the Core builds page) is not read again.
		sum, err := emuCachedCRC32(build.Path)
		if err != nil || sum == entry.CRC32 {
			continue
		}
		updates = append(updates, coreUpdate{
			Core:    entry.Core,
			Date:    entry.Date,
			CRC32:   entry.CRC32,
			URL:     emuCoreBaseURL() + entry.File,
			Current: build.Version,
			Active:  build.Path,
		})
	}
	return updates
}

// fetchCoreIndex downloads and parses the buildbot index.
func fetchCoreIndex(ctx context.Context, client *http.Client) ([]coreIndexEntry, error) {
	data, err := fetchUpdateBytes(ctx, client, emuCoreIndexURL(), patchMaxMetadata)
	if err != nil {
		return nil, err
	}
	entries := parseCoreIndex(string(data))
	if len(entries) == 0 {
		return nil, errors.New("the core index is empty")
	}
	return entries, nil
}

// fetchCoreUpdate downloads one core zip to the card, checks the CRC32 the
// index promised, and writes the binary into the pool. Neither the archive nor
// its extracted binary needs to fit in RAM. A pool build is inert until
// something activates it: the Emulators page does, and so does the automatic
// pass.
//
// Two outcomes are refusals rather than failures (coreRefusedError): a build
// larger than the download limit, and one that cannot load on this firmware.
// Neither changes by trying again, so the build is remembered by its checksum
// (coreBuildRefused) and the daily pass does not download it every day; the
// next build the index lists gets its own check.
func fetchCoreUpdate(ctx context.Context, client *http.Client, upd coreUpdate, poolRoot string) (coreBuild, error) {
	if err := emulatorUpdateValidateURL(upd.URL); err != nil {
		return coreBuild{}, err
	}
	if upd.Core == "" || strings.ContainsAny(upd.Core, "/\\") || upd.Core == "." || upd.Core == ".." || !validCoreIndexDate(upd.Date) {
		return coreBuild{}, errors.New("the core index names an invalid core or date")
	}
	if err := os.MkdirAll(poolRoot, 0o755); err != nil {
		return coreBuild{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upd.URL, nil)
	if err != nil {
		return coreBuild{}, err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return coreBuild{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return coreBuild{}, fmt.Errorf("HTTP %d from %s", resp.StatusCode, upd.URL)
	}
	if resp.ContentLength > coreUpdateMaxDownload {
		return coreBuild{}, coreRefuse(upd, coreTooLargeReason(coreUpdateMaxDownload))
	}
	archive, err := os.CreateTemp(poolRoot, ".core-*.zip.part")
	if err != nil {
		return coreBuild{}, err
	}
	defer func() {
		archive.Close()
		_ = os.Remove(archive.Name())
	}()
	archiveSize, err := io.Copy(archive, io.LimitReader(resp.Body, coreUpdateMaxDownload+1))
	if err != nil {
		return coreBuild{}, err
	}
	if archiveSize > coreUpdateMaxDownload {
		return coreBuild{}, coreRefuse(upd, coreTooLargeReason(coreUpdateMaxDownload))
	}
	zr, err := zip.NewReader(archive, archiveSize)
	if err != nil {
		return coreBuild{}, fmt.Errorf("core archive is not a zip: %w", err)
	}
	// The archive holds the binary under the name the index published: a
	// shared object from the handheld tree, a DLL from the Windows tree.
	want := coreUpdateBinary(upd)
	var selected *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Base(f.Name), want) {
			continue
		}
		selected = f
		break
	}
	if selected == nil || selected.UncompressedSize64 == 0 {
		return coreBuild{}, fmt.Errorf("%s does not contain %s", upd.URL, want)
	}
	if selected.UncompressedSize64 > coreUpdateMaxBinary {
		return coreBuild{}, coreRefuse(upd, coreTooLargeReason(coreUpdateMaxBinary))
	}
	binary, err := os.CreateTemp(poolRoot, ".core-*.so.part")
	if err != nil {
		return coreBuild{}, err
	}
	defer func() {
		binary.Close()
		_ = os.Remove(binary.Name())
	}()
	rc, err := selected.Open()
	if err != nil {
		return coreBuild{}, err
	}
	crc := crc32.NewIEEE()
	sha := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(binary, crc, sha), io.LimitReader(rc, coreUpdateMaxBinary+1))
	closeErr := rc.Close()
	if copyErr != nil {
		return coreBuild{}, copyErr
	}
	if closeErr != nil {
		return coreBuild{}, closeErr
	}
	if size > coreUpdateMaxBinary {
		return coreBuild{}, coreRefuse(upd, coreTooLargeReason(coreUpdateMaxBinary))
	}
	if got := fmt.Sprintf("%08x", crc.Sum32()); got != upd.CRC32 {
		return coreBuild{}, fmt.Errorf("core checksum mismatch (index %s, download %s)", upd.CRC32, got)
	}
	if err := binary.Sync(); err != nil {
		return coreBuild{}, err
	}
	// The bytes are the build the index promised; whether this firmware can
	// load them is a separate question, asked before the pool is changed.
	if problem := coreLoadProblemFile(binary.Name(), upd.Active); problem != "" {
		return coreBuild{}, coreRefuse(upd, coreCannotLoadReason+problem)
	}

	dir := filepath.Join(poolRoot, upd.Core, upd.Date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return coreBuild{}, err
	}
	path := filepath.Join(dir, want)
	if err := binary.Chmod(0o755); err != nil {
		return coreBuild{}, err
	}
	if err := binary.Close(); err != nil {
		return coreBuild{}, err
	}
	if err := os.Rename(binary.Name(), path); err != nil {
		return coreBuild{}, err
	}
	// Provenance travels with the build: the pool is a folder a user can copy
	// to another card, and the URL is the only record of where it came from.
	// The buildbot states a CRC32 only; the SHA-256 of what was installed is
	// recorded so a later check can tell this file from a changed one.
	meta := map[string]string{
		"source":     upd.URL,
		"crc32":      upd.CRC32,
		"sha256":     hex.EncodeToString(sha.Sum(nil)),
		"index_date": upd.Date,
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
	}
	if encoded, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "source.json"), append(encoded, '\n'), 0o644)
	}
	// The pool is a build cache, not an archive: with the handheld updating its
	// cores by itself a new dated folder per core per day would be the fastest
	// growing thing on the card. The build just written is never a candidate.
	pruneCorePool(poolRoot, upd.Core, upd.Date, maxCorePoolBuilds)
	// A build refused before (on older firmware, say) that passed now is no
	// longer one to skip.
	coreRefusalForget(upd.Core, upd.CRC32)
	return coreBuild{
		Core:    upd.Core,
		Version: upd.Date,
		File:    want,
		Path:    path,
		Pool:    true,
		Size:    size,
	}, nil
}

// coreRefusedError is a core build this device will not take: one that cannot
// load on this firmware, or one larger than the download limit. The core that
// is installed stays as it is.
type coreRefusedError struct {
	Core string
	// Build names the build — the index date, or a kept build's time.
	Build  string
	Reason string
}

func (e *coreRefusedError) Error() string {
	build := "this build"
	if strings.TrimSpace(e.Build) != "" {
		build = "the " + e.Build + " build"
	}
	return build + " " + e.Reason
}

// coreCannotLoadReason prefixes a load problem from elfcompat.go, so every
// refusal of that kind reads "… cannot load on this firmware: needs …".
const coreCannotLoadReason = "cannot load on this firmware: "

// coreTooLargeReason is the refusal of a corrupt or unreasonably large archive
// or extracted binary.
func coreTooLargeReason(limit int64) string {
	return fmt.Sprintf("is larger than the %d MB a core may be", limit>>20)
}

// coreRefuse remembers that upd's build was refused and returns the error that
// says why.
func coreRefuse(upd coreUpdate, reason string) error {
	coreRefusalRecord(upd.Core, upd.CRC32, upd.Date, reason)
	logWarn("emulators", "core %s %s (crc %s) refused: %s", upd.Core, upd.Date, upd.CRC32, reason)
	return &coreRefusedError{Core: upd.Core, Build: upd.Date, Reason: reason}
}

// coreRefusal is one remembered refusal: the build, by the CRC32 the index
// states for it, and why it was refused.
type coreRefusal struct {
	CRC32  string `json:"crc32"`
	Build  string `json:"build,omitempty"`
	Reason string `json:"reason"`
	At     string `json:"at,omitempty"`
}

// coreRefusals holds one refusal per core — a newer refusal of the same core
// replaces the older one — in data/performance/core-refused.json, so a reboot
// does not mean downloading every refused build again. Only the exact build is
// skipped: the next one the index lists is fetched and checked like any other.
var coreRefusals struct {
	mu   sync.Mutex
	path string
	m    map[string]coreRefusal
}

func coreRefusalsPath() string { return filepath.Join(compatDir(), "core-refused.json") }

// coreRefusalsLoadLocked reads the file the first time, and again whenever the
// data folder is another one (a test pointing compatDir elsewhere).
func coreRefusalsLoadLocked() {
	path := coreRefusalsPath()
	if coreRefusals.m != nil && coreRefusals.path == path {
		return
	}
	coreRefusals.path = path
	coreRefusals.m = map[string]coreRefusal{}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return
	}
	loaded := map[string]coreRefusal{}
	if json.Unmarshal(data, &loaded) != nil {
		return
	}
	for core, r := range loaded {
		if core != "" && r.CRC32 != "" {
			coreRefusals.m[core] = r
		}
	}
}

func coreRefusalsSaveLocked() {
	data, err := json.MarshalIndent(coreRefusals.m, "", "  ")
	if err != nil {
		return
	}
	if err := writeCompatAtomic(coreRefusals.path, append(data, '\n'), 0o644); err != nil {
		logWarn("emulators", "could not save the refused core builds: %v", err)
	}
}

// coreBuildRefused reports whether this exact build of core (by its index
// CRC32) was refused before, and why.
func coreBuildRefused(core, crc string) (string, bool) {
	core = normalizeCoreName(core)
	crc = strings.ToLower(strings.TrimSpace(crc))
	if core == "" || crc == "" {
		return "", false
	}
	coreRefusals.mu.Lock()
	defer coreRefusals.mu.Unlock()
	coreRefusalsLoadLocked()
	r, ok := coreRefusals.m[core]
	if !ok || r.CRC32 != crc {
		return "", false
	}
	return r.Reason, true
}

// coreRefusalRecord remembers a refused build.
func coreRefusalRecord(core, crc, build, reason string) {
	core = normalizeCoreName(core)
	crc = strings.ToLower(strings.TrimSpace(crc))
	if core == "" || crc == "" {
		return
	}
	coreRefusals.mu.Lock()
	defer coreRefusals.mu.Unlock()
	coreRefusalsLoadLocked()
	coreRefusals.m[core] = coreRefusal{CRC32: crc, Build: build, Reason: reason, At: time.Now().UTC().Format(time.RFC3339)}
	coreRefusalsSaveLocked()
}

// coreRefusalForget drops core's refusal when it names this build.
func coreRefusalForget(core, crc string) {
	core = normalizeCoreName(core)
	crc = strings.ToLower(strings.TrimSpace(crc))
	coreRefusals.mu.Lock()
	defer coreRefusals.mu.Unlock()
	coreRefusalsLoadLocked()
	if r, ok := coreRefusals.m[core]; ok && r.CRC32 == crc {
		delete(coreRefusals.m, core)
		coreRefusalsSaveLocked()
	}
}

// maxCorePoolBuilds is how many fetched builds of one core stay in the pool.
// Five is enough to activate an older nightly from the compatibility panel after
// a bad one lands, and small enough that a core's cache cannot become the
// biggest thing on a 1 GB card.
const maxCorePoolBuilds = 5

// pruneCorePool keeps the newest keep date folders for one core under the pool
// and deletes the rest, returning what it removed. newest names the build that
// was just fetched, which is never removed even if the sort would place it late
// (a clock that jumped, or a build published with an older date).
func pruneCorePool(poolRoot, core, newest string, keep int) []string {
	if strings.TrimSpace(poolRoot) == "" || strings.TrimSpace(core) == "" {
		return nil
	}
	if keep < 1 {
		keep = 1
	}
	coreDir := filepath.Join(poolRoot, core)
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		return nil
	}
	dates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			dates = append(dates, entry.Name())
		}
	}
	// Date folders are YYYY-MM-DD, so a plain descending sort is newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	var removed []string
	for i, date := range dates {
		if date == newest || i < keep {
			continue
		}
		path := filepath.Join(coreDir, date)
		if err := os.RemoveAll(path); err != nil {
			logWarn("emulators", "could not prune pool build %q: %v", path, err)
			continue
		}
		removed = append(removed, path)
	}
	if len(removed) > 0 {
		logDebug("emulators", "pruned %d old pool build(s) of %s (keeping %d)", len(removed), core, keep)
	}
	return removed
}

// verifyReleaseDigest checks GitHub's sha256 for a downloaded asset. A release
// that publishes no digest is fetched anyway; the caller logs that it was not
// verified rather than pretending it was.
func verifyReleaseDigest(data []byte, digest string) error {
	d := strings.TrimSpace(digest)
	if d == "" {
		return nil
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(strings.ToLower(d), prefix) {
		return fmt.Errorf("unsupported release digest %q", digest)
	}
	sum := sha256.Sum256(data)
	want := strings.ToLower(strings.TrimSpace(d[len(prefix):]))
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("asset checksum mismatch (release %s, download %s)", want, got)
	}
	return nil
}

// emulatorUpdateReport is what one check-and-fetch pass did.
type emulatorUpdateReport struct {
	CoresChecked int
	Cores        []coreBuild
	CoreErrors   []string
	// Refused names the cores whose new build this device will not take — it
	// cannot load on this firmware, or it is over the download limit
	// (coreRefusedError). Their installed builds stay, and it is not a failure.
	Refused []string
}

// summary is the one-line result shown in the toast.
func (r emulatorUpdateReport) summary() string {
	parts := make([]string, 0, 3)
	if len(r.Cores) > 0 {
		parts = append(parts, pluralizeCount(len(r.Cores), "core", "cores")+" added to the pool")
	}
	if len(r.Refused) > 0 {
		parts = append(parts, strings.Join(r.Refused, ", ")+": new build not usable here, current kept")
	}
	if len(r.CoreErrors) > 0 {
		parts = append(parts, itoa(len(r.CoreErrors))+" failed")
	}
	if len(parts) == 0 {
		if r.CoresChecked == 0 {
			return "No RetroArch cores to check"
		}
		return "All " + itoa(r.CoresChecked) + " cores are up to date"
	}
	return strings.Join(parts, " · ")
}

// runEmulatorUpdates performs one pass over the cores: every installed core
// whose build differs from the index gets that build in the pool. It never
// touches an active core — activation is the separate press on the
// compatibility panel's core tab, or the Emulators page's update.
func runEmulatorUpdates(ctx context.Context, client *http.Client) emulatorUpdateReport {
	var report emulatorUpdateReport
	inv := scanCoreInventory()
	report.CoresChecked = len(inv.Active)

	installBarPhase(installPhaseResolving)
	entries, err := fetchCoreIndex(ctx, client)
	if err != nil {
		report.CoreErrors = append(report.CoreErrors, "core index: "+err.Error())
		return report
	}
	updates := planCoreUpdates(entries, inv.Active)
	pool := compatCorePoolDir()
	for i, upd := range updates {
		if err := ctx.Err(); err != nil {
			report.CoreErrors = append(report.CoreErrors, err.Error())
			break
		}
		installBarPhase(installPhaseDownload)
		installBarDetail(fmt.Sprintf("%s (%d/%d)", upd.Core, i+1, len(updates)))
		build, err := fetchCoreUpdate(ctx, client, upd, pool)
		if err != nil {
			var refused *coreRefusedError
			if errors.As(err, &refused) {
				report.Refused = append(report.Refused, coreConfigDisplayName(upd.Core))
				continue
			}
			report.CoreErrors = append(report.CoreErrors, upd.Core+": "+err.Error())
			continue
		}
		report.Cores = append(report.Cores, build)
	}
	return report
}

// newEmulatorUpdateClient is the patch client with a longer deadline: a core or
// emulator archive can exceed 100 MB, and the 25 s metadata timeout would
// cut a slow handheld download in half. Redirects are still checked against the
// same host policy.
func newEmulatorUpdateClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			return emulatorUpdateValidateURL(req.URL.String())
		},
	}
}

// startEmulatorUpdates runs the check off the event thread and reports through
// the install bar and a toast, the same way the other download actions do.
func startEmulatorUpdates() {
	if !IsOnline() {
		ShowToast("No internet connection — connect to check for emulator builds", ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	installBarBegin("Core builds")
	installBarPhase(installPhaseResolving)
	ShowToast("Checking the libretro buildbot for newer core builds…", ToastKindInfo)
	go func() {
		defer endPatchOp()
		// The timeout derives from the card's run token, so a cancel from the
		// card or the activity strip stops the transfer mid-flight.
		ctx, cancel := context.WithTimeout(installBarRunContext(), 15*time.Minute)
		defer cancel()
		report := runEmulatorUpdates(ctx, newEmulatorUpdateClient())
		installBarPhase(installPhaseDone)
		installBarDone()
		kind := ToastKindSuccess
		if len(report.CoreErrors) > 0 {
			kind = ToastKindWarning
		}
		PlaySuccessSound()
		ShowToast(report.summary(), kind)
	}()
}
