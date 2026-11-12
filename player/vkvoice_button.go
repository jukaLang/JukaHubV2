package main

// vkvoice_button.go — a button for voice typing, and voice typing that starts
// by itself.
//
// Voice typing (vkvoice.go) sat behind the mic chip in the keyboard's top row
// (Up, across, A) or Menu, which a handheld may have set to go back. Testers
// who text by voice asked for one press: R2 now starts listening while the
// on-screen keyboard is up, and the next press stops and sends what was said
// (one more while it is being written out cancels it — the mic chip's rule).
// It works in the main loop (controller.go) and in the modal loop the
// browser's address bar runs (core.go runInputModal). The pointer keeps its
// wheel where there is something to scroll: with the pointer up over the page
// above the keyboard L2/R2 still scroll it, and over the keyboard R2 is voice
// typing (virtualcursor.go). L2 gets nothing new. A keyboard keeps F4.
//
// Settings → Voice & AI → "Start voice typing when the keyboard opens"
// (vkVoiceAuto, off by default) is for people whose main way of texting is
// their voice: a message field — the Chat composer, in every tab — starts
// listening the moment its keyboard opens. Never a password, a key, an
// address, a search or a command: only vkVoiceAutoTarget's fields. A start
// that hears nothing for a few seconds stops by itself, so a keyboard opened
// to type does not keep the microphone on.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vkVoiceAutoQuietMs is how long an automatic start listens for a first
	// word before it stops on its own.
	vkVoiceAutoQuietMs = 7000
	// vkVoiceAutoSettleMs is the start of an automatic recording that does
	// not count as speech: the start chime, heard by the microphone.
	vkVoiceAutoSettleMs = 600
)

// vkVoiceAutoState follows the setting's starts. Main thread only.
var vkVoiceAutoState struct {
	session uint64 // the keyboard session last looked at (vkSessionAnim)
	live    bool   // the recording under way started by itself and has heard nothing yet
}

// vkVoiceButtonName is the voice typing button the hints name: R2 on a pad,
// F4 on a keyboard.
func vkVoiceButtonName() string {
	if hintsUseKeyboard() {
		return "F4"
	}
	return "R2"
}

// vkVoiceHintShown reports whether the keyboard's footer offers voice typing:
// the mic chip's own rule (a field that takes it, a microphone not known to
// be missing).
func vkVoiceHintShown() bool {
	return vkToolEnabled(vkToolMic)
}

// vkVoiceHint is the footer's badge for voice typing.
func vkVoiceHint() FooterHint {
	return FooterHint{Button: vkVoiceButtonName(), Label: "Voice"}
}

// vkVoiceTriggerBlocked reports something drawn above the keyboard that owns
// the pad (main.go draws them after it): R2 is theirs then.
func vkVoiceTriggerBlocked() bool {
	return voiceOrbModal() || launchFailure.open || recoveryCard.open || qrZoom.open ||
		alarmFiringActive() || ambientIsActive() || videoPrepareActive()
}

// vkVoiceTrigger is R2 pressed while the keyboard is up: voice typing on, or
// off and sent. It reports whether the press was the keyboard's; false sends
// it on to the shoulder routing.
func vkVoiceTrigger(config *Config) bool {
	if !virtualKeyboardActive || vkVoiceTriggerBlocked() {
		return false
	}
	vkVoiceAutoState.live = false
	if vkStripModeNow() == vkStripHidden && vkVoice.phase != vkVoiceRecording && vkVoice.phase != vkVoiceSending {
		// A password, a key, a command or a number: no voice typing, and no
		// strip to say so in.
		vkPlay(PlayErrorSound)
		return true
	}
	vkVoiceToggle(config)
	return true
}

// vkVoiceOwnsR2At reports whether R2, with the pointer up at logical (x, y),
// is voice typing rather than the wheel: the keyboard is up and the pointer is
// over it, where nothing scrolls.
func vkVoiceOwnsR2At(x, y float64) bool {
	if !virtualKeyboardActive || vkVoiceTriggerBlocked() {
		return false
	}
	return pointInRect(int32(x), int32(y)-vkAnimOffset(), vkPanelRect)
}

// vkVoiceModalAxis is a trigger moving in the modal keyboard loop
// (runInputModal), which has no shoulder routing: R2 toggles voice typing,
// and both triggers' latches follow them so the main loop finds them in step.
func vkVoiceModalAxis(e *sdl.ControllerAxisEvent, config *Config) {
	switch e.Axis {
	case sdl.CONTROLLER_AXIS_TRIGGERLEFT:
		triggerPressedEdge(0, e.Value)
	case sdl.CONTROLLER_AXIS_TRIGGERRIGHT:
		if !triggerPressedEdge(1, e.Value) {
			return
		}
		screensaverNotifyInput()
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		vkVoiceTrigger(config)
	}
}

// vkVoiceAutoTarget reports a field that starts listening by itself: one a
// message is written in.
func vkVoiceAutoTarget() bool {
	return vkTarget == vkTargetChat && !vkSecretTarget()
}

// vkVoiceAutoWanted reports whether this keyboard session starts listening on
// its own: the setting is on, the field is a message, and voice typing would
// work right now. The checks vkVoiceToggle makes out loud are made quietly
// here, so a keyboard opened offline or without a key just opens.
func vkVoiceAutoWanted(config *Config) bool {
	return vkVoiceAutoOn(config) && vkVoiceAutoTarget() && vkVoice.phase == vkVoiceIdle &&
		vkVoiceAvailable() && !voiceOrbActive() && strings.TrimSpace(groqAPIKey()) != "" && IsOnline()
}

// vkVoiceAutoTick runs with the keyboard's frame tick (vkVoiceTick): the
// first frame of a keyboard session starts listening when the setting asks
// for it, and an automatic start that hears nothing gives up quietly.
func vkVoiceAutoTick(config *Config) {
	if vkSessionAnim != vkVoiceAutoState.session {
		vkVoiceAutoState.session = vkSessionAnim
		vkVoiceAutoState.live = false
		if vkSessionAnim != 0 && vkVoiceAutoWanted(config) {
			vkVoiceToggle(config)
			vkVoiceAutoState.live = vkVoice.phase == vkVoiceRecording
		}
		return
	}
	if !vkVoiceAutoState.live {
		return
	}
	elapsed := sdl.GetTicks64() - vkVoice.started
	switch {
	case vkVoice.phase != vkVoiceRecording:
		vkVoiceAutoState.live = false
	case elapsed >= vkVoiceAutoSettleMs && vkVoice.level >= vkVoiceSilence:
		// Talking: from here it is ordinary voice typing.
		vkVoiceAutoState.live = false
	case elapsed >= vkVoiceAutoQuietMs:
		vkVoiceAutoState.live = false
		vkVoiceCancel()
		vkVoiceSay("Didn't hear anything — "+vkVoiceButtonName()+" to talk", vkVoiceNoAction)
	}
}
