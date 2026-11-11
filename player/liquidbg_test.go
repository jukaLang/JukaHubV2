package main

// The liquid background's arithmetic: grid sizing, the wave step's stability
// and decay, drops at the edges, the focus-ripple rules, the settings, the
// tilt maths and the baked picture. All of it is pure: no window, no renderer.

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestLiquidGridSize(t *testing.T) {
	cases := []struct {
		w, h       int32
		low        bool
		cols, rows int
	}{
		{1280, 720, false, 64, 36}, // Smart Pro
		{960, 720, false, 48, 36},  // Brick canvas
		{1024, 768, false, 51, 38},
		{1280, 720, true, 48, 27}, // Low Power
		{960, 720, true, 48, 36},  // already at the Low Power width
		{320, 240, false, 32, 24}, // never coarser than 32 columns
		{3840, 2160, false, 80, 45},
	}
	for _, c := range cases {
		cols, rows := liquidGridSize(c.w, c.h, c.low)
		if cols != c.cols || rows != c.rows {
			t.Errorf("%dx%d low=%v: got %dx%d, want %dx%d", c.w, c.h, c.low, cols, rows, c.cols, c.rows)
		}
	}
	if cols, rows := liquidGridSize(0, 720, false); cols != 0 || rows != 0 {
		t.Errorf("an empty canvas got a %dx%d grid", cols, rows)
	}
}

// liquidPeak is the largest |height| anywhere, and whether every node is a
// finite number.
func liquidPeak(s *liquidSim) (float32, bool) {
	var peak float32
	for i := range s.cur {
		v := s.cur[i]
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return 0, false
		}
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak, true
}

func TestLiquidDropDecaysToCalm(t *testing.T) {
	for _, damp := range []float32{0.984, 0.988, 0.975} {
		s := newLiquidSim(64, 36)
		s.drop(32, 18, 4, 1.0)
		for n := 0; n < 2000; n++ {
			s.step(damp)
			peak, finite := liquidPeak(s)
			if !finite {
				t.Fatalf("damp %v: a node is not a number after %d steps", damp, n+1)
			}
			if peak > liquidMaxHeight {
				t.Fatalf("damp %v: peak %v over the clamp after %d steps", damp, peak, n+1)
			}
		}
		if s.maxAbs >= liquidCalmLevel {
			t.Errorf("damp %v: still moving after 2000 steps (peak %v)", damp, s.maxAbs)
		}
	}
}

func TestLiquidStepStableWithoutDamping(t *testing.T) {
	// The leapfrog step is stable for k ≤ 0.5. An unstable one grows
	// exponentially and would sit on the clamp long before 2000 steps.
	s := newLiquidSim(48, 27)
	s.drop(20, 12, 3, 0.3)
	for n := 0; n < 2000; n++ {
		s.step(1)
	}
	peak, finite := liquidPeak(s)
	if !finite || peak > 1.5 {
		t.Fatalf("undamped water grew to %v (finite=%v)", peak, finite)
	}
	if peak == 0 {
		t.Fatal("the drop vanished without damping")
	}
}

func TestLiquidDropAtTheEdges(t *testing.T) {
	s := newLiquidSim(32, 18)
	s.drop(0, 0, 3, 1)
	s.drop(32, 18, 3, 1)
	s.drop(-50, -50, 3, 1) // wholly outside: nothing
	s.drop(16, 9, 0, 1)    // no radius: nothing
	for j := 0; j <= s.rows; j++ {
		for i := 0; i <= s.cols; i++ {
			if i != 0 && j != 0 && i != s.cols && j != s.rows {
				continue
			}
			if v := s.cur[j*s.stride+i]; v != 0 {
				t.Fatalf("border node (%d,%d) moved to %v", i, j, v)
			}
		}
	}
	if s.cur[1*s.stride+1] == 0 {
		t.Error("a drop on the corner left the nearest inner node still")
	}
	if s.cur[9*s.stride+16] != 0 {
		t.Error("a drop with no radius changed the water")
	}
	// A huge drop is clamped, not wrapped.
	s.drop(16, 9, 4, 50)
	if peak, _ := liquidPeak(s); peak > liquidMaxHeight {
		t.Errorf("peak %v over the clamp", peak)
	}
}

func TestLiquidTiltIsAPlaneBetweenFixedEdges(t *testing.T) {
	s := newLiquidSim(40, 20)
	s.tilt(1, 0)
	left, right := s.cur[10*s.stride+2], s.cur[10*s.stride+38]
	if !(right > 0 && left < 0) {
		t.Fatalf("tilt(1,0): left %v, right %v; want the right side up", left, right)
	}
	if s.prev[10*s.stride+38] != right {
		t.Error("tilt moved the surface without moving its past: it would throw the water")
	}
	if s.cur[10*s.stride+0] != 0 || s.cur[10*s.stride+40] != 0 {
		t.Error("tilt moved a border node")
	}
}

func TestLiquidResetFlattens(t *testing.T) {
	s := newLiquidSim(32, 18)
	s.drop(10, 10, 3, 1)
	s.step(0.98)
	s.reset()
	if peak, _ := liquidPeak(s); peak != 0 || s.maxAbs != 0 {
		t.Fatalf("reset left peak %v (maxAbs %v)", peak, s.maxAbs)
	}
	for _, v := range s.prev {
		if v != 0 {
			t.Fatal("reset left the past moving")
		}
	}
}

func TestLiquidSlopeSign(t *testing.T) {
	s := newLiquidSim(32, 18)
	s.tilt(1, 1)
	sx, sy := s.slope(16, 9)
	if sx <= 0 || sy <= 0 {
		t.Fatalf("slope on a plane rising right and down = (%v, %v)", sx, sy)
	}
	// One-sided at the borders, never out of range.
	s.slope(0, 0)
	s.slope(s.cols, s.rows)
}

func TestLiquidGridIndices(t *testing.T) {
	cols, rows := 64, 36
	idx := liquidGridIndices(cols, rows)
	if len(idx) != cols*rows*6 {
		t.Fatalf("%d indices, want %d", len(idx), cols*rows*6)
	}
	nodes := int32((cols + 1) * (rows + 1))
	for _, i := range idx {
		if i < 0 || i >= nodes {
			t.Fatalf("index %d outside %d nodes", i, nodes)
		}
	}
	if got := liquidGridIndices(0, 5); got != nil {
		t.Errorf("an empty grid got %d indices", len(got))
	}
}

func TestLiquidFocusMoved(t *testing.T) {
	if !liquidFocusMoved(0, 0, false, 100, 100, 80) {
		t.Error("the first focus should ripple")
	}
	// A 100 px tile: the threshold is 35 px.
	if liquidFocusMoved(100, 100, true, 110, 100, 100) {
		t.Error("a 10 px settle rippled")
	}
	if !liquidFocusMoved(100, 100, true, 140, 100, 100) {
		t.Error("a step to the next tile did not ripple")
	}
	// A small row: never under 24 px.
	if liquidFocusMoved(100, 100, true, 120, 100, 20) {
		t.Error("a 20 px move on a small row rippled")
	}
}

func TestLiquidRippleRadius(t *testing.T) {
	for _, c := range []struct{ side, want float64 }{{20, 28}, {100, 50}, {400, 110}} {
		if got := liquidRippleRadius(c.side); got != c.want {
			t.Errorf("side %v: radius %v, want %v", c.side, got, c.want)
		}
	}
}

func TestParseLiquidLevel(t *testing.T) {
	cases := map[interface{}]liquidLevel{
		"":         liquidLevelOff,
		"on":       liquidLevelOff,
		"bubbles":  liquidLevelOff,
		"off":      liquidLevelOff,
		"gentle":   liquidLevelGentle,
		" Lively ": liquidLevelLively,
		"GENTLE":   liquidLevelGentle,
		true:       liquidLevelOff,
		1.0:        liquidLevelOff,
	}
	for in, want := range cases {
		if got := parseLiquidLevel(in); got != want {
			t.Errorf("parseLiquidLevel(%#v) = %v, want %v", in, got, want)
		}
	}
	if parseLiquidLevel(nil) != liquidLevelOff {
		t.Error("a missing value turned the water on")
	}
	for level, value := range liquidLevelValues {
		if parseLiquidLevel(value) != liquidLevel(level) {
			t.Errorf("stored value %q does not read back as level %d", value, level)
		}
		if liquidOptions[level].Value != value {
			t.Errorf("Settings stop %d is %q, want %q", level, liquidOptions[level].Value, value)
		}
	}
}

func TestLiquidLevelAndTiltFromConfig(t *testing.T) {
	c := &Config{}
	if liquidLevelOf(c) != liquidLevelOff {
		t.Error("a config without the key has the water on")
	}
	c.Variables.Custom = map[string]interface{}{liquidKey: "lively"}
	if liquidLevelOf(c) != liquidLevelLively {
		t.Error("lively did not read back")
	}
	if !liquidTiltOn(c) {
		t.Error("tilt should default to on")
	}
	for _, off := range []interface{}{"false", false, "off", "0"} {
		c.Variables.Custom[liquidTiltKey] = off
		if liquidTiltOn(c) {
			t.Errorf("tilt %#v read as on", off)
		}
	}
	c.Variables.Custom[liquidTiltKey] = "true"
	if !liquidTiltOn(c) {
		t.Error("tilt \"true\" read as off")
	}
}

func TestLiquidTuning(t *testing.T) {
	gentle := liquidTuningFor(liquidLevelGentle, false, false)
	lively := liquidTuningFor(liquidLevelLively, false, false)
	if !(lively.drop > gentle.drop && lively.rainMax < gentle.rainMax && lively.damp > gentle.damp) {
		t.Errorf("lively %+v is not livelier than gentle %+v", lively, gentle)
	}
	low := liquidTuningFor(liquidLevelGentle, false, true)
	if low.rainMin != 2*gentle.rainMin || low.rainMax != 2*gentle.rainMax {
		t.Errorf("Low Power rain %v–%v, want twice %v–%v", low.rainMin, low.rainMax, gentle.rainMin, gentle.rainMax)
	}
	calm := liquidTuningFor(liquidLevelLively, true, false)
	if calm.rainMin != 0 || calm.rainMax != 0 || calm.slosh != 0 {
		t.Errorf("Reduce Motion kept rain or slosh: %+v", calm)
	}
	if !(calm.damp < gentle.damp && calm.drop < lively.drop && calm.drop > 0) {
		t.Errorf("Reduce Motion ripples %+v should be shallower and settle sooner, but still answer", calm)
	}
}

func TestLiquidUpProjectionAndSlosh(t *testing.T) {
	if _, _, ok := liquidUpProjection(0, 0, 0); ok {
		t.Error("a zero reading has no direction")
	}
	if _, _, ok := liquidUpProjection(math.NaN(), 0, 1); ok {
		t.Error("a NaN reading was accepted")
	}
	ux, uy, ok := liquidUpProjection(0, 0, 9.8) // flat on a table
	if !ok || math.Abs(ux) > 1e-9 || math.Abs(uy) > 1e-9 {
		t.Fatalf("flat reading projected to (%v, %v)", ux, uy)
	}
	// The right edge goes down: "up" leans left, the water runs right.
	ux, _, _ = liquidUpProjection(-0.7, 0, 0.7)
	if ux >= 0 {
		t.Fatalf("right edge down: up leans %v, want left", ux)
	}
	ax, _ := liquidTiltPlane(ux, 0, 2)
	s := newLiquidSim(32, 18)
	s.tilt(ax, 0)
	if !(s.cur[9*s.stride+30] > s.cur[9*s.stride+2]) {
		t.Error("the water did not pile toward the lowered right edge")
	}
	// The top edge tips away: up leans toward the screen's bottom, the water
	// runs to the top.
	_, uy, _ = liquidUpProjection(0, -0.7, 0.7)
	if uy <= 0 {
		t.Fatalf("top edge down: up leans %v, want down the screen", uy)
	}
	_, ay := liquidTiltPlane(0, uy, 2)
	s.reset()
	s.tilt(0, ay)
	if !(s.cur[2*s.stride+16] > s.cur[16*s.stride+16]) {
		t.Error("the water did not pile toward the lowered top edge")
	}
	if got := liquidPoseAverage([2]float64{1, -2}, 4); got != [2]float64{0.25, -0.5} {
		t.Errorf("pose average %v", got)
	}
	if got := liquidPoseAverage([2]float64{1, 1}, 0); got != [2]float64{} {
		t.Errorf("pose of no samples %v", got)
	}
}

func TestLiquidLightIsUnit(t *testing.T) {
	for _, tilt := range [][2]float64{{0, 0}, {0.3, -0.2}, {-1, 1}} {
		lx, ly, lz, hx, hy, hz := liquidLight(tilt[0], tilt[1])
		if n := math.Sqrt(lx*lx + ly*ly + lz*lz); math.Abs(n-1) > 1e-9 {
			t.Errorf("light %v has length %v", tilt, n)
		}
		if n := math.Sqrt(hx*hx + hy*hy + hz*hz); math.Abs(n-1) > 1e-9 {
			t.Errorf("half vector %v has length %v", tilt, n)
		}
		if lz <= 0 || hz <= 0 {
			t.Errorf("tilt %v put the light under the water", tilt)
		}
	}
}

func TestLiquidBakePixels(t *testing.T) {
	k := liquidBakeKey{
		base:   sdl.Color{R: 11, G: 15, B: 23, A: 255},
		bloom1: sdl.Color{R: 85, G: 216, B: 255, A: 255},
		bloom2: sdl.Color{R: 124, G: 58, B: 237, A: 255},
		w:      1280,
		h:      720,
		bw:     256,
		bh:     144,
	}
	pix := liquidBakePixels(k)
	if len(pix) != 256*144*4 {
		t.Fatalf("%d bytes, want %d", len(pix), 256*144*4)
	}
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 255 {
			t.Fatal("the baked picture has a transparent pixel")
		}
	}
	// The top-right corner is outside both blooms: plain base.
	i := (0*256 + 255) * 4
	if pix[i] != 11 || pix[i+1] != 15 || pix[i+2] != 23 {
		t.Errorf("top-right corner %v, want the base", pix[i:i+3])
	}
	// Near the first bloom's centre (60, 300 on 1280x720) the cyan shows.
	i = ((300*144/720)*256 + 60*256/1280) * 4
	if !(pix[i+1] > 15 && pix[i+2] > 23) {
		t.Errorf("bloom centre %v is not tinted", pix[i:i+3])
	}
	// The living background's ramp: top colour at the top, bottom at the
	// bottom, and no blooms.
	d := liquidBakeKey{
		dynamic: true,
		top:     sdl.Color{R: 0, G: 0, B: 0, A: 255},
		bottom:  sdl.Color{R: 200, G: 100, B: 50, A: 255},
		w:       960,
		h:       720,
		bw:      192,
		bh:      144,
	}
	pix = liquidBakePixels(d)
	top, bottom := pix[0:3], pix[(143*192)*4:(143*192)*4+3]
	if top[0] > 5 || bottom[0] < 190 {
		t.Errorf("ramp top %v bottom %v", top, bottom)
	}
	if liquidBakePixels(liquidBakeKey{}) != nil {
		t.Error("an empty key baked a picture")
	}
}

func TestLiquidClipRows(t *testing.T) {
	// The header band (64 px of a 720 px canvas on a 36-row grid) covers 3.2
	// rows, so 4; a clip ending exactly on a row boundary does not take the
	// next row; an empty, negative or off-canvas clip covers nothing; a clip
	// larger than the canvas covers every row without overflowing.
	cases := []struct {
		y, h   int32
		canvas int32
		rows   int
		j0, j1 int
	}{
		{0, 64, 720, 36, 0, 4},
		{0, 64, 960, 48, 0, 4},
		{0, 40, 720, 36, 0, 2},
		{0, 20, 720, 36, 0, 1},
		{20, 20, 720, 36, 1, 2},
		{10, 20, 720, 36, 0, 2},
		{360, 1, 720, 36, 18, 19},
		{360, 360, 720, 36, 18, 36},
		{700, 20, 720, 36, 35, 36},
		{719, 1, 720, 27, 26, 27},
		{100, 0, 720, 36, 5, 5},
		{100, -10, 720, 36, 5, 5},
		{900, 100, 720, 36, 36, 36},
		{-30, 10, 720, 36, 0, 0},
		{-50, 2000, 720, 36, 0, 36},
		{0, 2147483647, 720, 36, 0, 36},
		{0, 720, 0, 36, 0, 0},
		{0, 64, 720, 0, 0, 0},
	}
	for _, c := range cases {
		j0, j1 := liquidClipRows(c.y, c.h, c.canvas, c.rows)
		if j0 != c.j0 || j1 != c.j1 {
			t.Errorf("clip y=%d h=%d on %d px / %d rows: rows [%d, %d), want [%d, %d)", c.y, c.h, c.canvas, c.rows, j0, j1, c.j0, c.j1)
		}
	}
}

func TestLiquidUpdateDue(t *testing.T) {
	if !liquidUpdateDue(false, 0, 0, 0, 1) {
		t.Error("the first draw did not update")
	}
	if liquidUpdateDue(true, 7, 7, 10.0, 10.01) {
		t.Error("the header band's redraw in the same frame updated again")
	}
	if !liquidUpdateDue(true, 7, 8, 10.0, 10.01) {
		t.Error("a new frame did not update")
	}
	// A modal loop presents without advancing frameCount.
	if !liquidUpdateDue(true, 7, 7, 10.0, 10.0+2*liquidModalStep) {
		t.Error("a modal loop froze the water")
	}
	if !liquidUpdateDue(true, 7, 7, 10.0, 9.0) {
		t.Error("a clock that went back did not update")
	}
}

func TestLiquidTiltSmoothed(t *testing.T) {
	// The first reading after a start is taken as it is.
	f, ok, change := liquidTiltSmoothed([2]float64{}, false, 0.1, -0.05, [2]float64{})
	if !ok || math.Abs(f[0]-0.1) > 1e-12 || math.Abs(f[1]+0.05) > 1e-12 || math.Abs(change-0.15) > 1e-12 {
		t.Fatalf("first reading: f %v ok %v change %v", f, ok, change)
	}
	// Noise of a few thousandths around rest never reaches the threshold.
	f, ok = [2]float64{}, true
	for n := 0; n < 300; n++ {
		v := 0.004
		if n%2 == 1 {
			v = -0.004
		}
		f, ok, change = liquidTiltSmoothed(f, ok, v, -v, [2]float64{})
		if change >= liquidTiltMin {
			t.Fatalf("noise reached %v after %d readings", change, n+1)
		}
	}
	// A deliberate ten-degree lean gets there within a few frames, and past
	// the "someone is handling it" line.
	f, ok = [2]float64{}, true
	frames := 0
	for change < liquidTiltUser && frames < 30 {
		f, ok, change = liquidTiltSmoothed(f, ok, 0.17, 0, [2]float64{})
		frames++
	}
	if change < liquidTiltUser || frames > 6 {
		t.Errorf("a ten-degree lean took %d frames to reach %v", frames, change)
	}
}

func TestLiquidStirWords(t *testing.T) {
	// Smart Pro, Brick (with and without a keyboard), desktop on the
	// keyboard, desktop with a controller.
	cases := []struct {
		handheld, sticks, keyboard bool
		want                       string
	}{
		{true, true, false, "the sticks stir the water"},
		{true, false, false, "in mouse mode the pointer stirs the water"},
		{true, false, true, "in mouse mode the pointer stirs the water"},
		{false, true, true, "the mouse stirs the water"},
		{false, true, false, "the sticks and the mouse stir the water"},
	}
	for _, c := range cases {
		if got := liquidStirWords(c.handheld, c.sticks, c.keyboard); got != c.want {
			t.Errorf("handheld=%v sticks=%v keyboard=%v: %q, want %q", c.handheld, c.sticks, c.keyboard, got, c.want)
		}
	}
}

func TestLiquidMeshRowsAndRelease(t *testing.T) {
	var p liquidPoolState
	p.sim = newLiquidSim(32, 18)
	p.sim.drop(16, 9, 4, 0.8)
	for n := 0; n < 6; n++ {
		p.sim.step(0.984)
	}
	p.dirty = true
	p.buildMesh(640, 360, 0, 0, 1, 1)
	nodes := (32 + 1) * (18 + 1)
	if len(p.verts) != nodes || len(p.spec) != nodes || len(p.idx) != 32*18*6 {
		t.Fatalf("mesh %d verts, %d spec, %d indices", len(p.verts), len(p.spec), len(p.idx))
	}
	if len(p.specRow) != 18+1 || p.specRow[18] != len(p.specIdx) {
		t.Fatalf("specRow %v for %d glint indices", p.specRow, len(p.specIdx))
	}
	if len(p.specIdx) == 0 {
		t.Fatal("a ripple caught no light")
	}
	// Every row's slice holds only that row's cells.
	for j := 0; j < 18; j++ {
		if p.specRow[j] > p.specRow[j+1] {
			t.Fatalf("specRow is not monotonic at row %d: %v", j, p.specRow)
		}
		for _, v := range p.specIdx[p.specRow[j]:p.specRow[j+1]] {
			if row := int(v) / 33; row != j && row != j+1 {
				t.Fatalf("row %d's glints use node %d of row %d", j, v, row)
			}
		}
	}
	// A degraded pool has no light pass, so nothing is built for one.
	p.degraded, p.dirty = true, true
	p.buildMesh(640, 360, 0, 0, 1, 1)
	if len(p.specIdx) != 0 || p.specRow[18] != 0 {
		t.Errorf("a degraded mesh kept %d glint indices", len(p.specIdx))
	}
	// Turning the water off lets everything go, and costs nothing after.
	p.release(nil)
	if p.sim != nil || p.verts != nil || p.spec != nil || p.idx != nil || p.specIdx != nil || p.specRow != nil || p.meshOK {
		t.Error("release kept the grid's buffers")
	}
	p.release(nil)
}

func TestLiquidTexWindowWithoutTexture(t *testing.T) {
	u0, v0, u1, v1 := liquidTexWindow(nil, &sdl.Rect{X: 10, Y: 10, W: 20, H: 20})
	if u0 != 0 || v0 != 0 || u1 != 1 || v1 != 1 {
		t.Errorf("no texture: window %v %v %v %v, want the whole texture", u0, v0, u1, v1)
	}
}

func TestLiquidIIOProbeAndRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("IIO device names contain a colon, which Windows paths cannot")
	}
	root := t.TempDir()
	partial := filepath.Join(root, "iio:device0")
	full := filepath.Join(root, "iio:device1")
	for _, dir := range []string{partial, full} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, value string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(partial, "in_accel_x_raw", "1\n") // a device without y and z is skipped
	write(full, "in_accel_x_raw", "100\n")
	write(full, "in_accel_y_raw", "-50\n")
	write(full, "in_accel_z_raw", "980\n")
	write(full, "in_accel_scale", "0.01\n")
	write(full, "in_accel_z_scale", "0.02\n")
	if got := liquidFindIIOAccel(root); got != full {
		t.Fatalf("probe found %q, want %q", got, full)
	}
	scales := liquidIIOScales(full)
	if scales != [3]float64{0.01, 0.01, 0.02} {
		t.Errorf("scales %v", scales)
	}
	var paths [3]string
	for i, axis := range [3]string{"x", "y", "z"} {
		paths[i] = filepath.Join(full, "in_accel_"+axis+"_raw")
	}
	v, ok := liquidReadIIO(paths, scales)
	if !ok || math.Abs(v[0]-1) > 1e-9 || math.Abs(v[1]+0.5) > 1e-9 || math.Abs(v[2]-19.6) > 1e-9 {
		t.Errorf("sample %v (ok=%v)", v, ok)
	}
	write(full, "in_accel_y_raw", "garbage")
	if _, ok := liquidReadIIO(paths, scales); ok {
		t.Error("an unreadable channel produced a sample")
	}
	if got := liquidFindIIOAccel(filepath.Join(root, "missing")); got != "" {
		t.Errorf("a missing root found %q", got)
	}
}
