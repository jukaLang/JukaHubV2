package main

// ratuning_settings.go — Settings → Emulation: the rows behind ratuning.go.
//
// The first row picks what the scoped rows change: "All consoles", or one
// console that then gets its own Audio quality, Latency, Overlay, Look and
// Crop overscan (a Game Boy border is no use on a PlayStation game, a PSP game
// wants the cheap resampler while the NES gets the best one, and the Game Boy
// wants SkyWalker541's PT screen where the NES wants a CRT). Each per-console
// row starts with "Default · …", which follows the all-consoles choice and
// shows what that is for this console. The picked console is remembered for
// this run of the app only; it is a view, not a setting.
//
// Under Look, Tune look / Value / Reset look change the knobs of the scope's
// look (shader_looks_tune.go), and Download looks fetches the PT, CRT and LCD
// shaders and SkyWalker541's Brick overlays (shader_looks_download.go). The
// all-consoles Look is the same setting as the Games page's Video filter.
//
// Smooth motion, Rumble strength, Arcade rotation and PAL mode are one choice
// for every console. Every row stores its value in the custom map through
// setStoreString, so it is saved with the rest of the settings; nothing
// touches RetroArch until the next library launch.

import (
	"path/filepath"
	"strings"
	"time"
)

// raTuneSettingsSection is the section's id in the Settings rail.
const raTuneSettingsSection = "emulation"

// raTuneScope is the console the scoped rows edit; "" is all consoles. Main
// thread.
var raTuneScope string

// raTuneRACache holds whether a RetroArch config was found: rows ask on every
// refresh, and the answer is a dozen stat calls on the card. Main thread.
var raTuneRACache struct {
	at time.Time
	ok bool
}

func raTuneHasRetroArch() bool {
	c := &raTuneRACache
	if now := time.Now(); c.at.IsZero() || now.Sub(c.at) >= 10*time.Second {
		c.at = now
		c.ok = len(raScopeTargets(raTuneCandidates())) > 0
	}
	return c.ok
}

// raTuneScopeName is the scope as the rows say it.
func raTuneScopeName() string {
	if raTuneScope == "" {
		return "All consoles"
	}
	return raTuneScope
}

// raTuneScopedKey is the key a scoped row writes: the all-consoles one, or
// the picked console's own.
func raTuneScopedKey(globalKey, what string) string {
	if raTuneScope == "" {
		return globalKey
	}
	return raTuneConsoleKey(raTuneScope, what)
}

// raTuneScopedToast is a change's confirmation, naming the console when the
// change is that console's alone.
func raTuneScopedToast(what, label string) {
	if raTuneScope != "" {
		what = raTuneScope + " " + strings.ToLower(what)
	}
	setToast(what + ": " + label)
}

// raTuneOptionIndex is the stop whose value is value, or -1.
func raTuneOptionIndex(options []setOption, value string) int {
	for i, o := range options {
		if o.Value == value {
			return i
		}
	}
	return -1
}

// ── Labels ───────────────────────────────────────────────────────────────

func raTuneAudioLabel(mode string) string {
	switch mode {
	case raTuneAuto:
		return "Auto"
	case raTuneBest:
		return "Best"
	case raTuneBalanced:
		return "Balanced"
	case raTuneFast:
		return "Fast"
	}
	return "RetroArch's own"
}

func raTuneLatencyLabel(mode string) string {
	switch mode {
	case raTuneAuto:
		return "Auto"
	case raTuneLow:
		return "Low"
	case raTuneLowest:
		return "Lowest"
	}
	return "Standard"
}

// raTuneOverlayLabel names a stored overlay: its place in the list when the
// scan found it, else its file name.
func raTuneOverlayLabel(v string) string {
	switch v {
	case "", raTuneOwn:
		return "RetroArch's own"
	case raTuneOverlayNone:
		return "None"
	}
	if userOverlayIsOurs(v) { // Overlays/<Console> on the card (useroverlays.go)
		return userOverlayLabel(v)
	}
	if list, ok := raTuneOverlays(); ok {
		for _, o := range list {
			if o.Path == v {
				return o.Label
			}
		}
	}
	return strings.TrimSuffix(filepath.Base(v), filepath.Ext(v))
}

// raTuneDefaultNote is the per-console "Default" stop's second line.
const raTuneDefaultNote = "Follow the All consoles choice"

// ── Audio quality ────────────────────────────────────────────────────────

func raTuneAudioOptions(c *Config) []setOption {
	out := make([]setOption, 0, 5)
	if raTuneScope == "" {
		out = append(out, setOption{Value: raTuneAuto, Label: "Auto",
			Note: "Best on 8/16-bit and handhelds, Balanced on PlayStation, N64 and DS, Fast on PSP, Dreamcast and Saturn"})
	} else {
		sys, _ := raTuneSystemFor(raTuneScope)
		eff := raTuneAudioSetting(c)
		if eff == raTuneAuto {
			eff = raTuneAutoAudio(sys.Tier)
		}
		out = append(out, setOption{Value: "", Label: "Default · " + raTuneAudioLabel(eff), Note: raTuneDefaultNote})
	}
	return append(out,
		setOption{Value: raTuneBest, Label: "Best", Note: "High-quality sinc resampling; clean sound, a little more CPU"},
		setOption{Value: raTuneBalanced, Label: "Balanced", Note: "Lighter sinc resampling"},
		setOption{Value: raTuneFast, Label: "Fast", Note: "The cheapest good resampler, for heavy consoles"},
		setOption{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's audio settings alone"})
}

func raTuneAudioStored(c *Config) string {
	if raTuneScope == "" {
		return raTuneAudioSetting(c)
	}
	return raTuneConsoleAudio(c, raTuneScope)
}

func raTuneAudioRow() setRow {
	return setRow{
		ID: "raTuneAudio", Label: "Audio quality", Kind: setKindChoice,
		Detail: func(*Config) string {
			if raTuneScope == "" {
				return "How RetroArch resamples game sound · higher quality costs CPU"
			}
			return raTuneScope + " games only"
		},
		Options: raTuneAudioOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(raTuneAudioOptions(c), raTuneAudioStored(c)) },
		Choose: func(c *Config, value string) {
			value = normaliseRATuneAudio(value)
			if raTuneScope != "" && value == raTuneAuto {
				value = ""
			}
			setStoreString(c, raTuneScopedKey(raTuneAudioKey, "audio"), value)
			label := raTuneAudioLabel(value)
			if value == "" {
				label = "Default"
			}
			raTuneScopedToast("Audio quality", label)
		},
	}
}

// ── Latency ──────────────────────────────────────────────────────────────

func raTuneLatencyOptions(c *Config) []setOption {
	out := make([]setOption, 0, 4)
	if raTuneScope == "" {
		out = append(out, setOption{Value: raTuneAuto, Label: "Auto",
			Note: "Low on 8/16-bit consoles and handhelds, Standard on the rest"})
	} else {
		sys, _ := raTuneSystemFor(raTuneScope)
		eff := raTuneLatencySetting(c)
		if eff == raTuneAuto {
			eff = raTuneAutoLatency(sys.Tier)
		}
		out = append(out, setOption{Value: "", Label: "Default · " + raTuneLatencyLabel(eff), Note: raTuneDefaultNote})
	}
	return append(out,
		setOption{Value: raTuneStandard, Label: "Standard", Note: "RetroArch's own buffering"},
		setOption{Value: raTuneLow, Label: "Low", Note: "One frame less queued, automatic frame delay, 64 ms audio"},
		setOption{Value: raTuneLowest, Label: "Lowest", Note: "Low with 48 ms audio, plus a frame of run-ahead on light consoles"})
}

func raTuneLatencyStored(c *Config) string {
	if raTuneScope == "" {
		return raTuneLatencySetting(c)
	}
	return raTuneConsoleLatency(c, raTuneScope)
}

func raTuneLatencyRow() setRow {
	return setRow{
		ID: "raTuneLatency", Label: "Latency", Kind: setKindChoice,
		Detail: func(*Config) string {
			if raTuneScope == "" {
				return "The delay between a press and the picture · Lowest needs a fast console"
			}
			return raTuneScope + " games only"
		},
		Options: raTuneLatencyOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(raTuneLatencyOptions(c), raTuneLatencyStored(c)) },
		Choose: func(c *Config, value string) {
			value = normaliseRATuneLatency(value)
			if raTuneScope != "" && value == raTuneAuto {
				value = ""
			}
			setStoreString(c, raTuneScopedKey(raTuneLatencyKey, "latency"), value)
			label := raTuneLatencyLabel(value)
			if value == "" {
				label = "Default"
			}
			raTuneScopedToast("Latency", label)
		},
	}
}

// ── Overlay ──────────────────────────────────────────────────────────────

func raTuneOverlayOptions(c *Config) []setOption {
	list, _ := raTuneOverlays()
	// The console's own pictures in Overlays/<Console> (useroverlays.go).
	var own []userOverlay
	if raTuneScope != "" {
		own = userOverlaysCached(raTuneScope)
	}
	out := make([]setOption, 0, len(list)+len(own)+3)
	if raTuneScope != "" {
		if auto, ok := userOverlaysAutoPick(own, raTuneScope); ok {
			out = append(out, setOption{Value: "", Label: "Default · " + userOverlayLabel(auto.Path),
				Note: "The first overlay in Overlays/" + auto.Dir + " on the card"})
		} else {
			out = append(out, setOption{Value: "", Label: "Default · " + raTuneOverlayLabel(raTuneOverlaySetting(c)), Note: raTuneDefaultNote})
		}
	}
	out = append(out,
		setOption{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's overlay setting alone"},
		setOption{Value: raTuneOverlayNone, Label: "None", Note: "No overlay, even if RetroArch has one on"})
	for _, o := range own {
		out = append(out, setOption{Value: o.Path, Label: userOverlayLabel(o.Path), Note: "From Overlays/" + o.Dir + " on the card"})
	}
	for _, o := range list {
		out = append(out, setOption{Value: o.Path, Label: o.Label})
	}
	return out
}

func raTuneOverlayStored(c *Config) string {
	if raTuneScope == "" {
		return raTuneOverlaySetting(c)
	}
	return raTuneConsoleOverlay(c, raTuneScope)
}

func raTuneOverlayRow() setRow {
	return setRow{
		ID: "raTuneOverlay", Label: "Overlay", Kind: setKindChoice,
		Detail: func(*Config) string {
			list, ok := raTuneOverlays()
			what := "A border or bezel over the game"
			if raTuneScope != "" {
				what = raTuneScope + " games only"
				// Its own pictures go in Overlays/<Console> (useroverlays.go).
				if len(userOverlaysCached(raTuneScope)) == 0 {
					return what + " · put your own in Overlays/" + raTuneScope + " on the card"
				}
			}
			switch {
			case !ok:
				return what + " · looking for overlays…"
			case len(list) == 0 && raTuneScope == "":
				return what + " · no overlays found in RetroArch's overlays folder"
			}
			return what + " · A lists them all"
		},
		Options:    raTuneOverlayOptions,
		Sheet:      raTuneOverlayOptions,
		SheetValue: raTuneOverlayStored,
		SheetTitle: "Overlay",
		Current:    func(c *Config) int { return raTuneOptionIndex(raTuneOverlayOptions(c), raTuneOverlayStored(c)) },
		Value: func(c *Config) string {
			v := raTuneOverlayStored(c)
			label := raTuneOverlayLabel(v)
			if v != raTuneOwn && v != raTuneOverlayNone && v != "" && !regularFile(v) {
				label += " · not found"
			}
			return label
		},
		Choose: func(c *Config, value string) {
			value = normaliseRATuneOverlay(value)
			if raTuneScope == "" && value == "" {
				value = raTuneOwn
			}
			setStoreString(c, raTuneScopedKey(raTuneOverlayKey, "overlay"), value)
			label := raTuneOverlayLabel(value)
			if value == "" {
				label = "Default"
			}
			raTuneScopedToast("Overlay", label)
		},
	}
}

// ── Look ─────────────────────────────────────────────────────────────────

// raTuneLookOptions is the Look ladder: Auto for All consoles, "Default · …"
// for a console, then every mode the card offers (videoFilterChoices).
func raTuneLookOptions(c *Config) []setOption {
	modes := videoFilterChoices()
	out := make([]setOption, 0, len(modes)+1)
	if raTuneScope == "" {
		out = append(out, setOption{Value: videoFilterAuto, Label: "Auto", Note: videoFilterAutoNote})
	} else {
		label := videoFilterModeLabel(videoFilterForConfig(c))
		if videoFilterForConfig(c) == videoFilterAuto {
			label = "Auto · " + videoFilterModeLabel(videoFilterAutoMode(raTuneScope, videoFilterUIHas))
		}
		out = append(out, setOption{Value: "", Label: "Default · " + label, Note: raTuneDefaultNote})
	}
	for _, m := range modes {
		out = append(out, setOption{Value: m, Label: videoFilterModeLabel(m), Note: videoFilterChoiceNote(m)})
	}
	return out
}

func raTuneLookStored(c *Config) string {
	if raTuneScope == "" {
		return videoFilterForConfig(c)
	}
	return videoFilterConsoleLook(c, raTuneScope)
}

func raTuneLookRow() setRow {
	return setRow{
		ID: "raTuneLook", Label: "Look", Kind: setKindChoice,
		Detail: func(*Config) string {
			what := "A shader over the game: CRT, LCD, PT"
			if raTuneScope != "" {
				what = raTuneScope + " games only"
			}
			probe, ok := videoFilterUIProbe()
			switch {
			case !ok:
				return what + " · looking for shaders…"
			case len(probe.Looks) == 0:
				return what + " · no looks on the card: Download looks below"
			}
			return what + " · a game can choose its own (Start in the library)"
		},
		Options:    raTuneLookOptions,
		Sheet:      raTuneLookOptions,
		SheetValue: raTuneLookStored,
		SheetTitle: "Look",
		Current:    func(c *Config) int { return raTuneOptionIndex(raTuneLookOptions(c), raTuneLookStored(c)) },
		Value: func(c *Config) string {
			v := raTuneLookStored(c)
			label := videoFilterModeLabel(v)
			if videoFilterMissing(v) {
				label += " · not installed"
			}
			return label
		},
		Choose: func(c *Config, value string) {
			lookTuneSel = ""
			if raTuneScope == "" {
				videoFilterSettingChoose(c, value)
				raTuneScopedToast("Look", videoFilterModeLabel(normaliseVideoFilterMode(value)))
				return
			}
			value = normaliseGameVideoFilter(value)
			if value == videoFilterAuto {
				value = ""
			}
			setStoreString(c, raTuneConsoleKey(raTuneScope, "look"), value)
			label := videoFilterModeLabel(value)
			if value == "" {
				label = "Default"
			}
			raTuneScopedToast("Look", label)
		},
	}
}

// ── Crop overscan ────────────────────────────────────────────────────────

func raTuneCropLabel(v string) string {
	switch v {
	case "on":
		return "On"
	case "off":
		return "Off"
	case "":
		return "Default"
	}
	return "RetroArch's own"
}

func raTuneCropOptions(c *Config) []setOption {
	out := make([]setOption, 0, 3)
	if raTuneScope == "" {
		out = append(out, setOption{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's setting (it crops unless told not to)"})
	} else {
		out = append(out, setOption{Value: "", Label: "Default · " + raTuneCropLabel(raTuneCropSetting(c)), Note: raTuneDefaultNote})
	}
	return append(out,
		setOption{Value: "on", Label: "On", Note: "Drop the border a TV hid, where the emulator follows RetroArch"},
		setOption{Value: "off", Label: "Off", Note: "Keep the whole picture, borders included"})
}

func raTuneCropStored(c *Config) string {
	if raTuneScope == "" {
		return raTuneCropSetting(c)
	}
	return raTuneConsoleCrop(c, raTuneScope)
}

func raTuneCropRow() setRow {
	return setRow{
		ID: "raTuneCrop", Label: "Crop overscan", Kind: setKindChoice,
		Detail: func(*Config) string {
			if raTuneScope == "" {
				return "Cut the edge a TV hid (NES, SNES and other TV consoles), or keep it"
			}
			return raTuneScope + " games only"
		},
		Options: raTuneCropOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(raTuneCropOptions(c), raTuneCropStored(c)) },
		Choose: func(c *Config, value string) {
			v := raTuneCropWord(value)
			if raTuneScope == "" && v == "" {
				v = raTuneOwn
			}
			setStoreString(c, raTuneScopedKey(raTuneCropKey, "crop"), v)
			raTuneScopedToast("Crop overscan", raTuneCropLabel(v))
		},
	}
}

// ── The console picker ───────────────────────────────────────────────────

// raTuneConsoleHasOwn reports whether a console has any choice of its own.
func raTuneConsoleHasOwn(c *Config, name string) bool {
	return raTuneConsoleAudio(c, name) != "" || raTuneConsoleLatency(c, name) != "" || raTuneConsoleOverlay(c, name) != "" ||
		videoFilterConsoleLook(c, name) != "" || raTuneConsoleCrop(c, name) != ""
}

func raTuneConsoleOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(raTuneSystems)+1)
	out = append(out, setOption{Value: "", Label: "All consoles", Note: "The choices every console starts with"})
	for _, s := range raTuneSystems {
		note := ""
		if raTuneConsoleHasOwn(c, s.Name) {
			note = "Has its own settings"
		}
		out = append(out, setOption{Value: s.Name, Label: s.Name, Note: note})
	}
	return out
}

func raTuneConsoleRow() setRow {
	return setRow{
		ID: "raTuneConsole", Label: "Adjust for", Kind: setKindChoice,
		Detail: func(*Config) string {
			if raTuneScope == "" {
				return "Pick a console to give it its own audio, latency, overlay, look and crop"
			}
			return "Audio, latency, overlay, look and crop below change " + raTuneScope + " games only"
		},
		Options:    raTuneConsoleOptions,
		Sheet:      raTuneConsoleOptions,
		SheetValue: func(*Config) string { return raTuneScope },
		SheetTitle: "Adjust emulation for",
		Current:    func(c *Config) int { return raTuneOptionIndex(raTuneConsoleOptions(c), raTuneScope) },
		Value:      func(*Config) string { return raTuneScopeName() },
		Choose: func(_ *Config, value string) {
			raTuneScope = ""
			for _, s := range raTuneSystems {
				if s.Name == value {
					raTuneScope = s.Name
				}
			}
		},
	}
}

// ── The all-consoles rows ────────────────────────────────────────────────

func raTuneRumbleOptions(*Config) []setOption {
	return []setOption{
		{Value: raTuneOwn, Label: "RetroArch's own"},
		{Value: "0", Label: "Off"},
		{Value: "25", Label: "25%"},
		{Value: "50", Label: "50%"},
		{Value: "75", Label: "75%"},
		{Value: "100", Label: "100%"},
	}
}

func raTuneRotateOptions(*Config) []setOption {
	return []setOption{
		{Value: raTuneUpright, Label: "Upright", Note: "Vertical games stand upright, with bars at the sides"},
		{Value: raTuneTate, Label: "Sideways (TATE)", Note: "Vertical games fill the screen; hold the handheld on its side"},
		{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's rotation settings alone"},
	}
}

// raTuneSettingsRows is the Emulation page.
func raTuneSettingsRows() []setRow {
	rows := []setRow{
		{
			ID: "raTuneNoRA", Label: "RetroArch", Kind: setKindInfo,
			Detail:  setStatic("Not found yet: these settings apply once RetroArch is installed"),
			Value:   setStatic("Not found"),
			Visible: func(*Config) bool { return !raTuneHasRetroArch() },
		},
		raTuneConsoleRow(),
		raTuneAudioRow(),
		raTuneLatencyRow(),
		raTuneOverlayRow(),
		raTuneLookRow(),
	}
	rows = append(rows, lookTuneRows()...)
	rows = append(rows, raTuneCropRow())
	// Aspect ratio and hard GPU sync, per console too (raratio_ui.go).
	rows = append(rows, raRatioSettingsRows()...)
	// Widescreen 16:9 hacks, per console, wide screens only (widescreen_ui.go).
	rows = append(rows, widescreenSettingsRows()...)
	// Fast-forward speed, per console too (raffspeed.go); N64 graphics (n64light.go).
	rows = append(rows, raFFSpeedSettingsRows()...)
	rows = append(rows, n64LightSettingsRows()...)
	rows = append(rows, looksDownloadRow())
	return append(rows, []setRow{
		{
			ID: "raTuneSmooth", Label: "Smooth motion", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if raTuneSmoothOn(c) {
					return "V-sync and audio sync on: no tearing, no stutter"
				}
				return "Off: RetroArch's own sync settings"
			},
			Get: raTuneSmoothOn,
			Set: func(c *Config, on bool) {
				setStoreBool(c, raTuneSmoothKey, on)
				setToast("Smooth motion: " + setOnOff(on))
			},
		},
		// Threaded video: Auto (off on the Smart Pro S), On, Off (ra_threaded.go).
		raThreadedSettingsRow(),
		{
			ID: "raTuneRumble", Label: "Rumble strength", Kind: setKindChoice, Clamp: true,
			Detail:  setStatic("How hard the motor shakes in RetroArch games"),
			Options: raTuneRumbleOptions,
			Current: func(c *Config) int { return raTuneOptionIndex(raTuneRumbleOptions(c), raTuneRumbleSetting(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, raTuneRumbleKey, value)
				label := "RetroArch's own"
				if i := raTuneOptionIndex(raTuneRumbleOptions(c), value); i >= 0 {
					label = raTuneRumbleOptions(c)[i].Label
				}
				setToast("Rumble strength: " + label)
			},
		},
		{
			ID: "raTuneRotate", Label: "Arcade rotation", Kind: setKindChoice,
			Detail:  setStatic("Vertical arcade games (FBNeo, MAME 2003-Plus)"),
			Options: raTuneRotateOptions,
			Current: func(c *Config) int { return raTuneOptionIndex(raTuneRotateOptions(c), raTuneRotateSetting(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, raTuneRotateKey, value)
				label := "Upright"
				if i := raTuneOptionIndex(raTuneRotateOptions(c), value); i >= 0 {
					label = raTuneRotateOptions(c)[i].Label
				}
				setToast("Arcade rotation: " + label)
			},
		},
		{
			ID: "raTunePAL", Label: "PAL mode", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if raTunePALOn(c) {
					return "NES, SNES, Mega Drive, Master System and PlayStation games run at 50 Hz European timing"
				}
				return "Off: every game runs at its own region's speed"
			},
			Get: raTunePALOn,
			Set: func(c *Config, on bool) {
				setStoreBool(c, raTunePALKey, on)
				setToast("PAL mode: " + setOnOff(on))
			},
		},
	}...)
}
