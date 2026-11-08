package main

// setlang.go — the subtitle languages Settings offers.
//
// The player fetches one subtitle track, in the language the subtitleLanguage
// setting names (videosubs.go). It used to be a free-text box whose placeholder
// ran out of the field, and a typo there just meant "no subtitles". The row is
// now a carousel over the languages people actually pick, plus a list of the
// rest, so every value it can store is one yt-dlp understands.
//
// Names are shown in their own language when the bundled Inter can draw them
// (Latin, Cyrillic, Greek, Vietnamese). Scripts it cannot draw — Japanese,
// Chinese, Korean, Arabic, Hebrew, Hindi, Thai, Persian — use the English name
// with the code, "Japanese (ja)", rather than a row of empty boxes; the test
// checks every native name against Inter's character map.

import "strings"

// setLanguage is one subtitle language: the code yt-dlp asks YouTube for, the
// name shown on the row, and the English name the list adds under it.
type setLanguage struct {
	Code    string
	Native  string
	English string
}

// setSubtitleDefaultLabel is the carousel's first stop: no code stored, which
// the player reads as English.
const setSubtitleDefaultLabel = "Default (English)"

// setSubtitleLanguages is the full list, the order the "All languages" sheet
// shows: the quick stops first, then the rest alphabetically by English name.
var setSubtitleLanguages = []setLanguage{
	{"en", "English", "English"},
	{"es", "Español", "Spanish"},
	{"fr", "Français", "French"},
	{"de", "Deutsch", "German"},
	{"ru", "Русский", "Russian"},
	{"uk", "Українська", "Ukrainian"},
	{"pt", "Português", "Portuguese"},
	{"it", "Italiano", "Italian"},
	{"ja", "Japanese (ja)", "Japanese"},
	{"ar", "Arabic (ar)", "Arabic"},
	{"bg", "Български", "Bulgarian"},
	{"ca", "Català", "Catalan"},
	{"zh-Hans", "Chinese, Simplified (zh-Hans)", "Chinese (Simplified)"},
	{"zh-Hant", "Chinese, Traditional (zh-Hant)", "Chinese (Traditional)"},
	{"hr", "Hrvatski", "Croatian"},
	{"cs", "Čeština", "Czech"},
	{"da", "Dansk", "Danish"},
	{"nl", "Nederlands", "Dutch"},
	{"fil", "Filipino", "Filipino"},
	{"fi", "Suomi", "Finnish"},
	{"el", "Ελληνικά", "Greek"},
	{"iw", "Hebrew (iw)", "Hebrew"},
	{"hi", "Hindi (hi)", "Hindi"},
	{"hu", "Magyar", "Hungarian"},
	{"id", "Bahasa Indonesia", "Indonesian"},
	{"ko", "Korean (ko)", "Korean"},
	{"ms", "Bahasa Melayu", "Malay"},
	{"no", "Norsk", "Norwegian"},
	{"fa", "Persian (fa)", "Persian"},
	{"pl", "Polski", "Polish"},
	{"pt-BR", "Português (Brasil)", "Portuguese (Brazil)"},
	{"ro", "Română", "Romanian"},
	{"sr", "Српски", "Serbian"},
	{"sk", "Slovenčina", "Slovak"},
	{"sv", "Svenska", "Swedish"},
	{"th", "Thai (th)", "Thai"},
	{"tr", "Türkçe", "Turkish"},
	{"vi", "Tiếng Việt", "Vietnamese"},
}

// setSubtitleQuickCount is how many of the list's first entries the carousel
// steps through; the rest are one press away in the sheet.
const setSubtitleQuickCount = 9

// setSubtitleQuickOptions is the carousel: Default, then the quick languages.
func setSubtitleQuickOptions() []setOption {
	out := make([]setOption, 0, setSubtitleQuickCount+1)
	out = append(out, setOption{Value: "", Label: setSubtitleDefaultLabel})
	for _, l := range setSubtitleLanguages[:setSubtitleQuickCount] {
		out = append(out, setOption{Value: l.Code, Label: l.Native})
	}
	return out
}

// setSubtitleAllOptions is the sheet: Default and every language, each with its
// English name and code as the secondary line.
func setSubtitleAllOptions() []setOption {
	out := make([]setOption, 0, len(setSubtitleLanguages)+1)
	out = append(out, setOption{Value: "", Label: setSubtitleDefaultLabel, Note: "No code · the player asks for English"})
	for _, l := range setSubtitleLanguages {
		note := l.English + " · " + l.Code
		if strings.HasSuffix(l.Native, ")") {
			// "Japanese (ja)" already carries both.
			note = l.Code
		}
		out = append(out, setOption{Value: l.Code, Label: l.Native, Note: note})
	}
	return out
}

// setSubtitleLabel names a stored code: its native name, the default label for
// an empty value, or the raw code for one a hand-edited config holds.
func setSubtitleLabel(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return setSubtitleDefaultLabel
	}
	for _, l := range setSubtitleLanguages {
		if strings.EqualFold(l.Code, code) {
			return l.Native
		}
	}
	return code
}
