package main

// cmddeck.go — the Command Deck: run common commands without typing.
//
// Typing a shell command on a handheld means walking an on-screen keyboard
// with a D-pad, character by character. The deck is a sheet over the Terminal
// that lists ready-made commands by category (System, Storage, Network,
// Files, Processes, Logs, Emulators, PortMaster, JukaHub), the user's own
// Favorites and macros, and Ask Juka questions. A command with blanks opens a
// small form whose slots are filled with pickers — a folder or file browser,
// a choice list, a number stepper, a process list, recent picks — while a live
// preview shows the exact line that will run. Nothing is typed.
//
// Opening it: Select in the Terminal (Ctrl+P on a keyboard), or "Commands"
// in the Tools hub, the Start menu's Tools group and Home search, which open
// the Terminal with the deck on top (B then returns to where the user was if
// nothing ran). Home search also finds each command by name.
//
// Controls (pad / keyboard):
//
//	List   Up/Down choose · Left/Right or L1/R1 category (Tab, PgUp/PgDn) ·
//	       A open or run (Enter) · Start run now (Ctrl+Enter) · Y put in the
//	       prompt to edit (Ctrl+E) · X save to Favorites, or remove one there
//	       (Ctrl+S, Delete) · B close (Esc) · typing filters every category
//	Form   Up/Down slot · Left/Right ±1 or next choice · L1/R1 ±10 (PgUp/PgDn)
//	       · A pick a value (Enter) · Start run · Y edit · X save · B back
//	Ask    commands that change things (delete, kill) ask first: A/Start run,
//	       B cancel
//
// Pickers live in cmddeck_pick.go, drawing in cmddeck_ui.go, the catalogue in
// cmddeck_catalog.go, favorites in cmddeck_store.go and the keyboard's token
// chips in cmddeck_chips.go.

import (
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type cmdDeckMode uint8

const (
	cmdModeBrowse cmdDeckMode = iota
	cmdModeForm
	cmdModePick
	cmdModeConfirm
)

type cmdItemKind uint8

const (
	cmdItemEntry cmdItemKind = iota
	cmdItemFav
	cmdItemNewMacro
)

// cmdItem is one row of the list.
type cmdItem struct {
	kind  cmdItemKind
	entry int // index into cmdDeck.entries
	fav   int // index into the favorites
	title string
	sub   string
}

// cmdCatView is one rail entry with the rows it lists on this device.
type cmdCatView struct {
	id    string
	title string
	items []cmdItem
}

// cmdDeckFilterMax bounds the typed filter.
const cmdDeckFilterMax = 60

// cmdDeck is the deck's state. Render thread only.
var cmdDeck struct {
	open bool
	mode cmdDeckMode
	// fromOutside: opened from Tools / Start / search, so B leaves the
	// Terminal again unless something ran.
	fromOutside bool
	ran         bool

	entries []cmdEntry
	avail   cmdDeckAvail
	probing bool

	cats     []cmdCatView
	cat      int
	cursor   int
	first    int
	filter   string
	filtered []cmdItem

	// The form.
	entry       int
	values      []string
	slot        int
	preview     string
	previewOK   bool
	missingSlot int

	// The confirm step.
	confirmLine  string
	confirmWhy   string
	confirmEntry int
	confirmMacro bool
	confirmBack  cmdDeckMode

	// Hit boxes of the last frame.
	card      sdl.Rect
	railRows  []sdl.Rect
	rows      []sdl.Rect
	rowsFirst int
	visible   int
	slotRows  []sdl.Rect
}

// cmdDeckPendingOpen asks termInit to open the deck (a "Commands" entry
// outside the Terminal); cmdDeckPendingEntry names a command to open on.
var (
	cmdDeckPendingOpen  bool
	cmdDeckPendingEntry string
)

// ── Opening and closing ──────────────────────────────────────────────────

// cmdDeckOpen shows the deck over the Terminal.
func cmdDeckOpen() {
	termHistoryClose()
	// A deck opened by a click while the pad sat on the top bar would leave
	// the bar's handler (which runs before the scene) taking every press.
	termFocusPrompt()
	// Favorites are read off the render thread from the Terminal's entry on
	// (cmdDeckTermEntered); if they have not landed yet, the deck opens on a
	// device category and the Favorites rows fill in when they do.
	cmdStoreLoadStart()
	if cmdDeck.entries == nil {
		cmdDeck.entries = cmdDeckCatalog()
	}
	cmdDeckProbeStart()
	cmdDeck.open = true
	cmdDeck.mode = cmdModeBrowse
	cmdDeck.fromOutside = false
	cmdDeck.ran = false
	cmdDeck.filter = ""
	cmdDeck.cursor, cmdDeck.first = 0, 0
	cmdDeck.cat = -1
	cmdDeckRebuild()
	// Favorites when there are some, else the first device category.
	cmdDeck.cat = 0
	if len(cmdStore.data.Favorites) == 0 {
		cmdDeck.cat = cmdDeckCatIndex("system")
	}
	if cmdDeck.cat < 0 || cmdDeck.cat >= len(cmdDeck.cats) {
		cmdDeck.cat = 0
	}
	PlayActivateSound()
}

// cmdDeckClose hides the deck and every sub-sheet.
func cmdDeckClose() {
	cmdDeck.open = false
	cmdDeck.mode = cmdModeBrowse
	cmdDeck.filter = ""
	cmdDeck.card = sdl.Rect{}
	cmdDeck.rows = cmdDeck.rows[:0]
	cmdDeck.railRows = cmdDeck.railRows[:0]
	cmdDeck.slotRows = cmdDeck.slotRows[:0]
	cmdPickReset()
}

// cmdDeckBack is B / Escape on the list: close, and leave the Terminal again
// when the deck was opened from outside and nothing ran.
func cmdDeckBack(config *Config) {
	leave := cmdDeck.fromOutside && !cmdDeck.ran
	cmdDeckClose()
	PlayBackSound()
	if leave && config != nil {
		termLeave(config)
	}
}

// cmdDeckOpenFromAnywhere is the "Commands" entry: the Terminal with the deck
// on top. entryID, when set, opens that command's form.
func cmdDeckOpenFromAnywhere(config *Config, entryID string) {
	if config == nil {
		return
	}
	idx := sceneIndexExact(config, "Terminal")
	if idx < 0 {
		ShowToast("Terminal is not part of this build", ToastKindInfo)
		return
	}
	if idx == currentSceneIndex {
		cmdDeckOpen()
		cmdDeckJumpTo(entryID)
		return
	}
	cmdDeckPendingOpen = true
	cmdDeckPendingEntry = entryID
	changeSceneTo(config, idx)
	if pendingSceneIndex != idx {
		cmdDeckPendingOpen = false
		cmdDeckPendingEntry = ""
	}
}

// cmdDeckTermEntered is termInit's hook: open the deck a "Commands" entry
// asked for, and start the probe early so the deck is ready when Select is.
func cmdDeckTermEntered() {
	if cmdDeck.entries == nil {
		cmdDeck.entries = cmdDeckCatalog()
	}
	cmdDeckProbeStart()
	cmdStoreLoadStart()
	if !cmdDeckPendingOpen {
		cmdDeckClose()
		return
	}
	cmdDeckPendingOpen = false
	id := cmdDeckPendingEntry
	cmdDeckPendingEntry = ""
	cmdDeckOpen()
	cmdDeck.fromOutside = true
	cmdDeckJumpTo(id)
}

// cmdDeckJumpTo selects entry id and opens its form (or runs nothing: a
// command without blanks is only selected, so the user still presses A).
func cmdDeckJumpTo(id string) {
	if id == "" {
		return
	}
	for i := range cmdDeck.entries {
		if cmdDeck.entries[i].id != id {
			continue
		}
		if c := cmdDeckCatIndex(cmdDeck.entries[i].cat); c >= 0 {
			cmdDeck.cat = c
			for j, it := range cmdDeck.cats[c].items {
				if it.kind == cmdItemEntry && it.entry == i {
					cmdDeck.cursor = j
				}
			}
		}
		if len(cmdDeck.entries[i].slots) > 0 {
			cmdDeckOpenForm(i)
		}
		return
	}
}

// cmdDeckProbeStart refreshes what this device offers, off the render thread.
func cmdDeckProbeStart() {
	if cmdDeck.probing || (cmdDeck.avail.ready && time.Since(cmdDeck.avail.at) < cmdDeckProbeMaxAge) {
		return
	}
	cmdDeck.probing = true
	entries := cmdDeck.entries
	go func() {
		a := cmdDeckProbe(entries)
		tbxPost(func() {
			cmdDeck.avail = a
			cmdDeck.probing = false
			cmdDeckRebuild()
		})
	}()
}

// ── The list ─────────────────────────────────────────────────────────────

// cmdDeckCatIndex is the visible rail index of category id, or -1.
func cmdDeckCatIndex(id string) int {
	for i, c := range cmdDeck.cats {
		if c.id == id {
			return i
		}
	}
	return -1
}

// cmdDeckRebuild recomputes the rail and rows from the catalogue, the probe
// and the favorites, keeping the selected category where it can.
func cmdDeckRebuild() {
	keep := ""
	if cmdDeck.cat >= 0 && cmdDeck.cat < len(cmdDeck.cats) {
		keep = cmdDeck.cats[cmdDeck.cat].id
	}
	cmdDeck.cats = cmdDeck.cats[:0]
	tokens := cmdDeck.avail.tokens
	for _, c := range cmdDeckCats {
		view := cmdCatView{id: c.id, title: c.title}
		if c.id == "fav" {
			for i, f := range cmdStore.data.Favorites {
				sub := cmdFavLine(f)
				if sub == f.Name {
					sub = ""
				}
				if len(f.Lines) > 1 {
					sub = itoaSmall(len(f.Lines)) + " commands · " + sub
				}
				view.items = append(view.items, cmdItem{kind: cmdItemFav, fav: i, title: f.Name, sub: sub})
			}
			view.items = append(view.items, cmdItem{kind: cmdItemNewMacro, title: "New macro from history…", sub: "Pick commands you ran; they run in order"})
			cmdDeck.cats = append(cmdDeck.cats, view)
			continue
		}
		for i := range cmdDeck.entries {
			e := &cmdDeck.entries[i]
			if e.cat != c.id {
				continue
			}
			if e.ask == "" && !cmdDeck.avail.ok[e.id] {
				continue
			}
			sub := "Juka answers in Chat, using its device tools"
			if e.ask == "" {
				sub = cmdDeckDisplayTemplate(e, tokens)
			}
			view.items = append(view.items, cmdItem{kind: cmdItemEntry, entry: i, title: e.title, sub: sub})
		}
		// Before the probe answers only Ask Juka has rows; the device
		// categories are kept as placeholders so the rail does not jump.
		if len(view.items) > 0 || (!cmdDeck.avail.ready && c.id != "pm" && c.id != "emu") {
			cmdDeck.cats = append(cmdDeck.cats, view)
		}
	}
	cmdDeck.cat = 0
	if keep != "" {
		if i := cmdDeckCatIndex(keep); i >= 0 {
			cmdDeck.cat = i
		}
	}
	cmdDeckRefilter()
	cmdDeck.cursor = clampIndex(cmdDeck.cursor, len(cmdDeckItems()))
}

// cmdDeckRefilter rebuilds the typed filter's matches across every category.
func cmdDeckRefilter() {
	cmdDeck.filtered = cmdDeck.filtered[:0]
	needle := strings.ToLower(strings.TrimSpace(cmdDeck.filter))
	if needle == "" {
		return
	}
	for _, c := range cmdDeck.cats {
		for _, it := range c.items {
			if it.kind == cmdItemNewMacro {
				continue
			}
			hay := strings.ToLower(it.title + " " + it.sub)
			if it.kind == cmdItemEntry {
				hay += " " + strings.ToLower(cmdDeck.entries[it.entry].keys)
			}
			if strings.Contains(hay, needle) {
				cmdDeck.filtered = append(cmdDeck.filtered, it)
			}
		}
	}
	cmdDeck.cursor = clampIndex(cmdDeck.cursor, len(cmdDeck.filtered))
}

// cmdDeckItems is the list on screen.
func cmdDeckItems() []cmdItem {
	if cmdDeck.filter != "" {
		return cmdDeck.filtered
	}
	if cmdDeck.cat >= 0 && cmdDeck.cat < len(cmdDeck.cats) {
		return cmdDeck.cats[cmdDeck.cat].items
	}
	return nil
}

// cmdDeckSelected is the highlighted row.
func cmdDeckSelected() (cmdItem, bool) {
	items := cmdDeckItems()
	if cmdDeck.cursor < 0 || cmdDeck.cursor >= len(items) {
		return cmdItem{}, false
	}
	return items[cmdDeck.cursor], true
}

func cmdDeckMove(delta int) {
	n := len(cmdDeckItems())
	if n == 0 {
		return
	}
	next := clampIndex(cmdDeck.cursor+delta, n)
	if next != cmdDeck.cursor {
		cmdDeck.cursor = next
		PlayNavSound()
	}
}

// cmdDeckStepCat moves to the previous/next category, wrapping.
func cmdDeckStepCat(delta int) {
	n := len(cmdDeck.cats)
	if n == 0 {
		return
	}
	if cmdDeck.filter != "" {
		cmdDeck.filter = ""
		cmdDeckRefilter()
	}
	cmdDeck.cat = ((cmdDeck.cat+delta)%n + n) % n
	cmdDeck.cursor, cmdDeck.first = 0, 0
	PlayNavSound()
}

// ── Values, preview, running ─────────────────────────────────────────────

// cmdSlotRecentKey is where a slot's recent picks are kept: folders and
// files are shared by every command, choices are per command.
func cmdSlotRecentKey(e *cmdEntry, i int) string {
	if i < 0 || i >= len(e.slots) {
		return ""
	}
	switch s := e.slots[i]; s.kind {
	case cmdSlotFolder:
		return "folder"
	case cmdSlotFile:
		if s.filter != "" {
			return "file:" + s.filter
		}
		return "file"
	case cmdSlotChoice, cmdSlotPattern:
		return e.id + "/" + itoaSmall(i+1)
	}
	return ""
}

// cmdDeckDefaults is a fresh form: the last value picked for each slot, else
// its default, else the slot's starting folder; files and processes start
// empty (a stale one would be a guess).
func cmdDeckDefaults(e *cmdEntry) []string {
	values := make([]string, len(e.slots))
	tokens := cmdDeck.avail.tokens
	for i, s := range e.slots {
		for _, r := range cmdRecentValues(cmdSlotRecentKey(e, i)) {
			if cmdSlotValueValid(s, r, tokens) {
				values[i] = r
				break
			}
		}
		if values[i] != "" {
			continue
		}
		switch s.kind {
		case cmdSlotFolder:
			v := tokens[s.start]
			if v == "" {
				v = tokens["sd"]
			}
			values[i] = v
		case cmdSlotInt, cmdSlotChoice, cmdSlotPattern:
			values[i] = s.def
		}
	}
	return values
}

// cmdDeckLineFor expands entry i with values.
func cmdDeckLineFor(i int, values []string) (string, bool, int) {
	if i < 0 || i >= len(cmdDeck.entries) {
		return "", false, -1
	}
	e := &cmdDeck.entries[i]
	return cmdDeckExpand(e.tmpl, e.slots, values, cmdDeck.avail.tokens, cmdEntryQuote(e))
}

// cmdDeckRefreshPreview recomputes the form's line after any change.
func cmdDeckRefreshPreview() {
	line, ok, missing := cmdDeckLineFor(cmdDeck.entry, cmdDeck.values)
	cmdDeck.previewOK, cmdDeck.missingSlot = ok, missing
	cmdDeckRefreshSlotDisp()
	if ok {
		cmdDeck.preview = line
		return
	}
	cmdDeck.preview = cmdDeckDisplayTemplate(&cmdDeck.entries[cmdDeck.entry], cmdDeck.avail.tokens)
}

// cmdDeckNoteRecents remembers the form's folder, file and choice picks.
func cmdDeckNoteRecents(i int, values []string) {
	if i < 0 || i >= len(cmdDeck.entries) {
		return
	}
	e := &cmdDeck.entries[i]
	changed := false
	for k := range e.slots {
		if k < len(values) && values[k] != "" {
			if key := cmdSlotRecentKey(e, k); key != "" && cmdRecentNote(key, values[k]) {
				changed = true
			}
		}
	}
	if changed {
		cmdStoreSave()
	}
}

// cmdDeckCanRun refuses while a command is still running: the Terminal runs
// one at a time and would only print an error behind the sheet.
func cmdDeckCanRun() bool {
	if term.job == nil {
		return true
	}
	PlayErrorSound()
	if hintsUseKeyboard() {
		ShowToast("A command is still running — Ctrl+C stops it", ToastKindWarning)
	} else {
		ShowToast("A command is still running — X stops it", ToastKindWarning)
	}
	return false
}

// cmdDeckRun runs line in the Terminal. A dangerous entry asks first.
func cmdDeckRun(line string, entry int, confirmed bool) {
	if strings.TrimSpace(line) == "" || !cmdDeckCanRun() {
		return
	}
	if !confirmed && entry >= 0 && entry < len(cmdDeck.entries) && cmdDeck.entries[entry].danger != "" {
		cmdDeck.confirmLine = line
		cmdDeck.confirmWhy = cmdDeck.entries[entry].danger
		cmdDeck.confirmEntry = entry
		cmdDeck.confirmMacro = false
		cmdDeck.confirmBack = cmdDeck.mode
		cmdDeck.mode = cmdModeConfirm
		PlayActivateSound()
		return
	}
	cmdDeckLaunch(line, false)
}

// cmdDeckLaunch closes the deck and runs line in the Terminal. A macro goes
// to the shell whole: termExecute picks a built-in by the first word, so
// "cd X && ls" or "sysinfo && df -h" would run only (or misread) the first
// command.
func cmdDeckLaunch(line string, macro bool) {
	if strings.TrimSpace(line) == "" || !cmdDeckCanRun() {
		return
	}
	cmdDeck.ran = true
	cmdDeckClose()
	PlayActivateSound()
	termClearLine()
	termFocusPrompt()
	if !macro {
		termExecute(line)
		return
	}
	termRecordHistory(line)
	term.histIdx = -1
	termFollow()
	termAppend(termPrompt+line, termKindEcho)
	termRunExternal(line)
}

// cmdDeckConfirmYes is Run on the confirm card.
func cmdDeckConfirmYes() {
	cmdDeckLaunch(cmdDeck.confirmLine, cmdDeck.confirmMacro)
}

// cmdFavDangerWords are commands that delete or stop things: a saved line
// with one asks first, like the templates that do (a favorite saved from
// "Delete a file" must not lose its confirm).
var cmdFavDangerWords = map[string]bool{
	"rm": true, "rmdir": true, "del": true, "erase": true, "rd": true, "kill": true,
	"killall": true, "pkill": true, "taskkill": true, "dd": true, "mkfs": true,
	"format": true, "reboot": true, "poweroff": true, "shutdown": true, "halt": true,
}

// cmdDeckRunFav runs favorite f, asking first when it deletes or stops
// something.
func cmdDeckRunFav(f cmdFav) {
	line := cmdFavLine(f)
	macro := len(f.Lines) > 1
	danger := false
	for _, w := range strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
		return r == ' ' || r == '\t' || r == ';' || r == '&' || r == '|' || r == '(' || r == ')'
	}) {
		if cmdFavDangerWords[strings.TrimSuffix(w, ".exe")] {
			danger = true
			break
		}
	}
	if !danger {
		cmdDeckLaunch(line, macro)
		return
	}
	if !cmdDeckCanRun() {
		return
	}
	cmdDeck.confirmLine = line
	cmdDeck.confirmWhy = "This deletes or stops something, and cannot be undone."
	cmdDeck.confirmEntry = -1
	cmdDeck.confirmMacro = macro
	cmdDeck.confirmBack = cmdDeck.mode
	cmdDeck.mode = cmdModeConfirm
	PlayActivateSound()
}

// cmdDeckEdit puts line in the prompt to be read and changed, not run.
func cmdDeckEdit(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	cmdDeck.ran = true
	cmdDeckClose()
	PlayActivateSound()
	termClearLine()
	termInsertText(line)
	termFocusPrompt()
	// Editing on a pad means the on-screen keyboard; open it on the line
	// (its token chips help with the change).
	if !hintsUseKeyboard() {
		vkOpenTerminal()
	}
}

// cmdDeckSaveFav saves line under name and says so.
func cmdDeckSaveFav(name, line string) {
	if cmdFavAdd(name, []string{line}) {
		PlayActivateSound()
		ShowToast("Saved to Commands → Favorites", ToastKindSuccess)
		cmdDeckRebuild()
		return
	}
	PlayNavSound()
	ShowToast("Already in Favorites", ToastKindInfo)
}

// cmdDeckAsk hands an Ask Juka question to the assistant (Chat's Juka AI
// tab, which uses its own device tools to answer).
func cmdDeckAsk(e *cmdEntry) {
	cmdDeck.ran = true
	cmdDeckClose()
	PlayActivateSound()
	aiAgentAskFromDeck(e.ask)
}

// ── Actions on the list ──────────────────────────────────────────────────

// cmdDeckAction is what a button does to the highlighted row.
type cmdDeckAction uint8

const (
	cmdActOpen cmdDeckAction = iota // A / Enter
	cmdActRun                       // Start / Ctrl+Enter
	cmdActEdit                      // Y / Ctrl+E
	cmdActSave                      // X / Ctrl+S (Delete on a favorite)
)

func cmdDeckActivate(act cmdDeckAction) {
	it, ok := cmdDeckSelected()
	if !ok {
		PlayErrorSound()
		return
	}
	switch it.kind {
	case cmdItemNewMacro:
		if act == cmdActOpen || act == cmdActRun {
			cmdPickOpenMacro()
		}
		return
	case cmdItemFav:
		if it.fav < 0 || it.fav >= len(cmdStore.data.Favorites) {
			return
		}
		f := cmdStore.data.Favorites[it.fav]
		switch act {
		case cmdActOpen, cmdActRun:
			cmdDeckRunFav(f)
		case cmdActEdit:
			cmdDeckEdit(cmdFavLine(f))
		case cmdActSave:
			if cmdFavRemove(it.fav) {
				PlayBackSound()
				ShowToast("Removed from Favorites", ToastKindInfo)
				cmdDeckRebuild()
			}
		}
		return
	}
	e := &cmdDeck.entries[it.entry]
	if e.ask != "" {
		if act == cmdActOpen || act == cmdActRun {
			cmdDeckAsk(e)
		} else {
			PlayErrorSound()
		}
		return
	}
	if len(e.slots) > 0 && act == cmdActOpen {
		cmdDeckOpenForm(it.entry)
		return
	}
	values := cmdDeckDefaults(e)
	line, complete, _ := cmdDeckLineFor(it.entry, values)
	if !complete {
		// A blank with no sensible default (a file, a process): the form
		// opens on it with its picker.
		cmdDeckOpenForm(it.entry)
		return
	}
	switch act {
	case cmdActOpen, cmdActRun:
		cmdDeckNoteRecents(it.entry, values)
		cmdDeckRun(line, it.entry, false)
	case cmdActEdit:
		cmdDeckNoteRecents(it.entry, values)
		cmdDeckEdit(line)
	case cmdActSave:
		cmdDeckSaveFav(e.title, line)
	}
}

// ── The form ─────────────────────────────────────────────────────────────

// cmdDeckOpenForm shows entry i's slots, opening the first empty slot's
// picker straight away (a file or a process has nothing to default to).
func cmdDeckOpenForm(i int) {
	if i < 0 || i >= len(cmdDeck.entries) {
		return
	}
	cmdDeck.mode = cmdModeForm
	cmdDeck.entry = i
	cmdDeck.values = cmdDeckDefaults(&cmdDeck.entries[i])
	cmdDeck.slot = 0
	cmdDeckRefreshPreview()
	PlayActivateSound()
	cmdDeckPickNextEmpty()
}

// cmdDeckPickNextEmpty opens the picker of the first slot still empty.
func cmdDeckPickNextEmpty() {
	for k, v := range cmdDeck.values {
		if v == "" {
			cmdDeck.slot = k
			cmdPickOpen(k)
			return
		}
	}
}

// cmdDeckSetValue fills the form's slot k (a picker's answer).
func cmdDeckSetValue(k int, v string) {
	if cmdDeck.mode != cmdModeForm && cmdDeck.mode != cmdModePick {
		return
	}
	if k < 0 || k >= len(cmdDeck.values) {
		return
	}
	cmdDeck.values[k] = v
	cmdDeck.slot = k
	cmdDeck.mode = cmdModeForm
	cmdDeckRefreshPreview()
}

// cmdDeckStep changes the focused slot by delta: numbers step, choices
// cycle; pickers have nothing to step.
func cmdDeckStep(delta int) {
	e := &cmdDeck.entries[cmdDeck.entry]
	k := cmdDeck.slot
	if k < 0 || k >= len(e.slots) {
		return
	}
	s := e.slots[k]
	switch s.kind {
	case cmdSlotInt:
		n := 0
		for _, r := range cmdDeck.values[k] {
			if r >= '0' && r <= '9' && n < 1e6 {
				n = n*10 + int(r-'0')
			}
		}
		n += delta
		if n < s.min {
			n = s.min
		}
		if n > s.max {
			n = s.max
		}
		cmdDeck.values[k] = itoaSmall(n)
	case cmdSlotChoice, cmdSlotPattern:
		var opts []string
		for _, c := range s.choices {
			if c == "@gw" && cmdDeck.avail.tokens["gw"] == "" {
				continue
			}
			opts = append(opts, c)
		}
		if len(opts) == 0 {
			return
		}
		cur := 0
		for j, c := range opts {
			if c == cmdDeck.values[k] {
				cur = j
			}
		}
		step := 1
		if delta < 0 {
			step = -1
		}
		cmdDeck.values[k] = opts[((cur+step)%len(opts)+len(opts))%len(opts)]
	default:
		PlayErrorSound()
		return
	}
	PlayNavSound()
	cmdDeckRefreshPreview()
}

// cmdDeckFormAction runs, edits or saves the form's command; an incomplete
// form opens the picker of the slot it is missing instead.
func cmdDeckFormAction(act cmdDeckAction) {
	if !cmdDeck.previewOK {
		PlayErrorSound()
		if cmdDeck.missingSlot >= 0 && cmdDeck.missingSlot < len(cmdDeck.values) {
			cmdDeck.slot = cmdDeck.missingSlot
			cmdPickOpen(cmdDeck.missingSlot)
		}
		return
	}
	line := cmdDeck.preview
	e := &cmdDeck.entries[cmdDeck.entry]
	switch act {
	case cmdActRun:
		cmdDeckNoteRecents(cmdDeck.entry, cmdDeck.values)
		cmdDeckRun(line, cmdDeck.entry, false)
	case cmdActEdit:
		cmdDeckNoteRecents(cmdDeck.entry, cmdDeck.values)
		cmdDeckEdit(line)
	case cmdActSave:
		cmdDeckNoteRecents(cmdDeck.entry, cmdDeck.values)
		cmdDeckSaveFav(e.title, line)
	}
}

func cmdDeckFormMove(delta int) {
	n := len(cmdDeck.values)
	if n == 0 {
		return
	}
	next := clampIndex(cmdDeck.slot+delta, n)
	if next != cmdDeck.slot {
		cmdDeck.slot = next
		PlayNavSound()
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// cmdDeckPad is the pad while the deck is open. It is modal: every button is
// consumed so nothing reaches the Terminal behind it.
func cmdDeckPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	b := sdl.GameControllerButton(e.Button)
	switch cmdDeck.mode {
	case cmdModePick:
		cmdPickPad(b)
		return true
	case cmdModeConfirm:
		switch b {
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
			cmdDeckConfirmYes()
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			cmdDeck.mode = cmdDeck.confirmBack
			PlayBackSound()
		}
		return true
	case cmdModeForm:
		switch b {
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			cmdDeck.mode = cmdModeBrowse
			PlayBackSound()
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			cmdDeckFormMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			cmdDeckFormMove(1)
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			cmdDeckStep(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			cmdDeckStep(1)
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			cmdDeckStep(-10)
		case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
			cmdDeckStep(10)
		case sdl.CONTROLLER_BUTTON_A:
			cmdPickOpen(cmdDeck.slot)
		case sdl.CONTROLLER_BUTTON_START:
			cmdDeckFormAction(cmdActRun)
		case sdl.CONTROLLER_BUTTON_Y:
			cmdDeckFormAction(cmdActEdit)
		case sdl.CONTROLLER_BUTTON_X:
			cmdDeckFormAction(cmdActSave)
		}
		return true
	}
	switch b {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		cmdDeckBack(config)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		cmdDeckMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		cmdDeckMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		cmdDeckStepCat(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		cmdDeckStepCat(1)
	case sdl.CONTROLLER_BUTTON_A:
		cmdDeckActivate(cmdActOpen)
	case sdl.CONTROLLER_BUTTON_START:
		cmdDeckActivate(cmdActRun)
	case sdl.CONTROLLER_BUTTON_Y:
		cmdDeckActivate(cmdActEdit)
	case sdl.CONTROLLER_BUTTON_X:
		cmdDeckActivate(cmdActSave)
	}
	return true
}

// cmdDeckKey is the keyboard while the deck is open.
func cmdDeckKey(e *sdl.KeyboardEvent, config *Config) {
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	sym := e.Keysym.Sym
	switch cmdDeck.mode {
	case cmdModePick:
		cmdPickKey(e)
		return
	case cmdModeConfirm:
		switch sym {
		case sdl.K_RETURN, sdl.K_KP_ENTER:
			cmdDeckConfirmYes()
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			cmdDeck.mode = cmdDeck.confirmBack
			PlayBackSound()
		}
		return
	case cmdModeForm:
		switch {
		case sym == sdl.K_ESCAPE, sym == sdl.K_p && ctrl:
			cmdDeck.mode = cmdModeBrowse
			PlayBackSound()
		case sym == sdl.K_UP:
			cmdDeckFormMove(-1)
		case sym == sdl.K_DOWN:
			cmdDeckFormMove(1)
		case sym == sdl.K_LEFT:
			cmdDeckStep(-1)
		case sym == sdl.K_RIGHT:
			cmdDeckStep(1)
		case sym == sdl.K_PAGEUP:
			cmdDeckStep(-10)
		case sym == sdl.K_PAGEDOWN:
			cmdDeckStep(10)
		case (sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER) && ctrl:
			cmdDeckFormAction(cmdActRun)
		case sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER:
			cmdPickOpen(cmdDeck.slot)
		case sym == sdl.K_e && ctrl:
			cmdDeckFormAction(cmdActEdit)
		case sym == sdl.K_s && ctrl:
			cmdDeckFormAction(cmdActSave)
		}
		return
	}
	switch {
	case sym == sdl.K_ESCAPE:
		if cmdDeck.filter != "" {
			cmdDeck.filter = ""
			cmdDeckRefilter()
			PlayBackSound()
			return
		}
		cmdDeckBack(config)
	case sym == sdl.K_p && ctrl:
		cmdDeckBack(config)
	case sym == sdl.K_UP:
		cmdDeckMove(-1)
	case sym == sdl.K_DOWN:
		cmdDeckMove(1)
	case sym == sdl.K_HOME:
		cmdDeckMove(-len(cmdDeckItems()))
	case sym == sdl.K_END:
		cmdDeckMove(len(cmdDeckItems()))
	case sym == sdl.K_LEFT || sym == sdl.K_PAGEUP || (sym == sdl.K_TAB && e.Keysym.Mod&sdl.KMOD_SHIFT != 0):
		cmdDeckStepCat(-1)
	case sym == sdl.K_RIGHT || sym == sdl.K_PAGEDOWN || sym == sdl.K_TAB:
		cmdDeckStepCat(1)
	case (sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER) && ctrl:
		cmdDeckActivate(cmdActRun)
	case sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER:
		cmdDeckActivate(cmdActOpen)
	case sym == sdl.K_e && ctrl:
		cmdDeckActivate(cmdActEdit)
	case sym == sdl.K_s && ctrl:
		cmdDeckActivate(cmdActSave)
	case sym == sdl.K_DELETE:
		if it, ok := cmdDeckSelected(); ok && it.kind == cmdItemFav {
			cmdDeckActivate(cmdActSave)
		}
	case sym == sdl.K_BACKSPACE:
		if cmdDeck.filter != "" {
			cmdDeck.filter = trimLastRune(cmdDeck.filter)
			cmdDeckRefilter()
		}
	}
}

// cmdDeckType adds typed text to the list's filter (keyboard only; the pad
// browses by category).
func cmdDeckType(text string) {
	if cmdDeck.mode != cmdModeBrowse {
		return
	}
	var b strings.Builder
	for _, r := range text {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 || len(cmdDeck.filter)+b.Len() > cmdDeckFilterMax {
		return
	}
	cmdDeck.filter += b.String()
	cmdDeck.cursor, cmdDeck.first = 0, 0
	cmdDeckRefilter()
}

// cmdDeckClick handles a click while the deck is open: a rail row picks the
// category, a row is selected and a second click on it opens it, a slot row
// opens its picker and the form's Run button runs (as Start / Ctrl+Enter);
// outside the card closes the sheet in front.
func cmdDeckClick(mx, my int32, config *Config) bool {
	switch cmdDeck.mode {
	case cmdModePick:
		return cmdPickClick(mx, my)
	case cmdModeConfirm:
		// Only the Run button runs; Cancel or a click outside the card goes
		// back, and a stray click on the card's text does nothing.
		switch {
		case pointInRect(mx, my, cmdDeckUI.confirmBt[0]):
			cmdDeckConfirmYes()
		case pointInRect(mx, my, cmdDeckUI.confirmBt[1]) || !pointInRect(mx, my, cmdDeck.card):
			cmdDeck.mode = cmdDeck.confirmBack
			PlayBackSound()
		}
		return true
	}
	if cmdDeck.card.W <= 0 || !pointInRect(mx, my, cmdDeck.card) {
		if cmdDeck.mode == cmdModeForm {
			cmdDeck.mode = cmdModeBrowse
			PlayBackSound()
			return true
		}
		cmdDeckBack(config)
		return true
	}
	if cmdDeck.mode == cmdModeForm {
		if pointInRect(mx, my, cmdDeckUI.formRun) {
			cmdDeckFormAction(cmdActRun)
			return true
		}
		for k, r := range cmdDeck.slotRows {
			if pointInRect(mx, my, r) {
				cmdDeck.slot = k
				cmdPickOpen(k)
				return true
			}
		}
		return true
	}
	for i, r := range cmdDeck.railRows {
		if pointInRect(mx, my, r) && i < len(cmdDeck.cats) {
			if cmdDeck.filter != "" {
				cmdDeck.filter = ""
				cmdDeckRefilter()
			}
			cmdDeck.cat, cmdDeck.cursor, cmdDeck.first = i, 0, 0
			PlayNavSound()
			return true
		}
	}
	for i, r := range cmdDeck.rows {
		if !pointInRect(mx, my, r) {
			continue
		}
		idx := cmdDeck.rowsFirst + i
		if idx == cmdDeck.cursor {
			cmdDeckActivate(cmdActOpen)
		} else if idx >= 0 && idx < len(cmdDeckItems()) {
			cmdDeck.cursor = idx
			PlayNavSound()
		}
		return true
	}
	return true
}

// cmdDeckWheel scrolls whichever list is in front.
func cmdDeckWheel(dy int32) {
	switch cmdDeck.mode {
	case cmdModePick:
		cmdPickMove(-int(dy))
	case cmdModeForm:
		cmdDeckFormMove(-int(dy))
	case cmdModeBrowse:
		cmdDeckMove(-int(dy))
	}
}

// ── Search (SHELL-5) ─────────────────────────────────────────────────────

// cmdDeckSearchScreens are the deck's commands as Home search rows, so "free
// space" or "ping" finds the command and opens it in the Terminal. Only what
// this device offers is listed; before the first probe nothing is (the probe
// starts here, so the next search has them).
func cmdDeckSearchScreens() []homeSearchScreen {
	if cmdDeck.entries == nil {
		cmdDeck.entries = cmdDeckCatalog()
	}
	cmdDeckProbeStart()
	out := []homeSearchScreen{{
		title:  "Commands",
		where:  "Tools",
		icon:   "terminal",
		keys:   "commands command deck terminal shortcuts run without typing favorites macros",
		action: func(c *Config) { cmdDeckOpenFromAnywhere(c, "") },
	}}
	if !cmdDeck.avail.ready {
		return out
	}
	for i := range cmdDeck.entries {
		e := cmdDeck.entries[i]
		if e.ask != "" || !cmdDeck.avail.ok[e.id] {
			continue
		}
		id := e.id
		out = append(out, homeSearchScreen{
			title:  e.title,
			where:  "Commands",
			icon:   "terminal",
			keys:   e.keys + " " + strings.ReplaceAll(e.tmpl, "{", " "),
			action: func(c *Config) { cmdDeckOpenFromAnywhere(c, id) },
		})
	}
	return out
}
