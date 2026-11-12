package main

// alarmedit.go — the alarm editor: one screen of rows, the way Settings reads.
//
// Up/Down walks the rows — Hour, Minute, Label, Ringtone, then the Save and
// Delete buttons — and Left/Right changes the focused value (a held direction
// repeats, navrepeat.go). A moves on from the time rows, opens the on-screen
// keyboard on Label and presses a button; Y saves from any row; B leaves, and
// with unsaved changes asks for a second press first. Every row answers a tap:
// the chevrons step, Label opens the keyboard, the buttons press.
//
// It replaces a step-through editor where A meant "next field" and B "previous
// field": the label could only be typed on a physical keyboard (the handheld
// has none, so it could not be edited at all), saving meant walking every
// field, and a day-of-week strip suggested repeat days the alarms do not have.

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The alarm screen's focus: the list, or one of the editor's rows.
const (
	alarmTabList   = 0
	alarmTabHour   = 1
	alarmTabMinute = 2
	alarmTabLabel  = 3
	alarmTabSound  = 4
	alarmTabSave   = 5
	alarmTabDelete = 6
)

// alarmLabelMax is the longest label, in characters.
const alarmLabelMax = 40

// alarmLabelVKTarget aims the on-screen keyboard at the label being edited
// (vkeyboard.go). Like the zone picker's search it lives outside the iota list.
const alarmLabelVKTarget vkTargetKind = 66

// alarmLabelBuffer is the label while the keyboard is up. It reaches the
// editor on Done only, so B on the keyboard leaves the label as it was.
var alarmLabelBuffer string

// alarmEditValues is what Save would store; the editor keeps the values it
// opened with to tell whether B is about to throw changes away.
type alarmEditValues struct {
	hour, minute int
	label, sound string
}

// alarmEditHit is one row's geometry for taps: the row, and its two step
// chevrons (zero on rows that do not step).
type alarmEditHit struct{ row, dec, inc sdl.Rect }

// alarmEditHits is written by the renderer and read by the mouse handler.
var alarmEditHits [alarmTabDelete + 1]alarmEditHit

// alarmDiscardArm is the first B on an editor with unsaved changes; a second
// one inside alarmDeleteWindow leaves without saving.
var alarmDiscardArm time.Time

// alarmDigits is a time typed on a keyboard's number keys: "0" then "7" on the
// Hour row is 07, a lone "7" is 7. A digit more than a second after the last
// one starts again.
var alarmDigits struct {
	tab int
	at  time.Time
}

// alarmEditing reports whether the editor (not the list) is open.
func alarmEditing() bool { return alarmUI.tab != alarmTabList }

// alarmEditNow is the editor's current values.
func alarmEditNow() alarmEditValues {
	return alarmEditValues{hour: alarmUI.editHour, minute: alarmUI.editMin,
		label: alarmUI.editLabel, sound: alarmSoundByID(alarmUI.editSound).ID}
}

// alarmEditChanged reports whether Save would store anything new.
func alarmEditChanged() bool { return alarmEditNow() != alarmUI.orig }

// alarmEditOpen opens the editor on a (id -1 for a new alarm), focus on Hour.
func alarmEditOpen(a alarmEntry, id int) {
	alarmUI.editHour, alarmUI.editMin = a.Hour, a.Minute
	alarmUI.editLabel = a.Label
	alarmUI.editSound = alarmSoundByID(a.Sound).ID
	alarmUI.editID = id
	alarmUI.orig = alarmEditNow()
	alarmUI.tab = alarmTabHour
	alarmDiscardArm = time.Time{}
	alarmDeleteArm.at = time.Time{}
	alarmDigits.at = time.Time{}
}

// alarmEditStops are the editor's rows in Up/Down order; Delete only for an
// alarm that exists.
func alarmEditStops() []int {
	stops := []int{alarmTabHour, alarmTabMinute, alarmTabLabel, alarmTabSound, alarmTabSave}
	if alarmUI.editID >= 0 {
		stops = append(stops, alarmTabDelete)
	}
	return stops
}

// alarmEditFocus puts the focus on row tab. Leaving the ringtone row ends its
// preview, and any half-done delete or discard is forgotten.
func alarmEditFocus(tab int) {
	if tab == alarmUI.tab {
		return
	}
	if alarmUI.tab == alarmTabSound {
		alarmPlayStop()
	}
	alarmUI.tab = tab
	alarmDeleteArm.at = time.Time{}
	alarmDiscardArm = time.Time{}
	alarmDigits.at = time.Time{}
}

// alarmEditMove is Up/Down: the previous or next row, stopping at the ends.
func alarmEditMove(dir int) {
	stops := alarmEditStops()
	at := 0
	for i, s := range stops {
		if s == alarmUI.tab {
			at = i
		}
	}
	next := at + dir
	if next < 0 || next >= len(stops) {
		return
	}
	alarmEditFocus(stops[next])
	PlayNavSound()
}

// alarmEditAdjust is Left/Right: change the focused value. Between the two
// buttons it moves from one to the other, the way they sit side by side.
func alarmEditAdjust(dir int) {
	switch alarmUI.tab {
	case alarmTabHour:
		alarmUI.editHour = (alarmUI.editHour + dir + 24) % 24
		PlayNavSound()
	case alarmTabMinute:
		alarmUI.editMin = (alarmUI.editMin + dir + 60) % 60
		PlayNavSound()
	case alarmTabSound:
		alarmSoundStep(dir)
	case alarmTabSave:
		if dir > 0 && alarmUI.editID >= 0 {
			alarmEditFocus(alarmTabDelete)
			PlayNavSound()
		}
	case alarmTabDelete:
		if dir < 0 {
			alarmEditFocus(alarmTabSave)
			PlayNavSound()
		}
	}
}

// alarmEditBigStep is L1/R1 on the time rows: ten minutes at a time, carrying
// into the hour, so 6:55 goes to 7:05.
func alarmEditBigStep(dir int) {
	if alarmUI.tab != alarmTabHour && alarmUI.tab != alarmTabMinute {
		return
	}
	total := alarmUI.editHour*60 + alarmUI.editMin + dir*10
	total = ((total % (24 * 60)) + 24*60) % (24 * 60)
	alarmUI.editHour, alarmUI.editMin = total/60, total%60
	PlayNavSound()
}

// alarmEditDigit types d on the Hour or Minute row (keyboard number keys).
func alarmEditDigit(d int) {
	limit := 24
	cur := &alarmUI.editHour
	switch alarmUI.tab {
	case alarmTabHour:
	case alarmTabMinute:
		limit, cur = 60, &alarmUI.editMin
	default:
		return
	}
	now := time.Now()
	v := d
	if alarmDigits.tab == alarmUI.tab && !alarmDigits.at.IsZero() && now.Sub(alarmDigits.at) < time.Second {
		if two := *cur*10 + d; two < limit {
			v = two
		}
	}
	*cur = v
	alarmDigits.tab, alarmDigits.at = alarmUI.tab, now
	PlayNavSound()
}

// alarmEditActivate is A on the focused row.
func alarmEditActivate() {
	switch alarmUI.tab {
	case alarmTabHour:
		alarmEditFocus(alarmTabMinute)
		PlayActivateSound()
	case alarmTabMinute:
		alarmEditFocus(alarmTabLabel)
		PlayActivateSound()
	case alarmTabLabel:
		alarmOpenLabelKeyboard(true)
		PlayActivateSound()
	case alarmTabSound:
		if alarmUI.editSound == alarmMusicPickID {
			alarmPickOpen(appConfig)
			return
		}
		alarmEditFocus(alarmTabSave)
		PlayActivateSound()
	case alarmTabSave:
		alarmEditSave()
	case alarmTabDelete:
		alarmEditDelete()
	}
}

// alarmEditSave stores the alarm and returns to the list with it focused.
func alarmEditSave() {
	if alarmUI.editSound == alarmMusicPickID {
		// "Choose from music…" is a way to a track, not a ringtone: open the
		// picker rather than save a stop that plays nothing.
		alarmEditFocus(alarmTabSound)
		alarmPickOpen(appConfig)
		return
	}
	alarmPlayStop()
	alarmSaveAlarm()
	id := alarmUI.editID
	if id < 0 {
		id = alarmNextID // alarmSaveAlarm gave the new alarm this id
	}
	for i, a := range alarmStore.Alarms {
		if a.ID == id {
			alarmUI.cursor = i
		}
	}
	at := clockDigitsHM(appConfig, alarmUI.editHour, alarmUI.editMin)
	alarmUI.tab = alarmTabList
	alarmDiscardArm = time.Time{}
	ShowToast("Alarm set for "+at+" · "+alarmEditWhen(time.Now(), alarmUI.editHour, alarmUI.editMin), ToastKindSuccess)
	PlaySuccessSound()
}

// alarmEditDelete is the Delete button, pressed twice like L1 on the list.
func alarmEditDelete() {
	id := alarmUI.editID
	if id < 0 {
		return
	}
	if alarmDeleteArm.at.IsZero() || alarmDeleteArm.id != id || time.Since(alarmDeleteArm.at) > alarmDeleteWindow {
		alarmDeleteArm.at, alarmDeleteArm.id = time.Now(), id
		ShowToast("Delete again within 3 seconds to remove this alarm", ToastKindWarning)
		PlayNavSound()
		return
	}
	alarmDeleteArm.at = time.Time{}
	alarmPlayStop()
	for i, a := range alarmStore.Alarms {
		if a.ID == id {
			alarmUI.cursor = i
			alarmDeleteFocused()
			break
		}
	}
	alarmUI.tab = alarmTabList
	ShowToast("Alarm deleted", ToastKindInfo)
	PlayBackSound()
}

// alarmEditBack is B in the editor: back to the list, asking once when there
// are changes Save has not stored.
func alarmEditBack() {
	if alarmEditChanged() && (alarmDiscardArm.IsZero() || time.Since(alarmDiscardArm) > alarmDeleteWindow) {
		alarmDiscardArm = time.Now()
		ShowToast(hintSentence2("Not saved yet: %s saves, %s again leaves without saving", "Y", "Save", "B", "Back"), ToastKindWarning)
		PlayNavSound()
		return
	}
	alarmDiscardArm = time.Time{}
	alarmPlayStop()
	alarmUI.tab = alarmTabList
	PlayBackSound()
}

// alarmEditClearLabel is X on the Label row.
func alarmEditClearLabel() {
	if alarmUI.editLabel == "" {
		return
	}
	alarmUI.editLabel = ""
	PlayBackSound()
}

// alarmEditWhen says when an alarm set for hour:minute next rings: "rings
// tomorrow · in 9 h 12 min".
func alarmEditWhen(now time.Time, hour, minute int) string {
	at := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	day := "today"
	if !now.Before(at) {
		at = at.AddDate(0, 0, 1)
		day = "tomorrow"
	}
	return "rings " + day + " · in " + alarmInWords(at.Sub(now))
}

// alarmInWords writes a wait, rounded up to the minute: "45 min", "9 h",
// "9 h 12 min".
func alarmInWords(d time.Duration) string {
	mins := int((d + time.Minute - 1) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	h, m := mins/60, mins%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// ── The label keyboard ────────────────────────────────────────────────────

// alarmOpenLabelKeyboard aims the on-screen keyboard at the label. selected
// holds the current label selected, so the first key replaces it (A on the
// row); without it typing goes on after the label (a physical key on the row).
func alarmOpenLabelKeyboard(selected bool) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = alarmLabelVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	alarmLabelBuffer = alarmUI.editLabel
	vkTextSelected = selected && alarmLabelBuffer != ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// alarmLabelSubmit is the keyboard's Done: the typed label, trimmed and cut to
// alarmLabelMax characters, becomes the editor's.
func alarmLabelSubmit(text string) {
	text = strings.Join(strings.Fields(text), " ")
	n := 0
	for i := range text {
		if n == alarmLabelMax {
			text = text[:i]
			break
		}
		n++
	}
	alarmUI.editLabel = text
}

// ── Input ─────────────────────────────────────────────────────────────────

// alarmEditKey handles a key in the editor (the music picker is handled
// before this).
func alarmEditKey(e *sdl.KeyboardEvent) {
	sym := e.Keysym.Sym
	if alarmUI.tab == alarmTabLabel {
		switch sym {
		case sdl.K_UP:
			alarmEditMove(-1)
		case sdl.K_DOWN, sdl.K_TAB:
			alarmEditMove(1)
		case sdl.K_RETURN, sdl.K_KP_ENTER:
			alarmOpenLabelKeyboard(true)
			PlayActivateSound()
		case sdl.K_ESCAPE:
			// Escape only: letters (b, y, x…) type into the label here.
			alarmEditBack()
		case sdl.K_BACKSPACE:
			alarmUI.editLabel = trimLastRune(alarmUI.editLabel)
		case sdl.K_DELETE:
			alarmEditClearLabel()
		default:
			// A printable key starts typing: the keyboard opens after the
			// label, and the character SDL sends next lands in it.
			if sym >= 32 && sym < 127 && e.Keysym.Mod&uint16(sdl.KMOD_CTRL|sdl.KMOD_ALT|sdl.KMOD_GUI) == 0 {
				alarmOpenLabelKeyboard(false)
			}
		}
		return
	}
	switch {
	case sym >= sdl.K_0 && sym <= sdl.K_9:
		alarmEditDigit(int(sym - sdl.K_0))
		return
	case sym >= sdl.K_KP_1 && sym <= sdl.K_KP_9:
		alarmEditDigit(int(sym-sdl.K_KP_1) + 1)
		return
	case sym == sdl.K_KP_0:
		alarmEditDigit(0)
		return
	}
	switch sym {
	case sdl.K_UP:
		alarmEditMove(-1)
	case sdl.K_DOWN, sdl.K_TAB:
		alarmEditMove(1)
	case sdl.K_LEFT:
		alarmEditAdjust(-1)
	case sdl.K_RIGHT:
		alarmEditAdjust(1)
	case sdl.K_PAGEDOWN:
		alarmEditBigStep(-1)
	case sdl.K_PAGEUP:
		alarmEditBigStep(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		alarmEditActivate()
	case sdl.K_y, sdl.K_s:
		alarmEditSave()
	case sdl.K_x, sdl.K_m:
		if alarmUI.tab == alarmTabSound {
			alarmPickOpen(appConfig)
		}
	case sdl.K_DELETE:
		if e.Repeat == 0 {
			alarmEditDelete()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		alarmEditBack()
	}
}

// alarmEditPad handles a pad button in the editor and reports whether it was
// used.
func alarmEditPad(button uint8) bool {
	switch sdl.GameControllerButton(button) {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		alarmEditMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		alarmEditMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		alarmEditAdjust(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		alarmEditAdjust(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		alarmEditBigStep(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		alarmEditBigStep(1)
	case sdl.CONTROLLER_BUTTON_A:
		alarmEditActivate()
	case sdl.CONTROLLER_BUTTON_B:
		alarmEditBack()
	case sdl.CONTROLLER_BUTTON_Y:
		alarmEditSave()
	case sdl.CONTROLLER_BUTTON_X:
		switch alarmUI.tab {
		case alarmTabSound:
			alarmPickOpen(appConfig)
		case alarmTabLabel:
			alarmEditClearLabel()
		}
	default:
		return false
	}
	return true
}

// alarmEditClick handles a tap in the editor and reports whether it was used.
func alarmEditClick(mx, my int32) bool {
	if alarmUI.tab == alarmTabSound && alarmRingtoneMusicRect.W > 0 && pointInRect(mx, my, alarmRingtoneMusicRect) {
		alarmPickOpen(appConfig)
		return true
	}
	for tab := alarmTabHour; tab <= alarmTabDelete; tab++ {
		h := alarmEditHits[tab]
		switch {
		case h.dec.W > 0 && pointInRect(mx, my, h.dec):
			alarmEditFocus(tab)
			alarmEditAdjust(-1)
		case h.inc.W > 0 && pointInRect(mx, my, h.inc):
			alarmEditFocus(tab)
			alarmEditAdjust(1)
		case h.row.W > 0 && pointInRect(mx, my, h.row):
			switch tab {
			case alarmTabLabel:
				alarmEditFocus(tab)
				alarmOpenLabelKeyboard(true)
				PlayActivateSound()
			case alarmTabSave:
				alarmEditFocus(tab)
				alarmEditSave()
			case alarmTabDelete:
				alarmEditFocus(tab)
				alarmEditDelete()
			default:
				if tab != alarmUI.tab {
					alarmEditFocus(tab)
					PlayNavSound()
				}
			}
		default:
			continue
		}
		return true
	}
	return false
}

// alarmEditFooterHints are the footer's keys for the focused row.
func alarmEditFooterHints() []FooterHint {
	switch alarmUI.tab {
	case alarmTabHour, alarmTabMinute:
		return []FooterHint{{"Up/Down", "Row"}, {"Left/Right", "Change"}, {"L1/R1", "±10 min"}, {"Y", "Save"}, {"B", "Back"}}
	case alarmTabLabel:
		if hintsUseKeyboard() {
			// Letters type into the label here, so x and y are not keys.
			return []FooterHint{{"Up/Down", "Row"}, {"Enter", "Type label"}, {"Del", "Clear"}, {"Esc", "Back"}}
		}
		return []FooterHint{{"Up/Down", "Row"}, {"A", "Type label"}, {"X", "Clear"}, {"Y", "Save"}, {"B", "Back"}}
	case alarmTabSound:
		return []FooterHint{{"Up/Down", "Row"}, {"Left/Right", "Ringtone"}, {"X", "Music"}, {"Y", "Save"}, {"B", "Back"}}
	case alarmTabDelete:
		return []FooterHint{{"Up/Down", "Row"}, {"A", "Delete"}, {"Y", "Save"}, {"B", "Back"}}
	}
	return []FooterHint{{"Up/Down", "Row"}, {"A", "Save"}, {"B", "Back"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// renderAlarmEditor draws the editor under the header: the time it is being
// set to, when that rings, the rows and the buttons.
func renderAlarmEditor(renderer *sdl.Renderer, config *Config) {
	font, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	small, _ := getCachedFont(config, "small")
	for i := range alarmEditHits {
		alarmEditHits[i] = alarmEditHit{}
	}
	alarmRingtoneMusicRect = sdl.Rect{}
	if font == nil || small == nil {
		return
	}
	if bigFont == nil {
		bigFont = font
	}
	rowH, gap := int32(52), int32(8)
	if screenHeight < 600 {
		rowH, gap = 42, 6
	}
	w := alarmListW
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	x := (screenWidth - w) / 2
	y := headerBarHeight() + 10

	if screenHeight >= 600 {
		// A small screen keeps the room for the rows: the preview says it all.
		title := "New alarm"
		if alarmUI.editID >= 0 {
			title = "Edit alarm"
		}
		tw, th := measureText(small, title)
		renderText(renderer, config, small, title, WithAlpha(ColorTextTertiary(), 220), (screenWidth-tw)/2, y)
		y += th + 2
	}

	preview := clockDigitsHM(config, alarmUI.editHour, alarmUI.editMin)
	pw, ph := measureText(bigFont, preview)
	renderText(renderer, config, bigFont, preview, ColorTextPrimary(), (screenWidth-pw)/2, y)
	y += ph

	when := alarmEditWhen(time.Now(), alarmUI.editHour, alarmUI.editMin)
	when = strings.ToUpper(when[:1]) + when[1:]
	ww, wh := measureText(small, when)
	renderText(renderer, config, small, when, sdl.Color{R: 140, G: 200, B: 140, A: 200}, (screenWidth-ww)/2, y+2)
	y += wh + 14

	hour := fmt.Sprintf("%02d", alarmUI.editHour)
	if clockHour12(config) {
		h, suffix := alarmUI.editHour%12, "AM"
		if alarmUI.editHour >= 12 {
			suffix = "PM"
		}
		if h == 0 {
			h = 12
		}
		hour = fmt.Sprintf("%d %s", h, suffix)
	}
	row := func(tab int, caption, value string, step, dim bool) {
		alarmDrawEditRow(renderer, config, font, small, tab, sdl.Rect{X: x, Y: y, W: w, H: rowH}, caption, value, step, dim)
		y += rowH + gap
	}
	row(alarmTabHour, "Hour", hour, true, false)
	row(alarmTabMinute, "Minute", fmt.Sprintf("%02d", alarmUI.editMin), true, false)
	if alarmUI.editLabel == "" {
		row(alarmTabLabel, "Label", "None", false, true)
	} else {
		row(alarmTabLabel, "Label", alarmUI.editLabel, false, false)
	}
	row(alarmTabSound, "Ringtone", alarmSoundName(alarmUI.editSound), true, false)

	y += 4
	btnH := rowH - 6
	if alarmUI.editID >= 0 {
		half := (w - gap) / 2
		save := sdl.Rect{X: x, Y: y, W: half, H: btnH}
		del := sdl.Rect{X: x + half + gap, Y: y, W: w - half - gap, H: btnH}
		drawGlassButton(renderer, config, save, "Save", font, WithAlpha(glassAccent(), 60), glassState{Focused: alarmUI.tab == alarmTabSave})
		drawGlassButton(renderer, config, del, "Delete", font, sdl.Color{R: 220, G: 80, B: 80, A: 60}, glassState{Focused: alarmUI.tab == alarmTabDelete})
		alarmEditHits[alarmTabSave].row, alarmEditHits[alarmTabDelete].row = save, del
	} else {
		bw := w / 2
		save := sdl.Rect{X: x + (w-bw)/2, Y: y, W: bw, H: btnH}
		drawGlassButton(renderer, config, save, "Save", font, WithAlpha(glassAccent(), 60), glassState{Focused: alarmUI.tab == alarmTabSave})
		alarmEditHits[alarmTabSave].row = save
	}
}

// alarmDrawEditRow paints one editor row: the caption on the left, the value
// in the right part — between chevrons on a row that steps — and the row's own
// key badge while it has the focus.
func alarmDrawEditRow(renderer *sdl.Renderer, config *Config, font, small *ttf.Font, tab int, r sdl.Rect, caption, value string, step, dim bool) {
	focused := alarmUI.tab == tab
	drawGlassSurface(renderer, r, 10, glassState{Focused: focused, NoLift: true})
	hit := alarmEditHit{row: r}
	cw, ch := measureText(small, caption)
	capInk := WithAlpha(ColorTextSecondary(), 210)
	if focused {
		capInk = ColorTextPrimary()
	}
	renderText(renderer, config, small, caption, capInk, r.X+18, r.Y+(r.H-ch)/2)

	valueL := r.X + r.W*2/5
	if floor := r.X + 18 + cw + 24; valueL < floor {
		valueL = floor
	}
	valueR := r.X + r.W - 16

	// The row's key, where A or X does something other than the footer's
	// general keys: X for music on Ringtone, A to type on Label.
	badgeKey, badgeLabel := "", ""
	switch {
	case focused && tab == alarmTabSound:
		badgeKey, badgeLabel = "X", "Music"
	case focused && tab == alarmTabLabel:
		badgeKey, badgeLabel = "A", "Type"
	}
	if badgeKey != "" {
		bw, bh := measureText(small, hintKeyFor(badgeKey, badgeLabel))
		lw, _ := measureText(small, badgeLabel)
		badge := sdl.Rect{W: bw + 20, H: bh + 6}
		badge.Y = r.Y + (r.H-badge.H)/2
		if tab == alarmTabSound {
			// Between the caption and the chevrons, if it fits there.
			badge.X = r.X + 18 + cw + 14
			if badge.X+badge.W+8+lw+12 <= valueL {
				drawGlassHintBadge(renderer, config, small, badge, badgeKey, badgeLabel, badge.X+badge.W+8, badge.Y+3)
				alarmRingtoneMusicRect = sdl.Rect{X: badge.X - 6, Y: r.Y, W: badge.W + 8 + lw + 12, H: r.H}
			}
		} else {
			badge.X = valueR - lw - 8 - badge.W
			drawGlassHintBadge(renderer, config, small, badge, badgeKey, badgeLabel, badge.X+badge.W+8, badge.Y+3)
			valueR = badge.X - 12
		}
	}

	ink := WithAlpha(ColorTextPrimary(), 235)
	if dim {
		ink = WithAlpha(ColorTextTertiary(), 170)
	}
	if step {
		arrow := ColorTextTertiary()
		if focused {
			arrow = glassAccent()
		}
		alarmDrawChevron(renderer, valueL+10, r.Y+r.H/2, 6, -1, arrow)
		alarmDrawChevron(renderer, valueR-10, r.Y+r.H/2, 6, 1, arrow)
		hit.dec = sdl.Rect{X: valueL - 10, Y: r.Y, W: 48, H: r.H}
		hit.inc = sdl.Rect{X: valueR - 38, Y: r.Y, W: 48, H: r.H}
		text := ellipsize(font, value, valueR-valueL-64)
		vw, vh := measureText(font, text)
		renderText(renderer, config, font, text, ink, valueL+(valueR-valueL-vw)/2, r.Y+(r.H-vh)/2)
	} else {
		text := ellipsize(font, value, valueR-valueL)
		_, vh := measureText(font, text)
		renderText(renderer, config, font, text, ink, valueL, r.Y+(r.H-vh)/2)
	}
	alarmEditHits[tab] = hit
}
