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

	"github.com/veandco/go-sdl2/sdl"
)

type musicSettingsRowKind int

const (
	msRowLayout musicSettingsRowKind = iota
	msRowArtOpacity
	msRowScreenMode
	msRowBackground
	msRowBitPerfect
	msRowCrossfade
	msRowReplayGain
	msRowPEQ
	msRowPlaylists
	msRowShortcuts
	msRowGameMusic
	msRowGameLevel
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

// musicSettingsRows is the fixed list; the order is the reading order. Opened
// over the player, its Now playing rows lead (music_nowplaying_rows.go).
func musicSettingsRows() []musicSettingsRow {
	rows := []musicSettingsRow{
		{msRowLayout, "Now Playing layout", "Auralis Pro is the artwork card; StockOS is the compact list-first screen; iPod is the click-wheel style screen with its own menus, themes and servers."},
		{msRowArtOpacity, "Cover art opacity", "How strongly the artwork tile and its spectrum show through the card."},
		{msRowScreenMode, "Screen-off controls", "What still works while the panel is dark: any key wakes, Controls keeps transport and volume, Select only ignores stray presses."},
		{msRowBackground, "Background playback", "Keep playing when you leave the player. In the player, Y + Left toggles it too."},
		{msRowGameMusic, "Keep music playing in games", "Music that plays when you start a game carries on under it, mixed with the game's sound (speaker or USB DAC). A Bluetooth headset plays one app at a time, so there the music pauses."},
		{msRowGameLevel, "Music volume in games", "How loud the music plays under a game, so the game's own sound stays on top."},
		{msRowGameQuiet, "Quiet the game while music plays", "Music started from the in-game menu (Select + L2) mutes a RetroArch game's own sound until the music stops."},
		{msRowMusicTidy, "Tidy the Music folder", "After a library scan, songs loose in Music move into Music/Singles and an album folder dropped into Music moves to Music/Artist/Album. Nothing is ever overwritten."},
		{msRowBitPerfect, "Bit-perfect DAC output", "Bypass the equaliser, tempo and spatial audio. With a USB DAC, lossless tracks play at their own rate and bit depth, unscaled (use the DAC's volume); UI sounds are muted while they do."},
		{msRowCrossfade, "Crossfade", "Overlap the end of one track with the next: 0 to 10 seconds, off at 0. The join is sample-accurate and never runs on the bit-perfect path."},
		{msRowReplayGain, "ReplayGain", "Use a track's ReplayGain / album gain tags, with peak limiting so a boosted track cannot clip. Off on the bit-perfect path."},
		{msRowPEQ, "31-band PEQ", "One-third-octave equaliser from 20 Hz to 20 kHz, with the iPod's presets. A opens it (L2 or G in the player); Left/Right switch it on or off."},
		{msRowAutoEQ, "AutoEQ headphones", "Correct your headphones to neutral with an AutoEq profile, searched offline once the list is downloaded. Your own ParametricEQ.txt files in Music/AutoEQ are listed first."},
		{msRowSoundFX, "Sound effects", "Tape Mode (wow and flutter, hiss, saturation, roll-off, tape speed), reverb, ping-pong delay, chorus, flanger and the playback speed. R2 or X in the player."},
		{msRowVisualizer, "Visualizer", "The full-screen visualizer's style (Now playing's first row in the player, or V): ten styles drawn from what you hear, in four dithered shades."},
		{msRowServers, "Music servers & scrobbling", "Navidrome, Jellyfin, Last.fm and ListenBrainz: A shows a code that opens the setup page on your phone."},
		{msRowSpatialMode, "Spatial audio", spatialModeHint},
		{msRowSpatialStrength, "Spatial strength", spatialStrengthHint},
		{msRowSpatialRoom, "Virtual room", spatialRoomHint},
		{msRowSpatialGames, "Spatial audio in games & apps", spatialGamesHint},
		{msRowPlaylists, "Playlists", "Left/right picks a saved list, A plays it, X saves the current queue under a name."},
		{msRowShortcuts, "Keyboard shortcuts", "Show every key and controller binding."},
	}
	if now := musicNowPlayingRows(); len(now) > 0 {
		return append(now, rows...)
	}
	return rows
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
	if v, ok := musicNowPlayingValue(row.kind, config); ok {
		return v // music_nowplaying_rows.go
	}
	if v, ok := spatialPanelValue(row.kind, config); ok {
		return v
	}
	if v, ok := musicExtrasPanelValue(row.kind, config); ok {
		return v // musictidy.go, ingame_music.go
	}
	if v, ok := musicPodSettingsValue(row.kind, config); ok {
		return v // musicpanels.go
	}
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
	case msRowCrossfade:
		if sec := musicCrossfadeSeconds(config); sec > 0 {
			return fmt.Sprintf("%ds", sec)
		}
		return "Off"
	case msRowReplayGain:
		return onOff(musicReplayGainEnabled(config))
	case msRowGameMusic:
		return onOff(musicGameKeepEnabled(config))
	case msRowGameLevel:
		return fmt.Sprintf("%d%%", musicGameLevel(config))
	case msRowPEQ:
		return musicPEQStatus()
	case msRowPlaylists:
		if pl, ok := musicSelectedPlaylist(); ok {
			return fmt.Sprintf("%s (%d)", pl.Name, len(pl.Tracks))
		}
		return "None yet"
	}
	return ""
}

// musicSettingsAdjust moves a row's value. dir < 0 is left, dir > 0 is right.
func musicSettingsAdjust(row musicSettingsRow, dir int, config *Config) bool {
	// The Now playing rows act on the player and sound for themselves
	// (music_nowplaying_rows.go).
	if musicNowPlayingAdjust(row.kind, dir, config) {
		return true
	}
	// The spatial rows save and sound for themselves (spatial_ui.go).
	if spatialPanelAdjust(row.kind, dir, config) {
		return true
	}
	// Two switches that save and sound for themselves (musictidy.go).
	if musicExtrasPanelAdjust(row.kind, config) {
		return true
	}
	// The AutoEQ, effects, visualizer and servers rows (musicpanels.go).
	if musicPodSettingsAdjust(row.kind, dir, config) {
		return true
	}
	switch row.kind {
	case msRowLayout:
		next := musicLayoutCycle(musicLayoutFromConfig(config), dir)
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
	case msRowGameMusic:
		on := !musicGameKeepEnabled(config)
		musicGameSetKeep(config, on)
		showToast("Keep music playing in games: "+onOff(on), ToastInfo())
	case msRowGameLevel:
		pct := musicGameLevel(config) + 10*dir
		if pct < 10 {
			pct = 10
		}
		if pct > 100 {
			pct = 100
		}
		musicGameSetLevel(config, pct)
		showToast(fmt.Sprintf("Music volume in games: %d%%", pct), ToastInfo())
	case msRowBitPerfect:
		musicSetBitPerfect(!musicEQ.bitPerfect, config)
		if musicEQ.bitPerfect {
			showToast("Bit-perfect: EQ, tempo and spatial audio bypassed", ToastSuccess())
		} else {
			showToast("Bit-perfect off", ToastInfo())
		}
	case msRowCrossfade:
		sec := musicCrossfadeSeconds(config) + dir
		if sec < 0 {
			sec = 0
		}
		if sec > musicCrossfadeMax {
			sec = musicCrossfadeMax
		}
		musicSetCrossfadeSeconds(config, sec)
		if sec == 0 {
			showToast("Crossfade off", ToastInfo())
		} else {
			showToast(fmt.Sprintf("Crossfade: %ds", sec), ToastInfo())
		}
	case msRowReplayGain:
		on := !musicReplayGainEnabled(config)
		musicSetReplayGainEnabled(config, on)
		showToast("ReplayGain: "+onOff(on), ToastInfo())
	case msRowPEQ:
		musicSetPEQEnabled(!musicEQ.enabled, config)
		showToast("PEQ: "+onOff(musicEQ.enabled), ToastInfo())
	case msRowPlaylists:
		if pl, ok := musicCyclePlaylistSelection(dir); ok {
			showToast(fmt.Sprintf("Playlist: %s (%d tracks)", pl.Name, len(pl.Tracks)), ToastInfo())
		} else {
			showToast("No saved playlists yet — X saves the queue", ToastInfo())
		}
	default:
		return false
	}
	musicPEQSave(config)
	// Bit-perfect holds spatial audio back: publish the switch at once.
	spatialBind(config)
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
	// The full-screen visualizer and search (music_nowplaying_rows.go).
	if musicNowPlayingActivate(row.kind) {
		return
	}
	// The rows that open a panel of their own (musicpanels.go).
	if musicPodSettingsActivate(row.kind, config) {
		return
	}
	if row.kind == msRowShortcuts {
		shortcutsOpen = true
		PlayActivateSound()
		return
	}
	if row.kind == msRowPlaylists {
		if pl, ok := musicSelectedPlaylist(); ok {
			if musicPlaySavedPlaylist(config, pl.Name) {
				showToast("Playing "+pl.Name, ToastSuccess())
			} else {
				showToast("That playlist has no playable tracks", ToastWarn())
			}
			PlayActivateSound()
			return
		}
		// Nothing saved yet: A starts the same flow X does.
		vkOpenPlaylistName()
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
	wasOpen := virtualKeyboardActive
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
	case sdl.K_x:
		// Save the current queue under a name typed on the on-screen keyboard.
		vkOpenPlaylistName()
	case sdl.K_y:
		// Export the selected playlist as M3U8 for a PC or another card.
		musicExportSelectedPlaylist()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_k:
		closeMusicSettingsPanel()
	}
	// X (or Space on a row that asks for a name) opened the keyboard: its own
	// character must not be typed into the name (vkopenertext.go).
	vkDropOpenerText(e, wasOpen)
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
		// On the visualizer row Right steps the style and A shows it, as
		// Right and Enter do on a keyboard (music_nowplaying_rows.go).
		if e.Button == sdl.CONTROLLER_BUTTON_DPAD_RIGHT && rows[cur].kind == msRowNowVisualizer {
			musicSettingsAdjust(rows[cur], 1, config)
		} else {
			musicSettingsActivate(rows[cur], config)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		musicExportSelectedPlaylist()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		// Start too: in the player it is the button that opened the panel.
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
	// The settings sheet opens with the shared page header, measured before the
	// card is sized so the rows start where the header band ends.
	subtitle := "Settings"
	if musicSceneActive() {
		subtitle = "Now playing & settings"
	}
	header := UIPanelHeader("Audio", subtitle, "media")
	band := UIPanelHeaderBand(config, header, 12)
	panelW := int32(860)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := band + int32(len(rows))*rowH + 12 + 2*smallH + 20 + footerH
	// Leave the scene's footer band uncovered: a centred panel capped at
	// screenHeight-56 still ended 28px into the band, under the player's own
	// hint row. The rows are scrolled, so a shorter card loses no content.
	maxPanelH := screenHeight - 2*footerBarHeight()
	if panelH > maxPanelH {
		panelH = maxPanelH
	}
	x := (screenWidth - panelW) / 2
	y := (screenHeight - panelH) / 2
	if footerY := sceneFooterBand().Y; y+panelH > footerY {
		y = footerY - panelH
	}
	card := sdl.Rect{X: x, Y: y, W: panelW, H: panelH}
	musicSettings.panelRect = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	musicSettings.rowHits = musicSettings.rowHits[:0]
	listTop := y + band
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
		rect := sdl.Rect{X: x + OverlayPadX, Y: ry, W: panelW - 2*OverlayPadX, H: rowH - 4}
		if i == musicSettings.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(ColorCardFocus, 255))
			strokeRoundedRectAt(renderer, rect, homeFocusGlide.apply(rect), 10, 1, accentColor)
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
		for _, line := range wrapMeasured([]rune(detail), panelW-2*OverlayPadX, measure) {
			renderText(renderer, config, font, line, ColorTextTertiary(), x+OverlayPadX, detailY)
			detailY += smallH + 2
		}
	}

	hint := hintLine(FooterHint{"Up/Down", "Row"}, FooterHint{"Left/Right", "Change"},
		FooterHint{"A", "Select"}, FooterHint{"Y", "Export"}, FooterHint{"B", "Close"})
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, sdl.Color{R: 110, G: 125, B: 150, A: 200},
		x+(panelW-int32(hw))/2, y+panelH-footerH+6)
}

// musicArtOpacitySteps are the values the settings row walks through.
var musicArtOpacitySteps = []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

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
