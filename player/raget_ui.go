package main

// raget_ui.go — when raget.go installs RetroArch, and what the player sees.
//
//   - Start-up (startRetroArchEnsure, from startCoreAutoUpdate): a minute and a
//     half after JukaHub starts, a handheld whose card has no RetroArch gets
//     one — online, between games, with the install bar, the patch slot held
//     like every other install — unless Emulator updates is Off, which gets a
//     one-time note instead. A try that fails is repeated twice more in the
//     session, ten minutes apart; offline, during a game or under 15 % battery
//     off the charger, it waits.
//   - The Emulators page: a card with no RetroArch shows a RetroArch tile
//     saying so, and A on it (or on a RetroArch folder without its binary)
//     installs it.
//   - A game whose launch script found no RetroArch (gamelaunch_firmware.go):
//     the card says JukaHub is installing it, and the install starts.
//   - The Emulator updates pass keeps the RetroArch JukaHub installed current
//     (raGetUpdate), as one more updater (emuautoupdate_registry.go).
//
// raGetOwnsLaunch is ra_glibc.go's question: a launch of the RetroArch JukaHub
// installed starts on JukaHub's glibc whatever Automatic would decide.

import (
	"context"
	"errors"
	"net/http"
	"os"
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
	res, err := raGetInstallFirst(ctx, emuPassClient(newEmulatorUpdateClient()), func(phase string, done, total int64) {
		installBarUpdateRun(gen, phase, done, total)
	})
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
// startCoreAutoUpdate): it waits raGetStartDelay, then looks every so often
// until the card has a RetroArch or the session has tried enough.
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
		return time.Minute, false
	}
	if !raGetMissingNow() {
		return 0, true
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
	if !IsOnline() || gameScreenSuspended() || installBarActive() {
		return time.Minute, false
	}
	if pct := getBatteryPercent(); pct >= 0 && pct < raGetLowBattery && !getBatteryCharging() {
		return 5 * time.Minute, false
	}
	if !raGetClaim() {
		return time.Minute, false
	}
	err := raGetRunFirst("start-up", true)
	raGetRelease()
	switch {
	case err == nil, errors.Is(err, errRaGetPresent):
		return 0, true
	case errors.Is(err, context.Canceled):
		// Stopped from the install bar: not again this session (the tile and a
		// game still install it on a press).
		logInfo("emulators", "RetroArch install stopped from the install bar; not tried again until the next start")
		return 0, true
	case errors.Is(err, errRaGetBusy), errors.Is(err, errRaGetOffline):
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
	raGetRank(newer)
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

// raGetLaunchNote is what a game's diagnosis adds when the card has no
// RetroArch at all (gamelaunch_firmware.go): the install, started now when it
// can be.
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
