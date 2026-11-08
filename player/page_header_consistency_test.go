package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// page_header_consistency_test.go — the promise the header work makes: a
// full-screen panel opens with the same shared page header, so no screen can
// quietly drift back to a bespoke title.
//
// Every entry below used to lay its title band out by hand, with its own idea
// of where the title sat and where the subtitle went underneath it. They all
// build a UIPanelHeader now, and the band is the only thing this test looks at:
// a panel that stopped using the component has no bright title pixels in the
// strip the component owns, and says so.
//
// The same frames also pin the shared card recipe: the pixel beside the card is
// the backdrop under the shared navy dim (glassScrimColor at
// OverlayScrimAlpha), and the pixel inside the card near its top edge is the
// shared glass sheet (overlayCardFill). A panel that paints its own dim, or its
// own card fill, fails here even if its title still comes from the component.

func TestFullScreensShareThePageHeader(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	renderer := probeRenderer(t)

	screens := []struct {
		name string
		// begin opens the panel and returns the undo, so one screen's state
		// cannot leak into the next case.
		begin  func() func()
		render func(*sdl.Renderer, *Config)
		rect   func() sdl.Rect
		// fill is the card surface the page paints. Downloads recesses its card
		// so its raised rows read as separate bands; every other page is the
		// shared glass sheet.
		fill sdl.Color
	}{
		{
			name: "Games library",
			begin: func() func() {
				prev, prevRect := gameLib.open, gameLib.panel
				gameLib.open = true
				return func() { gameLib.open, gameLib.panel = prev, prevRect }
			},
			render: renderGameLibraryPanel,
			rect:   func() sdl.Rect { return gameLib.panel },
		},
		{
			name: "Emulators",
			begin: func() func() {
				prevOpen, prevCursor, prevRect := emuPanel.open, emuPanel.cursor, emuPanelRect
				emuPanel.open = true
				emuPanel.cursor = 0
				return func() { emuPanel.open, emuPanel.cursor, emuPanelRect = prevOpen, prevCursor, prevRect }
			},
			render: renderEmuPanel,
			rect:   func() sdl.Rect { return emuPanelRect },
		},
		{
			name: "Save manager",
			begin: func() func() {
				prev, prevRect := savePanel.open, savePanel.rect
				savePanel.open = true
				return func() { savePanel.open, savePanel.rect = prev, prevRect }
			},
			render: renderSaveManagerPanel,
			rect:   func() sdl.Rect { return savePanel.rect },
		},
		{
			name: "Downloads",
			begin: func() func() {
				prevOpen, prevTab, prevRect := dlPanel.open, dlPanel.tab, dlPanel.panel
				dlPanel.open = true
				dlPanel.tab = downloadTabAll
				return func() {
					dlPanel.open, dlPanel.tab, dlPanel.panel = prevOpen, prevTab, prevRect
				}
			},
			render: renderDownloadsPanel,
			rect:   func() sdl.Rect { return dlPanel.panel },
			fill:   glassSheetDeepFill(),
		},
		{
			name: "Curation hub",
			begin: func() func() {
				prev, prevRect := curationPanel.open, curationPanel.rect
				curationPanel.open = true
				return func() { curationPanel.open, curationPanel.rect = prev, prevRect }
			},
			render: renderCurationPanel,
			rect:   func() sdl.Rect { return curationPanel.rect },
		},
		{
			name: "Community",
			begin: func() func() {
				prevOpen, prevPage, prevRect := communityPanel.open, communityPanel.page, communityPanelRect
				communityPanel.open = true
				communityPanel.page = communityPageHome
				return func() {
					communityPanel.open, communityPanel.page = prevOpen, prevPage
					communityPanelRect = prevRect
				}
			},
			render: renderCommunityPanel,
			rect:   func() sdl.Rect { return communityPanelRect },
		},
		{
			name: "Compatibility",
			begin: func() func() {
				prev, prevRect := cpPanel.open, compatPanelRect
				cpPanel.open = true
				return func() { cpPanel.open, compatPanelRect = prev, prevRect }
			},
			render: renderCompatibilityPanel,
			rect:   func() sdl.Rect { return compatPanelRect },
		},
		{
			name: "Windows apps",
			begin: func() func() {
				prev, prevRect := winAppsPanel.open, windowsAppPanelRect
				winAppsPanel.open = true
				return func() { winAppsPanel.open, windowsAppPanelRect = prev, prevRect }
			},
			render: renderWindowsAppsPanel,
			rect:   func() sdl.Rect { return windowsAppPanelRect },
		},
		{
			name: "Game profiles",
			begin: func() func() {
				prev, prevRect := gpPanel.open, gameProfilePanelRect
				gpPanel.open = true
				return func() { gpPanel.open, gameProfilePanelRect = prev, prevRect }
			},
			render: renderGameProfilesPanel,
			rect:   func() sdl.Rect { return gameProfilePanelRect },
		},
		{
			name: "IP streams",
			begin: func() func() {
				prev, prevRect := ipPanelOpen, ipPanelRect
				ipPanelOpen = true
				return func() { ipPanelOpen, ipPanelRect = prev, prevRect }
			},
			render: renderIPPanel,
			rect:   func() sdl.Rect { return ipPanelRect },
		},
		{
			name: "Power tools",
			begin: func() func() {
				prevState, prevRect := powerTools, powerPanelRect
				powerTools.open = true
				powerTools.page = powerPageMenu
				return func() { powerTools, powerPanelRect = prevState, prevRect }
			},
			render: renderPowerToolsPanel,
			rect:   func() sdl.Rect { return powerPanelRect },
		},
		{
			name: "RetroAchievements",
			begin: func() func() {
				prevState, prevRect := raPanel, raPanelRect
				raPanel.open = true
				raPanel.settings = false
				return func() { raPanel, raPanelRect = prevState, prevRect }
			},
			render: renderRetroAchievementsPanel,
			rect:   func() sdl.Rect { return raPanelRect },
		},
		{
			name: "Game options",
			begin: func() func() {
				prev, prevGame, prevRect := gameOptsUI.open, gameOptsUI.game, gameOptsUI.panel
				gameOptsUI.open = true
				gameOptsUI.game = libraryGame{Title: "Chrono Trigger", System: "SNES"}
				return func() {
					gameOptsUI.open, gameOptsUI.game, gameOptsUI.panel = prev, prevGame, prevRect
				}
			},
			render: renderGameOptionsPanel,
			rect:   func() sdl.Rect { return gameOptsUI.panel },
		},
		{
			name: "Random game",
			begin: func() func() {
				prevState := gameRandomUI
				gameRandomUI.open = true
				return func() { gameRandomUI = prevState }
			},
			render: renderGameRandomPicker,
			rect:   func() sdl.Rect { return gameRandomUI.panel },
		},
		{
			name: "LAN Play",
			begin: func() func() {
				prevState := netplayPanel
				netplayPanel.open = true
				return func() { netplayPanel = prevState }
			},
			render: renderNetplayPanel,
			rect:   func() sdl.Rect { return netplayPanel.panel },
		},
		{
			name: "Community / What's new",
			begin: func() func() {
				prevOpen, prevPage, prevRect := communityPanel.open, communityPanel.page, communityPanelRect
				communityPanel.open = true
				communityPanel.page = communityPageChangelog
				return func() {
					communityPanel.open, communityPanel.page = prevOpen, prevPage
					communityPanelRect = prevRect
				}
			},
			render: renderCommunityPanel,
			rect:   func() sdl.Rect { return communityPanelRect },
		},
		{
			name: "Browser / links",
			begin: func() func() {
				prev, prevRect := browserLinkPanel, browserLinkPanelRect
				setBrowserLinks("browser_content", []browserLink{
					{Text: "Issue #1234 — the header runs across the card", URL: "https://example.com/1"},
					{Text: "Pull request #1240", URL: "https://example.com/2"},
				})
				browserLinkPanel = browserLinkPanelState{open: true, variable: "browser_content"}
				return func() {
					browserLinkPanel, browserLinkPanelRect = prev, prevRect
					setBrowserLinks("browser_content", nil)
				}
			},
			render: renderBrowserLinkPanel,
			rect:   func() sdl.Rect { return browserLinkPanelRect },
		},
		{
			name: "Browser / saved pages",
			begin: func() func() {
				browserBookmarks.mu.Lock()
				prevPath, prevList, prevLoaded := browserBookmarks.path, browserBookmarks.list, browserBookmarks.loaded
				// An empty path keeps a save during the frame off the user's real file.
				browserBookmarks.path = ""
				browserBookmarks.list = []browserBookmark{
					{Title: "Example issue", URL: "https://example.com/1"},
				}
				browserBookmarks.loaded = true
				browserBookmarks.mu.Unlock()
				prev := browserBookmarkPanel
				prevRect := browserBookmarkPanelRect
				browserBookmarkPanel.open = true
				return func() {
					browserBookmarkPanel, browserBookmarkPanelRect = prev, prevRect
					browserBookmarks.mu.Lock()
					browserBookmarks.path, browserBookmarks.list, browserBookmarks.loaded = prevPath, prevList, prevLoaded
					browserBookmarks.mu.Unlock()
				}
			},
			render: renderBrowserBookmarkPanel,
			rect:   func() sdl.Rect { return browserBookmarkPanelRect },
		},
		{
			name: "Browser / saved cookies",
			begin: func() func() {
				prevCookiesPath := browserCookiesPath
				browserCookiesPath = func() string { return filepath.Join(t.TempDir(), "cookies.json") }
				browserResetCookies()
				site, _ := url.Parse("https://example.com/")
				browserCookies().SetCookies(site, []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})
				prev := browserCookiePanel
				prevRect := browserCookiePanelRect
				browserCookiePanel.open = true
				return func() {
					browserCookiePanel, browserCookiePanelRect = prev, prevRect
					browserCookiesPath = prevCookiesPath
					browserResetCookies()
				}
			},
			render: renderBrowserCookiePanel,
			rect:   func() sdl.Rect { return browserCookiePanelRect },
		},
		{
			name: "LED",
			begin: func() func() {
				prev := ledPanel
				ledPanel.open = true
				ledPanel.row = 0
				return func() { ledPanel = prev }
			},
			render: renderLEDPanel,
			rect:   func() sdl.Rect { return ledPanel.rect },
		},
		{
			name: "Podcasts",
			begin: func() func() {
				prev := podPanel
				podPanel.open = true
				return func() { podPanel = prev }
			},
			render: renderPodcastPanel,
			rect:   func() sdl.Rect { return podcastPanelRect },
		},
		{
			name: "Audio settings",
			begin: func() func() {
				prev := musicSettings
				musicSettings.open = true
				return func() { musicSettings = prev }
			},
			render: renderMusicSettingsPanel,
			rect:   func() sdl.Rect { return musicSettings.panelRect },
		},
		{
			name: "Music PEQ",
			begin: func() func() {
				prev := musicEQ.panel
				prevSel := musicEQ.sel
				musicEQ.panel = true
				musicEQ.sel = 0
				return func() { musicEQ.panel, musicEQ.sel = prev, prevSel }
			},
			render: renderMusicPEQPanel,
			rect:   func() sdl.Rect { return musicPEQPanelRect() },
		},
		{
			// The Tools pages are full-screen scenes now; what they open over
			// themselves (a probe's result, a server's or a network's details,
			// the Wi-Fi diagnostics) is one shared sheet on the shared card.
			name: "Network tools / result sheet",
			begin: func() func() {
				prev := netTools.sheet
				netTools.sheet = toolsHubSheet{
					open:     true,
					title:    "Public IP",
					subtitle: "Updated just now",
					icon:     "public-ip",
					text:     "Public IP: 203.0.113.9\nCountry:   AU\nEdge:      SYD",
					pills: []toolsHubPill{
						{id: "run", label: "Run again", accent: true},
						{id: "copy", label: "Copy"},
						{id: "close", label: "Close"},
					},
				}
				return func() { netTools.sheet = prev }
			},
			render: func(r *sdl.Renderer, c *Config) { toolsHubDrawSheet(r, c, &netTools.sheet) },
			rect:   func() sdl.Rect { return netTools.sheet.card },
		},
		{
			name: "Wi-Fi / network details",
			begin: func() func() {
				prev := wifiScr.sheet
				wifiScr.sheet = toolsHubSheet{
					open:     true,
					title:    "HomeNet",
					subtitle: "Connected",
					icon:     "wifi",
					text:     "Status:     Connected\nSignal:     -47 dBm · excellent signal\nIP address: 192.168.1.20",
					pills: []toolsHubPill{
						{id: "reconnect", label: "Reconnect", accent: true},
						{id: "forget", label: "Forget", danger: true},
						{id: "close", label: "Close"},
					},
				}
				return func() { wifiScr.sheet = prev }
			},
			render: func(r *sdl.Renderer, c *Config) { toolsHubDrawSheet(r, c, &wifiScr.sheet) },
			rect:   func() sdl.Rect { return wifiScr.sheet.card },
		},
		{
			name: "Accessibility",
			begin: func() func() {
				prev, prevRect := a11y, a11yPanelRect
				a11y.open = true
				a11y.cursor = 0
				return func() { a11y, a11yPanelRect = prev, prevRect }
			},
			render: renderAccessibilityPanel,
			rect:   func() sdl.Rect { return a11yPanelRect },
		},
		{
			name: "Controller test",
			begin: func() func() {
				prev, prevRect := ctrldiag, ctrldiagPanelRect
				ctrldiag.open = true
				return func() { ctrldiag, ctrldiagPanelRect = prev, prevRect }
			},
			render: renderCtrldiagPanel,
			rect:   func() sdl.Rect { return ctrldiagPanelRect },
		},
		{
			name: "Memory tools",
			begin: func() func() {
				prev, prevRect := memPanel, memPanelRect
				memPanel.open = true
				memPanel.phase = memPhaseTarget
				return func() { memPanel, memPanelRect = prev, prevRect }
			},
			render: renderMemPanel,
			rect:   func() sdl.Rect { return memPanelRect },
		},
		{
			name: "Ports",
			begin: func() func() {
				prev, prevRect := portsPanel, portsPanelRect
				portsPanel.open = true
				portsPanel.cursor = 0
				return func() { portsPanel, portsPanelRect = prev, prevRect }
			},
			render: renderPortsPanel,
			rect:   func() sdl.Rect { return portsPanelRect },
		},
		{
			name: "Processes",
			begin: func() func() {
				prev, prevRect := processPanel, processPanelRect
				processPanel.open = true
				return func() { processPanel, processPanelRect = prev, prevRect }
			},
			render: renderProcessPanel,
			rect:   func() sdl.Rect { return processPanelRect },
		},
		{
			name: "PS Remote Play",
			begin: func() func() {
				prev, prevRect := chiakiPanel, chiakiPanelRect
				chiakiPanel.open = true
				chiakiPanel.cursor = 0
				return func() { chiakiPanel, chiakiPanelRect = prev, prevRect }
			},
			render: renderChiakiPanel,
			rect:   func() sdl.Rect { return chiakiPanelRect },
		},
		{
			name: "Smart home",
			begin: func() func() {
				prev, prevRect := smartHomePanel, smartHomePanelRect
				smartHomePanel.open = true
				smartHomePanel.cursor = 0
				return func() { smartHomePanel, smartHomePanelRect = prev, prevRect }
			},
			render: renderSmartHomePanel,
			rect:   func() sdl.Rect { return smartHomePanelRect },
		},
	}

	buf := make([]byte, probeW*probeH*4)
	// A known backdrop well away from the dim's own navy, so the shared dim is
	// measurable from the frame (a navy dim over a navy backdrop would read the
	// same with or without it).
	backdrop := sdl.Color{R: 150, G: 150, B: 150, A: 255}
	for _, s := range screens {
		undo := s.begin()
		renderer.SetDrawColor(backdrop.R, backdrop.G, backdrop.B, 255)
		renderer.Clear()
		s.render(renderer, config)
		card := s.rect()
		undo()

		if card.W <= 0 || card.H <= 0 {
			t.Errorf("%s: the panel did not lay out a card: %+v", s.name, card)
			continue
		}
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}

		// The shared scrim: a pixel beside the card is the backdrop under the
		// shared navy dim, not an undimmed scene and not a different dim.
		sx, sy := int(card.X)-6, int(card.Y+card.H/2)
		if sx < 0 {
			sx = 0
		}
		if sy >= int(probeH) {
			sy = int(probeH) - 1
		}
		i := (sy*int(probeW) + sx) * 4
		got := int(buf[i]) + int(buf[i+1]) + int(buf[i+2])
		dim := glassScrimColor(1)
		blend := func(under, over uint8) int {
			return (int(under)*(255-int(dim.A)) + int(over)*int(dim.A)) / 255
		}
		base := blend(backdrop.R, dim.R) + blend(backdrop.G, dim.G) + blend(backdrop.B, dim.B)
		if diff := got - base; diff > 10 || diff < -10 {
			t.Errorf("%s: the scrim beside the card read %d, want about %d for the shared dim", s.name, got, base)
		}

		// The shared card surface, sampled just inside the top edge away from
		// the rounded corner and the edge highlight.
		cx, cy := int(card.X+OverlayPadX), int(card.Y+6)
		if cx < int(probeW) && cy < int(probeH) {
			i := (cy*int(probeW) + cx) * 4
			got := int(buf[i]) + int(buf[i+1]) + int(buf[i+2])
			surface := s.fill
			if surface.A == 0 {
				surface = overlayCardFill()
			}
			want := int(surface.R) + int(surface.G) + int(surface.B)
			if diff := got - want; diff > 12 || diff < -12 {
				t.Errorf("%s: the card surface read %d, want %d (%+v)", s.name, got, want, surface)
			}
		}

		// The band the shared header owns: where the component starts the bar,
		// plus the room a title and a subtitle under it need. Rows begin below
		// this, so anything bright in here is the header itself.
		bright := 0
		x0, y0 := int(card.X), int(card.Y+UIPanelHeaderInset)
		x1, y1 := int(card.X+card.W), int(card.Y+UIPanelHeaderInset+64)
		for y := y0; y < y1 && y < int(probeH); y++ {
			for x := x0; x < x1 && x < int(probeW); x++ {
				i := (y*int(probeW) + x) * 4
				if int(buf[i])+int(buf[i+1])+int(buf[i+2]) > 420 {
					bright++
				}
			}
		}
		if bright < 80 {
			t.Errorf("%s: the header band drew %d bright pixels: the shared page header is missing",
				s.name, bright)
		}
	}
}
