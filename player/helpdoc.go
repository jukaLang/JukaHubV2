package main

// helpdoc.go — the on-device manual and Home's tip of the day (backlog
// SHELL-9).
//
// Settings → About → Quick start and Controls open docs/GETTING_STARTED.md and
// docs/CONTROLS.md in the Markdown reader Security & trust uses for
// SECURITY.md (sectrustdoc.go): headings, tables, a contents list on Y,
// paging on the shoulders, and B back to Settings. Both pages are embedded,
// so they read offline. go:embed cannot reach ../docs, so player/helpdocs/
// holds copies (LF). The build steps that make a binary (player/build.bat,
// the Makefile's build targets, .github/scripts/trimui-build.sh) refresh them
// from docs/ first with
//
//	tr -d '\r' < docs/GETTING_STARTED.md > player/helpdocs/GETTING_STARTED.md
//	tr -d '\r' < docs/CONTROLS.md > player/helpdocs/CONTROLS.md
//
// and the committed copies are the fallback for a plain `go build`.
//
// Home shows one tip a day as a line above the footer (Standard, Hero and
// Tabs layouts: homeTipDraw is called from HomeLayout.drawFooter). The tips
// are a fixed list naming real controls from CONTROLS.md, the buttons in the
// current hint vocabulary (inputhints.go). Each tip says where it is true
// (helpTipFit): one that names a stick or a pad chord is skipped while the
// hints name keyboard keys, one about the handheld's Power button is skipped
// on a PC. The day number picks among the tips that fit, so two days in a
// row never show the same tip. Settings → About → "Show tips" turns them
// off; "Hide today's tip" dismisses the current one.

import (
	_ "embed"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

//go:embed helpdocs/GETTING_STARTED.md
var helpQuickStartMD string

//go:embed helpdocs/CONTROLS.md
var helpControlsMD string

const (
	helpDocsURL = "https://github.com/jukaLang/JukaHubV2/blob/main/docs/"
	// helpTipsSetting is the "Show tips" switch (on unless switched off).
	helpTipsSetting = "homeTips"
	// helpTipHiddenSetting is the day whose tip was dismissed (bookkeeping).
	helpTipHiddenSetting = "homeTipHiddenDay"
)

// The two Help pages. back returns to the screen that opened them.
var (
	helpQuickStartSpec = mdDocSpec{
		name: "Quick start",
		sub:  "Find your way around JukaHub (docs/GETTING_STARTED.md)",
		icon: "info",
		url:  helpDocsURL + "GETTING_STARTED.md",
		load: func() (string, string) { return helpQuickStartMD, "embedded" },
		back: goBackScene,
	}
	helpControlsSpec = mdDocSpec{
		name: "Controls",
		sub:  "Every button on every screen (docs/CONTROLS.md)",
		icon: "gamepad",
		url:  helpDocsURL + "CONTROLS.md",
		load: func() (string, string) { return helpControlsMD, "embedded" },
		back: goBackScene,
	}
)

// helpPending is a Help page asked for while the reader's screen was not up
// yet: the Security & trust screen resets its reader on a fresh visit, then
// takes this (helpDocTakePending). Main thread.
var helpPending struct {
	doc     *mdDocSpec
	section string
}

// helpDocOpen shows doc in the reader, at section when it names a heading.
// Main thread.
func helpDocOpen(config *Config, doc *mdDocSpec, section string) {
	helpPending.doc, helpPending.section = doc, section
	if !tbxOpenScene(config, sectrustSceneName) {
		helpPending.doc = nil
		ShowToast("Help could not be opened", ToastKindError)
		return
	}
	PlayActivateSound()
}

// helpDocTakePending opens the page helpDocOpen asked for. The Security &
// trust screen calls it every frame, after its fresh-visit reset.
func helpDocTakePending() {
	if helpPending.doc == nil {
		return
	}
	doc, section := helpPending.doc, helpPending.section
	helpPending.doc, helpPending.section = nil, ""
	mdDocOpen(doc, section)
}

// helpOpenQuickStart and helpOpenControls are the Settings rows' actions.
func helpOpenQuickStart(c *Config) { helpDocOpen(c, &helpQuickStartSpec, "") }
func helpOpenControls(c *Config)   { helpDocOpen(c, &helpControlsSpec, "") }

// ── Tips ─────────────────────────────────────────────────────────────────

// helpTipFit says where a tip is true. Zero is "keyboard-OK": every control
// the tip names is a key, or a pad button whose key twin the hint vocabulary
// shows (written "[L2 / Q]" where only the screen knows the key).
type helpTipFit uint8

const (
	tipKeyboardOK helpTipFit = 0      // true with a pad and with a keyboard
	tipPadOnly    helpTipFit = 1 << 0 // a stick, a pad chord or a pad-only button
	tipHandheld   helpTipFit = 1 << 1 // the TrimUI's Power button, sleep, its radios
)

// helpTip is one tip: text with [Button] or [Button|label] tokens put in the
// current hint vocabulary (label is the action, which decides the key twin
// of a shoulder or Start), the CONTROLS.md section A opens, and where it fits.
type helpTip struct {
	text    string
	section string
	fit     helpTipFit
}

// helpTips is the bounded list (taken from CONTROLS.md; every control named
// there is real). A chord is one token, "[Select + L1]", so the hint
// vocabulary sees the whole chord: the quit and mouse-mode chords keep their
// physical names under an Accessibility remap, and the quit chord becomes the
// PC's Alt+F4.
var helpTips = [...]helpTip{
	{"Press [Start|Start menu] anywhere for the Start menu: screens, games, tools and the device", "Start menu", tipKeyboardOK},
	{"[B] goes back one screen; [Select] falls back to Home", "Everywhere", tipPadOnly},
	{"To quit JukaHub: [Hold Select + Start]", "Everywhere", tipKeyboardOK},
	{"[L1 / R1|Page] page through the focused list; [L2|top] jumps to its top", "Everywhere", tipKeyboardOK},
	{"[X] is the second action on most screens: delete, toggle or search", "Everywhere", tipKeyboardOK},
	{"[Y] creates, saves or submits — it is the third action on most screens", "Everywhere", tipKeyboardOK},
	{"The right stick moves a virtual cursor; [L2 / R2|scroll] scroll under it", "Virtual cursor", tipPadOnly},
	{"Hold [Select + L1] to turn mouse mode on or off", "Mouse mode (hold Select + L1)", tipPadOnly},
	{"A short press of Power puts the handheld to sleep — a game carries on at wake", "Everywhere", tipHandheld},
	{"Hold Power for 1.5 s for the power card: sleep, restart, power off or exit", "Everywhere", tipHandheld},
	{"In a game, hold [Select + L2] for the in-game menu: save states, rewind, sleep", "In-game menu (hold Select + L2)", tipPadOnly | tipHandheld},
	{"With Game assistant on, hold [Select + R2] in a game to ask Juka about it", "Ask Juka in a game (hold Select + R2)", tipPadOnly | tipHandheld},
	{"On Home, [L2 / Q] opens Edit Home; [L2 / Q] again is Done", "Edit Home", tipKeyboardOK},
	{"On Home, [R2 / W] steps the liquid background: Off, Slight, Medium, Lively", "Liquid background", tipKeyboardOK},
	{"With the liquid background on, tilt the left stick to stir the water round the focused tile", "Liquid background", tipPadOnly},
	{"Home's power button asks before quitting — Exit is the focused answer", "The power card", tipKeyboardOK},
	{"Favourites keep the games and apps you open most one press from Home", "Favorites", tipKeyboardOK},
	{"In the video player, [L1/R1 / Shift+←→] skip 10 s back or forward", "Video player", tipKeyboardOK},
	{"In the video player, [L2/R2 / P/N] go to the previous or next video", "Video player", tipKeyboardOK},
	{"The on-screen keyboard opens wherever text is typed — no keyboard needed", "On-screen keyboard", tipPadOnly},
	{"Settings → About → Replay the welcome tour shows the first-run walkthrough again", "First-run tour", tipKeyboardOK},
	{"Save manager backs up and restores your game saves", "Save manager", tipKeyboardOK},
	{"Downloads shows every file JukaHub is fetching; [X] removes one", "Downloads", tipKeyboardOK},
	{"Files moves, copies and deletes on the card; delete asks for a second press", "Files", tipKeyboardOK},
	{"The Wi-Fi picker joins a network; forgetting one asks first", "Wi-Fi picker", tipKeyboardOK},
	{"Bluetooth devices pairs pads and headphones from Nearby", "Bluetooth devices", tipHandheld},
	{"Watch Party plays one video on several handhelds together", "Watch Party", tipKeyboardOK},
	{"Screen Share shows this screen on a phone or PC on the same Wi-Fi", "Screen Share", tipKeyboardOK},
	{"Controller test shows every button, stick and trigger as you press it", "Controller test", tipPadOnly},
}

// helpTipFits reports whether a tip is true on this device in this
// vocabulary.
func helpTipFits(fit helpTipFit, keyboard, handheld bool) bool {
	if keyboard && fit&tipPadOnly != 0 {
		return false
	}
	return handheld || fit&tipHandheld == 0
}

// helpTipPick is the tip shown on day among those that fit, or -1 when none
// does. Consecutive days pick consecutive entries of the fitting list, so
// they always differ.
func helpTipPick(day int, keyboard, handheld bool) int {
	var fitting [len(helpTips)]uint8
	n := 0
	for i := range helpTips {
		if helpTipFits(helpTips[i].fit, keyboard, handheld) {
			fitting[n] = uint8(i)
			n++
		}
	}
	if n == 0 {
		return -1
	}
	i := day % n
	if i < 0 {
		i += n
	}
	return int(fitting[i])
}

// helpTipsEnabled reads the "Show tips" switch (main thread): on unless
// switched off.
func helpTipsEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	if v, ok := c.Variables.Custom[helpTipsSetting]; ok {
		return coerceBool(v)
	}
	return true
}

// helpSetTipsEnabled stores the switch (main thread).
func helpSetTipsEnabled(c *Config, on bool) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[helpTipsSetting] = on
	saveUserConfigDebounced(userConfigFromVariables(c))
	homeTip.key = -1
}

// helpTipDay is today's day number in local time; the tip index follows it,
// so consecutive days always differ.
func helpTipDay(now time.Time) int {
	y, m, d := now.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// helpTipKey names one token's control in the current vocabulary. tok is
// "Button" or "Button|label". "Hold" before the quit chord is dropped where the
// chord becomes the PC's Alt+F4, which is pressed, not held.
func helpTipKey(tok string) string {
	button, label, _ := strings.Cut(tok, "|")
	if rest, ok := strings.CutPrefix(button, "Hold "); ok && hintIsQuitChord(rest) {
		k := hintKeyFor(rest, label)
		if !hintIsQuitChord(k) {
			return k
		}
		return "Hold " + k
	}
	return hintKeyFor(button, label)
}

// helpTipRender puts a tip's [Button] tokens in the current vocabulary. It
// runs when the tip changes, never per frame.
func helpTipRender(t helpTip) string {
	var b strings.Builder
	s := t.text
	for {
		open := strings.IndexByte(s, '[')
		if open < 0 {
			break
		}
		end := strings.IndexByte(s[open:], ']')
		if end < 0 {
			break
		}
		b.WriteString(s[:open])
		b.WriteString(helpTipKey(s[open+1 : open+end]))
		s = s[open+end+1:]
	}
	b.WriteString(s)
	return b.String()
}

// homeTip caches the line Home draws, rebuilt only when the day, the hint
// vocabulary, the Accessibility remap, the switch, the width or the font
// changes — never per frame.
var homeTip struct {
	key     int    // day*2 + keyboard; -1 forces a rebuild
	remap   uint32 // generation of the Accessibility remap the text names
	checkAt uint64
	show    bool
	index   int
	text    string
	fitW    int32     // -1 forces fitted and tw to be measured again
	fitFont *ttf.Font // the font fitted and tw were measured with
	fitted  string
	tw      int32
	pill    sdl.Rect
}

func init() {
	homeTip.key = -1
	// The dismissed day is written by the app, not chosen by the player: the
	// settings reference must not offer it as an editable variable.
	cfgdocRuntimeKeys[helpTipHiddenSetting] = true
}

// helpTipHiddenDay is the day whose tip was dismissed (main thread).
func helpTipHiddenDay(c *Config) int {
	if c == nil {
		return -1
	}
	if v, ok := c.Variables.Custom[helpTipHiddenSetting]; ok {
		if n, ok := coerceInt(v); ok {
			return n
		}
	}
	return -1
}

// helpHideTodaysTip dismisses today's tip; tomorrow brings the next one.
func helpHideTodaysTip(c *Config) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[helpTipHiddenSetting] = helpTipDay(time.Now())
	saveUserConfigDebounced(userConfigFromVariables(c))
	homeTip.key = -1
	setToast("Today's tip is hidden — a new one comes tomorrow")
}

// helpTipsDetail is the line under the Settings switch.
func helpTipsDetail(c *Config) string {
	if !helpTipsEnabled(c) {
		return "Off: Home shows no tip"
	}
	return "One a day above Home's footer, never the same two days running"
}

// homeTipRefresh re-decides the tip at most once a second (main thread).
func homeTipRefresh() {
	nowMS := uiClockMS()
	if homeTip.key >= 0 && nowMS < homeTip.checkAt {
		return
	}
	homeTip.checkAt = nowMS + 1000
	day := helpTipDay(time.Now())
	keyboard := hintsUseKeyboard()
	kb := 0
	if keyboard {
		kb = 1
	}
	key := day*2 + kb
	remap := uint32(0)
	if r := hintRemapNow.Load(); r != nil {
		remap = r.gen
	}
	if key == homeTip.key && remap == homeTip.remap {
		return
	}
	homeTip.key, homeTip.remap = key, remap
	homeTip.show = helpTipsEnabled(appConfig) && helpTipHiddenDay(appConfig) != day
	homeTip.index = helpTipPick(day, keyboard, hintOnHandheld())
	if homeTip.index < 0 {
		homeTip.show = false
		homeTip.text = ""
	} else {
		homeTip.text = "Tip · " + helpTipRender(helpTips[homeTip.index])
	}
	homeTip.fitW = -1
}

// homeTipDraw draws today's tip as a line just above Home's footer band.
// Called from HomeLayout.drawFooter (homelayout.go) with the band's rect.
func homeTipDraw(renderer *sdl.Renderer, fonts HomeFonts, footer sdl.Rect, width, height int32) {
	homeTip.pill = sdl.Rect{}
	if fonts.Small == nil || homeEditActive() {
		return
	}
	homeTipRefresh()
	if !homeTip.show {
		homeTipFocusable(false)
		return
	}
	fh := int32(fonts.Small.Height())
	padX := scaleX(12, width)
	maxW := footer.W / 2
	// Measured once per text, width and font: a text-size change swaps
	// fonts.Small, and the ellipsis and width follow it.
	if homeTip.fitW != maxW || homeTip.fitFont != fonts.Small {
		homeTip.fitW, homeTip.fitFont = maxW, fonts.Small
		homeTip.fitted = ellipsize(fonts.Small, homeTip.text, maxW-2*padX)
		homeTip.tw, _ = measureText(fonts.Small, homeTip.fitted)
	}
	pill := sdl.Rect{X: footer.X, W: homeTip.tw + 2*padX, H: fh + scaleY(8, height)}
	pill.Y = footer.Y - pill.H - scaleY(6, height)
	if pill.Y < 0 {
		homeTipFocusable(false)
		return
	}
	homeTip.pill = pill
	homeTipFocusable(true)
	fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	strokeRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, 1, homeGlass(30))
	drawText(renderer, fonts.Small, homeTip.fitted, pill.X+padX, pill.Y+(pill.H-fh)/2, ColorTextMuted(), textAlignLeft)
}

// homeTipOpen opens the Help page today's tip comes from (a tap on the tip).
func homeTipOpen(config *Config) {
	if homeTip.index < 0 || homeTip.index >= len(helpTips) {
		return
	}
	helpDocOpen(config, &helpControlsSpec, helpTips[homeTip.index].section)
}

// homeTipClick handles a click or tap at (mx, my) on Home: the tip opens its
// Help page. It reports whether the tip took it.
func homeTipClick(config *Config, mx, my int32) bool {
	if !homeTipVisible() || !pointInRect(mx, my, homeTip.pill) {
		return false
	}
	homeTipOpen(config)
	return true
}

// homeTipVisible reports whether Home drew the tip this frame, which is also
// when its focus stop exists.
func homeTipVisible() bool {
	return homeTip.show && homeTip.pill.W > 0
}

// homeTipFocusable adds or removes the tip's stop in the Home focus graph.
// Home calls it from the draw that knows whether the line fitted.
func homeTipFocusable(on bool) {
	if homeLayout != nil {
		homeLayout.Focus.SetEnabled(HomeFocusTip, on)
	}
}
