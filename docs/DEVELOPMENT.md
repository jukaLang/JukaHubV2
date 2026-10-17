# JukaHub developer guide

Everything below is for building, packaging and extending JukaHub. For
installation on a handheld, see the [README](../README.md).

---

## How it works

JukaHub has two parts:

1. **Web builder** — drag-and-drop interface at
   [generate.jukalang.com](https://generate.jukalang.com) that produces
   `jukaconfig.json`.
2. **Player** — this repository's Go + SDL2 runtime. It reads
   `jukaconfig.json` and renders the interface. Changing the file changes the
   app; no recompilation.

The player is controller-first, drawn entirely with SDL2 primitives, and
targets a 1280×720 logical canvas.

## Repository layout

```text
JukaHubV2/
├── index.html, style.css, script.js   # web builder UI
├── player/                            # the Go application
│   ├── main.go                        # entry point, event loop
│   ├── core.go                        # scene rendering, input, text helpers
│   ├── vkeyboard.go                   # on-screen keyboard
│   ├── controller.go                  # gamepad / keyboard / touch routing
│   ├── home.go, dashboard.go          # Home screen
│   ├── patch_*.go                     # package manager
│   ├── tools.go, netmon.go            # tool download and network helpers
│   ├── design.go, themes.go           # design tokens and theme presets
│   ├── jukaconfig.json                # design config (generated)
│   ├── trimui/                        # device package assets and notes
│   └── build.bat, release.sh, package-sdcard.sh
├── .github/workflows/                 # CI: SD-card package, releases
└── docs/                              # developer documentation
```

---

## Building

### Windows

Requires Go 1.25+ and a MinGW-w64 GCC (CGO links against SDL2).

```bash
cd player
./build.bat
```

`build.bat` checks for GCC, downloads the SDL2 development libraries into
`.sdl2/`, builds `JukaHub.exe`, copies the SDL2 DLLs next to it and launches
the app. Set `SDL2_DIR` to use an existing SDL2 installation.

Manual build:

```bash
cd player
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64
set CGO_CFLAGS=-I%CD%\.sdl2\x86_64-w64-mingw32\include
set CGO_LDFLAGS=-L%CD%\.sdl2\x86_64-w64-mingw32\lib
go build -o JukaHub.exe .
```

### Linux (development)

```bash
sudo apt install libsdl2-dev libsdl2-image-dev libsdl2-ttf-dev
cd player
go build -o JukaHub .
./JukaHub
```

### ARM64 handhelds

```bash
export GOOS=linux GOARCH=arm64 CGO_ENABLED=1
export CC=aarch64-linux-gnu-gcc
go build -o JukaHub .
```

The TrimUI Smart Pro needs the official SDK sysroot (glibc 2.23, SDL2 2.26.1),
so use the packaging script instead of a plain cross-build:

```bash
cd player
SYSROOT=/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot ./package-sdcard.sh
```

This produces `dist/JukaHub-SDCARD/Apps/JukaHub/` and
`dist/JukaHub-SDCARD.zip`, including ARM64 `yt-dlp` and ffmpeg builds. The same
package is built by the **Build SD card package** workflow, which uploads the
`JukaHub-SDCARD` artifact on every push to `main`.

TrimUI notes, launcher registration and the on-device config are described in
[../player/trimui/README-ID.md](../player/trimui/README-ID.md).

---

## Configuration

Two JSON files:

### `jukaconfig.json` — design config

Static layout: scenes, elements, positions, colours, fonts, variables. Generated
by the web builder. Do not hand-edit unless you know the schema.

### `jukauser.json` — user data

Created automatically next to the app. Stores mutable settings: theme overrides,
fullscreen, weather unit, username, playback resolution, file-explorer root and
custom variables.

### Variables reference

| Variable | Type | Description |
|----------|------|-------------|
| `buttonColor` | `#rrggbb` | Button background color |
| `labelColor` | `#rrggbb` | Label text color |
| `inputColor` | `#rrggbb` | Input field background |
| `fullscreen` | `bool` | Launch in fullscreen |
| `screenWidth` | `int` | Logical screen width |
| `screenHeight` | `int` | Logical screen height |
| `weatherEnabled` | `bool` | Enable weather fetch |
| `weatherUnit` | `"C"` / `"F"` | Temperature unit |
| `tspUsername` | `string` | Username for greetings |
| `playbackResolution` | `string` | Max video height (e.g. `"720"`) |
| `fileExplorerRoot` | `string` | Root directory for the file browser |
| `tools_path` | `string` | Path to yt-dlp / ffplay binaries |

---

## Patch & updates (package manager)

**Patch** lives under **MISC → Patch** ("Update and repair JukaHub"). It is a
small, safe package manager for the TrimUI Smart Pro — `apt-get` for your own
files — plus JukaHub's own update flow.

### The package index: `packages.json`

Packages live in one editable JSON index in the Patch state directory, next to
the installation:

```json
{
  "schema": 1,
  "packages": [
    {
      "id": "my-theme",
      "name": "My theme",
      "version": "1.0.0",
      "description": "My custom JukaHub theme",
      "root": "/mnt/SDCARD/JukaHub/packages/my-theme",
      "dest_root": "/mnt/SDCARD",
      "files": [
        { "src": "theme.json", "dest": "/JukaHub/themes/my-theme.json", "sha256": "sha256:...", "keep_user": true },
        { "src": "tool.sh", "dest": "/Tools/tool.sh", "executable": true }
      ]
    }
  ]
}
```

- **Local packages**: `root` points at a directory on the device; files are
  copied from there.
- **Remote packages**: give `archive_url` (HTTPS, GitHub-only) and
  `archive_sha`; the archive is downloaded, verified and extracted into staging
  before install.
- Destinations default to the SD-card root (or the app directory on desktop)
  and can be overridden per file. Installing into `Roms/`, `BIOS`, `Saves`,
  `States`, `jukauser.json` etc. is **refused**.

### What Patch does

- **Install / Update / Remove** packages from the index, with a per-file action
  menu (A on a package row).
- **Edit packages.json** — opens the Patch state directory in the File
  Explorer.
- **JukaHub bundled assets** starter package (fonts, background, default
  config), to restore JukaHub's own files.
- **Check for updates** — downloads the release archive for this platform
  (`JukaHub-Windows.zip` / `JukaHub-Trimui.zip`) and stages it; one more press
  applies it and restarts. The same check runs automatically at launch, at
  most once a day, and applies without prompting on installed releases
  (`JUKAHUB_NO_AUTOUPDATE=1` disables that).
- **Verify & repair** — validate config, tools and assets; clear stale
  downloads.
- **Backups & Restore** — every install/update/remove is journaled, every
  replaced file is backed up, and an interrupted operation is rolled back
  automatically at next launch.
- **Export diagnostics** — redacted report.

### Signed repository (trust model)

Patch packages come from a JukaHub-maintained repository. The repository index
is **Ed25519-signed** and the verification public key is embedded in the
application (`patch_signed.go`). Every package archive is SHA-256-verified
against the signed index before install; HTTPS alone is never treated as
authenticity. The signing private key exists only in the offline repository
tooling:

```bash
PATCH_SIGN_KEY=<hex private key> go run ./tools/build-patch-repo --src ./patch-packages --out ./patch-repo
```

The tool assembles deterministic package archives (`packages/<name>.zip` with
`manifest.json` + `payload/`), computes digests and signs
`repository/index.json`. An example package (`jukahub-cache-clean`) ships in
`player/patch-packages/`.

### Package format

A package is a zip with a declarative `manifest.json` (schema 1) and a
`payload/` directory:

```json
{
  "schema": 1, "name": "jukahub-cache-clean", "version": "1.0.0",
  "architecture": "all", "devices": ["TG5040"], "os": ["trimui-stock"],
  "risk": "low", "restart": "none",
  "operations": [
    { "kind": "install", "src": "jukahub-cache-clean.sh", "dest": "/JukaHub/tools/jukahub-cache-clean.sh" },
    { "kind": "executable", "dest": "/JukaHub/tools/jukahub-cache-clean.sh" }
  ],
  "rollback": "automatic"
}
```

Supported operations: `install`, `replace` (baseline-hash gated), `remove`
(package-owned files only), `patch` (structured key/value), `executable`,
`chmod`. No arbitrary lifecycle scripts are executed; unknown operations and
unknown schema versions are rejected.

### Apt-like operations

- **Refresh** — download and verify the signed repository index (`apt-get
  update` equivalent; offline keeps the cached verified index).
- **Browse / Install / Upgrade / Remove** — with dependency resolution
  (`depends` / `conflicts` / `provides`), cycle detection, reverse-dependency
  checks, plan preview, digest verification and hold/unhold.
- **Verify** — hash every installed managed file and report damage.
- **History & Rollback** — transaction log with rollback to the last committed
  backup set (user edits made after the transaction are never silently
  overwritten).
- **Maintenance** — repair, cache clean, diagnostics.

Every install/upgrade/remove is a journaled transaction: backups first,
journal before mutation, rollback on failure or interruption (auto-recovered at
next launch). The local package database (`db/db.json`) is written atomically.

### Safety model

- Destructive steps are journaled **before** they happen; interruption rolls
  back automatically.
- Archives are inspected (no traversal, absolute paths, symlinks or devices).
- Checksums are enforced when declared; user-owned files (`keep_user`) are
  backed up before any change.
- No firmware flashing, no SD-card reformatting, no ROM/BIOS/save access.
  JukaHub never touches system partitions.
- Fully usable offline: local repair, backups, restore and diagnostics need no
  network.

### Releases

Every push to `main` runs `.github/workflows/docker-image.yml`, which creates a
`JukaHub-<timestamp>` release with two assets:

- `JukaHub-Windows.zip` — `JukaHub.exe`, the SDL DLLs, fonts/background and the
  config.
- `JukaHub-Trimui.zip` — the ARM64 binary, `launch.sh`, fonts/background and the
  config for every supported TrimUI model.

Both archives include `release-info.json` (`tag` + RFC3339 `date`). The app
reads it to show the release build date and to decide whether a newer release
exists; the build also embeds the date with
`-ldflags "-X main.buildDate=YYYY-MM-DD"`. Release tags are timestamps, so the
updater compares release times rather than semantic versions.

---

## Design system

Controller-first, dark, optimised for a 1280×720 logical canvas read at arm's
length.

### Home screen layout

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

### Scene shell

1. **Global header** (56 px) — brand, version and active scene on the left;
   network / weather / time / battery on the right, produced by one shared
   helper so it never shifts between screens.
2. **Scene body** — only the active scene, clipped above the footer.
3. **Contextual footer** (48 px) — hints that reflect real bindings.
4. Toasts and overlays render last, above the footer.

### Ambient background

Four soft colour blobs (cyan, purple, blue, soft white), tinted from the active
theme, orbit and breathe on multi-minute cycles at low alpha. Rendered from one
cached radial-glow texture with a handful of copies per frame — no per-frame
allocation, no blur shaders. Cards and text stay opaque on top.

### Colour tokens

| Token | Value |
|-------|-------|
| Background | `#090E19` |
| Surface | `#121A2A` |
| Card | `#172033` |
| Card focused | `#22314B` |
| Border | `#2A3953` |
| Focus border | `#55D6FF` |
| Text primary | `#F6F8FC` |
| Text secondary | `#9CA9BD` |
| Success / Warning / Error | `#57D68D` / `#FFCB66` / `#FF6B7A` |

### Typography

Bundled Inter font; `jukauser.json` can override sizes per role.

| Role | Size | Weight |
|------|-----:|--------|
| Brand | 25–27 px | Medium |
| Greeting | 30–34 px | Medium/Bold |
| Card title | 22–24 px | Medium |
| Card helper | 16–18 px | Regular |
| Status / footer | 15–17 px | Medium |

### Focus and icons

- Selected cards use a 3 px cyan border on a raised surface, unmistakable in
  grayscale and from viewing distance.
- Focus never changes card geometry; pressed feedback darkens the card and
  shifts content 1–2 px for ~100 ms.
- Icons are drawn with SDL geometry — never emoji or glyphs the font may lack.

### Themes

Seven built-in presets (Dark, Light, OLED, Midnight, Hub, Ocean, Sunset) can be
applied live from Settings → Themes and persist in `jukauser.json`. Each preset
defines background, surfaces, text, borders and accent colour.

---

## Controls reference

### Menus

| Button | Action |
|--------|--------|
| D-pad / left stick | Move |
| A | Open / confirm |
| B | Back |
| L1 / R1 | Previous / next section or page |
| X | Secondary action |
| Y | Tertiary action (save, ask AI in chat) |

### On-screen keyboard

| Button | Action |
|--------|--------|
| D-pad | Move between keys |
| A | Type the selected key |
| L1 | Shift |
| Y | Submit (send / save) |
| B, Esc | Close |

The keyboard also closes with its close button, by tapping outside the panel,
and on `Done` / `Send`.

### Video player

| Button | Action |
|--------|--------|
| A | Play / pause |
| B | Stop and go back |
| Left / Right | Seek |
| Up / Down | Volume |
| L1 / R1 | Playback speed ±0.25× |

### Desktop keyboard

| Key | Action |
|-----|--------|
| Arrows / Enter | Navigate / confirm |
| Esc | Back |
| Q / E | Previous / next scene |
| Ctrl+K | Jump to YouTube search |
| Ctrl+Q | Quick settings |
| F1 | Shortcut overlay |
| F2 | Global search |
| F12 | Screenshot |

### JukaLand

| Button | Action |
|--------|--------|
| D-pad | Move |
| A | Jump |
| B | Break block |
| X | Place block |
| Y | Enter / exit car |
| Start | Craft menu |
| Back / Select | Exit game |

---

## Security

JukaHub encrypts sensitive values (API keys, tokens) with AES-256-GCM. Set
`JUKAHUB_CRYPTO_KEY` (and optionally `JUKAHUB_CRYPTO_SALT`) before launching;
values look like `ENC:default-v3-enhanced/...` in the config. At startup the
player logs warnings for plaintext secrets, the built-in default key, Discord
user tokens and values that fail input validation.

Secrets are managed without a GUI, so a token can be prepared on a PC and
copied to the handheld:

```bash
cd player
./JukaHub -set-secret discord_token        # value read from stdin
./JukaHub -check-secrets                   # which values decrypt, and why not
```

The tool uses the same key derivation as the player. Decryption accepts values
written with the older, untrimmed default salt (same `default-v3-enhanced` key
ID), so configs from earlier builds keep working.

Never commit real credentials: keep them out of `jukaconfig.json` in version
control. Details: [../player/SECURITY.md](../player/SECURITY.md).

---

## Key files

| File | Purpose |
|------|---------|
| `player/main.go` | App entry point, SDL2 init, scene loop |
| `player/core.go` | Scene rendering, input routing, text helpers |
| `player/vkeyboard.go` | On-screen keyboard (layout, hit testing, rendering) |
| `player/crypto.go` | AES-256-GCM encryption and key management |
| `player/config_health.go` | Config validation and security checks |
| `player/controller.go` | Gamepad / keyboard / touch routing |
| `player/tools.go` | Tool paths, downloads, GitHub API helpers |
| `player/patch_engine.go` | Install / update / verify / rollback engine |
| `player/patch_signed.go` | Signed repository verification (Ed25519) |
| `build.bat` | Windows build wrapper (GCC check, SDL2 staging, launch) |
| `player/package-sdcard.sh` | TrimUI SD-card package |
| `player/release.sh` | Release artifacts and `manifest.json` |

## Contributing

Fork, branch, keep changes focused, then open a pull request. Run `go vet` and
`go test ./...` in `player/` before submitting. Rendering changes are expected
to include pixel or geometry tests; the existing tests in
`installbar_render_test.go`, `vkeyboard_test.go` and `vkeyboard_ui_test.go`
show the pattern (SDL dummy driver + software renderer).

## Roadmap

Ported from JukaHub V1: canvas sandbox, favorites tabs, unit converter, disk
space chart, ffplay/ffmpeg playback, image viewer, chat, YouTube Shorts, random
I/O benchmark, log viewer, theme presets, plugin system — all done.
