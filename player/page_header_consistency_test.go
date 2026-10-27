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
	}

	buf := make([]byte, probeW*probeH*4)
	for _, s := range screens {
		undo := s.begin()
		renderer.SetDrawColor(0, 0, 0, 255)
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
