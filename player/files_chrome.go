package main

// files_chrome.go — the chrome the Files viewers share: the text viewer, the
// image viewer and the PDF reader are full-screen pages, not cards, and each
// draws Home's glass page around its content the same way:
//
//   - a header with the file's glass badge, its name and a status line, and a
//     toolbar of glass chips on the right (each one a click target, and each
//     one also reachable from a pad button named in the footer);
//   - the content in between, which always scrolls inside a fixed viewport;
//   - Home's footer band with the controller hints as glass badges.
//
// The options sheet (filesMenu) is the pad's way to every toggle a chip offers.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// filesChip is one toolbar chip. On marks a toggle that is switched on, which
// is drawn with the accent wash.
type filesChip struct {
	ID    string
	Label string
	On    bool
	Rect  sdl.Rect
}

// filesChipFont is the chips' type: the footer's 16px hint size, so the
// toolbar reads as chrome and never as content.
func filesChipFont(config *Config) *ttf.Font {
	if f := FontForRole(config, TypeLabel); f != nil {
		return f
	}
	f, _ := getCachedFont(config, "small")
	return f
}

// filesDrawToolbar lays the chips out right to left, ending at right, and
// returns them with their rects plus the x where the leftmost chip starts.
func filesDrawToolbar(renderer *sdl.Renderer, config *Config, chips []filesChip, right, y, h int32) ([]filesChip, int32) {
	font := filesChipFont(config)
	if renderer == nil || font == nil {
		return chips, right
	}
	x := right
	for i := len(chips) - 1; i >= 0; i-- {
		tw, _ := measureText(font, chips[i].Label)
		w := tw + 28
		if w < h {
			w = h
		}
		x -= w
		rect := sdl.Rect{X: x, Y: y, W: w, H: h}
		chips[i].Rect = rect
		hovered := pointInRect(mouseX, mouseY, rect)
		tint := sdl.Color{}
		if chips[i].On {
			tint = WithAlpha(glassAccent(), 70)
		}
		drawGlassButton(renderer, config, rect, chips[i].Label, font, tint, glassState{Hovered: hovered})
		x -= 8
	}
	return chips, x + 8
}

// filesChipAt returns the ID of the chip under the pointer.
func filesChipAt(chips []filesChip, mx, my int32) string {
	for _, chip := range chips {
		if chip.Rect.W > 0 && pointInRect(mx, my, chip.Rect) {
			return chip.ID
		}
	}
	return ""
}

// filesDrawFooter paints Home's footer band with a viewer's own hints, laid out
// by the same planner the scene footer uses so the two cannot look different.
// It returns the band's top edge.
func filesDrawFooter(renderer *sdl.Renderer, config *Config, hints []FooterHint) int32 {
	band := sceneFooterBand()
	if renderer == nil || config == nil {
		return band.Y
	}
	drawGlassFooterBand(renderer, band)
	pad := scaleX(16, screenWidth)
	plan := planFooterHints(config, hints, band.W-2*pad)
	if plan.font == nil {
		return band.Y
	}
	x := band.X + band.W - pad - plan.width
	textY := band.Y + (band.H-plan.fontHeight)/2
	badgeY := band.Y + (band.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
	return band.Y
}

// filesDrawHeader paints a viewer's header: the glass badge with its glyph, the
// title and the status line, kept clear of the toolbar on the right. It
// returns the y under the header.
func filesDrawHeader(renderer *sdl.Renderer, config *Config, glyph, title, subtitle string, x, y, w int32, subtitleInk sdl.Color) int32 {
	header := UIPanelHeader(title, subtitle, glyph)
	header.X, header.Y, header.W = x, y, w
	header.SubtitleColor = subtitleInk
	return UIPageHeaderDraw(renderer, config, header)
}

// filesDrawNotice paints a one-line notice strip (a read-only file, a binary
// shown as hex, a large file shown in windows) and returns its height.
func filesDrawNotice(renderer *sdl.Renderer, config *Config, text string, x, y, w int32, warn bool) int32 {
	if renderer == nil || strings.TrimSpace(text) == "" {
		return 0
	}
	font := filesChipFont(config)
	if font == nil {
		return 0
	}
	h := int32(font.Height()) + 12
	ink := ColorTextSecondary()
	fill := glassA(12)
	if warn {
		ink = WarningInk()
		fill = WithAlpha(WarningInk(), 26)
	}
	fillRoundedRect(renderer, x, y, w, h, h/2, fill)
	strokeRoundedRect(renderer, x, y, w, h, h/2, 1, glassA(22))
	renderText(renderer, config, font, ellipsize(font, text, w-32), ink, x+16, y+6)
	return h
}

// --- Options sheet ----------------------------------------------------------

// filesMenuRow is one row of an options sheet. Value is the right-hand reading
// (On/Off, 20 px); a row with Adjust set also answers Left/Right.
type filesMenuRow struct {
	ID      string
	Label   string
	Value   string
	Adjust  bool
	Enabled bool
}

// filesMenu is a small modal list of options over a viewer.
type filesMenu struct {
	open   bool
	title  string
	rows   []filesMenuRow
	cursor int
	rects  []sdl.Rect
	card   sdl.Rect
}

// filesMenuInput is what one press did to an open menu.
type filesMenuInput struct {
	run    string
	adjust int
	closed bool
}

func filesMenuMove(m *filesMenu, delta int) {
	if m == nil || len(m.rows) == 0 {
		return
	}
	m.cursor = (m.cursor + delta + len(m.rows)) % len(m.rows)
	PlayNavSound()
}

// filesMenuController answers one pad press on an open menu.
func filesMenuController(m *filesMenu, button uint8) filesMenuInput {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		filesMenuMove(m, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		filesMenuMove(m, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return filesMenuAdjust(m, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return filesMenuAdjust(m, 1)
	case sdl.CONTROLLER_BUTTON_A:
		return filesMenuRun(m)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		m.open = false
		PlayBackSound()
		return filesMenuInput{closed: true}
	}
	return filesMenuInput{}
}

// filesMenuKey answers one key on an open menu.
func filesMenuKey(m *filesMenu, sym sdl.Keycode) filesMenuInput {
	switch sym {
	case sdl.K_UP:
		filesMenuMove(m, -1)
	case sdl.K_DOWN:
		filesMenuMove(m, 1)
	case sdl.K_LEFT:
		return filesMenuAdjust(m, -1)
	case sdl.K_RIGHT:
		return filesMenuAdjust(m, 1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		return filesMenuRun(m)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		m.open = false
		PlayBackSound()
		return filesMenuInput{closed: true}
	}
	return filesMenuInput{}
}

// filesMenuMouse answers a click: a row runs, the card swallows, outside
// closes.
func filesMenuMouse(m *filesMenu, mx, my int32) filesMenuInput {
	for i, r := range m.rects {
		if r.W > 0 && pointInRect(mx, my, r) && i < len(m.rows) {
			m.cursor = i
			return filesMenuRun(m)
		}
	}
	if m.card.W > 0 && pointInRect(mx, my, m.card) {
		return filesMenuInput{}
	}
	m.open = false
	PlayBackSound()
	return filesMenuInput{closed: true}
}

func filesMenuRun(m *filesMenu) filesMenuInput {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return filesMenuInput{}
	}
	row := m.rows[m.cursor]
	if !row.Enabled {
		showToast(row.Label+" is not available here", ToastInfo())
		return filesMenuInput{}
	}
	PlayActivateSound()
	return filesMenuInput{run: row.ID}
}

func filesMenuAdjust(m *filesMenu, delta int) filesMenuInput {
	if m.cursor < 0 || m.cursor >= len(m.rows) || !m.rows[m.cursor].Adjust || !m.rows[m.cursor].Enabled {
		return filesMenuInput{}
	}
	PlayNavSound()
	return filesMenuInput{run: m.rows[m.cursor].ID, adjust: delta}
}

// filesMenuRender draws the sheet centred over the viewer.
func filesMenuRender(renderer *sdl.Renderer, config *Config, m *filesMenu) {
	if renderer == nil || m == nil || !m.open || len(m.rows) == 0 {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	rowH := int32(font.Height()) + 18
	header := UIPanelHeader(m.title, "", "settings")
	headerH := UIPanelHeaderBand(config, header, 10)
	hintFont := filesChipFont(config)
	footerH := int32(36)
	panelW := int32(620)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + int32(len(m.rows))*rowH + footerH
	if maxH := screenHeight - 48; panelH > maxH {
		panelH = maxH
	}
	card := sdl.Rect{X: (screenWidth - panelW) / 2, Y: (screenHeight - panelH) / 2, W: panelW, H: panelH}
	m.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	m.rects = m.rects[:0]
	y := card.Y + headerH
	for i, row := range m.rows {
		rect := sdl.Rect{X: card.X + 12, Y: y, W: card.W - 24, H: rowH - 6}
		if rect.Y+rect.H > card.Y+card.H-footerH {
			break
		}
		m.rects = append(m.rects, rect)
		focused := i == m.cursor
		UIListRow(renderer, rect, 10, focused, pointInRect(mouseX, mouseY, rect))
		ink := ColorTextPrimary()
		if !row.Enabled {
			ink = ColorTextTertiary()
		}
		textY := rect.Y + (rect.H-int32(font.Height()))/2
		valueW := int32(0)
		if row.Value != "" {
			value := row.Value
			if row.Adjust {
				value = "‹ " + value + " ›"
			}
			valueW, _ = measureText(font, value)
			renderText(renderer, config, font, value, ColorTextSecondary(), rect.X+rect.W-16-valueW, textY)
		}
		renderText(renderer, config, font, ellipsize(font, row.Label, rect.W-valueW-48), ink, rect.X+16, textY)
		y += rowH
	}
	if hintFont != nil {
		hint := hintLine(FooterHint{"A", "Choose"}, FooterHint{"Left/Right", "Change"}, FooterHint{"B", "Close"})
		hw, _ := measureText(hintFont, hint)
		renderText(renderer, config, hintFont, hint, ColorTextTertiary(), card.X+(card.W-hw)/2, card.Y+card.H-footerH+10)
	}
}
