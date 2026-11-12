package main

// onboarding_lang.go — the first page of the first-run tour: "Choose your
// language".
//
// The interface language itself lives in i18n.go (the catalogue, uiT, the
// Settings → General → Language row). This page only asks the question on the
// first start, before the user has read a word of English:
//
//   - The list shows each language in itself ("Deutsch", "Русский"), drawn raw so
//     no catalogue ever renames it.
//   - The highlight starts on the device's own language when it can be read —
//     TrimUI's system.json, the LANG family of variables, Windows' user locale
//     (uiSystemLocale, per OS) — and on English otherwise.
//   - Moving the highlight switches the interface at once, so the rest of the
//     card (step counter, buttons, footer) and every later tour page already
//     read in the language being considered. The title is a small table of its
//     own, so it is right even before a catalogue has the phrase.
//   - The tour's own sentences are translated in i18n/<code>.tour.json, which
//     i18n.go merges into the language (uiMergeCatalogParts): the main
//     catalogues were extracted before the tour pages had translations.
//   - Leaving the page in any way (Next, Skip, a click on Next) saves what is
//     on screen: what the user saw is what they keep, and Settings changes it
//     later. A replayed tour starts on the saved language.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// onbLang is the language page's state: the highlighted row (an index into
// uiLanguages) and the rows' hit rects from the last frame.
var onbLang struct {
	cursor int
	hits   []sdl.Rect
}

// onbLangTitles is "Choose your language" in each offered language. It is not
// a catalogue lookup so the page's own title never depends on a translation
// having been written; a language missing here falls back to English.
var onbLangTitles = map[string]string{
	"en":    "Choose your language",
	"es":    "Elige tu idioma",
	"pt-BR": "Escolha seu idioma",
	"fr":    "Choisissez votre langue",
	"de":    "Wähle deine Sprache",
	"it":    "Scegli la lingua",
	"ru":    "Выберите язык",
}

// onbLangWord is the one-line "Language" in every offered language, under the
// title, so someone who reads none of the current one still sees their word.
func onbLangWord() string {
	words := map[string]string{
		"en": "Language", "es": "Idioma", "pt-BR": "Idioma", "fr": "Langue",
		"de": "Sprache", "it": "Lingua", "ru": "Язык",
	}
	seen := map[string]bool{}
	parts := make([]string, 0, len(uiLanguages))
	for _, l := range uiLanguages {
		w, ok := words[l.Code]
		if !ok || seen[w] {
			continue
		}
		seen[w] = true
		parts = append(parts, w)
	}
	return strings.Join(parts, " · ")
}

// onbLangTitle is the page title in the highlighted language.
func onbLangTitle() string {
	if t, ok := onbLangTitles[uiLanguageCode()]; ok {
		return t
	}
	return onbLangTitles[uiLanguageDefault]
}

// onboardingLangStepIs reports whether the tour is on the language page.
func onboardingLangStepIs() bool {
	if onboarding.step < 0 || onboarding.step >= len(onboardingSteps) {
		return false
	}
	return onboardingSteps[onboarding.step].kind == "language"
}

// onboardingLangReset puts the highlight where the tour should start: the
// saved language when there is one (a replay), else the device's language.
// The choice is applied at once so the page is drawn in it.
func onboardingLangReset(config *Config) {
	code := ""
	if config != nil {
		if v, ok := customValue(config.Variables.Custom, uiLanguageKey); ok {
			code = uiNormalizeLanguage(coerceString(v))
		}
	}
	if code == "" {
		code = uiDetectSystemLanguage()
	}
	onbLang.cursor = 0
	for i, l := range uiLanguages {
		if l.Code == code {
			onbLang.cursor = i
			break
		}
	}
	onbLang.hits = onbLang.hits[:0]
	uiSetLanguage(uiLanguages[onbLang.cursor].Code)
}

// onboardingLangSync puts the highlight on the language in force, for a page
// shown again with Back (Settings may have changed it meanwhile).
func onboardingLangSync() {
	code := uiLanguageCode()
	for i, l := range uiLanguages {
		if l.Code == code {
			onbLang.cursor = i
			return
		}
	}
}

// onboardingLangMove walks the list and switches the interface with it.
func onboardingLangMove(delta int) {
	next := onbLang.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(uiLanguages) {
		next = len(uiLanguages) - 1
	}
	if next == onbLang.cursor {
		return
	}
	onbLang.cursor = next
	uiSetLanguage(uiLanguages[next].Code)
	PlayNavSound()
}

// onboardingLangClick highlights the row under a pointer; Next confirms it.
func onboardingLangClick(mx, my int32) bool {
	for i, r := range onbLang.hits {
		if !pointInRect(mx, my, r) {
			continue
		}
		if i != onbLang.cursor {
			onboardingLangMove(i - onbLang.cursor)
		}
		return true
	}
	return false
}

// onboardingLangStore writes the highlighted language into the config's Custom
// map. The caller saves (onboardingFinish already does; onboardingAdvance saves
// through onboardingLangCommit). Written directly rather than through
// setStoreString, the same way the tour records onboarding_done.
func onboardingLangStore(config *Config) {
	if config == nil || onbLang.cursor < 0 || onbLang.cursor >= len(uiLanguages) {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[uiLanguageKey] = uiLanguages[onbLang.cursor].Code
}

// onboardingLangCommit stores and saves the language when the page is left
// with Next.
func onboardingLangCommit(config *Config) {
	if config == nil {
		return
	}
	onboardingLangStore(config)
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// onboardingRenderLanguage draws the language list in the card's body area.
func onboardingRenderLanguage(renderer *sdl.Renderer, config *Config, cy, cardH, textX, textW, bodyY int32, bodyFont, smallFont *ttf.Font, accent sdl.Color) {
	onbLang.hits = onbLang.hits[:0]

	// Fit, in order of preference: one column under the "Language · Idioma"
	// line; one column without that line; two columns. A big text scale or a
	// short canvas must not stack the rows on top of each other.
	const gap = int32(8)
	n := int32(len(uiLanguages))
	bottom := cy + cardH - 106
	font := bodyFont
	if smallFont.Height() < bodyFont.Height() && (bottom-bodyY)/n < int32(bodyFont.Height())+10 {
		font = smallFont
	}
	need := int32(font.Height()) + 10
	wordH := int32(smallFont.Height()) + 8
	top := bodyY + wordH
	showWord := (bottom-top)/n >= need
	if !showWord {
		top = bodyY - 6
	}
	cols := int32(1)
	if (bottom-top)/n < need {
		cols = 2
	}
	perCol := (n + cols - 1) / cols
	pitch := int32(56)
	if fit := (bottom - top) / perCol; fit < pitch {
		pitch = fit
	}
	rowH := pitch - gap
	if floor := min(int32(24), pitch); rowH < floor {
		rowH = floor
	}
	colW := (textW + 20 - gap*(cols-1)) / cols

	if showWord {
		// Every language's word for "Language", raw: it must not be translated.
		word := ellipsize(smallFont, onbLangWord(), textW)
		renderTextRaw(renderer, config, smallFont, word, ColorTextTertiary(), textX, bodyY-6)
	}

	for i, l := range uiLanguages {
		col, row := int32(i)/perCol, int32(i)%perCol
		rect := sdl.Rect{X: textX - 10 + col*(colW+gap), Y: top + row*pitch, W: colW, H: rowH}
		onbLang.hits = append(onbLang.hits, rect)
		selected := i == onbLang.cursor
		UIListRow(renderer, rect, RadiusMD, selected, false)

		// The language in force wears the filled tick the checklist uses.
		dotX := rect.X + 24
		dotY := rect.Y + rowH/2
		if selected {
			fillCircle(renderer, dotX, dotY, 7, WithAlpha(accent, 235))
			setDrawColor(renderer, ColorTopBar)
			renderer.DrawLine(dotX-3, dotY, dotX-1, dotY+3)
			renderer.DrawLine(dotX-1, dotY+3, dotX+4, dotY-3)
		} else {
			strokeCircle(renderer, dotX, dotY, 7, WithAlpha(ColorTextTertiary(), 150))
		}

		color := ColorTextSecondary()
		if selected {
			color = ColorTextPrimary()
		}
		labelX := dotX + 18
		label := ellipsize(font, l.Native, rect.X+rect.W-14-labelX)
		renderTextRaw(renderer, config, font, label, color, labelX, rect.Y+(rowH-int32(font.Height()))/2)
	}
}

// ── The device's own language ─────────────────────────────────────────────

// uiDetectSystemLanguage is the offered language closest to the device's own,
// or English. TrimUI's settings file comes first (the handheld has no LANG),
// then the OS's user locale (Windows; a LANG there is usually Git Bash's
// en_US, not the user's), then the POSIX variables in glibc's order —
// LANGUAGE counts only when a real locale is set.
func uiDetectSystemLanguage() string {
	if code := uiTrimuiSystemLanguage(); code != "" {
		return code
	}
	if code := uiLanguageFromLocale(uiSystemLocale()); code != "" {
		return code
	}
	realLocale := false
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" && !uiLocaleIsPlain(v) {
			realLocale = true
		}
	}
	if realLocale {
		if code := uiLanguageFromLocale(os.Getenv("LANGUAGE")); code != "" {
			return code
		}
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if code := uiLanguageFromLocale(os.Getenv(name)); code != "" {
			return code
		}
	}
	return uiLanguageDefault
}

// uiLocaleIsPlain reports the "C" and "POSIX" locales ("C.UTF-8" too), which
// name no language.
func uiLocaleIsPlain(s string) bool {
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	return strings.EqualFold(s, "C") || strings.EqualFold(s, "POSIX")
}

// uiLanguageFromLocale maps a locale name ("de_DE.UTF-8", "pt-BR",
// "ru_RU@euro", "fr:en" from LANGUAGE) onto an offered code, or "" when it
// names none of them. "C" and "POSIX" say nothing about a language.
func uiLanguageFromLocale(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "_", "-")
	if s == "" || strings.EqualFold(s, "C") || strings.EqualFold(s, "POSIX") {
		return ""
	}
	for _, l := range uiLanguages {
		if strings.EqualFold(s, l.Code) {
			return l.Code
		}
	}
	primary := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		primary = s[:i]
	}
	if strings.EqualFold(primary, "pt") {
		return "pt-BR"
	}
	for _, l := range uiLanguages {
		if strings.EqualFold(primary, l.Code) {
			return l.Code
		}
	}
	return ""
}

// uiTrimuiSystemLanguage reads the stock firmware's "language" from
// system.json. Its exact form differs between firmware builds (a code, a
// "lang/xx.lang" path, an English name), so all three are accepted and
// anything else is ignored.
func uiTrimuiSystemLanguage() string {
	if !IsTrimuiDevice() {
		return ""
	}
	data, err := os.ReadFile(trimuiSystemJSONPath)
	if err != nil {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	v, ok := m["language"].(string)
	if !ok {
		return ""
	}
	v = filepath.Base(strings.TrimSpace(v))
	v = strings.TrimSuffix(v, filepath.Ext(v))
	switch strings.ToLower(v) {
	case "english":
		return "en"
	case "spanish", "español", "espanol":
		return "es"
	case "portuguese", "português", "portugues":
		return "pt-BR"
	case "french", "français", "francais":
		return "fr"
	case "german", "deutsch":
		return "de"
	case "italian", "italiano":
		return "it"
	case "russian", "русский":
		return "ru"
	}
	return uiLanguageFromLocale(v)
}
