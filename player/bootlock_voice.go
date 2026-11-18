package main

// bootlock_voice.go — Voice unlock: a spoken phrase for the startup lock
// (bootlock.go).
//
// The owner asked for "voice authentication for password ... maybe just
// listening for keyword", and for it to come with the code because "voice
// authentication can be glitchy". So the phrase never stands alone: it is set
// next to a button code or a password, in one of two ways (Settings → Privacy
// & data → Voice unlock):
//
//   - "Code or voice" (either): the lock screen listens while it waits; the
//     phrase or the code opens it. The code is always there when the
//     microphone or the recognizer lets the phrase down;
//   - "Code and voice" (both): the code first, then the phrase. A two-step
//     lock for someone who fears a code watched over the shoulder.
//
// It is a phrase, not a voice print: JukaHub recognizes words, not who says
// them (the Settings row says so). Anyone who hears it can say it.
//
// The phrase is set by saying it twice, in Settings, after the current code.
// What the recognizer heard is folded (voiceNormalize: lower case, no
// punctuation, the wake word and "please" gone; digits as words) and the two
// must fold the same — which proves the recognizer hears it the same way
// twice, the best guess that it will again. Stored: never the words.
// startupLockVoice holds "jhvoice1$<engine>$<words>$<iterations>$<salt>$<key>",
// PBKDF2-HMAC-SHA256 over "voice:<folded phrase>". A heard sentence is checked
// window by window (every run of <words> words in it, up to
// bootLockVoiceMaxWindows), so "um, open sesame" opens "open sesame".
//
// Engines. The words must come from the recognizer that heard them at setup —
// the offline one (Vosk, then Whisper: sttLocalTranscribe) or Groq's Whisper —
// so the engine is part of the stored value. Offline is preferred whenever it
// is installed (the handheld): nothing leaves the device, and only then does
// the lock screen listen by itself (hands-free, for bootLockVoiceAutoMs after it
// comes up, again after R2). With Groq the lock listens only when asked (R2):
// a lock screen must not send the room's talk to a server on its own.
//
// Misses. A phrase that does not match counts in startupLockVoiceMisses
// (saved before the check, as the code's tries are). After
// bootLockVoiceMaxMisses in a row the phrase rests: "either" leaves only the
// code (a right code clears the count); "both" goes back to the code step and
// counts one wrong code, so the code's growing waits pace a guesser too. A clip
// with no words in it is not a miss.

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// bootLockVoiceKey holds the hashed phrase; bootLockVoiceModeKey says how
	// it combines with the code; bootLockVoiceMissKey counts misses in a row.
	// All three are bookkeeping (cfgdocRuntimeKeys): a change needs the code.
	bootLockVoiceKey     = "startupLockVoice"
	bootLockVoiceModeKey = "startupLockVoiceMode"
	bootLockVoiceMissKey = "startupLockVoiceMisses"

	bootLockVoiceOff    = "off"
	bootLockVoiceEither = "either"
	bootLockVoiceBoth   = "both"
	// bootLockVoicePhrase is the setup target "say a new phrase, keep the mode".
	bootLockVoicePhrase = "phrase"

	bootLockVoiceEngineOffline = "offline"
	bootLockVoiceEngineGroq    = "groq"

	bootLockVoiceTag  = "jhvoice1"
	bootLockVoiceIter = 60000

	// A phrase is at least bootLockVoiceMinLetters letters (spaces not
	// counted) and at most bootLockVoiceMaxWords words.
	bootLockVoiceMinLetters = 8
	bootLockVoiceMaxWords   = 8
	// bootLockVoiceMaxWindows bounds the checks one heard sentence costs.
	bootLockVoiceMaxWindows = 12
	bootLockVoiceMaxMisses  = 5

	// bootLockVoiceAutoMs is how long the lock screen listens by itself.
	bootLockVoiceAutoMs = 90000
	// The clip: 300 ms kept from before the voice starts, at most 6 s of it,
	// ended by 900 ms of quiet; less than 250 ms of sound (a click) is dropped.
	bootLockVoicePreBytes  = vkVoiceRate * 2 * 3 / 10
	bootLockVoiceClipBytes = vkVoiceRate * 2 * 6
	bootLockVoiceQuietMs   = 900
	bootLockVoiceSegMaxMs  = 6000
	bootLockVoiceMinSegMs  = 250
	bootLockVoiceWait      = 30 * time.Second

	bootLockForVoiceUnlock bootLockPurpose = 20
	bootLockForVoiceSave   bootLockPurpose = 21

	bootLockStepVoiceNew     bootLockStep = 20
	bootLockStepVoiceConfirm bootLockStep = 21
)

// bootLockHeard is one clip's words, from the transcription goroutine.
type bootLockHeard struct {
	gen  int
	text string
	err  error
}

// bootLockVoice is the voice part of the overlay. Main thread.
var bootLockVoice struct {
	setup  bool   // the open setup is Voice unlock's (Settings)
	target string // setup: off, either, both or phrase
	second bool   // lock screen, "both": the code was right, the phrase is next
	first  string // setup: the phrase as heard the first time (folded)
	engine string // setup: the engine that heard it
	paused bool   // "either": misses used up; the code only now

	mic        *micStream
	on         bool   // the microphone is open and recorded
	manual     bool   // opened by a press (its chime is not speech)
	openedAt   uint64 // ticks
	autoArmed  bool   // this lock-up's hands-free window was given
	autoUntil  uint64 // ticks; hands-free listening ends then (0: one clip)
	seg        bool   // a voice is being recorded
	segAt      uint64
	quietSince uint64
	pre        []byte
	pcm        []byte
	rbuf       []byte
	level      float64

	waiting bool
	gen     int
	cancel  context.CancelFunc
	result  chan bootLockHeard
	pending string // a heard phrase waiting for a code check to end

	lookAt   uint64
	engineOK string // "" or why the stored engine cannot listen now
}

// ── Hashing (pure) ───────────────────────────────────────────────────────

type bootLockVoiceStored struct {
	engine string
	words  int
	iter   int
	salt   []byte
	key    []byte
}

// bootLockVoiceFold is the phrase as it is hashed: voiceNormalize's folding,
// letters and digits only, every digit as its word ("4" and "four" alike).
func bootLockVoiceFold(text string) string {
	digits := [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	var out []string
	for _, w := range strings.Fields(voiceNormalize(text)) {
		var b strings.Builder
		flush := func() {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		}
		for _, r := range w {
			switch {
			case r >= '0' && r <= '9':
				flush()
				out = append(out, digits[r-'0'])
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(unicode.ToLower(r))
			}
		}
		flush()
	}
	return strings.Join(out, " ")
}

// bootLockVoiceProblem says what is wrong with a new phrase, or "".
func bootLockVoiceProblem(fold string) string {
	words := strings.Fields(fold)
	letters := 0
	for _, w := range words {
		letters += len([]rune(w))
	}
	switch {
	case letters < bootLockVoiceMinLetters:
		return "Too short: use at least " + strconv.Itoa(bootLockVoiceMinLetters) + " letters, like two or three words"
	case len(words) > bootLockVoiceMaxWords:
		return "Too long: use " + strconv.Itoa(bootLockVoiceMaxWords) + " words or fewer"
	}
	return ""
}

// bootLockVoiceHash makes the stored form of a folded phrase.
func bootLockVoiceHash(engine, fold string) (string, error) {
	salt := make([]byte, bootLockSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, "voice:"+fold, salt, bootLockVoiceIter, bootLockKeyLen)
	if err != nil {
		return "", err
	}
	return bootLockVoiceTag + "$" + engine + "$" + strconv.Itoa(len(strings.Fields(fold))) + "$" +
		strconv.Itoa(bootLockVoiceIter) + "$" + bootLockB64.EncodeToString(salt) + "$" +
		bootLockB64.EncodeToString(key), nil
}

// bootLockVoiceParse reads a stored phrase; anything else is no phrase.
func bootLockVoiceParse(s string) (bootLockVoiceStored, bool) {
	parts := strings.Split(strings.TrimSpace(s), "$")
	if len(parts) != 6 || parts[0] != bootLockVoiceTag {
		return bootLockVoiceStored{}, false
	}
	st := bootLockVoiceStored{engine: parts[1]}
	if st.engine != bootLockVoiceEngineOffline && st.engine != bootLockVoiceEngineGroq {
		return bootLockVoiceStored{}, false
	}
	var err error
	if st.words, err = strconv.Atoi(parts[2]); err != nil || st.words < 1 || st.words > bootLockVoiceMaxWords {
		return bootLockVoiceStored{}, false
	}
	if st.iter, err = strconv.Atoi(parts[3]); err != nil || st.iter < 1000 || st.iter > 5000000 {
		return bootLockVoiceStored{}, false
	}
	if st.salt, err = bootLockB64.DecodeString(parts[4]); err != nil || len(st.salt) < 8 {
		return bootLockVoiceStored{}, false
	}
	if st.key, err = bootLockB64.DecodeString(parts[5]); err != nil || len(st.key) != bootLockKeyLen {
		return bootLockVoiceStored{}, false
	}
	return st, true
}

// bootLockVoiceWindows is every run of n words in a heard sentence, at most
// bootLockVoiceMaxWindows of them, without repeats.
func bootLockVoiceWindows(fold string, n int) []string {
	words := strings.Fields(fold)
	if n < 1 || len(words) < n {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for i := 0; i+n <= len(words) && len(out) < bootLockVoiceMaxWindows; i++ {
		w := strings.Join(words[i:i+n], " ")
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// bootLockVoiceVerify reports whether one of the windows is the stored
// phrase. Slow on purpose: off the main thread.
func bootLockVoiceVerify(stored string, windows []string) bool {
	st, ok := bootLockVoiceParse(stored)
	if !ok {
		return false
	}
	match := false
	for _, w := range windows {
		key, err := pbkdf2.Key(sha256.New, "voice:"+w, st.salt, st.iter, len(st.key))
		if err == nil && subtle.ConstantTimeCompare(key, st.key) == 1 {
			match = true
		}
	}
	return match
}

// ── Stored values ────────────────────────────────────────────────────────

func bootLockVoiceStoredFrom(m map[string]interface{}) (string, bootLockVoiceStored, bool) {
	s, _ := m[bootLockVoiceKey].(string)
	st, ok := bootLockVoiceParse(s)
	return strings.TrimSpace(s), st, ok
}

// bootLockVoiceModeOf is the voice unlock in force (either or both), or ""
// for none: it needs a lock and a phrase.
func bootLockVoiceModeOf(c *Config) string {
	if c == nil || bootLockKindOf(c) == "" {
		return ""
	}
	if _, _, ok := bootLockVoiceStoredFrom(c.Variables.Custom); !ok {
		return ""
	}
	switch s, _ := c.Variables.Custom[bootLockVoiceModeKey].(string); s {
	case bootLockVoiceBoth:
		return bootLockVoiceBoth
	case bootLockVoiceOff:
		return ""
	}
	return bootLockVoiceEither
}

func bootLockVoiceMissesFrom(m map[string]interface{}) int {
	n := 0
	switch v := m[bootLockVoiceMissKey].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		n, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	return max(0, min(n, 1000))
}

// bootLockVoiceSetMisses saves the miss count when it changes.
func bootLockVoiceSetMisses(c *Config, n int) {
	if c != nil && bootLockVoiceMissesFrom(c.Variables.Custom) != n {
		bootLockWrite(c, bootLockVoiceMissKey, float64(n))
	}
}

// bootLockVoiceForget removes the phrase (the lock went, or Voice unlock was
// turned off). Values, not deletions, as the code's own removal does.
func bootLockVoiceForget(c *Config) {
	if c == nil || c.Variables.Custom == nil {
		return
	}
	if _, ok := c.Variables.Custom[bootLockVoiceKey]; !ok {
		return
	}
	c.Variables.Custom[bootLockVoiceModeKey] = bootLockVoiceOff
	c.Variables.Custom[bootLockVoiceMissKey] = float64(0)
	bootLockWrite(c, bootLockVoiceKey, "")
}

// ── Engines ──────────────────────────────────────────────────────────────

// bootLockVoiceEngineFor is the engine a new phrase is heard with: offline
// when installed, else Groq when the speech setting allows it and a key is
// set; "" when neither.
func bootLockVoiceEngineFor(c *Config) string {
	if sttLocalReady() {
		return bootLockVoiceEngineOffline
	}
	if sttModeOf(c) != sttModeOffline && strings.TrimSpace(groqAPIKey()) != "" {
		return bootLockVoiceEngineGroq
	}
	return ""
}

// bootLockVoiceEngineProblem says why engine cannot listen now, or "".
func bootLockVoiceEngineProblem(engine string) string {
	switch engine {
	case bootLockVoiceEngineOffline:
		if !sttLocalReady() {
			return "Offline speech is not installed: Settings → Voice & AI"
		}
	case bootLockVoiceEngineGroq:
		if strings.TrimSpace(groqAPIKey()) == "" {
			return "Voice unlock needs the Groq key it was set up with"
		}
		if !IsOnline() {
			return "Voice unlock needs the internet (Groq)"
		}
	default:
		return "Voice unlock needs offline speech or a Groq key: Settings → Voice & AI"
	}
	return ""
}

// bootLockVoiceEngineNow is the engine this overlay listens with.
func bootLockVoiceEngineNow(c *Config) string {
	if bootLock.mode == bootLockModeSetup {
		if bootLockVoice.engine != "" {
			return bootLockVoice.engine
		}
		return bootLockVoiceEngineFor(c)
	}
	if c == nil {
		return ""
	}
	_, st, _ := bootLockVoiceStoredFrom(c.Variables.Custom)
	return st.engine
}

// ── Where it is up ───────────────────────────────────────────────────────

// bootLockVoiceStepActive reports a screen that wants the phrase now: the
// setup's two recordings, or the lock's second step.
func bootLockVoiceStepActive() bool {
	switch bootLock.mode {
	case bootLockModeSetup:
		return bootLockVoice.setup && (bootLock.step == bootLockStepVoiceNew || bootLock.step == bootLockStepVoiceConfirm)
	case bootLockModeLocked:
		return bootLockVoice.second
	}
	return false
}

// bootLockVoiceEitherUp reports the lock screen of an "either" lock, where
// the phrase can stand in for the code.
func bootLockVoiceEitherUp(c *Config) bool {
	return bootLock.mode == bootLockModeLocked && !bootLockVoice.second &&
		bootLockVoiceModeOf(c) == bootLockVoiceEither
}

// bootLockVoiceWanted reports whether listening may go on now.
func bootLockVoiceWanted(c *Config) bool {
	if bootLock.power || alarmFiringActive() || powerSleepActive() {
		return false
	}
	if bootLockVoiceStepActive() {
		return true
	}
	return bootLockVoiceEitherUp(c) && !bootLockVoice.paused && bootLockWaitLeft() == 0
}

// ── Setup (Settings) ─────────────────────────────────────────────────────

// bootLockVoiceOpenSetup changes Voice unlock to target (off, either, both, or
// phrase for a new phrase): the current code first, then, when there is no
// phrase yet, the phrase twice.
func bootLockVoiceOpenSetup(c *Config, target string) {
	if c == nil || bootLock.mode != bootLockModeOff {
		return
	}
	st, set := bootLockParse(bootLockSecretFrom(c.Variables.Custom))
	if !set {
		setToast("Set a button code or a password first")
		return
	}
	cur := bootLockVoiceModeOf(c)
	switch {
	case target == bootLockVoiceOff && cur == "":
		setToast("Voice unlock is already off")
		return
	case target == cur:
		return
	}
	_, _, hasPhrase := bootLockVoiceStoredFrom(c.Variables.Custom)
	if target == bootLockVoicePhrase || (target != bootLockVoiceOff && !hasPhrase) {
		if msg := bootLockVoiceEngineProblem(bootLockVoiceEngineFor(c)); msg != "" {
			ShowToast(msg, ToastKindWarning)
			return
		}
	}
	bootLockReset()
	bootLock.mode = bootLockModeSetup
	bootLock.target = st.kind // the lock's kind stays; this setup only adds to it
	bootLock.step, bootLock.kind = bootLockStepCurrent, st.kind
	bootLockImposeWait(c)
	bootLock.wantKeyboard = st.kind == bootLockKindPassword
	bootLockVoice.setup, bootLockVoice.target = true, target
}

// bootLockVoiceAfterCurrent is the setup's next step once the current code
// was right (bootLockFinish).
func bootLockVoiceAfterCurrent(c *Config) {
	target := bootLockVoice.target
	_, _, hasPhrase := bootLockVoiceStoredFrom(c.Variables.Custom)
	switch {
	case target == bootLockVoiceOff:
		bootLockVoiceForget(c)
		logInfo("lock", "startup lock: voice unlock turned off")
		bootLockClose()
		PlayActivateSound()
		ShowToast("Voice unlock is off", ToastKindSuccess)
		return
	case target != bootLockVoicePhrase && hasPhrase:
		// The code was just right: misses from the other mode do not carry.
		c.Variables.Custom[bootLockVoiceMissKey] = float64(0)
		bootLockWrite(c, bootLockVoiceModeKey, target)
		logInfo("lock", "startup lock: voice unlock %s", target)
		bootLockClose()
		PlayActivateSound()
		ShowToast("Voice unlock: "+bootLockVoiceModeLabel(c, target), ToastKindSuccess)
		return
	}
	bootLock.step = bootLockStepVoiceNew
	bootLock.wantKeyboard = false
	bootLockVoice.first, bootLockVoice.engine = "", bootLockVoiceEngineFor(c)
	bootLockSay("", false)
	PlayNavSound()
	bootLockVoiceStart(c, false)
}

// ── The lock's second step ───────────────────────────────────────────────

// bootLockVoiceSecondStep is bootLockFinish's word that the code was right on
// the lock screen. For a "both" lock it asks for the phrase instead of
// unlocking (true). Then neither the misses nor the wrong codes are cleared
// (the phrase being right does that, bootLockFinish): someone who knows only
// the code could otherwise go back and forth for fresh tries at the phrase,
// and the wrong code a used-up phrase counts would never grow a wait. The
// "never unlocked" mark stays until the phrase is right. Otherwise (an
// unlock) the misses go.
func bootLockVoiceSecondStep(c *Config) bool {
	if bootLock.mode != bootLockModeLocked || c == nil {
		return false
	}
	bootLockVoice.paused = false
	if bootLockVoice.second || bootLockVoiceModeOf(c) != bootLockVoiceBoth {
		bootLockVoiceSetMisses(c, 0)
		return false
	}
	bootLock.waitUntil = 0
	bootLockClearEntry()
	bootLock.wantKeyboard = false
	bootLockVoice.second = true
	bootLockVoice.autoArmed = false
	bootLockSay("", false)
	PlayNavSound()
	logInfo("lock", "startup lock: code right, waiting for the phrase")
	if bootLockVoiceEngineNow(c) == bootLockVoiceEngineGroq {
		// Asked for just now: the one clip may go to Groq without a press.
		bootLockVoiceStart(c, false)
	}
	return true
}

// bootLockCodeFree reports a heard phrase being checked on an "either" lock
// with no code waiting yet: the code stays free to type meanwhile (a press
// dropped there made a wrong code, and a counted one), and a code submitted
// waits in bootLock.queued for the check to end.
func bootLockCodeFree() bool {
	return bootLock.checking && bootLock.checkFor == bootLockForVoiceUnlock &&
		bootLock.mode == bootLockModeLocked && !bootLockVoice.second && bootLock.queued == ""
}

// bootLockVoiceBackToCode leaves the second step for the code again.
func bootLockVoiceBackToCode() {
	bootLockVoiceStop()
	bootLockVoice.second = false
	bootLockVoice.autoArmed = false
	bootLockClearEntry()
	bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
}

// ── Per frame ────────────────────────────────────────────────────────────

// bootLockVoiceTick is bootLockTick's voice part.
func bootLockVoiceTick(c *Config) {
	if c == nil || bootLock.mode == bootLockModeOff {
		return
	}
	if bootLockVoice.waiting && bootLockVoice.result != nil {
		select {
		case h := <-bootLockVoice.result:
			bootLockVoiceHeardIt(c, h)
		default:
		}
	}
	if bootLockVoice.pending != "" && !bootLock.checking {
		fold := bootLockVoice.pending
		bootLockVoice.pending = ""
		bootLockVoiceCheck(c, fold)
	}
	now := sdl.GetTicks64()
	if now-bootLockVoice.lookAt >= 2000 || bootLockVoice.lookAt == 0 {
		// File and network reads: not every frame.
		bootLockVoice.lookAt = now
		bootLockVoice.engineOK = ""
		if bootLockVoiceStepActive() || bootLockVoiceEitherUp(c) {
			bootLockVoice.engineOK = bootLockVoiceEngineProblem(bootLockVoiceEngineNow(c))
		}
	}
	if !bootLockVoice.paused && bootLockVoiceEitherUp(c) &&
		bootLockVoiceMissesFrom(c.Variables.Custom) >= bootLockVoiceMaxMisses {
		// The misses were used up before a restart: the pause outlives it, or
		// every restart would be one more try at the phrase.
		bootLockVoice.paused = true
	}
	if !bootLockVoiceWanted(c) {
		if bootLockVoice.on {
			bootLockVoiceStop()
		}
		return
	}
	offline := bootLockVoiceEngineNow(c) == bootLockVoiceEngineOffline
	if !bootLockVoice.autoArmed && bootLock.mode == bootLockModeLocked && offline && bootLockVoice.engineOK == "" {
		// The lock just came up (or the code was right): listen by itself.
		bootLockVoice.autoArmed = true
		bootLockVoice.autoUntil = now + bootLockVoiceAutoMs
		sttServeWarm()
	}
	if !bootLockVoice.on && !bootLockVoice.waiting && bootLockVoice.autoUntil > now && !bootLock.checking {
		bootLockVoiceStart(c, false)
	}
	if bootLockVoice.on {
		bootLockVoiceRecord(c, now)
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput() // the level moves
		}
	}
}

// ── Listening ────────────────────────────────────────────────────────────

// bootLockVoiceStart opens the microphone for the phrase. manual: a press
// asked (its chime is skipped, and offline it reopens the hands-free window).
func bootLockVoiceStart(c *Config, manual bool) {
	if bootLockVoice.on || bootLockVoice.waiting {
		return
	}
	engine := bootLockVoiceEngineNow(c)
	if msg := bootLockVoiceEngineProblem(engine); msg != "" {
		bootLockVoice.autoUntil = 0
		bootLockSay(msg, true)
		if manual {
			PlayErrorSound()
		}
		return
	}
	jukaSpeakStop()
	mic, err := micOpen()
	if err != nil {
		bootLockVoice.autoUntil = 0
		msg := "Couldn't open the microphone"
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			msg = "No microphone found"
		}
		if bootLockVoiceEitherUp(c) {
			msg += " · use your code"
		}
		bootLockSay(msg, true)
		if manual {
			PlayErrorSound()
		}
		return
	}
	vkMic.Store(vkMicPresent)
	now := sdl.GetTicks64()
	v := &bootLockVoice
	v.mic, v.on, v.manual, v.openedAt = mic, true, manual, now
	v.seg, v.segAt, v.quietSince, v.level = false, 0, 0, 0
	v.pre, v.pcm = v.pre[:0], v.pcm[:0]
	if engine == bootLockVoiceEngineOffline {
		sttServeWarm()
		if manual && bootLock.mode == bootLockModeLocked {
			v.autoUntil = now + bootLockVoiceAutoMs
		}
	} else if manual {
		v.autoUntil = 0
	}
	if manual {
		PlayActivateSound()
	}
	if bootLock.warn && bootLockVoiceStepActive() {
		bootLockSay("", false)
	}
}

// bootLockVoiceStop closes the microphone and drops the clip being recorded
// (an answer already on its way still counts).
func bootLockVoiceStop() {
	v := &bootLockVoice
	if v.mic != nil {
		v.mic.Close()
		v.mic = nil
	}
	v.on, v.seg = false, false
	v.pre, v.pcm = v.pre[:0], v.pcm[:0]
	v.level = 0
}

// bootLockVoiceReset drops everything voice: microphone, answer, setup.
// bootLockReset calls it.
func bootLockVoiceReset() {
	bootLockVoiceStop()
	v := &bootLockVoice
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.gen++
	v.waiting, v.result, v.pending = false, nil, ""
	v.setup, v.target, v.second, v.first, v.engine, v.paused = false, "", false, "", "", false
	v.autoArmed, v.autoUntil = false, 0
}

// bootLockVoiceRecord takes what the microphone captured: a voice starts a
// clip (with the moment before it), quiet or the cap ends it.
func bootLockVoiceRecord(c *Config, now uint64) {
	v := &bootLockVoice
	if cap(v.rbuf) < vkVoiceRate*2 {
		v.rbuf = make([]byte, vkVoiceRate*2)
	}
	n := v.mic.Read(v.rbuf[:cap(v.rbuf)])
	if n > 0 && !(v.manual && now-v.openedAt < voiceChimeMs) {
		chunk := v.rbuf[:n&^1]
		level := vkPCMLevel(chunk)
		if level > v.level {
			v.level = level
		} else {
			v.level = v.level*0.82 + level*0.18
		}
		if !v.seg {
			v.pre = append(v.pre, chunk...)
			if over := len(v.pre) - bootLockVoicePreBytes; over > 0 {
				v.pre = append(v.pre[:0], v.pre[over:]...)
			}
			if level >= voiceSpeechLevel {
				v.seg, v.segAt, v.quietSince = true, now, 0
				v.pcm = append(v.pcm[:0], v.pre...)
			}
		} else {
			if room := bootLockVoiceClipBytes - len(v.pcm); room > 0 {
				v.pcm = append(v.pcm, chunk[:min(len(chunk), room)]...)
			}
			switch {
			case level < voiceQuietLevel && v.quietSince == 0:
				v.quietSince = now
			case level >= voiceQuietLevel:
				v.quietSince = 0
			}
		}
	}
	if v.seg {
		if (v.quietSince != 0 && now-v.quietSince >= bootLockVoiceQuietMs) ||
			now-v.segAt >= bootLockVoiceSegMaxMs || len(v.pcm) >= bootLockVoiceClipBytes {
			bootLockVoiceEndClip(c, now)
		}
		return
	}
	switch {
	case v.autoUntil == 0 && now-v.openedAt >= voiceNoSpeechMs:
		bootLockVoiceStop()
		bootLockSay("Didn't hear anything · "+bootLockVoiceAgainHint(), true)
	case v.autoUntil != 0 && now >= v.autoUntil:
		bootLockVoiceStop()
		v.autoUntil = 0
	}
}

// bootLockVoiceAgainHint is the press that listens again here.
func bootLockVoiceAgainHint() string {
	if bootLockVoiceStepActive() {
		return "press A to try again"
	}
	return "press R2 to speak"
}

// bootLockVoiceEndClip closes the microphone and sends the clip to the
// engine; a clip too short to hold a word is dropped (hands-free goes on).
func bootLockVoiceEndClip(c *Config, now uint64) {
	v := &bootLockVoice
	speechMs := now - v.segAt
	if v.quietSince != 0 {
		speechMs = v.quietSince - v.segAt
	}
	if speechMs < bootLockVoiceMinSegMs {
		v.seg, v.quietSince = false, 0
		v.pcm = v.pcm[:0]
		return
	}
	wav := vkEncodeWAV(v.pcm, vkVoiceRate)
	bootLockVoiceStop()
	engine := bootLockVoiceEngineNow(c)
	key := strings.TrimSpace(groqAPIKey())
	v.gen++
	gen := v.gen
	ch := make(chan bootLockHeard, 1)
	ctx, cancel := context.WithTimeout(context.Background(), bootLockVoiceWait)
	v.result, v.cancel, v.waiting = ch, cancel, true
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				ch <- bootLockHeard{gen: gen, err: errors.New("speech recognition stopped")}
			}
		}()
		var text string
		var err error
		if engine == bootLockVoiceEngineGroq {
			text, err = voiceTranscribe(ctx, key, wav, "")
		} else {
			text, err = sttLocalTranscribe(ctx, wav, "en", "")
		}
		ch <- bootLockHeard{gen: gen, text: text, err: err}
	}()
}

// bootLockVoiceHeardIt takes a clip's words. Main thread.
func bootLockVoiceHeardIt(c *Config, h bootLockHeard) {
	v := &bootLockVoice
	if h.gen != v.gen {
		return
	}
	v.waiting, v.result = false, nil
	v.cancel = nil
	if h.err != nil {
		if errors.Is(h.err, context.Canceled) {
			return
		}
		msg, _ := voiceErrorText(h.err)
		logInfo("lock", "startup lock: voice: %v", h.err)
		v.autoUntil = 0 // not again and again by itself
		bootLockSay(msg, true)
		return
	}
	fold := bootLockVoiceFold(h.text)
	if fold == "" {
		// Not a word in it (a cough, the room): not a try.
		if bootLockVoiceStepActive() && v.autoUntil == 0 {
			bootLockSay("Didn't catch that · "+bootLockVoiceAgainHint(), true)
		}
		return
	}
	if bootLock.mode == bootLockModeSetup {
		bootLockVoiceSetupHeard(c, fold)
		return
	}
	bootLockVoiceCheck(c, fold)
}

// bootLockVoiceSetupHeard is a setup recording: the first sets the phrase,
// the second must fold the same.
func bootLockVoiceSetupHeard(c *Config, fold string) {
	v := &bootLockVoice
	switch bootLock.step {
	case bootLockStepVoiceNew:
		if msg := bootLockVoiceProblem(fold); msg != "" {
			bootLockSay("Heard \""+fold+"\" · "+msg, true)
			PlayErrorSound()
			return
		}
		v.first = fold
		bootLock.step = bootLockStepVoiceConfirm
		bootLockSay("Heard \""+fold+"\"", false)
		PlayNavSound()
		bootLockVoiceStart(c, false)
	case bootLockStepVoiceConfirm:
		if subtle.ConstantTimeCompare([]byte(fold), []byte(v.first)) != 1 {
			bootLockSay("Heard \""+fold+"\", not the same · say your phrase again", true)
			v.first = ""
			bootLock.step = bootLockStepVoiceNew
			PlayErrorSound()
			return
		}
		phrase, engine := v.first, v.engine
		v.first = ""
		bootLockRun(bootLockForVoiceSave, func() bootLockResult {
			h, err := bootLockVoiceHash(engine, phrase)
			return bootLockResult{ok: err == nil, hash: h}
		})
	}
}

// bootLockVoiceCheck counts and checks a heard sentence on the lock screen.
func bootLockVoiceCheck(c *Config, fold string) {
	if bootLock.mode != bootLockModeLocked || !(bootLockVoice.second || bootLockVoiceEitherUp(c)) {
		return
	}
	if !bootLockVoice.second && bootLockVoice.paused {
		return // the code only now
	}
	if bootLock.checking {
		bootLockVoice.pending = fold
		return
	}
	stored, st, ok := bootLockVoiceStoredFrom(c.Variables.Custom)
	if !ok {
		return
	}
	windows := bootLockVoiceWindows(fold, st.words)
	if !bootLockVoice.manual && !bootLockVoice.second && len(strings.Fields(fold)) > st.words+2 {
		// Listening by itself on an "either" lock: a long sentence is the
		// room talking (a TV, a conversation), not someone trying the phrase.
		// Neither a miss nor a beep.
		return
	}
	if len(windows) == 0 {
		// Fewer words than the phrase: not it, and not worth a try. Only a
		// press to speak hears about it; hands-free stays quiet.
		if bootLockVoice.manual || bootLockVoice.second {
			bootLockSay("Not your phrase", true)
			PlayErrorSound()
		}
		return
	}
	// Counted before the check, as a code is: power pulled mid-check cannot
	// keep a miss off the count.
	bootLockWrite(c, bootLockVoiceMissKey, float64(bootLockVoiceMissesFrom(c.Variables.Custom)+1))
	bootLockRun(bootLockForVoiceUnlock, func() bootLockResult {
		return bootLockResult{ok: bootLockVoiceVerify(stored, windows)}
	})
}

// bootLockVoiceFinish applies a voice check's or hash's result; false for
// the code's own purposes (bootLockFinish).
func bootLockVoiceFinish(c *Config, r bootLockResult) bool {
	switch r.purpose {
	case bootLockForVoiceUnlock:
		if bootLock.mode != bootLockModeLocked {
			return true
		}
		if r.ok {
			logInfo("lock", "startup lock: phrase heard")
			bootLockVoiceSetMisses(c, 0)
			bootLockFinish(c, bootLockResult{purpose: bootLockForUnlock, ok: true})
			return true
		}
		bootLockVoiceMissed(c)
		return true
	case bootLockForVoiceSave:
		if bootLock.mode != bootLockModeSetup {
			return true
		}
		if !r.ok || r.hash == "" {
			bootLockClose()
			PlayErrorSound()
			ShowToast("Voice unlock: the phrase could not be saved", ToastKindWarning)
			return true
		}
		mode := bootLockVoice.target
		if mode != bootLockVoiceEither && mode != bootLockVoiceBoth {
			if mode = bootLockVoiceModeOf(c); mode == "" {
				mode = bootLockVoiceEither
			}
		}
		c.Variables.Custom[bootLockVoiceModeKey] = mode
		c.Variables.Custom[bootLockVoiceMissKey] = float64(0)
		bootLockWrite(c, bootLockVoiceKey, r.hash)
		logInfo("lock", "startup lock: voice phrase set (%s, %s)", mode, bootLockVoice.engine)
		bootLockClose()
		PlayActivateSound()
		ShowToast("Voice unlock is on: "+bootLockVoiceModeLabel(c, mode), ToastKindSuccess)
		return true
	}
	return false
}

// bootLockVoiceMissed is a heard phrase that did not match (already counted).
func bootLockVoiceMissed(c *Config) {
	misses := bootLockVoiceMissesFrom(c.Variables.Custom)
	logInfo("lock", "startup lock: phrase not recognized (%d in a row)", misses)
	PlayErrorSound()
	if misses < bootLockVoiceMaxMisses {
		left := bootLockVoiceMaxMisses - misses
		msg := "Not your phrase"
		if left <= 3 {
			msg += " · " + strconv.Itoa(left) + " more"
		}
		bootLockSay(msg, true)
		return
	}
	bootLockVoiceStop()
	bootLockVoice.autoUntil = 0
	if bootLockVoice.second {
		// "both": back to the code, which counts as one wrong code so the
		// waits pace this too.
		bootLockVoiceSetMisses(c, 0)
		fails := bootLockFailsFrom(c.Variables.Custom) + 1
		bootLockWrite(c, bootLockFailsKey, float64(fails))
		if d := bootLockWaitFor(fails); d > 0 {
			bootLock.waitUntil = sdl.GetTicks64() + uint64(d.Milliseconds())
		}
		bootLockVoiceBackToCode()
		bootLockSay("Phrase not recognized · enter your code again", true)
		return
	}
	bootLockVoice.paused = true
	bootLockSay("Voice unlock paused · use your code", true)
}

// ── Input ────────────────────────────────────────────────────────────────

// bootLockVoicePress is A or R2 (Enter or V on a keyboard) where the phrase
// is wanted: listen, or end the clip now, or stop.
func bootLockVoicePress(c *Config) {
	v := &bootLockVoice
	stepUp := bootLockVoiceStepActive()
	if !stepUp && !bootLockVoiceEitherUp(c) {
		return
	}
	if bootLock.power || alarmFiringActive() || powerSleepActive() {
		// R2 still reaches the lock under a ringing alarm (bootLockEvent).
		return
	}
	switch {
	case v.waiting || bootLock.checking:
		return
	case !stepUp && v.paused:
		bootLockSay("Voice unlock paused · use your code", true)
		PlayErrorSound()
		return
	case !stepUp && bootLockWaitLeft() > 0:
		PlayErrorSound()
		return
	case v.on && v.seg:
		bootLockVoiceEndClip(c, sdl.GetTicks64())
		return
	case v.on:
		bootLockVoiceStop()
		v.autoUntil = 0
		PlayBackSound()
		return
	}
	bootLockVoiceStart(c, true)
}

// bootLockVoicePad is a pad press while the phrase is wanted (setup or the
// second step): A/R2/Start listen, B and Select go back, Menu is Menu.
func bootLockVoicePad(c *Config, b sdl.GameControllerButton) {
	switch b {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
		bootLockVoicePress(c)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		bootLockVoiceBack()
	case sdl.CONTROLLER_BUTTON_GUIDE:
		bootLockMenu()
	}
}

// bootLockVoiceKeyPress is a key press while the phrase is wanted.
func bootLockVoiceKeyPress(c *Config, sym sdl.Keycode) {
	switch sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_v:
		bootLockVoicePress(c)
	case sdl.K_BACKSPACE:
		bootLockVoiceBack()
	case sdl.K_ESCAPE:
		bootLockMenu()
	}
}

// bootLockVoiceBack is B/Select: the setup ends, the second step goes back
// to the code.
func bootLockVoiceBack() {
	if bootLock.mode == bootLockModeSetup {
		bootLockCancel()
		return
	}
	if bootLockVoice.waiting {
		return // the answer is a moment away
	}
	bootLockVoiceBackToCode()
	bootLockSay("", false)
	PlayBackSound()
}

// bootLockAxis is a trigger moving over the lock: R2 is "speak" where the
// phrase can be said. Both latches follow (shoulders.go).
func bootLockAxis(c *Config, e *sdl.ControllerAxisEvent) {
	switch e.Axis {
	case sdl.CONTROLLER_AXIS_TRIGGERLEFT:
		triggerPressedEdge(0, e.Value)
	case sdl.CONTROLLER_AXIS_TRIGGERRIGHT:
		if !triggerPressedEdge(1, e.Value) || bootLock.power {
			return
		}
		screensaverNotifyInput()
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		if bootLockTyping() {
			return // the password keyboard is up: A, B and Start are its own
		}
		bootLockVoicePress(c)
	}
}

// ── Words on screen ──────────────────────────────────────────────────────

// bootLockVoicePrompt is the card's prompt during a voice step, or "".
func bootLockVoicePrompt() string {
	if !bootLockVoiceStepActive() {
		return ""
	}
	switch {
	case bootLockVoice.second:
		return "Now say your unlock phrase"
	case bootLock.step == bootLockStepVoiceConfirm:
		return "Say it again to confirm"
	}
	return "Say your unlock phrase"
}

// bootLockVoiceLine is the extra line on an "either" lock screen, or "".
func bootLockVoiceLine(c *Config) string {
	if !bootLockVoiceEitherUp(c) {
		return ""
	}
	v := &bootLockVoice
	switch {
	case v.paused:
		return "Voice unlock paused · use your code"
	case v.engineOK != "":
		return v.engineOK
	case v.waiting || (bootLock.checking && v.pending == "" && !v.on):
		return "Checking what you said…"
	case v.on && v.seg:
		return "Hearing you…"
	case v.on:
		return "Listening · say your unlock phrase"
	case bootLockWaitLeft() > 0:
		return ""
	}
	return "R2: say your unlock phrase"
}

// bootLockVoicePillLabel is the voice step's pill.
func bootLockVoicePillLabel() string {
	v := &bootLockVoice
	switch {
	case v.engineOK != "":
		return "Speech is not available"
	case v.waiting || bootLock.checking:
		return "Checking…"
	case v.on && v.seg:
		return "Hearing you…"
	case v.on:
		return "Listening…"
	}
	return "Press A to speak"
}

// bootLockVoiceHelp is the setup's small print during a voice step, or "".
func bootLockVoiceHelp() string {
	if bootLock.mode != bootLockModeSetup || !bootLockVoiceStepActive() {
		return ""
	}
	return "Say it the same way each time. It is stored scrambled, but it is words, not a voice print: anyone who hears it can say it."
}

// bootLockVoiceHints is the footer during a voice step, or nil.
func bootLockVoiceHints() []FooterHint {
	if !bootLockVoiceStepActive() || bootLock.power {
		return nil
	}
	speak := FooterHint{Button: "A", Label: "Speak"}
	if bootLockVoice.on {
		speak.Label = "Stop"
		if bootLockVoice.seg {
			speak.Label = "Done"
		}
	}
	if bootLock.mode == bootLockModeSetup {
		return []FooterHint{speak, {Button: "B", Label: "Cancel"}}
	}
	return []FooterHint{speak, {Button: "Select", Label: "Back"}, {Button: "Menu", Label: "Power"}}
}

// bootLockVoiceDrawPill is the voice step's entry: a microphone, its level
// and what it is doing.
func bootLockVoiceDrawPill(renderer *sdl.Renderer, f bootLockFontSet, cx, y, h int32) {
	pillW := int32(300)
	fillRoundedRect(renderer, cx-pillW/2, y, pillW, h, h/2, glassA(18))
	stroke := glassA(40)
	if bootLockVoice.on {
		stroke = glassAccent()
		// The level as a fill from the left: the user sees it hears them.
		lvl := bootLockVoice.level / (2 * voiceSpeechLevel)
		if lvl > 1 {
			lvl = 1
		}
		if fw := int32(float64(pillW) * lvl); fw > h {
			tint := glassAccent()
			tint.A = 60
			fillRoundedRect(renderer, cx-pillW/2, y, fw, h, h/2, tint)
		}
	}
	strokeRoundedRect(renderer, cx-pillW/2, y, pillW, h, h/2, 1, stroke)
	icon := h - 10
	label := bootLockVoicePillLabel()
	tw := int32(0)
	if f.small != nil {
		if w, _, err := f.small.SizeUTF8(label); err == nil {
			tw = int32(w)
		}
	}
	x := cx - (icon+8+tw)/2
	drawHomeGlyphTinted(renderer, "mic", sdl.Rect{X: x, Y: y + 5, W: icon, H: icon}, ColorTextSecondary())
	if f.small != nil {
		drawText(renderer, f.small, label, x+icon+8, y+(h-int32(f.small.Height()))/2, ColorTextSecondary(), textAlignLeft)
	}
}

// ── Settings rows (Privacy & data) ───────────────────────────────────────

func bootLockVoiceModeLabel(c *Config, mode string) string {
	what := "Code"
	if bootLockKindOf(c) == bootLockKindPassword {
		what = "Password"
	}
	switch mode {
	case bootLockVoiceEither:
		return what + " or voice"
	case bootLockVoiceBoth:
		return what + " and voice"
	}
	return "Off"
}

func bootLockVoiceOptions(c *Config) []setOption {
	return []setOption{
		{Value: bootLockVoiceOff, Label: bootLockVoiceModeLabel(c, bootLockVoiceOff)},
		{Value: bootLockVoiceEither, Label: bootLockVoiceModeLabel(c, bootLockVoiceEither)},
		{Value: bootLockVoiceBoth, Label: bootLockVoiceModeLabel(c, bootLockVoiceBoth)},
	}
}

func bootLockVoiceValue(c *Config) string {
	if m := bootLockVoiceModeOf(c); m != "" {
		return m
	}
	return bootLockVoiceOff
}

// bootLockVoiceRow is "Voice unlock", shown while a lock is set.
func bootLockVoiceRow() setRow {
	return setRow{
		ID: "startupLockVoice", Label: "Voice unlock", Kind: setKindChoice,
		Visible: func(c *Config) bool { return bootLockKindOf(c) != "" },
		Detail: func(c *Config) string {
			switch bootLockVoiceModeOf(c) {
			case bootLockVoiceEither:
				return "Say your phrase or enter the code; it listens by itself offline, R2 asks"
			case bootLockVoiceBoth:
				return "The code first, then your phrase"
			}
			return "A spoken phrase with the code. It knows words, not voices"
		},
		Options: bootLockVoiceOptions,
		Current: func(c *Config) int {
			switch bootLockVoiceModeOf(c) {
			case bootLockVoiceEither:
				return 1
			case bootLockVoiceBoth:
				return 2
			}
			return 0
		},
		Value:      func(c *Config) string { return bootLockVoiceModeLabel(c, bootLockVoiceModeOf(c)) },
		Sheet:      bootLockVoiceOptions,
		SheetValue: bootLockVoiceValue,
		SheetTitle: "Voice unlock",
		Choose:     func(c *Config, value string) { bootLockVoiceOpenSetup(c, value) },
	}
}

// bootLockVoiceChangeRow is "Change the voice phrase", shown while Voice
// unlock is on.
func bootLockVoiceChangeRow() setRow {
	return setRow{
		ID: "startupLockVoicePhrase", Label: "Change the voice phrase", Kind: setKindAction,
		Visible: func(c *Config) bool { return bootLockVoiceModeOf(c) != "" },
		Detail:  setStatic("The code first, then the new phrase twice"),
		Open:    func(c *Config) { bootLockVoiceOpenSetup(c, bootLockVoicePhrase) },
	}
}
