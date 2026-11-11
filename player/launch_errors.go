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

type launchFailureKind uint8

const (
	launchFailureGameFolderMissing launchFailureKind = iota + 1
	launchFailureProfileExecutableMissing
	launchFailureUnsupportedLauncherType
	launchFailureRuntimeMissing
)

// launchFailureCause adds a stable category while preserving the underlying
// error for errors.Is/As and for the reason shown in the dialog.
type launchFailureCause struct {
	kind launchFailureKind
	err  error
}

func (e *launchFailureCause) Error() string { return e.err.Error() }
func (e *launchFailureCause) Unwrap() error { return e.err }

func withLaunchFailureKind(kind launchFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &launchFailureCause{kind: kind, err: err}
}

func launchFailureSuggestion(err error) string {
	var categorized *launchFailureCause
	if errors.As(err, &categorized) {
		switch categorized.kind {
		case launchFailureGameFolderMissing:
			return "Put the game folder under one of the configured game roots, or correct the profile’s folder path."
		case launchFailureProfileExecutableMissing:
			return "Check the profile’s executable path and make sure the game files are installed."
		case launchFailureUnsupportedLauncherType:
			return "Use a supported launcher file (.exe, .bat, .cmd, .lnk, .url or .sh), then rescan."
		case launchFailureRuntimeMissing:
			return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
		}
	}
	// An unclassified exec.ErrNotFound comes from the command or runtime. A
	// missing profile executable has its own category above.
	if errors.Is(err, exec.ErrNotFound) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrNotExist) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrPermission) {
		return "The launcher is not executable. Check file permissions or reinstall the emulator."
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
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

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
