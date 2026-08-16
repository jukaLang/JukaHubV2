package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- Canvas Sandbox ---

func renderCanvasSandbox(renderer *sdl.Renderer, config *Config, element Element) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	elemW := getElementWidth(element, 1160)
	elemH := getElementHeight(element, 500)

	editorW := elemW * 55 / 100
	previewW := elemW - editorW - 20

	editorX := element.X + 10
	editorY := element.Y + 10
	previewX := editorX + editorW + 10
	previewY := editorY

	// editor panel
	fillRoundedRect(renderer, editorX, editorY, editorW, elemH, 10, ColorSurfaceAlt)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 60)
	renderer.FillRect(&sdl.Rect{X: editorX, Y: editorY, W: editorW, H: 1})

	// preview panel
	fillRoundedRect(renderer, previewX, previewY, previewW, elemH, 10, ColorSurfaceAlt)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 60)
	renderer.FillRect(&sdl.Rect{X: previewX, Y: previewY, W: previewW, H: 1})

	lines := strings.Split(canvasCode, "\n")
	lineH := int32(18)
	maxVisible := int((elemH - 20) / lineH)
	if maxVisible < 1 {
		maxVisible = 1
	}
	start := 0
	if len(lines) > maxVisible {
		start = len(lines) - maxVisible
	}

	for i := start; i < len(lines); i++ {
		lineNum := fmt.Sprintf("%d", i+1)
		renderText(renderer, config, font, lineNum, ColorTextTertiary(), editorX+8, editorY+12+int32(i-start)*lineH)
		codeLine := lines[i]
		if len(codeLine) > 55 {
			codeLine = codeLine[:52] + "..."
		}
		renderText(renderer, config, font, codeLine, ColorTextSecondary(), editorX+40, editorY+12+int32(i-start)*lineH)
	}

	if canvasSurface != nil {
		scale := float64(previewW-20) / float64(canvasSurface.W)
		if float64(canvasSurface.H)*scale > float64(elemH-20) {
			scale = float64(elemH-20) / float64(canvasSurface.H)
		}
		dw := int32(float64(canvasSurface.W) * scale)
		dh := int32(float64(canvasSurface.H) * scale)
		dx := previewX + (previewW-dw)/2
		dy := previewY + (elemH-dh)/2
		tex, _ := renderer.CreateTextureFromSurface(canvasSurface)
		if tex != nil {
			renderer.Copy(tex, nil, &sdl.Rect{X: dx, Y: dy, W: dw, H: dh})
			tex.Destroy()
		}
	}
}

func executeCanvasCode(code string) *sdl.Surface {
	w := int32(400)
	h := int32(300)
	surface, err := sdl.CreateRGBSurfaceWithFormat(0, w, h, 32, sdl.PIXELFORMAT_RGBA8888)
	if err != nil {
		return nil
	}

	fill := func(c sdl.Color) {
		rect := &sdl.Rect{X: 0, Y: 0, W: w, H: h}
		surface.FillRect(rect, sdl.MapRGBA(surface.Format, c.R, c.G, c.B, c.A))
	}
	fill(sdl.Color{R: 255, G: 255, B: 255, A: 255})

	// optional TTF font for real text rendering in fillText()
	textFont, fontErr := ttf.OpenFont(resolvePath("Inter-Regular.ttf"), 20)
	if fontErr == nil {
		defer textFont.Close()
	}

	parseColor := func(s string) sdl.Color {
		s = strings.Trim(s, "\"")
		switch strings.ToLower(s) {
		case "red":
			return sdl.Color{R: 255, G: 0, B: 0, A: 255}
		case "green":
			return sdl.Color{R: 0, G: 255, B: 0, A: 255}
		case "blue":
			return sdl.Color{R: 0, G: 0, B: 255, A: 255}
		case "white":
			return sdl.Color{R: 255, G: 255, B: 255, A: 255}
		case "black":
			return sdl.Color{R: 0, G: 0, B: 0, A: 255}
		case "yellow":
			return sdl.Color{R: 255, G: 255, B: 0, A: 255}
		case "cyan":
			return sdl.Color{R: 0, G: 255, B: 255, A: 255}
		case "magenta":
			return sdl.Color{R: 255, G: 0, B: 255, A: 255}
		case "orange":
			return sdl.Color{R: 255, G: 165, B: 0, A: 255}
		case "purple":
			return sdl.Color{R: 128, G: 0, B: 128, A: 255}
		case "gray":
			return sdl.Color{R: 128, G: 128, B: 128, A: 255}
		}
		if strings.HasPrefix(s, "#") && len(s) == 7 {
			var r, g, b uint64
			fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
			return sdl.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
		}
		return sdl.Color{R: 0, G: 0, B: 0, A: 255}
	}

	setPixel := func(x, y int, c sdl.Color) {
		if x < 0 || x >= int(w) || y < 0 || y >= int(h) {
			return
		}
		surface.Lock()
		pixels := surface.Pixels()
		offset := (y*int(w) + x) * 4
		if offset >= 0 && offset+3 < len(pixels) {
			pixels[offset] = c.R
			pixels[offset+1] = c.G
			pixels[offset+2] = c.B
			pixels[offset+3] = c.A
		}
		surface.Unlock()
	}

	lines := strings.Split(code, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		if strings.HasPrefix(line, "clear") {
			c := sdl.Color{R: 255, G: 255, B: 255, A: 255}
			if strings.HasPrefix(line, "clear(") {
				line = strings.TrimPrefix(line, "clear(")
				line = strings.TrimSuffix(line, ")")
				c = parseColor(strings.TrimSpace(line))
			}
			fill(c)
			continue
		}

		if strings.HasPrefix(line, "fillRect(") {
			line = strings.TrimPrefix(line, "fillRect(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 5 {
				var x, y, rw, rh float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &x)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &y)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &rw)
				fmt.Sscanf(strings.TrimSpace(parts[3]), "%f", &rh)
				c := parseColor(strings.TrimSpace(parts[4]))
				rect := &sdl.Rect{X: int32(x), Y: int32(y), W: int32(rw), H: int32(rh)}
				surface.FillRect(rect, sdl.MapRGBA(surface.Format, c.R, c.G, c.B, c.A))
			}
			continue
		}

		if strings.HasPrefix(line, "strokeRect(") {
			line = strings.TrimPrefix(line, "strokeRect(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 5 {
				var x, y, rw, rh float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &x)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &y)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &rw)
				fmt.Sscanf(strings.TrimSpace(parts[3]), "%f", &rh)
				c := parseColor(strings.TrimSpace(parts[4]))
				for i := 0; i < int(rw); i++ {
					setPixel(int(x)+i, int(y), c)
					setPixel(int(x)+i, int(y+rh)-1, c)
				}
				for j := 0; j < int(rh); j++ {
					setPixel(int(x), int(y)+j, c)
					setPixel(int(x+rw)-1, int(y)+j, c)
				}
			}
			continue
		}

		if strings.HasPrefix(line, "fillCircle(") {
			line = strings.TrimPrefix(line, "fillCircle(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 4 {
				var cx, cy, r float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &cx)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &cy)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &r)
				c := parseColor(strings.TrimSpace(parts[3]))
				r2 := r * r
				for yy := -int(r); yy <= int(r); yy++ {
					for xx := -int(r); xx <= int(r); xx++ {
						if xx*xx+yy*yy <= int(r2) {
							setPixel(int(cx)+xx, int(cy)+yy, c)
						}
					}
				}
			}
			continue
		}

		if strings.HasPrefix(line, "strokeCircle(") {
			line = strings.TrimPrefix(line, "strokeCircle(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 4 {
				var cx, cy, r float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &cx)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &cy)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &r)
				c := parseColor(strings.TrimSpace(parts[3]))
				for a := 0.0; a < 2*math.Pi; a += 0.03 {
					setPixel(int(cx+r*math.Cos(a)), int(cy+r*math.Sin(a)), c)
				}
			}
			continue
		}

		if strings.HasPrefix(line, "line(") {
			line = strings.TrimPrefix(line, "line(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 5 {
				var x1, y1, x2, y2 float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &x1)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &y1)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &x2)
				fmt.Sscanf(strings.TrimSpace(parts[3]), "%f", &y2)
				c := parseColor(strings.TrimSpace(parts[4]))
				steps := int(math.Max(math.Abs(x2-x1), math.Abs(y2-y1))) + 1
				if steps < 1 {
					steps = 1
				}
				for i := 0; i <= steps; i++ {
					t := float64(i) / float64(steps)
					setPixel(int(x1+(x2-x1)*t), int(y1+(y2-y1)*t), c)
				}
			}
			continue
		}

		if strings.HasPrefix(line, "setPixel(") {
			line = strings.TrimPrefix(line, "setPixel(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 3 {
				var x, y float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &x)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &y)
				c := parseColor(strings.TrimSpace(parts[2]))
				setPixel(int(x), int(y), c)
			}
			continue
		}

		if strings.HasPrefix(line, "fillText(") {
			line = strings.TrimPrefix(line, "fillText(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.SplitN(line, ",", 3)
			if len(parts) >= 3 {
				text := strings.Trim(parts[0], "\" ")
				var tx, ty float64
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &tx)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &ty)
				c := sdl.Color{R: 0, G: 0, B: 0, A: 255}
				if len(parts) >= 4 {
					c = parseColor(strings.TrimSpace(parts[3]))
				}
				if textFont != nil && fontErr == nil {
					if ts, terr := textFont.RenderUTF8Blended(text, c); terr == nil {
						surface.Blit(&sdl.Rect{X: int32(tx), Y: int32(ty)}, ts, nil)
						ts.Free()
					}
				} else {
					for i, ch := range text {
						setPixel(int(tx)+i, int(ty), c)
						_ = ch
					}
				}
			}
			continue
		}

		if strings.HasPrefix(line, "beginPath()") || strings.HasPrefix(line, "fill()") || strings.HasPrefix(line, "stroke()") {
			continue
		}

		if strings.HasPrefix(line, "arc(") {
			line = strings.TrimPrefix(line, "arc(")
			line = strings.TrimSuffix(line, ")")
			parts := strings.Split(line, ",")
			if len(parts) >= 5 {
				var cx, cy, radius float64
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &cx)
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &cy)
				fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &radius)
				c := sdl.Color{R: 0, G: 0, B: 0, A: 255}
				if len(parts) >= 6 {
					c = parseColor(strings.TrimSpace(parts[5]))
				}
				for angle := 0.0; angle < 2*math.Pi; angle += 0.05 {
					x := cx + radius*math.Cos(angle)
					y := cy + radius*math.Sin(angle)
					setPixel(int(x), int(y), c)
				}
			}
			continue
		}
	}

	return surface
}

func handleCanvasInput(e *sdl.KeyboardEvent, config *Config) {
	if e.Type == sdl.KEYDOWN {
		switch e.Keysym.Sym {
		case sdl.K_RETURN:
			canvasCode = inputTextBuffer
			if canvasSurface != nil {
				canvasSurface.Free()
				canvasSurface = nil
			}
			canvasSurface = executeCanvasCode(canvasCode)
		case sdl.K_BACKSPACE:
			if len(inputTextBuffer) > 0 {
				inputTextBuffer = inputTextBuffer[:len(inputTextBuffer)-1]
			}
		default:
			if e.Keysym.Sym != 0 {
				inputTextBuffer += string(rune(e.Keysym.Sym))
			}
		}
	}
}
