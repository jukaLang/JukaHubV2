package main

// devcheck_ui.go — the Device check page (devcheck.go): one check at a time,
// what to do, what JukaHub measured, and the answer; all nine checks below
// it; the report at the end.
//
// Reached from Settings → About → Device check and Tools → System. It is a
// tool screen (tbxui.go): Home's glass, the shared header and footer, D-pad
// focus that a click and the wheel follow too, B back through the scene
// history. L1/R1 turn to the previous or next check, X writes a note for the
// check on screen and Y saves the report.
//
// The page answers from memory: the probes run on goroutines
// (devcheck_probe.go) and post back. The buttons check opens the Controller
// test over the page and reads what it saw once it closes; the game check
// leaves for the game and reads its run, and its Verified record, when
// JukaHub is back.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// devCheckButton is one button of the step card.
type devCheckButton struct {
	id       string
	label    string
	primary  bool
	disabled bool
}

// devCheckModel is what the page draws, rebuilt when the store changes and
// a few times a second for the live line.
type devCheckModel struct {
	builtAt  time.Time
	gen      uint64
	header   UIPageHeader
	icon     string
	headline string
	pill     string
	pillTone tbxTone
	todo     string
	live     string
	evidence string
	note     string
	actions  []devCheckButton
	verdict  []devCheckButton
	rows     [len(devCheckSteps)]tbxRowSpec
	saveSub  string
	newSub   string
	histSub  string
}

// devCheckScreenState is the page's state. Main thread only.
type devCheckScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model devCheckModel
	// step is the check on screen; -1 is the overview.
	step     int
	wrapTodo tbxWrapCache
	wrapEv   tbxWrapCache
	// busy names the probes in flight, by step id.
	busy map[string]bool
	// ctrlOpen is set while the Controller test this page opened is up.
	ctrlOpen bool
	// The headphones check's jack readings: the last one, whether there was
	// one, whether it read unplugged during this visit, and the pacing.
	jack, jackKnown, jackOut, jackBusy bool
	jackAt                             time.Time
	// The game check's offer: 0 not looked for, 1 looking, 2 found, 3 none.
	game      libraryGame
	gameWhy   string
	gameState int
	gameBase  int64
	gameGen   int
	gameVF    int64
	gameBack  bool
}

var devCheckScreen = devCheckScreenState{step: -1}

// devCheckOpenScreen opens the page. Cross-area contract: Settings → About
// and the Tools hub call it.
func devCheckOpenScreen(config *Config) {
	devCheckEnsureLoaded()
	if !tbxOpenScene(config, devCheckSceneName) {
		ShowToast("Device check is not part of this build", ToastKindInfo)
	}
}

type devCheckScene struct{ sceneBase }

func (devCheckScene) render(r *sdl.Renderer, c *Config) bool {
	if devCheckScreen.visit.begin() {
		devCheckEnsureLoaded()
		if devCheck.loaded {
			devCheckEnterStep(devCheckScreen.step)
		}
	}
	devCheckRenderScreen(r, c)
	return true
}

func (devCheckScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), devCheckSceneAction)
}

func (devCheckScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), devCheckSceneAction)
}

func init() { registerScene(devCheckScene{sceneBase{name: devCheckSceneName}}) }

// ── Entry points ──────────────────────────────────────────────────────────

// devCheckSettingsRow is Settings → About → Device check (setmodel.go).
func devCheckSettingsRow() setRow {
	return setRow{
		ID: "devcheck", Label: "Device check", Kind: setKindAction,
		Detail: devCheckSettingsDetail,
		Open:   devCheckOpenScreen,
	}
}

// devCheckSettingsDetail is the line under Settings → About → Device check.
func devCheckSettingsDetail(*Config) string {
	if run := devCheckRunNow(); run != nil && !run.Finished {
		return fmt.Sprintf("Unfinished: check %d of %d, picks up where it stopped", run.Step+1, len(devCheckSteps))
	}
	return "Walk through the hands-on checks and save a report"
}

// devCheckHubTile is the Tools hub's System tile.
func devCheckHubTile() toolsHubTile {
	t := toolsHubTile{id: "devcheck", label: "Device check", icon: "checklist", value: "Hands-on checks with a report"}
	if run := devCheckRunNow(); run != nil && !run.Finished {
		t.value = fmt.Sprintf("Check %d of %d · unfinished", run.Step+1, len(devCheckSteps))
		t.tone = toolsHubToneWarn
	}
	return t
}

// ── Steps ─────────────────────────────────────────────────────────────────

// devCheckScreenAfterLoad puts the loaded run on screen: an unfinished one at
// its step, a finished one on the overview.
func devCheckScreenAfterLoad() {
	run := devCheckRunNow()
	if run == nil {
		return
	}
	step := run.Step
	if run.Finished {
		step = -1
	}
	devCheckEnterStep(step)
}

// devCheckEnterStep shows step i (-1 the overview).
func devCheckEnterStep(i int) {
	ui := &devCheckScreen
	if i < -1 || i >= len(devCheckSteps) {
		i = -1
	}
	ui.step = i
	ui.jackKnown, ui.jackOut = false, false
	ui.nav.scroll = 0
	ui.nav.focus = devCheckDefaultFocus()
	ui.model.builtAt = time.Time{}
	if i < 0 {
		return
	}
	devCheckSetStep(i)
	if devCheckSteps[i].id == dcStepGame {
		// Read before the launch (devCheckLaunchGame); it never waits.
		vfEnsureLoaded()
		if ui.gameState == 0 {
			devCheckStartPickGame()
		}
	}
}

// devCheckDefaultFocus is where the focus lands on the step on screen: its
// first action, or its first answer.
func devCheckDefaultFocus() string {
	ui := &devCheckScreen
	run := devCheckRunNow()
	if run == nil {
		return ""
	}
	if ui.step < 0 {
		if run.Finished {
			return "act:save"
		}
		return "act:continue"
	}
	id := devCheckSteps[ui.step].id
	if run.Steps[id].State == dcNotHere {
		return "next"
	}
	// The last action is "Add a note": the step's own come first.
	acts := devCheckActions(run, ui.step)
	for _, a := range acts[:len(acts)-1] {
		if !a.disabled {
			return a.id
		}
	}
	return "pass"
}

// devCheckStepBy turns to the previous or next check; the overview sits
// before the first and after the last.
func devCheckStepBy(d int) {
	n := len(devCheckSteps) + 1
	pos := (devCheckScreen.step + 1 + d + n) % n
	devCheckEnterStep(pos - 1)
	PlayNavSound()
}

// devCheckBusy reports whether the probe of step id is running.
func devCheckBusy(id string) bool { return devCheckScreen.busy[id] }

func devCheckSetBusy(id string, on bool) {
	ui := &devCheckScreen
	if ui.busy == nil {
		ui.busy = map[string]bool{}
	}
	ui.busy[id] = on
	ui.model.builtAt = time.Time{}
}

// devCheckActions are the step's own buttons, then "Add a note".
func devCheckActions(run *devCheckRun, i int) []devCheckButton {
	ui := &devCheckScreen
	if i < 0 {
		var out []devCheckButton
		if !run.Finished {
			out = append(out, devCheckButton{id: "act:continue", label: "Continue", primary: true})
		}
		out = append(out,
			devCheckButton{id: "act:save", label: "Save report", primary: run.Finished},
			devCheckButton{id: "act:new", label: "Start a new check"})
		return out
	}
	id := devCheckSteps[i].id
	var out []devCheckButton
	if run.Steps[id].State != dcNotHere {
		busy := devCheckBusy(id)
		switch id {
		case dcStepButtons:
			out = append(out, devCheckButton{id: "act:controller", label: "Open the controller test", primary: true})
		case dcStepSpeaker, dcStepHeadphones, dcStepUSBDAC:
			label := "Play the test sound"
			if busy {
				label = "Playing…"
			}
			out = append(out, devCheckButton{id: "act:tone", label: label, primary: true, disabled: busy})
		case dcStepBluetooth:
			label := "Search for devices"
			if busy {
				label = "Searching…"
			}
			out = append(out, devCheckButton{id: "act:btscan", label: label, primary: true, disabled: busy},
				devCheckButton{id: "act:btpage", label: "Open Bluetooth"})
		case dcStepWiFi:
			label := "Check Wi-Fi"
			if busy {
				label = "Checking…"
			}
			out = append(out, devCheckButton{id: "act:wifi", label: label, primary: true, disabled: busy},
				devCheckButton{id: "act:wifipage", label: "Open Wi-Fi"})
		case dcStepGame:
			label, off := "Looking for a game…", true
			switch ui.gameState {
			case 2:
				label, off = "Play "+devCheckClip(ui.game.Label(), 28), false
			case 3:
				label = "No game to play"
			}
			out = append(out, devCheckButton{id: "act:game", label: label, primary: true, disabled: off})
		}
	}
	return append(out, devCheckButton{id: "note", label: "Add a note"})
}

// devCheckVerdict is the step's answer row.
func devCheckVerdict(run *devCheckRun, i int) []devCheckButton {
	if i < 0 {
		return nil
	}
	s := devCheckSteps[i]
	if run.Steps[s.id].State == dcNotHere {
		return []devCheckButton{{id: "next", label: "Next check", primary: true}}
	}
	return []devCheckButton{
		{id: "pass", label: s.pass},
		{id: "fail", label: s.fail},
		{id: "skip", label: "Skip"},
	}
}

// ── Model ─────────────────────────────────────────────────────────────────

// devCheckPill is a result's pill.
func devCheckPill(res devCheckResult) (string, tbxTone) {
	switch res.State {
	case dcPass:
		return "Passed", tbxToneGood
	case dcFail:
		return "Failed", tbxToneBad
	case dcSkip:
		return "Skipped", tbxToneNeutral
	case dcNotHere:
		return "Not on this device", tbxToneNeutral
	}
	return "To do", tbxToneAccent
}

// devCheckLive is the step's live line: what the page is waiting for or
// doing right now.
func devCheckLive(run *devCheckRun, i int) string {
	ui := &devCheckScreen
	id := devCheckSteps[i].id
	if run.Steps[id].State == dcNotHere {
		return ""
	}
	switch id {
	case dcStepButtons:
		if hintVocabNow() == hintVocabKeyboard {
			return "Esc closes the controller test."
		}
		return "Hold Select to close the controller test."
	case dcStepHeadphones:
		switch {
		case !ui.jackKnown:
			return "Reading the jack…"
		case ui.jack:
			return "Headphones: plugged in"
		}
		return "Headphones: not plugged in"
	case dcStepSleep:
		if devCheckSleepMeasuring {
			return "Measuring the wake: Wi-Fi and sound…"
		}
		return "Waiting for the power key…"
	case dcStepBluetooth:
		if devCheckBusy(id) {
			return "Searching for about " + devCheckDur(bzScanDuration) + "…"
		}
	case dcStepWiFi:
		if devCheckBusy(id) {
			return "Checking the link and the internet…"
		}
	case dcStepUSBDAC:
		if card := usbDACCurrent(); card.id != "" {
			return "USB DAC: " + devCheckClip(card.name, 60)
		}
		return "No USB DAC found yet"
	case dcStepGame:
		st := devCheckGameNow()
		switch {
		case ui.gameState == 1:
			return "Looking for a game…"
		case ui.gameState == 3:
			return "No game found in your library. Add one, or skip this check."
		case ui.gameState == 2 && st.path == ui.game.Path && st.runID == run.ID && st.exited:
			return "Back from " + devCheckClip(ui.game.Label(), 40) + ". Did it come back to JukaHub cleanly?"
		case ui.gameState == 2:
			return devCheckClip(ui.game.Label(), 48) + " (" + devCheckClip(ui.game.System, 24) + "), " + ui.gameWhy
		}
	}
	return ""
}

// devCheckBuildModel builds what the page draws for run.
func devCheckBuildModel(run *devCheckRun, now time.Time) devCheckModel {
	ui := &devCheckScreen
	m := devCheckModel{builtAt: now, gen: devCheck.saveGen}
	sub := devCheckModelName(run.Model)
	if fw := devCheckFirmwareLabel(run); fw != "" {
		sub += " · " + fw
	}
	status, color := "Overview", ColorTextMuted()
	switch {
	case ui.step >= 0:
		status, color = fmt.Sprintf("Check %d of %d", ui.step+1, len(devCheckSteps)), glassAccent()
	case run.Finished:
		status, color = "Finished", ToastSuccessInk()
	}
	m.header = tbxHeader("Device check", sub, "checklist", status, color)

	if ui.step < 0 {
		m.icon = "checklist"
		m.headline = "Device check"
		if run.Finished {
			m.headline = "All checks done"
		}
		m.todo = devCheckSummary(run) + ". The report is a section for the hardware log: save it, then copy it from the SD card's \"" +
			devCheckReportDir + "\" folder."
		m.pill, m.pillTone = "", tbxToneNeutral
	} else {
		s := devCheckSteps[ui.step]
		res := run.Steps[s.id]
		m.icon = s.icon
		m.headline = strconv.Itoa(ui.step+1) + ". " + s.title
		m.pill, m.pillTone = devCheckPill(res)
		if res.Auto && (res.State == dcPass || res.State == dcFail) {
			m.pill += " · measured"
		}
		m.todo = s.todo
		if res.State == dcNotHere {
			m.todo = "This check cannot run here."
		}
		m.live = devCheckLive(run, ui.step)
		if len(res.Evidence) > 0 {
			m.evidence = "• " + strings.Join(res.Evidence, "\n• ")
		}
		if res.Note != "" {
			m.note = "Your note: " + res.Note
		}
	}
	m.actions = devCheckActions(run, ui.step)
	m.verdict = devCheckVerdict(run, ui.step)

	for i, s := range devCheckSteps {
		res := run.Steps[s.id]
		pill, tone := devCheckPill(res)
		spec := tbxRowSpec{icon: s.icon, title: strconv.Itoa(i+1) + ". " + s.title, pill: pill, tone: tone, dim: res.State == dcNotHere}
		switch {
		case len(res.Evidence) > 0:
			spec.subtitle = res.Evidence[0]
		case res.Note != "":
			spec.subtitle = res.Note
		default:
			spec.subtitle = "Not checked yet"
		}
		if res.Auto && (res.State == dcPass || res.State == dcFail) {
			spec.note = "measured"
		}
		m.rows[i] = spec
	}
	m.saveSub = "Into \"" + devCheckReportDir + "\" on the SD card, ready for the hardware log"
	if !devCheck.facts.trimui {
		m.saveSub = "Into Documents, JukaHub, " + devCheckReportDir
	}
	if run.Report != "" {
		m.saveSub = "Saved as " + run.Report + "; saving again updates it"
	}
	m.newSub = "This one stays among your last " + strconv.Itoa(devCheckMaxRuns)
	m.histSub = pluralizeCount(len(devCheck.file.Runs), "check", "checks") + " kept on this device"
	return m
}

// ── Live work ─────────────────────────────────────────────────────────────

// devCheckTick does the step's per-frame work: reading the controller test
// when it closes, pacing the jack reads, marking the sleep step as up, and
// following the game.
func devCheckTick(run *devCheckRun) {
	ui := &devCheckScreen
	if ui.step < 0 {
		return
	}
	id := devCheckSteps[ui.step].id
	if run.Steps[id].State == dcNotHere {
		return
	}
	switch id {
	case dcStepButtons:
		if ui.ctrlOpen && !ctrldiag.open {
			ui.ctrlOpen = false
			devCheckButtonsDone(run)
		}
	case dcStepHeadphones:
		if !ui.jackBusy && time.Since(ui.jackAt) >= time.Second {
			ui.jackBusy, ui.jackAt = true, time.Now()
			go func() {
				in := devCheckJack()
				tbxPost(func() {
					ui.jackBusy = false
					if !in {
						ui.jackOut = true
					}
					if in && ui.jackKnown && !ui.jack && ui.step >= 0 && devCheckSteps[ui.step].id == dcStepHeadphones {
						ShowToast("Headphones plugged in · play the test sound", ToastKindInfo)
					}
					ui.jack, ui.jackKnown = in, true
					ui.model.builtAt = time.Time{}
				})
			}()
		}
	case dcStepSleep:
		devCheckSleepArm(run.ID)
	case dcStepGame:
		devCheckGameTick(run)
	}
}

// devCheckButtonsDone reads the controller test that just closed.
func devCheckButtonsDone(run *devCheckRun) {
	lines, complete, touched := devCheckButtonsResult(devCheck.facts.trimui, deviceHardwareCurrent().HasSticks)
	if !touched {
		ShowToast("Nothing was pressed in the controller test", ToastKindInfo)
		return
	}
	auto := ""
	if complete {
		auto = dcPass
	}
	devCheckSetEvidence(run.ID, dcStepButtons, lines, auto)
	devCheckScreen.nav.focus = "pass"
	if complete {
		ShowToast("Every button and both sticks were seen", ToastKindSuccess)
	} else {
		ShowToast("Not everything was seen: the list says what", ToastKindWarning)
	}
}

// devCheckJackLines are the headphones check's jack lines, from this visit's
// readings.
func devCheckJackLines() []string {
	ui := &devCheckScreen
	switch {
	case !ui.jackKnown:
		return nil
	case ui.jack && ui.jackOut:
		return []string{"Jack: seen unplugged, then plugged in"}
	case ui.jack:
		return []string{"Jack: plugged in"}
	}
	return []string{"Jack: not plugged in"}
}

// devCheckStartTone plays the test sound for step id.
func devCheckStartTone(id string) {
	run := devCheckRunNow()
	if run == nil || devCheckBusy(id) {
		return
	}
	vol := -1
	if volumeControlAvailable() {
		if pct, ok := volumeCurrent(); ok {
			vol = pct
		}
	}
	var pre []string
	if id == dcStepHeadphones {
		pre = devCheckJackLines()
	}
	devCheckSetBusy(id, true)
	runID := run.ID
	go devCheckSoundProbe(runID, id, vol, pre, func() { devCheckSetBusy(id, false) })
}

// devCheckStartTask runs a scene task for step id: leaving the page stops it.
func devCheckStartTask(id string, fn func(ctx context.Context, runID int64, done func())) {
	run := devCheckRunNow()
	if run == nil || devCheckBusy(id) {
		return
	}
	devCheckSetBusy(id, true)
	runID := run.ID
	done := func() { devCheckSetBusy(id, false) }
	if !runSceneTask(func(ctx context.Context) { fn(ctx, runID, done) }) {
		done()
	}
}

// devCheckStartPickGame looks for the game the game check offers.
func devCheckStartPickGame() {
	ui := &devCheckScreen
	ui.gameState = 1
	go func() {
		game, why, ok := devCheckPickGame()
		tbxPost(func() {
			ui.gameState = 3
			if ok {
				ui.game, ui.gameWhy, ui.gameState = game, why, 2
			}
			ui.model.builtAt = time.Time{}
		})
	}()
}

// devCheckLaunchGame starts the offered game the library's way and follows
// it. Its Verified record's results that come after this point are the
// check's evidence.
func devCheckLaunchGame() {
	ui := &devCheckScreen
	run := devCheckRunNow()
	if run == nil || ui.gameState != 2 {
		return
	}
	if !vfLoaded() {
		// The record before the launch is the line between old results and
		// this run's: without it every earlier result would look new.
		vfEnsureLoaded()
		ShowToast("One moment: reading what was tested here", ToastKindInfo)
		return
	}
	game := ui.game
	kind, id, _ := vfGameRef(game)
	ui.gameBase = 0
	if rec, ok := vfLookup(kind, id); ok {
		ui.gameBase = rec.Seq
		for _, res := range rec.Checks {
			ui.gameBase = max(ui.gameBase, res.Seq)
		}
	}
	ui.gameBack = false
	devCheckGameWatch(game.Path, run.ID)
	launcher := strings.TrimSpace(resolveGameLauncherFor(game))
	if err := launchLibraryGame(game, launcher); err != nil {
		devCheckGameWatch("", 0)
		ShowToast("Cannot launch "+game.Label()+": "+firstLine(err.Error()), ToastKindError)
		devCheckSetEvidence(run.ID, dcStepGame, []string{
			"Game: " + devCheckClip(game.Label(), 60) + " (" + devCheckClip(game.System, 30) + ")",
			"Could not start: " + devCheckClip(firstLine(err.Error()), 100),
		}, "")
		return
	}
	if gameLaunchWasHeld(game.Path) {
		ShowToast(game.Label()+" is not ready yet: its card says why", ToastKindWarning)
		return
	}
	ShowToast("Playing "+game.Label()+" · quit it the usual way to come back", ToastKindInfo)
}

// devCheckGameTick follows the game the check started: its start, its exit
// and the Verified results that arrive after it.
func devCheckGameTick(run *devCheckRun) {
	ui := &devCheckScreen
	if ui.gameState != 2 {
		return
	}
	st := devCheckGameNow()
	if st.path == "" || st.path != ui.game.Path || st.runID != run.ID {
		return
	}
	vg := vfGeneration.Load()
	if st.gen == ui.gameGen && (!st.exited || vg == ui.gameVF) {
		return
	}
	ui.gameGen, ui.gameVF = st.gen, vg
	devCheckSetEvidence(run.ID, dcStepGame, devCheckGameLines(ui.game, st, ui.gameBase), "")
	if st.exited && !ui.gameBack {
		ui.gameBack = true
		ui.nav.focus = "pass"
		ShowToast("Back from "+ui.game.Label()+" · did it come back cleanly?", ToastKindInfo)
	}
}

// devCheckNoteGameReturn adds the player's answer to the game's Verified
// record as its "quits back" check, when this run's check started the game
// and saw it come back.
func devCheckNoteGameReturn(state string) {
	ui := &devCheckScreen
	st := devCheckGameNow()
	run := devCheckRunNow()
	if run == nil || ui.gameState != 2 || !st.exited || st.path != ui.game.Path || st.runID != run.ID {
		return
	}
	kind, id, title := vfGameRef(ui.game)
	go vfNote(kind, id, title, vfCheckReturn, state, vfByUser, "guided device check", "")
}

// ── Input ─────────────────────────────────────────────────────────────────

func devCheckSceneAction(config *Config, act tbxAction) bool {
	if devCheckRunNow() == nil {
		// Still reading the store: only the way out and the top bar.
		return act != tbxActBack && act != tbxActUp
	}
	nav := &devCheckScreen.nav
	switch act {
	case tbxActConfirm:
		devCheckActivate(config, nav.focus)
		return true
	case tbxActX:
		devCheckEditNote()
		return true
	case tbxActY:
		PlayActivateSound()
		devCheckSaveReport()
		return true
	case tbxActPageUp:
		devCheckStepBy(-1)
		return true
	case tbxActPageDown:
		devCheckStepBy(1)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func devCheckSceneClick(mx, my int32, config *Config) bool {
	it, ok := devCheckScreen.nav.hit(mx, my)
	if !ok || it.disabled {
		return ok
	}
	devCheckScreen.nav.focus = it.id
	devCheckActivate(config, it.id)
	return true
}

func devCheckSceneHints() []FooterHint {
	label := "Select"
	switch f := devCheckScreen.nav.focus; {
	case strings.HasPrefix(f, "step:"):
		label = "Open"
	case f == "pass", f == "fail", f == "skip":
		label = "Answer"
	case f == "save", f == "act:save":
		label = "Save"
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}}
	if devCheckScreen.step >= 0 {
		hints = append(hints, FooterHint{Button: "X", Label: "Note"})
	}
	return append(hints,
		FooterHint{Button: "Y", Label: "Save report"},
		FooterHint{Button: "L1/R1", Label: "Page"},
		FooterHint{Button: "B", Label: "Back"})
}

// devCheckActivate runs the control id.
func devCheckActivate(config *Config, id string) {
	ui := &devCheckScreen
	run := devCheckRunNow()
	if run == nil {
		return
	}
	for _, b := range append(append([]devCheckButton(nil), ui.model.actions...), ui.model.verdict...) {
		if b.id == id && b.disabled {
			return
		}
	}
	switch {
	case id == "act:controller":
		ui.ctrlOpen = true
		openCtrldiag()
	case id == "act:tone":
		PlayActivateSound()
		if ui.step >= 0 {
			devCheckStartTone(devCheckSteps[ui.step].id)
		}
	case id == "act:btscan":
		PlayActivateSound()
		devCheckStartTask(dcStepBluetooth, devCheckBluetoothProbe)
	case id == "act:btpage":
		PlayActivateSound()
		btScreenOpen(config)
	case id == "act:wifi":
		PlayActivateSound()
		devCheckStartTask(dcStepWiFi, devCheckWiFiProbe)
	case id == "act:wifipage":
		PlayActivateSound()
		wifiScrOpen(config)
	case id == "act:game":
		PlayActivateSound()
		devCheckLaunchGame()
	case id == "act:continue":
		PlayActivateSound()
		devCheckEnterStep(run.Step)
	case id == "act:save", id == "save":
		PlayActivateSound()
		devCheckSaveReport()
	case id == "act:new", id == "new":
		PlayActivateSound()
		devCheckAskNewRun(config)
	case id == "history":
		PlayActivateSound()
		devCheckOpenHistory(config)
	case id == "note":
		devCheckEditNote()
	case id == "pass":
		devCheckAnswerStep(dcPass)
	case id == "fail":
		devCheckAnswerStep(dcFail)
	case id == "skip":
		devCheckAnswerStep(dcSkip)
	case id == "next":
		PlayNavSound()
		next := devCheckNextOpen(run, ui.step)
		devCheckEnterStep(next)
	case strings.HasPrefix(id, "step:"):
		if n, err := strconv.Atoi(strings.TrimPrefix(id, "step:")); err == nil {
			PlayActivateSound()
			devCheckEnterStep(n)
		}
	}
	ui.model.builtAt = time.Time{}
}

// devCheckAnswerStep records the answer for the check on screen and turns to
// the next one without an answer, or to the overview when none is left.
func devCheckAnswerStep(state string) {
	ui := &devCheckScreen
	i := ui.step
	if i < 0 {
		return
	}
	if devCheckSteps[i].id == dcStepGame && (state == dcPass || state == dcFail) {
		devCheckNoteGameReturn(state)
	}
	if state == dcPass {
		PlaySuccessSound()
	} else {
		PlayActivateSound()
	}
	if devCheckAnswer(i, state) {
		devCheckEnterStep(-1)
		ShowToast(hintSentence("All checks done · %s saves the report", "Y", "Save report"), ToastKindSuccess)
		return
	}
	if run := devCheckRunNow(); run != nil {
		devCheckEnterStep(run.Step)
	}
}

// devCheckEditNote opens the keyboard for the note of the check on screen.
func devCheckEditNote() {
	ui := &devCheckScreen
	run := devCheckRunNow()
	if run == nil || ui.step < 0 {
		return
	}
	i, runID := ui.step, run.ID
	s := devCheckSteps[i]
	PlayActivateSound()
	osPromptOpen("Note: "+s.title, "What did you notice?", "Save", run.Steps[s.id].Note, devCheckNoteMax, func(text string) {
		if cur := devCheckRunNow(); cur == nil || cur.ID != runID {
			return
		}
		devCheckSetNote(i, text)
		devCheckScreen.model.builtAt = time.Time{}
		if text == "" {
			ShowToast("Note removed", ToastKindInfo)
		} else {
			ShowToast("Note saved", ToastKindSuccess)
		}
	})
}

// devCheckAskNewRun starts a new run, asking first when this one is not
// finished.
func devCheckAskNewRun(config *Config) {
	start := func() {
		devCheckNewRun()
		devCheckScreen.gameState = 0
		devCheckScreenAfterLoad()
		ShowToast("New device check started", ToastKindInfo)
	}
	run := devCheckRunNow()
	if run == nil || run.Finished {
		start()
		return
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Start a new check?",
		icon:  "refresh",
		body:  "This one is not finished. It stays among your last " + strconv.Itoa(devCheckMaxRuns) + " checks with its answers, and a new one starts at the first check.",
		buttons: []tbxSheetButton{
			{label: "Keep going"},
			{label: "Start a new check", primary: true, run: start},
		},
	})
}

// devCheckOpenHistory lists the runs this device keeps, newest first.
func devCheckOpenHistory(config *Config) {
	var b strings.Builder
	runs := devCheck.file.Runs
	for i := len(runs) - 1; i >= 0; i-- {
		r := &runs[i]
		fmt.Fprintf(&b, "## Check %d · %s\n", r.ID, setOr(r.Date, "undated"))
		line := devCheckModelName(r.Model) + " · " + vfVersionLabel(setOr(r.Release, "unknown release"))
		if i == devCheck.cur {
			line += " · on screen"
		}
		b.WriteString(line + "\n")
		b.WriteString(devCheckSummary(r) + "\n")
		if r.Report != "" {
			b.WriteString("Report: " + r.Report + "\n")
		}
		b.WriteString("\n")
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:   "Earlier checks",
		icon:    "history",
		body:    strings.TrimSpace(b.String()),
		buttons: []tbxSheetButton{{label: "Close", primary: true}},
	})
}

// ── Drawing ───────────────────────────────────────────────────────────────

func devCheckRenderScreen(renderer *sdl.Renderer, config *Config) {
	ui := &devCheckScreen
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	nav := &ui.nav
	run := devCheckRunNow()
	if run == nil {
		frame := tbxBeginFrame(renderer, config, tbxHeader("Device check", "Hands-on checks of this device", "checklist", "", ColorTextMuted()))
		body := frame.bodyRect()
		nav.begin(body.Y, body.H)
		nav.end()
		tbxDrawEmpty(renderer, config, body, "checklist", "Getting ready…", "")
		tbxEndFrame(renderer, config)
		return
	}
	devCheckTick(run)
	now := time.Now()
	if ui.model.gen != devCheck.saveGen || now.Sub(ui.model.builtAt) > 250*time.Millisecond {
		ui.model = devCheckBuildModel(run, now)
	}
	m := &ui.model

	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0

	// The step card: what to do, what is happening, what was measured.
	badge := int32(56)
	textX := x + 22 + badge + 18
	textW := x + w - 22 - textX
	todo := ui.wrapTodo.get(fonts.small, m.todo, textW)
	if len(todo) > 4 {
		todo = todo[:4]
	}
	ev := ui.wrapEv.get(fonts.small, m.evidence, textW)
	if len(ev) > devCheckMaxLines+2 {
		ev = ev[:devCheckMaxLines+2]
	}
	lineH := fonts.smallH + 2
	block := fonts.titleH + 6 + int32(len(todo))*lineH
	if m.live != "" {
		block += 6 + lineH
	}
	if len(ev) > 0 {
		block += 8 + int32(len(ev))*lineH
	}
	if m.note != "" {
		block += 6 + lineH
	}
	heroH := max(int32(120), block+36)
	hero := sdl.Rect{X: x, Y: top + y, W: w, H: heroH}
	UICard(renderer, hero, DefaultUICardStyle())
	box := drawGlassBadge(renderer, sdl.Rect{X: hero.X + 22, Y: hero.Y + 18, W: badge, H: badge})
	DrawHomeIcon(renderer, m.icon, box, false)
	ty := hero.Y + 18
	titleW := textW
	if m.pill != "" {
		p := tbxDrawPill(renderer, config, fonts.tiny, hero.X+hero.W-22, ty+fonts.titleH/2, m.pill, m.pillTone)
		titleW = p.X - 12 - textX
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.headline, titleW), ColorTextPrimary(), textX, ty)
	ty += fonts.titleH + 6
	for _, line := range todo {
		renderText(renderer, config, fonts.small, line, ColorTextMuted(), textX, ty)
		ty += lineH
	}
	if m.live != "" {
		ty += 6
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.live, textW), ColorTextAccent(), textX, ty)
		ty += lineH
	}
	if len(ev) > 0 {
		ty += 8
		for _, line := range ev {
			renderText(renderer, config, fonts.small, line, ColorTextPrimary(), textX, ty)
			ty += lineH
		}
	}
	if m.note != "" {
		ty += 6
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.note, textW), ColorTextTertiary(), textX, ty)
	}
	y += heroH + 16

	// The step's buttons, then its answers; a row that does not fit wraps.
	drawButtons := func(buttons []devCheckButton) {
		if len(buttons) == 0 {
			return
		}
		bx := x
		for _, b := range buttons {
			bw := min(tbxButtonWidth(fonts.body, b.label, 140), w)
			if bx > x && bx+bw > x+w {
				bx = x
				row++
				y += 48 + 12
			}
			rect := sdl.Rect{X: bx, Y: y, W: bw, H: 48}
			screen := rect
			screen.Y += top
			tbxDrawButton(renderer, config, fonts.body, screen, ellipsize(fonts.body, b.label, bw-24), b.primary, nav.focused(b.id), b.disabled)
			nav.add(b.id, rect, row, b.disabled)
			bx += bw + 12
		}
		row++
		y += 48 + 12
	}
	drawButtons(m.actions)
	drawButtons(m.verdict)
	y += 10

	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}

	tbxDrawSection(renderer, config, "All checks", "checklist", x, top+y, w)
	y += tbxSectionHeight
	for i := range m.rows {
		drawRow("step:"+strconv.Itoa(i), m.rows[i])
	}
	y += 14

	tbxDrawSection(renderer, config, "Report", "document", x, top+y, w)
	y += tbxSectionHeight
	drawRow("save", tbxRowSpec{icon: "document", title: "Save report", subtitle: m.saveSub})
	drawRow("new", tbxRowSpec{icon: "refresh", title: "Start a new check", subtitle: m.newSub})
	drawRow("history", tbxRowSpec{icon: "history", title: "Earlier checks", subtitle: m.histSub})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}
