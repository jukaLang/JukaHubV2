package main

import (
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// --- YouTube Shorts ---

var (
	shortsList      []VideoInfo
	shortsMutex     sync.Mutex
	currentShortIdx int = -1
	shortsScrollY   int32
	shortsCols      = 1

	// shortsAutoplay keeps the feed running: when a short ends the next one
	// starts, until the list runs out or the user opens something else.
	shortsAutoplay bool
	// shortsAutoAdvance is raised by the player on a natural end of stream.
	shortsAutoAdvance bool
	// shortsPendingStart waits for the feed to download before playing the
	// first short, so the combo does not open an empty grid.
	shortsPendingStart bool
)

type shortsGridLayout struct {
	thumbWidth  int32
	thumbHeight int32
	cellWidth   int32
	cellHeight  int32
	gap         int32
	cols        int
	originX     int32
}

func newShortsGridLayout(element Element, elemW int32) shortsGridLayout {
	layout := shortsGridLayout{
		thumbWidth:  160,
		thumbHeight: 250,
		cellWidth:   180,
		cellHeight:  298,
		gap:         16,
		cols:        1,
	}
	if elemW <= 0 {
		return layout
	}
	layout.cols = int((elemW + layout.gap) / (layout.cellWidth + layout.gap))
	if layout.cols < 1 {
		layout.cols = 1
	}
	gridWidth := int32(layout.cols)*layout.cellWidth + int32(layout.cols-1)*layout.gap
	layout.originX = element.X + (elemW-gridWidth)/2 + (layout.cellWidth-layout.thumbWidth)/2
	return layout
}

func shortsGridCellRect(element Element, layout shortsGridLayout, index int, scrollY int32) sdl.Rect {
	col := int32(index % layout.cols)
	row := int32(index / layout.cols)
	return sdl.Rect{
		X: element.X + (layout.originX - element.X - (layout.cellWidth-layout.thumbWidth)/2) + col*(layout.cellWidth+layout.gap),
		Y: element.Y + 10 + row*(layout.cellHeight+layout.gap) - scrollY,
		W: layout.cellWidth,
		H: layout.cellHeight,
	}
}

func fetchYouTubeShorts(config *Config, resultVar string, vars map[string]interface{}) {
	cmd := `yt-dlp --flat-playlist --dump-single-json --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors "ytsearch10:shorts"` + " " + ytDlpExtraArgs(config)
	executeYouTubeSearch(config, cmd, resultVar, vars)
}

// prefetchShortsFocused warms the highlighted short and the ones after it, so
// Play does not wait for yt-dlp, matching the results grid.
func prefetchShortsFocused(config *Config, videos []VideoInfo) {
	prefetchVideos(config, videos, currentShortIdx)
}

// shortsVideos returns the feed published by the search worker.
func shortsVideos(config *Config) []VideoInfo {
	if config == nil {
		return nil
	}
	videos, _ := config.Variables.Custom["shorts_list"].([]VideoInfo)
	return videos
}

// nextShortIndex is the feed step after a finished short. It reports false when
// the list is exhausted, which ends autoplay.
func nextShortIndex(current, total int) (int, bool) {
	next := current + 1
	if next < 0 || next >= total {
		return 0, false
	}
	return next, true
}

// openShortsFeed is the Media shortcut: jump to the Shorts feed, pre-load it
// and start playing the first short. Playback then continues on its own.
func openShortsFeed(config *Config) bool {
	idx := sceneIndexExact(config, "Shorts")
	if idx < 0 {
		ShowToast("This build has no Shorts feed", ToastKindWarning)
		return false
	}
	shortsAutoplay = true
	if currentSceneIndex != idx {
		changeSceneTo(config, idx)
	}
	videos := shortsVideos(config)
	if len(videos) == 0 {
		// Entering the scene already starts the download; play as soon as the
		// list lands instead of leaving a blank grid behind the combo.
		shortsPendingStart = true
		ShowToast("Loading Shorts…", ToastKindInfo)
		return true
	}
	start := currentShortIdx
	if start < 0 || start >= len(videos) {
		start = 0
	}
	startShortAt(config, start)
	return true
}

// startShortAt plays one short and pre-loads the ones after it, so the
// auto-advance does not wait for yt-dlp between videos.
func startShortAt(config *Config, index int) {
	videos := shortsVideos(config)
	if index < 0 || index >= len(videos) {
		return
	}
	currentShortIdx = index
	prefetchShortsFocused(config, videos)
	playVideoInfo(config, videos[index])
	shortsAutoplay = true
}

// drainShortsAutoplay runs on the main loop: it starts a feed that was waiting
// for its list and advances after a short ends. Everything here touches the
// player on the event thread, like the rest of the scene code.
func drainShortsAutoplay(config *Config) {
	if shortsPendingStart {
		// Waiting for the feed only makes sense while the Shorts screen is up:
		// leaving it must not start a video behind whatever the user opened.
		if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
			config.Scenes[currentSceneIndex].Name != "Shorts" {
			shortsPendingStart = false
			shortsAutoplay = false
			return
		}
		videos := shortsVideos(config)
		if len(videos) == 0 {
			return
		}
		shortsPendingStart = false
		startShortAt(config, 0)
		return
	}
	if !shortsAutoAdvance {
		return
	}
	shortsAutoAdvance = false
	if !shortsAutoplay {
		return
	}
	videos := shortsVideos(config)
	next, ok := nextShortIndex(currentShortIdx, len(videos))
	if !ok {
		shortsAutoplay = false
		ShowToast("That was the last Short", ToastKindInfo)
		return
	}
	startShortAt(config, next)
}

func renderShortsGrid(renderer *sdl.Renderer, config *Config, element Element) {
	// Read from the config variable (published by executeYouTubeSearch).
	shortsMutex.Lock()
	if vs, ok := config.Variables.Custom["shorts_list"].([]VideoInfo); ok {
		shortsList = vs
	}
	videos := shortsList
	shortsMutex.Unlock()

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		return
	}

	elemW := getElementWidth(element, 1080)
	elemH := getElementHeight(element, 500)
	layout := newShortsGridLayout(element, elemW)
	shortsCols = layout.cols
	thumbWidth, thumbHeight := layout.thumbWidth, layout.thumbHeight
	cellWidth, cellHeight, cols := layout.cellWidth, layout.cellHeight, layout.cols
	rowGap := layout.gap

	totalRows := (len(videos) + cols - 1) / cols
	if totalRows < 1 {
		totalRows = 1
	}
	maxVisibleRows := int((elemH - 20 + rowGap) / (cellHeight + rowGap))
	if maxVisibleRows < 1 {
		maxVisibleRows = 1
	}

	targetScrollY := int32(0)
	if currentShortIdx >= 0 {
		focusedRow := int32(currentShortIdx) / int32(cols)
		if int(focusedRow) >= maxVisibleRows {
			targetScrollY = (focusedRow - int32(maxVisibleRows) + 1) * (cellHeight + rowGap)
		}
	}
	if targetScrollY < 0 {
		targetScrollY = 0
	}
	maxScroll := int32(0)
	if totalRows > maxVisibleRows {
		maxScroll = int32(totalRows-maxVisibleRows) * (cellHeight + rowGap)
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

	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(220), accentColor)
	fontSmall, _ := getCachedFont(config, "small")
	if fontSmall == nil {
		fontSmall = font
	}

	renderWithClip(renderer, element.X, element.Y, elemW, elemH, func(r *sdl.Renderer) {
		for i, vid := range videos {
			cell := shortsGridCellRect(element, layout, i, shortsScrollY)
			cellX, yPos := cell.X, cell.Y
			xPos := cellX + (cellWidth-thumbWidth)/2

			if yPos+cellHeight < element.Y || yPos > element.Y+elemH {
				continue
			}

			if i == currentShortIdx {
				fillRoundedRect(r, cellX-3, yPos-3, cellWidth+6, cellHeight+6, 13, WithAlpha(accentColor, 90))
			}

			fillRoundedRect(r, cellX, yPos, cellWidth, cellHeight, 12, ColorSurfaceAlt)
			strokeRoundedRect(r, cellX, yPos, cellWidth, cellHeight, 12, 1, ColorBorder)

			thumbLoaded := false
			if vid.Thumbnail != "" {
				tex := loadThumbnail(r, vid.Thumbnail)
				if tex != nil {
					r.Copy(tex, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
					thumbLoaded = true
				}
			}
			if !thumbLoaded && len(vid.Thumbnails) > 0 {
				tex := loadThumbnailFromURLs(r, vid.Thumbnails)
				if tex != nil {
					r.Copy(tex, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
					thumbLoaded = true
				}
			}
			if !thumbLoaded && placeholderTexture != nil {
				r.Copy(placeholderTexture, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
			}

			drawDurationBadge(r, config, fontSmall, vid.Duration, xPos+thumbWidth-58, yPos+thumbHeight-23, 52, 18)
			// Same preload marker as the results list: bolt plus yellow title
			// once the stream is resolved, so Play starts immediately.
			titleX, titleW := cellX+8, cellWidth-16
			titleColor := ColorTextPrimary()
			if videoStreamPreloaded(vid.GetURL()) {
				const boltSize = 12
				titleColor = ColorWarning
				drawPreloadBolt(r, titleX, yPos+thumbHeight+14, boltSize, ColorWarning)
				titleX += boltSize + 5
				titleW -= boltSize + 5
			}
			title := ellipsize(font, vid.Title, titleW)
			renderText(r, config, font, title, titleColor, titleX, yPos+thumbHeight+12)
		}
	})
}

func handleShortsInput(e *sdl.KeyboardEvent, config *Config) {
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		for _, element := range config.Scenes[currentSceneIndex].Elements {
			if element.Type == "shortslist" {
				shortsCols = newShortsGridLayout(element, getElementWidth(element, 1080)).cols
				break
			}
		}
	}
	shortsMutex.Lock()
	videos := shortsList
	shortsMutex.Unlock()

	switch e.Keysym.Sym {
	case sdl.K_UP:
		if currentShortIdx >= 0 {
			if next := currentShortIdx - shortsCols; next >= 0 {
				currentShortIdx = next
			}
		}
		prefetchShortsFocused(config, videos)
	case sdl.K_DOWN:
		if currentShortIdx < 0 && len(videos) > 0 {
			currentShortIdx = 0
		} else if currentShortIdx >= 0 {
			if next := currentShortIdx + shortsCols; next < len(videos) {
				currentShortIdx = next
			}
		}
		prefetchShortsFocused(config, videos)
	case sdl.K_LEFT:
		if currentShortIdx > 0 {
			currentShortIdx--
		}
		prefetchShortsFocused(config, videos)
	case sdl.K_RIGHT:
		if currentShortIdx < len(videos)-1 {
			currentShortIdx++
		}
		prefetchShortsFocused(config, videos)
	case sdl.K_RETURN, sdl.K_SPACE:
		if currentShortIdx >= 0 && currentShortIdx < len(videos) {
			// Playing from the grid starts the feed, so the next short follows.
			startShortAt(config, currentShortIdx)
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
