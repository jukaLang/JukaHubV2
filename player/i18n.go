package main

// i18n.go — the interface language (Settings → General → Language).
//
// JukaHub's interface is written in English, as string literals at several
// thousand call sites. Wrapping every one of them in a T("…") call would be a
// change to most files of the package and would have to be repeated by every
// screen written after it. Instead the translation happens where text becomes
// pixels: the few helpers every label goes through to be measured, clipped,
// wrapped and drawn (textcache.go's measureText and fitTextCached, renderText
// and wrapTextLines in core.go, drawText in homelayout.go, the config canvas)
// call uiT on the string they were given. Measuring and drawing translate the
// same way, so a centred or right-aligned label is laid out at the width that
// is actually drawn, and clipping translates BEFORE it shortens, so a long
// German label ends in "…" rather than reverting to a clipped English one.
//
// The catalogue is gettext-style: the key is the exact English string. Each
// language is one JSON file in i18n/ compiled into the binary:
//
//	{"language": "es", "name": "Español",
//	 "strings": {"Settings": "Ajustes", "%d games": "%d juegos", …}}
//
// A key without a placeholder is an exact match. A key with %d (digits) or %s
// (any text) is a pattern: "Deleted %d files" matches "Deleted 3 files" and
// becomes "Se eliminaron 3 archivos". A value may reorder the captures with
// %[2]s. A %s capture that is itself a catalogue key is translated too, so
// "Weather: %s" turns "Weather: On" into "Tiempo: Activado". Two more cheap
// rules cover the way the UI joins words: "Label: value" (a toast or read-out
// whose label is a key) and "part · part" (a detail line made of known parts).
//
// What is NOT translated: content. Web pages draw through drawTextRun (raw),
// the PDF viewer and the boot film rasterise on their own, chat bubbles, file
// names and the text browser's and Notes' content lines use the raw paths too
// (drawTextRun, renderTextRaw). Because keys are exact whole strings, a game
// called "Settings" would be translated in a list — rare, harmless, and the
// price of not touching every call site. A web form's button that is clipped
// through ellipsize is translated the same way when its label is a key.
//
// Not covered yet: text measured with font.SizeUTF8 directly instead of
// measureText (~340 sites, mostly inside screens) is laid out at the English
// width while the translation is drawn; left-aligned text is unaffected, a
// centred one sits slightly off. Those sites move to measureText as screens
// are touched. The boot film (bootliquid.go) and the PDF viewer rasterise
// their own text and stay English/raw.
//
// Cost. The render path runs every frame on the Smart Pro's A133P. English is
// the default and the fast path: one atomic load and a nil check, no lookup,
// no allocation. In another language a label is one map access (no
// allocation); only a string that is not a key (a clock, a battery percentage,
// a game title) goes on to the pattern rules, and that answer is remembered in
// a small bounded cache, so a steady frame pays one locked map read per such
// string.
//
// A translated value is never re-translated by the fit/draw path: fitTextCached
// marks the fitted translation as already translated, and uiT/the raw draw
// helpers strip that mark instead of looking the value up again. The catalogue
// test (TestUICataloguesParse in core_substitute_test.go) checks that path,
// including a value which is also another key.

import (
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

//go:embed i18n/*.json
var uiCatalogFiles embed.FS

// uiLanguageKey is the Custom setting that names the interface language.
const uiLanguageKey = "uiLanguage"

// uiLanguageDefault is the language the strings are written in.
const uiLanguageDefault = "en"

// uiLanguage is one language Settings offers: its code (the catalogue file is
// i18n/<code>.json) and its name in itself. Only languages the bundled Inter
// can draw are offered (Latin and Cyrillic; see setlang.go and fonts.go): a
// language whose letters fall outside Inter would render as substitutes.
type uiLanguage struct {
	Code   string
	Native string
}

// uiLanguages is the list Settings shows, English first.
var uiLanguages = []uiLanguage{
	{"en", "English"},
	{"es", "Español"},
	{"pt-BR", "Português (Brasil)"},
	{"fr", "Français"},
	{"de", "Deutsch"},
	{"it", "Italiano"},
	{"ru", "Русский"},
}

// uiCatalogFile is the decoded shape of one i18n/<code>.json.
type uiCatalogFile struct {
	Language string            `json:"language"`
	Name     string            `json:"name"`
	Strings  map[string]string `json:"strings"`
}

// uiPattern is one catalogue key with placeholders, split for matching: lits
// are the literal runs around the placeholders (len(kinds)+1 of them), kinds
// the placeholder letters ('d' or 's'), value the translation template.
type uiPattern struct {
	lits  []string
	kinds []byte
	value string
}

// uiCatalog is one loaded language.
type uiCatalog struct {
	code     string
	exact    map[string]string
	patterns []uiPattern

	// slow remembers what the pattern rules made of strings that are not
	// keys, including "nothing" (the string itself). Bounded: cleared when
	// full, since the working set is one screen of dynamic labels.
	slowMu sync.Mutex
	slow   map[string]string
}

// uiActive is the language in force; nil means English (no translation).
var uiActive atomic.Pointer[uiCatalog]

// uiSlowMax bounds the per-language cache of non-key strings.
const uiSlowMax = 2048

// uiSlowMaxLen keeps paragraphs (a description, a log line) out of the
// pattern rules and the cache: UI labels and toasts are shorter.
const uiSlowMaxLen = 240

// uiT is the string to draw for the English UI string s.
func uiT(s string) string {
	c := uiActive.Load()
	if c == nil {
		return uiUnmarkTranslated(s)
	}
	if s == "" {
		return s
	}
	if strings.Contains(s, uiTranslatedStart) {
		return c.translateMarked(s)
	}
	if t, ok := c.exact[s]; ok {
		return t
	}
	return c.translateSlow(s)
}

// uiTranslatedMark wraps text that fitTextCached has already translated and
// fitted. The marks are internal to the text path: uiT strips them instead of
// translating again, and the raw draw helpers strip them before rendering. It
// carries provenance that a plain string cannot, which is why a translated
// value that is also another key (Spanish Network -> Red) can safely reach a
// draw call without being sent through the catalogue a second time.
const (
	// Zero-width markers: direct font.SizeUTF8 measurements ignore them,
	// while a C string does not terminate on them like it does on NUL.
	uiTranslatedStart = "\u200b"
	uiTranslatedEnd   = "\u2060"
)

func uiMarkTranslated(s string) string {
	return uiTranslatedStart + s + uiTranslatedEnd
}

func uiUnmarkTranslated(s string) string {
	if !strings.Contains(s, uiTranslatedStart) && !strings.Contains(s, uiTranslatedEnd) {
		return s
	}
	return strings.NewReplacer(uiTranslatedStart, "", uiTranslatedEnd, "").Replace(s)
}

// translateMarked translates the unmarked parts of a composed label and keeps
// the marked parts as they are. A fitted label can be concatenated with other
// UI text ("Loading " + fitted), so the marks must survive composition until
// the draw boundary; otherwise the fitted part would be translated again.
func (c *uiCatalog) translateMarked(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		start := strings.Index(s, uiTranslatedStart)
		if start < 0 {
			out.WriteString(c.translatePlain(s))
			break
		}
		out.WriteString(c.translatePlain(s[:start]))
		s = s[start+len(uiTranslatedStart):]
		end := strings.Index(s, uiTranslatedEnd)
		if end < 0 {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:end])
		s = s[end+len(uiTranslatedEnd):]
	}
	return out.String()
}

func (c *uiCatalog) translatePlain(s string) string {
	if s == "" {
		return s
	}
	if t, ok := c.exact[s]; ok {
		return t
	}
	return c.translateSlow(s)
}

// uiLanguageCode is the language in force ("en" when none is set).
func uiLanguageCode() string {
	if c := uiActive.Load(); c != nil {
		return c.code
	}
	return uiLanguageDefault
}

// translateSlow applies the pattern rules to a string that is not a key.
func (c *uiCatalog) translateSlow(s string) string {
	if len(s) > uiSlowMaxLen {
		return s
	}
	c.slowMu.Lock()
	if t, ok := c.slow[s]; ok {
		c.slowMu.Unlock()
		return t
	}
	c.slowMu.Unlock()

	out := c.applyRules(s)

	c.slowMu.Lock()
	if len(c.slow) >= uiSlowMax {
		c.slow = make(map[string]string, 256)
	}
	c.slow[s] = out
	c.slowMu.Unlock()
	return out
}

// applyRules tries the placeholder patterns, then the " · " and ": " joins.
// It returns s itself when nothing applies.
func (c *uiCatalog) applyRules(s string) string {
	for i := range c.patterns {
		if out, ok := c.patterns[i].apply(s, c.exact); ok {
			return out
		}
	}
	if strings.Contains(s, " · ") {
		parts := strings.Split(s, " · ")
		changed := false
		for i, p := range parts {
			if t := c.piece(p); t != p {
				parts[i] = t
				changed = true
			}
		}
		if changed {
			return strings.Join(parts, " · ")
		}
		return s
	}
	return c.labelValue(s)
}

// piece translates one part of a joined line: a key, a pattern, or a
// "Label: value" pair.
func (c *uiCatalog) piece(p string) string {
	if t, ok := c.exact[p]; ok {
		return t
	}
	for i := range c.patterns {
		if out, ok := c.patterns[i].apply(p, c.exact); ok {
			return out
		}
	}
	return c.labelValue(p)
}

// labelValue translates "Label: value" when Label is a key; the value is
// translated too when it is a key ("On", "Auto"), and kept otherwise (a name,
// a number).
func (c *uiCatalog) labelValue(s string) string {
	i := strings.Index(s, ": ")
	if i <= 0 {
		return s
	}
	label, ok := c.exact[s[:i]]
	if !ok {
		return s
	}
	value := s[i+2:]
	if t, ok := c.exact[value]; ok {
		value = t
	}
	return label + ": " + value
}

// apply matches s against the pattern and fills in the translation. The
// literal runs are matched left to right, each at its first occurrence.
func (p *uiPattern) apply(s string, exact map[string]string) (string, bool) {
	n := len(p.kinds)
	first, last := p.lits[0], p.lits[n]
	if len(s) < len(first)+len(last)+n || !strings.HasPrefix(s, first) || !strings.HasSuffix(s, last) {
		return "", false
	}
	rest := s[len(first) : len(s)-len(last)]
	var capsBuf [4]string
	caps := capsBuf[:0]
	for i := 0; i < n; i++ {
		var cp string
		if i == n-1 {
			cp = rest
		} else {
			j := strings.Index(rest, p.lits[i+1])
			if j < 0 {
				return "", false
			}
			cp = rest[:j]
			rest = rest[j+len(p.lits[i+1]):]
		}
		if cp == "" || (p.kinds[i] == 'd' && !uiIsNumber(cp)) {
			return "", false
		}
		if p.kinds[i] == 's' {
			if t, ok := exact[cp]; ok {
				cp = t
			}
		}
		caps = append(caps, cp)
	}
	return uiFill(p.value, caps), true
}

// uiIsNumber accepts the numbers the UI prints: digits with an optional sign
// and thousands or decimal separators.
func uiIsNumber(s string) bool {
	digits := 0
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
		case b >= '0' && b <= '9':
			digits++
		case b == '-' || b == '+':
			if i != 0 {
				return false
			}
		case b == ',' || b == '.':
		default:
			return false
		}
	}
	return digits > 0
}

// uiFill writes the captures into a translation template: %d and %s take the
// next capture, %[n]d and %[n]s capture n, %% is a percent sign.
func uiFill(tmpl string, caps []string) string {
	var b strings.Builder
	b.Grow(len(tmpl) + 16)
	next := 0
	for i := 0; i < len(tmpl); i++ {
		ch := tmpl[i]
		if ch != '%' || i+1 >= len(tmpl) {
			b.WriteByte(ch)
			continue
		}
		switch tmpl[i+1] {
		case '%':
			b.WriteByte('%')
			i++
		case 'd', 's':
			if next < len(caps) {
				b.WriteString(caps[next])
			}
			next++
			i++
		case '[':
			// %[n]d / %[n]s with a one-digit n.
			if i+4 < len(tmpl) && tmpl[i+3] == ']' && tmpl[i+2] >= '1' && tmpl[i+2] <= '9' &&
				(tmpl[i+4] == 'd' || tmpl[i+4] == 's') {
				if k := int(tmpl[i+2] - '1'); k < len(caps) {
					b.WriteString(caps[k])
				}
				i += 4
			} else {
				b.WriteByte(ch)
			}
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// uiParsePattern splits a key with placeholders. ok is false for a key with
// no placeholder (an exact key) or with two placeholders side by side, which
// cannot be matched unambiguously.
func uiParsePattern(key string) (lits []string, kinds []byte, ok bool) {
	start := 0
	for i := 0; i+1 < len(key); i++ {
		if key[i] != '%' {
			continue
		}
		switch key[i+1] {
		case 'd', 's':
			lits = append(lits, key[start:i])
			kinds = append(kinds, key[i+1])
			start = i + 2
			i++
		case '%':
			i++
		}
	}
	if len(kinds) == 0 {
		return nil, nil, false
	}
	lits = append(lits, key[start:])
	for i := 1; i < len(lits)-1; i++ {
		if lits[i] == "" {
			return nil, nil, false
		}
	}
	// A key that is (almost) all placeholder ("%s", "%d %s") would match
	// every label on screen.
	if len(strings.TrimSpace(strings.Join(lits, ""))) < 3 {
		return nil, nil, false
	}
	// "Word %s" with nothing after the capture would also rewrite a game
	// title or file name that merely starts with that word ("Hidden Dragon"
	// → "Dragon oculto", "Still Life" → "Aún Life"). Only progress verbs
	// ("Launching %s", "Fetching %s"), which titles do not start with, keep
	// that shape; other one-word prefixes stay English.
	if len(kinds) == 1 && kinds[0] == 's' && lits[1] == "" {
		if w := lits[0]; strings.Count(w, " ") == 1 && strings.HasSuffix(w, " ") && !strings.HasSuffix(w, "ing ") {
			return nil, nil, false
		}
	}
	return lits, kinds, true
}

// uiPlaceholders lists a string's placeholder letters in order (%[n]x counts
// as x); the catalogue test compares a key's with its value's.
func uiPlaceholders(s string) []byte {
	var out []byte
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		switch c := s[i+1]; c {
		case 'd', 's':
			out = append(out, c)
			i++
		case '%':
			i++
		case '[':
			if i+4 < len(s) && s[i+3] == ']' && (s[i+4] == 'd' || s[i+4] == 's') {
				out = append(out, s[i+4])
				i += 4
			}
		}
	}
	return out
}

// uiParseCatalog decodes one catalogue file. Empty values are dropped (they
// would blank a label), as are keys whose value names other placeholders.
func uiParseCatalog(code string, data []byte) (*uiCatalog, error) {
	var f uiCatalogFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	c := &uiCatalog{
		code:  code,
		exact: make(map[string]string, len(f.Strings)),
		slow:  make(map[string]string, 256),
	}
	for k, v := range f.Strings {
		if k == "" || strings.TrimSpace(v) == "" {
			continue
		}
		if lits, kinds, ok := uiParsePattern(k); ok {
			if string(uiSortedBytes(uiPlaceholders(v))) != string(uiSortedBytes(kinds)) {
				continue
			}
			c.patterns = append(c.patterns, uiPattern{lits: lits, kinds: kinds, value: v})
			continue
		}
		c.exact[k] = v
	}
	// Longest literal text first: "Deleted %d files from %s" must be tried
	// before "Deleted %s".
	sortPatternsBySpecificity(c.patterns)
	return c, nil
}

// uiSortedBytes is a sorted copy, for comparing placeholder multisets.
func uiSortedBytes(b []byte) []byte {
	out := append([]byte(nil), b...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// sortPatternsBySpecificity orders patterns by the length of their literal
// text, longest first (insertion sort: a few hundred patterns, once).
func sortPatternsBySpecificity(ps []uiPattern) {
	litLen := func(p *uiPattern) int {
		n := 0
		for _, l := range p.lits {
			n += len(l)
		}
		return n
	}
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && litLen(&ps[j]) > litLen(&ps[j-1]); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

// uiNormalizeLanguage maps a stored value onto an offered code ("pt_br",
// "PT-BR" and "pt" all mean Português (Brasil)); anything unknown is English.
func uiNormalizeLanguage(v string) string {
	v = strings.ReplaceAll(strings.TrimSpace(v), "_", "-")
	for _, l := range uiLanguages {
		if strings.EqualFold(v, l.Code) {
			return l.Code
		}
	}
	if strings.EqualFold(v, "pt") {
		return "pt-BR"
	}
	return uiLanguageDefault
}

// uiLoadCatalog reads a language's file from the binary.
func uiLoadCatalog(code string) (*uiCatalog, error) {
	data, err := uiCatalogFiles.ReadFile("i18n/" + code + ".json")
	if err != nil {
		return nil, err
	}
	c, err := uiParseCatalog(code, data)
	if err != nil {
		return nil, err
	}
	uiMergeCatalogParts(c)
	return c, nil
}

// uiMergeCatalogParts adds i18n/<code>.<part>.json files (the first-run
// tour's strings are i18n/<code>.tour.json, onboarding_lang.go) to a loaded
// language. The main file wins where both name a key.
func uiMergeCatalogParts(c *uiCatalog) {
	parts, _ := fs.Glob(uiCatalogFiles, "i18n/"+c.code+".*.json")
	added := false
	for _, name := range parts {
		data, err := uiCatalogFiles.ReadFile(name)
		if err != nil {
			continue
		}
		p, err := uiParseCatalog(c.code, data)
		if err != nil {
			logWarn("i18n", "%s: %v", name, err)
			continue
		}
		for k, v := range p.exact {
			if _, ok := c.exact[k]; !ok {
				c.exact[k] = v
			}
		}
		if len(p.patterns) > 0 {
			c.patterns = append(c.patterns, p.patterns...)
			added = true
		}
	}
	if added {
		sortPatternsBySpecificity(c.patterns)
	}
}

// uiSetLanguage makes code the interface language. Text caches keyed by the
// English string (fitted and wrapped labels) are dropped so the switch shows
// on the next frame; texture caches are keyed by the drawn string and need
// nothing. Safe from any goroutine.
func uiSetLanguage(code string) {
	code = uiNormalizeLanguage(code)
	if code == uiLanguageCode() {
		return
	}
	if code == uiLanguageDefault {
		uiActive.Store(nil)
	} else {
		c, err := uiLoadCatalog(code)
		if err != nil {
			logWarn("i18n", "language %s: %v", code, err)
			return
		}
		uiActive.Store(c)
	}
	clearTextFitCache()
	clearAppLabelCache()
}

// uiLanguageApply puts the configured language in force. It runs where the
// user's settings are merged into a config (boot, hot reload, a restored
// backup); a config without the key means English.
func uiLanguageApply(config *Config) {
	if config == nil {
		return
	}
	// No key means English: a reset to defaults or a restored backup from
	// before this setting must not leave the previous language in force.
	v, _ := customValue(config.Variables.Custom, uiLanguageKey)
	uiSetLanguage(coerceString(v))
}

// renderTextRaw is renderText without the interface language: for content
// (a note's body, a page line in the text browser) that must be drawn exactly
// as written even when a line happens to equal a catalogue key.
func renderTextRaw(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) (int32, int32) {
	processed := substituteVars(text, config.Variables.Custom)
	if processed == "" || font == nil {
		return 0, 0
	}
	return drawTextRun(renderer, config, font, sanitizeCached(font, processed), color, x, y)
}

// ── Settings row ──────────────────────────────────────────────────────────

// uiLanguageOptions are the stops of the Language row, each in its own
// language (those names are not catalogue keys, so they are never translated).
func uiLanguageOptions(*Config) []setOption {
	out := make([]setOption, 0, len(uiLanguages))
	for _, l := range uiLanguages {
		out = append(out, setOption{Value: l.Code, Label: l.Native})
	}
	return out
}

// uiLanguageCurrent is the index of the language in force.
func uiLanguageCurrent(*Config) int {
	code := uiLanguageCode()
	for i, l := range uiLanguages {
		if l.Code == code {
			return i
		}
	}
	return 0
}

// uiLanguageChoose saves and applies a language.
func uiLanguageChoose(c *Config, value string) {
	code := uiNormalizeLanguage(value)
	setStoreString(c, uiLanguageKey, code)
	uiSetLanguage(code)
	setToast(uiT("Language") + ": " + uiLanguageNative(code))
}

// uiLanguageNative is a code's name in its own language.
func uiLanguageNative(code string) string {
	for _, l := range uiLanguages {
		if l.Code == code {
			return l.Native
		}
	}
	return code
}
