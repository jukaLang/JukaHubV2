package main

// gameswitcher.go — the Game switcher: your recent games side by side, each
// at the moment you left it.
//
// NextUI and Onion OS both have one, and it is the feature their users name
// first: a full-screen strip of the recently played games, every card a
// screenshot of where that game was left, Left / Right to choose and A to be
// back in it — two presses from one game to another. JukaHub already had
// every part except the strip itself:
//
//   - the recents list (game-recents.json, recordGameLaunch in
//     game_library.go), which the Continue card reads;
//   - the resume: every RetroArch launch from the library writes an auto state
//     when it quits and loads it when it starts (savestate_resume.go), so
//     starting a recent game again *is* resuming it;
//   - the save states and the pictures beside them, found the way the game
//     page and the in-game menu find them (gmeta_states.go).
//
// What this file adds is the screenshot switch, the screen and the ways in.
// RetroArch writes "<state>.png" beside a state ("Game.state.auto.png",
// "Game.state3.png") only with savestate_thumbnail_enable on, which is off
// by default; prepareGameSwitcher turns it on for every RetroArch launch from
// the library through the scoped config edit (racfgscope.go), so the user's
// own value is back when the game exits (LIB-11). The game page's states strip
// (gmeta_states.go) shows the same pictures, so this holds with the switcher
// off too. The in-game menu turns it on as well when it can open
// (savestateMenuValues); this covers games where it cannot.
//
// A card's picture is the newest state picture the game has (the auto state's
// or a numbered slot's); a game with none shows its box art, and a game with
// neither a plain card with its console's name. The pictures are decoded off
// the render thread, one at a time under the shared decode permit, uploaded
// one per frame, and freed when the screen is left.
//
// Ways in: X on Home (the Standard and Hero layouts, where X had no job;
// the Carousel and Widgets keep X for Edit Home), Start → Games → Game
// switcher, Home search ("switcher", "recent games"), and the in-game menu's
// "Game switcher" row, which quits the game the way Quit to JukaHub does —
// RetroArch writes its auto state on the way out — and opens the switcher
// as soon as JukaHub has the screen back. Settings → Games & emulators →
// Game switcher (`gameSwitcher`, on unless false) turns all of it off.
//
// On the screen: Left / Right choose (L1 / R1 jump to the ends), A resumes
// the game, X removes it from the recent games (its saves stay), B goes back.
// The drawing is in gameswitcher_ui.go.

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gameSwitcherKey is the Custom setting behind Settings → Games &
	// emulators → Game switcher. Missing means on.
	gameSwitcherKey = "gameSwitcher"
	// gameSwitcherSceneName is the in-memory scene the screen lives in; it
	// has no Icon, so the Start menu's Screens list leaves it out and the
	// Games group's own row (gameSwitcherEntryName) is the way there.
	gameSwitcherSceneName = "Game switcher"
	gameSwitcherEntryName = "Game switcher"

	// gswPicW × gswPicH is the box a card picture is decoded into. A state
	// screenshot is the core's own resolution (240 × 160 for a GBA, 320 × 240
	// for a PlayStation) and is never enlarged here; box art is shrunk.
	gswPicW = 640
	gswPicH = 480
	// gswThumbSlack is how much older than its state a state picture may be
	// and still show it: RetroArch writes the two together, and a picture
	// left from a save made with pictures on would otherwise show an older
	// moment than the state that replaced it with them off.
	gswThumbSlack = 30 * time.Second
	// gswAfterGameWait bounds the wait for the screen after the in-game menu's
	// Game switcher row: the quit itself is bounded by gmQuitGrace, and a
	// switcher that opened minutes later, out of nowhere, would be a surprise.
	gswAfterGameWait = 30 * time.Second
	gswAfterGamePoll = 150 * time.Millisecond
)

// gameSwitcherEnabled reports the setting; a config that predates it, or no
// config yet, gets the switcher.
func gameSwitcherEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, gameSwitcherKey, true)
}

// gameSwitcherSettingsRow is the switch in Settings → Games & emulators.
func gameSwitcherSettingsRow() setRow {
	return setRow{
		ID: gameSwitcherKey, Label: "Game switcher", Kind: setKindToggle,
		Detail: setStatic("Your recent games side by side, each where you left it: X on Home, Start → Games, or the in-game menu"),
		Get:    gameSwitcherEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, gameSwitcherKey, on)
			setToast("Game switcher: " + setOnOff(on))
		},
	}
}

// gameSwitcherValues is the one RetroArch switch the state pictures need: the
// switcher's cards and the game page's states strip.
func gameSwitcherValues() []raScopeValue {
	return []raScopeValue{{Key: "savestate_thumbnail_enable", Value: "true"}}
}

// prepareGameSwitcher is the launch-time hook (launchLibraryGame): a RetroArch
// game started from the library writes a picture beside each state it saves,
// the auto state on exit included, for this launch only (racfgscope.go), the
// way the OSD preset is applied (osdpreset.go). Not tied to the switcher's
// setting: the game page shows the pictures as well, and a switcher turned
// on later then has them. The key's write is skipped when the file already
// holds it (raScopePlan), so the in-game menu's identical value costs nothing
// twice. It never blocks or fails a launch.
func prepareGameSwitcher(launcher string, game libraryGame) {
	if strings.TrimSpace(game.Path) == "" || !retroArchLauncher(launcher) {
		return
	}
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if _, err := raScopeApply(path, gameSwitcherValues()); err != nil {
			logWarn("switcher", "cannot turn on save-state pictures in %s: %v", path, err)
		}
	}
}

// ── Cards ────────────────────────────────────────────────────────────────

// gswCard is one recent game on the strip.
type gswCard struct {
	game recentGame
	// shot is the newest state picture, art the box art; either may be "".
	shot string
	art  string
	// auto is when the auto state — what A resumes from — was written, zero
	// for a game without one (a standalone emulator's, a port).
	auto time.Time
	// The uploaded picture, render thread only; fromShot says which file it
	// came from.
	tex        *sdl.Texture
	texW, texH int32
	fromShot   bool
}

// gswPic is a decoded picture waiting for its upload.
type gswPic struct {
	img      *image.NRGBA
	fromShot bool
}

// gsw is the screen's state. Render thread only.
var gsw struct {
	visit  tbxVisit
	gen    uint64
	loaded bool
	cards  []gswCard
	cursor int
	// pos is the strip's position in cards, easing toward cursor; lastMS the
	// clock at the last frame, for the ease.
	pos    float64
	lastMS uint64
	// stamp is the newest recent's PlayedAt when the cards were read: a game
	// started meanwhile (a voice command, a launch the switcher did not
	// make) reads them again (gswRecentsStamp).
	stamp time.Time
	// owner is the renderer the textures belong to.
	owner *sdl.Renderer
	// The words under the strip and the header's count, rebuilt when the
	// card, the number of cards or the second changes (gswInfo).
	infoFor   int
	infoLen   int
	infoAt    uint64
	infoTitle string
	infoLine  string
	infoCount string
}

// gswRecentsStamp is the newest recent game's PlayedAt, read without copying
// the list (it is asked every frame the screen is up).
func gswRecentsStamp() time.Time {
	gameRecentsMu.Lock()
	defer gameRecentsMu.Unlock()
	if len(gameRecents) == 0 {
		return time.Time{}
	}
	return gameRecents[0].PlayedAt
}

// gswLoad hands the reader goroutine's work to the render thread.
var gswLoad struct {
	mu    sync.Mutex
	gen   uint64
	ready bool
	cards []gswCard
	stamp time.Time         // gsw.stamp of these cards
	pics  map[string]gswPic // by game path
}

// gswAfterGame is set by the in-game menu's row until the switcher opens (or
// the wait for the screen gives up). Any goroutine.
var gswAfterGame atomic.Bool

// gswHasStates reports whether a system's games can have RetroArch states:
// Android, HTML5 and port games cannot, and are not searched for them.
func gswHasStates(system string) bool {
	return system != androidLibrarySystem && system != html5LibrarySystem && system != portsLibrarySystem
}

// gswStates reads a game's states: when its auto state was written, and the
// newest usable picture of the auto state and the numbered slots. The auto
// state is nearly always that newest one, since RetroArch writes it as the
// game quits. The previous auto save (gmetaPrevAutoSlot) is left out: it is
// older than the auto state by definition, and starting the game does not
// load it.
func gswStates(romPath string) (auto time.Time, shot string) {
	var shotTime time.Time
	for _, st := range gmetaListStates(romPath, gmetaStateDirs(romPath)) {
		if st.Slot == gmetaPrevAutoSlot {
			continue
		}
		if st.Slot == gmetaAutoSlot && st.Time.After(auto) {
			auto = st.Time
		}
		if st.Thumb != "" && (shot == "" || st.Time.After(shotTime)) && gswThumbFresh(st) {
			shot, shotTime = st.Thumb, st.Time
		}
	}
	return auto, shot
}

// gswThumbFresh reports whether a state's picture was written with it rather
// than left over from an older save (gswThumbSlack).
func gswThumbFresh(st gmetaState) bool {
	info, err := os.Stat(st.Thumb)
	if err != nil {
		return false
	}
	return !info.ModTime().Before(st.Time.Add(-gswThumbSlack))
}

// gswBuildCards turns the recents into cards: games whose file is gone are
// left out (starting them could only fail), the rest keep the recents' order,
// newest first. It reads the card, so it runs off the render thread. At most
// maxRecentGames cards, so at most that many pictures are decoded and held as
// textures for a visit (each fits gswPicW × gswPicH).
func gswBuildCards(recents []recentGame) []gswCard {
	cards := make([]gswCard, 0, min(len(recents), maxRecentGames))
	for _, g := range recents {
		if len(cards) == maxRecentGames {
			break
		}
		if strings.TrimSpace(g.Path) == "" || !regularFile(g.Path) {
			continue
		}
		c := gswCard{game: g}
		if gswHasStates(g.System) {
			c.auto, c.shot = gswStates(g.Path)
		}
		c.art = homeArtPreviewJob(g).File
		cards = append(cards, c)
	}
	return cards
}

// gswLoadPicture decodes one picture into the card box under the shared
// decode permit, as gmetaLoadThumb does for the strip on a game's page.
func gswLoadPicture(ctx context.Context, path string) (*image.NRGBA, error) {
	var err error
	if path, err = safeAbsolutePath(path); err != nil {
		return nil, err
	}
	if pathHasParentComponent(path) {
		return nil, os.ErrInvalid
	}
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)
	pic, err := imgvDecodeFile(path)
	if err != nil {
		return nil, err
	}
	b := pic.Bounds()
	w, h := imgvFitInside(b.Dx(), b.Dy(), gswPicW, gswPicH)
	return imgvScale(pic, w, h), nil
}

// gswRead is the visit's reader: the cards first (titles show at once), then
// each picture in strip order, the state picture and, failing that, the box
// art. A newer visit or leaving the screen stops it.
func gswRead(ctx context.Context, gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("switcher", "reading the recent games failed: %v", r)
		}
	}()
	recents := recentGames()
	var stamp time.Time
	if len(recents) > 0 {
		stamp = recents[0].PlayedAt
	}
	cards := gswBuildCards(recents)
	gswLoad.mu.Lock()
	if gswLoad.gen != gen {
		gswLoad.mu.Unlock()
		return
	}
	// A copy: the render thread edits and removes its cards while this loop
	// still walks its own.
	gswLoad.cards, gswLoad.stamp, gswLoad.ready = append([]gswCard(nil), cards...), stamp, true
	gswLoad.mu.Unlock()
	for _, c := range cards {
		if ctx.Err() != nil {
			return
		}
		var pic gswPic
		for _, src := range []struct {
			path     string
			fromShot bool
		}{{c.shot, true}, {c.art, false}} {
			if src.path == "" {
				continue
			}
			img, err := gswLoadPicture(ctx, src.path)
			if err != nil {
				logDebug("switcher", "no picture for %s from %s: %v", c.game.Title, src.path, err)
				continue
			}
			pic = gswPic{img: img, fromShot: src.fromShot}
			break
		}
		if pic.img == nil {
			continue
		}
		gswLoad.mu.Lock()
		if gswLoad.gen != gen {
			gswLoad.mu.Unlock()
			return
		}
		gswLoad.pics[c.game.Path] = pic
		gswLoad.mu.Unlock()
	}
}

// gswEnter starts a visit: everything is read again, the cursor on the game
// played last.
func gswEnter() {
	gswDropTextures()
	gsw.gen++
	gsw.loaded, gsw.cards, gsw.cursor, gsw.pos = false, nil, 0, 0
	gsw.infoFor = -1
	gen := gsw.gen
	gswLoad.mu.Lock()
	gswLoad.gen, gswLoad.ready, gswLoad.cards = gen, false, nil
	gswLoad.pics = map[string]gswPic{}
	gswLoad.mu.Unlock()
	ctx := sceneWorkContext()
	go gswRead(ctx, gen)
	go gswWatchLeave(ctx, gen)
}

// gswWatchLeave frees the visit's pictures once the screen is left: the
// scene's work context ends when the transition away starts (scenetasks.go),
// and the textures go on the render thread when the fade has finished.
func gswWatchLeave(ctx context.Context, gen uint64) {
	<-ctx.Done()
	var drop func()
	drop = func() {
		if gsw.gen != gen {
			return // a newer visit owns the pictures now
		}
		if gswOnScreen(appConfig) && transitionPhase != "none" {
			tbxPost(drop) // still fading out: the cards stay until it ends
			return
		}
		if !gswOnScreen(appConfig) {
			gswLoad.mu.Lock()
			gswLoad.gen++ // the reader stops; nothing waits for an upload
			gswLoad.pics = nil
			gswLoad.mu.Unlock()
			gswDropTextures()
		}
	}
	tbxPost(drop)
}

// gswTake moves the reader's results onto the screen: the cards once, and at
// most one picture upload per frame.
func gswTake(renderer *sdl.Renderer) {
	if gsw.owner != nil && gsw.owner != renderer {
		// Textures belong to the renderer that made them and went with it;
		// destroying them now would touch freed memory (homePreviewTexture).
		for i := range gsw.cards {
			gsw.cards[i].tex = nil
		}
		gsw.owner = nil
	}
	gswLoad.mu.Lock()
	defer gswLoad.mu.Unlock()
	if gswLoad.gen != gsw.gen {
		return
	}
	if !gsw.loaded {
		if !gswLoad.ready {
			requestFrameEvery(100)
			return
		}
		gsw.cards, gsw.stamp, gsw.loaded = gswLoad.cards, gswLoad.stamp, true
		gswLoad.cards = nil
	}
	if len(gswLoad.pics) == 0 {
		return
	}
	// The card in view first, then the rest in order.
	idx := -1
	if c := gsw.cursor; c >= 0 && c < len(gsw.cards) && gsw.cards[c].tex == nil {
		if _, ok := gswLoad.pics[gsw.cards[c].game.Path]; ok {
			idx = c
		}
	}
	for i := 0; idx < 0 && i < len(gsw.cards); i++ {
		if gsw.cards[i].tex != nil {
			continue
		}
		if _, ok := gswLoad.pics[gsw.cards[i].game.Path]; ok {
			idx = i
		}
	}
	if idx < 0 {
		return
	}
	c := &gsw.cards[idx]
	pic := gswLoad.pics[c.game.Path]
	delete(gswLoad.pics, c.game.Path)
	if tex := imgvTexture(renderer, pic.img); tex != nil {
		c.tex, c.fromShot = tex, pic.fromShot
		c.texW, c.texH = int32(pic.img.Rect.Dx()), int32(pic.img.Rect.Dy())
		gsw.owner = renderer
	}
	// One upload per frame; the next frame takes the next one soon.
	requestFrameEvery(16)
}

// gswDropTextures frees every card picture. Render thread.
func gswDropTextures() {
	for i := range gsw.cards {
		if gsw.cards[i].tex != nil {
			_ = gsw.cards[i].tex.Destroy()
			gsw.cards[i].tex = nil
		}
	}
}

// gswForget removes a game from the recent games — the Continue card and the
// library's recents with it — and keeps its saves, states and play time.
func gswForget(path string) {
	gameRecentsMu.Lock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	next := make([]recentGame, 0, len(gameRecents))
	for _, it := range gameRecents {
		if it.Path != path {
			next = append(next, it)
		}
	}
	changed := len(next) != len(gameRecents)
	if changed {
		gameRecents = next
	}
	gameRecentsMu.Unlock()
	if !changed {
		return
	}
	if err := saveGameRecentsFile(gameRecentsFile(), next); err != nil {
		logWarn("switcher", "cannot save the recent games: %v", err)
	}
	homeContinueInvalidate()
}

// ── Ways in ──────────────────────────────────────────────────────────────

// gswEnsureScene gives the screen its scene in memory, like the Minecraft
// screen (minecraft_ui.go); the user's config is never rewritten for it.
func gswEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, gameSwitcherSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: gameSwitcherSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == gameSwitcherSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// gswOnScreen reports whether the switcher is the scene on show.
func gswOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == gameSwitcherSceneName
}

// gameSwitcherOpen opens the switcher (Home, the Start menu, search, the
// in-game menu's row). The games library and Quick Settings are panels over
// the scenes; they close, or they would cover it. Main thread.
func gameSwitcherOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	if !gameSwitcherEnabled(config) {
		ShowToast("The game switcher is off · Settings → Games & emulators", ToastKindInfo)
		return
	}
	gswEnsureScene(config)
	idx := sceneIndexExact(config, gameSwitcherSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	closeGameLibraryPanel()
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// gameSwitcherFromHome is X on Home (home.go): it reports false, leaving X
// to whatever had it, when the switcher is off.
func gameSwitcherFromHome(config *Config) bool {
	if !gameSwitcherEnabled(config) {
		return false
	}
	PlayActivateSound()
	gameSwitcherOpen(config)
	return true
}

// gameSwitcherFromGameMenu is the in-game menu's Game switcher row: the game
// quits exactly as Quit to JukaHub quits it (gmQuitGame: an interrupt, which
// RetroArch answers by writing its auto state and leaving), and the switcher
// opens once the screen is JukaHub's again.
func gameSwitcherFromGameMenu() {
	gswAfterGame.Store(true)
	gmQuitGame()
	go gswWaitForScreen()
}

// gswWaitForScreen waits for the game's process group to be gone and the
// screen handed back (gamesuspend.go), then opens the switcher on the main
// loop. A game that holds on past gswAfterGameWait gets no switcher.
func gswWaitForScreen() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("switcher", "waiting for the game to quit failed: %v", r)
		}
	}()
	deadline := time.Now().Add(gswAfterGameWait)
	for {
		time.Sleep(gswAfterGamePoll)
		if !gameScreenSuspended() {
			break
		}
		if time.Now().After(deadline) {
			gswAfterGame.Store(false)
			logInfo("switcher", "the game did not hand the screen back within %s; not opening the game switcher", gswAfterGameWait)
			return
		}
	}
	tbxPost(func() {
		if gswAfterGame.CompareAndSwap(true, false) {
			gameSwitcherOpen(appConfig)
		}
	})
}

// ── Input ────────────────────────────────────────────────────────────────

// gswAction is one press on the screen. Up is left unused, which is how the
// top bar is reached (tbxDispatch); B leaves through tbxDispatch too.
func gswAction(config *Config, act tbxAction) bool {
	n := len(gsw.cards)
	switch act {
	case tbxActLeft:
		gswMoveTo(gsw.cursor - 1)
	case tbxActRight:
		gswMoveTo(gsw.cursor + 1)
	case tbxActPageUp, tbxActHome:
		gswMoveTo(0)
	case tbxActPageDown, tbxActEnd:
		gswMoveTo(n - 1)
	case tbxActConfirm:
		gswResume(config)
	case tbxActX:
		gswRemove()
	case tbxActDown:
		// Nothing below the strip; the press is the screen's.
	default:
		return false
	}
	return true
}

// gswMoveTo moves the cursor, stopping at the ends.
func gswMoveTo(i int) {
	n := len(gsw.cards)
	if n == 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	if i == gsw.cursor {
		return
	}
	gsw.cursor = i
	PlayNavSound()
}

// gswResume starts the highlighted game, which continues from its auto state
// (savestate_resume.go). The screen is left first, so the game hands the
// screen back to where the switcher was opened from; its pictures go at once
// rather than sit in memory beside the game.
func gswResume(config *Config) {
	if gsw.cursor < 0 || gsw.cursor >= len(gsw.cards) {
		PlayErrorSound()
		return
	}
	g := gsw.cards[gsw.cursor].game
	gswLoad.mu.Lock()
	gswLoad.gen++
	gswLoad.pics = nil
	gswLoad.mu.Unlock()
	gswDropTextures()
	goBackScene(config)
	if g.Path == "" || !regularFile(g.Path) {
		playRecentGame(g) // says the ROM is missing
		return
	}
	// The switcher resumes a game where it was left: in the emulator it last
	// ran in, without asking "Which emulator?" again (emupicker.go).
	game := libraryGame{Path: g.Path, System: g.System, Title: g.Title}
	chooseGameLauncherResume(game, func(l string) { playRecentGameWith(g, game, l) })
}

// gswRemove takes the highlighted game off the recent games.
func gswRemove() {
	i := gsw.cursor
	if i < 0 || i >= len(gsw.cards) {
		PlayErrorSound()
		return
	}
	c := gsw.cards[i]
	if c.tex != nil {
		_ = c.tex.Destroy()
	}
	gswLoad.mu.Lock()
	delete(gswLoad.pics, c.game.Path) // a picture still waiting for its upload
	gswLoad.mu.Unlock()
	gsw.cards = append(gsw.cards[:i], gsw.cards[i+1:]...)
	if gsw.cursor >= len(gsw.cards) && gsw.cursor > 0 {
		gsw.cursor = len(gsw.cards) - 1
	}
	if gsw.pos > float64(len(gsw.cards)-1) {
		gsw.pos = float64(max(len(gsw.cards)-1, 0))
	}
	gsw.infoFor = -1
	gswForget(c.game.Path)
	// Removing the newest game is not a new game played (gswRender).
	gsw.stamp = gswRecentsStamp()
	PlayBackSound()
	ShowToast(gswTitle(c.game)+" removed from recent games · its saves are kept", ToastKindInfo)
}

// gswTitle is a recent game's name on its card.
func gswTitle(g recentGame) string {
	if t := strings.TrimSpace(g.Title); t != "" {
		return t
	}
	return strings.TrimSuffix(filepath.Base(g.Path), filepath.Ext(g.Path))
}

// ── Scene ────────────────────────────────────────────────────────────────

type gswSceneModule struct{ sceneBase }

func (gswSceneModule) render(r *sdl.Renderer, c *Config) bool {
	gswRender(r, c)
	return true
}

func (gswSceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), gswAction)
}

func (gswSceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), gswAction)
}

func init() { registerScene(gswSceneModule{sceneBase{name: gameSwitcherSceneName}}) }
