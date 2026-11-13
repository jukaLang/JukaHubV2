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
// screen alive. It is off until switched on; once on it is behind every
// screen, and R2 — on any screen where R2 has no job of its own, W on Home's
// keyboard — steps its level Off → Slight → Medium → Lively (liquidCycle).
//
// The water is a height field on a grid — a node every 10 px or so, 129×73
// nodes on the Smart Pro (97×55 under Low Power) — stepped with the textbook
// leapfrog wave equation at a fixed 30 Hz, so it moves the same at the 15–30
// fps the handheld runs at. Nothing is rasterised per pixel on the CPU: the
// backdrop's picture (the user's wallpaper, or a small texture of Home's own
// base and blooms baked once per theme) is drawn through a mesh with
// SDL_RenderGeometry, each vertex's texture coordinate pushed along the local
// slope (refraction). The light the ripples catch and the shadow on the slopes
// facing away are two small textures, a texel per node, that the GPU
// stretches over the finished backdrop with linear filtering (the shadow
// blended, the light added): drawn as vertex colours they were Gouraud
// triangles, and a ring's edge showed their staircase. Once the water is calm
// it is one plain copy.
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
//   - In space: the cells' diagonals alternate so no grain runs one way
//     across the screen, and the wallpaper is sampled with
//     linear filtering (gifwall.go appWallpaperTextureSmooth): a ripple is a
//     lens that enlarges the picture locally, and nearest sampling showed
//     its pixels as blocks.
//
// The tester came back after that: still pixelated, still not calming. The
// mesh was no longer the problem — the water was. Ripples 30 px wide on a
// grid with a node every 20–27 px were a node or two across, which a grid
// cannot carry: they rang as jagged, grid-aligned crumbs, and the sharp glints
// on them flickered from vertex to vertex. They also ran at ~400 px/s, 13 px
// a frame at the handheld's 30 fps, which reads as stutter however smoothly it
// is drawn. A numerical replay of the old and new water (frame-to-frame
// change of the picture, a 1280×720 canvas, the Low Power grid) went from a
// mean of 8.0 to 1.9 per channel with the changes below:
//
//   - Waves travel at a fixed share of the screen's width a second
//     (liquidWaveSpeed; the sim's k follows from its cell size), and a
//     little viscosity (liquidViscosity) damps the short wavelengths a grid
//     draws badly much faster than the long swells it draws well.
//   - Nothing smaller than liquidMinRadiusNodes grid nodes is dropped.
//   - A drop is pressed in over liquidPressSteps steps and mostly from rest
//     (liquidDropRest): pressed into one time level in one step, it was a
//     kick with every frequency in it, Nyquist's included, and popped in
//     within a frame.
//
// Then the water was calm but hard to see at all ("it doesn't seem to do any
// liquid animation"): bending Home's soft gradients shows next to nothing,
// the wallpaper's dim (drawAppBackdrop) took 60% of the shade, and the broad
// drops made fog rather than rings. So the grid became twice as fine (the
// mesh is the grid itself, the same vertex count as the old interpolated
// mesh), ripples are narrower rings again — now 3–8 nodes across instead of
// 1–2 — and the light and a real shadow are drawn over everything, dim
// included, from the textures above.
//
// Per frame that is a grid step or two, a blend, ~9,400 vertices and one
// geometry call plus two small texture uploads and copies — about a
// millisecond of the A53 for the vertices, the rest SDL expanding the
// ~55,000 indices (liquidBudgetMs watches the whole of it and falls back to a
// coarser grid).

import (
	"math"
	"math/rand"
	"strings"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// Settings: Custom keys and the values the level is stored as.
const (
	liquidKey       = "liquidBg"
	liquidResumeKey = "liquidBgResume"
	liquidTiltKey   = "liquidTilt"
)

// liquidLevel is how lively the water is.
type liquidLevel int

const (
	liquidLevelOff liquidLevel = iota
	liquidLevelSlight
	liquidLevelMedium
	liquidLevelLively
)

// liquidLevelValues and liquidLevelLabels are indexed by liquidLevel; the
// Settings row's stops and R2's cycle (liquidCycle) are in the same order.
// Medium is what was called Gentle ("gentle" still reads as Medium).
var (
	liquidLevelValues = [...]string{"off", "slight", "medium", "lively"}
	liquidLevelLabels = [...]string{"Off", "Slight", "Medium", "Lively"}
)

const (
	liquidCellPx  = 10.0 // the grid's target cell size, canvas pixels
	liquidMinCols = 48
	liquidMaxCols = 160
	// Low Power keeps the full grid since the refracting mesh became coarser
	// than the grid (liquidMeshStep): its 96 columns, a light texel every
	// 13 px stretched over the Smart Pro's screen, drew the ripples blurry.
	liquidLowPowerCols = 128
	// liquidDegradedCols is the grid once the budget has stepped the water
	// down (noteCost), when the mesh also thins to every fourth node.
	liquidDegradedCols = 96
	liquidMinRows      = 8
	// liquidSlopeRefCell is the cell size (px) the light and refraction gains
	// are tuned at; a slope measured per node on another grid is scaled by
	// liquidSlopeRefCell/cell, so a ripple looks the same on every grid.
	liquidSlopeRefCell = 13.33
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

	// liquidWaveSpeed is how fast a ripple spreads, in canvas widths a
	// second: 160 px/s on the 1280 px canvas, about 5 px a frame at 30 fps.
	// (100 px/s was tried and read as worse, not calmer.)
	// The sim's k = c²·dt²/dx² follows from it and the grid's cell size
	// (liquidWaveKFor), up to the stability cap — reached at about 128
	// columns, so a finer grid (a canvas past ~1290 px) runs a little slower.
	liquidWaveSpeed = 0.125
	// liquidViscosity damps a wave by its curvature: short waves, the ones a
	// grid draws as crumbs, die within a few steps, broad swells hardly
	// notice. The damped leapfrog stays stable while k + 2·viscosity ≤ 0.5;
	// liquidLeapfrogLimit keeps a margin under that, so k itself is capped
	// at liquidLeapfrogLimit − 2·liquidViscosity = 0.29.
	liquidViscosity     = 0.08
	liquidLeapfrogLimit = 0.45
	liquidMaxHeight     = 2.0
	// liquidSoftFrom is where a drop's height starts easing toward
	// liquidMaxHeight (liquidSoftHeight).
	liquidSoftFrom = 1.3
	// liquidMinRadiusNodes is the narrowest drop, in grid nodes: narrower
	// ones are too few nodes across to ring round instead of jagged.
	liquidMinRadiusNodes = 2.5
	// liquidMinWaveRadiusNodes is the narrowest wave drop (liquidShape): its
	// dip is half its radius and its rim finer still, so it needs more room.
	liquidMinWaveRadiusNodes = 4.0
	// liquidWaveScale widens a wave drop over the plain dip it replaces, so
	// its dip (half its radius) is about the plain dip's size.
	liquidWaveScale = 1.2
	// Every wave drop lands up to liquidJitter of its radius off the mark and
	// up to liquidSizeSpread larger or smaller and deeper or shallower.
	liquidJitter     = 0.12
	liquidSizeSpread = 0.15
	// A focus move rolls liquidSweepDrops wave drops along its path over
	// liquidSweepSteps steps (sweepTo), their depths adding up to
	// liquidSweepDepth times one drop: in a list the path is shorter than a
	// drop is wide, so the drops land on one another.
	liquidSweepDrops = 3
	liquidSweepSteps = 4.0
	liquidSweepDepth = 1.0
	// A drop that lands on the move (liquidDirectedShape) is stretched up to
	// liquidSpillStretch along its way, its rim leans liquidSpillLean of its
	// radius further ahead, and its older level is laid liquidSpillPush
	// nodes behind, which sets the water moving that way.
	liquidSpillStretch = 1.3
	liquidSpillLean    = 0.2
	liquidSpillPush    = 0.9
	// liquidPressSteps is how many steps a drop is pressed in over, and
	// liquidDropRest how much of it is applied to the older time level too,
	// so it starts mostly at rest: a drop pressed into one level in one step
	// is a kick that rings at every frequency the grid has.
	liquidPressSteps = 4
	liquidDropRest   = 0.7
	liquidMaxPresses = 48
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

	// Slopes are per node at liquidSlopeRefCell.
	liquidRefraction = 0.03 // texture fraction a unit slope shifts the picture
	liquidSlopeGain  = 5.5  // how steep a slope looks to the light
	liquidUVInset    = 0.0005
	// The light and shadow textures: what a fully lit slope adds (0–255,
	// before the soft knee), the glint on top, the shadow's gain and the
	// darkest it gets (alpha share of black), and what a crest (convex) adds
	// to the light and a trough (concave) to the shadow per unit of
	// curvature. The pale themes get more shadow and less light: white light
	// hardly shows on them.
	//
	// The crest and trough terms are what draw a ripple as rings of light:
	// lit by slope alone, a dip was one bright half and one dark half — the
	// "blurry… big blobs" on the Smart Pro — so the slope light is half what
	// it was and the curvature carries the shape.
	liquidDiffuse        = 110.0
	liquidGlint          = 110.0
	liquidShadow         = 1.1
	liquidShadowMax      = 0.6
	liquidCrest          = 900.0
	liquidTrough         = 4.0
	liquidDiffuseLight   = 60.0
	liquidGlintLight     = 60.0
	liquidShadowLight    = 1.6
	liquidShadowMaxLight = 0.45
	liquidCrestLight     = 500.0
	liquidTroughLight    = 5.0

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

// parseLiquidLevel reads a stored level. Only the level names (and the old
// "gentle", now Medium) turn the water on; anything else — empty, "on", a
// typo — is off.
func parseLiquidLevel(v interface{}) liquidLevel {
	s, ok := v.(string)
	if !ok {
		return liquidLevelOff
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "slight", "slightly":
		return liquidLevelSlight
	case "medium", "gentle":
		return liquidLevelMedium
	case "lively":
		return liquidLevelLively
	}
	return liquidLevelOff
}

// liquidLevelDefault is the level of a setup that never chose one: off. The
// water was on by default for a while; its owner asked for it off by default,
// switched on and stepped with R2 (liquidCycle), on every screen once on.
const liquidLevelDefault = liquidLevelOff

// liquidLevelFirstOn is the level a switch-on picks when no earlier level was
// saved (the Start menu's switch, liquidToggle).
const liquidLevelFirstOn = liquidLevelMedium

// liquidCycle is R2 on any screen where R2 has no job of its own
// (shoulders.go), and W on Home's keyboard: the next level, Off → Slight →
// Medium → Lively → Off, saved at once, with a toast naming it.
func liquidCycle(config *Config) {
	if config == nil {
		return
	}
	next := (liquidLevelOf(config) + 1) % liquidLevel(len(liquidLevelValues))
	liquidSetLevel(config, next)
	if next != liquidLevelOff && liquidUnsupportedSeen {
		setToast("Liquid background is not supported by this renderer")
		return
	}
	setToast("Liquid background: " + liquidLevelLabels[next])
}

// liquidLevelOf is config's level (appConfig's when config is nil): the
// stored one, or liquidLevelDefault when none is stored. An explicit "off"
// stays off.
func liquidLevelOf(config *Config) liquidLevel {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return liquidLevelOff
	}
	v, ok := config.Variables.Custom[liquidKey]
	if !ok || v == nil {
		return liquidLevelDefault
	}
	if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
		return liquidLevelDefault
	}
	return parseLiquidLevel(v)
}

// liquidSetLevel saves a level and remembers the active one when switching
// off, so the Start menu's one-press switch can restore the level it had.
func liquidSetLevel(config *Config, level liquidLevel) {
	if config == nil {
		return
	}
	if level == liquidLevelOff {
		if previous := liquidLevelOf(config); previous != liquidLevelOff {
			if config.Variables.Custom == nil {
				config.Variables.Custom = map[string]interface{}{}
			}
			config.Variables.Custom[liquidResumeKey] = liquidLevelValues[previous]
		}
	}
	setStoreString(config, liquidKey, liquidLevelValues[level])
}

// liquidToggle switches the backdrop from the Start menu without opening
// Settings. An unset preference starts at Medium, including after an old
// config with the water off is enabled for the first time.
func liquidToggle(config *Config) {
	if config == nil {
		return
	}
	next := liquidLevelOff
	if liquidLevelOf(config) == liquidLevelOff {
		next = parseLiquidLevel(config.Variables.Custom[liquidResumeKey])
		if next == liquidLevelOff {
			next = liquidLevelFirstOn
		}
	}
	liquidSetLevel(config, next)
	if next != liquidLevelOff && liquidUnsupportedSeen {
		setToast("Liquid background is not supported by this renderer")
		return
	}
	setToast("Liquid background: " + liquidLevelLabels[next])
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
	swell      float64 // the swell's strength while someone is around (updateSwell)
}

// liquidTuningFor picks the numbers. Reduce Motion — which the handheld's
// own config turns on by default, as does Battery Saver — calms the water
// without hiding it: no slosh, slightly shallower ripples that settle a little
// sooner, rain half as often. It used to keep no rain and 60% ripples, which
// on the handheld (Reduce Motion on from the factory) left a water the owner
// had asked for that "doesn't seem to do any liquid animation".
func liquidTuningFor(level liquidLevel, reduced, lowPower bool) liquidTuning {
	// Drops start mostly at rest, which sends out about half the wave a kick
	// did, so they are deeper than the 0.7 they once were.
	t := liquidTuning{damp: 0.987, drop: 0.95, wake: 0.16, wakeRadius: 30, rainMin: 2.0, rainMax: 5.0, slosh: 2.4, swell: 1.0}
	switch level {
	case liquidLevelSlight:
		// Slight is Medium turned down, not slowed down (a slower water was
		// tried and read as worse): shallower drops and wakes that settle a
		// little sooner, rain about half as often, a softer swell and slosh.
		t = liquidTuning{damp: 0.984, drop: 0.6, wake: 0.1, wakeRadius: 26, rainMin: 4.0, rainMax: 9.0, slosh: 1.4, swell: 0.55}
	case liquidLevelLively:
		// Lively is livelier in how long its waves last and how often it
		// rains, more than in depth: a drop much deeper under a big tile,
		// stepped onto again before it settles, rings into the height clamp
		// and draws flat-topped crests.
		t = liquidTuning{damp: 0.99, drop: 1.1, wake: 0.22, wakeRadius: 36, rainMin: 0.9, rainMax: 2.6, slosh: 3.2, swell: 1.4}
	}
	if lowPower {
		t.rainMin *= 1.5
		t.rainMax *= 1.5
	}
	if reduced {
		t.damp -= 0.004
		t.drop *= 0.85
		t.wake *= 0.85
		t.rainMin *= 2
		t.rainMax *= 2
		t.slosh = 0
		t.swell *= 0.6
	}
	return t
}

// liquidGridSize is the grid for a w×h canvas: square cells of about
// liquidCellPx, a coarser grid on Low Power and coarser still once the budget
// has stepped the water down.
func liquidGridSize(w, h int32, lowPower, degraded bool) (cols, rows int) {
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
	if degraded && cols > liquidDegradedCols {
		cols = liquidDegradedCols
	}
	// Even both ways, so the mesh can take every second node
	// (liquidMeshStep).
	cols -= cols % 2
	rows = int(math.Round(float64(cols) * float64(h) / float64(w)))
	if rows < liquidMinRows {
		rows = liquidMinRows
	}
	rows += rows % 2
	return cols, rows
}

// ── The water ────────────────────────────────────────────────────────────

// liquidSim is the height field: (cols+1)×(rows+1) nodes, two time levels for
// the leapfrog step and a third it writes the next one into (the viscosity
// reads the older level's neighbours, so it cannot be overwritten in place).
// The border nodes stay at zero, so waves reflect off the screen's edges.
type liquidSim struct {
	cols, rows      int
	stride          int
	k               float32 // c²·dt²/dx² for this grid (liquidWaveKFor)
	cur, prev, next []float32
	maxAbs          float32 // the largest |height| after the last step
	// presses are drops still being pressed in (press), a share each step.
	presses []liquidPress
}

// liquidPress is a drop being pressed in: after delay steps, a share of its
// depth each step for steps more steps.
type liquidPress struct {
	x, y, radius, share float32
	steps               int
	delay               int // steps still to wait before the first share
	shape               liquidShape
}

// liquidWaveKFor is the sim's k for a grid of cols columns across a canvas w
// pixels wide: waves at liquidWaveSpeed whatever the cell size, held inside
// the damped, viscous leapfrog's stability limit.
func liquidWaveKFor(w int32, cols int) float32 {
	if w <= 0 || cols <= 0 {
		return 0
	}
	cell := float64(w) / float64(cols)
	c := liquidWaveSpeed * float64(w) / (cell * liquidStepHz) // nodes per step
	return float32(math.Min(c*c, liquidLeapfrogLimit-2*liquidViscosity))
}

func newLiquidSim(cols, rows int, k float32) *liquidSim {
	if cols < 2 || rows < 2 {
		return nil
	}
	n := (cols + 1) * (rows + 1)
	return &liquidSim{cols: cols, rows: rows, stride: cols + 1, k: k,
		cur: make([]float32, n), prev: make([]float32, n), next: make([]float32, n)}
}

// step advances the water by one tick, after pressing in its share of every
// drop still being pressed: next = 2·now − before + k·∇²now + ν·∇²(now −
// before), all of it scaled by damp, which settles both the waves and any
// leftover offset. The ν term is viscosity: it damps a wave by how sharply it
// curves, so the grid's shortest waves fade first. The levels then rotate.
func (s *liquidSim) step(damp float32) {
	s.pressStep()
	cur, prev, next, st := s.cur, s.prev, s.next, s.stride
	kw := s.k
	const nu = float32(liquidViscosity)
	var peak float32
	for j := 1; j < s.rows; j++ {
		row := j * st
		for i := 1; i < s.cols; i++ {
			k := row + i
			c, b := cur[k], prev[k]
			lap := cur[k-1] + cur[k+1] + cur[k-st] + cur[k+st] - 4*c
			lapB := prev[k-1] + prev[k+1] + prev[k-st] + prev[k+st] - 4*b
			v := (2*c - b + kw*lap + nu*(lap-lapB)) * damp
			if v > liquidMaxHeight {
				v = liquidMaxHeight
			} else if v < -liquidMaxHeight {
				v = -liquidMaxHeight
			}
			next[k] = v
			if v > peak {
				peak = v
			} else if -v > peak {
				peak = -v
			}
		}
	}
	// The border of next is zero: it was made zero, and only the inside is
	// ever written (drop clamps to the inside too).
	s.prev, s.cur, s.next = cur, next, prev
	s.maxAbs = peak
}

// press queues a round drop to be pressed in over liquidPressSteps steps
// (pressShaped with the plain shape and no delay).
func (s *liquidSim) press(x, y, radius, depth float32) {
	s.pressShaped(x, y, radius, depth, liquidShape{}, 0)
}

// pressShaped queues a drop of the given shape, pressed in over
// liquidPressSteps steps starting delay steps from now. A plain drop next to
// the newest one, queued since the last step with the same size (a wake from
// a fast mouse, several frames a step), joins it instead of queueing; with the
// queue full anyway a plain drop lands at once and a wave is skipped.
func (s *liquidSim) pressShaped(x, y, radius, depth float32, shape liquidShape, delay int) {
	if s == nil || depth == 0 || radius <= 0 {
		return
	}
	if n := len(s.presses); n > 0 && delay == 0 && !shape.wave {
		last := &s.presses[n-1]
		if last.steps == liquidPressSteps && last.delay == 0 && !last.shape.wave && last.radius == radius &&
			math.Abs(float64(last.x-x)) < 1 && math.Abs(float64(last.y-y)) < 1 {
			last.share += depth / liquidPressSteps
			return
		}
	}
	if len(s.presses) >= liquidMaxPresses {
		// A wave lands with the next one; a plain wake drop lands at once.
		if !shape.wave {
			s.drop(x, y, radius, depth, shape)
		}
		return
	}
	s.presses = append(s.presses, liquidPress{x: x, y: y, radius: radius,
		share: depth / liquidPressSteps, steps: liquidPressSteps, delay: delay, shape: shape})
}

// pressStep presses in one share of every queued drop whose delay is over.
func (s *liquidSim) pressStep() {
	n := 0
	for _, pr := range s.presses {
		if pr.delay > 0 {
			pr.delay--
		} else {
			s.drop(pr.x, pr.y, pr.radius, pr.share, pr.shape)
			pr.steps--
		}
		if pr.steps > 0 {
			s.presses[n] = pr
			n++
		}
	}
	s.presses = s.presses[:n]
}

// pressing reports drops still to be pressed in.
func (s *liquidSim) pressing() bool {
	return s != nil && len(s.presses) > 0
}

// liquidShape is how a drop departs from a plain round dip. The zero value is
// the plain dip: a raised cosine `radius` nodes wide, what a stirrer's wake
// presses. A wave drop is a dip with a raised rim around it (a Ricker
// wavelet, reaching 1.6 radii), so it sends out a short train of crests
// instead of one ring; its outline is an ellipse (elong along rot) with a
// wobble of the second and third harmonics, so no two are the same circle.
type liquidShape struct {
	wave   bool
	a2, a3 float32 // wobble: the amplitudes of cos 2(θ−p2) and cos 3(θ−p3)
	elong  float32 // the ellipse's stretch (1 = round)
	// cos and sin of 2·p2, 3·p3 and the ellipse's angle, worked out once
	// (liquidRandomShape): liquidShapeWeight runs for every node a drop
	// covers, every step it is pressed.
	c2, s2, c3, s3, cr, sr float32
	// A directed drop (liquidDirectedShape) lands moving: (c1, s1) is the
	// way it travels, a1 how much further its rim reaches ahead than behind,
	// and push how many nodes behind its older time level is laid, which
	// gives the water a shove that way — a spill, not a drip from above.
	a1, c1, s1, push float32
}

// liquidWaveReach is how far a wave drop reaches, in radii.
const liquidWaveReach = 1.6

// liquidRandomShape is a wave drop's shape, different every time.
func liquidRandomShape() liquidShape {
	p2 := 2 * math.Pi * rand.Float64()
	p3 := 2 * math.Pi * rand.Float64()
	rot := math.Pi * rand.Float64()
	return liquidShape{
		wave:  true,
		a2:    0.05 + 0.11*rand.Float32(),
		a3:    0.03 + 0.07*rand.Float32(),
		elong: 0.8 + 0.45*rand.Float32(),
		c2:    float32(math.Cos(2 * p2)),
		s2:    float32(math.Sin(2 * p2)),
		c3:    float32(math.Cos(3 * p3)),
		s3:    float32(math.Sin(3 * p3)),
		cr:    float32(math.Cos(rot)),
		sr:    float32(math.Sin(rot)),
	}
}

// liquidDirectedShape is a wave drop landing on the move along (ux, uy), a
// unit vector: stretched along the way it came, its rim reaching further
// ahead, and laid with a shove that way (liquidShape.push), so a focus that
// arrives from the right spills its wave on to the left. strength (0–1)
// scales how far the spill leans; the wobble stays random.
func liquidDirectedShape(ux, uy, strength float64) liquidShape {
	sh := liquidRandomShape()
	n := math.Hypot(ux, uy)
	if n < 1e-6 || strength <= 0 {
		return sh
	}
	ux, uy = ux/n, uy/n
	strength = math.Min(1, strength)
	sh.cr, sh.sr = float32(ux), float32(uy)
	sh.elong = float32(1 + (liquidSpillStretch-1+0.15*rand.Float64())*strength)
	sh.c1, sh.s1 = float32(ux), float32(uy)
	sh.a1 = float32(liquidSpillLean * strength)
	sh.push = float32(liquidSpillPush * strength)
	return sh
}

// liquidShapeWeight is the drop's profile at offset (dx, dy) nodes from its
// centre, for a drop radius nodes wide: 0 outside it.
func liquidShapeWeight(dx, dy, radius float32, sh liquidShape) float32 {
	if !sh.wave {
		d2 := (dx*dx + dy*dy) / (radius * radius)
		if d2 >= 1 {
			return 0
		}
		return float32(0.5 * (1 + math.Cos(math.Pi*math.Sqrt(float64(d2)))))
	}
	rho := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	wob := float32(1)
	if rho > 1e-4 {
		c, s := dx/rho, dy/rho
		cos2, sin2 := c*c-s*s, 2*c*s
		cos3, sin3 := 4*c*c*c-3*c, 3*s-4*s*s*s
		wob += sh.a1*(c*sh.c1+s*sh.s1) + sh.a2*(cos2*sh.c2+sin2*sh.s2) + sh.a3*(cos3*sh.c3+sin3*sh.s3)
	}
	u, v := dx*sh.cr+dy*sh.sr, -dx*sh.sr+dy*sh.cr
	el := sh.elong
	if el <= 0 {
		el = 1
	}
	d := float32(math.Sqrt(float64(u*u/(el*el)+v*v*el*el))) / (wob * radius)
	if d >= liquidWaveReach {
		return 0
	}
	// Ricker: (1 − 2q²)·e^(−q²) with q = 2d/√2, eased to zero over the last
	// 0.4 radii so the rim has no edge.
	q2 := 2 * d * d
	w := (1 - q2) * float32(math.Exp(-float64(q2)))
	if fade := (liquidWaveReach - d) / 0.4; fade < 1 {
		w *= fade
	}
	return w
}

// drop presses a smooth dip of the given depth and shape into the water at
// (x, y), in node coordinates. A negative depth raises a bump. The current
// level takes all of it and the one before liquidDropRest of it, so the dip
// starts nearly at rest and rings outward as it springs back, without the
// kick that rang at every frequency.
func (s *liquidSim) drop(x, y, radius, depth float32, shape liquidShape) {
	if s == nil || depth == 0 || radius <= 0 {
		return
	}
	// Narrower than this the dip is a node or two across: a ripple the grid
	// cannot draw round, which rings as jagged crumbs instead. A wave drop's
	// rim is finer than its dip, so it needs more room.
	minR := float32(liquidMinRadiusNodes)
	reach := float32(1)
	if shape.wave {
		// The wobble and the stretch carry the rim further out along some
		// directions: the box must hold all of it, or the rim ends in an edge.
		minR = liquidMinWaveRadiusNodes
		el := shape.elong
		if el <= 0 {
			el = 1
		}
		wobble := math.Abs(float64(shape.a1)) + math.Abs(float64(shape.a2)) + math.Abs(float64(shape.a3))
		reach = liquidWaveReach * (1 + float32(wobble)) * max(el, 1/el)
	}
	if radius < minR {
		radius = minR
	}
	// The older level's copy sits push nodes behind: the box holds both.
	push := float32(math.Abs(float64(shape.push)))
	bx, by := shape.push*shape.c1, shape.push*shape.s1
	ext := radius*reach + push
	i0 := int(math.Floor(float64(x - ext)))
	i1 := int(math.Ceil(float64(x + ext)))
	j0 := int(math.Floor(float64(y - ext)))
	j1 := int(math.Ceil(float64(y + ext)))
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
	ext2 := ext * ext
	for j := j0; j <= j1; j++ {
		dy := float32(j) - y
		for i := i0; i <= i1; i++ {
			dx := float32(i) - x
			if dx*dx+dy*dy >= ext2 {
				continue
			}
			w := liquidShapeWeight(dx, dy, radius, shape)
			wb := w
			if push != 0 {
				wb = liquidShapeWeight(dx+bx, dy+by, radius, shape)
			}
			if w == 0 && wb == 0 {
				continue
			}
			k := j*s.stride + i
			s.cur[k] = liquidSoftHeight(s.cur[k] - depth*w)
			s.prev[k] = liquidSoftHeight(s.prev[k] - depth*wb*liquidDropRest)
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

// liquidSoftHeight is what a drop leaves at a node: heights past
// liquidSoftFrom ease toward liquidMaxHeight instead of stopping at it, so
// drops landing on one another (a quick run down a list, a splash on a
// fresh ripple) round off instead of drawing a flat-bottomed dip.
func liquidSoftHeight(v float32) float32 {
	a := v
	if a < 0 {
		a = -a
	}
	if a <= liquidSoftFrom {
		return v
	}
	room := float32(liquidMaxHeight - liquidSoftFrom)
	over := a - liquidSoftFrom
	a = liquidSoftFrom + over/(1+over/room)
	if v < 0 {
		return -a
	}
	return a
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
		s.next[i] = 0
	}
	s.maxAbs = 0
	s.presses = s.presses[:0]
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
	return math.Max(28, math.Min(58, minSide*0.3))
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
	step           int // liquidMeshStep
	u0, v0, u1, v1 float32
	lx, ly         float32 // the light's tilt shift
	tint           sdl.Color
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
	// preview: the next update drops the splash a level just switched on or
	// changed shows itself with.
	preview bool

	level  liquidLevel
	tuning liquidTuning
	sim    *liquidSim
	w, h   int32
	low    bool // the grid is the Low Power one
	coarse bool // the grid is the budget's coarse one (degraded)

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
	focusSide           float64 // the focused control's shorter side, px
	focusDropAt         float64 // clock of the last focus ripple

	// A navigation press waiting to see whether the focus ring answers it
	// with a ripple of its own (liquidNoteEvent, updateNav).
	navPending   bool
	navAt        float64
	navDX, navDY float64

	// The swell (updateSwell, surface): swellAmp eases toward the tuning's
	// strength while someone is around and back to nothing after; its waves
	// are picked when the grid is made. swS*/swC* are the waves' sines and
	// cosines along x and along y, per wave, rebuilt each frame.
	swellAmp   float64
	swellWaves [liquidSwellCount]liquidSwellWave
	swellSet   bool
	swSX, swCX []float32
	swSY, swCY []float32

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
	// the two newest time levels blended by it into blend (surface).
	alpha float32
	blend []float32

	verts            []sdl.Vertex
	idx              []int32
	idxCols, idxRows int // the mesh idx was built for
	gridCols         int // the grid the light textures were built for
	gridRows         int
	meshKey          liquidMeshKey
	meshOK           bool

	// The light and the shadow, a texel per node in R,G,B,A byte order
	// (glowPix, darkPix), uploaded to glowTex and darkTex whenever the mesh
	// is rebuilt; lit says whether the last build had any light or shadow
	// in it at all.
	glowPix, darkPix []byte
	glowTex, darkTex *sdl.Texture
	litW, litH       int32 // the textures' size
	litFailed        bool
	lit              bool

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
	if p.baked == nil && p.sim == nil && p.verts == nil && p.idx == nil &&
		p.glowTex == nil && p.darkTex == nil && p.glowPix == nil {
		return
	}
	mine := p.renderer != nil && p.renderer == renderer
	if p.baked != nil && mine {
		_ = p.baked.Destroy()
	}
	p.baked, p.bakedKey, p.bakeFailed = nil, liquidBakeKey{}, false
	p.dropLitTextures(mine)
	p.glowPix, p.darkPix = nil, nil
	p.sim = nil
	p.verts, p.idx = nil, nil
	p.blend = nil
	p.idxCols, p.idxRows = 0, 0
	p.gridCols, p.gridRows = 0, 0
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
	s.press(gx, gy, float32(radiusPx/cell), depth)
	p.disturb(now)
}

// waveAt drops a wave at canvas pixel (x, y) after delay steps: a dip with a
// rim, never quite round (liquidShape), its place, size and depth a little
// different every time so no two look stamped from one mould.
func (p *liquidPoolState) waveAt(x, y, radiusPx float64, depth float32, delay int, now float64) {
	p.waveShaped(x, y, radiusPx, depth, delay, liquidRandomShape(), now)
}

// waveShaped is waveAt with the shape chosen by the caller (a directed one,
// liquidDirectedShape).
func (p *liquidPoolState) waveShaped(x, y, radiusPx float64, depth float32, delay int, shape liquidShape, now float64) {
	s := p.sim
	if s == nil || p.w <= 0 || p.h <= 0 || depth == 0 {
		return
	}
	j := liquidJitter
	x += (rand.Float64()*2 - 1) * j * radiusPx
	y += (rand.Float64()*2 - 1) * j * radiusPx
	radiusPx *= liquidWaveScale * (1 + (rand.Float64()*2-1)*liquidSizeSpread)
	depth *= float32(1 + (rand.Float64()*2-1)*liquidSizeSpread)
	cell := float64(p.w) / float64(s.cols)
	gx := float32(x / float64(p.w) * float64(s.cols))
	gy := float32(y / float64(p.h) * float64(s.rows))
	s.pressShaped(gx, gy, float32(radiusPx/cell), depth, shape, delay)
	p.disturb(now)
}

// sweepTo rolls a wave along the way the focus went, from (x0, y0) to (x1, y1):
// a few wave drops along the path, each a little later, deeper and wider than
// the one before, ending under the new focus. Each lands on the move
// (liquidDirectedShape), leaning and shoving the way the focus went, the last
// the most, so a focus arriving from the right spills its wave on to the
// left instead of dripping straight down.
func (p *liquidPoolState) sweepTo(x0, y0, x1, y1, radiusPx float64, depth float32, now float64) {
	weight := func(f float64) float64 { return 0.45 + 0.55*f }
	total := 0.0
	for q := 1; q <= liquidSweepDrops; q++ {
		total += weight(float64(q) / liquidSweepDrops)
	}
	for q := 1; q <= liquidSweepDrops; q++ {
		f := float64(q) / liquidSweepDrops
		x := x0 + (x1-x0)*f
		y := y0 + (y1-y0)*f
		delay := int(math.Round(f * liquidSweepSteps))
		share := float32(weight(f) / total * liquidSweepDepth)
		shape := liquidDirectedShape(x1-x0, y1-y0, 0.5+0.5*f)
		p.waveShaped(x, y, radiusPx*(0.7+0.3*f), depth*share, delay, shape, now)
	}
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
	if p.sim == nil || p.w != w || p.h != h || p.low != low || p.coarse != p.degraded {
		cols, rows := liquidGridSize(w, h, low, p.degraded)
		p.sim = newLiquidSim(cols, rows, liquidWaveKFor(w, cols))
		p.w, p.h, p.low, p.coarse = w, h, low, p.degraded
		p.swellSet = false
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
	p.updateNav(now)
	p.updateSticks(config, now, dt)
	p.updateMouse(now, dt)
	p.updateTilt(config, now)
	p.updateRain(now)
	p.updateSwell(now, dt)

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
			if p.sim.maxAbs < liquidCalmLevel && now-p.lastStir > liquidCalmHold && !p.sim.pressing() &&
				p.swellAmp < liquidSwellRest {
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
	p.focusSide = minSide
	if liquidFocusMoved(p.rippleX, p.rippleY, p.haveRipple, cx, cy, minSide) {
		r := liquidRippleRadius(minSide)
		if p.haveRipple && math.Hypot(cx-p.rippleX, cy-p.rippleY) < 0.6*float64(p.w) {
			p.sweepTo(p.rippleX, p.rippleY, cx, cy, r, p.tuning.drop, now)
		} else {
			p.waveAt(cx, cy, r, p.tuning.drop, 0, now)
		}
		p.rippleX, p.rippleY, p.haveRipple = cx, cy, true
		p.lastUser = now
		p.focusDropAt = now
		p.navPending = false
	}
}

// liquidNavWait is how long a navigation press waits for the focus ring to
// answer with its ripple before it drops one itself: a frame or three.
const liquidNavWait = 0.12

// updateNav drops the ripple of a navigation press the focus ring did not
// answer — a screen whose highlight is not a glass focus ring, a list that
// scrolled under a still highlight, an edge the focus could not pass —
// where the focus is, pushed the way the press pointed.
func (p *liquidPoolState) updateNav(now float64) {
	if !p.navPending || now-p.navAt < liquidNavWait {
		return
	}
	p.navPending = false
	if p.focusDropAt >= p.navAt {
		return
	}
	x, y, side := float64(p.w)/2, float64(p.h)/2, 120.0
	if p.haveFocus {
		x, y, side = p.focusX, p.focusY, p.focusSide
	}
	r := liquidRippleRadius(side)
	x = clampFloat(x+p.navDX*r, 0, float64(p.w-1))
	y = clampFloat(y+p.navDY*r, 0, float64(p.h-1))
	p.waveShaped(x, y, r*0.8, p.tuning.drop*0.8, 0, liquidDirectedShape(p.navDX, p.navDY, 0.8), now)
	p.lastUser = now
}

// liquidNavKind is what a press means to the water.
type liquidNavKind int

const (
	liquidNavNone   liquidNavKind = iota // not a press the water answers
	liquidNavMove                        // a step: the focus ring's ripple, or updateNav's
	liquidNavSelect                      // A / Enter / a click: a splash
	liquidNavBack                        // B / Escape: the water swells up instead of dipping
)

// liquidPressKind sorts a press, with the direction of a step.
func liquidPressKind(event sdl.Event) (kind liquidNavKind, dx, dy float64) {
	switch e := event.(type) {
	case *sdl.KeyboardEvent:
		if e.Type != sdl.KEYDOWN {
			return liquidNavNone, 0, 0
		}
		switch e.Keysym.Sym {
		case sdl.K_UP:
			return liquidNavMove, 0, -1
		case sdl.K_DOWN:
			return liquidNavMove, 0, 1
		case sdl.K_LEFT:
			return liquidNavMove, -1, 0
		case sdl.K_RIGHT:
			return liquidNavMove, 1, 0
		case sdl.K_TAB, sdl.K_PAGEUP, sdl.K_PAGEDOWN:
			return liquidNavMove, 0, 0
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			if e.Repeat != 0 {
				return liquidNavNone, 0, 0
			}
			return liquidNavSelect, 0, 0
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			if e.Repeat != 0 {
				return liquidNavNone, 0, 0
			}
			return liquidNavBack, 0, 0
		}
	case *sdl.ControllerButtonEvent:
		if e.Type != sdl.CONTROLLERBUTTONDOWN {
			return liquidNavNone, 0, 0
		}
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			return liquidNavMove, 0, -1
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			return liquidNavMove, 0, 1
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			return liquidNavMove, -1, 0
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			return liquidNavMove, 1, 0
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			return liquidNavMove, -1, 0
		case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
			return liquidNavMove, 1, 0
		case sdl.CONTROLLER_BUTTON_A:
			return liquidNavSelect, 0, 0
		case sdl.CONTROLLER_BUTTON_B:
			return liquidNavBack, 0, 0
		}
	}
	return liquidNavNone, 0, 0
}

// liquidNoteEvent is the main loop's hook for every input event: moving
// around the app stirs the water even where no focus ring moves, choosing
// something splashes under it, and going back swells the water up under the
// focus. A mouse click splashes where it lands. It does nothing unless the
// water was drawn a moment ago — a press under a game, a video or a
// full-screen page must not leave a ripple waiting for the backdrop's return.
func liquidNoteEvent(event sdl.Event) {
	p := &liquidPool
	if !p.on || p.sim == nil || event == nil {
		return
	}
	now := liquidClock()
	if now-p.updatedAt > 0.25 {
		return
	}
	if m, ok := event.(*sdl.MouseButtonEvent); ok {
		if m.Type == sdl.MOUSEBUTTONDOWN && m.Button == sdl.BUTTON_LEFT {
			x, y := physicalToLogical(m.X, m.Y)
			p.waveAt(float64(x), float64(y), liquidRippleRadius(90)*1.2, p.tuning.drop*1.2, 0, now)
			p.lastUser = now
		}
		return
	}
	kind, dx, dy := liquidPressKind(event)
	x, y, side := float64(p.w)/2, float64(p.h)/2, 120.0
	if p.haveFocus {
		x, y, side = p.focusX, p.focusY, p.focusSide
	}
	switch kind {
	case liquidNavMove:
		p.navPending, p.navAt, p.navDX, p.navDY = true, now, dx, dy
		p.lastUser = now
	case liquidNavSelect:
		p.waveAt(x, y, liquidRippleRadius(side)*1.25, p.tuning.drop*1.1, 0, now)
		p.lastUser = now
	case liquidNavBack:
		p.waveAt(x, y, liquidRippleRadius(side)*1.4, -p.tuning.drop*0.9, 0, now)
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
	// A small wave drop: a ring or two, wide enough for the grid to draw
	// round, narrow enough to read as rain and not a swell.
	r, depth := 22+rand.Float64()*12, float32(0.4+rand.Float64()*0.25)
	if p.degraded {
		// On the budget's coarse grid a wave drop's minimum size is wider
		// than a raindrop should be: a plain dip reads as rain there.
		p.dropAt(x, y, r, depth, now)
	} else {
		p.waveAt(x, y, r, depth, 0, now)
	}
	p.nextRain = now + t.rainMin + rand.Float64()*(t.rainMax-t.rainMin)
}

// ── The swell ────────────────────────────────────────────────────────────

// A few long waves rolling across the whole pool while someone is around,
// so between the ripples the water still moves as water — the drops ride on
// it instead of landing on glass. It is not simulated: it is a sum of
// travelling sines added to the surface as drawn (surface), so it costs a few
// multiply-adds a node and never rings into the grid. It eases out
// liquidSwellHold seconds after the last touch, and the water can rest again.
const (
	liquidSwellCount = 3
	liquidSwellHold  = 25.0 // seconds after the last touch before it eases out
	liquidSwellEase  = 0.5  // strength a second it eases in and out by
	// liquidSwellRest is the strength under which the swell no longer keeps
	// the water awake.
	liquidSwellRest = 0.01
)

// liquidSwellWave is one of the swell's waves, in canvas pixels.
type liquidSwellWave struct {
	kx, ky float64 // wave vector, radians a pixel
	omega  float64 // radians a second
	phase  float64
	amp    float64 // height at full strength
}

// liquidSwellWavesFor picks the swell's waves for a w-pixel-wide canvas: one
// main direction, the other two well off it, wavelengths of a quarter to
// nearly half the screen, drifting at 3–4% of its width a second.
func liquidSwellWavesFor(w int32) [liquidSwellCount]liquidSwellWave {
	var out [liquidSwellCount]liquidSwellWave
	scale := float64(w) / 1280
	if scale <= 0 {
		scale = 1
	}
	base := 2 * math.Pi * rand.Float64()
	offsets := [liquidSwellCount]float64{0, 2.2, 3.9}
	amps := [liquidSwellCount]float64{0.06, 0.05, 0.04}
	for n := range out {
		ang := base + offsets[n] + (rand.Float64()*2-1)*0.4
		lambda := (300 + 260*rand.Float64()) * scale
		speed := (30 + 20*rand.Float64()) * scale
		k := 2 * math.Pi / lambda
		out[n] = liquidSwellWave{
			kx:    k * math.Cos(ang),
			ky:    k * math.Sin(ang),
			omega: k * speed,
			phase: 2 * math.Pi * rand.Float64(),
			amp:   amps[n],
		}
	}
	return out
}

// updateSwell eases the swell toward the tuning's strength while someone has
// touched anything in the last liquidSwellHold seconds, and toward nothing
// after; while it is up the water is never calm.
func (p *liquidPoolState) updateSwell(now, dt float64) {
	if !p.swellSet {
		p.swellWaves = liquidSwellWavesFor(p.w)
		p.swellSet = true
	}
	target := 0.0
	if p.tuning.swell > 0 && now-p.lastUser < liquidSwellHold {
		target = p.tuning.swell
	}
	step := liquidSwellEase * dt
	switch {
	case p.swellAmp < target:
		p.swellAmp = math.Min(target, p.swellAmp+step)
	case p.swellAmp > target:
		p.swellAmp = math.Max(target, p.swellAmp-step)
	}
	if p.swellAmp < liquidSwellRest && target == 0 {
		p.swellAmp = 0
		return
	}
	p.calm = false
	p.dirty = true
}

// addSwell adds the swell at time t to the blended surface: sin(A + B) =
// sin A·cos B + cos A·sin B, A along x and B along y, so the sines are taken
// per column and per row, not per node.
func (p *liquidPoolState) addSwell(height []float32, t float64) {
	s := p.sim
	if p.swellAmp <= 0 || s == nil || p.w <= 0 || p.h <= 0 {
		return
	}
	nc, nr := s.cols+1, s.rows+1
	if len(p.swSX) != liquidSwellCount*nc {
		p.swSX = make([]float32, liquidSwellCount*nc)
		p.swCX = make([]float32, liquidSwellCount*nc)
	}
	if len(p.swSY) != liquidSwellCount*nr {
		p.swSY = make([]float32, liquidSwellCount*nr)
		p.swCY = make([]float32, liquidSwellCount*nr)
	}
	dx := float64(p.w) / float64(s.cols)
	dy := float64(p.h) / float64(s.rows)
	for n, wv := range p.swellWaves {
		a := wv.amp * p.swellAmp
		for i := 0; i < nc; i++ {
			sn, cs := math.Sincos(wv.kx * float64(i) * dx)
			p.swSX[n*nc+i], p.swCX[n*nc+i] = float32(sn*a), float32(cs*a)
		}
		for j := 0; j < nr; j++ {
			sn, cs := math.Sincos(wv.ky*float64(j)*dy - wv.omega*t + wv.phase)
			p.swSY[n*nr+j], p.swCY[n*nr+j] = float32(sn), float32(cs)
		}
	}
	for j := 0; j < nr; j++ {
		row := height[j*nc : (j+1)*nc]
		for n := 0; n < liquidSwellCount; n++ {
			sy, cy := p.swSY[n*nr+j], p.swCY[n*nr+j]
			sx, cx := p.swSX[n*nc:(n+1)*nc], p.swCX[n*nc:(n+1)*nc]
			for i := range row {
				row[i] += sx[i]*cy + cx[i]*sy
			}
		}
	}
}

// noteCost folds the previous frame's cost into the average and, on the
// handheld, steps down to the coarse grid (liquidDegradedCols) for the rest
// of the session when the water keeps going over budget.
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
		logInfo("liquidbg", "the water costs %.1f ms a frame; using the coarse grid", p.costEMA)
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
		if level == liquidLevelOff {
			p.preview = false
			if renderer != nil {
				p.release(renderer)
			}
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
		p.dropLitTextures(false)
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
	// A level just switched on, or changed (R2), shows itself with a splash.
	if !p.on || p.level != level {
		p.preview = true
	}
	p.on = true
	p.level = level
	if now := liquidClock(); liquidUpdateDue(p.framed, p.frame, frameCount, p.updatedAt, now) {
		p.framed, p.frame, p.updatedAt = true, frameCount, now
		p.epoch++
		liquidUpdate(config, w, h, now)
		if p.preview && p.sim != nil {
			p.preview = false
			// Give the setting an immediate, gentle preview instead of leaving
			// the water looking static until focus moves or the first raindrop.
			// liquidTuningFor already scales this down for Reduce Motion.
			radius := math.Min(80, float64(min(w, h))*0.1)
			p.waveAt(float64(w)/2, float64(h)/2, radius, p.tuning.drop, 0, now)
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
	if p.buildMesh(w, h, u0, v0, u1, v1) && p.lit {
		// Flat water has nothing to light, and nothing is drawn from the
		// textures while lit is false.
		p.uploadLit(renderer)
	}
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

// liquidLitRect is where a texture with a texel per node of a cols×rows grid
// over a w×h canvas is drawn so each texel's centre lands on its node: half a
// cell past every edge.
func liquidLitRect(w, h int32, cols, rows int) sdl.FRect {
	if cols <= 0 || rows <= 0 {
		return sdl.FRect{}
	}
	cw, ch := float32(w)/float32(cols), float32(h)/float32(rows)
	return sdl.FRect{X: -cw / 2, Y: -ch / 2, W: float32(w) + cw, H: float32(h) + ch}
}

// liquidBackdropLight lays the shadow and the light the ripples catch over
// the finished backdrop — over the wallpaper's dim and the blooms too, which
// is what keeps the water visible on a dimmed wallpaper: the shadow texture
// blended (black, alpha by depth of shadow), then the light added. Both are
// stretched by the GPU with linear filtering; under a clip (the header band's
// repaint) the GPU fills only the band.
func liquidBackdropLight(renderer *sdl.Renderer) {
	p := &liquidPool
	if !p.on || p.lastDrew == liquidDrewNothing || p.calm || !p.lit || renderer == nil ||
		renderer != p.renderer || p.glowTex == nil || p.darkTex == nil || p.sim == nil {
		return
	}
	dst := liquidLitRect(p.w, p.h, p.gridCols, p.gridRows)
	if dst.W <= 0 || dst.H <= 0 {
		return
	}
	start := time.Now()
	err := renderer.CopyF(p.darkTex, nil, &dst)
	if err == nil {
		err = renderer.CopyF(p.glowTex, nil, &dst)
	}
	p.frameCost += time.Since(start)
	if err != nil {
		p.giveUpLit("draw", err)
	}
}

// giveUpLit stops drawing the light and shadow on this renderer at this grid
// size (refraction alone goes on): the textures are destroyed, and uploadLit
// does not try again until the renderer or the grid changes.
func (p *liquidPoolState) giveUpLit(what string, err error) {
	p.dropLitTextures(true)
	p.litW, p.litH = int32(p.gridCols+1), int32(p.gridRows+1)
	p.litFailed = true
	p.lit = false
	logWarn("liquidbg", "cannot %s the water's light (%v); refraction only", what, err)
}

// dropLitTextures forgets the light textures, destroying them only when they
// belong to the renderer still drawing (mine).
func (p *liquidPoolState) dropLitTextures(mine bool) {
	if mine {
		if p.glowTex != nil {
			_ = p.glowTex.Destroy()
		}
		if p.darkTex != nil {
			_ = p.darkTex.Destroy()
		}
	}
	p.glowTex, p.darkTex = nil, nil
	p.litW, p.litH = 0, 0
	p.litFailed = false
}

// uploadLit hands the light and shadow built with the mesh to their
// textures, (re)making them when the grid's size changed.
func (p *liquidPoolState) uploadLit(renderer *sdl.Renderer) {
	tw, th := int32(p.gridCols+1), int32(p.gridRows+1)
	n := int(tw) * int(th) * 4
	if renderer == nil || p.gridCols <= 0 || p.gridRows <= 0 || len(p.glowPix) != n || len(p.darkPix) != n {
		return
	}
	if p.glowTex == nil || p.darkTex == nil || p.litW != tw || p.litH != th {
		if p.litFailed && p.litW == tw && p.litH == th {
			return
		}
		p.dropLitTextures(true)
		p.litW, p.litH = tw, th
		p.glowTex = liquidLitTexture(renderer, tw, th, sdl.BLENDMODE_ADD)
		p.darkTex = liquidLitTexture(renderer, tw, th, sdl.BLENDMODE_BLEND)
		if p.glowTex == nil || p.darkTex == nil {
			p.giveUpLit("make", sdl.GetError())
			return
		}
	}
	start := time.Now()
	err := p.glowTex.Update(nil, unsafe.Pointer(&p.glowPix[0]), int(tw)*4)
	if err == nil {
		err = p.darkTex.Update(nil, unsafe.Pointer(&p.darkPix[0]), int(tw)*4)
	}
	p.frameCost += time.Since(start)
	if err != nil {
		p.giveUpLit("upload", err)
	}
}

// liquidLitTexture makes one light texture: R,G,B,A bytes, streamed, sampled
// linearly so the GPU's stretch is smooth.
func liquidLitTexture(renderer *sdl.Renderer, w, h int32, mode sdl.BlendMode) *sdl.Texture {
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STREAMING, w, h)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return nil
	}
	_ = tex.SetBlendMode(mode)
	return tex
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

// surface is the water as the frame draws it, in p.blend: the two newest time
// levels blended by alpha — the leapfrog keeps both, so this costs one pass —
// with the swell on top (addSwell).
// The buffer is made once per size, so a frame allocates nothing.
func (p *liquidPoolState) surface() []float32 {
	s := p.sim
	n := (s.cols + 1) * (s.rows + 1)
	if len(p.blend) != n {
		p.blend = make([]float32, n)
	}
	a := p.alpha
	for k := range p.blend {
		p.blend[k] = s.prev[k] + (s.cur[k]-s.prev[k])*a
	}
	p.addSwell(p.blend, p.updatedAt)
	return p.blend
}

// liquidSoftKnee is limit·(1 − e^(−x/limit)): x itself while small, easing
// into limit instead of clipping there. A clipped light or shadow drew a hard
// edge where it met its limit.
func liquidSoftKnee(x, limit float64) float64 {
	if x <= 0 || limit <= 0 {
		return 0
	}
	return limit * (1 - math.Exp(-x/limit))
}

// liquidMeshStep is how many grid cells one mesh cell spans each way: 2,
// or 4 once the budget has stepped the water down, when the grid divides by
// it (else 1). The refraction is a gentle bend that a vertex every 20 px
// carries well, and SDL expanding the mesh is most of what the water costs
// the handheld; the light and shadow, which make the ripples crisp, stay a
// texel per node.
func liquidMeshStep(cols, rows int, degraded bool) int {
	for _, m := range []int{4, 2} {
		if m == 4 && !degraded {
			continue
		}
		if cols%m == 0 && rows%m == 0 && cols/m >= 2 && rows/m >= 2 {
			return m
		}
	}
	return 1
}

// buildMesh lays the mesh's vertices for the current water — on every
// liquidMeshStep-th node, texture coordinates pushed along the slope — and
// the light and shadow texels, one per grid node. It reports whether it
// rebuilt anything: nothing is rebuilt when neither the water nor anything the
// mesh depends on changed (the band repaint, a calm frame).
func (p *liquidPoolState) buildMesh(w, h int32, u0, v0, u1, v1 float32) bool {
	s := p.sim
	if s == nil {
		return false
	}
	cols, rows := s.cols, s.rows
	m := liquidMeshStep(cols, rows, p.degraded)
	mcols, mrows := cols/m, rows/m
	light := glassLightTheme()
	tint := glassMix(sdl.Color{R: 255, G: 255, B: 255, A: 255}, glassAccent(), 0.3)
	key := liquidMeshKey{
		w: w, h: h, cols: cols, rows: rows, step: m,
		u0: u0, v0: v0, u1: u1, v1: v1,
		lx: float32(p.lightTX), ly: float32(p.lightTY),
		tint: tint, light: light,
	}
	if p.meshOK && !p.dirty && key == p.meshKey {
		return false
	}
	start := time.Now()
	nodes := (cols + 1) * (rows + 1)
	if len(p.glowPix) != nodes*4 {
		p.glowPix = make([]byte, nodes*4)
		p.darkPix = make([]byte, nodes*4)
	}
	p.gridCols, p.gridRows = cols, rows
	if mnodes := (mcols + 1) * (mrows + 1); len(p.verts) != mnodes {
		p.verts = make([]sdl.Vertex, mnodes)
	}
	if p.idxCols != mcols || p.idxRows != mrows || len(p.idx) != mcols*mrows*6 {
		p.idx = liquidGridIndices(mcols, mrows)
		p.idxCols, p.idxRows = mcols, mrows
	}
	mstride := mcols + 1
	height := p.surface()

	lx, ly, lz, hx, hy, hz := liquidLight(p.lightTX, p.lightTY)
	// A tenth-power glint: on the 10 px grid, its light a texture the GPU
	// filters, it is small without flickering (the sixteenth power once lit
	// a patch a node or two across of a 20 px grid, and flickered).
	h2 := hz * hz
	h10 := h2 * h2
	h10 *= h10 * h2
	diffuse, glint, shadow, shadowMax := liquidDiffuse, liquidGlint, liquidShadow, liquidShadowMax
	crest, trough := liquidCrest, liquidTrough
	if light {
		diffuse, glint, shadow, shadowMax = liquidDiffuseLight, liquidGlintLight, liquidShadowLight, liquidShadowMaxLight
		crest, trough = liquidCrestLight, liquidTroughLight
	}
	tr, tg, tb := float64(tint.R)/255, float64(tint.G)/255, float64(tint.B)/255
	fw, fh := float32(w), float32(h)
	du, dv := u1-u0, v1-v0
	umin, umax := u0+liquidUVInset, u1-liquidUVInset
	vmin, vmax := v0+liquidUVInset, v1-liquidUVInset
	stride := cols + 1
	// Slopes are measured per node and scaled to liquidSlopeRefCell, the
	// cell the gains are tuned at, so a ripple of a given size in pixels
	// looks the same on every grid.
	scale := float32(1)
	if cols > 0 && w > 0 {
		scale = float32(liquidSlopeRefCell * float64(cols) / float64(w))
	}
	edge, mid := scale, scale/2
	curve := float64(scale * scale) // curvature per node², scaled like the slopes
	seen := false
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
			if i%m == 0 && j%m == 0 {
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
				p.verts[(j/m)*mstride+i/m] = sdl.Vertex{
					Position: sdl.FPoint{X: fx * fw, Y: fy * fh},
					Color:    sdl.Color{R: 255, G: 255, B: 255, A: 255},
					TexCoord: sdl.FPoint{X: u, Y: v},
				}
			}

			// The light: slopes facing it are lit (and glint), slopes facing
			// away are in shadow.
			nx, ny := -float64(sx)*liquidSlopeGain, -float64(sy)*liquidSlopeGain
			inv := 1 / math.Sqrt(nx*nx+ny*ny+1)
			rel := (nx*lx+ny*ly+lz)*inv - lz
			glow, dark := 0.0, 0.0
			if rel > 0 {
				glow = rel * diffuse
			} else {
				dark = -rel * shadow
			}
			if ndh := (nx*hx + ny*hy + hz) * inv; ndh > 0 && h10 < 1 {
				n2 := ndh * ndh
				n10 := n2 * n2
				n10 *= n10 * n2
				if n10 > h10 {
					glow += (n10 - h10) / (1 - h10) * glint
				}
			}
			// Curvature: a crest catches a line of light, a trough a line of
			// shadow — the rings. The border nodes, held flat, have none.
			if i > 0 && i < cols && j > 0 && j < rows {
				lap := float64(height[k-1]+height[k+1]+height[k-stride]+height[k+stride]-4*height[k]) * curve
				if lap < 0 {
					glow += -lap * crest
				} else {
					dark += liquidSoftKnee(lap*trough, shadowMax)
				}
			}
			glow = liquidSoftKnee(glow, 255)
			dark = liquidSoftKnee(dark, shadowMax)
			o := k * 4
			p.glowPix[o] = uint8(glow*tr + 0.5)
			p.glowPix[o+1] = uint8(glow*tg + 0.5)
			p.glowPix[o+2] = uint8(glow*tb + 0.5)
			p.glowPix[o+3] = 255
			p.darkPix[o], p.darkPix[o+1], p.darkPix[o+2] = 0, 0, 0
			p.darkPix[o+3] = uint8(255*dark + 0.5)
			if glow >= 1 || dark >= 1.0/255 {
				seen = true
			}
		}
	}
	p.lit = seen
	p.meshKey, p.meshOK, p.dirty = key, true, false
	p.frameCost += time.Since(start)
	return true
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

// liquidTextureBytes is what the water holds as textures — the baked
// backdrop and the light and shadow, when there are any — for the memory line
// (memstat.go).
func liquidTextureBytes() int64 {
	p := &liquidPool
	var n int64
	if p.baked != nil {
		n += int64(p.bakedKey.bw) * int64(p.bakedKey.bh) * 4
	}
	if p.glowTex != nil && p.darkTex != nil {
		n += 2 * int64(p.litW) * int64(p.litH) * 4
	}
	return n
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
	{Value: "slight", Label: "Slight", Note: "Soft ripples and the odd drop of rain"},
	{Value: "medium", Label: "Medium", Note: "Ripples and a drop of rain now and then"},
	{Value: "lively", Label: "Lively", Note: "Deeper, longer waves and more rain"},
}

// liquidSettingDetail is the level row's second line.
func liquidSettingDetail(c *Config) string {
	switch {
	case liquidUnsupportedSeen:
		return "Not supported by this renderer"
	case liquidLevelOf(c) != liquidLevelOff && effectiveReducedMotion(c):
		return "Reduce Motion is on: less rain, softer ripples"
	}
	return liquidCycleWords(hintsUseKeyboard()) + "; ripples follow your moves; " +
		liquidStirWords(hintOnHandheld(), stickHardware(), hintsUseKeyboard())
}

// liquidCycleWords says how to step the level without Settings: R2 on a pad
// (any screen whose R2 is free), W on Home with a keyboard.
func liquidCycleWords(keyboard bool) string {
	if keyboard {
		return "W on Home changes it"
	}
	return "R2 changes it on any screen"
}

// liquidR2Taken reports whether a screen's footer row gives R2 a job of its
// own ("R2 More", "L2/R2 Zoom", "L2/R2 Stores"…): R2 is then that screen's,
// and only where it is not does R2 step the water (shoulders.go). The footer
// is the screen's contract with the user, so the "R2 Water" hint and the
// press always agree.
func liquidR2Taken(hints []FooterHint) bool {
	for _, h := range hints {
		if hintContainsFold(h.Button, "R2") {
			return true
		}
	}
	return false
}

// liquidFooterHint adds "R2 Water" to a screen's shared footer row
// (renderFooter) when R2 would step the water there: nothing drawn over the
// screen and nothing in it owns R2. It goes right after the three navigation
// hints, which a narrow canvas keeps, so the key stays on screen. A keyboard
// has no R2 (W works on Home only), so a keyboard row has no water hint.
func liquidFooterHint(config *Config, hints []FooterHint) []FooterHint {
	if liquidUnsupportedSeen || hintsUseKeyboard() || liquidR2Taken(hints) ||
		snapshotVideoState().phase != "idle" || fileEditor.open || steamPanel.open ||
		emuPanel.open || modalPanelBlocksShoulders() || triggerOwnedByOverlay() ||
		// handleChromeShoulder's overlays page with R2 themselves.
		tb.startOpen || nc.open || clipSearchOpen || globalSearchOpen || wpOpen ||
		imageViewerPath != "" || shortcutsOpen ||
		pdfvSceneActive(config) || synthSceneActive(config) ||
		pkgHubStep(pkgHubLocation(config), padR2) >= 0 {
		return hints
	}
	at := min(3, len(hints))
	out := make([]FooterHint, 0, len(hints)+1)
	out = append(out, hints[:at]...)
	out = append(out, FooterHint{Button: "R2", Label: "Water"})
	return append(out, hints[at:]...)
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
			liquidSetLevel(c, level)
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
