package main

// haptics.go — short vibration feedback for the interface.
//
// Two kinds of motor are supported, because they are not the same thing:
//
//   * The TrimUI handhelds wire their vibration motor to a vendor GPIO, not to
//     the gamepad's force-feedback node. NextUI drives the TG5040 family with
//     /sys/class/gpio/gpio227/value plus /sys/class/motor/voltage, and the
//     TG5050 with gpio236 plus /sys/class/motor/level. A pulse there is "switch
//     the GPIO on, wait, switch it off", run on a timer so the UI never waits.
//   * Any other pad with rumble (a USB or Bluetooth controller on a desktop)
//     answers SDL's controller-rumble API instead.
//
// Pulses mirror the UI beeps: the Play*Sound helpers in sound.go are the one
// place every navigation, confirm, cancel, toggle and toast already goes
// through, so the vibration rides there instead of at hundreds of call sites.
//
// Settings → General gets a Vibration switch only when a motor was found. A
// switch that cannot do anything is worse than no switch, which is why the
// shipped configs are left alone and the row is injected only on hardware that
// can actually buzz.

import (
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// hapticsVariable is the custom setting the Settings switch writes. It is
	// absent on a fresh install, which means "on".
	hapticsVariable = "hapticsEnabled"

	// hapticsIntensityVariable is how hard those pulses are. It is a separate
	// setting from the switch on purpose: the switch answers "buzz or not",
	// this answers "how much", which is the question someone asks at night.
	hapticsIntensityVariable = "hapticsIntensity"

	// hapticNavGap stops a held direction from turning the motor into one
	// continuous buzz. It is shorter than the d-pad repeat interval, so every
	// deliberate step still gets its own tick.
	hapticNavGap = 60 * time.Millisecond
)

// hapticPulse is one burst: strength is 0..1, ms is the motor-on time.
//
// The durations are a little longer than a beep on purpose: a coin motor takes
// a few tens of milliseconds to spin up, so a 10 ms pulse is felt as nothing at
// all on the Smart Pro.
type hapticPulse struct {
	strength float32
	ms       uint32
}

// The pulses are deliberately distinct so the device can be used without
// looking at it: a tick when the focus moves, a firmer bump when something
// opens, a longer jolt on an error.
var (
	hapticNavPulse      = hapticPulse{strength: 0.40, ms: 26}
	hapticActivatePulse = hapticPulse{strength: 0.70, ms: 45}
	hapticBackPulse     = hapticPulse{strength: 0.50, ms: 30}
	hapticTogglePulse   = hapticPulse{strength: 0.60, ms: 35}
	hapticSuccessPulse  = hapticPulse{strength: 0.80, ms: 60}
	hapticErrorPulse    = hapticPulse{strength: 1.00, ms: 150}
)

// hapticLevel is one step of that setting. The scale multiplies every pulse's
// strength, so one choice covers navigation ticks, confirmations and errors
// instead of the UI developing a second table of pulses per level.
//
// The pulse length is deliberately left alone. A coin motor needs tens of
// milliseconds to spin up, so shortening a pulse would make "soft" feel
// unreliable rather than gentle; the drive level is the honest knob.
type hapticLevel struct {
	key   string
	label string
	scale float32
}

// hapticLevels is the cycle order of the row: softest first, so holding right
// walks the same way as every other setting on that panel.
var hapticLevels = []hapticLevel{
	{key: "soft", label: "Soft", scale: 0.55},
	{key: "normal", label: "Normal", scale: 1},
	{key: "strong", label: "Strong", scale: 1.35},
}

// hapticLevelDefault is Normal: an install that never touches the row behaves
// exactly as it did before the setting existed.
const hapticLevelDefault = 1

// hapticBackend is one motor. It is an interface so tests can record pulses
// without hardware and so a pad and a GPIO can share the same call site.
type hapticBackend interface {
	play(strength float32, ms uint32) error
	stop() error
}

// ── TrimUI GPIO motor ─────────────────────────────────────────────────────

// TrimUI motor nodes. The TG5040 family (Smart Pro, Brick, Brick Pro) shares
// the first pair; the TG5050 (Smart Pro S) moves to GPIO 236 and a 16-bit
// level node. These are vendor nodes, not evdev force feedback, so they are
// written directly.
const (
	trimuiMotorGPIO5040 = "sys/class/gpio/gpio227/value"
	trimuiMotorVoltage  = "sys/class/motor/voltage"
	trimuiMotorGPIO5050 = "sys/class/gpio/gpio236/value"
	trimuiMotorLevel    = "sys/class/motor/level"

	// Voltages are microvolts. NextUI caps the Brick Pro at 2.5 V because its
	// motor rattles the shell at 3.3 V; everything else runs at full scale.
	trimuiMotorMinVoltage = 500000
	trimuiMotorMaxVoltage = 3300000
	trimuiBrickProVolt    = 2500000
)

// sysfsMotor drives one of those binary GPIO motors. The motor has no duration
// register, so a pulse is a timer that switches the GPIO back off.
type sysfsMotor struct {
	gpio      string
	levelPath string
	levelMax  int // 65535 for level nodes; max microvolts for voltage nodes
	levelMin  int

	mu    sync.Mutex
	on    bool
	gen   uint64
	timer *time.Timer
	// levelBlocked is set when the firmware refuses strength writes. The GPIO
	// still works at its default voltage, so the pulse continues rather than
	// losing feedback over a cosmetic setting.
	levelBlocked bool
}

func (m *sysfsMotor) play(strength float32, ms uint32) error {
	if ms == 0 {
		return nil
	}
	if strength < 0 {
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}
	if m.levelPath != "" && !m.levelBlocked {
		level := m.levelMin + int(math.Round(float64(m.levelMax-m.levelMin)*float64(strength)))
		if err := writeSysfsInt(m.levelPath, level); err != nil {
			m.levelBlocked = true
			log.Printf("[HAPTIC] motor strength node is not writable, using its default level: %v", err)
		}
	}
	if err := writeSysfsInt(m.gpio, 1); err != nil {
		return err
	}
	m.mu.Lock()
	m.gen++
	gen := m.gen
	m.on = true
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = time.AfterFunc(time.Duration(ms)*time.Millisecond, func() { m.offGeneration(gen) })
	m.mu.Unlock()
	return nil
}

// offGeneration is the timer's edge. The generation check makes a timer that
// was already firing when a new pulse started a no-op instead of cutting the
// new pulse short.
func (m *sysfsMotor) offGeneration(gen uint64) {
	m.mu.Lock()
	if !m.on || m.gen != gen {
		m.mu.Unlock()
		return
	}
	m.on = false
	m.timer = nil
	m.mu.Unlock()
	_ = writeSysfsInt(m.gpio, 0)
}

// stop cuts the motor now. It is safe to call when nothing is running.
func (m *sysfsMotor) stop() error {
	m.mu.Lock()
	if !m.on {
		m.mu.Unlock()
		return nil
	}
	m.on = false
	m.gen++
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.mu.Unlock()
	return writeSysfsInt(m.gpio, 0)
}

func writeSysfsInt(path string, value int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(value)), 0o644)
}

// detectSysfsMotor looks for a TrimUI motor under root ("/" on the device, a
// temp directory in tests). The vendor motor node is checked first,
// so the GPIO export below can only ever happen on hardware that really has
// that motor — never on an unrelated Linux machine where GPIO 227 belongs to
// something else.
func detectSysfsMotor(root string) *sysfsMotor {
	type layout struct {
		gpio      string
		level     string
		levelMax  int
		levelMin  int
		exportPin string
	}
	layouts := []layout{
		{gpio: trimuiMotorGPIO5040, level: trimuiMotorVoltage, levelMin: trimuiMotorMinVoltage,
			levelMax: trimuiMotorMaxVoltage, exportPin: "227"},
		{gpio: trimuiMotorGPIO5050, level: trimuiMotorLevel, levelMin: 0,
			levelMax: 65535, exportPin: "236"},
	}
	for _, l := range layouts {
		gpioPath := filepath.Join(root, l.gpio)
		levelPath := filepath.Join(root, l.level)
		if _, err := os.Stat(levelPath); err != nil {
			continue
		}
		if _, err := os.Stat(gpioPath); err != nil {
			export := filepath.Join(root, "sys/class/gpio/export")
			if err := os.WriteFile(export, []byte(l.exportPin), 0o644); err != nil {
				log.Printf("[HAPTIC] motor found but GPIO %s is not exported: %v", l.exportPin, err)
				continue
			}
			// The kernel creates the value node synchronously, but a slow
			// firmware may need a moment.
			deadline := time.Now().Add(150 * time.Millisecond)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(gpioPath); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(gpioPath); err != nil {
				continue
			}
		}
		// A previous run may have been killed mid-pulse, leaving the GPIO high.
		// Start from a known state instead of a buzzing handheld.
		_ = writeSysfsInt(gpioPath, 0)
		return &sysfsMotor{gpio: gpioPath, levelPath: levelPath, levelMin: l.levelMin, levelMax: l.levelMax}
	}
	return nil
}

// capMotorForDevice applies the per-model voltage ceiling. The Brick Pro's
// motor is bolted to a thin shell and rattles everything off a table at full
// scale, so it stops at 2.5 V.
func capMotorForDevice(m *sysfsMotor, device DeviceModel) {
	if m != nil && device == DeviceTrimuiBrickPro && m.levelMax == trimuiMotorMaxVoltage {
		m.levelMax = trimuiBrickProVolt
	}
}

// ── SDL controller rumble ─────────────────────────────────────────────────

// padHaptics drives a controller that exposes rumble through SDL.
type padHaptics struct {
	pad *sdl.GameController
}

func (p padHaptics) play(strength float32, ms uint32) error {
	if strength < 0 {
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}
	v := uint16(math.Round(float64(strength) * 65535))
	// Both channels get the same value, so a two-motor pad reads as one bump
	// instead of a lopsided one.
	return p.pad.Rumble(v, v, ms)
}

func (p padHaptics) stop() error { return p.pad.Rumble(0, 0, 0) }

var (
	hapticsMu    sync.Mutex
	hapticDev    hapticBackend
	hapticTried  bool
	hapticOn     atomic.Bool
	hapticStep   atomic.Int32
	hapticLastAt time.Time

	// openPadHaptics is a variable so tests can inject a fake backend (or a
	// temp-directory motor) without touching the detector.
	openPadHaptics = detectPadHaptics
)

// Feedback is on until the saved preference says otherwise: a splash sound can
// fire before hapticsBind runs and should not be silent.
func init() {
	hapticOn.Store(true)
	hapticStep.Store(hapticLevelDefault)
}

// hapticsLevelIndex maps a stored spelling to its step. An unknown or missing
// value is Normal, which is what a hand-edited config gets for a typo.
func hapticsLevelIndex(key string) int {
	want := strings.ToLower(strings.TrimSpace(key))
	for i, level := range hapticLevels {
		if level.key == want {
			return i
		}
	}
	return hapticLevelDefault
}

// hapticsConfiguredLevel reads the saved step. It accepts the same shapes the
// Vibration switch accepts, so a config written by hand or by the web builder
// behaves like the panel.
func hapticsConfiguredLevel(config *Config) int {
	if config == nil {
		return hapticLevelDefault
	}
	v, ok := customValue(config.Variables.Custom, hapticsIntensityVariable)
	if !ok {
		return hapticLevelDefault
	}
	switch t := v.(type) {
	case string:
		return hapticsLevelIndex(t)
	case float64:
		// A number is a step index, which is what a JSON number in the custom
		// map reads as.
		idx := int(math.Round(t))
		if idx >= 0 && idx < len(hapticLevels) {
			return idx
		}
	}
	return hapticLevelDefault
}

// hapticsLevelNow is the step in force. The event thread changes it and the
// pulse path reads it, so it lives in an atomic instead of behind the motor
// mutex.
func hapticsLevelNow() hapticLevel {
	idx := int(hapticStep.Load())
	if idx < 0 || idx >= len(hapticLevels) {
		return hapticLevels[hapticLevelDefault]
	}
	return hapticLevels[idx]
}

// hapticsSetLevel applies a step and records it beside the Vibration switch. It
// writes the value into the config in memory and leaves persisting to its
// caller, which is how every other row on the Accessibility panel behaves: the
// panel saves once per adjustment, through the user-config writer, and a design
// save is a different file with a different owner.
//
// A nil config applies the step for this session only.
func hapticsSetLevel(config *Config, idx int) hapticLevel {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(hapticLevels) {
		idx = len(hapticLevels) - 1
	}
	level := hapticLevels[idx]
	hapticStep.Store(int32(idx))
	if config == nil {
		return level
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	// Keep the key's casing as it is in the shipped config rather than adding a
	// second spelling the reader would then have to choose between.
	config.Variables.Custom[hapticsIntensityVariable] = level.key
	return level
}

// hapticsCycleLevel steps the setting by one in dir, wrapping at both ends so
// the row is a ring like the other cycling rows on the panel.
func hapticsCycleLevel(config *Config, dir int) hapticLevel {
	idx := int(hapticStep.Load())
	if idx < 0 || idx >= len(hapticLevels) {
		idx = hapticLevelDefault
	}
	step := 1
	if dir < 0 {
		step = -1
	}
	idx = (idx + step + len(hapticLevels)) % len(hapticLevels)
	return hapticsSetLevel(config, idx)
}

// scaleHaptic applies the current step to one pulse. Strength is the only part
// that changes, and it stays inside the 0..1 the backends clamp anyway, so a
// Strong setting on an already maximum error pulse is simply maximum.
func scaleHaptic(p hapticPulse) hapticPulse {
	scale := hapticsLevelNow().scale
	p.strength *= scale
	if p.strength > 1 {
		p.strength = 1
	}
	if p.strength < 0 {
		p.strength = 0
	}
	return p
}

// detectPadHaptics prefers the handheld's own motor and falls back to SDL
// rumble, which is what a USB or Bluetooth pad answers.
func detectPadHaptics() hapticBackend {
	device := DetectDevice()
	if runtime.GOOS == "linux" {
		if motor := detectSysfsMotor("/"); motor != nil {
			capMotorForDevice(motor, device)
			log.Printf("[HAPTIC] vibration ready on the %s motor (%s)", deviceDisplayName(device), motor.gpio)
			return motor
		}
	}
	if gameController == nil {
		log.Printf("[HAPTIC] no controller open and no handheld motor — vibration unavailable")
		return nil
	}
	name := gameController.Name()
	// HasRumble only answers meaningfully on SDL 2.0.18+; older libraries (some
	// handheld firmware ships one) report false even when the kernel exposes
	// force feedback. A zero-strength, 1 ms pulse is inaudible and is the real
	// test of whether the motor answers.
	hasRumble := gameController.HasRumble()
	if err := gameController.Rumble(0, 0, 1); err != nil {
		log.Printf("[HAPTIC] pad %q has no usable rumble motor (HasRumble=%v): %v — vibration hidden",
			name, hasRumble, err)
		return nil
	}
	log.Printf("[HAPTIC] vibration ready on %q (HasRumble=%v)", name, hasRumble)
	return padHaptics{pad: gameController}
}

// ── Preference ────────────────────────────────────────────────────────────

// hapticsConfiguredOn reads the saved preference. A missing or unparseable
// value means on: the device ships with feedback enabled.
func hapticsConfiguredOn(config *Config) bool {
	if config == nil {
		return true
	}
	v, ok := customValue(config.Variables.Custom, hapticsVariable)
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		// Accept the same words the toggle element does, so a hand-edited
		// config behaves like the switch.
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "on", "yes":
			return true
		case "off", "no":
			return false
		}
		b, err := strconv.ParseBool(strings.TrimSpace(strings.ToLower(t)))
		if err != nil {
			return true
		}
		return b
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return true
}

// hapticsBind applies the saved preferences and opens the motor once.
func hapticsBind(config *Config) {
	hapticOn.Store(hapticsConfiguredOn(config))
	hapticStep.Store(int32(hapticsConfiguredLevel(config)))
	hapticsMu.Lock()
	defer hapticsMu.Unlock()
	if !hapticTried {
		hapticTried = true
		hapticDev = openPadHaptics()
	}
}

// hapticsAvailable reports whether a motor answered, detecting lazily the first
// time so callers that run before hapticsBind still get a truthful answer.
func hapticsAvailable() bool {
	hapticsMu.Lock()
	defer hapticsMu.Unlock()
	if !hapticTried {
		hapticTried = true
		hapticDev = openPadHaptics()
	}
	return hapticDev != nil
}

// hapticsSetEnabled applies a change from Settings and cuts a pulse that is
// already running, so switching feedback off is felt immediately.
func hapticsSetEnabled(on bool) {
	hapticOn.Store(on)
	if on {
		return
	}
	hapticsMu.Lock()
	dev := hapticDev
	hapticsMu.Unlock()
	if dev != nil {
		_ = dev.stop()
	}
}

// playHaptic fires one pulse unless feedback is off, no motor answered, or the
// pulse is a navigation tick arriving faster than the gap allows.
func playHaptic(p hapticPulse) {
	if !hapticOn.Load() {
		return
	}
	hapticsMu.Lock()
	defer hapticsMu.Unlock()
	if !hapticTried {
		hapticTried = true
		hapticDev = openPadHaptics()
	}
	if hapticDev == nil {
		return
	}
	p = scaleHaptic(p)
	now := time.Now()
	if p.ms <= hapticNavPulse.ms && !hapticLastAt.IsZero() && now.Sub(hapticLastAt) < hapticNavGap {
		return
	}
	if err := hapticDev.play(p.strength, p.ms); err != nil {
		// A node can disappear after an update or a pad can be unplugged.
		// Stop trying instead of logging on every key press.
		log.Printf("[HAPTIC] vibration failed, feedback disabled until restart: %v", err)
		hapticDev = nil
		return
	}
	hapticLastAt = now
}

// ensureHapticsSetting adds the Vibration switch to Settings → General when a
// motor was found. It is injected in memory like the Smart Widgets page, so no
// shipped config changes and configs without the row still get the switch.
//
// The slot is the empty bottom-left corner of General, under the Smart Widgets
// button; the right column there already carries the audio-backend labels and
// the Save button.
func ensureHapticsSetting(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil || !hapticsAvailable() {
		return
	}
	for _, el := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(el.Variable), hapticsVariable) {
			return
		}
	}
	scene.Elements = append(scene.Elements, Element{
		Type:         "toggle",
		Text:         "Vibration",
		Variable:     hapticsVariable,
		X:            40,
		Y:            592,
		Width:        "280",
		Height:       "48",
		TriggerValue: "true",
	})
}
