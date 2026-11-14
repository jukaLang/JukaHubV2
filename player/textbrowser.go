package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmlstd "html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
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
	// A page is allowed no more than this much HTML. The actual limit is
	// chosen per download from MemAvailable: the floor keeps small pages
	// working on a tight device, and the ceiling stops one runaway page from
	// eating the 1 GB handheld's whole heap. Anything past the chosen limit is
	// parsed and drawn as far as it goes, with a note where it was cut.
	textBrowserMinPageBytes = 8 << 20
	textBrowserMaxPageBytes = 64 << 20
	textBrowserMaxTextBytes = 512 << 10
	textBrowserURLBarHeight = int32(36)

	// textBrowserFetchTimeout bounds a page download. A TrimUI on handheld
	// Wi-Fi takes several times longer than a desktop for the same 1.5 MiB of
	// GitHub HTML, and cutting it off at ten seconds showed "Could not load"
	// on pages that were about to arrive. Reload still refetches from scratch.
	textBrowserFetchTimeout = 20 * time.Second
)

// textBrowserFetchTimeoutFor gives a larger page more time to arrive without
// letting every small request sit for a minute. The base timeout covers the
// old 8 MiB budget; each MiB above it adds a second, so the 64 MiB ceiling gets
// 76 seconds while a tight device's 8 MiB page keeps the 20-second bound.
func textBrowserFetchTimeoutFor(limit int) time.Duration {
	mib := limit >> 20
	if mib <= 8 {
		return textBrowserFetchTimeout
	}
	return textBrowserFetchTimeout + time.Duration(mib-8)*time.Second
}

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

// browserNavigation is one top-level request. POST bodies live only for this
// load; history, the address bar and the page cache retain just URL strings.
type browserNavigation struct {
	URL         string
	Method      string
	Body        []byte
	ContentType string
	Referrer    string
}

// browserPageFetcher loads one page. It is a variable so the browser's load
// path (cache, stale-request guard, history) can be tested without a network.
// The second argument is the load's script-pass generation, read on the main
// thread when the load began (webJSLoadGeneration).
//
// A load runs on its own goroutine, so the seam is read under a lock: assigning
// the variable directly races with a fetch that is still on the wire, which the
// race detector reports as soon as a page load outlives the test that started
// it.
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
func currentBrowserPageFetcher() func(browserNavigation, uint64) browserPage {
	browserPageFetcherMu.RLock()
	defer browserPageFetcherMu.RUnlock()
	return browserPageFetcher
}

var textBrowserRequestMutex sync.Mutex
var textBrowserAddressEditing bool
var textBrowserAddressSubmitted bool

// The Search shortcut types into its own buffer: the address bar keeps the
// page's URL, so cancelling the keyboard leaves the browser where it was.
var textBrowserSearchEditing bool
var textBrowserSearchSubmitted bool

// Find-in-page uses its own buffer and flags for the same reason: a search
// *within* the page on screen must never touch the address bar or navigate.
var textBrowserFindEditing bool
var textBrowserFindSubmitted bool

// browserLandedURL carries "this load started at A and ended at B" from the
// fetch goroutine to the render pass. The address bar can only be rewritten
// there: the reader may be typing a new address, and a background load must
// never replace it.
var browserLandedURL struct {
	mu           sync.Mutex
	from, target string
}

// browserRememberLandedURL records where a redirected load ended.
func browserRememberLandedURL(from, target string) {
	if from == "" || target == "" || from == target {
		return
	}
	browserLandedURL.mu.Lock()
	browserLandedURL.from, browserLandedURL.target = from, target
	browserLandedURL.mu.Unlock()
}

// browserTakeLandedURL returns and clears the pending correction.
func browserTakeLandedURL() (string, string) {
	browserLandedURL.mu.Lock()
	defer browserLandedURL.mu.Unlock()
	from, target := browserLandedURL.from, browserLandedURL.target
	browserLandedURL.from, browserLandedURL.target = "", ""
	return from, target
}

// browserSyncLandedURL moves the address bar and the current history entry onto
// the URL a redirect landed on, which is the page actually on screen. It runs
// from the render pass so it can see whether the address is being edited: an
// address the reader is typing is theirs to finish.
func browserSyncLandedURL(config *Config) {
	if config == nil || textBrowserAddressEditing {
		return
	}
	from, target := browserTakeLandedURL()
	if from == "" || target == "" {
		return
	}
	if textBrowserAddress(config) != from {
		// The reader navigated somewhere else in the meantime; the correction
		// describes a page that is no longer on screen.
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[textBrowserURLVariable] = target
	browserReplaceURL(from, target)
}

// renderTextBrowser renders a scrollable text panel, including readable web pages.
func renderTextBrowser(renderer *sdl.Renderer, config *Config, element Element) {
	defer func() {
		if r := recover(); r != nil {
			logError("textbrowser", "recovered panic in render: %v", r)
		}
	}()
	// The Forget chip carries the saved-session count, so the toolbar answers
	// "am I still signed in somewhere?" without a press. The count only changes
	// when a page stores a cookie or Forget drops them, so syncing it once per
	// frame here (the web panel is the Browser scene) keeps it honest for every
	// route that can change it — link click, address bar, reload, auto-refresh.
	if textBrowserElementSource(element) == textBrowserSourceWeb {
		browserSyncForgetChip(config)
		// Save and Marks carry live state the same way: whether the page on
		// screen is already saved, and how many pages the list holds.
		browserSyncBookmarkChips(config)
		// A page a redirect moved lands under the URL it actually came from.
		browserSyncLandedURL(config)
		// A load that ended on a YouTube video plays it in Media, and coming
		// back from the player puts the page where it was (browser_youtube.go).
		brwTakeLanding(config, element)
		brwRestoreReturn(config)
	}

	elemW := getElementWidth(element, 1100)
	elemH := getElementHeight(element, 480)
	// The full-screen Browser draws its page edge to edge: no panel, header
	// band, toolbar or cookie strip around it (brw_fullscreen.go).
	full := brwFullscreenElem(element)
	if !full {
		drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(235), accentColor)
	}

	text := ""
	if element.Variable != "" {
		if v, ok := config.Variables.Custom[element.Variable].(string); ok {
			text = v
		}
	}
	if text == "" {
		if full {
			text = "Press " + hintKeyFor("Select / M", "Menu") + " for the menu, then Go to address."
		} else if textBrowserElementSource(element) == textBrowserSourceWeb {
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
	// Cleared per frame so a panel that is not the web one (or a web panel with
	// no room for the strip) cannot leave a stale tap target behind.
	browserCookieStripHit = sdl.Rect{}

	headerH := int32(28)
	if full {
		headerH = 0
	}
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
	// The strip under the page names the sites the saved cookies belong to, so
	// the count on the Forget chip has a host attached. Its height is reserved
	// for the whole session rather than only while cookies exist, so the page
	// does not reflow the moment a login cookie arrives.
	cookieStripH := int32(0)
	cookieFont, _ := getCachedFont(config, "small")
	if source == textBrowserSourceWeb && cookieFont != nil && !full {
		cookieStripH = int32(cookieFont.Height()) + 8
	}
	contentH := elemH - headerH - 8
	if source == textBrowserSourceWeb {
		if !full {
			contentH = elemH - headerH - textBrowserURLBarHeight - 12 - cookieStripH
		}
		browserCookieStripReserved = cookieStripH
	}
	maxLines := int((contentH) / lineH)
	var lines []string
	var styledDoc *webDocument
	var styledLayout *webLayout
	totalLines := 0
	// A page that is still on the wire has no document yet; the renderer
	// shows a loading card instead of a one-line text stub.
	loading := source == textBrowserSourceWeb && strings.HasPrefix(text, "Loading ")
	// A failed load (the offline card) or JukaLand owns the page area: the
	// header then has no line count to report (brw_offline.go).
	pageOwned := false
	if source == textBrowserSourceWeb {
		_, failed := brwFailureFor(element.Variable)
		pageOwned = failed || (brwGame.active && brwGame.variable == element.Variable)
	}
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

	// Full screen the page's own paper is the ground, edge to edge, in place of
	// the panel; the header band below is not drawn at all.
	if full {
		brwDrawFullscreenPaper(renderer, element, styledDoc, styledLayout != nil && !pageOwned)
	}

	// The header is Home's glass band with the source named in its own colour.
	// It used to be a saturated slab with white type: white on the cyan WEB band
	// was hard to read, and on a light preset the strip was the loudest thing on
	// the screen. The 8px corner also keeps it inside the panel's rounding.
	if !full {
		fillRoundedRect(renderer, element.X+4, headerY, elemW-8, headerH, 8, glassA(14))
		strokeRoundedRect(renderer, element.X+4, headerY, elemW-8, headerH, 8, 1, glassEdge())
	}
	labelFont, _ := getCachedFont(config, "small")
	if labelFont != nil && !full {
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
		renderText(renderer, config, labelFont, sourceLabel, TextInk(headerColor), element.X+12, textY)

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
			renderText(renderer, config, labelFont, ts, ColorTextMuted(), tsX, textY)
			rightEdge = tsX - 12
		}
		// A zoomed page names its scale on the toolbar chip (see the zoom
		// chip below), which is the one place the reader both reads and
		// changes it. The header stays free for the page's own status.
		if text != "" && totalLines > 0 && !loading && !pageOwned {
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
			renderText(renderer, config, labelFont, lineInfo, ColorTextMuted(), rightEdge, textY)
			rightEdge -= 12
		}
		if styledDoc != nil {
			title := strings.TrimSpace(styledDoc.Title)
			if title != "" {
				left := element.X + 12 + int32(lw) + 10
				if avail := rightEdge - left; avail > 40 {
					renderText(renderer, config, labelFont, ellipsize(labelFont, title, avail), ColorTextPrimary(), left, textY)
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
			ink := glassInk()
			for i := 0; i < 8; i++ {
				a := float64(i) * 45.0 * math.Pi / 180.0
				x := spinnerX + int32(math.Cos(angle+a)*6)
				y := spinnerY + int32(math.Sin(angle+a)*6)
				alpha := uint8(255 - (i * 28))
				renderer.SetDrawColor(ink.R, ink.G, ink.B, alpha)
				renderer.FillRect(&sdl.Rect{X: x, Y: y, W: 2, H: 2})
			}
		}
	}

	contentTop := element.Y + 8 + headerH
	if full {
		// The page starts 4px below the screen's top edge (contentTop+4 below);
		// what the toolbar did is in the Select menu (brw_menu.go).
		contentTop = element.Y
	} else if source == textBrowserSourceWeb {
		addressRect, modeRect, searchRect, goRect, filterRect, zoomRect := textBrowserToolbarLayout(config, element)
		backRect, forwardRect := textBrowserHistoryRects(config, element)
		drawBrowserHistoryIcon(renderer, backRect, -1, browserHistoryAvailable(-1))
		drawBrowserHistoryIcon(renderer, forwardRect, 1, browserHistoryAvailable(1))
		// The toolbar is Home's glass: the address field and the quiet chips
		// are glass pills with the footer badges' edge, an active chip is an
		// accent wash. The navy and slate fills they had were dark-theme-only
		// slabs that did not match anything else on screen.
		fillRoundedRect(renderer, addressRect.X, addressRect.Y, addressRect.W, addressRect.H, 8, glassA(16))
		strokeRoundedRect(renderer, addressRect.X, addressRect.Y, addressRect.W, addressRect.H, 8, 1, glassChipEdge())
		address := textBrowserAddress(config)
		if styledDoc != nil && styledDoc.URL != "" {
			address = styledDoc.URL // the final URL after redirects, not the requested URL
		}
		if labelFont != nil {
			// The padlock opens Site info (brw_siteinfo.go): a lock for https,
			// an open lock for http, an info mark while nothing is loaded.
			lock := brwPadlockRect(config, element)
			brwDrawPadlock(renderer, lock, styledDoc)
			icon := sdl.Rect{X: addressRect.X + 6, Y: addressRect.Y + (addressRect.H-20)/2, W: 20, H: 20}
			if lock.W > 0 {
				icon.X = lock.X + lock.W + 6
			}
			drawBrowserFavicon(renderer, config, styledDoc, icon)
			textX := icon.X + icon.W + 6
			avail := addressRect.X + addressRect.W - textX - 8
			renderWithClip(renderer, textX, addressRect.Y, avail, addressRect.H, func(r *sdl.Renderer) {
				display := ellipsize(labelFont, address, avail)
				y := addressRect.Y + (addressRect.H-int32(labelFont.Height()))/2
				if styledDoc != nil && browserHTTPSAddress(styledDoc.URL) && strings.HasPrefix(display, "https://") {
					prefix := "https://"
					pw, _, _ := labelFont.SizeUTF8(prefix)
					renderText(r, config, labelFont, prefix, SuccessInk(), textX, y)
					renderText(r, config, labelFont, strings.TrimPrefix(display, prefix), ColorTextPrimary(), textX+int32(pw), y)
				} else {
					renderText(r, config, labelFont, display, ColorTextPrimary(), textX, y)
				}
			})
			if searchRect.W > 0 {
				fillRoundedRect(renderer, searchRect.X, searchRect.Y, searchRect.W, searchRect.H, 8, glassChipFill())
				strokeRoundedRect(renderer, searchRect.X, searchRect.Y, searchRect.W, searchRect.H, 8, 1, WithAlpha(accentColor, 170))
				sw, sh, _ := labelFont.SizeUTF8("SEARCH")
				renderText(renderer, config, labelFont, "SEARCH", WithAlpha(accentColor, 235), searchRect.X+(searchRect.W-int32(sw))/2, searchRect.Y+(searchRect.H-int32(sh))/2)
			}
			tw, th, _ := labelFont.SizeUTF8("GO")
			// The label takes the accent's contrast ink: white on Home's cyan
			// was about 1.7:1.
			fillRoundedRect(renderer, goRect.X, goRect.Y, goRect.W, goRect.H, 8, accentColor)
			renderText(renderer, config, labelFont, "GO", contrastText(accentColor), goRect.X+(goRect.W-int32(tw))/2, goRect.Y+(goRect.H-int32(th))/2)
			if filterRect.W > 0 {
				// The chip is the page's filter state and its count at once:
				// amber says this site is excepted, accent says the load
				// skipped something, and neutral means nothing was in the way.
				view := browserAdBlockViewFor(element.Variable)
				label := ellipsize(labelFont, browserAdBlockLabel(view), filterRect.W-8)
				fill := glassChipFill()
				ink := ColorTextSecondary()
				border := glassChipEdge()
				switch {
				case view.Off:
					fill = WithAlpha(ColorWarning, 45)
					ink = WarningInk()
					border = WithAlpha(ColorWarning, 190)
				case view.Blocked > 0:
					fill = WithAlpha(accentColor, 45)
					ink = WithAlpha(accentColor, 240)
					border = WithAlpha(accentColor, 170)
				}
				fillRoundedRect(renderer, filterRect.X, filterRect.Y, filterRect.W, filterRect.H, 8, fill)
				strokeRoundedRect(renderer, filterRect.X, filterRect.Y, filterRect.W, filterRect.H, 8, 1, border)
				fw, fh, _ := labelFont.SizeUTF8(label)
				renderText(renderer, config, labelFont, label, ink,
					filterRect.X+(filterRect.W-int32(fw))/2, filterRect.Y+(filterRect.H-int32(fh))/2)
			}
			if modeRect.W > 0 {
				// The chip is the D-pad's current job, so switching modes is
				// visible without waiting for a toast: accent means the pad walks
				// links, a quiet chip means it scrolls.
				label := textBrowserModeChipLabel()
				fill := glassChipFill()
				ink := ColorTextSecondary()
				border := glassChipEdge()
				if browserNav == browserModeLinks {
					fill = accentColor
					ink = contrastText(fill)
					border = WithAlpha(accentColor, 220)
				}
				fillRoundedRect(renderer, modeRect.X, modeRect.Y, modeRect.W, modeRect.H, 8, fill)
				strokeRoundedRect(renderer, modeRect.X, modeRect.Y, modeRect.W, modeRect.H, 8, 1, border)
				mw, mh, _ := labelFont.SizeUTF8(label)
				renderText(renderer, config, labelFont, label, ink,
					modeRect.X+(modeRect.W-int32(mw))/2, modeRect.Y+(modeRect.H-int32(mh))/2)
			}
			if zoomRect.W > 0 {
				// The zoom chip names the reader's text scale and steps it on a
				// tap. Accent when it is off 100% (so a scaled page is visibly
				// a setting, not the site), quiet at normal size.
				label := browserZoomLabel()
				fill := glassChipFill()
				ink := ColorTextSecondary()
				border := glassChipEdge()
				if browserZoomScale() != 1.0 {
					fill = WithAlpha(accentColor, 45)
					ink = WithAlpha(accentColor, 240)
					border = WithAlpha(accentColor, 170)
				}
				fillRoundedRect(renderer, zoomRect.X, zoomRect.Y, zoomRect.W, zoomRect.H, 8, fill)
				strokeRoundedRect(renderer, zoomRect.X, zoomRect.Y, zoomRect.W, zoomRect.H, 8, 1, border)
				zw, zh, _ := labelFont.SizeUTF8(label)
				renderText(renderer, config, labelFont, label, ink,
					zoomRect.X+(zoomRect.W-int32(zw))/2, zoomRect.Y+(zoomRect.H-int32(zh))/2)
			}
		}
		contentTop += textBrowserURLBarHeight + 4
	}
	y := contentTop
	if full {
		// A plain-text page gets the margin the header band used to give it.
		y += 6
	}

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
	// A Back/Forward/Reload restore fires here: on the first frame the styled
	// page (and with it a real scrollMax) is actually drawn, the reader is put
	// back where they left that page. While the page is still on the wire
	// styledLayout is nil and the scroll stays at the top, which is correct —
	// there is nothing to place yet.
	if styledLayout != nil {
		browserScrollPendingApply(textBrowserAddress(config), scrollMax)
	}

	startIdx := int(textBrowserScrollY)
	endIdx := startIdx + maxLines
	if endIdx > totalLines {
		endIdx = totalLines
	}
	if startIdx < 0 {
		startIdx = 0
	}

	// The offline card (or JukaLand, played from it) takes the whole page area
	// and the cookie strip under it; nothing of the page is drawn under them.
	if source == textBrowserSourceWeb && brwDrawPageState(renderer, config, element,
		sdl.Rect{X: element.X + 7, Y: contentTop + 4, W: elemW - 14, H: contentH}, cookieStripH) {
		if full {
			// The card names no site: the status stack only lets go of the
			// targets the last frame drew.
			brwDrawFullscreenStatus(renderer, config, element, nil, true)
		}
		return
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
		// Then the in-page search's own marks, so the current match is never
		// covered by a link outline drawn over it.
		browserDrawPageFind(renderer, element, styledLayout, int(textBrowserScrollY), contentTop+4, contentH)
	} else if loading {
		drawTextBrowserLoading(renderer, config, element, contentTop, contentH, accentColor)
	} else {
		for i := startIdx; i < endIdx; i++ {
			ln := lines[i]
			if strings.HasPrefix(ln, "===") || strings.HasPrefix(ln, "---") {
				renderTextRaw(renderer, config, font, ln, accentColor, element.X+10, y)
			} else if source != textBrowserSourceWeb && strings.Contains(ln, ":") && !strings.HasPrefix(ln, " ") {
				parts := strings.SplitN(ln, ":", 2)
				if len(parts) == 2 {
					renderText(renderer, config, font, parts[0]+":", ColorTextSecondary(), element.X+10, y)
					renderText(renderer, config, font, parts[1], ColorTextPrimary(), element.X+10+int32(len(parts[0])*12)+20, y)
				} else {
					renderTextRaw(renderer, config, font, ln, ColorTextPrimary(), element.X+10, y)
				}
			} else {
				renderTextRaw(renderer, config, font, ln, ColorTextPrimary(), element.X+10, y)
			}
			y += lineH
		}
	}

	// The cookie strip: a hairline and one quiet line, the same chrome language
	// as the toolbar above the page. It names hosts, not addresses — a host is
	// what the jar keys a session on, and it is what the user recognises.
	if cookieStripH > 0 && cookieFont != nil {
		sites := browserCookieSites()
		stripY := contentTop + contentH
		hairline := glassHairline()
		renderer.SetDrawColor(hairline.R, hairline.G, hairline.B, hairline.A)
		renderer.FillRect(&sdl.Rect{X: element.X + 12, Y: stripY, W: elemW - 24, H: 1})
		ink := ColorTextTertiary()
		if len(sites) > 0 {
			ink = ColorTextSecondary()
		}
		line := ellipsize(cookieFont, browserCookieSitesLine(sites), elemW-24)
		renderText(renderer, config, cookieFont, line, ink, element.X+12, stripY+4)
		// Tapping the line opens the site picker, so the line that names the
		// hosts is also the way to act on one. The rect is the strip's own band,
		// not the text's, so a tap on empty space beside a short line still lands
		// (the line is the only thing there). It is clamped to the panel: a styled
		// page rounds the content height down to whole rows, which can leave the
		// band hanging a pixel or two past the panel's edge, and a tap target that
		// reaches outside the panel would swallow a tap on the chip row under it.
		hitTop := stripY - 1
		hitH := cookieStripH + 2
		if bottom := element.Y + elemH; hitTop+hitH > bottom {
			hitH = bottom - hitTop
		}
		if hitH > 0 {
			browserCookieStripHit = sdl.Rect{
				X: element.X + 8,
				Y: hitTop,
				W: elemW - 16,
				H: hitH,
			}
		}
	}

	// The shared glass scrollbar, beside the page itself. It used to start
	// under the header whatever the source, so on the web panel its top ran
	// down the side of the toolbar, and its pale track was the one light bar
	// on a dark screen.
	if scrollMax > 0 && totalLines > 0 {
		barY := contentTop
		if source == textBrowserSourceWeb {
			barY += 4
		}
		drawScrollbar(renderer, element.X+elemW-10, barY, 4, contentH,
			float64(maxLines)/float64(totalLines), float64(textBrowserScrollY)/float64(scrollMax))
	}

	// A running find names itself in the top-right of the page, so the reader
	// can see which match they are on without leaving the text. It is drawn
	// last: a pill over the page is chrome, and the page's own highlights must
	// not paint over it. Full screen it is one entry of the page's status stack,
	// under the site pill and over the link-mode chip (brw_fullscreen.go).
	if full {
		brwDrawFullscreenStatus(renderer, config, element, styledDoc, loading || styledLayout == nil)
	} else if textBrowserElementSource(element) == textBrowserSourceWeb && browserFind.active {
		browserDrawFindStatus(renderer, config, element)
	}
}

// browserDrawFindStatus paints the in-page search's status pill: the query and
// how many times it matches, with the reader's place in that count. It sits at
// the top-right of the page area, which is the corner the reader's eye already
// uses for the line counter in the header. The pill is clamped to the panel so
// a long query cannot run off the edge of the screen.
func browserDrawFindStatus(renderer *sdl.Renderer, config *Config, element Element) {
	label := browserFindChipLabel()
	if label == "" {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	elemW := getElementWidth(element, 1100)
	padX := int32(10)
	tw, th, err := font.SizeUTF8(label)
	if err != nil || tw <= 0 {
		return
	}
	pillW := int32(tw) + padX*2
	pillH := int32(th) + 8
	// Top-right of the content area, just under the toolbar.
	pillX := element.X + elemW - 8 - pillW
	if limit := element.X + elemW - 8; pillX+pillW > limit {
		pillX = limit - pillW
	}
	pillY := element.Y + 8 + 28 + textBrowserURLBarHeight + 6

	fillRoundedRect(renderer, pillX, pillY, pillW, pillH, 6, WithAlpha(ColorPanelRaised, 235))
	strokeRoundedRect(renderer, pillX, pillY, pillW, pillH, 6, 1, WithAlpha(ColorWarning, 200))
	renderText(renderer, config, font, label, ColorTextPrimary(), pillX+padX, pillY+4)
}

// drawTextBrowserLoading paints the placeholder shown while a page is still on
// the wire: a glass card naming the address, with three breathing dots and a
// sweeping bar, so a slow page reads as work in progress instead of an empty
// panel. Everything moves off the frame clock, so nothing has to be updated
// from the network goroutine.
func drawTextBrowserLoading(renderer *sdl.Renderer, config *Config, element Element, top, height int32, accent sdl.Color) {
	if renderer == nil || height < 90 {
		return
	}
	fullW := getElementWidth(element, 1100)
	panelW := min(max(fullW-80, int32(240)), int32(560))
	x := element.X + (fullW-panelW)/2
	cardH := int32(112)
	y := top + (height-cardH)/2

	fillRoundedRect(renderer, x, y, panelW, cardH, 20, glassSheetFill())
	strokeRoundedRect(renderer, x, y, panelW, cardH, 20, 1, glassSheetEdge())
	drawOverlayCardAccent(renderer, x, y, panelW)

	now := sdl.GetTicks64()
	phase := float64(now%1200) / 1200.0 * 2 * math.Pi
	dotX := x + panelW - 72
	for i := 0; i < 3; i++ {
		pulse := 0.5 + 0.5*math.Sin(phase-float64(i)*0.7)
		fillRoundedRect(renderer, dotX+int32(i*16), y+24, 10, 10, 5, WithAlpha(accent, uint8(90+150*pulse)))
	}

	// An indeterminate bar along the bottom: a short accent segment sweeping
	// across a hairline track.
	track := sdl.Rect{X: x + 22, Y: y + cardH - 18, W: panelW - 44, H: 4}
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, 2, glassA(22))
	seg := track.W / 4
	sweep := float64(now%1500) / 1500.0
	segX := track.X - seg + int32(sweep*float64(track.W+seg))
	left, right := max(segX, track.X), min(segX+seg, track.X+track.W)
	if right > left {
		fillRoundedRect(renderer, left, track.Y, right-left, track.H, 2, accent)
	}

	titleFont := FontForRole(config, TypeBody)
	labelFont := FontForRole(config, TypeLabel)
	if titleFont == nil || labelFont == nil {
		return
	}
	renderText(renderer, config, titleFont, "Loading page", ColorTextPrimary(), x+22, y+18)
	address := ellipsize(labelFont, textBrowserAddress(config), panelW-44)
	renderText(renderer, config, labelFont, address, ColorTextSecondary(), x+22, y+18+int32(titleFont.Height())+6)
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
	// The three widths below are fallbacks for the one moment no font exists
	// yet (a layout before the first frame); every real frame sizes the chips
	// from the labels they draw — see textBrowserToolbarLayout.
	textBrowserGoWidth     = int32(64)
	textBrowserSearchWidth = int32(84)
	textBrowserModeWidth   = int32(80)
	textBrowserZoomWidth   = int32(56)
	textBrowserBarGap      = int32(8)
	// textBrowserChipPad is the breathing room either side of a chip's label
	// when the width is measured rather than guessed.
	textBrowserChipPad = int32(8)
)

// textBrowserModeChipLabel is the D-pad mode in one word: the toolbar chip and
// the toast that announces the switch have to agree, or the chip becomes a
// second, contradicting source of truth.
func textBrowserModeChipLabel() string {
	if browserNav == browserModeLinks {
		return "LINKS"
	}
	return "SCROLL"
}

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
func textBrowserToolbarRects(config *Config, element Element) (address, goButton sdl.Rect) {
	address, _, _, goButton, _, _ = textBrowserToolbarLayout(config, element)
	return
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

func textBrowserToolbarLayout(config *Config, element Element) (address, mode, search, goButton, filter, zoom sdl.Rect) {
	width := getElementWidth(element, 1100)
	y := element.Y + 36
	h := textBrowserURLBarHeight - 4
	historyW := int32(0)
	if width >= 600 {
		historyW = 2 * (h + textBrowserBarGap)
	}
	width -= historyW
	// Chips are sized from the labels they will draw, measured in the same
	// "small" font the renderer draws them with — not from fixed pixels. The
	// old constants were narrower than their own labels ("SEARCH" is 97px and
	// "SCROLL" 94px at the 1280 type scale, against 84/80px chips), so both
	// words spilled past the chip's border and read as broken chrome; and
	// fixed pixels do not follow the canvas, so on the 960 panel they ate a
	// disproportionate share of the row and truncated URLs the Smart Pro shows
	// in full. A measured chip keeps its padding at every canvas and at every
	// accessibility text-size step, and it shrinks on compact panels in step
	// with the labels, which scale with the panel. The mode chip is sized for
	// "SCROLL", the widest of its three words, so switching modes does not
	// resize the row under the user's finger.
	labelFont, _ := getCachedFont(config, "small")
	chipWidth := func(label string, fallback int32) int32 {
		if labelFont == nil {
			return fallback
		}
		if tw, _, err := labelFont.SizeUTF8(label); err == nil && tw > 0 {
			return int32(tw) + 2*textBrowserChipPad
		}
		return fallback
	}
	goW := chipWidth("GO", textBrowserGoWidth)
	searchW := chipWidth("SEARCH", textBrowserSearchWidth)
	modeW := chipWidth("SCROLL", textBrowserModeWidth)
	// The zoom chip is sized for its widest word ("160%") so cycling steps
	// does not resize the row under the user's finger.
	zoomW := chipWidth("160%", textBrowserZoomWidth)
	// Full row: address, Search, Go, filter, mode, zoom. A narrow panel gives
	// up controls in order of what has another way in: the zoom chip first
	// (the keyboard's -/+ and the L2/R2 triggers already step it, and the
	// header names the scale), then Search (Y opens it, and the address bar
	// still takes typed queries), then the filter chip, then the mode chip —
	// which is last because it is the only control that explains what the
	// D-pad will do.
	filterW := textBrowserFilterWidth(width)
	addressW := width - goW - searchW - filterW - modeW - zoomW - textBrowserBarGap*5 - 16
	showZoom, showSearch, showFilter, showMode := true, true, true, true
	if addressW < 160 {
		showZoom = false
		addressW = width - goW - searchW - filterW - modeW - textBrowserBarGap*4 - 16
	}
	if addressW < 160 {
		showSearch = false
		addressW = width - goW - filterW - modeW - textBrowserBarGap*3 - 16
	}
	if addressW < 160 {
		showFilter = false
		addressW = width - goW - modeW - textBrowserBarGap*2 - 16
	}
	if addressW < 160 {
		showMode = false
		addressW = width - goW - textBrowserBarGap - 16
	}
	if addressW < 80 {
		addressW = 80
	}

	address = sdl.Rect{X: element.X + 8 + historyW, Y: y, W: addressW, H: h}
	cursor := address.X + address.W + textBrowserBarGap
	if showSearch {
		search = sdl.Rect{X: cursor, Y: y, W: searchW, H: h}
		cursor = search.X + search.W + textBrowserBarGap
	}
	goButton = sdl.Rect{X: cursor, Y: y, W: goW, H: h}
	cursor = goButton.X + goButton.W + textBrowserBarGap
	if showFilter {
		filter = sdl.Rect{X: cursor, Y: y, W: filterW, H: h}
		cursor = filter.X + filter.W + textBrowserBarGap
	}
	if showMode {
		mode = sdl.Rect{X: cursor, Y: y, W: modeW, H: h}
		cursor = mode.X + mode.W + textBrowserBarGap
	}
	if showZoom {
		zoom = sdl.Rect{X: cursor, Y: y, W: zoomW, H: h}
	}
	return
}

func textBrowserHistoryRects(config *Config, element Element) (sdl.Rect, sdl.Rect) {
	if getElementWidth(element, 1100) < 600 {
		return sdl.Rect{}, sdl.Rect{}
	}
	h := textBrowserURLBarHeight - 4
	back := sdl.Rect{X: element.X + 8, Y: element.Y + 36, W: h, H: h}
	forward := back
	forward.X += h + textBrowserBarGap
	return back, forward
}

func browserHistoryAvailable(delta int) bool {
	browserState.mu.Lock()
	defer browserState.mu.Unlock()
	next := browserState.index + delta
	return next >= 0 && next < len(browserState.history)
}

func drawBrowserHistoryIcon(r *sdl.Renderer, rect sdl.Rect, dir int32, enabled bool) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	ink := ColorTextTertiary()
	if enabled {
		ink = ColorTextPrimary()
	}
	fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 8, glassChipFill())
	strokeRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 8, 1, glassChipEdge())
	cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
	r.SetDrawColor(ink.R, ink.G, ink.B, ink.A)
	r.DrawLine(cx-6, cy, cx+6, cy)
	r.DrawLine(cx+dir*6, cy, cx+dir, cy-5)
	r.DrawLine(cx+dir*6, cy, cx+dir, cy+5)
}

func browserHTTPSAddress(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

const (
	// textBrowserTouchMin is the smallest area the browser's own chrome offers
	// a finger, from the touch-target audit (MODERNIZATION.md, UX-8). The
	// drawn chips are 32px tall and the cookie strip is a font height — fine
	// for a D-pad, short for a fingertip on the Brick's panel — so every tap
	// test grows its rect to this height while the drawn size stays put.
	textBrowserTouchMin = int32(44)
)

// textBrowserTouchTarget grows a drawn chrome rect to the touch minimum,
// centred vertically. Only hit-testing uses it: the layout stays the single
// source of the drawn geometry, and the growth never touches X or W, so the
// 8px gaps that keep neighbours apart still do their job.
func textBrowserTouchTarget(r sdl.Rect) sdl.Rect {
	if r.W <= 0 || r.H <= 0 || r.H >= textBrowserTouchMin {
		return r
	}
	grow := (textBrowserTouchMin - r.H + 1) / 2
	r.Y -= grow
	r.H += grow * 2
	return r
}

// textBrowserToolbarTapRects is the toolbar's hit areas: the layout's drawn
// rects, each grown to a finger's reach (textBrowserTouchTarget). The click
// path in main.go uses these; the renderer uses the layout directly, so a
// chip can be easy to hit without looking like a different chip.
func textBrowserToolbarTapRects(config *Config, element Element) (address, search, filter, mode, zoom, goButton sdl.Rect) {
	address, mode, search, goButton, filter, zoom = textBrowserToolbarLayout(config, element)
	return textBrowserTouchTarget(address), textBrowserTouchTarget(search),
		textBrowserTouchTarget(filter), textBrowserTouchTarget(mode), textBrowserTouchTarget(zoom),
		textBrowserTouchTarget(goButton)
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
	browserLoadNavigation(config, element, browserNavigation{URL: rawURL}, recordHistory, useCache)
}

func browserLoadNavigation(config *Config, element Element, nav browserNavigation, recordHistory, useCache bool) {
	if config == nil {
		return
	}
	outputVariable := element.Variable
	if outputVariable == "" {
		outputVariable = findTextBrowserVariable(config)
	}
	// Try again on the offline card has to reach the network even while the
	// monitor still says offline; the pass is good for this one load.
	bypassOffline := brwRetryBypass
	brwRetryBypass = false
	address, err := normalizeTextBrowserURL(nav.URL)
	if err != nil {
		config.Variables.Custom[outputVariable] = "Browser error: " + err.Error()
		showToast("Enter a valid http:// or https:// address.", ToastError())
		return
	}
	// A search result's click-tracking link opens the page it names
	// (browser_engines.go): Bing's is a script the reader cannot run, and the
	// others cost a request that only reports the click.
	post := nav.Method == http.MethodPost
	if !post {
		if target, ok := brwSearchClickTarget(address); ok {
			address = target
		}
	}
	// A YouTube video is not a page the reader can show: it plays in Media
	// (browser_youtube.go). The link handlers send their links there first;
	// this catches a typed or pasted address, a bookmark and the history.
	if !post && brwOpenAddress(config, outputVariable, address) {
		return
	}
	nav.URL = address
	if post {
		useCache = false
		// A previous GET at the action URL cannot stand in for a submission,
		// including when Back or Forward visits that URL later.
		browserForgetPage(address)
	}
	recordURL := func() {
		if recordHistory {
			browserRecordURLMode(address, post)
		}
	}
	// The page on screen before this load — its document's URL, not the
	// address variable, which holds whatever was typed: a load that only
	// redirects to a video puts it back (brwQueueLanding).
	back := brwPageOnScreen(outputVariable)
	// Remember where the reader was on the page being left, so Back can
	// return them there. This reads the address before it is overwritten, so
	// "from" is the page still on screen.
	browserScrollMemorySave(textBrowserAddress(config), textBrowserScrollY)
	config.Variables.Custom[textBrowserURLVariable] = address
	textBrowserScrollY = 0
	textBrowserScrollVelocity = 0
	// An in-page search belongs to the page it was run against. Navigating
	// leaves the old match list pointing at rows that no longer exist, so the
	// search ends with the page.
	browserFindReset()
	// Back, Forward and Reload return to a page the reader was already on, so
	// arm a restore to where they left it. A fresh navigation (a typed
	// address, a link, Home) is a new place and starts at the top.
	if recordHistory {
		browserScrollPendingClear()
	} else {
		browserScrollPendingSet(address)
	}
	// The toolbar's filter chip belongs to the address being opened, not to the
	// page still on screen: a reload after turning filtering off shows the new
	// state immediately. Settings → Browser → Block ads and trackers is copied
	// for the fetch goroutines first, here on the main thread that owns the
	// config (brw_adblock.go).
	refreshBrowserBlockAdsSetting(config)
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
	// The same goes for a page whose scripts are still running: its result
	// would be dropped, and it would keep the handheld's cores busy beside this
	// load (webdom_runtime.go). The JavaScript setting is copied for the fetch
	// goroutine here, on the main thread that owns the config.
	webJSStopRunning()
	refreshBrowserJavaScriptSetting(config)
	// This load's generation is read here, on the main thread, right after the
	// stop moved it on: read on the fetch goroutine instead, a load superseded
	// before its goroutine ran would pick up the newer number and could no
	// longer be stopped.
	scriptGen := webJSLoadGeneration()

	// A new load replaces whatever owned the page area: the last failure's
	// card, and JukaLand if it was being played from it.
	brwSetFailure(outputVariable, nil)
	brwGameEnd(config)

	if useCache {
		if page, ok := browserCachedPage(address); ok {
			showBrowserPage(config, outputVariable, page)
			if recordHistory || address != back { // a navigation, not the page on screen again
				trophyEvent(tkPageVisit)
				trophyEventDistinct(tkSites, brwRegistrableDomain(browserAdBlockSiteKey(page.FinalURL)))
			}
			recordURL()
			return
		}
	}

	// Known to be offline: show the offline card now instead of waiting for
	// a name lookup to fail. A cached page (above) still opens.
	if !IsOnline() && !bypassOffline {
		fail := brwNewFailure(brwFailOffline, address, "")
		config.Variables.Custom[outputVariable] = fail.text()
		setBrowserLinks(outputVariable, nil)
		setBrowserDocument(outputVariable, nil)
		brwSetFailure(outputVariable, &fail)
		recordURL()
		return
	}

	config.Variables.Custom[outputVariable] = "Loading " + address + " ..."
	setBrowserLinks(outputVariable, nil)
	setBrowserDocument(outputVariable, nil)
	recordURL()

	// The seam is read once, here: a load keeps the fetcher it started with even
	// if a test swaps it while the request is still running.
	fetch := currentBrowserPageFetcher()
	browserFetches.Add(1)
	go func(variable string, request browserNavigation, id uint64) {
		defer browserFetches.Done()
		pageURL := request.URL
		// The fetch parses whatever the network sends — HTML, CSS, scripts —
		// and a parser bug on one hostile or broken page must not take the
		// whole hub down with it: a panic on this goroutine is not caught by
		// the render loop's recover. The page becomes an error message.
		defer func() {
			if r := recover(); r != nil {
				logError("browser", "reader crashed on %s: %v\n%s", pageURL, r, debug.Stack())
				textBrowserRequestMutex.Lock()
				isCurrent := textBrowserRequestSequence[variable] == id
				textBrowserRequestMutex.Unlock()
				if isCurrent {
					fail := brwNewFailure(brwFailOther, pageURL, "The reader hit an internal error on this page.")
					fail.Title = "This page could not be shown"
					brwSetFailure(variable, &fail)
					publishCustom(variable, "This page could not be shown: the reader hit an internal error on it.")
				}
			}
		}()
		page := fetch(request, scriptGen)
		textBrowserRequestMutex.Lock()
		isCurrent := textBrowserRequestSequence[variable] == id
		textBrowserRequestMutex.Unlock()
		if !isCurrent {
			return
		}
		// A redirect that ended on a YouTube video publishes and caches
		// nothing: the render pass plays it and puts the page back.
		if brwQueueLanding(variable, pageURL, back, recordHistory, id, page.FinalURL) {
			return
		}
		// Remember the page for Back/Forward before publishing it: the next
		// visit to the same address is then a map lookup instead of a download.
		if !post {
			browserCachePage(pageURL, page)
		}
		// A redirect (HTTP or the page's own meta refresh) landed somewhere
		// else. The render pass moves the address bar and the history entry
		// there; doing it from here would race with someone typing a new
		// address, and Back would then step through a URL that was only a hop.
		if page.FinalURL != "" && page.FinalURL != pageURL {
			browserRememberLandedURL(pageURL, page.FinalURL)
			// Cache the landing URL too, so Back to the corrected entry is a map
			// lookup instead of a second walk through the redirect.
			if !post {
				browserCachePage(page.FinalURL, page)
			} else {
				browserForgetPage(page.FinalURL)
			}
		}
		setBrowserAdBlockView(variable, browserAdBlockView{
			Site:    browserAdBlockSiteKey(page.FinalURL),
			Blocked: page.Blocked,
			Off:     browserAdBlockOff(page.FinalURL),
		})
		setBrowserLinks(variable, page.Links)
		setBrowserDocument(variable, page.Doc)
		// A failure draws its card (brw_offline.go); a page clears the last one.
		brwSetFailure(variable, page.Fail)
		publishCustom(variable, page.Text)
		trophyEventN(tkAdsBlocked, int64(page.Blocked))
		if page.Fail == nil && (recordHistory || (useCache && pageURL != back)) { // not Reload
			trophyEvent(tkPageVisit)
			trophyEventDistinct(tkSites, brwRegistrableDomain(browserAdBlockSiteKey(page.FinalURL)))
		}
	}(outputVariable, nav, requestID)
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
	brwSetFailure(variable, nil)
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[variable] = page.Text
}

// textBrowserSearchURL turns an address-bar entry that is clearly not a host
// into a web search, so the browser doubles as a search box the way a handheld
// user expects from a single field. The engine is the one chosen in Settings →
// Browser (browser_engines.go). Every caller runs on the main thread, which
// owns the config it reads: the fetch goroutine's own normalize only ever sees
// an address that already has a scheme.
func textBrowserSearchURL(query string) string {
	return brwSearchURLFor(appConfig, query)
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

// browseWebPageResult loads and prepares one page. gen is the script-pass
// generation browserLoadURL read when this load began: a newer load moves it
// on, which is how this load's script pass knows to stop (webdom_runtime.go).
func browseWebPageResult(nav browserNavigation, gen uint64) browserPage {
	// Every subresource this load requests is counted here, so the reader can
	// say how much advertising it kept off the wire.
	resetBrowserAdBlockCount()
	// The script pass's DOM prelude is compiled once per run — about a second
	// on the handheld — so the first page starts it beside its own download.
	if browserJavaScriptEnabled() {
		go webDOMPreludeWarm()
	}
	address, err := normalizeTextBrowserURL(nav.URL)
	if err != nil {
		f := brwNewFailure(brwFailOther, strings.TrimSpace(nav.URL), "That address can't be opened: "+err.Error()+".")
		f.Title = "Can't open that address"
		return browserPage{Text: "Browser error: " + err.Error(), Fail: &f}
	}
	nav.URL = address
	timeout := textBrowserFetchTimeoutFor(textBrowserPageLimitBytes())
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// The jar travels with the client, so a cookie a redirect sets is already
	// in hand when the next hop — and the page's stylesheets and images — go
	// out.
	client := browserHTTPClient(timeout)
	client.CheckRedirect = textBrowserRedirectPolicy
	fetched, err := fetchTextBrowserDocumentRequest(ctx, client, nav)
	if err != nil {
		var fetchErr *brwFetchError
		if errors.As(err, &fetchErr) {
			f := fetchErr.fail
			if f.Kind == brwFailWall {
				// The wall can be met on a meta-refresh hop; the card's buttons
				// run the search that was asked for, not that hop.
				f.URL = address
				return browserPage{Text: f.text(), Fail: &f}
			}
			return browserPage{Text: err.Error(), Fail: &f}
		}
		f := brwNewFailure(brwFailOther, address, brwFriendlyNetError(err))
		return browserPage{Text: f.text(), Fail: &f}
	}
	body, truncated, pageURL, pageLimit := fetched.Body, fetched.Truncated, fetched.URL, fetched.Limit
	// A redirect that ended on a YouTube watch page goes to the player
	// (brwQueueLanding); running its scripts and fetching its stylesheets and
	// images would only delay that.
	if landed := pageURL.String(); landed != address {
		if _, ok := brwYouTubeVideo(landed); ok {
			return browserPage{Text: "Opening the video in Media…", FinalURL: landed}
		}
	}
	// Google answers a browser without JavaScript with a wall, not results:
	// the card that replaces it offers the same search where it works
	// (browser_engines.go).
	if f, ok := brwSearchWall(address, pageURL, body); ok {
		return browserPage{Text: f.text(), Fail: &f}
	}
	// Ad and tracker scripts are removed before the script pass: there is no
	// reason to spend the page's script budget running them, and some ship an
	// inline fallback that would run even with the src blocked.
	html := stripBlockedWebScripts(string(body), pageURL)
	// Page scripts run first, against a real document (webdom_runtime.go), so
	// the text, link, CSS and image passes below see the markup they built.
	scripted := webJSRunPageFor(html, pageURL, gen, time.Time{})
	// A script that sends the page elsewhere — a stub whose only job is
	// location.replace(…), a consent or locale bounce — is followed like a meta
	// refresh: a few hops on the same client and time budget, never back to an
	// address this load already showed. A hop that fails keeps the page the
	// scripts were leaving, which is still worth drawing; so does a load the
	// reader has left, or one with too little of its time left to fetch the
	// next page and run its scripts. A hop's pass gets only what is left.
	seen := map[string]bool{address: true, pageURL.String(): true}
	for hop := 0; scripted.Navigate != "" && hop < textBrowserMetaRedirectLimit; hop++ {
		target := scripted.Navigate
		if seen[target] || webJSLoadGeneration() != gen {
			break
		}
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) < webJSMinBudget {
			break
		}
		seen[target] = true
		if _, ok := brwYouTubeVideo(target); ok {
			return browserPage{Text: "Opening the video in Media…", FinalURL: target}
		}
		next, err := fetchTextBrowserDocument(ctx, client, target)
		if err != nil {
			// A wall met on the way is the same card as one met by the first
			// fetch: its buttons run the search that was asked for.
			var fetchErr *brwFetchError
			if errors.As(err, &fetchErr) && fetchErr.fail.Kind == brwFailWall {
				f := fetchErr.fail
				f.URL = address
				return browserPage{Text: f.text(), Fail: &f}
			}
			logDebug("browser", "script navigation from %s not followed: %v", webJSLogURL(pageURL), err)
			break
		}
		landed := next.URL.String()
		if landed != target {
			if _, ok := brwYouTubeVideo(landed); ok {
				return browserPage{Text: "Opening the video in Media…", FinalURL: landed}
			}
			// It redirected back to a page this load already showed.
			if seen[landed] {
				break
			}
		}
		if f, ok := brwSearchWall(address, next.URL, next.Body); ok {
			return browserPage{Text: f.text(), Fail: &f}
		}
		body, truncated, pageURL, pageLimit = next.Body, next.Truncated, next.URL, next.Limit
		seen[landed] = true
		html = stripBlockedWebScripts(string(body), pageURL)
		scripted = webJSRunPageFor(html, pageURL, gen, deadline)
	}
	source := scripted.HTML
	title, pageText := extractTextBrowserHTML(source)
	if title == "" {
		title = pageURL.Hostname()
	}
	if pageText == "" {
		pageText = "This page has no readable text content."
	}
	pageText = capTextBrowserText(pageText)
	links := extractBrowserLinks(source, pageURL)
	// Linked stylesheets carry most of a real page's look. Fetching a few small
	// ones is cheap; the parser then applies the presentation subset it can
	// draw, and the plain text above remains the fallback for everything else.
	pageCSS := fetchWebStylesheets(source, pageURL)
	if truncated {
		// Say so on the drawn page itself: the reader shows the styled document
		// rather than the plain text, so a note only in the text would be
		// invisible, and a page that simply stops mid-sentence looks broken.
		source = withTextBrowserTruncationNote(source, pageLimit)
	}
	doc := parseWebPage(source, pageCSS, pageURL.String())
	doc.Favicon = browserFaviconURL(source, pageURL)
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
	text := browserReaderText(title, pageURL.String(), len(links), pageText,
		browserBlockedRequests(), browserAdBlockOff(pageURL.String()), truncated, pageLimit)
	return browserPage{
		Text:     text,
		Links:    links,
		FinalURL: pageURL.String(),
		Doc:      doc,
		Blocked:  browserBlockedRequests(),
	}
}

// textBrowserRedirectLimit is how many HTTP hops one request may walk: enough
// for the http→https, old-path→new-path and trailing-slash hops a site chains
// on purpose, and short enough that a misconfigured site cannot spin the
// reader forever.
const textBrowserRedirectLimit = 5

// textBrowserMetaRedirectLimit bounds the redirects a page asks for in its own
// markup. Sites use them for "this page moved" stubs and for the bounce a
// landing page makes to a locale or a login form; a page that points at
// itself, or a pair that point at each other, must not keep the reader loading.
const textBrowserMetaRedirectLimit = 3

// textBrowserMetaRedirectDelay is the longest <meta http-equiv="refresh"> delay
// still treated as a redirect. A longer one is a page asking to be reloaded
// periodically, which the reader should not act on behind the user's back.
const textBrowserMetaRedirectDelay = 2 * time.Second

// textBrowserRedirectPolicy is the browser's rule for an HTTP redirect: stay on
// a web scheme, and stop after a chain of five, so a site cannot send the
// reader to a scheme this app cannot show.
func textBrowserRedirectPolicy(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("redirected to %s, which this browser cannot open", req.URL.Scheme)
	}
	if len(via) > 0 && via[0].Method == http.MethodPost &&
		via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("a form submission cannot redirect from HTTPS to an unencrypted address")
	}
	if len(via) >= textBrowserRedirectLimit {
		return fmt.Errorf("more than %d redirects in a row", textBrowserRedirectLimit)
	}
	if len(via) > 0 && via[0].Method == http.MethodPost && req.Method == http.MethodGet {
		// A 303 (or 302) turns POST into a fresh GET. Do not carry the form's
		// Origin or Content-Type into that request.
		req.Header.Del("Origin")
		req.Header.Del("Content-Type")
	}
	return nil
}

// textBrowserDocument is one page as it arrived: the bytes, where the load
// finally landed after HTTP and in-page redirects, and whether the tail of a
// page larger than the cap was cut.
type textBrowserDocument struct {
	Body      []byte
	URL       *url.URL
	Truncated bool
	Limit     int
}

// textBrowserPageLimitBytes picks the HTML budget for one download from the
// memory the kernel says is available. One eighth of MemAvailable is reserved
// for the body and the strings/selector tree built from it, with an 8 MiB floor
// so a tight device still opens ordinary pages and a 64 MiB ceiling so no one
// page can consume the handheld's heap. An unavailable host reports the maximum
// sentinel from freeMemoryMB and gets the ceiling.
func textBrowserPageLimitBytes() int {
	return textBrowserPageLimitFor(freeMemoryMB())
}

func textBrowserPageLimitFor(availableMB uint64) int {
	mb := availableMB / 8
	if mb < uint64(textBrowserMinPageBytes>>20) {
		return textBrowserMinPageBytes
	}
	if mb > uint64(textBrowserMaxPageBytes>>20) {
		return textBrowserMaxPageBytes
	}
	return int(mb) << 20
}

// fetchTextBrowserDocument performs one page load and follows the redirect the
// page asks for in its own markup. HTTP redirects are the client's job, but a
// <meta http-equiv="refresh"> arrives with a 200 and a body, so nothing follows
// it automatically — and the reader used to be left on a stub whose whole
// content was "this page has moved", with the link that would have followed it
// stripped along with the page's scripts. The hops share the client, so the
// cookie jar and the load's one time budget carry across them.
func fetchTextBrowserDocument(ctx context.Context, client *http.Client, address string) (textBrowserDocument, error) {
	return fetchTextBrowserDocumentRequest(ctx, client, browserNavigation{URL: address})
}

func fetchTextBrowserDocumentRequest(ctx context.Context, client *http.Client, nav browserNavigation) (textBrowserDocument, error) {
	current := nav
	for hop := 0; ; hop++ {
		doc, err := fetchTextBrowserOnceRequest(ctx, client, current)
		if err != nil {
			return doc, err
		}
		if hop >= textBrowserMetaRedirectLimit {
			return doc, nil
		}
		target := browserMetaRefreshTarget(string(doc.Body), doc.URL)
		if target == "" || target == doc.URL.String() {
			return doc, nil
		}
		current = browserNavigation{URL: target}
	}
}

// fetchTextBrowserOnce is one request and body read, with the wording the error
// card shows.
func fetchTextBrowserOnce(ctx context.Context, client *http.Client, address string) (textBrowserDocument, error) {
	return fetchTextBrowserOnceRequest(ctx, client, browserNavigation{URL: address})
}

func fetchTextBrowserOnceRequest(ctx context.Context, client *http.Client, nav browserNavigation) (textBrowserDocument, error) {
	address := nav.URL
	fail := func(kind brwFailKind, detail string) (textBrowserDocument, error) {
		return textBrowserDocument{}, &brwFetchError{fail: brwNewFailure(kind, address, detail)}
	}
	method := http.MethodGet
	var requestBody io.Reader
	if nav.Method == http.MethodPost {
		method = http.MethodPost
		requestBody = bytes.NewReader(nav.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, address, requestBody)
	if err != nil {
		return fail(brwFailOther, "That address can't be opened.")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fail(brwFailOther, "That address can't be opened.")
	}
	browserChromeNavigate(req.Header)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", nav.ContentType)
	}
	if nav.Referrer != "" {
		browserChromeFormNavigate(req.Header, browserURL(nav.Referrer), req.URL, method)
	}
	resp, err := client.Do(req)
	if err != nil {
		// Nothing below shows the transport error itself: a resolver failure
		// is "offline" or "site not found", never "dial tcp: lookup …". A
		// failed lookup is the one error offline and a mistyped host share,
		// so it gets a quick look at whether the internet answers at all.
		timedOut := ctx.Err() != nil
		online := IsOnline()
		if online && !timedOut && brwLooksLikeResolverFailure(err) && !brwProbeOnline() {
			online = false
		}
		return textBrowserDocument{}, &brwFetchError{fail: brwClassifyError(address, err, timedOut, online)}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The client follows every redirect it can, so a 3xx here is a reply
		// with no Location to follow. Say that instead of a bare status code.
		return fail(brwFailHTTP, "HTTP "+resp.Status+" — the redirect named no usable target")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Google's robot check answers 429 at the end of a redirect: the wall
		// card, not "HTTP 429" (browser_engines.go).
		if wall, ok := brwSearchWall(address, resp.Request.URL, nil); ok {
			return textBrowserDocument{}, &brwFetchError{fail: wall}
		}
		return fail(brwFailHTTP, "HTTP "+resp.Status)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "html") {
		kind := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
		return fail(brwFailContent, "This address is a "+kind+" file, not a web page.")
	}
	limit := textBrowserPageLimitBytes()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		if ctx.Err() != nil {
			return fail(brwFailTimeout, "")
		}
		return fail(brwFailConnect, "The page stopped arriving halfway. Try again.")
	}
	body, truncated := truncateTextBrowserBody(body, limit)
	return textBrowserDocument{Body: body, URL: resp.Request.URL, Truncated: truncated, Limit: limit}, nil
}

// browserMetaRefreshTarget reads the redirect a page asks for in its markup:
// <meta http-equiv="refresh" content="0; url=/next">. A refresh that only
// reloads the page, waits longer than a couple of seconds, or names something
// other than an http(s) URL is not a redirect and returns "".
//
// A refresh inside <noscript> is skipped while page scripts are on. A browser
// that runs JavaScript never reads <noscript>, and that is where pages park the
// redirect meant only for script-less browsers — Google's "turn on JavaScript",
// a site's /nojs page. Following it sent the reader there even though its own
// script pass (webdom_runtime.go) could have run the page itself. With
// JavaScript off in Settings the reader is a script-less browser again and
// follows it.
func browserMetaRefreshTarget(html string, base *url.URL) string {
	if base == nil {
		return ""
	}
	lower := asciiLower(html)
	skipNoscript := browserJavaScriptEnabled()
	for from := 0; from < len(lower); {
		i := strings.Index(lower[from:], "<meta")
		if i < 0 {
			break
		}
		i += from
		end := findTextBrowserTagEnd(html, i+len("<meta"))
		if end < 0 {
			break
		}
		tag := html[i:end]
		from = end + 1
		if !strings.EqualFold(strings.TrimSpace(htmlTagAttribute(tag, "http-equiv")), "refresh") {
			continue
		}
		// Checked only for refresh tags: the look back over the page is cheap
		// for the one or two a page has, not for every <meta> of a big page.
		if skipNoscript && browserInsideNoscript(lower, i) {
			continue
		}
		delay, target := parseBrowserMetaRefresh(htmlAttrValue(htmlTagAttribute(tag, "content")))
		if target == "" || delay > textBrowserMetaRedirectDelay {
			return ""
		}
		resolved, err := base.Parse(target)
		if err != nil || (resolved.Scheme != "http" && resolved.Scheme != "https") {
			return ""
		}
		return resolved.String()
	}
	return ""
}

// browserInsideNoscript reports whether offset i of lower (the page,
// lower-cased) lies inside a <noscript> element: the nearest "<noscript" before
// it has not been closed by a "</noscript" yet. noscript's content is raw text
// in a browser that runs scripts, so no tag inside it can close it early.
func browserInsideNoscript(lower string, i int) bool {
	if i <= 0 || i > len(lower) {
		return false
	}
	before := lower[:i]
	open := strings.LastIndex(before, "<noscript")
	if open < 0 {
		return false
	}
	return strings.LastIndex(before, "</noscript") < open
}

// parseBrowserMetaRefresh splits a refresh content value into its delay and
// target. The grammar is "seconds[; url=...]", but pages ship the URL quoted,
// unquoted, with stray spaces or with "URL=" in any case, so all of those are
// accepted rather than the redirect being silently dropped.
func parseBrowserMetaRefresh(content string) (time.Duration, string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return 0, ""
	}
	delayPart, rest := content, ""
	if i := strings.IndexAny(content, ";,"); i >= 0 {
		delayPart, rest = content[:i], content[i+1:]
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(delayPart), 64)
	if err != nil || seconds < 0 {
		// No delay before the separator: the spec has no such form, so this is
		// not a refresh the reader should act on.
		return 0, ""
	}
	rest = strings.TrimSpace(rest)
	if len(rest) >= 3 && strings.EqualFold(rest[:3], "url") {
		i := strings.IndexByte(rest, '=')
		if i < 0 {
			return 0, ""
		}
		rest = strings.TrimSpace(rest[i+1:])
	}
	rest = strings.Trim(rest, "\"' \t")
	if rest == "" || strings.ContainsAny(rest, "\r\n") {
		return 0, ""
	}
	return time.Duration(seconds * float64(time.Second)), rest
}

// browserReaderText builds the plain-text view a page without a drawable
// document falls back to. It states what was skipped, so a reader can tell a
// trimmed page from a complete one.
func browserReaderText(title, address string, linkCount int, pageText string, blocked int, filterOff bool, truncated bool, pageLimit int) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Title: %s\nURL: %s\n", title, address)
	if filterOff {
		text.WriteString("Ad filtering is off for this site\n")
	} else if blocked > 0 {
		fmt.Fprintf(&text, "Blocked: %s\n", pluralizeCount(blocked, "ad/tracker request", "ad/tracker requests"))
	}
	if truncated {
		text.WriteString(strings.TrimSpace(textBrowserTruncationNote(pageLimit)))
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
func textBrowserTruncationNote(limit int) string {
	if limit <= 0 {
		limit = textBrowserMaxPageBytes
	}
	return fmt.Sprintf("\n[Page truncated: only the first %d MiB were downloaded. Follow a link to keep reading.]", limit>>20)
}

// withTextBrowserTruncationNote puts that sentence at the top of the page,
// inside the body. A truncated page is far past the parser's text budget, so a
// note at the end would be dropped along with the rest of the unread tail.
func withTextBrowserTruncationNote(source string, limit int) string {
	note := "<p>" + textBrowserTruncationNote(limit) + "</p>"
	lower := asciiLower(source)
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
			return fmt.Sprintf("Failed to fetch URL:\n%s\n%s", url, plainNetworkError(err))
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

// getRunningServices returns common running services/daemons on Linux.
func getRunningServices() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Services error: %v", r)
		}
	}()
	if runtime.GOOS != "linux" {
		return "Unavailable (not Linux)"
	}

	out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=running", "--no-pager", "--plain").Output()
	if err != nil {
		return "Unavailable"
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
		return "Unavailable (not Linux)"
	}
	out, err := exec.Command("ps", "-eo", "pid,ppid,user,%cpu,%mem,cmd", "--sort=-%cpu").Output()
	if err != nil {
		return "Unavailable"
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
		if skipScanEntryName(e.Name()) {
			continue
		}
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
