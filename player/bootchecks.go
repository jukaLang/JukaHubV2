package main

// bootchecks.go — the real checks the glass boot card reports.
//
// The card is only worth looking at if its rows are things the launch actually
// needed to know: whether the settings loaded or had to be rescued, whether the
// card can be written to (the difference between a hub that saves and one that
// silently loses everything), which helper tools are present — a missing one
// starts downloading here, during the splash — and whether the emulator cores
// are due their background update.
//
// Every check here runs on its own goroutine (bootEnqueueAsync), in parallel
// with each other and with the render-thread queue, so none of them costs the
// splash a frame and a normal boot is no slower for having them. Downloads they
// start outlive the splash and report on the activity strip; the network
// prefetches (weather, the update feed) stay after the first frame, where
// startup.go puts them, because their TLS handshakes would compete with the
// first frame's rasterisation on the handheld's CPU.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// bootLowSpaceBytes is the free space under which the storage row warns: a
// thumbnail cache, a save state and one downloaded video do not fit below it.
const bootLowSpaceBytes = 256 << 20

// bootToolsStarted records that the boot check already started the helper-tool
// install, so the deferred health check does not run a second pass over a
// download that is still going (that pass toasted "Missing tools: … restart
// JukaHub to install" over a working download).
var bootToolsStarted atomic.Bool

// bootEnqueueChecks queues the boot card's checks and its sound step. main
// calls it before the render-thread steps, so the checks start first and run
// beside them.
func bootEnqueueChecks(config *Config) {
	// The recovery notice is read here, on the main thread: the toast that
	// consumes it runs on the main thread after the splash.
	recovered := userConfigRecoveryNotice != ""
	bootEnqueueAsync("settings", func(note func(string)) error {
		return bootCheckSettings(recovered, note)
	})
	bootEnqueueAsync("storage", bootCheckStorage)
	if safeModeAllows(SafeFeatureToolInstall) {
		bootEnqueueAsync("tools", bootCheckTools)
	}
	if safeModeAllows(SafeFeatureUpdates) {
		// EMU's scheduler reads its setting from config, so it starts on the
		// main thread: as a queued step on the handheld (with a row), straight
		// away elsewhere, where a desktop RetroArch manages its own cores and
		// the row would only ever say "nothing to do".
		if IsTrimuiDevice() {
			bootEnqueue("cores", func() { bootCheckCores(config) })
		} else {
			startCoreAutoUpdate(config)
		}
	}
	if alarmVolumeMarkerExists() {
		bootEnqueueAsync("alarm-volume", alarmVolumeRecover)
	}
	// The UI sounds: their settings' live value now (the switch itself is the
	// Settings hub's "Interface sounds" row), the device and the rendered
	// buffers on a goroutine (opening an audio device can wait on a sound
	// daemon).
	bootEnqueue("sounds", func() {
		bootSfxBind(config)
		go bootSfxPrepare()
	})
}

// bootCheckSettings reports how the configuration came up: as saved, rescued
// from its backup, or on defaults after a failed load. None of these stop the
// boot; the row says which one happened.
func bootCheckSettings(userRecovered bool, note func(string)) error {
	configHealth.mu.Lock()
	loadErrs, restores, loadOK := configHealth.LoadErrors, configHealth.BackupRestores, configHealth.LastLoadOK
	configHealth.mu.Unlock()
	switch {
	case restores > 0 || userRecovered:
		note("restored from backup")
	case loadErrs > 0 && !loadOK:
		note("defaults in use")
	default:
		note("all good")
	}
	return nil
}

// bootStorageNoun is what the storage row calls the app's folder: the SD card
// on the handheld, the disk on a desktop.
func bootStorageNoun() string {
	if IsWindows() {
		return "disk"
	}
	return "card"
}

// bootCheckStorage writes and removes a probe file and reads the free space the
// Tools screen shows. A card that refuses the write fails the row: the hub
// still opens, but nothing it saves will stick, and the user should know.
func bootCheckStorage(note func(string)) error {
	noun := bootStorageNoun()
	probe := filepath.Join(".", ".jukahub-boot-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		note(noun + " is read-only")
		return fmt.Errorf("storage probe: %w", err)
	}
	_, werr := f.WriteString("jukahub\n")
	cerr := f.Close()
	_ = os.Remove(probe)
	if werr != nil || cerr != nil {
		note(noun + " refused a write")
		return fmt.Errorf("storage probe: %w", errors.Join(werr, cerr))
	}

	free, err := freeBytes(".")
	switch {
	case err != nil || free <= 0:
		note(noun + " is writable")
	case free < bootLowSpaceBytes:
		note("only " + bootFormatBytes(free) + " free")
		ShowToast(fmt.Sprintf("The %s is almost full (%s free) — downloads and saves may fail", noun, bootFormatBytes(free)), ToastKindWarning)
	default:
		note(bootFormatBytes(free) + " free")
	}
	return nil
}

// bootCheckTools inventories the helper tools and starts the auto-install for
// whatever is missing. The download runs on its own goroutine: the card reports
// that it started, the activity strip reports how far it got.
//
// IsOnline cannot answer during boot (the monitor has not probed yet, and an
// unprobed monitor says online), so a cheap local fact decides instead: with no
// network interface holding an address, a download could only fail and toast an
// error on every cold boot. The deferred health check and the network monitor's
// reconnect handler install the tools once Wi-Fi is up.
func bootCheckTools(note func(string)) error {
	ready, missing := 0, 0
	for _, tool := range autoInstallTools {
		if _, ok := checkToolPresence(tool); ok {
			ready++
		} else {
			missing++
		}
	}
	if missing == 0 {
		note(fmt.Sprintf("%d ready", ready))
		return nil
	}
	if localIPv4() == "" {
		note(fmt.Sprintf("%d missing · offline", missing))
		return nil
	}
	bootToolsStarted.Store(true)
	note(fmt.Sprintf("downloading %d", missing))
	go refreshToolHealth()
	return nil
}

// bootDeferredToolHealth is the deferred startup task that replaces a bare
// startToolHealthCheck: it stands down when the boot check already started the
// install, and otherwise runs the usual delayed pass (offline launches get its
// "connect WiFi" message once the monitor knows).
func bootDeferredToolHealth() {
	if bootToolsStarted.Load() {
		return
	}
	startToolHealthCheck()
}

// bootCheckCores starts the emulator cores' background updater — EMU's
// startCoreAutoUpdate, which decides for itself when a pass is due and waits
// until the interface is up — and says what it will do. It replaced the
// deferred "core-update" task, so the scheduler is started exactly once.
func bootCheckCores(config *Config) {
	startCoreAutoUpdate(config)
	startPPSSPPAutoUpdate(config)
	switch {
	case !emuAutoUpdateEnabled(config):
		bootStepNote("auto-update off")
	case localIPv4() == "":
		bootStepNote("will check when online")
	case coreUpdateModeFor(config) == coreUpdateInstall:
		bootStepNote("auto-update scheduled")
	default:
		bootStepNote("update check scheduled")
	}
}

// bootFormatBytes renders a card size the way the Tools screen does.
func bootFormatBytes(n int64) string {
	const gib = 1 << 30
	switch {
	case n >= 10*gib:
		return fmt.Sprintf("%.0f GB", float64(n)/gib)
	case n >= gib:
		return fmt.Sprintf("%.1f GB", float64(n)/gib)
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}
