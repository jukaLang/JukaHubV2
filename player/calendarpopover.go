package main

// calendarpopover.go — the top-bar clock's month view, with the events of the
// synced calendars (calsync.go).
//
// The popover used to reuse the Alarm screen's corner widget: 28x22 cells drawn
// with the 24px body font. A 24px numeral in a 22px row is taller than its cell,
// so the numbers sat on top of each other, and two-digit days nearly touched in
// a 28px column. It now has its own layout: roomier cells, a month header with
// back and forward arrows, and a fixed six-row grid so the arrows and the grid
// do not jump when a month needs five rows.
//
// With calendar sync it became a small calendar app. A day with events wears
// up to three dots under its number in the calendars' colours; a ring marks
// the selected day, which the D-pad moves (Left/Right a day, Up/Down a week,
// across months), while the shoulders still turn whole months and A (or the
// month title) comes back to today. Under the grid an agenda strip lists the
// selected day's events — time, title, place — and one of them is picked:
// Select steps through them, Y sets a one-time alarm for it (reminder minutes
// before it starts), X syncs now. The mouse and touch do the same: a tapped
// day is selected, a tapped event is picked and a second tap sets its alarm,
// and "Sync" in the agenda's header syncs. The card grew by the agenda and a
// second hint line, and still fits under the clock on 1280x720 and 1024x768.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// calPopCellW and calPopCellH are sized for the body font, not the other
	// way round: at 46x38 a two-digit day has room to breathe inside its cell.
	calPopCellW    = 46
	calPopCellH    = 38
	calPopPadX     = 14
	calPopPadY     = 12
	calPopHeaderH  = 44
	calPopWeekdayH = 26
	// calPopRows is fixed at six: a month that needs five rows must not move
	// the arrows the user is about to press.
	calPopRows = 6
	// calPopNavSize is the arrow button's square.
	calPopNavSize = 30
	// calPopHintH reserves one line that explains the controls; there are
	// two, always drawn, so switching days cannot shift anything.
	calPopHintH = 18
	// calEvGap, calEvHeadH, calEvRowH and calEvRows are the agenda strip:
	// a gap, the day's header line, and three event rows that scroll.
	calEvGap   = 6
	calEvHeadH = 24
	calEvRowH  = 24
	calEvRows  = 3
	// calEvTimeW is the agenda's time column ("12:30 PM" fits).
	calEvTimeW = 72
	// calEvDayMax bounds the events kept for one day of the month view.
	calEvDayMax = 40
)

var (
	// calendarViewYear and calendarViewMonth are the month on display. Zero
	// means "not shown yet", which starts on the current month.
	calendarViewYear  int
	calendarViewMonth time.Month

	// The header's hit targets, rebuilt every frame like the panel hitboxes.
	calendarPrevRect  sdl.Rect
	calendarNextRect  sdl.Rect
	calendarTitleRect sdl.Rect

	// calEvGridRect is the day grid, calEvRowRects the agenda rows drawn
	// (index = event index in the day), calEvSyncRect the header's "Sync".
	calEvGridRect sdl.Rect
	calEvRowRects []calEvRowHit
	calEvSyncRect sdl.Rect
)

// calEvRowHit is one agenda row's hit target.
type calEvRowHit struct {
	rect sdl.Rect
	idx  int
}

// calEvSel is the selected day (0 = the default: today in today's month,
// else the 1st) and the picked event of its agenda.
var calEvSel struct {
	y      int
	m      time.Month
	d      int
	idx    int
	scroll int
}

// calEvMonth caches the shown month's events per day, rebuilt when the
// index changes (calSyncRev), the month changes, or every minute.
var calEvMonth struct {
	rev   uint64
	y     int
	m     time.Month
	built time.Time
	days  [32][]calEvent
}

// calendarCardSize is the popover card's fixed size.
func calendarCardSize() (int32, int32) {
	return int32(calPopCellW*7 + calPopPadX*2),
		int32(calPopHeaderH + calPopWeekdayH + calPopRows*calPopCellH +
			calEvGap + calEvHeadH + calEvRows*calEvRowH +
			2*calPopHintH + calPopPadY*2)
}

// currentCalendarMonth is the month on display, defaulting to the real one.
func currentCalendarMonth() (int, time.Month) {
	if calendarViewYear == 0 && calendarViewMonth == 0 {
		now := time.Now()
		return now.Year(), now.Month()
	}
	return calendarViewYear, calendarViewMonth
}

// resetCalendarView puts the view and the selection back on today, and asks
// for a sync when the last one is a while ago.
func resetCalendarView() {
	now := time.Now()
	calEvSelect(now.Year(), now.Month(), now.Day())
	calSyncKickIfStale(5 * time.Minute)
}

// calEvSelectedDay is the selected day of the shown month.
func calEvSelectedDay() int {
	y, m := currentCalendarMonth()
	if calEvSel.y == y && calEvSel.m == m && calEvSel.d > 0 {
		return min(calEvSel.d, icsDaysIn(y, m))
	}
	now := time.Now()
	if y == now.Year() && m == now.Month() {
		return now.Day()
	}
	return 1
}

// calEvSelect selects a date (any day number: time.Date rolls it into the
// right month) and shows its month.
func calEvSelect(y int, m time.Month, d int) {
	t := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	calendarViewYear, calendarViewMonth = t.Year(), t.Month()
	calEvSel.y, calEvSel.m, calEvSel.d = t.Year(), t.Month(), t.Day()
	calEvSel.idx, calEvSel.scroll = 0, 0
}

// calEvMoveDays moves the selection by days, across months.
func calEvMoveDays(delta int) {
	y, m := currentCalendarMonth()
	calEvSelect(y, m, calEvSelectedDay()+delta)
	PlayNavSound()
}

// shiftCalendarMonth steps the view by whole months, rolling the year with it.
// The selection keeps its day number (the 31st becomes the month's last day).
func shiftCalendarMonth(delta int) {
	year, month := currentCalendarMonth()
	d := calEvSelectedDay()
	t := time.Date(year, month, 1, 12, 0, 0, 0, time.Local).AddDate(0, delta, 0)
	calEvSelect(t.Year(), t.Month(), min(d, icsDaysIn(t.Year(), t.Month())))
	PlayNavSound()
}

// calendarMonthTitle is the header label.
func calendarMonthTitle() string {
	year, month := currentCalendarMonth()
	return formatMonthYear(time.Date(year, month, 1, 0, 0, 0, 0, time.Local))
}

// calendarShowsCurrentMonth reports whether the view is on today's month.
func calendarShowsCurrentMonth() bool {
	year, month := currentCalendarMonth()
	now := time.Now()
	return year == now.Year() && month == now.Month()
}

// calEvMonthDays is the shown month's events per day (index 1..31).
func calEvMonthDays() *[32][]calEvent {
	y, m := currentCalendarMonth()
	now := time.Now()
	rev := calSyncRev()
	c := &calEvMonth
	if c.rev == rev && c.y == y && c.m == m && !c.built.IsZero() && now.Sub(c.built) < time.Minute && now.Sub(c.built) >= 0 {
		return &c.days
	}
	c.rev, c.y, c.m, c.built = rev, y, m, now
	c.days = [32][]calEvent{}
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.Local)
	next := first.AddDate(0, 1, 0)
	dim := icsDaysIn(y, m)
	for _, ev := range calEventsBetween(first, next) {
		from := 1
		if !ev.Start.Before(first) {
			from = ev.Start.Day()
		}
		to := from
		if last := ev.End.Add(-time.Nanosecond); ev.End.After(ev.Start) {
			if !last.Before(next) {
				to = dim
			} else if !last.Before(first) {
				to = last.Day()
			}
		}
		for d := from; d <= to && d <= dim; d++ {
			if len(c.days[d]) < calEvDayMax {
				c.days[d] = append(c.days[d], ev)
			}
		}
	}
	return &c.days
}

// calEvDayEvents is the selected day's events.
func calEvDayEvents() []calEvent {
	return calEvMonthDays()[calEvSelectedDay()]
}

// calEvPicked is the agenda's picked event.
func calEvPicked() (calEvent, bool) {
	evs := calEvDayEvents()
	if len(evs) == 0 {
		return calEvent{}, false
	}
	i := min(max(calEvSel.idx, 0), len(evs)-1)
	return evs[i], true
}

// calEvStep picks the next (or previous) event of the day, wrapping.
func calEvStep(delta int) {
	n := len(calEvDayEvents())
	if n == 0 {
		return
	}
	calEvSel.idx = ((calEvSel.idx+delta)%n + n) % n
	calEvFollow(n)
	PlayNavSound()
}

// calEvFollow scrolls the agenda so the picked row is drawn.
func calEvFollow(n int) {
	if calEvSel.idx < calEvSel.scroll {
		calEvSel.scroll = calEvSel.idx
	}
	if calEvSel.idx >= calEvSel.scroll+calEvRows {
		calEvSel.scroll = calEvSel.idx - calEvRows + 1
	}
	calEvSel.scroll = max(0, min(calEvSel.scroll, n-calEvRows))
}

// calEvAlarm is Y: a one-time alarm for the picked event.
func calEvAlarm() {
	ev, ok := calEvPicked()
	if !ok {
		ShowToast("No event on this day", ToastKindInfo)
		return
	}
	calEvSetAlarm(ev)
}

// calEvSyncNow is X: sync at once, or say where calendars are added.
func calEvSyncNow() {
	if !calSyncHasSources() {
		ShowToast("No calendars yet — add one in Settings → Email & calendar", ToastKindInfo)
		return
	}
	calSyncNow("calendar")
	ShowToast("Syncing the calendar…", ToastKindInfo)
	PlayActivateSound()
}

// calendarPopoverKey handles the popover's keys while it is open. It runs before
// the scene's own input on every screen, because the popover floats above them
// all. Keys it does not name are left alone, so typing in a text field behind
// the popover still works.
func calendarPopoverKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !homeCalendarOpen {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		calEvMoveDays(-1)
	case sdl.K_RIGHT:
		calEvMoveDays(1)
	case sdl.K_UP:
		calEvMoveDays(-7)
	case sdl.K_DOWN:
		calEvMoveDays(7)
	case sdl.K_PAGEUP:
		shiftCalendarMonth(-1)
	case sdl.K_PAGEDOWN:
		shiftCalendarMonth(1)
	case sdl.K_t, sdl.K_HOME, sdl.K_RETURN, sdl.K_SPACE:
		resetCalendarView()
		PlayActivateSound()
	case sdl.K_TAB:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			calEvStep(-1)
		} else {
			calEvStep(1)
		}
	case sdl.K_y:
		if e.Repeat == 0 {
			calEvAlarm()
		}
	case sdl.K_x:
		if e.Repeat == 0 {
			calEvSyncNow()
		}
	case sdl.K_ESCAPE, sdl.K_b:
		homeCalendarOpen = false
		PlayBackSound()
	default:
		return false
	}
	return true
}

// calendarPopoverController is the pad's twin of calendarPopoverKey: the D-pad
// moves the selected day, the shoulders change month, A returns to today,
// Select picks the day's next event, Y sets its alarm, X syncs, B closes. It
// consumes every button while the popover is open so the scene underneath
// cannot react to the same press.
func calendarPopoverController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !homeCalendarOpen {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		calEvMoveDays(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		calEvMoveDays(1)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		calEvMoveDays(-7)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		calEvMoveDays(7)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		shiftCalendarMonth(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		shiftCalendarMonth(1)
	case sdl.CONTROLLER_BUTTON_A:
		resetCalendarView()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_BACK:
		calEvStep(1)
	case sdl.CONTROLLER_BUTTON_Y:
		calEvAlarm()
	case sdl.CONTROLLER_BUTTON_X:
		calEvSyncNow()
	case sdl.CONTROLLER_BUTTON_B:
		homeCalendarOpen = false
		PlayBackSound()
	}
	return true
}

// drawChevronRight is the forward arrow beside drawChevronLeft.
func drawChevronRight(renderer *sdl.Renderer, cx, cy, r int32, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	renderer.DrawLine(cx-r/2, cy-r, cx+r/2, cy)
	renderer.DrawLine(cx+r/2, cy, cx-r/2, cy+r)
}

// calEvColor is an event's dot and bar colour: its calendar's, or the accent.
func calEvColor(ev calEvent) sdl.Color {
	if c, ok := parseHexColor(ev.Color); ok {
		return c
	}
	return ColorAccent
}

// calEvTimeLabel is the agenda's time column for ev on the day starting at
// day: its start, "All day", or "– 10:00" for one that began earlier.
func calEvTimeLabel(config *Config, ev calEvent, day time.Time) string {
	next := day.AddDate(0, 0, 1)
	if ev.AllDay || (!ev.Start.After(day) && !ev.End.Before(next)) {
		return "All day"
	}
	if ev.Start.Before(day) {
		return "– " + clockDigits(config, ev.End.Local())
	}
	return clockDigits(config, ev.Start.Local())
}

// renderCalendarCard draws the month grid for the view's month with its top-left
// corner at (cx, cy), and records the header's hit targets.
func renderCalendarCard(renderer *sdl.Renderer, config *Config, cx, cy int32) {
	cardW, cardH := calendarCardSize()
	calendarPrevRect, calendarNextRect, calendarTitleRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	calEvGridRect, calEvSyncRect = sdl.Rect{}, sdl.Rect{}
	calEvRowRects = calEvRowRects[:0]

	// The shared overlay sheet: Home's glass, made opaque.
	drawOverlayCard(renderer, sdl.Rect{X: cx, Y: cy, W: cardW, H: cardH})

	medium, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if medium == nil {
		medium = small
	}
	if small == nil {
		return
	}
	tiny := FontForRole(config, TypeCaption)
	if tiny == nil {
		tiny = small
	}

	// ── Header: [‹] Month Year [›] ──
	navY := cy + calPopPadY
	calendarPrevRect = sdl.Rect{X: cx + calPopPadX, Y: navY, W: calPopNavSize, H: calPopNavSize}
	calendarNextRect = sdl.Rect{X: cx + cardW - calPopPadX - calPopNavSize, Y: navY, W: calPopNavSize, H: calPopNavSize}
	titleX := calendarPrevRect.X + calendarPrevRect.W + 6
	calendarTitleRect = sdl.Rect{
		X: titleX,
		Y: navY,
		W: calendarNextRect.X - 6 - titleX,
		H: calPopNavSize,
	}

	onToday := calendarShowsCurrentMonth()
	for _, nav := range []sdl.Rect{calendarPrevRect, calendarNextRect} {
		fillRoundedRect(renderer, nav.X, nav.Y, nav.W, nav.H, 8, ColorIconSurface)
		strokeRoundedRect(renderer, nav.X, nav.Y, nav.W, nav.H, 8, 1, ColorBorder)
	}
	iconCol := ColorTextSecondary()
	drawChevronLeft(renderer, calendarPrevRect.X+calPopNavSize/2, calendarPrevRect.Y+calPopNavSize/2, 9, iconCol)
	drawChevronRight(renderer, calendarNextRect.X+calPopNavSize/2, calendarNextRect.Y+calPopNavSize/2, 9, iconCol)

	// The month is the "back to today" target, so it is tinted whenever it is
	// not showing today's month.
	titleCol := ColorTextPrimary()
	if !onToday {
		titleCol = ColorAccent
	}
	title := calendarMonthTitle()
	tw, th, _ := medium.SizeUTF8(title)
	renderText(renderer, config, medium, title, titleCol,
		calendarTitleRect.X+(calendarTitleRect.W-int32(tw))/2,
		calendarTitleRect.Y+(calendarTitleRect.H-int32(th))/2)

	// ── Weekday row ──
	year, month := currentCalendarMonth()
	firstDay := time.Date(year, month, 1, 0, 0, 0, 0, time.Local).Weekday()
	daysInMonth := icsDaysIn(year, month)
	weekdayCol := ColorTextTertiary()
	if onToday {
		weekdayCol = WithAlpha(ColorAccent, 210)
	}
	for i := 0; i < 7; i++ {
		d := formatWeekdayInitial(time.Weekday(i)) // Sunday first, in the language
		dw, _, _ := small.SizeUTF8(d)
		renderText(renderer, config, small, d, weekdayCol,
			cx+calPopPadX+int32(i)*calPopCellW+(calPopCellW-int32(dw))/2,
			cy+calPopHeaderH)
	}

	// ── Day grid ──
	gridY := cy + calPopHeaderH + calPopWeekdayH
	calEvGridRect = sdl.Rect{X: cx + calPopPadX, Y: gridY, W: 7 * calPopCellW, H: calPopRows * calPopCellH}
	now := time.Now()
	today := 0
	if onToday {
		today = now.Day()
	}
	selected := calEvSelectedDay()
	days := calEvMonthDays()
	for row := 0; row < calPopRows; row++ {
		for col := 0; col < 7; col++ {
			dayNum := row*7 + col - int(firstDay) + 1
			if dayNum < 1 || dayNum > daysInMonth {
				continue
			}
			cellX := cx + calPopPadX + int32(col)*calPopCellW
			cellY := gridY + int32(row)*calPopCellH
			if dayNum == today {
				ccx, ccy := cellX+calPopCellW/2, cellY+calPopCellH/2
				fillCircle(renderer, ccx, ccy, 15, WithAlpha(ColorAccent, 55))
				fillCircle(renderer, ccx, ccy, 13, WithAlpha(ColorAccent, 110))
			}
			if dayNum == selected {
				strokeRoundedRect(renderer, cellX+3, cellY+1, calPopCellW-6, calPopCellH-2, 9, 2, WithAlpha(ColorAccent, 230))
			}
			numStr := fmt.Sprintf("%d", dayNum)
			nw, nh, _ := medium.SizeUTF8(numStr)
			dayCol := ColorTextSecondary()
			switch {
			case dayNum == today:
				dayCol = ColorTextPrimary()
			case col == 0 || col == 6:
				dayCol = sdl.Color{R: 180, G: 140, B: 140, A: 200}
			}
			renderText(renderer, config, medium, numStr, dayCol,
				cellX+(calPopCellW-int32(nw))/2, cellY+(calPopCellH-int32(nh))/2-3)
			calEvDrawDots(renderer, days[dayNum], cellX+calPopCellW/2, cellY+calPopCellH-6)
			// A day with an open task wears a small box (tasks_cal.go).
			tasksCalDrawMark(renderer, year, month, dayNum, cellX, cellY)
		}
	}

	// ── Agenda of the selected day ──
	agendaY := gridY + calPopRows*calPopCellH + calEvGap
	innerW := cardW - 2*calPopPadX
	calEvRenderAgenda(renderer, config, small, tiny, days[selected],
		time.Date(year, month, selected, 0, 0, 0, 0, time.Local), cx+calPopPadX, agendaY, innerW)

	// ── Controls hint ──
	evs := days[selected]
	pick, havePick := calEvPicked()
	var line1, line2 string
	if hintsUseKeyboard() {
		line1 = hintLine(FooterHint{"Arrows", "Day"}, FooterHint{"PgUp/PgDn", "Month"}, FooterHint{"T", "Today"})
		second := []FooterHint{}
		if len(evs) > 1 {
			second = append(second, FooterHint{"Tab", "Next Event"})
		}
		if havePick && !pick.AllDay && pick.Start.After(now) {
			second = append(second, FooterHint{"Y", "Set Alarm"})
		}
		line2 = hintLine(append(second, FooterHint{"X", "Sync"})...)
	} else {
		line1 = hintLine(FooterHint{"D-Pad", "Day"}, FooterHint{"L1 / R1", "Month"}, FooterHint{"A", "Today"})
		second := []FooterHint{}
		if len(evs) > 1 {
			second = append(second, FooterHint{"Select", "Next Event"})
		}
		if havePick && !pick.AllDay && pick.Start.After(now) {
			second = append(second, FooterHint{"Y", "Set Alarm"})
		}
		line2 = hintLine(append(second, FooterHint{"X", "Sync"})...)
	}
	hintY := cy + cardH - calPopPadY - 2*calPopHintH + 2
	renderText(renderer, config, tiny, ellipsize(tiny, line1, innerW), ColorTextTertiary(), cx+calPopPadX, hintY)
	renderText(renderer, config, tiny, ellipsize(tiny, line2, innerW), ColorTextTertiary(), cx+calPopPadX, hintY+calPopHintH)
}

// calEvDrawDots draws up to three dots under a day number, one per calendar
// colour found that day.
func calEvDrawDots(renderer *sdl.Renderer, evs []calEvent, cx, y int32) {
	if len(evs) == 0 {
		return
	}
	var cols []sdl.Color
	for _, ev := range evs {
		c := calEvColor(ev)
		dup := false
		for _, have := range cols {
			if have == c {
				dup = true
				break
			}
		}
		if !dup {
			cols = append(cols, c)
			if len(cols) == 3 {
				break
			}
		}
	}
	const gap = 7
	x := cx - int32(len(cols)-1)*gap/2
	for i, c := range cols {
		fillCircle(renderer, x+int32(i)*gap, y, 2, c)
	}
}

// calEvRenderAgenda draws the day's header and its event rows, and records
// their hit targets.
func calEvRenderAgenda(renderer *sdl.Renderer, config *Config, smallFont, tinyFont *ttf.Font, evs []calEvent, day time.Time, x, y, w int32) {
	// Separator above the strip.
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, ColorBorder.A)
	renderer.DrawLine(x, y-calEvGap/2, x+w, y-calEvGap/2)

	// Header: "Tuesday 18 November" … "2/5   Sync".
	label := formatDayLong(config, day)
	right := "Sync"
	if calSyncBusy() {
		right = "Syncing…"
	}
	rw, rh, _ := tinyFont.SizeUTF8(right)
	ry := y + (calEvHeadH-int32(rh))/2
	renderText(renderer, config, tinyFont, right, ColorTextAccent(), x+w-int32(rw), ry)
	calEvSyncRect = sdl.Rect{X: x + w - int32(rw) - 6, Y: y, W: int32(rw) + 12, H: calEvHeadH}
	rightEdge := x + w - int32(rw) - 12
	if n := len(evs); n > calEvRows {
		count := fmt.Sprintf("%d/%d", min(max(calEvSel.idx, 0), n-1)+1, n)
		cw, _, _ := tinyFont.SizeUTF8(count)
		renderText(renderer, config, tinyFont, count, ColorTextTertiary(), rightEdge-int32(cw), ry)
		rightEdge -= int32(cw) + 10
	}
	_, lh, _ := smallFont.SizeUTF8(label)
	renderText(renderer, config, smallFont, ellipsize(smallFont, label, rightEdge-x), ColorTextPrimary(),
		x, y+(calEvHeadH-int32(lh))/2)

	rowY := y + calEvHeadH
	if len(evs) == 0 {
		// The day's tasks take the strip when it has no events (tasks_cal.go).
		if tasksCalAgenda(renderer, config, smallFont, tinyFont, day, x, rowY, w, calEvRows) > 0 {
			return
		}
		msg := "No events"
		if !calSyncHasSources() {
			msg = "No calendars yet — Settings → Email & calendar"
		}
		_, mh, _ := tinyFont.SizeUTF8(msg)
		renderText(renderer, config, tinyFont, ellipsize(tinyFont, msg, w), ColorTextTertiary(),
			x, rowY+(calEvRowH-int32(mh))/2)
		return
	}
	idx := min(max(calEvSel.idx, 0), len(evs)-1)
	calEvFollow(len(evs))
	for i := calEvSel.scroll; i < len(evs) && i < calEvSel.scroll+calEvRows; i++ {
		ev := evs[i]
		r := sdl.Rect{X: x, Y: rowY + int32(i-calEvSel.scroll)*calEvRowH, W: w, H: calEvRowH}
		calEvRowRects = append(calEvRowRects, calEvRowHit{rect: r, idx: i})
		if i == idx {
			fillRoundedRect(renderer, r.X-4, r.Y+1, r.W+8, r.H-2, 6, WithAlpha(ColorAccent, 45))
		}
		fillRoundedRect(renderer, r.X, r.Y+5, 3, r.H-10, 1, calEvColor(ev))
		timeStr := calEvTimeLabel(config, ev, day)
		_, sh, _ := tinyFont.SizeUTF8(timeStr)
		renderText(renderer, config, tinyFont, ellipsize(tinyFont, timeStr, calEvTimeW-10), ColorTextSecondary(),
			r.X+8, r.Y+(r.H-int32(sh))/2)
		text := calSafeText(ev.Title, 80)
		if text == "" {
			text = "Event"
		}
		if loc := calSafeText(ev.Location, 40); loc != "" {
			text += " · " + loc
		}
		_, th, _ := smallFont.SizeUTF8(text)
		renderText(renderer, config, smallFont, ellipsize(smallFont, text, w-calEvTimeW-4), ColorTextPrimary(),
			r.X+calEvTimeW, r.Y+(r.H-int32(th))/2)
	}
	// The day's tasks fill the rows its events left free (tasks_cal.go).
	drawn := len(calEvRowRects)
	tasksCalAgenda(renderer, config, smallFont, tinyFont, day, x, rowY+int32(drawn)*calEvRowH, w, calEvRows-drawn)
}

// calendarPopoverNavigation consumes a click inside the card: the month
// header, a day of the grid, an agenda row, or "Sync". It reports whether the
// click was one of those targets.
func calendarPopoverNavigation(mx, my int32) bool {
	if calendarPrevRect.W > 0 && pointInRect(mx, my, calendarPrevRect) {
		shiftCalendarMonth(-1)
		return true
	}
	if calendarNextRect.W > 0 && pointInRect(mx, my, calendarNextRect) {
		shiftCalendarMonth(1)
		return true
	}
	if calendarTitleRect.W > 0 && pointInRect(mx, my, calendarTitleRect) {
		resetCalendarView()
		PlayActivateSound()
		return true
	}
	if calEvSyncRect.W > 0 && pointInRect(mx, my, calEvSyncRect) {
		calEvSyncNow()
		return true
	}
	for _, hit := range calEvRowRects {
		if pointInRect(mx, my, hit.rect) {
			// A tap picks the event; a tap on the picked one sets its alarm.
			// The picked row is the drawn one: idx clamped to the day's events.
			if n := len(calEvDayEvents()); n > 0 && hit.idx == min(max(calEvSel.idx, 0), n-1) {
				calEvAlarm()
			} else {
				calEvSel.idx = hit.idx
				PlayNavSound()
			}
			return true
		}
	}
	if calEvGridRect.W > 0 && pointInRect(mx, my, calEvGridRect) {
		y, m := currentCalendarMonth()
		col := int((mx - calEvGridRect.X) / calPopCellW)
		row := int((my - calEvGridRect.Y) / calPopCellH)
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.Local).Weekday()
		day := row*7 + col - int(first) + 1
		if day >= 1 && day <= icsDaysIn(y, m) {
			if day != calEvSelectedDay() {
				calEvSelect(y, m, day)
				PlayNavSound()
			}
			return true
		}
	}
	return false
}
