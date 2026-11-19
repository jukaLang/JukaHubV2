package main

// startup_preload.go — what the first frame needs, read on the idle cores.
//
// From exec to the first frame the main thread is busy: SDL and the GPU driver,
// the config hand-over, the scenes. The A133P has three more cores that sit
// idle meanwhile, and a cold SD card answers a read on one of them as slowly as
// on the main one. So the reads and decodes the first frame would otherwise do
// on the main thread, in pure Go and touching nothing SDL owns, start here:
//
//   - the bundled Inter's bytes and its character map (home_fonts.go,
//     fonts.go): the first font open and the first label's character check
//     find both in memory; opening a size stays on the main thread (FreeType
//     is not thread-safe);
//   - the icon library (iconlang.go): every icon definition is parsed once,
//     which the first icon drawn used to pay for;
//   - the first-frame cache (startup_framecache.go): the labels and icons the
//     last start's first frame drew, so this one uploads them instead of
//     rendering them.
//
// main.go calls startupPreload once, right after InitLogging (the goroutines
// log), after the helper commands (processes of their own) and after JukaHub
// OS's session entry, so they run in the final mount namespace and folder.

import "testing"

// startupPreload starts the first frame's reads. Main thread, once.
func startupPreload() {
	if testing.Testing() {
		return
	}
	// The platform is chosen lazily and without a lock: chosen here, on the
	// main thread, before the goroutines below ask for the data folder.
	_ = P()
	go startupPreloadRun("interface font", func() { homeUIFontPreload(homeUIFontFile) })
	go startupPreloadRun("icon set", func() { iconLibGet() })
	frameCacheStart()
}

// startupPreloadRun runs one of the reads above. They start before the boot
// guard has counted this start and before the crash output is open, so a
// panic in one must not end the process there: it only costs the head start,
// and the main thread meets the same fault later, where it is counted.
func startupPreloadRun(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("startup", "reading the %s ahead stopped: %v", what, r)
		}
	}()
	fn()
}
