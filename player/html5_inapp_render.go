package main

import (
	"encoding/json"
	"math"
	"sort"
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

func html5ScreenPoint(v *html5GameView, p html5Point) (int32, int32) {
	x := float64(v.board.X) + p.X*float64(v.board.W)/float64(v.pkg.Width)
	y := float64(v.board.Y) + p.Y*float64(v.board.H)/float64(v.pkg.Height)
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return 0, 0
	}
	return int32(math.Round(x)), int32(math.Round(y))
}

func html5DrawRect(r *sdl.Renderer, v *html5GameView, cmd html5Draw) {
	x, y := html5ScreenPoint(v, html5Point{cmd.X, cmd.Y})
	x2, y2 := html5ScreenPoint(v, html5Point{cmd.X + cmd.W, cmd.Y + cmd.H})
	box := sdl.Rect{X: x, Y: y, W: x2 - x, H: y2 - y}
	if box.W <= 0 || box.H <= 0 {
		return
	}
	c := html5CanvasColor(cmd.Color)
	r.SetDrawColor(c.R, c.G, c.B, c.A)
	if cmd.Kind == "strokeRect" {
		_ = r.DrawRect(&box)
	} else {
		_ = r.FillRect(&box)
	}
}

func html5DrawPath(r *sdl.Renderer, v *html5GameView, cmd html5Draw) {
	if len(cmd.Points) < 2 {
		return
	}
	c := html5CanvasColor(cmd.Color)
	r.SetDrawColor(c.R, c.G, c.B, c.A)
	if cmd.Kind == "stroke" {
		for i := 1; i < len(cmd.Points); i++ {
			x0, y0 := html5ScreenPoint(v, cmd.Points[i-1])
			x1, y1 := html5ScreenPoint(v, cmd.Points[i])
			_ = r.DrawLine(x0, y0, x1, y1)
		}
		return
	}
	points := make([]html5Point, len(cmd.Points))
	minY, maxY := int32(math.MaxInt32), int32(math.MinInt32)
	for i, point := range cmd.Points {
		x, y := html5ScreenPoint(v, point)
		points[i] = html5Point{float64(x), float64(y)}
		minY, maxY = min(minY, y), max(maxY, y)
	}
	minY = max(minY, v.board.Y)
	maxY = min(maxY, v.board.Y+v.board.H)
	for y := minY; y < maxY; y++ {
		var crosses []float64
		for i := range points {
			a, b := points[i], points[(i+1)%len(points)]
			if (a.Y <= float64(y) && b.Y > float64(y)) || (b.Y <= float64(y) && a.Y > float64(y)) {
				crosses = append(crosses, a.X+(float64(y)-a.Y)*(b.X-a.X)/(b.Y-a.Y))
			}
		}
		sort.Float64s(crosses)
		for i := 1; i < len(crosses); i += 2 {
			_ = r.DrawLine(int32(crosses[i-1]), y, int32(crosses[i]), y)
		}
	}
}

func html5DrawText(r *sdl.Renderer, config *Config, v *html5GameView, cmd html5Draw) {
	fontName := "small"
	if strings.Contains(cmd.Font, "48px") || strings.Contains(cmd.Font, "40px") {
		fontName = "large"
	}
	font, _ := getCachedFont(config, fontName)
	if font == nil {
		return
	}
	x, y := html5ScreenPoint(v, html5Point{cmd.X, cmd.Y})
	width, _, _ := font.SizeUTF8(cmd.Text)
	if cmd.Align == "center" {
		x -= int32(width) / 2
	} else if cmd.Align == "right" {
		x -= int32(width)
	}
	y -= int32(font.Height())
	renderText(r, config, font, cmd.Text, html5CanvasColor(cmd.Color), x, y)
}

func renderHTML5GameInApp(r *sdl.Renderer, config *Config) {
	v := html5View
	if v == nil || r == nil || config == nil {
		return
	}
	v.frame(time.Now())
	v.board = html5CanvasRect(v)
	r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	r.SetDrawColor(7, 17, 30, 255)
	r.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	font, _ := getCachedFont(config, "small")
	large, _ := getCachedFont(config, "large")
	if large != nil {
		renderText(r, config, large, v.game.Title, sdl.Color{R: 225, G: 248, B: 255, A: 255}, 22, 12)
	}
	if hud, ok := gojaHUD(v); ok && font != nil {
		width, _, _ := font.SizeUTF8(hud)
		renderText(r, config, font, hud, sdl.Color{R: 130, G: 231, B: 210, A: 255}, screenWidth-int32(width)-24, 21)
	}
	if v.board.W > 0 && v.board.H > 0 {
		r.SetDrawColor(14, 33, 51, 255)
		r.FillRect(&v.board)
		r.SetClipRect(&v.board)
		for _, cmd := range v.commands {
			switch cmd.Kind {
			case "fillRect", "clearRect", "strokeRect":
				html5DrawRect(r, v, cmd)
			case "fill", "stroke":
				html5DrawPath(r, v, cmd)
			case "fillText":
				html5DrawText(r, config, v, cmd)
			}
		}
		r.SetClipRect(nil)
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
