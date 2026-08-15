package main

import (
	"fmt"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// --- YouTube Shorts ---

var (
	shortsList      []VideoInfo
	shortsMutex     sync.Mutex
	currentShortIdx int = -1
	shortsScrollY   int32
)

func fetchYouTubeShorts(config *Config, resultVar string, vars map[string]interface{}) {
	cmd := `yt-dlp --flat-playlist --dump-single-json --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors "ytsearch10:shorts"`
	executeYouTubeSearch(config, cmd, resultVar, vars)
}

func renderShortsGrid(renderer *sdl.Renderer, config *Config, element Element) {
	shortsMutex.Lock()
	videos := shortsList
	shortsMutex.Unlock()

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		return
	}

	thumbWidth := int32(160)
	thumbHeight := int32(280)
	cellWidth := thumbWidth + 20
	cellHeight := thumbHeight + 50
	cols := 3

	elemW := getElementWidth(element, 1080)
	elemH := getElementHeight(element, 500)

	totalRows := (len(videos) + cols - 1) / cols
	if totalRows < 1 {
		totalRows = 1
	}
	maxVisibleRows := int((elemH - 20) / cellHeight)
	if maxVisibleRows < 1 {
		maxVisibleRows = 1
	}

	targetScrollY := int32(0)
	if currentShortIdx >= 0 {
		focusedRow := int32(currentShortIdx) / int32(cols)
		if int(focusedRow) >= maxVisibleRows {
			targetScrollY = (focusedRow - int32(maxVisibleRows) + 1) * (cellHeight + 10)
		}
	}
	if targetScrollY < 0 {
		targetScrollY = 0
	}
	maxScroll := int32(0)
	if totalRows > maxVisibleRows {
		maxScroll = int32(totalRows-maxVisibleRows) * (cellHeight + 10)
		if maxScroll < 0 {
			maxScroll = 0
		}
	}
	if targetScrollY > maxScroll {
		targetScrollY = maxScroll
	}
	shortsScrollY = int32(float64(shortsScrollY) + (float64(targetScrollY-shortsScrollY) * 0.3))
	if abs(int(shortsScrollY-targetScrollY)) < 1 {
		shortsScrollY = targetScrollY
	}

	drawPanel(renderer, element.X, element.Y, elemW, elemH, sdl.Color{R: 16, G: 19, B: 26, A: 220}, accentColor)

	for i, vid := range videos {
		col := int32(i) % int32(cols)
		row := int32(i) / int32(cols)
		xPos := element.X + 20 + col*cellWidth
		yPos := element.Y + 10 + row*(cellHeight+10) - shortsScrollY

		if yPos+cellHeight < element.Y || yPos > element.Y+elemH {
			continue
		}

		if i == currentShortIdx {
			fillRoundedRect(renderer, xPos-4, yPos-4, cellWidth+8, cellHeight+8, 12, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 140})
		}

		// card shadow
		fillRoundedRect(renderer, xPos+3, yPos+4, thumbWidth, thumbHeight, 10, sdl.Color{R: 0, G: 0, B: 0, A: 60})
		// card background
		fillRoundedRect(renderer, xPos, yPos, thumbWidth, thumbHeight, 10, sdl.Color{R: 22, G: 26, B: 36, A: 255})

	thumbLoaded := false
	if vid.Thumbnail != "" {
		tex := loadThumbnail(renderer, vid.Thumbnail)
		if tex != nil {
			renderer.Copy(tex, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
			thumbLoaded = true
		}
	}
	if !thumbLoaded && len(vid.Thumbnails) > 0 {
		tex := loadThumbnailFromURLs(renderer, vid.Thumbnails)
		if tex != nil {
			renderer.Copy(tex, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
			thumbLoaded = true
		}
	}
	if !thumbLoaded && placeholderTexture != nil {
		renderer.Copy(placeholderTexture, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
	}

		// title overlay at bottom of thumbnail
		title := vid.Title
		if len(title) > 24 {
			title = title[:21] + "..."
		}
		titleY := yPos + thumbHeight - 30
		fillRoundedRect(renderer, xPos, titleY, thumbWidth, 30, 0, sdl.Color{R: 0, G: 0, B: 0, A: 140})
		renderText(renderer, config, font, title, sdl.Color{R: 245, G: 248, B: 255, A: 255}, xPos+8, titleY+6)

		dur := fmt.Sprintf("%d:%02d", int(vid.Duration)/60, int(vid.Duration)%60)
		bw, bh := int32(50), int32(20)
		bx := xPos + thumbWidth - bw - 6
		by := yPos + thumbHeight - 26
		fillRoundedRect(renderer, bx, by, bw, bh, 6, sdl.Color{R: 0, G: 0, B: 0, A: 160})
		renderText(renderer, config, font, dur, sdl.Color{R: 255, G: 255, B: 255, A: 255}, bx+6, by+3)
	}
}

func handleShortsInput(e *sdl.KeyboardEvent, config *Config) {
	shortsMutex.Lock()
	videos := shortsList
	shortsMutex.Unlock()

	switch e.Keysym.Sym {
	case sdl.K_UP:
		if currentShortIdx > 0 {
			currentShortIdx--
		}
	case sdl.K_DOWN:
		if currentShortIdx < len(videos)-1 {
			currentShortIdx++
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if currentShortIdx >= 0 && currentShortIdx < len(videos) {
			playVideoURL(config, videos[currentShortIdx].GetURL())
		}
	case sdl.K_ESCAPE:
		for _, scene := range config.Scenes {
			if scene.Name == "Main" {
				changeSceneTo(config, findSceneIndex(config, "Main"))
				break
			}
		}
	}
}
