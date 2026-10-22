package main

import (
	"path/filepath"
	"sort"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Continue / Recently played panel ---

var (
	recentFocusIndex int
	recentMutex      sync.Mutex
	emptyActionRect  sdl.Rect // "Open Media" action button in empty Continue panel
)

// gameRecentItem adapts a recorded game launch for the Continue list. The data
// stays a recentGame rather than a resolved launcher: a system's emulator can be
// reinstalled or re-pointed between sessions, so the launcher is resolved again
// at play time (see playRecentGame).
func gameRecentItem(g recentGame) FavoriteItem {
	return FavoriteItem{Type: "game", Data: g, Timestamp: g.PlayedAt}
}

// getRecentItems is the Continue list: game recents and media recents in one
// timeline, newest first. It used to read only the media favourites store, so a
// device that had played ten games still showed "Nothing played recently".
func getRecentItems() []FavoriteItem {
	favoritesMutex.Lock()
	out := append([]FavoriteItem{}, favoritesStore.Recent...)
	favoritesMutex.Unlock()

	for _, g := range recentGames() {
		out = append(out, gameRecentItem(g))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out
}

// playRecentGame relaunches a game from Continue. Failures are toasts instead
// of silent no-ops: the ROM may have been deleted or the emulator moved since
// the card was last scanned.
func playRecentGame(g recentGame) {
	if g.Path == "" || !regularFile(g.Path) {
		ShowToast("ROM is missing: "+filepath.Base(g.Path), ToastKindError)
		return
	}
	launcher := resolveGameLauncher(g.System)
	if launcher == "" {
		ShowToast("No emulator found for "+g.System+" — add one on the Emulators screen", ToastKindWarning)
		return
	}
	game := libraryGame{Path: g.Path, System: g.System, Title: g.Title}
	if err := launchLibraryGame(game, launcher); err != nil {
		ShowToast("Cannot launch "+g.Title+": "+err.Error(), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("Playing "+g.Title, ToastKindSuccess)
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
	case "game":
		return "game"
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
		// Compact empty state: icon + message + action button.
		iconSize := EmptyIconSize
		iconCX := listX + listW/2
		iconCY := listY + listH/2 + SpaceXS
		drawTileIcon(renderer, iconCX, iconCY, iconSize, "media", ColorTextSecondary(), HomeCardColor())
		msg1 := "Nothing played recently"
		msg2 := "Play a game or open Media to start"
		f, _ := getCachedFont(config, "small")
		if f == nil {
			f = font
		}
		mw1, _, _ := f.SizeUTF8(msg1)
		mw2, _, _ := f.SizeUTF8(msg2)
		renderText(renderer, config, f, msg1, ColorTextPrimary(), iconCX-int32(mw1)/2, iconCY+EmptyMsgOffset1)
		renderText(renderer, config, f, msg2, ColorTextSecondary(), iconCX-int32(mw2)/2, iconCY+EmptyMsgOffset2)

		// Action button.
		btnLabel := "Open Media"
		bwRaw, bhRaw, _ := f.SizeUTF8(btnLabel)
		bw := int32(bwRaw) + SpaceLG*2
		bh := int32(32)
		if bw < 120 {
			bw = 120
		}
		_ = bhRaw
		bx := listX + (listW-bw)/2
		by := iconCY + EmptyMsgOffset2 + SpaceMD
		fillRoundedRect(renderer, bx, by, bw, bh, RadiusSM, focusColorForAccent(accentColor))
		renderer.SetDrawColor(ColorTextInverse().R, ColorTextInverse().G, ColorTextInverse().B, ColorTextInverse().A)
		renderer.DrawRect(&sdl.Rect{X: bx, Y: by, W: bw, H: bh})
		renderText(renderer, config, f, btnLabel, ColorTextInverse(), bx+SpaceLG, by+(bh-int32(f.Height()))/2)
		emptyActionRect = sdl.Rect{X: bx, Y: by, W: bw, H: bh}

		if homeLayoutActive {
			focusCol := focusColorForAccent(accentColor)
			strokeRoundedRect(renderer, listX, listY, listW, listH, RadiusLG, FocusRing, WithAlpha(focusCol, 180))
		}
		return
	}

	// Up to 3 recent items in a compact horizontal row.
	emptyActionRect = sdl.Rect{}
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
		drawTileIcon(renderer, chipX+chipS/2, chipY+chipS/2, int32(float64(chipS)*0.55), icon, ColorTextPrimary(), ColorSurfaceAlt)

		// Label (clipped to a single line with ellipsis)
		label := recentItemLabel(items[i])
		lw := sw - LabelPadding
		f, _ := getCachedFont(config, "small")
		if f == nil {
			f = font
		}
		w, _, _ := f.SizeUTF8(label)
		if int32(w) > lw {
			cur := ""
			for _, r := range label {
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

		// Type tag under label: for games this names the console, which is the
		// piece of context the title alone does not carry.
		typeName := map[string]string{
			"video": "Video",
			"iptv":  "Channel",
			"file":  filepath.Base(recentItemLabel(items[i])),
		}[items[i].Type]
		if typeName == "" {
			typeName = "Item"
		}
		if g, ok := items[i].Data.(recentGame); ok && g.System != "" {
			typeName = g.System
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
			moveSelection(config, ActionNavigateUp)
		}
	case sdl.K_DOWN:
		if homeLayoutActive {
			moveHomeSelection(config, 0, 1)
		} else {
			moveSelection(config, ActionNavigateDown)
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
		// Empty panel: only the action button opens Media (Tube).
		if mx >= emptyActionRect.X && mx <= emptyActionRect.X+emptyActionRect.W &&
			my >= emptyActionRect.Y && my <= emptyActionRect.Y+emptyActionRect.H {
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
