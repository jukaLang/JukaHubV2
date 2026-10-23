package main

// memtool.go — the Memory panel: a RAM scanner and editor for one running
// process.
//
// The scanner core lives in ramsearch.go; this file is the modal that drives
// it. The flow is the one a cheat table is built with: pick the game, search
// for a value you can see on screen (health, ammo, coins), change it in the
// game, narrow to the addresses that followed the change, then write a new
// value to the address you picked.
//
// A session attaches to the target with ptrace, which stops it, so the panel
// detaches when it closes and the game keeps running. The candidate list
// survives that, so "scan, play until the value changes, narrow" works without
// repeating the first scan.
//
// Nothing here needs root and nothing leaves the device: it is a local
// debugger, not a cheat downloader.

import (
	"fmt"
	"os"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type memPhase int

const (
	// memPhaseTarget picks the process to scan.
	memPhaseTarget memPhase = iota
	// memPhaseActions edits the value, width and scan.
	memPhaseActions
	// memPhaseResults lists the surviving addresses.
	memPhaseResults
)

// memWidths are the integer sizes a scan can look for, in the order the width
// row cycles through them.
var memWidths = []int{1, 2, 4, 8}

// memAction is one row of the actions phase.
type memAction int

const (
	memActionTarget memAction = iota
	memActionValue
	memActionWidth
	memActionScan
	memActionNarrow
	memActionResults
	memActionReset
)

// memRow is one line of the actions phase, built on demand so the labels
// always reflect the live session.
type memRow struct {
	label   string
	value   string
	detail  string
	action  memAction
	enabled bool
}

type memPanelState struct {
	open  bool
	phase memPhase

	// target phase
	procs        []procRow
	procCursor   int
	procLoadedAt time.Time
	procErr      string

	// session: pid survives closing the panel, access does not.
	pid     int32
	pidName string
	access  ramAccess
	openErr string

	width     int
	valueText string

	search  *ramSearch
	scanErr string

	actionCursor int
	resultCursor int

	confirmAddr uint64
	confirmAt   time.Time
}

var memPanel = memPanelState{width: 4}

// memValueBuffer holds the number while the on-screen keyboard edits it.
var memValueBuffer string

// Rect slices map a click back to a row; only touched on the render and event
// thread.
var memActionRects []sdl.Rect
var memResultRects []sdl.Rect
var memTargetRects []sdl.Rect
var memPanelRect sdl.Rect

const (
	// memProcLimit keeps the picker readable on a handheld screen.
	memProcLimit = 40
	// memProcRefresh reloads the picker while it is open.
	memProcRefresh = 3 * time.Second
	// memWriteConfirmWindow is how long the first A press stays armed.
	memWriteConfirmWindow = 5 * time.Second
	// memResultVisible is how many addresses the results table shows at once.
	memResultVisible = 10
)

// openMemPanel shows the panel. A previous target keeps its candidate list, so
// reopening after playing a game is how a narrow is done.
func openMemPanel() {
	// One modal at a time.
	qs.open = false
	toolsPanel.open = false
	processPanel.open = false
	powerTools.open = false
	memPanel.open = true
	memPanel.confirmAddr = 0
	memActionRects = nil
	memResultRects = nil
	memTargetRects = nil
	memPanelRect = sdl.Rect{}
	if memPanel.pid == 0 {
		memPanel.phase = memPhaseTarget
		memLoadProcs()
	} else {
		memPanel.phase = memPhaseActions
	}
	memClampCursors()
	PlayActivateSound()
}

// closeMemPanel hides the panel and detaches, which resumes the game. The
// target and its candidates are kept for the next open.
func closeMemPanel() {
	memReleaseAccess()
	memPanel.open = false
	memPanel.confirmAddr = 0
	PlayBackSound()
}

// memReleaseAccess closes the session, detaching from the target.
func memReleaseAccess() {
	if memPanel.access != nil {
		_ = memPanel.access.Close()
		memPanel.access = nil
	}
}

// memResetSession forgets the target and every candidate.
func memResetSession() {
	memReleaseAccess()
	memPanel.pid = 0
	memPanel.pidName = ""
	memPanel.search = nil
	memPanel.scanErr = ""
	memPanel.valueText = ""
	memPanel.width = 4
	memPanel.resultCursor = 0
	memPanel.openErr = ""
	memPanel.phase = memPhaseTarget
	memLoadProcs()
}

// memEnsureAccess opens the process-memory session on demand. It is lazy so a
// picked target that is never scanned does not freeze the game.
func memEnsureAccess() (ramAccess, bool) {
	if memPanel.access != nil {
		return memPanel.access, true
	}
	if memPanel.pid <= 0 {
		ShowToast("Pick a target process first", ToastKindWarning)
		return nil, false
	}
	access, err := ramOpenProcess(int(memPanel.pid))
	if err != nil {
		memPanel.openErr = err.Error()
		ShowToast("Cannot read that process: "+firstLine(err.Error()), ToastKindError)
		return nil, false
	}
	memPanel.access = access
	memPanel.openErr = ""
	return access, true
}

// --- Target phase -----------------------------------------------------------

// memLoadProcs refreshes the process picker.
func memLoadProcs() {
	rows, err := collectProcesses(memProcLimit)
	if err != nil {
		memPanel.procErr = err.Error()
		return
	}
	memPanel.procErr = ""
	memPanel.procs = rows
	memPanel.procLoadedAt = time.Now()
	memClampCursors()
}

// memSelfPID is the app's own process. Attaching to it would freeze the panel
// that is drawing the scan.
func memSelfPID() int32 { return int32(os.Getpid()) }

// memChooseTarget opens a session on the selected process.
func memChooseTarget() {
	if memPanel.procCursor < 0 || memPanel.procCursor >= len(memPanel.procs) {
		return
	}
	row := memPanel.procs[memPanel.procCursor]
	if row.PID == memSelfPID() {
		ShowToast("JukaHub cannot scan its own memory", ToastKindWarning)
		return
	}
	pid, err := ramOpenProcess(int(row.PID))
	if err != nil {
		memPanel.openErr = err.Error()
		ShowToast("Cannot read "+row.Name+": "+firstLine(err.Error()), ToastKindError)
		return
	}
	memPanel.access = pid
	memPanel.openErr = ""
	memPanel.pid = row.PID
	memPanel.pidName = row.Name
	memPanel.search = nil
	memPanel.resultCursor = 0
	memPanel.phase = memPhaseActions
	memPanel.actionCursor = 0
	PlaySuccessSound()
	ShowToast(fmt.Sprintf("Scanning %s (PID %d)", row.Name, row.PID), ToastKindSuccess)
}

// --- Actions phase ----------------------------------------------------------

// memoryMenuValue is the Power tools menu line, so the last target is visible
// before the panel is opened.
func memoryMenuValue() string {
	if memPanel.pid > 0 {
		return fmt.Sprintf("Last: %s (PID %d)", memPanel.pidName, memPanel.pid)
	}
	return "RAM scan & edit"
}

// memRows builds the actions list for the live session.
func memRows() []memRow {
	targetValue := "No target"
	targetDetail := "Press A to pick a running process"
	if memPanel.pid > 0 {
		targetValue = fmt.Sprintf("%s (PID %d)", memPanel.pidName, memPanel.pid)
		targetDetail = "Press A to pick another process"
	}
	if memPanel.openErr != "" {
		targetDetail = firstLine(memPanel.openErr)
	}

	scanValue := "Search for the value"
	scanDetail := "Walks every writable region once"
	if memPanel.pid == 0 {
		scanDetail = "Pick a target process first"
	}
	matches := 0
	if memPanel.search != nil {
		matches = len(memPanel.search.Candidates)
	}

	return []memRow{
		{label: "Target", value: targetValue, detail: targetDetail, action: memActionTarget, enabled: true},
		{label: "Value", value: memValueLabel(), detail: "Decimal, or 0x hex", action: memActionValue, enabled: true},
		{label: "Width", value: memWidthLabel(), detail: "Left/Right to change", action: memActionWidth, enabled: true},
		{label: "Scan", value: scanValue, detail: scanDetail, action: memActionScan, enabled: memPanel.pid > 0},
		{label: "Narrow", value: "Keep only the new value", detail: "Change the value in game, then narrow", action: memActionNarrow, enabled: memPanel.search != nil},
		{label: "Results", value: fmt.Sprintf("%d address(es)", matches), detail: "Browse and write to a match", action: memActionResults, enabled: memPanel.search != nil},
		{label: "Reset", value: "Clear the scan", detail: "Start over with a new target", action: memActionReset, enabled: memPanel.search != nil || memPanel.pid > 0},
	}
}

// memValueLabel describes the number to search for or write.
func memValueLabel() string {
	raw := memPanel.valueText
	if raw == "" {
		return "Not set — press A"
	}
	v, ok := ramParseValue(raw)
	if !ok {
		return raw + "  (not a number)"
	}
	v = ramMask(memPanel.width, v)
	return fmt.Sprintf("%d  (%s)", v, ramFormatAddr(v))
}

// memWidthLabel names the search width.
func memWidthLabel() string {
	if memPanel.width == 1 {
		return "1 byte"
	}
	return fmt.Sprintf("%d bytes", memPanel.width)
}

// memCurrentValue is the parsed value field, truncated to the search width.
func memCurrentValue() (uint64, bool) {
	v, ok := ramParseValue(memPanel.valueText)
	if !ok {
		return 0, false
	}
	return ramMask(memPanel.width, v), true
}

// memCycleWidth moves through the supported widths. A different width makes the
// old addresses meaningless, so the scan is dropped.
func memCycleWidth(delta int) {
	idx := 0
	for i, w := range memWidths {
		if w == memPanel.width {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(memWidths)) % len(memWidths)
	memPanel.width = memWidths[idx]
	if memPanel.search != nil {
		memPanel.search = nil
		memPanel.resultCursor = 0
		memPanel.scanErr = ""
	}
	PlayNavSound()
}

// memStartScan runs the first pass and reports what it found.
func memStartScan() {
	value, ok := memCurrentValue()
	if !ok {
		ShowToast("Enter a number to search for", ToastKindWarning)
		return
	}
	access, ok := memEnsureAccess()
	if !ok {
		return
	}
	search, err := ramFirstScan(access, memPanel.width, value)
	if err != nil {
		memPanel.scanErr = err.Error()
		ShowToast("Scan failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	memPanel.scanErr = ""
	memPanel.search = search
	memPanel.resultCursor = 0
	if len(search.Candidates) == 0 {
		ShowToast("No address holds that value", ToastKindInfo)
		return
	}
	PlaySuccessSound()
	ShowToast(fmt.Sprintf("%d address(es) match", len(search.Candidates)), ToastKindSuccess)
}

// memNarrowScan keeps the addresses whose value is now the one in the field.
func memNarrowScan() {
	if memPanel.search == nil {
		ShowToast("Run a scan first", ToastKindWarning)
		return
	}
	value, ok := memCurrentValue()
	if !ok {
		ShowToast("Enter the value the game shows now", ToastKindWarning)
		return
	}
	access, ok := memEnsureAccess()
	if !ok {
		return
	}
	before := len(memPanel.search.Candidates)
	ramNarrow(access, memPanel.search, value)
	after := len(memPanel.search.Candidates)
	memPanel.resultCursor = 0
	switch {
	case after == 0:
		ShowToast("Nothing left — the value did not change as expected", ToastKindWarning)
	case after == 1:
		PlaySuccessSound()
		ShowToast("One address left — press A on Results to edit it", ToastKindSuccess)
	default:
		ShowToast(fmt.Sprintf("%d of %d address(es) kept", after, before), ToastKindInfo)
	}
}

// memWriteSelected writes the value field into the selected address. The first
// press arms, the second writes, because editing live memory cannot be undone
// by closing a dialog.
func memWriteSelected() {
	if memPanel.search == nil || len(memPanel.search.Candidates) == 0 {
		return
	}
	if memPanel.resultCursor < 0 || memPanel.resultCursor >= len(memPanel.search.Candidates) {
		return
	}
	cand := &memPanel.search.Candidates[memPanel.resultCursor]
	value, ok := memCurrentValue()
	if !ok {
		ShowToast("Enter the value to write", ToastKindWarning)
		return
	}
	if !(memPanel.confirmAddr == cand.Addr && time.Since(memPanel.confirmAt) <= memWriteConfirmWindow) {
		memPanel.confirmAddr = cand.Addr
		memPanel.confirmAt = time.Now()
		PlayNavSound()
		ShowToast(fmt.Sprintf("Press A again to write %d to %s", value, ramFormatAddr(cand.Addr)), ToastKindWarning)
		return
	}
	memPanel.confirmAddr = 0
	access, ok := memEnsureAccess()
	if !ok {
		return
	}
	if err := ramWrite(access, memPanel.width, cand.Addr, value); err != nil {
		ShowToast("Write failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	cand.Value = value
	PlaySuccessSound()
	ShowToast(fmt.Sprintf("Wrote %d to %s", value, ramFormatAddr(cand.Addr)), ToastKindSuccess)
}

// memActivateAction runs the focused actions row.
func memActivateAction() {
	rows := memRows()
	if memPanel.actionCursor < 0 || memPanel.actionCursor >= len(rows) {
		return
	}
	row := rows[memPanel.actionCursor]
	if !row.enabled {
		ShowToast(row.label+" is not available yet", ToastKindInfo)
		return
	}
	PlayActivateSound()
	switch row.action {
	case memActionTarget:
		memPanel.phase = memPhaseTarget
		memLoadProcs()
	case memActionValue:
		memOpenValueKeyboard()
	case memActionWidth:
		memCycleWidth(1)
	case memActionScan:
		memStartScan()
	case memActionNarrow:
		memNarrowScan()
	case memActionResults:
		if memPanel.search == nil {
			return
		}
		memPanel.phase = memPhaseResults
		memClampCursors()
	case memActionReset:
		memResetSession()
	}
}

// memOpenValueKeyboard edits the value field with the on-screen keyboard. It
// starts on the digits layer, since every value here is a number.
func memOpenValueKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetMemory
	vkLayer = vkLayerSymbols
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	memValueBuffer = memPanel.valueText
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// memApplyValueInput stores a submitted value field.
func memApplyValueInput(text string) {
	memPanel.valueText = text
}

// --- Navigation -------------------------------------------------------------

func memClampCursors() {
	if memPanel.procCursor >= len(memPanel.procs) {
		memPanel.procCursor = len(memPanel.procs) - 1
	}
	if memPanel.procCursor < 0 {
		memPanel.procCursor = 0
	}
	rows := len(memRows())
	if memPanel.actionCursor >= rows {
		memPanel.actionCursor = rows - 1
	}
	if memPanel.actionCursor < 0 {
		memPanel.actionCursor = 0
	}
	candidates := 0
	if memPanel.search != nil {
		candidates = len(memPanel.search.Candidates)
	}
	if memPanel.resultCursor >= candidates {
		memPanel.resultCursor = candidates - 1
	}
	if memPanel.resultCursor < 0 {
		memPanel.resultCursor = 0
	}
}

// memMove moves the cursor of the current phase.
func memMove(delta int) {
	switch memPanel.phase {
	case memPhaseTarget:
		if len(memPanel.procs) == 0 {
			return
		}
		memPanel.procCursor += delta
		if memPanel.procCursor < 0 {
			memPanel.procCursor = len(memPanel.procs) - 1
		}
		if memPanel.procCursor >= len(memPanel.procs) {
			memPanel.procCursor = 0
		}
	case memPhaseActions:
		rows := len(memRows())
		if rows == 0 {
			return
		}
		memPanel.actionCursor = (memPanel.actionCursor + delta + rows) % rows
	case memPhaseResults:
		candidates := 0
		if memPanel.search != nil {
			candidates = len(memPanel.search.Candidates)
		}
		if candidates == 0 {
			return
		}
		memPanel.resultCursor += delta
		if memPanel.resultCursor < 0 {
			memPanel.resultCursor = 0
		}
		if memPanel.resultCursor >= candidates {
			memPanel.resultCursor = candidates - 1
		}
	}
	memPanel.confirmAddr = 0
	PlayNavSound()
}

// memBack leaves the results table, or closes the panel from the top level.
func memBack() {
	switch memPanel.phase {
	case memPhaseResults:
		memPanel.phase = memPhaseActions
		PlayBackSound()
	case memPhaseTarget:
		if memPanel.pid > 0 {
			memPanel.phase = memPhaseActions
			PlayBackSound()
			return
		}
		closeMemPanel()
	default:
		closeMemPanel()
	}
}

// --- Input ------------------------------------------------------------------

// memKeyScroll is the page step for PageUp/PageDown.
const memKeyScroll = 10

// handleMemKey consumes every key while the panel is open so the scene
// underneath never reacts to typing behind the modal. While the value keyboard
// is up it owns the keys and this yields.
func handleMemKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !memPanel.open {
		return false
	}
	if virtualKeyboardActive {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		memMove(-1)
	case sdl.K_DOWN:
		memMove(1)
	case sdl.K_PAGEUP:
		memMove(-memKeyScroll)
	case sdl.K_PAGEDOWN:
		memMove(memKeyScroll)
	case sdl.K_LEFT:
		if memPanel.phase == memPhaseActions {
			memCycleWidth(-1)
		}
	case sdl.K_RIGHT:
		if memPanel.phase == memPhaseActions {
			memCycleWidth(1)
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		memPanelActivate()
	case sdl.K_y:
		if memPanel.search != nil {
			memNarrowScan()
		}
	case sdl.K_r:
		if memPanel.phase == memPhaseTarget {
			memLoadProcs()
			PlayNavSound()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		memBack()
	}
	return true
}

// memPanelActivate runs the focused row of the current phase.
func memPanelActivate() {
	switch memPanel.phase {
	case memPhaseTarget:
		memChooseTarget()
	case memPhaseResults:
		memWriteSelected()
	default:
		memActivateAction()
	}
}

// ctrlMemPanel is called before every other controller overlay.
func ctrlMemPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	if virtualKeyboardActive {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		memMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		memMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if memPanel.phase == memPhaseActions {
			memCycleWidth(-1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if memPanel.phase == memPhaseActions {
			memCycleWidth(1)
		}
	case sdl.CONTROLLER_BUTTON_A:
		memPanelActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		if memPanel.search != nil {
			memNarrowScan()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		memBack()
	}
	return true
}

// handleMemMouse selects and activates a clicked row, or closes the panel when
// the click lands outside it.
func handleMemMouse(mx, my int32, config *Config) bool {
	if !memPanel.open {
		return false
	}
	rows := memActionRects
	switch memPanel.phase {
	case memPhaseTarget:
		rows = memTargetRects
	case memPhaseResults:
		rows = memResultRects
	}
	first := memListFirst()
	for i, r := range rows {
		if r.W > 0 && pointInRect(mx, my, r) {
			switch memPanel.phase {
			case memPhaseTarget:
				memPanel.procCursor = first + i
			case memPhaseResults:
				memPanel.resultCursor = first + i
			default:
				memPanel.actionCursor = i
			}
			memPanelActivate()
			return true
		}
	}
	if memPanelRect.W > 0 && !pointInRect(mx, my, memPanelRect) {
		memBack()
	}
	return true
}

// --- Render -----------------------------------------------------------------

// memListFirst is the index of the first visible row, so the cursor stays in
// view in a long list.
func memListFirst() int {
	switch memPanel.phase {
	case memPhaseTarget:
		visible := memPanelVisible()
		if memPanel.procCursor >= visible {
			return memPanel.procCursor - visible + 1
		}
	case memPhaseResults:
		if memPanel.resultCursor >= memResultVisible {
			return memPanel.resultCursor - memResultVisible + 1
		}
	}
	return 0
}

// memPanelVisible is how many rows fit at the current screen size.
func memPanelVisible() int {
	if screenHeight < 700 {
		return 10
	}
	return 12
}

// renderMemPanel draws the modal over the current scene.
func renderMemPanel(renderer *sdl.Renderer, config *Config) {
	if !memPanel.open || renderer == nil {
		return
	}
	// The picker reloads on a timer instead of every frame: enumerating /proc
	// in a render loop is measurable on a handheld.
	if memPanel.phase == memPhaseTarget &&
		(memPanel.procLoadedAt.IsZero() || time.Since(memPanel.procLoadedAt) > memProcRefresh) {
		memLoadProcs()
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	const padX = int32(24)
	lineH := smallH + 8
	visible := memPanelVisible()

	headerH := 18 + medH + 4 + smallH + 10
	footerH := 20 + smallH

	var bodyH int32
	switch memPanel.phase {
	case memPhaseTarget:
		n := len(memPanel.procs)
		if n > visible {
			n = visible
		}
		if n < 1 {
			n = 1
		}
		bodyH = smallH + 8 + int32(n)*lineH
	case memPhaseResults:
		n := 0
		if memPanel.search != nil {
			n = len(memPanel.search.Candidates)
		}
		if n > memResultVisible {
			n = memResultVisible
		}
		if n < 1 {
			n = 1
		}
		bodyH = smallH + 8 + int32(n)*lineH
	default:
		bodyH = int32(len(memRows())) * lineH
	}

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + bodyH + footerH
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	memPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, memTitle(), ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, ellipsize(font, memSubtitle(), panelW-2*padX), ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	bodyY := panelY + headerH
	switch memPanel.phase {
	case memPhaseTarget:
		renderMemTargetBody(renderer, config, font, panelX, bodyY, panelW, padX, smallH, lineH, visible)
	case memPhaseResults:
		renderMemResultBody(renderer, config, font, panelX, bodyY, panelW, padX, smallH, lineH)
	default:
		renderMemActionBody(renderer, config, font, panelX, bodyY, panelW, padX, lineH)
	}

	footer := memFooter()
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}

// memTitle is the panel heading for the current phase.
func memTitle() string {
	switch memPanel.phase {
	case memPhaseTarget:
		return "Memory · pick a process"
	case memPhaseResults:
		return "Memory · addresses"
	}
	return "Memory"
}

// memSubtitle is the one-line status under the heading.
func memSubtitle() string {
	switch memPanel.phase {
	case memPhaseTarget:
		if memPanel.procErr != "" {
			return "Process list unavailable: " + firstLine(memPanel.procErr)
		}
		return fmt.Sprintf("%d running · pick the game you want to scan", len(memPanel.procs))
	case memPhaseResults:
		if memPanel.search == nil {
			return "No scan"
		}
		// A narrow can empty the list, so the first address is only shown when
		// there is one.
		if n := len(memPanel.search.Candidates); n > 0 {
			return fmt.Sprintf("%d address(es) · %s · first %s", n,
				memWidthLabel(), ramFormatAddr(memPanel.search.Candidates[0].Addr))
		}
		return "0 addresses · the narrow emptied the list"
	}
	if memPanel.pid == 0 {
		return "No target · pick a process first"
	}
	if memPanel.scanErr != "" {
		return "Last scan failed: " + firstLine(memPanel.scanErr)
	}
	if memPanel.search != nil {
		return fmt.Sprintf("%s (PID %d) · %d address(es) left", memPanel.pidName, memPanel.pid, len(memPanel.search.Candidates))
	}
	return fmt.Sprintf("%s (PID %d) · no scan yet", memPanel.pidName, memPanel.pid)
}

// memFooter is the hint line for the current phase.
func memFooter() string {
	switch memPanel.phase {
	case memPhaseTarget:
		return hintLine(FooterHint{"A", "Scan This"}, FooterHint{"Y", "Refresh"}, FooterHint{"B", "Back"})
	case memPhaseResults:
		if memPanel.confirmAddr != 0 {
			return hintLine(FooterHint{"A", "Confirm Write"}, FooterHint{"B", "Cancel"})
		}
		return hintLine(FooterHint{"A", "Write Value"}, FooterHint{"Y", "Narrow"}, FooterHint{"B", "Back"})
	}
	return hintLine(FooterHint{"A", "Select"}, FooterHint{"←→", "Width"}, FooterHint{"B", "Close"})
}

// renderMemTargetBody draws the process picker.
func renderMemTargetBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, bodyY, panelW, padX, smallH, lineH int32, visible int) {
	colPID := panelX + padX
	colName := colPID + 90
	renderText(renderer, config, font, "PID", ColorTextTertiary(), colPID, bodyY)
	renderText(renderer, config, font, "NAME", ColorTextTertiary(), colName, bodyY)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	memTargetRects = memTargetRects[:0]
	rowY := bodyY + smallH + 8
	first := memListFirst()
	for i := 0; i < visible; i++ {
		idx := first + i
		if idx >= len(memPanel.procs) {
			break
		}
		row := memPanel.procs[idx]
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		memTargetRects = append(memTargetRects, rect)
		focused := idx == memPanel.procCursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, focusColor)
		}
		textColor := ColorTextSecondary()
		if focused {
			textColor = ColorTextPrimary()
		}
		renderText(renderer, config, font, fmt.Sprintf("%d", row.PID), textColor, colPID, rowY+2)
		name := row.Name
		if row.PID == memSelfPID() {
			name += "  (this app)"
		}
		maxW := panelX + panelW - padX - colName
		renderText(renderer, config, font, ellipsize(font, name, maxW), textColor, colName, rowY+2)
		rowY += lineH
	}
	if len(memPanel.procs) == 0 {
		renderText(renderer, config, font, "No processes to show", ColorTextTertiary(), panelX+padX, rowY+2)
		rowY += lineH
	}
}

// renderMemActionBody draws the value/width/scan rows.
func renderMemActionBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, bodyY, panelW, padX, lineH int32) {
	rows := memRows()
	focusColor := installHighlight()
	rowW := panelW - 2*padX
	colLabel := panelX + padX
	colValue := panelX + padX + 150
	memActionRects = memActionRects[:0]
	rowY := bodyY
	for i, row := range rows {
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		memActionRects = append(memActionRects, rect)
		focused := i == memPanel.actionCursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, focusColor)
		}
		labelColor := ColorTextSecondary()
		valueColor := ColorTextPrimary()
		if !row.enabled {
			labelColor = ColorTextTertiary()
			valueColor = ColorTextTertiary()
		}
		if focused && row.enabled {
			labelColor = ColorTextPrimary()
		}
		renderText(renderer, config, font, row.label, labelColor, colLabel, rowY+2)
		renderText(renderer, config, font, row.value, valueColor, colValue, rowY+2)
		if focused && row.detail != "" {
			dw, _, _ := font.SizeUTF8(row.value)
			detailX := colValue + int32(dw) + 16
			maxW := panelX + panelW - padX - detailX
			if maxW > 40 {
				renderText(renderer, config, font, ellipsize(font, row.detail, maxW), ColorTextTertiary(), detailX, rowY+2)
			}
		}
		rowY += lineH
	}
}

// renderMemResultBody draws the candidate table with live values.
func renderMemResultBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, bodyY, panelW, padX, smallH, lineH int32) {
	colAddr := panelX + padX
	colValue := colAddr + 170
	colNote := colValue + 160
	valueHead := "VALUE NOW"
	if memPanel.access == nil {
		// Detached (the panel was reopened on a saved scan): the stored value is
		// the honest label, not a live read.
		valueHead = "VALUE (LAST)"
	}
	renderText(renderer, config, font, "ADDRESS", ColorTextTertiary(), colAddr, bodyY)
	renderText(renderer, config, font, valueHead, ColorTextTertiary(), colValue, bodyY)

	candidates := []ramCandidate(nil)
	if memPanel.search != nil {
		candidates = memPanel.search.Candidates
	}
	focusColor := installHighlight()
	rowW := panelW - 2*padX
	memResultRects = memResultRects[:0]
	rowY := bodyY + smallH + 8
	first := memListFirst()
	for i := 0; i < memResultVisible; i++ {
		idx := first + i
		if idx >= len(candidates) {
			break
		}
		cand := candidates[idx]
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		memResultRects = append(memResultRects, rect)
		focused := idx == memPanel.resultCursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, focusColor)
		}
		textColor := ColorTextSecondary()
		if focused {
			textColor = ColorTextPrimary()
		}
		renderText(renderer, config, font, ramFormatAddr(cand.Addr), textColor, colAddr, rowY+2)

		// The live value is what tells the user they found the right address:
		// spend coins and the number must drop with them. While the panel is
		// detached the value from the last scan is the best that is known.
		live := fmt.Sprintf("%d", cand.Value)
		if memPanel.access != nil {
			if v, err := ramReadValue(memPanel.access, memPanel.width, cand.Addr); err == nil {
				live = fmt.Sprintf("%d", v)
			} else {
				live = "unreadable"
			}
		}
		renderText(renderer, config, font, live, textColor, colValue, rowY+2)
		if focused && memPanel.confirmAddr == cand.Addr {
			renderText(renderer, config, font, "press A to confirm", ColorToastWarn, colNote, rowY+2)
		}
		rowY += lineH
	}
	if len(candidates) == 0 {
		renderText(renderer, config, font, "No addresses left — reset and scan again", ColorTextTertiary(), panelX+padX, rowY+2)
		rowY += lineH
	}
}
