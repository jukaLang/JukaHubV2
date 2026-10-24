# Architecture

A map of the moving parts and how they talk to each other. This is the
orientation layer above [DEVELOPMENT.md](DEVELOPMENT.md), which explains how to
build and verify; [SECURITY.md](../SECURITY.md) covers the trust boundaries.

## The three runtimes

| Part | Language | What it is |
|------|----------|------------|
| `player/` | Go + SDL2 (cgo, vendored deps) | the shipped app: launcher, menus, music, video, browser, games |
| `index.html` + `script.js` + `style.css` | plain web | the website **gallery builder** that authors `jukaconfig.json`s |
| `player/tools/build-patch-repo/` | Go | the **package manager** backend: builds and signs installable patch packs |

## The player

Everything the device runs is a single Go program in `player/`. Dependencies
are vendored on purpose ([`player/go.mod`](../player/go.mod)) so builds are
offline and reproducible; `player/jukaconfig.json` is the config data the
runtime loads, and everything user-configurable flows through the schema in
[`player/config.go`](../player/config.go) with the compatibility rules
described in [DEVELOPMENT.md](DEVELOPMENT.md#config-schema-and-compatibility).

Startup and render cadence are not left to chance:
[`player/adaptive.go`](../player/adaptive.go) pins the frame-rate tiers
(30 fps cap on the handheld, its active tier) and
[`player/startup.go`](../player/startup.go) owns the cold-boot path with its
warm-marker logic. Both budgets are measured headlessly by the benchmarks in
[`player/render_bench_test.go`](../player/render_bench_test.go) and
`player/startup-bench.sh`, with numbers recorded in
[`player/MODERNIZATION.md`](../player/MODERNIZATION.md) —
treat those as the reference before touching boot or layout code.

## Scenes, focus and background work

Screens are **scenes**: each scene owns its rendering and input through the
`sceneModule` interface and registers itself in its own `init` — the registry
lives in [`player/scene.go`](../player/scene.go), and the dispatchers in
`core.go` / `main.go` simply walk it. Focus is graph-based:
[`player/focus.go`](../player/focus.go) builds the D-pad graph per scene from
measured element rects, and Home draws its own through `homefocus.go` because
its layout is custom. The status bar is split by concern —
[`player/statusbar_status.go`](../player/statusbar_status.go) for the centred
cluster and [`player/statusbar_media.go`](../player/statusbar_media.go) for
media chips — so a change to one does not fight the other's layout.

Scene-scoped background work goes through
[`player/scenetasks.go`](../player/scenetasks.go): a scene transition cancels
the previous generation, and its rules (pass `ctx` into requests, check
`ctx.Err()` before publishing) are the same contract
[DEVELOPMENT.md](DEVELOPMENT.md#scene-scoped-background-work) specifies for
your own screens. Process-wide oxygen — network monitor, weather ticker,
audio output — keeps its own goroutines instead.

## The website gallery builder

`index.html`, `script.js` and `style.css` at the repository root, plus
[`gallery/layouts.js`](../gallery/layouts.js) for the preset layouts. No
framework, no bundler, no build step; `style.css` keys off the `sdl.img-*`
classes the Go scene renderer and the preview agree on. Tests are
[`tests/web/builder.test.mjs`](../tests/web/builder.test.mjs) plus Playwright
specs in `tests/e2e/`.

Constraint worth remembering: the builder's single-page static output must
only depend on the `jukaconfig.json` it writes. Anything that needs
device state (features fetched, cached artwork) belongs to the player, not
the builder.

## The package manager

Devices install content through signed **patches** (a `.juka` file from the
repo or an SD-card bundle):

- `tools/build-patch-repo/` builds, hashes and signs (PATCH_SIGN_KEY);
  the Ed25519 public key is embedded in the app,
  [`player/patch_signed.go`](../player/patch_signed.go), and the private
  half stays off-repo
- [`player/patch_engine.go`](../player/patch_engine.go) is the main engine;
  `patch_flow.go` walks stages and `patch_resolve.go` resolves patterns
- [`player/installbar.go`](../player/installbar.go) renders the install card;
  `sdhealth.go` gates the SD card state before any stage starts

The security model, from signature to `statfs` install validation, is
documented in [SECURITY.md](../SECURITY.md).

## Where changes usually land

Most work touches exactly one runtime; use the table above to tell which. If
you find yourself duplicating layout logic between a scene and the generic
renderer, or measuring your own frame budgets instead of reusing the
benchmarks in `player/render_bench_test.go`, that's a sign to read the
relevant section of [DEVELOPMENT.md](DEVELOPMENT.md) first — the player is
careful about both on purpose, given the 30 fps handheld class.
