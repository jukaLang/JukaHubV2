package main

// wine_weston_libs.go — the one library WestonPack's Weston links that neither
// its image nor a stock TrimUI firmware carries.
//
// Two launches on a Smart Pro, 2026-11-26 (JH-MSD7AY "Couldn't launch Notepad++
// (64-bit)", JH-JGRR6Q "Couldn't launch Wine Test (built in)"), ended as
// "Weston stopped while it was starting". The app's logs had Weston's own
// words:
//
//	[20:30:58.488] Loading module '…/lib_aarch64/libweston-14/xwayland.so'
//	[20:30:58.500] Failed to load module: libjpeg.so.8: cannot open shared object file
//	display: … ending the session
//
// and, in the OpenGL mode, the same for headless-backend.so followed by
// "fatal: failed to create compositor backend". Every WestonPack mode loads
// xwayland.so — westonwrap starts Weston with --xwayland — so no Windows
// program could start at all, whichever mode it asked for.
//
// The image (PortMaster's weston_pkg_0.2.aarch64.squashfs) carries libjpeg.so.62
// and every other library its modules link (libcairo, libwebp, libpng16,
// libpixman, the X libraries): libjpeg.so.8 — the libjpeg 8 / libjpeg-turbo
// ABI, which the modules import as the versioned symbols jpeg_*@LIBJPEG_8.0 —
// is one WestonPack takes from the firmware. ROCKNIX, ArkOS and muOS have it; a
// stock TrimUI firmware does not, which PS2 setup already knew from the other
// side: the libretro buildbot's ARMSX2 core links libjpeg.so.8 and is refused
// for it (ps2_setup.go).
//
// JukaHub answers it the way it answers the device libraries a desktop build of
// a core links (coredevstub.go): with a stand-in it writes itself, exporting
// exactly what the modules import from the library. Weston calls into libjpeg
// only where it reads an image — a capture — which no Wine program asks for, so
// the library has to load, not decode: jpeg_read_header answers "tried, not a
// JPEG", the rest 0. The promise is the same as for RetroArch's stand-ins: a
// library that answers "not here" is not a library that claims to work.
//
// Two launches on 2026-11-27 (JH-6G5TZF "Couldn't launch Notepad++ (32-bit)",
// JH-2S4CSH "Couldn't launch Wine Test (built in)") ended the same way for the
// other reason: the library is here, but too old. WestonPack's libpng16.so.16 —
// which libcairo, and through it xwayland.so and headless-backend.so, link —
// asks zlib for inflateValidate, the one function zlib 1.2.9 added, and a stock
// TrimUI firmware loads zlib 1.2.8:
//
//	Failed to load module: …/libpng16.so.16: undefined symbol: inflateValidate, version ZLIB_1.2.9
//
// Nothing is missing, so a stand-in for nothing would not help: zlib is used
// for real by everything else in the session (libpng's inflate, libcairo's
// crc), and a stub for it would decode nothing. JukaHub completes the library
// instead. The shim it writes is named and sonamed like the firmware's own file
// — so the loader takes it for libz.so.1 and every version requirement checked
// against that name finds the names zlib ever used — and names the firmware's
// file in DT_NEEDED, so the loader brings it in beside the shim: the calls the
// shim exports are JukaHub's answers, every other call runs the firmware's own
// code. Only libraries whose missing calls can safely be answered that way are
// completed (zlib's inflateValidate only turns on a header check); the gap in
// any other library is reported in the log and left to the loader.
//
// The stand-ins go in data/windows-runtime/weston-libs, not the cores' devlibs
// folder (a stand-in for libjpeg there would stand in for a port's real one),
// and the folder is appended to the session's LD_LIBRARY_PATH: last after the
// image's own libraries, so a firmware that carries the real library keeps it,
// and ahead of the default folders only because it was written when the loader
// found the library nowhere.

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// westonStubDirName is the folder of JukaHub's stand-ins under
// windowsRuntimeDir.
const westonStubDirName = "weston-libs"

// westonStubLibs are the libraries JukaHub stands in for a Weston session, by
// file name: which unversioned imports are the library's, and what each
// function answers. A versioned import (jpeg_*@LIBJPEG_8.0) names its library
// itself and needs no prefix.
var westonStubLibs = map[string]devStubLib{
	"libjpeg.so.8": {prefixes: []string{"jpeg_", "jcopy_", "jinit_"}, ret: westonStubJpegRet},
}

// westonStubJpegRet is what libjpeg's stand-in returns: JPEG_HEADER_TRIED (2)
// from jpeg_read_header, which is how a decoder says "this is not a JPEG", and
// 0 (FALSE, NULL, no scanlines) from everything else.
func westonStubJpegRet(name string) devStubRet {
	if name == "jpeg_read_header" {
		return devStubRet{value: 2}
	}
	return devStubRet{}
}

// westonCompatLib is a library this firmware carries but too old for a
// WestonPack module, and what JukaHub answers for it.
//
// versions are every version name the library's own project ever gave its
// symbols (zlib's zlib.map, oldest first). The shim defines them all: a
// requirement the loader checks against the name libz.so.1 is answered by
// whichever file carries that name, which is the shim, while the functions
// themselves stay the firmware's.
//
// prefixes claim the module's unversioned imports for the library.
//
// ret is what a function the firmware's copy does not have returns.
type westonCompatLib struct {
	versions []string
	prefixes []string
	ret      func(string) devStubRet
}

// westonCompatLibs are the libraries JukaHub completes this way, by file name.
var westonCompatLibs = map[string]westonCompatLib{
	// libz: libpng16 asks 1.2.9's inflateValidate, the only function its
	// version table adds. It returns Z_OK and nothing else: it turns zlib's own
	// header check on, which a PNG that libpng reads anyway supplies.
	"libz.so.1": {
		versions: []string{"ZLIB_1.2.0", "ZLIB_1.2.3.3", "ZLIB_1.2.3.4", "ZLIB_1.2.5", "ZLIB_1.2.9"},
		prefixes: []string{"inflate", "deflate", "crc32", "adler32", "zlib", "uncompress", "compress", "gz"},
		ret:      func(string) devStubRet { return devStubRet{} },
	},
}

// westonCompatWant is one library a session needs completed: the file this
// firmware has, the functions the session asks of it that it does not export,
// and the module whose libraries asked (for the log).
type westonCompatWant struct {
	path   string
	syms   []devStubSym
	module string
}

// westonCompatScanLimit bounds the library closure a session's gaps are looked
// for in.
const westonCompatScanLimit = 96

// westonCompatFile is one library of that closure: its file, and the module the
// walk reached it from.
type westonCompatFile struct {
	path, module string
}

// westonStubDir is where a session's stand-ins live.
func westonStubDir() string {
	return filepath.Join(windowsRuntimeDir(), westonStubDirName)
}

// westonSessionModules are the modules Weston loads for the four words of a
// mode: its backend, its GL renderer when the mode asks for one, its shell, and
// Xwayland — which westonwrap starts Weston with in every mode (--xwayland).
// Words outside the modes the wrapper documents are left out: westonwrap then
// names a module nothing has, and Weston says so itself.
func westonSessionModules(words []string) []string {
	var names []string
	if len(words) == 4 {
		switch words[0] {
		case "drm", "headless", "wayland", "x11":
			names = append(names, words[0]+"-backend.so")
		}
		if words[1] == "gl" {
			names = append(names, "gl-renderer.so")
		}
		if words[2] != "" {
			names = append(names, words[2]+"-shell.so")
		}
	}
	return append(names, "xwayland.so")
}

// westonModuleFile finds one of those modules in a WestonPack tree: its
// backends, renderers and Xwayland under lib_aarch64/libweston-*, its shells
// under lib_aarch64/weston. "" when the tree does not have it.
func westonModuleFile(root, name string) string {
	if root == "" || name == "" {
		return ""
	}
	base := filepath.Join(root, "lib_aarch64")
	dirs := []string{base}
	// The image names the folder after the Weston it was built from
	// (libweston-14); westonwrap finds it with a glob of its own.
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "libweston-") {
				dirs = append(dirs, filepath.Join(base, e.Name()))
			}
		}
	}
	dirs = append(dirs, filepath.Join(base, "weston"))
	for _, dir := range dirs {
		if path := filepath.Join(dir, name); regularFile(path) {
			return path
		}
	}
	return ""
}

// westonLibraryDirs are the folders the session's loader reads, in the order
// westonwrap and the loader reach them: the image's own libraries and the
// graphics folder the backend takes its EGL and GBM from (symlinks to the
// firmware's libmali), the session's LD_LIBRARY_PATH, then the firmware's
// default folders. The module's own folder comes first, in the caller.
func westonLibraryDirs(root string, env []string) []string {
	base := filepath.Join(root, "lib_aarch64")
	dirs := []string{
		base,
		filepath.Join(base, "extra_wayland"),
		filepath.Join(root, "lib"),
		filepath.Join(root, "lib64"),
		filepath.Join(root, "usr", "lib"),
	}
	if entries, err := os.ReadDir(filepath.Join(base, "graphics")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(base, "graphics", e.Name()))
			}
		}
	}
	if path, ok := childEnvLookup(env, "LD_LIBRARY_PATH"); ok {
		for _, dir := range strings.Split(path, childEnvListSep) {
			if dir != "" {
				dirs = append(dirs, dir)
			}
		}
	}
	return append(dirs, elfSystemLibDirs...)
}

// westonEnsureWestonLibs is westonModuleLibs for a session: the modules the
// mode's words load from the WestonPack tree at root, the stand-ins they need
// written into westonStubDir. dir is the folder to put on the session's library
// path ("" when there is nothing to add); unavailable names the libraries
// nothing could supply, for the log.
func westonEnsureWestonLibs(root string, words []string, env []string, log wineLog) (dir string, unavailable []string, err error) {
	if root == "" {
		return "", nil, nil
	}
	return westonModuleLibs(root, westonStubDir(), westonSessionModules(words), env, log)
}

// westonModuleLibs reads every module named, looks its DT_NEEDED up where the
// session's loader will, and writes a stand-in into stubDir for each library
// that is missing everywhere and that westonStubLibs knows how to answer. dir
// is stubDir when it now holds them all, "" otherwise; unavailable are the
// missing libraries JukaHub has no stand-in for (it only reports those: a
// library the loader might yet find another way must not stop a launch).
func westonModuleLibs(root, stubDir string, names, env []string, log wineLog) (dir string, unavailable []string, err error) {
	dirs := westonLibraryDirs(root, env)
	versions := map[string][]string{}
	syms := map[string][]devStubSym{}
	var want []string
	for _, name := range names {
		path := westonModuleFile(root, name)
		if path == "" {
			continue
		}
		f, openErr := elf.Open(path)
		if openErr != nil {
			continue
		}
		if needed, libErr := f.ImportedLibraries(); libErr == nil {
			for _, lib := range needed {
				base := filepath.Base(lib)
				if westonLoaderProvided(base) {
					continue
				}
				search := append([]string{filepath.Dir(path)}, dirs...)
				if _, state := elfFindLibrary(lib, f.Class, f.Machine, search); state != elfLibMissing {
					continue
				}
				info, known := westonStubLibs[base]
				if !known {
					if !slices.Contains(unavailable, base) {
						unavailable = append(unavailable, base)
					}
					continue
				}
				libVersions, libSyms := devStubNeedsOf(f, base, info)
				versions[base] = append(versions[base], libVersions...)
				syms[base] = append(syms[base], libSyms...)
				if !slices.Contains(want, base) {
					want = append(want, base)
				}
			}
		}
		_ = f.Close()
	}
	// The gaps in the libraries that are here. A module names none of them
	// itself: WestonPack's xwayland.so imports no zlib function, its
	// libpng16.so.16 does, two libraries down.
	compat := westonCompatGaps(root, dirs, names)
	westonDropCompatShims(stubDir, compat)
	if (len(want) == 0 && len(compat) == 0) || stubDir == "" {
		return "", unavailable, nil
	}
	if mkErr := os.MkdirAll(stubDir, 0o755); mkErr != nil {
		return "", unavailable, fmt.Errorf("prepare %s for WestonPack's libraries: %w", stubDir, mkErr)
	}
	var wrote []string
	complete := true
	for _, lib := range want {
		libVersions := westonSortedUnique(versions[lib])
		libSyms := westonUniqueSyms(syms[lib])
		path := filepath.Join(stubDir, lib)
		if westonStubCovers(path, libVersions, libSyms) {
			continue
		}
		data, buildErr := devStubBuild(lib, libVersions, libSyms, westonStubLibs[lib].ret)
		if buildErr != nil {
			log.printf("display: a stand-in for %s: %v\n", lib, buildErr)
			complete = false
			continue
		}
		// Through a temporary file and a rename (linuxPkgWriteFile): a
		// session that has the old one open keeps it whole.
		if writeErr := linuxPkgWriteFile(path, data); writeErr != nil {
			log.printf("display: a stand-in for %s in %s: %v\n", lib, stubDir, writeErr)
			complete = false
			continue
		}
		wrote = append(wrote, fmt.Sprintf("%s (%d functions)", lib, len(libSyms)))
	}
	if len(wrote) > 0 {
		log.printf("display: WestonPack's Weston links %s and this firmware has no copy; wrote JukaHub's stand-in in %s\n",
			strings.Join(wrote, ", "), stubDir)
	}
	for _, lib := range westonSortedNames(compat) {
		want := compat[lib]
		answer := westonCompatLibs[lib]
		libVersions := westonSortedUnique(answer.versions)
		libSyms := westonUniqueSyms(want.syms)
		path := filepath.Join(stubDir, lib)
		if !westonCompatCovers(path, libVersions, libSyms, want.path) {
			data, buildErr := devStubBuildNeeding(lib, []string{want.path}, libVersions, libSyms, answer.ret)
			if buildErr != nil {
				log.printf("display: a shim for %s: %v\n", lib, buildErr)
				complete = false
				continue
			}
			if writeErr := linuxPkgWriteFile(path, data); writeErr != nil {
				log.printf("display: a shim for %s in %s: %v\n", lib, stubDir, writeErr)
				complete = false
				continue
			}
		}
		log.printf("display: %s asks %s for %s, which this firmware's copy does not have; JukaHub's %s in %s answers it, with the firmware's library underneath\n",
			want.module, lib, westonSymVersions(libSyms), lib, stubDir)
	}
	if !complete {
		return "", unavailable, nil
	}
	return stubDir, unavailable, nil
}

// westonCompatGaps walks the libraries the modules load — the modules, their
// DT_NEEDED, and theirs — and reports the ones that are here but do not export
// what is asked of them, for the libraries JukaHub knows how to complete
// (westonCompatLibs). Anything else is left to the loader: a library may still
// be newer somewhere the loader looks, and JukaHub must not replace what it
// does not understand.
func westonCompatGaps(root string, dirs, names []string) map[string]westonCompatWant {
	out := map[string]westonCompatWant{}
	var queue []westonCompatFile
	for _, name := range names {
		if path := westonModuleFile(root, name); path != "" {
			queue = append(queue, westonCompatFile{path: path, module: name})
		}
	}
	seen := map[string]bool{}
	for steps := 0; len(queue) > 0 && steps < westonCompatScanLimit; steps++ {
		item := queue[0]
		queue = queue[1:]
		if seen[item.path] {
			continue
		}
		seen[item.path] = true
		f, openErr := elf.Open(item.path)
		if openErr != nil {
			continue
		}
		class, machine := f.Class, f.Machine
		search := append([]string{filepath.Dir(item.path)}, dirs...)
		imports := westonImportsByLib(f)
		needed, libErr := f.ImportedLibraries()
		_ = f.Close()
		if libErr == nil {
			for _, lib := range needed {
				base := filepath.Base(lib)
				// The firmware's own libraries import nothing JukaHub
				// completes (and libc is large to read twice).
				if westonLoaderProvided(base) || elfWellKnownSystemLib(base) {
					continue
				}
				if found, state := elfFindLibrary(lib, class, machine, search); state == elfLibFound && !seen[found.path] {
					queue = append(queue, westonCompatFile{path: found.path, module: item.module})
				}
			}
		}
		for _, lib := range westonSortedNames(imports) {
			base := filepath.Base(lib)
			if _, known := westonCompatLibs[base]; !known {
				continue
			}
			found, state := elfFindLibrary(lib, class, machine, search)
			if state != elfLibFound {
				continue
			}
			missing, readable := westonLibGap(found.path, imports[lib])
			if !readable || len(missing) == 0 {
				continue
			}
			want := out[base]
			want.path, want.module = found.path, item.module
			want.syms = append(want.syms, missing...)
			out[base] = want
		}
	}
	return out
}

// westonImportsByLib is the functions a file imports from each library its
// version table names, plus the unversioned ones a compat library's prefixes
// claim (a library built without symbol versions names no library itself).
func westonImportsByLib(f *elf.File) map[string][]devStubSym {
	symbols, err := f.ImportedSymbols()
	if err != nil {
		return nil
	}
	out := map[string][]devStubSym{}
	for _, s := range symbols {
		switch {
		case s.Library != "":
			out[s.Library] = append(out[s.Library], devStubSym{name: s.Name, version: s.Version})
		case s.Version == "":
			for base, info := range westonCompatLibs {
				if devStubClaims(s.Name, info.prefixes) {
					out[base] = append(out[base], devStubSym{name: s.Name})
				}
			}
		}
	}
	return out
}

// westonLibGap reports the functions a file imports from the library at path
// that the library does not export: what a shim has to answer. readable is
// false when the file cannot be read as ELF, and then nothing is reported.
func westonLibGap(path string, syms []devStubSym) (missing []devStubSym, readable bool) {
	exports, ok := westonLibraryExports(path)
	if !ok {
		return nil, false
	}
	for _, s := range syms {
		if !exports[s.name] {
			missing = append(missing, s)
		}
	}
	return missing, true
}

// westonExportsEntry is one library's exported functions, with the file's size
// and time so a changed library is read again.
type westonExportsEntry struct {
	size  int64
	mod   time.Time
	names map[string]bool
}

// westonExportsCache holds the firmware's libraries' exports once per process.
var westonExportsCache struct {
	mu   sync.Mutex
	libs map[string]westonExportsEntry
}

// westonLibraryExports is the set of names the library at path defines.
func westonLibraryExports(path string) (map[string]bool, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, false
	}
	westonExportsCache.mu.Lock()
	if hit, ok := westonExportsCache.libs[path]; ok && hit.size == st.Size() && hit.mod.Equal(st.ModTime()) {
		westonExportsCache.mu.Unlock()
		return hit.names, true
	}
	westonExportsCache.mu.Unlock()
	f, err := elf.Open(path)
	if err != nil {
		return nil, false
	}
	symbols, symErr := f.DynamicSymbols()
	_ = f.Close()
	if symErr != nil {
		return nil, false
	}
	names := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		if s.Name != "" && s.Section != elf.SHN_UNDEF {
			names[s.Name] = true
		}
	}
	westonExportsCache.mu.Lock()
	if westonExportsCache.libs == nil {
		westonExportsCache.libs = map[string]westonExportsEntry{}
	}
	westonExportsCache.libs[path] = westonExportsEntry{size: st.Size(), mod: st.ModTime(), names: names}
	westonExportsCache.mu.Unlock()
	return names, true
}

// westonCompatCovers reports whether the shim at path already answers syms,
// defines versions, and keeps needed underneath: a firmware update moves the
// library, and the shim has to name the new file.
func westonCompatCovers(path string, versions []string, syms []devStubSym, needed string) bool {
	if !westonStubCovers(path, versions, syms) {
		return false
	}
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	libs, err := f.ImportedLibraries()
	return err == nil && len(libs) == 1 && libs[0] == needed
}

// westonDropCompatShims removes the shim of a library whose gap has closed (a
// firmware that now has the call): left in the folder, it would shadow the
// newer library in a session that needs the folder for something else.
func westonDropCompatShims(stubDir string, compat map[string]westonCompatWant) {
	if stubDir == "" {
		return
	}
	for lib := range westonCompatLibs {
		if _, still := compat[lib]; still {
			continue
		}
		_ = os.Remove(filepath.Join(stubDir, lib))
	}
}

// westonSortedNames sorts a map's keys, so a session's log reads the same way
// every launch.
func westonSortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// westonSymVersions names functions for the log: "inflateValidate (ZLIB_1.2.9)".
func westonSymVersions(syms []devStubSym) string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		if s.version == "" {
			out = append(out, s.name)
			continue
		}
		out = append(out, s.name+" ("+s.version+")")
	}
	return strings.Join(out, ", ")
}

// westonLoaderProvided reports a DT_NEEDED name the loader itself answers: the
// modules name the dynamic linker (ld-linux-aarch64.so.1), which is the one
// already running JukaHub's Weston and never a file on a search path, and the
// kernel's vDSO. Neither goes in the "no copy on this firmware" line.
func westonLoaderProvided(name string) bool {
	return strings.HasPrefix(name, "ld-linux") || name == "linux-vdso.so.1"
}

// westonStubCovers reports whether the library at path already defines every
// version in versions and exports every symbol in syms, for this device's
// loader: a stand-in is rewritten only when it does not.
func westonStubCovers(path string, versions []string, syms []devStubSym) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_AARCH64 {
		return false
	}
	defs := elfVersionDefs(f)
	for _, v := range versions {
		if !defs[v] {
			return false
		}
	}
	symbols, err := f.DynamicSymbols()
	if err != nil {
		return false
	}
	have := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		if s.Section != elf.SHN_UNDEF {
			have[s.Name] = true
		}
	}
	for _, s := range syms {
		if !have[s.name] {
			return false
		}
	}
	return true
}

// westonSortedUnique sorts a copy of in and drops its repeats.
func westonSortedUnique(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// westonUniqueSyms sorts a copy of syms and drops its repeats: two modules
// import the same functions from the same library.
func westonUniqueSyms(syms []devStubSym) []devStubSym {
	out := slices.Clone(syms)
	sort.Slice(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].version < out[j].version
	})
	return slices.Compact(out)
}
