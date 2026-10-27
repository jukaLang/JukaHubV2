package main

// header_test.go — the top bar is one design on every screen.
//
// It used to be two. Home drew a 26px wordmark and a 16px caption on a scaled
// 56px band, put the release offer inside the status cluster (left of the
// weather text) and had no Back button; a scene drew a 24px wordmark and a 24px
// caption on a fixed 56/40px band, put the same offer beside the bell, and drew
// Back as a bare "<" in a 22px pill. These tests pin the single design: one bar
// height, one button height, the offer in the same corner, and a Back control
// that is bigger than the rest and changes colour when it takes focus.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// renderSceneBarForTest draws the real status bar over a non-home scene and
// returns the probe frame it drew into.
func renderSceneBarForTest(t *testing.T, scene string) *probeFrame {
	t.Helper()
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, scene)
	if idx < 0 {
		t.Fatalf("%s scene missing", scene)
	}
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()

	prevScene := currentSceneIndex
	prevHome := homeLayoutActive
	prevBack := sceneTitleBackRect
	prevStart, prevBell := tb.startRect, nc.bellRect
	prevBadge := updatePrompt.badge
	prevAccent := accentColor
	currentSceneIndex = idx
	homeLayoutActive = false
	sceneTitleBackRect = sdl.Rect{}
	tb.startRect, nc.bellRect = sdl.Rect{}, sdl.Rect{}
	updatePrompt.badge = sdl.Rect{}
	accentColor = ColorAccent
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		homeLayoutActive = prevHome
		sceneTitleBackRect = prevBack
		tb.startRect, nc.bellRect = prevStart, prevBell
		updatePrompt.badge = prevBadge
		accentColor = prevAccent
		ClearTextCache()
	})

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderStatusBar(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	return f
}

// TestSceneHeaderUsesTheSharedButtonHeight pins the bar's geometry: Menu and
// the release offer share one height, and Back is both wider and taller than
// them, because it is the control the bar exists to make obvious.
func TestSceneHeaderUsesTheSharedButtonHeight(t *testing.T) {
	snapshotHomeGlobals(t)
	withAvailableUpdate(t, "JukaHub-20261019120000")
	_ = renderSceneBarForTest(t, "Settings")

	barH := headerBarHeight()
	pillH := headerPillHeight()
	if sceneTitleBackRect.W == 0 {
		t.Fatal("the scene bar drew no Back button")
	}
	for name, r := range map[string]sdl.Rect{
		"menu":   tb.startRect,
		"update": updatePrompt.badge,
	} {
		if r.H != pillH {
			t.Errorf("%s pill is %dpx tall, want the shared %d", name, r.H, pillH)
		}
		if r.Y < 0 || r.Y+r.H > barH {
			t.Errorf("%s pill %+v escapes the %dpx bar", name, r, barH)
		}
	}
	if b := sceneTitleBackRect; b.Y < 0 || b.Y+b.H > barH {
		t.Errorf("Back pill %+v escapes the %dpx bar", b, barH)
	}
	if sceneTitleBackRect.H <= tb.startRect.H {
		t.Fatalf("Back is %dpx tall, the same as Menu or shorter: it is meant to stand a step above the bar's other buttons", sceneTitleBackRect.H)
	}
	if sceneTitleBackRect.W <= tb.startRect.W {
		t.Fatalf("Back (%dpx) is not wider than Menu (%dpx)", sceneTitleBackRect.W, tb.startRect.W)
	}
}

// TestUpdatePillSitsInTheSameCornerOnEveryScreen is the consistency guard: the
// release offer is immediately left of the bell on Home and in every scene, at
// the same size. One control, one place, one height.
func TestUpdatePillSitsInTheSameCornerOnEveryScreen(t *testing.T) {
	snapshotHomeGlobals(t)
	prevChip := homeUpdateRect
	t.Cleanup(func() { homeUpdateRect = prevChip })
	withAvailableUpdate(t, "JukaHub-20261019120000")

	// Home.
	homeUpdateProbe(t)
	homeChip, homeBell := homeUpdateRect, nc.bellRect
	if homeChip.W == 0 || homeBell.W == 0 {
		t.Fatalf("home probe drew chip %+v and bell %+v", homeChip, homeBell)
	}

	// A scene.
	renderSceneBarForTest(t, "Settings")
	sceneChip, sceneBell := updatePrompt.badge, nc.bellRect
	if sceneChip.W == 0 || sceneBell.W == 0 {
		t.Fatalf("scene bar drew chip %+v and bell %+v", sceneChip, sceneBell)
	}

	for name, pair := range map[string][2]sdl.Rect{
		"home":  {homeChip, homeBell},
		"scene": {sceneChip, sceneBell},
	} {
		chip, bell := pair[0], pair[1]
		if gap := bell.X - (chip.X + chip.W); gap < 0 || gap > 12 {
			t.Errorf("%s: chip %+v is not immediately left of the bell %+v (gap %d)", name, chip, bell, gap)
		}
		if chip.H != headerPillHeight() {
			t.Errorf("%s: chip is %dpx tall, want the shared %d", name, chip.H, headerPillHeight())
		}
	}
	if homeChip.W != sceneChip.W || homeChip.H != sceneChip.H {
		t.Fatalf("the offer is %dx%d on Home and %dx%d in a scene", homeChip.W, homeChip.H, sceneChip.W, sceneChip.H)
	}
}

// TestBackPillChangesColourWithFocus pins the visible state change: the idle
// pill is a tinted surface with accent type, and while focused it becomes a
// solid accent pill with dark type, so a controller user can see where focus is.
func TestBackPillChangesColourWithFocus(t *testing.T) {
	_ = newProbeFrame(t)
	withScreen(t, probeW, probeH)
	t.Cleanup(ClearTextCache)
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(r)

	config := &Config{}
	capture := func(focused bool) (sdl.Rect, []byte) {
		r.SetDrawColor(9, 14, 25, 255)
		r.Clear()
		rect := drawHeaderBack(r, config, 200, headerBarHeight(), focused)
		buf := make([]byte, probeW*probeH*4)
		if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed: %v", err)
		}
		return rect, buf
	}

	idle, idleBuf := capture(false)
	focused, focusedBuf := capture(true)
	if idle.W == 0 {
		t.Fatal("the Back pill was not drawn")
	}
	if idle != focused {
		t.Fatalf("the pill moved when it took focus: %+v vs %+v", idle, focused)
	}
	if idle.H < headerPillHeight()+headerBackLift {
		t.Fatalf("the Back pill is %dpx tall, want %dpx (a step above the %dpx shared button)",
			idle.H, headerPillHeight()+headerBackLift, headerPillHeight())
	}
	if idle.W < idle.H {
		t.Fatalf("the Back pill is %d×%d, too narrow to carry both a chevron and its label", idle.W, idle.H)
	}

	// Count pixels that are close to the accent colour inside the pill: the
	// focused state fills the pill with it, the idle state only tints.
	near := func(buf []byte, c sdl.Color) int {
		n := 0
		for y := idle.Y + 2; y < idle.Y+idle.H-2; y++ {
			for x := idle.X + 2; x < idle.X+idle.W-2; x++ {
				i := (y*probeW + x) * 4
				b, g, rr := buf[i], buf[i+1], buf[i+2]
				if absI(int(b)-int(c.B)) < 24 && absI(int(g)-int(c.G)) < 24 && absI(int(rr)-int(c.R)) < 24 {
					n++
				}
			}
		}
		return n
	}
	idleAccent := near(idleBuf, accentColor)
	focusedAccent := near(focusedBuf, accentColor)
	if focusedAccent < idleAccent*3 || focusedAccent < int(idle.W*idle.H)/3 {
		t.Fatalf("focus did not fill the pill with the accent: %d accent pixels idle, %d focused (pill %dx%d)",
			idleAccent, focusedAccent, idle.W, idle.H)
	}
}

// TestEveryScreenDrawsTheSharedBar walks the shipped scene list and renders
// each one. Every screen — including the two that used to draw their own strip,
// the music player and the terminal — must come out with the same bar: the app
// menu pill, the Back pill at the bar's own height, the release offer in the
// same corner, and the bell. This is the test the old bespoke headers failed:
// they drew a title and a text "B Back" hint and left every one of these rects
// empty.
func TestEveryScreenDrawsTheSharedBar(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	snapshotHomeGlobals(t)
	withAvailableUpdate(t, "JukaHub-20261019120000")

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	withScreen(t, probeW, probeH)
	renderer := newKeyboardTestRenderer(t)
	t.Cleanup(ClearTextCache)

	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig = config
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	pillH := headerPillHeight()
	backH := headerBackPillHeight()
	var offer sdl.Rect
	screens := 0
	for i := range config.Scenes {
		scene := config.Scenes[i]
		currentSceneIndex = i
		menuTilesApply(config, i)

		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderScene(renderer, config, config.Scenes[i])

		if scene.Layout == "home" {
			// Home has no Back (it is the root) and its own card grid, but the
			// offer and the bell are the same controls in the same corner.
			if homeUpdateRect.W == 0 || nc.bellRect.W == 0 {
				t.Errorf("%s: home header drew offer %+v and bell %+v", scene.Name, homeUpdateRect, nc.bellRect)
			}
			if offer.W == 0 {
				offer = homeUpdateRect
			} else if offer != homeUpdateRect {
				t.Errorf("%s: the offer is %+v here but %+v on the first screen", scene.Name, homeUpdateRect, offer)
			}
			continue
		}

		if tb.startRect.W == 0 || tb.startRect.H != pillH {
			t.Errorf("%s: the shared bar drew no Menu pill (rect %+v, want %dpx tall)", scene.Name, tb.startRect, pillH)
		}
		// JukaLand's B is a game action, so the bar deliberately omits Back
		// there; every other scene has to carry it.
		if scene.Name != "JukaLand" {
			if sceneTitleBackRect.W == 0 {
				t.Errorf("%s: no Back pill was drawn", scene.Name)
			} else if sceneTitleBackRect.H != backH {
				t.Errorf("%s: Back is %dpx tall, want the shared %d", scene.Name, sceneTitleBackRect.H, backH)
			}
		}
		if updatePrompt.badge.W == 0 {
			t.Errorf("%s: no release offer was drawn while one is available", scene.Name)
		} else {
			if offer.W == 0 {
				offer = updatePrompt.badge
			} else if offer != updatePrompt.badge {
				t.Errorf("%s: the offer is %+v here but %+v on the first screen", scene.Name, updatePrompt.badge, offer)
			}
			if gap := nc.bellRect.X - (updatePrompt.badge.X + updatePrompt.badge.W); gap != headerCornerGap {
				t.Errorf("%s: the offer sits %dpx from the bell, want the shared corner gap of %d", scene.Name, gap, headerCornerGap)
			}
		}
		if nc.bellRect.W == 0 {
			t.Errorf("%s: the notification bell is missing from the bar", scene.Name)
		}
		screens++
	}
	if screens < 20 {
		t.Fatalf("only %d scenes were checked", screens)
	}
}

// TestToolScreensDrawTheSharedBar is the guard for the five applets that used
// to skip the bar: Notes and the PDF reader drew a 44px strip with a medium
// title of their own, the alarm drew a bespoke "< Back" pill at 24,24, the
// Pomodoro timer drew no chrome at all, and the calculator drew no header.
// None of them is in a shipped config — they are registered modules a user's
// config adds — so the shipped-scene sweep above cannot see them.
func TestToolScreensDrawTheSharedBar(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	snapshotHomeGlobals(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// Append the applets the shipped configs do not carry, so renderScene takes
	// each scene's real module path.
	names := []string{"Notes", "Calculator", "PDFReader", "Alarm", "Pomodoro"}
	base := len(config.Scenes)
	for _, name := range names {
		config.Scenes = append(config.Scenes, SceneConfig{Name: name, Icon: "A"})
	}

	withScreen(t, probeW, probeH)
	renderer := newKeyboardTestRenderer(t)
	t.Cleanup(ClearTextCache)
	frame := newProbeFrame(t)

	prevConfig, prevScene := appConfig, currentSceneIndex
	prevNotes, prevPom, prevAlarm := notes, pom, alarmUI
	appConfig = config
	notes.tab = 0
	pom.phase = pomIdle
	alarmUI.tab = 0
	t.Cleanup(func() {
		appConfig, currentSceneIndex = prevConfig, prevScene
		notes, pom, alarmUI = prevNotes, prevPom, prevAlarm
	})

	barH := headerBarHeight()
	wantBackH := headerBackPillHeight()
	footer := HomeFooterColor()

	for i, name := range names {
		currentSceneIndex = base + i
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderScene(renderer, config, config.Scenes[currentSceneIndex])

		if tb.startRect.W == 0 {
			t.Errorf("%s: the shared bar drew no Menu pill", name)
		}
		if sceneTitleBackRect.W == 0 {
			t.Errorf("%s: the shared bar drew no Back pill", name)
		} else if sceneTitleBackRect.H != wantBackH {
			t.Errorf("%s: Back is %dpx tall, want the shared %d", name, sceneTitleBackRect.H, wantBackH)
		} else if sceneTitleBackRect.Y < 0 || sceneTitleBackRect.Y+sceneTitleBackRect.H > barH {
			t.Errorf("%s: Back pill %+v escapes the %dpx bar", name, sceneTitleBackRect, barH)
		}

		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
			unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("%s: ReadPixels failed on the offscreen renderer: %v", name, err)
		}

		// The wordmark sits at the bar's left margin; its absence means the
		// screen painted over the bar or skipped drawing it.
		bright := 0
		for y := int32(4); y < barH-4; y++ {
			for x := int32(StatusBarMargin); x < StatusBarMargin+140 && x < probeW; x++ {
				i := (int(y)*int(probeW) + int(x)) * 4
				if int(frame.buf[i])+int(frame.buf[i+1])+int(frame.buf[i+2]) > 420 {
					bright++
				}
			}
		}
		if bright < 40 {
			t.Errorf("%s: only %d bright wordmark pixels in the bar", name, bright)
		}

		// And the shared footer band is on screen at the bottom edge.
		i := (int(probeH-4)*int(probeW) + 4) * 4
		if absI(int(frame.buf[i+2])-int(footer.R)) > 2 ||
			absI(int(frame.buf[i+1])-int(footer.G)) > 2 ||
			absI(int(frame.buf[i])-int(footer.B)) > 2 {
			t.Errorf("%s: the bottom edge is not the shared footer surface (got r=%d g=%d b=%d, want %d/%d/%d)",
				name, frame.buf[i+2], frame.buf[i+1], frame.buf[i], footer.R, footer.G, footer.B)
		}
	}
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
