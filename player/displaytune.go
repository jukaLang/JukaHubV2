package main

// displaytune.go — Settings → Display → Contrast, Saturation,
// Exposure and Colour temperature.
//
// NextUI tunes the TrimUI panel's picture with the Allwinner display driver's
// own enhancement: the disp2 driver publishes it as sysfs attributes
// (/sys/class/disp/disp/attr/enhance_contrast, enhance_saturation,
// enhance_bright, color_temperature), and the stock firmware's "display
// enhance colour setting" (TrimUI 1.0.4) writes the same nodes. The engine sits
// after every layer, so a change shows on JukaHub, a game and a video alike,
// at no CPU cost. Brightness is not here: the header's sun already sets the
// backlight.
//
// Nobody documents the nodes' scales (0..10, 0..100 and signed ones all exist
// across disp2 kernels), so JukaHub never writes an absolute value. Each row is
// an offset from what the driver showed before JukaHub first touched it — the
// firmware's own setting — in NextUI's ranges (contrast -4..5, saturation
// -5..5, exposure -4..5; colour temperature in steps, lower is warmer). That
// baseline is read once, the first time a row moves off 0, and kept in the
// config (displayTuneBaseKey), so a restart (or a crash) never mistakes
// JukaHub's own write for the firmware's. Reset writes the baseline back and
// forgets it.
//
// Nothing is written at startup unless a row is off 0, so a device whose user
// never opens these rows keeps the firmware's picture untouched.
//
// The rows live on Settings → Display and always show (2026-11-17: the owner
// asked to "make sure" contrast, saturation and exposure can be set; hidden
// where the node was missing and folded under More, nobody found them). Where
// the driver has no such node (the desktop builds, a firmware without disp2's
// enhancement) contrast, exposure and colour temperature are drawn by JukaHub
// itself over its finished frame (displaytune_soft.go) — JukaHub's screens
// only, games keep their own picture, and each row says so. Saturation needs
// every pixel's own grey, which blending cannot give, so without the node its
// row stays, greyed, with the reason.
//
// All of it runs on the main thread: the rows, the startup apply
// (displayTuneApplyStartup, called beside the stick lights' restore) and the
// re-apply after a wake (displayTuneSoftRender watches wakeCount).

import (
	"strconv"
	"strings"
	"sync"
)

// displayTuneBaseKey holds the firmware's values JukaHub found before its first
// write, "node=value;node=value". The leading underscore keeps it out of the
// exported settings (cfgdoc.go): it describes this panel, not a choice.
const displayTuneBaseKey = "_display_tune_base"

// displayTuneControl is one row: its Custom key (the user's offset), the
// driver node it moves, and the offset range.
type displayTuneControl struct {
	key    string
	node   string
	label  string
	detail string
	min    int
	max    int
	step   int
	temp   bool // colour temperature: Warmer / Cooler labels
	soft   bool // JukaHub can draw it itself where the node is missing
}

var displayTuneControls = []displayTuneControl{
	{key: "display_contrast", node: "enhance_contrast", label: "Contrast",
		detail: "The panel's own contrast, for games and JukaHub alike", min: -4, max: 5, step: 1, soft: true},
	{key: "display_saturation", node: "enhance_saturation", label: "Saturation",
		detail: "Richer or softer colours on the whole screen", min: -5, max: 5, step: 1},
	{key: "display_exposure", node: "enhance_bright", label: "Exposure",
		detail: "Lifts or lowers the picture itself; the backlight stays as set", min: -4, max: 5, step: 1, soft: true},
	{key: "display_colortemp", node: "color_temperature", label: "Colour temperature",
		detail: "Warmer is easier on the eyes at night", min: -20, max: 20, step: 2, temp: true, soft: true},
}

// displayTuneSoftDetail is a row's line where JukaHub draws the control
// itself: honest about what it reaches.
const displayTuneSoftDetail = "On JukaHub's screens; this display has no hardware control, so games keep their own picture"

// displayTuneNoNodeDetail is the line of a row JukaHub cannot draw itself.
const displayTuneNoNodeDetail = "Needs the screen's own colour engine, which this display does not have"

// displayTuneAvail caches which nodes exist and take writes: sysfs attributes
// do not come and go while the app runs. Main thread (the Once makes a stray
// caller safe too).
var displayTuneAvail struct {
	once sync.Once
	ok   map[string]bool
}

func displayTuneNodeOK(node string) bool {
	displayTuneAvail.once.Do(func() {
		displayTuneAvail.ok = make(map[string]bool, len(displayTuneControls))
		for _, ctl := range displayTuneControls {
			displayTuneAvail.ok[ctl.node] = displayTuneProbe(ctl.node)
		}
	})
	return displayTuneAvail.ok[node]
}

func displayTuneControlFor(id string) (displayTuneControl, bool) {
	for _, ctl := range displayTuneControls {
		if ctl.key == id || ctl.node == id {
			return ctl, true
		}
	}
	return displayTuneControl{}, false
}

// displayTuneOffset is the stored offset, clamped to the row's range; unset or
// unreadable is 0.
func displayTuneOffset(c *Config, ctl displayTuneControl) int {
	if c == nil {
		return 0
	}
	raw, ok := customString(c.Variables.Custom, ctl.key)
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	if v < ctl.min {
		v = ctl.min
	}
	if v > ctl.max {
		v = ctl.max
	}
	return v
}

// displayTuneParseBase reads displayTuneBaseKey's "node=value;…". Pure.
func displayTuneParseBase(raw string) map[string]int {
	out := map[string]int{}
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		out[strings.TrimSpace(name)] = v
	}
	return out
}

// displayTuneFormatBase is displayTuneParseBase's inverse, in the controls'
// order so the stored string does not change from one save to the next. Pure.
func displayTuneFormatBase(base map[string]int) string {
	parts := make([]string, 0, len(base))
	for _, ctl := range displayTuneControls {
		if v, ok := base[ctl.node]; ok {
			parts = append(parts, ctl.node+"="+strconv.Itoa(v))
		}
	}
	return strings.Join(parts, ";")
}

func displayTuneBases(c *Config) map[string]int {
	if c == nil {
		return map[string]int{}
	}
	raw, _ := customString(c.Variables.Custom, displayTuneBaseKey)
	return displayTuneParseBase(raw)
}

// displayTuneBaseFor is the firmware's value for ctl's node: the stored one,
// or — before JukaHub's first write — what the node reads now, which is then
// stored. Reports false when the node cannot be read.
func displayTuneBaseFor(c *Config, ctl displayTuneControl) (int, bool) {
	bases := displayTuneBases(c)
	if v, ok := bases[ctl.node]; ok {
		return v, true
	}
	v, ok := displayTuneRead(ctl.node)
	if !ok || c == nil {
		return 0, false
	}
	bases[ctl.node] = v
	ledPanelEnsureKeys(c)
	c.Variables.Custom[displayTuneBaseKey] = displayTuneFormatBase(bases)
	return v, true
}

// displayTuneApply writes one control's node: baseline plus the offset. An
// offset of 0 with no baseline stored writes nothing — JukaHub never touched
// the node, so the firmware's value is already there.
func displayTuneApply(c *Config, ctl displayTuneControl) {
	if !displayTuneNodeOK(ctl.node) {
		return
	}
	off := displayTuneOffset(c, ctl)
	if _, stored := displayTuneBases(c)[ctl.node]; off == 0 && !stored {
		return
	}
	base, ok := displayTuneBaseFor(c, ctl)
	if !ok {
		return
	}
	if err := displayTuneWrite(ctl.node, base+off); err != nil {
		logWarn("display", "could not set %s to %d: %v", ctl.node, base+off, err)
	}
}

// displayTuneApplyStartup puts the saved picture back at launch. Only rows the
// user moved are written. Main thread.
func displayTuneApplyStartup(c *Config) {
	if c == nil {
		return
	}
	for _, ctl := range displayTuneControls {
		if displayTuneOffset(c, ctl) != 0 {
			displayTuneApply(c, ctl)
		}
	}
	displayTuneSoftSync(c)
}

// displayTuneReset writes every baseline back and forgets the offsets and the
// baselines. Main thread.
func displayTuneReset(c *Config) {
	if c == nil {
		return
	}
	bases := displayTuneBases(c)
	for _, ctl := range displayTuneControls {
		if v, ok := bases[ctl.node]; ok && displayTuneNodeOK(ctl.node) {
			if err := displayTuneWrite(ctl.node, v); err != nil {
				logWarn("display", "could not restore %s to %d: %v", ctl.node, v, err)
			}
		}
		delete(c.Variables.Custom, ctl.key)
	}
	delete(c.Variables.Custom, displayTuneBaseKey)
	displayTuneSoftSync(c)
	ledPanelPersist(c)
}

// displayTuneLabel is one stop's text: "+2", "0 · Default", or for colour
// temperature "Warmer 4" / "Default" / "Cooler 4". Pure.
func displayTuneLabel(ctl displayTuneControl, v int) string {
	if v == 0 {
		return "Default"
	}
	if ctl.temp {
		if v < 0 {
			return "Warmer " + strconv.Itoa(-v)
		}
		return "Cooler " + strconv.Itoa(v)
	}
	if v > 0 {
		return "+" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

func displayTuneOptions(ctl displayTuneControl) []setOption {
	step := ctl.step
	if step <= 0 {
		step = 1
	}
	out := make([]setOption, 0, (ctl.max-ctl.min)/step+1)
	for v := ctl.min; v <= ctl.max; v += step {
		out = append(out, setOption{Value: strconv.Itoa(v), Label: displayTuneLabel(ctl, v)})
	}
	return out
}

// displayTuneUsable reports whether a control can change anything here: its
// driver node, or JukaHub's own layer for the controls it can draw.
func displayTuneUsable(ctl displayTuneControl) bool {
	return ctl.node != "" && (displayTuneNodeOK(ctl.node) || ctl.soft)
}

// displayTuneRow is one control's Settings row. It always shows: with the
// driver's node it moves the panel itself, without it JukaHub draws the
// control over its own frames (displaytune_soft.go), and a control it cannot
// draw stays greyed with the reason. id is the control's Custom key.
func displayTuneRow(id string) setRow {
	ctl, _ := displayTuneControlFor(id)
	options := displayTuneOptions(ctl)
	return setRow{
		ID: ctl.key, Label: ctl.label, Kind: setKindChoice, Clamp: true,
		Detail: func(*Config) string {
			switch {
			case ctl.node == "" || displayTuneNodeOK(ctl.node):
				return ctl.detail
			case ctl.soft && ctl.key == "display_contrast" && displayTuneSoftNoCustom():
				return "Only lower contrast can be drawn on this screen"
			case ctl.soft:
				return displayTuneSoftDetail
			}
			return displayTuneNoNodeDetail
		},
		Disabled: func(*Config) bool { return !displayTuneUsable(ctl) },
		Options:  func(*Config) []setOption { return options },
		Current: func(c *Config) int {
			return setNearestStop(options, displayTuneOffset(c, ctl))
		},
		Choose: func(c *Config, value string) {
			v, err := strconv.Atoi(value)
			if c == nil || err != nil || !displayTuneUsable(ctl) {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[ctl.key] = strconv.Itoa(v)
			displayTuneApply(c, ctl)
			displayTuneSoftSync(c)
			ledPanelPersist(c)
		},
	}
}

// displayTuneResetRow puts the firmware's picture back.
func displayTuneResetRow() setRow {
	return setRow{
		ID: "displayTuneReset", Label: "Reset picture", Kind: setKindAction,
		Detail: setStatic("Contrast, saturation, exposure and colour temperature back to the firmware's"),
		Disabled: func(c *Config) bool {
			if c == nil {
				return true
			}
			if _, ok := customString(c.Variables.Custom, displayTuneBaseKey); ok {
				return false
			}
			for _, ctl := range displayTuneControls {
				if displayTuneOffset(c, ctl) != 0 {
					return false
				}
			}
			return true
		},
		Open: func(c *Config) {
			displayTuneReset(c)
			setToast("Picture reset")
		},
	}
}
