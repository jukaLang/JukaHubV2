package main

// gamepage_more.go — the game page's More button: one sheet that lists what
// else can be done with this game, and room for the screens its rows open.
//
// The page's button row (gmeta_ui.go) has no space left for every feature that
// belongs to one title: its manual and guide, its control layout, its saves on
// another device, a side-by-side test of two emulators, whether it is ready to
// start. Each of those features registers here instead of editing the page:
//
//   - gamePageRegisterExtra adds a row to the More sheet: its label and second
//     line for the game (or no row at all), and what A on it opens;
//   - gamePageRegisterOverlay adds a screen that opens over the page: while
//     its IsOpen() is true it gets the page's input before the page does and
//     is drawn above it, and closing the page closes it;
//   - gamePageRegisterStatus adds a chip to the page's details column
//     ("Ready", "Manual", "Controls: Arcade 6-button"). Chips are drawn every
//     frame, so a provider answers from memory, never from the disk: it reads
//     its files on a goroutine when the page opens (gamePageRegisterOpened)
//     and posts the answer back with tbxPost.
//
// Registration happens in init(); the lists are read on the main thread only.

import (
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// gamePageExtra is one row of the More sheet.
type gamePageExtra struct {
	ID    string
	Order int
	// Icon is a home glyph drawn at the row's start ("" draws none).
	Icon string
	// Row is the row for game: its label and a second line ("" none). ok
	// false leaves the row out for this game. Called when the sheet opens.
	Row func(game libraryGame) (label, detail string, ok bool)
	// Open runs when the row is chosen; the sheet is already closed.
	Open func(game libraryGame)
}

// gamePageOverlay is a screen a feature opens over the game page.
type gamePageOverlay struct {
	ID     string
	IsOpen func() bool
	// Key, Pad and Mouse get the page's input while IsOpen() is true; true
	// means the event was used. An open overlay should use everything: input
	// it leaves goes to the page underneath.
	Key    func(e *sdl.KeyboardEvent) bool
	Pad    func(e *sdl.ControllerButtonEvent) bool
	Mouse  func(mx, my int32) bool
	Render func(renderer *sdl.Renderer, config *Config)
	// Close shuts the overlay without a sound (the page is closing).
	Close func()
}

// The tones of a status chip.
const (
	gamePageToneNeutral = iota
	gamePageToneGood
	gamePageToneWarn
)

// gamePageStatus is one chip of the page's details column.
type gamePageStatus struct {
	ID    string
	Order int
	// Chip is the chip for game, answered from memory (drawn every frame).
	Chip func(game libraryGame) (text string, tone int, ok bool)
}

var (
	gamePageExtras   []gamePageExtra
	gamePageOverlays []gamePageOverlay
	gamePageStatuses []gamePageStatus
	// gamePageOpenedHooks run when a game's page opens (main thread), so a
	// status provider can start reading what its chip needs.
	gamePageOpenedHooks []func(game libraryGame)
	// gamePageClosedHooks run when the page closes (main thread), so a
	// provider can stop work nobody will see.
	gamePageClosedHooks []func()
)

// gamePageRegisterClosed runs fn each time the game's page closes (also
// just before another game's page opens). Call from init().
func gamePageRegisterClosed(fn func()) {
	if fn != nil {
		gamePageClosedHooks = append(gamePageClosedHooks, fn)
	}
}

// gamePageRegisterExtra adds a row to the More sheet. Call from init().
func gamePageRegisterExtra(x gamePageExtra) {
	if x.Row == nil || x.Open == nil {
		return
	}
	gamePageExtras = append(gamePageExtras, x)
	sort.SliceStable(gamePageExtras, func(i, j int) bool { return gamePageExtras[i].Order < gamePageExtras[j].Order })
}

// gamePageRegisterOverlay adds a screen over the page. Call from init().
func gamePageRegisterOverlay(o gamePageOverlay) {
	if o.IsOpen == nil {
		return
	}
	gamePageOverlays = append(gamePageOverlays, o)
}

// gamePageRegisterStatus adds a chip to the details column. Call from init().
func gamePageRegisterStatus(s gamePageStatus) {
	if s.Chip == nil {
		return
	}
	gamePageStatuses = append(gamePageStatuses, s)
	sort.SliceStable(gamePageStatuses, func(i, j int) bool { return gamePageStatuses[i].Order < gamePageStatuses[j].Order })
}

// gamePageRegisterOpened runs fn each time a game's page opens. Call from
// init().
func gamePageRegisterOpened(fn func(game libraryGame)) {
	if fn != nil {
		gamePageOpenedHooks = append(gamePageOpenedHooks, fn)
	}
}

// gamePageOpened is called by the page as it opens (gmetaOpenDetails).
func gamePageOpened(game libraryGame) {
	gamePageMoreUI.open = false
	for _, fn := range gamePageOpenedHooks {
		fn(game)
	}
}

// ── The More sheet ───────────────────────────────────────────────────────────

// gamePageMoreRow is one row the open sheet shows.
type gamePageMoreRow struct {
	extra  int
	label  string
	detail string
	icon   string
}

// gamePageMoreUI is the sheet stacked on the page. Main thread only.
var gamePageMoreUI struct {
	open   bool
	cursor int
	first  int
	game   libraryGame
	rows   []gamePageMoreRow
	hits   []gameLibRowHit
	card   sdl.Rect
}

// gamePageMoreOpen opens the sheet for the page's game.
func gamePageMoreOpen() {
	if !gmetaUI.open {
		return
	}
	game := gmetaUI.game
	rows := make([]gamePageMoreRow, 0, len(gamePageExtras))
	for i, x := range gamePageExtras {
		label, detail, ok := x.Row(game)
		if !ok || strings.TrimSpace(label) == "" {
			continue
		}
		rows = append(rows, gamePageMoreRow{extra: i, label: label, detail: detail, icon: x.Icon})
	}
	if len(rows) == 0 {
		ShowToast("Nothing more for this game yet", ToastKindInfo)
		return
	}
	gamePageMoreUI.open = true
	gamePageMoreUI.game = game
	gamePageMoreUI.rows = rows
	gamePageMoreUI.cursor, gamePageMoreUI.first = 0, 0
	PlayActivateSound()
}

// gamePageMoreChoose closes the sheet and opens the focused row.
func gamePageMoreChoose() {
	ui := &gamePageMoreUI
	if ui.cursor < 0 || ui.cursor >= len(ui.rows) {
		return
	}
	row := ui.rows[ui.cursor]
	game := ui.game
	ui.open = false
	if row.extra < 0 || row.extra >= len(gamePageExtras) {
		return
	}
	PlayActivateSound()
	gamePageExtras[row.extra].Open(game)
}

// gamePageMoreAction is the sheet's input.
func gamePageMoreAction(act tbxAction) {
	ui := &gamePageMoreUI
	switch act {
	case tbxActUp:
		if ui.cursor > 0 {
			ui.cursor--
			PlayNavSound()
		}
	case tbxActDown:
		if ui.cursor < len(ui.rows)-1 {
			ui.cursor++
			PlayNavSound()
		}
	case tbxActPageUp, tbxActHome:
		if ui.cursor != 0 {
			ui.cursor = 0
			PlayNavSound()
		}
	case tbxActPageDown, tbxActEnd:
		if last := len(ui.rows) - 1; ui.cursor != last && last >= 0 {
			ui.cursor = last
			PlayNavSound()
		}
	case tbxActConfirm:
		gamePageMoreChoose()
	case tbxActBack:
		ui.open = false
		PlayBackSound()
	}
}

// gamePageMoreMouse handles a click while the sheet is open.
func gamePageMoreMouse(mx, my int32) {
	ui := &gamePageMoreUI
	for _, hit := range ui.hits {
		if pointInRect(mx, my, hit.rect) {
			ui.cursor = hit.index
			gamePageMoreChoose()
			return
		}
	}
	if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
		ui.open = false
		PlayBackSound()
	}
}

// gamePageMoreRender draws the sheet.
func gamePageMoreRender(renderer *sdl.Renderer, config *Config) {
	ui := &gamePageMoreUI
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("More", ui.game.Label(), "list")
	headerH := UIPanelHeaderBand(config, header, 10)
	rowH := fonts.bodyH + fonts.tinyH + 22
	n := len(ui.rows)
	footerH := fonts.smallH + 20
	cardW := min(int32(640), screenWidth-64)
	maxH := screenHeight - 48
	cardH := min(headerH+int32(n)*rowH+footerH+8, maxH)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	visible := int((card.Y + card.H - footerH - 8 - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	if ui.cursor < ui.first {
		ui.first = ui.cursor
	}
	if ui.cursor >= ui.first+visible {
		ui.first = ui.cursor - visible + 1
	}
	ui.hits = ui.hits[:0]
	for i := ui.first; i < n && i < ui.first+visible; i++ {
		row := ui.rows[i]
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i-ui.first)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 6}
		UIListRow(renderer, rect, glassRowRadius, i == ui.cursor, false)
		textX := rect.X + 16
		if row.icon != "" {
			icon := sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-28)/2, W: 28, H: 28}
			DrawHomeIcon(renderer, row.icon, icon, i == ui.cursor)
			textX = icon.X + icon.W + 14
		}
		textW := rect.X + rect.W - 14 - textX
		if row.detail == "" {
			_, th := measureText(fonts.body, row.label)
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, row.label, textW), ColorTextPrimary(), textX, rect.Y+(rect.H-th)/2)
		} else {
			top := rect.Y + (rect.H-fonts.bodyH-fonts.tinyH-2)/2
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, row.label, textW), ColorTextPrimary(), textX, top)
			renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, row.detail, textW), ColorTextSecondary(), textX, top+fonts.bodyH+2)
		}
		ui.hits = append(ui.hits, gameLibRowHit{index: i, rect: rect})
	}
	hint := hintLine(FooterHint{"A", "Open"}, FooterHint{"B", "Back"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// ── Routing (gmeta_ui.go) ───────────────────────────────────────────────────

// gamePageTopOverlay is the open overlay that gets input: the last registered
// one that is open (a feature opened from another sits above it).
func gamePageTopOverlay() (gamePageOverlay, bool) {
	for i := len(gamePageOverlays) - 1; i >= 0; i-- {
		if o := gamePageOverlays[i]; o.IsOpen() {
			return o, true
		}
	}
	return gamePageOverlay{}, false
}

// gamePageRouteKey gives a key press to an open overlay or the More sheet.
func gamePageRouteKey(e *sdl.KeyboardEvent) bool {
	if o, ok := gamePageTopOverlay(); ok {
		if o.Key != nil && o.Key(e) {
			return true
		}
		return e != nil && e.Type == sdl.KEYDOWN
	}
	if gamePageMoreUI.open {
		gamePageMoreAction(tbxKeyAction(e))
		return true
	}
	return false
}

// gamePageRoutePad gives a pad press to an open overlay or the More sheet.
func gamePageRoutePad(e *sdl.ControllerButtonEvent) bool {
	if o, ok := gamePageTopOverlay(); ok {
		if o.Pad != nil && o.Pad(e) {
			return true
		}
		return e != nil && e.Type == sdl.CONTROLLERBUTTONDOWN
	}
	if gamePageMoreUI.open {
		gamePageMoreAction(tbxPadAction(e))
		return true
	}
	return false
}

// gamePageRouteMouse gives a click to an open overlay or the More sheet.
func gamePageRouteMouse(mx, my int32) bool {
	if o, ok := gamePageTopOverlay(); ok {
		if o.Mouse != nil {
			o.Mouse(mx, my)
		}
		return true
	}
	if gamePageMoreUI.open {
		gamePageMoreMouse(mx, my)
		return true
	}
	return false
}

// gamePageRenderOver draws the More sheet, then the open overlays, over the
// page.
func gamePageRenderOver(renderer *sdl.Renderer, config *Config) {
	if gamePageMoreUI.open {
		gamePageMoreRender(renderer, config)
	}
	for _, o := range gamePageOverlays {
		if o.IsOpen() && o.Render != nil {
			o.Render(renderer, config)
		}
	}
}

// gamePageCloseAll closes the sheet and every overlay (the page is closing).
func gamePageCloseAll() {
	gamePageMoreUI.open = false
	for _, o := range gamePageOverlays {
		if o.IsOpen() && o.Close != nil {
			o.Close()
		}
	}
	for _, fn := range gamePageClosedHooks {
		fn()
	}
}

// gamePageDrawChips draws the status chips for game in one line at (x, y),
// at most w wide, and returns the height it used (0 when there is none).
func gamePageDrawChips(renderer *sdl.Renderer, config *Config, fonts tbxFonts, game libraryGame, x, y, w int32) int32 {
	if len(gamePageStatuses) == 0 || w < 60 {
		return 0
	}
	rowH := max32(fonts.tinyH+8, 20)
	cx, cy := x, y+rowH/2
	drawn := false
	for _, s := range gamePageStatuses {
		text, tone, ok := s.Chip(game)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		ink := glassAccent()
		switch tone {
		case gamePageToneGood:
			ink = ColorSuccess
		case gamePageToneWarn:
			ink = ColorWarning
		}
		tw, th := measureText(fonts.tiny, text)
		cw := tw + 20
		if cx+cw > x+w {
			if !drawn {
				text = ellipsize(fonts.tiny, text, w-20)
				tw, th = measureText(fonts.tiny, text)
				cw = tw + 20
			} else {
				break
			}
		}
		h := th + 8
		fillRoundedRect(renderer, cx, cy-h/2, cw, h, h/2, WithAlpha(ink, 40))
		strokeRoundedRect(renderer, cx, cy-h/2, cw, h, h/2, 1, WithAlpha(ink, 140))
		renderText(renderer, config, fonts.tiny, text, ColorTextPrimary(), cx+10, cy-th/2)
		cx += cw + 8
		drawn = true
	}
	if !drawn {
		return 0
	}
	return rowH + 10
}
