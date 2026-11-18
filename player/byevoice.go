package main

// byevoice.go — the goodbye and the greetings, said in the user's own name.
//
// With Settings → Your name set, the shutdown card ends on "Goodbye, <name>"
// (bootgreet.go), and Juka says it too, the moment the line comes in. Making
// speech on the way out would cost the exit a second or more of Piper (or of
// PowerShell, on Windows): longer than the card is up. So the line is made
// ahead, by the device's own offline voice (tts_local.go), as soon as a new
// name has settled, and kept as a small WAV in the app's data folder
// (voice-name/), with a restart's "See you soon, <name>" beside it. On the
// way out the sound is already in memory and plays at once, on its own audio
// device like the shutdown chime (bootchime.go): the teardown's "stop playback"
// step never cuts it, and the exit waits for it to finish.
//
// The launch greeting (bootSpeakGreeting: "Good morning, <name>", "Good
// afternoon, …", "Good evening, …") is made the same way, all three at once,
// and each is also put into Juka's voice cache under the very key the launch
// asks for (tts_cache.go), so the launch plays it from there at once instead
// of loading Piper at boot. A copy the cache has pushed out is put back from
// voice-name/ at the next look, without asking the voice again.
//
// A name typed in Settings is saved on every keystroke, so a name is made only
// once it has stayed the same for byeVoiceSettle. A name already set when
// JukaHub starts is checked at once (the files are loaded if they are there)
// and made, when missing, once the boot has settled; an offline voice
// installed or upgraded later is noticed by the next look (byeVoiceRecheck).
// Nothing is made while a game runs or Juka is speaking: the line waits.
//
// Nothing is made or said while Juka's voice is Off, and nothing is said in
// Quiet Mode, on a stop from outside (a signal) or on a long sleep's silent
// power off. A sound made for another name is never played — a goodbye to the
// wrong name is worse than none — and clearing the name deletes the files.
// The log never carries the name.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// byeVoiceSettle is how long a changed name must stay the same before it
	// is made: Settings saves the name on every keystroke.
	byeVoiceSettle = 2 * time.Second
	// byeVoiceBootQuiet keeps a line missing at start-up from being made
	// while the boot and the first screens still load.
	byeVoiceBootQuiet = 20 * time.Second
	// byeVoiceRecheck is how often the worker looks again with nothing new
	// (an offline voice installed or upgraded since), and byeVoiceWaitAgain
	// how soon it looks while a line waits for a game or Juka to finish.
	byeVoiceRecheck   = time.Minute
	byeVoiceWaitAgain = 10 * time.Second
	// byeVoiceTalkQuiet keeps lines from being made while a talk with Juka
	// may still go on: the orb's own voice is loaded then.
	byeVoiceTalkQuiet = 15 * time.Second
	// byeVoiceRetry is how long a line that could not be made waits before
	// the same voice is asked again.
	byeVoiceRetry = 10 * time.Minute
	// byeVoiceMakeTimeout bounds one line's making.
	byeVoiceMakeTimeout = 60 * time.Second
	// byeVoiceMaxMS is the longest goodbye kept, in milliseconds of sound:
	// the exit waits for it, and it is queued whole (outFIFOMax).
	byeVoiceMaxMS = 3500
	// byeVoiceTickEvery is how often the main loop reads the name.
	byeVoiceTickEvery = 500 * time.Millisecond
	// byeVoiceDelay is how far into the held farewell card the voice starts:
	// as the line begins to come in (bootgreet.go, bye 0.2).
	byeVoiceDelay = 200 * time.Millisecond
	// byeVoiceTailMS keeps the line on the card a moment after it is said,
	// and byeVoiceHoldMaxMS caps the held card the voice lengthens.
	byeVoiceTailMS    = 150.0
	byeVoiceHoldMaxMS = 2600.0
	// byeVoiceOpenSlack is room for opening the audio device before the line
	// plays, in the check that it ends inside the teardown's budget.
	byeVoiceOpenSlack = 300 * time.Millisecond
)

// The lines: the farewells as the card shows them (shutdownFarewellText), then
// the launch greetings, made first to last.
const (
	byeVoiceGoodbye   = iota // a power off or a quit
	byeVoiceSoon             // a restart
	byeVoiceMorning          // the launch greeting from 5 in the morning
	byeVoiceAfternoon        // from noon
	byeVoiceEvening          // from 6 in the evening
	byeVoiceKinds
)

// byeVoiceKindNames name the lines in file names and the log.
var byeVoiceKindNames = [byeVoiceKinds]string{"goodbye", "soon", "morning", "afternoon", "evening"}

// byeVoiceText is the line Juka says for kind and name. The device's voice
// speaks English (the voice commands' answers are English too), so the line
// is English whatever the interface language; the card shows it translated.
// The greetings are bootSpeakGreeting's words exactly — homeGreeting's
// "Good morning, <name>" (defaultGreeting, homelayout.go) and a period — or
// the launch would not find them in the cache. Pure.
func byeVoiceText(kind int, name string) string {
	switch kind {
	case byeVoiceSoon:
		return "See you soon, " + name + "."
	case byeVoiceMorning:
		return "Good morning, " + name + "."
	case byeVoiceAfternoon:
		return "Good afternoon, " + name + "."
	case byeVoiceEvening:
		return "Good evening, " + name + "."
	}
	return "Goodbye, " + name + "."
}

// byeVoiceGreeting reports whether kind is a launch greeting. Pure.
func byeVoiceGreeting(kind int) bool {
	return kind >= byeVoiceMorning && kind < byeVoiceKinds
}

// byeVoiceChunk is a greeting as the launch's session asks the cache for it:
// the one chunk ttsPrepare makes of it, "" when it makes more than one or the
// chunk is too long to keep (a very long name).
func byeVoiceChunk(text string) string {
	chunks := ttsPrepare(text)
	if len(chunks) != 1 || !ttsCacheable(chunks[0]) {
		return ""
	}
	return chunks[0]
}

// byeVoiceClip is one line's sound.
type byeVoiceClip struct {
	text    string // the words, name included
	voice   string // byeVoiceHash of the voice that made it
	samples []int16
	rate    int
}

// byeVoiceWant is what the main loop last saw: the name, and whether Juka's
// voice may speak at all.
type byeVoiceWant struct {
	name string
	on   bool
}

// byeVoice is the worker's state. Any goroutine, under mu.
var byeVoice struct {
	mu      sync.Mutex
	want    byeVoiceWant
	changed time.Time // when want last changed (zero: as found at start-up)
	clips   [byeVoiceKinds]byeVoiceClip
	failed  map[string]time.Time // voice and words → not tried again before
	cleared bool                 // the files are gone (no name)
	wake    chan struct{}        // nil until the worker starts
}

// byeVoiceTickAt is when byeVoiceTick last read the name. Main thread only.
var byeVoiceTickAt time.Time

// byeVoicePruned is the name whose lines alone the folder was last left with
// (byeVoicePrune). The worker only.
var byeVoicePruned string

// byeVoiceTick hands the name to the worker, starting it the first time. Main
// thread: it reads the config. Cheap; called every frame.
//
// While the on-screen keyboard is up nothing is handed over: a name typed
// there is saved letter by letter, with pauses on a D-pad longer than
// byeVoiceSettle, and only the name it closes on is made.
func byeVoiceTick(c *Config) {
	now := time.Now()
	if c == nil || virtualKeyboardActive || now.Sub(byeVoiceTickAt) < byeVoiceTickEvery {
		return
	}
	byeVoiceTickAt = now
	w := byeVoiceWant{name: userName(c), on: ttsModeAllows(c, ttsKindVoice)}
	byeVoice.mu.Lock()
	if byeVoice.wake == nil {
		ctx, stop := context.WithCancel(context.Background())
		byeVoice.wake, byeVoice.want = make(chan struct{}, 1), w
		byeVoice.mu.Unlock()
		// A line being made when the app quits ends with it (shutdown.go's
		// services row), instead of leaving a Piper behind.
		bootOnShutdown(stop)
		go byeVoiceRun(ctx, byeVoice.wake)
		return
	}
	if w != byeVoice.want {
		byeVoice.want, byeVoice.changed = w, now
		select {
		case byeVoice.wake <- struct{}{}:
		default:
		}
	}
	byeVoice.mu.Unlock()
}

// byeVoiceRun is the worker: it looks at once, then whenever the name changes
// (after it settles) and every so often, until the app quits.
func byeVoiceRun(ctx context.Context, wake <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("tts", "the goodbye voice stopped: %v", r)
		}
	}()
	started := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		byeVoice.mu.Lock()
		w, changed := byeVoice.want, byeVoice.changed
		byeVoice.mu.Unlock()
		wait := byeVoiceRecheck
		if left := byeVoiceSettle - time.Since(changed); !changed.IsZero() && left > 0 {
			// Still being typed: look again once it has settled.
			wait = left
		} else if quiet := changed.IsZero() && time.Since(started) < byeVoiceBootQuiet; !byeVoiceSync(ctx, w, quiet) {
			wait = byeVoiceWaitAgain
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
}

// byeVoiceSync brings the kept lines in line with w: loads them, makes what is
// missing (or was made by another voice) and forgets them without a name. It
// reports false while a line still waits to be made (quiet: the boot has not
// settled; or a game or Juka's voice is busy).
func byeVoiceSync(ctx context.Context, w byeVoiceWant, quiet bool) bool {
	if w.name == "" {
		byeVoiceForget()
		return true
	}
	// Another name's lines go as soon as the name changes, whether or not
	// this one can be made.
	byeVoicePrune(w.name)
	if !w.on {
		// Nothing is made while Juka's voice is off; what is kept stays for
		// when it is back on.
		return true
	}
	v, ok := ttsLocal()
	voice := ""
	if ok {
		voice = byeVoiceHash(ttsCacheLocalID(v, ""))
	}
	// The launch says its greeting only with offline answers installed
	// (bootVoiceGreetingReady): without them the greetings are not made.
	greet := offlineAIReady()
	done := true
	for kind := 0; kind < byeVoiceKinds && ctx.Err() == nil; kind++ {
		text := byeVoiceText(kind, w.name)
		if byeVoiceGreeting(kind) && (!greet || byeVoiceChunk(text) == "") {
			continue
		}
		byeVoice.mu.Lock()
		have, stale := byeVoice.clips[kind], byeVoice.want != w
		byeVoice.mu.Unlock()
		if stale {
			// A newer name came in meanwhile: the wake it sent brings it.
			return true
		}
		if have.text != text {
			have, _ = byeVoiceLoad(kind, text, voice)
			byeVoiceKeep(kind, have)
		}
		if !ok {
			// No voice to make it with.
			continue
		}
		if have.text != text || have.voice != voice {
			if quiet || byeVoiceBusy() {
				done = false
				continue
			}
			key := voice + "\x00" + text
			byeVoice.mu.Lock()
			until := byeVoice.failed[key]
			byeVoice.mu.Unlock()
			if time.Now().Before(until) {
				continue
			}
			made, again := byeVoiceMake(ctx, v, voice, kind, text)
			if !made {
				if ctx.Err() == nil {
					// A line that came out silent or too long would come out
					// the same again: not this run.
					wait := byeVoiceRetry
					if !again {
						wait = 24 * time.Hour
					}
					byeVoice.mu.Lock()
					if byeVoice.failed == nil {
						byeVoice.failed = map[string]time.Time{}
					}
					byeVoice.failed[key] = time.Now().Add(wait)
					byeVoice.mu.Unlock()
				}
				continue
			}
			byeVoice.mu.Lock()
			have = byeVoice.clips[kind]
			byeVoice.mu.Unlock()
		}
		if byeVoiceGreeting(kind) {
			byeVoiceCacheGreeting(v, have)
		}
	}
	return done
}

// byeVoiceCacheGreeting puts a greeting made in the current voice into Juka's
// voice cache under the launch's own key: the start-up session speaks with the
// device's voice and no gender (ttsBegin), and asks for the one chunk
// ttsPrepare makes of the line (byeVoiceChunk). Already there: nothing to do.
func byeVoiceCacheGreeting(v ttsLocalVoice, clip byeVoiceClip) {
	chunk := byeVoiceChunk(clip.text)
	if chunk == "" || len(clip.samples) == 0 || clip.rate <= 0 {
		return
	}
	if id := ttsCacheLocalID(v, ""); !ttsCacheHas(id, chunk) {
		ttsCacheStore(id, chunk, clip.samples, clip.rate)
	}
}

// byeVoiceUncache takes name's greetings out of Juka's voice cache (the
// current voice's), when the name changes or is cleared: the cache would
// otherwise keep saying an old name until it pushed it out. The worker only.
func byeVoiceUncache(name string) {
	if name == "" {
		return
	}
	v, ok := ttsLocal()
	if !ok {
		return
	}
	id := ttsCacheLocalID(v, "")
	for kind := 0; kind < byeVoiceKinds; kind++ {
		if !byeVoiceGreeting(kind) {
			continue
		}
		if chunk := byeVoiceChunk(byeVoiceText(kind, name)); chunk != "" {
			if p := ttsCachePath(id, chunk); p != "" {
				_ = os.Remove(p)
			}
		}
	}
}

// byeVoicePrune leaves the folder (and memory) with name's lines alone: what
// was made for another name goes. It looks once per name. The worker only.
func byeVoicePrune(name string) {
	if name == byeVoicePruned {
		return
	}
	byeVoiceUncache(byeVoicePruned)
	byeVoicePruned = name
	byeVoice.mu.Lock()
	for kind := range byeVoice.clips {
		if byeVoice.clips[kind].text != byeVoiceText(kind, name) {
			byeVoice.clips[kind] = byeVoiceClip{}
		}
	}
	byeVoice.mu.Unlock()
	dir := byeVoiceDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		ours := false
		for kind := 0; kind < byeVoiceKinds; kind++ {
			if strings.HasPrefix(n, byeVoicePrefix(kind, byeVoiceText(kind, name))) && strings.HasSuffix(n, ".wav") {
				ours = true
			}
		}
		if !ours {
			_ = os.RemoveAll(filepath.Join(dir, n))
		}
	}
}

// byeVoiceBusy reports whether making a line now would compete with something
// the user is doing: a game, Juka speaking, or a talk with Juka still going
// (the orb up, its voice loaded: ttsKeepWarm). Any goroutine.
func byeVoiceBusy() bool {
	if last := ttsTalkLast(); !last.IsZero() && time.Since(last) < byeVoiceTalkQuiet {
		return true
	}
	return runningGameChildren.Load() > 0 || jukaSpeaking()
}

// byeVoiceHash names a voice or a line's words in a file name. Pure.
func byeVoiceHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// byeVoiceDir is the folder, "" when the platform has no data folder.
func byeVoiceDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "voice-name")
}

// byeVoicePrefix starts the names of the files kind's line text is kept in,
// in any voice: <kind>-<words>-. Pure.
func byeVoicePrefix(kind int, text string) string {
	return byeVoiceKindNames[kind] + "-" + byeVoiceHash(text) + "-"
}

// byeVoiceFile is the file kind's line text in voice is kept in:
// <kind>-<words>-<voice>.wav, by hash. "" with no folder.
func byeVoiceFile(kind int, text, voice string) string {
	dir := byeVoiceDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, byeVoicePrefix(kind, text)+voice+".wav")
}

// byeVoiceLoad reads kind's line text from its folder: in voice when that is
// kept, else in whichever voice made it. A file that does not read as a whole
// WAV is removed. The folder is listed rather than globbed: a data folder
// under a name with "[" in it would match nothing.
func byeVoiceLoad(kind int, text, voice string) (byeVoiceClip, bool) {
	dir := byeVoiceDir()
	if dir == "" {
		return byeVoiceClip{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return byeVoiceClip{}, false
	}
	prefix := byeVoicePrefix(kind, text)
	var paths []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".wav") {
			continue
		}
		if p := filepath.Join(dir, n); n == prefix+voice+".wav" {
			// The current voice's first.
			paths = append([]string{p}, paths...)
		} else {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		samples, rate, err := ttsDecodeWAV(data, false)
		if err != nil || rate <= 0 || len(samples) == 0 || !ttsWAVComplete(data) || len(samples) > rate*byeVoiceMaxMS/1000 {
			_ = os.Remove(p)
			continue
		}
		made := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), prefix), ".wav")
		return byeVoiceClip{text: text, voice: made, samples: samples, rate: rate}, true
	}
	return byeVoiceClip{}, false
}

// byeVoiceKeep holds clip as kind's line in memory (an empty clip: none).
func byeVoiceKeep(kind int, clip byeVoiceClip) {
	byeVoice.mu.Lock()
	byeVoice.clips[kind] = clip
	if clip.text != "" {
		byeVoice.cleared = false
	}
	byeVoice.mu.Unlock()
}

// byeVoiceMake has the device's voice say kind's line text, keeps it in its
// file (replacing that line's older files) and in memory. It reports whether
// the line was made and, when not, whether trying again could help.
func byeVoiceMake(parent context.Context, v ttsLocalVoice, voice string, kind int, text string) (made, again bool) {
	path := byeVoiceFile(kind, text, voice)
	if path == "" {
		return false, false
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("tts", "the name voice folder: %v", err)
		return false, true
	}
	// The words as Juka's voice reads any line (no emoji, tags or symbols a
	// name may carry).
	spoken := strings.Join(ttsPrepare(text), " ")
	if strings.TrimSpace(spoken) == "" {
		return false, false
	}
	ctx, cancel := context.WithTimeout(parent, byeVoiceMakeTimeout)
	defer cancel()
	began := time.Now()
	again = true
	samples, rate, err := ttsLocalSynth(ctx, v, spoken, "")
	if err == nil && rate > 0 {
		// Kept as it will play: no silence around it, soft ends.
		if samples = ttsTrim(samples, rate); len(samples) == 0 {
			err, again = errors.New("the voice made only silence"), false
		} else if len(samples) > rate*byeVoiceMaxMS/1000 {
			err, again = errors.New("the line came out too long to keep"), false
		}
	} else if err == nil {
		err = errors.New("the voice made no sound")
	}
	if err != nil {
		// Not when the app is quitting; a line that ran out of time is.
		if parent.Err() == nil {
			logWarn("tts", "the %s line could not be made (%s): %v", byeVoiceKindNames[kind], v.kind, err)
		}
		return false, again
	}
	ttsFade(samples, rate)
	pcm := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	// Written whole, then named: a half-written file is never read.
	tmp := path + ".part"
	if err := os.WriteFile(tmp, vkEncodeWAV(pcm, rate), 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("tts", "keeping the %s line: %v", byeVoiceKindNames[kind], err)
		return false, true
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("tts", "keeping the %s line: %v", byeVoiceKindNames[kind], err)
		return false, true
	}
	// The line's older files (another name, another voice) and anything an
	// interrupted write left go.
	if entries, err := os.ReadDir(dir); err == nil {
		mine := byeVoiceKindNames[kind] + "-"
		for _, e := range entries {
			if p := filepath.Join(dir, e.Name()); strings.HasPrefix(e.Name(), mine) && p != path {
				_ = os.Remove(p)
			}
		}
	}
	byeVoiceKeep(kind, byeVoiceClip{text: text, voice: voice, samples: samples, rate: rate})
	logInfo("tts", "the %s line was made by %s in %d ms (%d ms of sound)", byeVoiceKindNames[kind], v.kind,
		time.Since(began).Milliseconds(), int64(len(samples))*1000/int64(rate))
	return true, false
}

// byeVoiceForget drops the lines with the name: from memory and from the card.
// A folder that could not be removed is tried again at the next look.
func byeVoiceForget() {
	byeVoice.mu.Lock()
	if byeVoice.cleared {
		byeVoice.mu.Unlock()
		return
	}
	byeVoice.clips = [byeVoiceKinds]byeVoiceClip{}
	byeVoice.mu.Unlock()
	byeVoiceUncache(byeVoicePruned)
	byeVoicePruned = ""
	dir := byeVoiceDir()
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		logWarn("tts", "removing the name voice: %v", err)
		return
	}
	byeVoice.mu.Lock()
	byeVoice.cleared = true
	byeVoice.mu.Unlock()
}

// ── On the way out ──────────────────────────────────────────────────────

// byeVoiceLine is the goodbye ShowShutdown says, ready to play. Main thread.
type byeVoiceLine struct {
	samples []int16
	rate    int
	started bool
}

// byeVoiceForShutdown is the line to say on this exit to name, the name the
// card greets ("" for none: no line), or nil when there is none to say. It
// never makes speech: only a line already made for this very name plays. Main
// thread: it reads the config.
func byeVoiceForShutdown(c *Config, name string) *byeVoiceLine {
	if c == nil || name == "" || exitRequestedBySignal.Load() || quickPowerOffSilent() ||
		quietModeOn() || quietModeFromConfig(c) || !ttsModeAllows(c, ttsKindVoice) {
		return nil
	}
	kind := byeVoiceGoodbye
	if shutdownRestarting() {
		kind = byeVoiceSoon
	}
	text := byeVoiceText(kind, name)
	byeVoice.mu.Lock()
	clip := byeVoice.clips[kind]
	byeVoice.mu.Unlock()
	if clip.text != text {
		// The worker has not looked yet (a quit in the first moments): the
		// file is small enough to read now.
		if clip, _ = byeVoiceLoad(kind, text, ""); clip.text != text {
			return nil
		}
	}
	if len(clip.samples) == 0 || clip.rate <= 0 {
		return nil
	}
	return &byeVoiceLine{samples: clip.samples, rate: clip.rate}
}

// duration is how long the line plays.
func (l *byeVoiceLine) duration() time.Duration {
	if l == nil || l.rate <= 0 {
		return 0
	}
	return time.Duration(len(l.samples)) * time.Second / time.Duration(l.rate)
}

// fitHold is the held card's length from start: hold, lengthened so the line
// stays up while it is said, when that and the dissolve after it still fit
// the teardown's budget (deadline). A line that could not end inside the
// budget is not said at all: what runs after the card must fit the exit
// watchdog's grace too (exitguard.go).
func (l *byeVoiceLine) fitHold(hold float64, start, deadline time.Time) float64 {
	if l == nil || l.started {
		return hold
	}
	said := byeVoiceDelay + l.duration()
	if start.Add(said + byeVoiceOpenSlack).After(deadline) {
		l.started = true
		logInfo("tts", "the goodbye was skipped: the shutdown ran out of time")
		return hold
	}
	need := float64(said.Milliseconds()) + byeVoiceTailMS
	if need > byeVoiceHoldMaxMS {
		need = byeVoiceHoldMaxMS
	}
	if need <= hold || start.Add(time.Duration((need+shutdownDissolveMS)*float64(time.Millisecond))).After(deadline) {
		// Said over the usual hold.
		return hold
	}
	return need
}

// startAt starts the line once the card has been held byeVoiceDelay, if it can
// still end inside the teardown's budget (deadline).
func (l *byeVoiceLine) startAt(held time.Duration, deadline time.Time) {
	if l == nil || l.started || held < byeVoiceDelay {
		return
	}
	l.started = true
	if time.Now().Add(l.duration() + byeVoiceOpenSlack).After(deadline) {
		logInfo("tts", "the goodbye was skipped: the shutdown ran out of time")
		return
	}
	byeVoicePlay(l.samples, l.rate, deadline)
}

// byeVoicePlay plays samples on their own device, like a chime: it joins
// chimeState.playing, so shutdownChimeWait holds the exit until it has played.
// It gives up chimeGrace after the line's length, and never later than limit
// (the teardown's budget). It returns at once.
func byeVoicePlay(samples []int16, rate int, limit time.Time) {
	p := &chimePlayback{done: make(chan struct{})}
	length := time.Duration(len(samples)) * time.Second / time.Duration(rate)
	// From now, so a slow open counts against it (as chimeStart's).
	deadline := time.Now().Add(length + time.Duration(chimeGrace*float64(time.Second)))
	if deadline.After(limit) {
		deadline = limit
	}
	chimeState.mu.Lock()
	chimeState.playing = append(chimeState.playing, p)
	chimeState.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("tts", "the goodbye could not play: %v", r)
			}
			chimeState.mu.Lock()
			for i, q := range chimeState.playing {
				if q == p {
					chimeState.playing = append(chimeState.playing[:i], chimeState.playing[i+1:]...)
					break
				}
			}
			chimeState.mu.Unlock()
			close(p.done)
		}()
		byeVoiceOut(samples, rate, deadline)
	}()
}

// byeVoiceOut opens a mono device, queues the whole line, waits for it to play
// and closes the device, giving up at deadline. The LEDs are left alone: the
// teardown has already turned them off for good.
func byeVoiceOut(samples []int16, rate int, deadline time.Time) {
	desired := sdl.AudioSpec{
		Freq:     48000,
		Format:   sdl.AUDIO_S16LSB,
		Channels: 1,
		Samples:  1024,
	}
	var obtained sdl.AudioSpec
	dev, err := outOpen(&desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || dev == 0 {
		logDebug("tts", "no audio device for the goodbye: %v", err)
		return
	}
	defer outClose(dev)
	devRate := int(obtained.Freq)
	if devRate <= 0 {
		devRate = int(desired.Freq)
	}
	// A copy: ttsClipFor may fade a clip at the device's rate in place, and
	// the kept one is shared.
	clip := ttsClipFor(append([]int16(nil), samples...), rate, devRate)
	if len(clip) == 0 {
		return
	}
	pcm := make([]byte, len(clip)*2)
	for i, s := range clip {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	if err := outQueue(dev, pcm); err != nil {
		logDebug("tts", "queueing the goodbye: %v", err)
		return
	}
	outPause(dev, false)
	for outQueued(dev) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// The device's own buffer is still playing when the queue reads empty.
	if time.Until(deadline) > 0 {
		time.Sleep(min(60*time.Millisecond, time.Until(deadline)))
	}
}
