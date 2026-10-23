package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeHaptics records what the UI asked the motor to do, so the feedback can be
// checked without a pad.
type fakeHaptics struct {
	pulses   []hapticPulse
	attempts int
	stops    int
	err      error
}

func (f *fakeHaptics) play(strength float32, ms uint32) error {
	f.attempts++
	if f.err != nil {
		return f.err
	}
	f.pulses = append(f.pulses, hapticPulse{strength: strength, ms: ms})
	return nil
}

func (f *fakeHaptics) stop() error {
	f.stops++
	return nil
}

// installFakeHaptics swaps in a backend and restores the real state
// afterwards. A nil device models hardware with no motor.
func installFakeHaptics(t *testing.T, dev hapticBackend) {
	t.Helper()
	hapticsMu.Lock()
	prevDev, prevTried := hapticDev, hapticTried
	hapticDev = dev
	hapticTried = true
	hapticLastAt = time.Time{}
	hapticsMu.Unlock()
	prevOn := hapticOn.Load()
	hapticOn.Store(true)
	t.Cleanup(func() {
		hapticsMu.Lock()
		hapticDev, hapticTried = prevDev, prevTried
		hapticLastAt = time.Time{}
		hapticsMu.Unlock()
		hapticOn.Store(prevOn)
	})
}

func hapticsTestConfig(custom map[string]interface{}) *Config {
	return &Config{Variables: Variables{Custom: custom}}
}

// New installs have no stored preference, and that has to mean "on" — the
// handheld ships with feedback enabled.
func TestHapticsPreferenceDefaultsOn(t *testing.T) {
	cases := []struct {
		name   string
		custom map[string]interface{}
		want   bool
	}{
		{"no map", nil, true},
		{"empty", map[string]interface{}{}, true},
		{"string true", map[string]interface{}{hapticsVariable: "true"}, true},
		{"string false", map[string]interface{}{hapticsVariable: "false"}, false},
		{"bool false", map[string]interface{}{hapticsVariable: false}, false},
		{"upper off", map[string]interface{}{hapticsVariable: "OFF"}, false},
		{"number zero", map[string]interface{}{hapticsVariable: float64(0)}, false},
		{"garbage", map[string]interface{}{hapticsVariable: "maybe"}, true},
	}
	for _, tc := range cases {
		if got := hapticsConfiguredOn(hapticsTestConfig(tc.custom)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if !hapticsConfiguredOn(nil) {
		t.Error("a nil config must not disable feedback")
	}
}

func TestHapticsPulseRespectsSetting(t *testing.T) {
	dev := &fakeHaptics{}
	installFakeHaptics(t, dev)

	off := hapticsTestConfig(map[string]interface{}{hapticsVariable: "false"})
	hapticsBind(off)
	playHaptic(hapticActivatePulse)
	if len(dev.pulses) != 0 {
		t.Fatalf("a disabled setting fired %d pulses", len(dev.pulses))
	}

	on := hapticsTestConfig(nil)
	hapticsBind(on)
	playHaptic(hapticActivatePulse)
	if len(dev.pulses) != 1 {
		t.Fatalf("pulses = %d, want 1", len(dev.pulses))
	}
	if got := dev.pulses[0]; got != hapticActivatePulse {
		t.Errorf("pulse = %+v, want %+v", got, hapticActivatePulse)
	}
}

// A held direction repeats navigation sounds; the motor must not turn that into
// one long buzz, but a real action arriving in the same window still fires.
func TestHapticsNavIsCoalesced(t *testing.T) {
	dev := &fakeHaptics{}
	installFakeHaptics(t, dev)
	hapticsBind(nil)

	playHaptic(hapticNavPulse)
	playHaptic(hapticNavPulse)
	if len(dev.pulses) != 1 {
		t.Fatalf("nav pulses = %d, want 1 inside the gap", len(dev.pulses))
	}

	// Non-navigation feedback is never dropped behind a navigation tick.
	playHaptic(hapticActivatePulse)
	if len(dev.pulses) != 2 {
		t.Fatalf("pulses = %d, want the activation too", len(dev.pulses))
	}

	// Once the gap has passed, the next tick lands.
	hapticsMu.Lock()
	hapticLastAt = time.Now().Add(-2 * hapticNavGap)
	hapticsMu.Unlock()
	playHaptic(hapticNavPulse)
	if len(dev.pulses) != 3 {
		t.Fatalf("pulses = %d, want the tick after the gap", len(dev.pulses))
	}
}

func TestHapticsDisableStopsRunningMotor(t *testing.T) {
	dev := &fakeHaptics{}
	installFakeHaptics(t, dev)
	hapticsBind(nil)

	playHaptic(hapticErrorPulse)
	hapticsSetEnabled(false)
	if dev.stops != 1 {
		t.Fatalf("stops = %d, want the switch to cut the running pulse", dev.stops)
	}
	playHaptic(hapticErrorPulse)
	if len(dev.pulses) != 1 {
		t.Fatalf("pulses = %d, want nothing after switching off", len(dev.pulses))
	}

	hapticsSetEnabled(true)
	playHaptic(hapticTogglePulse)
	if len(dev.pulses) != 2 {
		t.Fatalf("pulses = %d, want feedback back on", len(dev.pulses))
	}
}

// A pad can be unplugged mid-session; the failure must be logged once and then
// left alone instead of being retried on every key press.
func TestHapticsFailureDisablesItself(t *testing.T) {
	dev := &fakeHaptics{err: errHapticsGone{}}
	installFakeHaptics(t, dev)
	hapticsBind(nil)

	playHaptic(hapticNavPulse)
	playHaptic(hapticActivatePulse)
	if dev.attempts != 1 {
		t.Fatalf("attempts = %d, want no retry after the motor failed", dev.attempts)
	}
	if len(dev.pulses) != 0 {
		t.Fatalf("pulses = %d, want none from a failing motor", len(dev.pulses))
	}
}

type errHapticsGone struct{}

func (errHapticsGone) Error() string { return "pad disconnected" }

// With no motor every pulse is a no-op; nothing may panic or block.
func TestHapticsWithoutMotorIsSilent(t *testing.T) {
	installFakeHaptics(t, nil)
	hapticsBind(nil)
	playHaptic(hapticNavPulse)
	playHaptic(hapticErrorPulse)
	hapticsSetEnabled(false)
	hapticsSetEnabled(true)
}

// The switch is injected only when the pad answered, lands in the free slot of
// Settings → General, and running the injection twice must not duplicate it.
func TestEnsureHapticsSetting(t *testing.T) {
	dev := &fakeHaptics{}
	installFakeHaptics(t, dev)

	// Both shipped configs, with the other runtime injections applied, so the
	// row is checked against the General screen the user actually sees.
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		t.Run(path, func(t *testing.T) {
			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			ensureSettingsWidgets(cfg)
			ensureOnboardingEntry(cfg)
			ensureHapticsSetting(cfg)
			ensureHapticsSetting(cfg)

			scene := settingsGeneralScene(cfg)
			if scene == nil {
				t.Fatal("no SettingsGeneral scene")
			}
			var found []Element
			for _, el := range scene.Elements {
				if el.Variable == hapticsVariable {
					found = append(found, el)
				}
			}
			if len(found) != 1 {
				t.Fatalf("Vibration rows = %d, want exactly 1", len(found))
			}
			row := found[0]
			if row.Type != "toggle" || row.Text != "Vibration" || row.TriggerValue != "true" {
				t.Errorf("row = %+v, want an on-by-default Vibration toggle", row)
			}
			w, h := resolveElementDimensions(row, 320, 48)
			if row.X < 24 || row.Y < 96 || row.X+w > int32(cfg.Width)-24 || row.Y+h > int32(cfg.Height)-64 {
				t.Errorf("row %+v is outside the content panel of the %dx%d canvas", row, cfg.Width, cfg.Height)
			}
			for _, el := range scene.Elements {
				if el.Variable == hapticsVariable {
					continue
				}
				ew, eh := resolveElementDimensions(el, 320, 48)
				overlapX := row.X < el.X+ew && el.X < row.X+w
				overlapY := row.Y < el.Y+eh && el.Y < row.Y+h
				if overlapX && overlapY && interactiveElement(el) {
					t.Errorf("Vibration switch %+v overlaps %q", row, el.Text)
				}
			}
		})
	}
}

// The switch must actually draw where it was injected, inside the content panel
// and above the footer, with its label visible.
func TestVibrationSwitchRenders(t *testing.T) {
	installFakeHaptics(t, &fakeHaptics{})
	config := widgetRenderConfig(t)
	ensureOnboardingEntry(config)
	ensureHapticsSetting(config)

	f, scene := renderNamedScene(t, config, "SettingsGeneral")
	var row *Element
	for i := range scene.Elements {
		if scene.Elements[i].Variable == hapticsVariable {
			row = &scene.Elements[i]
			break
		}
	}
	if row == nil {
		t.Fatal("no Vibration row in Settings → General")
	}
	// The content panel ends 16px above the 48px footer, so y+48 must stay
	// above 656.
	if row.Y+48 > 656 {
		t.Fatalf("Vibration row bottom at y=%d runs into the footer", row.Y+48)
	}
	if got := brightPixelsIn(f, *row, config); got < 40 {
		t.Errorf("Vibration row drew %d bright pixels: its label is missing", got)
	}
}

// Without a motor the setting is not offered at all: a switch that does nothing
// is worse than no switch.
func TestEnsureHapticsSettingHiddenWithoutMotor(t *testing.T) {
	installFakeHaptics(t, nil)
	cfg, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ensureSettingsWidgets(cfg)
	ensureHapticsSetting(cfg)
	for _, el := range settingsGeneralScene(cfg).Elements {
		if el.Variable == hapticsVariable {
			t.Fatal("Vibration switch was injected without a motor")
		}
	}
}

// The Settings switch applies immediately, including confirming the moment it
// is turned back on.
func TestHapticsToggleAppliesImmediately(t *testing.T) {
	dev := &fakeHaptics{}
	installFakeHaptics(t, dev)
	cfg := hapticsTestConfig(nil)
	hapticsBind(cfg)

	if widgetToggleSideEffect(cfg, hapticsVariable, false) {
		t.Fatal("the haptics switch must leave the standard On/Off toast in place")
	}
	if hapticOn.Load() {
		t.Fatal("switching Vibration off did not disable feedback")
	}
	dev.pulses = nil
	if widgetToggleSideEffect(cfg, hapticsVariable, true) {
		t.Fatal("the haptics switch must leave the standard On/Off toast in place")
	}
	if !hapticOn.Load() {
		t.Fatal("switching Vibration on did not enable feedback")
	}
	if len(dev.pulses) != 1 {
		t.Fatalf("pulses = %d, want one confirmation bump when enabling", len(dev.pulses))
	}
}

// ── TrimUI GPIO motor ─────────────────────────────────────────────────────

func writeMotorFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

func readMotorFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// waitForMotorValue polls a GPIO node until it reaches want, because the off
// edge is written by a timer rather than the caller.
func waitForMotorValue(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if readMotorFile(t, path) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("GPIO %s = %q, want %q", path, readMotorFile(t, path), want)
}

func TestTrimuiMotorPulseDrivesGPIO(t *testing.T) {
	dir := t.TempDir()
	gpio := writeMotorFile(t, dir, "gpio227/value", "0")
	voltage := writeMotorFile(t, dir, "motor/voltage", "3300000")
	motor := &sysfsMotor{gpio: gpio, levelPath: voltage, levelMin: trimuiMotorMinVoltage, levelMax: trimuiMotorMaxVoltage}

	if err := motor.play(0.5, 30); err != nil {
		t.Fatalf("play: %v", err)
	}
	if got := readMotorFile(t, gpio); got != "1" {
		t.Fatalf("GPIO right after the pulse = %q, want 1", got)
	}
	// 0.5 strength is halfway between the 0.5 V floor and 3.3 V ceiling.
	if got, _ := strconv.Atoi(readMotorFile(t, voltage)); got != 1900000 {
		t.Errorf("voltage = %d, want 1900000 microvolts", got)
	}
	waitForMotorValue(t, gpio, "0")
}

// Switching Vibration off while a pulse runs must cut it now, not after the
// timer expires.
func TestTrimuiMotorStopCutsPulse(t *testing.T) {
	dir := t.TempDir()
	gpio := writeMotorFile(t, dir, "gpio227/value", "0")
	motor := &sysfsMotor{gpio: gpio}

	if err := motor.play(1, 5000); err != nil {
		t.Fatalf("play: %v", err)
	}
	if got := readMotorFile(t, gpio); got != "1" {
		t.Fatalf("GPIO after play = %q, want 1", got)
	}
	if err := motor.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := readMotorFile(t, gpio); got != "0" {
		t.Fatalf("GPIO after stop = %q, want 0", got)
	}
	time.Sleep(40 * time.Millisecond)
	if got := readMotorFile(t, gpio); got != "0" {
		t.Fatalf("GPIO later = %q, want it to stay off", got)
	}
}

// A firmware that refuses strength writes must still buzz at its default level;
// losing the motor over a cosmetic setting would be worse than a fixed level.
func TestTrimuiMotorSurvivesUnwritableLevel(t *testing.T) {
	dir := t.TempDir()
	gpio := writeMotorFile(t, dir, "gpio227/value", "0")
	motor := &sysfsMotor{gpio: gpio, levelPath: filepath.Join(dir, "motor", "missing"),
		levelMin: trimuiMotorMinVoltage, levelMax: trimuiMotorMaxVoltage}

	if err := motor.play(0.5, 20); err != nil {
		t.Fatalf("play: %v", err)
	}
	if got := readMotorFile(t, gpio); got != "1" {
		t.Fatalf("GPIO = %q, want the pulse despite the level node", got)
	}
	if !motor.levelBlocked {
		t.Error("the failed level write was not remembered")
	}
	waitForMotorValue(t, gpio, "0")
}

// The Smart Pro S exposes a 16-bit level node instead of a voltage node.
func TestTrimuiMotorLevelNodeScalesTo16Bit(t *testing.T) {
	dir := t.TempDir()
	gpio := writeMotorFile(t, dir, "gpio236/value", "0")
	level := writeMotorFile(t, dir, "motor/level", "0")
	motor := &sysfsMotor{gpio: gpio, levelPath: level, levelMin: 0, levelMax: 65535}

	if err := motor.play(1, 5); err != nil {
		t.Fatalf("play: %v", err)
	}
	if got, _ := strconv.Atoi(readMotorFile(t, level)); got != 65535 {
		t.Errorf("level = %d, want full scale 65535", got)
	}
	waitForMotorValue(t, gpio, "0")
}

func TestDetectSysfsMotor(t *testing.T) {
	dir := t.TempDir()
	writeMotorFile(t, dir, "sys/class/motor/voltage", "3300000")
	gpio := writeMotorFile(t, dir, "sys/class/gpio/gpio227/value", "0")

	motor := detectSysfsMotor(dir)
	if motor == nil {
		t.Fatal("the TG5040 layout was not detected")
	}
	if motor.gpio != gpio || motor.levelMax != trimuiMotorMaxVoltage {
		t.Errorf("motor = %+v, want the gpio227/voltage layout", motor)
	}

	// A motor node without its GPIO is not a usable motor.
	bare := t.TempDir()
	writeMotorFile(t, bare, "sys/class/motor/voltage", "3300000")
	if detectSysfsMotor(bare) != nil {
		t.Fatal("a motor without an exported GPIO must not be detected")
	}
}

// The Brick Pro cap only applies to that model, and only to the voltage-style
// node.
func TestTrimuiMotorBrickProCap(t *testing.T) {
	motor := &sysfsMotor{levelMax: trimuiMotorMaxVoltage}
	capMotorForDevice(motor, DeviceTrimuiBrickPro)
	if motor.levelMax != trimuiBrickProVolt {
		t.Fatalf("Brick Pro cap = %d, want %d", motor.levelMax, trimuiBrickProVolt)
	}

	pro := &sysfsMotor{levelMax: trimuiMotorMaxVoltage}
	capMotorForDevice(pro, DeviceTrimuiSmartPro)
	if pro.levelMax != trimuiMotorMaxVoltage {
		t.Fatalf("Smart Pro ceiling = %d, want %d", pro.levelMax, trimuiMotorMaxVoltage)
	}

	level := &sysfsMotor{levelMax: 65535}
	capMotorForDevice(level, DeviceTrimuiBrickPro)
	if level.levelMax != 65535 {
		t.Fatalf("level node ceiling = %d, want it left alone", level.levelMax)
	}
}

// The full path from the setting to the GPIO: disabled means the motor node is
// never touched.
func TestTrimuiMotorRespectsSetting(t *testing.T) {
	dir := t.TempDir()
	gpio := writeMotorFile(t, dir, "gpio227/value", "0")
	voltage := writeMotorFile(t, dir, "motor/voltage", "3300000")
	motor := &sysfsMotor{gpio: gpio, levelPath: voltage, levelMin: trimuiMotorMinVoltage, levelMax: trimuiMotorMaxVoltage}
	installFakeHaptics(t, motor)

	hapticsBind(hapticsTestConfig(map[string]interface{}{hapticsVariable: "false"}))
	playHaptic(hapticActivatePulse)
	if got := readMotorFile(t, gpio); got != "0" {
		t.Fatalf("GPIO = %q, want it untouched while vibration is off", got)
	}

	hapticsBind(hapticsTestConfig(nil))
	playHaptic(hapticActivatePulse)
	if got := readMotorFile(t, gpio); got != "1" {
		t.Fatalf("GPIO = %q, want the pulse once feedback is on", got)
	}
	waitForMotorValue(t, gpio, "0")
}
