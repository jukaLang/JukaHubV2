package main

// jland_world.go — JukaLand's rules, with no SDL in sight.
//
// JukaLand is the small block-building game that ships with JukaHub: a side-on
// world of grass, stone, caves and ore that the player walks around, digs into
// and builds on. Everything that decides what happens lives here — the world
// and its generator, the player's movement and collision, which block a press
// reaches and whether it may be placed or broken, and the save format — so it
// can be tested without a window. The drawing (jland_render.go), the input and
// the session (jland_input.go) and the scene (jukaland.go) only ask this file.
//
// Units are tiles: the player's X is the centre of their feet and Y is the
// bottom of their feet, both in tile coordinates with y growing downwards.

import (
	"math"
	"math/rand"
)

// jlandTile is one cell of the world grid.
type jlandTile uint8

const (
	jlandAir jlandTile = iota
	jlandGrass
	jlandDirt
	jlandStone
	jlandCobble
	jlandSand
	jlandWater
	jlandTrunk
	jlandLeaves
	jlandLog
	jlandPlanks
	jlandBrick
	jlandGlass
	jlandTorch
	jlandCoal
	jlandIron
	jlandGold
	jlandDiamond
	jlandSnow
	jlandBedrock
	jlandTileCount
)

// jlandTileDef is what a tile is: whether it stops the player, whether a press
// may remove it, whether a placed block may take its cell, and how far it
// lights the dark around it.
type jlandTileDef struct {
	Name      string
	Solid     bool
	Breakable bool
	Replace   bool
	Light     float64
	Ore       int // 1..4 indexes jlandStats.Ores; 0 = not an ore
}

// jlandTiles is the tile table. Tree trunks and leaves are scenery the player
// walks through (a forest is not a maze), while a log or a plank the player
// placed is a wall.
var jlandTiles = [jlandTileCount]jlandTileDef{
	jlandAir:     {Name: "Air", Replace: true},
	jlandGrass:   {Name: "Grass", Solid: true, Breakable: true},
	jlandDirt:    {Name: "Dirt", Solid: true, Breakable: true},
	jlandStone:   {Name: "Stone", Solid: true, Breakable: true},
	jlandCobble:  {Name: "Cobblestone", Solid: true, Breakable: true},
	jlandSand:    {Name: "Sand", Solid: true, Breakable: true},
	jlandWater:   {Name: "Water", Replace: true},
	jlandTrunk:   {Name: "Tree", Breakable: true},
	jlandLeaves:  {Name: "Leaves", Breakable: true},
	jlandLog:     {Name: "Log", Solid: true, Breakable: true},
	jlandPlanks:  {Name: "Planks", Solid: true, Breakable: true},
	jlandBrick:   {Name: "Brick", Solid: true, Breakable: true},
	jlandGlass:   {Name: "Glass", Solid: true, Breakable: true},
	jlandTorch:   {Name: "Torch", Breakable: true, Light: 7},
	jlandCoal:    {Name: "Coal", Solid: true, Breakable: true, Ore: 1},
	jlandIron:    {Name: "Iron", Solid: true, Breakable: true, Ore: 2},
	jlandGold:    {Name: "Gold", Solid: true, Breakable: true, Ore: 3},
	jlandDiamond: {Name: "Diamond", Solid: true, Breakable: true, Ore: 4},
	jlandSnow:    {Name: "Snow", Solid: true, Breakable: true},
	jlandBedrock: {Name: "Bedrock", Solid: true},
}

// jlandDef returns a tile's definition; a value no tile has (a damaged save)
// reads as air rather than indexing past the table.
func jlandDef(t jlandTile) jlandTileDef {
	if int(t) >= len(jlandTiles) {
		return jlandTiles[jlandAir]
	}
	return jlandTiles[t]
}

// jlandHotbar is what the player builds with, in the order the slots are drawn
// and the number keys pick them. Building is creative: every block is always
// available, so the game is about what you make, not about grinding for it.
var jlandHotbar = [...]jlandTile{
	jlandGrass, jlandDirt, jlandStone, jlandCobble, jlandSand,
	jlandPlanks, jlandLog, jlandBrick, jlandGlass, jlandTorch,
}

// jlandOreNames names the four ore counters in the order jlandStats.Ores keeps
// them.
var jlandOreNames = [4]string{"Coal", "Iron", "Gold", "Diamond"}

const (
	// jlandWorldW and jlandWorldH are a new world's size in tiles: several
	// screens wide and deep enough for caves and every ore band.
	jlandWorldW = 160
	jlandWorldH = 64
	// jlandSaveVersion marks the save format. Saves from the first JukaLand
	// (a 50x28 grid of a different tile set) carry no version and are replaced
	// by a new world.
	jlandSaveVersion = 2

	jlandPlayerW   = 0.7
	jlandPlayerH   = 1.7
	jlandGravity   = 38.0
	jlandMaxFall   = 22.0
	jlandWalkSpeed = 6.5
	// jlandJumpSpeed clears a little over two tiles, so a two-high ledge can be
	// climbed without building a step.
	jlandJumpSpeed = 13.2
	jlandSwimSpeed = 5.5
	// jlandReach is how far (tile centre to the player's middle) a block can be
	// broken or placed.
	jlandReach = 4.6
	// jlandStep is the fixed physics step; a frame runs as many as fit.
	jlandStep = 1.0 / 60.0
	jlandEps  = 1e-6
	// jlandDayLength is one day and night, in seconds of play.
	jlandDayLength = 600.0
)

// ── World ────────────────────────────────────────────────────────────────

// jlandWorld is the grid. Surface remembers where the ground was when the
// world was made, so a hole dug below it shows a cave wall instead of sky;
// skyTop is the first solid row of each column, kept current for lighting.
type jlandWorld struct {
	W, H    int
	Tiles   []jlandTile
	Surface []int
	skyTop  []int
}

func (w *jlandWorld) in(x, y int) bool {
	return w != nil && x >= 0 && y >= 0 && x < w.W && y < w.H
}

// at reads a cell. Past the side walls and the floor is solid rock, so the
// player can never leave the world; above the top is open sky.
func (w *jlandWorld) at(x, y int) jlandTile {
	if w == nil || x < 0 || x >= w.W || y >= w.H {
		return jlandBedrock
	}
	if y < 0 {
		return jlandAir
	}
	return w.Tiles[y*w.W+x]
}

func (w *jlandWorld) solid(x, y int) bool {
	return jlandDef(w.at(x, y)).Solid
}

// set writes a cell and keeps the column's light boundary current.
func (w *jlandWorld) set(x, y int, t jlandTile) bool {
	if !w.in(x, y) {
		return false
	}
	w.Tiles[y*w.W+x] = t
	w.refreshColumn(x)
	return true
}

// firstSolid is the first solid row of a column from the top (H when the
// column is open all the way down, which the bedrock floor never allows).
func (w *jlandWorld) firstSolid(x int) int {
	for y := 0; y < w.H; y++ {
		if jlandDef(w.Tiles[y*w.W+x]).Solid {
			return y
		}
	}
	return w.H
}

func (w *jlandWorld) refreshColumn(x int) {
	if x < 0 || x >= w.W {
		return
	}
	if len(w.skyTop) != w.W {
		w.skyTop = make([]int, w.W)
		for i := range w.skyTop {
			w.skyTop[i] = w.firstSolid(i)
		}
		return
	}
	w.skyTop[x] = w.firstSolid(x)
}

func (w *jlandWorld) rebuildSky() {
	w.skyTop = nil
	w.refreshColumn(0)
}

// skyRow is the first solid row of column x; 0 outside the world.
func (w *jlandWorld) skyRow(x int) int {
	if x < 0 || x >= w.W || len(w.skyTop) != w.W {
		return 0
	}
	return w.skyTop[x]
}

// surfaceRow is the ground row column x was generated with.
func (w *jlandWorld) surfaceRow(x int) int {
	if x < 0 || x >= len(w.Surface) {
		return 0
	}
	return w.Surface[x]
}

// boxRange is the span of cells an axis-aligned box covers. A box whose edge
// sits exactly on a cell boundary does not reach into the next cell.
func jlandBoxRange(left, top, right, bottom float64) (x0, y0, x1, y1 int) {
	x0 = int(math.Floor(left))
	x1 = int(math.Floor(right - jlandEps))
	y0 = int(math.Floor(top))
	y1 = int(math.Floor(bottom - jlandEps))
	return x0, y0, x1, y1
}

// boxHits reports whether a player standing with feet at (x, y) overlaps a
// solid cell.
func (w *jlandWorld) boxHits(x, y float64) bool {
	x0, y0, x1, y1 := jlandBoxRange(x-jlandPlayerW/2, y-jlandPlayerH, x+jlandPlayerW/2, y)
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if w.solid(tx, ty) {
				return true
			}
		}
	}
	return false
}

// boxTouches reports whether that box overlaps a cell of type t.
func (w *jlandWorld) boxTouches(x, y float64, t jlandTile) bool {
	x0, y0, x1, y1 := jlandBoxRange(x-jlandPlayerW/2, y-jlandPlayerH, x+jlandPlayerW/2, y)
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if w.at(tx, ty) == t {
				return true
			}
		}
	}
	return false
}

// spawnPoint is where a player starts: on dry ground as close to the middle of
// the world as there is room to stand.
func (w *jlandWorld) spawnPoint() (float64, float64) {
	center := w.W / 2
	for d := 0; d < w.W/2; d++ {
		for _, x := range [2]int{center - d, center + d} {
			if x < 1 || x >= w.W-1 {
				continue
			}
			y := w.firstSolid(x)
			if y <= 2 || y >= w.H-1 || w.at(x, y-1) == jlandWater {
				continue
			}
			px := float64(x) + 0.5
			if !w.boxHits(px, float64(y)) {
				return px, float64(y)
			}
		}
	}
	return float64(center) + 0.5, 2
}

// ── Generation ───────────────────────────────────────────────────────────

// jlandNoise is smooth 1D value noise: random heights on a lattice every
// period columns, blended with a cosine so hills have no corners.
type jlandNoise struct {
	period float64
	values []float64
}

func newJlandNoise(rng *rand.Rand, width int, period float64) jlandNoise {
	n := int(float64(width)/period) + 3
	values := make([]float64, n)
	for i := range values {
		values[i] = rng.Float64()*2 - 1
	}
	return jlandNoise{period: period, values: values}
}

func (n jlandNoise) at(x float64) float64 {
	if n.period <= 0 || len(n.values) < 2 {
		return 0
	}
	f := x / n.period
	i := int(math.Floor(f))
	if i < 0 {
		i = 0
	}
	if i > len(n.values)-2 {
		i = len(n.values) - 2
	}
	t := math.Max(0, math.Min(1, f-float64(i)))
	s := (1 - math.Cos(t*math.Pi)) / 2
	return n.values[i]*(1-s) + n.values[i+1]*s
}

// jlandGenerate builds a world from a seed: rolling hills with grass, sandy
// stretches and snowy peaks, dirt over stone, worm caves, ore veins that get
// rarer and richer with depth, lakes in the valleys, trees, and a bedrock
// floor. The same seed always makes the same world.
func jlandGenerate(seed int64, width, height int) *jlandWorld {
	if width < 32 {
		width = 32
	}
	if height < 24 {
		height = 24
	}
	rng := rand.New(rand.NewSource(seed))
	w := &jlandWorld{W: width, H: height, Tiles: make([]jlandTile, width*height), Surface: make([]int, width)}

	hills := newJlandNoise(rng, width, 40)
	bumps := newJlandNoise(rng, width, 13)
	detail := newJlandNoise(rng, width, 5)
	biome := newJlandNoise(rng, width, 56)
	base := float64(height) * 0.40
	minSurface, maxSurface := height/5, height*3/5
	for x := 0; x < width; x++ {
		fx := float64(x)
		s := int(math.Round(base + hills.at(fx)*7 + bumps.at(fx)*3 + detail.at(fx)*1.2))
		if s < minSurface {
			s = minSurface
		}
		if s > maxSurface {
			s = maxSurface
		}
		w.Surface[x] = s
	}

	seaLevel := int(base) + 4
	snowLine := int(base) - 5
	for x := 0; x < width; x++ {
		s := w.Surface[x]
		desert := biome.at(float64(x)) > 0.45
		dirtDepth := 3 + rng.Intn(2)
		for y := s; y < height; y++ {
			depth := y - s
			var t jlandTile
			switch {
			case y >= height-1:
				t = jlandBedrock
			case y >= height-3 && rng.Intn(3) == 0:
				t = jlandBedrock
			case depth == 0 && desert:
				t = jlandSand
			case depth == 0 && s <= snowLine:
				t = jlandSnow
			case depth == 0:
				t = jlandGrass
			case depth < dirtDepth && desert:
				t = jlandSand
			case depth < dirtDepth:
				t = jlandDirt
			default:
				t = jlandStone
			}
			w.Tiles[y*width+x] = t
		}
	}

	// Caves: worms that wander through the rock and dirt, never closer than a
	// few rows to the grass so the surface stays walkable.
	worms := width / 10
	for i := 0; i < worms; i++ {
		x := float64(rng.Intn(width))
		room := height - w.Surface[int(x)] - 12
		if room < 1 {
			room = 1
		}
		y := float64(w.Surface[int(x)] + 6 + rng.Intn(room))
		angle := rng.Float64() * 2 * math.Pi
		length := 25 + rng.Intn(45)
		for step := 0; step < length; step++ {
			w.carve(x, y, 1.0+rng.Float64()*1.2)
			angle += (rng.Float64() - 0.5) * 0.9
			x += math.Cos(angle)
			y += math.Sin(angle) * 0.6
			if x < 1 || x > float64(width-2) || y > float64(height-4) {
				break
			}
		}
	}

	// Ore: coal anywhere in the rock, iron a little deeper, gold deeper still
	// and diamonds only near the floor.
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if w.Tiles[y*width+x] != jlandStone {
				continue
			}
			depth := y - w.Surface[x]
			ore := jlandAir
			switch roll := rng.Float64(); {
			case roll < 0.003:
				if depth > 26 {
					ore = jlandDiamond
				}
			case roll < 0.009:
				if depth > 16 {
					ore = jlandGold
				}
			case roll < 0.021:
				if depth > 8 {
					ore = jlandIron
				}
			case roll < 0.035:
				ore = jlandCoal
			}
			if ore != jlandAir {
				w.vein(rng, x, y, ore)
			}
		}
	}

	// Lakes: every valley that dips below the sea line fills with water and
	// gets a sandy bed.
	for x := 0; x < width; x++ {
		s := w.Surface[x]
		if s <= seaLevel {
			continue
		}
		for y := seaLevel; y < s; y++ {
			w.Tiles[y*width+x] = jlandWater
		}
		for y := s; y < s+3 && y < height-1; y++ {
			switch w.Tiles[y*width+x] {
			case jlandGrass, jlandDirt, jlandSnow:
				w.Tiles[y*width+x] = jlandSand
			}
		}
	}

	// Trees on grass and snow, spaced so their crowns do not merge into one
	// green wall.
	last := -10
	for x := 3; x < width-3; x++ {
		s := w.Surface[x]
		top := w.Tiles[s*width+x]
		if top != jlandGrass && top != jlandSnow {
			continue
		}
		if x-last < 5 || rng.Float64() > 0.32 {
			continue
		}
		trunk := 4 + rng.Intn(3)
		if s-trunk-3 < 1 || w.Tiles[(s-1)*width+x] != jlandAir {
			continue
		}
		for i := 1; i <= trunk; i++ {
			w.Tiles[(s-i)*width+x] = jlandTrunk
		}
		crown := s - trunk
		for dy := -2; dy <= 1; dy++ {
			span := 2
			if dy == -2 {
				span = 1
			}
			for dx := -span; dx <= span; dx++ {
				if (dx == -2 || dx == 2) && dy != 0 && rng.Intn(2) == 0 {
					continue
				}
				tx, ty := x+dx, crown+dy
				if w.in(tx, ty) && w.Tiles[ty*width+tx] == jlandAir {
					w.Tiles[ty*width+tx] = jlandLeaves
				}
			}
		}
		last = x
	}

	w.rebuildSky()
	return w
}

// carve hollows a disc of rock and dirt for a cave.
func (w *jlandWorld) carve(cx, cy, r float64) {
	x0, x1 := int(math.Floor(cx-r)), int(math.Ceil(cx+r))
	y0, y1 := int(math.Floor(cy-r)), int(math.Ceil(cy+r))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !w.in(x, y) || y <= w.Surface[x]+3 || y >= w.H-2 {
				continue
			}
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy > r*r {
				continue
			}
			switch w.Tiles[y*w.W+x] {
			case jlandDirt, jlandStone, jlandSand:
				w.Tiles[y*w.W+x] = jlandAir
			}
		}
	}
}

// vein grows an ore cell into a small cluster through the rock around it.
func (w *jlandWorld) vein(rng *rand.Rand, x, y int, ore jlandTile) {
	w.Tiles[y*w.W+x] = ore
	n := rng.Intn(4)
	for i := 0; i < n; i++ {
		x += rng.Intn(3) - 1
		y += rng.Intn(3) - 1
		if w.in(x, y) && w.Tiles[y*w.W+x] == jlandStone {
			w.Tiles[y*w.W+x] = ore
		}
	}
}

// ── Player and game ──────────────────────────────────────────────────────

// jlandPlayer is the one character. Lift is a cosmetic offset that eases the
// sprite up a stair the physics climbed in one step.
type jlandPlayer struct {
	X, Y      float64
	VX, VY    float64
	OnGround  bool
	InWater   bool
	Facing    int
	Lift      float64
	WalkPhase float64
	coyote    float64
	jumpBuf   float64
}

// jlandControls is one frame of held input, already merged from the pad, the
// keyboard and the pointer. Jump is a press (an edge); JumpHeld keeps a
// swimmer rising.
type jlandControls struct {
	MoveX    float64
	Jump     bool
	JumpHeld bool
}

// jlandStats are the player's tallies. Ores counts the four ores found, in
// jlandOreNames order.
type jlandStats struct {
	Mined  int
	Placed int
	Ores   [4]int
}

// Aim modes: the arc around the player (pad Up/Down), an offset from the
// player moved by the right stick, or the cell under the pointer.
const (
	jlandAimArc = iota
	jlandAimRelative
	jlandAimPointer
)

// jlandArc is the aim dial for a player facing right: above the head, the
// cell in front of the head row's top, in front of the head, in front of the
// feet (the default), in front and below, and straight down. Facing left
// mirrors the column.
var jlandArc = [...][2]int{{0, -2}, {1, -2}, {1, -1}, {1, 0}, {1, 1}, {0, 1}}

const jlandArcDefault = 3

// jlandGame is one world being played.
type jlandGame struct {
	World   *jlandWorld
	P       jlandPlayer
	Seed    int64
	Slot    int
	Aim     int
	AimMode int
	RelX    int
	RelY    int
	PtrX    int
	PtrY    int
	Clock   float64
	Stats   jlandStats
	dirty   bool
	acc     float64
}

// newJlandGame makes a fresh world from seed and stands the player on it.
func newJlandGame(seed int64) *jlandGame {
	g := &jlandGame{
		World: jlandGenerate(seed, jlandWorldW, jlandWorldH),
		Seed:  seed,
		Aim:   jlandArcDefault,
		Clock: jlandDayLength * 0.08,
	}
	g.P.X, g.P.Y = g.World.spawnPoint()
	g.P.Facing = 1
	g.P.OnGround = true
	g.dirty = true
	return g
}

// selected is the block the hotbar slot builds with.
func (g *jlandGame) selected() jlandTile {
	if g.Slot < 0 || g.Slot >= len(jlandHotbar) {
		g.Slot = 0
	}
	return jlandHotbar[g.Slot]
}

// cycleSlot steps the hotbar, wrapping at both ends.
func (g *jlandGame) cycleSlot(delta int) {
	n := len(jlandHotbar)
	g.Slot = ((g.Slot+delta)%n + n) % n
}

// stepAim moves the arc cursor and hands aiming back to the arc.
func (g *jlandGame) stepAim(delta int) {
	if g.AimMode != jlandAimArc {
		g.AimMode = jlandAimArc
		g.Aim = jlandArcDefault
	}
	g.Aim += delta
	if g.Aim < 0 {
		g.Aim = 0
	}
	if g.Aim >= len(jlandArc) {
		g.Aim = len(jlandArc) - 1
	}
}

// anchor is the cell under the middle of the player's feet row.
func (g *jlandGame) anchor() (int, int) {
	return int(math.Floor(g.P.X)), int(math.Floor(g.P.Y - jlandEps))
}

// arcTarget is the cell the aim dial points at. "In front" is the first
// column the player's body does not overlap, so a player straddling two
// columns never aims into their own body. "Straight down" in the air reaches
// down to the cell just above the ground: jumping and building there puts the
// block where the player was standing, which is how a player climbs.
func (g *jlandGame) arcTarget() (int, int) {
	cx, feet := g.anchor()
	aim := g.Aim
	if aim < 0 || aim >= len(jlandArc) {
		aim = jlandArcDefault
	}
	off := jlandArc[aim]
	tx := cx
	if off[0] != 0 {
		if g.P.Facing >= 0 {
			tx = int(math.Floor(g.P.X+jlandPlayerW/2-jlandEps)) + 1
		} else {
			tx = int(math.Floor(g.P.X-jlandPlayerW/2)) - 1
		}
	}
	ty := feet + off[1]
	if off[0] == 0 && off[1] > 0 {
		w := g.World
		for k := 0; k < 3 && jlandDef(w.at(tx, ty)).Replace && jlandDef(w.at(tx, ty+1)).Replace; k++ {
			ty++
		}
	}
	return tx, ty
}

// target is the cell a mine or build press acts on.
func (g *jlandGame) target() (int, int) {
	switch g.AimMode {
	case jlandAimRelative:
		cx, feet := g.anchor()
		return cx + g.RelX, feet + g.RelY
	case jlandAimPointer:
		return g.PtrX, g.PtrY
	}
	return g.arcTarget()
}

// nudgeRelative moves the right-stick cursor one cell, keeping it in reach.
func (g *jlandGame) nudgeRelative(dx, dy int) {
	if g.AimMode != jlandAimRelative {
		tx, ty := g.target()
		cx, feet := g.anchor()
		g.RelX, g.RelY = tx-cx, ty-feet
		g.AimMode = jlandAimRelative
	}
	nx, ny := g.RelX+dx, g.RelY+dy
	cx, feet := g.anchor()
	if g.inReach(cx+nx, feet+ny) {
		g.RelX, g.RelY = nx, ny
	}
}

// pointAt aims at a cell under the pointer.
func (g *jlandGame) pointAt(tx, ty int) {
	g.AimMode = jlandAimPointer
	g.PtrX, g.PtrY = tx, ty
}

// inReach reports whether a cell's centre is within reach of the player's
// middle.
func (g *jlandGame) inReach(tx, ty int) bool {
	dx := float64(tx) + 0.5 - g.P.X
	dy := float64(ty) + 0.5 - (g.P.Y - jlandPlayerH/2)
	return dx*dx+dy*dy <= jlandReach*jlandReach
}

// overlapsPlayer reports whether a cell intersects the player's box.
func (g *jlandGame) overlapsPlayer(tx, ty int) bool {
	left, right := g.P.X-jlandPlayerW/2, g.P.X+jlandPlayerW/2
	top, bottom := g.P.Y-jlandPlayerH, g.P.Y
	return float64(tx) < right && float64(tx+1) > left && float64(ty) < bottom && float64(ty+1) > top
}

// jlandResult is why a press did or did not change the world.
type jlandResult int

const (
	jlandOK jlandResult = iota
	jlandOutside
	jlandTooFar
	jlandOccupied
	jlandNothing
	jlandInTheWay
	jlandTooHard
)

// canPlace decides whether t may go into the cell: inside the world, within
// reach, into air or water, and (for a solid block) not into the player.
func (g *jlandGame) canPlace(tx, ty int, t jlandTile) jlandResult {
	w := g.World
	switch {
	case !w.in(tx, ty):
		return jlandOutside
	case !g.inReach(tx, ty):
		return jlandTooFar
	case !jlandDef(w.at(tx, ty)).Replace:
		return jlandOccupied
	case jlandDef(t).Solid && g.overlapsPlayer(tx, ty):
		return jlandInTheWay
	}
	return jlandOK
}

// canBreak decides whether the cell may be removed.
func (g *jlandGame) canBreak(tx, ty int) jlandResult {
	w := g.World
	switch {
	case !w.in(tx, ty):
		return jlandOutside
	case !g.inReach(tx, ty):
		return jlandTooFar
	}
	def := jlandDef(w.at(tx, ty))
	switch {
	case def.Replace:
		return jlandNothing
	case !def.Breakable:
		return jlandTooHard
	}
	return jlandOK
}

// placeAt builds the selected block into a cell.
func (g *jlandGame) placeAt(tx, ty int) jlandResult {
	t := g.selected()
	if r := g.canPlace(tx, ty, t); r != jlandOK {
		return r
	}
	g.World.set(tx, ty, t)
	g.Stats.Placed++
	trophyEvent(tkJlandPlace)
	g.dirty = true
	return jlandOK
}

// breakAt removes a cell and returns what it was. Ores are counted.
func (g *jlandGame) breakAt(tx, ty int) (jlandTile, jlandResult) {
	if r := g.canBreak(tx, ty); r != jlandOK {
		return jlandAir, r
	}
	was := g.World.at(tx, ty)
	g.World.set(tx, ty, jlandAir)
	g.Stats.Mined++
	trophyEvent(tkJlandBreak)
	if ore := jlandDef(was).Ore; ore >= 1 && ore <= len(g.Stats.Ores) {
		g.Stats.Ores[ore-1]++
		trophyEvent(tkJlandOre)
		trophyEventDistinct(tkJlandOreKinds, jlandOreNames[ore-1])
	}
	g.dirty = true
	return was, jlandOK
}

// ── Physics ──────────────────────────────────────────────────────────────

// jlandApproach moves v toward target by at most step.
func jlandApproach(v, target, step float64) float64 {
	if v < target {
		return math.Min(v+step, target)
	}
	return math.Max(v-step, target)
}

// advance runs the world for dt seconds in fixed steps. A press of Jump is
// consumed by the first step, so a slow frame cannot jump twice.
func (g *jlandGame) advance(dt float64, c jlandControls) {
	if g == nil || g.World == nil || dt <= 0 {
		return
	}
	g.Clock += dt
	g.acc += dt
	if g.acc > 0.25 {
		g.acc = 0.25
	}
	for steps := 0; g.acc >= jlandStep && steps < 16; steps++ {
		g.stepPlayer(c, jlandStep)
		c.Jump = false
		g.acc -= jlandStep
	}
	if g.P.Lift > 0 {
		g.P.Lift = math.Max(0, g.P.Lift-dt*7)
	}
}

// stepPlayer is one physics step: walk, jump or swim, then move along each
// axis and stop at whatever is solid. A one-tile step is climbed without a
// jump, which is what makes rough ground walkable on a D-pad.
func (g *jlandGame) stepPlayer(c jlandControls, dt float64) {
	p := &g.P
	w := g.World
	p.InWater = w.boxTouches(p.X, p.Y, jlandWater)

	speed := jlandWalkSpeed
	if p.InWater {
		speed *= 0.55
	}
	accel := 70.0
	if !p.OnGround {
		accel = 40
	}
	move := math.Max(-1, math.Min(1, c.MoveX))
	p.VX = jlandApproach(p.VX, move*speed, accel*dt)
	if move > 0.2 {
		p.Facing = 1
	} else if move < -0.2 {
		p.Facing = -1
	}

	if c.Jump {
		p.jumpBuf = 0.12
	} else if p.jumpBuf > 0 {
		p.jumpBuf = math.Max(0, p.jumpBuf-dt)
	}
	if p.OnGround {
		p.coyote = 0.1
	} else if p.coyote > 0 {
		p.coyote = math.Max(0, p.coyote-dt)
	}

	if p.InWater {
		if c.JumpHeld || p.jumpBuf > 0 {
			p.VY = -jlandSwimSpeed
			p.jumpBuf = 0
		}
		p.VY += jlandGravity * 0.3 * dt
		if p.VY > 3.5 {
			p.VY = 3.5
		}
	} else {
		if p.jumpBuf > 0 && p.coyote > 0 {
			p.VY = -jlandJumpSpeed
			p.jumpBuf = 0
			p.coyote = 0
			p.OnGround = false
		}
		p.VY += jlandGravity * dt
		if p.VY > jlandMaxFall {
			p.VY = jlandMaxFall
		}
	}

	if dx := p.VX * dt; dx != 0 {
		nx := p.X + dx
		switch {
		case !w.boxHits(nx, p.Y):
			p.X = nx
			p.WalkPhase += math.Abs(dx)
		case p.OnGround && g.stepUp(nx):
			p.WalkPhase += math.Abs(dx)
		case dx > 0:
			col := int(math.Floor(nx + jlandPlayerW/2 - jlandEps))
			p.X = math.Max(p.X, float64(col)-jlandPlayerW/2-1e-4)
			p.VX = 0
		default:
			col := int(math.Floor(nx - jlandPlayerW/2))
			p.X = math.Min(p.X, float64(col+1)+jlandPlayerW/2+1e-4)
			p.VX = 0
		}
	}

	dy := p.VY * dt
	ny := p.Y + dy
	switch {
	case !w.boxHits(p.X, ny):
		p.Y = ny
		p.OnGround = false
	case dy > 0:
		p.Y = math.Floor(ny - jlandEps)
		p.VY = 0
		p.OnGround = true
	default:
		row := math.Floor(ny - jlandPlayerH)
		p.Y = row + 1 + jlandPlayerH + 1e-4
		p.VY = 0
	}

	// A player can only leave the world through a bug; put them back on the
	// ground rather than let them fall forever.
	if p.Y > float64(w.H)+2 || p.Y < -float64(w.H) || math.IsNaN(p.X) || math.IsNaN(p.Y) {
		g.respawn()
	}
}

// stepUp climbs onto a one-tile step in front of a walking player, when there
// is head room for it.
func (g *jlandGame) stepUp(nx float64) bool {
	p := &g.P
	w := g.World
	ny := math.Floor(p.Y - jlandEps)
	if p.Y-ny > 1+jlandEps || w.boxHits(nx, ny) || w.boxHits(p.X, ny) {
		return false
	}
	p.X, p.Y = nx, ny
	p.Lift = 1
	return true
}

// respawn stands the player back on the world's spawn point.
func (g *jlandGame) respawn() {
	g.P = jlandPlayer{Facing: 1, OnGround: true}
	g.P.X, g.P.Y = g.World.spawnPoint()
	g.dirty = true
}

// ── Light ────────────────────────────────────────────────────────────────

// jlandSmooth is smoothstep on 0..1.
func jlandSmooth(t float64) float64 {
	t = math.Max(0, math.Min(1, t))
	return t * t * (3 - 2*t)
}

// jlandDaylight is how bright the sky is at a point of the day: full light
// for most of it, a short dusk, a moonlit night and a dawn.
func jlandDaylight(clock float64) float64 {
	t := math.Mod(clock, jlandDayLength) / jlandDayLength
	if t < 0 {
		t += 1
	}
	switch {
	case t < 0.55:
		return 1
	case t < 0.62:
		return 1 - 0.72*jlandSmooth((t-0.55)/0.07)
	case t < 0.93:
		return 0.28
	default:
		return 0.28 + 0.72*jlandSmooth((t-0.93)/0.07)
	}
}

// jlandLight is one light source near the view: a torch, or the faint glow the
// player carries so a tunnel is never pitch black around them.
type jlandLight struct {
	X, Y   float64
	Radius float64
	Power  float64
}

// lightAt is how lit a cell is, 0..1: open sky gets the daylight, which fades
// with depth below a column's first solid cell, and any light source brightens
// the cells around it.
func (g *jlandGame) lightAt(tx, ty int, daylight float64, lights []jlandLight) float64 {
	w := g.World
	sky := daylight
	if top := w.skyRow(tx); ty > top {
		sky = daylight * math.Max(0, 1-float64(ty-top)*0.2)
	}
	level := math.Max(sky, 0.05)
	cx, cy := float64(tx)+0.5, float64(ty)+0.5
	for _, l := range lights {
		dx, dy := cx-l.X, cy-l.Y
		d2 := dx*dx + dy*dy
		if d2 >= l.Radius*l.Radius {
			continue
		}
		if v := l.Power * (1 - math.Sqrt(d2)/l.Radius); v > level {
			level = v
		}
	}
	return math.Min(1, level)
}

// ── Save format ──────────────────────────────────────────────────────────

// JukaLandState is the world as jukauser.json keeps it (the "jukaland"
// section). Tiles and Surface are byte slices, which JSON writes as base64: a
// 160x64 world is about 14 KB in the file.
type JukaLandState struct {
	Version int     `json:"v"`
	Seed    int64   `json:"seed"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	Tiles   []byte  `json:"tiles"`
	Surface []byte  `json:"surface"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Facing  int     `json:"facing"`
	Slot    int     `json:"slot"`
	Clock   float64 `json:"clock"`
	Mined   int     `json:"mined"`
	Placed  int     `json:"placed"`
	Ores    [4]int  `json:"ores"`
}

// snapshot copies the game into its save form. The copy shares nothing with
// the live world, so it can be written from another goroutine.
func (g *jlandGame) snapshot() *JukaLandState {
	if g == nil || g.World == nil {
		return nil
	}
	w := g.World
	s := &JukaLandState{
		Version: jlandSaveVersion,
		Seed:    g.Seed,
		W:       w.W,
		H:       w.H,
		Tiles:   make([]byte, len(w.Tiles)),
		Surface: make([]byte, len(w.Surface)),
		X:       g.P.X,
		Y:       g.P.Y,
		Facing:  g.P.Facing,
		Slot:    g.Slot,
		Clock:   math.Mod(g.Clock, jlandDayLength*1000),
		Mined:   g.Stats.Mined,
		Placed:  g.Stats.Placed,
		Ores:    g.Stats.Ores,
	}
	for i, t := range w.Tiles {
		s.Tiles[i] = byte(t)
	}
	for i, v := range w.Surface {
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		s.Surface[i] = byte(v)
	}
	return s
}

// jlandFromState rebuilds a game from a save, or returns nil when the save is
// from another version or damaged — the caller then starts a new world rather
// than loading half of one.
func jlandFromState(s *JukaLandState) *jlandGame {
	if s == nil || s.Version != jlandSaveVersion || s.W < 32 || s.H < 24 || s.W > 2048 || s.H > 512 ||
		len(s.Tiles) != s.W*s.H || len(s.Surface) != s.W {
		return nil
	}
	w := &jlandWorld{W: s.W, H: s.H, Tiles: make([]jlandTile, len(s.Tiles)), Surface: make([]int, s.W)}
	for i, b := range s.Tiles {
		t := jlandTile(b)
		if t >= jlandTileCount {
			t = jlandAir
		}
		w.Tiles[i] = t
	}
	for i, b := range s.Surface {
		w.Surface[i] = int(b)
	}
	w.rebuildSky()
	g := &jlandGame{
		World: w,
		Seed:  s.Seed,
		Slot:  s.Slot,
		Aim:   jlandArcDefault,
		Clock: s.Clock,
		Stats: jlandStats{Mined: s.Mined, Placed: s.Placed, Ores: s.Ores},
	}
	g.selected() // clamps a slot from a longer hotbar
	g.P = jlandPlayer{X: s.X, Y: s.Y, Facing: s.Facing}
	if g.P.Facing == 0 {
		g.P.Facing = 1
	}
	bad := math.IsNaN(s.X) || math.IsNaN(s.Y) || math.IsInf(s.X, 0) || math.IsInf(s.Y, 0) ||
		s.X < 0 || s.X > float64(s.W) || s.Y < 1 || s.Y > float64(s.H)
	if bad || w.boxHits(g.P.X, g.P.Y) {
		g.respawn()
	}
	if math.IsNaN(g.Clock) || math.IsInf(g.Clock, 0) || g.Clock < 0 {
		g.Clock = 0
	}
	g.dirty = false
	return g
}
