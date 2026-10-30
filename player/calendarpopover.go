package main

// calendarpopover.go — the top-bar clock's month view.
//
// The popover used to reuse the Alarm screen's corner widget: 28x22 cells drawn
// with the 24px body font. A 24px numeral in a 22px row is taller than its cell,
// so the numbers sat on top of each other, and two-digit days nearly touched in
// a 28px column. It also always showed the current month — there was no way to
// look back at last week or ahead at next month.
//
// The popover now has its own layout: roomier cells, a month header with back
// and forward arrows, and a fixed six-row grid so the arrows and the grid do not
// jump when a month needs five rows. Left/Right (or the shoulders) change month,
// the month title returns to today, and the same actions are touch targets.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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
	// calPopHintH reserves the line that explains the controls. It is always
	// drawn, so switching between months cannot shift the grid.
	calPopHintH = 18
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
)

// calendarCardSize is the popover card's fixed size.
func calendarCardSize() (int32, int32) {
	return int32(calPopCellW*7 + calPopPadX*2),
		int32(calPopHeaderH + calPopWeekdayH + calPopRows*calPopCellH + calPopHintH + calPopPadY*2)
}

// currentCalendarMonth is the month on display, defaulting to the real one.
func currentCalendarMonth() (int, time.Month) {
	if calendarViewYear == 0 && calendarViewMonth == 0 {
		now := time.Now()
		return now.Year(), now.Month()
	}
	return calendarViewYear, calendarViewMonth
}

// resetCalendarView puts the view back on the current month.
func resetCalendarView() {
	now := time.Now()
	calendarViewYear, calendarViewMonth = now.Year(), now.Month()
}

// shiftCalendarMonth steps the view by whole months, rolling the year with it.
func shiftCalendarMonth(delta int) {
	year, month := currentCalendarMonth()
	t := time.Date(year, month, 1, 0, 0, 0, 0, time.Local).AddDate(0, delta, 0)
	calendarViewYear, calendarViewMonth = t.Year(), t.Month()
	PlayNavSound()
}

// calendarMonthTitle is the header label.
func calendarMonthTitle() string {
	year, month := currentCalendarMonth()
	return time.Date(year, month, 1, 0, 0, 0, 0, time.Local).Format("January 2006")
}

// calendarShowsCurrentMonth reports whether the view is on today's month.
func calendarShowsCurrentMonth() bool {
	year, month := currentCalendarMonth()
	now := time.Now()
	return year == now.Year() && month == now.Month()
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
	case sdl.K_LEFT, sdl.K_PAGEUP:
		shiftCalendarMonth(-1)
	case sdl.K_RIGHT, sdl.K_PAGEDOWN:
		shiftCalendarMonth(1)
	case sdl.K_t, sdl.K_RETURN, sdl.K_SPACE:
		resetCalendarView()
		PlayActivateSound()
	case sdl.K_UP, sdl.K_DOWN:
		// The popover is open, so the D-pad must not scroll the scene behind
		// it. There is nothing to move within the month view itself.
	case sdl.K_ESCAPE, sdl.K_b:
		homeCalendarOpen = false
		PlayBackSound()
	default:
		return false
	}
	return true
}

// calendarPopoverController is the pad's twin of calendarPopoverKey: the arrows
// change month, A returns to today, B closes. It consumes every button while the
// popover is open so the scene underneath cannot react to the same press.
func calendarPopoverController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !homeCalendarOpen {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		shiftCalendarMonth(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		shiftCalendarMonth(1)
	case sdl.CONTROLLER_BUTTON_A:
		resetCalendarView()
		PlayActivateSound()
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

// renderCalendarCard draws the month grid for the view's month with its top-left
// corner at (cx, cy), and records the header's hit targets.
func renderCalendarCard(renderer *sdl.Renderer, config *Config, cx, cy int32) {
	cardW, cardH := calendarCardSize()
	calendarPrevRect, calendarNextRect, calendarTitleRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}

	fillRoundedRect(renderer, cx, cy, cardW, cardH, 14, sdl.Color{R: 12, G: 16, B: 28, A: 240})
	strokeRoundedRect(renderer, cx, cy, cardW, cardH, 14, 1, ColorBorder)

	medium, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if medium == nil {
		medium = small
	}
	if small == nil {
		return
	}
	tiny := getFontSized(config, "small", 11)
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
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, time.Local).Day()
	weekdayCol := ColorTextTertiary()
	if onToday {
		weekdayCol = WithAlpha(ColorAccent, 210)
	}
	for i, d := range [7]string{"S", "M", "T", "W", "T", "F", "S"} {
		dw, _, _ := small.SizeUTF8(d)
		renderText(renderer, config, small, d, weekdayCol,
			cx+calPopPadX+int32(i)*calPopCellW+(calPopCellW-int32(dw))/2,
			cy+calPopHeaderH)
	}

	// ── Day grid ──
	gridY := cy + calPopHeaderH + calPopWeekdayH
	now := time.Now()
	today := 0
	if onToday {
		today = now.Day()
	}
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
				cellX+(calPopCellW-int32(nw))/2, cellY+(calPopCellH-int32(nh))/2)
		}
	}

	// ── Controls hint ──
	hint := "Left/Right change month · select the month for today"
	renderText(renderer, config, tiny, ellipsize(tiny, hint, cardW-2*calPopPadX),
		ColorTextTertiary(), cx+calPopPadX, cy+cardH-calPopPadY-calPopHintH+2)
}

// calendarPopoverNavigation consumes a click on the month header. It reports
// whether the click was one of the header's targets.
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
	return false
}
