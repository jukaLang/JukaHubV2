package main

// travellock.go — Travel lock: the Smart Pro's FN switch as a hold switch for
// the power button (PWR-10).
//
// A handheld in a bag gets its power button pressed. Settings → Power &
// battery → Travel lock turns the FN slide switch into a lock: with the switch
// in the lock position, a press neither wakes the device nor leaves it running
// after a power-on. Slide it back and the button works again. While the device
// is awake the switch changes nothing but a toast.
//
// The switch. On the TrimUI A133P boards the FN switch is a latching GPIO,
// gpio243 (PH19). Which reading
// means "slid away from the FN label" is not documented, so the lock does not
// guess: turning Travel lock on stores the position the switch is in right
// then as "unlocked" (the device is in use, so that is the open position) and
// the other one as "locked" (travelLockAtKey). That choice is also what makes
// the lock fail safe: a pin that is not a switch at all reads the same value
// forever and so never reaches the locked value.
//
// Three ways the device comes on, three guards:
//   - JukaHub's own sleep (powersleep.go). After a real suspend,
//     travelLockKeepAsleep sends every wake straight back to sleep while the
//     switch is locked — screen dark, input muted, the game still paused —
//     except a wake for a JukaHub alarm. The screen-off sleep (a kernel that
//     will not suspend) ignores the power press while locked.
//   - A sleep the firmware made itself (keymon on some images, or the Power
//     button row set to Nothing): the wake is seen by the wake watcher, and
//     travelLockTick puts the device back to sleep through sleepNow.
//   - Power-on from off. The PMIC turns the board on in hardware, before any
//     software runs, so the press itself cannot be refused. The boot hook
//     System/starts/00-jukahub-travel-lock.sh (travelLockHookTemplate), named
//     to run before the other boot scripts, reads the switch and powers the
//     device straight back off. It checks that it is on the board it was
//     written on (the device tree's compatible string), so a card moved to
//     another handheld never powers that one off on a pin that means something
//     else there. Under JukaHub OS's own init (jukaosNativeActive), where no
//     boot scripts run, the same check runs at JukaHub's power-on start
//     (travelLockBootCheck).
//
// Unlocking is always one slide away: no code, nothing to remember. A lock
// that went wrong is undone by sliding the switch the other way, or by deleting
// the hook from the card on a computer.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// travelLockKey is the setting (a bool string).
	travelLockKey = "travelLock"
	// travelLockAtKey is the switch reading that means locked, "0" or "1",
	// stored when the setting is turned on. Bookkeeping (cfgdocRuntimeKeys):
	// on another device it would be another switch's reading.
	travelLockAtKey = "travelLockAt"
	// travelLockGPIO is the FN switch's pin on the TrimUI A133P boards.
	travelLockGPIO = "243"
	// travelLockHookFile is the boot hook in <SD>/System/starts; "00-" sorts
	// it before the firmware's and JukaHub's other boot scripts.
	travelLockHookFile = "00-jukahub-travel-lock.sh"
	// travelLockHookStaging is the hook's next version while it is written,
	// one folder up from System/starts (writeTrimuiHook's rule).
	travelLockHookStaging = ".jukahub-travel-lock.tmp"
	// travelLockAlarmSlack is how close a JukaHub alarm may be before a wake
	// is taken as the alarm's and the device stays up.
	travelLockAlarmSlack = 2 * time.Minute
	// travelLockSpuriousMax is how many suspends in a row may come straight
	// back before the screen-off sleep takes over.
	travelLockSpuriousMax = 3
	// travelLockFirmwareGuard is how long after a JukaHub sleep a wake the
	// watcher reports is taken as that sleep's own.
	travelLockFirmwareGuard = 10 * time.Second
	// travelLockPowerOnWindow is how soon after power-on a first start counts
	// as the power-on itself (JukaHub OS starts JukaHub within seconds).
	travelLockPowerOnWindow = 2 * time.Minute
	// travelLockBootMarker notes the boot JukaHub last started in.
	travelLockBootMarker = "/tmp/jukahub-travel-lock.boot"
)

// Overridable paths: the sysfs GPIO class and the board description.
var (
	travelLockGPIOClass  = "/sys/class/gpio"
	travelLockCompatible = "/proc/device-tree/compatible"
)

// travelLock is the lock's live state. The atomics are read by the sleep and
// power-key goroutines; the rest belongs to the main thread.
var travelLock struct {
	// armed is set while the setting is on, on a board with the switch.
	armed atomic.Bool
	// lockedAt is the reading that means locked (0 or 1).
	lockedAt atomic.Int32
	// lastPos is the last reading the main thread took (-1: none yet).
	lastPos atomic.Int32
	// exportOnce exports the pin at most once per run.
	exportOnce sync.Once
	// seenWakes is the wake watcher's count the tick last looked at.
	seenWakes uint64
	seenInit  bool
}

func init() { travelLock.lastPos.Store(-1) }

// travelLockHardware reports whether this device has the FN switch the lock
// uses: the original Smart Pro. The Smart Pro S reads its switch elsewhere and
// the Brick family has none on this pin.
func travelLockHardware() bool {
	return devPowerSupported() && DetectDevice() == DeviceTrimuiSmartPro
}

// travelLockReadSwitch reads the FN switch: 0 or 1, ok false when it cannot be
// read. The pin is exported once if nothing has; a pin set up as an output is
// not a switch and reads as unreadable.
func travelLockReadSwitch() (int, bool) {
	if !travelLockHardware() {
		return 0, false
	}
	dir := filepath.Join(travelLockGPIOClass, "gpio"+travelLockGPIO)
	value := filepath.Join(dir, "value")
	if _, err := os.Stat(value); err != nil {
		travelLock.exportOnce.Do(func() {
			if err := os.WriteFile(filepath.Join(travelLockGPIOClass, "export"), []byte(travelLockGPIO), 0o200); err != nil {
				logDebug("power", "travel lock: could not export gpio%s: %v", travelLockGPIO, err)
			}
		})
	}
	if d, err := os.ReadFile(filepath.Join(dir, "direction")); err == nil && strings.TrimSpace(string(d)) == "out" {
		return 0, false
	}
	data, err := os.ReadFile(value)
	if err != nil {
		return 0, false
	}
	switch strings.TrimSpace(string(data)) {
	case "0":
		return 0, true
	case "1":
		return 1, true
	}
	return 0, false
}

// travelLockEnabled reads the setting.
func travelLockEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	v, ok := customValue(c.Variables.Custom, travelLockKey)
	return ok && coerceBool(v)
}

// travelLockLockedValue is the stored locked reading; ok false when missing.
func travelLockLockedValue(c *Config) (int, bool) {
	if c == nil {
		return 0, false
	}
	s, ok := customString(c.Variables.Custom, travelLockAtKey)
	if !ok {
		return 0, false
	}
	switch strings.TrimSpace(s) {
	case "0":
		return 0, true
	case "1":
		return 1, true
	}
	return 0, false
}

// travelLockNoteConfig mirrors the setting for the goroutines. Main thread.
func travelLockNoteConfig(c *Config) {
	at, ok := travelLockLockedValue(c)
	armed := ok && travelLockEnabled(c) && travelLockHardware()
	travelLock.lockedAt.Store(int32(at))
	travelLock.armed.Store(armed)
}

// travelLockEngagedNow reports whether the lock is on and the switch is in the
// lock position right now (a fresh read). Any goroutine.
func travelLockEngagedNow() bool {
	if !travelLock.armed.Load() {
		return false
	}
	pos, ok := travelLockReadSwitch()
	return ok && int32(pos) == travelLock.lockedAt.Load()
}

// travelLockAlarmNear reports a JukaHub alarm close enough that a wake is its
// own: the device stays up for it, lock or not.
func travelLockAlarmNear() bool {
	if alarmFiringActive() {
		return true
	}
	at := powerSleepNextAlarm.Load()
	return at > 0 && time.Until(time.Unix(0, at)) <= powerSleepAlarmLead+travelLockAlarmSlack
}

// travelLockKeepAsleep runs on the sleep goroutine after a real suspend
// (powerSleepRun): while the switch holds the lock, each wake goes straight
// back to sleep with the screen still dark. It reports false when the kernel
// keeps coming straight back, so the caller falls back to the screen-off sleep
// (which ignores the power press while locked).
func travelLockKeepAsleep() bool {
	spurious := 0
	for travelLockEngagedNow() && !travelLockAlarmNear() && !exitRequested.Load() {
		logInfo("sleep", "travel lock: woken with the FN switch locked, back to sleep")
		before, haveBefore := powerSleepAsleepClock()
		// The wall clock (Round(0) drops the monotonic reading) keeps counting
		// through a suspend: the measure when the sleep clocks are missing.
		wallBefore := time.Now().Round(0)
		err := suspendDevice()
		after, haveAfter := powerSleepAsleepClock()
		slept := time.Now().Round(0).Sub(wallBefore)
		if haveBefore && haveAfter {
			slept = after - before
		}
		if err == nil && slept >= powerSleepSpuriousWake {
			spurious = 0
			powerKeyNoteOwnWake()
			continue
		}
		spurious++
		if err != nil {
			logWarn("sleep", "travel lock: the kernel did not suspend again: %v", err)
		}
		if spurious >= travelLockSpuriousMax {
			logWarn("sleep", "travel lock: the device keeps waking straight up; keeping the screen dark instead")
			return false
		}
		time.Sleep(time.Second)
	}
	return true
}

// travelLockTick is the main loop's part, once a second from powerKeyTick
// (also while a game owns the screen): it mirrors the setting, says when the
// switch is slid, and puts the device back to sleep after a firmware sleep
// woke it while locked. Main thread.
func travelLockTick(c *Config) {
	travelLockNoteConfig(c)
	if !travelLock.armed.Load() {
		travelLock.lastPos.Store(-1)
		travelLock.seenInit = false
		return
	}
	pos, ok := travelLockReadSwitch()
	if ok {
		prev := travelLock.lastPos.Swap(int32(pos))
		if prev >= 0 && prev != int32(pos) && !powerSleepActive() && !gameScreenSuspended() {
			if int32(pos) == travelLock.lockedAt.Load() {
				showToast("Power button locked: it won't wake or start the device until FN is slid back", ToastInfo())
			} else {
				showToast("Power button unlocked", ToastInfo())
			}
		}
	}
	startWakeWatcher()
	n := wakeCount()
	if !travelLock.seenInit {
		travelLock.seenWakes, travelLock.seenInit = n, true
		return
	}
	if n == travelLock.seenWakes {
		return
	}
	travelLock.seenWakes = n
	ev, have := wakeLast()
	if !have || !ev.Suspend || powerSleepActive() || powerSleepJustWoke(travelLockFirmwareGuard) {
		return
	}
	if !ok || int32(pos) != travelLock.lockedAt.Load() || travelLockAlarmNear() {
		return
	}
	logInfo("power", "travel lock: the device woke from a firmware sleep with the FN switch locked; back to sleep")
	sleepNow("travel-lock")
}

// travelLockSettingsRow is Settings → Power & battery → Travel lock, shown on
// the Smart Pro only.
func travelLockSettingsRow() setRow {
	return setRow{
		ID: travelLockKey, Label: "Travel lock", Kind: setKindToggle,
		Visible: func(*Config) bool { return travelLockHardware() },
		Detail: func(c *Config) string {
			if _, ok := travelLockLockedValue(c); !ok || !travelLockEnabled(c) {
				return "Use the FN switch to stop the power button turning the device on in a bag"
			}
			if pos := travelLock.lastPos.Load(); pos >= 0 && pos == travelLock.lockedAt.Load() {
				return "Locked: slide the FN switch back to use the power button"
			}
			return "Slide the FN switch to lock the power button; slide it back to unlock"
		},
		Get: func(c *Config) bool {
			// travelLockAt is per device: a setting carried over from another
			// handheld without it is off here until turned on again.
			_, ok := travelLockLockedValue(c)
			return ok && travelLockEnabled(c)
		},
		Set: travelLockSet,
	}
}

// travelLockSet turns the lock on or off. On: the switch's position now is the
// unlocked one and the other is stored as locked; the boot hook goes on the
// card. Main thread.
func travelLockSet(c *Config, on bool) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	if !on {
		c.Variables.Custom[travelLockKey] = boolString(false)
		saveUserConfigDebounced(userConfigFromVariables(c))
		travelLockNoteConfig(c)
		if err := travelLockRemoveHook(); err != nil {
			logWarn("power", "travel lock: could not remove the boot hook: %v", err)
		}
		setToast("Travel lock off")
		return
	}
	pos, ok := travelLockReadSwitch()
	if !ok {
		setToast("Travel lock needs the FN switch, and JukaHub cannot read it here")
		return
	}
	locked := 1 - pos
	c.Variables.Custom[travelLockKey] = boolString(true)
	c.Variables.Custom[travelLockAtKey] = strconv.Itoa(locked)
	saveUserConfigDebounced(userConfigFromVariables(c))
	travelLockNoteConfig(c)
	travelLock.lastPos.Store(int32(pos))
	travelLock.seenInit = false
	if err := travelLockWriteHook(locked); err != nil {
		logWarn("power", "travel lock: could not write the boot hook: %v", err)
		setToast("Travel lock on for sleep; the power-on guard could not be written to the card")
		return
	}
	setToast("Travel lock on: slide the FN switch to lock the power button")
}

// travelLockBoard is the board's compatible string with its NUL separators
// dropped — what the hook's `tr -d '\000'` reads — or "" without a device tree.
func travelLockBoard() string {
	data, err := os.ReadFile(travelLockCompatible)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(bytes.ReplaceAll(data, []byte{0}, nil)), "\n")
}

// travelLockHookPath is the boot hook's place on the card.
func travelLockHookPath() (string, error) {
	root := trimuiSDRootFn()
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("cannot find the SD card")
	}
	return filepath.Join(root, "System", "starts", travelLockHookFile), nil
}

// travelLockHookScript is the boot hook for this board, the locked reading and
// the app folder (where it logs).
func travelLockHookScript(locked int, board, appDir string) (string, error) {
	if locked != 0 && locked != 1 {
		return "", fmt.Errorf("bad switch reading %d", locked)
	}
	if strings.IndexFunc(board+appDir, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("a control character in the board name or the JukaHub folder")
	}
	return strings.NewReplacer(
		"@LOCKED@", strconv.Itoa(locked),
		"@BOARD@", shellQuote(board),
		"@APP_DIR@", shellQuote(appDir),
		"@GPIO@", travelLockGPIO,
	).Replace(travelLockHookTemplate), nil
}

// travelLockWriteHook puts the boot hook on the card, atomically (the firmware
// may run every file in System/starts, so a half-written one must never be
// there).
func travelLockWriteHook(locked int) error {
	if !IsTrimuiDevice() || jukaosNativeActive() {
		return nil // JukaHub OS's init runs no boot scripts: travelLockBootCheck covers it
	}
	path, err := travelLockHookPath()
	if err != nil {
		return err
	}
	appDir, err := trimuiAppDir()
	if err != nil {
		return err
	}
	script, err := travelLockHookScript(locked, travelLockBoard(), appDir)
	if err != nil {
		return err
	}
	starts := filepath.Dir(path)
	if got, err := os.ReadFile(path); err == nil && string(got) == script {
		return nil
	}
	if err := os.MkdirAll(starts, 0o755); err != nil {
		return err
	}
	if err := renameIntoPlace(filepath.Join(filepath.Dir(starts), travelLockHookStaging), path, []byte(script)); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o755)
	syncDirectory(starts)
	return nil
}

// travelLockRemoveHook takes the boot hook off the card.
func travelLockRemoveHook() error {
	if !IsTrimuiDevice() {
		return nil
	}
	path, err := travelLockHookPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

// reconcileTravelLock brings the boot hook in line with the setting at start:
// written (or rewritten for a moved app folder) while it is on, removed while
// it is off or the board has no switch.
func reconcileTravelLock(c *Config) {
	travelLockNoteConfig(c)
	if !IsTrimuiDevice() {
		return
	}
	at, ok := travelLockLockedValue(c)
	if travelLockEnabled(c) && ok && travelLockHardware() {
		if err := travelLockWriteHook(at); err != nil {
			logWarn("power", "travel lock: could not write the boot hook: %v", err)
		}
		return
	}
	if path, err := travelLockHookPath(); err == nil {
		if _, err := os.Stat(path); err == nil {
			if err := travelLockRemoveHook(); err != nil {
				logWarn("power", "travel lock: could not remove the boot hook: %v", err)
			} else {
				logInfo("power", "travel lock: removed the boot hook (the lock is off or this device has no FN switch)")
			}
		}
	}
}

// travelLockBootCheck is the power-on guard under JukaHub OS's own init, which
// runs no System/starts scripts: a power-on start with the switch locked
// powers straight back off. It reports whether it did. Elsewhere the boot hook
// does this before JukaHub starts, and a JukaHub opened later from the menu is
// never powered off.
func travelLockBootCheck(c *Config) bool {
	travelLockNoteConfig(c)
	if !jukaosNativeActive() || !travelLockFirstStartThisBoot() || !travelLockEngagedNow() {
		return false
	}
	logInfo("power", "travel lock: powered on with the FN switch locked; powering off")
	if err := syncDisks(); err != nil {
		logWarn("power", "travel lock: sync failed: %v", err)
	}
	if err := devPowerRun("poweroff"); err != nil {
		// JukaHub exits anyway: JukaHub OS's launcher then stops and its init
		// powers the device off.
		logWarn("power", "travel lock: poweroff failed: %v", err)
	}
	return true
}

// travelLockFirstStartThisBoot reports the first JukaHub start of this boot,
// soon after power-on: JukaHub OS's launcher passes no power-on flag, and its
// relaunches (a game handing the screen back, an update) must never count.
// The boot id is noted in /tmp, so a second start of the same boot finds it.
func travelLockFirstStartThisBoot() bool {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return false
	}
	id := strings.TrimSpace(string(boot))
	if old, err := os.ReadFile(travelLockBootMarker); err == nil && strings.TrimSpace(string(old)) == id {
		return false
	}
	if err := os.WriteFile(travelLockBootMarker, []byte(id+"\n"), 0o600); err != nil {
		return false // without the note a relaunch could not be told apart
	}
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(up))
	if len(fields) == 0 {
		return false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	return err == nil && secs < travelLockPowerOnWindow.Seconds()
}

// travelLockHookTemplate is the boot hook. @LOCKED@ is the locked reading,
// @BOARD@ the shell-quoted compatible string it was written on (” skips the
// board check), @APP_DIR@ the shell-quoted app folder, @GPIO@ the pin. Written
// for busybox ash and POSIX sh, safe sourced (one subshell), and quick: when
// the switch is open it costs two file reads. Go raw string: no backquote.
const travelLockHookTemplate = `#!/bin/sh
# Written by JukaHub's "Travel lock" setting (Settings > Power & battery).
# With the FN switch in the lock position this turns the device straight back
# off at power-on, so a power button pressed in a bag does not leave it on.
# Slide the FN switch back to start normally. Turn the setting off, or delete
# this file, to remove the guard. Each lock is noted in travel-lock.log in the
# JukaHub folder.
(
set +e +u +f
unset IFS
PATH="${PATH:+$PATH:}/usr/sbin:/usr/bin:/sbin:/bin"
LOCKED=@LOCKED@
BOARD=@BOARD@
APP_DIR=@APP_DIR@
G=/sys/class/gpio/gpio@GPIO@

# Only on the board this was written on: a card moved to another handheld
# must not power it off on a pin that means something else there.
if [ -n "$BOARD" ]; then
	b="$(tr -d '\000' </proc/device-tree/compatible 2>/dev/null)"
	[ "$b" = "$BOARD" ] || exit 0
fi
[ -e "$G/value" ] || echo @GPIO@ 2>/dev/null >/sys/class/gpio/export
d=
read -r d 2>/dev/null <"$G/direction"
[ "$d" = out ] && exit 0
v=
read -r v 2>/dev/null <"$G/value"
[ "$v" = "$LOCKED" ] || exit 0
# A second look a moment later: only a switch that stays put locks.
sleep 1
v=
read -r v 2>/dev/null <"$G/value"
[ "$v" = "$LOCKED" ] || exit 0

LOG="$APP_DIR/travel-lock.log"
if [ -f "$LOG" ]; then
	size="$(wc -c <"$LOG" 2>/dev/null | tr -d ' ')"
	[ "${size:-0}" -gt 16384 ] 2>/dev/null && mv -f "$LOG" "$LOG.1" 2>/dev/null
fi
printf '[%s] powered on with the FN switch in the lock position: powering off\n' "$(date '+%Y-%m-%d %H:%M:%S')" >>"$LOG" 2>/dev/null
# If JukaHub's start hook already ran at this boot, its start never happened:
# that must not count as a start that hung at the next boot.
[ -d /tmp/jukahub-autostart.lock ] && rm -f "$APP_DIR/.autostart-pending"
sync
if poweroff 2>/dev/null || busybox poweroff 2>/dev/null || /sbin/poweroff 2>/dev/null; then
	# Hold the boot here while init powers off, so the menu never comes up.
	sleep 20
fi
)
`
