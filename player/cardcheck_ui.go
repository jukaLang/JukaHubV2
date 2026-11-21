package main

// cardcheck_ui.go — the Card check screen (Tools → System → Card check,
// Settings → About → Card check), drawn like the other tool screens
// (hotspot_ui.go).
//
// Top to bottom: the card that holds the games; what the check will do (read
// every file, the fake card test) and the Start / Stop row; one row per step
// of the last run, its outcome as a pill and its details in a sheet; the
// report; and how to repair a card on a computer. The disk work runs on the
// worker in cardcheck.go; this file reads its published state.

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// cardCheckSceneName is the scene the Tools hub and Settings open.
const cardCheckSceneName = "CardCheck"

type cardCheckScreenState struct {
	visit tbxVisit
	nav   tbxNav
	opts  cardCheckOptions
	// optsSet marks opts as chosen once (read every file starts on).
	optsSet bool
	// vol is the card as the visit found it, before any run.
	vol    *cardCheckVolume
	volErr string
}

var cardCheckScreen cardCheckScreenState

// cardCheckOpenScreen opens the screen. Cross-area contract: the Tools hub
// and Settings call it.
func cardCheckOpenScreen(config *Config) {
	tbxOpenScene(config, cardCheckSceneName)
}

type cardCheckScene struct{ sceneBase }

func (cardCheckScene) render(r *sdl.Renderer, c *Config) bool {
	if cardCheckScreen.visit.begin() {
		cardCheckScreenEnter()
	}
	cardCheckRenderScreen(r, c)
	return true
}

func (cardCheckScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), cardCheckSceneAction)
}

func (cardCheckScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), cardCheckSceneAction)
}

func init() { registerScene(cardCheckScene{sceneBase{name: cardCheckSceneName}}) }

// cardCheckSettingsRow is Settings → About's door to the screen.
func cardCheckSettingsRow() setRow {
	return setRow{
		ID: "cardCheck", Label: "Card check", Kind: setKindAction,
		Detail:  setStatic("Health flags, a read of every file and a fake card test for the games' SD card"),
		Visible: func(*Config) bool { return cardCheckSupported() },
		Value:   func(*Config) string { return cardCheckTileValue() },
		Open:    cardCheckOpenScreen,
	}
}

// cardCheckHubTile is the Tools hub tile (handheld only).
func cardCheckHubTile() toolsHubTile {
	t := toolsHubTile{id: "cardcheck", label: "Card check", icon: "disk", value: cardCheckTileValue()}
	snap := cardCheckSnapshot()
	switch {
	case snap.running:
		t.tone = toolsHubToneBusy
	case len(snap.steps) > 0 && cardCheckWorst(snap.steps) == ccBad:
		t.tone = toolsHubToneWarn
	}
	return t
}

// cardCheckTileValue is the one-liner Settings and the tile show.
func cardCheckTileValue() string {
	snap := cardCheckSnapshot()
	if snap.running {
		for _, st := range snap.steps {
			if st.state == ccRunning {
				return "Checking: " + strings.ToLower(st.title) + "…"
			}
		}
		return "Checking…"
	}
	if len(snap.steps) > 0 {
		return "Last check: " + cardCheckVerdict(snap.steps)
	}
	return "Is the SD card with your games healthy?"
}

// cardCheckScreenEnter runs once per visit: the options get their defaults
// once, and the card is looked up off the main thread.
func cardCheckScreenEnter() {
	cardCheckScreen.nav.scroll = 0
	if !cardCheckScreen.optsSet {
		cardCheckScreen.opts = cardCheckOptions{readAll: true}
		cardCheckScreen.optsSet = true
	}
	if !cardCheckSupported() {
		return
	}
	games := cardCheckGamesDir()
	go func() {
		vol, err := cardCheckFindVolume(games)
		tbxPost(func() {
			if err != nil {
				cardCheckScreen.vol, cardCheckScreen.volErr = nil, err.Error()
				return
			}
			cardCheckScreen.vol, cardCheckScreen.volErr = &vol, ""
		})
	}()
}

// ── Drawing ──────────────────────────────────────────────────────────────

// cardCheckPill is a step's pill and tone.
func cardCheckPill(st cardCheckStep) (string, tbxTone) {
	switch st.state {
	case ccRunning:
		return "Checking…", tbxToneAccent
	case ccOK:
		return "OK", tbxToneGood
	case ccWarn:
		return "Warning", tbxToneWarn
	case ccBad:
		return "Problem", tbxToneBad
	case ccSkipped:
		return "Skipped", tbxToneNeutral
	case ccStopped:
		return "Stopped", tbxToneNeutral
	}
	return "Waiting", tbxToneNeutral
}

// cardCheckStepIcon is a step's icon.
func cardCheckStepIcon(st cardCheckStep) string {
	switch st.state {
	case ccOK:
		return "check"
	case ccWarn, ccBad:
		return "warning"
	}
	switch st.id {
	case ccStepWhere:
		return "disk"
	case ccStepRead:
		return "files"
	case ccStepFake:
		return "scan"
	case ccStepFsck:
		return "checklist"
	}
	return "shield"
}

// cardCheckStepSubtitle is a step's line: its progress while it runs.
func cardCheckStepSubtitle(st cardCheckStep) string {
	if st.state != ccRunning {
		if st.summary != "" {
			return st.summary
		}
		return "Not run yet"
	}
	var parts []string
	if st.phase != "" {
		parts = append(parts, st.phase)
	}
	if st.total > 0 && st.bytes {
		pct := int(float64(st.done) * 100 / float64(st.total))
		if pct > 100 {
			pct = 100
		}
		parts = append(parts, fmt.Sprintf("%s of %s · %d%%", formatSize(st.done), formatSize(st.total), pct))
	}
	if len(parts) == 0 {
		return "Working…"
	}
	return strings.Join(parts, " · ")
}

// cardCheckFakeLabel is the fake card test choice as the row shows it.
func cardCheckFakeLabel(mode int) string {
	switch mode {
	case cardCheckFakeQuick:
		return "1 GB"
	case cardCheckFakeFull:
		return "All free space"
	}
	return "Off"
}

// cardCheckReadEstimate is roughly how long reading the used space takes.
func cardCheckReadEstimate(vol *cardCheckVolume) string {
	if vol == nil || vol.Total <= 0 {
		return ""
	}
	used := vol.Total - vol.Free
	mins := int(used/cardCheckReadRate/60) + 1
	if mins < 2 {
		return "about a minute"
	}
	return fmt.Sprintf("about %d minutes or more", mins)
}

func cardCheckRenderScreen(renderer *sdl.Renderer, config *Config) {
	snap := cardCheckSnapshot()
	status, statusColor := "", ColorTextMuted()
	switch {
	case snap.running:
		status, statusColor = "Checking…", ToastWarnInk()
	case len(snap.steps) > 0:
		status = "Last check: " + cardCheckVerdict(snap.steps)
		switch cardCheckWorst(snap.steps) {
		case ccOK:
			statusColor = ToastSuccessInk()
		case ccWarn, ccBad:
			statusColor = ToastWarnInk()
		}
	}
	header := tbxHeader("Card check", "Is the SD card with your games healthy?", "disk", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &cardCheckScreen.nav
	nav.begin(body.Y, body.H)

	if !cardCheckSupported() {
		tbxDrawEmpty(renderer, config, body, "disk", "For the handheld",
			"Card check looks at the SD card that holds the handheld's games: its health flags, every file on it, and whether it really holds what it claims. On a computer, use the system's own disk tools.")
		nav.end()
		tbxEndFrame(renderer, config)
		return
	}

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

	// The card.
	tbxDrawSection(renderer, config, "Your card", "disk", x, top+y, w)
	y += tbxSectionHeight
	vol := snap.vol
	if vol == nil {
		vol = cardCheckScreen.vol
	}
	switch {
	case vol != nil:
		_, summary, _ := cardCheckWhereResult(*vol)
		title := "Games card"
		if vol.CardName != "" {
			title = "Games card · " + vol.CardName
		}
		drawRow("card", tbxRowSpec{icon: "disk", title: title, subtitle: summary, pill: "Details", tone: tbxToneNeutral})
	case cardCheckScreen.volErr != "":
		drawRow("card", tbxRowSpec{icon: "warning", title: "Games card", subtitle: cardCheckScreen.volErr, pill: "Not found", tone: tbxToneWarn})
	default:
		drawRow("", tbxRowSpec{icon: "disk", title: "Games card", subtitle: "Looking…", dim: true})
	}
	y += 14

	// What the check does.
	tbxDrawSection(renderer, config, "Check", "checklist", x, top+y, w)
	y += tbxSectionHeight
	opts := cardCheckScreen.opts
	readSub := "Finds files the card can no longer read"
	if est := cardCheckReadEstimate(vol); est != "" {
		readSub += " · " + est
	}
	toggle := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	toggleScreen := toggle
	toggleScreen.Y += top
	cardCheckDrawToggleRow(renderer, config, fonts, toggleScreen, "Read every file", readSub, opts.readAll, nav.focused("optRead"))
	nav.add("optRead", toggle, row, false)
	row++
	y += rowH + 8
	fakeSub := "Writes test files into free space, reads them back, deletes them"
	if opts.fake == cardCheckFakeFull {
		fakeSub = "Fills the free space: finds a card that claims more than it holds · can take hours"
	}
	drawRow("optFake", tbxRowSpec{icon: "scan", title: "Fake card test", subtitle: fakeSub, pill: cardCheckFakeLabel(opts.fake), tone: tbxToneNeutral})
	if snap.running {
		drawRow("start", tbxRowSpec{icon: "stop", title: "Stop the check", subtitle: "What has been found so far is kept", pill: "Stop", tone: tbxToneWarn})
	} else {
		drawRow("start", tbxRowSpec{icon: "play", title: "Start the check", subtitle: "Nothing on the card is changed; test files are deleted", pill: "Start", tone: tbxToneAccent})
	}
	y += 14

	// The last run.
	if len(snap.steps) > 0 {
		tbxDrawSection(renderer, config, "Results", "check", x, top+y, w)
		y += tbxSectionHeight
		for _, st := range snap.steps {
			pill, tone := cardCheckPill(st)
			drawRow("step:"+st.id, tbxRowSpec{icon: cardCheckStepIcon(st), title: st.title, subtitle: cardCheckStepSubtitle(st), pill: pill, tone: tone,
				dim: st.state == ccSkipped || st.state == ccPending})
		}
		if !snap.running && snap.report != "" {
			sub := "The whole check as text"
			if snap.saved != "" {
				sub = "Saved as " + snap.saved
			}
			drawRow("report", tbxRowSpec{icon: "text", title: "Report", subtitle: sub, pill: "Open", tone: tbxToneAccent})
		}
		y += 14
	}

	tbxDrawSection(renderer, config, "Repair", "info", x, top+y, w)
	y += tbxSectionHeight
	drawRow("repair", tbxRowSpec{icon: "info", title: "How to repair a card", subtitle: "On a computer: chkdsk on Windows, First Aid on a Mac", pill: "Read", tone: tbxToneNeutral})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// cardCheckDrawToggleRow draws a switch row: the title over its line, the
// switch at the right edge.
func cardCheckDrawToggleRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, title, sub string, on, focused bool) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	toggle := tbxDrawToggle(renderer, r.X+r.W-18, r.Y+r.H/2, on)
	textW := toggle.X - r.X - 34
	block := fonts.titleH + 3 + fonts.smallH
	ty := r.Y + (r.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), r.X+18, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), ColorTextMuted(), r.X+18, ty+fonts.titleH+3)
}

// ── Input ────────────────────────────────────────────────────────────────

func cardCheckSceneAction(config *Config, act tbxAction) bool {
	nav := &cardCheckScreen.nav
	switch act {
	case tbxActConfirm:
		cardCheckActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func cardCheckSceneClick(mx, my int32, config *Config) bool {
	it, ok := cardCheckScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	cardCheckScreen.nav.focus = it.id
	cardCheckActivate(config, it.id)
	return true
}

func cardCheckSceneHints() []FooterHint {
	if !cardCheckSupported() {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	focus := cardCheckScreen.nav.focus
	switch {
	case focus == "optRead":
		hints = append(hints, FooterHint{Button: "A", Label: "Switch"})
	case focus == "optFake":
		hints = append(hints, FooterHint{Button: "A", Label: "Change"})
	case focus == "start":
		if cardCheckRunning() {
			hints = append(hints, FooterHint{Button: "A", Label: "Stop"})
		} else {
			hints = append(hints, FooterHint{Button: "A", Label: "Start"})
		}
	case focus == "report", focus == "repair":
		hints = append(hints, FooterHint{Button: "A", Label: "Read"})
	case focus == "card", strings.HasPrefix(focus, "step:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Details"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func cardCheckActivate(config *Config, id string) {
	if !cardCheckSupported() {
		return
	}
	switch {
	case id == "optRead":
		if cardCheckRunning() {
			ShowToast("Stop the check first", ToastKindInfo)
			return
		}
		PlayActivateSound()
		cardCheckScreen.opts.readAll = !cardCheckScreen.opts.readAll
	case id == "optFake":
		if cardCheckRunning() {
			ShowToast("Stop the check first", ToastKindInfo)
			return
		}
		PlayActivateSound()
		cardCheckScreen.opts.fake = (cardCheckScreen.opts.fake + 1) % 3
	case id == "start":
		if cardCheckRunning() {
			cardCheckAskStop(config)
			return
		}
		cardCheckAskStart(config)
	case id == "card":
		cardCheckOpenCardSheet(config)
	case strings.HasPrefix(id, "step:"):
		cardCheckOpenStepSheet(config, strings.TrimPrefix(id, "step:"))
	case id == "report":
		snap := cardCheckSnapshot()
		if snap.report == "" {
			return
		}
		PlayActivateSound()
		sub := "Not saved"
		if snap.saved != "" {
			sub = "Saved as " + snap.saved
		}
		tbxOpenSheet(config, tbxSheetSpec{title: "Card check report", subtitle: sub, icon: "text", body: snap.report, tall: true,
			buttons: []tbxSheetButton{{label: "Close", primary: true}}})
	case id == "repair":
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{title: "How to repair a card", subtitle: "On a computer, with the card out of the handheld", icon: "info",
			body:    cardCheckRepairText + "\n\n## Why not here\nThe card holds the system that is running, and a repair must have the card to itself. None of the handheld's start-up paths can check the card before they use it.",
			buttons: []tbxSheetButton{{label: "OK", primary: true}}})
	}
}

// cardCheckAskStart starts the check; the fake card test asks first, since
// it fills the card for a while.
func cardCheckAskStart(config *Config) {
	opts := cardCheckScreen.opts
	opts.gamesDir = cardCheckGamesDir()
	start := func() {
		if !cardCheckStart(opts) {
			ShowToast("A check is already running", ToastKindInfo)
			return
		}
		cardCheckScreen.nav.setFocus("start")
		ShowToast("Card check started — you can leave this screen", ToastKindInfo)
	}
	if opts.fake == cardCheckFakeOff {
		PlayActivateSound()
		start()
		return
	}
	body := "Test files go into the card's free space, are read back and are deleted again. Games cannot start until it is done.\n\n" +
		"## Before you start\n• Copy your saves to a computer first: on a fake card, writing past what it really holds can damage files already on it — the same thing that would happen the day you fill it with games.\n" +
		fmt.Sprintf("• Plug in the charger, or start with at least %d%%.\n", cardCheckMinBattery)
	if opts.fake == cardCheckFakeFull {
		body += "• Filling all the free space can take hours on a large card."
	} else {
		body += "• The quick test writes 1 GB: a card that lies about its size usually only shows it near the end, which the full test reaches."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Test the card's free space?", subtitle: "Fake card test · " + cardCheckFakeLabel(opts.fake), icon: "scan", body: body,
		buttons: []tbxSheetButton{{label: "Cancel"}, {label: "Start", primary: true, run: start}},
	})
}

// cardCheckAskStop stops a running check after asking.
func cardCheckAskStop(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Stop the card check?", icon: "stop",
		body:    "What has been found so far stays in the report. Test files are deleted.",
		buttons: []tbxSheetButton{{label: "Keep checking"}, {label: "Stop", primary: true, run: func() { cardCheckStop() }}},
	})
}

// cardCheckOpenCardSheet shows the card's details.
func cardCheckOpenCardSheet(config *Config) {
	vol := cardCheckSnapshot().vol
	if vol == nil {
		vol = cardCheckScreen.vol
	}
	PlayActivateSound()
	if vol == nil {
		tbxOpenSheet(config, tbxSheetSpec{title: "Games card", icon: "warning", body: cardCheckScreen.volErr,
			buttons: []tbxSheetButton{{label: "OK", primary: true}}})
		return
	}
	_, summary, lines := cardCheckWhereResult(*vol)
	tbxOpenSheet(config, tbxSheetSpec{title: "Games card", subtitle: summary, icon: "disk", body: strings.Join(lines, "\n"),
		buttons: []tbxSheetButton{{label: "OK", primary: true}}})
}

// cardCheckOpenStepSheet shows one step's details.
func cardCheckOpenStepSheet(config *Config, id string) {
	for _, st := range cardCheckSnapshot().steps {
		if st.id != id {
			continue
		}
		PlayActivateSound()
		body := strings.Join(st.lines, "\n")
		if body == "" {
			body = cardCheckStepSubtitle(st)
		}
		pill, _ := cardCheckPill(st)
		tbxOpenSheet(config, tbxSheetSpec{title: st.title, subtitle: pill + " · " + cardCheckStepSubtitle(st), icon: cardCheckStepIcon(st),
			body: body, tall: len(st.lines) > 8, buttons: []tbxSheetButton{{label: "OK", primary: true}}})
		return
	}
}
