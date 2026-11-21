package main

// startup_target.go — where JukaHub opens when the handheld is switched on
// (Settings → Power & battery → "Start in"), and "Play a random game" at one
// press.
//
// Start in: Home (the default, nothing changes), Game switcher, Random game or
// Last game played. It acts once, on the first JukaHub start of a boot — the
// power-on with autostart, or the first launch from the stock menu — and
// never on a relaunch in the same boot (an update, a crash respawn, JukaHub
// coming back after a game handed off to another frontend): bootos.go's
// relaunch test, which compares the kernel's boot id with the one the last
// start wrote. Not during the first-run tour, nor in a screenshot session.
// And never when Resume game at power on is already starting the game the
// last power off saved (quickresume.go): that one is where the player left,
// so it wins.
//
// The target is taken the way quickBootTick takes a resume: once the first
// frames are up (quickResumeBootFrames), the startup lock is open, no
// "Who is playing?" (profiles) is on screen, and the tasks queued for after
// the first frame have run or had quickResumeStartupWait. It waits on the UI
// queue (tbxPost), one look per frame, so nothing new runs in the main loop.
// B (or Esc) held at that moment stays on Home, as it skips a resume. A press
// since the last question was answered (the lock, the profile picker), a
// screen the player opened meanwhile (any scene but the first, the library,
// Settings) or an emulator question (a game being started) means they are
// already somewhere: the target is dropped. The queue does not run while a
// game owns the screen, so the press check is what keeps a game the player
// started from Home in those seconds from being followed by the target when
// it ends.
//
//   - Game switcher opens the switcher (gameswitcher.go); with the switcher
//     off it stays on Home.
//   - Last game played starts the newest game of the recents (the Continue
//     card's list) the way the switcher resumes one: in the emulator it last
//     ran in, from its auto state.
//   - Random game is randomPlayNow below.
//
// Play a random game (Start → Games, Home search): the library is the list
// of games, and it is read by the library panel's scan (game_library_ui.go),
// so the press opens the library, waits for the scan, then rolls with the
// random picker's own roll (game_random.go: buildGameRandomScopes, "Whole
// library", randomLibraryGame — the game played last is avoided while another
// exists) and starts the pick the way the game page's Play does
// (gmetaPlayWith, which counts the Feeling Lucky trophy). The highlight is left
// on the pick, so the library shows what was played when the game ends. The
// older "Pick a game for me" plugin row still only highlights a game.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// startInKey is the Custom setting behind Settings → Power & battery →
	// Start in.
	startInKey      = "startIn"
	startInHome     = "home"
	startInSwitcher = "switcher"
	startInRandom   = "random"
	startInLast     = "last"
	// startInQuickMenu opens the Quick menu (quickmenu.go).
	startInQuickMenu = "quickmenu"
	// randomPlayEntryName is the Start menu's and Home search's row.
	randomPlayEntryName = "Play a random game"
	// randomPlayWaitMax bounds the wait for the library's scan: a card that
	// takes longer is read in the background, and the press asks again.
	randomPlayWaitMax = 30 * time.Second
	// bootToInputGrace lets a key that answered the last question repeat a
	// moment (a held Enter) without counting as the player moving on.
	bootToInputGrace = 300 * time.Millisecond
)

// bootTo is the start-up target waiting for its frame. Main thread.
var bootTo struct {
	pending bool
	target  string
	ticks   int
	since   time.Time
	// scene is the scene on show at the first look: a different one later
	// means the player went somewhere first.
	scene int
	// calm is the last look at which a question (the startup lock, "Who is
	// playing?") was up, or was answered since the look before: a press after
	// it is the player doing something. asking is that look's answer.
	calm   time.Time
	asking bool
}

// randomPlay is a "Play a random game" press waiting for the library's scan.
// Main thread.
var randomPlay struct {
	pending  bool
	deadline time.Time
	unasked  bool // started at power-on, not by a press (hakuxStartUnasked)
}

// startInOptions are the setting's stops.
func startInOptions() []setOption {
	return []setOption{
		{Value: startInHome, Label: "Home"},
		{Value: startInSwitcher, Label: "Game switcher"},
		{Value: startInRandom, Label: "Random game"},
		{Value: startInLast, Label: "Last game played"},
		{Value: startInQuickMenu, Label: "Quick menu"},
	}
}

// startInSetting is the stored target; anything unknown is Home.
func startInSetting(c *Config) string {
	if c == nil {
		return startInHome
	}
	v, _ := customString(c.Variables.Custom, startInKey)
	for _, o := range startInOptions() {
		if o.Value == v {
			return v
		}
	}
	return startInHome
}

// startInLabel is a target's words.
func startInLabel(v string) string {
	for _, o := range startInOptions() {
		if o.Value == v {
			return o.Label
		}
	}
	return "Home"
}

// startInSettingsRow is Settings → Power & battery → Start in, under Resume
// game at power on (setmodel.go).
func startInSettingsRow() setRow {
	return setRow{
		ID: startInKey, Label: "Start in", Kind: setKindChoice,
		Detail: func(*Config) string {
			// Esc on a keyboard (bootToSkipHeld), through the hint layer.
			return hintSentence("Where JukaHub opens when the handheld is switched on · a resumed game comes first · hold %s for Home", "B", "Back")
		},
		Options: func(*Config) []setOption { return startInOptions() },
		Current: func(c *Config) int {
			cur := startInSetting(c)
			for i, o := range startInOptions() {
				if o.Value == cur {
					return i
				}
			}
			return -1
		},
		Value: func(c *Config) string { return startInLabel(startInSetting(c)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, startInKey, value)
			setToast("Start in: " + startInLabel(value))
		},
	}
}

// bootToPlan decides once, at start-up, whether this start goes to the
// chosen target: main.go calls it right after the power-on resume's markers
// are settled (quickResumeBootCommit), before the first frame — so the last
// start's record is still the one bootos.go compares with. Main thread.
func bootToPlan(config *Config) {
	target := startInSetting(config)
	if target == startInHome {
		return
	}
	switch {
	case quickBoot.pending:
		logInfo("startup", "start in %s: a game resumes at power on instead", startInLabel(target))
		return
	case !onboardingDone(config) || shotModeOn():
		return
	case bootOSRelaunch(bootOSLiveInputs(config, time.Now())):
		// JukaHub started again in the same boot: the player is mid-session.
		return
	}
	bootTo.pending, bootTo.target, bootTo.ticks, bootTo.since, bootTo.scene = true, target, 0, time.Time{}, -1
	bootTo.calm, bootTo.asking = time.Time{}, false
	logInfo("startup", "start in: %s once the first frame is up", startInLabel(target))
	tbxPost(bootToTick)
}

// bootToTick is one look per frame until the target can be taken (the
// package header). Main thread, from the UI queue.
func bootToTick() {
	if !bootTo.pending {
		return
	}
	if exitRequested.Load() || gameScreenSuspended() || quickBoot.pending {
		// Quitting, or something else took the screen first.
		bootTo.pending = false
		return
	}
	now := time.Now()
	bootTo.ticks++
	if bootTo.since.IsZero() {
		bootTo.since = now
		bootTo.scene = currentSceneIndex
	}
	// The "Who is playing?" question at start (profiles_ui.go) is answered
	// first: the last game played is that person's. The presses that answer
	// a question are not the player moving on; the first look after one is
	// answered still counts as calm, so the answering press is behind it.
	asking := startupLocked() || onboarding.active || profilesPicker.sheet.open
	if asking || bootTo.asking || bootTo.calm.IsZero() {
		bootTo.calm = now
	}
	bootTo.asking = asking
	pressed := lastInputUnixNano.Load() > bootTo.calm.Add(bootToInputGrace).UnixNano()
	if pressed || bootTo.scene != currentSceneIndex || gameLib.open || setHub.sheet.open || emuPickUI.open {
		bootTo.pending = false
		if bootToSkipHeld() {
			logInfo("startup", "B held at start: staying on Home instead of %s", startInLabel(bootTo.target))
			ShowToast("Start in "+startInLabel(bootTo.target)+" skipped", ToastKindInfo)
			return
		}
		logInfo("startup", "start in %s: dropped, the player did something first", startInLabel(bootTo.target))
		return
	}
	if bootTo.ticks < quickResumeBootFrames || asking ||
		(quickStartupTasksLeft() && now.Sub(bootTo.since) < quickResumeStartupWait) {
		requestFrameEvery(50)
		tbxPost(bootToTick)
		return
	}
	bootTo.pending = false
	if bootToSkipHeld() {
		logInfo("startup", "B held at start: staying on Home instead of %s", startInLabel(bootTo.target))
		ShowToast("Start in "+startInLabel(bootTo.target)+" skipped", ToastKindInfo)
		return
	}
	bootToRun(appConfig, bootTo.target)
}

// bootToSkipHeld reports B on the pad, or Esc on a keyboard, held now.
func bootToSkipHeld() bool {
	if gameController != nil && gameController.Button(sdl.CONTROLLER_BUTTON_B) != 0 {
		return true
	}
	keys := sdl.GetKeyboardState()
	sc := int(sdl.SCANCODE_ESCAPE)
	return sc < len(keys) && keys[sc] != 0
}

// bootToRun takes the target. Main thread.
func bootToRun(config *Config, target string) {
	switch target {
	case startInSwitcher:
		if !gameSwitcherEnabled(config) {
			logInfo("startup", "start in the game switcher: the switcher is off, staying on Home")
			return
		}
		gameSwitcherOpen(config)
	case startInRandom:
		randomPlayStart(config, true)
	case startInLast:
		recents := recentGames()
		if len(recents) == 0 {
			logInfo("startup", "start in the last game: nothing played yet, staying on Home")
			return
		}
		g := recents[0]
		if g.Path == "" || !regularFile(g.Path) {
			logInfo("startup", "start in the last game: %s is gone, staying on Home", g.Path)
			return
		}
		logInfo("startup", "start in the last game: %s", gswTitle(g))
		game := libraryGame{Path: g.Path, System: g.System, Title: g.Title}
		// Nobody pressed anything: an Xbox game whose hakuX is gone only says
		// so (hakux_auto.go), as at a power-on resume.
		hakuxStartUnasked(func() {
			chooseGameLauncherResume(game, func(l string) { playRecentGameWith(g, game, l) })
		})
	case startInQuickMenu:
		logInfo("startup", "start in the quick menu")
		quickMenuOpen()
	}
}

// ── Play a random game ───────────────────────────────────────────────────

// randomPlayNow is the Start menu's and Home search's "Play a random game".
func randomPlayNow(c *Config) { randomPlayStart(c, false) }

// randomPlayStart opens the library (its scan is the list of games) and
// plays a random game from it as soon as the scan is in. unasked: no press
// started it (Start in → Random game). Main thread.
func randomPlayStart(_ *Config, unasked bool) {
	if randomPlay.pending {
		return // a second press while the first waits
	}
	if !gameLib.open {
		openGameLibraryPanel()
	}
	if !gameLib.scanning {
		randomPlayRoll(unasked)
		return
	}
	randomPlay.pending, randomPlay.deadline, randomPlay.unasked = true, time.Now().Add(randomPlayWaitMax), unasked
	ShowToast("Picking a game…", ToastKindInfo)
	tbxPost(randomPlayTick)
}

// randomPlayTick waits for the scan, one look per frame. Main thread, from
// the UI queue.
func randomPlayTick() {
	if !randomPlay.pending {
		return
	}
	switch {
	case !gameLib.open || gameScreenSuspended() || exitRequested.Load():
		// The library was closed, or a game took the screen: the press is spent.
		randomPlay.pending = false
		return
	case gameLib.scanning:
		if time.Now().After(randomPlay.deadline) {
			randomPlay.pending = false
			ShowToast("The library is still being read — try again in a moment", ToastKindInfo)
			return
		}
		requestFrameEvery(100)
		tbxPost(randomPlayTick)
		return
	}
	randomPlay.pending = false
	randomPlayRoll(randomPlay.unasked)
}

// randomPlayRoll rolls the whole library with the picker's roll, leaves the
// highlight on the pick and starts it. Main thread.
func randomPlayRoll(unasked bool) {
	scopes := buildGameRandomScopes()
	if len(scopes) == 0 {
		ShowToast("No games to pick from yet — copy ROMs and press Y to rescan", ToastKindInfo)
		return
	}
	avoid := ""
	if recents := recentGames(); len(recents) > 0 {
		avoid = recents[0].Path
	}
	// "Whole library" is the last scope whenever there are games at all.
	game, ok := randomLibraryGame(scopes[len(scopes)-1].games, avoid)
	if !ok {
		ShowToast("No games to pick from yet — copy ROMs and press Y to rescan", ToastKindInfo)
		return
	}
	jumpToLibraryGame(game)
	// The roll's game: playing it counts for the Feeling Lucky trophy.
	gameRandomPick = game.Path
	logInfo("library", "random game: %s (%s)", game.Title, game.System)
	// A built-in game (JukaLand, the Synth, an arcade minigame) has no
	// emulator: it opens its own screen, as A in the library does.
	if openBuiltInLibraryGame(game, appConfig) {
		return
	}
	start := func() {
		chooseGameLauncher(game, func(launcher string) { gmetaPlayWith(game, nil, launcher) })
	}
	if unasked {
		hakuxStartUnasked(start)
		return
	}
	start()
}
