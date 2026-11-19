package main

// bootframe.go — "Home appears instantly" on JukaHub OS.
//
// On the standalone image (jukaosNativeActive) JukaHub keeps a copy of the
// panel's framebuffer as it showed Home, and the image's launcher writes it
// straight back to the panel at the next power-on, as soon as the card is
// mounted and before JukaHub itself is started (os/plastron/launch.sh,
// os/smartpro/launch.sh show_bootframe). The player sees their Home about a
// second earlier than JukaHub could draw it; JukaHub's first frame then
// replaces the same picture.
//
// What is kept is the session's last settled Home: Home on screen with no
// panel, menu, toast, transition or idle screen over it, and no press for
// bootFrameSettle. It is read off the main thread, checked again on it (a press
// while it was read throws it away), and held in memory until the card has
// it. The card gets it once per boot when it has none yet, and at the
// teardown (a power-off or a restart, jukaosShutdownStepAdd) — the picture
// taken before the power card and the goodbye card were drawn, never those.
//
// Never with the startup lock on: the frame would show Home before the code
// was asked. Turning the lock on deletes it (bootLockFinish), and no frame is
// kept while a lock is set.
//
// Files, in jukaosNativeDataDir (USERDATA_PATH): bootframe.fb, the visible page
// row by row at the framebuffer's own stride, and bootframe.info,
// "xres yres bits_per_pixel stride\n". The launcher shows it only when all four
// match /sys/class/graphics/fb0 (virtual_size, bits_per_pixel, stride) and the
// file is exactly stride*yres bytes; anything else is skipped silently.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	bootFrameFile     = "bootframe.fb"
	bootFrameInfoFile = "bootframe.info"
	// bootFrameSettle is how long Home must be left alone before it is kept.
	bootFrameSettle = 2 * time.Second
	// bootFrameEvery is how often the main thread is asked to look.
	bootFrameEvery = 2 * time.Second
	// bootFrameRefresh is the least time between two reads of the panel.
	bootFrameRefresh = 15 * time.Second
	// bootFrameMaxBytes bounds a frame: 1280x720 or 1024x768 at 32 bits is
	// under 4 MB; anything far larger is not a handheld panel.
	bootFrameMaxBytes = 16 << 20
)

// bootFrame is the kept picture. pix, info, gen and dirty are shared with the
// workers under mu; the rest is the main thread's.
var bootFrame struct {
	mu    sync.Mutex
	pix   []byte
	info  string
	gen   int
	dirty bool // pix is newer than the card's copy

	started  bool
	busy     bool
	at       time.Time // the last read of the panel
	input    int64     // lastInputUnixNano at that read
	boot     bool      // this boot's first kept frame went to the card check
	lockSeen bool      // a startup lock was seen and the files removed
}

// bootFramePosted keeps one look queued at most: the queue is drained by the
// main loop, and a long game must not pile looks up behind it.
var bootFramePosted atomic.Bool

// bootFrameIO serialises the card writes with the removal a lock asks for.
var bootFrameIO sync.Mutex

// bootFrameBlocked is set while a startup lock is on: no write reaches the
// card, whichever worker was already on its way.
var bootFrameBlocked atomic.Bool

// bootFrameStart begins keeping Home's picture, at the first frame of a
// standalone boot (jukaosFirstFrame). Main thread.
func bootFrameStart() {
	if !jukaosNativeActive() || bootFrame.started {
		return
	}
	bootFrame.started = true
	go func() {
		t := time.NewTicker(bootFrameEvery)
		defer t.Stop()
		for range t.C {
			if exitRequested.Load() {
				return
			}
			if bootFramePosted.CompareAndSwap(false, true) {
				tbxPost(bootFrameCheck)
			}
		}
	}()
}

// bootFrameOnScreen reports a start whose launcher already put the boot frame
// on the panel (JUKAHUB_BOOTFRAME=1): Home is showing, so the start should go
// straight to it rather than play a hello over it.
func bootFrameOnScreen() bool {
	return jukaosNativeActive() && os.Getenv("JUKAHUB_BOOTFRAME") == "1"
}

// bootFrameLockOn reports a start that must not open on the kept Home picture:
// a startup lock that is set (up or not), or a boot animation the player chose
// (the picture would show Home, then the animation, then Home again).
func bootFrameLockOn(c *Config) bool {
	return bootLockKindOf(c) != "" || !bootStyleOff(c)
}

// bootFrameHomeSettled reports Home alone on screen and left alone. Main
// thread: it reads the scene, the panels and the overlays.
func bootFrameHomeSettled(c *Config, now time.Time) bool {
	switch {
	case c == nil, exitRequested.Load(), startupLocked(), bootLockOverlayUp(), gameScreenSuspended():
		return false
	case currentSceneIndex < 0 || currentSceneIndex != homeSceneIndex(c), transitionPhase != "none":
		return false
	case ambientIsActive(), ss.active, alarmFiringActive(), voiceOrbActive(), homeEdit.sheet.open:
		return false
	case homeEditActive(), inmapOverlayOpen(), hoffCardVisible(), trophyOverlayActive(), powerSleepActive():
		// Edit Home, a popover, the hand-off card, a trophy, or a sleep
		// darkening the panel: not the Home the next boot should show.
		return false
	case userInputAge(now) < bootFrameSettle, uiAnimating(), saverToastShowing():
		return false
	}
	// The panels and screens the idle screen also waits for; music playing
	// behind Home does not count (background music would keep it from ever
	// settling), a video does.
	return !ambientEntryBlocked(c) && !videoHoldsScreenAwake() && !modalPanelBlocksShoulders() && !triggerOwnedByOverlay()
}

// bootFrameCheck reads the panel when Home has settled since the last read.
// Main thread (queued by bootFrameStart).
func bootFrameCheck() {
	bootFramePosted.Store(false)
	if bootFrame.busy || exitRequested.Load() {
		return
	}
	// The Smart Pro image's bootloader picture follows the same rules
	// (bootframe_ubootlogo.go).
	bootFrameUbootTick(appConfig)
	if bootFrameLockOn(appConfig) {
		if !bootFrame.lockSeen {
			bootFrame.lockSeen = true
			bootFrameForget()
		}
		return
	}
	if bootFrame.lockSeen || bootFrameBlocked.Load() {
		// The lock was taken off: Home may be kept again, and the card,
		// which has no picture now, gets the next one at once.
		bootFrame.lockSeen, bootFrame.boot = false, false
		bootFrameBlocked.Store(false)
	}
	now := time.Now()
	if !bootFrameHomeSettled(appConfig, now) {
		return
	}
	input := lastInputUnixNano.Load()
	if !bootFrame.at.IsZero() && (input == bootFrame.input || now.Sub(bootFrame.at) < bootFrameRefresh) {
		return
	}
	first := bootFrame.at.IsZero()
	bootFrame.busy = true
	go func() {
		pix, info, err := bootFrameGrab()
		tbxPost(func() {
			bootFrame.busy = false
			if err != nil {
				// Not retried until Home changes again.
				bootFrame.at, bootFrame.input = time.Now(), input
				if first {
					logWarn("bootframe", "Home could not be kept for the next boot: %v", err)
				}
				return
			}
			// A press, a panel or a lock since the look: the picture may be
			// half of something else. The next settle tries again.
			if lastInputUnixNano.Load() != input || !bootFrameHomeSettled(appConfig, time.Now()) || bootFrameLockOn(appConfig) {
				return
			}
			bootFrame.at, bootFrame.input = time.Now(), input
			bootFrame.mu.Lock()
			bootFrame.pix, bootFrame.info = pix, info
			bootFrame.gen++
			bootFrame.dirty = true
			bootFrame.mu.Unlock()
			if !bootFrame.boot {
				// The first boot with this card (or after a lock was taken
				// off) gets its picture now, not only at the power-off.
				bootFrame.boot = true
				go func() {
					if !fileExists(filepath.Join(jukaosNativeDataDir(), bootFrameInfoFile)) {
						if err := bootFrameSave(); err != nil {
							logWarn("bootframe", "not saved: %v", err)
						}
					}
				}()
			}
		})
	}()
}

// bootFrameDirty reports a kept picture the card does not have yet.
func bootFrameDirty() bool {
	bootFrame.mu.Lock()
	defer bootFrame.mu.Unlock()
	return bootFrame.dirty && bootFrame.pix != nil
}

// bootFrameSave writes the kept picture to the card: the old description
// first goes (a cut power then leaves no frame rather than a wrong one), then
// the pixels and the new description, each through a synced temporary file
// and a rename. Workers only.
func bootFrameSave() error {
	bootFrame.mu.Lock()
	pix, info, gen := bootFrame.pix, bootFrame.info, bootFrame.gen
	bootFrame.mu.Unlock()
	if pix == nil {
		return nil
	}
	bootFrameIO.Lock()
	defer bootFrameIO.Unlock()
	if bootFrameBlocked.Load() {
		return errors.New("the startup lock is on")
	}
	dir := jukaosNativeDataDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	infoPath := filepath.Join(dir, bootFrameInfoFile)
	if err := os.Remove(infoPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, f := range []struct {
		path string
		data []byte
	}{{filepath.Join(dir, bootFrameFile), pix}, {infoPath, []byte(info)}} {
		tmp := f.path + ".tmp"
		if err := writeFileSynced(tmp, f.data, 0644); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, f.path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	syncDirectory(dir)
	bootFrame.mu.Lock()
	if bootFrame.gen == gen {
		// The card has it: the megabytes are not kept for a game's sake.
		bootFrame.dirty = false
		bootFrame.pix = nil
	}
	bootFrame.mu.Unlock()
	logInfo("bootframe", "Home kept for the next boot (%s)", strings.TrimSpace(info))
	// On the Smart Pro image the bootloader shows it too, from power-on.
	bootFrameUbootAfterSave(pix, info)
	return nil
}

// bootFrameShutdownStep is the teardown's row on JukaHub OS (added by
// jukaosShutdownStepAdd while a newer picture waits).
func bootFrameShutdownStep() (string, error) {
	if err := bootFrameSave(); err != nil {
		return "", err
	}
	return "Home shows at once next time", nil
}

// bootFrameForget drops the kept picture and removes the card's copy: the
// startup lock was turned on (bootLockFinish) or is set. The removal runs
// off the main thread.
func bootFrameForget() {
	bootFrameBlocked.Store(true)
	bootFrame.mu.Lock()
	bootFrame.pix, bootFrame.info, bootFrame.dirty = nil, "", false
	bootFrame.gen++
	bootFrame.mu.Unlock()
	if !jukaosNativeActive() {
		return
	}
	go func() {
		bootFrameIO.Lock()
		defer bootFrameIO.Unlock()
		dir := jukaosNativeDataDir()
		for _, name := range []string{bootFrameInfoFile, bootFrameFile} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				logWarn("bootframe", "%s not removed: %v", name, err)
			}
		}
		// The bootloader's picture goes back to the image's own logo.
		bootFrameUbootForgetLocked()
	}()
}
