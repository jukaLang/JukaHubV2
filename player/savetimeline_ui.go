package main

// savetimeline_ui.go — the Save Time Machine's screen (savetimeline.go keeps
// the moments).
//
// One game: its checkpoints (named, kept) above its timeline of moments
// (newest first), each row saying when, what led to it and what it holds.
// A restores the row's moment at once — the saves as they were become a
// "Before restore" moment first, so a wrong press is one more press to undo.
// X opens the row's options (pin as a checkpoint, rename, restore and play,
// delete); Y pins the saves as they are under a name. Opened from the game
// page (More → Time Machine), the library's options sheet and the Save
// Manager, which first lists every game that has moments.
//
// The lists are read on a goroutine whenever the time machine changed
// (saveTimelineGen); restores, pins and deletes run on one too and post their
// answer back with tbxPost, so a big state never holds a frame.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// timeMachineSceneName is the scene the openers switch to.
const timeMachineSceneName = "TimeMachine"

type timeMachineScreenState struct {
	visit tbxVisit
	nav   tbxNav
	// game is the game shown; all says the screen was opened on the list of
	// games (B goes back to it).
	game    libraryGame
	hasGame bool
	all     bool
	// The lists, as last read; gen is saveTimelineGen when they were read.
	moments     []saveMoment
	checkpoints []saveMoment
	games       []saveTimelineGame
	gen         uint64
	loaded      bool
	loading     bool
	// busy is the work under way ("Restoring…"), "" when none.
	busy string
}

var timeMachineScreen timeMachineScreenState

type timeMachineScene struct{ sceneBase }

func (timeMachineScene) render(r *sdl.Renderer, c *Config) bool {
	if timeMachineScreen.visit.begin() {
		timeMachineScreen.nav.scroll = 0
		timeMachineScreen.loaded = false
	}
	timeMachineRefresh()
	timeMachineRender(r, c)
	return true
}

func (timeMachineScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), timeMachineAction)
}

func (timeMachineScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), timeMachineAction)
}

func init() {
	registerScene(timeMachineScene{sceneBase{name: timeMachineSceneName}})
	// The game page's More sheet: Time Machine, beside Saves.
	gamePageRegisterExtra(gamePageExtra{ID: "time-machine", Order: saveLedgerRowOrder - 1, Icon: "history", Row: timeMachineGameRow, Open: func(game libraryGame) {
		timeMachineOpenFor(appConfig, game)
	}})
}

// ── Opening ──────────────────────────────────────────────────────────────

// timeMachineOpenFor opens the screen on one game. Cross-area contract: the
// game page, the options sheet and the phone companion's words point here.
func timeMachineOpenFor(config *Config, game libraryGame) {
	s := &timeMachineScreen
	s.game, s.hasGame, s.all = game, true, false
	s.moments, s.checkpoints, s.loaded = nil, nil, false
	s.nav.focus, s.nav.scroll = "", 0
	tbxOpenScene(config, timeMachineSceneName)
}

// timeMachineOpenAll opens the screen on the list of games (the Save
// Manager's row).
func timeMachineOpenAll(config *Config) {
	s := &timeMachineScreen
	s.game, s.hasGame, s.all = libraryGame{}, false, true
	s.games, s.loaded = nil, false
	s.nav.focus, s.nav.scroll = "", 0
	tbxOpenScene(config, timeMachineSceneName)
}

// timeMachineGameRow is More → Time Machine on the game page: only for a game
// that keeps emulator saves.
func timeMachineGameRow(game libraryGame) (label, detail string, ok bool) {
	if !shoApplies(game) {
		return "", "", false
	}
	if saveTimelineMinutes(appConfig) <= 0 {
		return "Time Machine", "Checkpoints of your saves · moments are off in Settings", true
	}
	return "Time Machine", "Go back to an earlier save · pin a checkpoint", true
}

// ── Reading ──────────────────────────────────────────────────────────────

// timeMachineRefresh starts a read when the lists are stale. Main thread.
func timeMachineRefresh() {
	s := &timeMachineScreen
	gen := saveTimelineGen.Load()
	if s.loading || (s.loaded && s.gen == gen) {
		return
	}
	s.loading = true
	game, hasGame := s.game, s.hasGame
	go func() {
		var moments, checkpoints []saveMoment
		var games []saveTimelineGame
		if hasGame {
			moments, checkpoints = saveTimelineList(game.Path)
		} else {
			games = saveTimelineGames()
		}
		tbxPost(func() {
			s := &timeMachineScreen
			s.loading = false
			if s.hasGame != hasGame || s.game.Path != game.Path {
				return // the screen moved on; the next frame reads again
			}
			s.moments, s.checkpoints, s.games = moments, checkpoints, games
			s.gen, s.loaded = gen, true
		})
	}()
}

// ── Words ────────────────────────────────────────────────────────────────

// saveMomentWhen says when a moment was, as well as the handheld knows:
// "4 min ago", "Today, 15:04" (a believable clock), "This session", or its
// number.
func saveMomentWhen(config *Config, m saveMoment) string {
	if ago := saveLedgerAgoWords(m.M.Stamp); ago != "" {
		return saveLedgerSentence(ago)
	}
	if m.M.Stamp.Date != "" && !m.M.Created.IsZero() {
		return formatDateTimeNear(config, m.M.Created.Local(), time.Now())
	}
	if m.M.Stamp.Boot != "" && m.M.Stamp.Boot == saveLedgerBootID() {
		return "This session"
	}
	return "Moment " + strconv.FormatUint(m.M.Seq, 10)
}

// saveMomentTriggerWords says what led to a moment.
func saveMomentTriggerWords(trigger string) string {
	switch trigger {
	case saveLedgerTrigPlay:
		return "While playing"
	case saveLedgerTrigSleep:
		return "Before sleep"
	case saveLedgerTrigBattery:
		return "When the battery ran low"
	case saveLedgerTrigPowerOff:
		return "Before power off"
	case saveLedgerTrigQuit:
		return "When the game closed"
	case saveLedgerEndCrash:
		return "When the game crashed"
	case saveMomentBeforeRestore:
		return "Before a restore"
	case saveMomentBeforeUpload:
		return "Before a save came from the phone"
	case saveMomentManual:
		return "Pinned by you"
	}
	return "Saved"
}

// saveMomentKindsWords names what a moment holds: "In-game save + auto
// state".
func saveMomentKindsWords(kinds []string) string {
	words := make([]string, 0, len(kinds))
	for _, k := range kinds {
		switch k {
		case saveLedgerKindSave:
			words = append(words, "in-game save")
		case saveLedgerKindAuto:
			words = append(words, "auto state")
		case saveLedgerKindState:
			words = append(words, "save states")
		case saveLedgerKindEmu:
			words = append(words, "emulator save")
		}
	}
	if len(words) == 0 {
		return "Saves"
	}
	return saveLedgerSentence(strings.Join(words, " + "))
}

// saveMomentTitle is a row's title: a checkpoint's name, else when.
func saveMomentTitle(config *Config, m saveMoment) string {
	if m.Checkpoint && strings.TrimSpace(m.M.Name) != "" {
		return m.M.Name
	}
	return saveMomentWhen(config, m)
}

// saveMomentSubtitle is a row's second line.
func saveMomentSubtitle(config *Config, m saveMoment) string {
	parts := make([]string, 0, 4)
	if m.Checkpoint && strings.TrimSpace(m.M.Name) != "" {
		parts = append(parts, saveMomentWhen(config, m))
	} else {
		parts = append(parts, saveMomentTriggerWords(m.M.Trigger))
	}
	parts = append(parts, saveMomentKindsWords(m.M.Kinds), humanBytes(m.M.TotalBytes))
	return strings.Join(parts, " · ")
}

// ── Drawing ──────────────────────────────────────────────────────────────

func timeMachineRender(renderer *sdl.Renderer, config *Config) {
	s := &timeMachineScreen
	status, statusColor := "", ColorTextMuted()
	sub := "Every game's saves, back to any moment"
	if s.hasGame {
		sub = s.game.Label()
	}
	switch {
	case s.busy != "":
		status, statusColor = s.busy, glassAccent()
	case saveTimelineMinutes(config) <= 0:
		status = "Moments off"
	}
	header := tbxHeader("Time Machine", sub, "history", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &s.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
	}
	section := func(title, icon string) {
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
	}
	pill := "Restore"
	if s.busy != "" {
		pill = "Wait"
	}

	switch {
	case !s.loaded && !s.hasGame && len(s.games) == 0:
		drawRow("", tbxRowSpec{icon: "history", title: "Reading the time machine…", dim: true})
	case !s.hasGame:
		section("Games with moments", "games")
		if len(s.games) == 0 {
			drawRow("", tbxRowSpec{icon: "history", title: "Nothing yet",
				subtitle: "Play a game that saves: its moments appear here", dim: true})
		}
		for i, g := range s.games {
			parts := []string{}
			if g.Moments > 0 {
				parts = append(parts, pluralizeCount(g.Moments, "moment", "moments"))
			}
			if g.Checkpoints > 0 {
				parts = append(parts, pluralizeCount(g.Checkpoints, "checkpoint", "checkpoints"))
			}
			parts = append(parts, "newest "+strings.ToLower(saveMomentWhen(config, g.Newest)))
			title := strings.TrimSpace(g.Title)
			if title == "" {
				title = saveLedgerTitle("", g.Path)
			}
			drawRow("g:"+strconv.Itoa(i), tbxRowSpec{icon: "history", title: title, subtitle: strings.Join(parts, " · "),
				note: consoleDisplayName(g.System), pill: "Open", tone: tbxToneAccent})
		}
	default:
		section("This game", "saves")
		drawRow("pin-now", tbxRowSpec{icon: "star", title: "Pin the saves as they are",
			subtitle: "A checkpoint with a name; after a RetroArch game closed, its place in the game too", pill: "Pin", tone: tbxToneNeutral})
		if len(s.checkpoints) > 0 {
			y += 6
			section("Checkpoints", "star")
			for i, m := range s.checkpoints {
				drawRow("c:"+strconv.Itoa(i), tbxRowSpec{icon: "star", title: saveMomentTitle(config, m),
					subtitle: saveMomentSubtitle(config, m), pill: pill, tone: tbxToneAccent})
			}
		}
		y += 6
		section("Timeline", "history")
		switch {
		case !s.loaded:
			drawRow("", tbxRowSpec{icon: "history", title: "Reading…", dim: true})
		case len(s.moments) == 0:
			msg := "While you play, a copy of this game's saves is kept every " +
				strconv.Itoa(saveTimelineMinutes(config)) + " minutes and when it closes"
			if saveTimelineMinutes(config) <= 0 {
				msg = "Moments are off: Settings → Games & emulators → Save Time Machine"
			}
			drawRow("", tbxRowSpec{icon: "history", title: "No moments yet", subtitle: msg, dim: true})
		}
		for i, m := range s.moments {
			spec := tbxRowSpec{icon: "history", title: saveMomentTitle(config, m), subtitle: saveMomentSubtitle(config, m), pill: pill, tone: tbxToneNeutral}
			if i == 0 {
				spec.note = "Newest"
			}
			drawRow("m:"+strconv.Itoa(i), spec)
		}
		y += 6
		section("Good to know", "info")
		drawRow("", tbxRowSpec{icon: "shield", title: "Every restore can be undone",
			subtitle: "The saves as they were become a \"Before restore\" moment first", dim: true})
		drawRow("", tbxRowSpec{icon: "info", title: "Only safe saves",
			subtitle: "A moment is taken after JukaHub read the save back whole from the card", dim: true})
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func timeMachineAction(config *Config, act tbxAction) bool {
	s := &timeMachineScreen
	switch act {
	case tbxActConfirm:
		timeMachineActivate(config, s.nav.focus)
		return true
	case tbxActX:
		if m, ok := timeMachineFocused(); ok {
			timeMachineOptions(config, m)
		}
		return true
	case tbxActY:
		if s.hasGame {
			timeMachinePinNow(config)
		}
		return true
	case tbxActBack:
		if s.hasGame && s.all {
			// Back to the list of games.
			s.game, s.hasGame = libraryGame{}, false
			s.loaded, s.nav.focus, s.nav.scroll = false, "", 0
			PlayBackSound()
			return true
		}
		return false
	}
	return tbxNavAction(&s.nav, act)
}

func timeMachineSceneClick(mx, my int32, config *Config) bool {
	it, ok := timeMachineScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	timeMachineScreen.nav.focus = it.id
	timeMachineActivate(config, it.id)
	return true
}

func timeMachineSceneHints() []FooterHint {
	s := &timeMachineScreen
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch {
	case strings.HasPrefix(s.nav.focus, "g:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	case strings.HasPrefix(s.nav.focus, "m:"), strings.HasPrefix(s.nav.focus, "c:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Restore"}, FooterHint{Button: "X", Label: "Options"})
	case s.nav.focus == "pin-now":
		hints = append(hints, FooterHint{Button: "A", Label: "Pin"})
	}
	if s.hasGame {
		hints = append(hints, FooterHint{Button: "Y", Label: "Pin now"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// timeMachineFocused is the moment or checkpoint under the focus.
func timeMachineFocused() (saveMoment, bool) {
	s := &timeMachineScreen
	id := s.nav.focus
	list := s.moments
	switch {
	case strings.HasPrefix(id, "c:"):
		list = s.checkpoints
	case !strings.HasPrefix(id, "m:"):
		return saveMoment{}, false
	}
	i, err := strconv.Atoi(id[2:])
	if err != nil || i < 0 || i >= len(list) {
		return saveMoment{}, false
	}
	return list[i], true
}

func timeMachineActivate(config *Config, id string) {
	s := &timeMachineScreen
	switch {
	case id == "pin-now":
		timeMachinePinNow(config)
	case strings.HasPrefix(id, "g:"):
		i, err := strconv.Atoi(id[2:])
		if err != nil || i < 0 || i >= len(s.games) {
			return
		}
		g := s.games[i]
		PlayActivateSound()
		s.game = libraryGame{Path: g.Path, System: g.System, Title: g.Title}
		s.hasGame, s.loaded = true, false
		s.moments, s.checkpoints = nil, nil
		s.nav.focus, s.nav.scroll = "", 0
	default:
		if m, ok := timeMachineFocused(); ok {
			timeMachineRestore(config, m, false)
		}
	}
}

// timeMachineBusy refuses a second job while one runs.
func timeMachineBusy() bool {
	if timeMachineScreen.busy != "" {
		ShowToast(timeMachineScreen.busy, ToastKindInfo)
		return true
	}
	return false
}

// timeMachineRestore restores m (and with play, starts the game after).
func timeMachineRestore(config *Config, m saveMoment, play bool) {
	s := &timeMachineScreen
	if timeMachineBusy() {
		return
	}
	if saveTimelineGameRunning() {
		ShowToast("Quit the game first: it holds its saves open", ToastKindWarning)
		return
	}
	game := s.game
	if !s.hasGame || game.Path != m.M.Game {
		return
	}
	when := saveMomentTitle(config, m)
	s.busy = "Restoring…"
	PlayActivateSound()
	go func() {
		before, err := saveTimelineRestore(game, m, saveManagerRoots())
		tbxPost(func() {
			timeMachineScreen.busy = ""
			if err != nil {
				ShowToast("Time Machine: "+firstLine(err.Error()), ToastKindError)
				return
			}
			PlaySuccessSound()
			msg := "Restored to " + when
			if before.Dir != "" {
				msg += " · the saves from before are the newest moment"
			}
			ShowToast(msg, ToastKindSuccess)
			timeMachineScreen.nav.focus, timeMachineScreen.nav.scroll = "", 0
			if play {
				playRecentGame(recentGame{Path: game.Path, System: game.System, Title: game.Title})
			}
		})
	}()
}

// timeMachinePinNow asks for a name, then pins the saves as they are.
func timeMachinePinNow(config *Config) {
	s := &timeMachineScreen
	if !s.hasGame || timeMachineBusy() {
		return
	}
	if saveTimelineGameRunning() {
		ShowToast("Quit the game first: its saves are still being written", ToastKindWarning)
		return
	}
	game := s.game
	PlayActivateSound()
	osPromptOpen("Name this checkpoint", "Before the boss", "Pin", "", saveMomentNameMax, func(name string) {
		if strings.TrimSpace(name) == "" {
			name = "Checkpoint"
		}
		s := &timeMachineScreen
		s.busy = "Pinning…"
		go func() {
			m, err := saveTimelineCapture(game, saveManagerRoots(), saveMomentManual, name, true, false)
			tbxPost(func() {
				timeMachineScreen.busy = ""
				if err != nil {
					ShowToast("Time Machine: "+firstLine(err.Error()), ToastKindError)
					return
				}
				PlaySuccessSound()
				ShowToast("Pinned \""+m.M.Name+"\" ("+humanBytes(m.M.TotalBytes)+")", ToastKindSuccess)
			})
		}()
	})
}

// timeMachineOptions is X on a row: what else can be done with it.
func timeMachineOptions(config *Config, m saveMoment) {
	if timeMachineBusy() {
		return
	}
	PlayActivateSound()
	buttons := []tbxSheetButton{{label: "Restore and play", primary: true, run: func() { timeMachineRestore(config, m, true) }}}
	if m.Checkpoint {
		buttons = append(buttons, tbxSheetButton{label: "Rename", run: func() { timeMachineName(m, "Rename this checkpoint", "Rename") }})
	} else {
		buttons = append(buttons, tbxSheetButton{label: "Pin as checkpoint", run: func() { timeMachineName(m, "Name this checkpoint", "Pin") }})
	}
	buttons = append(buttons,
		tbxSheetButton{label: "Delete", run: func() { timeMachineDelete(m) }},
		tbxSheetButton{label: "Cancel"},
	)
	body := saveMomentSubtitle(config, m) + "\n\n"
	if m.Checkpoint {
		body += "A checkpoint stays until you delete it."
	} else {
		body += fmt.Sprintf("Moments are kept %d to a game, the newest first; pin one to keep it.", saveTimelineKeep)
	}
	body += " Restoring keeps the saves as they are now as a \"Before restore\" moment first."
	tbxOpenSheet(config, tbxSheetSpec{title: saveMomentTitle(config, m), subtitle: timeMachineScreen.game.Label(), icon: "history",
		body: body, buttons: buttons, focus: len(buttons) - 1})
}

// timeMachineName names a moment as a checkpoint, or renames a checkpoint.
func timeMachineName(m saveMoment, title, action string) {
	seed := ""
	if m.Checkpoint {
		seed = m.M.Name
	}
	osPromptOpen(title, "Before the boss", action, seed, saveMomentNameMax, func(name string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		timeMachineScreen.busy = "Pinning…"
		go func() {
			out, err := saveTimelinePin(m, name)
			tbxPost(func() {
				timeMachineScreen.busy = ""
				if err != nil {
					ShowToast("Time Machine: "+firstLine(err.Error()), ToastKindError)
					return
				}
				PlaySuccessSound()
				ShowToast("Checkpoint \""+out.M.Name+"\" kept", ToastKindSuccess)
			})
		}()
	})
}

// timeMachineDelete deletes a moment or checkpoint (its sheet asked).
func timeMachineDelete(m saveMoment) {
	timeMachineScreen.busy = "Deleting…"
	go func() {
		err := saveTimelineDelete(m)
		tbxPost(func() {
			timeMachineScreen.busy = ""
			if err != nil {
				ShowToast("Time Machine: "+firstLine(err.Error()), ToastKindError)
				return
			}
			timeMachineScreen.nav.focus = ""
			ShowToast("Deleted", ToastKindInfo)
		})
	}()
}

// ── Settings ─────────────────────────────────────────────────────────────

// saveTimelineSettingsRow is Settings → Games & emulators → Save Time
// Machine: how often a running game's saves become a moment.
func saveTimelineSettingsRow() setRow {
	stops := []int{0, 5, 10, 15, 30, 60}
	return setRow{
		ID: saveTimelineKey, Label: "Save Time Machine", Kind: setKindChoice, Clamp: true,
		Detail: func(c *Config) string {
			if saveTimelineMinutes(c) <= 0 {
				return "Off: no moments while you play (checkpoints still work)"
			}
			return "A copy of a game's safe saves while you play and when it closes; More → Time Machine goes back"
		},
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(stops))
			for _, n := range stops {
				label := "Off"
				if n > 0 {
					label = "Every " + strconv.Itoa(n) + " min"
				}
				out = append(out, setOption{Value: strconv.Itoa(n), Label: label})
			}
			return out
		},
		Current: func(c *Config) int {
			cur := saveTimelineMinutes(c)
			for i, n := range stops {
				if n == cur {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return "Every " + strconv.Itoa(saveTimelineMinutes(c)) + " min" },
		Choose: func(c *Config, value string) {
			setStoreString(c, saveTimelineKey, value)
			saveTimelinePublish(c)
			if n, _ := strconv.Atoi(value); n > 0 {
				setToast("Save Time Machine: every " + value + " min")
			} else {
				setToast("Save Time Machine: moments off")
			}
		},
	}
}
