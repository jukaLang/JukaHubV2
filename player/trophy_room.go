package main

// trophy_room.go — the Playtime scene: the trophy room and the playtime stats.
//
// The header is a level ring (trophy points, like a console's trophy level),
// the earned count with a completion bar and the per-tier counts. Under it two
// tabs, switched with L1/R1 (Tab on a keyboard): Trophies and Playtime.
//
// Trophies is a grid of cards — the trophy in its metal when earned, a dim
// silhouette with a progress bar when not, a "?" while a hidden one is still to
// be found — with a chip row above it: filters (All, Earned, Locked and the four
// tiers, applied as the focus moves along them), the sort order and the
// celebration style. A opens a card's details sheet: the trophy large, its tier
// and rarity, what it asks for, how far along it is or when it was earned, and
// Left/Right to step through the list.
//
// Playtime is the numbers the trophies are made of: this week's days as bars,
// the 13-week heatmap with streaks and the best day (wrapped.go), the most used
// screens and the games launched from JukaHub.
//
// The page has no config elements, so it owns its keys, its pad buttons, its
// clicks and the wheel. Up from the chip row, the top of the page, is handed
// back so the top bar can take it.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	trophyTabTrophies = 0
	trophyTabStats    = 1

	trophyZoneChips = 0
	trophyZoneGrid  = 1

	trophyFilterAll      = 0
	trophyFilterEarned   = 1
	trophyFilterLocked   = 2
	trophyFilterPlatinum = 3
	trophyFilterGold     = 4
	trophyFilterSilver   = 5
	trophyFilterBronze   = 6
	trophyFilterCount    = 7

	trophySortEarnedFirst = 0
	trophySortRecent      = 1
	trophySortTier        = 2
	trophySortClosest     = 3
	trophySortCount       = 4

	// The chip row: the filters, then the sort chip, then the style chip.
	trophyChipSort  = trophyFilterCount
	trophyChipStyle = trophyFilterCount + 1
	trophyChipCount = trophyFilterCount + 2

	// trophyCardsMax bounds the cards one frame can draw (and hit-test).
	trophyCardsMax = 64

	// trophyCardMinW is the narrowest a card may be: three columns on the wide
	// canvas, two on the Brick's.
	trophyCardMinW = int32(340)
)

var trophyFilterLabels = [trophyFilterCount]string{"All", "Earned", "Locked", "Platinum", "Gold", "Silver", "Bronze"}

var trophySortLabels = [trophySortCount]string{"Earned first", "Recent", "Tier", "Almost there"}

var trophyWeekdays = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// trophyFilterTier is the tier a tier filter shows.
func trophyFilterTier(filter int) (trophyTier, bool) {
	switch filter {
	case trophyFilterPlatinum:
		return trophyPlatinum, true
	case trophyFilterGold:
		return trophyGold, true
	case trophyFilterSilver:
		return trophySilver, true
	case trophyFilterBronze:
		return trophyBronze, true
	}
	return trophyBronze, false
}

// trophyFilterMatch reports whether a trophy belongs in a filter's list.
func trophyFilterMatch(filter int, a *playtimeAchievement, earned bool) bool {
	switch filter {
	case trophyFilterEarned:
		return earned
	case trophyFilterLocked:
		return !earned
	}
	if tier, ok := trophyFilterTier(filter); ok {
		return a.Tier == tier
	}
	return true
}

// trophyTierCaps is a tier's name in capitals, for the header's small labels
// (constants, so a frame does not build them).
func trophyTierCaps(t trophyTier) string {
	switch t {
	case trophySilver:
		return "SILVER"
	case trophyGold:
		return "GOLD"
	case trophyPlatinum:
		return "PLATINUM"
	}
	return "BRONZE"
}

// trophyStyleToast is what the style chip says when it changes.
func trophyStyleToast(mode string) string {
	switch mode {
	case "full":
		return "Confetti for every trophy, even with Reduced Motion"
	case "calm":
		return "Trophies arrive calmly, without confetti"
	}
	return "Confetti follows the Reduced Motion setting"
}

// trophyStyleChipLabel is the style chip's text for a style.
func trophyStyleChipLabel(mode string) string {
	switch mode {
	case "full":
		return "Confetti: Full"
	case "calm":
		return "Confetti: Calm"
	}
	return "Confetti: Auto"
}

// trophySceneLabel is a screen's name as the stats show it.
func trophySceneLabel(scene string) string {
	switch scene {
	case "SaveManager":
		return "Save Manager"
	case "Curation":
		return "Curation Hub"
	case "Ports":
		return "PortMaster"
	case "Emulator":
		return "Emulators"
	case "Home", "Main":
		return "Home"
	}
	return sceneDisplayName(scene)
}

// trophyProgressText is a locked trophy's progress in its own unit.
func trophyProgressText(a *playtimeAchievement, cur, goal int64) string {
	if cur > goal {
		cur = goal
	}
	if cur < 0 {
		cur = 0
	}
	switch a.Metric {
	case trophyMetricTotal, trophyMetricBestDay, trophyMetricWeek, trophyMetricScene:
		return formatPlaytime(cur) + " of " + formatPlaytime(goal)
	case trophyMetricDays:
		return fmt.Sprintf("%d of %d days", cur, goal)
	case trophyMetricStreak:
		return fmt.Sprintf("%d of %d days in a row", cur, goal)
	case trophyMetricScreens:
		return fmt.Sprintf("%d of %d screens", cur, goal)
	}
	return fmt.Sprintf("%d of %d trophies", cur, goal)
}

// trophyRow is one trophy's display data, formatted when the list is rebuilt
// so a frame only draws strings it already has.
type trophyRow struct {
	earned   bool
	stamp    time.Time
	frac     float64 // progress, 0..1
	pct      string  // "64%"
	progress string  // "3h 12m of 5h"
	when     string  // "Earned 2 Mar 2026"
	whenLong string  // "Earned Monday, 2 March 2026 at 20:11"
}

// trophyStatsView is the Playtime tab's numbers, formatted with the list.
type trophyStatsView struct {
	week       [7]int64 // this week, Monday first
	weekFuture [7]bool
	weekVals   [7]string
	today      int
	weekMax    int64
	weekLabel  string
	scenes     []playtimeSceneTotal
	sceneVals  []string
	games      []playtimeSceneTotal
	gameVals   []string
	ins        wrappedInsights
	insLabels  [5]string
	insValues  [5]string
	totals     [6][2]string
}

// trophyRoomState is the room's state and the last frame's geometry.
type trophyRoomState struct {
	tab    int
	zone   int
	chip   int
	filter int
	sort   int
	cursor int // position in list

	sheet      bool
	sheetIdx   int // index into playtimeAchievements
	sheetEnter uiEnter

	// The list on screen and what it was built from.
	list      []int
	rows      []trophyRow
	built     bool
	builtRev  uint64
	builtGen  uint64
	builtFilt int
	builtSort int
	builtDay  int

	// Header numbers, formatted once per rebuild.
	shown      int
	earned     int
	tierEarned [trophyTierCount]int
	points     int
	level      int
	levelFrac  float64
	levelText  string
	heroSub    string
	heroTime   string
	tierCounts [trophyTierCount]string
	stats      trophyStatsView

	// The last frame's geometry, for the input handlers and the tests.
	cols       int
	cardH      int32
	gap        int32
	grid       sdl.Rect
	scrollF    float64
	chipRects  [trophyChipCount]sdl.Rect
	chipScroll int32
	tabRects   [2]sdl.Rect
	raRect     sdl.Rect
	cardRects  [trophyCardsMax]sdl.Rect
	cardPos    [trophyCardsMax]int
	cardN      int
	sheetRect  sdl.Rect
	prevRect   sdl.Rect
	nextRect   sdl.Rect
	replayRect sdl.Rect
	heatCard   sdl.Rect
	barsRect   sdl.Rect
	debugTier  int
}

var trophyRoom trophyRoomState

var (
	// playtimeScroll is the trophy grid's scroll offset in pixels: where the
	// view is going (it eases there when motion is on). Clamped every frame.
	playtimeScroll int32
	// playtimeScrollFor is the scene index the room's state belongs to.
	// Entering the screen again starts fresh instead of where the last visit
	// stopped.
	playtimeScrollFor = -2
	// playtimeHeatRect is where the Playtime tab drew the 13-week grid this
	// frame, so a probe can assert against the real geometry.
	playtimeHeatRect sdl.Rect
)

// trophyRoomSyncVisit starts a fresh visit when the scene was left and entered
// again: the Trophies tab, the focus on the active filter, the grid at the top
// and no sheet. Filter and sort are kept for the session.
func trophyRoomSyncVisit() {
	if playtimeScrollFor == currentSceneIndex {
		return
	}
	playtimeScrollFor = currentSceneIndex
	playtimeScroll = 0
	r := &trophyRoom
	r.scrollF = 0
	r.tab = trophyTabTrophies
	r.zone = trophyZoneChips
	r.chip = r.filter
	r.cursor = 0
	r.sheet = false
	r.chipScroll = 0
}

// ── Model ─────────────────────────────────────────────────────────────────────

// trophyRoomRefresh rebuilds the list and every formatted number when the data,
// the availability, the filter, the sort or the day changed. Inside the room
// time accrues every second, so this runs about once a second, never per frame.
func trophyRoomRefresh(config *Config) {
	r := &trophyRoom
	now := time.Now()
	day := now.Year()*400 + now.YearDay()
	if r.built && r.builtRev == playtime.rev && r.builtGen == trophyAvail.gen &&
		r.builtFilt == r.filter && r.builtSort == r.sort && r.builtDay == day {
		return
	}
	r.built = true
	r.builtRev, r.builtGen, r.builtFilt, r.builtSort, r.builtDay = playtime.rev, trophyAvail.gen, r.filter, r.sort, day

	d := playtime.data
	st := trophyStatsOf(d, now)
	if len(r.rows) != len(playtimeAchievements) {
		r.rows = make([]trophyRow, len(playtimeAchievements))
	}
	r.list = r.list[:0]
	r.shown, r.earned, r.points = 0, 0, 0
	r.tierEarned = [trophyTierCount]int{}
	tierShown := [trophyTierCount]int{}
	for i := range playtimeAchievements {
		a := &playtimeAchievements[i]
		row := &r.rows[i]
		stamp, earned := d.Unlocked[a.ID]
		*row = trophyRow{earned: earned}
		if earned {
			row.frac = 1
			row.pct = "100%"
			row.when, row.whenLong = "Earned", "Earned"
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(stamp)); err == nil {
				row.stamp = t
				row.when = "Earned " + t.Format("2 Jan 2006")
				row.whenLong = "Earned " + t.Format("Monday, 2 January 2006") + " at " + clockDigits(config, t)
			}
		} else {
			cur, goal := trophyProgressWith(a, d, &st)
			if goal > 0 {
				row.frac = float64(cur) / float64(goal)
			}
			if row.frac > 1 {
				row.frac = 1
			}
			if row.frac < 0 {
				row.frac = 0
			}
			// Floored: a trophy one second short must not claim 100%.
			row.pct = strconv.Itoa(int(row.frac*100)) + "%"
			row.progress = trophyProgressText(a, cur, goal)
		}
		if !trophyShown(i, d) {
			continue
		}
		r.shown++
		if int(a.Tier) < trophyTierCount {
			tierShown[a.Tier]++
		}
		if earned {
			r.earned++
			r.points += trophyTierPoints(a.Tier)
			if int(a.Tier) < trophyTierCount {
				r.tierEarned[a.Tier]++
			}
		}
		if trophyFilterMatch(r.filter, a, earned) {
			r.list = append(r.list, i)
		}
	}
	trophyRoomSortList(r.list, r.rows, r.sort)

	r.level, r.levelFrac = trophyLevel(r.points)
	r.levelText = strconv.Itoa(r.level)
	pct := 0
	if r.shown > 0 {
		pct = r.earned * 100 / r.shown
	}
	r.heroSub = fmt.Sprintf("%d of %d earned · %d%% · %d pts", r.earned, r.shown, pct, r.points)
	r.heroTime = formatPlaytime(d.Total) + " in total · " + formatPlaytime(st.week) + " in the last 7 days"
	for t := 0; t < trophyTierCount; t++ {
		r.tierCounts[t] = strconv.Itoa(r.tierEarned[t]) + "/" + strconv.Itoa(tierShown[t])
	}
	if r.cursor >= len(r.list) {
		r.cursor = len(r.list) - 1
	}
	if r.cursor < 0 {
		r.cursor = 0
	}
	if len(r.list) == 0 && r.zone == trophyZoneGrid {
		r.zone = trophyZoneChips
		r.chip = r.filter
	}
	trophyRoomBuildStats(d, now)
}

// trophyRoomSortList orders the list for a sort mode. Every mode is stable on
// the definition order, so equal trophies never trade places between frames.
func trophyRoomSortList(list []int, rows []trophyRow, mode int) {
	earned := func(i int) bool { return i >= 0 && i < len(rows) && rows[i].earned }
	switch mode {
	case trophySortRecent:
		sort.SliceStable(list, func(x, y int) bool {
			a, b := list[x], list[y]
			ea, eb := earned(a), earned(b)
			if ea != eb {
				return ea
			}
			if ea {
				return rows[a].stamp.After(rows[b].stamp)
			}
			return a < b
		})
	case trophySortTier:
		sort.SliceStable(list, func(x, y int) bool {
			a, b := list[x], list[y]
			ta, tb := playtimeAchievements[a].Tier, playtimeAchievements[b].Tier
			if ta != tb {
				return ta > tb
			}
			ea, eb := earned(a), earned(b)
			if ea != eb {
				return ea
			}
			return a < b
		})
	case trophySortClosest:
		sort.SliceStable(list, func(x, y int) bool {
			a, b := list[x], list[y]
			ea, eb := earned(a), earned(b)
			if ea != eb {
				return eb // locked first: the ones still to get
			}
			if !ea {
				// A hidden trophy gives nothing away, so it sorts after the rest.
				ha, hb := playtimeAchievements[a].Hidden, playtimeAchievements[b].Hidden
				if ha != hb {
					return hb
				}
				if rows[a].frac != rows[b].frac {
					return rows[a].frac > rows[b].frac
				}
			}
			return a < b
		})
	default:
		sort.SliceStable(list, func(x, y int) bool {
			ea, eb := earned(list[x]), earned(list[y])
			if ea != eb {
				return ea
			}
			return list[x] < list[y]
		})
	}
}

// trophyRoomBuildStats formats the Playtime tab's numbers.
func trophyRoomBuildStats(d playtimeData, now time.Time) {
	s := &trophyRoom.stats
	weekday := (int(now.Weekday()) + 6) % 7 // Monday = 0
	monday := now.AddDate(0, 0, -weekday)
	s.today = weekday
	s.weekMax = 0
	var total int64
	for i := 0; i < 7; i++ {
		secs := d.Days[monday.AddDate(0, 0, i).Format(wrappedDayKey)]
		s.week[i] = secs
		s.weekFuture[i] = i > weekday
		s.weekVals[i] = ""
		if secs > 0 {
			s.weekVals[i] = formatPlaytime(secs)
		}
		if secs > s.weekMax {
			s.weekMax = secs
		}
		total += secs
	}
	s.weekLabel = formatPlaytime(total)

	allScenes := sortedPlaytimeScenes(d)
	s.scenes = allScenes
	if len(s.scenes) > 8 {
		s.scenes = s.scenes[:8]
	}
	s.sceneVals = s.sceneVals[:0]
	for _, row := range s.scenes {
		s.sceneVals = append(s.sceneVals, formatPlaytime(row.Seconds))
	}
	s.games = sortedPlaytimeGames(d)
	if len(s.games) > 8 {
		s.games = s.games[:8]
	}
	s.gameVals = s.gameVals[:0]
	for _, row := range s.games {
		s.gameVals = append(s.gameVals, formatPlaytime(row.Seconds))
	}

	s.ins = buildWrappedInsights(d, now)
	s.insLabels = [5]string{"STREAK", "LONGEST", "BEST DAY", "DAILY AVERAGE", "ACTIVE DAYS"}
	s.insValues = [5]string{
		wrappedDaysLabel(s.ins.CurrentStreak),
		wrappedDaysLabel(s.ins.LongestStreak),
		wrappedBestDayLabel(s.ins),
		formatPlaytime(s.ins.DailyAverage),
		fmt.Sprintf("%d of %d", s.ins.ActiveDays, wrappedDays),
	}
	r := &trophyRoom
	s.totals = [6][2]string{
		{"TOTAL TIME", formatPlaytime(d.Total)},
		{"DAYS USED", strconv.Itoa(daysUsed(d))},
		{"SCREENS", strconv.Itoa(len(allScenes))},
		{"TROPHIES", strconv.Itoa(r.earned) + " of " + strconv.Itoa(r.shown)},
		{"TROPHY POINTS", strconv.Itoa(r.points)},
		{"TROPHY LEVEL", r.levelText},
	}
}

// ── Layout ────────────────────────────────────────────────────────────────────

// trophyFonts are the room's text sizes for this frame.
type trophyFonts struct {
	title, sub, small, level, caption *ttf.Font
	count, tab, chip, section, value  *ttf.Font
	cardTitle, cardDesc, cardMeta     *ttf.Font
	sheetTitle, big                   *ttf.Font
}

// trophyRoomFonts opens the room's fonts through the shared cache.
func trophyRoomFonts(config *Config) trophyFonts {
	get := func(size int) *ttf.Font { return getFontSized(config, "small", size) }
	return trophyFonts{
		title: get(24), sub: get(16), small: get(14), level: get(30), caption: get(11),
		count: get(21), tab: get(16), chip: get(15), section: get(13), value: get(20),
		cardTitle: get(18), cardDesc: get(14), cardMeta: get(13),
		sheetTitle: get(26), big: get(40),
	}
}

// trophyFontH is a font's line height, or fallback when it failed to open.
func trophyFontH(f *ttf.Font, fallback int32) int32 {
	if f == nil {
		return fallback
	}
	return int32(f.Height())
}

// trophyRoomLayout is the page's geometry on this canvas.
type trophyRoomLayout struct {
	body    sdl.Rect // between the bars
	hero    sdl.Rect
	tabsY   int32
	tabsH   int32
	content sdl.Rect // under the tab row
	cols    int
	cardW   int32
	cardH   int32
	gap     int32
}

// trophyRoomLayoutFor lays the page out for the current canvas and fonts.
func trophyRoomLayoutFor(f trophyFonts) trophyRoomLayout {
	margin := scaleX(homeSideMargin, screenWidth)
	if margin < 12 {
		margin = 12
	}
	barH := headerBarHeight()
	footY := sceneFooterBand().Y
	short := screenHeight < 600
	var l trophyRoomLayout
	l.body = sdl.Rect{X: 0, Y: barH, W: screenWidth, H: footY - barH}
	heroH := int32(112)
	if short {
		heroH = 80
	}
	l.hero = sdl.Rect{X: margin, Y: barH + 8, W: screenWidth - 2*margin, H: heroH}
	l.tabsH = 36
	if short {
		l.tabsH = 30
	}
	l.tabsY = l.hero.Y + l.hero.H + 12
	top := l.tabsY + l.tabsH + 12
	l.content = sdl.Rect{X: margin, Y: top, W: screenWidth - 2*margin, H: footY - 10 - top}
	if l.content.H < 60 {
		l.content.H = 60
	}
	l.gap = 14
	l.cols = UIGridColumns(l.content.W, trophyCardMinW, l.gap)
	if l.cols > 4 {
		l.cols = 4
	}
	l.cardW = UIGridCellWidth(l.content.W, l.cols, l.gap)
	h := 2*14 + trophyFontH(f.cardTitle, 22) + 4 + trophyFontH(f.cardDesc, 18) + 10 + trophyFontH(f.cardMeta, 16)
	if h < 92 {
		h = 92
	}
	l.cardH = h
	return l
}

// trophyRoomMetrics is the grid's geometry for the input handlers: the last
// frame's, or a font-less estimate before the first frame.
func trophyRoomMetrics() (cols int, cardH, gap, viewH int32) {
	r := &trophyRoom
	if r.cols > 0 && r.cardH > 0 && r.grid.H > 0 {
		return r.cols, r.cardH, r.gap, r.grid.H
	}
	margin := scaleX(homeSideMargin, screenWidth)
	cols = UIGridColumns(screenWidth-2*margin, trophyCardMinW, 14)
	if cols > 4 {
		cols = 4
	}
	viewH = screenHeight - headerBarHeight() - 260
	if viewH < 100 {
		viewH = 100
	}
	return cols, 96, 14, viewH
}

// trophyRoomMaxScroll is how far the grid can scroll.
func trophyRoomMaxScroll() int32 {
	cols, cardH, gap, viewH := trophyRoomMetrics()
	n := len(trophyRoom.list)
	if n == 0 || cols <= 0 {
		return 0
	}
	rows := int32((n + cols - 1) / cols)
	content := rows*(cardH+gap) - gap + 6
	if content <= viewH {
		return 0
	}
	return content - viewH
}

// trophyRoomClampScroll keeps the scroll inside the content.
func trophyRoomClampScroll() {
	if limit := trophyRoomMaxScroll(); playtimeScroll > limit {
		playtimeScroll = limit
	}
	if playtimeScroll < 0 {
		playtimeScroll = 0
	}
}

// trophyRoomReveal scrolls so the focused card is fully on screen.
func trophyRoomReveal() {
	cols, cardH, gap, viewH := trophyRoomMetrics()
	if cols <= 0 {
		return
	}
	row := int32(trophyRoom.cursor / cols)
	top := row*(cardH+gap) - 4
	bottom := row*(cardH+gap) + cardH + 6
	if top < playtimeScroll {
		playtimeScroll = top
	}
	if bottom > playtimeScroll+viewH {
		playtimeScroll = bottom - viewH
	}
	trophyRoomClampScroll()
}

// ── Render ────────────────────────────────────────────────────────────────────

// renderPlaytime draws the room. Every number is formatted, never raw seconds,
// and every string is measured to the space it gets.
func renderPlaytime(renderer *sdl.Renderer, config *Config) {
	trophyRoomSyncVisit()
	playtimeHeatRect = sdl.Rect{}
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	if renderer == nil || config == nil {
		return
	}
	trophySyncAvailability(config)
	trophyRoomRefresh(config)

	f := trophyRoomFonts(config)
	if f.sub == nil {
		return
	}
	l := trophyRoomLayoutFor(f)
	r := &trophyRoom
	r.cols, r.cardH, r.gap = l.cols, l.cardH, l.gap
	r.grid = l.content
	trophyRoomClampScroll()

	trophyRoomDrawHero(renderer, config, f, l)
	trophyRoomDrawTabs(renderer, config, f, l)
	r.cardN = 0
	if r.tab == trophyTabStats {
		trophyRoomDrawStats(renderer, config, f, l)
	} else {
		trophyRoomDrawGrid(renderer, config, f, l)
	}

	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if r.sheet {
		trophyRoomDrawSheet(renderer, config, f, l)
	} else {
		r.sheetRect = sdl.Rect{}
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// trophyRoomFocusVisible reports whether the room's own focus ring is shown:
// not while a top-bar button holds the focus, so there is only ever one ring.
func trophyRoomFocusVisible() bool {
	return chromeTier(focusEngine.ElementIndex()) == 0
}

// trophyRoomDrawHero draws the header card: level ring, counts, tier totals.
func trophyRoomDrawHero(renderer *sdl.Renderer, config *Config, f trophyFonts, l trophyRoomLayout) {
	r := &trophyRoom
	hero := l.hero
	drawGlassSurface(renderer, hero, homeTileRadius, glassState{})

	accent := glassAccent()
	ringR := hero.H/2 - 14
	if ringR < 20 {
		ringR = 20
	}
	cx := hero.X + 22 + ringR
	cy := hero.Y + hero.H/2
	homeGlowBlob(renderer, sdl.Rect{X: cx - ringR*2, Y: cy - ringR*2, W: ringR * 4, H: ringR * 4}, accent, 60)
	ringW := ringR / 6
	if ringW < 4 {
		ringW = 4
	}
	trophyDrawRing(renderer, cx, cy, ringR, ringW, 1, glassA(40))
	trophyDrawRing(renderer, cx, cy, ringR, ringW, r.levelFrac, accent)
	if f.level != nil {
		lw, lh := measureText(f.level, r.levelText)
		capH := trophyFontH(f.caption, 13)
		top := cy - (lh+capH)/2
		renderText(renderer, config, f.level, r.levelText, ColorTextPrimary(), cx-lw/2, top)
		if f.caption != nil {
			cw, _ := measureText(f.caption, "LEVEL")
			renderText(renderer, config, f.caption, "LEVEL", ColorTextTertiary(), cx-cw/2, top+lh)
		}
	}

	// The tier totals sit at the right edge: Platinum first, the rarest. Each is
	// a small trophy and its "earned/available" count, with the tier's name
	// under the count when there is room for it.
	type tierBlock struct {
		tier trophyTier
		w    int32
	}
	blocks := [trophyTierCount]tierBlock{{tier: trophyPlatinum}, {tier: trophyGold}, {tier: trophySilver}, {tier: trophyBronze}}
	glyph := int32(34)
	if hero.H < 100 {
		glyph = 28
	}
	textX := cx + ringR + 22
	minText := int32(240)
	withNames := true
	measure := func(names bool) int32 {
		total := int32(0)
		for i := range blocks {
			b := &blocks[i]
			w, _ := measureText(f.count, r.tierCounts[b.tier])
			if names && f.caption != nil {
				if nw, _ := measureText(f.caption, trophyTierCaps(b.tier)); nw > w {
					w = nw
				}
			}
			b.w = glyph + 8 + w
			total += b.w
		}
		return total + int32(len(blocks)-1)*18
	}
	total := measure(true)
	if hero.X+hero.W-22-total < textX+minText {
		withNames = false
		total = measure(false)
	}
	showTiers := hero.X+hero.W-22-total >= textX+160
	tiersX := hero.X + hero.W - 22 - total
	textRight := hero.X + hero.W - 22
	if showTiers {
		textRight = tiersX - 24
		x := tiersX
		for _, b := range blocks {
			earned := r.tierEarned[b.tier] > 0
			trophyDrawTrophy(renderer, sdl.Rect{X: x, Y: cy - glyph/2, W: glyph, H: glyph}, b.tier, earned, false)
			tx := x + glyph + 8
			countH := trophyFontH(f.count, 24)
			nameH := int32(0)
			if withNames {
				nameH = trophyFontH(f.caption, 13)
			}
			ty := cy - (countH+nameH)/2
			renderText(renderer, config, f.count, r.tierCounts[b.tier], ColorTextPrimary(), tx, ty)
			if withNames && f.caption != nil {
				renderText(renderer, config, f.caption, trophyTierCaps(b.tier), trophyTierInk(b.tier), tx, ty+countH)
			}
			x += b.w + 18
		}
	}

	// The title block between the ring and the tier totals.
	avail := textRight - textX
	if avail < 60 {
		return
	}
	titleH := trophyFontH(f.title, 29)
	subH := trophyFontH(f.sub, 20)
	smallH := trophyFontH(f.small, 17)
	blockH := titleH + 2 + subH + 8 + 6 + 8 + smallH
	y := cy - blockH/2
	if f.title != nil {
		renderText(renderer, config, f.title, ellipsize(f.title, "Trophies", avail), ColorTextPrimary(), textX, y)
	}
	y += titleH + 2
	renderText(renderer, config, f.sub, ellipsize(f.sub, r.heroSub, avail), ColorTextSecondary(), textX, y)
	y += subH + 8
	barW := min32(avail, 360)
	frac := 0.0
	if r.shown > 0 {
		frac = float64(r.earned) / float64(r.shown)
	}
	UIProgressBar(renderer, sdl.Rect{X: textX, Y: y, W: barW, H: 6}, frac, glassA(26), accent)
	y += 6 + 8
	if f.small != nil {
		renderText(renderer, config, f.small, ellipsize(f.small, r.heroTime, avail), ColorTextTertiary(), textX, y)
	}
}

// trophyRoomPill draws one glass pill with a centred label: active fills it
// with the accent, focused rings it.
func trophyRoomPill(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, label string, active, focused bool) {
	radius := rect.H / 2
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	accent := glassAccent()
	ink := ColorTextSecondary()
	if active {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, accent)
		ink = contrastText(accent)
	} else {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassChipFill())
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, glassChipEdge())
	}
	if focused {
		drawGlassFocusRing(renderer, glassLifted(rect), radius+glassFocusLift)
	}
	if font != nil && label != "" {
		tw, th := measureText(font, label)
		renderText(renderer, config, font, label, ink, rect.X+(rect.W-tw)/2, rect.Y+(rect.H-th)/2)
	}
}

// trophyRoomBadge draws a controller-button badge ("L1") at x, centred on cy,
// and returns its width.
func trophyRoomBadge(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, cy int32, label string) int32 {
	if font == nil {
		return 0
	}
	tw, th := measureText(font, label)
	h := th + 6
	w := tw + 12
	if w < h {
		w = h
	}
	y := cy - h/2
	fillRoundedRect(renderer, x, y, w, h, h/2, glassChipFill())
	strokeRoundedRect(renderer, x, y, w, h, h/2, 1, glassChipEdge())
	renderText(renderer, config, font, label, glassChipInk(), x+(w-tw)/2, y+(h-th)/2)
	return w
}

// trophyRoomChipWidth is chip i's width at the chip font.
func trophyRoomChipWidth(config *Config, f trophyFonts, i int) int32 {
	label := trophyRoomChipLabel(config, i)
	tw, _ := measureText(f.chip, label)
	w := tw + 2*14
	if trophyRoomChipHasGlyph(i) {
		w += 16 + 6
	}
	return w
}

// trophyRoomChipLabel is chip i's text.
func trophyRoomChipLabel(config *Config, i int) string {
	switch {
	case i < trophyFilterCount:
		return trophyFilterLabels[i]
	case i == trophyChipSort:
		return trophySortLabels[trophyRoom.sort]
	}
	return trophyStyleChipLabel(trophyCelebrationMode(config))
}

// trophyRoomChipHasGlyph reports whether chip i leads with a glyph.
func trophyRoomChipHasGlyph(i int) bool {
	if _, ok := trophyFilterTier(i); ok && i < trophyFilterCount {
		return true
	}
	return i == trophyChipSort || i == trophyChipStyle
}

// trophyRoomDrawTabs draws the tab pills and, on the Trophies tab, the chip
// row; on the Playtime tab the RetroAchievements button takes its place.
func trophyRoomDrawTabs(renderer *sdl.Renderer, config *Config, f trophyFonts, l trophyRoomLayout) {
	r := &trophyRoom
	cy := l.tabsY + l.tabsH/2
	x := l.content.X
	x += trophyRoomBadge(renderer, config, f.caption, x, cy, "L1") + 8
	for t, label := range [2]string{"Trophies", "Playtime"} {
		tw, _ := measureText(f.tab, label)
		rect := sdl.Rect{X: x, Y: l.tabsY, W: tw + 36, H: l.tabsH}
		trophyRoomPill(renderer, config, f.tab, rect, label, r.tab == t, false)
		r.tabRects[t] = rect
		x += rect.W + 8
	}
	x += trophyRoomBadge(renderer, config, f.caption, x, cy, "R1") + 20

	for i := range r.chipRects {
		r.chipRects[i] = sdl.Rect{}
	}
	r.raRect = sdl.Rect{}
	right := l.content.X + l.content.W
	if r.tab == trophyTabStats {
		if retroAchievementsSupported() && f.chip != nil {
			label := "RetroAchievements"
			tw, _ := measureText(f.chip, label)
			rect := sdl.Rect{X: right - (tw + 2*16), Y: l.tabsY, W: tw + 2*16, H: l.tabsH}
			if rect.X > x {
				trophyRoomPill(renderer, config, f.chip, rect, label, false, false)
				r.raRect = rect
			}
		}
		return
	}
	if f.chip == nil {
		return
	}

	// The chip row. It is a carousel when it does not fit: the focused chip is
	// always scrolled into view and the row is clipped to its own band.
	area := sdl.Rect{X: x, Y: l.tabsY - 6, W: right - x, H: l.tabsH + 12}
	if area.W < 80 {
		return
	}
	const gap = int32(8)
	var widths [trophyChipCount]int32
	total := int32(0)
	for i := 0; i < trophyChipCount; i++ {
		widths[i] = trophyRoomChipWidth(config, f, i)
		total += widths[i]
		if i > 0 {
			total += gap
		}
	}
	// A little room either side for the focus lift.
	inner := area.W - 8
	if total <= inner {
		r.chipScroll = 0
	} else {
		start := int32(0)
		for i := 0; i < r.chip && i < trophyChipCount; i++ {
			start += widths[i] + gap
		}
		end := start + widths[r.chip]
		if start-r.chipScroll < 0 {
			r.chipScroll = start
		}
		if end-r.chipScroll > inner {
			r.chipScroll = end - inner
		}
		if r.chipScroll > total-inner {
			r.chipScroll = total - inner
		}
		if r.chipScroll < 0 {
			r.chipScroll = 0
		}
	}
	focusOn := r.zone == trophyZoneChips && !r.sheet && trophyRoomFocusVisible()
	renderWithClip(renderer, area.X, area.Y, area.W, area.H, func(_ *sdl.Renderer) {
		cx := area.X + 4 - r.chipScroll
		for i := 0; i < trophyChipCount; i++ {
			rect := sdl.Rect{X: cx, Y: l.tabsY + 2, W: widths[i], H: l.tabsH - 4}
			cx += widths[i] + gap
			if rect.X+rect.W < area.X || rect.X > area.X+area.W {
				continue
			}
			visible := glassIntersect(rect, area)
			r.chipRects[i] = visible
			active := i < trophyFilterCount && i == r.filter
			focused := focusOn && i == r.chip
			trophyRoomDrawChip(renderer, config, f, rect, i, active, focused)
		}
	})
}

// trophyRoomDrawChip draws one chip of the row.
func trophyRoomDrawChip(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, i int, active, focused bool) {
	radius := rect.H / 2
	accent := glassAccent()
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	ink := ColorTextSecondary()
	if active {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, WithAlpha(accent, 58))
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, WithAlpha(accent, 200))
		ink = ColorTextPrimary()
	} else {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassChipFill())
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, glassChipEdge())
	}
	if focused {
		drawGlassFocusRing(renderer, glassLifted(rect), radius+glassFocusLift)
	}
	label := trophyRoomChipLabel(config, i)
	_, th := measureText(f.chip, label)
	x := rect.X + 14
	if trophyRoomChipHasGlyph(i) {
		box := sdl.Rect{X: x, Y: rect.Y + (rect.H-16)/2, W: 16, H: 16}
		if tier, ok := trophyFilterTier(i); ok && i < trophyFilterCount {
			trophyDrawCupFlat(renderer, box, trophyTierColor(tier))
		} else if i == trophyChipSort {
			DrawHomeIcon(renderer, "sort", box, false)
		} else {
			DrawHomeIcon(renderer, "sparkles", box, false)
		}
		x += 16 + 6
	}
	renderText(renderer, config, f.chip, label, ink, x, rect.Y+(rect.H-th)/2)
}

// trophyRoomDrawGrid draws the trophy cards that are on screen.
func trophyRoomDrawGrid(renderer *sdl.Renderer, config *Config, f trophyFonts, l trophyRoomLayout) {
	r := &trophyRoom
	grid := l.content
	if len(r.list) == 0 {
		title, body := "No trophies here yet", "Keep exploring — the first one is minutes away."
		switch r.filter {
		case trophyFilterLocked:
			title, body = "Nothing left to earn", "Every trophy on this device is yours."
		case trophyFilterPlatinum, trophyFilterGold, trophyFilterSilver, trophyFilterBronze:
			body = "None of this tier yet — keep going."
		}
		UIEmptyState(renderer, config, grid, "@trophy", title, body)
		return
	}

	// Ease the view toward the target scroll a row at a time; a jump of more
	// than a screen (End, Home, a page) and reduced motion snap instead.
	target := float64(playtimeScroll)
	if uiMotionEnabled(config) && math.Abs(target-r.scrollF) > 0.5 && math.Abs(target-r.scrollF) <= float64(grid.H) {
		r.scrollF += (target - r.scrollF) * 0.35
	} else {
		r.scrollF = target
	}
	scroll := int32(math.Round(r.scrollF))

	clip := sdl.Rect{X: grid.X - 10, Y: grid.Y - 6, W: grid.W + 20, H: grid.H + 6}
	focusOn := r.zone == trophyZoneGrid && !r.sheet && trophyRoomFocusVisible()
	renderWithClip(renderer, clip.X, clip.Y, clip.W, clip.H, func(_ *sdl.Renderer) {
		for pos, idx := range r.list {
			col := int32(pos % l.cols)
			row := int32(pos / l.cols)
			rect := sdl.Rect{
				X: grid.X + col*(l.cardW+l.gap),
				Y: grid.Y + row*(l.cardH+l.gap) - scroll,
				W: l.cardW,
				H: l.cardH,
			}
			if rect.Y+rect.H < clip.Y || rect.Y > clip.Y+clip.H {
				continue
			}
			if r.cardN < trophyCardsMax {
				r.cardRects[r.cardN] = glassIntersect(rect, grid)
				r.cardPos[r.cardN] = pos
				r.cardN++
			}
			trophyRoomDrawCard(renderer, config, f, rect, idx, focusOn && pos == r.cursor)
		}
	})

	// The scrollbar, when the grid is taller than its viewport.
	if limit := trophyRoomMaxScroll(); limit > 0 {
		barX := grid.X + grid.W + 6
		if barX > screenWidth-6 {
			barX = screenWidth - 6
		}
		fillRoundedRect(renderer, barX, grid.Y, 4, grid.H, 2, glassA(22))
		thumb := grid.H * grid.H / (grid.H + limit)
		if thumb < 28 {
			thumb = 28
		}
		at := scroll
		if at > limit {
			at = limit
		}
		if at < 0 {
			at = 0
		}
		y := grid.Y + int32(int64(grid.H-thumb)*int64(at)/int64(limit))
		fillRoundedRect(renderer, barX, y, 4, thumb, 2, glassAccent())
	}
}

// trophyRoomDrawCard draws one trophy card.
func trophyRoomDrawCard(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, idx int, focused bool) {
	if idx < 0 || idx >= len(playtimeAchievements) || idx >= len(trophyRoom.rows) {
		return
	}
	a := &playtimeAchievements[idx]
	row := &trophyRoom.rows[idx]
	secret := a.Hidden && !row.earned
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	drawGlassSurface(renderer, rect, glassRowRadius+2, glassState{Focused: focused})
	surface := rect
	if focused {
		surface = glassLifted(rect)
	}

	medal := int32(56)
	if rect.H < 96 {
		medal = 50
	}
	mbox := sdl.Rect{X: surface.X + 14, Y: surface.Y + (surface.H-medal)/2, W: medal, H: medal}
	trophyDrawTrophy(renderer, mbox, a.Tier, row.earned, secret)
	if secret && f.title != nil {
		qw, qh := measureText(f.title, "?")
		renderText(renderer, config, f.title, "?", ColorTextTertiary(), mbox.X+(mbox.W-qw)/2, mbox.Y+(mbox.H-qh)/2)
	}
	if !row.earned && !secret {
		// A small lock on the medallion's shoulder.
		lr := int32(10)
		lx := mbox.X + mbox.W - lr
		ly := mbox.Y + mbox.H - lr
		fillCircle(renderer, lx, ly, lr, glassSheetFill())
		DrawHomeIcon(renderer, "lock", sdl.Rect{X: lx - 7, Y: ly - 7, W: 14, H: 14}, false)
	}

	textX := mbox.X + mbox.W + 14
	textW := surface.X + surface.W - 14 - textX
	if textW < 40 {
		return
	}
	title := a.Title
	desc := a.Desc
	if secret {
		title = "Hidden trophy"
		desc = "Keep exploring to reveal it"
	}
	titleInk := ColorTextPrimary()
	if !row.earned {
		titleInk = ColorTextSecondary()
	}
	titleH := trophyFontH(f.cardTitle, 22)
	descH := trophyFontH(f.cardDesc, 18)
	metaH := trophyFontH(f.cardMeta, 16)
	blockH := titleH + 4 + descH + 10 + metaH
	y := surface.Y + (surface.H-blockH)/2

	// The tier, right-aligned on the title row.
	tierLabel := trophyTierName(a.Tier)
	tierW := int32(0)
	if f.cardMeta != nil {
		tierW, _ = measureText(f.cardMeta, tierLabel)
		renderText(renderer, config, f.cardMeta, tierLabel, trophyTierInk(a.Tier), textX+textW-tierW, y+(titleH-metaH)/2)
	}
	if f.cardTitle != nil {
		renderText(renderer, config, f.cardTitle, ellipsize(f.cardTitle, title, textW-tierW-10), titleInk, textX, y)
	}
	y += titleH + 4
	if f.cardDesc != nil {
		renderText(renderer, config, f.cardDesc, ellipsize(f.cardDesc, desc, textW), ColorTextTertiary(), textX, y)
	}
	y += descH + 10
	switch {
	case row.earned:
		if f.cardMeta != nil {
			renderText(renderer, config, f.cardMeta, ellipsize(f.cardMeta, row.when, textW), trophyTierInk(a.Tier), textX, y)
		}
	case secret:
		if f.cardMeta != nil {
			renderText(renderer, config, f.cardMeta, "???", ColorTextTertiary(), textX, y)
		}
	default:
		pw := int32(0)
		if f.cardMeta != nil {
			pw, _ = measureText(f.cardMeta, row.pct)
		}
		barW := textW - pw - 10
		if barW > 8 {
			fill := trophyTierColor(a.Tier)
			fill.A = 220
			UIProgressBar(renderer, sdl.Rect{X: textX, Y: y + metaH/2 - 3, W: barW, H: 6}, row.frac, glassA(22), fill)
		}
		if f.cardMeta != nil {
			renderText(renderer, config, f.cardMeta, row.pct, ColorTextTertiary(), textX+textW-pw, y)
		}
	}
}

// trophyRoomSectionCard draws a stats card with its caption and an optional
// read-out at the right, and returns the y its body starts at.
func trophyRoomSectionCard(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, caption, value string) int32 {
	drawCard(renderer, rect.X, rect.Y, rect.W, rect.H, homeTileRadius)
	if f.section == nil {
		return rect.Y + 40
	}
	renderText(renderer, config, f.section, caption, ColorTextTertiary(), rect.X+20, rect.Y+16)
	if value != "" {
		vw, _ := measureText(f.section, value)
		renderText(renderer, config, f.section, value, ColorTextSecondary(), rect.X+rect.W-20-vw, rect.Y+16)
	}
	return rect.Y + 16 + trophyFontH(f.section, 16) + 12
}

// trophyRoomDrawStats draws the Playtime tab: this week, the 13 weeks, the
// most used screens and the games (or the totals before any game was played).
func trophyRoomDrawStats(renderer *sdl.Renderer, config *Config, f trophyFonts, l trophyRoomLayout) {
	s := &trophyRoom.stats
	c := l.content
	gap := int32(14)
	row1H := c.H * 52 / 100
	if row1H < 150 {
		row1H = 150
	}
	row2Y := c.Y + row1H + gap
	row2H := c.Y + c.H - row2Y
	leftW := (c.W - gap) * 44 / 100
	rightX := c.X + leftW + gap
	rightW := c.X + c.W - rightX

	week := sdl.Rect{X: c.X, Y: c.Y, W: leftW, H: row1H}
	heat := sdl.Rect{X: rightX, Y: c.Y, W: rightW, H: row1H}
	trophyRoom.heatCard = heat
	trophyRoomDrawWeek(renderer, config, f, week, s)
	trophyRoomDrawInsights(renderer, config, f, heat, s)
	if row2H >= 80 {
		half := (c.W - gap) / 2
		used := sdl.Rect{X: c.X, Y: row2Y, W: half, H: row2H}
		other := sdl.Rect{X: c.X + half + gap, Y: row2Y, W: c.X + c.W - (c.X + half + gap), H: row2H}
		trophyRoomDrawBars(renderer, config, f, used, "MOST USED", s.scenes, s.sceneVals, true,
			"Start exploring — your screens show up here.")
		if len(s.games) > 0 {
			trophyRoomDrawBars(renderer, config, f, other, "GAMES", s.games, s.gameVals, false, "")
		} else {
			trophyRoomDrawTotals(renderer, config, f, other, s)
		}
	}
}

// trophyRoomDrawWeek draws this week as seven bars, Monday first. Today is in
// the accent, the days before it a step dimmer, the days still to come empty.
func trophyRoomDrawWeek(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, s *trophyStatsView) {
	top := trophyRoomSectionCard(renderer, config, f, rect, "THIS WEEK", s.weekLabel)
	labelH := trophyFontH(f.section, 16)
	valH := trophyFontH(f.cardMeta, 16)
	area := sdl.Rect{X: rect.X + 20, Y: top + valH + 4, W: rect.W - 40, H: rect.Y + rect.H - 14 - labelH - 6 - (top + valH + 4)}
	trophyRoom.barsRect = area
	if area.W < 70 || area.H < 20 {
		return
	}
	colW := area.W / 7
	barW := colW * 56 / 100
	if barW > 34 {
		barW = 34
	}
	scale := s.weekMax
	if scale < 3600 {
		scale = 3600 // an hour is the smallest full bar, so ten minutes stays small
	}
	accent := glassAccent()
	for i := 0; i < 7; i++ {
		x := area.X + int32(i)*colW + (colW-barW)/2
		fillRoundedRect(renderer, x, area.Y, barW, area.H, 6, glassA(14))
		if !s.weekFuture[i] && s.week[i] > 0 {
			h := int32(float64(area.H) * float64(s.week[i]) / float64(scale))
			if h < 6 {
				h = 6
			}
			if h > area.H {
				h = area.H
			}
			fill := WithAlpha(accent, 150)
			if i == s.today {
				fill = accent
			}
			fillRoundedRect(renderer, x, area.Y+area.H-h, barW, h, 6, fill)
			if f.cardMeta != nil && (i == s.today || s.week[i] == s.weekMax) {
				vw, _ := measureText(f.cardMeta, s.weekVals[i])
				vx := x + barW/2 - vw/2
				if vx < rect.X+8 {
					vx = rect.X + 8
				}
				if vx+vw > rect.X+rect.W-8 {
					vx = rect.X + rect.W - 8 - vw
				}
				renderText(renderer, config, f.cardMeta, s.weekVals[i], ColorTextSecondary(), vx, area.Y+area.H-h-valH-2)
			}
		}
		if f.section != nil {
			label := trophyWeekdays[i]
			lw, _ := measureText(f.section, label)
			if lw > colW-2 {
				label = label[:1]
				lw, _ = measureText(f.section, label)
			}
			ink := ColorTextTertiary()
			if i == s.today {
				ink = ColorTextPrimary()
			}
			renderText(renderer, config, f.section, label, ink, area.X+int32(i)*colW+(colW-lw)/2, area.Y+area.H+6)
		}
	}
}

// trophyRoomDrawInsights draws the 13-week heatmap with the streak, the best
// day and the averages beside it (one column of stats on a narrow card).
func trophyRoomDrawInsights(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, s *trophyStatsView) {
	top := trophyRoomSectionCard(renderer, config, f, rect, "LAST 13 WEEKS", "")
	gridW := int32(300)
	if rect.W < 640 {
		gridW = 200
	}
	if gridW > rect.W-60 {
		gridW = rect.W - 60
	}
	gridH := rect.Y + rect.H - 16 - top
	if gridH > 160 {
		gridH = 160
	}
	if gridW < 70 || gridH < 40 {
		return
	}
	heat := sdl.Rect{X: rect.X + 20, Y: top, W: gridW, H: gridH}
	playtimeHeatRect = heat
	drawWrappedHeatmap(renderer, config, heat, s.ins)

	if f.section == nil || f.cardMeta == nil {
		return
	}
	statsX := heat.X + heat.W + 22
	statsW := rect.X + rect.W - 20 - statsX
	if statsW < 60 {
		return
	}
	labelH := trophyFontH(f.section, 16)
	valueFont := f.value
	if valueFont == nil {
		valueFont = f.cardMeta
	}
	valueH := trophyFontH(valueFont, 24)
	pitch := labelH + valueH + 8
	// Two columns when the card has room for them, one otherwise; the rows
	// that do not fit the card's height are left out rather than clipped.
	cols := int32(1)
	if statsW >= 300 {
		cols = 2
	}
	colW := statsW / cols
	rowsFit := (rect.Y + rect.H - 12 - top) / pitch
	for i := range s.insLabels {
		col := int32(i) % cols
		row := int32(i) / cols
		if row >= rowsFit {
			break
		}
		x := statsX + col*colW
		y := top + row*pitch
		renderText(renderer, config, f.section, ellipsize(f.section, s.insLabels[i], colW-8), ColorTextTertiary(), x, y)
		renderText(renderer, config, valueFont, ellipsize(valueFont, s.insValues[i], colW-8), ColorTextPrimary(), x, y+labelH+2)
	}
}

// trophyRoomDrawBars draws a ranked list as horizontal bars: name, bar, value.
func trophyRoomDrawBars(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, caption string,
	rows []playtimeSceneTotal, vals []string, scenes bool, empty string) {
	top := trophyRoomSectionCard(renderer, config, f, rect, caption, "")
	if f.cardDesc == nil {
		return
	}
	if len(rows) == 0 {
		if empty != "" {
			renderText(renderer, config, f.cardDesc, ellipsize(f.cardDesc, empty, rect.W-40), ColorTextSecondary(), rect.X+20, top+4)
		}
		return
	}
	lineH := trophyFontH(f.cardDesc, 18)
	pitch := lineH + 12
	fit := int((rect.Y + rect.H - 12 - top) / pitch)
	nameW := rect.W * 34 / 100
	valW := int32(0)
	for i := 0; i < len(vals) && i < fit; i++ {
		if w, _ := measureText(f.cardDesc, vals[i]); w > valW {
			valW = w
		}
	}
	barX := rect.X + 20 + nameW + 10
	barW := rect.X + rect.W - 20 - valW - 12 - barX
	maxSecs := rows[0].Seconds
	accent := glassAccent()
	for i := 0; i < len(rows) && i < fit && i < len(vals); i++ {
		y := top + int32(i)*pitch
		name := rows[i].Name
		if scenes {
			name = trophySceneLabel(name)
		}
		renderText(renderer, config, f.cardDesc, ellipsize(f.cardDesc, name, nameW), ColorTextSecondary(), rect.X+20, y)
		if barW > 10 {
			frac := 0.0
			if maxSecs > 0 {
				frac = float64(rows[i].Seconds) / float64(maxSecs)
			}
			fill := accent
			if i > 0 {
				fill = WithAlpha(accent, 170)
			}
			UIProgressBar(renderer, sdl.Rect{X: barX, Y: y + lineH/2 - 4, W: barW, H: 8}, frac, glassA(16), fill)
		}
		vw, _ := measureText(f.cardDesc, vals[i])
		renderText(renderer, config, f.cardDesc, vals[i], ColorTextTertiary(), rect.X+rect.W-20-vw, y)
	}
}

// trophyRoomDrawTotals draws the six headline numbers in a grid.
func trophyRoomDrawTotals(renderer *sdl.Renderer, config *Config, f trophyFonts, rect sdl.Rect, s *trophyStatsView) {
	top := trophyRoomSectionCard(renderer, config, f, rect, "TOTALS", "")
	if f.section == nil {
		return
	}
	valueFont := f.value
	if valueFont == nil {
		valueFont = f.cardDesc
	}
	labelH := trophyFontH(f.section, 16)
	valueH := trophyFontH(valueFont, 24)
	pitch := labelH + valueH + 10
	cols := int32(3)
	if rect.W < 420 {
		cols = 2
	}
	colW := (rect.W - 40) / cols
	rowsFit := (rect.Y + rect.H - 12 - top) / pitch
	for i := range s.totals {
		col := int32(i) % cols
		row := int32(i) / cols
		if row >= rowsFit {
			break
		}
		x := rect.X + 20 + col*colW
		y := top + row*pitch
		renderText(renderer, config, f.section, ellipsize(f.section, s.totals[i][0], colW-8), ColorTextTertiary(), x, y)
		if valueFont != nil {
			renderText(renderer, config, valueFont, ellipsize(valueFont, s.totals[i][1], colW-8), ColorTextPrimary(), x, y+labelH+2)
		}
	}
}

// ── Details sheet ─────────────────────────────────────────────────────────────

// trophyRoomOpenSheet opens the details of the trophy at list position pos.
func trophyRoomOpenSheet(pos int) {
	r := &trophyRoom
	if pos < 0 || pos >= len(r.list) {
		return
	}
	r.cursor = pos
	r.sheet = true
	r.sheetIdx = r.list[pos]
	r.sheetEnter.begin(uiClockMS(), uiPanelEnterMS)
	trophyRoomReveal()
}

// trophyRoomStepSheet moves the sheet to the next or previous trophy.
func trophyRoomStepSheet(dir int) bool {
	r := &trophyRoom
	n := len(r.list)
	if n == 0 {
		return false
	}
	next := r.cursor + dir
	if next < 0 || next >= n {
		return false
	}
	r.cursor = next
	r.sheetIdx = r.list[next]
	trophyRoomReveal()
	return true
}

// trophyRoomDrawSheet draws the details sheet over the page.
func trophyRoomDrawSheet(renderer *sdl.Renderer, config *Config, f trophyFonts, l trophyRoomLayout) {
	r := &trophyRoom
	idx := r.sheetIdx
	if idx < 0 || idx >= len(playtimeAchievements) || idx >= len(r.rows) {
		r.sheet = false
		return
	}
	a := &playtimeAchievements[idx]
	row := &r.rows[idx]
	secret := a.Hidden && !row.earned

	progress := 1.0
	if uiMotionEnabled(config) {
		progress = r.sheetEnter.progress(uiClockMS())
	} else {
		r.sheetEnter.cancel()
	}
	// The dim covers the page between the bars; the footer keeps its hints,
	// which now describe the sheet.
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassScrimColor(uiScrimProgress(progress, uiPanelEnterMS)))
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: l.body.Y, W: screenWidth, H: l.body.H})

	titleFont := f.sheetTitle
	if titleFont == nil {
		titleFont = f.title
	}
	titleH := trophyFontH(titleFont, 32)
	subH := trophyFontH(f.sub, 20)
	descH := trophyFontH(f.sub, 20)
	smallH := trophyFontH(f.small, 17)
	medal := int32(104)
	if l.body.H < 520 {
		medal = 76
	}
	w := min32(600, screenWidth-2*48)
	descLines := 2
	h := 24 + medal + 16 + titleH + 6 + subH + 14 + int32(descLines)*descH + 18 + 8 + 10 + smallH + 8 + smallH + 22
	if h > l.body.H-16 {
		h = l.body.H - 16
	}
	sheet := sdl.Rect{X: (screenWidth - w) / 2, Y: l.body.Y + (l.body.H-h)/2, W: w, H: h}
	r.sheetRect = sheet
	r.prevRect, r.nextRect, r.replayRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}

	fade := uiFadeIn.at(progress)
	rise := int32(math.Round(uiPanelRise.at(progress)))
	compositeUILayerIn(renderer, fade, rise, sheet, func() {
		metal := trophyMetalOf(a.Tier)
		if row.earned {
			homeGlowBlob(renderer, sdl.Rect{X: sheet.X + sheet.W/2 - medal*3/2, Y: sheet.Y - medal/2, W: medal * 3, H: medal * 3}, metal.base, 80)
		}
		drawGlassSheet(renderer, sheet, glassSheetFill())
		drawOverlayCardAccent(renderer, sheet.X, sheet.Y, sheet.W)

		cx := sheet.X + sheet.W/2
		y := sheet.Y + 24
		mbox := sdl.Rect{X: cx - medal/2, Y: y, W: medal, H: medal}
		trophyDrawTrophy(renderer, mbox, a.Tier, row.earned, secret)
		if row.earned && uiMotionEnabled(config) {
			el := uiClockMS() - r.sheetEnter.start
			if r.sheetEnter.start > 0 && el < 2000 {
				cup := medal * 72 / 100
				cupDst := sdl.Rect{X: cx - cup/2, Y: y + medal/2 - cup/2 + medal/40, W: cup, H: cup}
				trophyDrawShine(renderer, cupDst, (float64(el)-200)/700, 170)
			}
		}
		if secret && f.big != nil {
			qw, qh := measureText(f.big, "?")
			renderText(renderer, config, f.big, "?", ColorTextTertiary(), cx-qw/2, y+(medal-qh)/2)
		}
		y += medal + 16

		title := a.Title
		desc := a.Desc
		if secret {
			title = "Hidden trophy"
			desc = "This one is a secret until you earn it. Keep exploring JukaHub."
		}
		inner := sheet.W - 2*OverlayPadX
		if titleFont != nil {
			t := ellipsize(titleFont, title, inner)
			tw, _ := measureText(titleFont, t)
			renderText(renderer, config, titleFont, t, ColorTextPrimary(), cx-tw/2, y)
		}
		y += titleH + 6
		tierLine := trophyTierName(a.Tier) + " · " + trophyRarityLabel(a.Tier) + " · " + strconv.Itoa(trophyTierPoints(a.Tier)) + " pts"
		if f.sub != nil {
			t := ellipsize(f.sub, tierLine, inner)
			tw, _ := measureText(f.sub, t)
			dot := int32(5)
			sx := cx - (tw+2*dot+8)/2
			fillCircle(renderer, sx+dot, y+subH/2, dot, metal.base)
			renderText(renderer, config, f.sub, t, trophyTierInk(a.Tier), sx+2*dot+8, y)
		}
		y += subH + 14
		if f.sub != nil {
			for _, line := range wrapTextLines(f.sub, desc, inner, descLines) {
				lw, _ := measureText(f.sub, line)
				renderText(renderer, config, f.sub, line, ColorTextSecondary(), cx-lw/2, y)
				y += descH
			}
		}
		y = sheet.Y + 24 + medal + 16 + titleH + 6 + subH + 14 + int32(descLines)*descH + 18

		// Progress: the bar and what it means.
		barW := inner - 40
		barX := cx - barW/2
		fill := trophyTierColor(a.Tier)
		UIProgressBar(renderer, sdl.Rect{X: barX, Y: y, W: barW, H: 8}, row.frac, glassA(22), fill)
		y += 8 + 10
		status := row.progress
		switch {
		case row.earned:
			status = row.whenLong
		case secret:
			status = "Progress is hidden"
		case status != "":
			status += " · " + row.pct
		}
		if f.small != nil && status != "" {
			t := ellipsize(f.small, status, inner)
			tw, _ := measureText(f.small, t)
			ink := ColorTextSecondary()
			if row.earned {
				ink = trophyTierInk(a.Tier)
			}
			renderText(renderer, config, f.small, t, ink, cx-tw/2, y)
		}
		y += smallH + 8
		meta := "#" + a.Icon + " · " + a.Group
		if a.Metric == trophyMetricScene && !secret {
			meta += " · " + trophySceneLabel(a.Scene)
		}
		if f.small != nil {
			t := ellipsize(f.small, meta, inner)
			tw, _ := measureText(f.small, t)
			renderText(renderer, config, f.small, t, ColorTextTertiary(), cx-tw/2, y)
		}

		// Stepping arrows on the sheet's sides, and Replay on an earned trophy.
		arrow := int32(40)
		ay := sheet.Y + 24 + medal/2 - arrow/2
		if r.cursor > 0 {
			r.prevRect = sdl.Rect{X: sheet.X + 16, Y: ay, W: arrow, H: arrow}
			trophyRoomArrow(renderer, r.prevRect, -1)
		}
		if r.cursor < len(r.list)-1 {
			r.nextRect = sdl.Rect{X: sheet.X + sheet.W - 16 - arrow, Y: ay, W: arrow, H: arrow}
			trophyRoomArrow(renderer, r.nextRect, 1)
		}
		if row.earned && f.chip != nil {
			label := "Replay"
			tw, _ := measureText(f.chip, label)
			rect := sdl.Rect{X: sheet.X + sheet.W - 16 - (tw + 28), Y: sheet.Y + 16, W: tw + 28, H: 32}
			if r.nextRect.W > 0 && rect.Y+rect.H > r.nextRect.Y {
				rect.Y = r.nextRect.Y - rect.H - 8
			}
			if rect.Y >= sheet.Y+8 {
				trophyRoomPill(renderer, config, f.chip, rect, label, false, false)
				r.replayRect = rect
			}
		}
	})
}

// trophyRoomArrow draws a round glass button with a chevron pointing dir.
func trophyRoomArrow(renderer *sdl.Renderer, rect sdl.Rect, dir int) {
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.W/2, glassChipFill())
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.W/2, 1, glassChipEdge())
	cx := rect.X + rect.W/2
	cy := rect.Y + rect.H/2
	arm := rect.W / 6
	tip := cx + int32(dir)*arm/2
	back := cx - int32(dir)*arm/2
	setDrawColor(renderer, ColorTextPrimary())
	for t := int32(-1); t <= 1; t++ {
		_ = renderer.DrawLine(back+t, cy-arm, tip+t, cy)
		_ = renderer.DrawLine(tip+t, cy, back+t, cy+arm)
	}
}

// ── Input ─────────────────────────────────────────────────────────────────────

// trophyAction is one input, whichever device it came from.
type trophyAction int

const (
	trophyActUp trophyAction = iota
	trophyActDown
	trophyActLeft
	trophyActRight
	trophyActConfirm
	trophyActBack
	trophyActSort
	trophyActRetro
	trophyActPrevTab
	trophyActNextTab
	trophyActPageUp
	trophyActPageDown
	trophyActFirst
	trophyActLast
	trophyActDebug
)

// trophyRoomAction applies one input. It reports false only for what the page
// does not use, above all Up at the top of the page, which the top bar takes.
func trophyRoomAction(config *Config, act trophyAction) bool {
	trophyRoomSyncVisit()
	r := &trophyRoom
	if !r.built {
		trophyRoomRefresh(config)
	}

	if act == trophyActDebug {
		// The hidden test hook: each press celebrates the next tier.
		trophyCelebrateTest(trophyTier(r.debugTier % trophyTierCount))
		r.debugTier++
		return true
	}

	if r.sheet {
		switch act {
		case trophyActLeft, trophyActUp:
			if trophyRoomStepSheet(-1) {
				PlayNavSound()
			}
		case trophyActRight, trophyActDown:
			if trophyRoomStepSheet(1) {
				PlayNavSound()
			}
		case trophyActConfirm, trophyActBack:
			r.sheet = false
			PlayBackSound()
		case trophyActSort:
			if r.sheetIdx >= 0 && r.sheetIdx < len(playtimeAchievements) &&
				trophyReplay(playtimeAchievements[r.sheetIdx].ID) {
				PlayActivateSound()
			}
		}
		return true
	}

	switch act {
	case trophyActBack:
		if config != nil {
			goBackScene(config)
		}
		return true
	case trophyActRetro:
		openRetroAchievementsPanel()
		return true
	case trophyActPrevTab, trophyActNextTab:
		r.tab = 1 - r.tab
		r.zone = trophyZoneChips
		r.chip = r.filter
		PlayNavSound()
		return true
	}

	if r.tab == trophyTabStats {
		switch act {
		case trophyActUp:
			return false
		case trophyActLeft:
			r.tab = trophyTabTrophies
			r.zone = trophyZoneChips
			r.chip = r.filter
			PlayNavSound()
		}
		return true
	}

	if r.zone == trophyZoneChips {
		switch act {
		case trophyActUp:
			return false
		case trophyActLeft, trophyActRight:
			next := r.chip - 1
			if act == trophyActRight {
				next = r.chip + 1
			}
			if next < 0 || next >= trophyChipCount {
				return true
			}
			r.chip = next
			if r.chip < trophyFilterCount && r.filter != r.chip {
				// Filters apply as the focus moves along them, like tabs.
				r.filter = r.chip
				r.cursor = 0
				playtimeScroll = 0
				trophyRoomRefresh(config)
			}
			PlayNavSound()
		case trophyActDown, trophyActPageDown:
			if len(r.list) > 0 {
				r.zone = trophyZoneGrid
				cols, _, _, _ := trophyRoomMetrics()
				if r.cursor >= cols {
					r.cursor = 0
				}
				trophyRoomReveal()
				PlayNavSound()
			}
		case trophyActConfirm:
			switch {
			case r.chip == trophyChipSort:
				trophyRoomCycleSort(config)
				PlayActivateSound()
			case r.chip == trophyChipStyle:
				// The toast's own chirp is the feedback.
				ShowToast(trophyStyleToast(trophyCycleCelebrationMode(config)), ToastKindSwitch)
			default:
				// A on a filter: into its list.
				if len(r.list) > 0 {
					r.zone = trophyZoneGrid
					r.cursor = 0
					trophyRoomReveal()
				}
				PlayActivateSound()
			}
		case trophyActSort:
			trophyRoomCycleSort(config)
			PlayActivateSound()
		}
		return true
	}

	// The grid.
	n := len(r.list)
	cols, cardH, gap, viewH := trophyRoomMetrics()
	if cols <= 0 {
		cols = 1
	}
	page := int(viewH / (cardH + gap))
	if page < 1 {
		page = 1
	}
	move := func(to int) {
		if to < 0 {
			to = 0
		}
		if to > n-1 {
			to = n - 1
		}
		if to != r.cursor {
			r.cursor = to
			PlayNavSound()
		}
		trophyRoomReveal()
	}
	switch act {
	case trophyActUp:
		if r.cursor < cols {
			r.zone = trophyZoneChips
			r.chip = r.filter
			playtimeScroll = 0
			PlayNavSound()
			return true
		}
		move(r.cursor - cols)
	case trophyActDown:
		if r.cursor+cols < n {
			move(r.cursor + cols)
		} else if r.cursor/cols < (n-1)/cols {
			move(n - 1) // the last row is short: land on its last card
		}
	case trophyActLeft:
		move(r.cursor - 1)
	case trophyActRight:
		move(r.cursor + 1)
	case trophyActPageUp:
		move(r.cursor - page*cols)
	case trophyActPageDown:
		move(r.cursor + page*cols)
	case trophyActFirst:
		move(0)
	case trophyActLast:
		move(n - 1)
	case trophyActConfirm:
		trophyRoomOpenSheet(r.cursor)
		PlayActivateSound()
	case trophyActSort:
		trophyRoomCycleSort(config)
		PlayActivateSound()
	}
	return true
}

// trophyRoomCycleSort steps the sort order and keeps the list at its top.
func trophyRoomCycleSort(config *Config) {
	r := &trophyRoom
	r.sort = (r.sort + 1) % trophySortCount
	r.cursor = 0
	playtimeScroll = 0
	if config != nil {
		trophyRoomRefresh(config)
	}
}

// trophyRoomKeyAction maps a key to an action.
func trophyRoomKeyAction(e *sdl.KeyboardEvent) (trophyAction, bool) {
	shift := e.Keysym.Mod&sdl.KMOD_SHIFT != 0
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return trophyActUp, true
	case sdl.K_DOWN:
		return trophyActDown, true
	case sdl.K_LEFT:
		return trophyActLeft, true
	case sdl.K_RIGHT:
		return trophyActRight, true
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		return trophyActConfirm, true
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		return trophyActBack, true
	case sdl.K_x:
		return trophyActSort, true
	case sdl.K_y:
		return trophyActRetro, true
	case sdl.K_TAB:
		if shift {
			return trophyActPrevTab, true
		}
		return trophyActNextTab, true
	case sdl.K_PAGEUP:
		return trophyActPageUp, true
	case sdl.K_PAGEDOWN:
		return trophyActPageDown, true
	case sdl.K_HOME:
		return trophyActFirst, true
	case sdl.K_END:
		return trophyActLast, true
	case sdl.K_F9:
		return trophyActDebug, true
	}
	return 0, false
}

// trophyRoomPadAction maps a controller button to an action.
func trophyRoomPadAction(button uint8) (trophyAction, bool) {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return trophyActUp, true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return trophyActDown, true
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return trophyActLeft, true
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return trophyActRight, true
	case sdl.CONTROLLER_BUTTON_A:
		return trophyActConfirm, true
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		return trophyActBack, true
	case sdl.CONTROLLER_BUTTON_X:
		return trophyActSort, true
	case sdl.CONTROLLER_BUTTON_Y:
		return trophyActRetro, true
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return trophyActPrevTab, true
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return trophyActNextTab, true
	}
	return 0, false
}

// trophyRoomFooterHints are the footer's hints for the room's current state.
func trophyRoomFooterHints() []FooterHint {
	r := &trophyRoom
	if r.sheet {
		hints := []FooterHint{{Button: "Left/Right", Label: "Previous / next"}}
		if r.sheetIdx >= 0 && r.sheetIdx < len(r.rows) && r.rows[r.sheetIdx].earned {
			hints = append(hints, FooterHint{Button: "X", Label: "Replay"})
		}
		return append(hints, FooterHint{Button: "B", Label: "Close"})
	}
	if r.tab == trophyTabStats {
		hints := []FooterHint{{Button: "L1/R1", Label: "Trophies"}, {Button: "B", Label: "Back"}}
		if retroAchievementsSupported() {
			hints = append(hints, FooterHint{Button: "Y", Label: "RetroAchievements"})
		}
		return hints
	}
	confirm := "Details"
	if r.zone == trophyZoneChips {
		switch {
		case r.chip == trophyChipSort:
			confirm = "Sort"
		case r.chip == trophyChipStyle:
			confirm = "Confetti"
		default:
			confirm = "Trophies"
		}
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: confirm},
		{Button: "B", Label: "Back"},
		{Button: "L1/R1", Label: "Playtime"},
		{Button: "X", Label: "Sort"},
	}
	if retroAchievementsSupported() {
		hints = append(hints, FooterHint{Button: "Y", Label: "RetroAchievements"})
	}
	return hints
}

// trophyRoomClick handles a click or a tap. It reports whether the room used it.
func trophyRoomClick(mx, my int32) bool {
	trophyRoomSyncVisit()
	r := &trophyRoom
	if r.sheet {
		switch {
		case r.prevRect.W > 0 && hitTouchTarget(r.prevRect, mx, my):
			if trophyRoomStepSheet(-1) {
				PlayNavSound()
			}
		case r.nextRect.W > 0 && hitTouchTarget(r.nextRect, mx, my):
			if trophyRoomStepSheet(1) {
				PlayNavSound()
			}
		case r.replayRect.W > 0 && pointInRect(mx, my, r.replayRect):
			if r.sheetIdx >= 0 && r.sheetIdx < len(playtimeAchievements) &&
				trophyReplay(playtimeAchievements[r.sheetIdx].ID) {
				PlayActivateSound()
			}
		case pointInRect(mx, my, r.sheetRect):
			// Inside the sheet: nothing underneath may react.
		default:
			r.sheet = false
			PlayBackSound()
		}
		return true
	}
	for t, rect := range r.tabRects {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			if r.tab != t {
				r.tab = t
				r.zone = trophyZoneChips
				r.chip = r.filter
				PlayNavSound()
			}
			return true
		}
	}
	if r.raRect.W > 0 && pointInRect(mx, my, r.raRect) {
		openRetroAchievementsPanel()
		return true
	}
	if r.tab == trophyTabStats {
		return false
	}
	for i, rect := range r.chipRects {
		if rect.W <= 0 || !pointInRect(mx, my, rect) {
			continue
		}
		r.zone = trophyZoneChips
		r.chip = i
		switch {
		case i < trophyFilterCount:
			if r.filter != i {
				r.filter = i
				r.cursor = 0
				playtimeScroll = 0
			}
			PlayNavSound()
		case i == trophyChipSort:
			trophyRoomCycleSort(nil)
			PlayActivateSound()
		default:
			ShowToast(trophyStyleToast(trophyCycleCelebrationMode(appConfig)), ToastKindSwitch)
		}
		return true
	}
	for k := 0; k < r.cardN; k++ {
		if pointInRect(mx, my, r.cardRects[k]) {
			r.zone = trophyZoneGrid
			trophyRoomOpenSheet(r.cardPos[k])
			PlayActivateSound()
			return true
		}
	}
	return false
}

// trophyRoomWheel scrolls the grid. Up (a positive delta) scrolls back.
func trophyRoomWheel(y int32) bool {
	if y == 0 {
		return false
	}
	trophyRoomSyncVisit()
	r := &trophyRoom
	if r.sheet {
		if y > 0 {
			trophyRoomStepSheet(-1)
		} else {
			trophyRoomStepSheet(1)
		}
		return true
	}
	if r.tab != trophyTabTrophies {
		return false
	}
	playtimeScroll -= y * 48
	trophyRoomClampScroll()
	return true
}

// ── Scene ─────────────────────────────────────────────────────────────────────

// playtimeScene is the registered module: the room owns its rendering and its
// input because the scene has no config elements.
type playtimeScene struct{ sceneBase }

func (playtimeScene) render(r *sdl.Renderer, c *Config) bool {
	renderPlaytime(r, c)
	return true
}

func (playtimeScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	act, ok := trophyRoomKeyAction(e)
	if !ok {
		return false
	}
	return trophyRoomAction(c, act)
}

func (playtimeScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	act, ok := trophyRoomPadAction(e.Button)
	if !ok {
		return false
	}
	return trophyRoomAction(c, act)
}

// handlePlaytimeWheel scrolls the room from a mouse wheel or a touchpad drag.
func handlePlaytimeWheel(y int32) bool {
	return trophyRoomWheel(y)
}

// handlePlaytimeMouse routes a click on the room. The scene has no config
// elements, so main.go forwards the click here.
func handlePlaytimeMouse(mx, my int32) bool {
	return trophyRoomClick(mx, my)
}
