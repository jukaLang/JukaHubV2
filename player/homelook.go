package main

// homelook.go — Home's main menu look: three switches
// and what they share.
//
//   - Dim unselected art (homeDimArt, on unless switched off): on the Tabs
//     Home and in the Games library every cover but the selected one is drawn
//     darker and greyer, so the selection stands out. The cover textures are
//     cached and shared, so the dim is a colour mod put back after each copy
//     and a grey wash over the copy — never a greyscale copy of the picture,
//     which would double the covers' memory on the 1 GB TrimUI. The newly
//     selected cover brightens, and the one it left darkens, over
//     homeLookDimMS.
//   - List edge fade (listEdgeFade, on unless switched off): a long list or
//     grid fades into the page at an edge with more past it (listfade.go).
//   - Tabs position (homeTabsPosition, "top" unless "bottom"): the Tabs Home's
//     strip in the top bar, or in a bar of its own over the footer
//     (hometabs_draw.go). Down from the page's bottom row then reaches it, and
//     Up from it goes back into the page (hometabs.go, tabsMove).
//
// The three are Custom strings ("true"/"false", "top"/"bottom") written the
// way the Tabs Home writes its own (homeTabsWrite). They are read on every
// frame, so the readers below look the value up without formatting it.
// The rows (homeLookRows) are the Tabs Home's arranging sheet's (L2,
// hometabs_edit.go), and are made to stand in Display & theme too.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The keys in Variables.Custom.
const (
	homeDimArtKey       = "homeDimArt"
	listEdgeFadeKey     = "listEdgeFade"
	homeTabsPositionKey = "homeTabsPosition"
)

// The stops of Tabs position.
const (
	homeTabsPositionTop    = "top"
	homeTabsPositionBottom = "bottom"
)

const (
	// homeLookDimMod is the colour mod of a fully dimmed cover (of 255).
	homeLookDimMod = 132
	// homeLookDimWash is the alpha of the grey wash over a fully dimmed
	// cover: it takes the colour out as well as the light.
	homeLookDimWash = 80
	// homeLookDimMS is how long a cover takes to brighten or darken when the
	// selection moves.
	homeLookDimMS = 160
)

// homeLookBool reads a Custom switch stored as a bool or as "true"/"false"
// ("on"/"off", "1"/"0"), def when unset or unreadable. It does not allocate.
func homeLookBool(c *Config, key string, def bool) bool {
	if c == nil || c.Variables.Custom == nil {
		return def
	}
	switch v := c.Variables.Custom[key].(type) {
	case bool:
		return v
	case string:
		switch strings.TrimSpace(v) {
		case "true", "True", "on", "1", "yes":
			return true
		case "false", "False", "off", "0", "no":
			return false
		}
	}
	return def
}

// homeDimArtOn reports whether unselected covers are dimmed.
func homeDimArtOn(c *Config) bool { return homeLookBool(c, homeDimArtKey, true) }

// listEdgeFadeOn reports whether lists fade at an edge with more past it.
func listEdgeFadeOn(c *Config) bool { return homeLookBool(c, listEdgeFadeKey, true) }

// homeTabsStripBottom reports whether the Tabs Home's strip sits over the
// footer instead of in the top bar.
func homeTabsStripBottom() bool {
	c := appConfig
	if c == nil || c.Variables.Custom == nil {
		return false
	}
	s, _ := c.Variables.Custom[homeTabsPositionKey].(string)
	return strings.TrimSpace(s) == homeTabsPositionBottom
}

// homeTabsPositionLabel is a position's name.
func homeTabsPositionLabel(bottom bool) string {
	if bottom {
		return "Bottom"
	}
	return "Top"
}

// homeTabsSetPosition stores the strip's place and lets the strip and the
// pages lay themselves out again.
func homeTabsSetPosition(c *Config, bottom bool) {
	v := homeTabsPositionTop
	if bottom {
		v = homeTabsPositionBottom
	}
	homeTabsWrite(c, homeTabsPositionKey, v)
	homeTabs.pillPlaced = false
	homeTabs.brandYield = false
	for i := range homeTabs.pages {
		for j := range homeTabs.pages[i].levels {
			homeTabs.pages[i].levels[j].placed = false
		}
	}
}

// ── The dim ──────────────────────────────────────────────────────────────

// homeLookEase follows one list's selection for the dim: cur is selected,
// prev was, and enter runs from the change. key names the list (a tab, a
// folder, the library's level): a new list starts settled, with nothing
// fading out.
type homeLookEase struct {
	key string
	// depth is how deep in its tab the list is. A folder with no view of its
	// own shares its parent's key, so the key alone would carry the parent's
	// selection into the folder and fade a cover that was never selected there.
	depth     int
	cur, prev int
	enter     uiEnter
	p         float64
	// from is how bright prev was when the selection left it (1 once it had
	// brightened): a quick second move darkens it from there, not from full.
	from   float64
	placed bool
	// frame is the frame (frameCount) the list was last drawn in. A list
	// that missed a frame was off screen, and its selection may have moved
	// meanwhile (the library's console picked from Home, a random game, a
	// rescan): easing that move on the way back in would flash, so it starts
	// settled instead.
	frame uint64
}

// The two lists that dim their covers.
var (
	homeTabsDimEase homeLookEase
	gameLibDimEase  homeLookEase
)

// note records this frame's selection in the list key at depth; motion off
// snaps it. It reports whether the ease is still running (the caller keeps
// frames coming).
func (e *homeLookEase) note(key string, depth, focus int, motion bool) bool {
	now := uiClockMS()
	// Drawn twice in one frame (a layer redraw) is still on screen.
	missed := e.frame+1 < frameCount
	e.frame = frameCount
	switch {
	case !e.placed || e.key != key || e.depth != depth || missed:
		e.key, e.depth, e.cur, e.prev, e.placed, e.from = key, depth, focus, -1, true, 0
		e.enter.cancel()
	case focus != e.cur:
		e.from = 1 - e.dim(e.cur)
		e.prev, e.cur = e.cur, focus
		if motion {
			e.enter.begin(now, homeLookDimMS)
		} else {
			e.enter.cancel()
		}
	}
	if !motion {
		e.enter.cancel()
	}
	e.p = uiEaseOutCubic(e.enter.progress(now))
	return e.enter.active
}

// animating reports whether the ease is still running, for the frame-rate
// controller (uiAnimating). Only note() and this move the clock on, so a
// list left mid-ease would otherwise read as running for ever and hold the
// fast frame cadence (and the battery) after it is gone.
func (e *homeLookEase) animating() bool {
	e.enter.progress(uiClockMS())
	return e.enter.active
}

// dim is item i's dim this frame: 0 for the selected cover once it has
// brightened, 1 for every cover that is not selected.
func (e *homeLookEase) dim(i int) float64 {
	switch i {
	case e.cur:
		return 1 - e.p
	case e.prev:
		return 1 - e.from*(1-e.p)
	}
	return 1
}

// homeLookWashColor is the grey a dimmed cover is washed with: a dark grey on
// a dark theme, a pale one on a light theme, so the dim reads as "faded"
// either way.
func homeLookWashColor() sdl.Color {
	if glassLightTheme() {
		return sdl.Color{R: 214, G: 216, B: 220, A: 255}
	}
	return sdl.Color{R: 58, G: 60, B: 66, A: 255}
}

// homeLookCopyDim copies tex (src of it, nil for all) to dst dimmed by dim (0
// full colour, 1 fully dimmed) at the page's alpha. The colour mod is put back
// to white after the copy: the texture is shared with every other screen.
// The caller has set any alpha mod it wants and puts it back itself.
func homeLookCopyDim(renderer *sdl.Renderer, tex *sdl.Texture, src *sdl.Rect, dst sdl.Rect, dim float64, alpha uint8) {
	if renderer == nil || tex == nil {
		return
	}
	dim = uiClamp01(dim)
	if dim < 0.01 {
		_ = renderer.Copy(tex, src, &dst)
		return
	}
	m := uint8(255 - dim*float64(255-homeLookDimMod) + 0.5)
	_ = tex.SetColorMod(m, m, m)
	_ = renderer.Copy(tex, src, &dst)
	_ = tex.SetColorMod(255, 255, 255)
	wash := homeLookWashColor()
	wash.A = uint8(dim*homeLookDimWash*float64(alpha)/255 + 0.5)
	if wash.A > 0 {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		setDrawColor(renderer, wash)
		_ = renderer.FillRect(&dst)
	}
}

// ── Settings ─────────────────────────────────────────────────────────────

// homeDimArtRow is Display & theme → Dim unselected art.
func homeDimArtRow() setRow {
	return setRow{
		ID: homeDimArtKey, Label: "Dim unselected art", Kind: setKindToggle,
		Detail: setStatic("Grey out every cover but the selected one, on Home and in the Games library"),
		Get:    homeDimArtOn,
		Set: func(c *Config, on bool) {
			homeTabsWrite(c, homeDimArtKey, boolString(on))
			setToast("Dim unselected art: " + setOnOff(on))
		},
	}
}

// listEdgeFadeRow is Display & theme → List edge fade.
func listEdgeFadeRow() setRow {
	return setRow{
		ID: listEdgeFadeKey, Label: "List edge fade", Kind: setKindToggle,
		Detail: setStatic("Long lists fade out at an edge when there is more to scroll to"),
		Get:    listEdgeFadeOn,
		Set: func(c *Config, on bool) {
			homeTabsWrite(c, listEdgeFadeKey, boolString(on))
			setToast("List edge fade: " + setOnOff(on))
		},
	}
}

var homeTabsPositionOptions = []setOption{
	{Value: homeTabsPositionTop, Label: "Top", Note: "The tabs sit in the top bar"},
	{Value: homeTabsPositionBottom, Label: "Bottom", Note: "The tabs sit in a bar over the button hints"},
}

// homeTabsPositionRow is Display & theme → Tabs position (Tabs Home only).
func homeTabsPositionRow() setRow {
	return setRow{
		ID: homeTabsPositionKey, Label: "Tabs position", Kind: setKindChoice,
		Detail:  setStatic("Show the Home tabs in the top bar or along the bottom"),
		Visible: homeTabsOnTabs,
		Options: func(*Config) []setOption { return homeTabsPositionOptions },
		Current: func(*Config) int {
			if homeTabsStripBottom() {
				return 1
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			bottom := value == homeTabsPositionBottom
			homeTabsSetPosition(c, bottom)
			setToast("Tabs position: " + homeTabsPositionLabel(bottom))
		},
	}
}

// homeLookRowsCache holds the three rows once built: the arranging sheet
// reads them on every frame it is open, and rows hold functions, not values.
var homeLookRowsCache []setRow

// homeLookRows are the three rows, in the order Display & theme and the
// arranging sheet list them (homeTabsEditPosition, Dim, Fade).
func homeLookRows() []setRow {
	if homeLookRowsCache == nil {
		// Grid selection ring is hometabs_tools.go's (homeTabsEditRing).
		homeLookRowsCache = []setRow{homeTabsPositionRow(), homeDimArtRow(), listEdgeFadeRow(), homeTabsGridRingRow()}
	}
	return homeLookRowsCache
}

// ── The arranging sheet's rows (hometabs_edit.go) ─────────────────────────

// The sheet's rows after the tabs, in order.
const (
	homeTabsEditRemember = iota
	homeTabsEditPosition
	homeTabsEditDim
	homeTabsEditFade
	homeTabsEditRing
	homeTabsEditLayout
	homeTabsEditExtraRows
)

// homeLookEditRowOf is the Settings row behind one of the sheet's look rows
// (which is homeTabsEditPosition, Dim or Fade), so the two cannot differ.
func homeLookEditRowOf(which int) (setRow, bool) {
	rows := homeLookRows()
	i := which - homeTabsEditPosition
	if i < 0 || i >= len(rows) {
		return setRow{}, false
	}
	return rows[i], true
}

// homeLookEditRow is the label and value of one of the sheet's look rows.
func homeLookEditRow(c *Config, which int) (label, value string) {
	r, ok := homeLookEditRowOf(which)
	if !ok {
		return "", ""
	}
	if r.Kind == setKindToggle {
		return r.Label, setOnOff(r.Get(c))
	}
	opts := r.Options(c)
	if i := r.Current(c); i >= 0 && i < len(opts) {
		return r.Label, opts[i].Label
	}
	return r.Label, ""
}

// homeLookEditChange is Left/Right on one of the sheet's look rows: each
// has two stops, so either way flips it (and toasts, as in Settings).
func homeLookEditChange(c *Config, which int) {
	r, ok := homeLookEditRowOf(which)
	if !ok {
		return
	}
	if r.Kind == setKindToggle {
		r.Set(c, !r.Get(c))
		return
	}
	opts := r.Options(c)
	if len(opts) == 0 {
		return
	}
	r.Choose(c, opts[(max(r.Current(c), 0)+1)%len(opts)].Value)
}
