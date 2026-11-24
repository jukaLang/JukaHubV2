package main

// conncheck_ui.go — the Connection check screen, drawn like the other tool
// screens (cardcheck_ui.go): a verdict card that says in one sentence where
// the connection fails and what to do, a row of buttons (the fix, Run again,
// Copy report, Network tools), and one row per step with its outcome as a
// pill and its facts in a sheet. Opening the screen runs the check; the work
// itself is in conncheck.go.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// connCheckUI is the screen's focus list and the wrapped advice.
var connCheckUI struct {
	nav  tbxNav
	wrap tbxWrapCache
}

// connCheckOpen opens the screen, which runs a fresh check (cross-area
// contract: the Tools hub, Network tools and Home search call it).
func connCheckOpen(config *Config) {
	toolsPagesOpen(config, connCheckSceneName)
}

// connCheckEnter runs once per visit, on the visit's task scope: a fresh
// check, with the focus on the button row.
func connCheckEnter() {
	connCheckUI.nav.scroll = 0
	connCheckUI.nav.focus = ""
	connCheckStart()
}

type connCheckScene struct{ sceneBase }

func (connCheckScene) render(r *sdl.Renderer, c *Config) bool {
	connCheckRender(r, c)
	return true
}

func (connCheckScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), connCheckAction)
}

func (connCheckScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), connCheckAction)
}

func init() { registerScene(connCheckScene{sceneBase{name: connCheckSceneName}}) }

// connCheckHubTile is the Tools hub's tile (Connectivity). It needs no
// network: an offline device is exactly when it is wanted.
func connCheckHubTile() toolsHubTile {
	t := toolsHubTile{id: "conncheck", label: "Connection check", icon: "internet", value: "Wi-Fi, router, DNS and internet, in order"}
	t.value, t.tone = connCheckTileValue(t.value)
	return t
}

// connCheckTileValue is the tile's line after a check (the Tools hub's and
// the Network tools page's): the verdict and its tone, or idle.
func connCheckTileValue(idle string) (string, toolsHubTone) {
	run := connCheckSnapshot()
	switch {
	case run.running:
		return "Checking…", toolsHubToneBusy
	case run.verdict == "":
		return idle, toolsHubToneNeutral
	case run.tone == connOK:
		return "Last check: everything works", toolsHubToneGood
	}
	return "Last check: " + strings.ToLower(run.verdict[:1]) + run.verdict[1:], toolsHubToneWarn
}

// connStepPill is a step's pill and tone.
func connStepPill(s connStepState) (string, tbxTone) {
	switch s {
	case connRunning:
		return "Checking…", tbxToneAccent
	case connOK:
		return "OK", tbxToneGood
	case connWarn:
		return "Warning", tbxToneWarn
	case connFail:
		return "Problem", tbxToneBad
	case connSkipped:
		return "Not checked", tbxToneNeutral
	}
	return "Waiting", tbxToneNeutral
}

// connStepIcon is a step's glyph: its own, or the outcome once it has one.
func connStepIcon(st connStep) string {
	switch st.state {
	case connOK:
		return "check"
	case connWarn, connFail:
		return "warning"
	}
	return st.icon
}

// connStepLine is a step's row line.
func connStepLine(st connStep) string {
	if st.summary != "" {
		return st.summary
	}
	if st.state == connPending {
		return "Waiting for the steps before it"
	}
	return ""
}

// connCheckButtons are the button row, in order: the fix first when the
// connection fails on this device's side, then Run again, Copy report and
// Network tools.
func connCheckButtons(run connRun) []string {
	ids := make([]string, 0, 5)
	if run.wifiFix {
		ids = append(ids, "wifi")
	}
	if run.clockFix {
		ids = append(ids, "clock")
	}
	return append(ids, "run", "copy", "nettools")
}

// connCheckButtonLabel is a button's word.
func connCheckButtonLabel(id string, run connRun) string {
	switch id {
	case "wifi":
		return "Open Wi-Fi"
	case "clock":
		return "Date & time"
	case "run":
		if run.running {
			return "Start over"
		}
		return "Run again"
	case "copy":
		return "Copy report"
	case "nettools":
		return "Network tools"
	}
	return id
}

func connCheckRender(renderer *sdl.Renderer, config *Config) {
	run := connCheckSnapshot()
	status, statusColor := "", ColorTextMuted()
	switch {
	case run.running:
		status, statusColor = "Checking…", ColorTextAccent()
	case run.tone == connOK && run.verdict != "":
		status, statusColor = "All good", ToastSuccessInk()
	case run.tone == connWarn && run.verdict != "":
		status, statusColor = "Warning", ToastWarnInk()
	case run.tone == connFail:
		status, statusColor = "Problem found", ToastWarnInk()
	}
	header := tbxHeader("Connection check", "Wi-Fi, the router, name lookups and the internet, in order", "internet", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &connCheckUI.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)

	// The verdict.
	cardH := connCheckDrawVerdict(renderer, config, fonts, x, top+y, w, run)
	y += cardH + 14

	// The buttons, side by side in one focus row.
	bh := int32(46)
	bx := x
	for i, id := range connCheckButtons(run) {
		label := connCheckButtonLabel(id, run)
		bw := tbxButtonWidth(fonts.body, label, 132)
		if bx+bw > x+w {
			break
		}
		rect := sdl.Rect{X: bx, Y: y, W: bw, H: bh}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.body, screen, label, i == 0, nav.focused("btn:"+id), false)
		nav.add("btn:"+id, rect, 0, false)
		bx += bw + 12
	}
	y += bh + 18

	// The steps.
	tbxDrawSection(renderer, config, "Steps", "checklist", x, top+y, w)
	y += tbxSectionHeight
	rowH := tbxRowHeight(fonts)
	for i, st := range run.steps {
		pill, tone := connStepPill(st.state)
		id := "step:" + strconv.Itoa(int(st.id))
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, tbxRowSpec{
			icon: connStepIcon(st), title: st.title, subtitle: connStepLine(st), pill: pill, tone: tone,
			dim: st.state == connPending || st.state == connSkipped,
		}, nav.focused(id))
		nav.add(id, rect, 1+i, false)
		y += rowH + 8
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// connCheckDrawVerdict paints the verdict card at (x, y) and returns its
// height: the outcome's glyph in a badge, the headline, and the advice under
// it, wrapped to three lines.
func connCheckDrawVerdict(renderer *sdl.Renderer, config *Config, fonts tbxFonts, x, y, w int32, run connRun) int32 {
	icon, accent := "internet", glassAccent()
	title, advice := run.verdict, run.advice
	switch {
	case run.running:
		title = "Checking your connection…"
		advice = connRunningLine(run)
	case title == "":
		title = "Not run yet"
		advice = "Run the check to see where the connection fails."
	case run.tone == connOK:
		icon, accent = "check", ColorToastSuccess
	case run.tone == connWarn:
		icon, accent = "warning", ColorToastWarn
	case run.tone == connFail:
		icon, accent = "warning", ColorToastError
	}
	const pad = int32(18)
	badge := int32(52)
	textX := x + pad + badge + 18
	textW := x + w - pad - textX
	if textW < 80 {
		textW = 80
	}
	lines := connCheckUI.wrap.get(fonts.body, advice, textW)
	if len(lines) > 3 {
		lines = lines[:3]
	}
	lineH := fonts.bodyH + 3
	h := pad*2 + fonts.titleH + 6 + int32(len(lines))*lineH
	if h < badge+2*pad {
		h = badge + 2*pad
	}
	sysToolCard(renderer, sdl.Rect{X: x, Y: y, W: w, H: h}, run.running)
	fillRoundedRect(renderer, x+6, y+14, 4, h-28, 2, accent)
	box := drawGlassBadge(renderer, sdl.Rect{X: x + pad, Y: y + (h-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, icon, box, false)
	ty := y + pad
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), textX, ty)
	ty += fonts.titleH + 6
	for _, line := range lines {
		renderText(renderer, config, fonts.body, line, ColorTextSecondary(), textX, ty)
		ty += lineH
	}
	return h
}

// connRunningLine names the step under way: "Step 3 of 5: Name lookups".
func connRunningLine(run connRun) string {
	for i, st := range run.steps {
		if st.state == connRunning {
			return "Step " + strconv.Itoa(i+1) + " of " + strconv.Itoa(int(connStepCount)) + ": " + st.title
		}
	}
	return "Starting…"
}

// ── Input ────────────────────────────────────────────────────────────────

func connCheckAction(config *Config, act tbxAction) bool {
	nav := &connCheckUI.nav
	switch act {
	case tbxActConfirm:
		connCheckActivate(config, nav.focus)
		return true
	case tbxActX:
		// X runs the check again from anywhere on the screen.
		connCheckActivate(config, "btn:run")
		return true
	case tbxActY:
		connCheckActivate(config, "btn:copy")
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func connCheckClick(mx, my int32, config *Config) bool {
	it, ok := connCheckUI.nav.hit(mx, my)
	if !ok {
		return false
	}
	connCheckUI.nav.focus = it.id
	connCheckActivate(config, it.id)
	return true
}

func connCheckHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	focus := connCheckUI.nav.focus
	switch {
	case strings.HasPrefix(focus, "step:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Details"})
	case focus != "":
		hints = append(hints, FooterHint{Button: "A", Label: "Select"})
	}
	return append(hints,
		FooterHint{Button: "X", Label: "Run again"},
		FooterHint{Button: "Y", Label: "Copy report"},
		FooterHint{Button: "B", Label: "Back"})
}

func connCheckActivate(config *Config, id string) {
	run := connCheckSnapshot()
	switch {
	case id == "btn:run":
		PlayActivateSound()
		connCheckStart()
	case id == "btn:copy":
		if run.verdict == "" && !run.running {
			ShowToast("Run the check first", ToastKindInfo)
			return
		}
		PlayActivateSound()
		if err := copyTextToClipboard(connCheckReport(run), "Connection check"); err != nil {
			ShowToast("Could not copy: "+firstLine(err.Error()), ToastKindError)
			return
		}
		ShowToast("Report copied", ToastKindSuccess)
	case id == "btn:wifi":
		PlayActivateSound()
		wifiScrOpen(config)
	case id == "btn:clock":
		PlayActivateSound()
		dateTimeSettingsOpen(config)
	case id == "btn:nettools":
		PlayActivateSound()
		netToolsOpen(config)
	case strings.HasPrefix(id, "step:"):
		n, err := strconv.Atoi(strings.TrimPrefix(id, "step:"))
		if err != nil || n < 0 || n >= int(connStepCount) {
			return
		}
		connCheckOpenStep(config, run.steps[n])
	}
}

// connCheckOpenStep shows one step's facts and, where the connection fails,
// what to do.
func connCheckOpenStep(config *Config, st connStep) {
	PlayActivateSound()
	var b strings.Builder
	for _, f := range st.facts {
		b.WriteString("• " + f + "\n")
	}
	if st.fix != "" {
		b.WriteString("\n## What to do\n" + st.fix + "\n")
	}
	body := strings.TrimSpace(b.String())
	if body == "" {
		body = connStepLine(st)
	}
	if body == "" {
		body = "This step has not run yet."
	}
	pill, _ := connStepPill(st.state)
	sub := pill
	if st.summary != "" {
		sub += " · " + st.summary
	}
	tbxOpenSheet(config, tbxSheetSpec{title: st.title, subtitle: sub, icon: connStepIcon(st), body: body,
		buttons: []tbxSheetButton{{label: "OK", primary: true}}})
}
