package main

import (
	"bytes"
	"context"
	"fmt"
	htmlstd "html"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	gopsutilnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/tidwall/gjson"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// textBrowserSource constants
const (
	textBrowserSourceSystem   = "system"
	textBrowserSourceZeroconf = "zeroconf"
	textBrowserSourceJSON     = "json"
	textBrowserSourceWeb      = "web"

	textBrowserURLVariable = "textbrowser_url"
	textBrowserDefaultURL  = "https://example.com"
	// 8 MiB of HTML: modern news, docs and social pages routinely pass the
	// old 2 MiB mark, and refusing to open them was worse than the cost of
	// holding one page in RAM. Anything past this is not an error: the first
	// 8 MiB are parsed and drawn, with a note where the page was cut.
	textBrowserMaxPageBytes = 8 << 20
	textBrowserMaxTextBytes = 512 << 10
	textBrowserURLBarHeight = int32(36)

	// textBrowserFetchTimeout bounds a page download. A TrimUI on handheld
	// Wi-Fi takes several times longer than a desktop for the same 1.5 MiB of
	// GitHub HTML, and cutting it off at ten seconds showed "Could not load"
	// on pages that were about to arrive. Reload still refetches from scratch.
	textBrowserFetchTimeout = 20 * time.Second
)

// textBrowserMode constants
const (
	textBrowserModeSummary   = "summary"
	textBrowserModeDetails   = "details"
	textBrowserModeProcesses = "processes"
	textBrowserModeNetwork   = "network"
)

// textBrowserAutoRefreshInterval is the default auto-refresh duration.
const textBrowserAutoRefreshInterval = 5 * time.Second

// textBrowserAutoRefreshTimers tracks active auto-refresh timers by variable
// name. Timers are stopped on scene transitions.
var textBrowserAutoRefreshTimers = make(map[string]*time.Timer)
var textBrowserAutoRefreshMutex sync.Mutex
var textBrowserRequestSequence = make(map[string]uint64)

// browserPageFetcher loads one page. It is a variable so the browser's load
// path (cache, stale-request guard, history) can be tested without a network.
//
// A load runs on its own goroutine, so the seam is read under a lock and tests
// swap it with setBrowserPageFetcherForTest: assigning the variable directly
// races with a fetch that is still on the wire, which the race detector reports
// as soon as a page load outlives the test that started it.
var (
	browserPageFetcherMu sync.RWMutex
	browserPageFetcher   = browseWebPageResult
)

// browserFetches counts page loads still on the wire, so a test reset can wait
// for them instead of wiping state a late fetch is about to publish into.
var browserFetches sync.WaitGroup

// browserFetchWait bounds how long a reset waits for page loads to return, so a
// stuck fetch fails the suite with a log line rather than hanging it.
const browserFetchWait = 10 * time.Second

// currentBrowserPageFetcher returns the fetch function a new load should use.
func currentBrowserPageFetcher() func(string) browserPage {
	browserPageFetcherMu.RLock()
	defer browserPageFetcherMu.RUnlock()
	return browserPageFetcher
}

// setBrowserPageFetcherForTest replaces the network seam and returns the
// function that restores it. Tests must use this instead of assigning
// browserPageFetcher, which would race with an in-flight load.
func setBrowserPageFetcherForTest(fn func(string) browserPage) func() {
	browserPageFetcherMu.Lock()
	prev := browserPageFetcher
	browserPageFetcher = fn
	browserPageFetcherMu.Unlock()
	return func() {
		browserPageFetcherMu.Lock()
		browserPageFetcher = prev
		browserPageFetcherMu.Unlock()
	}
}

// waitForBrowserFetches blocks until the loads started by the test that just
// finished have returned (or the wait is capped), so its cleanup can reset the
// browser globals without a fetch publishing into the next test.
func waitForBrowserFetches() {
	done := make(chan struct{})
	go func() {
		browserFetches.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(browserFetchWait):
		log.Printf("[browser] a page load is still running after %s; resetting anyway", browserFetchWait)
	}
}

var textBrowserRequestMutex sync.Mutex
var textBrowserAddressEditing bool
var textBrowserAddressSubmitted bool

// The Search shortcut types into its own buffer: the address bar keeps the
// page's URL, so cancelling the keyboard leaves the browser where it was.
var textBrowserSearchEditing bool
var textBrowserSearchSubmitted bool

// renderTextBrowser renders a scrollable text panel, including readable web pages.
func renderTextBrowser(renderer *sdl.Renderer, config *Config, element Element) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[textbrowser] recovered panic in render: %v", r)
		}
	}()
	elemW := getElementWidth(element, 1100)
	elemH := getElementHeight(element, 480)
	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(235), accentColor)

	text := ""
	if element.Variable != "" {
		if v, ok := config.Variables.Custom[element.Variable].(string); ok {
			text = v
		}
	}
	if text == "" {
		if textBrowserElementSource(element) == textBrowserSourceWeb {
			text = "Enter a website address above and press Go."
		} else if element.AutoRefresh {
			text = "Auto-refreshing..."
		} else {
			text = "Press a trigger to load content.\nSources: system, zeroconf, json, web"
		}
	}

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font == nil {
		return
	}

	headerH := int32(28)
	headerY := element.Y + 4
	source := textBrowserElementSource(element)

	var headerColor sdl.Color
	switch source {
	case "zeroconf":
		headerColor = sdl.Color{R: 46, G: 204, B: 113, A: 255}
	case textBrowserSourceJSON:
		headerColor = sdl.Color{R: 155, G: 89, B: 182, A: 255}
	case textBrowserSourceWeb:
		headerColor = sdl.Color{R: 24, G: 143, B: 173, A: 255}
	default:
		headerColor = sdl.Color{R: 67, G: 97, B: 238, A: 255}
	}

	// A web page is laid out (or wrapped) once per page and then reused every
	// frame. Doing it per frame handed the GC a mountain of throwaway strings,
	// which is what made the browser feel like it would fall over on a 1 GB
	// device. Styled pages come from webpage.go; everything else keeps using
	// the plain-text reader.
	lineH := int32(22)
	contentH := elemH - headerH - 8
	if source == textBrowserSourceWeb {
		contentH = elemH - headerH - textBrowserURLBarHeight - 12
	}
	maxLines := int((contentH) / lineH)
	var lines []string
	var styledDoc *webDocument
	var styledLayout *webLayout
	totalLines := 0
	// A page that is still on the wire has no document yet; the renderer
	// shows a loading card instead of a one-line text stub.
	loading := source == textBrowserSourceWeb && strings.HasPrefix(text, "Loading ")
	if source == textBrowserSourceWeb {
		webWidth := elemW - 14
		styledDoc = browserDocumentFor(element.Variable)
		if layout, rows := webDocumentRows(styledDoc, config, element, webWidth); layout != nil {
			styledLayout = layout
			lineH = layout.RowH
			maxLines = int(contentH / lineH)
			if maxLines < 1 {
				maxLines = 1
			}
			// The page is clipped to whole rows so the last line is never
			// cut through the middle of its glyphs.
			contentH = int32(maxLines) * lineH
			totalLines = rows
		} else {
			lines = textBrowserWrappedLines(text, font, elemW-28)
			totalLines = len(lines)
		}
	} else {
		lines = strings.Split(text, "\n")
		totalLines = len(lines)
	}

	fillRoundedRect(renderer, element.X+4, headerY, elemW-8, headerH, 4, headerColor)
	labelFont, _ := getCachedFont(config, "small")
	if labelFont != nil {
		sourceLabel := "SYSTEM"
		switch source {
		case textBrowserSourceZeroconf:
			sourceLabel = "ZEROCONF"
		case textBrowserSourceJSON:
			sourceLabel = "JSON"
		case textBrowserSourceWeb:
			sourceLabel = "WEB"
		}
		lw, lh, _ := labelFont.SizeUTF8(sourceLabel)
		textY := headerY + (headerH-int32(lh))/2
		renderText(renderer, config, labelFont, sourceLabel, sdl.Color{R: 255, G: 255, B: 255, A: 255}, element.X+12, textY)

		// The header is a browser tab strip in miniature: source badge,
		// page title, then the position and refresh time pushed right.
		rightEdge := element.X + elemW - 12
		if element.AutoRefresh {
			rightEdge -= 20
		}
		if textBrowserLastUpdate > 0 {
			ts := time.Unix(textBrowserLastUpdate, 0).Format("15:04:05")
			tw, _, _ := labelFont.SizeUTF8(ts)
			tsX := rightEdge - int32(tw)
			renderText(renderer, config, labelFont, ts, sdl.Color{R: 255, G: 255, B: 255, A: 180}, tsX, textY)
			rightEdge = tsX - 12
		}
		if text != "" && totalLines > 0 && !loading {
			lineInfo := ""
			if totalLines > maxLines {
				startLine := int(textBrowserScrollY) + 1
				endLine := startLine + maxLines
				if endLine > totalLines {
					endLine = totalLines
				}
				lineInfo = fmt.Sprintf("%d-%d/%d", startLine, endLine, totalLines)
			} else {
				lineInfo = fmt.Sprintf("%d lines", totalLines)
			}
			infoW, _, _ := labelFont.SizeUTF8(lineInfo)
			rightEdge -= int32(infoW)
			renderText(renderer, config, labelFont, lineInfo, sdl.Color{R: 255, G: 255, B: 255, A: 180}, rightEdge, textY)
			rightEdge -= 12
		}
		if styledDoc != nil {
			title := strings.TrimSpace(styledDoc.Title)
			if title != "" {
				left := element.X + 12 + int32(lw) + 10
				if avail := rightEdge - left; avail > 40 {
					renderText(renderer, config, labelFont, ellipsize(labelFont, title, avail), sdl.Color{R: 255, G: 255, B: 255, A: 230}, left, textY)
				}
			}
		}

		if element.AutoRefresh {
			spinnerX := element.X + elemW - 24
			if textBrowserLastUpdate > 0 {
				spinnerX -= 20
			}
			spinnerY := headerY + headerH/2
			angle := float64(sdl.GetTicks64()%360) * math.Pi / 180.0
			for i := 0; i < 8; i++ {
				a := float64(i) * 45.0 * math.Pi / 180.0
				x := spinnerX + int32(math.Cos(angle+a)*6)
				y := spinnerY + int32(math.Sin(angle+a)*6)
				alpha := uint8(255 - (i * 28))
				renderer.SetDrawColor(255, 255, 255, alpha)
				renderer.FillRect(&sdl.Rect{X: x, Y: y, W: 2, H: 2})
			}
		}
	}

	contentTop := element.Y + 8 + headerH
	if source == textBrowserSourceWeb {
		addressRect, searchRect, goRect, filterRect := textBrowserToolbarLayout(element)
		fillRoundedRect(renderer, addressRect.X, addressRect.Y, addressRect.W, addressRect.H, 6, sdl.Color{R: 27, G: 34, B: 48, A: 255})
		strokeRoundedRect(renderer, addressRect.X, addressRect.Y, addressRect.W, addressRect.H, 6, 1, sdl.Color{R: 93, G: 110, B: 138, A: 210})
		address := textBrowserAddress(config)
		if labelFont != nil {
			hint := "EDIT URL"
			hw, _, _ := labelFont.SizeUTF8(hint)
			// Fit the address to the bar in pixels. A long URL used to run
			// under the hint and into the Go button.
			if avail := addressRect.W - int32(hw) - 30; avail > 40 {
				address = ellipsize(labelFont, address, avail)
			}
			renderText(renderer, config, labelFont, address, ColorTextPrimary(), addressRect.X+10, addressRect.Y+9)
			renderText(renderer, config, labelFont, hint, sdl.Color{R: 155, G: 170, B: 195, A: 255}, addressRect.X+addressRect.W-int32(hw)-10, addressRect.Y+9)
			if searchRect.W > 0 {
				fillRoundedRect(renderer, searchRect.X, searchRect.Y, searchRect.W, searchRect.H, 6, sdl.Color{R: 36, G: 45, B: 63, A: 255})
				strokeRoundedRect(renderer, searchRect.X, searchRect.Y, searchRect.W, searchRect.H, 6, 1, WithAlpha(accentColor, 170))
				sw, sh, _ := labelFont.SizeUTF8("SEARCH")
				renderText(renderer, config, labelFont, "SEARCH", WithAlpha(accentColor, 235), searchRect.X+(searchRect.W-int32(sw))/2, searchRect.Y+(searchRect.H-int32(sh))/2)
			}
			tw, th, _ := labelFont.SizeUTF8("GO")
			fillRoundedRect(renderer, goRect.X, goRect.Y, goRect.W, goRect.H, 6, accentColor)
			renderText(renderer, config, labelFont, "GO", sdl.Color{R: 255, G: 255, B: 255, A: 255}, goRect.X+(goRect.W-int32(tw))/2, goRect.Y+(goRect.H-int32(th))/2)
			if filterRect.W > 0 {
				// The chip is the page's filter state and its count at once:
				// amber says this site is excepted, accent says the load
				// skipped something, and neutral means nothing was in the way.
				view := browserAdBlockViewFor(element.Variable)
				label := ellipsize(labelFont, browserAdBlockLabel(view), filterRect.W-8)
				fill := sdl.Color{R: 36, G: 45, B: 63, A: 255}
				ink := sdl.Color{R: 155, G: 170, B: 195, A: 255}
				border := sdl.Color{R: 93, G: 110, B: 138, A: 170}
				switch {
				case view.Off:
					fill = sdl.Color{R: 68, G: 48, B: 30, A: 255}
					ink = ColorWarning
					border = WithAlpha(ColorWarning, 190)
				case view.Blocked > 0:
					fill = sdl.Color{R: 24, G: 58, B: 66, A: 255}
					ink = WithAlpha(accentColor, 240)
					border = WithAlpha(accentColor, 170)
				}
				fillRoundedRect(renderer, filterRect.X, filterRect.Y, filterRect.W, filterRect.H, 6, fill)
				strokeRoundedRect(renderer, filterRect.X, filterRect.Y, filterRect.W, filterRect.H, 6, 1, border)
				fw, fh, _ := labelFont.SizeUTF8(label)
				renderText(renderer, config, labelFont, label, ink,
					filterRect.X+(filterRect.W-int32(fw))/2, filterRect.Y+(filterRect.H-int32(fh))/2)
			}
		}
		contentTop += textBrowserURLBarHeight + 4
	}
	y := contentTop

	scrollMax := int32(0)
	if int32(totalLines) > int32(maxLines) {
		scrollMax = int32(totalLines) - int32(maxLines)
	}
	if textBrowserScrollY < 0 {
		textBrowserScrollY = 0
	}
	if textBrowserScrollY > scrollMax {
		textBrowserScrollY = scrollMax
	}

	startIdx := int(textBrowserScrollY)
	endIdx := startIdx + maxLines
	if endIdx > totalLines {
		endIdx = totalLines
	}
	if startIdx < 0 {
		startIdx = 0
	}

	loading = styledLayout == nil && loading
	if styledLayout != nil {
		drawWebDocument(renderer, config, styledDoc, styledLayout, sdl.Rect{
			X: element.X + 7,
			Y: contentTop + 4,
			W: elemW - 14,
			H: contentH,
		}, int(textBrowserScrollY))
		// Above the page, below the toolbar: the D-pad's current link.
		browserDrawPageLinkFocus(renderer, element, styledLayout, int(textBrowserScrollY), contentTop+4, contentH)
	} else if loading {
		drawTextBrowserLoading(renderer, config, element, contentTop, contentH, accentColor)
	} else {
		for i := startIdx; i < endIdx; i++ {
			ln := lines[i]
			if strings.HasPrefix(ln, "===") || strings.HasPrefix(ln, "---") {
				renderText(renderer, config, font, ln, accentColor, element.X+10, y)
			} else if source != textBrowserSourceWeb && strings.Contains(ln, ":") && !strings.HasPrefix(ln, " ") {
				parts := strings.SplitN(ln, ":", 2)
				if len(parts) == 2 {
					renderText(renderer, config, font, parts[0]+":", ColorTextSecondary(), element.X+10, y)
					renderText(renderer, config, font, parts[1], ColorTextPrimary(), element.X+10+int32(len(parts[0])*12)+20, y)
				} else {
					renderText(renderer, config, font, ln, ColorTextPrimary(), element.X+10, y)
				}
			} else {
				renderText(renderer, config, font, ln, ColorTextPrimary(), element.X+10, y)
			}
			y += lineH
		}
	}

	if scrollMax > 0 {
		barX := element.X + elemW - 10
		barY := element.Y + 8 + headerH
		barH := contentH
		thumbH := int32(0)
		if totalLines > 0 {
			thumbH = barH * int32(maxLines) / int32(totalLines)
			if thumbH < 20 {
				thumbH = 20
			}
		}
		thumbY := barY + int32(float64(barH-thumbH)*float64(textBrowserScrollY)/float64(scrollMax))

		renderer.SetDrawColor(220, 225, 235, 100)
		renderer.FillRect(&sdl.Rect{X: barX, Y: barY, W: 4, H: barH})

		renderer.SetDrawColor(140, 150, 175, 220)
		renderer.FillRect(&sdl.Rect{X: barX, Y: thumbY, W: 4, H: thumbH})

		if thumbH > 4 {
			renderer.SetDrawColor(160, 170, 190, 180)
			renderer.FillRect(&sdl.Rect{X: barX + 1, Y: thumbY + 2, W: 2, H: thumbH - 4})
		}
	}
}

// drawTextBrowserLoading paints the placeholder shown while a page is still on
// the wire: a small card with pulsing dots, so a slow page reads as work in
// progress instead of an empty panel with one line of text in the corner.
func drawTextBrowserLoading(renderer *sdl.Renderer, config *Config, element Element, top, height int32, accent sdl.Color) {
	if renderer == nil || height < 90 {
		return
	}
	panelW := getElementWidth(element, 1100) - 80
	if panelW > 560 {
		panelW = 560
	}
	if panelW < 240 {
		panelW = 240
	}
	fullW := getElementWidth(element, 1100)
	x := element.X + (fullW-panelW)/2
	cardH := int32(104)
	y := top + (height-cardH)/2

	fillRoundedRect(renderer, x, y, panelW, cardH, 10, ColorPanelRaised)
	strokeRoundedRect(renderer, x, y, panelW, cardH, 10, 1, ColorDivider)
	// Accent rail: the card reads as the browser's own surface rather than a
	// floating dialog.
	fillRoundedRect(renderer, x, y, 4, cardH, 2, accent)

	// Three accent dots breathe in sequence; the phase comes from the frame
	// clock, so there is nothing to update from the network goroutine.
	phase := float64(sdl.GetTicks64()%1200) / 1200.0 * 2 * math.Pi
	dotX := x + panelW - 74
	dotY := y + 26
	for i := 0; i < 3; i++ {
		pulse := 0.5 + 0.5*math.Sin(phase-float64(i)*0.7)
		alpha := uint8(90 + 150*pulse)
		fillRoundedRect(renderer, dotX+int32(i*16), dotY, 10, 10, 5, sdl.Color{R: accent.R, G: accent.G, B: accent.B, A: alpha})
	}

	labelFont, _ := getCachedFont(config, "small")
	if labelFont == nil {
		return
	}
	renderText(renderer, config, labelFont, "LOADING PAGE", accent, x+20, y+16)
	address := ellipsize(labelFont, textBrowserAddress(config), panelW-40)
	renderText(renderer, config, labelFont, address, ColorTextPrimary(), x+20, y+46)
	renderText(renderer, config, labelFont, "Rendering text as soon as the page arrives", ColorTextSecondary(), x+20, y+72)
}

// findTextBrowserVariable returns the variable name of the first textbrowser
// element in the current scene, or a sensible default. This lets trigger
// buttons (which may have an empty variable) write to the correct place.
func findTextBrowserVariable(config *Config) string {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return "textbrowser_content"
	}
	for _, elem := range config.Scenes[currentSceneIndex].Elements {
		if elem.Type == "textbrowser" && elem.Variable != "" {
			return elem.Variable
		}
	}
	return "textbrowser_content"
}

// textBrowserElementSource resolves the source used by both exported configs
// (source) and older hand-written configs (text).
func textBrowserElementSource(element Element) string {
	source := strings.ToLower(strings.TrimSpace(element.Source))
	if source == "" {
		source = strings.ToLower(strings.TrimSpace(element.Text))
	}
	switch {
	case source == "", source == textBrowserSourceSystem, strings.Contains(source, "system"):
		return textBrowserSourceSystem
	case source == textBrowserSourceZeroconf, strings.Contains(source, "zeroconf"):
		return textBrowserSourceZeroconf
	case source == textBrowserSourceJSON, strings.Contains(source, "json"):
		return textBrowserSourceJSON
	case source == textBrowserSourceWeb, strings.Contains(source, "web") || strings.Contains(source, "browser"):
		return textBrowserSourceWeb
	default:
		return textBrowserSourceSystem
	}
}

// textBrowserRefresh refreshes the source configured for a textbrowser element.
func textBrowserRefresh(config *Config, element Element) {
	switch textBrowserElementSource(element) {
	case textBrowserSourceZeroconf:
		publishCustom(element.Variable, browseZeroconfServices())
	case textBrowserSourceJSON:
		publishCustom(element.Variable, browseJSONContent(element))
	case textBrowserSourceWeb:
		// Auto-refresh and the scene trigger are not navigations: keep the
		// back/forward stack stable while the page reloads.
		loadTextBrowserURLAt(config, element, textBrowserAddress(config), false)
	default:
		publishCustom(element.Variable, browseSystemInfo(element))
	}
	textBrowserLastUpdate = time.Now().Unix()
}

func handleTextBrowserAddressInput(renderer *sdl.Renderer, config *Config, element Element) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	for i, current := range config.Scenes[currentSceneIndex].Elements {
		if current.Type == "textbrowser" && current.Variable == element.Variable {
			textBrowserAddressEditing = true
			textBrowserAddressSubmitted = false
			vkOpenSceneInput(config, currentSceneIndex, i)
			runInputModal(renderer, config)
			textBrowserAddressEditing = false
			activeSceneIndex = -1
			activeElementIndex = -1
			if textBrowserAddressSubmitted {
				loadTextBrowserURL(config, current, textBrowserAddress(config))
			}
			textBrowserAddressSubmitted = false
			return
		}
	}
}

func textBrowserAddress(config *Config) string {
	if config != nil {
		if address, ok := config.Variables.Custom[textBrowserURLVariable].(string); ok && strings.TrimSpace(address) != "" {
			return strings.TrimSpace(address)
		}
	}
	return textBrowserDefaultURL
}

const (
	textBrowserGoWidth     = int32(64)
	textBrowserSearchWidth = int32(84)
	textBrowserBarGap      = int32(8)
)

// textBrowserFilterWidth is the filter chip's share of the toolbar. It is
// derived from the panel width rather than fixed, because the type scales with
// the panel: a chip that fits "99+ BLOCKED" at 800x480 would clip it at
// 1280x720.
func textBrowserFilterWidth(width int32) int32 {
	w := width / 7
	if w < 96 {
		w = 96
	}
	if w > 200 {
		w = 200
	}
	return w
}

// textBrowserToolbarRects lays out the web toolbar: the address field and Go.
// The Search chip exists so the controller has a visible target for Y instead
// of a hidden shortcut, and the filter chip reports the page's blocked count.
func textBrowserToolbarRects(element Element) (address, goButton sdl.Rect) {
	address, _, goButton, _ = textBrowserToolbarLayout(element)
	return
}

// textBrowserFilterRect is the ad-filter chip's hit area.
func textBrowserFilterRect(element Element) sdl.Rect {
	_, _, _, filter := textBrowserToolbarLayout(element)
	return filter
}

// textBrowserScrollLimit is the last scroll row for an element, measured the
// same way the renderer draws it. A styled web page scrolls by layout rows; a
// plain-text panel scrolls by its 22px lines. -1 means "not measured yet": the
// caller must not clamp, because the page is still on the wire.
func textBrowserScrollLimit(config *Config, element Element) int32 {
	if textBrowserElementSource(element) == textBrowserSourceWeb {
		if layout := browserPageLayout(config, element); layout != nil {
			return browserScrollMax(element, layout)
		}
		return -1
	}
	contentH := getElementHeight(element, 480) - 28 - 8
	maxLines := int(contentH / 22)
	totalLines := 0
	if v, ok := config.Variables.Custom[element.Variable].(string); ok {
		totalLines = len(strings.Split(v, "\n"))
	}
	if totalLines > maxLines {
		return int32(totalLines - maxLines)
	}
	return 0
}

func textBrowserToolbarLayout(element Element) (address, search, goButton, filter sdl.Rect) {
	width := getElementWidth(element, 1100)
	y := element.Y + 36
	h := textBrowserURLBarHeight - 4
	// Full row: address, Search, Go, filter. A narrow panel drops Search
	// first (the Y button still opens it) and only then the filter, which is
	// the one control that can rescue a page filtering broke.
	filterW := textBrowserFilterWidth(width)
	addressW := width - textBrowserGoWidth - textBrowserSearchWidth - filterW - textBrowserBarGap*3 - 16
	showSearch, showFilter := true, true
	if addressW < 160 {
		showSearch = false
		addressW = width - textBrowserGoWidth - filterW - textBrowserBarGap*2 - 16
	}
	if addressW < 160 {
		showFilter = false
		addressW = width - textBrowserGoWidth - textBrowserBarGap - 16
	}
	if addressW < 80 {
		addressW = 80
	}

	address = sdl.Rect{X: element.X + 8, Y: y, W: addressW, H: h}
	cursor := address.X + address.W + textBrowserBarGap
	if showSearch {
		search = sdl.Rect{X: cursor, Y: y, W: textBrowserSearchWidth, H: h}
		cursor = search.X + search.W + textBrowserBarGap
	}
	goButton = sdl.Rect{X: cursor, Y: y, W: textBrowserGoWidth, H: h}
	cursor = goButton.X + goButton.W + textBrowserBarGap
	if showFilter {
		filter = sdl.Rect{X: cursor, Y: y, W: filterW, H: h}
	}
	return
}

// textBrowserSearchRect is the Search chip's hit area.
func textBrowserSearchRect(element Element) sdl.Rect {
	_, search, _, _ := textBrowserToolbarLayout(element)
	return search
}

// loadTextBrowserURL is the address-bar entry point: it records the page in
// the browser's back/forward history. Internal reloads and history traversal
// call loadTextBrowserURLAt directly so they do not grow the stack.
func loadTextBrowserURL(config *Config, element Element, rawURL string) {
	loadTextBrowserURLAt(config, element, rawURL, true)
}

func loadTextBrowserURLAt(config *Config, element Element, rawURL string, recordHistory bool) {
	browserLoadURL(config, element, rawURL, recordHistory, true)
}

// reloadTextBrowserURL refetches a page even when the cache still holds a copy,
// which is what the Reload button promises after a page rendered badly or a
// connection dropped halfway.
func reloadTextBrowserURL(config *Config, element Element, rawURL string) {
	browserLoadURL(config, element, rawURL, false, false)
}

// browserLoadURL is the single path into the reader for web pages. A cached
// page is shown immediately (no request, no "Loading" card); everything else is
// fetched on a goroutine, so a slow page never stalls a frame.
func browserLoadURL(config *Config, element Element, rawURL string, recordHistory, useCache bool) {
	if config == nil {
		return
	}
	outputVariable := element.Variable
	if outputVariable == "" {
		outputVariable = findTextBrowserVariable(config)
	}
	address, err := normalizeTextBrowserURL(rawURL)
	if err != nil {
		config.Variables.Custom[outputVariable] = "Browser error: " + err.Error()
		showToast("Enter a valid http:// or https:// address.", ToastError())
		return
	}
	config.Variables.Custom[textBrowserURLVariable] = address
	textBrowserScrollY = 0
	textBrowserScrollVelocity = 0
	// The toolbar's filter chip belongs to the address being opened, not to the
	// page still on screen: a reload after turning filtering off shows the new
	// state immediately.
	setBrowserAdBlockView(outputVariable, browserAdBlockView{
		Site: browserAdBlockSiteKey(address),
		Off:  browserAdBlockOff(address),
	})

	// Bumping the sequence here cancels anything still on the wire for this
	// variable: a cached page must not be replaced a second later by an older
	// request the user already navigated away from.
	textBrowserRequestMutex.Lock()
	textBrowserRequestSequence[outputVariable]++
	requestID := textBrowserRequestSequence[outputVariable]
	textBrowserRequestMutex.Unlock()

	if useCache {
		if page, ok := browserCachedPage(address); ok {
			showBrowserPage(config, outputVariable, page)
			if recordHistory {
				browserRecordURL(address)
			}
			return
		}
	}

	config.Variables.Custom[outputVariable] = "Loading " + address + " ..."
	setBrowserLinks(outputVariable, nil)
	setBrowserDocument(outputVariable, nil)
	if recordHistory {
		browserRecordURL(address)
	}

	// The seam is read once, here: a load keeps the fetcher it started with even
	// if a test swaps it while the request is still running.
	fetch := currentBrowserPageFetcher()
	browserFetches.Add(1)
	go func(variable, pageURL string, id uint64) {
		defer browserFetches.Done()
		page := fetch(pageURL)
		textBrowserRequestMutex.Lock()
		isCurrent := textBrowserRequestSequence[variable] == id
		textBrowserRequestMutex.Unlock()
		if !isCurrent {
			return
		}
		// Remember the page for Back/Forward before publishing it: the next
		// visit to the same address is then a map lookup instead of a download.
		browserCachePage(pageURL, page)
		setBrowserAdBlockView(variable, browserAdBlockView{
			Site:    browserAdBlockSiteKey(page.FinalURL),
			Blocked: page.Blocked,
			Off:     browserAdBlockOff(page.FinalURL),
		})
		setBrowserLinks(variable, page.Links)
		setBrowserDocument(variable, page.Doc)
		publishCustom(variable, page.Text)
	}(outputVariable, address, requestID)
}

// showBrowserPage publishes a page the cache already holds, synchronously:
// there is nothing to wait for, so Back can land in the same frame.
func showBrowserPage(config *Config, variable string, page browserPage) {
	setBrowserAdBlockView(variable, browserAdBlockView{
		Site:    browserAdBlockSiteKey(page.FinalURL),
		Blocked: page.Blocked,
		Off:     browserAdBlockOff(page.FinalURL),
	})
	setBrowserLinks(variable, page.Links)
	setBrowserDocument(variable, page.Doc)
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[variable] = page.Text
}

// textBrowserSearchURL turns an address-bar entry that is clearly not a host
// into a web search, so the browser doubles as a search box the way a handheld
// user expects from a single field.
func textBrowserSearchURL(query string) string {
	return browserSearchURL + url.QueryEscape(query)
}

// textBrowserLooksLikeAddress reports whether the entry names a host rather
// than being something the user typed to look up.
func textBrowserLooksLikeAddress(address string) bool {
	// Whitespace means the user typed a query, not a URL: "github issues"
	// searches, while "example.com/docs" opens.
	if strings.ContainsAny(address, " \t") {
		return false
	}
	host := address
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return false
	}
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if host == "localhost" || strings.HasPrefix(host, "[") {
		return true
	}
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	// Something like example.com, 192.168.1.4 or api.github.com. A bare word
	// with no dot is a search, not a host.
	return strings.Contains(host, ".") && !strings.HasSuffix(host, ".")
}

func normalizeTextBrowserURL(rawURL string) (string, error) {
	address := strings.TrimSpace(rawURL)
	if address == "" {
		return "", fmt.Errorf("address is empty")
	}
	if !strings.Contains(address, "://") {
		if !textBrowserLooksLikeAddress(address) {
			return textBrowserSearchURL(address), nil
		}
		address = "https://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return "", fmt.Errorf("invalid address: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("only http and https pages are supported")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("address must contain a host and cannot include credentials")
	}
	return parsed.String(), nil
}

func browseWebPage(rawURL string) string {
	return browseWebPageResult(rawURL).Text
}

func browseWebPageResult(rawURL string) browserPage {
	// Every subresource this load requests is counted here, so the reader can
	// say how much advertising it kept off the wire.
	resetBrowserAdBlockCount()
	address, err := normalizeTextBrowserURL(rawURL)
	if err != nil {
		return browserPage{Text: "Browser error: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), textBrowserFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return browserPage{Text: "Browser error: " + err.Error()}
	}
	req.Header.Set("User-Agent", "JukaHub Browser/1.1")
	// The jar travels with the client, so a cookie a redirect sets is already
	// in hand when the next hop — and the page's stylesheets and images — go
	// out.
	client := browserHTTPClient(textBrowserFetchTimeout)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirected to an unsupported URL scheme")
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return browserPage{Text: fmt.Sprintf("Could not load %s\nTimed out after %s. Check Wi-Fi, then press Reload (or Y to search).",
				address, textBrowserFetchTimeout)}
		}
		return browserPage{Text: fmt.Sprintf("Could not load %s\n%s", address, err.Error())}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return browserPage{Text: fmt.Sprintf("Could not load %s\nHTTP %s", address, resp.Status)}
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "html") {
		return browserPage{Text: fmt.Sprintf("This page is not HTML (%s).\nTry a website that serves readable HTML.", contentType)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, textBrowserMaxPageBytes+1))
	if err != nil {
		return browserPage{Text: fmt.Sprintf("Could not read %s\n%s", address, err.Error())}
	}
	body, truncated := truncateTextBrowserBody(body, textBrowserMaxPageBytes)
	// Ad and tracker scripts are removed before the script pass: there is no
	// reason to spend the page's 5-second script budget running them, and some
	// ship an inline fallback that would run even with the src blocked.
	html := stripBlockedWebScripts(string(body), resp.Request.URL)
	// Page scripts run first, so the text, link, CSS and image passes below see
	// the markup they produced (webscript.go).
	source := runWebPageScripts(html, resp.Request.URL)
	title, pageText := extractTextBrowserHTML(source)
	if title == "" {
		title = resp.Request.URL.Hostname()
	}
	if pageText == "" {
		pageText = "This page has no readable text content."
	}
	pageText = capTextBrowserText(pageText)
	links := extractBrowserLinks(source, resp.Request.URL)
	// Linked stylesheets carry most of a real page's look. Fetching a few small
	// ones is cheap; the parser then applies the presentation subset it can
	// draw, and the plain text above remains the fallback for everything else.
	pageCSS := fetchWebStylesheets(source, resp.Request.URL)
	if truncated {
		// Say so on the drawn page itself: the reader shows the styled document
		// rather than the plain text, so a note only in the text would be
		// invisible, and a page that simply stops mid-sentence looks broken.
		source = withTextBrowserTruncationNote(source)
	}
	doc := parseWebPage(source, pageCSS, resp.Request.URL.String())
	// Images load with the page: one bounded concurrent pass stores the bytes
	// and natural sizes the reader paints from (webpage_image.go).
	fetchWebImages(doc)
	if doc.Title == "" {
		doc.Title = title
	}
	if len(doc.Blocks) == 0 {
		doc = nil
	}
	// The reader text is assembled last: the blocked count and truncation state
	// are only final once the stylesheet and image passes have run.
	text := browserReaderText(title, resp.Request.URL.String(), len(links), pageText,
		browserBlockedRequests(), browserAdBlockOff(resp.Request.URL.String()), truncated)
	return browserPage{
		Text:     text,
		Links:    links,
		FinalURL: resp.Request.URL.String(),
		Doc:      doc,
		Blocked:  browserBlockedRequests(),
	}
}

// browserReaderText builds the plain-text view a page without a drawable
// document falls back to. It states what was skipped, so a reader can tell a
// trimmed page from a complete one.
func browserReaderText(title, address string, linkCount int, pageText string, blocked int, filterOff bool, truncated bool) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Title: %s\nURL: %s\n", title, address)
	if filterOff {
		text.WriteString("Ad filtering is off for this site\n")
	} else if blocked > 0 {
		fmt.Fprintf(&text, "Blocked: %d ad/tracker request(s)\n", blocked)
	}
	if truncated {
		text.WriteString(strings.TrimSpace(textBrowserTruncationNote()))
		text.WriteByte('\n')
	}
	if linkCount > 0 {
		fmt.Fprintf(&text, "Links: %d (press X or L to choose one)\n", linkCount)
	}
	text.WriteByte('\n')
	text.WriteString(pageText)
	return text.String()
}

// truncateTextBrowserBody keeps the first limit bytes of a page and reports
// whether the rest was dropped. The cut lands after the last complete tag so
// the fragment still parses: cutting through a tag or an attribute value would
// swallow the remainder of the document.
func truncateTextBrowserBody(body []byte, limit int) ([]byte, bool) {
	if len(body) <= limit {
		return body, false
	}
	cut := limit
	if i := bytes.LastIndexByte(body[:limit], '>'); i >= limit/2 {
		cut = i + 1
	}
	return body[:cut], true
}

// textBrowserTruncationNote is the sentence shown where a page was cut off.
func textBrowserTruncationNote() string {
	return fmt.Sprintf("\n[Page truncated: only the first %d MiB were downloaded. Follow a link to keep reading.]", textBrowserMaxPageBytes>>20)
}

// withTextBrowserTruncationNote puts that sentence at the top of the page,
// inside the body. A truncated page is far past the parser's text budget, so a
// note at the end would be dropped along with the rest of the unread tail.
func withTextBrowserTruncationNote(source string) string {
	note := "<p>" + textBrowserTruncationNote() + "</p>"
	lower := strings.ToLower(source)
	for from := 0; from < len(lower); {
		at := strings.Index(lower[from:], "<body")
		if at < 0 {
			break
		}
		at += from
		after := at + len("<body")
		if after < len(source) && (source[after] == '>' || source[after] == ' ' || source[after] == '\t' || source[after] == '\n' || source[after] == '\r') {
			if end := strings.IndexByte(source[after:], '>'); end >= 0 {
				cut := after + end + 1
				return source[:cut] + note + source[cut:]
			}
		}
		from = after
	}
	return note + source
}

// capTextBrowserText bounds how much readable text a page may contribute. The
// text is what the renderer keeps wrapped in memory, and the TrimUI Smart Pro
// has 1 GB of RAM shared with everything else: a handful of very long articles
// must never be able to grow the page buffer without limit.
func capTextBrowserText(text string) string {
	if len(text) <= textBrowserMaxTextBytes {
		return text
	}
	cut := strings.LastIndexByte(text[:textBrowserMaxTextBytes], '\n')
	if cut < textBrowserMaxTextBytes/2 {
		cut = textBrowserMaxTextBytes
	}
	return text[:cut] + "\n\n[Page truncated after 512 KiB of text. Follow a link to keep reading.]"
}

func extractTextBrowserHTML(source string) (string, string) {
	var body, title strings.Builder
	var skipped []string
	inTitle := false
	for i := 0; i < len(source); {
		if source[i] != '<' {
			end := strings.IndexByte(source[i:], '<')
			if end < 0 {
				end = len(source)
			} else {
				end += i
			}
			text := source[i:end]
			if inTitle {
				title.WriteString(text)
			} else if len(skipped) == 0 {
				body.WriteString(text)
			}
			i = end
			continue
		}
		if strings.HasPrefix(source[i:], "<!--") {
			if end := strings.Index(source[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				break
			}
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			if len(skipped) == 0 && !inTitle {
				body.WriteByte(source[i])
			}
			i++
			continue
		}
		tag := strings.TrimSpace(source[i+1 : end])
		closing := strings.HasPrefix(tag, "/")
		if closing {
			tag = strings.TrimSpace(tag[1:])
		}
		nameEnd := strings.IndexAny(tag, " \t\r\n/>")
		name := strings.ToLower(tag)
		if nameEnd >= 0 {
			name = strings.ToLower(tag[:nameEnd])
		}
		if name == "title" {
			inTitle = !closing
		} else if closing {
			for j := len(skipped) - 1; j >= 0; j-- {
				if skipped[j] == name {
					skipped = skipped[:j]
					break
				}
			}
		}
		if len(skipped) == 0 && !inTitle && isTextBrowserBlockTag(name) {
			body.WriteByte('\n')
		}
		if !closing && isTextBrowserSuppressedTag(name) && !strings.HasSuffix(tag, "/") {
			skipped = append(skipped, name)
		}
		i = end + 1
	}
	return cleanTextBrowserHTML(title.String()), cleanTextBrowserHTML(body.String())
}

func findTextBrowserTagEnd(source string, start int) int {
	var quote byte
	for i := start; i < len(source); i++ {
		if quote != 0 {
			if source[i] == quote {
				quote = 0
			}
			continue
		}
		switch source[i] {
		case '\'', '"':
			quote = source[i]
		case '>':
			return i
		}
	}
	return -1
}

func isTextBrowserSuppressedTag(tag string) bool {
	switch tag {
	case "head", "script", "style", "noscript", "svg", "iframe", "template":
		return true
	default:
		return false
	}
}

func isTextBrowserBlockTag(tag string) bool {
	switch tag {
	case "address", "article", "blockquote", "br", "dd", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "table", "td", "th", "tr", "ul":
		return true
	default:
		return false
	}
}

func cleanTextBrowserHTML(raw string) string {
	raw = strings.ReplaceAll(raw, "\u00a0", " ")
	raw = htmlstd.UnescapeString(raw)
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", "\n"), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// textBrowserWrapCache keeps the wrapped page for the text that is currently
// loaded. The page only changes on navigation, so one entry is all it needs.
type textBrowserWrapCacheEntry struct {
	text  string
	width int32
	fontH int32
	lines []string
}

var textBrowserWrapCache struct {
	mu    sync.Mutex
	entry textBrowserWrapCacheEntry
}

// textBrowserWrappedLines returns the page as display lines, wrapping it only
// when the text, the element width or the font size changed.
func textBrowserWrappedLines(text string, font *ttf.Font, width int32) []string {
	var fontH int32
	if font != nil {
		fontH = int32(font.Height())
	}
	cache := &textBrowserWrapCache
	cache.mu.Lock()
	entry := cache.entry
	cache.mu.Unlock()
	// Length first: a page that changed size never pays for a full compare.
	if len(entry.lines) > 0 && entry.width == width && entry.fontH == fontH &&
		len(entry.text) == len(text) && entry.text == text {
		return entry.lines
	}
	lines := strings.Split(wrapTextBrowserContent(text, font, width), "\n")
	cache.mu.Lock()
	cache.entry = textBrowserWrapCacheEntry{text: text, width: width, fontH: fontH, lines: lines}
	cache.mu.Unlock()
	return lines
}

// resetTextBrowserWrapCache drops the cached wrap; the next render pays for
// one fresh wrap. Used by tests.
func resetTextBrowserWrapCache() {
	textBrowserWrapCache.mu.Lock()
	textBrowserWrapCache.entry = textBrowserWrapCacheEntry{}
	textBrowserWrapCache.mu.Unlock()
}

func wrapTextBrowserContent(text string, font *ttf.Font, maxWidth int32) string {
	if font == nil || maxWidth <= 0 {
		return text
	}
	var output []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			output = append(output, "")
			continue
		}
		line := ""
		for _, word := range words {
			candidate := word
			if line != "" {
				candidate = line + " " + word
			}
			width, _, _ := font.SizeUTF8(candidate)
			if line != "" && int32(width) > maxWidth {
				output = append(output, line)
				line = word
			} else {
				line = candidate
			}
		}
		output = append(output, line)
	}
	return strings.Join(output, "\n")
}

// browseSystemInfo gathers system information using gopsutil and formats it
// as a human-readable text block.
func browseSystemInfo(element Element) (result string) {
	// gopsutil can panic on unsupported platforms (e.g. ARM64 Trimui).
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("System info error: %v", r)
		}
	}()
	mode := strings.ToLower(strings.TrimSpace(element.Variable))
	if mode == "" {
		mode = textBrowserModeSummary
	}

	var sb strings.Builder
	sb.WriteString("=== System Information ===\n")

	hostInfo, err := host.Info()
	if err == nil {
		sb.WriteString(fmt.Sprintf("Hostname    : %s\n", hostInfo.Hostname))
		sb.WriteString(fmt.Sprintf("OS          : %s %s\n", hostInfo.OS, hostInfo.Platform))
		sb.WriteString(fmt.Sprintf("Kernel      : %s\n", hostInfo.KernelVersion))
		sb.WriteString(fmt.Sprintf("Uptime      : %s\n", formatUptime(hostInfo.Uptime)))
		sb.WriteString(fmt.Sprintf("Boot Time   : %s\n", time.Unix(int64(hostInfo.BootTime), 0).Format("2006-01-02 15:04:05")))
		sb.WriteString(fmt.Sprintf("Procs       : %d\n", hostInfo.Procs))
	}

	cpuInfo, err := cpu.Info()
	if err == nil && len(cpuInfo) > 0 {
		sb.WriteString("\n=== CPU ===\n")
		c := cpuInfo[0]
		sb.WriteString(fmt.Sprintf("Model       : %s\n", c.ModelName))
		sb.WriteString(fmt.Sprintf("Cores       : %d\n", c.Cores))
		sb.WriteString(fmt.Sprintf("MHz         : %.0f\n", c.Mhz))
	}

	vm, err := mem.VirtualMemory()
	if err == nil {
		sb.WriteString("\n=== Memory ===\n")
		sb.WriteString(fmt.Sprintf("Total       : %s\n", formatBytes(vm.Total)))
		sb.WriteString(fmt.Sprintf("Available   : %s\n", formatBytes(vm.Available)))
		sb.WriteString(fmt.Sprintf("Used        : %s (%0.1f%%)\n", formatBytes(vm.Used), vm.UsedPercent))
	}

	parts, err := disk.Partitions(true)
	if err == nil {
		sb.WriteString("\n=== Disk ===\n")
		for _, p := range parts {
			usage, err := disk.Usage(p.Mountpoint)
			if err != nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("%s (%s)\n", p.Mountpoint, p.Fstype))
			sb.WriteString(fmt.Sprintf("  Total  : %s\n", formatBytes(usage.Total)))
			sb.WriteString(fmt.Sprintf("  Used   : %s (%0.1f%%)\n", formatBytes(usage.Used), usage.UsedPercent))
			sb.WriteString(fmt.Sprintf("  Free   : %s\n", formatBytes(usage.Free)))
		}
	}

	if mode == textBrowserModeDetails || mode == textBrowserModeNetwork || mode == textBrowserModeProcesses {
		if mode == textBrowserModeNetwork || mode == textBrowserModeDetails {
			ifaces, err := net.Interfaces()
			if err == nil {
				sb.WriteString("\n=== Network Interfaces ===\n")
				for _, iface := range ifaces {
					if iface.Flags&net.FlagLoopback != 0 {
						continue
					}
					addrs, _ := iface.Addrs()
					var ips []string
					for _, a := range addrs {
						if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
							ips = append(ips, ipnet.IP.String())
						}
					}
					sb.WriteString(fmt.Sprintf("%s: %s (up=%v)\n", iface.Name, strings.Join(ips, ", "), iface.Flags&net.FlagUp != 0))
				}
			}

			ioStats, err := gopsutilnet.IOCounters(true)
			if err == nil {
				sb.WriteString("\n=== Network I/O ===\n")
				for _, s := range ioStats {
					if s.BytesSent == 0 && s.BytesRecv == 0 {
						continue
					}
					sb.WriteString(fmt.Sprintf("%s: sent=%s recv=%s\n", s.Name, formatBytes(s.BytesSent), formatBytes(s.BytesRecv)))
				}
			}
		}

		if mode == textBrowserModeProcesses || mode == textBrowserModeDetails {
			procs, err := process.Processes()
			if err == nil {
				sb.WriteString("\n=== Top Processes ===\n")
				type procEntry struct {
					name string
					mem  float64
					cpu  float64
					pid  int32
				}
				var entries []procEntry
				for _, p := range procs {
					name, _ := p.Name()
					memPercent, _ := p.MemoryPercent()
					cpuPercent, _ := p.CPUPercent()
					if name != "" {
						entries = append(entries, procEntry{name: name, mem: float64(memPercent), cpu: float64(cpuPercent), pid: p.Pid})
					}
				}
				limit := 15
				if len(entries) < limit {
					limit = len(entries)
				}
				sb.WriteString(fmt.Sprintf("%-8s %-10s %-10s %s\n", "PID", "CPU%", "MEM%", "NAME"))
				sb.WriteString(strings.Repeat("-", 50) + "\n")
				for _, e := range entries[:limit] {
					sb.WriteString(fmt.Sprintf("%-8d %-10.1f %-10.1f %s\n", e.pid, e.cpu, e.mem, e.name))
				}
			}
		}
	}

	return sb.String()
}

// browseZeroconfServices performs an mDNS/Bonjour discovery scan using the
// zeroconf library and returns formatted results.
func browseZeroconfServices() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Zeroconf error: %v", r)
		}
	}()
	return browseZeroconfServicesWithTimeout(4 * time.Second)
}

// browseZeroconfServicesWithTimeout performs a real zeroconf scan with a
// timeout and returns the discovered services as formatted text.
func browseZeroconfServicesWithTimeout(timeout time.Duration) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Zeroconf error: %v", r)
		}
	}()
	var sb strings.Builder
	sb.WriteString("=== Zeroconf / mDNS Discovery ===\n")
	sb.WriteString(fmt.Sprintf("Scanning for services (%s)...\n\n", timeout))

	serviceTypes := []string{
		"_http._tcp.local.",
		"_ssh._tcp.local.",
		"_smb._tcp.local.",
		"_airplay._tcp.local.",
		"_googlecast._tcp.local.",
		"_raop._tcp.local.",
		"_spotify-connect._tcp.local.",
		"_workstation._tcp.local.",
		"_printer._tcp.local.",
		"_ftp._tcp.local.",
		"_sftp-ssh._tcp.local.",
	}

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return fmt.Sprintf("Zeroconf resolver error: %v\n", err)
	}

	entries := make(chan *zeroconf.ServiceEntry)
	done := make(chan bool)
	var foundEntries []*zeroconf.ServiceEntry

	go func(results <-chan *zeroconf.ServiceEntry, done chan<- bool, found *[]*zeroconf.ServiceEntry) {
		for entry := range results {
			*found = append(*found, entry)
		}
		done <- true
	}(entries, done, &foundEntries)

	for _, st := range serviceTypes {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err = resolver.Browse(ctx, st, "", entries)
		cancel()
		if err != nil {
			continue
		}
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
		}
	}

	close(entries)
	select {
	case <-done:
	case <-time.After(timeout):
	}

	if len(foundEntries) > 0 {
		seen := make(map[string]bool)
		for _, e := range foundEntries {
			key := fmt.Sprintf("%s:%s:%d", e.Service, e.Instance, e.Port)
			if seen[key] {
				continue
			}
			seen[key] = true
			sb.WriteString(fmt.Sprintf("  %s (%s)\n", e.Service, e.Instance))
			for _, ip := range e.AddrIPv4 {
				sb.WriteString(fmt.Sprintf("    IPv4: %s:%d\n", ip, e.Port))
			}
			if len(e.Text) > 0 {
				txtMap := make(map[string]string, len(e.Text))
				for _, t := range e.Text {
					if idx := strings.Index(t, "="); idx >= 0 {
						txtMap[t[:idx]] = t[idx+1:]
					} else {
						txtMap[t] = ""
					}
				}
				sb.WriteString(fmt.Sprintf("    TXT: %s\n", formatTextRecords(txtMap)))
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("No services discovered.\n")
		sb.WriteString("Ensure avahi-daemon is running and devices are on the same network.\n")
	}

	return sb.String()
}

// browseJSONContent fetches JSON from a URL or uses a local variable, then
// extracts and displays it using gjson paths.
func browseJSONContent(element Element) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("JSON error: %v", r)
		}
	}()
	var sb strings.Builder
	sb.WriteString("=== JSON Browser ===\n")

	url := strings.TrimSpace(element.Variable)
	if url == "" {
		url = strings.TrimSpace(element.Text)
	}
	if url == "" {
		return "No URL or JSON data specified.\nSet the element Variable to a URL or JSON string."
	}

	var jsonData string
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		data, err := fetchURL(url, 10*time.Second)
		if err != nil {
			return fmt.Sprintf("Failed to fetch URL:\n%s\n%s", url, err.Error())
		}
		jsonData = data
		sb.WriteString(fmt.Sprintf("Source: %s\n\n", url))
	} else {
		jsonData = url
		sb.WriteString("Source: inline variable\n\n")
	}

	if !gjson.Valid(jsonData) {
		return "Invalid JSON data."
	}

	path := strings.TrimSpace(element.JsonPath)
	if path == "" {
		path = "."
	}

	jsonResult := gjson.Get(jsonData, path)
	if !jsonResult.Exists() {
		return fmt.Sprintf("Path '%s' not found in JSON.\n\nRaw JSON (first 500 chars):\n%s", path, truncate(jsonData, 500))
	}

	switch jsonResult.Type {
	case gjson.JSON:
		sb.WriteString(jsonResult.Raw)
	case gjson.String:
		sb.WriteString(jsonResult.String())
	default:
		sb.WriteString(jsonResult.Raw)
	}

	return sb.String()
}

// fetchURL is a simple HTTP GET helper with timeout.
func fetchURL(url string, timeout time.Duration) (string, error) {
	return fetchURLContext(context.Background(), url, timeout)
}

// fetchURLContext is fetchURL tied to a caller's context. Scene work uses it so
// leaving the screen closes the request instead of waiting out the timeout.
func fetchURLContext(ctx context.Context, url string, timeout time.Duration) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// Cap the body: a hostile or runaway server must not be able to exhaust the
	// handheld's memory. Reading one byte past the cap tells "exactly at" from
	// "over", and going over is an error rather than a silently cut document.
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchURLMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > fetchURLMaxBytes {
		return "", fmt.Errorf("response is larger than %d MiB", fetchURLMaxBytes>>20)
	}
	return string(data), nil
}

// fetchURLMaxBytes bounds one fetchURL response (pages, JSON, package zips).
const fetchURLMaxBytes = 64 << 20

// formatUptime converts seconds to a human-readable duration.
func formatUptime(seconds uint64) string {
	d := int64(seconds) / 86400
	h := (int64(seconds) % 86400) / 3600
	m := (int64(seconds) % 3600) / 60
	return fmt.Sprintf("%dd %dh %dm", d, h, m)
}

// formatBytes converts bytes to human-readable form.
func formatBytes(b uint64) string {
	f := float64(b)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	idx := 0
	for f >= 1024 && idx < len(units)-1 {
		f /= 1024
		idx++
	}
	return fmt.Sprintf("%.1f %s", f, units[idx])
}

// formatTextRecords formats zeroconf TXT records for display.
func formatTextRecords(records map[string]string) string {
	var parts []string
	for k, v := range records {
		if v == "" {
			parts = append(parts, k)
		} else {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

// truncate limits a string to maxLen characters (runes), appending "..." if truncated.
// Unlike ellipsize, this operates on character count rather than rendered width,
// making it suitable for log messages, labels, and non-rendered text.
// Handles UTF-8 correctly by counting runes, not bytes.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	// Count runes to handle UTF-8 properly
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// ---------------------------------------------------------------------------
// System info helpers (gopsutil-backed)
// ---------------------------------------------------------------------------

// getGPSUtilization returns a simple GPU info string if available via
// gopsutil. On many handheld devices GPU info is exposed through nvidia-smi,
// intel_gpu_top, or similar tools.
func getGPSUtilization() string {
	var candidates []string
	switch runtime.GOOS {
	case "linux":
		candidates = []string{
			"nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader",
			"intel_gpu_top -s",
		}
	case "windows":
		candidates = []string{
			"nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader",
		}
	}

	for _, cmd := range candidates {
		out, err := exec.Command("sh", "-c", cmd).Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}
	return "N/A"
}

// getRunningServices returns common running services/daemons on Linux.
func getRunningServices() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Services error: %v", r)
		}
	}()
	if runtime.GOOS != "linux" {
		return "N/A (not Linux)"
	}

	out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=running", "--no-pager", "--plain").Output()
	if err != nil {
		return "N/A"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Running services (%d):\n", len(lines)-1))
	for i, line := range lines {
		if i == 0 || i >= 20 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			sb.WriteString("  " + fields[0] + "\n")
		}
	}
	if len(lines) > 21 {
		sb.WriteString(fmt.Sprintf("  ... and %d more\n", len(lines)-21))
	}
	return sb.String()
}

// getSystemTemperature returns a temperature summary.
//
// This used to call gopsutil's host.SensorsTemperatures, which the v4 release
// removed (its Linux implementation needed cgo). It now reads the kernel's
// thermal zones directly, which is what the handheld hardware panels already
// do (see getCPUTempC in hardware.go).
func getSystemTemperature() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Temp error: %v", r)
		}
	}()
	var sb strings.Builder
	sb.WriteString("=== Temperatures ===\n")

	if runtime.GOOS != "linux" {
		sb.WriteString("No sensor data available.\n")
		return sb.String()
	}
	zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*")
	if err != nil || len(zones) == 0 {
		sb.WriteString("No sensor data available.\n")
		return sb.String()
	}
	for _, zone := range zones {
		name, _ := os.ReadFile(filepath.Join(zone, "type"))
		data, err := os.ReadFile(filepath.Join(zone, "temp"))
		if err != nil {
			continue
		}
		if v, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && v > 0 {
			sb.WriteString(fmt.Sprintf("%s: %.1f C\n", strings.TrimSpace(string(name)), float64(v)/1000.0))
		}
	}

	return sb.String()
}

// getProcessTree returns a simple process tree using ps.
func getProcessTree() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Process error: %v", r)
		}
	}()
	if runtime.GOOS != "linux" {
		return "N/A (not Linux)"
	}
	out, err := exec.Command("ps", "-eo", "pid,ppid,user,%cpu,%mem,cmd", "--sort=-%cpu").Output()
	if err != nil {
		return "N/A"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Processes (%d):\n", len(lines)-1))
	for i, line := range lines {
		if i == 0 || i > 20 {
			continue
		}
		sb.WriteString(line + "\n")
	}
	if len(lines) > 21 {
		sb.WriteString(fmt.Sprintf("... and %d more\n", len(lines)-21))
	}
	return sb.String()
}

// scanLocalFiles scans a local directory and returns formatted entries.
func scanLocalFiles(dir string) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Files error: %v", r)
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("Cannot read directory: %s\n%s", dir, err.Error())
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== %s ===\n", dir))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		prefix := "[DIR] "
		if !e.IsDir() {
			prefix = fmt.Sprintf("[%8s] ", formatBytes(uint64(info.Size())))
		}
		sb.WriteString(fmt.Sprintf("%s%s\n", prefix, e.Name()))
	}
	return sb.String()
}

// startTextBrowserAutoRefresh starts a periodic refresh timer for a textbrowser
// element. Any existing timer for the same variable is stopped first.
func startTextBrowserAutoRefresh(config *Config, element Element) {
	if !element.AutoRefresh || element.Variable == "" || textBrowserElementSource(element) == textBrowserSourceWeb {
		return
	}

	stopTextBrowserAutoRefresh(element.Variable)

	textBrowserAutoRefreshMutex.Lock()
	defer textBrowserAutoRefreshMutex.Unlock()

	timer := time.AfterFunc(textBrowserAutoRefreshInterval, func() {
		textBrowserRefresh(config, element)
		startTextBrowserAutoRefresh(config, element)
	})
	textBrowserAutoRefreshTimers[element.Variable] = timer
}

// stopTextBrowserAutoRefresh stops the auto-refresh timer for a given variable.
func stopTextBrowserAutoRefresh(variable string) {
	textBrowserAutoRefreshMutex.Lock()
	defer textBrowserAutoRefreshMutex.Unlock()

	if timer, ok := textBrowserAutoRefreshTimers[variable]; ok {
		timer.Stop()
		delete(textBrowserAutoRefreshTimers, variable)
	}
}

// stopAllTextBrowserAutoRefresh stops all active auto-refresh timers.
func stopAllTextBrowserAutoRefresh() {
	textBrowserAutoRefreshMutex.Lock()
	defer textBrowserAutoRefreshMutex.Unlock()

	for _, timer := range textBrowserAutoRefreshTimers {
		timer.Stop()
	}
	textBrowserAutoRefreshTimers = make(map[string]*time.Timer)
}

// refreshSceneTextBrowsers starts auto-refresh for all textbrowser elements in
// the current scene that have autoRefresh enabled.
func refreshSceneTextBrowsers(config *Config) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	for _, elem := range config.Scenes[currentSceneIndex].Elements {
		if elem.Type == "textbrowser" && elem.AutoRefresh {
			startTextBrowserAutoRefresh(config, elem)
		}
	}
}
