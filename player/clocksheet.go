package main

// clocksheet.go — Settings → Date & time → Set date & time: the clock set by
// hand, where JukaHub is the one that sets it (the handheld, as root:
// clockSetSupported). Elsewhere the row opens the system's own page.
//
// The sheet reads like the alarm editor: Up/Down walks Year, Month, Day,
// Hour and Minute and then the Set and Cancel buttons, Left/Right changes the
// focused value (L1/R1 by ten on Year and Minute), Y sets the clock from any
// row and B closes without touching it. Setting it by hand turns "Set time
// automatically" off — otherwise the next hourly sync would undo it — and
// says so. The seconds start at zero.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The sheet's focus stops, top to bottom.
const (
	clockFieldYear = iota
	clockFieldMonth
	clockFieldDay
	clockFieldHour
	clockFieldMinute
	clockFieldSet
	clockFieldCancel
)

// The years the sheet offers: a clock outside these is certainly wrong.
const (
	clockSheetMinYear = 2020
	clockSheetMaxYear = 2099
)

// clockSheet is the open sheet: the values being set, the focus, and the
// geometry the renderer leaves for taps.
var clockSheet struct {
	open                           bool
	year, month, day, hour, minute int
	focus                          int
	card                           sdl.Rect
	hits                           [clockFieldCancel + 1]alarmEditHit
}

// clockSheetOpen opens the sheet on the clock's current reading, the focus on
// Hour (the usual fix).
func clockSheetOpen() {
	now := time.Now()
	clockSheet.open = true
	clockSheet.year, clockSheet.month, clockSheet.day = now.Year(), int(now.Month()), now.Day()
	clockSheet.hour, clockSheet.minute = now.Hour(), now.Minute()
	if clockSheet.year < clockSheetMinYear || clockSheet.year > clockSheetMaxYear {
		// A clock that lost its date entirely (1970) starts at the floor.
		clockSheet.year = clockSheetMinYear
	}
	clockSheet.focus = clockFieldHour
	PlayActivateSound()
}

// clockSheetClose dismisses the sheet without setting anything.
func clockSheetClose() {
	if !clockSheet.open {
		return
	}
	clockSheet.open = false
	PlayBackSound()
}

// clockSheetHubEnter is the hub's new-visit hook: a sheet left open on an
// earlier visit does not reappear.
func clockSheetHubEnter() { clockSheet.open = false }

// clockSheetDaysIn is the number of days in month of year.
func clockSheetDaysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// clockSheetWrap steps v by d inside [lo, hi], wrapping at the ends.
func clockSheetWrap(v, d, lo, hi int) int {
	n := hi - lo + 1
	return lo + ((v-lo+d)%n+n)%n
}

// clockSheetStep changes the focused value by dir (ten times that on Year and
// Minute when big). Between the buttons Left/Right moves from one to the other.
func clockSheetStep(dir int, big bool) {
	d := dir
	if big {
		d = dir * 10
	}
	switch clockSheet.focus {
	case clockFieldYear:
		y := clockSheet.year + d
		if y < clockSheetMinYear {
			y = clockSheetMinYear
		}
		if y > clockSheetMaxYear {
			y = clockSheetMaxYear
		}
		clockSheet.year = y
	case clockFieldMonth:
		clockSheet.month = clockSheetWrap(clockSheet.month, dir, 1, 12)
	case clockFieldDay:
		clockSheet.day = clockSheetWrap(clockSheet.day, dir, 1, clockSheetDaysIn(clockSheet.year, clockSheet.month))
	case clockFieldHour:
		clockSheet.hour = clockSheetWrap(clockSheet.hour, dir, 0, 23)
	case clockFieldMinute:
		clockSheet.minute = clockSheetWrap(clockSheet.minute, d, 0, 59)
	case clockFieldSet:
		if dir > 0 && !big {
			clockSheet.focus = clockFieldCancel
		}
	case clockFieldCancel:
		if dir < 0 && !big {
			clockSheet.focus = clockFieldSet
		}
	default:
		return
	}
	// 31 March to February is 28 or 29 February, not 3 March.
	if dim := clockSheetDaysIn(clockSheet.year, clockSheet.month); clockSheet.day > dim {
		clockSheet.day = dim
	}
	PlayNavSound()
}

// clockSheetMove is Up/Down between the rows; the two buttons share a row.
func clockSheetMove(dir int) {
	f := clockSheet.focus
	if f == clockFieldCancel {
		f = clockFieldSet
	}
	f += dir
	if f < clockFieldYear || f > clockFieldSet {
		return
	}
	clockSheet.focus = f
	PlayNavSound()
}

// clockSheetActivate is A: on a value, the next row; on a button, the button.
func clockSheetActivate(c *Config) {
	switch clockSheet.focus {
	case clockFieldSet:
		clockSheetApply(c)
	case clockFieldCancel:
		clockSheetClose()
	default:
		clockSheet.focus++
		PlayNavSound()
	}
}

// clockSheetTime is the moment the sheet would set, in the zone in force.
func clockSheetTime() time.Time {
	return time.Date(clockSheet.year, time.Month(clockSheet.month), clockSheet.day,
		clockSheet.hour, clockSheet.minute, 0, 0, time.Local)
}

// clockSheetApply sets the system clock to the sheet's time.
func clockSheetApply(c *Config) {
	if !clockSetSupported() {
		clockSheet.open = false
		ShowToast("This system sets its own clock", ToastKindInfo)
		return
	}
	t := clockSheetTime()
	// Off first: the next sync would put the network's time back, and one
	// already measuring checks the switch before it sets (ntpsync.go).
	wasAuto := netTimeEnabledIn(c)
	netTimeSetEnabled(false)
	now := time.Now()
	shift := t.Sub(now)
	_, offNow := now.Zone()
	_, offThen := t.Zone()
	if err := clockSetSystem(t); err != nil {
		netTimeSetEnabled(wasAuto)
		logWarn("clock", "clock not set by hand: %v", err)
		ShowToast("The clock could not be set: "+err.Error(), ToastKindError)
		PlayErrorSound()
		return
	}
	// At once, before anything slow: the alarms and the playtime day
	// re-anchor by this shift, the wall clock by it and any change of UTC
	// offset it crossed (a daylight-saving boundary; clockjump.go).
	clockJumpNotify(shift, shift+time.Duration(offThen-offNow)*time.Second)
	// The last network check no longer describes this clock.
	clockNoteCheck(clockCheck{})
	clockSheet.open = false
	setHub.dirty = true
	msg := "Clock set to " + formatDateTime(c, t)
	if wasAuto {
		setStoreBool(c, netTimeKey, false)
		msg += " · Set time automatically is off"
	}
	logInfo("clock", "clock set by hand to %s (moved %s)", t.Format(time.RFC3339), shift.Round(time.Second))
	at := time.Now()
	go clockRecordSave(clockRecord{At: at, How: "set by hand", OffsetMS: shift.Milliseconds()})
	ShowToast(msg, ToastKindSuccess)
	PlaySuccessSound()
}

// ── Input ─────────────────────────────────────────────────────────────────

// clockSheetKey handles a key while the sheet is open.
func clockSheetKey(e *sdl.KeyboardEvent, c *Config) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		clockSheetMove(-1)
	case sdl.K_DOWN, sdl.K_TAB:
		clockSheetMove(1)
	case sdl.K_LEFT:
		clockSheetStep(-1, false)
	case sdl.K_RIGHT:
		clockSheetStep(1, false)
	case sdl.K_PAGEDOWN:
		clockSheetStep(-1, true)
	case sdl.K_PAGEUP:
		clockSheetStep(1, true)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		clockSheetActivate(c)
	case sdl.K_y, sdl.K_s:
		clockSheetApply(c)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		clockSheetClose()
	}
}

// clockSheetPad handles a pad button while the sheet is open.
func clockSheetPad(button uint8, c *Config) {
	switch sdl.GameControllerButton(button) {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		clockSheetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		clockSheetMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		clockSheetStep(-1, false)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		clockSheetStep(1, false)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		clockSheetStep(-1, true)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		clockSheetStep(1, true)
	case sdl.CONTROLLER_BUTTON_A:
		clockSheetActivate(c)
	case sdl.CONTROLLER_BUTTON_Y:
		clockSheetApply(c)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		clockSheetClose()
	}
}

// clockSheetMouse handles a tap: the chevrons step, a row takes the focus, the
// buttons press, and a tap outside the card closes it.
func clockSheetMouse(mx, my int32, c *Config) {
	if !pointInRect(mx, my, clockSheet.card) {
		clockSheetClose()
		return
	}
	for f := clockFieldYear; f <= clockFieldCancel; f++ {
		h := clockSheet.hits[f]
		switch {
		case h.dec.W > 0 && pointInRect(mx, my, h.dec):
			clockSheet.focus = f
			clockSheetStep(-1, false)
		case h.inc.W > 0 && pointInRect(mx, my, h.inc):
			clockSheet.focus = f
			clockSheetStep(1, false)
		case h.row.W > 0 && pointInRect(mx, my, h.row):
			clockSheet.focus = f
			if f == clockFieldSet || f == clockFieldCancel {
				clockSheetActivate(c)
			} else {
				PlayNavSound()
			}
		default:
			continue
		}
		return
	}
}

// clockSheetWheel steps the focused value.
func clockSheetWheel(dy int32) {
	switch {
	case dy > 0:
		clockSheetStep(1, false)
	case dy < 0:
		clockSheetStep(-1, false)
	}
}

// clockSheetHints is the footer while the sheet is open.
func clockSheetHints() []FooterHint {
	return []FooterHint{{"Up/Down", "Field"}, {"Left/Right", "Change"}, {"Y", "Set clock"}, {"B", "Cancel"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// clockSheetDraw paints the sheet over the Settings page.
func clockSheetDraw(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !clockSheet.open {
		return
	}
	body := FontForRole(config, TypeBody)
	small := FontForRole(config, TypeLabel)
	if body == nil || small == nil {
		return
	}
	for i := range clockSheet.hits {
		clockSheet.hits[i] = alarmEditHit{}
	}
	drawOverlayScrim(renderer)

	w := int32(600)
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	top := headerBarHeight() + 8
	avail := screenHeight - top - bottomBarHeight("Settings") - 16
	h := avail
	if h > 600 {
		h = 600
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top + (avail-h)/2, W: w, H: h}
	clockSheet.card = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, "Set date & time", "Zone: "+dateTimeZoneLabel(time.Now()), "clock")
	y := UIPageHeaderDraw(renderer, config, header) + 6

	// What the clock will read, in the user's date and clock formats.
	t := clockSheetTime()
	preview := formatDateLong(config, t) + " · " + clockDigitsHM(config, clockSheet.hour, clockSheet.minute)
	x := card.X + OverlayPadX
	inner := card.W - 2*OverlayPadX
	drawText(renderer, body, ellipsize(body, preview, inner), card.X+card.W/2, y, ColorTextPrimary(), textAlignCenter)
	y += int32(body.Height()) + 10

	gap := int32(6)
	bottom := card.Y + card.H - OverlayPadX
	rowH := (bottom - y - 5*gap - 4) / 6
	if rowH > 46 {
		rowH = 46
	}
	if rowH < 24 {
		rowH = 24
	}

	hour := fmt.Sprintf("%02d", clockSheet.hour)
	if clockHour12(config) {
		h12, suffix := clockSheet.hour%12, "AM"
		if clockSheet.hour >= 12 {
			suffix = "PM"
		}
		if h12 == 0 {
			h12 = 12
		}
		hour = fmt.Sprintf("%d %s", h12, suffix)
	}
	values := [...]struct {
		caption, value string
	}{
		{"Year", fmt.Sprintf("%d", clockSheet.year)},
		{"Month", time.Month(clockSheet.month).String()},
		{"Day", fmt.Sprintf("%d · %s", clockSheet.day, t.Weekday().String()[:3])},
		{"Hour", hour},
		{"Minute", fmt.Sprintf("%02d", clockSheet.minute)},
	}
	for f, v := range values {
		r := sdl.Rect{X: x, Y: y, W: inner, H: rowH}
		clockSheet.hits[f] = clockSheetDrawRow(renderer, body, small, r, v.caption, v.value, clockSheet.focus == f)
		y += rowH + gap
	}

	y += 4
	half := (inner - gap) / 2
	set := sdl.Rect{X: x, Y: y, W: half, H: rowH}
	cancel := sdl.Rect{X: x + half + gap, Y: y, W: inner - half - gap, H: rowH}
	drawGlassButton(renderer, config, set, "Set clock", body, WithAlpha(glassAccent(), 60), glassState{Focused: clockSheet.focus == clockFieldSet})
	drawGlassButton(renderer, config, cancel, "Cancel", body, sdl.Color{}, glassState{Focused: clockSheet.focus == clockFieldCancel})
	clockSheet.hits[clockFieldSet].row, clockSheet.hits[clockFieldCancel].row = set, cancel
}

// clockSheetDrawRow paints one value row (caption left, value between
// chevrons on the right) and returns its tap geometry.
func clockSheetDrawRow(renderer *sdl.Renderer, body, small *ttf.Font, r sdl.Rect, caption, value string, focused bool) alarmEditHit {
	drawGlassSurface(renderer, r, 10, glassState{Focused: focused, NoLift: true})
	capInk := WithAlpha(ColorTextSecondary(), 210)
	arrow := ColorTextTertiary()
	if focused {
		capInk, arrow = ColorTextPrimary(), glassAccent()
	}
	sh := int32(small.Height())
	drawText(renderer, small, caption, r.X+16, r.Y+(r.H-sh)/2, capInk, textAlignLeft)
	valueL := r.X + r.W*2/5
	valueR := r.X + r.W - 14
	alarmDrawChevron(renderer, valueL+10, r.Y+r.H/2, 6, -1, arrow)
	alarmDrawChevron(renderer, valueR-10, r.Y+r.H/2, 6, 1, arrow)
	bh := int32(body.Height())
	drawText(renderer, body, ellipsize(body, value, valueR-valueL-64), (valueL+valueR)/2, r.Y+(r.H-bh)/2,
		WithAlpha(ColorTextPrimary(), 235), textAlignCenter)
	return alarmEditHit{
		row: r,
		dec: sdl.Rect{X: valueL - 10, Y: r.Y, W: 48, H: r.H},
		inc: sdl.Rect{X: valueR - 38, Y: r.Y, W: 48, H: r.H},
	}
}
