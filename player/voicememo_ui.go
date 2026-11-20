package main

// voicememo_ui.go — Voice Memos on screen: the recorder card, the memo list
// with the playing memo's progress, the Microphone test page, the Options
// sheet, the footer, and mouse clicks and the wheel.
//
// The page is a tool screen (tbxui.go): the same frame, header, rows, focus
// ring and delete question as the Voice shortcuts page beside it. The Options
// sheet is this app's own: a short vertical list, because the tool screens'
// sheet lays its buttons out in one row, which five actions overflow on a
// 640-pixel screen.

import (
	"math"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// The list's fixed focus ids (a memo's is "m:<file>").
const (
	vmIDRecord   = "rec"
	vmIDRecPause = "pause"
	vmIDRecStop  = "stop"
	vmIDMicTest  = "mictest"
)

// vmMenuItem is one line of the Options sheet.
type vmMenuItem struct {
	id    string
	label string
	sub   string
	icon  string
}

// vmMenu is the Options sheet (X). Main thread only.
type vmMenu struct {
	open  bool
	file  string // the memo it acts on, "" for none
	title string
	sub   string
	items []vmMenuItem
	focus int
	rects []sdl.Rect
	card  sdl.Rect
}

// vmProgress is the playing memo's progress bar as drawn, so a click on it
// seeks. Main thread only.
var vmProgress struct {
	rect sdl.Rect
	file string
}

// ── Header ───────────────────────────────────────────────────────────────

// vmHeader is the page header: the count and size of the memos, and the
// microphone's state opposite the title.
func vmHeader() UIPageHeader {
	if vm.mode == vmModeTest {
		status, color := vmMicStatus()
		return tbxHeader("Microphone test", "Voice Memos checks the microphone before your first memo", "mic", status, color)
	}
	sub := "Record, name and replay short recordings"
	if vm.loaded {
		var total int64
		for _, m := range vm.memos {
			total += m.size
		}
		switch n := len(vm.memos); n {
		case 0:
			sub = "No memos yet"
		case 1:
			sub = "1 memo · " + formatSize(total)
		default:
			sub = strconv.Itoa(n) + " memos · " + formatSize(total)
		}
	}
	if vm.rec != nil {
		word := "Recording "
		if vm.rec.paused {
			word = "Paused "
		}
		return tbxHeader("Voice Memos", sub, "mic", word+vmClock(vm.rec.elapsed()), tbxToneColor(tbxToneBad))
	}
	status, color := vmMicStatus()
	return tbxHeader("Voice Memos", sub, "mic", status, color)
}

// vmMicStatus is the header's word about the microphone.
func vmMicStatus() (string, sdl.Color) {
	switch {
	case vkMic.Load() == vkMicAbsent:
		return "No microphone", tbxToneColor(tbxToneBad)
	case vm.saved.TestPassed:
		return "Microphone works", tbxToneColor(tbxToneGood)
	case vm.saved.TestResult != "":
		return "Microphone: " + vm.saved.TestResult, tbxToneColor(tbxToneWarn)
	}
	return "Microphone not tested", tbxToneColor(tbxToneWarn)
}

// vmTestSummary is the Microphone test row's line.
func vmTestSummary() string {
	switch {
	case vm.saved.TestPassed && vm.saved.TestDevice != "":
		return "Last test: works on " + vm.saved.TestDevice
	case vm.saved.TestResult != "":
		return "Last test: " + vm.saved.TestResult
	}
	return "Check that the microphone opens, hears you and plays back"
}

// ── Page ─────────────────────────────────────────────────────────────────

// vmRender draws the page on show and the Options sheet over it.
func vmRender(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, vmHeader())
	body := frame.bodyRect()
	if vm.mode == vmModeTest {
		vmRenderTest(renderer, config, fonts, frame, body)
	} else {
		vmRenderList(renderer, config, fonts, frame, body)
	}
	tbxEndFrame(renderer, config)
	if vm.menu.open {
		vmRenderMenu(renderer, config, fonts)
	}
}

// vmRenderList draws the recorder card and the memos.
func vmRenderList(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect) {
	nav := &vm.nav
	nav.begin(body.Y, body.H)
	vmProgress.rect, vmProgress.file = sdl.Rect{}, ""
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0

	if r := vm.rec; r != nil {
		h := vmRecorderHeight(fonts)
		pause, stop := vmDrawRecorder(renderer, config, fonts, sdl.Rect{X: x, Y: top + y, W: w, H: h}, r)
		pause.Y -= top
		stop.Y -= top
		nav.add(vmIDRecPause, pause, row, false)
		nav.add(vmIDRecStop, stop, row, false)
		nav.extend(y + h)
		y += h + 16
	} else {
		h := vmRecordCardHeight(fonts)
		rect := sdl.Rect{X: x, Y: y, W: w, H: h}
		screen := rect
		screen.Y += top
		vmDrawRecordCard(renderer, config, fonts, screen, nav.focused(vmIDRecord))
		nav.add(vmIDRecord, rect, row, false)
		y += h + 16
	}
	row++

	rowH := tbxRowHeight(fonts)
	switch {
	case !vm.loaded:
		renderText(renderer, config, fonts.small, "Reading the memos…", ColorTextTertiary(), x+4, top+y+8)
		y += fonts.smallH + 16
	case len(vm.memos) == 0:
		emptyH := int32(130)
		hint := hintSentence("Press %s to record your first one", "Y / R", "Record")
		tbxDrawEmpty(renderer, config, sdl.Rect{X: x, Y: top + y, W: w, H: emptyH}, "mic", "No memos yet", hint)
		y += emptyH + 8
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, tbxRowSpec{icon: "mic", title: "Microphone test", subtitle: vmTestSummary()}, nav.focused(vmIDMicTest))
		nav.add(vmIDMicTest, rect, row, false)
		y += rowH + 8
	default:
		tbxDrawSection(renderer, config, "Memos", "music", x, top+y, w)
		y += tbxSectionHeight
		for _, m := range vm.memos {
			var p *vmPlayer
			if vm.play != nil && vm.play.file == m.file {
				p = vm.play
			}
			h := rowH
			if p != nil {
				h += 26
			}
			rect := sdl.Rect{X: x, Y: y, W: w, H: h}
			screen := rect
			screen.Y += top
			if screen.Y+h >= body.Y && screen.Y <= body.Y+body.H {
				vmDrawMemoRow(renderer, config, fonts, screen, rowH, m, nav.focused(m.id()), p)
			}
			nav.add(m.id(), rect, row, false)
			row++
			y += h + 8
		}
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
}

// vmRecordCardHeight is the idle recorder card's height.
func vmRecordCardHeight(fonts tbxFonts) int32 {
	return max(int32(92), fonts.titleH+fonts.smallH+44)
}

// vmDrawRecordCard draws the idle recorder: a record button and how to start.
func vmDrawRecordCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, focused bool) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	r := min(int32(28), rect.H/3)
	cx := rect.X + 22 + r
	cy := rect.Y + rect.H/2
	fillCircle(renderer, cx, cy, r, WithAlpha(ColorToastError, 230))
	strokeCircle(renderer, cx, cy, r+3, WithAlpha(ColorToastError, 90))
	fillCircle(renderer, cx, cy, r/3, sdl.Color{R: 255, G: 255, B: 255, A: 240})
	x := cx + r + 22
	textW := rect.X + rect.W - 18 - x
	title := "New memo"
	sub := hintSentence("Press %s to record — up to 30 minutes, saved as you go", "Y / R", "Record")
	block := fonts.titleH + 4 + fonts.smallH
	ty := rect.Y + (rect.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), x, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), ColorTextMuted(), x, ty+fonts.titleH+4)
}

// vmRecorderHeight is the recording card's height.
func vmRecorderHeight(fonts tbxFonts) int32 {
	return fonts.titleH + fonts.smallH + 44 + 52 + 46 + 18
}

// vmDrawRecorder draws the recording in progress — the pulsing dot, the
// clock, the waveform, the level and the Pause and Stop buttons — and returns
// the buttons' rects (on screen).
func vmDrawRecorder(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, r *vmRecording) (sdl.Rect, sdl.Rect) {
	UICard(renderer, rect, DefaultUICardStyle())
	pad := int32(20)
	x := rect.X + pad
	w := rect.W - 2*pad
	y := rect.Y + 16

	// The dot pulses while recording and holds still while paused.
	red := ColorToastError
	alpha := uint8(255)
	if !r.paused {
		phase := float64(sdl.GetTicks64()%1200) / 1200
		alpha = uint8(150 + 105*math.Abs(math.Cos(phase*math.Pi)))
	} else {
		red = ColorToastWarn
	}
	dotR := int32(8)
	fillCircle(renderer, x+dotR, y+fonts.titleH/2, dotR, WithAlpha(red, alpha))
	word := "Recording"
	if r.paused {
		word = "Paused"
	}
	renderText(renderer, config, fonts.title, word, ColorTextPrimary(), x+2*dotR+10, y)
	clock := vmClock(r.elapsed()) + " / " + vmClock(vmMaxRecord)
	cw, _ := measureText(fonts.title, clock)
	renderText(renderer, config, fonts.title, clock, ColorTextPrimary(), x+w-cw, y)
	y += fonts.titleH + 4
	via := "From " + r.via
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, via, w), ColorTextMuted(), x, y)
	y += fonts.smallH + 12

	// The waveform: the last few seconds, one bar per twentieth of a second.
	waveH := int32(44)
	vmDrawWave(renderer, sdl.Rect{X: x, Y: y, W: w, H: waveH}, r.wave, r.paused)
	y += waveH + 8

	// The level, and the buttons.
	bh := int32(44)
	stopW := tbxButtonWidth(fonts.body, "Stop & save", 150)
	pauseLabel := "Pause"
	if r.paused {
		pauseLabel = "Resume"
	}
	pauseW := tbxButtonWidth(fonts.body, pauseLabel, 120)
	stop := sdl.Rect{X: x + w - stopW, Y: y, W: stopW, H: bh}
	pause := sdl.Rect{X: stop.X - 12 - pauseW, Y: y, W: pauseW, H: bh}
	meterW := pause.X - 20 - x
	if meterW > 40 {
		vmDrawMeter(renderer, sdl.Rect{X: x, Y: y + bh/2 - 4, W: meterW, H: 8}, r.level)
	}
	tbxDrawButton(renderer, config, fonts.body, pause, pauseLabel, false, vm.nav.focused(vmIDRecPause), false)
	tbxDrawButton(renderer, config, fonts.body, stop, "Stop & save", true, vm.nav.focused(vmIDRecStop), false)
	return pause, stop
}

// vmMeterFraction maps an RMS level (0..1) onto a meter: 50 dB of range,
// which a voice at arm's length fills about two thirds of. Pure.
func vmMeterFraction(level float64) float64 {
	if level <= 0 {
		return 0
	}
	f := (20*math.Log10(level) + 50) / 50
	return math.Max(0, math.Min(1, f))
}

// vmDrawMeter draws a level meter: green, amber near the top, red at it.
func vmDrawMeter(renderer *sdl.Renderer, rect sdl.Rect, level float64) {
	f := vmMeterFraction(level)
	fill := ColorToastSuccess
	switch {
	case f >= 0.94:
		fill = ColorToastError
	case f >= 0.8:
		fill = ColorToastWarn
	}
	UIProgressBar(renderer, rect, f, glassA(36), fill)
}

// vmDrawWave draws the rolling waveform, newest bar at the right.
func vmDrawWave(renderer *sdl.Renderer, rect sdl.Rect, wave []float32, paused bool) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, glassA(14))
	step := rect.W / vmWaveBars
	if step < 2 {
		step = 2
	}
	barW := max(step-2, 1)
	ink := WithAlpha(glassAccent(), 220)
	if paused {
		ink = WithAlpha(glassAccent(), 110)
	}
	mid := rect.Y + rect.H/2
	right := rect.X + rect.W - 6
	for i := len(wave) - 1; i >= 0; i-- {
		bx := right - int32(len(wave)-1-i)*step - barW
		if bx < rect.X+4 {
			break
		}
		h := int32(vmMeterFraction(float64(wave[i])) * float64(rect.H-8))
		if h < 2 {
			h = 2
		}
		fillRoundedRect(renderer, bx, mid-h/2, barW, h, barW/2, ink)
	}
}

// vmDrawMemoRow draws one memo: play or pause badge, name, length and size,
// and for the memo in the player a progress bar under it.
func vmDrawMemoRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, rowH int32, m vmMemo, focused bool, p *vmPlayer) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	icon := "play"
	if p != nil && !p.isPaused() {
		icon = "pause"
	}
	badge := min(max(rowH-24, 28), 44)
	box := drawGlassBadge(renderer, sdl.Rect{X: rect.X + 14, Y: rect.Y + (rowH-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, icon, box, focused)
	x := rect.X + 14 + badge + 14
	right := rect.X + rect.W - 16
	if p != nil {
		word, tone := "Playing", tbxToneAccent
		if p.isPaused() {
			word, tone = "Paused", tbxToneNeutral
		}
		pill := tbxDrawPill(renderer, config, fonts.tiny, right, rect.Y+rowH/2, word, tone)
		right = pill.X - 12
	}
	textW := max(right-x, 40)
	titleColor, subColor := ColorTextPrimary(), ColorTextMuted()
	if !m.ok {
		titleColor, subColor = ColorTextTertiary(), ColorTextTertiary()
	}
	block := fonts.titleH + 4 + fonts.smallH
	ty := rect.Y + (rowH-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.name, textW), titleColor, x, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, vmMemoDetail(m), textW), subColor, x, ty+fonts.titleH+4)
	if p == nil {
		return
	}
	line := vmPlayLine(p)
	lw, lh := measureText(fonts.tiny, line)
	barRight := rect.X + rect.W - 16 - lw - 12
	bar := sdl.Rect{X: x, Y: rect.Y + rowH + 2, W: barRight - x, H: 6}
	if bar.W > 20 {
		frac := 0.0
		if total := p.length(); total > 0 {
			frac = float64(p.position()) / float64(total)
		}
		UIProgressBar(renderer, bar, frac, glassA(36), glassAccent())
		// A generous target around the thin bar, for a finger or a mouse.
		vmProgress.rect = sdl.Rect{X: bar.X, Y: bar.Y - 10, W: bar.W, H: bar.H + 20}
		vmProgress.file = m.file
	}
	renderText(renderer, config, fonts.tiny, line, ColorTextMuted(), rect.X+rect.W-16-lw, bar.Y+bar.H/2-lh/2)
}

// ── Microphone test page ─────────────────────────────────────────────────

// vmRenderTest draws the test: what it is doing (or found), the device, the
// level, the checklist and the buttons. On a short screen the checklist is
// left out before anything else.
func vmRenderTest(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect) {
	vm.nav.begin(body.Y, body.H)
	vm.nav.end()
	x, w := frame.x, frame.w
	headline, sub := vmTestHeadline()

	badge := int32(64)
	textX := x + 22 + badge + 22
	textW := x + w - 22 - textX
	lines := vmTest.wrap.get(fonts.small, sub, textW)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	block := fonts.titleH + 6 + int32(len(lines))*(fonts.smallH+2)
	heroH := max(int32(116), block+36)
	hero := sdl.Rect{X: x, Y: body.Y, W: w, H: heroH}
	UICard(renderer, hero, DefaultUICardStyle())
	bx := hero.X + 22
	by := hero.Y + (heroH-badge)/2
	if vmTest.phase == vmTestListening || vmTest.phase == vmTestSampling {
		// A ring that swells with the voice: the microphone is live.
		swell := int32(vmMeterFraction(vmTest.level) * 14)
		strokeCircle(renderer, bx+badge/2, by+badge/2, badge/2+4+swell, WithAlpha(glassAccent(), 160))
	}
	box := drawGlassBadge(renderer, sdl.Rect{X: bx, Y: by, W: badge, H: badge})
	DrawHomeIcon(renderer, "mic", box, false)
	ink := ColorTextPrimary()
	if vmTest.phase == vmTestDone {
		switch vmTest.result {
		case vmResultWorks, vmResultQuiet:
			ink = ToastSuccessInk()
		default:
			ink = DangerInk()
		}
	}
	ty := hero.Y + (heroH-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, headline, textW), ink, textX, ty)
	ty += fonts.titleH + 6
	for _, line := range lines {
		renderText(renderer, config, fonts.small, line, ColorTextMuted(), textX, ty)
		ty += fonts.smallH + 2
	}
	y := hero.Y + heroH + 16

	// The buttons sit at the bottom of the body.
	buttons := vmTestButtons()
	if vmTest.focus >= len(buttons) {
		vmTest.focus = len(buttons) - 1
	}
	bh := int32(46)
	btnY := body.Y + body.H - bh
	if btnY < y {
		btnY = y
	}

	// The device and the level.
	device := "Device: —"
	if vmTest.via != "" {
		device = "Device: " + vmTest.via
	}
	if y+fonts.smallH+20 <= btnY-8 {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, device, w/2), ColorTextMuted(), x, y)
		meterX := x + w/2 + 12
		vmDrawMeter(renderer, sdl.Rect{X: meterX, Y: y + fonts.smallH/2 - 4, W: x + w - meterX, H: 8}, vmTest.level)
		y += fonts.smallH + 18
	}

	// The checklist, while there is room for it.
	stepH := fonts.bodyH + 12
	if y+4*stepH <= btnY-8 {
		steps := vmTestSteps()
		for i, label := range vmTestStepLabels {
			vmDrawTestStep(renderer, config, fonts, x, y, stepH, label, steps[i])
			y += stepH
		}
	}

	vmTest.rects = vmTest.rects[:0]
	bxr := x + w
	rects := make([]sdl.Rect, len(buttons))
	for i := len(buttons) - 1; i >= 0; i-- {
		bw := tbxButtonWidth(fonts.body, buttons[i].label, 140)
		bxr -= bw
		rects[i] = sdl.Rect{X: bxr, Y: btnY, W: bw, H: bh}
		bxr -= 12
	}
	for i, b := range buttons {
		tbxDrawButton(renderer, config, fonts.body, rects[i], b.label, b.primary, i == vmTest.focus && !tbxChromeFocused(), false)
	}
	vmTest.rects = append(vmTest.rects, rects...)
}

// vmDrawTestStep draws one checklist line with its mark.
func vmDrawTestStep(renderer *sdl.Renderer, config *Config, fonts tbxFonts, x, y, h int32, label string, state int) {
	size := int32(22)
	box := sdl.Rect{X: x + 4, Y: y + (h-size)/2, W: size, H: size}
	ink := ColorTextPrimary()
	switch state {
	case 0:
		strokeCircle(renderer, box.X+size/2, box.Y+size/2, size/2-2, glassA(70))
		ink = ColorTextTertiary()
	case 1:
		phase := float64(sdl.GetTicks64()%1000) / 1000
		alpha := uint8(120 + 135*math.Abs(math.Cos(phase*math.Pi)))
		fillCircle(renderer, box.X+size/2, box.Y+size/2, size/2-4, WithAlpha(glassAccent(), alpha))
	case 2:
		iconDraw(renderer, "check", box, ToastSuccessInk())
	case 3:
		iconDraw(renderer, "close", box, DangerInk())
		ink = DangerInk()
	}
	_, th := measureText(fonts.body, label)
	renderText(renderer, config, fonts.body, label, ink, box.X+size+14, y+(h-th)/2)
}

// ── Options sheet ────────────────────────────────────────────────────────

// vmOpenMenu opens the Options sheet for the focused memo (or, with none
// focused, the app's own actions).
func vmOpenMenu() {
	m, ok := vmFocusedMemo()
	var items []vmMenuItem
	where := vmDisplayDir()
	title, sub := "Voice Memos", where
	if ok {
		title, sub = m.name, vmMemoDetail(m)
		play := "Play"
		if vm.play != nil && vm.play.file == m.file {
			play = "Pause"
			if vm.play.isPaused() {
				play = "Resume"
			}
		}
		items = append(items,
			vmMenuItem{id: "play", label: play, sub: "Listen to the memo", icon: "play"},
			vmMenuItem{id: "rename", label: "Rename", sub: "Give it a name you will find again", icon: "edit"},
			vmMenuItem{id: "phone", label: "Send to phone", sub: "A QR code the phone scans to download it", icon: "phone"},
			vmMenuItem{id: "delete", label: "Delete", sub: "Remove it from the card", icon: "trash"},
		)
	} else {
		items = append(items, vmMenuItem{id: "record", label: "Record a memo", sub: "Up to 30 minutes", icon: "mic"})
	}
	items = append(items,
		vmMenuItem{id: "mictest", label: "Microphone test", sub: vmTestSummary(), icon: "mic"},
		vmMenuItem{id: "folder", label: "Where memos are kept", sub: where, icon: "folder"},
	)
	vm.menu = vmMenu{open: true, file: m.file, title: title, sub: sub, items: items}
	PlayActivateSound()
}

// vmMenuAction is one action on the Options sheet.
func vmMenuAction(c *Config, act tbxAction) {
	n := len(vm.menu.items)
	switch act {
	case tbxActUp, tbxActLeft:
		if vm.menu.focus > 0 {
			vm.menu.focus--
			PlayNavSound()
		}
	case tbxActDown, tbxActRight:
		if vm.menu.focus < n-1 {
			vm.menu.focus++
			PlayNavSound()
		}
	case tbxActHome, tbxActPageUp:
		vm.menu.focus = 0
	case tbxActEnd, tbxActPageDown:
		vm.menu.focus = max(n-1, 0)
	case tbxActConfirm:
		if vm.menu.focus >= 0 && vm.menu.focus < n {
			vmMenuRun(c, vm.menu.items[vm.menu.focus].id)
		}
	case tbxActBack, tbxActX:
		vm.menu.open = false
		PlayBackSound()
	}
}

// vmMenuRun closes the sheet and does what its line says.
func vmMenuRun(c *Config, id string) {
	file := vm.menu.file
	vm.menu.open = false
	var m vmMemo
	have := false
	for _, x := range vm.memos {
		if x.file == file {
			m, have = x, true
			break
		}
	}
	switch id {
	case "play":
		if have {
			vmPlayToggle(m)
		}
	case "rename":
		if have {
			vmAskRename(m)
		}
	case "phone":
		if have {
			PlayActivateSound()
			vmSendToPhone(m)
		}
	case "delete":
		if have {
			vmAskDelete(c, m)
		}
	case "record":
		vmRecStart()
	case "mictest":
		PlayActivateSound()
		vmOpenTest()
	case "folder":
		PlayActivateSound()
		ShowToast("Memos are kept in "+vmDisplayDir(), ToastKindInfo)
	}
}

// vmMenuClick handles a click while the sheet is up: a line runs, anywhere
// outside the card closes it.
func vmMenuClick(c *Config, mx, my int32) {
	for i, r := range vm.menu.rects {
		if r.W > 0 && pointInRect(mx, my, r) && i < len(vm.menu.items) {
			vm.menu.focus = i
			PlayActivateSound()
			vmMenuRun(c, vm.menu.items[i].id)
			return
		}
	}
	if vm.menu.card.W > 0 && !pointInRect(mx, my, vm.menu.card) {
		vm.menu.open = false
		PlayBackSound()
	}
}

// vmRenderMenu draws the Options sheet: the memo's name over its actions.
// Lines drop their second line on a short screen so all of them fit.
func vmRenderMenu(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	items := vm.menu.items
	cardW := min(int32(580), screenWidth-48)
	header := UIPanelHeader(vm.menu.title, vm.menu.sub, "mic")
	headerH := UIPanelHeaderBand(config, header, 8)
	rowH := max(fonts.titleH+fonts.smallH+22, int32(56))
	withSub := true
	n := int32(len(items))
	if headerH+n*(rowH+6)+24 > screenHeight-32 {
		rowH = max(fonts.titleH+20, int32(44))
		withSub = false
	}
	cardH := headerH + n*(rowH+6) + 24
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	if card.Y < 16 {
		card.Y = 16
	}
	vm.menu.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	if cap(vm.menu.rects) < len(items) {
		vm.menu.rects = make([]sdl.Rect, len(items))
	}
	vm.menu.rects = vm.menu.rects[:len(items)]
	y := card.Y + headerH
	for i, it := range items {
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: rowH}
		spec := tbxRowSpec{icon: it.icon, title: it.label}
		if withSub {
			spec.subtitle = it.sub
		}
		tbxDrawRow(renderer, config, fonts, rect, spec, i == vm.menu.focus)
		vm.menu.rects[i] = rect
		y += rowH + 6
	}
}

// ── Contract: footer, mouse, wheel ───────────────────────────────────────

// voiceMemoFooterHints names the keys of what is on show.
func voiceMemoFooterHints() []FooterHint {
	if tbxSheetVisible(appConfig) {
		return tbxSheetHints()
	}
	if vm.menu.open {
		return []FooterHint{{Button: "Up/Down", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	}
	if vm.mode == vmModeTest {
		buttons := vmTestButtons()
		label := buttons[0].label
		if vmTest.focus >= 0 && vmTest.focus < len(buttons) {
			label = buttons[vmTest.focus].label
		}
		if len(buttons) > 1 {
			return []FooterHint{{Button: "Left/Right", Label: "Choose"}, {Button: "A", Label: label}, {Button: "B", Label: "Memos"}}
		}
		return []FooterHint{{Button: "A", Label: label}, {Button: "B", Label: "Skip"}}
	}
	if r := vm.rec; r != nil {
		pause := "Pause"
		if r.paused {
			pause = "Resume"
		}
		return []FooterHint{{Button: "A", Label: "Stop & save"}, {Button: "X / Space", Label: pause}, {Button: "B", Label: "Stop & save"}}
	}
	if p := vm.play; p != nil {
		a := "Play"
		if vm.nav.focus == "m:"+p.file {
			a = "Pause"
			if p.isPaused() {
				a = "Resume"
			}
		}
		return []FooterHint{{Button: "A", Label: a}, {Button: "L1/R1 / ← →", Label: "-5 s / +5 s"}, {Button: "X", Label: "Options"}, {Button: "B", Label: "Stop"}}
	}
	switch vm.nav.focus {
	case vmIDRecord:
		return []FooterHint{{Button: "A", Label: "Record"}, {Button: "X", Label: "Options"}, {Button: "B", Label: "Back"}}
	case vmIDMicTest:
		return []FooterHint{{Button: "A", Label: "Test"}, {Button: "Y / R", Label: "Record"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "A", Label: "Play"}, {Button: "X", Label: "Options"}, {Button: "Y / R", Label: "Record"}, {Button: "B", Label: "Back"}}
}

// voiceMemoClick routes a click: the delete question, the Options sheet, the
// test's buttons, the progress bar (seek), then the list.
func voiceMemoClick(mx, my int32, config *Config) bool {
	if tbxCurrentScene(config) != voiceMemoSceneName {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	if vm.menu.open {
		vmMenuClick(config, mx, my)
		return true
	}
	if vm.mode == vmModeTest {
		buttons := vmTestButtons()
		for i, r := range vmTest.rects {
			if i < len(buttons) && pointInRect(mx, my, r) {
				vmTest.focus = i
				vmTestPress(buttons[i].id)
				return true
			}
		}
		return false
	}
	if p := vm.play; p != nil && vmProgress.file == p.file && vmProgress.rect.W > 0 && pointInRect(mx, my, vmProgress.rect) {
		p.seekFraction(float64(mx-vmProgress.rect.X) / float64(vmProgress.rect.W))
		return true
	}
	it, ok := vm.nav.hit(mx, my)
	if !ok {
		return false
	}
	vm.nav.focus = it.id
	if vm.rec != nil {
		switch it.id {
		case vmIDRecPause:
			vmRecTogglePause()
		case vmIDRecStop:
			vmRecStopAndName()
		}
		return true
	}
	vmActivate(config, it.id)
	return true
}

// voiceMemoWheel scrolls the delete question or the list.
func voiceMemoWheel(y int32) bool {
	if y == 0 {
		return false
	}
	if tbxSheetVisible(appConfig) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	if vm.menu.open || vm.mode == vmModeTest {
		return true
	}
	vm.nav.scrollBy(-y * 48)
	return true
}
