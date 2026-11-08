package main

import (
	"encoding/json"
	"math"
	"testing"
)

// jlandFlatWorld is a test world: open sky above row ground, grass on it,
// stone below and bedrock on the last row.
func jlandFlatWorld(w, h, ground int) *jlandWorld {
	world := &jlandWorld{W: w, H: h, Tiles: make([]jlandTile, w*h), Surface: make([]int, w)}
	for x := 0; x < w; x++ {
		world.Surface[x] = ground
		for y := ground; y < h; y++ {
			t := jlandStone
			switch {
			case y == h-1:
				t = jlandBedrock
			case y == ground:
				t = jlandGrass
			}
			world.Tiles[y*w+x] = t
		}
	}
	world.rebuildSky()
	return world
}

// jlandStandingGame stands a player on the ground at column x.
func jlandStandingGame(world *jlandWorld, x float64) *jlandGame {
	g := &jlandGame{World: world, Aim: jlandArcDefault}
	g.P = jlandPlayer{X: x, Y: float64(world.Surface[int(x)]), Facing: 1, OnGround: true}
	return g
}

// jlandRun steps the physics for seconds with the same controls; a jump press
// is only delivered on the first step.
func jlandRun(g *jlandGame, seconds float64, c jlandControls) {
	for t := 0.0; t < seconds; t += jlandStep {
		g.advance(jlandStep, c)
		c.Jump = false
	}
}

func TestJlandGenerateIsDeterministicAndSound(t *testing.T) {
	a := jlandGenerate(42, jlandWorldW, jlandWorldH)
	b := jlandGenerate(42, jlandWorldW, jlandWorldH)
	c := jlandGenerate(43, jlandWorldW, jlandWorldH)
	if len(a.Tiles) != jlandWorldW*jlandWorldH {
		t.Fatalf("world has %d cells, want %d", len(a.Tiles), jlandWorldW*jlandWorldH)
	}
	same, differs := true, false
	for i := range a.Tiles {
		if a.Tiles[i] != b.Tiles[i] {
			same = false
		}
		if a.Tiles[i] != c.Tiles[i] {
			differs = true
		}
	}
	if !same {
		t.Fatal("the same seed made two different worlds")
	}
	if !differs {
		t.Fatal("two seeds made the same world")
	}
	counts := map[jlandTile]int{}
	for x := 0; x < a.W; x++ {
		if got := a.at(x, a.H-1); got != jlandBedrock {
			t.Fatalf("column %d has %v on the floor, want bedrock", x, got)
		}
		if a.firstSolid(x) >= a.H {
			t.Fatalf("column %d has no ground", x)
		}
	}
	for _, tile := range a.Tiles {
		counts[tile]++
	}
	for _, want := range []jlandTile{jlandGrass, jlandDirt, jlandStone, jlandCoal, jlandTrunk, jlandLeaves} {
		if counts[want] == 0 {
			t.Errorf("a %dx%d world has no %s", a.W, a.H, jlandDef(want).Name)
		}
	}
	x, y := a.spawnPoint()
	if a.boxHits(x, y) {
		t.Fatalf("the spawn point (%.2f, %.2f) is inside the ground", x, y)
	}
	if !a.solid(int(math.Floor(x)), int(y)) {
		t.Fatalf("the spawn point (%.2f, %.2f) is not standing on anything", x, y)
	}
}

func TestJlandPlayerFallsAndLands(t *testing.T) {
	world := jlandFlatWorld(40, 30, 20)
	g := jlandStandingGame(world, 10.5)
	g.P.Y = 16
	g.P.OnGround = false
	jlandRun(g, 1.5, jlandControls{})
	if !g.P.OnGround || g.P.Y != 20 {
		t.Fatalf("player at y=%.3f ground=%v, want standing on row 20", g.P.Y, g.P.OnGround)
	}
	if world.boxHits(g.P.X, g.P.Y) {
		t.Fatal("the player ended up inside the ground")
	}
}

func TestJlandWalkStopsAtAWall(t *testing.T) {
	world := jlandFlatWorld(40, 30, 20)
	for y := 17; y < 20; y++ {
		world.set(20, y, jlandStone)
	}
	g := jlandStandingGame(world, 15.5)
	jlandRun(g, 3, jlandControls{MoveX: 1})
	if right := g.P.X + jlandPlayerW/2; right > 20+1e-3 || right < 19.9 {
		t.Fatalf("player's right edge stopped at %.4f, want against the wall at 20", right)
	}
	if g.P.Facing != 1 {
		t.Fatalf("facing = %d after walking right", g.P.Facing)
	}
}

func TestJlandWalkClimbsAOneTileStep(t *testing.T) {
	world := jlandFlatWorld(40, 30, 20)
	world.set(20, 19, jlandDirt)
	g := jlandStandingGame(world, 16.5)
	jlandRun(g, 2, jlandControls{MoveX: 1})
	if g.P.X < 22 {
		t.Fatalf("player stopped at x=%.2f: a one-tile step should be walked over", g.P.X)
	}
	if world.boxHits(g.P.X, g.P.Y) {
		t.Fatal("the step-up left the player inside a block")
	}
}

func TestJlandJumpClearsATwoTileWall(t *testing.T) {
	world := jlandFlatWorld(40, 30, 20)
	world.set(20, 19, jlandStone)
	world.set(20, 18, jlandStone)

	walker := jlandStandingGame(world, 16.5)
	jlandRun(walker, 2, jlandControls{MoveX: 1})
	if walker.P.X > 20 {
		t.Fatal("a two-tile wall was walked over without a jump")
	}

	jumper := jlandStandingGame(world, 16.5)
	for i := 0; i < 8 && jumper.P.X < 21.5; i++ {
		jlandRun(jumper, 0.5, jlandControls{MoveX: 1, Jump: true})
	}
	if jumper.P.X < 21.5 {
		t.Fatalf("jumping never cleared the wall (x=%.2f)", jumper.P.X)
	}
}

func TestJlandHeadBumpStopsTheJump(t *testing.T) {
	world := jlandFlatWorld(40, 30, 20)
	for x := 5; x < 15; x++ {
		world.set(x, 17, jlandStone) // a ceiling just above a standing head
	}
	g := jlandStandingGame(world, 10.5)
	for i := 0; i < 20; i++ {
		jlandRun(g, 0.05, jlandControls{Jump: i == 0})
		if world.boxHits(g.P.X, g.P.Y) {
			t.Fatalf("the jump went into the ceiling at y=%.3f", g.P.Y)
		}
		if top := g.P.Y - jlandPlayerH; top < 18-1e-3 {
			t.Fatalf("the head reached %.3f, above the ceiling's underside at 18", top)
		}
	}
	jlandRun(g, 1, jlandControls{})
	if !g.P.OnGround || g.P.Y != 20 {
		t.Fatalf("player did not land again (y=%.3f)", g.P.Y)
	}
}

func TestJlandArcTargets(t *testing.T) {
	world := jlandFlatWorld(40, 40, 30)
	g := jlandStandingGame(world, 10.5)
	want := [][2]int{{10, 27}, {11, 27}, {11, 28}, {11, 29}, {11, 30}, {10, 30}}
	for i, w := range want {
		g.Aim = i
		if x, y := g.target(); x != w[0] || y != w[1] {
			t.Errorf("facing right, aim %d -> (%d,%d), want (%d,%d)", i, x, y, w[0], w[1])
		}
	}
	g.P.Facing = -1
	g.Aim = jlandArcDefault
	if x, y := g.target(); x != 9 || y != 29 {
		t.Errorf("facing left, default aim -> (%d,%d), want (9,29)", x, y)
	}
	// Straddling two columns, "in front" is the first column the body does not
	// touch.
	g.P.Facing = 1
	g.P.X = 10.9
	if x, _ := g.target(); x != 12 {
		t.Errorf("straddling player aims at column %d, want 12", x)
	}
	g.Aim = 0
	g.stepAim(-5)
	if g.Aim != 0 {
		t.Errorf("aim went past the top of the dial: %d", g.Aim)
	}
	g.stepAim(50)
	if g.Aim != len(jlandArc)-1 {
		t.Errorf("aim went past the bottom of the dial: %d", g.Aim)
	}
}

func TestJlandPlaceRules(t *testing.T) {
	world := jlandFlatWorld(40, 40, 30)
	world.set(12, 29, jlandWater)
	g := jlandStandingGame(world, 10.5)
	g.Slot = 5 // planks
	cases := []struct {
		name   string
		x, y   int
		tile   jlandTile
		result jlandResult
	}{
		{"into the ground", 10, 30, jlandPlanks, jlandOccupied},
		{"into the player", 10, 29, jlandPlanks, jlandInTheWay},
		{"a torch where the player stands", 10, 29, jlandTorch, jlandOK},
		{"in front of the feet", 11, 29, jlandPlanks, jlandOK},
		{"into water", 12, 29, jlandPlanks, jlandOK},
		{"out of reach", 20, 29, jlandPlanks, jlandTooFar},
		{"outside the world", -1, 29, jlandPlanks, jlandOutside},
	}
	for _, tc := range cases {
		if got := g.canPlace(tc.x, tc.y, tc.tile); got != tc.result {
			t.Errorf("%s: canPlace = %d, want %d", tc.name, got, tc.result)
		}
	}
	if got := g.placeAt(11, 29); got != jlandOK || world.at(11, 29) != jlandPlanks {
		t.Fatalf("placeAt = %d, cell = %v", got, world.at(11, 29))
	}
	if g.Stats.Placed != 1 || !g.dirty {
		t.Fatalf("placing did not count (placed=%d dirty=%v)", g.Stats.Placed, g.dirty)
	}
	if world.skyRow(11) != 29 {
		t.Fatalf("the light boundary did not follow the new block: %d", world.skyRow(11))
	}
}

func TestJlandBreakRules(t *testing.T) {
	world := jlandFlatWorld(40, 40, 30)
	world.set(11, 31, jlandDiamond)
	g := jlandStandingGame(world, 10.5)
	if _, r := g.breakAt(11, 29); r != jlandNothing {
		t.Errorf("breaking air = %d, want nothing", r)
	}
	if _, r := g.breakAt(10, 39); r != jlandTooFar {
		t.Errorf("breaking the far bedrock = %d, want too far", r)
	}
	g.P.Y = 38
	world.set(10, 38, jlandAir)
	world.set(10, 37, jlandAir)
	if _, r := g.breakAt(10, 39); r != jlandTooHard {
		t.Errorf("breaking bedrock = %d, want too hard", r)
	}
	g.P.Y = 30
	if was, r := g.breakAt(10, 30); r != jlandOK || was != jlandGrass {
		t.Fatalf("breaking the grass underfoot = %v, %d", was, r)
	}
	if was, r := g.breakAt(11, 31); r != jlandOK || was != jlandDiamond {
		t.Fatalf("breaking the diamond = %v, %d", was, r)
	}
	if g.Stats.Ores[3] != 1 || g.Stats.Mined != 2 {
		t.Fatalf("stats after mining = %+v", g.Stats)
	}
	// With the ground gone, the player falls into the hole.
	jlandRun(g, 1, jlandControls{})
	if g.P.Y <= 30 {
		t.Fatalf("the player did not fall into the dug hole (y=%.2f)", g.P.Y)
	}
}

// Jumping and building straight down is how a player climbs: the block goes
// under the feet and the player lands on it.
func TestJlandPillarJump(t *testing.T) {
	world := jlandFlatWorld(40, 40, 30)
	g := jlandStandingGame(world, 10.5)
	g.Aim = len(jlandArc) - 1 // straight down
	jlandRun(g, jlandStep, jlandControls{Jump: true})
	for i := 0; i < 120 && g.P.VY < 0; i++ {
		jlandRun(g, jlandStep, jlandControls{})
	}
	if r := g.placeAt(g.target()); r != jlandOK {
		t.Fatalf("placing under the feet at the top of a jump = %d", r)
	}
	jlandRun(g, 1, jlandControls{})
	if !g.P.OnGround || g.P.Y != 29 {
		t.Fatalf("player at y=%.3f, want standing on the new block (29)", g.P.Y)
	}
}

func TestJlandHotbarCycles(t *testing.T) {
	g := &jlandGame{}
	g.cycleSlot(-1)
	if g.Slot != len(jlandHotbar)-1 {
		t.Fatalf("stepping left from the first slot = %d", g.Slot)
	}
	g.cycleSlot(1)
	if g.Slot != 0 || g.selected() != jlandHotbar[0] {
		t.Fatalf("stepping right wraps to %d", g.Slot)
	}
	g.Slot = 99
	if g.selected() != jlandHotbar[0] || g.Slot != 0 {
		t.Fatal("a slot from a longer hotbar was not clamped")
	}
	for i, t2 := range jlandHotbar {
		if !jlandDef(t2).Breakable {
			t.Errorf("hotbar slot %d builds %s, which could never be removed", i, jlandDef(t2).Name)
		}
	}
}

func TestJlandSaveRoundTrip(t *testing.T) {
	g := newJlandGame(7)
	g.World.set(5, 5, jlandBrick)
	g.Slot = 3
	g.Stats.Ores = [4]int{1, 2, 3, 4}
	g.Clock = 123.5
	data, err := json.Marshal(g.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var state JukaLandState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	back := jlandFromState(&state)
	if back == nil {
		t.Fatal("a fresh save did not load")
	}
	for i := range g.World.Tiles {
		if g.World.Tiles[i] != back.World.Tiles[i] {
			t.Fatalf("cell %d changed across a save: %v -> %v", i, g.World.Tiles[i], back.World.Tiles[i])
		}
	}
	if back.P.X != g.P.X || back.P.Y != g.P.Y || back.Slot != 3 || back.Clock != 123.5 || back.Stats.Ores != g.Stats.Ores {
		t.Fatalf("state changed across a save: %+v vs %+v", back.P, g.P)
	}
	if back.World.skyRow(5) != 5 {
		t.Fatalf("the light boundary was not rebuilt: %d", back.World.skyRow(5))
	}
}

func TestJlandFromStateRejectsOldAndDamagedSaves(t *testing.T) {
	if jlandFromState(nil) != nil {
		t.Fatal("nil save loaded")
	}
	// The first JukaLand's save has none of these fields.
	var old JukaLandState
	if err := json.Unmarshal([]byte(`{"PlayerX":800,"TileMap":[[0,1]],"Score":3}`), &old); err != nil {
		t.Fatal(err)
	}
	if jlandFromState(&old) != nil {
		t.Fatal("a save from the first JukaLand was loaded as this format")
	}
	s := newJlandGame(3).snapshot()
	short := *s
	short.Tiles = short.Tiles[:10]
	if jlandFromState(&short) != nil {
		t.Fatal("a truncated save loaded")
	}
	// A tile value no tile has reads as air; a player stuck inside rock is
	// put back on the surface.
	damaged := *s
	damaged.Tiles = append([]byte(nil), s.Tiles...)
	damaged.Tiles[0] = 250
	damaged.X, damaged.Y = 10.5, float64(s.H-2)
	g := jlandFromState(&damaged)
	if g == nil {
		t.Fatal("a repairable save did not load")
	}
	if g.World.Tiles[0] != jlandAir {
		t.Fatalf("an unknown tile loaded as %v", g.World.Tiles[0])
	}
	if g.World.boxHits(g.P.X, g.P.Y) {
		t.Fatal("a player saved inside the rock was not moved out")
	}
}

func TestJlandLighting(t *testing.T) {
	world := jlandFlatWorld(40, 40, 10)
	g := jlandStandingGame(world, 5.5)
	if l := g.lightAt(5, 4, 1, nil); l != 1 {
		t.Errorf("open sky at noon = %.2f, want 1", l)
	}
	if l := g.lightAt(5, 30, 1, nil); l > 0.06 {
		t.Errorf("twenty rows down = %.2f, want dark", l)
	}
	torch := []jlandLight{{X: 5.5, Y: 30.5, Radius: 7, Power: 0.95}}
	if l := g.lightAt(5, 30, 1, torch); l < 0.9 {
		t.Errorf("next to a torch = %.2f, want lit", l)
	}
	if d := jlandDaylight(jlandDayLength * 0.75); d > 0.3 {
		t.Errorf("midnight daylight = %.2f", d)
	}
	if d := jlandDaylight(jlandDayLength * 0.2); d != 1 {
		t.Errorf("midday daylight = %.2f", d)
	}
}

func TestJlandAtlasArt(t *testing.T) {
	pix := jlandAtlasPixels()
	stride := jlandAtlasCells * jlandArt * 4
	if len(pix) != stride*jlandArt {
		t.Fatalf("atlas is %d bytes, want %d", len(pix), stride*jlandArt)
	}
	opaque := func(cell int) int {
		n := 0
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				if pix[y*stride+(cell*jlandArt+x)*4+3] >= 200 {
					n++
				}
			}
		}
		return n
	}
	for _, tile := range []jlandTile{jlandGrass, jlandDirt, jlandStone, jlandCobble, jlandPlanks, jlandBrick, jlandBedrock, jlandDiamond} {
		if n := opaque(int(tile)); n < jlandArt*jlandArt*9/10 {
			t.Errorf("%s is only %d/%d opaque", jlandDef(tile).Name, n, jlandArt*jlandArt)
		}
	}
	if n := opaque(int(jlandGlass)); n > jlandArt*jlandArt/2 {
		t.Errorf("glass is %d/%d opaque: it should be see-through", n, jlandArt*jlandArt)
	}
	if opaque(int(jlandAir)) != 0 {
		t.Error("air has visible pixels")
	}
	if jlandTileColor(jlandGrass) == jlandTileColor(jlandDirt) || jlandTileColor(jlandGold) == jlandTileColor(jlandIron) {
		t.Error("distinct tiles share one colour")
	}
}

func TestJlandKeysCapturedOnTheJukaLandScreen(t *testing.T) {
	prev := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prev })
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "JukaLand"}}}
	currentSceneIndex = 0
	if jlandKeysCaptured(config) {
		t.Fatal("Home's keys were captured by JukaLand")
	}
	currentSceneIndex = 1
	if !jlandKeysCaptured(config) {
		t.Fatal("JukaLand's E would also switch scenes")
	}
}
