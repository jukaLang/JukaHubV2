package main

// calibration_ui.go — the PPSSPP stick calibration wizard.
//
// The Smart Pro's left stick reaches PPSSPP through SDL, and two PPSSPP defaults
// make it feel broken: AnalogDeadzone is high enough that small pushes vanish,
// and AnalogLimiter (0.6 by default) clamps the stick to 60% of its range, which
// is why a full push reads as half input. The fix is two numbers in
// [ControlMapping]; measuring them is the hard part on a device with no
// keyboard.
//
// The wizard does the measuring: release the stick, roll it around the rim, and
// it writes AnalogDeadzone plus AnalogLimiter=0, keeping a one-time .bak.

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type calibrationPhase int

const (
	calPhaseIdle calibrationPhase = iota // waiting for the stick to rest
	calPhaseRoll                         // user rolls the stick around
	calPhaseDone                         // values written, result shown
)

const (
	calIdleDuration = 1200 * time.Millisecond
	calRollDuration = 4000 * time.Millisecond
	// calAxisFull is SDL's maximum absolute axis value.
	calAxisFull = 32767.0
)

type calibrationState struct {
	open       bool
	phase      calibrationPhase
	phaseStart time.Time
	idlePeak   float64
	maxDeflect float64
	path       string
	result     string
	err        string
	joy        *sdl.Joystick
	enter      uiEnter
	rect       sdl.Rect
	// live axis values (normalised) for the crosshair.
	x, y float64
}

var calibrationPanel calibrationState

// ppssppSearchRoots is where the wizard looks for an installed PPSSPP: the SD
// card first (where the handheld keeps it), then the per-user Linux locations.
func ppssppSearchRoots() []string {
	var roots []string
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		roots = append(roots, root)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots,
			filepath.Join(home, ".config", "ppsspp"),
			filepath.Join(home, ".local", "share", "ppsspp"),
		)
	}
	roots = append(roots, ".")
	return roots
}

// calibrationValues turns the measurement into the two ini numbers.
//
// A stick that cannot reach half range is the limiter's fault, not the
// deadzone's: the deadzone stays at the floor so the emulator hears the whole
// range the stick actually produces. Otherwise the deadzone sits just above the
// idle noise and never above half of what the stick really reached.
func calibrationValues(idlePeak, maxDeflection float64) (deadzone, limiter float64) {
	limiter = 0 // AnalogLimiter 0 disables the 60% clamp
	if math.IsNaN(idlePeak) || idlePeak < 0 {
		idlePeak = 0
	}
	if math.IsNaN(maxDeflection) || maxDeflection < 0 {
		maxDeflection = 0
	}
	if maxDeflection < 0.5 {
		return 0.02, limiter
	}
	dz := idlePeak*1.6 + 0.02
	if cap := maxDeflection * 0.5; dz > cap {
		dz = cap
	}
	if dz < 0.02 {
		dz = 0.02
	}
	if dz > 0.12 {
		dz = 0.12
	}
	return math.Floor(dz*1000) / 1000, limiter
}

// openStickCalibrationPanel starts the wizard. A missing PPSSPP install is said
// out loud instead of leaving the user in an empty modal.
func openStickCalibrationPanel(config *Config) {
	if calibrationPanel.open {
		closeStickCalibrationPanel()
	}
	qs.open = false
	toolsPanel.open = false
	smartHomePanel.open = false
	path := findPPSSPPConfig(ppssppSearchRoots())
	if path == "" {
		ShowToast("PPSSPP is not installed — no ppsspp.ini found", ToastKindWarning)
		return
	}
	calibrationPanel = calibrationState{
		open:       true,
		phase:      calPhaseIdle,
		phaseStart: time.Now(),
		path:       path,
		joy:        sdl.JoystickOpen(0),
	}
	calibrationPanel.enter.begin(uiClockMS(), 160)
	PlayActivateSound()
}

func closeStickCalibrationPanel() {
	if !calibrationPanel.open {
		return
	}
	calibrationPanel.open = false
	calibrationPanel.enter.cancel()
	if calibrationPanel.joy != nil {
		calibrationPanel.joy.Close()
		calibrationPanel.joy = nil
	}
	PlayBackSound()
}

// calibrationAxisMagnitude reads the left stick as a 0..1 magnitude. A value
// under a few hundred counts is drift, so it is treated as centred.
func calibrationAxisMagnitude(x, y float64) float64 {
	m := math.Hypot(x, y)
	if m < 600 {
		return 0
	}
	if m > calAxisFull {
		m = calAxisFull
	}
	return m / calAxisFull
}

// calibrationTick samples the pad and advances the phases. It runs on the render
// thread (SDL calls belong there) and never blocks.
func calibrationTick(now time.Time) {
	if !calibrationPanel.open || calibrationPanel.phase == calPhaseDone {
		return
	}
	if calibrationPanel.joy == nil {
		calibrationPanel.err = "no controller found"
		calibrationPanel.phase = calPhaseDone
		return
	}
	x := float64(calibrationPanel.joy.Axis(0))
	y := float64(calibrationPanel.joy.Axis(1))
	calibrationPanel.x, calibrationPanel.y = x/calAxisFull, y/calAxisFull
	mag := calibrationAxisMagnitude(x, y)

	switch calibrationPanel.phase {
	case calPhaseIdle:
		if mag > calibrationPanel.idlePeak {
			calibrationPanel.idlePeak = mag
		}
		if now.Sub(calibrationPanel.phaseStart) >= calIdleDuration {
			calibrationPanel.phase = calPhaseRoll
			calibrationPanel.phaseStart = now
		}
	case calPhaseRoll:
		if mag > calibrationPanel.maxDeflect {
			calibrationPanel.maxDeflect = mag
		}
		if now.Sub(calibrationPanel.phaseStart) >= calRollDuration {
			deadzone, limiter := calibrationValues(calibrationPanel.idlePeak, calibrationPanel.maxDeflect)
			if err := writePPSSPPStickFix(calibrationPanel.path, deadzone, limiter); err != nil {
				calibrationPanel.err = err.Error()
			} else {
				calibrationPanel.result = "deadzone " + trimZeroes(deadzone) + " · limiter off · backup saved"
				PlaySuccessSound()
				logPatch("ppsspp calibration: deadzone %.3f, limiter %.3f, %s", deadzone, limiter, calibrationPanel.path)
			}
			calibrationPanel.phase = calPhaseDone
		}
	}
}

// trimZeroes prints 0.040 as 0.04 for the wizard's summaries.
func trimZeroes(v float64) string {
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 3, 64), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// handleStickCalibrationKey is modal: while the wizard is open it owns the keys.
func handleStickCalibrationKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !calibrationPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeStickCalibrationPanel()
	case sdl.K_r, sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		if calibrationPanel.phase == calPhaseDone {
			openStickCalibrationPanel(config)
		}
	}
	return true
}

func handleStickCalibrationMouse(mx, my int32, config *Config) bool {
	if !calibrationPanel.open {
		return false
	}
	if calibrationPanel.rect.W > 0 && pointInRect(mx, my, calibrationPanel.rect) {
		if calibrationPanel.phase == calPhaseDone {
			openStickCalibrationPanel(config)
		}
		return true
	}
	closeStickCalibrationPanel()
	return true
}

func ctrlStickCalibration(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !calibrationPanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeStickCalibrationPanel()
	case sdl.CONTROLLER_BUTTON_A:
		if calibrationPanel.phase == calPhaseDone {
			openStickCalibrationPanel(config)
		}
	}
	return true
}

// renderStickCalibrationPanel draws the card: instructions, the live stick
// crosshair and the measured numbers.
func renderStickCalibrationPanel(renderer *sdl.Renderer, config *Config) {
	if !calibrationPanel.open || renderer == nil {
		return
	}
	calibrationTick(time.Now())

	medium, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if medium == nil {
		return
	}
	if small == nil {
		small = medium
	}
	smallH := int32(small.Height())
	medH := int32(medium.Height())

	panelW := int32(760)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := int32(380)
	if panelH > screenHeight-64 {
		panelH = screenHeight - 64
	}
	x := (screenWidth - panelW) / 2
	y := (screenHeight - panelH) / 2
	calibrationPanel.rect = sdl.Rect{X: x, Y: y, W: panelW, H: panelH}

	progress := 1.0
	if !calibrationPanel.enter.settled() {
		progress = calibrationPanel.enter.progress(uiClockMS())
	}
	offset := int32((1 - progress) * 24)

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	top := y + offset
	fillRoundedRect(renderer, x, top, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, x, top, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: x + 18, Y: top + 1, W: panelW - 36, H: 2})

	pad := int32(28)
	renderText(renderer, config, medium, "PPSSPP stick calibration", ColorTextPrimary(), x+pad, top+18)

	lineY := top + 18 + medH + 16
	line := func(text string, col sdl.Color) {
		renderText(renderer, config, small, ellipsize(small, text, panelW-2*pad), col, x+pad, lineY)
		lineY += smallH + 6
	}

	switch calibrationPanel.phase {
	case calPhaseIdle:
		line("Let go of the left stick. Measuring the resting position…", ColorTextSecondary())
		line("Drift at rest is why small pushes read as nothing.", ColorTextTertiary())
	case calPhaseRoll:
		line("Roll the stick around the outer edge, all the way round.", ColorTextSecondary())
		line("The limiter is removed too: it caps the stick at 60%.", ColorTextTertiary())
	default:
		if calibrationPanel.err != "" {
			line("Could not save: "+calibrationPanel.err, ColorToastWarn)
			line("Check that the SD card is writable and try again.", ColorTextTertiary())
		} else {
			line(calibrationPanel.result, ColorToastSuccess)
			line("Saved to "+calibrationPanel.path, ColorTextTertiary())
			line("Restart PPSSPP for the new values to load.", ColorTextTertiary())
		}
	}

	// Live stick crosshair: the dot is the current position, the ring the peak,
	// so reaching the rim is visible.
	const boxSize = int32(170)
	boxX := x + panelW - pad - boxSize
	boxY := top + 18 + medH + 22
	fillRoundedRect(renderer, boxX, boxY, boxSize, boxSize, 14, ColorSurfaceRow)
	strokeRoundedRect(renderer, boxX, boxY, boxSize, boxSize, 14, 1, ColorBorderDefault)
	cx := boxX + boxSize/2
	cy := boxY + boxSize/2
	renderer.SetDrawColor(ColorBorderDefault.R, ColorBorderDefault.G, ColorBorderDefault.B, 120)
	renderer.DrawLine(cx-boxSize/2+10, cy, cx+boxSize/2-10, cy)
	renderer.DrawLine(cx, boxY+10, cx, boxY+boxSize-10)

	scale := float64(boxSize/2-12) * 0.5
	if r := calibrationPanel.maxDeflect * scale; r > 0 {
		strokeCircle(renderer, cx, cy, int32(r), WithAlpha(accentColor, 160))
	}
	fillCircle(renderer, cx+int32(calibrationPanel.x*scale), cy+int32(calibrationPanel.y*scale), 6,
		sdl.Color{R: 245, G: 250, B: 255, A: 235})

	measured := "rest " + trimZeroes(calibrationPanel.idlePeak) + " · reach " + trimZeroes(calibrationPanel.maxDeflect)
	renderText(renderer, config, small, measured, ColorTextTertiary(), boxX, boxY+boxSize+8)

	hint := hintLine(FooterHint{"B", "Cancel"})
	if calibrationPanel.phase == calPhaseDone {
		hint = hintLine(FooterHint{"A", "Measure Again"}, FooterHint{"B", "Close"})
	}
	renderText(renderer, config, small, hint, ColorTextSecondary(), x+pad, top+panelH-16-smallH)
}
