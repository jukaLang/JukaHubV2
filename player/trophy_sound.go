package main

// trophy_sound.go — the trophy chime.
//
// An unlock plays a short rising arpeggio of bell tones: bright enough to cut
// through a game's music on the handheld's small speaker, short and soft enough
// not to startle. Each note is a sine with two faster-decaying upper partials —
// the strike of a celesta rather than the UI's beeps — and the notes overlap,
// so the arpeggio rings instead of stepping. Higher tiers climb further; the
// platinum adds a held chord with a shimmer on top, about two seconds long.
//
// The PCM is synthesised once per tier and sample rate, off the render thread,
// and queued on the UI sound device that sound.go opens (the same device, lock
// and LED-sync tap as every other UI sound).

import (
	"math"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// trophyNote is one bell strike of the chime.
type trophyNote struct {
	at    float64 // start, seconds from the beginning of the chime
	freq  float64 // fundamental, Hz
	gain  float64 // relative loudness
	decay float64 // time constant of the ring, seconds
}

// The notes, in Hz. The chime sits in the octave above the UI sounds (C6–C7),
// where the Smart Pro's speaker is loudest and a game's mix is thinnest.
const (
	trophyG5 = 783.99
	trophyC6 = 1046.50
	trophyE6 = 1318.51
	trophyG6 = 1567.98
	trophyC7 = 2093.00
	trophyE7 = 2637.02
	trophyG7 = 3135.96
)

// trophyChimePeak is the chime's loudest sample as a fraction of full scale:
// close to the UI's own taps (0.20–0.26), well under a toast's beep.
const trophyChimePeak = 0.24

// trophyChimeScore returns a tier's notes and the chime's length in seconds.
func trophyChimeScore(tier trophyTier) ([]trophyNote, float64) {
	switch tier {
	case trophySilver:
		return []trophyNote{
			{0, trophyC6, 1, 0.34}, {0.07, trophyE6, 0.95, 0.34},
			{0.14, trophyG6, 0.9, 0.36}, {0.21, trophyC7, 0.85, 0.42},
		}, 0.95
	case trophyGold:
		return []trophyNote{
			{0, trophyG5, 0.9, 0.32}, {0.065, trophyC6, 1, 0.34}, {0.13, trophyE6, 0.95, 0.36},
			{0.195, trophyG6, 0.9, 0.38}, {0.26, trophyC7, 0.9, 0.48}, {0.32, trophyE7, 0.35, 0.36},
		}, 1.2
	case trophyPlatinum:
		return []trophyNote{
			// A quick climb...
			{0, trophyG5, 0.85, 0.28}, {0.07, trophyC6, 0.95, 0.30},
			{0.14, trophyE6, 0.95, 0.30}, {0.21, trophyG6, 0.95, 0.32},
			// ...that lands on a held chord...
			{0.36, trophyC6, 0.55, 0.85}, {0.36, trophyE6, 0.55, 0.85},
			{0.36, trophyG6, 0.55, 0.85}, {0.36, trophyC7, 0.7, 0.95},
			// ...with a shimmer over it.
			{0.52, trophyG7, 0.22, 0.30}, {0.64, trophyE7, 0.26, 0.40},
			{0.78, trophyG7, 0.18, 0.30}, {0.92, trophyC7, 0.30, 0.60},
		}, 2.4
	}
	return []trophyNote{
		{0, trophyC6, 1, 0.32}, {0.075, trophyE6, 0.95, 0.32}, {0.15, trophyG6, 0.9, 0.38},
	}, 0.85
}

// trophyChimePCM synthesises a tier's chime as mono signed 16-bit samples at
// rate, little-endian: the UI device's format. The mix is normalised to
// trophyChimePeak whatever the overlap, starts at silence and fades its last
// 30 ms out, so it neither clicks nor depends on how many notes stack up.
func trophyChimePCM(rate int32, tier trophyTier) []byte {
	if rate <= 0 {
		return nil
	}
	notes, length := trophyChimeScore(tier)
	n := int(float64(rate) * length)
	if n <= 0 {
		return nil
	}
	mix := make([]float64, n)
	sr := float64(rate)
	attack := 0.003 * sr
	for _, note := range notes {
		start := int(note.at * sr)
		for i := start; i < n; i++ {
			t := float64(i-start) / sr
			env := note.gain * math.Exp(-t/note.decay)
			if env < 1e-4 {
				break
			}
			if a := float64(i - start); a < attack {
				env *= a / attack
			}
			w := 2 * math.Pi * note.freq * t
			v := math.Sin(w) +
				0.30*math.Exp(-t/0.12)*math.Sin(2*w) +
				0.10*math.Exp(-t/0.08)*math.Sin(3.01*w)
			mix[i] += env * v
		}
	}
	peak := 0.0
	for _, v := range mix {
		if a := math.Abs(v); a > peak {
			peak = a
		}
	}
	if peak <= 0 {
		return make([]byte, n*2)
	}
	scale := trophyChimePeak / peak
	fade := int(0.03 * sr)
	out := make([]byte, n*2)
	for i, v := range mix {
		v *= scale
		if rest := n - 1 - i; rest < fade {
			v *= float64(rest) / float64(fade)
		}
		s := int16(math.Round(v * 32767))
		out[i*2] = byte(s)
		out[i*2+1] = byte(s >> 8)
	}
	return out
}

// trophyChimeCache keeps each tier's PCM for the device's rate.
var trophyChimeCache struct {
	mu   sync.Mutex
	rate int32
	pcm  [trophyTierCount][]byte
}

// trophyChimeFor returns a tier's PCM at rate, synthesising it once.
func trophyChimeFor(rate int32, tier trophyTier) []byte {
	if int(tier) >= trophyTierCount {
		tier = trophyBronze
	}
	trophyChimeCache.mu.Lock()
	defer trophyChimeCache.mu.Unlock()
	if trophyChimeCache.rate != rate {
		trophyChimeCache.rate = rate
		trophyChimeCache.pcm = [trophyTierCount][]byte{}
	}
	if trophyChimeCache.pcm[tier] == nil {
		trophyChimeCache.pcm[tier] = trophyChimePCM(rate, tier)
	}
	return trophyChimeCache.pcm[tier]
}

// trophyPlayChime queues a tier's chime on the UI sound device. It is called on
// its own goroutine: the first synthesis of the platinum fanfare is tens of
// milliseconds on the handheld, which the render thread must not wait for. A
// device already holding two seconds of sound is busy with something louder
// than a trophy, and the chime is skipped rather than played late.
func trophyPlayChime(tier trophyTier) {
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable {
		return
	}
	rate := audioSampleRate
	pcm := trophyChimeScaled(trophyChimeFor(rate, tier), bootSfxVolume())
	if len(pcm) == 0 {
		return
	}
	audioMutex.Lock()
	defer audioMutex.Unlock()
	if sdl.GetQueuedAudioSize(audioDev) > uint32(rate)*4 {
		return
	}
	publishAudioLevel(pcm, int(rate), 1)
	_ = sdl.QueueAudio(audioDev, pcm)
}

// trophyChimeScaled applies the UI sound volume (percent) to a chime, on the
// same curve the UI sounds use (the slider's fraction squared, so its lower
// half stays useful). Full volume returns pcm itself; anything less is a copy,
// one per celebration, made off the render thread.
func trophyChimeScaled(pcm []byte, percent int) []byte {
	if percent >= 100 || len(pcm) == 0 {
		return pcm
	}
	if percent <= 0 {
		return nil
	}
	gain := float64(percent) / 100
	gain *= gain
	out := make([]byte, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		v := int16(math.Round(float64(s) * gain))
		out[i] = byte(v)
		out[i+1] = byte(uint16(v) >> 8)
	}
	return out
}
