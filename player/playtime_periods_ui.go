package main

// playtime_periods_ui.go — the Playtime screen's By game tab: the games, or the
// consoles, ranked by the time played today, this week, this month, this year
// or ever, each with a bar against the longest. Left/Right steps the period, X
// switches between games and consoles, Up/Down scroll the list. It is the
// trophy room's third tab (trophy_room.go routes its input, clicks, wheel and
// footer here); the numbers come from playtime_periods.go.
//
// Ever is the lifetime ledger, so it is complete from the first day. The other
// periods count from the day the store began (its Since): a period that began
// earlier says so under the list rather than passing a partial week off as a
// whole one.

import (
	"context"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

var playtimePeriodsUI struct {
	// period and consoles are kept for the session, like the trophy filter.
	period    int
	periodSet bool
	consoles  bool
	// scroll is the first row on screen; a new visit starts at the top.
	scroll int
	visit  context.Context

	// The list and its strings, rebuilt only when the figures, the day, the
	// period or the view change.
	built        bool
	builtRev     uint64
	builtPlayRev uint64
	builtDay     int
	builtPeriod  int
	builtCons    bool
	rows         []playtimePeriodRow
	vals         []string
	total        string
	caption      string
	note         string
	empty        string

	// The last frame's geometry, for clicks, the wheel and paging.
	periodRects [playtimePeriodCount]sdl.Rect
	modeRects   [2]sdl.Rect
	fit         int
}

// playtimePeriodsSyncVisit starts the list at the top on a new visit to the
// room; the period and the view stay as they were.
func playtimePeriodsSyncVisit() {
	ui := &playtimePeriodsUI
	if !ui.periodSet {
		ui.period, ui.periodSet = playtimePeriodWeek, true
	}
	if ui.visit != trophyRoomVisitCtx {
		ui.visit = trophyRoomVisitCtx
		ui.scroll = 0
	}
}

// playtimePeriodsRefresh rebuilds the list when something it shows changed.
// Inside the room time accrues every second, which moves Ever (the lifetime
// ledger is part of it) but not the other periods.
func playtimePeriodsRefresh(config *Config) {
	ui := &playtimePeriodsUI
	playtimePeriodsEnsure()
	now := time.Now()
	day := now.Year()*400 + now.YearDay()
	playRev := uint64(0)
	if ui.period == playtimePeriodEver {
		playRev = playtime.rev
	}
	if ui.built && ui.builtRev == playtimePeriods.rev && ui.builtPlayRev == playRev && ui.builtDay == day &&
		ui.builtPeriod == ui.period && ui.builtCons == ui.consoles {
		return
	}
	ui.built = true
	ui.builtRev, ui.builtPlayRev, ui.builtDay = playtimePeriods.rev, playRev, day
	ui.builtPeriod, ui.builtCons = ui.period, ui.consoles

	ui.rows = playtimePeriodRows(playtimePeriodTotals(ui.period, now), ui.consoles)
	ui.vals = ui.vals[:0]
	var sum int64
	for _, row := range ui.rows {
		ui.vals = append(ui.vals, formatPlaytime(row.Seconds))
		sum += row.Seconds
	}
	ui.total = ""
	if sum > 0 {
		ui.total = formatPlaytime(sum) + " in all"
	}
	view := "GAMES"
	if ui.consoles {
		view = "CONSOLES"
	}
	label := playtimePeriodLabels[ui.period]
	ui.caption = strings.ToUpper(label) + " · " + view
	if ui.period == playtimePeriodEver {
		ui.empty = "Games you start from JukaHub show up here."
	} else {
		ui.empty = "No game played " + strings.ToLower(label) + " yet."
	}
	ui.note = ""
	since := playtimePeriods.data.Since
	if ui.period != playtimePeriodEver && since != "" && since > playtimePeriodStart(ui.period, now) {
		if t, err := time.ParseInLocation(wrappedDayKey, since, now.Location()); err == nil {
			ui.note = "Counted from " + formatDateNear(config, t, now) + ": days before that were not kept by game."
		}
	}
	playtimePeriodsClampScroll()
}

// playtimePeriodsMaxScroll is the last first-row the list can show.
func playtimePeriodsMaxScroll() int {
	ui := &playtimePeriodsUI
	fit := ui.fit
	if fit < 1 {
		fit = 1
	}
	return max(0, len(ui.rows)-fit)
}

// playtimePeriodsClampScroll keeps the list inside its rows.
func playtimePeriodsClampScroll() {
	ui := &playtimePeriodsUI
	ui.scroll = min(max(ui.scroll, 0), playtimePeriodsMaxScroll())
}

// playtimePeriodsBadgeWidth is how wide trophyRoomBadge draws label.
func playtimePeriodsBadgeWidth(f trophyFonts, label string) int32 {
	if f.caption == nil {
		return 0
	}
	tw, th := measureText(f.caption, label)
	return max(tw+12, th+6)
}

// playtimePeriodsDraw draws the tab under the room's header and tabs: the
// period pills, the Games / Consoles pair, and the ranked list in a card.
func playtimePeriodsDraw(renderer *sdl.Renderer, config *Config, f trophyFonts, l trophyRoomLayout) {
	ui := &playtimePeriodsUI
	playtimePeriodsSyncVisit()
	playtimePeriodsRefresh(config)
	ui.periodRects = [playtimePeriodCount]sdl.Rect{}
	ui.modeRects = [2]sdl.Rect{}
	if f.chip == nil || f.cardDesc == nil {
		return
	}
	c := l.content
	pillH := l.tabsH - 4
	const gap = int32(8)

	// The periods from the left.
	x, y := c.X, c.Y
	for i := 0; i < playtimePeriodCount; i++ {
		label := playtimePeriodLabels[i]
		tw, _ := measureText(f.chip, label)
		rect := sdl.Rect{X: x, Y: y, W: tw + 2*14, H: pillH}
		trophyRoomPill(renderer, config, f.chip, rect, label, i == ui.period, false)
		ui.periodRects[i] = rect
		x += rect.W + gap
	}
	// X and the Games / Consoles pair at the right, or on a row of their own
	// when the periods leave no room for them.
	modes := [2]string{"Games", "Consoles"}
	var modeW [2]int32
	key := hintKeyFor("X", "Consoles")
	pairW := playtimePeriodsBadgeWidth(f, key) + 8
	for i, label := range modes {
		tw, _ := measureText(f.chip, label)
		modeW[i] = tw + 2*14
		pairW += modeW[i]
	}
	pairW += gap
	mx, my := c.X+c.W-pairW, y
	if mx < x+12 {
		mx, my = c.X, y+pillH+gap
	}
	mx += trophyRoomBadge(renderer, config, f.caption, mx, my+pillH/2, key) + 8
	for i, label := range modes {
		rect := sdl.Rect{X: mx, Y: my, W: modeW[i], H: pillH}
		trophyRoomPill(renderer, config, f.chip, rect, label, (i == 1) == ui.consoles, false)
		ui.modeRects[i] = rect
		mx += rect.W + gap
	}

	top := my + pillH + 12
	card := sdl.Rect{X: c.X, Y: top, W: c.W, H: c.Y + c.H - top}
	if card.H < 80 {
		return
	}
	bodyY := trophyRoomSectionCard(renderer, config, f, card, ui.caption, ui.total)
	listBottom := card.Y + card.H - 12
	if ui.note != "" && f.cardMeta != nil {
		metaH := trophyFontH(f.cardMeta, 16)
		listBottom -= metaH + 8
		renderText(renderer, config, f.cardMeta, ellipsize(f.cardMeta, ui.note, card.W-40), ColorTextTertiary(),
			card.X+20, card.Y+card.H-12-metaH)
	}
	if len(ui.rows) == 0 {
		renderText(renderer, config, f.cardDesc, ellipsize(f.cardDesc, ui.empty, card.W-40), ColorTextSecondary(),
			card.X+20, bodyY+4)
		return
	}

	lineH := trophyFontH(f.cardDesc, 18)
	pitch := lineH + 12
	ui.fit = max(1, int((listBottom-bodyY)/pitch))
	playtimePeriodsClampScroll()
	scrollable := len(ui.rows) > ui.fit
	x0 := card.X + 20
	right := card.X + card.W - 20
	if scrollable {
		right -= 16 // the scrollbar's lane
	}
	inner := right - x0
	nameW, subW := inner*34/100, inner*20/100
	if ui.consoles {
		nameW, subW = inner*30/100, inner*14/100
	}
	valW := int32(0)
	for i := ui.scroll; i < len(ui.vals) && i < ui.scroll+ui.fit; i++ {
		if w, _ := measureText(f.cardDesc, ui.vals[i]); w > valW {
			valW = w
		}
	}
	subFont := f.cardMeta
	if subFont == nil {
		subFont = f.cardDesc
	}
	subH := trophyFontH(subFont, 16)
	barX := x0 + nameW + 10 + subW + 10
	barW := right - valW - 12 - barX
	maxSecs := ui.rows[0].Seconds
	accent := glassAccent()
	for i := 0; i < ui.fit && ui.scroll+i < len(ui.rows) && ui.scroll+i < len(ui.vals); i++ {
		idx := ui.scroll + i
		row := ui.rows[idx]
		ry := bodyY + int32(i)*pitch
		// The console as the library names it (Theme & look → Console names,
		// consolenames.go), looked up as it draws so a new style shows at once.
		name, sub := row.Name, row.Sub
		if ui.consoles {
			name = consoleDisplayName(name)
		} else {
			sub = consoleDisplayName(sub)
		}
		renderText(renderer, config, f.cardDesc, ellipsize(f.cardDesc, name, nameW), ColorTextSecondary(), x0, ry)
		if sub != "" {
			renderText(renderer, config, subFont, ellipsize(subFont, sub, subW), ColorTextTertiary(),
				x0+nameW+10, ry+(lineH-subH)/2)
		}
		if barW > 10 {
			frac := 0.0
			if maxSecs > 0 {
				frac = float64(row.Seconds) / float64(maxSecs)
			}
			fill := accent
			if idx > 0 {
				fill = WithAlpha(accent, 170)
			}
			UIProgressBar(renderer, sdl.Rect{X: barX, Y: ry + lineH/2 - 4, W: barW, H: 8}, frac, glassA(16), fill)
		}
		vw, _ := measureText(f.cardDesc, ui.vals[idx])
		renderText(renderer, config, f.cardDesc, ui.vals[idx], ColorTextTertiary(), right-vw, ry)
	}
	if scrollable {
		trackH := int32(ui.fit)*pitch - 12
		drawScrollbar(renderer, card.X+card.W-20-6, bodyY, 6, trackH,
			float64(ui.fit)/float64(len(ui.rows)), float64(ui.scroll)/float64(playtimePeriodsMaxScroll()))
	}
}

// playtimePeriodsSetPeriod shows period from the top of its list.
func playtimePeriodsSetPeriod(period int) {
	ui := &playtimePeriodsUI
	if period < 0 || period >= playtimePeriodCount || period == ui.period {
		return
	}
	ui.period, ui.scroll = period, 0
	PlayNavSound()
}

// playtimePeriodsSetConsoles switches between games and consoles.
func playtimePeriodsSetConsoles(consoles bool) {
	ui := &playtimePeriodsUI
	if ui.consoles == consoles {
		return
	}
	ui.consoles, ui.scroll = consoles, 0
	PlayActivateSound()
}

// playtimePeriodsScrollTo moves the list's first row, with a sound when it
// moved.
func playtimePeriodsScrollTo(to int) {
	ui := &playtimePeriodsUI
	to = min(max(to, 0), playtimePeriodsMaxScroll())
	if to != ui.scroll {
		ui.scroll = to
		PlayNavSound()
	}
}

// playtimePeriodsAction applies one input on the tab (trophyRoomAction hands
// it everything but Back, Y and the tab switches). Up at the top of the list
// is not used, so the top bar can take it.
func playtimePeriodsAction(act trophyAction) bool {
	ui := &playtimePeriodsUI
	playtimePeriodsSyncVisit()
	page := max(1, ui.fit)
	switch act {
	case trophyActLeft:
		playtimePeriodsSetPeriod(ui.period - 1)
	case trophyActRight:
		playtimePeriodsSetPeriod(ui.period + 1)
	case trophyActSort:
		playtimePeriodsSetConsoles(!ui.consoles)
	case trophyActUp:
		if ui.scroll <= 0 {
			return false
		}
		playtimePeriodsScrollTo(ui.scroll - 1)
	case trophyActDown:
		playtimePeriodsScrollTo(ui.scroll + 1)
	case trophyActPageUp:
		playtimePeriodsScrollTo(ui.scroll - page)
	case trophyActPageDown:
		playtimePeriodsScrollTo(ui.scroll + page)
	case trophyActFirst:
		playtimePeriodsScrollTo(0)
	case trophyActLast:
		playtimePeriodsScrollTo(playtimePeriodsMaxScroll())
	}
	return true
}

// playtimePeriodsClick handles a click or a tap on the period pills and the
// Games / Consoles pair. It reports whether one was hit.
func playtimePeriodsClick(mx, my int32) bool {
	ui := &playtimePeriodsUI
	for i, rect := range ui.periodRects {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			playtimePeriodsSetPeriod(i)
			return true
		}
	}
	for i, rect := range ui.modeRects {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			playtimePeriodsSetConsoles(i == 1)
			return true
		}
	}
	return false
}

// playtimePeriodsWheel scrolls the list a row per notch. Up (a positive
// delta) scrolls back.
func playtimePeriodsWheel(y int32) bool {
	if y == 0 {
		return false
	}
	ui := &playtimePeriodsUI
	if y > 0 {
		ui.scroll--
	} else {
		ui.scroll++
	}
	playtimePeriodsClampScroll()
	return true
}

// playtimePeriodsFooterHints are the footer's hints on the tab, in the pad's
// words; the hints layer names the keys for a keyboard.
func playtimePeriodsFooterHints() []FooterHint {
	ui := &playtimePeriodsUI
	mode := "Consoles"
	if ui.consoles {
		mode = "Games"
	}
	hints := []FooterHint{{Button: "Left/Right", Label: "Period"}, {Button: "X", Label: mode}}
	if ui.fit > 0 && len(ui.rows) > ui.fit {
		hints = append(hints, FooterHint{Button: "Up/Down", Label: "Scroll"})
	}
	hints = append(hints, FooterHint{Button: "L1/R1", Label: "Tabs"}, FooterHint{Button: "B", Label: "Back"})
	if retroAchievementsSupported() {
		hints = append(hints, FooterHint{Button: "Y", Label: "RetroAchievements"})
	}
	return hints
}
