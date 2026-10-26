package main

// browserbookmarks_panel.go — opening a page you already saved.
//
// The Save chip writes the list; this is the other half. It is the same
// overlay shape as the cookie picker: one row per saved page, D-pad to choose,
// A to open, and a remove that asks twice so a stray press cannot delete a
// page that took a D-pad session to save. X removes rather than a second A,
// because A is "open" everywhere else in the browser and one key meaning two
// things on the same row is how pages get deleted by accident.
//
// A row opens the page it names, which is the only reason to have the list.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// browserBookmarkPanelState is the overlay's cursor and its one armed remove.
// The arm is per URL: removing one page and then moving to another and pressing
// X there asks again rather than deleting that one too.
type browserBookmarkPanelState struct {
	open    bool
	cursor  int
	scroll  int
	armed   string
	armedAt time.Time
}

var browserBookmarkPanel browserBookmarkPanelState

var browserBookmarkRowHits []struct {
	index int
	rect  sdl.Rect
}

var browserBookmarkPanelRect sdl.Rect

// openBrowserBookmarkPanel opens the list, or says why there is nothing to
// show. An empty list is the normal first run, so it is an info toast rather
// than an error.
func openBrowserBookmarkPanel(config *Config) {
	marks := browserBookmarksList()
	if len(marks) == 0 {
		ShowToast("No saved pages yet — press Save on a page you want to keep", ToastKindInfo)
		return
	}
	browserBookmarkPanel = browserBookmarkPanelState{open: true}
	browserBookmarkRowHits = nil
	browserBookmarkPanelRect = sdl.Rect{}
	PlayActivateSound()
}

func closeBrowserBookmarkPanel() {
	if !browserBookmarkPanel.open {
		return
	}
	browserBookmarkPanel = browserBookmarkPanelState{}
	browserBookmarkRowHits = nil
	browserBookmarkPanelRect = sdl.Rect{}
	PlayBackSound()
}

func browserBookmarkPanelMove(delta int) {
	marks := browserBookmarksList()
	if len(marks) == 0 {
		closeBrowserBookmarkPanel()
		return
	}
	browserBookmarkPanel.cursor += delta
	if browserBookmarkPanel.cursor < 0 {
		browserBookmarkPanel.cursor = len(marks) - 1
	}
	if browserBookmarkPanel.cursor >= len(marks) {
		browserBookmarkPanel.cursor = 0
	}
	PlayNavSound()
}

// browserBookmarkPanelActivate opens the focused page. The panel closes first
// so the page it loaded is not drawn under an overlay that is about to be
// stale.
func browserBookmarkPanelActivate(config *Config) {
	marks := browserBookmarksList()
	if browserBookmarkPanel.cursor < 0 || browserBookmarkPanel.cursor >= len(marks) {
		return
	}
	mark := marks[browserBookmarkPanel.cursor]
	elem, ok := findBrowserElement(config)
	if !ok {
		showToast("The browser is not available on this screen", ToastError())
		return
	}
	closeBrowserBookmarkPanel()
	PlayActivateSound()
	loadTextBrowserURL(config, elem, mark.URL)
}

// browserBookmarkPanelRemove is X: the first press arms the row and says what
// would go, the second inside the same window removes it. Removing does not
// navigate — the page on screen is left exactly as it was, because a user who
// is tidying a list is not asking to lose their place in an article.
func browserBookmarkPanelRemove(config *Config) {
	marks := browserBookmarksList()
	if browserBookmarkPanel.cursor < 0 || browserBookmarkPanel.cursor >= len(marks) {
		return
	}
	mark := marks[browserBookmarkPanel.cursor]
	if browserBookmarkPanel.armed != mark.URL ||
		browserBookmarkPanel.armedAt.IsZero() ||
		time.Since(browserBookmarkPanel.armedAt) > browserForgetConfirmWindow {
		browserBookmarkPanel.armed = mark.URL
		browserBookmarkPanel.armedAt = time.Now()
		PlayNavSound()
		ShowToast("Press X again to remove \u201c"+mark.Title+"\u201d", ToastKindWarning)
		return
	}
	browserBookmarkPanel.armed = ""
	browserBookmarkPanel.armedAt = time.Time{}
	if !browserBookmarkRemove(mark.URL) {
		ShowToast("That page is no longer saved", ToastKindInfo)
		closeBrowserBookmarkPanel()
		return
	}
	PlayActivateSound()
	ShowToast("Removed \u201c"+mark.Title+"\u201d", ToastKindInfo)
	// A removal can empty the list; the renderer closes the panel when it has
	// nothing left to draw, which also covers this case.
}

func ctrlBrowserBookmarks(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		browserBookmarkPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		browserBookmarkPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		browserBookmarkPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		browserBookmarkPanelRemove(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeBrowserBookmarkPanel()
	}
	return true
}

func handleBrowserBookmarksKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !browserBookmarkPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		browserBookmarkPanelMove(-1)
	case sdl.K_DOWN:
		browserBookmarkPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		browserBookmarkPanelActivate(config)
	case sdl.K_x:
		browserBookmarkPanelRemove(config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		closeBrowserBookmarkPanel()
	}
	return true
}

func handleBrowserBookmarksMouse(mx, my int32, config *Config) bool {
	if !browserBookmarkPanel.open {
		return false
	}
	for _, hit := range browserBookmarkRowHits {
		if pointInRect(mx, my, hit.rect) {
			browserBookmarkPanel.cursor = hit.index
			browserBookmarkPanelActivate(config)
			return true
		}
	}
	if browserBookmarkPanelRect.W > 0 && pointInRect(mx, my, browserBookmarkPanelRect) {
		return true
	}
	closeBrowserBookmarkPanel()
	return true
}

// renderBrowserBookmarkPanel draws the list: title on the left, the page's
// host under it, and the saved date pushed right the way the cookie picker
// sizes its rows. The armed remove is spelled out in the subtitle line, which
// has the panel's full width and does not move as the cursor does — a second
// line inside a one-line row drew over the title the last time it was tried.
func renderBrowserBookmarkPanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !browserBookmarkPanel.open {
		return
	}
	marks := browserBookmarksList()
	if len(marks) == 0 {
		closeBrowserBookmarkPanel()
		return
	}
	if browserBookmarkPanel.cursor >= len(marks) {
		browserBookmarkPanel.cursor = len(marks) - 1
	}
	if browserBookmarkPanel.cursor < 0 {
		browserBookmarkPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	// Two lines per row: the title, then the host beneath it. A saved page is
	// recognised by its title, but the host is what tells two similarly named
	// pages apart (an issue and its pull request, say).
	rowH := smallH*2 + 12
	armedNow := browserBookmarkPanel.armed != "" &&
		time.Since(browserBookmarkPanel.armedAt) <= browserForgetConfirmWindow
	subtitle := fmt.Sprintf("%d page(s) · A opens · X removes · B closes", len(marks))
	if len(marks) == 1 {
		subtitle = "1 page · A opens · X removes · B closes"
	}
	subtitleInk := ColorTextSecondary()
	if armedNow {
		subtitle = "Press X again to remove this saved page"
		subtitleInk = ColorWarning
	}
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height. Trailing 9
	// gives back the exact band this panel has always reserved.
	header := UIPanelHeader("Saved pages", subtitle, "favorites")
	header.SubtitleColor = subtitleInk
	headerH := UIPanelHeaderBand(config, header, 9)
	footerH := 12 + smallH

	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 120 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	if int32(len(marks)) < maxRows {
		maxRows = int32(len(marks))
	}
	panelH := headerH + maxRows*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	browserBookmarkPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if browserBookmarkPanel.cursor < browserBookmarkPanel.scroll {
		browserBookmarkPanel.scroll = browserBookmarkPanel.cursor
	}
	if browserBookmarkPanel.cursor >= browserBookmarkPanel.scroll+int(maxRows) {
		browserBookmarkPanel.scroll = browserBookmarkPanel.cursor - int(maxRows) + 1
	}
	if browserBookmarkPanel.scroll > len(marks)-int(maxRows) {
		browserBookmarkPanel.scroll = len(marks) - int(maxRows)
	}
	if browserBookmarkPanel.scroll < 0 {
		browserBookmarkPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, 1, ColorBorderDefault)

	header.X = panelX + 22
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 44
	UIPageHeaderDraw(renderer, config, header)

	browserBookmarkRowHits = browserBookmarkRowHits[:0]
	y := panelY + headerH
	for i := browserBookmarkPanel.scroll; i < len(marks) && i < browserBookmarkPanel.scroll+int(maxRows); i++ {
		mark := marks[i]
		rect := sdl.Rect{X: panelX + 12, Y: y, W: panelW - 24, H: rowH - 4}
		if i == browserBookmarkPanel.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, sdl.Color{R: 52, G: 66, B: 110, A: 230})
		}

		// The date sits top-right, the way the cookie picker sizes its rows,
		// and the title's budget shrinks by its real width so the two can
		// never be drawn through each other.
		meta := ""
		if stamp, err := time.Parse(time.RFC3339, mark.Added); err == nil {
			meta = stamp.Local().Format("Jan 2, 2006")
		}
		budget := rect.W - 24
		if meta != "" {
			if w, _, err := font.SizeUTF8(meta); err == nil {
				if avail := rect.W - int32(w) - 34; avail < budget {
					budget = avail
				}
			}
		}
		if budget < 120 {
			budget = 120
		}
		renderText(renderer, config, font, ellipsize(font, mark.Title, budget), ColorTextPrimary(), rect.X+12, rect.Y+4)
		if meta != "" {
			if w, _, err := font.SizeUTF8(meta); err == nil {
				renderText(renderer, config, font, meta, ColorTextSecondary(),
					rect.X+rect.W-12-int32(w), rect.Y+4)
			}
		}
		// The host under the title: the page's own name tells two saved
		// pages apart, the host tells two sites apart.
		renderText(renderer, config, font, ellipsize(font, browserLinkHost(mark.URL), rect.W-24),
			ColorTextTertiary(), rect.X+12, rect.Y+4+smallH)

		browserBookmarkRowHits = append(browserBookmarkRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: i, rect: rect})
		y += rowH
	}

	hint := hintLine(FooterHint{"D-Pad / Mouse", "Choose"}, FooterHint{"A / Enter", "Open"})
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+22, panelY+panelH-footerH+8)
}
