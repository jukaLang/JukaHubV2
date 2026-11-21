package main

// corerisk.go — which libretro cores draw through RetroArch's GPU context.
//
// Most cores hand RetroArch a finished picture in memory, and any build of
// them runs under any RetroArch. A core that renders with the GPU instead
// (GLideN64 in Mupen64Plus-Next, Flycast, PPSSPP, the "HW" PlayStation core…)
// asks RetroArch for an OpenGL, OpenGL ES or Vulkan context of a particular
// kind. The TrimUI firmware's RetroArch (ra64.trimui) only has OpenGL ES, and
// the libretro buildbot's generic linux/aarch64 builds are made for desktop
// Linux, where the GPU context is usually desktop OpenGL. Such a build can load
// and then show nothing, or quit straight back to the menu, while the build the
// firmware or CrossMix shipped — made for this GPU — worked.
//
// The update pass (emulatorupdates.go, emulatorautoupdate.go) and the "more
// cores" downloads (emumorecores.go) both ask this before they put a buildbot
// build in front of the player.
//
// What a build file itself can tell, without running it, is the GPU library
// it links (DT_NEEDED): libGL.so.1 is desktop OpenGL, libGLESv2.so.2 OpenGL ES,
// libvulkan.so.1 Vulkan. The handheld's firmware has EGL and GLES only (the
// PowerVR GE8300 on the Smart Pro and Brick), so a build that needs libGL.so.1
// cannot even be opened there: RetroArch's dlopen fails ("libGL.so.1: cannot
// open shared object file") and the game is back at JukaHub in a second.
// coreGLLoadProblem turns that into a refusal before anything is replaced
// (compat.go's coreLoadProblemFile, the one load check every activation
// passes). A build that links no GPU library at all fetches its GL functions
// through RetroArch and says nothing about which kind of context it will ask
// for — that is the case only a run can settle, which is why an automatic pass
// leaves a GPU core the firmware shipped alone (coreGPUAutoHold) and why every
// activation is watched through its first game (corebuilds_probation.go).

import (
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// coreGPURendered lists the cores (by normalizeCoreName) whose picture comes
// from RetroArch's GPU context, by default or as their usual renderer.
var coreGPURendered = map[string]bool{
	"mupen64plus_next":       true,
	"mupen64plus_next_gles2": true,
	"mupen64plus_next_gles3": true,
	"mupen64plus":            true,
	"parallel_n64":           true,
	"flycast":                true,
	"flycast_gles2":          true,
	"reicast":                true,
	"ppsspp":                 true,
	"mednafen_psx_hw":        true,
	"beetle_psx_hw":          true,
	"dolphin":                true,
	"citra":                  true,
	"kronos":                 true,
	"yabasanshiro":           true,
	"play":                   true,
	"pcsx2":                  true,
	"lrps2":                  true,
	"armsx2":                 true,
	"openlara":               true,
	"vitaquake3":             true,
	"boom3":                  true,
	"boom3_xp":               true,
}

// coreNeedsGPU reports whether core renders through RetroArch's GPU context,
// so a generic buildbot build of it may not run under the handheld's OpenGL ES
// RetroArch even when it loads.
func coreNeedsGPU(core string) bool {
	core = normalizeCoreName(core)
	if coreGPURendered[core] {
		return true
	}
	// Variants named after their GPU API ("…_gles3", "…_vulkan") are GPU
	// cores by definition.
	for _, suffix := range []string{"_gles", "_gles2", "_gles3", "_gl", "_glcore", "_vulkan"} {
		if strings.HasSuffix(core, suffix) {
			return true
		}
	}
	return false
}

// coreLibIs reports whether a DT_NEEDED name is one of stems ("libGL.so" is
// libGL.so and libGL.so.1, never libGLESv2.so.2).
func coreLibIs(name string, stems ...string) bool {
	base := filepath.Base(name)
	for _, stem := range stems {
		if base == stem || strings.HasPrefix(base, stem+".") {
			return true
		}
	}
	return false
}

// coreDesktopGLLib is a desktop OpenGL library: what a desktop Linux build
// links and the handheld's firmware does not have.
func coreDesktopGLLib(name string) bool {
	return coreLibIs(name, "libGL.so", "libOpenGL.so", "libGLX.so")
}

// coreGLESLib is an OpenGL ES (or EGL) library: what a build made for this
// GPU links.
func coreGLESLib(name string) bool {
	return coreLibIs(name, "libGLESv2.so", "libGLESv1_CM.so", "libGLESv3.so", "libEGL.so")
}

// coreGPUAPIOf names the GPU library a core file links, for the log and the
// diagnosis card: "desktop OpenGL (libGL.so.1)", "OpenGL ES (libGLESv2.so.2)",
// "Vulkan (libvulkan.so.1)", "no GPU library linked", or "" when the file is
// not an ELF build this can read.
func coreGPUAPIOf(path string) string {
	fh, err := os.Open(path)
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
	var gl, gles, vk string
	for _, lib := range needed {
		switch {
		case coreDesktopGLLib(lib):
			gl = lib
		case coreGLESLib(lib) && gles == "":
			gles = lib
		case coreLibIs(lib, "libvulkan.so") && vk == "":
			vk = lib
		}
	}
	switch {
	case gl != "":
		return "desktop OpenGL (" + gl + ")"
	case gles != "":
		return "OpenGL ES (" + gles + ")"
	case vk != "":
		return "Vulkan (" + vk + ")"
	}
	return "no GPU library linked"
}

// coreGLExtraDirs are the folders a RetroArch install keeps libraries in
// beside the system's: its own folder and lib/ (CrossMix's and the stock
// layouts), the card's System/lib, and the multiarch folders a desktop's
// loader reads, so a desktop with libGL is never told it lacks it.
func coreGLExtraDirs(retroArch string) []string {
	var out []string
	if retroArch != "" {
		root := filepath.Dir(retroArch)
		out = append(out, root, filepath.Join(root, "lib"), filepath.Join(root, "lib64"),
			filepath.Join(root, ".retroarch", "lib"))
	}
	out = append(out, filepath.Join(childEnvFirmwareCard(), "System", "lib"),
		"/usr/lib/aarch64-linux-gnu", "/lib/aarch64-linux-gnu",
		"/usr/lib/x86_64-linux-gnu", "/lib/x86_64-linux-gnu", "/usr/local/lib")
	return out
}

// coreGLLoadProblem is the GPU half of the load check: a core that links a
// desktop OpenGL library loads only where some library folder has it or where
// the RetroArch that opens it has already loaded it. When neither is true the
// dlopen fails, whatever the rest of the build looks like. "" when the build
// links no desktop GL library, when the library is found, and whenever the
// check cannot tell (not ELF, an unreadable RetroArch is not held against the
// build). retroArch is the RetroArch binary that owns the target's cores
// folder ("" when unknown).
func coreGLLoadProblem(r io.ReaderAt, env elfLoadEnv, retroArch string) string {
	f, err := elf.NewFile(r)
	if err != nil {
		return ""
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return ""
	}
	var gl []string
	for _, lib := range needed {
		if coreDesktopGLLib(lib) {
			gl = append(gl, lib)
		}
	}
	if len(gl) == 0 {
		return ""
	}
	dirs := append(append(elfRunPaths(f, env.origin), env.libDirs...), coreGLExtraDirs(retroArch)...)
	raNeeds := coreRetroArchNeeds(retroArch)
	for _, lib := range gl {
		if raNeeds[filepath.Base(lib)] {
			continue
		}
		if _, state := elfFindLibrary(lib, f.Class, f.Machine, dirs); state != elfLibMissing {
			continue
		}
		return "it is built for desktop OpenGL (needs " + filepath.Base(lib) + "), and this device's RetroArch draws with OpenGL ES only"
	}
	return ""
}

// coreRetroArchNeeds is the set of libraries a RetroArch binary links
// (DT_NEEDED, by base name); empty when it is not an ELF file (a wrapper
// script) or cannot be read.
func coreRetroArchNeeds(retroArch string) map[string]bool {
	out := map[string]bool{}
	if retroArch == "" {
		return out
	}
	fh, err := os.Open(retroArch)
	if err != nil {
		return out
	}
	defer fh.Close()
	f, err := elf.NewFile(fh)
	if err != nil {
		return out
	}
	needed, _ := f.ImportedLibraries()
	for _, lib := range needed {
		out[filepath.Base(lib)] = true
	}
	return out
}

// ── What an automatic pass leaves to a press ─────────────────────────────

// coreHistoryHasBuildbot reports whether crc is a build of core that one of
// JukaHub's own updates put on the card: a history entry with a buildbot date
// (corebuilds_history.go). The firmware's or CrossMix's build is the history's
// first entry, recorded without one.
func coreHistoryHasBuildbot(core, crc string) bool {
	crc = strings.ToLower(strings.TrimSpace(crc))
	if crc == "" {
		return false
	}
	for _, e := range coreHistoryFor(normalizeCoreName(core)) {
		if strings.EqualFold(e.CRC32, crc) && e.Build != "" {
			return true
		}
	}
	return false
}

// coreGPUAutoHold reports a core an automatic pass must not replace: a
// GPU-rendered core (coreNeedsGPU) whose active build did not come from
// JukaHub's updates — the firmware's, CrossMix's, or one copied by hand. Those
// were made for this GPU; the buildbot's generic build may not draw here, and
// nothing in its file says so beforehand (see the file comment). A press on
// the Emulators page still installs it, and the probation watches its first
// game. A core JukaHub already moved to the buildbot's line keeps following
// it: the build it would replace is a buildbot build too. crc is the active
// build's CRC32 ("" when it could not be read, which also holds). Only the
// handheld holds: a PC's RetroArch has desktop OpenGL.
func coreGPUAutoHold(b coreBuild) (crc string, hold bool) {
	if IsWindows() || !coreNeedsGPU(b.Core) || strings.TrimSpace(b.Path) == "" {
		return "", false
	}
	sum, err := emuCachedCRC32(b.Path)
	if err != nil {
		return "", true
	}
	if coreHistoryHasBuildbot(b.Core, sum) {
		return sum, false
	}
	return sum, true
}

// coreGPUHeldNote is how a toast names the GPU cores an automatic pass left
// alone, the first time it leaves each build.
func coreGPUHeldNote(names []string) string {
	list := names
	more := ""
	if len(list) > 3 {
		more = " +" + itoa(len(list)-3) + " more"
		list = list[:3]
	}
	if len(names) == 1 {
		return "kept the firmware's own " + names[0] + " (it draws with this GPU; the generic build may not) — update it on the Emulators page to try"
	}
	return "kept the firmware's own " + strings.Join(list, ", ") + more + " (they draw with this GPU; the generic builds may not) — update them on the Emulators page to try"
}

// coreConsoleShort is the console a GPU core is known by, for a toast ("N64"),
// or "" when the core is not one of these.
func coreConsoleShort(core string) string {
	core = normalizeCoreName(core)
	switch {
	case strings.HasPrefix(core, "mupen64plus"), core == "parallel_n64":
		return "N64"
	case strings.HasPrefix(core, "flycast"), core == "reicast":
		return "Dreamcast"
	case core == "ppsspp":
		return "PSP"
	case strings.HasSuffix(core, "psx_hw"):
		return "PlayStation"
	case core == "kronos", core == "yabasanshiro":
		return "Saturn"
	}
	return ""
}

// coreLabelFor names a core for a toast: "N64 core (Mupen64Plus-Next)", or the
// core's display name when its console is not known here.
func coreLabelFor(core string) string {
	name := coreConfigDisplayName(core)
	if console := coreConsoleShort(core); console != "" {
		return console + " core (" + name + ")"
	}
	return name + " core"
}
