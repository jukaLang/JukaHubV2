package main

import (
	"math"
	"testing"
)

// TestChimesRenderCleanly renders both chimes the way the device does (block
// by block, at the two common rates) and checks what a listener would notice
// first: the right length, a peak near the designed level and never clipped,
// silence at both ends (no click when the device opens or closes), and no
// NaN from a bad parameter.
func TestChimesRenderCleanly(t *testing.T) {
	cases := []struct {
		name    string
		score   []chimeNote
		seconds float64
		gain    float64
	}{
		{"start", chimeStartScore, chimeStartSeconds, chimeStartGain},
		{"stop", chimeStopScore, chimeStopSeconds, chimeStopGain},
	}
	for _, tc := range cases {
		for _, rate := range []int{48000, 44100} {
			synth := newChimeSynth(tc.score, rate, tc.seconds, tc.gain)
			var pcm []byte
			for {
				n := len(pcm)
				pcm = synth.render(pcm)
				if len(pcm) == n {
					break
				}
			}
			frames := len(pcm) / 4
			if want := int(math.Ceil(tc.seconds * float64(rate))); frames != want {
				t.Fatalf("%s@%d: %d frames, want %d", tc.name, rate, frames, want)
			}
			peak, edge := 0.0, 0.0
			for i := 0; i < len(pcm); i += 2 {
				v := math.Abs(float64(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))) / 32767
				peak = math.Max(peak, v)
				if f := i / 4; f < rate/500 || f >= frames-rate/100 {
					edge = math.Max(edge, v)
				}
			}
			if peak < 0.35 || peak > 0.75 {
				t.Errorf("%s@%d: peak %.3f, want about 0.56", tc.name, rate, peak)
			}
			if edge > 0.002 {
				t.Errorf("%s@%d: %.4f of full scale at the first 2 ms or last 10 ms, want silence", tc.name, rate, edge)
			}
		}
	}
}
