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
	HomeActionVolume         HomeAction = "toggle_volume"
	HomeActionBrightness     HomeAction = "toggle_brightness"
	HomeActionBluetooth      HomeAction = "open_bluetooth"
	HomeActionQR             HomeAction = "show_pairing_qr"
	HomeActionTrophies       HomeAction = "open_trophies"
	HomeActionBugReport      HomeAction = "report_bug"
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
	BuildDate   string
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

	// The focused card grows by a few pixels, then settles after a tiny
	// overshoot. Motion is disabled by reduced motion.
	Motion     bool
	focusEnter uiEnter
	focusID    HomeFocusID
	// Glide lets the focus ring travel between controls with a small
	// overshoot (homefocusglide.go). Unlike Motion it stays on in Low Power:
	// it is one ring moving for 300 ms.
	Glide bool

	// Arrangement is the alternative layout in force (homeArrangementCustom
	// when the scene's own geometry is authoritative).
	Arrangement string

	// geometryEdited is set when jukaconfig.json moved a body card off its
	// built-in rect: the hand-tuned graph (homeFocusGraph) describes the
	// built-in composition, so focus then steps by the drawn geometry instead.
	geometryEdited bool

	// dyn is the body of the Carousel and Widgets arrangements, which place
	// the tile list instead of the built-in cards (home_dynbody.go).
	dyn homeDyn
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
		Items:  make(map[HomeFocusID]HomeItem, 23),
		// The search pill comes after the cards: its grown touch target reaches
		// a few pixels toward the Continue card, and a tap on the card's own
		// edge must stay the card's. The Bluetooth rune comes before the meter
		// beside it, as core.go's bar click tests it.
		Order: []HomeFocusID{
			HomeFocusContinue, HomeFocusMedia, HomeFocusFiles,
			HomeFocusPackages, HomeFocusApps, HomeFocusFavorites,
			HomeFocusBrowser, HomeFocusChat, HomeFocusTools, HomeFocusEmulator,
			HomeFocusSettings, HomeFocusSearch, HomeFocusUpdate, HomeFocusBrightness,
			HomeFocusVolume, HomeFocusBluetooth, HomeFocusWiFi, HomeFocusWeather, HomeFocusClock,
			// The bell is hit-tested before the trophy room's cup beside it,
			// as main.go's click path tests it, so a hover and a click on the
			// strip where their touch targets meet pick the same control.
			HomeFocusNowPlaying, HomeFocusBell, HomeFocusBug, HomeFocusTrophy, HomeFocusExit, HomeFocusQR,
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
//
// Carousel and Widgets do not place the built-in cards at all: they lay out
// the tile list the user edits (home_tiles.go, home_dynbody.go).
const (
	homeArrangementCustom    = "custom"
	homeArrangementDashboard = "dashboard"
	homeArrangementGrid      = "grid"
	homeArrangementCarousel  = "carousel"
	homeArrangementWidgets   = "widgets"
)

// homeArrangementLabel is the name the UI shows for an arrangement.
func homeArrangementLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case homeArrangementDashboard:
		return "Dashboard"
	case homeArrangementGrid:
		return "Hero grid"
	case homeArrangementCarousel:
		return "Carousel"
	case homeArrangementWidgets:
		return "Widgets"
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
	case homeArrangementCarousel:
		return homeArrangementCarousel
	case homeArrangementWidgets, "widget", "tiles":
		return homeArrangementWidgets
	}
	return homeArrangementCustom
}

// homeArrangementIsDynamic reports whether an arrangement lays out the tile
// list (Carousel, Widgets) rather than the built-in cards.
func homeArrangementIsDynamic(name string) bool {
	switch normalizeHomeArrangement(name) {
	case homeArrangementCarousel, homeArrangementWidgets:
		return true
	}
	return false
}

// homeArrangementChoiceList is what Settings → Home layout and Edit Home's
// strip offer, in order. The dashboard is not listed apart: on an unedited
// config it is what Standard already shows, and two identical choices would
// only confuse — a theme that installs it shows as Standard.
var homeArrangementChoiceList = [...]string{
	homeArrangementCustom, homeArrangementGrid, homeArrangementCarousel, homeArrangementWidgets,
}

// homeArrangementChoiceLabel is a choice's name in Settings and on the strip.
func homeArrangementChoiceLabel(name string) string {
	switch normalizeHomeArrangement(name) {
	case homeArrangementGrid:
		return "Hero grid"
	case homeArrangementCarousel:
		return "Carousel"
	case homeArrangementWidgets:
		return "Widgets"
	}
	return "Standard"
}

// homeArrangementChoiceIndex is an arrangement's place in
// homeArrangementChoiceList; the dashboard (and anything unknown) is Standard.
func homeArrangementChoiceIndex(name string) int {
	n := normalizeHomeArrangement(name)
	for i, c := range homeArrangementChoiceList {
		if c == n {
			return i
		}
	}
	return 0
}

// homeSetArrangement stores the user's layout choice and applies it to the
// live Home at once.
func homeSetArrangement(config *Config, name string) {
	next := normalizeHomeArrangement(name)
	if config != nil {
		setStoreString(config, "homeArrangement", next)
	}
	if homeLayout != nil {
		homeLayout.SetArrangement(next)
	}
}

// homeArrangement is the arrangement the user is on. The unset case is Custom:
// the shipped configs place their own cards, so an unset value must not move
// anything.
func homeArrangement(config *Config) string {
	// Edit Home opened on Standard or the Hero grid shows Widgets without
	// saving it until something is changed (home_edit.go homeEdit.restore).
	if homeEdit.open && homeEdit.restore != "" {
		return homeArrangementWidgets
	}
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
	// occupies. Games follows Media, as in the dashboard.
	rest := []HomeFocusID{
		HomeFocusMedia, HomeFocusEmulator, HomeFocusPackages,
		HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser,
		HomeFocusChat, HomeFocusTools, HomeFocusFiles, HomeFocusSettings,
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
	if !homeArrangementIsDynamic(next) && h.dyn.active {
		// Back to the built-in cards: the order, the cards' focus and the
		// items the tiles added are put back first.
		h.dynDeactivate()
	}
	h.rebuildItems()
	// The tile layouts build their body on the next refresh, from the config
	// (dynSync); forget what the last one was built from.
	h.dyn.sig = homeDynSig{}
}

func (h *HomeLayout) rebuildItems() {
	if h == nil {
		return
	}
	// A built-in arrangement moves the body cards; the rects below are the
	// dashboard composition and stay the default for Custom. Both are in the
	// 1280x720 design space and scaled to the canvas once, by card(): asking
	// for the arrangement at the canvas size scaled the Brick's cards twice.
	arrangementRects := homeArrangementRects(h.Arrangement, HomeBaseWidth, HomeBaseHeight)
	h.geometryEdited = false
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
	// Games heads the row under Media, beside the hero: playing the library is
	// what a gaming handheld is picked up for, so it is the first tile the
	// D-pad meets after Continue and Media rather than the fifth of the bottom
	// row. It opens the game library (games by console, with box art), not the
	// emulator manager; "Emulator" named the plumbing instead of the thing.
	card(HomeFocusEmulator, "Games", "Play your library", "emulator", HomeActionEmulator,
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
	card(HomeFocusFiles, "Files", "Browse storage", "files", HomeActionFiles,
		sdl.Rect{X: 854, Y: 496, W: 189, H: 152})
	card(HomeFocusSettings, "Settings", "Device and design", "settings", HomeActionSettings,
		sdl.Rect{X: 1059, Y: 496, W: 189, H: 152})
	// The search pill under the greeting. It is a control, not a menu card, so
	// a config tile cannot move it (homeConfigOverlayables).
	card(HomeFocusSearch, "Search", "Apps, settings, files and the web", "search", HomeActionSearch,
		sdl.Rect{X: homeSearchPillX, Y: homeSearchPillY, W: homeSearchPillW, H: homeSearchPillH})
	// The power button at the footer band's left end (home_power.go): a glass
	// pill that asks before it quits. This is its slot, the design sheet's; the
	// pill is drawn at its label's width inside it.
	card(HomeFocusExit, "Exit", "Quit JukaHub", "power", HomeActionExit,
		sdl.Rect{X: 48, Y: 674, W: 116, H: 36})
	// The header chips (sun, speaker, Bluetooth rune, signal meter, weather,
	// clock, now-playing, bell, update) are the header text itself: drawHeader
	// replaces every one of these rects with what it measured this frame. They
	// keep placeholders so a tap or a D-pad press before the first frame still
	// resolves to a control.
	card(HomeFocusWiFi, "Wi-Fi", "Connect to a network", "wifi", HomeActionWiFi,
		sdl.Rect{X: 930, Y: 10, W: 52, H: 36})
	card(HomeFocusBluetooth, "Bluetooth", "Connect a device", "bluetooth", HomeActionBluetooth,
		sdl.Rect{X: 890, Y: 10, W: 34, H: 36})
	card(HomeFocusWeather, "Weather", "Local forecast", "weather", HomeActionToggleWeather,
		sdl.Rect{X: 1000, Y: 10, W: 92, H: 36})
	card(HomeFocusClock, "Clock", "Toggle calendar", "clock", HomeActionToggleCalendar,
		sdl.Rect{X: 1090, Y: 10, W: 76, H: 36})
	card(HomeFocusNowPlaying, "Now playing", "Open the player", "media", HomeActionNowPlaying,
		sdl.Rect{X: 1160, Y: 10, W: 44, H: 36})
	card(HomeFocusBell, "Notifications", "Recent alerts", "bell", HomeActionNotifications,
		sdl.Rect{X: 1204, Y: 10, W: 46, H: 36})
	card(HomeFocusBug, "Report a bug", "Send a report", "bug", HomeActionBugReport,
		sdl.Rect{X: 1180, Y: 10, W: 36, H: 36})
	card(HomeFocusTrophy, "Trophies", "Your trophies and playtime", "trophy", HomeActionTrophies,
		sdl.Rect{X: 1160, Y: 10, W: 36, H: 36})
	card(HomeFocusUpdate, "Update", "Install the new release", "update", HomeActionUpdate,
		sdl.Rect{X: 900, Y: 12, W: 26, H: 24})
	card(HomeFocusVolume, "Volume", "Change the volume", "volume", HomeActionVolume,
		sdl.Rect{X: 850, Y: 10, W: 34, H: 36})
	card(HomeFocusBrightness, "Brightness", "Change the screen brightness", "brightness", HomeActionBrightness,
		sdl.Rect{X: 810, Y: 10, W: 34, H: 36})
	// The pairing code's slot in the header band; drawHeader replaces it with
	// the tile renderCompanionHeaderQR drew (companionHeaderRect).
	card(HomeFocusQR, "Pairing code", "Show the QR code full screen", "qr", HomeActionQR,
		sdl.Rect{X: 1148, Y: 52, W: 100, H: 100})
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
		HomeFocusBug:        false,
		HomeFocusTrophy:     false,
		HomeFocusNowPlaying: false,
		HomeFocusWiFi:       false,
		HomeFocusVolume:     false,
		HomeFocusBluetooth:  false,
		HomeFocusBrightness: false,
		HomeFocusQR:         false,
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
				if homeIsBodyCard(id) && homeRectMoved(item.Rect, rect) {
					h.geometryEdited = true
				}
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
// insensitive) title, or one of the names a card has had (homeTitleAlias).
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
	return homeTitleAlias(title)
}

// homeTitleAlias maps a config tile title that is not a card's built-in title
// to the card it has always meant: "Emulator" — the Games card's name until
// it was renamed, still in every jukaconfig.json a device kept through an
// update (the file is a user file) — and any title containing "tools", which
// is how the shipped config once spelled the Tools row ("Search / Tools").
func homeTitleAlias(title string) HomeFocusID {
	t := strings.ToLower(strings.TrimSpace(title))
	switch t {
	case "games", "game library", "games library", "library", "emulator", "emulators":
		return HomeFocusEmulator
	}
	if strings.Contains(t, "tools") {
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

// Move moves focus in the given direction. The hand-tuned graph
// (homeFocusGraph) describes the dashboard composition; the Hero grid
// arrangement puts the same cards in other cells, so there a step inside the
// body is taken by geometry instead (homeGridStep) — otherwise Down from Media
// followed the dashboard and landed on a card to its right. Leaving the body
// upwards still follows the graph into the header. The Carousel and Widgets
// bodies have no graph at all; they step by their own geometry (dynMove).
func (h *HomeLayout) Move(direction FocusDirection) HomeFocusID {
	if h == nil {
		return HomeFocusContinue
	}
	if h.dyn.active {
		return h.dynMove(direction)
	}
	if h.Arrangement == homeArrangementGrid || h.geometryEdited {
		// The power button is not a body card, and the graph's Up from it names
		// the dashboard's Favorites: here Up takes whichever card is drawn
		// above it.
		if direction == FocusUp && h.Focus.Current() == HomeFocusExit {
			if id := h.cardAbove(h.Items[HomeFocusExit].Rect); id != "" && h.Focus.Set(id) {
				h.Focus.Remember()
				return id
			}
		}
		if next, ok := h.homeGridStep(direction); ok {
			if next != h.Focus.Current() && h.Focus.Set(next) {
				h.Focus.Remember()
			}
			return h.Focus.Current()
		}
		// Up left the body. The graph's Up from a card under Media names
		// Media (a body card); here the press means the header, so it takes
		// the edge Media itself leaves by. The card under the header launcher
		// keeps its own edge to the launcher.
		cur := h.Focus.Current()
		if direction == FocusUp && cur != HomeFocusMedia && cur != h.Focus.TileLink() &&
			homeIsBodyCard(homeFocusGraph[cur].Up) && h.Focus.Set(HomeFocusMedia) {
			if next := h.Focus.Move(FocusUp); next != HomeFocusMedia {
				return next
			}
			h.Focus.Set(cur)
			return h.Focus.Current()
		}
	}
	return h.Focus.Move(direction)
}

// homeIsBodyCard reports whether id is one of the cards an arrangement places.
func homeIsBodyCard(id HomeFocusID) bool {
	for _, b := range homeBodyCards {
		if b == id {
			return true
		}
	}
	return false
}

// homeRectMoved reports whether a config rect differs from a built-in one by
// more than the rounding device scaling can introduce.
func homeRectMoved(a, b sdl.Rect) bool {
	const tol = 4
	diff := func(x, y int32) bool { return x-y > tol || y-x > tol }
	return diff(a.X, b.X) || diff(a.Y, b.Y) || diff(a.W, b.W) || diff(a.H, b.H)
}

// homeBodyCards are the cards an arrangement places (homeGridRects).
var homeBodyCards = [...]HomeFocusID{
	HomeFocusContinue, HomeFocusMedia, HomeFocusEmulator, HomeFocusPackages,
	HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser, HomeFocusChat,
	HomeFocusTools, HomeFocusFiles, HomeFocusSettings,
}

// homeGridStep picks the body card a press reaches by the drawn geometry: the
// nearest enabled card lying wholly past the focused card's edge in that
// direction, preferring one that shares its row (or column). It reports false
// when focus is not on a body card, or when an Up press leaves the body, so
// the graph takes over; at the other edges focus stays where it is.
func (h *HomeLayout) homeGridStep(direction FocusDirection) (HomeFocusID, bool) {
	cur := h.Focus.Current()
	from, ok := h.Items[cur]
	if !ok || !homeIsBodyCard(cur) || from.Rect.W <= 0 || from.Rect.H <= 0 {
		return "", false
	}
	const tol = 4
	f := from.Rect
	best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
	for _, id := range homeBodyCards {
		if id == cur || !h.Focus.IsEnabled(id) {
			continue
		}
		r := h.Items[id].Rect
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		var gap, offset int32
		switch direction {
		case FocusRight:
			if r.X < f.X+f.W-tol {
				continue
			}
			gap, offset = r.X-(f.X+f.W), spanOffset(f.Y, f.H, r.Y, r.H)
		case FocusLeft:
			if r.X+r.W > f.X+tol {
				continue
			}
			gap, offset = f.X-(r.X+r.W), spanOffset(f.Y, f.H, r.Y, r.H)
		case FocusDown:
			if r.Y < f.Y+f.H-tol {
				continue
			}
			gap, offset = r.Y-(f.Y+f.H), spanOffset(f.X, f.W, r.X, r.W)
		case FocusUp:
			if r.Y+r.H > f.Y+tol {
				continue
			}
			gap, offset = f.Y-(r.Y+r.H), spanOffset(f.X, f.W, r.X, r.W)
		default:
			return "", false
		}
		if gap < 0 {
			gap = 0
		}
		// The cross-axis miss weighs double: a card in the same row a little
		// further away beats a nearer one a row up or down.
		score := int64(gap) + 2*int64(offset)
		if score < bestScore {
			best, bestScore = id, score
		}
	}
	if best == "" {
		if direction == FocusUp {
			return "", false
		}
		// Down off the bottom row reaches the power button from the card that
		// starts over its end of the footer, as Favorites does in the dashboard.
		if direction == FocusDown && h.Focus.IsEnabled(HomeFocusExit) {
			if exit := h.Items[HomeFocusExit].Rect; exit.W > 0 && f.X <= exit.X+exit.W {
				return HomeFocusExit, true
			}
		}
		return cur, true
	}
	return best, true
}

// cardAbove is the enabled body card drawn nearest above r, preferring one
// that overlaps it horizontally: where Up from the power button lands.
func (h *HomeLayout) cardAbove(r sdl.Rect) HomeFocusID {
	if r.W <= 0 || r.H <= 0 {
		return ""
	}
	best, bestScore := HomeFocusID(""), int64(math.MaxInt64)
	for _, id := range homeBodyCards {
		if !h.Focus.IsEnabled(id) {
			continue
		}
		c := h.Items[id].Rect
		if c.W <= 0 || c.H <= 0 || c.Y+c.H > r.Y+4 {
			continue
		}
		score := int64(r.Y-(c.Y+c.H)) + 2*int64(spanOffset(r.X, r.W, c.X, c.W))
		if score < bestScore {
			best, bestScore = id, score
		}
	}
	return best
}

// spanOffset is how far apart two spans are along one axis: zero when they
// overlap, otherwise the gap between their nearest ends.
func spanOffset(aStart, aLen, bStart, bLen int32) int32 {
	switch {
	case bStart >= aStart+aLen:
		return bStart - (aStart + aLen)
	case aStart >= bStart+bLen:
		return aStart - (bStart + bLen)
	}
	return 0
}

func (h *HomeLayout) FocusAt(x, y int32) bool {
	// While the power button's card is up the pointer is the card's: hovering
	// the cards behind it must not move Home's focus.
	if h == nil || homePowerConfirm.open {
		return false
	}
	// Keep the three corner chips' visible faces ahead of their overlapping
	// finger-sized margins. A tap on the report icon must open its form.
	for _, id := range []HomeFocusID{HomeFocusBell, HomeFocusBug, HomeFocusTrophy} {
		if h.Focus.IsEnabled(id) && pointInRect(x, y, h.Items[id].Rect) {
			return h.Focus.Set(id)
		}
	}
	for _, id := range h.Order {
		// A header chip that is not drawn (no weather forecast, no release on
		// offer, nothing playing) keeps its placeholder rect; it must not
		// swallow a click meant for the chip beside it.
		if !h.Focus.IsEnabled(id) {
			continue
		}
		// A Widgets tile is hit only inside the body it scrolls in; a carousel
		// card never takes the focus from the pointer (home_dynbody.go).
		if handled, hit := h.dynFocusHover(id, x, y); handled {
			if hit {
				return true
			}
			continue
		}
		item := h.Items[id]
		// Hit-test against the grown tap area, not the drawn rect: a compact
		// 36px Exit pill or a header chip looks right but is smaller than a
		// fingertip, and a press a few pixels off does nothing. The drawn
		// geometry never moves — only the invisible target grows.
		//
		// The sun, the speaker, the Bluetooth rune and the signal meter are
		// already finger-sized (statusFingerRect), with each gap between them
		// split and the meter stopped short of the forecast: growing them
		// again reached into the forecast chip, and since the meter is tested
		// first, a click on the forecast's left edge opened Wi-Fi.
		r := item.Rect
		if id != HomeFocusWiFi && id != HomeFocusVolume && id != HomeFocusBrightness && id != HomeFocusBluetooth {
			r = growTouchTarget(item.Rect)
		}
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
		// The tile layouts' tiles scroll and slide by design; only the
		// controls around them have to stay on the canvas.
		if h.dynIsTile(id) || (id == HomeFocusEditPill && !h.Focus.IsEnabled(id)) {
			continue
		}
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
		case HomeFocusClock, HomeFocusWeather, HomeFocusUpdate, HomeFocusBell, HomeFocusBug, HomeFocusNowPlaying, HomeFocusWiFi,
			HomeFocusVolume, HomeFocusBrightness, HomeFocusQR, HomeFocusTrophy, HomeFocusBluetooth:
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
	// Every focus highlight drawn from here to the end of Render glides when
	// the focus steps (homefocusglide.go); overlays drawn after it do not.
	homeFocusGlide.beginFrame(uiClockMS(), h.Focus.Current(), h.Glide)
	defer homeFocusGlide.endFrame()

	// The caller (renderHomeModern) owns the background; the layout only adds
	// its translucent backdrop blocks on top of it.
	h.drawBackdrop(renderer)
	h.drawHeader(renderer, fonts, data)
	// Edit Home's strip (the layouts, Reset, Done) takes the greeting's and
	// the search pill's place while Home is being edited (home_edit.go).
	editing := homeEditActive()
	if editing {
		h.drawEditStrip(renderer, fonts)
	} else {
		h.drawGreeting(renderer, fonts, data)
	}

	// The power button takes its label's width inside its slot, and is a
	// control only while it can be drawn (home_power.go). Sized before the
	// footer, whose hints keep clear of it. Edit mode hides it: the strip's
	// Done is the way out there. The tile layouts' Edit pill sits beside it.
	h.sizePowerPill(fonts)
	if editing {
		h.Focus.SetEnabled(HomeFocusExit, false)
	}
	h.sizeEditPill(fonts)

	// The footer is plain chrome: corner chips draw after it on top of its
	// band instead of disappearing behind it.
	h.drawFooter(renderer, fonts, data.ShowBackHint)

	focused := h.Focus.Current()
	if h.focusID != focused {
		h.focusID = focused
		h.focusEnter.begin(uiClockMS(), uiFocusBounceMS)
	}
	lift := 1.0
	if h.Motion {
		lift = uiFocusBounce.at(h.focusEnter.progress(uiClockMS()))
	} else {
		h.focusEnter.cancel()
	}
	// The Carousel and Widgets bodies draw their own tiles (home_dynbody.go);
	// the loop below then draws only the controls around them.
	if h.dyn.active {
		h.dynRender(renderer, fonts, data, focused, lift)
	} else if editing {
		// Editing Standard or the Hero grid: the strip has the focus, so no
		// card under the veil wears the ring.
		focused = ""
	}
	for _, id := range h.Order {
		if h.dyn.active && h.dynIsTile(id) {
			continue
		}
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
			if h.Focus.IsEnabled(HomeFocusExit) {
				h.drawPowerPill(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift)
			}
		case HomeFocusSearch:
			if !editing {
				h.drawSearchPill(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift, data.Subtitle)
			}
		case HomeFocusEditPill:
			if h.Focus.IsEnabled(HomeFocusEditPill) {
				h.drawEditPill(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift)
			}
		case HomeFocusClock, HomeFocusWeather, HomeFocusUpdate, HomeFocusPS5, HomeFocusPS4, HomeFocusNowPlaying, HomeFocusBell, HomeFocusBug, HomeFocusWiFi,
			HomeFocusVolume, HomeFocusBrightness, HomeFocusQR, HomeFocusTrophy, HomeFocusBluetooth:
			// Header chips are the buttons; drawHeader draws them and their focus
			// rings. Rendering them as action cards would paint a clipped card
			// (title and icon) over the header -- the now-playing entry sits on
			// the bell's shoulder, so a stray card would cover it.
		default:
			h.drawActionCard(renderer, fonts, item, id == focused, data.Pressed == id, pressDepth, cardLift)
		}
	}
	// Standard and the Hero grid cannot be rearranged: while editing, their
	// cards sit under a veil that points at the strip.
	if editing && !h.dyn.active {
		h.drawEditVeil(renderer, fonts)
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
	statusVolumeRect = sdl.Rect{}
	statusBluetoothRect = sdl.Rect{}
	statusBrightnessRect = sdl.Rect{}
	statusMediaRect = sdl.Rect{}
	ps5TileRect = sdl.Rect{}
	ps4TileRect = sdl.Rect{}
	homeUpdateRect = sdl.Rect{}
	headerTrophyClear()
	headerBugClear()
	headerSlotRectsClear()
	// A scene's release pill is not on Home: left over, its rect would still
	// take a click (handleChromeBarClick) where Home draws something else.
	updatePrompt.badge = sdl.Rect{}
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
	// The build date belongs beside the wordmark on Home alone.
	caption := ""
	if data.BuildDate != "" {
		caption = " · " + data.BuildDate
	}
	brandEnd := drawHeaderBrand(renderer, headerConfig, barH, caption)

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
		// focus can never land on a control that is not drawn — nor on the
		// sun, the speaker, the Bluetooth rune, the meter or the pairing code,
		// which are not drawn either and would otherwise keep their
		// placeholder rects (and stay clickable through the rects the last
		// frame left).
		companionHeaderRect = sdl.Rect{}
		statusVolumeRect = sdl.Rect{}
		statusBluetoothRect = sdl.Rect{}
		statusBrightnessRect = sdl.Rect{}
		h.Focus.SetEnabled(HomeFocusBell, false)
		h.Focus.SetEnabled(HomeFocusBug, false)
		h.Focus.SetEnabled(HomeFocusTrophy, false)
		h.Focus.SetEnabled(HomeFocusVolume, false)
		h.Focus.SetEnabled(HomeFocusBluetooth, false)
		h.Focus.SetEnabled(HomeFocusBrightness, false)
		h.Focus.SetEnabled(HomeFocusWiFi, false)
		h.Focus.SetEnabled(HomeFocusQR, false)
		return
	}
	sep := "   "
	sepW, _ := measureText(fonts.Small, sep)
	partW := func(p string) int32 {
		return statusClusterPartWidth(fonts.Small, p)
	}
	total := int32(0)
	for i, part := range data.StatusParts {
		total += partW(part)
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
	right := h.Width - StatusBarMargin
	bellY := (header.H - bellH) / 2
	// The corner's buttons are decided the way they always were: the bell and
	// Report a bug always; the trophy room's cup unless the cluster beside it
	// would reach the wordmark; the release offer unless it would push the
	// cluster into the greeting column (on such a screen the modal prompt and
	// the scene headers still reach the release). usedW is the room they
	// take, each with its corner gap.
	var corner [hsCount]int32
	usedW := int32(0)
	if bellW > 0 {
		corner[hsBell] = bellW
		usedW += bellW + headerCornerGap
	}
	if bugW, _ := headerBugSize(headerConfig); bugW > 0 && !headerSlotHidden(headerConfig, hsBug) {
		corner[hsBug] = bugW
		usedW += bugW + headerCornerGap
	}
	if trophyW, _ := headerTrophySize(headerConfig); trophyW > 0 && !headerSlotHidden(headerConfig, hsTrophy) &&
		right-usedW-trophyW-headerCornerGap-total >= brandEnd+Space2XL {
		corner[hsTrophy] = trophyW
		usedW += trophyW + headerCornerGap
	}
	if updateAvailable() && !headerSlotHidden(headerConfig, hsUpdate) {
		if chipW, _ := homeUpdateChipSize(headerCaptionFont(headerConfig)); chipW > 0 &&
			right-usedW-chipW-headerCornerGap-total >= scaleX(480, h.Width) {
			corner[hsUpdate] = chipW
			usedW += chipW + headerCornerGap
		}
	}
	// The whole right of the bar in the user's order (Edit Home → Top bar,
	// header_arrange.go); the shipped order puts the corner's buttons last,
	// the bell at the margin. Home's cluster is never trimmed.
	plan := headerPlan(headerConfig, data.StatusParts, partW, &corner, sepW, headerCornerGap, right)
	bellDrawn := false
	for _, it := range plan {
		switch it.slot {
		case hsBell:
			renderHomeBell(renderer, headerConfig, bellFont, it.x, bellY, bellW, bellH)
			bellDrawn = true
			headerSlotRects[hsBell] = nc.bellRect
		case hsBug:
			if bug := drawHeaderBug(renderer, headerConfig, it.x+it.w, barH,
				h.Focus.Current() == HomeFocusBug); bug.W > 0 {
				item := h.Items[HomeFocusBug]
				item.Rect = bug
				h.Items[HomeFocusBug] = item
				headerSlotRects[hsBug] = bug
			}
		case hsTrophy:
			if cup := drawHeaderTrophy(renderer, headerConfig, it.x+it.w, barH,
				h.Focus.Current() == HomeFocusTrophy); cup.W > 0 {
				item := h.Items[HomeFocusTrophy]
				item.Rect = cup
				h.Items[HomeFocusTrophy] = item
				headerSlotRects[hsTrophy] = cup
			}
		case hsUpdate:
			if pill := drawHeaderUpdatePill(renderer, headerConfig, it.x+it.w, barH,
				h.Focus.Current() == HomeFocusUpdate); pill.W > 0 {
				homeUpdateRect = pill
				item := h.Items[HomeFocusUpdate]
				item.Rect = pill
				h.Items[HomeFocusUpdate] = item
				headerSlotRects[hsUpdate] = pill
			}
		}
	}
	if !bellDrawn {
		// No bell this frame: its last rect must not stay a focus stop.
		nc.bellRect = sdl.Rect{}
	}
	// The bell is a full member of the home focus graph: record the rect it was
	// actually drawn at, and only let the D-pad land on it while it is visible.
	if nc.bellRect.W > 0 {
		item := h.Items[HomeFocusBell]
		item.Rect = nc.bellRect
		h.Items[HomeFocusBell] = item
	}
	h.Focus.SetEnabled(HomeFocusBell, nc.bellRect.W > 0)
	h.Focus.SetEnabled(HomeFocusBug, headerBugRect.W > 0)
	h.Focus.SetEnabled(HomeFocusTrophy, headerTrophyRect.W > 0)

	// Header controls, in the band under the system cluster and clear of the
	// greeting column: the phone-pairing code and the PS4/PS5 launcher pills.
	// The design sheet's header carries exactly these controls.
	bandY := scaleY(78, h.Height)
	bandH := scaleY(44, h.Height)
	companionHeaderRect = sdl.Rect{}
	ps4TileRect, ps5TileRect = sdl.Rect{}, sdl.Rect{}
	bandRight := h.Width - scaleX(homeSideMargin, h.Width)
	// The pairing code takes the band's right end at the largest size the band
	// has room for: from just under the system corner to just above the first
	// card below it. The code is only as readable as its modules are big, and
	// renderCompanionHeaderQR sizes them from this square.
	statusBottom := max32(bellY+bellH, (barH+int32(fonts.Small.Height()))/2+scaleY(6, h.Height))
	if qr := h.headerQRRect(bandRight, statusBottom); qr.W > 0 {
		renderCompanionHeaderQR(renderer, fonts, qr)
	}
	// The code is a control: the D-pad reaches it (Right from the PS5
	// launcher, Down from the bell) and A shows it full screen. It is
	// focusable only while a code is drawn, and Down from it lands on the card
	// actually under it.
	if companionHeaderRect.W > 0 {
		item := h.Items[HomeFocusQR]
		item.Rect = companionHeaderRect
		h.Items[HomeFocusQR] = item
	} else if h.Focus.Current() == HomeFocusQR {
		// The code went away under the focus (Wi-Fi dropped, a new PIN is
		// being made): hand the focus to its neighbour on the band, or the
		// bell above it. Disabling it first would throw the focus to the
		// Continue card at the other end of the screen and forget where Home
		// was.
		if !h.Focus.Set(HomeFocusPS5) {
			h.Focus.Set(HomeFocusBell)
		}
	}
	h.Focus.SetEnabled(HomeFocusQR, companionHeaderRect.W > 0)
	h.Focus.SetQRLink(h.CardUnderTile(companionHeaderRect))
	if h.Focus.Current() == HomeFocusQR && companionHeaderRect.W > 0 {
		h.drawHeaderQRRing(renderer, companionHeaderRect)
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
	// Remote Play only runs on the handheld build (psRemoteSupported); elsewhere
	// each pill could only answer with a warning, so the group is not drawn and
	// the D-pad walks past it. The group says what it is ("Remote Play") when
	// the band has room for the caption, and drops the caption before the pills.
	// Edit Home's strip has the band while Home is being edited.
	if psRemoteSupported() && !homeEditActive() {
		label := psPillGroupLabel
		group, p4, p5 := psPillRects(fonts, groupRight, groupY, bandH, label)
		if group.W > 0 && group.X < minGroupX {
			label = ""
			group, p4, p5 = psPillRects(fonts, groupRight, groupY, bandH, label)
		}
		if group.W > 0 && group.X >= minGroupX {
			renderPSPillGroup(renderer, fonts, group, p4, p5, psPillVisual{
				PS4On:   ps4TileCurrentState() == ps4TileLaunch,
				PS5On:   ps5TileCurrentState() == ps5TileLaunch,
				Focused: h.Focus.Current(),
				Pressed: data.Pressed,
				Label:   label,
			})
			ps4TileRect, ps5TileRect = p4, p5
		}
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
	clockDrawn := false
	for _, it := range plan {
		if it.part == "" {
			continue
		}
		part, x, w := it.part, it.x, it.w
		r := sdl.Rect{
			X: x - scaleX(8, h.Width),
			Y: y - scaleY(6, h.Height),
			W: w + scaleX(16, h.Width),
			H: int32(fonts.Small.Height()) + scaleY(12, h.Height),
		}
		headerSlotRects[it.slot] = r
		switch {
		case isClockStatusPart(part):
			item := h.Items[HomeFocusClock]
			item.Rect = r
			h.Items[HomeFocusClock] = item
			clockDrawn = true
		case isWeatherStatusPart(part):
			item := h.Items[HomeFocusWeather]
			item.Rect = r
			h.Items[HomeFocusWeather] = item
			weatherStatusRect = r
		case isWiFiStatusPart(part):
			statusWiFiRect = r
		case isVolumeStatusPart(part):
			statusVolumeRect = r
		case isBluetoothStatusPart(part):
			statusBluetoothRect = r
		case isBrightnessStatusPart(part):
			statusBrightnessRect = r
		case isChipStatusPart(part):
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
	}
	// The clock is a button (the calendar) while it is drawn; the user may
	// have hidden it (Edit Home → Top bar).
	h.Focus.SetEnabled(HomeFocusClock, clockDrawn)
	// The network meter, the Bluetooth rune, the speaker and the sun are tap
	// targets as well as readings, so grow their hit areas to finger size once
	// the bar is placed: none grows over another control (the forecast, the
	// clock, the corner's buttons, wherever the user put them), and each gap
	// between two of the buttons is split.
	headerGrowStatusButtons(header.H)
	// The meter is a button, not just a reading: the D-pad focuses the same
	// grown hit area a fingertip taps, and the entry is disabled whenever the
	// cluster had to drop the network slot for space.
	if statusWiFiRect.W > 0 {
		item := h.Items[HomeFocusWiFi]
		item.Rect = statusWiFiRect
		h.Items[HomeFocusWiFi] = item
	}
	h.Focus.SetEnabled(HomeFocusWiFi, statusWiFiRect.W > 0)
	// So is the Bluetooth rune: A opens the Bluetooth page. Where there is no
	// page to open (Home's cluster is never trimmed, so that is the only
	// reason it is missing here) the entry is disabled and Left from the meter
	// goes on to the speaker.
	if statusBluetoothRect.W > 0 {
		item := h.Items[HomeFocusBluetooth]
		item.Rect = statusBluetoothRect
		h.Items[HomeFocusBluetooth] = item
	}
	h.Focus.SetEnabled(HomeFocusBluetooth, statusBluetoothRect.W > 0)
	// So is the speaker: A opens the volume popover. No mixer, no part, so the
	// entry is disabled and Left from the rune goes on to the sun.
	if statusVolumeRect.W > 0 {
		item := h.Items[HomeFocusVolume]
		item.Rect = statusVolumeRect
		h.Items[HomeFocusVolume] = item
	}
	h.Focus.SetEnabled(HomeFocusVolume, statusVolumeRect.W > 0)
	// And the sun: A opens the brightness card. It is drawn once the
	// brightness controller has found what it drives; until then (a moment, on
	// Windows) the entry is disabled and Left from the speaker goes on to the
	// search pill.
	if statusBrightnessRect.W > 0 {
		item := h.Items[HomeFocusBrightness]
		item.Rect = statusBrightnessRect
		h.Items[HomeFocusBrightness] = item
	}
	h.Focus.SetEnabled(HomeFocusBrightness, statusBrightnessRect.W > 0)
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
	// The text chips (clock, forecast, signal meter, Bluetooth rune, speaker,
	// sun, now playing) wear the header ring here. The bell, the release offer
	// and the Remote Play pills draw their own ring with their own shape
	// (drawNotifBell, drawUpdatePill, renderPSPillGroup); a second,
	// square-cornered one on top of theirs was the double outline a focused
	// bell showed. The pairing code wears its own (drawHeaderQRRing).
	for _, id := range []HomeFocusID{HomeFocusClock, HomeFocusWeather, HomeFocusBrightness, HomeFocusVolume, HomeFocusBluetooth, HomeFocusWiFi, HomeFocusNowPlaying} {
		if h.Focus.Current() != id {
			continue
		}
		r := h.Items[id].Rect
		if r.W <= 0 {
			continue
		}
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		strokeRoundedRectAt(renderer, r, homeFocusGlide.apply(r), 10, homeFocusRingWidth, ColorAccent)
	}
	// Edit Home's Top bar row rings the control its focus is on, here in the
	// live bar (home_edit_header.go).
	drawEditBarLiveRing(renderer)
}

// CardUnderTile returns the card sitting directly below a header tile: same
// column, nearest top edge. The PS5 launcher uses it to claim the Up edge of
// the card beneath it. Header chips and the tile itself are skipped, and an
// empty tile or a tile with nothing under it returns "".
func (h *HomeLayout) CardUnderTile(tile sdl.Rect) HomeFocusID {
	if h == nil || tile.W <= 0 {
		return ""
	}
	if h.dyn.active {
		// The tile layouts' bodies scroll and turn: the card under a header
		// tile is the one on screen nearest it (home_dynbody.go).
		return h.dynEntryBelow(tile)
	}
	center := tile.X + tile.W/2
	best := HomeFocusID("")
	bestY := int32(0)
	for _, id := range h.Order {
		switch id {
		case HomeFocusPS5, HomeFocusPS4, HomeFocusClock, HomeFocusWeather, HomeFocusBell, HomeFocusBug,
			HomeFocusNowPlaying, HomeFocusWiFi, HomeFocusExit, HomeFocusSearch, HomeFocusUpdate,
			HomeFocusVolume, HomeFocusBrightness, HomeFocusQR, HomeFocusTrophy, HomeFocusBluetooth:
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

// The header pairing code's size bounds, in 1280x720 canvas units: the band
// gives it about 100px between the system corner and the cards; the cap keeps a
// tall window from growing it past the band's proportions, and below the floor
// a phone could not read it, so the band is left to the launchers instead.
const (
	homeHeaderQRMax int32 = 120
	homeHeaderQRMin int32 = 56
)

// headerQRRect is the square the header's pairing code is drawn in: right-
// aligned at bandRight, its top just below the system corner (top) and its
// bottom clear of the first card under its column — the Media card in the
// dashboard, whichever card an arrangement or a config puts there otherwise.
// "Clear" includes that card's focus treatment: it lifts 3px and its ring
// stands homeFocusRingOutset off it, and neither may cut into the tile.
// Empty when that leaves less than the floor.
func (h *HomeLayout) headerQRRect(bandRight, top int32) sdl.Rect {
	// Edit Home's strip has the band while Home is being edited.
	if homeEditActive() {
		return sdl.Rect{}
	}
	maxSide := scaleY(homeHeaderQRMax, h.Height)
	y := top + scaleY(6, h.Height)
	ringOut := max32(2, scaleX(homeFocusRingGap, h.Width)) + homeFocusRingWidth
	gapBelow := ringOut + 3 + 1
	bottom := h.Height
	if h.dyn.active {
		// The tile layouts scroll their tiles through the body: the band
		// ends where the body begins, whatever is under it right now.
		bottom = scaleY(homeDynBodyTop, h.Height)
	} else {
		for _, id := range homeBodyCards {
			r := h.Items[id].Rect
			if r.W <= 0 || r.H <= 0 || r.X >= bandRight || r.X+r.W <= bandRight-maxSide || r.Y+r.H <= y {
				continue
			}
			if r.Y < bottom {
				bottom = r.Y
			}
		}
	}
	side := min32(bottom-gapBelow-y, maxSide)
	if side < scaleY(homeHeaderQRMin, h.Height) {
		return sdl.Rect{}
	}
	return sdl.Rect{X: bandRight - side, Y: y, W: side, H: side}
}

// drawHeaderQRRing marks the focused pairing code: the accent ring standing
// off the white tile like a card's halo, concentric with the tile's corners
// (renderCompanionHeaderQR rounds them by side/10), so it never covers a
// module. The gap is 2px rather than a card's 4: the tile sits 6px under the
// system corner, and a card-sized halo would touch the bell above it.
func (h *HomeLayout) drawHeaderQRRing(renderer *sdl.Renderer, tile sdl.Rect) {
	if renderer == nil || tile.W <= 0 || tile.H <= 0 {
		return
	}
	out := homeFocusRingWidth + 2
	radius := max32(6, min32(tile.W, tile.H)/10) + out
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	ring := sdl.Rect{X: tile.X - out, Y: tile.Y - out, W: tile.W + 2*out, H: tile.H + 2*out}
	strokeRoundedRectAt(renderer, ring, homeFocusGlide.apply(ring), radius, homeFocusRingWidth, ColorAccent)
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
		strokeRoundedRectAt(renderer, item.Rect, homeFocusGlide.apply(draw), radius, 3, ColorAccent)
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
	// The Chat tile's unread count (chatbadge.go), painted last so it sits
	// over the tile.
	item, unread := homeChatTileUnread(item)
	if unread > 0 {
		defer h.drawHomeChatBadge(renderer, fonts, draw, unread)
	}

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
	// The focused tile lights its icon the way the Media card always does — the
	// tinted badge and an accent glyph — so focus is carried by the tile's
	// content as well as its outline.
	glyph := drawIconBadge(renderer, badge, focused)
	if focused {
		drawHomeGlyphTinted(renderer, item.Icon, glyph, ColorAccent)
	} else {
		DrawHomeIcon(renderer, item.Icon, glyph, false)
	}

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
	// The resting accent edge stays quiet so that focus — the standing-off ring
	// — is a change a glance can see; at 110 alpha the resting card already
	// looked half-selected.
	edge := WithAlpha(ColorAccent, 70)
	if focused {
		edge = WithAlpha(ColorAccent, 150)
	}
	strokeRoundedRect(renderer, draw.X, draw.Y, draw.W, draw.H, homeTileRadius, 1, edge)
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

// scaleHomeCard grows a card by three pixels at rest, and by four for the
// brief focus bounce.
// The rect is only used for drawing; hit areas keep their settled geometry so
// clicks land where the card actually is.
func scaleHomeCard(rect sdl.Rect, lift float64) sdl.Rect {
	if lift <= 0.01 {
		return rect
	}
	grow := int32(math.Round(math.Min(lift, 1.3) * 3))
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
		// frame, the same ramp the ambient artwork uses, in the theme's page
		// colour so the theme's text colours read on it (continueScrim).
		from, to := continueScrim(215)
		fillVerticalScrim(renderer, draw, draw.Y+draw.H/3, from, to)
	} else {
		drawContinueThumb(renderer, draw)
	}
	if focused {
		// The hero's thumbnail is bright, and a ring drawn on its edge read as
		// part of the artwork; the standing-off ring has the dark gap between
		// the two, so the selection is obvious over any frame or box art.
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 1, homeCardEdgeFocus())
		drawHomeFocusRing(renderer, item.Rect, draw, homeTileRadius)
	} else {
		strokeRoundedRectAt(renderer, item.Rect, draw, homeTileRadius, 1, homeCardEdge())
	}

	pad := scaleX(20, h.Width)

	// Continue chip, top-left: the card says what pressing it will do. With
	// nothing to resume it is the first-run card (homeContinueEmptyState) and
	// says how to start instead.
	chipText := "Continue watching"
	if c := strings.TrimSpace(state.Chip); c != "" {
		chipText = c
	}
	if !state.HasRecent && strings.TrimSpace(state.Chip) == "" {
		chipText = "Get started"
	}
	label := strings.ToUpper(uiT(chipText)) // translate before upper-casing (i18n.go)
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
	if a := strings.TrimSpace(state.Action); a != "" && !state.HasRecent {
		// The first-run card names its own destination (games on a handheld,
		// media on the desktop); homeActivate opens the same one.
		action = a
	}
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
		// The most valuable card on the screen is not spent on "nothing here":
		// it says what it will hold and offers the first step.
		if title == "" {
			title = "Your next video starts here"
		}
		if subtitle == "" {
			subtitle = "What you watch shows up here, ready to resume."
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

// footerHint pairs a controller button label with the action it performs.
type footerHint struct {
	button string
	label  string
}

// Home paints its hint row every frame. Its text and button vocabulary change
// only with focus, input device or settings, so keep the measured widths until
// one of those inputs changes.
var homeFooterWidthCache struct {
	valid    bool
	font     *ttf.Font
	width    int32
	height   int32
	vocab    hintVocab
	remap    *hintPadRemap
	language string
	hints    [len(homeFooterHintsStart)]footerHint
	n        int
	badgeW   [len(homeFooterHintsStart)]int32
	groupW   [len(homeFooterHintsStart)]int32
	total    int32
}

func homeFooterWidths(font *ttf.Font, hints []footerHint, width, height int32) (*[len(homeFooterHintsStart)]int32, *[len(homeFooterHintsStart)]int32, int32) {
	c := &homeFooterWidthCache
	vocab, remap, language := hintVocabNow(), hintRemapNow.Load(), uiLanguageCode()
	if c.valid && c.font == font && c.width == width && c.height == height &&
		c.vocab == vocab && c.remap == remap && c.language == language && c.n == len(hints) {
		same := true
		for i, hint := range hints {
			if c.hints[i] != hint {
				same = false
				break
			}
		}
		if same {
			return &c.badgeW, &c.groupW, c.total
		}
	}
	c.font, c.width, c.height = font, width, height
	c.vocab, c.remap, c.language = vocab, remap, language
	c.n, c.total = len(hints), 0
	badgeD, itemGap := scaleY(22, height), scaleX(18, width)
	for i, hint := range hints {
		c.hints[i] = hint
		bw, _ := measureText(font, homeHintKey(hint.button, hint.label))
		lw, _ := measureText(font, hint.label)
		c.badgeW[i] = max32(badgeD, bw+scaleX(12, width))
		c.groupW[i] = c.badgeW[i] + scaleX(7, width) + lw
		c.total += c.groupW[i]
	}
	if len(hints) > 1 {
		c.total += itemGap * int32(len(hints)-1)
	}
	c.valid = font != nil
	return &c.badgeW, &c.groupW, c.total
}

func (h *HomeLayout) drawFooter(renderer *sdl.Renderer, fonts HomeFonts, showBack bool) {
	// The footer is a 56px translucent band with rounded top corners, inset by
	// the frame margin like every other row: the power button at its left end
	// (home_power.go, drawn with the cards), the controller hints on the right.
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
	// the list with append allocated on every frame Home was on screen. A copy
	// on the stack lets A's label say what it does on the two header controls
	// whose action is not "open a screen".
	// The tile layouts and Edit Home have rows of their own
	// (homeFooterHintSet, home_edit.go); an empty slot in a row is skipped.
	set := h.homeFooterHintSet(showBack)
	if !homeEditActive() {
		if voice, ok := voiceOrbFooterHint(appConfig); ok {
			set[homeFooterVoiceSlot] = footerHint{button: voice.Button, label: voice.Label}
		}
	}
	if liquidUnsupportedSeen {
		// R2 cannot show water this renderer does not draw.
		for i := range set {
			if set[i].button == "R2" {
				set[i] = footerHint{}
			}
		}
	}
	if label := homeFooterActionLabel(h.Focus.Current()); label != "" && !homeEditActive() {
		for i := range set {
			if set[i].button == "A" {
				set[i].label = label
			}
		}
	}
	n := 0
	for i := range set {
		if set[i].button != "" {
			set[n] = set[i]
			n++
		}
	}
	hints := set[:n]

	// Right-aligned hint groups: a circular glyph badge and its label.
	itemGap := scaleX(18, h.Width)
	badgeD := scaleY(22, h.Height)
	badgeGap := scaleX(7, h.Width)
	fh := int32(fonts.Small.Height())

	badgeWs, groupWs, total := homeFooterWidths(fonts.Small, hints, h.Width, h.Height)
	// The hints must stay inside the band and clear of the power button at its
	// left end: on a narrow canvas the least specific hint (the D-pad) is
	// dropped first.
	limit := footer.X + scaleX(16, h.Width)
	if power := h.Items[HomeFocusExit].Rect; power.W > 0 && h.Focus.IsEnabled(HomeFocusExit) {
		limit = max32(limit, power.X+power.W+scaleX(16, h.Width))
	}
	if pill := h.Items[HomeFocusEditPill].Rect; pill.W > 0 && h.Focus.IsEnabled(HomeFocusEditPill) {
		limit = max32(limit, pill.X+pill.W+scaleX(16, h.Width))
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
		drawText(renderer, fonts.Small, homeHintKey(g.button, g.label), badge.X+badge.W/2,
			badge.Y+(badge.H-fh)/2, homeGlass(210), textAlignCenter)
		drawText(renderer, fonts.Small, g.label, badge.X+badge.W+badgeGap, textY, ColorTextMuted(), textAlignLeft)
		x += groupWs[i] + itemGap
	}
}

// homeFooterActionLabel is A's label on a focused header control whose press
// is not "open a screen": the pairing code goes full screen, the speaker opens
// the volume card, the sun the brightness card. "" keeps the row's own "Open".
func homeFooterActionLabel(id HomeFocusID) string {
	switch id {
	case HomeFocusQR:
		return "Show QR"
	case HomeFocusVolume:
		return "Volume"
	case HomeFocusBrightness:
		return "Brightness"
	}
	return ""
}

// homeFooterHintsStart is Home's footer: Home is the root scene, so B has
// nothing to go back to and the footer names Start, the menu that reaches
// every other screen, and the platform's quit chord — the shortcut beside the
// power button (exit_chord.go; TrimUI launchers all quit with Select + Start).
// homeFooterHintsBack is the same row for a layout that does have somewhere
// to go back to. Both must stay the same length. The row is dropped from the
// front on a narrow canvas, so the most specific hints sit at the end.
var (
	homeFooterHintsStart = [...]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Open"},
		{button: "Y", label: "Search"},
		{button: "L2", label: "Edit"},  // Edit Home (home_edit.go homeEditTrigger)
		{button: "R2", label: "Water"}, // liquid background level (liquidbg.go liquidCycle)
		{button: "Start", label: "Menu"},
		{button: "Select+Start", label: "Quit"},
		{}, // homeFooterVoiceSlot: Menu/F4 Talk to Juka, when available
	}
	homeFooterHintsBack = [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "A", label: "Open"},
		{button: "Y", label: "Search"},
		{button: "L2", label: "Edit"},
		{button: "R2", label: "Water"},
		{button: "B", label: "Back"},
		{button: "Select+Start", label: "Quit"},
		{}, // homeFooterVoiceSlot
	}
)

// homeFooterVoiceSlot is the slot of Home's footer rows that "Talk to Juka"
// fills (drawFooter).
const homeFooterVoiceSlot = len(homeFooterHintsStart) - 1

// defaultGreeting is the greeting for the hour. Morning starts at five: a
// handheld picked up at half past midnight is still in the evening for the
// person holding it, and "Good morning" at 00:30 read as a broken clock. In
// screenshot mode it follows the 9:41 the clock shows (shotClock).
func defaultGreeting(now time.Time) string {
	hour := shotClock(now).Hour()
	switch {
	case hour >= 5 && hour < 12:
		return "Good morning"
	case hour >= 12 && hour < 18:
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
	// The interface language (i18n.go); English passes through unchanged.
	drawTextRaw(renderer, font, uiT(text), x, y, color, alignment)
}

// drawTextRaw is drawText for content drawn exactly as written — what the
// user is typing in the virtual keyboard's field — with no interface-language
// translation (a typed "On" must not appear as "Activado").
func drawTextRaw(renderer *sdl.Renderer, font *ttf.Font, text string, x, y int32, color sdl.Color, alignment textAlignment) {
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
