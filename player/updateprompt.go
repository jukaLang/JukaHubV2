package main

// updateprompt.go — the "a new JukaHub release is available" prompt.
//
// The startup check only advertises a release (see autoupdate.go); this file
// turns that advertisement into a visible, dismissible prompt:
//   - a download badge in the status cluster next to the clock/bell, and
//   - one modal per launch with "Update now" / "Not now".
//
// Choosing Update now opens the Patch scene and its install confirmation, which
// still requires the user to confirm the high-risk install. Nothing downloads
// until that confirmation, and Not now leaves the release advertised in Patch.

import (
	"fmt"

	"github.com/veandco/go-sdl2/sdl"
)

var updatePrompt struct {
	open     bool
	focus    int // 0 = Update now, 1 = Not now
	seen     bool
	badge    sdl.Rect
	nowBtn   sdl.Rect
	laterBtn sdl.Rect
}

func updateAvailable() bool { return availableUpdateTag() != "" }

// openUpdatePrompt shows the modal (from a badge click or the automatic offer).
func openUpdatePrompt() {
	if !updateAvailable() {
		return
	}
	updatePrompt.open = true
	updatePrompt.focus = 0
	updatePrompt.seen = true
}

func closeUpdatePrompt() {
	updatePrompt.open = false
	updatePrompt.nowBtn = sdl.Rect{}
	updatePrompt.laterBtn = sdl.Rect{}
}

// maybeOfferUpdate opens the prompt once per launch, after the app has finished
// starting and no other modal owns the screen. Calling it every frame is safe.
func maybeOfferUpdate() {
	if updatePrompt.open || updatePrompt.seen || !updateAvailable() {
		return
	}
	if virtualKeyboardActive || toolsPanel.open || imageViewerPath != "" || qs.open ||
		patchModal != nil || transitionPhase != "none" || videoFullscreen || playerPhase() != "idle" ||
		nc.open || tb.startOpen || processPanel.open || ipPanelOpen || emuPanel.open || gpPanel.open || winAppsPanel.open || portsPanel.open || raPanel.open ||
		wpOpen || socialNotifOpen || shortcutsOpen || globalSearchOpen || clipSearchOpen {
		return
	}
	openUpdatePrompt()
}

// activateUpdatePrompt runs the focused choice.
func activateUpdatePrompt(config *Config) {
	if updatePrompt.focus == 0 {
		closeUpdatePrompt()
		if idx := sceneIndexExact(config, "Patch"); idx >= 0 {
			changeSceneTo(config, idx)
			openInstallUpdateModal()
			ShowToast("Confirm the update in Patch to download it", ToastKindInfo)
		} else {
			ShowToast("This build has no Patch scene — update from Releases", ToastKindWarning)
		}
		return
	}
	closeUpdatePrompt()
}

// ──────────────────────────────────────────────────────────────────────
// Drawing
// ──────────────────────────────────────────────────────────────────────

// drawUpdateBadge draws the download pill ending at rightX and returns the
// horizontal space it consumed (including the gap before the bell). It records
// the hit rect used by handleChromeBarClick.
func drawUpdateBadge(renderer *sdl.Renderer, config *Config, rightX, y, h int32) int32 {
	updatePrompt.badge = sdl.Rect{}
	if renderer == nil || !updateAvailable() {
		return 0
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return 0
	}
	text := "Update"
	tw, _, _ := font.SizeUTF8(text)
	pillW := int32(tw) + 46
	pillH := h
	if pillH <= 0 {
		pillH = int32(font.Height()) + 8
	}
	x := rightX - pillW
	rect := sdl.Rect{X: x, Y: y, W: pillW, H: pillH}
	fillRoundedRect(renderer, x, y, pillW, pillH, pillH/2, WithAlpha(ColorWarning, 40))
	strokeRoundedRect(renderer, x, y, pillW, pillH, pillH/2, 1, WithAlpha(ColorWarning, 200))

	// Down-arrow drawn with lines: Inter may not contain U+2193 on the device.
	cx := x + 16
	cy := y + pillH/2
	renderer.SetDrawColor(ColorWarning.R, ColorWarning.G, ColorWarning.B, 235)
	renderer.DrawLine(cx, cy-7, cx, cy+5)
	renderer.DrawLine(cx-5, cy, cx, cy+5)
	renderer.DrawLine(cx+5, cy, cx, cy+5)

	renderText(renderer, config, font, text, ColorTextPrimary(), x+30, y+(pillH-int32(font.Height()))/2)
	updatePrompt.badge = rect
	return pillW + SpaceSM
}

func renderUpdatePrompt(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !updatePrompt.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	smallH, medH := int32(font.Height()), int32(medium.Height())
	cardW := int32(620)
	if cardW > screenWidth-64 {
		cardW = screenWidth - 64
	}
	cardH := int32(250)
	if cardH > screenHeight-64 {
		cardH = screenHeight - 64
	}
	x := (screenWidth - cardW) / 2
	y := (screenHeight - cardH) / 2
	fillRoundedRect(renderer, x, y, cardW, cardH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, x, y, cardW, cardH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 140)
	renderer.FillRect(&sdl.Rect{X: x + 18, Y: y + 1, W: cardW - 36, H: 2})

	renderText(renderer, config, medium, "JukaHub update available", ColorTextPrimary(), x+28, y+24)
	version := availableUpdateLabel()
	body := fmt.Sprintf("Release %s is ready to install.\nDownload and restart when you are ready — nothing is installed without your confirmation.", version)
	renderText(renderer, config, font, body, ColorTextTertiary(), x+28, y+24+medH+12)
	renderText(renderer, config, font, "You can always install it later from Tools → Patch.", ColorTextTertiary(), x+28, y+24+medH+12+smallH*2+8)

	// Buttons side by side, right-aligned.
	btnH := int32(42)
	updateW := int32(160)
	laterW := int32(120)
	laterX := x + cardW - 28 - laterW
	updateX := laterX - 12 - updateW
	btnY := y + cardH - 28 - btnH
	updatePrompt.laterBtn = sdl.Rect{X: laterX, Y: btnY, W: laterW, H: btnH}
	updatePrompt.nowBtn = sdl.Rect{X: updateX, Y: btnY, W: updateW, H: btnH}

	updateFill := WithAlpha(ColorInfo, 90)
	if updatePrompt.focus == 0 {
		updateFill = WithAlpha(uiHighlightColor(), 220)
	}
	updatePrompt.nowBtn = UIButton(renderer, config, updatePrompt.nowBtn, "Update now", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       updateFill,
		HoverFill:  updateFill,
		FocusFill:  updateFill,
		Border:     uiHighlightColor(),
		LabelColor: ColorTextPrimary(),
		Font:       font,
		Pill:       true,
		NoShadow:   true,
	}, UIButtonState{Focused: updatePrompt.focus == 0})
	updatePrompt.laterBtn = UIButton(renderer, config, updatePrompt.laterBtn, "Not now", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       ColorIconSurface,
		HoverFill:  ColorIconSurface,
		FocusFill:  ColorIconSurface,
		LabelColor: ColorTextSecondary(),
		Font:       font,
		Pill:       true,
		NoShadow:   true,
	}, UIButtonState{Focused: updatePrompt.focus == 1})
}

// ──────────────────────────────────────────────────────────────────────
// Input
// ──────────────────────────────────────────────────────────────────────

func ctrlUpdatePrompt(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		updatePrompt.focus = 1 - updatePrompt.focus
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		PlayActivateSound()
		activateUpdatePrompt(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeUpdatePrompt()
		PlayBackSound()
	}
	return true
}

func handleUpdatePromptKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !updatePrompt.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_RIGHT, sdl.K_TAB:
		updatePrompt.focus = 1 - updatePrompt.focus
	case sdl.K_RETURN, sdl.K_SPACE:
		activateUpdatePrompt(config)
	case sdl.K_ESCAPE:
		closeUpdatePrompt()
	}
	return true
}

func handleUpdatePromptMouse(mx, my int32, config *Config) bool {
	if !updatePrompt.open {
		return false
	}
	if pointInRect(mx, my, updatePrompt.nowBtn) {
		updatePrompt.focus = 0
		activateUpdatePrompt(config)
		return true
	}
	if pointInRect(mx, my, updatePrompt.laterBtn) {
		updatePrompt.focus = 1
		activateUpdatePrompt(config)
		return true
	}
	// Clicking the dimmed background dismisses it like "Not now".
	closeUpdatePrompt()
	return true
}
