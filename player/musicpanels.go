package main

// musicpanels.go — the music player's modal panels in one place: the 31-band
// equaliser (musicpeq.go), Sound effects (musicfx_ui.go) and the AutoEQ
// headphone picker (musicautoeq.go). Every layout routes its keys, pad
// presses, taps and drawing through here, so a panel opened from the iPod
// screen behaves exactly as one opened from Auralis Pro.

import "github.com/veandco/go-sdl2/sdl"

// musicPanelsOpen reports whether one of the panels owns the input.
func musicPanelsOpen() bool {
	return musicEQ.panel || musicFXPanel.open || autoEQPanel.open
}

// musicPanelsKey routes a key to the open panel.
func musicPanelsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if handleAutoEQKey(e, config) {
		return true
	}
	if handleMusicFXKey(e, config) {
		return true
	}
	return handleMusicPEQKey(e)
}

// musicPanelsPad routes a pad press to the open panel. While the on-screen
// keyboard is up (the AutoEQ search) the keyboard gets it.
func musicPanelsPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if virtualKeyboardActive {
		return false
	}
	switch {
	case autoEQPanel.open:
		return ctrlAutoEQ(e, config)
	case musicFXPanel.open:
		return ctrlMusicFXPanel(e, config)
	case musicEQ.panel:
		return ctrlMusicPEQPanel(e)
	}
	return false
}

// musicPanelsClick routes a tap to the open panel.
func musicPanelsClick(mx, my int32, config *Config) bool {
	if handleAutoEQClick(mx, my, config) {
		return true
	}
	if handleMusicFXClick(mx, my, config) {
		return true
	}
	return HandleMusicPEQClick(mx, my)
}

// renderMusicPanels draws whichever panel is open, over the layout.
func renderMusicPanels(renderer *sdl.Renderer, config *Config) {
	renderMusicPEQPanel(renderer, config)
	renderMusicFXPanel(renderer, config)
	renderAutoEQPanel(renderer, config)
}

// musicPanelsCloseAll closes every panel (leaving the player).
func musicPanelsCloseAll() {
	musicEQ.panel = false
	musicFXPanel.open = false
	autoEQPanel.open = false
}

// The Audio settings rows this file answers for.
const (
	msRowAutoEQ musicSettingsRowKind = 300 + iota
	msRowSoundFX
	msRowVisualizer
	msRowServers
)

// musicPodSettingsValue is the right-hand text of those rows.
func musicPodSettingsValue(kind musicSettingsRowKind, config *Config) (string, bool) {
	switch kind {
	case msRowAutoEQ:
		if name := musicAutoEQName(); name != "" {
			return name, true
		}
		return "Off", true
	case msRowSoundFX:
		return musicFXStatus(), true
	case msRowVisualizer:
		return musicVizNames[musicVizStyle(config)], true
	case msRowServers:
		return musicServersSummary(), true
	}
	return "", false
}

// musicPodSettingsAdjust handles Left/Right on those rows.
func musicPodSettingsAdjust(kind musicSettingsRowKind, dir int, config *Config) bool {
	switch kind {
	case msRowVisualizer:
		musicVizStep(config, dir)
		return true
	case msRowAutoEQ, msRowSoundFX, msRowServers:
		return musicPodSettingsActivate(kind, config)
	}
	return false
}

// musicPodSettingsActivate handles A on the rows that open something: the
// settings sheet closes so the panel (or the phone code) gets the pad.
func musicPodSettingsActivate(kind musicSettingsRowKind, config *Config) bool {
	switch kind {
	case msRowAutoEQ:
		musicSettings.open = false
		musicAutoEQOpen(false)
	case msRowSoundFX:
		musicSettings.open = false
		musicFXPanelOpen()
	case msRowPEQ:
		musicSettings.open = false
		musicEQ.panel = true
		musicFXPanel.open = false
		PlayActivateSound()
	case msRowServers:
		musicSettings.open = false
		musicServersPhoneOpen("")
	default:
		return false
	}
	return true
}
