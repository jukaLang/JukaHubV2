package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// --- Image Viewer ---

// imageViewerCloseBtn is the screen rect of the on-screen close button,
// populated each frame by renderImageViewer so the mouse handler can detect taps.
var imageViewerCloseBtn sdl.Rect

// imageViewerFiles is the ordered list of images in the folder the viewer was
// opened from, so Left/Right (or the controller bumpers) can walk them without
// going back to the file grid.
var (
	imageViewerFiles []string
	imageViewerIndex int = -1
)

// imageViewerRemote marks the viewer as showing a downloaded image (a chat
// preview URL) instead of a file on disk.
var imageViewerRemote bool

// openImageViewerURL shows an image that was fetched over the network, such as
// a link or attachment pasted into chat. The texture comes from the chat image
// cache, so the picture is already there when its thumbnail was drawn.
func openImageViewerURL(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	imageViewerFiles = nil
	imageViewerIndex = -1
	imageViewerRemote = true
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
	imageViewerPath = url
	if imageViewerTexture != nil {
		imageViewerTexture.Destroy()
		imageViewerTexture = nil
	}
}

// isViewableImagePath reports whether the image decoder handles the file.
func isViewableImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".bmp":
		return true
	}
	return false
}

// openImageViewer shows path and collects the other images in the current File
// Explorer folder as a browsable list. Non-Files callers (for example a chat
// attachment) get a one-image list.
func openImageViewer(config *Config, path string) {
	path = filepath.Clean(path)
	imageViewerFiles = nil
	imageViewerIndex = -1
	if config != nil {
		if entries, ok := config.Variables.Custom["fe_entries"].([]FileEntry); ok {
			for _, entry := range entries {
				if entry.IsDir || !isViewableImagePath(entry.Name) {
					continue
				}
				resolved, err := resolveExplorerEntry(config, entry.Path)
				if err != nil {
					continue
				}
				imageViewerFiles = append(imageViewerFiles, resolved)
				if filepath.Clean(resolved) == path && imageViewerIndex < 0 {
					imageViewerIndex = len(imageViewerFiles) - 1
				}
			}
		}
	}
	if imageViewerIndex < 0 {
		imageViewerFiles = []string{path}
		imageViewerIndex = 0
	}
	imageViewerShow(path)
}

// imageViewerShow swaps the displayed image without closing the overlay.
func imageViewerShow(path string) {
	if imageViewerTexture != nil {
		imageViewerTexture.Destroy()
		imageViewerTexture = nil
	}
	imageViewerRemote = false
	imageViewerPath = path
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
}

// imageViewerStep moves through the folder's images, wrapping at both ends.
func imageViewerStep(delta int) {
	if len(imageViewerFiles) == 0 || delta == 0 {
		return
	}
	if imageViewerIndex < 0 || imageViewerIndex >= len(imageViewerFiles) {
		imageViewerIndex = 0
	}
	imageViewerIndex = (imageViewerIndex + delta + len(imageViewerFiles)) % len(imageViewerFiles)
	imageViewerShow(imageViewerFiles[imageViewerIndex])
}

// closeImageViewer tears down the image viewer overlay (used by ESC, a click and
// the on-screen close button).
func closeImageViewer() {
	if imageViewerTexture != nil {
		imageViewerTexture.Destroy()
		imageViewerTexture = nil
	}
	imageViewerPath = ""
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
	imageViewerFiles = nil
	imageViewerIndex = -1
	imageViewerRemote = false
}

func renderImageViewer(renderer *sdl.Renderer, config *Config, path string) {
	texture := imageViewerTexture
	texW, texH := imageViewerW, imageViewerH

	if imageViewerRemote {
		// The picture is cached by the chat renderer; while it downloads we
		// draw the empty viewer rather than closing the overlay.
		texture = getChatImageTexture(renderer, path)
		if texture == nil {
			renderImageViewerBackdrop(renderer, config, "Loading image…")
			return
		}
		_, _, qw, qh, err := texture.Query()
		if err != nil || qw == 0 || qh == 0 {
			showToast("Cannot display image", ToastError())
			closeImageViewer()
			return
		}
		texW, texH = qw, qh
	} else if texture == nil {
		surface, err := img.Load(path)
		if err != nil {
			showToast("Cannot load image: "+filepath.Base(path), ToastError())
			// Leaving the path set would retry (and toast) every frame.
			closeImageViewer()
			return
		}
		imageViewerW = surface.W
		imageViewerH = surface.H
		imageViewerTexture, _ = renderer.CreateTextureFromSurface(surface)
		surface.Free()
		texture = imageViewerTexture
		texW, texH = imageViewerW, imageViewerH
	}

	if texture == nil {
		showToast("Cannot create texture from image", ToastError())
		return
	}

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

	renderer.Copy(texture, nil, &sdl.Rect{X: dx, Y: dy, W: dw, H: dh})

	font, _ := getCachedFont(config, "small")
	if font != nil {
		// bottom-left info pill
		zoomText := fmt.Sprintf("%.0f%%", imageViewerZoom*100)
		label := filepath.Base(imageViewerPath)
		if imageViewerRemote {
			// A URL's basename is often meaningless, so name the source instead.
			label = "Chat image"
		}
		infoText := label + "  ·  " + zoomText
		if len(imageViewerFiles) > 1 && imageViewerIndex >= 0 {
			infoText = fmt.Sprintf("%s  ·  %d/%d  ·  Left/Right browse", label, imageViewerIndex+1, len(imageViewerFiles))
		}
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
		// Use × (U+00D7), which Inter contains — ✕ (U+2715) is not in Inter and
		// would render as a missing-glyph box.
		imageViewerCloseBtn = UIButton(renderer, config, sdl.Rect{X: bx, Y: by, W: btnW, H: btnH}, "Close  X", UIButtonStyle{
			Variant: UIButtonSecondary,
			Font:    font,
			Radius:  RadiusMD,
		}, UIButtonState{})
	}
}

// renderImageViewerBackdrop paints the empty viewer (dark page, status text and
// the Close button) while a remote image is still downloading.
func renderImageViewerBackdrop(renderer *sdl.Renderer, config *Config, msg string) {
	renderer.SetDrawColor(6, 8, 14, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	w, h, _ := font.SizeUTF8(msg)
	renderText(renderer, config, font, msg, ColorTextSecondary(),
		(screenWidth-int32(w))/2, (screenHeight-int32(h))/2)

	btnW, btnH := int32(120), int32(44)
	bx, by := screenWidth-btnW-20, int32(20)
	imageViewerCloseBtn = UIButton(renderer, config, sdl.Rect{X: bx, Y: by, W: btnW, H: btnH}, "Close  X", UIButtonStyle{
		Variant: UIButtonSecondary,
		Font:    font,
		Radius:  RadiusMD,
	}, UIButtonState{})
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
		imageViewerStep(-1)
	case sdl.K_RIGHT:
		imageViewerStep(1)
	}
}
