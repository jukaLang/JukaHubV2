package main

// ledsync.go — Ambilight-style LED sync driven by the audio the player is
// already sending to SDL.
//
// Audio side: both the video decoder and the music player hand stereo S16 PCM
// to QueueVideoAudio, and the UI beeps build their buffer in playTone. Both
// publish a tiny analysis (RMS amplitude plus a zero-crossing frequency
// estimate) into audioBus; the publish is a bounded loop over at most 2048
// frames, so the decoder never waits on the lights.
//
// Light side: /sys/class/leds is the only tree touched. Each LED is classified
// once by the files it exposes — multi_intensity (the kernel multicolor class),
// color (per-driver hex), or brightness (single channel). Only colour-capable
// LEDs and LEDs whose names look like stick/ring lights are used, so a power or
// keyboard LED is never pulsed. Values are quantised before writing and
// unchanged frames are skipped, because this loop runs 25 times a second on a
// handheld with an SD card for a root filesystem.
//
// This file deliberately imports no SDL so its logic can be tested on its own.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ledRootDir is the only directory the LED writer may touch.
	ledRootDir = "/sys/class/leds"
	// ledFrameInterval is the animation step. 25 fps is smooth for a glow and
	// keeps sysfs traffic low.
	ledFrameInterval = 40 * time.Millisecond
	// ledSilenceAfter is how long an audio sample stays valid. When playback
	// stops the lights fall dark instead of freezing on the last frame.
	ledSilenceAfter = 220 * time.Millisecond
	// ledAnalysisFrames caps how much PCM one publish inspects.
	ledAnalysisFrames = 2048
)

// ── Audio analysis ─────────────────────────────────────────────────────────

// audioSample is the latest slice of audio the player has produced.
type audioSample struct {
	RMS  float64 // 0..1, full scale = 1
	Freq float64 // rough dominant frequency in Hz, 0 when unknown
	At   time.Time
}

var audioBus struct {
	mu sync.Mutex
	s  audioSample
}

// publishAudioLevel analyses PCM (signed 16-bit little-endian, interleaved) and
// stores the result for the LED loop. It is called from the decoder and mixer
// paths, so it must stay cheap and must never block on the LED side.
func publishAudioLevel(pcm []byte, rate, channels int) {
	if len(pcm) < 4 || channels <= 0 {
		return
	}
	frames := len(pcm) / (2 * channels)
	if frames > ledAnalysisFrames {
		frames = ledAnalysisFrames
	}
	if frames <= 0 {
		return
	}
	s := audioSample{
		RMS:  rmsPCM16(pcm, frames, channels),
		Freq: zeroCrossHz(pcm, rate, channels),
		At:   time.Now(),
	}
	audioBus.mu.Lock()
	audioBus.s = s
	audioBus.mu.Unlock()
}

func currentAudioSample() audioSample {
	audioBus.mu.Lock()
	defer audioBus.mu.Unlock()
	return audioBus.s
}

// rmsPCM16 returns the root-mean-square level of interleaved S16 samples in the
// range 0..1. Pure.
func rmsPCM16(pcm []byte, frames, channels int) float64 {
	if frames <= 0 || channels <= 0 {
		return 0
	}
	samples := frames * channels
	if samples*2 > len(pcm) {
		samples = len(pcm) / 2
	}
	if samples <= 0 {
		return 0
	}
	var sum float64
	for i := 0; i < samples; i++ {
		v := int16(uint16(pcm[i*2]) | uint16(pcm[i*2+1])<<8)
		f := float64(v) / 32768.0
		sum += f * f
	}
	return math.Sqrt(sum / float64(samples))
}

// zeroCrossHz estimates the dominant frequency from the zero-crossing rate of
// the first channel. It is coarse on purpose: the lights need a colour band,
// not a spectrum, and this costs one pass over the buffer. Pure.
func zeroCrossHz(pcm []byte, rate, channels int) float64 {
	if rate <= 0 || channels <= 0 {
		return 0
	}
	frames := len(pcm) / (2 * channels)
	if frames < 2 {
		return 0
	}
	crossings := 0
	var prev int16
	havePrev := false
	for i := 0; i < frames; i++ {
		off := i * 2 * channels
		if off+1 >= len(pcm) {
			break
		}
		s := int16(uint16(pcm[off]) | uint16(pcm[off+1])<<8)
		// Ignore tiny values so DAC noise around zero does not read as treble.
		if s > -64 && s < 64 {
			continue
		}
		if havePrev {
			if (prev < 0 && s >= 0) || (prev >= 0 && s < 0) {
				crossings++
			}
		}
		prev = s
		havePrev = true
	}
	return float64(crossings) * float64(rate) / (2 * float64(frames))
}

// levelFromRMS maps amplitude onto 0..1 with a noise gate and a log curve, so
// quiet passages do not flicker and loud ones saturate progressively. Pure.
func levelFromRMS(rms float64) float64 {
	const gate = 0.004 // about -48 dBFS
	if rms <= gate {
		return 0
	}
	if rms >= 0.5 {
		return 1
	}
	lvl := (math.Log10(rms) - math.Log10(gate)) / (math.Log10(0.5) - math.Log10(gate))
	if lvl < 0 {
		return 0
	}
	if lvl > 1 {
		return 1
	}
	return lvl
}

// hueForFrequency maps a band onto the colour wheel: bass runs red/orange, mids
// green/cyan, treble violet. Pure.
func hueForFrequency(hz float64) float64 {
	switch {
	case hz <= 0:
		return 0.58 // cool blue when the frequency is unknown
	case hz < 120:
		return 0.00 // red
	case hz < 400:
		return 0.08 // orange
	case hz < 1200:
		return 0.33 // green
	case hz < 3000:
		return 0.50 // cyan
	default:
		return 0.75 // violet
	}
}

// hsvToRGB converts hue (0..1), saturation and value (0..1) to 8-bit RGB. Pure.
func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	h = math.Mod(h, 1)
	if h < 0 {
		h += 1
	}
	i := int(math.Floor(h * 6))
	f := h*6 - float64(i)
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return uint8(math.Round(r * 255)), uint8(math.Round(g * 255)), uint8(math.Round(b * 255))
}

// ledColorFor turns one audio sample into the colour the LEDs should show. A
// silent sample is black, which the writer treats as "off". Pure.
func ledColorFor(level, hz float64) (uint8, uint8, uint8) {
	if level <= 0 {
		return 0, 0, 0
	}
	// Keep a floor of 25% so a quiet passage still shows its band colour.
	return hsvToRGB(hueForFrequency(hz), 1, 0.25+0.75*level)
}

// ── LED devices ────────────────────────────────────────────────────────────

type ledMode int

const (
	ledModeMultiIntensity ledMode = iota // /sys/class/leds/<dev>/multi_intensity
	ledModeColor                         // /sys/class/leds/<dev>/color
	ledModeBrightness                    // /sys/class/leds/<dev>/brightness only
)

type ledDevice struct {
	Name          string
	Dir           string
	Mode          ledMode
	Max           int
	HasBrightness bool
}

// addressableLEDName reports whether a brightness-only LED looks like a stick or
// ring light. Colour-capable LEDs are accepted regardless of name; a plain
// brightness LED is only pulsed when the device says it is decorative.
func addressableLEDName(name string) bool {
	n := strings.ToLower(name)
	for _, hint := range []string{"rgb", "joy", "stick", "stk", "ring", "gamepad", "analog"} {
		if strings.Contains(n, hint) {
			return true
		}
	}
	return false
}

// discoverLEDs lists the LEDs the sync may drive. Only direct children of root
// are considered, and every candidate must be writable by this process; a
// symlinked entry is skipped so the writer cannot be steered outside the tree.
func discoverLEDs(root string) []ledDevice {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	devs := make([]ledDevice, 0, len(names))
	for _, name := range names {
		dir := filepath.Join(root, name)
		dev, ok := classifyLED(dir, name)
		if !ok {
			continue
		}
		devs = append(devs, dev)
	}
	return devs
}

// classifyLED decides how one LED is written. Pure filesystem reads.
func classifyLED(dir, name string) (ledDevice, bool) {
	dev := ledDevice{Name: name, Dir: dir, Max: 255}
	dev.HasBrightness = canWriteFile(filepath.Join(dir, "brightness"))
	if v, ok := readLEDInt(filepath.Join(dir, "max_brightness")); ok && v > 0 {
		dev.Max = v
	}
	if canWriteFile(filepath.Join(dir, "multi_intensity")) {
		dev.Mode = ledModeMultiIntensity
		return dev, true
	}
	if canWriteFile(filepath.Join(dir, "color")) {
		dev.Mode = ledModeColor
		return dev, true
	}
	if dev.HasBrightness && addressableLEDName(name) {
		dev.Mode = ledModeBrightness
		return dev, true
	}
	return ledDevice{}, false
}

func readLEDInt(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return v, true
}

// applyLED writes one colour to one device. Brightness-mode devices show the
// brightest channel; colour-capable devices get the full RGB value. r==g==b==0
// means "off", which also clears brightness because some multicolor drivers keep
// showing the intensity while brightness is non-zero.
func applyLED(dev ledDevice, r, g, b uint8) error {
	off := r == 0 && g == 0 && b == 0
	switch dev.Mode {
	case ledModeMultiIntensity:
		if err := writeLEDFile(filepath.Join(dev.Dir, "multi_intensity"),
			fmt.Sprintf("%d %d %d", r, g, b)); err != nil {
			return err
		}
		if dev.HasBrightness {
			level := 0
			if !off {
				level = dev.Max
			}
			return writeLEDFile(filepath.Join(dev.Dir, "brightness"), strconv.Itoa(level))
		}
		return nil
	case ledModeColor:
		if err := writeLEDFile(filepath.Join(dev.Dir, "color"),
			fmt.Sprintf("#%02x%02x%02x", r, g, b)); err != nil {
			return err
		}
		if dev.HasBrightness {
			level := 0
			if !off {
				level = dev.Max
			}
			return writeLEDFile(filepath.Join(dev.Dir, "brightness"), strconv.Itoa(level))
		}
		return nil
	default:
		level := 0
		if !off {
			peak := r
			if g > peak {
				peak = g
			}
			if b > peak {
				peak = b
			}
			level = int(math.Round(float64(dev.Max) * float64(peak) / 255))
			if level < 1 {
				level = 1
			}
		}
		return writeLEDFile(filepath.Join(dev.Dir, "brightness"), strconv.Itoa(level))
	}
}

func writeLEDFile(path, value string) error {
	return os.WriteFile(path, []byte(value), 0o644)
}

// ledSyncAvailable reports whether this device has any LED the sync can drive.
func ledSyncAvailable() bool {
	return len(discoverLEDs(ledRootDir)) > 0
}

// ── Loop ───────────────────────────────────────────────────────────────────

var (
	ledSyncMu      sync.Mutex
	ledSyncStop    chan struct{}
	ledSyncDevices []ledDevice
)

// LEDSyncRunning reports whether the animation loop is active.
func LEDSyncRunning() bool {
	ledSyncMu.Lock()
	defer ledSyncMu.Unlock()
	return ledSyncStop != nil
}

// SetLEDSync starts or stops the animation. Starting fails when there is nothing
// to drive, which the caller surfaces instead of pretending it worked.
func SetLEDSync(on bool) error {
	ledSyncMu.Lock()
	defer ledSyncMu.Unlock()
	if !on {
		if ledSyncStop != nil {
			close(ledSyncStop)
			ledSyncStop = nil
			ledSyncDevices = nil
		}
		return nil
	}
	if ledSyncStop != nil {
		return nil
	}
	devs := discoverLEDs(ledRootDir)
	if len(devs) == 0 {
		return fmt.Errorf("no addressable LED under %s", ledRootDir)
	}
	stop := make(chan struct{})
	ledSyncStop = stop
	ledSyncDevices = devs
	go runLEDLoop(stop, devs)
	return nil
}

// StartLEDSyncIfEnabled restores the saved setting at launch.
func StartLEDSyncIfEnabled(config *Config) {
	if config == nil || !config.Variables.LEDSync {
		return
	}
	if err := SetLEDSync(true); err != nil {
		LogScene("led").Warn("LED sync unavailable", "err", err)
	}
}

// runLEDLoop animates the lights until stop is closed, then blanks them so the
// device does not sit frozen on the last colour.
func runLEDLoop(stop <-chan struct{}, devs []ledDevice) {
	ticker := time.NewTicker(ledFrameInterval)
	defer ticker.Stop()

	lastKey := make(map[string]string, len(devs))
	for {
		select {
		case <-stop:
			for _, dev := range devs {
				_ = applyLED(dev, 0, 0, 0)
			}
			return
		case <-ticker.C:
			s := currentAudioSample()
			level := 0.0
			if time.Since(s.At) <= ledSilenceAfter {
				level = levelFromRMS(s.RMS)
			}
			r, g, b := ledColorFor(level, s.Freq)
			// Quantise to 32 steps per channel: visually identical, far fewer
			// sysfs writes.
			r, g, b = quantiseLEDChannel(r), quantiseLEDChannel(g), quantiseLEDChannel(b)
			key := fmt.Sprintf("%d,%d,%d", r, g, b)
			for _, dev := range devs {
				if lastKey[dev.Dir] == key {
					continue
				}
				if err := applyLED(dev, r, g, b); err != nil {
					// One unwritable LED must not stop the others; log once per
					// transition and keep the last good key so we do not spin.
					LogScene("led").Warn("LED write failed", "device", dev.Name, "err", err)
					lastKey[dev.Dir] = key
					continue
				}
				lastKey[dev.Dir] = key
			}
		}
	}
}

// quantiseLEDChannel snaps an 8-bit channel to 32 steps. The result is clamped
// because rounding 255 up to 256 would wrap to 0 and switch the LED off at full
// volume.
func quantiseLEDChannel(v uint8) uint8 {
	q := (int(v) + 4) / 8 * 8
	if q > 255 {
		q = 255
	}
	return uint8(q)
}
