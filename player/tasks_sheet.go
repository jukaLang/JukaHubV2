package main

// tasks_sheet.go — the Tasks app's sheets: one task's settings, the picker for
// its due date and time, and one list's settings.
//
// The task sheet (X on a task) is a column of rows the way Settings reads:
// Text (A opens the keyboard), Due (A opens the picker), Remind me, Ring as
// alarm, Repeat and List (A or Left/Right change them), and Delete task,
// which wants a second A. A row that cannot do anything yet says why instead:
// a reminder needs a date, an alarm a time.
//
// The due picker keeps a draft until it is saved: quick picks (Today,
// Tomorrow, Next week, No date — Left/Right choose one, A uses it), the day
// (Left/Right a day, L1/R1 a week), the time (Left/Right five minutes, L1/R1
// an hour, A switches the time on or off) and Save; Y saves from any row and
// B goes back to the task sheet without changing anything.
//
// The list sheet (X on a list) renames the list, clears its finished tasks,
// or deletes it with its tasks (a second A; the last list stays). Every row
// answers a tap, and a tap outside the card closes it.

import (
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The sheets.
const (
	tasksSheetNone = iota
	tasksSheetItem
	tasksSheetDue
	tasksSheetList
)

// The task sheet's rows.
const (
	tasksItemText = iota
	tasksItemDue
	tasksItemRemind
	tasksItemAlarm
	tasksItemRepeat
	tasksItemList
	tasksItemDelete
	tasksItemRows
)

// The due picker's rows.
const (
	tasksDueQuick = iota
	tasksDueDay
	tasksDueTime
	tasksDueSaveRow
	tasksDueRows
)

// The list sheet's rows.
const (
	tasksListRename = iota
	tasksListClear
	tasksListDelete
	tasksListRows
)

// tasksQuickPicks are the due picker's shortcuts, in order.
var tasksQuickPicks = [...]string{"Today", "Tomorrow", "Next week", "No date"}

// tasksRepeats is the order Repeat steps through.
var tasksRepeats = [...]string{tasksRepeatNone, tasksRepeatDaily, tasksRepeatWeekly}

// tasksSheetHit is a tappable part of a sheet: a row, one of its step arrows
// (dir -1 or 1), or a quick pick (quick 0..3; -1 otherwise).
type tasksSheetHit struct {
	rect  sdl.Rect
	row   int
	dir   int
	quick int
}

// tasksSheet is the open sheet's state. Main thread only.
var tasksSheet struct {
	itemID int
	listID int
	cursor int
	quick  int
	draft  tasksDue
	delArm time.Time
	card   sdl.Rect
	hits   []tasksSheetHit
}

// ── Opening and closing ───────────────────────────────────────────────────

// tasksOpenItemSheet opens task id's sheet on its first row.
func tasksOpenItemSheet(id int) {
	tasksSheet.itemID, tasksSheet.cursor, tasksSheet.delArm = id, tasksItemText, time.Time{}
	tasksUI.sheet = tasksSheetItem
	PlayActivateSound()
}

// tasksOpenListSheet opens list id's sheet.
func tasksOpenListSheet(id int) {
	tasksSheet.listID, tasksSheet.cursor, tasksSheet.delArm = id, tasksListRename, time.Time{}
	tasksUI.sheet = tasksSheetList
	PlayActivateSound()
}

// tasksOpenDuePicker opens the picker on the task's due: on the day row when
// it has one, on the quick picks when it has none.
func tasksOpenDuePicker(it taskItem) {
	d, ok := tasksParseDue(it.Due)
	if !ok {
		d = tasksDue{}
	}
	tasksSheet.draft, tasksSheet.quick = d, 0
	tasksSheet.cursor = tasksDueQuick
	if ok {
		tasksSheet.cursor = tasksDueDay
	}
	tasksUI.sheet = tasksSheetDue
	PlayActivateSound()
}

// tasksSheetBack is B: the picker goes back to its task, a sheet closes.
func tasksSheetBack() {
	if tasksUI.sheet == tasksSheetDue {
		tasksUI.sheet = tasksSheetItem
		tasksSheet.cursor = tasksItemDue
	} else {
		tasksUI.sheet = tasksSheetNone
	}
	tasksSheet.delArm = time.Time{}
	PlayBackSound()
}

// tasksSheetTask is the task the sheet is about. It may have gone (deleted on
// the phone), which closes the sheet.
func tasksSheetTask() (taskItem, bool) {
	for _, it := range tasksUI.items {
		if it.ID == tasksSheet.itemID {
			return it, true
		}
	}
	return taskItem{}, false
}

// tasksSheetListNow is the list the list sheet is about.
func tasksSheetListNow() (taskList, bool) {
	for _, l := range tasksUI.lists {
		if l.ID == tasksSheet.listID {
			return l, true
		}
	}
	return taskList{}, false
}

// tasksSheetRows is the open sheet's row count.
func tasksSheetRows() int {
	switch tasksUI.sheet {
	case tasksSheetItem:
		return tasksItemRows
	case tasksSheetDue:
		return tasksDueRows
	case tasksSheetList:
		return tasksListRows
	}
	return 0
}

// tasksSheetFail says why a change did not happen.
func tasksSheetFail(err error) {
	ShowToast(err.Error(), ToastKindInfo)
	PlayErrorSound()
}

// ── Actions ───────────────────────────────────────────────────────────────

// tasksSheetMove is Up/Down.
func tasksSheetMove(delta int) {
	next := tasksClamp(tasksSheet.cursor+delta, 0, tasksSheetRows()-1)
	if next == tasksSheet.cursor {
		return
	}
	tasksSheet.cursor = next
	tasksSheet.delArm = time.Time{}
	PlayNavSound()
}

// tasksSheetStep is Left/Right (big: L1/R1).
func tasksSheetStep(dir int, big bool) {
	switch tasksUI.sheet {
	case tasksSheetItem:
		switch tasksSheet.cursor {
		case tasksItemRemind, tasksItemAlarm, tasksItemRepeat, tasksItemList:
			tasksItemChange(dir)
		}
	case tasksSheetDue:
		tasksDueStep(dir, big)
	}
}

// tasksSheetActivate is A.
func tasksSheetActivate() {
	switch tasksUI.sheet {
	case tasksSheetItem:
		tasksItemChange(0)
	case tasksSheetDue:
		tasksDueActivate()
	case tasksSheetList:
		tasksListActivate()
	}
}

// tasksItemChange is A (dir 0) or Left/Right on the task sheet's row.
func tasksItemChange(dir int) {
	it, ok := tasksSheetTask()
	if !ok {
		tasksUI.sheet = tasksSheetNone
		return
	}
	var err error
	switch tasksSheet.cursor {
	case tasksItemText:
		if dir != 0 {
			return
		}
		id := it.ID
		tasksPrompt(func() {
			osPromptOpen("Edit task", "Task", "Save", it.Text, tasksTextMax, func(text string) {
				if text == "" {
					return
				}
				if err := tasksSetText(id, text); err != nil {
					tasksSheetFail(err)
					return
				}
				tasksAlarmApply(time.Now())
			})
		})
		return
	case tasksItemDue:
		if dir == 0 {
			tasksOpenDuePicker(it)
		}
		return
	case tasksItemRemind:
		err = tasksSetRemind(it.ID, !it.Remind)
	case tasksItemAlarm:
		err = tasksSetAlarm(it.ID, !it.Alarm)
	case tasksItemRepeat:
		at := 0
		for i, r := range tasksRepeats {
			if r == it.Repeat {
				at = i
			}
		}
		step := max(dir, 1)
		if dir < 0 {
			step = -1
		}
		n := len(tasksRepeats)
		err = tasksSetRepeat(it.ID, tasksRepeats[((at+step)%n+n)%n])
	case tasksItemList:
		n := len(tasksUI.lists)
		if n < 2 {
			return
		}
		at := 0
		for i, l := range tasksUI.lists {
			if l.ID == it.List {
				at = i
			}
		}
		step := max(dir, 1)
		if dir < 0 {
			step = -1
		}
		err = tasksMoveItem(it.ID, tasksUI.lists[((at+step)%n+n)%n].ID)
	case tasksItemDelete:
		if dir != 0 {
			return
		}
		if tasksSheet.delArm.IsZero() || time.Since(tasksSheet.delArm) > tasksDeleteWindow {
			tasksSheet.delArm = time.Now()
			PlayNavSound()
			return
		}
		tasksSheet.delArm = time.Time{}
		tasksUI.sheet = tasksSheetNone
		tasksDeleteTask(it.ID)
		return
	}
	if err != nil {
		tasksSheetFail(err)
		return
	}
	tasksAlarmApply(time.Now())
	PlayToggleSound()
}

// tasksDueStep is Left/Right (big: L1/R1) in the picker. A day or time
// changed on a task without a date starts from today.
func tasksDueStep(dir int, big bool) {
	now := time.Now()
	d := &tasksSheet.draft
	switch tasksSheet.cursor {
	case tasksDueQuick:
		if big {
			return
		}
		n := len(tasksQuickPicks)
		tasksSheet.quick = ((tasksSheet.quick+dir)%n + n) % n
	case tasksDueDay:
		if !d.set() {
			*d = tasksDueOn(now)
			break
		}
		step := 1
		if big {
			step = 7
		}
		*d = d.addDays(dir * step)
	case tasksDueTime:
		if !d.set() {
			*d = tasksDueOn(now)
		}
		if !d.timed {
			*d = d.withTime(true, now)
			break
		}
		step := 5
		if big {
			step = 60
		}
		*d = d.addMinutes(dir * step)
	default:
		return
	}
	PlayNavSound()
}

// tasksDueActivate is A in the picker.
func tasksDueActivate() {
	now := time.Now()
	d := &tasksSheet.draft
	switch tasksSheet.cursor {
	case tasksDueQuick:
		var day time.Time
		switch tasksSheet.quick {
		case 0:
			day = now
		case 1:
			day = now.AddDate(0, 0, 1)
		case 2:
			day = now.AddDate(0, 0, 7)
		default:
			*d = tasksDue{}
			PlayToggleSound()
			return
		}
		next := tasksDueOn(day)
		if d.timed {
			// A quick pick changes the day and keeps a time already chosen.
			next.timed, next.min = true, d.min
		}
		*d = next
		PlayToggleSound()
	case tasksDueDay:
		tasksSheetMove(1)
	case tasksDueTime:
		if !d.set() {
			*d = tasksDueOn(now)
		}
		*d = d.withTime(!d.timed, now)
		PlayToggleSound()
	case tasksDueSaveRow:
		tasksDueSave()
	}
}

// tasksDueSave stores the draft and goes back to the task sheet.
func tasksDueSave() {
	if err := tasksSetDue(tasksSheet.itemID, tasksSheet.draft.String()); err != nil {
		tasksUI.sheet = tasksSheetNone
		tasksSheetFail(err)
		return
	}
	tasksAlarmApply(time.Now())
	tasksUI.sheet = tasksSheetItem
	tasksSheet.cursor = tasksItemDue
	PlaySuccessSound()
}

// tasksListActivate is A on the list sheet.
func tasksListActivate() {
	l, ok := tasksSheetListNow()
	if !ok {
		tasksUI.sheet = tasksSheetNone
		return
	}
	switch tasksSheet.cursor {
	case tasksListRename:
		id := l.ID
		tasksPrompt(func() {
			osPromptOpen("Rename list", "List name", "Save", l.Name, tasksListNameMax, func(name string) {
				if name == "" {
					return
				}
				if err := tasksRenameList(id, name); err != nil {
					tasksSheetFail(err)
				}
			})
		})
	case tasksListClear:
		n := tasksClearDone(l.ID)
		tasksAlarmApply(time.Now())
		switch n {
		case 0:
			ShowToast("No done tasks to clear", ToastKindInfo)
		case 1:
			ShowToast("Cleared 1 done task", ToastKindSuccess)
		default:
			ShowToast("Cleared "+strconv.Itoa(n)+" done tasks", ToastKindSuccess)
		}
		PlayToggleSound()
	case tasksListDelete:
		if len(tasksUI.lists) <= 1 {
			tasksSheetFail(errTasksLastList)
			return
		}
		if tasksSheet.delArm.IsZero() || time.Since(tasksSheet.delArm) > tasksDeleteWindow {
			tasksSheet.delArm = time.Now()
			PlayNavSound()
			return
		}
		tasksSheet.delArm = time.Time{}
		n, err := tasksDeleteList(l.ID)
		if err != nil {
			tasksSheetFail(err)
			return
		}
		tasksAlarmApply(time.Now())
		tasksUI.sheet = tasksSheetNone
		tasksUI.pane = tasksPaneLists
		msg := "Deleted " + l.Name
		if n > 0 {
			msg += " and its " + tasksPlural(n, "task", "tasks")
		}
		ShowToast(msg, ToastKindInfo)
		PlayBackSound()
	}
}

// tasksPlural is "1 task" or "3 tasks".
func tasksPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ── Input ─────────────────────────────────────────────────────────────────

// tasksSheetPad is the pad while a sheet is open. The sheet is modal: it
// takes every button.
func tasksSheetPad(button uint8) bool {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		tasksSheetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		tasksSheetMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		tasksSheetStep(-1, false)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		tasksSheetStep(1, false)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		tasksSheetStep(-1, true)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		tasksSheetStep(1, true)
	case sdl.CONTROLLER_BUTTON_A:
		tasksSheetActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		if tasksUI.sheet == tasksSheetDue {
			tasksDueSave()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		tasksSheetBack()
	}
	return true
}

// tasksSheetKey is the keyboard while a sheet is open.
func tasksSheetKey(e *sdl.KeyboardEvent) bool {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		tasksSheetMove(-1)
	case sdl.K_DOWN, sdl.K_TAB:
		tasksSheetMove(1)
	case sdl.K_LEFT:
		tasksSheetStep(-1, false)
	case sdl.K_RIGHT:
		tasksSheetStep(1, false)
	case sdl.K_PAGEUP:
		tasksSheetStep(-1, true)
	case sdl.K_PAGEDOWN:
		tasksSheetStep(1, true)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			tasksSheetActivate()
		}
	case sdl.K_y:
		if e.Repeat == 0 && tasksUI.sheet == tasksSheetDue {
			tasksDueSave()
		}
	case sdl.K_DELETE:
		if e.Repeat == 0 && tasksUI.sheet == tasksSheetItem {
			if tasksSheet.cursor != tasksItemDelete {
				tasksSheet.cursor, tasksSheet.delArm = tasksItemDelete, time.Time{}
			}
			tasksItemChange(0)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		tasksSheetBack()
	}
	return true
}

// tasksSheetClick answers a tap while a sheet is open: an arrow steps, a
// quick pick is used, a row acts, and a tap outside the card goes back.
func tasksSheetClick(mx, my int32) bool {
	if !pointInRect(mx, my, tasksSheet.card) {
		tasksSheetBack()
		return true
	}
	// Arrows and chips are recorded after their row, so they are looked at
	// first.
	for i := len(tasksSheet.hits) - 1; i >= 0; i-- {
		h := tasksSheet.hits[i]
		if !pointInRect(mx, my, h.rect) {
			continue
		}
		if h.row != tasksSheet.cursor {
			tasksSheet.cursor, tasksSheet.delArm = h.row, time.Time{}
		}
		switch {
		case h.quick >= 0:
			tasksSheet.quick = h.quick
			tasksSheetActivate()
		case h.dir != 0:
			tasksSheetStep(h.dir, false)
		default:
			tasksSheetActivate()
		}
		return true
	}
	return true
}

// tasksSheetFooterHints is the footer row while a sheet is open.
func tasksSheetFooterHints() []FooterHint {
	choose := FooterHint{Button: "D-Pad", Label: "Choose"}
	switch tasksUI.sheet {
	case tasksSheetDue:
		hints := []FooterHint{choose}
		switch tasksSheet.cursor {
		case tasksDueQuick:
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "Pick"}, FooterHint{Button: "A", Label: "Use"})
		case tasksDueDay:
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "Day"}, FooterHint{Button: "L1/R1 / PgUp/PgDn", Label: "Week"})
		case tasksDueTime:
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "5 min"}, FooterHint{Button: "L1/R1 / PgUp/PgDn", Label: "Hour"},
				FooterHint{Button: "A", Label: "Time on/off"})
		}
		return append(hints, FooterHint{Button: "Y", Label: "Save"}, FooterHint{Button: "B", Label: "Cancel"})
	case tasksSheetItem:
		hints := []FooterHint{choose}
		switch tasksSheet.cursor {
		case tasksItemText:
			hints = append(hints, FooterHint{Button: "A", Label: "Edit"})
		case tasksItemDue:
			hints = append(hints, FooterHint{Button: "A", Label: "Change"})
		case tasksItemRemind, tasksItemAlarm:
			hints = append(hints, FooterHint{Button: "A", Label: "Switch"})
		case tasksItemRepeat, tasksItemList:
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "Change"})
		case tasksItemDelete:
			hints = append(hints, FooterHint{Button: "A", Label: "Delete"})
		}
		return append(hints, FooterHint{Button: "B", Label: "Close"})
	}
	return []FooterHint{choose, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// tasksDrawSheet draws the open sheet over a dimmed screen.
func tasksDrawSheet(renderer *sdl.Renderer, config *Config, font, small, head *ttf.Font) {
	tasksSheet.hits = tasksSheet.hits[:0]
	switch tasksUI.sheet {
	case tasksSheetItem:
		it, ok := tasksSheetTask()
		if !ok {
			tasksUI.sheet = tasksSheetNone
			return
		}
		tasksDrawItemSheet(renderer, config, font, small, head, it)
	case tasksSheetDue:
		if _, ok := tasksSheetTask(); !ok {
			tasksUI.sheet = tasksSheetNone
			return
		}
		tasksDrawDueSheet(renderer, config, font, small, head)
	case tasksSheetList:
		l, ok := tasksSheetListNow()
		if !ok {
			tasksUI.sheet = tasksSheetNone
			return
		}
		tasksDrawListSheet(renderer, font, small, head, l)
	}
}

// tasksSheetCard places a sheet's card for rows rows under a heading headH
// tall, and returns it with the row height that fits: rows shrink on a small
// screen rather than run off the card.
func tasksSheetCard(rows int, rowH, headH int32) (sdl.Rect, int32) {
	margin := scaleX(homeSideMargin, screenWidth)
	w := min(screenWidth-2*margin, 760)
	top := headerBarHeight() + 8
	room := screenHeight - top - footerBarHeight() - 8
	if fit := (room-headH-2*OverlayPadX)/int32(rows) - 6; fit < rowH {
		rowH = max(fit, 28)
	}
	h := min(headH+int32(rows)*(rowH+6)+2*OverlayPadX, room)
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top + (room-h)/2, W: w, H: h}
	tasksSheet.card = card
	return card, rowH
}

// tasksSheetHeading draws a sheet's title and the line under it, and returns
// where the rows start.
func tasksSheetHeading(renderer *sdl.Renderer, head, small *ttf.Font, card sdl.Rect, title, sub string) int32 {
	x := card.X + OverlayPadX
	w := card.W - 2*OverlayPadX
	y := card.Y + OverlayPadX
	drawText(renderer, head, ellipsize(head, title, w), x, y, ColorTextPrimary(), textAlignLeft)
	y += int32(head.Height())
	if sub != "" {
		drawText(renderer, small, ellipsize(small, sub, w), x, y+2, ColorTextTertiary(), textAlignLeft)
		y += int32(small.Height()) + 4
	}
	return y + 10
}

// tasksSheetRowSpec is what one sheet row shows.
type tasksSheetRowSpec struct {
	label    string
	value    string
	valueInk sdl.Color
	labelInk sdl.Color
	enabled  bool
	steps    bool
}

// tasksDrawSheetRow draws one row: its label on the left, its value on the
// right between step arrows when it steps, dimmed when it can do nothing now.
func tasksDrawSheetRow(renderer *sdl.Renderer, font, small *ttf.Font, rect sdl.Rect, row int, spec tasksSheetRowSpec) {
	selected := row == tasksSheet.cursor
	UIListRow(renderer, rect, glassRowRadius, selected, !selected && sysToolPointIn(rect))
	tasksSheet.hits = append(tasksSheet.hits, tasksSheetHit{rect: rect, row: row, quick: -1})
	labelInk := spec.labelInk
	if labelInk.A == 0 {
		labelInk = ColorTextSecondary()
	}
	if !spec.enabled {
		labelInk = ColorTextTertiary()
	}
	_, lh := measureText(small, spec.label)
	drawText(renderer, small, spec.label, rect.X+16, rect.Y+(rect.H-lh)/2, labelInk, textAlignLeft)
	if spec.value == "" {
		return
	}
	valueInk := spec.valueInk
	if valueInk.A == 0 {
		valueInk = ColorTextPrimary()
	}
	if !spec.enabled {
		valueInk = ColorTextTertiary()
	}
	right := rect.X + rect.W - 16
	as := min(rect.H-12, int32(28))
	steps := spec.steps && spec.enabled
	if steps {
		inc := sdl.Rect{X: right - as, Y: rect.Y + (rect.H-as)/2, W: as, H: as}
		drawChevronRight(renderer, inc.X+as/2, inc.Y+as/2, as/4, ColorTextSecondary())
		tasksSheet.hits = append(tasksSheet.hits, tasksSheetHit{rect: inc, row: row, dir: 1, quick: -1})
		right = inc.X - 6
	}
	value := ellipsize(font, spec.value, rect.W/2)
	vw, vh := measureText(font, value)
	drawText(renderer, font, value, right-vw, rect.Y+(rect.H-vh)/2, valueInk, textAlignLeft)
	if steps {
		dec := sdl.Rect{X: right - vw - 6 - as, Y: rect.Y + (rect.H-as)/2, W: as, H: as}
		drawChevronLeft(renderer, dec.X+as/2, dec.Y+as/2, as/4, ColorTextSecondary())
		tasksSheet.hits = append(tasksSheet.hits, tasksSheetHit{rect: dec, row: row, dir: -1, quick: -1})
	}
}

// tasksOnOff is a switch's value.
func tasksOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// tasksDrawItemSheet draws one task's sheet.
func tasksDrawItemSheet(renderer *sdl.Renderer, config *Config, font, small, head *ttf.Font, it taskItem) {
	drawOverlayScrim(renderer)
	headH := int32(head.Height()) + int32(small.Height()) + 14
	card, rowH := tasksSheetCard(tasksItemRows, int32(font.Height())+18, headH)
	drawOverlayCard(renderer, card)
	sub := tasksListName(it.List)
	if it.Done {
		sub += " · Done"
	}
	y := tasksSheetHeading(renderer, head, small, card, it.Text, sub)
	now := time.Now()
	d, hasDue := tasksParseDue(it.Due)
	for row := 0; row < tasksItemRows; row++ {
		spec := tasksSheetRowSpec{enabled: true}
		switch row {
		case tasksItemText:
			spec.label, spec.value = "Text", it.Text
		case tasksItemDue:
			spec.label, spec.value = "Due", "No date"
			if hasDue {
				spec.value = tasksDueLabel(config, d, now)
				if !it.Done && d.overdue(now) {
					spec.valueInk = WarningInk()
				}
			}
		case tasksItemRemind:
			spec.label, spec.enabled = "Remind me", hasDue
			switch {
			case !hasDue:
				spec.value = "Needs a date"
			case it.Remind && !d.timed:
				spec.value = "On, at " + clockDigitsHM(config, calRemindAllDayHour, 0)
			default:
				spec.value = tasksOnOff(it.Remind)
			}
		case tasksItemAlarm:
			spec.label, spec.enabled = "Ring as alarm", hasDue && d.timed
			spec.value = tasksOnOff(it.Alarm)
			if !spec.enabled {
				spec.value = "Needs a time"
			}
		case tasksItemRepeat:
			spec.label, spec.value, spec.steps = "Repeat", tasksRepeatLabel(it.Repeat), true
		case tasksItemList:
			spec.label, spec.value = "List", tasksListName(it.List)
			spec.steps = len(tasksUI.lists) > 1
		case tasksItemDelete:
			spec.label, spec.labelInk = "Delete task", DangerInk()
			if !tasksSheet.delArm.IsZero() && time.Since(tasksSheet.delArm) <= tasksDeleteWindow {
				spec.value, spec.valueInk = hintSentence("Press %s again", "A", "Delete"), DangerInk()
			}
		}
		tasksDrawSheetRow(renderer, font, small, sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: rowH}, row, spec)
		y += rowH + 6
	}
}

// tasksDrawDueSheet draws the due picker over the task's sheet.
func tasksDrawDueSheet(renderer *sdl.Renderer, config *Config, font, small, head *ttf.Font) {
	drawOverlayScrim(renderer)
	headH := int32(head.Height()) + int32(small.Height()) + 14
	card, rowH := tasksSheetCard(tasksDueRows, int32(font.Height())+18, headH)
	drawOverlayCard(renderer, card)
	now := time.Now()
	d := tasksSheet.draft
	summary := "No date"
	if d.set() {
		summary = tasksDueLabel(config, d, now)
	}
	y := tasksSheetHeading(renderer, head, small, card, "Due date", summary)
	x, w := card.X+OverlayPadX, card.W-2*OverlayPadX
	accent := glassAccent()
	for row := 0; row < tasksDueRows; row++ {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		switch row {
		case tasksDueQuick:
			tasksDrawQuickRow(renderer, small, rect, accent)
		case tasksDueDay:
			spec := tasksSheetRowSpec{label: "Day", value: "No date", enabled: true, steps: true}
			if d.set() {
				spec.value = formatDateBrief(config, d.day)
				if d.day.Year() != now.Year() {
					spec.value = formatDate(config, d.day)
				}
			}
			tasksDrawSheetRow(renderer, font, small, rect, row, spec)
		case tasksDueTime:
			spec := tasksSheetRowSpec{label: "Time", value: "No time", enabled: true, steps: true}
			if d.timed {
				spec.value = clockDigitsHM(config, d.min/60, d.min%60)
			} else {
				spec.valueInk = ColorTextTertiary()
			}
			tasksDrawSheetRow(renderer, font, small, rect, row, spec)
		case tasksDueSaveRow:
			selected := tasksSheet.cursor == row
			UIListRow(renderer, rect, glassRowRadius, selected, !selected && sysToolPointIn(rect))
			tasksSheet.hits = append(tasksSheet.hits, tasksSheetHit{rect: rect, row: row, quick: -1})
			label := "Save"
			lw, lh := measureText(font, label)
			drawText(renderer, font, label, rect.X+(rect.W-lw)/2, rect.Y+(rect.H-lh)/2, ColorTextAccent(), textAlignLeft)
		}
		y += rowH + 6
	}
}

// tasksDrawQuickRow draws the quick picks as a row of chips; the focused one
// is filled while the row has the cursor.
func tasksDrawQuickRow(renderer *sdl.Renderer, small *ttf.Font, rect sdl.Rect, accent sdl.Color) {
	selected := tasksSheet.cursor == tasksDueQuick
	UIListRow(renderer, rect, glassRowRadius, selected, false)
	tasksSheet.hits = append(tasksSheet.hits, tasksSheetHit{rect: rect, row: tasksDueQuick, quick: -1})
	gap := int32(8)
	n := int32(len(tasksQuickPicks))
	chipW := (rect.W - 24 - gap*(n-1)) / n
	chipH := rect.H - 14
	x := rect.X + 12
	for i, label := range tasksQuickPicks {
		chip := sdl.Rect{X: x, Y: rect.Y + 7, W: chipW, H: chipH}
		fill, ink := glassA(14), ColorTextSecondary()
		if selected && i == tasksSheet.quick {
			fill, ink = WithAlpha(accent, 90), ColorTextPrimary()
		}
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chipH/2, fill)
		text := ellipsize(small, label, chipW-12)
		tw, th := measureText(small, text)
		drawText(renderer, small, text, chip.X+(chip.W-tw)/2, chip.Y+(chip.H-th)/2, ink, textAlignLeft)
		tasksSheet.hits = append(tasksSheet.hits, tasksSheetHit{rect: chip, row: tasksDueQuick, quick: i})
		x += chipW + gap
	}
}

// tasksDrawListSheet draws one list's sheet.
func tasksDrawListSheet(renderer *sdl.Renderer, font, small, head *ttf.Font, l taskList) {
	drawOverlayScrim(renderer)
	headH := int32(head.Height()) + int32(small.Height()) + 14
	card, rowH := tasksSheetCard(tasksListRows, int32(font.Height())+18, headH)
	drawOverlayCard(renderer, card)
	open, done := 0, 0
	for _, it := range tasksUI.items {
		switch {
		case it.List != l.ID:
		case it.Done:
			done++
		default:
			open++
		}
	}
	parts := []string{strconv.Itoa(open) + " open"}
	if done > 0 {
		parts = append(parts, strconv.Itoa(done)+" done")
	}
	y := tasksSheetHeading(renderer, head, small, card, l.Name, strings.Join(parts, " · "))
	for row := 0; row < tasksListRows; row++ {
		spec := tasksSheetRowSpec{enabled: true}
		switch row {
		case tasksListRename:
			spec.label, spec.value = "Rename list", l.Name
		case tasksListClear:
			spec.label, spec.enabled = "Clear done tasks", done > 0
			spec.value = strconv.Itoa(done)
		case tasksListDelete:
			spec.label, spec.labelInk = "Delete list", DangerInk()
			spec.enabled = len(tasksUI.lists) > 1
			switch {
			case !spec.enabled:
				spec.value = "The only list"
			case !tasksSheet.delArm.IsZero() && time.Since(tasksSheet.delArm) <= tasksDeleteWindow:
				spec.value, spec.valueInk = hintSentence("Press %s again", "A", "Delete"), DangerInk()
			case open+done > 0:
				spec.value = "With its " + tasksPlural(open+done, "task", "tasks")
			}
		}
		tasksDrawSheetRow(renderer, font, small, sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: rowH}, row, spec)
		y += rowH + 6
	}
}
