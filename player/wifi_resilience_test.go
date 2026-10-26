package main

// wifi_resilience_test.go — the ways a "wireless device not working" report
// turned out to be code, not hardware:
//
//   - firmware tools in sbin directories the launcher's PATH misses,
//   - a soft-blocked radio surfacing as wpa_cli's bare FAIL,
//   - WPA3-personal routers the WPA-PSK-only default cannot join,
//   - a stale saved password the panel could toast but never retype,
//   - an image whose only DHCP client was never attempted.
//
// Every test runs against the stubbed runner; no radio is touched.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWifiToolFallsBackToSystemDirs: a tool PATH misses (a sbin-only install)
// must resolve to its absolute path, and one that exists nowhere must stay
// absent instead of being probed from nowhere.
func TestWifiToolFallsBackToSystemDirs(t *testing.T) {
	const tool = "jukahub_fake_wpa_cli"
	prevDirs := wifiToolDirs
	t.Cleanup(func() { wifiToolDirs = prevDirs })

	if got := wifiToolCommand(tool); got != tool {
		t.Fatalf("missing tool resolved to %q, want the bare name", got)
	}
	if wifiToolAvailable(tool) {
		t.Fatal("a tool that exists nowhere reported present")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, tool)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("plant the fake tool: %v", err)
	}
	wifiToolDirs = []string{dir}
	if got := wifiToolCommand(tool); got != path {
		t.Fatalf("PATH-missed tool resolved to %q, want %q", got, path)
	}
	if !wifiToolAvailable(tool) {
		t.Fatal("a tool only present in the system dirs reported absent")
	}

	// When PATH has the tool, the bare allowlisted name is kept — that is
	// the normal case and what the runner has always seen.
	if _, err := exec.LookPath("sh"); err == nil {
		if got := wifiToolCommand("sh"); got != "sh" {
			t.Fatalf("PATH-hit tool resolved to %q, want the bare name", got)
		}
	}
}

// TestWifiScanNamesTheRadioOffState: with the radio soft-blocked, wpa_cli can
// only answer FAIL; the picker must say the radio is off instead.
func TestWifiScanNamesTheRadioOffState(t *testing.T) {
	_ = stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		switch wifiStubCommand(args) {
		case "scan":
			return "FAIL\n", nil
		case "scan_results":
			return "bssid\tfrequency\tsignal\tflags\tssid\n", nil
		}
		return "OK\n", nil
	})

	prevRadio := wifiRadioState
	t.Cleanup(func() { wifiRadioState = prevRadio })

	wifiRadioState = func() (bool, bool) { return false, true }
	_, err := wifiScanNetworks()
	if err == nil || !strings.Contains(err.Error(), "Wi-Fi is turned off") {
		t.Fatalf("scan with the radio off = %v, want the radio message", err)
	}

	// Radio on: the supplicant's own failure stays the answer.
	wifiRadioState = func() (bool, bool) { return true, true }
	_, err = wifiScanNetworks()
	if err == nil || strings.Contains(err.Error(), "turned off") {
		t.Fatalf("scan with the radio on = %v, want the raw failure", err)
	}
}

// TestWifiJoinOffersSAEAndSurvivesOldBuilds: secured joins offer WPA3 (SAE)
// and optional management-frame protection, and a supplicant that rejects the
// values still joins on its defaults — the fallback is today's behaviour.
func TestWifiJoinOffersSAEAndSurvivesOldBuilds(t *testing.T) {
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if stdin != "" {
			return "OK\n", nil
		}
		switch wifiStubCommand(args) {
		case "add_network":
			return "0\n", nil
		case "list_networks":
			return "network id / ssid / bssid / flags\n", nil
		case "status":
			return "wpa_state=COMPLETED\nssid=NewNet\nip_address=10.0.0.4\n", nil
		case "set_network":
			// Reject the modern values the way a build without them does.
			if len(args) > 4 && (args[4] == "key_mgmt" || args[4] == "ieee80211w") {
				return "FAIL\n", nil
			}
		}
		return "OK\n", nil
	})

	res, err := wifiJoin("NewNet", "password123", true)
	if err != nil {
		t.Fatalf("a supplicant without SAE must still join: %v", err)
	}
	if res.IP != "10.0.0.4" {
		t.Fatalf("join result = %+v, want the lease reported", res)
	}

	sawSAE, sawPMF := false, false
	for _, c := range *calls {
		joined := strings.Join(append([]string{c.name}, c.args...), " ")
		if strings.Contains(joined, "key_mgmt WPA-PSK SAE") {
			sawSAE = true
		}
		if strings.Contains(joined, "ieee80211w 1") {
			sawPMF = true
		}
	}
	if !sawSAE {
		t.Error("the join never offered WPA3 (key_mgmt WPA-PSK SAE)")
	}
	if !sawPMF {
		t.Error("the join never offered protected management frames")
	}
}

// TestWifiJoinReplacesTheEntryItSupersedes: rejoining an SSID the supplicant
// already holds (with a new password) must end with one entry — the stale one
// would win every later lookup — and only after the new join works.
func TestWifiJoinReplacesTheEntryItSupersedes(t *testing.T) {
	added := false
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if stdin != "" {
			return "OK\n", nil
		}
		switch wifiStubCommand(args) {
		case "add_network":
			added = true
			return "5\n", nil
		case "list_networks":
			if !added {
				return "network id / ssid / bssid / flags\n2\tNewNet\tany\t[DISABLED]\n", nil
			}
			return "network id / ssid / bssid / flags\n2\tNewNet\tany\t[DISABLED]\n5\tNewNet\tany\t\n", nil
		case "status":
			return "wpa_state=COMPLETED\nssid=NewNet\nip_address=10.0.0.9\n", nil
		}
		return "OK\n", nil
	})

	if _, err := wifiJoin("NewNet", "newpassword1", true); err != nil {
		t.Fatalf("rejoin over the stale entry: %v", err)
	}

	sawRemove := false
	for _, c := range *calls {
		joined := strings.Join(append([]string{c.name}, c.args...), " ")
		if strings.Contains(joined, "remove_network 2") {
			sawRemove = true
		}
	}
	if !sawRemove {
		t.Fatalf("the superseded entry was not removed; calls: %+v", *calls)
	}
}

// TestWifiDHCPFallsBackToDHClient: an image whose only DHCP client is dhclient
// must still get a lease attempt instead of "no IP address was assigned".
func TestWifiDHCPFallsBackToDHClient(t *testing.T) {
	leased := false
	_ = stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if strings.Contains(name, "dhclient") {
			leased = true
			return "", nil
		}
		if wifiStubCommand(args) == "status" {
			if leased {
				return "wpa_state=COMPLETED\nssid=Net\nip_address=10.1.2.3\n", nil
			}
			return "wpa_state=COMPLETED\nssid=Net\n", nil
		}
		return "OK\n", nil
	})
	prevExists := wifiToolExists
	wifiToolExists = func(name string) bool { return name == "wpa_cli" || name == "dhclient" }
	t.Cleanup(func() { wifiToolExists = prevExists })

	ip, err := wifiObtainAddress("wlan0")
	if err != nil {
		t.Fatalf("dhclient lease path: %v", err)
	}
	if ip != "10.1.2.3" {
		t.Fatalf("IP = %q, want the dhclient lease", ip)
	}
	if !leased {
		t.Fatal("dhclient was never attempted")
	}
}

// TestWifiSelectSavedSurfacesRejectedPassword: the failure a stale stored key
// produces is the one the panel turns into a passphrase prompt — without an
// entry point to retype, a changed router password was unfixable from the UI.
func TestWifiSelectSavedSurfacesRejectedPassword(t *testing.T) {
	_ = stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		switch wifiStubCommand(args) {
		case "list_networks":
			return "network id / ssid / bssid / flags\n2\tHomeNet\tany\t\n", nil
		case "status":
			return "wpa_state=4WAY_HANDSHAKE\nssid=HomeNet\n", nil
		}
		return "OK\n", nil
	})
	prevTimeout := wifiAssociateTimeout
	wifiAssociateTimeout = 60 * time.Millisecond
	t.Cleanup(func() { wifiAssociateTimeout = prevTimeout })

	res, err := wifiSelectSaved(2)
	if err == nil {
		t.Fatal("selecting with a stale key reported success")
	}
	if !wifiPasswordRejected(err) {
		t.Fatalf("error %q not recognised as a rejected password", err)
	}
	if res.SSID != "HomeNet" {
		t.Fatalf("result SSID = %q, want HomeNet to prompt for", res.SSID)
	}

	// An unrelated failure must not open the keyboard.
	if wifiPasswordRejected(os.ErrNotExist) {
		t.Error("a non-password failure was treated as a rejected password")
	}
}
