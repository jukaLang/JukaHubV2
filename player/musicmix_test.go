package main

import (
	"encoding/binary"
	"math"
	"testing"
)

func mixSample(v int16) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint16(b[0:], uint16(v))
	binary.LittleEndian.PutUint16(b[2:], uint16(v))
	return b
}

func mixSamples(v int16, frames int) []byte {
	b := make([]byte, 0, frames*4)
	for i := 0; i < frames; i++ {
		b = append(b, mixSample(v)...)
	}
	return b
}

func mixValue(b []byte, frame int) int16 {
	return int16(binary.LittleEndian.Uint16(b[frame*4:]))
}

func TestMusicCrossfadeSettings(t *testing.T) {
	var config Config
	if got := musicCrossfadeSeconds(&config); got != 0 {
		t.Fatalf("default crossfade = %d, want 0", got)
	}
	musicSetCrossfadeSeconds(&config, 7)
	if got := musicCrossfadeSeconds(&config); got != 7 {
		t.Fatalf("crossfade = %d, want 7", got)
	}
	musicSetCrossfadeSeconds(&config, 99)
	if got := musicCrossfadeSeconds(&config); got != musicCrossfadeMax {
		t.Fatalf("clamped crossfade = %d, want %d", got, musicCrossfadeMax)
	}
	musicSetCrossfadeSeconds(&config, -2)
	if got := musicCrossfadeSeconds(&config); got != 0 {
		t.Fatalf("negative crossfade = %d, want 0", got)
	}
	if musicReplayGainEnabled(&config) {
		t.Fatal("ReplayGain is on by default")
	}
	musicSetReplayGainEnabled(&config, true)
	if !musicReplayGainEnabled(&config) {
		t.Fatal("ReplayGain did not stay on")
	}
}

func TestMusicCrossfadeSettingsRows(t *testing.T) {
	var config Config
	crossfade := musicSettingsRow{kind: msRowCrossfade}
	replayGain := musicSettingsRow{kind: msRowReplayGain}
	if got := musicSettingsValue(crossfade, &config); got != "Off" {
		t.Fatalf("crossfade row = %q, want Off", got)
	}
	musicSetCrossfadeSeconds(&config, 4)
	if got := musicSettingsValue(crossfade, &config); got != "4s" {
		t.Fatalf("crossfade row = %q, want 4s", got)
	}
	if got := musicSettingsValue(replayGain, &config); got != "Off" {
		t.Fatalf("ReplayGain row = %q, want Off", got)
	}
	musicSetReplayGainEnabled(&config, true)
	if got := musicSettingsValue(replayGain, &config); got != "On" {
		t.Fatalf("ReplayGain row = %q, want On", got)
	}
}

func TestMusicCrossfadePeekDoesNotConsumeQueue(t *testing.T) {
	m := musicState{
		current: 0,
		tracks:  []musicTrack{{Path: "a.mp3"}, {Path: "b.mp3"}},
		queue:   []string{"b.mp3"},
	}
	next, ok := m.musicPeekNextLocked()
	if !ok || next != 1 {
		t.Fatalf("peek = %d/%v, want 1/true", next, ok)
	}
	if len(m.queue) != 1 || m.queue[0] != "b.mp3" {
		t.Fatalf("peek consumed the queue: %#v", m.queue)
	}
	m.queue = nil
	m.repeatMode = musicRepeatAll
	if next, ok := m.musicPeekNextLocked(); !ok || next != 1 {
		t.Fatalf("sequential peek = %d/%v, want 1/true", next, ok)
	}
	m.current = 1
	m.repeatMode = musicRepeatAll
	if next, ok := m.musicPeekNextLocked(); !ok || next != 0 {
		t.Fatalf("wrapped peek = %d/%v, want 0/true", next, ok)
	}
}

func TestMusicFeaturesAreOffOnBitPerfect(t *testing.T) {
	var config Config
	musicSetCrossfadeSeconds(&config, 5)
	musicSetReplayGainEnabled(&config, true)
	was := musicEQ.bitPerfect
	defer func() { musicEQ.bitPerfect = was }()
	musicEQ.bitPerfect = false
	if !musicCrossfadeAvailable(&config) || !musicReplayGainAvailable(&config) {
		t.Fatal("features unavailable with bit-perfect off")
	}
	musicEQ.bitPerfect = true
	if musicCrossfadeAvailable(&config) {
		t.Fatal("crossfade is available on the bit-perfect path")
	}
	if musicReplayGainAvailable(&config) {
		t.Fatal("ReplayGain is available on the bit-perfect path")
	}
}

func TestMusicCrossfadeFrames(t *testing.T) {
	if got := musicCrossfadeFrames(10, 44100); got != 441000 {
		t.Fatalf("10 s at 44.1 kHz = %d frames, want 441000", got)
	}
	if got := musicCrossfadeFrames(0, 48000); got != 0 {
		t.Fatalf("off = %d frames, want 0", got)
	}
	if got := musicCrossfadeFrames(99, 8000); got != 10*8000 {
		t.Fatalf("clamped = %d, want %d", got, 10*8000)
	}
}

func TestMusicCrossfadeEnvelopeIsEqualPower(t *testing.T) {
	for _, pos := range []int{0, 1, 50, 99, 100} {
		out, in := musicCrossfadeGains(pos, 100)
		if math.Abs(out*out+in*in-1) > 1e-9 {
			t.Fatalf("gains at %d: %.6f² + %.6f² != 1", pos, out, in)
		}
	}
	if out, in := musicCrossfadeGains(0, 100); out != 1 || in != 0 {
		t.Fatalf("start gains = %f/%f, want 1/0", out, in)
	}
	if out, in := musicCrossfadeGains(100, 100); out != 0 || in != 1 {
		t.Fatalf("end gains = %f/%f, want 0/1", out, in)
	}
}

func TestMusicCrossfadeMixBoundariesAndNoClick(t *testing.T) {
	cur := mixSamples(10000, 100)
	next := mixSamples(30000, 100)
	out := musicCrossfadeMixTo(nil, cur, next, 0, 100)
	if got := mixValue(out, 0); got != 10000 {
		t.Fatalf("first frame = %d, want the current track's 10000", got)
	}
	out = musicCrossfadeMixTo(nil, cur, next, 100, 100)
	if got := mixValue(out, 0); got != 30000 {
		t.Fatalf("last frame = %d, want the next track's 30000", got)
	}
	out = musicCrossfadeMixTo(nil, cur, next, 50, 100)
	want := int16(math.Round((10000 + 30000) / math.Sqrt2))
	if got := mixValue(out, 0); got != want {
		t.Fatalf("midpoint = %d, want %d", got, want)
	}

	// A constant signal on both sides must cross without a step or click: the
	// envelope moves smoothly and the ends are exactly the two sources.
	flat := mixSamples(12000, 200)
	mixed := musicCrossfadeMixTo(nil, flat, flat, 0, 200)
	if got := mixValue(mixed, 0); got != 12000 {
		t.Fatalf("first flat frame = %d, want 12000", got)
	}
	if got := mixValue(mixed, 199); got != 12000 {
		t.Fatalf("last flat frame = %d, want 12000", got)
	}
	prev := mixValue(mixed, 0)
	for i := 1; i < 200; i++ {
		got := mixValue(mixed, i)
		delta := int(got) - int(prev)
		if delta < 0 {
			delta = -delta
		}
		if delta > 400 {
			t.Fatalf("frame %d jumps by %d, want a smooth envelope", i, delta)
		}
		prev = got
	}
}

func TestMusicCrossfadeMixLengthsAndClipping(t *testing.T) {
	out := musicCrossfadeMixTo(nil, mixSamples(1000, 3), mixSamples(2000, 5), 0, 5)
	if len(out) != 5*4 {
		t.Fatalf("mixed %d frames, want 5", len(out)/4)
	}
	loud := musicCrossfadeMixTo(nil, mixSamples(30000, 2), mixSamples(30000, 2), 50, 100)
	for i := 0; i < 2; i++ {
		if got := mixValue(loud, i); got != 32767 {
			t.Fatalf("clipped frame %d = %d, want 32767", i, got)
		}
	}
}

func TestMusicParseReplayGainTags(t *testing.T) {
	raw := "TAG:REPLAYGAIN_TRACK_GAIN=-6.0206 dB\n" +
		"TAG:REPLAYGAIN_TRACK_PEAK=0.800000\n" +
		"TAG:REPLAYGAIN_ALBUM_GAIN=-3.0 dB\n" +
		"TAG:REPLAYGAIN_ALBUM_PEAK=0.950000\n"
	tags := musicParseReplayGainTags(raw)
	if !tags.HasTrackGain || math.Abs(tags.TrackGain+6.0206) > 1e-9 {
		t.Fatalf("track gain = %v (%v)", tags.TrackGain, tags.HasTrackGain)
	}
	if !tags.HasTrackPeak || math.Abs(tags.TrackPeak-0.8) > 1e-9 {
		t.Fatalf("track peak = %v", tags.TrackPeak)
	}
	if !tags.HasAlbumGain || !tags.HasAlbumPeak {
		t.Fatal("album tags were not parsed")
	}

	// Track tags win; the album is the fallback.
	gain, peak, ok := musicReplayGainLevel(tags)
	if !ok || math.Abs(gain-0.5) > 1e-6 || peak != 0.8 {
		t.Fatalf("track level = %v/%v/%v, want 0.5/0.8/true", gain, peak, ok)
	}
	albumOnly := musicParseReplayGainTags("TAG:REPLAYGAIN_ALBUM_GAIN=12.0412 dB\nTAG:REPLAYGAIN_ALBUM_PEAK=0.5")
	gain, peak, ok = musicReplayGainLevel(albumOnly)
	if !ok || math.Abs(gain-2) > 1e-6 || peak != 0.5 {
		t.Fatalf("album level = %v/%v/%v, want 2/0.5/true (peak-limited 4×)", gain, peak, ok)
	}
	if gain, peak, ok := musicReplayGainLevel(musicParseReplayGainTags("")); ok || gain != 1 || peak != 0 {
		t.Fatalf("missing tags = %v/%v/%v, want 1/0/false", gain, peak, ok)
	}
}

func TestMusicReplayGainApply(t *testing.T) {
	pcm := mixSample(20000)
	musicReplayGainApply(pcm, 0.5)
	if got := mixValue(pcm, 0); got != 10000 {
		t.Fatalf("half gain = %d, want 10000", got)
	}
	pcm = mixSample(20000)
	musicReplayGainApply(pcm, 2)
	if got := mixValue(pcm, 0); got != 32767 {
		t.Fatalf("clipped positive = %d, want 32767", got)
	}
	pcm = mixSample(-20000)
	musicReplayGainApply(pcm, 2)
	if got := mixValue(pcm, 0); got != -32768 {
		t.Fatalf("clipped negative = %d, want -32768", got)
	}
	pcm = mixSample(1234)
	musicReplayGainApply(pcm, 1)
	if got := mixValue(pcm, 0); got != 1234 {
		t.Fatalf("unity gain changed the sample to %d", got)
	}
}
