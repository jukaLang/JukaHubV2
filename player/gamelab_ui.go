package main

// gamelab_ui.go — the Game Lab screen on a game's page (More → Game Lab).
//
// One card: setups A and B side by side (emulator, speed profile, clock
// limit, and a Run button each), the results table under them (speed, sound,
// battery, time played), the verdict line, the actions (Use A, Use B, Share)
// and the setups other players shared for this game. Left and Right move
// between the columns and buttons, A changes the value under the cursor
// (each one cycles through its choices) or presses the button.
//
// Run asks first, on a small card, because the game takes the screen: "Play
// for a minute or two, then quit the game". The Lab stays open under the
// game, and the result fills its column when JukaHub is back.
//
// The emulator choices are the ones the game's own start offers
// (findGameLaunchersFor, with the recommended one first, as the emulator
// picker lists them), read on a goroutine when the Lab opens, with the
// shared setups and the stored Lab. Drawing reads memory only.

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// gameLabChoice is one emulator the game can start in.
type gameLabChoice struct {
	Launcher string
	Name     string
	Kind     string
	Core     string
}

// The screen's rows: the setup grid, the actions, then one per shared setup.
const (
	gameLabRowEmu = iota
	gameLabRowCPU
	gameLabRowClock
	gameLabRowRun
	gameLabRowActions
	gameLabRowShared
)

// The action buttons, left to right.
const (
	gameLabActUseA = iota
	gameLabActUseB
	gameLabActShare
	gameLabActCount
)

// gameLabHit is a clickable place on the card.
type gameLabHit struct {
	rect sdl.Rect
	row  int
	col  int
}

// gameLabUI is the open screen. Main thread only.
var gameLabUI struct {
	open    bool
	loading bool
	gen     uint64
	game    libraryGame
	entry   gameLabEntry
	choices []gameLabChoice
	shared  []gameLabShared
	row     int
	col     int
	// confirm is the side the Run card asks about; "" when it is closed.
	confirm string
	hits    []gameLabHit
	card    sdl.Rect
	// The Run card's two buttons (start, cancel) and the card itself.
	confirmHits []sdl.Rect
	confirmCard sdl.Rect
}

func init() {
	gamePageRegisterExtra(gamePageExtra{ID: "gamelab", Order: 40, Icon: "bench", Row: gameLabMoreRow, Open: gameLabOpen})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "gamelab",
		IsOpen: func() bool { return gameLabUI.open },
		Key:    gameLabKey,
		Pad:    gameLabPad,
		Mouse:  gameLabMouse,
		Render: gameLabRender,
		Close:  gameLabClose,
	})
	gamePageRegisterStatus(gamePageStatus{ID: "gamelab", Order: 60, Chip: gameLabChip})
	gamePageRegisterOpened(func(game libraryGame) { gameLabPeek(game.Path) })
}

// gameLabSupported reports whether a game starts through an emulator the Lab
// can choose: not Android's, HTML5's or a port's.
func gameLabSupported(game libraryGame) bool {
	switch game.System {
	case androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		return false
	}
	return strings.TrimSpace(game.Path) != ""
}

// gameLabMoreRow is the More sheet's row.
func gameLabMoreRow(game libraryGame) (string, string, bool) {
	if !gameLabSupported(game) {
		return "", "", false
	}
	if side, ok := gameLabRunning(game.Path); ok {
		return "Game Lab", "Measuring setup " + strings.ToUpper(side) + "…", true
	}
	if e, ok := gameLabPeek(game.Path); ok {
		if e.Winner != "" && gameLabInUse(e.setup(e.Winner), gameOptionsFor(game.Path)) {
			return "Game Lab", "Using " + e.WinnerName, true
		}
		if e.ResultA != nil || e.ResultB != nil {
			line, _ := gameLabVerdict(e.ResultA, e.ResultB)
			return "Game Lab", line, true
		}
	}
	return "Game Lab", "Try two emulators or speed settings and compare them", true
}

// gameLabChip is the page's chip: the setup the player chose in the Lab, while
// the game's options still start it that way.
func gameLabChip(game libraryGame) (string, int, bool) {
	key := gameOptionsKey(game.Path)
	var s gameLabSetup
	name := ""
	gameLabStore.Lock()
	if e := gameLabStore.games[key]; gameLabStore.loaded && e != nil && e.Winner != "" {
		s, name = e.setup(e.Winner), e.WinnerName
	}
	gameLabStore.Unlock()
	if name == "" || !gameLabInUse(s, gameOptionsFor(game.Path)) {
		return "", 0, false
	}
	return "Lab: " + name, gamePageToneGood, true
}

// ── Opening ──────────────────────────────────────────────────────────────

// gameLabListChoices lists the emulators game can start in, as the emulator
// picker does. Off the main thread: it reads launch scripts.
func gameLabListChoices(game libraryGame) []gameLabChoice {
	options := pico8NativeLaunchers(game.System, findGameLaunchersFor(game.System, game.Path, emulatorEntries()))
	options, _ = emuBestOrder(game.System, options)
	out := make([]gameLabChoice, 0, len(options))
	for _, e := range options {
		c := gameLabChoice{Launcher: e.Launch, Name: e.Name, Kind: "standalone"}
		switch {
		case e.Extra == emuChoiceCore:
			c.Kind = "core"
		case retroArchLauncher(e.Launch) || strings.EqualFold(filepath.Ext(e.Launch), ".dll"):
			c.Kind = "retroarch"
		}
		if c.Kind != "standalone" {
			c.Core = gameCoreFromLauncher(e.Launch, game.System)
		}
		out = append(out, c)
	}
	return out
}

// gameLabOpen opens the Lab for game (the More sheet's row).
func gameLabOpen(game libraryGame) {
	ui := &gameLabUI
	ui.open, ui.loading = true, true
	ui.gen++
	ui.game = game
	ui.entry = gameLabEntry{}
	ui.choices, ui.shared = nil, nil
	ui.row, ui.col, ui.confirm = gameLabRowRun, 0, ""
	gen := ui.gen
	go func() {
		gameLabEnsureLoaded()
		choices := gameLabListChoices(game)
		current := resolveGameLauncherFor(game)
		opts := gameOptionsFor(game.Path)
		shared := gameLabScanImports(game)
		tbxPost(func() { gameLabLoaded(gen, choices, current, opts, shared) })
	}()
}

// gameLabLoaded fills the screen once its reads are in. A Lab the game
// already has keeps its setups; a new one starts from what the game uses
// now (A) and the next choice (B), or the same emulator at another speed
// when there is only one.
func gameLabLoaded(gen uint64, choices []gameLabChoice, current string, opts gameOptions, shared []gameLabShared) {
	ui := &gameLabUI
	if !ui.open || ui.gen != gen {
		return
	}
	ui.loading = false
	ui.choices, ui.shared = choices, shared
	if e, ok := gameLabPeek(ui.game.Path); ok {
		ui.entry = e
		ui.entry.A = gameLabRefresh(ui.entry.A, choices)
		ui.entry.B = gameLabRefresh(ui.entry.B, choices)
		return
	}
	if len(choices) == 0 {
		return
	}
	ia := 0
	for i, c := range choices {
		if gameLabSameLauncher(c.Launcher, current) {
			ia = i
			break
		}
	}
	a := gameLabSetupOf(choices[ia], opts.CPUProfile, opts.MaxFreqKHz)
	b := gameLabSetupOf(choices[ia], gameLabOtherProfile(opts.CPUProfile), 0)
	if len(choices) > 1 {
		b = gameLabSetupOf(choices[(ia+1)%len(choices)], opts.CPUProfile, opts.MaxFreqKHz)
	}
	ui.entry = gameLabEntry{System: ui.game.System, Title: ui.game.Title, A: a, B: b}
}

// gameLabSetupOf is a setup with choice's emulator.
func gameLabSetupOf(c gameLabChoice, profile string, khz int64) gameLabSetup {
	return gameLabSetup{Launcher: c.Launcher, Name: c.Name, Kind: c.Kind, Core: c.Core, Profile: normaliseGameCPUProfile(profile), MaxFreqKHz: khz}
}

// gameLabRefresh updates a stored setup's names from the emulators here.
func gameLabRefresh(s gameLabSetup, choices []gameLabChoice) gameLabSetup {
	for _, c := range choices {
		if gameLabSameLauncher(c.Launcher, s.Launcher) {
			s.Name, s.Kind, s.Core = c.Name, c.Kind, c.Core
		}
	}
	return s
}

// gameLabChoiceIndex is the index of s's emulator in the choices, or -1.
func gameLabChoiceIndex(s gameLabSetup) int {
	for i, c := range gameLabUI.choices {
		if gameLabSameLauncher(c.Launcher, s.Launcher) {
			return i
		}
	}
	return -1
}

// gameLabClose shuts the Lab without a sound (the page is closing). A run in
// progress goes on and keeps its result; a Run press whose launch a card or a
// download still holds goes with the Lab (gameLabDropPending).
func gameLabClose() {
	gameLabUI.open = false
	gameLabUI.confirm = ""
	gameLabUI.gen++
	gameLabDropPending()
}

// ── Changing setups ──────────────────────────────────────────────────────

// gameLabSideOf is the side of a grid column.
func gameLabSideOf(col int) string {
	if col == 1 {
		return gameLabSideB
	}
	return gameLabSideA
}

// gameLabSetSide stores a changed setup; its old result no longer describes
// it and goes. The other side is written too when the Lab is new.
func gameLabSetSide(side string, s gameLabSetup) {
	ui := &gameLabUI
	if side == gameLabSideB {
		ui.entry.B, ui.entry.ResultB = s, nil
	} else {
		ui.entry.A, ui.entry.ResultA = s, nil
	}
	gameLabCommit(side)
}

// gameLabCommit writes the screen's setups to the store: side's setup and
// its result as the screen has them, the other side's setup only for a new
// Lab (a result that came in meanwhile is never overwritten). "" writes
// neither side's result.
func gameLabCommit(side string) {
	ui := &gameLabUI
	game, a, b := ui.game, ui.entry.A, ui.entry.B
	ra, rb := ui.entry.ResultA, ui.entry.ResultB
	gameLabUpdate(game.Path, true, func(e *gameLabEntry) {
		if e.System == "" {
			e.System, e.Title = game.System, game.Title
		}
		switch side {
		case gameLabSideA:
			e.A, e.ResultA = a, ra
		case gameLabSideB:
			e.B, e.ResultB = b, rb
		}
		if e.A.Launcher == "" {
			e.A = a
		}
		if e.B.Launcher == "" {
			e.B = b
		}
	})
}

// gameLabBusySide reports whether side's run is waiting or measuring now.
func gameLabBusySide(side string) bool {
	s, ok := gameLabRunning(gameLabUI.game.Path)
	return ok && s == side
}

// gameLabChange is A on a grid value: the next emulator, profile or clock.
func gameLabChange(row, col int) {
	ui := &gameLabUI
	side := gameLabSideOf(col)
	if _, ok := gameLabRunning(ui.game.Path); ok {
		ShowToast("Wait for the Lab run to finish first", ToastKindInfo)
		return
	}
	s := ui.entry.setup(side)
	switch row {
	case gameLabRowEmu:
		if len(ui.choices) < 2 {
			ShowToast("This game has one emulator here; compare speed settings instead", ToastKindInfo)
			return
		}
		next := ui.choices[(gameLabChoiceIndex(s)+1+len(ui.choices))%len(ui.choices)]
		s.Launcher, s.Name, s.Kind, s.Core = next.Launcher, next.Name, next.Kind, next.Core
	case gameLabRowCPU:
		i := 0
		for j, p := range gameCPUProfiles {
			if p == normaliseGameCPUProfile(s.Profile) {
				i = j
			}
		}
		s.Profile = gameCPUProfiles[(i+1)%len(gameCPUProfiles)]
	case gameLabRowClock:
		ladder := gameOptionsCeilings()
		s.MaxFreqKHz = ladder[(gameOptionCeilingIndex(s.MaxFreqKHz)+1)%len(ladder)]
	default:
		return
	}
	PlayNavSound()
	gameLabSetSide(side, s)
}

// gameLabAskRun opens the Run card for side.
func gameLabAskRun(side string) {
	ui := &gameLabUI
	if gameLabBusySide(side) {
		ShowToast("Setup "+strings.ToUpper(side)+" is being measured", ToastKindInfo)
		return
	}
	if gameLabBusy() {
		ShowToast("Wait for the Lab run to finish first", ToastKindInfo)
		return
	}
	s := ui.entry.setup(side)
	if !gameLauncherStillUsable(s.Launcher) {
		ShowToast(s.Name+" cannot start here any more; pick another emulator", ToastKindWarning)
		return
	}
	ui.confirm = side
	PlayActivateSound()
}

// gameLabRunConfirmed starts the run the card asked about.
func gameLabRunConfirmed() {
	ui := &gameLabUI
	side := ui.confirm
	ui.confirm = ""
	if side == "" {
		return
	}
	gameLabCommit("")
	if err := gameLabStartRun(ui.game, side, ui.entry.setup(side)); err != nil {
		ShowToast("Cannot start "+ui.game.Label()+": "+err.Error(), ToastKindError)
		PlayBackSound()
		return
	}
	// A launch a card or a download holds has its own sound and words.
	if _, started := gameLabRunning(ui.game.Path); started {
		PlaySuccessSound()
	}
}

// gameLabResultIn is a finished run, on the main thread: the screen shows it
// and a toast says it.
func gameLabResultIn(game libraryGame, side string, r gameLabResult) {
	ui := &gameLabUI
	if ui.open && !ui.loading && gameOptionsKey(ui.game.Path) == gameOptionsKey(game.Path) {
		if e, ok := gameLabPeek(game.Path); ok {
			ui.entry = e
			ui.entry.A = gameLabRefresh(ui.entry.A, ui.choices)
			ui.entry.B = gameLabRefresh(ui.entry.B, ui.choices)
		}
	}
	name := "Setup " + strings.ToUpper(side)
	switch {
	case r.Short:
		ShowToast(fmt.Sprintf("%s played %s — too short to judge; play at least a minute", name, gameLabPlayedText(r.Played)), ToastKindInfo)
	case r.FPS > 0:
		ShowToast(fmt.Sprintf("Game Lab: %s measured · %s fps · More → Game Lab to compare", name, gameLabNum(r.FPS)), ToastKindSuccess)
	default:
		ShowToast("Game Lab: "+name+" measured · More → Game Lab to compare", ToastKindSuccess)
	}
}

// gameLabAction is A on one of the action buttons.
func gameLabAction(col int) {
	ui := &gameLabUI
	switch col {
	case gameLabActUseA, gameLabActUseB:
		side := gameLabSideA
		if col == gameLabActUseB {
			side = gameLabSideB
		}
		if ui.entry.result(side) == nil {
			ShowToast("Run "+strings.ToUpper(side)+" first, then decide", ToastKindInfo)
			return
		}
		gameLabCommit("")
		gameLabApply(ui.game, side)
		if e, ok := gameLabPeek(ui.game.Path); ok {
			ui.entry.Winner, ui.entry.WinnerName = e.Winner, e.WinnerName
		}
	case gameLabActShare:
		side := gameLabShareSide()
		if side == "" {
			ShowToast("Run a setup first; Share saves the one you choose", ToastKindInfo)
			return
		}
		PlayActivateSound()
		go gameLabExport(ui.game, ui.entry.clone(), side)
	}
}

// gameLabShareSide is the setup Share saves: the one the player applied,
// else the one the verdict favours, else the only one measured.
func gameLabShareSide() string {
	e := &gameLabUI.entry
	if e.Winner != "" && e.result(e.Winner) != nil {
		return e.Winner
	}
	if _, side := gameLabVerdict(e.ResultA, e.ResultB); side != "" {
		return side
	}
	switch {
	case e.ResultA != nil && e.ResultB == nil:
		return gameLabSideA
	case e.ResultB != nil && e.ResultA == nil:
		return gameLabSideB
	}
	return ""
}

// gameLabTryShared loads a shared setup into B, when its emulator is here.
// It never starts the game.
func gameLabTryShared(i int) {
	ui := &gameLabUI
	if i < 0 || i >= len(ui.shared) {
		return
	}
	if _, ok := gameLabRunning(ui.game.Path); ok {
		ShowToast("Wait for the Lab run to finish first", ToastKindInfo)
		return
	}
	p := ui.shared[i].Profile
	c, ok := gameLabMatchChoice(p, ui.choices)
	if !ok {
		ShowToast(p.Setup.Emulator+" is not installed here, so this setup cannot be tried", ToastKindWarning)
		return
	}
	khz := int64(0)
	if p.Setup.MaxFreqKHz > 0 {
		khz = gameOptionsCeilings()[gameOptionCeilingIndex(p.Setup.MaxFreqKHz)]
	}
	gameLabSetSide(gameLabSideB, gameLabSetupOf(c, p.Setup.CPUProfile, khz))
	ui.row, ui.col = gameLabRowRun, 1
	PlayActivateSound()
	ShowToast("Loaded as setup B: "+gameLabSetupName(ui.entry.B)+" · Run B to try it", ToastKindSuccess)
}

// ── Input ────────────────────────────────────────────────────────────────

// gameLabRows is the number of rows the cursor can be on.
func gameLabRows() int { return gameLabRowShared + len(gameLabUI.shared) }

// gameLabDo is one action on the screen.
func gameLabDo(act tbxAction) {
	ui := &gameLabUI
	if ui.confirm != "" {
		switch act {
		case tbxActConfirm:
			gameLabRunConfirmed()
		case tbxActBack:
			ui.confirm = ""
			PlayBackSound()
		}
		return
	}
	if act == tbxActBack {
		gameLabClose()
		PlayBackSound()
		return
	}
	if ui.loading || (len(ui.choices) == 0 && ui.entry.A.Launcher == "") {
		return
	}
	cols := func(row int) int {
		switch {
		case row <= gameLabRowRun:
			return 2
		case row == gameLabRowActions:
			return gameLabActCount
		}
		return 1
	}
	switch act {
	case tbxActUp:
		if ui.row > 0 {
			ui.row--
			ui.col = min(ui.col, cols(ui.row)-1)
			PlayNavSound()
		}
	case tbxActDown:
		if ui.row < gameLabRows()-1 {
			ui.row++
			ui.col = min(ui.col, cols(ui.row)-1)
			PlayNavSound()
		}
	case tbxActLeft:
		if ui.col > 0 {
			ui.col--
			PlayNavSound()
		}
	case tbxActRight:
		if ui.col < cols(ui.row)-1 {
			ui.col++
			PlayNavSound()
		}
	case tbxActConfirm:
		gameLabPress(ui.row, ui.col)
	}
}

// gameLabPress is A (or a click) on a place of the card.
func gameLabPress(row, col int) {
	switch {
	case row < gameLabRowRun:
		gameLabChange(row, col)
	case row == gameLabRowRun:
		gameLabAskRun(gameLabSideOf(col))
	case row == gameLabRowActions:
		gameLabAction(col)
	default:
		gameLabTryShared(row - gameLabRowShared)
	}
}

func gameLabKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	gameLabDo(tbxKeyAction(e))
	return true
}

func gameLabPad(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	gameLabDo(tbxPadAction(e))
	return true
}

func gameLabMouse(mx, my int32) bool {
	ui := &gameLabUI
	if ui.confirm != "" {
		switch {
		case len(ui.confirmHits) == 2 && pointInRect(mx, my, ui.confirmHits[0]):
			gameLabRunConfirmed()
		case len(ui.confirmHits) == 2 && pointInRect(mx, my, ui.confirmHits[1]),
			ui.confirmCard.W > 0 && !pointInRect(mx, my, ui.confirmCard):
			ui.confirm = ""
			PlayBackSound()
		}
		return true
	}
	for _, hit := range ui.hits {
		if pointInRect(mx, my, hit.rect) {
			ui.row, ui.col = hit.row, hit.col
			gameLabPress(hit.row, hit.col)
			return true
		}
	}
	if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
		gameLabClose()
		PlayBackSound()
	}
	return true
}

// ── What the table says ──────────────────────────────────────────────────

// gameLabPlayedText is a run's length: "45 s", "2 min 05 s".
func gameLabPlayedText(secs float64) string {
	s := int(math.Round(secs))
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %02d s", s/60, s%60)
}

// gameLabSpeedText is the Speed cell.
func gameLabSpeedText(r *gameLabResult) string {
	if r == nil {
		return "—"
	}
	if r.FPS > 0 && r.Target > 0 {
		pct := math.Min(r.FPS/r.Target*100, 100)
		return fmt.Sprintf("%s fps · %.0f%%", gameLabNum(r.FPS), math.Floor(pct))
	}
	switch r.FPSWhy {
	case "standalone":
		return "Not measured (standalone)"
	case "threaded":
		return "Not measured (threaded video)"
	case "short":
		return "Not measured (too short)"
	}
	return "Not measured"
}

// gameLabAudioText is the Audio cell.
func gameLabAudioText(r *gameLabResult) string {
	if r == nil {
		return "—"
	}
	switch {
	case !r.Audio && r.AudioWhy == "standalone":
		return "Not measured (standalone)"
	case !r.Audio:
		return "Not measured"
	case r.SoundLost:
		return "Sound lost"
	case r.Glitches == 0 && r.HasUnderrun:
		return fmt.Sprintf("None logged · %s%% near empty", gameLabNum(r.UnderrunPct))
	case r.Glitches == 0:
		return "None logged"
	}
	rate, _ := gameLabGlitchRate(r)
	return gameLabNum(rate) + " glitches/min"
}

// gameLabBatteryText is the Battery cell.
func gameLabBatteryText(r *gameLabResult) string {
	if r == nil {
		return "—"
	}
	switch {
	case r.Watts > 0:
		return gameLabNum(r.Watts) + " W"
	case r.PctPerHour > 0:
		return gameLabNum(r.PctPerHour) + "% an hour"
	case r.BatteryWhy == "charging":
		return "Not measured (charging)"
	case r.BatteryWhy == "nobattery":
		return "Not measured here"
	}
	return "Not measured (too short)"
}

// gameLabPlayedCell is the Played cell.
func gameLabPlayedCell(r *gameLabResult) string {
	if r == nil {
		return "—"
	}
	if r.Short {
		return gameLabPlayedText(r.Played) + " · too short"
	}
	return gameLabPlayedText(r.Played)
}

// gameLabValueText is a grid cell's value.
func gameLabValueText(row int, s gameLabSetup, side string) string {
	switch row {
	case gameLabRowEmu:
		if s.Name == "" {
			return filepath.Base(s.Launcher)
		}
		return s.Name
	case gameLabRowCPU:
		return gameLabProfileWord(s.Profile)
	case gameLabRowClock:
		if s.MaxFreqKHz <= 0 {
			return "No limit"
		}
		return formatGameCeiling(s.MaxFreqKHz)
	}
	if running, ok := gameLabRunning(gameLabUI.game.Path); ok && running == side {
		return "Measuring…"
	}
	if gameLabUI.entry.result(side) != nil {
		return "Run " + strings.ToUpper(side) + " again"
	}
	return "Run " + strings.ToUpper(side)
}

// ── Drawing ──────────────────────────────────────────────────────────────

func gameLabRender(renderer *sdl.Renderer, config *Config) {
	ui := &gameLabUI
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("Game Lab", ui.game.Label(), "bench")
	headerH := UIPanelHeaderBand(config, header, 10)
	pad := int32(OverlayPadX)
	cellH := fonts.bodyH + 16
	lineH := max32(fonts.smallH, fonts.tinyH) + 8
	footerH := fonts.smallH + 20
	cardW := min(int32(900), screenWidth-48)
	empty := ui.loading || (len(ui.choices) == 0 && ui.entry.A.Launcher == "")
	shown := len(ui.shared)
	body := func(shown int) int32 {
		h := lineH + 4*cellH + 10 + 5*lineH + lineH + 10 + cellH
		if shown > 0 {
			h += lineH + int32(shown)*cellH
		}
		return h
	}
	if empty {
		shown = 0
	}
	maxH := screenHeight - 32
	for shown > 0 && headerH+body(shown)+footerH+12 > maxH {
		shown--
	}
	cardH := headerH + body(shown) + footerH + 12
	if empty {
		cardH = headerH + 3*(fonts.bodyH+4) + 16 + footerH + 12
	}
	cardH = min(cardH, maxH)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	ui.hits = ui.hits[:0]
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	innerX, innerW := card.X+pad, card.W-2*pad
	y := card.Y + headerH
	footerY := card.Y + card.H - footerH + 4
	if empty {
		msg := "Looking for this game's emulators…"
		if !ui.loading {
			msg = "No emulator can start this game here. Add one on the Emulators screen."
		}
		for i, line := range wrapTextLines(fonts.body, msg, innerW, 3) {
			renderText(renderer, config, fonts.body, line, ColorTextSecondary(), innerX, y+8+int32(i)*(fonts.bodyH+4))
		}
		hint := hintLine(FooterHint{"B", "Back"})
		renderText(renderer, config, fonts.small, hint, ColorTextSecondary(), innerX, footerY)
		return
	}

	// The two columns: a label column for the table, then A and B.
	labelW := min(int32(150), innerW/5)
	colW := (innerW - labelW - 12) / 2
	colX := [2]int32{innerX + labelW, innerX + labelW + colW + 12}
	sides := [2]string{gameLabSideA, gameLabSideB}
	opts := gameOptionsFor(ui.game.Path)
	for i, side := range sides {
		title := "Setup " + strings.ToUpper(side)
		if ui.entry.Winner == side && gameLabInUse(ui.entry.setup(side), opts) {
			title += " · in use"
		}
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, title, colW-8), glassAccent(), colX[i]+8, y+(lineH-fonts.smallH)/2)
	}
	y += lineH
	rowNames := [4]string{"Emulator", "Speed", "Clock limit", ""}
	for row := gameLabRowEmu; row <= gameLabRowRun; row++ {
		if rowNames[row] != "" {
			renderText(renderer, config, fonts.small, ellipsize(fonts.small, rowNames[row], labelW-8), ColorTextSecondary(), innerX, y+(cellH-6-fonts.smallH)/2)
		}
		for i, side := range sides {
			rect := sdl.Rect{X: colX[i], Y: y, W: colW, H: cellH - 6}
			focused := ui.confirm == "" && ui.row == row && ui.col == i
			UIListRow(renderer, rect, glassRowRadius, focused, false)
			text := gameLabValueText(row, ui.entry.setup(side), side)
			tw, th := measureText(fonts.body, ellipsize(fonts.body, text, rect.W-24))
			tx := rect.X + 12
			if row == gameLabRowRun {
				tx = rect.X + (rect.W-tw)/2
			}
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, text, rect.W-24), ColorTextPrimary(), tx, rect.Y+(rect.H-th)/2)
			ui.hits = append(ui.hits, gameLabHit{rect: rect, row: row, col: i})
		}
		y += cellH
	}
	y += 10

	// The results table.
	ra, rb := ui.entry.ResultA, ui.entry.ResultB
	table := []struct {
		label string
		text  func(*gameLabResult) string
	}{
		{"Speed", gameLabSpeedText},
		{"Sound", gameLabAudioText},
		{"Battery", gameLabBatteryText},
		{"Played", gameLabPlayedCell},
	}
	fillRoundedRect(renderer, innerX, y, innerW, 5*lineH, 10, WithAlpha(ColorSurfaceRow, 120))
	renderText(renderer, config, fonts.tiny, "Results", ColorTextTertiary(), innerX+12, y+(lineH-fonts.tinyH)/2)
	y += lineH
	for _, line := range table {
		renderText(renderer, config, fonts.tiny, line.label, ColorTextSecondary(), innerX+12, y+(lineH-fonts.tinyH)/2)
		for i, r := range [2]*gameLabResult{ra, rb} {
			text := ellipsize(fonts.small, line.text(r), colW-16)
			renderText(renderer, config, fonts.small, text, ColorTextPrimary(), colX[i]+12, y+(lineH-fonts.smallH)/2)
		}
		y += lineH
	}
	verdict, favoured := gameLabVerdict(ra, rb)
	ink := ColorTextSecondary()
	if favoured != "" {
		ink = glassAccent()
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, verdict, innerW), ink, innerX, y+(lineH-fonts.smallH)/2+2)
	y += lineH + 10

	// The actions.
	labels := [gameLabActCount]string{"Use A", "Use B", "Share"}
	ready := [gameLabActCount]bool{ra != nil, rb != nil, gameLabShareSide() != ""}
	btnW := (innerW - 2*12) / gameLabActCount
	for i := 0; i < gameLabActCount; i++ {
		rect := sdl.Rect{X: innerX + int32(i)*(btnW+12), Y: y, W: btnW, H: cellH - 6}
		focused := ui.confirm == "" && ui.row == gameLabRowActions && ui.col == i
		UIListRow(renderer, rect, glassRowRadius, focused, false)
		color := ColorTextPrimary()
		if !ready[i] {
			color = ColorTextTertiary()
		}
		tw, th := measureText(fonts.body, labels[i])
		renderText(renderer, config, fonts.body, labels[i], color, rect.X+(rect.W-tw)/2, rect.Y+(rect.H-th)/2)
		ui.hits = append(ui.hits, gameLabHit{rect: rect, row: gameLabRowActions, col: i})
	}
	y += cellH

	// The shared setups for this game.
	if shown > 0 {
		renderText(renderer, config, fonts.small, "Shared setups", ColorTextSecondary(), innerX, y+(lineH-fonts.smallH)/2)
		y += lineH
		first := 0
		if cur := ui.row - gameLabRowShared; cur >= shown {
			first = cur - shown + 1
		}
		for i := first; i < len(ui.shared) && i < first+shown; i++ {
			rect := sdl.Rect{X: innerX, Y: y, W: innerW, H: cellH - 6}
			row := gameLabRowShared + i
			UIListRow(renderer, rect, glassRowRadius, ui.confirm == "" && ui.row == row, false)
			name, facts := gameLabSharedLabel(ui.shared[i])
			nw, nh := measureText(fonts.body, ellipsize(fonts.body, name, rect.W/2))
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, name, rect.W/2), ColorTextPrimary(), rect.X+12, rect.Y+(rect.H-nh)/2)
			renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, facts, rect.W-nw-40), ColorTextSecondary(), rect.X+nw+28, rect.Y+(rect.H-fonts.tinyH)/2)
			ui.hits = append(ui.hits, gameLabHit{rect: rect, row: row, col: 0})
			y += cellH
		}
	}

	verb := "Change"
	switch {
	case ui.row == gameLabRowRun:
		verb = "Run"
	case ui.row == gameLabRowActions && ui.col == gameLabActShare:
		verb = "Share"
	case ui.row == gameLabRowActions:
		verb = "Use"
	case ui.row >= gameLabRowShared:
		verb = "Try as B"
	}
	hint := hintLine(FooterHint{"D-Pad", "Move"}, FooterHint{"A", verb}, FooterHint{"B", "Back"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, innerW), ColorTextSecondary(), innerX, footerY)

	if ui.confirm != "" {
		gameLabRenderConfirm(renderer, config, fonts)
	}
}

// gameLabRenderConfirm draws the Run card over the Lab.
func gameLabRenderConfirm(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	ui := &gameLabUI
	side := ui.confirm
	s := ui.entry.setup(side)
	pad := int32(OverlayPadX)
	cardW := min(int32(560), screenWidth-96)
	innerW := cardW - 2*pad
	text := "Play for a minute or two, then quit the game. JukaHub measures its speed, sound and battery while you play."
	switch {
	case s.Kind == "standalone" && IsTrimuiDevice():
		text = "Play for a minute or two, then quit the game. A standalone emulator's speed and sound cannot be measured; its battery use can."
	case s.Kind == "standalone":
		text = "Play for a minute or two, then quit the game. A standalone emulator's speed and sound cannot be measured here."
	case !IsTrimuiDevice():
		// A PC reads RetroArch's own log; it has no handheld battery to read.
		text = "Play for a minute or two, then quit the game. JukaHub reads its speed and sound from RetroArch's log afterwards."
	}
	lines := wrapTextLines(fonts.small, text, innerW, 4)
	btnH := fonts.bodyH + 14
	cardH := 24 + fonts.bodyH + 6 + fonts.smallH + 14 + int32(len(lines))*(fonts.smallH+4) + 16 + btnH + 20
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.confirmCard = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	y := card.Y + 24
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, "Run "+strings.ToUpper(side), innerW), ColorTextPrimary(), card.X+pad, y)
	y += fonts.bodyH + 6
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, gameLabSetupName(s), innerW), glassAccent(), card.X+pad, y)
	y += fonts.smallH + 14
	for _, line := range lines {
		renderText(renderer, config, fonts.small, line, ColorTextSecondary(), card.X+pad, y)
		y += fonts.smallH + 4
	}
	y += 16
	btnW := (innerW - 12) / 2
	ui.confirmHits = ui.confirmHits[:0]
	// The buttons in this frame's vocabulary (keyboard keys on a PC without a
	// pad), like every hint.
	for i, label := range []string{hintLine(FooterHint{"A", "Start"}), hintLine(FooterHint{"B", "Cancel"})} {
		rect := sdl.Rect{X: card.X + pad + int32(i)*(btnW+12), Y: y, W: btnW, H: btnH}
		UIListRow(renderer, rect, glassRowRadius, i == 0, false)
		tw, th := measureText(fonts.body, label)
		renderText(renderer, config, fonts.body, label, ColorTextPrimary(), rect.X+(rect.W-tw)/2, rect.Y+(rect.H-th)/2)
		ui.confirmHits = append(ui.confirmHits, rect)
	}
}
