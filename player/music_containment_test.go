package main

import (
	"fmt"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// musicContainmentRenderer makes a software renderer at a canvas size and
// restores the global surface when the test ends.
func musicContainmentRenderer(t *testing.T, w, h int32) *sdl.Renderer {
	t.Helper()
	surface, err := sdl.CreateRGBSurface(0, w, h, 32, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("create test surface: %v", err)
	}
	renderer, err := sdl.CreateSoftwareRenderer(surface)
	if err != nil {
		surface.Free()
		t.Fatalf("create software renderer: %v", err)
	}
	t.Cleanup(func() {
		renderer.Destroy()
		surface.Free()
	})
	return renderer
}

// musicContainmentTracks is a list long enough to fill the player's playlist
// and the search sheet's seven-row window.
func musicContainmentTracks() []musicTrack {
	names := []string{
		"Midnight City Lights (Extended Mix)", "The Long Way Home", "Neon Rain",
		"A Thousand Quiet Mornings", "Static Bloom", "Night Drive, Part II",
		"Paper Aeroplanes", "Slow Motion Summer", "Golden Hour Interlude",
		"After the Last Train", "Satellite Hearts", "Low Tide",
	}
	out := make([]musicTrack, 0, len(names))
	for i, name := range names {
		out = append(out, musicTrack{
			Path:     fmt.Sprintf("C:\\Music\\Night Drive\\%02d. %s.flac", i+1, name),
			Name:     fmt.Sprintf("%02d. %s.flac", i+1, name),
			Duration: 190 + float64(i*13),
		})
	}
	return out
}

func musicContainmentSeed(t *testing.T) {
	t.Helper()
	oldTracks, oldCurrent, oldCursor := music.tracks, music.current, music.cursor
	oldPlaying, oldPaused, oldReady := music.playing, music.paused, music.ready
	oldVisualizerFull := music.visualizerFull
	oldPosition, oldDuration := music.position, music.duration
	oldSearch, oldQuery, oldHits, oldSel := music.searchOpen, music.searchQuery, music.searchHits, music.searchSel
	oldLayout := musicStockOSLayout
	oldSettings := musicSettings
	oldEQPanel, oldEQSel := musicEQ.panel, musicEQ.sel
	t.Cleanup(func() {
		music.mu.Lock()
		music.tracks, music.current, music.cursor = oldTracks, oldCurrent, oldCursor
		music.playing, music.paused, music.ready = oldPlaying, oldPaused, oldReady
		music.visualizerFull = oldVisualizerFull
		music.position, music.duration = oldPosition, oldDuration
		music.searchOpen, music.searchQuery = oldSearch, oldQuery
		music.searchHits, music.searchSel = oldHits, oldSel
		music.mu.Unlock()
		musicStockOSLayout = oldLayout
		musicSettings = oldSettings
		musicEQ.panel, musicEQ.sel = oldEQPanel, oldEQSel
		musicSearchLayout = struct {
			card, field, hint sdl.Rect
			rows              int
		}{}
		// The player draws the shared bar. Its hit rects must not survive into
		// the next test: the focus-graph tests would scan phantom chrome.
		sceneTitleBackRect, tb.startRect, statusClockRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
		nc.bellRect, statusWiFiRect, weatherStatusRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
		statusMediaRect, updatePrompt.badge = sdl.Rect{}, sdl.Rect{}
		statusVolumeRect, statusBrightnessRect, statusBluetoothRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
		headerBugRect, headerTrophyRect = sdl.Rect{}, sdl.Rect{}
	})
	music.mu.Lock()
	music.tracks = musicContainmentTracks()
	music.current, music.cursor = 0, 0
	music.playing, music.paused, music.ready = true, false, true
	music.searchOpen = false
	music.searchQuery = ""
	music.searchHits = nil
	music.searchSel = -1
	music.mu.Unlock()
}

// TestMusicSearchSheetContainsItsRows draws the songs search sheet at both
// canvases and checks that the shared header, field, every hit and the hint
// line are inside the card and above the scene footer. The sheet's old fixed
// height painted the hint through the field and the last hit.
func TestMusicSearchSheetContainsItsRows(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	musicContainmentSeed(t)

	wide, okWide := deviceScreenProfileFor(DeviceTrimuiSmartPro)
	compact, okCompact := deviceScreenProfileFor(DeviceTrimuiBrick)
	if !okWide || !okCompact {
		t.Fatal("the supported device profiles are missing")
	}
	cfg := &Config{}
	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", wide.CanvasW, wide.CanvasH},
		{"compact", compact.CanvasW, compact.CanvasH},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			renderer := musicContainmentRenderer(t, c.width, c.height)
			footer := sceneFooterBand().Y

			music.mu.Lock()
			music.searchOpen = true
			music.searchQuery = "n"
			music.searchHits = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
			music.searchSel = 0
			music.mu.Unlock()
			renderMusicSearchOverlay(renderer, cfg)
			layout := musicSearchLayout

			if layout.card.W <= 0 || layout.card.H <= 0 {
				t.Fatalf("search card has no size: %+v", layout.card)
			}
			if layout.card.X < 0 || layout.card.X+layout.card.W > c.width ||
				layout.card.Y < 0 || layout.card.Y+layout.card.H > footer {
				t.Fatalf("search card = %+v, outside the %dx%d canvas above the footer at %d", layout.card, c.width, c.height, footer)
			}
			if layout.field.X < layout.card.X || layout.field.X+layout.field.W > layout.card.X+layout.card.W ||
				layout.field.Y < layout.card.Y || layout.field.Y+layout.field.H > layout.hint.Y {
				t.Fatalf("search field = %+v, not inside the card above the hint at %d", layout.field, layout.hint.Y)
			}
			if layout.rows != 7 {
				t.Fatalf("search rows = %d, want the seven-row window", layout.rows)
			}
			if len(musicSearchRowHits) != 7 {
				t.Fatalf("search drew %d hit rows, want 7", len(musicSearchRowHits))
			}
			for i, hit := range musicSearchRowHits {
				if hit.rect.X < layout.card.X || hit.rect.X+hit.rect.W > layout.card.X+layout.card.W {
					t.Fatalf("search hit %d = %+v, outside the card horizontally", i, hit.rect)
				}
				if hit.rect.Y < layout.field.Y+layout.field.H || hit.rect.Y+hit.rect.H > layout.hint.Y {
					t.Fatalf("search hit %d = %+v, outside the list above the hint at %d", i, hit.rect, layout.hint.Y)
				}
			}
			if layout.hint.X < layout.card.X || layout.hint.X+layout.hint.W > layout.card.X+layout.card.W ||
				layout.hint.Y+layout.hint.H > layout.card.Y+layout.card.H {
				t.Fatalf("search hint = %+v, outside the card %+v", layout.hint, layout.card)
			}

			// An empty result still reserves its line, so the hint cannot sit
			// on top of "No matches yet".
			music.mu.Lock()
			music.searchHits = nil
			music.searchSel = -1
			music.mu.Unlock()
			renderMusicSearchOverlay(renderer, cfg)
			layout = musicSearchLayout
			if layout.rows != 0 {
				t.Fatalf("empty search rows = %d, want 0", layout.rows)
			}
			if layout.field.Y+layout.field.H+12+34 > layout.hint.Y {
				t.Fatalf("empty-state row ends at %d, past the hint at %d", layout.field.Y+layout.field.H+12+34, layout.hint.Y)
			}
			music.mu.Lock()
			music.searchOpen = false
			music.mu.Unlock()
		})
	}
}

// TestMusicPEQLabelsDoNotCollide checks every selected band at both canvases.
// The selected band's label and the fifth-band label beside it used to be
// painted on top of each other (the 960 canvas showed "160200").
func TestMusicPEQLabelsDoNotCollide(t *testing.T) {
	legibleTestInitFonts(t)
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	cfg := &Config{}
	font, _ := getCachedFont(cfg, "small")
	if font == nil {
		t.Fatal("the PEQ font did not load")
	}

	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", 1280, 720},
		{"compact", 960, 720},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
				t.Fatalf("initialize SDL video: %v", err)
			}
			renderer := musicContainmentRenderer(t, c.width, c.height)
			musicEQ.panel = true
			musicEQ.sel = 9
			renderMusicPEQPanel(renderer, cfg)
			musicEQ.panel = false
			if musicPEQLayout.hint.Y < musicPEQLayout.panel.Y || musicPEQLayout.hint.Y+musicPEQLayout.hint.H > musicPEQLayout.bandTop {
				t.Fatalf("PEQ hint = %+v, not above the sliders at %d", musicPEQLayout.hint, musicPEQLayout.bandTop)
			}
			panel := musicPEQPanelRect()
			for selected := 0; selected < musicPEQBands; selected++ {
				type labelRect struct {
					band int
					rect sdl.Rect
				}
				shown := make([]labelRect, 0, musicPEQBands/5+2)
				for band := 0; band < musicPEQBands; band++ {
					if !musicPEQLabelShown(band, selected) {
						continue
					}
					r := musicPEQBandRect(panel, band)
					text := musicBandLabel(musicPEQFrequencies[band])
					tw, _, err := font.SizeUTF8(text)
					if err != nil {
						t.Fatalf("measure %q: %v", text, err)
					}
					x := r.X + (r.W-int32(tw))/2
					if x < panel.X || x+int32(tw) > panel.X+panel.W {
						t.Fatalf("selected %d: label %q at %d..%d, outside panel %d..%d", selected, text, x, x+int32(tw), panel.X, panel.X+panel.W)
					}
					if y := panel.Y + 96 + r.H + 8; y+int32(font.Height()) > panel.Y+panel.H-34 {
						t.Fatalf("selected %d: label %q ends at %d, into the panel footer", selected, text, y+int32(font.Height()))
					}
					shown = append(shown, labelRect{band: band, rect: sdl.Rect{X: x, Y: panel.Y + 96 + r.H + 8, W: int32(tw), H: int32(font.Height())}})
				}
				for i := 1; i < len(shown); i++ {
					prev, cur := shown[i-1], shown[i]
					if prev.rect.X+prev.rect.W > cur.rect.X {
						t.Fatalf("selected %d: labels for bands %d and %d overlap (%+v, %+v)", selected, prev.band, cur.band, prev.rect, cur.rect)
					}
				}
				if len(shown) < 5 {
					t.Fatalf("selected %d: only %d labels drawn", selected, len(shown))
				}
			}
			if musicPEQLabelShown(10, 9) {
				t.Fatal("the fifth-band label beside a selected band is still drawn")
			}
			if !musicPEQLabelShown(9, 9) {
				t.Fatal("the selected band's label is not drawn")
			}
		})
	}
}

// TestMusicStockOSKeepsTextClearOfControls locks the list-first strip: title
// and meta text end before the transport cluster, the controls stay inside the
// strip, and the queue rows stay inside their card above the hint line.
func TestMusicStockOSKeepsTextClearOfControls(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutStockOS)
	musicShelf.open = false
	t.Cleanup(func() { musicShelf.open = false })

	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", 1280, 720},
		{"compact", 960, 720},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			renderer := musicContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			l := musicStockOSLayout
			footer := sceneFooterBand().Y

			if l.strip.X < 0 || l.strip.X+l.strip.W > c.width || l.strip.Y < 0 || l.strip.Y+l.strip.H > footer {
				t.Fatalf("strip = %+v, outside the %dx%d canvas above the footer at %d", l.strip, c.width, c.height, footer)
			}
			if l.text.X < l.strip.X || l.text.X+l.text.W > l.controls.X {
				t.Fatalf("strip text = %+v, reaches the controls at %d", l.text, l.controls.X)
			}
			if l.controls.X < l.strip.X || l.controls.X+l.controls.W > l.strip.X+l.strip.W ||
				l.controls.Y < l.strip.Y || l.controls.Y+l.controls.H > l.strip.Y+l.strip.H {
				t.Fatalf("transport controls = %+v, outside the strip %+v", l.controls, l.strip)
			}
			if l.meta.Y+l.meta.H > l.seek.Y || l.seek.Y+l.seek.H > l.times.Y || l.times.Y+l.times.H > l.strip.Y+l.strip.H {
				t.Fatalf("strip lines overlap: meta=%+v seek=%+v times=%+v strip=%+v", l.meta, l.seek, l.times, l.strip)
			}
			if l.list.X < 0 || l.list.X+l.list.W > c.width || l.list.Y < l.strip.Y+l.strip.H || l.list.Y+l.list.H > footer {
				t.Fatalf("queue card = %+v, outside the canvas or overlapping the strip/footer", l.list)
			}
			if l.hintY < l.list.Y+l.list.H {
				t.Fatalf("hint line at %d, over the queue card ending at %d", l.hintY, l.list.Y+l.list.H)
			}
			if font, _ := getCachedFont(cfg, "small"); font != nil && l.hintY+int32(font.Height()) > c.height {
				t.Fatalf("hint line ends at %d, past the %d canvas", l.hintY+int32(font.Height()), c.height)
			}
			if len(musicRects.rows) == 0 {
				t.Fatal("the queue drew no rows")
			}
			for i, row := range musicRects.rows {
				if row.X < l.list.X || row.X+row.W > l.list.X+l.list.W ||
					row.Y < l.list.Y || row.Y+row.H > l.list.Y+l.list.H {
					t.Fatalf("queue row %d = %+v, outside the card %+v", i, row, l.list)
				}
			}
		})
	}
}

// TestMusicSettingsAndStreamCardsStayAboveFooter covers the two modal panels
// the audit found touching the scene footer band. Their rows must stay inside
// the card, and the card must end above the band at both canvases.
func TestMusicSettingsAndStreamCardsStayAboveFooter(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	oldStreams, oldLoaded, oldCursor, oldOpen := ipStreams, ipStreamsLoaded, ipStreamsCursor, ipPanelOpen
	oldTab, oldQuery, oldTabStart, oldStatus := ipPanelTab, ipPanelQuery, ipPanelTabStart, ipPanelStatus
	t.Cleanup(func() {
		screenWidth, screenHeight = oldW, oldH
		musicSettings.open = false
		ipStreamsMu.Lock()
		ipStreams, ipStreamsLoaded, ipStreamsCursor = oldStreams, oldLoaded, oldCursor
		ipStreamsMu.Unlock()
		ipPanelOpen = oldOpen
		ipPanelTab, ipPanelQuery, ipPanelTabStart, ipPanelStatus = oldTab, oldQuery, oldTabStart, oldStatus
	})
	musicContainmentSeed(t)
	cfg := &Config{}

	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", 1280, 720},
		{"compact", 960, 720},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			renderer := musicContainmentRenderer(t, c.width, c.height)
			footer := sceneFooterBand().Y

			musicSettings.open = true
			musicSettings.cursor = 11
			musicSettings.scroll = 6
			renderMusicSettingsPanel(renderer, cfg)
			card := musicSettings.panelRect
			if card.W <= 0 || card.H <= 0 {
				t.Fatalf("audio settings card has no size: %+v", card)
			}
			if card.Y < 0 || card.Y+card.H > footer {
				t.Fatalf("audio settings card = %+v, past the footer at %d", card, footer)
			}
			if len(musicSettings.rowHits) == 0 {
				t.Fatal("audio settings drew no rows")
			}
			for i, hit := range musicSettings.rowHits {
				if hit.rect.X < card.X || hit.rect.X+hit.rect.W > card.X+card.W ||
					hit.rect.Y < card.Y || hit.rect.Y+hit.rect.H > card.Y+card.H {
					t.Fatalf("audio settings row %d = %+v, outside the card %+v", i, hit.rect, card)
				}
			}
			musicSettings.open = false

			ipStreamsMu.Lock()
			ipStreamsLoaded = true
			ipStreams = []IPStream{
				{Name: "Nightwave Radio", URL: "https://example.test/nightwave.m3u8", Group: "Radio"},
				{Name: "Coastal FM", URL: "https://example.test/coastal.m3u8", Group: "Radio"},
				{Name: "World Service", URL: "https://example.test/world.m3u8", Group: "News"},
				{Name: "Studio One", URL: "https://example.test/studio1.m3u8", Group: "Music"},
				{Name: "Jazz After Dark", URL: "https://example.test/jazz.m3u8", Group: "Music"},
				{Name: "The Breakfast Show", URL: "https://example.test/breakfast.m3u8", Group: "Radio"},
				{Name: "Deep Space Network", URL: "https://example.test/deepspace.m3u8", Group: "Space"},
				{Name: "Local Weather Loop", URL: "https://example.test/weather.m3u8", Group: "News"},
			}
			ipStreamsCursor = 2
			ipStreamsMu.Unlock()
			ipPanelOpen = true
			ipPanelTab = 0
			ipPanelQuery = ""
			ipPanelTabStart = 0
			ipPanelStatus = ""
			renderIPPanel(renderer, cfg)
			panel := ipPanelRect
			if panel.W <= 0 || panel.H <= 0 {
				t.Fatalf("stream card has no size: %+v", panel)
			}
			if panel.Y < 0 || panel.Y+panel.H > footer {
				t.Fatalf("stream card = %+v, past the footer at %d", panel, footer)
			}
			if len(ipPanelRowRects) == 0 {
				t.Fatal("stream card drew no rows")
			}
			for i, row := range ipPanelRowRects {
				if row.X < panel.X || row.X+row.W > panel.X+panel.W ||
					row.Y < panel.Y || row.Y+row.H > panel.Y+panel.H {
					t.Fatalf("stream row %d = %+v, outside the card %+v", i, row, panel)
				}
			}
			ipPanelOpen = false
		})
	}
}
