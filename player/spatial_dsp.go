package main

// spatial_dsp.go — the virtual-surround DSP for what JukaHub itself plays.
//
// This is a float64 port of player/spatialfx/jh_spatial_ref.py, which is the
// specification: the same constants, the same coefficient formulas and the
// same order of operations in every frame, so the golden vectors that script
// writes (player/testdata/spatial/*.txt) hold for this code too
// (spatial_dsp_test.go). The C build of the same DSP that RetroArch games use
// (player/spatialfx/jukahub_spatial.c) is checked against the same vectors.
//
// The model, in short (the reference has the long version):
//
//   - Headphones: the stereo input plays through four virtual loudspeakers —
//     a front pair at ±30° fed with L and R, and a rear pair at ±110° fed with
//     the band-limited, decorrelated side signal 15 ms later, so the front
//     image stays in front. Every loudspeaker reaches each ear through a
//     spherical-head model (Brown & Duda): a one-pole/one-zero head-shadow
//     filter and the head's path delay, with an extra low-pass on the far ear.
//     Six early reflections per ear and a 4-line feedback delay network add
//     the small room that moves the sound out of the head.
//   - Speakers: binaural cues do not survive two speakers a few centimetres
//     apart, so this mode only widens the side signal above 300 Hz.
//   - A stateless soft limiter keeps every sample inside (-1, 1): a tanh knee,
//     capped at the reference's LIMIT_CEIL (0.99999994), because in floating
//     point the curve itself rounds to exactly 1.0 for loud enough input.
//
// One deviation from the reference, far below anything audible or testable:
// the recursive states are flushed to zero once they fall under 1e-30
// (spatialFlush), so a decaying reverb tail never runs on denormal numbers,
// which cost x86 desktops a hundred cycles an operation. The difference to the
// reference is under 1e-30 per sample.
//
// Nothing here allocates once a DSP is built: the S16 path writes into a
// buffer the DSP keeps (out) and reuses on the next call.

import "math"

// The DSP modes (the reference's MODE_*).
const (
	spatialDSPOff        = 0
	spatialDSPHeadphones = 1
	spatialDSPSpeakers   = 2
)

// The virtual rooms (the reference's ROOM_*).
const (
	spatialDSPRoomSmall  = 0
	spatialDSPRoomMedium = 1
	spatialDSPRoomLarge  = 2
)

// Constants — normative, from jh_spatial_ref.py.
const (
	spatialHeadRadius    = 0.0875 // metres
	spatialSoundSpeed    = 343.0  // metres per second
	spatialAlphaMin      = 0.1    // head-shadow HF gain at spatialThetaMin
	spatialThetaMin      = 150.0  // degrees
	spatialFrontAz       = 30.0   // front pair at -/+ this many degrees
	spatialRearAz        = 110.0  // rear pair at -/+ this many degrees
	spatialRearDelayMs   = 15.0
	spatialRearHPFHz     = 120.0
	spatialRearLPFHz     = 7000.0
	spatialContraLPFHz   = 1500.0 // extra low-pass on every far-ear path
	spatialAPGain        = 0.5
	spatialERLPFHz       = 6000.0
	spatialFDNDampHz     = 4000.0
	spatialFDNPredelayMs = 20.0
	spatialSideHPFHz     = 300.0 // speakers mode
	spatialLimitKnee     = 0.9
	spatialLimitCeil     = 0.99999994 // the limiter's largest output magnitude
	spatialNormLoHz      = 100.0      // loudness normalisation band ...
	spatialNormHiHz      = 8000.0
	spatialNormPoints    = 24 // ... sampled at this many log-spaced points
	// spatialDenormal is where a recursive state is flushed to zero.
	spatialDenormal = 1e-30
)

// spatialRoomSpec is one virtual room: time scale, early-reflection gain and
// RT60 in seconds.
type spatialRoomSpec struct {
	scale, erGain, rt60 float64
}

// spatialERSpec is one early reflection: delay in ms, gain, and the weights of
// the left and right inputs.
type spatialERSpec struct {
	ms, gain, wl, wr float64
}

var (
	spatialRooms = [3]spatialRoomSpec{
		{0.6, 0.8, 0.25}, // small
		{1.0, 1.0, 0.40}, // medium
		{1.5, 1.0, 0.70}, // large
	}
	spatialAPMsLeft  = [2]float64{3.1, 7.3}
	spatialAPMsRight = [2]float64{4.7, 5.9}
	spatialERLeft    = [6]spatialERSpec{
		{7.1, 0.35, 0.8, 0.2}, {11.3, -0.28, 0.3, 0.7}, {17.9, 0.22, 0.7, 0.3},
		{23.5, -0.17, 0.4, 0.6}, {31.1, 0.13, 0.6, 0.4}, {38.9, -0.10, 0.5, 0.5},
	}
	spatialERRight = [6]spatialERSpec{
		{7.9, 0.35, 0.2, 0.8}, {12.7, -0.28, 0.7, 0.3}, {16.1, 0.22, 0.3, 0.7},
		{25.3, -0.17, 0.6, 0.4}, {29.7, 0.13, 0.4, 0.6}, {41.3, -0.10, 0.5, 0.5},
	}
	spatialFDNMs = [4]float64{29.7, 37.1, 41.1, 43.7}
	spatialFDNIn = [4]float64{0.5, -0.5, 0.5, -0.5}
)

// spatialIRound is the reference's iround: floor(x + 0.5), never banker's
// rounding.
func spatialIRound(x float64) int {
	return int(math.Floor(x + 0.5))
}

// spatialHeadAlpha is the Brown-Duda head-shadow HF gain for the angle between
// the source and the ear axis.
func spatialHeadAlpha(thetaEarDeg float64) float64 {
	return (1.0 + spatialAlphaMin/2.0) + (1.0-spatialAlphaMin/2.0)*math.Cos(thetaEarDeg/spatialThetaMin*math.Pi)
}

// spatialHeadDelay is the path delay to the ear in seconds, offset so it is
// never negative.
func spatialHeadDelay(thetaEarDeg float64) float64 {
	ac := spatialHeadRadius / spatialSoundSpeed
	if thetaEarDeg < 90.0 {
		return ac * (1.0 - math.Cos(thetaEarDeg*math.Pi/180.0))
	}
	return ac * (1.0 + (thetaEarDeg-90.0)*math.Pi/180.0)
}

// spatialEarAngle is the angle between a source at az and the ear axis at
// earAz, in [0, 180].
func spatialEarAngle(azDeg, earAzDeg float64) float64 {
	d := math.Mod(math.Abs(azDeg-earAzDeg), 360.0)
	if d > 180.0 {
		d = 360.0 - d
	}
	return d
}

// spatialShadowCoeffs is the bilinear one-pole/one-zero head-shadow filter
// H(s) = (2*w0 + alpha*s) / (2*w0 + s), w0 = c/a, as
// y[n] = b0*x[n] + b1*x[n-1] - a1*y[n-1].
func spatialShadowCoeffs(alpha, fs float64) (b0, b1, a1 float64) {
	w0 := spatialSoundSpeed / spatialHeadRadius
	k := 2.0 * fs
	d0 := 2.0*w0 + k
	b0 = (2.0*w0 + alpha*k) / d0
	b1 = (2.0*w0 - alpha*k) / d0
	a1 = (2.0*w0 - k) / d0
	return b0, b1, a1
}

// spatialPole is the one-pole coefficient p = exp(-2*pi*fc/fs).
func spatialPole(fc, fs float64) float64 {
	return math.Exp(-2.0 * math.Pi * fc / fs)
}

// spatialFlush returns 0 for a value too small to matter (see the file
// comment), so recursive states never decay into denormal numbers.
func spatialFlush(x float64) float64 {
	if x > -spatialDenormal && x < spatialDenormal {
		return 0
	}
	return x
}

// spatialLimit is the stateless soft limiter: identity up to the knee, then a
// tanh curve capped at spatialLimitCeil. The cap is not decoration: math.Tanh
// returns exactly 1 once its argument is large (and 0.9 + 0.1*1 is exactly
// 1.0), so without it a loud enough sample would leave at full scale.
func spatialLimit(x float64) float64 {
	// The reference and the C port answer 0 for NaN; S16 input cannot make
	// one today, but the three implementations keep the same rule.
	if math.IsNaN(x) {
		return 0
	}
	a := math.Abs(x)
	if a <= spatialLimitKnee {
		return x
	}
	y := spatialLimitKnee + (1.0-spatialLimitKnee)*math.Tanh((a-spatialLimitKnee)/(1.0-spatialLimitKnee))
	if y > spatialLimitCeil {
		y = spatialLimitCeil
	}
	if x > 0 {
		return y
	}
	return -y
}

// spatialLine is a circular delay line: written first, then read, so tap(0)
// is the sample written in the same frame.
type spatialLine struct {
	buf []float64
	w   int
}

func newSpatialLine(n int) spatialLine {
	if n < 1 {
		n = 1
	}
	return spatialLine{buf: make([]float64, n)}
}

func (l *spatialLine) write(x float64) {
	l.w++
	if l.w == len(l.buf) {
		l.w = 0
	}
	l.buf[l.w] = x
}

func (l *spatialLine) tap(i int) float64 {
	j := l.w - i
	if j < 0 {
		j += len(l.buf)
	}
	return l.buf[j]
}

// frac reads a fractional delay with linear interpolation:
// (1 - f) * tap(i) + f * tap(i + 1), i = floor(d), f = d - i.
func (l *spatialLine) frac(d float64) float64 {
	i := int(math.Floor(d))
	f := d - float64(i)
	return (1.0-f)*l.tap(i) + f*l.tap(i+1)
}

// clear empties the line the way a freshly made one starts.
func (l *spatialLine) clear() {
	for i := range l.buf {
		l.buf[i] = 0
	}
	l.w = 0
}

// spatialAllpass is a Schroeder allpass in direct form, D >= 1 samples:
// y[n] = -g*x[n] + x[n-D] + g*y[n-D], over two lines of length D+1.
type spatialAllpass struct {
	d    int
	x, y spatialLine
}

func newSpatialAllpass(d int) spatialAllpass {
	if d < 1 {
		d = 1
	}
	return spatialAllpass{d: d, x: newSpatialLine(d + 1), y: newSpatialLine(d + 1)}
}

func (a *spatialAllpass) process(v float64) float64 {
	a.x.write(v)
	// y is written last, so before the write y.tap(d-1) is y[n-D].
	out := -spatialAPGain*v + a.x.tap(a.d) + spatialAPGain*a.y.tap(a.d-1)
	out = spatialFlush(out)
	a.y.write(out)
	return out
}

func (a *spatialAllpass) clear() {
	a.x.clear()
	a.y.clear()
}

// spatialPath is one loudspeaker-to-ear path: delay in samples and the
// head-shadow filter's coefficients.
type spatialPath struct {
	delay      float64
	b0, b1, a1 float64
}

// spatialTap is one early reflection after the room scaled it.
type spatialTap struct {
	d            int
	gain, wl, wr float64
}

// spatialDSP is one running instance at one sample rate, mode and room.
type spatialDSP struct {
	fs       float64
	mode     int
	room     int
	strength float64

	erRoomGain float64
	// paths[k][e]: loudspeaker k (FL, FR, RL, RR) to ear e (0 left, 1 right).
	paths    [4][2]spatialPath
	spk      [4]spatialLine
	shX1     [4][2]float64
	shY1     [4][2]float64
	pContra  float64
	contraLP [4][2]float64

	pRearHP, pRearLP float64
	rearHP, rearLP   float64
	apLeft, apRight  [2]spatialAllpass

	erLeft, erRight [6]spatialTap
	predelay        int
	erLines         [2]spatialLine
	pER             float64
	erLP            [2]float64

	fdnLen   [4]int
	fdnGain  [4]float64
	fdnLines [4]spatialLine
	pDamp    float64
	fdnZ     [4]float64

	pSide  float64
	sideLP float64

	// Strength-derived gains (setStrength).
	gContra, gRear, gER, gRev, width, makeup float64

	// The S16 path: a partial stereo frame left over from the last call, and
	// the output buffer it hands back.
	carry  [4]byte
	carryN int
	out    []byte
}

// newSpatialDSP builds an instance. mode and room out of range fall back to
// off and medium; strength is clamped to [0, 1].
func newSpatialDSP(fs int, mode int, strength float64, room int) *spatialDSP {
	if fs <= 0 {
		fs = 48000
	}
	if mode < spatialDSPOff || mode > spatialDSPSpeakers {
		mode = spatialDSPOff
	}
	if room < spatialDSPRoomSmall || room > spatialDSPRoomLarge {
		room = spatialDSPRoomMedium
	}
	d := &spatialDSP{fs: float64(fs), mode: mode, room: room}
	d.configure()
	d.setStrength(strength)
	return d
}

// configure computes the coefficients and makes the delay lines (the
// reference's configure()).
func (d *spatialDSP) configure() {
	fs := d.fs
	room := spatialRooms[d.room]
	d.erRoomGain = room.erGain
	// Virtual loudspeakers in the order FL, FR, RL, RR: azimuth and extra delay.
	speakers := [4][2]float64{
		{-spatialFrontAz, 0.0},
		{spatialFrontAz, 0.0},
		{-spatialRearAz, spatialRearDelayMs / 1000.0},
		{spatialRearAz, spatialRearDelayMs / 1000.0},
	}
	earAz := [2]float64{-90.0, 90.0} // left ear, then right ear
	var theta, delay [4][2]float64
	tmin := math.Inf(1)
	for k := 0; k < 4; k++ {
		for e := 0; e < 2; e++ {
			th := spatialEarAngle(speakers[k][0], earAz[e])
			t := spatialHeadDelay(th) + speakers[k][1]
			theta[k][e] = th
			delay[k][e] = t
			if t < tmin {
				tmin = t
			}
		}
	}
	maxd := 0.0
	for k := 0; k < 4; k++ {
		for e := 0; e < 2; e++ {
			b0, b1, a1 := spatialShadowCoeffs(spatialHeadAlpha(theta[k][e]), fs)
			dd := (delay[k][e] - tmin) * fs
			d.paths[k][e] = spatialPath{delay: dd, b0: b0, b1: b1, a1: a1}
			if dd > maxd {
				maxd = dd
			}
		}
	}
	for k := 0; k < 4; k++ {
		d.spk[k] = newSpatialLine(int(math.Floor(maxd)) + 3)
	}
	d.pContra = spatialPole(spatialContraLPFHz, fs)
	// Rear feed: band-limit, then decorrelate each side.
	d.pRearHP = spatialPole(spatialRearHPFHz, fs)
	d.pRearLP = spatialPole(spatialRearLPFHz, fs)
	for i := 0; i < 2; i++ {
		d.apLeft[i] = newSpatialAllpass(spatialIRound(spatialAPMsLeft[i] * fs / 1000.0))
		d.apRight[i] = newSpatialAllpass(spatialIRound(spatialAPMsRight[i] * fs / 1000.0))
	}
	// Early reflections: integer taps scaled with the room.
	erMax := 0
	for i := 0; i < 6; i++ {
		l := spatialERLeft[i]
		r := spatialERRight[i]
		d.erLeft[i] = spatialTap{d: spatialIRound(l.ms * room.scale * fs / 1000.0), gain: l.gain, wl: l.wl, wr: l.wr}
		d.erRight[i] = spatialTap{d: spatialIRound(r.ms * room.scale * fs / 1000.0), gain: r.gain, wl: r.wl, wr: r.wr}
		if d.erLeft[i].d > erMax {
			erMax = d.erLeft[i].d
		}
		if d.erRight[i].d > erMax {
			erMax = d.erRight[i].d
		}
	}
	d.predelay = spatialIRound(spatialFDNPredelayMs * room.scale * fs / 1000.0)
	if d.predelay > erMax {
		erMax = d.predelay
	}
	d.erLines[0] = newSpatialLine(erMax + 2)
	d.erLines[1] = newSpatialLine(erMax + 2)
	d.pER = spatialPole(spatialERLPFHz, fs)
	// Late reverb: 4 lines, Householder feedback, damped.
	for i := 0; i < 4; i++ {
		n := spatialIRound(spatialFDNMs[i] * room.scale * fs / 1000.0)
		if n < 1 {
			n = 1
		}
		d.fdnLen[i] = n
		d.fdnGain[i] = math.Pow(10.0, -3.0*float64(n)/(room.rt60*fs))
		d.fdnLines[i] = newSpatialLine(n)
	}
	d.pDamp = spatialPole(spatialFDNDampHz, fs)
	// Speakers mode.
	d.pSide = spatialPole(spatialSideHPFHz, fs)
	d.clearFilterState()
}

// setStrength scales the effect (0..1) in place: the reference's
// set_strength, including the loudness make-up gain.
func (d *spatialDSP) setStrength(s float64) {
	if !(s > 0) { // NaN or <= 0
		s = 0
	}
	if s > 1 {
		s = 1
	}
	d.strength = s
	d.gContra = 0.6 * s
	d.gRear = 0.4 * s
	d.gER = 0.6 * s * d.erRoomGain
	d.gRev = 0.25 * s
	d.width = 1.0 + 0.8*s
	d.makeup = d.loudnessMakeup()
}

// pathResponse is the complex response of the direct path k -> ear e at w
// radians per sample, including the far-ear low-pass.
func (d *spatialDSP) pathResponse(k, e int, w float64) complex128 {
	p := d.paths[k][e]
	z1 := complex(math.Cos(w), -math.Sin(w))
	h := (complex(p.b0, 0) + complex(p.b1, 0)*z1) / (complex(1.0, 0) + complex(p.a1, 0)*z1)
	h *= complex(math.Cos(w*p.delay), -math.Sin(w*p.delay))
	if k%2 != e { // far ear: FL->R, FR->L, RL->R, RR->L
		h *= complex(1.0-d.pContra, 0) / (complex(1.0, 0) - complex(d.pContra, 0)*z1)
	}
	return h
}

// loudnessMakeup is 1 / sqrt(mean power gain) of a centred signal: the direct
// paths over the normalisation band plus the room's energy.
func (d *spatialDSP) loudnessMakeup() float64 {
	acc := 0.0
	for i := 0; i < spatialNormPoints; i++ {
		f := spatialNormLoHz * math.Pow(spatialNormHiHz/spatialNormLoHz, float64(i)/(spatialNormPoints-1.0))
		w := 2.0 * math.Pi * f / d.fs
		// Left ear: FL near + FR far (a centred signal has no side, so no rears).
		h := d.pathResponse(0, 0, w) + complex(d.gContra, 0)*d.pathResponse(1, 0, w)
		mag := math.Hypot(real(h), imag(h))
		acc += mag * mag
	}
	direct := acc / spatialNormPoints
	erEnergy := 0.0
	for i := 0; i < 6; i++ {
		g := spatialERLeft[i].gain
		erEnergy += g * g
	}
	total := direct + d.gER*d.gER*erEnergy + d.gRev*d.gRev*0.5
	return 1.0 / math.Sqrt(total)
}

// clearFilterState zeroes every recursive state and delay line, which is
// exactly how a fresh instance starts (the reference's reset()).
func (d *spatialDSP) clearFilterState() {
	for k := 0; k < 4; k++ {
		d.spk[k].clear()
		for e := 0; e < 2; e++ {
			d.shX1[k][e] = 0
			d.shY1[k][e] = 0
			d.contraLP[k][e] = 0
		}
	}
	d.rearHP, d.rearLP = 0, 0
	for i := 0; i < 2; i++ {
		d.apLeft[i].clear()
		d.apRight[i].clear()
		d.erLines[i].clear()
		d.erLP[i] = 0
	}
	for i := 0; i < 4; i++ {
		d.fdnLines[i].clear()
		d.fdnZ[i] = 0
	}
	d.sideLP = 0
}

// reset clears the state and drops any partial S16 frame: a seek or a stop
// starts a new stream at a frame boundary.
func (d *spatialDSP) reset() {
	d.clearFilterState()
	d.carryN = 0
}

// active reports whether process changes anything.
func (d *spatialDSP) active() bool {
	return d.mode != spatialDSPOff && d.strength > 0
}

// process runs one stereo frame through the DSP and the limiter (the
// reference's process()).
func (d *spatialDSP) process(xl, xr float64) (float64, float64) {
	if !d.active() {
		return xl, xr
	}
	ol, or := d.processRaw(xl, xr)
	return spatialLimit(ol), spatialLimit(or)
}

// processRaw is process without the limiter, so the S16 path can apply the
// player volume first.
func (d *spatialDSP) processRaw(xl, xr float64) (float64, float64) {
	if d.mode == spatialDSPSpeakers {
		return d.processSpeakers(xl, xr)
	}
	return d.processHeadphones(xl, xr)
}

func (d *spatialDSP) processSpeakers(xl, xr float64) (float64, float64) {
	mid := 0.5 * (xl + xr)
	side := 0.5 * (xl - xr)
	d.sideLP = spatialFlush(d.sideLP + (1.0-d.pSide)*(side-d.sideLP))
	side2 := side + (d.width-1.0)*(side-d.sideLP)
	return mid + side2, mid - side2
}

func (d *spatialDSP) processHeadphones(xl, xr float64) (float64, float64) {
	// 1. Rear feed: side signal, high-passed, low-passed, decorrelated per side.
	side := 0.5 * (xl - xr)
	d.rearHP = spatialFlush(d.rearHP + (1.0-d.pRearHP)*(side-d.rearHP))
	hp := side - d.rearHP
	d.rearLP = spatialFlush(d.rearLP + (1.0-d.pRearLP)*(hp-d.rearLP))
	rl := d.rearLP
	rr := -d.rearLP
	for i := 0; i < 2; i++ {
		rl = d.apLeft[i].process(rl)
	}
	for i := 0; i < 2; i++ {
		rr = d.apRight[i].process(rr)
	}
	feeds := [4]float64{xl, xr, rl, rr}
	// 2. Virtual loudspeakers to the two ears.
	var ear [2]float64
	for k := 0; k < 4; k++ {
		line := &d.spk[k]
		line.write(feeds[k])
		for e := 0; e < 2; e++ {
			p := &d.paths[k][e]
			v := line.frac(p.delay)
			y := p.b0*v + p.b1*d.shX1[k][e] - p.a1*d.shY1[k][e]
			y = spatialFlush(y)
			d.shX1[k][e] = v
			d.shY1[k][e] = y
			near := k%2 == e
			if !near {
				d.contraLP[k][e] = spatialFlush(d.contraLP[k][e] + (1.0-d.pContra)*(y-d.contraLP[k][e]))
				y = d.contraLP[k][e]
			}
			var g float64
			switch {
			case k < 2 && near:
				g = 1.0
			case k < 2:
				g = d.gContra
			default:
				g = d.gRear
			}
			ear[e] += g * y
		}
	}
	// 3. Early reflections from the low-passed inputs.
	d.erLP[0] = spatialFlush(d.erLP[0] + (1.0-d.pER)*(xl-d.erLP[0]))
	d.erLP[1] = spatialFlush(d.erLP[1] + (1.0-d.pER)*(xr-d.erLP[1]))
	d.erLines[0].write(d.erLP[0])
	d.erLines[1].write(d.erLP[1])
	erL := 0.0
	for i := 0; i < 6; i++ {
		t := &d.erLeft[i]
		erL += t.gain * (t.wl*d.erLines[0].tap(t.d) + t.wr*d.erLines[1].tap(t.d))
	}
	erR := 0.0
	for i := 0; i < 6; i++ {
		t := &d.erRight[i]
		erR += t.gain * (t.wl*d.erLines[0].tap(t.d) + t.wr*d.erLines[1].tap(t.d))
	}
	// 4. Late reverb: read every line's oldest sample, damp, mix, write back.
	revIn := 0.5 * (d.erLines[0].tap(d.predelay) + d.erLines[1].tap(d.predelay))
	for i := 0; i < 4; i++ {
		y := d.fdnLines[i].tap(d.fdnLen[i] - 1)
		d.fdnZ[i] = spatialFlush(d.fdnZ[i] + (1.0-d.pDamp)*(y-d.fdnZ[i]))
	}
	zsum := d.fdnZ[0] + d.fdnZ[1] + d.fdnZ[2] + d.fdnZ[3]
	for i := 0; i < 4; i++ {
		fb := d.fdnZ[i] - 0.5*zsum
		d.fdnLines[i].write(spatialFlush(spatialFDNIn[i]*revIn + d.fdnGain[i]*fb))
	}
	revL := 0.5 * (d.fdnZ[0] + d.fdnZ[2])
	revR := 0.5 * (d.fdnZ[1] + d.fdnZ[3])
	// 5. Mix and normalise (the limiter is the caller's).
	outL := d.makeup * (ear[0] + d.gER*erL + d.gRev*revL)
	outR := d.makeup * (ear[1] + d.gER*erR + d.gRev*revR)
	return outL, outR
}

// ── The S16 path ──────────────────────────────────────────────────────────

// spatialS16Scale converts between S16 and the DSP's float range; the same
// factor both ways keeps unity gain exact.
const spatialS16Scale = 32768.0

// spatialToS16 rounds a float sample back to S16 with clamping.
func spatialToS16(y float64) int16 {
	if y != y { // NaN never comes out of a stable filter; never wrap on one
		return 0
	}
	v := math.Floor(y*spatialS16Scale + 0.5)
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// processS16 runs interleaved little-endian stereo S16 through the DSP at the
// given player volume (applied before the limiter, 0..1) and returns the frames
// to queue. The result is the DSP's own buffer, valid until the next call.
// pcm need not hold whole frames: up to three trailing bytes are kept and lead
// the next call's first frame, so the stream never slips a byte.
func (d *spatialDSP) processS16(pcm []byte, volume float64) []byte {
	if !(volume > 0) { // NaN or <= 0
		volume = 0
	}
	if volume > 1 {
		volume = 1
	}
	need := ((d.carryN + len(pcm)) / 4) * 4
	if cap(d.out) < need {
		d.out = make([]byte, need, need+need/4+64)
	}
	out := d.out[:need]
	n := 0
	i := 0
	if d.carryN > 0 {
		if d.carryN+len(pcm) < 4 {
			d.carryN += copy(d.carry[d.carryN:], pcm)
			return out[:0]
		}
		var frame [4]byte
		copy(frame[:], d.carry[:d.carryN])
		i = copy(frame[d.carryN:], pcm)
		d.carryN = 0
		d.frameS16(frame[:], out[0:4], volume)
		n = 4
	}
	for ; i+4 <= len(pcm); i += 4 {
		d.frameS16(pcm[i:i+4], out[n:n+4], volume)
		n += 4
	}
	d.carryN = copy(d.carry[:], pcm[i:])
	return out[:n]
}

// frameS16 processes one 4-byte frame from in into out.
func (d *spatialDSP) frameS16(in, out []byte, volume float64) {
	l := float64(int16(uint16(in[0])|uint16(in[1])<<8)) / spatialS16Scale
	r := float64(int16(uint16(in[2])|uint16(in[3])<<8)) / spatialS16Scale
	ol, or := d.processRaw(l, r)
	sl := uint16(spatialToS16(spatialLimit(ol * volume)))
	sr := uint16(spatialToS16(spatialLimit(or * volume)))
	out[0] = byte(sl)
	out[1] = byte(sl >> 8)
	out[2] = byte(sr)
	out[3] = byte(sr >> 8)
}
