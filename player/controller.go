package main

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Controller Input — Xbox 360 / Trimui Smart Pro D-pad routing
//
// Button mapping (Xbox 360 → SDL):
//   A (bottom)     → CONTROLLER_BUTTON_A         → Select / confirm
//   B (right)      → CONTROLLER_BUTTON_B         → Back / cancel
//   X (left)       → CONTROLLER_BUTTON_X         → Secondary action
//   Y (top)        → CONTROLLER_BUTTON_Y         → Create / tertiary
//   D-pad          → CONTROLLER_BUTTON_DPAD_*    → Navigate
//   L1 / R1        → LEFTSHOULDER / RIGHTSHOULDER → Scene switch / tab / page
//   Start           → CONTROLLER_BUTTON_START     → Open start menu / pause
//   Back            → CONTROLLER_BUTTON_BACK      → Settings / menu
//   Left stick     → CONTROLLERAXISMOTION        → Navigate (deadzone)
// ──────────────────────────────────────────────────────────────────────

// handleControllerEvent routes all controller button events to overlays and scenes.
// Returns true if consumed.
func handleControllerEvent(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}

	// ── Overlay priority (highest to lowest) ──
	// The IPStream library is modal: nothing underneath may react.
	if ipPanelOpen {
		return ctrlIPPanel(e, config)
	}
	// The process list is modal: nothing underneath may react while it is open.
	if processPanel.open {
		return ctrlProcessPanel(e, config)
	}
	// Update prompt is the topmost modal.
	if updatePrompt.open {
		return ctrlUpdatePrompt(e, config)
	}
	// The browser's link picker is modal while it is open.
	if browserLinkPanel.open {
		return ctrlBrowserLinks(e, config)
	}
	// The Phone Companion QR card is modal, and B is how a phone closes it
	// after pairing: the phone's remote pushes a real B event.
	if companion.open {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			closeCompanionOverlay()
			PlayBackSound()
		case sdl.CONTROLLER_BUTTON_Y:
			companionRestart()
		}
		return true
	}
	// Watch Party is modal: nothing underneath may react while it is open.
	if partyUI.open {
		return partyHandleController(e, config)
	}
	// Community hub is the topmost modal when open.
	if communityPanel.open {
		return ctrlCommunity(e, config)
	}
	// Developer / power-user tools sit above the regular overlays.
	if powerTools.open {
		return ctrlPowerTools(e, config)
	}
	// RetroAchievements is modal; the on-screen keyboard gets the controller
	// while a credential field is being typed.
	if raPanel.open && !virtualKeyboardActive {
		return ctrlRetroAchievements(e, config)
	}
	// Game Profiles is modal and sits above the panel that launched it.
	if gpPanel.open {
		return ctrlGameProfiles(e, config)
	}
	// Windows Apps is modal in the same way.
	if winAppsPanel.open {
		return ctrlWindowsApps(e, config)
	}
	// Remote Cast panel is modal while open.
	if castPanel.open {
		return ctrlCastPanel(e, config)
	}
	// Audio settings are modal: the panel owns the d-pad while open.
	if musicSettings.open {
		return ctrlMusicSettings(e, config)
	}
	// Accessibility settings are modal: the panel owns the d-pad while open.
	if a11y.open {
		return ctrlAccessibilityPanel(e, config)
	}
	// Smart Home is modal too: the d-pad walks its rows.
	if smartHomePanel.open {
		return ctrlSmartHomePanel(e, config)
	}
	// PS Remote Play installer/launcher is modal too.
	if chiakiPanel.open {
		return ctrlChiakiPanel(e)
	}
	// PortMaster Ports is modal too.
	if portsPanel.open {
		return ctrlPortsPanel(e)
	}
	// Tools is modal: nothing underneath may react while it is open.
	if toolsPanel.open {
		return ctrlToolsPanel(e, config)
	}
	if emuPanel.open {
		return ctrlEmuPanel(e, config)
	}
	// The notification centre is a top-bar overlay: while it is open the D-pad
	// walks its rows and A/B close it, matching handleChromeOverlayKey so the
	// controller and the keyboard behave the same on every scene, Home included.
	if nc.open {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			notifNavigate(-1)
			PlayNavSound()
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			notifNavigate(1)
			PlayNavSound()
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B,
			sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
			notifDismiss()
			PlayBackSound()
		}
		return true
	}
	// The Local weather card floats above every scene, so it consumes the
	// controller until it is dismissed.
	if weatherPopoverOpen {
		return ctrlWeatherPopover(e)
	}
	if shortcutsOpen {
		return ctrlShortcuts(e)
	}
	if clipSearchOpen {
		return ctrlClipboard(e)
	}
	if globalSearchOpen {
		return ctrlGlobalSearch(e, config)
	}
	if qs.open {
		return ctrlQuickSettings(e, config)
	}
	if wpOpen {
		return ctrlWallpaper(e)
	}
	if socialNotifOpen {
		return ctrlSocialNotif(e)
	}
	if imageViewerPath != "" {
		return ctrlImageViewer(e)
	}

	// A failed helper-tool install keeps its card on screen with an
	// "A: retry" hint; A starts the download again instead of activating the
	// scene underneath (the usual failure is launching before WiFi is joined).
	if e.Button == sdl.CONTROLLER_BUTTON_A {
		if tool, ok := installBarRetryPending(); ok {
			PlayActivateSound()
			switch tool {
			case chiakiAppName:
				retryChiakiInstall()
			case chiakiPS5AppName:
				retryChiakiPS5Install()
			default:
				retryHelperToolInstall(tool)
			}
			return true
		}
	}
	return false
}

// ── Shortcuts overlay ──

func ctrlShortcuts(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		shortcutsOpen = false
		PlayBackSound()
		return true
	}
	return true // consume all
}

// ── Clipboard overlay ──

func ctrlClipboard(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if clipCursorIdx > 0 {
			clipCursorIdx--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if clipCursorIdx < len(clipHistory)-1 {
			clipCursorIdx++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if clipCursorIdx >= 0 && clipCursorIdx < len(clipHistory) {
			sdl.SetClipboardText(clipHistory[clipCursorIdx].text)
			ShowToast("Copied to clipboard", ToastKindSuccess)
			clipSearchOpen = false
			clipSearchBuf = ""
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		clipSearchOpen = false
		clipSearchBuf = ""
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_X:
		if clipCursorIdx >= 0 && clipCursorIdx < len(clipHistory) {
			clipHistory = append(clipHistory[:clipCursorIdx], clipHistory[clipCursorIdx+1:]...)
			if clipCursorIdx >= len(clipHistory) {
				clipCursorIdx = len(clipHistory) - 1
			}
			PlayNavSound()
		}
	}
	return true
}

// ── Global search ──

func ctrlGlobalSearch(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if gsCursor > 0 {
			gsCursor--
		} else if len(gsResults) > 0 {
			gsCursor = len(gsResults) - 1
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if gsCursor < len(gsResults)-1 {
			gsCursor++
		} else {
			gsCursor = 0
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if gsCursor >= 0 && gsCursor < len(gsResults) && gsResults[gsCursor].Action != nil {
			gsResults[gsCursor].Action(config)
			gsQuery = ""
			gsResults = nil
			globalSearchOpen = false
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		gsQuery = ""
		gsResults = nil
		globalSearchOpen = false
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_X:
		if len(gsQuery) > 0 {
			gsQuery = ""
			gsResults = gsSearch("")
			gsCursor = 0
			PlayNavSound()
		}
	}
	return true
}

// ── Quick Settings ──

func ctrlQuickSettings(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		qsNavigate(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		qsNavigate(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		qsNavigateSlider(-5)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		qsNavigateSlider(5)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		qsSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		qsToggleOpen()
		PlayBackSound()
	default:
		return true
	}
	return true
}

// ── Wallpaper picker ──

func ctrlWallpaper(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if wpCursor > 0 {
			wpCursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if wpCursor < len(wpEntries)-1 {
			wpCursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		wpCursor -= 5
		if wpCursor < 0 {
			wpCursor = 0
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		wpCursor += 5
		if wpCursor >= len(wpEntries) {
			wpCursor = len(wpEntries) - 1
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if wpCursor >= 0 && wpCursor < len(wpEntries) {
			en := wpEntries[wpCursor]
			if strings.HasPrefix(en.name, "\U0001f4c1") {
				wpDir = en.path
				scanWallpaperDir()
			} else {
				appConfig.Variables.BackgroundImage = en.path
				dynbg.wallpaper = nil
				wpOpen = false
				ShowToast("[P] Wallpaper set!", ToastKindSuccess)
			}
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		wpOpen = false
		PlayBackSound()
	}
	return true
}

// ── Social notifications ──

func ctrlSocialNotif(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if socialNotifCursor > 0 {
			socialNotifCursor--
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if socialNotifCursor < len(socialNotifHistory)-1 {
			socialNotifCursor++
		}
	case sdl.CONTROLLER_BUTTON_X:
		socialNotifHistory = nil
		socialNotifCursor = 0
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		socialNotifOpen = false
		PlayBackSound()
	}
	return true
}

// ── Image viewer ──

func ctrlImageViewer(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_A:
		closeImageViewer()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		imageViewerStep(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		imageViewerStep(1)
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Left stick analog → D-pad mapping
// ──────────────────────────────────────────────────────────────────────

const stickDeadzone = 12000

func handleControllerAxis(e *sdl.ControllerAxisEvent) bool {
	// Left stick X axis → left/right
	if e.Axis == sdl.CONTROLLER_AXIS_LEFTX {
		if e.Value > stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
		}
		if e.Value < -stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_LEFT)
		}
	}
	// Left stick Y axis → up/down
	if e.Axis == sdl.CONTROLLER_AXIS_LEFTY {
		if e.Value > stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
		}
		if e.Value < -stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_UP)
		}
	}
	return false
}

var lastStickBtn uint8
var lastStickTime uint64

func handleStickDpad(btn uint8) bool {
	now := sdl.GetTicks64()
	if btn == lastStickBtn && now-lastStickTime < 120 {
		return false // repeat rate limiter
	}
	lastStickBtn = btn
	lastStickTime = now
	fakeEvent := &sdl.ControllerButtonEvent{
		Type:   sdl.CONTROLLERBUTTONDOWN,
		Button: btn,
	}
	// Re-queue the synthetic press instead of routing it here: the main loop
	// is the only place that knows about the virtual keyboard and the scene
	// handlers, and handleControllerEvent covers overlays only. Calling the
	// overlay router directly left the left stick unable to move focus in
	// menus or on the on-screen keyboard.
	if _, err := sdl.PushEvent(fakeEvent); err == nil {
		return true
	}
	return handleControllerEvent(fakeEvent, appConfig)
}
