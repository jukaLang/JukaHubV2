package main

// musicsettings.go — Audio settings panel.
//
// The player's options are reached from the app menu and from the player itself
// (K), because they apply to every scene: the layout the player draws, the
// cover-art opacity, what the controls do while the screen is dark, whether
// playback survives leaving the player, and the two decoding modes.
//
// The panel follows the Accessibility panel's shape (one row per setting, label
// left, value right, D-pad to change) so the handheld's controller story stays
// the same everywhere.

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type musicSettingsRowKind int

const (
	msRowLayout musicSettingsRowKind = iota
	msRowArtOpacity
	msRowScreenMode
	msRowBackground
	msRowBitPerfect
	msRowPEQ
	msRowShortcuts
)

type musicSettingsRow struct {
	kind  musicSettingsRowKind
	label string
	hint  string
}

type musicSettingsState struct {
	open      bool
	cursor    int
	scroll    int
	panelRect sdl.Rect
	rowHits   []musicSettingsHit
}

type musicSettingsHit struct {
	rect  sdl.Rect
	index int
}

var musicSettings musicSettingsState

// musicSettingsRows is the fixed list; the order is the reading order.
func musicSettingsRows() []musicSettingsRow {
	return []musicSettingsRow{
		{msRowLayout, "Now Playing layout", "Auralis Pro is the artwork card; StockOS is the compact list-first screen."},
		{msRowArtOpacity, "Cover art opacity", "How strongly the artwork tile and its spectrum show through the card."},
		{msRowScreenMode, "Screen-off controls", "What still works while the panel is dark: any key wakes, Controls keeps transport and volume, Select only ignores stray presses."},
		{msRowBackground, "Background playback", "Keep playing when you leave the player. In the player, Y + Left toggles it too."},
		{msRowBitPerfect, "Bit-perfect DAC output", "Bypass the equaliser and tempo so decoded samples reach the DAC unprocessed."},
		{msRowPEQ, "31-band PEQ", "One-third-octave equaliser from 20 Hz to 20 kHz. Open it with G in the player."},
		{msRowShortcuts, "Keyboard shortcuts", "Show every key and controller binding."},
	}
}

// musicBackgroundKey is the custom variable behind background playback.
const musicBackgroundKey = "musicBackgroundPlay"

// musicBackgroundEnabled reports whether leaving the player keeps the music on.
// The default is on: playback already survived a scene change, and switching it
// off must be a deliberate choice.
func musicBackgroundEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, musicBackgroundKey, true)
}

func musicSetBackgroundEnabled(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicBackgroundKey] = boolString(on)
}

// musicSettingsValue is the right-hand text of a row.
func musicSettingsValue(row musicSettingsRow, config *Config) string {
	switch row.kind {
	case msRowLayout:
		return musicLayoutFromConfig(config).label()
	case msRowArtOpacity:
		return fmt.Sprintf("%d%%", musicArtOpacityPercent(config))
	case msRowScreenMode:
		return musicScreenModeFromConfig(config).label()
	case msRowBackground:
		if musicBackgroundEnabled(config) {
			return "On"
		}
		return "Off"
	case msRowBitPerfect:
		if musicEQ.bitPerfect {
			return "On"
		}
		return "Off"
	case msRowPEQ:
		return musicPEQStatus()
	}
	return ""
}

// musicSettingsAdjust moves a row's value. dir < 0 is left, dir > 0 is right.
func musicSettingsAdjust(row musicSettingsRow, dir int, config *Config) bool {
	switch row.kind {
	case msRowLayout:
		next := musicLayoutStockOS
		if musicLayoutFromConfig(config) == musicLayoutStockOS {
			next = musicLayoutAuralis
		}
		musicSetLayout(config, next)
		showToast("Layout: "+next.label(), ToastInfo())
	case msRowArtOpacity:
		step := 10 * dir
		percent := musicArtOpacityPercent(config) + step
		if percent < 10 {
			percent = 10
		}
		if percent > 100 {
			percent = 100
		}
		musicSetArtOpacity(config, percent)
		showToast(fmt.Sprintf("Cover art: %d%%", percent), ToastInfo())
	case msRowScreenMode:
		mode := musicScreenModeFromConfig(config)
		switch {
		case dir < 0:
			if mode == musicScreenAny {
				mode = musicScreenSelect
			} else {
				mode--
			}
		default:
			mode = (mode + 1) % 3
		}
		musicSetScreenMode(config, mode)
		showToast("Screen-off controls: "+mode.label(), ToastInfo())
	case msRowBackground:
		on := !musicBackgroundEnabled(config)
		musicSetBackgroundEnabled(config, on)
		showToast("Background playback: "+onOff(on), ToastInfo())
	case msRowBitPerfect:
		musicSetBitPerfect(!musicEQ.bitPerfect, config)
		if musicEQ.bitPerfect {
			showToast("Bit-perfect: EQ and tempo bypassed", ToastSuccess())
		} else {
			showToast("Bit-perfect off", ToastInfo())
		}
	case msRowPEQ:
		musicSetPEQEnabled(!musicEQ.enabled, config)
		showToast("PEQ: "+onOff(musicEQ.enabled), ToastInfo())
	default:
		return false
	}
	musicPEQSave(config)
	PlayNavSound()
	return true
}

func onOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// musicSettingsActivate handles A / Enter on a row: the ones that are really
// commands act, the rest step forward like Right.
func musicSettingsActivate(row musicSettingsRow, config *Config) {
	if row.kind == msRowShortcuts {
		shortcutsOpen = true
		PlayActivateSound()
		return
	}
	musicSettingsAdjust(row, 1, config)
}

// ──────────────────────────────────────────────────────────────────────
// Open / close and input
// ──────────────────────────────────────────────────────────────────────

func openMusicSettingsPanel(config *Config) {
	qs.open = false
	toolsPanel.open = false
	a11y.open = false
	smartHomePanel.open = false
	musicSettings.open = true
	musicSettings.cursor = 0
	musicSettings.scroll = 0
	musicSettings.rowHits = nil
	musicSettings.panelRect = sdl.Rect{}
	musicPEQEnsureLoaded()
	PlayActivateSound()
}

func closeMusicSettingsPanel() {
	if !musicSettings.open {
		return
	}
	musicSettings.open = false
	musicSettings.rowHits = nil
	musicSettings.panelRect = sdl.Rect{}
	PlayBackSound()
}

func musicSettingsMove(delta int) {
	rows := musicSettingsRows()
	if len(rows) == 0 {
		return
	}
	musicSettings.cursor += delta
	if musicSettings.cursor < 0 {
		musicSettings.cursor = len(rows) - 1
	}
	if musicSettings.cursor >= len(rows) {
		musicSettings.cursor = 0
	}
	PlayNavSound()
}

func handleMusicSettingsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !musicSettings.open {
		return false
	}
	rows := musicSettingsRows()
	cur := musicSettings.cursor
	if cur < 0 || cur >= len(rows) {
		cur = 0
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		musicSettingsMove(-1)
	case sdl.K_DOWN:
		musicSettingsMove(1)
	case sdl.K_LEFT:
		musicSettingsAdjust(rows[cur], -1, config)
	case sdl.K_RIGHT:
		musicSettingsAdjust(rows[cur], 1, config)
	case sdl.K_RETURN, sdl.K_SPACE:
		musicSettingsActivate(rows[cur], config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_k:
		closeMusicSettingsPanel()
	}
	return true
}

func ctrlMusicSettings(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return true
	}
	rows := musicSettingsRows()
	cur := musicSettings.cursor
	if cur < 0 || cur >= len(rows) {
		cur = 0
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		musicSettingsMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		musicSettingsMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		musicSettingsAdjust(rows[cur], -1, config)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_A:
		musicSettingsActivate(rows[cur], config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeMusicSettingsPanel()
	}
	return true
}

func handleMusicSettingsMouse(mx, my int32) bool {
	if !musicSettings.open {
		return false
	}
	for _, hit := range musicSettings.rowHits {
		if pointInRect(mx, my, hit.rect) {
			musicSettings.cursor = hit.index
			if musicSettings.cursor < len(musicSettingsRows()) {
				musicSettingsAdjust(musicSettingsRows()[musicSettings.cursor], 1, appConfig)
			}
			return true
		}
	}
	if musicSettings.panelRect.W > 0 && pointInRect(mx, my, musicSettings.panelRect) {
		return true
	}
	closeMusicSettingsPanel()
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderMusicSettingsPanel(renderer *sdl.Renderer, config *Config) {
	if !musicSettings.open || renderer == nil {
		return
	}
	rows := musicSettingsRows()
	if musicSettings.cursor >= len(rows) {
		musicSettings.cursor = len(rows) - 1
	}
	if musicSettings.cursor < 0 {
		musicSettings.cursor = 0
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	rowH := 10 + medH + 10
	footerH := 18 + smallH
	panelW := int32(860)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := int32(24) + medH + 12 + int32(len(rows))*rowH + 12 + 2*smallH + 20 + footerH
	if panelH > screenHeight-56 {
		panelH = screenHeight - 56
	}
	x := (screenWidth - panelW) / 2
	y := (screenHeight - panelH) / 2
	musicSettings.panelRect = sdl.Rect{X: x, Y: y, W: panelW, H: panelH}

	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	drawCard(renderer, x, y, panelW, panelH, 18)

	title := "Audio"
	renderText(renderer, config, medium, title, ColorTextPrimary(), x+24, y+20)
	version := "Settings"
	vw, _, _ := font.SizeUTF8(version)
	renderText(renderer, config, font, version, ColorTextTertiary(), x+panelW-int32(vw)-24, y+24)

	musicSettings.rowHits = musicSettings.rowHits[:0]
	listTop := y + 24 + medH + 12
	maxRows := int((panelH - (listTop - y) - 2*smallH - 44 - footerH) / rowH)
	if maxRows < 1 {
		maxRows = 1
	}
	if musicSettings.cursor < musicSettings.scroll {
		musicSettings.scroll = musicSettings.cursor
	}
	if musicSettings.cursor >= musicSettings.scroll+maxRows {
		musicSettings.scroll = musicSettings.cursor - maxRows + 1
	}
	if musicSettings.scroll < 0 {
		musicSettings.scroll = 0
	}
	last := musicSettings.scroll + maxRows
	if last > len(rows) {
		last = len(rows)
	}

	for i := musicSettings.scroll; i < last; i++ {
		row := rows[i]
		ry := listTop + int32(i-musicSettings.scroll)*rowH
		rect := sdl.Rect{X: x + 16, Y: ry, W: panelW - 32, H: rowH - 4}
		if i == musicSettings.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(ColorCardFocus, 255))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, accentColor)
		}
		renderText(renderer, config, medium, row.label, ColorTextPrimary(),
			rect.X+16, rect.Y+(rect.H-medH)/2)
		value := musicSettingsValue(row, config)
		vw, _, _ := font.SizeUTF8(value)
		col := ColorTextSecondary()
		if row.kind == msRowBitPerfect && musicEQ.bitPerfect {
			col = accentColor
		}
		renderText(renderer, config, font, value, col,
			rect.X+rect.W-int32(vw)-16, rect.Y+(rect.H-smallH)/2)
		musicSettings.rowHits = append(musicSettings.rowHits, musicSettingsHit{rect: rect, index: i})
	}

	detailY := listTop + int32(last-musicSettings.scroll)*rowH + 8
	if musicSettings.cursor < len(rows) {
		detail := rows[musicSettings.cursor].hint
		measure := func(s string) int32 { w, _, _ := font.SizeUTF8(s); return int32(w) }
		for _, line := range wrapMeasured([]rune(detail), panelW-48, measure) {
			renderText(renderer, config, font, line, ColorTextTertiary(), x+24, detailY)
			detailY += smallH + 2
		}
	}

	hint := "UP/DOWN row   LEFT/RIGHT change   A select   B close"
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, sdl.Color{R: 110, G: 125, B: 150, A: 200},
		x+(panelW-int32(hw))/2, y+panelH-footerH+6)
}

// musicSettingsUSBLine describes the attached DAC, for the panel's footnote on
// builds that expose ALSA. It is informational: the port's power is firmware
// policy, not something the player toggles.
func musicSettingsUSBLine() string {
	name := detectUSBDacName()
	if name == "" {
		return ""
	}
	return "USB DAC: " + name
}

// detectUSBDacName reports a USB sound card by name, if the kernel lists one.
// On a handheld this is the DAC plugged into the bottom USB-C port; on desktop
// builds without ALSA it simply reports nothing rather than guessing.
func detectUSBDacName() string {
	data, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return ""
	}
	var found string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(strings.ToLower(line), "usb") {
			continue
		}
		// The card list indents its description line: " 1 [Device         ]: USB-Audio - ...".
		if idx := strings.Index(line, ": "); idx >= 0 {
			rest := strings.TrimSpace(line[idx+2:])
			if rest != "" {
				found = rest
			}
		}
	}
	return found
}

// musicArtOpacitySteps are the values the settings row walks through.
var musicArtOpacitySteps = []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

// musicArtOpacityIndex snaps a percentage to the nearest step, so starting from
// a hand-edited config still moves predictably.
func musicArtOpacityIndex(percent int) int {
	best, idx := math.MaxFloat64, 0
	for i, v := range musicArtOpacitySteps {
		if d := math.Abs(float64(v - percent)); d < best {
			best, idx = d, i
		}
	}
	return idx
}

// musicScreenModeSetting is the config writer the panel uses; the reader lives
// in musicfeatures.go next to the mode rules.
func musicSetScreenMode(config *Config, mode musicScreenMode) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	switch mode {
	case musicScreenControls:
		config.Variables.Custom["musicScreenOffMode"] = "controls"
	case musicScreenSelect:
		config.Variables.Custom["musicScreenOffMode"] = "select-only"
	default:
		config.Variables.Custom["musicScreenOffMode"] = "any"
	}
}

// musicSettingsBackgroundNote is the toast shown when Y + Left is used in the
// player, so the state is always visible where it is toggled.
func musicSettingsBackgroundNote(config *Config) string {
	return "Background playback: " + strings.ToUpper(onOff(musicBackgroundEnabled(config)))
}
