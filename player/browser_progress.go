package main

// browser_progress.go — a page drawn before its load finished says so.
//
// The reader draws a page as soon as it has one: the markup its server sent
// while the page's scripts run, then the page they built before its pictures
// and web fonts are in. Nothing on screen said the load was still going, so a
// page whose pictures were still grey looked finished, and broken. While the
// load that drew the page is still running, a thin line now runs along the
// top of the page, as a browser's progress bar does, and it goes when the load
// ends (its pictures and fonts in, or given up on).

import (
	"github.com/veandco/go-sdl2/sdl"
)

// browserLoadInFlight reports whether a page load for variable is still
// running (browserLoadNavigation's goroutine has not ended).
func browserLoadInFlight(variable string) bool {
	textBrowserRequestMutex.Lock()
	defer textBrowserRequestMutex.Unlock()
	_, running := textBrowserRequestCancels[variable]
	return running
}

// brwDrawLoadProgress draws the running line along the top edge of the page
// at rect: a band of accent that sweeps across a faint track, 3 px tall.
// Reduced motion keeps the band still at the left. Render thread.
func brwDrawLoadProgress(r *sdl.Renderer, rect sdl.Rect, accent sdl.Color) {
	if r == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	const h = int32(3)
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = r.SetDrawColor(accent.R, accent.G, accent.B, 50)
	_ = r.FillRect(&sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: h})
	band := rect.W / 3
	x := rect.X
	if !effectiveReducedMotion(appConfig) {
		// One sweep every 1.4 s, from off the left edge to off the right.
		phase := int32(sdl.GetTicks64() % 1400)
		x = rect.X - band + (rect.W+band)*phase/1400
	}
	left, right := max(x, rect.X), min(x+band, rect.X+rect.W)
	if right <= left {
		return
	}
	_ = r.SetDrawColor(accent.R, accent.G, accent.B, 230)
	_ = r.FillRect(&sdl.Rect{X: left, Y: rect.Y, W: right - left, H: h})
}
