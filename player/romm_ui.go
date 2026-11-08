package main

// romm_ui.go — the ROM manager screen (Tools → ROM manager).
//
// The page scans as it opens and shows what it found as a plan: one heading per
// destination ("Move 12 into Roms/GBA", "2 BIOS files into BIOS", "3 duplicate
// copies", "Left alone"), every file under it with a switch. Moves are on, the
// deletions are off; "Apply" carries out what is on in one press, and asks a
// second time, with Cancel focused, before anything is deleted. Every organise
// can be put back with "Undo last organise" (romm_apply.go keeps the journal).
//
// The page is drawn with the tool screens' frame (tbxui.go): Home's glass, the
// shared header, a focus list that scrolls, and the shared sheet for the two
// questions it asks. The scan is scene work, so leaving the page stops it; an
// organise that has started finishes even if the page is left.

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// rommSceneName is the scene the page is drawn in. No shipped config carries
// it: ensureRommScene adds it in memory.
const rommSceneName = "RomManager"

// rommScanEnabled is a test seam: a render test that lands on the page must
// not walk the developer's Downloads folder.
var rommScanEnabled = true

// rommPhase is what the page is doing.
type rommPhase int

const (
	rommPhaseScanning rommPhase = iota
	rommPhaseReady
	rommPhaseApplying
	rommPhaseDone
)

// rommSection is one heading of the plan and the items listed under it.
type rommSection struct {
	id    string
	kind  rommKind
	title string
	sub   string
	icon  string
	pill  string
	tone  tbxTone
	items []int
	// itemIDs are the focus ids of the items, built once with the section so a
	// frame does not format hundreds of strings.
	itemIDs []string
}

// rommButton is one button of the page's summary card.
type rommButton struct {
	id       string
	label    string
	primary  bool
	disabled bool
}

// rommUIState is the page's state. It is only touched on the main loop; the
// background work reports through rommLive and tbxPost.
type rommUIState struct {
	visit    tbxVisit
	nav      tbxNav
	phase    rommPhase
	plan     rommPlan
	sections []rommSection
	// itemSection maps an item to its section.
	itemSection []int
	gen         int
	cancel      context.CancelFunc
	undoRuns    int
	note        string
	doneTitle   string
	doneSub     string
	doneErrors  []string
	doneMoved   bool
}

var rommUI rommUIState

// rommLive is the progress the background work reports.
var rommLive struct {
	mu    sync.Mutex
	stage string
	files int
	done  int
	total int
}

// rommLiveSet records progress; it matches rommReport.
func rommLiveSet(stage string, files, done, total int) {
	rommLive.mu.Lock()
	rommLive.stage, rommLive.files, rommLive.done, rommLive.total = stage, files, done, total
	rommLive.mu.Unlock()
}

func rommLiveGet() (string, int, int, int) {
	rommLive.mu.Lock()
	defer rommLive.mu.Unlock()
	return rommLive.stage, rommLive.files, rommLive.done, rommLive.total
}

// rommCount is "1 file" or "12 files".
func rommCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ── Scene ───────────────────────────────────────────────────────────────────

// ensureRommScene gives the page its scene, in memory, like ensureAlarmScene:
// no shipped config carries it and the user's config is never rewritten for
// it. The page draws itself, so its element list stays empty. Idempotent;
// called at startup and after every config reload.
func ensureRommScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, rommSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: rommSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == rommSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// rommOpen opens the ROM manager (the Tools hub tile and the Settings row).
func rommOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	ensureRommScene(config)
	idx := sceneIndexExact(config, rommSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

type rommScene struct{ sceneBase }

func (rommScene) render(r *sdl.Renderer, c *Config) bool {
	rommRender(r, c)
	return true
}

func (rommScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), rommOnAction)
}

func (rommScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), rommOnAction)
}

func init() { registerScene(rommScene{sceneBase{name: rommSceneName}}) }

// ── Work ────────────────────────────────────────────────────────────────────

// rommEnter runs once per visit: the page loads its plan as it opens, unless an
// organise is still running from the last visit.
func rommEnter() {
	if rommUI.phase == rommPhaseApplying {
		return
	}
	rommStartScan()
}

// rommStartScan scans in the background. Leaving the page cancels it.
func rommStartScan() {
	if rommUI.cancel != nil {
		rommUI.cancel()
		rommUI.cancel = nil
	}
	rommUI.gen++
	gen := rommUI.gen
	rommUI.phase = rommPhaseScanning
	rommUI.note = ""
	rommUI.nav.focus = "stop"
	rommUI.nav.scroll = 0
	rommLiveSet("Getting ready…", 0, 0, 0)
	roots, targets := rommDefaultRoots()
	if !rommScanEnabled {
		rommSetPlan(rommPlan{Targets: targets})
		return
	}
	journal := rommJournalPath()
	ctx, cancel := context.WithCancel(sceneWorkContext())
	rommUI.cancel = cancel
	started := runSceneTask(func(context.Context) {
		defer cancel()
		plan := rommScan(ctx, roots, targets, rommLiveSet)
		runs := rommUndoable(journal)
		tbxPost(func() {
			if gen != rommUI.gen {
				return
			}
			rommUI.cancel = nil
			rommUI.undoRuns = runs
			rommSetPlan(plan)
		})
	})
	if !started {
		cancel()
		rommUI.cancel = nil
		rommSetPlan(rommPlan{Targets: targets})
		rommUI.note = "The scan could not start — try again in a moment."
	}
}

// rommStop asks the running scan or organise to stop after the current file.
func rommStop() {
	if rommUI.cancel != nil {
		rommUI.cancel()
		PlayBackSound()
	}
}

// rommSetPlan shows a finished scan.
func rommSetPlan(plan rommPlan) {
	rommUI.plan = plan
	rommUI.sections, rommUI.itemSection = rommBuildSections(plan.Items)
	rommRefreshSections()
	rommUI.phase = rommPhaseReady
	rommUI.nav.scroll = 0
	if changes, _, _ := rommPlanCounts(plan.Items); changes > 0 {
		rommUI.nav.focus = "apply"
	} else {
		rommUI.nav.focus = "rescan"
	}
}

// rommBuildSections groups the plan: one section per destination folder for
// moves and firmware, one per kind for the rest.
func rommBuildSections(items []rommItem) ([]rommSection, []int) {
	var out []rommSection
	byKey := map[string]int{}
	itemSection := make([]int, len(items))
	for i, it := range items {
		key := strconv.Itoa(int(it.Kind))
		if it.Kind == rommKindMisplaced || it.Kind == rommKindBIOS {
			key += "|" + it.Group
		}
		idx, ok := byKey[key]
		if !ok {
			idx = len(out)
			byKey[key] = idx
			out = append(out, rommSection{id: "sec" + strconv.Itoa(idx), kind: it.Kind})
		}
		out[idx].items = append(out[idx].items, i)
		out[idx].itemIDs = append(out[idx].itemIDs, "it"+strconv.Itoa(i))
		itemSection[i] = idx
	}
	for i := range out {
		sec := &out[i]
		n := len(sec.items)
		first := items[sec.items[0]]
		switch sec.kind {
		case rommKindMisplaced:
			sec.title = "Move " + strconv.Itoa(n) + " into " + first.Group
			sec.sub = first.System + " games found in other places"
			sec.icon = iconEmuGlyphFor(first.System)
		case rommKindBIOS:
			sec.title = "Move " + rommCount(n, "BIOS file", "BIOS files") + " into " + first.Group
			sec.sub = "Firmware the emulators look for there"
			sec.icon = "key"
		case rommKindDuplicate:
			sec.title = rommCount(n, "duplicate copy", "duplicate copies")
			sec.sub = "Byte-for-byte copies of a game that stays · deleting asks again"
			sec.icon = "copy"
		case rommKindEmpty:
			sec.title = rommCount(n, "empty file", "empty files")
			sec.sub = "0-byte files, usually failed downloads · deleting asks again"
			sec.icon = "broom"
		default:
			sec.title = rommCount(n, "file left alone", "files left alone")
			sec.sub = "They look like games but could not be identified"
			sec.icon = "info"
		}
	}
	return out, itemSection
}

// rommRefreshSections recounts the switches behind every section's pill.
func rommRefreshSections() {
	for i := range rommUI.sections {
		sec := &rommUI.sections[i]
		if sec.kind == rommKindUnknown {
			sec.pill, sec.tone = "Left alone", tbxToneNeutral
			continue
		}
		on := 0
		for _, idx := range sec.items {
			if rommUI.plan.Items[idx].On {
				on++
			}
		}
		deletes := sec.kind == rommKindDuplicate || sec.kind == rommKindEmpty
		switch {
		case on == 0:
			sec.pill, sec.tone = "Off", tbxToneNeutral
		case on == len(sec.items):
			sec.pill, sec.tone = "All on", tbxToneAccent
		default:
			sec.pill, sec.tone = strconv.Itoa(on)+" of "+strconv.Itoa(len(sec.items))+" on", tbxToneAccent
		}
		if deletes && on > 0 {
			sec.tone = tbxToneBad
		}
	}
}

// rommToggleItem flips one item's switch.
func rommToggleItem(idx int) {
	if idx < 0 || idx >= len(rommUI.plan.Items) {
		return
	}
	it := &rommUI.plan.Items[idx]
	if it.Action == rommActNone {
		ShowToast(it.Path, ToastKindInfo)
		return
	}
	it.On = !it.On
	rommRefreshSections()
	PlayNavSound()
}

// rommToggleSection turns a whole section on, or off when it is all on.
func rommToggleSection(si int) {
	if si < 0 || si >= len(rommUI.sections) {
		return
	}
	sec := rommUI.sections[si]
	if sec.kind == rommKindUnknown {
		return
	}
	all := true
	for _, idx := range sec.items {
		if !rommUI.plan.Items[idx].On {
			all = false
			break
		}
	}
	for _, idx := range sec.items {
		rommUI.plan.Items[idx].On = !all
	}
	rommRefreshSections()
	PlayActivateSound()
}

// rommFocusedSection is the section the focus is in (on its heading or on one
// of its items), or -1.
func rommFocusedSection() int {
	focus := rommUI.nav.focus
	for i, sec := range rommUI.sections {
		if sec.id == focus {
			return i
		}
	}
	if len(focus) > 2 && focus[:2] == "it" {
		if idx, err := strconv.Atoi(focus[2:]); err == nil && idx >= 0 && idx < len(rommUI.itemSection) {
			return rommUI.itemSection[idx]
		}
	}
	return -1
}

// rommConfirmApply carries out the plan. Moves can be undone, so they go at
// once; deletions are asked about again, with Cancel focused.
func rommConfirmApply(config *Config) {
	changes, moves, deletes := rommPlanCounts(rommUI.plan.Items)
	if changes == 0 {
		ShowToast("Nothing is switched on — turn on the changes you want first", ToastKindInfo)
		return
	}
	if deletes == 0 {
		PlayActivateSound()
		rommApplyNow(false)
		return
	}
	body := "These files are removed from the card. Moves can be undone; deleted files cannot.\n\n## To delete\n"
	listed := 0
	for _, it := range rommUI.plan.Items {
		if !it.On || it.Action != rommActDelete {
			continue
		}
		if listed == 6 {
			body += "• … and " + strconv.Itoa(deletes-listed) + " more\n"
			break
		}
		body += "• " + filepath.Base(it.Path) + "\n"
		listed++
	}
	buttons := []tbxSheetButton{{label: "Cancel"}}
	if moves > 0 {
		buttons = append(buttons, tbxSheetButton{label: "Only move", run: func() { rommApplyNow(false) }})
	}
	buttons = append(buttons, tbxSheetButton{label: "Delete " + strconv.Itoa(deletes), primary: true, run: func() { rommApplyNow(true) }})
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Delete " + rommCount(deletes, "file", "files") + " for good?",
		subtitle: "This cannot be undone",
		icon:     "trash",
		body:     body,
		buttons:  buttons,
		focus:    0,
	})
}

// rommApplyNow organises in the background. The organise is not scene work: a
// run that has started finishes even if the page is left, so the journal always
// describes what really happened.
func rommApplyNow(includeDeletes bool) {
	items := make([]rommItem, len(rommUI.plan.Items))
	copy(items, rommUI.plan.Items)
	if !includeDeletes {
		for i := range items {
			if items[i].Action == rommActDelete {
				items[i].On = false
			}
		}
	}
	_, moves, deletes := rommPlanCounts(items)
	if moves+deletes == 0 {
		return
	}
	if rommUI.cancel != nil {
		rommUI.cancel()
	}
	rommUI.gen++
	gen := rommUI.gen
	rommUI.phase = rommPhaseApplying
	rommUI.nav.focus = "stop"
	rommUI.nav.scroll = 0
	stage := "Organising your games…"
	rommLiveSet(stage, 0, 0, moves+deletes)
	ctx, cancel := context.WithCancel(context.Background())
	rommUI.cancel = cancel
	journal := rommJournalPath()
	go func() {
		defer cancel()
		res := rommApplyItems(ctx, items, journal, func(done, total int) {
			rommLiveSet(stage, 0, done, total)
		})
		rommAfterMoves(res.Moves)
		runs := rommUndoable(journal)
		tbxPost(func() {
			if gameLib.open {
				refreshGameLibrary()
			}
			if gen != rommUI.gen {
				return
			}
			rommUI.cancel = nil
			rommUI.undoRuns = runs
			rommShowApplyResult(res)
		})
	}()
}

// rommShowApplyResult turns a finished organise into the done card.
func rommShowApplyResult(res rommApplyResult) {
	rommUI.phase = rommPhaseDone
	rommUI.nav.scroll = 0
	rommUI.doneMoved = res.Moved > 0
	switch {
	case res.Moved > 0 && res.Deleted > 0:
		rommUI.doneTitle = "Done — " + rommCount(res.Moved, "game", "games") + " filed, " + rommCount(res.Deleted, "file", "files") + " deleted"
	case res.Moved > 0:
		rommUI.doneTitle = "Done — " + rommCount(res.Moved, "game", "games") + " filed where the library looks"
	case res.Deleted > 0:
		rommUI.doneTitle = "Done — " + rommCount(res.Deleted, "file", "files") + " deleted"
	default:
		rommUI.doneTitle = "Nothing was changed"
	}
	sub := "The Games library shows the new places at once"
	if res.Failed > 0 {
		sub = rommCount(res.Failed, "file", "files") + " could not be changed — they are listed below"
	}
	if res.Stopped {
		sub = "Stopped early · " + sub
	}
	rommUI.doneSub = sub
	rommUI.doneErrors = res.Errors
	if rommUI.undoRuns > 0 && res.Moved > 0 {
		rommUI.nav.focus = "undo"
	} else {
		rommUI.nav.focus = "rescan"
	}
	switch {
	case res.Failed > 0:
		PlayErrorSound()
		ShowToast(rommUI.doneTitle+" · "+rommCount(res.Failed, "problem", "problems"), ToastKindWarning)
	case res.Moved > 0 || res.Deleted > 0:
		PlaySuccessSound()
		ShowToast(rommUI.doneTitle, ToastKindSuccess)
	}
}

// rommConfirmUndo asks before putting the last organise back.
func rommConfirmUndo(config *Config) {
	run, ok := rommLastRun(rommJournalPath())
	if !ok {
		ShowToast("There is nothing to undo", ToastKindInfo)
		return
	}
	body := "The " + rommCount(len(run.Moves), "file", "files") + " it moved go back where they were, beside anything that " +
		"took their place since. Folders it created are removed when they are empty."
	if len(run.Deleted) > 0 {
		body += "\n\n" + rommCount(len(run.Deleted), "file it deleted cannot", "files it deleted cannot") + " come back."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Undo the organise of " + run.At.Format("2 Jan 15:04") + "?",
		subtitle: rommCount(len(run.Moves), "file goes back", "files go back"),
		icon:     "refresh",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "Put back", primary: true, run: rommUndoNow},
		},
		focus: 1,
	})
}

// rommUndoNow puts the last organise back in the background.
func rommUndoNow() {
	if rommUI.cancel != nil {
		rommUI.cancel()
		rommUI.cancel = nil
	}
	rommUI.gen++
	gen := rommUI.gen
	rommUI.phase = rommPhaseApplying
	rommUI.nav.focus = "stop"
	rommLiveSet("Putting files back…", 0, 0, 0)
	journal := rommJournalPath()
	go func() {
		res, err := rommUndoLast(journal)
		rommAfterMoves(res.Moves)
		runs := rommUndoable(journal)
		tbxPost(func() {
			if gameLib.open {
				refreshGameLibrary()
			}
			if gen != rommUI.gen {
				return
			}
			rommUI.undoRuns = runs
			rommUI.phase = rommPhaseDone
			rommUI.doneMoved = false
			rommUI.doneErrors = res.Errors
			rommUI.nav.focus = "rescan"
			if err != nil && res.Restored == 0 {
				rommUI.doneTitle = "Nothing was put back"
				rommUI.doneSub = firstLine(err.Error())
				PlayErrorSound()
				return
			}
			rommUI.doneTitle = "Put back " + rommCount(res.Restored, "file", "files")
			rommUI.doneSub = "Everything the organise moved is where it was before"
			if res.Failed > 0 {
				rommUI.doneSub = rommCount(res.Failed, "file", "files") + " could not go back — they are listed below"
			}
			if res.Lost > 0 {
				rommUI.doneSub += " · " + rommCount(res.Lost, "deleted file", "deleted files") + " could not come back"
			}
			PlaySuccessSound()
			ShowToast(rommUI.doneTitle, ToastKindSuccess)
		})
	}()
}

// ── Input ───────────────────────────────────────────────────────────────────

// rommActivate presses the control with focus id.
func rommActivate(config *Config, id string) {
	switch id {
	case "":
		return
	case "stop":
		rommStop()
		return
	case "apply":
		rommConfirmApply(config)
		return
	case "rescan":
		PlayActivateSound()
		rommStartScan()
		return
	case "undo":
		PlayActivateSound()
		rommConfirmUndo(config)
		return
	case "library":
		openGameLibraryPanel()
		return
	}
	for i, sec := range rommUI.sections {
		if sec.id == id {
			rommToggleSection(i)
			return
		}
	}
	if len(id) > 2 && id[:2] == "it" {
		if idx, err := strconv.Atoi(id[2:]); err == nil {
			rommToggleItem(idx)
		}
	}
}

// rommOnAction is the page's input, whatever device it came from.
func rommOnAction(config *Config, act tbxAction) bool {
	nav := &rommUI.nav
	working := rommUI.phase == rommPhaseScanning || rommUI.phase == rommPhaseApplying
	switch act {
	case tbxActConfirm:
		if working {
			rommStop()
			return true
		}
		rommActivate(config, nav.focus)
		return true
	case tbxActX:
		if si := rommFocusedSection(); si >= 0 && rommUI.phase == rommPhaseReady {
			rommToggleSection(si)
		}
		return true
	case tbxActY:
		if rommUI.phase != rommPhaseApplying {
			PlayActivateSound()
			rommStartScan()
		}
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// rommSceneClick routes a click or a tap on the page: the sheet first, then
// the control under the pointer. A miss is left for the bars.
func rommSceneClick(mx, my int32, config *Config) bool {
	if tbxCurrentScene(config) != rommSceneName {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := rommUI.nav.hit(mx, my)
	if !ok {
		return false
	}
	rommUI.nav.focus = it.id
	rommActivate(config, it.id)
	return true
}

// rommSceneWheel scrolls the page, or its open sheet.
func rommSceneWheel(config *Config, y int32) bool {
	if y == 0 || tbxCurrentScene(config) != rommSceneName {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	rommUI.nav.scrollBy(-y * 48)
	return true
}

// rommFooterHints is the footer while the page is on screen: what A does on
// the focused control, and the page's other keys.
func rommFooterHints(config *Config) []FooterHint {
	if tbxSheetVisible(config) {
		return tbxSheetHints()
	}
	if rommUI.phase == rommPhaseScanning || rommUI.phase == rommPhaseApplying {
		return []FooterHint{{Button: "A", Label: "Stop"}, {Button: "B", Label: "Back"}}
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if label := rommConfirmLabel(); label != "" {
		hints = append(hints, FooterHint{Button: "A", Label: label})
	}
	hints = append(hints, FooterHint{Button: "B", Label: "Back"})
	if rommUI.phase == rommPhaseReady && rommFocusedSection() >= 0 {
		hints = append(hints, FooterHint{Button: "X", Label: "Whole group"})
	}
	return append(hints, FooterHint{Button: "Y", Label: "Rescan"}, FooterHint{Button: "L1/R1", Label: "Page"})
}

// rommConfirmLabel names what A does on the focused control.
func rommConfirmLabel() string {
	switch rommUI.nav.focus {
	case "apply":
		return "Apply"
	case "rescan":
		return "Rescan"
	case "undo":
		return "Undo"
	case "library":
		return "Games library"
	case "":
		return ""
	}
	if si := rommFocusedSection(); si >= 0 {
		if rommUI.sections[si].kind == rommKindUnknown {
			return "Show path"
		}
		if rommUI.sections[si].id == rommUI.nav.focus {
			return "Whole group"
		}
		return "Switch"
	}
	return ""
}

// ── Drawing ─────────────────────────────────────────────────────────────────

// rommHeader is the page bar, with what the page is doing at the right.
func rommHeader() UIPageHeader {
	status := ""
	switch rommUI.phase {
	case rommPhaseScanning:
		status = "Scanning…"
	case rommPhaseApplying:
		status = "Working…"
	case rommPhaseReady:
		changes, _, _ := rommPlanCounts(rommUI.plan.Items)
		status = rommCount(changes, "change", "changes") + " · " + strconv.Itoa(rommUI.plan.InPlace) + " in place"
	case rommPhaseDone:
		status = "Done"
	}
	return tbxHeader("ROM manager", "Finds games outside their console's folder and files them where the library looks",
		"rom", status, ColorTextTertiary())
}

// rommItemRowH is the height of one file row.
func rommItemRowH(fonts tbxFonts) int32 {
	h := fonts.bodyH + fonts.smallH + 24
	if h < 56 {
		h = 56
	}
	return h
}

// rommOnScreen reports whether a row at screen y with height h shows in body.
func rommOnScreen(y, h int32, body sdl.Rect) bool {
	return y+h >= body.Y && y <= body.Y+body.H
}

// rommRender draws the page.
func rommRender(renderer *sdl.Renderer, config *Config) {
	if rommUI.visit.begin() {
		rommEnter()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, rommHeader())
	body := frame.bodyRect()
	nav := &rommUI.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	switch rommUI.phase {
	case rommPhaseScanning, rommPhaseApplying:
		rommDrawWorking(renderer, config, fonts, frame, body)
	case rommPhaseDone:
		rommDrawDone(renderer, config, fonts, frame, body)
	default:
		rommDrawPlan(renderer, config, fonts, frame, body)
	}
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// rommDrawCard draws the summary card at body y: an icon badge, a title, a line
// under it, an optional progress bar and a row of buttons, which join the focus
// list as row 0. It returns the card's height.
func rommDrawCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32,
	icon, title, sub string, progress float64, buttons []rommButton) int32 {
	nav := &rommUI.nav
	const pad = int32(18)
	const btnH = int32(46)
	badge := int32(48)
	textH := fonts.titleH + 6 + fonts.smallH
	h := pad + max32(badge, textH)
	if progress >= 0 {
		h += 14 + 8
	}
	if len(buttons) > 0 {
		h += 18 + btnH
	}
	h += pad
	card := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: h}
	UICard(renderer, card, DefaultUICardStyle())
	box := drawGlassBadge(renderer, sdl.Rect{X: card.X + pad, Y: card.Y + pad, W: badge, H: badge})
	DrawHomeIcon(renderer, icon, box, false)
	tx := card.X + pad + badge + 16
	tw := card.X + card.W - pad - tx
	ty := card.Y + pad + (badge-textH)/2
	if ty < card.Y+pad {
		ty = card.Y + pad
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, tw), ColorTextPrimary(), tx, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, tw), ColorTextMuted(), tx, ty+fonts.titleH+6)
	cy := card.Y + pad + max32(badge, textH)
	if progress >= 0 {
		cy += 14
		UIProgressBar(renderer, sdl.Rect{X: card.X + pad, Y: cy, W: card.W - 2*pad, H: 8}, progress, glassA(24), glassAccent())
		cy += 8
	}
	if len(buttons) > 0 {
		cy += 18
		bx := card.X + pad
		for _, b := range buttons {
			bw := tbxButtonWidth(fonts.body, b.label, 120)
			if bx+bw > card.X+card.W-pad {
				break // the compact canvas: the rest is still on the keys
			}
			screen := sdl.Rect{X: bx, Y: cy, W: bw, H: btnH}
			tbxDrawButton(renderer, config, fonts.body, screen, b.label, b.primary, nav.focused(b.id), b.disabled)
			nav.add(b.id, sdl.Rect{X: bx, Y: screen.Y - top, W: bw, H: btnH}, 0, b.disabled)
			bx += bw + 12
		}
	}
	nav.extend(y + h)
	return h
}

// rommDrawWorking draws the progress card of a scan or an organise.
func rommDrawWorking(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect) {
	top := body.Y - rommUI.nav.scroll
	stage, files, done, total := rommLiveGet()
	title := "Looking for games…"
	icon := "scan"
	if rommUI.phase == rommPhaseApplying {
		title = "Organising your games…"
		icon = "rom"
	}
	sub := stage
	progress := float64(-1)
	switch {
	case total > 0:
		sub = stage + " " + strconv.Itoa(done) + " of " + strconv.Itoa(total)
		progress = float64(done) / float64(total)
	case files > 0:
		sub = stage + " " + rommCount(files, "file", "files") + " so far"
	}
	buttons := []rommButton{{id: "stop", label: "Stop", primary: true}}
	h := rommDrawCard(renderer, config, fonts, frame, top, 0, icon, title, sub, progress, buttons)
	note := "Leaving this page stops the scan; nothing has been changed yet."
	if rommUI.phase == rommPhaseApplying {
		note = "Stop finishes the file in hand and keeps a record, so Undo can put back what was done."
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, note, frame.w), ColorTextTertiary(), frame.x, top+h+14)
	rommUI.nav.extend(h + 14 + fonts.smallH)
}

// rommDrawDone draws the result of an organise or an undo.
func rommDrawDone(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect) {
	top := body.Y - rommUI.nav.scroll
	buttons := make([]rommButton, 0, 3)
	if rommUI.undoRuns > 0 && rommUI.doneMoved {
		buttons = append(buttons, rommButton{id: "undo", label: "Undo"})
	}
	buttons = append(buttons, rommButton{id: "rescan", label: "Scan again"}, rommButton{id: "library", label: "Games library", primary: true})
	icon := "check"
	if len(rommUI.doneErrors) > 0 {
		icon = "warning"
	}
	h := rommDrawCard(renderer, config, fonts, frame, top, 0, icon, rommUI.doneTitle, rommUI.doneSub, -1, buttons)
	y := h + 16
	lineH := fonts.smallH + 8
	for _, msg := range rommUI.doneErrors {
		if rommOnScreen(top+y, lineH, body) {
			renderText(renderer, config, fonts.small, ellipsize(fonts.small, "• "+msg, frame.w), ToastWarnInk(), frame.x, top+y)
		}
		y += lineH
	}
	rommUI.nav.extend(y)
}

// rommDrawPlan draws the summary card and the plan's sections.
func rommDrawPlan(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect) {
	nav := &rommUI.nav
	top := body.Y - nav.scroll
	plan := &rommUI.plan
	changes, _, _ := rommPlanCounts(plan.Items)
	title := rommCount(changes, "change ready", "changes ready")
	icon := "rom"
	if len(plan.Items) == 0 {
		title = "Everything is in its place"
		icon = "check"
	} else if changes == 0 {
		title = "Nothing switched on"
	}
	sub := rommCount(plan.InPlace, "game", "games") + " already in place · " +
		rommCount(plan.Scanned, "file", "files") + " checked in " + rommDuration(plan.Took)
	if plan.Partial {
		sub = "Stopped early · " + sub
	}
	if rommUI.note != "" {
		sub = rommUI.note
	}
	buttons := make([]rommButton, 0, 3)
	applyLabel := "Apply"
	if changes > 0 {
		applyLabel = "Apply " + strconv.Itoa(changes)
	}
	if len(plan.Items) > 0 {
		buttons = append(buttons, rommButton{id: "apply", label: applyLabel, primary: true, disabled: changes == 0})
	}
	buttons = append(buttons, rommButton{id: "rescan", label: "Rescan"})
	if rommUI.undoRuns > 0 {
		buttons = append(buttons, rommButton{id: "undo", label: "Undo last organise"})
	}
	y := rommDrawCard(renderer, config, fonts, frame, top, 0, icon, title, sub, -1, buttons)
	if len(plan.Items) == 0 {
		y += 16
		hint := "Copy games anywhere — Downloads, the card's root, another console's folder — and this page files them."
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, frame.w), ColorTextTertiary(), frame.x, top+y)
		nav.extend(y + fonts.smallH)
		return
	}
	secH := tbxRowHeight(fonts)
	itemH := rommItemRowH(fonts)
	row := 1
	for _, sec := range rommUI.sections {
		y += 16
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: secH}
		if rommOnScreen(top+y, secH, body) {
			screen := rect
			screen.Y += top
			spec := tbxRowSpec{icon: sec.icon, title: sec.title, subtitle: sec.sub, pill: sec.pill, tone: sec.tone}
			tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(sec.id))
		}
		nav.add(sec.id, rect, row, false)
		row++
		y += secH + 8
		for k, idx := range sec.items {
			rect := sdl.Rect{X: frame.x + 28, Y: y, W: frame.w - 28, H: itemH}
			if rommOnScreen(top+y, itemH, body) {
				screen := rect
				screen.Y += top
				rommDrawItemRow(renderer, config, fonts, screen, plan.Items[idx], nav.focused(sec.itemIDs[k]))
			}
			nav.add(sec.itemIDs[k], rect, row, false)
			row++
			y += itemH + 6
		}
	}
	nav.extend(y + 8)
}

// rommDrawItemRow draws one file: its name over the reason, and the switch (a
// "Left alone" pill for files that are only listed).
func rommDrawItemRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, it rommItem, focused bool) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	cy := rect.Y + rect.H/2
	right := rect.X + rect.W - 16
	if it.Action == rommActNone {
		p := tbxDrawPill(renderer, config, fonts.tiny, right, cy, "Left alone", tbxToneNeutral)
		right = p.X - 12
	} else {
		t := tbxDrawToggle(renderer, right, cy, it.On)
		right = t.X - 14
		if it.Action == rommActDelete && it.On {
			p := tbxDrawPill(renderer, config, fonts.tiny, right, cy, "Deletes", tbxToneBad)
			right = p.X - 12
		}
	}
	x := rect.X + 16
	if it.Size > 0 {
		size := humanBytes(it.Size)
		if sw, sh := measureText(fonts.small, size); sw < right-x-160 {
			renderText(renderer, config, fonts.small, size, ColorTextTertiary(), right-sw, cy-sh/2)
			right -= sw + 14
		}
	}
	textW := right - x
	if textW < 40 {
		textW = 40
	}
	block := fonts.bodyH + 3 + fonts.smallH
	ty := rect.Y + (rect.H-block)/2
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, filepath.Base(it.Path), textW), ColorTextPrimary(), x, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, it.Reason, textW), ColorTextMuted(), x, ty+fonts.bodyH+3)
}

// rommDuration says how long a scan took, the way a person would.
func rommDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "under a second"
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " s"
	}
	return strconv.Itoa(int(d/time.Minute)) + " min " + strconv.Itoa(int(d%time.Minute/time.Second)) + " s"
}
