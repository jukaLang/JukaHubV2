# Contributing to JukaHub

Thanks for helping. This page covers the local loop: get it building, keep the
checks green, and know which files are safe to commit.

## Set up

```bash
make dev    # build and launch the player
make        # list every target
```

On Windows that bootstraps Go, GCC and SDL2 through `player\build.bat` (the
first build compiles the SDL2 cgo bindings and takes a few minutes). On
Linux/macOS install the SDL2 dev packages first. Both paths, cross-builds and
packaging are in [docs/BUILDING.md](docs/BUILDING.md); if you do not have make,
the raw commands are `player\build.bat` on Windows and `cd player && go build -o
JukaHub .` elsewhere.

## Checks to run before opening a pull request

```bash
make check   # gofmt, go vet, builder syntax, config JSON, player + jsdom tests
```

Or run the pieces yourself, from `player/`:

```bash
gofmt -l $(find . -path './vendor' -prune -o -name '*.go' -print)   # your files must not be listed
go build ./...
go vet ./...
go test ./...
```

For a startup, render-loop or dependency change, also run the cold-boot
benchmark. It builds once, runs headless three times and fails on a median over
the limit; CI uses the same script as a blocking gate.

```bash
cd player
JUKAHUB_BENCH_RUNS=3 JUKAHUB_BENCH_LIMIT_MS=5000 ./startup-bench.sh
```

For a builder change, also run the web checks from the repository root:

```bash
node --check script.js && node --check builder-device.js \
  && node --check builder-gallery.js && node --check gallery/layouts.js
(cd tests/web && npm ci && npm test)      # jsdom: import, share, security
(cd tests/e2e && npm ci && npx playwright test)   # Chromium: add, undo/redo, round trip
```

The first Playwright run needs `npx playwright install chromium`. CI runs the
same four checks plus `go vet`/`go test` for the player; a builder change is not
finished until the Chromium smoke test passes, because it is the only check that
drives the real drop handler, history buttons and import path.

> **One known flaky test.** `TestVideoFullscreenHidesControlBar` crashes
> intermittently in `SDL_RenderReadPixels` (a native SDL assertion while the
> ffmpeg decoder thread holds the renderer). It reproduces in roughly 1 of 6
> isolated runs and is unrelated to your change — re-run before digging.
> `TestDecryptWithAuthRoundTrip` and `TestKeyboardCachedFrameIsCheaperThanFullRedraw`
> have each been seen to fail once without a clear cause; re-run those too.
> Fixing the flake is welcome.

## What a good change looks like

- **Fix the cause.** No skipping tests, weakening assertions, swallowing errors
  or adding a lint/type suppression to make a check pass. If a check has to
  change because the behaviour changed, say so in the pull request and verify
  the new behaviour.
- **Keep it focused.** One concern per pull request; unrelated formatting or
  refactors make review harder.
- **Comment the why.** The codebase explains reasoning in comments (especially
  where a naive fix would be wrong); match that tone. Explain the decision, not
  the syntax.
- **Respect the device.** The target is a 1280×720 handheld with ~1 GB of RAM
  and one slow core. Avoid per-frame allocations, blocking calls on the render
  thread and unbounded caches.
- **One scene per file.** A scene's renderer, key handler and controller mapping
  belong in one file, registered through the `sceneModule` interface in
  [`player/scene.go`](player/scene.go) from its `init`. Do not add branches to
  the dispatch tables — the registry is the only table.

### Tests for what you changed

| Change | Expected |
|--------|----------|
| Logic, parsing, config, safety checks | table-driven unit tests |
| Rendering, layout, focus | pixel or geometry tests |
| Bug fix | a test that fails before your fix |

Rendering tests run headless: `SDL_VIDEODRIVER=dummy` with the software
renderer, then read the pixels back and assert on them. See
[`player/installbar_render_test.go`](player/installbar_render_test.go),
[`player/widgets_render_test.go`](player/widgets_render_test.go) and
[`player/statusbar_render_test.go`](player/statusbar_render_test.go) for the
pattern, and
[`player/widgets_test.go`](player/widgets_test.go) for focus-graph and
config-injection tests. Tests must not write into the project directory; use
`t.TempDir()` and `t.Chdir()` for anything that saves a config.

## Repository layout

```text
JukaHubV2/
├── index.html, style.css, script.js   # web builder UI
├── player/                            # the Go application
│   ├── main.go                        # entry point, event loop
│   ├── core.go                        # scene rendering, input, triggers
│   ├── controller.go                  # gamepad / keyboard / touch routing
│   ├── focus.go                       # focus graph and spatial navigation
│   ├── design.go, themes.go           # design tokens, theme presets
│   ├── vkeyboard.go                   # on-screen keyboard
│   ├── patch_*.go, packages.go        # package manager
│   ├── plugins.go                     # plugin loader and runner
│   ├── *_test.go                      # unit, render and focus tests
│   ├── jukaconfig.json                # committed design config
│   ├── trimui/                        # device package assets and notes
│   └── build.bat, release.sh, package-sdcard.sh
├── docs/                              # user and developer documentation
└── .github/workflows/                 # CI: SD-card package, release, tags
```

## Which files to commit

| File | Commit? |
|------|---------|
| `player/*.go`, `docs/`, `CONTRIBUTING.md`, `README.md`, `CHANGELOG.md` | yes |
| `player/jukaconfig.json` — the design config | yes (it is tracked, and the app's default UI) |
| `player/trimui/jukaconfig.device.json` | yes — regenerate it when the full config changes (see [`player/trimui/README-ID.md`](player/trimui/README-ID.md)) |
| `player/jukauser.json` | **never** — your personal settings; git-ignored |
| `player/errors.txt`, `logs.txt`, `jukaalarm.json`, `safe-mode.flag` | **never** — runtime files |
| API keys, Discord tokens, `JUKAHUB_CRYPTO_KEY` values | **never** — see [`player/SECURITY.md`](player/SECURITY.md) |

Running the test suite can rewrite `player/jukauser.json.bak` (which is,
unfortunately, tracked). If it shows up as modified and you did not mean to
change it, restore it:

```bash
git checkout -- player/jukauser.json.bak
```

## Documentation

User-facing docs live in [`docs/`](docs) and are linked from the
[README](README.md); keep the README slim and put detail in the matching page:

| Topic | Page |
|-------|------|
| Config files, variables, elements, triggers | [docs/CONFIGURATION.md](docs/CONFIGURATION.md) |
| Buttons and keys on every screen | [docs/CONTROLS.md](docs/CONTROLS.md) |
| Building, packaging, release artifacts, CI | [docs/BUILDING.md](docs/BUILDING.md) |
| Packages and the Patch package manager | [docs/PACKAGES.md](docs/PACKAGES.md) |
| Plugin manifest and execution contract | [docs/PLUGINS.md](docs/PLUGINS.md) |
| Architecture, design system, key files | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) |

Two rules for documentation changes:

1. **Describe what the code does.** If you document a trigger, a flag or a
   default, point at the implementation that provides it. Aspirational docs are
   worse than no docs.
2. **Say when something is not implemented.** Documented gaps (see the
   "Not implemented yet" sections) are more useful than silence.

Commands, env vars and file paths in the docs are expected to be real; if you
rename something, update the pages that mention it.

## Commit and pull request

History in this repository is mixed in style, so anything clear works. A short
imperative subject naming the area is ideal:

```text
Fix the device config being excluded by .gitignore
Add Clean Cache action to the storage screen
```

The pull request template asks for the same thing in checklist form, including
a `CHANGELOG.md` entry under `Unreleased` for anything a user can see. In the
pull request, describe the user-visible change, how you verified it
(commands, rendered screenshots, on-device checks) and anything you could not
verify. Say explicitly which checks failed, if any — "the suite's video
fullscreen test crashed as usual" is useful information, not a failure.

## Security

- Never commit credentials. Configs that can hold secrets are git-ignored for
  exactly this reason.
- Report a vulnerability privately rather than in a public issue; see
  [`player/SECURITY.md`](player/SECURITY.md).
- Anything that executes a program — external apps, plugins, packages, the
  terminal — must stay explicit, path-confined and journaled. Do not wire in
  a silent auto-run path.

## License

JukaHub is AGPL-3.0 (see [LICENSE](LICENSE)). By contributing you agree your
changes are released under the same license.
