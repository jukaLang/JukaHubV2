# JukaHub developer guide

Orientation for working on the player. Practical tasks live on their own pages;
this page is the architecture and the design system.

## How it works

JukaHub has two parts:

1. **Web builder** — the drag-and-drop editor at
   [generate.jukalang.com](https://generate.jukalang.com) (this repository's
   `index.html`, `style.css`, `script.js`) that produces `jukaconfig.json`.
2. **Player** — the Go + SDL2 runtime in `player/`. It reads the config and
   renders it, so changing the file changes the app with no rebuild.

The player is controller-first, drawn entirely with SDL2 primitives, and
targets a 1280×720 logical canvas.

## Where to read next

| Page | Covers |
|------|--------|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | local setup, the checks to run, tests, what to commit |
| [QUICK_START.md](QUICK_START.md) | the five-minute path: build, run, check, open a PR |
| [ARCHITECTURE.md](ARCHITECTURE.md) | the three runtimes and where a change lands |
| [BUILDING.md](BUILDING.md) | Windows/Linux/ARM64 builds, packaging, releases, CI |
| [CONFIGURATION.md](CONFIGURATION.md) | config files, variables, elements and every trigger |
| [CONTROLS.md](CONTROLS.md) | controller and keyboard bindings per screen |
| [PACKAGES.md](PACKAGES.md) | package format, the signed repository, Patch transactions |
| [GAME_PROFILES.md](GAME_PROFILES.md) | game-profile schema, Box64/Wine launching, import/export |
| [WINDOWS_APPS.md](WINDOWS_APPS.md) | .exe discovery, Wine display bridge, WestonPack setup |
| [ANDROID.md](ANDROID.md) | the Android container engine: binder gate, package list, launch and display bridge |
| [PLUGINS.md](PLUGINS.md) | plugin manifest and the script execution contract |
| [SECURITY.md](../SECURITY.md) | secret storage, crypto, threat notes |
| [IMPROVEMENTS.md](IMPROVEMENTS.md) | what we learned from BrickOS, the backlog it implies, and where we are ahead |
| [FEATURE-BACKLOG.md](FEATURE-BACKLOG.md) | netplay, OSD/recording, simple mode, box art, game switcher: what exists and what cannot work |
| [trimui/README.md](../player/trimui/README.md) | device package and launcher details |

## Repository layout

```text
JukaHubV2/
├── index.html, style.css, script.js   # web builder UI
├── player/                            # the Go application
│   ├── main.go                        # entry point, event loop, background services
│   ├── core.go                        # scene rendering, input, trigger dispatch
│   ├── controller.go                  # gamepad / keyboard / touch routing
│   ├── focus.go                       # focus graph and spatial navigation
│   ├── design.go, themes.go           # design tokens and theme presets
│   ├── homelayout.go, home.go         # Home screen
│   ├── vkeyboard.go                   # on-screen keyboard
│   ├── videoplayer.go, videosubs.go   # embedded ffmpeg playback
│   ├── patch_*.go, packages.go        # package manager
│   ├── community.go, changelog.go     # community hub, bug report, release notes
│   ├── powertools.go, powertools_backend.go  # developer tools overlay and probes
│   ├── telemetry.go                   # opt-in device/crash telemetry
│   ├── plugins.go                     # plugin loader and runner
│   ├── widgets.go, autostart.go       # contextual widgets, start-at-login
│   ├── config.go, xmlconfig.go        # config types and both loaders
│   ├── *_test.go                      # unit, render and focus tests
│   ├── jukaconfig.json                # committed design config
│   ├── trimui/                        # device package assets and notes
│   └── build.bat, release.sh, package-sdcard.sh
├── docs/                              # user and developer documentation
└── .github/workflows/                 # CI: SD-card package, releases, tags
```

## Architecture

### Startup

`main.go` runs one sequence, in order, because each step depends on the last:

1. SDL/TTF/image init, then load the config (JSON or XML, whichever is newer —
   see [CONFIGURATION.md](CONFIGURATION.md)) and validate it.
2. Merge `jukauser.json` over the design defaults.
3. Tune the garbage collector for the device, then resolve safe mode (plugins,
   updates and network work off for this launch).
4. Inject the scenes the config does not ship: the alarm editor and the Smart
   Widgets page. Both are in-memory only, so a user config is never rewritten.
5. Start the background services — Patch, disk chart, FTP/SSH servers,
   screensaver — each behind a safe-mode check. Network work (weather, tool
   health, telemetry, the update check, the network monitor, the companion
   listener and social notifications) is queued with `deferStartupTask` and run
   after the first frame instead of competing with cold font rasterisation.

The boot budget is two seconds from process start to a usable first frame,
managed in `startup.go`. `startupMarkNow` records a cheap timestamp per phase
(pre-SDL, SDL, config, modules, window, splash, first frame) and
`startupLog` prints one line with the deltas and the total:

```
startup: pre-sdl=0ms sdl=+18ms config=+5ms modules=+71ms window=+152ms splash=+761ms first-frame=+112ms total=1122ms target=2000ms
```

The splash is not a fixed animation: it receives what is left of the budget
after the phases above, minus a reserve for the first frame (300 ms, or half the
boot cost when the boot is slower than that). It is capped at 750 ms and
skipped entirely when less than 250 ms remain, so a slow card opens the UI
directly instead of adding an animation on top. The whole 3.6 s splash timeline
is compressed into whatever window it gets, so a short run still shows the
complete sequence.

A relaunch within ten minutes skips the splash entirely. The marker in
`<data>/startup-state.json` is written only after a successful first frame, so
a crashed or still-booting launch is never mistaken for a warm one. The bench
mode always measures the cold path; `JUKAHUB_BENCH_WARM=1` exercises the warm
path with the same exit harness.

### Boot measurements

Headless Windows desktop (SDL dummy drivers, shipped 1280x720 config, cold
runs before and after the October 2026 pass):

| Phase | Before | After |
|---|---:|---:|
| SDL init | 20–24 ms | 17–19 ms |
| config | 3–5 ms | 3–5 ms |
| modules (scene setup, deferred listeners removed) | 90–110 ms | 66–83 ms |
| window + renderer | 148–300 ms | 148–161 ms |
| splash | 757–776 ms | 757–770 ms |
| first frame | 121–133 ms | 109–112 ms |
| **cold total** | **1165–1321 ms** | **1107–1129 ms** |
| **warm total (splash skipped)** | — | **370–408 ms** |

These are desktop numbers. On the TrimUI the SD card and the 1 GB CPU move the
phases, which is why the same script runs on the device; the 2 s target is a
device claim. The important comparison is warm vs cold, not the absolute
desktop milliseconds.

`GODEBUG=inittrace=1` shows only ~2.8 ms of package initialisation in total
(`main` 1.2 ms, `net` 1.0 ms, gopsutil 0.6 ms), so lazy-loading imports is not
where the remaining time is. The first frame is rasterisation, not init.

Environment variables:

| Variable | Effect |
|----------|--------|
| `JUKAHUB_STARTUP_TARGET_MS` | boot budget in ms (500–30000), default 2000 |
| `JUKAHUB_STARTUP_BENCH=1` | exit after the first frame, so a headless run can be timed |
| `JUKAHUB_BENCH_WARM=1` | with bench mode, use the warm path instead of forcing cold |
| `JUKAHUB_STARTUP_STATE` | override the marker path (the bench uses a temp file) |
| `JUKAHUB_BENCH_RUNS`, `JUKAHUB_BENCH_LIMIT_MS` | sample count and threshold for `startup-bench.sh` |

`startup-bench.sh` builds once, deletes the marker before every sample and
fails on a median over the limit. CI runs it with a 5 s limit (the runner is
shared and slower); on a device run it with the 2 s budget:

```bash
cd player
JUKAHUB_BENCH_RUNS=5 JUKAHUB_BENCH_LIMIT_MS=2000 ./startup-bench.sh
```

`JUKAHUB_STARTUP_BENCH=1` with `SDL_VIDEODRIVER=dummy` is how boot time is
checked without a display; on a development machine the reported total is
~1.1 s cold and ~0.38 s warm.

### Render loop and cadence

One loop per frame: poll events → advance animation state → render the scene
plus overlays → present → sleep. The sleep comes from the frame-rate controller
in `adaptive.go`, which picks a cadence from activity:

| Platform | Active | Idle | Deep idle |
|----------|-------:|-----:|----------:|
| Handheld (TrimUI) | 33 ms (~30 fps) | 42 ms (~24 fps) | 66 ms (~15 fps) |
| Desktop | 16 ms (~60 fps) | 33 ms (~30 fps) | 50 ms (~20 fps) |

The handheld is capped at 30 fps on purpose: it cannot sustain 60 while drawing
menus, the keyboard or decoded video, and the extra frames only cost battery and
heat. The `lowPower` setting caps the cadence at the idle profile. Animations
select the fastest tier through `SetAnimationActive`.

Battery saver (see the Smart Widgets page) turns `lowPower` and `reducedMotion`
on and switches to the OLED theme below 20%, restoring your own settings when
charged again.

### Scenes

A scene either uses the generic element renderer or owns its body. Owning it is
one small interface ([`player/scene.go`](../player/scene.go)) registered from the
scene's own file:

```go
type sceneModule interface {
	sceneName() string
	render(renderer *sdl.Renderer, config *Config) bool      // false → generic renderer
	handleKey(e *sdl.KeyboardEvent, config *Config) bool     // false → generic navigation
	handleController(e *sdl.ControllerButtonEvent, config *Config) bool
}
```

`sceneBase` supplies the name and the no-op defaults, so a scene declares only
the methods it needs and an `init` registers it:

```go
type alarmModule struct{ sceneBase }

func (alarmModule) render(r *sdl.Renderer, c *Config) bool { renderAlarmClock(r, c); return true }
func (alarmModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleAlarmInput(e, c)
	return true
}
func (alarmModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlAlarm(e)
}

func init() { registerScene(alarmModule{sceneBase{name: "Alarm"}}) }
```

All three dispatchers (`renderScene` in `core.go`, the key chain in `main.go`,
`handleSceneController`) ask the registry, so a scene is defined in exactly one
table: its own `init`. The scene's renderer, key handler and controller mapping
live in the same file. Scenes that need nothing special — the config-driven
screens — are simply not registered and keep the generic path.

Overlays (wallpaper picker, social notifications, Quick Settings, search) are
checked before scene routing, so a floating panel always wins the D-pad.

Before adding a scene, check the name is not already taken by a different type
in the file (for example the alarm editor already had an `alarmScene`); the
registry logs and ignores a duplicate name instead of panicking at startup.

### Scene-scoped background work

Work that only matters while a screen is up belongs to that screen. A scene
transition cancels the previous generation and starts a new one
([`player/scenetasks.go`](../player/scenetasks.go)):

```go
runSceneTask(func(ctx context.Context) {
	fetchSomethingContext(ctx)
	if ctx.Err() != nil {
		return // the scene is gone; do not publish into the next one
	}
	publishCustom("something", value)
})
```

The yt-dlp media searches joined the same scope: `executeYouTubeSearch` and
`executeYouTubeSearchPage` spawn their worker through `runSceneTask`
(`executeYouTubeSearchContext`), so a search or a Shorts fetch belongs to the
screen that shows its list. A scene transition cancels the run, kills the
extractor process through `exec.CommandContext` and publishes nothing into the
next screen — before this, leaving while a search loaded left yt-dlp running
behind the home screen, with its results landing there afterwards.

Rules that keep it honest:

- pass `ctx` into the request (`httpGetTextContext`, `fetchURLContext`,
  `doWithRetry`) so cancellation closes it instead of waiting out a timeout;
- check `ctx.Err()` before publishing: a cancelled task must not leave a stale
  value on the next screen;
- use `runCancelableGroup` when one unit of scene work fans out into several
  calls and one failure should stop the rest — it is the errgroup contract,
  kept in-tree because the vendored module set is pinned;
- clean up temporary state on the cancel path (the benchmarks remove their temp
  file);
- `runSceneTask` never blocks the caller: a transition does not wait for the
  network. Tests join tasks with `waitForSceneWork`.

Process-wide chrome (network monitor, weather ticker, audio output) is not scene
work and keeps its own goroutine.

### Focus and navigation

`focus.go` builds a spatial focus graph per scene from the focusable elements,
plus the top-bar chrome (back pill, Apps pill, clock, notification bell), which
is hand-wired left-to-right so a diagonal vector cannot hijack Left/Right along
a content row. The graph is rebuilt on each key press so dynamic buttons and
freshly measured chrome rects are included, and re-installing it keeps the
current node instead of resetting your place.

Home is the exception: it draws its own layout, so `homefocus.go` owns a
separate graph. The header chips are nodes there too (weather, clock, the
notification bell and the update offer), each enabled only while the layout
actually drew it — that is how **Right** from the clock reaches the bell on
Home, where the chrome graph is not in charge.

Back navigation is universal: the header pill, **B**, `Escape` and any
body-level Back trigger all pop the same scene history, falling back to Home.

### Config, schema and compatibility

The config format is versioned (`schemaVersion`, currently **1**): older files
are migrated in memory, and a file from a newer build is refused rather than
downgraded and later overwritten. Unknown element types are logged and skipped;
unrecognised triggers are inert. Both alone mean a config from a newer builder
degrades instead of crashing.

### Menu audit

`player/menu_audit_test.go` walks both shipped configs (`jukaconfig.json` and
`trimui/jukaconfig.device.json`) and fails when the UI and the code disagree:

- every scene has a renderer (a registered scene module or renderable elements),
  so no menu row leads to a blank screen;
- every button trigger is a `case` in `handleTrigger` — the test parses that
  switch, so a trigger that only survives in a comment does not count;
- every `change_scene:` target, Apps row and home card resolves to a scene the
  config really has;
- interactive elements stay inside the content panel and never overlap;
- every scene paints text when rendered offscreen;
- a scene that ships in both configs has identical geometry on the desktop and
  the handheld — both panels are 1280×720, so a screen has no reason to look
  different.

`TestFooterHintsMatchTheHandlers` keeps the footer badges honest (Chat says
"A Type", because A opens the keyboard), and
`TestAboutSceneDrawsTheSharedChrome` pins the About screen to the shared header
and footer. Set `JUKAHUB_SCENE_DUMP_DIR=/tmp/scenes` to have the audit write one
PNG per scene for a visual review.

The test binary turns off `jukauser.json` writes (`TestMain` in
`testmain_test.go`), so a run cannot rewrite the checkout's user config; the few
tests that assert a real save re-enable `userConfigWritesEnabled` themselves.

A test seam that production code reads **on a goroutine** must be swapped under
a lock, not by assignment. `browserPageFetcher` is the worked example: the load
path reads it once per request through `currentBrowserPageFetcher`, tests swap
it with `setBrowserPageFetcherForTest`, and `resetBrowserStateForTest` joins the
loads still on the wire (`waitForBrowserFetches`). A bare swap is a real data
race — the `player-race` CI job caught exactly that, because a page load can
outlive the test that started it. `TestBrowserFetcherSwapDuringLoadIsSafe` keeps
the pattern honest.

### Text input and wrapping

Every buffer that accepts typed text must delete with `trimLastRune`, never by
slicing a byte: the on-screen keyboard inserts arbitrary runes, so a byte-sliced
backspace leaves half a character behind and the next press looks dead.
`TestNoByteSlicedTextEdits` fails on any `x = x[:len(x)-1]` in the package (the
a few slice-of-lines cases are listed in the test).

Use `wrapTextLines` for dialog and card copy. `renderText` draws a single line:
passing it a string with `\n` used to run the text off the panel instead of
breaking it.

### Memory on the device

`applyGCForDevice()` installs a soft heap limit before the first heavy
allocation, and a memory-pressure monitor evicts caches on the handheld when
free RAM drops. Caches (textures, text, thumbnails, chat images) are bounded
and invalidated when their inputs change — follow that pattern rather than
adding an unbounded map.

Per-frame paths must not allocate. Text goes through `substituteVars` and
the texture cache for every label on every frame, so both have zero-allocation
fast paths (`varPattern` is compiled once; the cache key is a struct and its
LRU is O(1)). The Live TV filter bar reads a cached count instead of filtering
the whole playlist per frame, and the File Explorer toolbar caches its
validated path instead of stat-ing the SD card. `go test -bench .` in `player/`
measures those paths (`BenchmarkFrameTextPass`, `BenchmarkTVFilterBarCounts`).

The frame itself has a budget too: `render_bench_test.go` draws a real 1280×720
home frame (warm and with a cold text cache) and times one D-pad step, so a
layout or text change can be compared against a number rather than a
feeling:

```
cd player
go test -run '^$' -bench 'BenchmarkHome' -benchmem
```

The absolute values are a desktop software-renderer proxy (see the recorded
numbers in [MODERNIZATION.md](../player/MODERNIZATION.md), Phase 6);
their job is to catch a regression, not to predict the device.

## Design system

Controller-first, dark, optimised for a 1280×720 canvas read at arm's length.

### Scene shell

1. **Global header** (56 px) — brand, version and the active scene on the left;
   the Wi-Fi meter, weather, clock, battery gauge and (while playing) the
   now-playing title with its headphone glyph on the right, from one shared
   helper so it never shifts between screens. The meter and the gauge are drawn
   by `player/statusbar_status.go`, the title by `player/statusbar_media.go`, for
   both this bar and the home header; the cluster itself is still
   `headerStatusParts` returning strings.
2. **Scene body** — clipped inside a content panel that ends 16 px above the
   footer, so no element can reach the footer band.
3. **Footer** (48 px) — hints that reflect real bindings.
4. Toasts, the start menu and overlays render last, on top of everything.

### Home screen

The grid is fixed in `homelayout.go` (a config can retarget a card, not
rearrange it): **Continue** spans the left column, **Media**, **Files**,
**Packages** and **Apps** fill the middle-right, **Settings** takes the top-right
corner, and **Favorites**, **Browser**, **Chat**, **Tools** and **Emulator** run
along the bottom row. **Exit** sits on the footer's left end. The header carries
the weather chip, the clock, the notification bell and the update offer, and the
PS4/PS5 launcher tiles and phone-pairing QR are drawn just below it on the right.

### Ambient background

Four soft colour blobs (cyan, purple, blue, soft white), tinted from the active
theme, orbit and breathe on multi-minute cycles at low alpha. They are drawn
from one cached radial-glow texture with a handful of copies per frame — no
per-frame allocation and no blur shaders. Cards and text stay opaque on top.
`variables.dynamicBg` swaps this for the animated, weather-aware background.

### Colour tokens

Base palette from `design.go` (theme presets override these through
`ApplyThemeColors`):

| Token | Value |
|-------|-------|
| Background | `#090E19` |
| Top bar | `#111725` |
| Surface / panel | `#121A2A` |
| Card | `#182236` |
| Card focused | `#24344E` |
| Card hover | `#1C273C` |
| Border | `#2A3953` |
| Border focus | `#6EE7FF` at ~31% alpha |
| Accent | `#55D8FF` |
| Accent secondary | `#8B7CFF` |
| Text primary / secondary / tertiary | `#F6F8FC` / `#9CA9BD` / `#66758B` |
| Success / warning / danger | `#48D597` / `#FFBF69` / `#E26878` |

Spacing follows a 4 pt grid (`SpaceXS`…`Space3XL`) with an "airy" variant for
larger screens; corner radii match the same scale (`RadiusSM` … `RadiusXXL`).

### Typography

Bundled Inter (`Inter-Regular.ttf`). Text is drawn through the caching text
renderer, which reads the font's `cmap` and substitutes characters the font
cannot draw (full-width forms, `【】`, circled digits, emoji) so a title never
becomes a row of boxes; characters with no sensible substitute collapse to a
single `…`. A font role that is not configured falls back to
`Inter-Regular.ttf` at 24 px.

### Focus and icons

- The focused card gets a bright border on a raised surface, readable in
  grayscale and from viewing distance.
- Focus never changes card geometry; pressed feedback darkens the card and
  shifts content 1–2 px briefly.
- Icons are drawn with SDL geometry — never emoji or glyphs the font may lack.

### Themes

25 presets, applied live from Settings → Themes or the Smart Widgets page and
persisted in `jukauser.json`: `dark`, `light`, `oled`, `midnight`, `hub`,
`ocean`, `sunset`, `red`, `green`, `blue`, `highcontrast`, `anime`, `cute`,
`girly`, `sakura`, `lavender`, `sand`, `nord`, `gruvbox`, `dracula`, `matrix`,
`fallout`, `cyberpunk`, `mono`, `vaporwave`. Each preset defines background,
surfaces, text, borders and the accent colour. The gallery order is pinned in
`themePresetOrder` — never rely on map iteration, which would shuffle the cards
every frame.

A `themes/*.json` file next to the app adds a preset to the gallery at startup
(`themeloader.go`): every colour must be `#rrggbb` or `#rrggbbaa`, the file name
is the preset id, a built-in id wins on a collision, and invalid files are
logged and skipped.

## Security

Sensitive values (API keys, tokens) are encrypted with AES-256-GCM and can be
prepared from a PC:

```bash
cd player
./JukaHub -set-secret discord_token        # value read from stdin
./JukaHub -check-secrets                   # which values decrypt, and why not
```

Set `JUKAHUB_CRYPTO_KEY` (and optionally `JUKAHUB_CRYPTO_SALT`) before
launching; encrypted values look like `ENC:default-v3-enhanced/…` in the
config, and the player warns about plaintext secrets, the built-in default key
and tokens that fail validation. Never commit a config with real credentials —
`jukaconfig.json` and `jukauser.json` are git-ignored for that reason.

Full details: [SECURITY.md](../SECURITY.md).

## Key files

| File | Purpose |
|------|---------|
| `player/main.go` | entry point, SDL2 init, event loop, background services |
| `player/core.go` | scene rendering, input routing, text helpers, triggers |
| `player/scene.go` | scene interface, registry and per-scene routing |
| `player/focus.go` | focus graph, spatial navigation, chrome tiers |
| `player/controller.go` | gamepad, keyboard and touch routing |
| `player/design.go`, `player/themes.go` | design tokens and theme presets |
| `player/config.go`, `player/xmlconfig.go` | config types, JSON/XML loading |
| `player/config_health.go` | config validation, atomic writes, backups |
| `player/crypto.go` | AES-256-GCM encryption and key management |
| `player/patch_engine.go` | install / update / verify / rollback engine |
| `player/patch_signed.go` | signed repository verification (Ed25519) |
| `player/plugins.go` | plugin loader and script runner |
| `player/companion.go` | phone companion HTTP server, PIN auth, uploads (no SDL) |
| `player/companionui.go` | companion QR card and phone-to-SDL input bridge |
| `player/widgets.go` | battery saver, time-of-day theme, Smart Widgets page |
| `player/build.bat` | Windows build wrapper (toolchain check, SDL2 staging) |
| `player/package-sdcard.sh` | TrimUI SD-card package |
| `player/release.sh` | release artifacts and `manifest.json` |

## Roadmap

Ported from JukaHub V1: canvas sandbox, favorites tabs, unit converter, disk
space chart, ffplay/ffmpeg playback, image viewer, chat, YouTube Shorts, random
I/O benchmark, log viewer, theme presets, plugin system — all done.

Open work, documented here so it is not rediscovered:

- A plugin install/uninstall screen (the API exists, the UI does not).
- Drawing callbacks for plugins (`PluginContext` is declared but never passed).
- Fixing the intermittent native crash in the fullscreen video test — see the
  note in [CONTRIBUTING.md](../CONTRIBUTING.md).
- The shortcuts overlay lists `F5` (refresh), which is not wired to anything
  yet. `Ctrl+L` is: it opens the LogExporter scene everywhere except the
  terminal, which keeps Ctrl+L to clear its output.

[IMPROVEMENTS.md](IMPROVEMENTS.md) holds the comparison-driven backlog: update
UX, button remapping, component versions, and the BrickOS failure modes worth
copying. Add to that list instead of starting a second one.
