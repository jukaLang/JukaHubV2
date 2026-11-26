package main

// dolphin_x11.go — the real X11 libraries linked into the buildbot's Dolphin
// core, even though RetroArch gives that core its graphics context on TrimUI.
// The Smart Pro firmware has none of libX11, libxcb, libXau or libXdmcp. A
// core with libX11 in DT_NEEDED cannot be opened without them, whether or not
// it ever opens an X display. JukaHub keeps these four ARM64 libraries beside
// its private C runtime and adds them only to Dolphin's RetroArch launch.
// Each Ubuntu package has a pinned size and SHA-256; only its one expected ELF
// member is extracted. The bundle marker is written last, so an interrupted
// download is retried instead of exposing a partial library chain.

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
	dolphinX11Folder  = "dolphin-x11"
	dolphinX11Marker  = "ready.txt"
	dolphinX11Version = "ubuntu-x11-2026-11-25-v1"
)

// The three Focal libraries use no glibc newer than the firmware's. Focal's
// libXdmcp links libbsd (and that links libmd), neither present on the card;
// Noble's libXdmcp instead needs glibc 2.36, which JukaHub's private runtime
// already supplies for the buildbot core. All four packages are from Ubuntu's
// ARM64 archive and their hashes are pinned to the bytes published there.
type dolphinX11Package struct {
	name, archive, member, sha string
	size, expanded             int64
}

var dolphinX11Packages = []dolphinX11Package{
	{
		name: "libX11.so.6", archive: "pool/main/libx/libx11/libx11-6_1.6.9-2ubuntu1.6_arm64.deb",
		member: "libX11.so.6.3.0", sha: "68cbdb8a44dac09acaf52505b75cbdcaf43aeeee544dd9982e4754adf865daa4",
		size: 553708, expanded: 1263920,
	},
	{
		name: "libxcb.so.1", archive: "pool/main/libx/libxcb/libxcb1_1.14-2_arm64.deb",
		member: "libxcb.so.1.1.0", sha: "88e171a33249719366a4e29330775e2ac777226f1f379557daf0777083a1fc93",
		size: 42988, expanded: 158072,
	},
	{
		name: "libXau.so.6", archive: "pool/main/libx/libxau/libxau6_1.0.9-0ubuntu1_arm64.deb",
		member: "libXau.so.6.0.0", sha: "0f19eb67992cf9a755a781502529750841d93fb8729691df02b9b16aec845b40",
		size: 7356, expanded: 14360,
	},
	{
		name: "libXdmcp.so.6", archive: "pool/main/libx/libxdmcp/libxdmcp6_1.1.5-1build1_arm64.deb",
		member: "libXdmcp.so.6.0.0", sha: "5ba5b21512f35c0369365919c2e3cf098a8f80df660de5cf797b5f5e3c8b3444",
		size: 11220, expanded: 67760,
	},
}

var dolphinX11InstallMu sync.Mutex

func dolphinX11Dir() string {
	base, err := privateGlibcBase()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, dolphinX11Folder)
}

// dolphinX11Ready is deliberately cheap: the package hashes were checked
// before extraction, and the marker was written only after all four ELF files
// landed. Sizes catch an interrupted or hand-edited file on later launches.
func dolphinX11Ready() bool {
	dir := dolphinX11Dir()
	if dir == "" {
		return false
	}
	marker, err := os.ReadFile(filepath.Join(dir, dolphinX11Marker))
	if err != nil || strings.TrimSpace(string(marker)) != dolphinX11Version {
		return false
	}
	for _, p := range dolphinX11Packages {
		info, err := os.Stat(filepath.Join(dir, p.name))
		if err != nil || !info.Mode().IsRegular() || info.Size() != p.expanded {
			return false
		}
	}
	return true
}

// dolphinX11Dirs adds the bundle only for Dolphin and only when it is whole.
// The caller adds it to a RetroArch that starts on JukaHub's private runtime.
func dolphinX11Dirs(core string) []string {
	if !dolphinIsCore(core) || !dolphinX11Ready() {
		return nil
	}
	return []string{dolphinX11Dir()}
}

// dolphinX11CoreNeeds checks an already installed core, including one copied
// onto the card by hand. JukaHub's own TrimUI build switches X11 off and does
// not need the bundle; the current libretro buildbot core links it directly.
func dolphinX11CoreNeeds(core string) bool {
	if !dolphinIsCore(core) {
		return false
	}
	f, err := elf.Open(core)
	if err != nil {
		return false
	}
	defer f.Close()
	needed, err := f.ImportedLibraries()
	return err == nil && slices.Contains(needed, "libX11.so.6")
}

// dolphinX11RefusalMayPass retries a build JukaHub refused before this bundle
// was present. The ordinary load check still decides whether it can run.
func dolphinX11RefusalMayPass(reason, target string) bool {
	return dolphinX11Ready() && raGlibcLoadDir(target) != "" &&
		strings.Contains(reason, "needs libX11.so.6")
}

// dolphinX11Ensure prepares the private runtime and the complete library
// chain for a Dolphin core in target's RetroArch folder. It runs only in a
// setup goroutine; a first download is about 600 KiB in all.
func dolphinX11Ensure(ctx context.Context, target string, phase func(string, string)) error {
	if !raGlibcWanted() || !raGlibcEnsureRuntimeWith(ctx, target, true) {
		return errors.New("RetroArch could not start on JukaHub's C runtime for Dolphin")
	}
	if dolphinX11Ready() {
		return nil
	}
	dolphinX11InstallMu.Lock()
	defer dolphinX11InstallMu.Unlock()
	if dolphinX11Ready() {
		return nil
	}
	dir := dolphinX11Dir()
	if dir == "" {
		return errors.New("JukaHub has no data folder for Dolphin's display libraries")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, dolphinX11Marker))
	for _, p := range dolphinX11Packages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if phase != nil {
			phase(installPhaseDownload, "Dolphin display libraries: "+p.name)
		}
		deb := filepath.Join(dir, filepath.Base(p.archive))
		if err := linuxPkgFetch(ctx, p.archive, deb, 0, &linuxPkgSum{SHA256: p.sha, Size: p.size}, nil); err != nil {
			return fmt.Errorf("%s from Ubuntu: %w", p.name, err)
		}
		if phase != nil {
			phase(installPhaseExtract, "Dolphin display libraries: "+p.name)
		}
		if err := dolphinX11Extract(ctx, deb, dir, p); err != nil {
			return fmt.Errorf("%s from Ubuntu: %w", p.name, err)
		}
		_ = os.Remove(deb)
	}
	if err := linuxPkgWriteFile(filepath.Join(dir, dolphinX11Marker), []byte(dolphinX11Version+"\n")); err != nil {
		return err
	}
	if !dolphinX11Ready() {
		return errors.New("Dolphin's display libraries did not finish installing")
	}
	logInfo("library", "GameCube: Dolphin's four X11 libraries are ready in %s", dir)
	return nil
}

// dolphinX11Extract takes only the one named library in a verified Debian
// archive. Package paths are never used as output paths, and an ELF with the
// wrong architecture or SONAME is refused before it replaces anything.
func dolphinX11Extract(ctx context.Context, deb, dir string, p dolphinX11Package) error {
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
	tr := tar.NewReader(r)
	want := "usr/lib/aarch64-linux-gnu/" + p.member
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
