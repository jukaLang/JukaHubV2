# Contributing to JukaHub

Thanks for helping. This page covers the local loop: get it building, keep the
checks green, and know which files are safe to commit.

## Set up

```bash
make dev    # build and launch the player
make        # list every target
```

A complete five-minute walkthrough is in [docs/QUICK_START.md](docs/QUICK_START.md).

On Windows that bootstraps Go, GCC and SDL2 through `player\build.bat` (the
first build compiles the SDL2 cgo bindings and takes a few minutes). On
Linux/macOS install the SDL2 dev packages first. Both paths, cross-builds and
packaging are in [docs/BUILDING.md](docs/BUILDING.md); if you do not have make,
the raw commands are `player\build.bat` on Windows and `cd player && go build -o
JukaHub .` elsewhere.

## First task

1. Start with `player/MODERNIZATION.md`; it is the player and builder backlog.
2. Pick a task you can finish and verify; read its `[HW]`, `[NET]` and `[OWNER]` tags.
3. Read the listed source files and docs before changing anything.
4. Check `git status` so you preserve work already in the checkout.
5. Make one focused change and avoid dependency edits unless the task needs them.
6. Build and run the player with `make dev`.
7. Run `make check` and address failures caused by your change.
8. Without a device, use the host checks and leave hardware-only acceptance open.
9. When all acceptance steps pass, remove the task and update `CHANGELOG.md`.
10. Review the diff and add any follow-up task with a reason and acceptance steps.

## Checks to run before opening a pull request

```bash
make check   # gofmt, vet, builder syntax, config JSON, workflow pins, player + jsdom tests
```

Or run the pieces yourself, from `player/`:

```bash
gofmt -l $(find . -path './vendor' -prune -o -name '*.go' -print)   # your files must not be listed
go build ./...
go vet ./...
go test ./...
```

CI on every push and pull request is two jobs (`.github/workflows/ci.yml`): the
web builder checks, and the player's `go vet`, staticcheck and `go test`. The
slow checks run once a week and by hand (`.github/workflows/weekly.yml`): the
race detector, the Go vulnerability scan, the cold-boot budget, the Chromium
smoke test, the CI helper scripts' and the game-audio plugin's tests and the
native Windows build and tests. Keep it that way: a new check goes into
`weekly.yml` or a `make` target, not into a new per-push job.

For a startup, render-loop or dependency change, also run the cold-boot
benchmark. It builds once, runs headless three times and fails on a median over
the limit; the weekly checks run the same script.

```bash
cd player
JUKAHUB_BENCH_RUNS=3 JUKAHUB_BENCH_LIMIT_MS=5000 ./startup-bench.sh
```

Coverage is advisory, not a gate, and CI does not measure it; the total plus
the functions at 0% are one command locally:

```bash
make coverage      # total plus the worst-covered functions
```

Code conventions (naming, comments, config keys, rendering budget and the test
shape a change is expected to have) are in [docs/STYLE.md](docs/STYLE.md).

Decisions that other code has to live with — a new indirection everything goes
through, an option being removed, a hardware budget being spent — are recorded
in [docs/adr/](docs/adr/), starting from
[0000-record-architecture-decisions.md](docs/adr/0000-record-architecture-decisions.md).
Bug fixes and refactors that keep the existing decisions do not need a record.

For a builder change, also run the web checks from the repository root:

```bash
node --check script.js && node --check builder-device.js \
  && node --check builder-gallery.js && node --check gallery/layouts.js
(cd tests/web && npm ci && npm test)      # jsdom: import, share, security
(cd tests/e2e && npm ci && npx playwright test)   # Chromium: add, undo/redo, round trip
```

The first Playwright run needs `npx playwright install chromium`. CI runs the
syntax checks and the jsdom tests on every push and the Chromium smoke test once
a week; a builder change is still not finished until the Chromium smoke test
passes, because it is the only check that drives the real drop handler, history
buttons and import path.

> **Renderer-owned caches.** A texture belongs to the renderer that created it.
> The static-layer caches in `player/vkeyboard.go` (keyboard, chat and video
> overlay) record their owning renderer and drop a foreign texture instead of
> touching it; a cache that outlived its renderer once made the app draw or free
> a dangling pointer. Follow the same rule for any new texture cache

## Error handling conventions

The codebase has settled on a few habits; match them when you add or touch
error paths:

- **Wrap with `%w` and say what the step was doing.** Every `fmt.Errorf` in
  the player wraps with `%w` and names the operation or file, so a failure
  reads as a path from cause to call site:
  `fmt.Errorf("download %s: write failed: %w", url, err)`. Breaking the wrap
  (`%v`) makes `errors.Is`/`errors.As` checks upstream impossible.
- **Best-effort errors are logged, not returned.** Housekeeping that can fail
  without changing app behaviour (cache drops, auxiliary saves, cleanup) logs
  through `log/slog` with the operation fields (`LogScene`, `LogOp`) instead of
  inventing new error plumbing. A silent `_ =` discard is only acceptable when
  the value genuinely cannot matter — that precondition is worth a comment.
- **User-facing text is not error text.** Internal errors keep their
  technical wording (`os.PathError` and friends); what the user sees comes out
  of the failure-message mapping in the UI layer (see
  [`player/ui_helpers.go`](player/ui_helpers.go)). Don't paste a wrapped error
  into a toast unmodified.
- **Cancelled work publishes nothing.** Anything running under a scene or
  playback context is already wired so that `ctx.Err() != nil` means "return,
  do not publish" — see the scene-task rules in
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#scene-scoped-background-work).
- **Nothing crashes on a malformed input.** Malformed configs, packs and
  manifests degrade (skip the element, log, keep the app up); the schema and
  loader tests pin behaviour for corrupt data. Reserve `panic` for
  programmer-error invariants caught in tests.

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

The Go suite is deliberately small and fast: it guards what would hurt users
silently (saved data, config migration, security checks, the updater and
package installer, parsers of files from outside) and nothing else. `go test`
should finish in seconds, so it gets run.

| Change | Expected |
|--------|----------|
| Persistence, migration, security, installer or updater logic | a table-driven unit test |
| A parser of files or data from outside (archives, playlists, manifests, binaries) | a unit test |
| Bug fix in one of the above | a test that fails before your fix |
| A feature's flow or state, rendering, layout, copy, colours, focus geometry, timing, the maths behind an effect or an estimate | no test — check it on screen (Windows build, then the device) |

Owner rule (2026-11-10): the suite grew from 90 to 180 files in two days,
almost all of it feature tests, and was cut back to the rows above. Add a case
to an existing test file before starting a new one, and start a new one only
for a row above.

Tests never open an SDL window, renderer or font, never sleep for more than a
few milliseconds, never touch the network beyond an `httptest` server, and never
write into the project directory: use `t.TempDir()` and `t.Chdir()` for anything
that saves a file. Shared helpers live in
[`player/testhelpers_test.go`](player/testhelpers_test.go). A test may skip only
when the machine cannot run it (no POSIX `sh`, no symlink privilege, no
listener), never because its own fixture failed.

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
│   ├── *_test.go                      # the lean unit-test core (data, security, updates)
│   ├── jukaconfig.json                # committed design config
│   ├── trimui/                        # device package assets and notes
│   └── build.bat, release.sh, package-sdcard.sh
├── docs/                              # user and developer documentation
└── .github/workflows/                 # CI (two jobs per push), weekly checks, release, SD-card package (by hand)
```

Every workflow action is pinned to a full commit SHA with the release it matches in a
trailing comment (`uses: actions/checkout@3d3c42e5... # v7.0.1`). Tags are mutable and
a re-pointed one would change what CI runs without a commit here, which is what
Scorecard's `Pinned-Dependencies` check reports. Dependabot's `github-actions` entry
bumps both the SHA and the comment, so an update arrives as a normal PR; pin any new
action the same way rather than by tag.

A download is held to the same rule: CI fails when a workflow fetches an archive
with `curl`/`wget` and does not verify it, so a new fetch needs a `sha256sum -c`
check on or within the next two lines (or an explicit `# download-pin-ok:
<reason>` when the source publishes no digest). The three SDL tarballs both
Windows builds need are listed once, digests included, in
[`.github/scripts/install-sdl-mingw.sh`](.github/scripts/install-sdl-mingw.sh).
The fetches that cannot carry a digest are the release jobs' Go build caches
(Windows and TrimUI) and the reused Remote Play helpers, because each release
run produces them; instead
[`.github/scripts/go-build-cache.sh`](.github/scripts/go-build-cache.sh) accepts
them only from an earlier push-to-main run of the same workflow whose commit
is on `main`, never from `actions/cache`, which any workflow on `main` can
write. A container image is pinned by digest wherever a workflow names it:
under `container: image:`, or in an env key ending in `_IMAGE` for an image a
step starts itself (the TrimUI compile's `TOOLCHAIN_IMAGE`).

All three pinning rules live in
[`.github/scripts/check-workflows.sh`](.github/scripts/check-workflows.sh), and
`make check` runs that same script, so a workflow edit that breaks one fails
before the push rather than after it. The script also checks that every job
calling a workflow of this repository (`uses: ./.github/workflows/...`) grants
each permission that workflow's jobs ask for: GitHub does not downgrade such a
request, it refuses the whole calling run before it starts.

## Which files to commit

| File | Commit? |
|------|---------|
| `player/*.go`, `docs/`, `CONTRIBUTING.md`, `README.md`, `CHANGELOG.md` | yes |
| `player/jukaconfig.json` — the design config | yes (it is tracked, and the app's default UI) |
| `player/trimui/jukaconfig.device.json` | yes — regenerate it when the full config changes (see [`player/trimui/README.md`](player/trimui/README.md)) |
| `player/jukauser.json` | **never** — your personal settings; git-ignored |
| `player/errors.txt`, `logs.txt`, `jukaalarm.json`, `safe-mode.flag` | **never** — runtime files |
| API keys, Discord tokens, `JUKAHUB_CRYPTO_KEY` values | **never** — see [`SECURITY.md`](SECURITY.md) |

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
| Component map of the three runtimes | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Code conventions and test expectations | [docs/STYLE.md](docs/STYLE.md) |
| Checkpoints / quick-switch design (not implemented) | [docs/CHECKPOINTS.md](docs/CHECKPOINTS.md) |

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
  [`SECURITY.md`](SECURITY.md).
- Anything that executes a program — external apps, plugins, packages, the
  terminal — must stay explicit, path-confined and journaled. Do not wire in
  a silent auto-run path.

## License

JukaHub is AGPL-3.0 (see [LICENSE](LICENSE)). By contributing you agree your
changes are released under the same license.
