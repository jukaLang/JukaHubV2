package main

// emulators.go — the Emulators page: a cross-platform catalog of emulators
// with their availability.
//
// It is an overlay (like Tools) so it works on every config: the desktop
// config has no "Emulator" scene (the home card used to dead-end there), and
// on the handheld the card must list every emulator with its availability
// instead of a static launcher label. The catalog is built from the platform's
// real emulator locations: known install folders on Windows (the portable
// Emulators folder JukaHub installs into first), the SD-card Emus/ and Roms/
// trees plus installed and published RetroArch cores on TrimUI. emutiles.go
// draws it as a page of tiles; emuwininstall.go and emulatorinstall.go install
// what is missing, and
// hakux.go the handheld's one standalone install, hakuX for the Xbox.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// emuStatus is the availability of one emulator.
type emuStatus uint8

const (
	emuAvailable   emuStatus = iota // installed with a usable launcher
	emuBroken                       // installed but unusable (no launcher)
	emuMissing                      // expected but not installed
	emuUnsupported                  // cannot run on this device at all
)

// emuEntry is one tile of the availability page. Launch is the entry point to
// execute for available emulators; entries without one are listed for
// availability only (e.g. RetroArch cores).
type emuEntry struct {
	Name   string
	Detail string
	Status emuStatus
	Launch string
	Pinned bool // RetroAchievements and the tool rows come first
	// Core is the libretro core that provides this tile's system, when there
	// is one. It is set on core rows themselves (the core that runs them is
	// the core) and resolved from the system name for the rest, so the page can
	// install the emulator a system is missing.
	Core string
	// IconPath is the emulator's own picture when the install ships one: the
	// icon in a TrimUI Emus/ folder, or an image next to a Windows executable.
	IconPath string
	// Glyph is the shared vector icon (an emu-* name, see emuGlyphFor) drawn
	// when there is no picture, so every tile shows what it emulates.
	Glyph string
	// Version is the installed build when it is known: the release tag of a
	// Windows emulator JukaHub installed.
	Version string
	// Managed marks a Windows emulator JukaHub installed (or the handheld's
	// hakuX package, hakux.go), and can therefore update and roll back; an
	// install found elsewhere is launched but left alone.
	Managed bool
	// KeptVersion is the version a rollback of a managed emulator would
	// restore ("" when none is kept). Read once per catalog build, so the
	// footer never touches the disk while drawing.
	KeptVersion string
	// Via names the Windows emulator that plays this tile's system (Roms/XBOX
	// is played by xemu), so Install on the system installs it.
	Via string
	// Note says why an unsupported tile cannot run here.
	Note string
}

// ── Windows catalog ──────────────────────────────────────────────────────

// winEmuSpec pairs a display name with the executable names its installs use.
type winEmuSpec struct {
	name  string
	files []string
}

var winEmuCatalog = []winEmuSpec{
	{"RetroArch", []string{"retroarch.exe"}},
	{"PPSSPP", []string{"PPSSPPWindows64.exe", "PPSSPPWindows.exe", "ppsspp.exe"}},
	{"Dolphin", []string{"Dolphin.exe"}},
	{"PCSX2", []string{"pcsx2-qt.exe", "pcsx2-qtx64-avx2.exe", "pcsx2-qtx64.exe", "pcsx2.exe"}},
	{"RPCS3", []string{"rpcs3.exe"}},
	{"DuckStation", []string{"duckstation-qt-x64-ReleaseLTCG.exe", "DuckStation.exe", "duckstation-qt.exe"}},
	{"Cemu", []string{"Cemu.exe"}},
	{"Vita3K", []string{"Vita3K.exe", "vita3k.exe"}},
	{"xemu", []string{"xemu.exe"}},
	// Xbox 360: Xenia Canary's build is xenia_canary.exe; xenia.exe is the
	// upstream master build, found when it is on PATH.
	{emuXeniaName, []string{"xenia_canary.exe", "xenia.exe"}},
	{"melonDS", []string{"melonDS.exe"}},
	{"mGBA", []string{"mGBA.exe", "mgba.exe"}},
	{"FCEUX", []string{"fceux.exe"}},
	{"Snes9x", []string{"snes9x.exe"}},
	{"Mednafen", []string{"mednafen.exe"}},
	{"MAME", []string{"mame.exe"}},
	{"bsnes", []string{"bsnes.exe"}},
}

// winSearchDirs returns the folders searched for Windows emulator installs:
// the Emulators folder JukaHub installs into, portable copies next to
// JukaHub.exe, then the standard program roots, RetroArch's own installer
// folder and Steam's library.
func winSearchDirs() []string {
	dirs := append([]string(nil), emuWinExistingRoots()...)
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			dirs = append(dirs, v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); v != "" {
		dirs = append(dirs, filepath.Join(v, "Programs"))
	}
	if drive := strings.TrimSpace(os.Getenv("SystemDrive")); drive != "" {
		// RetroArch's own installer defaults to C:\RetroArch-Win64.
		dirs = append(dirs, filepath.Join(drive+string(filepath.Separator), "RetroArch-Win64"))
	}
	if v := strings.TrimSpace(os.Getenv("ProgramFiles(x86)")); v != "" {
		dirs = append(dirs, filepath.Join(v, "Steam", "steamapps", "common"))
	}
	dirs = append(dirs, winExtraSearchDirs()...)
	// One folder listed twice (Steam's default library is both a fixed root
	// and a library) would be probed twice for every emulator.
	seen := map[string]bool{}
	out := dirs[:0]
	for _, dir := range dirs {
		key := strings.ToLower(filepath.Clean(dir))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, dir)
	}
	return out
}

// winExtraDirsCache holds winExtraSearchDirs: it reads the registry and
// Steam's library list, and the netplay panel and the Cores tab ask for
// RetroArch every frame. The first call fills it; after that a stale list is
// served while a background refresh runs, so no frame waits on the registry.
var winExtraDirsCache struct {
	mu         sync.Mutex
	at         time.Time
	dirs       []string
	refreshing bool
}

// winExtraDirsTTL is how long winExtraSearchDirs trusts its list before a
// background refresh; invalidateEmulatorCache (an install) expires it at once.
const winExtraDirsTTL = 5 * time.Minute

// winExtraSearchDirs adds the Windows installs the fixed folders above miss:
// the folders installers recorded in the registry (RetroArch installed to
// D:\Games\RetroArch), every Steam library (Steam's RetroArch on a second
// drive), a Scoop install, and portable copies unzipped to the Desktop,
// Downloads or Documents. Only folders that exist are returned. Nothing on
// other platforms: a TrimUI never runs the Windows catalog.
func winExtraSearchDirs() []string {
	if !IsWindows() {
		return nil
	}
	winExtraDirsCache.mu.Lock()
	if !winExtraDirsCache.at.IsZero() {
		dirs := append([]string(nil), winExtraDirsCache.dirs...)
		if time.Since(winExtraDirsCache.at) >= winExtraDirsTTL && !winExtraDirsCache.refreshing {
			winExtraDirsCache.refreshing = true
			go func() {
				fresh := winExtraSearchDirsScan()
				winExtraDirsCache.mu.Lock()
				winExtraDirsCache.dirs = fresh
				winExtraDirsCache.at = time.Now()
				winExtraDirsCache.refreshing = false
				winExtraDirsCache.mu.Unlock()
			}()
		}
		winExtraDirsCache.mu.Unlock()
		return dirs
	}
	winExtraDirsCache.mu.Unlock()
	fresh := winExtraSearchDirsScan()
	winExtraDirsCache.mu.Lock()
	winExtraDirsCache.dirs = fresh
	winExtraDirsCache.at = time.Now()
	winExtraDirsCache.mu.Unlock()
	return append([]string(nil), fresh...)
}

// winExtraSearchDirsScan is winExtraSearchDirs without the cache.
func winExtraSearchDirsScan() []string {
	names := make([]string, 0, len(winEmuCatalog))
	for _, spec := range winEmuCatalog {
		names = append(names, spec.name)
	}
	cands := emuWinRegistryDirs(names)
	if root := steamRoot(); root != "" {
		for _, lib := range steamLibraryFolders(root) {
			cands = append(cands, filepath.Join(lib, "common"))
		}
	}
	if drive := strings.TrimSpace(os.Getenv("SystemDrive")); drive != "" {
		cands = append(cands,
			filepath.Join(drive+string(filepath.Separator), "RetroArch"),
			filepath.Join(drive+string(filepath.Separator), "Emulators"),
			filepath.Join(drive+string(filepath.Separator), "Games"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cands = append(cands, filepath.Join(home, "scoop", "apps", "retroarch", "current"))
		for _, sub := range []string{"Desktop", "Downloads", "Documents", "Emulators", "Games"} {
			cands = append(cands, filepath.Join(home, sub))
		}
	}
	if v := strings.TrimSpace(os.Getenv("APPDATA")); v != "" {
		cands = append(cands, filepath.Join(v, "RetroArch"))
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range cands {
		key := strings.ToLower(filepath.Clean(dir))
		if seen[key] || !dirExists(dir) {
			continue
		}
		seen[key] = true
		out = append(out, filepath.Clean(dir))
	}
	return out
}

// windowsEmulatorEntries detects the cataloged Windows emulators inside dirs.
// lookPath covers PATH installs; the deterministic folder probes cover
// standard installs like "C:\Program Files\PPSSPP\PPSSPPWindows.exe", and a
// folder JukaHub installed is found through its manifest even when the
// launcher sits one level down.
func windowsEmulatorEntries(dirs []string, lookPath func(string) (string, error)) []emuEntry {
	entries := make([]emuEntry, 0, len(winEmuCatalog))
	for _, spec := range winEmuCatalog {
		found, managedDir, manifest := emuWinDetect(spec, dirs, lookPath)
		e := emuEntry{Name: spec.name, Glyph: emuGlyphFor(spec.name, "")}
		if found != "" {
			e.Status = emuAvailable
			e.Launch = found
			e.Detail = found
			e.IconPath = sidecarIcon(found)
			if managedDir != "" {
				e.Managed = true
				e.Version = manifest.Tag
				e.KeptVersion = emuWinKeptTag(managedDir)
				if e.Version != "" {
					e.Detail = e.Version + " · " + found
				}
			}
			// What still stands between the emulator and a game, said on its
			// tile: xemu without the Xbox files, Xenia without its runtime.
			if note := emuWinSetupNote(spec.name, found); note != "" {
				e.Detail = note + " · " + e.Detail
			}
		} else {
			e.Status = emuMissing
			e.Detail = "Not installed"
			if install, ok := emuWinSpecFor(spec.name); ok {
				if install.installable() {
					e.Detail = "Not installed · Install downloads the official Windows build"
				} else if install.Site != "" {
					e.Detail = "Not installed · Get it from " + strings.TrimPrefix(strings.TrimPrefix(install.Site, "https://"), "www.")
				}
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// emuWinDetect finds one cataloged emulator: the folder JukaHub installed
// (its manifest names the launcher and the release, which is what makes Update
// and Roll back possible), then a PATH install, then the deterministic folder
// probes. managedDir is set only for JukaHub's own installs.
func emuWinDetect(spec winEmuSpec, dirs []string, lookPath func(string) (string, error)) (found, managedDir string, manifest emuWinManifest) {
	for _, dir := range dirs {
		folder := filepath.Join(dir, spec.name)
		m, ok := emuWinReadManifest(folder)
		if !ok {
			continue
		}
		if exe := filepath.Join(folder, m.Exe); m.Exe != "" && regularFile(exe) {
			return exe, folder, m
		}
	}
	if lookPath != nil {
		for _, file := range spec.files {
			if p, err := lookPath(file); err == nil && p != "" {
				return p, "", emuWinManifest{}
			}
		}
	}
	for _, dir := range dirs {
		for _, file := range spec.files {
			for _, cand := range []string{
				filepath.Join(dir, file),
				filepath.Join(dir, spec.name, file),
				// RetroArch's installer and its zip both name the folder
				// RetroArch-Win64, so one unzipped to Downloads is found.
				filepath.Join(dir, spec.name+"-Win64", file),
			} {
				if regularFile(cand) {
					return cand, "", emuWinManifest{}
				}
			}
		}
	}
	return "", "", emuWinManifest{}
}

// emuEntriesHave reports whether entries already list name.
func emuEntriesHave(entries []emuEntry, name string) bool {
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return true
		}
	}
	return false
}

// emuWinExe finds a cataloged Windows emulator's launcher the way the catalog
// does, or "" when it is not installed. Only that one emulator is probed.
func emuWinExe(name string) string {
	key := strings.ToLower(name)
	emuWinExeCache.mu.Lock()
	if hit, ok := emuWinExeCache.m[key]; ok && time.Since(hit.at) < emuWinExeTTL {
		emuWinExeCache.mu.Unlock()
		return hit.path
	}
	emuWinExeCache.mu.Unlock()
	found := ""
	for _, spec := range winEmuCatalog {
		if strings.EqualFold(spec.name, name) {
			found, _, _ = emuWinDetect(spec, winSearchDirs(), exec.LookPath)
			break
		}
	}
	emuWinExeCache.mu.Lock()
	if emuWinExeCache.m == nil {
		emuWinExeCache.m = map[string]emuWinExeHit{}
	}
	emuWinExeCache.m[key] = emuWinExeHit{path: found, at: time.Now()}
	emuWinExeCache.mu.Unlock()
	return found
}

// emuWinExeCache remembers emuWinExe for a few seconds: the Cores tab and the
// netplay panel ask for RetroArch every frame, and each answer is a PATH search
// plus a few stats per search folder. invalidateEmulatorCache (an install or
// uninstall) clears it.
var emuWinExeCache struct {
	mu sync.Mutex
	m  map[string]emuWinExeHit
}

// emuWinExeHit is one cached emuWinExe answer ("" = not installed).
type emuWinExeHit struct {
	path string
	at   time.Time
}

// emuWinExeTTL bounds how stale a cached emuWinExe answer may be.
const emuWinExeTTL = 3 * time.Second

// emuRetroArchForCore is the RetroArch that loads a Windows core: the
// retroarch.exe in the folder above the core's cores folder, else the one the
// catalog finds.
func emuRetroArchForCore(corePath string) string {
	if strings.TrimSpace(corePath) == "" {
		return ""
	}
	return emuRetroArchForCoresDir(filepath.Dir(corePath))
}

// emuRetroArchForCoresDir is emuRetroArchForCore for every core in one cores
// folder, so a catalog build looks RetroArch up once per folder, not per core.
func emuRetroArchForCoresDir(coresDir string) string {
	if exe := filepath.Join(filepath.Dir(coresDir), "retroarch.exe"); regularFile(exe) {
		return exe
	}
	return emuWinExe("RetroArch")
}

// emuWinRetroArchCoreDirs lists the cores folder beside every RetroArch the
// Windows catalog would find, JukaHub's own install first. compat.go's
// retroArchCoreDirs adds them on Windows, so a portable RetroArch's cores can be
// installed, updated and rolled back like the handheld's.
func emuWinRetroArchCoreDirs() []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range winSearchDirs() {
		for _, cand := range []string{
			filepath.Join(dir, "retroarch.exe"),
			filepath.Join(dir, "RetroArch", "retroarch.exe"),
		} {
			if !regularFile(cand) {
				continue
			}
			cores := filepath.Join(filepath.Dir(cand), "cores")
			key := strings.ToLower(filepath.Clean(cores))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, cores)
		}
	}
	// The RetroArch the catalog actually picks may sit where the two probes
	// above do not look (a registry or Steam-library install, a
	// RetroArch-Win64 folder): its cores folder counts too.
	if exe := emuWinExe("RetroArch"); exe != "" {
		cores := filepath.Join(filepath.Dir(exe), "cores")
		if key := strings.ToLower(filepath.Clean(cores)); !seen[key] {
			out = append(out, cores)
		}
	}
	return out
}

// windowsSystemEntries adds what a Windows catalog shows beside the standalone
// emulators: the cores installed in RetroArch, and every Roms/ system that has
// games but nothing to play them with — a libretro core JukaHub can install, or
// the Windows emulator a desktop-only system needs (Roms/XBOX → xemu).
func windowsSystemEntries(romsRoots, coresDirs []string, standalone []emuEntry) []emuEntry {
	var out []emuEntry
	installed := map[string]bool{}
	for _, dir := range coresDirs {
		rows, cores := emuCoreEntries(dir)
		for core := range cores {
			installed[core] = true
		}
		// A Windows core is playable straight from the Games library: the core
		// file is its launcher, and gameLaunchCommandArgs starts the RetroArch
		// beside it with -L (emuRetroArchForCore).
		retroArch := ""
		if len(rows) > 0 {
			retroArch = emuRetroArchForCoresDir(dir)
		}
		for _, row := range rows {
			if emuEntriesHave(out, row.Name) {
				continue
			}
			if dll := filepath.Join(dir, row.Core+"_libretro.dll"); retroArch != "" && regularFile(dll) {
				row.Launch = dll
			}
			out = append(out, row)
		}
		if len(rows) > 0 {
			break
		}
	}
	available := map[string]bool{}
	for _, e := range standalone {
		if e.Status == emuAvailable {
			available[strings.ToLower(e.Name)] = true
		}
	}
	seen := map[string]bool{}
	for _, root := range romsRoots {
		systems, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, r := range systems {
			if !r.IsDir() {
				continue
			}
			key := strings.ToLower(r.Name())
			if seen[key] {
				continue
			}
			folder := filepath.Join(root, r.Name())
			if sys, ok := emuDesktopSystemFor(r.Name()); ok {
				if sys.Emulator == "" || available[strings.ToLower(sys.Emulator)] {
					continue
				}
				seen[key] = true
				out = append(out, emuEntry{
					Name:   r.Name(),
					Detail: "Games in " + folder + " — install " + sys.Emulator + " to play them",
					Status: emuMissing,
					Glyph:  emuGlyphFor(sys.System, ""),
					Via:    sys.Emulator,
				})
				continue
			}
			if coreCoversSystem(installed, r.Name()) {
				continue
			}
			core := normalizeCoreName(defaultCoreForSystem(r.Name()))
			if core == "" {
				continue
			}
			seen[key] = true
			out = append(out, emuEntry{
				Name:   r.Name(),
				Detail: "Games in " + folder + " — no emulator yet",
				Status: emuMissing,
				Glyph:  emuGlyphFor(r.Name(), core),
			})
		}
	}
	return out
}

func regularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// ── Handheld (TrimUI) catalog ────────────────────────────────────────────

// TrimUI emulator locations: the SD card first, then the internal firmware
// tree. Stock and CrossMix both use the Emus/ and Roms/ layout.
var (
	deviceEmuRoots = []string{"/mnt/SDCARD/Emus", "/usr/trimui/emus"}
	deviceRomsRoot = "/mnt/SDCARD/Roms"
	deviceRACores  = "/mnt/SDCARD/RetroArch/.retroarch/cores"
)

// deviceRetroArchCoresDir is the cores folder of the handheld's RetroArch:
// deviceRACores when that RetroArch is there, else the first other layout
// that holds a RetroArch binary — the card JukaHub runs from (a second card
// slot), an Apps/RetroArch app, or the firmware's internal copy. Only
// /mnt/SDCARD was ever looked at, so a RetroArch anywhere else had no tile.
func deviceRetroArchCoresDir() string {
	var roots []string
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		roots = append(roots, root)
	}
	roots = append(roots, "/mnt/SDCARD", "/mnt/sdcard")
	cands := []string{deviceRACores}
	for _, root := range roots {
		cands = append(cands,
			filepath.Join(root, "RetroArch", ".retroarch", "cores"),
			filepath.Join(root, "Apps", "RetroArch", ".retroarch", "cores"),
		)
	}
	cands = append(cands, "/usr/trimui/RetroArch/.retroarch/cores")
	for _, cores := range cands {
		ra := filepath.Dir(filepath.Dir(cores))
		for _, name := range netplayRetroArchNames {
			if regularFile(filepath.Join(ra, name)) {
				return cores
			}
		}
	}
	return deviceRACores
}

// emuFolderConfigMax bounds how much of an emulator folder's config.json a
// catalog build reads. The firmware's files are a few kilobytes even with a
// long CrossMix launchlist; a damaged or mislabelled file must not be slurped.
const emuFolderConfigMax = 256 << 10

// emuConfigField is one key of a parsed config.json: the exact spelling
// first, then any capitalisation, so a hand-edited "Launch" still counts. Of
// two other spellings the first in byte order wins, the same on every run.
func emuConfigField(fields map[string]json.RawMessage, want string) json.RawMessage {
	if value, ok := fields[want]; ok {
		return value
	}
	found, best := false, ""
	var value json.RawMessage
	for key, v := range fields {
		if strings.EqualFold(key, want) && (!found || key < best) {
			found, best, value = true, key, v
		}
	}
	return value
}

// emuFolderConfig reads an emulator folder's config.json into its top-level
// fields: nil when the file is missing, oversized or not a JSON object. The
// catalog reads it once per folder for both the icon and the launcher.
func emuFolderConfig(folder string) map[string]json.RawMessage {
	f, err := os.Open(filepath.Join(folder, "config.json"))
	if err != nil {
		return nil
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, emuFolderConfigMax+1))
	if err != nil || len(raw) > emuFolderConfigMax {
		return nil
	}
	// A config.json saved by a Windows editor can start with a byte-order
	// mark, which the JSON decoder refuses.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields
}

// emuConfigLaunchNames lists the launchers a parsed config.json declares, in
// the order the firmware uses them: "launch" first — the script MainUI starts,
// launch.sh on the stock firmware but on CrossMix whatever that system's
// default emulator is (drastic.sh, ppsspp_1.17.1_gl.sh) — then every
// "launchlist" entry, CrossMix's alternative emulators. A field of an
// unexpected shape is skipped without losing the others.
func emuConfigLaunchNames(fields map[string]json.RawMessage) []string {
	var names []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	var launch string
	if json.Unmarshal(emuConfigField(fields, "launch"), &launch) == nil {
		add(launch)
	}
	var list []json.RawMessage
	if json.Unmarshal(emuConfigField(fields, "launchlist"), &list) == nil {
		for _, item := range list {
			var name string
			if json.Unmarshal(item, &name) == nil {
				add(name)
				continue
			}
			var entry map[string]json.RawMessage
			if json.Unmarshal(item, &entry) == nil &&
				json.Unmarshal(emuConfigField(entry, "launch"), &name) == nil {
				add(name)
			}
		}
	}
	return names
}

// emuFolderLauncher is the script MainUI would start for an emulator folder
// whose config.json fields are given: the first launcher they declare that is
// a shell script inside the folder, then the conventional launch.sh and
// launch. Only launch.sh used to count, so every CrossMix system whose default
// emulator has its own script — PSP's PPSSPP, NDS's DraStic — read "no
// launcher" and its games "no emulator found" while MainUI ran them fine. A
// declared name that leaves the folder ("../x.sh", an absolute path elsewhere)
// is ignored, the rule the Apps scanner applies to an app's "launch", and a
// declared file that is not a .sh script is skipped because JukaHub starts
// launchers with sh.
func emuFolderLauncher(folder string, fields map[string]json.RawMessage) string {
	for _, name := range emuConfigLaunchNames(fields) {
		if !strings.EqualFold(filepath.Ext(name), ".sh") {
			continue
		}
		if path, err := resolveWithinRoot(folder, name); err == nil && regularFile(path) {
			return path
		}
	}
	for _, name := range []string{"launch.sh", "launch"} {
		if path := filepath.Join(folder, name); regularFile(path) {
			return path
		}
	}
	return ""
}

// emuCoreEntries lists the libretro cores in one cores folder as catalog rows
// (shared objects on the handheld, DLLs on Windows), and the set of core names
// found.
func emuCoreEntries(coresDir string) ([]emuEntry, map[string]bool) {
	installed := map[string]bool{}
	if coresDir == "" {
		return nil, installed
	}
	cores, err := os.ReadDir(coresDir)
	if err != nil {
		return nil, installed
	}
	var rows []emuEntry
	for _, c := range cores {
		if c.IsDir() {
			continue
		}
		file := c.Name()
		ext := strings.ToLower(filepath.Ext(file))
		if ext != ".so" && ext != ".dll" {
			continue
		}
		name := strings.TrimSuffix(file, filepath.Ext(file))
		name = strings.TrimSuffix(name, "_libretro")
		if name == "" || installed[name] {
			continue
		}
		installed[name] = true
		detail := "RetroArch core — " + filepath.Join(coresDir, file)
		if system, _ := emuCoreInfo(name); system != "" {
			detail = system + " · RetroArch core — " + filepath.Join(coresDir, file)
		}
		rows = append(rows, emuEntry{
			Name:   name,
			Detail: detail,
			Status: emuAvailable,
			Core:   name,
			Glyph:  emuGlyphFor(name, name),
		})
	}
	return rows, installed
}

// emuPublishedCoreRows adds every core the handheld's buildbot channel offers
// that is not already installed or represented by a launcher/system tile. The
// index is fetched in the background; no catalog build waits for the network.
func emuPublishedCoreRows(index []coreIndexEntry, installed map[string]bool, existing map[string]emuEntry) []emuEntry {
	rows := make([]emuEntry, 0, len(index))
	for _, entry := range index {
		if entry.Core == "" || installed[entry.Core] {
			continue
		}
		if _, present := existing[strings.ToLower(entry.Core)]; present {
			continue
		}
		detail := "Downloadable RetroArch core · " + entry.Date
		if system, _ := emuCoreInfo(entry.Core); system != "" {
			detail = system + " · " + detail
		}
		rows = append(rows, emuEntry{
			Name:   entry.Core,
			Detail: detail,
			Status: emuMissing,
			Core:   entry.Core,
			Glyph:  emuGlyphFor(entry.Core, entry.Core),
		})
	}
	return rows
}

// deviceEmulatorEntries builds the handheld list: every emulator folder under
// the Emus roots with its availability, installed and published RetroArch
// cores, every ROM system that has games but no emulator at all, and the
// consoles this hardware cannot run (when their Roms/ folder exists), marked
// "Not supported" with the reason instead of offering a dead end, and always
// one Xbox tile: hakuX where
// it is offered or installed, the honest reason where it is not.
func deviceEmulatorEntries(emuRoots []string, romsRoot, coresDir string) []emuEntry {
	byName := map[string]emuEntry{}
	add := func(e emuEntry) {
		key := strings.ToLower(e.Name)
		if old, ok := byName[key]; ok {
			// Keep the first installed entry; anything replaces a broken or
			// missing one so a second root can rescue it.
			if old.Status == emuAvailable {
				return
			}
			byName[key] = e
			return
		}
		byName[key] = e
	}

	for _, root := range emuRoots {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			// Hidden and Mac/Windows metadata folders are never emulators.
			if !de.IsDir() || skipScanEntryName(de.Name()) {
				continue
			}
			folder := filepath.Join(root, de.Name())
			if hakuxIsXboxFolder(de.Name(), folder) {
				// The Xbox launcher folder (hakuX's Emus/XBOX) is the Xbox
				// tile below, not a row of its own.
				continue
			}
			fields := emuFolderConfig(folder)
			e := emuEntry{
				Name:     de.Name(),
				Detail:   folder,
				Glyph:    emuGlyphFor(de.Name(), defaultCoreForSystem(de.Name())),
				IconPath: emuFolderIconFrom(folder, fields),
			}
			// The launcher is the script config.json names (launch.sh on the
			// stock firmware, the default emulator's own script on CrossMix),
			// else launch.sh; a folder with none still gets listed, as
			// installed-but-unusable.
			if launcher := emuFolderLauncher(folder, fields); launcher != "" {
				e.Status = emuAvailable
				e.Launch = launcher
				if base := strings.ToLower(filepath.Base(launcher)); base != "launch.sh" && base != "launch" {
					// On CrossMix the script's name is the emulator
					// (ppsspp_1.17.1_gl.sh), so the tile says which one runs.
					e.Detail = folder + " · " + filepath.Base(launcher)
				}
				if e.IconPath == "" {
					// A picture beside the launcher, looked up once here: the
					// tiles draw every frame (emuEntryIconPath).
					e.IconPath = sidecarIcon(launcher)
				}
			} else {
				e.Status = emuBroken
				e.Detail = folder + " — no launcher"
			}
			add(e)
		}
	}

	// RetroArch cores are emulators in their own right: listing each installed
	// core shows exactly which systems are covered.
	coreRows, installed := emuCoreEntries(coresDir)
	for _, row := range coreRows {
		add(row)
	}
	// RetroArch itself, beside those cores: where it lives, its core count and
	// config, and A opens its menu (raemu.go).
	if ra, ok := deviceRetroArchEntry(coresDir, len(coreRows)); ok {
		add(ra)
	}

	// Systems with games but no emulator are the rows that matter most: the
	// user finds out here instead of when launching fails. A system a core
	// already covers is not one of those rows — the core's own tile is the
	// row, and calling the folder unplayable next to it would be wrong.
	if romsRoot != "" {
		if roms, err := os.ReadDir(romsRoot); err == nil {
			for _, r := range roms {
				if !r.IsDir() {
					continue
				}
				if _, ok := byName[strings.ToLower(r.Name())]; ok {
					continue
				}
				folder := filepath.Join(romsRoot, r.Name())
				if sys, ok := emuDesktopSystemFor(r.Name()); ok {
					if sys.System == emuXboxEntryName {
						// The Xbox tile below speaks for every Xbox folder.
						continue
					}
					add(emuEntry{
						Name:   r.Name(),
						Detail: sys.System + " — not supported on this device",
						Status: emuUnsupported,
						Glyph:  emuGlyphFor(sys.System, ""),
						Note:   sys.Reason,
					})
					continue
				}
				if coreCoversSystem(installed, r.Name()) {
					continue
				}
				add(emuEntry{
					Name:   r.Name(),
					Detail: "Games in " + folder + " — no emulator found",
					Status: emuMissing,
					Glyph:  emuGlyphFor(r.Name(), defaultCoreForSystem(r.Name())),
				})
			}
		}
	}

	// Xbox is listed whether or not a Roms/ folder exists: people ask for it
	// (Halo 2). On the Smart Pro the tile installs the experimental hakuX port;
	// on a model it is not offered for, it says why. A launcher folder that is
	// already on the card is the tile, whoever put it there.
	xboxFolder, _, _ := hakuxInstalled(emuRoots)
	add(hakuxXboxEntry(xboxFolder, romsRoot, hakuxDevice()))
	if IsTrimuiDevice() {
		for _, row := range emuPublishedCoreRows(emuPublishedCores(), installed, byName) {
			add(row)
		}
	}

	entries := make([]emuEntry, 0, len(byName))
	for _, e := range byName {
		entries = append(entries, e)
	}
	sortEmulatorEntries(entries)
	return entries
}

// coreCoversSystem reports whether an installed libretro core is the default
// emulator for a system, so a Roms/ folder is not reported as having no
// emulator when RetroArch already holds the core that runs it.
func coreCoversSystem(installed map[string]bool, system string) bool {
	core := normalizeCoreName(defaultCoreForSystem(system))
	return core != "" && installed[core]
}

// sortEmulatorEntries groups rows by availability (available first, then
// broken, missing and finally unsupported), then alphabetically.
func sortEmulatorEntries(entries []emuEntry) {
	rank := map[emuStatus]int{emuAvailable: 0, emuBroken: 1, emuMissing: 2, emuUnsupported: 3}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Pinned != entries[j].Pinned {
			return entries[i].Pinned
		}
		if entries[i].Pinned {
			// Pinned order is intentional; stable sort keeps it instead of
			// alphabetising the utility rows.
			return false
		}
		if rank[entries[i].Status] != rank[entries[j].Status] {
			return rank[entries[i].Status] < rank[entries[j].Status]
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// ── Catalog cache ────────────────────────────────────────────────────────

// emuCache memoizes the catalog. It is rebuilt whenever the panel opens, so
// installing an emulator shows up on the next visit without a restart.
var emuCache struct {
	mu      sync.Mutex
	entries []emuEntry
	built   bool
}

// emulatorEntries returns the platform's emulator catalog.
func emulatorEntries() []emuEntry {
	emuCache.mu.Lock()
	defer emuCache.mu.Unlock()
	if emuCache.built {
		return emuCache.entries
	}
	var entries []emuEntry
	if IsWindows() {
		entries = windowsEmulatorEntries(winSearchDirs(), exec.LookPath)
		entries = append(entries, windowsSystemEntries(romLibraryRoots(), emuWinRetroArchCoreDirs(), entries)...)
	} else {
		entries = deviceEmulatorEntries(deviceEmuRoots, deviceRomsRoot, deviceRetroArchCoresDir())
		// The PSP tile says which PPSSPP plays PSP games (ppsspp_managed_ui.go).
		entries = ppssppDecorateCatalog(entries)
	}
	// Pinned rows keep their append order: RetroAchievements stays first (it
	// was the original top row), followed by BIOS Doctor and the launchers.
	// The profile row is always present so the folder is discoverable before
	// the first import.
	// Every pinned row gets a glyph so its tile reads before any icon exists;
	// the catalog functions themselves stay unaware of presentation.
	pin := func(e emuEntry, glyph string) emuEntry {
		e.Pinned = true
		e.Glyph = glyph
		return e
	}
	if retroAchievementsSupported() {
		entries = append(entries, pin(retroAchievementsEmuEntry(), "trophy"))
	}
	biosDetail := "Check PS1, Dreamcast and Saturn firmware"
	if IsWindows() {
		biosDetail = "Check PS1, Dreamcast, Saturn and Xbox firmware"
	} else if hakuxOffered() {
		biosDetail = "Check PS1, Dreamcast and Saturn firmware, and the Xbox setup"
	}
	entries = append(entries, pin(emuEntry{
		Name:   biosDoctorEntryName,
		Detail: biosDetail,
		Status: emuAvailable,
	}, "shield"))
	entries = append(entries, pin(gamesLibraryEmuEntry(), "rom"))
	entries = append(entries, pin(gameProfilesEmuEntry(), "list"))
	entries = append(entries, pin(compatibilityEmuEntry(), "speed"))
	// Windows Apps runs .exe files through Wine on the handheld and directly
	// on Windows (windowsapps.go).
	entries = append(entries, pin(windowsAppsEmuEntry(), "apps"))
	if !IsWindows() {
		// PortMaster is a handheld app; listing it on Windows would only offer
		// an installer that cannot produce a working runtime there.
		entries = append(entries, pin(portMasterEmuEntry(), "packages"))
	}
	sortEmulatorEntries(entries)
	emuCache.entries = entries
	emuCache.built = true
	return entries
}

func invalidateEmulatorCache() {
	invalidateEmuCorePresence()
	winExtraDirsCache.mu.Lock()
	if !winExtraDirsCache.at.IsZero() {
		// Stale now: the next look refreshes in the background.
		winExtraDirsCache.at = time.Now().Add(-winExtraDirsTTL)
	}
	winExtraDirsCache.mu.Unlock()
	emuWinExeCache.mu.Lock()
	emuWinExeCache.m = nil
	emuWinExeCache.mu.Unlock()
	emuCache.mu.Lock()
	emuCache.built = false
	emuCache.entries = nil
	emuCache.mu.Unlock()
}

// gamesLibraryEmuEntry is the pinned door to the game browser. It deliberately
// reports no counts: counting a card full of ROMs on every open would make the
// Emulators panel wait on the SD card, and the library screen scans when it
// opens anyway.
func gamesLibraryEmuEntry() emuEntry {
	return emuEntry{
		Name:   gamesLibraryEntryName,
		Detail: "Browse Roms/ by console, with box art and one-press resume",
		Status: emuAvailable,
		Pinned: true,
	}
}

// gameProfilesEmuEntry is the pinned launcher row. It reports the catalog
// totals so an empty or partially broken library is obvious before opening it.
func gameProfilesEmuEntry() emuEntry {
	total, ready, issues := gameProfileStats()
	detail := "Open the launcher · drop JSON into " + gameProfilesImportDir()
	if total > 0 {
		detail = pluralizeCount(total, "profile", "profiles") + fmt.Sprintf(" · %d ready", ready)
		if issues > 0 {
			detail += " · " + pluralizeCount(issues, "file issue", "file issues")
		}
	}
	return emuEntry{
		Name:   gameProfilesEntryName,
		Detail: detail,
		Status: emuAvailable,
		Pinned: true,
	}
}

// compatibilityEmuEntry is the pinned door to the per-game intelligence
// panel. It reports catalog coverage without walking the ROM tree, so opening
// Emulators stays fast on a large SD card.
func compatibilityEmuEntry() emuEntry {
	reports, verified, systems := compatStats()
	detail := pluralizeCount(reports, "report", "reports") + " · " + pluralizeCount(systems, "system", "systems")
	if verified > 0 {
		detail += fmt.Sprintf(" · %d verified", verified)
	}
	if reports == 0 {
		detail = "Per-game reports and boost plans · import a community pack"
	}
	return emuEntry{
		Name:   compatEntryName,
		Detail: detail,
		Status: emuAvailable,
		Pinned: true,
	}
}

// windowsAppsEmuEntry is the pinned launcher for plain .exe files. It is
// always available to open so the setup page can explain a missing Wine or
// WestonPack install; "Unavailable" means no runtime yet, not a hidden row.
func windowsAppsEmuEntry() emuEntry {
	entries := windowsAppEntries()
	runtime := currentWindowsRuntimeStatus()
	e := emuEntry{Name: windowsAppsEntryName, Pinned: true}
	if !runtime.ready() {
		e.Status = emuBroken
		e.Detail = "Open for setup · " + runtime.Problem
		return e
	}
	e.Status = emuAvailable
	e.Detail = windowsAppsSummary(entries, runtime)
	return e
}

// ── Panel ────────────────────────────────────────────────────────────────

type emuPanelState struct {
	open   bool
	cursor int
	scroll int
}

var emuPanel emuPanelState

// emuRowHit maps one visible tile back to its catalog index for clicks.
type emuRowHit struct {
	index int
	rect  sdl.Rect
}

// emuRowHits and emuPanelRect are filled by renderEmuPanel: the visible tile
// rects and the page rect. Only touched on the render/event thread.
var emuRowHits []emuRowHit
var emuPanelRect sdl.Rect

// openEmulatorPanel shows the tile page.
func openEmulatorPanel() {
	// Never stack modals: this panel replaces Quick Settings and Tools.
	qs.open = false
	toolsPanel.open = false
	emuCloseXboxSetup()
	invalidateEmulatorCache()
	// The pinned Game Profiles row reports catalog totals, so rebuild those too.
	invalidateGameProfileCache()
	// On Windows an executable's icon comes from the shell in the background;
	// tiles show their vector glyph until the picture lands in the cache.
	requestAppIconExtraction(emuProgramPaths(emulatorEntries()))
	// "Update available" badges come from a throttled background check; the
	// page draws at once and the badges appear when it answers.
	emuStartUpdateCheck(emuUpdateCheckInterval)
	emuPanel.open = true
	emuPanel.cursor = 0
	emuPanel.scroll = 0
	emuPanelRect = sdl.Rect{}
	emuRowHits = nil
	emuHintHits = nil
	PlayActivateSound()
}

func closeEmulatorPanel() {
	emuPanel.open = false
	emuCloseXboxSetup()
	emuHintHits = nil
	if restoreLibraryFromHandoff() {
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
		PlayBackSound()
		return
	}
	PlayBackSound()
}

// emuPanelMove walks the tile grid. Left/Right stay inside the row and
// Up/Down move a whole row, clamping at the ends so focus never jumps across
// the page the way a flat list did.
func emuPanelMove(dx, dy int) {
	entries := emulatorEntries()
	if len(entries) == 0 {
		return
	}
	next := emuGridTarget(emuPanel.cursor, len(entries), emuGridColumns(), dx, dy)
	if next == emuPanel.cursor {
		return
	}
	emuPanel.cursor = next
	PlayNavSound()
}

// emuPanelJump moves the cursor to an absolute tile (Home/End, a page step),
// clamped to the catalog.
func emuPanelJump(index int) {
	entries := emulatorEntries()
	if len(entries) == 0 {
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(entries) {
		index = len(entries) - 1
	}
	if index == emuPanel.cursor {
		return
	}
	emuPanel.cursor = index
	PlayNavSound()
}

// emuPanelPageStep is how many tiles one page step (PageUp/PageDown, the
// wheel) moves: the rows the page shows, minus one for context.
func emuPanelPageStep() int {
	layout := emuGridView(appConfig)
	rows := layout.rows - 1
	if rows < 1 {
		rows = 1
	}
	return rows * layout.cols
}

// launchEmulatorEntry starts a discovered emulator detached from JukaHub. The
// paths come from this file's fixed scan roots and catalog - never from
// JukaHub's config input; the one name a firmware file supplies, an Emus
// folder's config.json launcher, is confined to that folder when it is read
// (emuFolderLauncher) - so unlike the Apps launcher there is no folder sandbox
// to pass here; the entry point itself is still re-checked before it runs.
func launchEmulatorEntry(e emuEntry) {
	path := strings.TrimSpace(e.Launch)
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".exe", ".bat", ".cmd", ".lnk", ".url", ".sh", "":
	case ".dll":
		// A Windows libretro core: RetroArch loads it with -L.
		if !IsWindows() {
			showLaunchFailureDialog(e.Name, fmt.Errorf("a %s core only runs inside RetroArch on Windows", e.Name), "")
			return
		}
	default:
		logWarn("emulators", "refusing to launch %q", path)
		err := fmt.Errorf("unsupported launcher file type %q", ext)
		showLaunchFailureDialog(e.Name, withLaunchFailureKind(launchFailureUnsupportedLauncherType, err), "")
		return
	}
	if !regularFile(path) {
		logDebug("emulators", "%s vanished: %s", e.Name, path)
		showLaunchFailureDialog(e.Name, fmt.Errorf("launcher file is missing: %w", os.ErrNotExist), "")
		invalidateEmulatorCache()
		return
	}
	logDebug("emulators", "launching %s (%s)", e.Name, path)
	// xemu started from its tile takes up Xbox files the setup sheet found but
	// nobody applied yet; what is still missing goes into the launch toast.
	launchNote := ""
	if IsWindows() && emuIsXemuLauncher(path) {
		launchNote = emuXemuPrepareLaunch(path)
	}
	// No shell ever sees a command line here: path comes from the scan roots
	// and catalog (never from JukaHub's config input) and goes to the program
	// as one argument.
	var cmd *exec.Cmd
	dir := filepath.Dir(path)
	switch {
	case IsWindows() && (ext == ".lnk" || ext == ".url"):
		// Shortcuts are resolved by the shell handler, not executed directly.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case IsWindows() && (ext == ".bat" || ext == ".cmd"):
		// cmd.exe parses its own command line: winBatchCommand quotes the path
		// for it, so a folder like "Emulators (x64)" stays one path.
		bat, err := winBatchCommand(path)
		if err != nil {
			logWarn("emulators", "refusing to launch %s: %v", path, err)
			showLaunchFailureDialog(e.Name, err, "")
			return
		}
		cmd = bat
	case ext == ".dll":
		retroArch := emuRetroArchForCore(path)
		if retroArch == "" {
			showLaunchFailureDialog(e.Name, fmt.Errorf("RetroArch is not installed: %w", os.ErrNotExist), "")
			return
		}
		cmd = exec.Command(retroArch, "-L", path)
		dir = filepath.Dir(retroArch)
	case ext == ".sh" || ext == "":
		// sh reads the launch script; the absolute path is an operand, not code.
		cmd = exec.Command("sh", path)
	default:
		cmd = exec.Command(path)
	}
	cmd.Dir = dir
	// Own process group, same rule as the game launcher: a wrapper script that
	// backgrounds the emulator would otherwise return the screen early.
	screenChildGroupConfigure(cmd)
	// Raise the CPU profile for the emulator and drop it back when the child
	// exits. beginGamePowerProfile never fails a launch: an unsupported
	// firmware only logs.
	restoreProfile := beginLaunchPowerProfile(appConfig, path) // per-game profile, PWR-6
	if err := screenChildStart(cmd); err != nil {
		restoreProfile()
		logWarn("emulators", "launch failed for %s: %v", path, err)
		showLaunchFailureDialog(e.Name, err, "")
		return
	}
	noteGameChildStarted(cmd)
	recorder := startTimelapse(appConfig, e.Name)
	go func() {
		_ = cmd.Wait() // reap the child, keep JukaHub responsive
		screenChildReaped(cmd)
		restoreProfile()
		recorder.Stop()
	}()
	PlaySuccessSound()
	if launchNote != "" {
		ShowToast("Launching "+e.Name+" · "+launchNote, ToastKindInfo)
		return
	}
	ShowToast("Launching "+e.Name, ToastKindSuccess)
}

// emuSelectedEntry is the highlighted tile.
func emuSelectedEntry() (emuEntry, bool) {
	entries := emulatorEntries()
	if emuPanel.cursor < 0 || emuPanel.cursor >= len(entries) {
		return emuEntry{}, false
	}
	return entries[emuPanel.cursor], true
}

func emuPanelActivate(config *Config) {
	e, ok := emuSelectedEntry()
	if !ok {
		return
	}
	if e.Name == biosDoctorEntryName {
		openCommunityPanel(config, communityPageBIOSDoctor)
		emuPanel.open = false
		return
	}
	if e.Name == gamesLibraryEntryName {
		openGameLibraryPanel()
		return
	}
	if e.Name == gameProfilesEntryName {
		emuPanel.open = false
		openGameProfilePanel(true)
		return
	}
	if e.Name == compatEntryName {
		emuPanel.open = false
		openCompatPanel(true)
		return
	}
	if e.Name == windowsAppsEntryName {
		emuPanel.open = false
		openWindowsAppsPanel(true)
		return
	}
	if e.Name == portMasterEntryName {
		emuPanel.open = false
		openPortsPanel(true)
		return
	}
	if e.Name == retroAchievementsEntryName {
		openRetroAchievementsPanelFromEmu()
		return
	}
	if hakuxTile(e) {
		// The handheld's Xbox tile: its games once hakuX is installed, the
		// setup sheet before that (hakuxpage.go).
		hakuxActivate(e)
		return
	}
	if emuRetroArchMenuTile(e) {
		// The handheld's RetroArch tile opens RetroArch's own menu (raemu.go).
		emuPanel.open = false
		launchRetroArchMenu(e)
		return
	}
	switch e.Status {
	case emuUnsupported:
		// Said plainly, every time it is asked: what this console needs and
		// why this device does not have it.
		PlayNavSound()
		ShowToast(e.Name+" is not supported here — "+e.Note, ToastKindInfo)
	case emuAvailable:
		if e.Launch == "" {
			// A core row: RetroArch runs it from a system's launcher, so A shows
			// the games it plays in the Games library (raemu.go).
			emuCoreRowActivate(e)
			return
		}
		emuPanel.open = false
		launchEmulatorEntry(e)
	case emuBroken:
		// A folder without a launcher is unusable, but the system may still have
		// a core this page can install, so that is the offered fix first.
		if emuCoreForEntry(e) != "" && emuCoreInstallSupported() {
			startEmulatorInstall(e)
			return
		}
		ShowToast(e.Name+" is installed but has no launcher", ToastKindWarning)
	default:
		// The page used to dead-end here with "no emulator found". Windows
		// installs the emulator itself from its official release; the buildbot
		// channel the updater reads installs the core that runs a system.
		if IsWindows() && emuWinStandaloneFor(e) != "" {
			startEmulatorInstall(e)
			return
		}
		if emuCoreForEntry(e) != "" && emuCoreInstallSupported() {
			startEmulatorInstall(e)
			return
		}
		ShowToast(e.Name+": no emulator found", ToastKindWarning)
	}
}

// emuPanelInstall starts an install or update for the highlighted tile. X is
// the deliberate press: A opens an emulator that is there, X fixes the one that
// is not (or is out of date).
func emuPanelInstall() {
	e, ok := emuSelectedEntry()
	if !ok {
		return
	}
	// The handheld's PSP tile opens the PPSSPP card (ppsspp_managed_ui.go):
	// JukaHub's own PPSSPP, which PPSSPP plays PSP games, and RetroArch's PPSSPP
	// core — what X installed on this tile before.
	if ppssppTileOpensCard(e) {
		openPPSSPPCard(e)
		return
	}
	startEmulatorInstall(e)
}

// emuPanelAlt is Y: the Xbox setup checklist on xemu's tile (Windows) and on
// the Xbox tile where hakuX is offered (the handheld), the RetroAchievements
// panel everywhere else it is supported.
func emuPanelAlt() {
	if e, ok := emuSelectedEntry(); ok && emuXboxSetupAvailable(e) {
		emuOpenXboxSetup()
		return
	}
	if retroAchievementsSupported() {
		openRetroAchievementsPanelFromEmu()
	}
}

// ctrlEmuPanel is called from the controller overlay priority chain.
func ctrlEmuPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	if emuXboxSheetOpen() {
		return ctrlEmuXboxSheet(e)
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		emuPanelMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		emuPanelMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		emuPanelMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		emuPanelMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		emuPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		emuPanelInstall()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		// R1 puts back the build the last update replaced. It is the undo for
		// L1 and for a single X update, and it only does anything when the
		// highlighted tile has a kept build.
		startEmulatorRollback()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// L1 updates every installed emulator that has a newer build, the
		// same key the compatibility panel uses to fetch cores.
		startEmulatorUpdateInstalled()
	case sdl.CONTROLLER_BUTTON_Y:
		emuPanelAlt()
	case sdl.CONTROLLER_BUTTON_START:
		// Start opens the Core builds page: the same page R2 and the keyboard's
		// K reach, for pads whose triggers are axes this page never sees.
		openCoreBuildsPanel(true)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeEmulatorPanel()
	}
	return true
}

// handleEmuKey processes keyboard input while the panel is open and consumes
// every key so the scene underneath never reacts behind the modal.
func handleEmuKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !emuPanel.open {
		return false
	}
	if emuXboxSheetOpen() {
		return handleEmuXboxKey(e)
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		emuPanelMove(0, -1)
	case sdl.K_DOWN:
		emuPanelMove(0, 1)
	case sdl.K_LEFT:
		emuPanelMove(-1, 0)
	case sdl.K_RIGHT:
		emuPanelMove(1, 0)
	case sdl.K_PAGEUP:
		emuPanelJump(emuPanel.cursor - emuPanelPageStep())
	case sdl.K_PAGEDOWN:
		emuPanelJump(emuPanel.cursor + emuPanelPageStep())
	case sdl.K_HOME:
		emuPanelJump(0)
	case sdl.K_END:
		emuPanelJump(len(emulatorEntries()) - 1)
	case sdl.K_RETURN, sdl.K_SPACE:
		emuPanelActivate(config)
	case sdl.K_x:
		emuPanelInstall()
	case sdl.K_u:
		startEmulatorUpdateInstalled()
	case sdl.K_r:
		startEmulatorRollback()
	case sdl.K_y:
		emuPanelAlt()
	case sdl.K_k, sdl.K_c:
		openCoreBuildsPanel(true)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeEmulatorPanel()
	}
	return true
}

// handleEmuMouse activates a clicked tile, presses a clicked hint badge, or
// closes the page on a click outside it. Returns true when the click was
// consumed.
func handleEmuMouse(mx, my int32, config *Config) bool {
	if !emuPanel.open {
		return false
	}
	if emuXboxSheetOpen() {
		return handleEmuXboxMouse(mx, my)
	}
	for _, hit := range emuHintHits {
		if pointInRect(mx, my, hit.rect) {
			emuHintPress(hit.button, config)
			return true
		}
	}
	for _, hit := range emuRowHits {
		if pointInRect(mx, my, hit.rect) {
			emuPanel.cursor = hit.index
			emuPanelActivate(config)
			return true
		}
	}
	if emuPanelRect.W > 0 && pointInRect(mx, my, emuPanelRect) {
		// Clicks inside the page but off a tile only consume.
		return true
	}
	closeEmulatorPanel()
	return true
}

// handleEmuWheel scrolls the grid a row per notch. It reports whether the
// page consumed the wheel.
func handleEmuWheel(dy int32) bool {
	if !emuPanel.open {
		return false
	}
	if emuXboxSheetOpen() || dy == 0 {
		return true
	}
	if dy > 0 {
		emuPanelMove(0, -1)
	} else {
		emuPanelMove(0, 1)
	}
	return true
}

// emuHintPress runs the action a footer hint badge names, so every key the
// footer advertises is also a click or tap target.
func emuHintPress(button string, config *Config) {
	// A badge may carry its keyboard twin ("R1 / R", inputhints.go); the pad
	// half names the action.
	if i := strings.Index(button, " / "); i >= 0 {
		button = button[:i]
	}
	switch strings.TrimSpace(button) {
	case "A":
		emuPanelActivate(config)
	case "X":
		emuPanelInstall()
	case "Y":
		emuPanelAlt()
	case "R1":
		startEmulatorRollback()
	case "L1":
		startEmulatorUpdateInstalled()
	case "R2", "Start":
		openCoreBuildsPanel(true)
	case "B":
		closeEmulatorPanel()
	}
}

func emuStatusLabel(s emuStatus) (string, sdl.Color) {
	switch s {
	case emuAvailable:
		return "Available", ColorToastSuccess
	case emuBroken:
		return "Unavailable", ColorToastError
	case emuUnsupported:
		return "Not supported", ColorToastWarn
	default:
		return "Not installed", ColorTextTertiary()
	}
}

func emuSummary(entries []emuEntry) string {
	var ok, broken, missing, unsupported int
	for _, e := range entries {
		switch e.Status {
		case emuAvailable:
			ok++
		case emuBroken:
			broken++
		case emuUnsupported:
			unsupported++
		default:
			missing++
		}
	}
	parts := []string{fmt.Sprintf("%d available", ok)}
	if broken > 0 {
		parts = append(parts, fmt.Sprintf("%d unavailable", broken))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d not installed", missing))
	}
	if unsupported > 0 {
		parts = append(parts, fmt.Sprintf("%d not supported", unsupported))
	}
	if updates := emuUpdateCount(); updates > 0 {
		parts = append(parts, pluralizeCount(updates, "update", "updates"))
	}
	return strings.Join(parts, " · ")
}
