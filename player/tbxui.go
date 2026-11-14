package main

// tbxui.go — the frame the five tool screens share: Watch Party, Toolbox,
// Audit toolkit, Security & trust and Report a bug.
//
// They used to be overlays (a Watch Party modal, three Power tools pages and the
// Community & support card) stacked over whatever screen opened them. Each is a
// full-screen scene now, reached from the Tools hub, drawn in Home's glass and
// left with B / Esc through the scene history like every other screen.
//
// What lives here is the part they have in common, so the five screens cannot
// drift apart:
//
//   - the scene names, the in-memory scenes (tbxEnsureScenes) and the opener;
//   - the page frame: Home's backdrop, the shared page header, a clipped body
//     viewport between the status bar and the footer band, and the shared bars;
//   - a focus list (tbxNav) that walks rows and side-by-side controls with the
//     D-pad, keeps the focused one scrolled into view and maps clicks back;
//   - one details sheet (tbxSheet) with wrapped text, an optional QR code and a
//     row of buttons — every "what is this / what can I do" answer uses it;
//   - list rows, status pills, toggles and section headings in the glass recipe;
//   - a small UI-thread queue (tbxPost) so network and install goroutines never
//     touch screen state directly.
//
// Nothing here allocates per frame once a screen is laid out: focus lists reuse
// their slices, wrapped text is cached by width, and labels go through the
// shared text caches.

import (
	"context"
	"strings"
	"sync"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The five scenes. None is in a shipped config: tbxEnsureScenes adds them in
// memory at startup and on hot reload, like the Alarm scene.
const (
	partySceneName    = "WatchParty"
	tbxSceneName      = "Toolbox"
	auditSceneName    = "AuditToolkit"
	sectrustSceneName = "SecurityTrust"
	bugrepSceneName   = "ReportBug"
)

// tbxSceneNames is the list tbxEnsureScenes walks, in Tools-hub order.
var tbxSceneNames = [...]string{partySceneName, tbxSceneName, auditSceneName, sectrustSceneName, bugrepSceneName, rommCloudSceneName}

// tbxEnsureScenes gives each tool screen a scene. The edit is in memory and
// idempotent; the user's config file is never rewritten for it. The scenes have
// no Icon on purpose: the Start menu and the search list skip icon-less scenes,
// and these screens are reached from the Tools hub rather than listed twice.
func tbxEnsureScenes(config *Config) {
	if config == nil {
		return
	}
	for _, name := range tbxSceneNames {
		found := false
		for i := range config.Scenes {
			if config.Scenes[i].Name == name {
				found = true
				break
			}
		}
		if !found {
			config.Scenes = append(config.Scenes, SceneConfig{Name: name})
		}
	}
}

// tbxIsToolScene reports whether name is one of the five screens.
func tbxIsToolScene(name string) bool {
	if name == rommCloudSceneName {
		return true
	}
	for _, n := range tbxSceneNames {
		if n == name {
			return true
		}
	}
	return false
}

// tbxCurrentScene names the scene on screen ("" when there is none).
func tbxCurrentScene(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	return config.Scenes[currentSceneIndex].Name
}

// tbxOpenScene switches to one of the screens. Panels that could still be up
// (the old Power tools and Community cards) are put away first: a scene change
// must never leave a modal floating over the next screen.
func tbxOpenScene(config *Config, name string) bool {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return false
	}
	tbxEnsureScenes(config)
	idx := sceneIndexExact(config, name)
	if idx < 0 {
		return false
	}
	powerTools.open = false
	communityPanel.open = false
	qs.open = false
	if idx == currentSceneIndex {
		return true
	}
	changeSceneTo(config, idx)
	return true
}

// tbxDisplayName is what the header caption and the Start menu say for a
// scene name; "" means the name is not one of these screens.
func tbxDisplayName(name string) string {
	switch name {
	case partySceneName:
		return "Watch Party"
	case tbxSceneName:
		return "Toolbox"
	case auditSceneName:
		return "Audit toolkit"
	case sectrustSceneName:
		return "Security & trust"
	case bugrepSceneName:
		return "Report a bug"
	case rommCloudSceneName:
		return "RomM"
	}
	return ""
}

// ── Visits ────────────────────────────────────────────────────────────────

// tbxVisit notices a fresh visit to a screen. Every completed scene transition
// starts a new scene-work generation (scenetasks.go), so a context that differs
// from the remembered one means the user just arrived — including by B from a
// screen this one opened, which is when an install status must be re-read.
type tbxVisit struct {
	ctx context.Context
}

// begin reports true once per visit.
func (v *tbxVisit) begin() bool {
	ctx := sceneWorkContext()
	if v.ctx == ctx {
		return false
	}
	v.ctx = ctx
	return true
}

// ── UI-thread queue ──────────────────────────────────────────────────────

// tbxUIQueue carries work finished on a goroutine back to the main loop. Screen
// state is only ever written there; a network answer or an install result is
// posted as a closure and applied on the next frame.
var tbxUIQueue struct {
	mu  sync.Mutex
	fns []func()
}

// tbxPost queues fn for the main loop.
func tbxPost(fn func()) {
	if fn == nil {
		return
	}
	tbxUIQueue.mu.Lock()
	tbxUIQueue.fns = append(tbxUIQueue.fns, fn)
	tbxUIQueue.mu.Unlock()
}

// tbxDrainUI runs the queued closures. partyTick calls it every frame, so a
// result lands even while another screen is up.
func tbxDrainUI() {
	tbxUIQueue.mu.Lock()
	fns := tbxUIQueue.fns
	tbxUIQueue.fns = nil
	tbxUIQueue.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// ── Fonts and frame ──────────────────────────────────────────────────────

// tbxFonts are the four sizes the screens use: Home's 20px card title, a 17px
// body, a 15px caption and a 13px tag.
type tbxFonts struct {
	title, body, small, tiny     *ttf.Font
	titleH, bodyH, smallH, tinyH int32
}

// tbxLoadFonts opens the sizes through the shared font cache. ok is false only
// when not even the theme's small font loads, and the screen then draws nothing
// rather than text at random sizes.
func tbxLoadFonts(config *Config) (tbxFonts, bool) {
	f := tbxFonts{
		title: FontForRole(config, TypeBody),
		body:  FontForRole(config, TypeBody),
		small: FontForRole(config, TypeLabel),
		tiny:  FontForRole(config, TypeCaption),
	}
	if f.small == nil {
		f.small, _ = getCachedFont(config, "small")
	}
	if f.small == nil {
		return f, false
	}
	if f.body == nil {
		f.body = f.small
	}
	if f.title == nil {
		f.title = f.body
	}
	if f.tiny == nil {
		f.tiny = f.small
	}
	f.titleH = int32(f.title.Height())
	f.bodyH = int32(f.body.Height())
	f.smallH = int32(f.small.Height())
	f.tinyH = int32(f.tiny.Height())
	return f, true
}

// tbxFrame is a screen's measured frame: the content column, where the header
// sits and the body viewport between the header and the footer band. The
// numbers are the Tools hub's, so moving between the hub and a tool screen does
// not shift the page.
type tbxFrame struct {
	x, w    int32
	headerY int32
	bodyTop int32
	bodyBot int32
}

func (f tbxFrame) bodyRect() sdl.Rect {
	return sdl.Rect{X: f.x, Y: f.bodyTop, W: f.w, H: f.bodyBot - f.bodyTop}
}

// tbxMeasureFrame lays the frame out for header on the current canvas.
func tbxMeasureFrame(config *Config, header UIPageHeader) tbxFrame {
	margin := scaleX(homeSideMargin, screenWidth)
	if margin < 16 {
		margin = 16
	}
	f := tbxFrame{x: margin, w: screenWidth - 2*margin}
	f.headerY = headerBarHeight() + 12
	m := uiPageHeaderMetricsFor(config, header)
	f.bodyTop = f.headerY + m.Height + 16
	f.bodyBot = screenHeight - bottomBarHeight("") - 16
	if f.bodyBot < f.bodyTop+60 {
		f.bodyBot = f.bodyTop + 60
	}
	return f
}

// tbxBeginFrame paints Home's backdrop and the page header and returns the
// frame the body is drawn into.
func tbxBeginFrame(renderer *sdl.Renderer, config *Config, header UIPageHeader) tbxFrame {
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	f := tbxMeasureFrame(config, header)
	header.X, header.Y, header.W = f.x, f.headerY, f.w
	UIPageHeaderDraw(renderer, config, header)
	return f
}

// tbxEndFrame draws the shared status bar and footer, then the open sheet over
// both, and leaves the renderer in the state the next pass expects.
func tbxEndFrame(renderer *sdl.Renderer, config *Config) {
	renderer.SetClipRect(nil)
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	tbxRenderSheet(renderer, config)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// tbxHeader is the shared page header for one screen: the glyph well, the title,
// the subtitle and an optional status read-out opposite the title.
func tbxHeader(title, subtitle, glyph, status string, statusColor sdl.Color) UIPageHeader {
	h := UIPanelHeader(title, subtitle, glyph)
	h.Status = status
	h.StatusColor = statusColor
	return h
}

// tbxChromeFocused reports whether the top bar holds the focus. A screen then
// draws its own selection without the ring: two rings would claim one press.
func tbxChromeFocused() bool {
	return chromeTier(focusEngine.ElementIndex()) == 1
}

// ── Input ────────────────────────────────────────────────────────────────

// tbxAction is one input, whatever device produced it.
type tbxAction int

const (
	tbxActNone tbxAction = iota
	tbxActUp
	tbxActDown
	tbxActLeft
	tbxActRight
	tbxActConfirm
	tbxActBack
	tbxActX
	tbxActY
	tbxActPageUp
	tbxActPageDown
	tbxActHome
	tbxActEnd
)

// tbxKeyAction maps a key press. B and Backspace are Back on every screen, the
// same keys the other scenes take, so a keyboard user can always leave.
func tbxKeyAction(e *sdl.KeyboardEvent) tbxAction {
	if e == nil || e.Type != sdl.KEYDOWN {
		return tbxActNone
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return tbxActUp
	case sdl.K_DOWN:
		return tbxActDown
	case sdl.K_LEFT:
		return tbxActLeft
	case sdl.K_RIGHT:
		return tbxActRight
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		return tbxActConfirm
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		return tbxActBack
	case sdl.K_x, sdl.K_DELETE:
		return tbxActX
	case sdl.K_y:
		return tbxActY
	case sdl.K_PAGEUP:
		return tbxActPageUp
	case sdl.K_PAGEDOWN:
		return tbxActPageDown
	case sdl.K_HOME:
		return tbxActHome
	case sdl.K_END:
		return tbxActEnd
	}
	return tbxActNone
}

// tbxPadAction maps a controller press. The handheld's A/B swap is applied
// before events reach the scenes, so A is always confirm here.
func tbxPadAction(e *sdl.ControllerButtonEvent) tbxAction {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return tbxActNone
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return tbxActUp
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return tbxActDown
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return tbxActLeft
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return tbxActRight
	case sdl.CONTROLLER_BUTTON_A:
		return tbxActConfirm
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		return tbxActBack
	case sdl.CONTROLLER_BUTTON_X:
		return tbxActX
	case sdl.CONTROLLER_BUTTON_Y:
		return tbxActY
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return tbxActPageUp
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return tbxActPageDown
	}
	return tbxActNone
}

// tbxDispatch routes one action: the open sheet first, then the screen. It
// reports whether the action was used; an unused Up is how the top bar is
// reached (screenPadPress steps onto it), and an unused Back leaves the scene.
func tbxDispatch(config *Config, act tbxAction, screen func(config *Config, act tbxAction) bool) bool {
	if act == tbxActNone {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetAction(act)
		return true
	}
	if screen != nil && screen(config, act) {
		return true
	}
	if act == tbxActBack {
		goBackScene(config)
		return true
	}
	return false
}

// ── Focus list ───────────────────────────────────────────────────────────

// tbxItem is one focusable control. rect is in body coordinates: y=0 is the top
// of the body viewport before scrolling. row groups controls that sit side by
// side; Up/Down walk rows, Left/Right walk a row.
type tbxItem struct {
	id       string
	rect     sdl.Rect
	row      int
	disabled bool
}

// tbxNav is one screen's focus and scroll. Focus is kept by id, so a list that
// gains or loses a row above the focused one does not move the selection.
type tbxNav struct {
	focus  string
	items  []tbxItem
	scroll int32
	// top and viewH are the body viewport on screen, from the last frame.
	top   int32
	viewH int32
	// contentH is the laid-out height, for the scroll limit.
	contentH int32
	rows     int
}

// begin starts a layout pass for a viewport at top with height viewH.
func (n *tbxNav) begin(top, viewH int32) {
	n.items = n.items[:0]
	n.top = top
	n.viewH = viewH
	n.contentH = 0
	n.rows = 0
}

// add records a control laid out at rect (body coordinates) in row.
func (n *tbxNav) add(id string, rect sdl.Rect, row int, disabled bool) {
	n.items = append(n.items, tbxItem{id: id, rect: rect, row: row, disabled: disabled})
	if bottom := rect.Y + rect.H; bottom > n.contentH {
		n.contentH = bottom
	}
	if row+1 > n.rows {
		n.rows = row + 1
	}
}

// extend grows the content height for something drawn that is not focusable.
func (n *tbxNav) extend(bottom int32) {
	if bottom > n.contentH {
		n.contentH = bottom
	}
}

// end clamps focus and scroll after a layout pass.
func (n *tbxNav) end() {
	if n.index() < 0 && len(n.items) > 0 {
		n.focus = n.items[0].id
	}
	n.clampScroll()
}

func (n *tbxNav) maxScroll() int32 {
	m := n.contentH - n.viewH
	if m < 0 {
		return 0
	}
	return m
}

func (n *tbxNav) clampScroll() {
	if max := n.maxScroll(); n.scroll > max {
		n.scroll = max
	}
	if n.scroll < 0 {
		n.scroll = 0
	}
}

// index is the focused item's position, or -1.
func (n *tbxNav) index() int {
	for i := range n.items {
		if n.items[i].id == n.focus {
			return i
		}
	}
	return -1
}

// current returns the focused item.
func (n *tbxNav) current() (tbxItem, bool) {
	if i := n.index(); i >= 0 {
		return n.items[i], true
	}
	return tbxItem{}, false
}

// is reports whether id holds the focus.
func (n *tbxNav) is(id string) bool { return n.focus == id }

// setFocus moves the focus to id and scrolls it into view.
func (n *tbxNav) setFocus(id string) {
	n.focus = id
	n.reveal()
}

// moveRow steps the focus to the nearest control in the next row in dir (-1 up,
// +1 down). It reports false at the edge, so the caller can hand Up to the top
// bar.
func (n *tbxNav) moveRow(dir int) bool {
	cur, ok := n.current()
	if !ok {
		if len(n.items) == 0 {
			return false
		}
		n.focus = n.items[0].id
		n.reveal()
		return true
	}
	cx := cur.rect.X + cur.rect.W/2
	target := -1
	for row := cur.row + dir; row >= 0 && row < n.rows; row += dir {
		best, bestDist := -1, int32(1<<30)
		for i := range n.items {
			if n.items[i].row != row {
				continue
			}
			ix := n.items[i].rect.X + n.items[i].rect.W/2
			d := ix - cx
			if d < 0 {
				d = -d
			}
			if d < bestDist {
				best, bestDist = i, d
			}
		}
		if best >= 0 {
			target = best
			break
		}
	}
	if target < 0 {
		return false
	}
	n.focus = n.items[target].id
	n.reveal()
	return true
}

// moveCol steps the focus left (-1) or right (+1) within its row.
func (n *tbxNav) moveCol(dir int) bool {
	cur, ok := n.current()
	if !ok {
		return false
	}
	best := -1
	var bestX int32
	for i := range n.items {
		it := n.items[i]
		if it.row != cur.row || it.id == cur.id {
			continue
		}
		if dir > 0 && it.rect.X > cur.rect.X && (best < 0 || it.rect.X < bestX) {
			best, bestX = i, it.rect.X
		}
		if dir < 0 && it.rect.X < cur.rect.X && (best < 0 || it.rect.X > bestX) {
			best, bestX = i, it.rect.X
		}
	}
	if best < 0 {
		return false
	}
	n.focus = n.items[best].id
	n.reveal()
	return true
}

// page moves the focus by about one viewport.
func (n *tbxNav) page(dir int) bool {
	cur, ok := n.current()
	if !ok {
		return n.moveRow(dir)
	}
	targetY := cur.rect.Y + int32(dir)*n.viewH*3/4
	best, bestDist := -1, int32(1<<30)
	for i := range n.items {
		d := n.items[i].rect.Y - targetY
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	if best < 0 || n.items[best].id == cur.id {
		return false
	}
	n.focus = n.items[best].id
	n.reveal()
	return true
}

// first and last jump to the ends of the list.
func (n *tbxNav) first() {
	if len(n.items) > 0 {
		n.focus = n.items[0].id
		n.scroll = 0
	}
}

func (n *tbxNav) last() {
	if len(n.items) > 0 {
		n.focus = n.items[len(n.items)-1].id
		n.reveal()
	}
}

// reveal scrolls the focused control into the viewport, with a little air so a
// row never sits flush against the footer band.
func (n *tbxNav) reveal() {
	cur, ok := n.current()
	if !ok || n.viewH <= 0 {
		return
	}
	const air = int32(12)
	if cur.rect.Y-air < n.scroll {
		n.scroll = cur.rect.Y - air
	}
	if bottom := cur.rect.Y + cur.rect.H + air; bottom > n.scroll+n.viewH {
		n.scroll = bottom - n.viewH
	}
	if cur.row == 0 {
		n.scroll = 0
	}
	n.clampScroll()
}

// scrollBy moves the viewport without moving the focus (mouse wheel).
func (n *tbxNav) scrollBy(dy int32) {
	n.scroll += dy
	n.clampScroll()
}

// screenRect is where an item was drawn on the last frame.
func (n *tbxNav) screenRect(it tbxItem) sdl.Rect {
	r := it.rect
	r.Y += n.top - n.scroll
	return r
}

// hit maps a click to a control, honouring the viewport clip.
func (n *tbxNav) hit(mx, my int32) (tbxItem, bool) {
	if my < n.top || my >= n.top+n.viewH {
		return tbxItem{}, false
	}
	for _, it := range n.items {
		if pointInRect(mx, my, n.screenRect(it)) {
			return it, true
		}
	}
	return tbxItem{}, false
}

// focused reports whether id should draw its focus ring this frame.
func (n *tbxNav) focused(id string) bool {
	return n.focus == id && !tbxChromeFocused()
}

// tbxNavAction applies the movement actions every list screen shares. It
// reports false for actions it does not handle and for Up at the top (the top
// bar takes that one).
func tbxNavAction(n *tbxNav, act tbxAction) (handled bool) {
	switch act {
	case tbxActUp:
		if n.moveRow(-1) {
			PlayNavSound()
			return true
		}
		return false
	case tbxActDown:
		if n.moveRow(1) {
			PlayNavSound()
		}
		return true
	case tbxActLeft:
		if n.moveCol(-1) {
			PlayNavSound()
		}
		return true
	case tbxActRight:
		if n.moveCol(1) {
			PlayNavSound()
		}
		return true
	case tbxActPageUp:
		if n.page(-1) {
			PlayNavSound()
		}
		return true
	case tbxActPageDown:
		if n.page(1) {
			PlayNavSound()
		}
		return true
	case tbxActHome:
		n.first()
		return true
	case tbxActEnd:
		n.last()
		return true
	}
	return false
}

// ── Drawing ──────────────────────────────────────────────────────────────

// tbxTone colours a status pill or a dot.
type tbxTone int

const (
	tbxToneNeutral tbxTone = iota
	tbxToneGood
	tbxToneWarn
	tbxToneBad
	tbxToneAccent
)

// tbxToneColor is the fill colour of a tone.
func tbxToneColor(t tbxTone) sdl.Color {
	switch t {
	case tbxToneGood:
		return ColorToastSuccess
	case tbxToneWarn:
		return ColorToastWarn
	case tbxToneBad:
		return ColorToastError
	case tbxToneAccent:
		return glassAccent()
	}
	return glassA(60)
}

// tbxPillSize measures a pill for text.
func tbxPillSize(font *ttf.Font, text string) (int32, int32) {
	tw, th := measureText(font, text)
	return tw + 26, th + 8
}

// tbxDrawPill draws a status pill whose right edge is at right and whose centre
// is at cy, and returns its rect: a tinted glass capsule with a tone dot.
func tbxDrawPill(renderer *sdl.Renderer, config *Config, font *ttf.Font, right, cy int32, text string, tone tbxTone) sdl.Rect {
	if renderer == nil || font == nil || strings.TrimSpace(text) == "" {
		return sdl.Rect{}
	}
	w, h := tbxPillSize(font, text)
	r := sdl.Rect{X: right - w, Y: cy - h/2, W: w, H: h}
	c := tbxToneColor(tone)
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(c, 40))
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, WithAlpha(c, 110))
	fillCircle(renderer, r.X+11, r.Y+r.H/2, 3, c)
	_, th := measureText(font, text)
	renderText(renderer, config, font, text, ColorTextPrimary(), r.X+19, r.Y+(r.H-th)/2)
	return r
}

// tbxRowSpec is what one list row shows.
type tbxRowSpec struct {
	icon     string
	title    string
	subtitle string
	pill     string
	tone     tbxTone
	// note is a quiet right-aligned word left of the pill (a size, a count).
	note string
	dim  bool
}

// tbxRowHeight is the height of a two-line row with an icon badge.
func tbxRowHeight(fonts tbxFonts) int32 {
	h := fonts.titleH + fonts.smallH + 30
	if h < 68 {
		h = 68
	}
	return h
}

// tbxDrawRow draws a list row: the glass row, the icon badge, the title over
// its one-line explanation, and the status pill at the right edge.
func tbxDrawRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, row tbxRowSpec, focused bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	x := rect.X + 14
	if row.icon != "" {
		badge := rect.H - 24
		if badge > 44 {
			badge = 44
		}
		if badge < 28 {
			badge = 28
		}
		box := drawGlassBadge(renderer, sdl.Rect{X: x, Y: rect.Y + (rect.H-badge)/2, W: badge, H: badge})
		DrawHomeIcon(renderer, row.icon, box, focused)
		x += badge + 14
	}
	right := rect.X + rect.W - 16
	if row.pill != "" {
		p := tbxDrawPill(renderer, config, fonts.tiny, right, rect.Y+rect.H/2, row.pill, row.tone)
		right = p.X - 12
	}
	if row.note != "" {
		nw, nh := measureText(fonts.small, row.note)
		if nw < right-x-80 {
			renderText(renderer, config, fonts.small, row.note, ColorTextTertiary(), right-nw, rect.Y+(rect.H-nh)/2)
			right -= nw + 14
		}
	}
	textW := right - x
	if textW < 40 {
		textW = 40
	}
	titleColor := ColorTextPrimary()
	subColor := ColorTextMuted()
	if row.dim {
		titleColor = ColorTextTertiary()
		subColor = ColorTextTertiary()
	}
	block := fonts.titleH
	if row.subtitle != "" {
		block += 4 + fonts.smallH
	}
	y := rect.Y + (rect.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, row.title, textW), titleColor, x, y)
	if row.subtitle != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, row.subtitle, textW), subColor, x, y+fonts.titleH+4)
	}
}

// tbxSectionHeight is the height UISectionLabel needs, gap included.
const tbxSectionHeight = int32(40)

// tbxDrawSection draws a group heading with its glyph chip.
func tbxDrawSection(renderer *sdl.Renderer, config *Config, title, icon string, x, y, w int32) {
	var glyph func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color)
	if icon != "" {
		name := icon
		glyph = func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, name, box, false)
		}
	}
	UISectionLabel(renderer, config, title, x, y, w, glyph)
}

// tbxDrawButton draws a labelled glass button; primary is the accent pill.
func tbxDrawButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, label string, primary, focused, disabled bool) {
	variant := UIButtonSecondary
	if primary {
		variant = UIButtonPrimary
	}
	UIButton(renderer, config, rect, label, UIButtonStyle{Variant: variant, Font: font}, UIButtonState{Focused: focused, Disabled: disabled})
}

// tbxButtonWidth is a button's width for label: the text plus Home's padding.
func tbxButtonWidth(font *ttf.Font, label string, min int32) int32 {
	w, _ := measureText(font, label)
	w += 44
	if w < min {
		w = min
	}
	return w
}

// tbxDrawToggle draws an on/off switch with its right edge at right, centred on
// cy, and returns its rect.
func tbxDrawToggle(renderer *sdl.Renderer, right, cy int32, on bool) sdl.Rect {
	r := sdl.Rect{X: right - 52, Y: cy - 15, W: 52, H: 30}
	track := glassA(36)
	if on {
		track = glassAccent()
	}
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, track)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassA(40))
	knobX := r.X + 15
	if on {
		knobX = r.X + r.W - 15
	}
	fillCircle(renderer, knobX, cy, 11, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	return r
}

// tbxDrawScrollbar draws a thin scrollbar at the right of a viewport.
func tbxDrawScrollbar(renderer *sdl.Renderer, x, top, viewH, contentH, scroll int32) {
	if contentH <= viewH || viewH <= 0 {
		return
	}
	fillRoundedRect(renderer, x, top, 4, viewH, 2, glassA(24))
	thumb := viewH * viewH / contentH
	if thumb < 28 {
		thumb = 28
	}
	maxScroll := contentH - viewH
	y := top + (viewH-thumb)*scroll/maxScroll
	fillRoundedRect(renderer, x, y, 4, thumb, 2, WithAlpha(glassAccent(), 210))
}

// tbxClip sets the clip rect for a body viewport; tbxUnclip restores it.
func tbxClip(renderer *sdl.Renderer, r sdl.Rect) {
	renderer.SetClipRect(&r)
}

func tbxUnclip(renderer *sdl.Renderer) {
	renderer.SetClipRect(nil)
}

// tbxQRModules encodes text as a QR code for a sheet, or nil when it does not
// fit one.
func tbxQRModules(text string) [][]bool {
	if text == "" {
		return nil
	}
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil
	}
	return code.Bitmap()
}

// tbxDrawEmpty draws an empty or waiting state centred in rect.
func tbxDrawEmpty(renderer *sdl.Renderer, config *Config, rect sdl.Rect, icon, title, body string) {
	UIEmptyState(renderer, config, rect, "@"+icon, title, body)
}

// ── Text ─────────────────────────────────────────────────────────────────

// tbxWrapByMeasure wraps text at maxW with measure, at spaces where it can and
// mid-word where a word alone is wider than the line. Blank lines are kept as
// paragraph breaks. Pure, so the rules are testable without SDL.
func tbxWrapByMeasure(text string, maxW int32, measure func(string) int32) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		indent := ""
		trimmed := strings.TrimLeft(para, " ")
		if lead := len(para) - len(trimmed); lead > 0 {
			indent = para[:lead]
		}
		// A bullet's continuation lines keep the bullet's indent, so a wrapped
		// item still reads as one item.
		cont := indent
		if strings.HasPrefix(trimmed, "• ") {
			cont = indent + "  "
		}
		words := strings.Fields(trimmed)
		line := indent
		lineHasWord := false
		for _, word := range words {
			candidate := line + word
			if lineHasWord {
				candidate = line + " " + word
			}
			if measure(candidate) <= maxW {
				line = candidate
				lineHasWord = true
				continue
			}
			if lineHasWord {
				out = append(out, line)
				line = cont
				lineHasWord = false
			}
			// The word alone may still be too wide (a URL, a hash): cut it.
			runes := []rune(word)
			for len(runes) > 0 {
				n := len(runes)
				for n > 1 && measure(line+string(runes[:n])) > maxW {
					n--
				}
				if n == len(runes) {
					line += string(runes)
					lineHasWord = true
					break
				}
				out = append(out, line+string(runes[:n]))
				line = cont
				runes = runes[n:]
			}
		}
		if lineHasWord {
			out = append(out, line)
		}
	}
	return out
}

// tbxWrapCache remembers one wrapped text, so a screen re-wraps only when the
// text, the font or the width changes.
type tbxWrapCache struct {
	font  *ttf.Font
	width int32
	text  string
	lines []string
}

// get returns text wrapped for font at width.
func (c *tbxWrapCache) get(font *ttf.Font, text string, width int32) []string {
	if font == nil {
		return nil
	}
	if c.font == font && c.width == width && c.text == text && c.lines != nil {
		return c.lines
	}
	measure := func(s string) int32 {
		w, _, err := font.SizeUTF8(s)
		if err != nil {
			return 0
		}
		return int32(w)
	}
	c.font, c.width, c.text = font, width, text
	c.lines = tbxWrapByMeasure(text, width, measure)
	if c.lines == nil {
		c.lines = []string{}
	}
	return c.lines
}

// ── Sheet ────────────────────────────────────────────────────────────────

// tbxSheetButton is one button of a sheet. run is called on the main loop; the
// sheet closes first unless keepOpen is set, so run may open the next sheet.
type tbxSheetButton struct {
	label    string
	primary  bool
	keepOpen bool
	run      func()
}

// tbxSheetSpec is everything a sheet shows. body is plain text: blank lines
// separate paragraphs, a line starting with "## " is a small heading and a line
// starting with "• " is a bullet.
type tbxSheetSpec struct {
	// token names a sheet that a background task fills in later
	// (tbxUpdateSheet): an answer for a sheet that was closed is dropped.
	token    string
	title    string
	subtitle string
	icon     string
	body     string
	qr       [][]bool
	qrLabel  string
	buttons  []tbxSheetButton
	// tall sheets (command output, previews) use the whole screen height.
	tall bool
	// focus is the button that starts focused (default 0).
	focus int
}

// tbxSheetState is the open sheet. One exists at a time; it belongs to the
// scene that opened it and is neither drawn nor fed input on another scene.
type tbxSheetState struct {
	open  bool
	scene string
	spec  tbxSheetSpec
	focus int
	// scroll is in pixels; maxScroll and the rects come from the last frame.
	scroll    int32
	maxScroll int32
	card      sdl.Rect
	buttons   []sdl.Rect
	qrRect    sdl.Rect
	lineH     int32
	wrap      tbxWrapCache
}

var tbxSheet tbxSheetState

// tbxOpenSheet shows spec over the current screen.
func tbxOpenSheet(config *Config, spec tbxSheetSpec) {
	tbxSheet = tbxSheetState{
		open:  true,
		scene: tbxCurrentScene(config),
		spec:  spec,
		focus: spec.focus,
	}
	if tbxSheet.focus < 0 || tbxSheet.focus >= len(spec.buttons) {
		tbxSheet.focus = 0
	}
}

// tbxCloseSheet dismisses the sheet.
func tbxCloseSheet() {
	tbxSheet = tbxSheetState{}
}

// tbxUpdateSheet replaces the body (and, when given, the buttons) of the sheet
// opened with token. It reports false when that sheet is no longer up.
func tbxUpdateSheet(token, body string, buttons []tbxSheetButton) bool {
	if !tbxSheet.open || token == "" || tbxSheet.spec.token != token {
		return false
	}
	tbxSheet.spec.body = body
	tbxSheet.scroll = 0
	if buttons != nil {
		tbxSheet.spec.buttons = buttons
		tbxSheet.focus = len(buttons) - 1
	}
	return true
}

// tbxSheetVisible reports whether a sheet is up on the current screen. A sheet
// left behind by a scene change is dropped here instead of reappearing later.
func tbxSheetVisible(config *Config) bool {
	if !tbxSheet.open {
		return false
	}
	if tbxSheet.scene != tbxCurrentScene(config) {
		tbxCloseSheet()
		return false
	}
	return true
}

// tbxSheetActivate presses button i.
func tbxSheetActivate(i int) {
	if i < 0 || i >= len(tbxSheet.spec.buttons) {
		return
	}
	b := tbxSheet.spec.buttons[i]
	if !b.keepOpen {
		tbxCloseSheet()
	}
	PlayActivateSound()
	if b.run != nil {
		b.run()
	}
}

// tbxSheetAction applies one action to the open sheet.
func tbxSheetAction(act tbxAction) {
	n := len(tbxSheet.spec.buttons)
	step := tbxSheet.lineH * 3
	if step <= 0 {
		step = 60
	}
	switch act {
	case tbxActLeft:
		if tbxSheet.focus > 0 {
			tbxSheet.focus--
			PlayNavSound()
		}
	case tbxActRight:
		if tbxSheet.focus < n-1 {
			tbxSheet.focus++
			PlayNavSound()
		}
	case tbxActUp:
		tbxSheet.scroll -= step
	case tbxActDown:
		tbxSheet.scroll += step
	case tbxActPageUp:
		tbxSheet.scroll -= step * 4
	case tbxActPageDown:
		tbxSheet.scroll += step * 4
	case tbxActHome:
		tbxSheet.scroll = 0
	case tbxActEnd:
		tbxSheet.scroll = tbxSheet.maxScroll
	case tbxActY:
		// Y enlarges the sheet's QR code, the pad's way to what a tap on the
		// code does.
		if len(tbxSheet.spec.qr) > 0 {
			PlayActivateSound()
			qrZoomOpenCard(tbxSheet.spec.qr, tbxSheet.spec.title)
		}
		return
	case tbxActConfirm:
		if n == 0 {
			tbxCloseSheet()
			PlayBackSound()
			return
		}
		tbxSheetActivate(tbxSheet.focus)
		return
	case tbxActBack:
		tbxCloseSheet()
		PlayBackSound()
		return
	}
	if tbxSheet.scroll > tbxSheet.maxScroll {
		tbxSheet.scroll = tbxSheet.maxScroll
	}
	if tbxSheet.scroll < 0 {
		tbxSheet.scroll = 0
	}
}

// tbxSheetMouse handles a click while a sheet is up: a button presses, the QR
// code enlarges, anywhere outside the card closes it.
func tbxSheetMouse(mx, my int32) {
	for i, r := range tbxSheet.buttons {
		if r.W > 0 && pointInRect(mx, my, r) {
			tbxSheet.focus = i
			tbxSheetActivate(i)
			return
		}
	}
	if tbxSheet.qrRect.W > 0 && pointInRect(mx, my, tbxSheet.qrRect) {
		qrZoomOpenCard(tbxSheet.spec.qr, tbxSheet.spec.title)
		return
	}
	if tbxSheet.card.W > 0 && !pointInRect(mx, my, tbxSheet.card) {
		tbxCloseSheet()
		PlayBackSound()
	}
}

// tbxSheetHints is the footer while a sheet is up.
func tbxSheetHints() []FooterHint {
	hints := make([]FooterHint, 0, 4)
	if tbxSheet.maxScroll > 0 {
		hints = append(hints, FooterHint{Button: "Up/Down", Label: "Scroll"})
	}
	if n := len(tbxSheet.spec.buttons); n > 1 {
		hints = append(hints, FooterHint{Button: "Left/Right", Label: "Choose"})
	}
	if tbxSheet.focus >= 0 && tbxSheet.focus < len(tbxSheet.spec.buttons) {
		hints = append(hints, FooterHint{Button: "A", Label: tbxSheet.spec.buttons[tbxSheet.focus].label})
	}
	if len(tbxSheet.spec.qr) > 0 {
		hints = append(hints, FooterHint{Button: "Y", Label: "Enlarge"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Close"})
}

// tbxRenderSheet draws the open sheet: the scrim, the glass card, the shared
// header, the wrapped body (scrollable), the optional QR code and the buttons.
func tbxRenderSheet(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !tbxSheetVisible(config) {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	spec := tbxSheet.spec
	cardW := int32(860)
	if cardW > screenWidth-64 {
		cardW = screenWidth - 64
	}
	qrSize := int32(0)
	if len(spec.qr) > 0 {
		qrSize = 232
		if cardW-2*OverlayPadX-qrSize < 300 {
			qrSize = cardW - 2*OverlayPadX - 300
		}
		if qrSize < 120 {
			qrSize = 0
		}
	}
	header := UIPanelHeader(spec.title, spec.subtitle, spec.icon)
	headerH := UIPanelHeaderBand(config, header, 12)
	textW := cardW - 2*OverlayPadX
	if qrSize > 0 {
		textW -= qrSize + 24
	}
	// Wrapped 20px narrower than the column: bullets and their continuation
	// lines are drawn 18px in.
	lines := tbxSheet.wrap.get(fonts.body, spec.body, textW-20)
	lineH := fonts.bodyH + 5
	tbxSheet.lineH = lineH
	buttonsH := int32(0)
	if len(spec.buttons) > 0 {
		buttonsH = 46 + 24
	}
	bodyH := int32(len(lines)) * lineH
	if qrSize > 0 && bodyH < qrSize+fonts.smallH+12 {
		bodyH = qrSize + fonts.smallH + 12
	}
	maxCardH := screenHeight - 48
	cardH := headerH + bodyH + buttonsH + 28
	if spec.tall || cardH > maxCardH {
		cardH = maxCardH
	}
	if cardH < 200 {
		cardH = 200
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	tbxSheet.card = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	viewTop := card.Y + headerH
	viewH := card.H - headerH - buttonsH - 16
	if viewH < lineH {
		viewH = lineH
	}
	tbxSheet.maxScroll = bodyH - viewH
	if tbxSheet.maxScroll < 0 {
		tbxSheet.maxScroll = 0
	}
	if tbxSheet.scroll > tbxSheet.maxScroll {
		tbxSheet.scroll = tbxSheet.maxScroll
	}
	if tbxSheet.scroll < 0 {
		tbxSheet.scroll = 0
	}

	textX := card.X + OverlayPadX
	tbxClip(renderer, sdl.Rect{X: card.X + 1, Y: viewTop, W: card.W - 2, H: viewH})
	y := viewTop - tbxSheet.scroll
	for _, line := range lines {
		if y+lineH >= viewTop && y <= viewTop+viewH {
			tbxDrawSheetLine(renderer, config, fonts, line, textX, y, textW)
		}
		y += lineH
	}
	tbxSheet.qrRect = sdl.Rect{}
	if qrSize > 0 {
		qx := card.X + card.W - OverlayPadX - qrSize
		qy := viewTop + 4 - tbxSheet.scroll
		tile := sdl.Rect{X: qx, Y: qy, W: qrSize, H: qrSize}
		fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 14, sdl.Color{R: 255, G: 255, B: 255, A: 255})
		drawCompanionQR(renderer, insetRect(tile, 12), spec.qr)
		tbxSheet.qrRect = tile
		if spec.qrLabel != "" {
			lw, _ := measureText(fonts.small, spec.qrLabel)
			if lw > qrSize+20 {
				lw = qrSize + 20
			}
			renderText(renderer, config, fonts.small, ellipsize(fonts.small, spec.qrLabel, qrSize+20), ColorTextMuted(),
				qx+(qrSize-lw)/2, qy+qrSize+8)
		}
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, card.X+card.W-10, viewTop, viewH, bodyH, tbxSheet.scroll)

	// The buttons sit right-aligned along the bottom edge, primary last, the way
	// a dialog reads; the focused one carries Home's ring.
	if cap(tbxSheet.buttons) < len(spec.buttons) {
		tbxSheet.buttons = make([]sdl.Rect, len(spec.buttons))
	}
	tbxSheet.buttons = tbxSheet.buttons[:len(spec.buttons)]
	if len(spec.buttons) > 0 {
		bh := int32(46)
		by := card.Y + card.H - bh - 20
		x := card.X + card.W - OverlayPadX
		rects := tbxSheet.buttons
		for i := len(spec.buttons) - 1; i >= 0; i-- {
			w := tbxButtonWidth(fonts.body, spec.buttons[i].label, 120)
			if x-w < card.X+OverlayPadX {
				w = x - card.X - OverlayPadX
			}
			x -= w
			rects[i] = sdl.Rect{X: x, Y: by, W: w, H: bh}
			x -= 12
		}
		for i, b := range spec.buttons {
			tbxDrawButton(renderer, config, fonts.body, rects[i], b.label, b.primary, i == tbxSheet.focus, false)
		}
	}
}

// tbxDrawSheetLine draws one wrapped body line with the sheet's light markup.
func tbxDrawSheetLine(renderer *sdl.Renderer, config *Config, fonts tbxFonts, line string, x, y, w int32) {
	switch {
	case strings.HasPrefix(line, "## "):
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, strings.ToUpper(strings.TrimPrefix(line, "## ")), w),
			ColorTextTertiary(), x, y+4)
	case strings.HasPrefix(line, "• "):
		fillCircle(renderer, x+5, y+fonts.bodyH/2+1, 3, glassAccent())
		renderText(renderer, config, fonts.body, strings.TrimPrefix(line, "• "), ColorTextSecondary(), x+18, y)
	case strings.HasPrefix(line, "  "):
		// The continuation of a bullet keeps the bullet's indent.
		renderText(renderer, config, fonts.body, strings.TrimLeft(line, " "), ColorTextSecondary(), x+18, y)
	default:
		renderText(renderer, config, fonts.body, line, ColorTextSecondary(), x, y)
	}
}

// ── Dispatch hooks (main.go, extras.go) ──────────────────────────────────

// tbxSceneMouse routes a click on one of the tool screens. The sheet takes it
// first; then the screen's own controls.
func tbxSceneMouse(mx, my int32, config *Config) bool {
	name := tbxCurrentScene(config)
	if !tbxIsToolScene(name) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	switch name {
	case partySceneName:
		return partySceneClick(mx, my, config)
	case tbxSceneName:
		return tbxToolboxClick(mx, my, config)
	case auditSceneName:
		return auditSceneClick(mx, my, config)
	case sectrustSceneName:
		return sectrustSceneClick(mx, my, config)
	case bugrepSceneName:
		return bugrepSceneClick(mx, my, config)
	case rommCloudSceneName:
		return rommCloudClick(mx, my, config)
	}
	return false
}

// tbxSceneWheel scrolls the sheet or the screen under the mouse wheel.
func tbxSceneWheel(y int32, config *Config) bool {
	name := tbxCurrentScene(config)
	if !tbxIsToolScene(name) || y == 0 {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	if nav := tbxSceneNav(name); nav != nil {
		nav.scrollBy(-y * 48)
		return true
	}
	return false
}

// tbxSceneNav returns the focus list of a tool screen.
func tbxSceneNav(name string) *tbxNav {
	switch name {
	case partySceneName:
		return &partyScreen.nav
	case tbxSceneName:
		return &tbxScreen.nav
	case auditSceneName:
		return &auditScreen.nav
	case sectrustSceneName:
		return sectrustActiveNav()
	case bugrepSceneName:
		return &bugrepScreen.nav
	case rommCloudSceneName:
		return &rommCloud.nav
	}
	return nil
}

// tbxFooterHints is the footer for a tool screen: the open sheet's buttons, or
// the screen's own bindings. ok is false for any other scene.
func tbxFooterHints(config *Config, name string) ([]FooterHint, bool) {
	if !tbxIsToolScene(name) {
		return nil, false
	}
	if tbxSheetVisible(config) {
		return tbxSheetHints(), true
	}
	switch name {
	case partySceneName:
		return partySceneHints(), true
	case tbxSceneName:
		return tbxToolboxHints(), true
	case auditSceneName:
		return auditSceneHints(), true
	case sectrustSceneName:
		return sectrustSceneHints(), true
	case bugrepSceneName:
		return bugrepSceneHints(), true
	case rommCloudSceneName:
		return rommCloudHints(), true
	}
	return nil, false
}
