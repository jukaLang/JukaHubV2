package main

// sidplay_chip.go — a cheap model of the C64's SID sound chip (6581/8580).
//
// Three voices, each a 24-bit phase accumulator feeding four waveforms
// (triangle, sawtooth, pulse, noise), an ADSR envelope and the voice's share
// of the mix. It is stepped once per output sample (about 22 chip cycles),
// not once per cycle as reSID does, which is what keeps it at a percent or
// two of an A133P core:
//
//   - the oscillators add 22.34 cycles' worth of frequency per sample, with
//     hard sync and ring modulation taken from the voice before;
//   - the noise register is clocked once for every rise of accumulator bit 19
//     the step passed;
//   - combined waveforms are the AND of their parts (close to the 8580, a
//     rough stand-in for the 6581's analogue mixing);
//   - the envelope uses the real rate-counter periods and the exponential
//     decay steps, without the ADSR delay bug;
//   - the filter is one state-variable filter (the zero-delay "TPT" form,
//     stable at any cutoff) with a cutoff curve per model: nearly linear on
//     the 8580, the steep curve of a typical 6581 otherwise;
//   - 4-bit "digi" samples written through the volume register are not
//     reproduced: the tune runs between samples, not cycle by cycle.

import "math"

// sidRatePeriods are the envelope rate counter's periods in chip cycles for
// the 16 attack/decay/release settings (the SID datasheet's 2 ms … 8 s).
var sidRatePeriods = [16]float32{9, 32, 63, 95, 149, 220, 267, 313, 392, 977, 1954, 3126, 3907, 11720, 19532, 31251}

const (
	sidGate  = 0x01
	sidSync  = 0x02
	sidRing  = 0x04
	sidTest  = 0x08
	sidTri   = 0x10
	sidSaw   = 0x20
	sidPulse = 0x40
	sidNoise = 0x80

	// sidBit19 and sidBit23 are the phase bits that clock the noise and
	// mark the top-bit rise, with the 8 fraction bits below them.
	sidBit19 = 1 << 27
	sidBit23 = 1 << 31

	sidAttack  = 0
	sidDecay   = 1
	sidRelease = 2
)

type sidVoice struct {
	freq    uint32
	pw      uint32
	ctrl    byte
	ad, sr  byte
	acc     uint32 // 24-bit phase with 8 bits of fraction below it
	noise   uint32 // 23-bit noise shift register
	msbUp   bool   // the accumulator's top bit rose during the last step
	env     int32
	state   int
	rateCnt float32
	expCnt  int32
}

type sidChip struct {
	v      [3]sidVoice
	cps256 uint32  // chip cycles per sample, times 256
	cps    float32 // the same as a float, for the envelope counters
	rate   float32
	m8580  bool

	fc      uint32
	res     byte
	filt    byte
	modeVol byte

	// Filter coefficients (recomputed on a register write) and state.
	dirty         bool
	fa1, fa2, fa3 float32
	fk            float32
	ic1, ic2      float32
	dcIn, dcOut   float32
	lastWave3     uint32
}

// reset powers the chip on for a clock rate, an output rate and a model.
func (c *sidChip) reset(clock float64, rate int, m8580 bool) {
	*c = sidChip{}
	cps := clock / float64(rate)
	c.cps = float32(cps)
	c.cps256 = uint32(cps*256 + 0.5)
	c.rate = float32(rate)
	c.m8580 = m8580
	for i := range c.v {
		c.v[i].noise = 0x7FFFF8
		c.v[i].state = sidRelease
	}
	c.dirty = true
}

// write stores one register ($D400 + reg).
func (c *sidChip) write(reg byte, val byte) {
	if reg < 21 {
		v := &c.v[reg/7]
		switch reg % 7 {
		case 0:
			v.freq = v.freq&0xFF00 | uint32(val)
		case 1:
			v.freq = v.freq&0x00FF | uint32(val)<<8
		case 2:
			v.pw = v.pw&0xF00 | uint32(val)
		case 3:
			v.pw = v.pw&0x0FF | uint32(val&0x0F)<<8
		case 4:
			was := v.ctrl
			v.ctrl = val
			if val&sidGate != 0 && was&sidGate == 0 {
				v.state = sidAttack
			} else if val&sidGate == 0 && was&sidGate != 0 {
				v.state = sidRelease
			}
			if val&sidTest != 0 {
				v.acc = 0
				v.noise = 0x7FFFF8
			}
		case 5:
			v.ad = val
		case 6:
			v.sr = val
		}
		return
	}
	switch reg {
	case 0x15:
		c.fc = c.fc&0x7F8 | uint32(val&7)
		c.dirty = true
	case 0x16:
		c.fc = c.fc&7 | uint32(val)<<3
		c.dirty = true
	case 0x17:
		c.res, c.filt = val>>4, val&0x0F
		c.dirty = true
	case 0x18:
		c.modeVol = val
	}
}

// read answers the readable registers: voice 3's oscillator and envelope
// (tunes use them as a random source and for effects), and the paddles.
func (c *sidChip) read(reg byte) byte {
	switch reg {
	case 0x19, 0x1A:
		return 0xFF
	case 0x1B:
		return byte(c.lastWave3 >> 4)
	case 0x1C:
		return byte(c.v[2].env)
	}
	return 0
}

// sidNoiseOut taps the shift register's eight output bits.
func sidNoiseOut(n uint32) uint32 {
	return (n>>9)&0x800 | (n>>8)&0x400 | (n>>5)&0x200 | (n>>3)&0x100 |
		(n>>2)&0x080 | (n<<1)&0x040 | (n<<3)&0x020 | (n<<4)&0x010
}

// wave is a voice's 12-bit waveform output; ringMSB is the top bit of the
// voice ring modulation takes from.
func (v *sidVoice) wave(ringMSB bool) uint32 {
	a := v.acc >> 8
	out, on := uint32(0xFFF), false
	if v.ctrl&sidTri != 0 {
		msb := a&0x800000 != 0
		if v.ctrl&sidRing != 0 && ringMSB {
			msb = !msb
		}
		t := a
		if msb {
			t = ^a
		}
		out &= (t >> 11) & 0xFFF
		on = true
	}
	if v.ctrl&sidSaw != 0 {
		out &= a >> 12
		on = true
	}
	if v.ctrl&sidPulse != 0 {
		if v.ctrl&sidTest == 0 && a>>12 < v.pw {
			out = 0
		}
		on = true
	}
	if v.ctrl&sidNoise != 0 {
		out &= sidNoiseOut(v.noise)
		on = true
	}
	if !on {
		return 0x800 // silence sits at the waveform's middle, not at a DC step
	}
	return out
}

// envelope advances a voice's ADSR by one sample's worth of cycles.
func (v *sidVoice) envelope(cps float32) {
	var period float32
	switch v.state {
	case sidAttack:
		period = sidRatePeriods[v.ad>>4]
	case sidDecay:
		period = sidRatePeriods[v.ad&0x0F]
	default:
		period = sidRatePeriods[v.sr&0x0F]
	}
	v.rateCnt += cps
	for v.rateCnt >= period {
		v.rateCnt -= period
		switch v.state {
		case sidAttack:
			v.env++
			if v.env >= 0xFF {
				v.env = 0xFF
				v.state = sidDecay
				v.expCnt = 0
				return
			}
		case sidDecay, sidRelease:
			floor := int32(0)
			if v.state == sidDecay {
				floor = int32(v.sr>>4) * 0x11
			}
			if v.env <= floor {
				v.rateCnt = 0
				return
			}
			v.expCnt++
			if v.expCnt >= sidExpPeriod(v.env) {
				v.expCnt = 0
				v.env--
			}
		}
	}
}

// sidExpPeriod is the exponential counter's divider at an envelope level:
// the decay and release slow down as the level falls.
func sidExpPeriod(env int32) int32 {
	switch {
	case env > 0x5D:
		return 1
	case env > 0x36:
		return 2
	case env > 0x1A:
		return 4
	case env > 0x0E:
		return 8
	case env > 0x06:
		return 16
	}
	return 30
}

// filterCoefficients turns the cutoff and resonance registers into the
// state-variable filter's coefficients.
func (c *sidChip) filterCoefficients() {
	c.dirty = false
	x := float64(c.fc) / 2047
	var hz float64
	if c.m8580 {
		hz = 30 + x*12000
	} else {
		hz = 220 + 17800*math.Pow(x, 2.2)
	}
	if limit := float64(c.rate) * 0.45; hz > limit {
		hz = limit
	}
	g := math.Tan(math.Pi * hz / float64(c.rate))
	k := 1.41 - float64(c.res)/15*0.9
	a1 := 1 / (1 + g*(g+k))
	c.fa1, c.fa2, c.fa3, c.fk = float32(a1), float32(g*a1), float32(g*g*a1), float32(k)
}

// sample steps the chip by one output sample and returns it. render false
// (seeking) moves the oscillators and envelopes without mixing anything.
func (c *sidChip) sample(render bool) int16 {
	// Oscillators: all three move first, so sync and ring see this step's edges.
	for i := range c.v {
		v := &c.v[i]
		if v.ctrl&sidTest != 0 {
			v.msbUp = false
			continue
		}
		step := uint64(v.freq) * uint64(c.cps256)
		old := uint64(v.acc)
		next := old + step
		// Noise is clocked on each rise of bit 19 of the 24-bit phase (bit 27
		// here, under the fraction).
		lo, hi := (old+sidBit19)>>28, (next+sidBit19)>>28
		if rises := hi - lo; rises > 0 {
			if rises > 4 {
				rises = 4
			}
			for ; rises > 0; rises-- {
				bit := v.noise>>22&1 ^ v.noise>>17&1
				v.noise = (v.noise<<1 | bit) & 0x7FFFFF
			}
		}
		// The top bit rose if the step passed a 0x80000000 point.
		v.msbUp = (next+sidBit23)>>32 != (old+sidBit23)>>32
		v.acc = uint32(next)
	}
	for i := range c.v {
		v := &c.v[i]
		if v.ctrl&sidSync != 0 && c.v[(i+2)%3].msbUp {
			v.acc = 0
		}
		v.envelope(c.cps)
	}
	c.lastWave3 = c.v[2].wave(c.v[1].acc&(1<<31) != 0)
	if !render {
		return 0
	}
	if c.dirty {
		c.filterCoefficients()
	}
	var direct, filtered float32
	for i := range c.v {
		v := &c.v[i]
		w := c.lastWave3
		if i < 2 {
			w = v.wave(c.v[(i+2)%3].acc&(1<<31) != 0)
		}
		out := float32(int32(w)-0x800) * float32(v.env)
		switch {
		case c.filt&(1<<uint(i)) != 0:
			filtered += out
		case i == 2 && c.modeVol&0x80 != 0:
			// Voice 3 off: silent unless it goes through the filter.
		default:
			direct += out
		}
	}
	mix := direct
	if c.modeVol&0x70 != 0 || filtered != 0 {
		v3 := filtered - c.ic2
		v1 := c.fa1*c.ic1 + c.fa2*v3
		v2 := c.ic2 + c.fa2*c.ic1 + c.fa3*v3
		c.ic1 = 2*v1 - c.ic1
		c.ic2 = 2*v2 - c.ic2
		if c.modeVol&0x10 != 0 {
			mix += v2 // low pass
		}
		if c.modeVol&0x20 != 0 {
			mix += v1 // band pass
		}
		if c.modeVol&0x40 != 0 {
			mix += filtered - c.fk*v1 - v2 // high pass
		}
	}
	mix *= float32(c.modeVol&0x0F) / 15 / 72
	// A DC blocker (about 20 Hz): combined waveforms and the envelope steps
	// otherwise leave the output off centre.
	out := mix - c.dcIn + 0.997*c.dcOut
	c.dcIn, c.dcOut = mix, out
	switch {
	case out > 32767:
		return 32767
	case out < -32768:
		return -32768
	}
	return int16(out)
}
