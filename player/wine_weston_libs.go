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
	if len(want) == 0 || stubDir == "" {
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
	if !complete {
		return "", unavailable, nil
	}
	return stubDir, unavailable, nil
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
