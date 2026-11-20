package main

// shelf_dos_settings.go — one DOS game's own settings: which program starts
// it, how fast a PC it gets, its graphics chip and memory, the mouse speed and
// what Select opens.
//
// DOSBox Pure keeps these as core options, one set for every game unless the
// player saves a game's own .opt in RetroArch's menu — on a handheld nobody
// does. So the shelf keeps them per game in shelves.json and sets them for one
// launch (raScopeApply on the options file raTuneCoreOptionFile picks, put
// back when RetroArch exits), only when a RetroArch launch plays dosbox_pure.
// The values are DOSBox Pure's own spellings (its core_options.h): a value a
// core does not know falls back to its default in RetroArch, so a wrong one
// costs nothing worse than no change.
//
// What Select opens: DOSBox Pure shows its menu or its on-screen keyboard on
// L3, which every DOS layout puts on Select. With no choice of the player's,
// the keyboard comes first (dosbox_pure_on_screen_keyboard "keyboard") — the
// thing a PC game on a pad most often needs — unless their RetroArch already
// has a choice other than the core's default.
//
// The mouse on the stick is a preset of DOSBox Pure's own Controller Mapper,
// kept per game by the core; no core option sets it, so the screen says where
// it is instead of pretending to switch it.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// shelfChoice is one value of a setting and what the screen calls it.
type shelfChoice struct {
	Value string
	Label string
}

var (
	shelfDOSCycles = []shelfChoice{
		{"", "Default"}, {"auto", "Auto"}, {"max", "As fast as possible"},
		{"315", "8086 · 4.77 MHz"}, {"1320", "286 · 6 MHz"}, {"2750", "286 · 12 MHz"},
		{"4720", "386 · 20 MHz"}, {"7800", "386 · 33 MHz"}, {"13400", "486 · 33 MHz"},
		{"26800", "486 · 66 MHz"}, {"77000", "Pentium · 100 MHz"}, {"200000", "Pentium II · 300 MHz"},
	}
	shelfDOSMachines = []shelfChoice{
		{"", "Default"}, {"svga", "SVGA"}, {"vga", "VGA"}, {"ega", "EGA"}, {"cga", "CGA"},
		{"tandy", "Tandy"}, {"hercules", "Hercules"}, {"pcjr", "PCjr"},
	}
	shelfDOSMemory = []shelfChoice{
		{"", "Default"}, {"none", "No extra memory"}, {"4", "4 MB"}, {"8", "8 MB"},
		{"16", "16 MB"}, {"32", "32 MB"}, {"64", "64 MB"},
	}
	shelfDOSMouseSpeeds = []shelfChoice{
		{"", "Default"}, {"0.5", "Slow"}, {"1.0", "Normal"}, {"1.5", "Fast"}, {"2.0", "Faster"}, {"3.0", "Fastest"},
	}
	shelfDOSKeyboards = []shelfChoice{
		{"", "Keyboard first"}, {"true", "Menu first"}, {"onlyosk", "Keyboard only"},
	}
)

// The screen's rows.
const (
	shelfDOSRowStart = iota
	shelfDOSRowCycles
	shelfDOSRowMachine
	shelfDOSRowMemory
	shelfDOSRowMouse
	shelfDOSRowKeyboard
	shelfDOSRowStick
	shelfDOSRowCount
)

// shelfDOSUI is the settings screen over the game page. Main thread only.
var shelfDOSUI struct {
	open   bool
	game   libraryGame
	key    string
	gen    int
	cursor int
	set    shelfDOSSettings
	// startChanged asks for a rescan when the screen closes.
	startChanged bool
	// startWas is the program the game started with when the screen opened
	// (read at the first change, startKnown) and replacedWas the replaced
	// list then: only that program joins the list, not every one the player
	// stepped past on the way (a SETUP.EXE opened from Files later must still
	// start as asked).
	startWas    string
	startKnown  bool
	replacedWas []string
	// progs are the folder game's programs (nil while they are read, or for a
	// file straight in Roms/DOS); guess is the program picked without a choice.
	folder  bool
	loading bool
	progs   []string
	guess   string
	card    sdl.Rect
	hits    []gameLibRowHit
}

func init() {
	gameLaunchRegisterPrepare(shelfDOSPrepare)
	gamePageRegisterExtra(gamePageExtra{
		ID:    "dos-settings",
		Order: 70,
		Icon:  "dos",
		Row:   shelfDOSSettingsRow,
		Open:  shelfDOSSettingsOpen,
	})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "dos-settings",
		IsOpen: func() bool { return shelfDOSUI.open },
		Key: func(e *sdl.KeyboardEvent) bool {
			shelfDOSAction(tbxKeyAction(e))
			return true
		},
		Pad: func(e *sdl.ControllerButtonEvent) bool {
			shelfDOSAction(tbxPadAction(e))
			return true
		},
		Mouse:  shelfDOSMouse,
		Render: shelfDOSRender,
		Close:  shelfDOSClose,
	})
}

// shelfChoiceLabel is value's label in choices ("" unknown: the value itself).
func shelfChoiceLabel(choices []shelfChoice, value string) string {
	for _, c := range choices {
		if c.Value == value {
			return c.Label
		}
	}
	return value
}

// shelfChoiceStep is the value delta steps away from value in choices.
func shelfChoiceStep(choices []shelfChoice, value string, delta int) string {
	i := 0
	for j, c := range choices {
		if c.Value == value {
			i = j
			break
		}
	}
	n := len(choices)
	return choices[((i+delta)%n+n)%n].Value
}

func shelfDOSSettingsRow(game libraryGame) (string, string, bool) {
	if game.System != "DOS" {
		return "", "", false
	}
	set := shelfDOSGet(shelfGameKey(game.Path))
	var parts []string
	if set.Start != "" {
		parts = append(parts, "starts "+filepath.Base(filepath.FromSlash(set.Start)))
	}
	if set.Cycles != "" {
		parts = append(parts, shelfChoiceLabel(shelfDOSCycles, set.Cycles))
	}
	if set.Machine != "" {
		parts = append(parts, shelfChoiceLabel(shelfDOSMachines, set.Machine))
	}
	if set.Memory != "" {
		parts = append(parts, shelfChoiceLabel(shelfDOSMemory, set.Memory))
	}
	detail := "Start program, PC speed, graphics, memory, mouse"
	if len(parts) > 0 {
		detail = strings.Join(parts, " · ")
	}
	return "DOS settings", detail, true
}

// shelfDOSSettingsOpen opens the screen; a folder game's programs are read on
// a goroutine.
func shelfDOSSettingsOpen(game libraryGame) {
	ui := &shelfDOSUI
	ui.gen++
	gen := ui.gen
	ui.open = true
	ui.game = game
	ui.key = shelfGameKey(game.Path)
	ui.set = shelfDOSGet(ui.key)
	ui.cursor = 0
	ui.startChanged = false
	ui.startWas, ui.startKnown = "", false
	ui.replacedWas = append([]string(nil), ui.set.Replaced...)
	ui.progs, ui.guess = nil, ""
	ui.folder = ui.key != filepath.Clean(game.Path)
	ui.loading = ui.folder
	ui.card = sdl.Rect{}
	if !ui.folder {
		return
	}
	gameDir := ui.key
	go func() {
		progs, others := shelfDOSPrograms(gameDir)
		guess := shelfDOSGuess(filepath.Base(gameDir), progs, others)
		rels := make([]string, 0, len(progs))
		for _, p := range progs {
			rels = append(rels, p.Rel)
		}
		tbxPost(func() {
			if !shelfDOSUI.open || shelfDOSUI.gen != gen {
				return
			}
			shelfDOSUI.progs, shelfDOSUI.guess, shelfDOSUI.loading = rels, guess, false
		})
	}()
}

// shelfDOSClose closes the screen; a new start program shows in the list
// after a rescan.
func shelfDOSClose() {
	ui := &shelfDOSUI
	if !ui.open {
		return
	}
	ui.open = false
	if ui.startChanged {
		ui.startChanged = false
		libraryRescanSoon(libraryRescanTries)
	}
}

// shelfDOSSave stores the screen's settings.
func shelfDOSSave() {
	ui := &shelfDOSUI
	shelfDOSSet(ui.key, ui.set)
}

// shelfDOSChange steps the focused row's value.
func shelfDOSChange(delta int) {
	ui := &shelfDOSUI
	switch ui.cursor {
	case shelfDOSRowStart:
		if !ui.folder || ui.loading || len(ui.progs) == 0 {
			return
		}
		choices := []shelfChoice{{"", "Automatic"}}
		for _, p := range ui.progs {
			choices = append(choices, shelfChoice{p, p})
		}
		if !ui.startKnown {
			ui.startWas, ui.startKnown = shelfDOSEffective(ui.set.Start, ui.guess), true
		}
		ui.set.Start = shelfChoiceStep(choices, ui.set.Start, delta)
		ui.set.Replaced = shelfDOSReplace(ui.replacedWas, ui.startWas, shelfDOSEffective(ui.set.Start, ui.guess))
		ui.startChanged = true
		logInfo("library", "%s: start program %q (\"\" is automatic)", ui.game.Title, ui.set.Start)
		shelfDOSSave()
		shelfDOSForget(ui.key)
	case shelfDOSRowCycles:
		ui.set.Cycles = shelfChoiceStep(shelfDOSCycles, ui.set.Cycles, delta)
		shelfDOSSave()
	case shelfDOSRowMachine:
		ui.set.Machine = shelfChoiceStep(shelfDOSMachines, ui.set.Machine, delta)
		shelfDOSSave()
	case shelfDOSRowMemory:
		ui.set.Memory = shelfChoiceStep(shelfDOSMemory, ui.set.Memory, delta)
		shelfDOSSave()
	case shelfDOSRowMouse:
		ui.set.Mouse = shelfChoiceStep(shelfDOSMouseSpeeds, ui.set.Mouse, delta)
		shelfDOSSave()
	case shelfDOSRowKeyboard:
		ui.set.Keyboard = shelfChoiceStep(shelfDOSKeyboards, ui.set.Keyboard, delta)
		shelfDOSSave()
	case shelfDOSRowStick:
		ShowToast("In the game: Select, then Controller Mapper, then the preset Mouse with Left Analog Stick", ToastKindInfo)
		return
	default:
		return
	}
	PlayNavSound()
}

// shelfDOSEffective is the program a folder game starts with: the chosen
// one, else the guess.
func shelfDOSEffective(start, guess string) string {
	if start != "" {
		return start
	}
	return guess
}

// shelfDOSReplace records that the game no longer starts with was (it starts
// with now): now leaves the list, was joins it. The list keeps the last few.
func shelfDOSReplace(list []string, was, now string) []string {
	out := make([]string, 0, len(list)+1)
	for _, r := range list {
		if !strings.EqualFold(r, now) && !strings.EqualFold(r, was) {
			out = append(out, r)
		}
	}
	if was != "" && !strings.EqualFold(was, now) {
		out = append(out, was)
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return out
}

func shelfDOSAction(act tbxAction) {
	ui := &shelfDOSUI
	switch act {
	case tbxActUp:
		if ui.cursor > 0 {
			ui.cursor--
			PlayNavSound()
		}
	case tbxActDown:
		if ui.cursor < shelfDOSRowCount-1 {
			ui.cursor++
			PlayNavSound()
		}
	case tbxActLeft:
		shelfDOSChange(-1)
	case tbxActRight, tbxActConfirm:
		shelfDOSChange(1)
	case tbxActBack:
		shelfDOSClose()
		PlayBackSound()
	}
}

func shelfDOSMouse(mx, my int32) bool {
	ui := &shelfDOSUI
	for _, hit := range ui.hits {
		if pointInRect(mx, my, hit.rect) {
			ui.cursor = hit.index
			shelfDOSChange(1)
			return true
		}
	}
	if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
		shelfDOSClose()
		PlayBackSound()
	}
	return true
}

// shelfDOSRowText is a row's label and value.
func shelfDOSRowText(row int) (string, string) {
	ui := &shelfDOSUI
	switch row {
	case shelfDOSRowStart:
		switch {
		case !ui.folder:
			return "Start program", "This file"
		case ui.loading:
			return "Start program", "Reading the folder…"
		case ui.set.Start != "":
			return "Start program", ui.set.Start
		case ui.guess != "":
			return "Start program", "Automatic (" + ui.guess + ")"
		}
		return "Start program", "Automatic"
	case shelfDOSRowCycles:
		return "PC speed", shelfChoiceLabel(shelfDOSCycles, ui.set.Cycles)
	case shelfDOSRowMachine:
		return "Graphics chip", shelfChoiceLabel(shelfDOSMachines, ui.set.Machine)
	case shelfDOSRowMemory:
		return "Memory", shelfChoiceLabel(shelfDOSMemory, ui.set.Memory)
	case shelfDOSRowMouse:
		return "Mouse speed", shelfChoiceLabel(shelfDOSMouseSpeeds, ui.set.Mouse)
	case shelfDOSRowKeyboard:
		return "Select opens", shelfChoiceLabel(shelfDOSKeyboards, ui.set.Keyboard)
	case shelfDOSRowStick:
		return "Mouse on the stick", "Set in DOSBox Pure's menu — A shows how"
	}
	return "", ""
}

func shelfDOSRender(renderer *sdl.Renderer, config *Config) {
	ui := &shelfDOSUI
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("DOS settings", ui.game.Label(), "dos")
	headerH := UIPanelHeaderBand(config, header, 10)
	rowH := fonts.bodyH + 18
	footerH := fonts.smallH + 20
	cardW := min(int32(680), screenWidth-64)
	cardH := min(headerH+int32(shelfDOSRowCount)*rowH+footerH+8, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	ui.hits = ui.hits[:0]
	for i := 0; i < shelfDOSRowCount; i++ {
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 6}
		if rect.Y+rect.H > card.Y+card.H-footerH {
			break
		}
		UIListRow(renderer, rect, glassRowRadius, i == ui.cursor, false)
		label, value := shelfDOSRowText(i)
		_, th := measureText(fonts.body, label)
		ty := rect.Y + (rect.H-th)/2
		labelW := rect.W * 2 / 5
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, label, labelW-16), ColorTextPrimary(), rect.X+16, ty)
		value = ellipsize(fonts.body, value, rect.W-labelW-16)
		vw, _ := measureText(fonts.body, value)
		renderText(renderer, config, fonts.body, value, ColorTextSecondary(), rect.X+rect.W-16-vw, ty)
		ui.hits = append(ui.hits, gameLibRowHit{index: i, rect: rect})
	}
	hint := hintLine(FooterHint{"Left/Right", "Change"}, FooterHint{"A", "Next"}, FooterHint{"B", "Back"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// ── Launch ──────────────────────────────────────────────────────────────────

// shelfDOSPrepare sets the game's DOS settings for this launch
// (gamelaunch_hooks.go).
func shelfDOSPrepare(launcher string, game libraryGame) {
	if game.System != "DOS" || appConfig == nil || !retroArchLauncher(launcher) {
		return
	}
	if gameCoreFromLauncher(launcher, game.System) != "dosbox_pure" {
		return
	}
	set := shelfDOSGet(shelfGameKey(game.Path))
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	opt := raTuneCoreOptionFile(targets[0], "dosbox_pure", game.Path)
	if opt == "" {
		logInfo("library", "dosbox_pure has no options file yet (start it once from RetroArch); %s's DOS settings skipped for this launch", game.Title)
		return
	}
	raw, err := os.ReadFile(opt)
	if err != nil {
		logWarn("library", "cannot read %s: %v", opt, err)
		return
	}
	var values []raScopeValue
	var what []string
	add := func(key, value, label string) {
		if value != "" {
			values = append(values, raScopeValue{Key: key, Value: value})
			what = append(what, label+"="+value)
		}
	}
	add("dosbox_pure_cycles", set.Cycles, "speed")
	add("dosbox_pure_machine", set.Machine, "graphics")
	add("dosbox_pure_memory_size", set.Memory, "memory")
	add("dosbox_pure_mouse_speed_factor", set.Mouse, "mouse")
	keyboard := set.Keyboard
	if keyboard == "" {
		if v, ok := raConfigGet(string(raw), "dosbox_pure_on_screen_keyboard"); !ok || raValueEqual(v, "true") {
			keyboard = "keyboard"
		}
	}
	add("dosbox_pure_on_screen_keyboard", keyboard, "select")
	if len(values) == 0 {
		return
	}
	if _, err := raScopeApply(opt, values); err != nil {
		logWarn("library", "cannot set %s's DOS settings in %s: %v", game.Title, opt, err)
		return
	}
	logInfo("library", "DOS settings for %s: %s", game.Title, strings.Join(what, ", "))
}
