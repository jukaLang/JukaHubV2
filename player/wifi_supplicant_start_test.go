package main

// wifi_supplicant_start_test.go — the supplicant route on an image that starts
// its own supplicant, which is how the TrimUI Brick is built.
//
// The Brick has a radio and a wpa_supplicant but no wpa_cli, and the supplicant
// is started by the firmware's own Wi-Fi menu — not at boot. On a device that has
// never had Wi-Fi switched on there, every source the picker knew was empty: no
// socket to name the interface, no CLI, and nothing to scan with. That state must
// (a) still be reachable from the menu, and (b) be fixed by the picker starting
// the supplicant itself, so the control socket it needs actually exists.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// supplicantImage is a fixture standing in for one TrimUI image: a sysfs net
// root, the supplicant's control directory, and its config.
type supplicantImage struct {
	root    string
	net     string
	sockets string
	config  string
}

// withSupplicantImage presents the Brick shape: the driver has created wlan0
// (with a phy80211 marker), the image ships wpa_supplicant but no wpa_cli, the
// config points the control socket at the fixture directory, and that directory
// starts empty because nothing has started the supplicant yet.
func withSupplicantImage(t *testing.T) *supplicantImage {
	t.Helper()
	root := t.TempDir()
	img := &supplicantImage{
		root:    root,
		net:     filepath.Join(root, "net"),
		sockets: filepath.Join(root, "sockets"),
		config:  filepath.Join(root, "wpa_supplicant.conf"),
	}
	if err := os.MkdirAll(filepath.Join(img.net, "wlan0", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(img.sockets, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# firmware config\nctrl_interface=DIR=" + img.sockets + " GROUP=netdev\nupdate_config=1\n"
	if err := os.WriteFile(img.config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	prevRoot, prevProc := wifiSysfsNetRoot, wifiProcWirelessPath
	prevDirs, prevConfigs := wifiSupplicantDirs, wifiSupplicantConfigs
	prevExists, prevRadio, prevSwitch := wifiToolExists, wifiRadioState, wifiRadioSwitchable
	prevBackend, prevSystem := wifiRadioBackend, systemCommandRunner

	wifiSysfsNetRoot = img.net
	wifiProcWirelessPath = filepath.Join(root, "no-proc-wireless")
	wifiSupplicantDirs = []string{img.sockets}
	wifiSupplicantConfigs = []string{img.config}
	// The image ships the supplicant and no client to it.
	wifiToolExists = func(name string) bool { return name == "wpa_supplicant" }
	wifiRadioState = func() (bool, bool) { return true, true }
	wifiRadioSwitchable = func() bool { return true }
	wifiRadioBackend = func() string { return "rfkill" }
	systemCommandRunner = func(name string, args ...string) (string, error) { return "", nil }
	t.Setenv("JUKAHUB_WIFI_IFACE", "")

	resetSupplicantInterfaceCache()
	resetSupplicantStart("wlan0")
	t.Cleanup(func() {
		wifiSysfsNetRoot, wifiProcWirelessPath = prevRoot, prevProc
		wifiSupplicantDirs, wifiSupplicantConfigs = prevDirs, prevConfigs
		wifiToolExists, wifiRadioState, wifiRadioSwitchable = prevExists, prevRadio, prevSwitch
		wifiRadioBackend, systemCommandRunner = prevBackend, prevSystem
		resetSupplicantInterfaceCache()
		resetSupplicantStart("wlan0")
	})
	return img
}

// A control socket in the directory the supplicant's own config names must name
// the interface even when none of the conventional directories holds one.
// Discovery used to read the conventional list only, while command resolution
// read the config — so the two disagreed about the same device.
func TestSupplicantSocketInConfigDirNamesTheInterface(t *testing.T) {
	root := t.TempDir()
	net := filepath.Join(root, "net")
	if err := os.MkdirAll(net, 0o755); err != nil {
		t.Fatal(err)
	}
	ctrl := filepath.Join(root, "image-ctrl")
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctrl, "wlan0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(root, "wpa_supplicant.conf")
	if err := os.WriteFile(conf, []byte("ctrl_interface=DIR="+ctrl+" GROUP=netdev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevRoot, prevProc := wifiSysfsNetRoot, wifiProcWirelessPath
	prevDirs, prevConfigs := wifiSupplicantDirs, wifiSupplicantConfigs
	prevExists := wifiToolExists
	wifiSysfsNetRoot = net
	wifiProcWirelessPath = filepath.Join(root, "no-proc-wireless")
	wifiSupplicantDirs = nil // nothing in the conventional places
	wifiSupplicantConfigs = []string{conf}
	wifiToolExists = func(string) bool { return false }
	resetSupplicantInterfaceCache()
	t.Cleanup(func() {
		wifiSysfsNetRoot, wifiProcWirelessPath = prevRoot, prevProc
		wifiSupplicantDirs, wifiSupplicantConfigs = prevDirs, prevConfigs
		wifiToolExists = prevExists
		resetSupplicantInterfaceCache()
	})

	if got := supplicantControlDirs(); len(got) == 0 || got[0] != ctrl {
		t.Fatalf("supplicantControlDirs = %v, want the config's %q first", got, ctrl)
	}
	if got := wifiInterface(); got != "wlan0" {
		t.Fatalf("wifiInterface = %q, want the interface the socket names", got)
	}
	if got := supplicantCtrlDir("wlan0"); got != ctrl {
		t.Fatalf("supplicantCtrlDir = %q, want %q", got, ctrl)
	}
}

// wpa_supplicant publishes a P2P-device socket and a global socket beside the
// station's. Both look like interface names and both sort ahead of "wlan0", so
// taking the first entry by name aimed every command at an object that cannot
// scan.
func TestSupplicantSocketNamesSkipP2PAndGlobalSockets(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"p2p-dev-wlan0", "wlan0_global", "wlan0"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := supplicantInterfaceNames([]string{dir}); len(got) != 1 || got[0] != "wlan0" {
		t.Fatalf("supplicantInterfaceNames = %v, want only wlan0", got)
	}

	only := t.TempDir()
	for _, name := range []string{"p2p-dev-wlan0", "wlan0_global"} {
		if err := os.WriteFile(filepath.Join(only, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := supplicantInterfaceNames([]string{only}); len(got) != 0 {
		t.Fatalf("supplicantInterfaceNames = %v, want no station interface", got)
	}

	// A station-looking name still comes first when a neighbour sorts ahead.
	mixed := t.TempDir()
	for _, name := range []string{"aaa0", "wlan0"} {
		if err := os.WriteFile(filepath.Join(mixed, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := supplicantInterfaceNames([]string{mixed}); len(got) != 2 || got[0] != "wlan0" {
		t.Fatalf("supplicantInterfaceNames = %v, want wlan0 ranked first", got)
	}
}

// The picker starts the supplicant the image ships, with the arguments this
// hardware uses, then finds the socket it just created. It does that once.
func TestEnsureWifiBackendStartsTheSupplicantOnce(t *testing.T) {
	img := withSupplicantImage(t)

	var runs int
	var args []string
	prevRunner := wifiRunner
	wifiRunner = func(_ time.Duration, _, name string, a ...string) (string, error) {
		runs++
		args = append([]string(nil), a...)
		if name != "wpa_supplicant" {
			return "", errors.New("unexpected command " + name)
		}
		// The daemon creates the control socket as it initialises.
		return "", os.WriteFile(filepath.Join(img.sockets, "wlan0"), nil, 0o600)
	}
	t.Cleanup(func() { wifiRunner = prevRunner })

	if got := wifiCommandBackend(); got != "" {
		t.Fatalf("backend = %q before anything started the supplicant, want none", got)
	}
	if !ensureWifiBackend() {
		t.Fatal("ensureWifiBackend did not start the supplicant on an image that has one")
	}
	if runs != 1 {
		t.Fatalf("wpa_supplicant started %d times, want 1", runs)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-B", "-D nl80211", "-i wlan0", "-c " + img.config, "-O " + img.sockets} {
		if !strings.Contains(joined, want) {
			t.Errorf("wpa_supplicant argv = %q, want it to contain %q", joined, want)
		}
	}
	if got := wifiCommandBackend(); got != wifiBackendSocket {
		t.Fatalf("backend = %q after the start, want the supplicant socket", got)
	}

	// A second visit must not start a second supplicant.
	if ensureWifiBackend() {
		t.Fatal("ensureWifiBackend started the supplicant a second time")
	}
	if runs != 1 {
		t.Fatalf("wpa_supplicant started %d times after a second call, want 1", runs)
	}
}

// A radio the firmware has switched off is not something to start a supplicant
// against: the picker's radio switch owns that decision, and the supplicant
// would only fail against a blocked interface.
func TestEnsureWifiBackendLeavesABlockedRadioAlone(t *testing.T) {
	withSupplicantImage(t)
	wifiRadioState = func() (bool, bool) { return false, true }

	prevRunner := wifiRunner
	wifiRunner = func(_ time.Duration, _, name string, _ ...string) (string, error) {
		t.Fatalf("%s must not run while the radio is off", name)
		return "", nil
	}
	t.Cleanup(func() { wifiRunner = prevRunner })

	if ensureWifiBackend() {
		t.Fatal("ensureWifiBackend started a supplicant against a blocked radio")
	}
}

// rfkill's verbs are block/unblock, and an image with no nmcli has only that.
// They were swapped, so "turn Wi-Fi on" blocked the radio and "turn it off"
// unblocked it: the switch did the opposite of what it said on every TrimUI
// handheld.
func TestWiFiRadioSwitchUsesTheRightRfkillVerb(t *testing.T) {
	prevBackend, prevSystem := wifiRadioBackend, systemCommandRunner
	wifiRadioBackend = func() string { return "rfkill" }
	var calls []string
	systemCommandRunner = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { wifiRadioBackend, systemCommandRunner = prevBackend, prevSystem })

	if err := setWifiEnabled(true); err != nil {
		t.Fatalf("enabling Wi-Fi: %v", err)
	}
	if err := setWifiEnabled(false); err != nil {
		t.Fatalf("disabling Wi-Fi: %v", err)
	}
	want := []string{"rfkill unblock wifi", "rfkill block wifi"}
	if len(calls) != len(want) {
		t.Fatalf("rfkill calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("rfkill calls = %v, want %v", calls, want)
		}
	}
}

// The picker must open on a device whose supplicant is not running: that is the
// state it exists to fix, and hiding the entry left the radio switch unreachable.
func TestWiFiPanelOpensOnADeviceWithNoSupplicantYet(t *testing.T) {
	_ = newProbeFrame(t)
	withScreen(t, probeW, probeH)
	withSupplicantImage(t)
	wifiRadioState = func() (bool, bool) { return false, true }

	if wifiControlAvailable() {
		t.Fatal("the fixture already had a live backend")
	}
	if !wifiEntryReachable() {
		t.Fatal("a device with a radio and a switch must keep the picker reachable")
	}

	prevPanel := wifiPanel
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			wifiDrainUpdates()
			if !wifiWorkPending || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		wifiGeneration++
		wifiDrainDiscarded()
		wifiPanel = prevPanel
	})

	wifiGeneration++
	wifiDrainDiscarded()
	openWiFiPanel()

	if !wifiPanel.open || !wifiPanel.startable || wifiPanel.available {
		t.Fatalf("panel state = open:%v startable:%v available:%v, want it open to turn Wi-Fi on",
			wifiPanel.open, wifiPanel.startable, wifiPanel.available)
	}
	if line, _ := wifiStatusLine(); !strings.Contains(line, "press A") {
		t.Fatalf("status line = %q, want the turn-it-on instruction", line)
	}

	// A on the empty list turns the radio on rather than doing nothing.
	var commands []string
	prevSystem := systemCommandRunner
	systemCommandRunner = func(name string, args ...string) (string, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { systemCommandRunner = prevSystem })

	wifiActivate()
	if !wifiPanel.busy {
		t.Fatal("pressing A on the empty list started no work")
	}
	waitForWiFiIdle(t)
	joined := strings.Join(commands, "; ")
	if !strings.Contains(joined, "rfkill unblock wifi") {
		t.Fatalf("commands = %q, want the radio switched on", joined)
	}
}
