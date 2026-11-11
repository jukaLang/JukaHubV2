package main

// ──────────────────────────────────────────────────────────────────────
// Shared UI components
//
// Scenes own their layout and state; these helpers own the visual recipe so
// the same card, row, badge or progress bar looks identical everywhere. They
// are stateless draw helpers and depend only on the design tokens.
// ──────────────────────────────────────────────────────────────────────

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// UICardStyle is the surface recipe for a panel or card.
type UICardStyle struct {
	Fill        sdl.Color
	Border      sdl.Color
	Radius      int32
	BorderWidth int32
	Shadow      uint8 // 0 disables the soft drop shadow
}

// DefaultUICardStyle returns the standard card: Home's glass tile.
func DefaultUICardStyle() UICardStyle {
	return UICardStyle{
		Fill:        glassFill(),
		Border:      glassEdge(),
		Radius:      homeTileRadius,
		BorderWidth: 1,
	}
}

// UICard draws a card surface: optional soft shadow, rounded fill, hairline.
func UICard(renderer *sdl.Renderer, rect sdl.Rect, style UICardStyle) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if style.Radius <= 0 {
		style.Radius = RadiusMD
	}
	if style.BorderWidth <= 0 {
		style.BorderWidth = 1
	}
	if style.Shadow > 0 {
		fillRoundedRect(renderer, rect.X+2, rect.Y+3, rect.W, rect.H, style.Radius, ShadowFill(style.Shadow))
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, style.Radius, style.Fill)
	if style.Border.A > 0 {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, style.Radius, style.BorderWidth, style.Border)
	}
}

// UIButtonVariant selects one of the app's semantic button roles. A custom
// variant uses the fill and label colours supplied in the style instead.
type UIButtonVariant int

const (
	UIButtonSecondary UIButtonVariant = iota
	UIButtonPrimary
	UIButtonGhost
	UIButtonDanger
	UIButtonCustom
)

// UIButtonStyle is the visual recipe for a button. Zero values fall back to the
// variant defaults, so callers only set the parts they need to override.
type UIButtonStyle struct {
	Variant       UIButtonVariant
	Fill          sdl.Color
	HoverFill     sdl.Color
	FocusFill     sdl.Color
	Border        sdl.Color
	LabelColor    sdl.Color
	Font          *ttf.Font
	Radius        int32
	Pill          bool
	NoShadow      bool
	HideFocusRing bool
	Progress      float64 // 0..1 hold-to-confirm fill over the surface
	ProgressFill  sdl.Color
}

// UIButtonState carries the interaction flags for one button draw. Layout and
// hit-testing stay with the caller; this only controls the pixels.
type UIButtonState struct {
	Focused  bool
	Hovered  bool
	Pressed  bool
	Disabled bool
}

func uiButtonFill(variant UIButtonVariant) sdl.Color {
	switch variant {
	case UIButtonPrimary:
		return accentColor
	case UIButtonDanger:
		return ColorDanger
	case UIButtonGhost:
		return WithAlpha(ColorSurfaceAlt, 120)
	case UIButtonCustom:
		return sdl.Color{}
	default:
		return ColorSurfaceAlt
	}
}

func uiButtonHoverFill(variant UIButtonVariant, fill sdl.Color) sdl.Color {
	switch variant {
	case UIButtonPrimary, UIButtonDanger, UIButtonCustom:
		return lighten(fill, 6)
	case UIButtonGhost:
		return WithAlpha(ColorSurfaceRaised, 170)
	default:
		return ColorCardHover
	}
}

func uiButtonFocusFill(variant UIButtonVariant, fill sdl.Color) sdl.Color {
	switch variant {
	case UIButtonPrimary, UIButtonDanger, UIButtonCustom:
		return lighten(fill, 10)
	case UIButtonGhost:
		return WithAlpha(ColorSurfaceRaised, 200)
	default:
		return ColorCardFocus
	}
}

// UIButton draws the app-wide button recipe, in Home's language: a secondary
// or ghost button is a glass surface with a hairline, a primary button is the
// solid accent pill with dark type (the Continue card's "Open Media"), a danger
// button is the danger colour, and a custom style keeps the caller's colours.
// Every one takes Home's focus treatment — the bloom, the 3px lift and the ring
// on the lifted edge — and a centred label. It returns rect so scene code can
// record the hit target in the same expression.
func UIButton(renderer *sdl.Renderer, config *Config, rect sdl.Rect, label string, style UIButtonStyle, state UIButtonState) sdl.Rect {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return rect
	}

	radius := style.Radius
	if radius <= 0 {
		radius = glassButtonRadius(rect.H)
	}
	if style.Pill {
		radius = rect.H / 2
	}
	ring := state.Focused && !state.Disabled && !style.HideFocusRing
	glass := (style.Variant == UIButtonSecondary || style.Variant == UIButtonGhost) && style.Fill.A == 0

	labelColor := style.LabelColor
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if ring {
		drawGlassFocusGlow(renderer, rect)
	}
	if glass {
		st := glassState{
			Focused:  ring,
			Hovered:  state.Hovered || (state.Focused && style.HideFocusRing),
			Pressed:  state.Pressed,
			Disabled: state.Disabled,
		}
		if style.Variant == UIButtonGhost && !st.Focused && !st.Hovered && !st.Pressed {
			// A ghost button rests as its label on a whisper of glass.
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassA(6))
		} else {
			drawGlassSurface(renderer, rect, radius, st)
		}
		if style.Border.A > 0 && !st.Focused {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, style.Border)
		}
		if style.Progress > 0 {
			progressFill := style.ProgressFill
			if progressFill.A == 0 {
				progressFill = WithAlpha(glassAccent(), 90)
			}
			UIProgressBar(renderer, rect, style.Progress, sdl.Color{}, progressFill)
		}
		if labelColor.A == 0 {
			labelColor = ColorTextPrimary()
		}
		if state.Disabled {
			labelColor = WithAlpha(ColorTextTertiary(), 160)
		}
	} else {
		fill := style.Fill
		if fill.A == 0 {
			fill = uiButtonFill(style.Variant)
		}
		hoverFill := style.HoverFill
		if hoverFill.A == 0 {
			hoverFill = uiButtonHoverFill(style.Variant, fill)
		}
		focusFill := style.FocusFill
		if focusFill.A == 0 {
			focusFill = uiButtonFocusFill(style.Variant, fill)
		}
		border := style.Border
		if border.A == 0 {
			border = glassEdge()
		}
		if labelColor.A == 0 {
			labelColor = contrastText(fill)
		}
		if state.Disabled {
			fill = WithAlpha(fill, 90)
			border = WithAlpha(border, 90)
			labelColor = WithAlpha(ColorTextTertiary(), 160)
		} else if state.Pressed {
			fill = darken(fill, 16)
		} else if state.Focused {
			fill = focusFill
		} else if state.Hovered {
			fill = hoverFill
			border = glassEdgeHover()
		}
		surface, surfaceR := rect, radius
		if ring {
			surface, surfaceR = glassLifted(rect), radius+glassFocusLift
		}
		fillRoundedRect(renderer, surface.X, surface.Y, surface.W, surface.H, surfaceR, fill)
		if style.Progress > 0 {
			progressFill := style.ProgressFill
			if progressFill.A == 0 {
				progressFill = lighten(fill, 24)
			}
			UIProgressBar(renderer, rect, style.Progress, sdl.Color{}, progressFill)
		}
		if ring {
			drawGlassFocusRing(renderer, surface, surfaceR)
		} else {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, border)
		}
	}

	// Substitute before measuring: a label like "Screensaver · $timeout" is only
	// as wide as its value once the variable is in it. Ellipsizing the template
	// and then drawing the substituted value either ran the text past the button
	// or, when the template was the longer string, cut the "$name" in half so the
	// raw placeholder was what reached the screen.
	if config != nil {
		label = substituteVars(label, config.Variables.Custom)
	}
	if style.Font != nil && label != "" {
		text := label
		// Compact pills and icon buttons are sized from their label; only
		// trim labels on full-size buttons where a caller may pass a long
		// string.
		if maxW := rect.W - 2*SpaceXS; maxW > 0 && rect.W >= 60 {
			text = ellipsize(style.Font, text, maxW)
		}
		if tw, th, err := style.Font.SizeUTF8(text); err == nil {
			drawGlassText(renderer, config, style.Font, text, labelColor,
				rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
		}
	}
	return rect
}

// ── Icon button ──────────────────────────────────────────────────────────

// UIIconButtonStyle is the visual recipe for a chip that is only an icon: the
// notification bell, a close cross, a toolbar control. The caller keeps the
// geometry; this owns the surface, the focus treatment, the glyph box and the
// badge, which every hand-drawn icon button used to re-implement.
type UIIconButtonStyle struct {
	// Surface is the resting pill colour. OpenSurface replaces it while the
	// control's panel is open; the zero value keeps Surface.
	Surface     sdl.Color
	OpenSurface sdl.Color
	// Ring is the accent the focused/open outline is drawn in.
	Ring sdl.Color
	// Icon is the glyph ink.
	Icon sdl.Color
	// IconInset is the total padding the glyph gives up inside the chip.
	IconInset int32
	// Radius overrides the pill (0 = fully rounded).
	Radius int32
	// RingWidth is the focused outline's width (0 = 2) and RingOutset how far
	// outside the chip it sits (0 = 2).
	RingWidth  int32
	RingOutset int32
	// Glyph paints the icon inside box. tint is Icon, already resolved for the
	// disabled state.
	Glyph func(renderer *sdl.Renderer, box sdl.Rect, tint sdl.Color)
}

// UIIconButtonState is one icon button's interaction flags.
type UIIconButtonState struct {
	Focused  bool
	Hovered  bool
	Pressed  bool
	Open     bool
	Disabled bool
}

// UIIconButton is one icon chip draw: geometry, recipe, state and an optional
// unread badge.
type UIIconButton struct {
	Rect  sdl.Rect
	Style UIIconButtonStyle
	State UIIconButtonState
	// Badge is the count drawn on the chip's top-right corner. Empty draws
	// nothing; a nil BadgeFont leaves it undrawn because the pill must be
	// measured before it can be placed.
	Badge      string
	BadgeFont  *ttf.Font
	BadgeColor sdl.Color
	BadgeInk   sdl.Color
	// BadgeOffsetX/Y move the badge off the shared corner placement.
	BadgeOffsetX int32
	BadgeOffsetY int32
}

// uiIconButtonBadgeRect hangs a count on an icon chip's top-right corner. It
// is the shared placement the notification bell's badge already used, so a
// second icon button with a count cannot put the pill somewhere else.
func uiIconButtonBadgeRect(font *ttf.Font, chip sdl.Rect, label string, offX, offY int32) sdl.Rect {
	if font == nil || label == "" {
		return sdl.Rect{}
	}
	tw, th, err := font.SizeUTF8(label)
	if err != nil {
		return sdl.Rect{}
	}
	bh := int32(th) + 3
	bw := int32(tw) + SpaceXS*2
	if bw < bh {
		bw = bh
	}
	return sdl.Rect{
		X: chip.X + chip.W - bw + SpaceXS + offX,
		Y: chip.Y - bh/2 + offY,
		W: bw,
		H: bh,
	}
}

// UIIconButtonDraw paints an icon chip and returns its rect, so a caller can
// record the hit target in the same expression. The focus ring sits outside
// the chip (the bloom the header's labelled buttons wear), an open control
// wears the same accent on its own edge, and the glyph is drawn inside the
// inset box through Glyph — so the bell, a cross or a future toolbar chip all
// get one focus and badge recipe.
func UIIconButtonDraw(renderer *sdl.Renderer, b UIIconButton) sdl.Rect {
	rect := b.Rect
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return rect
	}
	radius := b.Style.Radius
	if radius <= 0 {
		radius = rect.H / 2
	}
	fill := b.Style.Surface
	if b.State.Disabled {
		fill = WithAlpha(fill, 110)
	} else if b.State.Pressed {
		fill = darken(fill, 14)
	} else if b.State.Hovered {
		fill = lighten(fill, 8)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, fill)

	ring := b.Style.Ring
	if ring.A == 0 {
		ring = accentColor
	}
	if b.State.Open {
		if open := b.Style.OpenSurface; open.A > 0 && !b.State.Disabled {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, open)
		}
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, ring)
	}
	if b.State.Focused && !b.State.Disabled {
		width := b.Style.RingWidth
		if width <= 0 {
			width = 2
		}
		outset := b.Style.RingOutset
		if outset <= 0 {
			outset = 2
		}
		strokeRoundedRect(renderer, rect.X-outset, rect.Y-outset, rect.W+2*outset, rect.H+2*outset, radius+outset, width, ring)
	}

	ink := b.Style.Icon
	if ink.A == 0 {
		ink = ColorTextPrimary()
	}
	if b.State.Disabled {
		ink = WithAlpha(ColorTextTertiary(), 160)
	}
	if b.Style.Glyph != nil {
		box := rect
		if b.Style.IconInset > 0 {
			box = insetRect(rect, b.Style.IconInset/2)
		}
		if box.W > 0 && box.H > 0 {
			b.Style.Glyph(renderer, box, ink)
		}
	}

	if b.Badge != "" {
		badge := uiIconButtonBadgeRect(b.BadgeFont, rect, b.Badge, b.BadgeOffsetX, b.BadgeOffsetY)
		if badge.W > 0 && badge.H > 0 {
			bg := b.BadgeColor
			if bg.A == 0 {
				bg = sdl.Color{R: 220, G: 60, B: 60, A: 240}
			}
			fg := b.BadgeInk
			if fg.A == 0 {
				fg = sdl.Color{R: 255, G: 255, B: 255, A: 255}
			}
			tw, th, _ := b.BadgeFont.SizeUTF8(b.Badge)
			fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, bg)
			drawText(renderer, b.BadgeFont, b.Badge, badge.X+(badge.W-int32(tw))/2, badge.Y+(badge.H-int32(th))/2, fg, textAlignLeft)
		}
	}
	return rect
}

// UIProgressBar draws a rounded progress bar. A transparent track skips the
// background, which is what a progress-in-place button wants.
func UIProgressBar(renderer *sdl.Renderer, rect sdl.Rect, fraction float64, track, fill sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if fraction < 0 {
		fraction = 0
	} else if fraction > 1 {
		fraction = 1
	}
	radius := rect.H / 2
	if track.A > 0 {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, track)
	}
	w := int32(math.Round(float64(rect.W) * fraction))
	if w > 0 {
		fillRoundedRect(renderer, rect.X, rect.Y, w, rect.H, radius, fill)
	}
}

// UIListRow draws one list row in Home's glass: a translucent row with a
// hairline; the hovered row a step brighter; the selected row brighter still,
// washed with the accent and outlined in the accent all the way round. The ring
// sits on the row's own edge rather than a lifted one, because rows sit a few
// pixels apart inside clipped scroll viewports, and it is the tiles' 3px: at
// 2px on a 296 PPI panel the cursor was a hairline at arm's length, and a
// marked or playing row's tint could out-shout it.
func UIListRow(renderer *sdl.Renderer, rect sdl.Rect, radius int32, selected, hovered bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if radius <= 0 {
		radius = glassRowRadius
	}
	if selected {
		// The wash goes under the glass and the ring over it, so the ring stays
		// the brightest edge on a dim handheld panel.
		accent := glassAccent()
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, WithAlpha(accent, 36))
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassFillFocus())
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassFocusRingWidth, accent)
		return
	}
	drawGlassSurface(renderer, rect, radius, glassState{Hovered: hovered, NoLift: true})
}

// UIEmptyState draws the standard "nothing here yet" block: an optional muted
// glyph, a title and one supporting line, centred inside rect.
func UIEmptyState(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body string) {
	uiStateBlock(renderer, config, rect, glyph, title, body, ColorTextTertiary())
}

// UIErrorState is the same block in the danger colour, for failures.
func UIErrorState(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body string) {
	uiStateBlock(renderer, config, rect, glyph, title, body, DangerInk())
}

// The empty state's type is a heading over a caption. Both lines used to
// resolve to the theme's 24px roles, so the title and its explanation printed
// at one size and read as two titles; and the explanation was one unclipped
// line, so a sentence wider than its panel ran out of it. The body is now a
// step down and wraps to two lines inside the block's side padding.
const (
	uiStateBodySize  = 18
	uiStateBodyLines = 2
	uiStateSidePad   = int32(24)
)

// uiStateBodyFont is the explanation's font: the caption size, falling back
// to the theme's caption role.
func uiStateBodyFont(config *Config) *ttf.Font {
	if config == nil {
		return nil
	}
	if font := getFontSized(config, "small", uiStateBodySize); font != nil {
		return font
	}
	return FontForRole(config, TypeCaption)
}

// uiStateTextWidth is the width the title and body lines may use inside a
// block of the given width.
func uiStateTextWidth(width int32) int32 {
	if w := width - 2*uiStateSidePad; w >= 120 {
		return w
	}
	return width
}

// uiStateBodyLinesFor wraps the explanation for a block of the given width (at
// most two lines, the second ellipsized when the sentence is longer).
func uiStateBodyLinesFor(config *Config, body string, width int32) []string {
	font := uiStateBodyFont(config)
	if font == nil || strings.TrimSpace(body) == "" || width <= 0 {
		return nil
	}
	return appTileLabelLines(font, body, uiStateTextWidth(width), uiStateBodyLines)
}

// uiStateBlockMetrics measures the vertical pieces of an empty/error state.
// Layout code uses the same metrics (via uiStateBlockHeight) to size the
// surface, so a centred block can never be taller than its background. The
// body is measured as one line here, where the width is not known; the draw
// wraps it to the width it is given.
func uiStateBlockMetrics(config *Config, glyph, title, body string) (glyphH, titleH, bodyH, gap int32) {
	titleFont := FontForRole(config, TypeHeading)
	if titleFont == nil {
		return 0, 0, 0, 0
	}
	bodyFont := uiStateBodyFont(config)
	glyphFont := FontForRole(config, TypeTitle)
	titleH = int32(titleFont.Height())
	if bodyFont != nil && body != "" {
		bodyH = int32(bodyFont.Height())
	}
	if strings.HasPrefix(glyph, "@") {
		// A drawn glyph needs a little more room than a text one, and reads as
		// an empty-state illustration instead of a marker. Every empty state
		// used to pass the literal "·", which painted a stray dot above the
		// title — visible on Live TV, Weather and the package list.
		glyphH = 36 + SpaceMD
	} else if glyphFont != nil && glyph != "" {
		glyphH = int32(glyphFont.Height()) + SpaceMD
	}
	if bodyH > 0 {
		gap = SpaceXS
	}
	return glyphH, titleH, bodyH, gap
}

// uiStateBlockHeight returns the exact height the glyph/title/body stack
// needs. Callers that draw the surface themselves (the notification dropdown)
// reserve this instead of guessing a row height.
func uiStateBlockHeight(config *Config, glyph, title, body string) int32 {
	glyphH, titleH, bodyH, gap := uiStateBlockMetrics(config, glyph, title, body)
	return glyphH + titleH + gap + bodyH
}

func uiStateBlock(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body string, tone sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	titleFont := FontForRole(config, TypeHeading)
	bodyFont := uiStateBodyFont(config)
	glyphFont := FontForRole(config, TypeTitle)
	if titleFont == nil {
		return
	}

	glyphH, titleH, bodyH, gap := uiStateBlockMetrics(config, glyph, title, body)
	lines := uiStateBodyLinesFor(config, body, rect.W)
	if bodyH > 0 && len(lines) > 1 {
		bodyH *= int32(len(lines))
	}
	stackH := glyphH + titleH + gap + bodyH
	y := rect.Y + (rect.H-stackH)/2
	if y < rect.Y {
		y = rect.Y
	}
	cx := rect.X + rect.W/2
	textW := uiStateTextWidth(rect.W)

	if glyphH > 0 {
		if strings.HasPrefix(glyph, "@") {
			uiStateGlyph(renderer, glyph[1:], cx, y+glyphH/2, 36, tone)
		} else {
			gw, _, _ := glyphFont.SizeUTF8(glyph)
			renderText(renderer, config, glyphFont, glyph, tone, cx-int32(gw)/2, y)
		}
		y += glyphH
	}
	title = ellipsize(titleFont, title, textW)
	tw, _, _ := titleFont.SizeUTF8(title)
	renderText(renderer, config, titleFont, title, ColorTextPrimary(), cx-int32(tw)/2, y)
	y += titleH + gap
	if bodyFont != nil && bodyH > 0 {
		lineH := int32(bodyFont.Height())
		for _, line := range lines {
			bw, _, _ := bodyFont.SizeUTF8(line)
			renderText(renderer, config, bodyFont, line, ColorTextSecondary(), cx-int32(bw)/2, y)
			y += lineH
		}
	}
}

// uiStateGlyph draws the illustration above an empty or error state. "@bell"
// keeps its own painter (the notification dropdown's mark); every other name is
// a home-icon glyph, so a list says "media" or "packages" at a glance instead of
// showing a punctuation mark.
func uiStateGlyph(renderer *sdl.Renderer, name string, cx, cy, size int32, tone sdl.Color) {
	if renderer == nil || size <= 0 {
		return
	}
	if name == "bell" {
		drawBellIcon(renderer, cx, cy, size, tone)
		return
	}
	DrawHomeIcon(renderer, name, sdl.Rect{X: cx - size/2, Y: cy - size/2, W: size, H: size}, false)
}

// UIGridColumns returns how many cells of at least minCellW fit across areaW
// with the given gap. It always returns at least 1 so layout maths never
// divides by zero, even in a zero-width element during startup.
func UIGridColumns(areaW, minCellW, gap int32) int {
	if areaW <= 0 || minCellW <= 0 {
		return 1
	}
	if gap < 0 {
		gap = 0
	}
	cols := int((areaW + gap) / (minCellW + gap))
	if cols < 1 {
		cols = 1
	}
	return cols
}

// UIGridCellWidth distributes areaW across cols cells and gaps. The result is
// never negative; a too-narrow area yields zero-width cells rather than a
// rectangle that wraps around.
func UIGridCellWidth(areaW int32, cols int, gap int32) int32 {
	if cols < 1 {
		cols = 1
	}
	if gap < 0 {
		gap = 0
	}
	w := (areaW - gap*int32(cols-1)) / int32(cols)
	if w < 0 {
		return 0
	}
	return w
}

// UIGridVisibleRows returns how many rows of at least rowH fit in areaH. It
// always returns at least 1, matching UIGridColumns.
func UIGridVisibleRows(areaH, rowH, gap int32) int {
	if areaH <= 0 || rowH <= 0 {
		return 1
	}
	if gap < 0 {
		gap = 0
	}
	rows := int((areaH + gap) / (rowH + gap))
	if rows < 1 {
		rows = 1
	}
	return rows
}

// ── Page header ───────────────────────────────────────────────────────────

// UIPageHeader is the bar a page or a full-screen panel opens with: an optional
// glyph well, the title, an optional subtitle under it and an optional status
// read-out at the right edge.
//
// The Tools page, the Tools overlay and the About screen each used to lay this
// bar out by hand, with their own offsets and their own idea of where the
// subtitle goes; they all build one of these now, and UIPageHeaderDraw is the
// only code that draws it.
type UIPageHeader struct {
	X, Y, W  int32
	Title    string
	Subtitle string
	// Status is the right-aligned read-out ("WiFi · 10.0.0.5", "Offline"). In a
	// centred header it becomes a third stacked line.
	Status      string
	StatusColor sdl.Color
	// SubtitleColor inks the subtitle line; the zero value means the dimmed
	// tertiary ink every page uses, so a panel that used to warn in amber keeps
	// warning in amber.
	SubtitleColor sdl.Color
	// TitleRole picks the title's font role ("big", "medium" or "small"; the
	// zero value means medium); TitleSize overrides its pixel size.
	TitleRole    string
	TitleSize    int
	SubtitleRole string // default "small"
	StatusRole   string // default "small"
	// Glyph draws the icon inside the tinted well. Nil means no well, and the
	// title starts at X.
	Glyph func(renderer *sdl.Renderer, box sdl.Rect, tint sdl.Color)
	// Centered stacks the three lines in the middle of the band: a brand block
	// (About) rather than a page bar.
	Centered bool
}

// UIPageHeaderMetrics is the band's measured geometry. A layout that reserves
// room for the header and the draw that fills it both call
// uiPageHeaderMetricsFor, so the two cannot disagree about how tall it is.
type UIPageHeaderMetrics struct {
	TitleH    int32
	LineH     int32
	WellBox   int32
	TitleY    int32
	SubtitleY int32
	StatusY   int32
	Height    int32
}

func uiHeaderRole(role, fallback string) string {
	if strings.TrimSpace(role) == "" {
		return fallback
	}
	return role
}

// uiPageHeaderFont resolves one line's font, falling back through the theme's
// own roles so a config that overrides only "small" still gets text.
func uiPageHeaderFont(config *Config, role string, size int) *ttf.Font {
	role = uiHeaderRole(role, "medium")
	if size > 0 {
		if font := getFontSized(config, role, size); font != nil {
			return font
		}
	}
	if font, _ := getCachedFont(config, role); font != nil {
		return font
	}
	if font, _ := getCachedFont(config, "medium"); font != nil {
		return font
	}
	font, _ := getCachedFont(config, "small")
	return font
}

// uiPageHeaderTitleFont is the title line's face. UX-2: a page title is at
// least a Heading, whatever the theme's "medium" role happens to be, and a
// larger role or size steps up to Title. Subtitles and status lines keep the
// theme's own roles through uiPageHeaderFont.
func uiPageHeaderTitleFont(config *Config, h UIPageHeader) *ttf.Font {
	if config == nil {
		return nil
	}
	large := h.TitleSize >= int(TypeTitle.Size()) ||
		(h.TitleSize <= 0 && strings.EqualFold(strings.TrimSpace(h.TitleRole), "large"))
	if large {
		return FontForRole(config, TypeTitle)
	}
	return FontForRole(config, TypeHeading)
}

func uiPageHeaderMetricsFor(config *Config, h UIPageHeader) UIPageHeaderMetrics {
	title := uiPageHeaderTitleFont(config, h)
	sub := uiPageHeaderFont(config, h.SubtitleRole, 0)
	status := uiPageHeaderFont(config, h.StatusRole, 0)
	if sub == nil {
		sub = status
	}
	if status == nil {
		status = sub
	}
	m := UIPageHeaderMetrics{TitleH: 28, LineH: 20}
	if title != nil {
		m.TitleH = int32(title.Height())
	}
	if sub != nil {
		m.LineH = int32(sub.Height())
	}
	if h.Glyph != nil {
		m.WellBox = m.TitleH + 10
	}

	if h.Centered {
		m.TitleY = 0
		m.SubtitleY = m.TitleY + m.TitleH + 6
		m.StatusY = m.SubtitleY + m.LineH + 4
		m.Height = m.TitleY + m.TitleH
		if strings.TrimSpace(h.Subtitle) != "" {
			m.Height = m.SubtitleY + m.LineH
		}
		if strings.TrimSpace(h.Status) != "" {
			m.Height = m.StatusY + m.LineH
		}
		return m
	}

	// Left-aligned: the title sits in the well's first line, the subtitle goes
	// directly under the well, and the status is centred against the well.
	m.TitleY = 5
	m.SubtitleY = m.WellBox + 2
	if m.WellBox == 0 {
		m.SubtitleY = m.TitleY + m.TitleH + 4
	}
	m.StatusY = m.TitleY
	if m.WellBox > 0 {
		m.StatusY = (m.WellBox - m.LineH) / 2
	}
	m.Height = m.TitleY + m.TitleH
	if m.WellBox > 0 {
		m.Height = m.WellBox
	}
	if strings.TrimSpace(h.Subtitle) != "" {
		m.Height = m.SubtitleY + m.LineH
	}
	return m
}

// UIPanelHeaderInset is how far a full-screen panel's shared header starts
// below its top edge. A panel that laid its title out by hand put it 18px down
// and the component puts its own title 5px inside the band, so a band drawn at
// 13 lands the title exactly where it used to be.
const UIPanelHeaderInset int32 = 13

// UIPanelHeaderBand is a panel's whole header band: the inset, the measured
// height of the bar that UIPanelHeaderInset positions, and the trailing gap
// before the body starts. The hand-rolled band was "18 + medH + 4 + smallH +
// trailing"; this is the same number expressed through the component, so a
// panel sizes itself from the same geometry that draws it.
func UIPanelHeaderBand(config *Config, h UIPageHeader, trailing int32) int32 {
	return UIPanelHeaderInset + uiPageHeaderMetricsFor(config, h).Height + trailing
}

// UIPanelHeader is the header a full-screen panel opens with: the same bar a
// page opens with — a tinted glyph well, the title, the subtitle and (via its
// Status field) any read-out opposite — built once so the band a panel
// reserves and the bar it draws are one measurement.
//
// Save Manager, the Games library, Curation, Downloads and the rest each used
// to lay this band out by hand, with their own offsets and their own idea of
// where the subtitle goes. They build one of these now and hand it to
// UIPanelHeaderBand and UIPageHeaderDraw, which are the only code that
// measures and paints it.
func UIPanelHeader(title, subtitle, glyph string) UIPageHeader {
	h := UIPageHeader{
		Title:    title,
		Subtitle: subtitle,
		// The title size every page header uses, so a panel and a page agree on
		// what a title looks like when a bug report shows both.
		TitleSize: menuTitleSize,
	}
	if glyph != "" {
		name := glyph
		h.Glyph = func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, name, box, false)
		}
	}
	return h
} // ── The shared overlay page ─────────────────────────────────────────────
//
// A full-screen overlay is a card over the dimmed scene, and every one of them
// is the same card: the same scrim, corner radius, inside margin and page
// header at the same inset. Those numbers used to be picked per panel (radius
// 16, 18 or 20; scrim 150, 180, 190 or 200; padding 16, 24, 26 or 28), which is
// what made a panel opened from Files look like a different app from one opened
// from Settings.

const (
	// OverlayCardRadius is every overlay card's corner radius.
	OverlayCardRadius = int32(20)
	// OverlayScrimAlpha is the dim behind a modal overlay. The dim is the
	// backdrop's navy (glassScrimColor), which reads lighter than black at the
	// same alpha, so it is a step stronger than the old black scrim's 180.
	OverlayScrimAlpha = uint8(200)
	// OverlayPadX is the card's inside margin: the header, the rows and the
	// footer all start this far from the card's edge.
	OverlayPadX = int32(24)
	// OverlayAccentInset is how far the accent hairline along a card's top edge
	// starts from its corners. It is inset from the radius so the line never
	// clips the curve.
	OverlayAccentInset = int32(18)
)

// drawOverlayScrim dims the scene behind a modal overlay.
func drawOverlayScrim(renderer *sdl.Renderer) {
	drawOverlayScrimFade(renderer, 1)
}

// drawOverlayScrimFade is drawOverlayScrim scaled by a panel's enter animation,
// for the one page that fades its dim in instead of painting it at full alpha.
func drawOverlayScrimFade(renderer *sdl.Renderer, progress float64) {
	if renderer == nil {
		return
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	// The dim is the backdrop's own navy, not black: a dialog sits in Home's
	// ambience instead of in a hole cut out of it.
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassScrimColor(progress))
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
}

// overlayCardFill is the surface every overlay card is painted with: Home's
// glass made opaque (glassSheetFill), so the dimmed screen underneath cannot
// ghost through the card's text.
func overlayCardFill() sdl.Color {
	return glassSheetFill()
}

// drawOverlayCard paints a panel card with the shared surface, hairline border
// and radius. A panel only decides its rect and what goes inside it.
func drawOverlayCard(renderer *sdl.Renderer, rect sdl.Rect) {
	drawGlassSheet(renderer, rect, overlayCardFill())
}

// drawOverlayCardAccent marks a card's top edge. Home's cards have no accent
// hairline — light catches their top edge instead — so this is the glass edge
// highlight: one soft line of the glass ink, inset from the corners. Every page
// still calls it, so the edge stays one recipe app-wide.
func drawOverlayCardAccent(renderer *sdl.Renderer, x, y, w int32) {
	if renderer == nil || w <= 2*OverlayAccentInset {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassA(22))
	renderer.FillRect(&sdl.Rect{X: x + OverlayAccentInset, Y: y + 1, W: w - 2*OverlayAccentInset, H: 1})
}

// overlayHeaderOnCard places a panel header at the shared inset and inside
// margin, so the band a panel reserves and the bar it draws are one placement.
func overlayHeaderOnCard(h UIPageHeader, card sdl.Rect) UIPageHeader {
	h.X = card.X + OverlayPadX
	h.Y = card.Y + UIPanelHeaderInset
	h.W = card.W - 2*OverlayPadX
	return h
}

// overlayPageHeader is overlayHeaderOnCard for the panels whose header is a
// plain title, subtitle and glyph.
func overlayPageHeader(card sdl.Rect, title, subtitle, glyph string) UIPageHeader {
	return overlayHeaderOnCard(UIPanelHeader(title, subtitle, glyph), card)
}

// UIPageHeaderDraw paints the band and returns the y where the page's content
// starts, so a caller never has to re-derive the header's height.
func UIPageHeaderDraw(renderer *sdl.Renderer, config *Config, h UIPageHeader) int32 {
	if renderer == nil {
		return h.Y
	}
	m := uiPageHeaderMetricsFor(config, h)
	title := uiPageHeaderTitleFont(config, h)
	sub := uiPageHeaderFont(config, h.SubtitleRole, 0)
	status := uiPageHeaderFont(config, h.StatusRole, 0)

	textX := h.X
	if h.Glyph != nil && m.WellBox > 0 {
		// Home's icon badge: a rounded glass square with a white glyph, the
		// same badge a Home tile carries in its corner.
		box := drawGlassBadge(renderer, sdl.Rect{X: h.X, Y: h.Y, W: m.WellBox, H: m.WellBox})
		h.Glyph(renderer, box, ColorTextPrimary())
		textX = h.X + m.WellBox + 14
	}

	if h.Centered {
		if title != nil {
			tw, _, _ := title.SizeUTF8(h.Title)
			renderText(renderer, config, title, h.Title, ColorTextPrimary(), h.X+(h.W-int32(tw))/2, h.Y+m.TitleY)
		}
		if sub != nil && strings.TrimSpace(h.Subtitle) != "" {
			sw, _, _ := sub.SizeUTF8(h.Subtitle)
			renderText(renderer, config, sub, h.Subtitle, ColorTextSecondary(), h.X+(h.W-int32(sw))/2, h.Y+m.SubtitleY)
		}
		if status != nil && strings.TrimSpace(h.Status) != "" {
			color := h.StatusColor
			if color.A == 0 {
				color = ColorTextTertiary()
			}
			sw, _, _ := status.SizeUTF8(h.Status)
			renderText(renderer, config, status, h.Status, color, h.X+(h.W-int32(sw))/2, h.Y+m.StatusY)
		}
		return h.Y + m.Height
	}

	if title != nil {
		avail := h.X + h.W - textX
		renderText(renderer, config, title, ellipsize(title, h.Title, avail), ColorTextPrimary(), textX, h.Y+m.TitleY)
	}
	if sub != nil && strings.TrimSpace(h.Subtitle) != "" {
		// Home's subline colour ("What do you want to do?").
		color := h.SubtitleColor
		if color.A == 0 {
			color = ColorTextMuted()
		}
		renderText(renderer, config, sub, ellipsize(sub, h.Subtitle, h.W), color, h.X, h.Y+m.SubtitleY)
	}
	if status != nil && strings.TrimSpace(h.Status) != "" {
		color := h.StatusColor
		if color.A == 0 {
			color = ColorTextTertiary()
		}
		if sw, _, err := status.SizeUTF8(h.Status); err == nil {
			renderText(renderer, config, status, h.Status, color, h.X+h.W-int32(sw), h.Y+m.StatusY)
		}
	}
	return h.Y + m.Height
}

// ── Category tile ────────────────────────────────────────────────────────

// UICategoryTile is the launcher's tile recipe: a card, a tinted icon well, a
// two-line name and the shared focus treatment. The Tools page and the Tools
// overlay draw through it (and the Apps grid uses the same colours), so a tile
// looks the same wherever it appears.
type UICategoryTile struct {
	Rect  sdl.Rect
	Label string
	// Surface picks the card recipe: a tile on the page's own backdrop, or a
	// tile standing on a raised panel (the Tools overlay), which needs the
	// panel's surface instead of the page's card colour.
	Surface UITileSurface
	// Glyph draws the icon inside the well; it receives the inset box and the
	// tile's tint (accent while focused).
	Glyph    func(renderer *sdl.Renderer, box sdl.Rect, tint sdl.Color)
	Focused  bool
	Hovered  bool
	Pressed  bool
	Disabled bool
	// HoverProgress is 0..1, so a hover ring can fade in.
	HoverProgress float64
	// Status draws the corner dot (running / needs attention). Zero alpha draws
	// nothing.
	Status sdl.Color
	// LabelSize caps the two-line name; 0 uses the tile default.
	LabelSize int
	Radius    int32
}

// UITileSurface is which surface a category tile is painted on.
type UITileSurface int

const (
	// UITileCard is the launcher's resting card: the theme's card colour, for a
	// tile on the page background.
	UITileCard UITileSurface = iota
	// UITileRaised is for a tile inside a raised panel, where the card colour is
	// the panel colour and the tile would otherwise disappear into it.
	UITileRaised
)

// uiCategoryTileLabelSize is a tile name's size: Home's 20px card title.
const uiCategoryTileLabelSize = 20

// UICategoryTileDraw paints one tile in Home's card anatomy: a glass card, the
// icon badge in the top-left corner and the name anchored bottom-left — never
// centred, the rule Home's cards are built on — with Home's bloom, lift and
// ring when focused. A tile too short to stack the badge over its name puts
// the name beside the badge instead, the way Home's wide Media card does.
// Hit-testing and layout stay with the caller; this only owns the pixels.
func UICategoryTileDraw(renderer *sdl.Renderer, config *Config, tile UICategoryTile) {
	w, h := tile.Rect.W, tile.Rect.H
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	radius := tile.Radius
	if radius <= 0 {
		radius = homeTileRadius
	}
	if radius > h/3 {
		radius = h / 3
	}

	ox, oy := tile.Rect.X, tile.Rect.Y
	if tile.Pressed {
		age := float64(sdl.GetTicks64() - pressStartTime)
		oy += int32(math.Round(float64(PressOffset) * uiPressProgress(config, age, float64(pressDuration))))
	}
	card := sdl.Rect{X: ox, Y: oy, W: w, H: h}
	if tile.Focused && !tile.Disabled {
		drawGlassFocusGlow(renderer, card)
	}
	drawGlassSurface(renderer, card, radius, glassState{
		Focused:  tile.Focused,
		Hovered:  tile.Hovered || tile.HoverProgress > 0.01,
		Pressed:  tile.Pressed,
		Disabled: tile.Disabled,
	})
	if tile.Status.A > 0 {
		fillCircle(renderer, ox+w-13, oy+13, 4, tile.Status)
	}

	// Badge and padding are shares of the tile, clamped to Home's numbers (a
	// 48px badge 20px in from the corner on a 152px card).
	pad := h / 7
	if pad < 10 {
		pad = 10
	}
	if pad > 20 {
		pad = 20
	}
	badge := h * 32 / 100
	if badge < 28 {
		badge = 28
	}
	if badge > homeIconSize {
		badge = homeIconSize
	}
	ink := ColorTextPrimary()
	if tile.Disabled {
		ink = ColorTextTertiary()
	}
	label := strings.TrimSpace(tile.Label)
	labelCap := tile.LabelSize
	if labelCap <= 0 {
		labelCap = uiCategoryTileLabelSize
	}

	badgeRect := sdl.Rect{X: ox + pad, Y: oy + pad, W: badge, H: badge}
	top := badgeRect.Y + badge + 4
	bottom := oy + h - pad
	font, lineH, lines := glassTileLabel(config, label, w-2*pad, bottom-top, labelCap)
	if label != "" && len(lines) == 0 {
		// Too short to stack: the badge sits on the left, centred, and the name
		// takes one line beside it.
		badgeRect.Y = oy + (h-badge)/2
		glyph := drawGlassBadge(renderer, badgeRect)
		if tile.Glyph != nil {
			tile.Glyph(renderer, glyph, ink)
		}
		lineFont := getFontSized(config, "small", labelCap)
		if lineFont == nil {
			return
		}
		textX := badgeRect.X + badge + pad/2 + 4
		text := ellipsize(lineFont, label, ox+w-pad-textX)
		renderText(renderer, config, lineFont, text, ink, textX, oy+(h-int32(lineFont.Height()))/2)
		return
	}
	glyph := drawGlassBadge(renderer, badgeRect)
	if tile.Glyph != nil {
		tile.Glyph(renderer, glyph, ink)
	}
	if font == nil || lineH <= 0 {
		return
	}
	y := bottom - int32(len(lines))*lineH
	for li, line := range lines {
		renderText(renderer, config, font, line, ink, ox+pad, y+int32(li)*lineH)
	}
}

// ── Section label ────────────────────────────────────────────────────────

// UISectionLabel draws a group heading: a tinted glyph chip, the uppercase
// title, and a hairline running to the panel edge. The Tools page and the Tools
// overlay both group their tiles this way.
func UISectionLabel(renderer *sdl.Renderer, config *Config, title string, x, y, w int32, glyph func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color)) {
	font := FontForRole(config, TypeLabel)
	if renderer == nil || font == nil {
		return
	}
	label := strings.ToUpper(strings.TrimSpace(title))
	_, th, _ := font.SizeUTF8(label)
	textY := y + 6
	textX := x
	if th > 0 && glyph != nil {
		// A small glass badge, the heading's version of a tile's icon badge.
		chip := int32(th) + 12
		chipY := y + (int32(th)+14-chip)/2
		fillRoundedRect(renderer, x, chipY, chip, chip, chip/4, glassA(16))
		strokeRoundedRect(renderer, x, chipY, chip, chip, chip/4, 1, glassA(26))
		glyph(renderer, insetRect(sdl.Rect{X: x, Y: chipY, W: chip, H: chip}, 4), ColorTextSecondary())
		textX += chip + 10
	}
	tw, _, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, ColorTextTertiary(), textX, textY)
	if lx := textX + int32(tw) + 12; lx < x+w {
		setDrawColor(renderer, glassHairline())
		renderer.FillRect(&sdl.Rect{X: lx, Y: textY + int32(th)/2, W: x + w - lx, H: 1})
	}
}

// UILoadingSkeleton draws a shimmering placeholder block. phase is a free
// running value in seconds; it only needs to change over time.
func UILoadingSkeleton(renderer *sdl.Renderer, rect sdl.Rect, phase float64) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	pulse := 0.5 + 0.5*math.Sin(phase*2*math.Pi)
	alpha := uint8(16 + 22*pulse)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusSM, WithAlpha(ColorTextTertiary(), alpha))
}
