package main

// voiceshortcuts_settings.go — Settings → Voice & AI rows for voice
// shortcuts: the words the user teaches the voice assistant and what each one
// opens (voiceai_settings.go).

// voiceShortcutSettingsRows opens the shortcut list. The detail line carries
// the words Home search finds the row by ("teach a word", "custom voice
// commands"): a Settings row's search keys are its id, its detail and its
// section.
func voiceShortcutSettingsRows() []setRow {
	return []setRow{{
		ID: voiceShortcutsKey, Label: "Voice shortcuts", Kind: setKindAction,
		Detail: setStatic("Teach your own words: custom voice commands that open any screen, game, emulator or setting"),
		Value:  voiceShortcutCountLabel,
		Open:   vscOpen,
	}}
}
