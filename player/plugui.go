package main

// plugui.go — the Plugins page.
//
// It used to be a read-only text log of "- name vX (author) [enabled]" lines
// behind a Refresh button, with no way to run anything or switch a plugin on.
// Now the folder is scanned as the page opens; every plugin is a row with its
// icon, name, description and an on/off switch; and the focused plugin's card
// on the right shows its details, a button per action from its manifest, and
// what the last run printed. A plugin that cannot run on this system (no sh on
// Windows, a broken manifest) says why instead of failing on the press.
//
// Pad: Up/Down choose a plugin, A or Right go to its actions, A runs one, X
// switches the plugin on or off, B goes back. Keys: arrows, Enter, Space
// (on/off), Esc/Backspace. Mouse and touch: tap a row, its switch or a button.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// plugSceneName is the config scene the page draws.
const plugSceneName = "Plugins"

// plugUIState is the page's focus and the last frame's geometry.
type plugUIState struct {
	visit     context.Context
	cursor    int
	name      string // the focused plugin, kept across rescans
	inActions bool
	action    int
	listTop   int32
	// The last frame's geometry, for the mouse.
	rowRects    []sdl.Rect
	switchRects []sdl.Rect
	actionRects []sdl.Rect
	list        sdl.Rect
	// Text built from the catalogue, rebuilt only when what it shows changes,
	// so drawing a frame builds no strings: the header's count, the focused
	// card's meta line and wrapped description, its last output and when.
	statusOn, statusTotal int
	status                string
	cardName, cardVersion string
	cardAuthor, cardDesc  string
	cardW                 int32
	cardMeta              string
	descLines             []string
	outName, outText      string
	outW                  int32
	outMax                int
	outLines              []string
	outAt                 time.Time
	outLabel              string
}

var plugUI plugUIState

// plugEnsureVisit rescans the folder when the page is (re)entered.
func plugEnsureVisit(c *Config) {
	ctx := sceneWorkContext()
	if ctx == plugUI.visit {
		return
	}
	plugUI.visit = ctx
	plugUI.inActions = false
	plugUI.action = 0
	plugUI.listTop = 0
	if c != nil {
		LoadPlugins(c)
	}
	plugSyncCursor()
}

// plugSyncCursor keeps the focus on the same plugin after a rescan reordered
// or shortened the list.
func plugSyncCursor() {
	entries := plugCatalogEntries()
	if plugUI.name != "" {
		for i, e := range entries {
			if e.Name == plugUI.name {
				plugUI.cursor = i
				return
			}
		}
	}
	if plugUI.cursor >= len(entries) {
		plugUI.cursor = len(entries) - 1
	}
	if plugUI.cursor < 0 {
		plugUI.cursor = 0
	}
	if plugUI.cursor < len(entries) {
		plugUI.name = entries[plugUI.cursor].Name
	}
}

// plugFocused is the focused plugin.
func plugFocused() (plugEntry, bool) {
	entries := plugCatalogEntries()
	if plugUI.cursor < 0 || plugUI.cursor >= len(entries) {
		return plugEntry{}, false
	}
	return entries[plugUI.cursor], true
}

// plugCanRun reports whether a plugin's actions can be pressed, and why not.
// The sentence is built with the scan (plugRunNote), not per call.
func plugCanRun(e plugEntry) (bool, string) {
	return e.RunNote == "", e.RunNote
}

// ── Actions ───────────────────────────────────────────────────────────────

// plugMoveCursor steps the focused plugin.
func plugMoveCursor(delta int) {
	entries := plugCatalogEntries()
	if len(entries) == 0 {
		return
	}
	next := plugUI.cursor + delta
	if next < 0 {
		next = 0
	}
	if next > len(entries)-1 {
		next = len(entries) - 1
	}
	if next == plugUI.cursor {
		return
	}
	plugUI.cursor = next
	plugUI.name = entries[next].Name
	plugUI.action = 0
	if ok, _ := plugCanRun(entries[next]); !ok {
		plugUI.inActions = false
	}
	PlayNavSound()
}

// plugUp is Up on the list: the plugin above, or from the first one the top
// bar, which sits above every screen's first control.
func plugUp() {
	if plugUI.cursor <= 0 {
		focusEngine.ClearFocus()
		focusChromeBar()
		return
	}
	plugMoveCursor(-1)
}

// plugToggle switches the focused plugin on or off.
func plugToggle(c *Config) {
	e, ok := plugFocused()
	if !ok || c == nil {
		return
	}
	on := !e.Enabled
	PlayToggleSound()
	plugSetEnabled(c, e.Name, on)
	plugSyncCursor()
	// Says what the switch changed and, switched on, runs the plugin's
	// onEnable action (plughooks.go).
	plugAfterToggle(c, e.Name, on)
	if !on {
		plugUI.inActions = false
	}
}

// plugEnterActions moves the focus onto the plugin's buttons.
func plugEnterActions() {
	e, ok := plugFocused()
	if !ok {
		return
	}
	if can, why := plugCanRun(e); !can {
		ShowToast(why, ToastKindInfo)
		return
	}
	plugUI.inActions = true
	if plugUI.action >= len(e.Actions) {
		plugUI.action = 0
	}
	PlayNavSound()
}

// plugRun runs the focused action.
func plugRun(c *Config) {
	e, ok := plugFocused()
	if !ok || c == nil {
		return
	}
	if can, why := plugCanRun(e); !can {
		ShowToast(why, ToastKindInfo)
		return
	}
	if plugUI.action < 0 || plugUI.action >= len(e.Actions) {
		return
	}
	info, loaded := GetPlugins()[e.Name]
	if !loaded || info == nil {
		ShowToast(e.Title+" is not loaded; switch it off and on again", ToastKindWarning)
		return
	}
	a := e.Actions[plugUI.action]
	if !plugDispatch(c, *info, a.ID, e.Title, snapshotVars(c)) {
		ShowToast(e.Title+" is still running", ToastKindInfo)
		return
	}
	// JukaHub's own actions play their own sound (or open a screen that does).
	if plugUIActionFor(*info, a.ID) == nil {
		PlayActivateSound()
	}
}

// plugMoveAction steps the focused button in the two-column grid.
func plugMoveAction(delta int) {
	e, ok := plugFocused()
	if !ok {
		return
	}
	next := plugUI.action + delta
	if next < 0 || next >= len(e.Actions) {
		return
	}
	plugUI.action = next
	PlayNavSound()
}

// plugBack is B: from the buttons back to the list, from the list off the page.
func plugBack(c *Config) {
	if plugUI.inActions {
		plugUI.inActions = false
		PlayBackSound()
		return
	}
	goBackScene(c)
}

// ── Input ─────────────────────────────────────────────────────────────────

func plugKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	plugEnsureVisit(c)
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if plugUI.inActions {
			plugMoveAction(-2)
		} else {
			plugUp()
		}
	case sdl.K_DOWN:
		if plugUI.inActions {
			plugMoveAction(2)
		} else {
			plugMoveCursor(1)
		}
	case sdl.K_LEFT:
		if plugUI.inActions {
			if plugUI.action%2 == 0 {
				plugBack(c)
			} else {
				plugMoveAction(-1)
			}
		}
	case sdl.K_RIGHT:
		if plugUI.inActions {
			plugMoveAction(1)
		} else {
			plugEnterActions()
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if plugUI.inActions {
			plugRun(c)
		} else {
			plugEnterActions()
		}
	case sdl.K_SPACE:
		plugToggle(c)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		plugBack(c)
	case sdl.K_PAGEUP:
		plugMoveCursor(-1)
	case sdl.K_PAGEDOWN:
		plugMoveCursor(1)
	case sdl.K_HOME:
		plugMoveCursor(-len(plugCatalogEntries()))
	case sdl.K_END:
		plugMoveCursor(len(plugCatalogEntries()))
	default:
		return false
	}
	return true
}

func plugPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	}
	plugEnsureVisit(c)
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if plugUI.inActions {
			plugMoveAction(-2)
		} else {
			plugUp()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if plugUI.inActions {
			plugMoveAction(2)
		} else {
			plugMoveCursor(1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if plugUI.inActions {
			if plugUI.action%2 == 0 {
				plugBack(c)
			} else {
				plugMoveAction(-1)
			}
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if plugUI.inActions {
			plugMoveAction(1)
		} else {
			plugEnterActions()
		}
	case sdl.CONTROLLER_BUTTON_A:
		if plugUI.inActions {
			plugRun(c)
		} else {
			plugEnterActions()
		}
	case sdl.CONTROLLER_BUTTON_X:
		plugToggle(c)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		plugBack(c)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		plugMoveCursor(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		plugMoveCursor(1)
	}
	return true
}

// plugMouse handles a click or a tap on the page.
func plugMouse(mx, my int32, c *Config) bool {
	plugEnsureVisit(c)
	for i, r := range plugUI.switchRects {
		if r.W > 0 && hitTouchTarget(r, mx, my) {
			plugMoveCursorTo(i)
			plugToggle(c)
			return true
		}
	}
	for i, r := range plugUI.actionRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			plugUI.action = i
			plugUI.inActions = true
			plugRun(c)
			return true
		}
	}
	for i, r := range plugUI.rowRects {
		if r.W > 0 && pointInRect(mx, my, r) && pointInRect(mx, my, plugUI.list) {
			plugMoveCursorTo(i)
			plugUI.inActions = false
			return true
		}
	}
	return true
}

// plugMoveCursorTo focuses plugin i (a tap).
func plugMoveCursorTo(i int) {
	entries := plugCatalogEntries()
	if i < 0 || i >= len(entries) || i == plugUI.cursor {
		return
	}
	plugUI.cursor = i
	plugUI.name = entries[i].Name
	plugUI.action = 0
	PlayNavSound()
}

// plugWheel moves the focus three plugins per notch.
func plugWheel(dy int32) {
	switch {
	case dy > 0:
		plugMoveCursor(-3)
	case dy < 0:
		plugMoveCursor(3)
	}
}

// plugHints is the page's footer.
func plugHints() []FooterHint {
	if plugUI.inActions {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Run"},
			{Button: "X", Label: "On/Off"}, {Button: "B", Label: "List"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Actions"},
		{Button: "X", Label: "On/Off"}, {Button: "B", Label: "Back"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// plugRender draws the page.
func plugRender(renderer *sdl.Renderer, c *Config) {
	drawAppBackdrop(renderer, c, screenWidth, screenHeight)
	plugEnsureVisit(c)
	title := FontForRole(c, TypeBody)
	small := FontForRole(c, TypeLabel)
	big := FontForRole(c, TypeHeading)
	body := FontForRole(c, TypeLabel)
	if title == nil || small == nil || big == nil || body == nil {
		renderStatusBar(renderer, c)
		renderFooter(renderer, c)
		return
	}
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + scaleY(8, screenHeight)
	bottom := screenHeight - bottomBarHeight(plugSceneName) - 16
	on, total := plugEnabledCount()
	header := UIPanelHeader("Plugins", "Switch one on and its buttons join the Start menu", "plugin")
	header.X, header.Y, header.W = margin, top, screenWidth-2*margin
	if total > 0 {
		if plugUI.status == "" || plugUI.statusOn != on || plugUI.statusTotal != total {
			plugUI.statusOn, plugUI.statusTotal = on, total
			plugUI.status = strconv.Itoa(on) + " of " + strconv.Itoa(total) + " on"
		}
		header.Status = plugUI.status
	}
	contentY := UIPageHeaderDraw(renderer, c, header) + 16
	if total == 0 {
		plugUI.rowRects, plugUI.switchRects, plugUI.actionRects = plugUI.rowRects[:0], plugUI.switchRects[:0], plugUI.actionRects[:0]
		UIEmptyState(renderer, c, sdl.Rect{X: margin, Y: contentY, W: screenWidth - 2*margin, H: bottom - contentY},
			"@plugin", "No plugins yet", "Copy a plugin folder (plugin.json and its script) into plugins/")
		renderStatusBar(renderer, c)
		renderFooter(renderer, c)
		return
	}
	listW := (screenWidth - 2*margin) * 44 / 100
	list := sdl.Rect{X: margin, Y: contentY, W: listW, H: bottom - contentY}
	card := sdl.Rect{X: margin + listW + 20, Y: contentY, W: screenWidth - margin - (margin + listW + 20), H: bottom - contentY}
	plugDrawList(renderer, title, small, list)
	plugDrawCard(renderer, c, title, small, big, body, card)
	renderStatusBar(renderer, c)
	renderFooter(renderer, c)
}

// plugDrawList draws the plugin rows.
func plugDrawList(renderer *sdl.Renderer, title, small *ttf.Font, list sdl.Rect) {
	entries := plugCatalogEntries()
	plugUI.list = list
	th, sh := int32(title.Height()), int32(small.Height())
	rowH := th + sh + 24
	if rowH < 64 {
		rowH = 64
	}
	const gap = int32(8)
	n := len(entries)
	if cap(plugUI.rowRects) < n {
		plugUI.rowRects = make([]sdl.Rect, n)
		plugUI.switchRects = make([]sdl.Rect, n)
	}
	plugUI.rowRects = plugUI.rowRects[:n]
	plugUI.switchRects = plugUI.switchRects[:n]
	// Keep the focused row in view.
	fy := int32(plugUI.cursor) * (rowH + gap)
	if fy < plugUI.listTop {
		plugUI.listTop = fy
	}
	if fy+rowH > plugUI.listTop+list.H {
		plugUI.listTop = fy + rowH - list.H
	}
	if plugUI.listTop < 0 {
		plugUI.listTop = 0
	}
	chrome := chromeTier(focusEngine.ElementIndex()) != 0
	clip := list
	_ = renderer.SetClipRect(&clip)
	for i, e := range entries {
		r := sdl.Rect{X: list.X, Y: list.Y + int32(i)*(rowH+gap) - plugUI.listTop, W: list.W, H: rowH}
		plugUI.rowRects[i] = r
		plugUI.switchRects[i] = sdl.Rect{}
		if r.Y+r.H < list.Y || r.Y > list.Y+list.H {
			continue
		}
		selected := i == plugUI.cursor
		focused := selected && !plugUI.inActions && !chrome
		hovered := !selected && pointInRect(mouseX, mouseY, r)
		switch {
		case focused:
			UIListRow(renderer, r, glassRowRadius, true, false)
		case selected:
			// The card on the right belongs to this row: keep it marked while
			// the buttons hold the focus.
			drawGlassSurface(renderer, r, glassRowRadius, glassState{Hovered: true, NoLift: true})
			fillRoundedRect(renderer, r.X+6, r.Y+14, 4, r.H-28, 2, glassAccent())
		default:
			drawGlassSurface(renderer, r, glassRowRadius, glassState{Hovered: hovered, NoLift: true})
		}
		badge := rowH - 26
		if badge > 40 {
			badge = 40
		}
		icon := e.Icon
		if icon == "" {
			icon = "plugin"
		}
		glyph := drawGlassBadge(renderer, sdl.Rect{X: r.X + 16, Y: r.Y + (r.H-badge)/2, W: badge, H: badge})
		DrawHomeIcon(renderer, icon, glyph, selected)
		sw, swh := setSwitchSize(r.H)
		sr := sdl.Rect{X: r.X + r.W - 16 - sw, Y: r.Y + (r.H-swh)/2, W: sw, H: swh}
		setSwitchDraw(renderer, sr, e.Enabled, false)
		plugUI.switchRects[i] = sr
		x := r.X + 16 + badge + 14
		avail := sr.X - 14 - x
		if avail < 40 {
			avail = 40
		}
		ty := r.Y + (r.H-th-sh-2)/2
		drawText(renderer, title, ellipsize(title, e.Title, avail), x, ty, ColorTextPrimary(), textAlignLeft)
		line, ink := e.Description, ColorTextSecondary()
		if e.Problem != "" {
			// RunNote leads with the problem ("Can't run here: …").
			line, ink = e.RunNote, WarningInk()
		}
		if line == "" {
			line = "No description"
		}
		drawText(renderer, small, ellipsize(small, line, avail), x, ty+th+2, ink, textAlignLeft)
	}
	_ = renderer.SetClipRect(nil)
}

// plugDrawCard draws the focused plugin's details, buttons and last output.
func plugDrawCard(renderer *sdl.Renderer, c *Config, title, small, big, body *ttf.Font, card sdl.Rect) {
	plugUI.actionRects = plugUI.actionRects[:0]
	e, ok := plugFocused()
	if !ok || card.W <= 0 || card.H <= 0 {
		return
	}
	UICard(renderer, card, DefaultUICardStyle())
	const pad = int32(22)
	x := card.X + pad
	w := card.W - 2*pad
	y := card.Y + 18
	drawText(renderer, big, ellipsize(big, e.Title, w), x, y, ColorTextPrimary(), textAlignLeft)
	y += int32(big.Height()) + 4
	// The meta line and the wrapped description are rebuilt only when the
	// focused plugin, its manifest or the card's width changes.
	if plugUI.cardName != e.Name || plugUI.cardVersion != e.Version || plugUI.cardAuthor != e.Author ||
		plugUI.cardDesc != e.Description || plugUI.cardW != w || plugUI.cardMeta == "" {
		plugUI.cardName, plugUI.cardVersion, plugUI.cardAuthor = e.Name, e.Version, e.Author
		plugUI.cardDesc, plugUI.cardW = e.Description, w
		meta := "Version " + setOr(e.Version, "unknown")
		if e.Author != "" {
			meta += " · " + e.Author
		}
		plugUI.cardMeta = meta + " · " + e.Name + "/"
		plugUI.descLines = wrapTextLines(body, setOr(e.Description, "No description in plugin.json."), w, 4)
	}
	drawText(renderer, small, ellipsize(small, plugUI.cardMeta, w), x, y, ColorTextTertiary(), textAlignLeft)
	y += int32(small.Height()) + 12

	bh := int32(body.Height()) + 2
	for _, line := range plugUI.descLines {
		drawText(renderer, body, line, x, y, ColorTextSecondary(), textAlignLeft)
		y += bh
	}
	y += 10
	// What the plugin adds to JukaHub itself (plugx.go): one line per theme,
	// ROM format, input profile or pack, and the entries that were skipped.
	if len(e.ExtLines) > 0 {
		UISectionLabel(renderer, c, "Adds", x, y, w, nil)
		y += int32(small.Height()) + 20
		for i, line := range e.ExtLines {
			if i >= 6 || y+int32(small.Height()) > card.Y+card.H-pad {
				break
			}
			ink := ColorTextSecondary()
			if strings.HasPrefix(line, "Skipped ") {
				ink = WarningInk()
			}
			drawText(renderer, small, ellipsize(small, line, w), x, y, ink, textAlignLeft)
			y += int32(small.Height()) + 4
		}
		y += 8
	}
	if can, why := plugCanRun(e); !can {
		ink := ColorTextTertiary()
		if e.Problem != "" {
			ink = WarningInk()
		}
		drawText(renderer, small, ellipsize(small, why, w), x, y, ink, textAlignLeft)
		y += int32(small.Height()) + 10
	}

	// One button per action, two to a row.
	running, output, at := plugRunStatus(e.Name)
	if len(e.Actions) > 0 {
		UISectionLabel(renderer, c, "Actions", x, y, w, nil)
		y += int32(small.Height()) + 20
		const gap = int32(10)
		bw := (w - gap) / 2
		bhh := int32(title.Height()) + 18
		if bhh < 44 {
			bhh = 44
		}
		can, _ := plugCanRun(e)
		chrome := chromeTier(focusEngine.ElementIndex()) != 0
		for i, a := range e.Actions {
			col, row := int32(i%2), int32(i/2)
			r := sdl.Rect{X: x + col*(bw+gap), Y: y + row*(bhh+gap), W: bw, H: bhh}
			if r.Y+r.H > card.Y+card.H-pad {
				break
			}
			label := a.Label
			if running == a.ID {
				label = "Running…"
			}
			focused := plugUI.inActions && i == plugUI.action && !chrome
			UIButton(renderer, c, r, label, UIButtonStyle{Font: small}, UIButtonState{
				Focused:  focused,
				Hovered:  pointInRect(mouseX, mouseY, r),
				Disabled: !can,
			})
			if can {
				plugUI.actionRects = append(plugUI.actionRects, r)
			} else {
				plugUI.actionRects = append(plugUI.actionRects, sdl.Rect{})
			}
		}
		rows := int32((len(e.Actions) + 1) / 2)
		y += rows*(bhh+gap) + 8
	}

	// What the last run printed.
	if output == "" || y+int32(small.Height())*2 > card.Y+card.H-pad {
		return
	}
	if plugUI.outLabel == "" || !plugUI.outAt.Equal(at) {
		plugUI.outAt = at
		plugUI.outLabel = "Last result"
		if !at.IsZero() {
			plugUI.outLabel += " · " + clockDigits(c, at)
		}
	}
	drawText(renderer, small, plugUI.outLabel, x, y, ColorTextTertiary(), textAlignLeft)
	y += int32(small.Height()) + 6
	box := sdl.Rect{X: x, Y: y, W: w, H: card.Y + card.H - pad - y}
	if box.H < bh+12 {
		return
	}
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 12, glassA(10))
	strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, 12, 1, glassA(22))
	maxLines := int((box.H - 16) / bh)
	if plugUI.outName != e.Name || plugUI.outText != output || plugUI.outW != box.W || plugUI.outMax != maxLines {
		plugUI.outName, plugUI.outText, plugUI.outW, plugUI.outMax = e.Name, output, box.W, maxLines
		plugUI.outLines = wrapTextLines(body, strings.TrimSpace(output), box.W-24, maxLines)
	}
	ly := box.Y + 8
	for _, line := range plugUI.outLines {
		drawText(renderer, body, line, box.X+12, ly, ColorTextPrimary(), textAlignLeft)
		ly += bh
	}
}

// ── Scene registration ────────────────────────────────────────────────────

type plugScene struct{ sceneBase }

func (plugScene) render(r *sdl.Renderer, c *Config) bool {
	plugRender(r, c)
	return true
}

func (plugScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return plugKey(e, c) }

func (plugScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return plugPad(e, c)
}

func init() { registerScene(plugScene{sceneBase{name: plugSceneName}}) }
