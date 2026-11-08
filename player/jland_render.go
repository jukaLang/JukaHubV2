package main

// jland_render.go — drawing JukaLand into any rectangle.
//
// The game is drawn into a view rectangle it is handed — the whole JukaLand
// screen under the header, or the page area of the browser's offline card — so
// nothing here assumes the full screen. The tiles are 16-pixel pixel art
// painted by code into one small atlas texture (no image files to ship), then
// scaled by whole multiples so they stay crisp. A frame is the sky with its
// day/night colours, the tiles, the player, water over the player, a darkness
// pass that turns caves and nights dark and torches into light, the aim cursor,
// break particles and the hotbar.
//
// TrimUI: every draw call reuses the package-level rects below (a pointer to a
// fresh sdl.Rect handed to cgo would escape to the heap once per tile), the
// atlas is built once per renderer, and the darkness pass merges runs of equal
// shade into one fill.

import (
	"math"
	"strconv"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// jlandArt is the atlas cell size in art pixels; tiles are drawn at whole
	// multiples of it.
	jlandArt            = 16
	jlandAtlasBackDirt  = int(jlandTileCount)
	jlandAtlasBackStone = int(jlandTileCount) + 1
	jlandAtlasWaterTop  = int(jlandTileCount) + 2
	jlandAtlasCells     = int(jlandTileCount) + 3
)

// jlandSlotKeys are the number keys that pick each hotbar slot.
var jlandSlotKeys = [...]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}

// ── Atlas pixels (pure Go, testable) ─────────────────────────────────────

// jlandCanvas paints one atlas cell inside the shared RGBA buffer.
type jlandCanvas struct {
	pix    []byte
	stride int
	cell   int
}

func (c *jlandCanvas) put(x, y int, col sdl.Color) {
	if x < 0 || y < 0 || x >= jlandArt || y >= jlandArt {
		return
	}
	i := y*c.stride + (c.cell*jlandArt+x)*4
	c.pix[i] = col.R
	c.pix[i+1] = col.G
	c.pix[i+2] = col.B
	c.pix[i+3] = col.A
}

// darken scales the cell's colour (not its alpha) by f.
func (c *jlandCanvas) darken(f float64) {
	for y := 0; y < jlandArt; y++ {
		for x := 0; x < jlandArt; x++ {
			i := y*c.stride + (c.cell*jlandArt+x)*4
			c.pix[i] = uint8(float64(c.pix[i]) * f)
			c.pix[i+1] = uint8(float64(c.pix[i+1]) * f)
			c.pix[i+2] = uint8(float64(c.pix[i+2]) * f)
		}
	}
}

// jlandHash is a stable per-pixel random number, so the art is the same on
// every run and every device.
func jlandHash(x, y, salt int) uint32 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + uint32(salt)*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	return h ^ (h >> 16)
}

func jlandRGB(r, g, b uint8) sdl.Color { return sdl.Color{R: r, G: g, B: b, A: 255} }

func jlandRGBA(r, g, b, a uint8) sdl.Color { return sdl.Color{R: r, G: g, B: b, A: a} }

// jlandShade scales a colour's channels by f, alpha untouched.
func jlandShade(col sdl.Color, f float64) sdl.Color {
	s := func(v uint8) uint8 {
		return uint8(math.Max(0, math.Min(255, float64(v)*f)))
	}
	return sdl.Color{R: s(col.R), G: s(col.G), B: s(col.B), A: col.A}
}

// jlandMix blends a toward b by t, opaque.
func jlandMix(a, b sdl.Color, t float64) sdl.Color {
	t = math.Max(0, math.Min(1, t))
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return sdl.Color{R: m(a.R, b.R), G: m(a.G, b.G), B: m(a.B, b.B), A: 255}
}

// speckle fills the cell with base and scatters dark and light flecks at the
// given rates (per mille).
func (c *jlandCanvas) speckle(base, dark, light sdl.Color, darkRate, lightRate uint32, salt int) {
	for y := 0; y < jlandArt; y++ {
		for x := 0; x < jlandArt; x++ {
			col := base
			switch h := jlandHash(x, y, salt) % 1000; {
			case h < darkRate:
				col = dark
			case h < darkRate+lightRate:
				col = light
			}
			c.put(x, y, col)
		}
	}
}

func (c *jlandCanvas) dirt(salt int) {
	c.speckle(jlandRGB(134, 90, 52), jlandRGB(108, 71, 40), jlandRGB(156, 108, 64), 180, 120, salt)
	for i := 0; i < 3; i++ {
		x, y := int(jlandHash(i, 7, salt)%15), int(jlandHash(i, 9, salt)%15)
		c.put(x, y, jlandRGB(150, 140, 128))
	}
}

func (c *jlandCanvas) stone(salt int) {
	c.speckle(jlandRGB(128, 133, 142), jlandRGB(106, 110, 119), jlandRGB(150, 155, 163), 200, 150, salt)
	crack := jlandRGB(92, 96, 104)
	for i := 0; i < 3; i++ {
		x0, y0 := int(jlandHash(i, 1, salt)%12), int(jlandHash(i, 2, salt)%14)+1
		for k := 0; k < 3; k++ {
			c.put(x0+k, y0+k/2, crack)
		}
	}
}

// ore paints stone with clusters of the ore's colour and a glint each.
func (c *jlandCanvas) ore(col, glint sdl.Color, salt int) {
	c.stone(salt)
	for i := 0; i < 4; i++ {
		x := 1 + int(jlandHash(i, 3, salt)%12)
		y := 1 + int(jlandHash(i, 5, salt)%12)
		c.put(x, y, col)
		c.put(x+1, y, col)
		c.put(x, y+1, col)
		c.put(x+1, y+1, jlandShade(col, 0.78))
		if jlandHash(i, 8, salt)%2 == 0 {
			c.put(x+2, y+1, col)
		}
		c.put(x, y, glint)
	}
}

// jlandCobbleStones are the rounded stones of a cobblestone cell: centre and
// radii in art pixels.
var jlandCobbleStones = [...][4]float64{
	{3.5, 3.5, 3.3, 3.0},
	{10.5, 3.0, 4.6, 2.8},
	{2.5, 10.5, 2.6, 4.2},
	{9.0, 9.5, 3.8, 3.4},
	{14.6, 9.0, 1.9, 3.2},
	{8.0, 14.6, 5.5, 1.7},
}

// jlandPaintCell draws one atlas cell.
func jlandPaintCell(c *jlandCanvas, cell int) {
	switch cell {
	case int(jlandAir):
		// Transparent: air is never drawn.
	case int(jlandGrass):
		c.dirt(11)
		for x := 0; x < jlandArt; x++ {
			depth := 3 + int(jlandHash(x, 0, 21)%3)
			for y := 0; y < depth; y++ {
				col := jlandRGB(95, 176, 62)
				switch h := jlandHash(x, y, 22) % 100; {
				case y == 0:
					col = jlandRGB(132, 212, 92)
				case h < 18:
					col = jlandRGB(70, 140, 48)
				case h < 30:
					col = jlandRGB(118, 198, 80)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandDirt):
		c.dirt(12)
	case int(jlandStone):
		c.stone(31)
	case int(jlandCobble):
		mortar := jlandRGB(78, 82, 90)
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				col := mortar
				for i, s := range jlandCobbleStones {
					dx := (float64(x) + 0.5 - s[0]) / s[2]
					dy := (float64(y) + 0.5 - s[1]) / s[3]
					if dx*dx+dy*dy > 1 {
						continue
					}
					col = jlandShade(jlandRGB(124, 129, 138), 0.88+float64(i%3)*0.08)
					if dy < -0.35 && dx < 0.2 {
						col = jlandShade(col, 1.18)
					} else if dy > 0.55 {
						col = jlandShade(col, 0.82)
					}
					break
				}
				c.put(x, y, col)
			}
		}
	case int(jlandSand):
		c.speckle(jlandRGB(222, 205, 140), jlandRGB(204, 186, 120), jlandRGB(236, 222, 166), 200, 150, 41)
	case int(jlandWater), jlandAtlasWaterTop:
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				col := jlandRGBA(52, 120, 212, 170)
				if jlandHash(x, y, 51)%100 < 12 {
					col = jlandRGBA(92, 156, 232, 170)
				}
				if cell == jlandAtlasWaterTop && y < 2 {
					col = jlandRGBA(112, 172, 240, 195)
					if (x+y*2)%5 < 3 {
						col = jlandRGBA(160, 206, 250, 215)
					}
				}
				c.put(x, y, col)
			}
		}
	case int(jlandTrunk):
		for y := 0; y < jlandArt; y++ {
			for x := 3; x <= 12; x++ {
				col := jlandRGB(110, 76, 46)
				switch {
				case x == 3 || x == 12:
					col = jlandRGB(70, 48, 28)
				case x%3 == 0:
					col = jlandRGB(84, 58, 34)
				case jlandHash(x, y, 61)%100 < 10:
					col = jlandRGB(134, 96, 62)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandLeaves):
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				h := jlandHash(x, y, 71) % 100
				edge := x == 0 || y == 0 || x == jlandArt-1 || y == jlandArt-1
				if h < 3 || (edge && h < 14) {
					continue
				}
				col := jlandRGB(60, 142, 60)
				if h < 30 {
					col = jlandRGB(42, 112, 46)
				} else if h < 42 {
					col = jlandRGB(94, 178, 82)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandLog):
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				col := jlandRGB(118, 82, 50)
				switch {
				case y == 0 || y == jlandArt-1:
					col = jlandRGB(74, 50, 30)
				case x%4 == 1:
					col = jlandRGB(90, 62, 37)
				case jlandHash(x, y, 81)%100 < 9:
					col = jlandRGB(140, 100, 64)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandPlanks):
		for y := 0; y < jlandArt; y++ {
			board := y / 4
			for x := 0; x < jlandArt; x++ {
				col := jlandRGB(186, 142, 88)
				switch h := jlandHash(x, y, 91) % 100; {
				case y%4 == 3:
					col = jlandRGB(132, 96, 56)
				case x == (board*7+3)%jlandArt:
					col = jlandRGB(150, 110, 66)
				case h < 10:
					col = jlandRGB(202, 160, 106)
				case h < 18:
					col = jlandRGB(168, 126, 76)
				}
				c.put(x, y, col)
			}
			if y%4 == 1 {
				c.put(1, y, jlandRGB(118, 98, 80))
				c.put(jlandArt-2, y, jlandRGB(118, 98, 80))
			}
		}
	case int(jlandBrick):
		for y := 0; y < jlandArt; y++ {
			row := y / 4
			for x := 0; x < jlandArt; x++ {
				col := jlandRGB(170, 74, 58)
				switch h := jlandHash(x, y, 101) % 100; {
				case y%4 == 3 || (x+(row%2)*4)%8 == 7:
					col = jlandRGB(210, 202, 190)
				case h < 15:
					col = jlandRGB(146, 60, 48)
				case h < 25:
					col = jlandRGB(190, 92, 74)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandGlass):
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				col := jlandRGBA(210, 236, 255, 46)
				switch {
				case x == 0 || y == 0 || x == jlandArt-1 || y == jlandArt-1:
					col = jlandRGBA(226, 244, 255, 215)
				case x == 1 || y == 1:
					col = jlandRGBA(255, 255, 255, 90)
				case (x+y == 6 || x+y == 7 || x+y == 12) && x >= 3 && x <= 12:
					col = jlandRGBA(255, 255, 255, 150)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandTorch):
		for y := 7; y < jlandArt; y++ {
			c.put(7, y, jlandRGB(142, 102, 62))
			c.put(8, y, jlandRGB(112, 78, 46))
		}
		outer := jlandRGB(255, 146, 40)
		for y := 4; y <= 6; y++ {
			c.put(6, y, outer)
			c.put(9, y, outer)
		}
		c.put(7, 3, outer)
		c.put(8, 3, outer)
		c.put(7, 2, jlandRGBA(255, 120, 30, 200))
		c.put(7, 4, jlandRGB(255, 206, 82))
		c.put(8, 4, jlandRGB(255, 206, 82))
		for y := 5; y <= 6; y++ {
			c.put(7, y, jlandRGB(255, 246, 204))
			c.put(8, y, jlandRGB(255, 236, 170))
		}
	case int(jlandCoal):
		c.ore(jlandRGB(36, 36, 40), jlandRGB(84, 84, 92), 111)
	case int(jlandIron):
		c.ore(jlandRGB(214, 168, 136), jlandRGB(244, 214, 192), 121)
	case int(jlandGold):
		c.ore(jlandRGB(248, 206, 54), jlandRGB(255, 244, 170), 131)
	case int(jlandDiamond):
		c.ore(jlandRGB(86, 226, 228), jlandRGB(236, 255, 255), 141)
	case int(jlandSnow):
		c.dirt(13)
		for x := 0; x < jlandArt; x++ {
			depth := 3 + int(jlandHash(x, 0, 151)%3)
			for y := 0; y < depth; y++ {
				col := jlandRGB(242, 247, 255)
				if jlandHash(x, y, 152)%100 < 25 {
					col = jlandRGB(216, 228, 242)
				}
				c.put(x, y, col)
			}
		}
	case int(jlandBedrock):
		c.speckle(jlandRGB(66, 66, 74), jlandRGB(40, 40, 46), jlandRGB(96, 96, 106), 350, 150, 161)
	case jlandAtlasBackDirt:
		c.dirt(14)
		c.darken(0.42)
	case jlandAtlasBackStone:
		c.stone(32)
		c.darken(0.42)
	}
}

// jlandAtlasCache is the painted atlas and the average colour of every cell,
// built once (both are pure functions of the code).
var jlandAtlasCache struct {
	pix    []byte
	colors [jlandAtlasCells]sdl.Color
}

// jlandAtlasPixels returns the atlas as RGBA bytes, row-major, one 16x16 cell
// per tile side by side.
func jlandAtlasPixels() []byte {
	if jlandAtlasCache.pix != nil {
		return jlandAtlasCache.pix
	}
	stride := jlandAtlasCells * jlandArt * 4
	pix := make([]byte, stride*jlandArt)
	for cell := 0; cell < jlandAtlasCells; cell++ {
		jlandPaintCell(&jlandCanvas{pix: pix, stride: stride, cell: cell}, cell)
		var sr, sg, sb, n int
		for y := 0; y < jlandArt; y++ {
			for x := 0; x < jlandArt; x++ {
				i := y*stride + (cell*jlandArt+x)*4
				if pix[i+3] < 100 {
					continue
				}
				sr, sg, sb, n = sr+int(pix[i]), sg+int(pix[i+1]), sb+int(pix[i+2]), n+1
			}
		}
		if n > 0 {
			jlandAtlasCache.colors[cell] = jlandRGB(uint8(sr/n), uint8(sg/n), uint8(sb/n))
		}
	}
	jlandAtlasCache.pix = pix
	return pix
}

// jlandTileColor is a tile's average colour (break particles, and the hotbar
// when no atlas texture could be made).
func jlandTileColor(t jlandTile) sdl.Color {
	jlandAtlasPixels()
	if int(t) < len(jlandAtlasCache.colors) {
		return jlandAtlasCache.colors[t]
	}
	return jlandRGB(128, 128, 128)
}

// ── Atlas texture ────────────────────────────────────────────────────────

// jlandAtlas is the texture, owned by the renderer it was made for. A new
// renderer gets a new texture; the old one went with its renderer.
var jlandAtlas struct {
	renderer *sdl.Renderer
	tex      *sdl.Texture
	failed   bool
}

func jlandAtlasFor(r *sdl.Renderer) *sdl.Texture {
	if r == nil {
		return nil
	}
	if jlandAtlas.renderer == r && (jlandAtlas.tex != nil || jlandAtlas.failed) {
		return jlandAtlas.tex
	}
	jlandAtlas.renderer = r
	jlandAtlas.tex = nil
	jlandAtlas.failed = true
	pix := jlandAtlasPixels()
	w := int32(jlandAtlasCells * jlandArt)
	tex, err := r.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, jlandArt)
	if err != nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pix[0]), int(w)*4); err != nil {
		tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	jlandAtlas.tex = tex
	jlandAtlas.failed = false
	return tex
}

// jlandDropAtlasForRenderer forgets the texture of a renderer that is about to
// be destroyed (tests create one per probe).
func jlandDropAtlasForRenderer(r *sdl.Renderer) {
	if jlandAtlas.renderer == r {
		jlandAtlas.renderer = nil
		jlandAtlas.tex = nil
		jlandAtlas.failed = false
	}
}

// ── Draw primitives ──────────────────────────────────────────────────────

var (
	jlandSrcRect  sdl.Rect
	jlandDstRect  sdl.Rect
	jlandFillRect sdl.Rect
)

func jlandCopyCell(r *sdl.Renderer, atlas *sdl.Texture, cell int, x, y, size int32) {
	jlandSrcRect = sdl.Rect{X: int32(cell * jlandArt), Y: 0, W: jlandArt, H: jlandArt}
	jlandDstRect = sdl.Rect{X: x, Y: y, W: size, H: size}
	_ = r.Copy(atlas, &jlandSrcRect, &jlandDstRect)
}

func jlandFill(r *sdl.Renderer, x, y, w, h int32, c sdl.Color) {
	if w <= 0 || h <= 0 || c.A == 0 {
		return
	}
	_ = r.SetDrawColor(c.R, c.G, c.B, c.A)
	jlandFillRect = sdl.Rect{X: x, Y: y, W: w, H: h}
	_ = r.FillRect(&jlandFillRect)
}

// jlandFloorDiv is integer division rounding toward negative infinity.
func jlandFloorDiv(a, b int32) int32 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// jlandWrap is x modulo m, always in [0, m).
func jlandWrap(x, m float64) float64 {
	v := math.Mod(x, m)
	if v < 0 {
		v += m
	}
	return v
}

// jlandTileSize is the on-screen tile size for a view: whole multiples of the
// 16-pixel art so the pixels stay square.
func jlandTileSize(view sdl.Rect) int32 {
	switch {
	case view.H >= 900:
		return 48
	case view.H >= 300:
		return 32
	}
	return 16
}

// jlandViewport maps world cells onto the screen for one frame.
type jlandViewport struct {
	view  sdl.Rect
	tile  int32
	camPX int32
	camPY int32
}

func (v jlandViewport) cellX(tx int32) int32 { return v.view.X + tx*v.tile - v.camPX }
func (v jlandViewport) cellY(ty int32) int32 { return v.view.Y + ty*v.tile - v.camPY }

// worldX / worldY turn a world coordinate (in tiles) into a screen pixel.
func (v jlandViewport) worldX(x float64) int32 {
	return v.view.X + int32(math.Round(x*float64(v.tile))) - v.camPX
}

func (v jlandViewport) worldY(y float64) int32 {
	return v.view.Y + int32(math.Round(y*float64(v.tile))) - v.camPY
}

// jlandViewportFor is the viewport of the session's camera on view.
func jlandViewportFor(view sdl.Rect) jlandViewport {
	t := jlandRT.tile
	if t <= 0 {
		t = jlandTileSize(view)
	}
	return jlandViewport{
		view:  view,
		tile:  t,
		camPX: int32(math.Floor(jlandRT.camX * float64(t))),
		camPY: int32(math.Floor(jlandRT.camY * float64(t))),
	}
}

// ── Frame ────────────────────────────────────────────────────────────────

// jlandDraw paints the session into view. hudBottom is the y the hotbar sits
// on (the standalone screen keeps it above its footer band).
func jlandDraw(r *sdl.Renderer, config *Config, view sdl.Rect, hudBottom int32) {
	g := jlandRT.game
	if r == nil || g == nil || g.World == nil || view.W <= 0 || view.H <= 0 {
		return
	}
	w := g.World
	vp := jlandViewportFor(view)
	T := vp.tile
	atlas := jlandAtlasFor(r)
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	hadClip := r.IsClipEnabled()
	prevClip := r.GetClipRect()
	clip := view
	_ = r.SetClipRect(&clip)

	daylight := jlandDaylight(g.Clock)
	jlandDrawSky(r, view, daylight, g.Clock, jlandRT.camX)

	tx0, ty0 := jlandFloorDiv(vp.camPX, T), jlandFloorDiv(vp.camPY, T)
	tx1, ty1 := jlandFloorDiv(vp.camPX+view.W, T), jlandFloorDiv(vp.camPY+view.H, T)
	tx0, ty0 = max(tx0, 0), max(ty0, 0)
	tx1, ty1 = min(tx1, int32(w.W-1)), min(ty1, int32(w.H-1))

	if atlas != nil {
		for ty := ty0; ty <= ty1; ty++ {
			for tx := tx0; tx <= tx1; tx++ {
				t := w.at(int(tx), int(ty))
				switch t {
				case jlandAir, jlandWater:
					if surface := int32(w.surfaceRow(int(tx))); ty > surface {
						back := jlandAtlasBackDirt
						if ty > surface+4 {
							back = jlandAtlasBackStone
						}
						jlandCopyCell(r, atlas, back, vp.cellX(tx), vp.cellY(ty), T)
					}
				default:
					jlandCopyCell(r, atlas, int(t), vp.cellX(tx), vp.cellY(ty), T)
				}
			}
		}
	}

	jlandDrawPlayer(r, g, vp)

	// Water goes over the player, so wading reads as being in the lake.
	if atlas != nil {
		for ty := ty0; ty <= ty1; ty++ {
			for tx := tx0; tx <= tx1; tx++ {
				if w.at(int(tx), int(ty)) != jlandWater {
					continue
				}
				cell := int(jlandWater)
				if w.at(int(tx), int(ty)-1) != jlandWater {
					cell = jlandAtlasWaterTop
				}
				jlandCopyCell(r, atlas, cell, vp.cellX(tx), vp.cellY(ty), T)
			}
		}
	}

	jlandDrawDarkness(r, g, vp, daylight, tx0, ty0, tx1, ty1)
	jlandDrawCursor(r, g, atlas, vp)
	jlandDrawParticles(r, vp)

	if hadClip {
		_ = r.SetClipRect(&prevClip)
	} else {
		_ = r.SetClipRect(nil)
	}
	jlandDrawHUD(r, config, g, atlas, view, hudBottom)
}

// jlandSkyColors is the sky's top and horizon colour for the light of day,
// with the horizon warming through dusk and dawn.
func jlandSkyColors(daylight float64) (sdl.Color, sdl.Color) {
	t := (daylight - 0.28) / 0.72
	top := jlandMix(jlandRGB(10, 16, 40), jlandRGB(66, 148, 228), t)
	bottom := jlandMix(jlandRGB(28, 42, 82), jlandRGB(178, 222, 250), t)
	if twilight := 1 - math.Abs(2*t-1); twilight > 0 {
		bottom = jlandMix(bottom, jlandRGB(250, 158, 96), twilight*0.55)
	}
	return top, bottom
}

// jlandDrawSky paints the gradient, the stars at night, the sun or the moon,
// a few drifting clouds and the far hills.
func jlandDrawSky(r *sdl.Renderer, view sdl.Rect, daylight, clock, camX float64) {
	top, bottom := jlandSkyColors(daylight)
	const bands = 14
	for i := int32(0); i < bands; i++ {
		y0 := view.Y + view.H*i/bands
		y1 := view.Y + view.H*(i+1)/bands
		jlandFill(r, view.X, y0, view.W, y1-y0, jlandMix(top, bottom, float64(i)/float64(bands-1)))
	}
	day := (daylight - 0.28) / 0.72
	if night := 1 - day; night > 0.05 {
		for i := 0; i < 48; i++ {
			sx := int32(jlandWrap(float64(jlandHash(i, 1, 7)%4096)-camX*2, float64(view.W)))
			sy := int32(jlandHash(i, 2, 7) % uint32(max(1, view.H*3/5)))
			a := uint8(math.Min(255, night*float64(120+jlandHash(i, 3, 7)%120)))
			jlandFill(r, view.X+sx, view.Y+sy, 2, 2, jlandRGBA(255, 255, 255, a))
		}
	}

	// The sun crosses during the day and the moon at night, on the same arc.
	phase := jlandWrap(clock, jlandDayLength) / jlandDayLength
	isSun := phase < 0.58
	progress := (phase - 0.58) / 0.42
	if isSun {
		progress = phase / 0.58
	}
	radius := max(10, view.H/18)
	cx := view.X + int32(float64(view.W)*(0.08+0.84*progress))
	cy := view.Y + int32(float64(view.H)*0.55) - int32(math.Sin(progress*math.Pi)*float64(view.H)*0.42)
	if isSun {
		homeGlowBlob(r, sdl.Rect{X: cx - radius*4, Y: cy - radius*4, W: radius * 8, H: radius * 8}, jlandRGB(255, 214, 120), 90)
		fillCircle(r, cx, cy, radius, jlandRGB(255, 236, 160))
	} else {
		homeGlowBlob(r, sdl.Rect{X: cx - radius*3, Y: cy - radius*3, W: radius * 6, H: radius * 6}, jlandRGB(170, 190, 255), 60)
		fillCircle(r, cx, cy, radius, jlandRGB(228, 234, 246))
		fillCircle(r, cx+radius/3, cy-radius/4, radius*3/4, jlandMix(top, bottom, 0.3))
	}

	if day > 0.05 {
		ink := jlandRGBA(255, 255, 255, uint8(205*math.Min(1, day)))
		span := float64(view.W + 360)
		for i := 0; i < 5; i++ {
			speed := 6 + float64(jlandHash(i, 4, 9)%8)
			base := float64(jlandHash(i, 5, 9) % 4096)
			x := view.X + int32(jlandWrap(base+clock*speed-camX*3, span)) - 180
			y := view.Y + 8 + int32(jlandHash(i, 6, 9)%uint32(max(1, view.H/3)))
			cw := int32(90 + jlandHash(i, 7, 9)%70)
			fillRoundedRect(r, x, y+cw/8, cw, cw/4, cw/8, ink)
			fillRoundedRect(r, x+cw/5, y, cw/2, cw/4, cw/8, ink)
		}
	}

	// Far hills: a soft silhouette between the sky and the ground.
	hill := jlandMix(bottom, jlandRGB(40, 92, 70), 0.45*day+0.25)
	for x := int32(0); x < view.W; x += 8 {
		wx := float64(x) + camX*12
		h := 0.36 + 0.12*math.Sin(wx*0.0062) + 0.07*math.Sin(wx*0.017+1.3)
		ridge := view.Y + int32(float64(view.H)*(1-h))
		jlandFill(r, view.X+x, ridge, 8, view.Y+view.H-ridge, hill)
	}
}

// jlandDrawPlayer draws the character in art pixels (11x27), mirrored when
// facing left, with a walk cycle and a swinging arm.
func jlandDrawPlayer(r *sdl.Renderer, g *jlandGame, vp jlandViewport) {
	p := g.P
	unit := float64(vp.tile) / jlandArt
	left := vp.worldX(p.X) - int32(math.Round(5.5*unit))
	top := vp.worldY(p.Y+p.Lift) - int32(math.Round(27*unit))
	px := func(ax, ay, aw, ah float64, c sdl.Color) {
		x := ax
		if p.Facing < 0 {
			x = 11 - ax - aw
		}
		jlandFill(r, left+int32(math.Round(x*unit)), top+int32(math.Round(ay*unit)),
			int32(math.Ceil(aw*unit)), int32(math.Ceil(ah*unit)), c)
	}
	skin := jlandRGB(240, 196, 160)
	hair := jlandRGB(88, 56, 32)
	shirt := jlandRGB(48, 164, 216)
	pants := jlandRGB(52, 64, 98)
	shoe := jlandRGB(36, 36, 44)

	stride := 0.0
	if p.OnGround && math.Abs(p.VX) > 0.3 {
		stride = math.Sin(p.WalkPhase * 3.2)
	} else if !p.OnGround {
		stride = 0.8
	}
	legA, legB := math.Round(stride*1.4), math.Round(-stride*1.4)

	px(2, 17, 3, 9, pants)
	px(6, 17, 3, 9, pants)
	px(2+legA, 25, 3, 2, shoe)
	px(6+legB, 25, 3, 2, shoe)
	px(2, 8, 7, 9, shirt)
	px(4, 8, 3, 1, jlandShade(shirt, 1.2))
	px(2, 16, 7, 1, jlandRGB(40, 44, 56))
	px(2, 1, 7, 7, skin)
	px(2, 0, 7, 2, hair)
	px(2, 2, 2, 4, hair)
	px(7, 4, 1, 2, jlandRGB(30, 30, 40))
	swing := math.Round(stride * 1.5)
	px(5+swing, 9, 2, 5, jlandShade(shirt, 0.82))
	px(5+swing, 14, 2, 2, skin)
}

// jlandDrawDarkness shades caves, deep ground and night. Open sky is left to
// the sky's own colours; torches and the player's glow push the dark back.
func jlandDrawDarkness(r *sdl.Renderer, g *jlandGame, vp jlandViewport, daylight float64, tx0, ty0, tx1, ty1 int32) {
	rt := &jlandRT
	w := g.World
	T := vp.tile
	rt.lights = rt.lights[:0]
	for ty := ty0 - 8; ty <= ty1+8; ty++ {
		for tx := tx0 - 8; tx <= tx1+8; tx++ {
			if w.at(int(tx), int(ty)) == jlandTorch && len(rt.lights) < 96 {
				rt.lights = append(rt.lights, jlandLight{X: float64(tx) + 0.5, Y: float64(ty) + 0.5,
					Radius: jlandDef(jlandTorch).Light, Power: 0.95})
			}
		}
	}
	torches := len(rt.lights)
	rt.lights = append(rt.lights, jlandLight{X: g.P.X, Y: g.P.Y - jlandPlayerH/2, Radius: 3.4, Power: 0.62})

	shade := jlandRGBA(4, 6, 16, 0)
	for ty := ty0; ty <= ty1; ty++ {
		runStart, runLevel := tx0, -1
		for tx := tx0; tx <= tx1+1; tx++ {
			level := -1
			if tx <= tx1 {
				level = 0
				open := w.at(int(tx), int(ty)) == jlandAir && int(ty) < w.skyRow(int(tx))
				if !open {
					light := g.lightAt(int(tx), int(ty), daylight, rt.lights)
					level = int(math.Round((1 - light) * 15))
				}
			}
			if level == runLevel {
				continue
			}
			if runLevel > 0 {
				shade.A = uint8(runLevel * 15)
				jlandFill(r, vp.cellX(runStart), vp.cellY(ty), (tx-runStart)*T, T, shade)
			}
			runStart, runLevel = tx, level
		}
	}

	if daylight < 0.95 || torches > 0 {
		glow := uint8(55 + 70*(1-daylight))
		size := T * 5
		for _, l := range rt.lights[:torches] {
			x, y := vp.worldX(l.X), vp.worldY(l.Y)
			homeGlowBlob(r, sdl.Rect{X: x - size/2, Y: y - size/2, W: size, H: size}, jlandRGB(255, 186, 104), glow)
		}
	}
}

// jlandDrawCursor outlines the cell the next press acts on: a ghost of the
// block when it can be placed there, a light wash when it can be mined, red
// when neither is possible (out of reach, inside the player, bedrock).
func jlandDrawCursor(r *sdl.Renderer, g *jlandGame, atlas *sdl.Texture, vp jlandViewport) {
	T := vp.tile
	tx, ty := g.target()
	x, y := vp.cellX(int32(tx)), vp.cellY(int32(ty))
	placeable := g.canPlace(tx, ty, g.selected()) == jlandOK
	mineable := g.canBreak(tx, ty) == jlandOK
	if placeable && atlas != nil {
		_ = atlas.SetAlphaMod(110)
		jlandCopyCell(r, atlas, int(g.selected()), x, y, T)
		_ = atlas.SetAlphaMod(255)
	} else if mineable {
		jlandFill(r, x, y, T, T, jlandRGBA(255, 255, 255, 46))
	}
	ink := jlandRGBA(255, 255, 255, 235)
	if (!placeable && !mineable) || sdl.GetTicks64() < jlandRT.invalidUntil {
		ink = jlandRGBA(240, 120, 138, 230)
	}
	dark := jlandRGBA(10, 12, 20, 150)
	jlandFill(r, x-1, y-1, T+2, 1, dark)
	jlandFill(r, x-1, y+T, T+2, 1, dark)
	jlandFill(r, x-1, y, 1, T, dark)
	jlandFill(r, x+T, y, 1, T, dark)
	jlandFill(r, x, y, T, 2, ink)
	jlandFill(r, x, y+T-2, T, 2, ink)
	jlandFill(r, x, y+2, 2, T-4, ink)
	jlandFill(r, x+T-2, y+2, 2, T-4, ink)
}

// jlandParticle is one chip of a broken block.
type jlandParticle struct {
	X, Y   float64
	VX, VY float64
	Life   float64
	Color  sdl.Color
}

// jlandBurst throws a handful of chips from a broken cell.
func jlandBurst(tx, ty int, t jlandTile) {
	rt := &jlandRT
	col := jlandTileColor(t)
	for i := 0; i < 10; i++ {
		h := jlandHash(tx+i, ty, rt.burstSeed)
		p := &rt.parts[rt.partNext]
		rt.partNext = (rt.partNext + 1) % len(rt.parts)
		*p = jlandParticle{
			X:     float64(tx) + 0.2 + float64(h%60)/100,
			Y:     float64(ty) + 0.2 + float64((h/60)%60)/100,
			VX:    (float64(h%200)/100 - 1) * 4,
			VY:    -3 - float64((h/200)%300)/100,
			Life:  0.6 + float64(h%40)/100,
			Color: jlandShade(col, 0.8+float64(h%40)/100),
		}
	}
	rt.burstSeed++
}

// jlandStepParticles moves the chips (called from the tick, not the draw).
func jlandStepParticles(dt float64) {
	rt := &jlandRT
	for i := range rt.parts {
		p := &rt.parts[i]
		if p.Life <= 0 {
			continue
		}
		p.Life -= dt
		p.VY += jlandGravity * 0.6 * dt
		p.X += p.VX * dt
		p.Y += p.VY * dt
	}
}

func jlandDrawParticles(r *sdl.Renderer, vp jlandViewport) {
	rt := &jlandRT
	size := max(2, vp.tile/8)
	for i := range rt.parts {
		p := &rt.parts[i]
		if p.Life <= 0 {
			continue
		}
		c := p.Color
		c.A = uint8(math.Min(1, p.Life*2) * 255)
		jlandFill(r, vp.worldX(p.X), vp.worldY(p.Y), size, size, c)
	}
}

// ── HUD ──────────────────────────────────────────────────────────────────

// jlandHotbarLayout places the hotbar at the bottom centre of the view and
// returns the slot size, the gap and the bar's rect.
func jlandHotbarLayout(view sdl.Rect, hudBottom int32) (int32, int32, sdl.Rect) {
	n := int32(len(jlandHotbar))
	gap := int32(6)
	pad := int32(8)
	tile := jlandRT.tile
	if tile <= 0 {
		tile = jlandTileSize(view)
	}
	slot := min(int32(56), max(int32(32), tile+tile/3))
	if fit := (view.W - 2*pad - (n-1)*gap - 24) / n; slot > fit {
		slot = max(int32(20), fit)
	}
	barW := n*slot + (n-1)*gap + 2*pad
	barH := slot + 2*pad
	return slot, gap, sdl.Rect{X: view.X + (view.W-barW)/2, Y: hudBottom - barH, W: barW, H: barH}
}

// jlandSlotRect is slot i of the hotbar.
func jlandSlotRect(bar sdl.Rect, slot, gap int32, i int) sdl.Rect {
	return sdl.Rect{X: bar.X + 8 + int32(i)*(slot+gap), Y: bar.Y + 8, W: slot, H: slot}
}

// jlandDrawHUD draws the hotbar, the selected block's name for a moment after
// it changes, the ore tally and (for a mouse or a finger) the Mine / Build
// switch.
func jlandDrawHUD(r *sdl.Renderer, config *Config, g *jlandGame, atlas *sdl.Texture, view sdl.Rect, hudBottom int32) {
	rt := &jlandRT
	if config == nil {
		config = appConfig
	}
	slot, gap, bar := jlandHotbarLayout(view, hudBottom)
	rt.hotbar = bar
	rt.hotbarSlot, rt.hotbarGap = slot, gap
	fillRoundedRect(r, bar.X, bar.Y, bar.W, bar.H, 16, WithAlpha(ColorBackground, 178))
	strokeRoundedRect(r, bar.X, bar.Y, bar.W, bar.H, 16, 1, glassEdge())
	for i, t := range jlandHotbar {
		s := jlandSlotRect(bar, slot, gap, i)
		if i == g.Slot {
			lift := glassLifted(s)
			fillRoundedRect(r, lift.X, lift.Y, lift.W, lift.H, 12, glassA(34))
			strokeRoundedRect(r, lift.X, lift.Y, lift.W, lift.H, 12, glassFocusRingWidth, glassAccent())
		} else {
			fillRoundedRect(r, s.X, s.Y, s.W, s.H, 10, glassA(14))
		}
		inset := max(int32(4), slot/7)
		size := slot - 2*inset
		if atlas != nil {
			jlandCopyCell(r, atlas, int(t), s.X+inset, s.Y+inset, size)
		} else {
			jlandFill(r, s.X+inset, s.Y+inset, size, size, jlandTileColor(t))
		}
	}
	if config == nil {
		return
	}
	if small := getFontSized(config, "small", 13); small != nil && slot >= 32 {
		for i := range jlandHotbar {
			s := jlandSlotRect(bar, slot, gap, i)
			renderText(r, config, small, jlandSlotKeys[i], WithAlpha(ColorTextPrimary(), 210), s.X+4, s.Y+1)
		}
	}

	// The block's name, for a moment after the selection changes.
	now := sdl.GetTicks64()
	if age := now - rt.slotShownAt; rt.slotShownAt != 0 && age < 1600 {
		if font := getFontSized(config, "small", 18); font != nil {
			name := jlandDef(g.selected()).Name
			tw, th := measureText(font, name)
			alpha := 1.0
			if age > 1200 {
				alpha = float64(1600-age) / 400
			}
			pill := sdl.Rect{X: bar.X + (bar.W-tw)/2 - 14, Y: bar.Y - th - 18, W: tw + 28, H: th + 10}
			fillRoundedRect(r, pill.X, pill.Y, pill.W, pill.H, pill.H/2, WithAlpha(ColorBackground, uint8(190*alpha)))
			renderText(r, config, font, name, WithAlpha(ColorTextPrimary(), uint8(255*alpha)), pill.X+14, pill.Y+5)
		}
	}

	// Ore found so far, top left; nothing until the first one.
	if font := getFontSized(config, "small", 16); font != nil {
		x, y := view.X+12, view.Y+12
		for i, count := range g.Stats.Ores {
			if count <= 0 {
				continue
			}
			if rt.oreCounts[i] != count || rt.oreLabels[i] == "" {
				rt.oreCounts[i] = count
				rt.oreLabels[i] = strconv.Itoa(count)
			}
			tw, th := measureText(font, rt.oreLabels[i])
			icon := max(int32(18), th)
			chip := sdl.Rect{X: x, Y: y, W: icon + tw + 22, H: icon + 10}
			fillRoundedRect(r, chip.X, chip.Y, chip.W, chip.H, chip.H/2, WithAlpha(ColorBackground, 178))
			if atlas != nil {
				jlandCopyCell(r, atlas, int(jlandCoal)+i, chip.X+7, chip.Y+5, icon)
			}
			renderText(r, config, font, rt.oreLabels[i], ColorTextPrimary(), chip.X+icon+13, chip.Y+(chip.H-th)/2)
			x += chip.W + 8
		}
	}

	// The Mine / Build switch, while a pointer is in use: it decides what a
	// left click (or a tap) does. A right click always builds.
	rt.toolMine, rt.toolBuild = sdl.Rect{}, sdl.Rect{}
	if !rt.pointerActive(now) {
		return
	}
	font := getFontSized(config, "small", 16)
	if font == nil {
		return
	}
	mineW, th := measureText(font, "Mine")
	buildW, _ := measureText(font, "Build")
	h := th + 14
	total := mineW + buildW + 52
	x := bar.X + bar.W + 12
	if x+total > view.X+view.W-8 {
		x = view.X + view.W - total - 8
	}
	y := bar.Y + (bar.H-h)/2
	if x < bar.X+bar.W {
		y = bar.Y - h - 12
	}
	fillRoundedRect(r, x, y, total, h, h/2, WithAlpha(ColorBackground, 178))
	strokeRoundedRect(r, x, y, total, h, h/2, 1, glassEdge())
	rt.toolMine = sdl.Rect{X: x + 4, Y: y + 3, W: mineW + 22, H: h - 6}
	rt.toolBuild = sdl.Rect{X: rt.toolMine.X + rt.toolMine.W + 4, Y: y + 3, W: buildW + 22, H: h - 6}
	for i, seg := range [2]sdl.Rect{rt.toolMine, rt.toolBuild} {
		label := "Mine"
		if i == 1 {
			label = "Build"
		}
		ink := ColorTextSecondary()
		if (i == 1) == rt.buildMode {
			fillRoundedRect(r, seg.X, seg.Y, seg.W, seg.H, seg.H/2, glassAccent())
			ink = contrastText(glassAccent())
		}
		renderText(r, config, font, label, ink, seg.X+11, seg.Y+(seg.H-th)/2)
	}
}
