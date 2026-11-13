package main

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/veandco/go-sdl2/sdl"
)

func html5CanvasColor(value string) sdl.Color {
	c, ok := parseWebColor(value)
	if !ok {
		return sdl.Color{R: 255, G: 255, B: 255, A: 255}
	}
	if strings.HasPrefix(value, "#") && len(value) == 9 {
		if alpha, ok := webHexByte(value[7:9]); ok {
			c.A = alpha
		}
	}
	return c
}

func html5CanvasRect(v *html5GameView) sdl.Rect {
	if v == nil || v.pkg.Width == 0 || v.pkg.Height == 0 {
		return sdl.Rect{}
	}
	const top, bottom, margin = int32(58), int32(76), int32(18)
	availW, availH := screenWidth-margin*2, screenHeight-top-bottom
	if availW <= 0 || availH <= 0 {
		return sdl.Rect{}
	}
	scale := math.Min(float64(availW)/float64(v.pkg.Width), float64(availH)/float64(v.pkg.Height))
	w, h := int32(float64(v.pkg.Width)*scale), int32(float64(v.pkg.Height)*scale)
	return sdl.Rect{X: (screenWidth - w) / 2, Y: top + (availH-h)/2, W: w, H: h}
}

// html5Xform maps canvas coordinates to the surface being drawn: 1:1 into the
// retained canvas texture, or scaled onto the board when there is none.
type html5Xform struct{ sx, sy, ox, oy float64 }

func (t html5Xform) pt(p html5Point) (float32, float32) {
	return float32(t.ox + p.X*t.sx), float32(t.oy + p.Y*t.sy)
}

func html5Clamp32(f float64) int32 {
	switch {
	case math.IsNaN(f):
		return 0
	case f > 1<<20:
		return 1 << 20
	case f < -(1 << 20):
		return -(1 << 20)
	}
	return int32(math.Round(f))
}

func (v *html5GameView) drawRect(r *sdl.Renderer, t html5Xform, cmd html5Draw, retained bool) {
	x, y := html5Clamp32(t.ox+cmd.X*t.sx), html5Clamp32(t.oy+cmd.Y*t.sy)
	x2, y2 := html5Clamp32(t.ox+(cmd.X+cmd.W)*t.sx), html5Clamp32(t.oy+(cmd.Y+cmd.H)*t.sy)
	if x2 < x {
		x, x2 = x2, x
	}
	if y2 < y {
		y, y2 = y2, y
	}
	box := sdl.Rect{X: x, Y: y, W: x2 - x, H: y2 - y}
	if box.W <= 0 || box.H <= 0 {
		return
	}
	c := cmd.RGBA
	switch cmd.Kind {
	case "clearRect":
		if retained {
			// Cleared canvas pixels are transparent, not painted black.
			r.SetDrawBlendMode(sdl.BLENDMODE_NONE)
			r.SetDrawColor(0, 0, 0, 0)
			_ = r.FillRect(&box)
			r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			return
		}
		r.SetDrawColor(14, 33, 51, 255)
		_ = r.FillRect(&box)
	case "strokeRect":
		r.SetDrawColor(c.R, c.G, c.B, c.A)
		lw := html5Clamp32(cmd.LineWidth * t.sx)
		if lw <= 1 {
			_ = r.DrawRect(&box)
			return
		}
		lw = min(lw, box.W/2+1, box.H/2+1)
		v.rects = append(v.rects[:0],
			sdl.Rect{X: box.X, Y: box.Y, W: box.W, H: lw},
			sdl.Rect{X: box.X, Y: box.Y + box.H - lw, W: box.W, H: lw},
			sdl.Rect{X: box.X, Y: box.Y + lw, W: lw, H: box.H - 2*lw},
			sdl.Rect{X: box.X + box.W - lw, Y: box.Y + lw, W: lw, H: box.H - 2*lw})
		_ = r.FillRects(v.rects)
	default:
		r.SetDrawColor(c.R, c.G, c.B, c.A)
		_ = r.FillRect(&box)
	}
}

// html5Convex reports whether one closed polygon turns the same way at every
// corner and goes round exactly once (a pentagram turns one way too, but
// twice), so a triangle fan fills it exactly.
func html5Convex(pts []sdl.FPoint) bool {
	sign := 0
	n := len(pts)
	turn := 0.0
	for i := 0; i < n; i++ {
		a, b, c := pts[i], pts[(i+1)%n], pts[(i+2)%n]
		cross := (b.X-a.X)*(c.Y-b.Y) - (b.Y-a.Y)*(c.X-b.X)
		if cross > 1e-3 {
			if sign < 0 {
				return false
			}
			sign = 1
		} else if cross < -1e-3 {
			if sign > 0 {
				return false
			}
			sign = -1
		}
		d1 := math.Atan2(float64(b.Y-a.Y), float64(b.X-a.X))
		d2 := math.Atan2(float64(c.Y-b.Y), float64(c.X-b.X))
		d := d2 - d1
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		turn += d
	}
	return math.Abs(math.Abs(turn)-2*math.Pi) < 0.5
}

// html5CopyBlend is how the canvas texture goes onto the board. Paint drawn
// into a transparent texture is already multiplied by its alpha there, so the
// copy must not multiply it again (BLEND would darken see-through pixels).
var html5CopyBlend = sdl.ComposeCustomBlendMode(
	sdl.BLENDFACTOR_ONE, sdl.BLENDFACTOR_ONE_MINUS_SRC_ALPHA, sdl.BLENDOPERATION_ADD,
	sdl.BLENDFACTOR_ONE, sdl.BLENDFACTOR_ONE_MINUS_SRC_ALPHA, sdl.BLENDOPERATION_ADD)

func (v *html5GameView) vertex(x, y float32, c sdl.Color) sdl.Vertex {
	return sdl.Vertex{Position: sdl.FPoint{X: x, Y: y}, Color: c}
}

// drawFill fills a path. When every shape in it is convex (circles, boxes,
// most sprites drawn with paths, a burst of particles in one path) each is a
// triangle fan and the whole path goes to the GPU in one call; anything else
// is scanned into spans (even-odd) and sent as one batch of rects.
func (v *html5GameView) drawFill(r *sdl.Renderer, t html5Xform, cmd html5Draw, height int32) {
	src := v.points[cmd.P0:cmd.P1]
	c := cmd.RGBA
	if !v.noGeometry {
		v.verts = v.verts[:0]
		v.indices = v.indices[:0]
		convex := true
		shape := func() {
			pts := v.fpts
			if n := len(pts); n > 1 && pts[0] == pts[n-1] {
				pts = pts[:n-1]
			}
			if len(pts) < 3 {
				v.fpts = v.fpts[:0]
				return
			}
			if !html5Convex(pts) {
				convex = false
				return
			}
			base := int32(len(v.verts))
			for i, p := range pts {
				v.verts = append(v.verts, v.vertex(p.X, p.Y, c))
				if i >= 2 {
					v.indices = append(v.indices, base, base+int32(i-1), base+int32(i))
				}
			}
			v.fpts = v.fpts[:0]
		}
		v.fpts = v.fpts[:0]
		for _, p := range src {
			if math.IsNaN(p.X) {
				if shape(); !convex {
					break
				}
				continue
			}
			x, y := t.pt(p)
			v.fpts = append(v.fpts, sdl.FPoint{X: x, Y: y})
		}
		if convex {
			shape()
		}
		if convex {
			if len(v.indices) == 0 || r.RenderGeometry(nil, v.verts, v.indices) == nil {
				return
			}
			v.noGeometry = true
		}
	}
	v.fpts = v.fpts[:0]
	for _, p := range src {
		if !math.IsNaN(p.X) {
			x, y := t.pt(p)
			v.fpts = append(v.fpts, sdl.FPoint{X: x, Y: y})
		}
	}
	if len(v.fpts) < 3 {
		return
	}
	// Even-odd scanline over every subpath's edges.
	minY, maxY := float32(math.MaxFloat32), float32(-math.MaxFloat32)
	for _, p := range v.fpts {
		minY, maxY = min(minY, p.Y), max(maxY, p.Y)
	}
	y0 := max(int32(math.Floor(float64(minY))), 0)
	y1 := min(int32(math.Ceil(float64(maxY))), height)
	v.rects = v.rects[:0]
	for y := y0; y < y1; y++ {
		fy := float64(y) + 0.5
		v.crosses = v.crosses[:0]
		start := -1
		for i := 0; i <= len(src); i++ {
			// Edges run between consecutive points of one subpath, plus the
			// closing edge back to the subpath's first point.
			var a, b html5Point
			if i == len(src) || math.IsNaN(src[i].X) {
				if start >= 0 && i-1 > start {
					a, b = src[i-1], src[start]
					v.crossing(t, a, b, fy)
				}
				start = -1
				continue
			}
			if start < 0 {
				start = i
				continue
			}
			a, b = src[i-1], src[i]
			v.crossing(t, a, b, fy)
		}
		slices.Sort(v.crosses)
		for i := 1; i < len(v.crosses); i += 2 {
			x0, x1 := html5Clamp32(v.crosses[i-1]), html5Clamp32(v.crosses[i])
			if x1 > x0 {
				v.rects = append(v.rects, sdl.Rect{X: x0, Y: y, W: x1 - x0, H: 1})
			}
		}
	}
	if len(v.rects) > 0 {
		r.SetDrawColor(c.R, c.G, c.B, c.A)
		_ = r.FillRects(v.rects)
	}
}

func (v *html5GameView) crossing(t html5Xform, a, b html5Point, fy float64) {
	ax, ay := t.ox+a.X*t.sx, t.oy+a.Y*t.sy
	bx, by := t.ox+b.X*t.sx, t.oy+b.Y*t.sy
	if (ay <= fy && by > fy) || (by <= fy && ay > fy) {
		v.crosses = append(v.crosses, ax+(fy-ay)*(bx-ax)/(by-ay))
	}
}

// drawStroke draws a path's outline: thin lines as one polyline call per
// subpath, wider ones as quads in one geometry call.
func (v *html5GameView) drawStroke(r *sdl.Renderer, t html5Xform, cmd html5Draw) {
	src := v.points[cmd.P0:cmd.P1]
	c := cmd.RGBA
	width := float32(cmd.LineWidth * t.sx)
	if width > 1.5 && !v.noGeometry {
		v.verts = v.verts[:0]
		v.indices = v.indices[:0]
		half := width / 2
		for i := 1; i < len(src); i++ {
			if math.IsNaN(src[i].X) || math.IsNaN(src[i-1].X) {
				continue
			}
			ax, ay := t.pt(src[i-1])
			bx, by := t.pt(src[i])
			dx, dy := bx-ax, by-ay
			l := float32(math.Hypot(float64(dx), float64(dy)))
			if l < 1e-3 {
				continue
			}
			nx, ny := -dy/l*half, dx/l*half
			// Extend each end by half the width so corners meet.
			ex, ey := dx/l*half, dy/l*half
			base := int32(len(v.verts))
			v.verts = append(v.verts,
				v.vertex(ax+nx-ex, ay+ny-ey, c), v.vertex(bx+nx+ex, by+ny+ey, c),
				v.vertex(bx-nx+ex, by-ny+ey, c), v.vertex(ax-nx-ex, ay-ny-ey, c))
			v.indices = append(v.indices, base, base+1, base+2, base, base+2, base+3)
		}
		if len(v.indices) == 0 {
			return
		}
		if r.RenderGeometry(nil, v.verts, v.indices) == nil {
			return
		}
		v.noGeometry = true
	}
	r.SetDrawColor(c.R, c.G, c.B, c.A)
	v.fpts = v.fpts[:0]
	flush := func() {
		if len(v.fpts) > 1 {
			_ = r.DrawLinesF(v.fpts)
		}
		v.fpts = v.fpts[:0]
	}
	for _, p := range src {
		if math.IsNaN(p.X) {
			flush()
			continue
		}
		x, y := t.pt(p)
		v.fpts = append(v.fpts, sdl.FPoint{X: x, Y: y})
	}
	flush()
}

// html5FontPixels reads the size out of a CSS font shorthand ("bold 48px
// system-ui"), 16 when it names none.
func html5FontPixels(font string) int {
	for _, field := range strings.Fields(font) {
		if px, ok := strings.CutSuffix(field, "px"); ok {
			if n, err := strconv.ParseFloat(px, 64); err == nil && n > 0 {
				return int(n)
			}
		}
	}
	return 16
}

func (v *html5GameView) drawText(r *sdl.Renderer, config *Config, t html5Xform, cmd html5Draw) {
	fontName := "small"
	if html5FontPixels(cmd.Font) >= 32 {
		fontName = "large"
	}
	font, _ := getCachedFont(config, fontName)
	if font == nil || cmd.Text == "" {
		return
	}
	// Game text is drawn as the game wrote it: not translated, no $variables.
	text := sanitizeCached(font, cmd.Text)
	x, y := html5Clamp32(t.ox+cmd.X*t.sx), html5Clamp32(t.oy+cmd.Y*t.sy)
	if cmd.Align == "center" || cmd.Align == "right" || cmd.Align == "end" {
		width, _, _ := font.SizeUTF8(text)
		if cmd.Align == "center" {
			x -= int32(width) / 2
		} else {
			x -= int32(width)
		}
	}
	// The canvas y is the alphabetic baseline.
	y -= int32(font.Ascent())
	drawTextRun(r, config, font, text, cmd.RGBA, x, y)
}

// drawCommands draws the queued calls onto the current target.
func (v *html5GameView) drawCommands(r *sdl.Renderer, config *Config, t html5Xform, retained bool, height int32) {
	r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for _, cmd := range v.commands {
		switch cmd.Kind {
		case "fillRect", "clearRect", "strokeRect":
			v.drawRect(r, t, cmd, retained)
		case "fill":
			v.drawFill(r, t, cmd, height)
		case "stroke":
			v.drawStroke(r, t, cmd)
		case "fillText":
			v.drawText(r, config, t, cmd)
		}
	}
}

// canvasTexture returns the game's retained canvas, made on first use and
// cleared to transparent like a new browser canvas.
func (v *html5GameView) canvasTexture(r *sdl.Renderer) *sdl.Texture {
	if v.canvas != nil || v.canvasFailed {
		return v.canvas
	}
	tex, err := r.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, int32(v.pkg.Width), int32(v.pkg.Height))
	if err != nil || tex == nil {
		v.canvasFailed = true
		return nil
	}
	if tex.SetBlendMode(html5CopyBlend) != nil {
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	}
	prev := r.GetRenderTarget()
	if r.SetRenderTarget(tex) != nil {
		_ = tex.Destroy()
		v.canvasFailed = true
		return nil
	}
	r.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	r.SetDrawColor(0, 0, 0, 0)
	_ = r.Clear()
	_ = r.SetRenderTarget(prev)
	r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	v.canvas = tex
	return tex
}

// paintBoard brings the board up to date. With a canvas texture only the calls
// since the last frame are drawn, into the texture, which is then copied
// scaled onto the board in one call; without one every retained call is
// replayed onto the board.
func (v *html5GameView) paintBoard(r *sdl.Renderer, config *Config) {
	if tex := v.canvasTexture(r); tex != nil {
		if len(v.commands) > 0 {
			prev := r.GetRenderTarget()
			if r.SetRenderTarget(tex) == nil {
				v.drawCommands(r, config, html5Xform{sx: 1, sy: 1}, true, int32(v.pkg.Height))
				_ = r.SetRenderTarget(prev)
				v.resetCommands()
			} else {
				// The target went away (a lost device): replay from now on.
				_ = tex.Destroy()
				v.canvas, v.canvasFailed = nil, true
			}
		}
		if v.canvas != nil {
			_ = r.Copy(v.canvas, nil, &v.board)
			return
		}
	}
	t := html5Xform{
		sx: float64(v.board.W) / float64(v.pkg.Width), sy: float64(v.board.H) / float64(v.pkg.Height),
		ox: float64(v.board.X), oy: float64(v.board.Y),
	}
	r.SetClipRect(&v.board)
	v.drawCommands(r, config, t, false, v.board.Y+v.board.H)
	r.SetClipRect(nil)
}

func renderHTML5GameInApp(r *sdl.Renderer, config *Config) {
	v := html5View
	if v == nil || r == nil || config == nil {
		return
	}
	v.frame(time.Now())
	if html5View != v {
		return // the frame closed the game
	}
	v.board = html5CanvasRect(v)
	r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	r.SetDrawColor(7, 17, 30, 255)
	r.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	font, _ := getCachedFont(config, "small")
	large, _ := getCachedFont(config, "large")
	if large != nil {
		renderText(r, config, large, v.game.Title, sdl.Color{R: 225, G: 248, B: 255, A: 255}, 22, 12)
	}
	// The score line is read from the page a few times a second, not every
	// frame: it is a JSON round trip through the game's runtime.
	if now := time.Now(); now.Sub(v.hudAt) >= 250*time.Millisecond {
		v.hudAt = now
		v.hud, _ = gojaHUD(v)
	}
	if v.hud != "" && font != nil {
		width, _, _ := font.SizeUTF8(v.hud)
		renderText(r, config, font, v.hud, sdl.Color{R: 130, G: 231, B: 210, A: 255}, screenWidth-int32(width)-24, 21)
	}
	if v.board.W > 0 && v.board.H > 0 {
		r.SetDrawColor(14, 33, 51, 255)
		r.FillRect(&v.board)
		v.paintBoard(r, config)
		r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		r.SetDrawColor(49, 99, 125, 255)
		r.DrawRect(&v.board)
	}
	if v.errorText != "" {
		r.SetDrawColor(8, 14, 26, 235)
		r.FillRect(&v.board)
		if large != nil {
			renderText(r, config, large, "Game stopped", sdl.Color{R: 255, G: 223, B: 180, A: 255}, v.board.X+28, v.board.Y+28)
		}
		if font != nil {
			renderText(r, config, font, firstLine(v.errorText), sdl.Color{R: 255, G: 236, B: 220, A: 255}, v.board.X+28, v.board.Y+86)
		}
	}
	if font != nil {
		renderText(r, config, font, "D-pad / stick  ·  A start  ·  B return", sdl.Color{R: 174, G: 202, B: 219, A: 255}, 246, screenHeight-43)
	}
	for i, label := range []string{"UP", "DN", "LT", "RT"} {
		button := html5ArrowButton(i)
		r.SetDrawColor(28, 59, 80, 255)
		r.FillRect(&button)
		if font != nil {
			renderText(r, config, font, label, sdl.Color{R: 224, G: 245, B: 255, A: 255}, button.X+8, button.Y+9)
		}
	}
	start := html5StartButton()
	r.SetDrawColor(29, 111, 103, 255)
	r.FillRect(&start)
	if font != nil {
		renderText(r, config, font, "Start", sdl.Color{R: 235, G: 255, B: 250, A: 255}, start.X+18, start.Y+9)
	}
}

func gojaHUD(v *html5GameView) (string, bool) {
	fn, ok := goja.AssertFunction(v.rt.Get("__jhHud"))
	if !ok {
		return "", false
	}
	result, err := fn(goja.Undefined())
	if err != nil {
		return "", false
	}
	var rows [][2]string
	if json.Unmarshal([]byte(result.String()), &rows) != nil {
		return "", false
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		if row[0] == "" {
			continue
		}
		parts = append(parts, strings.ToUpper(row[0][:1])+row[0][1:]+" "+row[1])
	}
	return strings.Join(parts, "  ·  "), len(parts) > 0
}

func html5StartButton() sdl.Rect {
	return sdl.Rect{X: screenWidth - 130, Y: screenHeight - 60, W: 108, H: 44}
}

func html5ArrowButton(i int) sdl.Rect {
	return sdl.Rect{X: 22 + int32(i)*52, Y: screenHeight - 60, W: 45, H: 44}
}

func html5GamePointer(e *sdl.MouseButtonEvent) bool {
	if html5View == nil || e == nil {
		return false
	}
	x, y := physicalToLogical(int32(e.X), int32(e.Y))
	if e.Type == sdl.MOUSEBUTTONDOWN {
		for i, key := range []string{"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"} {
			if pointInRect(x, y, html5ArrowButton(i)) {
				html5View.pressedKey = key
				html5View.event("keydown", key, 0, 0, 0)
				return true
			}
		}
		if pointInRect(x, y, html5StartButton()) {
			html5View.event("keydown", "Enter", 0, 0, 0)
			return true
		}
		if pointInRect(x, y, html5View.board) {
			cx := float64(x-html5View.board.X) * float64(html5View.pkg.Width) / float64(html5View.board.W)
			cy := float64(y-html5View.board.Y) * float64(html5View.pkg.Height) / float64(html5View.board.H)
			html5View.event("pointermove", "", cx, cy, 1)
			html5View.event("pointerdown", "", cx, cy, 1)
		}
	} else if html5View.pressedKey != "" {
		html5View.event("keyup", html5View.pressedKey, 0, 0, 0)
		html5View.pressedKey = ""
	}
	return true
}

func html5GameMotion(e *sdl.MouseMotionEvent) bool {
	if html5View == nil || e == nil {
		return false
	}
	x, y := physicalToLogical(int32(e.X), int32(e.Y))
	if pointInRect(x, y, html5View.board) {
		cx := float64(x-html5View.board.X) * float64(html5View.pkg.Width) / float64(html5View.board.W)
		cy := float64(y-html5View.board.Y) * float64(html5View.pkg.Height) / float64(html5View.board.H)
		html5View.event("pointermove", "", cx, cy, int(e.State))
	}
	return true
}
