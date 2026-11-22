package main

// musicfx_ui.go — the Sound effects panel: Tape Mode, the stereo effects and
// the playback speed, one row each. Up/Down picks a row, Left/Right changes
// it, Y steps through the presets and A resets the row (or the whole panel on
// its last row). It opens with X on a keyboard, R2 on a pad, from the Audio
// settings and from the iPod screen's Settings.

import (
	"fmt"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

type musicFXRow struct {
	label string
	hint  string
	tape  bool // only heard while Tape Mode is on
	value func(p musicFXParams) (text string, frac float64)
	adj   func(p *musicFXParams, dir int)
}

func musicFXPct(v int) (string, float64) { return strconv.Itoa(v) + "%", float64(v) / 100 }

// musicFXRows is the panel's reading order.
var musicFXRows = []musicFXRow{
	{label: "Tape Mode", hint: "A Walkman's sound: wow and flutter, hiss, saturation and a darker top end.",
		value: func(p musicFXParams) (string, float64) { return onOff(p.Tape), -1 },
		adj:   func(p *musicFXParams, dir int) { p.Tape = !p.Tape }},
	{label: "Wow & flutter", tape: true, hint: "The slow and fast pitch wobble of a tape transport.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Wow) },
		adj:   func(p *musicFXParams, dir int) { p.Wow += 5 * dir }},
	{label: "Tape hiss", tape: true, hint: "The noise floor of a cassette.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Hiss) },
		adj:   func(p *musicFXParams, dir int) { p.Hiss += 5 * dir }},
	{label: "Saturation", tape: true, hint: "The soft, warm overdrive of a hot recording.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Saturation) },
		adj:   func(p *musicFXParams, dir int) { p.Saturation += 5 * dir }},
	{label: "High-frequency roll-off", tape: true, hint: "A worn head loses the treble: 0% keeps it all, 100% stops near 2.5 kHz.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.RollOff) },
		adj:   func(p *musicFXParams, dir int) { p.RollOff += 5 * dir }},
	{label: "Tape speed", tape: true, hint: "Pitch and tempo together, like a deck's speed trimmer (0.50x to 1.50x).",
		value: func(p musicFXParams) (string, float64) {
			return formatSpeedLabel(float64(p.TapeSpeed) / 100), float64(p.TapeSpeed-50) / 100
		},
		adj: func(p *musicFXParams, dir int) { p.TapeSpeed += 5 * dir }},
	{label: "Reverb", hint: "A stereo room around the music.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Reverb) },
		adj:   func(p *musicFXParams, dir int) { p.Reverb += 5 * dir }},
	{label: "Room size", hint: "From a small room to a cathedral.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Room) },
		adj:   func(p *musicFXParams, dir int) { p.Room += 5 * dir }},
	{label: "Ping-pong delay", hint: "Echoes that bounce between the left and right ear.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Delay) },
		adj:   func(p *musicFXParams, dir int) { p.Delay += 5 * dir }},
	{label: "Delay time", hint: "The time between two echoes.",
		value: func(p musicFXParams) (string, float64) {
			return strconv.Itoa(p.DelayMs) + " ms", float64(p.DelayMs-60) / 840
		},
		adj: func(p *musicFXParams, dir int) { p.DelayMs += 20 * dir }},
	{label: "Echo feedback", hint: "How many echoes follow, up to 90%.",
		value: func(p musicFXParams) (string, float64) { return strconv.Itoa(p.Feedback) + "%", float64(p.Feedback) / 90 },
		adj:   func(p *musicFXParams, dir int) { p.Feedback += 5 * dir }},
	{label: "Chorus", hint: "A thicker, shimmering sound from a moving copy of itself.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Chorus) },
		adj:   func(p *musicFXParams, dir int) { p.Chorus += 5 * dir }},
	{label: "Flanger", hint: "The sweeping jet-plane effect.",
		value: func(p musicFXParams) (string, float64) { return musicFXPct(p.Flanger) },
		adj:   func(p *musicFXParams, dir int) { p.Flanger += 5 * dir }},
	{label: "Playback speed", hint: "Faster or slower with the pitch kept: 1/16x to 2x. Audiobooks keep their own."},
	{label: "Reset all effects", hint: "Every effect off, the tape controls back to their Walkman levels."},
}

const (
	musicFXRowSpeed = 13
	musicFXRowReset = 14
)

// musicFXPanel is the panel's state. Main thread only.
var musicFXPanel struct {
	open    bool
	cursor  int
	scroll  int
	preset  int
	card    sdl.Rect
	rowHits []sdl.Rect
}

func musicFXPanelOpen() {
	musicEQ.panel = false
	musicCloseSearch()
	musicFXPanel.open = true
	musicFXPanel.rowHits = nil
	PlayActivateSound()
}

func musicFXPanelClose() {
	if !musicFXPanel.open {
		return
	}
	musicFXPanel.open = false
	musicFXPanel.rowHits = nil
	PlayBackSound()
}

func musicFXPanelMove(delta int) {
	n := len(musicFXRows)
	musicFXPanel.cursor = ((musicFXPanel.cursor+delta)%n + n) % n
	PlayNavSound()
}

// musicFXPanelAdjust changes the focused row; dir 0 is A (toggle or reset).
func musicFXPanelAdjust(config *Config, dir int) {
	row := musicFXPanel.cursor
	if row < 0 || row >= len(musicFXRows) {
		return
	}
	switch row {
	case musicFXRowSpeed:
		if dir == 0 {
			musicSetSpeed(1)
		} else {
			musicCycleSpeed(dir)
		}
		music.mu.Lock()
		s := music.speed
		music.mu.Unlock()
		showToast("Speed "+formatSpeedLabel(s), ToastInfo())
		PlayNavSound()
		return
	case musicFXRowReset:
		if dir != 0 {
			return
		}
		musicFXSet(config, musicFXDefaults())
		showToast("Sound effects reset", ToastInfo())
		PlayActivateSound()
		return
	}
	p := musicFXNow()
	if dir == 0 {
		if row == 0 {
			p.Tape = !p.Tape
		} else {
			// A puts one row back to its factory value.
			d := musicFXDefaults()
			switch row {
			case 1:
				p.Wow = d.Wow
			case 2:
				p.Hiss = d.Hiss
			case 3:
				p.Saturation = d.Saturation
			case 4:
				p.RollOff = d.RollOff
			case 5:
				p.TapeSpeed = d.TapeSpeed
			case 6:
				p.Reverb = d.Reverb
			case 7:
				p.Room = d.Room
			case 8:
				p.Delay = d.Delay
			case 9:
				p.DelayMs = d.DelayMs
			case 10:
				p.Feedback = d.Feedback
			case 11:
				p.Chorus = d.Chorus
			case 12:
				p.Flanger = d.Flanger
			}
		}
	} else if adj := musicFXRows[row].adj; adj != nil {
		adj(&p, dir)
		// Shaping a tape control is an explicit "I want Tape Mode".
		if musicFXRows[row].tape {
			p.Tape = true
		}
	}
	if musicEQ.bitPerfect && p.active() {
		showToast("Bit-perfect is on — effects stay off until it is turned off", ToastInfo())
	}
	musicFXSet(config, p)
	PlayNavSound()
}

// musicFXPanelPreset applies the next preset.
func musicFXPanelPreset(config *Config) {
	musicFXPanel.preset = (musicFXPanel.preset + 1) % len(musicFXPresets)
	preset := musicFXPresets[musicFXPanel.preset]
	p := musicFXNow()
	preset.apply(&p)
	musicFXSet(config, p)
	showToast("Effects: "+preset.name, ToastInfo())
	PlayNavSound()
}

// handleMusicFXKey drives the panel from a keyboard.
func handleMusicFXKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !musicFXPanel.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		musicFXPanelMove(-1)
	case sdl.K_DOWN:
		musicFXPanelMove(1)
	case sdl.K_LEFT:
		musicFXPanelAdjust(config, -1)
	case sdl.K_RIGHT:
		musicFXPanelAdjust(config, 1)
	case sdl.K_RETURN, sdl.K_SPACE:
		musicFXPanelAdjust(config, 0)
	case sdl.K_y:
		musicFXPanelPreset(config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_x:
		musicFXPanelClose()
	}
	return true
}

// ctrlMusicFXPanel drives the panel from a pad.
func ctrlMusicFXPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !musicFXPanel.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		musicFXPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		musicFXPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		musicFXPanelAdjust(config, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		musicFXPanelAdjust(config, 1)
	case sdl.CONTROLLER_BUTTON_A:
		musicFXPanelAdjust(config, 0)
	case sdl.CONTROLLER_BUTTON_Y:
		musicFXPanelPreset(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		musicFXPanelClose()
	}
	return true
}

// handleMusicFXClick selects (and steps) a row from a tap; outside the card it
// closes the panel.
func handleMusicFXClick(mx, my int32, config *Config) bool {
	if !musicFXPanel.open {
		return false
	}
	for i, r := range musicFXPanel.rowHits {
		if pointInRect(mx, my, r) {
			row := musicFXPanel.scroll + i
			if row == musicFXPanel.cursor {
				dir := 1
				if mx < r.X+r.W/2 {
					dir = -1
				}
				if row == 0 || row == musicFXRowReset {
					dir = 0
				}
				musicFXPanelAdjust(config, dir)
			} else {
				musicFXPanel.cursor = row
				PlayNavSound()
			}
			return true
		}
	}
	if !pointInRect(mx, my, musicFXPanel.card) {
		musicFXPanelClose()
	}
	return true
}

// renderMusicFXPanel draws the panel over the player.
func renderMusicFXPanel(renderer *sdl.Renderer, config *Config) {
	if !musicFXPanel.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	p := musicFXNow()

	w := screenWidth - 96
	if w > 900 {
		w = 900
	}
	h := screenHeight - 2*footerBarHeight()
	if h > 640 {
		h = 640
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	musicFXPanel.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	header := overlayPageHeader(card, "Sound effects", musicFXStatus(), "media")
	band := UIPanelHeaderBand(config, header, 6)
	UIPageHeaderDraw(renderer, config, header)

	rowH := smallH + 20
	listTop := card.Y + band
	footerH := 3*smallH + 30
	visible := int((card.H - band - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if musicFXPanel.cursor < musicFXPanel.scroll {
		musicFXPanel.scroll = musicFXPanel.cursor
	}
	if musicFXPanel.cursor >= musicFXPanel.scroll+visible {
		musicFXPanel.scroll = musicFXPanel.cursor - visible + 1
	}
	last := musicFXPanel.scroll + visible
	if last > len(musicFXRows) {
		last = len(musicFXRows)
	}
	musicFXPanel.rowHits = musicFXPanel.rowHits[:0]
	music.mu.Lock()
	speed := music.speed
	music.mu.Unlock()
	if speed <= 0 {
		speed = 1
	}
	for i := musicFXPanel.scroll; i < last; i++ {
		row := musicFXRows[i]
		r := sdl.Rect{X: card.X + OverlayPadX, Y: listTop + int32(i-musicFXPanel.scroll)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 4}
		if i == musicFXPanel.cursor {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(ColorCardFocus, 255))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, accentColor)
		}
		ink := ColorTextPrimary()
		if row.tape && !p.Tape {
			ink = ColorTextTertiary()
		}
		renderText(renderer, config, font, row.label, ink, r.X+14, r.Y+(r.H-smallH)/2)
		text, frac := "", -1.0
		switch {
		case i == musicFXRowSpeed:
			text = formatSpeedLabel(speed)
		case i == musicFXRowReset:
			text = "A"
		case row.value != nil:
			text, frac = row.value(p)
		}
		tw, _, _ := font.SizeUTF8(text)
		valX := r.X + r.W - int32(tw) - 14
		renderText(renderer, config, font, text, ColorTextSecondary(), valX, r.Y+(r.H-smallH)/2)
		if frac >= 0 {
			if frac > 1 {
				frac = 1
			}
			barW := r.W / 4
			barX := valX - 16 - barW
			barY := r.Y + r.H/2 - 3
			fillRoundedRect(renderer, barX, barY, barW, 6, 3, ColorTrack)
			if fill := int32(float64(barW) * frac); fill > 0 {
				col := accentColor
				if row.tape && !p.Tape {
					col = WithAlpha(ColorTextTertiary(), 160)
				}
				fillRoundedRect(renderer, barX, barY, fill, 6, 3, col)
			}
		}
		musicFXPanel.rowHits = append(musicFXPanel.rowHits, r)
	}

	detailY := listTop + int32(last-musicFXPanel.scroll)*rowH + 6
	if musicFXPanel.cursor >= 0 && musicFXPanel.cursor < len(musicFXRows) {
		detail := musicFXRows[musicFXPanel.cursor].hint
		if musicEQ.bitPerfect {
			detail = "Bit-perfect output is on: effects, tempo and the equaliser are bypassed until it is turned off."
		}
		measure := func(s string) int32 { w, _, _ := font.SizeUTF8(s); return int32(w) }
		for n, line := range wrapMeasured([]rune(detail), card.W-2*OverlayPadX, measure) {
			if n >= 2 {
				break
			}
			renderText(renderer, config, font, line, ColorTextTertiary(), card.X+OverlayPadX, detailY)
			detailY += smallH + 2
		}
	}
	hint := hintLine(FooterHint{"Up/Down", "Row"}, FooterHint{"Left/Right", "Change"},
		FooterHint{"A", "Reset"}, FooterHint{"Y", fmt.Sprintf("Preset (%s)", musicFXPresets[musicFXPanel.preset].name)}, FooterHint{"B", "Close"})
	hint = ellipsize(font, hint, card.W-24)
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, ColorTextTertiary(), card.X+(card.W-int32(hw))/2, card.Y+card.H-smallH-12)
}
