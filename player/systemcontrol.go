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

// findBacklight picks the backlight device inside root. Only direct children of
// root are considered and the entry must expose both max_brightness and
// brightness; symlinked entries are skipped, so the returned directory is
// always a real sysfs device and never a path the config could have chosen.
func findBacklight(root string) (string, int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", 0, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// Sorted so a device exposing two controllers always picks the same one.
	sort.Strings(names)
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

// brightnessAvailable reports whether this device exposes a writable backlight.
func brightnessAvailable() bool {
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

// setScreenBrightness writes a 0..100 percentage to the backlight device.
func setScreenBrightness(pct int) error {
	dir, max, err := findBacklight(backlightRoot)
	if err != nil {
		return err
	}
	raw := brightnessRawFor(pct, max)
	if err := os.WriteFile(filepath.Join(dir, "brightness"), []byte(strconv.Itoa(raw)), 0o644); err != nil {
		return err
	}
	return nil
}

// screenBrightness reads the current backlight level as a percentage.
func screenBrightness() (int, bool) {
	dir, max, err := findBacklight(backlightRoot)
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(dir, "brightness"))
	if err != nil {
		return 0, false
	}
	raw, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return brightnessPercentFor(raw, max), true
}

// ── Volume ─────────────────────────────────────────────────────────────────

// volumeBackend names the mixer this device exposes, best first.
func volumeBackend() string {
	if commandExists("pactl") {
		return "pactl"
	}
	if commandExists("amixer") {
		return "amixer"
	}
	return ""
}

// setSystemVolume applies a 0..100 percentage through the available mixer.
func setSystemVolume(pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	level := strconv.Itoa(pct) + "%"
	switch volumeBackend() {
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

// systemVolume reads the current mixer level.
func systemVolume() (int, bool) {
	switch volumeBackend() {
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
func wifiEnabled() (bool, bool) {
	switch wifiBackend() {
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
	switch wifiBackend() {
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
	verb := "unblock"
	if on {
		verb = "block"
	}
	// rfkill's argument is a block verb; "block" turns the radio off.
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
