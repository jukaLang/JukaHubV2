package main

// The standalone image carries this marker in its read-only root filesystem.
// A card copied onto stock firmware does not become a native OS by accident.
//
// The image's boot scripts (os/plastron/launch.sh, os/smartpro/init and
// launch.sh) and JukaHub share one folder on the card, exported to JukaHub as
// USERDATA_PATH: boot.env (which radios the next boot starts), boot.log and the
// boot frame (bootframe.go) live there.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const jukaosNativeMarker = "/etc/jukahub-os-release"
const jukaosNativeRebootMarker = "/tmp/jukahub-native-reboot"

// jukaosNativeDataDefault is the shared folder when the launcher did not say:
// the same path both images' launch.sh export as USERDATA_PATH.
const jukaosNativeDataDefault = "/mnt/SDCARD/.userdata/jukahub"

// The plastron image's radio services and the names its launcher uses for
// them (os/plastron/launch.sh radios()).
const (
	jukaosWifiService  = "/etc/init.d/wpa_supplicant"
	jukaosBTService    = "/etc/init.d/bluetooth"
	jukaosWifiIface    = "wlan0"
	jukaosUdhcpcPid    = "/var/run/udhcpc.wlan0.pid"
	jukaosServiceLimit = 15 * time.Second
	// jukaosWifiJoinWait is how long a started supplicant gets to join a saved
	// network before the address is asked for (launch.sh waits as long).
	jukaosWifiJoinWait = 25 * time.Second
	// jukaosDHCPLimit lets udhcpc's own tries (-t 5 -T 7: 35 s) end in its
	// going to the background, as in launch.sh, rather than being killed.
	jukaosDHCPLimit = 45 * time.Second
)

var jukaosNativeActive = sync.OnceValue(func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile(jukaosNativeMarker)
	return err == nil && strings.Contains(string(data), "JUKAHUB_OS_NATIVE=1\n")
})

// jukaosNativeBase is the marker's BASE= value: "plastron" (Brick) or
// "smartpro-stock-kernel" (the Smart Pro's own kernel), "" when unknown.
var jukaosNativeBase = sync.OnceValue(func() string {
	data, err := os.ReadFile(jukaosNativeMarker)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "BASE="); ok {
			return v
		}
	}
	return ""
})

// jukaosNativeBaseSmartPro is BASE= on the Smart Pro image
// (os/smartpro/build_image.py transform_root).
const jukaosNativeBaseSmartPro = "smartpro-stock-kernel"

// jukaosNativeSmartPro reports a boot of the Smart Pro image: the handheld's
// own U-Boot and kernel, read from the card (bootframe_ubootlogo.go).
func jukaosNativeSmartPro() bool {
	return jukaosNativeActive() && jukaosNativeBase() == jukaosNativeBaseSmartPro
}

// jukaosNativeBaseLabel says what the standalone image is built on, from the
// BASE= line of its marker: plastron (Brick) or the Smart Pro's own kernel.
func jukaosNativeBaseLabel() string {
	switch jukaosNativeBase() {
	case "smartpro-stock-kernel":
		return "JukaHub boots on the Smart Pro's own kernel"
	case "plastron":
		return "JukaHub boots on plastron"
	}
	return "JukaHub boots as the system"
}

func jukaosNativeBootSeconds() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	value, _, _ := strings.Cut(string(data), " ")
	return strings.TrimSpace(value)
}

// jukaosNativeDataDir is the folder JukaHub shares with the image's boot
// scripts: USERDATA_PATH as launch.sh exported it, else the path it exports.
func jukaosNativeDataDir() string {
	if dir := filepath.Clean(os.Getenv("USERDATA_PATH")); filepath.IsAbs(dir) && dir != "/" {
		return dir
	}
	return jukaosNativeDataDefault
}

// ── boot.env: the radios the next boot starts ─────────────────────────────

// jukaosBootEnvMu keeps two radio switches from interleaving their rewrites.
var jukaosBootEnvMu sync.Mutex

// jukaosBootEnvWith is boot.env with KEY=value: the first KEY= line is
// replaced (keeping its line ending, the file may come from Windows), later
// ones are dropped because the scripts' read loop lets the last one win, and
// every other line — comments, keys a newer image reads — stays as it was. A
// missing key is appended.
func jukaosBootEnvWith(old, key, value string) string {
	lines := strings.Split(old, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out := make([]string, 0, len(lines)+1)
	done := false
	for _, line := range lines {
		bare := strings.TrimSuffix(line, "\r")
		k, _, ok := strings.Cut(bare, "=")
		if !ok || strings.TrimSpace(k) != key {
			out = append(out, line)
			continue
		}
		if done {
			continue
		}
		done = true
		out = append(out, key+"="+value+line[len(bare):])
	}
	if !done {
		out = append(out, key+"="+value)
	}
	return strings.Join(out, "\n") + "\n"
}

// jukaosNativeBootEnvSet writes KEY=value into boot.env atomically. Off the
// main thread (a card write).
func jukaosNativeBootEnvSet(key, value string) error {
	path := filepath.Join(jukaosNativeDataDir(), "boot.env")
	jukaosBootEnvMu.Lock()
	defer jukaosBootEnvMu.Unlock()
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := jukaosBootEnvWith(string(old), key, value)
	if next == string(old) {
		return nil
	}
	return AtomicWrite(path, []byte(next), 0644)
}

// jukaosNativeRadioIntent keeps a radio the player switched in JukaHub the way
// it was for the next boot of JukaHub OS: the launcher starts Wi-Fi
// (WIFI=1) and Bluetooth (BLUETOOTH=1) only when boot.env asks. A no-op
// outside the standalone image. Radio workers call it, never the main thread.
func jukaosNativeRadioIntent(key string, on bool) {
	if !jukaosNativeActive() {
		return
	}
	value := "0"
	if on {
		value = "1"
	}
	if err := jukaosNativeBootEnvSet(key, value); err != nil {
		logWarn("jukaos", "boot.env: %s=%s not saved: %v", key, value, err)
	}
}

// ── Radios on the plastron image ──────────────────────────────────────────

// jukaosNativeWifiService reports whether Wi-Fi on this boot is plastron's
// service: there no rfkill or nmcli switch is promised, the supplicant is the
// radio, and JukaHub starts and stops it as the image's launcher does.
func jukaosNativeWifiService() bool {
	return jukaosNativeActive() && jukaosNativeBase() == "plastron" && fileExists(jukaosWifiService)
}

// jukaosNativeWifiState is wifiEnabled's answer on the plastron image: on
// while the supplicant runs. native is false elsewhere.
func jukaosNativeWifiState() (on, known, native bool) {
	if !jukaosNativeWifiService() {
		return false, false, false
	}
	return btProcessRunning("/proc", "wpa_supplicant"), true, true
}

// jukaosNativeSetWifi is setWifiEnabled's first step on JukaHub OS: the choice
// is kept in boot.env, and on plastron the radio is its service (handled).
// On the Smart Pro image the stock switch follows (handled false). Workers.
func jukaosNativeSetWifi(on bool) (handled bool, err error) {
	if !jukaosNativeActive() {
		return false, nil
	}
	jukaosNativeRadioIntent("WIFI", on)
	if !jukaosNativeWifiService() {
		return false, nil
	}
	// A network change like any radio switch (setWifiEnabled, netmon.go).
	defer netNetworkSwitching()()
	if !on {
		jukaosNativeStopDHCP()
		err = jukaosNativeService(jukaosWifiService, "stop")
		resetWiFiInterfaceCache()
		return true, err
	}
	if btProcessRunning("/proc", "wpa_supplicant") {
		return true, nil
	}
	if err = jukaosNativeService(jukaosWifiService, "start"); err != nil {
		return true, err
	}
	resetWiFiInterfaceCache()
	go jukaosNativeWifiAddress()
	return true, nil
}

// jukaosNativeWifiAddress asks for an address once the supplicant has joined
// a saved network, with the launcher's own udhcpc command (a background
// client that keeps the lease; JukaHub's joins renew it, wifi.go).
func jukaosNativeWifiAddress() {
	end := time.Now().Add(jukaosWifiJoinWait)
	for time.Now().Before(end) {
		time.Sleep(time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, _ := hiddenCommandContext(ctx, "wpa_cli", "-i", jukaosWifiIface, "status").Output()
		cancel()
		if !strings.Contains(string(out), "wpa_state=COMPLETED") {
			continue
		}
		if jukaosUdhcpcRunning() {
			return
		}
		// Detached, with the system's environment and no pipes: the client
		// stays behind as a daemon (btRunBounded).
		err := btRunBounded(context.Background(), jukaosDHCPLimit, "udhcpc", "-i", jukaosWifiIface, "-S",
			"-t", "5", "-T", "7", "-b", "-p", jukaosUdhcpcPid)
		if err != nil {
			logWarn("jukaos", "udhcpc on %s: %v", jukaosWifiIface, err)
		}
		netNudge()
		return
	}
}

// jukaosUdhcpcPID is the launcher's udhcpc, 0 when none runs.
func jukaosUdhcpcPID() int {
	data, err := os.ReadFile(jukaosUdhcpcPid)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0
	}
	// A pid file left behind may name a reused number: only udhcpc counts.
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil || strings.TrimSpace(string(comm)) != "udhcpc" {
		return 0
	}
	return pid
}

func jukaosUdhcpcRunning() bool { return jukaosUdhcpcPID() > 0 }

// jukaosNativeStopDHCP ends the launcher's udhcpc with the radio.
func jukaosNativeStopDHCP() {
	if pid := jukaosUdhcpcPID(); pid > 0 {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(os.Interrupt)
		}
	}
}

// jukaosNativeBluetoothOn is btBringUp's first step on JukaHub OS: Bluetooth
// switched on is kept for the next boot, and on plastron its service is
// started as the launcher does, so the bring-up finds an adapter and a
// bluetoothd instead of attaching the chip the stock firmware's way. remember
// is false for a short scan that restores the radio and must leave no boot
// intent behind. Workers.
func jukaosNativeBluetoothOn(ctx context.Context, remember bool) {
	if !jukaosNativeActive() {
		return
	}
	// A probe that switches the radio on for a scan must not write a boot
	// intent: the user left Bluetooth off, and the probe switches it off again.
	if remember {
		jukaosNativeRadioIntent("BLUETOOTH", true)
	}
	if jukaosNativeBase() != "plastron" || !fileExists(jukaosBTService) {
		return
	}
	if btAdapterPresent() && btDaemonRunning() {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := jukaosNativeService(jukaosBTService, "start"); err != nil {
		logWarn("jukaos", "bluetooth service: %v", err)
	}
}

// jukaosNativeService runs one of the image's init scripts, bounded. Through
// btRunBounded: the daemon a script starts must not hold a pipe JukaHub waits
// on, nor inherit JukaHub's own libraries (launch.sh's LD_LIBRARY_PATH).
func jukaosNativeService(script, verb string) error {
	if err := btRunBounded(context.Background(), jukaosServiceLimit, script, verb); err != nil {
		return errors.New(filepath.Base(script) + " " + verb + ": " + err.Error())
	}
	logInfo("jukaos", "%s %s", script, verb)
	return nil
}
