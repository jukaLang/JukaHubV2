package main

// voiceai_settings.go — Settings → Voice & AI: Juka's spoken answers, voice
// shortcuts, the in-game assistant and voice typing. Each feature keeps its
// rows in its own file (tts_settings.go, voiceshortcuts_settings.go,
// gameassist_settings.go, vkvoice_settings.go); this section only puts them
// on one page, in the order they are listed here. The Groq key for cloud
// features stays in Settings → General ("Groq API key", groqkey_sheet.go).

// voiceAISettingsSection is the rail section's id, used by deep links and search.
const voiceAISettingsSection = "voiceai"

// jukaSystemPrompt is the system prompt every question to Juka starts with
// (Chat, the voice orb, pictures): the fixed one, plus what Juka should know
// about the game being played when the game assistant is on
// (gameAssistPromptSuffix; empty otherwise).
func jukaSystemPrompt() string {
	return groqSystemPrompt + gameAssistPromptSuffix()
}

// voiceAISettingsRows gathers the section's rows in the order they are shown.
func voiceAISettingsRows() []setRow {
	var rows []setRow
	rows = append(rows, ttsSettingsRows()...)
	rows = append(rows, offlineAISettingsRows()...)
	rows = append(rows, voiceScreenSettingsRows()...) // "Let Juka see the screen" (voicescreen.go)
	rows = append(rows, voiceShortcutSettingsRows()...)
	rows = append(rows, gameAssistSettingsRows()...)
	rows = append(rows, hjSettingsRows()...) // "Hey Juka" (wakeword.go)
	rows = append(rows, vkVoiceSettingsRows()...)
	return rows
}
