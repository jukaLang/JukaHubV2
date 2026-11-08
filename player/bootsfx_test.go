package main

// bootsfx_test.go — the UI sounds: their shape, their settings.

import (
	"bytes"
	"math"
	"testing"
)

// bootSfxSamples decodes mono S16LE bytes.
func bootSfxSamples(pcm []byte) []int16 {
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8)
	}
	return out
}

// Every UI sound is a short, soft tock: it starts and ends at silence (no
// click), stays well under full scale, is clearly audible, and is the same
// buffer every time it is rendered.
func TestBootSfxSoundsAreShortSoftAndClickFree(t *testing.T) {
	const rate = 48000
	for kind := bootSfxKind(0); kind < bootSfxKindCount; kind++ {
		pcm := bootSfxPCM(rate, kind)
		samples := bootSfxSamples(pcm)
		if len(samples) == 0 {
			t.Fatalf("sound %d rendered nothing", kind)
		}
		ms := float64(len(samples)) * 1000 / rate
		if ms < 20 || ms > 200 {
			t.Errorf("sound %d lasts %.0f ms", kind, ms)
		}
		if samples[0] != 0 || samples[len(samples)-1] != 0 {
			t.Errorf("sound %d starts at %d and ends at %d, want silence at both ends", kind, samples[0], samples[len(samples)-1])
		}
		peak := 0
		for _, s := range samples {
			if a := int(math.Abs(float64(s))); a > peak {
				peak = a
			}
		}
		if peak > 32767/2 {
			t.Errorf("sound %d peaks at %d, louder than a tap should be", kind, peak)
		}
		if float64(peak) < 0.08*32767 {
			t.Errorf("sound %d peaks at %d, too quiet to hear", kind, peak)
		}
		// The first quarter millisecond is deep in the fade-in: no sample there
		// may jump (a jump at the onset is the click the old beeps had).
		for i := 1; i < rate/4000 && i < len(samples); i++ {
			if d := math.Abs(float64(samples[i]) - float64(samples[i-1])); d > 0.02*32767 {
				t.Errorf("sound %d jumps by %.0f at sample %d (a click)", kind, d, i)
				break
			}
		}
		if !bytes.Equal(pcm, bootSfxPCM(rate, kind)) {
			t.Errorf("sound %d is not deterministic", kind)
		}
	}
	// Navigation is the lightest of the family.
	if len(bootSfxPCM(rate, bootSfxNav)) >= len(bootSfxPCM(rate, bootSfxActivate)) {
		t.Error("the navigate tock is not shorter than the activate one")
	}
	if bootSfxPCM(0, bootSfxNav) != nil {
		t.Error("a zero rate rendered a sound")
	}
}

// The body's pitch sits low-mid and glides down: measured between upward zero
// crossings once the tap's noise has died away (after 6 ms), the first period
// is shorter than the last, and both are in the low-mid band of a tock.
func TestBootSfxPitchGlidesDown(t *testing.T) {
	const rate = 48000
	samples := bootSfxSamples(bootSfxPCM(rate, bootSfxActivate))
	var ups []float64
	// Up to 4 ms before the end: the last few milliseconds fade to zero, where
	// rounding would blur a crossing.
	for i := rate * 6 / 1000; i < len(samples)-rate*4/1000; i++ {
		a, b := float64(samples[i-1]), float64(samples[i])
		if a < 0 && b >= 0 {
			ups = append(ups, float64(i-1)+(-a)/(b-a))
		}
	}
	if len(ups) < 3 {
		t.Fatalf("only %d upward crossings: no tonal body", len(ups))
	}
	early := rate / (ups[1] - ups[0])
	late := rate / (ups[len(ups)-1] - ups[len(ups)-2])
	if early <= late {
		t.Fatalf("pitch does not glide down: %.0f Hz then %.0f Hz", early, late)
	}
	if late < 120 || early > 360 {
		t.Fatalf("pitch %.0f → %.0f Hz is outside the low-mid tock", early, late)
	}
}

// The settings read every shape a config holds and default to on, full level.
func TestBootSfxSettings(t *testing.T) {
	prevMuted, prevLevel, prevSet := bootSfxMuted.Load(), bootSfxLevel.Load(), bootSfxLevelSet.Load()
	t.Cleanup(func() {
		bootSfxMuted.Store(prevMuted)
		bootSfxLevel.Store(prevLevel)
		bootSfxLevelSet.Store(prevSet)
	})
	for _, tc := range []struct {
		name   string
		custom map[string]interface{}
		on     bool
		level  int
	}{
		{"defaults", map[string]interface{}{}, true, 100},
		{"toggle string off", map[string]interface{}{"uiSounds": "false"}, false, 100},
		{"bool on", map[string]interface{}{"uiSounds": true, "uiSoundVolume": float64(40)}, true, 40},
		{"percent string", map[string]interface{}{"uiSoundVolume": "65%"}, true, 65},
		{"fraction", map[string]interface{}{"uiSoundVolume": 0.5}, true, 50},
		{"fraction string at full", map[string]interface{}{"uiSoundVolume": "1.0"}, true, 100},
		{"one percent", map[string]interface{}{"uiSoundVolume": "1"}, true, 1},
		{"clamped", map[string]interface{}{"uiSoundVolume": float64(250)}, true, 100},
		{"zero volume is off", map[string]interface{}{"uiSoundVolume": "0"}, false, 0},
		{"garbage keeps defaults", map[string]interface{}{"uiSounds": "maybe", "uiSoundVolume": "loud"}, true, 100},
	} {
		config := &Config{}
		config.Variables.Custom = tc.custom
		bootSfxBind(config)
		if got := bootSfxEnabled(); got != tc.on {
			t.Errorf("%s: enabled = %v, want %v", tc.name, got, tc.on)
		}
		if got := bootSfxVolume(); got != tc.level {
			t.Errorf("%s: volume = %d, want %d", tc.name, got, tc.level)
		}
	}

	config := &Config{}
	bootSfxSetPrefs(config, false, 30)
	if bootSfxEnabled() || bootSfxVolume() != 30 {
		t.Fatalf("set prefs: enabled %v volume %d", bootSfxEnabled(), bootSfxVolume())
	}
	if config.Variables.Custom[bootSfxEnabledKey] != "false" || config.Variables.Custom[bootSfxVolumeKey] != "30" {
		t.Fatalf("set prefs wrote %+v", config.Variables.Custom)
	}
}
