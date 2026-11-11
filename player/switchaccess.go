package main

// switchaccess.go — A11Y-9: one- and two-button scanning.
//
// A player who can press a single switch cannot drive a d-pad menu. In switch
// access the focus walks the scene's focus graph (focus.go BuildGraph) on its
// own, one control at a time, and the switch presses the highlighted control.
// In two-button mode the second switch steps early instead of waiting out the
// dwell.
//
// The scanner moves the same focus the pad moves, so every screen's ring,
// hints and activation path apply unchanged: no scene has to know the mode is
// on. It stands aside while the on-screen keyboard is up, where A is a letter
// and the focus graph is not the thing being typed into.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	a11ySwitchOff = "off"
	a11ySwitchOne = "one"
	a11ySwitchTwo = "two"

	a11ySwitchDefaultIntervalMS = 1200
)

// a11ySwitchIntervalSteps are the dwell times the settings row offers, in
// milliseconds.
var a11ySwitchIntervalSteps = []int{800, 1200, 1600, 2000, 3000}

// a11ySwitchModeValue normalises a stored mode. Anything unknown is off.
func a11ySwitchModeValue(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case a11ySwitchOne:
		return a11ySwitchOne
	case a11ySwitchTwo:
		return a11ySwitchTwo
	}
	return a11ySwitchOff
}

// a11ySwitchIntervalMs is the highlight dwell, clamped to the range the
// settings row offers.
func a11ySwitchIntervalMs() uint64 {
	return uint64(clampInt(a11y.switchIntervalMs, 400, 5000))
}

// a11ySwitchIntervalLabel is the row's spelling of a dwell time.
func a11ySwitchIntervalLabel(ms int) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64) + " s"
}

// switchScanState is the dwell clock. A zero last means "not armed yet", so
// turning the mode on highlights a control before the scan starts moving.
type switchScanState struct {
	last uint64
}

var switchScan switchScanState

func switchScanReset() { switchScan = switchScanState{} }

// switchScanOn reports whether scanning is active.
func switchScanOn() bool { return a11ySwitchModeValue(a11y.switchMode) != a11ySwitchOff }

// switchScanTickAt advances the focus when the dwell has elapsed, and reports
// whether it moved. now is the loop's millisecond clock.
func switchScanTickAt(config *Config, now uint64) bool {
	if !switchScanOn() {
		switchScanReset()
		return false
	}
	// The keyboard owns the pad and the focus graph is not what is being
	// typed into, so the scan holds still.
	if virtualKeyboardActive {
		switchScan.last = now
		return false
	}
	if switchScan.last == 0 {
		switchScan.last = now
		return false
	}
	if now-switchScan.last < a11ySwitchIntervalMs() {
		return false
	}
	switchScan.last = now
	return focusEngine.ScanStep(1) >= 0
}

func switchScanTick(config *Config) { switchScanTickAt(config, sdl.GetTicks64()) }

// switchScanConfirm presses the highlighted control through the same
// activation the pad uses: the top bar first, then the scene's own element.
func switchScanConfirm(renderer *sdl.Renderer, config *Config) bool {
	if !switchScanOn() || config == nil {
		return false
	}
	focusEngine.SyncSelected()
	idx := focusEngine.Confirm()
	if idx < 0 {
		return false
	}
	if activateChromeFocus(config) {
		return true
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	elems := config.Scenes[currentSceneIndex].Elements
	if idx >= len(elems) {
		return false
	}
	if elems[idx].Type == "input" {
		handleInputSelection(renderer, config, currentSceneIndex, idx)
		return true
	}
	handleTrigger(renderer, config, elems[idx])
	return true
}

// switchScanPadPress owns the scan's buttons on an ordinary screen, after the
// overlays have declined the press. One-button mode only presses; two-button
// mode also steps with B. It reports whether the press belonged to the scan.
func switchScanPadPress(renderer *sdl.Renderer, e *sdl.ControllerButtonEvent, config *Config) bool {
	if !switchScanOn() || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		return switchScanConfirm(renderer, config)
	case sdl.CONTROLLER_BUTTON_B:
		if a11ySwitchModeValue(a11y.switchMode) != a11ySwitchTwo {
			// One switch: B keeps its back meaning, or the user could never
			// leave a screen.
			return false
		}
		switchScan.last = sdl.GetTicks64()
		return focusEngine.ScanStep(1) >= 0
	}
	return false
}
