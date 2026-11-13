package main

// wakeword.go — "Hey Juka": the voice assistant without a button.
//
// With Settings → Voice & AI → Hey Juka on (off until turned on: it keeps the
// microphone open), saying "Hey Juka" on Home or any menu opens the voice orb
// already listening, and in a game opens the game assistant the way holding
// Select + R2 does ("Hey Juka in games", on with it). Words said straight after
// the name in one breath ("Hey Juka, open YouTube") are kept: the wake word's
// open microphone and the audio after the name go to the orb
// (hjTakeHandoff, voiceOrbListen), so nothing is cut off.
//
// The handheld only (offline speech installed, a package that streams), and
// nothing leaves it: the listener is the warm offline recognizer
// (stt_stream.go) with a tiny grammar — "hey" plus the spellings of the name
// the model knows, and "[unk]" for everything else — so decoding is cheap,
// and it decodes only while there is sound:
//
//   - an energy gate reads the microphone in 20 ms frames against a noise
//     floor that follows the room (the orb's own rule); 60 ms of sound starts
//     a segment, which includes the 300 ms before it (the "h" of "hey"), and
//     600 ms of quiet or 4 s end it. In silence nothing is decoded at all;
//   - the decoder runs at background priority (nice 10), one core at most;
//   - a match needs "hey" and the name, both with enough confidence, and is
//     followed by a 2 s rest.
//
// It stands aside — microphone closed — while anything else listens or the
// handheld is not in use: the orb or the game assistant up, voice typing, the
// Voice command page, Chat's microphone or voice shortcuts (micOpen calls
// hjYield), Juka speaking, an alarm, sleep, the screensaver or a dark
// screen, a video or music playing, the keyboard open, the first-run tour,
// and memory under sttServeStartAvail. The stick lights' Microphone mode
// (ledmic.go) follows the wake word's own reading while it holds the
// microphone.
//
// Cost on the Smart Pro (estimates, see docs/VOICE.md): the recognizer stays
// loaded while the wake word is on (about 100-150 MB of the 1 GB); in a quiet
// room the CPU cost is the gate's arithmetic, well under 1% of a core; while
// people talk or a game is loud it decodes on one core at low priority.
//
// Main thread only (hjTick runs from the main loop and from the parked loop
// while a game has the screen); the recognizer's goroutines hand back through
// sttStream.

import (
	"strings"
	"sync/atomic"
	"time"
)

const (
	// hjWordKey and hjGamesKey are the Custom settings behind the rows.
	hjWordKey  = "wakeWord"
	hjGamesKey = "wakeWordGames"
	// hjFrameBytes is one 20 ms frame of 16 kHz mono.
	hjFrameBytes = micRate / 50 * 2
	// hjPreRollBytes is the audio kept from before a segment starts.
	hjPreRollBytes = hjFrameBytes * 15
	// hjOnsetFrames of sound start a segment; hjQuietFrames of quiet end
	// it; hjMaxSegment bounds it (a wake phrase takes about a second).
	hjOnsetFrames = 3
	hjQuietFrames = 30
	hjMaxSegment  = 4 * time.Second
	// hjKeepBytes bounds the segment's own copy, kept for the handover.
	hjKeepBytes = micRate * 2 * 8
	// hjFeedBytes batches the frames handed to the recognizer (100 ms).
	hjFeedBytes = micRate / 10 * 2
	// hjReadBytes is one read of the microphone (the device's own limit is
	// two seconds; the parked loop reads ten times a second).
	hjReadBytes = micRate * 2
	// hjCooldown follows a match, so one phrase does not open twice.
	hjCooldown = 2 * time.Second
	// hjMinLevel is the quietest sound (RMS) that can start a segment, so
	// breath in a silent room is not decoded.
	hjMinLevel   = 0.006
	hjFloorCreep = 1.002
	// hjConfHey and hjConfName are the confidences the two words need.
	hjConfHey  = 0.5
	hjConfName = 0.6
	// hjRetryAfter is the pause after the microphone would not open;
	// hjResumeAfter the wait after another feature lets go of it.
	hjRetryAfter  = 30 * time.Second
	hjResumeAfter = 1500 * time.Millisecond
	// hjDeadAfter reopens a microphone that gives nothing.
	hjDeadAfter = 3 * time.Second
	// hjHandoffLife is how long a handover may wait for the orb or the
	// game assistant to take it.
	hjHandoffLife = 4 * time.Second
	// hjMemEvery paces the free-memory look.
	hjMemEvery = 5 * time.Second
)

// hjNames are spellings and sound-alikes of "Juka" tried against the
// model's vocabulary; the ones it knows (sttServer.knownWords) make the
// grammar. A two-word one counts when both words are known.
var hjNames = []string{"juka", "jukka", "juca", "yuka", "yukka", "yucca", "jooka", "joka", "juke a", "you ka", "you car", "joker"}

// hjHandoff is the wake word's microphone on its way to the orb.
type hjHandoff struct {
	mic    *micStream
	pcm    []byte // what was said after the name
	speech bool   // pcm holds speech: the user went straight on talking
	at     time.Time
}

// wake is the listener's state. Main thread.
var hj struct {
	mic      *micStream
	inGame   bool
	buf      []byte
	work     []byte
	carry    []byte
	preroll  []byte
	floor    float64
	floorSet bool
	loud     int
	level    float64
	gotAt    time.Time

	seg     *sttStream
	segPCM  []byte
	segAt   time.Time
	pending []byte
	quiet   int
	ending  bool

	hand      *hjHandoff
	cooldown  time.Time
	retryAt   time.Time
	resumeAt  time.Time
	hadVoices bool
	failed    bool
	deadRuns  int    // reopens in a row after a microphone that gave nothing
	why       string // the last reason it stood aside, for the log
	memAt     time.Time
	memOK     bool
	localOK   bool
}

// hjGrammar is the resolved grammar (nil until the model was asked).
var (
	hjGrammar    atomic.Value // []string
	hjResolving  atomic.Bool
	hjNamesKnown atomic.Int32 // -1: no name known, so the wake word cannot work
)

func init() {
	Cleanup = append(Cleanup, hjShutdown)
}

// hjShutdown closes the listener and the warm recognizer when JukaHub quits.
// It runs after voicemic.go's own wait, so it waits for its close itself.
func hjShutdown() {
	hjRelease("JukaHub is closing")
	hjDropHandoff("JukaHub is closing")
	sttServeStop("JukaHub is closing")
	micCloseWait()
}

// hjEnabled reports Settings → Voice & AI → Hey Juka.
func hjEnabled(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, hjWordKey, false)
}

// hjGamesEnabled reports "Hey Juka in games" (on unless turned off).
func hjGamesEnabled(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, hjGamesKey, true)
}

// hjHoldsMic reports whether the wake word has the microphone (ledmic.go
// stays closed then and follows hjLevel instead).
func hjHoldsMic() bool { return hj.mic != nil || hj.hand != nil }

// hjTick runs the listener, once a frame from the main loop and ten times a
// second from the parked loop (inGame).
func hjTick(config *Config, inGame bool) {
	now := time.Now()
	if hj.hand != nil && now.Sub(hj.hand.at) > hjHandoffLife {
		hjDropHandoff("nothing took it")
	}
	if micVoiceUsers.Load() > 0 {
		hj.hadVoices = true
	} else if hj.hadVoices {
		hj.hadVoices = false
		hj.resumeAt = now.Add(hjResumeAfter)
	}
	if why := hjBlocked(config, inGame, now); why != "" {
		hjRelease(why)
		return
	}
	if hj.mic != nil && hj.inGame != inGame {
		// Into or out of a game: the parked loop reads at its own pace.
		hjRelease("the screen changed hands")
	}
	if hj.mic == nil && !hjOpen(now, inGame) {
		return
	}
	hjRead(now)
	if !inGame {
		ledMicFeedVoice(hj.level) // the stick lights (ledmic.go) follow the room
	}
	hjCheck(config, now)
}

// hjBlocked names why the listener must stand aside now, or "".
func hjBlocked(config *Config, inGame bool, now time.Time) string {
	switch {
	case !hjEnabled(config):
		return "off"
	case !sttSupported():
		return "not this device"
	case !hjLook(now):
		return "offline speech is not installed"
	case !sttServeUsable():
		return "offline speech cannot stream"
	case hjNamesKnown.Load() < 0:
		return "the speech model knows no spelling of the name"
	case hj.hand != nil:
		return "handing over"
	case inGame && !hjGamesEnabled(config):
		return "off in games"
	case inGame && (!gameAssistSupported() || !gameAssistEnabled(config)):
		return "the game assistant is off"
	case gameAssistOverlayActive(), voiceOrb.phase != orbHidden:
		return "Juka is up"
	case micVoiceUsers.Load() > 0, now.Before(hj.resumeAt):
		return "another feature has the microphone"
	case jukaSpeaking():
		return "Juka is speaking"
	case alarmFiringActive(), powerSleepActive(), screensaverAsleep(), musicScreenOffActive(),
		saverPausesBackground() && !inGame, windowMinimised():
		return "the handheld is resting"
	case !inGame && (playerPhase() == "playing" || hjMusicPlaying()):
		return "media is playing"
	case virtualKeyboardActive, onboarding.active:
		return "a screen needs the buttons"
	case !hj.memOK:
		return "memory is low"
	}
	return ""
}

// hjMusicPlaying reports music that is playing (the speaker would keep the
// decoder busy, and the music itself has words).
func hjMusicPlaying() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.playing && !music.paused
}

// hjLook refreshes, every hjMemEvery, whether offline speech is installed and
// whether memory allows the recognizer (it needs sttServeStartAvail to start
// and gives way under sttServeKeepAvail), and reports the first: both are
// file and /proc reads, too many for every frame.
func hjLook(now time.Time) bool {
	if now.Sub(hj.memAt) < hjMemEvery && !hj.memAt.IsZero() {
		return hj.localOK
	}
	hj.memAt = now
	hj.localOK = sttLocalReady()
	avail := sttMemAvailable()
	need := int64(sttServeStartAvail)
	if hj.mic != nil {
		need = sttServeKeepAvail
	}
	hj.memOK = avail <= 0 || avail >= need
	return hj.localOK
}

// hjOpen opens the microphone (shared, like the lights') and warms the
// recognizer. false: not now.
func hjOpen(now time.Time, inGame bool) bool {
	if now.Before(hj.retryAt) || micClosing.n.Load() > 0 {
		return false
	}
	// The lights give the microphone to the wake word and follow its level.
	ledMicClose()
	m, err := micOpenShared()
	if err != nil {
		hj.retryAt = now.Add(hjRetryAfter)
		if !hj.failed {
			hj.failed = true
			logWarn("voice", "Hey Juka: no microphone: %v", err)
		}
		return false
	}
	hj.failed = false
	hj.mic, hj.inGame, hj.gotAt = m, inGame, now
	hj.carry, hj.preroll = hj.carry[:0], hj.preroll[:0]
	hj.floorSet, hj.loud, hj.level = false, 0, 0
	sttServeKeep(true)
	sttServeWarm()
	hjResolveGrammar()
	logInfo("voice", "Hey Juka is listening (%s, in a game: %t)", m.Via(), inGame)
	hj.why = ""
	return true
}

// hjRelease closes the listener's microphone and drops a segment; the
// recognizer may stop once idle. Safe when nothing is open.
func hjRelease(why string) {
	hjEndSegment()
	if hj.mic != nil {
		hj.mic.Close()
		hj.mic = nil
		logInfo("voice", "Hey Juka paused: %s", why)
	}
	if why != hj.why {
		hj.why = why
		if hj.hand == nil {
			sttServeKeep(false)
		}
	}
}

// hjYield is micOpen's word: a voice feature takes the microphone now.
func hjYield() {
	// A handover nobody took holds a second capture open (the orb takes its
	// own with hjTakeHandoff before it could get here).
	hjDropHandoff("another feature has the microphone")
	if hj.mic != nil {
		hjRelease("another feature has the microphone")
		hj.hadVoices = true
	}
}

func hjEndSegment() {
	if hj.seg != nil {
		hj.seg.Cancel()
		hj.seg = nil
	}
	hj.ending = false
	hj.segPCM, hj.pending = hj.segPCM[:0], hj.pending[:0]
	hj.quiet = 0
}

// hjRead takes what the microphone captured and runs the gate on it.
func hjRead(now time.Time) {
	if cap(hj.buf) < hjReadBytes {
		hj.buf = make([]byte, hjReadBytes)
	}
	got := false
	for i := 0; i < 4; i++ {
		n := hj.mic.Read(hj.buf[:hjReadBytes])
		if n <= 0 {
			break
		}
		got = true
		hjTakePCM(hj.buf[:n], now)
		if n < hjReadBytes {
			break
		}
	}
	if got {
		hj.gotAt = now
		hj.deadRuns = 0
	} else if now.Sub(hj.gotAt) > hjDeadAfter {
		hjRelease("the microphone gave nothing; reopening")
		// A capture that stays dead (a game holding the codec) is tried
		// less and less often, up to hjRetryAfter, not every few seconds.
		wait := hjResumeAfter << min(hj.deadRuns, 5)
		if wait > hjRetryAfter {
			wait = hjRetryAfter
		}
		hj.deadRuns++
		hj.resumeAt = now.Add(wait)
		return
	}
	if hj.seg != nil && !hj.ending && len(hj.pending) > 0 &&
		(len(hj.pending) >= hjFeedBytes || hj.quiet > 0) {
		hj.seg.Feed(hj.pending)
		hj.pending = hj.pending[:0]
	}
}

// hjTakePCM cuts audio into 20 ms frames (a partial one waits for the next
// read).
func hjTakePCM(pcm []byte, now time.Time) {
	hj.work = append(append(hj.work[:0], hj.carry...), pcm...)
	data := hj.work
	for len(data) >= hjFrameBytes {
		hjFrame(data[:hjFrameBytes], now)
		data = data[hjFrameBytes:]
	}
	hj.carry = append(hj.carry[:0], data...)
}

// hjFrame is the gate for one frame: the room's floor, the level, and a
// segment started, fed or ended.
func hjFrame(f []byte, now time.Time) {
	level := vkPCMLevel(f)
	switch {
	case !hj.floorSet:
		hj.floor, hj.floorSet = level, true
	case level < hj.floor:
		hj.floor = level
	default:
		hj.floor *= hjFloorCreep
	}
	if hj.floor < orbFloorMin {
		hj.floor = orbFloorMin
	}
	th := hj.floor * orbSpeechFloor
	if th < hjMinLevel {
		th = hjMinLevel
	}
	if th > 2*voiceSpeechLevel {
		th = 2 * voiceSpeechLevel
	}
	loud := level >= th
	if level > hj.level {
		hj.level = level
	} else {
		hj.level = hj.level*0.9 + level*0.1
	}
	if hj.seg == nil {
		hj.preroll = append(hj.preroll, f...)
		if over := len(hj.preroll) - hjPreRollBytes; over > 0 {
			hj.preroll = append(hj.preroll[:0], hj.preroll[over:]...)
		}
		if loud {
			hj.loud++
		} else {
			hj.loud = 0
		}
		if hj.loud >= hjOnsetFrames && !now.Before(hj.cooldown) {
			hjBeginSegment(now)
		}
		return
	}
	if len(hj.segPCM)+len(f) <= hjKeepBytes {
		hj.segPCM = append(hj.segPCM, f...)
	}
	if hj.ending {
		return
	}
	hj.pending = append(hj.pending, f...)
	if loud {
		hj.quiet = 0
	} else {
		hj.quiet++
	}
}

// hjBeginSegment starts decoding, from the pre-roll on.
func hjBeginSegment(now time.Time) {
	g, _ := hjGrammar.Load().([]string)
	if len(g) == 0 {
		g = hjGrammarFor(hjNames)
	}
	hj.seg = sttStreamStart(sttStreamOpts{grammar: g, words: true, background: true, partialEvery: 150 * time.Millisecond})
	hj.segPCM = append(hj.segPCM[:0], hj.preroll...)
	hj.pending = append(hj.pending[:0], hj.preroll...)
	hj.preroll = hj.preroll[:0]
	hj.segAt, hj.quiet, hj.ending, hj.loud = now, 0, false, 0
}

// hjCheck ends a segment on a heard wake phrase, a pause or its cap, and
// acts on the final words once they are in.
func hjCheck(config *Config, now time.Time) {
	s := hj.seg
	if s == nil {
		return
	}
	if !hj.ending {
		heard := hjFindText(s.Partial())
		if !heard && hj.quiet < hjQuietFrames && now.Sub(hj.segAt) < hjMaxSegment {
			return
		}
		if len(hj.pending) > 0 {
			s.Feed(hj.pending)
			hj.pending = hj.pending[:0]
		}
		// The final words carry each word's confidence and end time; the
		// microphone keeps recording into segPCM meanwhile.
		s.Finish()
		hj.ending = true
	}
	select {
	case <-s.Done():
	default:
		return
	}
	words, err := s.Words()
	pcm := hj.segPCM
	hj.seg, hj.ending = nil, false
	hj.quiet, hj.pending = 0, hj.pending[:0]
	if err != nil {
		logDebug("voice", "Hey Juka: %v", err)
		hj.cooldown = now.Add(hjCooldown)
		hj.segPCM = hj.segPCM[:0]
		return
	}
	end, ok := hjMatch(words)
	if !ok {
		// The tail of what was said may be the start of the next phrase.
		if len(pcm) > hjPreRollBytes {
			pcm = pcm[len(pcm)-hjPreRollBytes:]
		}
		hj.preroll = append(hj.preroll[:0], pcm...)
		hj.segPCM = hj.segPCM[:0]
		return
	}
	hjFire(config, end, now)
}

// hjFire hands the microphone and what followed the name to the orb, or to
// the game assistant in a game.
func hjFire(config *Config, end float64, now time.Time) {
	off := int(end*float64(micRate)) * 2
	if off < 0 || off > len(hj.segPCM) {
		off = len(hj.segPCM)
	}
	after := append([]byte(nil), hj.segPCM[off:]...)
	hj.segPCM = hj.segPCM[:0]
	hj.cooldown = now.Add(hjCooldown)
	hj.hand = &hjHandoff{mic: hj.mic, pcm: after, speech: hjHasSpeech(after), at: now}
	hj.mic = nil
	inGame := hj.inGame
	logInfo("voice", "Hey Juka heard (in a game: %t, went on talking: %t)", inGame, hj.hand.speech)
	if inGame {
		if !gaWakeAsk() {
			hjDropHandoff("the game assistant cannot open now")
		}
		return
	}
	if !voiceOrbWake(config) {
		hjDropHandoff("the orb cannot open here")
	}
}

// hjHasSpeech reports whether audio after the name has a frame of speech in
// it, by the listener's own floor.
func hjHasSpeech(pcm []byte) bool {
	th := hj.floor * orbSpeechFloor
	if th < hjMinLevel {
		th = hjMinLevel
	}
	for len(pcm) >= hjFrameBytes {
		if vkPCMLevel(pcm[:hjFrameBytes]) >= th {
			return true
		}
		pcm = pcm[hjFrameBytes:]
	}
	return false
}

// hjTakeHandoff is the orb's side of the handover (voiceOrbListen): the
// open microphone and the words after the name, or nil.
func hjTakeHandoff() *hjHandoff {
	h := hj.hand
	if h == nil {
		return nil
	}
	hj.hand = nil
	if time.Since(h.at) > hjHandoffLife || !h.mic.Open() {
		h.mic.Close()
		return nil
	}
	return h
}

// hjDropHandoff closes a microphone nobody took.
func hjDropHandoff(why string) {
	if hj.hand == nil {
		return
	}
	hj.hand.mic.Close()
	hj.hand = nil
	gaLink.wake.Store(false)
	logInfo("voice", "Hey Juka: %s", why)
}

// ── The phrase ───────────────────────────────────────────────────────────

// hjGrammarFor is the grammar for these names: "hey" plus each name, "hey"
// alone (so "hey there" is not forced into a name) and "[unk]". Pure.
func hjGrammarFor(names []string) []string {
	g := make([]string, 0, len(names)+2)
	for _, n := range names {
		g = append(g, "hey "+n)
	}
	return append(g, "hey", "[unk]")
}

// hjResolveGrammar asks the model, once per run, which spellings it knows.
func hjResolveGrammar() {
	if hjGrammar.Load() != nil || !hjResolving.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer hjResolving.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logWarn("voice", "Hey Juka: grammar check stopped: %v", r)
			}
		}()
		srv, err := sttServerGet()
		if err != nil {
			return
		}
		if srv.waitReady() != nil {
			return
		}
		var words []string
		for _, n := range hjNames {
			words = append(words, strings.Fields(n)...)
		}
		known, err := srv.knownWords(append(words, "hey"))
		if err != nil {
			return
		}
		ok := map[string]bool{}
		for _, w := range known {
			ok[w] = true
		}
		if !ok["hey"] {
			hjNamesKnown.Store(-1)
			logWarn("voice", "Hey Juka: the speech model does not know \"hey\"")
			return
		}
		var names []string
		for _, n := range hjNames {
			all := true
			for _, w := range strings.Fields(n) {
				all = all && ok[w]
			}
			if all {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			hjNamesKnown.Store(-1)
			logWarn("voice", "Hey Juka: the speech model knows none of %q", hjNames)
			return
		}
		hjNamesKnown.Store(int32(len(names)))
		hjGrammar.Store(hjGrammarFor(names))
		logInfo("voice", "Hey Juka listens for: %s", strings.Join(names, ", "))
	}()
}

// hjFindText reports whether recognized text holds "hey" and a name. Pure.
func hjFindText(text string) bool {
	f := strings.Fields(strings.ToLower(text))
	for i := range f {
		if f[i] == "hey" && hjNameAt(f[i+1:]) > 0 {
			return true
		}
	}
	return false
}

// hjNameAt is how many words of f, from its start, are a name (0: none).
// Pure.
func hjNameAt(f []string) int {
	for _, n := range hjNames {
		parts := strings.Fields(n)
		if len(parts) > len(f) {
			continue
		}
		match := true
		for j, p := range parts {
			if f[j] != p {
				match = false
				break
			}
		}
		if match {
			return len(parts)
		}
	}
	return 0
}

// hjMatch finds "hey" and a name with enough confidence in a final result
// and returns where the name ends (seconds from the segment's start). Pure.
func hjMatch(words []sttWord) (float64, bool) {
	f := make([]string, len(words))
	for i, w := range words {
		f[i] = strings.ToLower(w.Word)
	}
	for i := range words {
		if f[i] != "hey" || words[i].Conf < hjConfHey {
			continue
		}
		k := hjNameAt(f[i+1:])
		if k == 0 {
			continue
		}
		sure := true
		for _, w := range words[i+1 : i+1+k] {
			sure = sure && w.Conf >= hjConfName
		}
		if sure {
			return words[i+k].End, true
		}
	}
	return 0, false
}

// ── Settings → Voice & AI ────────────────────────────────────────────────

// hjUnavailable says why Hey Juka cannot run here, or "".
func hjUnavailable(*Config) string {
	switch {
	case !sttLocalReady():
		return "Needs offline speech — install it below"
	case sttServeProbe.state.Load() == sttServeNo:
		return "Needs the newer offline speech — it updates by itself on Wi-Fi"
	case hjNamesKnown.Load() < 0:
		return "The offline speech model can't hear the name"
	}
	sttServeUsable() // looks in the background the first time
	return ""
}

// hjStore saves one of the two switches.
func hjStore(c *Config, key string, on bool) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[key] = on
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// hjSettingsRows is Hey Juka's two switches. Only the Smart Pro has the
// offline recognizer the wake word runs on, so other devices do not show them.
func hjSettingsRows() []setRow {
	return []setRow{
		{
			ID: hjWordKey, Label: "Hey Juka", Kind: setKindToggle,
			Visible:  func(*Config) bool { return sttSupported() },
			Disabled: func(c *Config) bool { return hjUnavailable(c) != "" && !hjEnabled(c) },
			Detail: func(c *Config) string {
				if why := hjUnavailable(c); why != "" {
					return why
				}
				return "Say “Hey Juka” instead of pressing Menu. The mic stays on; nothing leaves the handheld"
			},
			Get: hjEnabled,
			Set: func(c *Config, on bool) {
				hjStore(c, hjWordKey, on)
				setToast("Hey Juka: " + setOnOff(on))
			},
		},
		{
			ID: hjGamesKey, Label: "Hey Juka in games", Kind: setKindToggle,
			Visible: func(c *Config) bool { return sttSupported() && gameAssistSupported() && hjEnabled(c) },
			Disabled: func(c *Config) bool {
				return !gameAssistEnabled(c)
			},
			Detail: func(c *Config) string {
				if !gameAssistEnabled(c) {
					return "Turn on Game assistant first"
				}
				return "Opens the game assistant. Uses a little CPU while the game is loud"
			},
			Get: hjGamesEnabled,
			Set: func(c *Config, on bool) {
				hjStore(c, hjGamesKey, on)
				setToast("Hey Juka in games: " + setOnOff(on))
			},
		},
	}
}
