package main

import (
	"encoding/json"
	"fmt"
	"sort"
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
	ID     int    `json:"id"`
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
	if !a.Enabled || a.firedToday(now) {
		return false
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
	return !now.Before(at) && now.Sub(at) < alarmCatchUp
}

// alarmArmFrom marks an alarm set (or reset) at now: a time already past today
// starts tomorrow instead of ringing at once through the catch-up window.
func alarmArmFrom(a *alarmEntry, now time.Time) {
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
	alarmStore    alarmList
	alarmNextID   int
	alarmMutating bool // prevents re-rendering during alarm editing
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
	tab       int // 0=list, 1=set-hour, 2=set-min, 3=set-label, 4=set-save
	editHour  int
	editMin   int
	editLabel string
	editID    int       // -1 = new, >=0 = editing existing
	now       time.Time // cached current time (updated each frame)
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

func alarmNavigate(dr int) {
	if alarmUI.tab == 0 {
		// List mode
		total := len(alarmStore.Alarms)
		if total == 0 {
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
	} else if alarmUI.tab >= 1 && alarmUI.tab <= 3 {
		// Edit mode: adjust value
		switch alarmUI.tab {
		case 1: // hour
			alarmUI.editHour = (alarmUI.editHour + dr + 24) % 24
			PlayNavSound()
		case 2: // minute
			alarmUI.editMin = (alarmUI.editMin + dr + 60) % 60
			PlayNavSound()
		}
	}
}

func alarmSelect() {
	now := time.Now()
	switch alarmUI.tab {
	case 0: // List
		if len(alarmStore.Alarms) == 0 {
			// Start creating a new alarm
			alarmStartNew(now)
			PlayActivateSound()
			return
		}
		// Enter edit mode for the focused alarm
		if alarmUI.cursor >= 0 && alarmUI.cursor < len(alarmStore.Alarms) {
			a := alarmStore.Alarms[alarmUI.cursor]
			alarmUI.editHour = a.Hour
			alarmUI.editMin = a.Minute
			alarmUI.editLabel = a.Label
			alarmUI.editID = a.ID
			alarmUI.tab = 1
			PlayActivateSound()
		}
	case 1: // Hour → advance to minute
		alarmUI.tab = 2
		PlayActivateSound()
	case 2: // Minute → advance to label
		alarmUI.tab = 3
		alarmUI.cursor = 0 // label character cursor
		PlayActivateSound()
	case 3: // Label → save
		alarmSaveAlarm()
		alarmUI.tab = 0
		PlaySuccessSound()
	case 4: // Confirm delete
		alarmDeleteFocused()
		alarmUI.tab = 0
		PlayActivateSound()
	}
}

func alarmBack() {
	if alarmUI.tab > 0 && alarmUI.tab < 4 {
		alarmUI.tab--
		PlayBackSound()
	} else if alarmUI.tab == 0 {
		goBackScene(appConfig)
		PlayBackSound()
	}
}

// alarmStartNew opens the editor on a new alarm five minutes from now. The
// hour follows the minutes past the top of the hour: (minute+5)%60 alone set
// 10:58 to 10:03, a time already gone.
func alarmStartNew(now time.Time) {
	soon := now.Add(5 * time.Minute)
	alarmUI.tab = 1
	alarmUI.editHour = soon.Hour()
	alarmUI.editMin = soon.Minute()
	alarmUI.editLabel = ""
	alarmUI.editID = -1
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
	if alarmUI.tab != 0 || alarmUI.cursor < 0 || alarmUI.cursor >= len(alarmStore.Alarms) {
		return
	}
	a := alarmStore.Alarms[alarmUI.cursor]
	if alarmDeleteArm.at.IsZero() || alarmDeleteArm.id != a.ID || time.Since(alarmDeleteArm.at) > alarmDeleteWindow {
		alarmDeleteArm.at, alarmDeleteArm.id = time.Now(), a.ID
		ShowToast(fmt.Sprintf("Press L1 again to delete the %02d:%02d alarm", a.Hour, a.Minute), ToastKindWarning)
		PlayNavSound()
		return
	}
	alarmDeleteArm.at = time.Time{}
	alarmDeleteFocused()
	PlayBackSound()
}

func alarmToggleFocused() {
	if alarmUI.tab == 0 && alarmUI.cursor >= 0 && alarmUI.cursor < len(alarmStore.Alarms) {
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
			Enabled: true,
			ID:      alarmNextID,
		}
		alarmArmFrom(&entry, time.Now())
		alarmStore.Alarms = append(alarmStore.Alarms, entry)
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

func alarmCheck(now time.Time) {
	for i := range alarmStore.Alarms {
		a := &alarmStore.Alarms[i]
		if alarmDue(*a, now) {
			a.FiredOn = alarmDate(now)
			a.RangOn = a.FiredOn
			alarmSave()
			// A darked ambient panel is the common way to sleep with an alarm
			// armed, so the alarm has to bring the screen back with it.
			ambientNotifyInput()
			// Play alarm sound: ascending arpeggio
			go alarmRing()
			label := a.Label
			if label == "" {
				label = fmt.Sprintf("%02d:%02d", a.Hour, a.Minute)
			}
			showToast("[!] Alarm: "+label, ToastSuccess())
		}
	}
}

func alarmRing() {
	// Play a pleasant ascending arpeggio, 3 repetitions.
	for rep := 0; rep < 3; rep++ {
		for _, freq := range []float64{523.25, 659.25, 783.99, 1046.5} {
			playTone(freq, 180, 0.5)
			time.Sleep(220 * time.Millisecond)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// --- Rendering ---

func renderAlarmClock(renderer *sdl.Renderer, config *Config) {
	alarmUI.now = time.Now()

	// Reuse the shared backdrop instead of clearing to near-black: the alarm
	// screen is a normal scene, and a bare Clear() read as a broken black page.
	// It is Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

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
		timeStr := alarmUI.now.Format("15:04:05")
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
			dateStr := alarmUI.now.Format("Monday, January 2, 2006")
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

	if alarmUI.tab == 0 {
		// List mode
		renderAlarmList(renderer, config, listX, listY, listW)
	} else {
		// Editor mode
		renderAlarmEditor(renderer, config, listX, listY, listW)
	}

	// The shared bars last. The Back pill is the bar's own, so touch users get
	// the same control every other screen has instead of a bespoke text pill.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	alarmBackRect = sceneTitleBackRect
}

func renderAlarmList(renderer *sdl.Renderer, config *Config, x, y, w int32) {
	smallFont, _ := getCachedFont(config, "small")
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	if len(alarmStore.Alarms) == 0 {
		if smallFont != nil {
			empty := "No alarms set. Tap + Add Alarm or press Y."
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
		timeStr := fmt.Sprintf("%02d:%02d", a.Hour, a.Minute)
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
	if alarmBackRect.W > 0 && pointInRect(mx, my, alarmBackRect) {
		alarmBack()
		return true
	}
	if alarmUI.tab != 0 {
		return false
	}
	if alarmAddRect.W > 0 && pointInRect(mx, my, alarmAddRect) {
		now := time.Now()
		alarmUI.tab = 1
		alarmUI.editHour = now.Hour()
		alarmUI.editMin = (now.Minute() + 5) % 60
		alarmUI.editLabel = ""
		alarmUI.editID = -1
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

func renderAlarmEditor(renderer *sdl.Renderer, config *Config, x, y, w int32) {
	font, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	smallFont, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	editY := y + 40

	// Title
	title := "Set Alarm"
	if alarmUI.editID >= 0 {
		title = "Edit Alarm"
	}
	tw, _, _ := font.SizeUTF8(title)
	renderText(renderer, config, font, title,
		WithAlpha(ColorTextPrimary(), 220),
		(screenWidth-int32(tw))/2, editY)

	editY += 50

	// Hour and minute display
	hourStr := fmt.Sprintf("%02d", alarmUI.editHour)
	minStr := fmt.Sprintf("%02d", alarmUI.editMin)
	colonStr := ":"

	if bigFont == nil {
		bigFont = font
	}

	// Hour
	hourCol := WithAlpha(ColorTextPrimary(), 220)
	if alarmUI.tab == 1 {
		hourCol = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255}
	}
	hw, hh, _ := bigFont.SizeUTF8(hourStr)
	hx := screenWidth/2 - int32(hw) - 30
	hy := editY
	if alarmUI.tab == 1 {
		// Highlight background
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 40)
		renderer.FillRect(&sdl.Rect{X: hx - 8, Y: hy - 4, W: int32(hw) + 16, H: int32(hh) + 8})
	}
	renderText(renderer, config, bigFont, hourStr, hourCol, hx, hy)

	// Colon (blinking)
	if alarmUI.now.Second()%2 == 0 {
		cw, ch, _ := bigFont.SizeUTF8(colonStr)
		renderText(renderer, config, bigFont, colonStr,
			WithAlpha(ColorTextSecondary(), 180),
			screenWidth/2-int32(cw)/2, editY)
		_ = ch
	}

	// Minute
	minCol := WithAlpha(ColorTextPrimary(), 220)
	if alarmUI.tab == 2 {
		minCol = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255}
	}
	mw, mh, _ := bigFont.SizeUTF8(minStr)
	mx := screenWidth/2 + 30
	if alarmUI.tab == 2 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 40)
		renderer.FillRect(&sdl.Rect{X: mx - 8, Y: hy - 4, W: int32(mw) + 16, H: int32(mh) + 8})
	}
	renderText(renderer, config, bigFont, minStr, minCol, mx, hy)

	editY += 80

	// Label input
	if smallFont != nil {
		labelTitle := "Label:"
		renderText(renderer, config, smallFont, labelTitle,
			WithAlpha(ColorTextSecondary(), 200), x+20, editY)

		editY += 28
		// The label field is a scene input: recessed glass, the accent edge
		// while it is the field being edited.
		labelBg, labelEdge, labelEdgeW := glassA(10), glassA(26), int32(1)
		if alarmUI.tab == 3 {
			labelBg, labelEdge, labelEdgeW = glassFillFocus(), glassAccent(), 2
		}
		fillRoundedRect(renderer, x+20, editY, w-40, 36, 8, labelBg)
		strokeRoundedRect(renderer, x+20, editY, w-40, 36, 8, labelEdgeW, labelEdge)

		displayLabel := alarmUI.editLabel
		if displayLabel == "" && alarmUI.tab != 3 {
			displayLabel = "(no label)"
		}
		if alarmUI.tab == 3 {
			displayLabel += "_" // cursor
		}
		renderText(renderer, config, smallFont, displayLabel,
			WithAlpha(ColorTextSecondary(), 220), x+32, editY+8)
	}

	editY += 60

	// Day of week indicators
	if smallFont != nil {
		days := []string{"S", "M", "T", "W", "T", "F", "S"}
		today := alarmUI.now.Weekday()
		for i, d := range days {
			dx := x + 60 + int32(i)*40
			// Highlight today's column
			col := WithAlpha(ColorTextTertiary(), 150)
			if i == int(today) {
				col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 200}
			}
			dw, _, _ := smallFont.SizeUTF8(d)
			renderText(renderer, config, smallFont, d, col,
				dx-int32(dw)/2, editY)
		}
	}
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

// handleAlarmInput processes keyboard/controller input for the alarm scene.
func handleAlarmInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch alarmUI.tab {
	case 0: // List
		switch e.Keysym.Sym {
		case sdl.K_UP:
			alarmNavigate(-1)
		case sdl.K_DOWN:
			alarmNavigate(1)
		case sdl.K_RETURN, sdl.K_SPACE:
			alarmSelect()
		case sdl.K_x:
			alarmToggleFocused()
		case sdl.K_y, sdl.K_n:
			// New alarm
			alarmStartNew(time.Now())
			PlayActivateSound()
		case sdl.K_ESCAPE, sdl.K_b:
			alarmBack()
		}
	case 1: // Hour
		switch e.Keysym.Sym {
		case sdl.K_UP:
			alarmNavigate(-1)
		case sdl.K_DOWN:
			alarmNavigate(1)
		case sdl.K_RETURN, sdl.K_SPACE:
			alarmSelect()
		case sdl.K_ESCAPE, sdl.K_b:
			alarmBack()
		}
	case 2: // Minute
		switch e.Keysym.Sym {
		case sdl.K_UP:
			alarmNavigate(-1)
		case sdl.K_DOWN:
			alarmNavigate(1)
		case sdl.K_RETURN, sdl.K_SPACE:
			alarmSelect()
		case sdl.K_ESCAPE, sdl.K_b:
			alarmBack()
		}
	case 3: // Label
		switch e.Keysym.Sym {
		case sdl.K_RETURN, sdl.K_SPACE:
			alarmSelect()
		case sdl.K_ESCAPE:
			// Escape only: B is the first letter of half the labels, so it
			// cannot also be the way out of the field.
			alarmBack()
		case sdl.K_BACKSPACE:
			alarmUI.editLabel = trimLastRune(alarmUI.editLabel)
		}
	case 4: // Confirm delete
		switch e.Keysym.Sym {
		case sdl.K_RETURN, sdl.K_SPACE:
			alarmSelect()
		case sdl.K_ESCAPE, sdl.K_b:
			alarmUI.tab = 0
			PlayBackSound()
		}
	}
}

// handleAlarmTextInput processes text input for the label field.
func handleAlarmTextInput(e *sdl.TextInputEvent) {
	if alarmUI.tab != 3 {
		return
	}
	for _, r := range e.GetText() {
		if r >= 32 && r <= 126 && len(alarmUI.editLabel) < 40 {
			alarmUI.editLabel += string(r)
		}
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlAlarm(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		alarmNavigate(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		alarmNavigate(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		alarmSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B:
		alarmBack()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_Y:
		// Y is "New alarm" — the legend and the empty list both say so; it
		// used to reset the screen instead.
		if alarmUI.tab == 0 {
			alarmStartNew(time.Now())
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_X:
		alarmToggleFocused()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		alarmRequestDelete()
	default:
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
