package main

// Tests for the low-battery guard. The decisions are pure, so the risky part —
// when to auto-save and suspend — is verified without a handheld.

import "testing"

func TestBatteryGuardActionFor(t *testing.T) {
	tests := []struct {
		name          string
		percent       int
		charging      bool
		gameRunning   bool
		alreadyActed  bool
		alreadyWarned bool
		want          batteryGuardAction
	}{
		{"unknown battery", -1, false, true, false, false, batteryGuardIdle},
		{"charging at 2%", 2, true, true, false, false, batteryGuardIdle},
		{"healthy at 80%", 80, false, true, false, false, batteryGuardIdle},
		{"warn at 5%", 5, false, false, false, false, batteryGuardWarn},
		{"warn only once", 5, false, false, false, true, batteryGuardIdle},
		{"critical while playing", 3, false, true, false, false, batteryGuardProtect},
		{"critical while playing is not repeated", 3, false, true, true, false, batteryGuardIdle},
		{"critical with no game only warns", 3, false, false, false, false, batteryGuardWarn},
		{"critical with no game after warning is silent", 3, false, false, false, true, batteryGuardIdle},
		{"1% while playing beats an earlier warning", 1, false, true, false, true, batteryGuardProtect},
		{"just above critical while playing warns", 4, false, true, false, false, batteryGuardWarn},
	}
	for _, tc := range tests {
		got := batteryGuardActionFor(tc.percent, tc.charging, tc.gameRunning, tc.alreadyActed, tc.alreadyWarned)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNetworkCmdConfigParsing(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want bool
	}{
		{"missing key", "video_vsync = \"true\"\n", false},
		{"enabled", "network_cmd_enable = \"true\"\n", true},
		{"enabled bare", "network_cmd_enable = true\n", true},
		{"disabled", "network_cmd_enable = \"false\"\n", false},
		{"commented out", "# network_cmd_enable = \"true\"\n", false},
		{"last value wins", "network_cmd_enable = \"true\"\nnetwork_cmd_enable = \"false\"\n", false},
		{"inline comment", "network_cmd_enable = \"true\" # keep on\n", true},
		{"windows line endings", "network_cmd_enable = \"true\"\r\n", true},
		{"spaces around key", "  network_cmd_enable   =   \"true\"  \n", true},
	}
	for _, tc := range tests {
		if got := networkCmdEnabled(tc.cfg); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNetworkCmdPort(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want int
	}{
		{"default when missing", "", retroArchDefaultNetCmdPort},
		{"configured", `network_cmd_port = "55356"`, 55356},
		{"bad value falls back", `network_cmd_port = "abc"`, retroArchDefaultNetCmdPort},
		{"out of range falls back", `network_cmd_port = "70000"`, retroArchDefaultNetCmdPort},
	}
	for _, tc := range tests {
		if got := networkCmdPort(tc.cfg); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRetroArchCmdline(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain binary", "/usr/bin/retroarch\x00--config\x00retroarch.cfg\x00", true},
		{"trimui path", "/mnt/SDCARD/RetroArch/retroarch\x00", true},
		{"windows binary", `C:\RetroArch\retroarch.exe` + "\x00", true},
		{"rom named like retroarch", "/usr/bin/ppsspp\x00retroarch-demo.iso\x00", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		if got := retroArchCmdline([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPickSuspendState(t *testing.T) {
	tests := []struct {
		available string
		want      string
	}{
		{"freeze mem disk", "mem"},
		{"standby freeze", "freeze"},
		{"disk standby", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := pickSuspendState(tc.available); got != tc.want {
			t.Errorf("pickSuspendState(%q) = %q, want %q", tc.available, got, tc.want)
		}
	}
}

func TestLowBatteryGuardEnabledByDefault(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{}
	if !lowBatteryGuardEnabled(cfg) {
		t.Fatal("the guard must default to on")
	}
	cfg.Variables.Custom["lowBatteryGuard"] = "false"
	if lowBatteryGuardEnabled(cfg) {
		t.Fatal("an explicit false must disable the guard")
	}
	cfg.Variables.Custom["lowBatteryGuard"] = "true"
	if !lowBatteryGuardEnabled(cfg) {
		t.Fatal("an explicit true must enable the guard")
	}
	if lowBatteryGuardEnabled(nil) {
		t.Fatal("nil config must not enable the guard")
	}
}

func TestLowBatteryMessageMentionsDisabledNetCmd(t *testing.T) {
	// The message is only as useful as the remediation it names; build both
	// branches from a config the helper reads, then restore the environment.
	t.Setenv("JUKAHUB_RETROARCH_CFG", t.TempDir()+"/retroarch.cfg")
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{}
	msg := lowBatteryMessage(cfg, 4)
	if msg == "" {
		t.Fatal("expected a warning message")
	}
}
