package main

import (
	"fmt"
	"path/filepath"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// --- Image Viewer ---

// imageViewerCloseBtn is the screen rect of the on-screen close button,
// populated each frame by renderImageViewer so the mouse handler can detect taps.
var imageViewerCloseBtn sdl.Rect

// closeImageViewer tears down the image viewer overlay (used by ESC and the
// on-screen close button).
func closeImageViewer() {
	if imageViewerTexture != nil {
		imageViewerTexture.Destroy()
		imageViewerTexture = nil
	}
	imageViewerPath = ""
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
}

func renderImageViewer(renderer *sdl.Renderer, config *Config, path string) {
	if imageViewerTexture == nil {
		surface, err := img.Load(path)
		if err != nil {
			showToast("Cannot load image: "+path, ToastError())
			return
		}
		imageViewerW = surface.W
		imageViewerH = surface.H
		imageViewerTexture, _ = renderer.CreateTextureFromSurface(surface)
		surface.Free()
	}

	if imageViewerTexture == nil {
		showToast("Cannot create texture from image", ToastError())
		return
	}

	texW, texH := imageViewerW, imageViewerH
	if texW == 0 || texH == 0 {
		return
	}

	renderer.SetDrawColor(6, 8, 14, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	scale := imageViewerZoom
	if float64(texW)*scale > float64(screenWidth-40) {
		scale = float64(screenWidth-40) / float64(texW)
	}
	if float64(texH)*scale > float64(screenHeight-40) {
		scale = float64(screenHeight-40) / float64(texH)
	}

	dw := int32(float64(texW) * scale)
	dh := int32(float64(texH) * scale)
	dx := (screenWidth-dw)/2 + imageViewerPanX
	dy := (screenHeight-dh)/2 + imageViewerPanY

	// soft glow behind the image
	glowCol := accentColor
	glowCol.A = 30
	fillRoundedRect(renderer, dx-14, dy-14, dw+28, dh+28, 12, glowCol)

	renderer.Copy(imageViewerTexture, nil, &sdl.Rect{X: dx, Y: dy, W: dw, H: dh})

	font, _ := getCachedFont(config, "small")
	if font != nil {
		// bottom-left info pill
		zoomText := fmt.Sprintf("%.0f%%", imageViewerZoom*100)
		infoText := filepath.Base(imageViewerPath) + "  ·  " + zoomText
		iw, _, _ := font.SizeUTF8(infoText)
		ipx := int32(20)
		ipy := screenHeight - 44
		fillRoundedRect(renderer, ipx, ipy, int32(iw)+24, 28, 14, WithAlpha(ColorSurfaceRaised, 220))
		renderer.SetDrawColor(255, 255, 255, 16)
		renderer.DrawRect(&sdl.Rect{X: ipx + 1, Y: ipy + 1, W: int32(iw) + 22, H: 1})
		renderText(renderer, config, font, infoText, sdl.Color{R: 220, G: 228, B: 242, A: 255}, ipx+12, ipy+7)

		// On-screen Close button (top-right)
		btnW, btnH := int32(120), int32(44)
		bx, by := screenWidth-btnW-20, int32(20)
		imageViewerCloseBtn = sdl.Rect{X: bx, Y: by, W: btnW, H: btnH}
		fillRoundedRect(renderer, bx+2, by+3, btnW, btnH, RadiusMD, ShadowFill(60))
		gradientRoundedRect(renderer, bx, by, btnW, btnH, RadiusMD, lighten(ColorButtonRaised, 18), darken(ColorButtonRaised, 12))
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 220)
		renderer.DrawRect(&sdl.Rect{X: bx, Y: by, W: btnW, H: btnH})
		renderText(renderer, config, font, "Close  ✕", ColorTextPrimary(), bx+16, by+12)
	}
}

func handleImageViewerInput(e *sdl.KeyboardEvent) {
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_RETURN:
		closeImageViewer()
	case sdl.K_UP:
		imageViewerZoom += 0.1
		if imageViewerZoom > 5.0 {
			imageViewerZoom = 5.0
		}
	case sdl.K_DOWN:
		imageViewerZoom -= 0.1
		if imageViewerZoom < 0.1 {
			imageViewerZoom = 0.1
		}
	case sdl.K_LEFT:
		imageViewerPanX += 20
	case sdl.K_RIGHT:
		imageViewerPanX -= 20
	}
}
