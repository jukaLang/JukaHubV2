package main

// tts_settings.go — Settings → Voice & AI rows for Juka's spoken answers
// (voiceai_settings.go lists them first): whether Juka speaks, and for which
// answers; the voice; a test line that also says what keeps the voice quiet;
// and — once Groq has said the voice's terms are not accepted yet — the way to
// accept them.
//
// The voice is one of Groq's six, or the device's own when it has one (always
// on a Windows PC; on the Smart Pro once the offline voice has downloaded,
// which it does by itself after ffmpeg). With no Groq key and no voice chosen
// the device's voice is the default (ttsVoiceImplied), and Juka says once per
// run that a key brings Groq's voices. A Groq voice that cannot speak — no
// key, no internet, the terms, the day's allowance — hands over to the
// device's voice when there is one; with none the answer stays on screen
// only, and Juka says why once.

import (
	"runtime"
	"strings"

	"github.com/skip2/go-qrcode"
)

const (
	// ttsModeKey is the Custom setting behind "Juka's voice": ttsModeOff,
	// ttsModeVoice (the default) or ttsModeAll.
	ttsModeKey   = "jukaVoice"
	ttsModeOff   = "off"
	ttsModeVoice = "voice"
	ttsModeAll   = "all"
	// ttsVoiceKey is the Custom setting behind "Voice": one of ttsGroqVoices,
	// or ttsVoiceDevice for the device's own (the default is the first Groq
	// voice).
	ttsVoiceKey    = "jukaVoiceName"
	ttsVoiceDevice = "device"
	// ttsTestLine is what Hear Juka says.
	ttsTestLine = "Hi, I'm Juka. This is how I sound when I answer you."
)

// ttsModeOptions are the stops of "Juka's voice".
var ttsModeOptions = []setOption{
	{Value: ttsModeOff, Label: "Off"},
	{Value: ttsModeVoice, Label: "Voice assistant"},
	{Value: ttsModeAll, Label: "Voice assistant and Chat"},
}

// ttsSettingsRows is Juka's voice: on/off, the voice, a test line.
func ttsSettingsRows() []setRow {
	return []setRow{
		{
			ID: ttsModeKey, Label: "Juka's voice", Kind: setKindChoice,
			Detail:  ttsModeDetail,
			Options: func(*Config) []setOption { return ttsModeOptions },
			Current: func(c *Config) int { return ttsOptionIndex(ttsModeOptions, ttsModeOf(c)) },
			Choose: func(c *Config, value string) {
				ttsStore(c, ttsModeKey, value)
				if value == ttsModeOff {
					jukaSpeakStop()
				}
				setToast("Juka's voice: " + ttsOptionLabel(ttsModeOptions, value))
			},
		},
		{
			ID: ttsVoiceKey, Label: "Voice", Kind: setKindChoice,
			Detail:     ttsVoiceDetail,
			Options:    ttsVoiceOptions,
			Current:    func(c *Config) int { return ttsOptionIndex(ttsVoiceOptions(c), ttsVoiceOf(c)) },
			Sheet:      ttsVoiceOptions,
			SheetValue: ttsVoiceOf,
			SheetTitle: "Juka's voice",
			Choose: func(c *Config, value string) {
				ttsStore(c, ttsVoiceKey, value)
				setToast("Voice: " + ttsOptionLabel(ttsVoiceOptions(c), value))
			},
		},
		{
			ID: "jukaOfflineVoice", Label: "Download offline voice", Kind: setKindAction,
			Visible: func(*Config) bool { return ttsOfflineVoiceSupported() },
			Detail:  ttsOfflineVoiceDetail,
			Value:   ttsOfflineVoiceValue,
			Open:    ttsInstallOfflineVoice,
		},
		{
			ID: "jukaVoiceTest", Label: "Hear Juka", Kind: setKindAction,
			Detail: setStatic("Says a short line in the chosen voice — or what keeps it quiet"),
			Value:  ttsTestValue,
			Open: func(*Config) {
				// A fresh try: Groq is asked again even after a refusal, and a
				// voice installed since the last look is found.
				ttsGateClear()
				ttsLocalRecheck()
				ttsSpeakFrom(ttsFromTest, ttsKindVoice, ttsTestLine)
			},
		},
		{
			ID: "jukaVoiceTerms", Label: "Turn on Groq's voice", Kind: setKindAction,
			Visible: func(*Config) bool {
				terms, _, _ := ttsGateShown()
				return terms
			},
			Detail: func(*Config) string {
				if runtime.GOOS == "windows" {
					return "Opens Groq's page in your browser: accept the voice's terms there once, then press Hear Juka"
				}
				return "Shows a code for your phone: sign in to Groq, accept the voice's terms once, then press Hear Juka"
			},
			Open: ttsOpenTerms,
		},
	}
}

// ttsModeOf is the stored "Juka's voice". A switch-shaped value from a
// hand-edited config counts: false is off, anything else unknown the default.
func ttsModeOf(c *Config) string {
	if c == nil {
		return ttsModeVoice
	}
	switch v := strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, ttsModeKey))); v {
	case ttsModeOff, ttsModeVoice, ttsModeAll:
		return v
	case "false", "0", "no", "none":
		return ttsModeOff
	}
	return ttsModeVoice
}

// ttsModeAllows reports whether answers of kind are spoken.
func ttsModeAllows(c *Config, kind ttsKind) bool {
	switch ttsModeOf(c) {
	case ttsModeAll:
		return true
	case ttsModeVoice:
		return kind == ttsKindVoice
	}
	return false
}

// ttsVoiceOf is the voice in use: a Groq voice, or ttsVoiceDevice while the
// device has a working voice of its own. Anything else is the default.
func ttsVoiceOf(c *Config) string {
	v := ""
	if c != nil {
		v = strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, ttsVoiceKey)))
	}
	if v == ttsVoiceDevice {
		if _, ok := ttsLocal(); ok {
			return v
		}
		return ttsGroqVoices[0]
	}
	for _, g := range ttsGroqVoices {
		if v == g {
			return v
		}
	}
	if ttsVoiceImplied(c) {
		return ttsVoiceDevice
	}
	return ttsGroqVoices[0]
}

// ttsVoiceImplied reports whether the device's own voice is Juka's voice only
// because no voice was ever chosen and there is no Groq key: without a key
// Groq's voice cannot speak, so the offline voice (the Smart Pro's download,
// Windows' synthesiser) is the default. Adding a key brings Groq's voice back.
func ttsVoiceImplied(c *Config) bool {
	if c == nil || strings.TrimSpace(chatRawGroqKey(c)) != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, ttsVoiceKey))) {
	case "", "default":
	default:
		return false
	}
	_, ok := ttsLocal()
	return ok
}

// ttsVoiceGender is a Groq voice's, so Windows can pick a like voice when it
// speaks instead.
func ttsVoiceGender(voice string) string {
	switch voice {
	case "austin", "daniel", "troy":
		return "male"
	case "hannah", "autumn", "diana":
		return "female"
	}
	return ""
}

// ttsVoiceLabel is a voice's name as Settings shows it.
func ttsVoiceLabel(voice string) string {
	if voice == ttsVoiceDevice {
		return ttsUpperFirst(ttsDeviceWord())
	}
	return ttsUpperFirst(voice)
}

// ttsVoiceOptions are the voices to choose from: Groq's six, and the device's
// own when it has one.
func ttsVoiceOptions(*Config) []setOption {
	out := make([]setOption, 0, len(ttsGroqVoices)+1)
	for i, v := range ttsGroqVoices {
		note := "Groq · natural · needs internet"
		if i == 0 {
			note += " · the default with a Groq key"
		}
		out = append(out, setOption{Value: v, Label: ttsVoiceLabel(v), Note: note})
	}
	if lv, ok := ttsLocal(); ok {
		out = append(out, setOption{Value: ttsVoiceDevice, Label: ttsVoiceLabel(ttsVoiceDevice),
			Note: lv.name + " · works offline, no daily limit"})
	}
	return out
}

// ttsModeDetail is the line under "Juka's voice": what it does, and what keeps
// Groq's voice from speaking right now.
func ttsModeDetail(c *Config) string {
	if ttsModeOf(c) == ttsModeOff {
		return "Answers stay on the screen"
	}
	device := ttsDeviceWord()
	if ttsVoiceOf(c) == ttsVoiceDevice {
		if ttsVoiceImplied(c) {
			return "Reads answers aloud in " + device + "'s offline voice — add a Groq key (Settings → General) for Groq's voices"
		}
		return "Reads answers aloud in " + device + "'s own voice — offline, no daily limit"
	}
	_, hasLocal := ttsLocal()
	instead := ""
	if hasLocal {
		instead = " — " + device + "'s voice speaks meanwhile"
	}
	terms, resting, daily := ttsGateShown()
	switch {
	case strings.TrimSpace(chatRawGroqKey(c)) == "":
		return "Groq's voice needs your Groq key (Settings → General)" + instead
	case terms:
		return "Groq's voice needs a one-time OK on Groq's website: “Turn on Groq's voice” below" + instead
	case daily:
		return "Resting: today's free Groq voice allowance is used up" + instead
	case resting:
		return "Resting for a few minutes: Groq's free voice limit was reached" + instead
	case hasLocal:
		return "Reads answers aloud. Groq's free voice allows a few minutes a day, then " + device + "'s voice takes over"
	}
	return "Reads answers aloud. Groq's free voice allows a few minutes of speech a day"
}

// ttsVoiceDetail is the line under "Voice".
func ttsVoiceDetail(c *Config) string {
	if ttsVoiceOf(c) == ttsVoiceDevice {
		return "Works offline, as often as you like · A lists the voices"
	}
	return "Groq's voices sound natural and need internet · A lists them"
}

// ttsTestValue is Hear Juka's read-out while its line plays.
func ttsTestValue(*Config) string {
	if origin, busy := ttsCurrentOrigin(); busy && origin == ttsFromTest {
		return "Speaking…"
	}
	return ""
}

// ttsTestOutcome says how Hear Juka went: the voice heard, or what kept it
// quiet. Main thread.
func ttsTestOutcome(rep ttsReport) {
	device := ttsDeviceWord()
	kind := ToastKindWarning
	var msg string
	switch rep.fail {
	case ttsFailNone:
		switch {
		case rep.spoke && rep.local:
			msg, kind = "That was "+device+"'s voice", ToastKindInfo
		case rep.spoke:
			msg, kind = "That was "+ttsVoiceLabel(rep.voice)+", Groq's voice", ToastKindInfo
		case rep.localFailed:
			msg = ttsUpperFirst(device) + "'s voice failed — try again"
		default:
			msg = "Juka's voice made no sound — try again"
		}
	case ttsFailQuiet:
		msg = "Quiet Mode is on — Juka stays silent until it is off"
	case ttsFailNoSound:
		msg = "No sound device — Juka can't speak here"
	case ttsFailNoKey:
		switch {
		case rep.localFailed:
			msg = ttsUpperFirst(device) + "'s voice failed — try again"
		case rep.spoke && rep.local:
			// The offline voice is the default without a key: no "instead".
			ShowToast("That was "+device+"'s offline voice — add a Groq key in Settings → General for Groq's voices", ToastKindInfo)
			return
		default:
			msg = "Groq's voice needs your Groq key — add it in Settings → General"
		}
	case ttsFailOffline:
		msg = "Groq's voice needs internet"
	case ttsFailKey:
		msg = "Groq rejected the key — check it in Settings → General"
	case ttsFailTerms:
		msg = "Groq's voice needs a one-time OK — use “Turn on Groq's voice” below"
	case ttsFailLimit:
		msg = "Groq's voice is busy — try again in a minute"
		if rep.daily {
			msg = "Today's free Groq voice allowance is used up"
		}
	default:
		msg = "Groq's voice didn't answer — try again"
	}
	if rep.fail != ttsFailNone && rep.fail != ttsFailQuiet && rep.fail != ttsFailNoSound && rep.spoke && rep.local {
		msg += " — you heard " + device + "'s voice instead"
		kind = ToastKindInfo
	}
	ShowToast(msg, kind)
}

// ttsOpenTerms shows where to accept the voice's terms: Groq's page in the
// PC's browser, or a QR code for the phone (the handheld has no browser that
// Groq's console works in).
func ttsOpenTerms(*Config) {
	if runtime.GOOS == "windows" {
		if err := P().OpenURL(ttsGroqTermsURL); err == nil {
			setToast("Accept the terms on Groq's page, then press Hear Juka")
			return
		}
	}
	code, err := qrcode.New(ttsGroqTermsURL, qrcode.Low)
	if err != nil {
		logWarn("tts", "terms QR code: %v", err)
		setToast("On console.groq.com, open Orpheus in the playground and accept its terms")
		return
	}
	qrZoomOpenCardDetail(code.Bitmap(), "Turn on Groq's voice",
		"Scan it, sign in to Groq and accept the terms for Orpheus — then press Hear Juka")
}

// ttsStore saves one of the voice's settings.
func ttsStore(c *Config, key, value string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[key] = value
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// ttsOptionIndex is the stop holding value, or -1.
func ttsOptionIndex(opts []setOption, value string) int {
	for i, o := range opts {
		if o.Value == value {
			return i
		}
	}
	return -1
}

// ttsOptionLabel is the label of the stop holding value (value itself when
// there is none).
func ttsOptionLabel(opts []setOption, value string) string {
	if i := ttsOptionIndex(opts, value); i >= 0 {
		return opts[i].Label
	}
	return value
}

// ttsDeviceWord names the device whose own voice it is.
func ttsDeviceWord() string {
	if runtime.GOOS == "windows" {
		return "this PC"
	}
	return "this device"
}

// ttsUpperFirst capitalises a word's first letter.
func ttsUpperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
