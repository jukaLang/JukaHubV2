package main

// narrate_hooks.go — what narration says on each screen.
//
// One function per screen, each called with one line from where the screen
// draws its focused control (or, for Home, the top bar and the plain element
// screens, from the frame's end): Settings (setui.go), Settings' option list
// (setsheet.go), the shared confirmation sheet (tbxui.go), the Start menu
// (startmenu.go), the Games library (game_library_ui.go) and the on-screen
// keyboard (vkui.go). Each returns at once while narration is off, and builds
// strings the screen already has — no I/O, nothing that waits.
//
// Settings rows are worded from their kind, so every row that exists or will
// be added reads the same way: a switch says its state and "Switch with A.",
// a carousel its value and "Left and right change it.", an action row what
// the footer says A does, a read-out only its value.

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// narrOnOff is a switch's state, said.
func narrOnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// narrCount is "1 game" / "12 games".
func narrCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// narrNear is up to two neighbours' names around i, for making ahead.
func narrNear(n, i int, name func(int) string) []string {
	var out []string
	for _, j := range []int{i + 1, i - 1} {
		if j >= 0 && j < n {
			if s := strings.TrimSpace(name(j)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// ── Settings ─────────────────────────────────────────────────────────────

// narrateSettingsFrame is Settings' hook, after the rail and the pane are
// drawn. While the top bar holds the focus the frame's end reads the bar.
func narrateSettingsFrame(c *Config, chrome bool) {
	if !narrActive() || chrome || c == nil {
		return
	}
	sections := setSections()
	if setHub.section < 0 || setHub.section >= len(sections) {
		return
	}
	sec := sections[setHub.section]
	switch {
	case !setHub.inPane && setHub.onSearch:
		narrateMark(narrLayerScene, narrItem{context: "Settings", key: "search", name: "Search settings",
			value: setSearch.query, hint: "Type"})
	case !setHub.inPane:
		i := setHub.section
		narrateMark(narrLayerScene, narrItem{context: "Settings", key: "section:" + sec.ID, name: sec.Title,
			hint: "Open", detail: sec.Subtitle,
			near: narrNear(len(sections), i, func(j int) string { return sections[j].Title })})
	default:
		i := setHub.row
		if !setHubRowVisible(i) || i >= len(setHub.rows) || i >= len(setHub.views) {
			return
		}
		ctx := sec.Title
		if setHub.onSearch {
			ctx = "Search results"
		}
		it := narrSettingsRowItem(setHub.rows[i], setHub.views[i], setHub.onSearch)
		it.context = ctx
		it.near = narrNear(len(setHub.rows), i, func(j int) string {
			if !setHubRowVisible(j) {
				return ""
			}
			return setHub.rows[j].Label
		})
		narrateMark(narrLayerScene, it)
	}
}

// narrSettingsRowItem words one Settings row from its kind. Pure.
func narrSettingsRowItem(row setRow, v setRowView, search bool) narrItem {
	it := narrItem{key: "row:" + row.ID + "\x00" + row.Label, name: row.Label, detail: v.detail}
	value := strings.TrimSpace(v.value)
	if value == "—" {
		value = ""
	}
	switch row.Kind {
	case setKindToggle:
		it.value = narrOnOff(v.on)
		it.hint = "Switch"
	case setKindChoice:
		it.value = value
		switch {
		case row.Pick != nil:
			it.hint = "Left and right change it, or pick any colour with A."
		case row.Sheet != nil:
			it.hint = "Left and right change it, or list them all with A."
		case row.Field != "":
			it.hint = "Left and right change it, or type one with A."
		default:
			it.hint = "Left and right change it."
		}
	case setKindText:
		it.value = value
		if row.Secret && value != "" {
			it.value = "set"
		}
		it.hint = "Edit"
	case setKindInfo:
		it.value = value
		it.noHint = true
	default:
		// An action: the footer says what A does ("Open", "Show more").
		it.value = value
	}
	if search {
		it.hint, it.noHint = "Go to", false
	}
	if v.disabled {
		if it.value == "" {
			it.value = "unavailable"
		} else {
			it.value += ", unavailable"
		}
		it.noHint = true
	}
	return it
}

// narrateChoiceSheet is the option list's hook (Settings' "All" sheets and
// the pickers that share setsheet.go).
func narrateChoiceSheet(s *setSheetState) {
	if !narrActive() || s == nil || !s.open || s.cursor < 0 || s.cursor >= len(s.options) {
		return
	}
	o := s.options[s.cursor]
	value := ""
	if o.Value == s.current {
		value = "selected"
	}
	if note := strings.TrimSpace(o.Note); note != "" {
		value = strings.TrimPrefix(value+", "+note, ", ")
	}
	narrateMark(narrLayerOverlay, narrItem{context: s.title, intro: s.note, key: "opt:" + o.Value, name: o.Label,
		value: value, hint: "Select",
		near: narrNear(len(s.options), s.cursor, func(j int) string { return s.options[j].Label })})
}

// ── Sheets ───────────────────────────────────────────────────────────────

// narrateSheet is the shared confirmation sheet's hook (tbxRenderSheet): its
// title and message once, then the focused button.
func narrateSheet() {
	if !narrActive() || !tbxSheet.open {
		return
	}
	spec := tbxSheet.spec
	title := strings.TrimSpace(spec.title)
	if title == "" {
		title = "Dialog"
	}
	it := narrItem{context: title, intro: narrSheetBody(spec.body), key: "sheet", noHint: true}
	if n := len(spec.buttons); n > 0 && tbxSheet.focus >= 0 && tbxSheet.focus < n {
		it.key = "button:" + strconv.Itoa(tbxSheet.focus)
		it.name = spec.buttons[tbxSheet.focus].label
		it.value = "button"
		if n > 1 {
			it.value = "button, " + strconv.Itoa(tbxSheet.focus+1) + " of " + strconv.Itoa(n)
		}
	}
	narrateMark(narrLayerOverlay, it)
}

// narrSheetBody is a sheet's message as one paragraph, without its headings'
// and bullets' marks.
func narrSheetBody(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(line), "## "), "• "))
		if line == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(line)
		if b.Len() > 400 {
			break
		}
	}
	return b.String()
}

// ── The Start menu ───────────────────────────────────────────────────────

// narrateStartMenu is the Start menu's hook: the group on the rail, or the
// entry in the list.
func narrateStartMenu(group startMenuGroup, items []startMenuEntry) {
	if !narrActive() {
		return
	}
	if tb.focus == 0 || len(items) == 0 {
		narrateMark(narrLayerOverlay, narrItem{context: "Menu", key: "group:" + group.name, name: group.name,
			value: narrCount(len(items), "item", "items"), hint: "Open",
			near: narrNear(len(items), -1, func(j int) string { return items[j].name })})
		return
	}
	i := tb.cursor
	if i < 0 || i >= len(items) {
		return
	}
	e := items[i]
	narrateMark(narrLayerOverlay, narrItem{context: "Menu", key: "entry:" + group.name + "\x00" + e.name, name: e.name,
		value: e.badge, hint: "Open",
		near: narrNear(len(items), i, func(j int) string { return items[j].name })})
}

// ── The Games library ────────────────────────────────────────────────────

// narrateGameLibrary is the library's hook: the console on the grid, or the
// game in a console's list.
func narrateGameLibrary(systems []librarySystem, system librarySystem, hasSystem bool) {
	if !narrActive() || !gameLib.open {
		return
	}
	if len(systems) == 0 {
		narrateMark(narrLayerPanel, narrItem{context: "Games", key: "empty", name: "No games found",
			value: gameLibraryStatus(), noHint: true})
		return
	}
	if gameLib.focus == 0 || !hasSystem {
		i := gameLib.system
		if i < 0 || i >= len(systems) {
			return
		}
		s := systems[i]
		narrateMark(narrLayerPanel, narrItem{context: "Games", key: "system:" + s.Name, name: s.Name,
			value: narrCount(len(s.Games), "game", "games"), hint: "Open",
			near: narrNear(len(systems), i, func(j int) string { return systems[j].Name })})
		return
	}
	i := gameLib.cursor
	if i < 0 || i >= len(system.Games) {
		return
	}
	title := func(g libraryGame) string {
		if g.DisplayTitle != "" {
			return g.DisplayTitle
		}
		return g.Title
	}
	g := system.Games[i]
	value := strconv.Itoa(i+1) + " of " + strconv.Itoa(len(system.Games))
	narrateMark(narrLayerPanel, narrItem{context: system.Name, key: "game:" + g.Path, name: title(g),
		value: value, hint: "Open",
		near: narrNear(len(system.Games), i, func(j int) string { return title(system.Games[j]) })})
}

// ── The on-screen keyboard ───────────────────────────────────────────────

// narrateKeyboard is the keyboard's hook: the focused key, suggestion, tool
// or the field, with what is typed so far said when it opens.
func narrateKeyboard() {
	if !narrActive() || !virtualKeyboardActive {
		return
	}
	text := vkCurrentText()
	intro := ""
	if t := strings.TrimSpace(text); t != "" {
		if utf8.RuneCountInString(t) > 60 {
			t = string([]rune(t)[:60])
		}
		intro = "Text: " + t
	}
	it := narrItem{context: "Keyboard", intro: intro, noHint: true}
	switch vkFocusZone {
	case vkZoneKeys:
		i := vkFocusedRectIndex()
		if i < 0 || i >= len(keyboardKeys) {
			return
		}
		it.key = "key:" + keyboardKeys[i]
		it.name = narrKeyName(keyboardKeys[i])
	case vkZoneStrip:
		s := strings.TrimSpace(vkStripDisplay(vkZoneIndex))
		if s == "" {
			return
		}
		it.key, it.name, it.value = "strip:"+s, s, "suggestion"
	case vkZoneField:
		it.key, it.name, it.value = "field", "Text field", strings.TrimSpace(text)
		if it.value == "" {
			it.value = "empty"
		}
	case vkZoneTools:
		t := vkTool(vkZoneIndex)
		name := vkToolLabel(t, false)
		switch t {
		case vkToolMic:
			name = "Voice typing"
		case vkToolClose:
			name = "Close keyboard"
		case vkToolGlobe:
			name = "Next language"
		}
		if name == "" {
			return
		}
		it.key, it.name, it.value = "tool:"+strconv.Itoa(vkZoneIndex), name, "button"
	default:
		return
	}
	narrateMark(narrLayerOverlay, it)
}

// narrKeySymbols are the keys whose cap is a mark, by name.
var narrKeySymbols = map[string]string{
	".": "period", ",": "comma", "?": "question mark", "!": "exclamation mark", "@": "at sign",
	"#": "hash", "$": "dollar", "%": "percent", "&": "and sign", "*": "star", "(": "open bracket",
	")": "close bracket", "-": "dash", "_": "underscore", "+": "plus", "=": "equals", "/": "slash",
	"\\": "backslash", ":": "colon", ";": "semicolon", "'": "apostrophe", "\"": "quote", "~": "tilde",
	"<": "less than", ">": "greater than", "[": "open square bracket", "]": "close square bracket",
	"{": "open brace", "}": "close brace", "|": "bar", "^": "caret", "`": "backtick", "€": "euro",
	"£": "pound", "¥": "yen", "°": "degree", "§": "section sign", "•": "bullet",
	".com": "dot com", "#+=": "more symbols", "?123": "numbers",
}

// narrKeyName is a key as it is said. Pure apart from the keyboard's state.
func narrKeyName(key string) string {
	switch key {
	case "SPACE":
		return "space"
	case "BACK":
		return "delete"
	case "ENTER":
		return vkActionLabel()
	case "SEND":
		return "Send"
	case "ASK_AI":
		return "Ask AI"
	case "⇧":
		if vkCapsLock {
			return "caps lock"
		}
		return "shift"
	case "GLOBE":
		return "next language"
	case "123":
		return "numbers"
	case "ABC":
		return "letters"
	}
	if name, ok := narrKeySymbols[key]; ok {
		return name
	}
	if r, size := utf8.DecodeRuneInString(key); size == len(key) && unicode.IsLetter(r) {
		// A letter alone is said by its name before a full stop (narrPhrases
		// adds one through ttsSpeakable); a capital is called so.
		if unicode.IsUpper(r) && unicode.ToLower(r) != r {
			return "capital " + key
		}
		return key
	}
	return key
}

// ── Home, the top bar and the element screens ────────────────────────────

// narrHomeNames name Home's header controls, which are not tiles.
var narrHomeNames = map[HomeFocusID]string{
	HomeFocusClock: "Clock", HomeFocusWeather: "Weather", HomeFocusBell: "Notifications",
	HomeFocusBug: "Report a bug", HomeFocusWiFi: "Wi-Fi", HomeFocusVolume: "Volume",
	HomeFocusBluetooth: "Bluetooth", HomeFocusBrightness: "Brightness", HomeFocusNowPlaying: "Now playing",
	HomeFocusUpdate: "Update", HomeFocusSearch: "Search", HomeFocusPS5: "PS5 Remote Play",
	HomeFocusPS4: "PS4 Remote Play", HomeFocusX1: "Xbox Remote Play", HomeFocusPC: "PC streaming",
	HomeFocusExit: "Exit", HomeFocusSettings: "Settings", HomeFocusContinue: "Continue",
	HomeFocusEmulator: "Games",
}

// narrSceneItem is what is focused on a screen that marked nothing: Home's
// tile, or the focus engine's element. Main thread.
func narrSceneItem(c *Config) (narrItem, bool) {
	if c == nil || currentSceneIndex < 0 || currentSceneIndex >= len(c.Scenes) {
		return narrItem{}, false
	}
	scene := c.Scenes[currentSceneIndex].Name
	if homeLayoutActive && homeLayout != nil && homeLayout.Focus != nil {
		id := homeLayout.Focus.Current()
		if id == "" {
			return narrItem{}, false
		}
		it := narrItem{context: "Home", key: "home:" + string(id), hint: "Open"}
		if hi, ok := homeLayout.Items[id]; ok && strings.TrimSpace(hi.Title) != "" {
			it.name, it.value = hi.Title, strings.TrimSpace(hi.Subtitle)
		} else if name, ok := narrHomeNames[id]; ok {
			it.name = name
		} else {
			it.name = voiceScreenHomeTile(id)
		}
		if id == HomeFocusQR {
			it.name, it.hint = "Phone pairing code", homeFooterActionLabel(id)
		}
		if it.name == "" {
			return narrItem{}, false
		}
		return it, true
	}
	if focusEngine == nil || focusEngine.SceneName() != scene {
		return narrItem{}, false
	}
	n, ok := focusEngine.Current()
	if !ok || n.Label == "" || n.Label == n.Type {
		return narrItem{}, false
	}
	it := narrItem{context: sceneDisplayName(scene), key: "el:" + strconv.Itoa(n.Index) + "\x00" + n.Label, name: n.Label}
	switch n.Type {
	case "input":
		it.value, it.hint = "text field", "Edit"
	case "toggle":
		it.value = "switch"
	}
	return it, true
}

// narrChromeItem is the top bar's focused button, when the bar holds the
// focus (not on Home, which reads its own header). Main thread.
func narrChromeItem(c *Config) (narrItem, bool) {
	if c == nil || homeLayoutActive || focusEngine == nil || currentSceneIndex < 0 || currentSceneIndex >= len(c.Scenes) {
		return narrItem{}, false
	}
	if focusEngine.SceneName() != c.Scenes[currentSceneIndex].Name {
		return narrItem{}, false
	}
	n, ok := focusEngine.Current()
	if !ok || chromeTier(n.Index) != 1 || strings.TrimSpace(n.Label) == "" {
		return narrItem{}, false
	}
	hint := "Open"
	if n.Index == ChromeBackIndex {
		hint = "Go back"
	}
	return narrItem{context: "Top bar", key: "chrome:" + n.Type, name: n.Label, hint: hint}, true
}
