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
// Those libraries are Ubuntu's, like the runtime. For a build whose only load
// problem left is such libraries, JukaHub installs the Ubuntu packages that
// hold them (libusb-1.0-0, libudev1, libbluetooth3, libevdev2: the ones the
// build lacks) with its Linux packages (linuxpkg_*.go: Ubuntu's signed index,
// every .deb checked against its SHA-256), and copies the libraries — and what
// they need that neither the runtime nor the firmware has (libudev's libcap) —
// into <data>/private-glibc/devlibs/. That folder is read
//   - by the load check, after every other folder, for a core whose RetroArch
//     starts on the runtime (compat.go coreLoadEnvWith, elfLoadEnv.deviceDirs);
//   - by the launch of such a core, last on the private loader's library path,
//     and only when the core links one of these libraries (ra_glibc.go
//     raGlibcPlanFor): every other game, port and RetroArch menu starts as
//     before — libudev in particular, which SDL picks up whenever it can, stays
//     out of them.
//
// The libraries need no udev daemon (these handhelds have none): libudev reads
// /sys by itself and only hears no hot-plug events.
//
// A build these libraries do not complete is still refused, with what it
// lacks named: once JukaHub's copies are in, every library the build links
// must be found where RetroArch looks (coreDevLibsStrictProblem) — the generic
// check stands aside for a library it cannot find, and a desktop build that
// also links X11, say, would otherwise install and fail every start.

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// coreDevLibsManifestName lists what the folder holds and where from.
	coreDevLibsManifestName = "devlibs.json"
	// coreDevLibsMaxFiles bounds the libraries one install copies.
	coreDevLibsMaxFiles = 16
	// coreDevLibsMaxSize bounds one library's size.
	coreDevLibsMaxSize = 8 << 20
	// coreDevLibsTTL is how long one look at the folder is used: every core's
	// load check asks for it.
	coreDevLibsTTL = 10 * time.Second
)

// coreDevLibsPackages names the Ubuntu package that holds each device library
// coreDeviceLibLoadProblem can name.
var coreDevLibsPackages = map[string]string{
	"libusb-1.0.so.0":   "libusb-1.0-0",
	"libudev.so.1":      "libudev1",
	"libbluetooth.so.3": "libbluetooth3",
	"libevdev.so.2":     "libevdev2",
}

// coreDevLibsNever are the C and C++ runtime's own files: always the
// runtime's, never copied beside it.
var coreDevLibsNever = map[string]bool{
	"ld-linux-aarch64.so.1": true, "libc.so.6": true, "libm.so.6": true, "libpthread.so.0": true,
	"libdl.so.2": true, "librt.so.1": true, "libresolv.so.2": true, "libstdc++.so.6": true,
	"libgcc_s.so.1": true,
}

// coreDevLibsManifest is devlibs.json.
type coreDevLibsManifest struct {
	Libs      []string `json:"libs"`
	Packages  []string `json:"packages"`
	Installed string   `json:"installed,omitempty"`
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
			if raw, err := os.ReadFile(filepath.Join(dir, coreDevLibsManifestName)); err == nil && len(raw) <= 16<<10 {
				var man coreDevLibsManifest
				if json.Unmarshal(raw, &man) == nil {
					for _, lib := range man.Libs {
						if lib == filepath.Base(lib) && regularFile(filepath.Join(dir, lib)) {
							m.libs = append(m.libs, lib)
						}
					}
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

// coreDevLibsDirs is the folder as a search list: nil while it holds no
// library JukaHub put there.
func coreDevLibsDirs() []string {
	dirs, _ := coreDevLibsLook()
	return dirs
}

// coreDevLibsForget drops that look: an install just changed the folder.
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

// coreDevLibsMissing are the libraries a load problem names when it is
// nothing but device libraries JukaHub can bring — coreMissingLibsProblem's
// sentence, every library in coreDevLibsPackages. nil for any other problem.
// Pure.
func coreDevLibsMissing(problem string) []string {
	list, ok := strings.CutPrefix(strings.TrimSpace(problem), "needs ")
	if !ok {
		return nil
	}
	list, ok = strings.CutSuffix(list, ", which this firmware does not have")
	if !ok || strings.ContainsAny(list, ";()") {
		return nil
	}
	var libs []string
	for _, part := range strings.Split(strings.ReplaceAll(list, " and ", ", "), ", ") {
		part = strings.TrimSpace(part)
		if _, known := coreDevLibsPackages[part]; !known {
			return nil
		}
		if !slices.Contains(libs, part) {
			libs = append(libs, part)
		}
	}
	return libs
}

// coreDevLibsCouldCover reports a load problem made only of device libraries
// JukaHub can bring, for a core that becomes target: Linux packages work here
// and RetroArch may start on JukaHub's glibc — the RetroArch that owns
// target's folder does, once the runtime is installed ("" target: any).
func coreDevLibsCouldCover(problem, target string) bool {
	if len(coreDevLibsMissing(problem)) == 0 || !linuxPkgSupported() || !raGlibcWanted() {
		return false
	}
	if _, _, ready := raGlibcRuntime(); !ready || strings.TrimSpace(target) == "" {
		return true
	}
	return raGlibcLoadDir(target) != ""
}

// coreDevLibsInstallMu serialises installs into the folder.
var coreDevLibsInstallMu sync.Mutex

// coreDevLibsEnsureForCore is the updater's (fetchCoreUpdate, a background
// goroutine): a downloaded build that becomes target and cannot load only for
// device libraries JukaHub can bring gets them. ok: the load check should be
// asked again. pending: they could not be installed now for a reason another
// try may not meet (offline, Linux packages busy, a download that broke), so
// the build is not remembered as refused.
func coreDevLibsEnsureForCore(ctx context.Context, problem, target string) (ok, pending bool) {
	libs := coreDevLibsMissing(problem)
	if len(libs) == 0 || strings.TrimSpace(target) == "" || !coreDevLibsCouldCover(problem, target) {
		return false, false
	}
	// The copies are read only by a RetroArch on JukaHub's glibc: the runtime
	// first (a build that only lacks device libraries never asked for it).
	if !raGlibcEnsureRuntimeFor(ctx, target) {
		_, _, ready := raGlibcRuntime()
		return false, !ready && ctx.Err() == nil
	}
	err := coreDevLibsInstall(ctx, libs, target)
	if err == nil {
		return true, false
	}
	logWarn("emulators", "%s for %s: %v", mlJoinAnd(libs), filepath.Base(target), err)
	if ctx.Err() != nil {
		return false, true
	}
	lower := strings.ToLower(err.Error())
	for _, final := range []string{"unable to locate package", "belongs to a full ubuntu system", "is not an arm64 library", "ubuntu's packages have no"} {
		if strings.Contains(lower, final) {
			return false, false
		}
	}
	return false, true
}

// coreDevLibsInstall installs the Ubuntu packages that hold libs and copies
// them, with what they need that no folder of the runtime, the firmware or
// RetroArch's has, into the folder. target is the core file they are for (its
// RetroArch's folders count as places a library already is).
func coreDevLibsInstall(ctx context.Context, libs []string, target string) error {
	if !linuxPkgSupported() {
		return errors.New("Linux packages are not available on this device")
	}
	var pkgs []string
	for _, lib := range libs {
		pkg, ok := coreDevLibsPackages[lib]
		if !ok {
			return fmt.Errorf("JukaHub has no package for %s", lib)
		}
		if !slices.Contains(pkgs, pkg) {
			pkgs = append(pkgs, pkg)
		}
	}
	sort.Strings(pkgs)
	coreDevLibsInstallMu.Lock()
	defer coreDevLibsInstallMu.Unlock()
	o := &linuxPkgIO{out: func(s string) { logDebug("emulators", "device libraries: %s", s) }}
	logInfo("emulators", "installing Ubuntu's %s for %s (%s)", strings.Join(pkgs, ", "), filepath.Base(target), mlJoinAnd(libs))
	if err := linuxPkgEnsure(ctx, pkgs, false, o); err != nil {
		return err
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	from := []string{
		filepath.Join(lay.root, "usr", "lib", "aarch64-linux-gnu"),
		filepath.Join(lay.root, "lib", "aarch64-linux-gnu"),
	}
	dest := coreDevLibsDir()
	if dest == "" {
		return errors.New("JukaHub has no data folder")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// Where a library already is for this core's RetroArch: the runtime, the
	// firmware's folders and RetroArch's own.
	env := coreLoadEnvWith(target, true)
	elsewhere := append(slices.Clone(env.libDirs), coreGLExtraDirs(coreRetroArchBinary(filepath.Dir(target)))...)
	man := coreDevLibsManifest{Packages: pkgs}
	if raw, err := os.ReadFile(filepath.Join(dest, coreDevLibsManifestName)); err == nil {
		var old coreDevLibsManifest
		if json.Unmarshal(raw, &old) == nil {
			// What an earlier install put there stays listed while it is there.
			for _, lib := range old.Libs {
				if lib == filepath.Base(lib) && regularFile(filepath.Join(dest, lib)) {
					man.Libs = append(man.Libs, lib)
				}
			}
			for _, p := range old.Packages {
				if !slices.Contains(man.Packages, p) {
					man.Packages = append(man.Packages, p)
				}
			}
		}
	}
	queue := slices.Clone(libs)
	seen := map[string]bool{}
	copied := 0
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] || coreDevLibsNever[name] || name != filepath.Base(name) {
			continue
		}
		seen[name] = true
		asked := slices.Contains(libs, name)
		if !asked {
			if _, state := elfFindLibrary(name, elf.ELFCLASS64, elf.EM_AARCH64, elsewhere); state != elfLibMissing {
				continue // the loader finds it there first anyway
			}
		}
		src := ""
		for _, dir := range from {
			if p := filepath.Join(dir, name); regularFile(p) {
				src = p
				break
			}
		}
		if src == "" {
			return fmt.Errorf("Ubuntu's packages have no %s", name)
		}
		needed, err := coreDevLibsReadNeeded(src)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if copied++; copied > coreDevLibsMaxFiles {
			return fmt.Errorf("more than %d libraries to copy", coreDevLibsMaxFiles)
		}
		if err := coreDevLibsCopy(src, filepath.Join(dest, name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !slices.Contains(man.Libs, name) {
			man.Libs = append(man.Libs, name)
		}
		queue = append(queue, needed...)
	}
	sort.Strings(man.Libs)
	sort.Strings(man.Packages)
	man.Installed = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	// The manifest last: the folder counts once it names the libraries.
	if err := linuxPkgWriteFile(filepath.Join(dest, coreDevLibsManifestName), append(data, '\n')); err != nil {
		return err
	}
	coreDevLibsForget()
	logInfo("emulators", "device libraries for RetroArch on JukaHub's glibc: %s in %s (from Ubuntu's %s)",
		strings.Join(man.Libs, ", "), dest, strings.Join(pkgs, ", "))
	return nil
}

// coreDevLibsReadNeeded checks that file is a 64-bit ARM shared library and
// returns the libraries it links.
func coreDevLibsReadNeeded(file string) ([]string, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if st.Size() > coreDevLibsMaxSize {
		return nil, fmt.Errorf("larger than %d MB", coreDevLibsMaxSize>>20)
	}
	f, err := elf.Open(file)
	if err != nil {
		return nil, fmt.Errorf("is not an arm64 library: %w", err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_AARCH64 || f.Type != elf.ET_DYN {
		return nil, errors.New("is not an arm64 library")
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(needed))
	for _, lib := range needed {
		out = append(out, filepath.Base(lib))
	}
	return out, nil
}

// coreDevLibsCopy copies src (a link is followed) to dst through a temporary
// file, so a RetroArch reading dst never sees half of it.
func coreDevLibsCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.LimitReader(in, coreDevLibsMaxSize+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// coreDevLibsStrictProblem is the last word on a build that loads only with
// JukaHub's copies (fetchCoreUpdate): every library file links must be found
// where its RetroArch looks — the runtime, the firmware's and RetroArch's own
// folders, the copies — or be linked by RetroArch itself. "" when each is;
// else the missing ones, in the load check's sentence. target is the file the
// build becomes.
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
// its library path (ra_glibc.go): the copies, when core links one of them;
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
