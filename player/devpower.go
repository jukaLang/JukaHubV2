package main

// devpower.go — the handheld's own power: Sleep, Restart and Power off from
// Home's power card, Auto sleep after a quiet spell, and switching the
// panel's backlight off for the dock's "Screen off".
//
// After Mosaik CFW 0.9.1 ("auto sleep in the launcher after 5 min idle; the
// Screen-off screensaver really turns the backlight off") and 0.9.11 ("Restart
// button in the power-off window").
//
// Restart and Power off are an ordinary quit first: the card marks what
// should follow and asks for the clean exit (requestAppExit), so the glass
// teardown stops games, flushes playtime, settings and saves and syncs the
// card exactly as Exit does. Only when the teardown and the Cleanup hooks
// (brightness, the TrimUI volume flush…) have finished — main defers
// devPowerAfterShutdown ahead of runCleanupHooks, and the exit watchdog's
// forced exit calls it after its bounded Cleanup run — does the device sync
// once more and run busybox's reboot or poweroff. An atomic swap makes it
// run once; a plain Exit leaves nothing pending, so it is a no-op.
//
// Sleep is the power key's sleep (powersleep.go sleepNow), so the card, Auto
// sleep, the power button and the in-game menu share one suspend and one set
// of wake rules. The wake itself (Wi-Fi, Chat, the network monitor) is
// noticed by the wake watcher like any firmware sleep (wake_resume.go).
//
// Auto sleep is off until chosen (Settings → Power): suspend under JukaHub has
// not been tried on hardware yet. When on, it sleeps the handheld after that
// many minutes without input, but never while a game owns the screen, while
// charging, while something plays, downloads, unpacks or is shared, or while
// an alarm is set — a sleeping device cannot ring.
//
// The backlight: the dock's "Screen off" used to stop drawing and leave the
// panel lit. On the TrimUI the display driver switches the light itself off
// (brightness_disp_linux.go). Every wake path switches it back on — any input
// (ambientNotifyInput), leaving the dock, an alarm, the exit and a guard on
// the per-frame idle tick — and a marker in the temp directory lets the next
// start undo a light a crash left off.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Restart and Power off ─────────────────────────────────────────────────

// devPowerAction is what follows the teardown.
type devPowerAction int32

const (
	devPowerNone devPowerAction = iota
	devPowerReboot
	devPowerPoweroff
)

// devPowerCmdTimeout bounds one reboot/poweroff command: busybox's signals
// init and returns at once; anything slower is not going to work.
const devPowerCmdTimeout = 3 * time.Second

// devPowerPending is the action the card asked for; the teardown reads it.
var devPowerPending atomic.Int32

// devPowerSupported reports whether this device can sleep, restart and power
// off from JukaHub: the TrimUI. A desktop keeps Cancel and Exit — Linux
// desktops included (P().IsHandheld() is true on every Linux, and Restart is
// a real reboot).
func devPowerSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// devPowerRequest quits through the normal clean exit, then restarts or powers
// off the device.
func devPowerRequest(a devPowerAction) {
	devPowerPending.Store(int32(a))
	logInfo("power", "%s requested from the power card", devPowerVerb(a))
	requestAppExit()
	// The action runs from main's deferred devPowerAfterShutdown once the
	// Cleanup hooks have flushed, or from the exit watchdog's forced exit
	// (exitguard.go) after its bounded Cleanup run.
}

// devPowerVerb is the busybox applet for a.
func devPowerVerb(a devPowerAction) string {
	if a == devPowerPoweroff {
		return "poweroff"
	}
	return "reboot"
}

// devPowerShutdownTitle is the teardown card's heading.
func devPowerShutdownTitle() string {
	switch devPowerAction(devPowerPending.Load()) {
	case devPowerReboot:
		return "Restarting"
	case devPowerPoweroff:
		return "Powering off"
	}
	return "Shutting down"
}

// devPowerAfterShutdown runs the pending restart or power off once the
// teardown has flushed everything. A no-op for a plain exit.
func devPowerAfterShutdown() {
	a := devPowerAction(devPowerPending.Swap(int32(devPowerNone)))
	if a == devPowerNone {
		return
	}
	verb := devPowerVerb(a)
	backlightOn()
	if err := syncDisks(); err != nil {
		logWarn("power", "sync before %s failed: %v", verb, err)
	}
	if err := devPowerRun(verb); err != nil {
		logWarn("power", "%s failed: %v", verb, err)
	}
}

// devPowerCommands are the ways to ask for verb, most specific first.
func devPowerCommands(verb string) [][]string {
	return [][]string{
		{"/sbin/" + verb},
		{"/bin/busybox", verb},
		{"/usr/sbin/" + verb},
		{verb},
	}
}

// devPowerRun runs the first command for verb that exists and succeeds.
func devPowerRun(verb string) error {
	var errs []error
	for _, argv := range devPowerCommands(verb) {
		if filepath.IsAbs(argv[0]) {
			if _, err := os.Stat(argv[0]); err != nil {
				continue
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), devPowerCmdTimeout)
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
		cancel()
		cmd := strings.Join(argv, " ")
		if err == nil {
			logInfo("power", "%s: %s accepted", verb, cmd)
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %v %s", cmd, err, strings.TrimSpace(string(out))))
	}
	if len(errs) == 0 {
		return fmt.Errorf("no %s command found", verb)
	}
	return errors.Join(errs...)
}

// ── Sleep ─────────────────────────────────────────────────────────────────

// devSleepNow sleeps the device through the power key's sleep (powersleep.go
// sleepNow: input muted, a running game stopped, backlight off, sync, the next
// alarm armed in the RTC, suspend — or a screen-off sleep when the kernel
// refuses — and every step undone on wake). The card and Auto sleep used to
// carry their own suspend; one implementation means one set of wake rules.
// It reports whether a sleep was asked for (not on a desktop, not while one is
// already running).
func devSleepNow(reason string) bool {
	if !devPowerSupported() || powerSleepActive() {
		return false
	}
	logInfo("power", "sleeping (%s)", reason)
	sleepNow(reason)
	return true
}

// ── Auto sleep ────────────────────────────────────────────────────────────

// autoSleepKey is the Custom setting: minutes without input, "0" for Off.
const autoSleepKey = "autoSleep"

// autoSleepSteps are the row's stops, in minutes; 0 is Off (the default).
var autoSleepSteps = []int{0, 3, 5, 10, 15, 30}

// autoSleepMinutes is the chosen delay; 0 when off or unreadable.
func autoSleepMinutes(c *Config) int {
	if c == nil {
		return 0
	}
	s, ok := customString(c.Variables.Custom, autoSleepKey)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// autoSleepLabel is a stop's text.
func autoSleepLabel(n int) string {
	if n <= 0 {
		return "Off"
	}
	return strconv.Itoa(n) + " min"
}

// autoSleepSettingsRow is Settings → Power → Auto sleep, on the handheld only.
func autoSleepSettingsRow() setRow {
	return setRow{
		ID: "autoSleep", Label: "Auto sleep", Kind: setKindChoice,
		Detail:  setStatic("Sleeps when idle; never while charging or playing. Off while an alarm is set"),
		Visible: func(*Config) bool { return devPowerSupported() },
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(autoSleepSteps))
			for _, n := range autoSleepSteps {
				out = append(out, setOption{Value: strconv.Itoa(n), Label: autoSleepLabel(n)})
			}
			return out
		},
		Current: func(c *Config) int {
			cur := autoSleepMinutes(c)
			for i, n := range autoSleepSteps {
				if n == cur {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return autoSleepLabel(autoSleepMinutes(c)) },
		Choose: func(c *Config, value string) {
			n, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			setStoreString(c, autoSleepKey, strconv.Itoa(n))
			setToast("Auto sleep: " + autoSleepLabel(n))
		},
	}
}

// autoSleepCheckEvery throttles the decision.
const autoSleepCheckEvery = time.Second

var autoSleepLastCheck time.Time

// autoSleepTick sleeps the handheld once it has been quiet for the chosen
// time and nothing is using it. Event thread, from screensaverAutoTick.
func autoSleepTick(config *Config, now time.Time) {
	if !autoSleepLastCheck.IsZero() && now.Sub(autoSleepLastCheck) < autoSleepCheckEvery {
		return
	}
	autoSleepLastCheck = now
	mins := autoSleepMinutes(config)
	if mins <= 0 || !devPowerSupported() || !powerSleepSupported() || powerSleepActive() {
		return
	}
	if userInputAge(now) < time.Duration(mins)*time.Minute {
		return
	}
	if why := autoSleepBlocker(config); why != "" {
		return
	}
	devSleepNow(fmt.Sprintf("auto sleep after %d min", mins))
	// A sleep that failed or was refused is tried again only after another
	// full idle period, not every second (each try may toast).
	noteUserInputTime(now)
}

// autoSleepBlocker names what keeps the handheld awake, or "" when nothing
// does. The cheap checks come first; the panel list and the process scan run
// only once the idle time is up.
func autoSleepBlocker(config *Config) string {
	switch {
	case exitRequested.Load():
		return "quitting"
	case gameScreenSuspended():
		return "a game owns the screen"
	case alarmFiringActive():
		return "an alarm is ringing"
	case alarmNextUpcoming() != nil:
		return "an alarm is set"
	case getBatteryCharging():
		return "charging"
	case installBarActive() || feExtractActive():
		return "an install or extraction is running"
	case castWatcherCount() > 0:
		return "the screen is being shared"
	case autoSleepDownloading():
		return "a download is running"
	case ytdlBgActive():
		return "a video is downloading in the background"
	case idleScreenHoldOff(config):
		return "something is playing or open"
	}
	return ""
}

// autoSleepDownloading reports a download that is moving.
func autoSleepDownloading() bool {
	if downloadsPaused() {
		return false
	}
	active, _, _, _ := downloadCounts(downloadsSnapshot())
	return active > 0
}

// ── Backlight ─────────────────────────────────────────────────────────────

// backlight is the panel's light while JukaHub has it off. raw is the level
// the fallback replaced with 0 (0 when the driver's switch did it).
var backlight struct {
	mu  sync.Mutex
	off bool
	raw int
}

// backlightIsOff mirrors backlight.off for the per-frame guard.
var backlightIsOff atomic.Bool

// backlightMarkerPath records a light JukaHub switched off, for the next
// start. The temp directory is RAM on the handheld: a reboot clears it, and
// a reboot also brings the light back.
var backlightMarkerPath = func() string {
	return filepath.Join(os.TempDir(), "jukahub-backlight-off")
}

// backlightSupported reports whether JukaHub can switch the light: the
// TrimUI's display driver.
func backlightSupported() bool { return IsTrimuiDevice() }

// backlightOff switches the light off while keep (re-checked under the lock,
// so a wake racing it always wins) still holds; nil keep means always.
func backlightOff(keep func() bool) {
	if !backlightSupported() {
		return
	}
	backlight.mu.Lock()
	defer backlight.mu.Unlock()
	if backlight.off || (keep != nil && !keep()) {
		return
	}
	// Marked off before the ioctl: a backlightOn racing it then takes the
	// lock and waits instead of reading "on" and skipping.
	backlightIsOff.Store(true)
	raw, err := dispBacklightOff()
	if err != nil {
		backlightIsOff.Store(false)
		logWarn("backlight", "could not switch the backlight off: %v", err)
		return
	}
	if raw > 0 {
		// Level 0 is not the user's level: polls must not read it as one,
		// and the exit puts the user's level back if nothing else does.
		brightnessBorrowed.Store(true)
	}
	backlight.off, backlight.raw = true, raw
	backlightIsOff.Store(true)
	if err := os.WriteFile(backlightMarkerPath(), []byte(strconv.Itoa(raw)), 0o600); err != nil {
		logWarn("backlight", "could not record the backlight state: %v", err)
	}
}

// backlightOn switches the light back on if JukaHub switched it off. Safe from
// any goroutine and cheap when the light is on.
func backlightOn() {
	if !backlightIsOff.Load() {
		return
	}
	backlight.mu.Lock()
	defer backlight.mu.Unlock()
	if !backlight.off {
		return
	}
	err := dispBacklightOn(backlight.raw)
	if backlight.raw > 0 {
		brightnessBorrowed.Store(false)
	}
	backlight.off, backlight.raw = false, 0
	backlightIsOff.Store(false)
	if err != nil {
		logWarn("backlight", "could not switch the backlight on: %v", err)
	}
	if rmErr := os.Remove(backlightMarkerPath()); rmErr != nil && !os.IsNotExist(rmErr) {
		logWarn("backlight", "could not remove %s: %v", backlightMarkerPath(), rmErr)
	}
}

// backlightGuard is the per-frame safety net: the light is off but neither
// the dark dock nor a sleep wants it off, so it comes back.
func backlightGuard() {
	if backlightIsOff.Load() && !powerSleepActive() && !ambientIsScreenOff() {
		backlightOn()
	}
}

// backlightRecover switches on a light a previous run left off (a crash, a
// kill), once at start.
func backlightRecover() {
	data, err := os.ReadFile(backlightMarkerPath())
	if err != nil {
		return
	}
	raw, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if backlightSupported() {
		if err := dispBacklightOn(raw); err != nil {
			logWarn("backlight", "could not switch on the backlight a previous run left off: %v", err)
		} else {
			logInfo("backlight", "switched on the backlight a previous run left off")
		}
	}
	_ = os.Remove(backlightMarkerPath())
}

func init() {
	// Every exit leaves the light on: the teardown's services row, and the
	// deferred cleanup for a panic or a signal.
	bootOnShutdown(backlightOn)
	Cleanup = append(Cleanup, backlightOn)
}
