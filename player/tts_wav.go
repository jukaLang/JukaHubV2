package main

// tts_wav.go — a voice's WAV file turned into samples the UI device plays.
//
// Every voice Juka has hands back a WAV: Groq's (reported as 24 kHz mono, but
// the fmt chunk is read, never assumed), Windows' speech synthesiser and the
// Linux engines (16–22 kHz). The UI device runs at audioSampleRate (usually
// 48 kHz) in mono 16-bit, so a clip is decoded to mono 16-bit, resampled with
// cubic (Catmull-Rom) interpolation — linear interpolation leaves a faint
// metallic edge on a voice going up from 24 kHz — and trimmed: Groq pads a
// clip with up to half a second of silence at each end, which between two
// chunks read as a stall rather than a pause. A short fade at both ends keeps
// a clip from clicking.
//
// Groq's WAV header can declare a data size that is plainly wrong (one was
// seen claiming 24 hours for a 10-second clip), so its data chunk is read to
// the end of the response whatever it declares. A local engine's file is read
// as declared when the declared size is sane: something may follow the data.
// Everything here is pure.

import (
	"encoding/binary"
	"errors"
	"math"
)

var (
	errTTSNotWAV   = errors.New("the voice did not send a WAV file")
	errTTSWAVShape = errors.New("the voice's WAV format is not one JukaHub can play")
)

const (
	// ttsSilenceLevel is the loudest sample that still counts as silence when
	// a clip is trimmed (about −40 dBFS).
	ttsSilenceLevel = 330
	// ttsKeepLeadMs and ttsKeepTailMs are the silence a clip keeps before its
	// first sound and after its last: a breath, and a sentence's pause.
	ttsKeepLeadMs = 60
	ttsKeepTailMs = 160
	// ttsFadeInMs and ttsFadeOutMs are the ramps at a clip's two ends.
	ttsFadeInMs  = 4
	ttsFadeOutMs = 10
)

// ttsDecodeWAV reads a RIFF/WAVE file into mono 16-bit samples and their rate.
// toEOF reads the data chunk to the end of data whatever size it declares
// (Groq's); otherwise a declared size that fits is trusted.
func ttsDecodeWAV(data []byte, toEOF bool) ([]int16, int, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errTTSNotWAV
	}
	var (
		haveFmt  bool
		format   int
		channels int
		rate     int
		bits     int
	)
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := uint64(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(data) {
				return nil, 0, errTTSWAVShape
			}
			format = int(binary.LittleEndian.Uint16(data[body:]))
			channels = int(binary.LittleEndian.Uint16(data[body+2:]))
			rate = int(binary.LittleEndian.Uint32(data[body+4:]))
			bits = int(binary.LittleEndian.Uint16(data[body+14:]))
			// WAVE_FORMAT_EXTENSIBLE names the real format in its sub-format
			// GUID, whose first two bytes are the classic format code.
			if format == 0xFFFE && size >= 40 && body+26 <= len(data) {
				format = int(binary.LittleEndian.Uint16(data[body+24:]))
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, 0, errTTSWAVShape
			}
			end := len(data)
			if !toEOF && size > 0 && uint64(body)+size <= uint64(len(data)) {
				end = body + int(size)
			}
			return ttsPCMFrom(data[body:end], format, channels, rate, bits)
		}
		next := uint64(body) + size + size&1
		if next > uint64(len(data)) {
			break
		}
		off = int(next)
	}
	return nil, 0, errTTSNotWAV
}

// ttsPCMFrom converts interleaved PCM (8/16/24/32-bit integer or 32/64-bit
// float) into mono 16-bit samples, averaging the channels.
func ttsPCMFrom(raw []byte, format, channels, rate, bits int) ([]int16, int, error) {
	if channels < 1 || channels > 8 || rate < 4000 || rate > 384000 || bits < 8 || bits%8 != 0 {
		return nil, 0, errTTSWAVShape
	}
	width := bits / 8
	var sample func(b []byte) float64 // one sample as a fraction of full scale
	switch {
	case format == 1 && bits == 8:
		sample = func(b []byte) float64 { return (float64(b[0]) - 128) / 128 }
	case format == 1 && bits == 16:
		sample = func(b []byte) float64 { return float64(int16(binary.LittleEndian.Uint16(b))) / 32768 }
	case format == 1 && bits == 24:
		sample = func(b []byte) float64 {
			v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
			return float64(v) / 8388608
		}
	case format == 1 && bits == 32:
		sample = func(b []byte) float64 { return float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648 }
	case format == 3 && bits == 32:
		sample = func(b []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }
	case format == 3 && bits == 64:
		sample = func(b []byte) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(b)) }
	default:
		return nil, 0, errTTSWAVShape
	}
	frame := width * channels
	n := len(raw) / frame
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		sum := 0.0
		base := i * frame
		for c := 0; c < channels; c++ {
			sum += sample(raw[base+c*width:])
		}
		out[i] = ttsClip16(sum / float64(channels) * 32767)
	}
	return out, rate, nil
}

// ttsClip16 rounds a sample to 16 bits, clamped to full scale (a float file
// may hold samples beyond ±1, and NaN plays as silence).
func ttsClip16(v float64) int16 {
	switch {
	case math.IsNaN(v):
		return 0
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return int16(math.Round(v))
}

// ttsResample converts mono samples from one rate to another with cubic
// (Catmull-Rom) interpolation. The same rate returns in itself.
func ttsResample(in []int16, from, to int) []int16 {
	if from == to || from <= 0 || to <= 0 || len(in) == 0 {
		return in
	}
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]int16, n)
	at := func(i int) float64 {
		if i < 0 {
			i = 0
		} else if i >= len(in) {
			i = len(in) - 1
		}
		return float64(in[i])
	}
	step := float64(from) / float64(to)
	for i := range out {
		x := float64(i) * step
		j := int(x)
		t := x - float64(j)
		p0, p1, p2, p3 := at(j-1), at(j), at(j+1), at(j+2)
		v := p1 + 0.5*t*(p2-p0+t*(2*p0-5*p1+4*p2-p3+t*(3*(p1-p2)+p3-p0)))
		out[i] = ttsClip16(v)
	}
	return out
}

// ttsTrim drops a clip's silence beyond ttsKeepLeadMs before its first sound
// and ttsKeepTailMs after its last. A clip with no sound at all gives nil.
func ttsTrim(s []int16, rate int) []int16 {
	first, last := -1, -1
	for i, v := range s {
		if v > ttsSilenceLevel || v < -ttsSilenceLevel {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	for i := len(s) - 1; i >= first; i-- {
		if v := s[i]; v > ttsSilenceLevel || v < -ttsSilenceLevel {
			last = i
			break
		}
	}
	start := first - rate*ttsKeepLeadMs/1000
	if start < 0 {
		start = 0
	}
	end := last + 1 + rate*ttsKeepTailMs/1000
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// ttsFade ramps a clip's first ttsFadeInMs up from silence and its last
// ttsFadeOutMs down to it, in place.
func ttsFade(s []int16, rate int) {
	in := rate * ttsFadeInMs / 1000
	outN := rate * ttsFadeOutMs / 1000
	if in+outN > len(s) {
		in, outN = len(s)/3, len(s)/3
	}
	for i := 0; i < in; i++ {
		s[i] = int16(float64(s[i]) * float64(i) / float64(in))
	}
	for i := 0; i < outN; i++ {
		k := len(s) - 1 - i
		s[k] = int16(float64(s[k]) * float64(i) / float64(outN))
	}
}

// ttsClipFor turns a decoded clip into what the feeder plays: resampled to the
// device's rate, trimmed and faded. It may fade samples in place (a clip at
// the device's rate is not copied); a silent clip gives nil.
func ttsClipFor(samples []int16, rate, deviceRate int) []int16 {
	out := ttsTrim(ttsResample(samples, rate, deviceRate), deviceRate)
	if len(out) == 0 {
		return nil
	}
	ttsFade(out, deviceRate)
	return out
}
