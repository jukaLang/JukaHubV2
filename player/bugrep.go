package main

// bugrep.go — the Report a bug screen.
//
// A full-screen form: what kind of problem (chips), a title and a description
// typed on the on-screen keyboard, two switches — the anonymous diagnostics
// report and the recent log, both redacted — a Preview that shows exactly what
// would be sent, and Submit. Where it goes and what happens when the handheld
// cannot reach it is bugrepsend.go's job; this file is the form and the sheets
// that report the outcome: "Sent · reference JH-…", "Queued", "Set the date
// and time first", or the phone path with a QR code when this build has no
// collector.

import (
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

// bugrepScreenState is the form and its focus.
type bugrepScreenState struct {
	visit    tbxVisit
	nav      tbxNav
	form     bugrepForm
	touched  bool
	sending  bool
	wrap     tbxWrapCache
	descWrap tbxWrapCache
	note     string
	noteAt   time.Time
}

var bugrepScreen = bugrepScreenState{form: bugrepForm{Category: -1, Diagnostics: true, Log: true}}

// bugrepOpenScreen opens Report a bug. Cross-area contract: the Tools hub, the
// Community card and Security & trust call it.
func bugrepOpenScreen(config *Config) {
	tbxOpenScene(config, bugrepSceneName)
}

type bugrepScene struct{ sceneBase }

func (bugrepScene) render(r *sdl.Renderer, c *Config) bool {
	bugrepRenderScreen(r, c)
	return true
}

func (bugrepScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), bugrepSceneAction)
}

func (bugrepScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), bugrepSceneAction)
}

func init() { registerScene(bugrepScene{sceneBase{name: bugrepSceneName}}) }

// ── Render ───────────────────────────────────────────────────────────────

func bugrepRenderScreen(renderer *sdl.Renderer, config *Config) {
	if bugrepScreen.visit.begin() {
		bugrepScreen.nav.scroll = 0
		bugrepScreen.nav.focus = ""
		if bugrepScreen.form.Category < 0 && !bugrepScreen.touched {
			bugrepScreen.nav.focus = "cat:0"
		}
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	endpoint := bugrepEndpointURL()
	status := "Sent through your phone"
	if endpoint != "" {
		status = "Anonymous"
	}
	if n := bugrepOutboxCount(); n > 0 {
		status += " · " + bugrepQueuedLabel(n)
	}
	header := tbxHeader("Report a bug", "Tell us what went wrong; you review everything before it is sent", "bug", status, ColorTextTertiary())
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &bugrepScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	scr := func(r sdl.Rect) sdl.Rect {
		r.Y += top
		return r
	}
	y := int32(0)
	row := 0
	form := &bugrepScreen.form

	// What kind of problem.
	renderText(renderer, config, fonts.small, "WHAT KIND OF PROBLEM?", ColorTextTertiary(), x+4, top+y)
	y += fonts.smallH + 10
	chipH := int32(44)
	cx := x
	for i, name := range bugrepCategories {
		id := bugrepChipIDs[i]
		cw := tbxButtonWidth(fonts.body, name, 96)
		rect := sdl.Rect{X: cx, Y: y, W: cw, H: chipH}
		bugrepDrawChip(renderer, config, fonts, scr(rect), name, form.Category == i, nav.focused(id))
		nav.add(id, rect, row, false)
		cx += cw + 10
	}
	row++
	y += chipH + 22

	// Title.
	titleRect := sdl.Rect{X: x, Y: y, W: w, H: fonts.smallH + fonts.titleH + 28}
	bugrepDrawField(renderer, config, fonts, scr(titleRect), "TITLE", form.Title, "A short summary, e.g. “Video freezes after resume”", 1, nav.focused("title"), nil)
	nav.add("title", titleRect, row, false)
	row++
	y += titleRect.H + 14

	// Description.
	descLines := int32(4)
	descRect := sdl.Rect{X: x, Y: y, W: w, H: fonts.smallH + descLines*(fonts.bodyH+3) + 30}
	bugrepDrawField(renderer, config, fonts, scr(descRect), "WHAT HAPPENED?", form.Description,
		"What did you do, what happened, and what did you expect?", int(descLines), nav.focused("desc"), &bugrepScreen.descWrap)
	nav.add("desc", descRect, row, false)
	row++
	y += descRect.H + 18

	// The two switches; Preview sits beside the first.
	toggleH := int32(64)
	pw := tbxButtonWidth(fonts.body, "Preview", 140)
	diag := sdl.Rect{X: x, Y: y, W: w - pw - 12, H: toggleH}
	bugrepDrawToggleRow(renderer, config, fonts, scr(diag), "Include anonymous diagnostics",
		"Build, device and health facts — no names, paths, addresses or keys", form.Diagnostics, nav.focused("diag"))
	nav.add("diag", diag, row, false)
	preview := sdl.Rect{X: x + w - pw, Y: y + (toggleH-48)/2, W: pw, H: 48}
	tbxDrawButton(renderer, config, fonts.body, scr(preview), "Preview", false, nav.focused("preview"), false)
	nav.add("preview", preview, row, false)
	row++
	y += toggleH + 10
	logRect := sdl.Rect{X: x, Y: y, W: w - pw - 12, H: toggleH}
	bugrepDrawToggleRow(renderer, config, fonts, scr(logRect), "Include recent log (redacted)",
		"The last few hundred lines, with credentials and personal details removed", form.Log, nav.focused("log"))
	nav.add("log", logRect, row, false)
	row++
	y += toggleH + 22

	// Submit, and where the report goes.
	label := "Submit"
	if bugrepScreen.sending {
		label = "Sending…"
	}
	sw := tbxButtonWidth(fonts.body, label, 180)
	submit := sdl.Rect{X: x + w - sw, Y: y, W: sw, H: 52}
	tbxDrawButton(renderer, config, fonts.body, scr(submit), label, true, nav.focused("submit"), bugrepScreen.sending)
	nav.add("submit", submit, row, bugrepScreen.sending)
	where := "Goes to the JukaHub report collector without your name or address."
	if endpoint == "" {
		where = "This build has no report collector: Submit shows a code your phone opens as a GitHub issue."
	}
	if bugrepScreen.note != "" && time.Since(bugrepScreen.noteAt) < 8*time.Second {
		where = bugrepScreen.note
	}
	lines := bugrepScreen.wrap.get(fonts.small, where, submit.X-x-24)
	ly := top + y + (52-int32(len(lines))*(fonts.smallH+2))/2
	for i, line := range lines {
		if i >= 2 {
			break
		}
		renderText(renderer, config, fonts.small, line, ColorTextMuted(), x+4, ly)
		ly += fonts.smallH + 2
	}
	nav.extend(y + 52 + 12)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// bugrepChipIDs are the chips' focus ids, built once.
var bugrepChipIDs = [...]string{"cat:0", "cat:1", "cat:2", "cat:3", "cat:4", "cat:5"}

func bugrepQueuedLabel(n int) string {
	if n == 1 {
		return "1 queued"
	}
	return sectrustItoa(int32(n)) + " queued"
}

// bugrepDrawChip draws one category chip: a glass pill, accent-filled when
// chosen.
func bugrepDrawChip(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, label string, chosen, focused bool) {
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	if chosen {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(glassAccent(), 70))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassAccent())
	} else {
		drawGlassSurface(renderer, r, r.H/2, glassState{NoLift: true})
	}
	if focused {
		drawGlassFocusRing(renderer, r, r.H/2)
	}
	tw, th := measureText(fonts.body, label)
	renderText(renderer, config, fonts.body, label, ColorTextPrimary(), r.X+(r.W-tw)/2, r.Y+(r.H-th)/2)
}

// bugrepDrawField draws a text field: its caption, then the value wrapped to
// lines (or the placeholder in the muted ink). cache keeps the wrapped value
// between frames; a one-line field needs none.
func bugrepDrawField(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, caption, value, placeholder string, lines int, focused bool, cache *tbxWrapCache) {
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassControlRadius, glassA(10))
	if focused {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassControlRadius, 2, glassAccent())
	} else {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassControlRadius, 1, glassA(26))
	}
	renderText(renderer, config, fonts.tiny, caption, ColorTextTertiary(), r.X+16, r.Y+10)
	y := r.Y + 10 + fonts.tinyH + 6
	if strings.TrimSpace(value) == "" {
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, placeholder, r.W-32), ColorTextTertiary(), r.X+16, y)
		return
	}
	if lines == 1 || cache == nil {
		renderText(renderer, config, fonts.title, ellipsize(fonts.title, value, r.W-32), ColorTextPrimary(), r.X+16, y)
		return
	}
	font := fonts.body
	wrapped := cache.get(font, value, r.W-32)
	for i, line := range wrapped {
		if i >= lines {
			break
		}
		if i == lines-1 && len(wrapped) > lines {
			line = ellipsize(font, line+" …", r.W-32)
		}
		renderText(renderer, config, font, line, ColorTextPrimary(), r.X+16, y)
		y += int32(font.Height()) + 3
	}
}

// bugrepDrawToggleRow draws a labelled switch row.
func bugrepDrawToggleRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, title, sub string, on, focused bool) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	toggle := tbxDrawToggle(renderer, r.X+r.W-18, r.Y+r.H/2, on)
	textW := toggle.X - r.X - 34
	block := fonts.titleH + 3 + fonts.smallH
	ty := r.Y + (r.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), r.X+18, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), ColorTextMuted(), r.X+18, ty+fonts.titleH+3)
}

// ── Input ────────────────────────────────────────────────────────────────

func bugrepSceneAction(config *Config, act tbxAction) bool {
	nav := &bugrepScreen.nav
	switch act {
	case tbxActConfirm:
		bugrepActivate(config, nav.focus)
		return true
	case tbxActX:
		if nav.focus == "title" || nav.focus == "desc" {
			bugrepClearField(nav.focus)
		}
		return true
	case tbxActY:
		bugrepActivate(config, "preview")
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func bugrepSceneClick(mx, my int32, config *Config) bool {
	it, ok := bugrepScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	bugrepScreen.nav.focus = it.id
	bugrepActivate(config, it.id)
	return true
}

func bugrepSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch id := bugrepScreen.nav.focus; {
	case strings.HasPrefix(id, "cat:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Choose"})
	case id == "title" || id == "desc":
		hints = append(hints, FooterHint{Button: "A", Label: "Type"}, FooterHint{Button: "X", Label: "Clear"})
	case id == "diag" || id == "log":
		hints = append(hints, FooterHint{Button: "A", Label: "Switch"})
	case id == "submit":
		hints = append(hints, FooterHint{Button: "A", Label: "Submit"})
	default:
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "Y", Label: "Preview"}, FooterHint{Button: "B", Label: "Back"})
}

func bugrepClearField(id string) {
	if id == "title" && bugrepScreen.form.Title != "" {
		bugrepScreen.form.Title = ""
		PlayBackSound()
	}
	if id == "desc" && bugrepScreen.form.Description != "" {
		bugrepScreen.form.Description = ""
		PlayBackSound()
	}
}

func bugrepActivate(config *Config, id string) {
	form := &bugrepScreen.form
	switch {
	case strings.HasPrefix(id, "cat:"):
		for i, cid := range bugrepChipIDs {
			if cid == id {
				form.Category = i
				bugrepScreen.touched = true
				PlayActivateSound()
				bugrepScreen.nav.setFocus("title")
			}
		}
	case id == "title":
		PlayActivateSound()
		tbxOpenPrompt("Title", "A short summary of the problem", form.Title, func(text string) {
			bugrepScreen.form.Title = text
			bugrepScreen.touched = true
			if text != "" {
				bugrepScreen.nav.setFocus("desc")
			}
		})
	case id == "desc":
		PlayActivateSound()
		tbxOpenPrompt("What happened?", "What you did, what happened, what you expected", form.Description, func(text string) {
			bugrepScreen.form.Description = text
			bugrepScreen.touched = true
			if text != "" {
				bugrepScreen.nav.setFocus("submit")
			}
		})
	case id == "diag":
		form.Diagnostics = !form.Diagnostics
		PlayNavSound()
	case id == "log":
		form.Log = !form.Log
		PlayNavSound()
	case id == "preview":
		PlayActivateSound()
		r := bugrepBuild(config, *form, time.Now())
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "What will be sent",
			subtitle: "Exactly this, nothing else",
			icon:     "info",
			body:     bugrepPreviewText(r),
			tall:     true,
			buttons:  []tbxSheetButton{{label: "Close", primary: true}},
		})
	case id == "submit":
		bugrepSubmit(config)
	}
}

// ── Submit ───────────────────────────────────────────────────────────────

func bugrepSubmit(config *Config) {
	if bugrepScreen.sending {
		return
	}
	form := bugrepScreen.form
	if strings.TrimSpace(form.Title) == "" && strings.TrimSpace(form.Description) == "" {
		ShowToast("Add a title or a description first", ToastKindWarning)
		bugrepScreen.nav.setFocus("title")
		return
	}
	if form.Category < 0 {
		form.Category = len(bugrepCategories) - 1
	}
	r := bugrepBuild(config, form, time.Now())
	endpoint := bugrepEndpointURL()
	if endpoint == "" {
		PlayActivateSound()
		bugrepPhoneSheet(config, r, "")
		return
	}
	if bugrepClockWrong(time.Now()) {
		bugrepHold(config, r, true)
		return
	}
	if !IsOnline() {
		bugrepHold(config, r, false)
		return
	}
	// The report goes straight to the collector and only lands in the outbox
	// when that fails: queued first, the outbox flush on the next frame would
	// send the same report a second time.
	bugrepScreen.sending = true
	PlayActivateSound()
	go func() {
		ref, kind, err := bugrepPostFunc(endpoint, r)
		tbxPost(func() {
			bugrepScreen.sending = false
			away := tbxCurrentScene(config) != bugrepSceneName
			switch {
			case err == nil:
				bugrepFormDone()
				if away {
					ShowToast("Bug report sent · reference "+ref, ToastKindSuccess)
					return
				}
				bugrepSentSheet(config, ref)
			case kind == bugrepErrClock:
				bugrepHold(config, r, true)
			case kind == bugrepErrTrust:
				if away {
					ShowToast("Bug report not sent — open Report a bug to send it from your phone", ToastKindWarning)
					return
				}
				bugrepPhoneSheet(config, r, "This firmware cannot check the report server's certificate, so the report goes through your phone instead.")
			case kind == bugrepErrRejected:
				ShowToast(firstLine(err.Error()), ToastKindError)
			default:
				bugrepHold(config, r, false)
			}
		})
	}()
}

// bugrepHold keeps a report that cannot go out now in the outbox, to be sent
// on its own later, and says so — as a sheet on this screen, or a toast when
// the user has moved on. When the outbox cannot be written either (no data
// folder, a read-only card), the form is kept and the phone path is offered,
// so a report is never announced as saved when it is not.
func bugrepHold(config *Config, r bugrepReport, clock bool) {
	away := tbxCurrentScene(config) != bugrepSceneName
	if err := bugrepQueue(r); err != nil {
		logPatch("[bugreport] cannot queue %s: %v", r.ID, err)
		if away {
			ShowToast("The bug report could not be saved — open Report a bug to send it from your phone", ToastKindError)
			return
		}
		bugrepPhoneSheet(config, r, "The report could not be saved on this device for later, so it goes through your phone instead.")
		return
	}
	bugrepFormDone()
	switch {
	case away && clock:
		ShowToast("Bug report saved — set the date and time so it can be sent", ToastKindWarning)
	case away:
		ShowToast("Bug report queued — it is sent when JukaHub is online", ToastKindInfo)
	case clock:
		bugrepClockSheet(config)
	default:
		bugrepQueuedSheet(config, r.ID)
	}
}

// bugrepFormDone clears the form after a report left (or was queued).
func bugrepFormDone() {
	bugrepScreen.form.Title = ""
	bugrepScreen.form.Description = ""
	bugrepScreen.form.Category = -1
	bugrepScreen.touched = false
	bugrepScreen.nav.focus = "cat:0"
}

func bugrepSentSheet(config *Config, ref string) {
	PlaySuccessSound()
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Thank you — report sent",
		subtitle: "Reference " + ref,
		icon:     "check",
		body: "## Your reference\n" + ref + "\n\n" +
			"Mention it if you talk to us about this problem — on GitHub or Discord — so the report can be found.",
		buttons: []tbxSheetButton{{label: "Done", primary: true}},
	})
}

func bugrepQueuedSheet(config *Config, ref string) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Queued",
		subtitle: "Reference " + ref,
		icon:     "upload",
		body: "There is no connection to the report service right now. The report is saved on this device and is sent " +
			"automatically the next time JukaHub is online — you do not need to do anything.",
		buttons: []tbxSheetButton{{label: "Done", primary: true}},
	})
}

func bugrepClockSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Set the date and time first",
		subtitle: "The device clock is wrong",
		icon:     "clock",
		body: "This device thinks it is " + time.Now().Format("2 January 2006") + ". Secure connections check dates, " +
			"so nothing can be sent until the clock is right.\n\nSet the date and time in the system settings (or connect to " +
			"Wi-Fi so it can set itself). Your report is saved and goes out on its own afterwards.",
		buttons: []tbxSheetButton{{label: "OK", primary: true}},
	})
}

// bugrepPhoneSheet is the no-collector path: a QR code that opens a prefilled
// GitHub issue on a phone, the full report saved on the device, and — on a
// desktop — the same issue in the browser.
func bugrepPhoneSheet(config *Config, r bugrepReport, why string) {
	link := bugrepIssueURL(r, 900)
	var modules [][]bool
	if code, err := qrcode.New(link, qrcode.Low); err == nil {
		modules = code.Bitmap()
	}
	saved := ""
	if path, err := bugrepSaveCopy(r); err == nil {
		saved = path
	}
	full := bugrepPreviewText(r)
	var body strings.Builder
	if why != "" {
		body.WriteString(why + "\n\n")
	}
	body.WriteString("Scan the code with your phone: it opens a GitHub issue with your title, description and the device facts filled in. " +
		"Sign in, check it and press Submit.\n\n")
	body.WriteString("## Reference\n" + r.ID + "\n")
	if saved != "" {
		body.WriteString("\n## Full report\nSaved on this device as " + bugrepBaseName(saved) +
			" (Patch reports folder), in case you want to attach the diagnostics.\n")
	}
	buttons := []tbxSheetButton{{label: "Copy report", keepOpen: true, run: func() {
		if err := copyTextToClipboard(full, "Report a bug"); err != nil {
			ShowToast("Could not copy the report", ToastKindError)
			return
		}
		ShowToast("Report copied", ToastKindSuccess)
	}}}
	if !P().IsHandheld() {
		desktopLink := bugrepIssueURL(r, 7000)
		buttons = append(buttons, tbxSheetButton{label: "Open in browser", run: func() {
			if err := openExternalURL(desktopLink); err != nil {
				ShowToast("Could not open a browser — scan the code instead", ToastKindWarning)
				return
			}
			bugrepFormDone()
		}})
	}
	buttons = append(buttons, tbxSheetButton{label: "Done", primary: true, run: bugrepFormDone})
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Send it from your phone",
		subtitle: "Reference " + r.ID,
		icon:     "phone",
		body:     body.String(),
		qr:       modules,
		qrLabel:  "Tap it or press Y to enlarge",
		buttons:  buttons,
		focus:    len(buttons) - 1,
	})
}

// bugrepBaseName is the last element of a path in either separator style.
func bugrepBaseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
