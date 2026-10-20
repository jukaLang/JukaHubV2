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
| [BUILDING.md](BUILDING.md) | Windows/Linux/ARM64 builds, packaging, releases, CI |
| [CONFIGURATION.md](CONFIGURATION.md) | config files, variables, elements and every trigger |
| [CONTROLS.md](CONTROLS.md) | controller and keyboard bindings per screen |
| [PACKAGES.md](PACKAGES.md) | package format, the signed repository, Patch transactions |
| [GAME_PROFILES.md](GAME_PROFILES.md) | game-profile schema, Box64/Wine launching, import/export |
| [WINDOWS_APPS.md](WINDOWS_APPS.md) | .exe discovery, Wine display bridge, WestonPack setup |
| [PLUGINS.md](PLUGINS.md) | plugin manifest and the script execution contract |
| [SECURITY.md](../player/SECURITY.md) | secret storage, crypto, threat notes |
| [trimui/README-ID.md](../player/trimui/README-ID.md) | device package and launcher details |

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
│   ├── videoplayer.go, videosubs.go   # embedded ffmpeg playback	│   ├── patch_*.go, packages.go        # package manager
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
5. Start the background services — tool health, Patch, updater, disk chart,
   network monitor, FTP/SSH servers, screensaver, notifications — each behind a
   safe-mode check.

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

## Design system

Controller-first, dark, optimised for a 1280×720 canvas read at arm's length.

### Scene shell

1. **Global header** (56 px) — brand, version and the active scene on the left;
   network / weather / clock / battery on the right, from one shared helper so
   it never shifts between screens.
2. **Scene body** — clipped inside a content panel that ends 16 px above the
   footer, so no element can reach the footer band.
3. **Footer** (48 px) — hints that reflect real bindings.
4. Toasts, the start menu and overlays render last, on top of everything.

### Home screen

```text
┌──────────────────────────────────────────────────────────────────────────┐
│  JukaHub - v0.4.0 · 2026-10-17                 Wi-Fi  24C/18C  16:47  82% │
├──────────────────────────────────────────────────────────────────────────┤
│  Good afternoon · What do you want to do?                                 │
│  ┌───────────────────────┐  ┌─────────────┐ ┌─────────────┐              │
│  │ CONTINUE              │  │    Media    │ │    Files    │              │
│  │ Nothing played yet    │  └─────────────┘ └─────────────┘              │
│  │ Open Media to start   │  ┌─────────────┐ ┌─────────────┐              │
│  │               [A]     │  │  Packages   │ │     Apps    │              │
│  └───────────────────────┘  └─────────────┘ └─────────────┘              │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐                 │
│  │Favorites │ │  Chat    │ │  Tools   │ │  Settings    │                 │
│  └──────────┘ └──────────┘ └──────────┘ └──────────────┘                 │
├──────────────────────────────────────────────────────────────────────────┤
│  D-Pad Navigate    A Open    B Back    L1/R1 Sections                     │
└──────────────────────────────────────────────────────────────────────────┘
```

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

Full details: [player/SECURITY.md](../player/SECURITY.md).

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
- The shortcuts overlay lists `F5` (refresh) and `Ctrl+L` (log viewer); only
  `Ctrl+L` *inside the terminal* (clear output) is implemented.
