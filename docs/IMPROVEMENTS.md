# Where JukaHub can get better

I read [BrickOS's improvements page](https://github.com/hugorosario/BrickOS/blob/main/docs/improvements.md)
(a TrimUI Brick frontend reimplementation) and mapped every item onto this
codebase. BrickOS is a firmware-level frontend on the same class of hardware, so
its list is a good stress test of the parts we share: an updater, text input, a
controller-driven UI, SDL rendering and a Wi-Fi stack.

This page records what was fixed right away, what is worth doing next and where
JukaHub is already ahead, so the comparison stays useful instead of being a
one-off reading.

## Fixed in this pass

| Problem | BrickOS | JukaHub now |
|---|---|---|
| Backspace removed one *byte*, so deleting from `café`, `日本語` or an emoji left a broken glyph and the next press looked dead | "UTF-8 editing: removes a complete UTF-8 code point" | `trimLastRune` in [core.go](../player/core.go) is used by all 11 text-buffer handlers; `TestNoByteSlicedTextEdits` in [text_edit_test.go](../player/text_edit_test.go) fails on any new `x = x[:len(x)-1]` |
| A failed automatic update check was not retried until the next launch — and the handheld often starts before Wi-Fi has associated | "retries a failed automatic check after a minute" | `runAutoUpdateAttempts` in [autoupdate.go](../player/autoupdate.go): two attempts, 60 s apart, never retrying the Patch lock |
| The update modal named a version and nothing else | "a short changelog of the available update (What's new)" | `updatePromptHighlights` shows up to three cleaned release-note bullets in the modal |
| The update modal passed `\n` straight to the renderer: the body drew as one line that ran off the card and across the screen | — | `wrapTextLines` in [core.go](../player/core.go), verified by [updateprompt_render_test.go](../player/updateprompt_render_test.go) |
| Patch confirmations cut the body after two rune-split lines, so an install or an error was explained only halfway | "long Settings confirmations now show their whole text instead of ending with '...'" | `layoutPatchModal` in [patch.go](../player/patch.go) grows the card to the wrapped body and only ellipsizes what the screen cannot hold |

Run the checks with the usual command list in [CONTRIBUTING.md](../CONTRIBUTING.md);
`JUKA_PREVIEW=1 go test -run 'TestUpdatePromptPreview|TestPatchModalRendersLongBody' ./`
writes `tmp/update-prompt.png` and `tmp/patch-confirm.png` for review.

## Next, in the order I would take them

1. **Notification history shows whole messages.** Rows in
   [notifications.go](../player/notifications.go) are a fixed `notifRowH` with
   the message ellipsized on one line, and the dropdown layout, the cursor hit
   test and the ticker all assume that height. The fix is a per-row measured
   height (message wrapped to two lines + timestamp) carried through
   `notifDropdownLayout` and the hit test, not a bigger constant — otherwise the
   rows overlap on the longest errors, which are the ones worth reading.
2. **A component-versions view.** We already store per-file sha256 digests in
   patch manifests and a release stamp in `release-info.json`, but the UI only
   shows one version string. A Diagnostics card listing app, launcher, bundled
   tools and their hashes would make bug reports checkable — BrickOS exposes
   ten components plus build IDs and hashes.
3. **Real button remapping.** [a11y.go](../player/a11y.go) can only swap A/B and
   X/Y; every handler reads physical SDL codes. A keymap layer
   (physical → logical) applied once before dispatch would give Nintendo/Xbox
   presets, remapped Select/Start and a "disabled" state, and would let the
   accessibility panel grow without touching scene code.
4. **Audit texture creation threads.** [thumbstore.go](../player/thumbstore.go)
   and the video path decode off the render thread. SDL textures belong to the
   renderer that made them; creating one on a worker is exactly what BrickOS
   lists as "downloaded covers could appear black after texture creation in the
   worker thread failed". Worth a `runtime.LockOSThread` audit plus a test that
   hammers the thumbnail queue.
5. **Wi-Fi: verify instead of assume.** BrickOS's three Wi-Fi entries are all
   failure modes of reporting success too early: credentials that "saved" while
   the old connection stayed up, a connect that completed after the UI declared
   failure, and a reboot where the interface came up without a DHCP client. Our
   `netmon.go`/`systemcontrol.go` paths need a device session to test, but the
   pattern — reconnect to verify, poll for the address, never trust the write —
   is worth copying into whatever command we use on the Brick.
6. **LED policies.** [ledsync.go](../player/ledsync.go) only mirrors audio.
   Battery/charging/temperature presets plus a custom effect and colour are a
   small, self-contained addition with real device value.
7. **Boot visuals.** [splash.go](../player/splash.go) and
   [wallpaper.go](../player/wallpaper.go) each pick one image. BrickOS browses
   presets with previews, marks the applied one and offers independent Random
   modes — the same shape we already use for wallpapers.
8. **Checkpoints / quick switch.** We have Continue, play time, save manager and
   power profiles, but nothing that snapshots a running emulator. This is the
   largest item on the list; treat it as a milestone, not a task.

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
| Nothing ran the player's ~150 test files in CI | `.github/workflows/ci.yml` (player job is non-blocking until it has been green once) |
| `docker-image.yml` built with Go 1.23 while `go.mod` needs 1.25 | `go-version-file: player/go.mod` |

Also added: `robots.txt`, `sitemap.xml`, Netlify `_headers` (nosniff, referrer and permissions policy), Dependabot for Actions, Go modules and the test package.

Not done, and why: a Content-Security-Policy (inline `style=` attributes and LAN device pushes over `http:` need a careful allow-list first), a service worker (a stale-cache bug would be hard to recover from; the favicon is 48 px, below the 192 px installable-PWA minimum), and any change under `player/` (the SDL2/CGO build could not be run in the environment used for this pass).
