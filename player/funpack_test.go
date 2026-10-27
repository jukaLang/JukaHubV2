package main

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestOnboardingCoversCoreControls(t *testing.T) {
	if len(onboardingSteps) < 4 {
		t.Fatalf("tour has %d steps, want at least 4", len(onboardingSteps))
	}
	for i, step := range onboardingSteps {
		if step.title == "" || step.body == "" {
			t.Fatalf("step %d is missing title/body", i)
		}
	}
	if !onboardingDone(&Config{Variables: Variables{Custom: map[string]interface{}{"onboarding_done": true}}}) {
		t.Fatal("onboarding_done=true was not recognised")
	}
	if onboardingDone(&Config{Variables: Variables{Custom: map[string]interface{}{}}}) {
		t.Fatal("an empty config must not be treated as having completed the tour")
	}
}

func TestOnboardingReplayEntryIsIdempotent(t *testing.T) {
	cfg := &Config{Scenes: []SceneConfig{{Name: "SettingsGeneral"}}}
	ensureOnboardingEntry(cfg)
	ensureOnboardingEntry(cfg)
	count := 0
	for _, el := range cfg.Scenes[0].Elements {
		if el.Trigger == "onboarding_replay" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Replay tour entry added %d times, want 1", count)
	}
}

func TestPlaytimeRecordAndAchievements(t *testing.T) {
	playtime = playtimeState{loaded: true, data: newPlaytimeData()}
	playtimeRecord(30*60, "Games")
	if got := playtime.data.Total; got != 30*60 {
		t.Fatalf("total = %d, want 1800", got)
	}
	if got := playtime.data.Scenes["Games"]; got != 30*60 {
		t.Fatalf("Games seconds = %d, want 1800", got)
	}
	if got := playtime.data.Days[playtimeTodayKey()]; got != 30*60 {
		t.Fatalf("today seconds = %d, want 1800", got)
	}
	for _, id := range []string{"first_steps", "gamer"} {
		if _, ok := playtime.data.Unlocked[id]; !ok {
			t.Fatalf("achievement %q did not unlock", id)
		}
	}
	if _, ok := playtime.data.Unlocked["audiophile"]; ok {
		t.Fatal("Audiophile unlocked from game time")
	}
}

func TestPlaytimePersistenceRoundTrip(t *testing.T) {
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	playtime = playtimeState{loaded: true, data: newPlaytimeData()}
	playtimeRecord(125, "MusicPlayer")
	// Store the structured snapshot the same way playtimePersist does, without
	// triggering the debounced writer from a unit test.
	cfg.Variables.Custom[playtimeCustomKey] = playtime.data

	playtime = playtimeState{}
	playtimeInit(cfg)
	if got := playtime.data.Total; got != 125 {
		t.Fatalf("round-trip total = %d, want 125", got)
	}
	if got := playtime.data.Scenes["MusicPlayer"]; got != 125 {
		t.Fatalf("round-trip music seconds = %d, want 125", got)
	}
}

func TestEnsurePlaytimeSceneAndEntry(t *testing.T) {
	cfg := &Config{Scenes: []SceneConfig{{Name: "Misc"}}}
	ensurePlaytimeScene(cfg)
	ensurePlaytimeEntry(cfg)
	ensurePlaytimeEntry(cfg)

	if sceneByName(cfg, playtimeSceneName) == nil {
		t.Fatal("Playtime scene was not added")
	}
	if _, ok := lookupScene(playtimeSceneName); !ok {
		t.Fatal("Playtime scene is not registered")
	}
	count := 0
	for _, el := range cfg.Scenes[0].Elements {
		if el.TriggerTarget == playtimeSceneName {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Playtime entry added %d times, want 1", count)
	}
}

func TestFunScenesAppearInStartMenu(t *testing.T) {
	cfg := &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"},
		{Name: "Misc", Icon: "T"},
	}}
	ensureMusicScene(cfg)
	ensureGamesScene(cfg)
	ensurePlaytimeScene(cfg)
	ensurePlaytimeEntry(cfg)

	found := map[string]bool{}
	for _, app := range getStartMenuApps(cfg) {
		found[app.scene] = true
	}
	for _, name := range []string{"MusicPlayer", "Games", playtimeSceneName} {
		if !found[name] {
			t.Fatalf("Start menu is missing %q", name)
		}
	}
}

func TestGameListHasRequestedGames(t *testing.T) {
	want := map[string]bool{
		"Snake": false, "2048": false, "Tetris": false, "Minesweeper": false, "Flappy": false,
	}
	for _, g := range gameList {
		if _, ok := want[g.name]; ok {
			want[g.name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("game %q is missing from the menu", name)
		}
	}
}

func TestMinesweeperFirstMoveIsSafe(t *testing.T) {
	initMinesweeper()
	mines := 0
	for y := 0; y < msH; y++ {
		for x := 0; x < msW; x++ {
			if msMine[y][x] {
				mines++
			}
		}
	}
	if mines != msMines {
		t.Fatalf("board has %d mines, want %d", mines, msMines)
	}
	for y := msCursorY - 1; y <= msCursorY+1; y++ {
		for x := msCursorX - 1; x <= msCursorX+1; x++ {
			if x >= 0 && x < msW && y >= 0 && y < msH && msMine[y][x] {
				t.Fatal("first move area contains a mine")
			}
		}
	}
	msRevealAtCursor()
	if gameOver {
		t.Fatal("first move hit a mine")
	}
}

func TestFlappyFlapSetsUpwardVelocity(t *testing.T) {
	initFlappy()
	flappyVy = 300
	flappyFlap()
	if flappyVy >= 0 {
		t.Fatalf("flap velocity = %.0f, want an upward (negative) value", flappyVy)
	}
}

func TestMusicVisualizerToggle(t *testing.T) {
	music.mu.Lock()
	music.visualizerFull = false
	music.mu.Unlock()
	handleMusicInput(&sdl.KeyboardEvent{
		Type:   sdl.KEYDOWN,
		Keysym: sdl.Keysym{Sym: sdl.K_v},
	}, &Config{})
	music.mu.Lock()
	on := music.visualizerFull
	music.mu.Unlock()
	if !on {
		t.Fatal("V did not enable the full-screen visualizer")
	}
	handleMusicInput(&sdl.KeyboardEvent{
		Type:   sdl.KEYDOWN,
		Keysym: sdl.Keysym{Sym: sdl.K_v},
	}, &Config{})
}

// renderFunProbe renders one overlay or scene into the shared offscreen probe.
func renderFunProbe(t *testing.T, screen func(*sdl.Renderer)) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	screen(renderer)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f
}

func TestOnboardingRendersNextButton(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	SetAccentColor(sdl.Color{R: 52, G: 152, B: 219, A: 255})
	onboardingStart()
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		setAccentColorRaw(prevAccent)
		onboarding.active = false
	})

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	f := renderFunProbe(t, func(r *sdl.Renderer) { renderOnboarding(r, cfg) })
	if onboarding.next.W == 0 {
		t.Fatal("tour did not record its Next button rect")
	}
	btn := accentColor
	if got := f.countColor(btn, onboarding.next.X, onboarding.next.Y, onboarding.next.X+onboarding.next.W, onboarding.next.Y+onboarding.next.H); got < 500 {
		t.Fatalf("Next button painted only %d accent pixels", got)
	}
}

func TestPlaytimeRendersBars(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	SetAccentColor(sdl.Color{R: 52, G: 152, B: 219, A: 255})
	playtime = playtimeState{loaded: true, data: newPlaytimeData()}
	playtimeRecord(900, "Games")
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		setAccentColorRaw(prevAccent)
		playtime = playtimeState{}
	})

	f := renderFunProbe(t, func(r *sdl.Renderer) { renderPlaytime(r, &Config{}) })
	bar := accentColor
	if got := f.countColor(bar, 244, 240, 440, 430); got < 40 {
		t.Fatalf("playtime bars painted only %d accent pixels", got)
	}
}

func TestNewGameBoardsRender(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		activeGame = gameNone
		gameOver = false
	})

	startGame(gameMinesweeper)
	initMinesweeper()
	f := renderFunProbe(t, func(r *sdl.Renderer) { renderActiveGame(r, &Config{}) })
	cell := sdl.Color{R: 38, G: 48, B: 68, A: 255}
	if got := f.countColor(cell, 0, 0, probeW-1, probeH-1); got < 500 {
		t.Fatalf("Minesweeper board painted only %d cells", got)
	}

	startGame(gameFlappy)
	f = renderFunProbe(t, func(r *sdl.Renderer) { renderActiveGame(r, &Config{}) })
	sky := sdl.Color{R: 60, G: 150, B: 215, A: 255}
	if got := f.countColor(sky, 0, 0, probeW-1, probeH-1); got < 1000 {
		t.Fatalf("Flappy sky painted only %d pixels", got)
	}
}

// setAccentColorRaw restores the global without the config-facing setter.
func setAccentColorRaw(c sdl.Color) {
	accentColor = c
}
