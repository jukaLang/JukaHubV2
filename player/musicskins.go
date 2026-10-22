package main

// musicskins.go — the two Now Playing layouts.
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
)

func (l musicLayout) label() string {
	if l == musicLayoutStockOS {
		return "StockOS"
	}
	return "Auralis Pro"
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
	if l == musicLayoutStockOS {
		config.Variables.Custom[musicLayoutKey] = "stockos"
		return
	}
	config.Variables.Custom[musicLayoutKey] = "auralis"
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

	const headerH = int32(56)
	renderer.SetDrawColor(17, 23, 37, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: headerH})
	renderer.SetDrawColor(255, 255, 255, 18)
	renderer.FillRect(&sdl.Rect{X: 0, Y: headerH - 1, W: screenWidth, H: 1})
	renderText(renderer, config, medFont, "Audio Player", ColorTextPrimary(), 24, (headerH-int32(medFont.Height()))/2)
	renderText(renderer, config, font, "StockOS layout", ColorTextTertiary(), 190, (headerH-int32(font.Height()))/2)
	back := hintLine(FooterHint{"B", "Back"})
	bw, _, _ := font.SizeUTF8(back)
	renderText(renderer, config, font, back, ColorTextTertiary(), screenWidth-int32(bw)-24, (headerH-int32(font.Height()))/2)

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

	// ── Now playing strip ──
	const stripH = int32(150)
	stripX := int32(24)
	stripY := headerH + 16
	stripW := screenWidth - 2*stripX
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
	renderText(renderer, config, font, ellipsize(font, meta, textW), ColorTextTertiary(), textX, stripY+22+int32(bigFont.Height())+6)

	// Flat progress line, stock style: a thin track with the elapsed time under
	// the left end and the total under the right.
	seekX := textX
	seekY := stripY + stripH - 42
	seekW := textW
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
	musicRects.stop = musicButton(renderer, config, font, rightX-56, btnY+btnH+8, 56, 32, "[]", false)
	musicRects.shuffle = musicButton(renderer, config, font, rightX-56-70, btnY+btnH+8, 62, 32, "Shuf", shuffleOn)
	musicRects.repeat = musicButton(renderer, config, font, rightX-56-70-64, btnY+btnH+8, 56, 32, repeatMode.short(), repeatMode != musicRepeatOff)

	// ── List ──
	listY := stripY + stripH + 16
	listH := screenHeight - listY - 62
	if listH < 120 {
		listH = 120
	}
	drawCard(renderer, stripX, listY, stripW, listH, 16)

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
			nameW := row.W - 62 - 80
			renderText(renderer, config, font, ellipsize(font, musicTrackTitle(tracks[i].Name), nameW), col,
				row.X+52, row.Y+(row.H-int32(font.Height()))/2)
			if musicIsFavorite(tracks[i].Path) {
				fillCircle(renderer, row.X+row.W-26, row.Y+row.H/2, 4, accentColor)
			}
			if d := tracks[i].Duration; d > 0 {
				dur := formatTime(d)
				dw, _, _ := font.SizeUTF8(dur)
				renderText(renderer, config, font, dur, ColorTextTertiary(), row.X+row.W-int32(dw)-40, row.Y+(row.H-int32(font.Height()))/2)
			}
			musicRects.rows = append(musicRects.rows, row)
		}
	}

	hints := []FooterHint{{"A", "Play"}, {"B", "Back"}, {"/", "Search"},
		{"G", "PEQ"}, {"O", "Screen Off"},
		{Label: fmt.Sprintf("Vol %d%%", int(math.Round(volume*100)))}}
	if queued > 0 {
		hints = append(hints, FooterHint{Label: fmt.Sprintf("%d Queued", queued)})
	}
	hint := hintLine(hints...)
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, sdl.Color{R: 110, G: 125, B: 150, A: 170},
		(screenWidth-int32(hw))/2, screenHeight-34)

	renderMusicSearchOverlay(renderer, config)
	renderMusicPEQPanel(renderer, config)
}
