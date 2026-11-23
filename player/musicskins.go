package main

// musicskins.go — the Now Playing layouts (the third, iPod, is musicpod.go).
//
// Auralis Pro is the rich card the player grew into: large artwork, the
// visualizer inside it, transport and play modes in one row. StockOS is the
// compact layout: a slim now-playing strip above a full-height list, closer to
// the player that shipped with the handheld.
//
// Both layouts fill the same musicRects, so the keyboard, controller and touch
// handlers work unchanged whichever one is selected.

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type musicLayout int

const (
	// musicLayoutAuralis is the default: artwork-led card + playlist card.
	musicLayoutAuralis musicLayout = iota
	// musicLayoutStockOS is the list-led layout.
	musicLayoutStockOS
	// musicLayoutPod is the iPod-style screen with its own menus, themes and
	// transitions (musicpod.go).
	musicLayoutPod
)

func (l musicLayout) label() string {
	switch l {
	case musicLayoutStockOS:
		return "StockOS"
	case musicLayoutPod:
		return "iPod"
	}
	return "Auralis Pro"
}

// musicLayoutCycle is the layout after (dir > 0) or before l.
func musicLayoutCycle(l musicLayout, dir int) musicLayout {
	order := []musicLayout{musicLayoutAuralis, musicLayoutStockOS, musicLayoutPod}
	at := 0
	for i, v := range order {
		if v == l {
			at = i
		}
	}
	step := 1
	if dir < 0 {
		step = -1
	}
	return order[(at+step+len(order))%len(order)]
}

const (
	musicLayoutKey  = "musicNowPlayingLayout"
	musicArtOpacity = "musicArtOpacity"
)

// musicLayoutFromConfig reads the layout. Anything unrecognised falls back to
// Auralis Pro so a typo cannot leave a blank player.
func musicLayoutFromConfig(config *Config) musicLayout {
	if config == nil {
		return musicLayoutAuralis
	}
	switch strings.ToLower(strings.TrimSpace(customStringValue(config.Variables.Custom, musicLayoutKey))) {
	case "stockos", "stock", "os", "list":
		return musicLayoutStockOS
	case "ipod", "uipod", "pod":
		return musicLayoutPod
	}
	return musicLayoutAuralis
}

// musicSetLayout stores the layout choice.
func musicSetLayout(config *Config, l musicLayout) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	switch l {
	case musicLayoutStockOS:
		config.Variables.Custom[musicLayoutKey] = "stockos"
	case musicLayoutPod:
		config.Variables.Custom[musicLayoutKey] = "ipod"
	default:
		config.Variables.Custom[musicLayoutKey] = "auralis"
	}
}

// musicArtOpacityPercent is the configured cover-art opacity, clamped to the
// range the settings panel offers. The default is fully opaque so an existing
// install looks unchanged.
func musicArtOpacityPercent(config *Config) int {
	raw := strings.TrimSpace(customStringValue(config.Variables.Custom, musicArtOpacity))
	if raw == "" {
		return 100
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 100
	}
	if n < 10 {
		n = 10
	}
	if n > 100 {
		n = 100
	}
	return n
}

// musicSetArtOpacity stores the opacity percentage.
func musicSetArtOpacity(config *Config, percent int) {
	if config == nil {
		return
	}
	if percent < 10 {
		percent = 10
	}
	if percent > 100 {
		percent = 100
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicArtOpacity] = strconv.Itoa(percent)
}

// musicArtAlpha turns the percentage into an alpha value. The floor keeps a
// faint tile readable against the dark card: below ~25% the artwork stops
// reading as artwork at all.
func musicArtAlpha(percent int) uint8 {
	if percent < 10 {
		percent = 10
	}
	if percent > 100 {
		percent = 100
	}
	a := int(math.Round(255 * float64(percent) / 100))
	if a < 26 {
		a = 26
	}
	return uint8(a)
}

// musicArtAlphaNow is the alpha for the player's current frame.
func musicArtAlphaNow(config *Config) uint8 {
	return musicArtAlpha(musicArtOpacityPercent(config))
}

// ──────────────────────────────────────────────────────────────────────
// Auralis Pro helpers
// ──────────────────────────────────────────────────────────────────────

// renderMusicVisualizerBars draws the synthetic spectrum inside a square tile.
// alpha scales the whole thing with the cover-art opacity.
func renderMusicVisualizerBars(renderer *sdl.Renderer, playing, paused bool, x, y, size int32, alpha uint8) {
	barW := int32(6)
	barGap := int32(7)
	bars := 12
	totalW := int32(bars)*(barW+barGap) - barGap
	barBase := y + size - 34
	for i := 0; i < bars; i++ {
		h := int32(music.visualizer[(i*visualBars/bars)%visualBars] * 1.1)
		if h < 6 {
			h = 6
		}
		if h > size-116 {
			h = size - 116
		}
		bx := x + (size-totalW)/2 + int32(i)*(barW+barGap)
		a := uint8(float64(alpha) * 0.86)
		if !playing || paused {
			a = uint8(float64(a) * 0.5)
		}
		fillRoundedRect(renderer, bx, barBase-h, barW, h, 3, WithAlpha(accentColor, a))
	}
}

// musicTrackTitle is the display name without the file extension.
func musicTrackTitle(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// musicRowVisibleRange returns the first row index to draw and how many fit,
// keeping the highlighted row on screen.
func musicRowVisibleRange(cursor, count, visible int) int {
	if visible < 1 {
		visible = 1
	}
	start := 0
	if cursor >= visible {
		start = cursor - visible + 1
	}
	if start > count {
		start = count
	}
	return start
}

// ──────────────────────────────────────────────────────────────────────
// StockOS layout
// ──────────────────────────────────────────────────────────────────────

// musicStockOSLayout is the list-first layout's measured geometry, kept for
// the real-font containment tests. Render thread only.
var musicStockOSLayout struct {
	strip, text, controls, list sdl.Rect
	meta, seek, times           sdl.Rect
	rowName, rowDuration        sdl.Rect
	hintY                       int32
	hintInk                     sdl.Color
}

// renderMusicStockOS draws the list-first layout. It is called from
// renderMusicPlayer once the background is cleared.
func renderMusicStockOS(renderer *sdl.Renderer, config *Config) {
	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if font == nil {
		return
	}
	if medFont == nil {
		medFont = font
	}
	if bigFont == nil {
		bigFont = medFont
	}
	artAlpha := musicArtAlphaNow(config)

	// Same shared bar as the Auralis layout and every other scene; the layout's
	// own name is a chip inside the now-playing strip instead of a second title
	// in the header.
	headerH := headerBarHeight()

	music.mu.Lock()
	tracks := append([]musicTrack(nil), music.tracks...)
	cur := music.current
	cursor := music.cursor
	playing := music.playing
	paused := music.paused
	position := music.position
	duration := music.duration
	volume := music.volume
	repeatMode := music.repeatMode
	shuffleOn := music.shuffle
	queued := music.queueLen
	speed := music.speed
	lyrics := music.lyrics
	showLyrics := music.showLyrics
	music.mu.Unlock()
	if speed <= 0 {
		speed = 1
	}
	musicUpdateVisualizer()

	for _, r := range []*sdl.Rect{
		&musicRects.prev, &musicRects.playPause, &musicRects.next, &musicRects.stop,
		&musicRects.shuffle, &musicRects.repeat, &musicRects.favorite,
		&musicRects.search, &musicRects.exitTimer, &musicRects.volDown, &musicRects.volUp,
		&musicRects.seek, &musicRects.art,
	} {
		*r = sdl.Rect{}
	}
	musicRects.rows = musicRects.rows[:0]
	musicRects.rowStart = 0
	musicRects.valid = true
	musicStockOSLayout.rowName, musicStockOSLayout.rowDuration = sdl.Rect{}, sdl.Rect{}

	// ── Now playing strip ──
	const stripH = int32(150)
	stripX := int32(24)
	stripY := headerH + 16
	stripW := screenWidth - 2*stripX
	musicStockOSLayout.strip = sdl.Rect{X: stripX, Y: stripY, W: stripW, H: stripH}
	drawCard(renderer, stripX, stripY, stripW, stripH, 16)

	artSize := int32(110)
	artX := stripX + 16
	artY := stripY + (stripH-artSize)/2
	fillRoundedRect(renderer, artX, artY, artSize, artSize, 12, WithAlpha(ColorPanelRaised, artAlpha))
	strokeRoundedRect(renderer, artX, artY, artSize, artSize, 12, 1, WithAlpha(ColorBorder, artAlpha))
	musicRects.art = sdl.Rect{X: artX, Y: artY, W: artSize, H: artSize}
	if showLyrics && len(lyrics) > 0 {
		renderLyricsTile(renderer, config, font, lyrics, position, artX, artY, artSize)
	} else {
		renderMusicVisualizerBars(renderer, playing, paused, artX, artY, artSize, artAlpha)
	}

	textX := artX + artSize + 20
	textW := stripW - (textX - stripX) - 260
	musicStockOSLayout.text = sdl.Rect{X: textX, Y: stripY, W: textW, H: stripH}
	title := "Nothing playing"
	meta := "Pick a song from the list below"
	if cur >= 0 && cur < len(tracks) {
		title = musicTrackTitle(tracks[cur].Name)
		meta = fmt.Sprintf("%d of %d", cur+1, len(tracks))
		if d := tracks[cur].Duration; d > 0 {
			meta += "   ·   " + formatTime(d)
		}
		if musicPEQStatus() != "" {
			meta += "   ·   " + musicPEQStatus()
		}
	} else if musicPEQStatus() != "" {
		meta += "   ·   " + musicPEQStatus()
	}
	renderText(renderer, config, bigFont, ellipsize(bigFont, title, textW), ColorTextPrimary(), textX, stripY+22)
	metaY := stripY + 22 + int32(bigFont.Height()) + 6
	renderText(renderer, config, font, ellipsize(font, meta, textW), ColorTextTertiary(), textX, metaY)
	fontH := int32(font.Height())
	musicStockOSLayout.meta = sdl.Rect{X: textX, Y: metaY, W: textW, H: fontH}

	// Flat progress line, stock style: a thin track with the elapsed time under
	// the left end and the total under the right. It follows the meta line
	// rather than the strip's bottom edge: with the real display font the meta
	// line reached y=108 and the old fixed seek at y=108 painted through it.
	seekX := textX
	seekY := metaY + fontH + SpaceSM
	seekW := textW
	musicStockOSLayout.seek = sdl.Rect{X: seekX, Y: seekY, W: seekW, H: 4}
	musicStockOSLayout.times = sdl.Rect{X: seekX, Y: seekY + 10, W: seekW, H: fontH}
	fillRoundedRect(renderer, seekX, seekY, seekW, 4, 2, ColorTrack)
	frac := 0.0
	if duration > 0 {
		frac = position / duration
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	if frac > 0 {
		fillRoundedRect(renderer, seekX, seekY, int32(float64(seekW)*frac), 4, 2, accentColor)
	}
	musicRects.seek = sdl.Rect{X: seekX, Y: seekY - 10, W: seekW, H: 24}
	totalText := "--:--"
	if duration > 0 {
		totalText = formatTime(duration)
	}
	renderText(renderer, config, font, formatTime(position), ColorTextSecondary(), seekX, seekY+10)
	tw, _, _ := font.SizeUTF8(totalText)
	renderText(renderer, config, font, totalText, ColorTextSecondary(), seekX+seekW-int32(tw), seekY+10)

	// Transport and modes on the right of the strip.
	btnY := stripY + 20
	btnH := int32(46)
	playIcon := ">"
	if playing && !paused {
		playIcon = "II"
	}
	rightX := stripX + stripW - 16
	musicRects.next = musicButton(renderer, config, medFont, rightX-56, btnY, 56, btnH, ">|", false)
	musicRects.playPause = musicButton(renderer, config, medFont, rightX-56-70, btnY, 62, btnH, playIcon, true)
	musicRects.prev = musicButton(renderer, config, medFont, rightX-56-70-64, btnY, 56, btnH, "|<", false)
	// The mode labels were fixed-width at 62 and 56 px. At 145% text "Shuf"
	// needed more, so the pill painted "Sh..." and the button no longer said
	// what it did. Measure the labels and walk the mode row left from Stop.
	const modeGap = int32(8)
	stopW := int32(56)
	shuffleLabel := "Shuf"
	shuffleW := musicLabelWidth(font, shuffleLabel, 62)
	repeatLabel := repeatMode.short()
	repeatW := musicLabelWidth(font, repeatLabel, 56)
	stopX := rightX - stopW
	shuffleX := stopX - modeGap - shuffleW
	repeatX := shuffleX - modeGap - repeatW
	musicRects.stop = musicButton(renderer, config, font, stopX, btnY+btnH+8, stopW, 32, "[]", false)
	musicRects.shuffle = musicButton(renderer, config, font, shuffleX, btnY+btnH+8, shuffleW, 32, shuffleLabel, shuffleOn)
	musicRects.repeat = musicButton(renderer, config, font, repeatX, btnY+btnH+8, repeatW, 32, repeatLabel, repeatMode != musicRepeatOff)
	musicStockOSLayout.controls = sdl.Rect{X: repeatX, Y: btnY, W: rightX - repeatX, H: btnH + 8 + 32}

	// ── List ──
	listY := stripY + stripH + 16
	listH := screenHeight - listY - 62
	if listH < 120 {
		listH = 120
	}
	drawCard(renderer, stripX, listY, stripW, listH, 16)
	musicStockOSLayout.list = sdl.Rect{X: stripX, Y: listY, W: stripW, H: listH}

	header := fmt.Sprintf("All songs   ·   %d", len(tracks))
	renderText(renderer, config, font, header, ColorTextSecondary(), stripX+20, listY+14)

	exitLabel := "Exit"
	exitW := int32(70)
	if line := musicExitStatusLine(); line != "" {
		exitLabel = line
		if w, _, _ := font.SizeUTF8(exitLabel); int32(w)+24 > exitW {
			exitW = int32(w) + 24
		}
	}
	exitX := stripX + stripW - 16 - exitW
	searchW := int32(96)
	if tw, _, _ := font.SizeUTF8("Search"); int32(tw)+24 > searchW {
		searchW = int32(tw) + 24
	}
	searchX := exitX - 10 - searchW
	musicRects.exitTimer = musicHeaderButton(renderer, config, font, exitX, listY+9, exitW, 30, exitLabel, musicExitRemaining() > 0)
	musicRects.search = musicHeaderButton(renderer, config, font, searchX, listY+9, searchW, 30, "Search", musicSearchActive())

	rowsTop := listY + 48
	visible := int((listH - 58) / musicRowHeight)
	if visible < 1 {
		visible = 1
	}
	start := musicRowVisibleRange(cursor, len(tracks), visible)
	end := start + visible
	if end > len(tracks) {
		end = len(tracks)
	}
	musicRects.rowStart = start
	if len(tracks) == 0 {
		empty := "No audio files here. Open Files, enter a folder and pick a song."
		renderText(renderer, config, font, ellipsize(font, empty, stripW-40), ColorTextTertiary(), stripX+20, rowsTop+20)
	} else {
		for i := start; i < end; i++ {
			rowY := rowsTop + int32(i-start)*musicRowHeight
			row := sdl.Rect{X: stripX + 12, Y: rowY, W: stripW - 24, H: musicRowHeight - 6}
			if i == cursor {
				fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 8, WithAlpha(ColorCardFocus, 255))
				drawGlassFocusRing(renderer, row, 8)
			}
			if i == cur {
				renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 220)
				renderer.FillRect(&sdl.Rect{X: row.X + 2, Y: row.Y + 8, W: 3, H: row.H - 16})
			}
			num := strconv.Itoa(i+1) + "."
			renderText(renderer, config, font, num, ColorTextTertiary(), row.X+14, row.Y+(row.H-int32(font.Height()))/2)
			col := ColorTextPrimary()
			if i == cur {
				col = accentColor
			}
			durText := ""
			var durW int32
			if d := tracks[i].Duration; d > 0 {
				durText = formatTime(d)
				dw, _, _ := font.SizeUTF8(durText)
				durW = int32(dw)
			}
			nameX := row.X + 52
			nameW := row.W - durW - 100
			if nameW < 40 {
				nameW = 40
			}
			nameY := row.Y + (row.H-int32(font.Height()))/2
			renderText(renderer, config, font, ellipsize(font, musicTrackTitle(tracks[i].Name), nameW), col,
				nameX, nameY)
			if musicIsFavorite(tracks[i].Path) {
				fillCircle(renderer, row.X+row.W-26, row.Y+row.H/2, 4, accentColor)
			}
			if durText != "" {
				durX := row.X + row.W - durW - 40
				renderText(renderer, config, font, durText, ColorTextTertiary(), durX, nameY)
				musicStockOSLayout.rowDuration = sdl.Rect{X: durX, Y: nameY, W: durW, H: int32(font.Height())}
			}
			musicStockOSLayout.rowName = sdl.Rect{X: nameX, Y: nameY, W: nameW, H: int32(font.Height())}
			musicRects.rows = append(musicRects.rows, row)
		}
	}

	// "pad / key" pairs name the half the user can press (inputhints.go);
	// search has no pad button of its own here (Start → Search songs does it).
	hints := []FooterHint{{"A", "Play"}, {"B", "Back"}, {"Start / K", "Options"},
		{"L2 / G", "PEQ"}, {"Select / O", "Screen Off"}}
	if hintsUseKeyboard() {
		hints = append(hints, FooterHint{"/", "Search"})
	}
	hints = append(hints, FooterHint{Label: fmt.Sprintf("Vol %d%%", int(math.Round(volume*100)))})
	if queued > 0 {
		hints = append(hints, FooterHint{Label: fmt.Sprintf("%d Queued", queued)})
	}
	hint := hintLine(hints...)
	hw, _, _ := font.SizeUTF8(hint)
	musicStockOSLayout.hintY = screenHeight - 34
	musicStockOSLayout.hintInk = ColorTextMuted()
	renderText(renderer, config, font, hint, musicStockOSLayout.hintInk,
		(screenWidth-int32(hw))/2, musicStockOSLayout.hintY)

	// Header last, so the list and its hints cannot paint over the bar.
	renderStatusBar(renderer, config)
	renderMusicSearchOverlay(renderer, config)
	renderMusicPanels(renderer, config)
}
