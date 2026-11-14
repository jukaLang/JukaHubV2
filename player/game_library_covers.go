package main

import (
	"github.com/veandco/go-sdl2/sdl"
)

// The cover layouts use the same selected game and click targets as the list.
// Opening, searching, and launching stay on the library's existing paths.
func drawGameCoverArt(renderer *sdl.Renderer, game libraryGame, rect sdl.Rect) {
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(ColorPanelRaised, 220))
	if tex := artTexture(renderer, game.Art); tex != nil {
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			dst := gaFit(tw, th, rect.W-8, rect.H-8)
			dst.X += rect.X + 4
			dst.Y += rect.Y + 4
			_ = renderer.Copy(tex, nil, &dst)
			return
		}
	}
	icon := rect.W / 3
	if rect.H/3 < icon {
		icon = rect.H / 3
	}
	glyph := "disc"
	if builtIn, ok := builtInLibraryGameFor(game); ok {
		glyph = builtIn.glyph
	}
	DrawHomeIcon(renderer, glyph, sdl.Rect{X: rect.X + (rect.W-icon)/2, Y: rect.Y + (rect.H-icon)/2, W: icon, H: icon}, false)
}

func renderGameCoverBrowser(renderer *sdl.Renderer, config *Config, system librarySystem, x, y, w, h int32, layout string) {
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	gameLib.gameHits = gameLib.gameHits[:0]
	if len(system.Games) == 0 || w <= 0 || h <= 0 {
		return
	}
	accent := installHighlight()
	if layout == libraryLayoutHorizontal {
		cardW := int32(150)
		if w < 3*cardW+24 {
			cardW = max32(100, (w-24)/3)
		}
		const gap = int32(12)
		visible := int((w + gap) / (cardW + gap))
		if visible < 1 {
			visible = 1
		}
		if visible > len(system.Games) {
			visible = len(system.Games)
		}
		start := gameLib.cursor - visible/2
		if start < 0 {
			start = 0
		}
		if start > len(system.Games)-visible {
			start = len(system.Games) - visible
		}
		gameLib.scroll = start
		cardH := min(int32(210), h-62)
		if cardH < 100 {
			cardH = 100
		}
		rowY := y + (h-cardH-18)/2
		shelfY := rowY + cardH + 7
		// A shaded top, deep front and cast shadow make the covers sit on a
		// shelf instead of floating as a flat row. The selected case lifts off it.
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		_ = renderer.SetDrawColor(0, 0, 0, 85)
		_ = renderer.FillRect(&sdl.Rect{X: x + 7, Y: shelfY + 18, W: w - 7, H: 7})
		fillRoundedRect(renderer, x, shelfY, w-8, 8, 2, WithAlpha(ColorPanelRaised, 245))
		fillRoundedRect(renderer, x+4, shelfY+8, w-8, 13, 2, WithAlpha(ColorSurfaceRow, 235))
		_ = renderer.SetDrawColor(255, 255, 255, 38)
		_ = renderer.DrawLine(x+3, shelfY+1, x+w-12, shelfY+1)
		for i := 0; i < visible; i++ {
			idx := start + i
			selected := idx == gameLib.cursor
			lift := int32(0)
			if selected {
				lift = 10
			}
			rect := sdl.Rect{X: x + int32(i)*(cardW+gap), Y: rowY - lift, W: cardW, H: cardH}
			gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: idx, rect: rect})
			_ = renderer.SetDrawColor(0, 0, 0, 95)
			_ = renderer.FillRect(&sdl.Rect{X: rect.X + 7, Y: rect.Y + 8, W: rect.W, H: rect.H})
			_ = renderer.SetDrawColor(0, 0, 0, 115)
			_ = renderer.FillRect(&sdl.Rect{X: rect.X + 4, Y: shelfY - 2, W: rect.W - 4, H: 5})
			fillRoundedRect(renderer, rect.X+rect.W-2, rect.Y+5, 7, rect.H-2, 2, WithAlpha(ColorBorderDefault, 225))
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(ColorSurfaceRow, 180))
			if selected {
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, accent)
			}
			art := sdl.Rect{X: rect.X + 8, Y: rect.Y + 8, W: rect.W - 16, H: rect.H - 56}
			drawGameCoverArt(renderer, system.Games[idx], art)
			renderText(renderer, config, font, ellipsize(font, system.Games[idx].Label(), rect.W-16), ColorTextPrimary(), rect.X+8, art.Y+art.H+7)
		}
		renderText(renderer, config, font, "←/→ games · ↑ consoles", ColorTextTertiary(), x, y+h-int32(font.Height())-4)
		return
	}
	const rowH = int32(122)
	visible := int((h - 24) / rowH)
	if visible < 1 {
		visible = 1
	}
	if gameLib.cursor < gameLib.scroll {
		gameLib.scroll = gameLib.cursor
	}
	if gameLib.cursor >= gameLib.scroll+visible {
		gameLib.scroll = gameLib.cursor - visible + 1
	}
	if gameLib.scroll > len(system.Games)-visible {
		gameLib.scroll = max(0, len(system.Games)-visible)
	}
	for i := 0; i < visible && gameLib.scroll+i < len(system.Games); i++ {
		idx := gameLib.scroll + i
		rect := sdl.Rect{X: x, Y: y + int32(i)*rowH, W: w, H: rowH - 7}
		gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == gameLib.cursor
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(ColorSurfaceRow, 175))
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, accent)
		}
		art := sdl.Rect{X: rect.X + 8, Y: rect.Y + 8, W: 99, H: 99}
		drawGameCoverArt(renderer, system.Games[idx], art)
		textX := art.X + art.W + 15
		textW := rect.X + rect.W - textX - 12
		renderText(renderer, config, medium, ellipsize(medium, system.Games[idx].Label(), textW), ColorTextPrimary(), textX, rect.Y+18)
		renderText(renderer, config, font, ellipsize(font, system.Games[idx].System, textW), ColorTextTertiary(), textX, rect.Y+18+int32(medium.Height())+8)
	}
}
