package main

// psremote_test.go — the parts of the native Remote Play client that do not
// need a console: the session file contract, credential validation, the host
// store, engine lookup and the handoff script.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func useTempPSHosts(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "psremote.json")
	old := psHostsPathOverride
	psHostsPathOverride = path
	t.Cleanup(func() { psHostsPathOverride = old })
	return path
}

func testConsole() psConsole {
	return psConsole{
		Host:      "192.168.1.40",
		Name:      "Living Room",
		Target:    psTargetPS5_1,
		PS5:       true,
		RegistKey: "0a1b2c3d",
		RPKey:     "AAECAwQFBgcICQoLDA0ODw==",
		PairedAt:  time.Now(),
	}
}

func TestPSSessionFileMatchesHelperContract(t *testing.T) {
	got := psSessionFile(testConsole(), psQualityPresets[1], 65)
	// The helper parses key=value lines and deletes the file after reading it.
	for _, want := range []string{
		"host=192.168.1.40",
		"regist_key=0a1b2c3d",
		"rp_key=AAECAwQFBgcICQoLDA0ODw==",
		"ps5=1",
		"target=1000100",
		"rp_version=1.0",
		"width=960",
		"height=540",
		"fps=30",
		"bitrate=4000",
		"volume=65",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("session file missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "verbose=1") {
		t.Fatalf("session file should not turn on verbose logs by default:\n%s", got)
	}
}

func TestPSSessionFileDefaultsAndClamping(t *testing.T) {
	console := testConsole()
	console.Target = 0
	console.PS5 = false
	console.RegistKey = "ABCDEF"
	got := psSessionFile(console, psQualityPresets[0], 500)
	if !strings.Contains(got, "target=1000\n") || !strings.Contains(got, "ps5=0\n") {
		t.Fatalf("unpaired target defaults wrong:\n%s", got)
	}
	if !strings.Contains(got, "volume=100\n") {
		t.Fatalf("volume not clamped:\n%s", got)
	}
	if !strings.Contains(got, "rp_version=10.0\n") {
		t.Fatalf("py version wrong:\n%s", got)
	}
}

func TestPSRPVersionByTarget(t *testing.T) {
	cases := map[int]string{
		psTargetPS4FW8:  "8.0",
		psTargetPS4FW9:  "9.0",
		psTargetPS4FW10: "10.0",
		psTargetPS5:     "1.0",
		psTargetPS5_1:   "1.0",
	}
	for target, want := range cases {
		if got := psRPVersion(target); got != want {
			t.Fatalf("target %d → %q, want %q", target, got, want)
		}
	}
}

func TestPSTargetFromSystemVersion(t *testing.T) {
	cases := []struct {
		version string
		ps5     bool
		want    int
	}{
		{"8050001", true, psTargetPS5_1},
		{"8050000", true, psTargetPS5},
		{"8000000", false, psTargetPS4FW10},
		{"7000000", false, psTargetPS4FW9},
		{"4500000", false, psTargetPS4FW8},
		{"", false, 0},
		{"8050001", false, psTargetPS4FW10},
	}
	for _, tc := range cases {
		if got := psTargetFromSystemVersion(tc.version, tc.ps5); got != tc.want {
			t.Fatalf("version %q ps5=%v → %d, want %d", tc.version, tc.ps5, got, tc.want)
		}
	}
}

func TestPSCredentialValidation(t *testing.T) {
	for _, key := range []string{"0a1b2c3d", "ABCDEF", "1"} {
		if !psRegistKeyValid(key) {
			t.Fatalf("%q should be a valid regist key", key)
		}
	}
	for _, key := range []string{"", "0a1b2c3d4", "zzzz", "123456789"} {
		if psRegistKeyValid(key) {
			t.Fatalf("%q should be rejected", key)
		}
	}
	if !psRPKeyValid(testConsole().RPKey) {
		t.Fatal("the fixture rp_key should be valid")
	}
	if psRPKeyValid("stub-rp-key-1") || psRPKeyValid("AAAA") {
		t.Fatal("short or placeholder rp_keys must be rejected")
	}
	converted, err := psRPKeyFromHex("000102030405060708090a0b0c0d0e0f")
	if err != nil || converted != testConsole().RPKey {
		t.Fatalf("hex conversion = %q (%v)", converted, err)
	}
	if _, err := psRPKeyFromHex("00ff"); err == nil {
		t.Fatal("a short rp_key should be refused")
	}
}

func TestPSHostStoreRoundTrip(t *testing.T) {
	useTempPSHosts(t)
	console := testConsole()
	upsertPSConsole(console)
	state := loadPSHosts()
	if len(state.Hosts) != 1 {
		t.Fatalf("stored %d hosts", len(state.Hosts))
	}
	if state.Hosts[0].RegistKey != console.RegistKey || !state.Hosts[0].PS5 {
		t.Fatalf("stored host = %+v", state.Hosts[0])
	}
	if info, err := os.Stat(psHostsPath()); err != nil {
		t.Fatalf("hosts file missing: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		// Windows has no 0600 mode; the handheld (Linux) is where it matters.
		t.Fatalf("hosts file mode = %v, want 0600", info.Mode().Perm())
	}

	// Discovery updates the name but must never clear the pairing.
	upsertPSConsole(psConsole{Host: console.Host, Name: "Studio", LastSeen: time.Now()})
	state = loadPSHosts()
	if state.Hosts[0].RegistKey != console.RegistKey || state.Hosts[0].RPKey != console.RPKey {
		t.Fatalf("discovery wiped credentials: %+v", state.Hosts[0])
	}
	if state.Hosts[0].Name != "Studio" {
		t.Fatalf("name not updated: %+v", state.Hosts[0])
	}

	state = removePSConsole(console.Host)
	if len(state.Hosts) != 0 {
		t.Fatalf("remove left %d hosts", len(state.Hosts))
	}
}

func TestPSHostStoreDoesNotPersistUnpairedDiscoveries(t *testing.T) {
	useTempPSHosts(t)
	upsertPSConsole(psConsole{Host: "192.168.1.99", Name: "Bedroom", Target: psTargetPS5_1, PS5: true})
	if state := loadPSHosts(); len(state.Hosts) != 0 {
		t.Fatalf("an unpaired console was stored: %+v", state.Hosts)
	}
}

func TestPSHostStoreDropsBrokenCredentials(t *testing.T) {
	path := useTempPSHosts(t)
	raw := `{"version":1,"settings":{"quality":0,"volume":80},"hosts":[
		{"host":"10.0.0.2","regist_key":"0a1b2c3d"},
		{"host":"10.0.0.3","regist_key":"0a1b2c3d","rp_key":"AAECAwQFBgcICQoLDA0ODw=="}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	state := loadPSHosts()
	if len(state.Hosts) != 2 {
		t.Fatalf("hosts = %+v", state.Hosts)
	}
	if state.Hosts[0].paired() {
		t.Fatalf("a host without rp_key must load as unpaired: %+v", state.Hosts[0])
	}
	if !state.Hosts[1].paired() {
		t.Fatalf("a complete host must stay paired: %+v", state.Hosts[1])
	}
}

func TestPSEngineLookup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stream := filepath.Join(bin, "chiaki-stream")
	regist := filepath.Join(bin, "chiaki-regist")
	for _, path := range []string{stream, regist} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	oldStream, oldRegist := psEngineStreamOverride, psEngineRegistOverride
	psEngineStreamOverride, psEngineRegistOverride = stream, regist
	t.Cleanup(func() { psEngineStreamOverride, psEngineRegistOverride = oldStream, oldRegist })

	engine := psRemoteEngine()
	if engine.Stream != stream || engine.Regist != regist || !engine.installed() {
		t.Fatalf("engine = %+v", engine)
	}

	oldRegist = psEngineRegistOverride
	psEngineRegistOverride = ""
	t.Cleanup(func() { psEngineRegistOverride = oldRegist })
	if engine := psRemoteEngine(); engine.Regist != "" {
		t.Fatalf("regist should be optional, got %+v", engine)
	}
}

func TestPSStreamRefusesIncompletePairing(t *testing.T) {
	useTempPSHosts(t)
	console := testConsole()
	console.RPKey = ""
	if err := psRemoteStream(console, psRemoteSettings{}); err == nil ||
		!strings.Contains(err.Error(), "pair") {
		t.Fatalf("unpaired stream should be refused, got %v", err)
	}
	console = testConsole()
	console.RegistKey = "0123456789"
	if err := psRemoteStream(console, psRemoteSettings{}); err == nil ||
		!strings.Contains(err.Error(), "pairing key") {
		t.Fatalf("bad regist key should be refused, got %v", err)
	}
}

func TestPSHandoffScriptRunsTheStreamHelper(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "chiaki-stream")
	script := psHandoffScript(4242, psEngine{Stream: stream, Root: dir}, "/tmp/session file.conf", "Living Room · PS5")
	for _, want := range []string{
		"JUKAHUB_PID=4242",
		"'" + stream + "'",
		"'/tmp/session file.conf'",
		`"$BIN" "$SESSION"`,
		"pkill -STOP runtrimui.sh",
		"pidof MainUI",
		"rm -f \"$SESSION\"",
		"rm -f \"$0\"",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("handoff script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "launch.sh") {
		t.Fatal("the native handoff must not run the Python port's launcher")
	}
}

func TestPSWriteSessionFileIsPrivate(t *testing.T) {
	path, err := psWriteSessionFile(testConsole(), psQualityPresets[0], 80)
	if err != nil {
		t.Fatalf("write session: %v", err)
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows reports the read-only bit only, so the permission check is a
	// Linux/handheld check; the file is created by CreateTemp at 0600 either way.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(data), "host=192.168.1.40\n") {
		t.Fatalf("session content = %q", string(data))
	}
}
