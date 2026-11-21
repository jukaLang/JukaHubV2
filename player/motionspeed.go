package main

// motionspeed.go — Settings → Theme & look → Animations: Comfy, Snappy or Off.
//
// Comfy is today's timings. Snappy plays the same motions in 60 % of the
// time: the scene fade between screens (uiSceneFadeMS, anim.go), every
// overlay's entrance (uiEnter.begin: menus, panels, sheets, Quick Settings)
// and the focus ring's glide (homefocusglide.go). Off is Reduce motion, the
// switch under it and the Quick Settings tile: the row is a three-way view
// over that switch and one more key, so there is never a second "animations
// off" to disagree with it. Choosing Off turns Reduce motion on through the
// Quick Settings path (which saves it and explains a Power mode that manages
// it); Comfy or Snappy store the speed and turn it back off.
//
// The stored speed is a Custom string, "comfy" (the default when unset) or
// "snappy" — motion.go's MotionPresetSnappy, the preset this row finally
// gives a meaning to.

import "strings"

// animSpeedKey is the setting in Variables.Custom.
const animSpeedKey = "animSpeed"

// The row's stops.
const (
	animSpeedComfy  = "comfy"
	animSpeedSnappy = MotionPresetSnappy
	animSpeedOff    = "off"
)

// animSpeedSnappyScale is Snappy's share of each duration.
const animSpeedSnappyScale = 0.6

// animSpeedStored is the stored speed: Snappy, or Comfy for anything else.
func animSpeedStored(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return animSpeedComfy
	}
	v, _ := c.Variables.Custom[animSpeedKey].(string)
	if strings.EqualFold(strings.TrimSpace(v), animSpeedSnappy) {
		return animSpeedSnappy
	}
	return animSpeedComfy
}

// animSpeedOf is what the row shows: Off whenever motion is reduced (by the
// switch or by Battery Saver), else the stored speed.
func animSpeedOf(c *Config) string {
	if c != nil && effectiveReducedMotion(c) {
		return animSpeedOff
	}
	return animSpeedStored(c)
}

// motionSpeedScale is the factor durations are played at (appConfig when c
// is nil): 0.6 on Snappy, 1 otherwise. Reduce motion is answered by its own
// callers, which snap instead of playing anything.
func motionSpeedScale(c *Config) float64 {
	if c == nil {
		c = appConfig
	}
	if animSpeedStored(c) == animSpeedSnappy {
		return animSpeedSnappyScale
	}
	return 1
}

// motionSpeedMS is a duration in ms at the chosen speed; a motion that has
// one keeps at least a millisecond.
func motionSpeedMS(ms uint64) uint64 {
	k := motionSpeedScale(nil)
	if ms == 0 || k >= 1 {
		return ms
	}
	out := uint64(float64(ms)*k + 0.5)
	if out < 1 {
		out = 1
	}
	return out
}

// animSpeedLabel is a stop's name.
func animSpeedLabel(v string) string {
	switch v {
	case animSpeedSnappy:
		return "Snappy"
	case animSpeedOff:
		return "Off"
	}
	return "Comfy"
}

// animSpeedOptions are the row's stops.
func animSpeedOptions(*Config) []setOption {
	return []setOption{
		{Value: animSpeedComfy, Label: "Comfy", Note: "Today's timings"},
		{Value: animSpeedSnappy, Label: "Snappy", Note: "The same motion in 60% of the time"},
		{Value: animSpeedOff, Label: "Off", Note: "No motion: the same as Reduce motion"},
	}
}

// animSpeedRow is Theme & look → Animations.
func animSpeedRow() setRow {
	return setRow{
		ID: animSpeedKey, Label: "Animations", Kind: setKindChoice, Quiet: true,
		Detail: func(c *Config) string {
			if mode := currentPowerMode(c); mode != PowerModeDefault {
				return "Power mode " + powerModeLabel(mode) + " decides whether they play"
			}
			return "How quickly screens, menus and the focus ring move"
		},
		Options: animSpeedOptions,
		Current: func(c *Config) int {
			v := animSpeedOf(c)
			for i, o := range animSpeedOptions(c) {
				if o.Value == v {
					return i
				}
			}
			return 0
		},
		Choose: animSpeedChoose,
	}
}

// animSpeedChoose applies a stop. Off and back go through Reduce motion's own
// Quick Settings path; a Power mode that manages it answers with its toast.
func animSpeedChoose(c *Config, value string) {
	if c == nil {
		return
	}
	switch value {
	case animSpeedOff:
		if !effectiveReducedMotion(c) {
			setQSWrite(qsActionReducedMotion)(c, true)
		}
	case animSpeedComfy, animSpeedSnappy:
		setStoreString(c, animSpeedKey, value)
		saveUserConfigDebounced(userConfigFromVariables(c))
		if effectiveReducedMotion(c) {
			setQSWrite(qsActionReducedMotion)(c, false)
		} else {
			PlayNavSound()
		}
	default:
		return
	}
	if got := animSpeedOf(c); got == value {
		setToast("Animations: " + animSpeedLabel(got))
	}
}
