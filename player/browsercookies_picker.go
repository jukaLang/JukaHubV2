package main

// browsercookies_picker.go — forgetting one site's cookies, not the whole jar.
//
// The Browser's Forget chip answers "am I still signed in somewhere?" for the
// whole card. That is the wrong size for the common case: a user who wants one
// site signed out, or one site's consent gate reset, otherwise loses every other
// login with it and retypes them on a D-pad.
//
// The picker lists the hosts the jar holds cookies for — the same names the
// strip under the page shows — with a count each and each site's own session
// start, plus one row that drops everything. It opens from the strip itself (tap it on the touchscreen) or from
// the toolbar's Sites chip (the controller path). A row asks twice, exactly like
// the Wi-Fi picker's forgotten network and the Forget chip: the first press arms
// and says what would go, the second drops it.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// browserCookieRow is one line in the picker: a site, how many cookies it
// holds, and when that site's own session began, or the row that forgets the
// lot. Since is zero for the forget-all row and for a site with no stamp: an
// unknown age is not drawn, rather than drawn as a date.
type browserCookieRow struct {
	Site      string
	Count     int
	Since     time.Time
	ForgetAll bool
}

// browserCookieRows is what the picker draws, in the order the strip names the
// sites (busiest first, ties by name) so the two never disagree about which site
// comes first. The "forget everything" row is last and carries the total, which
// is the count the Forget chip shows.
func browserCookieRows() []browserCookieRow {
	sites := browserCookieSites()
	if len(sites) == 0 {
		return nil
	}
	rows := make([]browserCookieRow, 0, len(sites)+1)
	total := 0
	for _, site := range sites {
		if site.Domain == "" || site.Count <= 0 {
			continue
		}
		rows = append(rows, browserCookieRow{Site: site.Domain, Count: site.Count, Since: site.Since})
		total += site.Count
	}
	if len(rows) == 0 {
		return nil
	}
	rows = append(rows, browserCookieRow{ForgetAll: true, Count: total})
	return rows
}

// browserCookiePanelState is the overlay's cursor and its one armed press. The
// arm is per site: arming youtube.com and then moving to github.com and pressing
// A drops github.com only after its own first press.
type browserCookiePanelState struct {
	open    bool
	cursor  int
	scroll  int
	armed   string
	armedAt time.Time
}

var browserCookiePanel browserCookiePanelState

var browserCookieRowHits []struct {
	index int
	rect  sdl.Rect
}

var browserCookiePanelRect sdl.Rect

// browserCookieStripHit is the strip's rectangle as the last frame drew it, so a
// tap on the line opens the picker. The renderer sets it every frame; a click
// outside it is the page's.
var browserCookieStripHit sdl.Rect

// openBrowserCookiePanel opens the picker, or says why there is nothing to list.
func openBrowserCookiePanel(config *Config) {
	rows := browserCookieRows()
	if len(rows) == 0 {
		ShowToast("No cookies are stored", ToastKindInfo)
		return
	}
	browserCookiePanel = browserCookiePanelState{open: true}
	browserCookieRowHits = nil
	browserCookiePanelRect = sdl.Rect{}
	PlayActivateSound()
}

// browserCookieStripTap opens the picker when a tap lands on the strip the last
// frame drew. It answers false for every other tap, which is the page's. The
// click handler calls this before the page's own link handling so the strip
// cannot be mistaken for text.
func browserCookieStripTap(mx, my int32, config *Config) bool {
	// The strip is drawn a font-height tall; the finger that opens it is not.
	// The tap test grows the rect the same way the toolbar chips do.
	if browserCookieStripHit.W <= 0 || !pointInRect(mx, my, textBrowserTouchTarget(browserCookieStripHit)) {
		return false
	}
	openBrowserCookiePanel(config)
	return true
}

func closeBrowserCookiePanel() {
	if !browserCookiePanel.open {
		return
	}
	browserCookiePanel = browserCookiePanelState{}
	browserCookieRowHits = nil
	browserCookiePanelRect = sdl.Rect{}
	PlayBackSound()
}

func browserCookiePanelMove(delta int) {
	rows := browserCookieRows()
	if len(rows) == 0 {
		closeBrowserCookiePanel()
		return
	}
	browserCookiePanel.cursor += delta
	if browserCookiePanel.cursor < 0 {
		browserCookiePanel.cursor = len(rows) - 1
	}
	if browserCookiePanel.cursor >= len(rows) {
		browserCookiePanel.cursor = 0
	}
	PlayNavSound()
}

// browserCookieRowDropped is the count one row would drop, for the arm message.
func browserCookieRowDropped(row browserCookieRow) string {
	if row.ForgetAll {
		return browserCookieCountLabel(row.Count)
	}
	return browserCookieCountLabel(row.Count) + " from " + row.Site
}

// browserCookiePanelActivate acts on the focused row: arm it, or drop it if this
// is the second press inside the same window.
func browserCookiePanelActivate(config *Config) {
	rows := browserCookieRows()
	if browserCookiePanel.cursor < 0 || browserCookiePanel.cursor >= len(rows) {
		return
	}
	row := rows[browserCookiePanel.cursor]
	if row.ForgetAll {
		// The whole-jar drop keeps its own confirm state, so an arm taken on the
		// Forget chip can be finished here and vice versa.
		closeBrowserCookiePanel()
		browserForgetCookiesAction(config)
		return
	}
	if browserCookiePanel.armed != row.Site ||
		browserCookiePanel.armedAt.IsZero() ||
		time.Since(browserCookiePanel.armedAt) > browserForgetConfirmWindow {
		browserCookiePanel.armed = row.Site
		browserCookiePanel.armedAt = time.Now()
		PlayNavSound()
		ShowToast("Press again to drop "+browserCookieRowDropped(row), ToastKindWarning)
		return
	}
	browserCookiePanel.armed = ""
	browserCookiePanel.armedAt = time.Time{}
	dropped, err := browserForgetSiteCookies(row.Site)
	if err != nil {
		ShowToast("Cannot forget "+row.Site+": "+err.Error(), ToastKindError)
		return
	}
	if dropped == 0 {
		ShowToast("No cookies are stored for "+row.Site, ToastKindInfo)
		closeBrowserCookiePanel()
		return
	}
	PlayActivateSound()
	ShowToast("Forgot "+browserCookieCountLabel(dropped)+" from "+row.Site+" — reloading", ToastKindInfo)
	closeBrowserCookiePanel()
	// The page on screen was the signed-in copy of that site; loading it again
	// is what shows the signed-out one.
	browserReloadIfOpen(config)
}

func ctrlBrowserCookies(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		browserCookiePanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		browserCookiePanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		browserCookiePanelActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeBrowserCookiePanel()
	}
	return true
}

func handleBrowserCookiesKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !browserCookiePanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		browserCookiePanelMove(-1)
	case sdl.K_DOWN:
		browserCookiePanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		browserCookiePanelActivate(config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		closeBrowserCookiePanel()
	}
	return true
}

func handleBrowserCookiesMouse(mx, my int32, config *Config) bool {
	if !browserCookiePanel.open {
		return false
	}
	for _, hit := range browserCookieRowHits {
		if pointInRect(mx, my, hit.rect) {
			browserCookiePanel.cursor = hit.index
			browserCookiePanelActivate(config)
			return true
		}
	}
	if browserCookiePanelRect.W > 0 && pointInRect(mx, my, browserCookiePanelRect) {
		return true
	}
	closeBrowserCookiePanel()
	return true
}

// renderBrowserCookiePanel draws the overlay: a site list with counts, the row
// that drops everything, and the armed row spelled out rather than left as a
// colour change. A jar that empties while the panel is open closes it.
func renderBrowserCookiePanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !browserCookiePanel.open {
		return
	}
	rows := browserCookieRows()
	if len(rows) == 0 {
		closeBrowserCookiePanel()
		return
	}
	if browserCookiePanel.cursor >= len(rows) {
		browserCookiePanel.cursor = len(rows) - 1
	}
	if browserCookiePanel.cursor < 0 {
		browserCookiePanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	rowH := smallH + 14
	// The armed press is spelled out in the subtitle line rather than on the row:
	// a second line inside a one-line row drew over the site's name. Here it has
	// the panel's full width and does not move as the cursor does.
	armedNow := browserCookiePanel.armed != "" &&
		time.Since(browserCookiePanel.armedAt) <= browserForgetConfirmWindow
	subtitle := fmt.Sprintf("%d site(s) · A forgets one · B closes", len(rows)-1)
	if len(rows)-1 == 1 {
		subtitle = "1 site · A forgets it · B closes"
	}
	subtitleInk := ColorTextSecondary()
	if armedNow {
		subtitle = "Press A again to drop the cookies from " + browserCookiePanel.armed
		subtitleInk = ColorWarning
	}
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height. Trailing 9
	// gives back the exact band this panel has always reserved.
	header := UIPanelHeader("Saved cookies", subtitle, "settings")
	header.SubtitleColor = subtitleInk
	headerH := UIPanelHeaderBand(config, header, 9)
	footerH := 12 + smallH

	panelW := int32(680)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 120 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	if int32(len(rows)) < maxRows {
		maxRows = int32(len(rows))
	}
	panelH := headerH + maxRows*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	browserCookiePanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if browserCookiePanel.cursor < browserCookiePanel.scroll {
		browserCookiePanel.scroll = browserCookiePanel.cursor
	}
	if browserCookiePanel.cursor >= browserCookiePanel.scroll+int(maxRows) {
		browserCookiePanel.scroll = browserCookiePanel.cursor - int(maxRows) + 1
	}
	if browserCookiePanel.scroll > len(rows)-int(maxRows) {
		browserCookiePanel.scroll = len(rows) - int(maxRows)
	}
	if browserCookiePanel.scroll < 0 {
		browserCookiePanel.scroll = 0
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

	browserCookieRowHits = browserCookieRowHits[:0]
	y := panelY + headerH
	for i := browserCookiePanel.scroll; i < len(rows) && i < browserCookiePanel.scroll+int(maxRows); i++ {
		row := rows[i]
		rect := sdl.Rect{X: panelX + 12, Y: y, W: panelW - 24, H: rowH - 4}
		if i == browserCookiePanel.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, sdl.Color{R: 52, G: 66, B: 110, A: 230})
		}
		label := row.Site
		ink := ColorTextPrimary()
		if row.ForgetAll {
			label = "Forget all " + browserCookieCountLabel(row.Count)
			ink = ColorToastError
		}
		// The count and the site's own session start sit on the right, the way
		// the package list right-aligns its version: the name identifies the
		// row, the metadata sizes it. The date is the half that makes the second
		// press an informed one — "2 cookies · since Oct 12" is the age of this
		// site's login on its own row, before anything is dropped — and it is
		// the same first-seen stamp the Settings row quotes for the jar as a
		// whole, per host here. A site with no stamp shows its count alone.
		meta := ""
		if !row.ForgetAll {
			meta = browserCookieCountLabel(row.Count)
			if !row.Since.IsZero() {
				meta += " · since " + row.Since.Local().Format("Jan 2")
			}
		}
		// The name's budget shrinks by the metadata's real width, so the two
		// right-aligned strings can never be drawn through each other; the
		// floor keeps a long line from squeezing the host out of its own row.
		budget := rect.W - 140
		if meta != "" {
			if w, _, err := font.SizeUTF8(meta); err == nil {
				if avail := rect.W - int32(w) - 24; avail < budget {
					budget = avail
				}
			}
		}
		if budget < 120 {
			budget = 120
		}
		renderText(renderer, config, font, ellipsize(font, label, budget), ink, rect.X+12, rect.Y+7)
		if meta != "" {
			if w, _, err := font.SizeUTF8(meta); err == nil {
				renderText(renderer, config, font, meta, ColorTextSecondary(),
					rect.X+rect.W-12-int32(w), rect.Y+7)
			}
		}
		browserCookieRowHits = append(browserCookieRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: i, rect: rect})
		y += rowH
	}

	hint := hintLine(FooterHint{"D-Pad / Mouse", "Choose"}, FooterHint{"A / Enter", "Forget"})
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+22, panelY+panelH-footerH+8)
}
