package main

// game_library_cube.go — the library's Cube layout (value "cube",
// game_library_view.go).
//
// The highlighted game's cover is the front face of a cube, seen a little
// from above and from the right so its top and side read as a solid. Up and
// Down roll the cube to the previous and next game; Left and Right turn it to
// the previous and next console, whose first game comes round on the side
// face. Mid-turn two faces show, the cover leaving on one and the next
// arriving on the other; the faces fade between cover and the cube's own
// colour as they turn, so the rest pose after a turn is the same picture as
// before it. B goes back to the consoles, as on every console page (Left no
// longer does here: it turns the cube); L2/R2 still jump by letter, A opens
// the game, and a click on the front face opens it too.
//
// The faces are drawn with the 3D helpers of game_library_flow.go: a 4×4
// mesh per face (a tilted face changes depth along both axes), back faces
// culled by their screen winding, so a convex cube needs no depth sort.

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	cubeTurnMS = 260
	cubeGrid   = 4     // mesh quads along each edge of a face
	cubeYaw    = 0.36  // rad: the rest pose's turn, which shows the right face
	cubePitch  = -0.27 // rad: and its tilt, which shows the top
)

// The six faces, as libVec3 corners of a cube of half-edge 1 (top-left,
// top-right, bottom-right, bottom-left as the face reads when it is the one in
// front), and the turn that brings each one to the front.
const (
	cubeFront = iota
	cubeBottom
	cubeTop
	cubeRight
	cubeLeft
	cubeBack
	cubeFaces
)

var cubeCorners = [cubeFaces][4]libVec3{
	cubeFront:  {{-1, -1, -1}, {1, -1, -1}, {1, 1, -1}, {-1, 1, -1}},
	cubeBottom: {{-1, 1, -1}, {1, 1, -1}, {1, 1, 1}, {-1, 1, 1}},
	cubeTop:    {{-1, -1, 1}, {1, -1, 1}, {1, -1, -1}, {-1, -1, -1}},
	cubeRight:  {{1, -1, -1}, {1, -1, 1}, {1, 1, 1}, {1, 1, -1}},
	cubeLeft:   {{-1, -1, 1}, {-1, -1, -1}, {-1, 1, -1}, {-1, 1, 1}},
	cubeBack:   {{1, -1, 1}, {-1, -1, 1}, {-1, 1, 1}, {1, 1, 1}},
}

var cubeNormals = [cubeFaces]libVec3{
	cubeFront: {0, 0, -1}, cubeBottom: {0, 1, 0}, cubeTop: {0, -1, 0},
	cubeRight: {1, 0, 0}, cubeLeft: {-1, 0, 0}, cubeBack: {0, 0, 1},
}

var libraryCube struct {
	turning  bool
	vertical bool // Up/Down (about the x axis) rather than Left/Right
	dir      int  // +1 next, -1 previous
	start    uint64
	from     libraryGame // the game on the face turning away

	// snapFailed: the video snap's texture would not draw on a face; the
	// cover stands in for it from then on.
	snapFailed bool
	verts      [(cubeGrid + 1) * (cubeGrid + 1)]sdl.Vertex
}

var cubeIdx = libraryMeshIndices(cubeGrid, cubeGrid, 1)

// cubeHints is the console page's extra footer hint in this layout.
var cubeHints = []FooterHint{{"←/→", "Console"}}

// libraryLayoutPageHints is the footer hint a layout adds on a console's page
// (game_library_ui.go's gameLibraryFooterHints).
func libraryLayoutPageHints() []FooterHint {
	if libraryLayoutMode() == libraryLayoutCube && len(currentGameSystems()) > 1 {
		return cubeHints
	}
	return nil
}

// gameLibraryLayoutMove is the D-pad on a console's page in Cover Flow and
// the Cube; false leaves the step to the other layouts' rules
// (gameLibraryMove).
func gameLibraryLayoutMove(dx, dy int) bool {
	switch libraryLayoutMode() {
	case libraryLayoutFlow:
		// The 3D shelf's keys: Left/Right through the games, Up to the
		// consoles.
		if dy < 0 {
			gameLib.focus = 0
			PlayNavSound()
			return true
		}
		system, ok := selectedLibrarySystem()
		if !ok || len(system.Games) == 0 || dx == 0 {
			return true
		}
		gameLib.cursor = (gameLib.cursor + dx + len(system.Games)) % len(system.Games)
		PlayNavSound()
		return true
	case libraryLayoutCube:
		libraryCubeMove(dx, dy)
		return true
	}
	return false
}

// libraryCubeMove rolls the cube: Up/Down through the console's games,
// Left/Right to the console before or after (its first game).
func libraryCubeMove(dx, dy int) {
	system, ok := selectedLibrarySystem()
	if !ok {
		return
	}
	from, _ := selectedLibraryGame()
	switch {
	case dy != 0:
		if len(system.Games) < 2 {
			return
		}
		step := 1
		if dy < 0 {
			step = -1
		}
		gameLib.cursor = (gameLib.cursor + step + len(system.Games)) % len(system.Games)
		libraryCubeTurn(true, step, from)
	case dx != 0:
		systems := currentGameSystems()
		if len(systems) < 2 {
			return
		}
		step := 1
		if dx < 0 {
			step = -1
		}
		gameLib.system = (gameLib.system + step + len(systems)) % len(systems)
		gameLib.cursor, gameLib.scroll = 0, 0
		libraryCubeTurn(false, step, from)
	default:
		return
	}
	PlayNavSound()
}

// libraryCubeTurn starts a turn away from the game from. A turn asked for
// mid-turn starts from the face the last one was bringing in.
func libraryCubeTurn(vertical bool, dir int, from libraryGame) {
	libraryCube.turning, libraryCube.vertical, libraryCube.dir = true, vertical, dir
	libraryCube.start = sdl.GetTicks64()
	libraryCube.from = from
}

// cubeRotX and cubeRotY turn a point about the x and y axes.
func cubeRotX(v libVec3, a float64) libVec3 {
	c, s := math.Cos(a), math.Sin(a)
	return libVec3{X: v.X, Y: v.Y*c + v.Z*s, Z: -v.Y*s + v.Z*c}
}

func cubeRotY(v libVec3, a float64) libVec3 {
	c, s := math.Cos(a), math.Sin(a)
	return libVec3{X: v.X*c + v.Z*s, Y: v.Y, Z: -v.X*s + v.Z*c}
}

// cubePose is the cube's turn at one moment: the turn in flight, then the
// rest pose's tilt.
type cubePose struct {
	vertical bool
	turn     float64
}

func (p cubePose) apply(v libVec3) libVec3 {
	if p.turn != 0 {
		if p.vertical {
			v = cubeRotX(v, p.turn)
		} else {
			v = cubeRotY(v, p.turn)
		}
	}
	return cubeRotX(cubeRotY(v, cubeYaw), cubePitch)
}

// renderLibraryCube draws the console's highlighted game as the cube's front
// face in the area x, y, w, h and records the front face as its click target.
func renderLibraryCube(renderer *sdl.Renderer, config *Config, system librarySystem, x, y, w, h int32, font, medium *ttf.Font) {
	n := len(system.Games)
	if n == 0 || renderer == nil {
		return
	}
	if gameLib.cursor < 0 || gameLib.cursor >= n {
		gameLib.cursor = max(0, min(gameLib.cursor, n-1))
	}
	gameLib.scroll = gameLib.cursor
	game := system.Games[gameLib.cursor]

	// How far the turn has gone: 0..1, eased; no turn without motion.
	p := 1.0
	if libraryCube.turning {
		if gswMotion(config) {
			now := sdl.GetTicks64()
			if now > libraryCube.start {
				p = float64(now-libraryCube.start) / cubeTurnMS
			} else {
				p = 0
			}
		}
		if p >= 1 {
			libraryCube.turning = false
		} else {
			requestFrameBurst()
		}
	}
	p = uiEaseOutCubic(p)
	turning := libraryCube.turning

	medH, smallH := int32(medium.Height()), int32(font.Height())
	textH := medH + smallH + 10
	half := math.Min(float64(h-textH-24)/2.7, float64(w)*0.19)
	if half < 40 {
		half = 40
	}
	cx := float64(x) + float64(w)/2
	cy := float64(y) + float64(h-textH)/2
	proj := libraryProjection{cx: cx, cy: cy, focal: half * 5}
	pose := cubePose{}
	toFace := cubeFront
	if turning {
		pose = cubePose{vertical: libraryCube.vertical, turn: float64(libraryCube.dir) * p * math.Pi / 2}
		switch {
		case libraryCube.vertical && libraryCube.dir > 0:
			toFace = cubeBottom
		case libraryCube.vertical:
			toFace = cubeTop
		case libraryCube.dir > 0:
			toFace = cubeRight
		default:
			toFace = cubeLeft
		}
	}

	// The cover on the front face, or the video snap at rest (snappreview.go).
	// Both covers are read first and then taken from the cache: reading one
	// can evict (and destroy) an older texture.
	libraryCubeLoad(renderer, game)
	if turning {
		libraryCubeLoad(renderer, libraryCube.from)
	}
	toTex := libraryCachedArt(game)
	snap := false
	if !turning && !libraryCube.snapFailed {
		if clip := snapPreviewTexture(renderer, game.Path); clip != nil {
			toTex, snap = clip, true
		}
	}
	var fromTex *sdl.Texture
	if turning {
		fromTex = libraryCachedArt(libraryCube.from)
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	var front sdl.Rect
	for face := 0; face < cubeFaces; face++ {
		var tex *sdl.Texture
		alpha := 0.0
		switch {
		case turning && face == cubeFront:
			tex, alpha = fromTex, 1-p
		case face == toFace:
			tex, alpha = toTex, p
		}
		r, ok := cubeDrawFace(renderer, proj, pose, half, face, tex, alpha, snap && face == toFace)
		if ok && face == toFace {
			front = r
		}
	}
	if front.W > 0 {
		gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: gameLib.cursor, rect: front})
		if !turning && toTex == nil && front.W > 24 {
			// No cover: the game's glyph on the blank face, as the shelf does.
			icon := min(front.W, front.H) / 3
			glyph := "disc"
			if builtIn, ok := builtInLibraryGameFor(game); ok {
				glyph = builtIn.glyph
			}
			DrawHomeIcon(renderer, glyph, sdl.Rect{X: front.X + (front.W-icon)/2, Y: front.Y + (front.H-icon)/2, W: icon, H: icon}, false)
		}
		if !turning {
			nasLibraryArtBadge(renderer, game, front) // a network drive's game (nas_index.go)
		}
	}

	// The neighbouring consoles beside the cube, where Left and Right go.
	systems := currentGameSystems()
	if len(systems) > 1 && gameLib.system >= 0 && gameLib.system < len(systems) {
		prev := systems[(gameLib.system-1+len(systems))%len(systems)].Name
		next := systems[(gameLib.system+1)%len(systems)].Name
		side := int32(half*1.9) + 16
		room := int32(cx) - side - x - 8
		if room > 60 {
			ly := int32(cy) - smallH/2
			drawText(renderer, font, ellipsize(font, "‹ "+consoleDisplayName(prev), room), x+8, ly, ColorTextTertiary(), textAlignLeft)
			drawText(renderer, font, ellipsize(font, consoleDisplayName(next)+" ›", room), x+w-8, ly, ColorTextTertiary(), textAlignRight)
		}
	}

	// The title, and where it is in the console, under the cube.
	titleY := int32(cy+half*1.45) + 10
	if limit := y + h - textH; titleY > limit {
		titleY = limit
	}
	titleScrollDrawText(renderer, config, medium, game.Label(), int32(cx), titleY, w-48, ColorTextPrimary(), textAlignCenter, gameLib.focus == 1)
	line := itoa(gameLib.cursor+1) + " of " + itoa(n)
	if game.System != "" && game.System != system.Name {
		line = consoleDisplayName(game.System) + " · " + line
	}
	if game.Hidden {
		line += " · Hidden"
	}
	drawText(renderer, font, ellipsize(font, line, w-48), int32(cx), titleY+medH+4, ColorTextTertiary(), textAlignCenter)
}

// libraryCubeLoad reads game's cover into the library's cache the first time
// it is shown (artTexture).
func libraryCubeLoad(renderer *sdl.Renderer, game libraryGame) {
	if game.Art != "" {
		artTexture(renderer, game.Art)
	}
}

// cubeDrawFace draws one face of the cube at pose: the cube's own shaded
// colour, then tex over it at alpha (0 draws none), fitted inside the face.
// It reports the face's screen rectangle, and false for a face turned away.
func cubeDrawFace(renderer *sdl.Renderer, proj libraryProjection, pose cubePose, half float64, face int, tex *sdl.Texture, alpha float64, snap bool) (sdl.Rect, bool) {
	var q [4]libVec3
	for i, c := range cubeCorners[face] {
		v := pose.apply(c)
		q[i] = libVec3{X: v.X * half, Y: v.Y * half, Z: (v.Z + 1) * half}
	}
	// Back faces wind the other way on screen.
	a, b, c, d := proj.at(q[0]), proj.at(q[1]), proj.at(q[2]), proj.at(q[3])
	area := (a.X*b.Y - b.X*a.Y) + (b.X*c.Y - c.X*b.Y) + (c.X*d.Y - d.X*c.Y) + (d.X*a.Y - a.X*d.Y)
	if area <= 0 {
		return sdl.Rect{}, false
	}
	// Lit from the front: a face turned away from the player is darker.
	normal := pose.apply(cubeNormals[face])
	k := 0.45 + 0.55*uiClamp01(-normal.Z)
	body := libraryShade(ColorPanelRaised, k, 255)
	edge := libraryShade(installHighlight(), 0.4+0.6*k, 200)

	v := libraryCube.verts[:]
	var box libBox
	full := [4]sdl.FPoint{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}}
	m := libraryMeshFill(v, proj, q, full, cubeGrid, cubeGrid, body, body, &box)
	rect := box.rect()
	if !libraryGeomFailed {
		if err := renderer.RenderGeometry(nil, v[:m], cubeIdx); err != nil {
			libraryGeomFailedNow(err)
		}
	}
	if libraryGeomFailed {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 4, body)
	}

	if tex != nil && alpha > 0.01 {
		// The picture keeps its shape: a band (or column) in the face's middle.
		fw, fh := libraryFitSize(tex, 1, 1, 1)
		s0, t0 := (1-fw)/2, (1-fh)/2
		sub := [4]libVec3{
			cubeFacePoint(q, s0, t0), cubeFacePoint(q, 1-s0, t0),
			cubeFacePoint(q, 1-s0, 1-t0), cubeFacePoint(q, s0, 1-t0),
		}
		lit := libraryShade(sdl.Color{R: 255, G: 255, B: 255, A: 255}, k, uint8(255*uiClamp01(alpha)))
		drawn := false
		if !libraryGeomFailed {
			m = libraryMeshFill(v, proj, sub, full, cubeGrid, cubeGrid, lit, lit, nil)
			_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
			err := renderer.RenderGeometry(tex, v[:m], cubeIdx)
			switch {
			case err == nil:
				drawn = true
			case snap:
				// The clip's YUV texture: the cover again from the next frame.
				libraryCube.snapFailed = true
			default:
				libraryGeomFailedNow(err)
			}
		}
		if !drawn && (libraryGeomFailed || snap) {
			var inner libBox
			for _, p := range sub {
				inner.add(proj.at(p))
			}
			dst := inner.rect()
			_ = tex.SetColorMod(lit.R, lit.G, lit.B)
			_ = tex.SetAlphaMod(lit.A)
			_ = renderer.Copy(tex, nil, &dst)
			_ = tex.SetColorMod(255, 255, 255)
			_ = tex.SetAlphaMod(255)
		}
	}

	// The cube's edges, so its faces read as one solid.
	_ = renderer.SetDrawColor(edge.R, edge.G, edge.B, edge.A)
	_ = renderer.DrawLineF(a.X, a.Y, b.X, b.Y)
	_ = renderer.DrawLineF(b.X, b.Y, c.X, c.Y)
	_ = renderer.DrawLineF(c.X, c.Y, d.X, d.Y)
	_ = renderer.DrawLineF(d.X, d.Y, a.X, a.Y)
	return rect, true
}

// cubeFacePoint is the point at (s, t) across the flat quad q.
func cubeFacePoint(q [4]libVec3, s, t float64) libVec3 {
	return libVec3{
		X: libLerp(libLerp(q[0].X, q[1].X, s), libLerp(q[3].X, q[2].X, s), t),
		Y: libLerp(libLerp(q[0].Y, q[1].Y, s), libLerp(q[3].Y, q[2].Y, s), t),
		Z: libLerp(libLerp(q[0].Z, q[1].Z, s), libLerp(q[3].Z, q[2].Z, s), t),
	}
}
