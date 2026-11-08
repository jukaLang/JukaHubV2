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
	HomeActionBrowser        HomeAction = "browser"
	HomeActionChat           HomeAction = "chat"
	HomeActionTools          HomeAction = "tools"
	HomeActionEmulator       HomeAction = "emulator"
	HomeActionSettings       HomeAction = "settings"
	HomeActionExit           HomeAction = "exit"
	HomeActionToggleCalendar HomeAction = "toggle_calendar"
	HomeActionToggleWeather  HomeAction = "toggle_weather"
	HomeActionUpdate         HomeAction = "install_update"
	HomeActionNotifications  HomeAction = "toggle_notifications"
	HomeActionNowPlaying     HomeAction = "now_playing"
	HomeActionWiFi           HomeAction = "toggle_wifi"
	HomeActionSearch         HomeAction = "search"
)

// The search pill's place in the design grid (1280x720 canvas units): under
// the greeting, as wide as the greeting column, ending 12px above the cards.
// The greeting moved up to make room; the pill replaced the old "What do you
// want to do?" line, which is now a field the user can answer.
const (
	homeGreetingY     int32 = 64
	homeSearchPillX   int32 = 32
	homeSearchPillY   int32 = 110
	homeSearchPillW   int32 = 448
	homeSearchPillH   int32 = 38
	homeSearchPillGap int32 = 16 // minimum gap to the header launchers on its right
)

type HomeFonts struct {
	Brand    *ttf.Font
	Greeting *ttf.Font
	Subtitle *ttf.Font
	Card     *ttf.Font
	// Tile and Meta are the standard card's own scale: a 20px name over a 15px
	// description, which is what lets "Messages and servers" fit a 189px tile
	// instead of being ellipsised.
	Tile   *ttf.Font
	Meta   *ttf.Font
	Helper *ttf.Font
	Small  *ttf.Font
}

type HomeContinueState struct {
	HasRecent bool
	Title     string
	Subtitle  string
	Progress  float32
	// Remaining is the "2:34 left" label next to the progress line. Empty when
	// the length of the item is unknown, in which case only the bar is drawn.
	Remaining string
	// Kind is what the card previews: "video" (a frame), "music" (an audio
	// graph), "game" (box art) or "" (the ambient artwork).
	Kind string
	// Chip is the label in the top-left chip ("Continue watching", "Now
	// playing"); Action is the pill's verb ("Resume", "Play", "Open player").
	// Both fall back to the card's defaults when empty.
	Chip   string
	Action string
	// Preview says how to make the video frame or box art (home_continue.go);
	// it is built off the render thread and drawn once it is ready.
	Preview homePreviewJob
	// Wave is the track's audio graph; WaveReal is false while it is still a
	// stand-in pattern, and Live animates it while the track plays.
	Wave     [homeWaveBars]float32
	HasWave  bool
	WaveReal bool
	Live     bool
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

	// Arrangement is the alternative layout in force (homeArrangementCustom
	// when the scene's own geometry is authoritative).
	Arrangement string
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
		Items:  make(map[HomeFocusID]HomeItem, 19),
		// The search pill comes after the cards: its grown touch target reaches
		// a few pixels toward the Continue card, and a tap on the card's own
		// edge must stay the card's.
		Order: []HomeFocusID{
			HomeFocusContinue, HomeFocusMedia, HomeFocusFiles,
			HomeFocusPackages, HomeFocusApps, HomeFocusFavorites,
			HomeFocusBrowser, HomeFocusChat, HomeFocusTools, HomeFocusEmulator,
			HomeFocusSettings, HomeFocusSearch, HomeFocusUpdate, HomeFocusWiFi,
			HomeFocusWeather, HomeFocusClock, HomeFocusNowPlaying, HomeFocusBell,
			HomeFocusExit,
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

// Home arrangements are the alternative layouts of the home screen. The
// dashboard is the composition the cards were designed around; the grid lays
// the same cards out as equal tiles. A total-conversion theme preset installs
// one of them, and "custom" — the default — keeps jukaconfig.json authoritative,
// which is why choosing a conversion never rewrites the scene's own geometry.
const (
	homeArrangementCustom    = "custom"
	homeArrangementDashboard = "dashboard"
	homeArrangementGrid      = "grid"
)

// homeArrangementLabel is the name the UI shows for an arrangement.
func homeArrangementLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case homeArrangementDashboard:
		return "Dashboard"
	case homeArrangementGrid:
		return "Hero grid"
	}
	return "Custom (from jukaconfig.json)"
}

// normalizeHomeArrangement clamps a stored value to a known arrangement.
func normalizeHomeArrangement(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case homeArrangementDashboard:
		return homeArrangementDashboard
	case homeArrangementGrid:
		return homeArrangementGrid
	}
	return homeArrangementCustom
}

// homeArrangement is the arrangement the user is on. The unset case is Custom:
// the shipped configs place their own cards, so an unset value must not move
// anything.
func homeArrangement(config *Config) string {
	if config == nil {
		return homeArrangementCustom
	}
	if s, ok := customString(config.Variables.Custom, "homeArrangement"); ok {
		return normalizeHomeArrangement(s)
	}
	return homeArrangementCustom
}

// homeGridRects lays the body cards out as a mosaic: four columns over the
// dashboard's body box, with the Continue card standing two rows tall in the
// first column. The hero tile is not decoration — a resume card carries a
// progress line and a chip, and at one row's height those collide — and it
// makes the arrangement fill exactly: two cells for Continue, ten for the ten
// remaining cards, three full rows and no gap to centre.
func homeGridRects(width, height int32) map[HomeFocusID]sdl.Rect {
	body := sdl.Rect{X: 32, Y: 164, W: width - 64, H: height - 164 - sdl.Rect{H: 108}.H}
	if body.W <= 0 || body.H <= 0 {
		return nil
	}
	const cols, rows = 4, 3
	gap := int32(12)
	cellW := (body.W - int32(cols-1)*gap) / int32(cols)
	cellH := (body.H - int32(rows-1)*gap) / int32(rows)
	if cellW <= 0 || cellH <= 0 {
		return nil
	}
	cell := func(col, row int32) sdl.Rect {
		return sdl.Rect{
			X: body.X + col*(cellW+gap),
			Y: body.Y + row*(cellH+gap),
			W: cellW,
			H: cellH,
		}
	}
	rects := map[HomeFocusID]sdl.Rect{
		// The hero tile: the first column, two rows tall.
		HomeFocusContinue: {X: body.X, Y: body.Y, W: cellW, H: 2*cellH + gap},
	}
	// Everything else in reading order, skipping the two cells the hero tile
	// occupies.
	rest := []HomeFocusID{
		HomeFocusMedia, HomeFocusFiles, HomeFocusPackages,
		HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser,
		HomeFocusChat, HomeFocusTools, HomeFocusEmulator, HomeFocusSettings,
	}
	col, row := int32(1), int32(0)
	for _, id := range rest {
		rects[id] = cell(col, row)
		col++
		if col == cols {
			col = 0
			row++
		}
		if col == 0 && row == 1 {
			// The hero tile's second cell is not available.
			col = 1
		}
	}
	return rects
}

// homeArrangementRects returns the card rects an arrangement specifies, or nil
// for Custom and for the dashboard, whose rects are the built-in ones below.
func homeArrangementRects(name string, width, height int32) map[HomeFocusID]sdl.Rect {
	switch normalizeHomeArrangement(name) {
	case homeArrangementGrid:
		return homeGridRects(width, height)
	}
	return nil
}

// SetArrangement switches the layout to another arrangement and rebuilds the
// cards. Custom returns the built-in geometry, which ApplyConfig then lets
// jukaconfig.json override as before.
func (h *HomeLayout) SetArrangement(name string) {
	if h == nil {
		return
	}
	next := normalizeHomeArrangement(name)
	if h.Arrangement == next {
		return
	}
	h.Arrangement = next
	h.rebuildItems()
}

func (h *HomeLayout) rebuildItems() {
	if h == nil {
		return
	}
	// A built-in arrangement moves the body cards; the rects below are the
	// dashboard composition and stay the default for Custom.
	arrangementRects := homeArrangementRects(h.Arrangement, h.Width, h.Height)
	card := func(id HomeFocusID, title, subtitle, icon string, action HomeAction, rect sdl.Rect) {
		if override, ok := arrangementRects[id]; ok {
			rect = override
		}
		h.Items[id] = HomeItem{
			ID: id, Title: title, Subtitle: subtitle,
			Icon: icon, Action: action,
			Rect: ScaleHomeRect(rect, h.Width, h.Height),
		}
	}

	// The design sheet's grid at 1280x720: 32px side margins, 16px gutters, a
	// 480px Continue card beside a 720px right column, then a 152px row of six
	// tiles. Every card is 152px (or 320px) tall so the rows line up exactly.
	card(HomeFocusContinue, "Continue", "Resume your latest activity", "continue", HomeActionContinue,
		sdl.Rect{X: 32, Y: 160, W: 480, H: 320})
	card(HomeFocusMedia, "Media", "Watch and listen", "media", HomeActionMedia,
		sdl.Rect{X: 528, Y: 160, W: 720, H: 152})
	card(HomeFocusFiles, "Files", "Browse storage", "files", HomeActionFiles,
		sdl.Rect{X: 528, Y: 328, W: 230, H: 152})
	card(HomeFocusPackages, "Packages", "Install and update", "packages", HomeActionPackages,
		sdl.Rect{X: 774, Y: 328, W: 229, H: 152})
	card(HomeFocusApps, "Apps", "Portable and desktop", "apps", HomeActionApps,
		sdl.Rect{X: 1019, Y: 328, W: 229, H: 152})
	// The bottom row: six equal tiles, the Settings card included here instead of
	// floating in the top-right corner.
	card(HomeFocusFavorites, "Favorites", "Pinned for later", "favorites", HomeActionFavorites,
		sdl.Rect{X: 32, Y: 496, W: 190, H: 152})
	card(HomeFocusBrowser, "Browser", "The open web", "browser", HomeActionBrowser,
		sdl.Rect{X: 238, Y: 496, W: 190, H: 152})
	card(HomeFocusChat, "Chat", "Messages and servers", "chat", HomeActionChat,
		sdl.Rect{X: 444, Y: 496, W: 189, H: 152})
	card(HomeFocusTools, "Tools", "Network and system", "tools", HomeActionTools,
		sdl.Rect{X: 649, Y: 496, W: 189, H: 152})
	card(HomeFocusEmulator, "Emulator", "Retro library", "emulator", HomeActionEmulator,
		sdl.Rect{X: 854, Y: 496, W: 189, H: 152})
	card(HomeFocusSettings, "Settings", "Device and design", "settings", HomeActionSettings,
		sdl.Rect{X: 1059, Y: 496, W: 189, H: 152})
	// The search pill under the greeting. It is a control, not a menu card, so
	// a config tile cannot move it (homeConfigOverlayables).
	card(HomeFocusSearch, "Search", "Apps, settings, files and the web", "search", HomeActionSearch,
		sdl.Rect{X: homeSearchPillX, Y: homeSearchPillY, W: homeSearchPillW, H: homeSearchPillH})
	// Exit sits on the footer band, inset by the frame margin like the rest of
	// the grid; the design sheet puts it at the band's left end.
	card(HomeFocusExit, "Exit", "Power down", "exit", HomeActionExit,
		sdl.Rect{X: 48, Y: 674, W: 116, H: 36})
	// The header chips (signal meter, weather, clock, now-playing, bell, update)
	// are the header text itself: drawHeader replaces every one of these rects
	// with what it measured this frame. They keep placeholders so a tap or a
	// D-pad press before the first frame still resolves to a control.
	card(HomeFocusWiFi, "Wi-Fi", "Connect to a network", "wifi", HomeActionWiFi,
		sdl.Rect{X: 930, Y: 10, W: 52, H: 36})
	card(HomeFocusWeather, "Weather", "Local forecast", "weather", HomeActionToggleWeather,
		sdl.Rect{X: 1000, Y: 10, W: 92, H: 36})
	card(HomeFocusClock, "Clock", "Toggle calendar", "clock", HomeActionToggleCalendar,
		sdl.Rect{X: 1090, Y: 10, W: 76, H: 36})
	card(HomeFocusNowPlaying, "Now playing", "Open the player", "media", HomeActionNowPlaying,
		sdl.Rect{X: 1160, Y: 10, W: 44, H: 36})
	card(HomeFocusBell, "Notifications", "Recent alerts", "bell", HomeActionNotifications,
		sdl.Rect{X: 1204, Y: 10, W: 46, H: 36})
	card(HomeFocusUpdate, "Update", "Install the new release", "update", HomeActionUpdate,
		sdl.Rect{X: 900, Y: 12, W: 26, H: 24})
}

// homeConfigOverlayables lists the HomeFocusID header chips whose drawn rect
// is replaced with the measured status text every frame (drawHeader). A config
// tile carrying one of their titles must not fight the live geometry, so they
// are never overridable from jukaconfig.json.
func homeConfigOverlayables() map[HomeFocusID]bool {
	return map[HomeFocusID]bool{
		// Header chips are drawn from the measured status cluster every frame
		// and never from jukaconfig.json.
		HomeFocusClock:      false,
		HomeFocusWeather:    false,
		HomeFocusUpdate:     false,
		HomeFocusBell:       false,
		HomeFocusNowPlaying: false,
		HomeFocusWiFi:       false,
		HomeFocusPS4:        false,
		HomeFocusPS5:        false,
		// The search pill is a fixed control under the greeting.
		HomeFocusSearch: false,
		// The menu cards + the Exit chip are fully config-driven.
		HomeFocusContinue:  true,
		HomeFocusMedia:     true,
		HomeFocusFiles:     true,
		HomeFocusPackages:  true,
		HomeFocusApps:      true,
		HomeFocusFavorites: true,
		HomeFocusBrowser:   true,
		HomeFocusChat:      true,
		HomeFocusTools:     true,
		HomeFocusEmulator:  true,
		HomeFocusSettings:  true,
		HomeFocusExit:      true,
	}
}

// ApplyConfig makes jukaconfig.json authoritative over the built-in home card
// layout: every Main-scene element of type "button" whose text names one of
// the home items is applied whole — position, size, title, and (when set)
// subtitle (placeholder) and icon. This used to be the other way round: the
// cards were hardcoded in rebuildItems and the config's Main tiles only chose
// where each card navigated, so editing jukaconfig.json moved nothing on
// screen and the shipped tile grid matched nothing anybody ever saw.
//
// The element coordinates are canvas-space, exactly like every other scene's
// elements (the compact-device profile scales them at load); geometry outside
// the canvas or with a zero/negative size is ignored and the built-in card
// keeps its slot instead. Header chips are excluded (homeConfigOverlayable)
// because their rects are re-measured underneath this call every frame.
func (h *HomeLayout) ApplyConfig(elements []Element) {
	if h == nil {
		return
	}
	// A total conversion owns the geometry while it is in force: the scene's
	// own coordinates are left untouched, so switching back to Custom puts the
	// authored arrangement back exactly as it was.
	if normalizeHomeArrangement(h.Arrangement) != homeArrangementCustom {
		return
	}
	overlayable := homeConfigOverlayables()
	for _, el := range elements {
		title := strings.TrimSpace(el.Text)
		if title == "" {
			continue
		}
		id := h.configItemFor(title)
		if id == "" || !overlayable[id] {
			continue
		}
		item := h.Items[id]
		w, hatW := parsePosInt(strings.TrimSpace(string(el.Width)))
		hgt, hatH := parsePosInt(strings.TrimSpace(string(el.Height)))
		if hatW && hatH && w > 0 && hgt > 0 {
			rect := sdl.Rect{X: el.X, Y: el.Y, W: w, H: hgt}
			if rect.X >= 0 && rect.Y >= 0 &&
				rect.X+rect.W <= h.Width && rect.Y+rect.H <= h.Height {
				item.Rect = rect
			}
		}
		item.Title = title
		if sub := strings.TrimSpace(el.Placeholder); sub != "" {
			item.Subtitle = sub
		}
		if icon := strings.TrimSpace(el.Icon); icon != "" {
			item.Icon = icon
		}
		h.Items[id] = item
	}
}

// configItemFor maps a config tile's text to the home item it customizes, with
// the same leniency tile triggers have always matched with: an exact (case
// insensitive) title, or any title containing "tools" naming the Tools card —
// the shipped config spells that row "Search / Tools".
func (h *HomeLayout) configItemFor(title string) HomeFocusID {
	for _, id := range h.Order {
		item, ok := h.Items[id]
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Title), title) {
			return id
		}
	}
	if strings.Contains(strings.ToLower(title), "tools") {
		return HomeFocusTools
	}
	return ""
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
		// A header chip that is not drawn (no weather forecast, no release on
		// offer, nothing playing) keeps its placeholder rect; it must not
		// swallow a click meant for the chip beside it.
		if !h.Focus.IsEnabled(id) {
			continue
		}
		item := h.Items[id]
		// Hit-test against the grown tap area, not the drawn rect: a compact
		// 36px Exit pill or a header chip looks right but is smaller than a
		// fingertip, and a press a few pixels off does nothing. The drawn
		// geometry never moves — only the invisible target grows.
		r := growTouchTarget(item.Rect)
		if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
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
		switch id {
		case HomeFocusClock, HomeFocusWeather, HomeFocusUpdate, HomeFocusBell, HomeFocusNowPlaying, HomeFocusWiFi:
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
		case HomeFocusMedia:
			h.drawMediaCard(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift)
		case HomeFocusExit:
			h.drawChipButton(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth)
		case HomeFocusSearch:
			h.drawSearchPill(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift, data.Subtitle)
		case HomeFocusClock, HomeFocusWeather, HomeFocusUpdate, HomeFocusPS5, HomeFocusPS4, HomeFocusNowPlaying, HomeFocusBell, HomeFocusWiFi:
			// Header chips are the buttons; drawHeader draws them and their focus
			// rings. Rendering them as action cards would paint a clipped card
			// (title and icon) over the header -- the now-playing entry sits on
			// the bell's shoulder, so a stray card would cover it.
		default:
			h.drawActionCard(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift)
		}
	}

	return nil
}

func (h *HomeLayout) drawBackdrop(renderer *sdl.Renderer) {
	// Near-solid navy base, then the two radials the design sheet uses: a cyan
	// bloom behind the Continue card and a violet one in the bottom-right
	// corner, then the slow ambient layer. Each is one textured blit, so the
	// ambience stays cheap on device. It is the app's one backdrop
	// (drawAppBackdrop, ui_glass.go): every scene and panel stands on the same
	// surface, so leaving Home no longer changes the background.
	cfg := homeBackdropConfig
	if cfg == nil {
		cfg = appConfig
	}
	drawAppBackdrop(renderer, cfg, h.Width, h.Height)
}

func (h *HomeLayout) drawHeader(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) {
	weatherStatusRect = sdl.Rect{}
	statusWiFiRect = sdl.Rect{}
	statusMediaRect = sdl.Rect{}
	ps5TileRect = sdl.Rect{}
	ps4TileRect = sdl.Rect{}
	homeUpdateRect = sdl.Rect{}
	// The bar's height, wordmark and caption come from header.go — the same code
	// the scene bar draws with — so Home and a scene can no longer differ by two
	// pixels of bar height and ten of caption size, which is what made the header
	// look like two designs.
	barH := headerBarHeight()
	header := sdl.Rect{X: 0, Y: 0, W: h.Width, H: barH}
	// The design sheet's status row has no bar chrome: the wordmark and the
	// system cluster sit directly on the background, so the screen reads as one
	// surface instead of a header strip plus a page.

	headerConfig := appConfig
	if headerConfig == nil {
		headerConfig = &Config{}
	}
	drawHeaderBrand(renderer, headerConfig, barH, headerCaptionText(data.Version, ""))

	// The right-side cluster comes from the shared headerStatusParts helper
	// (network, weather, time, battery) so it is identical on every screen.
	// It is drawn part by part so the clock's rect is known: the clock doubles
	// as the home screen's calendar button (a click or A toggles the popover),
	// so its rect is what focus, hit-testing and the focus ring use.
	//
	// The notification bell owns the far-right corner on every screen, so the
	// cluster is laid out to its left.
	if fonts.Small == nil {
		// No status bar, so no bell either. Keep it out of the D-pad path so
		// focus can never land on a control that is not drawn.
		h.Focus.SetEnabled(HomeFocusBell, false)
		return
	}
	sep := "   "
	sepW, _ := measureText(fonts.Small, sep)
	total := int32(0)
	for i, part := range data.StatusParts {
		w := statusClusterPartWidth(fonts.Small, part)
		total += w
		if i > 0 {
			total += sepW
		}
	}
	bellFont := data.BellFont
	if bellFont == nil {
		bellFont = fonts.Small
	}
	bellW, bellH := data.BellW, data.BellH
	if bellW <= 0 {
		// One sizing rule for the corner: the shared metrics the scene bar uses,
		// so the chip cannot be a different size before the view data is built.
		bellW, bellH = notifBellSize(headerConfig)
	}
	bellRight := h.Width - StatusBarMargin
	bellX := bellRight - bellW
	bellY := (header.H - bellH) / 2
	renderHomeBell(renderer, headerConfig, bellFont, bellX, bellY, bellW, bellH)
	// The bell is a full member of the home focus graph: record the rect it was
	// actually drawn at, and only let the D-pad land on it while it is visible.
	if nc.bellRect.W > 0 {
		item := h.Items[HomeFocusBell]
		item.Rect = nc.bellRect
		h.Items[HomeFocusBell] = item
	}
	h.Focus.SetEnabled(HomeFocusBell, nc.bellRect.W > 0)

	// The release offer is the same pill in the same corner as on every scene
	// header: immediately left of the bell. It is dropped when it would push the
	// cluster into the greeting column; on such a screen the modal prompt and
	// the scene headers still reach the release.
	updatePill := sdl.Rect{}
	if updateAvailable() {
		chipW, _ := homeUpdateChipSize(headerCaptionFont(headerConfig))
		if bellX-headerCornerGap-total-headerCornerGap-chipW >= scaleX(480, h.Width) {
			updatePill = drawHeaderUpdatePill(renderer, headerConfig, bellX-headerCornerGap, barH,
				h.Focus.Current() == HomeFocusUpdate)
			if updatePill.W > 0 {
				homeUpdateRect = updatePill
				item := h.Items[HomeFocusUpdate]
				item.Rect = updatePill
				h.Items[HomeFocusUpdate] = item
				total += updatePill.W + headerCornerGap
			}
		}
	}
	x := bellX - headerCornerGap - total

	// Header controls, in the band under the system cluster and clear of the
	// greeting column: the phone-pairing code and the PS4/PS5 launcher pills.
	// The design sheet's header carries exactly these controls.
	bandY := scaleY(78, h.Height)
	bandH := scaleY(44, h.Height)
	companionHeaderRect = sdl.Rect{}
	ps4TileRect, ps5TileRect = sdl.Rect{}, sdl.Rect{}
	bandRight := h.Width - scaleX(homeSideMargin, h.Width)
	qrcSize := scaleY(72, h.Height)
	if qrcSize > 0 && bandY+qrcSize <= h.Height {
		renderCompanionHeaderQR(renderer, fonts, sdl.Rect{X: bandRight - qrcSize, Y: bandY, W: qrcSize, H: qrcSize})
	}
	groupRight := bandRight
	groupY := bandY
	if companionHeaderRect.W > 0 {
		groupRight = companionHeaderRect.X - scaleX(12, h.Width)
		// Level with the code beside it rather than with the band's top edge.
		groupY = companionHeaderRect.Y + (companionHeaderRect.H-bandH)/2
	}
	// The launchers share the band with the search pill under the greeting, so
	// they are dropped rather than drawn over it on a canvas too narrow for both.
	minGroupX := scaleX(480, h.Width)
	if pill := h.Items[HomeFocusSearch].Rect; pill.W > 0 {
		minGroupX = max32(minGroupX, pill.X+pill.W+scaleX(homeSearchPillGap, h.Width))
	}
	if group, p4, p5 := psPillRects(fonts, groupRight, groupY, bandH); group.W > 0 && group.X >= minGroupX {
		renderPSPillGroup(renderer, fonts, group, p4, p5, psPillVisual{
			PS4On:   ps4TileCurrentState() == ps4TileLaunch,
			PS5On:   ps5TileCurrentState() == ps5TileLaunch,
			Focused: h.Focus.Current(),
			Pressed: data.Pressed,
		})
		ps4TileRect, ps5TileRect = p4, p5
	}
	h.Focus.SetEnabled(HomeFocusPS4, ps4TileRect.W > 0)
	h.Focus.SetEnabled(HomeFocusPS5, ps5TileRect.W > 0)
	// Link the launcher with whichever card sits under it, so Up reaches it and
	// Down comes back: the D-pad follows the layout instead of a guess.
	h.Focus.SetTileLink(h.CardUnderTile(ps5TileRect))

	// The network slot and the battery are the two gauged parts — signal bars
	// and a colour-coded cell — shared with every scene's status bar so the
	// system corner looks the same on Home as it does in a menu. The cluster's
	// baseline is the bar's centre, the same as a scene's.
	y := (barH - int32(fonts.Small.Height())) / 2
	drawStatusText := func(text string, color sdl.Color, x, y int32) {
		drawText(renderer, fonts.Small, text, x, y, color, textAlignLeft)
	}
	for _, part := range data.StatusParts {
		w := statusClusterPartWidth(fonts.Small, part)
		r := sdl.Rect{
			X: x - scaleX(8, h.Width),
			Y: y - scaleY(6, h.Height),
			W: w + scaleX(16, h.Width),
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
		if isWiFiStatusPart(part) {
			statusWiFiRect = r
		}
		if isChipStatusPart(part) {
			// The now-playing chip is a header button: clicks land on the
			// measured chip and the D-pad reaches it from the clock. The idle
			// output chip is measured the same way so it draws in the same
			// place, but it stays out of the focus order.
			statusMediaRect = r
			item := h.Items[HomeFocusNowPlaying]
			item.Rect = r
			h.Items[HomeFocusNowPlaying] = item
		}
		drawStatusClusterPart(renderer, fonts.Small, part, x, y, drawStatusText)
		x += w + sepW
	}
	// The network meter is a tap target as well as a reading, so grow its hit
	// area to finger size once the cluster is placed, stopping short of the
	// forecast and the clock so their taps still belong to them.
	if statusWiFiRect.W > 0 {
		statusWiFiRect = statusFingerRect(statusWiFiRect, header.H,
			weatherStatusRect, h.Items[HomeFocusClock].Rect)
	}
	// The meter is a button, not just a reading: the D-pad focuses the same
	// grown hit area a fingertip taps, and the entry is disabled whenever the
	// cluster had to drop the network slot for space.
	if statusWiFiRect.W > 0 {
		item := h.Items[HomeFocusWiFi]
		item.Rect = statusWiFiRect
		h.Items[HomeFocusWiFi] = item
	}
	h.Focus.SetEnabled(HomeFocusWiFi, statusWiFiRect.W > 0)
	// The D-pad may only focus the chip while it is on screen; with no update
	// (or no room) it stays disabled and the arrows skip it.
	h.Focus.SetEnabled(HomeFocusUpdate, homeUpdateRect.W > 0)
	// Chips that are not drawn are neither focusable nor clickable: the
	// weather entry keeps its placeholder rect when there is no forecast, and
	// that rect sits where the now-playing chip is drawn.
	h.Focus.SetEnabled(HomeFocusWeather, weatherStatusRect.W > 0)
	// The chip is a button only while a player is behind it: with nothing
	// playing it may still be drawn (naming the headphones), but pressing it
	// would open nothing, so the arrows skip it.
	h.Focus.SetEnabled(HomeFocusNowPlaying, statusMediaRect.W > 0 && nowPlayingChipIsButton())
	for _, id := range []HomeFocusID{HomeFocusClock, HomeFocusWeather, HomeFocusWiFi, HomeFocusNowPlaying, HomeFocusUpdate, HomeFocusBell, HomeFocusPS5, HomeFocusPS4} {
		if h.Focus.Current() != id {
			continue
		}
		r := h.Items[id].Rect
		switch id {
		case HomeFocusUpdate:
			r = homeUpdateRect
		case HomeFocusPS5:
			r = ps5TileRect
		case HomeFocusPS4:
			r = ps4TileRect
		}
		if r.W <= 0 {
			continue
		}
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 2, ColorAccent)
	}
}

// CardUnderTile returns the card sitting directly below a header tile: same
// column, nearest top edge. The PS5 launcher uses it to claim the Up edge of
// the card beneath it. Header chips and the tile itself are skipped, and an
// empty tile or a tile with nothing under it returns "".
func (h *HomeLayout) CardUnderTile(tile sdl.Rect) HomeFocusID {
	if h == nil || tile.W <= 0 {
		return ""
	}
	center := tile.X + tile.W/2
	best := HomeFocusID("")
	bestY := int32(0)
	for _, id := range h.Order {
		switch id {
		case HomeFocusPS5, HomeFocusPS4, HomeFocusClock, HomeFocusWeather, HomeFocusBell,
			HomeFocusNowPlaying, HomeFocusWiFi, HomeFocusExit, HomeFocusSearch, HomeFocusUpdate:
			continue
		}
		item, ok := h.Items[id]
		if !ok {
			continue
		}
		r := item.Rect
		if center < r.X || center >= r.X+r.W || r.Y < tile.Y+tile.H {
			continue
		}
		if best == "" || r.Y < bestY {
			best, bestY = id, r.Y
		}
	}
	return best
}

// renderHomeBell draws the notification bell in the home header's top-right
// corner. It mirrors the status-bar bell so the system corner is identical on
// every screen, and it records the rect the click handler uses.
func renderHomeBell(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32) {
	if font == nil {
		return
	}
	// The pill, icon and unread badge come from the shared notification-center
	// renderer; only the home header's position differs from the scene bar.
	drawNotifBell(renderer, config, font, sdl.Rect{X: x, Y: y, W: w, H: h})
}

func (h *HomeLayout) drawGreeting(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) {
	greeting := strings.TrimSpace(data.Greeting)
	if greeting == "" {
		greeting = defaultGreeting(time.Now())
	}
	// The greeting heads the column the search pill sits in, so it is cut to
	// the pill's width: a long user name must not run under the launchers.
	if fonts.Greeting != nil {
		greeting = ellipsize(fonts.Greeting, greeting, scaleX(homeSearchPillW, h.Width))
	}
	drawText(renderer, fonts.Greeting, greeting, scaleX(homeSearchPillX, h.Width), scaleY(homeGreetingY, h.Height), ColorTextPrimary(), textAlignLeft)
}

// homeSearchHints are the search pill's placeholder, longest first: the pill
// shows the first one that fits, so the compact canvas reads "Search apps,
// settings and files" instead of an ellipsized sentence.
var homeSearchHints = []string{
	"Search apps, settings, files and the web",
	"Search apps, settings and files",
	"Search JukaHub",
	"Search",
}

// homeSearchHintFor returns the placeholder for a pill whose text column is
// maxW wide. A caller-supplied hint (HomeViewData.Subtitle) wins; otherwise the
// longest built-in hint that fits.
func homeSearchHintFor(font *ttf.Font, custom string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return ""
	}
	if c := strings.TrimSpace(custom); c != "" {
		return ellipsize(font, c, maxW)
	}
	for _, hint := range homeSearchHints {
		if w, _ := measureText(font, hint); w <= maxW {
			return hint
		}
	}
	return ellipsize(font, homeSearchHints[len(homeSearchHints)-1], maxW)
}

// drawSearchPill paints the search field under the greeting, the way a phone
// launcher's search bar looks: a glass pill with the search glyph, a muted
// placeholder and, at the right end, the pad shortcut that opens it from
// anywhere on Home. It is a button — A, Enter or a tap opens the search sheet
// (globalsearch.go) — and takes Home's one focus treatment: bloom, lift, ring.
func (h *HomeLayout) drawSearchPill(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth, lift float64, hint string) {
	font := fonts.Helper
	if font == nil {
		font = fonts.Small
	}
	if font == nil || item.Rect.W <= 0 || item.Rect.H <= 0 {
		return
	}
	draw := scaleHomeCard(item.Rect, lift)
	draw.Y += homePressOffset(pressDepth, h.Height)
	radius := item.Rect.H / 2
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	fill, edge := homeGlass(18), homeGlass(36)
	switch {
	case pressed:
		fill = homeGlass(30)
	case focused:
		fill = homeGlass(26)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRectAt(renderer, item.Rect, draw, radius, fill)
	if focused {
		strokeRoundedRectAt(renderer, item.Rect, draw, radius, 3, ColorAccent)
	} else {
		strokeRoundedRectAt(renderer, item.Rect, draw, radius, 1, edge)
	}

	// The glyph sits in the rounded end like the magnifier of a phone search
	// bar; the placeholder follows it in the muted ink.
	glyph := min32(draw.H-scaleY(16, h.Height), scaleY(22, h.Height))
	if glyph < 12 {
		glyph = 12
	}
	padX := draw.H / 2
	icon := sdl.Rect{X: draw.X + padX - glyph/3, Y: draw.Y + (draw.H-glyph)/2, W: glyph, H: glyph}
	DrawHomeIcon(renderer, "search", icon, false)

	// The shortcut badge: "Y" in a glass chip, like the footer's hint badges.
	right := draw.X + draw.W - padX/2
	if small := fonts.Small; small != nil {
		const key = "Y"
		kw, _ := measureText(small, key)
		badgeH := draw.H - scaleY(14, h.Height)
		badgeW := max32(badgeH, kw+scaleX(12, h.Width))
		if badgeH >= 14 {
			badge := sdl.Rect{X: draw.X + draw.W - scaleX(10, h.Width) - badgeW, Y: draw.Y + (draw.H-badgeH)/2, W: badgeW, H: badgeH}
			fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, homeGlass(16))
			strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, homeGlass(45))
			drawText(renderer, small, key, badge.X+badge.W/2, badge.Y+(badge.H-int32(small.Height()))/2, homeGlass(210), textAlignCenter)
			right = badge.X
		}
	}

	textX := icon.X + icon.W + scaleX(10, h.Width)
	text := homeSearchHintFor(font, hint, right-scaleX(10, h.Width)-textX)
	if text == "" {
		return
	}
	drawText(renderer, font, text, textX, draw.Y+(draw.H-int32(font.Height()))/2, ColorTextMuted(), textAlignLeft)
}

// fitHomeTitle picks the largest of the card fonts that prints the whole title
// inside maxW. The bottom feature row on the compact 4:3 canvas is only ~100px
// wide per card, and a title that ellipsizes there ("Favo…", "Brow…") reads as
// a broken label, so the type steps down a tier instead before it truncates.
func fitHomeTitle(fonts HomeFonts, text string, maxW int32) (*ttf.Font, string) {
	for _, font := range []*ttf.Font{fonts.Card, fonts.Helper, fonts.Small} {
		if font == nil {
			continue
		}
		// Cached: ten cards are sized every frame and each one otherwise
		// walked the candidate fonts with its own SizeUTF8 calls.
		if w, _ := measureText(font, text); w <= maxW {
			return font, text
		}
	}
	fallback := fonts.Helper
	if fallback == nil {
		fallback = fonts.Card
	}
	if fallback == nil {
		return fonts.Card, text
	}
	return fallback, ellipsize(fallback, text, maxW)
}

func (h *HomeLayout) drawActionCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth, lift float64) {
	if fonts.Card == nil {
		return
	}
	rect := scaleHomeCard(item.Rect, lift)
	draw := rect
	draw.Y += homePressOffset(pressDepth, h.Height)
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	drawGlassTile(renderer, item.Rect, draw, focused, pressed)

	// One composition for every tile: the icon badge in the top-left corner and
	// the label stacked in the bottom-left. A card that centred its label read as
	// a different control, and the old stacked variant could put two glyphs on a
	// single card.
	pad := scaleX(homeTilePad, h.Width)
	iconSize := min32(scaleX(homeIconSize, h.Width), scaleY(homeIconSize, h.Height))
	if maxIcon := min32(draw.W-pad*2, draw.H-pad*2-scaleY(homeGlyphSize, h.Height)); iconSize > maxIcon {
		iconSize = maxIcon
	}
	if iconSize < scaleY(22, h.Height) {
		return
	}
	badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + pad, W: iconSize, H: iconSize}
	glyph := drawIconBadge(renderer, badge, false)
	DrawHomeIcon(renderer, item.Icon, glyph, false)

	textW := draw.W - pad*2
	if textW < 24 {
		textW = 24
	}
	titleFont := fonts.Tile
	if titleFont == nil {
		titleFont = fonts.Card
	}
	subFont := fonts.Meta
	if subFont == nil {
		subFont = fonts.Helper
	}
	title := ellipsize(titleFont, item.Title, textW)
	subtitle := ""
	if subFont != nil {
		subtitle = ellipsize(subFont, item.Subtitle, textW)
	}
	titleH := int32(titleFont.Height())
	subH := int32(0)
	if subFont != nil {
		subH = int32(subFont.Height())
	}
	textY := draw.Y + draw.H - pad - subH - titleH
	if subtitle != "" {
		textY -= scaleY(2, h.Height)
	}
	if textY < badge.Y+badge.H+scaleY(4, h.Height) {
		// The tile is too short for both blocks: keep the label below the badge
		// rather than letting the two collide.
		textY = badge.Y + badge.H + scaleY(4, h.Height)
	}
	drawText(renderer, titleFont, title, draw.X+pad, textY, ColorTextPrimary(), textAlignLeft)
	if subtitle != "" {
		drawText(renderer, subFont, subtitle, draw.X+pad, textY+titleH+scaleY(2, h.Height), ColorTextMuted(), textAlignLeft)
	}
}

// drawMediaCard paints the accent tile that opens the media library: the one
// card on the screen with a tinted face, a cyan edge and the waveform that says
// "watch and listen" without a screenshot.
func (h *HomeLayout) drawMediaCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth, lift float64) {
	if fonts.Card == nil {
		return
	}
	rect := scaleHomeCard(item.Rect, lift)
	draw := rect
	draw.Y += homePressOffset(pressDepth, h.Height)

	// A cyan->blue->violet wash at low alpha: the background still reads through
	// it, which is what makes this the accent tile and not a second palette.
	left := sdl.Color{R: ColorAccent.R, G: ColorAccent.G, B: ColorAccent.B, A: 74}
	mid := sdl.Color{R: 59, G: 130, B: 246, A: 58}
	right := sdl.Color{R: 124, G: 58, B: 237, A: 46}
	if focused {
		left.A, mid.A, right.A = 94, 74, 58
	}
	if pressed {
		left.A, mid.A, right.A = left.A+14, mid.A+14, right.A+14
	}
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// Two stops rather than three: the middle colour is the blend of the ends, so
	// the wash has no seam where a second pass would meet the first.
	fillRoundedGradient(renderer, draw, homeTileRadius, left, right, true)
	_ = mid
	strokeRoundedRect(renderer, draw.X, draw.Y, draw.W, draw.H, homeTileRadius, 1, WithAlpha(ColorAccent, 110))
	if focused {
		drawHomeFocusRing(renderer, item.Rect, draw, homeTileRadius)
	}

	// The waveform fills the right end of the tile. It is drawn before the label
	// so the text ellipsizes short of it instead of being painted over.
	waveW := min32(scaleX(200, h.Width), draw.W/3)
	wave := sdl.Rect{
		X: draw.X + draw.W - waveW - scaleX(16, h.Width),
		Y: draw.Y + scaleY(26, h.Height),
		W: waveW, H: draw.H - scaleY(52, h.Height),
	}
	drawWaveform(renderer, wave, WithAlpha(ColorAccent, 30))

	pad := scaleX(24, h.Width)
	iconSize := min32(scaleY(56, h.Height), draw.H-scaleY(28, h.Height))
	badge := sdl.Rect{X: draw.X + pad, Y: draw.Y + (draw.H-iconSize)/2, W: iconSize, H: iconSize}
	glyph := drawIconBadge(renderer, badge, true)
	drawHomeGlyphTinted(renderer, item.Icon, glyph, ColorAccent)

	textX := badge.X + badge.W + scaleX(20, h.Width)
	textW := wave.X - scaleX(12, h.Width) - textX
	if textW < 48 {
		textW = 48
	}
	title := ellipsize(fonts.Card, item.Title, textW)
	subtitle := ""
	if fonts.Helper != nil {
		subtitle = ellipsize(fonts.Helper, item.Subtitle, textW)
	}
	titleH := int32(fonts.Card.Height())
	subH := int32(0)
	if fonts.Helper != nil {
		subH = int32(fonts.Helper.Height())
	}
	textY := draw.Y + (draw.H-titleH-subH)/2
	drawText(renderer, fonts.Card, title, textX, textY, ColorTextPrimary(), textAlignLeft)
	if subtitle != "" {
		drawText(renderer, fonts.Helper, subtitle, textX, textY+titleH, ColorTextMuted(), textAlignLeft)
	}
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

// drawContinueCard paints the hero card: the preview of what Play resumes (a
// video frame, box art or the ambient artwork, with an audio graph for music),
// the chip naming the action, the play button, title, progress and the pill.
func (h *HomeLayout) drawContinueCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, pressDepth float64, state HomeContinueState, lift float64) {
	if fonts.Small == nil || fonts.Card == nil {
		return
	}
	rect := scaleHomeCard(item.Rect, lift)
	draw := rect
	draw.Y += homePressOffset(pressDepth, h.Height)
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	// The artwork: the video's frame or the game's box art once the worker has
	// made it (home_continue.go), the ambient blooms until then. The preview is
	// sized to the settled card and already carries its rounded corners.
	homeRequestPreview(state.Preview, item.Rect.W, item.Rect.H)
	if art, _, _ := homePreviewTexture(renderer, state.Preview, item.Rect.W, item.Rect.H); art != nil {
		_ = renderer.Copy(art, nil, &draw)
		// A scrim from the middle down keeps the labels readable over any
		// frame, the same ramp the ambient artwork uses.
		fillVerticalScrim(renderer, draw, draw.Y+draw.H/3,
			sdl.Color{R: 0, G: 0, B: 0, A: 0}, sdl.Color{R: 0, G: 0, B: 0, A: 215})
	} else {
		drawContinueThumb(renderer, draw)
	}
	if focused {
		// The hero card is large and its thumbnail is bright, so the standard
		// 3px accent ring read as part of the artwork. Four pixels outside a
		// dark inner hairline keeps the selection obvious on every theme.
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 4, ColorAccent)
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius-1, 1, WithAlpha(ColorIconDark, 120))
	} else {
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 1, homeCardEdge())
	}

	pad := scaleX(20, h.Width)

	// Continue chip, top-left: the card says what pressing it will do.
	chipText := "Continue watching"
	if c := strings.TrimSpace(state.Chip); c != "" {
		chipText = c
	}
	if !state.HasRecent {
		chipText = "Ready when you are"
	}
	label := strings.ToUpper(chipText)
	chipW, _ := measureText(fonts.Small, label)
	chipW += scaleX(20, h.Width)
	maxChipW := draw.W - pad*2 - scaleY(72, h.Height)
	if chipW > maxChipW {
		label = ellipsize(fonts.Small, label, maxChipW-scaleX(20, h.Width))
		chipW = maxChipW
	}
	chipH := int32(fonts.Small.Height()) + scaleY(10, h.Height)
	chip := sdl.Rect{X: draw.X + pad, Y: draw.Y + scaleY(20, h.Height), W: chipW, H: chipH}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 8, WithAlpha(ColorAccent, 38))
	strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 8, 1, WithAlpha(ColorAccent, 90))
	drawText(renderer, fonts.Small, label, chip.X+scaleX(10, h.Width),
		chip.Y+(chip.H-int32(fonts.Small.Height()))/2, ColorAccent, textAlignLeft)

	// Round play button, top-right.
	playSize := scaleY(56, h.Height)
	if playSize > draw.W/4 {
		playSize = draw.W / 4
	}
	play := sdl.Rect{X: draw.X + draw.W - pad - playSize, Y: draw.Y + scaleY(20, h.Height), W: playSize, H: playSize}
	drawPlayBadge(renderer, play)

	title := strings.TrimSpace(state.Title)
	subtitle := strings.TrimSpace(state.Subtitle)
	action := "Open Media"
	if state.HasRecent {
		action = "Resume"
		if a := strings.TrimSpace(state.Action); a != "" {
			action = a
		}
		if title == "" {
			title = "Continue playing"
		}
		if subtitle == "" {
			subtitle = "Pick up where you left off."
		}
	} else {
		if title == "" {
			title = "Nothing played recently"
		}
		if subtitle == "" {
			subtitle = "Open Media to start watching or listening."
		}
	}

	pillH := scaleY(44, h.Height)
	pillY := draw.Y + draw.H - scaleY(24, h.Height) - pillH
	progressH := scaleY(8, h.Height)
	progressY := pillY - scaleY(18, h.Height) - progressH
	textW := draw.W - pad*2
	titleText := ellipsize(fonts.Card, title, textW)
	titleH := int32(fonts.Card.Height())
	metaText := ""
	metaH := int32(0)
	if fonts.Helper != nil {
		metaText = ellipsize(fonts.Helper, subtitle, textW)
		metaH = int32(fonts.Helper.Height())
	}
	metaY := progressY - scaleY(14, h.Height) - metaH
	titleY := metaY - scaleY(6, h.Height) - titleH
	if titleY < chip.Y+chip.H+scaleY(8, h.Height) {
		// A config can make this card shorter than the design's 320px; drop the
		// meta line rather than letting the title run into the chip.
		titleY = chip.Y + chip.H + scaleY(8, h.Height)
		metaY = titleY + titleH + scaleY(6, h.Height)
	}
	// Music shows its audio graph in the band between the play button and the
	// title: the track's own levels, breathing while it plays.
	if state.HasWave {
		graphTop := play.Y + play.H + scaleY(12, h.Height)
		graphBottom := titleY - scaleY(12, h.Height)
		if graphBottom-graphTop >= scaleY(24, h.Height) {
			graph := sdl.Rect{X: draw.X + pad, Y: graphTop, W: draw.W - 2*pad, H: graphBottom - graphTop}
			ink := WithAlpha(ColorAccent, 170)
			if !state.WaveReal {
				ink = WithAlpha(ColorAccent, 110)
			}
			drawHomeAudioGraph(renderer, graph, &state.Wave, state.Live, ink)
		}
	}
	drawText(renderer, fonts.Card, titleText, draw.X+pad, titleY, ColorTextPrimary(), textAlignLeft)
	if metaText != "" && metaY+metaH <= progressY {
		drawText(renderer, fonts.Helper, metaText, draw.X+pad, metaY, ColorTextMuted(), textAlignLeft)
	}

	if state.HasRecent && state.Progress > 0 {
		labelW := int32(0)
		remaining := strings.TrimSpace(state.Remaining)
		if remaining != "" {
			labelW, _ = measureText(fonts.Small, remaining)
			labelW += scaleX(12, h.Width)
		}
		track := sdl.Rect{X: draw.X + pad, Y: progressY, W: textW - labelW, H: progressH}
		drawProgressBar(renderer, track, state.Progress, ColorAccent)
		if remaining != "" {
			drawText(renderer, fonts.Small, remaining, track.X+track.W+scaleX(12, h.Width),
				track.Y+(track.H-int32(fonts.Small.Height()))/2, ColorTextMuted(), textAlignLeft)
		}
	}

	// Resume pill, bottom-left: the A glyph makes the controller path explicit.
	glyphD := pillH - scaleY(16, h.Height)
	actionW, _ := measureText(fonts.Small, action)
	pillW := scaleX(16, h.Width) + glyphD + scaleX(10, h.Width) + actionW + scaleX(18, h.Width)
	if pillW > draw.W-pad*2 {
		pillW = draw.W - pad*2
	}
	fillRoundedRect(renderer, draw.X+pad, pillY, pillW, pillH, pillH/2, ColorAccent)
	glyphX := draw.X + pad + scaleX(8, h.Width)
	fillCircle(renderer, glyphX+glyphD/2, pillY+pillH/2, glyphD/2, WithAlpha(ColorIconDark, 60))
	drawText(renderer, fonts.Small, "A", glyphX+glyphD/2, pillY+(pillH-int32(fonts.Small.Height()))/2, ColorIconDark, textAlignCenter)
	drawText(renderer, fonts.Small, action, glyphX+glyphD+scaleX(10, h.Width),
		pillY+(pillH-int32(fonts.Small.Height()))/2, ColorIconDark, textAlignLeft)
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

	// The label is ellipsized to the space left of the glyph: a config can
	// rename this chip (item.Title), and an unmetered label would overflow
	// the pill it is drawn in.
	labelX := iconRect.X + iconRect.W + scaleX(8, h.Width)
	maxLabelW := rect.X + rect.W - scaleX(10, h.Width) - labelX
	if maxLabelW < scaleY(12, h.Height) {
		maxLabelW = scaleY(12, h.Height)
	}
	chipLabel := ellipsize(fonts.Small, item.Title, maxLabelW)
	drawText(renderer, fonts.Small, chipLabel, labelX,
		rect.Y+(rect.H-int32(fonts.Small.Height()))/2, fg, textAlignLeft)
}

// footerHint pairs a controller button label with the action it performs.
type footerHint struct {
	button string
	label  string
}

func (h *HomeLayout) drawFooter(renderer *sdl.Renderer, fonts HomeFonts, showBack bool) {
	// The footer is a 56px translucent band with rounded top corners, inset by
	// the frame margin like every other row; the Exit pill is a control on it.
	footer := ScaleHomeRect(sdl.Rect{X: homeSideMargin, Y: 664, W: 1216, H: 56}, h.Width, h.Height)
	if footer.H <= 0 {
		return
	}
	radius := scaleY(12, h.Height)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, footer.X, footer.Y, footer.W, footer.H, radius, sdl.Color{R: 0, G: 0, B: 0, A: 70})
	if radius > 0 {
		// Squared bottom edge: only the top corners are rounded.
		setDrawColor(renderer, sdl.Color{R: 0, G: 0, B: 0, A: 70})
		_ = renderer.FillRect(&sdl.Rect{X: footer.X, Y: footer.Y + footer.H - radius, W: footer.W, H: radius})
	}
	setDrawColor(renderer, homeGlass(16))
	_ = renderer.DrawLine(footer.X, footer.Y, footer.X+footer.W, footer.Y)
	if fonts.Small == nil {
		return
	}

	// The hint sets are fixed arrays: slicing one costs nothing, where building
	// the list with append allocated on every frame Home was on screen.
	hints := homeFooterHintsStart[:]
	if showBack {
		hints = homeFooterHintsBack[:]
	}

	// Right-aligned hint groups: a circular glyph badge and its label.
	itemGap := scaleX(18, h.Width)
	badgeD := scaleY(22, h.Height)
	badgeGap := scaleX(7, h.Width)
	fh := int32(fonts.Small.Height())

	var badgeWs, groupWs [len(homeFooterHintsStart)]int32
	total := int32(0)
	for i, g := range hints {
		bw, _ := measureText(fonts.Small, g.button)
		lw, _ := measureText(fonts.Small, g.label)
		badgeWs[i] = max32(badgeD, bw+scaleX(12, h.Width))
		groupWs[i] = badgeWs[i] + badgeGap + lw
		total += groupWs[i]
	}
	if len(hints) > 1 {
		total += itemGap * int32(len(hints)-1)
	}
	// The hints must stay clear of the Exit pill at the band's left end: on a
	// narrow canvas the least specific hint (the D-pad) is dropped first.
	limit := footer.X + scaleX(16, h.Width)
	if exit := h.Items[HomeFocusExit].Rect; exit.W > 0 {
		limit = exit.X + exit.W + scaleX(16, h.Width)
	}
	right := footer.X + footer.W - scaleX(16, h.Width)
	first := 0
	for first < len(hints)-1 && right-total < limit {
		total -= groupWs[first] + itemGap
		first++
	}

	x := right - total
	textY := footer.Y + (footer.H-fh)/2
	badgeY := footer.Y + (footer.H-badgeD)/2
	for i := first; i < len(hints); i++ {
		g := hints[i]
		badge := sdl.Rect{X: x, Y: badgeY, W: badgeWs[i], H: badgeD}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, homeGlass(16))
		strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, homeGlass(45))
		drawText(renderer, fonts.Small, g.button, badge.X+badge.W/2,
			badge.Y+(badge.H-fh)/2, homeGlass(210), textAlignCenter)
		drawText(renderer, fonts.Small, g.label, badge.X+badge.W+badgeGap, textY, ColorTextMuted(), textAlignLeft)
		x += groupWs[i] + itemGap
	}
}

// homeFooterHintsStart is Home's footer: Home is the root scene, so B has
// nothing to go back to and the footer names Start, the menu that reaches
// every other screen. homeFooterHintsBack is the same row for a layout that
// does have somewhere to go back to. Both must stay the same length.
var (
	homeFooterHintsStart = [...]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Open"},
		{button: "Y", label: "Search"},
		{button: "Start", label: "Menu"},
		{button: "L2/R2", label: "Sections"},
	}
	homeFooterHintsBack = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Open"},
		{button: "Y", label: "Search"},
		{button: "B", label: "Back"},
		{button: "L2/R2", label: "Sections"},
	}
)

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
	// w and h are the texture's size. drawText used to ask the texture on
	// every frame (texture.Query), which is a cgo round trip that allocates
	// its result and its error value for a number this cache already knows.
	w, h int32
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

// The home label ceilings. Labels are small (a title is ~10-40 KB), so a few MB
// holds thousands of strings; the handheld gets the tighter pair because its
// 1 GB is shared with the game it launches.
const (
	homeTextBudgetHandheldBytes = int64(8 << 20)
	homeTextBudgetDesktopBytes  = int64(48 << 20)
	homeTextHandheldItems       = 512
	homeTextDesktopItems        = 2048
)

// homeTextCacheBudgetBytes is the memory budget for drawText labels.
func homeTextCacheBudgetBytes() int64 {
	return sceneBudgetByDevice(homeTextBudgetHandheldBytes, homeTextBudgetDesktopBytes)
}

// homeTextCacheMaxItems bounds the entry count as well as the bytes: many
// tiny strings would otherwise never hit the byte budget.
func homeTextCacheMaxItems() int {
	return int(sceneBudgetByDevice(homeTextHandheldItems, homeTextDesktopItems))
}

// homeTextCacheApplyBudget retunes the live label cache from the device branch.
// The store is a package variable built at init, so pinning the branch after the
// fact — the scene-navigation harness does exactly that — has to move the
// operator's ceilings too, the way TextCache.SetBudget does for rendered text.
func homeTextCacheApplyBudget() {
	homeTextCache.maxBytes = homeTextCacheBudgetBytes()
	homeTextCache.maxItems = homeTextCacheMaxItems()
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
	entry := &homeTextCacheEntry{texture: texture, key: key, bytes: bytes, w: w, h: h}
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
	// .notdef boxes (see fonts.go). Supported text is returned unchanged;
	// the answer is cached because this runs for every label on every frame.
	text = sanitizeCached(font, text)

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
		tw, th = cached.w, cached.h
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

	// A zero-area destination is not a no-op to SDL: RenderCopy rejects it and
	// builds an error value (a C string copy plus errors.New) that nobody
	// reads. Space-only labels reach this path, so the guard is what keeps a
	// steady frame from allocating two objects per blank label.
	if tw <= 0 || th <= 0 {
		return
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
	DrawHomeCardSurfaceAt(renderer, rect, rect, focused, pressed)
}

// DrawHomeCardSurfaceAt draws the card's surface and border at rect, taking
// their shape from base's size. The focus lift grows the card by up to three
// pixels; shaping it from the settled rect means the whole lift animation is
// one cached texture per card instead of one per pixel it grows, on the screen
// whose cards are the largest textures in the shell.
func DrawHomeCardSurfaceAt(renderer *sdl.Renderer, base, rect sdl.Rect, focused, pressed bool) {
	drawGlassTile(renderer, base, rect, focused, pressed)
}

// The shared icon painters (DrawHomeIcon, drawHomeGlyphTinted,
// drawHomeIconShapes) live in icondraw.go; the glyphs themselves are vector
// definitions in icondefs.go.

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
	// Cached: the measurement is several FreeType calls through cgo, and the
	// UI re-clips the same unchanged labels on every frame. See textcache.go.
	return fitTextCached(font, text, maxW)
}

func insetRect(rect sdl.Rect, inset int32) sdl.Rect {
	return sdl.Rect{
		X: rect.X + inset,
		Y: rect.Y + inset,
		W: max32(0, rect.W-inset*2),
		H: max32(0, rect.H-inset*2),
	}
}
