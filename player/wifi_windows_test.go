package main

// wifi_windows_test.go — the netsh backend behind the picker.
//
// The suite pins the supplicant backend in TestMain, so these tests run the
// Windows functions directly and stub every netsh call: no radio, no netsh and
// no Windows locale is required to run them.

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// winWifiTestOutput is a Windows 11 `netsh wlan show interfaces` report, in the
// English layout, the way the panel's source actually prints it.
const winWifiTestOutput = `There is 1 interface on the system:

    Name                   : Wi-Fi
    Description            : Intel(R) Wi-Fi 6 AX201 160MHz
    GUID                   : 8f1c2f5e-1111-2222-3333-444455556666
    Physical address       : 3c:58:c2:11:22:33
    Interface type         : Primary
    State                  : connected
    SSID                   : HomeNet
    BSSID                  : a4:2b:8c:00:11:22
    Network type           : Infrastructure
    Radio type             : 802.11ac
    Authentication         : WPA2-Personal
    Cipher                 : CCMP
    Connection mode        : Profile
    Channel                : 36
    Receive rate (Mbps)    : 866.7
    Transmit rate (Mbps)   : 866.7
    Signal                 : 78%

    Hosted network status  : Not available

`

// stubWinWifi points the Wi-Fi layer at netsh and answers it through respond.
// The adapter cache and the association timeout are reset around the test so one
// case cannot leak into the next.
func stubWinWifi(t *testing.T, respond func(args []string) (string, error)) *[]string {
	t.Helper()
	calls := &[]string{}
	prevRunner, prevExists := wifiRunner, wifiToolExists
	prevForced := wifiPOSIXForced
	prevTimeout := wifiAssociateTimeout
	wifiPOSIXForced = false
	wifiAssociateTimeout = 2 * time.Second
	wifiToolExists = func(name string) bool { return name == "netsh" }
	wifiRunner = func(_ time.Duration, _ string, name string, args ...string) (string, error) {
		if name != "netsh" {
			t.Errorf("ran %q, want netsh", name)
		}
		*calls = append(*calls, strings.Join(args, " "))
		return respond(args)
	}
	resetWinWifiIfaceCache()
	t.Cleanup(func() {
		wifiRunner, wifiToolExists = prevRunner, prevExists
		wifiPOSIXForced, wifiAssociateTimeout = prevForced, prevTimeout
		resetWinWifiIfaceCache()
	})
	return calls
}

// netshVerbs is the command of one stubbed call, without the flags.
func netshVerbs(args []string) string {
	if len(args) >= 2 && args[0] == "wlan" {
		return args[1]
	}
	return strings.Join(args, " ")
}

// The interface block is split structurally, so the header, the fields and the
// trailing hosted-network section do not become three fake adapters.
func TestWinWifiParsesTheInterfaceReport(t *testing.T) {
	ifaces := parseNetshInterfaces(winWifiTestOutput)
	if len(ifaces) != 1 {
		t.Fatalf("parsed %d interfaces from one report: %+v", len(ifaces), ifaces)
	}
	got := ifaces[0]
	if got.Name != "Wi-Fi" || got.SSID != "HomeNet" || !got.Connected {
		t.Errorf("interface came out as %+v", got)
	}
	if got.BSSID != "a4:2b:8c:00:11:22" {
		t.Errorf("BSSID = %q", got.BSSID)
	}
	if got.Signal != 78 {
		t.Errorf("signal = %d%%, want 78", got.Signal)
	}
	if got.RxMbps != 866.7 {
		t.Errorf("receive rate = %v Mbit/s, want 866.7", got.RxMbps)
	}
}

// A localized Windows translates every label. The association still reads from
// the BSSID line and the strength from the percentage, so a German machine
// reports the same link as an English one.
func TestWinWifiReadsALocalizedReport(t *testing.T) {
	const german = `Es ist 1 Schnittstelle auf dem System vorhanden:

    Name                   : WLAN
    Beschreibung           : Intel(R) Wi-Fi 6 AX201 160MHz
    GUID                   : 8f1c2f5e-1111-2222-3333-444455556666
    Status                 : Verbunden
    SSID                   : Heimnetz
    BSSID                  : a4:2b:8c:00:11:22
    Signal                 : 87%

`
	ifaces := parseNetshInterfaces(german)
	if len(ifaces) != 1 {
		t.Fatalf("parsed %d interfaces: %+v", len(ifaces), ifaces)
	}
	got := ifaces[0]
	if got.SSID != "Heimnetz" || !got.Connected || got.Signal != 87 {
		t.Errorf("localized report read as %+v", got)
	}
}

// A disconnected adapter has no BSSID line, and must not be reported as linked.
func TestWinWifiDisconnectedHasNoLink(t *testing.T) {
	const out = `There is 1 interface on the system:

    Name                   : Wi-Fi
    GUID                   : 8f1c2f5e-1111-2222-3333-444455556666
    State                  : disconnected
    Signal                 : 0%

`
	ifaces := parseNetshInterfaces(out)
	if len(ifaces) != 1 {
		t.Fatalf("parsed %d interfaces", len(ifaces))
	}
	if ifaces[0].Connected {
		t.Fatalf("a disconnected adapter read as connected: %+v", ifaces[0])
	}
}

// The profile list has an empty group-policy section and repeats a profile, and
// only the real names may reach the picker.
func TestWinWifiParsesProfiles(t *testing.T) {
	const out = `Profiles on interface Wi-Fi:

Group policy profiles (read only)
---------------------------------
    <None>

User profiles
-------------
    All User Profile     : HomeNet
    All User Profile     : Cafe
    All User Profile     : HomeNet
`
	names := parseNetshProfiles(out)
	if strings.Join(names, ",") != "HomeNet,Cafe" {
		t.Fatalf("profiles = %v, want one row per profile", names)
	}
	if got := winWifiSavedNetworksFromTest(t, out); got != 2 {
		t.Fatalf("saved networks = %d, want 2", got)
	}
}

// winWifiSavedNetworksFromTest runs the real saved-networks read against a
// captured profile report.
func winWifiSavedNetworksFromTest(t *testing.T, out string) int {
	t.Helper()
	stubWinWifi(t, func(args []string) (string, error) {
		if netshVerbs(args) == "show" && strings.Contains(strings.Join(args, " "), "profiles") {
			return out, nil
		}
		return "", nil
	})
	nets, err := winWifiSavedNetworks()
	if err != nil {
		t.Fatalf("winWifiSavedNetworks: %v", err)
	}
	for i, n := range nets {
		if n.ID != i {
			t.Errorf("profile %d has id %d, want its position", i, n.ID)
		}
	}
	return len(nets)
}

// The scan is one row per network, pointing at the strongest access point of
// that network, with open and secured networks classified from the report.
func TestWinWifiParsesScanRows(t *testing.T) {
	const out = `Interface name : Wi-Fi
There are 2 networks currently visible.

SSID 1 : HomeNet
    Network type            : Infrastructure
    Authentication          : WPA2-Personal
    Encryption              : CCMP
    BSSID 1                 : a4:2b:8c:00:11:22
         Signal             : 78%
         Radio type         : 802.11ac
         Channel            : 36
    BSSID 2                 : a4:2b:8c:00:11:23
         Signal             : 90%
         Channel            : 6
SSID 2 : Cafe
    Network type            : Infrastructure
    Authentication          : Open
    Encryption              : None
    BSSID 1                 : 00:11:22:33:44:55
         Signal             : 40%
`
	nets := parseNetshNetworks(out)
	if len(nets) != 2 {
		t.Fatalf("parsed %d networks: %+v", len(nets), nets)
	}
	home := nets[0]
	if home.SSID != "HomeNet" {
		t.Fatalf("first row is %q", home.SSID)
	}
	// 90% maps to -55 dBm, and that address is the one the strength belongs to.
	if home.Signal != -55 || home.BSSID != "a4:2b:8c:00:11:23" {
		t.Errorf("HomeNet row = %+v, want the strongest access point", home)
	}
	if !home.Secured {
		t.Error("WPA2-Personal was not treated as secured")
	}
	cafe := nets[1]
	if cafe.Secured {
		t.Errorf("an open network was treated as secured: %+v", cafe)
	}
	if cafe.Signal != -80 {
		t.Errorf("Cafe signal = %d dBm, want -80", cafe.Signal)
	}
}

// A report with no networks is an answer, not a crash.
func TestWinWifiScanWithNothingInRange(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "networks") {
			return "Interface name : Wi-Fi\nThere are 0 networks currently visible.\n", nil
		}
		return "", nil
	})
	if nets, err := winWifiScanNetworks(); err == nil {
		t.Fatalf("empty scan returned %v and no error", nets)
	}
}

// Windows reports a percentage, and the picker speaks dBm.
func TestWinWifiSignalPercentToDBM(t *testing.T) {
	cases := []struct{ pct, dbm int }{
		{100, -50},
		{78, -61},
		{50, -75},
		{0, 0},
		{140, -50},
	}
	for _, c := range cases {
		if got := winSignalDBM(c.pct); got != c.dbm {
			t.Errorf("winSignalDBM(%d) = %d, want %d", c.pct, got, c.dbm)
		}
	}
}

// The profile XML is the only place the key is written, so it has to escape a
// name and a key that contain XML metacharacters, and it must never describe an
// open network as WPA2.
func TestWinWifiProfileXML(t *testing.T) {
	data, err := winWifiProfileXML(`Ben & Jerry's`, `p<a>ss&word`, true)
	if err != nil {
		t.Fatalf("winWifiProfileXML: %v", err)
	}
	xml := string(data)
	if !strings.Contains(xml, "<name>Ben &amp; Jerry&#39;s</name>") {
		t.Errorf("SSID was not escaped:\n%s", xml)
	}
	if !strings.Contains(xml, "<keyMaterial>p&lt;a&gt;ss&amp;word</keyMaterial>") {
		t.Errorf("key was not escaped:\n%s", xml)
	}
	if !strings.Contains(xml, "<authentication>WPA2PSK</authentication>") {
		t.Errorf("secured profile is not WPA2:\n%s", xml)
	}

	open, err := winWifiProfileXML("Cafe", "", false)
	if err != nil {
		t.Fatalf("open profile: %v", err)
	}
	if !strings.Contains(string(open), "<authentication>open</authentication>") {
		t.Errorf("open profile asked for a key:\n%s", open)
	}
	if strings.Contains(string(open), "sharedKey") {
		t.Errorf("open profile carries a shared key:\n%s", open)
	}
}

// Without a netsh the picker has no backend, and it says so in one line.
func TestWinWifiUnavailableWithoutNetsh(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) { return winWifiTestOutput, nil })
	wifiToolExists = func(string) bool { return false }
	if wifiControlAvailable() {
		t.Fatal("the backend reported itself usable without netsh")
	}
	if got := wifiUnavailableReason(); !strings.Contains(got, "netsh") {
		t.Errorf("reason = %q, want it to name netsh", got)
	}
}

// A Windows machine with no wireless adapter is a normal state, not an error to
// retry: netsh prints a sentence instead of an interface block.
func TestWinWifiNoAdapterIsReported(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) {
		return "There is no wireless interface on the system.\r\n", nil
	})
	if wifiControlAvailable() {
		t.Fatal("the backend reported itself usable with no adapter")
	}
	if got := wifiUnavailableReason(); !strings.Contains(got, "no wireless network interface") {
		t.Errorf("reason = %q", got)
	}
}

// The picker's status read goes through netsh on Windows: this is the dispatch,
// not just the parser.
func TestWinWifiStatusUsesNetsh(t *testing.T) {
	calls := stubWinWifi(t, func(args []string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "show interfaces") {
			return winWifiTestOutput, nil
		}
		return "", nil
	})
	st, err := wifiReadStatus()
	if err != nil {
		t.Fatalf("wifiReadStatus: %v", err)
	}
	if !st.Connected || st.SSID != "HomeNet" || st.State != wifiCompleteState {
		t.Errorf("status = %+v", st)
	}
	if len(*calls) == 0 {
		t.Fatal("no netsh call was made")
	}
}

// Joining writes a profile, points netsh at it, connects, and removes the file
// that held the key. The key itself is never an argument.
func TestWinWifiJoinWritesAProfile(t *testing.T) {
	const pass = "correct-horse-battery"
	var profilePath, profileBody string
	calls := stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "add profile"):
			for _, arg := range args {
				if name, ok := strings.CutPrefix(arg, "filename="); ok {
					profilePath = name
					data, err := os.ReadFile(name)
					if err != nil {
						t.Errorf("the profile file was not readable during the call: %v", err)
					}
					profileBody = string(data)
				}
			}
			return "Profile HomeNet is added on interface Wi-Fi.\r\n", nil
		case strings.Contains(joined, "connect"):
			return "Connection request was completed successfully.\r\n", nil
		case strings.Contains(joined, "show interfaces"):
			return winWifiTestOutput, nil
		}
		return "", nil
	})

	res, err := wifiJoin("HomeNet", pass, true)
	if err != nil {
		t.Fatalf("wifiJoin: %v", err)
	}
	if !res.Saved || res.SSID != "HomeNet" {
		t.Errorf("join result = %+v", res)
	}
	if !strings.Contains(profileBody, "<name>HomeNet</name>") || !strings.Contains(profileBody, pass) {
		t.Errorf("profile did not carry the network and its key:\n%s", profileBody)
	}
	if profilePath == "" {
		t.Fatal("netsh was never pointed at a profile file")
	}
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Errorf("the profile file holding the key was left behind at %s", profilePath)
	}
	for _, call := range *calls {
		if strings.Contains(call, pass) {
			t.Fatalf("the passphrase reached a command line: %q", call)
		}
	}
}

// A join Windows refuses to complete says what to check, rather than repeating
// netsh's own wording.
func TestWinWifiJoinFailureExplainsItself(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show interfaces") {
			return `There is 1 interface on the system:

    Name                   : Wi-Fi
    State                  : disconnected

`, nil
		}
		return "OK\r\n", nil
	})
	prev := wifiAssociateTimeout
	wifiAssociateTimeout = 500 * time.Millisecond
	t.Cleanup(func() { wifiAssociateTimeout = prev })

	_, err := wifiJoin("HomeNet", "wrong-password", true)
	if err == nil {
		t.Fatal("a join that never associates reported success")
	}
	msg := err.Error()
	if !strings.Contains(msg, "could not join HomeNet") {
		t.Fatalf("error = %q, want it to name the network", msg)
	}
	if !strings.Contains(msg, "password") || !strings.Contains(msg, "range") {
		t.Errorf("error = %q, want both possibilities named", msg)
	}
}

// Reconnecting a saved network connects the profile by name, with no key.
func TestWinWifiSelectSavedConnectsTheProfile(t *testing.T) {
	calls := stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "profiles"):
			return "Profiles on interface Wi-Fi:\n\nUser profiles\n-------------\n    All User Profile     : HomeNet\n", nil
		case strings.Contains(joined, "connect"):
			return "Connection request was completed successfully.\r\n", nil
		case strings.Contains(joined, "show interfaces"):
			return winWifiTestOutput, nil
		}
		return "", nil
	})
	res, err := wifiSelectSaved(0)
	if err != nil {
		t.Fatalf("wifiSelectSaved: %v", err)
	}
	if res.SSID != "HomeNet" || !res.Saved {
		t.Errorf("result = %+v", res)
	}
	for _, call := range *calls {
		if strings.Contains(call, "add profile") {
			t.Fatalf("a saved network was re-added instead of connected: %q", call)
		}
	}
}

// Forgetting deletes the profile and drops a link that was using it.
func TestWinWifiForgetDeletesTheProfile(t *testing.T) {
	calls := stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "profiles"):
			return "Profiles on interface Wi-Fi:\n\nUser profiles\n-------------\n    All User Profile     : HomeNet\n", nil
		case strings.Contains(joined, "show interfaces"):
			return winWifiTestOutput, nil
		}
		return "", nil
	})
	if err := wifiForget("HomeNet"); err != nil {
		t.Fatalf("wifiForget: %v", err)
	}
	sawDelete, sawDisconnect := false, false
	for _, call := range *calls {
		if strings.Contains(call, "delete profile name=HomeNet") {
			sawDelete = true
		}
		if strings.Contains(call, "disconnect") {
			sawDisconnect = true
		}
	}
	if !sawDelete {
		t.Errorf("the profile was not deleted: %v", *calls)
	}
	if !sawDisconnect {
		t.Errorf("the link was left up after its key was deleted: %v", *calls)
	}
	if err := wifiForget("NotSaved"); err == nil {
		t.Error("forgetting a network that was never saved reported success")
	}
}

// The status meter needs a strength even when the kernel has no /proc: the
// Windows poll answers from the interface report.
func TestWinWifiSignalPollReadsTheReport(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) {
		return winWifiTestOutput, nil
	})
	sample, err := wifiSignalPoll()
	if err != nil {
		t.Fatalf("wifiSignalPoll: %v", err)
	}
	// 78% maps to -61 dBm, which is three bars.
	if sample.RSSI != -61 {
		t.Errorf("RSSI = %d, want -61", sample.RSSI)
	}
	if got := wifiSignalBars(sample.RSSI); got != 3 {
		t.Errorf("bars = %d, want 3", got)
	}
	if sample.LinkKbps != 866700 {
		t.Errorf("link = %d kbit/s", sample.LinkKbps)
	}

	stubWinWifi(t, func(args []string) (string, error) {
		return "There is 1 interface on the system:\n\n    Name                   : Wi-Fi\n    State                  : disconnected\n\n", nil
	})
	if _, err := wifiSignalPoll(); err == nil {
		t.Error("a disconnected adapter reported a signal")
	}
}

// A netsh failure keeps netsh's own words instead of becoming "exit status 1".
func TestWinWifiCommandErrorKeepsTheReason(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) {
		return "The network specified by profile \"HomeNet\" is not available to connect.\r\n", errors.New("exit status 1")
	})
	_, err := winWifiSelectSaved(0)
	if err == nil {
		t.Fatal("no error from a refused connect")
	}
	if !strings.Contains(err.Error(), "not available to connect") {
		t.Errorf("error = %q, want netsh's reason", err)
	}
}
