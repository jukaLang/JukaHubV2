package main

// files_dualpane.go — Files in two panes: a dual-pane file manager.
//
// "Two panes" (Files → Y → Two panes, remembered as fe_dual_pane) shows two
// folder lists side by side, each with its own folder, cursor and marks. L1
// picks the left pane and R1 the right one (Tab switches on a keyboard), and
// the Y sheet gains Copy to other pane and Move to other pane, which send the
// marked rows — or the one under the cursor — to the other pane's folder on a
// background job (files_dualpane_xfer.go; F5 / F6 open those rows). One pane
// stays the default and is unchanged; in two panes L1/R1 choose a pane rather
// than page the list (L2 still jumps to the top).
//
// The pane being driven IS the ordinary Files state — fe_path, fe_entries,
// focusedFileIndex, filesNav and the marks — so every Files action works in it
// as before; the other pane is a snapshot of that state, swapped in when the
// user switches. Only drawing, the shoulders and a click in the other half
// know there are two. A Files visit opened to pick something (chat
// attachments, background music) stays in one pane.

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// filesDualSettingKey remembers the switch with the user's settings.
const filesDualSettingKey = "fe_dual_pane"

// The Y sheet's rows (feActions → filesDualSheetRows).
const (
	feActDualPane feActionKind = 1200
	feActDualCopy feActionKind = 1201
	feActDualMove feActionKind = 1202
)

// filesPaneSnap is a pane that is not being driven: the Files state it had.
type filesPaneSnap struct {
	valid        bool
	location     string
	atLocations  bool
	locations    []filesLocation
	path         string
	entries      []FileEntry
	focus        int
	listedPath   string
	listedRoot   string
	listErr      error
	hiddenCount  int
	systemFolder bool
	marks        map[string]bool
	// What the listing was read with: a change to Show hidden files or the
	// sort reads the pane again before it is drawn.
	hidden bool
	sort   feSortMode
	stale  bool
}

// filesDual is the two-pane state. Render thread.
var filesDual struct {
	// right is true while the right-hand pane is the one being driven.
	right bool
	other filesPaneSnap
	// The other pane as last drawn, for clicks: its bar, its list, the top of
	// its first row and the first row shown.
	otherBar   sdl.Rect
	otherList  sdl.Rect
	otherTop   int32
	otherStart int
	otherRows  int
}

// filesDualSetting reports the remembered switch.
func filesDualSetting(config *Config) bool {
	if config == nil || config.Variables.Custom == nil {
		return false
	}
	v, ok := config.Variables.Custom[filesDualSettingKey]
	return ok && coerceBool(v)
}

// filesDualOn reports whether Files is drawn in two panes right now.
func filesDualOn(config *Config) bool {
	return filesDualSetting(config) && !chatPickActive() && !bgmPickActive()
}

// filesDualSet switches two panes on or off and remembers it. Switched on, the
// second pane starts in the folder on screen.
func filesDualSet(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[filesDualSettingKey] = on
	saveUserConfigDebounced(userConfigFromVariables(config))
	filesDual.right = false
	filesDual.other = filesPaneSnap{}
	if on {
		filesDualEnsureOther(config)
	}
}

// filesDualCapture is the Files state on screen as a pane.
func filesDualCapture(config *Config) filesPaneSnap {
	path, _ := config.Variables.Custom["fe_path"].(string)
	return filesPaneSnap{
		valid:        true,
		location:     filesNav.location,
		atLocations:  filesNav.atLocations,
		locations:    filesNav.locations,
		path:         path,
		entries:      feEntriesOf(config),
		focus:        focusedFileIndex,
		listedPath:   filesNav.listedPath,
		listedRoot:   filesNav.listedRoot,
		listErr:      filesNav.listErr,
		hiddenCount:  filesNav.hiddenCount,
		systemFolder: filesNav.systemFolder,
		marks:        feSelectedPaths,
		hidden:       filesShowHidden(config),
		sort:         feSortModeOf(config),
	}
}

// filesDualRestore makes p the Files state on screen and reads its folder
// again, so a copy into it since shows.
func filesDualRestore(config *Config, p filesPaneSnap) {
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	filesNav.location = p.location
	filesNav.atLocations = p.atLocations
	filesNav.locations = p.locations
	config.Variables.Custom["fe_path"] = p.path
	config.Variables.Custom["fe_entries"] = p.entries
	filesNav.listedPath, filesNav.listedRoot = p.listedPath, p.listedRoot
	filesNav.listErr, filesNav.hiddenCount, filesNav.systemFolder = p.listErr, p.hiddenCount, p.systemFolder
	marks := p.marks
	if marks == nil {
		marks = map[string]bool{}
	}
	feSelectedPaths = marks
	focusedFileIndex = p.focus
	filesNav.restore = false
	feListDirectory(config)
}

// filesDualEnsureOther gives the second pane a folder the first time it is
// needed: the one on screen, with no marks.
func filesDualEnsureOther(config *Config) {
	if filesDual.other.valid || config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	snap := filesDualCapture(config)
	snap.marks = map[string]bool{}
	filesDual.other = snap
}

// filesDualRefreshOther reads the other pane's folder again (after a transfer,
// or a change to hidden files or the sort), keeping its cursor on the same row.
func filesDualRefreshOther(config *Config) {
	o := &filesDual.other
	o.stale = false
	o.hidden, o.sort = filesShowHidden(config), feSortModeOf(config)
	if !o.valid {
		return
	}
	if o.atLocations {
		o.locations = filesBuildLocations(config)
		list := make([]FileEntry, 0, len(o.locations))
		for _, loc := range o.locations {
			list = append(list, FileEntry{Name: loc.Label, Path: loc.Path, IsDir: true})
		}
		o.entries = list
		o.focus = clampIndex(o.focus, len(list))
		return
	}
	if o.listedPath == "" {
		return
	}
	focused := ""
	if o.focus >= 0 && o.focus < len(o.entries) {
		focused = o.entries[o.focus].Path
	}
	list, hidden, err := filesReadFolder(config, o.listedPath)
	feSortEntries(list, o.sort)
	o.entries, o.hiddenCount, o.listErr = list, hidden, err
	o.focus = clampIndex(o.focus, len(list))
	for i, entry := range list {
		if focused != "" && filesSamePath(entry.Path, focused) {
			o.focus = i
			break
		}
	}
	// Marks on files that are gone (moved away) are dropped.
	for marked := range o.marks {
		found := false
		for _, entry := range list {
			if entry.Path == marked {
				found = true
				break
			}
		}
		if !found {
			delete(o.marks, marked)
		}
	}
}

// filesDualFocusOther puts the other pane's cursor on path, if it is listed.
func filesDualFocusOther(path string) {
	for i, entry := range filesDual.other.entries {
		if filesSamePath(entry.Path, path) {
			filesDual.other.focus = i
			return
		}
	}
}

// filesDualSwitch drives the left (toRight false) or the right pane.
func filesDualSwitch(config *Config, toRight bool) bool {
	if !filesDualOn(config) || !feSceneActive(config) {
		return false
	}
	if toRight == filesDual.right {
		return true
	}
	filesDualEnsureOther(config)
	filesRememberFocus(config)
	// A system-folder confirm armed in one pane is not one for the other.
	feSheet.armed = ""
	live := filesDualCapture(config)
	filesDualRestore(config, filesDual.other)
	filesDual.other = live
	filesDual.right = toRight
	feFocusList(config)
	PlayNavSound()
	return true
}

// ── Input ────────────────────────────────────────────────────────────────────

// filesDualPad takes L1 (left pane) and R1 (right pane) in two panes; one pane
// leaves them paging the list.
func filesDualPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !filesDualOn(config) {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return filesDualSwitch(config, false)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return filesDualSwitch(config, true)
	}
	return false
}

// filesDualKey is the keyboard's half: Tab switches panes, F5 and F6 open the
// sheet on Copy / Move to other pane (Enter runs it).
func filesDualKey(key sdl.Keycode, config *Config) bool {
	if !filesDualOn(config) {
		return false
	}
	switch key {
	case sdl.K_TAB:
		return filesDualSwitch(config, !filesDual.right)
	case sdl.K_F5, sdl.K_F6:
		kind := feActDualCopy
		if key == sdl.K_F6 {
			kind = feActDualMove
		}
		openFeSheet(config)
		for i, a := range feActions(config) {
			if a.Kind == kind {
				feSheet.cursor = i
				break
			}
		}
		return true
	}
	return false
}

// filesDualMouse answers a press on the other pane: it becomes the driven one,
// with its cursor on the row pressed (a first press never opens anything).
func filesDualMouse(mx, my int32, config *Config) bool {
	if !filesDualOn(config) {
		return false
	}
	inBar := filesDual.otherBar.W > 0 && pointInRect(mx, my, filesDual.otherBar)
	inList := filesDual.otherList.W > 0 && pointInRect(mx, my, filesDual.otherList)
	if !inBar && !inList {
		return false
	}
	row := -1
	if inList && my >= filesDual.otherTop {
		if i := int((my - filesDual.otherTop) / dynamicListRowH); i < filesDual.otherRows {
			row = filesDual.otherStart + i
		}
	}
	if row >= len(filesDual.other.entries) {
		row = -1
	}
	filesDualSwitch(config, !filesDual.right)
	if row >= 0 {
		focusedFileIndex = clampIndex(row, len(feEntriesOf(config)))
	}
	return true
}

// filesDualFooterLabel is L1/R1's word in the Files footer.
func filesDualFooterLabel(config *Config) string {
	if filesDualOn(config) {
		return "Pane"
	}
	return "Page"
}

// ── The Y sheet ──────────────────────────────────────────────────────────────

// filesDualSheetRows adds the two-pane rows to the Files sheet: Copy and Move
// to other pane after Paste (in two panes), and the Two panes switch after
// Show hidden files.
func filesDualSheetRows(config *Config, actions []feAction, targets []FileEntry) []feAction {
	toggle := feAction{Kind: feActDualPane, Label: "Two panes", Detail: "Off", Enabled: true}
	switch {
	case chatPickActive() || bgmPickActive():
		toggle.Detail, toggle.Enabled = "not while picking", false
	case filesDualSetting(config):
		toggle.Detail = "On · L1/R1 pick a pane"
	}
	out := make([]feAction, 0, len(actions)+3)
	placed := false
	for _, a := range actions {
		out = append(out, a)
		switch a.Kind {
		case feActPaste:
			if filesDualOn(config) {
				out = append(out, filesDualTransferRows(config, targets)...)
			}
		case feActHidden:
			out = append(out, toggle)
			placed = true
		}
	}
	if !placed {
		out = append(out, toggle)
	}
	return out
}

// filesDualOtherName names the other pane's folder for a row's detail.
func filesDualOtherName() string {
	o := filesDual.other
	if o.atLocations || o.listedPath == "" {
		return "Locations"
	}
	if name := filepath.Base(o.listedPath); name != "" && name != "." && name != string(filepath.Separator) {
		return name
	}
	return feRootLabel(o.listedPath)
}

// filesDualTransferRows are Copy / Move to other pane, or the running
// transfer's progress (a press asks to stop it).
func filesDualTransferRows(config *Config, targets []FileEntry) []feAction {
	if s := filesDualSnapshot(); s.running {
		label := "Copying to other pane"
		if s.move {
			label = "Moving to other pane"
		}
		detail := fmt.Sprintf("%d of %d", min(s.done+1, s.total), s.total)
		if s.current != "" {
			detail += " · " + s.current
		}
		kind := feActDualCopy
		if s.move {
			kind = feActDualMove
		}
		return []feAction{{Kind: kind, Label: label, Detail: detail, Enabled: true}}
	}
	filesDualEnsureOther(config)
	o := filesDual.other
	detail := feItemCountLabel(len(targets)) + " → " + filesDualOtherName()
	enabled := len(targets) > 0
	switch {
	case filesNav.atLocations:
		detail, enabled = "open a folder first", false
	case o.atLocations || o.listedPath == "":
		detail, enabled = "open a folder in the other pane", false
	case filesSamePath(o.listedPath, filesNav.listedPath):
		detail, enabled = "both panes show this folder", false
	case len(targets) == 0:
		detail = "nothing selected"
	}
	return []feAction{
		{Kind: feActDualCopy, Label: "Copy to other pane", Detail: detail, Enabled: enabled},
		{Kind: feActDualMove, Label: "Move to other pane", Detail: detail, Enabled: enabled},
	}
}

// filesDualActivate runs one of the rows.
func filesDualActivate(config *Config, action feAction) {
	switch action.Kind {
	case feActDualPane:
		on := !filesDualSetting(config)
		filesDualSet(config, on)
		closeFeSheet()
		if on {
			showToast("Two panes — L1 and R1 pick the left or the right one", ToastInfo())
		} else {
			showToast("One pane", ToastInfo())
		}
	case feActDualCopy, feActDualMove:
		if filesDualSnapshot().running {
			if !feSheetArmed("dual-stop") {
				feSheet.armed, feSheet.armedAt = "dual-stop", time.Now()
				PlayNavSound()
				showToast(hintSentence("Press %s again to stop the transfer", "A", "Stop"), ToastWarn())
				return
			}
			feSheet.armed = ""
			filesDualStop()
			PlayBackSound()
			showToast("Stopping after the item being written…", ToastInfo())
			return
		}
		filesDualTransferStart(config, action.Kind == feActDualMove)
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────────

// filesDualRender draws the Files list in two panes, in place of the one
// renderDynamicList draws, and reports whether it did.
func filesDualRender(renderer *sdl.Renderer, config *Config, element Element, font *ttf.Font) bool {
	if renderer == nil || font == nil || element.Variable != "fe_entries" || !feSceneActive(config) || !filesDualOn(config) {
		return false
	}
	filesDualEnsureOther(config)
	o := &filesDual.other
	if o.stale || o.hidden != filesShowHidden(config) || o.sort != feSortModeOf(config) {
		filesDualRefreshOther(config)
	}
	elemW := getElementWidth(element, 600)
	elemH := getElementHeight(element, 500)
	const gap = int32(12)
	halfW := (elemW - gap) / 2
	liveX, otherX := element.X, element.X+halfW+gap
	if filesDual.right {
		liveX, otherX = otherX, liveX
	}

	// The pane being driven: the ordinary bar (crumbs, sort chip, the
	// extraction's progress) and rows, in its half.
	live := element
	live.X = liveX
	drawPanel(renderer, liveX, element.Y, halfW, elemH, PanelFill(220), accentColor)
	renderFilesBreadcrumbBar(renderer, config, live, halfW, font)
	entries := feEntriesOf(config)
	filesDualDrawRows(renderer, config, font, live, halfW, entries, focusedFileIndex, true)
	if len(entries) == 0 {
		title, body := "This folder is empty", hintSentence("Press %s to go back up.", "B", "Back")
		if filesNav.listErr != nil {
			title, body = filesListErrorText(filesNav.listErr)
		}
		UIEmptyState(renderer, config, sdl.Rect{X: liveX, Y: element.Y, W: halfW, H: elemH}, "@files", title, body)
	}
	// The driven pane wears the accent edge.
	strokeRoundedRect(renderer, liveX-2, element.Y-2, halfW+4, elemH+4, 14, 2, glassAccent())

	// The other pane: its own bar and rows, its marks and its Locations look
	// lent to the shared row drawer while it draws.
	other := element
	other.X = otherX
	drawPanel(renderer, otherX, element.Y, halfW, elemH, PanelFill(170), glassEdge())
	filesDualDrawOtherBar(renderer, config, font, sdl.Rect{X: otherX, Y: element.Y - 52, W: halfW, H: 44})
	start := func() int {
		// Put back even if a row's drawing panics: the driven pane's marks
		// and Locations state must never stay the other pane's.
		savedMarks, savedAt, savedLocs := feSelectedPaths, filesNav.atLocations, filesNav.locations
		defer func() { feSelectedPaths, filesNav.atLocations, filesNav.locations = savedMarks, savedAt, savedLocs }()
		feSelectedPaths = o.marks
		if feSelectedPaths == nil {
			feSelectedPaths = map[string]bool{}
		}
		filesNav.atLocations, filesNav.locations = o.atLocations, o.locations
		return filesDualDrawRows(renderer, config, font, other, halfW, o.entries, o.focus, false)
	}()
	if len(o.entries) <= 1 && !o.atLocations {
		title, body := "This folder is empty", "L1/R1 to work in it"
		if o.listErr != nil {
			title, body = filesListErrorText(o.listErr)
		}
		top := element.Y + dynamicListListOffset(element) + dynamicListRowH + 8
		if rect := (sdl.Rect{X: otherX, Y: top, W: halfW, H: element.Y + elemH - top - 8}); rect.H >= 80 {
			UIEmptyState(renderer, config, rect, "@folder", title, body)
		}
	}
	filesDual.otherList = sdl.Rect{X: otherX, Y: element.Y, W: halfW, H: elemH}
	filesDual.otherTop = element.Y + dynamicListListOffset(element)
	filesDual.otherStart = start
	filesDual.otherRows = dynamicListMaxVisible(element)
	return true
}

// filesDualDrawRows draws one pane's rows the way renderDynamicList does (the
// same row height, offset and scroll rule, so a click on the driven pane lands
// where main.go's list click expects it). The other pane's cursor is an
// outline. It returns the first row drawn.
func filesDualDrawRows(renderer *sdl.Renderer, config *Config, font *ttf.Font, element Element, w int32, entries []FileEntry, focus int, live bool) int {
	maxVisible := dynamicListMaxVisible(element)
	start := 0
	if focus >= maxVisible {
		start = focus - maxVisible + 1
	}
	end := min(start+maxVisible, len(entries))
	listOffset := dynamicListListOffset(element)
	for i := start; i < end; i++ {
		y := element.Y + listOffset + int32(i-start)*dynamicListRowH
		rowX, rowY, rowW, rowH := element.X+8, y+2, w-16, dynamicListRowH-4
		focused := i == focus
		drawRow(renderer, rowX, rowY, rowW, rowH, 8, focused && live, false)
		if focused && !live {
			strokeRoundedRect(renderer, rowX, rowY, rowW, rowH, 8, 1, glassEdgeHover())
		}
		renderFilesRow(renderer, config, font, entries[i], rowX, rowY, rowW, rowH, focused && live)
	}
	return start
}

// filesDualDrawOtherBar is the other pane's band above its list: the folder's
// name and what it holds, or the transfer writing into it.
func filesDualDrawOtherBar(renderer *sdl.Renderer, config *Config, font *ttf.Font, bar sdl.Rect) {
	filesDual.otherBar = bar
	o := filesDual.other
	fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, glassControlRadius, glassFill())
	strokeRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, glassControlRadius, 1, glassEdge())
	lineH := int32(font.Height())
	textY := bar.Y + (bar.H-lineH)/2
	right := ""
	if o.atLocations {
		right = pluralizeCount(len(o.entries), "place", "places")
	} else {
		count, bytes := feDirTotals(o.entries)
		right = feItemCountLabel(count) + " · " + humanBytes(bytes)
		if len(o.marks) > 0 {
			right = fmt.Sprintf("%d selected · ", len(o.marks)) + right
		}
	}
	rightInk := ColorTextTertiary()
	if s := filesDualSnapshot(); s.running && filesSamePath(s.dest, o.listedPath) {
		verb := "Copying"
		if s.move {
			verb = "Moving"
		}
		right = fmt.Sprintf("%s %d of %d here…", verb, min(s.done+1, s.total), s.total)
		rightInk = ColorTextAccent()
		track := sdl.Rect{X: bar.X + 14, Y: bar.Y + bar.H - 5, W: bar.W - 28, H: 3}
		frac := 0.0
		if s.total > 0 {
			frac = float64(s.done) / float64(s.total)
		}
		UIProgressBar(renderer, track, frac, glassA(18), glassAccent())
	}
	rightW, _ := measureText(font, right)
	if rightW > bar.W/2 {
		right = ellipsize(font, right, bar.W/2)
		rightW, _ = measureText(font, right)
	}
	renderText(renderer, config, font, right, rightInk, bar.X+bar.W-rightW-14, textY)
	name := ellipsize(font, filesDualOtherName(), bar.W-rightW-48)
	filesDrawPlainText(renderer, config, font, name, ColorTextSecondary(), bar.X+14, textY)
}
