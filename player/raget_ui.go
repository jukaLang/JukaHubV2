package main

// raget_ui.go — when raget.go installs RetroArch, and what the player sees.
//
//   - Start-up (startRetroArchEnsure, from startCoreAutoUpdate): about a
//     minute after JukaHub starts, a handheld whose games have no RetroArch
//     they can start (racard.go) gets one — online, between games, with the
//     install bar, the patch slot held like every other install — unless
//     Emulator updates is Off, which gets a one-time note instead. A try that
//     fails is repeated twice more in the session, ten minutes apart; offline,
//     during a game, while another install runs or under 15 % battery off the
//     charger, it waits. A card that has RetroArch is looked at again every
//     raGetWatchEvery: JH-X6HRVE's lost it a minute after the start.
//   - The Emulators page: a card with no RetroArch shows a RetroArch tile
//     saying so, and A on it (or on a RetroArch folder without its binary)
//     installs it.
//   - A core install (startEmulatorInstall) or "Get more cores"
//     (emuMoreCoresStart) on such a card puts RetroArch first, inside its own
//     patch slot (raGetForCoreInstall, raGetForMoreCores).
//   - A game whose launch script found no RetroArch (gamelaunch_firmware.go,
//     raLaunchMissingNote): the card says JukaHub is installing it, and the
//     install starts.
//   - The Emulator updates pass keeps the RetroArch JukaHub installed current
//     (raGetUpdate), as one more updater (emuautoupdate_registry.go).
//
// The last install's outcome is kept for the diagnostics (raGetDiagnostics):
// JH-X5T7PW, "cant install retroarch", came with a log that began after the
// try had failed, and nothing else said why. So is the start-up's last look
// when no install ran: JH-X6HRVE said only "not tried this session".
//
// raGetOwnsLaunch is ra_glibc.go's question: a launch of the RetroArch JukaHub
// installed starts on JukaHub's glibc whatever Automatic would decide.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// raGetStartDelay keeps the start-up install behind the release check and
	// the core pass's first look, which want the same patch slot.
	raGetStartDelay = coreAutoUpdateStartupDelay + 15*time.Second
	// raGetAutoTries bounds the start-up tries that fail in one session;
	// raGetRetryWait spaces them.
	raGetAutoTries = 3
	raGetRetryWait = 10 * time.Minute
	// raGetLowBattery holds the download off the charger below this charge.
	raGetLowBattery = 15
	// raGetSlotWait is how long an install a press or a game asked for waits
	// for another download to end.
	raGetSlotWait = 15 * time.Minute
	// raGetWatchEvery is how often the start-up looks again at a card that
	// has its RetroArch (a stat and a remembered answer while it is there).
	raGetWatchEvery = 3 * time.Minute
	// raGetUpdaterKey is RetroArch's key in the Emulator updates pass.
	raGetUpdaterKey = "retroarch"
)

// errRaGetBusy is an install that could not start because another one holds
// the patch slot (or this one runs already); errRaGetOffline, no network.
var (
	errRaGetBusy    = errors.New("another install is running")
	errRaGetOffline = errors.New("no internet connection")
)

// raGetRun guards the first install: one at a time, from whichever path asked.
var raGetRun struct {
	mu      sync.Mutex
	running bool
	tries   int  // start-up tries that failed this session
	told    bool // the Off note was shown
	// last is this session's latest install, for the diagnostics; look the
	// start-up's latest look and what it decided, lookAt when.
	last   raGetOutcome
	look   string
	lookAt time.Time
}

// raGetNoteLook records the start-up's look for raGetDiagnostics.
func raGetNoteLook(what string) {
	raGetRun.mu.Lock()
	raGetRun.look, raGetRun.lookAt = what, time.Now()
	raGetRun.mu.Unlock()
}

// raGetOutcome is one install as the diagnostics tell it: when it began, who
// asked, and — once it ended — what it put on the card or why it could not.
type raGetOutcome struct {
	at    time.Time
	why   string
	ended bool
	err   string
	note  string
}

// raGetNoteStart and raGetNoteEnd record an install for raGetDiagnostics.
func raGetNoteStart(why string) {
	raGetRun.mu.Lock()
	raGetRun.last = raGetOutcome{at: time.Now(), why: why}
	raGetRun.mu.Unlock()
}

func raGetNoteEnd(res raGetResult, err error) {
	raGetRun.mu.Lock()
	defer raGetRun.mu.Unlock()
	last := &raGetRun.last
	last.ended = true
	switch {
	case errors.Is(err, errRaGetPresent):
		last.note = "a RetroArch turned up on the card meanwhile"
	case err != nil:
		last.err = firstLine(err.Error())
	default:
		last.note = raGetDoneMessage(res, false)
	}
}

// raGetDiagnostics is the diagnostics' line on the RetroArch JukaHub installs
// ("" where it installs none): the one it installed, this session's latest
// try and how it ended, the start-up's failed tries.
func raGetDiagnostics() string {
	if !raGetSupported() {
		return ""
	}
	raGetRun.mu.Lock()
	last, tries, told, running := raGetRun.last, raGetRun.tries, raGetRun.told, raGetRun.running
	look, lookAt := raGetRun.look, raGetRun.lookAt
	raGetRun.mu.Unlock()
	var parts []string
	if _, man, ok := raGetManaged(); ok {
		what := "installed by JukaHub"
		if man.Version != "" {
			what += ": RetroArch " + man.Version
		}
		if m := DeviceModel(man.Model); m != "" {
			what += ", TrimUI's " + raGetModelName(m) + " build from " + man.Release
		}
		parts = append(parts, what)
	}
	when := last.at.Format("15:04")
	switch {
	case last.at.IsZero() && told:
		parts = append(parts, "not downloaded by itself: Emulator updates is Off")
	case last.at.IsZero():
	case !last.ended && running:
		parts = append(parts, "installing since "+when+" ("+last.why+")")
	case last.err != "":
		why := last.err
		if r := []rune(why); len(r) > 400 {
			why = string(r[:400]) + "…"
		}
		parts = append(parts, "failed at "+when+" ("+last.why+"): "+why)
	case last.note != "":
		parts = append(parts, last.note+" at "+when+" ("+last.why+")")
	}
	if tries > 0 {
		parts = append(parts, fmt.Sprintf("%d start-up tries failed", tries))
	}
	if len(parts) == 0 {
		if look != "" {
			// Why no install ran: the start-up's last look.
			return look + " (looked at " + lookAt.Format("15:04") + ")"
		}
		return "not tried this session"
	}
	return strings.Join(parts, " · ")
}

// raGetClaim takes the guard; false when an install runs.
func raGetClaim() bool {
	raGetRun.mu.Lock()
	defer raGetRun.mu.Unlock()
	if raGetRun.running {
		return false
	}
	raGetRun.running = true
	return true
}

func raGetRelease() {
	raGetRun.mu.Lock()
	raGetRun.running = false
	raGetRun.mu.Unlock()
}

// raGetBusy reports a first install running now.
func raGetBusy() bool {
	raGetRun.mu.Lock()
	defer raGetRun.mu.Unlock()
	return raGetRun.running
}

// raGetStart starts a first install on a goroutine of its own (any thread).
// It reports whether one is running now, this one or one already running.
func raGetStart(why string) bool {
	if !raGetSupported() {
		return false
	}
	if !raGetClaim() {
		return true
	}
	go func() {
		defer raGetRelease()
		_ = raGetRunFirst(why, false)
	}()
	return true
}

// raGetRunFirst is one first install with the install bar and the toasts, on
// the calling goroutine (the caller holds the guard). auto: the start-up's,
// which stays quiet about a network or slot it waits for.
func raGetRunFirst(why string, auto bool) error {
	if !IsOnline() {
		if !auto {
			tbxPost(func() { ShowToast("Connect to Wi-Fi to download RetroArch", ToastKindWarning) })
		}
		return errRaGetOffline
	}
	if !tryBeginPatchOp() {
		if auto {
			return errRaGetBusy
		}
		// A press or a game asked: RetroArch follows the download that holds
		// the slot, for up to raGetSlotWait.
		tbxPost(func() { ShowToast("RetroArch is next, after the download running now", ToastKindInfo) })
		if !raGetWaitSlot() {
			tbxPost(func() {
				ShowToast("Another download is still running — install RetroArch again when it ends", ToastKindInfo)
			})
			return errRaGetBusy
		}
	}
	defer endPatchOp()
	model := raGetModel()
	gen, ctx := installBarBeginRunLabelled("RetroArch", "Getting RetroArch for the "+raGetModelName(model))
	logInfo("emulators", "RetroArch is not on this card (%s): installing TrimUI's newest build for the %s", why, raGetModelName(model))
	raGetNoteStart(why)
	res, err := raGetInstallFirst(ctx, emuPassClient(newEmulatorUpdateClient()), func(phase string, done, total int64) {
		installBarUpdateRun(gen, phase, done, total)
	})
	raGetNoteEnd(res, err)
	switch {
	case errors.Is(err, errRaGetPresent):
		installBarAbortRun(gen)
		return err
	case err != nil:
		installBarFailRun(gen, firstLine(err.Error()))
		logWarn("emulators", "RetroArch install (%s): %v", why, err)
		if ctx.Err() == nil {
			msg := "RetroArch could not be installed: " + firstLine(err.Error())
			tbxPost(func() { ShowToast(msg, ToastKindWarning) })
		}
		return err
	}
	installBarDoneRun(gen)
	msg := raGetDoneMessage(res, false)
	tbxPost(func() {
		PlaySuccessSound()
		ShowToast(msg, ToastKindSuccess)
	})
	return nil
}

// raGetForCoreInstall is the RetroArch a core install on a card without one
// puts first (startEmulatorInstall), the way the Windows page installs a
// portable RetroArch before a core: inside that install's patch slot, on its
// install bar, which it retitles. A card with no RetroArch had no cores folder
// either, and the press ended in "RetroArch cores folder not found" (testers
// on the Smart Pro family, 2026-11-24). installed reports that this call put
// RetroArch there; a RetroArch that appeared meanwhile is no error.
func raGetForCoreInstall(ctx context.Context, core string) (installed bool, err error) {
	return raGetBeforeCores(ctx, "the "+coreConfigDisplayName(core)+" core", "a "+core+" core install")
}

// raGetForMoreCores is raGetForCoreInstall for "Get more cores"
// (emuMoreCoresStart): the Game Ready card's fetch for a console with no
// emulator ended in the same "RetroArch cores folder not found" on a card
// without RetroArch (JH-X5T7PW). system "" is every console with games.
func raGetForMoreCores(ctx context.Context, system string) (installed bool, err error) {
	what := "more cores"
	if system != "" {
		what = "cores for " + system
	}
	return raGetBeforeCores(ctx, what, "fetching "+what)
}

// raGetBeforeCores is the first install those two put before their cores:
// what names the cores on the install bar, why the request in the log.
func raGetBeforeCores(ctx context.Context, what, why string) (bool, error) {
	if !raGetClaim() {
		return false, errRaGetBusy
	}
	defer raGetRelease()
	name := raGetModelName(raGetModel())
	installBarRelabel("Installing RetroArch and " + what)
	installBarDetail("RetroArch is not on this card: TrimUI's build for the " + name + " comes first")
	logInfo("emulators", "RetroArch is not on this card (%s): installing TrimUI's newest build for the %s first", why, name)
	raGetNoteStart(why)
	res, err := raGetInstallFirst(ctx, emuPassClient(newEmulatorUpdateClient()), emuInstallBarReport)
	raGetNoteEnd(res, err)
	switch {
	case errors.Is(err, errRaGetPresent):
		return false, nil
	case err != nil:
		logWarn("emulators", "RetroArch install (%s): %v", why, err)
		return false, err
	}
	logInfo("emulators", "%s", raGetDoneMessage(res, false))
	return true, nil
}

// raGetWaitSlot waits for the patch slot, up to raGetSlotWait, and takes it.
func raGetWaitSlot() bool {
	deadline := time.Now().Add(raGetSlotWait)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		if tryBeginPatchOp() {
			return true
		}
	}
	return false
}

// startRetroArchEnsure is the start-up's install (main thread, from
// startCoreAutoUpdate): it waits raGetStartDelay, then looks every so often —
// at a card with RetroArch too, which may lose it — until the session has
// tried enough or Emulator updates is Off.
func startRetroArchEnsure() {
	if !raGetSupported() {
		return
	}
	go func() {
		time.Sleep(raGetStartDelay)
		for {
			wait, done := raGetEnsureTick()
			if done {
				return
			}
			time.Sleep(wait)
		}
	}()
}

// raGetEnsureTick is one look of the start-up's install: done when there is
// nothing left to do this session, else how long to wait for the next.
func raGetEnsureTick() (wait time.Duration, done bool) {
	if raGetBusy() {
		raGetNoteLook("not tried by the start-up: an install of RetroArch is running or waiting for its turn")
		return time.Minute, false
	}
	if gameScreenSuspended() {
		// A game is on screen: its RetroArch is there, and the card is the
		// game's.
		return time.Minute, false
	}
	card := raCardCheck()
	if !card.Missing {
		// The games' RetroArch is there: looked at again in a while.
		raGetNoteLook("not needed: " + raGetPresentWords(card))
		return raGetWatchEvery, false
	}
	// Why the install below may not start yet, for the diagnostics.
	waiting := func(why string) {
		raGetNoteLook("not tried yet: RetroArch is " + card.Problem + "; " + why)
	}
	if emuCoreUpdateModeNow() == coreUpdateOff {
		raGetRun.mu.Lock()
		told := raGetRun.told
		raGetRun.told = true
		raGetRun.mu.Unlock()
		if !told {
			logInfo("emulators", "RetroArch is not on this card; Emulator updates is Off, so it is not downloaded by itself")
			tbxPost(func() {
				ShowToast("RetroArch is not on this card — Emulators → RetroArch installs it", ToastKindInfo)
			})
		}
		return 0, true
	}
	switch {
	case !IsOnline():
		waiting("waiting for the internet")
		return time.Minute, false
	case installBarActive():
		waiting("waiting for the install on the install bar to end")
		return time.Minute, false
	}
	if pct := getBatteryPercent(); pct >= 0 && pct < raGetLowBattery && !getBatteryCharging() {
		waiting(fmt.Sprintf("waiting: the battery is at %d %% off the charger", pct))
		return 5 * time.Minute, false
	}
	if !raGetClaim() {
		raGetNoteLook("not tried by the start-up: an install of RetroArch is running or waiting for its turn")
		return time.Minute, false
	}
	err := raGetRunFirst("start-up", true)
	raGetRelease()
	switch {
	case err == nil, errors.Is(err, errRaGetPresent):
		// Installed (or there after all): watched like any card with RetroArch.
		return raGetWatchEvery, false
	case errors.Is(err, context.Canceled):
		// Stopped from the install bar: not again this session (the tile and a
		// game still install it on a press).
		logInfo("emulators", "RetroArch install stopped from the install bar; not tried again until the next start")
		return 0, true
	case errors.Is(err, errRaGetOffline):
		waiting("waiting for the internet")
		return time.Minute, false
	case errors.Is(err, errRaGetBusy):
		waiting("waiting for the download running now")
		return time.Minute, false
	}
	raGetRun.mu.Lock()
	raGetRun.tries++
	tries := raGetRun.tries
	raGetRun.mu.Unlock()
	if tries >= raGetAutoTries {
		logWarn("emulators", "RetroArch install: %d tries failed; the next start tries again (or Emulators → RetroArch)", tries)
		return 0, true
	}
	return raGetRetryWait, false
}

// ── The Emulator updates pass ────────────────────────────────────────────────

func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       raGetUpdaterKey,
		Title:     "RetroArch",
		Supported: raGetSupported,
		Installed: func() bool { return raGetManagedBinary() != "" },
		InUse:     raGetInUse,
		Check:     raGetCheckUpdate,
		Install:   raGetInstallUpdate,
	})
}

// raGetInUse holds the update while RetroArch may be running: a game on
// screen, RetroArch itself, or a first install.
func raGetInUse() bool {
	if gameScreenSuspended() || raGetBusy() {
		return true
	}
	bin := raGetManagedBinary()
	if bin == "" {
		return false
	}
	running := raRunningBinaryFn()
	if running == "" {
		return false
	}
	a, aerr := os.Stat(bin)
	b, berr := os.Stat(running)
	return aerr == nil && berr == nil && os.SameFile(a, b)
}

// raGetCheckUpdate is the pass's question: has TrimUI published a newer build
// than the one JukaHub installed? Only packages not looked at before are
// opened (their directories, never their files).
func raGetCheckUpdate(ctx context.Context, client *http.Client) (bool, string, error) {
	_, man, ok := raGetManaged()
	if !ok {
		return false, "", nil
	}
	newer, _, err := raGetUpdateCandidates(ctx, client, man)
	if err != nil || len(newer) == 0 {
		return false, "", err
	}
	raGetRank(newer, false)
	return true, newer[0].label(), nil
}

// raGetInstallUpdate is the pass's install (raGetUpdate), reported on the
// pass's card.
func raGetInstallUpdate(ctx context.Context, client *http.Client, progress func(line string)) error {
	if gameScreenSuspended() {
		return errRelPackBusy // a game started since the check: next quiet moment
	}
	card := emuRuntimeCardFrom(ctx)
	last := ""
	res, err := raGetUpdate(ctx, client, func(phase string, done, total int64) {
		card.bytes(phase, done, total)
		if progress != nil && phase != last {
			last = phase
			progress(phase)
		}
	})
	if err != nil {
		return err
	}
	logInfo("emulators", "%s", raGetDoneMessage(res, true))
	return nil
}

// ── The Emulators page ───────────────────────────────────────────────────────

// raGetMissingEntry is the RetroArch tile of a handheld with no RetroArch on
// the card: A installs it (raGetActivate). coresDir is the folder the catalog
// read; the search behind it (deviceRetroArchCoresDir) has just run, so its
// remembered answer is current.
func raGetMissingEntry(coresDir string) (emuEntry, bool) {
	if !raGetSupported() || coreRetroArchBinary(coresDir) != "" {
		return emuEntry{}, false
	}
	if loc, ok := raLocateCached(); ok && loc.Bin != "" {
		return emuEntry{}, false
	}
	e := emuEntry{Name: emuRetroArchTileName, Glyph: emuGlyphFor(emuRetroArchTileName, ""), Status: emuMissing}
	name := raGetModelName(raGetModel())
	if raGetBusy() {
		e.Detail = "Installing TrimUI's newest RetroArch for the " + name + " — the install bar shows how far"
	} else {
		e.Detail = "Not on this card · A installs TrimUI's newest RetroArch for the " + name +
			", started on JukaHub's glibc " + privateGlibcVersion
	}
	return e, true
}

// raGetTileWantsInstall reports A on a handheld RetroArch tile with nothing to
// open: no RetroArch on the card, or its folder without the binary.
func raGetTileWantsInstall(e emuEntry) bool {
	if IsWindows() || e.Pinned || e.Name != emuRetroArchTileName {
		return false
	}
	if e.Status != emuMissing && !(e.Status == emuBroken && strings.TrimSpace(e.Launch) == "") {
		return false
	}
	return raGetSupported()
}

// raGetActivate is A on such a tile (main thread).
func raGetActivate(e emuEntry) {
	switch {
	case raGetBusy():
		PlayNavSound()
		ShowToast("RetroArch is being installed — the install bar shows how far", ToastKindInfo)
	case !IsOnline():
		PlayErrorSound()
		ShowToast("Connect to Wi-Fi to download RetroArch", ToastKindWarning)
	default:
		raGetStart("the " + e.Name + " tile")
		PlayNavSound()
		ShowToast("Getting TrimUI's newest RetroArch for the "+raGetModelName(raGetModel())+"…", ToastKindInfo)
	}
}

// raGetTileNote is what the RetroArch tile adds for a RetroArch JukaHub
// installed ("" for any other).
func raGetTileNote(bin string) string {
	own, man, ok := raGetManaged()
	if !ok || !raGetSameFile(own, bin) {
		return ""
	}
	what := "installed by JukaHub"
	if man.Version != "" {
		what += ": RetroArch " + man.Version
	}
	if m := DeviceModel(man.Model); m != "" {
		what += ", TrimUI's " + raGetModelName(m) + " build"
	}
	return what
}

// raGetSameFile reports two paths naming one file.
func raGetSameFile(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// raGetPresentWords says where the games' RetroArch is, by its name (the
// diagnostics carry no private paths).
func raGetPresentWords(card raCardState) string {
	bin := card.Need
	if card.Found != "" {
		bin = card.Found
	}
	switch {
	case bin == "":
		return "no SD card to look on"
	case card.Problem != "" && card.Found == "":
		return filepath.Base(bin) + " is " + card.Problem + ", and there is no card to install one on"
	}
	return filepath.Base(bin) + " in " + raCardFolderWords(bin, card.Target) + " can start"
}

// raLaunchMissingNote is what a game's diagnosis adds when its launch script
// could not start the RetroArch at missing (gamelaunch_firmware.go): where
// JukaHub sees the games' RetroArch, and — when they have none that can start
// (racard.go) — that JukaHub is installing TrimUI's (raGetLaunchNote). The
// script's failure is news, so the card is looked at again. Off the handheld,
// where JukaHub installs no RetroArch, the search's answer as before.
func raLaunchMissingNote(missing string) string {
	if !raGetSupported() {
		if ra := netplayRetroArchPath(); ra != "" {
			return " JukaHub finds RetroArch at " + ra + "."
		}
		return " JukaHub finds no RetroArch either (it searched the RetroArch folders on " +
			childEnvFirmwareCard() + ", every launch script in Emus and the card itself)."
	}
	raCardForget()
	card := raCardCheck()
	switch {
	case card.Missing:
		note := " JukaHub finds no RetroArch the games can start either: " + filepath.Base(card.Need) + " is " + card.Problem + "."
		if !card.Lost.IsZero() {
			note += " It was there at " + card.Lost.Format("15:04") +
				": a card that loses files has errors or holds less than it claims — run Tools → System → Card check."
		}
		return note + raGetLaunchNote()
	case card.Found != "":
		return " JukaHub finds RetroArch at " + card.Found + ", but the launch scripts call " + card.Need + ", which is " + card.Problem + "."
	case card.Problem != "":
		return " " + card.Need + " is " + card.Problem + "."
	case strings.HasPrefix(missing, "/") && !raSamePath(missing, card.Need):
		return " JukaHub finds the games' RetroArch at " + card.Need + "; this script called " + missing + "."
	}
	// There now, though the script was told "not found" a moment ago.
	return " JukaHub finds RetroArch at " + card.Need + " now: start the game again — if it stops the same way, run Tools → System → Card check."
}

// raGetLaunchNote is what a game's diagnosis adds when the games have no
// RetroArch that can start (raLaunchMissingNote): the install, started now
// when it can be.
func raGetLaunchNote() string {
	if !raGetSupported() {
		return ""
	}
	name := raGetModelName(raGetModel())
	switch {
	case raGetBusy():
		return " JukaHub is installing RetroArch now (the install bar shows how far): start the game again when it is done."
	case !IsOnline():
		return " Connect to Wi-Fi: JukaHub then installs TrimUI's newest RetroArch for the " + name + " by itself (or Emulators → RetroArch)."
	case raGetStart("a game"):
		return " JukaHub is installing TrimUI's newest RetroArch for the " + name + " now: start the game again when the install bar finishes."
	}
	return ""
}

// raGetOwnsLaunch reports a launch that starts the RetroArch JukaHub installed
// — or an unknown RetroArch on a card that has it (the namespace wraps every
// RetroArch a launch script might call): ra_glibc.go starts it on JukaHub's
// glibc whatever Automatic would decide for the core.
func raGetOwnsLaunch(preferred string) bool {
	own := raGetManagedBinary()
	if own == "" {
		return false
	}
	if strings.TrimSpace(preferred) == "" {
		return true
	}
	return raGetSameFile(own, preferred)
}
