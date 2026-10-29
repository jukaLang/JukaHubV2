package main

// activity.go — the persistent activity strip.
//
// While the app works in the background (a tool or core install, the app's own
// update, an offline media download, a package install) a small stack of chips
// stays pinned to the right edge of every scene, naming what is happening and
// how far along it is. It is the same question the installer card answers on
// its own screen — what is running right now — without navigating anywhere.
//
// The strip is read-only: it aggregates the same locked state the installer
// card and the Downloads panel publish, so every row is a plain function of
// values the render loop already reads, and the sources never double-count.
// Rows appear only while work is running; an empty queue draws nothing.

import (
	"fmt"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The install card's geometry, enough to keep the strip from drawing under the
// card on narrow layouts (renderInstallBar's cardW, and its computed cardH
// with the app's small/medium fonts — an estimate, but the strip's fallback
// position is below the card, so slack is invisible).
const (
	activityCardW = 560
	activityCardH = 96
)

// activityItem is one row of the strip. frac is 0..1, or negative when the
// operation's size is unknown (an indeterminate bar, like a package install
// whose server sent no Content-Length).
type activityItem struct {
	glyph string
	label string // the source's own display name; the strip only trims it
	frac  float64
	tint  sdl.Color // status colour: accent while working, red/green on a result
	// cancel runs when the chip's X is pressed. A nil cancel (the overflow
	// line, a settled install holding its ✓) draws no button: the X is only
	// an affordance on work the app can actually stop.
	cancel func()
	// open runs when the chip body is tapped. An install focuses the scene it
	// came from, a download row opens the Downloads panel. nil is the same as
	// "look, don't tap" — the overflow fold has nowhere to go.
	open func()
}

// activityRows assembles the strip's rows from the background-work sources,
// installs first — the same claim to the network the installer card shows.
func activityRows() []activityItem {
	var rows []activityItem

	// Anything on the installer card: a tool install, an emulator core update,
	// the app's own update, a package install. They all publish through the
	// installBar* helpers, so one snapshot covers all four.
	if visible, tool, phase, _, done, total, _, _, _ := installBarSnapshot(); visible {
		title := installBarTitle()
		if title == "" {
			title = tool
		}
		frac := -1.0
		if total > 0 {
			frac = float64(done) / float64(total)
			if frac > 1 {
				frac = 1
			}
		}
		item := activityItem{
			glyph: activityGlyph(phase),
			label: title,
			frac:  frac,
			tint:  installPhaseTint(phase),
		}
		if installBarActive() {
			// The X stops the operation the card is running, the same token
			// every install flow shares.
			item.cancel = func() { installBarCancelActive() }
		}
		// The body tap returns to the scene that launched the install — where
		// its result page and its own retry/offer actions live.
		if src := installBarSource(); src != "" && sceneIndexExact(appConfig, src) >= 0 {
			item.open = func() { activityFocusInstall() }
		}
		rows = append(rows, item)
	}

	// Media downloads. Active rows each get their chip; queued rows fold into
	// one line, because waiting rows would otherwise spend the strip's small
	// budget on work that has not begun. Failed rows are absent here — a
	// failure is called out once as a toast, and the Downloads panel keeps the
	// full history with its retry action.
	queued := 0
	for _, it := range downloadsSnapshot() {
		switch it.Status {
		case downloadActive:
			frac := -1.0
			if it.Total > 0 {
				frac = it.percent()
			}
			id := it.ID
			rows = append(rows, activityItem{
				glyph: "↓",
				label: it.displayTitle(),
				frac:  frac,
				tint:  installHighlight(),
				// The X aborts the transfer: the row goes, its partial folder
				// goes with it — the same removal the Downloads panel offers.
				cancel: func() { downloadRemove(id) },
				// The body tap opens the Downloads panel, which owns progress,
				// retry and removal for these rows.
				open: func() { openDownloadsPanel() },
			})
		case downloadQueued:
			queued++
		}
	}
	if queued > 0 {
		paused := func() { downloadSetPaused(!downloadsPaused()) }
		label := fmt.Sprintf("%d waiting in queue", queued)
		if downloadsPaused() {
			label = fmt.Sprintf("%d waiting (paused)", queued)
		}
		rows = append(rows, activityItem{
			glyph: "•",
			label: label,
			frac:  -1,
			tint:  ColorIconSurface,
			// The waiting line's X pauses the queue instead of rescinding the
			// rows: those belong to the Downloads panel.
			cancel: paused,
			// Its body opens that panel too — same page, same controls.
			open: func() { openDownloadsPanel() },
		})
	}
	return rows
}

// activityGlyph picks the status mark for an installer phase, the same story
// the card's tile tells.
func activityGlyph(phase string) string {
	switch phase {
	case installPhaseDone:
		return "✓"
	case installPhaseError:
		return "!"
	}
	return "↓"
}

// activityFitLabel truncates a label to what fits maxWidth, so a long video
// title cannot push text outside its chip.
func activityFitLabel(font *ttf.Font, text string, maxWidth int32) string {
	ellipsis := "…"
	if text == "" || font == nil {
		return text
	}
	w, _, _ := font.SizeUTF8(text)
	if int32(w) <= maxWidth {
		return text
	}
	runes := []rune(text)
	for i := len(runes) - 1; i > 0; i-- {
		candidate := string(runes[:i]) + ellipsis
		if cw, _, _ := font.SizeUTF8(candidate); int32(cw) <= maxWidth {
			return candidate
		}
	}
	return ellipsis
}

// renderActivityStrip draws the strip on whatever scene is up. When nothing
// runs in the background, nothing is drawn — the chips are not decoration.
func renderActivityStrip(renderer *sdl.Renderer, config *Config) {
	// Each frame rebuilds the hit table from what is actually on screen, so a
	// chip that vanished mid-frame can never accept a click meant for the
	// scene underneath.
	activityHitsReset()
	if renderer == nil {
		return
	}
	rows := activityRows()
	if len(rows) == 0 {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	const (
		chipW   = int32(200)
		gap     = int32(6)
		maxRows = 4
	)
	glyphH := int32(font.Height())
	if glyphH < 12 {
		glyphH = 12
	}
	chipH := glyphH + 12 // label on top, progress rail under it

	shown := rows
	if len(rows) > maxRows {
		shown = append(rows[:maxRows:maxRows], activityItem{
			glyph: "•",
			label: fmt.Sprintf("%d more behind", len(rows)-maxRows),
			frac:  -1,
			tint:  ColorIconSurface,
		})
	}

	barH := int32(HomeTopBarH)
	if screenHeight < 600 {
		barH = HomeTopBarHSmall
	}
	x := screenWidth - chipW - 12
	y := barH + 10
	// The installer card slides in top-center; where its right half could
	// reach the strip's column (narrow layouts) the strip parks below the
	// card instead of beneath it.
	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); visible {
		if cardRight := (screenWidth + activityCardW) / 2; x < cardRight {
			y = barH + 10 + activityCardH + 10
		}
	}

	for i, row := range shown {
		cy := y + int32(i)*(chipH+gap)

		// A tappable chip body registers first; the X registers after it, and
		// the click router walks the table back to front, so a press on the
		// button area wins over the jump the body would make.
		if row.open != nil {
			activityHitAdd(sdl.Rect{X: x, Y: cy, W: chipW, H: chipH}, row.open)
		}

		// Soft shadow, panel, hairline border — the toast/scene-card recipe.
		fillRoundedRect(renderer, x, cy+2, chipW, chipH, 10, ShadowFill(60))
		fillRoundedRect(renderer, x, cy, chipW, chipH, 10, ColorPanelRaised)
		strokeRoundedRect(renderer, x, cy, chipW, chipH, 10, 1, ColorBorderDefault)

		// Status tile on the left: glyph over the tint, like the card's tile.
		tile := chipH
		fillRoundedRect(renderer, x, cy, tile, chipH, 10, row.tint)
		if gw, gh, _ := font.SizeUTF8(row.glyph); gw > 0 && gh > 0 {
			renderText(renderer, config, font, row.glyph, contrastText(row.tint),
				x+(tile-int32(gw))/2, cy+(chipH-glyphH)/2+3)
		}

		// Label: measured truncation so a long title cannot escape. A chip
		// with an X gives the button the last few pixels instead.
		labelX := x + tile + 8
		labelW := chipW - tile - 16
		xRect := sdl.Rect{}
		if row.cancel != nil {
			xRect = activityHitAdd(activityStripXRect(x, cy, chipW, chipH), row.cancel)
			labelW -= (xRect.W + 2)
		}
		label := activityFitLabel(font, row.label, labelW)
		renderText(renderer, config, font, label, ColorTextPrimary(), labelX, cy+3)
		if xRect.W > 0 {
			gw, gh, _ := font.SizeUTF8("✕")
			renderText(renderer, config, font, "✕", ColorIconSurface,
				xRect.X+(xRect.W-int32(gw))/2, xRect.Y+(chipH-4-int32(gh))/2)
		}

		// Progress rail along the bottom: a deterministic fraction when the
		// source knows its size, a soft travelling shimmer when it does not.
		railX := labelX
		railW := labelW
		railY := cy + chipH - 5
		railH := int32(2)
		fillRoundedRect(renderer, railX, railY, railW, railH, railH/2, ColorTrack)
		fill := installHighlight()
		if row.frac >= 0 {
			fillW := int32(float64(railW) * row.frac)
			if fillW < railH {
				fillW = railH
			}
			renderWithClip(renderer, railX, railY, railW, railH, func(r *sdl.Renderer) {
				fillRoundedRect(r, railX, railY, fillW, railH, railH/2, fill)
			})
		} else {
			span := railW / 3
			sh := sdl.Color{R: fill.R, G: fill.G, B: fill.B, A: 130}
			// A soft glide that advances about once every eight frames and
			// wraps over the rail exactly like the install card's shimmer.
			period := (railW + span) * 8
			ms := int32(time.Now().UnixNano() / int64(time.Millisecond))
			shift := (ms % period) / 8
			renderWithClip(renderer, railX, railY, railW, railH, func(r *sdl.Renderer) {
				fillRoundedRect(r, railX-span+shift, railY, span, railH, railH/2, sh)
			})
		}
	}
}

// activityStripXRect is the button footprint inside a chip: a square at the
// chip's right edge, comfortably bigger than the glyph so a stylus or a finger
// lands inside it.
func activityStripXRect(x, cy, chipW, chipH int32) sdl.Rect {
	return sdl.Rect{X: x + chipW - 24, Y: cy + 2, W: 20, H: chipH - 4}
}

// The strip's click targets for one drawn frame, rebuilt from scratch each
// render. The render loop and mouse handling both run on the main thread, so
// the table is momentarily consistent with the last frame; the mutex only
// covers tests that call in from other goroutines.
type activityHit struct {
	rect   sdl.Rect
	action func()
}

var (
	activityHitsMu sync.Mutex
	activityHits   []activityHit
)

func activityHitsReset() {
	activityHitsMu.Lock()
	activityHits = activityHits[:0]
	activityHitsMu.Unlock()
}

// activityHitAdd registers a chip's X button and returns the rect the caller
// draws the glyph into.
func activityHitAdd(rect sdl.Rect, action func()) sdl.Rect {
	activityHitsMu.Lock()
	activityHits = append(activityHits, activityHit{rect: rect, action: action})
	activityHitsMu.Unlock()
	return rect
}

// activityStripClick routes a tap on the strip: the X stops the work behind
// the chip (a running install, an active download, the queue's waiting line)
// and the chip body jumps to where that work is managed — an install's source
// scene, or the Downloads panel. It reports whether the strip consumed the
// click, so the scene underneath keeps the ones that miss.
func activityStripClick(mx, my int32) bool {
	activityHitsMu.Lock()
	hits := make([]activityHit, len(activityHits))
	copy(hits, activityHits)
	activityHitsMu.Unlock()
	for i := len(hits) - 1; i >= 0; i-- {
		r := hits[i].rect
		if mx >= r.X && mx < r.X+r.W && my >= r.Y && my < r.Y+r.H {
			if hits[i].action != nil {
				hits[i].action()
			}
			return true
		}
	}
	return false
}

// activityFocusInstall returns to the scene the running install was launched
// from, wherever the user navigated to since. Already being on that scene (or a
// run that began with no scene up, like a boot-time update) keeps the tap
// silent rather than re-fading to the screen that is already drawn.
func activityFocusInstall() {
	src := installBarSource()
	if src == "" || appConfig == nil {
		return
	}
	idx := sceneIndexExact(appConfig, src)
	if idx < 0 || idx == currentSceneIndex {
		return
	}
	PlayActivateSound()
	changeSceneTo(appConfig, idx)
}
