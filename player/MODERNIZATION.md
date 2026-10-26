# JukaHubV2 Modernization Plan

Status of every item below, checked against the code in this tree on
**2026-10-25** (go1.27.2 windows/amd64, SDL2 from `player/.sdl2`):

```
go vet ./...      # exit 0
go test ./...     # ok jukahub/JukaHub, ok jukahub/JukaHub/web
go build .        # exit 0
```

Legend: **✅ shipped** (code exists and is exercised by tests), **🟡 partial**
(some of the item works, the rest is genuinely missing), **⬜ not started**.
Library choices and measured numbers live in
[LIBRARY-EVALUATION.md](LIBRARY-EVALUATION.md).

**Scope, so this stays one list instead of two.** This file is the engineering
status of the player: what the code does today, checked against the tree on every
pass. Project plumbing — GitHub settings, CI jobs, release signing, contributor
ladders, coverage gating — is [../ROADMAP.md](../ROADMAP.md), because those are
facts about the repository rather than the app. The idea lists under `docs/`
([FEATURE-SLATE](../docs/FEATURE-SLATE.md),
[FEATURE-BACKLOG](../docs/FEATURE-BACKLOG.md),
[IMPROVEMENTS](../docs/IMPROVEMENTS.md)) hold **only unadopted ideas**. An idea
becomes work when it is adopted here with a status and a piece of code evidence,
or into the roadmap as a process item — and it then leaves the idea list, so no
idea is tracked in two places. What already shipped out of those lists is
inventoried in [5.5](#55-shipped-feature-surface-verified-2026-10-25).

## Executive Summary

This document outlines the modernization of JukaHubV2 for production use on **Trimui Smart Pro** (1280×720, ARM64, 1GB RAM) and **Windows** (x64, resizable). The goal is to create a controller-first, responsive, reliable handheld launcher while preserving the existing Go + SDL2 runtime and config-driven architecture.

## At a glance

| Phase | Done | Partial | Not started |
|-------|------|---------|-------------|
| 1. UI/UX fixes | 7 | 0 | 0 |
| 2. Architecture | 30 | 2 | 0 |
| 3. Async & performance | 17 | 2 | 3 |
| 4. Reliability | 25 | 0 | 0 |
| 5. Features | 13 | 2 | 3 |
| 6. Testing | 13 | 0 | 0 |
| 7. Web builder | 7 | 2 | 8 |
| 8. Handheld polish | 13 | 2 | 5 |
| 8b. GPU offload | 6 | 0 | 3 |
| 9. Browser & handheld input | 11 | 0 | 1 |

Rows count checkbox items only. The shipped-feature inventory in
[5.5](#55-shipped-feature-surface-verified-2026-10-25) is a record of what
already exists, not a task list, so it is not counted here.

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
**Status**: 🟡 Mostly complete (30 shipped, 2 partial, 0 not started)

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
- [x] ✅ `CategoryTile` — `UICategoryTileDraw` in `components.go`: card, tinted
      icon well, two-line label fitted to the space left, status dot, hover and
      focus ring, and the eased press dip. The Tools page (`menutiles.go`) and
      the Tools overlay (`toolsui.go`) both draw through it; the overlay uses
      the `UITileRaised` surface because its tiles stand on a raised panel where
      the page's card colour would disappear. The home/Apps grid still has its
      own renderer with the same colours — it also handles badges and per-tile
      geometry the component does not own yet.
- [ ] 🟡 `IconButton` — icon geometry helpers exist (`launcher.go`), but buttons are
      still drawn inline per scene.
- [x] ✅ `AppBar` — `UIPageHeader` / `UIPageHeaderDraw` in `components.go`:
      optional glyph well, title, subtitle and a right-aligned status read-out,
      plus a centred form for a brand block. The Tools overlay, the Tools page
      and About all build one, and `uiPageHeaderMetricsFor` is the single place
      that says how tall the band is, so a layout and its draw cannot disagree.
      The section heading the two Tools screens also duplicated is
      `UISectionLabel`.

#### 2.3 Focus System

- [x] ✅ Focus graph per scene — `focus.go` (`FocusEngine`, graph nodes, chrome tiers).
- [x] ✅ Focus preservation on scene return — `Persist`/`Restore`, wired in
      `core.go` scene changes.
- [x] ✅ Deterministic directional navigation — `focus.go` geometry-based movement;
      `navselection_test.go`, `chrome_nav_test.go`, `homefocus_nav_test.go`.
- [x] ✅ Every control reachable on every config-driven scene — the graph's
      per-direction fallback classifies an edge by its dominant axis, so a
      side control with a small vertical offset no longer counts as the one
      above it; `dpad_audit_test.go` walks both shipped configs.
- [x] ✅ Focus animation — hover/focus lift (100–140 ms), reduced-motion aware.
- [x] ✅ Reduced-motion mode — config flag, animation gates, battery-saver test.
- [x] ✅ Controller test screen — `ctrldiag.go`, opened from Power tools; see
      [../docs/CONTROLLER_TEST.md](../docs/CONTROLLER_TEST.md).
- [ ] 🟡 `Focusable` interface — behavior is centralized in `FocusEngine`, but
      there is no Go interface implemented by element types.
- [x] ✅ Input remapping screen — the Accessibility panel's per-button rows
      (`a11yRows`, `player/a11y.go`).

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
**Status**: 🟡 In progress (15 shipped, 2 partial, 5 not started)

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
- [x] ✅ Image-decode worker pool — `decodeImageRGBA` (`bounded.go`) decodes under a
      shared semaphore sized 2 jobs on Trimui / 4 on desktop, so a fan-out of
      image decodes (chat attachments, companion frames) cannot open one RGBA
      buffer per item on a 1 GB device. The dense-RGBA conversion happens while
      the permit is held, which is the whole point of the cap; the chat image
      path now routes through it (`uploadChatImage`). Covered by
      `bounded_test.go` (cap honoured under a 20-wide fan-out, cancellation
      refuses to decode, already-dense input is not re-copied, malformed input
      reports the decode error).
- [x] ✅ General bounded semaphore — `semaphore` (`bounded.go`), a weighted
      context-aware counting semaphore (`Acquire`/`TryAcquire`/`Release`), plus
      the one-shot `runBounded` fan-out helper. Cancellation mid-acquire
      releases whatever it took, so a cancelled caller cannot leak permits.
      Tests in `bounded_test.go`.
- [ ] ⬜ Debounced search input — **investigated, deliberately not done.** The
      candidate paths do not start work per keystroke: the Tube field only
      submits on Enter/its button, and the global search (`gsSearch` in
      `globalsearch.go`) filters the already-loaded `gsApps` slice in memory.
      A debounce would add latency to an operation that costs microseconds, so
      the item's own precondition ("a keystroke really starts work") is not met
      by any current search. Reopen only if a search gains a network or disk
      backend.

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

- [x] ✅ Cached text textures — `globalTextCache`, pressure trimming.
- [x] ✅ Clipping for lists/grids — `renderWithClip`.
- [x] ✅ Idle render mode — `FrameRateController` drops cadence when nothing animates.
- [x] ✅ Frame time budget — adaptive delay + low-power cadence (`adaptive.go`,
      `adaptive_test.go`).
- [x] ✅ Worker-thread texture audit — every `CreateTexture` call site is on a
      render path: the thumbnail workers fetch bytes only, and
      `loadThumbnailFromURLs` creates and uploads the texture while drawing. So
      there was no `LockOSThread` change to make, and the black-texture/SDL
      crash that looked like a worker-thread bug was the renderer-owned cache
      reuse recorded in Phase 6 (`renderer_cache_owner_test.go`).
- [ ] 🟡 Object reuse for cards/labels/images — card surfaces are stateless helpers;
      per-frame text layout still allocates.
- [ ] ⬜ Icon atlas as above.

#### 3.4 Power Management

- [x] ✅ Reduce animation in battery saver — motion gates (`anim_test.go`).
- [x] ✅ Named power profiles (Battery Saver / Balanced / Performance) —
      a "Power Mode" selector (`powermode.go`, `<variables>.powerMode`) now
      maps all three onto one value: Battery Saver forces Low Power + reduced
      motion, Performance pins the frame rate controller at the active
      cadence, Balanced is the smooth middle, and Default hands everything
      back to the individual toggles. Row in Quick Settings with A/←/→
      cycling, a toast and a persisted save; while a mode is live its two
      managed pills show the mode's verdict and their presses explain where
      the control lives (`powermode_test.go`).
- [ ] ⬜ Stop polling when a scene is hidden (weather ticker and monitors run for
      the whole process lifetime).

### Phase 4: Reliability & Configuration
**Status**: 🟡 Mostly complete (24 shipped, 1 partial, 0 not started)

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
- [x] ✅ Cached fallback for offline content — thumbnails have a disk cache; feeds
      now do too. `feedcache.go` keeps a bounded last-known-good copy of each
      podcast feed in the shared bbolt cache (30-day window, 1 MiB cap per feed)
      and `httpGetTextFallbackContext` serves it **only when the network fetch
      fails outright** — a live response, a 304, or an HTTP error status all win
      first, so an offline boot shows the last feed the user saw instead of an
      empty screen, and a feed that is genuinely gone (404) is never replayed as
      if it were live. Tick/quote endpoints deliberately do not use it: a stale
      price is worse than "n/a". Covered by `feedcache_test.go` (offline serves
      the stored copy, live fetches are unaffected, oversize bodies are not
      stored, the copy round-trips to disk).

#### 4.4 Network

- [x] ✅ Shared HTTP clients — `net.go` (`httpClientLong`) plus Patch's bounded client.
- [x] ✅ Connection reuse — keep-alive transport.
- [x] ✅ Request timeouts — per-client and per-context.
- [x] ✅ Max response sizes — `fetchBounded`, `downloadToFile`.
- [x] ✅ Retry with exponential backoff + jitter — `retry.go`, honors
      `Retry-After`, retries 408/425/429/5xx and transport errors, refuses to
      replay an unrewindable body; adopted by the Patch metadata fetch and
      weather forecast; covered by `retry_test.go`.
- [x] ✅ Conditional caching (`If-None-Match` / `If-Modified-Since`) —
      `httpcache.go` keeps the last successful body and its validators per URL
      (bounded: 64 entries, 1 MiB per body, process-lifetime by design — a
      restart costs one full fetch and an ETag from an older decoder can never
      outlive the build that wrote it). `fetchBounded` (release manifest) and
      `httpGetTextContext` (podcast feeds, ticker/quote endpoints) send the
      validators and answer an unchanged resource from the cache, so the update
      check and a screen re-entry cost a 304 instead of the body. A server that
      answers 304 for a URL this process holds no copy of is reported as an
      error rather than treated as an empty document, `Cache-Control: no-store`
      is honoured, and an unvalidated or oversized response drops its cached
      copy instead of leaving a body a later 304 could resurrect. Covered by `httpcache_test.go` (ETag path,
      Last-Modified path, changed content adopted, both call paths, the
      uncacheable-304 contract, eviction, oversize skip, concurrent use).

### Phase 5: Features
**Status**: 🟡 Partially complete (11 shipped, 3 partial, 4 not started)

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
- [x] ✅ Drag-and-drop (Windows) — a file dropped on the window is routed by
      `handleFileDrop` (`dropfile.go`): media plays through the embedded player,
      images open in the viewer, a folder is pointed at Files, and an unknown
      extension says so instead of silently ignoring the drop. A path that does
      not exist is refused up front (stat before route), never deep inside a
      decoder. The event is wired into the main loop (`main.go` DropEvent case);
      the handheld never fires it (no pointer). Covered by `dropfile_test.go`
      (extension routing incl. query-string stripping, the image table, missing
      and empty paths are no-ops).

#### 5.3 Packages

- [x] ✅ Package trust UX — signed manifests, checksum verification, Patch scene.
- [x] ✅ Source/version/checksum display.
- [x] ✅ Uninstall rollback — journaled install/remove (`patch_repo.go`).

#### 5.4 Settings

- [x] ✅ Controller remapping — every face and shoulder button has its own row
      in the Accessibility panel, cycling through the actions and ending at
      **Disabled**; the layout preset is a whole-state shortcut and the Select
      row still moves or disables the BACK/START pair together.
- [x] ✅ Accessibility settings — reduced motion, low power, font sizes, theme
      contrast tokens.
- [x] ✅ Power profile selection — the named "Power Mode" (Battery Saver /
      Balanced / Performance / Default) is now reachable from the Accessibility
      panel as its own row (`a11yRowPowerMode` in `a11y.go`), stepping through
      the same `cyclePowerModeTo` helper Quick Settings uses so there is one
      place a mode becomes live state. It reports the saved mode and reuses the
      existing effective-Low-Power / effective-Reduced-Motion verdicts.
      `a11y_powermode_test.go` pins the row's presence, its value, and that a
      step persists and drives the low-power verdict.
- [x] ✅ Theme preview — scrollable theme gallery with live apply.

#### 5.5 Shipped feature surface (verified 2026-10-25)

Everything below was the subject of an idea in
[FEATURE-SLATE](../docs/FEATURE-SLATE.md),
[FEATURE-BACKLOG](../docs/FEATURE-BACKLOG.md) or
[IMPROVEMENTS](../docs/IMPROVEMENTS.md), or was requested outright, and is
implemented in this tree. It is recorded once, here, so those pages can stay
forward-looking instead of carrying a shipped register each. Each row names the
code and the test that pins it.

| Feature | Where it lives | Pinned by |
|---|---|---|
| Continue card merging game and media recents | `recent.go` (`getRecentItems`) | `continue_games_test.go` |
| Offline weather from the last good forecast | `weather_cache.go` | `weather_cache_test.go` |
| LAN Play panel: RetroArch netplay host/join | `netplay.go`, `netplay_ui.go` | `netplay_test.go`, `netplay_ui_test.go` |
| `NetPlayEnabled` gate with a Quick Settings switch | `netplay_ui.go` | `netplay_gate_test.go` |
| Save-state resume (RetroArch auto save/load) | `savestate_resume.go` | `savestate_resume_test.go` |
| Box-art fetch for missing covers, with a 30-day not-found cache | `game_library.go`, `game-art/.missing-art.json` | `artwork_fetch_test.go` |
| Library completeness ring and resumable fill-the-gaps | `game_library_ui.go` | `game_library_test.go` |
| Saved browser cookies expire on a chosen window (Keep/7/14/30/90 days in Settings → General); per-cookie first-seen age, prune on load and on change; the row also carries when the session was first stored (`Cookies · 30d · since Oct 12`, the oldest stamp on the card) as the window's reference point; the **Forget now** press beside the row that drops the session from Settings with the Browser chip's shared two-press arm | `browsercookies_retention.go`, `widgets.go`, `core.go`, [BROWSER](../docs/BROWSER.md#dropping-them-after-a-while) | `browsercookies_retention_test.go`, `browsercookies_forgetrow_test.go`, `browsercookies_age_test.go` |
| Saved browser cookie jar on the card (mode 0600, restored on first use), the two-press **Forget** chip carrying the count, and the strip under the page naming the hosts those cookies belong to | `browsercookies.go`, `browser.go`, `textbrowser.go`, [BROWSER](../docs/BROWSER.md#forgetting-them) | `browsercookies_persist_test.go`, `browserforgetchip_test.go`, `browser_cookie_sites_test.go` |
| Video snaps in the library: a card-local clip plays in the preview box after a short focus dwell (320×180, 10fps, muted, looped; `gameSnaps` / `gameSnapsDir`), letterboxed with a SNAP chip and the cover as the fallback, switched from Settings → General → **Video snaps** so no JSON edit is needed, and that row reports how many of the library's games have a clip (`On · 12`, `On · none`) from a library scan taken off the render thread when the page opens | `snappreview.go`, `snappreview_count.go`, `game_library_ui.go`, [GAME_LIBRARY](../docs/GAME_LIBRARY.md#video-snaps) | `snappreview_test.go`, `snappreview_render_test.go`, `snappreview_setting_test.go`, `snappreview_count_test.go` |
| Recordings screen over RetroArch's recording folder | `recordings.go`, [RECORDINGS](../docs/RECORDINGS.md) | `recordings_test.go` |
| LAN watch party and PIN/QR phone companion | `partynet.go`, `companion.go` | `party_test.go`, `companion_test.go` |
| RetroAchievements (softcore; hardcore deliberately off) | `retroachievements.go` | `retroachievements_test.go` |
| Per-ROM box art from libretro-thumbnails during ROM setup | `romsetup.go` | `romsetup_test.go` |
| Screenshot of the hub's own frame | `screenshot.go` | `imageviewer_test.go` |
| LED sync and LED policies (audio, battery, charging, temperature, custom colour/pulse) | `ledsync.go` | `ledpolicy_test.go`, `ledsync_test.go` |
| Component-versions report (app, launcher, config, bundled tools, packages; SHA-256) with **A** to copy and the same section in the diagnostics export | Patch → **Component versions**, `componentreport.go` | `componentreport_test.go` |
| Controller legend card, first run and from Quick Settings; its **Find it in Tools** section names the Toolbox and Security & trust pages, which the tour's last step also covers | `controlslegend.go`, `onboarding.go` | `controlslegend_test.go`, `onboarding_test.go` |
| User theme store: `themes/*.json` into the gallery, invalid files skipped | `themeloader.go` | `themeloader_test.go` |
| Auto time-lapse of a playthrough (`/dev/fb0` frames, ffmpeg mp4 on exit) | `timelapse.go`, [TIMELAPSE](../docs/TIMELAPSE.md) | `timelapse_test.go` |
| Clock with calendar/alarm popover, and the weather popover | `clock.go`, `weather_popover.go` | `calendar_popover_test.go`, `weather_popover_test.go` |
| Now-playing dock and the ambient photo slideshow | `ambient.go` | `ambient_test.go` |
| Home Assistant entities, MQTT mirror, Pi-hole/router/Tailscale/WireGuard state | `smart_home.go` | `smart_home_test.go` |
| Disk and battery readings in Hardware and the status cluster | `hardware.go` | `hardware_poll_test.go` |
| Built-in minigames (Snake, Tetris, 2048, Pong, Minesweeper, Flappy) | Games scene, `games.go` | `funpack_test.go` |
| Phone keyboard bridge (text and key events) | `companion.go` | `companion_test.go` |
| Syncthing save-sync export (folder list + resume zip) | `savemanager.go` | `savemanager_test.go` |
| Animated backgrounds and the boot animation | `dynamicbg.go`, `backgroundfx.go`, `splash.go` | `anim_test.go`, `motion_test.go` |
| Seasonal theming — **partial**: time-of-day switching ships, date-keyed packs do not | `themes.go` | `themes_test.go` |
| Toolbox: nine curated CLI tools (rclone, jq, ripgrep, fd, pandoc, age plus the network scanners naabu, httpx, dnsx) installed on demand from allowlisted releases, run with typed arguments and visible output; scanner runs sit behind a one-time authorized-use acknowledgement stored in the config | `toolbox.go`, `powertools.go`, `vkeyboard.go`, [TOOLBOX](../docs/TOOLBOX.md#network-scanning), [SECURITY.md](../SECURITY.md#toolbox-and-helper-tool-downloads) | `toolbox_test.go`, `toolbox_scan_gate_test.go` |
| Security & trust page: plaintext secrets, storage key, package count, LAN listeners, browser session and telemetry state, read-only | `toolbox.go` (`securityPostureRows`), `toolsui.go` | `toolbox_test.go` |

#### 5.6 Adopted, not started

Ideas that have been accepted into this plan and do not have code yet. Being
here is a decision, not a schedule.

- [ ] ⬜ **Playtime Wrapped / insights** — adopted as the next feature build: a
      90-day heatmap from `playtimeData.Days`, top systems from `Scenes`, the
      current streak, and a PNG the phone companion can show. No new tracking;
      the data is already collected.
- [ ] ⬜ **Checkpoints / quick switch** — the largest item ever put on the
      backlog, designed in [CHECKPOINTS.md](../docs/CHECKPOINTS.md) and
      explicitly unimplemented. Named per-game save slots need core-specific
      state handling, and a real switcher needs two suspended emulator processes
      on a 1 GB device.

### Phase 6: Testing
**Status**: 🟡 Partially complete (12 shipped, 1 partial) — bounds, cache, retry,
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
- [x] ✅ Renderer-owned texture caches — the keyboard, chat and video-overlay
      caches record their owning renderer, drop a foreign texture without
      touching it, and release their textures before teardown. The intermittent
      `SDL_RenderReadPixels` crash was this reuse and not the thumbnail path, so
      the flaky test was fixed at its cause rather than quarantined
      (`renderer_cache_owner_test.go`).
- [x] ✅ Platform path selection tests — `platform_paths_test.go` covers the
      per-implementation behaviour directly rather than through callers: the
      GOOS-driven `InitPlatform` selection, distinct config/user-config paths,
      the handheld vs desktop window/handheld flag pairs, `RequiredDir` aliasing
      `ToolsDir` (install and lookup must agree), the well-known `required`
      folder name both platforms resolve into, and the Windows-only `.exe`
      suffix on `LookPath` (with the Linux side pinned to NOT add one).

### Phase 7: Web Builder
**Status**: 🟡 Mostly complete (6 shipped, 2 partial, 9 not started) — snapping,
guides, a module split and the CSP are still missing

- [x] ✅ Semantic design tokens — `style.css` (`--canvas-*`, surfaces, spacing).
- [x] ✅ State previews — preview mode (`script.js`, Ctrl+P).
- [x] ✅ Undo/redo — bounded 60-step stacks.
- [x] ✅ Autosave — localStorage workspace snapshots.
- [x] ✅ Production export — JSON/XML export with round-trip loaders.
- [ ] 🟡 Reusable components — shared CSS classes exist; element markup is still
      assembled per type.
- [ ] 🟡 Trimui performance linting — font-size/asset checks partly covered by
      the loader warnings.
- [x] ✅ Stylesheet repair and end-to-end coverage — `style.css` parses at brace
      depth 0 with no orphan declarations, the AI panel and four destroyed hover
      rules are back, and the Playwright suite (drop-at-scale, zoom round-trip,
      AI panel visible) guards them.
- [ ] ⬜ Split `script.js` into modules — the largest single-file risk in the
      repository.
- [ ] ⬜ Content-Security-Policy, once the allow-list exists for inline styles and
      the LAN device pushes over `http:`.
- [ ] ⬜ Self-hosted fonts instead of the CDN.
- [ ] ⬜ PWA / service worker — only with a stale-cache recovery story, and the
      favicon is currently below the installable minimum.
- [ ] ⬜ Snap-to-grid.
- [ ] ⬜ Alignment guides.
- [ ] ⬜ Safe-area overlay (1280×720 device frame).
- [ ] ⬜ Focus-order editor.
- [x] ✅ Schema stamp in the builder — `buildEditorConfig` (`script.js`) now emits
      `schemaVersion` on every export, so a file the builder writes carries the
      version the player checks on load instead of none. An imported design that
      already has a version keeps it (re-exporting an older design does not
      silently downgrade the stamp); a fresh project stamps the current
      `CONFIG_SCHEMA_VERSION` (1). The 17 web builder tests still pass.

### Phase 8: TrimUI handheld polish
**Status**: 🟡 Started (13 shipped, 1 partial, 6 not started) — interaction feedback,
card rendering, the Tools page and a repeating D-pad landed; the rest is queued

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

- [x] ✅ Hold-to-repeat on the D-pad in menus — the on-screen keyboard had
      polled the pad for a while (`vkTickNavRepeat`), but every other screen
      needed one press per row, so a two-hundred-game library was two hundred
      taps. `navrepeat.go` watches the held direction and, after a 320 ms delay,
      re-queues the same D-pad press through SDL at a 70 ms cadence, so the
      router stays in charge: overlays, modals, the video player and the
      keyboard all see ordinary presses. The analog stick's existing repeat
      (`handleStickDpad`) is untouched, the keyboard keeps moving its own
      highlight instead (`padTickRepeat` stands aside while it is up), and the
      accessibility slow-repeat setting doubles both intervals. Covered by
      `navrepeat_test.go` (cadence, direction change, release, press queue,
      diagonal preference, keyboard suppression, slow repeat).
- [ ] 🟡 Touch-target audit — the Exit-chip half is done: `growTouchTarget`
      (`touchtarget.go`) grows a drawn control's hit area to a 44px minimum,
      centred on what is drawn, so the 36px Exit pill absorbs a near-miss tap
      without its drawn geometry moving. `HomeLayout.FocusAt` now hit-tests
      against the grown rect, and `touchtarget_test.go` pins the enlargement, the
      centre-preserving growth, the near-miss, and that a degenerate rect never
      grows into a phantom target. Still open: applying the same growth to the
      remaining interactive controls scene by scene, and the safe-area insets so
      cards and footer chips clear the panel's rounded corners.
- [x] ✅ Cached ambient glow — verified stale: `backgroundfx.go` bakes one
      256×256 radial glow texture once and per frame is four tinted `Copy` calls
      with `SetColorMod`/`SetAlphaMod`, not four per-pixel blends. The item was
      already done when it was written down.
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
- [x] ✅ The classic Tools menu is a launcher page — the config's `Misc` scene
      was a wall of wide buttons with no icons and no title. It now draws a page
      header (glyph, title, subtitle, Wi-Fi address), section headings
      (SYSTEM, DIAGNOSTICS, UPDATES & TRANSFER) and icon tiles sized to the
      content panel, grouped by what each tool does (`menutiles.go`). The
      element order, indices and triggers are untouched, so focus memory, D-pad
      navigation, hover and clicks all follow the tiles; the layout is written
      into the in-memory config at scene entry and detached again for a config
      save, and a panel too short for the page keeps the authored buttons
      (`menutiles_test.go`). This is also the shape a reusable `CategoryTile`
      component should take — see the Phase 2 note.
- [x] ✅ The Settings menu as a page — Settings was the same kind of screen the
      Tools menu was (buttons on a fixed grid), and it now draws through the same
      layout with a profile of its own: a page header (Settings glyph, title,
      subtitle), section headings (APPEARANCE, SOUND, CONNECTIVITY, SYSTEM) and
      icon tiles, grouped by what each category changes (`menutiles.go`,
      `settingsMenuPage`). A menu is a `menuPageProfile` — sections, grouping
      rule, row and heading glyphs, subtitle, header status — so Tools and
      Settings share one layout and the next menu is data, not code. Sections
      whose rows would be all toggles or sliders never appear: the profile only
      keeps sections that have at least one button, so a Settings scene that
      gains a toggle element keeps the generic renderer and its authored
      geometry. Not done: nothing forces Settings to stay a page if a config
      author adds a non-button element to it (`menutiles_test.go`).
- [ ] ⬜ Scene-scoped texture release for scenes that are not open.
- [ ] 🟡 Scene-scoped polling — started with the disk pie: `startDiskPieAutoRefresh`
      used to statfs the card every five seconds for the whole session, so a
      hidden Disk Space scene kept the CPU awake for a chart nobody was looking
      at. The ticker now skips while the active scene has no `piechart` element
      (`sceneHasPieChart`) and scene entry takes a fresh reading instead
      (`hardware_poll_test.go`). What stays open: the weather ticker is a
      process-lifetime ten-minute loop, which is deliberate — the forecast feeds
      the status cluster on every screen — but the Weather screen's own refresh
      and the network monitor are still not scoped to the screen that shows them.
- [ ] ⬜ On-device measurement of cold start (<1.5 s interactive), transition
      frame time and idle RSS (<5% CPU at the capped cadence) — all numbers so
      far are desktop proxies.

**Needs a hardware session** (no code change can settle these):

- [ ] ⬜ Wi-Fi picker verified on both stock images (Smart Pro and Brick):
      interface name, `wpa_cli` path and DHCP client confirmed on the device. The
      picker already polls `status` for the requested SSID instead of trusting
      `select_network`, asks for a lease and re-reads the address, and reports a
      failed `save_config` as session-only — none of that replaces one session on
      each image. The status meter no longer depends on the proc table either:
      it falls back to `wpa_cli signal_poll` off-thread when
      `/proc/net/wireless` is missing or reports no measurement, and the
      picker's CONNECTED row now reads through that same chain instead of the
      scan table, so the session is confirming a path rather than discovering
      one (`wifi_test.go`, `statusbar_status_test.go`,
      `wifi_row_bars_test.go`).
- [ ] ⬜ LAN Play verified end to end between two machines on one network. The
      command builder, address parsing, core mapping and both preflights are
      unit-tested; the handshake is not.

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
**Status**: ✅ Landed this revision (12 shipped, 1 not started) — remaining
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

- [x] ✅ Physical pad toggle for the browser mode — **L3** (left stick click)
      cycles scroll ⇄ link mode on the controller path, alongside Tab on the
      keyboard path, and the toggle toast names both keys. `handleBrowserController`
      owns the button for web panels only; plain-text panels keep it for the
      scene behind them (`TestBrowserControllerTogglesNavMode`).
- [x] ✅ Nearest-row link walking — Up/Down in link mode now follow the page's
      visual rows instead of flat anchor order: `browserLinkRowStep` picks the
      adjacent row and, within it, the link nearest the current column, wrapping
      between the first and last rows (and staying put on a one-row page).
      Left/Right keep reading order. Covered by `TestBrowserLinkRowStepWalksColumns`
      (two-column walk, nearest-column pick, both wraps, one-row no-op), while
      the existing controller/keyboard walk tests still pass through the new
      row step.
- [x] ✅ Mode discoverability — the web toolbar shows a **SCROLL** / **LINKS**
      chip (`textBrowserModeRect`, `player/textbrowser.go`) that reads the live
      `browserNav` state, turns accent-coloured while the D-pad walks links, and
      is a tap target of its own, so the mode is visible without waiting for the
      toggle toast. Verified by the layout tests (including which chip a narrow
      panel drops) and `TestBrowserModeChipIsDrawn`, which diffs two rendered
      frames inside the chip's own rect.
- [x] ✅ Touch-sized chrome — the toolbar chips (32px drawn) and the cookie
      strip test against 44px tap targets centred on their drawn rects
      (`textBrowserTouchTarget`, applied in the click path); the two-canvas
      render test asserts the growth and that the drawn geometry never moves.
      This is the browser's slice of the Phase 8 touch-target audit.
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

## Audit log — 2026-10-25 (second pass)

Re-read every open item against this tree and worked the ones that were small,
self-contained and user-visible. Evidence:

- **Stale item found and corrected:** "cached ambient glow instead of
  re-blending four blobs per frame" was already shipped — `backgroundfx.go`
  bakes one radial texture and issues four tinted `Copy` calls per frame. It is
  marked done rather than left as a task somebody would re-do.
- **D-pad hold-to-repeat now works in menus** (`navrepeat.go`): the keyboard was
  the only screen that repeated a held direction. The tracker and its 320 ms /
  70 ms cadence moved out of `vkeyboard.go` into the shared file; menu presses
  are re-queued through SDL so the existing router (overlays → scene → elements)
  decides what a repeat means, the keyboard path keeps its own highlight move,
  and the accessibility slow-repeat setting doubles both intervals.
- **One hidden-screen poller retired:** `startDiskPieAutoRefresh` ran a 5 s
  statfs loop for the whole session; it now skips while the active scene has no
  `piechart` element and the scene takes a fresh reading on entry. The weather
  ticker stays global on purpose (its forecast is on every screen's status
  cluster), which is now written down instead of implied.
- **Tools menu became a launcher page** (`menutiles.go`, see Phase 8): the
  clearest remaining design mismatch after the Apps grid was restyled.
- **Gaps the plan did not name**, now closed: the shared `AppBar` consolidated
  its three inline copies (Tools overlay, Tools page, About), the two tile
  drawings merged into `UICategoryTile`, and the Settings menu moved onto the
  same page layout as Tools through a `menuPageProfile`.
- Checks after the changes: `gofmt -l` clean on the touched files,
  `go vet ./...` exit 0, `go test -count=1 ./...` green, including the new
  `navrepeat_test.go` and `hardware_poll_test.go`.

## Audit log — 2026-10-25 verification pass

- Conditional requests: `httpcache.go` now holds ETag / Last-Modified plus the
  body per URL (64 entries, 1 MiB bodies, in-memory), and `fetchBounded`
  (release manifest) and `httpGetTextContext` (podcast feeds, ticker and quote
  endpoints) revalidate. `httpcache_test.go` covers the ETag and Last-Modified
  paths end to end, changed content, the uncacheable-304 contract, eviction,
  oversize skip and concurrent use.
- Browser input: **L3** cycles scroll ⇄ link mode on the controller path, and
  Up/Down in link mode walk the nearest link row (`browserLinkRowStep`) instead
  of flat anchor order, with Left/Right keeping reading order. The toggle toast
  names Tab and L3. Tests: `TestBrowserControllerTogglesNavMode`,
  `TestBrowserLinkRowStepWalksColumns`, plus the existing keyboard/controller
  link-walk tests that now pass through the row step.

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

1. **Playtime Wrapped** — adopted next (Phase 5.6): the data is already there, so
   this is a page, a heatmap, a streak and a PNG export.
2. **Builder snap-to-grid + alignment guides + safe-area overlay** — pure front-end
   work with immediate design-time payoff.
3. **Debounced search input** — first name the path it is for: the Tube field
   only submits on Enter/its button, and the global search filters in memory, so
   this item is worth doing only where a keystroke really starts work.
4. **Phase 8 handheld polish** — touch-target audit, safe-area insets, cached
   ambient glow, list virtualization, and on-device measurement of
   cold start, transition frame time, idle RSS and the GC/decode tuning above
   (all numbers so far are desktop proxies).
5. **Convert remaining fixed-offset panel layouts to font metrics** — the
   Downloads panel's SD card now derives its rows from `font.Height()`
   (`downloadStorageCardHeight`); panels that still hard-code text offsets
   (Start menu, Quick Settings, the browser chrome) can adopt the same
   pattern so taller accessibility scales never overlap there either.
6. **Move more screens onto `UIPageHeader` / `UICategoryTile`** — the Settings
   menu is the next fixed grid, and the Downloads and Power tools panels still
   draw their own header band.
7. **Measure the hold-to-repeat cadence on the device** — 320 ms / 70 ms is the
   keyboard's long-standing value and feels right on a list, but a 1 GB ARM64
   device under a slow SD card may want a shorter delay. It is one constant in
   `navrepeat.go`; a hardware pass should confirm it before it is treated as
   settled.

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
  evidence. Engineering status lives only here — there is no companion
  checklist file; project-side work lives in the roadmap (see Scope at the
  top), so neither list has to guess what the other one already covers.
