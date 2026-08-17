package main

import (
	"path/filepath"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Continue / Recently played panel ---

var (
	recentFocusIndex int
	recentMutex      sync.Mutex
)

func getRecentItems() []FavoriteItem {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	if favoritesStore.Recent == nil {
		return nil
	}
	out := make([]FavoriteItem, 0, len(favoritesStore.Recent))
	for _, it := range favoritesStore.Recent {
		out = append(out, it)
	}
	return out
}

func recentItemLabel(it FavoriteItem) string {
	return it.Label()
}

func recentItemIcon(it FavoriteItem) string {
	switch it.Type {
	case "video":
		return "video"
	case "iptv":
		return "tv"
	case "file":
		return "files"
	default:
		return "play"
	}
}

func renderRecent(renderer *sdl.Renderer, config *Config, element Element) {
	font, _ := getCachedFont(config, "small")
	titleFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if titleFont == nil {
		titleFont = font
	}

	listX := element.X
	listY := element.Y
	listW := getElementWidth(element, 1100)
	listH := getElementHeight(element, 190)
	if listW < 200 {
		listW = 200
	}
	if listH < 60 {
		listH = 60
	}

	// Card body
	drawPanel(renderer, listX, listY, listW, listH, WithAlpha(ColorSurfaceAlt, 230), accentColor)

	// Header: icon + "Continue"
	renderText(renderer, config, titleFont, "Continue", ColorTextPrimary(), listX+18, listY+14)

	items := getRecentItems()
	if len(items) == 0 {
		renderText(renderer, config, font, "Nothing recently played yet.", ColorTextTertiary(), listX+18, listY+60)
		return
	}

	// Up to 3 recent items in a horizontal row of cards.
	cardCount := len(items)
	if cardCount > 3 {
		cardCount = 3
	}
	gap := int32(12)
	cardW := (listW - 18*2 - gap*int32(cardCount-1)) / int32(cardCount)
	if cardW < 120 {
		cardW = 120
	}
	cardH := listH - 54
	cardY := listY + 42
	if cardH < 40 {
		cardH = 40
	}

	accent := sdl.Color{R: 110, G: 231, B: 255, A: 255}
	if int(accentColor.R)+int(accentColor.G) > 420 && int(accentColor.R) > int(accentColor.B) {
		accent = sdl.Color{R: 139, G: 124, B: 255, A: 255}
	}

	for i := 0; i < cardCount; i++ {
		cx := listX + 18 + int32(i)*(cardW+gap)
		selected := i == recentFocusIndex

		// Focus scale for the selected card
		scale := 1.0
		if selected {
			scale = 1.03
		}
		scaledW := int32(float64(cardW) * scale)
		scaledH := int32(float64(cardH) * scale)
		sx := cx + (cardW-scaledW)/2
		sy := cardY + (cardH-scaledH)/2

		// Selected: glow + cyan/violet outline
		if selected {
			glow := WithAlpha(accent, 28)
			fillRoundedRect(renderer, sx-6, sy-6, scaledW+12, scaledH+12, 12, glow)
		}
		fillRoundedRect(renderer, sx, sy, scaledW, scaledH, 10, ColorSurfaceRaised)
		borderCol := ColorBorderDefault
		if selected {
			borderCol = accent
		}
		renderer.SetDrawColor(borderCol.R, borderCol.G, borderCol.B, 255)
		renderer.DrawRect(&sdl.Rect{X: sx, Y: sy, W: scaledW, H: scaledH})

		// Icon chip
		icon := recentItemIcon(items[i])
		chipS := int32(28)
		chipX := sx + 12
		chipY := sy + 12
		fillRoundedRect(renderer, chipX, chipY, chipS, chipS, 8, WithAlpha(accent, 40))
		drawTileIcon(renderer, chipX+chipS/2, chipY+chipS/2, int32(float64(chipS)*0.55), icon, ColorTextPrimary())

		// Label (clipped to two lines)
		label := recentItemLabel(items[i])
		lw := scaledW - 28
		fontSize := int32(13)
		f, _ := getCachedFont(config, "small")
		if f != nil {
			w, _, _ := f.SizeUTF8(label)
			if int32(w) > lw {
				// Ellipsize
				runes := []rune(label)
				cur := ""
				for _, r := range runes {
					test := cur + string(r) + "…"
					tw, _, _ := f.SizeUTF8(test)
					if int32(tw) > lw {
						break
					}
					cur += string(r)
				}
				label = cur + "…"
			}
		}
		_ = fontSize
		tw, th, _ := f.SizeUTF8(label)
		renderText(renderer, config, f, label, ColorTextPrimary(), sx+(scaledW-int32(tw))/2, sy+scaledH-int32(th)-14)

		// Type tag under label
		typeName := map[string]string{
			"video": "Video",
			"iptv":  "Channel",
			"file":  filepath.Base(recentItemLabel(items[i])),
		}[items[i].Type]
		if typeName == "" {
			typeName = "Item"
		}
		tw2, _, _ := f.SizeUTF8(typeName)
		renderText(renderer, config, f, typeName, ColorTextTertiary(), sx+(scaledW-int32(tw2))/2, sy+scaledH-14)
	}

	if len(items) > cardCount {
		renderText(renderer, config, font, "▾ View all in Favorites", ColorTextTertiary(), listX+18, listY+listH-26)
	}
}

func handleRecentInput(e *sdl.KeyboardEvent, config *Config) {
	items := getRecentItems()
	max := len(items)
	if max > 3 {
		max = 3
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		if recentFocusIndex > 0 {
			recentFocusIndex--
		}
	case sdl.K_RIGHT:
		if recentFocusIndex < max-1 {
			recentFocusIndex++
		}
	case sdl.K_UP, sdl.K_DOWN:
		// not scrollable
	case sdl.K_RETURN, sdl.K_SPACE:
		if recentFocusIndex >= 0 && recentFocusIndex < len(items) {
			items[recentFocusIndex].Play(config)
		}
	}
}

func handleRecentMouseClick(mx, my int32, config *Config) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	var elem *Element
	for i := range config.Scenes[currentSceneIndex].Elements {
		if config.Scenes[currentSceneIndex].Elements[i].Type == "recent" {
			elem = &config.Scenes[currentSceneIndex].Elements[i]
			break
		}
	}
	if elem == nil {
		return
	}
	listX := elem.X
	listY := elem.Y
	listW := getElementWidth(*elem, 1100)
	listH := getElementHeight(*elem, 190)
	if listW < 200 {
		listW = 200
	}
	if listH < 60 {
		listH = 60
	}
	items := getRecentItems()
	cardCount := len(items)
	if cardCount > 3 {
		cardCount = 3
	}
	if cardCount <= 0 {
		return
	}
	gap := int32(12)
	cardW := (listW - 18*2 - gap*int32(cardCount-1)) / int32(cardCount)
	if cardW < 120 {
		cardW = 120
	}
	cardH := listH - 54
	cardY := listY + 42
	if cardH < 40 {
		cardH = 40
	}
	for i := 0; i < cardCount; i++ {
		cx := listX + 18 + int32(i)*(cardW+gap)
		selected := i == recentFocusIndex
		scale := 1.0
		if selected {
			scale = 1.03
		}
		scaledW := int32(float64(cardW) * scale)
		scaledH := int32(float64(cardH) * scale)
		sx := cx + (cardW-scaledW)/2
		sy := cardY + (cardH-scaledH)/2
		if mx >= sx && mx <= sx+scaledW && my >= sy && my <= sy+scaledH {
			recentFocusIndex = i
			if i < len(items) {
				items[i].Play(config)
			}
			return
		}
	}
}