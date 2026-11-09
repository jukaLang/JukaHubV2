# Code style

This is the short version of the habits the player code already follows. It is
not a new standard to adopt: it describes what a review will look for, so a
change can match the file next to it instead of inventing a second convention.
The build and test loop lives in [CONTRIBUTING.md](../CONTRIBUTING.md).

## Go

- **Standard library first.** The player vendors SDL2 and uses `log/slog`,
  `net/http`, `encoding/json` and friends directly. A dependency needs to solve
  a problem the standard library cannot, on a 1 GB device with one core.
- **Errors: wrap with `%w` and name the step.** `fmt.Errorf("download %s: write
  failed: %w", url, err)`. Best-effort housekeeping logs through `LogScene` and
  is not returned. User-facing text is mapped in the UI layer
  ([player/ui_helpers.go](../player/ui_helpers.go)), never a raw wrapped error.
  See the full conventions in [CONTRIBUTING.md](../CONTRIBUTING.md#error-handling-conventions).
- **Name for the reader, not the type.** `netplayPanel`, `ledFrameColor`,
  `a11yResolveButton` say what they own and what they return. Avoid `util.go`,
  `helpers.go` and `Manager` suffixes; the scene registry already names the
  entry points ([player/scene.go](../player/scene.go)).
- **Comments explain why.** The codebase's comments record the failure a change
  prevents ("Destroying it touches a pointer SDL already freed with its
  renderer"), not a restatement of the syntax. A non-obvious constant gets the
  measurement or device fact behind it.
- **One scene per file**, registered from its `init` through `sceneModule`. Do
  not add branches to a dispatch table for a new scene; the registry is the only
  table ([docs/DEVELOPMENT.md](DEVELOPMENT.md)).
- **One concern per file** is the norm elsewhere too: `ledsync.go` imports no
  SDL so its maths is testable without a renderer, and `componentreport.go`
  returns data that both the Patch card and the diagnostics export render.
- **Config keys are data, not code.** A setting lives in `Variables` or the
  `Custom` map, is written through the same save path as its neighbours
  (`a11yStore`, `qsPersist`, `setNetplayEnabled`) and is readable by hand. When
  a key needs three states (unset / on / off), say so in the comment and use a
  presence check rather than treating the zero value as the answer.
- **Defaults are stated once.** A shipped default belongs in
  [player/jukaconfig.json](../player/jukaconfig.json); a per-user override
  belongs in the user config. Never re-derive a default in two places.

## Rendering and the device budget

- The target is a 1280×720 handheld with about 1 GB of RAM and one slow core.
  No per-frame allocations, no blocking calls on the render thread, no
  unbounded caches.
- Textures belong to the renderer that created them. Drop a renderer-owned
  cache before destroying the renderer (the `*Owner` fields in
  [player/vkeyboard.go](../player/vkeyboard.go) enforce it at runtime).
- New panels follow an existing panel's layout (`toolsui.go`, `a11y.go`,
  `netplay_ui.go`) instead of inventing a card: same header, same row height,
  same footer hint line. A screen that looks bolted on is a review finding.
- **Every screen draws with Home's glass.** Surfaces, focus and chrome come from
  [player/ui_glass.go](../player/ui_glass.go) (`glassA` and the named glass
  tokens, `drawAppBackdrop`, `drawGlassSurface`, `drawGlassButton`,
  `drawGlassSheet`, the footer band and hint badges) or the shared components
  built on it in [player/components.go](../player/components.go) (`UIListRow`,
  `UIButton`, `drawOverlayScrim`/`drawOverlayCard`, `UIPageHeaderDraw`,
  `UICategoryTileDraw`). Do not hard-code a slate fill or a white text colour:
  the glass ink and the text inks follow the theme, which is what keeps a screen
  readable on the light presets. Focus is Home's 3px accent ring
  (`glassAccent()`); there are no drop shadows.
- **The logo is one picture.** Every copy of it is rendered by
  [branding/mklogo.py](../branding/mklogo.py) (see
  [branding/README.md](../branding/README.md)). On screen, draw it with
  `drawBrandLogo` ([player/brandlogo.go](../player/brandlogo.go)), which
  resamples the artwork the binary already embeds to the size it is shown at;
  do not rebuild it from primitives or ship a second copy.
- Keyboard, controller, mouse and touch all reach the same action. If a row can
  be activated with A, it needs a click target and a key binding too.

## Tests

- Table-driven unit tests for logic, parsing and safety checks; offscreen pixel
  or geometry assertions for layout; a regression test for a bug fix that fails
  before the fix.
- Tests use `t.TempDir()` / `t.Chdir()` and never write into the project
  directory. Global state a test mutates is restored in `t.Cleanup`.
- Prefer a test that pins the contract over one that pins a number that will
  drift: "the cache is not shared across renderers" outlives "builds == 3".
- There are no rendering tests: the suite stays fast and guards data,
  security and updates. Check visual changes on screen, on Windows first and
  then on the device.

## Documentation

- Describe what the code does and link the implementation. Documented gaps are
  useful ([the engineering backlog](../player/MODERNIZATION.md)); aspirational docs are
  worse than none.
- A user-visible change gets a [CHANGELOG.md](../CHANGELOG.md) entry under
  `Unreleased`, and a config or control change updates
  [docs/CONFIGURATION.md](CONFIGURATION.md) or [docs/CONTROLS.md](CONTROLS.md).
