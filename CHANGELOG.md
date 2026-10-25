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

- Hold-to-repeat on the D-pad outside the on-screen keyboard. The keyboard had
  polled the pad for a while, but every other screen needed one press per row,
  so walking a long library was one tap per game. `navrepeat.go` now watches the
  held direction and, after 320 ms, re-queues the same D-pad press through SDL at
  a 70 ms cadence — the existing router still decides what a repeat means, so
  overlays, modals, the video player and the link picker all behave as if a
  person tapped repeatedly. The keyboard keeps its own highlight repeat, the
  analog stick's repeat is untouched, and the accessibility slow-repeat setting
  doubles both intervals (`navrepeat_test.go`).
- More of the CSS a real page ships, in the text browser: `line-height`
  (unitless, percentage and length), `border-radius`, `opacity`, gradients
  (`background-image`/`background`), `list-style-type`
  (`decimal`, `lower/upper-alpha`, `lower/upper-roman`, `disc`, `circle`,
  `square`, `none`, including on the list element so a nav menu's bullets
  really disappear), and the `margin`/`padding` shorthands now fill all four
  sides instead of only the vertical ones
  ([player/webpage_css.go](player/webpage_css.go)). Properties the row grid
  cannot honour are approximated rather than dropped: a gradient is drawn as
  the average of its stops, `opacity` is blended into the painted colours,
  `border-radius` is clamped to half the box, and `line-height` becomes extra
  row slots. See [docs/BROWSER.md](docs/BROWSER.md#what-it-can-do).
- Cookies in the text browser. Every browser request — the page, its
  stylesheets, images and scripts, and each redirect hop inside a load — now
  shares one in-memory cookie jar, so a consent answer, an age gate or a
  redirect that only sticks with a session cookie actually works
  ([player/browsercookies.go](player/browsercookies.go)). The jar is scoped by
  host, path, `Secure` and expiry like a browser's, is never offered to another
  site, and is never written to the card: the reader cannot submit a form, so a
  cookie file would outlive its usefulness and become a privacy surface. Feeds,
  update checks and the podcast fetcher keep their own clients, so a page's
  cookie cannot ride along on an unrelated request. See
  [docs/BROWSER.md](docs/BROWSER.md#cookies).
- A **Recordings** screen for the captures an emulator writes. Before a
  RetroArch launch the hub now points `recording_output_directory` at a folder
  it knows (`Recordings` on the card, or `recordingsDir` in the config) through
  the same crash-safe writer the save-state switches use, so the emulator's own
  record toggle and the hub agree on one folder. A folder the user set in
  `retroarch.cfg` is honoured and never rewritten, `recordings: false` keeps the
  hub out of the file, and the hotkey itself stays RetroArch's — guessing button
  codes would break controls on a device whose mapping we have not measured.
  The screen opens from **Apps → Recordings** (the Media button row is full —
  the IPStream chip is injected into it at runtime), lists the clips newest
  first, and plays the highlighted one with A in the existing video player
  ([player/recordings.go](player/recordings.go)).
- Conditional requests for the shared fetchers: `httpcache.go` keeps the last
  successful body next to its ETag / Last-Modified (bounded at 64 URLs and
  1 MiB per body, process-lifetime by design) and the release-manifest fetch
  plus the podcast/ticker text fetch now send those validators, answering an
  unchanged resource from the cache on a 304 instead of downloading it again.
  A server that answers 304 for a URL this process holds no copy of is reported
  as an error rather than being mistaken for an empty document, a newer body
  always replaces the cached one, and `Cache-Control: no-store` is honoured
  (`httpcache_test.go`).
- One-file support export: **Patch → Export diagnostics** now writes the
  redacted report *and* the last few hundred log lines to
  `reports/diagnostics-<timestamp>.txt`, which is the file to attach to a bug
  report ([player/diagnostics_bundle.go](player/diagnostics_bundle.go)).
  Credentials (tokens, API keys, passwords, bearer headers, URL userinfo) and
  home-directory account names are scrubbed from the log first, and the bundle
  says what was removed and that the user should read it before sharing. The QR
  and prefilled-issue flows keep the short report, which has to fit in a URL.
- Architecture decision records: [docs/adr/](docs/adr/) now holds the decisions
  other code has to live with, with the process itself
  ([0000](docs/adr/0000-record-architecture-decisions.md)), the
  physical → logical keymap ([0001](docs/adr/0001-physical-logical-keymap.md))
  and renderer-owned texture caches
  ([0002](docs/adr/0002-renderer-owned-caches.md)); CONTRIBUTING says when one
  is expected.
- A documentation link check in the test suite
  ([player/docs_links_test.go](player/docs_links_test.go)): every relative
  Markdown link in the checkout must resolve, so a renamed page fails the build
  instead of shipping a dead pointer. It found four broken links on the way in,
  including the Android-on-A133P study the Android docs cite (restored as
  [docs/ANDROID-GAMMAOS.md](docs/ANDROID-GAMMAOS.md)).
- Component versions in Patch (and in the diagnostics report): a new row lists
  the running app, launcher script, design config, helper tools and installed
  packages with their SHA-256 digests, so a bug report names exact builds.
  "Copy report" puts the full 64-character hashes on the clipboard; the
  exported diagnostics carries the same section
  ([player/componentreport.go](player/componentreport.go)).
- A real button-remapping layer. The accessibility panel still has the A/B and
  X/Y swaps, plus a "Button layout" preset (Nintendo order sets both) and a
  "Select button" row that can move Select to Start or disable Select while
  Start keeps opening the menu. Under the rows is a general physical → logical
  keymap stored as `a11y_keymap` (`select:start,start:select`, `select:none`),
  applied once before dispatch, so the next remappable button is data rather
  than a scene change ([player/a11y.go](player/a11y.go)).
- LED policies: the LED sync is no longer audio-only. Quick Settings' "LED
  Mode" cycles Audio / Battery / Charging / Temperature / Custom; battery runs
  red → green across 10–80%, charging breathes and goes dark unplugged,
  temperature runs green → red across 45–80 °C, and Custom uses
  `led_color` (`#rrggbb`) with an optional `led_effect` of `pulse`
  ([player/ledsync.go](player/ledsync.go)).
- LAN Play has a switch. Quick Settings' "LAN Play" row writes the previously
  dead `netPlayEnabled` flag, and the Game Library explains where to turn it on
  instead of opening a disabled panel. A config from a build without the switch
  keeps the shipped default (on) because the user copy is only written when the
  row is used ([player/netplay_ui.go](player/netplay_ui.go)).

- Controller legend ([player/controlslegend.go](player/controlslegend.go)): a
  first-run card that appears once the setup tour is completed, and stays
  reachable from a new "Controller Legend" row in Quick Settings. Its rows are
  generated live from the `footerHints` registry — the same audited bindings
  the footers draw — for the scene it opened over, plus the three global pad
  facts (Start menu, the Select + Start quit chord, F1). B closes it; F1 hands
  over to the full keyboard shortcuts reference.

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

- Config writes are crash-safe now. `AtomicWrite` previously renamed the live
  file to `.bak` *before* writing the replacement and never fsynced, so a power
  cut (the normal way a handheld stops) between those steps could leave the
  config missing or empty. It now copies the previous content to `.bak`, writes
  the temp file with an fsync, renames it over the live path and flushes the
  directory ([player/config_health.go](player/config_health.go)). A damaged
  `jukauser.json` is also recovered from `.bak` and written back, with a one-time
  toast explaining what happened, instead of silently starting from defaults and
  dropping the theme, keymap and every other preference
  ([player/extras.go](player/extras.go)).
- A LAN Play launch that cannot start RetroArch now keeps the underlying cause,
  so the failure dialog suggests reinstalling the launcher instead of falling
  back to the generic hint. Errors are wrapped with `%w` throughout the player
  (netplay, tools, crypto, system control, the Windows runtime installer and
  the patch resolver), and
  [player/errorwrap_audit_test.go](player/errorwrap_audit_test.go) fails on a
  new `%v` wrap instead of relying on review.
- The intermittent `SDL_RenderReadPixels` crash in the test suite is fixed at
  its cause. The static-layer caches (on-screen keyboard, chat, video overlay)
  kept textures after their renderer was destroyed; a later probe then reused
  or freed a dangling pointer and corrupted SDL's allocator. The caches now
  record their owning renderer, drop a foreign texture without touching it, and
  the test renderer releases them before destroying its renderer
  ([player/vkeyboard.go](player/vkeyboard.go),
  [player/renderer_cache_owner_test.go](player/renderer_cache_owner_test.go)).

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

- The classic Tools menu (the config's `Misc` scene) is a launcher page instead
  of a wall of wide buttons: a titled header with the Wi-Fi address, section
  headings (SYSTEM, DIAGNOSTICS, UPDATES & TRANSFER), and icon tiles sized to
  the content panel. The buttons are grouped by what they do, so a custom menu
  still lands in a sensible section, and each tile gets its own glyph
  (terminal, clock, gauge, drive, log, bin, update, transfer). The element
  order and triggers are untouched, so focus memory, D-pad navigation, hover
  and clicks all follow the tiles
  ([player/menutiles.go](player/menutiles.go)). The layout is generated in
  memory at scene entry and detached again for a config save, so
  `jukaconfig.json` keeps the geometry its author placed; a panel too short for
  the page (for example 960×544) keeps the authored buttons rather than drawing
  tiles over the footer.
- The disk pie chart only refreshes while its screen is up. The poller used to
  statfs the card every five seconds for the whole session, keeping the CPU
  awake for a chart nobody was looking at; it now skips while the active scene
  has no `piechart` element and the scene takes a fresh reading on entry
  (`hardware.go`, `hardware_poll_test.go`).
- Element `width`/`height` values with surrounding whitespace are accepted, so a
  padded number from the XML dialect behaves like the same number in JSON. A
  `px` suffix, a fraction and a negative size still fall back to the element's
  default, and [docs/CONFIGURATION.md](docs/CONFIGURATION.md#elements) says so
  instead of leaving it to be discovered ([player/core.go](player/core.go)).
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
- The Tools panel reads like the rest of the shell: its header opens with the
  same tools glyph as the Home tile, and each section heading ("FTP & IP",
  "Watch together", "System & updates") leads with a tinted chip carrying that
  group's glyph, so the grid can be scanned by shape before the small-caps
  label is read. A group the build has not seen yet falls back to the Tools
  glyph instead of rendering a bare heading. A tile row cut by the panel edge
  is drawn clipped like the Apps grid instead of hidden, which used to leave a
  blank band at the top of a scrolled panel, and a heading is never left as a
  fragment peeking above the footer (`toolsui.go`, pinned by
  `toolsui_render_test.go`).
- The browser's D-pad model is reachable on real gamepads and follows the page:
  **L3** (left stick click) cycles scroll ⇄ link mode alongside Tab, the toggle
  toast names both keys, and Up/Down in link mode now walk the nearest link on
  the adjacent visual row instead of flat anchor order, so a two-column page no
  longer throws the cursor across the screen. Left/Right keep reading order and
  a one-row page stays put (`browserLinkRowStep` in `browser.go`, pinned by
  `browser_test.go`).
- Power tools reads as a list instead of a stack of labels. Each row now leads
  with the tool's name in the larger face, the value ("20 snapshots", the sync
  target) sits quietly on the right, and the explanation runs underneath; the
  old menu put a small name above a large description, which inverted the
  hierarchy. The per-row `A Open` pills, the ▲/▼ scroll marks and the
  `Close`/`Back` rows are gone: the footer names what the focused row does, a
  thin scrollbar shows the way down, and **B** is the only way out. The panel is
  now sized to the rows it actually shows, so the capped height no longer left
  a dead band under the last row (`powertools.go`, pinned by
  `powertools_ui_test.go`).
