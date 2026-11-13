package main

// vkvoice_settings.go — Settings → Voice & AI rows for voice typing on the
// on-screen keyboard (voiceai_settings.go): the switch that starts it as a
// message field's keyboard opens (vkvoice_button.go), which button it is on,
// and the way to the keyboard's own voice settings (the speech language, the
// microphone). Voice typing itself has no on/off switch.

// vkVoiceAutoKey is Custom's key for "Start voice typing when the keyboard
// opens".
const vkVoiceAutoKey = "vkVoiceAuto"

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
			ID: vkVoiceAutoKey, Label: "Start voice typing when the keyboard opens", Kind: setKindToggle,
			Detail: func(*Config) string {
				if !sttCanListen() {
					return "Needs a Groq key and Wi-Fi, or offline speech on the card"
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
