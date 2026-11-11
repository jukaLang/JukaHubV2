package main

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// psOptsUseTempHosts gives a test its own psremote.json, report path and an
// empty panel, and puts all three back afterwards.
func psOptsUseTempHosts(t *testing.T) string {
	t.Helper()
	clearToastAfterTest(t)
	dir := t.TempDir()
	prevHosts, prevLast, prevPanel, prevOpts := psHostsPathOverride, psLastRunPathOverride, chiakiPanel, psOptsHost
	psHostsPathOverride = filepath.Join(dir, "psremote.json")
	psLastRunPathOverride = filepath.Join(dir, psLastRunFileName)
	chiakiPanel = chiakiPanelState{}
	psOptsHost = ""
	psStreamSettingsCache.mu.Lock()
	psStreamSettingsCache.loaded = false
	psStreamSettingsCache.mu.Unlock()
	psLastRunNoteSet("", ToastKindInfo, "")
	t.Cleanup(func() {
		psHostsPathOverride, psLastRunPathOverride, chiakiPanel, psOptsHost = prevHosts, prevLast, prevPanel, prevOpts
		psStreamSettingsCache.mu.Lock()
		psStreamSettingsCache.loaded = false
		psStreamSettingsCache.mu.Unlock()
		psLastRunNoteSet("", ToastKindInfo, "")
	})
	return dir
}

// sessionLines splits a session file into key -> value, failing on any line
// that is not key=value.
func sessionLines(t *testing.T, text string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			t.Fatalf("session line %q is not key=value in:\n%s", line, text)
		}
		if _, dup := out[key]; dup {
			t.Fatalf("key %q appears twice in:\n%s", key, text)
		}
		out[key] = value
	}
	return out
}

// Every key of the helper's contract is written, in the helper's spelling.
func TestPSSessionExtrasCarryEveryStreamKey(t *testing.T) {
	o := psStreamOptions{
		Name: "Living Room", Codec: psCodecH264, LoginPIN: "12 34", SelectButton: psSelectCreate,
		DpadStick: true, Rumble: true, StatusFile: "/mnt/SDCARD/Apps/JukaHub/psremote-last.txt", Hints: true,
		Motor: psStreamMotor{GPIO: "/sys/class/gpio/gpio227/value", Level: "/sys/class/motor/voltage", LevelMin: 500000, LevelMax: 3300000},
	}
	got := sessionLines(t, psSessionExtras(o))
	want := map[string]string{
		"name": "Living Room", "codec": "h264", "login_pin": "1234", "select_button": "create",
		"dpad_stick": "1", "rumble": "1", "motor_gpio": "/sys/class/gpio/gpio227/value",
		"motor_level": "/sys/class/motor/voltage", "motor_level_min": "500000", "motor_level_max": "3300000",
		"status_file": "/mnt/SDCARD/Apps/JukaHub/psremote-last.txt", "hints": "1", "remote": "0",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extras = %v, want exactly %v", got, want)
	}
}

// Optional keys stay out when there is nothing to say: no passcode that is
// not one, no motor nodes without a motor, no strength range that is empty.
func TestPSSessionExtrasLeaveOutWhatIsUnknown(t *testing.T) {
	got := sessionLines(t, psSessionExtras(psStreamOptions{Name: "PS5", Codec: psCodecH265, LoginPIN: "123",
		SelectButton: psSelectTouchpad, Motor: psStreamMotor{Level: "/sys/class/motor/level", LevelMax: 65535}}))
	for _, key := range []string{"login_pin", "motor_gpio", "motor_level", "status_file"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s written without a value worth sending: %v", key, got)
		}
	}
	if got["rumble"] != "0" || got["dpad_stick"] != "0" || got["hints"] != "0" {
		t.Errorf("off switches must still be written as 0: %v", got)
	}
	got = sessionLines(t, psSessionExtras(psStreamOptions{Motor: psStreamMotor{GPIO: "/g", Level: "/l", LevelMin: 5, LevelMax: 5}}))
	if got["motor_gpio"] != "/g" || got["motor_level"] != "" {
		t.Errorf("an empty strength range keeps only the switch: %v", got)
	}
}

// A console name is whatever the network said. A newline in it must not
// become a second line of the session file, which the helper would read as a
// key of its own.
func TestPSSessionExtrasCannotInjectALine(t *testing.T) {
	text := psSessionExtras(psStreamOptions{Name: "PS5\nhost=203.0.113.9\rrp_key=AAAA x", Codec: psCodecH264})
	got := sessionLines(t, text)
	if _, ok := got["host"]; ok {
		t.Fatalf("a name injected a host line:\n%s", text)
	}
	if _, ok := got["rp_key"]; ok {
		t.Fatalf("a name injected an rp_key line:\n%s", text)
	}
	if !strings.HasPrefix(got["name"], "PS5 host=") {
		t.Errorf("name = %q, want the text kept on one line", got["name"])
	}
}

// The extras follow psSessionFile's own keys without clashing with any.
func TestPSSessionFileWithExtrasHasNoDuplicateKeys(t *testing.T) {
	c := psConsole{Host: "192.168.1.40", Name: "PS5", PS5: true, Target: psTargetPS5_1, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA=="}
	o := psStreamOptionsFor(c, psRemoteSettings{}, DeviceTrimuiSmartPro, false)
	o.StatusFile = "/tmp/last.txt"
	got := sessionLines(t, psSessionFile(c, psQualityPresets[0], 80)+psSessionExtras(o))
	if got["host"] != "192.168.1.40" || got["codec"] != "h264" || got["name"] != "PS5" {
		t.Errorf("combined session file = %v", got)
	}
}

func TestPSResolveCodec(t *testing.T) {
	cases := []struct {
		setting string
		ps5     bool
		device  DeviceModel
		want    string
	}{
		{"", true, DeviceTrimuiSmartProS, psCodecH265},
		{"auto", true, DeviceTrimuiSmartPro, psCodecH264},
		{"", true, DeviceTrimuiBrick, psCodecH264},
		{"", true, DeviceTrimuiBrickPro, psCodecH264},
		{"HEVC", true, DeviceTrimuiSmartPro, psCodecH265},
		{"h.264", true, DeviceTrimuiSmartProS, psCodecH264},
		// A PS4 sends H.264 whatever is set.
		{"h265", false, DeviceTrimuiSmartProS, psCodecH264},
		{"", false, DeviceTrimuiSmartProS, psCodecH264},
	}
	for _, c := range cases {
		if got := psResolveCodec(c.setting, c.ps5, c.device); got != c.want {
			t.Errorf("psResolveCodec(%q, ps5=%v, %s) = %q, want %q", c.setting, c.ps5, c.device, got, c.want)
		}
	}
	if got := psCodecRowStatus("", DeviceTrimuiSmartPro); got != "Auto (H.264)" {
		t.Errorf("row status = %q, want Auto (H.264)", got)
	}
	if got := psCodecRowStatus("h265", DeviceTrimuiSmartPro); got != "H.265" {
		t.Errorf("row status = %q, want H.265", got)
	}
}

func TestPSNextCodecCycles(t *testing.T) {
	seen := []string{}
	s := ""
	for i := 0; i < 3; i++ {
		s = psNextCodec(s, 1)
		seen = append(seen, s)
	}
	if strings.Join(seen, ",") != "h264,h265,auto" {
		t.Errorf("forward cycle = %v", seen)
	}
	if got := psNextCodec("auto", -1); got != psCodecH265 {
		t.Errorf("back from auto = %q, want h265", got)
	}
}

func TestPSStreamOptionsForDevicesAndSettings(t *testing.T) {
	c := psConsole{Host: "192.168.1.40", PS5: true, LoginPIN: " 4321 "}
	o := psStreamOptionsFor(c, psRemoteSettings{SelectButton: "Create", NoRumble: true}, DeviceTrimuiBrick, true)
	if !o.DpadStick || o.Rumble || o.SelectButton != psSelectCreate || !o.Remote || o.LoginPIN != "4321" || !o.Hints {
		t.Errorf("Brick options = %+v", o)
	}
	o = psStreamOptionsFor(c, psRemoteSettings{}, DeviceTrimuiSmartPro, false)
	if o.DpadStick || !o.Rumble || o.SelectButton != psSelectTouchpad || o.Remote {
		t.Errorf("Smart Pro options = %+v", o)
	}
	if !psDpadStickDefault(DeviceTrimuiHammer) || psDpadStickDefault(DeviceTrimuiSmartProS) {
		t.Error("the D-pad drives the stick only where there are no sticks")
	}
}

func TestPSStreamName(t *testing.T) {
	if got := psStreamName(psConsole{PS5: true}); got != "PS5" {
		t.Errorf("unnamed PS5 = %q", got)
	}
	if got := psStreamName(psConsole{Target: psTargetPS4FW10}); got != "PS4" {
		t.Errorf("unnamed PS4 = %q", got)
	}
	long := psStreamName(psConsole{Name: strings.Repeat("Living room ", 10)})
	if n := len([]rune(long)); n > psStreamNameMax || !strings.HasSuffix(long, "...") {
		t.Errorf("long name = %q (%d runes)", long, n)
	}
	if got := psStreamName(psConsole{Name: "\tDen\x00 PS5 "}); got != "Den  PS5" {
		t.Errorf("control characters = %q", got)
	}
}

func TestPSLoginPINValid(t *testing.T) {
	for pin, want := range map[string]bool{"1234": true, "0000": true, "123": false, "12345": false, "12a4": false, "": false, "１２３４": false} {
		if got := psLoginPINValid(pin); got != want {
			t.Errorf("psLoginPINValid(%q) = %v, want %v", pin, got, want)
		}
	}
	if got := psNormalizeLoginPIN(" 12-34 "); got != "1234" {
		t.Errorf("normalized = %q", got)
	}
}

// The address is the home one unless the console has an internet address and
// the home one does not answer.
func TestPSChooseStreamHost(t *testing.T) {
	prev := psStreamProbe
	t.Cleanup(func() { psStreamProbe = prev })
	var probed []string
	answer := false
	psStreamProbe = func(host string, ps5 bool, timeout time.Duration) bool {
		probed = append(probed, host)
		if timeout <= 0 || timeout > time.Second {
			t.Errorf("probe timeout %v, want a short one", timeout)
		}
		return answer
	}
	home := psConsole{Host: "192.168.1.40", PS5: true}
	if host, remote := psChooseStreamHost(home); host != "192.168.1.40" || remote || len(probed) != 0 {
		t.Fatalf("no internet address: %q remote=%v probed=%v", host, remote, probed)
	}
	away := home
	away.RemoteHost = "myhome.duckdns.org"
	answer = true
	if host, remote := psChooseStreamHost(away); host != "192.168.1.40" || remote {
		t.Fatalf("answering at home: %q remote=%v", host, remote)
	}
	answer = false
	if host, remote := psChooseStreamHost(away); host != "myhome.duckdns.org" || !remote {
		t.Fatalf("silent at home: %q remote=%v", host, remote)
	}
	same := home
	same.RemoteHost = "192.168.1.40"
	probed = nil
	if host, remote := psChooseStreamHost(same); host != "192.168.1.40" || remote || len(probed) != 0 {
		t.Fatalf("internet address = home address: %q remote=%v probed=%v", host, remote, probed)
	}
}

// The probe is a real discovery exchange: an answer from the console counts,
// silence does not, and nothing waits past the timeout.
func TestPSStreamProbeAt(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer conn.Close()
	go func() {
		buf := make([]byte, 512)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil || !strings.HasPrefix(string(buf[:n]), "SRCH * HTTP/1.1") {
			return
		}
		_, _ = conn.WriteToUDP([]byte("HTTP/1.1 620 Server Standby\nhost-name:PS5-123\nsystem-version:08050001\n"+
			"device-discovery-protocol-version:"+psProtoVersionPS5+"\n"), from)
	}()
	if !psStreamProbeAt(conn.LocalAddr().String(), psProtoVersionPS5, time.Second) {
		t.Fatal("a resting console's answer is an answer")
	}

	silent, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	addr := silent.LocalAddr().String()
	defer silent.Close()
	start := time.Now()
	if psStreamProbeAt(addr, psProtoVersionPS5, 150*time.Millisecond) {
		t.Fatal("silence is not an answer")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the probe waited %v past its 150 ms", took)
	}
}
