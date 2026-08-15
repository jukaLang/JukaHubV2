package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Image Viewer ---

func renderImageViewer(renderer *sdl.Renderer, config *Config, path string) {
	if imageViewerTexture == nil {
		surface, err := sdl.LoadBMP(path)
		if err != nil {
			if strings.HasSuffix(strings.ToLower(path), ".png") ||
				strings.HasSuffix(strings.ToLower(path), ".jpg") ||
				strings.HasSuffix(strings.ToLower(path), ".jpeg") {
				showToast("Image format not supported (need SDL2_image)", sdl.Color{R: 230, G: 80, B: 80, A: 255})
			} else {
				showToast("Cannot load image: "+path, sdl.Color{R: 230, G: 80, B: 80, A: 255})
			}
			return
		}
		imageViewerW = surface.W
		imageViewerH = surface.H
		imageViewerTexture, _ = renderer.CreateTextureFromSurface(surface)
		surface.Free()
	}

	if imageViewerTexture == nil {
		showToast("Cannot create texture from image", sdl.Color{R: 230, G: 80, B: 80, A: 255})
		return
	}

	texW, texH := imageViewerW, imageViewerH
	if texW == 0 || texH == 0 {
		return
	}

	renderer.SetDrawColor(0, 0, 0, 255)
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

	renderer.Copy(imageViewerTexture, nil, &sdl.Rect{X: dx, Y: dy, W: dw, H: dh})

	font, _ := getCachedFont(config, "small")
	if font != nil {
		zoomText := fmt.Sprintf("%.0f%%", imageViewerZoom*100)
		renderText(renderer, config, font, filepath.Base(imageViewerPath)+"  "+zoomText, sdl.Color{R: 200, G: 210, B: 230, A: 255}, 20, screenHeight-40)
	}
}

func handleImageViewerInput(e *sdl.KeyboardEvent) {
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_RETURN:
		if imageViewerTexture != nil {
			imageViewerTexture.Destroy()
			imageViewerTexture = nil
		}
		imageViewerPath = ""
		imageViewerZoom = 1.0
		imageViewerPanX = 0
		imageViewerPanY = 0
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
