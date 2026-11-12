package main

import (
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

var gameHistoryUI struct {
	open   bool
	scroll int
	card   sdl.Rect
	prev   sdl.Rect
	next   sdl.Rect
}

func gameHistoryOpen() {
	if !gmetaUI.open {
		return
	}
	gameHistoryUI.open, gameHistoryUI.scroll = true, 0
	PlayActivateSound()
}

func gameHistoryClose() {
	gameHistoryUI.open = false
	PlayBackSound()
}

func gameHistoryMove(delta int) {
	rec := gameHistoryFor(gmetaUI.game.System, gmetaUI.game.Title)
	n := len(rec.Sessions)
	gameHistoryUI.scroll += delta
	if gameHistoryUI.scroll < 0 {
		gameHistoryUI.scroll = 0
	}
	if gameHistoryUI.scroll >= n && n > 0 {
		gameHistoryUI.scroll = n - 1
	}
	PlayNavSound()
}

func gameHistoryHandleKey(e *sdl.KeyboardEvent) bool {
	if !gameHistoryUI.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		gameHistoryClose()
	case sdl.K_UP, sdl.K_PAGEUP:
		gameHistoryMove(-1)
	case sdl.K_DOWN, sdl.K_PAGEDOWN:
		gameHistoryMove(1)
	}
	return true
}

func gameHistoryHandlePad(e *sdl.ControllerButtonEvent) bool {
	if !gameHistoryUI.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		gameHistoryClose()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameHistoryMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameHistoryMove(1)
	}
	return true
}

func gameHistoryHandleMouse(mx, my int32) bool {
	if !gameHistoryUI.open {
		return false
	}
	switch {
	case pointInRect(mx, my, gameHistoryUI.prev):
		gameHistoryMove(-1)
	case pointInRect(mx, my, gameHistoryUI.next):
		gameHistoryMove(1)
	case !pointInRect(mx, my, gameHistoryUI.card):
		gameHistoryClose()
	}
	return true
}

func gameHistoryRender(renderer *sdl.Renderer, config *Config) {
	if !gameHistoryUI.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	w := int32(780)
	if w > screenWidth-32 {
		w = screenWidth - 32
	}
	h := int32(650)
	if h > screenHeight-32 {
		h = screenHeight - 32
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	gameHistoryUI.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	x, y := card.X+26, card.Y+22
	innerW := card.W - 52
	renderText(renderer, config, medium, ellipsize(medium, gmetaUI.game.Label()+" · Play history", innerW), ColorTextPrimary(), x, y)
	y += int32(medium.Height()) + 18
	rec := gameHistoryFor(gmetaUI.game.System, gmetaUI.game.Title)
	st := gameHistorySummarize(rec, time.Now())
	total := playtimeGameSecondsFor(gmetaUI.game.System, gmetaUI.game.Title)
	if rec.Total > total {
		total = rec.Total
	}
	rows := []string{
		"Total " + formatPlaytime(total) + "  ·  " + strconv.Itoa(st.Launches) + " launches  ·  " + strconv.Itoa(st.Sessions) + " sessions",
		"Longest " + formatPlaytime(st.Longest) + "  ·  Last 7 days " + formatPlaytime(st.Last7) + "  ·  Last 30 days " + formatPlaytime(st.Last30),
	}
	if st.Interrupted > 0 {
		rows = append(rows, strconv.Itoa(st.Interrupted)+" interrupted launch(es) retained; duration unknown")
	}
	for _, row := range rows {
		renderText(renderer, config, font, ellipsize(font, row, innerW), ColorTextSecondary(), x, y)
		y += int32(font.Height()) + 10
	}
	y += 8
	renderText(renderer, config, font, "RECENT SESSIONS", ColorTextTertiary(), x, y)
	y += int32(font.Height()) + 8
	bottom := card.Y + card.H - int32(font.Height()) - 36
	visible := int((bottom - y) / 44)
	if visible < 1 {
		visible = 1
	}
	if gameHistoryUI.scroll > len(st.Recent)-visible {
		gameHistoryUI.scroll = max(0, len(st.Recent)-visible)
	}
	if len(st.Recent) == 0 {
		renderText(renderer, config, font, "Play a game to start its history.", ColorTextSecondary(), x, y+8)
	}
	for i := gameHistoryUI.scroll; i < len(st.Recent) && i < gameHistoryUI.scroll+visible; i++ {
		s := st.Recent[i]
		label := s.Started.Local().Format("Jan 2, 2006 · 3:04 PM")
		status := formatPlaytime(s.Seconds)
		if s.Interrupted {
			status = "Interrupted · duration unknown"
		} else if s.Seconds == 0 {
			status = "Ended quickly"
		}
		rect := sdl.Rect{X: x, Y: y + int32(i-gameHistoryUI.scroll)*44, W: innerW, H: 40}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(ColorSurfaceRow, 165))
		renderText(renderer, config, font, ellipsize(font, label, rect.W/2), ColorTextPrimary(), rect.X+10, rect.Y+8)
		status = ellipsize(font, status, rect.W/2-12)
		sw, _ := measureText(font, status)
		renderText(renderer, config, font, status, ColorTextSecondary(), rect.X+rect.W-sw-10, rect.Y+8)
	}
	footerY := card.Y + card.H - int32(font.Height()) - 16
	gameHistoryUI.prev = sdl.Rect{X: x, Y: footerY - 6, W: 76, H: int32(font.Height()) + 12}
	gameHistoryUI.next = sdl.Rect{X: x + 84, Y: footerY - 6, W: 76, H: int32(font.Height()) + 12}
	renderText(renderer, config, font, "↑ Newer", ColorTextSecondary(), x, footerY)
	renderText(renderer, config, font, "↓ Older", ColorTextSecondary(), x+84, footerY)
	renderText(renderer, config, font, "B Back", ColorTextSecondary(), card.X+card.W-88, footerY)
}
