package main

// bootwork.go — real startup work packed into the splash window.
//
// The splash occupies the better part of a second, and for all of that time
// the process used to do nothing but draw: the placeholder texture, the gamepad
// probe, the keyboard map, the thumbnail workers and the focus graph all ran
// after the animation had finished. None of them are needed to draw the
// animation, so they are queued here and executed between splash frames
// instead. The boot ends in the same place, but the first real frame arrives
// earlier by the cost of that work.
//
// Two kinds of step keep it honest:
//
//   - Queued steps (bootEnqueue) run on the render thread, in the order they
//     were enqueued, because most of them touch SDL objects that only that
//     thread may use. A step that overruns its frame is logged; the animation
//     drops a frame.
//   - Checks that only touch the disk or the network (bootEnqueueAsync) run on
//     their own goroutine from the moment they are queued, in parallel with
//     each other and with the queue: the card probe, the tool inventory and the
//     settings check never cost a frame. Their row reports when they finish;
//     a download one of them starts keeps going after the splash and shows on
//     the activity strip.
//
// bootWorkDrain runs whatever is left of the queue once the splash is over, so
// a warm start (no splash), a skipped splash and a card with no budget left all
// still boot through exactly the same setup. Async checks are never waited for
// past the splash: the UI does not depend on them.

import (
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// bootStep is one unit of startup work safe to run on the render thread. label
// is the row the glass boot card shows; name stays the short id the log uses;
// index is the step's row.
type bootStep struct {
	name  string
	label string
	fn    func()
	index int
}

// bootWork is the queue and, beside it, the progress the boot card reports. The
// counters live with the queue because they only mean anything together: a step
// that is popped is a step that is about to be reported as running.
var bootWork struct {
	mu       sync.Mutex
	pending  []bootStep
	labels   []string
	states   []stageState
	details  []string
	done     int
	failures int
	// current is the row of the queued step running right now; hasCurrent is
	// false between steps.
	current    int
	hasCurrent bool
	// async counts the checks still running on their own goroutines.
	async int
	// version bumps on every change the card shows, so the renderer can keep
	// its formatted strings until something actually changed.
	version uint64
}

// bootEnqueue queues a step to run during (or just after) the splash. A nil
// function is ignored so a feature that is compiled out does not leave a hole
// in the queue.
func bootEnqueue(name string, fn func()) {
	if fn == nil {
		return
	}
	label := bootStepLabel(name)
	bootWork.mu.Lock()
	index := len(bootWork.labels)
	bootWork.pending = append(bootWork.pending, bootStep{name: name, label: label, fn: fn, index: index})
	bootWork.labels = append(bootWork.labels, label)
	bootWork.states = append(bootWork.states, stagePending)
	bootWork.details = append(bootWork.details, "")
	bootWork.version++
	bootWork.mu.Unlock()
}

// bootEnqueueAsync starts a check on its own goroutine now and gives it a row
// on the card. fn reports its result line through note, which may be called
// any number of times; the row turns done when fn returns, failed when it
// returns an error or panics. Use it only for work that touches no SDL object.
func bootEnqueueAsync(name string, fn func(note func(string)) error) {
	if fn == nil {
		return
	}
	label := bootStepLabel(name)
	bootWork.mu.Lock()
	index := len(bootWork.labels)
	bootWork.labels = append(bootWork.labels, label)
	bootWork.states = append(bootWork.states, stageActive)
	bootWork.details = append(bootWork.details, "")
	bootWork.async++
	bootWork.version++
	bootWork.mu.Unlock()

	note := func(detail string) { bootWorkSetDetail(index, detail) }
	go func() {
		started := time.Now()
		failed := runBootCheck(name, fn, note)
		if took := time.Since(started); took > 250*time.Millisecond {
			logInfo("startup", "boot check %s took %dms", name, took.Milliseconds())
		}
		bootWork.mu.Lock()
		if index < len(bootWork.states) {
			if failed {
				bootWork.states[index] = stageFailed
				bootWork.failures++
			} else {
				bootWork.states[index] = stageDone
			}
			bootWork.done++
		}
		if bootWork.async > 0 {
			bootWork.async--
		}
		bootWork.version++
		bootWork.mu.Unlock()
	}()
}

// runBootCheck runs one async check and turns an error or a panic into a
// failed row.
func runBootCheck(name string, fn func(note func(string)) error, note func(string)) (failed bool) {
	defer func() {
		if r := recover(); r != nil {
			logError("startup", "boot check %s panicked: %v", name, r)
			failed = true
		}
	}()
	if err := fn(note); err != nil {
		logWarn("startup", "boot check %s: %v", name, err)
		return true
	}
	return false
}

// bootWorkSetDetail records the result line of one row.
func bootWorkSetDetail(index int, detail string) {
	bootWork.mu.Lock()
	if index >= 0 && index < len(bootWork.details) && bootWork.details[index] != detail {
		bootWork.details[index] = detail
		bootWork.version++
	}
	bootWork.mu.Unlock()
}

// bootWorkSettled reports whether the queue is empty and every async check has
// reported, which is when the splash may hand over without cutting a row off.
func bootWorkSettled() bool {
	bootWork.mu.Lock()
	defer bootWork.mu.Unlock()
	return len(bootWork.pending) == 0 && bootWork.async == 0
}

// runOneBootStep pops and runs the oldest queued step. It reports false when
// the queue is empty.
func runOneBootStep() bool {
	bootWork.mu.Lock()
	if len(bootWork.pending) == 0 {
		bootWork.mu.Unlock()
		return false
	}
	step := bootWork.pending[0]
	bootWork.pending[0] = bootStep{} // drop the closure before the slice moves
	bootWork.pending = bootWork.pending[1:]
	index := step.index
	if index >= 0 && index < len(bootWork.states) {
		bootWork.states[index] = stageActive
	}
	bootWork.current = index
	bootWork.hasCurrent = true
	bootWork.version++
	bootWork.mu.Unlock()

	started := time.Now()
	failed := runBootStep(step)
	took := time.Since(started)
	// Anything long enough to be felt is worth a line in the boot log: it is
	// the difference between "the splash stuttered" and "this step is slow".
	if took > 20*time.Millisecond {
		logInfo("startup", "boot step %s took %dms", step.name, took.Milliseconds())
	}

	bootWork.mu.Lock()
	if index >= 0 && index < len(bootWork.states) {
		if failed {
			bootWork.states[index] = stageFailed
			bootWork.failures++
		} else {
			bootWork.states[index] = stageDone
		}
	}
	bootWork.done++
	bootWork.hasCurrent = false
	bootWork.version++
	bootWork.mu.Unlock()
	return true
}

// runBootStep runs one step and turns a panic into a failed row. Boot work is
// best-effort setup: one broken step must not take the launch down, and the
// card should say which one it was.
func runBootStep(step bootStep) (failed bool) {
	defer func() {
		if r := recover(); r != nil {
			logError("startup", "boot step %s panicked: %v", step.name, r)
			failed = true
		}
	}()
	step.fn()
	return false
}

// bootStepNote records the result line the running queued step wants on its
// row (the card space a probe found, how many tools are ready). Async checks
// use the note function they were handed instead.
func bootStepNote(detail string) {
	bootWork.mu.Lock()
	index, ok := bootWork.current, bootWork.hasCurrent
	bootWork.mu.Unlock()
	if ok {
		bootWorkSetDetail(index, detail)
	}
}

// bootStepLabel is the human label for a queued step. The queue's ids are short
// log tokens; the card should read like a sentence.
func bootStepLabel(name string) string {
	if label, ok := bootStepLabels[name]; ok {
		return label
	}
	name = strings.TrimSpace(strings.ReplaceAll(name, "-", " "))
	if name == "" {
		return "Starting"
	}
	runes := []rune(name)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

var bootStepLabels = map[string]string{
	"settings":            "Checking your settings",
	"storage":             "Checking the SD card",
	"tools":               "Checking helper tools",
	"cores":               "Checking emulator cores",
	"alarm-volume":        "Restoring the speaker",
	"sounds":              "Tuning the sounds",
	"placeholder-texture": "Preparing artwork",
	"gamepad":             "Reading the gamepad",
	"haptics":             "Waking the vibration motor",
	"keyboard":            "Loading the keyboard",
	"thumbnail-workers":   "Starting artwork prefetch",
	"focus-graph":         "Building the home grid",
	"scene-warm":          "Warming the first screen",
	"volume-buttons":      "Mapping the volume keys",
}

// bootWorkSnapshotInto fills dst with the card's view of the queue: every row
// with its state and result, the one to headline, and how many finished or
// failed. dst.Items is reused, so the splash builds its snapshot every frame
// without allocating once the slice has grown to the queue's length.
//
// The headline row is the queued step running now; between queued steps it is
// a running async check, and with neither, the next queued step — that is what
// the frame is about to pay for. With nothing left the status line falls back
// to "Ready".
func bootWorkSnapshotInto(dst *stageSnapshot) {
	bootWork.mu.Lock()
	defer bootWork.mu.Unlock()

	dst.Title = "Starting up"
	dst.Done = bootWork.done
	dst.Total = len(bootWork.labels)
	dst.Active = -1
	dst.Failed = bootWork.failures
	dst.Current = ""
	dst.Detail = ""
	dst.Items = dst.Items[:0]
	firstActive, firstPending := -1, -1
	for i, label := range bootWork.labels {
		state := stagePending
		if i < len(bootWork.states) {
			state = bootWork.states[i]
		}
		detail := ""
		if i < len(bootWork.details) {
			detail = bootWork.details[i]
		}
		dst.Items = append(dst.Items, stageItem{Label: label, Detail: detail, State: state})
		switch {
		case state == stageActive && firstActive < 0:
			firstActive = i
		case state == stagePending && firstPending < 0:
			firstPending = i
		}
	}
	switch {
	case bootWork.hasCurrent && bootWork.current >= 0 && bootWork.current < len(dst.Items):
		dst.Active = bootWork.current
	case firstActive >= 0:
		dst.Active = firstActive
	case firstPending >= 0 && dst.Done < dst.Total:
		dst.Items[firstPending].State = stageActive
		dst.Active = firstPending
	}
	if dst.Active >= 0 {
		dst.Current = dst.Items[dst.Active].Label
		dst.Detail = dst.Items[dst.Active].Detail
	}
}

// bootWorkTick spends the frame's spare time on queued steps. At least one step
// always runs, so a single slow step cannot starve the queue while the splash
// is up: the next frame continues from where it stopped.
func bootWorkTick(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for runOneBootStep() {
		// Not After: on a coarse timer (Windows) two reads can be equal, and a
		// spent budget would then keep draining the queue in one tick.
		if !time.Now().Before(deadline) {
			return
		}
	}
}

// bootWorkDrain runs everything still queued. The caller invokes it after the
// splash (or immediately when no splash was drawn), so the UI never starts on
// half-applied setup.
func bootWorkDrain() {
	for runOneBootStep() {
	}
}

// bootHandoff keeps the warm render of the first screen for the splash's last
// frames: the splash cross-fades into it, so the hand-over to Home is one
// continuous picture instead of a cut. keep is raised by the splash for the
// time it can use the texture; a warm-up outside that window (no splash, a
// skipped one, the tests) destroys its target as before.
var bootHandoff struct {
	keep     bool
	tex      *sdl.Texture
	renderer *sdl.Renderer
}

// bootHandoffRelease destroys a kept hand-off texture. It must run on the
// render thread while its renderer is alive.
func bootHandoffRelease() {
	if bootHandoff.tex != nil {
		_ = bootHandoff.tex.Destroy()
	}
	bootHandoff.tex = nil
	bootHandoff.renderer = nil
	bootHandoff.keep = false
}

// warmSceneCaches draws the scene the first frame will show once into an
// offscreen texture while the splash is still up. Every label, shape, icon and
// image it touches lands in the renderer's caches as it is drawn, so the frame
// the user actually sees is mostly blits instead of a cold rasterisation pass
// — the same warm-up that render_bench_test.go performs before measuring.
//
// While the splash wants it (bootHandoff.keep) the texture is kept for the
// cross-fade into Home; otherwise it is destroyed at once and nothing drawn
// here is ever shown. If the driver refuses a render target (the fallback path
// on an exotic runtime), the warm-up is skipped and boot proceeds exactly as
// before.
func warmSceneCaches(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	if renderer == nil || config == nil {
		return
	}
	target, err := renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
	if err != nil || target == nil {
		return
	}
	kept := false
	defer func() {
		if !kept {
			_ = target.Destroy()
		}
	}()
	if err := renderer.SetRenderTarget(target); err != nil {
		return
	}

	// A warm-up is an optimisation, never a boot dependency: a scene that
	// cannot draw on a half-initialised boot must not take the app down.
	// The target is released outside the guard so a panic cannot leave the
	// renderer bound to a texture that is about to be destroyed — that would
	// break every frame after it, not just the warm-up.
	drawn := func() (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				logInfo("startup", "scene warm skipped: %v", r)
				ok = false
			}
		}()
		// An opaque start: the scene paints its own backdrop, but a target's
		// initial contents are undefined, and the hand-off copy blends.
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderScene(renderer, config, scene)
		return true
	}()

	if err := renderer.SetRenderTarget(nil); err != nil {
		logWarn("startup", "scene warm could not release its target: %v", err)
		drawn = false
	}
	// renderScene leaves clip and blend state set for its own draw pass; the
	// splash sets both at the top of every frame, but restoring them here keeps
	// the warm-up invisible to anything that runs between the two.
	renderer.SetClipRect(nil)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	if drawn && bootHandoff.keep && bootHandoff.tex == nil {
		_ = target.SetBlendMode(sdl.BLENDMODE_BLEND)
		bootHandoff.tex = target
		bootHandoff.renderer = renderer
		kept = true
	}
}

var bootWarmCache struct {
	once sync.Once
	wg   sync.WaitGroup
}

// bootWarmCacheInBackground opens the shared cache handle while the splash is
// drawing. bbolt open is file open plus mmap — cheap warm, most of a second on
// a tired SDL card — and every caller after that reuses the handle, so paying
// it here removes it from the first thumbnail or feed read.
//
// openSharedCache is mutex-guarded and idempotent: if the warm is still running
// when the UI asks for the cache, that caller waits on the same lock instead of
// opening the file a second time.
func bootWarmCacheInBackground() {
	bootWarmCache.once.Do(func() {
		bootWarmCache.wg.Add(1)
		go func() {
			defer bootWarmCache.wg.Done()
			db, err := openSharedCache("jukahub.cache")
			if err != nil {
				logWarn("startup", "cache warm failed: %v", err)
				return
			}
			// The probe read faults the first pages in; correctness does not
			// depend on it, so a miss is fine.
			_, _ = cacheGet(db, "boot:probe")
		}()
	})
}
