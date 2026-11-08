package main

// vklayouts.go — the on-screen keyboard's languages.
//
// Each layout is the letter grid of one language's phone keyboard (QWERTY,
// QWERTZ, AZERTY, ЙЦУКЕН, Greek) plus the accents a long press offers on its
// letters. Only scripts the bundled Inter font draws are listed: Latin,
// Cyrillic and Greek. The user picks which layouts are enabled and in what
// order in Keyboard settings; the globe key walks through them.
//
// Settings live in the config's Custom map, written through the same save path
// as every other setting (syncVariableOverrides):
//
//	vk_layouts         enabled layout ids, comma-separated, in order ("en-US,de")
//	vk_layout          the layout in use
//	vk_suggestions     word suggestions        (unset = on)
//	vk_learn_words     learn new words          (unset = on)
//	vk_autocap         capitalise sentences     (unset = on)
//	vk_sounds          key sounds               (unset = on)
//	vk_voice_language  "layout" (unset) or "auto": the language voice typing asks for

import (
	"strings"
	"unicode"
)

// vkLayout is one language's keyboard.
type vkLayout struct {
	ID     string // stable id stored in the config ("en-US", "de")
	Name   string // English name ("German")
	Native string // the name in its own language ("Deutsch"), on the space bar
	Short  string // chip label ("DE")
	Lang   string // ISO 639-1 code: Whisper's language hint and learned words
	// Rows are the three letter rows, lower case, space-separated.
	Rows [3]string
	// Currency replaces "$" on the symbols layer ("" keeps "$").
	Currency string
	// Accents lists the long-press variants per letter, the language's own
	// first, space-separated. Latin layouts also get the shared set after them.
	Accents map[string]string
	// Turkish casing: i <-> İ and ı <-> I.
	Turkish bool
	// NonLatin layouts get no shared Latin accents.
	NonLatin bool
}

const (
	vkKeyLayouts     = "vk_layouts"
	vkKeyLayout      = "vk_layout"
	vkKeySuggestions = "vk_suggestions"
	vkKeyLearn       = "vk_learn_words"
	vkKeyAutoCap     = "vk_autocap"
	vkKeySounds      = "vk_sounds"
	vkKeyVoiceLang   = "vk_voice_language"

	// vkDefaultLayoutID is the layout a fresh install starts on.
	vkDefaultLayoutID = "en-US"
	// vkMaxEnabledLayouts keeps the globe cycle short enough to be useful.
	vkMaxEnabledLayouts = 8
)

const (
	vkRowQwerty1 = "q w e r t y u i o p"
	vkRowQwerty2 = "a s d f g h j k l"
	vkRowQwerty3 = "z x c v b n m"
)

// vkLayouts is every layout the keyboard offers, in the order Keyboard
// settings lists them.
var vkLayouts = []vkLayout{
	{ID: "en-US", Name: "English (US)", Native: "English (US)", Short: "EN", Lang: "en",
		Rows: [3]string{vkRowQwerty1, vkRowQwerty2, vkRowQwerty3}},
	{ID: "en-GB", Name: "English (UK)", Native: "English (UK)", Short: "UK", Lang: "en",
		Rows: [3]string{vkRowQwerty1, vkRowQwerty2, vkRowQwerty3}, Currency: "£"},
	{ID: "de", Name: "German", Native: "Deutsch", Short: "DE", Lang: "de",
		Rows:     [3]string{"q w e r t z u i o p ü", "a s d f g h j k l ö ä", "y x c v b n m"},
		Currency: "€",
		Accents:  map[string]string{"s": "ß", "a": "ä", "o": "ö", "u": "ü"}},
	{ID: "fr", Name: "French", Native: "Français", Short: "FR", Lang: "fr",
		Rows:     [3]string{"a z e r t y u i o p", "q s d f g h j k l m", "w x c v b n '"},
		Currency: "€",
		Accents: map[string]string{"e": "é è ê ë", "a": "à â æ", "c": "ç", "u": "ù û ü",
			"i": "î ï", "o": "ô œ", "y": "ÿ"}},
	{ID: "es", Name: "Spanish", Native: "Español", Short: "ES", Lang: "es",
		Rows:     [3]string{vkRowQwerty1, "a s d f g h j k l ñ", vkRowQwerty3},
		Currency: "€",
		Accents: map[string]string{"a": "á", "e": "é", "i": "í", "o": "ó", "u": "ú ü",
			"n": "ñ"}},
	{ID: "pt", Name: "Portuguese", Native: "Português", Short: "PT", Lang: "pt",
		Rows:     [3]string{vkRowQwerty1, "a s d f g h j k l ç", vkRowQwerty3},
		Currency: "€",
		Accents: map[string]string{"a": "á à â ã", "e": "é ê", "i": "í", "o": "ó ô õ",
			"u": "ú ü", "c": "ç"}},
	{ID: "it", Name: "Italian", Native: "Italiano", Short: "IT", Lang: "it",
		Rows:     [3]string{vkRowQwerty1, vkRowQwerty2, vkRowQwerty3},
		Currency: "€",
		Accents:  map[string]string{"a": "à", "e": "è é", "i": "ì í", "o": "ò ó", "u": "ù ú"}},
	{ID: "nl", Name: "Dutch", Native: "Nederlands", Short: "NL", Lang: "nl",
		Rows:     [3]string{vkRowQwerty1, vkRowQwerty2, vkRowQwerty3},
		Currency: "€",
		Accents:  map[string]string{"e": "é ë è ê", "i": "ï í", "o": "ö ó", "u": "ü ú", "a": "á ä"}},
	{ID: "sv", Name: "Swedish", Native: "Svenska", Short: "SV", Lang: "sv",
		Rows:    [3]string{"q w e r t y u i o p å", "a s d f g h j k l ö ä", vkRowQwerty3},
		Accents: map[string]string{"a": "å ä", "o": "ö", "e": "é"}},
	{ID: "nb", Name: "Norwegian", Native: "Norsk", Short: "NO", Lang: "no",
		Rows:    [3]string{"q w e r t y u i o p å", "a s d f g h j k l ø æ", vkRowQwerty3},
		Accents: map[string]string{"a": "å æ", "o": "ø", "e": "é"}},
	{ID: "da", Name: "Danish", Native: "Dansk", Short: "DA", Lang: "da",
		Rows:    [3]string{"q w e r t y u i o p å", "a s d f g h j k l æ ø", vkRowQwerty3},
		Accents: map[string]string{"a": "å æ", "o": "ø", "e": "é"}},
	{ID: "fi", Name: "Finnish", Native: "Suomi", Short: "FI", Lang: "fi",
		Rows:     [3]string{"q w e r t y u i o p å", "a s d f g h j k l ö ä", vkRowQwerty3},
		Currency: "€",
		Accents:  map[string]string{"a": "ä å", "o": "ö", "s": "š", "z": "ž"}},
	{ID: "pl", Name: "Polish", Native: "Polski", Short: "PL", Lang: "pl",
		Rows: [3]string{vkRowQwerty1, vkRowQwerty2, vkRowQwerty3},
		Accents: map[string]string{"a": "ą", "c": "ć", "e": "ę", "l": "ł", "n": "ń", "o": "ó",
			"s": "ś", "z": "ż ź"}},
	{ID: "cs", Name: "Czech", Native: "Čeština", Short: "CS", Lang: "cs",
		Rows: [3]string{"q w e r t z u i o p", vkRowQwerty2, "y x c v b n m"},
		Accents: map[string]string{"a": "á", "c": "č", "d": "ď", "e": "é ě", "i": "í", "n": "ň",
			"o": "ó", "r": "ř", "s": "š", "t": "ť", "u": "ú ů", "y": "ý", "z": "ž"}},
	{ID: "tr", Name: "Turkish", Native: "Türkçe", Short: "TR", Lang: "tr",
		Rows:     [3]string{"q w e r t y u ı o p ğ ü", "a s d f g h j k l ş i", "z x c v b n m ö ç"},
		Currency: "₺",
		Accents:  map[string]string{"g": "ğ", "s": "ş", "c": "ç", "o": "ö", "u": "ü", "a": "â", "i": "î"},
		Turkish:  true},
	{ID: "ru", Name: "Russian", Native: "Русский", Short: "RU", Lang: "ru",
		Rows:     [3]string{"й ц у к е н г ш щ з х", "ф ы в а п р о л д ж э", "я ч с м и т ь б ю"},
		Currency: "₽",
		Accents:  map[string]string{"е": "ё", "ь": "ъ"},
		NonLatin: true},
	{ID: "uk", Name: "Ukrainian", Native: "Українська", Short: "UA", Lang: "uk",
		Rows:     [3]string{"й ц у к е н г ш щ з х ї", "ф і в а п р о л д ж є", "я ч с м и т ь б ю"},
		Currency: "₴",
		Accents:  map[string]string{"г": "ґ", "і": "ї", "ь": "'"},
		NonLatin: true},
	{ID: "be", Name: "Belarusian", Native: "Беларуская", Short: "BE", Lang: "be",
		Rows:     [3]string{"й ц у к е н г ш ў з х", "ф ы в а п р о л д ж э", "я ч с м і т ь б ю"},
		Accents:  map[string]string{"е": "ё", "ь": "'"},
		NonLatin: true},
	{ID: "el", Name: "Greek", Native: "Ελληνικά", Short: "EL", Lang: "el",
		Rows:     [3]string{"; ς ε ρ τ υ θ ι ο π", "α σ δ φ γ η ξ κ λ", "ζ χ ψ ω β ν μ"},
		Currency: "€",
		Accents: map[string]string{"α": "ά", "ε": "έ", "η": "ή", "ι": "ί ϊ ΐ", "ο": "ό",
			"υ": "ύ ϋ ΰ", "ω": "ώ", "σ": "ς"},
		NonLatin: true},
}

// vkLatinAccents is the shared long-press set for Latin letters, so a French
// name can be typed on the English layout too.
var vkLatinAccents = map[string]string{
	"a": "à á â ä æ ã å ā ą",
	"c": "ç ć č",
	"d": "ď",
	"e": "è é ê ë ē ė ę ě",
	"g": "ğ",
	"i": "ì í î ï ī į ı",
	"l": "ł ľ",
	"n": "ñ ń ň",
	"o": "ò ó ô ö õ ø ō œ ő",
	"r": "ř",
	"s": "ß ś š ş",
	"t": "ť",
	"u": "ù ú û ü ū ů ű",
	"y": "ý ÿ",
	"z": "ž ź ż",
}

// vkSymbolAccents are the long-press variants of punctuation and currency.
var vkSymbolAccents = map[string]string{
	"$":  "€ £ ¥ ₽ ₴ ₺ ¢",
	"€":  "$ £ ¥ ₽ ₴ ₺ ¢",
	"£":  "€ $ ¥ ₽ ₴ ₺ ¢",
	"-":  "– — •",
	"'":  "’ ‘ ‚",
	"\"": "“ ” „ « »",
	".":  "… ·",
	"?":  "¿",
	"!":  "¡",
	"%":  "‰",
	"&":  "§",
	"*":  "• ★",
	"0":  "°",
}

// vkMaxAccents caps a popup at what fits above a key on the handheld.
const vkMaxAccents = 8

// vkLayoutByID returns the layout with id, or nil.
func vkLayoutByID(id string) *vkLayout {
	for i := range vkLayouts {
		if strings.EqualFold(vkLayouts[i].ID, strings.TrimSpace(id)) {
			return &vkLayouts[i]
		}
	}
	return nil
}

// vkLayoutRows returns the layout's letter rows split into keys. They are split
// once and cached: vkRows runs on every layout rebuild.
func vkLayoutRows(l *vkLayout) [3][]string {
	if l == nil {
		l = &vkLayouts[0]
	}
	if rows, ok := vkLayoutRowCache[l.ID]; ok {
		return rows
	}
	var rows [3][]string
	for i, row := range l.Rows {
		rows[i] = strings.Fields(row)
	}
	vkLayoutRowCache[l.ID] = rows
	return rows
}

var vkLayoutRowCache = map[string][3][]string{}

// vkUpperKey is a letter key's shifted form for layout l: Turkish dots its
// capital I, and keys with no capital (";", "'", "ß") stay as they are.
func vkUpperKey(l *vkLayout, key string) string {
	if l != nil && l.Turkish {
		return strings.ToUpperSpecial(unicode.TurkishCase, key)
	}
	return strings.ToUpper(key)
}

// vkLowerKey reverses vkUpperKey, so an accent popup on "É" finds "é".
func vkLowerKey(l *vkLayout, key string) string {
	if l != nil && l.Turkish {
		return strings.ToLowerSpecial(unicode.TurkishCase, key)
	}
	return strings.ToLower(key)
}

// vkAccentsFor returns the long-press variants for a key on layout l, in the
// case the key shows, or nil when it has none.
func vkAccentsFor(l *vkLayout, key string) []string {
	if key == "" {
		return nil
	}
	lower := vkLowerKey(l, key)
	upper := lower != key
	var list []string
	seen := map[string]bool{key: true}
	addAll := func(s string) {
		for _, v := range strings.Fields(s) {
			if upper {
				v = vkUpperKey(l, v)
			}
			if seen[v] || len(list) >= vkMaxAccents {
				continue
			}
			seen[v] = true
			list = append(list, v)
		}
	}
	if l != nil && l.Accents != nil {
		addAll(l.Accents[lower])
	}
	if l == nil || !l.NonLatin {
		addAll(vkLatinAccents[lower])
	}
	addAll(vkSymbolAccents[key])
	return list
}

// ── Settings ─────────────────────────────────────────────────────────────

// vkCustomString reads a Custom string without fmt, so the per-keystroke and
// per-frame callers allocate nothing.
func vkCustomString(config *Config, key string) string {
	if config == nil || config.Variables.Custom == nil {
		return ""
	}
	if s, ok := config.Variables.Custom[key].(string); ok {
		return s
	}
	return ""
}

// vkCustomBool reads a three-state boolean: unset (or unreadable) is fallback.
func vkCustomBool(config *Config, key string, fallback bool) bool {
	if config == nil || config.Variables.Custom == nil {
		return fallback
	}
	switch v := config.Variables.Custom[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return v != 0
	}
	return fallback
}

// vkSetCustom writes one keyboard setting and schedules the user-config save.
func vkSetCustom(config *Config, key string, value interface{}) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[key] = value
	syncVariableOverrides(config)
}

// vkLayoutState caches the parsed settings, keyed by their raw strings, so the
// keyboard can ask for the current layout every frame for free.
var vkLayoutState struct {
	config     *Config
	rawEnabled string
	rawCurrent string
	enabled    []*vkLayout
	current    *vkLayout
}

// vkParseLayoutIDs turns "en-US, de,xx" into known layouts, deduplicated, in
// order, falling back to the default when nothing usable is listed.
func vkParseLayoutIDs(raw string) []*vkLayout {
	var out []*vkLayout
	for _, id := range strings.Split(raw, ",") {
		l := vkLayoutByID(id)
		if l == nil {
			continue
		}
		dup := false
		for _, have := range out {
			if have == l {
				dup = true
				break
			}
		}
		if !dup && len(out) < vkMaxEnabledLayouts {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		out = []*vkLayout{vkLayoutByID(vkDefaultLayoutID)}
	}
	return out
}

// vkFormatLayoutIDs is the inverse of vkParseLayoutIDs.
func vkFormatLayoutIDs(layouts []*vkLayout) string {
	ids := make([]string, 0, len(layouts))
	for _, l := range layouts {
		if l != nil {
			ids = append(ids, l.ID)
		}
	}
	return strings.Join(ids, ",")
}

func vkRefreshLayoutState() {
	config := appConfig
	rawEnabled := vkCustomString(config, vkKeyLayouts)
	rawCurrent := vkCustomString(config, vkKeyLayout)
	st := &vkLayoutState
	if st.enabled != nil && st.config == config && st.rawEnabled == rawEnabled && st.rawCurrent == rawCurrent {
		return
	}
	st.config, st.rawEnabled, st.rawCurrent = config, rawEnabled, rawCurrent
	st.enabled = vkParseLayoutIDs(rawEnabled)
	st.current = st.enabled[0]
	if l := vkLayoutByID(rawCurrent); l != nil {
		for _, e := range st.enabled {
			if e == l {
				st.current = l
				break
			}
		}
	}
}

// vkEnabledLayouts is the globe cycle, in order.
func vkEnabledLayouts() []*vkLayout {
	vkRefreshLayoutState()
	return vkLayoutState.enabled
}

// vkCurrentLayout is the layout the keys show now.
func vkCurrentLayout() *vkLayout {
	vkRefreshLayoutState()
	return vkLayoutState.current
}

// vkSetEnabledLayouts stores the enabled list (and keeps the current layout
// inside it).
func vkSetEnabledLayouts(config *Config, layouts []*vkLayout) {
	if config == nil || len(layouts) == 0 {
		return
	}
	cur := vkLayoutByID(vkCustomString(config, vkKeyLayout))
	keep := false
	for _, l := range layouts {
		if l == cur {
			keep = true
		}
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[vkKeyLayouts] = vkFormatLayoutIDs(layouts)
	if !keep {
		config.Variables.Custom[vkKeyLayout] = layouts[0].ID
	}
	syncVariableOverrides(config)
	vkLayoutChanged()
}

// vkSetCurrentLayout switches the keys to layout id.
func vkSetCurrentLayout(config *Config, id string) {
	if vkLayoutByID(id) == nil {
		return
	}
	vkSetCustom(config, vkKeyLayout, vkLayoutByID(id).ID)
	vkLayoutChanged()
}

// vkCycleLayout moves to the next enabled layout and returns it (the globe).
func vkCycleLayout(config *Config) *vkLayout {
	enabled := vkEnabledLayouts()
	cur := vkCurrentLayout()
	if len(enabled) < 2 {
		return cur
	}
	next := enabled[0]
	for i, l := range enabled {
		if l == cur {
			next = enabled[(i+1)%len(enabled)]
			break
		}
	}
	vkSetCurrentLayout(config, next.ID)
	return next
}

// vkLayoutChanged makes the key grid and its cached texture follow a layout
// switch on the next frame.
func vkLayoutChanged() {
	vkLayoutSig = ""
}

// vkSuggestionsEnabled, vkLearnEnabled, vkAutoCapEnabled and vkSoundsEnabled
// are the Typing switches of Keyboard settings; all default to on.
func vkSuggestionsEnabled() bool { return vkCustomBool(appConfig, vkKeySuggestions, true) }
func vkLearnEnabled() bool       { return vkCustomBool(appConfig, vkKeyLearn, true) }
func vkAutoCapEnabled() bool     { return vkCustomBool(appConfig, vkKeyAutoCap, true) }
func vkSoundsEnabled() bool      { return vkCustomBool(appConfig, vkKeySounds, true) }

// vkVoiceFollowsLayout reports whether voice typing tells the recogniser the
// layout's language (the default) rather than letting it detect one.
func vkVoiceFollowsLayout() bool {
	return !strings.EqualFold(vkCustomString(appConfig, vkKeyVoiceLang), "auto")
}
