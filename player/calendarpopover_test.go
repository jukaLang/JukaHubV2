package main

// calendarpopover_test.go — the top-bar month view.
//
// The popover used 28x22 cells with the 24px body font, so a day number was
// taller than its own cell and the two-digit days nearly touched. These checks
// pin the two things that were wrong: the type must fit inside the cell with
// room to spare, and there must be a way to leave the current month (arrows,
// D-pad, shoulders, keyboard) that the touch and pad paths both reach.

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// keepCalendarState restores everything these tests touch.
func keepCalendarState(t *testing.T) {
	t.Helper()
	prevOpen, prevYear, prevMonth := homeCalendarOpen, calendarViewYear, calendarViewMonth
	prevPrev, prevNext, prevTitle := calendarPrevRect, calendarNextRect, calendarTitleRect
	t.Cleanup(func() {
		homeCalendarOpen, calendarViewYear, calendarViewMonth = prevOpen, prevYear, prevMonth
		calendarPrevRect, calendarNextRect, calendarTitleRect = prevPrev, prevNext, prevTitle
	})
}

// The day numeral must fit its cell at both canvases: this is the legibility
// defect the roomier grid exists for.
func TestCalendarDayNumbersFitTheirCells(t *testing.T) {
	newProbeFrame(t)
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		withScreen(t, size.w, size.h)
		medium, _ := getCachedFont(&Config{}, "medium")
		if medium == nil {
			t.Fatalf("%dx%d: no medium font, so the check cannot run", size.w, size.h)
		}
		_, fontH, _ := medium.SizeUTF8("31")
		if int32(fontH) > calPopCellH-8 {
			t.Fatalf("%dx%d: a %dpx day numeral in a %dpx cell leaves no breathing room",
				size.w, size.h, fontH, calPopCellH)
		}
		numW, _, _ := medium.SizeUTF8("31")
		if int32(numW) > calPopCellW-12 {
			t.Fatalf("%dx%d: a two-digit day is %dpx wide in a %dpx column",
				size.w, size.h, numW, calPopCellW)
		}
	}
}

// Month stepping has to roll the year, and returning to today has to be one
// action from anywhere.
func TestCalendarMonthStepping(t *testing.T) {
	keepCalendarState(t)
	calendarViewYear, calendarViewMonth = 2026, time.January

	shiftCalendarMonth(-1)
	if calendarViewYear != 2025 || calendarViewMonth != time.December {
		t.Fatalf("back from January 2026 landed on %v %d", calendarViewMonth, calendarViewYear)
	}
	shiftCalendarMonth(1)
	if calendarViewYear != 2026 || calendarViewMonth != time.January {
		t.Fatalf("forward from December 2025 landed on %v %d", calendarViewMonth, calendarViewYear)
	}
	shiftCalendarMonth(11)
	if calendarViewYear != 2026 || calendarViewMonth != time.December {
		t.Fatalf("eleven months ahead landed on %v %d", calendarViewMonth, calendarViewYear)
	}

	resetCalendarView()
	now := time.Now()
	if calendarViewYear != now.Year() || calendarViewMonth != now.Month() {
		t.Fatalf("resetCalendarView landed on %v %d, want today's month", calendarViewMonth, calendarViewYear)
	}
	if !calendarShowsCurrentMonth() {
		t.Fatal("the reset view does not report the current month")
	}
	shiftCalendarMonth(1)
	if calendarShowsCurrentMonth() {
		t.Fatal("a month other than today's reports itself as the current month")
	}
}

// Both arrows and the month title are real tap targets inside the card, and a
// tap on them must not dismiss the popover.
func TestCalendarPopoverHeaderTargetsAreTappable(t *testing.T) {
	keepCalendarState(t)
	renderer := probeRenderer(t)
	resetCalendarView()
	homeCalendarOpen = true

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderCalendarPopover(renderer, &Config{})

	card := homeCalendarRect()
	for name, r := range map[string]sdl.Rect{
		"previous month": calendarPrevRect,
		"next month":     calendarNextRect,
		"month title":    calendarTitleRect,
	} {
		if r.W < 30 || r.H < 30 {
			t.Errorf("%s target %+v is smaller than a fingertip", name, r)
		}
		if !pointInRect(r.X+r.W/2, r.Y+r.H/2, card) {
			t.Errorf("%s target %+v escapes the card %+v", name, r, card)
		}
	}
	if calendarPrevRect.X+calendarPrevRect.W > calendarTitleRect.X ||
		calendarTitleRect.X+calendarTitleRect.W > calendarNextRect.X {
		t.Fatalf("header targets overlap: prev %+v title %+v next %+v",
			calendarPrevRect, calendarTitleRect, calendarNextRect)
	}

	startMonth := calendarViewMonth
	next := calendarNextRect
	if !calendarPopoverClick(next.X+next.W/2, next.Y+next.H/2, nil) {
		t.Fatal("a tap on the next-month arrow was not consumed")
	}
	if !homeCalendarOpen {
		t.Fatal("a tap on the arrow dismissed the popover")
	}
	if calendarViewMonth == startMonth {
		t.Fatal("a tap on the next-month arrow did not change the month")
	}
	if calendarShowsCurrentMonth() {
		t.Fatal("the view did not move off the current month")
	}

	title := calendarTitleRect
	if !calendarPopoverClick(title.X+title.W/2, title.Y+title.H/2, nil) {
		t.Fatal("a tap on the month title was not consumed")
	}
	if !calendarShowsCurrentMonth() {
		t.Fatal("a tap on the month title did not return to today")
	}
}

// The keyboard and the pad both drive the month view, and the popover keeps the
// scene underneath from reacting to the same press.
func TestCalendarPopoverKeysAndPad(t *testing.T) {
	keepCalendarState(t)
	resetCalendarView()
	homeCalendarOpen = true

	start := calendarMonthStart()
	key := func(sym sdl.Keycode) bool {
		return calendarPopoverKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}})
	}
	if !key(sdl.K_RIGHT) {
		t.Fatal("Right was not consumed by the open popover")
	}
	if calendarMonthStart().Equal(start) {
		t.Fatal("Right did not move to the next month")
	}
	if !key(sdl.K_PAGEDOWN) || calendarMonthStart().Equal(start.AddDate(0, 1, 0)) {
		t.Fatal("PageDown did not step a second month forward")
	}
	key(sdl.K_PAGEUP)
	key(sdl.K_PAGEUP)
	if !calendarMonthStart().Equal(start) {
		t.Fatalf("PageUp twice did not return to %v", start.Format("January 2006"))
	}
	if !key(sdl.K_t) || !calendarShowsCurrentMonth() {
		t.Fatal("T did not return to today")
	}
	if !key(sdl.K_ESCAPE) || homeCalendarOpen {
		t.Fatal("Escape did not close the popover")
	}

	// Pad: the D-pad and the shoulders step months, A returns to today, B
	// closes, and an unmapped button is still swallowed so the scene does not
	// scroll behind the popover.
	homeCalendarOpen = true
	resetCalendarView()
	start = calendarMonthStart()
	pad := func(b sdl.GameControllerButton) bool {
		return calendarPopoverController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: uint8(b)})
	}
	if !pad(sdl.CONTROLLER_BUTTON_DPAD_LEFT) || !calendarMonthStart().Equal(start.AddDate(0, -1, 0)) {
		t.Fatal("D-pad Left did not move one month back")
	}
	if !pad(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER) || !calendarMonthStart().Equal(start) {
		t.Fatal("the right shoulder did not move one month forward")
	}
	if !pad(sdl.CONTROLLER_BUTTON_DPAD_UP) {
		t.Fatal("D-pad Up was not consumed by the open popover")
	}
	if !calendarMonthStart().Equal(start) {
		t.Fatal("D-pad Up changed the month")
	}
	shiftCalendarMonth(3)
	if !pad(sdl.CONTROLLER_BUTTON_A) || !calendarShowsCurrentMonth() {
		t.Fatal("A did not return to today")
	}
	if !pad(sdl.CONTROLLER_BUTTON_B) || homeCalendarOpen {
		t.Fatal("B did not close the popover")
	}
}

// Opening the popover always starts on today's month: a view left on March must
// not look like the clock's date.
func TestCalendarPopoverOpensOnToday(t *testing.T) {
	keepCalendarState(t)
	homeCalendarOpen = false
	calendarViewYear, calendarViewMonth = 2020, time.March
	toggleCalendarPopover()
	if !homeCalendarOpen {
		t.Fatal("toggle did not open the popover")
	}
	if !calendarShowsCurrentMonth() {
		t.Fatalf("opened on %v %d, want today's month", calendarViewMonth, calendarViewYear)
	}
}

// calendarMonthStart is the first day of the month on display, for comparing
// month positions.
func calendarMonthStart() time.Time {
	year, month := currentCalendarMonth()
	return time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
}
