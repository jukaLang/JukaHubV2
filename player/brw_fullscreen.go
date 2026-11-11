package main

// brw_fullscreen.go — the Browser drawn edge to edge.
//
// The Browser used to frame its page with chrome on every side: the shared top
// bar, a header band, the address toolbar, a cookie strip under the page, a row
// of fourteen chips and the footer. On the handheld's 720-line panel that left
// the page a little over half the screen, and the pages a reader opens a
// browser for (articles, issues, docs) are the tallest thing in the app. The
// WebKit browser people run on the same device (WPE for the Smart Pro) draws
// the page full screen and keeps everything else behind Select; this does the
// same.
//
// Full screen is the default (Settings → Browser → Full-screen pages, Custom
// key browserFullScreen). The page element then covers the whole canvas, the
// scene draws neither the top bar nor the footer (core.go renderScene), and the
// renderer skips its header band, toolbar and cookie strip (textbrowser.go).
// What is left on the page is a small status stack in the top-right corner: a
// pill naming the site for a moment after a page lands (and while the pointer
// is near the top), the find count while a search runs, and "Link mode" while
// the D-pad walks links. Select — M or the Menu key on a keyboard, a right
// click with a mouse, a click on the pill — opens the menu that holds every
// action the toolbar and the chips had (brw_menu.go).
//
// Off, the classic layout comes back unchanged: the bars, the toolbar and the
// chip row, with Select opening the same menu.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// brwFullScreenKey is the Custom key of the layout switch: true, the default,
// draws the page edge to edge.
const brwFullScreenKey = "browserFullScreen"

// browserSceneDescription is the description of the Browser scene JukaHub
// injects (ensureBrowserScene). It also tells that scene apart from a Browser
// scene a config ships itself, which keeps its own layout: the full-screen
// switch only ever rebuilds the injected one.
const browserSceneDescription = "Web browser with GitHub shortcuts"

// brwFullScreenOn is the layout the Browser scene was built with. Only the
// scene builder sets it (browserSceneElements), so the geometry below and the
// element it measures always agree. Main thread.
var brwFullScreenOn = true

const (
	// brwPillHold is how long the site pill stays up after a page lands, in ms.
	brwPillHold = 2500
	// brwPointerHold is how long it stays up after the pointer moved near the
	// top of the screen, in ms.
	brwPointerHold = 3000
	// brwPointerBand is how close to the top the pointer has to move to bring
	// the pill back: the edge a full-screen browser reveals its toolbar from.
	brwPointerBand = int32(120)
	// brwStatusInset is the status stack's distance from the screen's edges.
	brwStatusInset = int32(12)
	// brwStatusGap is the space between two entries of the stack.
	brwStatusGap = int32(6)
)

// brwFSState is the full-screen page's status stack: the page the site pill
// last announced and when, when the pointer last moved near the top (and where
// it was, to tell a move from a still frame), and the rects the last frame drew
// the pill and the two chips at, for the mouse. A zero rect was not drawn.
type brwFSState struct {
	url       string
	shownAt   uint64
	pointerAt uint64
	mouseX    int32
	mouseY    int32
	pill      sdl.Rect
	find      sdl.Rect
	mode      sdl.Rect
}

var brwFS brwFSState

// brwFullScreenSetting reads the layout switch from a config: on unless a
// stored value says off. It takes the shapes a hand-edited or builder-made file
// stores (a bool, "false"/"off"/"no"/"0", a number) under any key casing.
func brwFullScreenSetting(c *Config) bool {
	if c == nil || c.Variables.Custom == nil {
		return true
	}
	v, ok := customValue(c.Variables.Custom, brwFullScreenKey)
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return true
}

// brwFullscreenElem reports whether elem is the Browser's page drawn full
// screen. Only the injected Browser scene's page qualifies: a web panel that a
// config places on a screen of its own keeps its frame.
func brwFullscreenElem(elem Element) bool {
	return brwFullScreenOn && elem.Variable == browserContentVariable &&
		textBrowserElementSource(elem) == textBrowserSourceWeb
}

// brwFullscreenScene reports whether scene is the Browser drawn full screen.
func brwFullscreenScene(scene SceneConfig) bool {
	return brwFullScreenOn && scene.Name == browserSceneName
}

// brwFullscreenSceneUp reports whether the full-screen Browser is on screen.
func brwFullscreenSceneUp(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		brwFullscreenScene(config.Scenes[currentSceneIndex])
}

// brwFullscreenElements is the full-screen layout: the page alone, covering
// the canvas.
func brwFullscreenElements(width, height int32) []Element {
	return []Element{{
		Type:     "textbrowser",
		Text:     "web",
		Color:    "#0c0f16",
		Width:    StringOrInt(strconv.Itoa(int(width))),
		Height:   StringOrInt(strconv.Itoa(int(height))),
		Variable: browserContentVariable,
		Font:     "small",
	}}
}

// brwApplyLayout rebuilds the Browser scene for the layout now stored. On
// screen the focus graph is rebuilt with it and lands on the page, which is
// element 0 in both layouts, and the old layout's hit areas are dropped.
func brwApplyLayout(config *Config) {
	if config == nil {
		return
	}
	idx := sceneIndexExact(config, browserSceneName)
	if idx < 0 {
		brwFullScreenOn = brwFullScreenSetting(config)
		return
	}
	// A Browser scene the config ships itself is left as it was authored.
	if config.Scenes[idx].Description != browserSceneDescription {
		return
	}
	config.Scenes[idx].Elements = browserSceneElements(config)
	brwFS.pill, brwFS.find, brwFS.mode = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	browserCookieStripHit = sdl.Rect{}
	if idx != currentSceneIndex {
		return
	}
	scene := config.Scenes[idx]
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	focusEngine.SetByElementIndex(0)
	selectedButtonIndex = 0
	hoveredButtonIndex = -1
	pressedButtonIndex = -1
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
}

// brwSceneIsOurs reports whether the Browser scene is the one JukaHub injects,
// or there is none yet: only that one follows the full-screen switch, so the
// switch is offered only then.
func brwSceneIsOurs(c *Config) bool {
	idx := sceneIndexExact(c, browserSceneName)
	return idx < 0 || c.Scenes[idx].Description == browserSceneDescription
}

// brwSetFullScreen stores the layout switch and rebuilds the scene to match.
func brwSetFullScreen(config *Config, on bool) {
	if config == nil {
		return
	}
	setStoreBool(config, brwFullScreenKey, on)
	brwApplyLayout(config)
}

// brwFullScreenSettingsRow is Settings → Browser → Full-screen pages.
func brwFullScreenSettingsRow() setRow {
	return setRow{
		ID: "browserFullScreen", Label: "Full-screen pages", Kind: setKindToggle,
		Detail: func(*Config) string {
			return "The page fills the screen; " + hintKeyFor("Select / M", "Menu") + " opens the menu with every action"
		},
		Visible: brwSceneIsOurs,
		Get:     brwFullScreenSetting,
		Set: func(c *Config, on bool) {
			brwSetFullScreen(c, on)
			setToast("Full-screen pages: " + setOnOff(on))
		},
	}
}

// brwClearChromeHits drops the top bar's hit areas for a frame that does not
// draw the bar. renderStatusBar clears and re-measures them every frame it
// draws; a rect left over from the screen before would still take a click and
// still be a stop in the focus graph. The PDF reader clears the same set.
func brwClearChromeHits() {
	sceneTitleBackRect, statusClockRect, statusWiFiRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	weatherStatusRect, statusMediaRect, statusVolumeRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	statusBrightnessRect = sdl.Rect{}
	tb.startRect = sdl.Rect{}
	updatePrompt.badge = sdl.Rect{}
	headerTrophyClear()
}

// brwDrawFullscreenPaper paints the ground of the full-screen page before
// anything is drawn on it: the page's own paper (its gradient, when it has one)
// behind a laid-out page, so the few pixels of margin around the text are the
// page's colour rather than a frame; Home's glass behind the loading card, the
// offline card and a plain-text page.
func brwDrawFullscreenPaper(renderer *sdl.Renderer, elem Element, doc *webDocument, paper bool) {
	if renderer == nil {
		return
	}
	rect := sdl.Rect{X: elem.X, Y: elem.Y, W: getElementWidth(elem, screenWidth), H: getElementHeight(elem, screenHeight)}
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if paper && doc != nil {
		if doc.HasPaperGradient {
			drawWebGradient(renderer, rect, 0, doc.PaperGradient)
			return
		}
		_ = renderer.SetDrawColor(doc.Paper.R, doc.Paper.G, doc.Paper.B, 255)
		_ = renderer.FillRect(&rect)
		return
	}
	setDrawColor(renderer, glassA(10))
	_ = renderer.FillRect(&rect)
}

// brwDrawFullscreenStatus draws the full-screen page's status stack in the
// top-right corner, top to bottom: the site pill (for a moment after a page
// lands, and while the pointer is near the top), the find count while a search
// runs, and "Link mode" while the D-pad walks links. busy is a page still on
// the wire or the offline card, which have no site to name yet.
func brwDrawFullscreenStatus(renderer *sdl.Renderer, config *Config, elem Element, doc *webDocument, busy bool) {
	brwFS.pill, brwFS.find, brwFS.mode = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	if renderer == nil || config == nil {
		return
	}
	now := sdl.GetTicks64()
	// A pointer that moved into the band along the top brings the pill back,
	// the way a full-screen browser reveals its toolbar.
	if mouseX != brwFS.mouseX || mouseY != brwFS.mouseY {
		brwFS.mouseX, brwFS.mouseY = mouseX, mouseY
		if mouseY >= elem.Y && mouseY < elem.Y+brwPointerBand {
			brwFS.pointerAt = now
		}
	}
	address := ""
	if doc != nil {
		address = strings.TrimSpace(doc.URL)
	}
	if address != "" && address != brwFS.url {
		brwFS.url = address
		brwFS.shownAt = now
	}
	// The menu names the page in its own header, and its scrim would only dim
	// the stack.
	if busy || brwMenu.open {
		return
	}
	font := getFontSized(config, "small", 15)
	if font == nil {
		return
	}
	right := elem.X + getElementWidth(elem, screenWidth) - brwStatusInset
	y := elem.Y + brwStatusInset
	if address != "" {
		if rect := brwDrawSitePill(renderer, config, font, address, right, y, now); rect.W > 0 {
			brwFS.pill = rect
			y += rect.H + brwStatusGap
		}
	}
	if label := browserFindChipLabel(); label != "" {
		brwFS.find = brwDrawStatusChip(renderer, config, font, label, right, y,
			WithAlpha(ColorPanelRaised, 235), WithAlpha(ColorWarning, 200), ColorTextPrimary())
		y += brwFS.find.H + brwStatusGap
	}
	if browserNav == browserModeLinks {
		brwFS.mode = brwDrawStatusChip(renderer, config, font, "Link mode", right, y,
			accentColor, WithAlpha(accentColor, 220), contrastText(accentColor))
	}
}

// brwDrawSitePill draws the pill that names the page's site — a padlock, the
// host, and the button that opens the menu — right-aligned at right, when it is
// due. It returns where it drew it, or a zero rect.
func brwDrawSitePill(renderer *sdl.Renderer, config *Config, font *ttf.Font, address string, right, y int32, now uint64) sdl.Rect {
	fh := int32(font.Height())
	h := fh + 12
	icon := fh
	host := ellipsize(font, brwHostOf(address), screenWidth*3/10)
	hostW, _ := measureText(font, host)
	key := hintKeyFor("Select / M", "Menu")
	keyW, _ := measureText(font, key)
	badgeW := keyW + 12
	labelW, _ := measureText(font, "Menu")
	w := 12 + icon + 8 + hostW + 14 + badgeW + 6 + labelW + 14
	rect := sdl.Rect{X: right - w, Y: y, W: w, H: h}
	due := now-brwFS.shownAt < brwPillHold || now-brwFS.pointerAt < brwPointerHold ||
		pointInRect(mouseX, mouseY, rect)
	if brwFS.shownAt == 0 && brwFS.pointerAt == 0 {
		due = false
	}
	if !due {
		return sdl.Rect{}
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, glassSheetFill())
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, 1, glassSheetEdge())
	glyph, ink := "unlock", WarningInk()
	if browserHTTPSAddress(address) {
		glyph, ink = "lock", SuccessInk()
	}
	x := rect.X + 12
	textY := y + (h-fh)/2
	drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: x, Y: y + (h-icon)/2, W: icon, H: icon}, ink)
	x += icon + 8
	drawGlassText(renderer, config, font, host, ColorTextPrimary(), x, textY)
	x += hostW + 14
	badge := sdl.Rect{X: x, Y: y + (h-(fh+4))/2, W: badgeW, H: fh + 4}
	drawGlassHintBadge(renderer, config, font, badge, "Select / M", "Menu", badge.X+badge.W+6, textY)
	return rect
}

// brwDrawStatusChip draws one chip of the stack right-aligned at right and
// returns its rect.
func brwDrawStatusChip(renderer *sdl.Renderer, config *Config, font *ttf.Font, label string, right, y int32, fill, edge, ink sdl.Color) sdl.Rect {
	fh := int32(font.Height())
	tw, _ := measureText(font, label)
	h := fh + 12
	rect := sdl.Rect{X: right - tw - 24, Y: y, W: tw + 24, H: h}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, 1, edge)
	drawGlassText(renderer, config, font, label, ink, rect.X+12, rect.Y+(h-fh)/2)
	return rect
}

// brwFullscreenClick takes a click on the full-screen Browser: the activity
// chips that float over every screen keep theirs, the site pill opens the menu,
// the find chip steps to the next match, the mode chip goes back to scrolling,
// and anything else is the page's. It reports false when the full-screen
// Browser is not on screen, so the click goes on to the usual handlers — the
// top bar's among them, which this screen does not draw.
func brwFullscreenClick(renderer *sdl.Renderer, mx, my int32, config *Config) bool {
	if !brwFullscreenSceneUp(config) {
		return false
	}
	if activityStripClick(mx, my) {
		return true
	}
	for i, elem := range config.Scenes[currentSceneIndex].Elements {
		if elem.Type != "textbrowser" || !brwFullscreenElem(elem) {
			continue
		}
		selectedButtonIndex = i
		focusEngine.SetByElementIndex(i)
		switch {
		case brwFS.pill.W > 0 && hitTouchTarget(brwFS.pill, mx, my):
			brwMenuOpen(config)
		case brwFS.find.W > 0 && hitTouchTarget(brwFS.find, mx, my):
			browserFindStep(config, elem, 1)
		case brwFS.mode.W > 0 && hitTouchTarget(brwFS.mode, mx, my):
			browserToggleNavMode()
		default:
			browserTapPageLink(renderer, config, elem, mx, my)
		}
		return true
	}
	return false
}
