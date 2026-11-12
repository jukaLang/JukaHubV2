package main

// liquidbg.go — the liquid background: Home's backdrop seen through a sheet of
// water that answers the controls.
//
// The menus float on glass over a soft backdrop (ui_glass.go). With Settings →
// Display & theme → Liquid background on, that backdrop lies under a shallow
// pool: moving the focus drops a ripple under the tile it lands on, the sticks
// stir the water (the right one through the virtual cursor when it is up), a
// mouse on the desktop drags a wake, a device that can feel tilt sloshes it
// (liquidbg_motion.go), and now and then a drop of rain keeps an untouched
// screen alive.
//
// The water is a height field on a coarse grid — a node every 20 px or so,
// 65×37 nodes on the Smart Pro — stepped with the textbook leapfrog wave
// equation at a fixed 30 Hz, so it moves the same at the 15–30 fps the
// handheld runs at. Nothing is rasterised per pixel on the CPU: the backdrop's
// picture (the user's wallpaper, or a small texture of Home's own base and
// blooms baked once per theme) is drawn through a mesh with
// SDL_RenderGeometry, each vertex's texture coordinate pushed along the local
// slope (refraction) and its colour darkened on slopes facing away from the
// light; a second, untextured mesh adds the light the ripples catch, over only
// the cells that catch any. Once the water is calm it is one plain copy.
//
// What is drawn is smoother than what is simulated, because a tester found
// the water "quite pixelated" with a "slow framerate":
//
//   - In time: the frame shows the two newest time levels blended by how far
//     the clock is into the next step, so the surface moves on every frame
//     instead of in 33 ms jumps — a frame landing between steps used to show
//     the last step again, and the next one two steps at once. While it moves
//     the water also asks for the active cadence (adaptive.go
//     requestFrameEvery); it used to idle down to 15 fps under the user.
//   - In space: the mesh has twice the grid's resolution (a vertex every
//     10 px on the Smart Pro, 129×73 of them), its heights Catmull-Rom
//     interpolated from the grid, so slopes, shade and glints vary smoothly
//     instead of in 20 px facets, and the cells' diagonals alternate so no
//     grain runs one way across the screen. The wallpaper is sampled with
//     linear filtering (gifwall.go appWallpaperTextureSmooth): a ripple is a
//     lens that enlarges the picture locally, and nearest sampling showed
//     its pixels as blocks.
//
// Per frame that is a grid step or two, a blend and an interpolation of the
// heights, ~9,400 vertices and two draw calls — about a millisecond of the
// A53 for the vertices, the rest SDL expanding the ~55,000 indices
// (liquidBudgetMs watches the whole of it and falls back to the coarse mesh).

import (
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// Settings: Custom keys and the values the level is stored as.
const (
	liquidKey     = "liquidBg"
	liquidTiltKey = "liquidTilt"
)

// liquidLevel is how lively the water is.
type liquidLevel int

const (
	liquidLevelOff liquidLevel = iota
	liquidLevelGentle
	liquidLevelLively
)

// liquidLevelValues and liquidLevelLabels are indexed by liquidLevel; the
// Settings row's stops are in the same order.
var (
	liquidLevelValues = [...]string{"off", "gentle", "lively"}
	liquidLevelLabels = [...]string{"Off", "Gentle", "Lively"}
)

const (
	liquidCellPx       = 20.0 // the grid's target cell size, canvas pixels
	liquidMinCols      = 32
	liquidMaxCols      = 80
	liquidLowPowerCols = 48
	liquidMinRows      = 8
	// liquidMeshSub is how many mesh cells each grid cell is drawn as, each
	// way. Two halves the facets for four times the vertices; the budget
	// fallback (degraded) draws the grid itself.
	liquidMeshSub = 2
	// liquidLivingFrameMS is the cadence the living background asks for while
	// it is drawn (requestFrameEvery): its lights drift a few pixels a second
	// and its particles twinkle, which reads smooth at the idle cadence
	// (about 24 fps on the handheld) and steps at deep idle's 15.
	liquidLivingFrameMS = 42
	// liquidLowPowerFrameMS is the cadence the moving water asks for under
	// the Low Power setting, past its idle cap (adaptive.go
	// requestFrameEveryPastLowPower): 30 fps, the handheld's active cadence,
	// and on a PC no faster than Low Power's own cap there.
	liquidLowPowerFrameMS = 33

	liquidStepHz      = 30.0
	liquidMaxSubsteps = 4
	// liquidGapReset is a frame gap that means the loop was parked (a game, a
	// modal loop), not slow: the water resumes instead of catching up.
	liquidGapReset = 0.5

	// liquidModalStep: a modal loop (the on-screen keyboard, the shutdown
	// card) presents frames without advancing frameCount, so the water also
	// steps once this many seconds have passed since its last update — but
	// never twice in one ordinary frame, where the header band is redrawn a
	// few milliseconds after the full backdrop.
	liquidModalStep = 0.05

	// liquidWaveK is c²·dt²/dx², kept under the 2-D leapfrog limit of 0.5.
	liquidWaveK     = 0.45
	liquidMaxHeight = 2.0
	// liquidCalmLevel is where the water counts as still: a 0.005 height
	// moves the picture by about a fifth of a pixel, so nothing visible is
	// lost when the field is flattened there, and a ripple stops costing the
	// mesh seconds sooner than at a stricter level.
	liquidCalmLevel = 0.005
	liquidCalmHold  = 1.0 // seconds without a disturbance before the water rests

	// Tilt. The reading is smoothed (an exponential average with this weight
	// per frame), a change under liquidTiltMin (about a degree) is sensor
	// noise and is not applied, and only a change past liquidTiltUser counts
	// as someone handling the device: that is what keeps the rain going and
	// the calm timer reset, so a device lying on a table settles.
	liquidTiltSmooth = 0.2
	liquidTiltMin    = 0.015
	liquidTiltUser   = 0.05

	liquidRefraction = 0.03 // texture fraction a unit slope shifts the picture
	liquidSlopeGain  = 2.0  // how steep a slope looks to the light
	liquidUVInset    = 0.0005

	// liquidWakeMin is the speed (px/s) under which a stirrer leaves no wake:
	// a stick settling near its rest, a cursor creeping.
	liquidWakeMin  = 60.0
	liquidWakeFull = 960.0
	// liquidRainIdleStop: rain keeps an untouched screen alive for a while,
	// then stops so a device left on a table settles and costs nothing.
	liquidRainIdleStop = 120.0

	// liquidBudgetMs is what the water may cost the handheld's CPU per frame
	// (simulation, mesh, and SDL expanding the mesh in RenderGeometry) before
	// it steps down for the session.
	liquidBudgetMs   = 5.0
	liquidBudgetHold = 3.0
)

// parseLiquidLevel reads a stored level. Only the two names turn the water
// on; anything else — empty, "on", a typo — is off.
func parseLiquidLevel(v interface{}) liquidLevel {
	s, ok := v.(string)
	if !ok {
		return liquidLevelOff
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "gentle":
		return liquidLevelGentle
	case "lively":
		return liquidLevelLively
	}
	return liquidLevelOff
}

// liquidLevelOf is config's level (appConfig's when config is nil).
func liquidLevelOf(config *Config) liquidLevel {
	if config == nil {
		config = appConfig
	}
	if config == nil || config.Variables.Custom == nil {
		return liquidLevelOff
	}
	return parseLiquidLevel(config.Variables.Custom[liquidKey])
}

// liquidTiltOn reports the tilt switch (on unless turned off). It reads the
// map directly: it runs every frame the water is on.
func liquidTiltOn(config *Config) bool {
	if config == nil {
		config = appConfig
	}
	if config == nil || config.Variables.Custom == nil {
		return true
	}
	switch v := config.Variables.Custom[liquidTiltKey].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "false", "0", "no", "off":
			return false
		}
	}
	return true
}

// liquidTuning is the set of numbers one level runs with.
type liquidTuning struct {
	damp       float32 // per-step decay of the whole surface
	drop       float32 // depth of a focus ripple
	wake       float32 // a stirrer's wake per step at full speed
	wakeRadius float64 // canvas pixels
	rainMin    float64 // seconds between raindrops; 0 means no rain
	rainMax    float64
	slosh      float64 // tilt gain; 0 means tilt is ignored
}

// liquidTuningFor picks the numbers. Reduce Motion — which the handheld's
// own config turns on by default, as does Battery Saver — keeps only the
// water's answer to the user's own moves: no rain, no slosh, shallower ripples
// that settle sooner. Pausing the water outright would leave the setting
// looking broken on exactly the device it was made for.
func liquidTuningFor(level liquidLevel, reduced, lowPower bool) liquidTuning {
	t := liquidTuning{damp: 0.984, drop: 0.7, wake: 0.10, wakeRadius: 26, rainMin: 1.6, rainMax: 4.0, slosh: 2.4}
	if level == liquidLevelLively {
		// Lively is livelier in how long its waves last and how often it
		// rains, not in depth: a drop much deeper than 0.75 under a big tile,
		// stepped onto again before it settles, rang into the height clamp
		// and drew flat-topped crests.
		t = liquidTuning{damp: 0.988, drop: 0.75, wake: 0.16, wakeRadius: 32, rainMin: 0.6, rainMax: 2.0, slosh: 3.2}
	}
	if lowPower {
		t.rainMin *= 2
		t.rainMax *= 2
	}
	if reduced {
		t.damp = 0.975
		t.drop *= 0.6
		t.wake *= 0.6
		t.rainMin, t.rainMax = 0, 0
		t.slosh = 0
	}
	return t
}

// liquidGridSize is the grid for a w×h canvas: square cells of about
// liquidCellPx, a coarser grid on Low Power.
func liquidGridSize(w, h int32, lowPower bool) (cols, rows int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	cols = int(math.Round(float64(w) / liquidCellPx))
	if cols < liquidMinCols {
		cols = liquidMinCols
	}
	if cols > liquidMaxCols {
		cols = liquidMaxCols
	}
	if lowPower && cols > liquidLowPowerCols {
		cols = liquidLowPowerCols
	}
	rows = int(math.Round(float64(cols) * float64(h) / float64(w)))
	if rows < liquidMinRows {
		rows = liquidMinRows
	}
	return cols, rows
}

// ── The water ────────────────────────────────────────────────────────────

// liquidSim is the height field: (cols+1)×(rows+1) nodes, two time levels for
// the leapfrog step. The border nodes stay at zero, so waves reflect off the
// screen's edges.
type liquidSim struct {
	cols, rows int
	stride     int
	cur, prev  []float32
	maxAbs     float32 // the largest |height| after the last step
}

func newLiquidSim(cols, rows int) *liquidSim {
	if cols < 2 || rows < 2 {
		return nil
	}
	n := (cols + 1) * (rows + 1)
	return &liquidSim{cols: cols, rows: rows, stride: cols + 1, cur: make([]float32, n), prev: make([]float32, n)}
}

// step advances the water by one tick: next = 2·now − before + k·∇², all of
// it scaled by damp, which settles both the waves and any leftover offset.
// The new heights are written over the old time level and the two swap.
func (s *liquidSim) step(damp float32) {
	cur, prev, st := s.cur, s.prev, s.stride
	var peak float32
	for j := 1; j < s.rows; j++ {
		row := j * st
		for i := 1; i < s.cols; i++ {
			k := row + i
			c := cur[k]
			lap := cur[k-1] + cur[k+1] + cur[k-st] + cur[k+st] - 4*c
			v := (2*c - prev[k] + liquidWaveK*lap) * damp
			if v > liquidMaxHeight {
				v = liquidMaxHeight
			} else if v < -liquidMaxHeight {
				v = -liquidMaxHeight
			}
			prev[k] = v
			if v > peak {
				peak = v
			} else if -v > peak {
				peak = -v
			}
		}
	}
	s.cur, s.prev = prev, cur
	s.maxAbs = peak
}

// drop presses a smooth dip of the given depth into the water — a raised
// cosine `radius` nodes wide around (x, y), in node coordinates. A negative
// depth raises a bump. Only the current level changes, so the dip also starts
// moving, which is what makes it ring outward.
func (s *liquidSim) drop(x, y, radius, depth float32) {
	if s == nil || depth == 0 || radius <= 0 {
		return
	}
	// Narrower than this the dip is one node: a ripple the grid cannot draw
	// round, which rings as a checkerboard instead.
	if radius < 1.5 {
		radius = 1.5
	}
	i0 := int(math.Floor(float64(x - radius)))
	i1 := int(math.Ceil(float64(x + radius)))
	j0 := int(math.Floor(float64(y - radius)))
	j1 := int(math.Ceil(float64(y + radius)))
	if i0 < 1 {
		i0 = 1
	}
	if j0 < 1 {
		j0 = 1
	}
	if i1 > s.cols-1 {
		i1 = s.cols - 1
	}
	if j1 > s.rows-1 {
		j1 = s.rows - 1
	}
	r2 := radius * radius
	for j := j0; j <= j1; j++ {
		dy := float32(j) - y
		for i := i0; i <= i1; i++ {
			dx := float32(i) - x
			d2 := dx*dx + dy*dy
			if d2 >= r2 {
				continue
			}
			d := math.Sqrt(float64(d2)) / float64(radius)
			w := float32(0.5 * (1 + math.Cos(math.Pi*d)))
			k := j*s.stride + i
			v := s.cur[k] - depth*w
			if v > liquidMaxHeight {
				v = liquidMaxHeight
			} else if v < -liquidMaxHeight {
				v = -liquidMaxHeight
			}
			s.cur[k] = v
		}
	}
}

// tilt adds a plane through the pool's centre — ax across its whole width, ay
// across its height — to both time levels, so the surface is displaced
// without being thrown: the waves come from the edges holding it back.
func (s *liquidSim) tilt(ax, ay float32) {
	if s == nil || (ax == 0 && ay == 0) {
		return
	}
	cx, cy := float32(s.cols)/2, float32(s.rows)/2
	sx, sy := ax/float32(s.cols), ay/float32(s.rows)
	for j := 1; j < s.rows; j++ {
		dy := (float32(j) - cy) * sy
		row := j * s.stride
		for i := 1; i < s.cols; i++ {
			k := row + i
			d := (float32(i)-cx)*sx + dy
			s.cur[k] = liquidClampHeight(s.cur[k] + d)
			s.prev[k] = liquidClampHeight(s.prev[k] + d)
		}
	}
}

func liquidClampHeight(v float32) float32 {
	if v > liquidMaxHeight {
		return liquidMaxHeight
	}
	if v < -liquidMaxHeight {
		return -liquidMaxHeight
	}
	return v
}

// reset flattens the water.
func (s *liquidSim) reset() {
	for i := range s.cur {
		s.cur[i] = 0
		s.prev[i] = 0
	}
	s.maxAbs = 0
}

// liquidGridIndices are the two triangles of every cell, six indices a cell
// in row order, built once per mesh. The diagonal alternates like a
// chequerboard: with every cell split the same way, the shading's linear
// steps lined up into a grain running corner to corner across the screen.
func liquidGridIndices(cols, rows int) []int32 {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	st := int32(cols + 1)
	idx := make([]int32, 0, cols*rows*6)
	for j := 0; j < rows; j++ {
		for i := 0; i < cols; i++ {
			a := int32(j)*st + int32(i)
			b := a + 1
			c := a + st
			d := c + 1
			if (i+j)&1 == 0 {
				idx = append(idx, a, b, c, b, d, c)
			} else {
				idx = append(idx, a, b, d, a, d, c)
			}
		}
	}
	return idx
}

// liquidCubic is the Catmull-Rom weights of the four grid nodes around a
// point t of the way (0 ≤ t < 1) from the second node to the third. The curve
// passes through the nodes and its slope is continuous across them, which is
// what lets the shading follow a ripple's curve instead of its grid.
func liquidCubic(t float32) [4]float32 {
	t2 := t * t
	t3 := t2 * t
	return [4]float32{
		(-t3 + 2*t2 - t) / 2,
		(3*t3 - 5*t2 + 2) / 2,
		(-3*t3 + 4*t2 + t) / 2,
		(t3 - t2) / 2,
	}
}

// liquidClampNode keeps a neighbour's index on the grid: past an edge the
// edge node stands in (it is held at zero anyway).
func liquidClampNode(i, last int) int {
	if i < 0 {
		return 0
	}
	if i > last {
		return last
	}
	return i
}

// ── Focus ────────────────────────────────────────────────────────────────

// liquidFocusNote is one focus ring as it was drawn. epoch is the water's
// update count when it was drawn: rings drawn after one update belong to the
// picture that update led to, whether the main loop or a modal loop (which
// does not advance frameCount) presented it.
type liquidFocusNote struct {
	epoch uint64
	rect  sdl.Rect
	ok    bool
}

// liquidFocusMoved reports whether a focus centred at (x, y) on a control
// whose shorter side is minSide has left the last ripple point far enough to
// earn a new ripple: a step to the next tile does, the lift animation or a
// list settling by a few pixels does not.
func liquidFocusMoved(lastX, lastY float64, have bool, x, y, minSide float64) bool {
	if !have {
		return true
	}
	return math.Hypot(x-lastX, y-lastY) > math.Max(24, 0.35*minSide)
}

// liquidRippleRadius is the size of the ripple a control of that shorter side
// drops: about the control itself, within limits.
func liquidRippleRadius(minSide float64) float64 {
	return math.Max(28, math.Min(110, minSide/2))
}

// liquidNoteFocus is the focus rings' hook (drawGlassFocusRing, Home's
// drawHomeFocusRing). It keeps the last ring drawn after each update of the
// water: overlays draw after the screen under them, so the last ring is the
// one in front.
func liquidNoteFocus(rect sdl.Rect) {
	if !liquidPool.on {
		return
	}
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	p := &liquidPool
	if p.focusCur.ok && p.focusCur.epoch != p.epoch {
		p.focusDone = p.focusCur
	}
	p.focusCur = liquidFocusNote{epoch: p.epoch, rect: rect, ok: true}
}

// ── State ────────────────────────────────────────────────────────────────

// liquidStirrer is one point that churns the water as it moves.
type liquidStirrer struct {
	x, y   float64 // canvas pixels
	vx, vy float64 // px/s, the left stick's spring
	placed bool
}

// liquidDrew says what liquidBackdropBase painted, and so what the backdrop
// still has to.
type liquidDrew int

const (
	liquidDrewNothing   liquidDrew = iota // the water is off: the usual backdrop
	liquidDrewWallpaper                   // the wallpaper; its dim and the blooms are still to come
	liquidDrewBackdrop                    // the base and both blooms, baked
)

// liquidMeshKey is what the mesh was last built for, besides the water.
type liquidMeshKey struct {
	w, h           int32
	cols, rows     int
	u0, v0, u1, v1 float32
	lx, ly         float32 // the light's tilt shift
	spec           sdl.Color
	light          bool // light theme
}

// liquidBakeKey is what the baked backdrop picture was made from.
type liquidBakeKey struct {
	base, top, bottom sdl.Color
	bloom1, bloom2    sdl.Color
	dynamic           bool
	w, h              int32
	bw, bh            int
}

// liquidPoolState is the water and everything drawn with it. Main thread only.
type liquidPoolState struct {
	// on is the focus hook's one check: true while the water is in use.
	on bool

	level  liquidLevel
	tuning liquidTuning
	sim    *liquidSim
	w, h   int32
	low    bool // the grid is the Low Power one

	// epoch counts updates (focus notes carry it); frame is the frameCount
	// and updatedAt the clock of the newest one.
	epoch     uint64
	frame     uint64
	framed    bool
	updatedAt float64

	last     float64 // clock of the last step, seconds
	acc      float64
	dirty    bool // the water changed since the mesh was built
	calm     bool
	lastStir float64 // clock of the last disturbance
	lastUser float64 // clock of the user's last stir
	nextRain float64
	lastDrew liquidDrew

	focusCur, focusDone liquidFocusNote
	focusUsed           uint64
	focusX, focusY      float64
	haveFocus           bool
	rippleX, rippleY    float64
	haveRipple          bool

	left, right      liquidStirrer
	leftWakeUntil    float64
	rightCursor      bool // the right stirrer is following the virtual cursor
	mouseX, mouseY   int32
	mouseSeen        bool
	motionOn         bool
	tiltApplied      [2]float64
	tiltF            [2]float64 // the smoothed reading
	tiltFOK          bool
	lightTX, lightTY float64

	// alpha is how far the clock is into the next step (0–1): the frame draws
	// the two newest time levels blended by it. blend, rowX and fine are the
	// surface as drawn (surface): the blended grid, the grid interpolated
	// along x, and the mesh's heights.
	alpha             float32
	blend, rowX, fine []float32

	verts            []sdl.Vertex
	spec             []sdl.Vertex
	idx              []int32
	idxCols, idxRows int // the mesh idx was built for
	specIdx          []int32
	// specRow[j] is where mesh row j's cells start in specIdx (specRow[rows]
	// is its end), so a clipped repaint can draw just the rows it covers.
	specRow []int
	meshKey liquidMeshKey
	meshOK  bool

	renderer    *sdl.Renderer
	baked       *sdl.Texture
	bakedKey    liquidBakeKey
	bakeFailed  bool
	unsupported bool
	warned      bool

	frameCost time.Duration
	costEMA   float64 // ms
	slowFrom  float64
	degraded  bool
}

var liquidPool liquidPoolState

// liquidUnsupportedSeen survives renderer changes so Settings can say why the
// water never showed.
var liquidUnsupportedSeen bool

// liquidFull is the scratch destination of the plain copy (a package value, so
// passing its address to SDL never allocates).
var liquidFull sdl.Rect

// liquidClock is the water's time in seconds.
func liquidClock() float64 {
	return float64(sdl.GetTicks64()) / 1000
}

// liquidPoolOff puts the water away: the focus hook is back to its one check
// and the tilt sampler stops.
func liquidPoolOff() {
	p := &liquidPool
	p.on = false
	p.lastDrew = liquidDrewNothing
	if p.motionOn {
		liquidMotionStop()
		p.motionOn = false
	}
	p.tiltApplied = [2]float64{}
	p.tiltFOK = false
	p.lightTX, p.lightTY = 0, 0
	// A ring noted before the water went away must not ripple when it is
	// back on.
	p.focusCur, p.focusDone = liquidFocusNote{}, liquidFocusNote{}
}

// release frees what the water holds once it is switched off: the baked
// picture and the mesh's buffers, about 1 MB between the GPU and the heap.
// The picture is destroyed only when it belongs to renderer, the one still
// drawing; one made by an earlier renderer died with it and is forgotten. It
// is called every frame the water is off, so it must cost nothing once
// everything is gone. (A renderer that cannot draw the water keeps its
// buffers: that path is markUnsupported, not this.)
func (p *liquidPoolState) release(renderer *sdl.Renderer) {
	if p.baked == nil && p.sim == nil && p.verts == nil && p.idx == nil {
		return
	}
	if p.baked != nil && p.renderer != nil && p.renderer == renderer {
		_ = p.baked.Destroy()
	}
	p.baked, p.bakedKey, p.bakeFailed = nil, liquidBakeKey{}, false
	p.sim = nil
	p.verts, p.spec, p.idx, p.specIdx, p.specRow = nil, nil, nil, nil, nil
	p.blend, p.rowX, p.fine = nil, nil, nil
	p.idxCols, p.idxRows = 0, 0
	p.meshOK, p.dirty, p.calm = false, false, true
	p.haveRipple = false
}

// disturb wakes the water.
func (p *liquidPoolState) disturb(now float64) {
	p.lastStir = now
	p.calm = false
	p.dirty = true
}

// dropAt drops a ripple at canvas pixel (x, y).
func (p *liquidPoolState) dropAt(x, y, radiusPx float64, depth float32, now float64) {
	s := p.sim
	if s == nil || p.w <= 0 || p.h <= 0 || depth == 0 {
		return
	}
	cell := float64(p.w) / float64(s.cols)
	gx := float32(x / float64(p.w) * float64(s.cols))
	gy := float32(y / float64(p.h) * float64(s.rows))
	s.drop(gx, gy, float32(radiusPx/cell), depth)
	p.disturb(now)
}

// wake is a stirrer's trail: a shallow dip where it is, deeper the faster it
// goes, scaled by the time it covered so the trail does not depend on the
// frame rate.
func (p *liquidPoolState) wake(x, y, speed, dt, now float64) {
	if speed < liquidWakeMin || dt <= 0 || p.tuning.wake <= 0 {
		return
	}
	f := math.Min(1, (speed-liquidWakeMin)/(liquidWakeFull-liquidWakeMin))
	depth := float32(f * float64(p.tuning.wake) * math.Min(dt, 0.1) * liquidStepHz)
	p.dropAt(x, y, p.tuning.wakeRadius, depth, now)
	p.lastUser = now
}

// ── Update ───────────────────────────────────────────────────────────────

// liquidUpdate runs once per frame: the inputs drop their ripples, then the
// water steps on the fixed clock.
func liquidUpdate(config *Config, w, h int32, now float64) {
	p := &liquidPool
	p.noteCost(now)
	start := time.Now()

	low := effectiveLowPower(config) || p.degraded
	p.tuning = liquidTuningFor(p.level, effectiveReducedMotion(config), low)
	if p.sim == nil || p.w != w || p.h != h || p.low != low {
		cols, rows := liquidGridSize(w, h, low)
		p.sim = newLiquidSim(cols, rows)
		p.w, p.h, p.low = w, h, low
		p.meshOK = false
		p.calm = true
		p.haveRipple = false
		p.left.placed, p.right.placed = false, false
	}
	if p.sim == nil {
		return
	}
	dt := now - p.last
	if p.last == 0 || dt < 0 || dt > liquidGapReset {
		dt = 0
		p.acc = 0
		if p.lastUser == 0 {
			// Rain starts with the water, not after the first touch.
			p.lastUser = now
		}
	}
	p.last = now

	p.updateFocus(now)
	p.updateSticks(config, now, dt)
	p.updateMouse(now, dt)
	p.updateTilt(config, now)
	p.updateRain(now)

	if !p.calm && !gameScreenSuspended() {
		p.acc += dt
		steps := int(p.acc * liquidStepHz)
		if steps > liquidMaxSubsteps {
			steps = liquidMaxSubsteps
			p.acc = 0
		} else {
			p.acc -= float64(steps) / liquidStepHz
		}
		for n := 0; n < steps; n++ {
			p.sim.step(p.tuning.damp)
		}
		if steps > 0 {
			p.dirty = true
			if p.sim.maxAbs < liquidCalmLevel && now-p.lastStir > liquidCalmHold {
				p.sim.reset()
				p.calm = true
				p.acc = 0
			}
		}
	}
	// The frame draws the surface this far into the next step (surface), so
	// every frame that moves the clock rebuilds the mesh, step or no step:
	// that is what keeps the water moving between the 30 Hz steps.
	alpha := float32(0)
	if !p.calm {
		alpha = float32(math.Min(1, math.Max(0, p.acc*liquidStepHz)))
	}
	if alpha != p.alpha {
		p.alpha = alpha
		p.dirty = true
	}
	p.frameCost += time.Since(start)
}

// updateFocus turns the newest finished frame's focus ring into the focus
// centre (where the left stick stirs from) and, when it moved, a ripple.
func (p *liquidPoolState) updateFocus(now float64) {
	note := p.focusDone
	if p.focusCur.ok && p.focusCur.epoch < p.epoch {
		note = p.focusCur
	}
	if !note.ok || note.epoch == p.focusUsed {
		return
	}
	p.focusUsed = note.epoch
	r := note.rect
	cx := float64(r.X) + float64(r.W)/2
	cy := float64(r.Y) + float64(r.H)/2
	minSide := float64(min32(r.W, r.H))
	p.focusX, p.focusY, p.haveFocus = cx, cy, true
	if liquidFocusMoved(p.rippleX, p.rippleY, p.haveRipple, cx, cy, minSide) {
		p.dropAt(cx, cy, liquidRippleRadius(minSide), p.tuning.drop, now)
		p.rippleX, p.rippleY, p.haveRipple = cx, cy, true
		p.lastUser = now
	}
}

// updateSticks moves the stick stirrers. The left one rides a critically
// damped spring toward the focus centre, offset by the stick — so stepping
// through a menu with it drags a wake from tile to tile — and only leaves a
// wake while the stick is tilted (and briefly as it springs back): D-pad
// steps drop their ripple and nothing else. The right one follows the virtual
// cursor while that is up, and is a free stirrer of its own where the cursor
// is off.
func (p *liquidPoolState) updateSticks(config *Config, now, dt float64) {
	w, h := float64(p.w), float64(p.h)
	sdt := math.Min(dt, 0.05)

	tx, ty := w/2, h/2
	if p.haveFocus {
		tx, ty = p.focusX, p.focusY
	}
	lx, ly := stickShaped(config, stickLeft)
	if math.Hypot(lx, ly) > 0.15 {
		tx += lx * 0.14 * h
		ty += ly * 0.14 * h
		p.leftWakeUntil = now + 0.35
	}
	l := &p.left
	if !l.placed {
		l.x, l.y, l.vx, l.vy, l.placed = tx, ty, 0, 0, true
	} else if sdt > 0 {
		const omega = 12.0 // rad/s: settles in about half a second
		l.vx += (omega*omega*(tx-l.x) - 2*omega*l.vx) * sdt
		l.vy += (omega*omega*(ty-l.y) - 2*omega*l.vy) * sdt
		l.x += l.vx * sdt
		l.y += l.vy * sdt
		if now < p.leftWakeUntil {
			p.wake(l.x, l.y, math.Hypot(l.vx, l.vy), dt, now)
		}
	}

	r := &p.right
	// Mouse mode (vcursor_mode.go) puts the pointer up even where the
	// right-stick pointer is switched off — and on the Brick, which has no
	// sticks, it is the only thing that can stir — so it counts too.
	cursorMode := (vcursorEnabled(config) || vcursorModeLive(config)) && !vcursorBlocked(config)
	if cursorMode != p.rightCursor {
		// Switching between the cursor and the free stirrer is a jump, not a
		// stroke: no wake for it.
		p.rightCursor = cursorMode
		r.placed = false
	}
	if cursorMode {
		if !vcursor.active {
			r.placed = false
			return
		}
		x, y := vcursor.x, vcursor.y
		if r.placed && dt > 0 {
			p.wake(x, y, math.Hypot(x-r.x, y-r.y)/dt, dt, now)
		}
		r.x, r.y, r.placed = x, y, true
		return
	}
	rx, ry := stickShaped(config, stickRight)
	m := math.Hypot(rx, ry)
	if m < 0.08 {
		return
	}
	if !r.placed {
		r.x, r.y, r.placed = w/2, h/2, true
	}
	if sdt <= 0 {
		return
	}
	const speed = 600.0
	ox, oy := r.x, r.y
	r.x = clampFloat(r.x+rx*speed*sdt, 0, w-1)
	r.y = clampFloat(r.y+ry*speed*sdt, 0, h-1)
	// Measured from where it went, so a stirrer held against an edge stops
	// stirring instead of digging a pit there.
	p.wake(r.x, r.y, math.Hypot(r.x-ox, r.y-oy)/sdt, dt, now)
}

// updateMouse lets a desktop mouse drag a wake. The handheld has no pointer
// but the virtual cursor, which the right stick's stirrer already follows.
func (p *liquidPoolState) updateMouse(now, dt float64) {
	if IsTrimuiDevice() || vcursor.active {
		p.mouseSeen = false
		return
	}
	mx, my, _ := sdl.GetMouseState()
	x, y := physicalToLogical(mx, my)
	if p.mouseSeen && (x != p.mouseX || y != p.mouseY) && dt > 0 {
		dist := math.Hypot(float64(x-p.mouseX), float64(y-p.mouseY))
		p.wake(float64(x), float64(y), dist/dt, dt, now)
	}
	p.mouseX, p.mouseY, p.mouseSeen = x, y, true
}

// updateTilt sloshes the water by the change of tilt since it last moved it
// and leans the light with it.
func (p *liquidPoolState) updateTilt(config *Config, now float64) {
	if p.tuning.slosh <= 0 || !liquidTiltOn(config) || !liquidMotionAvailable() {
		if p.motionOn {
			liquidMotionStop()
			p.motionOn = false
		}
		p.tiltApplied = [2]float64{}
		p.tiltFOK = false
		p.lightTX, p.lightTY = 0, 0
		return
	}
	p.motionOn = true
	if liquidMotionStart(now) {
		// A sampler that was (re)started measures a new resting pose: a tilt
		// applied against the old one would slosh the pool on every return
		// from a screen without the backdrop.
		p.tiltApplied = [2]float64{}
		p.tiltFOK = false
		p.lightTX, p.lightTY = 0, 0
	}
	tx, ty, ok := liquidMotionTilt(now)
	if !ok {
		return
	}
	var change float64
	p.tiltF, p.tiltFOK, change = liquidTiltSmoothed(p.tiltF, p.tiltFOK, tx, ty, p.tiltApplied)
	if change < liquidTiltMin {
		// Sensor noise, or a drift too slow to see: nothing moves, so the
		// mesh is not rebuilt for it either.
		return
	}
	tx, ty = p.tiltF[0], p.tiltF[1]
	ax, ay := liquidTiltPlane(tx-p.tiltApplied[0], ty-p.tiltApplied[1], p.tuning.slosh)
	p.sim.tilt(ax, ay)
	p.tiltApplied = [2]float64{tx, ty}
	p.lightTX, p.lightTY = tx, ty
	if change > liquidTiltUser {
		p.disturb(now)
		p.lastUser = now
		return
	}
	// A small lean still has to play out, but it is not someone handling
	// the device: it wakes the water without holding off its rest or keeping
	// the rain going.
	p.calm = false
	p.dirty = true
}

// liquidTiltSmoothed folds one tilt reading (tx, ty) into the smoothed one
// and reports how far the smoothed tilt now is from the tilt last applied to
// the water (|dx| + |dy|). The first reading after a (re)start is taken as it
// is.
func liquidTiltSmoothed(f [2]float64, ok bool, tx, ty float64, applied [2]float64) ([2]float64, bool, float64) {
	if !ok {
		f = [2]float64{tx, ty}
	}
	f[0] += (tx - f[0]) * liquidTiltSmooth
	f[1] += (ty - f[1]) * liquidTiltSmooth
	return f, true, math.Abs(f[0]-applied[0]) + math.Abs(f[1]-applied[1])
}

// updateRain drops the occasional raindrop while someone has been around in
// the last couple of minutes.
func (p *liquidPoolState) updateRain(now float64) {
	t := p.tuning
	if t.rainMin <= 0 || now-p.lastUser > liquidRainIdleStop {
		p.nextRain = 0
		return
	}
	if p.nextRain == 0 {
		p.nextRain = now + t.rainMin + rand.Float64()*(t.rainMax-t.rainMin)
		return
	}
	if now < p.nextRain {
		return
	}
	x := rand.Float64() * float64(p.w)
	y := rand.Float64() * float64(p.h)
	p.dropAt(x, y, 14+rand.Float64()*16, float32(0.25+rand.Float64()*0.2), now)
	p.nextRain = now + t.rainMin + rand.Float64()*(t.rainMax-t.rainMin)
}

// noteCost folds the previous frame's cost into the average and, on the
// handheld, steps down to the Low Power grid without the light pass for the
// rest of the session when the water keeps going over budget.
func (p *liquidPoolState) noteCost(now float64) {
	ms := float64(p.frameCost) / float64(time.Millisecond)
	p.frameCost = 0
	p.costEMA += (ms - p.costEMA) * 0.1
	if p.degraded || !IsTrimuiDevice() {
		return
	}
	if p.costEMA <= liquidBudgetMs {
		p.slowFrom = 0
		return
	}
	if p.slowFrom == 0 {
		p.slowFrom = now
		return
	}
	if now-p.slowFrom >= liquidBudgetHold {
		p.degraded = true
		logInfo("liquidbg", "the water costs %.1f ms a frame; using the coarse grid and mesh without the light pass", p.costEMA)
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// liquidBackdropBase paints the bottom of the backdrop through the water: the
// wallpaper when one is up, else Home's base and blooms baked into one small
// picture. drawAppBackdrop calls it first (with the config it resolved, so
// both decide on the same one) and draws the rest according to the answer.
// The water steps at most once a frame however many times the backdrop is
// drawn (the header band is repainted from it too).
func liquidBackdropBase(renderer *sdl.Renderer, config *Config, w, h int32) liquidDrew {
	p := &liquidPool
	// Every backdrop draw starts here, water or not, so this is also where
	// the living background — which has no hook of its own in the draw —
	// asks for its cadence while it is on screen (adaptive.go). Low Power
	// draws its reduced variant at the idle cadence anyway.
	if config != nil && config.Variables.DynamicBg && !effectiveLowPower(config) {
		requestFrameEvery(liquidLivingFrameMS)
	}
	level := liquidLevelOf(config)
	if level == liquidLevelOff || renderer == nil || w <= 0 || h <= 0 {
		if p.on || p.motionOn {
			liquidPoolOff()
		}
		if level == liquidLevelOff && renderer != nil {
			p.release(renderer)
		}
		p.lastDrew = liquidDrewNothing
		return liquidDrewNothing
	}
	if p.renderer != renderer {
		// Another renderer's textures died with it: forget them, never Destroy.
		p.renderer = renderer
		p.baked = nil
		p.bakedKey = liquidBakeKey{}
		p.bakeFailed = false
		p.unsupported = false
		p.meshOK = false
	}
	if p.unsupported {
		if p.on || p.motionOn {
			liquidPoolOff()
		}
		p.lastDrew = liquidDrewNothing
		return liquidDrewNothing
	}
	justEnabled := !p.on
	p.on = true
	p.level = level
	if now := liquidClock(); liquidUpdateDue(p.framed, p.frame, frameCount, p.updatedAt, now) {
		p.framed, p.frame, p.updatedAt = true, frameCount, now
		p.epoch++
		liquidUpdate(config, w, h, now)
		if justEnabled && p.sim != nil {
			// Give the setting an immediate, gentle preview instead of leaving
			// the water looking static until focus moves or the first raindrop.
			// liquidTuningFor already scales this down for Reduce Motion.
			radius := math.Min(110, float64(min(w, h))*0.15)
			p.dropAt(float64(w)/2, float64(h)/2, radius, p.tuning.drop*0.6, now)
			p.lastUser = now
		}
	}

	// The living background hides the wallpaper and paints a flat colour
	// (renderAmbientDynamicBg); with the water on it must look the same, so
	// the water bends that colour instead of the wallpaper.
	wall := config != nil && !config.Variables.DynamicBg && appWallpaperActive(config)
	var src *sdl.Texture
	var srcRect *sdl.Rect
	if wall {
		// Linear filtering: the ripples enlarge the picture (gifwall.go).
		src, srcRect = appWallpaperTextureSmooth(renderer, config)
	}
	if src == nil {
		wall = false
		srcRect = nil
		src = p.bakedSource(renderer, config, w, h)
	}
	if src == nil {
		p.lastDrew = liquidDrewNothing
		return liquidDrewNothing
	}
	drew := liquidDrewBackdrop
	if wall {
		drew = liquidDrewWallpaper
	}
	p.lastDrew = drew
	liquidFull = sdl.Rect{X: 0, Y: 0, W: w, H: h}
	if p.calm || p.sim == nil {
		_ = renderer.Copy(src, srcRect, &liquidFull)
		return drew
	}
	// The water is moving and on screen: it asks for frames as fast as the
	// active cadence goes, and stops asking the frame it settles into the
	// plain copy above. Under the Low Power setting — the handheld's default,
	// and the water at its 24 fps cap was the tester's "slow framerate" — it
	// still asks for 30 fps; Battery Saver (the low battery's, or the power
	// mode) keeps the cap.
	if effectiveLowPower(config) && !batterySaverActive && currentPowerMode(config) != PowerModeBatterySaver {
		requestFrameEveryPastLowPower(liquidLowPowerFrameMS)
	} else {
		requestFrameEvery(1)
	}
	u0, v0, u1, v1 := liquidTexWindow(src, srcRect)
	p.buildMesh(w, h, u0, v0, u1, v1)
	if len(p.verts) == 0 || len(p.idx) == 0 {
		_ = renderer.Copy(src, srcRect, &liquidFull)
		return drew
	}
	idx := p.idx
	if j0, j1, clipped := p.clipRows(renderer); clipped {
		// The header band's repaint: only the rows under the clip. The clip
		// alone limits the GPU's fill, but SDL would still expand and upload
		// the whole mesh on the CPU for a band a few rows high.
		idx = p.idx[j0*p.idxCols*6 : j1*p.idxCols*6]
		if len(idx) == 0 {
			return drew
		}
	}
	// SDL expands the indexed mesh into its vertex buffer on this thread, in
	// the call: that is part of what the water costs, so the budget counts it.
	start := time.Now()
	err := renderer.RenderGeometry(src, p.verts, idx)
	p.frameCost += time.Since(start)
	if err != nil {
		p.markUnsupported(err)
		_ = renderer.Copy(src, srcRect, &liquidFull)
	}
	return drew
}

// liquidUpdateDue reports whether this backdrop draw should update the water:
// the first draw of a frame, or — in a modal loop that presents frames
// without advancing frameCount — once liquidModalStep has passed since the
// last update. The header band, redrawn a few milliseconds after the full
// backdrop in the same frame, never steps the water twice.
func liquidUpdateDue(framed bool, frame, frameNow uint64, updatedAt, now float64) bool {
	if !framed || frame != frameNow {
		return true
	}
	return now-updatedAt >= liquidModalStep || now < updatedAt
}

// liquidClipRows is the band of grid rows [j0, j1) that a clip from clipY to
// clipY+clipH covers, on a canvas h pixels high cut into rows rows.
func liquidClipRows(clipY, clipH, h int32, rows int) (j0, j1 int) {
	if h <= 0 || rows <= 0 {
		return 0, 0
	}
	j0 = int(int64(clipY) * int64(rows) / int64(h))
	j1 = int((int64(clipY)+int64(clipH))*int64(rows)/int64(h) + 1)
	if (int64(clipY)+int64(clipH))*int64(rows)%int64(h) == 0 {
		// The clip ends exactly on a row boundary: that row is not covered.
		j1--
	}
	if j0 < 0 {
		j0 = 0
	}
	if j0 > rows {
		j0 = rows
	}
	if j1 > rows {
		j1 = rows
	}
	if j1 < j0 {
		j1 = j0
	}
	return j0, j1
}

// clipRows is the band of grid rows under the renderer's clip, when one is
// set (clipped is false, and the band all rows, otherwise).
func (p *liquidPoolState) clipRows(renderer *sdl.Renderer) (j0, j1 int, clipped bool) {
	rows := p.idxRows
	if rows <= 0 || renderer == nil || !renderer.IsClipEnabled() {
		return 0, rows, false
	}
	c := renderer.GetClipRect()
	j0, j1 = liquidClipRows(c.Y, c.H, p.h, rows)
	return j0, j1, true
}

// liquidBackdropLight lays the light the ripples catch over the finished
// backdrop: an untextured copy of the mesh drawn additively, over only the
// cells that catch any (and, under a clip, only the rows the clip covers).
func liquidBackdropLight(renderer *sdl.Renderer) {
	p := &liquidPool
	if !p.on || p.lastDrew == liquidDrewNothing || p.calm || p.degraded || renderer == nil ||
		renderer != p.renderer || len(p.spec) == 0 || len(p.specIdx) == 0 {
		return
	}
	idx := p.specIdx
	if j0, j1, clipped := p.clipRows(renderer); clipped {
		if len(p.specRow) != p.idxRows+1 {
			return
		}
		idx = p.specIdx[p.specRow[j0]:p.specRow[j1]]
	}
	if len(idx) == 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_ADD)
	start := time.Now()
	err := renderer.RenderGeometry(nil, p.spec, idx)
	p.frameCost += time.Since(start)
	if err != nil {
		p.markUnsupported(err)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
}

// markUnsupported gives up on the water for this renderer: it cannot draw
// geometry (an SDL older than 2.0.18, or a backend without it).
func (p *liquidPoolState) markUnsupported(err error) {
	p.unsupported = true
	liquidUnsupportedSeen = true
	if !p.warned {
		p.warned = true
		logWarn("liquidbg", "the renderer cannot draw the water (%v); showing the plain backdrop", err)
	}
	liquidPoolOff()
}

// liquidTexWindow is the part of the texture the picture uses, in texture
// coordinates: all of it, or the wallpaper's crop.
func liquidTexWindow(tex *sdl.Texture, src *sdl.Rect) (u0, v0, u1, v1 float32) {
	if src == nil || tex == nil {
		return 0, 0, 1, 1
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 || src.W <= 0 || src.H <= 0 {
		return 0, 0, 1, 1
	}
	u0 = float32(src.X) / float32(tw)
	v0 = float32(src.Y) / float32(th)
	u1 = float32(src.X+src.W) / float32(tw)
	v1 = float32(src.Y+src.H) / float32(th)
	return u0, v0, u1, v1
}

// liquidLight is the light the water is lit by: from the upper left, leaning
// with the device's tilt, plus the half vector for the glints.
func liquidLight(tx, ty float64) (lx, ly, lz, hx, hy, hz float64) {
	lx, ly, lz = -0.45+0.8*tx, -0.55+0.8*ty, 0.70
	n := math.Sqrt(lx*lx + ly*ly + lz*lz)
	lx, ly, lz = lx/n, ly/n, lz/n
	hx, hy, hz = lx, ly, lz+1
	n = math.Sqrt(hx*hx + hy*hy + hz*hz)
	return lx, ly, lz, hx / n, hy / n, hz / n
}

// surface is the water as the frame draws it, in p.fine (or p.blend for a
// mesh that is the grid itself): the two newest time levels blended by alpha
// — the leapfrog keeps both, so this costs one pass — then, for a mesh sub
// times finer than the grid, Catmull-Rom interpolated onto its nodes, along x
// for every grid row and then along y. Buffers are made once per size, so a
// frame allocates nothing.
func (p *liquidPoolState) surface(sub int) []float32 {
	s := p.sim
	sub = min(sub, liquidMeshSub) // the weight table below holds no more
	n := (s.cols + 1) * (s.rows + 1)
	if len(p.blend) != n {
		p.blend = make([]float32, n)
	}
	a := p.alpha
	for k := range p.blend {
		p.blend[k] = s.prev[k] + (s.cur[k]-s.prev[k])*a
	}
	if sub <= 1 {
		return p.blend
	}
	fcols, frows := s.cols*sub, s.rows*sub
	fst, st := fcols+1, s.cols+1
	if len(p.rowX) != (s.rows+1)*fst {
		p.rowX = make([]float32, (s.rows+1)*fst)
	}
	if len(p.fine) != (frows+1)*fst {
		p.fine = make([]float32, (frows+1)*fst)
	}
	var wt [liquidMeshSub][4]float32
	for k := 1; k < sub && k < liquidMeshSub; k++ {
		wt[k] = liquidCubic(float32(k) / float32(sub))
	}
	for j := 0; j <= s.rows; j++ {
		src := p.blend[j*st : (j+1)*st]
		dst := p.rowX[j*fst : (j+1)*fst]
		for fi := range dst {
			i, k := fi/sub, fi%sub
			if k == 0 {
				dst[fi] = src[i]
				continue
			}
			c := wt[k]
			dst[fi] = c[0]*src[liquidClampNode(i-1, s.cols)] + c[1]*src[i] +
				c[2]*src[liquidClampNode(i+1, s.cols)] + c[3]*src[liquidClampNode(i+2, s.cols)]
		}
	}
	for fj := 0; fj <= frows; fj++ {
		j, k := fj/sub, fj%sub
		dst := p.fine[fj*fst : (fj+1)*fst]
		if k == 0 {
			copy(dst, p.rowX[j*fst:(j+1)*fst])
			continue
		}
		c := wt[k]
		r0 := p.rowX[liquidClampNode(j-1, s.rows)*fst:]
		r1 := p.rowX[j*fst:]
		r2 := p.rowX[liquidClampNode(j+1, s.rows)*fst:]
		r3 := p.rowX[liquidClampNode(j+2, s.rows)*fst:]
		for fi := range dst {
			dst[fi] = c[0]*r0[fi] + c[1]*r1[fi] + c[2]*r2[fi] + c[3]*r3[fi]
		}
	}
	return p.fine
}

// buildMesh lays the mesh's vertices for the current water: positions on the
// nodes, texture coordinates pushed along the slope, shade for the picture
// pass and light for the glint pass. Nothing is rebuilt when neither the water
// nor anything the mesh depends on changed (the band repaint, a calm frame).
func (p *liquidPoolState) buildMesh(w, h int32, u0, v0, u1, v1 float32) {
	s := p.sim
	if s == nil {
		return
	}
	// The budget's fallback draws the grid itself: a quarter of the vertices.
	sub := liquidMeshSub
	if p.degraded {
		sub = 1
	}
	cols, rows := s.cols*sub, s.rows*sub
	light := glassLightTheme()
	spec := glassMix(sdl.Color{R: 255, G: 255, B: 255, A: 255}, glassAccent(), 0.35)
	key := liquidMeshKey{
		w: w, h: h, cols: cols, rows: rows,
		u0: u0, v0: v0, u1: u1, v1: v1,
		lx: float32(p.lightTX), ly: float32(p.lightTY),
		spec: spec, light: light,
	}
	if p.meshOK && !p.dirty && key == p.meshKey {
		return
	}
	start := time.Now()
	nodes := (cols + 1) * (rows + 1)
	if len(p.verts) != nodes {
		p.verts = make([]sdl.Vertex, nodes)
		p.spec = make([]sdl.Vertex, nodes)
	}
	if p.idxCols != cols || p.idxRows != rows || len(p.idx) != cols*rows*6 || len(p.specRow) != rows+1 {
		p.idx = liquidGridIndices(cols, rows)
		p.specIdx = make([]int32, 0, len(p.idx))
		p.specRow = make([]int, rows+1)
		p.idxCols, p.idxRows = cols, rows
	}
	height := p.surface(sub)
	// Once the budget has stepped the water down there is no light pass to
	// feed: the glint maths and the lit-cell scan are skipped with it.
	glints := !p.degraded

	lx, ly, lz, hx, hy, hz := liquidLight(p.lightTX, p.lightTY)
	h16 := hz * hz
	h16 *= h16
	h16 *= h16
	h16 *= h16
	shadeGain, diffuse, glint := 0.55, 200.0, 110.0
	if light {
		// White light hardly shows on a pale palette; the shade has to carry
		// the ripples there.
		shadeGain, diffuse, glint = 0.8, 90.0, 60.0
	}
	fw, fh := float32(w), float32(h)
	du, dv := u1-u0, v1-v0
	umin, umax := u0+liquidUVInset, u1-liquidUVInset
	vmin, vmax := v0+liquidUVInset, v1-liquidUVInset
	stride := cols + 1
	// Slopes stay in height per grid node, the unit liquidRefraction and
	// liquidSlopeGain are tuned in, whatever the mesh's step: a central
	// difference spans two mesh nodes, 2/sub grid nodes.
	edge, mid := float32(sub), float32(sub)/2
	for j := 0; j <= rows; j++ {
		fy := float32(j) / float32(rows)
		for i := 0; i <= cols; i++ {
			fx := float32(i) / float32(cols)
			k := j*stride + i
			var sx, sy float32
			switch i {
			case 0:
				sx = (height[k+1] - height[k]) * edge
			case cols:
				sx = (height[k] - height[k-1]) * edge
			default:
				sx = (height[k+1] - height[k-1]) * mid
			}
			switch j {
			case 0:
				sy = (height[k+stride] - height[k]) * edge
			case rows:
				sy = (height[k] - height[k-stride]) * edge
			default:
				sy = (height[k+stride] - height[k-stride]) * mid
			}
			nx, ny := -float64(sx)*liquidSlopeGain, -float64(sy)*liquidSlopeGain
			inv := 1 / math.Sqrt(nx*nx+ny*ny+1)
			rel := (nx*lx+ny*ly+lz)*inv - lz
			shade := 1.0
			if rel < 0 {
				shade = math.Max(0.76, 1+rel*shadeGain)
			}
			u := u0 + (fx+sx*liquidRefraction)*du
			v := v0 + (fy+sy*liquidRefraction)*dv
			if u < umin {
				u = umin
			} else if u > umax {
				u = umax
			}
			if v < vmin {
				v = vmin
			} else if v > vmax {
				v = vmax
			}
			pos := sdl.FPoint{X: fx * fw, Y: fy * fh}
			c := uint8(255*shade + 0.5)
			p.verts[k] = sdl.Vertex{Position: pos, Color: sdl.Color{R: c, G: c, B: c, A: 255}, TexCoord: sdl.FPoint{X: u, Y: v}}
			if !glints {
				continue
			}
			ndh := (nx*hx + ny*hy + hz) * inv
			glow := 0.0
			if rel > 0 {
				glow = rel * diffuse
			}
			if ndh > 0 && h16 < 1 {
				n16 := ndh * ndh
				n16 *= n16
				n16 *= n16
				n16 *= n16
				if n16 > h16 {
					glow += (n16 - h16) / (1 - h16) * glint
				}
			}
			alpha := uint8(math.Min(255, glow))
			p.spec[k] = sdl.Vertex{Position: pos, Color: sdl.Color{R: spec.R, G: spec.G, B: spec.B, A: alpha}}
		}
	}
	// The glint pass covers only the cells with light in them: on a mostly
	// calm screen that is a few dozen triangles instead of the whole canvas.
	// specRow remembers where each mesh row starts, for a clipped repaint.
	p.specIdx = p.specIdx[:0]
	if glints {
		for j := 0; j < rows; j++ {
			p.specRow[j] = len(p.specIdx)
			for i := 0; i < cols; i++ {
				a := j*stride + i
				if p.spec[a].Color.A > 1 || p.spec[a+1].Color.A > 1 ||
					p.spec[a+stride].Color.A > 1 || p.spec[a+stride+1].Color.A > 1 {
					p.specIdx = append(p.specIdx, p.idx[(j*cols+i)*6:(j*cols+i)*6+6]...)
				}
			}
		}
	} else {
		for j := 0; j < rows; j++ {
			p.specRow[j] = 0
		}
	}
	p.specRow[rows] = len(p.specIdx)
	p.meshKey, p.meshOK, p.dirty = key, true, false
	p.frameCost += time.Since(start)
}

// bakedSource is Home's base and blooms as one small picture for the water to
// bend, rebuilt only when the colours or the canvas change. With the living
// background on it is the flat time-of-day colour that background paints
// under its drifting lights (renderAmbientDynamicBg fills the screen with
// pal.top), so turning the water on does not change what the background is;
// the lights themselves are drawn over the water as usual.
func (p *liquidPoolState) bakedSource(renderer *sdl.Renderer, config *Config, w, h int32) *sdl.Texture {
	// A texel every four pixels, filtered linearly: the picture is all soft
	// gradients, so a finer one would show nothing more. It used to follow
	// the grid (four texels a cell), which baked it again — coarser — every
	// time Low Power switched the grid.
	key := liquidBakeKey{w: w, h: h, bw: max(int(w)/4, 64), bh: max(int(h)/4, 36)}
	if config != nil && config.Variables.DynamicBg {
		pal := getTimePalette()
		key.dynamic, key.top, key.bottom = true, pal.top, pal.top
	} else {
		key.base = ColorBackground
		key.bloom1, key.bloom2 = themeBackdropBlooms(config)
	}
	if p.baked != nil && key == p.bakedKey {
		return p.baked
	}
	if p.bakeFailed && key == p.bakedKey {
		return nil
	}
	pix := liquidBakePixels(key)
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	tex := bootLiquidTexture(renderer, pix, key.bw, key.bh)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if p.baked != nil {
		// Built on this renderer (a renderer change forgets it first).
		_ = p.baked.Destroy()
		p.baked = nil
	}
	p.bakedKey = key
	if tex == nil {
		p.bakeFailed = true
		return nil
	}
	p.bakeFailed = false
	// Opaque: the GPU need not blend a full-screen layer.
	_ = tex.SetBlendMode(sdl.BLENDMODE_NONE)
	p.baked = tex
	return tex
}

// liquidTextureBytes is what the water holds as a texture — the baked
// backdrop, when there is one — for the memory line (memstat.go).
func liquidTextureBytes() int64 {
	p := &liquidPool
	if p.baked == nil {
		return 0
	}
	return int64(p.bakedKey.bw) * int64(p.bakedKey.bh) * 4
}

// liquidBakePixels renders the baked picture in R,G,B,A byte order: the base
// with the two blooms where drawAppBackdrop puts them, with homeGlowBlob's
// falloff and alphas — or, for the living background, a vertical ramp from
// top to bottom and no blooms (flat, as bakedSource asks for it).
func liquidBakePixels(k liquidBakeKey) []byte {
	if k.bw <= 0 || k.bh <= 0 || k.w <= 0 || k.h <= 0 {
		return nil
	}
	pix := make([]byte, k.bw*k.bh*4)
	fw, fh := float64(k.w), float64(k.h)
	radius := float64(scaleX(900, k.w)) / 2
	c1x, c1y := float64(scaleX(60, k.w)), float64(scaleY(300, k.h))
	c2x, c2y := fw-float64(scaleX(60, k.w)), fh
	for y := 0; y < k.bh; y++ {
		sy := (float64(y) + 0.5) / float64(k.bh) * fh
		base := k.base
		if k.dynamic {
			base = lerpColorF64(k.top, k.bottom, sy/fh)
		}
		for x := 0; x < k.bw; x++ {
			sx := (float64(x) + 0.5) / float64(k.bw) * fw
			r, g, b := float64(base.R), float64(base.G), float64(base.B)
			if !k.dynamic {
				r, g, b = liquidBloom(r, g, b, sx, sy, c1x, c1y, radius, k.bloom1, 44)
				r, g, b = liquidBloom(r, g, b, sx, sy, c2x, c2y, radius, k.bloom2, 36)
			}
			i := (y*k.bw + x) * 4
			pix[i] = uint8(math.Round(math.Min(255, math.Max(0, r))))
			pix[i+1] = uint8(math.Round(math.Min(255, math.Max(0, g))))
			pix[i+2] = uint8(math.Round(math.Min(255, math.Max(0, b))))
			pix[i+3] = 255
		}
	}
	return pix
}

// liquidBloom blends one bloom into a colour: the glow texture's (1 − d/r)²
// falloff at the bloom's alpha.
func liquidBloom(r, g, b, x, y, cx, cy, radius float64, c sdl.Color, alpha uint8) (float64, float64, float64) {
	if radius <= 0 {
		return r, g, b
	}
	t := 1 - math.Hypot(x-cx, y-cy)/radius
	if t <= 0 {
		return r, g, b
	}
	a := float64(alpha) / 255 * t * t
	return r + (float64(c.R)-r)*a, g + (float64(c.G)-g)*a, b + (float64(c.B)-b)*a
}

// ── Settings ─────────────────────────────────────────────────────────────

// liquidOptions are the level row's stops, in liquidLevel order.
var liquidOptions = []setOption{
	{Value: "off", Label: "Off"},
	{Value: "gentle", Label: "Gentle", Note: "Soft ripples and a drop of rain now and then"},
	{Value: "lively", Label: "Lively", Note: "Deeper waves and more rain"},
}

// liquidSettingDetail is the level row's second line.
func liquidSettingDetail(c *Config) string {
	switch {
	case liquidUnsupportedSeen:
		return "Not supported by this renderer"
	case liquidLevelOf(c) != liquidLevelOff && effectiveReducedMotion(c):
		return "Reduce Motion is on: no rain, softer ripples"
	}
	return "Ripples follow your moves; " + liquidStirWords(hintOnHandheld(), stickHardware(), hintsUseKeyboard())
}

// liquidStirWords names what stirs the water on this device, in the words its
// owner uses: the sticks on a Smart Pro; on the Brick family, which has none,
// the pointer of mouse mode (Select + L1), which the right stirrer follows;
// on the desktop the mouse — and the sticks too while a controller is the
// device in use.
func liquidStirWords(handheld, sticks, keyboard bool) string {
	switch {
	case !handheld && keyboard:
		return "the mouse stirs the water"
	case !handheld:
		return "the sticks and the mouse stir the water"
	case !sticks:
		return "in mouse mode the pointer stirs the water"
	}
	return "the sticks stir the water"
}

// liquidLevelRow is Settings → Display & theme → Liquid background.
func liquidLevelRow() setRow {
	return setRow{
		ID: "liquidBg", Label: "Liquid background", Kind: setKindChoice,
		Detail:  liquidSettingDetail,
		Options: func(*Config) []setOption { return liquidOptions },
		Current: func(c *Config) int { return int(liquidLevelOf(c)) },
		Choose: func(c *Config, value string) {
			level := parseLiquidLevel(value)
			setStoreString(c, liquidKey, liquidLevelValues[level])
			if level != liquidLevelOff && liquidUnsupportedSeen {
				setToast("Liquid background is not supported by this renderer")
				return
			}
			setToast("Liquid background: " + liquidLevelLabels[level])
		},
	}
}

// liquidTiltRow is the tilt switch, shown only where there is a sensor.
func liquidTiltRow() setRow {
	return setRow{
		ID: "liquidTilt", Label: "Tilt to slosh", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if effectiveReducedMotion(c) {
				return "Off while Reduce Motion is on"
			}
			return "Tilting the device rocks the water"
		},
		// Only once the water is on: the look-up initialises SDL's sensor
		// subsystem on the desktop, which nobody who never turns the water on
		// should pay for.
		Visible: func(c *Config) bool { return liquidLevelOf(c) != liquidLevelOff && liquidMotionAvailable() },
		Get:     liquidTiltOn,
		Set: func(c *Config, on bool) {
			setStoreBool(c, liquidTiltKey, on)
			setToast("Tilt to slosh: " + setOnOff(on))
		},
	}
}
