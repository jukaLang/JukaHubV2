package main

// gamepad_sony.go — DualShock 4 / DualSense pads SDL has no mapping for.
//
// SDL drives a PlayStation pad itself (its HIDAPI driver) where it can reach
// the pad's HID node; otherwise the pad comes through the kernel's event device
// and SDL needs a mapping for its GUID. A Bluetooth DualShock 4 on the TrimUI's
// 4.9 kernel (hid-sony before 4.10 = the HID report's button order) or a
// DualSense through hid-generic often has none: it was then only a raw
// joystick, and JukaHub — which listens to game controllers only — ignored it.
// sonyPadPrepare adds a mapping for the pad's GUID in the order its driver
// uses, so the pad opens as a game controller like any other.
//
// The Remote Play helper does the same in its own process
// (player/native/stream_sys.c ss_sony_*: keep the two tables in step).

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const sonyPadVendor = 0x054c

// sonyPadModel is 4 for a DualShock 4 (both models and Sony's USB wireless
// adaptor), 5 for a DualSense / DualSense Edge and 0 for anything else.
func sonyPadModel(vendor, product int) int {
	if vendor != sonyPadVendor {
		return 0
	}
	switch product {
	case 0x05c4, 0x09cc, 0x0ba0:
		return 4
	case 0x0ce6, 0x0df2:
		return 5
	}
	return 0
}

type sonyPadLayout int

const (
	sonyLayoutNone sonyPadLayout = iota
	// hid-generic, or hid-sony before Linux 4.10: Square Cross Circle Triangle
	// L1 R1 L2 R2 Share Options L3 R3 PS Touchpad; sticks on axes 0 1 2 5,
	// L2/R2 on axes 3 4.
	sonyLayoutHIDGeneric
	// hid-sony 4.10+ / hid-playstation: Cross Circle Triangle Square L1 R1 L2
	// R2 Share Options PS L3 R3; L2/R2 on axes 2 5; the touchpad is a node of
	// its own.
	sonyLayoutEvdevGamepad
)

// sonyPadLayoutOf tells the two kernel layouts apart by their counts: both
// have six axes and the D-pad as a hat.
func sonyPadLayoutOf(buttons, axes, hats int) sonyPadLayout {
	if hats < 1 || axes < 6 {
		return sonyLayoutNone
	}
	switch {
	case buttons >= 14:
		return sonyLayoutHIDGeneric
	case buttons == 13:
		return sonyLayoutEvdevGamepad
	}
	return sonyLayoutNone
}

// sonyPadMapping is the SDL mapping line for the pad, or "" when the GUID,
// model or layout is not one it knows. touchpad names the touchpad click
// (an element SDL knows from 2.0.14 on).
func sonyPadMapping(guid string, model int, layout sonyPadLayout, touchpad bool) string {
	if len(guid) != 32 || (model != 4 && model != 5) {
		return ""
	}
	for _, r := range guid {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return ""
		}
	}
	var buttons string
	switch layout {
	case sonyLayoutHIDGeneric:
		buttons = "a:b1,b:b2,x:b0,y:b3,leftshoulder:b4,rightshoulder:b5,lefttrigger:a3,righttrigger:a4," +
			"back:b8,start:b9,leftstick:b10,rightstick:b11,guide:b12,"
		if touchpad {
			buttons += "touchpad:b13,"
		}
		buttons += "leftx:a0,lefty:a1,rightx:a2,righty:a5,"
	case sonyLayoutEvdevGamepad:
		buttons = "a:b0,b:b1,x:b3,y:b2,leftshoulder:b4,rightshoulder:b5,lefttrigger:a2,righttrigger:a5," +
			"back:b8,start:b9,guide:b10,leftstick:b11,rightstick:b12," +
			"leftx:a0,lefty:a1,rightx:a3,righty:a4,"
	default:
		return ""
	}
	name := "PS4 Controller"
	if model == 5 {
		name = "PS5 Controller"
	}
	return fmt.Sprintf("%s,%s,%sdpup:h0.1,dpright:h0.2,dpdown:h0.4,dpleft:h0.8,", guid, name, buttons)
}

// sonyPadPrepare maps the joystick at index when it is a PlayStation pad SDL
// has no mapping for. True when it is a game controller now. Main thread only.
func sonyPadPrepare(index int) bool {
	if index < 0 || index >= sdl.NumJoysticks() || sdl.IsGameController(index) {
		return false
	}
	model := sonyPadModel(sdl.JoystickGetDeviceVendor(index), sdl.JoystickGetDeviceProduct(index))
	if model == 0 {
		return false
	}
	joy := sdl.JoystickOpen(index)
	if joy == nil {
		return false
	}
	buttons, axes, hats := joy.NumButtons(), joy.NumAxes(), joy.NumHats()
	guid := sdl.JoystickGetGUIDString(joy.GUID())
	joy.Close()
	var v sdl.Version
	sdl.GetVersion(&v)
	touchpad := sdl.VERSIONNUM(int(v.Major), int(v.Minor), int(v.Patch)) >= sdl.VERSIONNUM(2, 0, 14)
	layout := sonyPadLayoutOf(buttons, axes, hats)
	mapping := sonyPadMapping(guid, model, layout, touchpad)
	if mapping == "" {
		logDebug("gamepad", "PS%d pad with an unknown layout (%d buttons, %d axes, %d hats): left unmapped",
			model, buttons, axes, hats)
		return false
	}
	if sdl.GameControllerAddMapping(mapping) < 0 {
		logDebug("gamepad", "PS%d pad mapping refused: %v", model, sdl.GetError())
		return false
	}
	logDebug("gamepad", "PS%d pad mapped (%d buttons, %d axes)", model, buttons, axes)
	return sdl.IsGameController(index)
}
