# JukaHubV2 Modernization Plan

> **Implementation status (2026-10-18):** verified item-by-item in [MODERNIZATION-CHECKLIST.md](MODERNIZATION-CHECKLIST.md). `go vet ./...`, `go test ./...` and `go build .` pass on this revision. The newest handheld work is in [Phase 8](#phase-8-trimui-handheld-polish).

## Executive Summary

This document outlines the modernization of JukaHubV2 for production use on **Trimui Smart Pro** (1280×720, ARM64, 1GB RAM) and **Windows** (x64, resizable). The goal is to create a controller-first, responsive, reliable handheld launcher while preserving the existing Go + SDL2 runtime and config-driven architecture.

## Current State Audit

### Architecture Overview

**Runtime**: Go 1.23 + SDL2 (go-sdl2) + SDL_ttf + SDL_image  
**Config Model**: JSON-driven (`jukaconfig.json`, `jukauser.json`)  
**Rendering**: Single-threaded SDL2 renderer with per-frame allocations  
**State Management**: Global variables in `main.go`  
**Package Structure**: Monolithic `player/` package with feature files

### Key Files

| File | Purpose | Status |
|------|---------|--------|
| `main.go` | Main render loop, input handling, scene dispatch | ~5000 lines, monolithic |
| `design.go` | Design tokens, colors, spacing | Partially updated |
| `themes.go` | Theme presets | Basic color-only themes |
| `extras.go` | Status bar, error bar, footer hints | Needs footer rendering |
| `launcher.go` | Icon rendering helpers | Needs opaque backgrounds |
| `homelayout.go` | Home screen layout computation | Responsive grid logic exists |
| `recent.go` | Continue/recent panel | Partially updated |
| `jukaconfig.json` | Scene configuration | Config-driven, 8 home tiles |

### Current Issues Identified

#### 1. Rendering Issues (Fixed in Phase 1)
- ✅ Card rendering: Removed washed-out top highlight
- ✅ Footer rendering: Added controller hints
- ✅ Continue panel: Fixed to use layout tokens
- ✅ Status bar: Simplified for non-Main scenes
- ✅ Icon rendering: Made backgrounds opaque

#### 2. Architecture Issues (Phase 2)
- 🟡 **Large main.go**: split into core/extras/feature files, but `main.go` (~1.8k lines) still owns the loop
- 🟡 **Global state**: still package-level, though ownership is now split by file
- 🟡 **Async isolation**: most network work is off-thread; some render-path decoding remains
- 🟡 **Context cancellation**: Patch, downloads and text browser use contexts; older fetches do not
- 🟡 **Bounded concurrency**: thumbnail downloads are serialized; other paths are not pooled
- ✅ **Platform abstraction**: `platform.go` behind `P()`
- ✅ **Structured logging**: `log/slog` with scene/operation fields
- ✅ **Config validation + schema versioning**: `config_schema.go`, `config_health.go`
- ✅ **Atomic writes**: `AtomicWrite` with `.bak` fallback
- ✅ **Safe mode**: flag/env/marker disables plugins, updater and network work

#### 3. UI/UX Issues (Phase 1 - Partially Fixed)
- ✅ Home screen layout: Responsive grid with 4/3/2 columns
- ✅ Focus state: 3px cyan accent outline
- ✅ Focus preservation: per-scene persist/restore (`focus.go`)
- ✅ Focus graph: explicit per-scene graph with deterministic navigation
- ✅ Empty states: shared `UIEmptyState` / `UIErrorState`
- ❌ Icon consistency: Monochrome icons but mixed stroke weights
- 🟡 Typography: semantic size roles and font-size config; one font weight
- ✅ Spacing: 4pt scale plus density tokens

#### 4. Performance Issues (Phase 3)
- ✅ Text textures are cached (`textcache.go`)
- ✅ Texture/thumbnail caching with entry and byte limits
- 🟡 No list virtualization: full lists are still drawn, clipped to the viewport
- 🟡 Idle frame cadence exists; background polling is not scene-scoped
- 🟡 Low-power mode and adaptive frame rate; no named profiles
- ✅ Adaptive frame budget (`adaptive.go`)

#### 5. Reliability Issues (Phase 4)
- ✅ Config validation with defaults on failure
- ✅ Atomic writes with last-known-good backup
- ✅ Backup: `config_health.go` restores the `.bak` copy
- ✅ Crash recovery: update journal rollback at startup
- ✅ Offline handling: best-effort failures and connectivity monitor
- ✅ Missing tool detection: health check plus guided install bar
- ✅ Log viewer: captured ring buffer rendered by `textlog` scenes

## Implementation Plan

### Phase 1: UI/UX Fixes (Completed)
**Status**: ✅ Done

- [x] Fix card rendering (remove washed-out highlight)
- [x] Add footer with controller hints
- [x] Fix Continue panel styling
- [x] Simplify status bar
- [x] Make icon backgrounds opaque
- [x] Add Home color helpers
- [x] Format all Go files

### Phase 2: Architecture Refactoring
**Status**: 🟡 Mostly complete — see the checklist

#### 2.1 Design System
- [x] Semantic color tokens in `design.go`
- [x] Add spacing tokens (8, 12, 16, 24, 32)
- [x] Add radius tokens (small, medium, large)
- [x] Add typography tokens (title, heading, body, label)
- [x] Add motion tokens (fast, standard, reduced) — `anim.go`: `uiMenuEnterMS`/`uiPanelEnterMS`/`uiFadeMS`, all gated by `ReducedMotion`
- [x] Add density tokens (compact, comfortable)

#### 2.2 Component System
- [ ] Create `AppBar` component
- [x] Create `StatusBar` component
- [x] Footer hints (`FooterHint` + `renderFooter`)
- [x] Create `Card` component
- [ ] Create `CategoryTile` component
- [ ] Create `Button` component
- [ ] Create `IconButton` component
- [x] Create `ListRow` component
- [x] Create `Grid` component
- [x] Create `ProgressBar` component
- [x] Create `Badge` component
- [x] Create `Toast` component
- [x] Create `EmptyState` component
- [x] Create `ErrorState` component
- [x] Create `LoadingSkeleton` component

#### 2.3 Focus System
- [ ] Create `Focusable` interface
- [x] Build explicit focus graph per scene
- [x] Implement focus preservation on scene return
- [x] Add deterministic directional navigation
- [x] Add focus animation (100-140ms) — keyframed hover/focus lift on home cards and buttons
- [x] Add reduced-motion mode
- [ ] Add input remapping screen
- [ ] Add controller test screen

#### 2.4 Platform Abstraction
- [x] Define `Platform` interface
- [x] Add TrimUI/Linux platform implementation (`platform.go`)
- [x] Add Windows platform implementation (`platform.go`)
- [x] Move OS-specific paths to platform layer
- [x] Move process flags to platform layer
- [x] Move power behavior to platform layer
- [x] Move tool discovery to platform layer

### Phase 3: Async Work & Performance
**Status**: 🟡 In progress

#### 3.1 Concurrency Model
- [ ] Add `context.Context` everywhere
- [ ] Add `errgroup` for coordinated cancellation
- [ ] Add bounded semaphore for concurrency limits
- [ ] Create worker pool for image decoding (2 jobs on Trimui)
- [x] Create worker pool for network requests (2-3 jobs on Trimui) — thumbnail prefetch: 2 on handhelds, 3 on desktop
- [ ] Debounce search input (250-350ms)
- [ ] Cancel scene work on scene exit

#### 3.2 Caching
- [x] Add font texture cache (key: font+size+color+string)
- [ ] Add icon texture atlas
- [x] Add thumbnail cache with byte limit
- [x] Add network response cache with TTL
- [x] Add LRU eviction policy

#### 3.3 Rendering
- [x] Cache text textures
- [ ] Cache static icons in atlas
- [ ] Reuse card/label/image objects
- [x] Add clipping for lists/grids
- [x] Add idle render mode
- [x] Add frame time budget

#### 3.4 Power Management
- [ ] Add Battery Saver profile
- [ ] Add Balanced profile
- [ ] Add Performance profile
- [ ] Stop polling when scene hidden
- [x] Reduce animation in battery saver

### Phase 4: Reliability & Configuration
**Status**: 🟡 Mostly complete

#### 4.1 Configuration
- [x] Add schema versioning
- [x] Add config validation
- [x] Add migrations for old configs
- [x] Add atomic writes (temp file + rename)
- [x] Add last-known-good backup
- [x] Add config hot-reload (Windows dev only)
- [x] Add portable mode for packaging

#### 4.2 Logging
- [x] Switch to `log/slog`
- [x] Add structured fields (subsystem, scene, action, duration)
- [x] Add JSON logs for Windows
- [x] Add compact logs for Trimui
- [ ] Add log rotation
- [x] Add in-app log viewer
- [x] Add diagnostic bundle export

#### 4.3 Error Handling
- [x] Add offline detection
- [ ] Add cached fallback for offline content
- [x] Add guided remediation for missing tools
- [x] Add safe mode (disable plugins, themes, networking)
- [x] Add unclean shutdown detection
- [x] Add crash recovery UX

#### 4.4 Network
- [x] Create shared HTTP client
- [x] Add connection reuse
- [x] Add request timeouts
- [x] Add max response sizes
- [x] Add retry with exponential backoff + jitter
- [ ] Add conditional caching
- [x] Add offline detection

### Phase 5: Features
**Status**: 🟡 Partially complete

#### 5.1 Media
- [ ] Add media-backend interface (ffplay today; keep it swappable)
- [ ] Add playback queue
- [x] Add resume position
- [x] Add progress display
- [ ] Add cancellation
- [x] Add clear failure messages

#### 5.2 Files
- [x] Add file explorer improvements
- [ ] Add drag-and-drop (Windows)
- [x] Add clipboard integration (Windows)

#### 5.3 Packages
- [x] Add package trust UX
- [x] Add source/version/checksum display
- [x] Add uninstall rollback

#### 5.4 Settings
- [ ] Add controller remapping
- [x] Add accessibility settings
- [ ] Add power profile selection
- [x] Add theme preview

### Phase 6: Testing
**Status**: 🟡 Partially complete — bounds, cache, retry and persistence gaps closed

- [x] Add config validation tests
- [x] Add focus graph navigation tests
- [x] Add input normalization tests
- [x] Add layout bounds tests (1280×720)
- [x] Add cache eviction tests
- [ ] Add cancellation tests
- [x] Add external process cleanup tests — ffmpeg decoder join + teardown assertion
- [x] Add URL/path validation tests
- [x] Add offline/retry behavior tests
- [ ] Add platform path selection tests
- [ ] Add benchmarks for render, cache, navigation

### Phase 7: Web Builder
**Status**: 🟡 Mostly complete — snapping and guides still missing

- [x] Add semantic design tokens
- [ ] Add reusable components
- [ ] Add snap-to-grid
- [ ] Add alignment guides
- [ ] Add safe-area overlay
- [ ] Add focus-order editor
- [x] Add state previews
- [x] Add undo/redo
- [x] Add autosave
- [ ] Add Trimui performance linting
- [ ] Add schema validation
- [x] Add production export

### Phase 8: TrimUI handheld polish
**Status**: 🟡 Started — interaction feedback and card rendering landed; the rest is queued

This phase collects the work that specifically targets the TrimUI Smart Pro
(1280×720, ARM64, 1 GB RAM, 60 Hz panel) rather than desktop parity.

#### 8.1 Interaction feedback (implemented)
- [x] One icon per home card: `homeActionIconTile` picks the horizontal or the
      stacked composition **before** drawing, so Files and Settings no longer
      render a wide tile plus a small stacked tile on top of it
- [x] Eased press dip for every click: home cards, config buttons and app tiles
      travel down over the press window instead of toggling a fixed 2 px offset
      (`uiPressDepth` / `uiPressProgress` in `anim.go`)
- [x] Scene fade-out/fade-in retimed for the handheld: 160 ms out, 200 ms in
      (was 250/300 ms, which read as waiting after every button press)
- [x] Theme-aware fade scrim: the transition follows the active theme's
      background instead of hardcoded near-black, so light themes no longer
      flash dark between screens
- [x] Press feedback still animates under Low Power (the handheld default) and
      snaps under Reduced Motion only; entrance animations remain gated by both
- [ ] Haptic tick on card activation (SDL haptic, best-effort, off by default)
- [ ] Hold-to-repeat on list edges for D-pad scrolling

#### 8.2 Touch & layout
- [x] Responsive stacked composition for half-width cards (Files, Settings)
- [x] Stacked cards keep their subtitle: the tile shrinks just enough for both
      text lines instead of dropping the subtitle and leaving dead space
- [ ] Guarantee ≥44×44 px touch targets for every interactive control
- [ ] Safe-area insets so cards and footer chips clear the panel's rounded corners
- [ ] Enlarge the Exit chip's hit area without changing its drawn size

#### 8.3 Rendering budget
- [x] Text texture cache and byte-capped thumbnail cache
- [x] Bounded thumbnail prefetch (2 workers on handhelds)
- [x] **Bounded thumbnail texture cache** (`thumbstore.go`): an LRU with a
      64 MB budget on handhelds / 192 MB on desktop. The old map kept every
      thumbnail texture for the whole session, which is what forced SDL to
      re-upload and evict during long browsing sessions.
- [x] **Texture memory accounting** (`perf.go`, `JUKAHUB_TEXSTATS=1`): text,
      thumbnail, video overlay, decoded frame and UI-layer bytes are logged at
      startup, on critical pressure, and periodically when the env var is set.
- [x] **Bounded `drawText` label cache** (`homelayout.go`): 8 MB / 512 entries
      on handhelds, 48 MB / 2048 on desktop, LRU-evicted. Dynamic strings
      (clock, status, titles, toasts) used to accumulate textures forever.
- [x] Theme changes drop cached text and label textures immediately, so the
      new palette shows on the next frame instead of waiting for LRU eviction.
- [ ] Cache the ambient glow instead of re-blending four blobs per frame
- [ ] Icon atlas for the shared 32 px glyph set
- [ ] Virtualize lists and grids by row instead of drawing the full list clipped
- [ ] Keep thumbnail decoding out of the render path (decode budget of 2/frame)

#### 8.4 Memory & GC (implemented)
- [x] Go collector tuned per device (`applyGCForDevice`): GOGC=60 with a
      384 MB soft heap limit on 1 GB/handheld targets, GOGC=80 / 768 MB up to
      2 GB, stock settings on desktops. Explicit `GOGC`/`GOMEMLIMIT` env vars
      always win so profiling runs are not silently overridden.
- [x] Debounced cache-pressure eviction: the loop used to clear every texture
      cache and run a full `runtime.GC()` every ~10 frames while RAM was low
      (≈3 times a second). It now acts on a new pressure level and at most once
      per 15 s (critical) / 60 s (warning), and the reclamation runs off the
      render thread.
- [x] **Decode-size cap**: sources above the display size are scaled in ffmpeg
      (1280×720 handheld, 1080p desktop) instead of decoding 33 MB 4K RGBA
      frames for a 720p panel.
- [x] **Zero-copy frame handoff**: the decoder swaps its buffer with the
      renderer's instead of copying every frame (a 720p frame is 3.6 MB).
- [x] Video texture is recreated when a new source has different dimensions
      (previously the old texture size was reused and SDL rejected the upload).
- [ ] Scene-scoped texture release: free caches for scenes that are not open
- [ ] On-device measurement of cold start, transition frame time and idle RSS

#### 8.5 Power & startup
- [ ] Named power profiles (Battery Saver / Balanced / Performance) mapping to
      Low Power, adaptive FPS and motion in one setting
- [ ] Scene-scoped polling: stop weather/network refreshes for hidden scenes
- [ ] Warm start: draw the home shell on the first frame, fetch afterwards
- [ ] Measure cold-start time and idle frame cost on the device (target: home
      interactive < 1.5 s, idle < 5% CPU at the capped cadence)

## Acceptance Criteria

### UI
- [x] No overlap or clipping at 1280×720
- [ ] All primary text legible at physical size
- [ ] Every scene usable with D-pad, face buttons, shoulders, keyboard
- [ ] Focus always visible and deterministic
- [ ] Long labels truncate/wrap without changing geometry
- [ ] Empty/loading/error/offline/success states for every component

### Performance
- [ ] Home screen interactive quickly after launch
- [ ] Navigation responsive while thumbnails/network load
- [ ] No network/image/filesystem work on render loop
- [x] Texture/thumbnail caches have entry/byte limits
- [ ] Idle scenes reduce polling/rendering
- [ ] Trimui build stable under repeated scene changes

### Reliability
- [x] Corrupt user config doesn't prevent startup
- [x] Missing tools produce guided remediation
- [x] Writes are atomic and recoverable
- [x] Plugins can be disabled via safe mode
- [x] Logs identify scene/action/operation/platform (duration where measured)
- [ ] Windows/Trimui builds pass CI with smoke tests

## Timeline Estimate

| Phase | Duration | Dependencies |
|-------|----------|--------------|
| Phase 1: UI/UX Fixes | 1 week | None |
| Phase 2: Architecture | 3 weeks | Phase 1 |
| Phase 3: Async/Performance | 2 weeks | Phase 2 |
| Phase 4: Reliability | 2 weeks | Phase 2, 3 |
| Phase 5: Features | 4 weeks | Phase 2, 3, 4 |
| Phase 6: Testing | 2 weeks | Phase 5 |
| Phase 7: Web Builder | 3 weeks | Phase 2, 3, 4, 5 |
| **Total** | **~16 weeks** | - |

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking existing features | High | Test each feature after refactor |
| Performance regression | Medium | Benchmark before/after each phase |
| Build failures | Medium | Run `go vet`, `gofmt` after each change |
| Platform-specific bugs | Medium | Test on both Trimui and Windows |
| Memory leaks | Medium | Use profiling tools, bounded caches |
| Config corruption | High | Atomic writes, backups, safe mode |

## Next Steps

The phases above are now mostly implemented. The authoritative remaining work,
with file-level evidence, is the **Recommended next work** section of
[MODERNIZATION-CHECKLIST.md](MODERNIZATION-CHECKLIST.md):

1. Bounded async work and scene-scoped cancellation.
2. Log rotation and wiring `Ctrl+L` to the existing Logs scene.
3. Controller remapping screen.
4. Web builder snapping, alignment guides, safe-area overlay and schema validation.
5. Render/navigation benchmarks at 720p.
6. **Phase 8 handheld polish:** touch-target audit, safe-area insets, cached
   ambient glow, icon atlas, list virtualization, named power profiles, and
   on-device measurement of cold start, transition frame time, idle RSS and the
   GC/decode tuning above (all numbers so far are desktop proxies).

## Notes

- This is an **incremental** modernization, not a rewrite
- Each phase must produce **working builds**
- Preserve **existing features** unless clearly broken
- Keep **config-driven model** intact
- Optimize for **Trimui constraints** (1GB RAM, ARM64)
- Support **Windows** as secondary target
- Use **Go standard library** where possible
- Add **tests** as we go, not at the end