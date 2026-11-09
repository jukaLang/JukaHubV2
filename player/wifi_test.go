package main

// Tests for the Wi-Fi picker: the parsers, the passphrase rule, the guarantee
// that a password never reaches argv, and forgetting a saved network. No test
// touches a radio: wifiRunner and wifiToolExists are replaced.

import (
	"strings"
	"testing"
	"time"
)

const wifiTestScan = `bssid / frequency / signal level / flags / ssid
aa:bb:cc:dd:ee:01	2412	-47	[WPA2-PSK-CCMP][ESS]	HomeNet
aa:bb:cc:dd:ee:02	2437	-71	[WPA2-PSK-CCMP][ESS]	HomeNet
aa:bb:cc:dd:ee:03	5180	-88	[ESS]	Cafe
aa:bb:cc:dd:ee:04	2462	-60	[WPA2-PSK-CCMP][ESS]	`

func TestParseWPAScanResults(t *testing.T) {
	nets := parseWPAScanResults(wifiTestScan)
	// The parser keeps every BSSID it can read; the hidden row is the only one
	// it drops. Collapsing to one row per SSID is mergeWiFiNetworks' job, so a
	// second HomeNet BSSID here is expected.
	if len(nets) != 3 {
		t.Fatalf("parsed %d networks, want 3 (hidden dropped): %+v", len(nets), nets)
	}
	home := nets[0]
	if home.SSID != "HomeNet" || home.Signal != -47 || !home.Secured {
		t.Fatalf("HomeNet parsed as %+v", home)
	}
	if home.BSSID != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("HomeNet parsed with BSSID %q", home.BSSID)
	}
	if nets[2].SSID != "Cafe" || nets[2].Secured {
		t.Fatalf("open network parsed as %+v, want Cafe unsecured", nets[2])
	}

	merged := mergeWiFiNetworks(nets, nil, wifiStatus{})
	if len(merged) != 2 {
		t.Fatalf("merge kept %d rows, want 2: %+v", len(merged), merged)
	}
	if merged[0].Signal != -47 {
		t.Fatalf("merge kept signal %d, want the stronger BSSID (-47)", merged[0].Signal)
	}
}

func TestParseWPAStatus(t *testing.T) {
	st := parseWPAStatus("bssid=aa:bb:cc:dd:ee:ff\nssid=Home=Net\nwpa_state=COMPLETED\nip_address=192.168.1.20\n")
	if !st.Connected || st.SSID != "Home=Net" || st.IP != "192.168.1.20" {
		t.Fatalf("status parsed as %+v", st)
	}
	if dis := parseWPAStatus("wpa_state=DISCONNECTED\nssid=HomeNet\n"); dis.Connected {
		t.Fatalf("DISCONNECTED parsed as connected: %+v", dis)
	}
	// Leading and trailing spaces are part of an SSID and must survive parsing.
	spaced := parseWPAStatus("ssid= My Network \nwpa_state=COMPLETED\r\n")
	if spaced.SSID != " My Network " {
		t.Fatalf("status trimmed the SSID to %q", spaced.SSID)
	}
}

func TestParseWPANetworks(t *testing.T) {
	nets := parseWPANetworks("network id / ssid / bssid / flags\n0\tHomeNet\tany\t[CURRENT]\n1\tCafe\tany\t\n")
	if len(nets) != 2 {
		t.Fatalf("parsed %d saved networks: %+v", len(nets), nets)
	}
	if nets[0].ID != 0 || nets[0].SSID != "HomeNet" || nets[0].Flags != "[CURRENT]" {
		t.Fatalf("first saved network parsed as %+v", nets[0])
	}
	if nets[1].Flags != "" {
		t.Fatalf("empty flags parsed as %q", nets[1].Flags)
	}
}

func TestValidateWiFiCredentials(t *testing.T) {
	if err := validateWiFiSSID("HomeNet"); err != nil {
		t.Errorf("valid SSID rejected: %v", err)
	}
	if err := validateWiFiSSID(strings.Repeat("x", 33)); err == nil {
		t.Error("33-byte SSID accepted")
	}
	if err := validateWiFiSSID("bad\nname"); err == nil {
		t.Error("SSID with a newline accepted")
	}
	if err := validateWiFiPassphrase("short"); err == nil {
		t.Error("7-character password accepted")
	}
	if err := validateWiFiPassphrase(strings.Repeat("a", 64)); err != nil {
		t.Errorf("64-digit hex password rejected: %v", err)
	}
	if err := validateWiFiPassphrase(strings.Repeat("z", 64)); err == nil {
		t.Error("64 non-hex characters accepted")
	}
	if err := validateWiFiPassphrase("correct horse"); err != nil {
		t.Errorf("valid passphrase rejected: %v", err)
	}
	// wpa_supplicant takes a quoted value verbatim (no escapes), so the SSID
	// travels as hex and a passphrase is quoted exactly as typed.
	if got := wpaSSIDArg(`Home"Net\`); got != "486f6d65224e65745c" {
		t.Fatalf("wpaSSIDArg = %s", got)
	}
	if got := wpaSSIDArg("Café"); got != "436166c3a9" {
		t.Fatalf("wpaSSIDArg(UTF-8) = %s", got)
	}
	if got := wpaPSKArg(`pa"ss\word`); got != `"pa"ss\word"` {
		t.Fatalf("wpaPSKArg = %s, want the passphrase quoted verbatim", got)
	}
	raw := strings.Repeat("0123456789abcdef", 4)
	if got := wpaPSKArg(raw); got != raw {
		t.Fatalf("wpaPSKArg(raw key) = %s, want it unquoted", got)
	}
}

// The supplicant prints SSIDs with printf-style escapes; the parsers must hand
// the real name to the picker (and to set_network, via wpaSSIDArg).
func TestWPAUnescapeDecodesSupplicantSSIDs(t *testing.T) {
	cases := map[string]string{
		"HomeNet":      "HomeNet",
		`Caf\xc3\xa9`:  "Café",
		`Home\"Net\\`:  `Home"Net\`,
		`tab\there`:    "tab\there",
		`odd\q`:        `odd\q`,
		`short\x4`:     `short\x4`,
		`trailing\`:    `trailing\`,
		`\x00\x00\x00`: "\x00\x00\x00",
	}
	for in, want := range cases {
		if got := wpaUnescape(in); got != want {
			t.Errorf("wpaUnescape(%q) = %q, want %q", in, got, want)
		}
	}
	scan := "bssid / frequency / signal level / flags / ssid\n" +
		"aa:bb:cc:dd:ee:01\t2412\t-47\t[WPA2-PSK-CCMP][ESS]\tCaf\\xc3\\xa9\n" +
		"aa:bb:cc:dd:ee:02\t2412\t-60\t[ESS]\t\\x00\\x00\\x00\\x00\n"
	nets := parseWPAScanResults(scan)
	if len(nets) != 1 || nets[0].SSID != "Café" {
		t.Fatalf("scan = %+v, want the decoded name and the hidden row dropped", nets)
	}
	if st := parseWPAStatus("wpa_state=COMPLETED\nssid=Caf\\xc3\\xa9\n"); st.SSID != "Café" || !st.Connected {
		t.Fatalf("status = %+v, want the decoded name", st)
	}
	if saved := parseWPANetworks("network id / ssid / bssid / flags\n0\tCaf\\xc3\\xa9\tany\t[CURRENT]\n"); len(saved) != 1 || saved[0].SSID != "Café" {
		t.Fatalf("saved = %+v, want the decoded name", saved)
	}
}

// wifiCall records one stubbed command.
type wifiCall struct {
	stdin string
	name  string
	args  []string
}

// stubWiFi replaces the command runner and tool probe for one test.
func stubWiFi(t *testing.T, respond func(stdin, name string, args []string) (string, error)) *[]wifiCall {
	t.Helper()
	calls := &[]wifiCall{}
	prevRunner := wifiRunner
	prevExists := wifiToolExists
	wifiRunner = func(timeout time.Duration, stdin, name string, args ...string) (string, error) {
		*calls = append(*calls, wifiCall{stdin: stdin, name: name, args: append([]string(nil), args...)})
		return respond(stdin, name, args)
	}
	wifiToolExists = func(name string) bool { return name == "wpa_cli" }
	t.Cleanup(func() {
		wifiRunner = prevRunner
		wifiToolExists = prevExists
	})
	t.Setenv("JUKAHUB_WIFI_IFACE", "wlan0")
	return calls
}

// wifiStubCommand is the verb of a stubbed call. Flags are skipped with their
// value (-i <iface>, and -p <dir> once the control directory is resolved), so a
// test keeps naming the command rather than an argument's position.
func wifiStubCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-i", "-p":
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		return args[i]
	}
	return ""
}

const wifiTestJoinScript = "HUNTER2SECRET"

// TestWiFiJoinKeepsPassphraseOutOfArgv is the security contract: wpa_cli
// re-parses its command line, so the PSK goes on stdin and must never appear in
// any argument (where `ps` would show it) or in an error message.
func TestWiFiJoinKeepsPassphraseOutOfArgv(t *testing.T) {
	const pass = wifiTestJoinScript
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if stdin != "" {
			return "OK\n", nil
		}
		switch wifiStubCommand(args) {
		case "add_network":
			return "7\n", nil
		case "list_networks":
			return "network id / ssid / bssid / flags\n7\tHomeNet\tany\t\n", nil
		case "status":
			return "wpa_state=COMPLETED\nssid=HomeNet\nip_address=192.168.1.20\n", nil
		}
		return "OK\n", nil
	})

	res, err := wifiJoin("HomeNet", pass, true)
	if err != nil {
		t.Fatalf("wifiJoin: %v", err)
	}
	if res.IP != "192.168.1.20" || !res.Saved || res.Warn != "" {
		t.Fatalf("result = %+v, want saved with an IP and no warning", res)
	}

	stdinSeen := false
	for _, c := range *calls {
		for _, a := range append([]string{c.name}, c.args...) {
			if strings.Contains(a, pass) {
				t.Fatalf("passphrase appeared in argv of %s: %q", c.name, c.args)
			}
		}
		if c.stdin != "" {
			stdinSeen = true
			if !strings.Contains(c.stdin, `psk "`+pass+`"`) {
				t.Fatalf("stdin session did not carry the PSK: %q", c.stdin)
			}
			// The interactive session is `wpa_cli -i <iface>` with the commands
			// on stdin; a command in argv as well would mean the secret could
			// have been passed as an argument.
			if len(c.args) != 2 || c.args[0] != "-i" {
				t.Fatalf("unexpected stdin invocation: %v", c.args)
			}
			if strings.Count(c.stdin, "\n") != 1 {
				t.Fatalf("stdin session carried more than the PSK command: %q", c.stdin)
			}
		}
	}
	if !stdinSeen {
		t.Fatal("no stdin session was used for the PSK")
	}

	usedID := false
	for _, c := range *calls {
		if wifiStubCommand(c.args) == "set_network" && len(c.args) > 3 && c.args[3] == "7" {
			usedID = true
		}
	}
	if !usedID {
		t.Fatalf("join did not configure the id list_networks reported: %v", *calls)
	}
}

func TestWiFiJoinValidatesBeforeRunningCommands(t *testing.T) {
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		return "OK\n", nil
	})
	if _, err := wifiJoin("HomeNet", "short", true); err == nil {
		t.Fatal("wifiJoin accepted a 5-character WPA password")
	}
	if len(*calls) != 0 {
		t.Fatalf("invalid credentials reached the supplicant: %v", *calls)
	}
}

func TestWiFiJoinOpenNetworkSendsNoSecret(t *testing.T) {
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if stdin != "" {
			return "OK\n", nil
		}
		switch wifiStubCommand(args) {
		case "list_networks":
			return "network id / ssid / bssid / flags\n0\tCafe\tany\t\n", nil
		case "status":
			return "wpa_state=COMPLETED\nssid=Cafe\nip_address=10.0.0.5\n", nil
		}
		return "OK\n", nil
	})
	if _, err := wifiJoin("Cafe", "", false); err != nil {
		t.Fatalf("wifiJoin(open): %v", err)
	}
	keyMgmt := false
	for _, c := range *calls {
		if c.stdin != "" {
			t.Fatalf("open network used a stdin session: %q", c.stdin)
		}
		for i, a := range c.args {
			if a == "key_mgmt" && i+1 < len(c.args) && c.args[i+1] == "NONE" {
				keyMgmt = true
			}
		}
	}
	if !keyMgmt {
		t.Fatalf("open network did not set key_mgmt NONE: %v", *calls)
	}
}

func TestWiFiForgetRemovesSavedNetwork(t *testing.T) {
	calls := stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if wifiStubCommand(args) == "list_networks" {
			return "network id / ssid / bssid / flags\n2\tHomeNet\tany\t[CURRENT]\n", nil
		}
		return "OK\n", nil
	})
	if err := wifiForget("HomeNet"); err != nil {
		t.Fatalf("wifiForget: %v", err)
	}
	removed := false
	for _, c := range *calls {
		if wifiStubCommand(c.args) == "remove_network" && len(c.args) > 3 && c.args[3] == "2" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("forget did not remove id 2: %v", *calls)
	}
	if err := wifiForget("Missing"); err == nil {
		t.Fatal("forgetting an unsaved network succeeded")
	}
}
