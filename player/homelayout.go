package main

import (
	"container/list"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type HomeAction string

const (
	HomeActionContinue       HomeAction = "continue"
	HomeActionMedia          HomeAction = "media"
	HomeActionFiles          HomeAction = "files"
	HomeActionPackages       HomeAction = "packages"
	HomeActionApps           HomeAction = "apps"
	HomeActionFavorites      HomeAction = "favorites"
	HomeActionChat           HomeAction = "chat"
	HomeActionTools          HomeAction = "tools"
	HomeActionEmulator       HomeAction = "emulator"
	HomeActionSettings       HomeAction = "settings"
	HomeActionExit           HomeAction = "exit"
	HomeActionToggleCalendar HomeAction = "toggle_calendar"
	HomeActionToggleWeather  HomeAction = "toggle_weather"
)

type HomeFonts struct {
	Brand    *ttf.Font
	Greeting *ttf.Font
	Subtitle *ttf.Font
	Card     *ttf.Font
	Helper   *ttf.Font
	Small    *ttf.Font
}

type HomeContinueState struct {
	HasRecent bool
	Title     string
	Subtitle  string
	Progress  float32
}

type HomeViewData struct {
	Greeting    string
	Subtitle    string
	Version     string
	StatusParts []string // right-side header cluster, shared with scene headers
	// The notification bell is sized and drawn from the same font/metrics on
	// every screen, so the home header cannot drift from the scene status bar.
	BellFont *ttf.Font
	BellW    int32
	BellH    int32
	Continue HomeContinueState
	Pressed  HomeFocusID
	// PressDepth is the eased 0..1 weight of the click feedback for Pressed:
	// it rises and falls over the short press window so the card dips instead
	// of jumping. Zero means settled.
	PressDepth   float64
	ShowBackHint bool
}

type HomeItem struct {
	ID       HomeFocusID
	Title    string
	Subtitle string
	Icon     string
	Action   HomeAction
	Rect     sdl.Rect
}

type HomeLayout struct {
	Width  int32
	Height int32
	Focus  *HomeFocusController
	Items  map[HomeFocusID]HomeItem
	Order  []HomeFocusID

	// Hover/focus lift: the focused card grows by a couple of pixels, eased so
	// it settles instead of snapping. Motion is disabled by reduced motion.
	Motion  bool
	hover   uiHover
	hoverID HomeFocusID
}

func NewHomeLayout(width, height int32) *HomeLayout {
	if width <= 0 {
		width = HomeBaseWidth
	}
	if height <= 0 {
		height = HomeBaseHeight
	}

	layout := &HomeLayout{
		Width:  width,
		Height: height,
		Motion: true,
		Focus:  NewHomeFocusController(),
		Items:  make(map[HomeFocusID]HomeItem, 11),
		Order: []HomeFocusID{
			HomeFocusContinue, HomeFocusMedia, HomeFocusFiles,
			HomeFocusPackages, HomeFocusApps, HomeFocusFavorites,
			HomeFocusChat, HomeFocusTools, HomeFocusEmulator,
			HomeFocusSettings, HomeFocusWeather, HomeFocusClock, HomeFocusExit,
		},
	}
	layout.rebuildItems()
	return layout
}

func (h *HomeLayout) Resize(width, height int32) {
	if h == nil || width <= 0 || height <= 0 {
		return
	}
	h.Width = width
	h.Height = height
	h.rebuildItems()
}

func (h *HomeLayout) rebuildItems() {
	if h == nil {
		return
	}
	card := func(id HomeFocusID, title, subtitle, icon string, action HomeAction, rect sdl.Rect) {
		h.Items[id] = HomeItem{
			ID: id, Title: title, Subtitle: subtitle,
			Icon: icon, Action: action,
			Rect: ScaleHomeRect(rect, h.Width, h.Height),
		}
	}

	// Continue spans both left-column rows: its own row plus the slot the
	// Settings card used to occupy.
	card(HomeFocusContinue, "Continue", "Resume your latest activity", "continue", HomeActionContinue,
		sdl.Rect{X: 32, Y: 164, W: 480, H: 258})
	card(HomeFocusMedia, "Media", "Watch and listen", "media", HomeActionMedia,
		sdl.Rect{X: 536, Y: 164, W: 348, H: 121})
	// Files and Settings share the top-right slot but keep a clear gap: flush
	// edges made them read as one wide tile with a seam.
	card(HomeFocusFiles, "Files", "Browse storage", "files", HomeActionFiles,
		sdl.Rect{X: 900, Y: 164, W: 162, H: 121})
	card(HomeFocusPackages, "Packages", "Discover content", "packages", HomeActionPackages,
		sdl.Rect{X: 536, Y: 301, W: 348, H: 121})
	card(HomeFocusApps, "Apps", "Launch installed apps", "apps", HomeActionApps,
		sdl.Rect{X: 900, Y: 301, W: 348, H: 121})
	card(HomeFocusFavorites, "Favorites", "Saved content", "favorites", HomeActionFavorites,
		sdl.Rect{X: 32, Y: 454, W: 292, H: 158})
	card(HomeFocusChat, "Chat", "Messages and sharing", "chat", HomeActionChat,
		sdl.Rect{X: 340, Y: 454, W: 292, H: 158})
	card(HomeFocusTools, "Tools", "Network and utilities", "tools", HomeActionTools,
		sdl.Rect{X: 648, Y: 454, W: 292, H: 158})
	card(HomeFocusEmulator, "Emulator", "Launch games and emulators", "emulator", HomeActionEmulator,
		sdl.Rect{X: 956, Y: 454, W: 292, H: 158})
	card(HomeFocusSettings, "Settings", "Personalize JukaHub", "settings", HomeActionSettings,
		sdl.Rect{X: 1082, Y: 164, W: 166, H: 121})
	// The weather chip and clock are the header text itself - no separate
	// buttons are drawn. drawHeader replaces these rects with the measured
	// status text every frame, so focus and clicks land on the header.
	card(HomeFocusWeather, "Weather", "Local forecast", "weather", HomeActionToggleWeather,
		sdl.Rect{X: 1020, Y: 10, W: 92, H: 36})
	card(HomeFocusClock, "Clock", "Toggle calendar", "clock", HomeActionToggleCalendar,
		sdl.Rect{X: 1120, Y: 10, W: 76, H: 36})
	// Exit sits on the footer's left end (drawn above the footer band so it
	// stays visible) and is the primary way to quit from the home screen.
	// Back stays available through B/Esc and the footer hint.
	card(HomeFocusExit, "Exit", "Power down", "exit", HomeActionExit,
		sdl.Rect{X: 8, Y: 678, W: 116, H: 36})
}

func (h *HomeLayout) CurrentItem() HomeItem {
	if h == nil {
		return HomeItem{}
	}
	return h.Items[h.Focus.Current()]
}

func (h *HomeLayout) CurrentAction() HomeAction {
	return h.CurrentItem().Action
}

func (h *HomeLayout) Move(direction FocusDirection) HomeFocusID {
	if h == nil {
		return HomeFocusContinue
	}
	return h.Focus.Move(direction)
}

func (h *HomeLayout) FocusAt(x, y int32) bool {
	if h == nil {
		return false
	}
	for _, id := range h.Order {
		item := h.Items[id]
		if x >= item.Rect.X && x < item.Rect.X+item.Rect.W && y >= item.Rect.Y && y < item.Rect.Y+item.Rect.H {
			return h.Focus.Set(id)
		}
	}
	return false
}

// BoundsViolations reports layout problems that would show up as clipped or
// overlapping cards: empty rects, rects outside the viewport, and interactive
// items that overlap each other. The header clock is excluded from the overlap
// check because its rect is replaced with the measured status text each frame.
func (h *HomeLayout) BoundsViolations() []string {
	if h == nil {
		return []string{"nil layout"}
	}
	var out []string
	checked := make([]HomeItem, 0, len(h.Order))
	for _, id := range h.Order {
		item, ok := h.Items[id]
		if !ok {
			out = append(out, fmt.Sprintf("%s: missing", id))
			continue
		}
		if item.Rect.W <= 0 || item.Rect.H <= 0 {
			out = append(out, fmt.Sprintf("%s: empty rect %dx%d", id, item.Rect.W, item.Rect.H))
		} else if item.Rect.X < 0 || item.Rect.Y < 0 ||
			item.Rect.X+item.Rect.W > h.Width || item.Rect.Y+item.Rect.H > h.Height {
			out = append(out, fmt.Sprintf("%s: rect %+v escapes %dx%d", id, item.Rect, h.Width, h.Height))
		}
		if id == HomeFocusClock || id == HomeFocusWeather {
			continue
		}
		checked = append(checked, item)
	}
	for i := 0; i < len(checked); i++ {
		for j := i + 1; j < len(checked); j++ {
			if homeRectsOverlap(checked[i].Rect, checked[j].Rect) {
				out = append(out, fmt.Sprintf("%s overlaps %s", checked[i].ID, checked[j].ID))
			}
		}
	}
	return out
}

// homeRectsOverlap reports whether two rects share any pixel.
func homeRectsOverlap(a, b sdl.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W &&
		a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func (h *HomeLayout) Render(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) error {
	if h == nil || renderer == nil {
		return fmt.Errorf("home layout requires a renderer")
	}

	// The caller (renderHomeModern) owns the background; the layout only adds
	// its translucent backdrop blocks on top of it.
	h.drawBackdrop(renderer)
	h.drawHeader(renderer, fonts, data)
	h.drawGreeting(renderer, fonts, data)

	// The footer is plain chrome: corner chips draw after it on top of its
	// band instead of disappearing behind it.
	h.drawFooter(renderer, fonts, data.ShowBackHint)

	focused := h.Focus.Current()
	if h.hoverID != focused {
		// A new card takes the lift: restart the ease so the growth reads as a
		// hover response rather than a jump.
		h.hoverID = focused
		h.hover.weight = 0
	}
	lift := h.hover.update(1, h.Motion)
	for _, id := range h.Order {
		item := h.Items[id]
		cardLift := 0.0
		if id == focused {
			cardLift = lift
		}
		pressDepth := 0.0
		if data.Pressed == id {
			pressDepth = data.PressDepth
		}
		switch id {
		case HomeFocusContinue:
			h.drawContinueCard(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, data.Continue, cardLift)
		case HomeFocusExit:
			h.drawChipButton(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth)
		case HomeFocusClock, HomeFocusWeather:
			// Header chips are the buttons; drawHeader draws their focus rings.
		default:
			h.drawActionCard(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift)
		}
	}

	return nil
}

func (h *HomeLayout) drawBackdrop(renderer *sdl.Renderer) {
	// Nearly solid #090E19 base with only subtle low-alpha ambient shapes, so
	// the background never competes with the cards.
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, ColorBackground)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: h.Width, H: h.Height})
	// Slow animated glow layer, shared with the scene screens. The blobs sit
	// behind the cards and drift on multi-minute orbits at low alpha.
	renderAmbientBackground(renderer)
}

func (h *HomeLayout) drawHeader(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) {
	weatherStatusRect = sdl.Rect{}
	header := ScaleHomeRect(sdl.Rect{X: 0, Y: 0, W: 1280, H: 56}, h.Width, h.Height)
	setDrawColor(renderer, ColorSurface)
	_ = renderer.FillRect(&header)
	setDrawColor(renderer, ColorBorder)
	_ = renderer.DrawLine(0, header.Y+header.H-1, h.Width, header.Y+header.H-1)

	brandX := scaleX(32, h.Width)
	brandY := scaleY(15, h.Height)
	drawText(renderer, fonts.Brand, "JukaHub", brandX, brandY, ColorTextPrimary(), textAlignLeft)
	if v := strings.TrimSpace(data.Version); v != "" && fonts.Brand != nil && fonts.Small != nil {
		bw, _, _ := fonts.Brand.SizeUTF8("JukaHub")
		drawText(renderer, fonts.Small, " - "+v, brandX+int32(bw)+scaleX(6, h.Width), scaleY(19, h.Height), ColorTextMuted(), textAlignLeft)
	}

	// The right-side cluster comes from the shared headerStatusParts helper
	// (network, weather, time, battery) so it is identical on every screen.
	// It is drawn part by part so the clock's rect is known: the clock doubles
	// as the home screen's calendar button (a click or A toggles the popover),
	// so its rect is what focus, hit-testing and the focus ring use.
	//
	// The notification bell owns the far-right corner on every screen, so the
	// cluster is laid out to its left.
	if fonts.Small == nil {
		return
	}
	sep := "   "
	sepWi, _, _ := fonts.Small.SizeUTF8(sep)
	sepW := int32(sepWi)
	total := int32(0)
	for i, part := range data.StatusParts {
		w, _, _ := fonts.Small.SizeUTF8(part)
		total += int32(w)
		if i > 0 {
			total += sepW
		}
	}
	bellFont := data.BellFont
	if bellFont == nil {
		bellFont = fonts.Small
	}
	bellW, bellH := data.BellW, data.BellH
	if bellW <= 0 && bellFont != nil {
		bellH = int32(bellFont.Height()) + 10
		if bellH < 24 {
			bellH = 24
		}
		bellW = bellH + 14
	}
	bellRight := h.Width - scaleX(32, h.Width)
	bellX := bellRight - bellW
	bellY := (header.H - bellH) / 2
	renderHomeBell(renderer, bellFont, bellX, bellY, bellW, bellH)
	// A pending release shows a download badge beside the bell; it shifts the
	// status cluster left so the clock never overlaps it.
	var badgeAdvance int32
	if appConfig != nil {
		badgeAdvance = drawUpdateBadge(renderer, appConfig, bellX-scaleX(12, h.Width), bellY, bellH)
	}

	x := bellX - scaleX(12, h.Width) - badgeAdvance - total

	// Phone Companion QR: a scan-and-connect tile sits left of the network,
	// weather and clock cluster, in the empty band above the action cards. It
	// is not a focusable card; the Tools row and its trigger remain the
	// controller path to the full pairing card.
	companionHeaderRect = sdl.Rect{}
	qrCard := scaleX(104, h.Width)
	if sy := scaleY(104, h.Height); sy < qrCard {
		qrCard = sy
	}
	qrRight := x - scaleX(16, h.Width)
	qrX := qrRight - qrCard
	qrY := header.H + scaleY(2, h.Height)
	if qrCard >= 72 && qrX > scaleX(480, h.Width) {
		renderCompanionHeaderQR(renderer, fonts, sdl.Rect{X: qrX, Y: qrY, W: qrCard, H: qrCard})
	}

	y := scaleY(19, h.Height)
	for _, part := range data.StatusParts {
		w, _, _ := fonts.Small.SizeUTF8(part)
		r := sdl.Rect{
			X: x - scaleX(8, h.Width),
			Y: y - scaleY(6, h.Height),
			W: int32(w) + scaleX(16, h.Width),
			H: int32(fonts.Small.Height()) + scaleY(12, h.Height),
		}
		if isClockStatusPart(part) {
			item := h.Items[HomeFocusClock]
			item.Rect = r
			h.Items[HomeFocusClock] = item
		}
		if isWeatherStatusPart(part) {
			item := h.Items[HomeFocusWeather]
			item.Rect = r
			h.Items[HomeFocusWeather] = item
			weatherStatusRect = r
		}
		drawText(renderer, fonts.Small, part, x, y, ColorTextMuted(), textAlignLeft)
		x += int32(w) + sepW
	}
	for _, id := range []HomeFocusID{HomeFocusClock, HomeFocusWeather} {
		if h.Focus.Current() != id {
			continue
		}
		r := h.Items[id].Rect
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 2, ColorAccent)
	}
}

// isClockStatusPart reports whether a header status part is the clock text
// ("15:04"). The clock is the home screen's calendar button, so the header
// must know which of the status parts is the clock.
func isClockStatusPart(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 || len(s) > 5 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == len(s)-3 {
			if s[i] != ':' {
				return false
			}
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// renderHomeBell draws the notification bell in the home header's top-right
// corner. It mirrors the status-bar bell so the system corner is identical on
// every screen, and it records the rect the click handler uses.
func renderHomeBell(renderer *sdl.Renderer, font *ttf.Font, x, y, w, h int32) {
	if font == nil {
		return
	}
	// The pill, icon and unread badge come from the shared notification-center
	// renderer; only the home header's position differs from the scene bar.
	drawNotifBell(renderer, font, sdl.Rect{X: x, Y: y, W: w, H: h})
}

func (h *HomeLayout) drawGreeting(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) {
	greeting := strings.TrimSpace(data.Greeting)
	if greeting == "" {
		greeting = defaultGreeting(time.Now())
	}
	subtitle := strings.TrimSpace(data.Subtitle)
	if subtitle == "" {
		subtitle = "What do you want to do?"
	}
	drawText(renderer, fonts.Greeting, greeting, scaleX(32, h.Width), scaleY(78, h.Height), ColorTextPrimary(), textAlignLeft)
	drawText(renderer, fonts.Subtitle, subtitle, scaleX(32, h.Width), scaleY(119, h.Height), ColorTextMuted(), textAlignLeft)
}

func (h *HomeLayout) drawActionCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth, lift float64) {
	if fonts.Card == nil {
		return
	}
	rect := scaleHomeCard(item.Rect, lift)
	DrawHomeCardSurface(renderer, rect, focused, pressed)
	offsetY := homePressOffset(pressDepth, h.Height)

	// One icon tile per card, drawn exactly once. Narrow cards (Files and
	// Settings) cannot fit the 56px tile and a readable label side by side, so
	// they stack a smaller tile above the text. homeActionIconTile decides
	// which of the two compositions applies before anything is drawn: the
	// previous code drew the wide tile first and then drew the stacked tile on
	// top, which is how those two cards ended up with two icons each.
	pad := scaleX(20, h.Width)
	iconTile, stacked, ok := homeActionIconTile(rect, h.Width, h.Height)
	if !ok {
		return
	}
	if stacked && fonts.Helper != nil {
		// The half-width cards are short: a full 40px tile leaves no room for
		// both the title and the subtitle, and the card used to drop the
		// subtitle entirely, leaving empty space under the icon. Shrink the
		// tile just enough to keep both lines (down to a legible minimum).
		th := int32(fonts.Card.Height())
		sh := int32(fonts.Helper.Height())
		avail := rect.H - scaleY(14, h.Height) - scaleY(10, h.Height) - th - scaleY(4, h.Height) - sh - scaleY(8, h.Height)
		minSize := scaleY(26, h.Height)
		if avail >= minSize && avail < iconTile.H {
			iconTile = sdl.Rect{
				X: rect.X + (rect.W-avail)/2,
				Y: iconTile.Y,
				W: avail, H: avail,
			}
		}
	}
	iconTile.Y += offsetY
	glyph := DrawIconTile(renderer, iconTile, focused)
	DrawHomeIcon(renderer, item.Icon, glyph, focused)

	if stacked {
		stackPad := scaleX(16, h.Width)

		textW := rect.W - stackPad*2
		if textW < 48 {
			textW = 48
		}
		cx := rect.X + rect.W/2
		th := int32(fonts.Card.Height())
		sh := int32(fonts.Helper.Height())
		title := ellipsize(fonts.Card, item.Title, textW)
		subtitle := ellipsize(fonts.Helper, item.Subtitle, textW)
		textTop := iconTile.Y + iconTile.H + scaleY(10, h.Height)
		if textTop+th+scaleY(4, h.Height)+sh > rect.Y+rect.H-scaleY(8, h.Height) {
			// Too short for both lines: keep the title only.
			textTop = rect.Y + rect.H - scaleY(8, h.Height) - th
			drawText(renderer, fonts.Card, title, cx, textTop, ColorTextPrimary(), textAlignCenter)
			return
		}
		drawText(renderer, fonts.Card, title, cx, textTop, ColorTextPrimary(), textAlignCenter)
		drawText(renderer, fonts.Helper, subtitle, cx, textTop+th+scaleY(4, h.Height), ColorTextMuted(), textAlignCenter)
		return
	}

	textX := iconTile.X + iconTile.W + scaleX(16, h.Width)
	maxW := rect.X + rect.W - pad - textX
	if maxW < 24 {
		maxW = 24
	}
	title := ellipsize(fonts.Card, item.Title, maxW)
	subtitle := ellipsize(fonts.Helper, item.Subtitle, maxW)

	th := int32(fonts.Card.Height())
	sh := int32(fonts.Helper.Height())
	stackH := th + sh + scaleY(6, h.Height)
	textTop := rect.Y + (rect.H-stackH)/2 + offsetY
	drawText(renderer, fonts.Card, title, textX, textTop, ColorTextPrimary(), textAlignLeft)
	drawText(renderer, fonts.Helper, subtitle, textX, textTop+th+scaleY(6, h.Height), ColorTextMuted(), textAlignLeft)
}

// homePressOffset converts an eased press weight into a downward offset. Three
// pixels is enough to read as a push on both the 720p panel and a desktop
// window, and small enough that the card never leaves its slot.
func homePressOffset(depth float64, layoutHeight int32) int32 {
	if depth <= 0.01 {
		return 0
	}
	return int32(math.Round(uiClamp01(depth) * float64(scaleY(3, layoutHeight))))
}

// scaleHomeCard grows a card by up to three pixels for the hover/focus lift.
// The rect is only used for drawing; hit areas keep their settled geometry so
// clicks land where the card actually is.
func scaleHomeCard(rect sdl.Rect, lift float64) sdl.Rect {
	if lift <= 0.01 {
		return rect
	}
	grow := int32(math.Round(uiClamp01(lift) * 3))
	return sdl.Rect{X: rect.X - grow, Y: rect.Y - grow, W: rect.W + grow*2, H: rect.H + grow*2}
}

// homeCardStacksIcon reports whether an action card is too narrow for the
// side-by-side icon/label layout. Under roughly 120px of label column the
// title no longer fits and would render as an ellipsis, so the card switches
// to a stacked icon-over-text composition instead.
func homeCardStacksIcon(rect sdl.Rect, iconSize, pad, layoutWidth int32) bool {
	textX := rect.X + pad + iconSize + scaleX(16, layoutWidth)
	return rect.X+rect.W-pad-textX < scaleX(120, layoutWidth)
}

// homeActionIconTile returns the single icon tile an action card draws, plus
// whether the card uses the stacked (icon above label) composition. ok is
// false when the card is too small to hold an icon at all, in which case the
// caller draws no icon rather than a clipped one.
func homeActionIconTile(rect sdl.Rect, layoutWidth, layoutHeight int32) (tile sdl.Rect, stacked, ok bool) {
	pad := scaleX(20, layoutWidth)
	iconSize := min32(scaleX(56, layoutWidth), scaleY(56, layoutHeight))
	if maxIcon := rect.H - scaleY(12, layoutHeight); iconSize > maxIcon {
		iconSize = maxIcon
	}
	stacked = homeCardStacksIcon(rect, iconSize, pad, layoutWidth)
	if stacked {
		iconSize = min32(scaleX(40, layoutWidth), scaleY(40, layoutHeight))
		// Keep the stacked tile inside the card's horizontal padding even on
		// the narrowest layout the grid can produce.
		iconSize = min32(iconSize, rect.W-pad*2)
		if iconSize <= 0 {
			return sdl.Rect{}, true, false
		}
		return sdl.Rect{
			X: rect.X + (rect.W-iconSize)/2,
			Y: rect.Y + scaleY(14, layoutHeight),
			W: iconSize, H: iconSize,
		}, true, true
	}
	if iconSize <= 0 {
		return sdl.Rect{}, false, false
	}
	return sdl.Rect{
		X: rect.X + pad,
		Y: rect.Y + (rect.H-iconSize)/2,
		W: iconSize, H: iconSize,
	}, false, true
}

func (h *HomeLayout) drawContinueCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth float64, state HomeContinueState, lift float64) {
	if fonts.Small == nil {
		return
	}
	rect := scaleHomeCard(item.Rect, lift)
	DrawHomeCardSurface(renderer, rect, focused, pressed)
	offsetY := homePressOffset(pressDepth, h.Height)

	pad := scaleX(28, h.Width)

	// Uppercase CONTINUE label at top-left.
	labelColor := ColorTextMuted()
	if focused {
		labelColor = ColorAccent
	}
	labelY := rect.Y + scaleY(24, h.Height) + offsetY
	drawText(renderer, fonts.Small, "CONTINUE", rect.X+pad, labelY, labelColor, textAlignLeft)

	// 64x64 icon tile at top-right.
	iconSize := min32(scaleX(64, h.Width), scaleY(64, h.Height))
	iconTile := sdl.Rect{
		X: rect.X + rect.W - iconSize - scaleX(24, h.Width),
		Y: rect.Y + scaleY(24, h.Height) + offsetY,
		W: iconSize, H: iconSize,
	}
	glyph := DrawIconTile(renderer, iconTile, focused)
	DrawHomeIcon(renderer, "continue", glyph, focused)

	title := strings.TrimSpace(state.Title)
	subtitle := strings.TrimSpace(state.Subtitle)
	action := "A  Open Media"
	if state.HasRecent {
		if title == "" {
			title = "Continue playing"
		}
		if subtitle == "" {
			subtitle = "Pick up where you left off."
		}
		action = "A  Resume"
	} else {
		if title == "" {
			title = "Nothing played recently"
		}
		if subtitle == "" {
			subtitle = "Open Media to start watching or listening."
		}
	}

	// Text must never collide with the top-right icon tile.
	textW := rect.W - pad*2
	if right := iconTile.X - rect.X - pad; textW > right {
		textW = right
	}
	title = ellipsize(fonts.Card, title, textW)
	subtitle = ellipsize(fonts.Helper, subtitle, textW)

	titleY := labelY + int32(fonts.Small.Height()) + scaleY(12, h.Height)
	subtitleY := titleY + int32(fonts.Card.Height()) + scaleY(6, h.Height)
	drawText(renderer, fonts.Card, title, rect.X+pad, titleY, ColorTextPrimary(), textAlignLeft)
	drawText(renderer, fonts.Helper, subtitle, rect.X+pad, subtitleY, ColorTextMuted(), textAlignLeft)

	chipH := scaleY(28, h.Height)
	chipY := rect.Y + rect.H - scaleY(24, h.Height) - chipH

	if state.HasRecent {
		// Thin progress bar between the text stack and the action chip.
		progress := state.Progress
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		trackY := chipY - scaleY(22, h.Height)
		track := sdl.Rect{X: rect.X + pad, Y: trackY, W: rect.W - pad*2, H: scaleY(5, h.Height)}
		fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, 2, ColorBorder)
		fill := track
		fill.W = int32(float32(track.W) * progress)
		if fill.W > 0 {
			fillRoundedRect(renderer, fill.X, fill.Y, fill.W, fill.H, 2, ColorAccent)
		}
	}

	// Compact, measured action chip at bottom-left (clean pill).
	bw, _, _ := fonts.Small.SizeUTF8(action)
	chipW := int32(bw) + scaleX(36, h.Width)
	if chipW < scaleX(120, h.Width) {
		chipW = scaleX(120, h.Width)
	}
	chip := sdl.Rect{X: rect.X + pad, Y: chipY, W: chipW, H: chipH}
	chipColor := ColorIconSurface
	chipText := ColorTextPrimary()
	if focused {
		chipColor = ColorAccent
		chipText = ColorIconDark
	}
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, chipColor)
	drawText(renderer, fonts.Small, action, chip.X+chip.W/2, chip.Y+(chip.H-int32(fonts.Small.Height()))/2, chipText, textAlignCenter)
}

// drawChipButton renders a compact pill for the corner buttons (Exit, Back):
// a small glyph followed by a single label, sized so the text stays readable
// where the full action card would be too cramped.
func (h *HomeLayout) drawChipButton(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth float64) {
	if fonts.Small == nil {
		return
	}
	rect := item.Rect
	if pressDepth > 0 {
		rect.Y += homePressOffset(pressDepth, h.Height)
	}
	bg := ColorIconSurface
	fg := ColorTextPrimary()
	border := ColorBorder
	if focused {
		bg = ColorAccent
		fg = ColorIconDark
		border = ColorAccent
	}

	UIButton(renderer, nil, rect, "", UIButtonStyle{
		Variant:  UIButtonCustom,
		Fill:     bg,
		Border:   border,
		Pill:     true,
		NoShadow: true,
	}, UIButtonState{Pressed: pressed})

	// Small glyph on the left, then the label.
	glyph := rect.H - 16
	if glyph < 12 {
		glyph = 12
	}
	iconRect := sdl.Rect{X: rect.X + 12, Y: rect.Y + (rect.H-glyph)/2, W: glyph, H: glyph}
	DrawHomeIcon(renderer, item.Icon, iconRect, focused)

	labelX := iconRect.X + iconRect.W + 8
	drawText(renderer, fonts.Small, item.Title, labelX,
		rect.Y+(rect.H-int32(fonts.Small.Height()))/2, fg, textAlignLeft)
}

// footerHint pairs a controller button label with the action it performs.
type footerHint struct {
	button string
	label  string
}

func (h *HomeLayout) drawFooter(renderer *sdl.Renderer, fonts HomeFonts, showBack bool) {
	footer := ScaleHomeRect(sdl.Rect{X: 0, Y: 672, W: 1280, H: 48}, h.Width, h.Height)
	setDrawColor(renderer, ColorSurface)
	_ = renderer.FillRect(&footer)
	setDrawColor(renderer, ColorBorder)
	_ = renderer.DrawLine(0, footer.Y, h.Width, footer.Y)
	if fonts.Small == nil {
		return
	}

	hints := []footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Open"},
	}
	if showBack {
		hints = append(hints, footerHint{button: "B", label: "Back"})
	}
	hints = append(hints, footerHint{button: "L2/R2", label: "Sections"})

	// Four distinct hint groups: each is a small button badge pill plus a
	// separate label, with 32px gaps, centered as one row inside the footer.
	groupGap := scaleX(32, h.Width)
	badgeH := scaleY(20, h.Height)
	badgePad := scaleX(12, h.Width)
	badgeGap := scaleX(6, h.Width)
	fh := int32(fonts.Small.Height())

	type group struct {
		badgeW int32
		w      int32
	}
	groups := make([]group, 0, len(hints))
	total := int32(0)
	for _, g := range hints {
		bw, _, _ := fonts.Small.SizeUTF8(g.button)
		lw, _, _ := fonts.Small.SizeUTF8(g.label)
		badgeW := int32(bw) + badgePad*2
		w := badgeW + badgeGap + int32(lw)
		groups = append(groups, group{badgeW: badgeW, w: w})
		total += w
	}
	if len(groups) > 1 {
		total += groupGap * int32(len(groups)-1)
	}

	x := footer.X + (footer.W-total)/2
	textY := footer.Y + (footer.H-fh)/2
	badgeY := footer.Y + (footer.H-badgeH)/2
	for i, g := range hints {
		badge := sdl.Rect{X: x, Y: badgeY, W: groups[i].badgeW, H: badgeH}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, ColorIconSurface)
		drawText(renderer, fonts.Small, g.button, badge.X+badgePad, textY, ColorTextPrimary(), textAlignLeft)
		labelX := badge.X + groups[i].badgeW + badgeGap
		drawText(renderer, fonts.Small, g.label, labelX, textY, ColorTextMuted(), textAlignLeft)
		x += groups[i].w + groupGap
	}
}

func defaultGreeting(now time.Time) string {
	hour := now.Hour()
	switch {
	case hour < 12:
		return "Good morning"
	case hour < 18:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}

type textAlignment uint8

const (
	textAlignLeft textAlignment = iota
	textAlignCenter
	textAlignRight
)

// homeTextKey is the cache key for one rendered text label. Fonts and colors
// are stable across frames, so static home labels (brand, greeting, card
// titles, footer hints) rasterize once instead of every frame.
type homeTextKey struct {
	font  *ttf.Font
	text  string
	color uint32
}

// homeTextCache holds the rasterized labels drawn by drawText. It is bounded:
// drawText is used for dynamic text too (clock, status parts, video titles,
// search results, toasts), so an unbounded map slowly kept every string the
// user ever saw as an SDL texture. The LRU keeps a long session flat at a few
// megabytes while static labels still rasterize only once.
type homeTextCacheEntry struct {
	texture *sdl.Texture
	key     homeTextKey
	elem    *list.Element
	bytes   int64
}

type homeTextCacheStore struct {
	entries  map[homeTextKey]*homeTextCacheEntry
	lru      *list.List // front = most recently used
	bytes    int64
	maxBytes int64
	maxItems int
}

var homeTextCache = homeTextCacheStore{
	entries:  map[homeTextKey]*homeTextCacheEntry{},
	lru:      list.New(),
	maxBytes: homeTextCacheBudgetBytes(),
	maxItems: homeTextCacheMaxItems(),
}

// homeTextCacheBudgetBytes is the memory budget for drawText labels. They are
// small (a title is ~10-40 KB), so a few MB holds thousands of strings.
func homeTextCacheBudgetBytes() int64 {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 8 << 20
	}
	return 48 << 20
}

// homeTextCacheMaxItems bounds the entry count as well as the bytes: many
// tiny strings would otherwise never hit the byte budget.
func homeTextCacheMaxItems() int {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 512
	}
	return 2048
}

// homeTextCacheBytes reports the cache's memory for the texture accounting log.
func homeTextCacheBytes() int64 {
	return homeTextCache.bytes
}

// clearHomeTextCache destroys every cached label (shutdown / pressure).
func clearHomeTextCache() {
	for key, entry := range homeTextCache.entries {
		if entry.texture != nil {
			entry.texture.Destroy()
		}
		delete(homeTextCache.entries, key)
	}
	homeTextCache.lru.Init()
	homeTextCache.bytes = 0
}

// put inserts a texture and evicts the least recently used entries. No lock:
// drawText runs on the render thread only (the same contract the previous map
// relied on).
func (c *homeTextCacheStore) put(key homeTextKey, texture *sdl.Texture, w, h int32) {
	bytes := int64(w) * int64(h) * 4
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.bytes
		c.lru.Remove(old.elem)
		if old.texture != nil {
			old.texture.Destroy()
		}
		delete(c.entries, key)
	}
	entry := &homeTextCacheEntry{texture: texture, key: key, bytes: bytes}
	entry.elem = c.lru.PushFront(key)
	c.entries[key] = entry
	c.bytes += bytes
	for (c.bytes > c.maxBytes && c.lru.Len() > 1) || (c.lru.Len() > c.maxItems && c.lru.Len() > 1) {
		back := c.lru.Back()
		if back == nil {
			break
		}
		key := back.Value.(homeTextKey)
		if evicted, ok := c.entries[key]; ok {
			c.bytes -= evicted.bytes
			if evicted.texture != nil {
				evicted.texture.Destroy()
			}
			delete(c.entries, key)
		}
		c.lru.Remove(back)
	}
}

func drawText(renderer *sdl.Renderer, font *ttf.Font, text string, x, y int32, color sdl.Color, alignment textAlignment) {
	if renderer == nil || font == nil || strings.TrimSpace(text) == "" {
		return
	}
	// Substitute characters this font cannot draw so list rows never show
	// .notdef boxes (see fonts.go). Supported text is returned unchanged.
	text = sanitizeForFont(font, text)

	key := homeTextKey{
		font:  font,
		text:  text,
		color: uint32(color.R)<<24 | uint32(color.G)<<16 | uint32(color.B)<<8 | uint32(color.A),
	}
	var texture *sdl.Texture
	var tw, th int32
	if cached, ok := homeTextCache.entries[key]; ok && cached.texture != nil {
		homeTextCache.lru.MoveToFront(cached.elem)
		texture = cached.texture
		_, _, tw, th, _ = texture.Query()
	} else {
		surface, err := font.RenderUTF8Blended(text, color)
		if err != nil || surface == nil {
			return
		}
		defer surface.Free()
		texture, err = renderer.CreateTextureFromSurface(surface)
		if err != nil || texture == nil {
			return
		}
		tw, th = surface.W, surface.H
		homeTextCache.put(key, texture, tw, th)
	}

	dstX := x
	switch alignment {
	case textAlignCenter:
		dstX -= tw / 2
	case textAlignRight:
		dstX -= tw
	}
	dst := sdl.Rect{X: dstX, Y: y, W: tw, H: th}
	_ = renderer.Copy(texture, nil, &dst)
}

// --- helpers for home rendering ---

const (
	HomeBaseWidth  int32 = 1280
	HomeBaseHeight int32 = 720
	HomeCardRadius int32 = 16
)

var (
	ColorCardFocused = ColorCardFocus
	ColorIconSurface = sdl.Color{R: 34, G: 46, B: 70, A: 255}
	ColorIconDark    = sdl.Color{R: 7, G: 17, B: 31, A: 255}
)

func setDrawColor(renderer *sdl.Renderer, color sdl.Color) {
	_ = renderer.SetDrawColor(color.R, color.G, color.B, color.A)
}

func scaleX(v, width int32) int32 {
	return int32(float64(v) * float64(width) / float64(HomeBaseWidth))
}

func scaleY(v, height int32) int32 {
	return int32(float64(v) * float64(height) / float64(HomeBaseHeight))
}

func ScaleHomeRect(rect sdl.Rect, width, height int32) sdl.Rect {
	return sdl.Rect{
		X: scaleX(rect.X, width),
		Y: scaleY(rect.Y, height),
		W: scaleX(rect.W, width),
		H: scaleY(rect.H, height),
	}
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func DrawHomeCardSurface(renderer *sdl.Renderer, rect sdl.Rect, focused, pressed bool) {
	background := ColorCard
	if focused {
		background = ColorCardFocused
	}
	if pressed {
		background = darken(background, 18)
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, HomeCardRadius, background)
	if focused {
		// 3px cyan border drawn completely inside the card rect so it can
		// never be clipped; the card itself does not resize on focus.
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, HomeCardRadius, 3, ColorAccent)
	} else {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, HomeCardRadius, 1, ColorBorder)
	}
}

func DrawIconTile(renderer *sdl.Renderer, rect sdl.Rect, focused bool) sdl.Rect {
	color := ColorIconSurface
	if focused {
		color = ColorAccent
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 14, color)
	// Center a 32px glyph box inside the tile.
	inset := (rect.W - 32) / 2
	if inset < 0 {
		inset = 0
	}
	return insetRect(rect, inset)
}

// DrawHomeIcon renders one monochrome geometric glyph inside the given glyph
// box (32x32 at native size). Every glyph is built from filled shapes and
// 3px-equivalent strokes so the set reads with one consistent visual weight.
func DrawHomeIcon(renderer *sdl.Renderer, name string, rect sdl.Rect, focused bool) {
	// Icon keys may arrive from several sources; normalize so a card can
	// never silently miss its glyph.
	name = strings.ToLower(strings.TrimSpace(name))

	color := ColorTextPrimary()
	if focused {
		color = ColorIconDark
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, color)

	w := rect.W
	h := rect.H
	cx := rect.X + w/2
	cy := rect.Y + h/2

	switch name {
	case "media", "continue":
		// Play triangle inside a rounded frame.
		strokeRoundedRect(renderer, rect.X, rect.Y, w, h, 7, 3, color)
		fillTriangleFilled(renderer,
			pt{x: cx - w/5, y: cy - h/4},
			pt{x: cx - w/5, y: cy + h/4},
			pt{x: cx + w/4, y: cy}, color)
	case "files":
		// Filled folder: body + tab.
		body := sdl.Rect{X: rect.X + w/8, Y: rect.Y + h/3, W: w - w/4, H: h / 2}
		fillRoundedRect(renderer, body.X, body.Y, body.W, body.H, 4, color)
		tab := sdl.Rect{X: rect.X + w/8, Y: rect.Y + h/6, W: w/2 - w/8, H: h / 5}
		fillRoundedRect(renderer, tab.X, tab.Y, tab.W, tab.H, 2, color)
	case "packages":
		// Filled isometric cube: front, top, and right faces.
		front := sdl.Rect{X: rect.X + w/6, Y: rect.Y + h/3, W: w - w/3, H: h / 2}
		fillRoundedRect(renderer, front.X, front.Y, front.W, front.H, 3, color)
		topRight := pt{x: rect.X + rect.W - w/6, y: rect.Y + h/6}
		topLeft := pt{x: rect.X + w/4, y: rect.Y + h/6}
		fillQuad(renderer,
			pt{x: front.X, y: front.Y},
			pt{x: front.X + front.W, y: front.Y},
			topRight, topLeft, lighten(color, 28))
		fillQuad(renderer,
			pt{x: front.X + front.W, y: front.Y},
			topRight,
			pt{x: topRight.x, y: topRight.y + front.H},
			pt{x: front.X + front.W, y: front.Y + front.H}, darken(color, 30))
	case "apps":
		// Four filled rounded squares in a 2x2 grid: 10px cells with 5px gaps
		// and a 3px corner radius, optically centered in the glyph box so the
		// group survives integer scaling on the device.
		cell := max32(6, w/3) // 10 at native size
		gap := max32(5, w/8)  // 5 at native size
		total := cell*2 + gap
		startX := rect.X + (w-total)/2
		startY := rect.Y + (h-total)/2
		for row := int32(0); row < 2; row++ {
			for col := int32(0); col < 2; col++ {
				r := sdl.Rect{
					X: startX + col*(cell+gap),
					Y: startY + row*(cell+gap),
					W: cell, H: cell,
				}
				fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 3, color)
			}
		}
	case "favorites":
		fillStar(renderer, cx, cy, min32(w, h)/2-1, color)
	case "chat":
		// Filled speech bubble with a tail.
		bubble := sdl.Rect{X: rect.X + w/10, Y: rect.Y + h/8, W: w - w/5, H: h / 2}
		fillRoundedRect(renderer, bubble.X, bubble.Y, bubble.W, bubble.H, 7, color)
		tailTop := bubble.Y + bubble.H - 3
		fillTriangleFilled(renderer,
			pt{x: cx - w/8, y: tailTop},
			pt{x: cx - w/8 - w/6, y: rect.Y + h - h/8},
			pt{x: cx + w/10, y: tailTop}, color)
	case "tools":
		// Three filled slider tracks with knobs.
		trackW := w - w/4
		trackX := rect.X + w/8
		tracks := []int32{cy - h/4, cy, cy + h/4}
		knobs := []int32{trackX + trackW*2/5, trackX + trackW*3/5, trackX + trackW/4}
		for i, ty := range tracks {
			fillRoundedRect(renderer, trackX, ty-1, trackW, 3, 1, color)
			fillCircle(renderer, knobs[i], ty, 4, color)
		}
	case "settings":
		// Solid gear: center circle plus eight rounded teeth.
		fillCircle(renderer, cx, cy, 6, color)
		for i := 0; i < 8; i++ {
			a := float64(i) * math.Pi / 4
			tx := cx + int32(float64(w/3)*math.Cos(a))
			ty := cy + int32(float64(w/3)*math.Sin(a))
			fillCircle(renderer, tx, ty, 3, color)
		}
	case "clock":
		// Clock face with hour and minute hands.
		strokeCircle(renderer, cx, cy, w/2-1, color)
		drawClockHand(renderer, cx, cy, -math.Pi/2+0.7, w/4, 2, color)
		drawClockHand(renderer, cx, cy, -math.Pi/2+2.4, w/3, 2, color)
		fillCircle(renderer, cx, cy, 2, color)
	case "exit":
		// Power icon: a ring with a vertical bar through the top.
		tileBG := ColorIconSurface
		if focused {
			tileBG = ColorAccent
		}
		fillCircle(renderer, cx, cy, w/3, color)
		fillCircle(renderer, cx, cy, w/6, tileBG)
		fillRoundedRect(renderer, cx-2, rect.Y, 4, h/2, 2, color)
	case "back":
		// Left-pointing arrow head.
		fillTriangleFilled(renderer,
			pt{x: rect.X + w/5, y: cy},
			pt{x: cx + w/8, y: cy - h/3},
			pt{x: cx + w/8, y: cy + h/3}, color)
	default:
		// Safe visible fallback for any unknown non-empty icon name: a filled
		// ring inside the glyph box. An unrecognized key must never render an
		// empty tile, so this always draws something substantial.
		tileBG := ColorIconSurface
		if focused {
			tileBG = ColorAccent
		}
		fillCircle(renderer, cx, cy, w/3, color)
		fillCircle(renderer, cx, cy, w/6, tileBG)
	}
}

// fillStar fills a five-point star by fanning triangles from its center.
func fillStar(renderer *sdl.Renderer, cx, cy, radius int32, color sdl.Color) {
	if radius < 2 {
		return
	}
	inner := radius * 45 / 100
	var pts [10]pt
	for i := 0; i < 10; i++ {
		r := radius
		if i%2 == 1 {
			r = inner
		}
		a := -math.Pi/2 + float64(i)*math.Pi/5
		pts[i] = pt{x: cx + int32(float64(r)*math.Cos(a)), y: cy + int32(float64(r)*math.Sin(a))}
	}
	center := pt{x: cx, y: cy}
	for i := 0; i < 10; i++ {
		j := (i + 1) % 10
		fillTriangleFilled(renderer, center, pts[i], pts[j], color)
	}
}

// fillQuad fills an arbitrary convex quad as two triangles.
func fillQuad(renderer *sdl.Renderer, p1, p2, p3, p4 pt, color sdl.Color) {
	fillTriangleFilled(renderer, p1, p2, p3, color)
	fillTriangleFilled(renderer, p1, p3, p4, color)
}

// ellipsize shortens text to fit within a pixel width, appending an ellipsis.
// This is used for UI rendering where text must fit in a fixed-width area.
// Returns the original text if it already fits or if font/maxW is invalid.
// Measuring goes through the fallback engine (fonts.go), so a CJK or emoji
// glyph is measured at its real width rather than at the width of the .notdef
// box the primary font would otherwise draw for it.
func ellipsize(font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return text
	}
	return fitTextToWidth(font, text, maxW)
}

func insetRect(rect sdl.Rect, inset int32) sdl.Rect {
	return sdl.Rect{
		X: rect.X + inset,
		Y: rect.Y + inset,
		W: max32(0, rect.W-inset*2),
		H: max32(0, rect.H-inset*2),
	}
}
