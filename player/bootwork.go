package main

// bootwork.go — real startup work packed into the splash window.
//
// The splash occupies the better part of a second, and for all of that time
// the process used to do nothing but draw stars: the placeholder texture, the
// gamepad probe, the keyboard map, the thumbnail workers and the focus graph
// all ran after the animation had finished. None of them are needed to draw
// the animation, so they are queued here and executed between splash frames
// instead. The boot ends in the same place, but the first real frame arrives
// earlier by the cost of that work.
//
// Two rules keep it honest:
//
//   - Queued steps run on the render thread, in the order they were enqueued,
//     because most of them touch SDL objects that only that thread may use.
//     A step that overruns its frame is logged; the animation drops a frame,
//     which is invisible on a starfield.
//   - Work that only touches the disk runs on its own goroutine (see
//     bootWarmCacheInBackground), because it must never block a frame at all.
//
// bootWorkDrain runs whatever is left once the splash is over, so a warm start
// (no splash), a skipped splash and a card with no budget left all still boot
// through exactly the same setup.

import (
	"log"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// bootStep is one unit of startup work safe to run on the render thread.
type bootStep struct {
	name string
	fn   func()
}

var bootWork struct {
	mu      sync.Mutex
	pending []bootStep
}

// bootEnqueue queues a step to run during (or just after) the splash. A nil
// function is ignored so a feature that is compiled out does not leave a hole
// in the queue.
func bootEnqueue(name string, fn func()) {
	if fn == nil {
		return
	}
	bootWork.mu.Lock()
	bootWork.pending = append(bootWork.pending, bootStep{name: name, fn: fn})
	bootWork.mu.Unlock()
}

// bootWorkPending reports whether any step is still queued.
func bootWorkPending() bool {
	bootWork.mu.Lock()
	defer bootWork.mu.Unlock()
	return len(bootWork.pending) > 0
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
	bootWork.mu.Unlock()

	started := time.Now()
	step.fn()
	// Anything long enough to be felt is worth a line in the boot log: it is
	// the difference between "the splash stuttered" and "this step is slow".
	if took := time.Since(started); took > 20*time.Millisecond {
		log.Printf("[startup] boot step %s took %dms", step.name, took.Milliseconds())
	}
	return true
}

// bootWorkTick spends the frame's spare time on queued steps. At least one step
// always runs, so a single slow step cannot starve the queue while the splash
// is up: the next frame continues from where it stopped.
func bootWorkTick(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for runOneBootStep() {
		if time.Now().After(deadline) {
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

// warmSceneCaches draws the scene the first frame will show once into an
// offscreen texture while the splash is still up. Every label, shape, icon and
// image it touches lands in the renderer's caches as it is drawn, so the frame
// the user actually sees is mostly blits instead of a cold rasterisation pass
// — the same warm-up that render_bench_test.go performs before measuring.
//
// The texture is destroyed immediately and nothing drawn here is ever shown.
// If the driver refuses a render target (the fallback path on an exotic
// runtime), the warm-up is skipped and boot proceeds exactly as before.
func warmSceneCaches(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	if renderer == nil || config == nil {
		return
	}
	target, err := renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
	if err != nil || target == nil {
		return
	}
	defer target.Destroy()
	if err := renderer.SetRenderTarget(target); err != nil {
		return
	}

	// A warm-up is an optimisation, never a boot dependency: a scene that
	// cannot draw on a half-initialised boot must not take the app down.
	// The target is released outside the guard so a panic cannot leave the
	// renderer bound to a texture that is about to be destroyed — that would
	// break every frame after it, not just the warm-up.
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[startup] scene warm skipped: %v", r)
			}
		}()
		renderScene(renderer, config, scene)
	}()

	if err := renderer.SetRenderTarget(nil); err != nil {
		log.Printf("[startup] scene warm could not release its target: %v", err)
	}
	// renderScene leaves clip and blend state set for its own draw pass; the
	// splash sets both at the top of every frame, but restoring them here keeps
	// the warm-up invisible to anything that runs between the two.
	renderer.SetClipRect(nil)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
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
				log.Printf("[startup] cache warm failed: %v", err)
				return
			}
			// The probe read faults the first pages in; correctness does not
			// depend on it, so a miss is fine.
			_, _ = cacheGet(db, "boot:probe")
		}()
	})
}

// bootWaitForWarmCache blocks until the background open has finished. The
// splash queue uses it as its last step: by then the UI is about to start and
// the handle must be settled, not half-open.
func bootWaitForWarmCache() { bootWarmCache.wg.Wait() }
