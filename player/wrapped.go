package main

// wrapped.go — the Playtime screen's insights half.
//
// playtime.go already collects the series this needs (Days for the last 90
// days, Scenes for the lifetime split) and showed only the second one plus seven
// bars for the past week. This file turns the same data into the shape a player
// actually asks about — how consistent have I been (a 13-week heatmap and the
// current streak), and what was my best day. Nothing new is tracked; the numbers
// are derived per frame from the snapshot, which is a few hundred map lookups and
// therefore far cheaper than the render calls around it.
//
// The grid is 13 whole weeks ending on the current week, so every column is a
// real calendar week (Monday first) and the last column is today's week rather
// than a ragged stub. Days after today are present in the slice but marked
// Future, so a caller that draws the grid never needs to know the date again.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// wrappedWeeks is the heatmap width. 13 weeks is the largest whole-week
	// grid that fits the 90 days playtimePruneDays keeps.
	wrappedWeeks = 13
	// wrappedDays is the cell count: whole weeks only.
	wrappedDays = wrappedWeeks * 7
	// wrappedDayKey is the layout playtimeData.Days is keyed by.
	wrappedDayKey = "2006-01-02"
)

// wrappedDay is one heatmap cell.
type wrappedDay struct {
	Day     time.Time
	Seconds int64
	Future  bool // after today: drawn as an empty slot, never counted
}

// wrappedInsights is everything the insights card draws, computed once per
// frame so the renderer only formats strings.
type wrappedInsights struct {
	Days          []wrappedDay // oldest first, Monday-aligned, today inside
	Total         int64        // sum over the window, excluding future cells
	BestSeconds   int64
	BestDay       time.Time
	CurrentStreak int
	LongestStreak int
	ActiveDays    int
	DailyAverage  int64 // mean over days with any time, not calendar days
	MaxDay        int64 // the heatmap's brightest cell; 0 when the window is empty
}

// buildWrappedInsights derives the window, the streaks and the best day from a
// playtime snapshot. now is a parameter rather than time.Now() so the shape of
// the grid can be tested on a fixed date, including a Monday and a Sunday.
func buildWrappedInsights(data playtimeData, now time.Time) wrappedInsights {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// Monday-first row of today (time.Weekday puts Sunday at 0).
	todayRow := (int(today.Weekday()) + 6) % 7
	// The grid ends with this week's Sunday, so it starts todayRow days before
	// the last column's Monday. Whole weeks means the start is always a Monday.
	gridStart := today.AddDate(0, 0, -(wrappedDays - 7 + todayRow))

	ins := wrappedInsights{Days: make([]wrappedDay, 0, wrappedDays)}
	for i := 0; i < wrappedDays; i++ {
		day := gridStart.AddDate(0, 0, i)
		cell := wrappedDay{Day: day, Future: day.After(today)}
		if !cell.Future {
			cell.Seconds = data.Days[day.Format(wrappedDayKey)]
		}
		ins.Days = append(ins.Days, cell)
	}

	// One pass over the window: totals, the best day, the active-day count and
	// the longest run of consecutive active days.
	run := 0
	for _, cell := range ins.Days {
		if cell.Future {
			continue
		}
		if cell.Seconds > 0 {
			ins.Total += cell.Seconds
			ins.ActiveDays++
			run++
			if run > ins.LongestStreak {
				ins.LongestStreak = run
			}
			// ">=" keeps the later day on a tie: the most recent best day is
			// the one a player recognises.
			if cell.Seconds >= ins.BestSeconds {
				ins.BestSeconds = cell.Seconds
				ins.BestDay = cell.Day
			}
			if cell.Seconds > ins.MaxDay {
				ins.MaxDay = cell.Seconds
			}
		} else {
			run = 0
		}
	}
	if ins.ActiveDays > 0 {
		ins.DailyAverage = ins.Total / int64(ins.ActiveDays)
	}

	// The current streak counts back from today. An empty today does not break
	// it: the day is still in progress, and a streak that vanished at midnight
	// until the first minute of play would be a bad reading, not a strict one.
	ins.CurrentStreak = wrappedStreakEndingAt(data, today)
	return ins
}

// wrappedStreakEndingAt counts consecutive days with play, ending today (or
// yesterday when today is still empty).
func wrappedStreakEndingAt(data playtimeData, today time.Time) int {
	day := today
	if data.Days[day.Format(wrappedDayKey)] <= 0 {
		day = day.AddDate(0, 0, -1)
	}
	streak := 0
	for data.Days[day.Format(wrappedDayKey)] > 0 {
		streak++
		day = day.AddDate(0, 0, -1)
		// The window is bounded even if a hand-edited snapshot holds older
		// keys, so a corrupted file cannot spin here.
		if streak > wrappedDays+366 {
			break
		}
	}
	return streak
}

// wrappedHeatColor maps a day's seconds onto the heatmap ramp. Zero is the
// track colour; everything else steps through four accent alphas so the grid
// reads as quantity, not just presence.
func wrappedHeatColor(seconds, max int64) sdl.Color {
	if seconds <= 0 {
		return WithAlpha(ColorBorder, 60)
	}
	level := 1
	if max > 0 {
		switch {
		case seconds*4 >= max*3:
			level = 4
		case seconds*2 >= max:
			level = 3
		case seconds*4 >= max:
			level = 2
		}
	}
	alpha := [5]uint8{60, 80, 130, 185, 235}[level]
	return WithAlpha(accentColor, alpha)
}

// drawWrappedHeatmap draws the Monday-aligned grid into rect, one rounded cell
// per day, oldest in the top-left. The pitch is derived from the rect, so the
// caller owns the size and no cell can be clipped by the card's edge.
func drawWrappedHeatmap(renderer *sdl.Renderer, config *Config, rect sdl.Rect, ins wrappedInsights) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	// Pitch from the rect, not a fixed cell size: thirteen cells plus their gaps
	// have to fit whatever width the card gives them.
	gap := int32(3)
	pitchX := rect.W / wrappedWeeks
	pitchY := rect.H / 7
	cell := pitchX - gap
	if pitchY-gap < cell {
		cell = pitchY - gap
	}
	if cell < 6 {
		cell = 6
	}
	radius := cell / 4
	if radius < 2 {
		radius = 2
	}
	for i, day := range ins.Days {
		col := int32(i / 7)
		row := int32(i % 7)
		x := rect.X + col*pitchX
		y := rect.Y + row*pitchY
		if x+cell > rect.X+rect.W || y+cell > rect.Y+rect.H {
			continue
		}
		if day.Future {
			// An unfilled slot still gets its outline, so the current week
			// looks like a week rather than a shorter row.
			fillRoundedRect(renderer, x, y, cell, cell, radius, WithAlpha(ColorBorder, 25))
			continue
		}
		fillRoundedRect(renderer, x, y, cell, cell, radius, wrappedHeatColor(day.Seconds, ins.MaxDay))
	}
}

// wrappedBestDayLabel is the best day as "Sat · 2h 10m", or "—" before the
// first minute of play exists in the window.
func wrappedBestDayLabel(ins wrappedInsights) string {
	if ins.BestSeconds <= 0 {
		return "—"
	}
	return ins.BestDay.Format("Mon") + " · " + formatPlaytime(ins.BestSeconds)
}

// wrappedDaysLabel renders a streak as "1 day" / "12 days".
func wrappedDaysLabel(n int) string {
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}
