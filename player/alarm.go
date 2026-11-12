package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Alarm Clock
// ──────────────────────────────────────────────────────────────────────

type alarmEntry struct {
	Hour    int    `json:"hour"`
	Minute  int    `json:"minute"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	// FiredOn is the local date (2006-01-02) the alarm is done for: the day
	// it rang, or the day it was set for a time already past. The old "fired"
	// flag was only cleared by a launch at exactly midnight, so a daily alarm
	// rang once and then never again.
	FiredOn string `json:"fired_on,omitempty"`
	// RangOn is the date it last actually rang, which is what the list marks.
	RangOn string `json:"rang_on,omitempty"`
	// Sound is the ringtone id (alarmtone.go): a synthesised tone, or "file:"
	// and a track from the user's music. Empty means the default, which is
	// what alarms saved before the picker existed carry.
	Sound string `json:"sound,omitempty"`
	// SnoozeUntil is when a snoozed alarm rings again (RFC3339). While it is in
	// the future the alarm stays quiet even though it already rang today.
	SnoozeUntil string `json:"snooze_until,omitempty"`
	ID          int    `json:"id"`
}

// alarmDate is the key FiredOn is compared with.
func alarmDate(t time.Time) string { return t.Format("2006-01-02") }

// firedToday reports whether the alarm already rang on now's date.
func (a alarmEntry) firedToday(now time.Time) bool { return a.FiredOn == alarmDate(now) }

// alarmCatchUp is how late an alarm may still ring. The check runs about once
// a second, but a frame that blocks (a modal, a slow card) or a short sleep
// could step over the exact minute, and a missed alarm is the one failure an
// alarm clock must not have.
const alarmCatchUp = 5 * time.Minute

// alarmDue reports whether a ring is owed now: enabled, not rung today, and
// now inside [the alarm's time today, alarmCatchUp later).
func alarmDue(a alarmEntry, now time.Time) bool {
	if !a.Enabled {
		return false
	}
	// A snoozed alarm is quiet until its snooze ends, then rings once more on
	// the same day even though it already rang. A snooze the device slept
	// through is not replayed hours later: only the catch-up window counts.
	if a.SnoozeUntil != "" {
		if until, err := time.Parse(time.RFC3339, a.SnoozeUntil); err == nil {
			if now.Before(until) {
				return false
			}
			if now.Sub(until) < alarmCatchUp {
				return true
			}
		}
	}
	if a.firedToday(now) {
		return false
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
	return !now.Before(at) && now.Sub(at) < alarmCatchUp
}

// alarmArmFrom marks an alarm set (or reset) at now: a time already past today
// starts tomorrow instead of ringing at once through the catch-up window.
func alarmArmFrom(a *alarmEntry, now time.Time) {
	a.SnoozeUntil = ""
	at := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
	if now.Before(at) {
		a.FiredOn = ""
	} else {
		a.FiredOn = alarmDate(now)
	}
}

type alarmList struct {
	Alarms []alarmEntry `json:"alarms"`
}

var (
	alarmStore  alarmList
	alarmNextID int
)

// --- Persistence ---

func alarmLoad() {
	var al alarmList
	if _, err := readJSONWithBackup("jukaalarm.json", &al); err != nil {
		alarmStore = alarmList{}
		return
	}
	alarmStore = al
	for _, a := range al.Alarms {
		if a.ID >= alarmNextID {
			alarmNextID = a.ID + 1
		}
	}
}

func alarmSave() {
	alarmStore.Alarms = append([]alarmEntry{}, alarmStore.Alarms...)
	data, err := json.MarshalIndent(alarmStore, "", "  ")
	if err != nil {
		return
	}
	// Same crash-safe path as the config and the notes: an alarm the user set
	// must survive pulling the power, not just a clean shutdown.
	if err := AtomicWrite("jukaalarm.json", data, 0644); err != nil {
		Log().Warn("alarms could not be saved", "err", err)
	}
}

// --- State ---

type alarmScene struct {
	cursor    int // which row is focused
	tab       int // alarmTabList, or the editor row with the focus (alarmedit.go)
	editHour  int
	editMin   int
	editLabel string
	editSound string    // ringtone id, chosen on the editor's sound row
	editID    int       // -1 = new, >=0 = editing existing
	now       time.Time // cached current time (updated each frame)
	// The music picker on the ringtone row: open, its focused row, its first
	// visible row, and when the focused track's preview is due (zero = none).
	pickOpen    bool
	pickCursor  int
	pickScroll  int
	pickPreview time.Time
	// orig is what the editor opened with, so B can tell unsaved changes.
	orig alarmEditValues
}

var alarmUI alarmScene

// alarmBackRect is the on-screen Back pill, populated each frame so touch users
// are never trapped in the alarm screen (the keyboard/controller use B).
var alarmBackRect sdl.Rect

// alarmAddRect is the "+ Add Alarm" row, populated by renderAlarmList.
var alarmAddRect sdl.Rect

// Alarm list geometry shared by the renderer and the mouse handler.
const (
	alarmListX = int32(100)
	alarmListW = int32(740)
	alarmRowH  = int32(64)
)

// alarmListTop is where the row list starts: under the shared header, the big
// clock and the next-alarm countdown. On the compact canvas the mini calendar
// and the list share columns, so there the list also starts below the calendar
// instead of running under it. The renderer and the click hit-test both read
// this, so a tap cannot land on the row above the one under the finger.
func alarmListTop() int32 {
	top := headerBarHeight() + 144
	cardW, cardH := miniCalendarSize()
	calLeft := screenWidth - cardW - 24
	calBottom := headerBarHeight() + 8 + cardH + 12
	if alarmListX+alarmListW > calLeft && calBottom > top {
		top = calBottom
	}
	return top
}

func alarmInitUI() {
	now := time.Now()
	alarmUI = alarmScene{
		cursor:    0,
		tab:       0,
		editHour:  now.Hour(),
		editMin:   now.Minute(),
		editLabel: "",
		editSound: alarmDefaultSound,
		editID:    -1,
		now:       now,
	}
}

// ensureAlarmScene gives the alarm UI a home. The full-screen alarm editor is
// rendered through the scene pipeline (renderScene case "Alarm"), but no
// shipped config defines that scene, so the UI had no entry point at all.
// Like ensureToolsMenuEntry the edit is in-memory only: the user's config file
// is never rewritten.
func ensureAlarmScene(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == "Alarm" {
			return
		}
	}
	config.Scenes = append(config.Scenes, SceneConfig{Name: "Alarm", Icon: "A"})
}

// openAlarmScene switches to the Alarm scene so the status-bar clock (and the
// Start menu) can reach the alarm UI. It reports whether the scene opened.
func openAlarmScene(config *Config) bool {
	if config == nil {
		return false
	}
	ensureAlarmScene(config)
	idx := sceneIndexExact(config, "Alarm")
	if idx < 0 {
		return false
	}
	changeSceneTo(config, idx)
	return true
}

// --- Navigation ---

// alarmNavigate moves the list's focus (the editor moves with alarmEditMove).
func alarmNavigate(dr int) {
	total := len(alarmStore.Alarms)
	if alarmUI.tab != alarmTabList || total == 0 {
		return
	}
	alarmUI.cursor += dr
	if alarmUI.cursor < 0 {
		alarmUI.cursor = total - 1
	}
	if alarmUI.cursor >= total {
		alarmUI.cursor = 0
	}
	PlayNavSound()
}

// alarmSelect is A on the list: edit the focused alarm, or start one when
// there are none. In the editor A is alarmEditActivate.
func alarmSelect() {
	if alarmUI.tab != alarmTabList {
		alarmEditActivate()
		return
	}
	if len(alarmStore.Alarms) == 0 {
		alarmStartNew(time.Now())
		PlayActivateSound()
		return
	}
	if alarmUI.cursor >= 0 && alarmUI.cursor < len(alarmStore.Alarms) {
		a := alarmStore.Alarms[alarmUI.cursor]
		alarmEditOpen(a, a.ID)
		PlayActivateSound()
	}
}

func alarmBack() {
	if alarmUI.pickOpen {
		alarmPickClose()
		return
	}
	if alarmUI.tab != alarmTabList {
		alarmEditBack()
		return
	}
	goBackScene(appConfig)
	PlayBackSound()
}

// alarmStartNew opens the editor on a new alarm five minutes from now. The
// hour follows the minutes past the top of the hour: (minute+5)%60 alone set
// 10:58 to 10:03, a time already gone.
func alarmStartNew(now time.Time) {
	soon := now.Add(5 * time.Minute)
	alarmEditOpen(alarmEntry{Hour: soon.Hour(), Minute: soon.Minute(), Sound: alarmDefaultSound}, -1)
}

// alarmDeleteArm is the first L1 of a delete: a second one on the same alarm
// inside alarmDeleteWindow removes it. One press used to delete at once, from
// the editor too, with nothing to take it back.
var alarmDeleteArm struct {
	at time.Time
	id int
}

const alarmDeleteWindow = 3 * time.Second

// alarmRequestDelete is L1 on the list: arm, then confirm.
func alarmRequestDelete() {
	if alarmUI.tab != alarmTabList || alarmUI.cursor < 0 || alarmUI.cursor >= len(alarmStore.Alarms) {
		return
	}
	a := alarmStore.Alarms[alarmUI.cursor]
	if alarmDeleteArm.at.IsZero() || alarmDeleteArm.id != a.ID || time.Since(alarmDeleteArm.at) > alarmDeleteWindow {
		alarmDeleteArm.at, alarmDeleteArm.id = time.Now(), a.ID
		ShowToast(hintSentence("Press %s again to delete the ", "L1 / Del", "Delete")+
			fmt.Sprintf("%02d:%02d alarm", a.Hour, a.Minute), ToastKindWarning)
		PlayNavSound()
		return
	}
	alarmDeleteArm.at = time.Time{}
	alarmDeleteFocused()
	PlayBackSound()
}

func alarmToggleFocused() {
	if alarmUI.tab == alarmTabList && alarmUI.cursor >= 0 && alarmUI.cursor < len(alarmStore.Alarms) {
		a := &alarmStore.Alarms[alarmUI.cursor]
		a.Enabled = !a.Enabled
		if a.Enabled {
			// Switched back on a few minutes after its time, it must wait for
			// tomorrow rather than ring at once through the catch-up window.
			alarmArmFrom(a, time.Now())
		}
		alarmSave()
		PlayToggleSound()
	}
}

func alarmDeleteFocused() {
	if alarmUI.cursor >= 0 && alarmUI.cursor < len(alarmStore.Alarms) {
		alarmStore.Alarms = append(alarmStore.Alarms[:alarmUI.cursor], alarmStore.Alarms[alarmUI.cursor+1:]...)
		alarmSave()
		if alarmUI.cursor >= len(alarmStore.Alarms) && alarmUI.cursor > 0 {
			alarmUI.cursor--
		}
	}
}

func alarmSaveAlarm() {
	if alarmUI.editID >= 0 {
		// Update existing
		for i := range alarmStore.Alarms {
			if alarmStore.Alarms[i].ID == alarmUI.editID {
				alarmStore.Alarms[i].Hour = alarmUI.editHour
				alarmStore.Alarms[i].Minute = alarmUI.editMin
				alarmStore.Alarms[i].Label = alarmUI.editLabel
				alarmStore.Alarms[i].Sound = alarmSoundByID(alarmUI.editSound).ID
				alarmArmFrom(&alarmStore.Alarms[i], time.Now())
				break
			}
		}
	} else {
		// Create new
		alarmNextID++
		entry := alarmEntry{
			Hour:    alarmUI.editHour,
			Minute:  alarmUI.editMin,
			Label:   alarmUI.editLabel,
			Sound:   alarmSoundByID(alarmUI.editSound).ID,
			Enabled: true,
			ID:      alarmNextID,
		}
		alarmArmFrom(&entry, time.Now())
		alarmStore.Alarms = append(alarmStore.Alarms, entry)
		trophyEvent(tkAlarmSet)
	}
	// Sort by time
	sort.Slice(alarmStore.Alarms, func(i, j int) bool {
		if alarmStore.Alarms[i].Hour != alarmStore.Alarms[j].Hour {
			return alarmStore.Alarms[i].Hour < alarmStore.Alarms[j].Hour
		}
		return alarmStore.Alarms[i].Minute < alarmStore.Alarms[j].Minute
	})
	alarmSave()
}

// --- Alarm check (called from main loop) ---

func alarmCheck(config *Config, now time.Time) {
	// This is the main loop's once-a-second tick on every path (parked under
	// a game, dark panel, normal frame), so the UI-sound settings are re-read
	// here: a switch flipped in Settings applies within a second (sound.go).
	bootSfxBind(config)
	// Background music re-reads its settings and starts or stays away on the
	// same tick (bgmusic.go).
	bgmTick(config)
	// Network time or a new zone moved the clock: re-anchor first (clockjump.go).
	alarmClockJumpCheck(now)
	// A ring in progress keeps the idle screens away and ends itself if nobody
	// answers it.
	alarmRingTick(now)
	for i := range alarmStore.Alarms {
		a := &alarmStore.Alarms[i]
		if alarmDue(*a, now) {
			alarmMarkRang(a, now)
			alarmSave()
			// The ring takes over the screen (alarmring.go), not a toast under
			// whatever was open: an alarm painted over by a game is not an alarm.
			// One that comes due during another's ring takes that ring over.
			alarmTrigger(config, *a)
		}
	}
}

// alarmMarkRang records that a rings at now. FiredOn closes today's own ring,
// so it is only set once today's time has come: a snooze re-ring that lands
// after midnight (23:58 snoozed to 00:07) must not close the evening's 23:58
// ring. The snooze has done its job either way.
func alarmMarkRang(a *alarmEntry, now time.Time) {
	at := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
	if !now.Before(at) {
		a.FiredOn = alarmDate(now)
	}
	a.RangOn = alarmDate(now)
	a.SnoozeUntil = ""
}

// --- Rendering ---

func renderAlarmClock(renderer *sdl.Renderer, config *Config) {
	alarmUI.now = time.Now()

	// Reuse the shared backdrop instead of clearing to near-black: the alarm
	// screen is a normal scene, and a bare Clear() read as a broken black page.
	// It is Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

	// The editor has the whole page: its own preview of the time being set
	// replaces the live clock, the countdown and the calendar.
	if alarmUI.tab != alarmTabList {
		renderAlarmEditor(renderer, config)
		renderStatusBar(renderer, config)
		renderFooter(renderer, config)
		alarmBackRect = sceneTitleBackRect
		renderAlarmPicker(renderer, config)
		return
	}

	// The app's one bar owns the top of the screen; the clock starts under it.
	barH := headerBarHeight()
	font, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if bigFont == nil {
		bigFont = font
	}
	smallFont, _ := getCachedFont(config, "small")

	cx := screenWidth / 2

	// --- Large current time display ---
	timeBottom := barH + 10
	if bigFont != nil {
		timeStr := clockDigitsSeconds(config, alarmUI.now)
		tw, th, _ := bigFont.SizeUTF8(timeStr)
		tx := cx - int32(tw)/2
		ty := barH + 10
		timeBottom = ty + int32(th)

		// Glow behind time
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 25)
		renderer.FillRect(&sdl.Rect{X: tx - 12, Y: ty - 6, W: int32(tw) + 24, H: int32(th) + 12})

		renderText(renderer, config, bigFont, timeStr,
			ColorTextPrimary(), tx, ty)

		if smallFont != nil {
			dateStr := formatDateLong(config, alarmUI.now)
			dw, _, _ := smallFont.SizeUTF8(dateStr)
			renderText(renderer, config, smallFont, dateStr,
				WithAlpha(ColorTextTertiary(), 200),
				cx-int32(dw)/2, ty+int32(th)+8)
		}
	}

	// --- Next alarm countdown ---
	nextAlarm := alarmNextUpcoming()
	if nextAlarm != nil && smallFont != nil {
		remaining := time.Until(alarmTimeToday(nextAlarm))
		if remaining < 0 {
			remaining += 24 * time.Hour
		}
		h := int(remaining.Hours())
		m := int(remaining.Minutes()) % 60
		s := int(remaining.Seconds()) % 60
		countdown := fmt.Sprintf("Next alarm in %dh %dm %ds", h, m, s)
		cw, _, _ := smallFont.SizeUTF8(countdown)
		renderText(renderer, config, smallFont, countdown,
			sdl.Color{R: 140, G: 200, B: 140, A: 180},
			cx-int32(cw)/2, timeBottom+34)
	}

	// --- Mini calendar (top right, under the bar) ---
	renderMiniCalendar(renderer, config)

	// --- Alarm list or editor ---
	listX := alarmListX
	listY := alarmListTop()
	listW := alarmListW

	renderAlarmList(renderer, config, listX, listY, listW)

	// The shared bars last. The Back pill is the bar's own, so touch users get
	// the same control every other screen has instead of a bespoke text pill.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	alarmBackRect = sceneTitleBackRect
	// The music picker is modal over the editor.
	renderAlarmPicker(renderer, config)
}

func renderAlarmList(renderer *sdl.Renderer, config *Config, x, y, w int32) {
	smallFont, _ := getCachedFont(config, "small")
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	if len(alarmStore.Alarms) == 0 {
		if smallFont != nil {
			empty := hintSentence("No alarms set. Tap + Add Alarm or press %s.", "Y", "Add")
			ew, _, _ := smallFont.SizeUTF8(empty)
			renderText(renderer, config, smallFont, empty,
				WithAlpha(ColorTextTertiary(), 150),
				(screenWidth-int32(ew))/2, y+80)
		}
		renderAlarmAddRow(renderer, config, font, x, y+120, w)
		return
	}

	rowH := int32(64)
	for i, a := range alarmStore.Alarms {
		if y+int32(i)*rowH > screenHeight-80 {
			break
		}
		ry := y + int32(i)*rowH
		focused := i == alarmUI.cursor

		// Row background
		// Home's glass row: a disabled alarm is the dimmed glass, the cursor
		// row carries Home's ring (on its own edge: the list scrolls).
		drawGlassSurface(renderer, sdl.Rect{X: x, Y: ry, W: w, H: rowH - 4}, 12,
			glassState{Focused: focused, Disabled: !a.Enabled && !focused, NoLift: true})

		// Toggle indicator
		if a.Enabled {
			// Green dot
			fillCircle(renderer, x+20, ry+rowH/2-2, 5,
				sdl.Color{R: 80, G: 200, B: 120, A: 220})
		} else {
			// Gray dot
			fillCircle(renderer, x+20, ry+rowH/2-2, 5,
				sdl.Color{R: 60, G: 65, B: 75, A: 150})
		}

		// Time
		timeStr := clockDigitsHM(config, a.Hour, a.Minute)
		timeCol := ColorTextPrimary()
		if !a.Enabled {
			timeCol = WithAlpha(ColorTextTertiary(), 150)
		}
		renderText(renderer, config, font, timeStr, timeCol, x+40, ry+10)

		// Label
		if smallFont != nil && a.Label != "" {
			label := a.Label
			label = truncateRunes(label, 40)
			renderText(renderer, config, smallFont, label,
				WithAlpha(ColorTextSecondary(), 180),
				x+160, ry+20)
		}

		// Fired indicator: rang today (not merely set for a time already past).
		if a.RangOn == alarmDate(time.Now()) {
			fired := "[F]"
			if smallFont != nil {
				renderText(renderer, config, smallFont, fired,
					sdl.Color{R: 100, G: 200, B: 120, A: 200},
					x+w-30, ry+20)
			}
		}
	}

	// "Add new" row at bottom
	renderAlarmAddRow(renderer, config, font, x, y+int32(len(alarmStore.Alarms))*rowH, w)
}

// renderAlarmAddRow draws the tappable "+ Add Alarm" row and records its rect
// so the mouse handler can open the editor.
func renderAlarmAddRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w int32) {
	alarmAddRect = sdl.Rect{}
	if font == nil || y >= screenHeight-100 {
		return
	}
	addText := "+ Add Alarm"
	aw, ah, _ := font.SizeUTF8(addText)
	addCol := sdl.Color{R: 120, G: 160, B: 255, A: 180}
	alarmAddRect = sdl.Rect{X: x + (w-int32(aw))/2 - 16, Y: y, W: int32(aw) + 32, H: int32(ah) + 16}
	fillRoundedRect(renderer, alarmAddRect.X, alarmAddRect.Y, alarmAddRect.W, alarmAddRect.H, 10, WithAlpha(ColorInfo, 40))
	renderText(renderer, config, font, addText, addCol,
		x+(w-int32(aw))/2, y+8)
}

// handleAlarmMouseClick handles taps on the alarm screen: the Back pill, the
// alarm rows and the "+ Add Alarm" row. It reports whether the click was used.
func handleAlarmMouseClick(mx, my int32, config *Config) bool {
	// The music picker is modal: it takes every click while it is open.
	if alarmUI.pickOpen {
		alarmPickClick(mx, my)
		return true
	}
	if alarmBackRect.W > 0 && pointInRect(mx, my, alarmBackRect) {
		alarmBack()
		return true
	}
	if alarmUI.tab != alarmTabList {
		return alarmEditClick(mx, my)
	}
	if alarmAddRect.W > 0 && pointInRect(mx, my, alarmAddRect) {
		// The same new alarm the pad's Y starts — five minutes from now, the
		// hour carried — on the default ringtone, not the one the alarm edited
		// last happened to preview. (The tap used to take (minute+5)%60 alone,
		// which set 10:58 to 10:03.)
		alarmStartNew(time.Now())
		PlayActivateSound()
		return true
	}
	listTop := alarmListTop()
	if my < listTop || my >= listTop+int32(len(alarmStore.Alarms))*alarmRowH {
		return false
	}
	idx := int((my - listTop) / alarmRowH)
	if idx < 0 || idx >= len(alarmStore.Alarms) {
		return false
	}
	alarmUI.cursor = idx
	alarmSelect()
	return true
}

// --- Mini Calendar (top-right of alarm scene) ---

const (
	calCellW = 28
	calCellH = 22
	calPadX  = 8
	calPadY  = 6

	calHeaderH    = 28
	calDayHeaderH = 18
)

// renderMiniCalendar draws the mini month grid in the alarm scene's corner:
// under the shared header, against the right margin. The home screen places its
// own copy with renderMiniCalendarAt so the popover can sit under its clock.
func renderMiniCalendar(renderer *sdl.Renderer, config *Config) {
	cardW, _ := miniCalendarSize()
	x := screenWidth - cardW - 24
	if x < 24 {
		x = 24
	}
	renderMiniCalendarAt(renderer, config, x, headerBarHeight()+8)
}

// miniCalendarSize returns the card size of the mini month grid for the
// current month: the row count depends on how the month's days fall.
func miniCalendarSize() (int32, int32) {
	now := time.Now()
	year, month := now.Year(), now.Month()
	firstDay := time.Date(year, month, 1, 0, 0, 0, 0, now.Location()).Weekday()
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, now.Location()).Day()
	numRows := (int(firstDay) + daysInMonth + 6) / 7
	return int32(calCellW*7 + calPadX*2),
		int32(calHeaderH + calDayHeaderH + numRows*calCellH + calPadY*2)
}

// renderMiniCalendarAt draws the mini month grid with its top-left corner at
// (cx, cy).
func renderMiniCalendarAt(renderer *sdl.Renderer, config *Config, cx, cy int32) {
	// Always draw the current date: alarmUI.now is only refreshed while the
	// alarm scene renders, and the home scene reuses this widget too.
	now := time.Now()
	year, month, day := now.Date()
	weekday := now.Weekday()

	// Calendar card dimensions.
	cardW, cardH := miniCalendarSize()
	headerH := int32(calHeaderH)
	dayHeaderH := int32(calDayHeaderH)
	firstDay := time.Date(year, month, 1, 0, 0, 0, 0, now.Location()).Weekday()
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, now.Location()).Day()
	numRows := (int(firstDay) + daysInMonth + 6) / 7

	// Card background.
	drawOverlayCard(renderer, sdl.Rect{X: cx, Y: cy, W: cardW, H: cardH})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Month/year header.
	monthStr := now.Format("January 2006")
	mw, _, _ := font.SizeUTF8(monthStr)
	renderText(renderer, config, font, monthStr,
		WithAlpha(ColorTextPrimary(), 220),
		cx+(cardW-int32(mw))/2, cy+calPadY)

	// Day-of-week header: S M T W T F S.
	days := [7]string{"S", "M", "T", "W", "T", "F", "S"}
	for i, d := range days {
		dw, _, _ := font.SizeUTF8(d)
		dx := cx + calPadX + int32(i)*calCellW + (calCellW-int32(dw))/2
		dy := cy + headerH
		col := WithAlpha(ColorTextTertiary(), 170)
		if i == int(weekday) {
			col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
		}
		renderText(renderer, config, font, d, col, dx, dy)
	}

	// Day grid.
	gridY := cy + headerH + dayHeaderH
	dayNum := 1
	for row := 0; row < numRows; row++ {
		for col := 0; col < 7; col++ {
			cellX := cx + calPadX + int32(col)*calCellW
			cellY := gridY + int32(row)*calCellH

			if row == 0 && col < int(firstDay) || dayNum > daysInMonth {
				continue
			}

			isToday := dayNum == day

			// Today highlight.
			if isToday {
				r := int32(8)
				fillCircle(renderer, cellX+calCellW/2, cellY+calCellH/2, r,
					sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60})
				fillCircle(renderer, cellX+calCellW/2, cellY+calCellH/2, r-2,
					sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 120})
			}

			// Day number.
			numStr := fmt.Sprintf("%d", dayNum)
			nw, _, _ := font.SizeUTF8(numStr)
			nx := cellX + (calCellW-int32(nw))/2
			ny := cellY + (calCellH-14)/2
			dayCol := WithAlpha(ColorTextSecondary(), 200)
			if isToday {
				dayCol = ColorTextPrimary()
			} else if col == 0 || col == 6 {
				// Weekend tint.
				dayCol = sdl.Color{R: 180, G: 140, B: 140, A: 180}
			}
			renderText(renderer, config, font, numStr, dayCol, nx, ny)

			dayNum++
		}
	}
}

// --- Helpers ---

func alarmNextUpcoming() *alarmEntry {
	var best *alarmEntry
	bestDist := 24 * time.Hour
	for i := range alarmStore.Alarms {
		a := &alarmStore.Alarms[i]
		if !a.Enabled {
			continue
		}
		remaining := time.Until(alarmTimeToday(a))
		if remaining < 0 {
			remaining += 24 * time.Hour
		}
		if remaining < bestDist {
			bestDist = remaining
			best = a
		}
	}
	return best
}

func alarmTimeToday(a *alarmEntry) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(),
		a.Hour, a.Minute, 0, 0, now.Location())
}

// handleAlarmInput processes keyboard input for the alarm scene.
func handleAlarmInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if alarmUI.pickOpen {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			alarmPickMove(-1)
		case sdl.K_DOWN:
			alarmPickMove(1)
		case sdl.K_PAGEUP:
			alarmPickMove(-alarmPickMaxRows)
		case sdl.K_PAGEDOWN:
			alarmPickMove(alarmPickMaxRows)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			alarmPickChoose()
		case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
			alarmPickClose()
		}
		return
	}
	if alarmUI.tab != alarmTabList {
		alarmEditKey(e)
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		alarmNavigate(-1)
	case sdl.K_DOWN:
		alarmNavigate(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		alarmSelect()
	case sdl.K_x:
		// Once per press: a held key's auto-repeat flipped it on and off.
		if e.Repeat == 0 {
			alarmToggleFocused()
		}
	case sdl.K_y, sdl.K_n:
		// New alarm
		alarmStartNew(time.Now())
		PlayActivateSound()
	case sdl.K_DELETE:
		// The pad's L1: delete, pressed twice (the footer says "L1 / Del").
		// Two presses, not one held key: its auto-repeat would confirm.
		if e.Repeat == 0 {
			alarmRequestDelete()
		}
	case sdl.K_ESCAPE, sdl.K_b:
		alarmBack()
	}
}

// handleAlarmTextInput takes text that arrives with no key of its own on the
// Label row — the phone companion's, a non-Latin layout's, an input method's —
// and starts the label keyboard with it. While that keyboard is up it takes
// the text itself (main.go), so nothing lands twice.
func handleAlarmTextInput(e *sdl.TextInputEvent) {
	if e == nil || alarmUI.tab != alarmTabLabel || alarmUI.pickOpen || virtualKeyboardActive {
		return
	}
	// Space on the Minute row moves the focus here and then sends its " ":
	// that is the key's own character, not a label.
	text := e.GetText()
	if strings.TrimSpace(text) == "" {
		return
	}
	alarmOpenLabelKeyboard(false)
	vkInsertText(text, appConfig)
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlAlarm(e *sdl.ControllerButtonEvent) bool {
	// The music picker is modal: it owns the pad while it is open.
	if alarmUI.pickOpen {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			alarmPickMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			alarmPickMove(1)
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			alarmPickMove(-alarmPickMaxRows)
		case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			alarmPickMove(alarmPickMaxRows)
		case sdl.CONTROLLER_BUTTON_A:
			alarmPickChoose()
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			alarmPickClose()
		}
		return true
	}
	if alarmUI.tab != alarmTabList {
		return alarmEditPad(e.Button)
	}
	// Each handler below plays its own feedback sound; the extra Play* calls
	// this mapping used to add made every press tick twice.
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		alarmNavigate(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		alarmNavigate(1)
	case sdl.CONTROLLER_BUTTON_A:
		alarmSelect()
	case sdl.CONTROLLER_BUTTON_B:
		alarmBack()
	case sdl.CONTROLLER_BUTTON_Y:
		// Y is "New alarm" — the legend and the empty list both say so.
		alarmStartNew(time.Now())
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_X:
		alarmToggleFocused()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		alarmRequestDelete()
	default:
		// Left/Right included: the scene's own element navigation owns them.
		return false
	}
	return true
}

// Scene registration.
type alarmModule struct{ sceneBase }

func (alarmModule) render(r *sdl.Renderer, c *Config) bool { renderAlarmClock(r, c); return true }

func (alarmModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleAlarmInput(e, c)
	return true
}

func (alarmModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlAlarm(e)
}

func init() { registerScene(alarmModule{sceneBase{name: "Alarm"}}) }
