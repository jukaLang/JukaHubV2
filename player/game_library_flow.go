package main

// game_library_flow.go — the library's Cover Flow layout (value
// "covers_flow", game_library_view.go).
//
// The highlighted game's cover stands large in the middle of a console's
// page, facing the player; its neighbours on both sides are turned in
// perspective, overlapping and receding, over a soft reflection on the floor.
// Left/Right slide the row with the game switcher's easing (a frame burst
// while it moves, nothing under reduced motion or Battery Saver); Up goes back
// to the consoles, as on the 3D shelf. The cursor, the click targets (the
// nearest cover wins where two overlap), search, the letter jumps and the
// launch path are the other layouts' own. A video snap plays on the centre
// cover once the row has stopped.
//
// Each cover is one SDL_RenderGeometry call: a strip mesh over the turned
// quad, exact at every vertex so the affine error inside a strip stays
// invisible, and its reflection under it, sharing the cover's texture. The
// vertices live in a fixed array, so a frame allocates nothing. A renderer
// without geometry draws the same row as flat scaled covers.
//
// The 3D helpers at the end (libVec3, libraryProjection, libraryMeshFill) are
// the Cube layout's too (game_library_cube.go).

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	flowStrips   = 6    // vertical strips per cover
	flowSide     = 4    // covers drawn on each side of the centre
	flowSlideTau = 70.0 // ms: the row's easing time constant
	flowAngle    = 1.08 // rad, about 62°: how far a side cover is turned
	flowReflect  = 0.24 // the reflection's height, as a share of the cover's
	// flowLoadsPerFrame bounds the covers read from the card in one frame:
	// opening a console in this layout would otherwise read eleven pictures
	// before its first frame. The nearest come first; the rest follow.
	flowLoadsPerFrame = 2
	// flowSlots is how many covers one frame can draw: the centre, flowSide
	// on each side and one more on each side fading in.
	flowSlots = 2*flowSide + 3
)

var libraryFlow struct {
	pos    float64 // the row's position, in games, easing towards the cursor
	lastMS uint64
	moving bool
	frame  uint64 // frameCount when the row was last drawn
	system string // console the position belongs to: another one snaps
	count  int

	verts [4 * (flowStrips + 1)]sdl.Vertex // one cover and its reflection
	box   [flowSlots]sdl.Rect              // by game index - first: where it landed
	fade  [flowSlots]float64

	// tried lists the covers read since the cursor last moved (triedSys,
	// triedAt). One missing again was evicted by the cache's budget for
	// another cover of the row (covers too large for it to hold them all):
	// it stays blank until the cursor moves, rather than the covers reading
	// each other out of the cache frame after frame. The highlighted game's
	// is read again (only the row's first reads can evict it). An emptied
	// cache (a cover chosen, a fetch finished) starts the list again.
	tried    [3 * flowSlots]string
	triedN   int
	triedSys string
	triedAt  int
}

// libraryGeomFailed is set when SDL_RenderGeometry refuses (a renderer
// without it): both 3D layouts draw flat from then on.
var libraryGeomFailed bool

// flowIdx is the index list of one cover and its reflection: two strip meshes
// of flowStrips quads each.
var flowIdx = libraryMeshIndices(flowStrips, 1, 2)

// libraryFlowStep eases the row towards the cursor. A console just opened, a
// list that changed under it or motion switched off puts it there at once.
func libraryFlowStep(config *Config, system librarySystem) {
	now := sdl.GetTicks64()
	target := float64(gameLib.cursor)
	fresh := libraryFlow.frame == 0 || frameCount > libraryFlow.frame+1
	libraryFlow.frame = frameCount
	if fresh || system.Name != libraryFlow.system || len(system.Games) != libraryFlow.count {
		libraryFlow.system, libraryFlow.count = system.Name, len(system.Games)
		libraryFlow.pos, libraryFlow.moving = target, false
	}
	diff := target - libraryFlow.pos
	if math.Abs(diff) < 0.002 || !gswMotion(config) {
		libraryFlow.pos, libraryFlow.moving, libraryFlow.lastMS = target, false, now
		return
	}
	dt := 16.0
	if libraryFlow.moving && now > libraryFlow.lastMS {
		// From the last frame of the move; a move that starts from rest gets
		// one ordinary frame, not the idle screen's long sleep.
		dt = math.Min(float64(now-libraryFlow.lastMS), 100)
	}
	libraryFlow.lastMS, libraryFlow.moving = now, true
	// A jump (a letter, a wrap from the last game to the first) slides the
	// last two and a half covers rather than the whole console.
	if diff > 2.5 {
		libraryFlow.pos = target - 2.5
	} else if diff < -2.5 {
		libraryFlow.pos = target + 2.5
	}
	libraryFlow.pos += (target - libraryFlow.pos) * (1 - math.Exp(-dt/flowSlideTau))
	requestFrameBurst()
}

// flowPlace is where a cover d games from the row's position stands: its
// centre's x and depth, and its turn (positive turns its right edge away).
func flowPlace(d, s float64) (xc, zc, angle float64) {
	ad, sg := math.Abs(d), 1.0
	if d < 0 {
		sg = -1
	}
	gapC, gapS, depth := 0.64*s, 0.25*s, 0.5*s
	if ad < 1 {
		return sg * ad * gapC, ad * depth, sg * ad * flowAngle
	}
	return sg * (gapC + (ad-1)*gapS), depth, sg * flowAngle
}

// libraryFitSize is the texture's picture fitted into a square of side s, or
// the fallback size without one.
func libraryFitSize(tex *sdl.Texture, s, fallbackW, fallbackH float64) (float64, float64) {
	if tex == nil {
		return fallbackW, fallbackH
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return fallbackW, fallbackH
	}
	a := float64(tw) / float64(th)
	if a >= 1 {
		return s, s / a
	}
	return s * a, s
}

// flowLoad reads game's cover into the library's cache while the frame's
// budget lasts, and reports whether it had to wait for a later frame. A cover
// read already for this cursor is not read again (libraryFlow.tried), except
// the highlighted game's (cursor), which is always shown.
func flowLoad(renderer *sdl.Renderer, game libraryGame, loads *int, cursor bool) bool {
	path := game.Art
	if strings.TrimSpace(path) == "" {
		return false
	}
	if _, ok := gameLib.artCache[path]; ok {
		return false
	}
	seen := false
	for _, p := range libraryFlow.tried[:libraryFlow.triedN] {
		seen = seen || p == path
	}
	if seen && !cursor {
		return false // read already, and evicted since for another cover of the row
	}
	if *loads >= flowLoadsPerFrame {
		return true
	}
	*loads++
	if !seen && libraryFlow.triedN < len(libraryFlow.tried) {
		libraryFlow.tried[libraryFlow.triedN] = path
		libraryFlow.triedN++
	}
	artTexture(renderer, path)
	return false
}

// libraryCachedArt is a cover already in the library's cache, nil otherwise.
// Drawing takes the texture from the cache after the frame's loads: a load
// can evict (and destroy) an older entry, so one fetched before it may be gone.
func libraryCachedArt(game libraryGame) *sdl.Texture {
	if strings.TrimSpace(game.Art) == "" {
		return nil
	}
	return gameLib.artCache[game.Art]
}

// renderLibraryCoverFlow draws a console's games as Cover Flow in the area
// x, y, w, h and records their click targets.
func renderLibraryCoverFlow(renderer *sdl.Renderer, config *Config, system librarySystem, x, y, w, h int32, font, medium *ttf.Font) {
	n := len(system.Games)
	if n == 0 || renderer == nil {
		return
	}
	if gameLib.cursor < 0 || gameLib.cursor >= n {
		gameLib.cursor = max(0, min(gameLib.cursor, n-1))
	}
	gameLib.scroll = gameLib.cursor
	libraryFlowStep(config, system)
	pos := libraryFlow.pos

	medH, smallH := int32(medium.Height()), int32(font.Height())
	textH := medH + smallH + 10
	s := float64(h-textH-8) / (1 + flowReflect*0.8)
	if lim := float64(w) * 0.34; s > lim {
		s = lim
	}
	if s < 80 {
		s = 80
	}
	floorY := float64(y) + (float64(h-textH)-s*(1+flowReflect*0.8))/2 + s
	proj := libraryProjection{cx: float64(x) + float64(w)/2, cy: floorY - s/2, focal: s * 2.4}

	c := int(math.Floor(pos + 0.5))
	lo, hi := max(0, c-flowSide-1), min(n-1, c+flowSide+1)
	if c < lo {
		c = lo
	}
	if c > hi {
		c = hi
	}
	// Covers nearest the middle are read first.
	if libraryFlow.triedSys != system.Name || libraryFlow.triedAt != gameLib.cursor || len(gameLib.artCache) == 0 {
		libraryFlow.triedSys, libraryFlow.triedAt, libraryFlow.triedN = system.Name, gameLib.cursor, 0
	}
	loads := 0
	pending := flowLoad(renderer, system.Games[c], &loads, c == gameLib.cursor)
	for k := 1; k <= flowSide+1; k++ {
		for _, idx := range [2]int{c - k, c + k} {
			if idx >= lo && idx <= hi && flowLoad(renderer, system.Games[idx], &loads, idx == gameLib.cursor) {
				pending = true
			}
		}
	}
	if pending {
		requestFrameEvery(1)
	}

	settled := math.Abs(pos-float64(gameLib.cursor)) < 0.01
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// Painter's order: the outer covers first, the middle one last.
	for idx := lo; idx < c; idx++ {
		flowDrawCover(renderer, system, idx, lo, pos, s, proj, false)
	}
	for idx := hi; idx > c; idx-- {
		flowDrawCover(renderer, system, idx, lo, pos, s, proj, false)
	}
	flowDrawCover(renderer, system, c, lo, pos, s, proj, settled && c == gameLib.cursor)

	// Click targets, nearest first: where two covers overlap, the one drawn
	// on top takes the click.
	flowHit(c, lo)
	for k := 1; k <= flowSide+1; k++ {
		for _, idx := range [2]int{c - k, c + k} {
			if idx >= lo && idx <= hi {
				flowHit(idx, lo)
			}
		}
	}

	// The title, and where it is in the console, under the middle cover.
	game := system.Games[gameLib.cursor]
	cx := int32(proj.cx)
	titleY := int32(floorY+s*flowReflect*0.7) + 6
	titleScrollDrawText(renderer, config, medium, game.Label(), cx, titleY, w-48, ColorTextPrimary(), textAlignCenter, gameLib.focus == 1)
	line := itoa(gameLib.cursor+1) + " of " + itoa(n)
	if game.System != "" && game.System != system.Name {
		// The Continue row and a search mix consoles.
		line = consoleDisplayName(game.System) + " · " + line
	}
	if game.Hidden {
		line += " · Hidden"
	}
	drawText(renderer, font, ellipsize(font, line, w-48), cx, titleY+medH+4, ColorTextTertiary(), textAlignCenter)
}

// flowHit adds game idx's cover as a click target, unless it is all but
// faded out.
func flowHit(idx, lo int) {
	if r := libraryFlow.box[idx-lo]; r.W > 0 && r.H > 0 && libraryFlow.fade[idx-lo] > 0.3 {
		gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: idx, rect: r})
	}
}

// flowDrawCover draws game idx of the row at its place for the row position
// pos, and records where it landed.
func flowDrawCover(renderer *sdl.Renderer, system librarySystem, idx, lo int, pos, s float64, proj libraryProjection, focused bool) {
	slot := idx - lo
	libraryFlow.box[slot] = sdl.Rect{}
	d := float64(idx) - pos
	ad := math.Abs(d)
	fade := uiClamp01(float64(flowSide) + 1 - ad)
	libraryFlow.fade[slot] = fade
	if fade <= 0 {
		return
	}
	game := system.Games[idx]
	tex := libraryCachedArt(game)
	// A video snap on the middle cover once the row has stopped (snappreview.go).
	snap := false
	if focused {
		if clip := snapPreviewTexture(renderer, game.Path); clip != nil {
			tex, snap = clip, true
		}
	}
	cw, ch := libraryFitSize(tex, s, s*0.76, s)
	xc, zc, angle := flowPlace(d, s)
	ca, sa := math.Cos(angle), math.Sin(angle)
	half := cw / 2
	xl, zl := xc-half*ca, zc-half*sa
	xr, zr := xc+half*ca, zc+half*sa
	top, floor := s/2-ch, s/2
	rh := math.Min(flowReflect*s, ch)

	k := 1 - 0.38*math.Min(ad, 1) - 0.07*math.Max(0, ad-1)
	base := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	if tex == nil {
		base = ColorPanelRaised
	}
	lit := libraryShade(base, k, uint8(255*fade))
	refl := libraryShade(base, k, uint8(70*fade))
	gone := refl
	gone.A = 0

	v := libraryFlow.verts[:]
	var box libBox
	m := libraryMeshFill(v, proj, [4]libVec3{{xl, top, zl}, {xr, top, zr}, {xr, floor, zr}, {xl, floor, zl}},
		[4]sdl.FPoint{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}}, flowStrips, 1, lit, lit, &box)
	libraryMeshFill(v[m:], proj, [4]libVec3{{xl, floor, zl}, {xr, floor, zr}, {xr, floor + rh, zr}, {xl, floor + rh, zl}},
		[4]sdl.FPoint{{X: 0, Y: 1}, {X: 1, Y: 1}, {X: 1, Y: float32(1 - rh/ch)}, {X: 0, Y: float32(1 - rh/ch)}}, flowStrips, 1, refl, gone, nil)
	rect := box.rect()
	libraryFlow.box[slot] = rect

	drawn := false
	if !libraryGeomFailed || snap {
		if tex != nil {
			_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		}
		err := renderer.RenderGeometry(tex, v[:2*m], flowIdx)
		switch {
		case err == nil:
			drawn = true
		case !snap:
			libraryGeomFailedNow(err)
		}
	}
	if !drawn {
		// Flat: the turned cover's outline as a plain rectangle.
		if tex != nil {
			_ = tex.SetColorMod(lit.R, lit.G, lit.B)
			_ = tex.SetAlphaMod(lit.A)
			_ = renderer.Copy(tex, nil, &rect)
			_ = tex.SetColorMod(255, 255, 255)
			_ = tex.SetAlphaMod(255)
		} else {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 6, lit)
		}
	}
	if tex == nil && ad < 0.5 && rect.W > 24 {
		// No cover: the console's glyph on the blank box, as the shelf does.
		icon := min(rect.W, rect.H) / 3
		glyph := "disc"
		if builtIn, ok := builtInLibraryGameFor(game); ok {
			glyph = builtIn.glyph
		}
		DrawHomeIcon(renderer, glyph, sdl.Rect{X: rect.X + (rect.W-icon)/2, Y: rect.Y + (rect.H-icon)/2, W: icon, H: icon}, false)
	}
	if focused {
		strokeRoundedRect(renderer, rect.X-3, rect.Y-3, rect.W+6, rect.H+6, 4, 2, installHighlight())
		nasLibraryArtBadge(renderer, game, rect) // a network drive's game (nas_index.go)
	}
}

// libraryGeomFailedNow notes the first refusal: the layouts draw flat from
// here on, which is said once in the log.
func libraryGeomFailedNow(err error) {
	if libraryGeomFailed {
		return
	}
	libraryGeomFailed = true
	logWarn("library", "RenderGeometry is unavailable (%v): Cover Flow and the Cube draw flat covers", err)
}

// ── 3D helpers (Cover Flow and the Cube) ─────────────────────────────────

// libVec3 is a point of a library 3D layout: x right, y down, z away from the
// player, in screen pixels at depth 0.
type libVec3 struct{ X, Y, Z float64 }

// libraryProjection is a pinhole camera at focal pixels in front of the z = 0
// plane, which it draws 1:1 around (cx, cy).
type libraryProjection struct{ cx, cy, focal float64 }

func (p libraryProjection) at(v libVec3) sdl.FPoint {
	d := p.focal + v.Z
	if floor := p.focal * 0.1; d < floor {
		d = floor
	}
	k := p.focal / d
	return sdl.FPoint{X: float32(p.cx + v.X*k), Y: float32(p.cy + v.Y*k)}
}

// libBox is the screen rectangle around a mesh's vertices.
type libBox struct {
	x0, y0, x1, y1 float32
	ok             bool
}

func (b *libBox) add(p sdl.FPoint) {
	if !b.ok {
		b.x0, b.y0, b.x1, b.y1, b.ok = p.X, p.Y, p.X, p.Y, true
		return
	}
	b.x0, b.y0 = min(b.x0, p.X), min(b.y0, p.Y)
	b.x1, b.y1 = max(b.x1, p.X), max(b.y1, p.Y)
}

func (b libBox) rect() sdl.Rect {
	if !b.ok {
		return sdl.Rect{}
	}
	x0, y0 := int32(math.Floor(float64(b.x0))), int32(math.Floor(float64(b.y0)))
	return sdl.Rect{X: x0, Y: y0, W: int32(math.Ceil(float64(b.x1))) - x0, H: int32(math.Ceil(float64(b.y1))) - y0}
}

// libraryShade is c with its colour scaled by k and alpha a.
func libraryShade(c sdl.Color, k float64, a uint8) sdl.Color {
	k = uiClamp01(k)
	return sdl.Color{R: uint8(float64(c.R) * k), G: uint8(float64(c.G) * k), B: uint8(float64(c.B) * k), A: a}
}

// libraryMeshFill writes a (cols+1)×(rows+1) grid over the flat 3D quad q
// (top-left, top-right, bottom-right, bottom-left) into dst, row by row, with
// the texture corners uv and a colour fading from top to bottom. Every vertex
// is projected exactly; box, when given, grows around them. It returns the
// number of vertices written; dst must hold them.
func libraryMeshFill(dst []sdl.Vertex, proj libraryProjection, q [4]libVec3, uv [4]sdl.FPoint, cols, rows int, top, bottom sdl.Color, box *libBox) int {
	i := 0
	for r := 0; r <= rows; r++ {
		t := float64(r) / float64(rows)
		col := libraryMixColor(top, bottom, t)
		for c := 0; c <= cols; c++ {
			u := float64(c) / float64(cols)
			p := libVec3{
				X: libLerp(libLerp(q[0].X, q[1].X, u), libLerp(q[3].X, q[2].X, u), t),
				Y: libLerp(libLerp(q[0].Y, q[1].Y, u), libLerp(q[3].Y, q[2].Y, u), t),
				Z: libLerp(libLerp(q[0].Z, q[1].Z, u), libLerp(q[3].Z, q[2].Z, u), t),
			}
			at := proj.at(p)
			if box != nil {
				box.add(at)
			}
			dst[i] = sdl.Vertex{
				Position: at,
				Color:    col,
				TexCoord: sdl.FPoint{
					X: float32(libLerp(libLerp(float64(uv[0].X), float64(uv[1].X), u), libLerp(float64(uv[3].X), float64(uv[2].X), u), t)),
					Y: float32(libLerp(libLerp(float64(uv[0].Y), float64(uv[1].Y), u), libLerp(float64(uv[3].Y), float64(uv[2].Y), u), t)),
				},
			}
			i++
		}
	}
	return i
}

func libLerp(a, b, t float64) float64 { return a + (b-a)*t }

func libraryMixColor(a, b sdl.Color, t float64) sdl.Color {
	mix := func(x, y uint8) uint8 { return uint8(libLerp(float64(x), float64(y), t) + 0.5) }
	return sdl.Color{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: mix(a.A, b.A)}
}

// libraryMeshIndices is the triangle list of meshes grids of cols×rows quads
// laid out as libraryMeshFill writes them, one grid after the other.
func libraryMeshIndices(cols, rows, meshes int) []int32 {
	out := make([]int32, 0, meshes*cols*rows*6)
	per := (cols + 1) * (rows + 1)
	for m := 0; m < meshes; m++ {
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				tl := int32(m*per + r*(cols+1) + c)
				tr, bl := tl+1, tl+int32(cols+1)
				br := bl + 1
				out = append(out, tl, tr, br, tl, br, bl)
			}
		}
	}
	return out
}
