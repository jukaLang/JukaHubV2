package main

// bugrep.go — the Report a bug screen.
//
// A full-screen form: what kind of problem (chips), a title and a description
// typed on the on-screen keyboard, two switches — the anonymous diagnostics
// report and the recent log, both redacted — a Preview that shows exactly what
// would be sent, and Submit. Where it goes and what happens when the handheld
// cannot reach it is bugrepsend.go's job; this file is the form and the sheets
// that report the outcome: "Sent · issue #… · reference JH-…", "Queued", "Set
// the date and time first", or — only when the device cannot send it itself —
// the phone path with a QR code.
//
// Submit used to end on that QR code every time: no build carried a collector,
// so a tester had to finish every report on a phone signed in to GitHub. Every
// build now sends to the JukaHub report service (bugrepDefaultEndpoint), which
// files the report as a GitHub issue; the phone is the fallback.

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// draftRef is the reference of the report being written, made once per
	// draft (bugrepDraftRef) so the preview names the reference it is sent
	// with; a report that left (or was queued) starts a new one. draftAt is
	// the draft's "Created" time, pinned the same way: the preview shows the
	// time the report is sent with, and a second Submit of the same draft
	// (after an answer that never came) is the same report to the collector,
	// which recognises it as a duplicate instead of filing it twice.
	draftRef string
	draftAt  time.Time
}

var bugrepScreen = bugrepScreenState{form: bugrepForm{Category: -1, Diagnostics: true, Log: true}}

// bugrepEntryName is Report a bug's row in Start → Device.
const bugrepEntryName = "Report a bug"

// bugrepQuickTitle is the title a report gets when nothing was typed: a report
// is one press from the form (Submit, or Y then Send), and what the device
// knows — its diagnostics and recent log — is the report. The collector wants
// a title or a description, and "Other problem" would say less than this.
func bugrepQuickTitle(form bugrepForm) string {
	cat := "Quick report"
	if form.Category >= 0 && form.Category < len(bugrepCategories) && bugrepCategories[form.Category] != "Other" {
		cat = "Quick " + strings.ToLower(bugrepCategories[form.Category]) + " report"
	}
	if name := strings.TrimSpace(deviceDisplayName(DetectDevice())); name != "" {
		return cat + " from " + name
	}
	return cat
}

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
		status = "Sent from this device"
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
	where := bugrepWhereLine(endpoint, bugrepOutboxCount() > 0 && bugrepOutboxStopped())
	if bugrepScreen.note != "" && time.Since(bugrepScreen.noteAt) < 8*time.Second {
		where = bugrepScreen.note
	}
	whereW := submit.X - x - 24
	lines := bugrepScreen.wrap.get(fonts.small, where, whereW)
	shown := len(lines)
	if shown > 2 {
		shown = 2
	}
	ly := top + y + (52-int32(shown)*(fonts.smallH+2))/2
	for i := 0; i < shown; i++ {
		line := lines[i]
		if i == shown-1 && len(lines) > shown {
			// At a large text size the line runs longer than two rows: its
			// end is marked as cut instead of silently dropped.
			line = ellipsize(fonts.small, line+" …", whereW)
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

// bugrepWhereLine is the line beside Submit: where the report goes, for the
// collector at endpoint ("" when the build has none). waiting is true while
// queued reports wait for a collector that is not answering. Only the JukaHub
// report service is promised to file a public GitHub issue; a collector the
// environment, the config, the build or the telemetry setting chose is named
// by its host, because what it does with a report is not this app's to say.
func bugrepWhereLine(endpoint string, waiting bool) string {
	switch {
	case endpoint == "":
		return "This build has no report service: Submit shows a code your phone opens as a GitHub issue."
	case waiting:
		return bugrepCapital(bugrepServiceName(endpoint)) + " is not answering right now; queued reports go out on " +
			"the next start. Submit offers your phone or a later try."
	case bugrepIsDefaultEndpoint(endpoint):
		return "Sent from this device to the JukaHub report service, which files it as a public GitHub issue — " +
			"not under your name, and without your address."
	}
	return "Sent from this device to " + bugrepServiceName(endpoint) + ", which this build or its settings name."
}

// bugrepWhereText is the preview's opening section: where what follows goes.
func bugrepWhereText(endpoint string) string {
	switch {
	case endpoint == "":
		return "## Where it goes\nThis build has no report service: Submit shows a code your phone opens as a " +
			"prefilled GitHub issue, and you send it from there.\n\n"
	case bugrepIsDefaultEndpoint(endpoint):
		return "## Where it goes\nStraight from this device to the JukaHub report service, which files it as a " +
			"public issue on JukaHub's GitHub for anyone to read. It is not filed under your name, and your " +
			"address is not part of it. No phone or account is needed.\n\n"
	}
	return "## Where it goes\nStraight from this device to " + bugrepServiceName(endpoint) + ", which this build or " +
		"its settings name instead of the JukaHub report service. What happens to the report there is up to " +
		"whoever runs it.\n\n"
}

// bugrepCapital starts s with a capital letter, for a phrase that opens a
// sentence ("the JukaHub report service is …").
func bugrepCapital(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// bugrepSentence turns an error into a sentence for a sheet: its first line,
// capitalised, with a full stop.
func bugrepSentence(s string) string {
	s = strings.TrimSpace(firstLine(s))
	if s == "" {
		return ""
	}
	s = bugrepCapital(s)
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// bugrepDraftRef is the reference of the report being written: made once per
// draft, so the preview shows the reference the report is then sent with.
// bugrepFormDone starts the next draft with a new one.
func bugrepDraftRef() string {
	if !bugrepRefValid(bugrepScreen.draftRef) {
		bugrepScreen.draftRef = bugrepNewRef()
	}
	return bugrepScreen.draftRef
}

// bugrepDraftTime is the draft's "Created" time, made with its first preview
// or Submit and kept until bugrepFormDone starts the next draft.
func bugrepDraftTime() time.Time {
	if bugrepScreen.draftAt.IsZero() {
		bugrepScreen.draftAt = time.Now()
	}
	return bugrepScreen.draftAt
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
		preview := *form
		if strings.TrimSpace(preview.Title) == "" && strings.TrimSpace(preview.Description) == "" &&
			(preview.Diagnostics || preview.Log) {
			// The title Submit would give it, so the preview is still exactly
			// what is sent.
			preview.Title = bugrepQuickTitle(preview)
		}
		r := bugrepBuild(config, preview, bugrepDraftTime())
		r.ID = bugrepDraftRef()
		// Send is the sheet's first button and starts focused: Y then A is a
		// report from anywhere on the form, after seeing what it holds.
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "What will be sent",
			subtitle: "Exactly this, nothing else",
			icon:     "info",
			body:     bugrepWhereText(bugrepEndpointURL()) + bugrepPreviewText(r),
			tall:     true,
			buttons: []tbxSheetButton{
				{label: "Send", primary: true, run: func() { bugrepSend(config, false) }},
				{label: "Close"},
			},
		})
	case id == "submit":
		bugrepSubmit(config)
	}
}

// ── Submit ───────────────────────────────────────────────────────────────

func bugrepSubmit(config *Config) {
	bugrepSend(config, true)
}

// bugrepSend is Submit. sound is false when the press already played the
// activation sound: the preview's Send button (tbxSheetActivate plays it).
func bugrepSend(config *Config, sound bool) {
	if bugrepScreen.sending {
		return
	}
	form := bugrepScreen.form
	if strings.TrimSpace(form.Title) == "" && strings.TrimSpace(form.Description) == "" {
		// Nothing typed: the device's own diagnostics and log are the report,
		// under a title that says so — typing on a handheld is what kept a
		// report from being one press. With both switched off there is
		// nothing to send at all.
		if !form.Diagnostics && !form.Log {
			ShowToast("Add a title or a description, or include the diagnostics", ToastKindWarning)
			bugrepScreen.nav.setFocus("title")
			return
		}
		form.Title = bugrepQuickTitle(form)
	}
	if form.Category < 0 {
		form.Category = len(bugrepCategories) - 1
	}
	r := bugrepBuild(config, form, bugrepDraftTime())
	r.ID = bugrepDraftRef()
	endpoint := bugrepEndpointURL()
	if endpoint == "" {
		if sound {
			PlayActivateSound()
		}
		bugrepPhoneSheet(config, r, "", false)
		return
	}
	if bugrepClockWrong(time.Now()) {
		bugrepHold(config, r, bugrepHoldClock)
		return
	}
	if !IsOnline() {
		bugrepHold(config, r, bugrepHoldOffline)
		return
	}
	// The report goes straight to the collector and only lands in the outbox
	// when that fails: queued first, the outbox flush on the next frame would
	// send the same report a second time.
	bugrepScreen.sending = true
	if sound {
		PlayActivateSound()
	}
	go func() {
		sent, kind, err := bugrepPostFunc(endpoint, r)
		tbxPost(func() {
			bugrepScreen.sending = false
			away := tbxCurrentScene(config) != bugrepSceneName
			switch {
			case err == nil:
				bugrepFormDone()
				trophyEvent(tkBugReport)
				// The collector works, so anything that waited for it goes now.
				bugrepCollectorAnswered(endpoint)
				if away {
					ShowToast(bugrepSentToast(sent), ToastKindSuccess)
					return
				}
				bugrepSentSheet(config, sent)
			case kind == bugrepErrClock:
				if !bugrepClockWrong(time.Now()) {
					// A certificate that only looks expired cost a request.
					bugrepNoteSendFailed(time.Now())
				}
				bugrepHold(config, r, bugrepHoldClock)
			case kind == bugrepErrUnavailable:
				bugrepCollectorUnavailable()
				if away {
					// Nothing on screen can show the code, so the report is
					// kept and tried again on the next start.
					bugrepHold(config, r, bugrepHoldService)
					return
				}
				bugrepPhoneSheet(config, r, bugrepCapital(bugrepServiceName(endpoint))+" is not answering right now. "+
					"Send the report from your phone with the code, or choose Send later to keep it on this device and "+
					"send it on the next start.", true)
			case kind == bugrepErrTrust:
				// The outbox would meet the same certificate, so it waits for
				// the next start too.
				bugrepCollectorUnavailable()
				if away {
					bugrepHold(config, r, bugrepHoldTrust)
					return
				}
				bugrepPhoneSheet(config, r, "This firmware cannot check the report server's certificate, so the report goes through your phone instead.", false)
			case kind == bugrepErrRejected:
				// A refusal retires nothing here: the words are kept as a copy
				// on the device, and the phone can still send them.
				if away {
					// Said as saved only when the copy really was written.
					msg := "Bug report not sent: " + firstLine(err.Error())
					if _, cerr := bugrepSaveCopy(r); cerr == nil {
						msg += " — a copy is saved on this device"
					} else {
						msg += " — open Report a bug to send it from your phone"
					}
					ShowToast(msg, ToastKindError)
					return
				}
				bugrepPhoneSheet(config, r, bugrepSentence(err.Error())+" You can still send it from your phone with the code.", false)
			case kind == bugrepErrServer:
				bugrepNoteSendFailed(time.Now())
				bugrepHold(config, r, bugrepHoldBusy)
			default:
				bugrepNoteSendFailed(time.Now())
				bugrepHold(config, r, bugrepHoldOffline)
			}
		})
	}()
}

// bugrepSentToast is the confirmation when the user left the screen before the
// answer came.
func bugrepSentToast(sent bugrepSent) string {
	if sent.Issue > 0 {
		return "Bug report sent · issue #" + strconv.Itoa(sent.Issue)
	}
	return "Bug report sent · reference " + sent.Ref
}

// bugrepHoldWhy is why a report waits in the outbox, which decides what the
// user is told about when it goes out.
type bugrepHoldWhy int

const (
	// bugrepHoldOffline: no network; it goes when JukaHub is online again.
	bugrepHoldOffline bugrepHoldWhy = iota
	// bugrepHoldClock: the clock is wrong; it goes once the date is set.
	bugrepHoldClock
	// bugrepHoldService: the report service is not answering; it is tried
	// again on the next start.
	bugrepHoldService
	// bugrepHoldBusy: the report service answered but is busy or failing
	// (429, 5xx); the outbox retries it with its backoff.
	bugrepHoldBusy
	// bugrepHoldTrust: this firmware cannot check the service's certificate;
	// it waits for the next start, and a readable copy is saved as well.
	bugrepHoldTrust
)

// bugrepHold keeps a report that cannot go out now in the outbox, to be sent
// on its own later, and says so — as a sheet on this screen, or a toast when
// the user has moved on. When the outbox cannot be written either (no data
// folder, a read-only card), the form is kept and the phone path is offered,
// so a report is never announced as saved when it is not.
func bugrepHold(config *Config, r bugrepReport, why bugrepHoldWhy) {
	away := tbxCurrentScene(config) != bugrepSceneName
	if err := bugrepQueue(r); err != nil {
		logPatch("[bugreport] cannot queue %s: %v", r.ID, err)
		if away {
			ShowToast("The bug report could not be saved — open Report a bug to send it from your phone", ToastKindError)
			return
		}
		bugrepPhoneSheet(config, r, "The report could not be saved on this device for later, so it goes through your phone instead.", false)
		return
	}
	copied := false
	if why == bugrepHoldTrust {
		// The queued copy may wait a long time on this firmware: a readable
		// one goes next to the other support reports as well — and is only
		// mentioned when it was written.
		_, cerr := bugrepSaveCopy(r)
		copied = cerr == nil
	}
	bugrepFormDone()
	trophyEvent(tkBugReport)
	switch {
	case away && why == bugrepHoldClock:
		ShowToast("Bug report saved — set the date and time so it can be sent", ToastKindWarning)
	case away && why == bugrepHoldService:
		ShowToast("Bug report saved — the report service is not answering, so it is sent on the next start", ToastKindInfo)
	case away && why == bugrepHoldBusy:
		ShowToast("Bug report queued — the report service is busy, so it is sent in a little while", ToastKindInfo)
	case away && why == bugrepHoldTrust && copied:
		ShowToast("Bug report saved, not sent — this firmware cannot check the report server; a copy is in the Patch reports folder", ToastKindWarning)
	case away && why == bugrepHoldTrust:
		ShowToast("Bug report saved, not sent — this firmware cannot check the report server", ToastKindWarning)
	case away:
		ShowToast("Bug report queued — it is sent when JukaHub is online", ToastKindInfo)
	case why == bugrepHoldClock:
		bugrepClockSheet(config)
	default:
		bugrepQueuedSheet(config, r.ID, why, copied)
	}
}

// bugrepFormDone clears the form after a report left (or was queued), and
// starts the next draft with a new reference.
func bugrepFormDone() {
	bugrepScreen.form.Title = ""
	bugrepScreen.form.Description = ""
	bugrepScreen.form.Category = -1
	bugrepScreen.touched = false
	bugrepScreen.nav.focus = "cat:0"
	bugrepScreen.draftRef = ""
	bugrepScreen.draftAt = time.Time{}
}

// bugrepSentSheet thanks the user and names what the report became: the issue
// number when the report service filed one, and the reference either way. The
// issue's code is there for anyone who wants to follow it on a phone; nothing
// else needs doing.
func bugrepSentSheet(config *Config, sent bugrepSent) {
	PlaySuccessSound()
	subtitle := "Reference " + sent.Ref
	var body strings.Builder
	if sent.Issue > 0 {
		number := strconv.Itoa(sent.Issue)
		subtitle = "Issue #" + number + " · reference " + sent.Ref
		body.WriteString("## Filed as issue #" + number + "\n" +
			"It is public on JukaHub's GitHub, where anyone can read it and add to it. You do not need to do anything else.\n\n")
	}
	body.WriteString("## Your reference\n" + sent.Ref + "\n\n" +
		"Mention it if you talk to us about this problem — on GitHub or Discord — so the report can be found.")
	spec := tbxSheetSpec{
		title:    "Thank you — report sent",
		subtitle: subtitle,
		icon:     "check",
		body:     body.String(),
		buttons:  []tbxSheetButton{{label: "Done", primary: true}},
	}
	if sent.URL != "" {
		if code, err := qrcode.New(sent.URL, qrcode.Low); err == nil {
			spec.qr = code.Bitmap()
			spec.qrLabel = "Optional: scan to follow it on your phone"
		}
	}
	tbxOpenSheet(config, spec)
}

// bugrepQueuedSheet says the report waits on this device, and what for: the
// network, a report service that is not answering, one that is busy, or a
// certificate this firmware cannot check. copied says whether a readable copy
// was saved next to the support reports (the certificate case writes one).
func bugrepQueuedSheet(config *Config, ref string, why bugrepHoldWhy, copied bool) {
	body := "There is no connection to the report service right now. The report is saved on this device and is sent " +
		"automatically the next time JukaHub is online — you do not need to do anything."
	switch why {
	case bugrepHoldService:
		body = "The report service is not answering right now. The report is saved on this device and JukaHub " +
			"sends it on its own after the next start — you do not need to do anything."
	case bugrepHoldBusy:
		body = "The report service is busy right now. The report is saved on this device and is sent automatically " +
			"in a little while — you do not need to do anything."
	case bugrepHoldTrust:
		body = "This firmware cannot check the report server's certificate, so the report could not be sent. It is " +
			"saved on this device and JukaHub tries again after the next start."
		if copied {
			body = "This firmware cannot check the report server's certificate, so the report could not be sent. It is " +
				"saved on this device, with a readable copy in the Patch reports folder, and JukaHub tries again after " +
				"the next start."
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Queued",
		subtitle: "Reference " + ref,
		icon:     "upload",
		body:     body,
		buttons:  []tbxSheetButton{{label: "Done", primary: true}},
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

// bugrepPhoneSheet is the path for a report the device cannot send itself: a
// QR code that opens a prefilled GitHub issue on a phone, the full report saved
// on the device, and — on a desktop — the same issue in the browser. later
// adds "Send later", which keeps the report in the outbox instead (for a
// report service that is only not answering right now).
func bugrepPhoneSheet(config *Config, r bugrepReport, why string, later bool) {
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
	if later {
		buttons = append(buttons, tbxSheetButton{label: "Send later", run: func() {
			bugrepHold(config, r, bugrepHoldService)
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
