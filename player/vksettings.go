package main

// vksettings.go — Keyboard settings: the on-screen keyboard's languages,
// typing options and voice typing.
//
// The screen is a Go-owned scene that no shipped config defines, so
// vkEnsureSettingsScene adds it in memory at startup and on hot reload, like
// the Alarm scene. Settings opens it through vkOpenSettings; so does the
// keyboard itself (hold the globe, "Keyboard settings…").
//
// One list in Home's glass, in sections:
//
//	Languages     the enabled layouts in globe order: A uses one, L1/R1 move
//	              it, X removes it; "Add a language" lists the rest
//	Typing        word suggestions, learning new words, auto-capitals, key
//	              sounds, and forgetting the learned words
//	Voice typing  the Groq API key (shared with Settings → General), the
//	              speech language and whether a microphone was found
//	About         the dictionary's SCOWL licence
//
// Every change is written straight to the user config (vklayouts.go), so
// there is nothing to save and B just leaves.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// vkSettingsSceneName is the config name of the keyboard settings scene.
const vkSettingsSceneName = "KeyboardSettings"

// vkEnsureSettingsScene gives the keyboard settings a scene. The edit is
// in-memory and idempotent: the user's config file is never rewritten.
func vkEnsureSettingsScene(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == vkSettingsSceneName {
			return
		}
	}
	config.Scenes = append(config.Scenes, SceneConfig{Name: vkSettingsSceneName, Icon: "K"})
}

// vkOpenSettings opens the keyboard settings screen (languages, suggestions,
// voice typing). Settings links here; the keyboard's globe long-press too.
func vkOpenSettings(config *Config) {
	if config == nil {
		return
	}
	vkEnsureSettingsScene(config)
	idx := sceneIndexExact(config, vkSettingsSceneName)
	if idx < 0 {
		return
	}
	vkSet.focus, vkSet.scroll = -1, 0
	vkSet.sheet = vkSetSheetNone
	vkSet.clearArmedAt = 0
	vkMicProbeStart()
	vkLearnedEnsureLoaded()
	changeSceneTo(config, idx)
}

// ── Groq key ─────────────────────────────────────────────────────────────

// vkGroqKeyBuffer holds the key while it is typed or pasted; it is cleared
// when the keyboard closes.
var vkGroqKeyBuffer string

// vkOpenGroqKeyKeyboard opens the on-screen keyboard for the Groq API key. It
// starts empty: the stored key is a secret and is never shown.
func vkOpenGroqKeyKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetGroqKey
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	vkGroqKeyBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// vkSaveGroqKey stores a typed key in the setting Settings → General edits
// (groq_api_key). An empty submit keeps the old key rather than erasing it.
func vkSaveGroqKey(config *Config, key string) {
	key = strings.TrimSpace(key)
	if key == "" || config == nil {
		return
	}
	vkSetCustom(config, "groq_api_key", key)
	ShowToast("Groq key saved — voice typing is ready", ToastKindSuccess)
}

// vkGroqKeyHint shows the stored key's last four characters, never the key.
func vkGroqKeyHint() string {
	key := strings.TrimSpace(groqAPIKey())
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "••••"
	}
	return "••••" + key[len(key)-4:]
}

// ── Rows ─────────────────────────────────────────────────────────────────

type vkSetRowKind int

const (
	vkSetHeader vkSetRowKind = iota
	vkSetLanguage
	vkSetAddLanguage
	vkSetToggle
	vkSetClearWords
	vkSetGroqKey
	vkSetVoiceLang
	vkSetMic
	vkSetLicence
)

// vkSetRow is one row of the list.
type vkSetRow struct {
	kind   vkSetRowKind
	title  string
	detail string
	layout *vkLayout
	key    string // the setting a toggle writes
	on     bool
}

// vkSetSheet is a sheet over the list: the language picker or the licence.
type vkSetSheet int

const (
	vkSetSheetNone vkSetSheet = iota
	vkSetSheetAdd
	vkSetSheetLicence
)

// vkSetClearWindowMs is how long the first press on "Forget learned words"
// stays armed for the second.
const vkSetClearWindowMs = 3000

var vkSet struct {
	rows         []vkSetRow
	rowsAt       uint64
	rowRects     []sdl.Rect
	focus        int
	scroll       int32
	contentH     int32
	view         sdl.Rect
	sheet        vkSetSheet
	sheetFocus   int
	sheetScroll  int32
	sheetItems   []*vkLayout
	sheetDetail  []string
	sheetRects   []sdl.Rect
	sheetCard    sdl.Rect
	licence      []string
	clearArmedAt uint64
	buttons      []vkSetButton
	hintKind     int
	hintPlan     footerPlan
	hintPlanW    int32
	hintScale    float64
	hintPlanOK   bool
	hintKeys     hintStamp
}

// vkSetButton is a small click target on a language row (move up, move down,
// remove).
type vkSetButton struct {
	rect   sdl.Rect
	row    int
	action int // -1 up, +1 down, 0 remove
}

// vkLayoutPreview is the first letters of a layout's top row: "QWERTZ",
// "AZERTY", "ЙЦУКЕН".
func vkLayoutPreview(l *vkLayout) string {
	keys := vkLayoutRows(l)[0]
	var b strings.Builder
	for i, k := range keys {
		if i >= 6 {
			break
		}
		b.WriteString(vkUpperKey(l, k))
	}
	return b.String()
}

// vkSettingsRows builds the list from the current settings.
func vkSettingsRows() []vkSetRow {
	rows := make([]vkSetRow, 0, 24)
	enabled := vkEnabledLayouts()
	cur := vkCurrentLayout()
	rows = append(rows, vkSetRow{kind: vkSetHeader, title: "Languages"})
	for _, l := range enabled {
		detail := l.Name + " · " + vkLayoutPreview(l)
		if l == cur {
			detail += " · In use"
		}
		rows = append(rows, vkSetRow{kind: vkSetLanguage, title: l.Native, detail: detail, layout: l})
	}
	addDetail := strconv.Itoa(len(vkLayouts)-len(enabled)) + " more available · the globe key switches between them"
	if len(enabled) >= vkMaxEnabledLayouts {
		addDetail = "Up to " + strconv.Itoa(vkMaxEnabledLayouts) + " languages: remove one first"
	}
	rows = append(rows, vkSetRow{kind: vkSetAddLanguage, title: "Add a language", detail: addDetail})

	rows = append(rows, vkSetRow{kind: vkSetHeader, title: "Typing"})
	rows = append(rows,
		vkSetRow{kind: vkSetToggle, key: vkKeySuggestions, on: vkSuggestionsEnabled(),
			title: "Word suggestions", detail: "Completions and corrections above the keys"},
		vkSetRow{kind: vkSetToggle, key: vkKeyLearn, on: vkLearnEnabled(),
			title: "Learn new words", detail: "Names and slang you type are suggested next time"},
		vkSetRow{kind: vkSetToggle, key: vkKeyAutoCap, on: vkAutoCapEnabled(),
			title: "Auto-capitalization", detail: "A capital at the start of each sentence"},
		vkSetRow{kind: vkSetToggle, key: vkKeySounds, on: vkSoundsEnabled(),
			title: "Key sounds", detail: "A soft tick on every key"},
	)
	learned := vkLearnedCount()
	clearDetail := "No learned words yet"
	switch {
	case learned == 1:
		clearDetail = "1 learned word"
	case learned > 1:
		clearDetail = strconv.Itoa(learned) + " learned words"
	}
	if vkSet.clearArmedAt != 0 && sdl.GetTicks64() < vkSet.clearArmedAt+vkSetClearWindowMs {
		clearDetail = "Press again to forget them"
	}
	rows = append(rows, vkSetRow{kind: vkSetClearWords, title: "Forget learned words", detail: clearDetail})

	rows = append(rows, vkSetRow{kind: vkSetHeader, title: "Voice typing"})
	keyDetail := "Not set: voice typing needs a free key from console.groq.com"
	if hint := vkGroqKeyHint(); hint != "" {
		keyDetail = "Set " + hint + " · shared with Settings → General"
	}
	rows = append(rows, vkSetRow{kind: vkSetGroqKey, title: "Groq API key", detail: keyDetail})
	langDetail := "Match the keyboard's language"
	if !vkVoiceFollowsLayout() {
		langDetail = "Detect automatically"
	}
	rows = append(rows, vkSetRow{kind: vkSetVoiceLang, title: "Speech language", detail: langDetail, on: vkVoiceFollowsLayout()})
	micDetail := "Checking…"
	switch vkMic.Load() {
	case vkMicPresent:
		micDetail = "Found · press the mic on the keyboard to dictate"
	case vkMicAbsent:
		micDetail = "Not found · plug one in to dictate"
	}
	rows = append(rows, vkSetRow{kind: vkSetMic, title: "Microphone", detail: micDetail})

	rows = append(rows, vkSetRow{kind: vkSetHeader, title: "About"})
	rows = append(rows, vkSetRow{kind: vkSetLicence, title: "English dictionary",
		detail: "SCOWL word list by Kevin Atkinson · view the licence"})
	return rows
}

// vkSetFocusable reports whether a row takes the highlight.
func vkSetFocusable(r vkSetRow) bool { return r.kind != vkSetHeader }

// vkSetRefresh rebuilds the rows and keeps the highlight on a real row.
func vkSetRefresh() {
	vkSet.rows = vkSettingsRows()
	vkSet.rowsAt = sdl.GetTicks64()
	if vkSet.focus < 0 || vkSet.focus >= len(vkSet.rows) || !vkSetFocusable(vkSet.rows[vkSet.focus]) {
		vkSet.focus = vkSetNextFocusable(-1, 1)
	}
}

func vkSetNextFocusable(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(vkSet.rows); i += dir {
		if vkSetFocusable(vkSet.rows[i]) {
			return i
		}
	}
	if from >= 0 && from < len(vkSet.rows) {
		return from
	}
	return 0
}

// ── Actions ──────────────────────────────────────────────────────────────

// vkSetActivate is A (or a click) on row i.
func vkSetActivate(config *Config, i int) {
	if i < 0 || i >= len(vkSet.rows) {
		return
	}
	row := vkSet.rows[i]
	switch row.kind {
	case vkSetLanguage:
		vkSetCurrentLayout(config, row.layout.ID)
		PlayActivateSound()
		ShowToast(row.layout.Native+" is the keyboard's language now", ToastKindInfo)
	case vkSetAddLanguage:
		if len(vkEnabledLayouts()) >= vkMaxEnabledLayouts {
			PlayErrorSound()
			ShowToast("Up to "+strconv.Itoa(vkMaxEnabledLayouts)+" languages — remove one first", ToastKindInfo)
			return
		}
		vkSetOpenPicker()
		PlayActivateSound()
	case vkSetToggle:
		vkSetCustom(config, row.key, boolString(!row.on))
		vkSuggestState.valid = false
		PlayToggleSound()
	case vkSetClearWords:
		now := sdl.GetTicks64()
		if vkSet.clearArmedAt != 0 && now < vkSet.clearArmedAt+vkSetClearWindowMs {
			vkSet.clearArmedAt = 0
			vkLearnedClear()
			vkLearnedSaveAsync()
			PlayActivateSound()
			ShowToast("Learned words forgotten", ToastKindSuccess)
		} else {
			vkSet.clearArmedAt = now
			PlayNavSound()
		}
	case vkSetGroqKey:
		PlayActivateSound()
		vkOpenGroqKeyKeyboard()
	case vkSetVoiceLang:
		value := "auto"
		if !vkVoiceFollowsLayout() {
			value = "layout"
		}
		vkSetCustom(config, vkKeyVoiceLang, value)
		PlayToggleSound()
	case vkSetMic:
		vkMicProbeStart()
		PlayNavSound()
	case vkSetLicence:
		vkSetOpenLicence()
		PlayActivateSound()
	}
	vkSetRefresh()
}

// vkSetMoveLanguage moves the language on row i up (-1) or down (+1).
func vkSetMoveLanguage(config *Config, i, dir int) bool {
	if i < 0 || i >= len(vkSet.rows) || vkSet.rows[i].kind != vkSetLanguage {
		return false
	}
	enabled := append([]*vkLayout(nil), vkEnabledLayouts()...)
	pos := -1
	for j, l := range enabled {
		if l == vkSet.rows[i].layout {
			pos = j
		}
	}
	to := pos + dir
	if pos < 0 || to < 0 || to >= len(enabled) {
		return false
	}
	enabled[pos], enabled[to] = enabled[to], enabled[pos]
	vkSetEnabledLayouts(config, enabled)
	vkSetRefresh()
	vkSet.focus = i + dir
	PlayNavSound()
	return true
}

// vkSetRemoveLanguage removes the language on row i; the last one stays.
func vkSetRemoveLanguage(config *Config, i int) bool {
	if i < 0 || i >= len(vkSet.rows) || vkSet.rows[i].kind != vkSetLanguage {
		return false
	}
	enabled := vkEnabledLayouts()
	if len(enabled) <= 1 {
		PlayErrorSound()
		ShowToast("The keyboard needs at least one language", ToastKindInfo)
		return false
	}
	kept := make([]*vkLayout, 0, len(enabled))
	for _, l := range enabled {
		if l != vkSet.rows[i].layout {
			kept = append(kept, l)
		}
	}
	name := vkSet.rows[i].layout.Native
	vkSetEnabledLayouts(config, kept)
	vkSetRefresh()
	PlayBackSound()
	ShowToast(name+" removed", ToastKindInfo)
	return true
}

// vkSetOpenPicker lists the layouts not enabled yet.
func vkSetOpenPicker() {
	vkSet.sheetItems = vkSet.sheetItems[:0]
	vkSet.sheetDetail = vkSet.sheetDetail[:0]
	enabled := vkEnabledLayouts()
	for i := range vkLayouts {
		l := &vkLayouts[i]
		on := false
		for _, e := range enabled {
			if e == l {
				on = true
			}
		}
		if !on {
			vkSet.sheetItems = append(vkSet.sheetItems, l)
			vkSet.sheetDetail = append(vkSet.sheetDetail, l.Name+" · "+vkLayoutPreview(l))
		}
	}
	vkSet.sheet = vkSetSheetAdd
	vkSet.sheetFocus, vkSet.sheetScroll = 0, 0
}

// vkSetPick adds the picked layout at the end of the globe order.
func vkSetPick(config *Config, i int) {
	if i < 0 || i >= len(vkSet.sheetItems) {
		return
	}
	l := vkSet.sheetItems[i]
	enabled := append(append([]*vkLayout(nil), vkEnabledLayouts()...), l)
	vkSetEnabledLayouts(config, enabled)
	vkSet.sheet = vkSetSheetNone
	vkSetRefresh()
	for j, r := range vkSet.rows {
		if r.layout == l {
			vkSet.focus = j
		}
	}
	PlayActivateSound()
	ShowToast(l.Native+" added — the globe key switches to it", ToastKindSuccess)
}

// vkSetOpenLicence shows the SCOWL notice that travels with the word list.
func vkSetOpenLicence() {
	if vkSet.licence == nil {
		vkSet.licence = strings.Split(strings.ReplaceAll(vkDictNotice, "\r\n", "\n"), "\n")
	}
	vkSet.sheet = vkSetSheetLicence
	vkSet.sheetScroll = 0
}

// ── Input ────────────────────────────────────────────────────────────────

// vkSetMove moves the highlight (or scrolls a sheet).
func vkSetMove(dir int) {
	switch vkSet.sheet {
	case vkSetSheetAdd:
		n := len(vkSet.sheetItems)
		if n == 0 {
			return
		}
		vkSet.sheetFocus += dir
		if vkSet.sheetFocus < 0 {
			vkSet.sheetFocus = 0
		}
		if vkSet.sheetFocus >= n {
			vkSet.sheetFocus = n - 1
		}
		PlayNavSound()
		return
	case vkSetSheetLicence:
		vkSet.sheetScroll += int32(dir) * 48
		if vkSet.sheetScroll < 0 {
			vkSet.sheetScroll = 0
		}
		return
	}
	next := vkSetNextFocusable(vkSet.focus, dir)
	if next != vkSet.focus {
		vkSet.focus = next
		PlayNavSound()
	}
	vkSet.clearArmedAt = 0
}

// vkSetBack is B: close a sheet, else leave the screen.
func vkSetBack(config *Config) {
	if vkSet.sheet != vkSetSheetNone {
		vkSet.sheet = vkSetSheetNone
		PlayBackSound()
		return
	}
	PlayBackSound()
	goBackScene(config)
}

func vkSetHandleController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if vkSet.rows == nil {
		vkSetRefresh()
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		vkSetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		vkSetMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		switch vkSet.sheet {
		case vkSetSheetAdd:
			vkSetPick(config, vkSet.sheetFocus)
		case vkSetSheetLicence:
			vkSet.sheet = vkSetSheetNone
		default:
			vkSetActivate(config, vkSet.focus)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		vkSetBack(config)
	case sdl.CONTROLLER_BUTTON_X:
		if vkSet.sheet == vkSetSheetNone {
			vkSetRemoveLanguage(config, vkSet.focus)
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if vkSet.sheet == vkSetSheetNone {
			vkSetMoveLanguage(config, vkSet.focus, -1)
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if vkSet.sheet == vkSetSheetNone {
			vkSetMoveLanguage(config, vkSet.focus, 1)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		if vkSet.sheet == vkSetSheetNone {
			vkSetActivate(config, vkSetRowIndex(vkSetAddLanguage))
		}
	default:
		// Start and the rest keep their app-wide meaning (the Start menu).
		return false
	}
	return true
}

func vkSetHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if vkSet.rows == nil {
		vkSetRefresh()
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		vkSetMove(-1)
	case sdl.K_DOWN, sdl.K_TAB:
		vkSetMove(1)
	case sdl.K_PAGEUP:
		for i := 0; i < 5; i++ {
			vkSetMove(-1)
		}
	case sdl.K_PAGEDOWN:
		for i := 0; i < 5; i++ {
			vkSetMove(1)
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		switch vkSet.sheet {
		case vkSetSheetAdd:
			vkSetPick(config, vkSet.sheetFocus)
		case vkSetSheetLicence:
			vkSet.sheet = vkSetSheetNone
		default:
			vkSetActivate(config, vkSet.focus)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		vkSetBack(config)
	case sdl.K_DELETE, sdl.K_x:
		// X and Y do what the pad's X and Y do (vkSetHandleController). One
		// language per press: a held key's auto-repeat removed the next ones.
		if e.Repeat == 0 && vkSet.sheet == vkSetSheetNone {
			vkSetRemoveLanguage(config, vkSet.focus)
		}
	case sdl.K_y:
		if vkSet.sheet == vkSetSheetNone {
			vkSetActivate(config, vkSetRowIndex(vkSetAddLanguage))
		}
	case sdl.K_LEFTBRACKET:
		vkSetMoveLanguage(config, vkSet.focus, -1)
	case sdl.K_RIGHTBRACKET:
		vkSetMoveLanguage(config, vkSet.focus, 1)
	default:
		return false
	}
	return true
}

// vkSetRowIndex is the first row of a kind, or -1.
func vkSetRowIndex(kind vkSetRowKind) int {
	for i, r := range vkSet.rows {
		if r.kind == kind {
			return i
		}
	}
	return -1
}

// vkSettingsHandleClick answers a click or tap on the screen (main.go routes
// it while this scene is shown). It reports whether it was used.
func vkSettingsHandleClick(mx, my int32, config *Config) bool {
	if vkSet.rows == nil {
		return false
	}
	switch vkSet.sheet {
	case vkSetSheetAdd:
		for i, r := range vkSet.sheetRects {
			if pointInRect(mx, my, r) {
				vkSetPick(config, i)
				return true
			}
		}
		if !pointInRect(mx, my, vkSet.sheetCard) {
			vkSet.sheet = vkSetSheetNone
			PlayBackSound()
		}
		return true
	case vkSetSheetLicence:
		if !pointInRect(mx, my, vkSet.sheetCard) {
			vkSet.sheet = vkSetSheetNone
			PlayBackSound()
		}
		return true
	}
	for _, b := range vkSet.buttons {
		if hitTouchTarget(b.rect, mx, my) {
			vkSet.focus = b.row
			if b.action == 0 {
				vkSetRemoveLanguage(config, b.row)
			} else {
				vkSetMoveLanguage(config, b.row, b.action)
			}
			return true
		}
	}
	if !pointInRect(mx, my, vkSet.view) {
		return false
	}
	for i, r := range vkSet.rowRects {
		if i < len(vkSet.rows) && vkSetFocusable(vkSet.rows[i]) && r.H > 0 && pointInRect(mx, my, r) {
			vkSet.focus = i
			vkSetActivate(config, i)
			return true
		}
	}
	return false
}

// vkSettingsHandleWheel scrolls the list (or the licence) with the wheel.
func vkSettingsHandleWheel(dy int32) bool {
	if vkSet.sheet == vkSetSheetLicence {
		vkSet.sheetScroll -= dy * 48
		if vkSet.sheetScroll < 0 {
			vkSet.sheetScroll = 0
		}
		return true
	}
	if vkSet.sheet == vkSetSheetAdd {
		vkSetMove(int(-dy))
		return true
	}
	vkSet.scroll -= dy * 56
	vkSetClampScroll()
	return true
}

func vkSetClampScroll() {
	maxScroll := vkSet.contentH - vkSet.view.H
	if maxScroll < 0 {
		maxScroll = 0
	}
	if vkSet.scroll > maxScroll {
		vkSet.scroll = maxScroll
	}
	if vkSet.scroll < 0 {
		vkSet.scroll = 0
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// vkSetFonts are the screen's type sizes, resolved once per config.
var vkSetFonts struct {
	config *Config
	small  bool
	scale  float64
	title  *ttf.Font
	detail *ttf.Font
	badge  *ttf.Font
	body   *ttf.Font
}

func vkSetFontsFor(config *Config) {
	small := vkScreenSmall()
	if vkSetFonts.config == config && vkSetFonts.small == small && vkSetFonts.scale == a11y.fontScale && vkSetFonts.title != nil {
		return
	}
	size := func(big, compact int) int {
		if small {
			return compact
		}
		return big
	}
	vkSetFonts.config, vkSetFonts.small, vkSetFonts.scale = config, small, a11y.fontScale
	vkSetFonts.title = getFontSized(config, "small", size(19, 16))
	vkSetFonts.detail = getFontSized(config, "small", size(14, 12))
	vkSetFonts.badge = getFontSized(config, "small", size(13, 11))
	vkSetFonts.body = getFontSized(config, "small", size(15, 13))
}

// vkSettingsHintKind names the footer for what the highlight is on; the hint
// list itself is only built when this changes.
func vkSettingsHintKind() int {
	switch vkSet.sheet {
	case vkSetSheetAdd:
		return 1
	case vkSetSheetLicence:
		return 2
	}
	if vkSet.focus >= 0 && vkSet.focus < len(vkSet.rows) {
		switch vkSet.rows[vkSet.focus].kind {
		case vkSetLanguage:
			return 3
		case vkSetToggle, vkSetVoiceLang:
			return 4
		}
	}
	return 5
}

// vkSettingsHints is the footer for a hint kind.
func vkSettingsHints(kind int) []FooterHint {
	switch kind {
	case 1:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Add"}, {Button: "B", Label: "Close"}}
	case 2:
		return []FooterHint{{Button: "D-Pad", Label: "Scroll"}, {Button: "B", Label: "Close"}}
	case 3:
		// "/ [ ]": the keyboard moves a language with [ and ] (vkSetHandleKey).
		return []FooterHint{{Button: "A", Label: "Use"}, {Button: "B", Label: "Back"},
			{Button: "L1/R1 / [ ]", Label: "Move"}, {Button: "X", Label: "Remove"}, {Button: "Y", Label: "Add"}}
	case 4:
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Toggle"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
}

// renderKeyboardSettings draws the screen.
func renderKeyboardSettings(renderer *sdl.Renderer, config *Config) {
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	vkSetFontsFor(config)
	// The rows follow the settings, the mic probe and the learned count; they
	// are rebuilt after every action and otherwise twice a second, never per
	// frame (reading the key can mean decrypting it).
	if now := sdl.GetTicks64(); vkSet.rows == nil || vkSet.focus < 0 || now >= vkSet.rowsAt+500 {
		vkSetRefresh()
	}
	margin := scaleX(homeSideMargin, screenWidth)
	if vkScreenSmall() {
		margin = 16
	}
	top := headerBarHeight() + 10
	header := UIPanelHeader("Keyboard", "Languages, suggestions and voice typing", "keyboard")
	header.X, header.Y, header.W = margin, top, screenWidth-2*margin
	bodyTop := UIPageHeaderDraw(renderer, config, header) + 14
	band := sceneFooterBand()
	vkSet.view = sdl.Rect{X: margin, Y: bodyTop, W: screenWidth - 2*margin, H: band.Y - 10 - bodyTop}
	vkSetDrawList(renderer, config)
	vkSetDrawFooter(renderer, config, band)
	switch vkSet.sheet {
	case vkSetSheetAdd:
		vkSetDrawPicker(renderer, config)
	case vkSetSheetLicence:
		vkSetDrawLicence(renderer, config)
	}
	renderStatusBar(renderer, config)
	renderer.SetClipRect(nil)
}

// vkSetDrawList lays the rows out, keeps the highlighted one in view and
// draws what is visible.
func vkSetDrawList(renderer *sdl.Renderer, config *Config) {
	small := vkScreenSmall()
	rowH, headH, gap := int32(62), int32(40), int32(8)
	if small {
		rowH, headH, gap = int32(52), int32(34), int32(6)
	}
	if cap(vkSet.rowRects) < len(vkSet.rows) {
		vkSet.rowRects = make([]sdl.Rect, len(vkSet.rows))
	}
	vkSet.rowRects = vkSet.rowRects[:len(vkSet.rows)]
	y := int32(0)
	for i, r := range vkSet.rows {
		h := rowH
		if r.kind == vkSetHeader {
			h = headH
		}
		vkSet.rowRects[i] = sdl.Rect{X: vkSet.view.X, Y: y, W: vkSet.view.W, H: h}
		y += h + gap
	}
	vkSet.contentH = y
	// Keep the highlighted row (and its section heading) in view.
	if vkSet.focus >= 0 && vkSet.focus < len(vkSet.rowRects) {
		fr := vkSet.rowRects[vkSet.focus]
		top := fr.Y
		if vkSet.focus > 0 && vkSet.rows[vkSet.focus-1].kind == vkSetHeader {
			top = vkSet.rowRects[vkSet.focus-1].Y
		}
		if top < vkSet.scroll {
			vkSet.scroll = top
		}
		if fr.Y+fr.H > vkSet.scroll+vkSet.view.H {
			vkSet.scroll = fr.Y + fr.H - vkSet.view.H
		}
	}
	vkSetClampScroll()
	vkSet.buttons = vkSet.buttons[:0]
	clip := vkSet.view
	_ = renderer.SetClipRect(&clip)
	for i := range vkSet.rows {
		r := vkSet.rowRects[i]
		r.Y += vkSet.view.Y - vkSet.scroll
		vkSet.rowRects[i] = r
		if r.Y+r.H < vkSet.view.Y || r.Y > vkSet.view.Y+vkSet.view.H {
			continue
		}
		vkSetDrawRow(renderer, config, i, r)
	}
	_ = renderer.SetClipRect(nil)
	// A thin scrollbar says there is more below.
	if vkSet.contentH > vkSet.view.H && vkSet.view.H > 0 {
		trackH := vkSet.view.H
		thumbH := trackH * vkSet.view.H / vkSet.contentH
		if thumbH < 24 {
			thumbH = 24
		}
		thumbY := vkSet.view.Y + (trackH-thumbH)*vkSet.scroll/(vkSet.contentH-vkSet.view.H)
		x := vkSet.view.X + vkSet.view.W + 4
		fillRoundedRect(renderer, x, vkSet.view.Y, 3, trackH, 1, glassHairline())
		fillRoundedRect(renderer, x, thumbY, 3, thumbH, 1, glassA(90))
	}
}

// vkSetDrawRow draws one row of the list.
func vkSetDrawRow(renderer *sdl.Renderer, config *Config, i int, rect sdl.Rect) {
	row := vkSet.rows[i]
	if row.kind == vkSetHeader {
		UISectionLabel(renderer, config, row.title, rect.X, rect.Y+8, rect.W, nil)
		return
	}
	focused := i == vkSet.focus && vkSet.sheet == vkSetSheetNone
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	pad := int32(18)
	x := rect.X + pad
	right := rect.X + rect.W - pad
	accent := glassAccent()

	// The right-hand side first, so the text knows how much room is left.
	switch row.kind {
	case vkSetLanguage:
		// Move up, move down and remove, as tap targets; the pad does the
		// same with L1, R1 and X.
		size := rect.H - 22
		if size > 34 {
			size = 34
		}
		enabled := vkEnabledLayouts()
		pos := 0
		for j, l := range enabled {
			if l == row.layout {
				pos = j
			}
		}
		glyphs := [3]string{"×", "↓", "↑"}
		actions := [3]int{0, 1, -1}
		for k := 0; k < 3; k++ {
			b := sdl.Rect{X: right - size, Y: rect.Y + (rect.H-size)/2, W: size, H: size}
			usable := !(actions[k] == -1 && pos == 0) && !(actions[k] == 1 && pos == len(enabled)-1) &&
				!(actions[k] == 0 && len(enabled) <= 1)
			ink := ColorTextSecondary()
			if !usable {
				ink = WithAlpha(ColorTextTertiary(), 90)
			}
			fillRoundedRect(renderer, b.X, b.Y, b.W, b.H, b.H/2, glassA(14))
			strokeRoundedRect(renderer, b.X, b.Y, b.W, b.H, b.H/2, 1, glassA(30))
			if f := vkSetFonts.title; f != nil {
				gw, gh := measureText(f, glyphs[k])
				drawText(renderer, f, glyphs[k], b.X+(b.W-gw)/2, b.Y+(b.H-gh)/2-1, ink, textAlignLeft)
			}
			if usable {
				vkSet.buttons = append(vkSet.buttons, vkSetButton{rect: b, row: i, action: actions[k]})
			}
			right = b.X - 8
		}
		// The layout's code in a glass badge on the left, like a Home tile's.
		if f := vkSetFonts.badge; f != nil {
			code := row.layout.Short
			cw, ch := measureText(f, code)
			bw := cw + 18
			if bw < 40 {
				bw = 40
			}
			badge := sdl.Rect{X: x, Y: rect.Y + (rect.H-28)/2, W: bw, H: 28}
			fill := glassA(18)
			ink := ColorTextPrimary()
			if row.layout == vkCurrentLayout() {
				fill = accent
				ink = contrastText(accent)
			}
			fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, 8, fill)
			drawText(renderer, f, code, badge.X+(badge.W-cw)/2, badge.Y+(badge.H-ch)/2, ink, textAlignLeft)
			x += bw + 14
		}
	case vkSetToggle, vkSetVoiceLang:
		pillW, pillH := int32(76), int32(32)
		label := "OFF"
		fill, ink := ColorIconSurface, ColorTextSecondary()
		if row.kind == vkSetVoiceLang {
			pillW = 96
			label = "AUTO"
			if row.on {
				label = "KEYBOARD"
				fill, ink = accent, contrastText(accent)
			}
		} else if row.on {
			label = "ON"
			fill, ink = accent, contrastText(accent)
		}
		pill := sdl.Rect{X: right - pillW, Y: rect.Y + (rect.H-pillH)/2, W: pillW, H: pillH}
		fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pillH/2, fill)
		if f := vkSetFonts.badge; f != nil {
			lw, lh := measureText(f, label)
			drawText(renderer, f, label, pill.X+(pill.W-lw)/2, pill.Y+(pill.H-lh)/2, ink, textAlignLeft)
		}
		right = pill.X - 12
	case vkSetAddLanguage, vkSetGroqKey, vkSetLicence:
		icon := "plus"
		switch row.kind {
		case vkSetGroqKey:
			icon = "key"
		case vkSetLicence:
			icon = "info"
		}
		size := int32(24)
		drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: right - size, Y: rect.Y + (rect.H-size)/2, W: size, H: size}, ColorTextSecondary())
		right -= size + 12
	case vkSetMic:
		size := int32(24)
		ink := ColorTextSecondary()
		if vkMic.Load() == vkMicAbsent {
			ink = WithAlpha(ColorTextTertiary(), 120)
		}
		drawHomeGlyphTinted(renderer, "mic", sdl.Rect{X: right - size, Y: rect.Y + (rect.H-size)/2, W: size, H: size}, ink)
		right -= size + 12
	}

	title, detail := vkSetFonts.title, vkSetFonts.detail
	if title == nil {
		return
	}
	avail := right - x
	if avail < 40 {
		avail = 40
	}
	th := int32(title.Height())
	dh := int32(0)
	if detail != nil && row.detail != "" {
		dh = int32(detail.Height())
	}
	ty := rect.Y + (rect.H-th-dh-2)/2
	drawText(renderer, title, ellipsize(title, row.title, avail), x, ty, ColorTextPrimary(), textAlignLeft)
	if dh > 0 {
		ink := ColorTextSecondary()
		if row.kind == vkSetClearWords && vkSet.clearArmedAt != 0 {
			ink = WarningInk()
		}
		drawText(renderer, detail, ellipsize(detail, row.detail, avail), x, ty+th+2, ink, textAlignLeft)
	}
}

// vkSetDrawFooter is Home's footer band with the hints for the highlighted
// row. The plan is measured once per hint set, not per frame.
func vkSetDrawFooter(renderer *sdl.Renderer, config *Config, band sdl.Rect) {
	drawGlassFooterBand(renderer, band)
	kind := vkSettingsHintKind()
	pad := scaleX(16, screenWidth)
	avail := band.W - 2*pad
	// The plan's widths are measured in this frame's hint vocabulary and
	// remap (inputhints.go), so a switch between pad and keyboard, or a new
	// Accessibility remap, re-plans it.
	keys := hintStampNow()
	if !vkSet.hintPlanOK || vkSet.hintKind != kind || vkSet.hintPlanW != avail || vkSet.hintScale != a11y.fontScale || vkSet.hintKeys != keys {
		vkSet.hintPlan = planFooterHints(config, vkSettingsHints(kind), avail)
		vkSet.hintKind, vkSet.hintPlanW, vkSet.hintScale, vkSet.hintPlanOK, vkSet.hintKeys = kind, avail, a11y.fontScale, true, keys
	}
	plan := vkSet.hintPlan
	if plan.font == nil {
		return
	}
	x := band.X + band.W - pad - plan.width
	textY := band.Y + (band.H-plan.fontHeight)/2
	badgeY := band.Y + (band.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
}

// vkSetSheetCard is the shared card geometry of the two sheets.
func vkSetSheetCard() sdl.Rect {
	w := int32(620)
	if w > screenWidth-40 {
		w = screenWidth - 40
	}
	h := screenHeight - headerBarHeight() - 70
	if h > 560 {
		h = 560
	}
	return sdl.Rect{X: (screenWidth - w) / 2, Y: headerBarHeight() + (screenHeight-headerBarHeight()-h)/2, W: w, H: h}
}

// vkSetDrawPicker is the "Add a language" sheet.
func vkSetDrawPicker(renderer *sdl.Renderer, config *Config) {
	drawOverlayScrim(renderer)
	card := vkSetSheetCard()
	vkSet.sheetCard = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, "Add a language", "Latin, Cyrillic and Greek keyboards", "language")
	top := UIPageHeaderDraw(renderer, config, header) + 12
	rowH, gap := int32(56), int32(6)
	view := sdl.Rect{X: card.X + OverlayPadX, Y: top, W: card.W - 2*OverlayPadX, H: card.Y + card.H - OverlayPadX - top}
	if len(vkSet.sheetItems) == 0 {
		UIEmptyState(renderer, config, view, "@check", "Every language is on", "Remove one to add it back later.")
		vkSet.sheetRects = vkSet.sheetRects[:0]
		return
	}
	focusY := int32(vkSet.sheetFocus) * (rowH + gap)
	if focusY < vkSet.sheetScroll {
		vkSet.sheetScroll = focusY
	}
	if focusY+rowH > vkSet.sheetScroll+view.H {
		vkSet.sheetScroll = focusY + rowH - view.H
	}
	if cap(vkSet.sheetRects) < len(vkSet.sheetItems) {
		vkSet.sheetRects = make([]sdl.Rect, len(vkSet.sheetItems))
	}
	vkSet.sheetRects = vkSet.sheetRects[:len(vkSet.sheetItems)]
	clip := view
	_ = renderer.SetClipRect(&clip)
	for i, l := range vkSet.sheetItems {
		r := sdl.Rect{X: view.X, Y: view.Y + int32(i)*(rowH+gap) - vkSet.sheetScroll, W: view.W, H: rowH}
		vkSet.sheetRects[i] = r
		if r.Y+r.H < view.Y || r.Y > view.Y+view.H {
			vkSet.sheetRects[i] = sdl.Rect{}
			continue
		}
		UIListRow(renderer, r, glassRowRadius, i == vkSet.sheetFocus, false)
		x := r.X + 16
		if f := vkSetFonts.badge; f != nil {
			cw, ch := measureText(f, l.Short)
			fillRoundedRect(renderer, x, r.Y+(r.H-28)/2, 40, 28, 8, glassA(18))
			drawText(renderer, f, l.Short, x+(40-cw)/2, r.Y+(r.H-ch)/2, ColorTextPrimary(), textAlignLeft)
			x += 54
		}
		if f := vkSetFonts.title; f != nil {
			th := int32(f.Height())
			dh := int32(0)
			if vkSetFonts.detail != nil {
				dh = int32(vkSetFonts.detail.Height())
			}
			ty := r.Y + (r.H-th-dh-2)/2
			drawText(renderer, f, ellipsize(f, l.Native, r.X+r.W-16-x), x, ty, ColorTextPrimary(), textAlignLeft)
			if vkSetFonts.detail != nil && i < len(vkSet.sheetDetail) {
				drawText(renderer, vkSetFonts.detail, ellipsize(vkSetFonts.detail, vkSet.sheetDetail[i], r.X+r.W-16-x),
					x, ty+th+2, ColorTextSecondary(), textAlignLeft)
			}
		}
	}
	_ = renderer.SetClipRect(nil)
}

// vkSetDrawLicence is the SCOWL notice in a scrolling sheet.
func vkSetDrawLicence(renderer *sdl.Renderer, config *Config) {
	drawOverlayScrim(renderer)
	card := vkSetSheetCard()
	card.W += 120
	if card.W > screenWidth-40 {
		card.W = screenWidth - 40
	}
	card.X = (screenWidth - card.W) / 2
	vkSet.sheetCard = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, "Dictionary licence", "SCOWL / Hunspell en_US word list", "info")
	top := UIPageHeaderDraw(renderer, config, header) + 10
	font := vkSetFonts.detail
	if font == nil {
		return
	}
	view := sdl.Rect{X: card.X + OverlayPadX, Y: top, W: card.W - 2*OverlayPadX, H: card.Y + card.H - OverlayPadX - top}
	lineH := int32(font.Height()) + 3
	maxScroll := int32(len(vkSet.licence))*lineH - view.H
	if maxScroll < 0 {
		maxScroll = 0
	}
	if vkSet.sheetScroll > maxScroll {
		vkSet.sheetScroll = maxScroll
	}
	clip := view
	_ = renderer.SetClipRect(&clip)
	first := int(vkSet.sheetScroll / lineH)
	for i := first; i < len(vkSet.licence); i++ {
		y := view.Y + int32(i)*lineH - vkSet.sheetScroll
		if y > view.Y+view.H {
			break
		}
		if line := strings.TrimRight(vkSet.licence[i], " "); line != "" {
			drawText(renderer, font, ellipsize(font, line, view.W), view.X, y, ColorTextSecondary(), textAlignLeft)
		}
	}
	_ = renderer.SetClipRect(nil)
}

// ── Scene ────────────────────────────────────────────────────────────────

type vkSettingsScene struct{ sceneBase }

func (vkSettingsScene) render(r *sdl.Renderer, c *Config) bool {
	renderKeyboardSettings(r, c)
	return true
}

func (vkSettingsScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return vkSetHandleKey(e, c)
}

func (vkSettingsScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return vkSetHandleController(e, c)
}

func init() { registerScene(vkSettingsScene{sceneBase{name: vkSettingsSceneName}}) }
