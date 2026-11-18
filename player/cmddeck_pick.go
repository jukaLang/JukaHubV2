package main

// cmddeck_pick.go — the Command Deck's pickers: how a slot gets its value
// without typing.
//
//	Folder   a browser that starts at the slot's folder (the card, the app
//	         folder, the game logs …). Its first row uses the folder on
//	         screen; A enters a subfolder, Left goes up, the top level lists
//	         Files' Locations and the recent folders.
//	File     the same browser listing files too; a log slot shows logs and
//	         text files only.
//	Choice   the slot's values plus the ones picked lately.
//	Number   presets around the default (the form's Left/Right and L1/R1
//	         step it too).
//	Process  the running processes, busiest first; init and JukaHub itself
//	         are left out.
//	Macro    the last commands run, newest first: A ticks, Start saves the
//	         ticked ones as one favorite that runs them in order.
//
// Folders and processes are read on a goroutine and handed back with
// tbxPost; a generation counter drops an answer the user has moved past. A
// folder read stops at cmdPickMaxEntries, so a ROM folder with 20 000 files
// lists its first entries instead of stalling.

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	cmdPickMaxEntries = 400
	cmdPickMaxProcs   = 200
	cmdMacroHistory   = 30
)

type cmdPickKind uint8

const (
	cmdPickSlot cmdPickKind = iota
	cmdPickMacro
)

// cmdPickRow is one picker row.
type cmdPickRow struct {
	label  string
	detail string
	value  string // what choosing the row puts in the slot (or the folder to enter)
	dir    bool   // enters a folder
	up     bool   // the parent folder
	use    bool   // "use this folder"
	recent bool
	loc    bool // a Locations row
}

// cmdPick is the open picker. Render thread only.
var cmdPick struct {
	kind    cmdPickKind
	slot    int
	sk      cmdSlotKind
	filter  string
	title   string
	dir     string // folder on screen; "" = the top level
	rows    []cmdPickRow
	cursor  int
	first   int
	loading bool
	gen     uint64
	note    string // "first 400 shown", an error, …
	checked []bool // macro picker

	// Hit boxes of the last frame.
	rowRects  []sdl.Rect
	rowsFirst int
	visible   int
}

func cmdPickReset() {
	cmdPick.rows = cmdPick.rows[:0]
	cmdPick.rowRects = cmdPick.rowRects[:0]
	cmdPick.checked = cmdPick.checked[:0]
	cmdPick.loading = false
	cmdPick.gen++
}

// cmdPickOpen opens slot k's picker over the form.
func cmdPickOpen(k int) {
	if cmdDeck.entry < 0 || cmdDeck.entry >= len(cmdDeck.entries) {
		return
	}
	e := &cmdDeck.entries[cmdDeck.entry]
	if k < 0 || k >= len(e.slots) {
		return
	}
	s := e.slots[k]
	cmdPickReset()
	cmdPick.kind = cmdPickSlot
	cmdPick.slot = k
	cmdPick.sk = s.kind
	cmdPick.filter = s.filter
	cmdPick.cursor, cmdPick.first = 0, 0
	cmdPick.note = ""
	cmdPick.dir = ""
	cmdDeck.slot = k
	cmdDeck.mode = cmdModePick
	PlayActivateSound()
	tokens := cmdDeck.avail.tokens
	switch s.kind {
	case cmdSlotFolder, cmdSlotFile:
		cmdPick.title = "Choose a folder"
		if s.kind == cmdSlotFile {
			cmdPick.title = "Choose a file"
			if s.filter == "log" {
				cmdPick.title = "Choose a log"
			}
		}
		// Start where the current value is, else the slot's folder.
		start := ""
		if v := cmdDeck.values[k]; v != "" {
			if s.kind == cmdSlotFolder {
				start = v
			} else {
				start = filepath.Dir(v)
			}
		}
		if start == "" {
			start = tokens[s.start]
		}
		if start == "" {
			start = tokens["sd"]
		}
		cmdPickLoadDir(start)
	case cmdSlotProcess:
		cmdPick.title = "Choose a process"
		cmdPickLoadProcs()
	case cmdSlotInt:
		cmdPick.title = s.label
		cmdPickSetRows(cmdPickNumberRows(s, cmdDeck.values[k]))
	default:
		cmdPick.title = "Choose " + strings.ToLower(s.label)
		cmdPickSetRows(cmdPickChoiceRows(e, k))
	}
}

// cmdPickSetRows replaces the rows and puts the cursor on the current value.
func cmdPickSetRows(rows []cmdPickRow) {
	cmdPick.rows = rows
	cmdPick.loading = false
	cmdPick.cursor, cmdPick.first = 0, 0
	if cmdPick.kind == cmdPickSlot && cmdPick.slot < len(cmdDeck.values) {
		cur := cmdDeck.values[cmdPick.slot]
		for i, r := range rows {
			if !r.dir && !r.up && r.value == cur && cur != "" {
				cmdPick.cursor = i
				break
			}
		}
	}
}

// cmdPickChoiceRows lists a choice slot: recent picks first, then the list.
func cmdPickChoiceRows(e *cmdEntry, k int) []cmdPickRow {
	s := e.slots[k]
	tokens := cmdDeck.avail.tokens
	var rows []cmdPickRow
	seen := map[string]bool{}
	for _, r := range cmdRecentValues(cmdSlotRecentKey(e, k)) {
		if cmdSlotValueValid(s, r, tokens) && !seen[r] {
			seen[r] = true
			rows = append(rows, cmdPickRow{label: cmdChoiceLabel(r), value: r, recent: true, detail: "Recent"})
		}
	}
	for _, c := range s.choices {
		if seen[c] || (c == "@gw" && tokens["gw"] == "") {
			continue
		}
		seen[c] = true
		rows = append(rows, cmdPickRow{label: cmdChoiceLabel(c), value: c})
	}
	return rows
}

// cmdChoiceLabel is how a choice reads: the router with its address.
func cmdChoiceLabel(v string) string {
	if v == "@gw" {
		gw := cmdDeck.avail.tokens["gw"]
		if gw == "" {
			return "Router"
		}
		return "Router (" + gw + ")"
	}
	return v
}

// cmdPickNumberRows offers presets for a number slot, the current one first.
func cmdPickNumberRows(s cmdSlot, cur string) []cmdPickRow {
	presets := []int{1, 4, 10, 20, 50, 100, 200, 500}
	if s.label == "Days" {
		presets = []int{1, 2, 7, 14, 30, 90, 365}
	}
	var rows []cmdPickRow
	seen := map[string]bool{}
	add := func(v string, detail string) {
		if !cmdSlotValueValid(s, v, nil) || seen[v] {
			return
		}
		seen[v] = true
		rows = append(rows, cmdPickRow{label: v, value: v, detail: detail})
	}
	add(cur, "Current")
	add(s.def, "Default")
	for _, p := range presets {
		add(strconv.Itoa(p), "")
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := strconv.Atoi(rows[i].value)
		b, _ := strconv.Atoi(rows[j].value)
		return a < b
	})
	return rows
}

// cmdPickLogName reports whether a file name looks like a log or text file.
func cmdPickLogName(name string) bool {
	lower := strings.ToLower(name)
	switch strings.ToLower(filepath.Ext(lower)) {
	case ".log", ".txt", ".out", ".err", ".cfg", ".ini", ".json":
		return true
	}
	return strings.Contains(lower, "log")
}

// cmdPickTextName reports whether a file name looks like text worth showing
// (a binary would flood the console).
func cmdPickTextName(name string) bool {
	if cmdPickLogName(name) {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".sh", ".conf", ".xml", ".csv", ".yml", ".yaml", ".lua", ".py", ".m3u", ".cue", ".srt", ".toml", ".opt", ".rmp":
		return true
	}
	return false
}

// cmdPickTop is the top level: recent picks, then Files' Locations.
func cmdPickTop() {
	cmdPick.dir = ""
	cmdPick.note = ""
	gen := cmdPick.gen + 1
	cmdPick.gen = gen
	cmdPick.loading = true
	sk := cmdPick.sk
	var recent []string
	if cmdDeck.entry >= 0 && cmdDeck.entry < len(cmdDeck.entries) {
		recent = append(recent, cmdRecentValues(cmdSlotRecentKey(&cmdDeck.entries[cmdDeck.entry], cmdPick.slot))...)
	}
	extra := []string{cmdDeck.avail.tokens["gamelogs"], cmdDeck.avail.tokens["pm"]}
	config := appConfig
	go func() {
		var rows []cmdPickRow
		for _, r := range recent {
			info, err := os.Stat(r)
			if err != nil || info.IsDir() != (sk == cmdSlotFolder) {
				continue
			}
			rows = append(rows, cmdPickRow{label: filepath.Base(r), detail: r, value: r, recent: true})
		}
		for _, loc := range filesBuildLocations(config) {
			rows = append(rows, cmdPickRow{label: loc.Label, detail: loc.Path, value: loc.Path, dir: true, loc: true})
		}
		for _, x := range extra {
			if x != "" {
				rows = append(rows, cmdPickRow{label: filepath.Base(x), detail: x, value: x, dir: true, loc: true})
			}
		}
		tbxPost(func() {
			if cmdPick.gen != gen || cmdDeck.mode != cmdModePick {
				return
			}
			cmdPickSetRows(rows)
		})
	}()
}

// cmdPickLoadDir lists dir (folders first, then files for a file slot).
func cmdPickLoadDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		cmdPickTop()
		return
	}
	dir = filepath.Clean(dir)
	cmdPick.dir = dir
	cmdPick.note = ""
	gen := cmdPick.gen + 1
	cmdPick.gen = gen
	cmdPick.loading = true
	sk, filter := cmdPick.sk, cmdPick.filter
	go func() {
		rows, note := cmdPickReadDir(dir, sk, filter)
		tbxPost(func() {
			if cmdPick.gen != gen || cmdDeck.mode != cmdModePick {
				return
			}
			cmdPickSetRows(rows)
			cmdPick.note = note
		})
	}()
}

// cmdPickReadDir builds a folder's rows. Off the render thread.
func cmdPickReadDir(dir string, sk cmdSlotKind, filter string) ([]cmdPickRow, string) {
	var rows []cmdPickRow
	if sk == cmdSlotFolder {
		rows = append(rows, cmdPickRow{label: "Use this folder", detail: dir, value: dir, use: true})
	}
	rows = append(rows, cmdPickRow{label: "..", detail: "Up one folder", up: true})
	f, err := os.Open(dir)
	if err != nil {
		return rows, filesFriendlyError(err)
	}
	entries, err := f.ReadDir(cmdPickMaxEntries)
	_ = f.Close()
	note := ""
	if err != nil && len(entries) == 0 {
		return rows, filesFriendlyError(err)
	}
	if len(entries) >= cmdPickMaxEntries {
		note = "First " + itoaSmall(cmdPickMaxEntries) + " items shown"
	}
	var dirs, files []cmdPickRow
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if info, serr := os.Stat(path); serr == nil && info.IsDir() {
				isDir = true
			}
		}
		if isDir {
			dirs = append(dirs, cmdPickRow{label: name, value: path, dir: true})
			continue
		}
		if sk != cmdSlotFile || (filter == "log" && !cmdPickLogName(name)) || (filter == "text" && !cmdPickTextName(name)) {
			continue
		}
		detail := ""
		if info, ierr := e.Info(); ierr == nil {
			detail = formatBytesShort(info.Size())
		}
		files = append(files, cmdPickRow{label: name, value: path, detail: detail})
	}
	byName := func(rs []cmdPickRow) {
		sort.Slice(rs, func(i, j int) bool { return strings.ToLower(rs[i].label) < strings.ToLower(rs[j].label) })
	}
	byName(dirs)
	byName(files)
	rows = append(rows, dirs...)
	rows = append(rows, files...)
	if sk == cmdSlotFile && len(files) == 0 && note == "" {
		switch filter {
		case "log":
			note = "No logs here"
		case "text":
			note = "No text files here"
		default:
			note = "No files here"
		}
	}
	return rows, note
}

// formatBytesShort is a size in the units a file list uses: formatSize
// (clockformat.go), the one size format.
func formatBytesShort(n int64) string { return formatSize(n) }

// cmdPickLoadProcs samples the running processes off the render thread.
func cmdPickLoadProcs() {
	gen := cmdPick.gen + 1
	cmdPick.gen = gen
	cmdPick.loading = true
	go func() {
		procs, err := collectProcesses(cmdPickMaxProcs)
		var rows []cmdPickRow
		for _, p := range procs {
			if processIsProtected(p.PID) {
				continue
			}
			detail := "PID " + strconv.Itoa(int(p.PID))
			if p.CPU >= 0.1 {
				detail += " · " + strconv.FormatFloat(p.CPU, 'f', 1, 64) + "% CPU"
			}
			rows = append(rows, cmdPickRow{label: p.Name, detail: detail, value: strconv.Itoa(int(p.PID))})
		}
		note := ""
		if err != nil {
			note = "Could not list processes: " + err.Error()
		}
		tbxPost(func() {
			if cmdPick.gen != gen || cmdDeck.mode != cmdModePick {
				return
			}
			cmdPickSetRows(rows)
			cmdPick.note = note
		})
	}()
}

// cmdPickOpenMacro opens the history list to build a macro from.
func cmdPickOpenMacro() {
	if len(term.history) == 0 {
		PlayErrorSound()
		ShowToast("Run a few commands first — a macro is made from them", ToastKindInfo)
		return
	}
	cmdPickReset()
	cmdPick.kind = cmdPickMacro
	cmdPick.title = "New macro"
	cmdPick.note = ""
	cmdPick.dir = ""
	for i := len(term.history) - 1; i >= 0 && len(cmdPick.rows) < cmdMacroHistory; i-- {
		cmdPick.rows = append(cmdPick.rows, cmdPickRow{label: term.history[i], value: term.history[i]})
	}
	cmdPick.checked = make([]bool, len(cmdPick.rows))
	cmdPick.cursor, cmdPick.first = 0, 0
	cmdDeck.mode = cmdModePick
	PlayActivateSound()
}

// cmdPickMacroCount is how many rows are ticked.
func cmdPickMacroCount() int {
	n := 0
	for _, c := range cmdPick.checked {
		if c {
			n++
		}
	}
	return n
}

// cmdPickMacroSave saves the ticked commands, oldest first (the order they
// were run in), as one favorite.
func cmdPickMacroSave() {
	var lines []string
	for i := len(cmdPick.rows) - 1; i >= 0; i-- {
		if i < len(cmdPick.checked) && cmdPick.checked[i] {
			lines = append(lines, cmdPick.rows[i].value)
		}
	}
	if len(lines) == 0 {
		PlayErrorSound()
		ShowToast("Tick the commands to put in the macro first", ToastKindInfo)
		return
	}
	if len(lines) > cmdFavMaxLines {
		lines = lines[len(lines)-cmdFavMaxLines:]
	}
	cmdPickReset()
	cmdDeck.mode = cmdModeBrowse
	if cmdFavAdd("", lines) {
		PlayActivateSound()
		ShowToast("Macro saved to Favorites", ToastKindSuccess)
	} else {
		ShowToast("That macro is already saved", ToastKindInfo)
	}
	cmdDeckRebuild()
	if c := cmdDeckCatIndex("fav"); c >= 0 {
		cmdDeck.cat, cmdDeck.cursor, cmdDeck.first = c, 0, 0
	}
}

// cmdPickClose returns to the screen under the picker.
func cmdPickClose() {
	back := cmdModeForm
	if cmdPick.kind == cmdPickMacro {
		back = cmdModeBrowse
	}
	cmdPickReset()
	cmdDeck.mode = back
	PlayBackSound()
}

// cmdPickUp goes to the parent folder; a filesystem root or the top of a
// location goes to the top level.
func cmdPickUp() {
	if cmdPick.kind != cmdPickSlot || (cmdPick.sk != cmdSlotFolder && cmdPick.sk != cmdSlotFile) {
		return
	}
	if cmdPick.dir == "" {
		PlayErrorSound()
		return
	}
	parent := filepath.Dir(cmdPick.dir)
	PlayNavSound()
	if parent == cmdPick.dir || (runtime.GOOS == "windows" && strings.HasSuffix(cmdPick.dir, `:\`)) {
		cmdPickTop()
		return
	}
	cmdPickLoadDir(parent)
}

// cmdPickChoose is A on a row.
func cmdPickChoose() {
	if cmdPick.loading || cmdPick.cursor < 0 || cmdPick.cursor >= len(cmdPick.rows) {
		return
	}
	r := cmdPick.rows[cmdPick.cursor]
	if cmdPick.kind == cmdPickMacro {
		if cmdPick.cursor < len(cmdPick.checked) {
			if !cmdPick.checked[cmdPick.cursor] && cmdPickMacroCount() >= cmdFavMaxLines {
				PlayErrorSound()
				return
			}
			cmdPick.checked[cmdPick.cursor] = !cmdPick.checked[cmdPick.cursor]
			PlayNavSound()
		}
		return
	}
	switch {
	case r.up:
		cmdPickUp()
	case r.dir:
		PlayNavSound()
		cmdPickLoadDir(r.value)
	default:
		slot := cmdPick.slot
		cmdPickReset()
		PlayActivateSound()
		cmdDeckSetValue(slot, r.value)
		cmdDeckPickNextEmpty()
	}
}

// cmdPickUseFolder is Y in a folder picker: the folder on screen.
func cmdPickUseFolder() {
	if cmdPick.kind != cmdPickSlot || cmdPick.sk != cmdSlotFolder || cmdPick.dir == "" {
		PlayErrorSound()
		return
	}
	slot, dir := cmdPick.slot, cmdPick.dir
	cmdPickReset()
	PlayActivateSound()
	cmdDeckSetValue(slot, dir)
	cmdDeckPickNextEmpty()
}

func cmdPickMove(delta int) {
	n := len(cmdPick.rows)
	if n == 0 {
		return
	}
	next := clampIndex(cmdPick.cursor+delta, n)
	if next != cmdPick.cursor {
		cmdPick.cursor = next
		PlayNavSound()
	}
}

func cmdPickPage() int {
	if cmdPick.visible > 1 {
		return cmdPick.visible - 1
	}
	return 5
}

// cmdPickPad is the pad in a picker.
func cmdPickPad(b sdl.GameControllerButton) {
	switch b {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		cmdPickClose()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		cmdPickMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		cmdPickMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		cmdPickUp()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if cmdPick.cursor >= 0 && cmdPick.cursor < len(cmdPick.rows) && cmdPick.rows[cmdPick.cursor].dir {
			cmdPickChoose()
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		cmdPickMove(-cmdPickPage())
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		cmdPickMove(cmdPickPage())
	case sdl.CONTROLLER_BUTTON_A:
		cmdPickChoose()
	case sdl.CONTROLLER_BUTTON_Y:
		cmdPickUseFolder()
	case sdl.CONTROLLER_BUTTON_START:
		if cmdPick.kind == cmdPickMacro {
			cmdPickMacroSave()
		} else {
			cmdPickChoose()
		}
	}
}

// cmdPickKey is the keyboard in a picker.
func cmdPickKey(e *sdl.KeyboardEvent) {
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE:
		cmdPickClose()
	case sdl.K_UP:
		cmdPickMove(-1)
	case sdl.K_DOWN:
		cmdPickMove(1)
	case sdl.K_PAGEUP:
		cmdPickMove(-cmdPickPage())
	case sdl.K_PAGEDOWN:
		cmdPickMove(cmdPickPage())
	case sdl.K_HOME:
		cmdPickMove(-len(cmdPick.rows))
	case sdl.K_END:
		cmdPickMove(len(cmdPick.rows))
	case sdl.K_LEFT, sdl.K_BACKSPACE:
		cmdPickUp()
	case sdl.K_RIGHT:
		if cmdPick.cursor >= 0 && cmdPick.cursor < len(cmdPick.rows) && cmdPick.rows[cmdPick.cursor].dir {
			cmdPickChoose()
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if ctrl && cmdPick.kind == cmdPickMacro {
			cmdPickMacroSave()
		} else if ctrl {
			cmdPickUseFolder()
		} else {
			cmdPickChoose()
		}
	case sdl.K_SPACE:
		if cmdPick.kind == cmdPickMacro {
			cmdPickChoose()
		}
	}
}

// cmdPickClick selects a row, chooses the row already selected, and closes
// the picker on a click outside the card.
func cmdPickClick(mx, my int32) bool {
	if cmdDeck.card.W <= 0 || !pointInRect(mx, my, cmdDeck.card) {
		cmdPickClose()
		return true
	}
	for i, r := range cmdPick.rowRects {
		if !pointInRect(mx, my, r) {
			continue
		}
		idx := cmdPick.rowsFirst + i
		if idx == cmdPick.cursor {
			cmdPickChoose()
		} else if idx >= 0 && idx < len(cmdPick.rows) {
			cmdPick.cursor = idx
			PlayNavSound()
		}
		return true
	}
	return true
}
