package main

// aitranslate_settings.go — Settings → Voice & AI rows for "Translate games"
// (aitranslate.go), listed by voiceai_settings.go under the game assistant.
//
// Off until turned on: it sends a picture of the game screen to an AI
// service, so the switch's line says exactly where it goes and that nothing
// is kept. The other rows show only while it is on: the language (JukaHub's
// own unless chosen), how the translation is shown (only what can work with
// the chosen service and language), the service, and the custom address,
// typed in a submit-only prompt (osPromptOpen) and shown by its host only, so
// a key in its query is not on screen.

// aiTranslateSettingsRows are the rows, in the order shown.
func aiTranslateSettingsRows() []setRow {
	return []setRow{
		{
			ID: aiTrKey, Label: "Translate games", Kind: setKindToggle,
			Detail: aiTrToggleDetail,
			Get:    aiTrEnabled,
			Set: func(c *Config, on bool) {
				setStoreBool(c, aiTrKey, on)
				setToast("Translate games: " + setOnOff(on))
			},
		},
		{
			ID: aiTrLangKey, Label: "Translate to", Kind: setKindChoice,
			Detail:  setStatic("The language the game's text is put into"),
			Visible: aiTrEnabled,
			Options: func(*Config) []setOption {
				out := []setOption{{Value: aiTrLangAuto, Label: "Same as JukaHub (" + uiLanguageNative(uiLanguageCode()) + ")"}}
				for _, l := range aiTrLangs {
					out = append(out, setOption{Value: l.Code, Label: uiLanguageNative(l.Code)})
				}
				return out
			},
			Current: func(c *Config) int {
				choice := aiTrLangChoice(c)
				if choice == aiTrLangAuto {
					return 0
				}
				for i, l := range aiTrLangs {
					if l.Code == choice {
						return i + 1
					}
				}
				return 0
			},
			Choose: func(c *Config, value string) {
				setStoreString(c, aiTrLangKey, value)
				setToast("Translate to: " + uiLanguageNative(aiTrTargetLang(c).Code))
			},
		},
		{
			ID: aiTrShowKey, Label: "Show the translation as", Kind: setKindChoice,
			Detail:  aiTrShowDetail,
			Visible: aiTrEnabled,
			Options: aiTrShowOptions,
			Current: func(c *Config) int {
				show := aiTrShowMode(c)
				for i, o := range aiTrShowOptions(c) {
					if o.Value == show {
						return i
					}
				}
				return 0
			},
			Value: func(c *Config) string { return aiTrShowLabel(aiTrShowMode(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, aiTrShowKey, value)
				setToast("Show the translation as: " + aiTrShowLabel(value))
			},
		},
		{
			ID: aiTrServiceKey, Label: "Translation service", Kind: setKindChoice,
			Detail:  aiTrServiceDetail,
			Visible: aiTrEnabled,
			Options: func(*Config) []setOption {
				return []setOption{
					{Value: aiTrServiceJukaHub, Label: "JukaHub (your Groq key)"},
					{Value: aiTrServiceCustom, Label: "My own service"},
				}
			},
			Current: func(c *Config) int {
				if aiTrCustom(c) {
					return 1
				}
				return 0
			},
			Choose: func(c *Config, value string) {
				if value != aiTrServiceCustom {
					value = aiTrServiceJukaHub
				}
				setStoreString(c, aiTrServiceKey, value)
				if value == aiTrServiceCustom && aiTrCustomURL(c) == "" {
					aiTrAskURL(c)
					return
				}
				if value == aiTrServiceCustom {
					setToast("Translation service: " + aiTrURLHost(aiTrCustomURL(c)))
					return
				}
				setToast("Translation service: JukaHub")
			},
		},
		{
			ID: aiTrURLKey, Label: "Service address", Kind: setKindAction,
			Detail:  setStatic("A RetroArch-compatible AI service, e.g. a vgtranslate on your PC: http://192.168.1.20:4404"),
			Visible: func(c *Config) bool { return aiTrEnabled(c) && aiTrCustom(c) },
			Value: func(c *Config) string {
				if host := aiTrURLHost(aiTrCustomURL(c)); host != "" {
					return host
				}
				return "Not set"
			},
			Open: aiTrAskURL,
		},
	}
}

// aiTrToggleDetail is the switch's line: what it does and, while on, where
// the picture goes.
func aiTrToggleDetail(c *Config) string {
	how := "In a RetroArch game: Select + L2 → Translate screen, or RetroArch's AI service hotkey"
	if !aiTrEnabled(c) {
		return "Translate a game's on-screen text with AI. " + how
	}
	if aiTrCustom(c) {
		host := aiTrURLHost(aiTrCustomURL(c))
		if host == "" {
			return "Set the service's address below"
		}
		return "A picture of the game screen goes to " + host + " when you ask; JukaHub keeps nothing. " + how
	}
	if !aiTrHaveKey(c) {
		return "Needs your Groq API key (Settings → General → Groq API key)"
	}
	return "A picture of the game screen goes to Groq with your key when you ask; nothing is stored. " + how
}

// aiTrShowOptions are the ways the translation can be shown with the chosen
// service and language.
func aiTrShowOptions(c *Config) []setOption {
	out := []setOption{{Value: aiTrShowText, Label: aiTrShowLabel(aiTrShowText)}}
	if aiTrSpeechPossible(c) {
		out = append(out, setOption{Value: aiTrShowSpeech, Label: aiTrShowLabel(aiTrShowSpeech)})
	}
	if aiTrCustom(c) {
		out = append(out, setOption{Value: aiTrShowImage, Label: aiTrShowLabel(aiTrShowImage)})
	}
	return out
}

// aiTrShowLabel is a mode's name on the row.
func aiTrShowLabel(show string) string {
	switch show {
	case aiTrShowSpeech:
		return "Read aloud"
	case aiTrShowImage:
		return "Picture over the game"
	}
	return "Text on screen"
}

// aiTrShowDetail says what the chosen mode does and, for JukaHub's service,
// why reading aloud may be missing.
func aiTrShowDetail(c *Config) string {
	switch aiTrShowMode(c) {
	case aiTrShowSpeech:
		if aiTrCustom(c) {
			return "Your service sends the speech; RetroArch plays it as the game runs on"
		}
		return "The handheld's own voice reads the English translation as the game runs on"
	case aiTrShowImage:
		return "Your service draws the translation over the picture; the game waits until you translate again"
	}
	if !aiTrCustom(c) && aiTrTargetLang(c).Code == "en" && !aiTrSpeechPossible(c) {
		return "Like subtitles, over the running game. Read aloud needs the handheld's voice (Voice above)"
	}
	return "Like subtitles, over the running game (RetroArch 1.17 or newer)"
}

// aiTrServiceDetail says which service answers.
func aiTrServiceDetail(c *Config) string {
	if aiTrCustom(c) {
		return "RetroArch sends the screen straight to your service; picture mode needs one that draws"
	}
	return "JukaHub reads the screen with the AI it already uses: Groq, with your key"
}

// aiTrAskURL opens the keyboard for the custom service's address. Main
// thread.
func aiTrAskURL(c *Config) {
	osPromptOpen("Translation service address", "http://192.168.1.20:4404", "Save", aiTrCustomURL(c), aiTrURLMax, func(typed string) {
		if typed == "" {
			setStoreString(c, aiTrURLKey, "")
			setToast("Service address removed")
			return
		}
		clean, problem := aiTrCheckURL(typed)
		if problem != "" {
			ShowToast(problem, ToastKindWarning)
			return
		}
		setStoreString(c, aiTrURLKey, clean)
		setToast("Service address saved: " + aiTrURLHost(clean))
	})
}
