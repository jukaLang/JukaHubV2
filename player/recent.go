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

	// Use home layout rect if available for responsive positioning.
	var listX, listY, listW, listH int32
	if rect, ok := homeRecentRect(); ok {
		listX, listY, listW, listH = rect.X, rect.Y, rect.W, rect.H
	} else {
		listX = element.X
		listY = element.Y
		listW = getElementWidth(element, 1100)
		listH = getElementHeight(element, 190)
		if listW < 200 {
			listW = 200
		}
		if listH < 60 {
			listH = 60
		}
	}

	// Opaque panel with subtle border using home layout tokens.
	// Clip panel body to prevent overflow
	renderWithClip(renderer, listX, listY, listW, listH, func(r *sdl.Renderer) {
		fillRoundedRect(r, listX, listY, listW, listH, RadiusLG, HomeCardColor())
		// Hairline border.
		r.SetDrawColor(HomeBorderColor().R, HomeBorderColor().G, HomeBorderColor().B, 120)
		r.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + 1, W: listW - 2, H: 1})
		r.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + 1, W: 1, H: listH - 2})
		r.SetDrawColor(HomeBorderColor().R, HomeBorderColor().G, HomeBorderColor().B, 60)
		r.DrawRect(&sdl.Rect{X: listX + 1, Y: listY + listH - 2, W: listW - 2, H: 1})
		r.DrawRect(&sdl.Rect{X: listX + listW - 2, Y: listY + 1, W: 1, H: listH - 2})
	})

	// Header: "Continue" text.
	renderText(renderer, config, titleFont, "Continue", ColorTextPrimary(), listX+RecentPadding, listY+RecentPadding)

	items := getRecentItems()
	if len(items) == 0 {
		// Compact empty state: icon + message + action hint.
		iconSize := EmptyIconSize
		iconCX := listX + listW/2
		iconCY := listY + listH/2 + SpaceXS
		drawTileIcon(renderer, iconCX, iconCY, iconSize, "media", ColorTextSecondary())
		msg1 := "Nothing played recently"
		msg2 := "Open Media to start watching or listening"
		f, _ := getCachedFont(config, "small")
		if f == nil {
			f = font
		}
		mw1, _, _ := f.SizeUTF8(msg1)
		mw2, _, _ := f.SizeUTF8(msg2)
		renderText(renderer, config, f, msg1, ColorTextPrimary(), iconCX-int32(mw1)/2, iconCY+EmptyMsgOffset1)
		renderText(renderer, config, f, msg2, ColorTextSecondary(), iconCX-int32(mw2)/2, iconCY+EmptyMsgOffset2)
		if homeLayoutActive {
			focusCol := focusColorForAccent(accentColor)
			strokeRoundedRect(renderer, listX, listY, listW, listH, RadiusLG, FocusRing, WithAlpha(focusCol, 180))
		}
		return
	}

	// Up to 3 recent items in a compact horizontal row.
	cardCount := len(items)
	if cardCount > 3 {
		cardCount = 3
	}
	gap := RecentCardGap
	cardW := (listW - 2*CardAreaPad - gap*int32(cardCount-1)) / int32(cardCount)
	if cardW < MinCardWidth {
		cardW = MinCardWidth
	}
	cardH := listH - CardAreaOffset
	cardY := listY + CardYOffset
	if cardH < MinCardHeight {
		cardH = MinCardHeight
	}

	accent := focusColorForAccent(accentColor)

	for i := 0; i < cardCount; i++ {
		cx := listX + 18 + int32(i)*(cardW+gap)
		selected := i == recentFocusIndex

		sx := cx
		sy := cardY
		sw := cardW
		sh := cardH

		// Focus ring.
		if selected {
			strokeRoundedRect(renderer, sx-HoverRing, sy-HoverRing, sw+2*HoverRing, sh+2*HoverRing, RadiusMD, HoverRing, accent)
		}
		fillRoundedRect(renderer, sx, sy, sw, sh, RadiusMD, ColorCard)
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
		renderer.DrawRect(&sdl.Rect{X: sx, Y: sy, W: sw, H: sh})

		// Icon chip
		icon := recentItemIcon(items[i])
		chipS := ChipSize
		chipX := sx + SpaceSM
		chipY := sy + SpaceSM
		fillRoundedRect(renderer, chipX, chipY, chipS, chipS, RadiusSM, WithAlpha(ColorSurfaceAlt, 200))
		drawTileIcon(renderer, chipX+chipS/2, chipY+chipS/2, int32(float64(chipS)*0.55), icon, ColorTextPrimary())

		// Label (clipped to a single line with ellipsis)
		label := recentItemLabel(items[i])
		lw := sw - LabelPadding
		f, _ := getCachedFont(config, "small")
		if f == nil {
			f = font
		}
		w, _, _ := f.SizeUTF8(label)
		if int32(w) > lw {
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
		tw, th, _ := f.SizeUTF8(label)
		renderText(renderer, config, f, label, ColorTextPrimary(), sx+SpaceSM+(lw-int32(tw))/2, sy+sh-int32(th)-LabelBottomPad)

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
		renderText(renderer, config, f, typeName, ColorTextSecondary(), sx+SpaceSM+(lw-int32(tw2))/2, sy+sh-LabelBottomPad)
	}

	if len(items) > cardCount {
		renderText(renderer, config, font, "View all in Favorites", ColorTextSecondary(), listX+RecentPadding, listY+listH-SpaceLG)
	}
}

func handleRecentInput(e *sdl.KeyboardEvent, config *Config) {
	items := getRecentItems()
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		if homeLayoutActive {
			moveHomeSelection(config, -1, 0)
		} else if recentFocusIndex > 0 {
			recentFocusIndex--
		}
	case sdl.K_RIGHT:
		if homeLayoutActive {
			moveHomeSelection(config, 1, 0)
		} else if recentFocusIndex < len(items)-1 && recentFocusIndex < 2 {
			recentFocusIndex++
		}
	case sdl.K_UP:
		if homeLayoutActive {
			moveHomeSelection(config, 0, -1)
		} else {
			moveSelection(config, -1)
		}
	case sdl.K_DOWN:
		if homeLayoutActive {
			moveHomeSelection(config, 0, 1)
		} else {
			moveSelection(config, 1)
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if len(items) == 0 {
			if idx := findSceneIndex(config, "Tube"); idx >= 0 {
				changeSceneTo(config, idx)
			}
			return
		}
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
	if rect, ok := homeRecentRect(); ok {
		listX, listY, listW, listH = rect.X, rect.Y, rect.W, rect.H
	}

	items := getRecentItems()
	if len(items) == 0 {
		// Empty panel: any click opens Media (Tube).
		if mx >= listX && mx <= listX+listW && my >= listY && my <= listY+listH {
			if idx := findSceneIndex(config, "Tube"); idx >= 0 {
				changeSceneTo(config, idx)
			}
		}
		return
	}

	cardCount := len(items)
	if cardCount > 3 {
		cardCount = 3
	}
	gap := int32(14)
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
		sx := cx
		sy := cardY
		sw := cardW
		sh := cardH
		if mx >= sx && mx <= sx+sw && my >= sy && my <= sy+sh {
			recentFocusIndex = i
			if i < len(items) {
				items[i].Play(config)
			}
			return
		}
	}
}
