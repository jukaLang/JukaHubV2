package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func menuContainmentRenderer(t *testing.T, w, h int32) *sdl.Renderer {
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

func menuContainmentProfiles(t *testing.T) []struct {
	name          string
	width, height int32
} {
	t.Helper()
	wide, okWide := deviceScreenProfileFor(DeviceTrimuiSmartPro)
	compact, okCompact := deviceScreenProfileFor(DeviceTrimuiBrick)
	if !okWide || !okCompact {
		t.Fatal("the supported device profiles are missing")
	}
	return []struct {
		name          string
		width, height int32
	}{
		{"wide", wide.CanvasW, wide.CanvasH},
		{"compact", compact.CanvasW, compact.CanvasH},
	}
}

type containmentProfile struct {
	name          string
	width, height int32
	scale         float64
}

// containmentProfiles covers both shipped canvases and the smallest supported
// space: the 960-wide compact canvas at the largest accessibility text size.
func containmentProfiles(t *testing.T) []containmentProfile {
	t.Helper()
	wide, okWide := deviceScreenProfileFor(DeviceTrimuiSmartPro)
	compact, okCompact := deviceScreenProfileFor(DeviceTrimuiBrick)
	if !okWide || !okCompact {
		t.Fatal("the supported device profiles are missing")
	}
	return []containmentProfile{
		{"wide-1280", wide.CanvasW, wide.CanvasH, 1.0},
		{"compact-960", compact.CanvasW, compact.CanvasH, 1.0},
		{"small-960-maxtext", compact.CanvasW, compact.CanvasH, a11yMaxScale},
	}
}

func containmentApplyProfile(t *testing.T, c containmentProfile) {
	t.Helper()
	screenWidth, screenHeight = c.width, c.height
	a11y.fontScale = c.scale
	a11yRefreshFonts()
	ClearTextCache()
}

// TestStartMenuHeaderStaysAboveFirstRow covers the Start menu's fixed 64 px
// header. At 145% text the subtitle ran into the first row's focus ring; the
// header now grows from the measured title and subtitle.
func TestStartMenuHeaderStaysAboveFirstRow(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH, oldScale := screenWidth, screenHeight, a11y.fontScale
	oldMenu := tb
	t.Cleanup(func() {
		screenWidth, screenHeight, a11y.fontScale = oldW, oldH, oldScale
		tb = oldMenu
		a11yRefreshFonts()
		ClearTextCache()
	})
	cfg := &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"}, {Name: "Playtime", Icon: "T"}, {Name: "Tube", Icon: "Y"},
		{Name: "LiveTV", Icon: "L"}, {Name: "FileExplorer", Icon: "F"}, {Name: "Chat", Icon: "C"},
		{Name: "Tools", Icon: "T"}, {Name: "Calculator", Icon: "C"}, {Name: "Pomodoro", Icon: "P"},
		{Name: "Alarm", Icon: "A"}, {Name: "Dashboard", Icon: "D"}, {Name: "Cron", Icon: "R"},
	}}

	for _, c := range containmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			containmentApplyProfile(t, c)
			tb.startOpen = true
			tb.startRect = sdl.Rect{X: 80, Y: 8, W: 84, H: 34}
			tb.group, tb.focus, tb.cursor, tb.scroll = 0, 1, 0, 0
			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderStartMenu(renderer, cfg)
			l := startMenuLayout
			if l.firstRow.W <= 0 || l.subtitle.W <= 0 {
				t.Fatalf("menu geometry missing: subtitle=%+v firstRow=%+v", l.subtitle, l.firstRow)
			}
			if l.subtitle.Y+l.subtitle.H+2 > l.firstRow.Y {
				t.Fatalf("subtitle %+v runs into first row %+v", l.subtitle, l.firstRow)
			}
			if l.title.Y+l.title.H > l.subtitle.Y {
				t.Fatalf("title %+v runs into subtitle %+v", l.title, l.subtitle)
			}
			group, _ := startMenuCurrentGroup(cfg)
			if len(group.items) > 0 {
				tb.cursor = len(group.items) - 1
				renderStartMenu(renderer, cfg)
				if len(startMenuItemHits) == 0 {
					t.Fatal("no rows drawn at the end of the list")
				}
				last := startMenuItemHits[len(startMenuItemHits)-1].rect
				if last.Y+last.H > startMenuLayout.footer.Y {
					t.Fatalf("last row %+v runs into the menu footer %+v", last, startMenuLayout.footer)
				}
			}
		})
	}
}

// TestMusicAuralisFooterFitsAtLargestText covers the artwork player at 145%
// text. The footer hints used the body face, so they ran off the right edge,
// clipped at the bottom and hid most of the bindings; the Search pill also
// collapsed to "Sear...".
func TestMusicAuralisFooterFitsAtLargestText(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH, oldScale := screenWidth, screenHeight, a11y.fontScale
	t.Cleanup(func() {
		screenWidth, screenHeight, a11y.fontScale = oldW, oldH, oldScale
		a11yRefreshFonts()
		ClearTextCache()
	})
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutAuralis)

	for _, c := range containmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			containmentApplyProfile(t, c)
			music.mu.Lock()
			music.tracks = []musicTrack{{
				Path:     "https://example.test/podcast/1.mp3",
				Name:     "Episode 42: A very long conversation about the state of handheld audio and what comes next",
				Duration: 3723,
			}}
			music.current, music.cursor = 0, 0
			music.playing, music.paused, music.ready = true, false, true
			music.visualizerFull = false
			music.mu.Unlock()

			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			l := musicAuralisLayout
			if len(l.hints) != 2 || len(l.hintLines) != 2 {
				t.Fatalf("footer hints = %d/%d, want 2/2", len(l.hints), len(l.hintLines))
			}
			for i, r := range l.hints {
				if r.X < 0 || r.Y < 0 || r.X+r.W > c.width || r.Y+r.H > c.height {
					t.Fatalf("hint %d = %+v, outside the %dx%d canvas", i, r, c.width, c.height)
				}
				if i > 0 && l.hints[i-1].Y+l.hints[i-1].H > r.Y {
					t.Fatalf("hints overlap: %+v and %+v", l.hints[i-1], r)
				}
			}
			// Drawn whole: the lines name the buttons in hand (musicPlayerHintLines).
			full := musicPlayerHintLines()
			if l.hintLines[0] != full[0] || l.hintLines[1] != full[1] {
				t.Fatalf("footer hints were truncated: %q / %q, want %q / %q", l.hintLines[0], l.hintLines[1], full[0], full[1])
			}
			font, _ := getCachedFont(cfg, "small")
			if font == nil {
				t.Fatal("small font did not load")
			}
			if tw, _, _ := font.SizeUTF8("Search"); l.search.W < int32(tw)+24 {
				t.Fatalf("Search pill width %d, needs %d for %q", l.search.W, int32(tw)+24, "Search")
			}
		})
	}
}

// TestMusicStockOSReservesDurationAndModeLabels covers the list-first layout at
// 145% text: the title ran under the duration, the Search pill collapsed and
// the fixed shuffle pill painted "Sh...".
func TestMusicStockOSReservesDurationAndModeLabels(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH, oldScale := screenWidth, screenHeight, a11y.fontScale
	t.Cleanup(func() {
		screenWidth, screenHeight, a11y.fontScale = oldW, oldH, oldScale
		a11yRefreshFonts()
		ClearTextCache()
	})
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutStockOS)

	for _, c := range containmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			containmentApplyProfile(t, c)
			music.mu.Lock()
			music.tracks = []musicTrack{{
				Path:     "https://example.test/podcast/1.mp3",
				Name:     "Episode 42: A very long conversation about the state of handheld audio and what comes next",
				Duration: 3723,
			}}
			music.current, music.cursor = 0, 0
			music.playing, music.paused, music.ready = true, false, true
			music.visualizerFull = false
			music.mu.Unlock()

			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			l := musicStockOSLayout
			if l.rowName.W <= 0 || l.rowDuration.W <= 0 || len(musicRects.rows) == 0 {
				t.Fatalf("row geometry missing: name=%+v duration=%+v rows=%d", l.rowName, l.rowDuration, len(musicRects.rows))
			}
			if l.rowName.X+l.rowName.W > l.rowDuration.X {
				t.Fatalf("track title %+v runs under duration %+v", l.rowName, l.rowDuration)
			}
			row := musicRects.rows[0]
			if l.rowDuration.X < row.X || l.rowDuration.X+l.rowDuration.W > row.X+row.W {
				t.Fatalf("duration %+v, outside its row %+v", l.rowDuration, row)
			}
			font, _ := getCachedFont(cfg, "small")
			if font == nil {
				t.Fatal("small font did not load")
			}
			if tw, _, _ := font.SizeUTF8("Search"); musicRects.search.W < int32(tw)+24 {
				t.Fatalf("Search pill width %d, needs %d", musicRects.search.W, int32(tw)+24)
			}
			if musicRects.shuffle.W < musicLabelWidth(font, "Shuf", 62) {
				t.Fatalf("shuffle pill width %d, needs %d", musicRects.shuffle.W, musicLabelWidth(font, "Shuf", 62))
			}
			repeat := musicRepeatOff.short()
			if musicRects.repeat.W < musicLabelWidth(font, repeat, 56) {
				t.Fatalf("repeat pill width %d, needs %d", musicRects.repeat.W, musicLabelWidth(font, repeat, 56))
			}
			if l.controls.X < l.text.X+l.text.W {
				t.Fatalf("controls %+v overlap the text column %+v", l.controls, l.text)
			}
		})
	}
}

// TestSettingsRailShowsScrollIndicator covers the Settings section rail. On the
// compact canvas its sections are taller than the rail; without a track the
// sections past the fold had no sign that they existed.
func TestSettingsRailShowsScrollIndicator(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH, oldScene := screenWidth, screenHeight, currentSceneIndex
	oldHub := setHub
	t.Cleanup(func() {
		screenWidth, screenHeight, currentSceneIndex = oldW, oldH, oldScene
		setHub = oldHub
	})
	cfg := &Config{Scenes: []SceneConfig{{Name: "Settings"}}}

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			currentSceneIndex = 0
			setHub.scene = "Settings"
			setHub.cfg = cfg
			setHub.visit = sceneWorkContext()
			setHub.lastSection, setHub.section = 0, 0
			setHub.onSearch, setHub.inPane = false, false
			setHub.sheet.open = false
			setHub.scroll, setHub.target, setHub.railScroll = 0, 0, 0
			setHub.dirty = true
			renderer := menuContainmentRenderer(t, c.width, c.height)
			setHubRender(renderer, cfg)
			bar, rail := setHub.railBar, setHub.railRect
			if bar.W > 0 {
				if bar.X < rail.X || bar.X+bar.W > rail.X+rail.W ||
					bar.Y < rail.Y || bar.Y+bar.H > rail.Y+rail.H {
					t.Fatalf("rail scrollbar = %+v, outside the rail %+v", bar, rail)
				}
			} else if c.name == "compact" {
				t.Fatalf("the compact rail overflows but drew no scrollbar (rail %+v)", rail)
			}
		})
	}
}

// TestMusicVisualizerBottomStackStaysApart covers the immersive now-playing
// screen. It used to put the state line and hint at fixed offsets from the
// canvas bottom, so "Playing · Vol" printed through the time and the hint
// through the state at both profiles.
func TestMusicVisualizerBottomStackStaysApart(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutAuralis)

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			music.mu.Lock()
			music.tracks = []musicTrack{{
				Path:     "https://example.test/podcast/1.mp3",
				Name:     "Episode 42: A very long conversation about the state of handheld audio and what comes next",
				Duration: 3723,
			}}
			music.current, music.cursor = 0, 0
			music.playing, music.paused, music.ready = true, false, true
			music.position, music.duration = 62, 3723
			music.visualizerFull = true
			music.mu.Unlock()

			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			l := musicVisualizerLayout
			for name, r := range map[string]sdl.Rect{
				"title": l.title, "meta": l.meta, "spectrum": l.spectrum,
				"rail": l.rail, "time": l.time, "state": l.state, "hint": l.hint,
			} {
				if r.W < 0 || r.H < 0 || r.X < 0 || r.Y < 0 ||
					r.X+r.W > c.width || r.Y+r.H > c.height {
					t.Fatalf("%s = %+v, outside the %dx%d canvas", name, r, c.width, c.height)
				}
			}
			if l.title.Y+l.title.H > l.meta.Y || l.meta.Y+l.meta.H > l.spectrum.Y {
				t.Fatalf("title/meta/spectrum overlap: title=%+v meta=%+v spectrum=%+v", l.title, l.meta, l.spectrum)
			}
			if l.spectrum.Y+l.spectrum.H > l.rail.Y ||
				l.rail.Y+l.rail.H > l.time.Y ||
				l.time.Y+l.time.H > l.state.Y ||
				l.state.Y+l.state.H > l.hint.Y {
				t.Fatalf("bottom stack overlaps: spectrum=%+v rail=%+v time=%+v state=%+v hint=%+v",
					l.spectrum, l.rail, l.time, l.state, l.hint)
			}
			if l.timeLabel != "01:02  /  62:03" {
				t.Fatalf("time label = %q", l.timeLabel)
			}
		})
	}
}

// TestMusicVisualizerEmptyStateHasNoStaleClock locks the "Nothing playing"
// screen: with no current track the clock must not show the previous queue's
// position and length.
func TestMusicVisualizerEmptyStateHasNoStaleClock(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutAuralis)

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			music.mu.Lock()
			music.tracks = nil
			music.current, music.cursor = -1, -1
			music.playing, music.ready = false, false
			music.position, music.duration = 62, 3723
			music.visualizerFull = true
			music.mu.Unlock()

			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			if got := musicVisualizerLayout.timeLabel; got != "00:00  /  --:--" {
				t.Fatalf("empty visualizer time label = %q, want 00:00 / --:--", got)
			}
		})
	}
}

// TestMusicAuralisEmptyQueueHasNoStaleClock is the artwork layout's equivalent:
// with no current track the transport must not keep the last song's position,
// duration or thumb offset.
func TestMusicAuralisEmptyQueueHasNoStaleClock(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutAuralis)

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			music.mu.Lock()
			music.tracks = nil
			music.current, music.cursor = -1, -1
			music.playing, music.ready = false, false
			music.position, music.duration = 62, 3723
			music.visualizerFull = false
			music.mu.Unlock()

			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			l := musicAuralisLayout
			if !l.emptyHint || l.elapsed != "00:00" || l.total != "--:--" || l.barFrac != 0 {
				t.Fatalf("empty artwork transport = empty:%v elapsed:%q total:%q frac:%v; want true/00:00/--:--/0",
					l.emptyHint, l.elapsed, l.total, l.barFrac)
			}
			if r := musicRects.seek; r.X < 0 || r.Y < 0 || r.X+r.W > c.width || r.Y+r.H > c.height {
				t.Fatalf("seek target = %+v, outside the %dx%d canvas", r, c.width, c.height)
			}
		})
	}
}

// TestMusicAuralisHintsStayBelowThePlaylist covers the artwork layout's footer
// on a podcast-length title. The hint stride is now measured from the font so
// the two lines cannot paint through each other.
func TestMusicAuralisHintsStayBelowThePlaylist(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	musicContainmentSeed(t)
	cfg := &Config{}
	musicSetLayout(cfg, musicLayoutAuralis)

	for _, c := range menuContainmentProfiles(t) {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			music.mu.Lock()
			music.tracks = []musicTrack{{
				Path:     "https://example.test/podcast/1.mp3",
				Name:     "Episode 42: A very long conversation about the state of handheld audio and what comes next",
				Duration: 3723,
			}}
			music.current, music.cursor = 0, 0
			music.playing, music.paused, music.ready = true, false, true
			music.visualizerFull = false
			music.mu.Unlock()

			renderer := menuContainmentRenderer(t, c.width, c.height)
			renderMusicPlayer(renderer, cfg)
			l := musicAuralisLayout
			if len(l.hints) != 2 {
				t.Fatalf("footer hints = %d, want 2", len(l.hints))
			}
			if l.list.Y+l.list.H > l.hints[0].Y {
				t.Fatalf("playlist ends at %d, under the first hint at %d", l.list.Y+l.list.H, l.hints[0].Y)
			}
			for i, r := range l.hints {
				if r.X < 0 || r.Y < 0 || r.X+r.W > c.width || r.Y+r.H > c.height {
					t.Fatalf("hint %d = %+v, outside the %dx%d canvas", i, r, c.width, c.height)
				}
				if i > 0 && l.hints[i-1].Y+l.hints[i-1].H > r.Y {
					t.Fatalf("hints overlap: %+v and %+v", l.hints[i-1], r)
				}
			}
		})
	}
}
