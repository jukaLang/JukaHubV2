package main

// tts_api.go — Juka's spoken answers, as the rest of JukaHub calls them. The
// engine behind these three calls lives in the tts*.go files; callers (the
// voice orb, the Voice command page, the game assistant, Chat) only say what
// to speak and when to be quiet, and never wait for the audio.
//
// One line is spoken at a time. Every jukaSpeak and every jukaSpeakStop takes
// a new generation number: the session speaking an older one stops at its next
// slice of audio (a few milliseconds), and what it had already queued on the
// speaker is dropped at once (ttsSilence). jukaSpeak itself only records the
// line and posts the rest to the main loop (tbxPost), so it is safe from any
// goroutine — Chat's answer arrives on one — while the settings, the Groq key
// and the voice are read on the main thread, as config must be. A line posted
// while the main loop was parked behind a game, and only reached long after,
// is dropped rather than spoken out of the blue (ttsStaleAfter, measured on
// the monotonic clock: the handheld's wall clock jumps when NTP arrives).
//
// What Groq's voice refused is remembered for the key it refused (ttsGate): a
// model whose terms are not accepted yet, a key Groq rejects, and the free
// allowance being used up are each met once, not on every answer.

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ttsKind is which answers a line belongs to: Settings → Voice & AI lets the
// user hear the voice assistant only, or Chat's replies as well.
type ttsKind int

const (
	ttsKindVoice ttsKind = iota // the voice orb, the Voice command page, the game assistant
	ttsKindChat                 // Juka's replies in Chat
)

// ttsOrigin is where a line came from, which decides what else ends it besides
// a stop: leaving Chat's AI tab ends a reply read there, leaving the Voice
// command page ends its answer.
type ttsOrigin int

const (
	ttsFromOther ttsOrigin = iota // a jukaSpeak caller
	ttsFromOrb                    // the voice orb
	ttsFromPage                   // the Voice command page
	ttsFromChat                   // Chat's AI tab
	ttsFromTest                   // Settings → Voice & AI → Hear Juka
	ttsFromGame                   // the game assistant's answer over a paused game
)

const (
	// ttsStaleAfter drops a line the main loop reached this long after it was
	// asked for (it was parked behind a game).
	ttsStaleAfter = 4 * time.Second
	// ttsOrbLingerMs is how long an answer stays on the orb after Juka has
	// said it.
	ttsOrbLingerMs = 1500
)

// ttsCtl is who speaks now. Any goroutine, under mu.
var ttsCtl struct {
	mu      sync.Mutex
	gen     uint64             // the current line; every speak and stop bumps it
	cancel  context.CancelFunc // the running session's, while it is the current line
	pending bool               // the current line is posted and not begun
	running uint64             // the line whose session goroutine is live (0: none)
	kind    ttsKind
	origin  ttsOrigin
}

// The state as atomics, for the feeder, the media duck and the per-frame
// checks, none of which may wait on ttsCtl.mu.
var (
	ttsGenNow   atomic.Uint64 // ttsCtl.gen
	ttsBusy     atomic.Bool   // the current line is posted or speaking: jukaSpeaking
	ttsSounding atomic.Bool   // the current line's session runs (fetching or playing)
	ttsVoiceNow atomic.Bool   // ttsBusy, for a voice-assistant line (the orb waits for it)
	ttsFed      atomic.Bool   // a session has audio in the UI device's queue
)

// jukaSpeak speaks text in Juka's voice when the user has turned the voice on
// for answers of this kind; otherwise it does nothing. It returns at once
// (main thread safe; from another goroutine too) and replaces whatever Juka
// was saying.
func jukaSpeak(kind ttsKind, text string) {
	ttsSpeakFrom(ttsFromOther, kind, text)
}

// jukaSpeakStop silences Juka at once (a new question, the orb going away, a
// video starting). Any goroutine; nothing happens when Juka is quiet.
func jukaSpeakStop() {
	ttsStopLine(0)
}

// ttsStopLine silences line gen if it is still the current one (0: whatever
// is current). Any goroutine.
func ttsStopLine(gen uint64) {
	ttsCtl.mu.Lock()
	if (!ttsCtl.pending && ttsCtl.running == 0) || (gen != 0 && ttsCtl.gen != gen) {
		ttsCtl.mu.Unlock()
		return
	}
	ttsCtl.gen++
	ttsGenNow.Store(ttsCtl.gen)
	if ttsCtl.cancel != nil {
		ttsCtl.cancel()
		ttsCtl.cancel = nil
	}
	ttsCtl.pending = false
	ttsSyncFlagsLocked()
	ttsCtl.mu.Unlock()
	ttsSilence()
}

// jukaSpeaking reports whether Juka is speaking (or about to) right now.
func jukaSpeaking() bool { return ttsBusy.Load() }

// ttsSpeakFrom is jukaSpeak with the line's origin.
func ttsSpeakFrom(origin ttsOrigin, kind ttsKind, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	ttsStartLine(origin, kind, text, nil)
}

// ttsSpeakSource starts a line whose chunks arrive while it speaks
// (tts_live.go) and returns its number, for ttsStopLine. Any goroutine.
func ttsSpeakSource(origin ttsOrigin, kind ttsKind, src *ttsSource) uint64 {
	return ttsStartLine(origin, kind, "", src)
}

// ttsStartLine makes text — or src, when it is not nil — the current line,
// and returns its number.
func ttsStartLine(origin ttsOrigin, kind ttsKind, text string, src *ttsSource) uint64 {
	ttsCtl.mu.Lock()
	ttsCtl.gen++
	gen := ttsCtl.gen
	ttsGenNow.Store(gen)
	if ttsCtl.cancel != nil {
		ttsCtl.cancel()
		ttsCtl.cancel = nil
	}
	ttsCtl.pending = true
	ttsCtl.kind, ttsCtl.origin = kind, origin
	ttsSyncFlagsLocked()
	ttsCtl.mu.Unlock()
	// What the line before still had queued goes now.
	ttsSilence()
	posted := time.Now()
	tbxPost(func() { ttsBegin(gen, kind, origin, text, src, posted) })
	return gen
}

// ttsSyncFlagsLocked mirrors ttsCtl into the atomics. ttsCtl.mu is held.
func ttsSyncFlagsLocked() {
	live := ttsCtl.running != 0 && ttsCtl.running == ttsCtl.gen
	busy := ttsCtl.pending || live
	ttsBusy.Store(busy)
	ttsSounding.Store(live)
	ttsVoiceNow.Store(busy && ttsCtl.kind == ttsKindVoice)
}

// ttsAbandon drops the posted line gen without speaking it.
func ttsAbandon(gen uint64) {
	ttsCtl.mu.Lock()
	if ttsCtl.gen == gen {
		ttsCtl.pending = false
		ttsSyncFlagsLocked()
	}
	ttsCtl.mu.Unlock()
}

// ttsSessionEnded is a session goroutine's last word.
func ttsSessionEnded(gen uint64) {
	ttsCtl.mu.Lock()
	if ttsCtl.running == gen {
		ttsCtl.running = 0
		if ttsCtl.gen == gen && ttsCtl.cancel != nil {
			ttsCtl.cancel()
			ttsCtl.cancel = nil
		}
		ttsSyncFlagsLocked()
	}
	ttsCtl.mu.Unlock()
}

// ttsCurrentOrigin is the current line's origin and whether one is posted or
// speaking (the Hear Juka row's read-out).
func ttsCurrentOrigin() (ttsOrigin, bool) {
	ttsCtl.mu.Lock()
	defer ttsCtl.mu.Unlock()
	return ttsCtl.origin, ttsBusy.Load()
}

// ttsBegin starts the posted line gen, on the main thread: it reads the
// settings, the key and the voice, cuts the text into chunks and starts the
// session — or drops the line when the voice is off for it, Quiet Mode is on,
// an alarm rings, there is no speaker, or no voice can speak. A live line
// (src not nil) brings its own chunks, still arriving.
func ttsBegin(gen uint64, kind ttsKind, origin ttsOrigin, text string, src *ttsSource, posted time.Time) {
	ttsCtl.mu.Lock()
	current := ttsCtl.gen == gen && ttsCtl.pending
	ttsCtl.mu.Unlock()
	if !current {
		return
	}
	test := origin == ttsFromTest
	cfg := appConfig
	refuse := func(fail ttsFail) {
		ttsAbandon(gen)
		if test {
			ttsTestOutcome(ttsReport{gen: gen, origin: origin, fail: fail})
		}
	}
	switch {
	case !test && time.Since(posted) > ttsStaleAfter:
		refuse(ttsFailNone)
		return
	case origin == ttsFromGame && !gameAssistSpeakEnabled(cfg):
		// An answer over a paused game has its own switch (Read answers
		// aloud in games, on by default), which takes the place of "Juka's
		// voice" there.
		refuse(ttsFailNone)
		return
	case !test && origin != ttsFromGame && !ttsModeAllows(cfg, kind):
		refuse(ttsFailNone)
		return
	case quietModeOn():
		refuse(ttsFailQuiet)
		return
	case alarmFiringActive():
		refuse(ttsFailNone)
		return
	}
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable {
		refuse(ttsFailNoSound)
		return
	}
	if src == nil {
		chunks := ttsPrepare(text)
		if len(chunks) == 0 {
			refuse(ttsFailNone)
			return
		}
		src = ttsFixedSource(chunks)
	}
	s := &ttsSession{gen: gen, kind: kind, origin: origin, src: src, rate: int(audioSampleRate)}
	s.local, s.hasLocal = ttsLocal()
	pre := ttsFailNone
	if voice := ttsVoiceOf(cfg); voice != ttsVoiceDevice {
		s.gender = ttsVoiceGender(voice)
		key := strings.TrimSpace(groqAPIKey())
		switch {
		case key == "":
			pre = ttsFailNoKey
		case !IsOnline():
			pre = ttsFailOffline
		default:
			keyID := ttsKeyID(key)
			if why := ttsGateCheck(keyID); why != ttsFailNone {
				pre = why
			} else {
				s.key, s.keyID, s.voice = key, keyID, voice
			}
		}
	} else if ttsVoiceImplied(cfg) {
		// The offline voice speaks because there is no Groq key: still say
		// once that a key brings Groq's voices (ttsReported).
		pre = ttsFailNoKey
	}
	if s.key == "" && !s.hasLocal {
		ttsAbandon(gen)
		ttsReported(ttsReport{gen: gen, origin: origin, fail: pre})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ttsCtl.mu.Lock()
	if ttsCtl.gen != gen || !ttsCtl.pending {
		ttsCtl.mu.Unlock()
		cancel()
		return
	}
	ttsCtl.pending = false
	ttsCtl.running = gen
	ttsCtl.cancel = cancel
	ttsSyncFlagsLocked()
	ttsCtl.mu.Unlock()
	ttsWatchBegin(gen)
	go ttsRun(ctx, s, pre)
}

// ── What Groq refused ────────────────────────────────────────────────────

// ttsGate remembers Groq's refusals for one key (keyID, a hash: the key itself
// is not kept). Any goroutine, under mu.
var ttsGate struct {
	mu        sync.Mutex
	keyID     uint64
	terms     bool      // the model's terms are not accepted
	keyBad    bool      // Groq rejected the key
	restUntil time.Time // the allowance is used up until then (monotonic)
	daily     bool      // that rest is the day's allowance
	toldTerms bool      // the one-time notices were shown
	toldRest  bool
}

// ttsKeyID is a key's fingerprint.
func ttsKeyID(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// ttsGateKeyLocked starts afresh for another key. ttsGate.mu is held.
func ttsGateKeyLocked(keyID uint64) {
	if ttsGate.keyID == keyID {
		return
	}
	ttsGate.keyID = keyID
	ttsGate.terms, ttsGate.keyBad, ttsGate.daily = false, false, false
	ttsGate.restUntil = time.Time{}
	ttsGate.toldTerms, ttsGate.toldRest = false, false
}

// ttsGateCheck is the refusal that still stands for keyID, or ttsFailNone.
func ttsGateCheck(keyID uint64) ttsFail {
	ttsGate.mu.Lock()
	defer ttsGate.mu.Unlock()
	ttsGateKeyLocked(keyID)
	switch {
	case ttsGate.terms:
		return ttsFailTerms
	case ttsGate.keyBad:
		return ttsFailKey
	case time.Now().Before(ttsGate.restUntil):
		return ttsFailLimit
	}
	return ttsFailNone
}

// ttsGateNote remembers a refusal of keyID's request.
func ttsGateNote(keyID uint64, fail ttsFail, wait time.Duration, daily bool) {
	ttsGate.mu.Lock()
	defer ttsGate.mu.Unlock()
	ttsGateKeyLocked(keyID)
	switch fail {
	case ttsFailTerms:
		ttsGate.terms = true
	case ttsFailKey:
		ttsGate.keyBad = true
	case ttsFailLimit:
		now := time.Now()
		if !now.Before(ttsGate.restUntil) {
			// A new rest, not the one already told about.
			ttsGate.toldRest = false
		}
		ttsGate.restUntil = now.Add(ttsRestFor(wait, daily))
		ttsGate.daily = daily
	}
}

// ttsGateClear forgets every refusal: "Hear Juka" asks Groq again.
func ttsGateClear() {
	ttsGate.mu.Lock()
	defer ttsGate.mu.Unlock()
	ttsGate.terms, ttsGate.keyBad, ttsGate.daily = false, false, false
	ttsGate.restUntil = time.Time{}
	ttsGate.toldTerms, ttsGate.toldRest = false, false
}

// ttsGateTellOnce reports whether the notice for fail is still to be shown for
// keyID, and marks it shown.
func ttsGateTellOnce(keyID uint64, fail ttsFail) bool {
	ttsGate.mu.Lock()
	defer ttsGate.mu.Unlock()
	if ttsGate.keyID != keyID {
		return false
	}
	switch fail {
	case ttsFailTerms:
		if ttsGate.toldTerms {
			return false
		}
		ttsGate.toldTerms = true
		return true
	case ttsFailLimit:
		if ttsGate.toldRest {
			return false
		}
		ttsGate.toldRest = true
		return true
	}
	return false
}

// ttsGateShown is what Settings says about Groq's voice: whether the terms
// wait for an OK, and whether the voice rests (and for the day).
func ttsGateShown() (terms, resting, daily bool) {
	ttsGate.mu.Lock()
	defer ttsGate.mu.Unlock()
	resting = time.Now().Before(ttsGate.restUntil)
	return ttsGate.terms, resting, resting && ttsGate.daily
}

// ── Hooks for the callers ────────────────────────────────────────────────

// ttsOrbHold is voiceOrbTick's hook for an answer on the orb: while Juka is
// saying a voice-assistant line the bubble stays, and it goes ttsOrbLingerMs
// after the voice ends, or when its reading time is up if that is later. A
// hideAt of 0 (the answer stays) stays 0.
func ttsOrbHold(hideAt, now uint64) uint64 {
	if hideAt == 0 || !ttsVoiceNow.Load() {
		return hideAt
	}
	if until := now + ttsOrbLingerMs; until > hideAt {
		return until
	}
	return hideAt
}

// ttsSpeakOutcome reads the result of a voice command aloud. Actions are
// spoken after they run, so hiding the orb or leaving the page cannot cut off
// the first word.
func ttsSpeakOutcome(origin ttsOrigin, out voiceOutcome) {
	if ttsOutcomeSpoken(out) {
		ttsSpeakFrom(origin, ttsKindVoice, out.Say)
	}
}

// ttsSpeakPageOutcome is the Voice command page's hook: a command said on the
// phone is answered on the phone, so only the handheld's own are spoken.
func ttsSpeakPageOutcome(out voiceOutcome, src voiceSource) {
	if src != voiceFromPhone {
		ttsSpeakOutcome(ttsFromPage, out)
	}
}

// ttsOutcomeSpoken includes answers, completed commands, warnings and choice
// prompts. Pending actions speak after execution; cancellation stays silent.
func ttsOutcomeSpoken(out voiceOutcome) bool {
	return out.Act == nil && !out.Cancel && strings.TrimSpace(out.Say) != ""
}

// ttsSpeakAfterAction makes navigation and other pending commands talk back
// only once they have run. The orb and Voice page may already be gone then, so
// the line uses the scene-independent origin. The phone has its own answer.
func ttsSpeakAfterAction(out voiceOutcome, src voiceSource) voiceOutcome {
	if out.Act == nil || src == voiceFromPhone || strings.TrimSpace(out.Say) == "" {
		return out
	}
	act, say := out.Act, out.Say
	out.Act = func(c *Config) {
		act(c)
		ttsSpeakFrom(ttsFromOther, ttsKindVoice, say)
	}
	return out
}

// ttsSpeakChatReply is chatRunAI's hook, on its request goroutine: Juka's
// reply is read out when it lands in the AI tab while that tab is on screen
// and scrolled to its end (chatLiveTab), and Settings reads Chat's replies.
// Both are checked on the main thread first — a reply that will not be read
// must not cut off what the orb is saying.
func ttsSpeakChatReply(reply string) {
	tbxPost(func() {
		if chatLiveTab.Load() == int32(chatTabAI) && ttsModeAllows(appConfig, ttsKindChat) {
			ttsSpeakFrom(ttsFromChat, ttsKindChat, reply)
		}
	})
}
