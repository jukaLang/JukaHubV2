package main

// Tests for the low-battery guard. The decisions are pure, so the risky part —
// when to auto-save and suspend — is verified without a handheld.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

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

// The levels Settings → Power picks (batterywarn.go) move the same decision.
func TestBatteryGuardActionAtLevels(t *testing.T) {
	tests := []struct {
		name          string
		percent       int
		warn, save    int
		charging      bool
		gameRunning   bool
		alreadyWarned bool
		want          batteryGuardAction
	}{
		{"warn at a picked 20%", 20, 20, 3, false, false, false, batteryGuardWarn},
		{"above a picked 20%", 21, 20, 3, false, false, false, batteryGuardIdle},
		{"save at a picked 8%", 8, 5, 8, false, true, false, batteryGuardProtect},
		{"save level above the warning, no game: quiet", 8, 5, 8, false, false, false, batteryGuardIdle},
		{"still never while charging", 2, 20, 10, true, true, false, batteryGuardIdle},
	}
	for _, tc := range tests {
		got := batteryGuardActionAt(tc.percent, tc.warn, tc.save, tc.charging, tc.gameRunning, false, tc.alreadyWarned)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// Auto is the old levels exactly.
	auto := batteryWarnLevels{}
	if auto.messagePct() != lowBatteryWarnPct || auto.savePct() != criticalBatteryPct || auto.ledPct() != ledIndLowBatteryPct {
		t.Fatalf("Auto moved a level: %d %d %d", auto.messagePct(), auto.savePct(), auto.ledPct())
	}
	if !auto.headerRed(14) || auto.headerRed(15) || !auto.headerBlinks(4) || auto.headerBlinks(5) {
		t.Fatal("Auto moved the header's red or blink")
	}
	if picked := (batteryWarnLevels{warn: 10}); picked.blinkPct() != 5 || !picked.headerRed(10) || picked.headerRed(11) {
		t.Fatalf("a picked 10%%: blink %d", picked.blinkPct())
	}
}

// TestUniversalResumePlan is instantresume.go's whole plan as a table: what
// the battery's save level, the sleep's power off and Power off do to each
// family of game. A state is never traded away, a game nothing can save is
// held at the battery rather than closed, and nothing is held where the
// device cannot wake itself to power off.
func TestUniversalResumePlan(t *testing.T) {
	// The families, as the detection reads them.
	families := []struct {
		name                                 string
		class                                sessionSafeClass
		saveOnQuit, onRequest, raReady, game bool
		want                                 resumeFamily
	}{
		{"nothing running", sessionSafeNone, false, false, true, true, resumeFamilyNone},
		{"RetroArch", sessionSafeRetroArch, false, false, true, true, resumeFamilyRetroArch},
		{"RetroArch without network commands", sessionSafeRetroArch, false, false, false, true, resumeFamilyRetroArchOff},
		{"JukaHub's PPSSPP build", sessionSafeOnQuit, true, true, true, true, resumeFamilyOnRequest},
		{"DraStic with its auto save state", sessionSafeOnQuit, true, false, true, true, resumeFamilyOnQuit},
		{"PPSSPP without save on exit", sessionSafeOnQuit, false, false, true, true, resumeFamilyHold},
		{"a port, Wine, hakuX, Android", sessionSafeQuitOnly, false, false, true, true, resumeFamilyHold},
		{"Moonlight", sessionSafeStream, false, false, true, true, resumeFamilyStream},
		{"an app", sessionSafeQuitOnly, false, false, true, false, resumeFamilyApp},
	}
	for _, f := range families {
		if got := resumeFamilyOf(f.class, f.saveOnQuit, f.onRequest, f.raReady, f.game); got != f.want {
			t.Errorf("%s: family %v, want %v", f.name, got, f.want)
		}
	}

	type cell struct {
		cause   resumeCause
		fam     resumeFamily
		percent int
		canHold bool
		want    resumeAction
	}
	for _, c := range []cell{
		// The battery's save level.
		{resumeCauseBattery, resumeFamilyRetroArch, 3, true, resumeSave},
		{resumeCauseBattery, resumeFamilyOnRequest, 3, true, resumeSave},
		{resumeCauseBattery, resumeFamilyOnQuit, 3, true, resumeSave},
		{resumeCauseBattery, resumeFamilyHold, 3, true, resumeHold},
		{resumeCauseBattery, resumeFamilyHold, 3, false, resumeClose},
		{resumeCauseBattery, resumeFamilyRetroArchOff, 3, true, resumeHold},
		{resumeCauseBattery, resumeFamilyRetroArchOff, 3, false, resumeLeave},
		{resumeCauseBattery, resumeFamilyStream, 3, true, resumeClose},
		{resumeCauseBattery, resumeFamilyApp, 3, true, resumeLeave},
		{resumeCauseBattery, resumeFamilyNone, 3, true, resumeLeave},
		// The sleep's power off: nothing that cannot save is closed before
		// the battery's last 5%.
		{resumeCauseSleepPowerOff, resumeFamilyOnRequest, 40, false, resumeSave},
		{resumeCauseSleepPowerOff, resumeFamilyHold, 40, false, resumeSleepAgain},
		{resumeCauseSleepPowerOff, resumeFamilyHold, 5, false, resumeClose},
		{resumeCauseSleepPowerOff, resumeFamilyHold, -1, false, resumeSleepAgain},
		{resumeCauseSleepPowerOff, resumeFamilyApp, 4, false, resumeClose},
		{resumeCauseSleepPowerOff, resumeFamilyRetroArchOff, 2, false, resumeSleepAgain},
		{resumeCauseSleepPowerOff, resumeFamilyStream, 80, false, resumeClose},
		// Power off: saved where it can be, asked to quit otherwise.
		{resumeCausePowerOff, resumeFamilyOnQuit, 50, false, resumeSave},
		{resumeCausePowerOff, resumeFamilyHold, 50, true, resumeClose},
		{resumeCausePowerOff, resumeFamilyApp, 50, false, resumeClose},
	} {
		if got := resumeActionFor(c.cause, c.fam, c.percent, c.canHold); got != c.want {
			t.Errorf("cause %v, family %v at %d%% (hold %t): %v, want %v", c.cause, c.fam, c.percent, c.canHold, got, c.want)
		}
	}

	// The sleep's power off keeps its answers through the plan.
	for _, d := range []struct {
		percent    int
		class      sessionSafeClass
		saveOnQuit bool
		want       sessionSafeDeadlineAction
	}{
		{50, sessionSafeOnQuit, true, sessionSafeDeadlineSave},
		{50, sessionSafeQuitOnly, false, sessionSafeDeadlineSleep},
		{4, sessionSafeQuitOnly, false, sessionSafeDeadlineQuit},
		{50, sessionSafeStream, false, sessionSafeDeadlineQuit},
		{2, sessionSafeRetroArch, false, sessionSafeDeadlineSleep},
	} {
		if got := sessionSafeDeadlineActionFor(d.percent, d.class, d.saveOnQuit); got != d.want {
			t.Errorf("deadline at %d%%, class %v, save %t: %v, want %v", d.percent, d.class, d.saveOnQuit, got, d.want)
		}
	}

	// The hold's sleep powers off within ten minutes at the guard's 5%.
	if got := saveLedgerCapFor(lowBatteryWarnPct, false, true); got != 10*time.Minute {
		t.Errorf("sleep cap at %d%%: %s", lowBatteryWarnPct, got)
	}
	if got := saveLedgerCapFor(12, false, true); got != 30*time.Minute {
		t.Errorf("sleep cap at 12%%: %s", got)
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

func TestRetroArchCmdline(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain binary", "/usr/bin/retroarch\x00--config\x00retroarch.cfg\x00", true},
		{"trimui path", "/mnt/SDCARD/RetroArch/retroarch\x00", true},
		{"windows binary", `C:\RetroArch\retroarch.exe` + "\x00", true},
		{"trimui stock build", "/mnt/SDCARD/RetroArch/ra64.trimui\x00-v\x00-L\x00core.so\x00", true},
		{"rom named like the trimui build", "/usr/bin/ppsspp\x00ra64.trimui.iso\x00", false},
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
	// The kernel's mem_sleep line: the bracketed mode is what "mem" means
	// (quickresume_linux.go reports "s2idle" as the light sleep it is).
	for line, want := range map[string]string{"s2idle [deep]": "deep", "[s2idle]": "s2idle", "s2idle deep\n": "", "": ""} {
		if got := quickMemSleepMode(line); got != want {
			t.Errorf("quickMemSleepMode(%q) = %q, want %q", line, got, want)
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

// The in-game overlay's RetroArch pause (gamepause_ra.go) reads GET_STATUS
// answers and picks the audio driver's failures out of RetroArch's log.
func TestRetroArchPauseParsing(t *testing.T) {
	for reply, want := range map[string]string{
		"GET_STATUS PLAYING nes,Super Mario Bros,crc32=3337ec46": "PLAYING",
		"GET_STATUS PAUSED snes,Zelda,crc32=1":                   "PAUSED",
		"get_status contentless":                                 "CONTENTLESS",
		"LOAD_STATE_SLOT 1":                                      "",
		"GET_STATUS":                                             "",
		"":                                                       "",
	} {
		if got := raStatusWord(reply); got != want {
			t.Errorf("raStatusWord(%q) = %q, want %q", reply, got, want)
		}
	}
	log := "[INFO] [Core]: Paused.\n[ERROR] [ALSA]: (#2) Failed to recover from error (Broken pipe)\r\n" +
		"[INFO] [Video]: Found display server.\n[ERROR] [Audio]: Audio backend failed to write. Will continue without sound.\n"
	got := raAudioTrouble(log)
	if len(got) != 2 || !strings.Contains(got[0], "ALSA") || !strings.Contains(got[1], "without sound") {
		t.Fatalf("raAudioTrouble = %q", got)
	}
	if got := raAudioTrouble("[INFO] [Core]: Unpaused.\n"); len(got) != 0 {
		t.Fatalf("a quiet log reported %q", got)
	}
}

// TestBatteryCalParseAndMapping covers battery calibration's readings file (a
// damaged line and a torn last line are skipped) and the mapping learned from
// it: a gauge that reads 14% when the battery gives out maps that reading to
// about 0, the table never falls, and full stays full.
func TestBatteryCalParseAndMapping(t *testing.T) {
	const n = 200
	var b strings.Builder
	for i := 0; i < n; i++ {
		truth := 100 * float64(n-1-i) / float64(n-1)
		pct := int(14 + truth*0.86 + 0.5)
		b.WriteString(`{"t":` + strconv.Itoa(1700000000+60*i) + `,"a":` + strconv.Itoa(60*i) +
			`,"p":` + strconv.Itoa(pct) + `,"b":1}` + "\n")
		if i == 50 {
			b.WriteString("not json\n")
		}
	}
	b.WriteString(`{"t":1700012000,"a":`)
	samples := parseBatteryCalSamples([]byte(b.String()))
	if len(samples) != n {
		t.Fatalf("parsed %d readings, want %d", len(samples), n)
	}
	r, reason := batteryCalCompute(samples)
	if reason != "" {
		t.Fatalf("compute refused: %s", reason)
	}
	if r.EndPct != 14 || r.MaxErr <= batteryCalTolerance {
		t.Fatalf("end %d%%, max error %d: want 14%% and a correction", r.EndPct, r.MaxErr)
	}
	for g := 1; g < len(r.Table); g++ {
		if r.Table[g] < r.Table[g-1] {
			t.Fatalf("table falls at %d: %v", 5*g, r.Table)
		}
	}
	if r.Table[batteryCalGrid-1] != 100 {
		t.Fatalf("full maps to %d", r.Table[batteryCalGrid-1])
	}
	if got := batteryCalApply(r.Table, 14); got > 2 {
		t.Fatalf("the shutdown reading 14%% maps to %d", got)
	}
	if got := batteryCalApply(r.Table, 57); got < 46 || got > 54 {
		t.Fatalf("57%% maps to %d, want about 50", got)
	}
}
