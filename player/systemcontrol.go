package main

// systemcontrol.go — the device-control layer behind Quick Settings.
//
// Everything here touches the host: sysfs backlight, the audio mixer and the
// radio switches. The rules are the same as the rest of the app's safety model:
//
//   - no config value is ever used as a path. The backlight writer only looks
//     at direct children of one directory and only opens "brightness" inside
//     it, so a hand-edited config can never redirect it at another sysfs file;
//   - every binary is one of a small fixed set (pactl, amixer, nmcli, rfkill,
//     bluetoothctl). A command is chosen from that list, never composed from
//     user input;
//   - every probe and write is bounded by systemCommandTimeout and its failure
//     is reported to the caller, which turns it into a visible message instead
//     of a silent no-op;
//   - unavailable hardware is a normal state, not an error: the panel dims the
//     row and says so on this device.
//
// The layer is deliberately pure enough to test: brightness scaling, percent
// parsing and the backlight search take their inputs as arguments.
//
// The sysfs backlight here is one of the screen-brightness backends; the
// header's sun button, Quick Settings and the QR card all go through the
// brightness controller (brightnessctl.go), which picks between it, the
// Allwinner display driver the TrimUI handhelds actually use
// (brightness_disp_linux.go), Windows' own controls (brightness_windows.go)
// and a software dimmer.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// backlightRoot is the only directory the brightness writer may touch.
	backlightRoot = "/sys/class/backlight"
	// systemCommandTimeout bounds every probe and write so a wedged daemon
	// (bluetoothctl is a known offender) cannot freeze the render thread.
	systemCommandTimeout = 3 * time.Second
	// qsBrightnessFloorPct keeps a wrong input from turning the panel black on
	// a device with no physical way back.
	qsBrightnessFloorPct = 5
)

// systemCommandRunner runs one allowlisted command and returns its combined
// output. Tests replace it so no host is touched during a test run.
var systemCommandRunner = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// commandExists reports whether name is on PATH.
func commandExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// ── Backlight ──────────────────────────────────────────────────────────────

// sysfsClassChildren lists the device entries directly under a sysfs class
// directory such as /sys/class/backlight or /sys/class/leds, sorted so a device
// exposing two controllers always picks the same one.
//
// On a real kernel every entry there is a symlink into /sys/devices, and
// DirEntry.IsDir reports false for a link: the old IsDir-only filter therefore
// found no backlight and no LED on the Smart Pro or the Brick at all (the
// battery lookup had the same bug, see batterySysfsDirIn). A link is kept when
// it resolves to a directory; plain files and dangling links are dropped. The
// names are returned, not the link targets, so callers still only open fixed
// attribute names inside a child of the one root they were given — and sysfs
// links are made by the kernel, never by a config value.
func sysfsClassChildren(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.IsDir():
			names = append(names, e.Name())
		case e.Type()&os.ModeSymlink != 0:
			if info, err := os.Stat(filepath.Join(root, e.Name())); err == nil && info.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// findBacklight picks the backlight device inside root. Only direct children of
// root are considered (see sysfsClassChildren) and the entry must expose both
// max_brightness and brightness, so the returned directory is always a sysfs
// device and never a path the config could have chosen.
func findBacklight(root string) (string, int, error) {
	names, err := sysfsClassChildren(root)
	if err != nil {
		return "", 0, err
	}
	var lastErr error = fmt.Errorf("no backlight device under %s", root)
	for _, name := range names {
		dir := filepath.Join(root, name)
		rawMax, err := os.ReadFile(filepath.Join(dir, "max_brightness"))
		if err != nil {
			lastErr = err
			continue
		}
		max, err := strconv.Atoi(strings.TrimSpace(string(rawMax)))
		if err != nil || max <= 0 {
			lastErr = fmt.Errorf("%s: max_brightness %q is not a positive number", dir, strings.TrimSpace(string(rawMax)))
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "brightness")); err != nil {
			lastErr = err
			continue
		}
		return dir, max, nil
	}
	return "", 0, lastErr
}

// brightnessRawFor converts a 0..100 percentage to the device's raw scale,
// clamping to the floor and ceiling. Pure, so the mapping is testable.
func brightnessRawFor(pct, max int) int {
	if max <= 0 {
		return 0
	}
	if pct < qsBrightnessFloorPct {
		pct = qsBrightnessFloorPct
	}
	if pct > 100 {
		pct = 100
	}
	// Round rather than truncate: 50% of a 255-step device is 128, and reading
	// it back must not report 49%.
	raw := (pct*max + 50) / 100
	if raw < 1 {
		raw = 1
	}
	if raw > max {
		raw = max
	}
	return raw
}

// brightnessPercentFor is the inverse of brightnessRawFor.
func brightnessPercentFor(raw, max int) int {
	if max <= 0 {
		return 0
	}
	pct := (raw*100 + max/2) / max
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// sysfsBrightnessAvailable reports whether this device exposes a writable
// sysfs backlight (one of the brightness backends, brightnessFindBackend).
func sysfsBrightnessAvailable() bool {
	dir, _, err := findBacklight(backlightRoot)
	if err != nil {
		return false
	}
	// TryRead-only check would pass on a root-owned file we cannot write; report
	// availability from the open access we would need.
	return canWriteFile(filepath.Join(dir, "brightness"))
}

func canWriteFile(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// sysfsSetBacklightRaw writes a raw level to the sysfs backlight, kept inside
// 1..max_brightness: 0 is a dark panel. The rest of the app sets the level
// through the brightness controller (brightnessApplyLevel), which maps the
// percentage (brightnessRawForLevel) and calls this on its worker.
func sysfsSetBacklightRaw(raw int) error {
	dir, max, err := findBacklight(backlightRoot)
	if err != nil {
		return err
	}
	if raw < 1 {
		raw = 1
	}
	if raw > max {
		raw = max
	}
	return os.WriteFile(filepath.Join(dir, "brightness"), []byte(strconv.Itoa(raw)), 0o644)
}

// sysfsBacklightRaw reads the sysfs backlight's raw level and the top of its
// scale (max_brightness).
func sysfsBacklightRaw() (raw, max int, err error) {
	dir, max, err := findBacklight(backlightRoot)
	if err != nil {
		return 0, 0, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "brightness"))
	if err != nil {
		return 0, 0, err
	}
	raw, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, 0, err
	}
	return raw, max, nil
}

// ── Volume ─────────────────────────────────────────────────────────────────

// volumeBackend names the mixer this device exposes, best first: the
// platform's own (JukaHub's session volume on Windows, volume_windows.go), the
// TrimUI codec (volume_trimui.go), then PulseAudio and plain ALSA. It only
// looks for binaries, so it is cheap; what the TrimUI codec actually exposes
// is asked once, on the first read or write, off the render thread.
//
// The TrimUI case used to fall into the generic amixer path, which only knows
// Master and PCM. The tg5040 codec has neither, so every volume write on the
// Smart Pro and the Brick failed.
func volumeBackend() string {
	if b := volumePlatformBackend(); b != "" {
		return b
	}
	if IsTrimuiDevice() && commandExists("amixer") {
		return "trimui"
	}
	if commandExists("pactl") {
		return "pactl"
	}
	if commandExists("amixer") {
		return "amixer"
	}
	return ""
}

// setSystemVolume applies a 0..100 percentage through the available mixer.
// It may run a mixer process, so callers on the render thread go through
// volumeRequestLevel (volumectl.go) instead.
func setSystemVolume(pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	level := strconv.Itoa(pct) + "%"
	switch volumeBackend() {
	case "winmm":
		return winmmSetVolume(pct)
	case "trimui":
		return trimuiSetVolume(pct)
	case "pactl":
		if _, err := systemCommandRunner("pactl", "set-sink-volume", "@DEFAULT_SINK@", level); err == nil {
			return nil
		}
		// Fall through: a PulseAudio wrapper without a running daemon can exist
		// while ALSA still works.
		if commandExists("amixer") {
			return runAmixerSet(level)
		}
		return errors.New("pactl could not set the volume")
	case "amixer":
		return runAmixerSet(level)
	default:
		return errors.New("no audio mixer (pactl/amixer) on this device")
	}
}

// setSystemVolumeTransient applies a level that is only borrowed: the alarm's
// full volume for a ring, and the level it gives back afterwards
// (alarmvol.go). On the TrimUI it leaves the firmware's saved level
// (system.json) alone, because a crash or a power cut mid-ring must not leave
// the handheld coming back at 100%; everywhere else it is setSystemVolume.
func setSystemVolumeTransient(pct int) error {
	if volumeBackend() == "trimui" {
		return trimuiSetVolumeTransient(volumeClamp(pct))
	}
	return setSystemVolume(pct)
}

func runAmixerSet(level string) error {
	// Master first (what most ALSA setups expose), PCM second (common on
	// embedded codecs with no Master control).
	if _, err := systemCommandRunner("amixer", "-q", "sset", "Master", level); err == nil {
		return nil
	}
	if _, err := systemCommandRunner("amixer", "-q", "sset", "PCM", level); err != nil {
		return fmt.Errorf("amixer: %w", err)
	}
	return nil
}

// parseFirstPercent returns the first "NN%" in s, which is how both pactl and
// amixer print their levels. Pure.
func parseFirstPercent(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		j := i
		for j > 0 && s[j-1] >= '0' && s[j-1] <= '9' {
			j--
		}
		if j == i {
			continue
		}
		v, err := strconv.Atoi(s[j:i])
		if err != nil {
			continue
		}
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		return v, true
	}
	return 0, false
}

// systemVolume reads the current mixer level. Like setSystemVolume it may run
// a process, so the header reads it through volumeCurrent's background poll.
func systemVolume() (int, bool) {
	switch volumeBackend() {
	case "winmm":
		return winmmGetVolume()
	case "trimui":
		return trimuiVolume()
	case "pactl":
		if out, err := systemCommandRunner("pactl", "get-sink-volume", "@DEFAULT_SINK@"); err == nil {
			if v, ok := parseFirstPercent(out); ok {
				return v, true
			}
		}
		if commandExists("amixer") {
			break
		}
		return 0, false
	case "amixer":
	default:
		return 0, false
	}
	return amixerGenericVolume()
}

// amixerGenericVolume reads the level of the controls a desktop ALSA setup
// exposes, Master first and PCM second (runAmixerSet's order).
func amixerGenericVolume() (int, bool) {
	for _, ctl := range []string{"Master", "PCM"} {
		if out, err := systemCommandRunner("amixer", "get", ctl); err == nil {
			if v, ok := parseFirstPercent(out); ok {
				return v, true
			}
		}
	}
	return 0, false
}

// ── Radios ─────────────────────────────────────────────────────────────────

// wifiBackend names how Wi-Fi is switched on this device.
func wifiBackend() string {
	if commandExists("nmcli") {
		return "nmcli"
	}
	if commandExists("rfkill") {
		return "rfkill"
	}
	return ""
}

// bluetoothBackend names how Bluetooth is switched on this device.
func bluetoothBackend() string {
	if commandExists("bluetoothctl") {
		return "bluetoothctl"
	}
	if commandExists("rfkill") {
		return "rfkill"
	}
	return ""
}

// airplaneBackend reports whether rfkill can block every radio at once.
func airplaneBackend() bool { return commandExists("rfkill") }

// rfkillBlocked reports whether rfkill's output contains a soft block. The
// second result is false when rfkill printed nothing usable, which keeps "no
// radios" from being shown as "radios off".
func rfkillBlocked(out string) (blocked bool, ok bool) {
	if strings.TrimSpace(out) == "" {
		return false, false
	}
	if strings.Contains(out, "Soft blocked: yes") {
		return true, true
	}
	if strings.Contains(out, "Soft blocked: no") {
		return false, true
	}
	return false, false
}

// wifiEnabled reads the Wi-Fi radio state.
// wifiRadioBackend names how the Wi-Fi radio is switched. It is the same probe
// wifiBackend runs, behind a variable so a test can stand in for rfkill/nmcli
// exactly like systemCommandRunner.
var wifiRadioBackend = wifiBackend

func wifiEnabled() (bool, bool) {
	switch wifiRadioBackend() {
	case "nmcli":
		out, err := systemCommandRunner("nmcli", "radio", "wifi")
		if err != nil {
			return false, false
		}
		switch {
		case strings.Contains(out, "enabled"):
			return true, true
		case strings.Contains(out, "disabled"):
			return false, true
		}
		return false, false
	case "rfkill":
		out, err := systemCommandRunner("rfkill", "list", "wifi")
		if err != nil {
			return false, false
		}
		return rfkillState(out)
	}
	return false, false
}

// rfkillState turns rfkill output into an on/off answer. A list containing only
// "no" means the radio is up; any "yes" means it is blocked.
func rfkillState(out string) (bool, bool) {
	blocked, ok := rfkillBlocked(out)
	if !ok {
		return false, false
	}
	return !blocked, true
}

// setWifiEnabled switches the Wi-Fi radio.
func setWifiEnabled(on bool) error {
	switch wifiRadioBackend() {
	case "nmcli":
		arg := "off"
		if on {
			arg = "on"
		}
		if _, err := systemCommandRunner("nmcli", "radio", "wifi", arg); err == nil {
			return nil
		}
		if commandExists("rfkill") {
			return runRfkill("wifi", on)
		}
		return errors.New("nmcli could not change the Wi-Fi radio")
	case "rfkill":
		return runRfkill("wifi", on)
	}
	return errors.New("no Wi-Fi control (nmcli/rfkill) on this device")
}

// bluetoothEnabled reads the Bluetooth radio state.
func bluetoothEnabled() (bool, bool) {
	switch bluetoothBackend() {
	case "bluetoothctl":
		out, err := systemCommandRunner("bluetoothctl", "show")
		if err != nil {
			return false, false
		}
		switch {
		case strings.Contains(out, "Powered: yes"):
			return true, true
		case strings.Contains(out, "Powered: no"):
			return false, true
		}
		return false, false
	case "rfkill":
		out, err := systemCommandRunner("rfkill", "list", "bluetooth")
		if err != nil {
			return false, false
		}
		return rfkillState(out)
	}
	return false, false
}

// setBluetoothEnabled switches the Bluetooth radio.
func setBluetoothEnabled(on bool) error {
	switch bluetoothBackend() {
	case "bluetoothctl":
		arg := "off"
		if on {
			arg = "on"
		}
		if _, err := systemCommandRunner("bluetoothctl", "power", arg); err == nil {
			return nil
		}
		if commandExists("rfkill") {
			return runRfkill("bluetooth", on)
		}
		return errors.New("bluetoothctl could not change the radio")
	case "rfkill":
		return runRfkill("bluetooth", on)
	}
	return errors.New("no Bluetooth control (bluetoothctl/rfkill) on this device")
}

// airplaneEnabled reports whether every radio is soft-blocked.
func airplaneEnabled() (bool, bool) {
	if !airplaneBackend() {
		return false, false
	}
	out, err := systemCommandRunner("rfkill", "list")
	if err != nil {
		return false, false
	}
	if strings.TrimSpace(out) == "" {
		return false, false
	}
	yes := strings.Count(out, "Soft blocked: yes")
	no := strings.Count(out, "Soft blocked: no")
	if yes == 0 && no == 0 {
		return false, false
	}
	// Airplane mode means nothing is allowed to transmit.
	return yes > 0 && no == 0, true
}

// setAirplaneMode blocks or unblocks every radio.
func setAirplaneMode(on bool) error {
	if !airplaneBackend() {
		return errors.New("no rfkill on this device")
	}
	verb := "unblock"
	if on {
		verb = "block"
	}
	if _, err := systemCommandRunner("rfkill", verb, "all"); err != nil {
		return fmt.Errorf("rfkill %s all: %w", verb, err)
	}
	return nil
}

func runRfkill(kind string, on bool) error {
	// rfkill's argument is a block verb, not an on/off flag: switching a radio
	// ON is "unblock". These two were swapped, so on an image whose only radio
	// control is rfkill — every TrimUI handheld, which ships no nmcli — the
	// Wi-Fi switch turned the radio off every time it was asked to turn it on.
	verb := "block"
	if on {
		verb = "unblock"
	}
	if _, err := systemCommandRunner("rfkill", verb, kind); err != nil {
		return fmt.Errorf("rfkill %s %s: %w", verb, kind, err)
	}
	return nil
}

// ── Screen timeout ─────────────────────────────────────────────────────────

// screenTimeoutSteps are the values the screensaver row cycles through, in
// seconds. 25 is the shipped default, so it is the second stop for anyone who
// wants the device asleep almost as soon as they put it down. 0 would disable
// the screensaver, but the config validator treats a zero timeout as "unset"
// and restores the default, so the cycle never offers it.
var screenTimeoutSteps = []int{15, 25, 30, 60, 300, 600, 1800}

// screenTimeoutLabel renders a timeout for the panel.
func screenTimeoutLabel(sec int) string {
	switch {
	case sec <= 0:
		return "Off"
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec%60 == 0 && sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%dm %ds", sec/60, sec%60)
	}
}

// nextScreenTimeout returns the step after current, wrapping at the end. A value
// that is not a step (an old config, or 0) moves to the first step.
func nextScreenTimeout(current int) int {
	for _, s := range screenTimeoutSteps {
		if s > current {
			return s
		}
	}
	return screenTimeoutSteps[0]
}
