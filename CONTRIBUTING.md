# Contributing to JukaHub

Thanks for helping. This page covers the local loop: get it building, keep the
checks green, and know which files are safe to commit.

## Set up

```bash
cd player
./build.bat          # Windows: installs Go/GCC/SDL2 as needed, then runs
go build -o JukaHub . # Linux/macOS with SDL2 dev packages installed
```

Details, cross-builds and packaging are in [docs/BUILDING.md](docs/BUILDING.md).

## Checks to run before opening a pull request

```bash
cd player
gofmt -l .            # your files must not be listed
go build ./...
go vet ./...
go test ./...
```

`go vet` and `go test ./...` are the bar; CI additionally builds the ARM64
SD-card package for handheld changes.

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
| `player/*.go`, `docs/`, `CONTRIBUTING.md`, `README.md` | yes |
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

In the pull request, describe the user-visible change, how you verified it
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
