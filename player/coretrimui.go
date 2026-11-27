package main

// coretrimui.go — which of the buildbot's cores can work on a TrimUI handheld.
//
// Juka, 2026-11-20, on a Brick Pro: "do not show cores that will not work on
// Trimui". The Emulators page listed every core the libretro buildbot
// publishes for linux/aarch64 that the card did not have — 385 tiles, Cemu
// (Wii U) and Citra (3DS) among them — and Install on many of those could
// only end in a refusal or a game that never starts. The page's published-core
// tiles (emuPublishedCoreRows) and Get more cores (emuMoreCoresPlanFor) now
// leave a core out when coreTrimuiMisfit gives a reason:
//
//   - it plays a console no TrimUI chip can run (emuDesktopSystems: the 3DS,
//     Wii U, PS3, Vita, Switch, Xbox 360);
//   - it is a PlayStation 2 core other than ARMSX2, the PS2 core JukaHub sets
//     up here (ps2.go): Play! needs far more CPU, PCSX2 an x86-64 one;
//   - it needs what these handhelds do not have (coreTrimuiNever): desktop
//     OpenGL 4.3 (Kronos), a desktop-class CPU (Beetle Saturn, the
//     accuracy-first bsnes and higan builds), more memory and a desktop GPU
//     (Doom 3), a PSP on a USB cable (RemoteJoy), a capture card (the video
//     processor), a PC's own emulator (Dolphin's launcher core), or it is a
//     RetroArch developer test;
//   - the build the buildbot lists now was refused here before for a reason
//     no new try changes (coreBuildRefused, raGlibcRefusalMayPass): it cannot
//     load on this firmware even with JukaHub's glibc, or it is over the
//     download limit. The buildbot's next build gets its own check, and the
//     tile comes back with it;
//   - since 2026-11-26 (Juka, "If there are higher performance cores available
//     on TrimuiSmartPro, let's remove slow or unneeded cores like for N64 or
//     old version of PPSSPP"), a faster emulator for the same console plays its
//     games on this chip (coreTrimuiSlower, corePPSSPPSetUpHere): ParaLLEl N64
//     and the old Mupen64Plus core beside Mupen64Plus-Next, and RetroArch's
//     PPSSPP core beside the PPSSPP JukaHub installs here, which is newer. The
//     list offered a build that could only be the slower of two. An installed
//     core of theirs is still listed and updated (below), and the PSP tile's
//     PPSSPP card still installs RetroArch's core when you press there;
//   - since 2026-11-26 ("make sure all emulators listed can run on Trimui
//     Smart pro"), it draws with the GPU and nothing shows its build is made
//     for OpenGL ES, the only kind these firmwares' RetroArch has
//     (coreGPUNoBuildHere, coregpuoffer.go): the buildbot's N64, Dreamcast,
//     PSP, PlayStation-HW and Saturn 3D cores are desktop OpenGL builds.
//
// Dolphin and ARMSX2 have setups of their own (dolphin.go, ps2.go): their
// tiles stay while their console is on, whatever chip, since that setup picks
// a build that loads here. Installed cores are always listed: what is on the
// card is the player's, and its tile is where it is updated or removed.
//
// One more part of the load check lives here (coreDeviceLibLoadProblem, asked
// by compat.go's coreLoadProblemFile): a desktop build may link a library for
// device access — hidapi's libudev (Dolphin's Wii Remotes), libusb, BlueZ,
// libevdev — that a handheld's firmware may not have. The generic check stands
// aside for any library it cannot find, since RetroArch may bring it; these it
// does not, so such a build is refused with the library named instead of
// installing and then failing every start at RetroArch's dlopen. Where
// RetroArch starts on JukaHub's glibc, JukaHub writes stand-ins for libusb,
// libudev and libbluetooth instead (coredevlibs.go, coredevstub.go; since
// 2026-11-22, when the buildbot's Dolphin needed libusb on a Brick Pro), and
// since 2026-11-24 for the X Input extension, libXi, which the same Dolphin
// links for its X11 input backend (a Smart Pro's firmware had every other
// library that build links); libevdev it does not, so a build that links it
// stays refused.

import (
	"debug/elf"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// coreTrimuiNever lists the cores no TrimUI handheld runs, with the reason the
// log gives (by normalizeCoreName). Consoles that no TrimUI chip runs at all
// are not listed here: emuDesktopSystems answers for every core of theirs that
// emuCoreInfo knows.
var coreTrimuiNever = map[string]string{
	"cemu":                   "Wii U emulation needs a desktop-class CPU and GPU",
	"azahar":                 "3DS emulation needs far more than the TrimUI chips",
	"lime3ds":                "3DS emulation needs far more than the TrimUI chips",
	"panda3ds":               "3DS emulation needs far more than the TrimUI chips",
	"kronos":                 "Kronos draws with desktop OpenGL 4.3, which these GPUs do not have",
	"mednafen_saturn":        "Beetle Saturn needs a desktop-class CPU (Yaba Sanshiro plays Saturn here)",
	"beetle_saturn":          "Beetle Saturn needs a desktop-class CPU (Yaba Sanshiro plays Saturn here)",
	"bsnes_hd_beta":          "an accuracy-first SNES core that needs a desktop-class CPU (Snes9x plays SNES here)",
	"bsnes_mercury_accuracy": "an accuracy-first SNES core that needs a desktop-class CPU (Snes9x plays SNES here)",
	"bsnes2014_accuracy":     "an accuracy-first SNES core that needs a desktop-class CPU (Snes9x plays SNES here)",
	"higan_sfc":              "an accuracy-first SNES core that needs a desktop-class CPU (Snes9x plays SNES here)",
	"higan_sfc_balanced":     "an accuracy-first SNES core that needs a desktop-class CPU (Snes9x plays SNES here)",
	"boom3":                  "Doom 3 needs a desktop GPU and more memory than these handhelds have",
	"boom3_xp":               "Doom 3 needs a desktop GPU and more memory than these handhelds have",
	"remotejoy":              "RemoteJoy shows a real PSP connected over USB",
	"video_processor":        "the video processor shows a capture card's picture",
	"dolphin_launcher":       "it starts a PC's own Dolphin, which a handheld does not have",
}

// coreTrimuiPS2Others are the PlayStation 2 cores other than ARMSX2.
var coreTrimuiPS2Others = map[string]bool{"play": true, "pcsx2": true, "lrps2": true}

// coreTrimuiSlower lists the cores a faster emulator for the same console
// covers on this chip, with what plays its games instead (by
// normalizeCoreName). The buildbot still builds them, but nothing on a TrimUI
// handheld gains from one: each is the slow half of a pair whose other half the
// card already plays with, and listing it offered a build that would run slower
// than what is there.
var coreTrimuiSlower = map[string]string{
	"parallel_n64": "N64 plays faster in Mupen64Plus-Next here: ParaLLEl N64 draws the N64's graphics chip in software",
	"mupen64plus":  "N64 plays faster in Mupen64Plus-Next here: this core is the one it replaced",
}

// coreTrimuiSuperseded says why this handheld does not offer core, because a
// faster emulator plays its console here; "" when none does. handheld is this
// device (a PC keeps every core the buildbot publishes), ppssppReady that
// JukaHub installs its own PPSSPP for this model (corePPSSPPSetUpHere). Cheap
// and safe off the main thread.
func coreTrimuiSuperseded(core string, handheld, ppssppReady bool) string {
	if !handheld {
		return ""
	}
	core = normalizeCoreName(core)
	if why := coreTrimuiSlower[core]; why != "" {
		return why
	}
	if core == "ppsspp" && ppssppReady {
		return "PSP games play in the PPSSPP JukaHub installs for this chip (Emulators → PSP), a newer PPSSPP than this core's"
	}
	return ""
}

// corePPSSPPSetUpHere reports that JukaHub has a PPSSPP of its own for this
// model (ppsspp_managed.go), the newer build PSP games play in: RetroArch's
// PPSSPP core is then the older of two. Read once (the model does not change
// while JukaHub runs), since a published-core list asks per core.
func corePPSSPPSetUpHere() bool { return corePPSSPPReady() }

var corePPSSPPReady = sync.OnceValue(func() bool {
	if !ppssppManagedEnabled() {
		return false
	}
	platform, _ := ppssppPlatform()
	return platform != ""
})

// coreTrimuiMisfit says why core cannot work on this TrimUI handheld, "" when
// it may. crc is the CRC32 of the build the buildbot lists now ("" skips the
// look at refused builds). Cheap: a map, a table and the remembered
// refusals; safe off the main thread.
func coreTrimuiMisfit(core, crc string) string {
	core = normalizeCoreName(core)
	if core == "" {
		return ""
	}
	switch {
	case core == dolphinCoreName:
		// The Dolphin setup picks a build that loads here from sources of its
		// own (dolphin_setup.go), so the buildbot build's refusal says nothing
		// about it; while GameCube & Wii are off, the player chose so.
		if dolphinSoC() != "" && dolphinMode() == dolphinModeOff {
			return "GameCube & Wii are off on this handheld"
		}
		return ""
	case core == ps2CoreName:
		// Likewise ARMSX2, from ARMSX2's own releases (ps2_setup.go).
		if ps2SoC() != "" && !ps2PlaysHere() {
			return "PlayStation 2 is off on this handheld"
		}
		return ""
	case coreTrimuiPS2Others[core]:
		return "PS2 games play in ARMSX2 here; this core needs far more CPU, or an x86-64 one"
	case strings.HasPrefix(core, "test") || core == "advanced_tests":
		return "a RetroArch developer test, not a game core"
	default:
		if why, ok := coreTrimuiNever[core]; ok {
			return why
		}
		if system, _ := emuCoreInfo(core); system != "" {
			if sys, desktop := emuDesktopSystemFor(system); desktop {
				return sys.Reason
			}
		}
	}
	// A console whose faster emulator JukaHub already plays with
	// (coreTrimuiSlower, corePPSSPPSetUpHere).
	if why := coreTrimuiSuperseded(core, IsTrimuiDevice(), corePPSSPPSetUpHere()); why != "" {
		return why
	}
	// A GPU-rendered core whose published build is made for desktop OpenGL,
	// which no TrimUI firmware's RetroArch draws with (coregpuoffer.go).
	if why := coreGPUNoBuildHere(core); why != "" {
		return why
	}
	// What the cores workflow's survey says the buildbot's build needs, held
	// against this firmware and JukaHub's glibc (corecompat.go): a core that
	// loads neither way, with no JukaHub build of it for TrimUI, is not offered.
	if why := coreSurveyMisfit(core); why != "" {
		return why
	}
	if crc = strings.TrimSpace(crc); crc != "" {
		if reason, refused := coreBuildRefused(core, crc); refused && !raGlibcRefusalMayPass(reason, "") {
			return "this build was refused here: " + reason
		}
	}
	return ""
}

// coreDeviceLibs are the device-access libraries coreDeviceLibLoadProblem
// looks for, by the stem coreLibIs compares (libXi: X11's input devices).
var coreDeviceLibs = []string{"libudev.so", "libusb-1.0.so", "libbluetooth.so", "libevdev.so", "libXi.so"}

// coreDeviceLibLoadProblem is the device-library half of the load check (see
// the file comment): a build that links one of coreDeviceLibs loads only where
// some library folder has it — the firmware's, RetroArch's own and the card's
// (coreGLExtraDirs), then JukaHub's stand-ins for a RetroArch on its glibc
// (env.deviceDirs, coredevlibs.go) — or where the RetroArch that opens it
// links it itself. "" when the build links none of them, when each is found,
// and whenever the check cannot tell; else every one missing is named
// ("needs libusb-1.0.so.0 and libudev.so.1, which this firmware does not
// have"), so one pass of JukaHub's stand-ins covers them all. retroArch is the
// RetroArch binary that owns the target's cores folder ("" when unknown).
func coreDeviceLibLoadProblem(r io.ReaderAt, env elfLoadEnv, retroArch string) string {
	f, err := elf.NewFile(r)
	if err != nil {
		return ""
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return ""
	}
	var dirs, missing []string
	var raNeeds map[string]bool
	for _, lib := range needed {
		if !coreLibIs(lib, coreDeviceLibs...) {
			continue
		}
		if dirs == nil {
			dirs = append(append(elfRunPaths(f, env.origin), env.libDirs...), coreGLExtraDirs(retroArch)...)
			dirs = append(dirs, env.deviceDirs...)
			raNeeds = coreRetroArchNeeds(retroArch)
		}
		base := filepath.Base(lib)
		if raNeeds[base] || slices.Contains(missing, base) {
			continue
		}
		if _, state := elfFindLibrary(lib, f.Class, f.Machine, dirs); state != elfLibMissing {
			continue
		}
		missing = append(missing, base)
	}
	if len(missing) == 0 {
		return ""
	}
	return coreMissingLibsProblem(missing)
}

// coreMissingLibsProblem is the load check's sentence for libraries no folder
// has: "needs a, b and c, which this firmware does not have" (one library:
// "needs a, which …"). coreDevLibsMissing reads it back.
func coreMissingLibsProblem(libs []string) string {
	return "needs " + mlJoinAnd(libs) + ", which this firmware does not have"
}
