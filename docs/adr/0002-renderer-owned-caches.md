# 2. Static-layer caches are scoped to their renderer

- **Status:** accepted
- **Date:** 2026-10-25
- **Deciders:** maintainers

## Context

The keyboard, chat and video-overlay layers cache their static SDL texture so a
frame does not redraw text and panels. The test suite creates and destroys
renderers, and SDL frees every texture with the renderer that created it. A cache
that outlived its renderer therefore held a dangling pointer: a later frame would
reuse it, or a teardown would free it again, and the corruption surfaced as an
intermittent crash inside an unrelated SDL call — `SDL_RenderReadPixels`, in the
case that prompted this record (`TestVideoFullscreenHidesControlBar`).

The bug was not in the test. Any code path that re-creates the renderer (a mode
change, a device rotation, a test) could poison the caches the same way.

## Decision

Every static-layer cache records the renderer it was built with, next to the
texture. A lookup whose recorded owner is not the current renderer forgets the
entry instead of touching it: `vkForgetForeignTexture` clears the pointer, the
owner and the validity flag, and the next frame rebuilds. The test helper
`destroyTestRenderer` releases the caches before it destroys the renderer, so a
normal teardown never leaves an owner pointing at freed memory.

Forgetting rather than destroying is deliberate. Destroying a texture whose
renderer is already gone is the operation that corrupts SDL's allocator; a
rebuild costs one frame.

## Alternatives considered

- **Destroy each cache in its own teardown.** The state before this record. One
  forgotten cache brings the crash back, and the failure looks like a test flake
  rather than a missing line.
- **A global renderer generation counter.** Functionally close, but it adds a
  second piece of state that every cache has to consult; comparing the owner
  pointer is the same check with nothing to keep in sync.
- **Do not cache.** The keyboard redraw is measurably the expensive part of a
  frame; `TestKeyboardCachedFrameIsCheaperThanFullRedraw` exists because the
  cache is a performance decision, not an optimisation to drop.

## Consequences

- One rule covers all three caches and any cache added later: own it, or forget
  it.
- `player/renderer_cache_owner_test.go` pins the rule without needing a GL
  context to go wrong, and `CONTRIBUTING.md` records the diagnosis instead of a
  "known flaky test" warning.
- The cost is a rebuild after a renderer change, which is once per renderer.
