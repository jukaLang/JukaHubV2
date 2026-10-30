package main

// wifi_windows_test.go — the netsh backend behind the picker.
//
// The suite pins the supplicant backend in TestMain, so these tests run the
// Windows functions directly and stub every netsh call: no radio, no netsh and
// no Windows locale is required to run them.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// netshAdapter writes one interface block of `netsh wlan show interfaces`.
// Empty arguments are left out, the way Windows omits a line it has nothing to
// say about (a disconnected adapter has no SSID, BSSID or receive rate).
func netshAdapter(name, description, state, ssid, rssi, signal string) string {
	block := fmt.Sprintf("    Name                   : %s\n", name)
	if description != "" {
		block += fmt.Sprintf("    Description            : %s\n", description)
	}
	if state != "" {
		block += fmt.Sprintf("    State                  : %s\n", state)
	}
	if ssid != "" {
		block += fmt.Sprintf("    SSID                   : %s\n", ssid)
		block += "    BSSID                  : a4:2b:8c:00:11:22\n"
	}
	if rssi != "" {
		block += fmt.Sprintf("    Rssi                   : %s\n", rssi)
	}
	if signal != "" {
		block += fmt.Sprintf("    Signal                 : %s\n", signal)
	}
	return block + "\n"
}

// netshReport wraps adapter blocks in a report header.
func netshReport(blocks ...string) string {
	header := fmt.Sprintf("There are %d interfaces on the system:\n\n", len(blocks))
	return header + strings.Join(blocks, "")
}

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

// Windows 11 renamed the BSSID line to "AP BSSID" and added an exact RSSI, so
// a report captured on a current machine has to read the same link as the
// Windows 10 layout above.
func TestWinWifiParsesAWindows11Report(t *testing.T) {
	const out = `
There is 1 interface on the system:

    Name                   : Wi-Fi
    Description            : Intel(R) Wi-Fi 6 AX201 160MHz
    GUID                   : 35e2a7e9-7689-446b-a24b-abed36ad8f49
    Physical address       : a8:64:f1:bf:58:51
    Interface type         : Primary
    State                  : connected
    SSID                   : Coffee Shop
    AP BSSID               : cc:1b:5a:b2:b6:fd
    Band                   : 5 GHz
    Channel                : 36
    Network type           : Infrastructure
    Radio type             : 802.11ax
    Authentication         : Open
    Cipher                 : None
    Connection mode        : Auto Connect
    Receive rate (Mbps)    : 413
    Transmit rate (Mbps)   : 287
    Signal                 : 83%
    Rssi                   : -54
    Profile                : Coffee Shop
    QoS MSCS Configured         : 0

`
	ifaces := parseNetshInterfaces(out)
	if len(ifaces) != 1 {
		t.Fatalf("parsed %d interfaces from a Windows 11 report: %+v", len(ifaces), ifaces)
	}
	got := ifaces[0]
	if got.Name != "Wi-Fi" || got.SSID != "Coffee Shop" || !got.Connected {
		t.Errorf("interface came out as %+v", got)
	}
	if got.BSSID != "cc:1b:5a:b2:b6:fd" {
		t.Errorf("AP BSSID = %q, want the access point's address", got.BSSID)
	}
	if got.RssiDBM != -54 {
		t.Errorf("Rssi = %d dBm, want -54", got.RssiDBM)
	}
	if got.RxMbps != 413 {
		t.Errorf("receive rate = %v Mbit/s, want 413", got.RxMbps)
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

// Scans must use the physical adapter selected by the picker, not whichever
// wireless interface netsh happens to choose by default.
func TestWinWifiRunOnInterfaceRejectsAnEmptyAdapter(t *testing.T) {
	calls := stubWinWifi(t, func(args []string) (string, error) {
		return "", nil
	})
	if _, err := winWifiRunOnInterface(" \t", "wlan", "show", "networks"); err == nil {
		t.Fatal("an empty adapter name ran an unscoped netsh command")
	}
	if len(*calls) != 0 {
		t.Fatalf("empty adapter invoked netsh: %v", *calls)
	}
}

func TestWinWifiScanUsesTheSelectedAdapter(t *testing.T) {
	report := netshReport(
		netshAdapter("Local Area Connection* 10", "Microsoft Wi-Fi Direct Virtual Adapter", "disconnected", "", "", ""),
		netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "disconnected", "", "", ""),
	)
	calls := stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show interfaces") {
			return report, nil
		}
		if strings.Contains(joined, "show networks") {
			if !strings.Contains(joined, "interface=Wi-Fi") {
				t.Errorf("scan was not pinned to the selected physical adapter: %q", joined)
			}
			return "SSID 1 : Cafe\n    Authentication : Open\n    Encryption : None\n", nil
		}
		return "", nil
	})
	nets, err := winWifiScanResults()
	if err != nil {
		t.Fatalf("winWifiScanResults: %v", err)
	}
	if len(nets) != 1 || nets[0].SSID != "Cafe" {
		t.Fatalf("scan networks = %+v", nets)
	}
	found := false
	for _, call := range *calls {
		if strings.Contains(call, "show networks") && strings.Contains(call, "interface=Wi-Fi") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no scan was issued against the selected adapter: %v", *calls)
	}
}

// A report with no networks is an answer, not a crash.
func TestWinWifiScanWithNothingInRange(t *testing.T) {
	stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show interfaces") {
			return winWifiTestOutput, nil
		}
		if strings.Contains(joined, "networks") {
			if !strings.Contains(joined, "interface=Wi-Fi") {
				t.Errorf("scan was not pinned to Wi-Fi: %q", joined)
			}
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
	sawScopedAdd, sawScopedConnect := false, false
	for _, call := range *calls {
		if strings.Contains(call, pass) {
			t.Fatalf("the passphrase reached a command line: %q", call)
		}
		if strings.Contains(call, "add profile") {
			sawScopedAdd = strings.Contains(call, "interface=Wi-Fi")
		}
		if strings.Contains(call, "connect name=HomeNet") {
			sawScopedConnect = strings.Contains(call, "interface=Wi-Fi")
		}
	}
	if !sawScopedAdd || !sawScopedConnect {
		t.Errorf("profile import/connect were not pinned to the selected adapter: %v", *calls)
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
			if !strings.Contains(joined, "interface=Wi-Fi") {
				t.Errorf("saved-profile connect was not pinned to Wi-Fi: %q", joined)
			}
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
	sawScopedConnect := false
	for _, call := range *calls {
		if strings.Contains(call, "add profile") {
			t.Fatalf("a saved network was re-added instead of connected: %q", call)
		}
		if strings.Contains(call, "connect name=HomeNet") && strings.Contains(call, "interface=Wi-Fi") {
			sawScopedConnect = true
		}
	}
	if !sawScopedConnect {
		t.Errorf("no adapter-scoped saved-profile connect was made: %v", *calls)
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

// An exact dBm from Windows 11 wins over the quality percentage: -54 is what
// the adapter measured, while 83% maps to -59.
func TestWinWifiSignalPrefersAnExactRssi(t *testing.T) {
	const out = `There is 1 interface on the system:

    Name                   : Wi-Fi
    State                  : connected
    SSID                   : Coffee Shop
    AP BSSID               : cc:1b:5a:b2:b6:fd
    Signal                 : 83%
    Rssi                   : -54

`
	stubWinWifi(t, func(args []string) (string, error) { return out, nil })
	sample, err := wifiSignalPoll()
	if err != nil {
		t.Fatalf("wifiSignalPoll: %v", err)
	}
	if sample.RSSI != -54 {
		t.Errorf("RSSI = %d dBm, want the reported -54", sample.RSSI)
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

// A missing adapter is answers, not a verdict: Windows often has not brought the
// radio up yet when the app starts, so "there is no interface" is retried after
// winWifiNegativeTTL instead of being held for the full minute a real adapter is.
func TestWinWifiNoAdapterAnswerExpiresQuickly(t *testing.T) {
	var present bool
	calls := 0
	stubWinWifi(t, func(args []string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "show interfaces") {
			calls++
		}
		if present {
			return winWifiTestOutput, nil
		}
		return "There is no wireless interface on the system.\r\n", nil
	})
	prevNow := wifiDrivenNow
	now := time.Now()
	wifiDrivenNow = func() time.Time { return now }
	t.Cleanup(func() { wifiDrivenNow = prevNow })

	if wifiControlAvailable() {
		t.Fatal("an absent adapter reported itself usable")
	}
	// Inside the negative TTL the answer is cached: the status bar asks often.
	if wifiControlAvailable() {
		t.Fatal("the absent adapter became usable")
	}
	if calls != 1 {
		t.Fatalf("probed %d times inside the TTL, want the one cached probe", calls)
	}

	present = true
	now = now.Add(winWifiNegativeTTL + time.Second)
	if !wifiControlAvailable() {
		t.Fatal("a radio that appeared was not noticed after the negative TTL")
	}
	if got := winWifiInterface(); got != "Wi-Fi" {
		t.Errorf("interface = %q, want Wi-Fi", got)
	}
}

// Opening the picker asks the platform again on Windows too. Without this a
// radio that was still loading at boot keeps answering "no interface" from the
// cache, which is what made the panel unreachable after a resume or a plug-in.
func TestResetWiFiInterfaceCacheAsksNetshAgain(t *testing.T) {
	var present bool
	stubWinWifi(t, func(args []string) (string, error) {
		if present {
			return winWifiTestOutput, nil
		}
		return "There is no wireless interface on the system.\r\n", nil
	})
	if wifiControlAvailable() {
		t.Fatal("an absent adapter reported itself usable")
	}
	present = true
	resetWiFiInterfaceCache()
	if !wifiControlAvailable() {
		t.Fatal("the picker's reset did not re-probe the adapter")
	}
}

// The CONNECTED row and the status meter must read the same strength: the scan
// reports a quality percentage, the adapter reports the dBm it measured, and the
// measured level is the one both end up showing.
func TestWinWifiSnapshotUsesTheMeasuredSignal(t *testing.T) {
	// The adapter measured -54 dBm; the scan row for the same access point
	// reports 78%, which maps to -61.
	const interfaces = `There is 1 interface on the system:

    Name                   : Wi-Fi
    State                  : connected
    SSID                   : HomeNet
    AP BSSID               : a4:2b:8c:00:11:22
    Receive rate (Mbps)    : 866.7
    Signal                 : 78%
    Rssi                   : -54

`
	const networks = `Interface name : Wi-Fi
There are 1 networks currently visible.

SSID 1 : HomeNet
    Network type            : Infrastructure
    Authentication          : WPA2-Personal
    Encryption              : CCMP
    BSSID 1                 : a4:2b:8c:00:11:22
         Signal             : 78%
`
	stubWinWifi(t, func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "show interfaces"):
			return interfaces, nil
		case strings.Contains(joined, "show networks"):
			return networks, nil
		}
		return "", nil
	})
	snap, err := winWifiReadSnapshot()
	if err != nil {
		t.Fatalf("winWifiReadSnapshot: %v", err)
	}
	if len(snap.Networks) != 1 {
		t.Fatalf("snapshot holds %d networks: %+v", len(snap.Networks), snap.Networks)
	}
	row := snap.Networks[0]
	if !row.Active {
		t.Fatalf("the connected network is not marked active: %+v", row)
	}
	if row.Signal != -54 {
		t.Errorf("row signal = %d dBm, want the measured -54", row.Signal)
	}
}

// A machine can report several wireless adapters. The linked one is the one the
// picker must show: reading block zero reported "no network" while the machine
// sat on one, because Windows puts the disconnected virtual adapter first.
func TestWinWifiPicksTheLinkedAdapter(t *testing.T) {
	report := netshReport(
		netshAdapter("Local Area Connection* 10", "Microsoft Wi-Fi Direct Virtual Adapter", "disconnected", "", "", ""),
		netshAdapter("Wi-Fi 2", "Realtek RTL8852BE WiFi 6", "disconnected", "", "", ""),
		netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "connected", "BoatNet", "-54", "83%"),
	)
	stubWinWifi(t, func(args []string) (string, error) { return report, nil })

	if got := winWifiInterface(); got != "Wi-Fi" {
		t.Fatalf("interface = %q, want the linked adapter", got)
	}
	st, err := wifiReadStatus()
	if err != nil {
		t.Fatalf("wifiReadStatus: %v", err)
	}
	if !st.Connected || st.SSID != "BoatNet" || st.State != wifiCompleteState {
		t.Errorf("status = %+v, want the linked adapter's association", st)
	}
}

// With nothing linked, a real NIC beats a software adapter over the same radio.
func TestWinWifiPrefersARealAdapterOverAVirtualOne(t *testing.T) {
	report := netshReport(
		netshAdapter("Local Area Connection* 1", "Microsoft Wi-Fi Direct Virtual Adapter", "disconnected", "", "", ""),
		netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "disconnected", "", "", ""),
	)
	stubWinWifi(t, func(args []string) (string, error) { return report, nil })
	if got := winWifiInterface(); got != "Wi-Fi" {
		t.Fatalf("interface = %q, want the physical adapter", got)
	}
}

// A report whose blocks are all unrecognisable still has to answer with an
// adapter: a localized Description line must not turn into "no interface".
func TestWinWifiPicksAnAdapterWhenNoBlockLooksVirtual(t *testing.T) {
	report := netshReport(
		netshAdapter("WLAN", "", "getrennt", "", "", ""),
		netshAdapter("WLAN 2", "", "getrennt", "", "", ""),
	)
	stubWinWifi(t, func(args []string) (string, error) { return report, nil })
	if got := winWifiInterface(); got != "WLAN" {
		t.Fatalf("interface = %q, want the first adapter as the last resort", got)
	}
}

// The status read re-reads the report for a fresh association, and with several
// adapters it must stay on the one the panel is showing — even after the
// strength ranking between them flips.
func TestWinWifiStatusDoesNotSwitchWhenTheSelectedAdapterDisappears(t *testing.T) {
	first := true
	stubWinWifi(t, func(args []string) (string, error) {
		if first {
			first = false
			return netshReport(
				netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "connected", "HomeNet", "-55", "80%"),
			), nil
		}
		return netshReport(
			netshAdapter("Wi-Fi 2", "Realtek RTL8852BE WiFi 6", "connected", "CafeNet", "-40", "99%"),
		), nil
	})
	if got := winWifiInterface(); got != "Wi-Fi" {
		t.Fatalf("selected adapter = %q, want Wi-Fi", got)
	}
	st, err := wifiReadStatus()
	if err != nil {
		t.Fatalf("wifiReadStatus: %v", err)
	}
	if st.Connected || st.SSID != "" {
		t.Errorf("status silently switched to another adapter: %+v", st)
	}
}

func TestWinWifiStatusReadsTheAdapterItNamed(t *testing.T) {
	first := true
	stubWinWifi(t, func(args []string) (string, error) {
		// The first read links Wi-Fi at 80%; the second read has the dongle
		// stronger, so a fresh pick would move to it.
		if first {
			first = false
			return netshReport(
				netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "connected", "HomeNet", "-55", "80%"),
				netshAdapter("Wi-Fi 2", "Realtek RTL8852BE WiFi 6", "connected", "CafeNet", "-80", "40%"),
			), nil
		}
		return netshReport(
			netshAdapter("Wi-Fi 2", "Realtek RTL8852BE WiFi 6", "connected", "CafeNet", "-40", "99%"),
			netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "connected", "HomeNet", "-55", "80%"),
		), nil
	})

	if got := winWifiInterface(); got != "Wi-Fi" {
		t.Fatalf("the stronger adapter was not chosen first: %q", got)
	}
	st, err := wifiReadStatus()
	if err != nil {
		t.Fatalf("wifiReadStatus: %v", err)
	}
	if st.SSID != "HomeNet" {
		t.Errorf("status read %q, want the adapter the panel is showing (HomeNet)", st.SSID)
	}
}

// The status meter reads the adapter's measured level, so with several adapters
// it must read the linked one rather than a disconnected first block.
func TestWinWifiSignalPollUsesTheLinkedAdapter(t *testing.T) {
	report := netshReport(
		netshAdapter("Local Area Connection* 10", "Microsoft Wi-Fi Direct Virtual Adapter", "disconnected", "", "", ""),
		netshAdapter("Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", "connected", "BoatNet", "-54", "83%"),
	)
	stubWinWifi(t, func(args []string) (string, error) { return report, nil })
	sample, err := wifiSignalPoll()
	if err != nil {
		t.Fatalf("wifiSignalPoll: %v", err)
	}
	if sample.RSSI != -54 {
		t.Errorf("RSSI = %d dBm, want the linked adapter's -54", sample.RSSI)
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
