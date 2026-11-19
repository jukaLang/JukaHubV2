package main

// emupicker.go — "Which emulator?" when more than one can play a game.
//
// A console often has several emulators on the card or the PC: CrossMix ships
// a GBA folder and an mGBA one, Windows may have RetroArch's core beside
// standalone mGBA, a PlayStation game fits PCSX-ReARMed and DuckStation.
// findGameLaunchers (game_library.go) lists every usable one: the console's
// own launchers, the other emulators its Emus folder lists and, on the
// handheld, installed RetroArch cores no launcher starts (emuchoices.go).
//
// Until 2026-11-18 only the console's own launchers could open the question;
// the extras waited in the game options. Juka then asked for the question
// whenever a game has more than one core — and for more cores to choose from
// (emumorecores.go) — so every choice counts now. With two or more, starting a
// game (Games library, Continue, Home, Files, voice) opens this sheet, as
// often as the "Ask which emulator" setting says (Settings → Games &
// emulators, emuPickAskSetting):
//
//   - First time per game (the default): A plays and remembers the emulator
//     for that game (gameOptions.Emulator), so its next start goes straight
//     in; the game options' Emulator row changes it, or sets it back to
//     asking. Y plays once without remembering.
//   - Every launch: A plays (and only moves the highlight for next time); Y
//     always uses it for this game.
//   - Never: no sheet. The game starts in the emulator picked last, else the
//     one recommended for this hardware (emubest.go); the options row still
//     picks one.
//
// In both asking modes X uses the highlighted one for every game of the
// console (game-launchers.json; not for Arcade-like consoles,
// emuConsolePinOffered) — the options row's Left takes that back — and B
// cancels. The sheet's last row, when the buildbot has cores for the console
// that are not on the card, fetches them (emumorecores.go) instead of starting
// the game; the next start, once they are in, offers them.
//
// Each row says what its kind of emulator means for the game: JukaHub's save
// slots, Continue and RetroAchievements work through RetroArch only.
//
// What never asks: a pin for the game or the console, a single emulator, a
// NextUI pak, and every start that knows its emulator already — a save state
// from the game page, the resume after power-on (its marker names the
// launcher), a relaunch after a save sync — which use resolveGameLauncherFor
// or carry the launcher. The highlight starts on the emulator picked last,
// else the recommended one, which is the one such a game ran in: Continue on a
// game that never had the question is one press of A from where it was.

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The "Ask which emulator" setting: how often a game with more than one
// emulator asks.
const (
	emuPickAskSetting = "emuPickAsk"
	emuAskFirst       = "first"
	emuAskAlways      = "always"
	emuAskNever       = "never"
)

// emuPickAskModes is the order the setting offers them in.
var emuPickAskModes = []string{emuAskFirst, emuAskAlways, emuAskNever}

// emuPickAskModeFrom reads the setting; anything unknown is the default.
func emuPickAskModeFrom(c *Config) string {
	if c == nil {
		return emuAskFirst
	}
	s, _ := customString(c.Variables.Custom, emuPickAskSetting)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case emuAskAlways, "every", "each":
		return emuAskAlways
	case emuAskNever, "off", "auto":
		return emuAskNever
	}
	return emuAskFirst
}

// emuPickAskModeNow is the setting now (main thread: the Custom map is the
// main thread's).
func emuPickAskModeNow() string { return emuPickAskModeFrom(appConfig) }

// emuPickAskLabel names a mode in the setting.
func emuPickAskLabel(mode string) string {
	switch mode {
	case emuAskAlways:
		return "Every launch"
	case emuAskNever:
		return "Never — use the best one"
	}
	return "First time per game"
}

// emuPickAskSettingsRow is the choice in Settings → Games & emulators.
func emuPickAskSettingsRow() setRow {
	return setRow{
		ID: "emuPickAsk", Label: "Ask which emulator", Kind: setKindChoice,
		Detail: setStatic("When a game has more than one emulator or core"),
		Options: func(*Config) []setOption {
			notes := map[string]string{
				emuAskFirst:  "Asks once; the game remembers it (Game options → Emulator changes it)",
				emuAskAlways: "Asks every time; the last pick is highlighted",
				emuAskNever:  "Starts in the one used last, else the recommended one",
			}
			out := make([]setOption, 0, len(emuPickAskModes))
			for _, m := range emuPickAskModes {
				out = append(out, setOption{Value: m, Label: emuPickAskLabel(m), Note: notes[m]})
			}
			return out
		},
		Current: func(c *Config) int {
			cur := emuPickAskModeFrom(c)
			for i, m := range emuPickAskModes {
				if m == cur {
					return i
				}
			}
			return -1
		},
		Choose: func(c *Config, value string) {
			setStoreString(c, emuPickAskSetting, value)
			setToast("Ask which emulator: " + emuPickAskLabel(emuPickAskModeFrom(c)))
		},
	}
}

var emuPickUI struct {
	open    bool
	game    libraryGame
	options []emuEntry
	// details are the lines under the names, worked out once on open:
	// retroArchLauncher reads a launch script, too slow for every frame.
	details []string
	cursor  int
	scroll  int
	visible int
	then    func(launcher string)
	panel   sdl.Rect
	rowHits []gameLibRowHit
	// eatUp swallows the release of a click the sheet took (emuPickEatRelease).
	eatUp bool
	// recommended: options[0] is the emulator recommended for this hardware
	// (emubest.go), drawn with a badge.
	recommended bool
	// console: X may set the highlighted one for every game of the console
	// (emuConsolePinOffered), worked out on open.
	console bool
	// remember: A remembers the pick for the game (First time per game); in
	// Every launch, Y does.
	remember bool
	// more counts the cores the sheet's last row would fetch for the console
	// (emumorecores.go); 0 means no such row.
	more int
}

// emuPickAsks reports whether starting a game with these choices asks which:
// two or more, unless the setting is Never.
func emuPickAsks(choices []emuEntry) bool {
	return len(choices) >= 2 && emuPickAskModeNow() != emuAskNever
}

// gameLauncherPinned is the launcher a game was set to always use, or "" when
// none is set or its file is gone (then the picker asks again).
func gameLauncherPinned(opts gameOptions) string {
	if p := strings.TrimSpace(opts.Emulator); gameLauncherStillUsable(p) {
		return p
	}
	return ""
}

// gameLauncherStillUsable reports whether a remembered launcher can still
// start a game: its file is there and, for the official PICO-8, pico8.dat is
// still beside it. Without that check a PICO-8 whose data file was removed
// would be started anyway, without the switches that only apply to the real
// one, and fail.
func gameLauncherStillUsable(p string) bool {
	if p == "" || !regularFile(p) {
		return false
	}
	if emuIsCoreLaunch(p) && emuCoreLaunchCheck(p) != nil {
		// A core started directly (emuchoices.go) whose RetroArch is gone,
		// or cannot load it: the question again, not a start that fails.
		return false
	}
	return !pico8NameOnly(p) || pico8NativeIsLauncher(p)
}

// resolveGameLauncherFor is resolveGameLauncher for one game: the emulator it
// always uses, else the one picked last, else the system's. The save-state
// list, the diagnosis and Reset game read the emulator the game really runs
// in through it. Safe off the main thread.
func resolveGameLauncherFor(game libraryGame) string {
	if game.System == androidLibrarySystem || game.System == html5LibrarySystem || game.System == portsLibrarySystem {
		return resolveGameLauncher(game.System)
	}
	opts := gameOptionsFor(game.Path)
	if p := gameLauncherPinned(opts); p != "" {
		return p
	}
	if configured := configuredGameLauncher(game.System, loadGameLauncherOverrides(gameLauncherOverridesPath())); configured != "" {
		return configured
	}
	// On NextUI the game's pak, as chooseGameLauncher starts it (nextui.go).
	if pak := nextuiPakFor(game.Path); pak != "" {
		return pak
	}
	if last := strings.TrimSpace(opts.LastEmulator); gameLauncherStillUsable(last) {
		return last
	}
	// resolveGameLauncher's catalog step, for this game's own Roms folder:
	// of several, the one recommended for this hardware (emubest.go).
	// The user's own PICO-8 joins PICO-8's list (pico8native.go).
	if all := pico8NativeLaunchers(game.System, findGameLaunchersFor(game.System, game.Path, emulatorEntries())); len(all) > 0 {
		all, _ = emuBestOrder(game.System, all)
		return all[0].Launch
	}
	return ""
}

// emuPickLastIndex is the index in options of the emulator picked last, or -1.
func emuPickLastIndex(opts gameOptions, options []emuEntry) int {
	last := strings.TrimSpace(opts.LastEmulator)
	if last == "" {
		return -1
	}
	last = filepath.Clean(last)
	for i, e := range options {
		if strings.EqualFold(filepath.Clean(e.Launch), last) {
			return i
		}
	}
	return -1
}

// chooseGameLauncher runs launch with the emulator game should start in. When
// several fit, none is pinned and the setting asks, it asks first (the sheet
// below) and launch runs on the answer; B runs nothing. An empty launcher
// means none was found: the caller says so, as before. Main thread.
func chooseGameLauncher(game libraryGame, launch func(launcher string)) {
	if launch == nil {
		return
	}
	if emuPickUI.open {
		// One question at a time: a second start (a voice command, Continue
		// behind it) must not replace the game the sheet is asking about.
		return
	}
	if game.System == androidLibrarySystem || game.System == html5LibrarySystem || game.System == portsLibrarySystem {
		launch(resolveGameLauncher(game.System))
		return
	}
	opts := gameOptionsFor(game.Path)
	if p := gameLauncherPinned(opts); p != "" {
		launch(p)
		return
	}
	if configured := configuredGameLauncher(game.System, loadGameLauncherOverrides(gameLauncherOverridesPath())); configured != "" {
		launch(configured)
		return
	}
	options := pico8NativeLaunchers(game.System, findGameLaunchersFor(game.System, game.Path, emulatorEntries()))
	// The emulator recommended for this hardware leads the list, badged, and
	// takes the highlight unless another was picked last (emubest.go).
	options, recommended := emuBestOrder(game.System, options)
	mode := emuPickAskModeNow()
	switch {
	case len(options) == 0:
		launch("")
		return
	case len(options) == 1:
		emuPickNoteRan(game, opts, options[0].Launch)
		launch(options[0].Launch)
		return
	case mode == emuAskNever:
		// No question: the one picked last, else the recommended one — what
		// resolveGameLauncherFor reports for the game.
		pick := options[0].Launch
		if i := emuPickLastIndex(opts, options); i >= 0 {
			pick = options[i].Launch
		}
		emuPickNoteRan(game, opts, pick)
		launch(pick)
		return
	}
	emuPickUI.open = true
	emuPickUI.game = game
	emuPickUI.options = options
	emuPickUI.recommended = recommended
	emuPickUI.console = emuConsolePinOffered(game.System)
	emuPickUI.remember = mode == emuAskFirst
	emuPickUI.more = emuMoreCoresAvailableFor(game.System, true)
	emuPickUI.details = make([]string, len(options))
	for i, e := range options {
		emuPickUI.details[i] = emuPickDetail(e)
	}
	emuPickUI.cursor = 0
	emuPickUI.scroll = 0
	emuPickUI.visible = 0
	emuPickUI.then = launch
	emuPickUI.rowHits = emuPickUI.rowHits[:0]
	if i := emuPickLastIndex(opts, options); i >= 0 {
		emuPickUI.cursor = i
	}
	logInfo("library", "asking which emulator for %s: %d choices (%s)", filepath.Base(game.Path), len(options), mode)
	PlayActivateSound()
}

// emuPickNoteRan records launcher as the emulator game was started in without
// a question, when the record differs: the save-state list, a resume and the
// sheet's highlight read it back (resolveGameLauncherFor, emuPickLastIndex) as
// the emulator the game's states belong to — so a game that started in its
// console's only emulator, and later has a second, is highlighted on the one
// it ran in.
func emuPickNoteRan(game libraryGame, opts gameOptions, launcher string) {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" || strings.TrimSpace(game.Path) == "" {
		return
	}
	if last := strings.TrimSpace(opts.LastEmulator); last != "" && strings.EqualFold(filepath.Clean(last), filepath.Clean(launcher)) {
		return
	}
	updateGameOptions(game.Path, func(o gameOptions) gameOptions {
		o.LastEmulator = launcher
		return o
	})
}

// chooseGameLauncherResume is chooseGameLauncher for a start that continues
// where the game was left (the game switcher's cards): the emulator the game
// last ran in, without a question, since another core could not load the
// state it left. A game with no record of one asks as usual. Main thread.
func chooseGameLauncherResume(game libraryGame, launch func(launcher string)) {
	if launch == nil || emuPickUI.open {
		return
	}
	opts := gameOptionsFor(game.Path)
	if gameLauncherPinned(opts) == "" && !gameLauncherStillUsable(strings.TrimSpace(opts.LastEmulator)) {
		chooseGameLauncher(game, launch)
		return
	}
	launch(resolveGameLauncherFor(game))
}

// emuPickOpen reports whether the sheet is up (main.go routes input to it).
func emuPickOpen() bool { return emuPickUI.open }

func emuPickClose() {
	emuPickUI.open = false
	emuPickUI.then = nil
	emuPickUI.options = nil
	emuPickUI.details = nil
	emuPickUI.recommended = false
	emuPickUI.more = 0
	emuPickUI.rowHits = emuPickUI.rowHits[:0]
}

func emuPickCancel() {
	if !emuPickUI.open {
		return
	}
	emuPickClose()
	PlayBackSound()
}

// emuPickRows is how many rows the sheet has: the emulators, and the "Get
// more cores" row when there is one.
func emuPickRows() int {
	n := len(emuPickUI.options)
	if emuPickUI.more > 0 {
		n++
	}
	return n
}

// emuPickOnMoreRow reports whether the highlight is on the "Get more cores"
// row.
func emuPickOnMoreRow() bool {
	return emuPickUI.more > 0 && emuPickUI.cursor == len(emuPickUI.options)
}

// emuPickGetMore is A on the last row: the sheet closes without starting the
// game, and the console's other cores download (emumorecores.go).
func emuPickGetMore() {
	system := emuPickUI.game.System
	emuPickClose()
	emuMoreCoresStart(system)
}

// emuPickChoose starts the game with the highlighted emulator; remember pins
// it to the game.
func emuPickChoose(remember bool) {
	if !emuPickUI.open {
		return
	}
	if emuPickOnMoreRow() {
		emuPickGetMore()
		return
	}
	if emuPickUI.cursor < 0 || emuPickUI.cursor >= len(emuPickUI.options) {
		return
	}
	choice := emuPickUI.options[emuPickUI.cursor]
	game, then, firstMode := emuPickUI.game, emuPickUI.then, emuPickUI.remember
	emuPickClose()
	updateGameOptions(game.Path, func(o gameOptions) gameOptions {
		o.LastEmulator = choice.Launch
		if remember {
			o.Emulator = choice.Launch
		}
		return o
	})
	switch {
	case remember && firstMode:
		ShowToast(game.Label()+" starts in "+choice.Name+" from now on · Game options → Emulator to change", ToastKindInfo)
	case remember:
		ShowToast(game.Label()+" always starts in "+choice.Name+" · Game options → Emulator to change", ToastKindInfo)
	}
	if then != nil {
		then(choice.Launch)
	}
}

// emuPickChooseConsole makes the highlighted emulator the one every game of
// the console starts in (game-launchers.json, emuchoices.go), then starts this
// game in it. A file that cannot be written keeps the sheet up.
func emuPickChooseConsole() {
	if !emuPickUI.open || !emuPickUI.console || emuPickUI.cursor < 0 || emuPickUI.cursor >= len(emuPickUI.options) {
		return
	}
	choice := emuPickUI.options[emuPickUI.cursor]
	system := emuPickUI.game.System
	if err := emuConsoleLauncherSet(system, choice.Launch); err != nil {
		logWarn("library", "could not make %s the emulator for every %s game: %v", choice.Launch, system, err)
		ShowToast("Could not save it for every "+system+" game: "+err.Error(), ToastKindWarning)
		PlayBackSound()
		return
	}
	ShowToast("Every "+system+" game starts in "+choice.Name+" · Game options → Emulator, Left, to ask again", ToastKindInfo)
	emuPickChoose(false)
}

// emuPickPlay is A: in First time per game it remembers the pick.
func emuPickPlay() { emuPickChoose(emuPickUI.remember) }

// emuPickAlt is Y: the other answer — once only (First time per game), or
// always (Every launch). Nothing on the "Get more cores" row.
func emuPickAlt() {
	if emuPickOnMoreRow() {
		return
	}
	emuPickChoose(!emuPickUI.remember)
}

func emuPickMove(delta int) {
	n := emuPickRows()
	if n == 0 || delta == 0 {
		return
	}
	next := emuPickUI.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	if next == emuPickUI.cursor {
		return
	}
	emuPickUI.cursor = next
	PlayNavSound()
}

// handleEmuPickKey owns every key while the sheet is up.
func handleEmuPickKey(e *sdl.KeyboardEvent) bool {
	if e == nil || !emuPickUI.open {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		emuPickMove(-1)
	case sdl.K_DOWN:
		emuPickMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		emuPickPlay()
	case sdl.K_y:
		emuPickAlt()
	case sdl.K_x:
		emuPickChooseConsole()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		emuPickCancel()
	}
	return true
}

// handleEmuPickController owns the pad while the sheet is up.
func handleEmuPickController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || !emuPickUI.open {
		return false
	}
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		emuPickMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		emuPickMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		emuPickPlay()
	case sdl.CONTROLLER_BUTTON_Y:
		emuPickAlt()
	case sdl.CONTROLLER_BUTTON_X:
		emuPickChooseConsole()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		emuPickCancel()
	}
	return true
}

// handleEmuPickMouse: a row starts the game, a click off the card cancels.
func handleEmuPickMouse(mx, my int32) bool {
	if !emuPickUI.open {
		return false
	}
	emuPickUI.eatUp = true
	for _, hit := range emuPickUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			emuPickUI.cursor = hit.index
			emuPickPlay()
			return true
		}
	}
	if emuPickUI.panel.W > 0 && pointInRect(mx, my, emuPickUI.panel) {
		return true
	}
	emuPickCancel()
	return true
}

// gameEmulatorAskLabel is the options sheet's Emulator value with no emulator
// set for the game, when starting it would ask (or, set to Never, would not).
func gameEmulatorAskLabel() string {
	switch emuPickAskModeNow() {
	case emuAskAlways:
		return "Ask each time"
	case emuAskNever:
		return "Automatic"
	}
	return "Ask next time"
}

// gameEmulatorLabel is the options sheet's Emulator value: the pinned
// emulator's name, else the question ("Ask next time", "Ask each time") when
// starting it asks (emuPickAsks), else "Automatic".
func gameEmulatorLabel(pinned string, choices []emuEntry) string {
	pinned = strings.TrimSpace(pinned)
	if pinned == "" {
		if emuPickAsks(choices) {
			return gameEmulatorAskLabel()
		}
		return "Automatic"
	}
	for _, e := range choices {
		if strings.EqualFold(filepath.Clean(e.Launch), filepath.Clean(pinned)) {
			return e.Name
		}
	}
	if !gameLauncherStillUsable(pinned) {
		// What starting it does instead (gameLauncherPinned drops it): the
		// question when several fit, else the one there is.
		now := "Automatic"
		if emuPickAsks(choices) {
			now = gameEmulatorAskLabel()
		}
		why := " is gone)"
		if regularFile(pinned) {
			why = " cannot start here)"
		}
		return now + " (" + filepath.Base(pinned) + why
	}
	return filepath.Base(pinned)
}

// cycleGameEmulator steps along "Ask…" and the emulators that fit, without
// wrapping (like the sheet's other ladders). A pinned launcher no longer in
// the list steps back to asking.
func cycleGameEmulator(pinned string, choices []emuEntry, delta int) string {
	ladder := make([]string, 0, len(choices)+1)
	ladder = append(ladder, "")
	for _, e := range choices {
		ladder = append(ladder, e.Launch)
	}
	pinned = strings.TrimSpace(pinned)
	idx := -1
	for i, v := range ladder {
		if v == pinned || (v != "" && pinned != "" && strings.EqualFold(filepath.Clean(v), filepath.Clean(pinned))) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	return ladder[idx]
}

// emuPickEatRelease reports (once) whether a left-button release belongs to
// the sheet: it is up, or the press that closed it was on it.
func emuPickEatRelease() bool {
	eat := emuPickUI.open || emuPickUI.eatUp
	emuPickUI.eatUp = false
	return eat
}

// emuPickDetail is the line under an emulator's name: what kind it is, what
// that means for the game, and where it lives (the last two parts of its
// path). JukaHub's save slots (gamemenu_states.go), Continue
// (savestate_resume.go) and RetroAchievements reach RetroArch only, which is
// the trade-off people weigh against a standalone emulator's speed in 3D games.
func emuPickDetail(e emuEntry) string {
	launch := filepath.Clean(e.Launch)
	base := filepath.Base(launch)
	if e.Extra == emuChoiceCore {
		// "cores/parallel_n64", not the file's full name.
		base = strings.TrimSuffix(strings.TrimSuffix(base, filepath.Ext(base)), "_libretro")
	}
	where := base
	if dir := filepath.Base(filepath.Dir(launch)); dir != "" && dir != "." && dir != string(filepath.Separator) {
		where = dir + string(filepath.Separator) + base
	}
	kind, what := "Standalone", "no JukaHub save slots or Continue"
	switch {
	case e.Extra == emuChoiceCore:
		kind, what = "RetroArch core", "save slots, Continue, achievements"
	case retroArchLauncher(e.Launch) || strings.EqualFold(filepath.Ext(e.Launch), ".dll"):
		kind, what = "RetroArch", "save slots, Continue, achievements"
	}
	if e.Version != "" {
		kind += " " + e.Version
	}
	return kind + " · " + what + " · " + where
}

// emuPickMoreText is the "Get more cores" row's name and line.
func emuPickMoreText() (name, line string) {
	n := emuPickUI.more
	noun := "cores"
	if n == 1 {
		noun = "core"
	}
	return "Get more cores for " + emuPickUI.game.System,
		"Downloads " + strconv.Itoa(n) + " more RetroArch " + noun + " that play it · start the game again once they are in"
}

// renderEmuPick draws the sheet above whatever screen started the game.
func renderEmuPick(renderer *sdl.Renderer, config *Config) {
	if !emuPickUI.open || renderer == nil || config == nil {
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
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = OverlayPadX
	rowH := max32(58, medH+smallH+24)
	header := UIPanelHeader("Which emulator?", emuPickUI.game.Label()+" · "+emuPickUI.game.System, "gamepad")
	rows := emuPickRows()
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := int32(14) + smallH
	panelW := int32(620)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(rows) + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	emuPickUI.panel = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	visible := int((panelH - headerH - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if emuPickUI.cursor < emuPickUI.scroll {
		emuPickUI.scroll = emuPickUI.cursor
	}
	if emuPickUI.cursor >= emuPickUI.scroll+visible {
		emuPickUI.scroll = emuPickUI.cursor - visible + 1
	}
	if emuPickUI.scroll > rows-visible {
		emuPickUI.scroll = rows - visible
	}
	if emuPickUI.scroll < 0 {
		emuPickUI.scroll = 0
	}
	emuPickUI.visible = visible
	emuPickUI.rowHits = emuPickUI.rowHits[:0]
	focusColor := installHighlight()
	for i := 0; i < visible; i++ {
		idx := emuPickUI.scroll + i
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		emuPickUI.rowHits = append(emuPickUI.rowHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == emuPickUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		if idx >= len(emuPickUI.options) {
			// The "Get more cores" row (emumorecores.go).
			name, line := emuPickMoreText()
			renderText(renderer, config, medium, ellipsize(medium, name, rect.W-28), ColorTextPrimary(),
				rect.X+14, rect.Y+8)
			renderText(renderer, config, font, ellipsize(font, line, rect.W-28), ColorTextTertiary(),
				rect.X+14, rect.Y+8+medH+2)
			continue
		}
		e := emuPickUI.options[idx]
		nameRoom := rect.W - 28
		if idx == 0 && emuPickUI.recommended {
			// The emulator recommended for this hardware wears a badge at the
			// row's right edge (emubest.go).
			const badge = "Recommended"
			tw, _ := measureText(font, badge)
			bw, bh := tw+20, smallH+6
			bx := rect.X + rect.W - 14 - bw
			ink := glassAccent()
			fillRoundedRect(renderer, bx, rect.Y+8, bw, bh, bh/2, WithAlpha(ink, 50))
			renderText(renderer, config, font, badge, ink, bx+10, rect.Y+8+3)
			nameRoom -= bw + 10
		}
		renderText(renderer, config, medium, ellipsize(medium, e.Name, nameRoom), ColorTextPrimary(),
			rect.X+14, rect.Y+8)
		detail := ""
		if idx < len(emuPickUI.details) {
			detail = emuPickUI.details[idx]
		}
		renderText(renderer, config, font, ellipsize(font, detail, rect.W-28), ColorTextTertiary(),
			rect.X+14, rect.Y+8+medH+2)
	}

	console := FooterHint{}
	if emuPickUI.console && !emuPickOnMoreRow() {
		console = FooterHint{"X", "All " + emuPickUI.game.System}
	}
	play, alt := FooterHint{"A", "Play"}, FooterHint{"Y", "Always use"}
	if emuPickUI.remember {
		// First time per game: A is the choice the game keeps.
		play, alt = FooterHint{"A", "Play, remember"}, FooterHint{"Y", "Just once"}
	}
	if emuPickOnMoreRow() {
		play, alt = FooterHint{"A", "Download"}, FooterHint{}
	}
	hint := hintLine(FooterHint{"D-Pad", "Emulator"}, play, alt, console, FooterHint{"B", "Cancel"})
	if tw, _ := measureText(font, hint); tw > innerW {
		// A long console name ("PlayStation Portable"): the D-Pad hint goes
		// first, so Cancel stays on the line.
		hint = hintLine(play, alt, console, FooterHint{"B", "Cancel"})
	}
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
