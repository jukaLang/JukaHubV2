package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Alarm Clock
// ──────────────────────────────────────────────────────────────────────

type alarmEntry struct {
	Hour    int    `json:"hour"`
	Minute  int    `json:"minute"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Fired   bool   `json:"fired"` // true after alarm rings today
	ID      int    `json:"id"`
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
	data, err := os.ReadFile("jukaalarm.json")
	if err != nil {
		alarmStore = alarmList{}
		return
	}
	var al alarmList
	if json.Unmarshal(data, &al) == nil {
		alarmStore = al
		for _, a := range al.Alarms {
			if a.ID >= alarmNextID {
				alarmNextID = a.ID + 1
			}
		}
	}
	// Reset "fired" flags at midnight.
	now := time.Now()
	if now.Hour() == 0 && now.Minute() == 0 {
		for i := range alarmStore.Alarms {
			alarmStore.Alarms[i].Fired = false
		}
	}
}

func alarmSave() {
	alarmStore.Alarms = append([]alarmEntry{}, alarmStore.Alarms...)
	data, err := json.MarshalIndent(alarmStore, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile("jukaalarm.json", data, 0644)
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
			alarmUI.tab = 1
			alarmUI.editHour = now.Hour()
			alarmUI.editMin = (now.Minute() + 5) % 60
			alarmUI.editLabel = ""
			alarmUI.editID = -1
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

func alarmToggleFocused() {
	if alarmUI.tab == 0 && alarmUI.cursor >= 0 && alarmUI.cursor < len(alarmStore.Alarms) {
		alarmStore.Alarms[alarmUI.cursor].Enabled = !alarmStore.Alarms[alarmUI.cursor].Enabled
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
				alarmStore.Alarms[i].Fired = false
				break
			}
		}
	} else {
		// Create new
		alarmNextID++
		alarmStore.Alarms = append(alarmStore.Alarms, alarmEntry{
			Hour:    alarmUI.editHour,
			Minute:  alarmUI.editMin,
			Label:   alarmUI.editLabel,
			Enabled: true,
			ID:      alarmNextID,
		})
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
		if !a.Enabled || a.Fired {
			continue
		}
		if now.Hour() == a.Hour && now.Minute() == a.Minute {
			a.Fired = true
			alarmSave()
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

	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if bigFont == nil {
		bigFont = font
	}
	smallFont, _ := getCachedFont(config, "small")

	cx := int32(640)

	// --- Large current time display ---
	if bigFont != nil {
		timeStr := alarmUI.now.Format("15:04:05")
		tw, th, _ := bigFont.SizeUTF8(timeStr)
		tx := cx - int32(tw)/2
		ty := int32(30)

		// Glow behind time
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 25)
		renderer.FillRect(&sdl.Rect{X: tx - 12, Y: ty - 6, W: int32(tw) + 24, H: int32(th) + 12})

		renderText(renderer, config, bigFont, timeStr,
			sdl.Color{R: 230, G: 240, B: 255, A: 255}, tx, ty)

		if smallFont != nil {
			dateStr := alarmUI.now.Format("Monday, January 2, 2006")
			dw, _, _ := smallFont.SizeUTF8(dateStr)
			renderText(renderer, config, smallFont, dateStr,
				sdl.Color{R: 120, G: 135, B: 160, A: 200},
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
			cx-int32(cw)/2, int32(110))
	}

	// --- Mini calendar (top right) ---
	renderMiniCalendar(renderer, config)

	// --- Alarm list or editor ---
	listX := int32(100)
	listY := int32(160)
	listW := int32(740)

	if alarmUI.tab == 0 {
		// List mode
		renderAlarmList(renderer, config, listX, listY, listW)
	} else {
		// Editor mode
		renderAlarmEditor(renderer, config, listX, listY, listW)
	}

	// Controls hint
	if smallFont != nil {
		controls := ""
		switch alarmUI.tab {
		case 0:
			controls = "D-Pad: Navigate | A: Edit | X: Toggle | Y: Add New | B: Back"
			if len(alarmStore.Alarms) > 0 {
				controls = "D-Pad: Navigate | A: Edit | X: Toggle | Y: Add New | Hold Y+X: Delete"
			}
		case 1:
			controls = "D-Pad: Change Hour | A: Next →"
		case 2:
			controls = "D-Pad: Change Minute | A: Next →"
		case 3:
			controls = "Type label | A: Save | B: Back"
		case 4:
			controls = "A: Confirm Delete | B: Cancel"
		}
		chw, _, _ := smallFont.SizeUTF8(controls)
		chx := (screenWidth - int32(chw)) / 2
		renderText(renderer, config, smallFont, controls,
			sdl.Color{R: 90, G: 105, B: 130, A: 140}, chx, screenHeight-40)
	}
}

func renderAlarmList(renderer *sdl.Renderer, config *Config, x, y, w int32) {
	smallFont, _ := getCachedFont(config, "small")
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	if len(alarmStore.Alarms) == 0 {
		if smallFont != nil {
			empty := "No alarms set. Press Y to add one."
			ew, _, _ := smallFont.SizeUTF8(empty)
			renderText(renderer, config, smallFont, empty,
				sdl.Color{R: 100, G: 115, B: 140, A: 150},
				(screenWidth-int32(ew))/2, y+80)
		}
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
		fill := ColorCard
		if focused {
			fill = ColorCardFocus
		}
		if !a.Enabled {
			fill = sdl.Color{R: 15, G: 18, B: 25, A: 200}
		}
		fillRoundedRect(renderer, x, ry, w, rowH-4, 12, fill)
		if focused {
			strokeRoundedRect(renderer, x, ry, w, rowH-4, 12, 3, ColorAccent)
		}

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
		timeCol := sdl.Color{R: 220, G: 230, B: 245, A: 255}
		if !a.Enabled {
			timeCol = sdl.Color{R: 100, G: 110, B: 130, A: 150}
		}
		renderText(renderer, config, font, timeStr, timeCol, x+40, ry+10)

		// Label
		if smallFont != nil && a.Label != "" {
			label := a.Label
			if len(label) > 40 {
				label = label[:40] + "…"
			}
			renderText(renderer, config, smallFont, label,
				sdl.Color{R: 140, G: 155, B: 180, A: 180},
				x+160, ry+20)
		}

		// Fired indicator
		if a.Fired {
			fired := "[F]"
			if smallFont != nil {
				renderText(renderer, config, smallFont, fired,
					sdl.Color{R: 100, G: 200, B: 120, A: 200},
					x+w-30, ry+20)
			}
		}
	}

	// "Add new" row at bottom
	addY := y + int32(len(alarmStore.Alarms))*rowH
	if addY < screenHeight-100 {
		addText := "+ Add Alarm"
		aw, _, _ := font.SizeUTF8(addText)
		addCol := sdl.Color{R: 120, G: 160, B: 255, A: 180}
		renderText(renderer, config, font, addText, addCol,
			x+(w-int32(aw))/2, addY+8)
	}
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
		sdl.Color{R: 200, G: 210, B: 230, A: 220},
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
	hourCol := sdl.Color{R: 200, G: 210, B: 230, A: 220}
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
			sdl.Color{R: 140, G: 155, B: 180, A: 180},
			screenWidth/2-int32(cw)/2, editY)
		_ = ch
	}

	// Minute
	minCol := sdl.Color{R: 200, G: 210, B: 230, A: 220}
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
			sdl.Color{R: 140, G: 155, B: 180, A: 200}, x+20, editY)

		editY += 28
		labelBg := sdl.Color{R: 20, G: 24, B: 34, A: 255}
		if alarmUI.tab == 3 {
			labelBg = sdl.Color{R: 30, G: 36, B: 50, A: 255}
		}
		fillRoundedRect(renderer, x+20, editY, w-40, 36, 8, labelBg)
		strokeRoundedRect(renderer, x+20, editY, w-40, 36, 8, 1, ColorBorder)

		displayLabel := alarmUI.editLabel
		if displayLabel == "" && alarmUI.tab != 3 {
			displayLabel = "(no label)"
		}
		if alarmUI.tab == 3 {
			displayLabel += "_" // cursor
		}
		renderText(renderer, config, smallFont, displayLabel,
			sdl.Color{R: 180, G: 190, B: 210, A: 220}, x+32, editY+8)
	}

	editY += 60

	// Day of week indicators
	if smallFont != nil {
		days := []string{"S", "M", "T", "W", "T", "F", "S"}
		today := alarmUI.now.Weekday()
		for i, d := range days {
			dx := x + 60 + int32(i)*40
			// Highlight today's column
			col := sdl.Color{R: 70, G: 80, B: 100, A: 150}
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
	calX     = 880
	calY     = 28
	calCellW = 28
	calCellH = 22
	calPadX  = 8
	calPadY  = 6
)

func renderMiniCalendar(renderer *sdl.Renderer, config *Config) {
	now := alarmUI.now
	year, month, day := now.Date()
	weekday := now.Weekday()

	// Calendar card dimensions.
	cardW := int32(calCellW*7 + calPadX*2)
	headerH := int32(28)
	dayHeaderH := int32(18)
	firstDay := time.Date(year, month, 1, 0, 0, 0, 0, now.Location()).Weekday()
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, now.Location()).Day()
	numRows := (int(firstDay) + daysInMonth + 6) / 7
	cardH := headerH + dayHeaderH + int32(numRows)*calCellH + calPadY*2

	// Card background.
	cx := int32(calX)
	cy := int32(calY)
	fillRoundedRect(renderer, cx, cy, cardW, cardH, 12, sdl.Color{R: 12, G: 16, B: 28, A: 230})
	strokeRoundedRect(renderer, cx, cy, cardW, cardH, 12, 1, ColorBorder)

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Month/year header.
	monthStr := now.Format("January 2006")
	mw, _, _ := font.SizeUTF8(monthStr)
	renderText(renderer, config, font, monthStr,
		sdl.Color{R: 200, G: 210, B: 230, A: 220},
		cx+(cardW-int32(mw))/2, cy+calPadY)

	// Day-of-week header: S M T W T F S.
	days := [7]string{"S", "M", "T", "W", "T", "F", "S"}
	for i, d := range days {
		dw, _, _ := font.SizeUTF8(d)
		dx := cx + calPadX + int32(i)*calCellW + (calCellW-int32(dw))/2
		dy := cy + headerH
		col := sdl.Color{R: 80, G: 90, B: 110, A: 180}
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
			dayCol := sdl.Color{R: 160, G: 170, B: 190, A: 200}
			if isToday {
				dayCol = sdl.Color{R: 255, G: 255, B: 255, A: 255}
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
			alarmUI.tab = 1
			alarmUI.editHour = alarmUI.now.Hour()
			alarmUI.editMin = (alarmUI.now.Minute() + 5) % 60
			alarmUI.editLabel = ""
			alarmUI.editID = -1
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
		case sdl.K_ESCAPE, sdl.K_b:
			alarmBack()
		case sdl.K_BACKSPACE:
			if len(alarmUI.editLabel) > 0 {
				alarmUI.editLabel = alarmUI.editLabel[:len(alarmUI.editLabel)-1]
			}
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
	for _, r := range string(e.Text[:]) {
		if r >= 32 && r <= 126 && len(alarmUI.editLabel) < 40 {
			alarmUI.editLabel += string(r)
		}
	}
}
