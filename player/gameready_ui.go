package main

// gameready_ui.go — the Game Ready card (gameready.go): "<Game> isn't ready
// yet", what is missing in plain words, and the buttons that fix it.
//
// One card, two uses. Held, it replaces a start that would have failed: A is
// the first finding's fix, X starts the game anyway (not when there is no
// emulator at all: there is nothing to start), Y checks again — and starts the
// game when nothing holds it any more — and B goes back. As a report (the game
// page's More → Ready check) the same findings are listed with a tick for each
// thing that was fine, and X turns the card off or on for that game.
//
// The text is the launch-failure card's (launch_errors.go): file names, then
// the full folder to copy them into, every line wrapped at spaces and then
// after a path separator, never ellipsized, and Up/Down scroll a card taller
// than the screen. The buttons are drawn as pills so a mouse can press them;
// their labels are the footer hints, in the device's button names.
//
// A fix that opens another page (BIOS Doctor, the Emulators page) takes the
// library down the way the library's own "Open in…" does (handoff.go), so
// closing that page brings the library — and the game's page — back.
// main.go routes input here right after the launch-failure card and draws
// the card just under it.

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// gameReadyLineKind picks a body line's font, colour and icon room.
type gameReadyLineKind uint8

const (
	gameReadyLineHeading gameReadyLineKind = iota
	gameReadyLineBody
	gameReadyLinePath
	gameReadyLineDim
	gameReadyLineTick
	gameReadyLineGap
)

// gameReadyLine is one wrapped line of the card's body. icon is drawn before
// the first line of a finding or a tick; indent lines up its continuation.
type gameReadyLine struct {
	kind   gameReadyLineKind
	text   string
	icon   string
	indent bool
}

// gameReadyButton is one pill of the card's button row.
type gameReadyButton struct {
	act  tbxAction
	hint FooterHint
	rect sdl.Rect
}

// gameReadyCard is the card's state. Main thread.
type gameReadyCard struct {
	open     bool
	report   bool
	game     libraryGame
	launcher string
	res      gameReadyResult
	// issues are the result's and the live ones, the holding ones first.
	issues  []gameReadyIssue
	checked []string
	// fixLabel and fix are A: the first finding's fix ("" none).
	fixLabel string
	fix      func()
	quiet    bool
	// scroll is the first body line drawn; maxScroll is set by the renderer.
	scroll    int
	maxScroll int
	lines     []gameReadyLine
	linesW    int32
	card      sdl.Rect
	buttons   []gameReadyButton
	// eatUp: the release of a click the card took is the card's too.
	eatUp bool
}

var gameReadyUI gameReadyCard

// gameReadyDetour remembers a page the card opened over a hidden library.
// BIOS Doctor's community panel does not bring the library back itself:
// gameReadyTick does when the player leaves BIOS Doctor. The Emulators page
// does (closeEmulatorPanel); the tick then only reads the page's chip again.
var gameReadyDetour struct {
	community bool
	emulators bool
}

// gameReadyOpenCard opens the card for game. Main thread.
func gameReadyOpenCard(game libraryGame, launcher string, res gameReadyResult, report bool) {
	live, liveChecked := gameReadyLiveIssues(launcher)
	issues := append(append([]gameReadyIssue(nil), res.issues...), live...)
	// The holding findings first, the rest under "Good to know"; each group
	// in the diagnosis's order (most certain first).
	var held, rest []gameReadyIssue
	for _, is := range issues {
		if is.block {
			held = append(held, is)
		} else {
			rest = append(rest, is)
		}
	}
	gameReadyUI = gameReadyCard{
		open:     true,
		report:   report,
		game:     game,
		launcher: launcher,
		res:      res,
		issues:   append(held, rest...),
		checked:  append(append([]string(nil), res.checked...), liveChecked...),
		quiet:    gameReadyQuiet(game.Path),
	}
	gameReadyUI.fixLabel, gameReadyUI.fix = gameReadyFixFor(game, res, gameReadyUI.issues)
}

// gameReadyClose shuts the card.
func gameReadyClose(sound bool) {
	open := gameReadyUI.open
	eat := gameReadyUI.eatUp
	gameReadyUI = gameReadyCard{eatUp: eat}
	if sound && open {
		PlayBackSound()
	}
}

// gameReadyFixFor is A: the fix of the first finding that has one.
func gameReadyFixFor(game libraryGame, res gameReadyResult, issues []gameReadyIssue) (string, func()) {
	system := game.System
	for _, is := range issues {
		switch is.kind {
		case gameReadyKindEmulator:
			if res.desktopOnly {
				continue
			}
			if emuMoreCoresAvailableFor(system, false) > 0 {
				return "Get cores", func() { emuMoreCoresStart(system) }
			}
			return "Open Emulators", func() { gameReadyOpenEmulators(system) }
		case gameReadyKindCore:
			if !is.block {
				continue
			}
			if emuMoreCoresAvailableFor(system, true) > 0 {
				return "Get cores", func() { emuMoreCoresStart(system) }
			}
			return "Open Emulators", func() { gameReadyOpenEmulators(system) }
		case gameReadyKindBIOS:
			return "Open BIOS Doctor", gameReadyOpenBIOSDoctor
		case gameReadyKindLocalPlay:
			if is.block {
				return "Remove missing players", gameReadyDropMissingPlayers
			}
		}
	}
	return "", nil
}

// ── The fixes ────────────────────────────────────────────────────────────

// gameReadyHideLibrary takes the library down for a page the fix opens, with
// the way back the library's own handoff leaves (handoff.go). It reports
// whether the library was open.
func gameReadyHideLibrary() bool {
	if !gameLib.open {
		return false
	}
	handoffReturn.active = true
	handoffReturn.system = gameLib.system
	handoffReturn.cursor = gameLib.cursor
	handoffReturn.scroll = gameLib.scroll
	handoffReturn.focus = gameLib.focus
	handoffReturn.query = gameLib.query
	gameLib.open = false
	return true
}

// gameReadyOpenEmulators opens the Emulators page on the console's tile;
// closing it brings the library back (closeEmulatorPanel).
func gameReadyOpenEmulators(system string) {
	hid := gameReadyHideLibrary()
	entries := emulatorEntries()
	idx := emulatorEntryForSystem(entries, system)
	openEmulatorPanel()
	if idx >= 0 {
		emuPanel.cursor = clampPanelIndex(idx, len(entries))
	}
	gameReadyDetour.emulators = hid
}

// gameReadyOpenBIOSDoctor opens BIOS Doctor with a fresh scan.
func gameReadyOpenBIOSDoctor() {
	hid := gameReadyHideLibrary()
	biosScanStart()
	openCommunityPanel(appConfig, communityPageBIOSDoctor)
	gameReadyDetour.community = hid
	PlayActivateSound()
}

// gameReadyDropMissingPlayers takes the players whose controller is gone out
// of Local play, then checks again.
func gameReadyDropMissingPlayers() {
	var kept []hotseatPlayer
	removed := 0
	for _, p := range hotseat.players {
		if hotseatJoypadIndex(p.id) < 0 {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	hotseat.players = kept
	if removed > 0 {
		ShowToast("Removed "+pluralizeCount(removed, "missing player", "missing players")+" from Local play", ToastKindInfo)
	}
}

// gameReadyTick brings the library back when the player leaves BIOS Doctor:
// its B goes to the community hub's own page, which was never asked for
// here, so that closes too. Every frame (gameReadyRender).
func gameReadyTick() {
	if gameReadyDetour.community && (!communityPanel.open || communityPanel.page != communityPageBIOSDoctor) {
		gameReadyDetour.community = false
		communityPanel.open = false
		restoreLibraryFromHandoff()
		gameReadyRefreshPage()
	}
	if gameReadyDetour.emulators && !emuPanel.open {
		gameReadyDetour.emulators = false
		gameReadyRefreshPage()
	}
}

// ── Actions ──────────────────────────────────────────────────────────────

// gameReadyPlayable reports whether X can start the game: there is an
// emulator to start it in.
func gameReadyPlayable() bool {
	for _, is := range gameReadyUI.issues {
		if is.kind == gameReadyKindEmulator {
			return false
		}
	}
	return strings.TrimSpace(gameReadyUI.launcher) != ""
}

// gameReadyQuietOffered reports whether the report offers X to turn the card
// off or on for the game.
func gameReadyQuietOffered() bool {
	ui := &gameReadyUI
	return ui.report && (ui.quiet || (gameReadyHolds(ui.issues) && gameReadyPlayable()))
}

// gameReadyStart starts the game the card was about, past the gate.
func gameReadyStart(game libraryGame, launcher, note string) {
	gameReadyGrantPass(game.Path)
	if err := launchLibraryGame(game, launcher); err != nil {
		ShowToast("Cannot launch "+game.Label()+": "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("Playing "+game.Label()+note, ToastKindSuccess)
}

// gameReadyPlayAnyway is X on a held card.
func gameReadyPlayAnyway() {
	ui := &gameReadyUI
	if !gameReadyPlayable() {
		return
	}
	game, launcher := ui.game, ui.launcher
	quieted := gameReadyNotePlayedAnyway(game.Path)
	logInfo("library", "Game Ready: %s started anyway", filepath.Base(game.Path))
	gameReadyClose(false)
	note := ""
	if quieted {
		note = " · no more cards for it (its page → More → Ready check)"
	}
	gameReadyStart(game, launcher, note)
}

// gameReadyToggleQuiet is X on a report.
func gameReadyToggleQuiet() {
	ui := &gameReadyUI
	if !gameReadyQuietOffered() {
		return
	}
	ui.quiet = !ui.quiet
	gameReadySetQuiet(ui.game.Path, ui.quiet)
	ui.lines = nil
	PlayActivateSound()
	if ui.quiet {
		ShowToast(ui.game.Label()+" starts without this card from now on", ToastKindInfo)
	} else {
		ShowToast(ui.game.Label()+" shows this card again when something is missing", ToastKindInfo)
	}
}

// gameReadyCheckAgain is Y: everything is read again. A held start that
// nothing holds any more starts.
func gameReadyCheckAgain() {
	ui := &gameReadyUI
	game, launcher, report := ui.game, ui.launcher, ui.report
	if launcher == "" {
		// An emulator may have been installed meanwhile.
		launcher = strings.TrimSpace(resolveGameLauncherFor(game))
	}
	gameReadyForget(game.Path)
	res := gameReadyFor(game, launcher, !report)
	live, _ := gameReadyLiveIssues(launcher)
	if !report && !gameReadyHolds(append(append([]gameReadyIssue(nil), res.issues...), live...)) {
		gameReadyClose(false)
		gameReadyStart(game, launcher, "")
		return
	}
	eat := ui.eatUp
	gameReadyOpenCard(game, launcher, res, report)
	gameReadyUI.eatUp = eat
	PlayNavSound()
	if !report {
		ShowToast("Checked again · still not ready", ToastKindWarning)
	}
}

// gameReadyRunFix is A.
func gameReadyRunFix() {
	fix := gameReadyUI.fix
	if fix == nil {
		return
	}
	if gameReadyUI.fixLabel == "Remove missing players" {
		fix()
		gameReadyCheckAgain()
		return
	}
	// The page the fix opens plays its own sound.
	path := gameReadyUI.game.Path
	gameReadyClose(false)
	gameReadyForget(path)
	fix()
	// The page's chip is read again (Get cores leaves the page up; a page the
	// fix opened reads it again on the way back, gameReadyTick).
	gameReadyRefreshPage()
}

// gameReadyScroll moves a tall card by delta lines.
func gameReadyScroll(delta int) {
	ui := &gameReadyUI
	next := min(max(ui.scroll+delta, 0), ui.maxScroll)
	if next != ui.scroll {
		ui.scroll = next
		PlayNavSound()
	}
}

// gameReadyAction is the card's input.
func gameReadyAction(act tbxAction) {
	switch act {
	case tbxActUp:
		gameReadyScroll(-2)
	case tbxActDown:
		gameReadyScroll(2)
	case tbxActPageUp:
		gameReadyScroll(-8)
	case tbxActPageDown:
		gameReadyScroll(8)
	case tbxActConfirm:
		gameReadyRunFix()
	case tbxActX:
		if gameReadyUI.report {
			gameReadyToggleQuiet()
		} else {
			gameReadyPlayAnyway()
		}
	case tbxActY:
		gameReadyCheckAgain()
	case tbxActBack:
		gameReadyClose(true)
	}
}

// gameReadyKey takes every key while the card is up (main.go).
func gameReadyKey(e *sdl.KeyboardEvent) bool {
	if !gameReadyUI.open || e == nil {
		return false
	}
	if e.Type == sdl.KEYDOWN {
		gameReadyAction(tbxKeyAction(e))
	}
	return true
}

// gameReadyPad takes every button press while the card is up (main.go).
func gameReadyPad(e *sdl.ControllerButtonEvent) bool {
	if !gameReadyUI.open || e == nil {
		return false
	}
	gameReadyAction(tbxPadAction(e))
	return true
}

// gameReadyWheel scrolls a tall card and keeps the wheel from the screen
// behind it (main.go's wheel routing, beside the emulator picker's).
func gameReadyWheel(dy int32) bool {
	if !gameReadyUI.open {
		return false
	}
	switch {
	case dy > 0:
		gameReadyScroll(-2)
	case dy < 0:
		gameReadyScroll(2)
	}
	return true
}

// gameReadyMouse takes the left button while the card is up, and the release
// of a click it took (main.go): a pill presses its button, a click off the
// card goes back.
func gameReadyMouse(down bool, mx, my int32) bool {
	ui := &gameReadyUI
	if recoveryCard.open || launchFailure.open {
		// Those cards sit above this one and take the click first.
		return false
	}
	if !ui.open {
		if !down && ui.eatUp {
			ui.eatUp = false
			return true
		}
		return false
	}
	if !down {
		ui.eatUp = false
		return true
	}
	ui.eatUp = true
	for _, b := range ui.buttons {
		if b.act != tbxActNone && pointInRect(mx, my, b.rect) {
			gameReadyAction(b.act)
			return true
		}
	}
	if ui.card.W > 0 && !pointInRect(mx, my, ui.card) {
		gameReadyAction(tbxActBack)
	}
	return true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gameReadyParagraphs is the card's body before wrapping.
func gameReadyParagraphs(ui *gameReadyCard) []gameReadyLine {
	var p []gameReadyLine
	add := func(kind gameReadyLineKind, text, icon string) {
		if kind == gameReadyLineGap || strings.TrimSpace(text) != "" {
			p = append(p, gameReadyLine{kind: kind, text: text, icon: icon})
		}
	}
	var held, rest []gameReadyIssue
	for _, is := range ui.issues {
		if is.block {
			held = append(held, is)
		} else {
			rest = append(rest, is)
		}
	}
	switch {
	case len(held) > 0 && held[0].kind == gameReadyKindLocalPlay && len(held) == 1:
		add(gameReadyLineBody, "Local play isn't set up for this start:", "")
	case len(held) > 0 && ui.report:
		add(gameReadyLineBody, "It won't start until this is fixed:", "")
	case len(held) > 0:
		add(gameReadyLineBody, "It can't start until this is fixed:", "")
	case len(rest) > 0:
		add(gameReadyLineBody, "It should start. Good to know:", "")
	default:
		add(gameReadyLineBody, "Everything it needs is here.", "")
	}
	add(gameReadyLineGap, "", "")
	issue := func(n int, is gameReadyIssue, icon string) {
		heading := is.Title
		if n > 0 {
			heading = strconv.Itoa(n) + ". " + heading
		}
		add(gameReadyLineHeading, heading, icon)
		if len(is.Files) > 0 {
			add(gameReadyLineDim, "Missing:", "")
			for _, f := range is.Files {
				add(gameReadyLinePath, "• "+f, "")
			}
		}
		if is.Where != "" {
			// A finding about one file names that file (launch_errors.go):
			// told from a folder by its extension.
			if len(is.Files) == 0 && filepath.Ext(is.Where) != "" {
				add(gameReadyLineDim, "Location:", "")
			} else {
				add(gameReadyLineDim, "Copy to:", "")
			}
			add(gameReadyLinePath, is.Where, "")
		}
		add(gameReadyLineBody, is.Detail, "")
		add(gameReadyLineGap, "", "")
	}
	for i, is := range held {
		n := 0
		if len(held) > 1 {
			n = i + 1
		}
		issue(n, is, "warning")
	}
	if len(rest) > 0 && len(held) > 0 {
		add(gameReadyLineDim, "Good to know", "")
	}
	for _, is := range rest {
		issue(0, is, "info")
	}
	if ui.report && len(ui.checked) > 0 {
		add(gameReadyLineDim, "Checked", "")
		for _, c := range ui.checked {
			add(gameReadyLineTick, c, "check")
		}
		add(gameReadyLineGap, "", "")
	}
	if ui.quiet {
		add(gameReadyLineDim, "This game starts without this card: you asked not to be asked.", "")
	}
	return p
}

// gameReadyButtons is the button row for the card as it is.
func gameReadyButtons(ui *gameReadyCard) []gameReadyButton {
	var out []gameReadyButton
	if ui.maxScroll > 0 {
		out = append(out, gameReadyButton{act: tbxActNone, hint: FooterHint{"Up/Down", "Scroll"}})
	}
	if ui.fixLabel != "" {
		out = append(out, gameReadyButton{act: tbxActConfirm, hint: FooterHint{"A", ui.fixLabel}})
	}
	switch {
	case !ui.report && gameReadyPlayable():
		out = append(out, gameReadyButton{act: tbxActX, hint: FooterHint{"X", "Play anyway"}})
	case gameReadyQuietOffered() && ui.quiet:
		out = append(out, gameReadyButton{act: tbxActX, hint: FooterHint{"X", "Ask again"}})
	case gameReadyQuietOffered():
		out = append(out, gameReadyButton{act: tbxActX, hint: FooterHint{"X", "Don't ask again"}})
	}
	out = append(out,
		gameReadyButton{act: tbxActY, hint: FooterHint{"Y", "Check again"}},
		gameReadyButton{act: tbxActBack, hint: FooterHint{"B", "Back"}})
	return out
}

// gameReadyRender draws the card (main.go, every frame).
func gameReadyRender(renderer *sdl.Renderer, config *Config) {
	gameReadyTick()
	ui := &gameReadyUI
	if !ui.open || renderer == nil || config == nil {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	cardW := min(int32(820), screenWidth-48)
	textW := cardW - 2*OverlayPadX
	iconS := max32(fonts.bodyH, 22)
	iconW := iconS + 10
	if ui.lines == nil || ui.linesW != textW {
		ui.lines = ui.lines[:0]
		for _, para := range gameReadyParagraphs(ui) {
			if para.kind == gameReadyLineGap {
				ui.lines = append(ui.lines, para)
				continue
			}
			font := fonts.small
			if para.kind == gameReadyLineHeading {
				font = fonts.body
			}
			width := textW
			if para.icon != "" {
				width -= iconW
			}
			wrapped := launchWrapText(func(s string) int32 {
				w, _ := measureText(font, s)
				return w
			}, para.text, width)
			for i, text := range wrapped {
				line := gameReadyLine{kind: para.kind, text: text, indent: para.icon != ""}
				if i == 0 {
					line.icon = para.icon
				}
				ui.lines = append(ui.lines, line)
			}
		}
		if ui.lines == nil {
			ui.lines = []gameReadyLine{}
		}
		ui.linesW = textW
	}
	lineH := func(kind gameReadyLineKind) int32 {
		switch kind {
		case gameReadyLineHeading:
			return fonts.bodyH + 6
		case gameReadyLineTick:
			return max32(fonts.smallH, iconS) + 6
		case gameReadyLineGap:
			return 10
		}
		return fonts.smallH + 4
	}

	title := ui.game.Label() + " isn't ready yet"
	glyph := "warning"
	switch {
	case gameReadyHolds(ui.issues):
	case len(ui.issues) > 0:
		title, glyph = ui.game.Label()+" should start", "info"
	default:
		title, glyph = ui.game.Label()+" is ready", "check"
	}
	sub := ui.game.System
	if ui.res.emulator != "" {
		sub += " · " + ui.res.emulator
	}
	header := UIPanelHeader(title, sub, glyph)
	headerH := UIPanelHeaderBand(config, header, 10)
	pillH := fonts.smallH + 14
	footH := pillH + 24
	contentH := int32(0)
	for _, line := range ui.lines {
		contentH += lineH(line.kind)
	}
	cardH := min(max(headerH+contentH+footH+12, 260), screenHeight-48)
	bodyTop := headerH + 6
	bodyH := cardH - bodyTop - footH
	// The furthest the card scrolls: the first line from which the rest fits.
	ui.maxScroll = 0
	rest := int32(0)
	for i := len(ui.lines) - 1; i >= 0; i-- {
		rest += lineH(ui.lines[i].kind)
		if rest > bodyH {
			ui.maxScroll = i + 1
			break
		}
	}
	ui.scroll = min(ui.scroll, ui.maxScroll)

	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	x := card.X + OverlayPadX
	y := card.Y + bodyTop
	bottom := y + bodyH
	for i := ui.scroll; i < len(ui.lines); i++ {
		line := ui.lines[i]
		h := lineH(line.kind)
		if y+h > bottom {
			break
		}
		tx := x
		if line.indent {
			tx += iconW
		}
		if line.icon != "" {
			DrawHomeIcon(renderer, line.icon, sdl.Rect{X: x, Y: y + (h-6-iconS)/2, W: iconS, H: iconS}, false)
		}
		switch line.kind {
		case gameReadyLineHeading:
			renderText(renderer, config, fonts.body, line.text, ColorTextPrimary(), tx, y)
		case gameReadyLineTick:
			renderText(renderer, config, fonts.small, line.text, ColorTextPrimary(), tx, y+(h-6-fonts.smallH)/2)
		case gameReadyLinePath:
			renderText(renderer, config, fonts.small, line.text, ColorTextAccent(), tx, y)
		case gameReadyLineDim:
			renderText(renderer, config, fonts.small, line.text, ColorTextTertiary(), tx, y)
		case gameReadyLineBody:
			renderText(renderer, config, fonts.small, line.text, ColorTextSecondary(), tx, y)
		}
		y += h
	}

	// The button row: pills, from the left, as many as fit.
	ui.buttons = gameReadyButtons(ui)
	bx := x
	by := card.Y + card.H - footH + (footH-pillH)/2
	maxX := card.X + card.W - OverlayPadX
	for i := range ui.buttons {
		b := &ui.buttons[i]
		label := hintLine(b.hint)
		tw, th := measureText(fonts.small, label)
		w := tw + 24
		if bx+w > maxX {
			b.rect = sdl.Rect{}
			continue
		}
		b.rect = sdl.Rect{X: bx, Y: by, W: w, H: pillH}
		ink := ColorTextTertiary()
		if b.act != tbxActNone {
			ink = glassAccent()
			fillRoundedRect(renderer, b.rect.X, b.rect.Y, b.rect.W, b.rect.H, pillH/2, WithAlpha(ink, 36))
			strokeRoundedRect(renderer, b.rect.X, b.rect.Y, b.rect.W, b.rect.H, pillH/2, 1, WithAlpha(ink, 140))
		}
		color := ColorTextPrimary()
		if b.act == tbxActNone {
			color = ColorTextTertiary()
		}
		renderText(renderer, config, fonts.small, label, color, bx+12, by+(pillH-th)/2)
		bx += w + 10
	}
}
