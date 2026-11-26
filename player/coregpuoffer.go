package main

// coregpuoffer.go — which GPU-rendered cores the handheld offers to install.
//
// Juka, 2026-11-26: "make sure all emulators listed can run on Trimui Smart
// pro". The Emulators page offered every core the libretro buildbot publishes
// for linux/aarch64 that coreTrimuiMisfit let through, GPU-rendered ones too,
// with a warning on the tile ("draws with the GPU: this build may not run
// here"). The evening before, a Smart Pro installed Yaba Sanshiro from that
// list and the load check refused it: "it is built for desktop OpenGL (needs
// libGL.so.1), and this device's RetroArch draws with OpenGL ES only". The
// buildbot builds these cores for desktop Linux, and the TrimUI firmware's
// RetroArch draws with OpenGL ES alone (the PowerVR GE8300 of the A133P
// models, a Mali on the Smart Pro S). A build that links desktop OpenGL cannot
// even be opened there; one that links no GPU library asks RetroArch for a
// context of the kind it was built for, and a desktop build asks for desktop
// OpenGL — it loads, then quits or shows nothing (the N64 core of November
// 2026, corerisk.go). Neither runs here, so neither is offered any more:
//
//   - coreTrimuiMisfit leaves out a GPU-rendered core (coreNeedsGPU) unless
//     something shows its build draws with OpenGL ES (coreGLESBuildKnown): a
//     variant named for it (…_gles2, …_gles3), JukaHub's own build for TrimUI
//     (the cores-aarch64 release, made for this firmware), or the survey of
//     the buildbot's builds (corecompat.go) finding an OpenGL ES library in it
//     and no desktop one. So the page's downloadable tiles and Get more cores
//     no longer list such a core;
//   - a console's tile with games and no core ("no emulator found") does not
//     install the buildbot's build of one either: its A hint reads Details,
//     and the press says why before anything goes online (emuGPUCoreNotHere,
//     startEmulatorInstall; planEmuInstall turns such an install down too)
//     instead of putting a core on the card that cannot run;
//   - Dolphin and ARMSX2 keep their own setups (dolphin_setup.go,
//     ps2_setup.go), which pick a build that loads here and give it a renderer
//     the device has.
//
// A GPU core already on the card — the firmware's or CrossMix's, made for this
// GPU — is the player's, and stays listed and playable. No automatic pass
// replaces it with the buildbot's build (coreGPUAutoHold); a press on its tile
// still can, and its next game is watched (corebuilds_probation.go).

import "strings"

// coreGPUNoBuildHere says why this handheld does not install the published
// build of core: a GPU-rendered core with nothing to show its build draws with
// OpenGL ES. "" when it may: not a TrimUI handheld, not a GPU-rendered core,
// Dolphin or ARMSX2 (their own setups), or a build known to be made for
// OpenGL ES. Cheap (a name, a table, the cores-aarch64 index and the survey
// fetched last); safe off the main thread.
func coreGPUNoBuildHere(core string) string {
	core = normalizeCoreName(core)
	if core == "" || IsWindows() || !IsTrimuiDevice() || !coreNeedsGPU(core) {
		return ""
	}
	if core == dolphinCoreName || core == ps2CoreName || coreGLESBuildKnown(core) {
		return ""
	}
	return "it draws with the GPU, and the libretro buildbot builds it for desktop OpenGL, while this handheld's RetroArch draws with OpenGL ES only"
}

// emuGPUCoreNotHere is what a press on an Emulators page tile says instead of
// installing core, the libretro core that provides the tile's system, when
// that core is not on the card and coreGPUNoBuildHere turns its published
// build down; "" when the press goes on (a core that is there is the player's,
// and its tile's update runs as before). Cheap enough for the footer's hint:
// the catalog's cached set of installed cores and coreGPUNoBuildHere.
func emuGPUCoreNotHere(e emuEntry, core string) string {
	core = normalizeCoreName(core)
	if core == "" || e.Status == emuAvailable {
		return ""
	}
	why := coreGPUNoBuildHere(core)
	if why == "" {
		return ""
	}
	for have := range emuInstalledCoreSet() {
		if normalizeCoreName(have) == core {
			return ""
		}
	}
	return e.Name + ": the " + coreConfigDisplayName(core) + " core is not installed here — " + why
}

// coreGLESBuildKnown reports something that shows the build an install of core
// takes draws with OpenGL ES: its name (a GLES variant), JukaHub's own build
// for TrimUI being listed (an install takes that one, emuInstallOwnBuild), or
// the survey's read of the buildbot's build (coreSurveyLinksGLES).
func coreGLESBuildKnown(core string) bool {
	core = normalizeCoreName(core)
	for _, suffix := range []string{"_gles", "_gles2", "_gles3"} {
		if strings.HasSuffix(core, suffix) {
			return true
		}
	}
	return coreOwnBuildListed(core) || coreSurveyLinksGLES(core)
}

// coreSurveyLinksGLES reports that the survey of the buildbot's builds
// (buildbot-needs.json, corecompat.go) found core linking an OpenGL ES or EGL
// library and no desktop OpenGL one: a build made for this kind of GPU. False
// when there is no survey young enough or it does not list the core.
func coreSurveyLinksGLES(core string) bool {
	core = normalizeCoreName(core)
	if core == "" || !coreSurveySupported() {
		return false
	}
	s := &coreSurveyState
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := coreSurveyDocLocked()
	if doc == nil {
		return false
	}
	e, ok := doc.Cores[core]
	if !ok {
		return false
	}
	gles := false
	for _, lib := range e.Needed {
		switch {
		case coreDesktopGLLib(lib):
			return false
		case coreGLESLib(lib):
			gles = true
		}
	}
	return gles
}
