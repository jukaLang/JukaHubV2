# JukaHubV2 Modernization Plan

Status of the roadmap items below, checked against the code in this tree on
**2026-10-29**. For a concise overview of progress and improvement priorities,
start at [the project status page](../docs/PROJECT_STATUS.md); this is the detailed
evidence and phase checklist. “Partial” describes implementation state, not an
assigned owner or a promise that work is actively underway. The current Windows
build/launch path was also exercised via
[`build.bat`](build.bat) (Go 1.27.2, GCC 14.2.0, local SDL2): build succeeded
and the executable launched. The broader Go test/vet results recorded in the
2026-10-26 audit below have not been rerun as part of this documentation pass.

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
| 2. Architecture | 32 | 2 | 0 |
| 3. Async & performance | 18 | 2 | 1 |
| 4. Reliability | 26 | 0 | 1 |
| 5. Features | 14 | 2 | 2 |
| 6. Testing | 13 | 0 | 0 |
| 7. Web builder | 7 | 2 | 8 |
| 8. Handheld polish | 14 | 2 | 4 |
| 8b. GPU offload | 6 | 0 | 3 |
| 9. Browser & handheld input | 12 | 0 | 1 |

Rows count checkbox items only. The shipped-feature inventory in
[5.5](#55-shipped-feature-surface-verified-2026-10-25) is a record of what
already exists, not a task list, so it is not counted here.

## Current State Audit

### Architecture Overview

**Runtime**: Go 1.27 + SDL2 (go-sdl2) + SDL_ttf + SDL_image\
**Config Model**: JSON-driven (`jukaconfig.json`, `jukauser.json`)\
**Rendering**: Single-threaded SDL2 renderer; shared text/shape/icon caches and bounded hot paths\
**State Management**: Package-level state, with ownership split across feature files\
**Package Structure**: Single `player/` package with feature-focused files

### Key Files

| File | Purpose | Status |
|------|---------|--------|
| `main.go` | Main render loop, input handling, scene dispatch | 2,984 lines; still owns the loop |
| `design.go` | Semantic colors, spacing, typography, motion and density tokens | Shared design system |
| `themes.go` | Theme presets and application | Semantic colors and surface roles |
| `extras.go` | Status bar, error bar, footer hints | Shared status cluster and rendered footer |
| `launcher.go` | Home/app tile icon rendering helpers | Opaque tile surfaces and shared icon drawing |
| `homelayout.go` | Home screen layout computation | Responsive grid and home hit testing |
| `recent.go` | Continue/recent panel | Merged game/media recents and resume state |
| `jukaconfig.json` | Scene configuration | Config-driven scenes; the Home grid is drawn from `homelayout.go` |

### Current Issues Identified

#### 1. Rendering Issues (Fixed in Phase 1)
- ✅ Card rendering: Removed washed-out top highlight
- ✅ Footer rendering: Added controller hints
- ✅ Continue panel: Fixed to use layout tokens
- ✅ Status bar: Simplified for non-Main scenes
- ✅ Icon rendering: Made backgrounds opaque

#### 2. Architecture Issues (Phase 2)
- 🟡 **Large main.go**: split into core/extras/feature files, but `main.go` (~3k lines) still owns the loop
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
- ✅ Header focus visibility: every drawn header control that accepts focus is checked for a visible focus ring (`header_test.go`, `TestEveryFocusableHeaderControlDrawsFocus`).
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
- 🟡 Primary long lists bound row drawing to the visible window; audit the remaining screens for lists that can grow without bound (Phase 8)
- 🟡 Idle frame cadence and foreground-aware polling exist; polling is not yet consistently scoped to the screen or resource that needs it
- ✅ Named power modes (Battery Saver / Balanced / Performance / Default)
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
**Status**: 🟡 Mostly complete (32 shipped, 2 partial, 0 not started)

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
- [x] ✅ Header focus visibility audit — every drawn header control that accepts
      focus has a visible ring when selected; `TestEveryFocusableHeaderControlDrawsFocus`
      renders each eligible control and checks the ring pixels (`header_test.go`).

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
**Status**: 🟡 In progress (18 shipped, 2 partial, 1 not started)

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
- [x] ✅ Stop polling while JukaHub is not on screen — `backgroundwork.go` is one
      gate (`backgroundPaused`: a game/emulator/port owns the framebuffer, or the
      desktop window is minimised) that the weather ticker, the connectivity
      probe, the audio-output probe and the LED animation all wait through
      (`waitForPoll`), so a session behind a game no longer forks bluetoothctl,
      dials a probe host, fetches a forecast or writes sysfs thirty times a
      second for a panel it cannot see. The ticks stay, so returning costs one
      interval at most, and the transition back to the foreground (the last
      screen-taking child exiting, or a store/restore of the window) broadcasts a
      wake that refreshes the slow readings — the forecast is on a ten-minute
      timer — immediately. The disk pie's existing "a hidden screen does not
      poll" check is the same rule applied to its scene (`backgroundwork_test.go`).

### Phase 4: Reliability & Configuration
**Status**: 🟡 In progress (26 shipped, 0 partial, 1 not started)

- [ ] ⬜ Snapshot restore integrity preflight — `saveSnapshotFile` records each
      file's expected size and SHA-256 in `savemanager.go`, but
      `restoreSaveSnapshot` currently copies entries without checking either
      value and can begin writing before discovering a later missing/corrupt
      file. Validate the manifest paths, sizes and hashes for every source file
      before creating the safety snapshot or mutating live save roots; add tests
      proving corrupt/missing input leaves the live saves untouched.

The Phase 4 count now includes this preflight item; the earlier 4.1–4.5
configuration, logging, recovery, update and network safeguards remain shipped.

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
- [x] ✅ Windows Wi-Fi operations stay on the selected adapter — scans,
      profile import/connect, saved-profile reconnect and status/signal reads
      carry the chosen interface instead of letting `netsh` select another
      radio (`wifi_windows.go`, `wifi_windows_test.go`).
- [x] ✅ Wi-Fi without `wpa_cli` — the picker drives `wpa_supplicant`'s own
      control socket when the image ships no client, which is the state a
      TrimUI Brick reported (the start menu deletes the Wi-Fi row when the
      backend is unavailable, so the feature was invisible on a device that was
      online). `/etc/wifi/sockets`, where TG5040 images put `ctrl_interface`, is
      part of discovery (`wifi_ctrl.go`, `wifi_ctrl_test.go`).
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
**Status**: 🟡 Partially complete (14 shipped, 2 partial, 2 not started)

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

#### 5.6 Adopted features

Ideas that were accepted into this plan before they had code. A checked item has
shipped; an unchecked one is a decision, not a schedule.

- [x] ✅ **Playtime Wrapped / insights** — the Playtime screen's right-hand card
      is a Monday-aligned 13-week heatmap over `playtimeData.Days` (13 whole
      weeks is the largest grid the 90-day pruner fills), four accent levels for
      how long a day ran, unfilled slots for the rest of the current week, and
      the current streak, longest streak, best day, daily average over active
      days and active-day count beside it; the top-systems half was already the
      neighbouring MOST USED card, fed from `Scenes`. No new tracking: all of it
      is derived per frame from the same snapshot (`wrapped.go`), and days after
      today are marked future so a caller never consults the clock again. The
      companion PNG needed no new export — `/api/screen` already serves the
      rendered frame, so the card is what the phone sees. Checked on pixels, not
      just geometry (`wrapped_render_test.go`: a played window lights the grid,
      an empty one does not, the grid stays inside its card and nothing lands in
      the gap below it), with the date arithmetic and streak rules in
      `wrapped_test.go`.
- [ ] ⬜ **Checkpoints / quick switch** — the largest item ever put on the
      backlog, designed in [CHECKPOINTS.md](../docs/CHECKPOINTS.md) and
      explicitly unimplemented. Named per-game save slots need core-specific
      state handling, and a real switcher needs two suspended emulator processes
      on a 1 GB device.

### Phase 6: Testing
**Status**: ✅ Done (13 shipped, 0 partial) — bounds, cache, retry,
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
      (theme switch), a config-driven launcher page and one D-pad step.
      Measured on the desktop proxy (i7-1165G7, SDL dummy driver, software
      renderer):

      ```
      BenchmarkHomeFrame720p          46–53 µs/frame  3.5 KB   150 allocs
      BenchmarkHomeFrame720pColdText  ~10–18 ms/frame 78 KB    331 allocs
      BenchmarkMenuPageFrame720p      ~84 µs/frame     4.1 KB   183 allocs
      BenchmarkHomeNavigation720p     ~120–190 ns/step 0 B      0 allocs
      ```

      The cold-text frame is the deliberate worst case: it clears every cache
      per iteration, so it must not move, and it is what the splash covers.

      The frame budget used to be dominated by text measurement, not by
      drawing. Profiling a warm home frame put ~40% of it inside `ellipsize`
      and another ~20% in `sanitizeForFont`, because every card, chip, footer
      hint and status-bar label re-measured the same unchanged string on every
      frame — each measurement being a `TTF_SizeUTF8` call through cgo into
      FreeType. Three bounded caches in `textcache.go` now sit on those paths:
      the fitted label (`fitTextCached`, 512 entries), the plain width
      (`measureText`, 1024 entries) and the character substitution
      (`sanitizeCached`, 512 entries). They are cleared by `ClearTextCache`,
      which every font or theme change already calls, and each is pinned to its
      uncached function by `textcache_fit_test.go`. The home frame went from
      ~342 µs to ~70–100 µs and a launcher page from ~300 µs to ~87 µs on the
      same machine; the handheld gains more, because a cgo FreeType call costs
      several times what it does on the desktop.

      The remaining frame time is SDL blits (`SDL_RenderCopy` for each cached
      text texture and shape), which is the part a desktop profile cannot
      reduce: it is real pixel work on the device's software renderer.

      Two things that were *not* blits made up most of what was left. The home
      path painted the backdrop image or theme gradient and then a full-screen
      blended overlay, and the layout's own backdrop filled the screen opaquely
      on top of it — and `renderScene` ran the same block before calling the
      home renderer, so a home frame painted its background three times and
      showed the last. Only the config-driven scenes take that backdrop now.
      The other was texture churn rather than bandwidth: the focus lift grows a
      card by a pixel at a time and the shape cache keyed textures by size, so
      every step of the ease created a new card surface and focus ring (485 KB
      each at 720p) inside a cache budgeted at 12 MB for the whole screen. The
      cache now takes a base geometry (`fillRoundedRectAt`,
      `strokeRoundedRectAt`, `DrawHomeCardSurfaceAt`) and blits into the grown
      rect, so one texture per card serves the whole animation. A resting home
      frame draws its 55 cached blits and creates nothing; a full lift of the
      480×258 Continue card went from 2,972 KB of new shape textures to under
      64 KB (`shape_cache_animation_test.go`). Warm frame: 110–124 µs →
      46–53 µs on the same desktop proxy.
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
**Status**: 🟡 Mostly complete (7 shipped, 2 partial, 8 not started) — snapping,
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
      `CONFIG_SCHEMA_VERSION` (1) — the JavaScript twin of the player's
      `ConfigSchemaVersion`, and the web suite asserts the two agree. The 18 web
      builder tests still pass.

### Phase 8: TrimUI handheld polish
**Status**: 🟡 Started (14 shipped, 2 partial, 4 not started) — interaction feedback,
card rendering, the Tools page, visible-window list drawing and a repeating D-pad landed; the rest is queued

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
- [x] ✅ Bound primary long-list drawing to the visible window — the game
      library, Favorites and Save Manager already draw only their visible rows;
      Downloads now does the same even with variable section headers. Its sparse
      header offsets plus binary search select the viewport rows, and the render
      loop records hit targets only for those rows (`downloadsui.go`,
      `downloadsui_test.go`: 1,000-row panel and 10,000-row range coverage).
      This bounds per-frame row painting, not all list preparation: Downloads
      still constructs/filter-counts its snapshot before drawing. Audit other
      screens only when their data can grow substantially.
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
- [ ] 🟡 Scope background work to the resource that needs it — the disk pie skips
      polling off-scene (`hardware_poll_test.go`), and `backgroundwork.go` pauses
      weather, connectivity, audio-output and LED work while a game owns the
      screen or the desktop window is hidden. Remaining opportunity: avoid
      redundant or unnecessary work while the app is foregrounded, while keeping
      always-visible status data fresh and waking promptly on screen entry.
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
- [x] ✅ Bound long-list row drawing to the visible window — the launcher grid
      and the game library, Favorites, Save Manager and Downloads now paint only
      rows on screen. See the Phase 8 entry for the launcher and Downloads
      details; the other three already limited their row loops to the visible
      window.
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

## Audit log — 2026-10-26 (verification pass)

Everything the document cites was re-checked against this tree, mechanically
rather than by eye:

- **Every code reference resolves.** A script extracted all 182 distinct Go
  files and 15 test functions named in this document; every file exists and
  every named test is defined. (The 15 symbols that did not resolve are the
  component-catalogue names — `Card`, `Badge`, `ListRow` … — which map onto
  `UICard`, `UIBadge`, `UIListRow`, and the two items explicitly parked as
  not-built, `Focusable` and `IconButton`.)
- **Every named test runs and passes.** The browser suite
  (`TestBrowserKeyCyclesScrollAndLinkModes`, `TestBrowserControllerWalksLinks`,
  `TestBrowserLinkCursorWalksThePage`, `TestBrowserLinkFocusIsDrawn`,
  `TestBrowserTapOnPageLinkOpensIt`, `TestBrowserLinkPanelRendersSelectableRows`,
  `TestBrowserControllerTogglesNavMode`, `TestBrowserLinkRowStepWalksColumns`,
  `TestBrowserModeChipIsDrawn`), the focus audit
  (`TestEveryGenericSceneIsDpadReachable`,
  `TestEveryGenericSceneIsReachableViaTheRealKeyPath`,
  `TestEveryFocusableTypeIsInTheGraph`, `TestTheReachabilityAuditHasTeeth`,
  `TestFocusGraphEdgesAreBidirectional`), the concurrency and atomic-write
  tests, and the document-link test all execute (no skips) and pass.
- **The bookkeeping was wrong and is fixed.** Seven phase status lines
  disagreed with their own item lists (phases 2, 3, 4, 5, 6, 7, 8) and two rows
  of the at-a-glance table were stale (phase 2, phase 9). Phases 4 and 6 are in
  fact complete — 25/0/0 and 13/0/0 — so they read ✅ now. One item was also
  tracked twice: `3.3` still listed the icon atlas as ⬜ not started while `3.2`
  and Phase 8b both record it shipped; the duplicate is gone. Every number in
  the table and in the status lines now equals what the items below say.
- **A render test that had never run, and the defect it was hiding.**
  `TestLibraryArtFallsBackToTheCover` skipped itself on its second frame:
  `renderLibraryArt` deferred its renderer's destruction to the end of the
  test, so the next frame's `SDL_CreateRenderer` failed with "Surface already
  associated with window" and the whole probe skipped — three assertions that
  had never executed. Destroying the renderer when the helper returns fixed
  the skip, which immediately exposed a second defect behind it: the cover-art
  cache (`gameLib.artCache`) is renderer-owned but `destroyTestRenderer` did
  not drop it, so the next renderer was handed a dead texture, `Query` failed
  and the art box silently fell through to its initials placeholder instead of
  the cover. Both are fixed; the test now runs all three cases (another game's
  frame, no frame, a truncated frame) and passes.
- **Checks after the changes:** `gofmt -l` clean, `go vet ./...` exit 0,
  `go test -count=1 ./...` green across every package, `go test -race ./...`
  exit 0.
- **Not verifiable here:** the web builder's 18 JavaScript tests and the
  Playwright e2e specs, because `node` is not installed on this machine. Their
  claims (including the exported `schemaVersion` matching the player's
  `ConfigSchemaVersion`) are guarded by tests in `tests/web/builder.test.mjs`,
  but this pass did not execute them.

## Next Steps

This detailed list supports the concise, canonical summary in
[PROJECT_STATUS.md](../docs/PROJECT_STATUS.md). Priorities below favour concrete
user/data protection and responsiveness, with a
clear verification path. Hardware-only items remain explicitly separate from
work that can be completed and tested on a desktop:

1. **Preflight save snapshot integrity before restore** (Phase 4). Snapshot
   manifests already record per-file size and SHA-256, but restore currently
   does not verify those claims and may write earlier files before a later
   damaged entry fails. Validate every source path and digest before touching
   live saves; test corrupt, missing and path-escape cases leave live data
   unchanged. This protects user saves and does not depend on the emulator.
2. **Finish the handheld touch-target and safe-area audit** (Phase 8). The Exit
   chip and browser chrome already use `growTouchTarget`; extend the shared
   behavior to remaining interactive controls and keep drawn and hit geometry
   consistent. Verify on-device around rounded screen corners.
3. **Complete cancellation for feature network work** (Phase 3.1). `context.Context`
   already reaches Patch, downloads, text browser and scene tasks, but older
   feature fetches still use bare clients. Thread cancellation through the
   remaining calls so leaving a screen stops stale work instead of only hiding
   its result.
4. **Reduce redundant foreground work and release scene-owned resources**
   (Phases 3.4 / 8). Background work pauses behind games and minimized windows,
   but screen-specific refreshes and renderer-owned scene resources still merit a
   lifecycle audit; preserve always-visible status freshness and cache budgets.
5. **Add the already-identified media playback queue** (Phase 5.1). A small,
   explicit Up Next list would make sequential listening useful without adding a
   service dependency; keep queue persistence and video behavior separate unless
   user needs call for them.
6. **Run outstanding handheld measurements** (Phase 8). Capture cold-start,
   transition frame time, idle RSS and D-pad repeat feel on the actual 1 GB ARM64
   device; desktop proxies cannot settle those targets.
7. **Continue the large-list audit selectively.** Game library, Favorites, Save
   Manager and Downloads row drawing is now window-bounded. Profile other screens
   first; optimize list preparation only where real data demonstrates material
   cost.

## Audit log — 2026-10-29 (roadmap refresh)

- Rechecked proposed work against the implementation. Downloads row painting is
  viewport-bounded, but `downloadRowsFor` still snapshots, groups and counts the
  complete queue each panel frame; retained this as a profile-first opportunity,
  not a measured performance claim or unconditional rewrite task.
- Adopted a Phase 4 data-integrity item: `saveSnapshotFile` records size and
  SHA-256, while `restoreSaveSnapshot` copies entries without verifying those
  values or preflighting the whole manifest. The priority is to verify all source
  files before any live write and cover fail-closed behavior with tests.
- Recorded two completed safeguards in their implementation phases: the header
  focus-ring render audit (`TestEveryFocusableHeaderControlDrawsFocus`) and
  adapter-pinned Windows Wi-Fi scan/join operations (`wifi_windows_test.go`).
- Refreshed next-step ranking around save protection, handheld input, cancellation,
  lifecycle/performance and on-device verification. Kept speculative ideas (for
  example, larger sync services or a multi-emulator switcher) out of the plan.
- `player/build.bat` completed a Windows build and launched `JukaHub.exe` during
  this pass; the focus-ring render checks and Windows scan/join tests also passed.
  The broad Go test/vet results recorded on 2026-10-26 remain historical and were
  not rerun during this roadmap update.

## Audit log — 2026-10-26 (memory pass)

Framerate was the previous pass; this one asked the same question about memory
and answered it with numbers rather than by eye. The measurement is kept, not
throwaway: `TestSceneNavigationMemory` paints every scene of the shipped
handheld config four times on one renderer and samples `collectTextureMemoryStats`,
`ShapeCacheStats`, `IconAtlasStats`, the label/fit/measure/substitution caches,
the live Go heap and the process working set after each pass.

- **Measured baseline.** After all 36 scenes: Go heap 7 MB, home-label cache
  0.7 MB, shape cache at its 48 MB *desktop* budget, rendered-text cache 309
  entries / 7.4 MB (≈24 KB per label on average). So the Go side was never the
  problem; the SDL textures are, and they live outside the Go heap.
- **Repeated navigation does not leak, and the numbers now say so.** Passes 2–4
  are byte-identical to pass 1 in every cache, down to the byte (textures
  63,974 KB, of which shapes 47,968 KB at the desktop budget, text 15,323 KB /
  558 entries and the library's box art 97,500 KB / 52 covers — that walk ran on
  the desktop 96 MB budget, before the harness pinned the handheld ceiling; see
  the box-art bullet below for the figures it reports now), the fit/measure
  caches stay at 326/71 entries, and the working set has no trend. The scene
  pass alone accounts for 56,491 KB of that; the library phase adds the rest and
  is described below. The first pass is the only one that grows,
  which is what warm-up looks like; the test asserts zero growth from the
  second pass on, with slack only for collector noise (4 MB heap) and page
  cache (16 MB RSS).
- **Surfaces are measured where they are visible.** `runtime.MemStats` cannot
  see cgo allocations, so a leaked SDL surface would be invisible to a heap
  check; that is why the harness also samples the process's resident set
  (`VmRSS` on Linux, `GetProcessMemoryInfo` on Windows, test-only helpers) and
  fails on growth there. `TestSceneNavigationSamplerSeesGrowth` is the
  instrument checking itself: it puts known bytes into the text cache and
  requires the sampler to report them, so the guard cannot pass vacuously.
- **What this harness does not cover.** Thumbnails and decoded video frames are
  not allocated by scene painting on an empty card (they read 0 KB in the
  table), so their bounds are guarded by their own unit tests instead. Still no
  on-device RSS reading. The library's box art was on this list and is not any
  more — see the box-art bullet below.

Scene navigation being clean leaves the caches that grow with *use* rather than
with a paint — the ones a long session fills. Those were audited by reading
their lifetimes, and three were not bounded at all:

- **The unbounded one was the game library's box art.**
  `gameLib.artCache` was a plain map filled one cover per focused row and never
  emptied until the panel closed. A 200×270 cover is ~216 KB, so a 500-game
  collection reached ~100 MB of card RAM — on the device that is shared with
  ffmpeg and the frame pools. It is now byte-bounded (32 MB handheld / 96 MB
  desktop) and count-bounded (256, which also bounds cached *misses*), oldest
  first, with the cover currently drawn always the newest entry.
  (`TestLibraryArtCacheStaysWithinItsBudget`,
  `TestLibraryArtCacheBoundsMisses`.)
  The scene harness now drives it too, because a bound proven by calling
  `rememberArt` directly says nothing about a real walk: each pass opens the
  library on a synthetic card with a cover file for every game — sized from the
  art budget so the whole set is larger than the cache can hold — and presses
  Down through the list with a frame per press.
  The harness pins the **handheld** ceiling (`libraryArtBudgetForce`), so the
  budget that ships is the one measured on every host, including CI's `-race`
  job, instead of whichever branch the machine happens to take: before this the
  same test measured 96 MB on a Windows desktop and 32 MB on the Linux runner,
  and the 32 MB path went unverified on the machine the work was done on. On the
  desktop budget the last walk settled at 52 covers / 97,500 KB with 62 walked;
  with the handheld ceiling pinned it settles at 17 covers / 31,875 KB with 26
  walked, so the walk still crosses the budget and eviction is what keeps it
  there, and the figure is identical on every later pass. The guard fails if a
  pass loads no cover, if the walk never reaches the budget (so it cannot pass
  on a library too small to stress it), or if the cache ends a pass above its
  byte or entry bound. The desktop ceiling and the derivation are pinned separately by
  `TestLibraryArtBudgetBranches`, so forcing one branch here does not leave the
  other untested. Some of the working-set figure is that fixture being
  resident, not a regression: the covers are real textures the panel would hold
  on a card that size.
  The same pin now covers the other three columns. Rendered text, home labels
  and shapes each branch on the device too — 24, 8 and 12 MB handheld against
  64, 48 and 48 MB desktop — and the harness was leaving those to the host, so a
  desktop run printed 64/48/48 MB under a heading that read like the card's
  numbers. `forceHandheldCeilings` moves the live singletons onto the handheld
  branch (`TextCache.SetBudget` for rendered text, `homeTextCacheApplyBudget` for
  labels, and `shapeCacheBudgetBytes()` read at eviction time for shapes), fails
  if any of them did not take, and restores the derived branch afterwards.
  `TestSceneBudgetBranches` pins both sides plus the derivation, so the pin
  cannot go stale in one direction. With all four pinned the walk reads text
  10.5 MB / 417 entries (24 MB cap), labels 663 KB (8 MB), shapes 9.7 MB (12 MB)
  and box art 31.9 MB / 17 covers (32 MB); the shape column used to sit at
  47 MB against the desktop 48 MB, which is the ceiling a card never has.
  Shapes and text are still flat across passes at the tighter budgets, so
  eviction is doing the work the desktop numbers were hiding.
- **The rendered-text cache had no byte budget.** Every other texture cache
  here (shapes, home labels, thumbnails, chat images, app icons) was already
  bounded by bytes; this one was bounded only by 512 entries, and it is exactly
  where a wrapped web paragraph, a PDF line or a chat bubble — each easily a
  megabyte — lands. Now 24 MB handheld / 64 MB desktop, LRU, never evicting the
  label being drawn, and the `JUKAHUB_TEXSTATS=1` accounting line prints usage
  against the cap so a bug report can tell "at budget" from "leaking".
  (`TestTextCacheEvictsByByteBudget`, `TestTextCacheKeepsOneOversizedLabel`,
  `TestAppTextCacheHasABudget`.)
- **Two caches let their misses out of the bound.** A failed icon decode was
  written straight into the map, bypassing the eviction order the successful
  ones go through, so a launcher full of broken icons grew the map without
  limit; the library's art misses had the same shape until the bound above was
  added. (`TestAppIconCacheBoundsFailedDecodes`.)
- **In memory, the budgets now add up.** Handheld ceilings: shapes 12 MB, text
  24 MB, home labels 8 MB, thumbnails and web images 64 MB, library art 32 MB,
  chat images 16 × 256 KB, plus the fixed allocations (video frame and UI layer
  ~3.7 MB each, icon atlas 256 KB).
- **Checks after the changes:** `go vet ./` exit 0; the text, icon, library,
  snap-preview and artwork suites pass; `gofmt` clean.

## Audit log — 2026-10-26 (skip audit)

A skip reports green while asserting nothing, so every skip that is not a real
environment limitation is a hole in the regression net. The suite had 390 skip
sites across 132 test files and only 11 of them fire on this machine; the other
379 were latent, and a defect could move a test from "passes" to "skips"
without anything turning red. (That is not hypothetical: the previous pass
found `TestLibraryArtFallsBackToTheCover` skipping itself on its second frame
because its renderer could not be created, so three assertions had never run.)

The rule now applied: a test may skip only when the *machine* cannot run it.

- **What is still allowed** — SDL video/ttf/image unavailable; `-short`
  (a developer opt-out, never used by CI); symlink privilege, POSIX `sh`,
  executable-bit and `.bat` differences on Windows; `JUKAHUB_ANDROIDRT`, the
  TrimUI-native client, a mounted handheld card, a device that manages its own
  startup apps; no listener, no LAN address, no resolver, a port already bound;
  no `ffmpeg`/`tar` to shell out to; the `JUKA_PREVIEW` / `JUKAHUB_TEST_UPDATE`
  opt-ins; secrets that are not shipped; and the helper-process re-exec pattern.
- **290 sites converted to failures**, because the only way they could fire is a
  defect or a broken fixture: 124 `sdl.CreateRenderer` skips (SDL video is
  already up, and the dummy driver supports the software renderer), 88
  `ReadPixels` skips (a rendering test that cannot read its frame has no
  assertions left), 47 font skips (`player/Inter-Regular.ttf` is committed), 4
  probe-texture skips, 15 missing-config/scene skips, 5 cross-test leaks
  (another UI surface open, a child still registered, a video decoder that did
  not exit, playback not idle) and the assert-shaped ones: "the header QR was
  not drawn" → skip, "launcher was not drawn" → skip, plus tool-grid/unit-
  converter/app-grid fixtures that skipped when their own premise failed.
- **The one genuinely inert guard** was `TestToolsRowsOfflineHint`: it skipped
  whenever the host had network access, which is always on CI, so the offline
  hint was never checked there. It now forces the monitor offline (restoring it
  afterwards) and asserts the row.
- **Fixtures fixed rather than silenced**: the app-grid virtualization and
  clip tests now size their entry list from the measured layout (they used a
  fixed 300/40 entries that could fit the probe screen and skip), and the
  virtual-keyboard slide test no longer treats SDL's `0` tick as "no
  animation" — it starts the clock at 1 and fails if the offset is zero.
- **The rule is enforced, not documented.** `TestSkipsAreEnvironmentGated`
  scans every `t.Skip`/`b.Skip` site, requires a literal reason, and requires
  it to match the capability list in `player/skipaudit_test.go`; a new skip with
  any other reason fails with file, line and reason. `TestSkipAuditHasTeeth`
  pins the matcher both ways, and the audit was verified end to end against a
  deliberately unlisted skip (it failed, naming the line). 88 sites remain, 36 of
  them the `-short` opt-out.
- **Checks after the changes:** `go vet ./` exit 0, `gofmt -l` clean,
  `go test -count=1 ./` green on two consecutive runs (41 s), and `-race` green
  on the tests this pass changed (audit, offline hint, memory harness, the
  converted render probes). `CONTRIBUTING.md` states the rule next to the render
  test guidance.

## Audit log — 2026-10-26 (store durability pass)

The memory pass bounded the caches that grow with a paint; this one asked the
same question about the files the player writes on the user's behalf. Every JSON
store was read for two defects: a write that is not crash-safe, and a load that
turns damage into an empty store without telling anyone.

**The rule is one pair of helpers**, both in `config_health.go`: `AtomicWrite`
(temp file in the same directory, fsync, rename, previous content kept as
`.bak`) and `readJSONWithBackup` (live file, then `.bak`, logs the recovery, and
now also logs the case where neither parses — a store must not disappear
silently even when there is nothing left to recover). A missing file stays a
first run: the backup is never consulted for an absent store.

**What the audit found.**

- One store was truncated on every save: `favorites.json` used a bare
  `os.WriteFile`, and its loader unmarshalled into the live store, so a half
  document left half a list that the next save wrote over the good file.
- Sixteen others had the "atomic but unbacked" shape — temp file plus rename,
  with no fsync and no `.bak`, paired with a loader that returned empty on any
  parse error: per-game options, recents, music favourites, audio resume
  positions, TV favourites, the IPTV library, browser cookies, bookmarks and the
  per-site ad-block list, party friends and history, PS Remote hosts (pairing
  keys), startup state, the save-sync folder manifest, curation profiles and the
  Toolbox snippets. Fourteen had both halves; two — the IPTV library and the
  Toolbox snippets — already wrote atomically and only needed the read side.
  Several logged nothing at all on the way to an empty list.
- `secret` wrote `jukauser.json` through its own tmp+rename, bypassing the
  durability the player uses for the same file.

**What was left alone, and why.** Caches are regenerable, so a reset costs a
refetch: the weather cache, the RetroAchievements progress cache, the
artwork-miss store, the patch `db.json` and the emulator-update `source.json`.
`game-launchers.json` is written by the user or a patch and never by the player,
so no `.bak` could ever exist; its loader logs and falls back to the built-in
catalog. The game-profile catalogs and community imports report a damaged file
as an issue in the panel instead of resetting, and `port.json`/`plugin.json`/app
`config.json` belong to other software.

**Deleted means deleted.** A cleared bookmark list, a cookie Forget (whole jar,
one site, or the retention prune) and a deleted curation profile now remove
`<file>.bak` together with the live file. Otherwise the "forgotten"
session or list would still sit on the card, and the next damaged read would
restore it — the one privacy regression a backup can introduce.

**Checks:** `gofmt -l` clean over every touched file, `go vet ./` exit 0,
`go test -count=1 ./` green on the whole package, and `-race` green over the
converted stores plus the cookie suite. Each converted store has a test that
saves twice through its real code path, requires the `.bak` to hold the previous
version, corrupts the live file and requires the data back, and requires an
absent file to read as empty. `TestBrowserForgetTakesTheBackupWithIt` pins the
deletion, and the store inventory lives in `docs/CONFIGURATION.md` so the next
new store has a place to be listed.

## Audit log — 2026-10-26 (emulator install pass)

**What was wrong.** The Emulators page was a report, not a tool. It listed
every `Emus/` folder, every installed core and every `Roms/` system with no
emulator, and the last group was the one that mattered: a system with games and
no emulator said "no emulator found" when opened, and the only way to fix it
was to copy a folder onto the card from a computer. The update checker could
already fetch a fresh aarch64 core into the pool, but nothing on the Emulators
page could install one, and an installed core could only be replaced by walking
into the compatibility panel and activating a pool build by hand.

**What changed.** `emulatorinstall.go` owns the missing half: a tile resolves
to the core that provides its system (`Core` on a core row, otherwise
`defaultCoreForSystem` on the system name), the index's CRC32 is compared with
the bytes of the binary on the card, and the result is Install (no binary),
Update (different bytes) or Up to date. An install reuses the update path's
download and `activateCoreBuild`, so the build keeps its pool copy and its
`source.json` provenance, and a replaced binary keeps a timestamped `.bak`
beside it. On the page, **A** on a missing or unusable tile starts an install,
**X** starts one for any tile with a core, and **L1** plans every installed core
once and applies the stale ones. `deviceEmulatorEntries` no longer reports a
`Roms/` system as unplayable when an installed core covers it — installing
therefore changes the page instead of leaving the same row behind. The footer
hints are computed per tile (A Install / Launch / Open, X Update only where a
core exists, L1 Update all only where one is installed) and the hint line is
ellipsized to the page body, because the Brick's canvas is narrower. The
canonical system map gained the two TrimUI folder names it was missing, `PS`
and `SFC`, so those systems resolve like every other one.

**What is deliberately not offered.** No `Emus/<system>/` folder is written and
no launcher script is generated: the upstream standalone builds are desktop
Linux artifacts that cannot run on this firmware, and the stock launcher
scripts are the firmware's. On Windows the core actions are refused, because
the buildbot serves aarch64 shared objects and a desktop RetroArch loads
`.dll` files; the platform rule is a variable (`emuCoreInstallPlatform`) so the
panel's handheld path is still tested on a desktop run.

**Checks:** `go vet ./` exit 0, `go test ./` green on the whole package
(46.8 s), `gofmt -l` clean over every touched file. New tests pin the system
resolution table, plan-vs-inventory decisions (install / update / current /
not published), a first install writing the core and its provenance, an update
keeping the replaced binary's bytes in the `.bak`, a checksum mismatch leaving
the active core and the folder untouched, a current build never being
downloaded again, the `Roms/` folder going quiet once a core covers it, and the
footer hints matching what each press does.

## Audit log — 2026-10-26 (cache budget pass)

**What the audit found.** The player bounded most caches by entry count, and a
count reads like a memory ceiling while being nothing of the kind. Five were
bounded by count alone: the launcher's icon textures (64 icons of anything up
to 256×256×4, so 16 MB by accident), the chat image textures (16 decoded photos,
each up to 48 MB of RGBA), the chat avatars (48 fixed 64×64 textures), the
downloaded thumbnail payloads (512 entries of whatever the URL served), and the
validated bodies in the conditional-request cache (64 entries × the 1 MiB body
limit). The other caches were already byte-bounded: `TextCache`, `homeTextCache`,
`shapeTextures`, `thumbnailTextures` and the library's box-art cache. The fixed
ones — icon atlas, ambient glow, the video overlay and UI layers — are single
textures, and the cap-1 caches (text-browser wrap, lyrics) cannot grow.

**What changed.** `cachebudget.go` adds `byteOrderedStore`: an
insertion-ordered map bounded by entries and by bytes, with a release callback
so an evicted or replaced texture is destroyed exactly once, and one eviction
rule every cache now shares. The newest entry is never dropped — the caller
that just stored it is drawing with it — which is the rule the thumbnail cache
already followed. All five count-only caches were converted to it: app icons
(8/32 MB handheld/desktop), chat images (24/64 MB), avatars (4 MB), thumbnail
payloads (16/48 MB) and feed bodies (8/16 MB). The thumbnail payload cache also
lost its random eviction: it now drops the oldest insertion, and accounts the
bytes, so a batch of large images cannot sit under the 512-entry cap.

**Where it shows up.** `textureMemoryStats` gained `AppIcons`, `ChatImages` and
`ChatAvatars`, so the `JUKAHUB_TEXSTATS` line reports them (in KB, because the
avatar cache would otherwise read as 0 MB). `EvictCachesForPressure` and the
shutdown path both clear all five, so a critical sweep is not followed by three
caches refilling to their ceilings. `docs/DEVELOPMENT.md` now carries the whole
inventory as a table with the budget for each cache, and the rule that a new
cache needs both bounds.

**What was left alone.** The count-bounded string caches (text measurement,
sanitize, fit, app label, resolved-icon path) hold short strings with a hard
length limit; their combined worst case is a few hundred kilobytes, so they are
listed in the inventory rather than instrumented. The chat download staging map
(`chatImageDownloads`) is a hand-off to the render thread, not a cache: entries
are deleted as soon as the texture is uploaded, on the frame that draws chat.

**Checks:** `go vet ./` exit 0, `go test ./` green on the whole package
(49.3 s), `-race` green over the new budget tests plus the thumbnail flood
test, `gofmt -l` clean over every touched file. New tests pin the store's two
bounds, that the newest entry always survives, that a replaced or evicted value
is released once, each cache's budget wiring, the launcher's byte accounting
through a real decode, a 24 MB chat store keeping two of three 10 MB images, the
thumbnail payload cache evicting through its own API, the feed cache evicting by
bytes, and the pressure sweep clearing all five.

## Audit log — 2026-10-27 (automatic core updates)

**What was wrong.** Keeping the emulator cores current needed a person: open
Emulators, find the tile, press Update, or press L1 for every installed core.
The update pass existed (`startEmulatorUpdateInstalled`) but nothing ran it, so
a card could sit on a nightly from months ago — the games run worse, and there
is nothing on screen that says why. Two further problems came with making it
automatic: every install kept the binary it replaced forever (one `.bak` per
core per update, unbounded on a 1 GB card), and a background download had no
visible progress of its own.

**What changed.** `runInstalledCoreUpdates` is now the one implementation of
"bring the installed cores forward": read the index, plan the cores whose
CRC32 differs from the build it lists (`planCoreUpdates`), download into the
pool, activate
(the replaced binary kept as a `.bak`), report through the install bar. The
page's L1 action calls it with a toast summary; `startCoreAutoUpdate` calls it
on the handheld once per `coreAutoUpdateInterval` (24 h), after the boot has
settled and behind the release check's window, and only when
`emuCoreInstallSupported`, `IsTrimuiDevice` and `IsOnline` all hold. It takes
the one patch slot, so it can never overlap a manual install, and it is disabled
by `JUKAHUB_NO_CORE_AUTOUPDATE`. A pass that could not read the index returns
`Checked: false` and does not stamp `patchState.CoreUpdateCheckedAt`, so an
offline boot retries next launch instead of standing down for a day. The stamp
uses the same sane-clock rule as the release check, because the handheld has no
RTC battery.

**Backups.** `installCoreFile` now prunes after a successful replacement: the
newest `maxCoreBackups` (5) `.jukahub-<stamp>.bak` files beside that core stay,
the rest are deleted. It is the single path every core write goes through —
tile install, compatibility activation, rollback, automatic update — so no path
can drift from the limit, and the pruning reuses `coreBackupsIn`, the same
parser the rollback listing uses, so a `.bak` from another tool is never a
candidate.

**The pool.** A `.bak` is only half of what an update leaves behind: every
fetch also writes `data/performance/cores/<core>/<date>/`, and a card that
updates itself would gain one dated folder per core per day. `fetchCoreUpdate`
now calls `pruneCorePool` after writing, keeping the newest `maxCorePoolBuilds`
(5) for that core and never deleting the build just fetched, even when the index
publishes it under an older date than what the pool already holds. The pool is
still what the compatibility panel lists, so an older nightly stays activatable
for a few days after it stops being the newest.

**The bar.** The install card gained an optional title (`installBarBeginLabelled`),
so an update run reads "Updating emulator cores" instead of "Installing
Emulator cores". It is drawn by the main loop after the toast, so a download
started by the automatic pass is visible on whatever screen the user is on; its
phase and detail come from `installBarProgressHook`.

**Checks:** `go vet ./` exit 0, `go test ./` green on the whole package,
`gofmt -l` clean over every touched file. New tests pin the due rule (fresh,
stale, garbled, future and pre-2020 stamps), the device policy (desktop,
offline, environment opt-out), an automatic update of an installed core with
the replaced build kept and the bar driven, an up-to-date card left untouched,
an unreachable index reporting `Checked: false` without touching the active
core, seven updates leaving exactly five backups and five pool builds, the two
pruning rules (sparing another core's build, a foreign `.bak`, and the build
just fetched), and the bar's update title. The backup test fails without the
prune (`kept 7 backups`), which is the guard this pass needed.

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
