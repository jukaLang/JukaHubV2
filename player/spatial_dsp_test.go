package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// spatialGolden is one vector written by player/spatialfx/jh_spatial_ref.py.
type spatialGolden struct {
	name     string
	fs       int
	mode     int
	strength float64
	room     int
	input    string
	frames   int
	want     [][2]float64
}

// loadSpatialGolden parses a golden file: two comment lines (the second holds
// "fs=… mode=… strength=… room=… input=… frames=…"), then one "L R" line per
// frame. A checkout with CRLF line endings reads the same.
func loadSpatialGolden(path string) (spatialGolden, error) {
	g := spatialGolden{name: strings.TrimSuffix(filepath.Base(path), ".txt")}
	data, err := os.ReadFile(path)
	if err != nil {
		return g, err
	}
	headerSeen := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if !strings.Contains(line, "fs=") {
				continue
			}
			for _, field := range strings.Fields(strings.TrimPrefix(line, "#")) {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				var perr error
				switch key {
				case "fs":
					g.fs, perr = strconv.Atoi(value)
				case "mode":
					g.mode, perr = strconv.Atoi(value)
				case "strength":
					g.strength, perr = strconv.ParseFloat(value, 64)
				case "room":
					g.room, perr = strconv.Atoi(value)
				case "input":
					g.input = value
				case "frames":
					g.frames, perr = strconv.Atoi(value)
				}
				if perr != nil {
					return g, fmt.Errorf("%s: header %s: %v", g.name, field, perr)
				}
			}
			headerSeen = true
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return g, fmt.Errorf("%s: bad line %q", g.name, line)
		}
		l, err1 := strconv.ParseFloat(parts[0], 64)
		r, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			return g, fmt.Errorf("%s: bad numbers in %q", g.name, line)
		}
		g.want = append(g.want, [2]float64{l, r})
	}
	if !headerSeen || g.fs <= 0 || g.frames <= 0 {
		return g, fmt.Errorf("%s: no fs/frames header", g.name)
	}
	if len(g.want) != g.frames {
		return g, fmt.Errorf("%s: %d frames listed, header says %d", g.name, len(g.want), g.frames)
	}
	return g, nil
}

// spatialGoldenInput regenerates the reference's inputs: an impulse on one side,
// or two LCG noise channels plus a centred 440 Hz tone (lcg_noise).
func spatialGoldenInput(kind string, n int, fs float64) ([][2]float64, error) {
	in := make([][2]float64, n)
	switch kind {
	case "impL":
		if n > 0 {
			in[0][0] = 1
		}
	case "impR":
		if n > 0 {
			in[0][1] = 1
		}
	case "noise":
		state := uint64(12345)
		for i := 0; i < n; i++ {
			state = (1103515245*state + 12345) & 0x7FFFFFFF
			a := float64(state)/2147483647.0 - 0.5
			state = (1103515245*state + 12345) & 0x7FFFFFFF
			b := float64(state)/2147483647.0 - 0.5
			tone := 0.6 * math.Sin(2.0*math.Pi*440.0*float64(i)/fs)
			in[i] = [2]float64{a + tone, b + tone}
		}
	default:
		return nil, fmt.Errorf("unknown input %q", kind)
	}
	return in, nil
}

// The Go DSP is the reference's port: every golden vector must hold within
// 1e-5. The vectors carry 7 significant digits (a rounding of up to 5e-7 at
// full scale) and the port agrees to within that; a looser tolerance let a
// changed room constant or normalisation point slip through.
func TestSpatialDSPMatchesReference(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "spatial", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 7 {
		t.Fatalf("want the 7 golden vectors in testdata/spatial (jh_spatial_ref.py writes them), found %d", len(files))
	}
	const tol = 1e-5
	for _, path := range files {
		g, err := loadSpatialGolden(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(g.name, func(t *testing.T) {
			in, err := spatialGoldenInput(g.input, g.frames, float64(g.fs))
			if err != nil {
				t.Fatal(err)
			}
			d := newSpatialDSP(g.fs, g.mode, g.strength, g.room)
			for i := range in {
				l, r := d.process(in[i][0], in[i][1])
				if math.Abs(l-g.want[i][0]) > tol || math.Abs(r-g.want[i][1]) > tol {
					t.Fatalf("frame %d: got (%.7g, %.7g), want (%.7g, %.7g)", i, l, r, g.want[i][0], g.want[i][1])
				}
			}
		})
	}
}

func TestSpatialLimitStaysInside(t *testing.T) {
	for _, x := range []float64{0, 0.5, 0.9, -0.9, 0.95, 1, 1.5, 10, 1e9, -1e9, -2} {
		y := spatialLimit(x)
		if math.Abs(x) <= spatialLimitKnee {
			if y != x {
				t.Fatalf("limit(%v) = %v; below the knee it must be the identity", x, y)
			}
			continue
		}
		if !(math.Abs(y) < 1) || math.Abs(y) < spatialLimitKnee {
			t.Fatalf("limit(%v) = %v; must stay in [knee, 1)", x, y)
		}
		if (x > 0) != (y > 0) {
			t.Fatalf("limit(%v) = %v changed the sign", x, y)
		}
		if spatialLimit(-x) != -y {
			t.Fatalf("limit is not odd at %v", x)
		}
	}
	// Loud input saturates math.Tanh to exactly 1, and 0.9 + 0.1*1 is exactly
	// 1.0: the cap (the reference's LIMIT_CEIL) is what keeps it below.
	for _, x := range []float64{3, 10, 1e9, math.Inf(1)} {
		if got := spatialLimit(x); got != spatialLimitCeil || spatialLimit(-x) != -spatialLimitCeil {
			t.Fatalf("limit(±%v) = %v, want ±%v", x, got, spatialLimitCeil)
		}
	}
	if !(spatialLimitCeil < 1) {
		t.Fatal("the limiter's cap must be below full scale")
	}
}

func TestSpatialOffAndZeroStrengthAreIdentity(t *testing.T) {
	for _, d := range []*spatialDSP{
		newSpatialDSP(48000, spatialDSPOff, 0.75, spatialDSPRoomMedium),
		newSpatialDSP(48000, spatialDSPHeadphones, 0, spatialDSPRoomMedium),
		newSpatialDSP(48000, spatialDSPSpeakers, 0, spatialDSPRoomMedium),
	} {
		if d.active() {
			t.Fatalf("mode %d strength %v reports active", d.mode, d.strength)
		}
		for _, in := range [][2]float64{{0.25, -0.5}, {1.2, -1.3}, {0, 0}} {
			l, r := d.process(in[0], in[1])
			if l != in[0] || r != in[1] {
				t.Fatalf("mode %d strength %v changed %v to (%v, %v)", d.mode, d.strength, in, l, r)
			}
		}
	}
}

// spatialTestNoise is a deterministic stereo signal at the given level.
func spatialTestNoise(n int, level float64) [][2]float64 {
	in, _ := spatialGoldenInput("noise", n, 48000)
	for i := range in {
		in[i][0] *= level
		in[i][1] *= level
	}
	return in
}

func TestSpatialResetMatchesAFreshInstance(t *testing.T) {
	for _, mode := range []int{spatialDSPHeadphones, spatialDSPSpeakers} {
		used := newSpatialDSP(48000, mode, 0.75, spatialDSPRoomLarge)
		for _, f := range spatialTestNoise(3000, 0.5) {
			used.process(f[0], f[1])
		}
		used.reset()
		fresh := newSpatialDSP(48000, mode, 0.75, spatialDSPRoomLarge)
		for i, f := range spatialTestNoise(2500, 0.3) {
			ul, ur := used.process(f[0], f[1])
			fl, fr := fresh.process(f[0], f[1])
			if ul != fl || ur != fr {
				t.Fatalf("mode %d frame %d after reset: (%v, %v), fresh (%v, %v)", mode, i, ul, ur, fl, fr)
			}
		}
	}
}

func TestSpatialSetStrengthMatchesAFreshInstance(t *testing.T) {
	changed := newSpatialDSP(44100, spatialDSPHeadphones, 0.25, spatialDSPRoomSmall)
	changed.setStrength(0.9)
	fresh := newSpatialDSP(44100, spatialDSPHeadphones, 0.9, spatialDSPRoomSmall)
	if changed.makeup != fresh.makeup || changed.gContra != fresh.gContra || changed.gER != fresh.gER {
		t.Fatalf("setStrength gains differ from a fresh instance: %+v vs %+v", changed, fresh)
	}
	for i, f := range spatialTestNoise(1500, 0.4) {
		cl, cr := changed.process(f[0], f[1])
		fl, fr := fresh.process(f[0], f[1])
		if cl != fl || cr != fr {
			t.Fatalf("frame %d: (%v, %v) vs (%v, %v)", i, cl, cr, fl, fr)
		}
	}
	for _, s := range []float64{-1, math.NaN(), 7} {
		changed.setStrength(s)
		if changed.strength < 0 || changed.strength > 1 || changed.strength != changed.strength {
			t.Fatalf("setStrength(%v) left strength %v", s, changed.strength)
		}
	}
}

// spatialTestS16 packs float frames as interleaved little-endian S16.
func spatialTestS16(frames [][2]float64) []byte {
	out := make([]byte, 0, len(frames)*4)
	for _, f := range frames {
		for _, v := range f {
			s := uint16(spatialToS16(v))
			out = append(out, byte(s), byte(s>>8))
		}
	}
	return out
}

func spatialTestSample(b []byte, i int) int16 {
	return int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
}

// A stream split at any byte boundary (the music decoder reads a pipe) must
// come out exactly as the same stream processed whole.
func TestSpatialS16SplitStreamMatchesWhole(t *testing.T) {
	pcm := spatialTestS16(spatialTestNoise(1200, 0.5))
	whole := append([]byte(nil), newSpatialDSP(48000, spatialDSPHeadphones, 0.75, spatialDSPRoomMedium).processS16(pcm, 0.8)...)
	if len(whole) != len(pcm) {
		t.Fatalf("whole stream: %d bytes out of %d", len(whole), len(pcm))
	}
	d := newSpatialDSP(48000, spatialDSPHeadphones, 0.75, spatialDSPRoomMedium)
	var got []byte
	sizes := []int{1, 3, 5, 7, 2, 4, 1, 1, 1, 1, 13, 999, 6}
	pos := 0
	for i := 0; pos < len(pcm); i++ {
		n := sizes[i%len(sizes)]
		if pos+n > len(pcm) {
			n = len(pcm) - pos
		}
		got = append(got, d.processS16(pcm[pos:pos+n], 0.8)...)
		pos += n
	}
	if d.carryN != 0 {
		t.Fatalf("%d bytes left in the carry after a whole number of frames", d.carryN)
	}
	if string(got) != string(whole) {
		t.Fatal("split stream differs from the whole stream")
	}
}

func TestSpatialS16VolumeAndClamping(t *testing.T) {
	quiet := spatialTestS16(spatialTestNoise(800, 0.05))
	full := append([]byte(nil), newSpatialDSP(48000, spatialDSPHeadphones, 1, spatialDSPRoomMedium).processS16(quiet, 1)...)
	half := newSpatialDSP(48000, spatialDSPHeadphones, 1, spatialDSPRoomMedium).processS16(quiet, 0.5)
	for i := 0; i < len(full)/2; i++ {
		want := float64(spatialTestSample(full, i)) / 2
		if got := float64(spatialTestSample(half, i)); math.Abs(got-want) > 1.01 {
			t.Fatalf("sample %d at half volume: %v, want about %v", i, got, want)
		}
	}
	muted := newSpatialDSP(48000, spatialDSPSpeakers, 1, spatialDSPRoomMedium).processS16(quiet, 0)
	for i := 0; i < len(muted)/2; i++ {
		if spatialTestSample(muted, i) != 0 {
			t.Fatalf("sample %d is not silent at volume 0", i)
		}
	}
	// Full-scale square waves never wrap round: every S16 sample is the float
	// path's limited output, rounded, for the same (quantised) input.
	square := make([][2]float64, 2000)
	for i := range square {
		v := 1.0
		if (i/40)%2 == 1 {
			v = -1
		}
		square[i] = [2]float64{v, -v}
	}
	pcm := spatialTestS16(square)
	for _, mode := range []int{spatialDSPHeadphones, spatialDSPSpeakers} {
		out := newSpatialDSP(48000, mode, 1, spatialDSPRoomLarge).processS16(pcm, 1)
		ref := newSpatialDSP(48000, mode, 1, spatialDSPRoomLarge)
		for i := 0; i < len(out)/4; i++ {
			xl := float64(spatialTestSample(pcm, 2*i)) / spatialS16Scale
			xr := float64(spatialTestSample(pcm, 2*i+1)) / spatialS16Scale
			yl, yr := ref.process(xl, xr)
			for ch, y := range []float64{yl, yr} {
				got := float64(spatialTestSample(out, 2*i+ch)) / spatialS16Scale
				if math.Abs(got-y) > 1.5/spatialS16Scale {
					t.Fatalf("mode %d frame %d ch %d: S16 %v, float path %v", mode, i, ch, got, y)
				}
			}
		}
	}
}

func TestSpatialToS16(t *testing.T) {
	cases := []struct {
		in   float64
		want int16
	}{
		{0, 0}, {0.5, 16384}, {-0.5, -16384}, {1, 32767}, {-1, -32768}, {2, 32767}, {-2, -32768},
		{1.0 / 32768, 1}, {-1.0 / 32768, -1}, {math.NaN(), 0},
	}
	for _, c := range cases {
		if got := spatialToS16(c.in); got != c.want {
			t.Fatalf("spatialToS16(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSpatialS16CarryAndReset(t *testing.T) {
	d := newSpatialDSP(48000, spatialDSPHeadphones, 0.75, spatialDSPRoomMedium)
	out := d.processS16([]byte{1, 2, 3, 4, 5, 6}, 1)
	if len(out) != 4 || d.carryN != 2 {
		t.Fatalf("6 bytes in: %d out, %d carried; want 4 and 2", len(out), d.carryN)
	}
	// Two more bytes complete the held frame.
	if out := d.processS16([]byte{7, 8}, 1); len(out) != 4 || d.carryN != 0 {
		t.Fatalf("2 bytes after 2 held: %d out, %d carried; want 4 and 0", len(out), d.carryN)
	}
	// Fewer than a frame in total: nothing comes out, everything is held.
	if out := d.processS16([]byte{1}, 1); len(out) != 0 || d.carryN != 1 {
		t.Fatalf("1 byte: %d out, %d carried; want 0 and 1", len(out), d.carryN)
	}
	d.reset()
	if d.carryN != 0 {
		t.Fatal("reset must drop a held partial frame")
	}
}

// Every rate the video device may open at builds a working DSP whose output
// stays finite and inside the limiter.
func TestSpatialRatesStayFinite(t *testing.T) {
	for _, fs := range []int{22050, 32000, 44100, 48000, 96000} {
		for _, room := range []int{spatialDSPRoomSmall, spatialDSPRoomMedium, spatialDSPRoomLarge} {
			d := newSpatialDSP(fs, spatialDSPHeadphones, 1, room)
			for i, f := range spatialTestNoise(fs/10, 0.9) {
				l, r := d.process(f[0], f[1])
				if math.IsNaN(l) || math.IsNaN(r) || math.Abs(l) > 1 || math.Abs(r) > 1 {
					t.Fatalf("fs %d room %d frame %d: (%v, %v)", fs, room, i, l, r)
				}
			}
		}
	}
}
