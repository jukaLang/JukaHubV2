package main

// voiceorb.go — the voice assistant's orb.
//
// Press Menu — the TrimUI button of a Smart Pro or a Brick — or F4 on a
// keyboard, anywhere in JukaHub, and a glowing orb rises in the bottom-right
// corner and says "Listening…". Say a command ("open YouTube", "volume 50",
// "take a screenshot") or ask a question. The orb stops listening by itself
// about a second after you stop talking; hold the button instead and it
// listens until you let go. Whisper turns the clip into words, and the same
// parser and executor as the Voice command page (voicecmd_intent.go,
// voicecmd_exec.go) run it: the orb says what it did ("Volume 50%"), counts
// down a moment before it opens something (B stops a misheard command), lists
// the matches when a name could mean several things, and hands a question that
// is no command to Juka, the Groq model the Chat tab talks to, whose answer
// streams into the bubble.
//
// It is an overlay, not a screen: it floats over whatever is up, owns the
// controls only while it listens, thinks, counts down or offers a choice, and
// an answer lingers without taking them — any button puts it away and does its
// own job. With the on-screen keyboard up the same button is voice typing
// (vkvoice.go), and on the Voice command page it is that page's microphone.
// Where a modal panel, the first-run tour or a game has the screen, Menu does
// what it did before (Back), and Settings → Controls → "Menu button: voice"
// gives it back for good.
//
// The microphone is voicemic.go's, music is paused while the orb listens (the
// speaker is next to the microphone) and resumes as soon as the clip ends, and
// neither the audio nor the words are kept.
//
// Main thread only, apart from the two request goroutines, which hand their
// results back through tbxPost (and Juka's streaming text through orbAIText).

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// orbPhase is what the orb is doing.
type orbPhase int

const (
	orbHidden    orbPhase = iota
	orbListening          // the microphone is open
	orbThinking           // Whisper (or Juka) is working
	orbConfirm            // "Opening …": the pause before a command runs
	orbChoices            // "Which one did you mean?"
	orbAnswer             // a sentence, lingering; maybe a fix one press away
)

// orbOffer is what A does on an answer.
type orbOffer int

const (
	orbOfferNone    orbOffer = iota
	orbOfferGroqKey          // Settings → General, where the Groq key goes
	orbOfferPhone            // the Voice command page's phone QR code
	orbOfferSearch           // the Home search sheet with the words typed in
)

// orbSource is where a press of the voice button came from.
type orbSource int

const (
	orbFromPad orbSource = iota
	orbFromKey
	orbFromClick
)

const (
	// orbRadius is the sphere's radius on a 720-line canvas.
	orbRadius = 34
	// orbMargin keeps the orb off the screen's edge and the footer.
	orbMargin = 22
	// orbPad is the bubble's inner margin and orbBubbleRadius its corners.
	orbPad          = int32(14)
	orbBubbleRadius = int32(16)
	orbBubbleMaxW   = int32(540)
	// orbAppearMs and orbFadeMs time the pop-in and the fade-out.
	orbAppearMs = 260
	orbFadeMs   = 220
	// orbHoldMs is how long a press of the voice button must last to be
	// push-to-talk: the clip then ends when the button comes up.
	orbHoldMs = 450
	// orbAnswerMinMs, orbAnswerMaxMs and orbAIAnswerMaxMs bound how long an
	// answer stays; orbOfferMinMs is the least an answer with a fix stays.
	orbAnswerMinMs   = 2600
	orbAnswerMaxMs   = 12000
	orbAIAnswerMaxMs = 22000
	orbOfferMinMs    = 6500
	// orbMaxChoiceRows is how many matches the bubble lists.
	orbMaxChoiceRows = 4
	// orbChunkBytes is one read from the microphone (128 ms).
	orbChunkBytes = 4096
	// orbSilencePeak is the loudest level (RMS) a clip may reach and still be
	// silence, never sent (Whisper invents sentences for silence). Lower than
	// voice typing's: a handheld's microphone gain is unknown, and a quiet
	// clip that is sent costs one "didn't catch that".
	orbSilencePeak = 0.006
)

// voiceOrbMenuKey is the Custom setting behind Settings → Controls → "Menu
// button: voice" (on unless it is false).
const voiceOrbMenuKey = "voiceMenuButton"

// orbJukaVoicePrompt is added to Juka's system prompt for a spoken question.
const orbJukaVoicePrompt = "This question was spoken to the handheld's voice assistant and the answer is shown " +
	"in a small bubble: answer in one or two short sentences of plain text, with no lists and no markdown."

// voiceOrb is the orb. Main thread only.
var voiceOrb struct {
	phase   orbPhase
	shownAt uint64 // the pop-in began
	phaseAt uint64 // this phase began
	hideAt  uint64 // an answer starts to fade (0: it stays)
	fadeAt  uint64 // a fade-out began (0: none)

	// Capture.
	mic         *micStream
	buf         []byte
	chunk       []byte
	started     uint64
	level       float64
	shownLevel  float64
	peak        float64
	vad         orbVAD
	musicHushed bool

	// The press of the voice button that opened the listen, for push-to-talk.
	pressAt  uint64
	pressSrc orbSource
	held     bool
	// menuTaken and keyTaken: the orb took a Menu or F4 press, so it takes the
	// release too; mouseTaken is the same for a click.
	menuTaken  bool
	keyTaken   bool
	mouseTaken bool

	// What was heard and what the orb says.
	heard string
	title string
	sub   string
	tone  tbxTone
	offer orbOffer
	words string // what a search offer searches for

	// The command waiting out the confirm pause, and the choices.
	act       func(*Config)
	confirmAt uint64
	choices   []voiceMatch
	cursor    int

	// The request in flight: gen invalidates late answers, ai marks Juka's.
	gen    int
	cancel context.CancelFunc
	ai     bool

	// Hit areas from the last frame.
	orbRect    sdl.Rect
	bubbleRect sdl.Rect
	rowRects   [orbMaxChoiceRows]sdl.Rect
	offerRect  sdl.Rect

	// Wrapped text, kept while the text and the width stay the same.
	wrapTitle, wrapSub orbWrap
}

// orbAIText is Juka's answer as it streams in: the request goroutine writes
// it, the frame reads it.
var orbAIText struct {
	mu   sync.Mutex
	gen  int
	text string
}

// padEventRaw is the physical button of the controller event being handled,
// before applyButtonSwap folded GUIDE into BACK and swapped the face buttons
// (core.go records it): the orb's Menu is GUIDE, not BACK.
var padEventRaw uint8

// orbNow is SDL's clock in milliseconds, never 0: the orb keeps 0 for "not
// set" in its timestamps, and the clock reads 0 in its first millisecond.
func orbNow() uint64 {
	if now := sdl.GetTicks64(); now > 0 {
		return now
	}
	return 1
}

// voiceOrbActive reports whether the orb is up and not on its way out.
func voiceOrbActive() bool { return voiceOrb.phase != orbHidden && voiceOrb.fadeAt == 0 }

// voiceOrbModal reports whether the orb owns the controls: while it listens,
// thinks, counts down or offers a choice. An answer lingers without them.
func voiceOrbModal() bool {
	if !voiceOrbActive() {
		return false
	}
	switch voiceOrb.phase {
	case orbListening, orbThinking, orbConfirm, orbChoices:
		return true
	}
	return false
}

// ── Settings ─────────────────────────────────────────────────────────────

// voiceOrbMenuEnabled reports Settings → Controls → "Menu button: voice".
func voiceOrbMenuEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, voiceOrbMenuKey, true)
}

// voiceOrbSettingsRows is the switch in Settings → Controls (stickcal.go), on
// a handheld: a PC's voice key is F4 whatever it says.
func voiceOrbSettingsRows() []setRow {
	return []setRow{{
		ID: voiceOrbMenuKey, Label: "Menu button: voice", Kind: setKindToggle,
		Detail:  setStatic("Menu opens the voice orb (hold it to talk); off, Menu goes back like B"),
		Visible: func(*Config) bool { return P().IsHandheld() },
		Get:     voiceOrbMenuEnabled,
		Set: func(c *Config, on bool) {
			if c == nil {
				return
			}
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			c.Variables.Custom[voiceOrbMenuKey] = on
			saveUserConfigDebounced(userConfigFromVariables(c))
			if on {
				setToast("Menu button: voice")
			} else {
				setToast("Menu button: back")
			}
		},
	}}
}

// voiceOrbLegendRow is the controller card's "every screen" row for voice:
// F4 on a keyboard, Menu on a handheld whose Menu is the voice button.
func voiceOrbLegendRow(keyboard bool) (FooterHint, bool) {
	if keyboard {
		return FooterHint{Button: "F4", Label: "Voice"}, true
	}
	if !P().IsHandheld() || !voiceOrbMenuEnabled(appConfig) {
		return FooterHint{}, false
	}
	return FooterHint{Button: "Menu", Label: "Voice (hold to talk)"}, true
}

// ── Input ────────────────────────────────────────────────────────────────

// voiceOrbController routes a pad button to the orb. It runs before the quit
// chord and every overlay (main.go): the Menu button (raw GUIDE) opens, stops
// and answers the orb, and while the orb owns the controls its buttons are
// the orb's. It reports whether the event was used.
func voiceOrbController(e *sdl.ControllerButtonEvent, raw uint8, config *Config) bool {
	if e == nil || ctrldiag.open {
		return false
	}
	down := e.Type == sdl.CONTROLLERBUTTONDOWN
	if raw == sdl.CONTROLLER_BUTTON_GUIDE && voiceOrbMenuOwnsGuide(config, e.Which) {
		if !down {
			if voiceOrb.menuTaken {
				voiceOrb.menuTaken = false
				voiceOrbRelease(config, orbFromPad)
				return true
			}
			return false
		}
		if voiceOrbMenuPress(config) {
			voiceOrb.menuTaken = true
			return true
		}
		return false
	}
	// Releases always pass: a hold that began before the orb ends where it
	// began.
	if !down || !voiceOrbActive() {
		return false
	}
	if voiceOrbModal() {
		// Select + Start still quits from under the orb; the chord is fed here
		// because the press goes no further.
		if exitChordPressed(e) {
			requestAppExit()
			return true
		}
		b, dropped := a11yResolveButton(e.Button)
		if dropped {
			return true
		}
		switch b {
		case sdl.CONTROLLER_BUTTON_A:
			voiceOrbConfirmButton(config)
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			voiceOrbCancel()
			PlayBackSound()
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			voiceOrbMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			voiceOrbMove(1)
		}
		return true
	}
	// An answer: A runs its fix, B puts it away; any other button puts it
	// away and does its own job.
	b, _ := a11yResolveButton(e.Button)
	switch b {
	case sdl.CONTROLLER_BUTTON_A:
		if voiceOrb.offer != orbOfferNone {
			voiceOrbRunOffer(config)
			return true
		}
	case sdl.CONTROLLER_BUTTON_B:
		voiceOrbDismiss()
		PlayBackSound()
		return true
	}
	voiceOrbDismiss()
	return false
}

// voiceOrbMenuOwnsGuide reports whether the Menu button means voice here: a
// handheld, the setting on, not a game JukaHub runs itself (the arcade and
// JukaLand read their own buttons), and a pad whose mapping has a Select of
// its own — on one that reports Select as GUIDE, GUIDE stays Back.
func voiceOrbMenuOwnsGuide(config *Config, which sdl.JoystickID) bool {
	if !P().IsHandheld() || !voiceOrbMenuEnabled(config) {
		return false
	}
	switch tbxCurrentScene(config) {
	case "Games", "JukaLand":
		return false
	}
	return voiceOrbPadHasMenu(which)
}

// voiceOrbPadHasMenu reports whether the pad that sent the press maps both a
// Select (back) and a Menu (guide) button. A press with no pad behind it (a
// queued repeat, a control profile) asks the main pad.
func voiceOrbPadHasMenu(which sdl.JoystickID) bool {
	pad := sdl.GameControllerFromInstanceID(which)
	if pad == nil {
		pad = gameController
	}
	if pad == nil {
		return true
	}
	return orbMappingHasMenu(pad.Mapping())
}

// orbMappingHasMenu reads an SDL controller mapping string. An empty one (no
// mapping known) counts as yes: the TrimUI pads all have both. Pure.
func orbMappingHasMenu(mapping string) bool {
	m := strings.ToLower(mapping)
	if strings.TrimSpace(m) == "" {
		return true
	}
	return strings.Contains(m, ",back:") && strings.Contains(m, ",guide:")
}

// voiceOrbMenuPress is a press of the Menu button: voice typing while the
// keyboard is up, the Voice command page's own microphone on that page, the
// orb everywhere else. false leaves the press to do what Menu did before.
func voiceOrbMenuPress(config *Config) bool {
	if virtualKeyboardActive && !voiceOrbActive() {
		if vkToolEnabled(vkToolMic) {
			vkVoiceToggle(config)
			return true
		}
		return false
	}
	if !voiceOrbActive() && voiceOnPage(config) {
		voiceToggle(config)
		return true
	}
	return voiceOrbPress(config, orbFromPad)
}

// voiceOrbF4 is the keyboard's voice key, the same choices as Menu. It is
// used up even when nothing can listen, so F4 never falls through to a screen.
func voiceOrbF4(config *Config) bool {
	if virtualKeyboardActive && !voiceOrbActive() {
		if vkToolEnabled(vkToolMic) {
			vkVoiceToggle(config)
		}
		return true
	}
	if !voiceOrbActive() && voiceOnPage(config) {
		voiceToggle(config)
		return true
	}
	return voiceOrbPress(config, orbFromKey)
}

// voiceOrbKey routes a key to the orb ahead of every panel (main.go): F4 is
// the voice key, and while the orb owns the controls Enter, Space, Esc and
// the arrows answer it. Keys a handheld's own buttons send (volume, power)
// pass.
func voiceOrbKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	sym := e.Keysym.Sym
	if sym == sdl.K_F4 && e.Keysym.Mod&(sdl.KMOD_ALT|sdl.KMOD_CTRL) == 0 {
		if e.Repeat != 0 {
			// A held F4 repeats: the first press already acted.
			return voiceOrb.keyTaken
		}
		if voiceOrbF4(config) {
			voiceOrb.keyTaken = true
			return true
		}
		return false
	}
	if !voiceOrbActive() || !hintTypingScancode(int(e.Keysym.Scancode)) {
		return false
	}
	if voiceOrbModal() {
		switch sym {
		case sdl.K_UP:
			voiceOrbMove(-1)
		case sdl.K_DOWN:
			voiceOrbMove(1)
		}
		if e.Repeat != 0 {
			return true
		}
		switch sym {
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			voiceOrbConfirmButton(config)
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			voiceOrbCancel()
			PlayBackSound()
		}
		return true
	}
	switch sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		if voiceOrb.offer != orbOfferNone && e.Repeat == 0 {
			voiceOrbRunOffer(config)
			return true
		}
	case sdl.K_ESCAPE:
		voiceOrbDismiss()
		PlayBackSound()
		return true
	}
	voiceOrbDismiss()
	return false
}

// voiceOrbKeyUp is a key coming up: F4 after a long hold ends the clip.
func voiceOrbKeyUp(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYUP || e.Keysym.Sym != sdl.K_F4 || !voiceOrb.keyTaken {
		return
	}
	voiceOrb.keyTaken = false
	voiceOrbRelease(config, orbFromKey)
}

// voiceOrbMouse takes a left click (the press, then its release): on the orb
// it is the voice button, on a choice or the fix it picks, and while the orb
// owns the controls a click elsewhere cancels it. An answer goes away at a
// click elsewhere, which then does its own job.
func voiceOrbMouse(down bool, mx, my int32, config *Config) bool {
	if !down {
		if voiceOrb.mouseTaken {
			voiceOrb.mouseTaken = false
			return true
		}
		return false
	}
	if !voiceOrbActive() {
		return false
	}
	voiceOrb.mouseTaken = true
	if voiceOrb.orbRect.W > 0 && pointInRect(mx, my, voiceOrb.orbRect) {
		voiceOrbPress(config, orbFromClick)
		return true
	}
	if voiceOrb.phase == orbChoices {
		for i := 0; i < len(voiceOrb.choices) && i < orbMaxChoiceRows; i++ {
			if r := voiceOrb.rowRects[i]; r.W > 0 && pointInRect(mx, my, r) {
				voiceOrb.cursor = i
				voiceOrbPick(config, i)
				return true
			}
		}
	}
	if voiceOrb.offer != orbOfferNone && voiceOrb.offerRect.W > 0 && pointInRect(mx, my, voiceOrb.offerRect) {
		voiceOrbRunOffer(config)
		return true
	}
	if voiceOrb.bubbleRect.W > 0 && pointInRect(mx, my, voiceOrb.bubbleRect) {
		if voiceOrb.phase == orbConfirm {
			// A tap on "Opening …" goes now.
			voiceOrbRunPending(config)
		}
		return true
	}
	if voiceOrbModal() {
		voiceOrbCancel()
		PlayBackSound()
		return true
	}
	voiceOrb.mouseTaken = false
	voiceOrbDismiss()
	return false
}

// voiceOrbConfirmButton is A (Enter) while the orb owns the controls.
func voiceOrbConfirmButton(config *Config) {
	switch voiceOrb.phase {
	case orbListening:
		voiceOrbFinish(config)
	case orbConfirm:
		voiceOrbRunPending(config)
	case orbChoices:
		voiceOrbPick(config, voiceOrb.cursor)
	}
}

// voiceOrbMove walks the choices.
func voiceOrbMove(d int) {
	n := len(voiceOrb.choices)
	if voiceOrb.phase != orbChoices || n == 0 {
		return
	}
	if n > orbMaxChoiceRows {
		n = orbMaxChoiceRows
	}
	c := voiceOrb.cursor + d
	if c < 0 {
		c = 0
	}
	if c >= n {
		c = n - 1
	}
	if c != voiceOrb.cursor {
		voiceOrb.cursor = c
		PlayNavSound()
	}
}

// ── Listening ────────────────────────────────────────────────────────────

// voiceOrbPress is a press of the voice button (Menu, F4, a click on the orb):
// it opens the orb and listens, stops a listen, or skips the confirm pause. It
// reports false when the orb cannot come up now (see voiceOrbCanOpen).
func voiceOrbPress(config *Config, src orbSource) bool {
	if voiceOrbActive() {
		switch voiceOrb.phase {
		case orbListening:
			voiceOrbFinish(config)
			return true
		case orbThinking:
			// B cancels; another press must not throw the clip away.
			return true
		case orbConfirm:
			voiceOrbRunPending(config)
			return true
		}
	}
	if !voiceOrbCanOpen(config) {
		return false
	}
	voiceOrb.held = src != orbFromClick
	voiceOrb.pressSrc = src
	voiceOrb.pressAt = orbNow()
	voiceOrbListen(config)
	return true
}

// voiceOrbRelease is the voice button coming up: after a long hold the clip
// ends with it; after a tap the orb keeps listening until the speaker stops.
func voiceOrbRelease(config *Config, src orbSource) {
	if !voiceOrb.held || voiceOrb.pressSrc != src {
		return
	}
	voiceOrb.held = false
	if voiceOrb.phase == orbListening && voiceOrb.fadeAt == 0 && orbNow()-voiceOrb.pressAt >= orbHoldMs {
		voiceOrbFinish(config)
	}
}

// voiceOrbCanOpen reports whether the orb may come up now, putting away the
// light overlays it replaces: the Start menu, Quick Settings, the search
// sheet, the help cards, the notification list and the header's popovers. A
// modal panel, a card (the first-run tour, recovery, a failed launch, an
// update), the alarm, a game, a video being prepared and the controller test
// keep the screen.
func voiceOrbCanOpen(config *Config) bool {
	if config == nil || alarmFiringActive() || gameScreenSuspended() || onboarding.active ||
		recoveryCard.open || launchFailure.open || updatePrompt.open || videoPrepareActive() || ctrldiag.open ||
		qrZoomActive() || wpOpen || clipSearchOpen || patchModal != nil || chatOverlayOpen() {
		return false
	}
	dismissWeatherPopover()
	dismissVolumePopover()
	dismissBrightnessPopover()
	if why := modalPanelBlockReason(); why != "" {
		logDebug("voice", "orb not opened: %s", why)
		return false
	}
	tb.startOpen = false
	qs.open = false
	if globalSearchOpen {
		homeSearchClose()
	}
	shortcutsOpen = false
	controlsLegend.open = false
	nc.open = false
	homeCalendarOpen = false
	return true
}

// voiceOrbListen opens the microphone and starts a clip, or says what is
// missing with the fix one press away.
func voiceOrbListen(config *Config) {
	voiceOrbStopCapture()
	voiceOrbCancelRequest()
	voiceOrb.heard, voiceOrb.words = "", ""
	voiceOrb.choices, voiceOrb.act = nil, nil
	switch {
	case strings.TrimSpace(groqAPIKey()) == "":
		voiceOrbSay("Voice needs a Groq API key", "Add it in Settings → General — or say it on your phone", tbxToneWarn, orbOfferGroqKey)
		PlayErrorSound()
		return
	case !IsOnline():
		voiceOrbSay("Voice needs internet", "Your phone's keyboard microphone works over the same Wi-Fi", tbxToneWarn, orbOfferPhone)
		PlayErrorSound()
		return
	}
	mic, err := micOpen()
	if err != nil {
		title := "Couldn't open the microphone"
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			title = "No microphone found"
		}
		voiceOrbSay(title, "Say it on your phone instead — it turns speech into words itself", tbxToneWarn, orbOfferPhone)
		PlayErrorSound()
		return
	}
	vkMic.Store(vkMicPresent)
	voiceOrb.mic = mic
	if cap(voiceOrb.buf) < voiceMaxBytes {
		voiceOrb.buf = make([]byte, 0, voiceMaxBytes)
	}
	voiceOrb.buf = voiceOrb.buf[:0]
	voiceOrb.started = orbNow()
	voiceOrb.level, voiceOrb.peak = 0, 0
	voiceOrb.vad = orbVAD{}
	voiceOrbSetPhase(orbListening)
	voiceOrb.title = "Listening…"
	voiceOrb.sub = "Say “open YouTube”, “volume 50” — or ask a question"
	voiceOrb.tone, voiceOrb.offer = tbxToneNeutral, orbOfferNone
	// Music would be heard as speech: it waits while the orb listens.
	voiceOrbHushMusic()
	PlayActivateSound()
}

// voiceOrbReadAudio moves what the microphone captured into the clip and
// follows its level. The first voiceChimeMs are the start chime, not speech.
func voiceOrbReadAudio(now uint64) {
	m := voiceOrb.mic
	if m == nil {
		return
	}
	if len(voiceOrb.chunk) < orbChunkBytes {
		voiceOrb.chunk = make([]byte, orbChunkBytes)
	}
	for i := 0; i < 16; i++ {
		room := cap(voiceOrb.buf) - len(voiceOrb.buf)
		if room <= 0 {
			return
		}
		chunk := voiceOrb.chunk[:orbChunkBytes]
		if len(chunk) > room {
			chunk = chunk[:room&^1]
		}
		n := m.Read(chunk)
		if n <= 0 {
			return
		}
		if now-voiceOrb.started < voiceChimeMs {
			continue
		}
		data := chunk[:n]
		voiceOrb.buf = append(voiceOrb.buf, data...)
		level := vkPCMLevel(data)
		if level > voiceOrb.peak {
			voiceOrb.peak = level
		}
		// Fast attack, slow release: the orb jumps with a syllable and settles.
		if level > voiceOrb.level {
			voiceOrb.level = level
		} else {
			voiceOrb.level = voiceOrb.level*0.82 + level*0.18
		}
		voiceOrb.vad.feed(level, now)
	}
}

// voiceOrbListenTick records one frame's audio and ends the clip by itself:
// when it is full, after a pause that follows speech, or when nothing was said
// at all. A held button keeps it open until it comes up.
func voiceOrbListenTick(config *Config, now uint64) {
	voiceOrbReadAudio(now)
	pushToTalk := voiceOrb.held && now-voiceOrb.pressAt >= orbHoldMs
	switch {
	case len(voiceOrb.buf) >= cap(voiceOrb.buf):
		voiceOrbFinish(config)
	case pushToTalk:
	case voiceOrb.vad.speaking && voiceOrb.vad.quietSince != 0 && now-voiceOrb.vad.quietSince >= voiceEndQuietMs:
		voiceOrbFinish(config)
	case !voiceOrb.vad.speaking && now-voiceOrb.started >= voiceNoSpeechMs:
		voiceOrbHeardNothing(voiceOrb.mic != nil && !voiceOrb.mic.Heard())
	}
}

// voiceOrbHeardNothing says the clip had no speech in it. silent means the
// microphone gave no sound at all, which is a different problem.
func voiceOrbHeardNothing(silent bool) {
	voiceOrbStopCapture()
	voiceOrb.buf = voiceOrb.buf[:0]
	if silent {
		voiceOrbSay("The microphone gives no sound", "Its input may be off on this firmware — your phone's keyboard microphone works too", tbxToneWarn, orbOfferPhone)
	} else {
		voiceOrbSay("Didn't hear anything", "Press "+orbKeyName("Menu")+" and speak up — or use your phone", tbxToneWarn, orbOfferPhone)
	}
	PlayErrorSound()
}

// voiceOrbFinish ends the clip and sends it to Whisper, unless it was too
// short (a double press) or silent.
func voiceOrbFinish(config *Config) {
	if voiceOrb.phase != orbListening {
		return
	}
	voiceOrbReadAudio(orbNow())
	silent := voiceOrb.mic != nil && !voiceOrb.mic.Heard()
	voiceOrbStopCapture()
	ms := len(voiceOrb.buf) * 1000 / (vkVoiceRate * 2)
	switch {
	case ms < vkVoiceMinMs:
		voiceOrb.buf = voiceOrb.buf[:0]
		voiceOrbDismiss()
		PlayBackSound()
		return
	case voiceOrb.peak < orbSilencePeak:
		voiceOrbHeardNothing(silent)
		return
	}
	key := strings.TrimSpace(groqAPIKey())
	if key == "" {
		voiceOrb.buf = voiceOrb.buf[:0]
		voiceOrbSay("Voice needs a Groq API key", "Add it in Settings → General", tbxToneWarn, orbOfferGroqKey)
		PlayErrorSound()
		return
	}
	if config == nil {
		config = appConfig
	}
	wav := vkEncodeWAV(voiceOrb.buf, vkVoiceRate)
	voiceOrb.buf = voiceOrb.buf[:0]
	// The names on this handheld go with the clip, so "JukaLand" and a game's
	// title come back spelled right; the same catalog resolves the answer.
	targets := voiceTargetsFromApp(config)
	voiceCatalogKeep(targets)
	prompt := voiceWhisperPrompt(targets)
	voiceOrbCancelRequest()
	gen := voiceOrb.gen
	ctx, cancel := context.WithTimeout(context.Background(), vkVoiceTimeout)
	voiceOrb.cancel = cancel
	voiceOrb.ai = false
	voiceOrbSetPhase(orbThinking)
	voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = "Thinking…", "", tbxToneNeutral, orbOfferNone
	PlayActivateSound()
	go func() {
		defer cancel()
		text, err := voiceTranscribe(ctx, key, wav, prompt)
		tbxPost(func() { voiceOrbTranscribed(gen, text, err) })
	}()
}

// voiceOrbTranscribed takes Whisper's answer, if it is still the one waited
// for.
func voiceOrbTranscribed(gen int, text string, err error) {
	if gen != voiceOrb.gen || voiceOrb.phase != orbThinking || voiceOrb.ai || !voiceOrbActive() {
		return
	}
	voiceOrb.cancel = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		msg, fix := voiceErrorText(err)
		offer := orbOfferNone
		switch fix {
		case voiceFixGroqKey:
			offer = orbOfferGroqKey
		case voiceFixPhone, voiceFixType:
			offer = orbOfferPhone
		}
		voiceOrbSay(msg, "", tbxToneWarn, offer)
		PlayErrorSound()
		return
	}
	text = voiceClip(text)
	if text == "" {
		voiceOrbSay("Didn't catch that", "Press "+orbKeyName("Menu")+" and try again", tbxToneWarn, orbOfferNone)
		PlayErrorSound()
		return
	}
	voiceOrbCommand(appConfig, text)
}

// ── Running a command ────────────────────────────────────────────────────

// voiceOrbCommand runs what was heard through the Voice command page's parser
// and executor, then shows what it did — or asks Juka, when it was a question
// that names nothing here.
func voiceOrbCommand(config *Config, text string) {
	voiceOrb.heard = text
	in := voiceParse(text)
	logDebug("voice", "orb: command understood as intent %d", int(in.Kind))
	out := voiceExecute(config, in, voiceFromMic)
	if voiceOrbWantsJuka(in, out) {
		voiceOrbAsk(text)
		return
	}
	voiceOrbShowOutcome(out)
}

// voiceOrbShowOutcome puts an outcome on the orb: the pause before its action
// runs, the choices, or the answer.
func voiceOrbShowOutcome(out voiceOutcome) {
	heard := orbQuote(voiceOrb.heard)
	switch {
	case out.Cancel:
		voiceOrbDismiss()
		PlayBackSound()
	case len(out.Choices) > 0:
		voiceOrb.choices = out.Choices
		if len(voiceOrb.choices) > orbMaxChoiceRows {
			voiceOrb.choices = voiceOrb.choices[:orbMaxChoiceRows]
		}
		voiceOrb.cursor = 0
		voiceOrb.act = nil
		voiceOrbSetPhase(orbChoices)
		voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = out.Say, heard, tbxToneNeutral, orbOfferNone
		PlayNavSound()
	case out.Act != nil:
		voiceOrb.act = out.Act
		voiceOrb.choices = nil
		voiceOrb.confirmAt = orbNow() + voiceConfirmMs
		voiceOrbSetPhase(orbConfirm)
		voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = out.Say, heard, tbxToneAccent, orbOfferNone
		PlaySuccessSound()
	default:
		tone := out.Tone
		if out.Done && tone == tbxToneNeutral {
			tone = tbxToneGood
		}
		offer, words := orbOfferNone, ""
		switch {
		case out.Fix == voiceFixGroqKey:
			offer = orbOfferGroqKey
		case out.Fix == voiceFixPhone || out.Fix == voiceFixType:
			offer = orbOfferPhone
		case out.Search != "":
			offer, words = orbOfferSearch, out.Search
		}
		voiceOrbSay(out.Say, heard, tone, offer)
		voiceOrb.words = words
		switch {
		case out.Done:
			PlaySuccessSound()
		case tone == tbxToneWarn || tone == tbxToneBad:
			PlayErrorSound()
		}
	}
}

// voiceOrbPick opens one of the listed matches: the pick is the confirmation.
func voiceOrbPick(config *Config, i int) {
	if voiceOrb.phase != orbChoices || i < 0 || i >= len(voiceOrb.choices) {
		return
	}
	out := voiceOutcomeFor(voiceOrb.choices[i])
	if out.Act == nil {
		voiceOrbSay(out.Say, orbQuote(voiceOrb.heard), out.Tone, orbOfferNone)
		PlayErrorSound()
		return
	}
	voiceOrb.act = out.Act
	PlayActivateSound()
	voiceOrbRunPending(config)
}

// voiceOrbRunPending runs the confirmed command. The orb goes first, so the
// command's own screen — or a screenshot of the one behind — has no orb in it.
func voiceOrbRunPending(config *Config) {
	act := voiceOrb.act
	voiceOrb.act = nil
	voiceOrbHideNow()
	if act == nil {
		return
	}
	if config == nil {
		config = appConfig
	}
	act(config)
}

// voiceOrbRunOffer runs an answer's fix.
func voiceOrbRunOffer(config *Config) {
	offer, words := voiceOrb.offer, voiceOrb.words
	voiceOrbHideNow()
	if config == nil {
		config = appConfig
	}
	switch offer {
	case orbOfferGroqKey:
		PlayActivateSound()
		vkVoiceOpenKeySettings(config)
	case orbOfferPhone:
		PlayActivateSound()
		voiceOrbOpenPhone(config)
	case orbOfferSearch:
		PlayActivateSound()
		voiceOpenSearchSheet(config, words)
	}
}

// voiceOrbOpenPhone opens the Voice command page with its phone QR code: the
// phone's keyboard microphone turns speech into words itself, so it needs
// neither the handheld's microphone nor a Groq key.
func voiceOrbOpenPhone(config *Config) {
	if config == nil {
		return
	}
	if voiceOnPage(config) {
		voiceOpenPhoneSheet(config)
		return
	}
	voiceOpen(config)
	voiceAfterTransition(func(c *Config) {
		if voiceOnPage(c) {
			voiceOpenPhoneSheet(c)
		}
	})
}

// ── Juka ─────────────────────────────────────────────────────────────────

// orbQuestionWords open a question rather than a name.
var orbQuestionWords = voiceSet("what", "who", "whom", "whose", "why", "how", "when", "where", "which",
	"is", "are", "was", "were", "do", "does", "did", "can", "could", "should", "would", "will",
	"tell", "explain", "define", "describe", "recommend", "suggest", "translate", "calculate", "give")

// voiceOrbWantsJuka reports whether what was heard is a question for Juka: a
// sentence the grammar read as a bare name ("what is the capital of France")
// that matched nothing, while Juka can be reached.
func voiceOrbWantsJuka(in voiceIntent, out voiceOutcome) bool {
	if in.Kind != voiceIntentOpen || in.Verb != "" || out.Act != nil || len(out.Choices) > 0 {
		return false
	}
	if !orbLooksLikeQuestion(in.Heard) {
		return false
	}
	return strings.TrimSpace(groqAPIKey()) != "" && IsOnline()
}

// orbLooksLikeQuestion reports whether a folded sentence reads as a question
// or a request rather than a name: it opens with a question word, or it is
// four words or more. Pure.
func orbLooksLikeQuestion(folded string) bool {
	words := strings.Fields(folded)
	if len(words) == 0 {
		return false
	}
	return orbQuestionWords[words[0]] || len(words) >= 4
}

// voiceOrbAsk puts a question to Juka and streams the answer into the bubble.
func voiceOrbAsk(question string) {
	key := strings.TrimSpace(groqAPIKey())
	models := groqCandidatesFor(groqModel())
	voiceOrbCancelRequest()
	gen := voiceOrb.gen
	voiceOrb.ai = true
	voiceOrbSetPhase(orbThinking)
	voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = "Thinking…", orbQuote(question), tbxToneNeutral, orbOfferNone
	orbAIText.mu.Lock()
	orbAIText.gen, orbAIText.text = gen, ""
	orbAIText.mu.Unlock()
	messages := []map[string]string{
		{"role": "system", "content": groqSystemPrompt + " " + orbJukaVoicePrompt},
		{"role": "user", "content": question},
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("voice", "Juka's answer stopped: %v", r)
				tbxPost(func() { voiceOrbAnswered(gen, question, "", errors.New("Juka's answer stopped")) })
			}
		}()
		reply, err := chatGroqAsk(key, models, messages, func(partial string) {
			orbAIText.mu.Lock()
			if orbAIText.gen == gen {
				orbAIText.text = partial
			}
			orbAIText.mu.Unlock()
		})
		tbxPost(func() { voiceOrbAnswered(gen, question, reply, err) })
	}()
}

// voiceOrbTakeAIText shows the part of Juka's answer that has arrived.
func voiceOrbTakeAIText() {
	orbAIText.mu.Lock()
	gen, text := orbAIText.gen, orbAIText.text
	orbAIText.mu.Unlock()
	if gen != voiceOrb.gen || text == "" {
		return
	}
	if t := orbPlain(chatStripThink(text)); t != "" {
		voiceOrb.title = t
	}
}

// voiceOrbAnswered shows Juka's whole answer.
func voiceOrbAnswered(gen int, question, reply string, err error) {
	if gen != voiceOrb.gen || voiceOrb.phase != orbThinking || !voiceOrb.ai || !voiceOrbActive() {
		return
	}
	voiceOrb.ai = false
	if err != nil {
		logWarn("voice", "Juka could not answer: %v", err)
		voiceOrbSay(chatFriendlyAIError(err), orbQuote(question), tbxToneWarn, orbOfferNone)
		PlayErrorSound()
		return
	}
	reply = orbPlain(chatStripThink(reply))
	if reply == "" {
		voiceOrbSay("Juka had no answer — try again", orbQuote(question), tbxToneWarn, orbOfferNone)
		PlayErrorSound()
		return
	}
	voiceOrbSay(reply, orbQuote(question), tbxToneNeutral, orbOfferNone)
	voiceOrb.hideAt = voiceOrb.phaseAt + orbReadMs(reply, false, true)
	PlayNavSound()
}

// orbPlainReplacer drops the markdown a model may still use.
var orbPlainReplacer = strings.NewReplacer("**", "", "__", "", "*", "", "`", "", "#", "")

// orbPlain turns a model's answer into one paragraph of plain text: no
// markdown emphasis, code ticks, headings or list bullets. Pure.
func orbPlain(s string) string {
	s = orbPlainReplacer.Replace(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimLeft(strings.TrimSpace(l), "-•> ")
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// ── State ────────────────────────────────────────────────────────────────

// voiceOrbSay shows an answer: a sentence, the line under it, and the fix A
// runs. It stays as long as it takes to read.
func voiceOrbSay(title, sub string, tone tbxTone, offer orbOffer) {
	voiceOrbSetPhase(orbAnswer)
	voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = title, sub, tone, offer
	voiceOrb.words = ""
	voiceOrb.choices, voiceOrb.act = nil, nil
	voiceOrb.hideAt = voiceOrb.phaseAt + orbReadMs(title+" "+sub, offer != orbOfferNone, false)
}

// voiceOrbSetPhase moves the orb to p, bringing it up if it was down or
// going.
func voiceOrbSetPhase(p orbPhase) {
	now := orbNow()
	if voiceOrb.phase == orbHidden || voiceOrb.fadeAt != 0 {
		voiceOrb.shownAt = now
	}
	voiceOrb.phase = p
	voiceOrb.phaseAt = now
	voiceOrb.fadeAt, voiceOrb.hideAt = 0, 0
}

// voiceOrbCancel is B: stop listening or waiting, drop the command or the
// choices, and let the orb go.
func voiceOrbCancel() {
	voiceOrb.act = nil
	voiceOrbDismiss()
}

// voiceOrbDismiss lets the orb fade out. The microphone and any request stop
// at once.
func voiceOrbDismiss() {
	if voiceOrb.phase == orbHidden {
		return
	}
	voiceOrbStopCapture()
	voiceOrbCancelRequest()
	voiceOrb.act = nil
	voiceOrb.held = false
	if voiceOrb.fadeAt == 0 {
		voiceOrb.fadeAt = orbNow()
	}
}

// voiceOrbHideNow takes the orb off the screen at once.
func voiceOrbHideNow() {
	voiceOrbStopCapture()
	voiceOrbCancelRequest()
	voiceOrb.phase = orbHidden
	voiceOrb.fadeAt, voiceOrb.hideAt = 0, 0
	voiceOrb.act = nil
	voiceOrb.choices = nil
	voiceOrb.offer = orbOfferNone
	voiceOrb.ai = false
	voiceOrb.held = false
	voiceOrb.orbRect, voiceOrb.bubbleRect, voiceOrb.offerRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	voiceOrb.rowRects = [orbMaxChoiceRows]sdl.Rect{}
}

// voiceOrbAway is the main loop's word that a game or an app has the screen:
// nothing may listen behind it.
func voiceOrbAway() {
	if voiceOrb.phase != orbHidden {
		voiceOrbHideNow()
	}
}

// voiceOrbStopCapture closes the microphone and lets the music play again.
func voiceOrbStopCapture() {
	if voiceOrb.mic != nil {
		voiceOrb.mic.Close()
		voiceOrb.mic = nil
	}
	voiceOrbRestoreMusic()
}

// voiceOrbCancelRequest stops the request in flight; whatever it still sends
// back is stale.
func voiceOrbCancelRequest() {
	if voiceOrb.cancel != nil {
		voiceOrb.cancel()
		voiceOrb.cancel = nil
	}
	voiceOrb.gen++
}

// voiceOrbHushMusic pauses the music player while the orb listens.
func voiceOrbHushMusic() {
	if st := voiceMusicNow(); st.playing && !st.paused {
		musicTogglePause()
		voiceOrb.musicHushed = true
	}
}

// voiceOrbRestoreMusic resumes what voiceOrbHushMusic paused, before the
// command runs — so "pause" or "next song" acts on music that is playing.
func voiceOrbRestoreMusic() {
	if !voiceOrb.musicHushed {
		return
	}
	voiceOrb.musicHushed = false
	if st := voiceMusicNow(); st.playing && st.paused {
		musicTogglePause()
	}
}

// ── Pure helpers ─────────────────────────────────────────────────────────

// orbVAD follows the clip's level and decides when speech starts and ends.
// The threshold adapts to the room and to the microphone's gain, which on a
// handheld is unknown: the floor is the quietest level heard lately (it drops
// at once and creeps up about 1% a chunk, so a pause between words pulls it
// down and speech hardly lifts it, while a fan that keeps running is learnt),
// and speech is a level three times the floor — never below orbSpeechMin, so
// breath in a silent room is not speech, and never above twice the Voice
// command page's fixed level, so a noisy room can still be talked over.
type orbVAD struct {
	floor      float64
	started    bool
	speaking   bool
	quietSince uint64
}

const (
	orbFloorMin    = 0.0005
	orbSpeechMin   = 0.008
	orbFloorCreep  = 1.01
	orbSpeechFloor = 3.0 // speech is this many times the floor
	orbQuietRatio  = 0.7 // and a pause is under this share of the threshold
)

// threshold is the level that counts as speech now.
func (v *orbVAD) threshold() float64 {
	t := v.floor * orbSpeechFloor
	if t < orbSpeechMin {
		t = orbSpeechMin
	}
	if t > 2*voiceSpeechLevel {
		t = 2 * voiceSpeechLevel
	}
	return t
}

// feed takes one chunk's RMS level, heard at now (ms).
func (v *orbVAD) feed(level float64, now uint64) {
	switch {
	case !v.started:
		v.started = true
		v.floor = level
	case level < v.floor:
		v.floor = level
	default:
		v.floor *= orbFloorCreep
	}
	if v.floor < orbFloorMin {
		v.floor = orbFloorMin
	}
	th := v.threshold()
	switch {
	case level >= th:
		v.speaking, v.quietSince = true, 0
	case level < th*orbQuietRatio && v.speaking && v.quietSince == 0:
		v.quietSince = now
	}
}

// orbReadMs is how long a sentence stays up: about 1.5 s to notice it plus a
// little per character, longer when it offers a fix or is Juka's. Pure.
func orbReadMs(text string, offer, ai bool) uint64 {
	ms := 1500 + 55*utf8.RuneCountInString(text)
	lo, hi := orbAnswerMinMs, orbAnswerMaxMs
	if offer {
		lo = orbOfferMinMs
	}
	if ai {
		hi = orbAIAnswerMaxMs
	}
	if ms < lo {
		ms = lo
	}
	if ms > hi {
		ms = hi
	}
	return uint64(ms)
}

// orbQuote puts what was heard in quotes ("" stays empty). Pure.
func orbQuote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "“" + s + "”"
}

// orbKeyName names a control in this frame's vocabulary: the voice button is
// Menu on the pad and F4 on a keyboard.
func orbKeyName(button string) string {
	if button == "Menu" {
		if hintsUseKeyboard() || !P().IsHandheld() {
			return "F4"
		}
		return "Menu"
	}
	return hintKeyFor(button, "")
}

// orbOfferLabel is the fix button's label.
func orbOfferLabel(o orbOffer) string {
	switch o {
	case orbOfferGroqKey:
		return "Add Groq key"
	case orbOfferPhone:
		return "Use my phone"
	case orbOfferSearch:
		return "Search for it"
	}
	return ""
}

// orbHintLine is the bubble's last line: the controls of this phase.
func orbHintLine() string {
	b := orbKeyName("B")
	switch voiceOrb.phase {
	case orbListening:
		if voiceOrb.held && voiceOrb.pressSrc != orbFromClick && orbNow()-voiceOrb.pressAt >= orbHoldMs {
			return "Let go of " + orbKeyName("Menu") + " to send · " + b + " cancel"
		}
		return orbKeyName("Menu") + " done · " + b + " cancel"
	case orbThinking:
		return b + " cancel"
	case orbConfirm:
		return orbKeyName("A") + " now · " + b + " cancel"
	case orbChoices:
		return hintKeyFor("D-Pad", "Choose") + " choose · " + orbKeyName("A") + " open · " + b + " cancel"
	}
	if voiceOrb.offer != orbOfferNone {
		return b + " close"
	}
	return orbKeyName("Menu") + " ask again · " + b + " close"
}

// orbWrap keeps one wrapped text while its font, text and width stay.
type orbWrap struct {
	font  *ttf.Font
	text  string
	width int32
	max   int
	lines []string
}

func (w *orbWrap) get(font *ttf.Font, text string, width int32, max int) []string {
	if w.lines != nil && w.font == font && w.text == text && w.width == width && w.max == max {
		return w.lines
	}
	w.font, w.text, w.width, w.max = font, text, width, max
	w.lines = wrapTextLines(font, text, width, max)
	if w.lines == nil {
		w.lines = []string{}
	}
	return w.lines
}

// ── Frame ────────────────────────────────────────────────────────────────

// voiceOrbTick runs once a frame while the orb is up: it records, ends the
// confirm pause and the answer, and keeps the frame rate up while it moves.
func voiceOrbTick(config *Config) {
	if voiceOrb.phase == orbHidden {
		return
	}
	now := orbNow()
	if voiceOrb.fadeAt != 0 {
		if now-voiceOrb.fadeAt >= orbFadeMs {
			voiceOrbHideNow()
		}
		return
	}
	if alarmFiringActive() || gameScreenSuspended() {
		voiceOrbHideNow()
		return
	}
	switch voiceOrb.phase {
	case orbListening:
		voiceOrbListenTick(config, now)
	case orbThinking:
		if voiceOrb.ai {
			voiceOrbTakeAIText()
		}
	case orbConfirm:
		if now >= voiceOrb.confirmAt {
			voiceOrbRunPending(config)
		}
	case orbAnswer:
		if voiceOrb.hideAt != 0 && now >= voiceOrb.hideAt {
			voiceOrbDismiss()
		}
	}
	voiceOrb.shownLevel = voiceOrb.shownLevel*0.7 + voiceOrb.level*0.3
	if voiceOrb.phase != orbHidden && frameRateCtrl != nil {
		frameRateCtrl.NotifyInput()
	}
}

// voiceOrbVisibility is how far the orb is in (0..1): the pop-in eases out,
// the fade-out is linear.
func voiceOrbVisibility(now uint64) float64 {
	vis := 1.0
	if d := now - voiceOrb.shownAt; d < orbAppearMs {
		x := 1 - float64(d)/orbAppearMs
		vis = 1 - x*x*x
	}
	if voiceOrb.fadeAt != 0 {
		d := now - voiceOrb.fadeAt
		if d >= orbFadeMs {
			return 0
		}
		if f := 1 - float64(d)/orbFadeMs; f < vis {
			vis = f
		}
	}
	return vis
}

// orbGeom is one frame's layout.
type orbGeom struct {
	cx, cy, r  int32
	bubble     sdl.Rect
	titleLines []string
	subLines   []string
	rows       [orbMaxChoiceRows]sdl.Rect
	nRows      int
	offer      sdl.Rect
	hint       string
	hintY      int32
}

// voiceOrbLayout places the orb in the bottom-right corner, above the footer,
// and its bubble to its left, growing upward with what it says.
func voiceOrbLayout(config *Config, fonts tbxFonts) orbGeom {
	var g orbGeom
	scale := float64(screenHeight) / 720
	if scale <= 0 {
		scale = 1
	}
	g.r = int32(math.Round(orbRadius * scale))
	if g.r < 22 {
		g.r = 22
	}
	footer := bottomBarHeight(tbxCurrentScene(config))
	g.cx = screenWidth - orbMargin - g.r - 6
	g.cy = screenHeight - footer - orbMargin - g.r
	if g.cy < g.r+orbMargin {
		g.cy = g.r + orbMargin
	}
	bw := screenWidth * 44 / 100
	if bw > orbBubbleMaxW {
		bw = orbBubbleMaxW
	}
	textW := bw - 2*orbPad
	maxTitle := 2
	if (voiceOrb.phase == orbAnswer || voiceOrb.phase == orbThinking) && utf8.RuneCountInString(voiceOrb.title) > 60 {
		maxTitle = 8
	}
	g.titleLines = voiceOrb.wrapTitle.get(fonts.body, voiceOrb.title, textW, maxTitle)
	if voiceOrb.sub != "" {
		g.subLines = voiceOrb.wrapSub.get(fonts.small, voiceOrb.sub, textW, 2)
	}
	g.hint = orbHintLine()
	h := orbPad + int32(len(g.titleLines))*fonts.bodyH
	if len(g.subLines) > 0 {
		h += 4 + int32(len(g.subLines))*fonts.smallH
	}
	rowH := fonts.bodyH + 16
	if voiceOrb.phase == orbChoices {
		g.nRows = len(voiceOrb.choices)
		if g.nRows > orbMaxChoiceRows {
			g.nRows = orbMaxChoiceRows
		}
		if g.nRows > 0 {
			h += 10 + int32(g.nRows)*(rowH+6) - 6
		}
	}
	btnH := fonts.smallH + 18
	offer := voiceOrb.phase == orbAnswer && voiceOrb.offer != orbOfferNone
	if offer {
		h += 12 + btnH
	}
	h += 10 + fonts.tinyH + orbPad
	b := sdl.Rect{X: g.cx - g.r - 14 - bw, Y: g.cy + g.r - h, W: bw, H: h}
	if top := int32(56); b.Y < top {
		b.Y = top
	}
	if b.X < 8 {
		b.X = 8
	}
	g.bubble = b
	// The rows and the button below the text, in the same order as drawn.
	y := b.Y + orbPad + int32(len(g.titleLines))*fonts.bodyH
	if len(g.subLines) > 0 {
		y += 4 + int32(len(g.subLines))*fonts.smallH
	}
	if g.nRows > 0 {
		y += 10
		for i := 0; i < g.nRows; i++ {
			g.rows[i] = sdl.Rect{X: b.X + orbPad - 4, Y: y, W: b.W - 2*orbPad + 8, H: rowH}
			y += rowH + 6
		}
		y -= 6
	}
	if offer {
		y += 12
		w := tbxButtonWidth(fonts.small, orbKeyName("A")+"  "+orbOfferLabel(voiceOrb.offer), 150)
		if w > b.W-2*orbPad {
			w = b.W - 2*orbPad
		}
		g.offer = sdl.Rect{X: b.X + orbPad, Y: y, W: w, H: btnH}
		y += btnH
	}
	g.hintY = y + 10
	return g
}

// voiceOrbRender draws the orb: its tick, then the bubble and the orb over
// everything but the virtual cursor. main.go calls it once a frame, after the
// screenshot capture, so a "take a screenshot" it ran shows the screen alone.
func voiceOrbRender(renderer *sdl.Renderer, config *Config) {
	if voiceOrb.phase == orbHidden || renderer == nil {
		return
	}
	voiceOrbTick(config)
	if voiceOrb.phase == orbHidden {
		return
	}
	now := orbNow()
	vis := voiceOrbVisibility(now)
	if vis <= 0 {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	g := voiceOrbLayout(config, fonts)
	voiceOrb.orbRect = sdl.Rect{X: g.cx - g.r - 6, Y: g.cy - g.r - 6, W: 2*g.r + 12, H: 2*g.r + 12}
	voiceOrb.bubbleRect = g.bubble
	voiceOrb.rowRects = g.rows
	voiceOrb.offerRect = g.offer
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	voiceOrbDrawBubble(renderer, config, fonts, g, vis)
	voiceOrbDrawOrb(renderer, g, vis, now)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
}

// orbTextAlpha steps the text's alpha in three stops while the bubble fades,
// so the text cache keeps three textures per line, not one a frame.
func orbTextAlpha(vis float64) uint8 {
	switch {
	case vis >= 0.85:
		return 255
	case vis >= 0.5:
		return 170
	}
	return 85
}

// orbToneStrip is the colour of the bubble's left edge for a tone.
func orbToneStrip(t tbxTone) (sdl.Color, bool) {
	switch t {
	case tbxToneWarn, tbxToneBad, tbxToneGood:
		return tbxToneColor(t), true
	case tbxToneAccent:
		return glassAccent(), true
	}
	return sdl.Color{}, false
}

// voiceOrbDrawBubble draws the glass card left of the orb: what it says, the
// line under it, the choices or the fix, and the controls.
func voiceOrbDrawBubble(renderer *sdl.Renderer, config *Config, fonts tbxFonts, g orbGeom, vis float64) {
	b := g.bubble
	if b.W <= 0 || b.H <= 0 {
		return
	}
	slide := int32((1 - vis) * 18)
	b.X += slide
	sheet := glassSheetFill()
	sheet.A = uint8(242 * vis)
	fillRoundedRect(renderer, b.X, b.Y, b.W, b.H, orbBubbleRadius, sheet)
	edge := glassSheetEdge()
	edge.A = uint8(float64(edge.A) * vis)
	strokeRoundedRect(renderer, b.X, b.Y, b.W, b.H, orbBubbleRadius, 1, edge)
	if strip, ok := orbToneStrip(voiceOrb.tone); ok && b.H > 24 {
		strip.A = uint8(255 * vis)
		fillRoundedRect(renderer, b.X, b.Y+12, 4, b.H-24, 2, strip)
	}
	if vis < 0.2 {
		return
	}
	ta := orbTextAlpha(vis)
	x := b.X + orbPad
	y := b.Y + orbPad
	for _, line := range g.titleLines {
		drawText(renderer, fonts.body, line, x, y, WithAlpha(ColorTextPrimary(), ta), textAlignLeft)
		y += fonts.bodyH
	}
	if len(g.subLines) > 0 {
		y += 4
		for _, line := range g.subLines {
			drawText(renderer, fonts.small, line, x, y, WithAlpha(ColorTextSecondary(), ta), textAlignLeft)
			y += fonts.smallH
		}
	}
	for i := 0; i < g.nRows && i < len(voiceOrb.choices); i++ {
		r := g.rows[i]
		r.X += slide
		focused := i == voiceOrb.cursor
		UIListRow(renderer, r, 10, focused, false)
		m := voiceOrb.choices[i]
		textY := r.Y + (r.H-fonts.bodyH)/2
		where := ""
		whereW := int32(0)
		if w := strings.TrimSpace(m.Target.Where); w != "" {
			where = ellipsize(fonts.small, w, r.W/3)
			whereW, _ = measureText(fonts.small, where)
		}
		titleW := r.W - 24 - whereW
		if whereW > 0 {
			titleW -= 12
		}
		col := ColorTextPrimary()
		if m.Target.Blocked != "" {
			col = ColorTextTertiary()
		}
		drawText(renderer, fonts.body, ellipsize(fonts.body, m.Target.Title, titleW), r.X+12, textY, WithAlpha(col, ta), textAlignLeft)
		if where != "" {
			drawText(renderer, fonts.small, where, r.X+r.W-12-whereW, r.Y+(r.H-fonts.smallH)/2,
				WithAlpha(ColorTextTertiary(), ta), textAlignLeft)
		}
	}
	if g.offer.W > 0 {
		r := g.offer
		r.X += slide
		tbxDrawButton(renderer, config, fonts.small, r, orbKeyName("A")+"  "+orbOfferLabel(voiceOrb.offer), true, true, false)
	}
	if g.hint != "" {
		hint := ellipsize(fonts.tiny, g.hint, b.W-2*orbPad)
		drawText(renderer, fonts.tiny, hint, x, g.hintY, WithAlpha(ColorTextTertiary(), ta), textAlignLeft)
	}
}

// orbLights are the orb's colours: magenta, blue, cyan and violet, the lights
// of Siri's sphere.
var orbLights = [4]sdl.Color{
	{R: 255, G: 64, B: 170, A: 255},
	{R: 64, G: 118, B: 255, A: 255},
	{R: 0, G: 210, B: 255, A: 255},
	{R: 156, G: 96, B: 255, A: 255},
}

// orbWarnLights tint the orb while it reports a problem.
var orbWarnLights = [4]sdl.Color{
	{R: 255, G: 168, B: 48, A: 255},
	{R: 255, G: 112, B: 64, A: 255},
	{R: 255, G: 206, B: 96, A: 255},
	{R: 255, G: 136, B: 24, A: 255},
}

// orbSquare is the square of side d centred on (x, y).
func orbSquare(x, y, d float64) sdl.Rect {
	s := int32(math.Round(d))
	if s < 1 {
		s = 1
	}
	return sdl.Rect{X: int32(math.Round(x)) - s/2, Y: int32(math.Round(y)) - s/2, W: s, H: s}
}

// voiceOrbEnergy is how lively the lights are (0..1) and how fast they turn:
// the voice drives them while the orb listens; thinking spins them.
func voiceOrbEnergy(t float64) (float64, float64) {
	switch voiceOrb.phase {
	case orbListening:
		e := math.Min(1, voiceOrb.shownLevel*16)
		return 0.12 + 0.88*e, 1.2 + 1.8*e
	case orbThinking:
		return 0.35 + 0.15*math.Sin(t*5), 3.4
	case orbConfirm:
		return 0.55, 2.0
	}
	return 0.18, 0.7
}

// voiceOrbDrawOrb draws the sphere: a soft halo that swells with the voice, a
// dark glass ball, coloured lights circling inside it (added, so where they
// cross they burn white), a core that brightens as you speak, the glass rim,
// sparks circling it while it thinks and the countdown before a command runs.
func voiceOrbDrawOrb(renderer *sdl.Renderer, g orbGeom, vis float64, now uint64) {
	t := float64(now) / 1000
	lights := orbLights
	if voiceOrb.phase == orbAnswer && (voiceOrb.tone == tbxToneWarn || voiceOrb.tone == tbxToneBad) {
		lights = orbWarnLights
	}
	energy, speed := voiceOrbEnergy(t)
	r := float64(g.r)
	cx, cy := float64(g.cx), float64(g.cy)
	grow := 0.72 + 0.28*vis

	halo := glassMix(lights[0], lights[1], 0.5+0.5*math.Sin(t*0.8))
	homeGlowBlob(renderer, orbSquare(cx, cy, 2*r*(1.4+0.6*energy)*grow), halo, uint8((60+100*energy)*vis))

	fillCircle(renderer, g.cx, g.cy, g.r, sdl.Color{R: 10, G: 12, B: 26, A: uint8(232 * vis)})

	if glow := ambientGlowFor(renderer); glow != nil {
		_ = glow.SetBlendMode(sdl.BLENDMODE_ADD)
		for i, c := range lights {
			ph := float64(i) * math.Pi / 2
			ang := t*speed*(0.85+0.22*float64(i)) + ph
			orbit := r * (0.2 + 0.1*math.Sin(t*0.9+ph) + 0.12*energy)
			x := cx + math.Cos(ang)*orbit
			y := cy + math.Sin(ang*1.17)*orbit
			d := r * (1.3 + 0.5*energy + 0.12*math.Sin(t*2.1+ph)) * grow
			_ = glow.SetColorMod(c.R, c.G, c.B)
			_ = glow.SetAlphaMod(uint8((150 + 70*energy) * vis))
			rect := orbSquare(x, y, d)
			_ = renderer.Copy(glow, nil, &rect)
		}
		_ = glow.SetColorMod(255, 255, 255)
		_ = glow.SetAlphaMod(uint8((45 + 150*energy) * vis))
		core := orbSquare(cx, cy, r*(0.5+0.6*energy)*grow)
		_ = renderer.Copy(glow, nil, &core)
		if voiceOrb.phase == orbThinking {
			for i := 0; i < 3; i++ {
				ang := t*4.2 + float64(i)*2*math.Pi/3
				c := lights[i]
				_ = glow.SetColorMod(c.R, c.G, c.B)
				_ = glow.SetAlphaMod(uint8(230 * vis))
				spark := orbSquare(cx+math.Cos(ang)*r*0.98, cy+math.Sin(ang)*r*0.98, r*0.55)
				_ = renderer.Copy(glow, nil, &spark)
			}
		}
		_ = glow.SetBlendMode(sdl.BLENDMODE_BLEND)
		_ = glow.SetColorMod(255, 255, 255)
		_ = glow.SetAlphaMod(255)
	}

	strokeCircle(renderer, g.cx, g.cy, g.r, sdl.Color{R: 255, G: 255, B: 255, A: uint8(64 * vis)})
	homeGlowBlob(renderer, orbSquare(cx-r*0.32, cy-r*0.38, r*0.62), sdl.Color{R: 255, G: 255, B: 255, A: 255}, uint8(46*vis))

	if voiceOrb.phase == orbConfirm {
		left := float64(int64(voiceOrb.confirmAt)-int64(now)) / float64(voiceConfirmMs)
		if left < 0 {
			left = 0
		}
		const dots = 28
		lit := int(math.Ceil(left * dots))
		ring := r + 8
		dot := WithAlpha(glassAccent(), uint8(230*vis))
		for i := 0; i < lit && i < dots; i++ {
			ang := -math.Pi/2 + float64(i)/dots*2*math.Pi
			fillCircle(renderer, int32(math.Round(cx+math.Cos(ang)*ring)), int32(math.Round(cy+math.Sin(ang)*ring)), 2, dot)
		}
	}
}
