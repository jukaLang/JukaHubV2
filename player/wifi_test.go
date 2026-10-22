package main

// Tests for the Wi-Fi picker: the parsers, the passphrase rule, the guarantee
// that a password never reaches argv, the Hardware entry button, and the panel
// layout. No test touches a radio: wifiRunner and wifiToolExists are replaced.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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

func TestFindWirelessInterface(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"eth0", "wlan1", "wlan0"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// wlan1 uses the newer marker; wlan0 the old one. Sorted order picks
	// wlan0, so the result is stable on a device that exposes both.
	if err := os.MkdirAll(filepath.Join(root, "wlan1", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "wlan0", "wireless"), 0o755); err != nil {
		t.Fatal(err)
	}
	iface, err := findWirelessInterface(root)
	if err != nil || iface != "wlan0" {
		t.Fatalf("findWirelessInterface = %q, %v; want wlan0", iface, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "wlan0")); err != nil {
		t.Fatal(err)
	}
	if iface, err := findWirelessInterface(root); err != nil || iface != "wlan1" {
		t.Fatalf("phy80211 fallback = %q, %v; want wlan1", iface, err)
	}
	if iface, err := findWirelessInterface(t.TempDir()); err == nil || iface != "" {
		t.Fatalf("empty root returned %q, %v; want an error", iface, err)
	}
	if iface, err := findWirelessInterface(filepath.Join(root, "missing")); err == nil || iface != "" {
		t.Fatalf("missing root returned %q, %v; want an error", iface, err)
	}
	if !validIfaceName("wlan0") || validIfaceName("--help") || validIfaceName("a/b") || validIfaceName("") {
		t.Fatal("interface-name validation accepted an unsafe value")
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

func TestMergeWiFiNetworksMarksSavedAndActive(t *testing.T) {
	scanned := []wifiNetwork{
		{SSID: "Cafe", Signal: -80},
		{SSID: "HomeNet", Signal: -47, Secured: true},
	}
	saved := []savedNetwork{{ID: 3, SSID: "HomeNet"}}
	st := wifiStatus{State: wifiCompleteState, SSID: "HomeNet", Connected: true}

	nets := mergeWiFiNetworks(scanned, saved, st)
	if len(nets) != 2 {
		t.Fatalf("merged to %d networks: %+v", len(nets), nets)
	}
	if nets[0].SSID != "HomeNet" || !nets[0].Active || !nets[0].Saved || nets[0].SaveID != 3 {
		t.Fatalf("active row = %+v, want HomeNet active and saved as id 3", nets[0])
	}
	if !wifiNeedsPassphrase("[WPA2-PSK-CCMP][ESS]") {
		t.Error("WPA2 flags were classified as open")
	}
	if wifiNeedsPassphrase("[OWE-TRANSITION][ESS]") {
		t.Error("OWE was classified as needing a passphrase")
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
	if got := wpaQuote(`Home"Net\`); got != `"Home\"Net\\"` {
		t.Fatalf("wpaQuote = %s", got)
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

func wifiStubCommand(args []string) string {
	if len(args) >= 3 {
		return args[2]
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

func TestWiFiSignalBars(t *testing.T) {
	cases := []struct {
		rssi int
		bars int
	}{{-40, 4}, {-55, 4}, {-60, 3}, {-67, 3}, {-70, 2}, {-78, 2}, {-90, 1}, {0, 1}}
	for _, c := range cases {
		if got := wifiSignalBars(c.rssi); got != c.bars {
			t.Errorf("wifiSignalBars(%d) = %d, want %d", c.rssi, got, c.bars)
		}
	}
}

func TestWiFiPanelKeyboardOwnsPassphrase(t *testing.T) {
	prevTarget, prevActive := vkTarget, virtualKeyboardActive
	prevPending, prevBuffer := wifiPendingSSID, wifiPassphraseBuffer
	prevPanel := wifiPanel
	t.Cleanup(func() {
		vkTarget, virtualKeyboardActive = prevTarget, prevActive
		wifiPendingSSID, wifiPassphraseBuffer = prevPending, prevBuffer
		wifiPanel = prevPanel
	})

	vkTarget = vkTargetWiFi
	virtualKeyboardActive = true
	wifiPendingSSID = "HomeNet"
	vkSetText("hunter2secret", nil)
	if got := vkCurrentText(); got != "hunter2secret" {
		t.Fatalf("passphrase buffer = %q", got)
	}
	if title := vkTitle(); title != "WI-FI PASSWORD" {
		t.Fatalf("keyboard title = %q", title)
	}
	vkCloseKeyboard()
	if wifiPassphraseBuffer != "" || wifiPendingSSID != "" {
		t.Fatalf("closing the keyboard kept the secret: buffer=%q ssid=%q",
			wifiPassphraseBuffer, wifiPendingSSID)
	}

	wifiPanel = wifiPanelState{open: true}
	virtualKeyboardActive = true
	if handleWiFiKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}, &Config{}) {
		t.Fatal("the panel consumed a key while its keyboard was open")
	}
	virtualKeyboardActive = false
	if !handleWiFiKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, &Config{}) {
		t.Fatal("the panel did not consume a key while it was the active modal")
	}
}

func TestEnsureHardwareWiFiEntry(t *testing.T) {
	cfg := &Config{Height: 720, Scenes: []SceneConfig{{Name: "Hardware"}}}
	ensureHardwareProcessEntry(cfg)
	ensureHardwareWiFiEntry(cfg)
	ensureHardwareWiFiEntry(cfg)

	var found *Element
	count := 0
	for i := range cfg.Scenes[0].Elements {
		e := &cfg.Scenes[0].Elements[i]
		if e.Trigger == "wifi_open" {
			found = e
			count++
		}
	}
	if count != 1 || found == nil {
		t.Fatalf("Hardware scene has %d Wi-Fi buttons, want 1", count)
	}
	if found.Text != "Wi-Fi" {
		t.Fatalf("Wi-Fi button text = %q", found.Text)
	}
	for _, e := range cfg.Scenes[0].Elements {
		if e.Trigger != "processes_open" {
			continue
		}
		if right := e.X + getElementWidth(e, 160); found.X < right {
			t.Fatalf("Wi-Fi button x=%d overlaps Processes (ends at %d)", found.X, right)
		}
	}
	if bottom := found.Y + getElementHeight(*found, 45); bottom > int32(cfg.Height)-20 {
		t.Fatalf("Wi-Fi button bottom %d leaves the screen (%d)", bottom, cfg.Height)
	}
}

// waitForWiFiIdle drains results until no worker is in flight. It is the test
// stand-in for the render loop's drain.
func waitForWiFiIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		wifiDrainUpdates()
		if !wifiWorkPending && !wifiPanel.busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Wi-Fi worker did not finish (busy=%v, %q)", wifiPanel.busy, wifiPanel.busyLabel)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWiFiPanelJoinFlow walks the real UI path: open the picker, scan, choose a
// secured network, type the password on the keyboard, submit, and see the
// connected status. Only wpa_cli itself is stubbed.
func TestWiFiPanelJoinFlow(t *testing.T) {
	_ = newProbeFrame(t)
	withScreen(t, probeW, probeH)

	scanned := false
	joined := false
	stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		if stdin != "" {
			return "OK\n", nil
		}
		switch wifiStubCommand(args) {
		case "scan":
			scanned = true
			return "OK\n", nil
		case "scan_results":
			if !scanned {
				return "bssid / frequency / signal level / flags / ssid\n", nil
			}
			return wifiTestScan, nil
		case "status":
			if joined {
				return "wpa_state=COMPLETED\nssid=HomeNet\nip_address=192.168.1.20\n", nil
			}
			return "wpa_state=DISCONNECTED\n", nil
		case "add_network":
			return "0\n", nil
		case "list_networks":
			// Nothing is saved before the join; afterwards the network the test
			// just added is remembered.
			if joined {
				return "network id / ssid / bssid / flags\n0\tHomeNet\tany\t[CURRENT]\n", nil
			}
			return "network id / ssid / bssid / flags\n", nil
		case "select_network":
			joined = true
			return "OK\n", nil
		}
		return "OK\n", nil
	})

	prevPanel := wifiPanel
	prevActive, prevTarget := virtualKeyboardActive, vkTarget
	prevPending, prevBuffer := wifiPendingSSID, wifiPassphraseBuffer
	t.Cleanup(func() {
		// Let a worker finish before the stub runner is restored, so a failed
		// assertion cannot leave a goroutine calling a replaced function.
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
		virtualKeyboardActive, vkTarget = prevActive, prevTarget
		wifiPendingSSID, wifiPassphraseBuffer = prevPending, prevBuffer
	})

	wifiGeneration++
	wifiDrainDiscarded()
	openWiFiPanel()
	if !wifiPanel.available {
		t.Fatalf("stubbed picker reported unavailable: %s", wifiPanel.detail)
	}
	waitForWiFiIdle(t)
	if wifiPanel.err != "" {
		t.Fatalf("scan failed: %s", wifiPanel.err)
	}
	if len(wifiPanel.networks) < 1 || wifiPanel.networks[0].SSID != "HomeNet" {
		t.Fatalf("scan did not populate the list: %+v", wifiPanel.networks)
	}

	wifiActivate()
	if !virtualKeyboardActive || vkTarget != vkTargetWiFi {
		t.Fatalf("choosing a secured network did not open the password keyboard (active=%v target=%v)",
			virtualKeyboardActive, vkTarget)
	}
	if wifiPendingSSID != "HomeNet" {
		t.Fatalf("pending network = %q, want HomeNet", wifiPendingSSID)
	}

	vkSetText("hunter2secret", nil)
	vkSubmit(&Config{}, false)
	if wifiPassphraseBuffer != "" || wifiPendingSSID != "" {
		t.Fatalf("submit kept the secret: buffer=%q pending=%q", wifiPassphraseBuffer, wifiPendingSSID)
	}
	waitForWiFiIdle(t)

	if wifiPanel.err != "" {
		t.Fatalf("join failed: %s", wifiPanel.err)
	}
	if !wifiPanel.status.Connected || wifiPanel.status.IP != "192.168.1.20" {
		t.Fatalf("panel status after join = %+v", wifiPanel.status)
	}
	if len(wifiPanel.networks) == 0 || !wifiPanel.networks[0].Active {
		t.Fatalf("joined network is not marked active: %+v", wifiPanel.networks)
	}
	if !strings.Contains(currentToast.Message, "Connected to HomeNet") {
		t.Fatalf("no success toast, got %q", currentToast.Message)
	}
}

// TestShippedConfigHardwareGetsWiFiEntry checks the real config: loading it
// must leave exactly one Wi-Fi button on the Hardware scene.
func TestShippedConfigHardwareGetsWiFiEntry(t *testing.T) {
	cfg, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := sceneIndexExact(cfg, "Hardware")
	if idx < 0 {
		t.Skip("this config has no Hardware scene")
	}
	count := 0
	for _, e := range cfg.Scenes[idx].Elements {
		if e.Trigger == "wifi_open" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Hardware scene has %d Wi-Fi buttons, want 1", count)
	}
}

// TestWiFiPanelRenders is a layout smoke test: the modal must fit both the
// 1280x720 panel and a 480p handheld, and draw one rect per visible row.
func TestWiFiPanelRenders(t *testing.T) {
	_ = newProbeFrame(t)
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(r)

	prev := wifiPanel
	t.Cleanup(func() {
		wifiPanel = prev
		wifiRowRects = nil
		ClearTextCache()
	})

	for _, size := range []struct {
		w, h int32
	}{{probeW, probeH}, {800, 480}} {
		withScreen(t, size.w, size.h)
		wifiPanel = wifiPanelState{
			open:      true,
			available: true,
			loadedAt:  time.Now(),
			status: wifiStatus{
				State:     wifiCompleteState,
				SSID:      "HomeNet",
				IP:        "192.168.1.20",
				Connected: true,
			},
			networks: []wifiNetwork{
				{SSID: "HomeNet", BSSID: "aa:bb:cc:dd:ee:01", Signal: -47, Secured: true, Saved: true, Active: true},
				{SSID: "Cafe", BSSID: "aa:bb:cc:dd:ee:03", Signal: -82},
			},
		}
		r.SetDrawColor(9, 14, 25, 255)
		r.Clear()
		renderWiFiPanel(r, &Config{})

		if wifiPanelRect.W <= 0 || wifiPanelRect.H <= 0 {
			t.Fatalf("%dx%d: panel was not laid out", size.w, size.h)
		}
		if wifiPanelRect.X < 0 || wifiPanelRect.Y < 0 ||
			wifiPanelRect.X+wifiPanelRect.W > size.w || wifiPanelRect.Y+wifiPanelRect.H > size.h {
			t.Fatalf("%dx%d: panel %+v leaves the screen", size.w, size.h, wifiPanelRect)
		}
		if len(wifiRowRects) != len(wifiPanel.networks) {
			t.Fatalf("%dx%d: drew %d row rects for %d networks",
				size.w, size.h, len(wifiRowRects), len(wifiPanel.networks))
		}
	}

	// An unavailable backend still renders its explanation instead of an empty
	// frame, and must not leave clickable rows behind.
	wifiPanel = wifiPanelState{open: true, available: false, detail: "No wireless network interface on this device"}
	renderWiFiPanel(r, &Config{})
	if len(wifiRowRects) != 0 {
		t.Fatalf("unavailable panel drew %d rows", len(wifiRowRects))
	}
}
