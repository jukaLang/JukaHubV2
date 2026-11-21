package main

// ra_threaded.go — Settings → Emulation → "Threaded video": RetroArch's
// video_threaded for games started from the library.
//
// Threaded video runs the video driver on a thread of its own. It hides slow
// frames on a weak CPU, but the frame a game hands over is shown a little
// later and at a less regular moment, and on the Smart Pro S's A523 that is
// what shows: off gives smoother frame pacing there. The
// Emulation page's Smooth motion (ratuning.go) left video_threaded as the
// firmware set it; this choice decides it instead:
//
//	Auto  off on the Smart Pro S (A523), RetroArch's own value elsewhere
//	On    video_threaded = "true"
//	Off   video_threaded = "false"
//
// The value is one more line of the launch's tuning plan (raTunePlanFor), so
// it goes through raScopeApply like the rest: written for that launch,
// journalled and put back when RetroArch exits; RetroArch started from the
// firmware's menu keeps its own setting.

import "strings"

const (
	// raThreadedKey is the Custom setting: auto (unset), on or off.
	raThreadedKey = "raTuningThreaded"

	raThreadedAuto = "auto"
	raThreadedOn   = "on"
	raThreadedOff  = "off"
)

// raThreadedDevice identifies the hardware; a variable so tests could stand
// in for a Smart Pro S.
var raThreadedDevice = DetectDevice

// raThreadedSetting is the stored choice; unset or unknown is Auto.
func raThreadedSetting(c *Config) string {
	switch v := strings.ToLower(strings.TrimSpace(raTuneRaw(c, raThreadedKey))); v {
	case raThreadedOn, "true":
		return raThreadedOn
	case raThreadedOff, "false":
		return raThreadedOff
	}
	return raThreadedAuto
}

// raThreadedValue is the launch's video_threaded line and its words for the
// log; ok is false when RetroArch's own value stays.
func raThreadedValue(c *Config) (raScopeValue, string, bool) {
	switch raThreadedSetting(c) {
	case raThreadedOn:
		return raScopeValue{Key: "video_threaded", Value: "true"}, "threaded video", true
	case raThreadedOff:
		return raScopeValue{Key: "video_threaded", Value: "false"}, "no threaded video", true
	}
	if raThreadedDevice() == DeviceTrimuiSmartProS {
		return raScopeValue{Key: "video_threaded", Value: "false"}, "no threaded video (A523)", true
	}
	return raScopeValue{}, "", false
}

func raThreadedOptions(*Config) []setOption {
	auto := "Off on the Smart Pro S, RetroArch's own elsewhere"
	if raThreadedDevice() == DeviceTrimuiSmartProS {
		auto = "Off here: smoother frame pacing on the A523"
	}
	return []setOption{
		{Value: raThreadedAuto, Label: "Auto", Note: auto},
		{Value: raThreadedOn, Label: "On", Note: "Fewer dropped frames on a slow core, a little more delay"},
		{Value: raThreadedOff, Label: "Off", Note: "Even frame pacing, the least delay"},
	}
}

// raThreadedSettingsRow is the row under Smooth motion (ratuning_settings.go).
func raThreadedSettingsRow() setRow {
	return setRow{
		ID: "raTuneThreaded", Label: "Threaded video", Kind: setKindChoice,
		Detail:  setStatic("RetroArch draws on a thread of its own, in library games"),
		Options: raThreadedOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(raThreadedOptions(c), raThreadedSetting(c)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, raThreadedKey, value)
			label := "Auto"
			if i := raTuneOptionIndex(raThreadedOptions(c), value); i >= 0 {
				label = raThreadedOptions(c)[i].Label
			}
			setToast("Threaded video: " + label)
		},
	}
}
