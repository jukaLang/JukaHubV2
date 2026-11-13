package main

// themeswitch.go — the Switch 2 Home: what Switch 2 Style and Switch 2 Style
// Dark draw when Home is the Carousel. It is the console's home screen built
// from JukaHub's own pieces:
//
//   - the user's round picture in the top-left corner (A opens Trophies, the
//     closest thing to the console's user page); the time, the signal and the
//     battery stay in the top-right, the shared status cluster every screen
//     draws;
//   - one row of square tiles: the games played last, newest first, each its
//     box art edge to edge, then the Home tile list (Edit Home) as solid
//     colour squares with a glyph and a name. The focused tile wears the
//     bright outline, and its name sits over the row in the outline's colour;
//   - under the row, a centred line of round buttons — Games, Chat, Store,
//     Search, PlayStation and Xbox Remote Play, the phone pairing code,
//     Settings and Power — each with its name over it while focused;
//   - a hairline over the footer, a pair of controllers at its left end and
//     the button hints at its right.
//
// While it shows, the greeting, the search pill, the header band (the Remote
// Play pills and the pairing code) and the footer's power and Edit pills are
// not drawn: the round buttons do their jobs. Edit Home shows the usual chrome
// over the same row, and every other theme and arrangement draws as before.
//
// The look is carouselStyle "switch" (themeconsole.go keeps it a strip, so
// the row scrolls the strip's way). The other files call in here from one
// line each: themeconsole.go (carStyleRect, carDrawConsoleLabels,
// drawThemeSketchFor), home_carousel.go (carRender), home_dynbody.go (the
// body's signature, rebuild and dynMove), homelayout.go (Render, drawHeader,
// headerQRRect, FocusAt) and home.go (homeActivate).

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// homeCarouselSwitchHome is the carousel style that asks for the Switch Home.
// normalizeCarouselStyle maps it onto the strip; the look reads the raw value.
const homeCarouselSwitchHome = "switch"

// The Switch Home in 1280x720 design units, measured off the console's home
// screen: four whole tiles and the fifth peeking in, the round buttons
// centred between the row and the footer's hairline.
const (
	homeSwRowX    = 82.0
	homeSwRowTop  = 182.0
	homeSwRowSize = 268.0
	homeSwRowGap  = 10.0
	// homeSwTitleUp is how far over the row's top the focused tile's name
	// starts.
	homeSwTitleUp = 46.0
)

const (
	homeSwDockD     int32 = 68
	homeSwDockPitch int32 = 92
	homeSwLineY     int32 = 646
	homeSwAvatarD   int32 = 52
	homeSwRingGap   int32 = 3
	homeSwRingW     int32 = 4
	// homeSwRecentMax is how many recent games lead the row; the console
	// shows a dozen, a handheld's row is long enough with eight.
	homeSwRecentMax = 8
	// homeSwRecentEveryMS is how often the recent games are read again: they
	// change while Home is away (a game played), and a second is soon enough.
	homeSwRecentEveryMS = 1500
	// homeSwArtMax bounds the baked box art kept as textures.
	homeSwArtMax = 24
)

// homeSwArtRetry is how long a box art that failed to bake waits before the
// next try.
const homeSwArtRetry = 2 * time.Minute

// The Switch Home's own focus IDs: its picture, its recent games (by place in
// the row) and its round buttons. They are dynamic IDs (SetDynamic).
const homeSwAvatar HomeFocusID = "sw:avatar"

const (
	homeSwRecentPrefix = "sw:recent:"
	homeSwDockPrefix   = "sw:dock:"
)

// homeTileRecent is a recent game's tile in the row. It is never stored on
// the tile list: the row adds it (switchRowTiles) and Edit Home never sees it.
const homeTileRecent homeTileKind = 100

// homeSwDockEntry is one round button.
type homeSwDockEntry struct {
	ID    HomeFocusID
	Title string
	Icon  string
	// Ink is the glyph's colour; a Filled button is a disc of Ink with a
	// white glyph instead. Grey takes the theme's secondary ink.
	Ink    sdl.Color
	Filled bool
	Grey   bool
}

// homeSwRecent is one recent game of the row.
type homeSwRecent struct {
	item   FavoriteItem
	title  string
	note   string
	system string
	art    homePreviewJob
}

// homeSwArtJob is a game's box art lookup, kept for homeSwArtJobTTL: finding
// it stats the artwork folders, which the handheld's SD card makes slow.
type homeSwArtJob struct {
	job homePreviewJob
	at  time.Time
}

const homeSwArtJobTTL = 30 * time.Second

// homeSwitch is the Switch Home's state.
var homeSwitch struct {
	dock    []homeSwDockEntry
	recents []homeSwRecent
	// sig covers the recent games, their art and dropContinue, order their
	// paths alone; at is when they were read.
	sig, order uint64
	at         uint64
	// dropContinue: what Continue would resume is the newest game, which is
	// the row's first tile already.
	dropContinue bool
	// builtOrder is the order the row was last built with: a new order (a
	// game just played moved to the front) takes the focus back to the
	// first tile, as the console's row does (switchAfterRebuild).
	builtOrder uint64
	toFirst    bool
	// entered says the look is up: the first build of the row puts the focus
	// on its first tile. Edit Home does not count as leaving.
	entered bool
	// artJobs caches the box art lookups by game path.
	artJobs map[string]homeSwArtJob
	// avatar is where the picture was drawn this frame.
	avatar sdl.Rect
}

// homeIsSwitchHomeStyle reports whether a carousel style asks for the Switch
// Home.
func homeIsSwitchHomeStyle(style string) bool {
	return strings.EqualFold(strings.TrimSpace(style), homeCarouselSwitchHome)
}

// homeThemeFileCarouselStyle is the carousel style a theme file's value is
// kept as (themeloader.go): normalizeCarouselStyle's, except that "switch"
// stays itself, or the file would lose the Switch Home to the plain strip.
func homeThemeFileCarouselStyle(style string) string {
	if homeIsSwitchHomeStyle(style) {
		return homeCarouselSwitchHome
	}
	return normalizeCarouselStyle(style)
}

// switchRowWanted reports whether the body should be the Switch Home's row:
// the look is on, Home is the Carousel and is not being edited.
func (h *HomeLayout) switchRowWanted() bool {
	return h != nil && homeConsoleLook.switchHome && h.Arrangement == homeArrangementCarousel && !homeEdit.open
}

// switchChrome reports whether Home draws the Switch Home's chrome this
// frame: the row is wanted and built, round buttons and all.
func (h *HomeLayout) switchChrome() bool {
	return h.switchRowWanted() && h.dyn.active && len(homeSwitch.dock) > 0
}

// ── The row ───────────────────────────────────────────────────────────────

// switchRowSig is the dynamic body's signature part for the row: 0 when the
// row is not wanted, otherwise a value that changes with the recent games
// (dynSync rebuilds the body when it does).
func (h *HomeLayout) switchRowSig() uint64 {
	if !h.switchRowWanted() {
		return 0
	}
	homeSwRefresh()
	return homeSwitch.sig | 1
}

// homeSwRefresh reads the recent games again once homeSwRecentEveryMS has
// passed: the newest first, games only (videos and songs have Media), each
// with its box art if it has one.
func homeSwRefresh() {
	s := &homeSwitch
	now := sdl.GetTicks64()
	if s.at != 0 && now >= s.at && now-s.at < homeSwRecentEveryMS {
		return
	}
	s.at = max(now, 1)
	s.recents = s.recents[:0]
	sig, order := fnv.New64a(), fnv.New64a()
	items := getRecentItems()
	// Continue resumes the music player's track first, then the newest item
	// (homeContinueItem): it duplicates the row only when that is a game.
	_, _, _, music := homeMusicSession()
	s.dropContinue = !music && len(items) > 0 && items[0].Type == "game"
	if s.dropContinue {
		_, _ = sig.Write([]byte{1})
	}
	for _, it := range items {
		if it.Type != "game" {
			continue
		}
		g, ok := it.Data.(recentGame)
		if !ok || strings.TrimSpace(g.Path) == "" {
			continue
		}
		r := homeSwRecent{item: it, title: strings.TrimSpace(it.Label()), note: recentSourceLabel(it), system: g.System, art: homeSwArtJobFor(g)}
		if r.title == "" {
			r.title = strings.TrimSuffix(filepath.Base(g.Path), filepath.Ext(g.Path))
		}
		s.recents = append(s.recents, r)
		_, _ = order.Write([]byte(g.Path + "\x00"))
		_, _ = sig.Write([]byte(g.Path + "\x00" + r.art.Key + "\x00" + r.title + "\x00"))
		if len(s.recents) >= homeSwRecentMax {
			break
		}
	}
	s.sig, s.order = sig.Sum64(), order.Sum64()
}

// homeSwArtJobFor is a game's box art lookup, from the cache while it is
// fresh: art the scraper adds turns up within homeSwArtJobTTL.
func homeSwArtJobFor(g recentGame) homePreviewJob {
	s := &homeSwitch
	if s.artJobs == nil {
		s.artJobs = map[string]homeSwArtJob{}
	}
	now := time.Now()
	if c, ok := s.artJobs[g.Path]; ok && now.Sub(c.at) < homeSwArtJobTTL {
		return c.job
	}
	if len(s.artJobs) > 4*homeSwRecentMax {
		clear(s.artJobs)
	}
	job := homeArtPreviewJob(g)
	s.artJobs[g.Path] = homeSwArtJob{job: job, at: now}
	return job
}

// homeSwRecentID is the focus ID of the row's i-th recent game.
func homeSwRecentID(i int) HomeFocusID {
	return HomeFocusID(homeSwRecentPrefix + strconv.Itoa(i))
}

// homeSwRecentIndex is the recent game a focus ID names, or -1.
func homeSwRecentIndex(id HomeFocusID) int {
	rest, ok := strings.CutPrefix(string(id), homeSwRecentPrefix)
	if !ok {
		return -1
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 || i >= len(homeSwitch.recents) {
		return -1
	}
	return i
}

// switchRowTiles puts the recent games in front of the tile list
// (dynRebuild). The Continue card goes when what it resumes is a game: that
// game is the row's first tile already.
func (h *HomeLayout) switchRowTiles(out []homeDynTile) []homeDynTile {
	if !h.switchRowWanted() {
		return out
	}
	s := &homeSwitch
	homeSwRefresh()
	if s.order != s.builtOrder {
		s.toFirst = s.builtOrder != 0
		s.builtOrder = s.order
	}
	homeSwArtPrune()
	if len(s.recents) == 0 {
		return out
	}
	dropContinue := s.dropContinue
	row := make([]homeDynTile, 0, len(s.recents)+len(out))
	for i, r := range s.recents {
		id := homeSwRecentID(i)
		row = append(row, homeDynTile{
			ID: id, Ref: string(id), Kind: homeTileRecent, Size: homeTileSizeOne,
			Title: r.title, Subtitle: r.note, Icon: "emulator",
		})
	}
	for _, t := range out {
		if dropContinue && t.ID == HomeFocusContinue {
			continue
		}
		row = append(row, t)
	}
	return row
}

// switchAfterRebuild settles the focus once the body has been rebuilt
// (dynRebuild's last step): the look just came up (start-up, the theme or the
// arrangement picked) starts on the row's first tile, as the console does;
// and a game just played — the recent games' order changed — takes the focus
// back to the front if it was on a recent game. Edit Home is not leaving.
func (h *HomeLayout) switchAfterRebuild() {
	s := &homeSwitch
	if h == nil || !homeConsoleLook.switchHome || h.Arrangement != homeArrangementCarousel {
		s.entered, s.toFirst = false, false
		return
	}
	if homeEdit.open || len(h.dyn.tiles) == 0 {
		return
	}
	first := h.dyn.tiles[0].ID
	toFirst := !s.entered
	if s.toFirst {
		i := h.dynTileIndex(h.Focus.Current())
		toFirst = toFirst || (i > 0 && h.dyn.tiles[i].Kind == homeTileRecent)
	}
	s.entered, s.toFirst = true, false
	if toFirst && h.Focus.Set(first) {
		h.Focus.Remember()
		h.dynEnsureVisible(first)
	}
}

// homeSwScale is the canvas's design-to-pixel scales and the squares' one
// (the smaller, so a tile stays square on the 4:3 canvas).
func (h *HomeLayout) homeSwScale() (sx, sy, k float64) {
	sx = float64(h.Width) / float64(HomeBaseWidth)
	sy = float64(h.Height) / float64(HomeBaseHeight)
	return sx, sy, math.Min(sx, sy)
}

// switchRowRect is tile i's rect on the canvas (carStyleRect): squares from
// the left edge, slid by the strip's eased scroll. false: off screen.
func (h *HomeLayout) switchRowRect(i int) (sdl.Rect, bool) {
	r := h.switchRowRectAt(i, homeStrip.pos)
	// Kept a tile's width past the left edge, so the one leaving is drawn
	// until it is gone.
	if r.X+2*r.W < 0 || r.X > h.Width {
		return sdl.Rect{}, false
	}
	return r, true
}

// switchRowRectAt is tile i's rect with the strip scrolled to pos (the first
// whole tile shown).
func (h *HomeLayout) switchRowRectAt(i int, pos float64) sdl.Rect {
	sx, sy, k := h.homeSwScale()
	size := homeSwRowSize * k
	pitch := (homeSwRowSize + homeSwRowGap) * k
	x := homeSwRowX*sx + (float64(i)-pos)*pitch
	return sdl.Rect{
		X: int32(math.Round(x)),
		Y: int32(math.Round(homeSwRowTop * sy)),
		W: int32(math.Round(size)),
		H: int32(math.Round(size)),
	}
}

// switchSettledRect is tile i's rect once the strip has scrolled to show it
// (stripStep's rule), whatever the slide is doing now; empty for no tile.
func (h *HomeLayout) switchSettledRect(i int) sdl.Rect {
	n := len(h.dyn.tiles)
	if i < 0 || i >= n {
		return sdl.Rect{}
	}
	first := homeStrip.first
	if i < first {
		first = i
	}
	if i > first+homeStripShown-1 {
		first = i - homeStripShown + 1
	}
	first = max(0, min(first, n-homeStripShown))
	return h.switchRowRectAt(i, float64(first))
}

// switchRowBottom is the row's bottom edge on the canvas.
func (h *HomeLayout) switchRowBottom() int32 {
	_, sy, k := h.homeSwScale()
	return int32(math.Round(homeSwRowTop*sy + homeSwRowSize*k))
}

// switchRing is the console's focus outline around r: a gap of the page's
// colour, then the ring, pulsing after a move (homeFocusRingColor) and
// travelling with the focus (homefocusglide.go). radius is r's own corner.
func (h *HomeLayout) switchRing(renderer *sdl.Renderer, r sdl.Rect, radius int32) {
	if renderer == nil || r.W <= 0 || r.H <= 0 {
		return
	}
	gap := max32(2, scaleX(homeSwRingGap, h.Width))
	w := max32(2, scaleX(homeSwRingW, h.Width))
	out := gap + w
	ring := sdl.Rect{X: r.X - out, Y: r.Y - out, W: r.W + 2*out, H: r.H + 2*out}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	strokeRoundedRectAt(renderer, ring, homeFocusGlide.apply(ring), radius+out, w, homeFocusRingColor())
}

// homeSwRingInk is the ring's resting colour, for the names it labels.
func homeSwRingInk() sdl.Color {
	if homeConsoleLook.hasRing {
		return homeConsoleLook.ring
	}
	return ColorAccent
}

var homeSwWhite = sdl.Color{R: 255, G: 255, B: 255, A: 255}

// homeSwTilePalette is the colour squares' palette: saturated enough to carry
// white type, like the console's own software icons.
var homeSwTilePalette = [...]sdl.Color{
	{R: 0xe4, G: 0x00, B: 0x2b, A: 255}, // red
	{R: 0x1e, G: 0x7b, B: 0xe0, A: 255}, // blue
	{R: 0x2e, G: 0x9e, B: 0x46, A: 255}, // green
	{R: 0xf0, G: 0x64, B: 0x00, A: 255}, // orange
	{R: 0x6a, G: 0x4f, B: 0xc9, A: 255}, // purple
	{R: 0x00, G: 0x8f, B: 0xa0, A: 255}, // teal
	{R: 0xc2, G: 0x18, B: 0x5b, A: 255}, // magenta
	{R: 0x4f, G: 0x5b, B: 0x66, A: 255}, // slate
}

// homeSwCardColors gives the built-in cards their own colour, so the same
// card is the same colour on every device.
var homeSwCardColors = map[HomeFocusID]int{
	HomeFocusEmulator:  0,
	HomeFocusMedia:     1,
	HomeFocusChat:      2,
	HomeFocusPackages:  3,
	HomeFocusApps:      4,
	HomeFocusBrowser:   5,
	HomeFocusFavorites: 6,
	HomeFocusTools:     7,
	HomeFocusFiles:     7,
	HomeFocusSettings:  7,
}

// homeSwHashColor picks a palette colour for any other name.
func homeSwHashColor(name string) sdl.Color {
	f := fnv.New32a()
	_, _ = f.Write([]byte(strings.ToLower(name)))
	return homeSwTilePalette[f.Sum32()%uint32(len(homeSwTilePalette))]
}

// homeSwTileColor is a colour square's fill.
func homeSwTileColor(id HomeFocusID) sdl.Color {
	if i, ok := homeSwCardColors[id]; ok {
		return homeSwTilePalette[i]
	}
	return homeSwHashColor(string(id))
}

// switchPaintTile paints one row tile the Switch Home's way (carRender): it
// reports false when the Switch Home is not up, and the usual painters draw.
// item.Rect is the tile's place; the console's tiles do not lift, only dip
// when pressed.
func (h *HomeLayout) switchPaintTile(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, t homeDynTile, item HomeItem, focused, pressed bool, pressDepth float64) bool {
	if renderer == nil || !h.switchChrome() {
		return false
	}
	r := item.Rect
	if r.W <= 0 || r.H <= 0 {
		return true
	}
	r.Y += homePressOffset(pressDepth, h.Height)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	switch t.Kind {
	case homeTileRecent:
		h.switchPaintRecent(renderer, fonts, t, item, r)
	case homeTileWidget:
		// A white card under the widget, as the console's tiles are solid.
		fillRoundedRectAt(renderer, item.Rect, r, homeTileRadius, ColorSurfaceRaised)
		if t.Dim {
			h.drawHomeAppTile(renderer, fonts, item, r, false, pressed, true)
		} else {
			h.drawHomeWidget(renderer, fonts, homeTileWidgetName(t.Ref), item, r, false, pressed)
		}
	case homeTileAdd:
		h.drawEditAddTile(renderer, fonts, item, r, false, pressed)
	case homeTileCard:
		if t.ID == HomeFocusContinue {
			h.drawHomeContinueCompact(renderer, fonts, item, r, data.Continue, false, pressed)
			break
		}
		h.switchPaintSquare(renderer, fonts, item, homeSwTileColor(t.ID), item.Icon, item.Title, r, false)
	case homeTileApp:
		h.switchPaintSquare(renderer, fonts, item, homeSwTileColor(t.ID), item.Icon, item.Title, r, t.Dim)
	default:
		return false
	}
	if focused {
		// The settled rect, as drawHomeFocusRing does (liquidbg.go).
		liquidNoteFocus(item.Rect)
		h.switchRing(renderer, r, homeTileRadius)
	}
	return true
}

// switchPaintSquare is a colour square: the glyph in its upper middle, the
// name along its foot, and a Chat tile's unread count in its corner.
func (h *HomeLayout) switchPaintSquare(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, fill sdl.Color, icon, title string, r sdl.Rect, dim bool) {
	if dim {
		fill = sdl.Color{R: 0x8a, G: 0x8a, B: 0x8a, A: 255}
	}
	fillRoundedRectAt(renderer, item.Rect, r, homeTileRadius, fill)
	if icon == "" {
		icon = "apps"
	}
	g := r.W * 34 / 100
	drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: r.X + (r.W-g)/2, Y: r.Y + r.H*42/100 - g/2, W: g, H: g}, homeSwWhite)
	if f := fonts.Small; f != nil && title != "" {
		pad := max32(4, r.W/12)
		text := ellipsize(f, title, r.W-2*pad)
		drawText(renderer, f, text, r.X+r.W/2, r.Y+r.H-pad-int32(f.Height()), homeSwWhite, textAlignCenter)
	}
	if !dim {
		if _, n := homeChatTileUnread(item); n > 0 {
			h.drawHomeChatBadge(renderer, fonts, r, n)
		}
	}
}

// switchPaintRecent is a recent game: its box art edge to edge once it is
// baked, a colour square with the pad glyph and its name until then (or for
// good, for a game with no art).
func (h *HomeLayout) switchPaintRecent(renderer *sdl.Renderer, fonts HomeFonts, t homeDynTile, item HomeItem, r sdl.Rect) {
	var job homePreviewJob
	system := t.Title
	if i := homeSwRecentIndex(t.ID); i >= 0 {
		job, system = homeSwitch.recents[i].art, homeSwitch.recents[i].system
	}
	if tex := homeSwArtTexture(renderer, job, r.W, r.H); tex != nil {
		_ = renderer.Copy(tex, nil, &r)
		return
	}
	// The console's colour: the same game keeps it however long ago it was
	// played.
	h.switchPaintSquare(renderer, fonts, item, homeSwHashColor(system), "emulator", t.Title, r, false)
}

// switchDrawTitle writes the focused tile's name over the row, from the
// tile's left edge, in the ring's colour (carDrawConsoleLabels). Nothing
// while the focus is off the row, or while Home is being edited (the strip
// has that place). It always reports true: the row has no dots.
func (h *HomeLayout) switchDrawTitle(renderer *sdl.Renderer, fonts HomeFonts) bool {
	if renderer == nil || !h.switchChrome() {
		return true
	}
	cur := h.Focus.Current()
	if h.dynTileIndex(cur) < 0 {
		return true
	}
	item := h.Items[cur]
	title := item.Title
	if cur == HomeFocusContinue {
		if st := homeContinueSnapshot(); st.HasRecent && st.Title != "" {
			title = st.Title
		}
	}
	f := fonts.Card
	if f == nil {
		f = fonts.Tile
	}
	r := item.Rect
	if f == nil || r.W <= 0 || strings.TrimSpace(title) == "" {
		return true
	}
	margin := scaleX(homeSideMargin, h.Width)
	text := ellipsize(f, title, min32(r.W*5/2, h.Width-2*margin))
	tw, _ := measureText(f, text)
	x := max32(margin, min32(r.X, h.Width-margin-tw))
	_, sy, _ := h.homeSwScale()
	y := int32(math.Round((homeSwRowTop - homeSwTitleUp) * sy))
	drawText(renderer, f, text, x, y, TextInk(homeSwRingInk()), textAlignLeft)
	return true
}

// ── The round buttons ─────────────────────────────────────────────────────

// homeSwDockEntries are the round buttons, in order. PlayStation Remote Play
// is there only where it runs (psRemoteSupported); Xbox runs everywhere.
func homeSwDockEntries() []homeSwDockEntry {
	out := make([]homeSwDockEntry, 0, 9)
	out = append(out,
		homeSwDockEntry{ID: homeSwDockPrefix + "games", Title: "Games", Icon: "emulator", Ink: sdl.Color{R: 0xe6, G: 0x00, B: 0x12, A: 255}, Filled: true},
		homeSwDockEntry{ID: homeSwDockPrefix + "chat", Title: "Chat", Icon: "chat", Ink: sdl.Color{R: 0x58, G: 0x65, B: 0xf2, A: 255}},
		homeSwDockEntry{ID: homeSwDockPrefix + "store", Title: "Store", Icon: "store", Ink: sdl.Color{R: 0xf0, G: 0x6e, B: 0x00, A: 255}},
		homeSwDockEntry{ID: homeSwDockPrefix + "search", Title: "Search", Icon: "search", Ink: sdl.Color{R: 0x00, G: 0x9c, B: 0xd6, A: 255}},
	)
	if psRemoteSupported() {
		out = append(out, homeSwDockEntry{ID: homeSwDockPrefix + "ps", Title: "PlayStation Remote Play", Icon: "gamepad", Ink: sdl.Color{R: 0x23, G: 0x5c, B: 0xd8, A: 255}})
	}
	out = append(out,
		homeSwDockEntry{ID: homeSwDockPrefix + "xbox", Title: "Xbox Remote Play", Icon: "emu-xbox", Ink: sdl.Color{R: 0x10, G: 0x8a, B: 0x10, A: 255}},
		homeSwDockEntry{ID: homeSwDockPrefix + "phone", Title: "Phone Companion", Icon: "phone", Grey: true},
		homeSwDockEntry{ID: homeSwDockPrefix + "settings", Title: "Settings", Icon: "settings", Grey: true},
		homeSwDockEntry{ID: homeSwDockPrefix + "power", Title: "Power", Icon: "power", Grey: true},
	)
	return out
}

// switchDockIDs builds the round buttons for a rebuilt body and returns the
// focus IDs the Switch Home adds (the picture first); none when the row is
// not wanted (dynRebuild hands them to SetDynamic).
func (h *HomeLayout) switchDockIDs() []HomeFocusID {
	if !h.switchRowWanted() {
		homeSwitch.dock = nil
		if !homeConsoleLook.switchHome || h.Arrangement != homeArrangementCarousel {
			// The look is gone, not just being edited: so is its box art.
			homeSwArtDropTextures()
		}
		return nil
	}
	homeSwitch.dock = homeSwDockEntries()
	ids := make([]HomeFocusID, 0, len(homeSwitch.dock)+1)
	ids = append(ids, homeSwAvatar)
	// Items, so whatever names the focused control (the voice assistant's
	// "where am I", the footer) has their names; they are not on Order, so
	// neither the generic painter nor FocusAt's loop sees them.
	h.Items[homeSwAvatar] = HomeItem{ID: homeSwAvatar, Title: "Your page", Subtitle: "Trophies and playtime", Icon: "user"}
	for _, e := range homeSwitch.dock {
		ids = append(ids, e.ID)
		h.Items[e.ID] = HomeItem{ID: e.ID, Title: e.Title, Icon: e.Icon}
	}
	return ids
}

// homeSwDockIndex is a round button's place, or -1.
func homeSwDockIndex(id HomeFocusID) int {
	for i, e := range homeSwitch.dock {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// switchDockRect is round button i of n: discs centred on the screen,
// halfway between the row and the footer's hairline.
func (h *HomeLayout) switchDockRect(i, n int) sdl.Rect {
	d := min32(scaleX(homeSwDockD, h.Width), scaleY(homeSwDockD, h.Height))
	pitch := max32(scaleX(homeSwDockPitch, h.Width), d+scaleX(16, h.Width))
	if n > 1 {
		// A narrow window: the line closes up before it runs off screen.
		room := h.Width - 2*scaleX(homeSideMargin, h.Width) - d
		pitch = max32(min32(pitch, room/int32(n-1)), d+2)
	}
	cy := (h.switchRowBottom() + scaleY(homeSwLineY, h.Height)) / 2
	total := pitch*int32(max(n-1, 0)) + d
	return sdl.Rect{X: (h.Width-total)/2 + int32(i)*pitch, Y: cy - d/2, W: d, H: d}
}

// switchDrawDock draws the round buttons, the focused one's ring and name
// (Render, after the row). It is also where a game just played takes the
// focus back to the front of the row.
func (h *HomeLayout) switchDrawDock(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) {
	if renderer == nil || !h.switchChrome() {
		return
	}
	s := &homeSwitch
	cur := h.Focus.Current()
	n := len(s.dock)
	label, labelRect := "", sdl.Rect{}
	for i, e := range s.dock {
		r := h.switchDockRect(i, n)
		if cur == e.ID {
			liquidNoteFocus(r)
		}
		if data.Pressed == e.ID {
			r.Y += homePressOffset(data.PressDepth, h.Height)
		}
		h.switchDrawButton(renderer, fonts, e, r)
		if cur == e.ID {
			h.switchRing(renderer, r, r.W/2)
			label, labelRect = e.Title, r
		}
	}
	f := fonts.Helper
	if f == nil {
		f = fonts.Small
	}
	if label == "" || f == nil {
		return
	}
	tw, th := measureText(f, label)
	margin := scaleX(homeSideMargin, h.Width)
	x := max32(margin, min32(labelRect.X+labelRect.W/2-tw/2, h.Width-margin-tw))
	out := max32(2, scaleX(homeSwRingGap, h.Width)) + max32(2, scaleX(homeSwRingW, h.Width))
	y := labelRect.Y - out - scaleY(6, h.Height) - th
	drawText(renderer, f, label, x, y, TextInk(homeSwRingInk()), textAlignLeft)
}

// switchDrawButton is one round button: a soft shadow, the disc, the glyph,
// and on Chat the unread count.
func (h *HomeLayout) switchDrawButton(renderer *sdl.Renderer, fonts HomeFonts, e homeSwDockEntry, r sdl.Rect) {
	cx, cy, rad := r.X+r.W/2, r.Y+r.H/2, r.W/2
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillCircle(renderer, cx, cy+max32(1, rad/14), rad, sdl.Color{A: 34})
	disc, ink := ColorSurfaceRaised, e.Ink
	if e.Grey {
		ink = ColorTextSecondary()
	}
	if e.Filled {
		disc, ink = e.Ink, homeSwWhite
	}
	fillCircle(renderer, cx, cy, rad, disc)
	if !e.Filled {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, rad, 1, homeGlass(24))
	}
	g := r.W / 2
	drawHomeGlyphTinted(renderer, e.Icon, sdl.Rect{X: cx - g/2, Y: cy - g/2, W: g, H: g}, ink)
	if e.ID != homeSwDockPrefix+"chat" || fonts.Meta == nil {
		return
	}
	n := chatUnreadTotal()
	badge := chatBadgeLabel(n)
	if n <= 0 || badge == "" {
		return
	}
	tw, th := measureText(fonts.Meta, badge)
	bh := th + scaleY(4, h.Height)
	bw := max32(bh, tw+scaleX(10, h.Width))
	bx, by := r.X+r.W-bw*2/3, r.Y-bh/5
	fillRoundedRect(renderer, bx, by, bw, bh, bh/2, sdl.Color{R: 0xe6, G: 0x00, B: 0x12, A: 255})
	drawText(renderer, fonts.Meta, badge, bx+bw/2, by+(bh-th)/2, homeSwWhite, textAlignCenter)
}

// ── The corner picture ────────────────────────────────────────────────────

// homeSwAvatarColors are the picture's backgrounds, one per name.
var homeSwAvatarColors = [...]sdl.Color{
	{R: 0xff, G: 0x6f, B: 0x3c, A: 255},
	{R: 0x2f, G: 0x9b, B: 0xff, A: 255},
	{R: 0x3c, G: 0xc0, B: 0x6a, A: 255},
	{R: 0xb4, G: 0x5c, B: 0xff, A: 255},
	{R: 0xff, G: 0xb3, B: 0x00, A: 255},
	{R: 0xff, G: 0x4f, B: 0x8b, A: 255},
}

// homeBrandOrAvatar draws the header's left end (drawHeader): the wordmark,
// or on the Switch Home the user's picture. It returns where the bar's
// next control may start.
func (h *HomeLayout) homeBrandOrAvatar(renderer *sdl.Renderer, config *Config, fonts HomeFonts, barH int32, caption string) int32 {
	homeSwitch.avatar = sdl.Rect{}
	if !h.switchChrome() {
		return drawHeaderBrand(renderer, config, barH, caption)
	}
	d := min32(scaleX(homeSwAvatarD, h.Width), scaleY(homeSwAvatarD, h.Height))
	d = min32(d, barH)
	r := sdl.Rect{X: StatusBarMargin, Y: max32(2, (barH-d)/2+scaleY(4, h.Height)), W: d, H: d}
	h.switchDrawAvatar(renderer, config, fonts, r)
	homeSwitch.avatar = r
	return r.X + r.W
}

// switchDrawAvatar is the picture: a white-rimmed disc in the name's colour
// with its first letter, or the person glyph with no name (or in a
// screenshot, which never shows the name).
func (h *HomeLayout) switchDrawAvatar(renderer *sdl.Renderer, config *Config, fonts HomeFonts, r sdl.Rect) {
	if renderer == nil || r.W <= 0 {
		return
	}
	name := ""
	if !shotModeOn() {
		name = strings.TrimSpace(userName(config))
	}
	initial := ""
	if rs := []rune(name); len(rs) > 0 {
		initial = strings.ToUpper(string(rs[0]))
	}
	fill := homeSwAvatarColors[0]
	if name != "" {
		f := fnv.New32a()
		_, _ = f.Write([]byte(strings.ToLower(name)))
		fill = homeSwAvatarColors[f.Sum32()%uint32(len(homeSwAvatarColors))]
	}
	cx, cy, rad := r.X+r.W/2, r.Y+r.H/2, r.W/2
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillCircle(renderer, cx, cy+1, rad, sdl.Color{A: 40})
	fillCircle(renderer, cx, cy, rad, homeSwWhite)
	inner := rad - max32(2, rad/10)
	fillCircle(renderer, cx, cy, inner, fill)
	if font := fonts.Card; initial != "" && font != nil {
		drawText(renderer, font, initial, cx, cy-int32(font.Height())/2, homeSwWhite, textAlignCenter)
	} else {
		g := inner
		drawHomeGlyphTinted(renderer, "user", sdl.Rect{X: cx - g/2, Y: cy - g/2, W: g, H: g}, homeSwWhite)
	}
	if h.Focus.Current() == homeSwAvatar {
		liquidNoteFocus(r)
		h.switchRing(renderer, r, rad)
	}
}

// ── The footer ────────────────────────────────────────────────────────────

// switchDrawFooter draws the Switch Home's footer in place of the usual band
// (Render): the hairline, the controllers and two hints — Start for the
// menu, A for the focus ("Start" on a game, as the console says). false when
// the Switch Home is not up.
func (h *HomeLayout) switchDrawFooter(renderer *sdl.Renderer, fonts HomeFonts) bool {
	if renderer == nil || !h.switchChrome() {
		return false
	}
	edge := scaleX(20, h.Width)
	lineY := scaleY(homeSwLineY, h.Height)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, WithAlpha(ColorTextPrimary(), 70))
	_ = renderer.FillRect(&sdl.Rect{X: edge, Y: lineY, W: h.Width - 2*edge, H: max32(1, scaleY(2, h.Height)/2)})
	cy := (lineY + h.Height) / 2
	h.switchDrawPads(renderer, scaleX(36, h.Width), cy)
	f := fonts.Small
	if f == nil {
		return true
	}
	aLabel := "Open"
	if i := h.dynTileIndex(h.Focus.Current()); i >= 0 && h.dyn.tiles[i].Kind == homeTileRecent {
		aLabel = "Start"
	}
	hints := [...]footerHint{{button: "Start", label: "Menu"}, {button: "A", label: aLabel}}
	fh := int32(f.Height())
	badgeD := scaleY(26, h.Height)
	x := h.Width - scaleX(40, h.Width)
	for i := len(hints) - 1; i >= 0; i-- {
		hint := hints[i]
		key := homeHintKey(hint.button, hint.label)
		lw, _ := measureText(f, hint.label)
		kw, _ := measureText(f, key)
		bw := max32(badgeD, kw+scaleX(12, h.Width))
		x -= lw
		drawText(renderer, f, hint.label, x, cy-fh/2, ColorTextPrimary(), textAlignLeft)
		x -= scaleX(8, h.Width) + bw
		fillRoundedRect(renderer, x, cy-badgeD/2, bw, badgeD, badgeD/2, ColorTextPrimary())
		drawText(renderer, f, key, x+bw/2, cy-fh/2, ColorBackground, textAlignCenter)
		x -= scaleX(26, h.Width)
	}
	return true
}

// switchDrawPads is the pair of controllers at the footer's left end, with
// the four player lights beside them, the first one lit.
func (h *HomeLayout) switchDrawPads(renderer *sdl.Renderer, x, cy int32) {
	ink := WithAlpha(ColorTextSecondary(), 230)
	w := max32(6, scaleX(11, h.Width))
	ph := max32(14, scaleY(26, h.Height))
	gap := max32(2, scaleX(3, h.Width))
	dot := max32(2, scaleY(4, h.Height))
	step := (ph - dot) / 3
	for k := int32(0); k < 4; k++ {
		c := WithAlpha(ColorTextTertiary(), 150)
		if k == 0 {
			c = homeSwRingInk()
		}
		fillRoundedRect(renderer, x, cy-ph/2+k*step, dot, dot, dot/2, c)
	}
	px := x + dot + gap*2
	fillRoundedRect(renderer, px, cy-ph/2, w, ph, w/2, ink)
	fillRoundedRect(renderer, px+w+gap, cy-ph/2, w, ph, w/2, ink)
}

// switchChromeOff switches the controls the Switch Home does not draw off
// for the frame (Render, after the pills are sized): the search pill, the
// power and Edit pills. The search pill comes back on with the usual chrome;
// the pills come back on their own (sizePowerPill, sizeEditPill).
func (h *HomeLayout) switchChromeOff() {
	on := h.switchChrome()
	h.Focus.SetEnabled(HomeFocusSearch, !on)
	if !on {
		return
	}
	h.Focus.SetEnabled(HomeFocusExit, false)
	h.Focus.SetEnabled(HomeFocusEditPill, false)
	// The power card opens over the round Power button, not over the hidden
	// pill (renderHomePowerConfirm anchors to the pill's rect, which
	// sizePowerPill sets again every frame).
	if k := homeSwDockIndex(homeSwDockPrefix + "power"); k >= 0 {
		item := h.Items[HomeFocusExit]
		item.Rect = h.switchDockRect(k, len(homeSwitch.dock))
		h.Items[HomeFocusExit] = item
	}
}

// ── Pointer and D-pad ─────────────────────────────────────────────────────

// switchHitAt is the Switch Home control under a point (FocusAt): the
// picture or a round button; "" for anything else.
func (h *HomeLayout) switchHitAt(x, y int32) HomeFocusID {
	if !h.switchChrome() {
		return ""
	}
	if r := homeSwitch.avatar; r.W > 0 && pointInRect(x, y, r) {
		return homeSwAvatar
	}
	n := len(homeSwitch.dock)
	for i, e := range homeSwitch.dock {
		if pointInRect(x, y, h.switchDockRect(i, n)) {
			return e.ID
		}
	}
	return ""
}

// switchHeaderFirst is the status row's leftmost control drawn, or "".
func (h *HomeLayout) switchHeaderFirst() HomeFocusID {
	best, bestX := HomeFocusID(""), int32(math.MaxInt32)
	for _, id := range homeHeaderFocusIDs {
		if !h.Focus.IsEnabled(id) {
			continue
		}
		if r := h.Items[id].Rect; r.W > 0 && r.X < bestX {
			best, bestX = id, r.X
		}
	}
	return best
}

// switchDockBelow is the round button nearest under r's middle.
func (h *HomeLayout) switchDockBelow(r sdl.Rect) HomeFocusID {
	n := len(homeSwitch.dock)
	best, bestD := HomeFocusID(""), int32(math.MaxInt32)
	cx := r.X + r.W/2
	for i, e := range homeSwitch.dock {
		d := h.switchDockRect(i, n)
		dx := d.X + d.W/2 - cx
		if dx < 0 {
			dx = -dx
		}
		if dx < bestD {
			best, bestD = e.ID, dx
		}
	}
	return best
}

// switchMove is a D-pad step the Switch Home owns (dynMove): along the round
// buttons, Down from the row onto them and Up back, Up from the row's left
// end onto the picture and between the picture and the status row. false
// leaves the press to the body's usual rules.
func (h *HomeLayout) switchMove(cur HomeFocusID, dir FocusDirection) (HomeFocusID, bool) {
	if !h.switchChrome() {
		return "", false
	}
	s := &homeSwitch
	set := func(id HomeFocusID) (HomeFocusID, bool) {
		if id != "" && id != cur && h.Focus.Set(id) {
			h.Focus.Remember()
			if h.dynIsTile(id) {
				h.dynEnsureVisible(id)
			}
		}
		return h.Focus.Current(), true
	}
	if k := homeSwDockIndex(cur); k >= 0 {
		switch dir {
		case FocusLeft:
			if k > 0 {
				return set(s.dock[k-1].ID)
			}
		case FocusRight:
			if k < len(s.dock)-1 {
				return set(s.dock[k+1].ID)
			}
		case FocusUp:
			return set(h.dynEntryBelow(sdl.Rect{}))
		}
		return cur, true
	}
	if cur == homeSwAvatar {
		switch dir {
		case FocusDown:
			return set(h.dynEntryBelow(sdl.Rect{}))
		case FocusRight:
			return set(h.switchHeaderFirst())
		}
		return cur, true
	}
	if dir == FocusLeft && s.avatar.W > 0 && cur != "" && cur == h.switchHeaderFirst() {
		return set(homeSwAvatar)
	}
	if !h.dynIsTile(cur) {
		return "", false
	}
	// Where the tile settles, not where it is sliding through: a tile that
	// wrapped round from the other end has no rect until it is on screen.
	r := h.switchSettledRect(h.dynTileIndex(cur))
	switch dir {
	case FocusDown:
		if len(s.dock) == 0 {
			return cur, true
		}
		return set(h.switchDockBelow(r))
	case FocusUp:
		// The picture when it is nearer the tile than any status control.
		if s.avatar.W <= 0 {
			return "", false
		}
		best := h.dynHeaderAbove(cur)
		if best == "" || homeDynSideScore(r, s.avatar) <= homeDynSideScore(r, h.Items[best].Rect) {
			return set(homeSwAvatar)
		}
	}
	return "", false
}

// ── Activation ────────────────────────────────────────────────────────────

// homeSwitchActivate runs A on one of the Switch Home's own controls
// (homeActivate, before its generic sound): false for any other control.
func homeSwitchActivate(config *Config, id HomeFocusID) bool {
	sid := string(id)
	if !strings.HasPrefix(sid, "sw:") {
		return false
	}
	if config == nil {
		config = appConfig
	}
	logDebug("home", "activate card %s", id)
	homePressedID = id
	homePressedAt = sdl.GetTicks64()
	switch {
	case id == homeSwAvatar:
		PlayActivateSound()
		headerTrophyActivate(config)
	case strings.HasPrefix(sid, homeSwRecentPrefix):
		i := homeSwRecentIndex(id)
		if i < 0 {
			return true
		}
		PlayActivateSound()
		homeContinueInvalidate()
		homePlayRecent(config, homeSwitch.recents[i].item)
	case strings.HasPrefix(sid, homeSwDockPrefix):
		homeSwDockActivate(config, sid[len(homeSwDockPrefix):])
	}
	return true
}

// homeSwDockActivate runs one round button: each does what its Home card,
// header control or footer pill does.
func homeSwDockActivate(config *Config, name string) {
	switch name {
	case "phone":
		// The full-screen pairing code plays its own sound.
		homeShowPairingQR()
		return
	case "ps":
		// The PS launchers play their own sound too. The console that is
		// ready to stream; the PS5 (installing its client first) when
		// neither is.
		if ps4TileCurrentState() == ps4TileLaunch && ps5TileCurrentState() != ps5TileLaunch {
			ps4TileActivate()
		} else {
			ps5TileActivate()
		}
		return
	}
	PlayActivateSound()
	switch name {
	case "games":
		openGameLibraryPanel()
	case "chat":
		homeSwOpenCard(config, HomeFocusChat)
	case "store":
		homeSwOpenCard(config, HomeFocusPackages)
	case "settings":
		homeSwOpenCard(config, HomeFocusSettings)
	case "search":
		homeSearchOpen(config, homeActivateFromPad || homeOnHandheld())
	case "xbox":
		x1TileActivate()
	case "power":
		homePowerOpen()
	}
}

// homeSwOpenCard opens the screen a built-in Home card opens, the way
// homeActivate does for the card itself.
func homeSwOpenCard(config *Config, card HomeFocusID) {
	target := homeSceneTarget(config, card)
	if target == "" {
		return
	}
	idx := sceneIndexExact(config, target)
	logDebug("home", "%s -> scene %q (index %d)", card, target, idx)
	if idx < 0 {
		ShowToast(sceneDisplayName(target)+" is not part of this setup", ToastKindInfo)
		return
	}
	changeSceneTo(config, idx)
}

// ── Box art ───────────────────────────────────────────────────────────────

// homeSwArtKey names one baked box art: the picture (its preview key, which
// carries the file's time), the tile's size and the corner radius baked in.
type homeSwArtKey struct {
	key  string
	w, h int32
	r    int32
}

// homeSwArt holds the row's box art: the PNGs the worker baked (rounded,
// cropped to the square) and, on the render thread, their textures. One
// worker at a time, so a row of eight new covers does not decode eight
// pictures at once on the handheld.
var homeSwArt = struct {
	mu      sync.Mutex
	png     map[homeSwArtKey][]byte
	pending map[homeSwArtKey]bool
	failed  map[homeSwArtKey]time.Time
	busy    chan struct{}
	// Render thread only:
	tex   map[homeSwArtKey]*sdl.Texture
	owner *sdl.Renderer
}{
	png:     map[homeSwArtKey][]byte{},
	pending: map[homeSwArtKey]bool{},
	failed:  map[homeSwArtKey]time.Time{},
	busy:    make(chan struct{}, 1),
	tex:     map[homeSwArtKey]*sdl.Texture{},
}

// homeSwArtTexture is job's art baked for a w×h tile, or nil while it is
// being made (or when there is none). Cheap to call every frame.
func homeSwArtTexture(renderer *sdl.Renderer, job homePreviewJob, w, h int32) *sdl.Texture {
	if renderer == nil || job.Key == "" || job.File == "" || w <= 0 || h <= 0 {
		return nil
	}
	a := &homeSwArt
	if a.owner != nil && a.owner != renderer {
		// Textures belong to the renderer that made them and die with it:
		// forget them, never destroy them now.
		a.tex = map[homeSwArtKey]*sdl.Texture{}
		a.owner = nil
	}
	key := homeSwArtKey{key: job.Key, w: w, h: h, r: homeTileRadius}
	if tex := a.tex[key]; tex != nil {
		return tex
	}
	a.mu.Lock()
	data, ready := a.png[key]
	if !ready && !a.pending[key] {
		if at, failed := a.failed[key]; !failed || time.Since(at) >= homeSwArtRetry {
			a.pending[key] = true
			go homeSwArtBuild(job.File, key)
		}
	}
	a.mu.Unlock()
	if !ready || len(data) == 0 {
		return nil
	}
	// Uploading spends the per-frame picture budget the thumbnails use.
	if !allowThumbnailDecode(frameCount, thumbnailDecodeBudgetPerFrame()) {
		return nil
	}
	rw, err := sdl.RWFromMem(data)
	if err != nil {
		return nil
	}
	tex, err := img.LoadTextureRW(renderer, rw, false)
	rw.Close()
	if err != nil || tex == nil {
		a.mu.Lock()
		delete(a.png, key)
		a.failed[key] = time.Now()
		a.mu.Unlock()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	if len(a.tex) >= homeSwArtMax {
		homeSwArtDropTextures()
	}
	a.tex[key] = tex
	a.owner = renderer
	a.mu.Lock()
	delete(a.png, key)
	a.mu.Unlock()
	return tex
}

// homeSwArtBuild bakes one box art (disk cache first) and hands the PNG to
// the render thread.
func homeSwArtBuild(path string, key homeSwArtKey) {
	a := &homeSwArt
	a.busy <- struct{}{}
	defer func() { <-a.busy }()
	disk := fmt.Sprintf("hswart:%s|%dx%d|r%d", key.key, key.w, key.h, key.r)
	data := homePreviewDiskGet(disk)
	if len(data) == 0 {
		src, err := homeSwDecodeArt(path)
		if err == nil {
			data, err = homePreviewPNG(src, int(key.w), int(key.h), int(key.r))
		}
		if err != nil || len(data) == 0 {
			a.mu.Lock()
			delete(a.pending, key)
			a.failed[key] = time.Now()
			a.mu.Unlock()
			return
		}
		homePreviewDiskPut(disk, data)
	}
	a.mu.Lock()
	delete(a.pending, key)
	a.png[key] = data
	a.mu.Unlock()
}

// homeSwDecodeArt decodes a cover after checking its size: a scraped
// picture is untrusted, and a huge one would take the handheld's memory.
func homeSwDecodeArt(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return nil, err
	}
	if !libraryArtDimensionsAllowed(cfg.Width, cfg.Height) {
		return nil, errLibraryArtDimensions
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	pic, _, err := image.Decode(bufio.NewReader(f))
	return pic, err
}

// homeSwArtDropTextures frees every baked texture (the cap was reached).
func homeSwArtDropTextures() {
	a := &homeSwArt
	for k, tex := range a.tex {
		if tex != nil && a.owner != nil {
			tex.Destroy()
		}
		delete(a.tex, k)
	}
}

// homeSwArtPrune forgets the art of games that left the row (switchRowTiles,
// on the render thread).
func homeSwArtPrune() {
	keep := make(map[string]bool, len(homeSwitch.recents))
	for _, r := range homeSwitch.recents {
		if r.art.Key != "" {
			keep[r.art.Key] = true
		}
	}
	a := &homeSwArt
	for k, tex := range a.tex {
		if keep[k.key] {
			continue
		}
		if tex != nil && a.owner != nil {
			tex.Destroy()
		}
		delete(a.tex, k)
	}
	a.mu.Lock()
	for k := range a.png {
		if !keep[k.key] {
			delete(a.png, k)
		}
	}
	for k := range a.failed {
		if !keep[k.key] {
			delete(a.failed, k)
		}
	}
	a.mu.Unlock()
}

// ── The theme card ────────────────────────────────────────────────────────

// drawSwitchHomeSketch draws the theme card's picture of the Switch Home
// (drawThemeSketchFor): the row of squares with the outlined one and its
// name over it, the line of round buttons under it. false for any other
// look, which the caller draws.
func drawSwitchHomeSketch(renderer *sdl.Renderer, p ThemePreset, style string, x, y, w, h int32, ink, accent sdl.Color) bool {
	if style != homeCarouselStrip || !homeIsSwitchHomeStyle(p.CarouselStyle) || w <= 8 || h <= 8 {
		return false
	}
	ring := accent
	if c, ok := parseHexColor(p.FocusRing); ok {
		ring = c
	}
	soft := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 90}
	right := x + w
	gap := max32(w/48, 2)
	side := h * 44 / 100
	bar := max32(h/16, 2)
	y0 := y + h*24/100
	for k := int32(0); ; k++ {
		sx := x + w/14 + k*(side+gap)
		if sx >= right {
			break
		}
		fillRoundedRect(renderer, sx, y0, min32(side, right-sx), side, 2, soft)
		if k == 0 {
			strokeRoundedRect(renderer, sx-2, y0-2, side+4, side+4, 3, 2, ring)
			fillRoundedRect(renderer, sx, y0-bar*2-2, side*3/4, bar, 1, ring)
		}
	}
	d := max32(h/10, 3)
	cy := y0 + side + (y+h-(y0+side))/2
	const count = 7
	pitch := d + d/2 + 1
	start := x + w/2 - pitch*(count-1)/2
	for k := int32(0); k < count; k++ {
		c := soft
		if k == 0 {
			c = ring
		}
		fillCircle(renderer, start+k*pitch, cy, d/2, c)
	}
	return true
}
