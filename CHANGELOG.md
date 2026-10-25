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

- AndroidPak loader wiring for a real NDK APK's first run. JukaHub now hands
  the AndroidRT loader form what `launch.sh` used to arrange only for
  itself: `ANDROIDPAK_SHIMS` (the runtime's `shims/` directory, preloaded
  `RTLD_GLOBAL` by the runner) and a staged-first `LD_LIBRARY_PATH`
  (`<staged>/lib/<abi>` then `shims/`, the device's own GL userspace still
  last so the PowerVR EGL wins over a stray copy in the APK). In
  `player/androidrt/build-tsp.sh` every shim is now linked with
  `-Wl,-soname,<name>` so glibc satisfies a game's `DT_NEEDED`
  (`liblog.so`, `libandroid.so`, `libOpenSLES.so`) out of the preloaded
  shims instead of hunting the search path for a different bionic copy.
  The first run's failure is captured: when an AndroidPak profile exits
  non-zero, the launch log (`data/game-profiles/logs/<id>.log`) is scanned
  for the runner's "undefined symbol" line and the symbol is named in a
  toast and the JukaHub log — the next-shim answer without opening the log
  by hand. Go tests cover the env handoff and the capture.
- OpenSL ES shim audio path for AndroidRT: `player/androidrt/src/openslessim.c`
  now plays — real `Enqueue`/`SetPlayState` streaming of S16 1/2-channel
  BufferQueue sources through ALSA `default` (`snd_pcm_*` resolved from
  `libasound.so.2` via dlopen only; no header and no link-time ALSA on the
  device build). Per-buffer completion callbacks fire from the shim's worker
  thread like native Android delivers them, `GetPosition` tracks frames
  consumed, `SetPlayState(STOPPED)` drops queued content without late
  callbacks, and `Destroy` from inside a buffer-queue callback is safe (the
  worker frees its own object). When libasound or the playback device is
  missing, playback degrades to a wall-clock silent pacer at the game's
  sample rate — callback order and timing stay truthful, launch never fails
  for lack of audio. The ABI is verbatim 1.0.1: real `SL_RESULT_*` values,
  wilhelm interface UUIDs, per-interface vtable member order, and the three
  create functions return the `SLObjectItf` handle the header promises. The
  smoke harness gains a host audio probe asserting the whole chain, including
  the callback-destroy case.
- Input shim for AndroidRT: NativeActivity games get a real `AInputQueue`
  instead of the silent no-op. `player/androidrt/src/inputshim.c` implements
  the NDK queue contract — `AInputQueue_getEvent`/`hasEvents`,
  pre-dispatch no-swallow, `finishEvent`, joystick axis coalescing while
  pending, an event the app holds is never overwritten, and
  `ALooper_pollAll` reporting `LOOPER_ID_INPUT` with the queue as user data
  while events are pending, so `android_native_app_glue`'s input loop runs
  unchanged. On the device `player/androidrt/src/evdev.c` reads
  `/dev/input/event*` and maps the Linux gamepad spec (aarch64 layout
  pinned header-free) onto Android keycodes/axes; once per launch it prints
  which nodes opened, and an unreadable node set names the missing input
  group instead of failing silently. The runner's lifecycle now dispatches
  through the game's own callbacks table (the NDK contract `onCreate`
  implies) with the runner stubs as fallback, and the smoke harness adds an
  input probe asserting the whole semantics chain host-side. Disabled via
  `ANDROIDPAK_INPUT=off`. Run smoke green end-to-end; on-hardware
  verification of the TSP's evdev nodes is the open question.- Android layer, launch-focused: the AndroidPak profile runtime now runs a
  memory pre-flight before staging an APK launch (`androidPakPreflight` in
  player/androidpak.go) — staged bytes + 192 MiB runtime budget + 96 MiB hub
  reserve versus `MemAvailable`, with a refusal that names the numbers on the
  1 GB device and a `JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB` opt-out. The AndroidRT
  C side gains the surface-stack probe (`player/androidrt/src/surface.c`):
  header-free dlsym of the glibc GE8300 GPU userspace through its whole
  GBM+EGL chain, printing exactly which layer holds per launch with
  `ANDROIDPAK_SURFACE=1`; smoke builds a standalone probe binary host-side.
  Run smoke and AndroidPak suites verified; the probe's on-device verdict is
  still to be taken (no GPU stack on this desk).

- GammaOS study for the Android engine: docs/ANDROID-GAMMAOS.md records what
  full-Android community firmware (GammaOS Core/Nano for the same A133P
  hardware) proves — Android on this SoC is a firmware swap, not an app-level
  integration — retargets the AndroidRT surface milestone at the proven glibc
  GE8300 userspace instead of the Android/Bionic GPU blobs, and proposes a
  memory pre-flight for AndroidPak launches. The Android ways table gains the
  firmware row and the binder-missing panel now names the alternative.

- Hardware decode investigation doc: docs/VIDEO_VPU_OFFLOAD.md records the desk
  pass over offloading the video player's decoder to the A133P's Cedar VPU —
  silicon capabilities per codec, why the current ffmpeg rawvideo pipe bypasses
  them, why mainline Cedrus is not a stock-firmware option, and the recommended
  CedarX→stdout bridge design with its five open on-device probe questions.

- Icon atlas for the shared rendered glyph set: every home card's icon tile,
  tool/emulator tile and launcher empty-state glyph used to be built from
  filled primitives per frame — on the GLES2 renderer one cached-shape blit
  (texture bind + draw call) each, hundreds of small binds per frame. Each
  (name, focused) glyph is now drawn once at the canonical 32px into one
  1024×64 strip and every later occurrence is a single Copy of its sub-rect,
  with the same primitives painting the strip, off-32 callers still rendered
  natively, unknown icon names still drawing the fallback ring, and automatic
  direct-paint fallback on renderers without render targets. Cleared wherever
  shape textures are cleared (theme change, memory pressure, a11y font
  reload, shutdown) and dropped per renderer like every other texture cache.

- "While you were playing": background work that finishes while a game, port
  or Wine app owns the screen — saved-track downloads, helper-tool and
  PortMaster installs, achievements — is now collected into one summary toast
  shown on the first frame after the game hands the screen back, instead of
  each finisher's own toast piling up unseen and only the last one flashing
  by when you return.

- Config-driven home cards: every menu card on the home screen (Continue,
  Media, Files, Packages, Apps, Favorites, Browser, Chat, Tools, Emulator,
  Settings, Exit) now takes its position, size, title, subtitle and icon from
  the `Main` scene's `style: "tile"` buttons in jukaconfig.json, instead of
  being hardcoded while the config's stale tile grid was ignored. Both the
  desktop and handheld configs ship the real card set and geometry, an edit
  or config hot-reload reaches the screen on the next frame, and geometry
  outside the canvas falls back to the built-in card. The header cluster
  (network, weather, clock, update, notifications) stays system chrome.

- Embedded YouTube videos in the reader show the video's real thumbnail on
  its play card: the embed's video id maps to a `mqdefault.jpg` URL on the
  image CDN, which the same pass that fetches `<img>` bytes picks up under
  the same size/count budget. The artwork is center-cropped to the card and
  the play glyph and title draw on top; Vimeo, direct files and failed
  fetches keep the dark chrome card as before.

- Per-video playback quality: the video settings sheet's new **Quality** row
  (cycling Auto → 360p → 480p → 720p → 1080p) pins a yt-dlp format ceiling to
  the video on screen and restarts it from the current position, so one long
  video can stream at 480p without touching the global *Playback resolution*
  setting. The pin follows the video's own stream cache entry, applies to the
  temp-file download fallback too, and is forgotten when a different video
  starts.

- **AndroidPak** — the Tier-1 path for running Android games *without* the
  Android system image, aimed at NDK-only games on the 1 GB Smart Pro. A new
  `androidpak` game profile runtime triages an APK read-only (shipped ABIs,
  `arm64-v8a` preferred to match the A133P, the native entry `.so` declared or
  auto-detected, pure-Java packages refused with a reason), stages the native
  subset (`lib/<abi>`, `assets/`, `res/`, manifest, `resources.arsc` — never
  `classes.dex` or signatures) into a bounded tmpfs staging dir so gameplay
  never re-reads the SD card, and launches the user-installed bionic
  translation runtime (`loader` or its `launch.sh`) with the staged tree and
  ABI in `ANDROIDPAK_*` environment variables. Schedule it with
  `JUKAHUB_ANDROIDRT`; staging lands in `$TMPDIR/jukahub-android` unless
  `JUKAHUB_ANDROID_STAGE` says otherwise. Profiles without the runtime list as
  Unavailable with the exact reason, and the staged tree is deleted after the
  game exits. See [docs/ANDROID.md](docs/ANDROID.md). The runtime skeleton
  itself ships as source: `player/androidrt/` cross-builds `loader` plus
  `shims/{liblog,libandroid,libOpenSLES}.so` for the A133P, and a host-only
  smoke check (`tests/smoke/run-smoke.sh`) drives loader → onCreate → window
  → frames → clean teardown with no device and no Android system image.
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
- A random game picker in the Games Library. **R** (or a click of the right
  stick, **R3**) asks whether to roll from the console on screen or from every
  console, jumps the library to the game it picks — console, row and scroll
  position — so **A** plays it and another **R** rolls again. The roll never
  repeats the game already highlighted while the scope has another candidate,
  and it follows an active search so the jump cannot land on a filtered row.
  See [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#random-pick).
- Browser pages in the reader no longer lose their videos: a resolvable
  `<video>` element or video-bearing embed (YouTube, Vimeo, Dailymotion, Twitch,
or a direct media file) reserves a 16:9 play card on the image row grid, with a
play glyph and the video's title, and joins the page's links so the D-pad
reaches it like an anchor. Activating it never navigates — the URL goes to
`playSmartURL`, which streams media files directly and hands everything else to
yt-dlp in the embedded player, the same path a YouTube search result uses. A
structurally non-video iframe and an element with no resolvable source still
skipped as before. See [docs/BROWSER.md](docs/BROWSER.md#videos-and-yt-dlp-playback).
- Scene-scoped background work: leaving a screen now cancels what it started.
  yt-dlp media searches — the results screen, follow-up page fetches, trending
  and the Shorts feed — were still plain goroutines, so leaving while a search
  loaded left the extractor running behind the home screen and let its results
  land on a screen the user had already left. They run in `runSceneTask` with
  the scene's context now (`executeYouTubeSearchContext`), so the cancellation
  kills the process and a cancelled run publishes nothing, and a dead scope
  refuses to start a run at all.
  The podcast feeds, package registry, weather screen, crypto/stock board, speed
  test and both benchmarks take the screen's context, so a 100 MB download or a
  50 MB benchmark stops (and removes its temp file) instead of running behind
  the next screen, and a cancelled fetch publishes nothing. The board's
  independent endpoints also share one round trip now instead of three serial
  ones. Process-wide chrome (network monitor, weather ticker, audio output)
  keeps running. See
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#scene-scoped-background-work).
- `Ctrl+L` opens the log viewer from any scene. It stands aside in the terminal,
  where the key keeps clearing the output, and it is only consumed when the
  config actually has the LogExporter scene. See
  [docs/CONTROLS.md](docs/CONTROLS.md#desktop-keyboard).
- Log exports no longer destroy the previous file: an export moves `logs.txt` to
  `logs.txt.1` and writes the new one atomically, so exporting after a crash
  keeps the log from before it. The exported file is capped at 2 MiB and the
  in-memory ring at 512 KiB as well as 2000 lines, so one huge line cannot grow
  either without bound.
- 720p frame budgets for the home screen: `go test -run '^$' -bench
  BenchmarkHome` reports a warm frame, a cold-text frame (theme switch) and one
  D-pad step, so layout and text changes can be compared against a number.
- Shorts feed cells and search-result cards in the Media browser now fall back
  to real artwork when yt-dlp ships none: a YouTube entry without a thumbnail
  field (usual for flat-playlist Shorts rows) maps its video URL onto the image
  CDN — the same yt-dlp-free derivation the reader's play cards use — and
  paints the actual frame through the shared disk-backed cache and decode
  budget. Metadata thumbnails keep priority, the derived URL only trails as a
  fallback, and non-YouTube entries keep the placeholder. The Shorts grid also
  prefetches artwork like the results list so entering the feed does not paint
  grey tiles.

### Fixed

- One touch tap on a video card in the browser resolved the video twice.
  SDL synthesizes its own mouse press at finger-down, so a page's play card
  was hit by that ghost click plus the app's replayed tap at finger-up — two
  yt-dlp resolves for one tap (and a virtual-keyboard touch inserted a
  character twice). SDL's `SDL_TOUCH_MOUSE_EVENTS` mapping is now disabled;
  the app's own tap-to-click replay (`pushTapClick`) remains the single
  touch-to-mouse path.
- The `player-race` CI job is green again. The browser's fetch seam was a bare
  package variable, so a test that swapped it to a stub while a page load was
  still on the wire raced with the load goroutine reading it — reported as a
  data race in `TestBrowserToggleAdFilterIsPerSite`. The seam is now read once
  per load under a lock (`currentBrowserPageFetcher`), tests swap and restore it
  through `setBrowserPageFetcherForTest`, and a browser reset joins the loads in
  flight, so a late fetch cannot publish into the next test either.
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

- The shell now prefers the device's GLES2 GPU renderer on handheld targets
  (the A133P's PowerVR GE8300 where the vendor stack provides it) and logs the
  active video driver, renderer name, acceleration mode and maximum texture
  size once at startup, so a software fallback is visible instead of assumed.
  The snapshot now lives in one testable place (`renderPipelineInfo` in
  `adaptive.go`) covered by `renderpipeline_test.go`, which pins the exact
  "software fallback" values (including SDL's max-texture `0x0`) the device
  build would print if the GE8300 stack is ever bypassed.
- Rounded cards, rings and circles are drawn from a cached render-target
  texture instead of repeating every scanline fill every frame
  (`shapeCache.go`); the wallpaper readability scrim and day-cycle backdrop
  gradients got the same treatment in `dynamicbg.go`, replacing 80 resp. 24
  per-frame band fills with one blended Copy each. Both caches are
  renderer-local, budget-bounded on the handheld, and pixel-equal to the
  direct paths in the render tests.
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
- The launcher app grid is now row-virtualized: each frame visits only the
  rows the panel can show (plus one slack row for the partially visible row
  that hangs past the bottom edge), so scrolling a hundreds-of-apps launcher
  costs the same tile work no matter how far the list has scrolled. A row
  straddling the panel edge is now drawn clipped instead of hidden — partial
  cards read as the panel edge cut on the tile — with the scrollbar still
  indicating what is below (`appgrid.go`, pinned by `appgrid_window_test.go`).
- Thumbnail decode budget: a freshly scrolled grid used to start a JPEG decode
  and texture upload for every visible row in the same presented frame — 24
  rows could stack all of their decode cost onto one ~16 ms frame. The render
  path now issues at most 2 decodes per frame on the handheld (8 on desktop);
  the rest of an entering screenful is queued through the prefetch workers and
  fills in over the following frames instead of one frame stuttering
  (`thumbstore.go`, covered in `thumbdecode_test.go`).
