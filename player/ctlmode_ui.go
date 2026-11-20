package main

// ctlmode_ui.go — Controller Mode's page: Tools → Controller Mode, the Apps
// screen, the Start menu, Home search and a Quick Settings button all open it.
//
// The page has four faces, picked from the session in ctlmode.go:
//
//   - nothing running: the links (every registered transport, with its own
//     help rows under it), the installed TrimUI pad apps (GamePad to PC, the
//     Bluetooth pad app) and the two settings — the layout and what the screen
//     does while playing;
//   - a link running: its status card, its own rows (PCs found, "Forget …"),
//     Stop, and Resume once Menu has paused a connected link;
//   - the layout editor, over either of them;
//   - forwarding: a large live picture of the controller as the PC sees it.
//     The core owns every press then (ctlModeEvent), so this face only draws.
//
// A transport's question (a pairing code to compare, a phone asking to
// connect) is a modal card that takes A and B before anything else.
//
// B goes one level up: it closes the editor, stops a running link, and on
// the list leaves the page. However the page is left, the link stops
// (ctlModeSceneLeft, called by core.go when the scene transition lands).
//
// Drawing is in ctlmode_ui_draw.go.

import (
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// qsLinkControllerMode is the Quick Settings button that opens the page
// (quicksettings.go qsOpenLink), numbered past that file's own link actions
// and the Stopwatch's 100 so a new one there cannot collide.
const qsLinkControllerMode qsLinkAction = 101

// ctlUIDraftSave is how long the layout editor waits after the last change
// before it writes the layout, so a held Right on the dead zone is one save.
const ctlUIDraftSave = 600 * time.Millisecond

// ctlUIGameRunningMsg is the refusal while a game has the screen and the pad.
const ctlUIGameRunningMsg = "Quit the game first — Controller Mode needs the pad"

// ctlUIFullMsg is the refusal when the user's layouts are at ctlMaxLayouts.
const ctlUIFullMsg = "You have 16 layouts of your own — delete one first"

// ctlUI is the page's state (main thread only).
var ctlUI struct {
	visit tbxVisit
	nav   tbxNav
	// reveal scrolls the focus into view after the next layout pass (the
	// focus was set before its row existed).
	reveal bool
	// Wrapped text: the status card's detail, the notes, the prompt's body.
	wrap       tbxWrapCache
	noteWrap   tbxWrapCache
	promptWrap tbxWrapCache
	// editing: the layout editor replaces the lists.
	editing bool
	// draft is the layout the editor changes; dirty until it is saved.
	draft   ctlLayout
	dirty   bool
	dirtyAt time.Time
	// lastRunning and wasActive notice the session changing under the page,
	// so the focus lands on what the new face offers.
	lastRunning ctlTransport
	wasActive   bool
	// The page header, rebuilt only when its words change (tbxHeader
	// allocates a glyph closure).
	headerSub    string
	headerStatus string
	header       UIPageHeader
	// The layout row's summary, rebuilt only when the layout changes.
	summaryOf ctlLayout
	summary   string
	// The live face's second line, rebuilt at most once a second: its press
	// count would otherwise render a new line for every press.
	liveMeta   string
	liveMetaAt time.Time
	// The prompt card: its header, and its buttons on the last frame (clicks).
	promptTitle  string
	promptHeader UIPageHeader
	promptYes    sdl.Rect
	promptNo     sdl.Rect
}

// ── Scene ────────────────────────────────────────────────────────────────

type ctlModeScene struct{ sceneBase }

func (ctlModeScene) render(r *sdl.Renderer, c *Config) bool {
	if r == nil || c == nil {
		return false
	}
	ctlModeTick(time.Now())
	if ctlUI.visit.begin() {
		ctlUIVisit()
	}
	ctlUIFollowSession()
	ctlUISaveDraft(false)
	ctlUIRender(r, c)
	return true
}

func (ctlModeScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return ctlUIDispatch(c, tbxKeyAction(e))
}

func (ctlModeScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctlUIDispatch(c, tbxPadAction(e))
}

func init() { registerScene(ctlModeScene{sceneBase{name: ctlModeSceneName}}) }

// ctlModeGameRunning reports a game (or another program) on the screen: the
// page is not offered then, and no link starts.
func ctlModeGameRunning() bool {
	return runningGameChildren.Load() > 0 || foreignScreenSuspended() || gameScreenSuspended()
}

// ctlModeSceneLeft runs when another scene takes over (core.go
// completeSceneTransition): the editor's last change is saved and the link
// stops, however the page was left.
func ctlModeSceneLeft() {
	ctlUISaveDraft(true)
	ctlUI.editing = false
	if ctlModeRunning() != nil {
		ctlModeStopTransport()
	}
}

// ctlUIVisit sets the page up for a fresh visit: the link chosen last is
// focused, or the running one's Stop.
func ctlUIVisit() {
	ctlUI.nav.scroll = 0
	ctlUI.editing = false
	ctlUI.dirty = false
	ctlUI.lastRunning = ctlModeRunning()
	ctlUI.wasActive = ctlModeActive()
	ctlUI.reveal = true
	// Starts the card scan for the pad apps now, so their rows are there by
	// the time the user looks.
	extAppFind(extAppGamepadPC)
	if ctlModeRunning() != nil {
		ctlUI.nav.focus = "stop"
		return
	}
	ctlUI.nav.focus = ""
	if id := ctlLastTransport(); id != "" && ctlModeTransportByID(id) != nil {
		ctlUI.nav.focus = "t:" + id
	}
}

// ctlUIFollowSession moves the focus when the session changes under the
// page: a link started or stopped, forwarding began, Menu paused it.
func ctlUIFollowSession() {
	run := ctlModeRunning()
	if run != ctlUI.lastRunning {
		prev := ctlUI.lastRunning
		ctlUI.lastRunning = run
		switch {
		case ctlUI.editing:
			// The editor stays where it is; Done lands on the new face.
		case run != nil:
			ctlUI.nav.focus = "stop"
		case prev != nil:
			ctlUI.nav.focus = "t:" + prev.ID()
		}
		ctlUI.reveal = true
	}
	active := ctlModeActive()
	if active && !ctlUI.wasActive {
		ctlUI.liveMeta = ""
		// The PC connected under an open sheet (a setup step, the Delete
		// question): it would be drawn over the live controller with no
		// button reaching it, and answer the next press after Menu. It goes,
		// as Keep would.
		if tbxSheet.open && tbxSheet.scene == ctlModeSceneName {
			tbxCloseSheet()
		}
	}
	if active && !ctlUI.wasActive && ctlUI.editing {
		// Forwarding takes the screen: the editor's change goes in first.
		ctlUISaveDraft(true)
		ctlUI.editing = false
		// The rename keyboard goes with the editor (its answer would be
		// dropped anyway, and it reads the held D-pad itself).
		if osPromptActive() {
			vkCloseKeyboard()
		}
	}
	if !active && ctlUI.wasActive && ctlModePaused() {
		// Menu's tap: A resumes from here.
		ctlUI.nav.focus = "resume"
		ctlUI.reveal = true
	}
	ctlUI.wasActive = active
}

// ctlUILayout is the layout the page shows and edits: the live one while a
// link runs, else the one the next link starts with.
func ctlUILayout() ctlLayout {
	if ctlModeRunning() == nil {
		return ctlModeLayoutFor("")
	}
	return ctlModeLayoutNow()
}

// ── Input ────────────────────────────────────────────────────────────────

// ctlUIDispatch routes one action: nothing while forwarding (the core owns
// the pad; only a stray key-up gets here), the transport's question first,
// then the open sheet and the page (tbxDispatch, which leaves the page on an
// unused Back).
func ctlUIDispatch(c *Config, act tbxAction) bool {
	if c == nil || act == tbxActNone {
		return false
	}
	if ctlModeActive() {
		return true
	}
	if p := ctlUIPrompt(); p != nil {
		ctlUIPromptAction(p, act)
		return true
	}
	return tbxDispatch(c, act, ctlUIAction)
}

// ctlUIPrompt is the running transport's question, or nil.
func ctlUIPrompt() *ctlPrompt {
	t := ctlModeRunning()
	if t == nil || ctlModeActive() {
		return nil
	}
	return t.Prompt()
}

// ctlUIPromptAction answers the question: A is Yes, B is No. Everything else
// is swallowed, so the page behind cannot move under the card.
func ctlUIPromptAction(p *ctlPrompt, act tbxAction) {
	t := ctlModeRunning()
	if t == nil || p == nil {
		return
	}
	switch act {
	case tbxActConfirm:
		PlayActivateSound()
		if p.YesAction != "" {
			t.Do(p.YesAction)
		}
	case tbxActBack:
		PlayBackSound()
		if p.NoAction != "" {
			t.Do(p.NoAction)
		}
	}
}

// ctlUIAction is one action on the page itself.
func ctlUIAction(config *Config, act tbxAction) bool {
	nav := &ctlUI.nav
	switch act {
	case tbxActConfirm:
		ctlUIActivate(config, nav.focus)
		return true
	case tbxActBack:
		return ctlUIBack()
	case tbxActLeft, tbxActRight:
		dir := 1
		if act == tbxActLeft {
			dir = -1
		}
		if ctlUIStep(nav.focus, dir) {
			return true
		}
	case tbxActX, tbxActY:
		return true
	}
	return tbxNavAction(nav, act)
}

// ctlUIBack is B: the editor closes, a running link stops, and on the list
// the page is left (false: tbxDispatch goes back a scene).
func ctlUIBack() bool {
	if ctlUI.editing {
		ctlUICloseEditor()
		PlayBackSound()
		return true
	}
	if ctlModeRunning() != nil {
		ctlModeStopTransport()
		PlayBackSound()
		return true
	}
	ctlUISaveDraft(true)
	return false
}

// ctlUIStepsValue reports the rows Left/Right change in place.
func ctlUIStepsValue(id string) bool {
	switch id {
	case "layout", "screen", "ed:layout", "ed:face", "ed:dpad", "ed:dz", "ed:invert", "ed:guide":
		return true
	}
	return false
}

// ctlUIStep is Left/Right on a value row; false leaves the press to the
// focus list (side-by-side buttons).
func ctlUIStep(id string, dir int) bool {
	switch id {
	case "layout", "ed:layout":
		ctlUICycleLayout(dir)
	case "screen":
		ctlUIToggleScreen()
	case "ed:face":
		ctlUIChange(func(l *ctlLayout) { l.Face = ctlUIOther(l.Face, ctlFacePosition, ctlFaceLabel) })
	case "ed:dpad":
		ctlUIChange(func(l *ctlLayout) { l.Dpad = ctlUIOther(l.Dpad, ctlDpadDpad, ctlDpadStick) })
	case "ed:dz":
		ctlUIStepDeadzone(2*dir, false)
	case "ed:invert":
		ctlUIChange(func(l *ctlLayout) { l.InvertRY = !l.InvertRY })
	case "ed:guide":
		ctlUIChange(func(l *ctlLayout) { l.MenuGuide = !l.MenuGuide })
	default:
		return false
	}
	return true
}

// ctlUIActivate is A (or a click) on the focused control.
func ctlUIActivate(config *Config, id string) {
	switch {
	case strings.HasPrefix(id, "t:"):
		ctlUIStart(id[2:])
	case strings.HasPrefix(id, "h:"), strings.HasPrefix(id, "r:"):
		t, n := ctlUIRowOwner(id)
		ctlUIDoRow(config, t, n)
	case id == "app:pc":
		ctlUILaunchApp(extAppGamepadPC)
	case id == "app:bt":
		ctlUILaunchApp(extAppGamepadBT)
	case id == "layout":
		PlayActivateSound()
		ctlUIOpenEditor()
	case id == "screen":
		ctlUIToggleScreen()
	case id == "resume":
		// The focus can still name Resume for a press that lands before the
		// next frame drops the button (the link fell back to Waiting): the
		// pad must not go to a link with nobody on it.
		if t := ctlModeRunning(); t == nil || t.Status().State != ctlLinkConnected {
			return
		}
		ctlUISaveDraft(true)
		ctlModeBegin()
	case id == "retry":
		if t := ctlModeRunning(); t != nil {
			tid := t.ID()
			ctlModeStopTransport()
			ctlUIStart(tid)
		}
	case id == "stop":
		ctlModeStopTransport()
		PlayBackSound()
	case id == "ed:layout":
		ctlUICycleLayout(1)
	case id == "ed:name":
		ctlUIRename()
	case id == "ed:dz":
		ctlUIStepDeadzone(2, true)
	case id == "ed:face", id == "ed:dpad", id == "ed:invert", id == "ed:guide":
		ctlUIStep(id, 1)
	case id == "ed:new":
		ctlUIDuplicate()
	case id == "ed:delete":
		ctlUIAskDelete(config)
	case id == "ed:done":
		PlayBackSound()
		ctlUICloseEditor()
	}
}

// ctlUIDoRow runs row n of a transport's Rows. A row without an Action is a
// line of information: A shows the whole of it in a sheet (the row cuts a
// long setup step short).
func ctlUIDoRow(config *Config, t ctlTransport, n string) {
	r, ok := ctlUIRowAt(t, n)
	if !ok {
		return
	}
	if r.Action != "" {
		PlayActivateSound()
		t.Do(r.Action)
		return
	}
	if r.Detail == "" {
		return
	}
	PlayActivateSound()
	tbxOpenSheet(config, tbxSheetSpec{title: r.Label, subtitle: t.Title(), icon: ctlUIIcon(t.ID()), body: r.Detail})
}

// ctlUIRowAt is row n (a decimal index) of a transport's Rows.
func ctlUIRowAt(t ctlTransport, n string) (ctlRow, bool) {
	if t == nil {
		return ctlRow{}, false
	}
	i, err := strconv.Atoi(n)
	rows := t.Rows()
	if err != nil || i < 0 || i >= len(rows) {
		return ctlRow{}, false
	}
	return rows[i], true
}

// ctlUIStart starts a link; its refusal is a toast in the transport's words.
func ctlUIStart(id string) {
	if ctlModeGameRunning() {
		PlayErrorSound()
		ShowToast(ctlUIGameRunningMsg, ToastKindWarning)
		return
	}
	ctlUISaveDraft(true)
	if err := ctlModeStartTransport(id); err != nil {
		PlayErrorSound()
		ShowToast(err.Error(), ToastKindWarning)
		return
	}
	PlayActivateSound()
}

// ctlUILaunchApp opens an installed pad app. JukaHub's own link stops first:
// the app needs the pad and the radio to itself.
func ctlUILaunchApp(kind string) {
	app, ok := extAppFind(kind)
	if !ok {
		PlayErrorSound()
		ShowToast("That app is no longer on the card", ToastKindWarning)
		return
	}
	if ctlModeGameRunning() {
		PlayErrorSound()
		ShowToast(ctlUIGameRunningMsg, ToastKindWarning)
		return
	}
	ctlUISaveDraft(true)
	ctlModeStopTransport()
	PlayActivateSound()
	if extAppLaunch(app) {
		ShowToast("Opening "+app.Name+" — JukaHub comes back when it closes", ToastKindInfo)
	}
}

// ctlUIToggleScreen flips "Screen while playing".
func ctlUIToggleScreen() {
	if ctlScreenSetting() == "on" {
		ctlModeSetScreen("off")
	} else {
		ctlModeSetScreen("on")
	}
	PlayNavSound()
}

// ── Layouts ──────────────────────────────────────────────────────────────

// ctlUICycleLayout uses the next (dir 1) or previous layout.
func ctlUICycleLayout(dir int) {
	ctlUISaveDraft(true)
	list := ctlModeLayoutList()
	n := len(list)
	if n == 0 {
		return
	}
	cur := ctlUILayout().ID
	at := 0
	for i, l := range list {
		if l.ID == cur {
			at = i
			break
		}
	}
	at = (at + dir + n) % n
	if ctlModeUseLayout(list[at].ID) {
		PlayNavSound()
		if ctlUI.editing {
			ctlUI.draft = ctlUILayout()
		}
	}
}

// ctlUIOpenEditor opens the editor on the layout in use.
func ctlUIOpenEditor() {
	ctlUI.draft = ctlUILayout()
	ctlUI.dirty = false
	ctlUI.editing = true
	ctlUI.nav.focus = "ed:layout"
	ctlUI.nav.scroll = 0
	ctlUI.reveal = true
}

// ctlUICloseEditor saves and goes back to the face the editor covered.
func ctlUICloseEditor() {
	ctlUISaveDraft(true)
	ctlUI.editing = false
	ctlUI.nav.focus = "layout"
	ctlUI.reveal = true
}

// ctlUISaveDraft writes the editor's change once it has settled (or now,
// with force).
func ctlUISaveDraft(force bool) {
	if !ctlUI.dirty {
		return
	}
	if !force && time.Since(ctlUI.dirtyAt) < ctlUIDraftSave {
		return
	}
	ctlUI.dirty = false
	if saved, ok := ctlModeSaveLayout(ctlUI.draft); ok {
		ctlUI.draft = saved
	}
}

// ctlUIOwnCopy makes the draft a layout of the user's own: a built-in one is
// never changed, so the first change to it saves a copy and uses that.
func ctlUIOwnCopy(name string) bool {
	if !ctlUI.draft.Builtin {
		return true
	}
	l := ctlUI.draft
	l.ID = ""
	l.Name = name
	if l.Name == "" {
		l.Name = "My " + ctlUI.draft.Name
	}
	saved, ok := ctlModeSaveLayout(l)
	if !ok {
		PlayErrorSound()
		ShowToast(ctlUIFullMsg, ToastKindWarning)
		return false
	}
	ctlModeUseLayout(saved.ID)
	ctlUI.draft = saved
	ShowToast("Built-in layouts stay as they are — this change is saved as "+saved.Name, ToastKindInfo)
	return true
}

// ctlUIChange applies one edit to the draft (a copy of a built-in first).
func ctlUIChange(edit func(l *ctlLayout)) {
	if !ctlUIOwnCopy("") {
		return
	}
	edit(&ctlUI.draft)
	ctlUI.dirty = true
	ctlUI.dirtyAt = time.Now()
	PlayNavSound()
}

// ctlUIStepDeadzone moves the dead zone by delta percent, inside 0..30; wrap
// starts again at 0 past the top (A steps round, Left/Right stop at the ends).
func ctlUIStepDeadzone(delta int, wrap bool) {
	next := ctlUI.draft.Deadzone + delta
	switch {
	case next > 30 && wrap:
		next = 0
	case next > 30:
		next = 30
	case next < 0:
		next = 0
	}
	if next == ctlUI.draft.Deadzone {
		return
	}
	ctlUIChange(func(l *ctlLayout) { l.Deadzone = next })
}

// ctlUIOther is the other of two values.
func ctlUIOther(v, a, b string) string {
	if v == a {
		return b
	}
	return a
}

// ctlUIRename names the layout on the keyboard. A built-in layout's new name
// goes to a copy of it.
func ctlUIRename() {
	PlayActivateSound()
	id, seed := ctlUI.draft.ID, ctlUI.draft.Name
	if ctlUI.draft.Builtin {
		seed = "My " + seed
	}
	osPromptOpen("Layout name", "My layout", "Rename", seed, 32, func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || !ctlUI.editing || ctlUI.draft.ID != id {
			return
		}
		if ctlUI.draft.Builtin {
			ctlUIOwnCopy(name)
			return
		}
		ctlUI.draft.Name = name
		ctlUI.dirty = true
		ctlUISaveDraft(true)
	})
}

// ctlUIDuplicate saves a copy of the layout and uses it.
func ctlUIDuplicate() {
	ctlUISaveDraft(true)
	l := ctlUI.draft
	l.ID = ""
	if l.Builtin {
		l.Name = "My " + l.Name
	} else {
		l.Name += " copy"
	}
	saved, ok := ctlModeSaveLayout(l)
	if !ok {
		PlayErrorSound()
		ShowToast(ctlUIFullMsg, ToastKindWarning)
		return
	}
	PlayActivateSound()
	ctlModeUseLayout(saved.ID)
	ctlUI.draft = saved
	ShowToast("New layout: "+saved.Name, ToastKindSuccess)
}

// ctlUIAskDelete asks before a user layout goes.
func ctlUIAskDelete(config *Config) {
	if ctlUI.draft.Builtin {
		PlayErrorSound()
		ShowToast("Built-in layouts cannot be deleted", ToastKindInfo)
		return
	}
	ctlUISaveDraft(true)
	PlayActivateSound()
	id, name := ctlUI.draft.ID, ctlUI.draft.Name
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Delete " + name + "?",
		icon:  "trash",
		body:  "PCs and phones that used it get your last layout instead. This cannot be undone.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Delete", primary: true, run: func() {
				if !ctlModeDeleteLayout(id) {
					return
				}
				ctlUI.draft = ctlUILayout()
				ctlUI.dirty = false
				ctlUI.nav.focus = "ed:layout"
				ShowToast("Deleted "+name, ToastKindInfo)
			}},
		},
	})
}

// ── Hints and pointer ────────────────────────────────────────────────────

// ctlUIConfirmLabel is what A does on the focused control ("" none).
func ctlUIConfirmLabel(id string) string {
	switch {
	case strings.HasPrefix(id, "t:"):
		return "Start"
	case strings.HasPrefix(id, "h:"), strings.HasPrefix(id, "r:"):
		r, ok := ctlUIRowAt(ctlUIRowOwner(id))
		switch {
		case !ok:
			return ""
		case r.Action != "":
			return "Select"
		case r.Detail != "":
			return "Read"
		}
		return ""
	case strings.HasPrefix(id, "app:"):
		return "Open"
	}
	switch id {
	case "layout":
		return "Edit"
	case "screen", "ed:face", "ed:dpad", "ed:dz", "ed:invert", "ed:guide":
		return "Change"
	case "resume":
		return "Resume"
	case "retry":
		return "Try again"
	case "stop":
		return "Stop"
	case "ed:layout":
		return "Next"
	case "ed:name":
		return "Rename"
	case "ed:new":
		return "Create"
	case "ed:delete":
		return "Delete"
	case "ed:done":
		return "Done"
	}
	return ""
}

// ctlUIRowOwner splits a transport row's id: "h:<transport>:<n>" is a help
// row on the list, "r:<n>" a row of the running link.
func ctlUIRowOwner(id string) (ctlTransport, string) {
	if rest, ok := strings.CutPrefix(id, "h:"); ok {
		tid, n, _ := strings.Cut(rest, ":")
		return ctlModeTransportByID(tid), n
	}
	if rest, ok := strings.CutPrefix(id, "r:"); ok {
		return ctlModeRunning(), rest
	}
	return nil, ""
}

// ctlModeFooterHints is the page's footer (extras.go footerHints): the way
// back while forwarding, the question's answers, the sheet's buttons, or
// what the focused control takes.
func ctlModeFooterHints(config *Config) []FooterHint {
	if ctlModeActive() {
		switch {
		case hintsUseKeyboard():
			return []FooterHint{{Button: "Esc", Label: "Return to JukaHub"}}
		case !P().IsHandheld():
			hints := []FooterHint{{Button: "Guide", Label: "Return to JukaHub"}}
			if ctlModeLayoutNow().MenuGuide {
				hints = append(hints, FooterHint{Button: "Hold Guide", Label: "Guide"})
			}
			return hints
		}
		hints := []FooterHint{{Button: "Menu", Label: "Return to JukaHub"}}
		if ctlModeLayoutNow().MenuGuide {
			hints = append(hints, FooterHint{Button: "Hold Menu", Label: "Guide"})
		}
		return hints
	}
	if p := ctlUIPrompt(); p != nil {
		yes, no := ctlUIPromptWords(p)
		return []FooterHint{{Button: "A", Label: yes}, {Button: "B", Label: no}}
	}
	if tbxSheetVisible(config) {
		return tbxSheetHints()
	}
	focus := ctlUI.nav.focus
	hints := make([]FooterHint, 0, 4)
	hints = append(hints, FooterHint{Button: "D-Pad", Label: "Navigate"})
	if label := ctlUIConfirmLabel(focus); label != "" {
		hints = append(hints, FooterHint{Button: "A", Label: label})
	}
	if ctlUIStepsValue(focus) {
		hints = append(hints, FooterHint{Button: "Left/Right", Label: "Change"})
	}
	back := "Back"
	switch {
	case ctlUI.editing:
		back = "Done"
	case ctlModeRunning() != nil:
		back = "Stop"
	}
	return append(hints, FooterHint{Button: "B", Label: back})
}

// ctlUIPromptWords are the question's two answers, "Yes" and "No" unless the
// transport names them.
func ctlUIPromptWords(p *ctlPrompt) (string, string) {
	yes, no := p.Yes, p.No
	if yes == "" {
		yes = "Yes"
	}
	if no == "" {
		no = "No"
	}
	return yes, no
}

// ctlModeSceneClick is a click or tap on the page (main.go): the question's
// buttons, the open sheet, then the page's rows and buttons.
func ctlModeSceneClick(mx, my int32, config *Config) bool {
	if tbxCurrentScene(config) != ctlModeSceneName {
		return false
	}
	if ctlModeActive() {
		return true
	}
	if p := ctlUIPrompt(); p != nil {
		switch {
		case ctlUI.promptYes.W > 0 && pointInRect(mx, my, ctlUI.promptYes):
			ctlUIPromptAction(p, tbxActConfirm)
		case ctlUI.promptNo.W > 0 && pointInRect(mx, my, ctlUI.promptNo):
			ctlUIPromptAction(p, tbxActBack)
		}
		return true
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := ctlUI.nav.hit(mx, my)
	if !ok {
		return false
	}
	ctlUI.nav.focus = it.id
	ctlUIActivate(config, it.id)
	return true
}

// ctlModeSceneWheel scrolls the page or its sheet (main.go).
func ctlModeSceneWheel(y int32, config *Config) bool {
	if y == 0 || tbxCurrentScene(config) != ctlModeSceneName {
		return false
	}
	if ctlModeActive() || ctlUIPrompt() != nil {
		return true
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	ctlUI.nav.scrollBy(-y * 48)
	return true
}
