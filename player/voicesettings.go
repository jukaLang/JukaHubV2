package main

// voicesettings.go — "turn on dark mode", "make the text bigger", "is
// background music on?": settings by voice.
//
// The grammar (voicecmd_intent.go) only knew a handful of fixed commands, so a
// sentence about any other setting fell through to "open <the whole
// sentence>": at best Settings opened on the row and nothing changed, usually
// nothing matched and Juka was told the command had not run. This file reads
// the shapes people use for a setting — on/off/toggle, "set X to Y", up/down,
// max/min, and the read-outs "is X on?" / "what is X?" — and resolves the
// setting against the Settings catalogue itself (setSections, setmodel.go):
// every switch and list there can be reached without a list of its own here,
// so a new row is reachable by voice the day it is added. A few spoken names
// that are not a row's label are listed below (voiceSettingSynonyms); "dark
// mode" and "theme" are two rows that exist only for voice (the theme gallery
// is a screen, not a row).
//
// A change goes through the row's own Set or Choose — the same call a press in
// Settings makes — so it is applied, saved and confirmed exactly as there.
// Rows that are not a switch or a list (pages, text fields, keys, resets) are
// never pressed: voice offers to open them instead. A row that is hidden here
// or greyed out says why, in its own Detail line.
//
// The parser half (voiceSettingParse) is pure. The rest reads rows and runs
// their setters: main thread only, like every voice action.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// voiceSettingOp is what a sentence asks of a setting.
type voiceSettingOp int

const (
	voiceSetOpNone   voiceSettingOp = iota
	voiceSetOpOn                    // "turn on X", "X on"
	voiceSetOpOff                   // "turn off X", "disable X"
	voiceSetOpToggle                // "toggle X"
	voiceSetOpValue                 // "set X to Y"
	voiceSetOpUp                    // "make X bigger", "next X"
	voiceSetOpDown                  // "lower X", "previous X"
	voiceSetOpMax                   // "X to max"
	voiceSetOpMin                   // "X to the minimum"
	voiceSetOpRead                  // "what is X"
	voiceSetOpIsOn                  // "is X on"
)

// reads reports whether the op only reads.
func (op voiceSettingOp) reads() bool { return op == voiceSetOpRead || op == voiceSetOpIsOn }

// voiceSettingAsk is one parsed settings sentence.
type voiceSettingAsk struct {
	op    voiceSettingOp
	name  string // the setting as said, folded ("dark mode")
	value string // what to set it to, folded ("5 minutes"); voiceSetOpValue only
}

// ── Parsing (pure) ───────────────────────────────────────────────────────

// voiceSettingPrefixes open a change, longest first.
var voiceSettingPrefixes = [...]struct {
	words string
	op    voiceSettingOp
}{
	{"turn back on", voiceSetOpOn}, {"switch back on", voiceSetOpOn}, {"turn on", voiceSetOpOn},
	{"switch on", voiceSetOpOn}, {"enable", voiceSetOpOn}, {"activate", voiceSetOpOn},
	{"turn off", voiceSetOpOff}, {"switch off", voiceSetOpOff}, {"shut off", voiceSetOpOff},
	{"disable", voiceSetOpOff}, {"deactivate", voiceSetOpOff},
	{"toggle", voiceSetOpToggle}, {"flip", voiceSetOpToggle},
	{"turn up", voiceSetOpUp}, {"increase", voiceSetOpUp}, {"raise", voiceSetOpUp}, {"boost", voiceSetOpUp},
	{"turn down", voiceSetOpDown}, {"decrease", voiceSetOpDown}, {"reduce", voiceSetOpDown}, {"lower", voiceSetOpDown},
	{"maximize", voiceSetOpMax}, {"maximise", voiceSetOpMax}, {"minimize", voiceSetOpMin}, {"minimise", voiceSetOpMin},
	{"bigger", voiceSetOpUp}, {"larger", voiceSetOpUp}, {"smaller", voiceSetOpDown},
	{"next", voiceSetOpUp}, {"previous", voiceSetOpDown},
}

// voiceSettingSuffixes close a change: "turn X on", "X off", "X up".
var voiceSettingSuffixes = [...]struct {
	words string
	op    voiceSettingOp
}{
	{"back on", voiceSetOpOn}, {"on", voiceSetOpOn}, {"off", voiceSetOpOff},
	{"up", voiceSetOpUp}, {"down", voiceSetOpDown},
}

// voiceSettingVerbs may lead a suffix form or a "to" form and say nothing
// themselves: "turn the lights off", "set the theme to ocean".
var voiceSettingVerbs = [...]string{"turn", "switch", "set", "put", "change", "adjust", "move", "make", "keep", "have"}

// voiceSettingReadPrefixes open a read-out, longest first.
var voiceSettingReadPrefixes = [...]string{
	"what is the current", "what is my current", "what is my", "what is the", "what is", "what are my",
	"what are the", "what are", "how is the", "how is my",
}

// voiceSettingReadEnds close "what is X …" and "which X …" forms.
var voiceSettingReadEnds = [...]string{
	"currently set to", "set to right now", "set to", "set at", "right now", "currently", "now", "at",
	"is on", "is set", "is selected", "is active", "is in use", "am i using", "do i have", "is it", "is this",
	"i am using", "is used",
}

// voiceSettingStateEnds close "is X on?" forms.
var voiceSettingStateEnds = [...]string{
	"turned on", "switched on", "turned off", "switched off", "on", "off", "enabled", "disabled",
	"active", "activated",
}

// voiceSettingUpWords and voiceSettingDownWords are what "make X …" can end with.
var (
	voiceSettingUpWords = voiceSet("bigger", "larger", "higher", "louder", "brighter", "faster", "longer",
		"stronger", "more", "up")
	voiceSettingDownWords = voiceSet("smaller", "lower", "quieter", "softer", "dimmer", "slower", "shorter",
		"weaker", "less", "down")
	voiceSettingMaxWords = voiceSet("max", "maximum", "the maximum", "the max", "highest", "the highest",
		"full", "biggest", "the biggest", "largest", "the largest", "all the way up")
	voiceSettingMinWords = voiceSet("min", "minimum", "the minimum", "the min", "lowest", "the lowest",
		"smallest", "the smallest", "all the way down")
	voiceSettingOnWords  = voiceSet("on", "enabled", "enable", "true", "yes", "active")
	voiceSettingOffWords = voiceSet("off", "disabled", "disable", "false", "no", "inactive", "none", "never")
)

// voiceSettingParse reads one normalised command (voiceNormalize) as a
// settings sentence. It only knows the shapes; whether a setting of that name
// exists is decided against the catalogue later, and a sentence that names
// none goes back to the ordinary "open" path. Pure.
func voiceSettingParse(s string) (voiceSettingAsk, bool) {
	words := strings.Fields(s)
	if len(words) < 2 || len(words) > 14 {
		return voiceSettingAsk{}, false
	}
	// "is X on", "are notifications off".
	if words[0] == "is" || words[0] == "are" {
		rest := strings.Join(words[1:], " ")
		for _, end := range voiceSettingStateEnds {
			if name, ok := voiceCutSuffixWords(rest, end); ok && name != "" {
				return voiceSettingNamed(voiceSetOpIsOn, name, "")
			}
		}
		return voiceSettingAsk{}, false
	}
	// "which theme is on", "what language am i using".
	if words[0] == "which" || words[0] == "what" {
		rest := strings.Join(words[1:], " ")
		for _, end := range voiceSettingReadEnds {
			if name, ok := voiceCutSuffixWords(rest, end); ok && name != "" && !strings.HasPrefix(name, "is ") &&
				!strings.HasPrefix(name, "are ") {
				return voiceSettingNamed(voiceSetOpRead, name, "")
			}
		}
		if words[0] == "which" {
			return voiceSettingNamed(voiceSetOpRead, rest, "")
		}
	}
	// "what is the text size", "what is my brightness set to".
	for _, p := range voiceSettingReadPrefixes {
		if rest, ok := voiceCutWords(s, p); ok && rest != "" {
			for _, end := range voiceSettingReadEnds {
				if name, ok := voiceCutSuffixWords(rest, end); ok && name != "" {
					rest = name
					break
				}
			}
			return voiceSettingNamed(voiceSetOpRead, rest, "")
		}
	}
	// "switch to dark mode", "use the ocean theme".
	for _, p := range [...]string{"switch to", "change to", "use"} {
		rest, ok := voiceCutWords(s, p)
		if !ok {
			continue
		}
		name := voiceSettingTrimName(rest)
		if theme, ok := voiceCutSuffixWords(name, "theme"); ok && theme != "" {
			return voiceSettingValue("theme", theme)
		}
		if _, ok := voiceCutSuffixWords(name, "mode"); ok && name != "mode" {
			return voiceSettingNamed(voiceSetOpOn, name, "")
		}
	}
	// "turn on dark mode", "increase the text size to 130".
	for _, p := range voiceSettingPrefixes {
		rest, ok := voiceCutWords(s, p.words)
		if !ok || rest == "" {
			continue
		}
		if p.op == voiceSetOpUp || p.op == voiceSetOpDown {
			if name, value, ok := voiceSettingSplitTo(rest); ok {
				return voiceSettingValue(name, value)
			}
			// "increase the volume by 20" is a level, not a stop.
			if strings.Contains(" "+rest+" ", " by ") {
				return voiceSettingAsk{}, false
			}
		}
		return voiceSettingNamed(p.op, rest, "")
	}
	// "set the theme to ocean", "change screen timeout to 5 minutes",
	// "make the text bigger".
	for _, v := range voiceSettingVerbs {
		rest, ok := voiceCutWords(s, v)
		if !ok || rest == "" {
			continue
		}
		if name, value, ok := voiceSettingSplitTo(rest); ok {
			return voiceSettingValue(name, value)
		}
		if v == "make" {
			rw := strings.Fields(rest)
			last := rw[len(rw)-1]
			if len(rw) >= 2 {
				name := strings.Join(rw[:len(rw)-1], " ")
				switch {
				case voiceSettingUpWords[last]:
					return voiceSettingNamed(voiceSetOpUp, name, "")
				case voiceSettingDownWords[last]:
					return voiceSettingNamed(voiceSetOpDown, name, "")
				}
			}
		}
		break
	}
	// "turn the lights off", "dark mode on", "text size up".
	for _, suf := range voiceSettingSuffixes {
		rest, ok := voiceCutSuffixWords(s, suf.words)
		if !ok || rest == "" {
			continue
		}
		for _, v := range voiceSettingVerbs {
			if r, ok := voiceCutWords(rest, v); ok && r != "" {
				rest = r
				break
			}
		}
		// "Switch on", "turn off" on their own name no setting: "switch" was
		// read as Switch access.
		for _, v := range voiceSettingVerbs {
			if rest == v {
				return voiceSettingAsk{}, false
			}
		}
		return voiceSettingNamed(suf.op, rest, "")
	}
	// "text size to large" — but "go to settings" is a place.
	if name, value, ok := voiceSettingSplitTo(s); ok && !voiceStartsWithVerb(s) {
		return voiceSettingValue(name, value)
	}
	return voiceSettingAsk{}, false
}

// voiceSettingSplitTo splits "X to Y" (or "X at Y") at the last "to".
func voiceSettingSplitTo(s string) (string, string, bool) {
	padded := " " + s + " "
	for _, sep := range [...]string{" to ", " at "} {
		if i := strings.LastIndex(padded, sep); i > 0 {
			name := strings.TrimSpace(padded[:i])
			value := strings.TrimSpace(padded[i+len(sep):])
			if name != "" && value != "" {
				return name, value, true
			}
		}
	}
	return "", "", false
}

// voiceSettingValue reads the Y of "set X to Y": on/off and max/min words
// become their ops, anything else is a value to find among the stops.
func voiceSettingValue(name, value string) (voiceSettingAsk, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "be "))
	switch {
	case voiceSettingOnWords[value]:
		return voiceSettingNamed(voiceSetOpOn, name, "")
	case voiceSettingOffWords[value]:
		return voiceSettingNamed(voiceSetOpOff, name, "")
	case voiceSettingMaxWords[value]:
		return voiceSettingNamed(voiceSetOpMax, name, "")
	case voiceSettingMinWords[value]:
		return voiceSettingNamed(voiceSetOpMin, name, "")
	}
	return voiceSettingNamed(voiceSetOpValue, name, value)
}

// voiceSettingNamed builds the ask with the name tidied; an empty name is no ask.
func voiceSettingNamed(op voiceSettingOp, name, value string) (voiceSettingAsk, bool) {
	name = voiceSettingTrimName(name)
	if name == "" {
		return voiceSettingAsk{}, false
	}
	return voiceSettingAsk{op: op, name: name, value: value}, true
}

// voiceSettingTrimName drops the articles and the "setting" words around a
// setting's name.
func voiceSettingTrimName(s string) string {
	s = strings.TrimSpace(s)
	for changed := true; changed; {
		changed = false
		for _, w := range [...]string{"the", "my", "a", "an", "your", "current", "this", "jukahubs", "jukahub"} {
			if rest, ok := voiceCutWords(s, w); ok && rest != "" {
				s, changed = rest, true
			}
		}
		for _, w := range [...]string{"setting", "settings", "option", "options", "feature", "switch", "please", "for me"} {
			if rest, ok := voiceCutSuffixWords(s, w); ok && rest != "" {
				s, changed = rest, true
			}
		}
	}
	return s
}

// voiceSettingVolumeNames are what "turn the music down" means by the music:
// the device volume, not a setting called Music.
var voiceSettingVolumeNames = voiceSet("music", "song", "songs", "it", "sound", "audio", "game", "this", "speaker",
	"speakers", "headphones")

// voiceSettingAsCommand reads a sentence about "the music" as the command it
// is: "turn the music down" steps the volume, "turn off the music" pauses
// what plays (or switches the menus' background music off, voiceMusic),
// "music on" plays. Pure.
func voiceSettingAsCommand(ask voiceSettingAsk) (voiceIntentKind, int, bool) {
	if !voiceSettingVolumeNames[ask.name] {
		return voiceIntentNone, 0, false
	}
	switch ask.op {
	case voiceSetOpUp:
		return voiceIntentVolumeStep, voiceStepDefault, true
	case voiceSetOpDown:
		return voiceIntentVolumeStep, -voiceStepDefault, true
	}
	if ask.name == "music" || ask.name == "song" || ask.name == "songs" {
		switch ask.op {
		case voiceSetOpOff:
			return voiceIntentMusicPause, 0, true
		case voiceSetOpOn:
			return voiceIntentMusicPlay, 0, true
		}
	}
	return voiceIntentNone, 0, false
}

// ── The catalogue ────────────────────────────────────────────────────────

// voiceSettingCand is one setting a sentence can name.
type voiceSettingCand struct {
	Row      setRow
	Section  string // section id, for opening the row
	Where    string // section title
	Glyph    string
	NavRow   string // the row Settings opens on (a voice-only row opens a real one)
	Name     string // what the answers call it ("Dark mode", "Animations")
	Invert   bool   // a switch said the other way round: "animations off" = Reduce motion on
	Synonyms []voiceSettingSyn
}

// voiceSettingSyn is a spoken name for a row.
type voiceSettingSyn struct {
	ID     string
	Phrase string
	Invert bool
	Name   string // the answer's name when not the row's label ("" keeps it)
}

// voiceSettingSynonyms are the names people say that a row's label does not
// carry. An ID not in the catalogue (a row another build lacks) is skipped.
var voiceSettingSynonyms = [...]voiceSettingSyn{
	{ID: "lowPower", Phrase: "low power mode"}, {ID: "lowPower", Phrase: "power saving"},
	{ID: "lowPower", Phrase: "power saving mode"}, {ID: "lowPower", Phrase: "power saver"},
	{ID: "batterySaver", Phrase: "battery saver"}, {ID: "batterySaver", Phrase: "battery saving"},
	{ID: "bgMusic", Phrase: "background music"}, {ID: "bgMusic", Phrase: "menu music"},
	{ID: "bgMusic", Phrase: "bgm"}, {ID: "bgMusic", Phrase: "music", Name: "Background music"},
	{ID: "bgMusicVolume", Phrase: "background music volume"}, {ID: "bgMusicVolume", Phrase: "background music level"},
	{ID: "bgMusicVolume", Phrase: "bgm volume"},
	{ID: "uiSounds", Phrase: "ui sounds"}, {ID: "uiSounds", Phrase: "click sounds"}, {ID: "uiSounds", Phrase: "clicks"},
	{ID: "uiSounds", Phrase: "button sounds"}, {ID: "uiSounds", Phrase: "menu sounds"},
	{ID: "uiSounds", Phrase: "sound effects"}, {ID: "uiSoundLevel", Phrase: "click volume"},
	{ID: "uiSoundLevel", Phrase: "interface volume"}, {ID: "uiSoundLevel", Phrase: "sound effects volume"},
	{ID: "startupSound", Phrase: "startup sound"}, {ID: "startupSound", Phrase: "boot sound"},
	{ID: "startupSound", Phrase: "startup chime"}, {ID: "startupSound", Phrase: "chime"},
	{ID: "vibration", Phrase: "haptics"}, {ID: "vibration", Phrase: "rumble"},
	{ID: "vibrationLevel", Phrase: "vibration level"}, {ID: "vibrationLevel", Phrase: "rumble strength"},
	{ID: "motion", Phrase: "animations", Invert: true, Name: "Animations"},
	{ID: "motion", Phrase: "motion", Invert: true, Name: "Motion"},
	{ID: "motion", Phrase: "transitions", Invert: true, Name: "Transitions"},
	{ID: "reduceTransparency", Phrase: "transparency", Invert: true, Name: "Transparency"},
	{ID: "contrast", Phrase: "high contrast mode"},
	{ID: "textSize", Phrase: "text"}, {ID: "textSize", Phrase: "font size"}, {ID: "textSize", Phrase: "font"},
	{ID: "textSize", Phrase: "text scale"}, {ID: "textSize", Phrase: "letters"},
	{ID: "screensaver", Phrase: "screen timeout"}, {ID: "screensaver", Phrase: "screensaver"},
	{ID: "screensaver", Phrase: "screen saver"}, {ID: "screensaver", Phrase: "screensaver timeout"},
	{ID: "screensaver", Phrase: "display timeout"}, {ID: "screensaver", Phrase: "screen timer"},
	{ID: "autoSleep", Phrase: "sleep timer"},
	{ID: "notifyDuringVideo", Phrase: "notifications during video"},
	{ID: "notifyDuringVideo", Phrase: "video notifications"},
	{ID: "clock12", Phrase: "12 hour clock"}, {ID: "clock12", Phrase: "twelve hour clock"},
	{ID: "clock12", Phrase: "12 hour time"}, {ID: "clock12", Phrase: "am pm"},
	{ID: "clock12", Phrase: "24 hour clock", Invert: true, Name: "24-hour clock"},
	{ID: "clock12", Phrase: "twenty four hour clock", Invert: true, Name: "24-hour clock"},
	{ID: "clock12", Phrase: "24 hour time", Invert: true, Name: "24-hour clock"},
	{ID: "livebg", Phrase: "dynamic background"}, {ID: "livebg", Phrase: "live background"},
	{ID: "liquidBg", Phrase: "water"}, {ID: "liquidBg", Phrase: "liquid"}, {ID: "liquidBg", Phrase: "water background"},
	{ID: "focusBounce", Phrase: "focus bounce"}, {ID: "focusBounce", Phrase: "bounce"}, {ID: "focusBounce", Phrase: "bouncy effect"},
	{ID: "wallpaperAnimate", Phrase: "animated wallpaper"}, {ID: "wallpaperAnimate", Phrase: "gif wallpaper"},
	{ID: "timeTheme", Phrase: "automatic dark mode"}, {ID: "timeTheme", Phrase: "auto dark mode"},
	{ID: "timeTheme", Phrase: "theme schedule"}, {ID: "timeTheme", Phrase: "light and dark by time of day"},
	{ID: "weather", Phrase: "weather forecast"}, {ID: "weather", Phrase: "forecast"},
	{ID: "ledsync", Phrase: "lights"}, {ID: "ledsync", Phrase: "light"}, {ID: "ledsync", Phrase: "leds"}, {ID: "ledsync", Phrase: "led lights"},
	{ID: "ledsync", Phrase: "rgb"}, {ID: "ledsync", Phrase: "rgb lights"},
	{ID: "virtualCursor", Phrase: "cursor"}, {ID: "virtualCursor", Phrase: "pointer"},
	{ID: "wakeWord", Phrase: "hey juka"}, {ID: "wakeWord", Phrase: "wake word"},
	{ID: "wakeWord", Phrase: "always listening"}, {ID: "wakeWord", Phrase: "juka wake word"},
	{ID: "uiLanguage", Phrase: "interface language"}, {ID: "uiLanguage", Phrase: "system language"},
	{ID: "powerMode", Phrase: "power profile"}, {ID: "powerMode", Phrase: "performance mode"},
	{ID: "cpuBoost", Phrase: "full speed"}, {ID: "cpuBoost", Phrase: "cpu boost"}, {ID: "cpuBoost", Phrase: "turbo"},
	{ID: "swapAB", Phrase: "swap a b"}, {ID: "swapXY", Phrase: "swap x y"},
	{ID: "blockAds", Phrase: "ad blocker"}, {ID: "blockAds", Phrase: "adblock"}, {ID: "blockAds", Phrase: "ad block"},
	{ID: "subtitles", Phrase: "subtitles"}, {ID: "subtitles", Phrase: "captions"},
	{ID: "resolution", Phrase: "video quality"}, {ID: "resolution", Phrase: "playback quality"},
	{ID: "menuHoldScreenshot", Phrase: "menu screenshot"},
	{ID: "voiceMenuButton", Phrase: "menu button voice"}, {ID: "voiceMenuButton", Phrase: "voice button"},
	{ID: "javaScript", Phrase: "java script"},
	{ID: "weatherUnit", Phrase: "temperature units"}, {ID: "weatherUnit", Phrase: "units"},
	{ID: "jukaVoice", Phrase: "spoken answers"}, {ID: "jukaVoice", Phrase: "voice answers"},
	{ID: "jukaVoice", Phrase: "talking"}, {ID: "jukaVoice", Phrase: "speech"},
	{ID: "gameAssistSpeak", Phrase: "read answers aloud"},
	{ID: "gameAssist", Phrase: "game assist"},
	{ID: "netTime", Phrase: "automatic time"}, {ID: "netTime", Phrase: "network time"},
	{ID: "airplane", Phrase: "flight mode"},
	{ID: "voiceDarkMode", Phrase: "dark theme"}, {ID: "voiceDarkMode", Phrase: "night mode"},
	{ID: "voiceDarkMode", Phrase: "light mode", Invert: true, Name: "Light mode"},
	{ID: "voiceDarkMode", Phrase: "light theme", Invert: true, Name: "Light mode"},
	{ID: "voiceTheme", Phrase: "colour theme"}, {ID: "voiceTheme", Phrase: "color theme"},
	{ID: "voiceTheme", Phrase: "colours"}, {ID: "voiceTheme", Phrase: "colors"}, {ID: "voiceTheme", Phrase: "theme preset"},
	{ID: "voiceQuiet", Phrase: "do not disturb"}, {ID: "voiceQuiet", Phrase: "dnd"},
	{ID: "voiceQuiet", Phrase: "silent mode"}, {ID: "voiceQuiet", Phrase: "quiet"},
	{ID: "voiceQuiet", Phrase: "notifications", Invert: true, Name: "Notifications"},
	{ID: "voiceMagnifier", Phrase: "zoom"}, {ID: "voiceMagnifier", Phrase: "magnifying glass"},
	{ID: "voiceWifi", Phrase: "wireless"}, {ID: "voiceWifi", Phrase: "internet"},
}

// voiceSettingSkipSections are never searched: plugin rows are built from
// live data and act on code JukaHub does not own.
var voiceSettingSkipSections = voiceSet("plugins")

// voiceSettingCatalog is every row voice can name, in Settings' order, each
// once (Accessibility repeats rows of Display & theme under the same ids),
// with the voice-only rows last.
func voiceSettingCatalog() []voiceSettingCand {
	out := make([]voiceSettingCand, 0, 200)
	seen := make(map[string]bool, 200)
	byID := make(map[string]int, 200)
	for _, sec := range setSections() {
		if voiceSettingSkipSections[sec.ID] || sec.Dynamic != nil {
			continue
		}
		for _, row := range sec.Rows {
			key := row.ID + "\x00" + row.Label
			if seen[key] || strings.TrimSpace(row.Label) == "" {
				continue
			}
			seen[key] = true
			if _, ok := byID[row.ID]; !ok && row.ID != "" {
				byID[row.ID] = len(out)
			}
			out = append(out, voiceSettingCand{Row: row, Section: sec.ID, Where: sec.Title, Glyph: sec.Glyph,
				NavRow: row.ID, Name: row.Label})
		}
	}
	for _, v := range voiceSettingVirtualRows() {
		byID[v.Row.ID] = len(out)
		out = append(out, v)
	}
	for _, syn := range voiceSettingSynonyms {
		if i, ok := byID[syn.ID]; ok {
			out[i].Synonyms = append(out[i].Synonyms, syn)
		}
	}
	return out
}

// voiceThemePreset is the theme in use ("hub", JukaHub's own glass, when none
// was picked).
func voiceThemePreset(c *Config) string {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, "theme_preset"); ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return "hub"
}

// voiceThemeOptions are the colour themes, in the gallery's order. Total
// conversions rearrange Home and stay in the gallery.
func voiceThemeOptions(*Config) []setOption {
	names := ListThemePresetsFor(themeKindColour)
	out := make([]setOption, 0, len(names))
	for _, n := range names {
		out = append(out, setOption{Value: n, Label: setOr(GetThemePreset(n).Name, n)})
	}
	return out
}

// voiceSettingVirtualRows are the rows that exist only for voice: dark mode,
// the theme (the gallery is a screen, not a row) and a Wi-Fi read-out (the
// Wi-Fi row opens the network list; "Wi-Fi off" is the grammar's own).
func voiceSettingVirtualRows() []voiceSettingCand {
	dark := setRow{
		ID: "voiceDarkMode", Label: "Dark mode", Kind: setKindToggle,
		Get: func(c *Config) bool { return !themePresetIsLight(voiceThemePreset(c)) },
		Set: func(c *Config, on bool) {
			if on {
				ApplyThemePreset(c, "dark")
				return
			}
			ApplyThemePreset(c, "light")
		},
	}
	theme := setRow{
		ID: "voiceTheme", Label: "Theme", Kind: setKindChoice,
		Options: voiceThemeOptions,
		Current: func(c *Config) int {
			cur := voiceThemePreset(c)
			for i, o := range voiceThemeOptions(c) {
				if o.Value == cur {
					return i
				}
			}
			return -1
		},
		Value:  func(c *Config) string { return setOr(GetThemePreset(voiceThemePreset(c)).Name, voiceThemePreset(c)) },
		Choose: func(c *Config, value string) { ApplyThemePreset(c, value) },
	}
	wifi := setRow{
		ID: "voiceWifi", Label: "Wi-Fi", Kind: setKindToggle,
		Visible: setQSAvailable(qsActionWifi),
		// The switch's last reading: asking the radio runs a command, which
		// has no place on the render thread for a read-out.
		Get: setQSRead(qsActionWifi),
	}
	// Quick Settings' own switches that have no Settings row.
	quiet := setRow{
		ID: "voiceQuiet", Label: "Quiet Mode", Kind: setKindToggle, Quiet: true,
		Visible: setQSAvailable(qsActionQuiet),
		Get:     setQSRead(qsActionQuiet),
		Set:     setQSWrite(qsActionQuiet),
	}
	magnifier := setRow{
		ID: "voiceMagnifier", Label: "Magnifier", Kind: setKindToggle, Quiet: true,
		Visible: setQSAvailable(qsActionMagnifier),
		Get:     setQSRead(qsActionMagnifier),
		Set:     setQSWrite(qsActionMagnifier),
	}
	return []voiceSettingCand{
		{Row: quiet, Section: "sound", Where: "Quick Settings", Glyph: "settings", Name: "Quiet Mode"},
		{Row: magnifier, Section: a11ySettingsSection, Where: "Quick Settings", Glyph: "settings", Name: "Magnifier"},
		{Row: dark, Section: "display", Where: "Display & theme", Glyph: "palette", NavRow: "themes", Name: "Dark mode"},
		{Row: theme, Section: "display", Where: "Display & theme", Glyph: "palette", NavRow: "themes", Name: "Theme"},
		{Row: wifi, Section: "network", Where: "Network", Glyph: "wifi", NavRow: "wifi", Name: "Wi-Fi"},
	}
}

// ── Matching ─────────────────────────────────────────────────────────────

// voiceSettingFold folds a label the way speech is folded, mishearings
// included, so "Wi-Fi" and "wifi", "Juka's voice" and "jukas voice" meet.
func voiceSettingFold(s string) string {
	s = " " + voiceFold(s) + " "
	for _, p := range voicePhraseAlikes {
		s = strings.ReplaceAll(s, " "+p[0]+" ", " "+p[1]+" ")
	}
	words := strings.Fields(s)
	for i, w := range words {
		if fix, ok := voiceSoundAlikes[w]; ok {
			words[i] = fix
		}
	}
	return strings.Join(words, " ")
}

// voiceSettingNoise are words that do not tell two settings apart.
var voiceSettingNoise = voiceSet("the", "my", "a", "an", "mode", "your", "setting", "settings", "of", "for")

// voiceSettingCore is a folded name's telling words.
func voiceSettingCore(folded string) []string {
	var out []string
	for _, w := range strings.Fields(folded) {
		if !voiceSettingNoise[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return strings.Fields(folded)
	}
	return out
}

// voiceSettingWordIn reports whether w is one of words, a plural "s" aside.
func voiceSettingWordIn(w string, words []string) bool {
	for _, x := range words {
		if x == w || x+"s" == w || x == w+"s" {
			return true
		}
	}
	return false
}

// Match scores. A synonym said exactly beats a label said exactly, so the
// spoken "music" means the Background music switch, not the Music list.
const (
	voiceSetScoreSynonym       = 1100
	voiceSetScoreExact         = 1000
	voiceSetScoreContained     = 600 // every word said is in the name
	voiceSetScoreCovers        = 450 // every word of the name was said, and more
	voiceSetScoreFuzzyMax      = 500 // a near-miss of the whole name
	voiceSetScoreMin           = 450 // weaker never changes anything
	voiceSetScoreReadMin       = 600 // a read-out needs the name said whole
	voiceSetScoreActionPenalty = 400 // a page loses to a switch for "turn off …"
	voiceSetScoreWordForWord   = 50  // the exact words, not only the telling ones
	voiceSetScoreSection       = 50  // the section said too: "stick lights brightness"
	voiceSetFuzzyMin           = 0.84
	voiceSetTieGap             = 40
)

// voiceSettingScoreName scores the folded spoken name against one folded
// name. saidFull is the name as said, "mode" and articles included: said word
// for word it beats a name that only matches without them ("light mode" is
// the theme, "light" the stick lights).
func voiceSettingScoreName(said []string, saidCompact, saidFull string, name string) int {
	nameWords := voiceSettingCore(name)
	if len(nameWords) == 0 || len(said) == 0 {
		return 0
	}
	if saidFull == name {
		return voiceSetScoreExact + voiceSetScoreWordForWord
	}
	nameCompact := strings.Join(nameWords, "")
	if saidCompact == nameCompact {
		return voiceSetScoreExact
	}
	all := true
	for _, w := range said {
		if !voiceSettingWordIn(w, nameWords) {
			all = false
			break
		}
	}
	if all {
		return voiceSetScoreContained + 100*len(said)/len(nameWords)
	}
	covers := true
	for _, w := range nameWords {
		if !voiceSettingWordIn(w, said) {
			covers = false
			break
		}
	}
	// The name must be at least half of what was said: "turn on the lights in
	// my room" is not the stick lights.
	if covers && 2*len(nameWords) >= len(said) {
		return voiceSetScoreCovers + 100*len(nameWords)/len(said)
	}
	if utf8Len(saidCompact) >= 4 {
		if sim := voiceSimilarity(saidCompact, nameCompact); sim >= voiceSetFuzzyMin {
			return int(float64(voiceSetScoreFuzzyMax) * sim)
		}
	}
	return 0
}

// utf8Len is the length of s in runes.
func utf8Len(s string) int { return len([]rune(s)) }

// voiceSettingFits reports whether a row can do what op asks.
func voiceSettingFits(row setRow, op voiceSettingOp) bool {
	switch op {
	case voiceSetOpOn, voiceSetOpOff, voiceSetOpToggle:
		return row.Kind == setKindToggle || row.Kind == setKindChoice || row.Kind == setKindAction
	case voiceSetOpValue, voiceSetOpUp, voiceSetOpDown, voiceSetOpMax, voiceSetOpMin:
		return row.Kind == setKindChoice
	case voiceSetOpIsOn:
		return row.Kind == setKindToggle || row.Kind == setKindChoice
	case voiceSetOpRead:
		// "What is the weather" is the forecast, not the Weather switch: a
		// switch is read with "is it on".
		return row.Kind == setKindChoice || (row.Kind != setKindToggle && row.Value != nil && !row.Secret)
	}
	return false
}

// voiceSettingHit is a scored candidate, with the spoken name it matched.
type voiceSettingHit struct {
	Cand  voiceSettingCand
	Score int
	// Hidden rows mean nothing on this device (no motor, desktop only).
	Hidden bool
}

// voiceSettingRank scores every candidate that can do op against the spoken
// name, best first; section words said along with it ("stick lights
// brightness") break ties. c decides which rows are shown here.
func voiceSettingRank(c *Config, cands []voiceSettingCand, name string, op voiceSettingOp) []voiceSettingHit {
	folded := voiceSettingFold(name)
	said := voiceSettingCore(folded)
	if len(said) == 0 {
		return nil
	}
	saidCompact := strings.Join(said, "")
	var out []voiceSettingHit
	for _, cand := range cands {
		if !voiceSettingFits(cand.Row, op) {
			continue
		}
		best := voiceSettingScoreName(said, saidCompact, folded, voiceSettingFold(cand.Row.Label))
		hit := cand
		for _, syn := range cand.Synonyms {
			sc := voiceSettingScoreName(said, saidCompact, folded, voiceSettingFold(syn.Phrase))
			if sc >= voiceSetScoreExact {
				sc += voiceSetScoreSynonym - voiceSetScoreExact
			}
			if sc > best {
				best = sc
				hit = cand
				hit.Invert = syn.Invert
				if syn.Name != "" {
					hit.Name = syn.Name
				}
			}
		}
		if cand.Row.Kind == setKindAction && !op.reads() {
			// "Disable updates" is the Download updates switch before the
			// Updates page, which a change can only open.
			best -= voiceSetScoreActionPenalty
		}
		if best <= 0 {
			continue
		}
		if best < voiceSetScoreExact && cand.Where != "" {
			for _, w := range voiceSettingCore(voiceSettingFold(cand.Where)) {
				if voiceHasWord(said, w) {
					best += voiceSetScoreSection
					break
				}
			}
		}
		hidden := c != nil && cand.Row.Visible != nil && !cand.Row.Visible(c)
		out = append(out, voiceSettingHit{Cand: hit, Score: best, Hidden: hidden})
	}
	// Stable: on equal scores Settings' own order decides (Display's
	// Brightness before the stick lights').
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// voiceSettingDecide picks the setting a sentence means: the best when it is
// strong enough and clearly ahead, else the close ones to choose from (nil
// and nil when nothing is close).
func voiceSettingDecide(hits []voiceSettingHit, op voiceSettingOp) (*voiceSettingHit, []voiceSettingHit) {
	least := voiceSetScoreMin
	if op.reads() {
		least = voiceSetScoreReadMin
	}
	if len(hits) == 0 || hits[0].Score < least {
		return nil, nil
	}
	// A row hidden on this device only answers ("isn't available here") when
	// no shown row matches about as well: "Set time automatically" is two
	// rows, one per platform.
	var shown []voiceSettingHit
	for _, h := range hits {
		if !h.Hidden {
			shown = append(shown, h)
		}
	}
	if len(shown) > 0 && len(shown) < len(hits) && shown[0].Score >= least &&
		shown[0].Score+voiceSetTieGap > hits[0].Score {
		hits = shown
	}
	best := hits[0]
	if best.Score >= voiceSetScoreExact || len(hits) == 1 || best.Score-hits[1].Score >= voiceSetTieGap {
		return &best, nil
	}
	var near []voiceSettingHit
	for _, h := range hits {
		if h.Score < least || best.Score-h.Score >= voiceSetTieGap || len(near) >= voiceMaxChoices {
			break
		}
		near = append(near, h)
	}
	if len(near) == 1 {
		return &near[0], nil
	}
	return nil, near
}

// ── Running ──────────────────────────────────────────────────────────────

// voiceSettingExecute runs a settings intent. A sentence that names no setting
// is the ordinary "open" it would have been before.
func voiceSettingExecute(config *Config, in voiceIntent) voiceOutcome {
	cat := voiceSettingCatalog()
	hits := voiceSettingRank(config, cat, in.Setting, in.SetOp)
	best, near := voiceSettingDecide(hits, in.SetOp)
	if best == nil && len(near) < 2 && (in.SetOp == voiceSetOpUp || in.SetOp == voiceSetOpDown) {
		// "Reduce motion", "increase contrast": the verb is the switch's own
		// first word, so the whole sentence is its name.
		whole, _ := voiceSettingDecide(voiceSettingRank(config, cat, in.Heard, voiceSetOpOn), voiceSetOpOn)
		if whole != nil && whole.Score >= voiceSetScoreExact && whole.Cand.Row.Kind == setKindToggle {
			best, in.SetOp = whole, voiceSetOpOn
		}
	}
	if best != nil && !voiceSettingSure(in, *best) {
		// Only part of a name: what "open" made of the sentence before.
		best = nil
	}
	if best == nil && in.SetOp == voiceSetOpRead && !voiceSettingReadDefinite(in.Heard) {
		near = nil
	}
	if best != nil {
		logDebug("voice", "setting %q → %s (score %d)", in.Setting, best.Cand.Row.ID, best.Score)
		return voiceSettingRun(config, best.Cand, in.SetOp, in.SetTo)
	}
	if len(near) > 1 {
		choices := make([]voiceMatch, 0, len(near))
		for _, h := range near {
			cand, op, value := h.Cand, in.SetOp, in.SetTo
			choices = append(choices, voiceMatch{Score: h.Score, Target: voiceTarget{
				Title: cand.Name, Where: "Settings · " + cand.Where, Icon: setOr(cand.Glyph, "settings"),
				Kind: voiceTargetSetting, Say: "Changing " + cand.Name,
				Action: func(c *Config) { voiceSettingReport(c, voiceSettingRun(c, cand, op, value)) },
			}})
		}
		return voiceOutcome{Say: "Which setting did you mean?", Choices: choices}
	}
	if in.Target == "" {
		return voiceOutcome{Say: "Didn't catch a command — say what to open, like “open YouTube”", Tone: tbxToneWarn}
	}
	if voiceHomeTargets[in.Target] && in.Verb != "play" {
		return voiceOutcome{Say: "Going Home", Act: voiceGoHome}
	}
	return voiceOpenTarget(config, voiceIntent{Kind: voiceIntentOpen, Verb: in.Verb, Target: in.Target, Heard: in.Heard})
}

// voiceSettingSure reports whether a match that is not the whole name said is
// still safe to act on without asking. A setting said in full always is. A
// risky row (voiceSettingSayInFull) never is; nor is one word that is only part
// of a name for a change ("next video" is not Video filter, "disable ads" not
// the ad blocker turned off); nor a read-out without "my"/"the"/"set" ("what
// is this game" is a question, not Game library).
func voiceSettingSure(in voiceIntent, h voiceSettingHit) bool {
	if h.Score >= voiceSetScoreExact {
		return true
	}
	if voiceSettingSayInFull(h.Cand.Row.ID) {
		return false
	}
	if in.SetOp == voiceSetOpRead {
		return voiceSettingReadDefinite(in.Heard)
	}
	return in.SetOp.reads() || len(voiceSettingCore(voiceSettingFold(in.Setting))) >= 2
}

// voiceSettingReadDefinite reports whether a "what is X" sentence is about the
// user's own setting rather than asking what X is.
func voiceSettingReadDefinite(heard string) bool {
	words := strings.Fields(heard)
	for _, w := range [...]string{"my", "the", "current", "currently", "set", "which"} {
		if voiceHasWord(words, w) {
			return true
		}
	}
	return false
}

// voiceSettingReport shows the outcome of a change picked from a list: on the
// voice page as its answer, anywhere else as a toast.
func voiceSettingReport(c *Config, out voiceOutcome) {
	if out.Act != nil {
		out.Act(c)
		return
	}
	if voiceOnPage(c) {
		voiceSay(out.Say, out.Tone, voiceFixNone)
		return
	}
	kind := ToastKindInfo
	if out.Tone == tbxToneWarn || out.Tone == tbxToneBad {
		kind = ToastKindWarning
	}
	ShowToast(out.Say, kind)
}

// voiceSettingWarn is a "could not" answer.
func voiceSettingWarn(say string) voiceOutcome { return voiceOutcome{Say: say, Tone: tbxToneWarn} }

// voiceSettingOpen opens Settings on the row instead of pressing it.
func voiceSettingOpen(cand voiceSettingCand, say string) voiceOutcome {
	section, row, label := cand.Section, cand.NavRow, cand.Row.Label
	if say == "" {
		say = "Opening " + cand.Name + " in Settings"
	}
	return voiceOutcome{Say: say, Act: func(c *Config) { setOpenSettingRow(c, section, row, label) }}
}

// voiceSettingRun reads or changes one setting through its row. config is the
// live one; main thread only.
func voiceSettingRun(config *Config, cand voiceSettingCand, op voiceSettingOp, value string) voiceOutcome {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return voiceSettingWarn("Settings are not ready yet")
	}
	row, name := cand.Row, cand.Name
	if row.Visible != nil && !row.Visible(config) {
		return voiceSettingWarn(name + " isn't available on this device")
	}
	if op.reads() {
		return voiceSettingRead(config, cand, op)
	}
	if row.Kind != setKindToggle && row.Kind != setKindChoice {
		// A page, a text field, a key, a reset: voice never presses those.
		return voiceSettingOpen(cand, "")
	}
	if row.Disabled != nil && row.Disabled(config) {
		why := ""
		if row.Detail != nil {
			why = strings.TrimSpace(row.Detail(config))
		}
		if why == "" {
			return voiceSettingWarn(name + " can't be changed right now")
		}
		return voiceSettingWarn(name + ": " + why)
	}
	var out voiceOutcome
	if row.Kind == setKindToggle {
		out = voiceSettingToggle(config, cand, op, value)
	} else {
		out = voiceSettingChoice(config, cand, op, value)
	}
	if out.Done {
		// Settings, if it is open behind the orb, shows the new value.
		setHub.dirty = true
	}
	return out
}

// voiceSettingToggle runs a switch.
func voiceSettingToggle(config *Config, cand voiceSettingCand, op voiceSettingOp, value string) voiceOutcome {
	row, name := cand.Row, cand.Name
	if row.Get == nil || row.Set == nil {
		if cand.Row.ID == "voiceWifi" {
			return voiceSettingWarn("Say “turn Wi-Fi on” or “turn Wi-Fi off”")
		}
		return voiceSettingOpen(cand, "")
	}
	cur := row.Get(config) != cand.Invert
	var want bool
	switch op {
	case voiceSetOpOn, voiceSetOpUp, voiceSetOpMax:
		want = true
	case voiceSetOpOff, voiceSetOpDown, voiceSetOpMin:
		want = false
	case voiceSetOpToggle:
		want = !cur
	default:
		switch {
		case voiceSettingOnWords[value]:
			want = true
		case voiceSettingOffWords[value]:
			want = false
		default:
			return voiceSettingWarn(name + " is a switch — say on or off")
		}
	}
	if want == cur {
		return voiceOutcome{Done: true, Say: name + " is already " + strings.ToLower(setOnOff(want))}
	}
	row.Set(config, want != cand.Invert)
	if now := row.Get(config) != cand.Invert; now != want {
		if row.Quiet {
			// Quick Settings' switches report back a moment later.
			return voiceOutcome{Done: true, Say: "Turning " + name + " " + strings.ToLower(setOnOff(want))}
		}
		say := name + " did not switch " + strings.ToLower(setOnOff(want))
		if row.Detail != nil {
			if why := strings.TrimSpace(row.Detail(config)); why != "" {
				say += " — " + why
			}
		}
		return voiceSettingWarn(say)
	}
	return voiceOutcome{Done: true, Say: name + " " + strings.ToLower(setOnOff(want)), Tone: tbxToneGood}
}

// voiceSettingChoice runs a list.
func voiceSettingChoice(config *Config, cand voiceSettingCand, op voiceSettingOp, value string) voiceOutcome {
	row, name := cand.Row, cand.Name
	if row.Options == nil || row.Choose == nil {
		return voiceSettingOpen(cand, "")
	}
	opts := row.Options(config)
	n := len(opts)
	if n == 0 {
		return voiceSettingOpen(cand, "")
	}
	cur := -1
	if row.Current != nil {
		cur = row.Current(config)
	}
	if cur >= n {
		cur = -1
	}
	next := -1
	switch op {
	case voiceSetOpOn:
		if cur >= 0 && !voiceSettingOffOption(opts[cur]) {
			return voiceOutcome{Done: true, Say: name + " is already on (" + voiceOptionLabel(opts[cur]) + ")"}
		}
		next = voiceSettingOnIndex(opts)
		if next < 0 {
			return voiceSettingWarn(name + " has no on or off — say which, like “" + voiceOptionLabel(opts[n-1]) + "”")
		}
	case voiceSetOpOff:
		next = voiceSettingOffIndex(opts)
		if next < 0 {
			return voiceSettingWarn(name + " can't be turned off — " + voiceSettingChoicesLine(opts))
		}
	case voiceSetOpToggle:
		switch {
		case cur < 0:
			next = 0
		case cur+1 < n:
			next = cur + 1
		case row.Clamp:
			return voiceOutcome{Done: true, Say: name + " is already at the highest (" + voiceOptionLabel(opts[cur]) + ")"}
		default:
			next = 0
		}
	case voiceSetOpUp, voiceSetOpDown:
		dir := 1
		if op == voiceSetOpDown {
			dir = -1
		}
		switch {
		case cur < 0 && dir > 0:
			next = 0
		case cur < 0:
			next = n - 1
		default:
			next = cur + dir
		}
		if next < 0 || next >= n {
			end := "highest"
			if dir < 0 {
				end = "lowest"
			}
			return voiceOutcome{Done: true, Say: name + " is already at the " + end + " (" + voiceOptionLabel(opts[cur]) + ")"}
		}
	case voiceSetOpMax:
		next = n - 1
	case voiceSetOpMin:
		next = 0
	case voiceSetOpValue:
		next = voiceSettingPickOption(opts, value)
		if next < 0 {
			return voiceSettingWarn(name + " can be " + voiceSettingChoicesLine(opts))
		}
	}
	if next < 0 || next >= n {
		return voiceSettingOpen(cand, "")
	}
	if next == cur {
		return voiceOutcome{Done: true, Say: name + " is already " + voiceOptionLabel(opts[cur])}
	}
	row.Choose(config, opts[next].Value)
	return voiceOutcome{Done: true, Say: name + ": " + voiceOptionLabel(opts[next]), Tone: tbxToneGood}
}

// voiceSettingRead answers "is X on" and "what is X".
func voiceSettingRead(config *Config, cand voiceSettingCand, op voiceSettingOp) voiceOutcome {
	row, name := cand.Row, cand.Name
	switch row.Kind {
	case setKindToggle:
		if row.Get == nil {
			break
		}
		on := row.Get(config) != cand.Invert
		return voiceOutcome{Done: true, Say: name + " is " + strings.ToLower(setOnOff(on))}
	case setKindChoice:
		cur := -1
		var opts []setOption
		if row.Options != nil {
			opts = row.Options(config)
		}
		if row.Current != nil {
			cur = row.Current(config)
		}
		if cur >= 0 && cur < len(opts) {
			o := opts[cur]
			if op == voiceSetOpIsOn {
				if voiceSettingOffOption(o) {
					return voiceOutcome{Done: true, Say: name + " is off"}
				}
				return voiceOutcome{Done: true, Say: name + " is on (" + voiceOptionLabel(o) + ")"}
			}
			return voiceOutcome{Done: true, Say: name + " is " + voiceOptionLabel(o)}
		}
	}
	if row.Value != nil && !row.Secret {
		if v := strings.TrimSpace(row.Value(config)); v != "" && v != "—" {
			return voiceOutcome{Done: true, Say: name + ": " + v}
		}
	}
	return voiceSettingOpen(cand, "")
}

// voiceOptionLabel is a stop's label as said: "100% · default" → "100%,
// default", "5m" → "5 minutes".
func voiceOptionLabel(o setOption) string {
	l := strings.TrimSpace(o.Label)
	if l == "" {
		l = o.Value
	}
	if m := voiceShortTimeRe.FindStringSubmatch(l); m != nil {
		unit := map[string]string{"s": " second", "m": " minute", "h": " hour"}[m[2]]
		if m[1] != "1" {
			unit += "s"
		}
		return m[1] + unit
	}
	return strings.ReplaceAll(l, " · ", ", ")
}

// voiceShortTimeRe is a stop written as a short time: "30s", "5m", "1h".
var voiceShortTimeRe = regexp.MustCompile(`^(\d+)([smh])$`)

// voiceSettingChoicesLine lists a few stops: "Off, Slight, Medium or Lively".
func voiceSettingChoicesLine(opts []setOption) string {
	const shown = 6
	names := make([]string, 0, shown)
	for i, o := range opts {
		if i >= shown {
			break
		}
		names = append(names, voiceOptionLabel(o))
	}
	if len(opts) > shown {
		return strings.Join(names, ", ") + " and more"
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// voiceSettingOffOption reports whether a stop is the list's "off".
func voiceSettingOffOption(o setOption) bool {
	l := voiceSettingFold(o.Label)
	if cut := strings.Index(o.Label, " · "); cut > 0 {
		l = voiceSettingFold(o.Label[:cut])
	}
	switch l {
	case "off", "never", "none", "disabled", "no", "0 percent", "nothing":
		return true
	}
	return false
}

// voiceSettingOffIndex is the list's "off" stop, or -1.
func voiceSettingOffIndex(opts []setOption) int {
	for i, o := range opts {
		if voiceSettingOffOption(o) {
			return i
		}
	}
	return -1
}

// voiceSettingOnIndex is where "turn on" goes in a list: a stop called On,
// else the first stop that is not off — when the list has an off at all.
func voiceSettingOnIndex(opts []setOption) int {
	if voiceSettingOffIndex(opts) < 0 {
		return -1
	}
	for i, o := range opts {
		switch voiceSettingFold(o.Label) {
		case "on", "enabled", "always":
			return i
		}
	}
	for i, o := range opts {
		if !voiceSettingOffOption(o) {
			return i
		}
	}
	return -1
}

// voiceSettingValueAliases are values said in English for stops labelled in
// their own language or with a code.
var voiceSettingValueAliases = map[string]string{
	"english": "en", "spanish": "es", "portuguese": "pt-BR", "brazilian": "pt-BR", "french": "fr",
	"german": "de", "italian": "it", "russian": "ru", "celsius": "C", "fahrenheit": "F",
}

// voiceSettingPickOption finds the stop a spoken value means, or -1: its
// label or value said, a length of time ("5 minutes" for a "5m" stop), a
// number (the nearest level for "set music level to 35"), or a near-miss.
func voiceSettingPickOption(opts []setOption, value string) int {
	want := voiceSettingFold(value)
	if want == "" {
		return -1
	}
	if alias, ok := voiceSettingValueAliases[want]; ok {
		for i, o := range opts {
			if strings.EqualFold(o.Value, alias) {
				return i
			}
		}
	}
	wantCompact := strings.ReplaceAll(want, " ", "")
	for i, o := range opts {
		label := voiceSettingFold(o.Label)
		core := label
		if cut := strings.Index(o.Label, " · "); cut > 0 {
			core = voiceSettingFold(o.Label[:cut])
		}
		if want == label || want == core || wantCompact == strings.ReplaceAll(core, " ", "") ||
			(o.Value != "" && want == voiceSettingFold(o.Value)) {
			return i
		}
	}
	if secs, ok := voiceSettingDuration(want); ok {
		for i, o := range opts {
			if d, ok := voiceSettingDuration(voiceSettingFold(o.Label)); ok && d == secs {
				return i
			}
		}
	}
	if n, ok := voiceFindNumber(strings.Fields(want)); ok {
		best, bestD, all := -1, 0.0, true
		for i, o := range opts {
			v, ok := voiceSettingLeadingNumber(voiceSettingFold(o.Label))
			if !ok {
				all = false
				continue
			}
			d := v - float64(n)
			if d < 0 {
				d = -d
			}
			if d < 0.001 {
				return i
			}
			if best < 0 || d < bestD {
				best, bestD = i, d
			}
		}
		if all && best >= 0 {
			return best
		}
	}
	// Every word said is in one stop's label: "set the theme to ocean glass".
	wantWords := strings.Fields(want)
	found := -1
	for i, o := range opts {
		words := strings.Fields(voiceSettingFold(o.Label))
		all := true
		for _, w := range wantWords {
			if !voiceSettingWordIn(w, words) {
				all = false
				break
			}
		}
		if all {
			if found >= 0 {
				found = -2 // two stops: not this way
				break
			}
			found = i
		}
	}
	if found >= 0 {
		return found
	}
	if utf8Len(wantCompact) >= 4 {
		best, bestSim := -1, 0.0
		for i, o := range opts {
			sim := voiceSimilarity(wantCompact, strings.ReplaceAll(voiceSettingFold(o.Label), " ", ""))
			if sim > bestSim {
				best, bestSim = i, sim
			}
		}
		if bestSim >= 0.8 {
			return best
		}
	}
	return -1
}

// voiceSettingLeadingNumber is the first number in a folded label ("85
// percent" → 85, "1080p" → 1080).
func voiceSettingLeadingNumber(s string) (float64, bool) {
	start := strings.IndexFunc(s, unicode.IsDigit)
	if start < 0 {
		return 0, false
	}
	end := start
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.') {
		end++
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(s[start:end], "."), 64)
	return v, err == nil
}

// voiceSettingDurationRe reads "5m", "30s", "1m 30s", "5 minutes", "2 hours".
var voiceSettingDurationRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)\b`)

// voiceSettingDuration reads a folded length of time in seconds, with spoken
// numbers ("five minutes", "half an hour", "an hour") turned into digits first.
func voiceSettingDuration(s string) (int, bool) {
	words := strings.Fields(s)
	for i := 0; i < len(words); i++ {
		switch {
		case words[i] == "half" && i+2 < len(words) && (words[i+1] == "an" || words[i+1] == "a"):
			words[i], words[i+1] = "0.5", ""
		case (words[i] == "an" || words[i] == "a") && i+1 < len(words) &&
			strings.HasPrefix(words[i+1], "h") || (words[i] == "a" && i+1 < len(words) && strings.HasPrefix(words[i+1], "min")):
			words[i] = "1"
		default:
			if _, isWord := voiceNumberWords[words[i]]; isWord {
				if n, ok := voiceFindNumber(words[i:]); ok {
					words[i] = strconv.Itoa(n)
					// "twenty five": the second word was part of the number.
					if i+1 < len(words) {
						if u, ok := voiceNumberWords[words[i+1]]; ok && u > 0 && u < 10 && n%10 == u && n > 20 {
							words[i+1] = ""
						}
					}
				}
			}
		}
	}
	s = strings.Join(strings.Fields(strings.Join(words, " ")), " ")
	matches := voiceSettingDurationRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return 0, false
	}
	total := 0.0
	for _, m := range matches {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		switch m[2][0] {
		case 's':
			total += v
		case 'm':
			total += v * 60
		case 'h':
			total += v * 3600
		}
	}
	return int(total + 0.5), true
}

// voiceSettingPreview says what a settings sentence would do, for the voice
// shortcut editor. It resolves the setting but changes nothing.
func voiceSettingPreview(in voiceIntent) string {
	hits := voiceSettingRank(appConfig, voiceSettingCatalog(), in.Setting, in.SetOp)
	best, near := voiceSettingDecide(hits, in.SetOp)
	if best != nil && !voiceSettingSure(in, *best) {
		best = nil
	}
	if best == nil {
		if len(near) > 1 {
			names := make([]string, 0, len(near))
			for _, h := range near {
				names = append(names, h.Cand.Name)
			}
			return "Asks which setting: " + strings.Join(names, ", ")
		}
		return "Finds no setting called “" + in.Setting + "” yet"
	}
	name := best.Cand.Name
	switch in.SetOp {
	case voiceSetOpOn:
		return "Turns " + name + " on"
	case voiceSetOpOff:
		return "Turns " + name + " off"
	case voiceSetOpToggle:
		return "Switches " + name + " over"
	case voiceSetOpValue:
		return "Sets " + name + " to “" + in.SetTo + "”"
	case voiceSetOpUp:
		return "Turns " + name + " up a step"
	case voiceSetOpDown:
		return "Turns " + name + " down a step"
	case voiceSetOpMax:
		return "Sets " + name + " to the highest"
	case voiceSetOpMin:
		return "Sets " + name + " to the lowest"
	}
	return "Says what " + name + " is set to"
}
