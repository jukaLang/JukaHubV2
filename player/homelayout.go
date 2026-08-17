package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type HomeAction string

const (
	HomeActionContinue  HomeAction = "continue"
	HomeActionMedia     HomeAction = "media"
	HomeActionFiles     HomeAction = "files"
	HomeActionPackages  HomeAction = "packages"
	HomeActionApps      HomeAction = "apps"
	HomeActionFavorites HomeAction = "favorites"
	HomeActionChat      HomeAction = "chat"
	HomeActionTools     HomeAction = "tools"
	HomeActionSettings  HomeAction = "settings"
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
	Greeting     string
	Subtitle     string
	Version      string
	StatusParts  []string // right-side header cluster, shared with scene headers
	Continue     HomeContinueState
	Pressed      HomeFocusID
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
		Focus:  NewHomeFocusController(),
		Items:  make(map[HomeFocusID]HomeItem, 9),
		Order: []HomeFocusID{
			HomeFocusContinue, HomeFocusMedia, HomeFocusFiles,
			HomeFocusPackages, HomeFocusApps, HomeFocusFavorites,
			HomeFocusChat, HomeFocusTools, HomeFocusSettings,
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

	card(HomeFocusContinue, "Continue", "Resume your latest activity", "continue", HomeActionContinue,
		sdl.Rect{X: 32, Y: 164, W: 480, H: 258})
	card(HomeFocusMedia, "Media", "Watch and listen", "media", HomeActionMedia,
		sdl.Rect{X: 536, Y: 164, W: 348, H: 121})
	card(HomeFocusFiles, "Files", "Browse storage", "files", HomeActionFiles,
		sdl.Rect{X: 900, Y: 164, W: 348, H: 121})
	card(HomeFocusPackages, "Packages", "Discover content", "packages", HomeActionPackages,
		sdl.Rect{X: 536, Y: 301, W: 348, H: 121})
	card(HomeFocusApps, "Apps", "Launch installed apps", "apps", HomeActionApps,
		sdl.Rect{X: 900, Y: 301, W: 348, H: 121})
	card(HomeFocusFavorites, "Favorites", "Saved content", "favorites", HomeActionFavorites,
		sdl.Rect{X: 32, Y: 454, W: 292, H: 158})
	card(HomeFocusChat, "Chat", "Messages and sharing", "chat", HomeActionChat,
		sdl.Rect{X: 340, Y: 454, W: 292, H: 158})
	card(HomeFocusTools, "Tools", "System utilities", "tools", HomeActionTools,
		sdl.Rect{X: 648, Y: 454, W: 292, H: 158})
	card(HomeFocusSettings, "Settings", "Personalize JukaHub", "settings", HomeActionSettings,
		sdl.Rect{X: 956, Y: 454, W: 292, H: 158})
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

func (h *HomeLayout) Render(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData) error {
	if h == nil || renderer == nil {
		return fmt.Errorf("home layout requires a renderer")
	}

	// The caller (renderHomeModern) owns the background; the layout only adds
	// its translucent backdrop blocks on top of it.
	h.drawBackdrop(renderer)
	h.drawHeader(renderer, fonts, data)
	h.drawGreeting(renderer, fonts, data)

	focused := h.Focus.Current()
	for _, id := range h.Order {
		item := h.Items[id]
		if id == HomeFocusContinue {
			h.drawContinueCard(renderer, fonts, item, id == focused, data.Pressed == id, data.Continue)
		} else {
			h.drawActionCard(renderer, fonts, item, id == focused, data.Pressed == id)
		}
	}

	h.drawFooter(renderer, fonts, data.ShowBackHint)
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
	status := strings.Join(data.StatusParts, "   ")
	drawText(renderer, fonts.Small, status, h.Width-scaleX(32, h.Width), scaleY(19, h.Height), ColorTextMuted(), textAlignRight)
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

func (h *HomeLayout) drawActionCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool) {
	if fonts.Card == nil {
		return
	}
	rect := item.Rect
	DrawHomeCardSurface(renderer, rect, focused, pressed)
	offsetY := int32(0)
	if pressed {
		offsetY = scaleY(2, h.Height)
	}

	// One consistent horizontal layout for every action card (including the
	// bottom row): 20px padding, a 56x56 icon tile on the left, 16px gap, then
	// the title/subtitle stack. The icon is drawn exactly once.
	pad := scaleX(20, h.Width)
	iconSize := min32(scaleX(56, h.Width), scaleY(56, h.Height))
	if maxIcon := rect.H - scaleY(12, h.Height); iconSize > maxIcon {
		iconSize = maxIcon
	}
	iconTile := sdl.Rect{
		X: rect.X + pad,
		Y: rect.Y + (rect.H-iconSize)/2 + offsetY,
		W: iconSize, H: iconSize,
	}
	glyph := DrawIconTile(renderer, iconTile, focused)
	DrawHomeIcon(renderer, item.Icon, glyph, focused)

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

func (h *HomeLayout) drawContinueCard(renderer *sdl.Renderer, fonts HomeFonts, item HomeItem, focused, pressed bool, state HomeContinueState) {
	if fonts.Small == nil {
		return
	}
	rect := item.Rect
	DrawHomeCardSurface(renderer, rect, focused, pressed)
	offsetY := int32(0)
	if pressed {
		offsetY = scaleY(2, h.Height)
	}

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

// homeTextCache holds the rasterized labels drawn by drawText. Entries are
// destroyed together with textureCache at shutdown; theme changes naturally
// create fresh keys because the color is part of the key.
var homeTextCache = map[homeTextKey]*sdl.Texture{}

func drawText(renderer *sdl.Renderer, font *ttf.Font, text string, x, y int32, color sdl.Color, alignment textAlignment) {
	if renderer == nil || font == nil || strings.TrimSpace(text) == "" {
		return
	}

	key := homeTextKey{
		font:  font,
		text:  text,
		color: uint32(color.R)<<24 | uint32(color.G)<<16 | uint32(color.B)<<8 | uint32(color.A),
	}
	var texture *sdl.Texture
	var tw, th int32
	if cached, ok := homeTextCache[key]; ok && cached != nil {
		texture = cached
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
		homeTextCache[key] = texture
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

// ellipsize shortens text so it fits within maxW pixels, appending an
// ellipsis when truncated.
func ellipsize(font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return text
	}
	w, _, _ := font.SizeUTF8(text)
	if int32(w) <= maxW {
		return text
	}
	runes := []rune(text)
	for i := range runes {
		w, _, _ := font.SizeUTF8(string(runes[:i+1]) + "…")
		if int32(w) > maxW {
			return string(runes[:i]) + "…"
		}
	}
	return text + "…"
}

func insetRect(rect sdl.Rect, inset int32) sdl.Rect {
	return sdl.Rect{
		X: rect.X + inset,
		Y: rect.Y + inset,
		W: max32(0, rect.W-inset*2),
		H: max32(0, rect.H-inset*2),
	}
}
