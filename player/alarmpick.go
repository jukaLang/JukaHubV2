package main

// alarmpick.go — choosing an alarm's ringtone.
//
// The editor's ringtone row cycles in place with Left/Right, the way the hour
// does, through the synthesised tones and one more stop for music; every step
// previews the sound it lands on. X (or A on "Choose from music…") opens the
// music picker: a glass sheet listing the tracks in the music player's folders
// (alarmMusicRoots), scanned in the background, where moving the focus previews
// the track once the cursor rests and A makes it the alarm's ringtone. The
// choice is saved with the alarm, per alarm.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// alarmPickMaxRows is how many tracks the sheet shows at once, and how far
	// a page step moves.
	alarmPickMaxRows = 7
	// alarmPickPreviewDelay is how long the focus rests on a track before its
	// preview starts, so scrolling through a list does not start a decoder for
	// every row it passes.
	alarmPickPreviewDelay = 350 * time.Millisecond
)

// Hit rects written by the renderer and read by the mouse handler.
var (
	alarmRingtoneRowRect   sdl.Rect
	alarmRingtoneMusicRect sdl.Rect
	alarmPickSheetRect     sdl.Rect
	alarmPickRowRects      [alarmPickMaxRows]sdl.Rect
	alarmPickRowFirst      int
	alarmPickRowCount      int
)

// alarmSoundStep cycles the editor's ringtone and previews the result, so the
// choice is made by ear without leaving the editor.
func alarmSoundStep(dir int) {
	chosen := ""
	if alarmIsMusic(alarmUI.editSound) {
		chosen = alarmUI.editSound
	} else if id := alarmEditingSavedSound(); alarmIsMusic(id) {
		chosen = id
	}
	alarmUI.editSound = alarmCycleRingtone(alarmUI.editSound, chosen, dir)
	PlayNavSound()
	if alarmUI.editSound == alarmMusicPickID {
		alarmPlayStop()
		return
	}
	alarmPlayStart(alarmUI.editSound, true)
}

// alarmEditingSavedSound is the ringtone the alarm being edited has saved ("" for
// a new alarm), so cycling away from its track and back finds it again.
func alarmEditingSavedSound() string {
	if alarmUI.editID < 0 {
		return ""
	}
	for _, a := range alarmStore.Alarms {
		if a.ID == alarmUI.editID {
			return a.Sound
		}
	}
	return ""
}

// alarmDrawChevron draws a 2 px chevron pointing left (dir < 0) or right.
func alarmDrawChevron(renderer *sdl.Renderer, cx, cy, size int32, dir int32, c sdl.Color) {
	if dir < 0 {
		dir = -1
	} else {
		dir = 1
	}
	tipX := cx + dir*size/2
	armX := cx - dir*size/2
	setDrawColor(renderer, c)
	for o := int32(0); o < 2; o++ {
		_ = renderer.DrawLine(armX-dir*o, cy-size, tipX-dir*o, cy)
		_ = renderer.DrawLine(tipX-dir*o, cy, armX-dir*o, cy+size)
	}
}

// alarmDrawRingtoneRow paints the ringtone row: chevrons at both ends, the
// ringtone's name, and — while the row has focus — the X badge for music.
func alarmDrawRingtoneRow(renderer *sdl.Renderer, config *Config, font, small *ttf.Font, row sdl.Rect) {
	if renderer == nil || font == nil || small == nil {
		return
	}
	focused := alarmUI.tab == 4
	drawGlassSurface(renderer, row, 10, glassState{Focused: focused, NoLift: true})
	alarmRingtoneRowRect = row
	ink := ColorTextTertiary()
	if focused {
		ink = glassAccent()
	}
	alarmDrawChevron(renderer, row.X+18, row.Y+row.H/2, 6, -1, ink)
	alarmDrawChevron(renderer, row.X+row.W-18, row.Y+row.H/2, 6, 1, ink)

	nameRight := row.X + row.W - 40
	alarmRingtoneMusicRect = sdl.Rect{}
	if focused {
		label := "Music"
		bw, th := measureText(small, hintKeyFor("X", label))
		lw, _ := measureText(small, label)
		badge := sdl.Rect{W: bw + 20, H: th + 6}
		badge.X = nameRight - lw - 8 - badge.W
		badge.Y = row.Y + (row.H-badge.H)/2
		drawGlassHintBadge(renderer, config, small, badge, "X", label, badge.X+badge.W+8, badge.Y+3)
		alarmRingtoneMusicRect = sdl.Rect{X: badge.X - 6, Y: row.Y, W: nameRight - badge.X + 6, H: row.H}
		nameRight = badge.X - 12
	}
	name := ellipsize(font, alarmSoundName(alarmUI.editSound), nameRight-(row.X+40))
	_, nh := measureText(font, name)
	renderText(renderer, config, font, name, WithAlpha(ColorTextPrimary(), 230), row.X+40, row.Y+(row.H-nh)/2)
}

// alarmRingtoneClick handles a tap on the ringtone row: the music badge opens
// the picker, the left and right halves step the list.
func alarmRingtoneClick(config *Config, mx, my int32) bool {
	switch {
	case alarmRingtoneMusicRect.W > 0 && pointInRect(mx, my, alarmRingtoneMusicRect):
		alarmPickOpen(config)
	case alarmRingtoneRowRect.W > 0 && pointInRect(mx, my, alarmRingtoneRowRect):
		if mx < alarmRingtoneRowRect.X+alarmRingtoneRowRect.W/2 {
			alarmSoundStep(-1)
		} else {
			alarmSoundStep(1)
		}
	default:
		return false
	}
	return true
}

// ── The music picker ─────────────────────────────────────────────────────

// alarmPickOpen opens the picker on the alarm's current track (if it has one)
// and starts a scan of the music folders.
func alarmPickOpen(config *Config) {
	alarmUI.pickOpen = true
	alarmUI.pickCursor = 0
	alarmUI.pickScroll = 0
	alarmUI.pickPreview = time.Time{}
	alarmMusicRefresh(config)
	current := alarmMusicPath(alarmUI.editSound)
	if current == "" {
		current = alarmMusicPath(alarmEditingSavedSound())
	}
	if files, _ := alarmMusicSnapshot(); current != "" {
		for i, f := range files {
			if f.Path == current {
				alarmUI.pickCursor = i
				break
			}
		}
	}
	alarmPlayStop()
	PlayActivateSound()
}

// alarmPickClose closes the picker without changing the ringtone.
func alarmPickClose() {
	alarmUI.pickOpen = false
	alarmUI.pickPreview = time.Time{}
	alarmPlayStop()
	PlayBackSound()
}

// alarmPickMove moves the focus and schedules the focused track's preview.
func alarmPickMove(delta int) {
	files, _ := alarmMusicSnapshot()
	if len(files) == 0 {
		return
	}
	next := alarmUI.pickCursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(files) {
		next = len(files) - 1
	}
	if next == alarmUI.pickCursor {
		return
	}
	alarmUI.pickCursor = next
	alarmUI.pickPreview = time.Now().Add(alarmPickPreviewDelay)
	alarmPlayStop()
	PlayNavSound()
}

// alarmPickChoose makes the focused track the alarm's ringtone.
func alarmPickChoose() {
	files, _ := alarmMusicSnapshot()
	if alarmUI.pickCursor < 0 || alarmUI.pickCursor >= len(files) {
		return
	}
	alarmUI.editSound = alarmMusicPrefix + files[alarmUI.pickCursor].Path
	alarmUI.pickOpen = false
	alarmUI.pickPreview = time.Time{}
	alarmPlayStop()
	PlayActivateSound()
}

// alarmPickClick handles a tap while the picker is open: a row chooses its
// track, a tap outside the sheet closes it.
func alarmPickClick(mx, my int32) {
	for i := 0; i < alarmPickRowCount; i++ {
		if pointInRect(mx, my, alarmPickRowRects[i]) {
			alarmUI.pickCursor = alarmPickRowFirst + i
			alarmPickChoose()
			return
		}
	}
	if alarmPickSheetRect.W > 0 && !pointInRect(mx, my, alarmPickSheetRect) {
		alarmPickClose()
	}
}

// renderAlarmPicker draws the picker sheet over the editor, and starts the
// focused track's preview once the focus has rested on it.
func renderAlarmPicker(renderer *sdl.Renderer, config *Config) {
	alarmPickRowCount = 0
	alarmPickSheetRect = sdl.Rect{}
	if renderer == nil || !alarmUI.pickOpen {
		return
	}
	files, ready := alarmMusicSnapshot()
	if !alarmUI.pickPreview.IsZero() && !time.Now().Before(alarmUI.pickPreview) {
		alarmUI.pickPreview = time.Time{}
		// The sheet keeps drawing under a ring's takeover, and the player plays
		// one sound at a time: a preview never starts over the ringtone.
		if !alarmFiringActive() && alarmUI.pickCursor >= 0 && alarmUI.pickCursor < len(files) {
			alarmPlayStart(alarmMusicPrefix+files[alarmUI.pickCursor].Path, true)
		}
	}

	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if small == nil || medium == nil {
		return
	}
	drawOverlayScrim(renderer)
	rowH := scaleY(48, screenHeight)
	sheetW := scaleX(680, screenWidth)
	if sheetW > screenWidth-48 {
		sheetW = screenWidth - 48
	}
	pad := scaleX(28, screenWidth)
	_, titleH := measureText(medium, "Ag")
	_, smallH := measureText(small, "Ag")
	sheetH := pad + titleH + 6 + smallH + 16 + int32(alarmPickMaxRows)*rowH + 16 + smallH + 6 + pad
	if sheetH > screenHeight-48 {
		sheetH = screenHeight - 48
	}
	sheet := sdl.Rect{X: (screenWidth - sheetW) / 2, Y: (screenHeight - sheetH) / 2, W: sheetW, H: sheetH}
	alarmPickSheetRect = sheet
	drawGlassSheet(renderer, sheet, glassSheetFill())

	x := sheet.X + pad
	w := sheet.W - 2*pad
	y := sheet.Y + pad
	renderText(renderer, config, medium, "Choose a ringtone", ColorTextPrimary(), x, y)
	y += titleH + 6
	renderText(renderer, config, small, ellipsize(small, "From your music · plays as you choose", w), ColorTextMuted(), x, y)
	y += smallH + 16

	listTop := y
	listH := int32(alarmPickMaxRows) * rowH
	switch {
	case len(files) == 0 && !ready:
		renderText(renderer, config, small, "Looking for music…", ColorTextMuted(), x, listTop+rowH/2-smallH/2)
	case len(files) == 0:
		lines := []string{
			"No music found.",
			"Put MP3, OGG, WAV or FLAC files in a Music folder,",
			"or play a folder in the music player once.",
		}
		for i, line := range lines {
			renderText(renderer, config, small, ellipsize(small, line, w), ColorTextMuted(), x, listTop+int32(i)*(smallH+6))
		}
	default:
		if alarmUI.pickCursor >= len(files) {
			alarmUI.pickCursor = len(files) - 1
		}
		if alarmUI.pickCursor < alarmUI.pickScroll {
			alarmUI.pickScroll = alarmUI.pickCursor
		}
		if alarmUI.pickCursor >= alarmUI.pickScroll+alarmPickMaxRows {
			alarmUI.pickScroll = alarmUI.pickCursor - alarmPickMaxRows + 1
		}
		alarmPickRowFirst = alarmUI.pickScroll
		current := alarmMusicPath(alarmUI.editSound)
		for i := 0; i < alarmPickMaxRows && alarmUI.pickScroll+i < len(files); i++ {
			f := files[alarmUI.pickScroll+i]
			row := sdl.Rect{X: x, Y: listTop + int32(i)*rowH, W: w, H: rowH - 6}
			focused := alarmUI.pickScroll+i == alarmUI.pickCursor
			drawGlassSurface(renderer, row, glassRowRadius, glassState{Focused: focused, NoLift: true})
			alarmPickRowRects[i] = row
			alarmPickRowCount = i + 1
			nameW := row.W - 32
			if f.Path == current {
				// The track this alarm already rings with.
				fillCircle(renderer, row.X+row.W-20, row.Y+row.H/2, 5, glassAccent())
				nameW -= 24
			}
			_, nh := measureText(small, "Ag")
			ink := ColorTextSecondary()
			if focused {
				ink = ColorTextPrimary()
			}
			renderText(renderer, config, small, ellipsize(small, f.Name, nameW), ink, row.X+16, row.Y+(row.H-nh)/2)
		}
	}

	// Footer hints, Home's badges.
	hy := listTop + listH + 16
	hx := x
	for _, h := range [2][2]string{{"A", "Choose"}, {"B", "Back"}} {
		// Measured as drawGlassHintBadge names it (Enter/Esc on a keyboard).
		bw, _ := measureText(small, hintKeyFor(h[0], h[1]))
		badge := sdl.Rect{X: hx, Y: hy, W: bw + 20, H: smallH + 6}
		hx = drawGlassHintBadge(renderer, config, small, badge, h[0], h[1], badge.X+badge.W+8, hy+3) + scaleX(24, screenWidth)
	}
}
