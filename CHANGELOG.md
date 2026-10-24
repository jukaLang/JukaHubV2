# Changelog

Notable changes to JukaHub. The format is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow the
`Version` field in [`player/jukaconfig.json`](player/jukaconfig.json), which is
what the About screen and the updater read.

This file starts at the point it was added, so it does not backfill history.
For anything older, the git log and the
[GitHub releases](https://github.com/jukaLang/JukaHubV2/releases) are the record.

## [Unreleased]

### Added

- `Makefile` as the single entry point for the developer loop: `make dev`,
  `build`, `run`, `test`, `test-web`, `e2e`, `vet`, `fmt`, `lint`, `bench`,
  `web`, `release`, `clean`, and `make check` for everything a pull request
  must pass locally.
- Issue forms for bug reports and feature requests, with the version, device and
  redacted diagnostics report asked for in the form, plus contact links to the
  documentation and the security policy.
- Pull request template that records the definition of done and separates a
  quick win from a change that needs a migration.
- `.editorconfig` and an optional `.pre-commit-config.yaml` (gofmt, `go vet`,
  `node --check` on the builder scripts) so formatting noise stops reaching
  review.
- The game power profile now lifts the CPU frequency ceiling, not just the
  governor: the TrimUI Smart Pro advertises a 2 GHz top OPP while some firmware
  boots capped at 1.8 GHz, and `performance` can only reach what
  `scaling_max_freq` allows. A launch now raises the ceiling to the highest
  frequency the policy offers, logs the clock it applied, and restores the
  firmware value with the battery profile on return. `powerProfileMaxFreq`
  caps it (e.g. `1800` for a cooler 1.8 GHz). See
  [docs/DEVICE_TUNING.md](docs/DEVICE_TUNING.md).
- The browser's toolbar reports how many ad and tracker requests a page load
  skipped, and a **Filter** chip turns filtering off for one site when a page
  breaks without a request the filter drops. The exception is per host and
  saved in `data/browser-block.json`, so it survives a restart; pressing
  **Filter** again restores filtering. See
  [docs/BROWSER.md](docs/BROWSER.md#ads-and-trackers).
- **Audio**, **Video** and **Chat** buttons on every video result. The pills sit
  at the right edge of the row: **Audio** queues the track in the Downloads
  panel as an m4a, **Video** queues the best picture and sound merged into an
  MP4, and **Chat** posts the link as a chat message. The two downloads are
  separate because a track is small enough to keep a library on the card and a
  video is not. Left/Right on a focused row cycles row → Audio → Video → Chat →
  row so **A** presses the highlighted one, and a tap presses the pill it lands
  on. Video rows open in the embedded player from the Downloads panel. See
  [docs/CONTROLS.md](docs/CONTROLS.md#media).
- Per-game launch options in the Games Library. **Start** (or **O**) opens a
  sheet for the highlighted game with a CPU ceiling (Auto / 2.0 / 1.8 / 1.6 /
  1.4 / 1.2 GHz) and extra launcher arguments, stored in
  `data/game-options.json` by ROM path. The cap applies to that game's launch
  only and the firmware ceiling comes back when it exits; a capped game shows
  its clock in the list. See
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#per-game-launch-options).
- Backing out of a video no longer stops it. Back steps down one size instead:
  full screen returns to the scene's player, and the in-scene player shrinks
  into a floating window in the bottom-right corner while the previous screen
  opens. The session keeps its position and audio; a tap on the window returns
  to the player and its close button (or the Stop control, or `q`) still ends
  it. Only the dock/screensaver cover the window. See
  [docs/CONTROLS.md](docs/CONTROLS.md).
- The now-playing title in the top bar is a button: a click or tap, or **A**
  with the controller on the chip, opens what it names — the audio player for a
  track, the video scene (and its floating window, if the video was left in
  one) for a video. The chip joins the header focus order between the clock and
  the bell, so **Right** from the clock reaches it on Home and **Up** reaches it
  from the content on every other screen. Nothing playing means no chip and no
  focus stop. The chip also shows its state at a glance: the headphone glyph
  becomes two pause bars while paused, and a hairline under the title fills in
  with the played part once the track's or video's length is known. With nothing
  playing the same chip names the output instead — the connected Bluetooth
  device's own name (only devices that accept audio) or **Headphones** when the
  jack has a cable in — refreshed in the background every few seconds. That idle
  name is a reading, so it stays out of the focus order and a tap on it does
  nothing. See [docs/CONTROLS.md](docs/CONTROLS.md).

### Fixed

- A CPU cap below the ceiling the firmware booted with was ignored:
  `raiseFrequencyCeiling` only ever wrote a higher `scaling_max_freq`, so a
  device already running at 2 GHz kept 2 GHz despite `powerProfileMaxFreq:
  1800` (and a per-game cap would have had no effect at all). The launch
  profile now writes the requested ceiling in both directions and restores the
  firmware value on exit.
- The Wi-Fi picker is usable by touch from the status bar: the signal meter's
  tap target now spans the top bar and is at least 44px wide (it was the ~34px
  icon, a miss for a fingertip), and the panel has tappable **Rescan** /
  **Reconnect** / **Disconnect** / **Forget** / **Close** pills instead of only
  the pad's footer hints. Tapping the row already marked **CONNECTED** now
  reconnects it (drop the association, re-join with the saved key) rather than
  answering "Already connected" and nothing else. See [docs/WIFI.md](docs/WIFI.md).
- The app could appear locked after the TrimUI's power button: the stop signal
  killed the process before any cleanup, a running game kept the framebuffer and
  the stock launcher stayed dead, so the last frame froze with no input able to
  leave it. Stop signals now take the normal exit path, a running game (and any
  music/video decoder) is stopped before the display is handed back, and a
  6-second watchdog writes the goroutine stacks to `errors.txt` and exits if a
  cleanup step blocks. `launch.sh` catches the same signal and still restores
  the launcher. See [docs/RECOVERY.md](docs/RECOVERY.md).
- Launching a library game crashed on Linux when no config was loaded: the
  time-lapse probe reached `getToolPath` with a nil config, which dereferenced
  it and took the whole test binary down (`TestLaunchLibraryGamePassesROMAndRecords`).
  Tool resolution now falls through to the platform directory and `PATH`
  instead, and a test pins that contract.
- `beginGamePowerProfile` no longer logs "no cpufreq governors on this device"
  and shows a "Performance mode unavailable" toast on machines that have no
  cpufreq at all (desktops, CI runners). A handheld whose governor files exist
  but refuse the write still gets the one actionable warning.
- The browser address-bar test queues SDL events through union-sized wrappers,
  so `go test -race` no longer dies in checkptr with "converted pointer
  straddles multiple allocations".
- Two staticcheck findings in tests: a tautological tint comparison (`SA4000`)
  and a parsed base URL that was immediately overwritten (`SA4006`).

### Changed

- **B** on the video player no longer stops playback: it closes the settings
  sheet, then leaves full screen, then floats the video into the mini window
  described above. Stop is the square button, `q` on a keyboard.
- [CONTRIBUTING.md](CONTRIBUTING.md) points setup and the pre-pull-request
  checks at the make targets, and keeps the raw commands for anyone without
  make.
- The Emulators page states availability instead of hinting at it: each
  emulator tile carries an **Available** / **Unavailable** / **Not installed**
  pill at its bottom edge, and an unusable tile's picture is dimmed. The 4px
  corner dot it replaces was unreadable at handheld distance. The grid keeps
  sorting by availability (usable, then installed-but-unusable, then absent,
  alphabetical within each group), now covered by a test and described in
  [docs/CONTROLS.md](docs/CONTROLS.md).
