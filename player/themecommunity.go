package main

// themecommunity.go — the in-app Community theme library.
//
// The theme gallery on Settings → Appearance paints the palettes that ship with
// the app. This panel is the other half: the index the builder site publishes,
// browsable on the handheld, with one press installing an entry as a theme file
// in themes/ and applying it. It is a modal overlay like the community hub, so
// it works on every config without adding a scene to anyone's layout.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type themeCommunityPanelState struct {
	open   bool
	cursor int
	scroll int
}

var themeCommunityPanel themeCommunityPanelState

// themeCommunityRowRects is filled by the renderer so clicks map back to rows.
var themeCommunityRowRects []sdl.Rect

var themeCommunityPanelRect sdl.Rect

// openThemeCommunityPanel shows the library and starts (or reuses) the fetch.
func openThemeCommunityPanel(config *Config) {
	if config == nil {
		return
	}
	// Overlays never stack: the library replaces the other modals.
	toolsPanel.open = false
	qs.open = false
	communityPanel.open = false
	themeCommunityPanel.open = true
	themeCommunityPanel.cursor = 0
	themeCommunityPanel.scroll = 0
	themeCommunityRowRects = nil
	themeCommunityPanelRect = sdl.Rect{}
	startThemeCatalogLoad(false)
}

func closeThemeCommunityPanel() {
	themeCommunityPanel.open = false
	PlayBackSound()
}

func themeCommunityRows() []communityTheme {
	entries, _, _ := communityThemeEntries()
	return entries
}

// moveThemeCommunityCursor moves the focus by delta and wraps, reporting whether
// there was anywhere to move. It stays silent so the wheel can reuse it.
func moveThemeCommunityCursor(delta int) bool {
	rows := themeCommunityRows()
	if len(rows) == 0 {
		return false
	}
	// Wrap with modulo rather than resetting past either end: a multi-row jump
	// (the wheel moves three rows) then continues the list instead of snapping
	// back to its first row.
	cursor := (themeCommunityPanel.cursor + delta) % len(rows)
	if cursor < 0 {
		cursor += len(rows)
	}
	themeCommunityPanel.cursor = cursor
	return true
}

func themeCommunityMove(delta int) {
	if moveThemeCommunityCursor(delta) {
		PlayNavSound()
	}
}

// themeCommunityActivate installs the focused entry. A failure is reported in
// the toast and leaves the installed themes untouched.
func themeCommunityActivate(config *Config) {
	rows := themeCommunityRows()
	if themeCommunityPanel.cursor < 0 || themeCommunityPanel.cursor >= len(rows) {
		return
	}
	entry := rows[themeCommunityPanel.cursor]
	if err := installCommunityTheme(config, entry); err != nil {
		ShowToast("Couldn't install that theme: "+err.Error(), ToastKindWarning)
		return
	}
	PlayActivateSound()
}

func themeCommunityRefresh() {
	startThemeCatalogLoad(true)
	ShowToast("Refreshing community themes…", ToastKindInfo)
}

func handleThemeCommunityKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !themeCommunityPanel.open {
		return false
	}
	if virtualKeyboardActive {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		themeCommunityMove(-1)
	case sdl.K_DOWN:
		themeCommunityMove(1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		themeCommunityActivate(config)
	case sdl.K_r:
		themeCommunityRefresh()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeThemeCommunityPanel()
	}
	return true
}

func ctrlThemeCommunity(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !themeCommunityPanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		themeCommunityMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		themeCommunityMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		themeCommunityActivate(config)
	case sdl.CONTROLLER_BUTTON_Y:
		themeCommunityRefresh()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeThemeCommunityPanel()
	}
	return true
}

func handleThemeCommunityMouse(mx, my int32, config *Config) bool {
	if !themeCommunityPanel.open {
		return false
	}
	for i, r := range themeCommunityRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			themeCommunityPanel.cursor = i
			themeCommunityActivate(config)
			return true
		}
	}
	if themeCommunityPanelRect.W > 0 && !pointInRect(mx, my, themeCommunityPanelRect) {
		closeThemeCommunityPanel()
	}
	return true
}

// themeCommunitySwatches are the entry's own colours, so a card reads as the
// palette before it is installed. A malformed colour is simply not drawn.
func themeCommunitySwatches(entry communityTheme) []sdl.Color {
	values := []string{entry.Theme.Background, entry.Theme.Surface, entry.Theme.BorderFocus, entry.Theme.TextPrimary}
	out := make([]sdl.Color, 0, len(values))
	for _, value := range values {
		if c, ok := parseHexColor(value); ok {
			out = append(out, sdl.Color{R: c.R, G: c.G, B: c.B, A: 255})
		}
	}
	return out
}

func themeCommunityByline(entry communityTheme) string {
	author := strings.TrimSpace(entry.Author)
	if author == "" {
		author = "community"
	}
	byline := "by " + author
	if len(entry.Tags) > 0 {
		byline += " · " + joinTags(entry.Tags)
	}
	return byline
}

// joinTags keeps the byline to three tags: the row is one line, and the rest of
// the list would only be ellipsised away.
func joinTags(tags []string) string {
	kept := make([]string, 0, 3)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		kept = append(kept, tag)
		if len(kept) == 3 {
			break
		}
	}
	return strings.Join(kept, " / ")
}

func themeCommunityRowHint(entry communityTheme) string {
	// A shipped palette's id can never be installed, so saying "Installed" (or
	// offering the key) would promise something the install refuses.
	if isBuiltinThemeID(entry.ID) {
		return "Reserved id"
	}
	if _, ok := themePresets[entry.ID]; ok {
		return "Installed"
	}
	return "A install"
}

// renderThemeCommunityPanel draws the browser. Layout and hit rects come from
// the same measurements, so a click and the highlight cannot disagree.
func renderThemeCommunityPanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !themeCommunityPanel.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	const rowPad = int32(10)
	rowH := 2*rowPad + smallH + 2 + medH + 2 + smallH

	entries, problem, loading := communityThemeEntries()
	header := UIPanelHeader("Theme library", "Community themes from the generator gallery", "palette")
	headerH := UIPanelHeaderBand(config, header, 9)

	panelW := int32(880)
	if panelW > screenWidth-40 {
		panelW = screenWidth - 40
	}
	visible := len(entries)
	if visible == 0 {
		visible = 1
	}
	panelH := headerH + int32(visible)*rowH + 34 + 34
	if panelH > screenHeight-20 {
		panelH = screenHeight - 20
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	themeCommunityPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	const padX = OverlayPadX

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)

	rowW := panelW - 2*padX
	bandH := panelH - headerH - 68
	maxRows := int(bandH / rowH)
	if maxRows < 1 {
		maxRows = 1
	}
	cursor := themeCommunityPanel.cursor
	if cursor >= len(entries) {
		cursor = len(entries) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	scroll := themeCommunityPanel.scroll
	if cursor < scroll {
		scroll = cursor
	}
	if cursor >= scroll+maxRows {
		scroll = cursor - maxRows + 1
	}
	if scroll < 0 {
		scroll = 0
	}
	themeCommunityPanel.cursor, themeCommunityPanel.scroll = cursor, scroll

	if len(entries) == 0 {
		themeCommunityRowRects = themeCommunityRowRects[:0]
		message := "No community themes yet."
		if loading {
			message = "Fetching community themes…"
		} else if problem != "" {
			message = "Could not load the gallery: " + problem
		}
		renderText(renderer, config, font, ellipsize(font, message, rowW), ColorTextSecondary(), panelX+padX, panelY+headerH+12)
		lines := []string{
			"The index lives on the builder site, so this list needs Wi-Fi once.",
			"Your installed themes keep working offline.",
		}
		y := panelY + headerH + 12 + smallH + 8
		for _, line := range lines {
			renderText(renderer, config, font, ellipsize(font, line, rowW), ColorTextTertiary(), panelX+padX, y)
			y += smallH + 4
		}
	} else {
		focusColor := installHighlight()
		// The hit rects are reused frame to frame; rows scrolled out of view
		// keep an empty rect so a click cannot land on them.
		if cap(themeCommunityRowRects) < len(entries) {
			themeCommunityRowRects = make([]sdl.Rect, len(entries))
		}
		themeCommunityRowRects = themeCommunityRowRects[:len(entries)]
		for i := range themeCommunityRowRects {
			themeCommunityRowRects[i] = sdl.Rect{}
		}
		for i := scroll; i < len(entries) && i < scroll+maxRows; i++ {
			entry := entries[i]
			y := panelY + headerH + int32(i-scroll)*rowH
			r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
			themeCommunityRowRects[i] = r
			focused := i == cursor
			// The glass rows every list uses: a soft accent wash under the
			// focused one and the shared 3px ring on its edge.
			if focused {
				fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, WithAlpha(glassAccent(), 30))
			}
			drawGlassSurface(renderer, r, glassRowRadius, glassState{
				Focused: focused,
				Hovered: !focused && pointInRect(mouseX, mouseY, r),
				NoLift:  true,
			})
			labelY := r.Y + rowPad
			valueY := labelY + smallH + 2
			detailY := valueY + medH + 2
			for j, swatch := range themeCommunitySwatches(entry) {
				fillRoundedRect(renderer, r.X+16+int32(j)*18, labelY, 14, 14, 4, swatch)
			}
			textX := r.X + 16 + 4*18
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				name = entry.ID
			}
			renderText(renderer, config, font, ellipsize(font, name, r.W-160), ColorTextSecondary(), textX, labelY)
			renderText(renderer, config, medium, ellipsize(medium, themeCommunityByline(entry), r.W-160), ColorTextPrimary(), textX, valueY)
			detail := strings.TrimSpace(entry.Description)
			if detail == "" {
				detail = "Community theme"
			}
			renderText(renderer, config, font, ellipsize(font, detail, r.W-160), ColorTextTertiary(), textX, detailY)
			hint := themeCommunityRowHint(entry)
			hintColor := ColorTextSecondary()
			if focused {
				hintColor = focusColor
			}
			hw, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint, hintColor, r.X+r.W-int32(hw)-16, valueY)
		}
	}

	noteY := panelY + panelH - 62
	note := "Installs as a theme file in themes/ — built-in palettes are never replaced."
	renderText(renderer, config, font, ellipsize(font, note, rowW), ColorTextTertiary(), panelX+padX, noteY)

	footer := hintLine(FooterHint{"A", "Install"}, FooterHint{"Y", "Refresh"}, FooterHint{"B", "Close"})
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
