# JukaHubV2 Modernization Plan

Status of every item below, checked against the code in this tree on
**2026-10-24** (go1.27.2 windows/amd64, SDL2 from `player/.sdl2`):

```
go vet ./...      # exit 0
go test ./...     # ok jukahub/JukaHub, ok jukahub/JukaHub/web
go build .        # exit 0
```

Legend: **✅ shipped** (code exists and is exercised by tests), **🟡 partial**
(some of the item works, the rest is genuinely missing), **⬜ not started**.
Library choices and measured numbers live in
[LIBRARY-EVALUATION.md](LIBRARY-EVALUATION.md).

## Executive Summary

This document outlines the modernization of JukaHubV2 for production use on **Trimui Smart Pro** (1280×720, ARM64, 1GB RAM) and **Windows** (x64, resizable). The goal is to create a controller-first, responsive, reliable handheld launcher while preserving the existing Go + SDL2 runtime and config-driven architecture.

## At a glance

| Phase | Done | Partial | Not started |
|-------|------|---------|-------------|
| 2. Architecture | 27 | 5 | 2 |
| 3. Async & performance | 12 | 3 | 6 |
| 4. Reliability | 24 | 1 | 1 |
| 5. Features | 10 | 4 | 2 |
| 6. Testing | 11 | 1 | 0 |
| 7. Web builder | 5 | 2 | 5 |
| 8. Handheld polish | 21 | 0 | 13 |
| 9. Browser & handheld input | 8 | 0 | 4 |

## Current State Audit

### Architecture Overview

**Runtime**: Go 1.26 + SDL2 (go-sdl2) + SDL_ttf + SDL_image\
**Config Model**: JSON-driven (`jukaconfig.json`, `jukauser.json`)\
**Rendering**: Single-threaded SDL2 renderer with per-frame allocations\
**State Management**: Global variables in `main.go`\
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
| `jukaconfig.json` | Scene configuration | Config-driven scenes; the Home grid is drawn from `homelayout.go` |

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
- 🟡 **Bounded concurrency**: thumbnail prefetch runs in a bounded pool; other paths are not pooled
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
- ✅ Metric-driven panel cards: the Downloads SD card and its legend lay
  out from the font's own height (`downloadStorageCardHeight`), so a taller
  accessibility scale pushes rows down instead of the bar drawing through the
  title and the legend clipping at the card edge; the right-aligned capacity
  reading also ellipsizes against the title column on narrow windows.
- ✅ Downloads focus ring: the focused row wears the reference mockup's
  bright rounded outline plus a faint halo (`drawDownloadFocusRing`) on both
  the Add-a-link row and the track rows.
- ✅ Per-row color identity: download art tiles are two-tone in a hue hashed
  from title and URL (`downloadTileColors` over the shared `hsvToRGB`), so the
  same track always wears the same colors and the queue scans by color.
- ✅ Section headings carry counts ("FAILED · 2") in `downloadRowsFor`, so a
  long list answers how much is stuck while scrolling.

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

- [x] ✅ Fix card rendering (remove washed-out highlight)
- [x] ✅ Add footer with controller hints
- [x] ✅ Fix Continue panel styling
- [x] ✅ Simplify status bar
- [x] ✅ Make icon backgrounds opaque
- [x] ✅ Add Home color helpers
- [x] ✅ Format all Go files

### Phase 2: Architecture Refactoring
**Status**: 🟡 Mostly complete (27 shipped, 5 partial, 2 not started)

#### 2.1 Design System

- [x] ✅ Semantic color tokens — `design.go` (surfaces, borders, text accessors, semantic, progress, chips).
- [x] ✅ Spacing tokens — 4pt scale `SpaceXS … Space3XL` in `design.go`.
- [x] ✅ Radius tokens — `RadiusXS … RadiusRound`.
- [x] ✅ Typography tokens — `FontSizeXSS … FontSize3XL`, semantic `TypeRole`
      (`TypeTitle/Heading/Body/Label/Caption`) and `FontForRole`; covered by
      `design_tokens_test.go`.
- [x] ✅ Motion tokens — `AnimDuration*` in `design.go`, `anim.go` durations
      (`uiMenuEnterMS`/`uiPanelEnterMS`/`uiFadeMS`), all gated by reduced motion
      (`anim_test.go`, `TestUIMotionGate`).
- [x] ✅ Density tokens — `DensityCompact/DensityComfortable`, `DensityFor`,
      `DensitySpace`, airy spacing aliases; covered by `design_tokens_test.go`.

#### 2.2 Component System

- [x] ✅ `Card` — `UICard`/`UICardStyle`, adopted by notifications, Start menu and
      Quick Settings; render tests in `components_test.go`.
- [x] ✅ `StatusBar` — `renderStatusBar`; shared right-hand cluster order with the
      home header, overflow dropping, safe-mode chip; `statusbar_render_test.go`.
- [x] ✅ Footer hints — `renderFooter` + `FooterHint`, per-scene overrides
      (`patchFooterHints`).
- [x] ✅ `Button` (surface-level) — shared pressed/focus recipes in `ui_helpers.go`
      and per-scene button geometry; no single `UIButton` type yet 🟡.
- [x] ✅ `ListRow` — `UIListRow`, used by the generic scene list renderer.
- [x] ✅ `Grid` — `UIGridColumns`, `UIGridCellWidth`, `UIGridVisibleRows`,
      `UIGridCellRect`; theme gallery geometry now uses them (`grid_test.go`,
      `TestThemeGalleryUsesGridGeometry`).
- [x] ✅ `ProgressBar` — `UIProgressBar`, used by Patch's hold-to-confirm button and
      covered by `components_test.go`.
- [x] ✅ `Badge` — `UIBadge`, used by the notification bell.
- [x] ✅ `Toast` — `ShowToast`/`showToast` with semantic kinds and duration policy.
- [x] ✅ `EmptyState` / `ErrorState` — `UIEmptyState` / `UIErrorState`
      (`uiStateBlock`).
- [x] ✅ `LoadingSkeleton` — `UILoadingSkeleton` with pulse phase.
- [ ] 🟡 `CategoryTile` — the home renderer has a card recipe, but it is not a
      reusable component.
- [ ] 🟡 `IconButton` — icon geometry helpers exist (`launcher.go`), but buttons are
      still drawn inline per scene.
- [ ] ⬜ Create `AppBar` component — screen headers are drawn inline by scenes;
      the verified audit above never counted this as started.

#### 2.3 Focus System

- [x] ✅ Focus graph per scene — `focus.go` (`FocusEngine`, graph nodes, chrome tiers).
- [x] ✅ Focus preservation on scene return — `Persist`/`Restore`, wired in
      `core.go` scene changes.
- [x] ✅ Deterministic directional navigation — `focus.go` geometry-based movement;
      `navselection_test.go`, `chrome_nav_test.go`, `homefocus_nav_test.go`.
- [x] ✅ Focus animation — hover/focus lift (100–140 ms), reduced-motion aware.
- [x] ✅ Reduced-motion mode — config flag, animation gates, battery-saver test.
- [x] ✅ Controller test screen — `ctrldiag.go`, opened from Power tools; see
      [../docs/CONTROLLER_TEST.md](../docs/CONTROLLER_TEST.md).
- [ ] 🟡 `Focusable` interface — behavior is centralized in `FocusEngine`, but
      there is no Go interface implemented by element types.
- [ ] ⬜ Input remapping screen.

#### 2.4 Platform Abstraction

- [x] ✅ `Platform` interface — `platform.go` with `P()`, executable dir, device
      detection, process flags, tool paths.
- [x] ✅ TrimUI/Linux and Windows implementations — same file, platform-specific
      files (`patch_statfs_*`, `processes_*`).
- [x] ✅ OS-specific paths/process flags/power behavior/tool discovery moved behind
      `P()` (`platform.go`, `paths.go`).
- [x] ✅ Portable layout — data, apps, packages and shared folder resolve next to
      the executable (`patch_repo.go`, `apps.go`, `packages.go`).

### Phase 3: Async Work & Performance
**Status**: 🟡 In progress (12 shipped, 3 partial, 6 not started)

#### 3.1 Concurrency Model

- [ ] 🟡 `context.Context` — threaded through Patch, downloads, text browser,
      installers and now scene work: scene-entry feed fetches (podcasts, the
      package registry, weather) and the Tools jobs (tickers, speed test,
      benchmarks) take their scene's context. Older feature fetches still use
      bare clients.
- [x] ✅ `errgroup`-style coordinated cancellation — `runCancelableGroup`
      (`scenetasks.go`) implements the errgroup contract in-tree, because the
      vendored module set is pinned and `golang.org/x/sync` would require
      re-vendoring unrelated upgrades. It cancels siblings on the first error
      and returns that error; covered by `scenetasks_test.go`
      (`TestRunCancelableGroup*`).
- [x] ✅ Bounded thumbnail prefetch pool — 2 workers on handhelds, 3 on desktop,
      with in-flight dedupe (`thumbstore.go`); verified by
      `TestThumbnailPrefetchRunsConcurrently`. No general-purpose semaphore yet.
- [x] ✅ Cancel scene work on scene exit — `scenetasks.go` gives each scene a
      context generation; `switchScene` cancels the screen being left and
      `completeSceneTransition` opens the next one. Adopted by the podcast and
      package fetches, the weather screen, and the ticker/speed-test/benchmark
      jobs, which clean up their temporary files on cancel. A transition never
      waits for the network; tests join with `waitForSceneWork`.
- [ ] ⬜ Image-decode worker pool (2 jobs on Trimui).
- [ ] ⬜ General bounded semaphore for other concurrency paths.
- [ ] ⬜ Debounced search input — config saves and hot reload are debounced;
      search is not. First name the path it is for: the Tube field only submits
      on Enter/its button, and the global search filters in memory, so this is
      worth doing only where a keystroke really starts work.

#### 3.2 Caching

- [x] ✅ Font/text texture cache — `textcache.go`, LRU with size cap (key:
      font+size+color+string); eviction, trim, clear, capacity clamp and key
      tests in `textcache_test.go`.
- [x] ✅ Thumbnail cache with entry limit — `thumbstore.go` memory + disk cache.
- [x] ✅ Network response cache with TTL — bbolt `cache.go`.
- [x] ✅ LRU eviction — text cache and thumbnail memory cache.
- [x] ✅ Icon texture atlas — landed as the glyph-set atlas (`iconatlas.go`);
      see the Phase 8b entry for the full description.

#### 3.3 Rendering
#### 3.3 Rendering

- [x] ✅ Cached text textures — `globalTextCache`, pressure trimming.
- [x] ✅ Clipping for lists/grids — `renderWithClip`.
- [x] ✅ Idle render mode — `FrameRateController` drops cadence when nothing animates.
- [x] ✅ Frame time budget — adaptive delay + low-power cadence (`adaptive.go`,
      `adaptive_test.go`).
- [ ] 🟡 Object reuse for cards/labels/images — card surfaces are stateless helpers;
      per-frame text layout still allocates.
- [ ] ⬜ Icon atlas as above.

#### 3.4 Power Management

- [x] ✅ Reduce animation in battery saver — motion gates (`anim_test.go`).
- [ ] 🟡 Named power profiles (Battery Saver / Balanced / Performance) —
      `LowPower` + `ReducedMotion` + adaptive FPS exist; there is no selector
      that maps all three to one setting.
- [ ] ⬜ Stop polling when a scene is hidden (weather ticker and monitors run for
      the whole process lifetime).

### Phase 4: Reliability & Configuration
**Status**: 🟡 Mostly complete (24 shipped, 1 partial, 1 not started)

#### 4.1 Configuration

- [x] ✅ Schema versioning — `config_schema.go`, `SchemaVersion` on `Config`,
      newer-file refusal (`config_schema_test.go`).
- [x] ✅ Validation — `ConfigValidator` (`config_health.go`), used at startup and
      by hot reload.
- [x] ✅ Migrations — `MigrateConfig`, applied by every loader.
- [x] ✅ Atomic writes — `AtomicWrite` temp+rename with `.bak`; tested.
- [x] ✅ Last-known-good backup — `config_health.go` (`RecordBackupRestore`,
      `.bak` fallback).
- [x] ✅ Config hot reload (desktop dev) — `hotreload.go`, started in `main.go`,
      debounced 500 ms, validated before applying.
- [x] ✅ Portable mode — all state resolves next to the executable.
- [x] ✅ Persistence separation — design saved to `jukaconfig.json`, user state
      to `jukauser.json`; volatile keys filtered (`config_persist_test.go`).

#### 4.2 Logging

- [x] ✅ `log/slog` — `logging.go`, plus structured fields (`LogScene`, `LogOp`,
      `LogSceneOp`: scene, subsystem, platform, goos/arch).
- [x] ✅ JSON logs for Windows / compact logs for Trimui.
- [x] ✅ In-app log viewer — `appLog` ring (2000 lines) captured via
      `log.SetOutput`, shown by the shipped **LogExporter** scene's `textlog`
      panel. The panel shows the newest lines (tail view) instead of stopping
      at the oldest ones, covered by `logviewer_test.go`.
- [x] ✅ `Ctrl+L` shortcut — `handleLogShortcut` (`shortcuts.go`) jumps to the
      shipped **LogExporter** scene from any scene, including the ones with no
      config elements (Games, Patch, Music) that the per-element chord dispatch
      never reaches. It stands aside while the terminal has focus, which keeps
      Ctrl+L for clearing its output, and is only consumed when the config
      actually has the scene, so it never swallows a press that does nothing.
- [x] ✅ Log rotation — an export moves the previous `logs.txt` to `logs.txt.1`
      (one backup, then writes the new file through `AtomicWrite`), and both the
      exported file (2 MiB) and the in-memory `appLog` ring (512 KiB, in
      addition to its 2000-line cap) are size-capped, so one pathological line
      cannot grow either without bound (`logrotate.go`, `logrotate_test.go`).
- [x] ✅ Diagnostic bundle export — `ExportDiagnostics` (redacted report + Patch
      UI action).

#### 4.3 Error Handling

- [x] ✅ Offline detection — `netmon.go` + status cluster.
- [x] ✅ Guided remediation for missing tools — tool health check + install bar,
      offline-safe retry.
- [x] ✅ Safe mode — `safemode.go`: `--safe-mode`, `JUKAHUB_SAFE_MODE`, or
      `safe-mode.flag` disables plugins, updater, tool install and background
      network work for one launch, with a persistent `SAFE` chip in the status
      bar. Covered by `safemode_test.go` (resolution precedence, gating,
      plugin skip).
- [x] ✅ Unclean shutdown detection / crash recovery — update journal + rollback
      (`patch_engine.go`, `checkInterruptedJournal`), tested.
- [ ] 🟡 Cached fallback for offline content — thumbnails have disk cache;
      feeds do not.

#### 4.4 Network

- [x] ✅ Shared HTTP clients — `net.go` (`httpClientLong`) plus Patch's bounded client.
- [x] ✅ Connection reuse — keep-alive transport.
- [x] ✅ Request timeouts — per-client and per-context.
- [x] ✅ Max response sizes — `fetchBounded`, `downloadToFile`.
- [x] ✅ Retry with exponential backoff + jitter — `retry.go`, honors
      `Retry-After`, retries 408/425/429/5xx and transport errors, refuses to
      replay an unrewindable body; adopted by the Patch metadata fetch and
      weather forecast; covered by `retry_test.go`.
- [ ] ⬜ Conditional caching (`If-None-Match` / `If-Modified-Since`).

### Phase 5: Features
**Status**: 🟡 Partially complete (10 shipped, 4 partial, 2 not started)

#### 5.1 Media

- [ ] 🟡 Media-backend abstraction — ffplay and the embedded player are separate
      paths with a common UI, but there is no interface.
- [x] ✅ Resume position — saved in favorites/recent, throttled while playing.
- [x] ✅ Progress display — Continue card and player overlay.
- [ ] 🟡 Cancellation — playback stop/cancel exists; in-flight downloads are
      only partly context-aware.
- [x] ✅ Clear failure messages — player errors surface as toasts and scene text.
- [ ] ⬜ Playback queue.

#### 5.2 Files

- [x] ✅ File explorer — `features.go`/`paths.go`, sandboxed root, tested path guards.
- [x] ✅ Clipboard integration — clipboard manager (`clipboard.go`).
- [ ] ⬜ Drag-and-drop (Windows).

#### 5.3 Packages

- [x] ✅ Package trust UX — signed manifests, checksum verification, Patch scene.
- [x] ✅ Source/version/checksum display.
- [x] ✅ Uninstall rollback — journaled install/remove (`patch_repo.go`).

#### 5.4 Settings

- [ ] 🟡 Controller remapping — A/B and X/Y swaps plus the one-handed chord
      button exist in the Accessibility panel; a full per-button map does not.
- [x] ✅ Accessibility settings — reduced motion, low power, font sizes, theme
      contrast tokens.
- [ ] 🟡 Power profile selection (single LowPower toggle; no named profiles).
- [x] ✅ Theme preview — scrollable theme gallery with live apply.

### Phase 6: Testing
**Status**: 🟡 Partially complete (11 shipped, 1 partial) — bounds, cache, retry,
persistence and harness gaps closed

- [x] ✅ Config validation tests — `config_schema_test.go`, `config_persist_test.go`.
- [x] ✅ Focus graph navigation tests — `navselection_test.go`, `chrome_nav_test.go`,
      `homefocus_nav_test.go`.
- [x] ✅ Input normalization tests — `controller_swap_test.go`, touch/keyboard tests.
- [x] ✅ Layout bounds tests (1280×720 and smaller) — `layout_bounds_test.go`
      (`BoundsViolations` helper on `HomeLayout`, hit-testing for every card).
- [x] ✅ Cache eviction tests — `textcache_test.go` (LRU, trim, clear, zero capacity,
      key identity).
- [x] ✅ Cancellation tests — `scenetasks_test.go` covers scene-exit and shutdown
      cancellation, a task that finishes after its scene is gone, joining,
      fail-fast groups, parent cancellation, concurrent execution, and a real
      request closed by leaving Podcasts.
- [x] ✅ External process cleanup tests — embedded playback teardown now waits
      for the ffmpeg decoder goroutine (`waitForVideoDecoders`) and the decode
      test fails if it does not exit; this also fixed a reproducible SDL crash
      when a later render test reused the window.
- [x] ✅ URL/path validation tests — `paths_test.go`.
- [x] ✅ Offline/retry behavior tests — `retry_test.go` (500 recovery, 404 no-retry,
      exhaustion, context cancel, unrewindable body).
- [x] ✅ Benchmarks for render, cache, navigation — `hotpath_bench_test.go` covers
      the text hot path and `cache_bench_test.go` the caches. `render_bench_test.go`
      adds the 720p frame budget: a warm home frame, a cold-text home frame
      (theme switch) and one D-pad step. Measured on the desktop proxy
      (i7-1165G7, SDL dummy driver, software renderer):

      ```
      BenchmarkHomeFrame720p          699 µs/frame   24 KB   1588 allocs
      BenchmarkHomeFrame720pColdText  4.62 ms/frame  31 KB   1752 allocs
      BenchmarkHomeNavigation720p     130 ns/step     0 B        0 allocs
      ```
- [x] ✅ Test harness renders at varied resolutions — `probeW`/`probeH` are
      package vars and every offscreen probe reads pixels from a region limited
      to the probe rectangle (`ReadPixels(&Rect{0, 0, probeW, probeH})`)
      instead of the whole shared 1280×720 window, which used to overflow the
      capture buffer through cgo and crash the suite intermittently when a
      probe rendered a smaller frame.
- [ ] 🟡 Platform path selection tests — path guards exist, platform-specific
      selection is only indirectly covered.

### Phase 7: Web Builder
**Status**: 🟡 Mostly complete (5 shipped, 2 partial, 5 not started) — snapping
and guides still missing

- [x] ✅ Semantic design tokens — `style.css` (`--canvas-*`, surfaces, spacing).
- [x] ✅ State previews — preview mode (`script.js`, Ctrl+P).
- [x] ✅ Undo/redo — bounded 60-step stacks.
- [x] ✅ Autosave — localStorage workspace snapshots.
- [x] ✅ Production export — JSON/XML export with round-trip loaders.
- [ ] 🟡 Reusable components — shared CSS classes exist; element markup is still
      assembled per type.
- [ ] 🟡 Trimui performance linting — font-size/asset checks partly covered by
      the loader warnings.
- [ ] ⬜ Snap-to-grid.
- [ ] ⬜ Alignment guides.
- [ ] ⬜ Safe-area overlay (1280×720 device frame).
- [ ] ⬜ Focus-order editor.
- [ ] ⬜ Schema validation in the builder (no `schemaVersion` emitted).

### Phase 8: TrimUI handheld polish
**Status**: 🟡 Started (21 shipped, 13 not started) — interaction feedback and
card rendering landed; the rest is queued

This phase collects the work that specifically targets the TrimUI Smart Pro
(1280×720, ARM64, 1 GB RAM, 60 Hz panel) rather than desktop parity. Shipped
highlights with code evidence:

- [x] ✅ Press feedback — eased press dip on cards/buttons/tiles
      (`anim.go` `uiPressDepth`/`uiPressProgress`), 160/200 ms theme-aware
      scene fades, and haptics via vendor GPIO or SDL rumble (`haptics.go`,
      switch in Settings → General).
- [x] ✅ One icon per home card: `homeActionIconTile` picks the horizontal or
      the stacked composition before drawing, so Files and Settings no longer
      render a wide tile plus a small stacked tile on top of it.
- [x] ✅ Responsive stacked composition for half-width cards, and stacked cards
      keep their subtitle: the tile shrinks just enough for both text lines
      instead of dropping the subtitle and leaving dead space.
- [x] ✅ Texture/memory — bounded thumbnail texture store with a 64 MB handheld
      budget (`thumbstore.go`), byte accounting behind `JUKAHUB_TEXSTATS`
      (`perf.go`), bounded `drawText` label cache (8 MB / 512 entries handheld,
      48 MB / 2048 desktop; `homelayout.go`), and theme changes dropping cached
      text immediately.
- [x] ✅ GC/decode — device-tuned GOGC/soft-heap limits (`applyGCForDevice`:
      GOGC 60 / 384 MB on 1 GB targets, 80 / 768 MB up to 2 GB, stock on
      desktop; explicit env vars always win), debounced cache-pressure
      eviction (once per 15 s at critical / 60 s at warning, off the render
      thread), ffmpeg-side downscale before decode (1280×720 handheld),
      zero-copy frame handoff (a 720p frame is 3.6 MB), and texture recreation
      when a new source has different dimensions.
- [x] ✅ Startup — the home shell draws on frame one, network work waits past
      `Present()` (`deferStartupTask` in `main.go`), and a relaunch within ten
      minutes skips the splash (`startup.go`).

Open:

- [ ] ⬜ Hold-to-repeat on list edges for D-pad scrolling.
- [ ] ⬜ Touch-target audit: ≥44×44 px for every interactive control, safe-area
      insets so cards and footer chips clear the panel's rounded corners, and a
      bigger Exit-chip hit area without changing its drawn size.
- [ ] ⬜ Cached ambient glow instead of re-blending four blobs per frame.
- [x] ✅ Icon atlas for the shared 32 px glyph set — landed in Phase 8b
      (`iconatlas.go`), see that entry for the full description.
- [x] ✅ Row-virtualize the launcher app grid: the render loop visits only
      the row window the panel can show (`appGridVisibleRange` in
      `appgrid.go` — fully visible rows plus one slack row), so a long
      grid's per-frame tile cost is bound by the panel, not the list length;
      off-screen rows are appended lazily as scrolling moves the window, and
      a row straddling the bottom edge is drawn and clipped by the panel
      clip instead of being skipped whole (the scrollbar already said "more
      there"). Window bounds and the pixel consequences (virtualized row
      flat, window row painted, partial row drawn clipped) are pinned by
      `appgrid_window_test.go`.
- [ ] ⬜ Row-virtualize the remaining scrolling lists (game library,
      favorites, save manager, downloads) that still visit every row and
      rely on clipping — the same window arithmetic applies, but each list
      has its own row pitch and selection model, so they are separate edits.
- [x] ✅ Keep thumbnail decoding out of the render path (decode budget of
      2/frame, 8 desktop): `allowThumbnailDecode` (`thumbstore.go`) tokens
      per frame number, so a cold scrolled grid starts at most two decodes–
      uploads per presented frame and defers the surplus into the prefetch
      queue, which re-fills it over the following frames (list fills
      progressively instead of one frame stuttering; `core.go`
      `loadThumbnailFromURLs`, `thumbdecode_test.go`).
- [ ] ⬜ Scene-scoped texture release for scenes that are not open.
- [ ] ⬜ Scene-scoped polling: stop weather/network refreshes for hidden scenes.
- [ ] ⬜ Named power profiles (Battery Saver / Balanced / Performance) mapping to
      Low Power, adaptive FPS and motion in one setting.
- [ ] ⬜ On-device measurement of cold start (<1.5 s interactive), transition
      frame time and idle RSS (<5% CPU at the capped cadence) — all numbers so
      far are desktop proxies.

### Phase 8b: GPU offload on the A133P / PowerVR GE8300
**Status**: 🟡 First steps shipped (renderer selection + per-frame geometry
moved off the CPU); compute offload deliberately not attempted (see findings).

The A133P pairs four Cortex-A53 cores with a PowerVR Rogue GE8300. Research
this revision (sources: SDL2 hint docs, Imagination's own forum thread on the
GE8300, the Mesa PowerVR driver pages, the Knulli/muOS device threads) settled
these constraints:

1. **GLES2 is the only realistic GPU path in Go there.** OpenCL `libOpenCL.so`
   exists inside some GE8300 vendor DDK bundles, but no A133P handheld image
   ships a verified, working OpenCL runtime; Mesa's PowerVR Vulkan driver does
   not support the GE8300; and a Vulkan attempt on the TrimUI Smart Pro already
   failed in the wild (Knulli issue #487). Do not plan pipeline work around
   OpenCL/Vulkan.
2. **A working GLES2 stack is not guaranteed** — it depends on the exact
   userspace/kernel PowerVR driver and the SDL backend in the OS image. The
   practical move is: ask SDL for `opengles2` explicitly, log what we actually
   got, and keep the software renderer as the honest fallback. That is what
   shipped.
3. **The biggest CPU cost in a busy shell was per-frame geometry/gradient
   redraw.** Offloading starts by drawing those once into render-target
   textures and re-issuing one blended Copy per frame — each of those becomes
   a single GPU pass on the GLES2 renderer instead of dozens of CPU fills.

Shipped with code evidence:

- [x] ✅ Shape texture cache (`shapeCache.go`) — `fillRoundedRect`,
      `strokeRoundedRect`, `fillCircle`, `strokeCircle` render their shape once
      into a cached RGBA texture keyed by kind/colour/geometry and draw it with
      one `Copy` per frame (alpha via `SetAlphaMod`). Renderer-local, LRU with
      a 12 MB handheld / 48 MB desktop byte budget, invalidated on renderer
      change and drop-all on theme change (`ClearShapeCache`) and memory
      pressure (`adaptive.go`). Pixel-equivalence proven by
      `roundedrect_render_test.go`, which compares every pixel of the cached
      Copy against the original per-scanline reference for fills, strokes and
      gradients, plus a two-pass cached-vs-direct whole-frame diff test. One
      real bug found and fixed on the way: after drawing into the target
      texture, the old code skipped restoring the render target when
      `GetRenderTarget()` returned `nil` on the default backbuffer, which
      would have painted every cached shape into nowhere.
- [x] ✅ Gradient texture cache (`dynamicbg.go` `bgGrads`) — the wallpaper
      readability scrim (80 one-pixel band fills per frame) and the day-cycle
      backdrop (24 full-screen band fills per frame, re-rendered every frame
      even though the palette barely changes within a slot) are now baked once
      into render-target textures and re-drawn as one `Copy` per frame. Keyed
      by the palette colours (weather/hour changes rebuild once), size and
      renderer; a failed target build latches a failure flag so the per-pixel
      fallback never retries per frame. Cleared alongside the other caches on
      graceful quit and in the memory-pressure critical path.
- [x] ✅ GPU renderer preference + diagnostics (`main.go`) — on non-Windows
      targets SDL is given the `opengles2` hint before renderer creation (falls
      back to the documented default if the stack can't provide it), and after
      creation the app logs `video_driver`, `renderer` name, `acceleration`
      (gpu/software) and `max_texture` — the first on-device run will make
      explicit whether the shell is actually running on the GE8300 or falling
      back to software.
- [x] ✅ Diagnostics verifiable: `renderPipelineInfo`/`logRenderPipeline`
      (`adaptive.go`) extract the four-field snapshot out of `main`, and
      `renderpipeline_test.go` exercises it against a real SDL renderer so the
      diagnostics ship correct by construction, not by hope.

Open / next steps:

- [ ] ⬜ Read the new "render pipeline" log line on the device; if it says
      software, file the exact SDL/EGL driver config against the device image
      (expected: PowerVR userspace + `libGLESv2` present but SDL picking the
      dummy/software video backend). The snapshot itself is now
      construction-verified (`renderpipeline_test.go`); what remains is the
      genuine on-device reading below.

#### What the A133P reports — the render pipeline line

The startup log (text handler on TrimUI; `JUKAHUB_LOG_LEVEL` raises/lowers it)
carries one line from the captured snapshot (see `renderPipelineInfo` in
`adaptive.go` — facts are read from SDL, never inferred):

    level=INFO msg="render pipeline" video_driver=<backend>
    renderer=<name> acceleration=<gpu|software> max_texture=<WxH>

Field expectations for the TrimUI Smart Pro / Smart Pro S / Brick family
(A133P SoC, PowerVR Rogue GE8300), with what each value means for the shell:

- `video_driver` — TrimUI ships its own SDL2 build instead of a stock
  distro SDL: community devices report X11/Wayland absent and set
  `SDL_VIDEODRIVER=mali` (a vendor driver name — *not* a Mali GPU, the GPU is
  PowerVR) in launchers for the `tg5040` family, e.g. the NextUI PPSSPP
  launcher (https://github.com/ben16w/minui-psp, launch.sh). Expect either
  `mali` (stock-adjacent SDL stack) or the generic host default from CI-built
  binaries; this firmware's devices aren't covered by SDL's standard Linux
  drivers list (https://wiki.libsdl.org/SDL2/FAQUsingSDL). Generic SDL
  AppImages fail video init there rather than falling back to software.
- `renderer` — `opengles2` when the PowerVR blob wins (the firmware notes
  update the GLES/Vulkan blobs for exactly this;
  https://github.com/trimui/firmware_smartpro, and runtime reports show
  `OpenGL ES 3.2` / renderer string "PowerVR Rogue GE8300"
  https://github.com/cizia64/CrossMix-OS/issues/8). Anything else should be
  treated as the vendor stack being bypassed.
- `acceleration` — the go/no-go bit: `gpu` means per-frame geometry/batching
  lands on the GE8300 (what Phases 8/8b assume); `software` means every fill
  and cache `Copy` is on the Cortex-A53 CPU and the device image needs the
  EGL/driver fix before further GPU work claims validity.
- `max_texture` — GE8300 builds commonly report `GL_MAX_TEXTURE_SIZE=4096`
  (see https://github.com/google/filament/issues/9387 and
  https://github.com/gpuweb/gpuweb/issues/3631 for the GPU-report corroboration;
  Imagination does not publish a per-model texture cap on the
  https://www.imaginationtech.com/product/ge8300/ page, so the on-device line
  is the authoritative value here). Under the software renderer SDL reports
  `0x0` (its "no limit" representation — construction-verified by
  `TestRenderPipelineInfoUnderSoftwareFallback`, which pins that exact value
  for the dummy-driver fallback). The Brick's kernel driver for this GPU is
  `pvrsrvkm` (https://knulli.org/devices/trimui/brick/).
- [x] ✅ Row-virtualize the launcher app grid — `appGridVisibleRange`
      visits only the visible rows plus one slack row; see the Phase 8 entry
      for the full description. The remaining lists listed there stay open.
- [x] ✅ Icon atlas for the shared 32 px glyph set — `iconatlas.go` bakes each
      (name, focused) glyph once, at the canonical 32px `DrawIconTile` size,
      into one 1024×64 strip and every later occurrence is one Copy of its
      sub-rect. `DrawHomeIcon` (home cards, icon tiles, tool/emulator tiles,
      launcher empty states) used to build each glyph from filled primitives —
      on the GLES2 renderer one cached-shape blit (bind + draw call) each, so a
      card scene issued hundreds of small texture binds per frame just for
      glyphs; it now issues one blit per icon instance from the same bound
      strip. Fidelity is preserved, not approximated: the strip is painted by
      the same `drawHomeIconShapes` primitives the direct path uses, keys are
      renderer-local with `ClearIconAtlas()` riding along every `ClearShapeCache()`
      call site (theme change, memory-pressure eviction, a11y font reload,
      shutdown, test renderers), non-32px callers (46px empty-state, 36px bells,
      list-row glyphs) keep native rendering instead of scaled blits, unknown
      icon names bake the fallback ring, and Copy failures fall back to direct
      paint. Verified in `iconatlas_render_test.go`: bake-once/blit-many with
      the shape cache's per-primitive Copy stream measured at zero deltas on
      warm frames, atlas-vs-direct interior pixels identical at 32px,
      renderer-locality with fresh strips, off-size native rendering, unknown
      names, and the clear/rebake lifecycle.
- [ ] ⬜ ffmpeg hardware decode/VPU offload on the A133P — desk investigation
      done, see docs/VIDEO_VPU_OFFLOAD.md: upstream ffmpeg has no Cedar backend
      and mainline Cedrus does not cover the A133, so the only realistic path is
      a small aarch64 helper dlopen-ing the firmware's CedarX libraries and
      emitting the same yuv420p pipe contract ffmpegDecoderArgs produces, with
      automatic ffmpeg fallback. Blocked on the five on-device probe questions
      (/dev/cedar_dev + /dev/ion, shipped lib manifest, tplayerdemo, frame
      layout, measured CPU headroom); do not ship against unproven vendor
      libraries until those are measured on a TG5040/Brick image, and never
      generalize to the A523-based Smart Pro S.
- [ ] ⬜ OpenCL compute offload — parked until a verified runtime exists on a
      real device image (see finding 1 above); do not ship speculative code
      against unproven vendor libraries.

### Phase 9: Browser & handheld input
**Status**: ✅ Landed this revision (8 shipped, 4 not started) — remaining
follow-ups queued

The modern reader (styled web pages behind the `textbrowser` element) is now
usable without a touchscreen, on the Smart Pro's keyboard-event pad. All
shipped items verified against the code and the test suite this revision:

- [x] ✅ Scroll ⇄ link D-pad model with one cycle key — `handleBrowserKey`
      (Tab) and `handleBrowserController` share `browserNav`: Tab toggles the
      modes with a toast, link mode walks the page's anchors with
      Up/Down/Left/Right, A opens the focused link (focusing the first when
      nothing is picked), and B returns to scroll mode before leaving the
      page; scroll mode keeps the old scrolling/address-bar behaviour. Tests:
      `TestBrowserKeyCyclesScrollAndLinkModes`,
      `TestBrowserControllerWalksLinks`, `TestBrowserLinkCursorWalksThePage`.
- [x] ✅ Focus outline + auto-reveal — `browserDrawPageLinkFocus` outlines the
      focused anchor and `browserRevealLink` scrolls the minimum needed;
      `TestBrowserLinkFocusIsDrawn` renders the outline offscreen.
- [x] ✅ Tap-to-open links — the mouse path hands page taps to
      `browserTapPageLink`, which hit-tests only the anchors on screen at the
      current scroll; `TestBrowserTapOnPageLinkOpensIt`.
- [x] ✅ Link-list panel — X/L opens the modal list of the page's links with
      D-pad rows, A opens the selection, B closes;
      `TestBrowserLinkPanelRendersSelectableRows`.
- [x] ✅ Tools grid draws and hit-tests correctly — the row-wrapping loop built
      each wrapped tile row but never appended it to the drawn list, so half
      the grid was invisible; rows are now appended and the renderer draws in
      panel space so the cursor lands where the hit-test looks (`toolsui.go`,
      `toolsui_render_test.go`).
- [x] ✅ Top-bar chrome focus is order-independent — `useChromeBar`/
      `useTopChrome` snapshot and restore the whole status-button cluster
      (weather, Wi-Fi, media chip, update pill), so a stray rect left by an
      earlier test can no longer insert phantom bar buttons into the
      hand-wired chrome chain (`chrome_nav_test.go`).
- [x] ✅ yt-dlp stays fresh — `resolveToolURL` resolves the latest GitHub
      release asset from the allowlisted repos (`yt-dlp/yt-dlp`,
      `btbn/ffmpeg-builds`), with per-platform asset names pinned by
      `yt_dlp_asset_test.go`.
- [x] ✅ The idle screensaver cannot cover active content —
      `screensaverAutoTick` holds off while a modal owns the UI, audio or
      video is up (including the loading phase, via `playerPhase()`), or a
      download/install runs, and keeps the idle clock moving so the saver does
      not pop the second the activity ends (`screensaver.go`
      `idleScreenHoldOff`, `ambient.go` gate list `ambientEntryBlocked`).

Open:

- [ ] ⬜ Physical pad toggle for the browser mode — the cycle key (Tab) exists
      only on the keyboard-event path; a firmware that reports a real gamepad
      has no equivalent button yet.
- [ ] ⬜ Nearest-row link walking — the cursor follows flat anchor order; in
      multi-column pages the D-pad can jump visually far away. Walk to the
      nearest next link row instead.
- [ ] ⬜ Mode discoverability — a footer chip should name the current mode and
      the keys that switch it, so the feature is findable without this
      document.
- [ ] ⬜ On-device pass — the browser model is verified with the SDL dummy
      renderer; touch hit accuracy, Tab/anchor ergonomics and the link panel
      on real TrimUI hardware still need a hardware run.

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
| Phase 8: Handheld polish | ongoing | Phases 1–4 |
| Phase 9: Browser & handheld input | 1–2 weeks | Phases 1–3 |
| **Total** | **~17–18 weeks** (Phase 8 runs alongside) | - |

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking existing features | High | Test each feature after refactor |
| Performance regression | Medium | Benchmark before/after each phase |
| Build failures | Medium | Run `go vet`, `gofmt` after each change |
| Platform-specific bugs | Medium | Test on both Trimui and Windows |
| Memory leaks | Medium | Use profiling tools, bounded caches |
| Config corruption | High | Atomic writes, backups, safe mode |

## Audit log — 2026-10-24 verification pass

Evidence collected while re-checking every item against this tree:

- Design tokens: typography roles + density scale, adopted by shared components.
- Shared components: `UICard`, `UIBadge`, `UIProgressBar`, `UIListRow`,
  `UIEmptyState`, `UIErrorState`, `UILoadingSkeleton`, and the grid helpers; adopted
  in notifications, Start menu, Quick Settings, generic list rows, Patch's confirm
  button, the bell badge and the theme gallery.
- Config schema versioning: field, migration, load-time refusal of newer files,
  save stamping, persistence tests.
- Safe mode: three triggers, gating for network/plugins/updates/tool install,
  status-bar chip, tests.
- Network retry: bounded attempts, exponential backoff with jitter, `Retry-After`,
  context cancellation, no replay of unrewindable bodies, adopted by Patch metadata
  and weather; tests.
- Text cache: zero/negative capacity clamp (previously an unbounded-cache bug),
  full eviction test coverage.
- Home layout: `BoundsViolations` + viewport/hit-test coverage at 1280×720,
  960×544, 800×480 and 1920×1080.
- Grid: shared math with theme-gallery geometry pinned to it.
- Thumbnail prefetch: bounded worker pool + shared bbolt handle; measured
  105 ms → 56 ms per 8-image batch (1 → 2 workers), cache write 9.8 ms → 3.6 ms;
  see [LIBRARY-EVALUATION.md](LIBRARY-EVALUATION.md).
- Test stability: the embedded-player test now joins the ffmpeg decoder before
  tearing down its renderer, removing a crash that reproduced on roughly a
  quarter of suite runs; eight consecutive full-suite runs passed afterwards.
- Test isolation: debounced user-config writes now capture the directory where
  the change was made, so a queued save can no longer land in the checkout after
  a test restores its working directory (`config_debounce_test.go`). Four
  consecutive full-suite runs left the repository user files byte-identical.
- Log panel: fixed the live viewer to render the most recent log lines, with a
  pure tail-selection helper and tests.
- Scene-scoped cancellation: `scenetasks.go` runs work under the active
  scene's context; leaving a screen closes its requests and a cancelled task
  publishes nothing. Adopted by the podcast/package/weather fetches and by the
  ticker, network-speed and benchmark jobs, which also remove their temporary
  files. The podcast test drives a real scene-exit cancellation through the
  server's request context.
- Coordinated fan-out: the ticker board's independent endpoints now share one
  round trip through `runCancelableGroup` (fail-fast errgroup semantics), with
  the board's order preserved and a failed endpoint reported as `n/a`.
- Log export: an export rotates the previous `logs.txt` to `logs.txt.1` and
  caps the file at 2 MiB; the in-memory ring is byte-capped as well as
  line-capped, so one huge line cannot hold the heap.
- `Ctrl+L` opens the LogExporter scene from anywhere except the terminal, which
  keeps the key for clearing its output.
- 720p budgets: `render_bench_test.go` measures a warm home frame (~700 µs on the
  desktop software-renderer proxy, 1588 allocs), a cold-text frame (~4.6 ms) and
  one D-pad step (~130 ns, allocation-free), so layout and text changes can be
  compared against a number instead of a feeling.
- Race detector: the `player-race` job's failure in
  `TestBrowserToggleAdFilterIsPerSite` is fixed structurally. The browser's
  fetch seam is read once per load under a lock, tests swap it with
  `setBrowserPageFetcherForTest`, and a browser reset joins the loads still in
  flight, so no goroutine outlives the test that started it.
  `TestBrowserFetcherSwapDuringLoadIsSafe` pins the pattern.
- Full suite: `go vet ./...`, `go test ./...` (including `-race` on the new
  concurrency tests), `go test -race ./...` and `go build .` all pass.

- Downloads panel pass against the reference mockup: the SD card, its legend,
  the tabs, the queue rows and their focus now follow the design — a
  metric-driven card (`downloadStorageCardHeight`), an outline focus ring
  (`drawDownloadFocusRing`), two-tone hashed-color art tiles
  (`downloadTileColors`), section headings with counts, and an ellipsized
  right-aligned capacity reading. Verified by rendering offscreen frames at
  1280×720 (1.0× and 1.3× text scale) and 800×480 (1.0× and 1.15×) with real
  storage values, then deleting the temporary probe.

## Next Steps

The phases above are now mostly implemented. The highest-value remaining work:

1. **Phase 9 browser leftovers** — a physical pad button for the scroll⇄link
   toggle so it works on firmware paths that report a gamepad, and nearest-row
   link order so D-pad steps follow visual rows. Small, self-contained and
   testable offscreen.
2. **Controller remapping screen** — the A/B and X/Y swaps in Accessibility are the
   foundation, but there is no per-button keymap layer yet.
3. **Builder snap-to-grid + alignment guides + safe-area overlay** — pure front-end
   work with immediate design-time payoff.
4. **Conditional caching** (`If-None-Match`) for the feeds that already go through
   `retry.go`, so an unchanged package registry or podcast feed costs a 304.
5. **Named power profiles** (Battery Saver / Balanced / Performance) folding the
   existing Low Power, adaptive-FPS and motion switches into one setting.
6. **Debounced search input** — first name the path it is for: the Tube field
   only submits on Enter/its button, and the global search filters in memory, so
   this item is worth doing only where a keystroke really starts work.
7. **Phase 8 handheld polish** — touch-target audit, safe-area insets, cached
   ambient glow, icon atlas, list virtualization, and on-device measurement of
   cold start, transition frame time, idle RSS and the GC/decode tuning above
   (all numbers so far are desktop proxies).
8. **Convert remaining fixed-offset panel layouts to font metrics** — the
   Downloads panel's SD card now derives its rows from `font.Height()`
   (`downloadStorageCardHeight`); panels that still hard-code text offsets
   (Start menu, Quick Settings, the browser chrome) can adopt the same
   pattern so taller accessibility scales never overlap there either.

## Notes

- This is an **incremental** modernization, not a rewrite
- Each phase must produce **working builds**
- Preserve **existing features** unless clearly broken
- Keep **config-driven model** intact
- Optimize for **Trimui constraints** (1GB RAM, ARM64)
- Support **Windows** as secondary target
- Use **Go standard library** where possible
- Add **tests** as we go, not at the end
- Re-verify this document on every pass: update the date on top, re-run
  `go vet ./...`, `go test ./...` and `go build .`, and flip statuses with
  evidence. There is only one source of truth now — no companion file.
