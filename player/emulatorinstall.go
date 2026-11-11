package main

// emulatorinstall.go — installing and updating an emulator from the Emulators
// page.
//
// The page could only report what the firmware already had: a Roms/ folder
// with no emulator said "no emulator found", and the only fix was copying a
// folder onto the card by hand. The buildbot channel the update checker already
// reads publishes one archive per libretro core for this exact hardware, and an
// absent core is simply a file that is not on the card yet — so the same
// channel installs as well as updates:
//
//   - Install. The tile's system has no core in RetroArch's cores folder. The
//     archive is downloaded, the CRC32 the index states is checked against the
//     binary, the build lands in the core pool and is copied into the cores
//     folder. Nothing is replaced because nothing was there.
//   - Update. The tile's system does have a core, and its bytes differ from the
//     build the index lists. The new binary replaces it and the one that was
//     running is kept beside it as a timestamped .bak — the same rule the
//     compatibility panel's activation uses, so an update can always be undone
//     by renaming one file.
//
// What this deliberately does not do on the handheld: no Emus/<system>/ folder
// is written. Those folders are the firmware's launcher scripts, and the
// standalone emulators upstream publishes are desktop Linux builds that would
// install a launcher that cannot start. A core is a single file in a folder
// RetroArch owns, which is why it is the one thing safe to install there. The
// one exception is the Xbox tile on the Smart Pro: the hakuX port is published
// as exactly such a folder, built for this firmware, and hakux.go merges it
// (and keeps what it replaces); startEmulatorInstall routes that tile there.
//
// Windows has no such firmware: the same page installs a portable RetroArch
// (then the core, from the buildbot's windows/x86_64 tree) and the standalone
// emulators themselves — see emuwininstall.go. startEmulatorInstall routes a
// tile to whichever applies.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// emuInstallAction is what a press on Install/Update would do for one tile.
type emuInstallAction int

const (
	// emuInstallNone is a tile with no core this build can install: a tool row,
	// a standalone folder with no libretro core, or a core the index dropped.
	emuInstallNone emuInstallAction = iota
	emuInstallNew
	emuInstallUpdate
	emuInstallCurrent
)

// emuCoreInstallPlatform is the shipped policy: every platform installs cores,
// each from its own buildbot channel (emuCoreIndexURL) — aarch64 shared objects
// for the handheld's firmware, x86_64 DLLs for the portable RetroArch the
// Windows page installs first when there is none. It is a variable so the
// panel tests can stand the installer down.
var emuCoreInstallPlatform = func() bool { return true }

// emuCoreInstallSupported reports whether this build can install libretro
// cores.
func emuCoreInstallSupported() bool { return emuCoreInstallPlatform() }

// emuWinStandaloneFor names the Windows emulator a tile installs: the tile's
// own emulator ("PPSSPP"), or the one a desktop-only system needs (Roms/XBOX →
// xemu). Core rows, pinned tools and handheld tiles have none.
func emuWinStandaloneFor(e emuEntry) string {
	if e.Pinned || strings.TrimSpace(e.Core) != "" {
		return ""
	}
	if via := strings.TrimSpace(e.Via); via != "" {
		return via
	}
	if spec, ok := emuWinSpecFor(e.Name); ok {
		return spec.Name
	}
	return ""
}

// emuCoreForEntry resolves the libretro core that provides a tile's system, or
// "" when the tile is not something a core can cover.
func emuCoreForEntry(e emuEntry) string {
	if core := normalizeCoreName(e.Core); core != "" {
		return core
	}
	// Pinned rows are JukaHub's own tools and launchers, never a system.
	if e.Pinned {
		return ""
	}
	return normalizeCoreName(defaultCoreForSystem(e.Name))
}

// coreIndexEntryFor finds the index line for a core. The index names cores in
// the buildbot's own spelling, which is the spelling stored on the card.
func coreIndexEntryFor(index []coreIndexEntry, core string) (coreIndexEntry, bool) {
	core = normalizeCoreName(core)
	if core == "" {
		return coreIndexEntry{}, false
	}
	for _, entry := range index {
		if entry.Core == core {
			return entry, true
		}
	}
	return coreIndexEntry{}, false
}

// emuInstallPlan is the resolved answer for one tile: which core, which build,
// and whether it is missing, stale or already current.
type emuInstallPlan struct {
	Entry   string
	Core    string
	Action  emuInstallAction
	Index   coreIndexEntry
	Current string
	Reason  string
	// ActivePath is the binary an update replaces ("" for an install).
	ActivePath string
}

// planEmuInstall decides what installing the tile would do. It reads the
// installed binary's CRC32 rather than a version string, because the buildbot
// index is a list of builds and the only identity a core carries on the card is
// its bytes.
func planEmuInstall(e emuEntry, inv coreInventory, index []coreIndexEntry) emuInstallPlan {
	plan := emuInstallPlan{Entry: e.Name, Core: emuCoreForEntry(e)}
	if plan.Core == "" {
		plan.Action = emuInstallNone
		plan.Reason = "No libretro core is known for " + e.Name
		return plan
	}
	entry, ok := coreIndexEntryFor(index, plan.Core)
	if !ok {
		plan.Action = emuInstallNone
		plan.Reason = plan.Core + " is not published for this device"
		return plan
	}
	plan.Index = entry
	active, installed := inv.Active[plan.Core]
	if !installed {
		plan.Action = emuInstallNew
		plan.Reason = "Install the " + coreConfigDisplayName(plan.Core) + " core (" + entry.Date + ")"
		return plan
	}
	plan.Current = active.Version
	plan.ActivePath = active.Path
	if coreDirIsSystem(filepath.Dir(active.Path)) {
		// The firmware's own copy, on its read-only partition: nothing here
		// replaces it (coreActivationTarget would refuse at the write).
		plan.Action = emuInstallNone
		plan.Reason = "The " + coreConfigDisplayName(plan.Core) + " core is part of the firmware and is not updated here"
		return plan
	}
	if sum, err := emuCachedCRC32(active.Path); err == nil && sum == entry.CRC32 {
		plan.Action = emuInstallCurrent
		plan.Reason = coreConfigDisplayName(plan.Core) + " core is already the " + entry.Date + " build"
		return plan
	}
	plan.Action = emuInstallUpdate
	plan.Reason = "Replace the " + coreConfigDisplayName(plan.Core) + " core with the " + entry.Date + " build"
	return plan
}

// applyEmuInstallPlan downloads the planned build into the pool and makes it the
// active core. The previous binary, when there is one, is kept as a .bak by
// activateCoreBuild. The returned backup path is "" for a first-time install.
func applyEmuInstallPlan(ctx context.Context, client *http.Client, plan emuInstallPlan, poolRoot string) (coreBuild, string, error) {
	if plan.Action != emuInstallNew && plan.Action != emuInstallUpdate {
		return coreBuild{}, "", fmt.Errorf("%s has nothing to install", plan.Entry)
	}
	upd := coreUpdate{
		Core:    plan.Core,
		Date:    plan.Index.Date,
		CRC32:   plan.Index.CRC32,
		URL:     emuCoreBaseURL() + plan.Index.File,
		Current: plan.Current,
		Active:  plan.ActivePath,
	}
	build, err := fetchCoreUpdate(ctx, client, upd, poolRoot)
	if err != nil {
		return coreBuild{}, "", err
	}
	target, backup, err := activateCoreBuild(build)
	emuCRCForget(target)
	if err != nil {
		return build, "", err
	}
	logDebug("emulators", "installed core %s %s (backup %q)", plan.Core, plan.Index.Date, backup)
	return build, backup, nil
}

// emuCoreBuildsMatch compares the core RetroArch will actually load with the
// build that was just installed. A download can succeed while the copy into
// cores/ fails (a read-only or full card), and the tile then says "installed"
// while the game still refuses to start — the least useful outcome on a device
// where the user cannot see a console.
func emuCoreBuildsMatch(active, installed coreBuild) error {
	if strings.TrimSpace(active.Path) == "" || !regularFile(active.Path) {
		return fmt.Errorf("the download finished but RetroArch cannot see the %s core — check its cores folder", coreConfigDisplayName(installed.Core))
	}
	if active.Size > 0 && installed.Size > 0 && active.Size != installed.Size {
		return fmt.Errorf("RetroArch's %s core is %s, but %s was installed — the copy into the cores folder did not take",
			coreConfigDisplayName(installed.Core), formatBytes(uint64(active.Size)), formatBytes(uint64(installed.Size)))
	}
	return nil
}

// emuConfirmInstalledCore re-reads the inventory so the confirmation describes
// the file that is actually active, not the one that was requested.
func emuConfirmInstalledCore(core string, installed coreBuild) error {
	inv := scanCoreInventory()
	active, ok := inv.Active[core]
	if !ok {
		return fmt.Errorf("the download finished but RetroArch cannot see the %s core — check its cores folder", coreConfigDisplayName(core))
	}
	return emuCoreBuildsMatch(active, installed)
}

// emuInstallReport is what one install action did.
type emuInstallReport struct {
	Installed []string
	Updated   []string
	Current   int
	Skipped   int
	Errors    []string
	// Checked reports that the build index was read. An automatic pass uses it
	// to tell "nothing to update" from "could not ask", so an offline boot does
	// not stand down for the whole cooldown.
	Checked bool
	// Refused names the cores whose new build this device refused in this run
	// (coreRefusedError: it cannot load on this firmware, or it is over the
	// download limit); RefusedNote is the first refusal's reason. KnownRefused
	// counts the cores skipped because that exact build was refused before. In
	// every case the installed core stays, and none of it is a failure.
	Refused      []string
	RefusedNote  string
	KnownRefused int
	// Interrupted reports a run stopped before it reached every core: a game
	// started, the card was cancelled, or the time ran out.
	Interrupted bool
	// LowSpace is the free space a run found when it stopped because the card
	// is nearly full ("" when it did not stop for that): the cores it did not
	// reach keep their builds, and the toast says why.
	LowSpace string
}

// summary is the one-line result shown in the toast.
func (r emuInstallReport) summary() string {
	parts := make([]string, 0, 6)
	if len(r.Installed) > 0 {
		parts = append(parts, strings.Join(r.Installed, ", ")+" installed")
	}
	if len(r.Updated) > 0 {
		parts = append(parts, strings.Join(r.Updated, ", ")+" updated")
	}
	if r.Current > 0 {
		parts = append(parts, itoa(r.Current)+" already current")
	}
	if kept := len(r.Refused) + r.KnownRefused; kept > 0 {
		parts = append(parts, itoa(kept)+" kept (the new build is not usable here)")
	}
	if len(r.Errors) > 0 {
		parts = append(parts, itoa(len(r.Errors))+" failed")
	}
	if r.LowSpace != "" {
		parts = append(parts, coreLowSpaceNote(r.LowSpace))
	} else if r.Interrupted {
		parts = append(parts, "stopped early")
	}
	if len(parts) == 0 {
		if r.Skipped > 0 {
			return "No installable emulator here"
		}
		return "Nothing to install"
	}
	return strings.Join(parts, " · ")
}

// startEmulatorInstall installs or updates what one tile needs: on Windows the
// emulator itself when the tile is (or needs) a standalone emulator, otherwise
// the libretro core that provides the tile's system — after a portable
// RetroArch when Windows has none yet. It runs off the event thread and
// reports through the install bar and a toast, the same way every other
// download action does.
func startEmulatorInstall(e emuEntry) {
	if e.Status == emuUnsupported {
		PlayNavSound()
		ShowToast(e.Name+" is not supported here — "+e.Note, ToastKindInfo)
		return
	}
	if hakuxTile(e) {
		// The handheld's Xbox tile installs (or updates) the hakuX package.
		hakuxStartInstall()
		return
	}
	if IsWindows() {
		if name := emuWinStandaloneFor(e); name != "" {
			if e.Status == emuAvailable && !e.Managed && strings.EqualFold(name, e.Name) {
				ShowToast(e.Name+" was installed outside JukaHub — update it the way it was installed", ToastKindInfo)
				return
			}
			spec, _ := emuWinSpecFor(name)
			emuWinStartInstall(spec)
			return
		}
	}
	if !emuCoreInstallSupported() {
		ShowToast("Core installs are switched off in this build", ToastKindInfo)
		return
	}
	core := emuCoreForEntry(e)
	if core == "" {
		ShowToast(e.Name+": no emulator is published for this system", ToastKindWarning)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to install "+coreConfigDisplayName(core), ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	// A Windows core needs a RetroArch to load it. Installing one first is the
	// whole of what "Install" means for a system on a fresh PC.
	needRetroArch := IsWindows() && emuWinExe("RetroArch") == ""
	if needRetroArch {
		installBarBeginLabelled("RetroArch", "Installing RetroArch and the "+coreConfigDisplayName(core)+" core")
	} else {
		installBarBegin(coreConfigDisplayName(core) + " core")
	}
	installBarPhase(installPhaseResolving)
	emuSetInstalling(e.Name)
	go func() {
		defer endPatchOp()
		defer emuSetInstalling("")
		// The timeout derives from the card's run token, so a cancel from the
		// card or the activity strip stops the transfer mid-flight.
		timeout := 10 * time.Minute
		if needRetroArch {
			timeout = emuWinInstallTimeout
		}
		ctx, cancel := context.WithTimeout(installBarRunContext(), timeout)
		defer cancel()
		client := newEmulatorUpdateClient()

		if needRetroArch {
			retroArch, _ := emuWinSpecFor("RetroArch")
			if _, err := emuWinInstall(ctx, emuWinClient(), retroArch, emuInstallBarReport); err != nil {
				installBarFail(installCardDetail(emuFriendlyError(err)))
				ShowToast("RetroArch: "+emuFriendlyError(err), ToastKindError)
				return
			}
			invalidateEmulatorCache()
			installBarResetProgress()
		}

		index, err := fetchCoreIndex(ctx, client)
		if err != nil {
			installBarFail(installCardDetail(emuFriendlyError(err)))
			ShowToast("Core index: "+emuFriendlyError(err), ToastKindError)
			return
		}
		plan := planEmuInstall(e, scanCoreInventory(), index)
		switch plan.Action {
		case emuInstallNone:
			installBarPhase(installPhaseDone)
			installBarDone()
			ShowToast(plan.Reason, ToastKindWarning)
			return
		case emuInstallCurrent:
			installBarPhase(installPhaseDone)
			installBarDone()
			emuForgetUpdate(plan.Core)
			ShowToast(plan.Reason, ToastKindInfo)
			return
		}

		installBarPhase(installPhaseDownload)
		installBarDetail(coreConfigDisplayName(plan.Core) + " " + plan.Index.Date)
		build, backup, err := applyEmuInstallPlan(ctx, client, plan, compatCorePoolDir())
		if err != nil {
			installBarFail(installCardDetail(emuFriendlyError(err)))
			msg, kind := coreConfigDisplayName(plan.Core)+": "+emuFriendlyError(err), ToastKindError
			var refused *coreRefusedError
			if errors.As(err, &refused) {
				// Not a failure of the download: this device will not take
				// the build, and the core that works is untouched.
				kind = ToastKindWarning
				if plan.Action == emuInstallUpdate {
					msg += " · the installed build is kept"
				}
			}
			ShowToast(msg, kind)
			return
		}
		installBarPhase(installPhaseDone)
		installBarDone()
		// The tile list and the compatibility panel both read the cores folder,
		// so both caches are dropped rather than waiting for the next open.
		invalidateEmulatorCache()
		invalidateCompatCache()
		emuForgetUpdate(plan.Core)
		coreBuildsRequestReload()
		// Say "installed" only when RetroArch's own folder now holds the build
		// that was downloaded: an unwritable cores folder is reported here
		// instead of as a game that will not start.
		if err := emuConfirmInstalledCore(plan.Core, build); err != nil {
			ShowToast(err.Error(), ToastKindError)
			return
		}
		trophyEvent(tkEmuInstall) // one per press, also when a RetroArch came first
		PlaySuccessSound()
		if plan.Action == emuInstallNew {
			note := coreConfigDisplayName(plan.Core) + " core installed · " + build.Version
			if needRetroArch {
				note = "RetroArch and the " + note
			}
			ShowToast(note, ToastKindSuccess)
			return
		}
		note := coreConfigDisplayName(plan.Core) + " core updated · " + build.Version
		if backup != "" {
			note += " · previous build kept"
		}
		ShowToast(note, ToastKindSuccess)
	}()
}

// runInstalledCoreUpdates brings every installed core whose bytes differ from
// the build index forward to the index's build: download, verify the CRC32 the
// index states, then activate it (which keeps the replaced binary as a .bak and
// prunes the pile to maxCoreBackups). Cores the index does not carry, and cores
// that are already that build, are left alone.
//
// progress may be nil. It exists so both callers — the page's Update action and
// the automatic pass — report through the install bar with one implementation
// rather than two that can drift.
//
// A build this device refused before — it cannot load on this firmware, or it
// is over the download limit — is not downloaded again: without that, a card
// whose firmware is older than the buildbot's build image would fetch every
// stale core every day only to refuse each one. The next build the index lists
// is fetched and checked as usual. A stop (ctx) ends the run between cores and
// is reported as Interrupted, not as a failure of the core it interrupted.
func runInstalledCoreUpdates(ctx context.Context, client *http.Client, progress func(phase, detail string)) emuInstallReport {
	var report emuInstallReport
	notify := func(phase, detail string) {
		if progress != nil {
			progress(phase, detail)
		}
	}
	notify(installPhaseResolving, "")
	index, err := fetchCoreIndex(ctx, client)
	if err != nil {
		report.Errors = append(report.Errors, "core index: "+err.Error())
		return report
	}
	report.Checked = true
	inv := scanCoreInventory()
	// One update per core: several tiles can name the same emulator (a ROM
	// folder and the core that runs it), and the binary is the same file.
	planned := planCoreUpdates(index, inv.Active)
	report.Current = len(inv.Active) - len(planned)
	if report.Current < 0 {
		report.Current = 0
	}
	updates := make([]coreUpdate, 0, len(planned))
	for _, upd := range planned {
		if reason, refused := coreBuildRefused(upd.Core, upd.CRC32); refused {
			logDebug("emulators", "core %s %s not fetched: refused before (%s)", upd.Core, upd.Date, reason)
			report.KnownRefused++
			continue
		}
		updates = append(updates, upd)
	}
	pool := compatCorePoolDir()
	for i, upd := range updates {
		if ctx.Err() != nil {
			report.Interrupted = true
			break
		}
		// Each update writes the build twice (the pool copy and the active
		// file) and keeps the one it replaces, so a pass over a card's whole
		// core set — CrossMix's first one replaces most of them — can need
		// gigabytes. A card that fills up is one where the next save cannot be
		// written: the run stops while there is room, and says so.
		if free, err := freeBytes(filepath.Dir(upd.Active)); err == nil && free < coreUpdateFreeFloor(upd.Active) {
			report.LowSpace = bootFormatBytes(free)
			logWarn("emulators", "core updates stopped before %s: only %s free on the card", upd.Core, report.LowSpace)
			break
		}
		notify(installPhaseDownload, fmt.Sprintf("%s (%d/%d)", coreConfigDisplayName(upd.Core), i+1, len(updates)))
		build, err := fetchCoreUpdate(ctx, client, upd, pool)
		if err == nil {
			var target string
			target, _, err = activateCoreBuild(build)
			emuCRCForget(target)
		}
		if err != nil {
			var refused *coreRefusedError
			switch {
			case ctx.Err() != nil:
				// Stopped mid-download: this core was not reached, it did not
				// fail, and the next pass picks it up.
				report.Interrupted = true
			case errors.As(err, &refused):
				report.Refused = append(report.Refused, coreConfigDisplayName(upd.Core))
				if report.RefusedNote == "" {
					report.RefusedNote = err.Error()
				}
			default:
				report.Errors = append(report.Errors, coreConfigDisplayName(upd.Core)+": "+err.Error())
			}
			if report.Interrupted {
				break
			}
			continue
		}
		report.Updated = append(report.Updated, coreConfigDisplayName(upd.Core))
	}
	return report
}

// coreUpdateFreeFloor is the free space a core update must find on the card
// before it starts: what the boot card warns below (bootLowSpaceBytes), plus
// three copies of the core it replaces — the pool copy, the staged file and the
// kept build — counting at least 16 MB for one.
func coreUpdateFreeFloor(active string) int64 {
	size := int64(16 << 20)
	if info, err := os.Stat(active); err == nil && info.Size() > size {
		size = info.Size()
	}
	return bootLowSpaceBytes + 3*size
}

// coreLowSpaceNote is how a toast says a run stopped for a nearly full card.
func coreLowSpaceNote(free string) string {
	return "stopped: the card is almost full (" + free + " free)"
}

// emuWinManagedSpecs lists the Windows emulators JukaHub installed and can
// therefore update.
func emuWinManagedSpecs() []emuWinSpec {
	var out []emuWinSpec
	for _, spec := range emuWinSpecs {
		if !spec.installable() {
			continue
		}
		if _, _, ok := emuWinManaged(spec.Name); ok {
			out = append(out, spec)
		}
	}
	return out
}

// startEmulatorUpdateInstalled updates every installed core the index has a
// newer build of — and every standalone emulator JukaHub installed: the managed
// ones on Windows, hakuX on the handheld — so a card full of emulators is
// brought forward in one press instead of one tile at a time. Nothing that is
// not installed is added: this action updates.
func startEmulatorUpdateInstalled() {
	var standalone []emuWinSpec
	if IsWindows() {
		standalone = emuWinManagedSpecs()
	}
	hakux := hakuxManagedOnCard()
	// The build switch covers the cores only; standalone emulators and hakuX
	// update the same way their tile's X does.
	cores := emuCoreInstallSupported() && len(emuInstalledCoreSet()) > 0
	if !cores && len(standalone) == 0 && !hakux {
		if !emuCoreInstallSupported() {
			ShowToast("Core updates are switched off in this build", ToastKindInfo)
		} else {
			ShowToast("Nothing installed to update yet", ToastKindInfo)
		}
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to update emulators", ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	if len(standalone) > 0 || hakux {
		installBarBeginLabelled("Emulators", "Updating emulators")
	} else {
		installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	}
	ShowToast("Checking installed emulators…", ToastKindInfo)
	go func() {
		defer endPatchOp()
		// The timeout derives from the card's run token, so a cancel from the
		// card or the activity strip stops the transfer mid-flight.
		timeout := 20 * time.Minute
		if len(standalone) > 0 || hakux {
			timeout = emuWinInstallTimeout
		}
		ctx, cancel := context.WithTimeout(installBarRunContext(), timeout)
		defer cancel()
		var report emuInstallReport
		if cores {
			report = runInstalledCoreUpdates(ctx, newEmulatorUpdateClient(), installBarProgressHook())
		}
		for _, spec := range standalone {
			if ctx.Err() != nil {
				break
			}
			installBarResetProgress()
			installBarDetail(spec.Name)
			result, err := emuWinInstall(ctx, emuWinClient(), spec, emuInstallBarReport)
			switch {
			case err != nil:
				report.Errors = append(report.Errors, spec.Name+": "+emuFriendlyError(err))
			case result.Current:
				report.Current++
			default:
				report.Updated = append(report.Updated, spec.Name)
				emuForgetUpdate(spec.Name)
			}
		}
		if hakux && ctx.Err() == nil {
			installBarResetProgress()
			installBarDetail(hakuxName + " (Xbox)")
			emuSetInstalling(emuXboxEntryName)
			result, err := hakuxRunUpdate(ctx)
			emuSetInstalling("")
			switch {
			case err != nil:
				report.Errors = append(report.Errors, hakuxName+": "+emuFriendlyError(err))
			case result.Current:
				report.Current++
			default:
				report.Updated = append(report.Updated, hakuxName)
				emuForgetUpdate(emuXboxEntryName)
				if result.Edited > 0 {
					logInfo("emulators", "hakuX: %d changed file(s) replaced; copies in %s", result.Edited, result.EditedDir)
					ShowToast(fmt.Sprintf("hakuX: %s you had changed replaced — your copies are in %s", pluralizeCount(result.Edited, "file", "files"), result.EditedDir), ToastKindInfo)
				}
			}
		}
		installBarPhase(installPhaseDone)
		installBarDone()
		if len(report.Updated) > 0 {
			invalidateEmulatorCache()
			invalidateCompatCache()
			emuForgetAllUpdates()
			coreBuildsRequestReload()
			trophyEventN(tkEmuInstall, int64(len(report.Updated)))
			PlaySuccessSound()
		}
		kind := ToastKindSuccess
		if len(report.Errors) > 0 || report.LowSpace != "" {
			kind = ToastKindWarning
		} else if len(report.Updated) == 0 {
			kind = ToastKindInfo
		}
		ShowToast(report.summary(), kind)
	}()
}

// emuInstallHintLabel is the A hint for a tile: the page installs an emulator
// where it used to dead-end, so the label has to say which one it is. A
// Windows emulator with no download JukaHub can install says "Get" (it opens
// the official page), and a console this device cannot run says "Details".
func emuInstallHintLabel(e emuEntry) string {
	if hakuxTile(e) {
		return hakuxHintA(e)
	}
	switch {
	case e.Pinned:
		return "Open"
	case e.Status == emuUnsupported:
		return "Details"
	case e.Status == emuAvailable && e.Launch != "":
		return "Launch"
	case e.Status == emuAvailable:
		// A core row is already the install, so the footer does not advertise an
		// install that would replace it. A opens the Games library on the games
		// the core plays (raemu.go), and says so when the core's console is known.
		if emuCoreSystemQuery(e) != "" {
			return "Games"
		}
		return ""
	}
	if IsWindows() {
		if name := emuWinStandaloneFor(e); name != "" {
			if spec, ok := emuWinSpecFor(name); ok && spec.installable() {
				return "Install"
			}
			return "Get"
		}
	}
	if emuCoreInstallSupported() && emuCoreForEntry(e) != "" {
		return "Install"
	}
	return ""
}

// emuUpdateHintLabel is the X hint for a tile: Update when a core is on the
// card (whether it is stale needs the index) or the tile is a Windows emulator
// JukaHub installed, Install or Update on the handheld's Xbox tile, and nothing
// when there is nothing to update.
func emuUpdateHintLabel(e emuEntry) string {
	if hakuxTile(e) {
		return hakuxHintX(e)
	}
	// The handheld's PSP tile: X opens the PPSSPP card (ppsspp_managed_ui.go).
	if label := ppssppTileXLabel(e); label != "" {
		return label
	}
	if IsWindows() && e.Managed && e.Status == emuAvailable {
		return "Update"
	}
	if !emuCoreInstallSupported() {
		return ""
	}
	core := emuCoreForEntry(e)
	if core == "" || !emuInstalledCoreSet()[core] {
		return ""
	}
	return "Update"
}

// emuTileUpdateKnown reports whether the last background check found a newer
// build for a tile: its core's, or its own Windows release.
func emuTileUpdateKnown(e emuEntry) bool {
	if e.Pinned || e.Status != emuAvailable {
		return false
	}
	if e.Managed {
		if _, ok := emuWinUpdateKnown(e.Name); ok {
			return true
		}
	}
	core := emuCoreForEntry(e)
	if core == "" {
		return false
	}
	_, ok := emuCoreUpdateKnown(core)
	return ok
}

// ── Rolling a core back ──────────────────────────────────────────────────────
//
// An update keeps the build it replaced as `<core>_libretro.<ext>.jukahub-
// <stamp>.bak` beside the active binary (installCoreFile). That file is the
// whole reason to keep it: a nightly build can be slower, or break a game the
// old one ran, and the fix has to be doable on the device rather than by
// pulling the card and finding a copy. Rollback installs the newest backup over
// the active binary and keeps the build it replaces, so a rollback is itself
// reversible — pressing it again brings back the build that was just removed.

// coreBackup is one `.bak` binary sitting beside an active core.
type coreBackup struct {
	Core string
	// Path is the backup file; Target is the active binary it was taken from.
	Path   string
	Target string
	Stamp  time.Time
	Size   int64
}

// coreBackupMark is the infix that separates an active file's name from the
// timestamp of the build it held.
const coreBackupMark = ".jukahub-"

// coreBackupStampLayout is the time format installCoreFile writes.
const coreBackupStampLayout = "20060102-150405"

// parseCoreBackupName splits a backup file name into the core it belongs to,
// the active file it was taken from, and when. Anything else in the folder —
// .part files, .bak files from another tool, a pool build — does not parse, so
// a rollback can never pick one up.
func parseCoreBackupName(name string) (core, targetName string, stamp time.Time, ok bool) {
	if !strings.HasSuffix(name, ".bak") {
		return "", "", time.Time{}, false
	}
	body := strings.TrimSuffix(name, ".bak")
	i := strings.LastIndex(body, coreBackupMark)
	if i <= 0 {
		return "", "", time.Time{}, false
	}
	targetName = body[:i]
	stampText := body[i+len(coreBackupMark):]
	// A replacement inside the same second gets a "-2" collision counter after
	// the stamp; the stamp itself contains one dash, so only a suffix after the
	// fixed width can be that counter.
	if len(stampText) > len(coreBackupStampLayout)+1 && stampText[len(coreBackupStampLayout)] == '-' {
		if n := stampText[len(coreBackupStampLayout)+1:]; strings.Trim(n, "0123456789") == "" {
			stampText = stampText[:len(coreBackupStampLayout)]
		}
	}
	stamp, err := time.ParseInLocation(coreBackupStampLayout, stampText, time.Local)
	if err != nil {
		return "", "", time.Time{}, false
	}
	core, _ = parseCoreFile(targetName)
	if core == "" {
		return "", "", time.Time{}, false
	}
	return core, targetName, stamp, true
}

// coreBackupsIn lists the backups of one core in one folder, newest first.
func coreBackupsIn(dir, core string) []coreBackup {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]coreBackup, 0, 2)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		gotCore, targetName, stamp, ok := parseCoreBackupName(entry.Name())
		if !ok || !strings.EqualFold(gotCore, core) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, coreBackup{
			Core:   gotCore,
			Path:   path,
			Target: filepath.Join(dir, targetName),
			Stamp:  stamp,
			Size:   info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Stamp.Equal(out[j].Stamp) {
			return out[i].Stamp.After(out[j].Stamp)
		}
		return out[i].Path > out[j].Path
	})
	return out
}

// maxCoreBackups is how many replaced builds stay on the card. Every install
// keeps the binary it replaced so a nightly build that turns out worse can be
// undone from the Emulators page; with the handheld updating its cores by
// itself, an unbounded pile of them would be the largest thing JukaHub owns on
// a 1 GB card, one per update per core, forever.
const maxCoreBackups = 5

// pruneCoreBackups keeps the newest keep backups beside one core and deletes
// the rest, returning what it removed. It is called by installCoreFile after a
// replacement, so every path that changes a core binary — the Emulators page,
// the compatibility panel, a rollback, the automatic update — lands on the same
// limit instead of each remembering it.
func pruneCoreBackups(target string, keep int) []string {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	if keep < 0 {
		keep = 0
	}
	dir := filepath.Dir(target)
	core, _ := parseCoreFile(filepath.Base(target))
	if core == "" {
		return nil
	}
	backups := coreBackupsIn(dir, core)
	if len(backups) <= keep {
		return nil
	}
	removed := make([]string, 0, len(backups)-keep)
	for _, backup := range backups[keep:] {
		if err := os.Remove(backup.Path); err != nil {
			logWarn("emulators", "could not prune backup %q: %v", backup.Path, err)
			continue
		}
		removed = append(removed, backup.Path)
	}
	if len(removed) > 0 {
		logDebug("emulators", "pruned %d old backup(s) beside %s (keeping %d)", len(removed), filepath.Base(target), keep)
	}
	return removed
}

// scanCoreBackups reads every kept build beside every active core the card
// holds, newest first. The footer and the Emulators page want the newest one per
// core; the kept-builds panel wants all of them. Both read this walk, so they
// cannot disagree about which files count as a build.
func scanCoreBackups() []coreBackup {
	var out []coreBackup
	for _, dir := range retroArchCoreDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			core, targetName, stamp, ok := parseCoreBackupName(entry.Name())
			if !ok {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			out = append(out, coreBackup{
				Core:   core,
				Path:   filepath.Join(dir, entry.Name()),
				Target: filepath.Join(dir, targetName),
				Stamp:  stamp,
				Size:   info.Size(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Stamp.Equal(out[j].Stamp) {
			return out[i].Stamp.After(out[j].Stamp)
		}
		return out[i].Path > out[j].Path
	})
	return out
}

// newestCoreBackups maps every core with a backup to its newest one. The footer
// asks per frame, so this is built once per invalidation, never per draw.
func newestCoreBackups() map[string]coreBackup {
	found := map[string]coreBackup{}
	for _, backup := range scanCoreBackups() {
		if _, exists := found[backup.Core]; !exists {
			found[backup.Core] = backup
		}
	}
	return found
}

// deleteCoreBackup removes one kept build from the card. It is the only place a
// kept build is thrown away by hand, so the guards are the rollback's: the file
// must still be a regular file inside one of RetroArch's cores folders, which is
// where installCoreFile put it.
func deleteCoreBackup(b coreBackup) error {
	if strings.TrimSpace(b.Path) == "" {
		return errors.New("that build has no file")
	}
	if !isRetroArchCoreDir(filepath.Dir(b.Path)) {
		return errors.New("that build is not in RetroArch's cores folder")
	}
	if _, err := existingRegularFile(b.Path); err != nil {
		return err
	}
	if err := os.Remove(b.Path); err != nil {
		return fmt.Errorf("delete %s: %w", filepath.Base(b.Path), err)
	}
	logDebug("emulators", "deleted kept build %s (%d bytes freed)", b.Path, b.Size)
	return nil
}

// coreBackupsTotal sums the kept builds' sizes: the figure the panel's subtitle
// and the Emulators row quote, so the cost of the safety net is visible.
func coreBackupsTotal(rows []coreBackup) int64 {
	var total int64
	for _, b := range rows {
		total += b.Size
	}
	return total
}

// isRetroArchCoreDir reports whether dir is one of the cores folders this
// build looks at. A rollback replaces a file inside one of them and nowhere
// else: the path comes from a scan, but it is checked again at the point that
// writes.
func isRetroArchCoreDir(dir string) bool {
	want := filepath.Clean(dir)
	for _, candidate := range retroArchCoreDirs() {
		got := filepath.Clean(candidate)
		if got == want || (IsWindows() && strings.EqualFold(got, want)) {
			return true
		}
	}
	return false
}

// rollbackCoreBuild installs a backup over the active binary it was taken from
// and keeps the binary that was replaced, so the rollback can be undone by the
// same press. The restored .bak is removed: its bytes are the active core now,
// and leaving it would offer the same build again.
func rollbackCoreBuild(b coreBackup) (coreBuild, error) {
	if strings.TrimSpace(b.Path) == "" || strings.TrimSpace(b.Target) == "" {
		return coreBuild{}, errors.New("backup is missing its file")
	}
	dir := filepath.Dir(b.Path)
	if filepath.Dir(b.Target) != dir {
		return coreBuild{}, errors.New("backup and core are not in the same folder")
	}
	if !isRetroArchCoreDir(dir) {
		return coreBuild{}, errors.New("that backup is not in RetroArch's cores folder")
	}
	if _, err := existingRegularFile(b.Path); err != nil {
		return coreBuild{}, err
	}
	// A kept build ran here once, but not necessarily well: one installed
	// before the load check existed may be a nightly that never loaded.
	// Restoring that would trade a core for a broken one.
	if problem := coreLoadProblemFile(b.Path, b.Target); problem != "" {
		return coreBuild{}, &coreRefusedError{Core: b.Core, Build: "kept " + b.Stamp.Format("2006-01-02 15:04"), Reason: coreCannotLoadReason + problem}
	}
	_, installErr := installCoreFile(b.Path, b.Target)
	emuCRCForget(b.Target)
	if installErr != nil {
		return coreBuild{}, installErr
	}
	if err := os.Remove(b.Path); err != nil {
		// The rollback itself succeeded; a leftover copy is only a stale entry
		// in the next listing, and the caller still reports success.
		logWarn("emulators", "restored backup %q could not be removed: %v", b.Path, err)
	}
	build, ok := coreBuildFromPath(b.Target, false)
	if !ok {
		return coreBuild{}, errors.New("the restored core is not a loadable binary")
	}
	build.Active = true
	logDebug("emulators", "rolled core %s back to the %s build (%s)", build.Core, b.Stamp.Format(coreBackupStampLayout), b.Target)
	return build, nil
}

// emuRollbackHintLabel is the R1 hint: Roll back when this tile's core has a
// build to go back to, nothing otherwise. A rollback is a local file copy, so
// it is offered on every platform — the install policy is about what the
// buildbot serves, not about restoring a file the card already has.
func emuRollbackHintLabel(e emuEntry) string {
	if e.Managed && e.KeptVersion != "" {
		return "Roll back"
	}
	core := emuCoreForEntry(e)
	if core == "" {
		return ""
	}
	if _, ok := emuNewestCoreBackup(core); !ok {
		return ""
	}
	return "Roll back"
}

// startEmulatorRollback restores the newest backup of the highlighted tile's
// core, or the kept previous version of a Windows emulator JukaHub installed.
// A core rollback runs on the event thread: it is one small file copy on the
// card, and the user needs the result before the next press. Every rollback
// takes the patch lock, as the installs do, so it cannot land in the middle of
// the daily pass replacing the same files.
func startEmulatorRollback() {
	entries := emulatorEntries()
	if emuPanel.cursor < 0 || emuPanel.cursor >= len(entries) {
		return
	}
	e := entries[emuPanel.cursor]
	if hakuxTile(e) {
		if !e.Managed || e.KeptVersion == "" {
			ShowToast("No previous hakuX version is kept — one is kept after the next update", ToastKindInfo)
			return
		}
		hakuxStartRollback()
		return
	}
	if e.Managed {
		if e.KeptVersion == "" {
			ShowToast("No previous "+e.Name+" version is kept — one is kept after the next update", ToastKindInfo)
			return
		}
		emuWinStartRollback(e.Name)
		return
	}
	core := emuCoreForEntry(e)
	if core == "" {
		ShowToast(e.Name+": no emulator core is known for this system", ToastKindWarning)
		return
	}
	backup, ok := emuNewestCoreBackup(core)
	if !ok {
		ShowToast("No previous "+coreConfigDisplayName(core)+" build to roll back to", ToastKindInfo)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is running — roll back when it finishes", ToastKindInfo)
		return
	}
	build, err := rollbackCoreBuild(backup)
	endPatchOp()
	if err != nil {
		ShowToast(coreConfigDisplayName(core)+": "+err.Error(), ToastKindError)
		return
	}
	// The tile list, the compatibility panel and the footer's own cache all
	// read the cores folder; drop them together.
	invalidateEmulatorCache()
	invalidateCompatCache()
	emuForgetUpdate(build.Core)
	coreBuildsRequestReload()
	PlaySuccessSound()
	ShowToast(coreConfigDisplayName(build.Core)+" rolled back · "+
		backup.Stamp.Format("2006-01-02 15:04")+" build restored · the replaced build is kept",
		ToastKindSuccess)
}

// emuCorePresence caches which cores the card has, and the newest backup for
// each. The footer asks on every frame, and reading the cores folder per frame
// would put the SD card in the render loop.
var emuCorePresence struct {
	mu      sync.Mutex
	built   bool
	cores   map[string]bool
	backups map[string]coreBackup
}

func emuCorePresenceBuild() {
	cores := map[string]bool{}
	for _, build := range scanCoreInventory().Builds {
		cores[build.Core] = true
	}
	emuCorePresence.cores = cores
	emuCorePresence.backups = newestCoreBackups()
	emuCorePresence.built = true
}

func emuInstalledCoreSet() map[string]bool {
	emuCorePresence.mu.Lock()
	defer emuCorePresence.mu.Unlock()
	if !emuCorePresence.built {
		emuCorePresenceBuild()
	}
	return emuCorePresence.cores
}

// emuNewestCoreBackup is the newest .bak the footer can offer to restore.
func emuNewestCoreBackup(core string) (coreBackup, bool) {
	emuCorePresence.mu.Lock()
	defer emuCorePresence.mu.Unlock()
	if !emuCorePresence.built {
		emuCorePresenceBuild()
	}
	backup, ok := emuCorePresence.backups[core]
	return backup, ok
}

func invalidateEmuCorePresence() {
	emuCorePresence.mu.Lock()
	emuCorePresence.built = false
	emuCorePresence.cores = nil
	emuCorePresence.backups = nil
	emuCorePresence.mu.Unlock()
}
