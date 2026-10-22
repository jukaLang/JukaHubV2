package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type launchFailureState struct {
	open   bool
	target string
	reason string
	fix    string
	log    string
}

var launchFailure launchFailureState

func launchFailureSuggestion(err error) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "game folder") && strings.Contains(message, "not found") {
		return "Put the game folder under one of the configured game roots, or correct the profile’s folder path."
	}
	// exec.ErrNotFound's message says "executable file not found", so it must
	// be recognized before the generic profile-executable branch: this error
	// means the launcher itself is absent, not that a profile path is wrong.
	if errors.Is(err, exec.ErrNotFound) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if strings.Contains(message, "executable") && (strings.Contains(message, "not found") || strings.Contains(message, "no such file")) {
		return "Check the profile’s executable path and make sure the game files are installed."
	}
	if strings.Contains(message, "unsupported launcher file type") {
		return "Use a supported launcher file (.exe, .bat, .cmd, .lnk, .url or .sh), then rescan."
	}
	if errors.Is(err, os.ErrNotExist) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrPermission) {
		return "The launcher is not executable. Check file permissions or reinstall the emulator."
	}
	if strings.Contains(message, "permission denied") {
		return "The launcher is not executable. Check file permissions or reinstall the emulator."
	}
	if strings.Contains(message, "no such file") || strings.Contains(message, "not found") {
		return "A launcher or required runtime is missing. Reinstall the emulator and check its runtime setup."
	}
	return "Check the emulator's required BIOS, runtime, and launcher configuration. The game log may contain more detail."
}

func showLaunchFailureDialog(target string, err error, logPath string) {
	if err == nil {
		return
	}
	launchFailure = launchFailureState{
		open:   true,
		target: strings.TrimSpace(target),
		reason: strings.TrimSpace(err.Error()),
		fix:    launchFailureSuggestion(err),
		log:    strings.TrimSpace(logPath),
	}
}

func closeLaunchFailureDialog() {
	launchFailure = launchFailureState{}
	PlayBackSound()
}

func handleLaunchFailureKey(e *sdl.KeyboardEvent) bool {
	if !launchFailure.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	closeLaunchFailureDialog()
	return true
}

func handleLaunchFailureController(e *sdl.ControllerButtonEvent) bool {
	if !launchFailure.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	closeLaunchFailureDialog()
	return true
}

func handleLaunchFailureMouse() bool {
	if !launchFailure.open {
		return false
	}
	closeLaunchFailureDialog()
	return true
}

func renderLaunchFailure(renderer *sdl.Renderer, config *Config) {
	if !launchFailure.open || renderer == nil || config == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	large, _ := getCachedFont(config, "large")
	if font == nil || medium == nil || large == nil {
		return
	}
	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := int32(430)
	if panelH > screenHeight-48 {
		panelH = screenHeight - 48
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 205)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)

	title := "Couldn't launch"
	if launchFailure.target != "" {
		title += " " + launchFailure.target
	}
	renderText(renderer, config, large, ellipsize(large, title, panelW-48), ColorTextPrimary(), panelX+24, panelY+24)

	y := panelY + 92
	renderText(renderer, config, medium, "Reason", ColorTextPrimary(), panelX+24, y)
	y += int32(medium.Height()) + 8
	for _, line := range wrapMeasured([]rune(launchFailure.reason), panelW-48, func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}) {
		renderText(renderer, config, font, line, ColorTextSecondary(), panelX+24, y)
		y += int32(font.Height()) + 4
	}
	y += 14
	renderText(renderer, config, medium, "Suggested fix", ColorTextPrimary(), panelX+24, y)
	y += int32(medium.Height()) + 8
	for _, line := range wrapMeasured([]rune(launchFailure.fix), panelW-48, func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}) {
		renderText(renderer, config, font, line, ColorTextSecondary(), panelX+24, y)
		y += int32(font.Height()) + 4
	}
	if launchFailure.log != "" {
		name := launchFailure.log
		if i := strings.LastIndexAny(name, `/\\`); i >= 0 {
			name = name[i+1:]
		}
		renderText(renderer, config, font, "Log: "+name, ColorTextTertiary(), panelX+24, panelY+panelH-82)
	}
	footer := hintLine(FooterHint{"A / Enter / B", "Dismiss"})
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(), panelX+(panelW-int32(fw))/2, panelY+panelH-34)
}
