package main

// setsearch.go — search inside Settings.
//
// The first entry of the rail is Search. Its pane lists every row whose label,
// line, id or section matches what was typed, best first, and re-ranks on
// every key while the on-screen keyboard is up (setSearchVKTarget, a live
// field like Home search's). A result shows its real control, so Left/Right
// still change a switch or a level from the list; A goes to the row in its
// section, unfolding More when the row is behind it. Y (Ctrl+F on a keyboard)
// starts a search from anywhere on the page, and on a PC typing a letter on
// the rail does too.
//
// Words people use for a setting are often not its label ("wifi" for
// Network, "dark mode" for themes, "sleep" for the screensaver), so a small
// synonym table widens each word before matching.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// setSearchVKTarget aims the on-screen keyboard at the Settings search, like
// homeSearchVKTarget: one case per keyboard hook in vkeyboard.go.
const setSearchVKTarget vkTargetKind = 91

// setSearchMaxHits caps the result list; a two-letter query must not list
// half of Settings.
const setSearchMaxHits = 40

// setSearchEntry is one searchable row, folded once per search.
type setSearchEntry struct {
	section int
	where   string
	row     setRow
	label   string
	squash  string
	id      string
	detail  string
	sec     string
	order   int
}

// setSearchHit is one result.
type setSearchHit struct {
	section int
	where   string
	row     setRow
	score   int
	order   int
}

// setSearchState is the query and its results.
type setSearchState struct {
	query string
	hits  []setSearchHit
	index []setSearchEntry
}

var setSearch setSearchState

// setSearchPhrases are multi-word queries and what they mean. Checked before
// the single words, so "dark mode" is not read as "dark" and "mode".
var setSearchPhrases = []struct {
	phrase string
	alts   []string
}{
	{"dark mode", []string{"theme", "light dark", "dark"}},
	{"light mode", []string{"theme", "light dark"}},
	{"night mode", []string{"theme", "light dark"}},
	{"wi fi", []string{"wifi", "network"}},
	{"font size", []string{"text size"}},
	{"screen off", []string{"screensaver", "idle screen", "auto sleep"}},
	{"time zone", []string{"timezone"}},
	{"hey juka", []string{"wake word", "hey juka"}},
	{"stick lights", []string{"led", "lights"}},
}

// setSearchSynonyms widen one typed word.
var setSearchSynonyms = map[string][]string{
	"wifi":          {"wi fi", "network", "wireless", "internet"},
	"internet":      {"network", "wi fi"},
	"wireless":      {"wi fi", "bluetooth"},
	"dark":          {"theme", "light dark"},
	"light":         {"theme", "light dark", "brightness"},
	"theme":         {"themes", "colour", "wallpaper"},
	"color":         {"colour", "theme"},
	"look":          {"theme", "wallpaper", "glass"},
	"volume":        {"sound", "loud", "level"},
	"sound":         {"volume", "audio", "chime"},
	"audio":         {"sound", "volume", "equalizer"},
	"loud":          {"volume", "level"},
	"mute":          {"volume", "sound", "quiet"},
	"music":         {"background music", "song"},
	"sleep":         {"screensaver", "idle", "suspend", "power"},
	"screensaver":   {"idle screen", "sleep"},
	"battery":       {"power", "low power", "battery saver"},
	"power":         {"battery", "low power", "sleep"},
	"charge":        {"battery"},
	"language":      {"lang", "translate", "speech language"},
	"lang":          {"language"},
	"voice":         {"juka", "speech", "hey juka", "read aloud"},
	"speech":        {"voice", "speech recognition"},
	"assistant":     {"juka", "game assistant", "voice"},
	"ai":            {"juka", "groq", "offline answers"},
	"groq":          {"key", "ai"},
	"key":           {"groq"},
	"bluetooth":     {"bt", "headphones", "devices"},
	"bt":            {"bluetooth"},
	"headphones":    {"bluetooth", "audio output"},
	"controller":    {"controls", "button", "stick", "pad", "profile"},
	"gamepad":       {"controls", "button", "stick"},
	"buttons":       {"button", "controls", "swap"},
	"button":        {"buttons", "controls", "swap"},
	"joystick":      {"stick", "calibration"},
	"stick":         {"calibration", "deadzone"},
	"keyboard":      {"on screen keyboard", "typing", "key repeat"},
	"typing":        {"keyboard", "voice typing"},
	"time":          {"clock", "date", "time zone"},
	"clock":         {"time", "date", "12 hour"},
	"date":          {"time", "clock"},
	"brightness":    {"backlight", "screen"},
	"screen":        {"brightness", "display", "full screen"},
	"display":       {"theme", "brightness", "text size"},
	"text":          {"text size", "bold", "font"},
	"font":          {"text", "bold", "legible"},
	"big":           {"text size", "magnifier", "zoom"},
	"zoom":          {"magnifier", "text size"},
	"update":        {"updates", "emulator updates"},
	"updates":       {"update"},
	"reset":         {"restore defaults", "reset"},
	"backup":        {"backup restore"},
	"rumble":        {"vibration", "haptics"},
	"haptics":       {"vibration"},
	"vibrate":       {"vibration"},
	"led":           {"lights", "stick lights"},
	"rgb":           {"lights", "colour"},
	"lights":        {"led", "stick lights"},
	"notifications": {"notification", "alerts", "toast"},
	"alerts":        {"notifications"},
	"name":          {"your name", "device name"},
	"weather":       {"temperature"},
	"privacy":       {"cookies", "diagnostics"},
	"cookies":       {"sign ins"},
	"cache":         {"clear cache"},
	"phone":         {"companion"},
	"home":          {"home layout", "edit home", "home page"},
	"wallpaper":     {"background", "gif"},
	"background":    {"wallpaper", "living background", "background music"},
	"emulator":      {"emulators", "retroarch", "cores"},
	"games":         {"emulators", "library", "saves"},
	"save":          {"saves", "save manager"},
	"browser":       {"search engine", "home page", "javascript"},
	"ads":           {"block ads"},
	"subtitles":     {"captions", "subtitle"},
	"captions":      {"subtitles", "caption"},
	"accessibility": {"contrast", "magnifier", "text size"},
	"startup":       {"boot", "start at login", "startup animation"},
	"boot":          {"startup", "boot logo"},
	"plugin":        {"plugins"},
	"overlay":       {"in game overlay", "osd"},
	"fps":           {"in game overlay"},
}

// setSearchFold lowercases and turns punctuation into spaces.
func setSearchFold(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// setSearchSplitID turns a row id into words: "uiLanguage" → "ui language".
func setSearchSplitID(id string) string {
	var b strings.Builder
	for i, r := range id {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return setSearchFold(b.String())
}

// setSearchBuildIndex folds every row Settings shows here.
func setSearchBuildIndex(c *Config) []setSearchEntry {
	var out []setSearchEntry
	order := 0
	for si, s := range setSections() {
		where := setOr(s.Title, s.ID)
		sec := setSearchFold(s.Title + " " + s.Subtitle)
		for _, row := range setSectionRows(c, s) {
			if strings.TrimSpace(row.Label) == "" || (c != nil && row.Visible != nil && !row.Visible(c)) {
				continue
			}
			detail := ""
			if c != nil && row.Detail != nil {
				detail = row.Detail(c)
			}
			label := setSearchFold(row.Label)
			out = append(out, setSearchEntry{
				section: si,
				where:   where,
				row:     row,
				label:   label,
				squash:  strings.ReplaceAll(label, " ", ""),
				id:      setSearchSplitID(row.ID),
				detail:  setSearchFold(detail),
				sec:     sec,
				order:   order,
			})
			order++
		}
	}
	return out
}

// setSearchGroups splits a query into groups of alternatives; a row matches
// when every group has an alternative that matches it.
func setSearchGroups(query string) [][]string {
	q := setSearchFold(query)
	if q == "" {
		return nil
	}
	var groups [][]string
	padded := " " + q + " "
	for _, p := range setSearchPhrases {
		if strings.Contains(padded, " "+p.phrase+" ") {
			groups = append(groups, append([]string{p.phrase}, p.alts...))
			padded = strings.Replace(padded, " "+p.phrase+" ", " ", 1)
		}
	}
	for _, w := range strings.Fields(padded) {
		g := []string{w}
		for key, alts := range setSearchSynonyms {
			if key == w || (len(w) >= 3 && strings.HasPrefix(key, w)) {
				g = append(g, alts...)
			}
		}
		groups = append(groups, g)
	}
	return groups
}

// setSearchFieldScore is how well term matches a folded field: 3 when the
// field starts with it, 2 when a word does, 1 when it is inside a word.
func setSearchFieldScore(term, field string) int {
	switch {
	case term == "" || field == "":
		return 0
	case strings.HasPrefix(field, term):
		return 3
	case strings.Contains(" "+field, " "+term):
		return 2
	case len(term) >= 3 && strings.Contains(field, term):
		return 1
	}
	return 0
}

// setSearchTermScore scores one term against one row: the label counts most,
// then the id and the section, then the line under the label.
func setSearchTermScore(term string, e *setSearchEntry) int {
	best := 40 * setSearchFieldScore(term, e.label)
	if best == 0 && len(term) >= 4 && strings.Contains(e.squash, strings.ReplaceAll(term, " ", "")) {
		best = 40
	}
	for _, f := range [...]struct {
		w     int
		field string
	}{{15, e.id}, {12, e.sec}, {8, e.detail}} {
		if s := f.w * setSearchFieldScore(term, f.field); s > best {
			best = s
		}
	}
	return best
}

// setSearchRun ranks the index against the query.
func setSearchRun(c *Config) {
	setSearch.hits = setSearch.hits[:0]
	groups := setSearchGroups(setSearch.query)
	if len(groups) == 0 {
		return
	}
	if setSearch.index == nil {
		setSearch.index = setSearchBuildIndex(c)
	}
	for i := range setSearch.index {
		e := &setSearch.index[i]
		total := 0
		for _, g := range groups {
			best := 0
			for k, term := range g {
				s := setSearchTermScore(term, e)
				if k > 0 {
					// A synonym is a guess; the word itself ranks first.
					s = s * 2 / 3
				}
				if s > best {
					best = s
				}
			}
			if best == 0 {
				total = 0
				break
			}
			total += best
		}
		if total > 0 {
			setSearch.hits = append(setSearch.hits, setSearchHit{
				section: e.section, where: e.where, row: e.row, score: total, order: e.order,
			})
		}
	}
	sort.SliceStable(setSearch.hits, func(a, b int) bool {
		ha, hb := setSearch.hits[a], setSearch.hits[b]
		if ha.score != hb.score {
			return ha.score > hb.score
		}
		return ha.order < hb.order
	})
	// One row may sit in two sections (High contrast is in Display and in
	// Accessibility): list it once, where it matched best.
	seen := map[string]bool{}
	kept := setSearch.hits[:0]
	for _, h := range setSearch.hits {
		key := h.row.ID + "\x00" + h.row.Label
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, h)
		if len(kept) == setSearchMaxHits {
			break
		}
	}
	setSearch.hits = kept
}

// setSearchSetQuery is the keyboard writing the query: the results behind it
// re-rank on every key.
func setSearchSetQuery(text string) {
	setSearch.query = text
	setSearchRun(setHub.cfg)
	setHub.row = 0
	setHub.scroll, setHub.target = 0, 0
	setHub.dirty = true
}

// setSearchOpenKeyboard aims the on-screen keyboard at the query, the way the
// other live filters open it (homeSearchOpenKeyboard).
func setSearchOpenKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = setSearchVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// setSearchKeyboardDone is the keyboard's Search key: the results are already
// there, so the focus moves onto the first one and A opens it.
func setSearchKeyboardDone() {
	if !setHub.onSearch {
		return
	}
	setHub.dirty = true
	if len(setSearch.hits) > 0 {
		setHub.inPane = true
		setHub.row = 0
		setHub.follow = true
	}
}

// setSearchBegin puts the rail on Search, with a fresh index of the rows (a
// switch flipped since the last search may have shown or hidden some).
func setSearchBegin(c *Config) {
	if !setHub.onSearch {
		setSearch.index = nil
		setSearchRun(c)
	}
	setHub.onSearch = true
	setHub.inPane = false
	setHub.row = 0
	setHub.follow = true
	setHub.scroll, setHub.target = 0, 0
	setHub.dirty = true
}

// setSearchStart opens Search with the keyboard up: Y, Ctrl+F, a letter typed
// on the rail, or a click on the entry.
func setSearchStart(c *Config) {
	setSearchBegin(c)
	PlayActivateSound()
	setSearchOpenKeyboard()
}

// setSearchClear empties the query (B on the Search entry).
func setSearchClear() {
	setSearch.query = ""
	setSearch.hits = setSearch.hits[:0]
	setHub.dirty = true
}

// setSearchReset forgets the search for a new visit.
func setSearchReset() {
	setSearch.query = ""
	setSearch.hits = nil
	setSearch.index = nil
}

// setSearchJump goes to result i's row in its section.
func setSearchJump(c *Config, i int) {
	if i < 0 || i >= len(setSearch.hits) {
		return
	}
	h := setSearch.hits[i]
	PlayActivateSound()
	setHubSelectSection(c, h.section, false)
	if !setHubFocusRow(h.row.ID, h.row.Label) {
		setHubEnterPane(false)
	}
}

// setSearchRows are the results as rows for the pane, with their sections
// named on the line under each label.
func setSearchRows(c *Config) ([]setRow, []setRowView) {
	rows := make([]setRow, len(setSearch.hits))
	views := make([]setRowView, len(setSearch.hits))
	for i, h := range setSearch.hits {
		rows[i] = h.row
		v := setHubReadRow(c, h.row)
		if v.detail == "" {
			v.detail = h.where
		} else {
			v.detail = h.where + " · " + v.detail
		}
		views[i] = v
	}
	return rows, views
}

// setSearchSubtitle is the pane's line under "Search".
func setSearchSubtitle() string {
	q := strings.TrimSpace(setSearch.query)
	switch {
	case q == "":
		return "Type a word: wifi, brightness, sleep, language…"
	case len(setSearch.hits) == 0:
		return "Nothing matches “" + q + "” · try another word"
	case len(setSearch.hits) == 1:
		return "1 setting matches “" + q + "”"
	}
	return strconv.Itoa(len(setSearch.hits)) + " settings match “" + q + "”"
}

// setSearchHint is the footer badge of the search shortcut.
func setSearchHint() FooterHint {
	return FooterHint{Button: "Y / Ctrl+F", Label: "Search"}
}

// setHubActSearchEntry applies an intent while the rail's Search entry holds
// the focus: A types, Right steps into the results, Down goes to the first
// section, Up to the top bar, and B clears the words before it leaves.
func setHubActSearchEntry(c *Config, act setHubAction) {
	switch act {
	case setActUp:
		focusEngine.ClearFocus()
		focusChromeBar()
	case setActDown, setActFirst:
		setHubSelectSection(c, 0, true)
	case setActLast:
		setHubSelectSection(c, len(setSections())-1, true)
	case setActRight:
		if len(setSearch.hits) > 0 {
			setHubEnterPane(true)
			return
		}
		setSearchStart(c)
	case setActOK:
		setSearchStart(c)
	case setActBack:
		if strings.TrimSpace(setSearch.query) != "" {
			setSearchClear()
			PlayBackSound()
			return
		}
		goBackScene(c)
	}
}

// setHubKeyStartsSearch reports a key that opens Search: Ctrl+F anywhere on
// the page, or a letter or digit typed while the rail holds the focus. The
// keyboard opens on the key press, so the same key's text event lands in the
// query. B, Q and E keep the meanings every screen gives them (Back and the
// scene keys in main.go).
func setHubKeyStartsSearch(e *sdl.KeyboardEvent) bool {
	sym, mod := e.Keysym.Sym, e.Keysym.Mod
	if mod&sdl.KMOD_CTRL != 0 {
		return sym == sdl.K_f && mod&(sdl.KMOD_ALT|sdl.KMOD_SHIFT) == 0
	}
	if mod&(sdl.KMOD_ALT|sdl.KMOD_GUI) != 0 || e.Repeat != 0 || setHub.inPane {
		return false
	}
	switch sym {
	case sdl.K_b, sdl.K_q, sdl.K_e:
		return false
	}
	return (sym >= sdl.K_a && sym <= sdl.K_z) || (sym >= sdl.K_0 && sym <= sdl.K_9)
}

// setHubSectionOpen reports whether the open section has More unfolded.
func setHubSectionOpen() bool {
	sections := setSections()
	if setHub.section < 0 || setHub.section >= len(sections) {
		return false
	}
	return setMoreOpen[sections[setHub.section].ID]
}

// setHubDrawRailSearch draws the rail's Search entry: a field while idle, the
// rail's selection look while it is the open entry, with the words typed in
// place of "Search".
func setHubDrawRailSearch(renderer *sdl.Renderer, f setHubFonts, r sdl.Rect, chrome bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	selected := setHub.onSearch
	focused := selected && !setHub.inPane && !chrome && !setHub.sheet.open
	accent := glassAccent()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if selected {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, WithAlpha(accent, 34))
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, glassFillFocus())
		if focused {
			drawGlassFocusRing(renderer, r, glassRowRadius)
		} else {
			fillRoundedRect(renderer, r.X+6, r.Y+11, 4, r.H-22, 2, accent)
		}
	} else {
		drawGlassSurface(renderer, r, glassRowRadius, glassState{NoLift: true, Hovered: pointInRect(mouseX, mouseY, r)})
	}
	icon := r.H - 22
	if icon > 24 {
		icon = 24
	}
	if icon < 18 {
		icon = 18
	}
	ix := r.X + 18
	DrawHomeIcon(renderer, "search", sdl.Rect{X: ix, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, selected)
	tx := ix + icon + 12
	text, ink := "Search", ColorTextSecondary()
	if q := strings.TrimSpace(setSearch.query); q != "" {
		text, ink = q, ColorTextPrimary()
	} else if selected {
		ink = ColorTextPrimary()
	}
	textH := int32(f.rail.Height())
	drawText(renderer, f.rail, ellipsize(f.rail, text, r.X+r.W-12-tx), tx, r.Y+(r.H-textH)/2, ink, textAlignLeft)
}
