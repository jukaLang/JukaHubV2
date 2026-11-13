package main

// glass_real.go — the "Realistic" glass style (Settings → Display & theme →
// Glass). Classic, the default, is ui_glass.go's recipe: a translucent fill
// and a hairline. Realistic draws the same surfaces the way Apple's Liquid
// Glass reads: the backdrop seen through the pane is frosted (blurred and a
// little magnified, like a lens), the pane is lit — a sheen fading down from
// its top and a bright specular rim strongest where the light from the top
// left meets the edge, with a weaker catch-light on the opposite corner — and
// a Home card stands off the backdrop on a soft shadow.
//
// Every glass surface goes through the hooks at the top of drawGlassTile
// (home_design.go), drawGlassSurface, drawGlassSheet and drawGlassBadge
// (ui_glass.go). Each returns false when the style is off, Reduce
// Transparency or High Contrast is on, or the renderer cannot draw geometry,
// and the caller draws Classic.
//
// Cost on the Smart Pro's two cores and PowerVR GPU:
//   - The frost is a 1/16-size copy of the backdrop's still layers (base,
//     wallpaper or the GIF's current frame, the two blooms) made with render
//     targets and linear filtering, rebuilt only when what it shows changes:
//     a theme, a wallpaper, the screen size, or a GIF's frame at most four
//     times a second. drawAppBackdrop itself is never called for it (its
//     water, ambient and living layers keep state).
//   - A surface is two SDL_RenderGeometry calls (the frost through the
//     pane; the tint, sheen and rim in one mesh) plus the cached hairline.
//     The meshes are built once per (w, h, radius) and positioned at draw
//     time into reused slices, so a frame allocates nothing.
//   - The frost is only drawn where the pane sits on the backdrop: after a
//     full backdrop this frame and before any overlay scrim or sheet. A
//     button on an opaque sheet, a control over a video or a game, gets the
//     lit glass without the frost, so the backdrop never shows through
//     something that should hide it.

import (
	"math"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	glassStyleKey       = "glassStyle"
	glassStyleClassic   = "classic"
	glassStyleRealistic = "realistic"
)

// glassStyleOptions are the Glass row's stops.
var glassStyleOptions = []setOption{
	{Value: glassStyleClassic, Label: "Classic", Note: "Clear frosted panes with a hairline edge"},
	{Value: glassStyleRealistic, Label: "Realistic", Note: "Blurred, lit glass with a bright rim, like Apple's Liquid Glass"},
}

// glassStyleOf reads the stored style; anything but "realistic" is Classic.
func glassStyleOf(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return glassStyleClassic
	}
	if s, ok := c.Variables.Custom[glassStyleKey].(string); ok &&
		strings.EqualFold(strings.TrimSpace(s), glassStyleRealistic) {
		return glassStyleRealistic
	}
	return glassStyleClassic
}

// glassStyleRow is the Glass choice row in Display & theme (setmodel.go).
func glassStyleRow() setRow {
	return setRow{
		ID: glassStyleKey, Label: "Glass", Kind: setKindChoice,
		Detail: func(*Config) string {
			switch {
			case a11y.reduceTransparency:
				return "Off while Reduce Transparency is on"
			case a11y.highContrast:
				return "Off while High contrast is on"
			case glassReal.failed:
				return "Not supported by this renderer"
			}
			return "Classic panes, or realistic glass that blurs and catches the light"
		},
		Disabled: func(*Config) bool { return a11y.reduceTransparency || a11y.highContrast },
		Options:  func(*Config) []setOption { return glassStyleOptions },
		Current: func(c *Config) int {
			if glassStyleOf(c) == glassStyleRealistic {
				return 1
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			v := glassStyleClassic
			label := "Classic"
			if strings.EqualFold(value, glassStyleRealistic) {
				v, label = glassStyleRealistic, "Realistic"
			}
			setStoreString(c, glassStyleKey, v)
			glassReal.read = false
			setToast("Glass: " + label)
		},
	}
}

// glassMeshKey is the size a mesh was built for.
type glassMeshKey struct{ w, h, r int32 }

// glassMesh is one rounded pane in local coordinates (0,0 at its top left).
// Vertices are laid out [fill strips][rim and thickness rings][shadow rings]:
// the strips are two vertices a row, top to bottom; each ring is one contour
// of m points, clockwise from the top-left corner, at a growing depth.
//
// weight is per vertex: the sheen (0..1) on a strip vertex, the light the
// edge catches on a ring vertex, the shadow's strength on a shadow vertex.
// overIdx is the strips and then the rings, so one call draws both; the
// first shadow vertex is nFill+nRing.
type glassMesh struct {
	px, py    []float32
	weight    []float32
	nFill     int
	nRing     int
	fillIdx   []int32
	overIdx   []int32
	shadowIdx []int32
	shadowDy  float32
}

// glassFrostKey is everything the frost shows; a change rebuilds it.
type glassFrostKey struct {
	w, h           int32
	base           sdl.Color
	bloom1, bloom2 sdl.Color
	wall           *sdl.Texture
	wallPath       string // a new wallpaper can reuse the old texture's address
	gifTick        uint64
	gifFrames      int
	gif            bool
	dynamic        bool
}

// glassFrostLevels is the downsampling chain: screen/2, /4, /8, /16.
const glassFrostLevels = 4

var glassReal struct {
	// The setting, read once a frame (frameCount) or when the row writes it.
	read  bool
	frame uint64
	on    bool

	// failed: the renderer refused geometry once; Classic for the session.
	failed bool
	warned bool

	// What lies under the next pane: a full backdrop this frame, and no
	// overlay drawn over it since.
	backdropFrame uint64
	backdropSeen  bool
	covered       bool

	// The frost chain, on the renderer it was made on.
	renderer    *sdl.Renderer
	levels      [glassFrostLevels]*sdl.Texture
	levelW      [glassFrostLevels]int32
	levelH      [glassFrostLevels]int32
	frostKey    glassFrostKey
	frostValid  bool
	frostFailed bool
	keyRead     bool
	keyFrame    uint64

	meshes map[glassMeshKey]*glassMesh
	verts  []sdl.Vertex
	fverts []sdl.Vertex
}

// glassRealOn reports whether the realistic style draws this frame.
func glassRealOn() bool {
	if glassReal.failed || a11y.reduceTransparency || a11y.highContrast {
		return false
	}
	if !glassReal.read || glassReal.frame != frameCount {
		glassReal.read, glassReal.frame = true, frameCount
		glassReal.on = glassStyleOf(appConfig) == glassStyleRealistic
	}
	return glassReal.on
}

// glassRealNoteBackdrop is drawAppBackdrop's hook: a full (unclipped) backdrop
// means the panes drawn next sit on it. The header band's clipped repaint
// does not count.
func glassRealNoteBackdrop(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	if !glassRealOn() {
		for _, t := range glassReal.levels {
			if t != nil {
				glassFrostRelease(renderer)
				break
			}
		}
		return
	}
	if renderer.IsClipEnabled() {
		return
	}
	glassReal.backdropFrame, glassReal.backdropSeen, glassReal.covered = frameCount, true, false
}

// glassRealCover marks the backdrop hidden for the rest of the frame: an
// overlay's scrim or an opaque sheet was drawn, so a pane drawn after it must
// not show the backdrop through.
func glassRealCover() {
	glassReal.covered = true
}

// glassRealOverBackdrop reports whether the next pane sits on the backdrop.
func glassRealOverBackdrop() bool {
	return glassReal.backdropSeen && glassReal.backdropFrame == frameCount && !glassReal.covered
}

// glassRealFail falls back to Classic for the session.
func glassRealFail(err error) {
	glassReal.failed = true
	if !glassReal.warned {
		glassReal.warned = true
		logWarn("glass", "the renderer cannot draw realistic glass (%v); using Classic", err)
	}
}

// ── Looks ────────────────────────────────────────────────────────────────

// glassLook is how strongly one pane is lit.
type glassLook struct {
	tint   sdl.Color // the fill under the sheen
	sheen  float32   // white alpha at the very top
	rim    float32   // peak alpha of the specular rim
	frost  uint8     // the frosted backdrop's alpha; 0 = none
	shadow float32   // a Home card's drop shadow alpha; 0 = none
}

// glassLookFor scales a dark-palette look for the active palette: on a light
// one the white highlights need more weight to read, and the shadow less.
func glassLookFor(tint sdl.Color, sheen, rim float32, frost uint8, shadow float32) glassLook {
	if glassLightTheme() {
		sheen *= 2.2
		rim = rim*1.35 + 20
		shadow *= 0.6
	}
	if rim > 255 {
		rim = 255
	}
	if sheen > 255 {
		sheen = 255
	}
	return glassLook{tint: tint, sheen: sheen, rim: rim, frost: frost, shadow: shadow}
}

// glassScaleAlpha is c with its alpha scaled by k.
func glassScaleAlpha(c sdl.Color, k float32) sdl.Color {
	a := float32(c.A) * k
	if a > 255 {
		a = 255
	}
	c.A = uint8(a + 0.5)
	return c
}

// ── Hooks ────────────────────────────────────────────────────────────────

// glassRealTile is drawGlassTile in the realistic style: base is the settled
// rect the mesh is keyed to, rect the lifted one it is drawn at.
func glassRealTile(renderer *sdl.Renderer, base, rect sdl.Rect, focused, pressed bool) bool {
	if !glassRealOn() || base.W <= 0 || base.H <= 0 || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	fill, edge := homeCardFill(), homeCardEdge()
	sheen, rim, frost, shadow := float32(30), float32(150), uint8(228), float32(70)
	if focused {
		fill, edge = homeCardFillFocus(), homeCardEdgeFocus()
		// The focus bloom behind the card shows through the glass.
		sheen, rim, frost, shadow = 40, 180, 150, 0
	}
	if pressed {
		fill = homeGlass(32)
		sheen, rim = 22, 120
	}
	if !glassRealOverBackdrop() {
		frost = 0
	}
	look := glassLookFor(fill, sheen, rim, frost, shadow)
	if !glassRealDraw(renderer, base, rect, homeTileRadius, look) {
		return false
	}
	strokeRoundedRectAt(renderer, base, rect, homeTileRadius, 1, glassScaleAlpha(edge, 0.7))
	if focused {
		drawHomeFocusRing(renderer, base, rect, homeTileRadius)
	}
	return true
}

// glassRealSurface is drawGlassSurface in the realistic style.
func glassRealSurface(renderer *sdl.Renderer, rect sdl.Rect, radius int32, st glassState) bool {
	if !glassRealOn() {
		return false
	}
	fill, edge := glassFill(), glassEdge()
	sheen, rim, frost := float32(26), float32(125), uint8(215)
	switch {
	case st.Disabled:
		fill, edge = glassA(8), glassA(14)
		sheen, rim = 12, 60
	case st.Pressed:
		fill = glassFillPressed()
		sheen, rim = 18, 100
	case st.Focused:
		fill = glassFillFocus()
		sheen, rim, frost = 34, 160, 165
	case st.Hovered:
		fill, edge = glassFillHover(), glassEdgeHover()
		sheen, rim = 32, 150
	}
	// Pills and chips (header-sized, glassButtonRadius) are the controls
	// that sit on pictures — a badge on a thumbnail, a button on a cover —
	// where the frost would show the backdrop instead of the picture under
	// them. They keep the sheen and the rim only.
	if !glassRealOverBackdrop() || rect.H <= 40 {
		frost = 0
	}
	look := glassLookFor(fill, sheen, rim, frost, 0)
	if st.Focused && !st.Disabled {
		draw := rect
		if !st.NoLift {
			draw = glassLifted(rect)
			radius += glassFocusLift
		}
		if !glassRealDraw(renderer, draw, draw, radius, look) {
			return false
		}
		drawGlassFocusRing(renderer, draw, radius)
		return true
	}
	if !glassRealDraw(renderer, rect, rect, radius, look) {
		return false
	}
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, glassScaleAlpha(edge, 0.75))
	return true
}

// glassRealSheet is drawGlassSheet in the realistic style: the card stays
// opaque (its fill is the tint) and gets only the sheen and the rim. It hides
// the backdrop, so no pane drawn on it is frosted.
func glassRealSheet(renderer *sdl.Renderer, rect sdl.Rect, fill sdl.Color) bool {
	if !glassRealOn() {
		return false
	}
	glassRealCover()
	look := glassLookFor(fill, 16, 95, 0, 0)
	// A card fading in passes a translucent fill: its light fades with it, or
	// a white sheen and rim would show before the card does.
	k := float32(fill.A) / 255
	look.sheen *= k
	look.rim *= k
	if !glassRealDraw(renderer, rect, rect, OverlayCardRadius, look) {
		return false
	}
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, OverlayCardRadius, 1, glassScaleAlpha(glassSheetEdge(), 0.7*k))
	return true
}

// glassRealBadge is drawGlassBadge's square in the realistic style: glass on
// glass, so lit but never frosted.
func glassRealBadge(renderer *sdl.Renderer, rect sdl.Rect, radius int32) bool {
	if !glassRealOn() {
		return false
	}
	look := glassLookFor(glassA(20), 26, 110, 0, 0)
	if !glassRealDraw(renderer, rect, rect, radius, look) {
		return false
	}
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, glassScaleAlpha(glassA(16), 0.7))
	return true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// glassLightDir is where the light comes from: above, a little to the left.
var glassLightDirX, glassLightDirY = float32(-0.6), float32(-0.8)

// glassRealDraw draws one pane at dst with base's shape: the shadow, the
// frost, then the tint, sheen and rim in one mesh. false means nothing more
// should be drawn this way (the caller falls back to Classic).
func glassRealDraw(renderer *sdl.Renderer, base, dst sdl.Rect, radius int32, look glassLook) bool {
	if renderer == nil || base.W <= 0 || base.H <= 0 || dst.W <= 0 || dst.H <= 0 {
		return false
	}
	m := glassMeshFor(base.W, base.H, radius)
	if m == nil || len(m.overIdx) == 0 {
		return false
	}
	sx := float32(dst.W) / float32(base.W)
	sy := float32(dst.H) / float32(base.H)
	ox, oy := float32(dst.X), float32(dst.Y)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	n := len(m.px)
	if cap(glassReal.verts) < n {
		glassReal.verts = make([]sdl.Vertex, n, n+n/2)
	}
	verts := glassReal.verts[:n]
	highlight := sdl.Color{R: 255, G: 255, B: 255, A: 255}

	// The pane: tint under a sheen that fades down from the top.
	for i := 0; i < m.nFill; i++ {
		verts[i] = sdl.Vertex{
			Position: sdl.FPoint{X: ox + m.px[i]*sx, Y: oy + m.py[i]*sy},
			Color:    glassOver(look.tint, highlight, look.sheen*m.weight[i]),
		}
	}
	// The rim and the glass's thickness, lit by the edge's angle to the light.
	for i := m.nFill; i < m.nFill+m.nRing; i++ {
		c := highlight
		c.A = glassAlpha(look.rim * m.weight[i])
		verts[i] = sdl.Vertex{Position: sdl.FPoint{X: ox + m.px[i]*sx, Y: oy + m.py[i]*sy}, Color: c}
	}
	// The shadow: dark, a little below the pane, fading outwards.
	shade := sdl.Color{R: 0, G: 0, B: 0, A: 255}
	if glassLightTheme() {
		shade = sdl.Color{R: 15, G: 23, B: 42, A: 255}
	}
	for i := m.nFill + m.nRing; i < n; i++ {
		c := shade
		c.A = glassAlpha(look.shadow * m.weight[i])
		verts[i] = sdl.Vertex{Position: sdl.FPoint{X: ox + m.px[i]*sx, Y: oy + (m.py[i]+m.shadowDy)*sy}, Color: c}
	}

	var frost *sdl.Texture
	if look.frost > 0 {
		frost = glassFrostTexture(renderer)
	}
	// The shadow ring is hollow and nudged down, so its inner band lies under
	// the pane's top edge: only a frosted (near-opaque) pane hides it. Over a
	// translucent pane it would read as a dark line inside the card.
	if frost != nil && look.shadow > 0 && len(m.shadowIdx) > 0 {
		if err := renderer.RenderGeometry(nil, verts, m.shadowIdx); err != nil {
			glassRealFail(err)
			return false
		}
	}
	if frost != nil {
		glassDrawFrost(renderer, frost, m, ox, oy, sx, sy, float32(dst.W), float32(dst.H), look.frost)
	}
	if err := renderer.RenderGeometry(nil, verts, m.overIdx); err != nil {
		glassRealFail(err)
		return false
	}
	return true
}

// glassDrawFrost draws the frosted backdrop through the pane's strips. The
// texture coordinates are the screen position, pulled 6% towards the pane's
// centre: the glass magnifies what is behind it a little, so its edge reads
// as a lens instead of a cut-out. A failure turns only the frost off: the
// pane is still drawn lit.
func glassDrawFrost(renderer *sdl.Renderer, tex *sdl.Texture, m *glassMesh, ox, oy, sx, sy, w, h float32, alpha uint8) {
	sw, sh := float32(screenWidth), float32(screenHeight)
	if sw <= 0 || sh <= 0 {
		return
	}
	const lens = 0.94
	cx, cy := ox+w/2, oy+h/2
	if cap(glassReal.fverts) < m.nFill {
		glassReal.fverts = make([]sdl.Vertex, m.nFill, m.nFill+m.nFill/2)
	}
	fv := glassReal.fverts[:m.nFill]
	col := sdl.Color{R: 255, G: 255, B: 255, A: alpha}
	for i := 0; i < m.nFill; i++ {
		x, y := ox+m.px[i]*sx, oy+m.py[i]*sy
		fv[i] = sdl.Vertex{
			Position: sdl.FPoint{X: x, Y: y},
			Color:    col,
			// Clamped: a pane past the screen's edge (a carousel card) must
			// not hand the software renderer coordinates outside the texture.
			TexCoord: sdl.FPoint{X: glassUnit((cx + (x-cx)*lens) / sw), Y: glassUnit((cy + (y-cy)*lens) / sh)},
		}
	}
	if err := renderer.RenderGeometry(tex, fv, m.fillIdx); err != nil {
		glassReal.frostFailed = true
		glassFrostRelease(renderer)
		if !glassReal.warned {
			glassReal.warned = true
			logWarn("glass", "the renderer cannot draw the frosted backdrop (%v); realistic glass without blur", err)
		}
	}
}

// glassUnit clamps v to 0..1.
func glassUnit(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// glassAlpha is a 0..255 float as a byte.
func glassAlpha(a float32) uint8 {
	if a <= 0 {
		return 0
	}
	if a >= 255 {
		return 255
	}
	return uint8(a + 0.5)
}

// glassOver is over (at overA, 0..255) composited onto under, as one colour:
// what drawing under and then over would leave on the screen.
func glassOver(under, over sdl.Color, overA float32) sdl.Color {
	ao := overA / 255
	if ao <= 0 {
		return under
	}
	if ao > 1 {
		ao = 1
	}
	au := float32(under.A) / 255
	a := ao + au*(1-ao)
	if a <= 0 {
		return sdl.Color{}
	}
	mix := func(u, o uint8) uint8 {
		return glassAlpha((float32(o)*ao + float32(u)*au*(1-ao)) / a)
	}
	return sdl.Color{R: mix(under.R, over.R), G: mix(under.G, over.G), B: mix(under.B, over.B), A: glassAlpha(a * 255)}
}

// ── Meshes ───────────────────────────────────────────────────────────────

// glassMeshFor returns the cached mesh for a w×h pane with corner radius r.
func glassMeshFor(w, h, r int32) *glassMesh {
	key := glassMeshKey{w: w, h: h, r: r}
	if m, ok := glassReal.meshes[key]; ok {
		return m
	}
	if glassReal.meshes == nil || len(glassReal.meshes) >= 160 {
		// A sheet that animates its size would add one per frame; the cache
		// starts over instead of growing.
		glassReal.meshes = make(map[glassMeshKey]*glassMesh, 64)
	}
	m := glassBuildMesh(w, h, r)
	glassReal.meshes[key] = m
	return m
}

// glassRowInset is how far the pane's edge is inset at height y.
func glassRowInset(y, h, r float32) float32 {
	var d float32
	switch {
	case y < r:
		d = r - y
	case y > h-r:
		d = y - (h - r)
	default:
		return 0
	}
	if d > r {
		d = r
	}
	return r - float32(math.Sqrt(float64(r*r-d*d)))
}

// glassBuildMesh builds a pane: horizontal strips (with extra rows in the
// corners so they are round, and in the sheen so it fades smoothly), the rim
// and thickness rings, and the shadow rings. Built once per size.
func glassBuildMesh(w, h, r int32) *glassMesh {
	fw, fh := float32(w), float32(h)
	rr := float32(r)
	if rr > fw/2 {
		rr = fw / 2
	}
	if rr > fh/2 {
		rr = fh / 2
	}
	if rr < 0 {
		rr = 0
	}
	n := int(math.Round(float64(rr) / 2.5))
	if n < 2 {
		n = 2
	}
	if n > 10 {
		n = 10
	}
	m := &glassMesh{}

	// Strips.
	sheenEnd := fh * 0.45
	if sheenEnd > 110 {
		sheenEnd = 110
	}
	ys := make([]float32, 0, 2*n+8)
	for i := 0; i <= n; i++ {
		a := float64(i) / float64(n) * math.Pi / 2
		ys = append(ys, rr-rr*float32(math.Cos(a)), fh-rr+rr*float32(math.Sin(a)))
	}
	ys = append(ys, 0, fh, sheenEnd*0.2, sheenEnd*0.45, sheenEnd*0.7, sheenEnd)
	sort.Slice(ys, func(i, j int) bool { return ys[i] < ys[j] })
	rows := ys[:0]
	for _, y := range ys {
		if y < 0 || y > fh {
			continue
		}
		if len(rows) > 0 && y-rows[len(rows)-1] < 0.25 {
			continue
		}
		rows = append(rows, y)
	}
	for _, y := range rows {
		in := glassRowInset(y, fh, rr)
		s := float32(0)
		if y < sheenEnd && sheenEnd > 0 {
			t := 1 - y/sheenEnd
			s = t * t
		}
		m.px = append(m.px, in, fw-in)
		m.py = append(m.py, y, y)
		m.weight = append(m.weight, s, s)
	}
	m.nFill = len(m.px)
	for k := 0; k+1 < len(rows); k++ {
		a := int32(2 * k)
		m.fillIdx = append(m.fillIdx, a, a+1, a+3, a, a+3, a+2)
	}

	// The contour: four corner arcs of n+1 points, clockwise from the top
	// left, each point with its outward normal.
	pts := 4 * (n + 1)
	cxs := [4]float32{rr, fw - rr, fw - rr, rr}
	cys := [4]float32{rr, rr, fh - rr, fh - rr}
	inx := [4]float32{1, -1, -1, 1}
	iny := [4]float32{1, 1, -1, -1}
	nx := make([]float32, 0, pts)
	ny := make([]float32, 0, pts)
	corner := make([]int, 0, pts)
	for c := 0; c < 4; c++ {
		for i := 0; i <= n; i++ {
			a := math.Pi + float64(c)*math.Pi/2 + float64(i)/float64(n)*math.Pi/2
			nx = append(nx, float32(math.Cos(a)))
			ny = append(ny, float32(math.Sin(a)))
			corner = append(corner, c)
		}
	}
	at := func(j int, d float32) (float32, float32) {
		c := corner[j]
		ri := rr - d
		s := float32(0)
		if ri < 0 {
			s, ri = -ri, 0
		}
		return cxs[c] + inx[c]*s + ri*nx[j], cys[c] + iny[c]*s + ri*ny[j]
	}
	// The light an edge catches: the key light where it faces the light, a
	// weaker catch-light on the far side, and a faint line all round.
	light := make([]float32, pts)
	for j := 0; j < pts; j++ {
		dot := nx[j]*glassLightDirX + ny[j]*glassLightDirY
		key, back := float32(0), float32(0)
		if dot > 0 {
			key = dot * dot
		} else {
			nd := -dot
			back = nd * nd * nd * 0.5
		}
		v := key + back
		if v > 1 {
			v = 1
		}
		light[j] = 0.16 + 0.84*v
	}
	ring := func(depths, profile []float32, weightOf func(j int) float32) []int32 {
		first := int32(len(m.px))
		for k, d := range depths {
			for j := 0; j < pts; j++ {
				x, y := at(j, d)
				m.px = append(m.px, x)
				m.py = append(m.py, y)
				m.weight = append(m.weight, profile[k]*weightOf(j))
			}
		}
		var idx []int32
		for k := 0; k+1 < len(depths); k++ {
			for j := 0; j < pts; j++ {
				j1 := (j + 1) % pts
				a := first + int32(k*pts+j)
				b := first + int32(k*pts+j1)
				c := first + int32((k+1)*pts+j)
				d := first + int32((k+1)*pts+j1)
				idx = append(idx, a, b, d, a, d, c)
			}
		}
		return idx
	}

	// Rim and thickness: a bright line a pixel in from the edge, feathered
	// on both sides, then a soft band fading into the pane.
	half := fw
	if fh < half {
		half = fh
	}
	half = half/2 - 0.25
	band := half * 0.18
	if band < 3.5 {
		band = 3.5
	}
	if band > 9 {
		band = 9
	}
	depths := []float32{0, 1, 2.2, band}
	if band > half {
		k := half / band
		for i := range depths {
			depths[i] *= k
		}
	}
	ringIdx := ring(depths, []float32{0.55, 1, 0.3, 0}, func(j int) float32 { return light[j] })
	m.nRing = len(m.px) - m.nFill
	m.overIdx = append(append(make([]int32, 0, len(m.fillIdx)+len(ringIdx)), m.fillIdx...), ringIdx...)

	// Shadow: outward from the edge, nudged down.
	pad := half * 0.25
	if pad < 6 {
		pad = 6
	}
	if pad > 18 {
		pad = 18
	}
	m.shadowDy = pad / 3
	m.shadowIdx = ring([]float32{0, -pad * 0.45, -pad}, []float32{1, 0.35, 0}, func(int) float32 { return 1 })
	return m
}

// ── Frost ────────────────────────────────────────────────────────────────

// glassFrostKeyNow is what the backdrop's still layers show this frame: the
// same choices drawAppBackdrop makes.
func glassFrostKeyNow() glassFrostKey {
	config := appConfig
	if config == nil {
		config = homeBackdropConfig
	}
	k := glassFrostKey{w: screenWidth, h: screenHeight}
	if config != nil && config.Variables.DynamicBg {
		// The living background's base is one flat colour under drifting
		// lights (dynamicbg.go renderAmbientDynamicBg).
		k.dynamic = true
		k.base = getTimePalette().top
		k.base.A = 255
		return k
	}
	k.base = ColorBackground
	k.base.A = 255
	k.bloom1, k.bloom2 = themeBackdropBlooms(config)
	if appWallpaperActive(config) {
		if gifWallpaperReady(config) {
			k.gif = true
			// Frames still decoding: rebuild once they arrive. A still GIF
			// (one frame, held by Reduce motion) is painted once; a moving
			// one four times a second, once a second in Low Power.
			k.gifFrames = len(gifWall.frames)
			if k.gifFrames > 1 && !gifWallHoldStill(config) {
				step := uint64(250)
				if effectiveLowPower(config) {
					step = 1000
				}
				k.gifTick = sdl.GetTicks64() / step
			}
		} else {
			k.wall, k.wallPath = bgTexture, bgImagePath
		}
	}
	return k
}

// glassFrostTexture returns the frosted backdrop (nil when there is none).
func glassFrostTexture(renderer *sdl.Renderer) *sdl.Texture {
	g := &glassReal
	if g.renderer != renderer {
		// A new renderer took the old one's textures with it: forget them,
		// never destroy them.
		g.levels = [glassFrostLevels]*sdl.Texture{}
		g.levelW = [glassFrostLevels]int32{}
		g.levelH = [glassFrostLevels]int32{}
		g.frostValid, g.frostFailed, g.keyRead = false, false, false
		g.renderer = renderer
	}
	if g.frostFailed {
		return nil
	}
	if !g.keyRead || g.keyFrame != frameCount {
		g.keyRead, g.keyFrame = true, frameCount
		key := glassFrostKeyNow()
		if !g.frostValid || key != g.frostKey {
			g.frostKey = key
			g.frostValid = glassBuildFrost(renderer, key)
			if !g.frostValid {
				g.frostFailed = true
				// Nothing samples a half-built chain: free it now.
				glassFrostRelease(renderer)
				logWarn("glass", "could not build the frosted backdrop (%v); realistic glass without blur", sdl.GetError())
			}
		}
	}
	if !g.frostValid {
		return nil
	}
	return g.levels[glassFrostLevels-1]
}

// glassFrostRelease frees the frost chain once the style is off: up to 1.2 MB
// of textures nothing draws any more. Only on the renderer that made them; a
// new renderer took them with it, so they are just forgotten then.
func glassFrostRelease(renderer *sdl.Renderer) {
	g := &glassReal
	for i := range g.levels {
		if g.levels[i] != nil && g.renderer == renderer {
			_ = g.levels[i].Destroy()
		}
	}
	g.levels = [glassFrostLevels]*sdl.Texture{}
	g.levelW = [glassFrostLevels]int32{}
	g.levelH = [glassFrostLevels]int32{}
	g.frostValid, g.keyRead = false, false
}

// glassFrostLevel makes (or keeps) chain level i at w×h: a render target
// sampled linearly, so each halving averages and the last one, stretched over
// a pane, is smooth.
func glassFrostLevel(renderer *sdl.Renderer, i int, w, h int32) bool {
	g := &glassReal
	if g.levels[i] != nil && g.levelW[i] == w && g.levelH[i] == h {
		return true
	}
	if g.levels[i] != nil {
		_ = g.levels[i].Destroy()
		g.levels[i] = nil
	}
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, w, h)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return false
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	g.levels[i], g.levelW[i], g.levelH[i] = tex, w, h
	return true
}

// glassBuildFrost paints the backdrop's still layers into the chain and
// halves them down to 1/16 of the screen. Without a wallpaper the picture is
// already smooth, so it is painted straight into the last level.
func glassBuildFrost(renderer *sdl.Renderer, k glassFrostKey) bool {
	if renderer == nil || k.w <= 0 || k.h <= 0 || !renderer.RenderTargetSupported() {
		return false
	}
	g := &glassReal
	wall := k.wall != nil || k.gif
	first := glassFrostLevels - 1
	if wall {
		first = 0
	}
	// Only the levels this picture uses: the half-size one (0.9 MB on the
	// Smart Pro) exists only while a wallpaper does.
	for i := 0; i < glassFrostLevels; i++ {
		if i < first {
			if g.levels[i] != nil {
				_ = g.levels[i].Destroy()
				g.levels[i], g.levelW[i], g.levelH[i] = nil, 0, 0
			}
			continue
		}
		lw, lh := k.w>>(i+1), k.h>>(i+1)
		if lw < 4 {
			lw = 4
		}
		if lh < 4 {
			lh = 4
		}
		if !glassFrostLevel(renderer, i, lw, lh) {
			return false
		}
	}

	// Paint into a target and come back: SDL restores the screen's clip
	// itself, but a texture target comes back unclipped, so the clip is put
	// back by hand (core.go fitBackgroundTexture does the same).
	prev := renderer.GetRenderTarget()
	hadClip := renderer.IsClipEnabled()
	clip := renderer.GetClipRect()
	defer func() {
		_ = renderer.SetRenderTarget(prev)
		// Only a texture target needs it: SDL puts the screen's own clip
		// back exactly, and a logical-scale round trip could shift it.
		if hadClip && prev != nil {
			_ = renderer.SetClipRect(&clip)
		}
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	}()

	if err := renderer.SetRenderTarget(g.levels[first]); err != nil {
		return false
	}
	tw, th := g.levelW[first], g.levelH[first]
	fx, fy := float64(tw)/float64(k.w), float64(th)/float64(k.h)
	base := k.base
	setDrawColor(renderer, base)
	_ = renderer.Clear()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if wall {
		config := appConfig
		if config == nil {
			config = homeBackdropConfig
		}
		if tex, src := appWallpaperTexture(renderer, config); tex != nil {
			_ = renderer.Copy(tex, src, nil)
		}
		base.A = 150
		setDrawColor(renderer, base)
		_ = renderer.FillRect(nil)
	}
	if !k.dynamic {
		scale := func(r sdl.Rect) sdl.Rect {
			return sdl.Rect{
				X: int32(math.Round(float64(r.X) * fx)), Y: int32(math.Round(float64(r.Y) * fy)),
				W: int32(math.Round(float64(r.W) * fx)), H: int32(math.Round(float64(r.H) * fy)),
			}
		}
		blob := scaleX(900, k.w)
		homeGlowBlob(renderer, scale(sdl.Rect{
			X: scaleX(60, k.w) - blob/2, Y: scaleY(300, k.h) - blob/2,
			W: blob, H: blob,
		}), k.bloom1, 44)
		homeGlowBlob(renderer, scale(sdl.Rect{
			X: k.w - blob/2 - scaleX(60, k.w), Y: k.h - blob/2,
			W: blob, H: blob,
		}), k.bloom2, 36)
	}
	// Halve down the chain. The last step adds a little of the picture to
	// itself: glass brightens and saturates what is behind it.
	for i := first + 1; i < glassFrostLevels; i++ {
		src := g.levels[i-1]
		if err := renderer.SetRenderTarget(g.levels[i]); err != nil {
			return false
		}
		_ = src.SetBlendMode(sdl.BLENDMODE_NONE)
		_ = renderer.Copy(src, nil, nil)
		if i == glassFrostLevels-1 {
			_ = src.SetBlendMode(sdl.BLENDMODE_ADD)
			_ = src.SetAlphaMod(40)
			_ = renderer.Copy(src, nil, nil)
			_ = src.SetAlphaMod(255)
		}
		_ = src.SetBlendMode(sdl.BLENDMODE_BLEND)
	}
	return true
}
