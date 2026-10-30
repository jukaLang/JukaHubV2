# Quick start for developers

Build it, run it, change it, ship it — the five-minute path. This page stays
short on purpose; every step links to the detailed page that owns the topic.

## 1. Prerequisites

Everything is in the repo's [Makefile](../Makefile); on Windows it bootstraps
the toolchain itself.

| You need | Where it comes from |
|----------|---------------------|
| Go 1.27+ | [go.dev/dl](https://go.dev/dl/) (Windows: installed automatically) |
| A C compiler + SDL2 headers/libraries | Linux/macOS: `sudo apt install libsdl2-dev libsdl2-image-dev libsdl2-ttf-dev && gcc`; Windows: downloaded automatically into `player/.sdl2/` |
| Node.js 22+ | only for the web builder checks |

Go modules are vendored, so a build needs no network access.

## 2. Build and run

```bash
git clone https://github.com/jukaLang/JukaHubV2.git
cd JukaHubV2
make dev          # build and launch the player
```

- The first build compiles the SDL2 cgo bindings and takes a few minutes.
- `make` on its own lists every target.
- No make? `cd player && ../player/build.bat` on Windows, or
  `cd player && go build -o JukaHub .` elsewhere.
- No setup at all? Open the repo in VS Code or GitHub Codespaces — the
  [dev container](../.devcontainer/devcontainer.json) has Go, Node and the
  SDL2 dev packages installed; [mise.toml](../mise.toml) holds the same pins
  for people who use mise instead.

Detailed, platform-specific instructions (ARM64 cross-builds, packaging, the
release pipeline) live in [BUILDING.md](BUILDING.md).

## 3. Make a change

- The player is a config-driven Go + SDL2 app in `player/`: one scene per
  file, registered through the `sceneModule` interface in
  [`player/scene.go`](../player/scene.go). Add your file and its `init`, not a
  dispatch branch.
- The web builder is the plain-JS editor at the repository root
  (`index.html`, `style.css`, `script.js`) that produces `jukaconfig.json`.

Where a change normally lands is mapped in
[ARCHITECTURE.md](ARCHITECTURE.md); the code-structure rules are in
[CONTRIBUTING.md](../CONTRIBUTING.md#what-a-good-change-looks-like).

## 4. Check it

```bash
make check        # gofmt, go vet, builder syntax + ESLint, config JSON, player + jsdom tests
```

For a builder change, CI also runs the Chromium smoke test:

```bash
cd tests/e2e
npm ci && npx playwright install chromium && npx playwright test
```

One test (`TestVideoFullscreenHidesControlBar`) is known to crash
intermittently under the native SDL renderer; if it crashes on you, re-run
once before digging — see [CONTRIBUTING.md](../CONTRIBUTING.md#set-up).

## 5. Open a pull request

The [pull request template](../.github/PULL_REQUEST_TEMPLATE.md) walks you
through it: describe what the user sees, how you verified it, and add a
`CHANGELOG.md` entry under `Unreleased` for anything user-visible.

## Where everything else lives

| Task | Page |
|------|------|
| Full setup, checks, what to commit | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| Builds, cross-compile, releases | [BUILDING.md](BUILDING.md) |
| Architecture and the design system | [ARCHITECTURE.md](ARCHITECTURE.md), [DEVELOPMENT.md](DEVELOPMENT.md) |
| Config files, variables, elements, triggers | [CONFIGURATION.md](CONFIGURATION.md) |
| Buttons and keys on every screen | [CONTROLS.md](CONTROLS.md) |
| Packages and the Patch manager | [PACKAGES.md](PACKAGES.md) |
| Security model, reporting a vulnerability | [SECURITY.md](../SECURITY.md) |
| Current progress and improvement priorities | [PROJECT_STATUS.md](PROJECT_STATUS.md) |
