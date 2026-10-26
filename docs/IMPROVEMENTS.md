# Where JukaHub can get better

I read [BrickOS's improvements page](https://github.com/hugorosario/BrickOS/blob/main/docs/improvements.md)
(a TrimUI Brick frontend reimplementation) and mapped every item onto this
codebase. BrickOS is a firmware-level frontend on the same class of hardware, so
its list is a good stress test of the parts we share: an updater, text input, a
controller-driven UI, SDL rendering and a Wi-Fi stack.

This page records what the comparison found, where JukaHub is already ahead, and
the ideas from it that have **not** been adopted. Adopted work leaves this page:
shipped items are inventoried in
[MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25)
and the plan's own phases, and project-side items are in [ROADMAP.md](../ROADMAP.md).

## Fixed in this pass

| Problem | BrickOS | JukaHub now |
|---|---|---|
| Backspace removed one *byte*, so deleting from `café`, `日本語` or an emoji left a broken glyph and the next press looked dead | "UTF-8 editing: removes a complete UTF-8 code point" | `trimLastRune` in [core.go](../player/core.go) is used by all 11 text-buffer handlers; `TestNoByteSlicedTextEdits` in [text_edit_test.go](../player/text_edit_test.go) fails on any new `x = x[:len(x)-1]` |
| A failed automatic update check was not retried until the next launch — and the handheld often starts before Wi-Fi has associated | "retries a failed automatic check after a minute" | `runAutoUpdateAttempts` in [autoupdate.go](../player/autoupdate.go): two attempts, 60 s apart, never retrying the Patch lock |
| The update modal named a version and nothing else | "a short changelog of the available update (What's new)" | `updatePromptHighlights` shows up to three cleaned release-note bullets in the modal |
| The update modal passed `\n` straight to the renderer: the body drew as one line that ran off the card and across the screen | — | `wrapTextLines` in [core.go](../player/core.go), verified by [updateprompt_render_test.go](../player/updateprompt_render_test.go) |
| Patch confirmations cut the body after two rune-split lines, so an install or an error was explained only halfway | "long Settings confirmations now show their whole text instead of ending with '...'" | `layoutPatchModal` in [patch.go](../player/patch.go) grows the card to the wrapped body and only ellipsizes what the screen cannot hold |
| The notification history ellipsized every message to one line and then drew the timestamp on top of the clipped text — the long errors were the least readable rows | "notification history keeps the whole message" | `notifMeasureRows` and `notifWrapLines` in [notifications.go](../player/notifications.go): per-row measured height (up to two wrapped lines plus the timestamp), a path with no spaces split at `/` instead of truncated, and the cursor limit plus click selection taken from the rows the frame actually drew |

Run the checks with the usual command list in [CONTRIBUTING.md](../CONTRIBUTING.md);
`JUKA_PREVIEW=1 go test -run 'TestUpdatePromptPreview|TestPatchModalRendersLongBody' ./`
writes `tmp/update-prompt.png` and `tmp/patch-confirm.png` for review.

## Still open from this comparison

Seven items were ranked here. Six of them are settled, so the list is short:

1. **Boot visuals.** [splash.go](../player/splash.go) and
   [wallpaper.go](../player/wallpaper.go) each pick one image. BrickOS browses
   presets with previews, marks the applied one and offers independent Random
   modes — the same shape we already use for wallpapers.

The rest left for the plan, where each is tracked once: component versions and
LED policies in
[MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25),
the physical → logical keymap in §5.4 with
[ADR 0001](adr/0001-physical-logical-keymap.md), the texture-thread audit as a
finding in Phase 3 (there was no worker-thread texture to move), Wi-Fi
verification on both stock images and end-to-end LAN play in Phase 8, and the
checkpoint milestone in [CHECKPOINTS.md](CHECKPOINTS.md).

## Where JukaHub is ahead

- **Everything is data.** Scenes, triggers, bounds and layouts live in
  `jukaconfig.json` with a browser-based editor; BrickOS is only now adding
  JSON-defined settings rows, inside compiled sections.
- **The updater is a package manager.** Signed repository, staged installs,
  digest *and* signature verification, a rollback copy and a swap script, versus
  "download the release and install at boot".
- **Honest test coverage of the UI itself.** Scene audits, offscreen render
  probes and PNG dumps catch layout drift (`JUKAHUB_SCENE_DUMP_DIR`), which is
  how the three bugs above were found; the audit culture is the real advantage.
- **Breadth:** watch party, PS4/PS5 streaming, video player with subtitles, music
  with lyrics, FTP/SFTP servers, plugins, a11y panel, opt-in telemetry.

## Deliberately not copying

- **Firmware boot logo / loading screens.** We ship as an app on top of the
  firmware; writing the vendor's boot partition is out of scope and unsafe.
- **Kernel, suspend and radio policy.** BrickOS owns the firmware services; we
  inherit them, so their suspend and power fixes do not translate.
- **The 12-hour clock fix.** We render 24-hour time everywhere, so there is no
  AM/PM bug to fix. Adding a 12-hour option is a feature, not a correction.

## Web builder pass (October 2026)

| Problem | Fix |
|---|---|
| Variable names and values from imported JSON/XML and share links were spliced into `innerHTML` and into an inline `onclick="editVariable('…')"` — a design link could run script in the viewer's browser (5 call sites) | `createVariableItem` in [script.js](../script.js) builds each row with DOM APIs and `addEventListener`; `tests/web/builder.test.mjs` fails if a hostile name becomes markup |
| Validation panel and Dynamic List inspector inserted config-derived text unescaped | `escapeHtml` on the location label and on the `value=` attributes |
| AI panel's Anthropic call was missing the browser-access header (blocked by CORS) and named a retired model | header added, model updated |
| Viewport locked pinch-zoom (`user-scalable=no`) | removed |
| Theme preference read `localStorage` unguarded and threw when storage was blocked | wrapped in try/catch |
| Eight inline `onclick`/`onchange` attributes blocked any future strict CSP | moved to `data-click` / `data-change` bindings |
| Nothing ran the player's ~150 test files in CI | `.github/workflows/ci.yml` (player, race detector and staticcheck jobs are all blocking; the npm audit and govulncheck advisories stay non-blocking) |
| `docker-image.yml` built with Go 1.23 while `go.mod` needs 1.26 | `go-version-file: player/go.mod` |

Also added: `robots.txt`, `sitemap.xml`, Netlify `_headers` (nosniff, referrer and permissions policy, and a Content-Security-Policy that the Playwright suite enforces via tests/e2e/serve.mjs), Dependabot for Actions, Go modules and the test package.

Not done, and why: a Content-Security-Policy (inline `style=` attributes and LAN device pushes over `http:` need a careful allow-list first), a service worker (a stale-cache bug would be hard to recover from; the favicon is 48 px, below the 192 px installable-PWA minimum), and any change under `player/` (the SDL2/CGO build could not be run in the environment used for this pass).

## Phase 1 safety nets (October 2026)

The upgrade brief's first phase was CI that actually catches regressions. What
exists now, what it found, and what is still advisory:

| Check | Status | Where |
|---|---|---|
| Builder syntax (`node --check` on the four scripts) | blocking | `ci.yml` `web` |
| Builder jsdom tests (import, share sanitizer, hostile variable names) | blocking | `tests/web` |
| Builder smoke test in Chromium: load, drop a component, undo/redo, export JSON, re-import | blocking | `tests/e2e` (`builder.spec.js`) |
| Player vet + tests for packages without SDL | blocking | `ci.yml` `player-core` (`CGO_ENABLED=0`) |
| Player vet + tests for the SDL package | blocking | `ci.yml` `player` (runner pinned to `ubuntu-24.04`). Its first GitHub run failed two tests that only passed on a Windows desktop; both fixed, then green |
| `staticcheck` | advisory | `ci.yml` `player-staticcheck` |
| `go test -race` | advisory | `ci.yml` `player-race` |

### The smoke test paid for itself immediately

Its first run failed on the undo step. The cause was not the test:

```css
.app-bar-button:hover, ... {;
  transform: translateY(100%);
  opacity: 0;
  pointer-events: none;
}
```

A bad merge had copied the closed AI-panel state into the toolbar hover rule, so
hovering any app-bar button moved it off-screen and made it ignore the pointer.
The click then landed on the toolbar behind it: Undo and Redo were silently dead
for mouse users, and the rule also left a duplicated `.ai-panel.ai-open` block,
an orphaned declaration and a duplicate `:active` rule. Replaced with the
intended hover style (`--surface-tertiary` + `--secondary` border). The
Playwright test now presses the real buttons, so this cannot regress through the
CSS route again.

### staticcheck triage

232 reports on the first run, 199 after this pass, in four groups:

| Code | Count | Decision |
|---|---|---|
| `U1000` (unused symbol) | 166 | Deferred to the `core.go` / `tools.go` split in Phase 3. 45 are in `tools.go`, 28 in `core.go`; deleting them without the split makes review harder than the cleanup is worth. |
| `ST1005` (capitalised error string) | 28 | Deferred: those strings are surfaced verbatim in device toasts, so rewriting them is a user-facing copy change, not a lint fix. |
| `SA1019` (deprecated API) | 4 | Deliberate: the PS4 registration protocol requires CFB (`crypto/cipher.NewCFBDecrypter`), and `jukaland.go`/`morefeatures.go` use `math/rand` intentionally. |
| `SA6005` (`EqualFold`) | 1 | Left as-is: `ToLower(a) == ToLower(b)` and `EqualFold` differ for a few Unicode case pairs, and the dictionary highlight should not change behaviour for a lint suggestion. |

Fixed in this pass (each behaviour-preserving, all covered by the existing
suite): all nine `SA4006` dead stores (including one in a test), both `SA4003`
impossible `uint8` comparisons (one was a dead clamp in the spinner, the other a
tautological test now checking drawing tokens are not fully transparent), the
`SA4031` test whose nil check could never fail (replaced with a mirror-parity
assertion), `SA1012` (`nil` → `context.TODO()`), `SA1024` duplicate `TrimLeft`
cutset, `SA6005` in discovery sorting, `SetModTime` → `Modified`,
`tar.TypeRegA` → `tar.TypeReg`, `[]rune` range → string range, `fmt.Sprintf` on a
constant, a redundant `len()` nil check, `append` spread over a copy loop, and
the `== false` comparisons.

### Not verified

- The `player` job has now run on GitHub (Ubuntu 24.04) and is blocking. Its first
  run found what a Windows-only local run could not: `retroArchCmdline` used
  `filepath.Base`, which does not split a Windows path on Linux, and
  `TestApplyButtonSwapFlipsABOnly` assumed a desktop although the unset default
  is platform-dependent (handhelds swap A/B). Both are fixed and the handheld
  default now has its own assertion. Failures are also written to annotations
  and the step summary, because the raw log is easy to miss.
- The race job is advisory because a full `-race` run of the SDL package takes
  minutes and was only spot-checked here (PS Remote tests, `-race`, pass).
- Playwright was run against Chromium only; WebKit and Firefox are not in the
  matrix, and no mobile device profile is covered.

### Next

Those four are now Phase 7 items in the
[modernization plan](../player/MODERNIZATION.md#phase-7-web-builder): the
`script.js` module split, the CSP, self-hosted fonts and the PWA.

## Builder design pass (October 2026)

The web builder had a stylesheet that had survived a bad merge, and the damage was
structural rather than cosmetic. `style.css` opened with ~20 copies of the closed
AI-panel state; ten `.ai-panel.ai-open` blocks, twelve top-level declarations and
four `prop {;` fragments sat outside any rule. The file ended at brace depth -12.
Browsers kept rendering by dropping the invalid fragments, which hid three real
bugs:

- **The AI assistant never appeared.** A duplicate `.ai-panel` rule at the top of
  the file still applied `transform: translateY(100%)`, `opacity: 0` and
  `pointer-events: none`. The later floating-card rule overrode position and size
  but not those three, so the AI button lit up and no panel was painted.
- **Hover killed four kinds of control.** `.export-dropdown button:hover`,
  `.context-menu button:hover`, `.action-button:hover` and `.mobile-menu-item:hover`
  had each had their body replaced by the closed-panel state, so hovering moved
  the control off-screen and set `pointer-events: none` (the same defect already
  recorded for the app-bar buttons in Phase 1).
- **Motion and Text Browser colours were undefined.** `--transition-*` and `--tb-*`
  only existed in the orphaned block, so every transition resolved to nothing.

### What changed

- **CSS repair:** one canonical `.ai-panel`, tokens folded back into `:root`, four
  hover rules restored, the duplicate theme block removed. `style.css` now parses
  at brace depth 0 with no orphan declarations.
- **App bar:** 60 px, grouped tools separated by hairlines, quiet buttons, one
  filled action (Export). The title no longer ellipsises at 1024 px and "Dark Mode"
  no longer wraps. The theme toggle is icon-only and keeps a state-carrying
  `aria-label`. The subtitle follows the device picker.
- **Canvas:** fit-to-window zoom with `- / % / +` controls and a real status bar.
  The scale is applied through a sizing wrapper and every pointer delta is divided
  by it. Safe centring fixes a separate bug where a 1280 px device in a 500 px pane
  was centred *outside* the scroll origin, making its left edge unreachable.
- **Palette:** denser cards (all nine components fit at 1280x800), sticky filter,
  a scroll fade, fixed search-icon gutter (the generic `input[type=search]` reset
  used to win on specificity), and single-line variable rows.
- **Inspector:** the App Info panel was four unbalanced, nested fieldsets; it is
  now four titled cards. Position inputs share the grid, colours pair up, tabs are
  sticky, and the section headings read as labels instead of notched legends.
- **Guide modal:** the heading, subtitle, quick-start cards and close button were
  all direct flex children, so they laid out side by side and overlapped. The
  quick starts now open the body in a three-up grid (one-up on phones).
- **Mobile:** the fixed tab bar was painting over the canvas toolbar and the drawer
  header; both now offset it. The app bar keeps one row, drops desktop-only tools,
  and no longer cuts tools off the left edge. The AI glyph is no longer hidden by
  the label-hiding rule.

### Verification

```
tests/web   npm test                     8/8 PASS
tests/e2e   npx playwright test          5/5 PASS (4.3 s)
node --check (web + test scripts)        PASS
style.css                                depth 0, 0 orphan declarations, 1 .ai-panel
```

The two new e2e tests cover the regressions that were invisible in a build: a drop
at 200,100 screen px lands at 200/scale on the device, zoom in/fit round-trips, and
the AI panel is visible with `opacity: 1` and no transform.

Screenshots for review: `screenshots/design-desktop.png`, `design-laptop.png`,
`design-compact.png`, `design-light.png`, `design-phone.png`,
`state-element.png`, `state-ai.png`, `state-guide.png`.

### Not verified

- Chromium only. Firefox and WebKit are not in the Playwright matrix, and no
  physical device was used for touch sizing.
- Contrast was checked by eye against the existing tokens, not measured against
  WCAG thresholds.
- The gallery and device dialogs need network/hardware and were not re-styled.

## Startup pass (October 2026)

The launcher already had a two-second budget and phase marks, but the marks were
only ever read on a desk. This pass made the number something CI and a device can
both answer, and removed the two costs that were actually measurable.

### What changed

- **Network work moved after the first frame.** Weather, tool health, the update
  check, telemetry, the network monitor, the phone companion listener and social
  notifications are queued with `deferStartupTask` and start only after the
  first `Present()`. Before, they competed with cold font rasterisation on the
  same core. A task that takes over 20 ms is logged after the frame.
- **Warm starts skip the splash.** `<data>/startup-state.json` records the first
  frame of a successful boot. A relaunch within ten minutes skips the 750 ms
  animation; a cold launch keeps it. The marker is versioned, future timestamps
  are ignored, and a failed/partial boot never writes one.
- **`startup-bench.sh` is the repeatable measurement.** It builds once, runs the
  binary headless with SDL's dummy drivers, deletes the warm marker before each
  sample and fails if the median exceeds `JUKAHUB_BENCH_LIMIT_MS`. CI runs it as
  a blocking `player-boot-bench` job with a 5 s runner limit; on a device the
  same script is run with `JUKAHUB_BENCH_LIMIT_MS=2000`.
- **`JUKAHUB_BENCH_WARM=1`** exercises the warm path through the same
  exit-after-first-frame harness, so the improvement can be measured rather
  than inferred.

### Numbers

Windows desktop, SDL dummy drivers, 1280x720 config, cold samples:

| Phase | Before | After |
|---|---:|---:|
| SDL init | 20–24 ms | 17–19 ms |
| config | 3–5 ms | 3–5 ms |
| modules | 90–110 ms | 66–83 ms |
| window + renderer | 148–300 ms | 148–161 ms |
| splash | 757–776 ms | 757–770 ms |
| first frame | 121–133 ms | 109–112 ms |
| **cold total** | **1165–1321 ms** | **1107–1129 ms** |
| **warm total** | — | **370–408 ms** |

The warm column is the useful one: the same binary drops from ~1.1 s to ~0.38 s
when the splash is skipped. The absolute desktop milliseconds are not a device
claim; the SD card and the 1 GB CPU move the phases, which is why the script is
meant to be run on the TrimUI.

`GODEBUG=inittrace=1` reports ~2.8 ms of package initialisation in total
(`main` 1.2 ms, `net` 1.0 ms, gopsutil 0.6 ms). Lazy-loading the heavy imports
would save low single-digit milliseconds, not the splash-length gap, so it was
not done.

### Deliberately not done

- **A persisted label/glyph cache.** The first frame is ~110 ms of cold text
  rasterisation. Serialising label textures would trade CPU for thousands of
  small reads on the slowest storage in the device, with theme/font/size keys to
  invalidate. Not worth it before the device numbers exist.
- **Idle CPU and game-launch timing on the device.** The headless desktop uses
  the software renderer, so its idle CPU is a rendering benchmark, not a battery
  measurement, and launching a real emulator needs the hardware. Both need a
  device session; reporting desktop numbers as battery figures would be
  misleading.

### Verification

```
go vet ./...                              PASS
go test ./...                             PASS (all player packages, 11.7 s)
startup-bench.sh (3 cold runs)            median 1135 ms, limit 5000 ms PASS
warm bench (JUKAHUB_BENCH_WARM=1)         370–408 ms, splash=0 ms
.github/workflows/ci.yml                  YAML parses; new blocking job present
```

## Technical pass (October 2026)

This pass took five of the items this page ranked, plus the documentation gaps
around them. They are recorded where the plan tracks them, not here: component
versions and LED policies in
[MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25),
the keymap in §5.4 (with [ADR 0001](adr/0001-physical-logical-keymap.md)), the
`NetPlayEnabled` switch in §5.5, and the flaky-test root cause in Phase 6 — the
crash was renderer-owned textures reused after their renderer, not the thumbnail
workers.

Documentation in the same pass: [STYLE.md](STYLE.md) (the conventions a review
checks, not a new standard), [CHECKPOINTS.md](CHECKPOINTS.md) (the milestone
design, explicitly unimplemented), a curated-package section in
[PACKAGES.md](PACKAGES.md), bug-report and feature-request issue forms, a
"Why JukaHub" section in the README, and `make coverage` (the CI player job
already publishes the coverage total and the 0 % functions to the step summary;
it is advisory, not a gate).

What that pass left open is now tracked in the plan rather than in this list: the
two hardware verifications in MODERNIZATION Phase 8, and enabling GitHub Issues in
the [roadmap's maintainer checklist](../ROADMAP.md#appendix-c-maintainer-setup-checklist).
