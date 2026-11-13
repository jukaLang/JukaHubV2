package main

import (
	"image"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Screenshot flash + preview — a white camera flash the moment JukaHub's
// screen is captured, then the picture itself flies from full screen into
// a small card in the bottom-right corner ("Screenshot saved") instead of a
// text-only toast. Clicking the card opens the picture in the image viewer.
//
// A game's screenshot (Hold Menu while RetroArch has the screen, menushot.go)
// cannot flash — JukaHub draws nothing then — so its preview waits and shows
// when JukaHub has the screen back.
//
// Both are drawn after capturePendingScreenshot (main.go), so neither is in
// a screenshot.
// ──────────────────────────────────────────────────────────────────────

const (
	shotFlashMs    = 320  // the white flash fades out over this long
	shotFlyMs      = 420  // full screen → corner
	shotPreviewMs  = 3600 // on screen in all, fly-in included
	shotLeaveMs    = 320  // the last part: slides off to the right
	shotCardPad    = int32(8)
	shotThumbFrame = int32(3) // white frame around the picture
)

var shotFX struct {
	// Main thread only.
	flashAt uint64 // sdl ticks of the flash, 0 = none
	tex     *sdl.Texture
	texW    int32
	texH    int32
	texGen  uint64
	shownAt uint64
	rect    sdl.Rect // the card's hit area this frame (click → viewer)

	// Shared with the saving goroutines.
	mu     sync.Mutex
	gen    uint64       // one per screenshot
	thumb  *image.NRGBA // a new picture for the render thread to upload
	path   string       // where shot gen was saved ("" while saving)
	failed bool         // shot gen was not saved
}

// shotFlashStart starts the flash. Main thread, right after the read.
func shotFlashStart() {
	shotFX.flashAt = sdl.GetTicks64()
}

// shotThumbLimits is how large a preview picture may be on this screen.
func shotThumbLimits(canvasW, canvasH int32) (int, int) {
	w := clampInt32(canvasW*24/100, 140, 340)
	h := clampInt32(canvasH*32/100, 100, 260)
	return int(w), int(h)
}

// shotThumb scales an RGBA picture (R, G, B, A bytes) down into maxW×maxH,
// keeping its shape, by averaging each box of pixels. Any goroutine.
func shotThumb(pix []byte, stride, w, h, maxW, maxH int) *image.NRGBA {
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 || stride < w*4 || len(pix) < stride*(h-1)+w*4 {
		return nil
	}
	tw, th := maxW, maxW*h/w
	if th > maxH {
		th, tw = maxH, maxH*w/h
	}
	tw, th = min(max(tw, 1), w), min(max(th, 1), h)
	out := image.NewNRGBA(image.Rect(0, 0, tw, th))
	for ty := 0; ty < th; ty++ {
		y0 := ty * h / th
		y1 := max((ty+1)*h/th, y0+1)
		for tx := 0; tx < tw; tx++ {
			x0 := tx * w / tw
			x1 := max((tx+1)*w/tw, x0+1)
			var r, g, b, n int
			for y := y0; y < y1; y++ {
				row := pix[y*stride : y*stride+x1*4]
				for x := x0; x < x1; x++ {
					i := x * 4
					r += int(row[i])
					g += int(row[i+1])
					b += int(row[i+2])
					n++
				}
			}
			o := ty*out.Stride + tx*4
			out.Pix[o] = uint8(r / n)
			out.Pix[o+1] = uint8(g / n)
			out.Pix[o+2] = uint8(b / n)
			out.Pix[o+3] = 255
		}
	}
	return out
}

// shotPreviewPark hands a new screenshot's picture to the render thread and
// returns its number for shotPreviewSaved / shotPreviewFailed (0 = none).
// Any goroutine.
func shotPreviewPark(thumb *image.NRGBA) uint64 {
	if thumb == nil {
		return 0
	}
	shotFX.mu.Lock()
	defer shotFX.mu.Unlock()
	shotFX.gen++
	shotFX.thumb, shotFX.path, shotFX.failed = thumb, "", false
	return shotFX.gen
}

// shotPreviewSaved tells the card where shot gen landed. Any goroutine.
func shotPreviewSaved(gen uint64, path string) {
	if gen == 0 {
		return
	}
	shotFX.mu.Lock()
	if shotFX.gen == gen {
		shotFX.path = path
	}
	shotFX.mu.Unlock()
}

// shotPreviewFailed takes shot gen's card away (an error toast says why).
// Any goroutine.
func shotPreviewFailed(gen uint64) {
	if gen == 0 {
		return
	}
	shotFX.mu.Lock()
	if shotFX.gen == gen {
		shotFX.thumb, shotFX.failed = nil, true
	}
	shotFX.mu.Unlock()
}

// shotPreviewDrop removes the card. Main thread.
func shotPreviewDrop() {
	if shotFX.tex != nil {
		shotFX.tex.Destroy()
		shotFX.tex = nil
	}
	shotFX.rect = sdl.Rect{}
}

// shotFXRender draws the flash and the card. Main thread, every frame,
// after the screenshot capture.
func shotFXRender(renderer *sdl.Renderer, config *Config) {
	shotFX.rect = sdl.Rect{}
	if renderer == nil {
		return
	}
	now := sdl.GetTicks64()
	calm := config != nil && effectiveReducedMotion(config)
	shotFlashRender(renderer, now, calm)
	shotPreviewRender(renderer, config, now, calm)
}

// shotFlashRender is the white flash; Reduced motion keeps it to a soft
// one-third glow.
func shotFlashRender(renderer *sdl.Renderer, now uint64, calm bool) {
	if shotFX.flashAt == 0 {
		return
	}
	elapsed := now - shotFX.flashAt
	if elapsed >= shotFlashMs {
		shotFX.flashAt = 0
		return
	}
	// Bright at once, then an ease-out fade, like a camera's flash.
	left := 1 - float64(elapsed)/shotFlashMs
	peak := 235.0
	if calm {
		peak = 80
	}
	a := uint8(peak * left * left)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetDrawColor(255, 255, 255, a)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
}

func shotEaseOut(t float64) float64 {
	t = min(max(t, 0), 1)
	u := 1 - t
	return 1 - u*u*u
}

func shotLerp(a, b int32, t float64) int32 {
	return a + int32(float64(b-a)*t)
}

// shotPreviewRender is the card; Reduced motion puts it in the corner at
// once instead of flying it there.
func shotPreviewRender(renderer *sdl.Renderer, config *Config, now uint64, calm bool) {
	shotFX.mu.Lock()
	gen, thumb, path, failed := shotFX.gen, shotFX.thumb, shotFX.path, shotFX.failed
	shotFX.thumb = nil
	shotFX.mu.Unlock()

	if thumb != nil {
		shotPreviewDrop()
		shotFX.tex = imgvTexture(renderer, thumb)
		shotFX.texW, shotFX.texH = int32(thumb.Rect.Dx()), int32(thumb.Rect.Dy())
		shotFX.texGen = gen
		shotFX.shownAt = now
	}
	if shotFX.tex == nil {
		return
	}
	if gen != shotFX.texGen {
		// A newer shot is still being scaled; keep this card until it lands.
		path, failed = "", false
	}
	if failed {
		shotPreviewDrop()
		return
	}
	elapsed := now - shotFX.shownAt
	if elapsed >= shotPreviewMs {
		shotPreviewDrop()
		return
	}

	font, _ := getCachedFont(config, "small")
	caption := "Screenshot saved"
	if path == "" {
		caption = "Saving screenshot…"
	}
	var lineH, capW int32
	if font != nil {
		lineH = int32(font.Height())
		capW, _ = measureText(font, caption)
	}

	// The card: the picture in a white frame, the caption under it.
	tw, th := shotFX.texW, shotFX.texH
	cardW := max(tw+2*shotThumbFrame, capW) + 2*shotCardPad
	cardH := th + 2*shotThumbFrame + 2*shotCardPad
	if lineH > 0 {
		cardH += lineH + 6
	}
	x := screenWidth - toastMarginRight - cardW
	y := screenHeight - HomeFooterH - 16 - cardH
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		y = screenHeight - bottomBarHeight(config.Scenes[currentSceneIndex].Name) - 16 - cardH
	}
	x, y = max(x, toastMarginMin), max(y, toastMarginMin)

	// Leaving: slide right and fade over the last shotLeaveMs.
	alpha := 1.0
	if leave := int64(elapsed) - (shotPreviewMs - shotLeaveMs); leave > 0 {
		t := shotEaseOut(float64(leave) / shotLeaveMs)
		if !calm {
			x += int32(float64(screenWidth-x) * t)
		}
		alpha = 1 - t
	}

	// The picture's place in the card, and where it flies from: the whole
	// screen, so the shot visibly shrinks into the corner.
	endX := x + (cardW-tw)/2
	endY := y + shotCardPad + shotThumbFrame
	fly := shotEaseOut(float64(elapsed) / shotFlyMs)
	if calm {
		fly = 1
	}
	startW := screenWidth
	startH := screenWidth * th / max(tw, 1)
	if startH > screenHeight {
		startH = screenHeight
		startW = screenHeight * tw / max(th, 1)
	}
	startX, startY := (screenWidth-startW)/2, (screenHeight-startH)/2
	pic := sdl.Rect{
		X: shotLerp(startX, endX, fly),
		Y: shotLerp(startY, endY, fly),
		W: shotLerp(startW, tw, fly),
		H: shotLerp(startH, th, fly),
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	cardA := uint8(255 * alpha * fly)
	if cardA > 0 {
		shadow := sdl.Color{R: 0, G: 0, B: 0, A: uint8(70 * alpha * fly)}
		fillRoundedRect(renderer, x+2, y+4, cardW, cardH, 14, shadow)
		fill := glassSheetFill()
		fill.A = uint8(int(fill.A) * int(cardA) / 255)
		fillRoundedRect(renderer, x, y, cardW, cardH, 14, fill)
		edge := glassSheetEdge()
		edge.A = uint8(int(edge.A) * int(cardA) / 255)
		strokeRoundedRect(renderer, x, y, cardW, cardH, 14, 1, edge)
	}
	picA := uint8(255 * alpha)
	frame := sdl.Color{R: 255, G: 255, B: 255, A: picA}
	f := shotThumbFrame
	fillRoundedRect(renderer, pic.X-f, pic.Y-f, pic.W+2*f, pic.H+2*f, 6, frame)
	_ = shotFX.tex.SetAlphaMod(picA)
	_ = renderer.Copy(shotFX.tex, nil, &pic)
	_ = shotFX.tex.SetAlphaMod(255)

	if font != nil && cardA > 0 {
		c := ColorTextPrimary()
		c.A = cardA
		renderText(renderer, config, font, caption, c, x+(cardW-capW)/2, endY+th+shotThumbFrame+6)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)

	if fly >= 1 && alpha >= 1 && path != "" {
		shotFX.rect = sdl.Rect{X: x, Y: y, W: cardW, H: cardH}
	}
}

// shotPreviewClick opens the saved picture when the card is clicked or
// tapped. Main thread, from the mouse handler (main.go).
func shotPreviewClick(config *Config, mx, my int32) bool {
	r := shotFX.rect
	if r.W <= 0 || r.H <= 0 || !pointInRect(mx, my, r) {
		return false
	}
	shotFX.mu.Lock()
	path := ""
	if shotFX.gen == shotFX.texGen {
		path = shotFX.path
	}
	shotFX.mu.Unlock()
	shotPreviewDrop()
	if path == "" {
		return true
	}
	openImageViewer(config, path)
	return true
}
