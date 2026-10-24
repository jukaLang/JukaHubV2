package main

// Scene-scoped background work.
//
// A screen that starts a fetch owns it: leaving the screen must stop the work,
// not leave it racing the next one on a 1 GB handheld. Before this, the Tools
// jobs kept a 100 MB speed test or a 50 MB benchmark running behind the home
// screen, and scene-entry downloads (podcasts, the package registry, weather)
// kept the Wi-Fi busy after the user had moved on. Results published when the
// work finally finished then landed on a screen that was no longer showing.
//
// Every scene transition starts a new generation with its own context and
// cancels the previous one, so a task only has to respect its context to be
// stopped. Tasks are fire-and-forget: a transition never waits for the network,
// because cancel closes the request and a slow DNS lookup must not freeze the
// fade. Shutdown cancels without waiting too; tests use waitForSceneWork to
// prove a task actually exited.

import (
	"context"
	"sync"
)

// sceneWorkGeneration is all work started for one visit to one scene.
type sceneWorkGeneration struct {
	scene  string
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// sceneWorkManager hands out the context of the active scene and tracks its
// tasks. A zero value is usable: the first task creates the generation.
type sceneWorkManager struct {
	mu      sync.Mutex
	current *sceneWorkGeneration
	closed  bool
}

var sceneWork sceneWorkManager

// newSceneWorkGeneration starts a generation, already cancellable.
func newSceneWorkGeneration(scene string) *sceneWorkGeneration {
	gen := &sceneWorkGeneration{scene: scene}
	gen.ctx, gen.cancel = context.WithCancel(context.Background())
	return gen
}

// ensureLocked returns the active generation, creating a nameless one when work
// starts before the first transition (startup tasks, or a test).
func (m *sceneWorkManager) ensureLocked() *sceneWorkGeneration {
	if m.current == nil {
		m.current = newSceneWorkGeneration("")
	}
	return m.current
}

// enter starts a fresh generation for scene and cancels the one it replaces.
// It is called when a scene transition completes, so tasks started while the
// new screen loads see that screen's context.
func (m *sceneWorkManager) enter(scene string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	old := m.current
	m.current = newSceneWorkGeneration(scene)
	m.mu.Unlock()
	if old != nil {
		old.cancel()
	}
}

// context returns the context a task started for the current scene would get.
func (m *sceneWorkManager) context() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLocked().ctx
}

// scene names the generation currently accepting work ("" before the first
// transition).
func (m *sceneWorkManager) scene() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLocked().scene
}

// spawn starts fn on the active generation. It reports false after shutdown,
// when no task may outlive the process.
func (m *sceneWorkManager) spawn(fn func(context.Context)) bool {
	if fn == nil {
		return false
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	gen := m.ensureLocked()
	gen.wg.Add(1)
	ctx := gen.ctx
	m.mu.Unlock()
	go func() {
		defer gen.wg.Done()
		fn(ctx)
	}()
	return true
}

// cancel stops the active generation but keeps it, so work started afterwards
// (a render-path task, say) still gets a cancelled context instead of escaping
// the scope.
func (m *sceneWorkManager) cancel() {
	m.mu.Lock()
	gen := m.current
	m.mu.Unlock()
	if gen != nil {
		gen.cancel()
	}
}

// shutdown closes the manager: it cancels the active generation, and later
// spawns are refused.
func (m *sceneWorkManager) shutdown() {
	m.mu.Lock()
	m.closed = true
	gen := m.current
	m.current = nil
	m.mu.Unlock()
	if gen != nil {
		gen.cancel()
	}
}

// wait blocks until every task of the active generation has returned. The
// event loop never calls it; it exists for shutdown tests and for code that
// needs to observe completion.
func (m *sceneWorkManager) wait() {
	m.mu.Lock()
	gen := m.current
	m.mu.Unlock()
	if gen != nil {
		gen.wg.Wait()
	}
}

// ── Global entry points ────────────────────────────────────────────────

// enterSceneWork starts the task scope for a newly active scene.
func enterSceneWork(scene string) { sceneWork.enter(scene) }

// cancelSceneWork stops the work of the scene being left. Safe when nothing is
// running and safe to call from the transition path.
func cancelSceneWork() { sceneWork.cancel() }

// shutdownSceneWork cancels the active scene's work for app exit.
func shutdownSceneWork() { sceneWork.shutdown() }

// sceneWorkContext returns the context of the active scene.
func sceneWorkContext() context.Context { return sceneWork.context() }

// currentSceneWorkName names the scene the active scope belongs to.
func currentSceneWorkName() string { return sceneWork.scene() }

// runSceneTask starts fn in the background, tied to the scene that started it.
// A handler calling this never blocks the render loop; the task stops when the
// scene is left, provided it watches ctx.
func runSceneTask(fn func(context.Context)) bool { return sceneWork.spawn(fn) }

// waitForSceneWork joins the active scene's tasks.
func waitForSceneWork() { sceneWork.wait() }

// ── Coordinated fan-out ────────────────────────────────────────────────

// runCancelableGroup runs fns concurrently and cancels the rest as soon as one
// returns an error, returning the first error seen. It is the errgroup
// contract, kept in-tree: the vendored module set is pinned and adding
// golang.org/x/sync would mean re-vendoring unrelated upgrades.
//
// A nil ctx is treated as the background context; an already-cancelled ctx
// returns its error without starting anything.
func runCancelableGroup(ctx context.Context, fns ...func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		wg.Add(1)
		go func(fn func(context.Context) error) {
			defer wg.Done()
			if err := fn(gctx); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}(fn)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
