package main

// vkvoice_settings.go — Settings → Voice & AI rows for voice typing on the
// on-screen keyboard (voiceai_settings.go): the switch that starts it as a
// message field's keyboard opens (vkvoice_button.go), which button it is on,
// and the way to the keyboard's own voice settings (the speech language, the
// microphone). Voice typing itself has no on/off switch.

// vkVoiceAutoKey is Custom's key for "Start voice typing when the keyboard
// opens".
const vkVoiceAutoKey = "vkVoiceAuto"

var sttModeOptions = []setOption{
	{Value: sttModeAuto, Label: "Automatic", Note: "Use Groq when available; switch to offline speech if it fails"},
	{Value: sttModeOffline, Label: "Offline on device", Note: "Keep microphone recordings on this Smart Pro"},
	{Value: sttModeGroq, Label: "Groq", Note: "Use Groq only; needs a key and Wi-Fi"},
}

// vkVoiceAutoOn reports that switch; off unless it was turned on.
func vkVoiceAutoOn(config *Config) bool {
	if config == nil {
		return false
	}
	return customBool(config.Variables.Custom, vkVoiceAutoKey, false)
}

// vkVoiceSettingsRows is voice typing's rows.
func vkVoiceSettingsRows() []setRow {
	return []setRow{
		{
			ID: sttModeKey, Label: "Speech recognition", Kind: setKindChoice,
			Visible: func(*Config) bool { return sttSupported() },
			Detail: func(*Config) string {
				return "Choose how voice typing, commands and shortcuts hear you"
			},
			Options:    func(*Config) []setOption { return sttModeOptions },
			Current:    func(c *Config) int { return ttsOptionIndex(sttModeOptions, sttModeOf(c)) },
			Sheet:      func(*Config) []setOption { return sttModeOptions },
			SheetValue: sttModeOf,
			SheetTitle: "Speech recognition",
			Choose: func(c *Config, value string) {
				ttsStore(c, sttModeKey, value)
				setToast("Speech recognition: " + ttsOptionLabel(sttModeOptions, value))
			},
		},
		{
			ID: "offlineSpeech", Label: "Install offline speech", Kind: setKindAction,
			Visible: func(*Config) bool { return sttSupported() },
			Value: func(*Config) string {
				switch {
				case sttLocalReady():
					return "Ready"
				case relPackRunning(sttPackKey):
					return "Installing"
				default:
					return "Install"
				}
			},
			Detail: func(*Config) string {
				if sttLocalReady() {
					return "Included in the latest package · works without a key or Wi-Fi"
				}
				return "One-time download · about 78 MB · then works without Wi-Fi"
			},
			Open: sttInstallOffline,
		},
		{
			ID: vkVoiceAutoKey, Label: "Start voice typing when the keyboard opens", Kind: setKindToggle,
			Detail: func(*Config) string {
				if !sttCanListen() {
					return "Set up speech recognition above first"
				}
				return "Chat messages start listening at once; " + vkVoiceButtonName() + " stops and sends"
			},
			Get: vkVoiceAutoOn,
			Set: func(c *Config, on bool) {
				if c == nil {
					return
				}
				if c.Variables.Custom == nil {
					c.Variables.Custom = map[string]interface{}{}
				}
				c.Variables.Custom[vkVoiceAutoKey] = on
				saveUserConfigDebounced(userConfigFromVariables(c))
				setToast("Voice typing when the keyboard opens: " + setOnOff(on))
			},
		},
		{
			ID: "vkVoiceButton", Label: "Voice typing button", Kind: setKindInfo,
			Detail: setStatic("With the keyboard up: press to talk, press again to send"),
			Value:  func(*Config) string { return vkVoiceButtonName() },
		},
		{
			ID: "vkVoiceKeyboard", Label: "Speech language and microphone", Kind: setKindAction,
			Detail: setStatic("Keyboard settings: the language you speak, the microphone"),
			Open:   vkOpenSettings,
		},
	}
}
