package main

// tasks_ui.go — the Tasks app on the handheld: the lists on the left, the
// shown list's tasks on the right.
//
// The left column starts with Today — every list's tasks due today or before,
// in one place — then the lists with their open counts, "New list", and "From
// your phone", which shows the code that opens the Phone Companion's Tasks
// card. The right column starts with "Add a task", then the open tasks (by
// due, then in the order they were added), then a "Done" row that shows or
// hides the finished ones, dimmed and struck through. An overdue task's
// checkbox and date are in the warning colour.
//
// Controls: Up/Down choose; Left/Right move between the columns; A ticks a
// task off (a repeating one moves to its next date instead), adds on "Add a
// task", opens a list in the left column; Y adds a task to the shown list
// (Today adds to the first list, due today); X opens the task's sheet, or a
// list's sheet in the left column (tasks_sheet.go); L1/R1 switch lists; B
// goes back. A keyboard has the twins: arrows, Enter or Space, N or Y, X,
// PgUp/PgDn, Esc, Tab between the columns, and Delete pressed twice to delete
// a task. E is not "edit": main.go's global Q/E scene keys run after a scene
// has used the key, so E would also leave the app.
//
// The screen draws from a copy of the store (tasksSnapshot) taken whenever a
// task changed — on the handheld or from the phone — or the minute turned, so
// a task typed on the phone appears on the next frame.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The two columns.
const (
	tasksPaneLists = iota
	tasksPaneItems
)

// tasksTodayID is the Today view's id where a list id goes.
const tasksTodayID = -1

// The left column's kinds of entry.
const (
	tasksEntToday = iota
	tasksEntList
	tasksEntNewList
	tasksEntPhone
)

// The right column's kinds of row.
const (
	tasksRowAdd = iota
	tasksRowItem
	tasksRowDone
)

// tasksDeleteWindow is how long the first press of a two-press delete waits
// for the second, as on the alarm list.
const tasksDeleteWindow = 3 * time.Second

// tasksEntry is one entry of the left column. count is the open tasks (for
// Today, the ones due by today).
type tasksEntry struct {
	kind    int
	id      int
	name    string
	count   int
	overdue int
}

// tasksRow is one row of the right column, with its due worked out once per
// rebuild rather than every frame.
type tasksRow struct {
	kind    int
	item    taskItem
	due     tasksDue
	label   string
	overdue bool
	list    string
}

// tasksHit is a drawn row's tap target; check is a task's checkbox area.
type tasksHit struct {
	rect  sdl.Rect
	idx   int
	check sdl.Rect
}

// tasksUI is the screen's state. Main thread only.
var tasksUI struct {
	built    bool
	rev      uint64
	minute   int64
	lists    []taskList
	items    []taskItem
	entries  []tasksEntry
	rows     []tasksRow
	doneN    int
	todayDue int

	pane          int
	cur           int // the shown list's id, or tasksTodayID
	entCursor     int
	rowCursor     int
	focusID       int // the task the cursor follows across rebuilds (0: none)
	showDone      bool
	scroll        int32
	entScroll     int32
	ensureVisible bool

	sheet   int
	pending func()
	delArm  struct {
		at time.Time
		id int
	}

	entHits []tasksHit
	rowHits []tasksHit
}

// ── Contract ──────────────────────────────────────────────────────────────

// tasksEnter opens the app: no sheet, the tasks column focused, on the list
// shown last — or, the first time, Today when something is due, else the
// first list.
func tasksEnter(config *Config) {
	tasksUI.sheet = tasksSheetNone
	tasksUI.pending = nil
	tasksUI.pane = tasksPaneItems
	tasksUI.delArm.at = time.Time{}
	tasksUI.built = false
	tasksSyncView()
	if tasksUI.cur == 0 {
		tasksUI.cur = tasksUI.lists[0].ID
		if tasksUI.todayDue > 0 {
			tasksUI.cur = tasksTodayID
		}
		tasksUI.rowCursor, tasksUI.focusID = 1, 0
		tasksUI.built = false
		tasksSyncView()
	}
	tasksUI.ensureVisible = true
}

// tasksLeave closes the sheets and saves now rather than in two seconds.
func tasksLeave() {
	tasksUI.sheet = tasksSheetNone
	tasksUI.pending = nil
	tasksSaveNow(false)
}

// tasksFlush saves before JukaHub exits, waiting for the write. An alarm
// change the phone queued in the last second is carried out first: the queue
// is not saved, and a deleted task's alarm would otherwise stay behind.
func tasksFlush() {
	tasksAlarmApply(time.Now())
	tasksSaveNow(true)
}

// tasksFooterHints is the footer row for what has the focus.
func tasksFooterHints() []FooterHint {
	if tasksUI.sheet != tasksSheetNone {
		return tasksSheetFooterHints()
	}
	back := FooterHint{Button: "B", Label: "Back"}
	add := FooterHint{Button: "Y / N", Label: "Add"}
	lists := FooterHint{Button: "L1/R1 / PgUp/PgDn", Label: "Lists"}
	if tasksUI.pane == tasksPaneLists {
		hints := []FooterHint{{Button: "D-Pad", Label: "Choose"}}
		if e, ok := tasksCurEntry(); ok {
			switch e.kind {
			case tasksEntToday, tasksEntList:
				hints = append(hints, FooterHint{Button: "A", Label: "Open"})
				if e.kind == tasksEntList {
					hints = append(hints, FooterHint{Button: "X", Label: "List options"})
				}
			case tasksEntNewList:
				hints = append(hints, FooterHint{Button: "A", Label: "New list"})
			case tasksEntPhone:
				hints = append(hints, FooterHint{Button: "A", Label: "Show code"})
			}
		}
		return append(hints, add, back)
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Choose"}}
	if row, ok := tasksCurRow(); ok {
		switch row.kind {
		case tasksRowAdd:
			hints = append(hints, FooterHint{Button: "A", Label: "Add"})
		case tasksRowDone:
			label := "Show done"
			if tasksUI.showDone {
				label = "Hide done"
			}
			hints = append(hints, FooterHint{Button: "A", Label: label})
		case tasksRowItem:
			label := "Done"
			if row.item.Done {
				label = "Not done"
			}
			hints = append(hints, FooterHint{Button: "A", Label: label}, FooterHint{Button: "X", Label: "Edit"})
		}
	}
	return append(hints, add, lists, back)
}

// tasksClick answers a tap: a sheet takes them all; a column entry opens; a
// task's checkbox ticks it and the rest of its row opens its sheet.
func tasksClick(mx, my int32, config *Config) bool {
	if tasksUI.sheet != tasksSheetNone {
		return tasksSheetClick(mx, my)
	}
	for _, h := range tasksUI.entHits {
		if h.idx < len(tasksUI.entries) && pointInRect(mx, my, h.rect) {
			tasksUI.pane = tasksPaneLists
			tasksUI.entCursor = h.idx
			tasksActivate()
			return true
		}
	}
	for _, h := range tasksUI.rowHits {
		if h.idx >= len(tasksUI.rows) || !pointInRect(mx, my, h.rect) {
			continue
		}
		tasksUI.pane = tasksPaneItems
		tasksUI.rowCursor = h.idx
		tasksUI.focusID = tasksRowID(h.idx)
		if row := tasksUI.rows[h.idx]; row.kind == tasksRowItem && !pointInRect(mx, my, h.check) {
			tasksOpenItemSheet(row.item.ID)
		} else {
			tasksActivate()
		}
		return true
	}
	return false
}

// tasksWheel scrolls the tasks; a sheet swallows the wheel.
func tasksWheel(y int32) bool {
	if tasksUI.sheet != tasksSheetNone {
		return true
	}
	if y == 0 {
		return false
	}
	tasksUI.scroll -= y * 48
	return true
}

// ── View ──────────────────────────────────────────────────────────────────

// tasksSyncView rebuilds the columns when a task changed (here or on the
// phone) or the minute turned: a task can become overdue, and "Today" can
// become "Yesterday". Main thread.
func tasksSyncView() {
	now := time.Now()
	minute := now.Unix() / 60
	if tasksUI.built && tasksUI.rev == tasksRev.Load() && tasksUI.minute == minute {
		return
	}
	lists, items, rev := tasksSnapshot()
	tasksUI.built, tasksUI.rev, tasksUI.minute = true, rev, minute
	tasksUI.lists, tasksUI.items = lists, items
	tasksBuildEntries(now)
	tasksBuildRows(now)
}

// tasksBuildEntries lays out the left column and keeps the shown list one
// that exists.
func tasksBuildEntries(now time.Time) {
	all := tasksCount(tasksUI.items, 0, now)
	tasksUI.todayDue = all.due
	e := tasksUI.entries[:0]
	e = append(e, tasksEntry{kind: tasksEntToday, id: tasksTodayID, name: "Today", count: all.due, overdue: all.overdue})
	for _, l := range tasksUI.lists {
		c := tasksCount(tasksUI.items, l.ID, now)
		e = append(e, tasksEntry{kind: tasksEntList, id: l.ID, name: l.Name, count: c.open, overdue: c.overdue})
	}
	e = append(e, tasksEntry{kind: tasksEntNewList, name: "New list"}, tasksEntry{kind: tasksEntPhone, name: "From your phone"})
	tasksUI.entries = e
	if tasksUI.cur != 0 && tasksUI.cur != tasksTodayID && tasksEntryIndex(tasksUI.cur) < 0 && len(tasksUI.lists) > 0 {
		tasksUI.cur = tasksUI.lists[0].ID
	}
	if tasksUI.pane == tasksPaneItems {
		if i := tasksEntryIndex(tasksUI.cur); i >= 0 {
			tasksUI.entCursor = i
		}
	}
	tasksUI.entCursor = tasksClamp(tasksUI.entCursor, 0, len(e)-1)
}

// tasksBuildRows lays out the right column for the shown list. Today holds
// the open tasks due by today and the tasks finished today.
func tasksBuildRows(now time.Time) {
	var view []taskItem
	today := tasksDayIndex(now)
	for _, it := range tasksUI.items {
		if tasksUI.cur == tasksTodayID {
			d, ok := tasksParseDue(it.Due)
			open := !it.Done && ok && d.byDay(now)
			doneToday := it.Done && it.DoneAt > 0 && tasksDayIndex(time.Unix(it.DoneAt, 0)) == today
			if !open && !doneToday {
				continue
			}
		} else if it.List != tasksUI.cur {
			continue
		}
		view = append(view, it)
	}
	tasksSortItems(view)
	rows := append(tasksUI.rows[:0], tasksRow{kind: tasksRowAdd})
	doneN := 0
	for _, it := range view {
		if it.Done {
			doneN++
		}
	}
	header := false
	for _, it := range view {
		if it.Done && !header {
			header = true
			rows = append(rows, tasksRow{kind: tasksRowDone})
			if !tasksUI.showDone {
				break
			}
		}
		row := tasksRow{kind: tasksRowItem, item: it}
		if d, ok := tasksParseDue(it.Due); ok {
			row.due, row.label = d, tasksDueLabel(appConfig, d, now)
			row.overdue = !it.Done && d.overdue(now)
		}
		if tasksUI.cur == tasksTodayID {
			row.list = tasksListName(it.List)
		}
		rows = append(rows, row)
	}
	tasksUI.rows, tasksUI.doneN = rows, doneN
	// The cursor follows the task it was on; with that task gone (or ticked
	// off on purpose, focusID 0) it keeps its place and the next task slides
	// under it.
	cursor := tasksClamp(tasksUI.rowCursor, 0, len(rows)-1)
	if tasksUI.focusID != 0 {
		for i, r := range rows {
			if r.kind == tasksRowItem && r.item.ID == tasksUI.focusID {
				if i != cursor {
					tasksUI.ensureVisible = true
				}
				cursor = i
				break
			}
		}
	}
	tasksUI.rowCursor = cursor
	tasksUI.focusID = tasksRowID(cursor)
}

// tasksClamp keeps v inside [lo, hi] (lo when the range is empty).
func tasksClamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

// tasksRowID is the task on row i, 0 for another kind of row.
func tasksRowID(i int) int {
	if i >= 0 && i < len(tasksUI.rows) && tasksUI.rows[i].kind == tasksRowItem {
		return tasksUI.rows[i].item.ID
	}
	return 0
}

// tasksEntryIndex is the left column's entry for list id (or Today), or -1.
func tasksEntryIndex(id int) int {
	for i, e := range tasksUI.entries {
		if (e.kind == tasksEntToday || e.kind == tasksEntList) && e.id == id {
			return i
		}
	}
	return -1
}

// tasksCurEntry is the left column's focused entry.
func tasksCurEntry() (tasksEntry, bool) {
	if i := tasksUI.entCursor; i >= 0 && i < len(tasksUI.entries) {
		return tasksUI.entries[i], true
	}
	return tasksEntry{}, false
}

// tasksCurRow is the right column's focused row.
func tasksCurRow() (tasksRow, bool) {
	if i := tasksUI.rowCursor; i >= 0 && i < len(tasksUI.rows) {
		return tasksUI.rows[i], true
	}
	return tasksRow{}, false
}

// tasksListName is list id's name, "" when there is none.
func tasksListName(id int) string {
	for _, l := range tasksUI.lists {
		if l.ID == id {
			return l.Name
		}
	}
	return ""
}

// ── Actions ───────────────────────────────────────────────────────────────

// tasksMove is Up/Down in the focused column. It reports false at the very
// top, where Up goes on to the header bar.
func tasksMove(delta int) bool {
	tasksUI.delArm.at = time.Time{}
	if tasksUI.pane == tasksPaneLists {
		next := tasksUI.entCursor + delta
		if next < 0 && tasksUI.entCursor == 0 {
			return false
		}
		next = tasksClamp(next, 0, len(tasksUI.entries)-1)
		if next == tasksUI.entCursor {
			return true
		}
		tasksUI.entCursor = next
		if e := tasksUI.entries[next]; e.kind == tasksEntToday || e.kind == tasksEntList {
			tasksShowList(e.id)
		}
		PlayNavSound()
		return true
	}
	next := tasksUI.rowCursor + delta
	if next < 0 && tasksUI.rowCursor == 0 {
		return false
	}
	next = tasksClamp(next, 0, len(tasksUI.rows)-1)
	if next == tasksUI.rowCursor {
		return true
	}
	tasksUI.rowCursor = next
	tasksUI.focusID = tasksRowID(next)
	tasksUI.ensureVisible = true
	PlayNavSound()
	return true
}

// tasksMoveEnd jumps to the first (dir < 0) or last row of the column.
func tasksMoveEnd(dir int) {
	n := len(tasksUI.rows)
	if tasksUI.pane == tasksPaneLists {
		n = len(tasksUI.entries)
	}
	if dir < 0 {
		tasksMove(-n)
	} else {
		tasksMove(n)
	}
}

// tasksFocusPane moves the focus to a column; the left column's cursor
// starts on the shown list.
func tasksFocusPane(p int) {
	if p == tasksUI.pane {
		return
	}
	tasksUI.pane = p
	tasksUI.delArm.at = time.Time{}
	if p == tasksPaneLists {
		if i := tasksEntryIndex(tasksUI.cur); i >= 0 {
			tasksUI.entCursor = i
		}
	} else {
		tasksUI.ensureVisible = true
	}
	PlayNavSound()
}

// tasksShowList shows list id (or Today) in the right column, from its top.
func tasksShowList(id int) {
	if id == tasksUI.cur {
		return
	}
	tasksUI.cur = id
	tasksUI.rowCursor, tasksUI.focusID, tasksUI.scroll = 1, 0, 0
	tasksUI.ensureVisible = true
	tasksUI.built = false
}

// tasksStepList is L1/R1: the previous or next list, Today included, round.
func tasksStepList(delta int) {
	var ids []int
	at := 0
	for _, e := range tasksUI.entries {
		if e.kind != tasksEntToday && e.kind != tasksEntList {
			continue
		}
		if e.id == tasksUI.cur {
			at = len(ids)
		}
		ids = append(ids, e.id)
	}
	if len(ids) < 2 {
		return
	}
	id := ids[((at+delta)%len(ids)+len(ids))%len(ids)]
	tasksShowList(id)
	if i := tasksEntryIndex(id); i >= 0 {
		tasksUI.entCursor = i
	}
	PlayNavSound()
}

// tasksActivate is A on the focused entry or row.
func tasksActivate() {
	if tasksUI.pane == tasksPaneLists {
		e, ok := tasksCurEntry()
		if !ok {
			return
		}
		switch e.kind {
		case tasksEntToday, tasksEntList:
			tasksShowList(e.id)
			tasksUI.pane = tasksPaneItems
			PlayActivateSound()
		case tasksEntNewList:
			tasksPromptNewList()
		case tasksEntPhone:
			tasksOpenPhone()
		}
		return
	}
	row, ok := tasksCurRow()
	if !ok {
		return
	}
	switch row.kind {
	case tasksRowAdd:
		tasksPromptAdd()
	case tasksRowDone:
		tasksUI.showDone = !tasksUI.showDone
		tasksUI.built = false
		PlayToggleSound()
	case tasksRowItem:
		tasksToggleRow(row)
	}
}

// tasksToggleRow ticks a task off or back on, and says where a repeating one
// went.
func tasksToggleRow(row tasksRow) {
	done, next, err := tasksToggle(row.item.ID)
	if err != nil {
		ShowToast(err.Error(), ToastKindWarning)
		PlayErrorSound()
		return
	}
	tasksAlarmApply(time.Now())
	tasksUI.focusID = 0
	switch {
	case next.set():
		ShowToast("Done — next: "+tasksDueLabel(appConfig, next, time.Now()), ToastKindSuccess)
		PlaySuccessSound()
	case done:
		PlaySuccessSound()
	default:
		PlayToggleSound()
	}
}

// tasksEdit is X: the focused task's sheet, or the focused list's.
func tasksEdit() {
	if tasksUI.pane == tasksPaneLists {
		e, ok := tasksCurEntry()
		switch {
		case !ok:
		case e.kind == tasksEntList:
			tasksOpenListSheet(e.id)
		case e.kind == tasksEntToday:
			ShowToast("Today gathers the tasks due by today from every list", ToastKindInfo)
		}
		return
	}
	if row, ok := tasksCurRow(); ok && row.kind == tasksRowItem {
		tasksOpenItemSheet(row.item.ID)
	}
}

// tasksPrompt opens the keyboard on the next frame. A key that opens it (N,
// Y) also sends its letter as text right after the key; opened at once, the
// keyboard would take that letter as the first one typed.
func tasksPrompt(open func()) {
	tasksUI.pending = open
	PlayActivateSound()
}

// tasksPromptAdd asks for a new task for the shown list. From Today it goes
// to the first list, due today, so it shows where it was added.
func tasksPromptAdd() {
	list, due := tasksUI.cur, ""
	if list == tasksTodayID {
		list, due = 0, tasksDueOn(time.Now()).String()
	}
	title := "New task"
	if name := tasksListName(list); name != "" {
		title = "New task in " + name
	}
	tasksPrompt(func() {
		osPromptOpen(title, "Buy milk", "Add", "", tasksTextMax, func(text string) {
			if text == "" {
				return
			}
			id, err := tasksAddItem(list, text, due)
			if err != nil {
				ShowToast(err.Error(), ToastKindWarning)
				return
			}
			tasksUI.focusID = id
			tasksUI.pane = tasksPaneItems
			tasksUI.ensureVisible = true
			PlaySuccessSound()
		})
	})
}

// tasksPromptNewList asks for a new list's name and shows the list.
func tasksPromptNewList() {
	tasksPrompt(func() {
		osPromptOpen("New list", "Groceries", "Add", "", tasksListNameMax, func(name string) {
			if name == "" {
				return
			}
			id, err := tasksAddList(name)
			if err != nil {
				ShowToast(err.Error(), ToastKindWarning)
				return
			}
			tasksShowList(id)
			tasksUI.pane = tasksPaneItems
			PlaySuccessSound()
		})
	})
}

// tasksOpenPhone shows the code that opens the companion page at the Tasks
// card; the first task that arrives closes it (tasksPhoneNews).
func tasksOpenPhone() {
	if phoneCardOpen(phoneCardTasks, "Type tasks on your phone",
		"Scan, then type in the Tasks card: each task appears here at once") {
		PlayActivateSound()
	}
}

// tasksRequestDelete is the keyboard's Delete on a task: once to arm, again
// within tasksDeleteWindow to delete.
func tasksRequestDelete() {
	row, ok := tasksCurRow()
	if !ok || row.kind != tasksRowItem {
		return
	}
	id := row.item.ID
	if tasksUI.delArm.at.IsZero() || tasksUI.delArm.id != id || time.Since(tasksUI.delArm.at) > tasksDeleteWindow {
		tasksUI.delArm.at, tasksUI.delArm.id = time.Now(), id
		ShowToast("Press Delete again to delete \""+truncateRunes(row.item.Text, 40)+"\"", ToastKindWarning)
		PlayNavSound()
		return
	}
	tasksUI.delArm.at = time.Time{}
	tasksDeleteTask(id)
}

// tasksDeleteTask deletes a task and its alarm.
func tasksDeleteTask(id int) {
	if err := tasksDeleteItem(id); err != nil {
		ShowToast(err.Error(), ToastKindWarning)
		return
	}
	tasksAlarmApply(time.Now())
	tasksUI.focusID = 0
	PlayBackSound()
	ShowToast("Task deleted", ToastKindInfo)
}

// ── Input ─────────────────────────────────────────────────────────────────

// tasksHandlePad is the pad on the main view; a sheet takes it while open.
func tasksHandlePad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type == sdl.CONTROLLERBUTTONUP {
		return false
	}
	if tasksUI.sheet != tasksSheetNone {
		return tasksSheetPad(e.Button)
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if !tasksMove(-1) {
			return focusChromeBar()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		tasksMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		tasksFocusPane(tasksPaneLists)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		tasksFocusPane(tasksPaneItems)
	case sdl.CONTROLLER_BUTTON_A:
		tasksActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		tasksPromptAdd()
	case sdl.CONTROLLER_BUTTON_X:
		tasksEdit()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		tasksStepList(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		tasksStepList(1)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// tasksHandleKey is the keyboard on the main view; a sheet takes it while
// open.
func tasksHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if tasksUI.sheet != tasksSheetNone {
		return tasksSheetKey(e)
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if !tasksMove(-1) {
			return focusChromeBar()
		}
	case sdl.K_DOWN:
		tasksMove(1)
	case sdl.K_LEFT:
		tasksFocusPane(tasksPaneLists)
	case sdl.K_RIGHT:
		tasksFocusPane(tasksPaneItems)
	case sdl.K_TAB:
		tasksFocusPane(1 - tasksUI.pane)
	case sdl.K_HOME:
		tasksMoveEnd(-1)
	case sdl.K_END:
		tasksMoveEnd(1)
	case sdl.K_PAGEUP:
		tasksStepList(-1)
	case sdl.K_PAGEDOWN:
		tasksStepList(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		// Once per press: a held key's auto-repeat would tick a task off
		// and straight back on.
		if e.Repeat == 0 {
			tasksActivate()
		}
	case sdl.K_n, sdl.K_y:
		if e.Repeat == 0 {
			tasksPromptAdd()
		}
	case sdl.K_x:
		if e.Repeat == 0 {
			tasksEdit()
		}
	case sdl.K_DELETE:
		if e.Repeat == 0 && tasksUI.pane == tasksPaneItems {
			tasksRequestDelete()
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	default:
		return false
	}
	return true
}

// ── Drawing ───────────────────────────────────────────────────────────────

// tasksRender draws the screen: the two columns between the shared bars, and
// a sheet over them when one is open.
func tasksRender(renderer *sdl.Renderer, config *Config) {
	if open := tasksUI.pending; open != nil {
		tasksUI.pending = nil
		open()
	}
	if tasksUI.cur == 0 {
		// Drawn before tasksEnter ran: start the way it would.
		tasksEnter(config)
	}
	tasksSyncView()
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	font := FontForRole(config, TypeBody)
	small := FontForRole(config, TypeLabel)
	head := FontForRole(config, TypeHeading)
	if head == nil {
		head = font
	}
	if font == nil || small == nil {
		renderStatusBar(renderer, config)
		renderFooter(renderer, config)
		return
	}
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + 10
	bottom := screenHeight - footerBarHeight() - 10
	leftW := min(max(screenWidth*27/100, 180), 320)
	gap := int32(14)
	accent := glassAccent()
	tasksDrawEntries(renderer, font, small, sdl.Rect{X: margin, Y: top, W: leftW, H: bottom - top}, accent)
	tasksDrawRows(renderer, config, font, small, head,
		sdl.Rect{X: margin + leftW + gap, Y: top, W: screenWidth - 2*margin - leftW - gap, H: bottom - top}, accent)
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if tasksUI.sheet != tasksSheetNone {
		tasksDrawSheet(renderer, config, font, small, head)
	}
}

// tasksClipRect is the part of r inside clip's rows.
func tasksClipRect(r, clip sdl.Rect) sdl.Rect {
	if r.Y < clip.Y {
		r.H -= clip.Y - r.Y
		r.Y = clip.Y
	}
	if r.Y+r.H > clip.Y+clip.H {
		r.H = clip.Y + clip.H - r.Y
	}
	if r.H < 0 {
		r.H = 0
	}
	return r
}

// tasksDrawEntries draws the left column: Today, the lists with their
// counts (in the warning colour when something in them is overdue), New list
// and From your phone.
func tasksDrawEntries(renderer *sdl.Renderer, font, small *ttf.Font, area sdl.Rect, accent sdl.Color) {
	tasksUI.entHits = tasksUI.entHits[:0]
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, area.X, area.Y, area.W, area.H, glassRowRadius+4, glassA(8))
	inner := sdl.Rect{X: area.X + 8, Y: area.Y + 8, W: area.W - 16, H: area.H - 16}
	rowH := int32(font.Height()) + 18
	step := rowH + 6
	if tasksUI.pane == tasksPaneLists {
		cy := int32(tasksUI.entCursor) * step
		if cy < tasksUI.entScroll {
			tasksUI.entScroll = cy
		}
		if cy+rowH > tasksUI.entScroll+inner.H {
			tasksUI.entScroll = cy + rowH - inner.H
		}
	}
	maxScroll := max(int32(len(tasksUI.entries))*step-6-inner.H, 0)
	tasksUI.entScroll = min(max(tasksUI.entScroll, 0), maxScroll)
	renderWithClip(renderer, inner.X, inner.Y, inner.W, inner.H, func(r *sdl.Renderer) {
		for i, e := range tasksUI.entries {
			y := inner.Y + int32(i)*step - tasksUI.entScroll
			if y+rowH < inner.Y || y > inner.Y+inner.H {
				continue
			}
			row := sdl.Rect{X: inner.X, Y: y, W: inner.W, H: rowH}
			isList := e.kind == tasksEntToday || e.kind == tasksEntList
			switch {
			case tasksUI.pane == tasksPaneLists && i == tasksUI.entCursor:
				UIListRow(r, row, glassRowRadius, true, false)
			case isList && e.id == tasksUI.cur:
				fillRoundedRect(r, row.X, row.Y, row.W, row.H, glassRowRadius, WithAlpha(accent, 40))
			case sysToolPointIn(row):
				UIListRow(r, row, glassRowRadius, false, true)
			}
			glyph, ink := "checklist", ColorTextPrimary()
			switch e.kind {
			case tasksEntToday:
				glyph = "calendar"
			case tasksEntNewList:
				glyph, ink = "plus", ColorTextSecondary()
			case tasksEntPhone:
				glyph, ink = "phone", ColorTextSecondary()
			}
			gs := int32(small.Height()) + 2
			iconDraw(r, glyph, sdl.Rect{X: row.X + 12, Y: row.Y + (rowH-gs)/2, W: gs, H: gs}, ink)
			textX := row.X + 12 + gs + 10
			right := row.X + row.W - 12
			if isList && e.count > 0 {
				num := strconv.Itoa(e.count)
				nw, nh := measureText(small, num)
				col := ColorTextTertiary()
				if e.overdue > 0 {
					col = WarningInk()
				}
				drawText(r, small, num, right-nw, row.Y+(rowH-nh)/2, col, textAlignLeft)
				right -= nw + 10
			}
			name := ellipsize(font, e.name, right-textX)
			_, th := measureText(font, name)
			drawText(r, font, name, textX, row.Y+(rowH-th)/2, ink, textAlignLeft)
			if vis := tasksClipRect(row, inner); vis.H > 0 {
				tasksUI.entHits = append(tasksUI.entHits, tasksHit{rect: vis, idx: i})
			}
		}
	})
}

// tasksHeading is the right column's title and what is open in it.
func tasksHeading() (title, sub string, ink sdl.Color) {
	ink = ColorTextTertiary()
	i := tasksEntryIndex(tasksUI.cur)
	if i < 0 {
		return "Tasks", "", ink
	}
	e := tasksUI.entries[i]
	word := "open"
	if e.kind == tasksEntToday {
		word = "due"
	}
	switch {
	case e.overdue > 0:
		sub, ink = fmt.Sprintf("%d %s · %d overdue", e.count, word, e.overdue), WarningInk()
	case e.count > 0:
		sub = fmt.Sprintf("%d %s", e.count, word)
	case tasksUI.doneN > 0:
		sub = "All done"
	default:
		sub = "Nothing open"
	}
	return e.name, sub, ink
}

// tasksEmptyText is what an empty list says.
func tasksEmptyText() (string, string) {
	if tasksUI.cur == tasksTodayID {
		return "Nothing due today", "Tasks with today's date or an earlier one gather here from every list."
	}
	return "No tasks yet", hintSentence("Press %s to add one, or type them on your phone.", "Y / N", "Add")
}

// tasksDrawRows draws the right column: its heading, then the rows with the
// cursor kept in view, the scrollbar, and what an empty list says.
func tasksDrawRows(renderer *sdl.Renderer, config *Config, font, small, head *ttf.Font, area sdl.Rect, accent sdl.Color) {
	tasksUI.rowHits = tasksUI.rowHits[:0]
	title, sub, subInk := tasksHeading()
	_, hh := measureText(head, title)
	sw, sh := measureText(small, sub)
	if sub != "" {
		drawText(renderer, small, sub, area.X+area.W-sw-4, area.Y+(hh-sh)/2, subInk, textAlignLeft)
	}
	drawText(renderer, head, ellipsize(head, title, area.W-sw-28), area.X+4, area.Y, ColorTextPrimary(), textAlignLeft)
	listTop := area.Y + hh + 10
	list := sdl.Rect{X: area.X, Y: listTop, W: area.W, H: area.Y + area.H - listTop}

	addH := int32(font.Height()) + 16
	itemH := max(int32(font.Height())+int32(small.Height())+20, 56)
	doneH := int32(small.Height()) + 20
	gap := int32(6)
	height := func(kind int) int32 {
		switch kind {
		case tasksRowAdd:
			return addH
		case tasksRowDone:
			return doneH
		}
		return itemH
	}
	total, curTop, curBottom := int32(0), int32(-1), int32(-1)
	for i, row := range tasksUI.rows {
		h := height(row.kind)
		if i == tasksUI.rowCursor {
			curTop, curBottom = total, total+h
		}
		total += h + gap
	}
	maxScroll := max(total-gap-list.H, 0)
	scroll := tasksUI.scroll
	if tasksUI.ensureVisible && curTop >= 0 {
		if curTop < scroll {
			scroll = curTop
		}
		if curBottom > scroll+list.H {
			scroll = curBottom - list.H
		}
		tasksUI.ensureVisible = false
	}
	scroll = min(max(scroll, 0), maxScroll)
	tasksUI.scroll = scroll
	rowW := list.W - 10
	renderWithClip(renderer, list.X, list.Y, list.W, list.H, func(r *sdl.Renderer) {
		y := list.Y - scroll
		for i, row := range tasksUI.rows {
			h := height(row.kind)
			if y+h >= list.Y && y <= list.Y+list.H {
				rect := sdl.Rect{X: list.X, Y: y, W: rowW, H: h}
				selected := tasksUI.pane == tasksPaneItems && i == tasksUI.rowCursor
				hovered := !selected && sysToolPointIn(rect)
				var check sdl.Rect
				switch row.kind {
				case tasksRowAdd:
					tasksDrawAddRow(r, font, rect, selected, hovered)
				case tasksRowDone:
					tasksDrawDoneRow(r, small, rect, selected, hovered)
				default:
					check = tasksDrawItemRow(r, font, small, rect, row, selected, hovered, accent)
				}
				if vis := tasksClipRect(rect, list); vis.H > 0 {
					tasksUI.rowHits = append(tasksUI.rowHits, tasksHit{rect: vis, idx: i, check: check})
				}
			}
			y += h + gap
		}
	})
	if maxScroll > 0 && total > 0 {
		drawScrollbar(renderer, list.X+list.W-4, list.Y, 4, list.H,
			float64(list.H)/float64(total), float64(scroll)/float64(maxScroll))
	}
	if len(tasksUI.rows) == 1 {
		emptyTop := list.Y + addH + 24
		if bottom := list.Y + list.H; bottom-emptyTop > 90 {
			t, b := tasksEmptyText()
			UIEmptyState(renderer, config, sdl.Rect{X: list.X, Y: emptyTop, W: rowW, H: bottom - emptyTop}, "@checklist", t, b)
		}
	}
}

// tasksDrawAddRow draws "Add a task".
func tasksDrawAddRow(r *sdl.Renderer, font *ttf.Font, rect sdl.Rect, selected, hovered bool) {
	UIListRow(r, rect, glassRowRadius, selected, hovered)
	gs := int32(font.Height()) - 6
	ink := ColorTextAccent()
	iconDraw(r, "plus", sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-gs)/2, W: gs, H: gs}, ink)
	label := "Add a task"
	_, th := measureText(font, label)
	drawText(r, font, label, rect.X+14+gs+14, rect.Y+(rect.H-th)/2, ink, textAlignLeft)
}

// tasksDrawDoneRow draws the row that shows or hides the finished tasks.
func tasksDrawDoneRow(r *sdl.Renderer, small *ttf.Font, rect sdl.Rect, selected, hovered bool) {
	if selected || hovered {
		UIListRow(r, rect, glassRowRadius, selected, hovered)
	}
	label := "Done · " + strconv.Itoa(tasksUI.doneN)
	action := "Show"
	if tasksUI.showDone {
		action = "Hide"
	}
	_, lh := measureText(small, label)
	drawText(r, small, label, rect.X+16, rect.Y+(rect.H-lh)/2, ColorTextTertiary(), textAlignLeft)
	aw, ah := measureText(small, action)
	drawText(r, small, action, rect.X+rect.W-16-aw, rect.Y+(rect.H-ah)/2, ColorTextAccent(), textAlignLeft)
}

// tasksRowSub is a task's second line: its due ("Overdue · Mon 17 Nov"), its
// repeat, and in Today its list.
func tasksRowSub(row tasksRow) string {
	var parts []string
	if row.label != "" {
		if row.overdue {
			parts = append(parts, "Overdue · "+row.label)
		} else {
			parts = append(parts, row.label)
		}
	}
	if row.item.Repeat != tasksRepeatNone {
		parts = append(parts, tasksRepeatLabel(row.item.Repeat))
	}
	if row.list != "" {
		parts = append(parts, row.list)
	}
	return strings.Join(parts, " · ")
}

// tasksDrawItemRow draws one task: its checkbox, its text (struck through
// when done) and its second line with a bell for a reminder and a clock for
// an alarm. It returns the checkbox's tap area.
func tasksDrawItemRow(r *sdl.Renderer, font, small *ttf.Font, rect sdl.Rect, row tasksRow, selected, hovered bool, accent sdl.Color) sdl.Rect {
	UIListRow(r, rect, glassRowRadius, selected, hovered)
	it := row.item
	size := min(int32(26), rect.H-20)
	box := sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-size)/2, W: size, H: size}
	if it.Done {
		fillRoundedRect(r, box.X, box.Y, box.W, box.H, 6, accent)
		iconDraw(r, "check", sdl.Rect{X: box.X + 4, Y: box.Y + 4, W: box.W - 8, H: box.H - 8}, ColorIconDark)
	} else {
		edge := ColorTextSecondary()
		if row.overdue {
			edge = WarningInk()
		}
		strokeRoundedRect(r, box.X, box.Y, box.W, box.H, 6, 2, edge)
	}
	textX := box.X + box.W + 14
	textW := rect.X + rect.W - 16 - textX
	ink, subInk := ColorTextPrimary(), ColorTextSecondary()
	switch {
	case it.Done:
		ink, subInk = ColorTextTertiary(), ColorTextTertiary()
	case row.overdue:
		subInk = WarningInk()
	}
	sub := tasksRowSub(row)
	bell := it.Remind && row.due.set() && !it.Done
	ring := it.Alarm && !it.Done
	twoLines := sub != "" || bell || ring
	title := ellipsize(font, it.Text, textW)
	tw, th := measureText(font, title)
	sh := int32(small.Height())
	ty := rect.Y + (rect.H-th)/2
	if twoLines {
		ty = rect.Y + (rect.H-th-sh-2)/2
	}
	drawText(r, font, title, textX, ty, ink, textAlignLeft)
	if it.Done {
		_ = r.SetDrawColor(ink.R, ink.G, ink.B, ink.A)
		_ = r.FillRect(&sdl.Rect{X: textX, Y: ty + th/2, W: tw, H: 2})
	}
	if twoLines {
		sy := ty + th + 2
		x := textX
		icons := int32(0)
		if bell {
			icons += sh + 6
		}
		if ring {
			icons += sh + 6
		}
		if sub != "" {
			s := ellipsize(small, sub, textW-icons)
			w, _ := measureText(small, s)
			drawText(r, small, s, x, sy, subInk, textAlignLeft)
			x += w + 8
		}
		if bell {
			iconDraw(r, "bell", sdl.Rect{X: x, Y: sy, W: sh, H: sh}, subInk)
			x += sh + 6
		}
		if ring {
			iconDraw(r, "alarm", sdl.Rect{X: x, Y: sy, W: sh, H: sh}, subInk)
		}
	}
	return sdl.Rect{X: rect.X, Y: rect.Y, W: textX - rect.X - 4, H: rect.H}
}

// ── Scene ─────────────────────────────────────────────────────────────────

// tasksScene is the registered module for the Tasks scene.
type tasksScene struct{ sceneBase }

func (tasksScene) render(r *sdl.Renderer, c *Config) bool { tasksRender(r, c); return true }

func (tasksScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return tasksHandleKey(e, c) }

func (tasksScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tasksHandlePad(e, c)
}

func init() { registerScene(tasksScene{sceneBase{name: tasksSceneName}}) }
