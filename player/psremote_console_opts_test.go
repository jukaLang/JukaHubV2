package main

import (
	"strings"
	"testing"
	"time"
)

func TestPSCleanRemoteHost(t *testing.T) {
	for in, want := range map[string]string{
		" myhome.duckdns.org ":               "myhome.duckdns.org",
		"https://myhome.duckdns.org:9295/rp": "myhome.duckdns.org",
		"203.0.113.7:9295":                   "203.0.113.7",
		"[fd7a:115c:a1e0::1]:9295":           "fd7a:115c:a1e0::1",
		"fd7a:115c:a1e0::1":                  "fd7a:115c:a1e0::1",
		"100.101.102.103":                    "100.101.102.103",
		"":                                   "",
	} {
		if got := psCleanRemoteHost(in); got != want {
			t.Errorf("psCleanRemoteHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPSRemoteHostProblem(t *testing.T) {
	if p := psRemoteHostProblem("myhome.duckdns.org", "192.168.1.40"); p != "" {
		t.Errorf("a host name was refused: %q", p)
	}
	if p := psRemoteHostProblem("100.101.102.103", "192.168.1.40"); p != "" {
		t.Errorf("an IPv4 VPN address was refused: %q", p)
	}
	if p := psRemoteHostProblem("my home", "192.168.1.40"); p == "" {
		t.Error("an address with a space was accepted")
	}
	if p := psRemoteHostProblem("192.168.1.40", "192.168.1.40"); !strings.Contains(p, "home address") {
		t.Errorf("the home address as the internet address = %q", p)
	}
	// The wake and the home-or-away probe speak IPv4 only: an IPv6 address
	// would be saved and never streamed to.
	for _, v6 := range []string{"fd7a:115c:a1e0::1", "2001:db8::7"} {
		if p := psRemoteHostProblem(v6, "192.168.1.40"); !strings.Contains(p, "IPv6") {
			t.Errorf("IPv6 %s = %q, want it refused with the reason", v6, p)
		}
	}
}

// The port people type with an address is left out, and reported back so a
// forward to another port is not a silent failure later.
func TestPSSplitRemoteHost(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"203.0.113.7:9295", "203.0.113.7", "9295"},
		{"https://myhome.duckdns.org:19295/rp", "myhome.duckdns.org", "19295"},
		{"[fd7a:115c:a1e0::1]:9296", "fd7a:115c:a1e0::1", "9296"},
		{"[fd7a:115c:a1e0::1]", "fd7a:115c:a1e0::1", ""},
		{"myhome.duckdns.org", "myhome.duckdns.org", ""},
		{"::ffff:203.0.113.7", "203.0.113.7", ""},
		{"host:port", "host:port", ""},
	}
	for _, c := range cases {
		if host, port := psSplitRemoteHost(c.in); host != c.host || port != c.port {
			t.Errorf("psSplitRemoteHost(%q) = %q, %q; want %q, %q", c.in, host, port, c.host, c.port)
		}
	}
}

// The options a user sets on a console survive what happens to it later: a
// scan, a new pairing, the panel's own settings save.
func TestPSConsoleOptionsSurviveDiscoveryAndRepairing(t *testing.T) {
	psOptsUseTempHosts(t)
	host := "192.168.1.40"
	upsertPSConsole(psConsole{Host: host, Name: "Den", PS5: true, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA=="})
	if err := psUpdateConsole(host, func(c *psConsole) { c.RemoteHost, c.LoginPIN = "myhome.duckdns.org", "1234" }); err != nil {
		t.Fatal(err)
	}
	// A scan names the console again; a new pairing replaces its keys.
	upsertPSConsole(psConsole{Host: host, Name: "Den PS5", LastSeen: time.Now()})
	upsertPSConsole(psConsole{Host: host, PS5: true, RegistKey: "11223344", RPKey: "BBBBBBBBBBBBBBBBBBBBBA==", PairedAt: time.Now()})
	psPanelMergeDiscovered([]psDiscovered{{Host: host, Name: "Den PS5", PS5: true, State: "ready"}})
	chiakiPanel.quality, chiakiPanel.volume = 1, 70
	psPanelSaveSettings()

	state := loadPSHosts()
	if len(state.Hosts) != 1 {
		t.Fatalf("hosts = %+v", state.Hosts)
	}
	c := state.Hosts[0]
	if c.RemoteHost != "myhome.duckdns.org" || c.LoginPIN != "1234" || c.RegistKey != "11223344" {
		t.Errorf("after a scan and a re-pairing: %+v", c)
	}
	for _, listed := range chiakiPanel.consoles {
		if listed.Host == host && listed.RemoteHost != "myhome.duckdns.org" {
			t.Errorf("the panel's merged list lost the internet address: %+v", listed)
		}
	}
	if err := psUpdateConsole("192.168.1.99", func(c *psConsole) { c.LoginPIN = "0000" }); err == nil {
		t.Error("an unknown console took an option")
	}
}

// The stream settings keep the panel's own (quality, volume, Account-ID) and
// the other way round.
func TestPSStreamSettingsKeepThePanelSettings(t *testing.T) {
	psOptsUseTempHosts(t)
	chiakiPanel.quality, chiakiPanel.volume, chiakiPanel.account = 2, 55, "FYHpffQQIhE="
	psPanelSaveSettings()
	psSaveStreamSettings(func(s *psRemoteSettings) { s.Codec, s.SelectButton, s.NoRumble = psCodecH265, psSelectCreate, true })
	chiakiPanel.volume = 60
	psPanelSaveSettings()
	s := loadPSHosts().Settings
	if s.Quality != 2 || s.Volume != 60 || s.Account != "FYHpffQQIhE=" || s.Codec != psCodecH265 ||
		s.SelectButton != psSelectCreate || !s.NoRumble {
		t.Errorf("settings = %+v", s)
	}
	if got := psPanelStreamSettings(); got.Codec != psCodecH265 {
		t.Errorf("cached settings = %+v", got)
	}
}

func TestPSPanelCycleStreamSettings(t *testing.T) {
	psOptsUseTempHosts(t)
	if !psPanelCycleStreamSetting(psActionCodec, 1, false) || loadPSHosts().Settings.Codec != psCodecH264 {
		t.Errorf("codec after one step = %q", loadPSHosts().Settings.Codec)
	}
	psPanelCycleStreamSetting(psActionSelectButton, 1, true)
	if got := loadPSHosts().Settings.SelectButton; got != psSelectCreate {
		t.Errorf("select after one press = %q", got)
	}
	psPanelCycleStreamSetting(psActionRumble, -1, false)
	if !loadPSHosts().Settings.NoRumble {
		t.Error("rumble did not turn off")
	}
	if psPanelCycleStreamSetting(psActionQuality, 1, false) {
		t.Error("a row this file does not own was handled")
	}
	rows := psStreamSettingRows()
	if len(rows) != 3 || rows[1].status != "Create" || rows[2].status != "Off" {
		t.Errorf("stream rows = %+v", rows)
	}
}

// The options page shows what is set, and none of its rows carry the console's
// host — Y forgets the focused row's console, and only Forget should.
func TestPSConsoleOptsRows(t *testing.T) {
	psOptsUseTempHosts(t)
	host := "192.168.1.40"
	chiakiPanel.consoles = []psConsole{{Host: host, Name: "Den", PS5: true, RegistKey: "0a1b2c3d",
		RPKey: "AAAAAAAAAAAAAAAAAAAAAA==", RemoteHost: "myhome.duckdns.org", LoginPIN: "1234"}}
	psOptsHost = host
	rows := psConsoleOptsRows()
	labels := []string{}
	for _, row := range rows {
		labels = append(labels, row.label+"="+row.status)
		if row.host != "" {
			t.Errorf("row %q carries host %q", row.label, row.host)
		}
	}
	want := "Play=Play,Internet address=Edit,Login passcode=Saved,Remove the saved passcode=Remove,Forget this console=Forget,Back="
	if got := strings.Join(labels, ","); got != want {
		t.Errorf("rows = %s\nwant   %s", got, want)
	}

	chiakiPanel.consoles = []psConsole{{Host: host, Name: "Den", PS5: true}}
	rows = psConsoleOptsRows()
	if rows[0].action != psActionOptsPair || rows[1].action != psActionNone || rows[2].action != psActionNone {
		t.Errorf("an unpaired console's options = %+v", rows)
	}
	for _, row := range rows {
		if row.action == psActionOptsForget {
			t.Error("an unpaired console offers Forget")
		}
	}
	psOptsHost = "gone"
	if rows := psConsoleOptsRows(); rows[len(rows)-1].action != psActionOptsBack {
		t.Errorf("a vanished console still needs a way back: %+v", rows)
	}
}

// A console that is paired again at a new address leaves a stale entry for
// the same MAC behind; when a sweep drops it, its internet address and
// passcode stay with the console instead of going with it.
func TestPSRehomeKeepsTheConsoleOptions(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	hosts := []psConsole{
		{Host: "192.168.1.40", PS5: true, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA==", ServerMAC: "a1b2c3d4e5f6",
			PairedAt: old, RemoteHost: "myhome.duckdns.org", LoginPIN: "1234"},
		{Host: "192.168.1.41", PS5: true, RegistKey: "11223344", RPKey: "BBBBBBBBBBBBBBBBBBBBBA==", ServerMAC: "a1b2c3d4e5f6",
			PairedAt: time.Now()},
	}
	out, changed := psRehomeConsoles(hosts, []psDiscovered{{Host: "192.168.1.42", HostID: "A1B2C3D4E5F6", PS5: true}})
	if !changed || len(out) != 1 {
		t.Fatalf("rehome = %+v changed=%v, want one entry", out, changed)
	}
	c := out[0]
	if c.Host != "192.168.1.42" || c.RegistKey != "11223344" || c.RemoteHost != "myhome.duckdns.org" || c.LoginPIN != "1234" {
		t.Errorf("kept entry = %+v, want the newest pairing at the new address with the old options", c)
	}
	if hosts[1].RemoteHost != "" {
		t.Error("the caller's slice was changed in place")
	}
}

// Start opens options only where the footer offers them: on a row that plays
// or pairs, never on a busy one.
func TestPSPanelOpenConsoleOptionsOnlyOnPlayOrPair(t *testing.T) {
	psOptsUseTempHosts(t)
	host := "192.168.1.40"
	chiakiPanel.open = true
	chiakiPanel.page = psPageConsoles
	chiakiPanel.consoles = []psConsole{{Host: host, Name: "Den", PS5: true, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA=="}}
	chiakiPanel.cursor = 0
	chiakiPanel.busy = "Pairing " + host + "…"
	psPanelOpenConsoleOptions()
	if chiakiPanel.page != psPageConsoles {
		t.Fatal("Start on a console that is pairing opened its options")
	}
	chiakiPanel.busy = ""
	if rows := chiakiRows(); len(rows) == 0 || rows[0].host != host || rows[0].action != psActionPlay {
		t.Skipf("the first row is not the console's Play row here: %+v", rows)
	}
	psPanelOpenConsoleOptions()
	if chiakiPanel.page != psPageConsoleOptions || psOptsHost != host {
		t.Errorf("Start on a Play row: page %v, options for %q", chiakiPanel.page, psOptsHost)
	}
}

// A stream setting that cannot be saved leaves the row on what the file says.
func TestPSSaveStreamSettingsFailureKeepsTheCacheOnDisk(t *testing.T) {
	dir := psOptsUseTempHosts(t)
	if _, err := psSaveStreamSettings(func(s *psRemoteSettings) { s.Codec = psCodecH264 }); err != nil {
		t.Fatal(err)
	}
	// A directory where the store should be: every save fails.
	psHostsPathOverride = dir
	s, err := psSaveStreamSettings(func(s *psRemoteSettings) { s.Codec = psCodecH265 })
	if err == nil {
		t.Skip("could not make the save fail here")
	}
	if s.Codec == psCodecH265 || psPanelStreamSettings().Codec == psCodecH265 {
		t.Errorf("after a failed save: returned %+v, cached %+v", s, psPanelStreamSettings())
	}
}

// Screenshot mode prints stand-ins for the console's addresses.
func TestPSOptsShownHost(t *testing.T) {
	prev := shotMode.on
	t.Cleanup(func() { shotMode.on = prev })
	shotMode.on = false
	if got := psOptsShownHost("myhome.duckdns.org", true); got != "myhome.duckdns.org" {
		t.Errorf("normal mode = %q", got)
	}
	shotMode.on = true
	if got := psOptsShownHost("myhome.duckdns.org", true); got != psShotInternetHost {
		t.Errorf("screenshot mode internet address = %q", got)
	}
	if got := psOptsShownHost("192.168.7.9", false); got != shotLANAddress {
		t.Errorf("screenshot mode home address = %q", got)
	}
	if got := psOptsShownHost("", true); got != "" {
		t.Errorf("no address = %q", got)
	}
}

// The keyboard fields store only what the console can use.
func TestPSPanelApplyOptsEdit(t *testing.T) {
	psOptsUseTempHosts(t)
	host := "192.168.1.40"
	upsertPSConsole(psConsole{Host: host, Name: "Den", PS5: true, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA=="})
	psPanelReload()
	psOptsHost = host
	prevOpen := psOptsKeyboardHook
	reopened := ""
	psOptsKeyboardHook = func(edit psPanelEdit, seed string) { reopened = seed }
	t.Cleanup(func() { psOptsKeyboardHook = prevOpen })

	psPanelApplyOptsEdit(psEditRemoteHost, "https://myhome.duckdns.org:9295")
	if got := loadPSHosts().Hosts[0].RemoteHost; got != "myhome.duckdns.org" {
		t.Errorf("internet address = %q", got)
	}
	psPanelApplyOptsEdit(psEditRemoteHost, "my home")
	if reopened != "my home" || loadPSHosts().Hosts[0].RemoteHost != "myhome.duckdns.org" {
		t.Errorf("a bad address must go back to the keyboard and change nothing (reopened %q)", reopened)
	}
	psPanelApplyOptsEdit(psEditRemoteHost, "")
	if got := loadPSHosts().Hosts[0].RemoteHost; got != "" {
		t.Errorf("an emptied field must remove the address, got %q", got)
	}

	// A port other than 9295 is left out and said so; IPv6 goes back.
	psPanelApplyOptsEdit(psEditRemoteHost, "203.0.113.7:19295")
	currentToastMu.Lock()
	toast := currentToast.Message
	currentToastMu.Unlock()
	if got := loadPSHosts().Hosts[0].RemoteHost; got != "203.0.113.7" || !strings.Contains(toast, ":19295") {
		t.Errorf("address with port 19295 = %q, toast %q", got, toast)
	}
	reopened = ""
	psPanelApplyOptsEdit(psEditRemoteHost, "[2001:db8::7]:9295")
	if reopened != "2001:db8::7" || loadPSHosts().Hosts[0].RemoteHost != "203.0.113.7" {
		t.Errorf("an IPv6 address must go back to the keyboard and change nothing (reopened %q)", reopened)
	}

	reopened = ""
	psPanelApplyOptsEdit(psEditLoginPIN, "12 3")
	if reopened != "12 3" || loadPSHosts().Hosts[0].LoginPIN != "" {
		t.Errorf("a 3-digit passcode was not sent back (reopened %q)", reopened)
	}
	psPanelApplyOptsEdit(psEditLoginPIN, "1234")
	if got := loadPSHosts().Hosts[0].LoginPIN; got != "1234" {
		t.Errorf("passcode = %q", got)
	}
	psPanelApplyOptsEdit(psEditLoginPIN, "")
	if got := loadPSHosts().Hosts[0].LoginPIN; got != "1234" {
		t.Errorf("an empty submit must keep the saved passcode, got %q", got)
	}
}
