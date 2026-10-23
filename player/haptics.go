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
	hapticLastAt time.Time

	// openPadHaptics is a variable so tests can inject a fake backend (or a
	// temp-directory motor) without touching the detector.
	openPadHaptics = detectPadHaptics
)

// Feedback is on until the saved preference says otherwise: a splash sound can
// fire before hapticsBind runs and should not be silent.
func init() { hapticOn.Store(true) }

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

// hapticsBind applies the saved preference and opens the motor once.
func hapticsBind(config *Config) {
	hapticOn.Store(hapticsConfiguredOn(config))
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
