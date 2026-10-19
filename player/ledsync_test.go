package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// These cover the parts of the LED sync that decide what to write and where:
// audio analysis, colour mapping, LED discovery and the per-mode writers. The
// animation goroutine itself is timing-based and is exercised on the device.

// pcmSine builds interleaved S16 sine frames at the given amplitude (0..1).
func pcmSine(freq, rate float64, frames, channels int, amp float64) []byte {
	out := make([]byte, frames*channels*2)
	for i := 0; i < frames; i++ {
		v := int16(math.Sin(2*math.Pi*freq*float64(i)/rate) * amp * 32767)
		for c := 0; c < channels; c++ {
			off := (i*channels + c) * 2
			out[off] = byte(uint16(v))
			out[off+1] = byte(uint16(v) >> 8)
		}
	}
	return out
}

func TestRMSPCM16(t *testing.T) {
	silence := make([]byte, 400)
	if got := rmsPCM16(silence, 100, 2); got != 0 {
		t.Fatalf("silence RMS = %v, want 0", got)
	}

	// A full-scale square wave has RMS 1.0.
	full := make([]byte, 200*2*2)
	for i := 0; i < 400; i++ {
		v := int16(32767)
		full[i*2] = byte(uint16(v))
		full[i*2+1] = byte(uint16(v) >> 8)
	}
	if got := rmsPCM16(full, 200, 2); math.Abs(got-1) > 0.01 {
		t.Fatalf("full-scale RMS = %v, want ~1", got)
	}

	// A sine at amplitude 0.5 has RMS ~0.354.
	sine := pcmSine(440, 44100, 4410, 2, 0.5)
	if got := rmsPCM16(sine, 4410, 2); math.Abs(got-0.3536) > 0.02 {
		t.Fatalf("sine RMS = %v, want ~0.354", got)
	}

	// Degenerate inputs must not panic or divide by zero.
	if got := rmsPCM16(nil, 0, 2); got != 0 {
		t.Fatalf("empty RMS = %v, want 0", got)
	}
	if got := rmsPCM16([]byte{1, 2}, 10, 2); got == 0 {
		t.Fatal("short buffer produced zero instead of analysing the available sample")
	}
}

func TestZeroCrossHz(t *testing.T) {
	const rate = 44100
	for _, freq := range []float64{220, 440, 1000} {
		pcm := pcmSine(freq, rate, rate/10, 2, 0.8)
		got := zeroCrossHz(pcm, rate, 2)
		if math.Abs(got-freq) > freq*0.05 {
			t.Fatalf("%.0f Hz sine measured as %.1f Hz", freq, got)
		}
	}
	if got := zeroCrossHz(nil, rate, 2); got != 0 {
		t.Fatalf("empty buffer measured %v Hz", got)
	}
	// Pure silence must not be read as a high frequency.
	if got := zeroCrossHz(make([]byte, 4000), rate, 2); got != 0 {
		t.Fatalf("silence measured %v Hz", got)
	}
}

func TestLevelFromRMSGateAndCurve(t *testing.T) {
	if got := levelFromRMS(0); got != 0 {
		t.Fatalf("silence level = %v, want 0", got)
	}
	if got := levelFromRMS(0.001); got != 0 {
		t.Fatalf("below-gate level = %v, want 0", got)
	}
	if got := levelFromRMS(0.5); got != 1 {
		t.Fatalf("loud level = %v, want 1", got)
	}
	if got := levelFromRMS(0.9); got != 1 {
		t.Fatalf("clipped level = %v, want 1", got)
	}
	quiet := levelFromRMS(0.01)
	mid := levelFromRMS(0.05)
	if !(quiet > 0 && quiet < mid && mid < 1) {
		t.Fatalf("curve not monotonic: 0.01 -> %v, 0.05 -> %v", quiet, mid)
	}
}

func TestHueForFrequencyBands(t *testing.T) {
	cases := []struct {
		hz   float64
		want float64
	}{
		{0, 0.58},
		{60, 0.00},
		{200, 0.08},
		{800, 0.33},
		{2000, 0.50},
		{5000, 0.75},
	}
	for _, c := range cases {
		if got := hueForFrequency(c.hz); got != c.want {
			t.Fatalf("hueForFrequency(%v) = %v, want %v", c.hz, got, c.want)
		}
	}
}

func TestHSVToRGBPrimaries(t *testing.T) {
	cases := []struct {
		h       float64
		r, g, b uint8
	}{
		{0.0, 255, 0, 0},
		{0.333, 0, 255, 0},
		{0.5, 0, 255, 255},
		{0.667, 0, 0, 255},
	}
	for _, c := range cases {
		r, g, b := hsvToRGB(c.h, 1, 1)
		if absDiff8(r, c.r) > 1 || absDiff8(g, c.g) > 1 || absDiff8(b, c.b) > 1 {
			t.Fatalf("hsvToRGB(%v) = %d,%d,%d; want %d,%d,%d", c.h, r, g, b, c.r, c.g, c.b)
		}
	}
	// Value scales the whole colour, which is how brightness is applied.
	r, _, _ := hsvToRGB(0, 1, 0.5)
	if r < 120 || r > 136 {
		t.Fatalf("half-value red = %d, want ~128", r)
	}
}

func absDiff8(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestLedColorForSilenceIsOff(t *testing.T) {
	if r, g, b := ledColorFor(0, 440); r != 0 || g != 0 || b != 0 {
		t.Fatalf("silence produced %d,%d,%d, want black", r, g, b)
	}
	// A quiet bass note must still be visibly red.
	r, g, b := ledColorFor(0.1, 80)
	if r <= g || r <= b || r == 0 {
		t.Fatalf("bass tone produced %d,%d,%d, want red-dominant", r, g, b)
	}
}

func TestQuantiseLEDChannel(t *testing.T) {
	cases := []struct {
		in   uint8
		want uint8
	}{
		{0, 0},
		{3, 0},
		{4, 8},
		{128, 128},
		{255, 255}, // clamped: rounding up to 256 would wrap to 0
	}
	for _, c := range cases {
		if got := quantiseLEDChannel(c.in); got != c.want {
			t.Fatalf("quantiseLEDChannel(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// makeLED creates a fake LED directory with the given files.
func makeLED(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, content := range files {
		path := filepath.Join(dir, f)
		if content == "" {
			// Writable but empty, like an unwritten sysfs attribute.
			content = "0"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscoverLEDsClassifiesAndFilters(t *testing.T) {
	root := t.TempDir()

	multi := makeLED(t, root, "rgb1", map[string]string{
		"multi_intensity": "0 0 0",
		"max_brightness":  "255",
		"brightness":      "128",
	})
	colored := makeLED(t, root, "status", map[string]string{"color": "#000000"})
	stick := makeLED(t, root, "joypad_led", map[string]string{
		"brightness":     "1",
		"max_brightness": "2",
	})
	// A brightness-only LED with no decorative name must be left alone.
	makeLED(t, root, "input0_capslock", map[string]string{"brightness": "1"})
	// A plain file in the root is not a device.
	if err := os.WriteFile(filepath.Join(root, "not-a-dir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	devs := discoverLEDs(root)
	byName := map[string]ledDevice{}
	for _, d := range devs {
		byName[d.Name] = d
	}
	if len(devs) != 3 {
		t.Fatalf("discovered %d LEDs, want 3: %+v", len(devs), devs)
	}
	if d := byName["rgb1"]; d.Mode != ledModeMultiIntensity || d.Max != 255 || !d.HasBrightness || d.Dir != multi {
		t.Fatalf("rgb device classified wrong: %+v", d)
	}
	if d := byName["status"]; d.Mode != ledModeColor || d.Dir != colored {
		t.Fatalf("color device classified wrong: %+v", d)
	}
	if d := byName["joypad_led"]; d.Mode != ledModeBrightness || d.Max != 2 || d.Dir != stick {
		t.Fatalf("brightness device classified wrong: %+v", d)
	}
	if _, ok := byName["input0_capslock"]; ok {
		t.Fatal("a non-decorative brightness LED was picked up")
	}
}

func TestDiscoverLEDsMissingRoot(t *testing.T) {
	if got := discoverLEDs(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("missing root returned %+v, want nil", got)
	}
}

func TestApplyLEDModes(t *testing.T) {
	root := t.TempDir()

	multiDir := makeLED(t, root, "rgb1", map[string]string{
		"multi_intensity": "0 0 0",
		"max_brightness":  "255",
		"brightness":      "0",
	})
	multi := ledDevice{Name: "rgb1", Dir: multiDir, Mode: ledModeMultiIntensity, Max: 255, HasBrightness: true}
	if err := applyLED(multi, 255, 128, 0); err != nil {
		t.Fatalf("applyLED multi: %v", err)
	}
	if got := readTestFile(t, multiDir, "multi_intensity"); got != "255 128 0" {
		t.Fatalf("multi_intensity = %q, want %q", got, "255 128 0")
	}
	if got := readTestFile(t, multiDir, "brightness"); got != "255" {
		t.Fatalf("brightness = %q, want 255 while lit", got)
	}
	// Off clears both, because a multicolor driver keeps showing intensity.
	if err := applyLED(multi, 0, 0, 0); err != nil {
		t.Fatalf("applyLED multi off: %v", err)
	}
	if got := readTestFile(t, multiDir, "brightness"); got != "0" {
		t.Fatalf("brightness = %q, want 0 when off", got)
	}

	colorDir := makeLED(t, root, "status", map[string]string{"color": "#000000"})
	colored := ledDevice{Name: "status", Dir: colorDir, Mode: ledModeColor, Max: 255}
	if err := applyLED(colored, 16, 32, 48); err != nil {
		t.Fatalf("applyLED color: %v", err)
	}
	if got := readTestFile(t, colorDir, "color"); got != "#102030" {
		t.Fatalf("color = %q, want #102030", got)
	}

	brightDir := makeLED(t, root, "joypad_led", map[string]string{
		"brightness":     "0",
		"max_brightness": "100",
	})
	bright := ledDevice{Name: "joypad_led", Dir: brightDir, Mode: ledModeBrightness, Max: 100, HasBrightness: true}
	if err := applyLED(bright, 0, 0, 255); err != nil {
		t.Fatalf("applyLED brightness: %v", err)
	}
	if got := readTestFile(t, brightDir, "brightness"); got != "100" {
		t.Fatalf("brightness = %q, want 100", got)
	}
	// A dim colour is at least 1 so it is visible rather than silently zero.
	if err := applyLED(bright, 1, 1, 1); err != nil {
		t.Fatalf("applyLED dim: %v", err)
	}
	if got := readTestFile(t, brightDir, "brightness"); got != "1" {
		t.Fatalf("dim brightness = %q, want 1", got)
	}
	if err := applyLED(bright, 0, 0, 0); err != nil {
		t.Fatalf("applyLED off: %v", err)
	}
	if got := readTestFile(t, brightDir, "brightness"); got != "0" {
		t.Fatalf("off brightness = %q, want 0", got)
	}
}

func readTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAddressableLEDName(t *testing.T) {
	yes := []string{"rgb1", "joypad_led", "left_stick", "gamepad_ring", "RGB1"}
	no := []string{"input0_capslock", "mmc0", "power", "charging"}
	for _, n := range yes {
		if !addressableLEDName(n) {
			t.Fatalf("%q should be addressable", n)
		}
	}
	for _, n := range no {
		if addressableLEDName(n) {
			t.Fatalf("%q should not be addressable", n)
		}
	}
}
