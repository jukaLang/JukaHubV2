package main

// portmaster_codecs.go — the audio libraries PortMaster's LÖVE runtime loads
// from the firmware, installed beside JukaHub's private C runtime when the
// firmware does not have them.
//
// PortMaster's LÖVE 11.5 runtime (runtimes/love_11.5/) carries liblove-11.5.so,
// LuaJIT, libogg, libmodplug and libtheoradec in libs.aarch64, and libs.aarch64
// in PortMaster's own folder adds libtheoradec. liblove-11.5.so also links
// libmpg123.so.0, libvorbis.so.0 and libvorbisfile.so.3, which PortMaster
// expects the firmware to provide (funcs.txt: "libraries ports often need that
// some CFWs don't have"). Stock TrimUI 1.0.4 has none of the three, so every
// LÖVE port stops at once: Minecraft's launcher menu — the port's own LÖVE UI,
// run before Java — died with "love.aarch64: error while loading shared
// libraries: libmpg123.so.0" (report JH-TKWGE5).
//
// So JukaHub installs Ubuntu's ARM64 builds of those three libraries into
// <data>/private-glibc/portmaster-codecs/ and puts that folder at the END of a
// port's LD_LIBRARY_PATH — where PortMaster itself puts its own libs.aarch64
// (funcs.txt appends PM_LIBRARY_PATH after the system's own), so a firmware
// that has a library keeps using its copy. Each package is pinned by size and
// SHA-256 and only its one expected ELF member is extracted, never a symbolic
// link; the download is about 200 KB and only what is missing is fetched. No
// firmware file is touched, and a card whose firmware has the three libraries
// downloads nothing.

import (
	"archive/tar"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	// pmCodecFolder sits beside the private C runtime, like dolphin-x11.
	pmCodecFolder = "portmaster-codecs"
	// pmCodecMarker records what the folder was built from (support only).
	pmCodecMarker = "ready.txt"
	// pmCodecVersion is written into the marker after every library landed.
	pmCodecVersion = "ubuntu-love-codecs-2026-11-27-v1"
	// pmLoveRuntimeFolder is PortMaster's LÖVE runtime.
	pmLoveRuntimeFolder = "love_11.5"
	// pmLoveRuntimeLib is the library that needs these three.
	pmLoveRuntimeLib = "liblove-11.5.so"
	// pmCodecDebTree is where Ubuntu's arm64 packages keep their libraries.
	pmCodecDebTree = "usr/lib/aarch64-linux-gnu/"
)

// pmCodecPackage is one library: the SONAME the loader looks for, the pinned
// Ubuntu package it comes from, the one member taken out of it, and both sizes.
type pmCodecPackage struct {
	name, archive, member, sha string
	size, expanded             int64
}

// The packages are Ubuntu's arm64 builds whose glibc symbols are older than
// the TrimUI firmware's (bionic's libmpg123 needs 2.27, focal's libvorbis
// 2.29; the firmware has 2.33). Sizes and SHA-256 are those of the files the
// archive publishes; a .deb that does not match is deleted and the library is
// not installed.
var pmCodecPackages = []pmCodecPackage{
	{
		name: "libmpg123.so.0", archive: "pool/universe/m/mpg123/libmpg123-0_1.22.4-1_arm64.deb",
		member: "libmpg123.so.0.41.2", sha: "c2a42cb0c631e90fd44f85e4671e95295d55ab6fd81350f1b030ca68415acbed",
		size: 111140, expanded: 241360,
	},
	{
		name: "libvorbis.so.0", archive: "pool/main/libv/libvorbis/libvorbis0a_1.3.6-2ubuntu1_arm64.deb",
		member: "libvorbis.so.0.4.8", sha: "196da1a94c8499b149d33eb3be6b89eb7ed9cc9b0ac5c98c980dde0860657c84",
		size: 79536, expanded: 165736,
	},
	{
		name: "libvorbisfile.so.3", archive: "pool/main/libv/libvorbis/libvorbisfile3_1.3.6-2ubuntu1_arm64.deb",
		member: "libvorbisfile.so.3.3.7", sha: "b55509f1b24bbb6bbbf3c69b4b41504d1b1c5a629a22b3f54b2ab7b7e51009d9",
		size: 15300, expanded: 34616,
	},
}

var pmCodecInstallMu sync.Mutex

// pmCodecDir is the installed bundle, "" when JukaHub has no data folder.
func pmCodecDir() string {
	base, err := privateGlibcBase()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, pmCodecFolder)
}

// pmCodecInstalled reports the bundle holds this library whole: a regular file
// of the size the package's member has. A half-written file never counts.
func pmCodecInstalled(p pmCodecPackage) bool {
	dir := pmCodecDir()
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, p.name))
	return err == nil && info.Mode().IsRegular() && info.Size() == p.expanded
}

// pmCodecDirReady reports at least one library is installed, so the folder is
// worth putting on a port's library path.
func pmCodecDirReady() bool {
	for _, p := range pmCodecPackages {
		if pmCodecInstalled(p) {
			return true
		}
	}
	return false
}

// pmCodecInstalledNames are the libraries the bundle holds, for the log.
func pmCodecInstalledNames() []string {
	var out []string
	for _, p := range pmCodecPackages {
		if pmCodecInstalled(p) {
			out = append(out, p.name)
		}
	}
	return out
}

// pmCodecLibPresent reports a library of this name in one of dirs.
func pmCodecLibPresent(dirs []string, name string) bool {
	for _, dir := range dirs {
		if dir != "" && regularFile(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// pmLoveRuntimeDir is PortMaster's LÖVE 11.5 folder, "" when it is not there.
func pmLoveRuntimeDir(control string) string {
	if control == "" {
		return ""
	}
	dir := filepath.Join(control, "runtimes", pmLoveRuntimeFolder)
	if regularFile(filepath.Join(dir, pmLoveRuntimeLib)) || regularFile(filepath.Join(dir, "love.aarch64")) {
		return dir
	}
	return ""
}

// pmCodecSearchDirs is where the loader would find these three libraries on a
// port launch: the LÖVE runtime's own folder, PortMaster's shared libs folders
// (funcs.txt puts them on LD_LIBRARY_PATH too) and the firmware's.
func pmCodecSearchDirs(control string) []string {
	var dirs []string
	if love := pmLoveRuntimeDir(control); love != "" {
		dirs = append(dirs, filepath.Join(love, "libs.aarch64"))
	}
	if control != "" {
		dirs = append(dirs, filepath.Join(control, "libs.aarch64"), filepath.Join(control, "libs.armhf"),
			filepath.Join(control, "libs"))
	}
	return append(dirs, pmTrimuiLibDir, "/usr/lib", "/lib")
}

// pmCodecMissing is the libraries neither the card's PortMaster nor the
// firmware has and the bundle does not hold yet. Pure apart from existence
// checks.
func pmCodecMissing(control string) []pmCodecPackage {
	dirs := pmCodecSearchDirs(control)
	var out []pmCodecPackage
	for _, p := range pmCodecPackages {
		if pmCodecInstalled(p) || pmCodecLibPresent(dirs, p.name) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// pmCodecsNeeded is what a port launch still has to fetch: nothing unless this
// is an ARM64 TrimUI whose PortMaster has the LÖVE runtime but not the three
// libraries the runtime loads from the firmware.
func pmCodecsNeeded() []pmCodecPackage {
	if !pmRuntimeApplies() {
		return nil
	}
	control := portRealControlFolder()
	if control == "" || pmLoveRuntimeDir(control) == "" {
		return nil
	}
	return pmCodecMissing(control)
}

// pmCodecEnv puts the installed bundle on a port's library path, when it has
// one.
func pmCodecEnv(env []string) []string {
	dir := pmCodecDir()
	if dir == "" || !pmCodecDirReady() {
		return env
	}
	return pmCodecPathEnv(env, dir)
}

// pmCodecPathEnv adds dir last on env's LD_LIBRARY_PATH — PortMaster's own
// place for libraries the firmware may lack (funcs.txt appends its libs after
// the system's) — so a copy the firmware has still wins.
func pmCodecPathEnv(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	cur, _ := childEnvLookup(env, "LD_LIBRARY_PATH")
	if pmPathListHas(cur, dir) {
		return env
	}
	if cur == "" {
		return mergeGameProfileEnv(env, map[string]string{"LD_LIBRARY_PATH": dir})
	}
	return mergeGameProfileEnv(env, map[string]string{"LD_LIBRARY_PATH": cur + ":" + dir})
}

// pmCodecCoversLibs reports that every library a launch could not load is one
// the bundle supplies, so the diagnosis can point at JukaHub's download
// instead of telling the player to reinstall the port.
func pmCodecCoversLibs(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		found := false
		for _, p := range pmCodecPackages {
			if strings.EqualFold(p.name, name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// pmCodecEnsure downloads and installs what pmCodecsNeeded still lists. Off the
// main thread; installBarUpdateRun is safe from it.
func pmCodecEnsure(ctx context.Context, gen uint64) error {
	missing := pmCodecsNeeded()
	if len(missing) == 0 {
		return nil
	}
	dir := pmCodecDir()
	if dir == "" {
		return errors.New("JukaHub has no data folder for them")
	}
	pmCodecInstallMu.Lock()
	defer pmCodecInstallMu.Unlock()
	// The card may have changed while this waited.
	missing = pmCodecsNeeded()
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range missing {
		if err := ctx.Err(); err != nil {
			return err
		}
		installBarUpdateRun(gen, installPhaseDownload, 0, 0)
		deb := filepath.Join(dir, "."+filepath.Base(p.archive))
		if err := linuxPkgFetch(ctx, p.archive, deb, 0, &linuxPkgSum{SHA256: p.sha, Size: p.size}, nil); err != nil {
			return fmt.Errorf("%s from Ubuntu: %w", p.name, err)
		}
		installBarUpdateRun(gen, installPhaseExtract, 0, 0)
		err := pmCodecExtract(ctx, deb, dir, p)
		_ = os.Remove(deb)
		if err != nil {
			return fmt.Errorf("%s from Ubuntu: %w", p.name, err)
		}
	}
	if err := linuxPkgWriteFile(filepath.Join(dir, pmCodecMarker), []byte(pmCodecVersion+"\n")); err != nil {
		return err
	}
	if len(pmCodecsNeeded()) > 0 {
		return errors.New("the libraries did not finish installing")
	}
	logInfo("portmaster", "LÖVE audio libraries ready in %s: %s", dir, strings.Join(pmCodecInstalledNames(), " "))
	return nil
}

// pmCodecExtract takes only the one named library out of a verified Debian
// archive. Package paths are never used as output paths, and an ELF of another
// architecture, or with another SONAME, is refused before it replaces anything.
func pmCodecExtract(ctx context.Context, deb, dir string, p pmCodecPackage) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()
	r, done, err := privateGlibcDebData(f)
	if err != nil {
		return err
	}
	defer done()
	want := pmCodecDebTree + p.member
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("package has no %s", want)
		}
		if err != nil {
			return err
		}
		if strings.TrimPrefix(h.Name, "./") != want {
			continue
		}
		if (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) || h.Size != p.expanded {
			return fmt.Errorf("%s has an unexpected type or size", want)
		}
		out, err := os.CreateTemp(dir, "."+p.name+"-")
		if err != nil {
			return err
		}
		tmp := out.Name()
		defer os.Remove(tmp)
		_, copyErr := io.CopyN(out, tr, h.Size)
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		lib, err := elf.Open(tmp)
		if err != nil {
			return err
		}
		sonames, sonameErr := lib.DynString(elf.DT_SONAME)
		valid := lib.Class == elf.ELFCLASS64 && lib.Machine == elf.EM_AARCH64 &&
			lib.Type == elf.ET_DYN && sonameErr == nil && slices.Contains(sonames, p.name)
		_ = lib.Close()
		if !valid {
			return fmt.Errorf("%s is not the expected ARM64 %s", want, p.name)
		}
		if err := os.Chmod(tmp, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, filepath.Join(dir, p.name))
	}
}
