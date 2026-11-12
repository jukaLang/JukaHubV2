package main

// gameassist.go — the game assistant: Juka knows which game is being played,
// and on the handheld can be asked about it from inside the game.
//
// Two halves, both behind Settings → Voice & AI → Game assistant (on until
// turned off; gameassist_settings.go):
//
//   - What Juka knows. The main thread follows the screen children
//     (gamesuspend.go): a launch, the recents entry the library writes for it
//     (game_library.go recordGameLaunch), the end. The record — title, system,
//     running or when it ended on the monotonic clock — is all
//     gameAssistPromptSuffix needs to tell every question to Juka (Chat, the
//     voice orb, pictures) which game "this game" is. A program the library
//     did not start (an app, a port started elsewhere) has no title, and then
//     nothing is said about it.
//
//   - Asking from inside a game, on a Linux handheld. While a game JukaHub
//     launched owns the screen, a goroutine watches the pad
//     (gameassist_linux.go); Select + R2 held for 0.7 s grabs the pad and asks
//     the parked main loop for the overlay. The game is paused the way a
//     ringing alarm pauses it (SIGSTOP to its process group, alarmSignalChild),
//     its last frame is read from the panel's framebuffer before JukaHub draws
//     anything (Screen Share's reader, castcap_fb.go), and the main loop stops
//     parking (main.go asks gameAssistOverlayActive) to draw a full-screen
//     layer — the game's picture dimmed, or a dark one when the framebuffer is
//     black — with the voice orb listening on it: "Ask about <title>". The orb
//     stops by itself when the player stops talking (A or R2 sends sooner),
//     Whisper writes the words down, and every sentence goes to Juka as a
//     question about the game (never a command: one that opened a screen would
//     leave the game paused behind it). With "Juka can see the game screen" on,
//     the picture goes with the question to the vision model (groqvision.go);
//     the answer streams into the bubble, stays until the player leaves, and
//     is read aloud (ttsFromGame: "Read answers aloud in games", on by
//     default, decides there instead of "Juka's voice"). A asks again; B,
//     Menu or Select + R2 pressed again (no hold this time) goes back to the
//     game: the orb goes, the watcher gives the pad back once
//     nothing is held, and the game runs on (SIGCONT).
//
// Guard rails: the overlay lasts at most gaOverlayCap; an alarm that rings
// meanwhile wins (it pauses the game itself and resumes it when answered); a
// game that ends or a pad that goes away ends it; a Windows-app session keeps
// the pad (wineInputActive), so the watcher does not run then; quitting and a
// crash resume the game before asking it to quit (gamesuspend.go
// stopScreenChildren, the Cleanup hook below); and a JukaHub killed outright
// leaves a note the next start uses to let the game go (gaRecoverPaused).
//
// On a PC no layer can be drawn over a game's own window and JukaHub's loop
// parks behind it, so only the first half applies there: back in JukaHub, the
// orb (F4) and Chat know the game.
//
// Main thread only, apart from gaSession (any goroutine reads it), gaLink
// (shared with the pad watcher) and the request goroutine, which hands its
// result back through tbxPost.

import (
	"context"
	"errors"
	"image"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gaOverlayCap is the longest a game stays paused under the assistant: a
	// handheld put down mid-question gets its game back.
	gaOverlayCap = 3 * time.Minute
	// gaIdentifyFor is how far from a launch the recents entry it wrote may be
	// (the library writes it right after the start).
	gaIdentifyFor = 5 * time.Second
	// gaMirrorEvery paces the switches' mirror (gameAssistFrame).
	gaMirrorEvery = 500 * time.Millisecond
	// gaBlackLuma is the mean brightness under which the framebuffer is taken
	// to hold no picture: a GPU that never scans out of fb0 leaves it black.
	gaBlackLuma = 3.0
	// gaFrameMaxW caps the kept picture's width (it is never scaled up).
	gaFrameMaxW = 1280
	// gaAskTimeout bounds one question to the vision model.
	gaAskTimeout = 60 * time.Second
	// gaPressQueue bounds the presses waiting for the main loop.
	gaPressQueue = 16
)

// gaVoicePrompt is added to Juka's system prompt for a question asked over a
// paused game.
const gaVoicePrompt = "The player paused their game on the handheld and asked this out loud; the answer is shown " +
	"in a bubble over the paused game and read aloud: answer in two or three short sentences of " +
	"plain text, with no lists and no markdown."

// gaButton is a press the overlay acts on, from the pad watcher or from an
// SDL pad (a Bluetooth controller, which the watcher does not hold).
type gaButton int

const (
	gaBtnA         gaButton = iota // ask, or send what was said
	gaBtnB                         // back to the game
	gaBtnR2                        // like A: the finger is still on it after the chord
	gaBtnMenu                      // back to the game
	gaBtnChord                     // Select + R2 again: back to the game
	gaBtnUp                        // the D-pad up: the in-game menu's highlight
	gaBtnDown                      // the D-pad down
	gaBtnMenuChord                 // Select + L2 again: the in-game menu, or back to the game from it
)

// gaMode is what the overlay is for: the assistant listening (Select + R2) or
// the in-game menu (Select + L2, gamemenu.go).
type gaMode int32

const (
	gaModeAsk gaMode = iota
	gaModeMenu
)

// ── What Juka knows ──────────────────────────────────────────────────────

// gaGame is the latest game of this session: its title and system from the
// recents list ("" when the program was not a library game), whether it is
// running, and when it started and ended (monotonic readings of time.Now).
type gaGame struct {
	title, system string
	running       bool
	started       time.Time
	ended         time.Time
}

// gaSession is the record every prompt reads: written by the main thread,
// read by any goroutine.
var gaSession struct {
	mu  sync.Mutex
	cur gaGame
}

// gaSessionSnapshot is a copy of the record. Any goroutine.
func gaSessionSnapshot() gaGame {
	gaSession.mu.Lock()
	defer gaSession.mu.Unlock()
	return gaSession.cur
}

// gaSessionSet publishes the main thread's record.
func gaSessionSet(g gaGame) {
	gaSession.mu.Lock()
	gaSession.cur = g
	gaSession.mu.Unlock()
}

// gaTrack follows the screen children. Main thread only.
var gaTrack struct {
	wasRunning bool      // a screen child was alive last frame
	launchAt   time.Time // when this frame loop first saw it
	game       gaGame    // the main thread's copy of gaSession
	recovered  bool      // gaRecoverPaused has run
	mirrorAt   time.Time // the switches were last mirrored
}

// gaIdentify looks for the recents entry the running game's launch wrote, and
// reports whether it found it. The library records a game right after
// starting it (launchLibraryGame), so the newest entry recorded within
// gaIdentifyFor of the launch is this game; one this process recorded carries
// a monotonic reading, so the comparison holds even before NTP has set the
// clock, and an entry from an earlier session is hours or days away. A
// program the library did not start leaves the newest entry as it was.
func gaIdentify() bool {
	r := recentGames()
	if len(r) == 0 {
		return false
	}
	if d := r[0].PlayedAt.Sub(gaTrack.launchAt); d < -gaIdentifyFor || d > gaIdentifyFor {
		return false
	}
	title := strings.TrimSpace(r[0].Title)
	if title == "" {
		title = gameDisplayTitle(r[0].Path)
	}
	gaTrack.game.title, gaTrack.game.system = title, strings.TrimSpace(r[0].System)
	return title != ""
}

// gaTrackFrame follows a launch, the entry it writes and the end, once a
// frame (parked or not).
func gaTrackFrame(now time.Time) {
	running := gameScreenSuspended()
	switch {
	case running && !gaTrack.wasRunning:
		gaTrack.launchAt = now
		gaTrack.game = gaGame{running: true, started: now}
		gaIdentify()
		gaSessionSet(gaTrack.game)
	case running:
		// A launch on a goroutine may record its entry a moment later.
		if gaTrack.game.title == "" && now.Sub(gaTrack.launchAt) < gaIdentifyFor && gaIdentify() {
			gaSessionSet(gaTrack.game)
		}
	case gaTrack.wasRunning:
		gaTrack.game.running = false
		gaTrack.game.ended = now
		gaSessionSet(gaTrack.game)
	}
	gaTrack.wasRunning = running
}

// gameAssistFrame is the upkeep that runs every frame whatever is on screen
// (main thread): the switches mirrored for the prompt's goroutines (twice a
// second: a switch flipped in Settings is mirrored at once by
// gameAssistStore, and reading the config map formats each value), the game
// followed, and — once — a game a killed JukaHub left paused let go.
func gameAssistFrame(config *Config) {
	now := time.Now()
	if now.Sub(gaTrack.mirrorAt) >= gaMirrorEvery {
		gaTrack.mirrorAt = now
		gameAssistLive.on.Store(gameAssistEnabled(config))
		gameAssistLive.screen.Store(gameAssistScreenEnabled(config))
	}
	gaTrackFrame(now)
	if !gaTrack.recovered {
		gaTrack.recovered = true
		gaRecoverPaused()
	}
}

// ── The link with the pad watcher ────────────────────────────────────────

// gaLink is what the pad watcher (gameassist_linux.go) and the main thread
// share. request: the chord fired and the pad is held, the overlay should
// open. held: the watcher holds the pad. lost: the pad went away while held.
// release: the main thread wants the pad given back. live: the overlay is up.
// mode: which chord took the pad (a gaMode), set before request.
// presses: what was pressed on the held pad, oldest first.
var gaLink struct {
	mode    atomic.Int32
	request atomic.Bool
	held    atomic.Bool
	lost    atomic.Bool
	release atomic.Bool
	live    atomic.Bool
	mu      sync.Mutex
	presses []gaButton
}

// gaPush queues a press for the main loop. Any goroutine.
func gaPush(b gaButton) {
	gaLink.mu.Lock()
	if len(gaLink.presses) < gaPressQueue {
		gaLink.presses = append(gaLink.presses, b)
	}
	gaLink.mu.Unlock()
}

// gaTakePresses empties the queue.
func gaTakePresses() []gaButton {
	gaLink.mu.Lock()
	p := gaLink.presses
	gaLink.presses = nil
	gaLink.mu.Unlock()
	return p
}

// gameAssistOverlayActive reports whether the assistant is up over a paused
// game: the main loop draws then instead of parking, and the voice orb stays
// up although a game owns the screen. Any goroutine.
func gameAssistOverlayActive() bool { return gaLink.live.Load() }

// ── The overlay ──────────────────────────────────────────────────────────

// gaOverlay is the assistant over a paused game. Main thread only.
var gaOverlay struct {
	active   bool
	closing  bool // the hand-back frame is still to be drawn
	openedAt time.Time
	nudgedAt time.Time // the idle screens were last told someone is here
	paused   []*exec.Cmd
	// The game, as known when the overlay opened ("" when unknown).
	title, system string
	// shot: "Juka can see the game screen" was on when it opened.
	shot bool
	// mode: the assistant or the in-game menu; sel: the menu's highlighted
	// row; fromMenu: the assistant was opened from the menu, so B goes back
	// to it rather than to the game.
	mode     gaMode
	sel      int
	fromMenu bool
	// The game's last frame (nil when the framebuffer gave none) and its
	// texture, made on the first frame drawn.
	frame      *image.RGBA
	tex        *sdl.Texture
	texW, texH int32
	texFailed  bool
}

func init() {
	// Quitting resumes a game the assistant paused and gives the pad back.
	Cleanup = append(Cleanup, gameAssistShutdown)
}

// gameAssistParkedTick runs from the parked main loop while a game owns the
// screen: the per-frame upkeep, the pad watcher kept running while the switch
// is on, and the overlay opened when the watcher saw the chord.
func gameAssistParkedTick(config *Config) {
	gameAssistFrame(config)
	// A game quitting from the in-game menu is left alone: pausing it now
	// would stop it mid-save, and the quit watch would kill it stopped.
	can := gameAssistSupported() && !wineInputActive() && !alarmFiringActive() && !gmQuitting.Load()
	gaPadSync(config, can && gameAssistLive.on.Load(), can && gameMenuEnabled(config))
	if gaLink.request.Swap(false) && !gaOpen(config, gaMode(gaLink.mode.Load())) {
		gaLink.release.Store(true)
	}
}

// gaOpen pauses the game and brings the assistant (or the in-game menu) up
// over it. false: it cannot open now, and the caller gives the pad back.
func gaOpen(config *Config, mode gaMode) bool {
	if gaOverlay.active {
		return true
	}
	// Nor over a sleeping handheld (powersleep.go): the screen-off sleep keeps
	// the game stopped and would continue it on the wake under the overlay.
	if gmQuitting.Load() || powerSleepActive() {
		return false
	}
	enabled := gameAssistEnabled(config)
	if mode == gaModeMenu {
		enabled = gameMenuEnabled(config)
	}
	if !enabled || !gameScreenSuspended() || alarmFiringActive() || wineInputActive() ||
		!alarmCanPauseChildren() {
		return false
	}
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return false
	}
	// Written down before the stop: a JukaHub killed while the game is paused
	// leaves the next start the way to let it go (gaRecoverPaused).
	gaPausedNote(cmds)
	var paused []*exec.Cmd
	for _, cmd := range cmds {
		if err := alarmSignalChild(cmd, true); err != nil {
			logWarn("gameassist", "could not pause the game: %v", err)
			continue
		}
		paused = append(paused, cmd)
	}
	if len(paused) == 0 {
		gaPausedClear()
		return false
	}
	if gaTrack.game.running && gaTrack.game.title == "" && gaIdentify() {
		gaSessionSet(gaTrack.game)
	}
	gaDropFrame()
	gaOverlay.active, gaOverlay.closing = true, false
	gaOverlay.openedAt, gaOverlay.nudgedAt = time.Now(), time.Time{}
	gaOverlay.paused = paused
	gaOverlay.title, gaOverlay.system = "", ""
	if gaTrack.game.running {
		gaOverlay.title = gameAssistClean(gaTrack.game.title)
		gaOverlay.system = gameAssistClean(gaTrack.game.system)
	}
	gaOverlay.shot = gameAssistScreenEnabled(config)
	gaOverlay.mode, gaOverlay.sel, gaOverlay.fromMenu = mode, 0, false
	// The game is stopped and JukaHub has not drawn yet, so the framebuffer
	// still holds the game's last frame: it is read now, before the first
	// frame of the overlay replaces it.
	gaOverlay.frame = gaCapture(config)
	gaTakePresses()
	// No input reached JukaHub for the whole game: the idle clock starts again
	// now, as alarmBegin's does, or the first frame's screensaver check (before
	// gaTick's nudge) would put the saver up over the overlay.
	screensaverNotifyInput()
	gaLink.live.Store(true)
	name := gaOverlay.title
	if name == "" {
		name = "a game the library did not start"
	}
	if mode == gaModeMenu {
		PlayActivateSound()
		logInfo("gamemenu", "%s is paused under the in-game menu (picture: %t)", name, gaOverlay.frame != nil)
		return true
	}
	gaListen(config)
	logInfo("gameassist", "%s is paused while Juka listens (picture: %t)", name, gaOverlay.frame != nil)
	return true
}

// gaListen opens the orb's microphone for a question about the game.
func gaListen(config *Config) {
	jukaSpeakStop()
	voiceOrbListen(config)
	// A tap, never push-to-talk: the clip ends when the player stops talking,
	// or at A.
	voiceOrb.held = false
	voiceOrb.pressSrc = orbFromPad
	voiceOrb.pressAt = orbNow()
	if voiceOrb.phase != orbListening {
		// No key, no network or no microphone: the orb says which.
		return
	}
	voiceOrb.title = "Ask about this game"
	if gaOverlay.title != "" {
		voiceOrb.title = "Ask about " + gaOverlay.title
	}
	voiceOrb.sub = "“How do I get past this part?”"
}

// gameAssistHeard takes what the orb heard while the overlay is up: every
// sentence is a question about the game. It reports whether it took it
// (voiceOrbCommand runs the sentence as a command otherwise).
func gameAssistHeard(text string) bool {
	if !gaOverlay.active {
		return false
	}
	if gaOverlay.mode != gaModeAsk {
		// The menu has no orb; a late transcript from before it opened is
		// dropped rather than run as a command behind the paused game.
		return true
	}
	gaAsk(text)
	return true
}

// gaAsk puts a question about the game to Juka and streams the answer into
// the orb's bubble: with the game's picture when the player let Juka see the
// screen and there is one, else as text. A vision request that fails is asked
// again without the picture, so a question is never lost to it.
func gaAsk(question string) {
	key := strings.TrimSpace(groqAPIKey())
	models := groqCandidatesFor(groqModel())
	base := jukaSystemPrompt() + " " + gaVoicePrompt
	textSystem := base
	if gaOverlay.title == "" {
		textSystem += " Which game it is is not known: if the answer depends on it, ask them to name the game."
	}
	frame := gaOverlay.frame
	if !gaOverlay.shot {
		frame = nil
	}
	shotSystem := base + " The attached picture is the game's screen right now."
	if gaOverlay.title == "" {
		shotSystem += " If they do not say which game it is, work it out from the picture."
	}
	voiceOrbCancelRequest()
	gen := voiceOrb.gen
	voiceOrb.ai = true
	voiceOrbSetPhase(orbThinking)
	voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = "Thinking…", orbQuote(question), tbxToneNeutral, orbOfferNone
	orbAIText.mu.Lock()
	orbAIText.gen, orbAIText.text = gen, ""
	orbAIText.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), gaAskTimeout)
	voiceOrb.cancel = cancel
	messages := []map[string]string{
		{"role": "system", "content": textSystem},
		{"role": "user", "content": question},
	}
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				logWarn("gameassist", "Juka's answer stopped: %v", r)
				tbxPost(func() { gaAnswered(gen, question, "", errors.New("Juka's answer stopped")) })
			}
		}()
		onDelta := func(partial string) {
			orbAIText.mu.Lock()
			if orbAIText.gen == gen {
				orbAIText.text = partial
			}
			orbAIText.mu.Unlock()
		}
		var reply string
		var err error
		asText := frame == nil
		if !asText {
			reply, err = gaAskWithPicture(ctx, key, shotSystem, question, frame, onDelta)
			// Any failure but the player giving up (or the wait running out)
			// is asked again without the picture.
			asText = err != nil && ctx.Err() == nil
			if asText {
				logInfo("gameassist", "Juka could not look at the screen (%v); asking without it", err)
			}
		}
		if asText {
			reply, err = chatGroqAsk(key, models, messages, onDelta)
		}
		tbxPost(func() { gaAnswered(gen, question, reply, err) })
	}()
}

// gaAskWithPicture asks the vision model about the game's picture.
func gaAskWithPicture(ctx context.Context, key, system, question string, frame *image.RGBA, onDelta func(string)) (string, error) {
	img, err := jukaVisionFromImage(frame)
	if err != nil {
		return "", err
	}
	return groqVisionAsk(ctx, key, system, question, []jukaVisionImage{img}, onDelta)
}

// gaAnswered shows Juka's whole answer, if it is still the one waited for,
// and speaks it. Over a game an answer stays until the player leaves.
func gaAnswered(gen int, question, reply string, err error) {
	if !gaOverlay.active || gen != voiceOrb.gen || voiceOrb.phase != orbThinking || !voiceOrb.ai || !voiceOrbActive() {
		return
	}
	voiceOrb.ai = false
	voiceOrb.cancel = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		logWarn("gameassist", "Juka could not answer: %v", err)
		voiceOrbSay(chatFriendlyAIError(err), orbQuote(question), tbxToneWarn, orbOfferNone)
		voiceOrb.hideAt = 0
		PlayErrorSound()
		return
	}
	reply = orbPlain(chatStripThink(reply))
	if reply == "" {
		voiceOrbSay("Juka had no answer — try again", orbQuote(question), tbxToneWarn, orbOfferNone)
		voiceOrb.hideAt = 0
		PlayErrorSound()
		return
	}
	voiceOrbSay(reply, orbQuote(question), tbxToneNeutral, orbOfferNone)
	voiceOrb.hideAt = 0
	trophyEvent(tkAIAsk)
	PlayNavSound()
	// Shown and read aloud: Read answers aloud in games, on by default,
	// decides here instead of "Juka's voice" (tts_api.go ttsBegin).
	ttsSpeakFrom(ttsFromGame, ttsKindVoice, reply)
}

// gaPress acts on one press while the overlay is up.
func gaPress(config *Config, b gaButton) {
	if gaOverlay.mode == gaModeMenu {
		gmPress(config, b)
		return
	}
	switch b {
	case gaBtnMenuChord:
		// Select + L2 over the assistant: the in-game menu.
		if gameMenuEnabled(config) {
			PlayNavSound()
			gmShowMenu()
		}
	case gaBtnB:
		PlayBackSound()
		if gaOverlay.fromMenu && gameMenuEnabled(config) {
			gmShowMenu()
			return
		}
		gaClose("the player went back", true)
	case gaBtnMenu, gaBtnChord:
		PlayBackSound()
		gaClose("the player went back", true)
	case gaBtnA, gaBtnR2:
		switch {
		case voiceOrb.phase == orbListening && voiceOrb.fadeAt == 0:
			voiceOrbFinish(config)
		case voiceOrb.phase == orbThinking && voiceOrb.fadeAt == 0:
			// Whisper or Juka is working; B gives up.
		default:
			gaListen(config)
		}
	}
}

// gameAssistController is an SDL pad's press while the overlay is up (main.go
// hands it every one): a Bluetooth controller, which the watcher does not
// hold, works the overlay too, and nothing reaches the screen behind it.
func gameAssistController(e *sdl.ControllerButtonEvent, config *Config) {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !gaOverlay.active {
		return
	}
	if padEventRaw == sdl.CONTROLLER_BUTTON_GUIDE {
		gaPress(config, gaBtnMenu)
		return
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		gaPress(config, gaBtnA)
	case sdl.CONTROLLER_BUTTON_B:
		gaPress(config, gaBtnB)
	default:
		if b, ok := gmController(e.Button); ok {
			gaPress(config, b)
		}
	}
}

// gameAssistOrbHint is the orb bubble's last line over a paused game
// (orbHintLine asks first): A is the voice button there, B the way back.
func gameAssistOrbHint() (string, bool) {
	if !gaOverlay.active {
		return "", false
	}
	back := orbKeyName("B") + " back to the game"
	if gaOverlay.fromMenu && gameMenuEnabled(appConfig) {
		back = orbKeyName("B") + " menu"
	}
	switch voiceOrb.phase {
	case orbListening:
		return orbKeyName("A") + " send · " + back, true
	case orbThinking:
		return back, true
	}
	return orbKeyName("A") + " ask again · " + back, true
}

// gaTick is the overlay's upkeep, once a frame before it is drawn: what ends
// it, the presses from the pad, and the orb kept the way it works here.
func gaTick(config *Config) {
	switch {
	case alarmFiringActive():
		// The alarm takes the screen over the paused game (alarmring.go). The
		// music the listening orb paused is the alarm's to resume: gaClose's
		// voiceOrbHideNow would play it under the ring (voiceOrbTick runs later).
		voiceOrbHandMediaToAlarm()
		gaClose("an alarm rings", false)
		return
	case !gameScreenSuspended():
		gaClose("the game ended", false)
		return
	case gaLink.lost.Swap(false):
		gaClose("the pad went away", true)
		return
	case time.Since(gaOverlay.openedAt) >= gaOverlayCap && !powerSleepActive():
		// Not while the handheld sleeps: the screen-off sleep would hand the
		// game back with the screen dark (a real suspend stops this clock).
		gaClose("it was up for three minutes", true)
		return
	}
	// The idle screens stay away while it is up, the way they do for a
	// ringing alarm: no input reached JukaHub for the whole game.
	if now := time.Now(); now.Sub(gaOverlay.nudgedAt) >= time.Second {
		gaOverlay.nudgedAt = now
		screensaverNotifyInput()
	}
	for _, b := range gaTakePresses() {
		gaPress(config, b)
		if !gaOverlay.active {
			return
		}
	}
	// An answer stays until the player leaves, and the orb's fixes (Settings,
	// the phone page) are not offered: they would open a screen of JukaHub's
	// with the game still paused behind it.
	if voiceOrb.phase == orbAnswer {
		voiceOrb.hideAt = 0
		voiceOrb.offer = orbOfferNone
	}
}

// gaClose takes the overlay down and lets the game run on. handBack draws one
// last frame of the game's own picture (gameAssistRender), so the frame
// JukaHub presents before parking again is not its Home screen.
func gaClose(why string, handBack bool) {
	if !gaOverlay.active {
		return
	}
	gaOverlay.active = false
	gaOverlay.closing = handBack
	// The watcher gives the pad back once nothing is held on it, so the game
	// is never handed the end of a press it did not see begin.
	gaLink.release.Store(true)
	gaLink.live.Store(false)
	gaTakePresses()
	gmPickDrop() // the in-game menu's slot pictures (gamemenu_states.go)
	jukaSpeakStop()
	voiceOrbHideNow()
	gaResume()
	if !handBack {
		gaDropFrame()
	}
	logInfo("gameassist", "back to the game: %s", why)
}

// gaResume lets the paused game run again — all but a child a ringing alarm
// paused as well: the alarm resumes it when it is answered, and resuming it
// now would draw the game under the ring.
func gaResume() {
	ringing := alarmFiringActive()
	for _, cmd := range gaOverlay.paused {
		if ringing && gaCmdIn(alarmHeld.children, cmd) {
			continue
		}
		if err := alarmSignalChild(cmd, false); err != nil {
			logWarn("gameassist", "could not resume the game: %v", err)
		}
	}
	gaOverlay.paused = nil
	gaPausedClear()
}

// gaCmdIn reports whether cmd is in list.
func gaCmdIn(list []*exec.Cmd, cmd *exec.Cmd) bool {
	for _, c := range list {
		if c == cmd {
			return true
		}
	}
	return false
}

// gaDropFrame frees the game's picture and its texture.
func gaDropFrame() {
	if gaOverlay.tex != nil {
		_ = gaOverlay.tex.Destroy()
		gaOverlay.tex = nil
	}
	gaOverlay.frame = nil
	gaOverlay.texW, gaOverlay.texH = 0, 0
	gaOverlay.texFailed = false
}

// gameAssistShutdown is the Cleanup hook: a game the assistant paused runs
// again (the exit path asks it to quit, which a stopped process would sit
// out), and the pad watcher gives the pad back.
func gameAssistShutdown() {
	if len(gaOverlay.paused) > 0 {
		for _, cmd := range gaOverlay.paused {
			_ = alarmSignalChild(cmd, false)
		}
		gaOverlay.paused = nil
		gaPausedClear()
	}
	gaOverlay.active = false
	gaLink.live.Store(false)
	gaPadStop()
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gameAssistRender draws the overlay over whatever scene is behind it — the
// game's picture dimmed, or a dark layer, and the game's name — before the
// alarm's takeover and the voice orb, which main.go draws next. Every frame
// it runs the upkeep that does not wait for the overlay (gameAssistFrame).
func gameAssistRender(renderer *sdl.Renderer, config *Config) {
	gameAssistFrame(config)
	if renderer == nil {
		return
	}
	if gaOverlay.closing {
		// The frame JukaHub presents before it parks: the game's own picture,
		// so the hand-back shows no frame of the screen behind the overlay.
		gaOverlay.closing = false
		gaDrawBackdrop(renderer, false)
		gaDropFrame()
		return
	}
	if !gaOverlay.active {
		return
	}
	gaTick(config)
	if !gaOverlay.active {
		if gaOverlay.closing {
			gaOverlay.closing = false
			gaDrawBackdrop(renderer, false)
			gaDropFrame()
		}
		return
	}
	gaDrawBackdrop(renderer, true)
	if fonts, ok := tbxLoadFonts(config); ok {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		switch {
		case gaOverlay.mode == gaModeMenu:
			// The menu card names the game itself (gamemenu.go).
			gmDraw(renderer, config, fonts)
		default:
			gaDrawHeader(renderer, fonts)
			if voiceOrb.phase == orbHidden {
				gaDrawPrompt(renderer, fonts)
			}
		}
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
}

// gaDrawBackdrop covers the whole screen: an opaque base (the scene behind
// must never show), the game's picture fitted on it, and — dim — the shade
// that lets the orb and its words read over any game.
func gaDrawBackdrop(renderer *sdl.Renderer, dim bool) {
	full := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	setDrawColor(renderer, sdl.Color{R: 8, G: 9, B: 14, A: 255})
	_ = renderer.FillRect(&full)
	gaEnsureTexture(renderer)
	if gaOverlay.tex != nil {
		dst := gaFit(gaOverlay.texW, gaOverlay.texH, screenWidth, screenHeight)
		_ = renderer.Copy(gaOverlay.tex, nil, &dst)
	}
	if dim {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		setDrawColor(renderer, sdl.Color{R: 0, G: 0, B: 0, A: 150})
		_ = renderer.FillRect(&full)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
}

// gaEnsureTexture uploads the game's picture once.
func gaEnsureTexture(renderer *sdl.Renderer) {
	fr := gaOverlay.frame
	if fr == nil || gaOverlay.tex != nil || gaOverlay.texFailed || len(fr.Pix) == 0 {
		return
	}
	w, h := int32(fr.Rect.Dx()), int32(fr.Rect.Dy())
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, h)
	if err != nil {
		gaOverlay.texFailed = true
		logDebug("gameassist", "no texture for the game's picture: %v", err)
		return
	}
	if err := tex.Update(nil, unsafe.Pointer(&fr.Pix[0]), fr.Stride); err != nil {
		_ = tex.Destroy()
		gaOverlay.texFailed = true
		logDebug("gameassist", "could not upload the game's picture: %v", err)
		return
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_NONE)
	gaOverlay.tex, gaOverlay.texW, gaOverlay.texH = tex, w, h
}

// gaFit is the largest rectangle of w:h that fits a sw×sh screen, centred.
// Pure.
func gaFit(w, h, sw, sh int32) sdl.Rect {
	if w <= 0 || h <= 0 || sw <= 0 || sh <= 0 {
		return sdl.Rect{X: 0, Y: 0, W: sw, H: sh}
	}
	dw, dh := sw, int32(int64(h)*int64(sw)/int64(w))
	if dh > sh {
		dw, dh = int32(int64(w)*int64(sh)/int64(h)), sh
	}
	return sdl.Rect{X: (sw - dw) / 2, Y: (sh - dh) / 2, W: dw, H: dh}
}

// gaDrawHeader is the glass card in the top-left corner: the game is paused,
// which game it is, and whether Juka sees its picture.
func gaDrawHeader(renderer *sdl.Renderer, fonts tbxFonts) {
	margin := scaleX(28, screenWidth)
	maxText := screenWidth*55/100 - 2*orbPad
	kicker := "GAME PAUSED"
	title := gaOverlay.title
	if title == "" {
		title = "Ask Juka about this game"
	}
	title = ellipsize(fonts.body, title, maxText)
	sub := gaOverlay.system
	if gaOverlay.shot && gaOverlay.frame != nil {
		if sub != "" {
			sub += " · "
		}
		sub += "Juka can see this screen"
	}
	w, _ := measureText(fonts.body, title)
	if kw, _ := measureText(fonts.tiny, kicker); kw > w {
		w = kw
	}
	h := orbPad + fonts.tinyH + 2 + fonts.bodyH + orbPad
	if sub != "" {
		sub = ellipsize(fonts.small, sub, maxText)
		if sw, _ := measureText(fonts.small, sub); sw > w {
			w = sw
		}
		h += 2 + fonts.smallH
	}
	card := sdl.Rect{X: margin, Y: margin, W: w + 2*orbPad, H: h}
	fill := glassSheetFill()
	fill.A = 236
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())
	x, y := card.X+orbPad, card.Y+orbPad
	drawText(renderer, fonts.tiny, kicker, x, y, glassAccent(), textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, title, x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH
	if sub != "" {
		drawText(renderer, fonts.small, sub, x, y+2, ColorTextSecondary(), textAlignLeft)
	}
}

// gaDrawPrompt is what shows while the orb is down (a clip too short to send
// puts it away): how to ask and how to go back.
func gaDrawPrompt(renderer *sdl.Renderer, fonts tbxFonts) {
	line := orbKeyName("A") + " ask Juka · " + orbKeyName("B") + " back to the game"
	if gaOverlay.fromMenu && gameMenuEnabled(appConfig) {
		line = orbKeyName("A") + " ask Juka · " + orbKeyName("B") + " menu"
	}
	tw, _ := measureText(fonts.body, line)
	w, h := tw+4*orbPad, fonts.bodyH+2*orbPad
	r := sdl.Rect{X: (screenWidth - w) / 2, Y: screenHeight*2/3 - h/2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 236
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, orbBubbleRadius, 1, glassSheetEdge())
	drawText(renderer, fonts.body, line, screenWidth/2, r.Y+orbPad, ColorTextPrimary(), textAlignCenter)
}

// ── The game's picture ───────────────────────────────────────────────────

// gaCapture reads the game's last frame from the panel's framebuffer, or nil:
// off the handheld, a framebuffer that cannot be read, or one that is black
// (the game drew elsewhere). Main thread: it must run before JukaHub presents
// a frame of its own. A few tens of milliseconds on the A53, while the screen
// still shows the paused game.
func gaCapture(config *Config) *image.RGBA {
	if !gameAssistSupported() {
		return nil
	}
	cw, ch := int(screenWidth), int(screenHeight)
	raw, geom, err := gaReadFB(cw, ch)
	if err != nil {
		logDebug("gameassist", "no picture of the game: %v", err)
		return nil
	}
	deg := castRotateFor(config)
	if deg < 0 {
		deg = castAutoRotate(geom.W, geom.H, cw, ch)
	}
	img, ok := gaFrameFromFB(raw, geom, deg)
	if !ok {
		logInfo("gameassist", "the framebuffer is black: no picture of the game")
		return nil
	}
	return img
}

// gaReadFB copies the visible part of the framebuffer (the half of a
// double-buffered panel on screen now).
func gaReadFB(canvasW, canvasH int) ([]byte, castFBGeom, error) {
	f, err := os.Open(castFBDevice)
	if err != nil {
		return nil, castFBGeom{}, err
	}
	defer f.Close()
	geom, err := castProbeFB(f, canvasW, canvasH)
	if err != nil {
		return nil, castFBGeom{}, err
	}
	bpp := geom.BPP / 8
	x, y := castFBPan(f, geom)
	raw := make([]byte, geom.Stride*(geom.H-1)+geom.W*bpp)
	n, err := f.ReadAt(raw, int64(y)*int64(geom.Stride)+int64(x*bpp))
	if n == 0 {
		if err == nil {
			err = errors.New("the framebuffer read returned nothing")
		}
		return nil, castFBGeom{}, err
	}
	if n < len(raw) {
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, castFBGeom{}, err
		}
		clear(raw[n:])
	}
	return raw, geom, nil
}

// gaFrameFromFB turns a framebuffer copy into an upright RGBA picture at most
// gaFrameMaxW wide; ok is false for a black one. Pure.
func gaFrameFromFB(raw []byte, g castFBGeom, deg int) (*image.RGBA, bool) {
	if !g.valid() || len(raw) < g.Stride*(g.H-1)+g.W*g.BPP/8 {
		return nil, false
	}
	pix, stride, lay := raw, g.Stride, g.Layout
	if g.BPP == 16 {
		conv := make([]byte, g.W*g.H*4)
		castRGB565ToRGBA(conv, raw, g.W, g.H, g.Stride)
		pix, stride, lay = conv, g.W*4, castLayoutRGBA
	}
	if castMeanLuma(pix, stride, g.W, g.H, lay) < gaBlackLuma {
		return nil, false
	}
	tw, th := castTargetSize(g.W, g.H, gaFrameMaxW, deg)
	if tw <= 0 || th <= 0 {
		return nil, false
	}
	sw, sh := tw, th
	if deg == 90 || deg == 270 {
		sw, sh = th, tw
	}
	scaled := image.NewRGBA(image.Rect(0, 0, sw, sh))
	castBoxScale(scaled, pix, stride, g.W, g.H, lay, castBoxBounds(nil, g.W, sw), castBoxBounds(nil, g.H, sh))
	// The copy castBoxScale takes for an RGBA picture of the same size keeps
	// the framebuffer's fourth byte, which no driver promises is opaque.
	for i := 3; i < len(scaled.Pix); i += 4 {
		scaled.Pix[i] = 255
	}
	if deg != 90 && deg != 180 && deg != 270 {
		return scaled, true
	}
	turned := image.NewRGBA(image.Rect(0, 0, tw, th))
	castRotateRGBA(turned, scaled, deg)
	return turned, true
}
