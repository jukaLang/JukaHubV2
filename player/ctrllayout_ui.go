package main

// ctrllayout_ui.go — where a game's control layout is chosen and shown: a
// picker over the game page (its More sheet's "Controls" row) or over the
// game options sheet (its "Controls layout" row), and a "Controls: …" chip on
// the game page.
//
// The picker lists "None (console buttons)" and the layouts offered for the
// game's console (ctrlLayoutsFor), and draws the focused layout's chart on the
// right as the game will see it: for the core the game starts in and for this
// handheld (the DS stylus is on the D-pad on a Brick). A is "use for this
// game", X "use for every <console> game" (which also clears this game's own
// choice, so what was just picked is what it plays with), B goes back. A
// click focuses a row, a second click on it picks it, so the chart can be
// read first.
//
// Which emulator the game starts in is worked out on a goroutine when the
// picker or the page opens (resolveGameLauncherFor reads the card): until it
// answers the picker shows the chart for the console's usual core, and once it
// does, a standalone emulator gets "This emulator keeps its own controls" and
// no chip. The note under the list says which buttons a layout decides — the
// Buttons choice (Swap A and B …) still decides the others — and what the
// last launch could not apply.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// Where the picker is open.
const (
	ctrlLayoutHostPage  = 1 // over the game page (gamepage_more.go)
	ctrlLayoutHostSheet = 2 // over the game options sheet (game_options_ui.go)
)

// ctrlLayoutPickRow is one row of the picker: a layout, or None.
type ctrlLayoutPickRow struct {
	id     string
	name   string
	detail string
}

// ctrlLayoutPicker is the open picker. Main thread only.
var ctrlLayoutPicker struct {
	open   bool
	host   int
	game   libraryGame
	rows   []ctrlLayoutPickRow
	cursor int
	first  int
	hits   []gameLibRowHit
	card   sdl.Rect
	gen    int
	// What the game starts in, filled by a goroutine; known is false until
	// it answers.
	known     bool
	retroArch bool
	core      string
	// The focused row's chart, resolved once per row and core.
	chartKey string
	chart    ctrlLayoutResolved
}

// ctrlLayoutPageFacts is whether the open game page's game starts in
// RetroArch, for its chip. Main thread only.
var ctrlLayoutPageFacts struct {
	path      string
	known     bool
	retroArch bool
}

// ctrlLayoutLauncherFacts works out which emulator game starts in: whether it
// is a RetroArch core JukaHub can remap and, when withCore, which core.
// known is false when no emulator is found. Goroutine: it reads the card.
func ctrlLayoutLauncherFacts(game libraryGame, withCore bool) (retroArch bool, core string, known bool) {
	launcher := resolveGameLauncherFor(game)
	if launcher == "" {
		return false, "", false
	}
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return false, "", true
	}
	if withCore {
		core = normalizeCoreName(gameCoreFromLauncher(launcher, game.System))
	}
	return true, core, true
}

// ctrlLayoutOffered reports whether a game can have a control layout at all:
// browser games, ports and Android games start no emulator JukaHub remaps.
func ctrlLayoutOffered(game libraryGame) bool {
	switch game.System {
	case html5LibrarySystem, portsLibrarySystem, androidLibrarySystem:
		return false
	}
	return !isBuiltInLibraryGame(game)
}

// ctrlLayoutCurrentID is the id of the layout game plays with, "none" for
// none.
func ctrlLayoutCurrentID(game libraryGame) string {
	if l, ok := ctrlLayoutForGame(game); ok {
		return l.ID
	}
	return ctrlLayoutNone
}

// ctrlLayoutConsoleID is the id of the layout system's games play with
// unless they have their own, "none" for none.
func ctrlLayoutConsoleID(system string) string {
	if l, ok := ctrlLayoutForConsole(system); ok {
		return l.ID
	}
	return ctrlLayoutNone
}

// ctrlLayoutPickerRows lists None and the layouts for game's console.
func ctrlLayoutPickerRows(game libraryGame) []ctrlLayoutPickRow {
	rows := []ctrlLayoutPickRow{{
		id:     ctrlLayoutNone,
		name:   "None (console buttons)",
		detail: "Buttons: " + gameButtonsLabel(gameButtonsForGame(appConfig, game)),
	}}
	for _, l := range ctrlLayoutsFor(game.System) {
		rows = append(rows, ctrlLayoutPickRow{id: l.ID, name: l.Name, detail: l.Description})
	}
	return rows
}

// ctrlLayoutPickerOpen opens the picker for game over host.
func ctrlLayoutPickerOpen(game libraryGame, host int) {
	if !ctrlLayoutOffered(game) {
		// The options sheet has the row for every emulated game; an Android
		// or built-in game starts nothing a layout reaches.
		ShowToast(game.Label()+" keeps its own controls", ToastKindInfo)
		return
	}
	p := &ctrlLayoutPicker
	p.open, p.host, p.game = true, host, game
	p.rows = ctrlLayoutPickerRows(game)
	p.cursor, p.first = 0, 0
	p.hits = p.hits[:0] // drawn again before a click can land on a row
	cur := ctrlLayoutCurrentID(game)
	for i, row := range p.rows {
		if row.id == cur {
			p.cursor = i
		}
	}
	p.known, p.retroArch, p.core = false, false, ""
	p.chartKey = ""
	p.gen++
	gen := p.gen
	if host == ctrlLayoutHostSheet {
		PlayActivateSound() // the More sheet plays its own
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("controls", "checking the game's emulator failed: %v", r)
			}
		}()
		added := ctrlLayoutLoadUser() // a file copied over USB since the start
		retroArch, core, known := ctrlLayoutLauncherFacts(game, true)
		tbxPost(func() {
			if !p.open || p.gen != gen {
				return
			}
			p.known, p.retroArch, p.core = known, retroArch, core
			p.chartKey = ""
			if added > 0 && p.cursor < len(p.rows) {
				focused := p.rows[p.cursor].id
				p.rows = ctrlLayoutPickerRows(p.game)
				for i, row := range p.rows {
					if row.id == focused {
						p.cursor = i
					}
				}
			}
		})
	}()
}

// ctrlLayoutPickerCloseOn closes the picker when it is open over host, without
// a sound (the screen under it is closing).
func ctrlLayoutPickerCloseOn(host int) {
	if ctrlLayoutPicker.host == host {
		ctrlLayoutPicker.open = false
	}
}

// ctrlLayoutPickerOnSheet reports the picker open over the options sheet.
func ctrlLayoutPickerOnSheet() bool {
	return ctrlLayoutPicker.open && ctrlLayoutPicker.host == ctrlLayoutHostSheet
}

// ctrlLayoutPickerChoose stores the focused row for the game, or for every
// game of its console.
func ctrlLayoutPickerChoose(console bool) {
	p := &ctrlLayoutPicker
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return
	}
	row := p.rows[p.cursor]
	game := p.game
	system := strings.TrimSpace(game.System)
	what := row.name
	if row.id == ctrlLayoutNone {
		what = "no layout, console buttons"
	}
	if p.known && !p.retroArch {
		what += " · not used by this emulator"
	}
	if console {
		if system == "" {
			return
		}
		// The console's built-in default, picked again, is no choice at all.
		id := row.id
		if def, ok := ctrlLayoutDefaultFor(system); (ok && def.ID == id) || (!ok && id == ctrlLayoutNone) {
			id = ""
		}
		if !ctrlLayoutSetConsole(system, id, game.Path) {
			return
		}
		ShowToast("Every "+system+" game: "+what, ToastKindSuccess)
	} else {
		if !ctrlLayoutSetGame(game.Path, row.id) {
			return
		}
		ShowToast(game.Label()+": "+what, ToastKindSuccess)
	}
	PlayActivateSound()
	p.open = false
}

// ctrlLayoutPickerAction is the picker's input.
func ctrlLayoutPickerAction(act tbxAction) {
	p := &ctrlLayoutPicker
	switch act {
	case tbxActUp:
		if p.cursor > 0 {
			p.cursor--
			PlayNavSound()
		}
	case tbxActDown:
		if p.cursor < len(p.rows)-1 {
			p.cursor++
			PlayNavSound()
		}
	case tbxActPageUp, tbxActHome:
		if p.cursor != 0 {
			p.cursor = 0
			PlayNavSound()
		}
	case tbxActPageDown, tbxActEnd:
		if last := len(p.rows) - 1; p.cursor != last && last >= 0 {
			p.cursor = last
			PlayNavSound()
		}
	case tbxActConfirm:
		ctrlLayoutPickerChoose(false)
	case tbxActX:
		ctrlLayoutPickerChoose(true)
	case tbxActBack:
		p.open = false
		PlayBackSound()
	}
}

// ctrlLayoutPickerKey and ctrlLayoutPickerPad use every event while the
// picker is open, so nothing reaches the screen under it.
func ctrlLayoutPickerKey(e *sdl.KeyboardEvent) bool {
	if !ctrlLayoutPicker.open {
		return false
	}
	if e != nil && e.Type == sdl.KEYDOWN {
		ctrlLayoutPickerAction(tbxKeyAction(e))
	}
	return true
}

func ctrlLayoutPickerPad(e *sdl.ControllerButtonEvent) bool {
	if !ctrlLayoutPicker.open {
		return false
	}
	if e != nil && e.Type == sdl.CONTROLLERBUTTONDOWN {
		ctrlLayoutPickerAction(tbxPadAction(e))
	}
	return true
}

// ctrlLayoutPickerMouse focuses a clicked row, picks a focused one, and
// closes on a click outside the card.
func ctrlLayoutPickerMouse(mx, my int32) bool {
	p := &ctrlLayoutPicker
	if !p.open {
		return false
	}
	for _, hit := range p.hits {
		if pointInRect(mx, my, hit.rect) {
			if p.cursor == hit.index {
				ctrlLayoutPickerChoose(false)
			} else {
				p.cursor = hit.index
				PlayNavSound()
			}
			return true
		}
	}
	if p.card.W == 0 || !pointInRect(mx, my, p.card) {
		p.open = false
		PlayBackSound()
	}
	return true
}

// ctrlLayoutCoreLabel names a core as RetroArch does ("FinalBurn Neo").
func ctrlLayoutCoreLabel(core string) string {
	if names := raTuneCoreFolders[core]; len(names) > 0 {
		return names[0]
	}
	if names := resetCoreFolders(core); len(names) > 0 {
		return names[0]
	}
	return core
}

// ctrlLayoutTargetLabel names what a button sends, for a chart made from a
// layout's buttons.
func ctrlLayoutTargetLabel(n int) string {
	if n < 0 {
		return "Nothing"
	}
	if n < len(raRemapButtonNames) {
		return ctrlLayoutPadLabel(raRemapButtonNames[n])
	}
	for name, v := range ctrlLayoutStickTargets {
		if v == n {
			side, dir, _ := strings.Cut(name, "-")
			if side == "lstick" {
				return "Left stick " + dir
			}
			return "Right stick " + dir
		}
	}
	return "Button " + itoa(n)
}

// ctrlLayoutChartLines is the chart to draw for a resolved layout: its own,
// or one made from its buttons when it has none.
func ctrlLayoutChartLines(r ctrlLayoutResolved) []ctrlLayoutLine {
	if len(r.Chart) > 0 {
		return r.Chart
	}
	var out []ctrlLayoutLine
	for _, pad := range ctrlLayoutPadOrder {
		if n, ok := r.Buttons[pad]; ok {
			out = append(out, ctrlLayoutLine{Button: ctrlLayoutPadLabel(pad), Action: "Sends " + ctrlLayoutTargetLabel(n)})
		}
	}
	return out
}

// ctrlLayoutPickerFocus resolves the focused row's layout (cached per row and
// core). ok is false on the None row.
func ctrlLayoutPickerFocus() (ctrlLayoutResolved, bool) {
	p := &ctrlLayoutPicker
	if p.cursor < 0 || p.cursor >= len(p.rows) || p.rows[p.cursor].id == ctrlLayoutNone {
		return ctrlLayoutResolved{}, false
	}
	id := p.rows[p.cursor].id
	key := id + "\x00" + p.core
	if p.chartKey != key {
		l, ok := ctrlLayoutByID(id)
		if !ok {
			return ctrlLayoutResolved{}, false
		}
		p.chart = ctrlLayoutResolve(l, p.core, stickHardware())
		p.chartKey = key
	}
	return p.chart, true
}

// ctrlLayoutPickerNotes are the two lines under the list: what the focused
// row decides, and what stops it (a standalone emulator, the last launch).
func ctrlLayoutPickerNotes(r ctrlLayoutResolved, isLayout bool) (string, string, bool) {
	p := &ctrlLayoutPicker
	first := "No layout: the console's Buttons and Turbo choices apply"
	if isLayout {
		switch set := ctrlLayoutSetButtons(r); {
		case len(set) > 0:
			first = "Sets " + strings.Join(set, ", ") + " · the Buttons choice keeps the others"
		case len(r.CoreOptions) > 0:
			first = "Moves no buttons · sets the emulator's own options for the game"
		default:
			first = "Moves no buttons · the chart is how this emulator plays it"
		}
	}
	if p.known && !p.retroArch {
		return first, "This emulator keeps its own controls · layouts apply in RetroArch", true
	}
	if note := ctrlLayoutNote(p.game.Path); note != "" {
		return first, "Last time: " + note, true
	}
	if note := gameControlsNote(p.game.Path); note != "" {
		return first, note, true
	}
	if isLayout && len(r.Keys) > 0 {
		return first, "Its keyboard keys are listed for reference; JukaHub does not set them yet", false
	}
	return first, "Set each time the game starts; nothing in RetroArch is changed for good", false
}

// ctrlLayoutPickerRender draws the picker.
func ctrlLayoutPickerRender(renderer *sdl.Renderer, config *Config) {
	p := &ctrlLayoutPicker
	if !p.open || renderer == nil {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	system := strings.TrimSpace(p.game.System)
	sub := p.game.Label()
	if system != "" {
		sub += " · " + system
	}
	header := UIPanelHeader("Controls", sub, "gamepad")
	headerH := UIPanelHeaderBand(config, header, 10)

	focus, isLayout := ctrlLayoutPickerFocus()
	var title, caption string
	var lines []ctrlLayoutLine
	if isLayout {
		title, caption, lines = focus.Layout.Name, focus.Layout.Description, ctrlLayoutChartLines(focus)
		if p.known && p.retroArch && p.core != "" {
			caption = "As " + ctrlLayoutCoreLabel(p.core) + " plays it"
		}
	} else {
		title, caption = "Console buttons", "The Buttons and Turbo rows of Game options"
		lines = []ctrlLayoutLine{
			{Button: "Buttons", Action: gameButtonsLabel(gameButtonsForGame(appConfig, p.game))},
			{Button: "Turbo", Action: gameTurboLabel(gameTurboForGame(appConfig, p.game))},
		}
	}

	rowH := fonts.bodyH + fonts.tinyH + 22
	lineH := fonts.smallH + 8
	footerH := fonts.smallH + 20
	noteH := 2*fonts.tinyH + 18
	n := len(p.rows)
	chartH := 14 + fonts.bodyH + 4 + fonts.tinyH + 12 + int32(len(lines))*lineH + 10
	bodyH := max32(int32(n)*rowH, chartH)
	cardW := min(int32(900), screenWidth-48)
	cardH := min(headerH+bodyH+noteH+footerH+8, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	p.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	bottom := card.Y + card.H - footerH - noteH
	innerW := card.W - 2*OverlayPadX
	listW := innerW * 11 / 20

	// The list.
	visible := int((bottom - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	if p.cursor < p.first {
		p.first = p.cursor
	}
	if p.cursor >= p.first+visible {
		p.first = p.cursor - visible + 1
	}
	cur := ctrlLayoutCurrentID(p.game)
	consoleID := ctrlLayoutConsoleID(system)
	p.hits = p.hits[:0]
	for i := p.first; i < n && i < p.first+visible; i++ {
		row := p.rows[i]
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i-p.first)*rowH, W: listW, H: rowH - 6}
		UIListRow(renderer, rect, glassRowRadius, i == p.cursor, false)
		var tags []string
		if row.id == cur {
			tags = append(tags, "In use")
		}
		if row.id != ctrlLayoutNone && row.id == consoleID && system != "" {
			tags = append(tags, "Every "+system+" game")
		}
		detail, ink := row.detail, ColorTextSecondary()
		if len(tags) > 0 {
			detail, ink = strings.Join(tags, " · "), glassAccent()
		}
		textW := rect.W - 32
		top := rect.Y + (rect.H-fonts.bodyH-fonts.tinyH-2)/2
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, row.name, textW), ColorTextPrimary(), rect.X+16, top)
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, detail, textW), ink, rect.X+16, top+fonts.bodyH+2)
		p.hits = append(p.hits, gameLibRowHit{index: i, rect: rect})
	}

	// The focused row's chart.
	panel := sdl.Rect{X: card.X + OverlayPadX + listW + 14, Y: listY, W: innerW - listW - 14, H: bottom - listY - 6}
	fillRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, glassRowRadius, WithAlpha(ColorSurfaceRow, 150))
	px, pw := panel.X+16, panel.W-32
	y := panel.Y + 14
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, title, pw), ColorTextPrimary(), px, y)
	y += fonts.bodyH + 4
	renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, caption, pw), ColorTextTertiary(), px, y)
	y += fonts.tinyH + 12
	colW := int32(0)
	for _, line := range lines {
		w, _ := measureText(fonts.small, line.Button)
		colW = max32(colW, w)
	}
	colW = min(colW+18, pw*2/5)
	for _, line := range lines {
		if y+fonts.smallH > panel.Y+panel.H-8 {
			break
		}
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, line.Button, colW-12), glassAccent(), px, y)
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, line.Action, pw-colW), ColorTextPrimary(), px+colW, y)
		y += lineH
	}

	// What the focused row decides, and what stops it.
	first, second, warn := ctrlLayoutPickerNotes(focus, isLayout)
	noteY := bottom + 4
	renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, first, innerW), ColorTextSecondary(), card.X+OverlayPadX, noteY)
	secondInk := ColorTextTertiary()
	if warn {
		secondInk = ColorWarning
	}
	renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, second, innerW), secondInk, card.X+OverlayPadX, noteY+fonts.tinyH+4)

	hints := []FooterHint{{"A", "This game"}}
	if system != "" {
		hints = append(hints, FooterHint{"X", "Every " + system + " game"})
	}
	hints = append(hints, FooterHint{"B", "Back"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hintLine(hints...), innerW), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// ctrlLayoutPickerRenderSheet draws the picker when it is open over the
// options sheet (renderGameOptionsPanel defers it).
func ctrlLayoutPickerRenderSheet(renderer *sdl.Renderer, config *Config) {
	if ctrlLayoutPickerOnSheet() {
		ctrlLayoutPickerRender(renderer, config)
	}
}

// ── The options sheet's row (game_options_ui.go) ────────────────────────

// ctrlLayoutSheetValue is the "Controls layout" row's value: the game's own
// choice, or "Default · " and its console's.
func ctrlLayoutSheetValue(game libraryGame) string {
	name := "None"
	if l, ok := ctrlLayoutForGame(game); ok {
		name = l.Name
	}
	if ctrlLayoutGameChoice(game.Path) == "" {
		return "Default · " + name
	}
	return name
}

// ctrlLayoutSheetHint is the row's second line.
func ctrlLayoutSheetHint(game libraryGame) string {
	if !ctrlLayoutOffered(game) {
		return "Not for this game: it keeps its own controls"
	}
	if note := ctrlLayoutNote(game.Path); note != "" {
		return "Last time: " + note
	}
	layouts := ctrlLayoutsFor(game.System)
	if len(layouts) == 0 {
		return "No layouts for " + game.System + " yet · a file in control-layouts adds one"
	}
	// The console's own layouts, not a fixed list: a DOS game has no arcade one.
	names := layouts[0].Name
	if len(layouts) > 1 {
		names += " and " + itoa(len(layouts)-1) + " more"
	}
	return "For RetroArch: " + names + " · A opens the list"
}

// ctrlLayoutSheetOwn reports the game holding its own choice (the sheet draws
// the value in the focus colour).
func ctrlLayoutSheetOwn(game libraryGame) bool {
	return ctrlLayoutGameChoice(game.Path) != ""
}

// ── The game page (gamepage_more.go) ────────────────────────────────────

// ctrlLayoutChip is the page's "Controls: …" chip, from memory only.
func ctrlLayoutChip(game libraryGame) (string, int, bool) {
	if !ctrlLayoutOffered(game) || !ctrlLayoutStoreLoaded() {
		return "", 0, false
	}
	f := &ctrlLayoutPageFacts
	if f.path == game.Path && f.known && !f.retroArch {
		return "", 0, false
	}
	l, ok := ctrlLayoutForGame(game)
	if !ok {
		return "", 0, false
	}
	return "Controls: " + l.Name, gamePageToneNeutral, true
}

// ctrlLayoutPageOpened reads, off the main thread, what the chip needs.
func ctrlLayoutPageOpened(game libraryGame) {
	f := &ctrlLayoutPageFacts
	f.path, f.known, f.retroArch = game.Path, false, false
	if !ctrlLayoutOffered(game) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("controls", "checking the game's emulator failed: %v", r)
			}
		}()
		ctrlLayoutStoreLoad()
		retroArch, _, known := ctrlLayoutLauncherFacts(game, false)
		tbxPost(func() {
			if f.path == game.Path {
				f.known, f.retroArch = known, retroArch
			}
		})
	}()
}

func init() {
	gamePageRegisterExtra(gamePageExtra{
		ID:    "controls",
		Order: 40,
		Icon:  "gamepad",
		Row: func(game libraryGame) (string, string, bool) {
			if !ctrlLayoutOffered(game) {
				return "", "", false
			}
			// A console with no layouts gets no row, unless one is in use.
			l, ok := ctrlLayoutForGame(game)
			if !ok && len(ctrlLayoutsFor(game.System)) == 0 {
				return "", "", false
			}
			detail := "Console buttons"
			if ok {
				detail = l.Name
			}
			return "Controls", detail, true
		},
		Open: func(game libraryGame) { ctrlLayoutPickerOpen(game, ctrlLayoutHostPage) },
	})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "controls",
		IsOpen: func() bool { return ctrlLayoutPicker.open && ctrlLayoutPicker.host == ctrlLayoutHostPage },
		Key:    ctrlLayoutPickerKey,
		Pad:    ctrlLayoutPickerPad,
		Mouse:  ctrlLayoutPickerMouse,
		Render: ctrlLayoutPickerRender,
		Close:  func() { ctrlLayoutPickerCloseOn(ctrlLayoutHostPage) },
	})
	gamePageRegisterStatus(gamePageStatus{ID: "controls", Order: 40, Chip: ctrlLayoutChip})
	gamePageRegisterOpened(ctrlLayoutPageOpened)
}
