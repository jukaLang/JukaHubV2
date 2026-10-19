# JukaHubV2 Modernization — Verified Checklist

Status of every item in [MODERNIZATION.md](MODERNIZATION.md), checked against the
code in this tree on **2026-10-18** (go1.25.0 windows/amd64, SDL2 from
`player/.sdl2`).

Legend: **✅ shipped** (code exists and is exercised), **🟡 partial** (some of the
item works, the rest is genuinely missing), **⬜ not started**.

Library choices and measured numbers live in
[LIBRARY-EVALUATION.md](LIBRARY-EVALUATION.md).

Verification for this revision:

```
go vet ./...      # exit 0
go test ./...     # ok jukahub/JukaHub, ok jukahub/JukaHub/web
go build .        # exit 0
```

## At a glance

| Phase | Done | Partial | Not started |
|-------|------|---------|-------------|
| 2. Architecture | 26 | 3 | 2 |
| 3. Async & performance | 10 | 3 | 8 |
| 4. Reliability | 22 | 2 | 2 |
| 5. Features | 10 | 3 | 3 |
| 6. Testing | 8 | 2 | 1 |
| 7. Web builder | 5 | 2 | 5 |

---

## Phase 2 — Architecture

### 2.1 Design system

- [x] ✅ Semantic color tokens — `design.go` (surfaces, borders, text accessors, semantic, progress, chips).
- [x] ✅ Spacing tokens — 4pt scale `SpaceXS … Space3XL` in `design.go`.
- [x] ✅ Radius tokens — `RadiusXS … RadiusRound`.
- [x] ✅ Typography tokens — `FontSizeXSS … FontSize3XL`, semantic `TypeRole`
      (`TypeTitle/Heading/Body/Label/Caption`) and `FontForRole`; covered by
      `design_tokens_test.go`.
- [x] ✅ Motion tokens — `AnimDuration*` in `design.go`, `anim.go` durations, all
      gated by reduced motion (`anim_test.go`, `TestUIMotionGate`).
- [x] ✅ Density tokens — `DensityCompact/DensityComfortable`, `DensityFor`,
      `DensitySpace`, airy spacing aliases; covered by `design_tokens_test.go`.

### 2.2 Component system

- [x] ✅ `Card` — `UICard`/`UICardStyle`, adopted by notifications, Start menu and
      Quick Settings; render tests in `components_test.go`.
- [x] ✅ `StatusBar` — `renderStatusBar`; shared right-hand cluster order with the
      home header, overflow dropping, safe-mode chip; `statusbar_render_test.go`.
- [x] ✅ `FooterHints` — `renderFooter` + `FooterHint`, per-scene overrides
      (`patchFooterHints`).
- [x] ✅ `Button` (surface-level) — shared pressed/focus recipes in `ui_helpers.go`
      and per-scene button geometry; no single `UIButton` type yet 🟡.
- [ ] 🟡 `IconButton` — icon geometry helpers exist (`launcher.go`), but buttons are
      still drawn inline per scene.
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

### 2.3 Focus system

- [x] ✅ Focus graph per scene — `focus.go` (`FocusEngine`, graph nodes, chrome tiers).
- [x] ✅ Focus preservation on scene return — `Persist`/`Restore`, wired in
      `core.go` scene changes.
- [x] ✅ Deterministic directional navigation — `focus.go` geometry-based movement;
      `navselection_test.go`, `chrome_nav_test.go`, `homefocus_nav_test.go`.
- [x] ✅ Focus animation — hover/focus lift, reduced-motion aware.
- [x] ✅ Reduced-motion mode — config flag, animation gates, battery-saver test.
- [ ] 🟡 `Focusable` interface — behavior is centralized in `FocusEngine`, but there
      is no Go interface implemented by element types.
- [ ] ⬜ Input remapping screen.
- [ ] ⬜ Controller test screen.

### 2.4 Platform abstraction

- [x] ✅ `Platform` interface — `platform.go` with `P()`, executable dir, device
      detection, process flags, tool paths.
- [x] ✅ TrimUI/Linux and Windows implementations — same file, platform-specific
      files (`patch_statfs_*`, `processes_*`).
- [x] ✅ OS-specific paths/process flags/power behavior/tool discovery moved behind
      `P()` (`platform.go`, `paths.go`).
- [x] ✅ Portable layout — data, apps, packages and shared folder resolve next to
      the executable (`patch_repo.go`, `apps.go`, `packages.go`).

---

## Phase 3 — Async work & performance

### 3.1 Concurrency

- [ ] 🟡 `context.Context` — threaded through Patch, downloads, text browser and
      installers; older feature fetches still use bare clients.
- [ ] ⬜ `errgroup` coordinated cancellation.
- [x] ✅ Bounded thumbnail prefetch pool — 2 workers on handhelds, 3 on desktop,
      with in-flight dedupe (`thumbstore.go`); verified by
      `TestThumbnailPrefetchRunsConcurrently`. No general-purpose semaphore yet.
- [ ] ⬜ Image-decode worker pool (2 jobs on Trimui).
- [ ] ⬜ Network worker pool (2–3 jobs on Trimui).
- [ ] ⬜ Debounced search input (config saves and hot reload are debounced; search
      is not).
- [ ] ⬜ Cancel scene work on scene exit.

### 3.2 Caching

- [x] ✅ Font/text texture cache — `textcache.go`, LRU with size cap; eviction,
      trim, clear, capacity clamp and key tests in `textcache_test.go`.
- [x] ✅ Thumbnail cache with entry limit — `thumbstore.go` memory + disk cache.
- [x] ✅ Network response cache with TTL — bbolt `cache.go`.
- [x] ✅ LRU eviction — text cache and thumbnail memory cache.
- [ ] ⬜ Icon texture atlas — icons are drawn as glyphs/primitives, no atlas.

### 3.3 Rendering

- [x] ✅ Cached text textures — `globalTextCache`, pressure trimming.
- [x] ✅ Clipping for lists/grids — `renderWithClip`.
- [x] ✅ Idle render mode — `FrameRateController` drops cadence when nothing animates.
- [x] ✅ Frame time budget — adaptive delay + low-power cadence (`adaptive.go`,
      `adaptive_test.go`).
- [ ] 🟡 Object reuse for cards/labels/images — card surfaces are stateless helpers;
      per-frame text layout still allocates.
- [ ] ⬜ Icon atlas as above.

### 3.4 Power management

- [ ] 🟡 Named power profiles — `LowPower` + `ReducedMotion` + adaptive FPS exist;
      there is no Battery Saver / Balanced / Performance selector.
- [x] ✅ Reduce animation in battery saver — motion gates (`anim_test.go`).
- [ ] ⬜ Stop polling when a scene is hidden (weather ticker and monitors run for
      the whole process lifetime).

---

## Phase 4 — Reliability & configuration

### 4.1 Configuration

- [x] ✅ Schema versioning — `config_schema.go`, `SchemaVersion` on `Config`,
      newer-file refusal (`config_schema_test.go`).
- [x] ✅ Validation — `ConfigValidator` (`config_health.go`), used at startup and by
      hot reload.
- [x] ✅ Migrations — `MigrateConfig`, applied by every loader.
- [x] ✅ Atomic writes — `AtomicWrite` temp+rename with `.bak`; tested.
- [x] ✅ Last-known-good backup — `config_health.go` (`RecordBackupRestore`,
      `.bak` fallback).
- [x] ✅ Config hot reload (desktop dev) — `hotreload.go`, started in `main.go`,
      debounced 500 ms, validated before applying.
- [x] ✅ Portable mode — all state resolves next to the executable.
- [x] ✅ Persistence separation — design saved to `jukaconfig.json`, user state to
      `jukauser.json`; volatile keys filtered (`config_persist_test.go`).

### 4.2 Logging

- [x] ✅ `log/slog` — `logging.go`, JSON on desktop, compact text on Trimui.
- [x] ✅ Structured fields — `LogScene`, `LogOp`, `LogSceneOp` (scene, subsystem,
      platform, goos/arch).
- [x] ✅ JSON logs for Windows / compact logs for Trimui.
- [x] ✅ In-app log viewer — `appLog` ring (2000 lines) captured via `log.SetOutput`,
      shown by the shipped **LogExporter** scene's `textlog` panel. The panel now
      shows the newest lines (tail view) instead of stopping at the oldest ones,
      covered by `logviewer_test.go`.
- [ ] 🟡 `Ctrl+L` shortcut — listed in `shortcuts.go`, but no global handler exists
      (only the terminal uses Ctrl+L to clear). The viewer is reached through the
      Logs scene.
- [ ] ⬜ Log rotation — `logs.txt` is only written on explicit export; no size-based
      rotation.
- [x] ✅ Diagnostic bundle export — `ExportDiagnostics` (redacted report + Patch UI
      action).

### 4.3 Error handling

- [x] ✅ Offline detection — `netmon.go` + status cluster.
- [ ] 🟡 Cached fallback for offline content — thumbnails have disk cache; feeds do not.
- [x] ✅ Guided remediation for missing tools — tool health check + install bar,
      offline-safe retry.
- [x] ✅ Safe mode — `safemode.go`: `--safe-mode`, `JUKAHUB_SAFE_MODE`, or
      `safe-mode.flag` disables plugins, updater, tool install and background
      network work for one launch, with a persistent `SAFE` chip in the status bar.
      Covered by `safemode_test.go` (resolution precedence, gating, plugin skip).
- [x] ✅ Unclean shutdown detection / crash recovery — update journal + rollback
      (`patch_engine.go`, `checkInterruptedJournal`), tested.

### 4.4 Network

- [x] ✅ Shared HTTP clients — `net.go` (`httpClientLong`) plus Patch's bounded client.
- [x] ✅ Connection reuse — keep-alive transport.
- [x] ✅ Request timeouts — per-client and per-context.
- [x] ✅ Max response sizes — `fetchBounded`, `downloadToFile`.
- [x] ✅ Retry with exponential backoff + jitter — `retry.go`, honors `Retry-After`,
      retries 408/425/429/5xx and transport errors, refuses to replay an
      unrewindable body; adopted by the Patch metadata fetch and weather forecast;
      covered by `retry_test.go`.
- [ ] ⬜ Conditional caching (`If-None-Match` / `If-Modified-Since`).

---

## Phase 5 — Features

### 5.1 Media

- [ ] 🟡 Media-backend abstraction — ffplay and the embedded player are separate
      paths with a common UI, but there is no interface.
- [ ] ⬜ Playback queue.
- [x] ✅ Resume position — saved in favorites/recent, throttled while playing.
- [x] ✅ Progress display — Continue card and player overlay.
- [ ] 🟡 Cancellation — playback stop/cancel exists; in-flight downloads are only
      partly context-aware.
- [x] ✅ Clear failure messages — player errors surface as toasts and scene text.

### 5.2 Files

- [x] ✅ File explorer — `features.go`/`paths.go`, sandboxed root, tested path guards.
- [ ] ⬜ Drag-and-drop (Windows).
- [x] ✅ Clipboard integration — clipboard manager (`clipboard.go`).

### 5.3 Packages

- [x] ✅ Package trust UX — signed manifests, checksum verification, Patch scene.
- [x] ✅ Source/version/checksum display.
- [x] ✅ Uninstall rollback — journaled install/remove (`patch_repo.go`).

### 5.4 Settings

- [ ] ⬜ Controller remapping (only the A/B swap toggle exists).
- [x] ✅ Accessibility settings — reduced motion, low power, font sizes, theme
      contrast tokens.
- [ ] 🟡 Power profile selection (single LowPower toggle).
- [x] ✅ Theme preview — scrollable theme gallery with live apply.

---

## Phase 6 — Testing

- [x] ✅ Config validation tests — `config_schema_test.go`, `config_persist_test.go`.
- [x] ✅ Focus graph navigation tests — `navselection_test.go`, `chrome_nav_test.go`,
      `homefocus_nav_test.go`.
- [x] ✅ Input normalization tests — `controller_swap_test.go`, touch/keyboard tests.
- [x] ✅ Layout bounds tests (1280×720 and smaller) — `layout_bounds_test.go`
      (`BoundsViolations` helper on `HomeLayout`, hit-testing for every card).
- [x] ✅ Cache eviction tests — `textcache_test.go` (LRU, trim, clear, zero capacity,
      key identity).
- [x] ✅ URL/path validation tests — `paths_test.go`.
- [x] ✅ Offline/retry behavior tests — `retry_test.go` (500 recovery, 404 no-retry,
      exhaustion, context cancel, unrewindable body).
- [ ] ⬜ Cancellation tests (beyond retry backoff cancellation).
- [x] ✅ External process cleanup tests — embedded playback teardown now waits
      for the ffmpeg decoder goroutine (`waitForVideoDecoders`) and the decode
      test fails if it does not exit; this also fixed a reproducible SDL crash
      when a later render test reused the window.
- [ ] 🟡 Platform path selection tests — path guards exist, platform-specific
      selection is only indirectly covered.
- [ ] 🟡 Benchmarks — text-cache and font cache benchmarks exist
      (`vkeyboard_cache_bench_test.go`); no render/navigation benchmark suite.

---

## Phase 7 — Web builder

- [x] ✅ Semantic design tokens — `style.css` (`--canvas-*`, surfaces, spacing).
- [ ] 🟡 Reusable components — shared CSS classes exist; element markup is still
      assembled per type.
- [ ] ⬜ Snap-to-grid.
- [ ] ⬜ Alignment guides.
- [ ] ⬜ Safe-area overlay (1280×720 device frame).
- [ ] ⬜ Focus-order editor.
- [x] ✅ State previews — preview mode (`script.js`, Ctrl+P).
- [x] ✅ Undo/redo — bounded 60-step stacks.
- [x] ✅ Autosave — localStorage workspace snapshots.
- [x] ✅ Production export — JSON/XML export with round-trip loaders.
- [ ] 🟡 Trimui performance linting — font-size/asset checks partly covered by the
      loader warnings.
- [ ] ⬜ Schema validation in the builder (no `schemaVersion` emitted).

---

## Verified in this pass (2026-10-18)

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
- Full suite: `go vet ./...`, `go test ./...`, `go build .` all pass.

## Recommended next work (highest value first)

1. **General async + cancellation** — the thumbnail pool is bounded now; extend
   that pattern to other background work and thread `context` through
   scene-scoped tasks, the remaining performance risk on 1 GB RAM.
2. **Log rotation** — cap `logs.txt`/`appLog` writes by size and rotate one backup.
3. **Wire `Ctrl+L`** to the existing Logs scene, or remove the shortcut line.
4. **Controller remapping screen** — the only Phase 2/5 feature with no foundation.
5. **Builder snap-to-grid + alignment guides + safe-area overlay** — pure front-end
   work with immediate design-time payoff.
6. **Benchmarks** for text rendering and home navigation at 720p, so future
   rendering changes have a budget to compare against.
