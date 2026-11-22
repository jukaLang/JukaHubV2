package main

// coredevlibs.go — the device-access libraries a desktop build of a core
// links, for a RetroArch that starts on JukaHub's glibc.
//
// A tester on a Brick Pro, 2026-11-22 (relayed by Juka): "Could not install
// GameCube games — the 2026-11-21 build was refused here before: cannot load
// on this firmware: needs libusb-1.0.so…". The Dolphin setup (dolphin_setup.go)
// takes JukaHub's own build for TrimUI first; when that source has none, it
// falls back to the libretro buildbot's nightly. That build needs a newer
// glibc than the firmware's — RetroArch starts on JukaHub's for it
// (ra_glibc.go) — and, being a desktop Linux build, it also links libusb and
// libudev (Dolphin's GameCube-adapter and Wii Remote code), which no TrimUI
// firmware carries. The device-library check (coretrimui.go) refused it, and
// rightly: RetroArch's dlopen would have failed on every start.
//
// That morning JukaHub fetched Ubuntu's copies of them through Linux packages,
// and only when its own updater installed the core. Juka, the same noon: "On
// Trimui devices (and Trimui Brick Pro) libusb-1.0.so ...make sure to fix it".
// A core that came another way (RetroArch's own Online Updater, an earlier
// JukaHub, a card copied from another handheld) started without them, and on
// the firmware's glibc at that: raGlibcCoreNeeds saw a problem the runtime
// alone did not close. RetroArch's own menu never had them. And the Ubuntu
// route needed tens of megabytes of package index before the first library.
//
// Now JukaHub writes the libraries itself: stand-ins that export what the core
// imports and report no such device (coredevstub.go; libusb, libudev,
// libbluetooth — not libevdev). They go in <data>/private-glibc/devlibs/
// (devlibs.json lists them, with what each exports), and are made or widened
// whenever something is about to need them, in a moment and without a
// network:
//   - the updater's load check, for a build whose only problem left is such
//     libraries (fetchCoreUpdate);
//   - a launch of a core that links them, before its plan is made
//     (raGlibcCoreNeeds), whichever way the core reached the card;
//   - the start's look at the active cores (corefix.go).
// The folder is read
//   - by the load check, after every other folder, for a core whose RetroArch
//     starts on the runtime (compat.go coreLoadEnvWith, elfLoadEnv.deviceDirs);
//   - by the launch of such a core, last on the private loader's library path
//     (ra_glibc.go raGlibcPlanFor), and by RetroArch's own menu on the
//     runtime, which may load such a core. Last, so a firmware that has the
//     real library keeps it, and harmless anywhere else: a NULL udev is what
//     SDL meets on a firmware with no libudev.
//
// A build the stand-ins do not complete is still refused, with what it lacks
// named: once they are in, every library the build links must be found where
// RetroArch looks (coreDevLibsStrictProblem) — the generic check stands aside
// for a library it cannot find, and a desktop build that also links X11, say,
// would otherwise install and fail every start.

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// coreDevLibsDirName is the folder under <data>/private-glibc.
	coreDevLibsDirName = "devlibs"
	// coreDevLibsManifestName lists what the folder holds.
	coreDevLibsManifestName = "devlibs.json"
	// coreDevLibsKindStub marks a folder of JukaHub's stand-ins. A manifest
	// without it lists the Ubuntu copies the 2026-11-22 morning build
	// installed, which the next ensure replaces.
	coreDevLibsKindStub = "stub"
	// coreDevLibsTTL is how long one look at the folder is used: every core's
	// load check asks for it.
	coreDevLibsTTL = 10 * time.Second
)

// coreDevLibsManifest is devlibs.json.
type coreDevLibsManifest struct {
	Kind string   `json:"kind,omitempty"`
	Libs []string `json:"libs"`
	// Stubs are the stand-ins, by file name.
	Stubs map[string]coreDevStubRecord `json:"stubs,omitempty"`
	// Packages named the Ubuntu packages of the morning build's copies.
	Packages []string `json:"packages,omitempty"`
	Written  string   `json:"written,omitempty"`
}

// coreDevStubRecord is what one stand-in exports, and its size.
type coreDevStubRecord struct {
	Versions []string `json:"versions,omitempty"`
	// Symbols are "name" or "name@VERSION".
	Symbols []string `json:"symbols"`
	Size    int64    `json:"size"`
}

// coreDevLibsDir is the folder, "" without a data folder.
func coreDevLibsDir() string {
	base, err := privateGlibcBase()
	if err != nil {
		return ""
	}
	return filepath.Join(base, coreDevLibsDirName)
}

var coreDevLibsMemo struct {
	mu   sync.Mutex
	at   time.Time
	dirs []string
	libs []string
}

// coreDevLibsLook is the folder as a search list (nil when it holds no
// library) and the libraries in it, looked at once per coreDevLibsTTL.
func coreDevLibsLook() (dirs, libs []string) {
	if !raGlibcSupported() {
		return nil, nil
	}
	m := &coreDevLibsMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	if since := time.Since(m.at); m.at.IsZero() || since < 0 || since >= coreDevLibsTTL {
		m.dirs, m.libs = nil, nil
		if dir := coreDevLibsDir(); dir != "" {
			man, _ := coreDevLibsReadManifest(dir)
			for _, lib := range man.Libs {
				if lib == filepath.Base(lib) && regularFile(filepath.Join(dir, lib)) {
					m.libs = append(m.libs, lib)
				}
			}
			if len(m.libs) > 0 {
				m.dirs = []string{dir}
			}
		}
		m.at = time.Now()
	}
	return slices.Clone(m.dirs), slices.Clone(m.libs)
}

// coreDevLibsReadManifest reads dir's devlibs.json; ok is false when there is
// none that parses.
func coreDevLibsReadManifest(dir string) (man coreDevLibsManifest, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, coreDevLibsManifestName))
	if err != nil || len(raw) > 256<<10 {
		return man, false
	}
	if json.Unmarshal(raw, &man) != nil {
		return coreDevLibsManifest{}, false
	}
	return man, true
}

// coreDevLibsDirs is the folder as a search list: nil while it holds no
// library JukaHub put there.
func coreDevLibsDirs() []string {
	dirs, _ := coreDevLibsLook()
	return dirs
}

// coreDevLibsForget drops that look: the folder just changed.
func coreDevLibsForget() {
	coreDevLibsMemo.mu.Lock()
	coreDevLibsMemo.at = time.Time{}
	coreDevLibsMemo.mu.Unlock()
}

// coreDevLibsStamp names the folder's contents for corefix.go's remembered
// answers (raGlibcStamp): "" while it is empty.
func coreDevLibsStamp() string {
	_, libs := coreDevLibsLook()
	return strings.Join(libs, ",")
}

// coreDevLibsSplit reads coreMissingLibsProblem's sentence back: the
// libraries it names that JukaHub stands in for (devStubLibs), and the others
// (libevdev, say). ok is false for any other problem. Pure.
func coreDevLibsSplit(problem string) (standIn, others []string, ok bool) {
	list, ok := strings.CutPrefix(strings.TrimSpace(problem), "needs ")
	if !ok {
		return nil, nil, false
	}
	list, ok = strings.CutSuffix(list, ", which this firmware does not have")
	if !ok || strings.ContainsAny(list, ";()") {
		return nil, nil, false
	}
	for _, part := range strings.Split(strings.ReplaceAll(list, " and ", ", "), ", ") {
		part = strings.TrimSpace(part)
		switch _, known := devStubLibs[part]; {
		case part == "":
			return nil, nil, false
		case known:
			if !slices.Contains(standIn, part) {
				standIn = append(standIn, part)
			}
		case !slices.Contains(others, part):
			others = append(others, part)
		}
	}
	return standIn, others, true
}

// coreDevLibsMissing are the libraries a load problem names when it is
// nothing but device libraries JukaHub stands in for; nil for any other
// problem (one that names libevdev too, say). Pure.
func coreDevLibsMissing(problem string) []string {
	standIn, others, ok := coreDevLibsSplit(problem)
	if !ok || len(others) > 0 {
		return nil
	}
	return standIn
}

// coreDevLibsCouldCover reports a load problem made only of device libraries
// JukaHub stands in for, for a core that becomes target: RetroArch may start
// on JukaHub's glibc, whose launches read the stand-ins — the RetroArch that
// owns target's folder does, once the runtime is installed ("" target: any).
func coreDevLibsCouldCover(problem, target string) bool {
	if len(coreDevLibsMissing(problem)) == 0 || !raGlibcWanted() {
		return false
	}
	if _, _, ready := raGlibcRuntime(); !ready || strings.TrimSpace(target) == "" {
		return true
	}
	return raGlibcLoadDir(target) != ""
}

// coreDevLibsInstallMu serialises writes into the folder.
var coreDevLibsInstallMu sync.Mutex

// coreDevLibsEnsureForCore is the updater's (fetchCoreUpdate, a background
// goroutine): a downloaded build at file that becomes target and cannot load
// for device libraries gets JukaHub's stand-ins for those it can stand in for.
// ok: the load check should be asked again — it then names only what is still
// missing (libevdev, say), so a refusal says what really stops the build.
// pending: they could not be put in now for a reason another try may not meet
// (JukaHub's glibc did not download, the card refused the write), so the
// build is not remembered as refused.
func coreDevLibsEnsureForCore(ctx context.Context, problem, file, target string) (ok, pending bool) {
	libs, _, isDev := coreDevLibsSplit(problem)
	if !isDev || len(libs) == 0 || strings.TrimSpace(target) == "" || !raGlibcWanted() {
		return false, false
	}
	if _, _, ready := raGlibcRuntime(); ready && raGlibcLoadDir(target) == "" {
		// Installed, but this core's RetroArch does not start on it.
		return false, false
	}
	// The stand-ins are read only by a RetroArch on JukaHub's glibc: the
	// runtime first (a build that only lacks device libraries never asked for
	// it).
	if !raGlibcEnsureRuntimeFor(ctx, target) {
		_, _, ready := raGlibcRuntime()
		return false, !ready && ctx.Err() == nil
	}
	if _, err := coreDevLibsEnsure(file, target); err != nil {
		logWarn("emulators", "stand-ins for %s for %s: %v", mlJoinAnd(libs), filepath.Base(target), err)
		return false, ctx.Err() == nil
	}
	return true, false
}

// coreDevLibsEnsured remembers the cores coreDevLibsEnsureFor already saw, by
// file and folder state, so a launch reads the core's symbols once.
var coreDevLibsEnsured struct {
	mu sync.Mutex
	m  map[string]string
}

// coreDevLibsEnsureFor makes sure the stand-ins core needs are in, for a core
// already in its cores folder (a launch, the start's look, the Dolphin setup).
// true when it wrote one: the caller asks the load check again. Cheap once a
// core was seen: a stat and a map.
func coreDevLibsEnsureFor(core string) bool {
	if strings.TrimSpace(core) == "" || !raGlibcWanted() {
		return false
	}
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	key := coreFixFileStamp(info) + "|" + coreDevLibsStamp()
	coreDevLibsEnsured.mu.Lock()
	seen := coreDevLibsEnsured.m[core] == key
	coreDevLibsEnsured.mu.Unlock()
	if seen {
		return false
	}
	changed, err := coreDevLibsEnsure(core, core)
	if err != nil {
		logWarn("emulators", "stand-in device libraries for %s: %v", filepath.Base(core), err)
		return false
	}
	coreDevLibsEnsured.mu.Lock()
	if coreDevLibsEnsured.m == nil || len(coreDevLibsEnsured.m) > 64 {
		coreDevLibsEnsured.m = map[string]string{}
	}
	coreDevLibsEnsured.m[core] = coreFixFileStamp(info) + "|" + coreDevLibsStamp()
	coreDevLibsEnsured.mu.Unlock()
	return changed
}

// coreDevLibsEnsure writes the stand-ins the core at file needs as target:
// one for each device library it links (devStubLibs) that no folder of the
// runtime, the firmware or RetroArch has and RetroArch does not link itself,
// exporting what the core imports from it together with what that stand-in
// already exported (another core's needs). changed reports a write. The
// morning build's Ubuntu copies, when the folder has them, are replaced first.
func coreDevLibsEnsure(file, target string) (changed bool, err error) {
	if !raGlibcSupported() {
		return false, nil
	}
	fh, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer fh.Close()
	f, err := elf.NewFile(fh)
	if err != nil || f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_AARCH64 {
		// Not a 64-bit ARM core: the load check says what it is.
		return false, nil
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return false, nil
	}
	env := coreLoadEnvWith(target, true)
	ra := coreRetroArchBinary(filepath.Dir(target))
	elsewhere := append(append(elfRunPaths(f, env.origin), env.libDirs...), coreGLExtraDirs(ra)...)
	raNeeds := coreRetroArchNeeds(ra)
	var want []string
	for _, lib := range needed {
		base := filepath.Base(lib)
		if _, known := devStubLibs[base]; !known || raNeeds[base] || slices.Contains(want, base) {
			continue
		}
		if _, state := elfFindLibrary(lib, f.Class, f.Machine, elsewhere); state != elfLibMissing {
			continue // the loader finds a real one first
		}
		want = append(want, base)
	}
	if len(want) == 0 {
		return false, nil
	}
	dest := coreDevLibsDir()
	if dest == "" {
		return false, errors.New("JukaHub has no data folder")
	}
	coreDevLibsInstallMu.Lock()
	defer coreDevLibsInstallMu.Unlock()
	man, _ := coreDevLibsReadManifest(dest)
	if man.Kind != coreDevLibsKindStub {
		// The morning build's Ubuntu copies (and libudev's libcap beside
		// them): the stand-ins replace them, so every handheld runs the same.
		for _, lib := range man.Libs {
			if lib == filepath.Base(lib) && lib != coreDevLibsManifestName {
				_ = os.Remove(filepath.Join(dest, lib))
			}
		}
		if len(man.Libs) > 0 {
			logInfo("emulators", "device libraries: Ubuntu's copies (%s) make way for JukaHub's stand-ins", strings.Join(man.Libs, ", "))
		}
		man = coreDevLibsManifest{Kind: coreDevLibsKindStub}
	}
	if man.Stubs == nil {
		man.Stubs = map[string]coreDevStubRecord{}
	}
	var wrote []string
	for _, lib := range want {
		versions, syms := devStubNeeds(f, lib)
		path := filepath.Join(dest, lib)
		rec, have := man.Stubs[lib]
		if have {
			if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() == rec.Size && coreDevStubCovers(rec, versions, syms) {
				continue
			}
			// Widened, never narrowed: what it exported stays.
			versions = append(versions, rec.Versions...)
			syms = append(syms, coreDevStubSymsOf(rec)...)
		}
		data, err := devStubBuild(lib, versions, syms, devStubLibs[lib].ret)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", lib, err)
		}
		// Through a temporary file and a rename (linuxPkgWriteFile): a
		// RetroArch that has the old one open keeps it whole.
		if err := linuxPkgWriteFile(path, data); err != nil {
			return changed, fmt.Errorf("%s: %w", lib, err)
		}
		man.Stubs[lib] = coreDevStubRecordOf(versions, syms, int64(len(data)))
		if !slices.Contains(man.Libs, lib) {
			man.Libs = append(man.Libs, lib)
		}
		changed = true
		wrote = append(wrote, fmt.Sprintf("%s (%d functions)", lib, len(man.Stubs[lib].Symbols)))
	}
	if !changed {
		return false, nil
	}
	sort.Strings(man.Libs)
	man.Packages = nil
	man.Written = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return changed, err
	}
	// The manifest last: a library counts once it is listed.
	if err := linuxPkgWriteFile(filepath.Join(dest, coreDevLibsManifestName), append(raw, '\n')); err != nil {
		return changed, err
	}
	coreDevLibsForget()
	logInfo("emulators", "device libraries for RetroArch on JukaHub's glibc: JukaHub's stand-ins %s in %s, for %s",
		strings.Join(wrote, ", "), dest, filepath.Base(target))
	return true, nil
}

// coreDevStubRecordOf is the manifest's record of a stand-in written for
// versions and syms.
func coreDevStubRecordOf(versions []string, syms []devStubSym, size int64) coreDevStubRecord {
	rec := coreDevStubRecord{Size: size}
	for _, v := range versions {
		if v != "" && !slices.Contains(rec.Versions, v) {
			rec.Versions = append(rec.Versions, v)
		}
	}
	for _, s := range syms {
		name := s.name
		if s.version != "" {
			name += "@" + s.version
		}
		if !slices.Contains(rec.Symbols, name) {
			rec.Symbols = append(rec.Symbols, name)
		}
	}
	sort.Strings(rec.Versions)
	sort.Strings(rec.Symbols)
	return rec
}

// coreDevStubSymsOf reads a record's symbols back.
func coreDevStubSymsOf(rec coreDevStubRecord) []devStubSym {
	out := make([]devStubSym, 0, len(rec.Symbols))
	for _, s := range rec.Symbols {
		name, version, _ := strings.Cut(s, "@")
		if name != "" {
			out = append(out, devStubSym{name: name, version: version})
		}
	}
	return out
}

// coreDevStubCovers reports whether a stand-in recorded as rec defines every
// version and exports every function a core needs.
func coreDevStubCovers(rec coreDevStubRecord, versions []string, syms []devStubSym) bool {
	for _, v := range versions {
		if v != "" && !slices.Contains(rec.Versions, v) {
			return false
		}
	}
	for _, s := range syms {
		name := s.name
		if s.version != "" {
			name += "@" + s.version
		}
		if !slices.Contains(rec.Symbols, name) {
			return false
		}
	}
	return true
}

// coreDevLibsStrictProblem is the last word on a build that loads only with
// JukaHub's stand-ins (fetchCoreUpdate): every library file links must be
// found where its RetroArch looks — the runtime, the firmware's and
// RetroArch's own folders, the stand-ins — or be linked by RetroArch itself.
// "" when each is; else the missing ones, in the load check's sentence.
// target is the file the build becomes.
func coreDevLibsStrictProblem(file, target string) string {
	fh, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer fh.Close()
	f, err := elf.NewFile(fh)
	if err != nil {
		return ""
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return ""
	}
	env := coreLoadEnvWith(target, true)
	ra := coreRetroArchBinary(filepath.Dir(target))
	dirs := append(append(elfRunPaths(f, env.origin), env.libDirs...), coreGLExtraDirs(ra)...)
	dirs = append(dirs, env.deviceDirs...)
	raNeeds := coreRetroArchNeeds(ra)
	var missing []string
	for _, lib := range needed {
		base := filepath.Base(lib)
		if raNeeds[base] || slices.Contains(missing, base) {
			continue
		}
		if _, state := elfFindLibrary(lib, f.Class, f.Machine, dirs); state == elfLibMissing {
			missing = append(missing, base)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return coreMissingLibsProblem(missing)
}

// coreDevLibsForCore is the folder a launch of core on JukaHub's glibc adds to
// its library path (ra_glibc.go): the stand-ins, when core links one of them;
// nil otherwise, and for every other core.
func coreDevLibsForCore(core string) []string {
	dirs, libs := coreDevLibsLook()
	if len(dirs) == 0 || strings.TrimSpace(core) == "" {
		return nil
	}
	f, err := elf.Open(core)
	if err != nil {
		return nil
	}
	defer f.Close()
	needed, err := f.ImportedLibraries()
	if err != nil {
		return nil
	}
	for _, lib := range needed {
		if slices.Contains(libs, filepath.Base(lib)) {
			return dirs
		}
	}
	return nil
}
