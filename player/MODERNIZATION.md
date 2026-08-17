# JukaHubV2 Modernization Plan

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
- ❌ **Monolithic main.go**: ~5000 lines, no clear separation of concerns
- ❌ **Global state**: All state in package-level variables
- ❌ **No async work isolation**: HTTP, image decoding on render thread
- ❌ **No context cancellation**: Background work not cancellable
- ❌ **No bounded concurrency**: Unbounded goroutines for thumbnails, weather, etc.
- ❌ **No platform abstraction**: OS-specific code mixed throughout
- ❌ **No structured logging**: Uses `log.Printf` everywhere
- ❌ **No config validation**: Assumes valid JSON, no schema versioning
- ❌ **No atomic writes**: Config writes not atomic
- ❌ **No safe mode**: Corrupt config prevents startup

#### 3. UI/UX Issues (Phase 1 - Partially Fixed)
- ✅ Home screen layout: Responsive grid with 4/3/2 columns
- ✅ Focus state: 3px cyan accent outline
- ❌ Focus preservation: No remembered focus per scene
- ❌ Focus graph: No explicit focus order, relies on element index
- ❌ Empty states: Continue panel has compact empty state
- ❌ Icon consistency: Monochrome icons but mixed stroke weights
- ❌ Typography: Limited font weights (regular only)
- ❌ Spacing: Magic numbers, not consistent 8pt grid

#### 4. Performance Issues (Phase 3)
- ❌ Per-frame allocations: Textures created every frame
- ❌ No texture caching: Thumbnails downloaded fresh each time
- ❌ No list virtualization: Full lists rendered
- ❌ No idle mode: Polling continues when scene hidden
- ❌ No power profiles: No battery/thermal awareness
- ❌ No render budget: No frame time limits

#### 5. Reliability Issues (Phase 4)
- ❌ No config validation: Invalid JSON crashes app
- ❌ No atomic writes: Config corruption possible
- ❌ No backup: No last-known-good config
- ❌ No crash recovery: No unclean shutdown detection
- ❌ No offline handling: Network failures crash UI
- ❌ No missing tool detection: `yt-dlp`/`ffplay` errors in terminal
- ❌ No log viewer: Logs only to stderr

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
**Status**: 🔄 In Progress

#### 2.1 Design System
- [x] Semantic color tokens in `design.go`
- [ ] Add spacing tokens (8, 12, 16, 24, 32)
- [ ] Add radius tokens (small, medium, large)
- [ ] Add typography tokens (title, heading, body, label)
- [ ] Add motion tokens (fast, standard, reduced)
- [ ] Add density tokens (compact, comfortable)

#### 2.2 Component System
- [ ] Create `AppBar` component
- [ ] Create `StatusBar` component
- [ ] Create `FooterHints` component
- [ ] Create `Card` component
- [ ] Create `CategoryTile` component
- [ ] Create `Button` component
- [ ] Create `IconButton` component
- [ ] Create `ListRow` component
- [ ] Create `Grid` component
- [ ] Create `ProgressBar` component
- [ ] Create `Badge` component
- [ ] Create `Toast` component
- [ ] Create `EmptyState` component
- [ ] Create `ErrorState` component
- [ ] Create `LoadingSkeleton` component

#### 2.3 Focus System
- [ ] Create `Focusable` interface
- [ ] Build explicit focus graph per scene
- [ ] Implement focus preservation on scene return
- [ ] Add deterministic directional navigation
- [ ] Add focus animation (100-140ms)
- [ ] Add reduced-motion mode
- [ ] Add input remapping screen
- [ ] Add controller test screen

#### 2.4 Platform Abstraction
- [ ] Define `Platform` interface
- [ ] Create `platform/tsp/` for Trimui/Linux
- [ ] Create `platform/win/` for Windows
- [ ] Move OS-specific paths to platform layer
- [ ] Move process flags to platform layer
- [ ] Move power behavior to platform layer
- [ ] Move tool discovery to platform layer

### Phase 3: Async Work & Performance
**Status**: ⏳ Pending

#### 3.1 Concurrency Model
- [ ] Add `context.Context` everywhere
- [ ] Add `errgroup` for coordinated cancellation
- [ ] Add bounded semaphore for concurrency limits
- [ ] Create worker pool for image decoding (2 jobs on Trimui)
- [ ] Create worker pool for network requests (2-3 jobs on Trimui)
- [ ] Debounce search input (250-350ms)
- [ ] Cancel scene work on scene exit

#### 3.2 Caching
- [ ] Add font texture cache (key: font+size+color+string)
- [ ] Add icon texture atlas
- [ ] Add thumbnail cache with byte limit
- [ ] Add network response cache with TTL
- [ ] Add LRU eviction policy

#### 3.3 Rendering
- [ ] Cache text textures
- [ ] Cache static icons in atlas
- [ ] Reuse card/label/image objects
- [ ] Add clipping for lists/grids
- [ ] Add idle render mode
- [ ] Add frame time budget

#### 3.4 Power Management
- [ ] Add Battery Saver profile
- [ ] Add Balanced profile
- [ ] Add Performance profile
- [ ] Stop polling when scene hidden
- [ ] Reduce animation in battery saver

### Phase 4: Reliability & Configuration
**Status**: ⏳ Pending

#### 4.1 Configuration
- [ ] Add schema versioning
- [ ] Add config validation
- [ ] Add migrations for old configs
- [ ] Add atomic writes (temp file + rename)
- [ ] Add last-known-good backup
- [ ] Add config hot-reload (Windows dev only)
- [ ] Add portable mode for packaging

#### 4.2 Logging
- [ ] Switch to `log/slog`
- [ ] Add structured fields (subsystem, scene, action, duration)
- [ ] Add JSON logs for Windows
- [ ] Add compact logs for Trimui
- [ ] Add log rotation
- [ ] Add in-app log viewer
- [ ] Add diagnostic bundle export

#### 4.3 Error Handling
- [ ] Add offline detection
- [ ] Add cached fallback for offline content
- [ ] Add guided remediation for missing tools
- [ ] Add safe mode (disable plugins, themes, networking)
- [ ] Add unclean shutdown detection
- [ ] Add crash recovery UX

#### 4.4 Network
- [ ] Create shared HTTP client
- [ ] Add connection reuse
- [ ] Add request timeouts
- [ ] Add max response sizes
- [ ] Add retry with exponential backoff + jitter
- [ ] Add conditional caching
- [ ] Add offline detection

### Phase 5: Features
**Status**: ⏳ Pending

#### 5.1 Media
- [ ] Add media-backend interface (ffplay/MPV)
- [ ] Add playback queue
- [ ] Add resume position
- [ ] Add progress display
- [ ] Add cancellation
- [ ] Add clear failure messages

#### 5.2 Files
- [ ] Add file explorer improvements
- [ ] Add drag-and-drop (Windows)
- [ ] Add clipboard integration (Windows)

#### 5.3 Packages
- [ ] Add package trust UX
- [ ] Add source/version/checksum display
- [ ] Add uninstall rollback

#### 5.4 Settings
- [ ] Add controller remapping
- [ ] Add accessibility settings
- [ ] Add power profile selection
- [ ] Add theme preview

### Phase 6: Testing
**Status**: ⏳ Pending

- [ ] Add config validation tests
- [ ] Add focus graph navigation tests
- [ ] Add input normalization tests
- [ ] Add layout bounds tests (1280×720)
- [ ] Add cache eviction tests
- [ ] Add cancellation tests
- [ ] Add external process cleanup tests
- [ ] Add URL/path validation tests
- [ ] Add offline/retry behavior tests
- [ ] Add platform path selection tests
- [ ] Add benchmarks for render, cache, navigation

### Phase 7: Web Builder
**Status**: ⏳ Pending

- [ ] Add semantic design tokens
- [ ] Add reusable components
- [ ] Add snap-to-grid
- [ ] Add alignment guides
- [ ] Add safe-area overlay
- [ ] Add focus-order editor
- [ ] Add state previews
- [ ] Add undo/redo
- [ ] Add autosave
- [ ] Add Trimui performance linting
- [ ] Add schema validation
- [ ] Add production export

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
- [ ] Texture/thumbnail caches have entry/byte limits
- [ ] Idle scenes reduce polling/rendering
- [ ] Trimui build stable under repeated scene changes

### Reliability
- [ ] Corrupt user config doesn't prevent startup
- [ ] Missing tools produce guided remediation
- [ ] Writes are atomic and recoverable
- [ ] Plugins can be disabled via safe mode
- [ ] Logs identify scene/action/operation/platform/duration
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

1. **Complete Phase 1** (UI/UX fixes) - ✅ Done
2. **Start Phase 2** (Architecture refactoring)
   - Create component system
   - Build focus graph
   - Add platform abstraction
3. **Implement Phase 3** (Async work)
   - Add context cancellation
   - Create worker pools
   - Add caching
4. **Implement Phase 4** (Reliability)
   - Add config validation
   - Add structured logging
   - Add error handling
5. **Add Features** (Phase 5)
6. **Write Tests** (Phase 6)
7. **Update Web Builder** (Phase 7)

## Notes

- This is an **incremental** modernization, not a rewrite
- Each phase must produce **working builds**
- Preserve **existing features** unless clearly broken
- Keep **config-driven model** intact
- Optimize for **Trimui constraints** (1GB RAM, ARM64)
- Support **Windows** as secondary target
- Use **Go standard library** where possible
- Add **tests** as we go, not at the end