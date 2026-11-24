package main

// powercheck_ui.go — the battery life check's screen (powercheck.go runs the
// check): the last boot's time, the newest results, a button that starts a
// run and the still-frames switch, drawn like the other tool screens.

import (
	"fmt"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

type powerCheckScreenState struct {
	visit tbxVisit
	nav   tbxNav
	// history is the stored results as last read; gen moves when a run
	// stored a new one, read is the gen history was read at.
	history []powerCheckResult
	gen     int
	read    int
	loading bool
}

var powerCheckScreen powerCheckScreenState

type powerCheckScene struct{ sceneBase }

func (powerCheckScene) render(r *sdl.Renderer, c *Config) bool {
	s := &powerCheckScreen
	if s.visit.begin() {
		s.nav.scroll = 0
		s.read = -1
	}
	if s.read != s.gen && !s.loading {
		s.loading = true
		gen := s.gen
		go func() {
			list := powerCheckLoad()
			tbxPost(func() {
				powerCheckScreen.history, powerCheckScreen.read, powerCheckScreen.loading = list, gen, false
			})
		}()
	}
	powerCheckRender(r, c)
	return true
}

func (powerCheckScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), powerCheckSceneAction)
}

func (powerCheckScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), powerCheckSceneAction)
}

func init() { registerScene(powerCheckScene{sceneBase{name: powerCheckSceneName}}) }

// powerCheckSettingsRow is Settings → Power & battery's door to the screen.
func powerCheckSettingsRow() setRow {
	return setRow{
		ID: "powerCheck", Label: "Battery life check", Kind: setKindAction,
		Detail: setStatic("How long the last boot took, and what an idle Home draws with still frames on and off"),
		Open:   func(c *Config) { tbxOpenScene(c, powerCheckSceneName) },
	}
}

func powerCheckRender(renderer *sdl.Renderer, config *Config) {
	s := &powerCheckScreen
	header := tbxHeader("Battery life check", "What the last boot and an idle Home cost on this handheld", "battery", "", ColorTextMuted())
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

	tbxDrawSection(renderer, config, "Last boot", "startup", x, top+y, w)
	y += tbxSectionHeight
	if boot := startupBootSummary(); boot != "" {
		drawRow("", tbxRowSpec{icon: "startup", title: "Boot time", subtitle: boot, dim: true})
	} else {
		drawRow("", tbxRowSpec{icon: "startup", title: "Not measured", subtitle: "JukaHub was not started by this boot, or this system has no boot clock", dim: true})
	}
	y += 6
	tbxDrawSection(renderer, config, "Idle Home", "battery", x, top+y, w)
	y += tbxSectionHeight
	drawRow("run", tbxRowSpec{icon: "battery", title: "Measure now",
		subtitle: "About two minutes on Home left alone: still frames on, then off; any button stops it", pill: "Start", tone: tbxToneAccent})
	for i, r := range s.history {
		if i == 5 {
			break
		}
		title := "Check " + r.ID
		if r.Date != "" {
			title = "Check of " + r.Date
		}
		drawRow("r:"+strconv.Itoa(i), tbxRowSpec{icon: "chart", title: title, subtitle: powerCheckSummary(r), pill: "Report", tone: tbxToneNeutral})
	}
	y += 6
	tbxDrawSection(renderer, config, "Still frames", "speed", x, top+y, w)
	y += tbxSectionHeight
	on := stillFramesSetting(config)
	toggle := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	toggleScreen := toggle
	toggleScreen.Y += top
	davShareDrawToggleRow(renderer, config, fonts, toggleScreen, "Still frames", stillFramesSettingsRow().Detail(config), on, nav.focused("still"))
	nav.add("still", toggle, row, false)
	y += rowH + 8
	drawRow("", tbxRowSpec{icon: "info", title: "For docs/PERFORMANCE.md",
		subtitle: "Each check writes a report into \"" + devCheckReportDir + "\" with rows for the table", dim: true})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func powerCheckSceneAction(config *Config, act tbxAction) bool {
	nav := &powerCheckScreen.nav
	switch act {
	case tbxActConfirm:
		powerCheckActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func powerCheckSceneClick(mx, my int32, config *Config) bool {
	it, ok := powerCheckScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	powerCheckScreen.nav.focus = it.id
	powerCheckActivate(config, it.id)
	return true
}

func powerCheckSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch f := powerCheckScreen.nav.focus; {
	case f == "run":
		hints = append(hints, FooterHint{Button: "A", Label: "Start"})
	case f == "still":
		hints = append(hints, FooterHint{Button: "A", Label: "Switch"})
	case len(f) > 2 && f[:2] == "r:":
		hints = append(hints, FooterHint{Button: "A", Label: "Report"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

func powerCheckActivate(config *Config, id string) {
	s := &powerCheckScreen
	switch {
	case id == "run":
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{
			title: "Measure an idle Home", icon: "battery",
			body: "Home comes up and is left alone for about two minutes: a minute with still frames on, then a minute with them off. " +
				"Put the handheld down and do not press anything; any button stops the check.\n\n" +
				"Unplug the charger first: on it the battery's draw cannot be read.",
			buttons: []tbxSheetButton{{label: "Start", primary: true, run: func() { powerCheckStart(config) }}, {label: "Cancel"}},
		})
	case id == "still":
		on := !stillFramesSetting(config)
		stillFramesSettingsRow().Set(config, on)
	case len(id) > 2 && id[:2] == "r:":
		i, err := strconv.Atoi(id[2:])
		if err != nil || i < 0 || i >= len(s.history) {
			return
		}
		r := s.history[i]
		PlayActivateSound()
		sub := r.Device
		if r.Report != "" {
			sub = fmt.Sprintf("%s · %s", r.Device, r.Report)
		}
		tbxOpenSheet(config, tbxSheetSpec{title: "Battery life check", subtitle: sub, icon: "chart",
			body: powerCheckSheetBody(r), tall: true, buttons: []tbxSheetButton{{label: "OK", primary: true}}})
	}
}

// powerCheckSheetBody is one result as the sheet shows it.
func powerCheckSheetBody(r powerCheckResult) string {
	body := "JukaHub " + r.Version
	if r.Boot != "" {
		body += "\nLast boot: " + r.Boot
	}
	for _, p := range r.Phases {
		mode := "## Still frames off"
		if p.Still {
			mode = "## Still frames on"
		}
		body += "\n\n" + mode + "\n• " + powerCheckPhaseWords(p)
		if p.CPUKnown {
			body += fmt.Sprintf("\n• The whole CPU: %.1f%%", p.AllCPU)
		}
	}
	body += "\n\n" + powerCheckSummary(r) + "."
	if r.Note != "" {
		body += "\n\n" + r.Note + "."
	}
	if r.Report != "" {
		body += "\n\nReport: " + r.Report
	}
	return body
}
