package main

// ppsspp_managed_ui.go — the Emulators page side of JukaHub's own PPSSPP
// (ppsspp_managed.go).
//
// The tester's first question was "what version of PPSSPP does JukaHub use?",
// and nothing on the page answered it. The handheld's PSP tile now leads its
// footer line with the answer — "PPSSPP 1.18.1 (firmware) · 1.20.3 available"
// or "PPSSPP 1.20.3 (JukaHub) · firmware 1.18.1 kept" — and X on that tile opens
// a PPSSPP card: install or update JukaHub's build, choose which PPSSPP plays
// PSP games, or install RetroArch's PPSSPP core, which is what X did on this
// tile before. The card is the shared modal glass card (recoverycard.go), so it
// draws over the page and owns the input while it is up, and needs nothing from
// the page's own drawing code.
//
// Everything slow — the release check, the download, a firmware scan — runs off
// the render thread and reports through the install bar and toasts. What the
// tile reads per catalog build is the published state and the last firmware
// detection; a detection that is missing or old is started in the background and
// the catalog is rebuilt when it lands.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ppssppTileName is the name of the tile the last catalog build decorated, so a
// press can tell the PSP tile from the rest without scanning the card.
var ppssppTileName atomic.Pointer[string]

// ppssppPSPTileIndex finds the PSP tile in a handheld catalog: the tile whose
// launcher PSP games resolve to (findGameLauncher), else a tile named after the
// PSP (a Roms/PSP folder with no emulator). Core rows are RetroArch's, never it.
func ppssppPSPTileIndex(entries []emuEntry) int {
	if launcher := findGameLauncher(ppssppLibrarySystem, entries); launcher != "" {
		for i, e := range entries {
			if !e.Pinned && e.Launch == launcher {
				return i
			}
		}
	}
	for i, e := range entries {
		if e.Pinned || strings.TrimSpace(e.Core) != "" {
			continue
		}
		switch normaliseLauncherName(e.Name) {
		case "psp", "ppsspp", "playstationportable":
			return i
		}
	}
	return -1
}

// ppssppTileLine is the answer the PSP tile leads with: which PPSSPP plays PSP
// games, and what else there is.
func ppssppTileLine(st ppssppState, fw ppssppFirmwareInfo, haveFw bool, platform string) string {
	if b, ok := st.build(st.Active); ok {
		line := b.label() + " (JukaHub)"
		switch {
		case fw.Version != "":
			line += " · firmware " + fw.Version + " kept"
		case fw.Launcher != "" || !haveFw:
			line += " · firmware PPSSPP kept"
		}
		if ppssppNewer(st.LatestVersion, b.Version) && st.Bad[st.LatestTag] == "" {
			line += " · " + st.LatestVersion + " available"
		}
		return line
	}
	line := "PPSSPP (firmware)"
	switch {
	case fw.Version != "":
		line = "PPSSPP " + fw.Version + " (firmware)"
	case haveFw && fw.Launcher == "":
		line = "No PPSSPP on the card"
	}
	newest, installed := st.newest()
	switch {
	case installed && st.Bad[newest.Tag] != "":
		line += " · JukaHub's " + strings.TrimPrefix(newest.label(), "PPSSPP ") + " did not start"
	case installed:
		line += " · JukaHub's " + strings.TrimPrefix(newest.label(), "PPSSPP ") + " installed"
	case platform == "":
		line += " · no JukaHub build for this model"
	case st.LatestVersion != "" && (fw.Version == "" || ppssppNewer(st.LatestVersion, fw.Version)):
		line += " · " + st.LatestVersion + " available"
	}
	return line
}

// ppssppDecorateCatalog puts the PPSSPP line on the handheld's PSP tile. The
// tile keeps its own detail (its folder and script) after the line. On a card
// whose firmware has no PSP launcher at all, the tile becomes JukaHub's build
// while that build plays PSP games, so it is not listed as missing.
func ppssppDecorateCatalog(entries []emuEntry) []emuEntry {
	if !ppssppManagedEnabled() {
		return entries
	}
	i := ppssppPSPTileIndex(entries)
	if i < 0 {
		ppssppTileName.Store(nil)
		return entries
	}
	ppssppRequestFirmwareRefresh()
	fw, haveFw := ppssppFirmwareCached()
	st := ppssppSnapshot()
	platform, _ := ppssppPlatform()
	e := &entries[i]
	name := e.Name
	ppssppTileName.Store(&name)
	line := ppssppTileLine(st, fw, haveFw, platform) + " · " + hintSentence("%s: PPSSPP builds", "X", "PPSSPP builds")
	if detail := strings.TrimSpace(e.Detail); detail != "" {
		line += " — " + detail
	}
	e.Detail = line
	if e.Status != emuAvailable {
		if b, ok := st.build(st.Active); ok && regularFile(b.script()) {
			e.Status = emuAvailable
			e.Launch = b.script()
		}
	}
	return entries
}

// ppssppTileOpensCard reports whether X on this tile opens the PPSSPP card.
func ppssppTileOpensCard(e emuEntry) bool {
	if e.Pinned || !ppssppManagedEnabled() {
		return false
	}
	p := ppssppTileName.Load()
	return p != nil && *p == e.Name
}

// ppssppTileXLabel is the X hint for the PSP tile ("" elsewhere), for the
// page's footer.
func ppssppTileXLabel(e emuEntry) string {
	if ppssppTileOpensCard(e) {
		return "PPSSPP"
	}
	return ""
}

// ppssppCardBody explains, in a paragraph, what the card's choices mean.
func ppssppCardBody(st ppssppState, fw ppssppFirmwareInfo, haveFw bool, untested bool) string {
	var b strings.Builder
	active, usingOurs := st.build(st.Active)
	switch {
	case usingOurs:
		b.WriteString("PSP games run JukaHub's " + active.label() + " (minui-psp " + active.Tag + ") from JukaHub's data folder. ")
	case fw.Launcher != "":
		b.WriteString("PSP games run " + ppssppFirmwareLabel(fw) + ", the build on your card (" + filepath.Base(fw.Launcher) + "). ")
	case haveFw:
		b.WriteString("Your card has no PPSSPP launcher for PSP games. ")
	default:
		b.WriteString("PSP games run the PPSSPP on your card. ")
	}
	b.WriteString("JukaHub can keep a newer PPSSPP of its own — the TrimUI build packaged by minui-psp — and update it with the Emulator updates setting. ")
	b.WriteString("Both use the firmware's save folders, so saves and save states carry over either way, and if JukaHub's build does not start, the game opens in the firmware's PPSSPP instead.")
	if untested {
		b.WriteString(" This model is untested with that build: if its buttons do not work in PPSSPP, restart the device and choose the firmware's PPSSPP here.")
	}
	if usingOurs {
		if reason := ppssppFallbackReason(ppssppLastStatusLine(active)); reason != "" {
			b.WriteString(" Last launch opened in the firmware's PPSSPP: " + reason + ".")
		}
	}
	switch {
	case haveFw && fw.Stick == "" && fw.Launcher == "":
		b.WriteString(" With no firmware PPSSPP on this card, JukaHub's keeps PSP saves in its own folder.")
	case haveFw && fw.Stick == "":
		b.WriteString(" No firmware save folder was found yet — play a PSP game once with the firmware's PPSSPP so both can share it.")
	}
	return b.String()
}

// openPPSSPPCard opens the PPSSPP card for the PSP tile (main thread).
func openPPSSPPCard(e emuEntry) {
	st := ppssppSnapshot()
	fw, haveFw := ppssppFirmwareCached()
	platform, untested := ppssppPlatform()
	newest, installed := st.newest()
	_, usingOurs := st.build(st.Active)
	tile := e.Name
	var options []recoveryOption

	switch {
	case platform == "":
		options = append(options, recoveryOption{icon: "info", title: "No JukaHub build for this model",
			detail: "minui-psp publishes PPSSPP for the Smart Pro, the Brick and the Smart Pro S", disabled: true})
	case !installed:
		title := "Install PPSSPP"
		if st.LatestVersion != "" {
			title += " " + st.LatestVersion
		}
		options = append(options, recoveryOption{icon: "download", title: title + " for PSP games",
			detail: "The TrimUI build from minui-psp · uses your saves · the firmware's PPSSPP stays",
			run:    func(*Config) { recoveryCardClose(); ppssppStartInstall(tile, true) }})
	case st.LatestTag != "" && st.LatestTag != newest.Tag && st.Bad[st.LatestTag] == "":
		title := "Update JukaHub's PPSSPP"
		if st.LatestVersion != "" {
			title = "Update to PPSSPP " + st.LatestVersion
		}
		options = append(options, recoveryOption{icon: "update", title: title,
			detail: "minui-psp " + st.LatestTag + " · " + newest.label() + " is kept as the way back",
			run:    func(*Config) { recoveryCardClose(); ppssppStartInstall(tile, false) }})
	default:
		options = append(options, recoveryOption{icon: "refresh", title: "Check for a newer PPSSPP",
			detail: "Installed: " + newest.label() + " (minui-psp " + newest.Tag + ")",
			run:    func(*Config) { recoveryCardClose(); ppssppStartInstall(tile, false) }})
	}

	switch {
	case usingOurs:
		title := "Use the firmware's PPSSPP"
		if fw.Version != "" {
			title += " " + fw.Version
		}
		detail := "JukaHub's build stays installed"
		if fw.Launcher != "" {
			detail = "PSP games go back to " + filepath.Base(fw.Launcher) + " · " + detail
		}
		options = append(options, recoveryOption{icon: "restore", title: title,
			detail: detail, run: func(*Config) { recoveryCardClose(); ppssppStartSwitch(false) }})
	case installed:
		options = append(options, recoveryOption{icon: "check", title: "Use JukaHub's " + newest.label() + " for PSP games",
			detail: "Saves are shared · the firmware's PPSSPP takes over if it does not start",
			run:    func(*Config) { recoveryCardClose(); ppssppStartSwitch(true) }})
	}

	if installed && !usingOurs {
		options = append(options, recoveryOption{icon: "trash", title: "Remove JukaHub's PPSSPP",
			detail: "Frees the space its builds take · saves stay in the firmware's folders",
			run:    func(*Config) { recoveryCardClose(); ppssppStartRemove() }})
	}

	if emuCoreInstallSupported() && emuCoreForEntry(e) != "" {
		entry := e
		options = append(options, recoveryOption{icon: "gamepad", title: "RetroArch's PPSSPP core",
			detail: "Install or update the libretro core RetroArch plays PSP games with",
			run:    func(*Config) { recoveryCardClose(); startEmulatorInstall(entry) }})
	}

	recoveryCardShow(recoveryCardSpec{
		title:    "PPSSPP",
		subtitle: ppssppTileLine(st, fw, haveFw, platform),
		icon:     "gamepad",
		body:     ppssppCardBody(st, fw, haveFw, untested),
		options:  options,
	})
	PlayActivateSound()
}

// ppssppStartInstall installs (or updates to) the newest release off the event
// thread, through the install bar. use is the first install's promise: PSP
// games switch to it. An update switches by the automatic rule — when the user
// chose JukaHub's PPSSPP, already plays a JukaHub build, or the new build is
// newer than the firmware's.
func ppssppStartInstall(tile string, use bool) {
	platform, _ := ppssppPlatform()
	if platform == "" {
		ShowToast("No PPSSPP build is published for this model", ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to install PPSSPP", ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	installBarBeginLabelled("PPSSPP", "Installing PPSSPP for PSP games")
	installBarPhase(installPhaseResolving)
	emuSetInstalling(tile)
	go func() {
		defer endPatchOp()
		defer emuSetInstalling("")
		// The timeout derives from the card's run token, so a cancel from the
		// install card or the activity strip stops the transfer.
		ctx, cancel := context.WithTimeout(installBarRunContext(), 45*time.Minute)
		defer cancel()
		rel, err := ppssppFetchRelease(ctx, newEmulatorUpdateClient())
		if err != nil {
			installBarFail(installCardDetail(emuFriendlyError(err)))
			ShowToast("PPSSPP: "+emuFriendlyError(err), ToastKindError)
			return
		}
		ppssppRecordLatest(rel)
		ppssppStampCheck(time.Now())
		fw := ppssppDetectFirmwareNow()
		st := ppssppSnapshot()
		build, current := st.newest()
		current = current && build.Tag == rel.Tag && regularFile(build.binary()) && regularFile(build.script())
		if !current {
			build, err = ppssppInstall(ctx, emuWinClient(), rel, platform, fw, emuInstallBarReport)
			if err != nil {
				installBarFail(installCardDetail(emuFriendlyError(err)))
				ShowToast("PPSSPP: "+emuFriendlyError(err), ToastKindError)
				return
			}
			trophyEvent(tkEmuInstall) // a new build; "already the newest" installs nothing
		}
		installBarPhase(installPhaseDone)
		installBarDone()
		invalidateEmulatorCache()

		st = ppssppSnapshot()
		// A check or an update switches by the automatic rule, and never to a
		// build whose launcher already reported that it does not start; only
		// the first install, asked for in so many words, switches regardless.
		switchNow := use || (build.usable(st) && ppssppShouldSwitch(st, build, fw.Version, ppssppUserPinnedNow()))
		if _, active := st.build(st.Active); active && st.Active == build.Tag {
			switchNow = false
		}
		if switchNow {
			choice := ppssppChoiceAuto
			if use {
				choice = ppssppChoiceJukaHub
			}
			if _, err := ppssppSwitchToBuild(build.Tag, choice, fw); err != nil {
				ShowToast(build.label()+" is installed, but PSP games could not be switched to it: "+err.Error(), ToastKindWarning)
				return
			}
			invalidateEmulatorCache()
		}
		PlaySuccessSound()
		st = ppssppSnapshot()
		switch {
		case current && st.Active == build.Tag && !switchNow:
			ShowToast(build.label()+" is the newest build, and PSP games use it", ToastKindSuccess)
		case current && st.Active == build.Tag:
			ShowToast(build.label()+" is the newest build · PSP games use it now", ToastKindSuccess)
		case current:
			ShowToast(build.label()+" is the newest build · PSP games keep "+ppssppFirmwareLabel(fw), ToastKindInfo)
		case st.Active == build.Tag:
			ShowToast("PSP games now use "+build.label()+" · saves are shared with the firmware's PPSSPP", ToastKindSuccess)
		default:
			ShowToast(build.label()+" installed · PSP games keep "+ppssppFirmwareLabel(fw)+" until you switch", ToastKindSuccess)
		}
	}()
}

// ppssppStartSwitch is the card's "Use JukaHub's PPSSPP" / "Use the firmware's
// PPSSPP". It rewrites a launcher and the launcher file, so it waits for any
// install to finish rather than racing it.
func ppssppStartSwitch(useJukaHub bool) {
	if !tryBeginPatchOp() {
		ShowToast("An update is running — try again when it finishes", ToastKindInfo)
		return
	}
	go func() {
		defer endPatchOp()
		fw := ppssppDetectFirmwareNow()
		if useJukaHub {
			b, err := ppssppSwitchToBuild("", ppssppChoiceJukaHub, fw)
			if err != nil {
				ShowToast("PPSSPP: "+err.Error(), ToastKindError)
				return
			}
			invalidateEmulatorCache()
			PlaySuccessSound()
			ShowToast("PSP games now use JukaHub's "+b.label()+" · saves are shared with the firmware's PPSSPP", ToastKindSuccess)
			return
		}
		if err := ppssppSwitchToFirmware(ppssppChoiceFirmware); err != nil {
			ShowToast("PPSSPP: "+err.Error(), ToastKindError)
			return
		}
		invalidateEmulatorCache()
		PlaySuccessSound()
		ShowToast("PSP games use "+ppssppFirmwareLabel(fw)+" again · JukaHub's build stays installed", ToastKindSuccess)
	}()
}

// ppssppStartRemove is the card's "Remove JukaHub's PPSSPP", off the event
// thread (removing a build is thousands of files on the card).
func ppssppStartRemove() {
	if !tryBeginPatchOp() {
		ShowToast("An update is running — try again when it finishes", ToastKindInfo)
		return
	}
	go func() {
		defer endPatchOp()
		removed, kept, err := ppssppRemoveBuilds()
		invalidateEmulatorCache()
		switch {
		case err != nil:
			ShowToast("PPSSPP: "+err.Error(), ToastKindError)
		case len(kept) > 0:
			ShowToast(fmt.Sprintf("Removed %s · kept %s: a folder still holds saves or a mount — restart and try again",
				pluralizeCount(removed, "build", "builds"), strings.Join(kept, ", ")), ToastKindWarning)
		default:
			PlaySuccessSound()
			ShowToast("JukaHub's PPSSPP removed · PSP games use the firmware's PPSSPP", ToastKindSuccess)
		}
	}()
}
