package main

// batterycal_ui.go — the Battery calibration screen (Tools).
//
// One page: a card that says which step the cycle is on and what to do next,
// the buttons for that step, the last result with its "Correct the battery %"
// switch, and a live read-out of what the kernel reports about the battery.
// The cycle itself lives in batterycal.go; this file only reads a snapshot of
// it and calls its actions, which do their file work on goroutines.

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const batteryCalSceneName = "BatteryCalibration"

type batteryCalButton struct {
	id      string
	label   string
	primary bool
}

type batteryCalRowModel struct {
	id   string
	spec tbxRowSpec
}

type batteryCalModel struct {
	builtAt    time.Time
	view       batteryCalView
	ready      bool
	present    bool
	header     UIPageHeader
	headline   string
	sub        string
	good       bool
	actions    []batteryCalButton
	correctSub string
	result     []batteryCalRowModel
	live       []batteryCalRowModel
}

type batteryCalScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model batteryCalModel
	wrap  tbxWrapCache
}

var batteryCalScreen batteryCalScreenState

// batteryCalOpenScreen opens Battery calibration. Cross-area contract: the
// Tools hub calls it.
func batteryCalOpenScreen(config *Config) {
	batteryCalEnsureLoaded()
	tbxOpenScene(config, batteryCalSceneName)
}

type batteryCalScene struct{ sceneBase }

func (batteryCalScene) render(r *sdl.Renderer, c *Config) bool {
	if batteryCalScreen.visit.begin() {
		batteryCalScreen.nav.scroll = 0
		batteryCalScreen.model.builtAt = time.Time{}
	}
	batteryCalRenderScreen(r, c)
	return true
}

func (batteryCalScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), batteryCalSceneAction)
}

func (batteryCalScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), batteryCalSceneAction)
}

func init() { registerScene(batteryCalScene{sceneBase{name: batteryCalSceneName}}) }

// ── Model ────────────────────────────────────────────────────────────────

// batteryCalVolts writes microvolts as "3.92 V".
func batteryCalVolts(uv int) string {
	return fmt.Sprintf("%.2f V", float64(uv)/1e6)
}

func batteryCalBuildModel(now time.Time) batteryCalModel {
	v := batteryCalSnapshot()
	m := batteryCalModel{builtAt: now, view: v}
	m.ready = v.loaded && v.liveOK
	m.present = v.live.Present && v.live.Pct >= 0
	live := v.live
	status, statusColor := "Not calibrated", ColorTextMuted()
	switch {
	case v.st.Step == batteryCalDrain && v.st.AskBoot > 0:
		status, statusColor = "Step 3 of 3", ToastWarnInk()
		m.headline = "Did it switch itself off?"
		at := fmt.Sprintf("%d%%", v.lastBoot.Pct)
		if v.lastBoot.UV > 0 {
			at += " (" + batteryCalVolts(v.lastBoot.UV) + ")"
		}
		m.sub = "JukaHub started again during the drain; the last reading before that was " + at +
			". If the handheld shut down on its own, that was the end of the run and JukaHub can finish."
		m.actions = []batteryCalButton{
			{id: "died", label: "It switched off", primary: true},
			{id: "alive", label: "I turned it off"},
			{id: "cancel", label: "Cancel"},
		}
	case v.st.Step == batteryCalDrain:
		status, statusColor = "Step 2 of 3", glassAccent()
		m.headline = "Run it down"
		switch {
		case v.paused || live.Cable:
			m.sub = "The charger is in. Unplug it: the run needs the battery to go all the way down."
		case v.count == 0:
			m.sub = "Play as usual until the handheld switches itself off. The first reading is on its way."
		default:
			m.sub = fmt.Sprintf("Play as usual until the handheld switches itself off — games are fine. %s of use logged so far, now at %d%%.",
				batteryETADuration(int(v.used/60), false), v.last.Pct)
		}
		m.actions = []batteryCalButton{{id: "cancel", label: "Cancel"}}
	case v.st.Step == batteryCalCharge:
		status, statusColor = "Step 1 of 3", glassAccent()
		m.headline = "Charge to full"
		switch {
		case v.st.Full:
			m.sub = "Full. Unplug the cable and play as usual: step 2 starts when it comes out."
		case live.Cable && live.Pct >= 0:
			m.sub = fmt.Sprintf("Charging, now at %d%%. Leave it plugged in until it is full; that can take a while after it reads 100%%.", live.Pct)
		default:
			m.sub = "Plug in the charger and leave it until the battery is full."
		}
		m.actions = []batteryCalButton{{id: "cancel", label: "Cancel"}}
	case v.st.Result != nil:
		status, statusColor = "Calibrated", ToastSuccessInk()
		m.good = true
		m.headline = batteryCalFindings(v.st.Result)[0]
		if v.st.Correct {
			m.sub = "Correction is on: the % on screen follows what this battery really holds. Run it again whenever the % starts to drift."
		} else {
			m.sub = "Correction is off: the % on screen is the gauge's own. Run it again whenever the % starts to drift."
		}
		m.actions = []batteryCalButton{{id: "start", label: "Calibrate again"}}
	default:
		m.headline = "Teach the gauge this battery"
		m.sub = "If the % jumps around, or the handheld dies with charge still showing, one full charge and one full drain let the gauge relearn the battery. It takes a few hours and you can play meanwhile."
		m.actions = []batteryCalButton{{id: "start", label: "Start", primary: true}}
	}
	m.header = tbxHeader("Battery calibration", "Make the battery % tell the truth", "battery", status, statusColor)

	if r := v.st.Result; r != nil {
		switch {
		case r.MaxErr <= batteryCalTolerance && !v.st.Correct:
			m.correctSub = "Not needed: the gauge is accurate"
		case v.st.Correct && live.Pct >= 0:
			m.correctSub = fmt.Sprintf("On — the gauge's %d%% shows as %d%%", live.Pct, batteryCalApply(r.Table, live.Pct))
		case v.st.Correct:
			m.correctSub = "On — the % on screen is corrected"
		default:
			m.correctSub = "Off — the % on screen is the gauge's own"
		}
		endTone, endPill := tbxToneGood, fmt.Sprintf("read %d%%", r.EndPct)
		if r.EndPct > batteryCalEmptyPct {
			endTone = tbxToneWarn
		}
		accTone, accPill := tbxToneGood, fmt.Sprintf("within %d%%", max(r.MaxErr, 1))
		if r.MaxErr > batteryCalTolerance {
			accTone, accPill = tbxToneWarn, fmt.Sprintf("off by up to %d", r.MaxErr)
		}
		runSub := fmt.Sprintf("From %d%% down to %d%%, %d readings", r.FromPct, r.EndPct, r.Samples)
		if r.Rough {
			runSub += " · rough"
		}
		m.result = []batteryCalRowModel{
			{id: "res:end", spec: tbxRowSpec{icon: "battery", title: "When it shut down", subtitle: "What the gauge said as the battery gave out", pill: endPill, tone: endTone}},
			{id: "res:acc", spec: tbxRowSpec{icon: "chart", title: "Accuracy", subtitle: "Largest gap between the gauge and the measured charge", pill: accPill, tone: accTone}},
			{id: "res:run", spec: tbxRowSpec{icon: "clock", title: "Measured over", subtitle: runSub, pill: batteryETADuration(int(r.Awake/60), true), tone: tbxToneNeutral}},
		}
	}

	notReported := func(id, icon, title, sub string) batteryCalRowModel {
		return batteryCalRowModel{id: id, spec: tbxRowSpec{icon: icon, title: title, subtitle: sub, pill: "Not reported", tone: tbxToneNeutral, dim: true}}
	}
	row := func(id, icon, title, sub, pill string, tone tbxTone) batteryCalRowModel {
		return batteryCalRowModel{id: id, spec: tbxRowSpec{icon: icon, title: title, subtitle: sub, pill: pill, tone: tone}}
	}
	levelSub := "What the battery's gauge reports"
	if t := batteryCalTable.Load(); t != nil && live.Pct >= 0 {
		levelSub = fmt.Sprintf("Shown as %d%% with the correction", batteryCalApply(*t, live.Pct))
	}
	if live.Pct >= 0 {
		m.live = append(m.live, row("live:level", "battery", "Level", levelSub, fmt.Sprintf("%d%%", live.Pct), tbxToneAccent))
	} else {
		m.live = append(m.live, notReported("live:level", "battery", "Level", "What the battery's gauge reports"))
	}
	cable := "On battery"
	if live.Cable {
		cable = "Charger connected"
	}
	if live.Status != "" {
		m.live = append(m.live, row("live:status", "bolt", "Status", cable, batteryCalTitle(live.Status), tbxToneNeutral))
	} else {
		m.live = append(m.live, notReported("live:status", "bolt", "Status", cable))
	}
	if live.VoltageUV > 0 {
		m.live = append(m.live, row("live:volt", "power", "Voltage", "Near 4.2 V when full; handhelds switch off around 3.4 V", batteryCalVolts(live.VoltageUV), tbxToneNeutral))
	} else {
		m.live = append(m.live, notReported("live:volt", "power", "Voltage", "Near 4.2 V when full; handhelds switch off around 3.4 V"))
	}
	if live.HaveCurrent {
		m.live = append(m.live, row("live:current", "plug", "Current", "Flowing in or out of the battery right now", fmt.Sprintf("%d mA", live.CurrentMA), tbxToneNeutral))
	} else {
		m.live = append(m.live, notReported("live:current", "plug", "Current", "Flowing in or out of the battery right now"))
	}
	h := live.Health
	if h.HealthPct >= 0 {
		tone := tbxToneGood
		if h.HealthPct < batteryHealthThresholdPct {
			tone = tbxToneWarn
		}
		m.live = append(m.live, row("live:health", "chart", "Capacity", fmt.Sprintf("Holds %d of the %d mAh it was made for", h.FullCapacity, h.DesignCapacity), fmt.Sprintf("%d%%", h.HealthPct), tone))
	} else {
		m.live = append(m.live, notReported("live:health", "chart", "Capacity", "How much of its original charge the battery holds"))
	}
	if h.Cycles >= 0 {
		m.live = append(m.live, row("live:cycles", "refresh", "Charge cycles", "Batteries are usually rated for 300–500", fmt.Sprintf("%d", h.Cycles), tbxToneNeutral))
	} else {
		m.live = append(m.live, notReported("live:cycles", "refresh", "Charge cycles", "Batteries are usually rated for 300–500"))
	}
	if live.HaveTemp {
		m.live = append(m.live, row("live:temp", "thermometer", "Temperature", "Of the battery, where the kernel reports it", fmt.Sprintf("%.0f °C", float64(live.TempDeci)/10), tbxToneNeutral))
	} else {
		m.live = append(m.live, notReported("live:temp", "thermometer", "Temperature", "Of the battery, where the kernel reports it"))
	}
	return m
}

// batteryCalTitle capitalises a kernel status: "not charging" → "Not charging".
func batteryCalTitle(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ── Drawing ──────────────────────────────────────────────────────────────

func batteryCalRenderScreen(renderer *sdl.Renderer, config *Config) {
	batteryCalEnsureLoaded()
	batteryCalRefreshLive()
	now := time.Now()
	if now.Sub(batteryCalScreen.model.builtAt) > 500*time.Millisecond {
		batteryCalScreen.model = batteryCalBuildModel(now)
	}
	m := &batteryCalScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &batteryCalScreen.nav
	nav.begin(body.Y, body.H)
	if !m.ready || !m.present {
		nav.end()
		if !m.ready {
			tbxDrawEmpty(renderer, config, body, "battery", "Reading the battery…", "")
		} else {
			tbxDrawEmpty(renderer, config, body, "battery", "Not available on this device",
				"No battery is reported here, so there is nothing to calibrate.")
		}
		tbxEndFrame(renderer, config)
		return
	}
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0

	// The step card: where the cycle is and what to do next.
	badge := int32(56)
	textX := x + 22 + badge + 18
	textW := x + w - 22 - textX
	lines := batteryCalScreen.wrap.get(fonts.small, m.sub, textW)
	if len(lines) > 4 {
		lines = lines[:4]
	}
	block := fonts.titleH + 6 + int32(len(lines))*(fonts.smallH+2)
	heroH := max(int32(120), block+36)
	hero := sdl.Rect{X: x, Y: top + y, W: w, H: heroH}
	UICard(renderer, hero, DefaultUICardStyle())
	box := drawGlassBadge(renderer, sdl.Rect{X: hero.X + 22, Y: hero.Y + (heroH-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, "battery", box, false)
	ty := hero.Y + (heroH-block)/2
	ink := ColorTextPrimary()
	if m.good {
		ink = ToastSuccessInk()
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.headline, textW), ink, textX, ty)
	ty += fonts.titleH + 6
	for _, line := range lines {
		renderText(renderer, config, fonts.small, line, ColorTextMuted(), textX, ty)
		ty += fonts.smallH + 2
	}
	y += heroH + 16

	// The step's buttons.
	bx := x
	for _, a := range m.actions {
		bw := tbxButtonWidth(fonts.body, a.label, 160)
		rect := sdl.Rect{X: bx, Y: y, W: bw, H: 48}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.body, screen, a.label, a.primary, nav.focused(a.id), false)
		nav.add(a.id, rect, row, false)
		bx += bw + 12
	}
	row++
	y += 48 + 22

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

	if m.view.st.Result != nil {
		tbxDrawSection(renderer, config, "Last calibration", "chart", x, top+y, w)
		y += tbxSectionHeight
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		bugrepDrawToggleRow(renderer, config, fonts, screen, "Correct the battery %", m.correctSub, m.view.st.Correct, nav.focused("correct"))
		nav.add("correct", rect, row, false)
		row++
		y += rowH + 8
		for _, r := range m.result {
			drawRow(r.id, r.spec)
		}
		drawRow("reset", tbxRowSpec{icon: "trash", title: "Reset calibration", subtitle: "Forget this result and show the gauge's own % again"})
		y += 14
	}

	tbxDrawSection(renderer, config, "Right now", "battery", x, top+y, w)
	y += tbxSectionHeight
	for _, r := range m.live {
		drawRow(r.id, r.spec)
	}
	y += 14

	tbxDrawSection(renderer, config, "Help", "info", x, top+y, w)
	y += tbxSectionHeight
	drawRow("about", tbxRowSpec{icon: "info", title: "What calibration does", subtitle: "Why it helps, the three steps, and what JukaHub corrects"})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func batteryCalSceneAction(config *Config, act tbxAction) bool {
	nav := &batteryCalScreen.nav
	switch act {
	case tbxActConfirm:
		batteryCalActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func batteryCalSceneClick(mx, my int32, config *Config) bool {
	it, ok := batteryCalScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	batteryCalScreen.nav.focus = it.id
	batteryCalActivate(config, it.id)
	return true
}

func batteryCalSceneHints() []FooterHint {
	label := "Details"
	switch f := batteryCalScreen.nav.focus; {
	case f == "correct":
		label = "Toggle"
	case f == "about":
		label = "Read"
	case f == "start", f == "cancel", f == "reset", f == "died", f == "alive":
		label = "Select"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}, {Button: "B", Label: "Back"}}
}

func batteryCalActivate(config *Config, id string) {
	PlayActivateSound()
	m := &batteryCalScreen.model
	switch {
	case id == "start":
		if !m.present {
			ShowToast("No battery is reported on this device", ToastKindWarning)
			return
		}
		batteryCalBegin()
		ShowToast("Calibration started — charge the battery to full", ToastKindInfo)
	case id == "cancel":
		tbxOpenSheet(config, tbxSheetSpec{
			title: "Cancel calibration?",
			icon:  "battery",
			body:  "The readings so far are thrown away. An earlier result, and its correction, stay as they are.",
			buttons: []tbxSheetButton{
				{label: "Keep going"},
				{label: "Cancel it", primary: true, run: func() {
					batteryCalCancel()
					batteryCalScreen.model.builtAt = time.Time{}
					ShowToast("Calibration cancelled", ToastKindInfo)
				}},
			},
		})
	case id == "died":
		batteryCalAnswer(true)
	case id == "alive":
		batteryCalAnswer(false)
	case id == "correct":
		if m.view.st.Result != nil {
			on := !m.view.st.Correct
			batteryCalSetCorrect(on)
			if on {
				ShowToast("The battery % is corrected from now on", ToastKindSuccess)
			} else {
				ShowToast("The battery % is the gauge's own again", ToastKindInfo)
			}
		}
	case id == "reset":
		tbxOpenSheet(config, tbxSheetSpec{
			title: "Reset calibration?",
			icon:  "trash",
			body:  "JukaHub forgets the result and shows the gauge's own % again. Nothing on the battery itself changes.",
			buttons: []tbxSheetButton{
				{label: "Keep it"},
				{label: "Reset", primary: true, run: func() {
					batteryCalReset()
					batteryCalScreen.model.builtAt = time.Time{}
					ShowToast("Calibration reset", ToastKindInfo)
				}},
			},
		})
	case id == "about":
		batteryCalOpenAbout(config)
	case strings.HasPrefix(id, "res:"):
		batteryCalOpenResult(config, m.view.st.Result)
	case strings.HasPrefix(id, "live:"):
		batteryCalOpenReadings(config)
	}
	batteryCalScreen.model.builtAt = time.Time{}
}

// batteryCalOpenAbout is the "What calibration does" sheet.
func batteryCalOpenAbout(config *Config) {
	body := "## Why it helps\n" +
		"The battery's gauge estimates the charge from a model of the cell. As the cell ages, or after a swap, that model drifts: the % jumps, or the handheld dies with charge still showing.\n\n" +
		"Calibration lets the gauge see one full charge and one full drain, which is how it learns the cell's real capacity again. JukaHub also logs the drain once a minute and works out how far off the % was, so it can show a corrected one.\n\n" +
		"## The three steps\n" +
		"• Charge to full, and leave it plugged in until it says full.\n" +
		"• Unplug and play as usual until the handheld switches itself off. Games are fine: JukaHub keeps logging behind them.\n" +
		"• Switch it on again. JukaHub finishes and tells you what it found.\n\n" +
		"## Good to know\n" +
		"The measurement assumes a fairly steady load. A run that mixes menus and heavy games still helps, just less exactly. " +
		"Nothing is written to the battery chip; Reset calibration undoes everything JukaHub changed."
	tbxOpenSheet(config, tbxSheetSpec{title: "What calibration does", icon: "info", body: body, buttons: []tbxSheetButton{{label: "Close", primary: true}}})
}

// batteryCalOpenResult shows the last result in full.
func batteryCalOpenResult(config *Config, r *batteryCalResult) {
	if r == nil {
		return
	}
	var b strings.Builder
	b.WriteString("## What it found\n")
	for _, f := range batteryCalFindings(r) {
		b.WriteString("• " + f + "\n")
	}
	b.WriteString("\n## How it was measured\n")
	fmt.Fprintf(&b, "The true charge at each of the %d readings is the share of the run's use still ahead of it. ", r.Samples)
	b.WriteString("That assumes a fairly steady load, so treat a few points either way as noise.")
	tbxOpenSheet(config, tbxSheetSpec{title: "Last calibration", icon: "chart", body: b.String(), buttons: []tbxSheetButton{{label: "Close", primary: true}}})
}

// batteryCalOpenReadings explains the live rows.
func batteryCalOpenReadings(config *Config) {
	body := "These come straight from the battery driver and update every two seconds.\n\n" +
		"• Level is the gauge's own %, before any correction.\n" +
		"• Voltage falls as the battery empties; under load it dips, so it is a rough guide, not a gauge.\n" +
		"• Capacity compares what the battery holds now with what it held new.\n" +
		"• Not reported means this device's driver does not expose that number."
	tbxOpenSheet(config, tbxSheetSpec{title: "Reading the battery", icon: "battery", body: body, buttons: []tbxSheetButton{{label: "Close", primary: true}}})
}
