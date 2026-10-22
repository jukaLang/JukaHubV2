package main

// lowbattery.go — the last 3% protection layer.
//
// A handheld that dies mid-save corrupts the SD card, and RetroArch writes to
// that card constantly. So when the battery gets critical *while a game is
// running*, the guard:
//
//  1. asks RetroArch to write a save state and quit, through its network
//     command interface (UDP on 127.0.0.1, `network_cmd_enable` in
//     retroarch.cfg);
//  2. syncs the filesystems once the emulator has exited;
//  3. suspends the device (or explains why it could not).
//
// The guard is deliberately conservative:
//   - it never acts while charging, or when the charge level is unknown;
//   - it only auto-saves/suspends when a game is actually running, so reading a
//     note at 2% does not put the device to sleep without warning;
//   - it acts at most once per launch (a failed save must not loop forever on
//     an SD card that is about to lose power);
//   - the suspend write is bounded and never powers the device off. A hard
//     shutdown at an unknown point is exactly what this file exists to avoid.
//
// Everything that decides *what* to do is pure and tested; the I/O half is
// small, bounded and platform-guarded.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// criticalBatteryPct is where the guard saves and suspends.
	criticalBatteryPct = 3
	// lowBatteryWarnPct is where the user is told to plug in.
	lowBatteryWarnPct = 5

	// retroArchDefaultNetCmdPort is RetroArch's own default for
	// `network_cmd_port`; a config that never mentions the port uses it.
	retroArchDefaultNetCmdPort = 55355

	// lowBatteryNetCmdTimeout bounds the UDP round trip. The interface is
	// local, so a missing listener fails immediately; the timeout only covers
	// a wedged emulator.
	lowBatteryNetCmdTimeout = 900 * time.Millisecond
	// lowBatteryQuitWait is how long the guard waits for the emulator to exit
	// after QUIT before syncing and suspending anyway.
	lowBatteryQuitWait = 8 * time.Second
	// lowBatterySaveDelay gives RetroArch a moment to flush the state file
	// before QUIT is sent.
	lowBatterySaveDelay = 700 * time.Millisecond
	// lowBatterySyncTimeout bounds `sync`, which can hang on a failing card.
	lowBatterySyncTimeout = 15 * time.Second
)

// batteryGuardAction is what the guard wants to do at the current charge.
type batteryGuardAction int

const (
	batteryGuardIdle batteryGuardAction = iota
	batteryGuardWarn
	batteryGuardProtect
)

// batteryGuardActionFor is the whole decision, kept pure so every edge case is
// a table row instead of a handheld that sleeps in someone's hands.
func batteryGuardActionFor(percent int, charging, gameRunning, alreadyActed, alreadyWarned bool) batteryGuardAction {
	if percent < 0 || charging {
		return batteryGuardIdle
	}
	if percent > lowBatteryWarnPct {
		return batteryGuardIdle
	}
	if percent <= criticalBatteryPct && gameRunning && !alreadyActed {
		return batteryGuardProtect
	}
	if alreadyWarned || alreadyActed {
		return batteryGuardIdle
	}
	return batteryGuardWarn
}

// lowBatteryGuardEnabled reads the setting; it defaults to on, like the battery
// saver, because the failure it prevents (a corrupted SD card) is not something
// most people would know to opt into.
func lowBatteryGuardEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "lowBatteryGuard"); ok {
		return coerceBool(v)
	}
	return true
}

// --- RetroArch configuration -------------------------------------------------

// networkCmdEnabled reports whether a retroarch.cfg enables the network command
// interface. RetroArch writes `key = "value"` lines; both quoted and bare
// values are accepted because hand-edited configs contain both.
func networkCmdEnabled(cfg string) bool {
	v, ok := retroArchConfigValue(cfg, "network_cmd_enable")
	return ok && strings.EqualFold(v, "true")
}

// networkCmdPort reads `network_cmd_port`, falling back to RetroArch's default
// when the key is missing, unparsable or out of range.
func networkCmdPort(cfg string) int {
	v, ok := retroArchConfigValue(cfg, "network_cmd_port")
	if ok {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return retroArchDefaultNetCmdPort
}

// retroArchConfigValue finds the last occurrence of key, which is the one
// RetroArch itself would apply, and strips quotes and trailing comments.
func retroArchConfigValue(cfg, key string) (string, bool) {
	value, found := "", false
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(line[:eq]), key) {
			continue
		}
		v := strings.TrimSpace(line[eq+1:])
		if i := strings.IndexByte(v, '#'); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		v = strings.Trim(v, `"'`)
		value, found = strings.TrimSpace(v), true
	}
	return value, found
}

// retroArchConfigCandidates lists the config locations the handheld firmwares
// and desktop builds use, best first. JUKAHUB_RETROARCH_CFG wins when set.
func retroArchConfigCandidates() []string {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_RETROARCH_CFG")); override != "" {
		return []string{override}
	}
	var out []string
	home, _ := os.UserHomeDir()
	for _, base := range []string{"/mnt/SDCARD", "/mnt/sdcard"} {
		out = append(out,
			filepath.Join(base, "RetroArch", "retroarch.cfg"),
			filepath.Join(base, "retroarch", "retroarch.cfg"),
			filepath.Join(base, "Emus", "RetroArch", "retroarch.cfg"),
		)
	}
	if home != "" {
		out = append(out,
			filepath.Join(home, ".config", "retroarch", "retroarch.cfg"),
			filepath.Join(home, ".retroarch", "retroarch.cfg"),
		)
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		out = append(out, filepath.Join(dir, "RetroArch", "retroarch.cfg"))
	}
	return out
}

// retroArchConfig reads the first readable candidate. It returns the text, the
// path it came from, and whether one was found at all.
func retroArchConfig() (text, path string, ok bool) {
	for _, candidate := range retroArchConfigCandidates() {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		return string(data), candidate, true
	}
	return "", "", false
}

// --- Emulator detection ------------------------------------------------------

// retroArchCmdline reports whether a /proc/<pid>/cmdline belongs to RetroArch.
// The first NUL-separated field is the executable, which keeps a ROM called
// "retroarch-demo.zip" passed as an argument from counting as the emulator.
func retroArchCmdline(cmdline []byte) bool {
	if len(cmdline) == 0 {
		return false
	}
	argv0 := string(cmdline)
	if i := strings.IndexByte(argv0, 0); i >= 0 {
		argv0 = argv0[:i]
	}
	// Split on both separators by hand: filepath.Base only knows the host OS's
	// separator, so a Windows path would keep its directory on Linux.
	name := strings.TrimSpace(argv0)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	return name == "retroarch" || name == "retroarch.exe"
}

// retroArchRunning reports whether any process is running RetroArch. Only Linux
// has /proc, and the desktop builds do not need this guard, so other platforms
// answer false.
func retroArchRunning() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	self := strconv.Itoa(os.Getpid())
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if name == self || name[0] < '0' || name[0] > '9' {
			continue
		}
		checked++
		if checked > 512 { // bounded scan; handhelds never run this many processes
			break
		}
		data, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil || len(data) > 4096 {
			continue
		}
		if retroArchCmdline(data) {
			return true
		}
	}
	return false
}

// --- Network commands --------------------------------------------------------

// sendRetroArchNetCmd writes one command per line to RetroArch's command port.
// RetroArch does not acknowledge, so a successful write is the whole contract;
// the caller verifies the effect by watching the process exit.
func sendRetroArchNetCmd(port int, commands ...string) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid RetroArch command port %d", port)
	}
	conn, err := net.DialTimeout("udp", "127.0.0.1:"+strconv.Itoa(port), lowBatteryNetCmdTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(lowBatteryNetCmdTimeout))
	for _, cmd := range commands {
		if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
			return err
		}
	}
	return nil
}

// --- Device actions ----------------------------------------------------------

// suspendStates lists the suspend states a Linux device may advertise, most
// efficient first. Both keep RAM alive, so the save state on disk is a backup
// rather than the only copy.
var suspendStates = []string{"mem", "freeze"}

// pickSuspendState returns the state to write into /sys/power/state, or "" when
// the device supports neither.
func pickSuspendState(available string) string {
	fields := strings.Fields(available)
	for _, want := range suspendStates {
		for _, have := range fields {
			if have == want {
				return want
			}
		}
	}
	return ""
}

// suspendDevice puts the device into the first supported suspend state. It is
// Linux-only because /sys/power/state is; other platforms report that they
// cannot suspend rather than pretending they did.
func suspendDevice() error {
	if runtime.GOOS != "linux" {
		return errors.New("suspend is only implemented on Linux")
	}
	data, err := os.ReadFile("/sys/power/state")
	if err != nil {
		return fmt.Errorf("read /sys/power/state: %w", err)
	}
	state := pickSuspendState(string(data))
	if state == "" {
		return fmt.Errorf("device supports no suspend state (available: %s)", strings.TrimSpace(string(data)))
	}
	if err := os.WriteFile("/sys/power/state", []byte(state), 0o644); err != nil {
		return fmt.Errorf("suspend (%s): %w", state, err)
	}
	return nil
}

// syncDisks flushes dirty pages so an interrupted save cannot leave a torn file
// behind. Failure is reported but is not fatal: the suspend still saves power.
func syncDisks() error {
	if runtime.GOOS == "windows" {
		return nil // NTFS handles this itself and `sync` does not exist
	}
	ctx, cancel := context.WithTimeout(context.Background(), lowBatterySyncTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "sync").Run(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// --- Guard -------------------------------------------------------------------

type lowBatteryGuardState struct {
	mu      sync.Mutex
	acted   bool
	warned  bool
	running bool
}

var lowBatteryGuard lowBatteryGuardState

func lowBatteryGuardFlags() (acted, warned, running bool) {
	lowBatteryGuard.mu.Lock()
	defer lowBatteryGuard.mu.Unlock()
	return lowBatteryGuard.acted, lowBatteryGuard.warned, lowBatteryGuard.running
}

func markLowBatteryWarned() bool {
	lowBatteryGuard.mu.Lock()
	defer lowBatteryGuard.mu.Unlock()
	if lowBatteryGuard.warned {
		return false
	}
	lowBatteryGuard.warned = true
	return true
}

// resetLowBatteryWarning lets the warning fire again after the charge has
// recovered, so a user who plugged in and unplugged is warned a second time.
func resetLowBatteryWarning() {
	lowBatteryGuard.mu.Lock()
	lowBatteryGuard.warned = false
	lowBatteryGuard.mu.Unlock()
}

// beginLowBatteryProtect claims the one protection run this launch gets.
func beginLowBatteryProtect() bool {
	lowBatteryGuard.mu.Lock()
	defer lowBatteryGuard.mu.Unlock()
	if lowBatteryGuard.acted || lowBatteryGuard.running {
		return false
	}
	lowBatteryGuard.running = true
	lowBatteryGuard.acted = true
	return true
}

func endLowBatteryProtect() {
	lowBatteryGuard.mu.Lock()
	lowBatteryGuard.running = false
	lowBatteryGuard.mu.Unlock()
}

// lowBatteryTick is called from the once-a-second widget tick. It is cheap: the
// battery read already happens for the saver, and the /proc scan only runs when
// the charge is actually low.
func lowBatteryTick(config *Config) {
	if !lowBatteryGuardEnabled(config) {
		return
	}
	percent := getBatteryPercent()
	if percent < 0 || percent > lowBatteryWarnPct+2 {
		resetLowBatteryWarning()
		return
	}
	charging := getBatteryCharging()
	if charging {
		resetLowBatteryWarning()
		return
	}
	acted, warned, _ := lowBatteryGuardFlags()
	switch batteryGuardActionFor(percent, charging, retroArchRunning(), acted, warned) {
	case batteryGuardWarn:
		if markLowBatteryWarned() {
			showToast(lowBatteryMessage(config, percent), ToastWarn())
		}
	case batteryGuardProtect:
		if beginLowBatteryProtect() {
			showToast("Battery critical — saving and suspending", ToastWarn())
			go func() {
				defer endLowBatteryProtect()
				protectLowBattery()
			}()
		}
	}
}

// lowBatteryMessage explains the warning, including the one thing the user can
// fix: RetroArch's network command interface, without which no save is possible.
func lowBatteryMessage(config *Config, percent int) string {
	msg := "Battery low — plug in now"
	if percent >= 0 {
		msg = fmt.Sprintf("Battery %d%% — plug in now", percent)
	}
	if cfg, _, ok := retroArchConfig(); ok && !networkCmdEnabled(cfg) {
		return msg + " (auto-save is off: enable network commands in RetroArch)"
	}
	return msg
}

// protectLowBattery saves, syncs and suspends. It runs off the render thread.
func protectLowBattery() {
	saved := false
	cfg, cfgPath, haveCfg := retroArchConfig()
	if haveCfg && networkCmdEnabled(cfg) {
		port := networkCmdPort(cfg)
		if err := sendRetroArchNetCmd(port, "SAVE_STATE"); err == nil {
			saved = true
			Log().Info("low battery: save state requested", "port", port, "config", cfgPath)
			time.Sleep(lowBatterySaveDelay)
			_ = sendRetroArchNetCmd(port, "QUIT")
			// A UDP write always succeeds, so the exit is the only proof the
			// command reached RetroArch. Without it, say the save did not happen.
			if !waitForRetroArchExit(lowBatteryQuitWait) {
				saved = false
				Log().Warn("low battery: RetroArch did not exit after QUIT")
			}
		} else {
			Log().Warn("low battery: RetroArch command failed", "port", port, "err", err)
		}
	} else if !haveCfg {
		Log().Warn("low battery: no retroarch.cfg found; skipping save state")
	} else {
		Log().Warn("low battery: network commands disabled in RetroArch; skipping save state", "config", cfgPath)
	}
	if !saved {
		showToast("Could not save state — suspending without it", ToastWarn())
	}
	if err := syncDisks(); err != nil {
		Log().Warn("low battery: sync failed", "err", err)
	}
	if err := suspendDevice(); err != nil {
		Log().Warn("low battery: suspend failed", "err", err)
		showToast("Plug in now — this device could not suspend", ToastWarn())
	}
}

// waitForRetroArchExit polls until RetroArch is gone or the budget runs out, so
// the sync starts after the emulator has closed its files. It reports whether
// the emulator actually exited.
func waitForRetroArchExit(budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !retroArchRunning() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
