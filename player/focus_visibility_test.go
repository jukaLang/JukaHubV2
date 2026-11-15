package main

// focus_visibility_test.go — focus is a pixel, not a property.
//
// Each test paints a real-font frame into a software renderer, reads the frame
// back and inspects the focus ring itself. A ring has to be present on all four
// sides of the control, in the theme's focus ink (or the contrasting ink the
// solid buttons need), and it must not be present on the same control when it
// is only hovered or selected without focus.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

type focusFrame struct {
	w, h int32
	px   []byte
}

// focusRender paints one frame and reads it back. Every frame gets fresh
// texture caches because each call creates its own renderer.
func focusRender(t *testing.T, w, h int32, draw func(*sdl.Renderer)) focusFrame {
	t.Helper()
	surface, err := sdl.CreateRGBSurface(0, w, h, 32, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("create focus surface: %v", err)
	}
	renderer, err := sdl.CreateSoftwareRenderer(surface)
	if err != nil {
		surface.Free()
		t.Fatalf("create focus renderer: %v", err)
	}
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
	renderer.Clear()
	draw(renderer)
	px := make([]byte, int(w*h*4))
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&px[0]), int(w*4)); err != nil {
		renderer.Destroy()
		surface.Free()
		t.Fatalf("read focus frame: %v", err)
	}
	// The caches own textures created by this renderer. Clear them while the
	// renderer is still alive; clearing them after Destroy leaves dangling
	// texture pointers for the next test to destroy.
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	renderer.Destroy()
	surface.Free()
	return focusFrame{w: w, h: h, px: px}
}

func (f focusFrame) pixel(x, y int32) sdl.Color {
	if x < 0 || y < 0 || x >= f.w || y >= f.h {
		return sdl.Color{}
	}
	off := (int(y)*int(f.w) + int(x)) * 4
	// ARGB8888 is B,G,R,A in memory.
	return sdl.Color{R: f.px[off+2], G: f.px[off+1], B: f.px[off], A: f.px[off+3]}
}

func focusColorNear(a, b sdl.Color) bool {
	return abs8(a.R, b.R) <= 2 && abs8(a.G, b.G) <= 2 && abs8(a.B, b.B) <= 2
}

func focusFrameColorCount(f focusFrame, rect sdl.Rect, c sdl.Color) int {
	count := 0
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			if focusColorNear(f.pixel(x, y), c) {
				count++
			}
		}
	}
	return count
}

// focusRingSides counts the ring's ink in the four straight bands just inside
// the rect. Corners are rounded, so they are deliberately not sampled.
func focusRingSides(f focusFrame, rect sdl.Rect, ink sdl.Color) [4]int {
	th := glassFocusRingWidth
	if th < 1 {
		th = 1
	}
	inset := int32(8)
	if rect.W < 40 {
		inset = 3
	}
	var sides [4]int
	for y := rect.Y; y < rect.Y+th; y++ {
		for x := rect.X + inset; x < rect.X+rect.W-inset; x++ {
			if focusColorNear(f.pixel(x, y), ink) {
				sides[0]++
			}
		}
	}
	for y := rect.Y + rect.H - th; y < rect.Y+rect.H; y++ {
		for x := rect.X + inset; x < rect.X+rect.W-inset; x++ {
			if focusColorNear(f.pixel(x, y), ink) {
				sides[1]++
			}
		}
	}
	for x := rect.X; x < rect.X+th; x++ {
		for y := rect.Y + inset; y < rect.Y+rect.H-inset; y++ {
			if focusColorNear(f.pixel(x, y), ink) {
				sides[2]++
			}
		}
	}
	for x := rect.X + rect.W - th; x < rect.X+rect.W; x++ {
		for y := rect.Y + inset; y < rect.Y+rect.H-inset; y++ {
			if focusColorNear(f.pixel(x, y), ink) {
				sides[3]++
			}
		}
	}
	return sides
}

func focusAssertRing(t *testing.T, label string, f focusFrame, rect sdl.Rect, ink sdl.Color) {
	t.Helper()
	if rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("%s: focus rect is empty: %+v", label, rect)
	}
	sides := focusRingSides(f, rect, ink)
	min := int32(6)
	if rect.W < 60 {
		min = 3
	}
	for i, count := range sides {
		if int32(count) < min {
			t.Fatalf("%s: focus ring missing on side %d of %+v (ink %v, sides %v)",
				label, i, rect, ink, sides)
		}
	}
	// The middle of a ring is its fill, not its ink: prove the ring is an edge
	// rather than the control's whole surface having been painted in the ink.
	mid := f.pixel(rect.X+rect.W/2, rect.Y+rect.H/2)
	if focusColorNear(mid, ink) && focusFrameColorCount(f, rect, ink)*3 > int(rect.W*rect.H) {
		t.Fatalf("%s: the whole control is focus ink, so there is no distinguishable ring", label)
	}
}

func focusAssertNoRing(t *testing.T, label string, f focusFrame, rect sdl.Rect, ink sdl.Color) {
	t.Helper()
	sides := focusRingSides(f, rect, ink)
	// A selected row may keep a 1px hairline; the 3px focus ring must not be
	// there. Three or more pixels in every side band is the signature.
	for i, count := range sides {
		if count >= 6 {
			t.Fatalf("%s: non-focused row still has a ring on side %d (%+v, side counts %v)",
				label, i, rect, sides)
		}
	}
}

func focusTestTheme(t *testing.T, name string) {
	t.Helper()
	p, ok := themePresets[name]
	if !ok {
		t.Fatalf("theme %q missing", name)
	}
	a11y.increaseContrast = false
	a11y.highContrast = false
	a11y.colorblind = ""
	a11y.reduceTransparency = false
	ApplyThemeColors(p)
	glassThemeCache.valid = false
	homeFocusGlide = homeFocusGlideState{}
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
}

func focusTestProfile(c containmentProfile) {
	screenWidth, screenHeight = c.width, c.height
	a11y.fontScale = c.scale
	a11yRefreshFonts()
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
}

func focusTestRestore(t *testing.T) {
	t.Helper()
	prev := capturePalette()
	oldA11y := a11y
	oldW, oldH, oldScene := screenWidth, screenHeight, currentSceneIndex
	oldHub, oldMenu := setHub, tb
	t.Cleanup(func() {
		restorePalette(prev)
		a11y = oldA11y
		screenWidth, screenHeight, currentSceneIndex = oldW, oldH, oldScene
		setHub, tb = oldHub, oldMenu
		homeFocusGlide = homeFocusGlideState{}
		a11yRefreshFonts()
		ClearTextCache()
		ClearShapeCache()
		ClearIconAtlas()
		clearHomeTextCache()
	})
}

func focusMenuConfig() *Config {
	return &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"}, {Name: "Media", Icon: "M"}, {Name: "Tube", Icon: "Y"},
		{Name: "LiveTV", Icon: "L"}, {Name: "FileExplorer", Icon: "F"}, {Name: "Chat", Icon: "C"},
		{Name: "Tools", Icon: "T"}, {Name: "Calculator", Icon: "C"}, {Name: "Pomodoro", Icon: "P"},
		{Name: "Alarm", Icon: "A"}, {Name: "Dashboard", Icon: "D"}, {Name: "Cron", Icon: "R"},
		{Name: "Weather", Icon: "W"}, {Name: "Games", Icon: "G"}, {Name: "Settings", Icon: "S"},
	}}
}

func focusSeedMenu(cfg *Config, group, cursor, focus int) {
	tb.startOpen = true
	tb.startRect = sdl.Rect{X: 80, Y: 8, W: 84, H: 34}
	tb.group, tb.cursor, tb.focus = group, cursor, focus
	tb.scroll = 0
	tb.enter.cancel()
	startMenuClampScroll(cursor)
}

func focusSeedSettings(cfg *Config) {
	currentSceneIndex = 0
	setHub.scene = "Settings"
	setHub.cfg = cfg
	setHub.visit = sceneWorkContext()
	setHub.lastSection, setHub.section = 0, 0
	setHub.onSearch, setHub.inPane = false, false
	setHub.sheet.open = false
	setHub.follow = true
	setHub.scroll, setHub.target, setHub.railScroll = 0, 0, 0
	setHub.dirty = true
}

func focusSeedMusic(t *testing.T, cfg *Config, layout musicLayout, cursor int) {
	t.Helper()
	musicSetLayout(cfg, layout)
	music.mu.Lock()
	music.tracks = musicContainmentTracks()
	music.current, music.cursor = 0, cursor
	music.playing, music.paused, music.ready = true, false, true
	music.position, music.duration = 62, 3723
	music.visualizerFull = false
	music.searchOpen = false
	music.mu.Unlock()
}

func focusThemes() []string {
	return []string{"hub", "oled", "light", "minimal", "cute", "highcontrast", "cb-deuteranopia", "cb-protanopia", "cb-tritanopia"}
}

// TestFocusRingsPaintOnEveryNavigableSurface paints the four screens and walks
// the focus through the first and last row on each. Every ring is checked for
// all four sides, so a clip at a viewport edge or a fill painted over it fails.
func TestFocusRingsPaintOnEveryNavigableSurface(t *testing.T) {
	focusTestRestore(t)
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	musicContainmentSeed(t)
	cfg := focusMenuConfig()
	podcastShows := podcastContainmentShows()

	for _, theme := range focusThemes() {
		for _, profile := range containmentProfiles(t) {
			t.Run(theme+"/"+profile.name, func(t *testing.T) {
				focusTestProfile(profile)
				focusTestTheme(t, theme)
				focusSeedSettings(cfg)

				// Start menu: group rail, then the list, at both ends.
				focusSeedMenu(cfg, 0, 0, 0)
				f := focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderStartMenu(r, cfg)
				})
				if len(startMenuGroupHits) == 0 {
					t.Fatal("Start menu group rail drew no rows")
				}
				focusAssertRing(t, "Start group first", f, startMenuGroupHits[0].rect, glassAccent())

				focusSeedMenu(cfg, len(startMenuGroupHits)-1, 0, 0)
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderStartMenu(r, cfg)
				})
				focusAssertRing(t, "Start group last", f, startMenuGroupHits[len(startMenuGroupHits)-1].rect, glassAccent())

				focusSeedMenu(cfg, 0, 0, 1)
				group, _ := startMenuCurrentGroup(cfg)
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderStartMenu(r, cfg)
				})
				if len(startMenuItemHits) == 0 {
					t.Fatal("Start menu list drew no rows")
				}
				focusAssertRing(t, "Start list first", f, startMenuItemHits[0].rect, glassAccent())
				focusAssertNoRing(t, "Start group open without focus", f, startMenuGroupHits[0].rect, glassAccent())

				focusSeedMenu(cfg, 0, len(group.items)-1, 1)
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderStartMenu(r, cfg)
				})
				lastHit := startMenuItemHits[len(startMenuItemHits)-1]
				focusAssertRing(t, "Start list last", f, lastHit.rect, glassAccent())

				// Settings: rail ends, then pane rows at both ends.
				focusSeedSettings(cfg)
				setHub.onSearch, setHub.inPane = false, false
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					setHubRender(r, cfg)
				})
				if len(setHub.railRects) == 0 || setHub.railRects[0].H == 0 {
					t.Fatal("Settings rail drew no rows")
				}
				focusAssertRing(t, "Settings rail first", f, setHub.railRects[0], glassAccent())

				lastSection := len(setSections()) - 1
				focusSeedSettings(cfg)
				setHub.section, setHub.lastSection = lastSection, lastSection
				setHub.railScroll = 0
				for i := 0; i < 8; i++ {
					setHub.section = lastSection
					f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
						setHubRender(r, cfg)
					})
				}
				if r := setHub.railRects[lastSection]; r.H > 0 {
					focusAssertRing(t, "Settings rail last", f, r, glassAccent())
				} else {
					t.Fatalf("Settings last rail row is not visible after scrolling: %+v", r)
				}

				focusSeedSettings(cfg)
				setHub.inPane = true
				setHub.row = 0
				setHub.scroll, setHub.target = 0, 0
				for i := 0; i < 4; i++ {
					setHub.inPane, setHub.row = true, 0
					f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
						setHubRender(r, cfg)
					})
				}
				if len(setHub.rowRects) == 0 || setHub.rowRects[0].H == 0 {
					t.Fatal("Settings pane drew no rows")
				}
				focusAssertRing(t, "Settings pane first", f, setHub.rowRects[0], glassAccent())
				focusAssertNoRing(t, "Settings rail open without focus", f, setHub.railRects[0], glassAccent())

				lastRow := -1
				for i := len(setHub.rowRects) - 1; i >= 0; i-- {
					if setHub.rowRects[i].H > 0 {
						lastRow = i
						break
					}
				}
				if lastRow < 0 {
					t.Fatal("Settings pane has no visible row")
				}
				setHub.row = lastRow
				setHub.follow = true
				for i := 0; i < 10; i++ {
					setHub.inPane, setHub.row = true, lastRow
					f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
						setHubRender(r, cfg)
					})
				}
				focusAssertRing(t, "Settings pane last", f, setHub.rowRects[lastRow], glassAccent())

				// Podcasts: show column, then episode column, at both ends.
				podcastContainmentSeed(t, podcastShows)
				podPanel.column = podcastColumnShows
				podPanel.show, podPanel.firstShow = 0, 0
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderPodcastPanel(r, cfg)
				})
				focusAssertRing(t, "Podcast show first", f, podcastShowRowHits[0], glassAccent())

				podcastContainmentSeed(t, podcastShows)
				podPanel.column = podcastColumnShows
				podPanel.show = len(podcastShows) - 1
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderPodcastPanel(r, cfg)
				})
				focusAssertRing(t, "Podcast show last", f, podcastShowRowHits[len(podcastShowRowHits)-1], glassAccent())

				// The selected show keeps its mark while the episode column owns
				// focus, but it must not keep the focus ring.
				podcastContainmentSeed(t, podcastShows)
				podPanel.column = podcastColumnEpisodes
				podPanel.show, podPanel.episode = 0, 0
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderPodcastPanel(r, cfg)
				})
				focusAssertNoRing(t, "Podcast selected show without focus", f, podcastShowRowHits[0], glassAccent())

				podcastContainmentSeed(t, podcastShows)
				podPanel.column = podcastColumnEpisodes
				podPanel.show, podPanel.episode = 0, 0
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderPodcastPanel(r, cfg)
				})
				focusAssertRing(t, "Podcast episode first", f, podcastEpisodeHits[0], glassAccent())

				podcastContainmentSeed(t, podcastShows)
				podPanel.column = podcastColumnEpisodes
				podPanel.show = 0
				podPanel.episode = len(podcastShows[0].Episodes) - 1
				f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					renderPodcastPanel(r, cfg)
				})
				focusAssertRing(t, "Podcast episode last", f, podcastEpisodeHits[len(podcastEpisodeHits)-1], glassAccent())

				// Both music layouts: first and last playlist row.
				for _, layout := range []struct {
					name   string
					layout musicLayout
				}{{"Auralis", musicLayoutAuralis}, {"StockOS", musicLayoutStockOS}} {
					focusSeedMusic(t, cfg, layout.layout, 0)
					f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
						renderMusicPlayer(r, cfg)
					})
					if len(musicRects.rows) == 0 {
						t.Fatalf("%s drew no playlist rows", layout.name)
					}
					focusAssertRing(t, layout.name+" first", f, musicRects.rows[0], glassAccent())
					if len(musicRects.rows) > 1 {
						focusAssertNoRing(t, layout.name+" next row without focus", f, musicRects.rows[1], glassAccent())
					}

					focusSeedMusic(t, cfg, layout.layout, len(musicContainmentTracks())-1)
					f = focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
						renderMusicPlayer(r, cfg)
					})
					focusAssertRing(t, layout.name+" last", f, musicRects.rows[len(musicRects.rows)-1], glassAccent())
				}
			})
		}
	}
}

// TestFocusRingIsNotHoverAndSolidButtonsUseContrastingInk covers the shared
// button recipe and the hover-only row: a hover is a fill/hairline, not a ring;
// a focused solid accent button cannot use an accent-on-accent ring.
func TestFocusRingIsNotHoverAndSolidButtonsUseContrastingInk(t *testing.T) {
	focusTestRestore(t)
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	cfg := &Config{}
	for _, theme := range focusThemes() {
		for _, profile := range containmentProfiles(t) {
			t.Run(theme+"/"+profile.name, func(t *testing.T) {
				focusTestProfile(profile)
				focusTestTheme(t, theme)
				font, _ := getCachedFont(cfg, "small")
				if font == nil {
					t.Fatal("small font missing")
				}
				rect := sdl.Rect{X: 40, Y: 40, W: 240, H: 56}
				focused := focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					UIButton(r, cfg, rect, "Open", UIButtonStyle{Variant: UIButtonPrimary, Font: font}, UIButtonState{Focused: true})
				})
				focusAssertRing(t, "Primary focused", focused, glassLifted(rect), focusRingInk(lighten(accentColor, 10)))

				hovered := focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					UIButton(r, cfg, rect, "Open", UIButtonStyle{Variant: UIButtonPrimary, Font: font}, UIButtonState{Hovered: true})
				})
				focusAssertNoRing(t, "Primary hovered", hovered, glassLifted(rect), focusRingInk(lighten(accentColor, 10)))

				// The contrasting ring must also clear the button fill.
				ring := focusRingInk(lighten(accentColor, 10))
				if got := wcagContrast(ring, lighten(accentColor, 10)); got < 3.0 {
					t.Fatalf("%s/%s: solid-button focus ring %v on fill = %.2f:1, want >= 3:1",
						theme, profile.name, ring, got)
				}
				// A focused button at a clip edge must keep its ring: the lift is
				// abandoned rather than letting the clip eat three pixels.
				clipped := focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
					clip := rect
					_ = r.SetClipRect(&clip)
					UIButton(r, cfg, rect, "Open", UIButtonStyle{Variant: UIButtonSecondary, Font: font}, UIButtonState{Focused: true})
				})
				focusAssertRing(t, "Secondary focused at clip edge", clipped, rect, glassAccent())
			})
		}
	}
}

// TestFocusTokensAcrossCatalogue keeps the focus ink safe for every shipped
// preset, not just the eight rendered above. A catalogue entry can recolour the
// accent; the solid-button fallback and the row ring must stay distinguishable.
func TestFocusTokensAcrossCatalogue(t *testing.T) {
	focusTestRestore(t)
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	wide := containmentProfiles(t)[0]
	focusTestProfile(wide)
	for _, preset := range ListThemePresets() {
		t.Run(preset, func(t *testing.T) {
			p, ok := themePresets[preset]
			if !ok {
				t.Fatalf("theme preset %q missing", preset)
			}
			a11y.increaseContrast = false
			a11y.highContrast = false
			a11y.colorblind = ""
			a11y.reduceTransparency = false
			ApplyThemeColors(p)
			glassThemeCache.valid = false

			focus := glassAccent()
			rect := sdl.Rect{X: 40, Y: 40, W: 320, H: 44}
			focused := focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				UIListRow(r, rect, 10, true, false)
			})
			focusAssertRing(t, "catalogue row focused", focused, rect, focus)
			hovered := focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				UIListRow(r, rect, 10, false, true)
			})
			focusAssertNoRing(t, "catalogue row hovered", hovered, rect, focus)
			for _, surface := range []struct {
				name string
				c    sdl.Color
			}{{"background", ColorBackground}, {"surface", ColorSurface}, {"surface-alt", ColorSurfaceAlt}, {"raised", ColorSurfaceRaised}} {
				if got := wcagContrast(focus, surface.c); got < 3.0 {
					t.Fatalf("%s: focus ink %v on %s = %.2f:1, want >= 3:1",
						preset, focus, surface.name, got)
				}
			}

			primaryFill := uiButtonFocusFill(UIButtonPrimary, accentColor)
			ring := focusRingInk(primaryFill)
			if got := wcagContrast(ring, primaryFill); got < 3.0 {
				t.Fatalf("%s: solid-button ring %v on focus fill %v = %.2f:1, want >= 3:1",
					preset, ring, primaryFill, got)
			}
			if focusColorNear(ring, primaryFill) {
				t.Fatalf("%s: solid-button ring is its own fill", preset)
			}

			// A white accent saturates lighten(), so its focused and hovered
			// fills are both white. The contrasting ring is what distinguishes
			// focus there; the rendered button test proves hover has no ring.

			// Rows are told apart by the focus ring, not by a different wash:
			// the hover and focus glass fills are deliberately close. The
			// rendered row test above proves the focused row has the ring and
			// the hovered one does not.
		})
	}
}

// TestFocusKeyboardMovesAndPaints drives the real key handlers, not just the
// state fields, and checks the ring lands on the newly focused control. It runs
// on a near-black, a light and a white-accent theme so keyboard focus is not
// only proven on the default palette.
func TestFocusKeyboardMovesAndPaints(t *testing.T) {
	focusTestRestore(t)
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	wide := containmentProfiles(t)[0]
	cfg := focusMenuConfig()
	podcastShows := podcastContainmentShows()
	key := func(sym sdl.Keycode) *sdl.KeyboardEvent {
		return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
	}

	for _, theme := range []string{"hub", "light", "minimal"} {
		t.Run(theme, func(t *testing.T) {
			focusTestProfile(wide)
			focusTestTheme(t, theme)

			// Start: Down in the list, then Left to the rail and Down a group.
			focusSeedMenu(cfg, 0, 0, 1)
			startMenuNavigate(1, cfg)
			if tb.cursor != 1 || tb.focus != 1 {
				t.Fatalf("Start Down left cursor=%d focus=%d, want 1/1", tb.cursor, tb.focus)
			}
			f := focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				renderStartMenu(r, cfg)
			})
			focusAssertRing(t, "Start after Down", f, startMenuItemHits[1].rect, glassAccent())

			startMenuFocusRail()
			startMenuMoveGroup(1, cfg)
			if tb.focus != 0 || tb.group != 1 {
				t.Fatalf("Start Left+Down left focus=%d group=%d, want 0/1", tb.focus, tb.group)
			}
			f = focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				renderStartMenu(r, cfg)
			})
			focusAssertRing(t, "Start rail after Down", f, startMenuGroupHits[1].rect, glassAccent())

			// Settings: Down moves the rail, Right enters the pane, Down moves a row.
			focusSeedSettings(cfg)
			setHub.inPane = false
			setHubKey(key(sdl.K_DOWN), cfg)
			if setHub.section == 0 {
				t.Fatal("Settings Down did not move the rail")
			}
			f = focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				setHubRender(r, cfg)
			})
			focusAssertRing(t, "Settings rail after Down", f, setHub.railRects[setHub.section], glassAccent())

			setHub.inPane = false
			setHubKey(key(sdl.K_RIGHT), cfg)
			if !setHub.inPane {
				t.Fatal("Settings Right did not enter the pane")
			}
			setHubKey(key(sdl.K_DOWN), cfg)
			if setHub.row == 0 {
				t.Fatal("Settings pane Down did not move the row")
			}
			f = focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				setHubRender(r, cfg)
			})
			focusAssertRing(t, "Settings row after Down", f, setHub.rowRects[setHub.row], glassAccent())

			// Podcasts: Down moves the show cursor and the ring follows.
			podcastContainmentSeed(t, podcastShows)
			handlePodcastPanelKey(key(sdl.K_DOWN), cfg)
			if podPanel.show != 1 {
				t.Fatalf("Podcast Down left show=%d, want 1", podPanel.show)
			}
			f = focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
				renderPodcastPanel(r, cfg)
			})
			focusAssertRing(t, "Podcast after Down", f, podcastShowRowHits[1], glassAccent())

			// Both music layouts move their playlist cursor with Down.
			for _, layout := range []struct {
				name   string
				layout musicLayout
			}{{"Auralis", musicLayoutAuralis}, {"StockOS", musicLayoutStockOS}} {
				focusSeedMusic(t, cfg, layout.layout, 0)
				handleMusicInput(key(sdl.K_DOWN), cfg)
				music.mu.Lock()
				cursor := music.cursor
				music.mu.Unlock()
				if cursor != 1 {
					t.Fatalf("%s Down left cursor=%d, want 1", layout.name, cursor)
				}
				f = focusRender(t, wide.width, wide.height, func(r *sdl.Renderer) {
					renderMusicPlayer(r, cfg)
				})
				focusAssertRing(t, layout.name+" after Down", f, musicRects.rows[1], glassAccent())
			}
		})
	}
}

// TestFocusRowHoverHasNoRing proves the same distinction on a list row, which
// is the most common focus target. The chosen theme (Hub) has an accent far
// enough from the row fill that the ring is the theme focus ink.
func TestFocusRowHoverHasNoRing(t *testing.T) {
	focusTestRestore(t)
	legibleTestInitFonts(t)
	focusTestTheme(t, "hub")
	for _, profile := range containmentProfiles(t) {
		t.Run(profile.name, func(t *testing.T) {
			focusTestProfile(profile)
			rect := sdl.Rect{X: 40, Y: 40, W: 320, H: 44}
			focused := focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
				UIListRow(r, rect, 10, true, false)
			})
			focusAssertRing(t, "list row focused", focused, rect, glassAccent())
			hovered := focusRender(t, profile.width, profile.height, func(r *sdl.Renderer) {
				UIListRow(r, rect, 10, false, true)
			})
			focusAssertNoRing(t, "list row hovered", hovered, rect, glassAccent())
		})
	}
}
