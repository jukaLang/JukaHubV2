package main

// narrate.go — Spoken navigation (Settings → Accessibility → Narration).
//
// With narration on, JukaHub says what is focused: the control's name, its
// value and what pressing A does ("Vibration, on. Switch with A."), so the
// interface can be used without seeing the screen. A new focus cuts the old
// speech off at once; a held direction is not read item by item — only the
// item the focus settles on (narrSettle) is spoken.
//
// How the focus is found. The screens that matter call one line each while
// they draw their focused control (narrate_hooks.go): Settings, the Start
// menu, the Games library, the shared confirmation sheet and the on-screen
// keyboard. Each describes the control with narrateMark; the top layer drawn
// in a frame wins (an open sheet over Settings, the Start menu over a page).
// Where no screen marked anything, the frame's end asks what is known anyway:
// Home's focused tile, the top bar's focused button, or the focus engine's
// focused element (narrSceneItem). What A does comes from the item, or from
// the footer the screen drew this frame (narrateFooterHints) — the hint layer
// already words it for a pad or a keyboard ("Open with Enter." on a PC).
//
// Who speaks. Narration uses the device's own voice only — Piper on the
// handheld, Windows' voice on a PC (narrate_voice.go) — never Groq's: every
// focused row would be a request, the free allowance would be spent on menu
// names, and a cloud round trip is too slow to follow the D-pad. Juka's
// answers come first: narration never interrupts one, waits for it to end
// and then says only what is focused by then, and a line of Juka's starting
// silences narration at once (narrFeed watches ttsBusy). Toasts come last:
// they wait for the focus to be read and are dropped when they pile up.
// Nothing is said while a game, an HTML5 game or the game assistant is in
// front, or while an alarm rings. Quiet Mode does not silence narration (a
// screen reader is how a blind user works the device, as VoiceOver keeps
// speaking in silent mode); the toasts Quiet Mode holds are not read.
//
// Turning it on without seeing the screen: hold L1 and R1 together for three
// seconds (the way Android's TalkBack is turned on with both volume keys), or
// press Ctrl+Alt+Z on a keyboard (ChromeVox's shortcut). A rising pair of
// tones confirms on, a falling pair off — earcons, because the voice may not
// be ready yet (narrate_settings.go downloads it then).

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// narrSettle is how long the focus must rest before it is read: a held
	// direction steps every 70 ms, a deliberate press is read at once.
	narrSettle = 120 * time.Millisecond
	// narrValueInputAge: a value that changes by itself (a download's
	// percentage, a clock) is read only this soon after a press.
	narrValueInputAge = 2500 * time.Millisecond
	// narrDetailPause is the quiet after an item before its description.
	narrDetailPause = 900 * time.Millisecond
	// narrToastMax is how many toasts may wait to be read.
	narrToastMax = 2
	// narrToastStale drops a toast that waited this long.
	narrToastStale = 8 * time.Second
	// narrMixGen is narration's id in tts_play.go's interface-sound mixer;
	// Juka's lines count up from 1 and never reach it.
	narrMixGen = uint64(1) << 62
)

// narrLayer orders the controls marked in one frame: a sheet drawn over a
// page is what the user is on.
type narrLayer int

const (
	narrLayerScene   narrLayer = iota + 1 // a scene's own rows (Settings)
	narrLayerPanel                        // a full-screen panel over a scene (Games)
	narrLayerOverlay                      // a sheet, the Start menu, the keyboard
)

// narrItem is one focused control as narration reads it.
type narrItem struct {
	context string // the screen or dialog; said when it changes
	intro   string // said once with a new context (a dialog's message)
	key     string // the control's identity within its context
	name    string
	value   string
	hint    string   // what A does, as a verb ("Open"); "" uses the footer's
	noHint  bool     // the control does nothing on A (a read-out)
	detail  string   // a longer description, said after a pause
	near    []string // the neighbours' names, made ahead while idle
}

// The settings, mirrored for every goroutine (narrBind keeps them current).
var (
	narrOn     atomic.Bool
	narrHints  atomic.Bool
	narrToasts atomic.Bool
	narrRate   atomic.Int32 // speaking rate in percent
	narrGender atomic.Int32 // Windows' voice: 0 default, 1 female, 2 male
	// narrFront is set while a game (or anything else that must not be
	// talked over) is in front; written once a frame.
	narrFront atomic.Bool
	// narrOrbUp is set while the voice orb is on screen: narration makes
	// nothing in the background then, so Juka's answer finds the voice free.
	narrOrbUp atomic.Bool
)

// narrFrame is what this frame's screens marked. Main thread only.
var narrFrame struct {
	have    bool
	layer   narrLayer
	item    narrItem
	footerA string // the A label of the footer drawn this frame
}

// narrBoundAt is when narrBind last read the config. Main thread only.
var narrBoundAt time.Time

// narrActive reports whether a hook should describe its control. Main
// thread; one atomic load when narration is off.
func narrActive() bool { return narrOn.Load() }

// narrateMark records the control a screen drew focused this frame. The top
// layer wins; within a layer, the one drawn last.
func narrateMark(layer narrLayer, it narrItem) {
	if narrFrame.have && layer < narrFrame.layer {
		return
	}
	narrFrame.have, narrFrame.layer, narrFrame.item = true, layer, it
}

// narrateFooterHints is the footer planner's hook: it keeps what A does on
// this frame's screen. Main thread; nothing when narration is off.
func narrateFooterHints(hints []FooterHint) {
	if !narrOn.Load() {
		return
	}
	for _, h := range hints {
		first, _, _ := strings.Cut(h.Button, "/") // "A / Enter"
		if strings.TrimSpace(first) == "A" && strings.TrimSpace(h.Label) != "" {
			narrFrame.footerA = h.Label
			return
		}
	}
}

// narrateEndFrame is the main loop's hook after the frame is drawn: it reads
// the settings now and then, times the L1 + R1 hold, and hands the focused
// control to the narrator when it changed. Main thread.
func narrateEndFrame(c *Config) {
	if c != nil && time.Since(narrBoundAt) > time.Second {
		narrBoundAt = time.Now()
		narrBind(c)
	}
	narrChordTick(c)
	frame := narrFrame
	narrFrame.have, narrFrame.item, narrFrame.footerA = false, narrItem{}, ""
	if !narrOn.Load() {
		return
	}
	front := gameScreenSuspended() || html5GameActive() || gameAssistOverlayActive() || alarmFiringActive()
	wasFront := narrFront.Swap(front)
	narrOrbUp.Store(voiceOrb.phase != orbHidden)
	if front {
		if !wasFront {
			// Back in the menus, the focus is read afresh.
			narrLastNoted = narrItem{}
			narrDropPending()
		}
		return
	}
	it, ok := frame.item, frame.have
	if !ok || frame.layer == narrLayerScene {
		if chrome, cok := narrChromeItem(c); cok {
			it, ok = chrome, true
		}
	}
	if !ok {
		it, ok = narrSceneItem(c)
	}
	if !ok || strings.TrimSpace(it.name+it.value+it.intro) == "" {
		return
	}
	if it.hint == "" && !it.noHint {
		it.hint = frame.footerA
	}
	if narrSameItem(it, narrLastNoted) {
		return
	}
	last := narrLastNoted
	narrLastNoted = it
	if it.context == last.context && it.key == last.key && it.name == last.name &&
		userInputAge(time.Now()) > narrValueInputAge {
		// A value that moved by itself (a percentage, a clock): not worth
		// cutting off what is being said.
		return
	}
	narrAheadNear(it.near)
	narrSubmitFocus(it)
}

// narrLastNoted is the item last handed over. Main thread only.
var narrLastNoted narrItem

// narrSameItem compares what is said of two items.
func narrSameItem(a, b narrItem) bool {
	return a.context == b.context && a.key == b.key && a.name == b.name && a.value == b.value &&
		a.hint == b.hint && a.intro == b.intro
}

// ── The narrator ─────────────────────────────────────────────────────────

// narrReq is one focused item waiting to be read.
type narrReq struct {
	item narrItem
	at   time.Time
}

// narrToast is one toast waiting to be read.
type narrToast struct {
	text string
	at   time.Time
}

// narr is the narrator's queue. Any goroutine, under mu.
var narr struct {
	mu      sync.Mutex
	focus   *narrReq           // the latest focus not yet read
	toasts  []narrToast        // toasts waiting
	cancel  context.CancelFunc // ends what is being said now
	saying  bool               // a focus (not a toast) is being said
	started bool               // the narrator goroutine runs
	wake    chan struct{}
	// greet is said before the next focus ("Narration on."), or alone when
	// no focus follows within narrGreetWait of greetAt.
	greet   string
	greetAt time.Time
	// fresh forgets what was said: the next focus is read whole.
	fresh bool
}

// narrGreetWait is how long a greeting waits for a focus to go with.
const narrGreetWait = 400 * time.Millisecond

// narrFed is set while narration has sound in the UI device's queue. Under
// audioMutex.
var narrFed bool

// narrSubmitFocus makes it the item to read, cutting off what is being said.
// Main thread.
func narrSubmitFocus(it narrItem) {
	narr.mu.Lock()
	narr.focus = &narrReq{item: it, at: time.Now()}
	if narr.cancel != nil {
		narr.cancel()
		narr.cancel = nil
	}
	narrStartLocked()
	narr.mu.Unlock()
	narrSilence()
	narrWake()
}

// narrateToast is the toast's hook: the message is read after the focus,
// when Settings reads toasts. Any goroutine.
func narrateToast(msg string, kind ToastKind) {
	if !narrOn.Load() || !narrToasts.Load() || narrFront.Load() {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	narr.mu.Lock()
	if n := len(narr.toasts); n > 0 && narr.toasts[n-1].text == msg {
		narr.mu.Unlock()
		return
	}
	narr.toasts = append(narr.toasts, narrToast{text: msg, at: time.Now()})
	if len(narr.toasts) > narrToastMax {
		narr.toasts = append(narr.toasts[:0], narr.toasts[len(narr.toasts)-narrToastMax:]...)
	}
	narrStartLocked()
	narr.mu.Unlock()
	narrWake()
}

// narrDropPending forgets what waits to be read and stops what is said.
func narrDropPending() {
	narr.mu.Lock()
	narr.focus = nil
	narr.toasts = narr.toasts[:0]
	cancel := narr.cancel
	narr.cancel = nil
	narr.mu.Unlock()
	if cancel != nil {
		cancel()
		narrSilence()
	}
}

// narrStartLocked starts the narrator goroutine once. narr.mu is held.
func narrStartLocked() {
	if narr.started {
		return
	}
	narr.started = true
	narr.wake = make(chan struct{}, 1)
	go narrLoopFn()
}

// narrLoopFn is narrLoop, set in init: the narrator starts from ShowToast,
// which package-level initialisers call, and narrLoop reaches Settings'
// catalogue — named directly, that would be an initialisation cycle.
var narrLoopFn func()

func init() { narrLoopFn = narrLoop }

// narrWake rouses the narrator.
func narrWake() {
	narr.mu.Lock()
	wake := narr.wake
	narr.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// narrSleep waits d, or less when something new arrives.
func narrSleep(d time.Duration) {
	narr.mu.Lock()
	wake := narr.wake
	narr.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-wake:
	case <-t.C:
	}
}

// narrHeld reports why nothing may be said now: hold is true while Juka
// speaks (wait for the line to end), drop while a game is in front or
// narration is off (forget what waits).
func narrHeld() (hold, drop bool) {
	if !narrOn.Load() || narrFront.Load() || gameScreenSuspended() || alarmFiringActive() {
		return false, true
	}
	return ttsBusy.Load(), false
}

// narrLoop is the narrator: it reads the settled focus, then the toasts,
// then makes the neighbours' names ahead. One goroutine for the run.
func narrLoop() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("narrate", "narration stopped: %v", r)
			narr.mu.Lock()
			narr.started = false
			narr.mu.Unlock()
		}
	}()
	var spoken narrItem
	for {
		narr.mu.Lock()
		f := narr.focus
		var toast *narrToast
		if f == nil && len(narr.toasts) > 0 {
			t := narr.toasts[0]
			toast = &t
		}
		wake := narr.wake
		greet, greetAt := narr.greet, narr.greetAt
		if narr.fresh {
			narr.fresh = false
			spoken = narrItem{}
		}
		narr.mu.Unlock()

		if f == nil && greet != "" {
			// No focus came with the greeting: it is said alone.
			if wait := narrGreetWait - time.Since(greetAt); wait > 0 {
				narrSleep(wait)
				continue
			}
			if hold, drop := narrHeld(); hold {
				narrSleep(60 * time.Millisecond)
				continue
			} else if drop {
				narr.mu.Lock()
				narr.greet = ""
				narr.mu.Unlock()
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			narr.mu.Lock()
			narr.greet = ""
			narr.cancel = cancel
			narr.mu.Unlock()
			narrSay(ctx, []narrPart{{text: greet, gap: 0}})
			cancel()
			continue
		}
		if f == nil && toast == nil {
			if narrBackground(&spoken) {
				continue
			}
			<-wake
			continue
		}
		if hold, drop := narrHeld(); drop {
			narr.mu.Lock()
			narr.focus = nil
			narr.toasts = narr.toasts[:0]
			narr.mu.Unlock()
			spoken = narrItem{}
			continue
		} else if hold {
			narrSleep(60 * time.Millisecond)
			continue
		}
		if f != nil {
			if wait := narrSettle - time.Since(f.at); wait > 0 {
				narrSleep(wait)
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			narr.mu.Lock()
			if narr.focus != f {
				narr.mu.Unlock()
				cancel()
				continue
			}
			narr.focus = nil
			narr.cancel = cancel
			narr.saying = true
			greet = narr.greet
			narr.greet = ""
			narr.mu.Unlock()
			said, yielded := narrSayItem(ctx, f.item, spoken, greet)
			narr.mu.Lock()
			narr.saying = false
			if yielded && narr.focus == nil {
				// Juka began to speak: the item is read again after.
				narr.focus = &narrReq{item: f.item, at: time.Now()}
			}
			narr.mu.Unlock()
			cancel()
			if said {
				fresh := f.item.key != spoken.key || f.item.context != spoken.context
				spoken = f.item
				if fresh {
					narrSayDetail(f.item)
				}
			}
			continue
		}
		narr.mu.Lock()
		if len(narr.toasts) == 0 || narr.toasts[0].text != toast.text {
			narr.mu.Unlock()
			continue
		}
		narr.toasts = narr.toasts[1:]
		ctx, cancel := context.WithCancel(context.Background())
		narr.cancel = cancel
		narr.mu.Unlock()
		if time.Since(toast.at) <= narrToastStale {
			narrSay(ctx, []narrPart{{text: toast.text, gap: 200}})
		}
		cancel()
	}
}

// narrSayItem says what changed about it since spoken: everything for a new
// screen or control, the value alone when only it changed. said is false
// when it was cut off; yielded when Juka's voice took over.
func narrSayItem(ctx context.Context, it, spoken narrItem, greet string) (said, yielded bool) {
	parts := narrPhrases(it, spoken, narrHints.Load(), userInputAge(time.Now()) <= narrValueInputAge)
	if greet != "" {
		parts = append([]narrPart{{text: greet, gap: 250}}, parts...)
	}
	if len(parts) == 0 {
		return true, false
	}
	ttsKeepWarm()
	ttsTalkTyped.Store(true) // the voice stays loaded while the user browses
	ok, yielded := narrSay(ctx, parts)
	return ok, yielded
}

// narrSayDetail says an item's description once the user has rested on it.
func narrSayDetail(it narrItem) {
	if !narrHints.Load() || strings.TrimSpace(it.detail) == "" {
		return
	}
	deadline := time.Now().Add(narrDetailPause)
	for time.Now().Before(deadline) {
		narr.mu.Lock()
		busy := narr.focus != nil || len(narr.toasts) > 0
		narr.mu.Unlock()
		if busy {
			return
		}
		narrSleep(time.Until(deadline))
	}
	if hold, drop := narrHeld(); hold || drop {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	narr.mu.Lock()
	if narr.focus != nil {
		narr.mu.Unlock()
		cancel()
		return
	}
	narr.cancel = cancel
	narr.mu.Unlock()
	narrSay(ctx, []narrPart{{text: narrDetailText(it.detail), gap: 0, long: true}})
	cancel()
}

// narrDetailText is a description cut to its first two sentences.
func narrDetailText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	n, cut := 0, len(s)
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' {
			n++
			if n == 2 {
				cut = i + 1
				break
			}
		}
	}
	if cut > 220 {
		cut = 220
		if sp := strings.LastIndexByte(s[:cut], ' '); sp > 80 {
			cut = sp
		}
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}
	return s[:cut]
}

// ── Saying it ────────────────────────────────────────────────────────────

// narrPart is one piece of a line: made and kept on its own, so the words
// that come back ("on", "Open with A.") are made once.
type narrPart struct {
	text string
	gap  int  // ms of quiet after it, at normal speed
	long bool // a description: not kept on the card
}

// narrPhrases words it against spoken.
func narrPhrases(it, spoken narrItem, hints, recentInput bool) []narrPart {
	var parts []narrPart
	add := func(text string, gap int) {
		if t := strings.TrimSpace(text); t != "" {
			parts = append(parts, narrPart{text: t, gap: gap})
		}
	}
	full := func() {
		add(it.name, 90)
		add(it.value, 140)
		if hints && !it.noHint {
			add(narrHintPhrase(it.hint), 0)
		}
	}
	switch {
	case it.context != spoken.context:
		add(it.context, 220)
		if it.intro != "" {
			parts = append(parts, narrPart{text: narrDetailText(it.intro), gap: 260, long: true})
		}
		full()
	case it.key != spoken.key || it.name != spoken.name:
		full()
	case it.value != spoken.value && recentInput:
		add(it.value, 0)
	case it.intro != spoken.intro && it.intro != "":
		parts = append(parts, narrPart{text: narrDetailText(it.intro), gap: 0, long: true})
	}
	return parts
}

// narrHintPhrase words what A does: "Open with A." — the letter at the end
// of a sentence, where Piper's phonemiser says its name (inside a phrase a
// lone "A" is read as the article). A keyboard user hears the key the hint
// layer names ("Open with Enter.").
func narrHintPhrase(verb string) string {
	verb = strings.TrimSpace(verb)
	if verb == "" {
		return ""
	}
	if strings.HasSuffix(verb, ".") {
		// Already a sentence ("Left and right change it.").
		return narrSentenceKeys(verb)
	}
	key := narrSpokenKey(hintKeyFor("A", verb))
	if key == "" {
		return ""
	}
	return verb + " with " + key + "."
}

// narrSentenceKeys puts the key the hint layer names in place of a sentence's
// closing "with A.".
func narrSentenceKeys(s string) string {
	if rest, ok := strings.CutSuffix(s, " with A."); ok {
		if key := narrSpokenKey(hintKeyFor("A", rest)); key != "" {
			return rest + " with " + key + "."
		}
	}
	return s
}

// narrSpokenKey is a button or key name as it is said.
func narrSpokenKey(k string) string {
	k = strings.TrimSpace(k)
	switch k {
	case "↵", "⏎", "Return":
		return "Enter"
	case "Esc":
		return "Escape"
	case "␣":
		return "Space"
	}
	return k
}

// narrSay plays the parts in order, each made (or found) while the one before
// it plays. It reports whether all of it was heard, and whether Juka's voice
// took the speaker.
func narrSay(ctx context.Context, parts []narrPart) (bool, bool) {
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable || len(parts) == 0 {
		return false, false
	}
	devRate := int(audioSampleRate)
	speed := int(narrRate.Load())
	clips := make(chan []int16, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var failed atomic.Bool
	go func() {
		defer close(clips)
		defer func() {
			if r := recover(); r != nil {
				failed.Store(true)
				logWarn("narrate", "a line could not be made: %v", r)
			}
		}()
		for _, p := range parts {
			if ctx.Err() != nil {
				return
			}
			samples, rate, err := narrClip(ctx, p.text, !p.long)
			if err != nil {
				failed.Store(true)
				if ctx.Err() == nil {
					narrNoVoice(err)
				}
				return
			}
			if len(samples) == 0 || rate <= 0 {
				continue
			}
			src := samples
			if rate == devRate {
				// ttsClipFor fades a clip at the device's rate in place: the
				// kept one is not to be changed.
				src = append([]int16(nil), samples...)
			}
			clip := ttsClipFor(src, rate, devRate)
			if clip == nil {
				continue
			}
			if p.gap > 0 {
				gap := devRate * p.gap * 100 / (1000 * max(speed, 50))
				clip = append(clip, make([]int16, gap)...)
			}
			select {
			case clips <- clip:
			case <-ctx.Done():
				return
			}
		}
	}()
	all, yielded := narrFeed(ctx, clips, devRate)
	return all && !failed.Load(), yielded
}

// narrFeed plays clips through the UI sound device a slice at a time, like
// Juka's feeder (tts_play.go): the interface's taps are mixed in rather than
// queued behind it, and a stop drops what is queued. It gives way the moment
// a line of Juka's is posted.
func narrFeed(ctx context.Context, clips <-chan []int16, rate int) (all, yielded bool) {
	slice := rate * ttsSliceMs / 1000
	if slice < 64 {
		slice = 64
	}
	ahead := uint32(rate * 2 * ttsAheadMs / 1000)
	buf := make([]byte, slice*2)
	acc := make([]int32, slice)
	var cur []int16
	pos := 0
	moved := time.Now()
	if ttsBusy.Load() {
		// Juka's line may hold the interface sounds' mixer already.
		return false, true
	}
	ttsMixOpen(narrMixGen)
	defer func() {
		ttsMixClose(narrMixGen)
		if !all {
			narrSilence()
		} else {
			audioMutex.Lock()
			narrFed = false
			audioMutex.Unlock()
		}
	}()
	for {
		if ctx.Err() != nil || narrFront.Load() || gameScreenSuspended() || alarmFiringActive() {
			return false, false
		}
		if ttsBusy.Load() {
			return false, true
		}
		if pos >= len(cur) {
			select {
			case clip, ok := <-clips:
				if !ok {
					if ctx.Err() != nil {
						return false, false
					}
					narrDrain(ctx)
					return true, false
				}
				cur, pos = clip, 0
				moved = time.Now()
				continue
			default:
			}
			ttsMixFlush(narrMixGen)
			time.Sleep(ttsPoll)
			continue
		}
		audioMutex.Lock()
		queued := outQueued(audioDev)
		audioMutex.Unlock()
		if queued >= ahead {
			if time.Since(moved) > ttsStallMax {
				logWarn("narrate", "the speaker stopped taking narration")
				return false, false
			}
			time.Sleep(ttsPoll)
			continue
		}
		n := len(cur) - pos
		if n > slice {
			n = slice
		}
		for i := 0; i < n; i++ {
			acc[i] = int32(cur[pos+i])
		}
		ttsMixTake(narrMixGen, acc[:n])
		for i := 0; i < n; i++ {
			v := ttsClampS16(acc[i])
			buf[2*i] = byte(v)
			buf[2*i+1] = byte(uint16(v) >> 8)
		}
		audioMutex.Lock()
		var err error
		if ctx.Err() != nil {
			// Stopped meanwhile: whoever stopped it has silenced the queue and
			// may have queued a tone after (narration off) — nothing goes in now.
			err = ctx.Err()
		} else if ttsFed.Load() {
			// Juka's voice is in the queue: narration has no place there.
			err = errNarrYield
		} else if err = outQueue(audioDev, buf[:n*2]); err == nil {
			narrFed = true
		}
		audioMutex.Unlock()
		if err == errNarrYield {
			return false, true
		}
		if err != nil {
			return false, false
		}
		publishAudioLevel(buf[:n*2], rate, 1)
		pos += n
		moved = time.Now()
	}
}

// narrDrain waits (bounded) for the last slices to play, so a toast queued
// next does not start under them.
func narrDrain(ctx context.Context) {
	deadline := time.Now().Add(ttsDrainMax)
	for time.Now().Before(deadline) && ctx.Err() == nil && !ttsBusy.Load() {
		audioMutex.Lock()
		left := outQueued(audioDev)
		audioMutex.Unlock()
		if left == 0 {
			return
		}
		time.Sleep(ttsPoll)
	}
}

// narrSilence drops what narration still has queued — unless Juka's voice is
// queued there too, which is not narration's to clear. Any goroutine.
func narrSilence() {
	audioMutex.Lock()
	defer audioMutex.Unlock()
	if narrFed && !ttsFed.Load() && audioAvailable {
		outClear(audioDev)
	}
	narrFed = false
	ttsMixCloseLocked(narrMixGen)
}

// ── Earcons ──────────────────────────────────────────────────────────────

// narrEarcon is a short tone figure that needs no voice.
type narrEarcon int

const (
	narrEarconOn   narrEarcon = iota // rising: narration on
	narrEarconOff                    // falling: narration off
	narrEarconWait                   // one soft blip: heard, the voice is not ready
)

// narrPlayEarcon queues an earcon at the device's rate, whatever the
// "Interface sounds" switch says: it is how a blind user knows the chord
// worked. Any goroutine.
func narrPlayEarcon(kind narrEarcon) {
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable || alarmFiringActive() || gameScreenSuspended() {
		return
	}
	rate := int(audioSampleRate)
	var notes []float64
	switch kind {
	case narrEarconOn:
		notes = []float64{660, 990}
	case narrEarconOff:
		notes = []float64{990, 660}
	default:
		notes = []float64{520}
	}
	pcm := narrTones(rate, notes)
	audioMutex.Lock()
	defer audioMutex.Unlock()
	if ttsMixUISound(pcm) {
		return
	}
	_ = outQueue(audioDev, pcm)
}

// narrTones renders notes one after another as soft sine blips (S16 LE).
// Pure.
func narrTones(rate int, notes []float64) []byte {
	if rate <= 0 {
		return nil
	}
	note := rate * 90 / 1000
	gap := rate * 25 / 1000
	n := len(notes)*(note+gap) + gap
	out := make([]byte, n*2)
	for k, hz := range notes {
		start := k*(note+gap) + gap
		phase := 0.0
		for i := 0; i < note; i++ {
			phase += 2 * math.Pi * hz / float64(rate)
			env := 1.0
			if a := rate * 6 / 1000; i < a {
				env = float64(i) / float64(a)
			}
			if r := note - i; r < rate*30/1000 {
				env *= float64(r) / float64(rate*30/1000)
			}
			v := int16(9000 * env * math.Sin(phase))
			j := 2 * (start + i)
			out[j] = byte(v)
			out[j+1] = byte(uint16(v) >> 8)
		}
	}
	return out
}

// ── The L1 + R1 hold ─────────────────────────────────────────────────────

// narrChordHold is how long L1 and R1 are held together to switch narration.
const narrChordHold = 3 * time.Second

// narrChord times the hold. Main thread only.
var narrChord struct {
	l1, r1 bool
	since  time.Time      // both went down (zero: not both)
	fired  bool           // switched; waits for a release
	which  sdl.JoystickID // the pad whose press made the pair
}

// narrateChordButton is the main loop's hook for every pad press and release:
// it notes L1 and R1 for the hold and never takes the event. Main thread.
func narrateChordButton(e *sdl.ControllerButtonEvent) {
	if e == nil || e.Which == padRepeatWhich || e.Which == inmapSynthWhich {
		return
	}
	down := e.Type == sdl.CONTROLLERBUTTONDOWN
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		narrChord.l1 = down
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		narrChord.r1 = down
	default:
		return
	}
	switch {
	case narrChord.l1 && narrChord.r1:
		if narrChord.since.IsZero() {
			narrChord.since, narrChord.which = time.Now(), e.Which
		}
	default:
		narrChord.since = time.Time{}
		if !narrChord.l1 && !narrChord.r1 {
			narrChord.fired = false
		}
	}
}

// narrChordTick switches narration once the hold is long enough.
func narrChordTick(c *Config) {
	if narrChord.since.IsZero() || narrChord.fired || time.Since(narrChord.since) < narrChordHold {
		return
	}
	narrChord.fired = true
	// A release taken before the main loop's hook (an alarm, the game
	// assistant, an HTML5 game, the voice orb, a wake press) leaves a button
	// marked down: the pad itself says whether both are still held.
	if pad := sdl.GameControllerFromInstanceID(narrChord.which); pad != nil {
		l1 := pad.Button(sdl.CONTROLLER_BUTTON_LEFTSHOULDER) != 0
		r1 := pad.Button(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER) != 0
		if !l1 || !r1 {
			narrChord.l1, narrChord.r1, narrChord.since, narrChord.fired = l1, r1, time.Time{}, false
			return
		}
	}
	if gameScreenSuspended() || html5GameActive() || gameAssistOverlayActive() || alarmFiringActive() {
		return
	}
	narrToggle(c)
}

// narrateKeyChord is the global keys' hook: Ctrl+Alt+Z switches narration.
func narrateKeyChord(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || e.Repeat != 0 || e.Keysym.Sym != sdl.K_z {
		return false
	}
	// The left Alt only: AltGr arrives as Ctrl + right Alt on Windows, and
	// AltGr+Z types a letter on some layouts (ż on a Polish keyboard).
	mod := e.Keysym.Mod
	if mod&sdl.KMOD_CTRL == 0 || mod&sdl.KMOD_LALT == 0 || mod&sdl.KMOD_RALT != 0 {
		return false
	}
	narrToggle(c)
	return true
}

// errNarrYield: Juka's voice holds the speaker.
var errNarrYield = errors.New("Juka is speaking")
