package main

// tasks_cal.go — tasks in the clock's calendar popover (calendarpopover.go).
//
// A day of the month grid with an open task due on it wears a small box in
// its top-right corner, apart from the coloured event dots under the number.
// The agenda strip under the grid lists the selected day's tasks in the rows
// its events leave free, each with a checkbox where an event has its
// calendar's colour bar, the task's time (or "Task") in the time column, and
// finished ones ticked and dimmed. The popover only shows them: they are
// changed in the Tasks app.

import (
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// tasksCalDayMax bounds the tasks kept for one day of the month view.
const tasksCalDayMax = 40

// tasksCalEntry is one task in the popover.
type tasksCalEntry struct {
	text  string
	timed bool
	min   int
	done  bool
}

// tasksCalMonth caches the shown month's tasks per day, rebuilt when a task
// changes or the month does. Main thread only.
var tasksCalMonth struct {
	ok   bool
	rev  uint64
	y    int
	m    time.Month
	days [32][]tasksCalEntry
	open [32]bool
}

// tasksCalDays is month y/m's tasks per day (index 1..31), open ones first.
func tasksCalDays(y int, m time.Month) *[32][]tasksCalEntry {
	c := &tasksCalMonth
	if c.ok && c.rev == tasksRev.Load() && c.y == y && c.m == m {
		return &c.days
	}
	_, items, rev := tasksSnapshot()
	c.ok, c.rev, c.y, c.m = true, rev, y, m
	c.days, c.open = [32][]tasksCalEntry{}, [32]bool{}
	tasksSortItems(items)
	for _, it := range items {
		d, ok := tasksParseDue(it.Due)
		if !ok {
			continue
		}
		dy, dm, dd := d.day.Date()
		if dy != y || dm != m || len(c.days[dd]) >= tasksCalDayMax {
			continue
		}
		c.days[dd] = append(c.days[dd], tasksCalEntry{text: it.Text, timed: d.timed, min: d.min, done: it.Done})
		if !it.Done {
			c.open[dd] = true
		}
	}
	return &c.days
}

// tasksCalDrawMark marks a grid day that has an open task: a small box in
// the cell's top-right corner. calendarpopover.go calls it for every day.
func tasksCalDrawMark(renderer *sdl.Renderer, y int, m time.Month, day int, cellX, cellY int32) {
	tasksCalDays(y, m)
	if day < 1 || day > 31 || !tasksCalMonth.open[day] {
		return
	}
	const s = 8
	strokeRoundedRect(renderer, cellX+calPopCellW-s-5, cellY+4, s, s, 2, 1, ColorTextSecondary())
}

// tasksCalAgenda draws day's tasks in up to rows agenda rows from y down and
// returns how many rows it used. When they do not fit, the last row says how
// many more there are.
func tasksCalAgenda(renderer *sdl.Renderer, config *Config, smallFont, tinyFont *ttf.Font, day time.Time, x, y, w int32, rows int) int {
	if rows <= 0 || smallFont == nil || tinyFont == nil {
		return 0
	}
	dy, dm, dd := day.Date()
	list := tasksCalDays(dy, dm)[dd]
	if len(list) == 0 {
		return 0
	}
	show, more := min(len(list), rows), 0
	if len(list) > rows {
		show = rows - 1
		more = len(list) - show
	}
	for i := 0; i < show; i++ {
		t := list[i]
		r := sdl.Rect{X: x, Y: y + int32(i)*calEvRowH, W: w, H: calEvRowH}
		const box = 12
		bx, by := r.X, r.Y+(r.H-box)/2
		ink := ColorTextPrimary()
		if t.done {
			ink = ColorTextTertiary()
			fillRoundedRect(renderer, bx, by, box, box, 3, WithAlpha(ColorAccent, 160))
			iconDraw(renderer, "check", sdl.Rect{X: bx + 2, Y: by + 2, W: box - 4, H: box - 4}, ColorIconDark)
		} else {
			strokeRoundedRect(renderer, bx, by, box, box, 3, 1, ColorTextSecondary())
		}
		when := "Task"
		if t.timed {
			when = clockDigitsHM(config, t.min/60, t.min%60)
		}
		_, sh, _ := tinyFont.SizeUTF8(when)
		renderText(renderer, config, tinyFont, ellipsize(tinyFont, when, calEvTimeW-box-10), ColorTextSecondary(),
			r.X+box+6, r.Y+(r.H-int32(sh))/2)
		_, th, _ := smallFont.SizeUTF8(t.text)
		renderText(renderer, config, smallFont, ellipsize(smallFont, t.text, w-calEvTimeW-4), ink,
			r.X+calEvTimeW, r.Y+(r.H-int32(th))/2)
	}
	if more > 0 {
		msg := "+" + strconv.Itoa(more) + " more in Tasks"
		if show == 0 {
			msg = tasksPlural(more, "task", "tasks") + " in Tasks"
		}
		r := sdl.Rect{X: x, Y: y + int32(show)*calEvRowH, W: w, H: calEvRowH}
		_, mh, _ := tinyFont.SizeUTF8(msg)
		renderText(renderer, config, tinyFont, ellipsize(tinyFont, msg, w), ColorTextTertiary(), r.X, r.Y+(r.H-int32(mh))/2)
		return show + 1
	}
	return show
}
