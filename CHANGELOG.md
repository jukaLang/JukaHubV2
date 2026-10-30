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

- **Playtime now answers "have I been consistent?" instead of "what did I use?"**
  The second card on the Playtime screen was seven bars for the past week and
  nothing else, even though the tracker already keeps 90 days of daily totals.
  It now draws a Monday-aligned 13-week heatmap — one cell per day, four accent
  levels by how long that day ran, unfilled slots for the rest of the current
  week — with the current streak, the longest streak in the window, the best day,
  the daily average over active days and the active-day count beside it. The
  current streak survives an empty today (the day is still in progress) and a
  gap ends it; the longest streak comes from the same single pass over the
  window, and days after today are never counted. This is the "Playtime Wrapped"
  entry from the modernization plan, minus a separate PNG export: the phone
  companion already serves this screen through its screenshot endpoint, so the
  card is what the phone sees. The card is also the first Playtime surface
  checked on real pixels — a fully played window must light it, an empty one
  must not, and neither may spill into the gap under the card — and the
  "start exploring" empty state in the neighbouring card is now ellipsized
  instead of running under it. ([player/wrapped.go](player/wrapped.go),
  [player/playtime.go](player/playtime.go); tests in
  [player/wrapped_test.go](player/wrapped_test.go),
  [player/wrapped_render_test.go](player/wrapped_render_test.go)).

- **The Wi-Fi picker now works on Windows.** The backend was written against the
  handheld's `wpa_supplicant` — `wpa_cli`, `/sys/class/net`,
  `/proc/net/wireless` — so on a desktop every probe answered "No wireless
  network interface on this device" and nothing in the panel could connect.
  Windows drives the same operations through `netsh`, and
  [player/wifi_windows.go](player/wifi_windows.go) is that backend behind the
  unchanged panel: the adapter, the scan, the saved profiles, the signal meter
  and join, disconnect, reconnect and forget. A join stages a WLAN profile XML
  in a private temp file, adds it with `netsh wlan add profile`, connects by
  name and deletes the file, so the key is never an argument and never logged;
  the meter maps Windows' quality percentage onto dBm and uses the exact `Rssi`
  Windows 11 prints when it is there. `netsh` is localized, so association and
  signal strength are read from structure — a BSSID line, a percentage — with an
  English label used only for the adapter's name. A radio Windows has not
  brought up yet (a driver still loading at boot, or a dongle plugged in after
  the app started) is no longer remembered as "no interface": that answer is
  re-checked within seconds, and opening the picker asks Windows again.
  ([player/wifi.go](player/wifi.go), [docs/WIFI.md](docs/WIFI.md); tests in
  [player/wifi_windows_test.go](player/wifi_windows_test.go)).

- **A persistent activity strip now shows background work on every scene.** While
  a tool or core install, the app's own update, an offline audio/video download
  or a package install runs, a small stack of chips is pinned to the right edge
  under the status bar — each chip names the operation and carries its own mini
  progress rail (an indeterminate shimmer when the source has no byte size), and
  the chips appear on any screen, so a download started in the video player or
  an install started in Emulators stays visible over the Home screen, the
  Terminal or wherever the user navigates. Active download rows show
  individually and queued rows fold into one "N waiting in queue" line; four
  chips show at a time and the rest collapse into an overflow line, and when
  nothing runs in the background the strip draws nothing at all.
  Package installs switched to the shared installer card (the same channel
  tools, core updates and the app's own update use) instead of an opaque
  scene-level spinner, so an install now names its package, walks through the
  same Resolving → Downloading → Installing phases and reports success or
  failure once — and the strip reads the same state, so there is only one
  source of truth to aggregate.
  ([player/activity.go](player/activity.go), [player/main.go](player/main.go),
  [player/packages.go](player/packages.go); tests in
  [player/activity_test.go](player/activity_test.go)).

- **Frame cost is now measured on the device instead of inferred from a desktop.**
  The main loop times each iteration from the top of the loop to just after
  `Present` — input, scene logic, drawing and the flip, not the idle delay — and
  keeps a histogram in buckets that follow the hardware's budgets (≤8 ms is half
  a 60 Hz frame, ≤17 ms a whole one, ≤33 ms 30 fps, ≤50 ms a hitch, ≤100 ms a
  stall, then overflow). One line a minute goes to the log, and therefore into
  the redacted log tail of a diagnostics bundle:
  `[frame] last 1m: 3595 frames (59.9 fps), avg 7.8ms, p50 ≤8ms, p99 ≤8ms, max 42ms, over 17ms 0.1%`.
  `Diagnostics` gains a `Frame time` line with the session average and percentiles
  plus a `Frame share` table, so a stutter report carries the device's own
  numbers. The line describes the window that just ended (a slow boot does not
  colour the rest of the session); `JUKAHUB_FRAMESTATS=1` shortens the window to
  five seconds for a focused measurement. Frames are not recorded while a game
  owns the screen — those iterations present nothing. Percentiles are reported
  as bucket edges (`p50 ≤8ms`), which is honest about the resolution of a
  histogram and still tells which side of a vsync a frame landed on.
  ([player/framestats.go](player/framestats.go),
  [player/main.go](player/main.go), [player/patch_engine.go](player/patch_engine.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)).

- **The Quality row now says what the stream actually got.** It used to name only
  the ceiling — `Auto (720p)`, or a pin for this video — which is a request, not
  a fact: YouTube hands over a 480p rendition whenever no 720p one exists, and
  the row then described a picture that was never on screen. The row now carries
  the resolution the player is really decoding, read from the probe the decoder
  already does (`embeddedPlayer.sourceHeight`), so a capped stream reads
  `Auto (720p) · got 480p`. The second half is omitted when it matches the
  ceiling (repeating the same number is noise) and when nothing is playing, so
  the row never claims a stream that is not there; a cancelled quality switch
  still reverts the ceiling as before. The settings sheet is wider to hold the
  longest row (`Quality: 1080p (this video) · got 720p`), and the toast that
  announces a switch still names only the new ceiling, because at that instant
  the delivered figure is still the previous stream's.
  ([player/videoplayer.go](player/videoplayer.go),
  [player/videoquality_delivered_test.go](player/videoquality_delivered_test.go)).

- **A long chat and browser session is now measured, not assumed.** The texture
  budgets were sized from reasoning about a 1 GB device; the new long-session
  probe spends a session in the two panels that fill them — 300 messages with
  14 pictures and 24 senders, then 24 illustrated articles read page by page —
  through the real decode-and-cache paths, with every ceiling pinned to the
  handheld branch, and prints the device's own `[tex]` line beside each cap. On
  a desktop at the card's ceilings the session holds 24,576 KB of chat pictures
  (full: it is a sliding window), 256 KB of avatars, 140 KB of cached pages,
  57,600 KB of page images of 64 MB, 8,240 KB of text of 24 MB, no home labels
  (chat and reader draw through `renderText`), 11,684 KB of shapes of 12 MB, and
  88 MB in total. The figures are in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)
  with the read of them: page images are the largest item and the shape cache
  the tightest fit. The probe fails if the session caches nothing, if the
  picture set never crosses its budget, or if any cache ends above its ceiling;
  it also caught a fixture defect worth knowing about — a browser element with
  no source renders the plain-text reader and silently draws no images at all.
  ([player/session_memory_test.go](player/session_memory_test.go),
  [player/thumbstore.go](player/thumbstore.go),
  [player/cachebudget.go](player/cachebudget.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)).

- **The scene-navigation memory harness now measures the shipping ceilings for
  every cache it prints, not just box art.** Rendered text, home labels and
  shapes each pick a handheld or a desktop budget, and the harness left three of
  them to the host: a desktop run reported 64/48/48 MB for those columns while
  the card ships 24/8/12 MB, so the table looked like the device's numbers with
  most of it describing the machine it ran on. One seam (`sceneBudgetForce`)
  now pins all three, the live singletons are retuned to match (`TextCache.
  SetBudget`, `homeTextCacheApplyBudget`, and a shape budget read at eviction
  time rather than captured at construction), and the harness fails if any pin
  did not take. The shape cache's branch also stops being "Windows or not",
  which gave a Linux desktop the handheld ceiling and CI a different number from
  the machine the work was done on. `TestSceneBudgetBranches` pins both sides
  and the derivation, the way the box-art budgets already were.
  ([player/cachebudget.go](player/cachebudget.go),
  [player/textcache.go](player/textcache.go),
  [player/shapeCache.go](player/shapeCache.go),
  [player/homelayout.go](player/homelayout.go); tests in
  [player/memory_growth_test.go](player/memory_growth_test.go),
  [player/cachebudget_test.go](player/cachebudget_test.go)).

- **An emulator update can be rolled back from the Emulators page.** Every
  update already kept the binary it replaced as `<core>_libretro.so.jukahub-
  <stamp>.bak`, but there was no way to put it back without pulling the card and
  a file manager, which is exactly what the backup was for when a nightly build
  runs worse. **R1** on a tile whose core has a kept build restores the newest
  one; **R** does the same from a keyboard. The press is itself reversible: the
  build it replaces is kept as its own backup, so a second R1 goes forward
  again, and the restored file is deleted only after the new core has landed, so
  a failed rollback leaves the card untouched. The footer names the date of the
  kept build (`GBA · Available · backup 2026-10-20`) and the R1 hint appears
  only when one exists, so the key never offers a restore that would do nothing.
  Only this build's own `<core>_libretro.<ext>.jukahub-<stamp>.bak` names parse
  — `.part` downloads, `.jukahub.tmp` staging files and other tools' `.bak`
  files are ignored — and the target is re-checked against RetroArch's cores
  folders at the point that writes, because the path comes from a scan. The
  action is a local file copy, so unlike an install it is offered on the Windows
  build too.
  ([player/emulatorinstall.go](player/emulatorinstall.go),
  [player/emulators.go](player/emulators.go),
  [player/emutiles.go](player/emutiles.go),
  [docs/CONTROLS.md](docs/CONTROLS.md),
  [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md); tests in
  [player/emulatorinstall_test.go](player/emulatorinstall_test.go)).

- **Every texture and byte cache is now bounded in bytes, not just entries.**
  An entry cap is a byte cap only when every entry is the same size, and the
  launcher's icon cache (64 icons of up to 256×256), the chat image cache (16
  decoded photos, 48 MB each as RGBA), the avatar cache, the downloaded
  thumbnail payloads (512 entries of whatever the URL served) and the feed
  bodies kept for conditional requests (64 × 1 MiB) were all bounded by count
  alone. Each now carries a byte budget beside its count, checked in the same
  insertion-order eviction, and a replaced or evicted texture is destroyed
  exactly once. The three texture caches are reported by
  `JUKAHUB_TEXSTATS`, and a critical-memory sweep clears all five together with
  the caches it already cleared, so nothing grows straight back to its ceiling
  after RAM is freed. The budgets are 8/32 MB for launcher icons, 24/64 MB for
  chat images, 4 MB for avatars, 16/48 MB for thumbnail payloads and 8/16 MB
  for feed bodies (handheld/desktop).
  ([player/cachebudget.go](player/cachebudget.go),
  [player/appicons.go](player/appicons.go), [player/chat.go](player/chat.go),
  [player/thumbstore.go](player/thumbstore.go),
  [player/httpcache.go](player/httpcache.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); tests in
  [player/cachebudget_test.go](player/cachebudget_test.go)).

- **The Emulators page installs emulators, not just lists them.** A `Roms/`
  folder with no emulator was a dead end: the tile read "no emulator found" and
  the only fix was copying a folder onto the card by hand. The buildbot channel
  the update checker already reads publishes one aarch64 archive per libretro
  core for this device, so a missing emulator is now an offer: **A** on a
  missing or unusable tile installs the core that runs its system, **X**
  does the same for any tile while also checking an installed core for a newer
  build, and **L1** brings every installed core forward in one pass. A download
  is checked against the CRC32 the index states before anything is written, the
  build lands in the core pool with the URL it came from, and the binary it
  replaces stays beside it as `<core>_libretro.so.jukahub-<date>.bak` — an
  update is undone by renaming one file. Nothing is written outside RetroArch's
  cores folder: `Emus/<system>/` launcher scripts belong to the firmware, and
  the standalone emulators upstream publishes are desktop builds that cannot
  start there. Once a core is on the card, the `Roms/` folder it covers is no
  longer reported as unplayable — the core's own tile is the row from then on —
  and the page's A/X/L1 hints follow what the highlighted tile can really do, on
  both the Smart Pro and the Brick. The Windows build does not offer the two
  core actions: a Linux shared object is not a file a desktop RetroArch loads.
  ([player/emulatorinstall.go](player/emulatorinstall.go),
  [player/emulators.go](player/emulators.go),
  [player/emutiles.go](player/emutiles.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  [player/emulatorinstall_test.go](player/emulatorinstall_test.go)).

- **The browser can fill in and send a form.** The reader dropped every
  `<input>` as a void tag, so a search page arrived with no box to type into and
  the one thing the browser is most often opened for was impossible. A form is
  now content: a text field draws as a filled chip showing its value, its
  placeholder or **Tap to type**, and A (or a tap) opens the keyboard on it; a
  `<textarea>`
  does the same with the text between its tags; a `<select>` steps through its
  options in place, wrapping, since a handheld has no dropdown and needs none; a
  checkbox or radio draws its box and toggles in place, a radio clearing the rest
  of its group; a submit button sends the form. Every control is a cursor stop
  among the page's links, so the D-pad and the link picker reach it. A submission
  posts nothing — the reader cannot POST — so the form's fields go out as a GET
  to its own action, which is the shape a search box accepts; a form is sent
  with every named control it holds, not just the one that was typed into, so a
  text box plus a filter select produces one working address. A submit on an empty
  form opens the first field instead of sending a blank search. A field opens
  holding the page's own value selected, the way the address bar does, so
  correcting a prefilled search is one press rather than a hold-to-delete. A
  submission also carries the form's hidden inputs (the `ie=utf-8`, site-key and
  locale constants a search handler expects), and two forms that share one action
  stay separate, so one press cannot send the other form's fields.
  ([player/webpage.go](player/webpage.go), [player/browser.go](player/browser.go),
  [player/vkeyboard.go](player/vkeyboard.go), [docs/BROWSER.md](docs/BROWSER.md);
  tests in [player/browserforms_test.go](player/browserforms_test.go)).

- **One page header across every screen.** The Emulators page drew its own title,
  summary and count by hand, so it read as a bespoke panel next to Tools and
  About; it now opens with the same `UIPageHeader` those pages build — a tinted
  glyph well, the title, a summary under it and a read-out opposite. The
  full-screen panels that still laid the band out by hand (Games, Save Manager,
  Downloads, Curation, Community and its What's new / BIOS Doctor / Diagnostics
  sub-pages, Compatibility, Windows Apps, IP streams, Game profiles, Power Tools,
  RetroAchievements, Game options, Random game, LAN Play, and the browser's
  Links / Saved pages / Saved cookies overlays) go through it too, via
  `UIPanelHeader`/`UIPanelHeaderBand`, so the band a panel reserves and the bar
  that fills it are one measurement rather than two that can drift apart. A
  sub-page that started its body at a guessed offset now starts under the
  measured band, and the Power Tools output viewer and its scroll counter moved
  with it — the count sits beside the footer rather than over the subtitle.
  A subtitle colour was added to the component so a panel that warned in amber
  keeps warning in amber.
  ([player/emutiles.go](player/emutiles.go),
  [player/components.go](player/components.go); tests in
  [player/emulators_test.go](player/emulators_test.go) and
  [player/page_header_consistency_test.go](player/page_header_consistency_test.go)).
- **The browser remembers where you were reading.** Following a link and
  pressing Back (or Forward, or Reload) returns you to the paragraph you were
  on instead of the top of the page. Positions are kept per address for the
  session, bounded so a long browse cannot grow them without limit; a fresh
  navigation — a typed address, a link, Home — still starts at the top, because
  that is a new place, not a return. The restore is applied on the render thread
  once the page is laid out and clamped to its length, so a page that changed
  since it was last read is never scrolled out of range.
  ([player/browserscroll.go](player/browserscroll.go); tests in
  [player/browserscroll_test.go](player/browserscroll_test.go)).
- **Text zoom in the browser.** The reader's page can be scaled 75%–160% with
  L2/R2 on a pad or `-`/`+` on a keyboard, independent of the global
  accessibility text size. The whole styled page is re-measured from the new
  body size, so the wrap, row height and headings scale together; the toolbar
  chip names the scale whenever it is off 100%, and the choice is remembered across
  pages and relaunches. On a 5-inch panel the size of the words is a first-order
  control, so the web page's L2/R2 moved from jump-to-top/bottom (still one held
  D-pad away) to zoom.
  ([player/browserzoom.go](player/browserzoom.go); test in
  [player/browserzoom_test.go](player/browserzoom_test.go)).
- **A zoom chip on the browser toolbar.** The reader's text scale now has a
  home in the UI, not just the keyboard's `-`/`+` and the L2/R2 triggers: a
  measured chip beside the mode chip shows the current scale, lights up when it
  is off 100%, and steps up one notch on a tap — wrapping, so a tap always does
  something. On a gamepad-only handheld this is the discoverable way to reach
  the zoom. It gives up its spot first on a narrow panel, before Search.
  ([player/textbrowser.go](player/textbrowser.go)).
- **The named Power Mode is now in the Accessibility panel.** Battery Saver /
  Balanced / Performance / Default was only reachable from Quick Settings; it
  now has its own Accessibility row that steps through the same helper, so a
  player picks a battery-vs-speed trade-off without learning two separate
  toggles. ([player/a11y.go](player/a11y.go);
  test in [player/a11y_powermode_test.go](player/a11y_powermode_test.go)).
- **Dropping a file on the window plays it (desktop).** A video or audio file
  dragged onto JukaHub plays through the embedded player, an image opens in the
  viewer, a folder points at Files, and an unknown extension says so instead of
  silently ignoring the drop; a path that does not exist is refused up front.
  The handheld has no pointer, so this never fires there.
  ([player/dropfile.go](player/dropfile.go); test in
  [player/dropfile_test.go](player/dropfile_test.go)).
- **Podcast feeds survive an offline boot.** The last-known-good copy of each
  feed is kept on the card (30-day window, 1 MiB cap) and served only when the
  network fetch fails outright — a live response, a 304 or an HTTP error status
  all win first, so a feed that is genuinely gone is never replayed as if it
  were live. ([player/feedcache.go](player/feedcache.go); tests in
  [player/feedcache_test.go](player/feedcache_test.go)).
- **A bounded image-decode pool and a general semaphore.** Image decoding runs
  under a shared cap (2 jobs on the handheld, 4 on desktop) so a fan-out of
  decodes cannot open one RGBA buffer per item on a 1 GB device, and a weighted
  context-aware semaphore (`Acquire`/`TryAcquire`/`Release`) is available for
  every other concurrency path.
  ([player/bounded.go](player/bounded.go); tests in
  [player/bounded_test.go](player/bounded_test.go)).
- **A finger-sized hit area for compact controls.** `growTouchTarget` grows a
  drawn control's tap area to a 44px minimum, centred on what is drawn, so the
  36px Exit pill absorbs a near-miss press without its drawn geometry moving.
  ([player/touchtarget.go](player/touchtarget.go); tests in
  [player/touchtarget_test.go](player/touchtarget_test.go)).
- **The web builder stamps `schemaVersion`.** A config the builder exports now
  carries the format version the player checks on load instead of none, keeping
  an imported design's own version when it has one.
  ([script.js](script.js); the 17 web-builder tests still pass).

- **The builder's canvas now draws the shipped design the way the device does.**
  Tile buttons were plain coloured boxes with centred text, a `recent` element
  was a dark rectangle carrying its own (near-black) label colour, and the
  device surface was the designer's grey instead of the design's `Background`,
  so `player/jukaconfig.json` looked nothing like the handheld it was made for.
  The canvas now paints `Background` under the background image, draws a
  `style: "tile"` button as the player's launcher tile — 16 px radius, the glyph
  named by the config's `icon` at 44–64 px centred at 36% of the height, the
  label 14 px off the bottom edge — and draws a `recent` element as the Continue
  card (card surface, heading at 18 px, the icon-and-two-lines empty state),
  ignoring the colour fields the player ignores too. An `input` is now the
  device's 8 px rounded field with its text against the left edge and its
  placeholder in the muted colour, not a centred bold "Input", and generic
  elements follow the player's `RadiusMD` of 12 px instead of 6.
  ([script.js](script.js), [style.css](style.css); the 28 web-builder unit tests
  and 17 Playwright tests still pass).

- **Every tool screen draws the app's top bar.** Notes and the PDF reader
  painted a 44px strip with a title of their own, the alarm clock drew a
  bespoke "< Back" pill at 24,24, the Pomodoro timer drew no chrome at all and
  the calculator had no header — five screens, four different ideas of a
  header. All five
  now draw `renderStatusBar`/`renderFooter` like every other module scene, with
  their own read-outs moved under the bar (the note count on the search row, a
  PDF's title and page number on one quiet line, the alarm's clock and
  calendar) and their bodies laid out between `headerBarHeight()` and
  `footerBarHeight()`. The calculator's key grid and history column are centred
  in that band, which also keeps the history column on the Brick's 960px canvas
  instead of past its right edge, and the alarm's calendar does the same. The
  footer now describes each screen's real bindings. Two overlap defects the work
  exposed went with it: the notes list printed its title, preview and metadata
  through each other (three 30px lines in a 64px row; the preview and metadata
  now use a 16px face in an 80px row), and the PDF reader scrolled its text at a
  fixed 18px under a 30px font, with a line count taken from the card height
  rather than the clipped one.
  ([player/notes.go](player/notes.go), [player/calculator.go](player/calculator.go),
  [player/pdfreader.go](player/pdfreader.go), [player/alarm.go](player/alarm.go),
  [player/pomodoro.go](player/pomodoro.go), [player/extras.go](player/extras.go);
  tests in [player/header_test.go](player/header_test.go) and
  [player/calendar_popover_test.go](player/calendar_popover_test.go)).
### Fixed

- **"Could not install ffplay" no longer fails on every attempt on the TrimUI
  Brick.** The handheld FFmpeg bundle is a ~130 MB `.tar.xz`, and JukaHub
  unpacked it with the system `tar`/`busybox`/`xz`. The Brick firmware cannot do
  that (no xz in its busybox; a tar that can still writes the whole ~400 MB tree
  to the card and, as root, trips over chown on exFAT), so the unpack failed
  every time and the card only showed the release URL. Each retry, and again
  each of ffplay, ffmpeg and ffprobe, downloaded the full 130 MB again. The
  bundle is now decoded in-process with the already-vendored xz reader, streams
  out only the binaries it needs, and reports unpack progress. A finished
  download is kept until it has been unpacked, so a retry after a full card or a
  cancel does not download it again; a damaged one is deleted so the retry
  fetches a fresh copy. Leftover partial downloads and unpack stages from a
  killed install are cleared, a card too full for the install is caught before
  the download starts, and the card names the cause instead of a URL.
  ([player/tools.go](player/tools.go), [player/patch_engine.go](player/patch_engine.go);
  tests in [player/tools_unpack_test.go](player/tools_unpack_test.go)).

- **`ffplay` is gone from every download.** Nothing has run it since playback
  moved to the embedded player (which decodes with `ffmpeg`), and BtbN's build
  needs X11, which the handhelds do not have, yet it was still fetched first on
  every launch and copied into the Smart Pro SD-card package. It is no longer an
  installable helper tool (asking for it by name is refused), the SD-card
  package ships only `ffmpeg` and `ffprobe`, and the FFmpeg bundle's copy is
  never unpacked. The **Settings → General → Audio Backend** row now says
  `ffmpeg (built in)` instead of `ffplay`. Copies already on a card are left
  alone and simply unused. ([player/patch_engine.go](player/patch_engine.go),
  [player/netmon.go](player/netmon.go),
  [player/package-sdcard.sh](player/package-sdcard.sh); tests in
  [player/tools_unpack_test.go](player/tools_unpack_test.go) and
  [player/mpv_removal_test.go](player/mpv_removal_test.go)).

- **The Brick's front F1 / F2 keys do something.** The firmware reports them as
  L3 / R3, which almost nothing listened for, and the Controller card told Brick
  users to press "F1" for keyboard shortcuts. On a Brick (or Hammer) F1 now opens
  the Controller card for the current screen and F2 opens Quick Settings; the
  same key closes either. The audio player and the web browser keep their own
  L3 / R3 bindings. The card lists the Brick keys, and the Home footer names
  **Start** (the menu with every other screen) instead of a **B Back** that did
  nothing on Home. ([player/brickkeys.go](player/brickkeys.go),
  [player/controlslegend.go](player/controlslegend.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  [player/brickkeys_test.go](player/brickkeys_test.go)).

- **The header's "Update" pill can be selected from where it is drawn.** On Home
  the pill sits in the header's system corner, immediately left of the
  notification bell, but the home focus graph still had it where the chip used to
  live — left of the Wi-Fi meter, on the other side of the bar. The result was an
  offer that was visible and pressable with a mouse, yet impossible to reach with
  the D-pad: pressing **Left**/**Right** beside it walked past it (bell →
  now-playing chip → clock), and the only path the graph knew was pressing
  **Left** on the meter, which moved focus rightward, away from the pill. The
  graph now follows the drawn order — clock → now-playing → update → bell — so
  **Right** from the clock, and **Left** from the bell, land on it, while a
  hidden now-playing chip or an unadvertised release still falls through to the
  next control instead of trapping the pad.
  ([player/homefocus.go](player/homefocus.go), [docs/CONTROLS.md](docs/CONTROLS.md);
  tests in [player/updatechip_test.go](player/updatechip_test.go)).

- **The Podcasts list no longer extends past the screen bottom on the Brick.**
The curated-feed scene's `dynamiclist` element carried the root config's portrait
geometry (x=24, y=148, width=1232, height=1672) in both shipped configs, on a
device whose logical canvas is 720×576 with a header bar and a footer row below
it: the 1232-wide, 1672-tall list rendered entirely off the screen, so podcast
entries were drawn but invisible, and the scene looked empty with no scroll to
reach them. The element now uses the landscape geometry every other list
element in the config uses — x=25, y=82, width=670, height=508 on both configs,
top-anchored under the header and ending above the footer — so the entries fill
the content band and are reachable with the same navigation as the rest of the
scene. ([player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json),
[player/jukaconfig.json](player/jukaconfig.json); regression test in
[player/layout_bounds_test.go](player/layout_bounds_test.go)).

- **Opening the Wi-Fi picker no longer answers `exit status 255`.** On an image
  whose supplicant keeps its control socket somewhere `wpa_cli` does not look by
  default, every call failed with `Failed to connect to non-global ctrl_ifname`
  and a non-zero exit — and the panel showed Go's error text instead of the
  tool's, with an empty list under it. Three things changed. The control
  directory is now resolved once (`ctrl_interface=DIR=…` from the supplicant's
  own config first, then the conventional directories, verified by finding the
  interface's socket) and passed as `-p` to every call, so the tool reaches the
  supplicant wherever the image put it. A failed call reports what `wpa_cli`
  printed — `FAIL` as-is, an unreachable socket as *Wi-Fi control is
  unreachable — wpa_supplicant is not answering*, a permission error as *Wi-Fi is
  not permitted for this app* — instead of a number that names no cause and no
  fix. And `FAIL-BUSY` (the supplicant is already scanning, which the stock
  firmware causes routinely) is no longer a failure at all: the arriving results
  are used, and a scan that really fails now costs the list rather than the
  panel, which keeps showing the connected network and the saved entries with
  the reason in its header. ([player/wifi.go](player/wifi.go),
  [player/wifi_ui.go](player/wifi_ui.go), [docs/WIFI.md](docs/WIFI.md)).

- **A cancelled quality switch puts the Quality row back on the stream that is
  actually playing.** The switch pins its new ceiling before the re-resolve
  starts, because the yt-dlp format selector reads it at that moment; B cancels
  that resolve on purpose so the video already on screen keeps playing, but the
  row kept the cancelled ceiling — it named 1080p while 360p was on screen. The
  ceiling a stream is started at is now recorded as playback begins (by
  `startEmbeddedPlayback`, the one path every load reaches), and a switch that
  never gets there — cancelled, or failed while resolving — restores it, falling
  back to the value the press replaced when the stream was started outside the
  player's own load path. A late finish cannot rewrite the row once a newer play
  or switch has started, or once playback has moved to another video.
  ([player/streaming.go](player/streaming.go),
  [player/videoplayer.go](player/videoplayer.go); tests in
  [player/videoquality_revert_test.go](player/videoquality_revert_test.go)).
- **Discovery asks the supplicant which interface it drives when nothing on the
  card names it.** Some images expose the radio in neither `/sys/class/net` (no
  `wireless`/`phy80211` marker), nor `/proc/net/wireless`, nor any of the known
  control directories, and their netdev name is not one the naming convention
  knows — there discovery gave up and the picker said there was no radio. A
  fifth source now asks `wpa_cli interface` which netdev it is driving, falling
  back to `wpa_cli -i <netdev> status` over at most four candidates when the
  listing is empty or unusable. It is the
  last source before the name convention, and its answer is cached for a minute
  — including “nothing found” — because discovery is read while the status bar
  draws and an uncached probe would fork a process per frame. A probe never runs
  when an earlier source answered, and the cache is reset when a caller knows the
  hardware changed. ([player/wifi.go](player/wifi.go), [docs/WIFI.md](docs/WIFI.md);
  tests in [player/wifi_interface_test.go](player/wifi_interface_test.go)).
- **Play is modal from the press, and B cancels all of it.** Pressing Play on a
  cold card spends the first seconds downloading yt-dlp/ffmpeg, and the loading
  state only began after those tools were resolved — so the install card was up
  while the results grid stayed live underneath it: the D-pad kept scrolling and
  the wait could not be dismissed. The prepare state now begins on the press,
  before any tool resolution, so the grid is frozen for the whole wait; the
  download honours that state's context, so a cancel stops the transfer and
  removes the partial file, and the install card is hidden rather than left
  showing a paused progress bar. The card also draws its way out as a control:
  a **B** badge and its **Cancel** label in the bottom band, in place of the
  grey "B / Esc cancels" line. The Quality row goes through the same load path,
  so re-resolving a stream at a new ceiling is a modal wait too, and cancelling
  it leaves the video that was already playing untouched. ([player/core.go](player/core.go),
  [player/streaming.go](player/streaming.go),
  [player/videoplayer.go](player/videoplayer.go),
  [player/patch_engine.go](player/patch_engine.go), [player/tools.go](player/tools.go),
  [player/installbar.go](player/installbar.go),
  [player/loadingcard.go](player/loadingcard.go); tests in
  [player/playprepare_cancel_test.go](player/playprepare_cancel_test.go)).
- **The Wi-Fi picker finds the radio on a device that is online.** It reported `No wireless network interface on this device` while the handheld was connected. Interface discovery listed `/sys/class/net` and kept only entries that `DirEntry.IsDir` called a directory — but every entry there is a symlink into the device tree, so the link itself is never a directory and every real radio was filtered out. The probe now resolves the entry before testing it, accepts both the old `wireless` marker and the cfg80211 `phy80211` one, and falls back to `/proc/net/wireless`, to the supplicant's control sockets, and finally to a Wi-Fi interface name when a vendor driver exposes none of those. The status meter's "interface is up" check reads the same list, so a cfg80211 image no longer shows the no-link icon while connected. ([player/wifi.go](player/wifi.go), [player/statusbar_status.go](player/statusbar_status.go); tests in [player/wifi_interface_test.go](player/wifi_interface_test.go)).
- **Every JSON store that holds your work now survives a power cut and a damaged file.** Notes, alarms and the IPTV list got this first; an audit of the rest found the same two defects in sixteen more stores (fourteen on both halves, two only when reading). Each was written with a temp file + rename (atomic, but with no fsync and no backup), so a card pulled at the wrong moment could still leave a zero-length file, and each loader returned an empty store on a parse error with at most one log line — a favourites list, a set of per-game overrides, a saved browser session or a PS4 pairing all read as "never existed". They now go through `AtomicWrite` (temp file, fsync, rename, previous version kept as `.bak`) and `readJSONWithBackup`, which recovers the backup and says so in the log. `favorites.json` was worse still: it was truncated by a bare `os.WriteFile` on every save. The stores are `favorites.json`, `game-options.json`, `game-recents.json`, `musicfavorites.json`, `audioresume.json`, `ipstreams.json`, `tvfavorites.json`, `browser-cookies.json`, `browser-bookmarks.json`, `browser-block.json`, `partyfriends.json`, `partyhistory.json`, `psremote.json`, `startup-state.json`, `save-sync/folders.json`, the curation profiles and the Toolbox's `power-scripts.json`; the `secret` command writes `jukauser.json` the same way now instead of skipping the backup. A store the user deletes — a cleared bookmark list, the browser's Forget (whole jar, one site, or an expiry prune) and a deleted curation profile — deletes its `.bak` in the same step, because data the user removed must not survive on the card. Caches (weather, RetroAchievements, artwork misses, the patch database) are deliberately left regenerable, and a file the app only reads (`game-launchers.json`) still falls back to its built-in default with a log rather than looking for a backup that could never exist. ([player/config_health.go](player/config_health.go), [player/browsercookies.go](player/browsercookies.go), [docs/CONFIGURATION.md](docs/CONFIGURATION.md); tests in [player/store_durability_test.go](player/store_durability_test.go), [player/store_durability_library_test.go](player/store_durability_library_test.go), [player/store_durability_media_test.go](player/store_durability_media_test.go) and [player/store_durability_social_test.go](player/store_durability_social_test.go)).
- **The game library releases box art it is done with.** The preview loads one
  cover per focused row, and the texture cache behind it was an unbounded map:
  scrolling through a few hundred games kept every cover for the life of the
  process — a 200×270 cover is ~216 KB, so a large collection pinned tens of
  megabytes of texture memory on a 1 GB card, exactly what the video decoder
  and the rest of the UI need. The cache is now bounded by bytes (32 MB on the
  handheld, 96 MB on desktop) and by count (256 entries, which also covers
  covers that are missing and cached as a miss), evicting the oldest first; the
  cover on screen is always the newest entry, so it is never the one evicted.
  ([player/game_library_ui.go](player/game_library_ui.go); tests in
  [player/game_library_test.go](player/game_library_test.go)).
- **The rendered-text texture cache has a byte budget.** It was bounded only by
  entry count (512), but that cache is where a wrapped web-page paragraph, a PDF
  line, a terminal row and a chat bubble land — a single one of those can be a
  megabyte, so the count alone was a multi-hundred-megabyte worst case. It now
  also honours 24 MB on the handheld and 64 MB on desktop, evicting least
  recently used entries and always keeping the label being drawn; the texture
  accounting line (`JUKAHUB_TEXSTATS=1`) prints usage against the budget.
  ([player/textcache.go](player/textcache.go), [player/perf.go](player/perf.go);
  tests in [player/textcache_test.go](player/textcache_test.go)).
- **A launcher full of broken icons no longer grows the icon cache.** An icon
  that fails to decode is cached as a miss so the file is not re-read every
  frame, but the miss skipped the eviction order: the count cap only ever
  counted icons that loaded, while the map grew with every missing image. Misses
  now go through the same bound. ([player/appicons.go](player/appicons.go); test
  in [player/appicons_test.go](player/appicons_test.go)).
- **Notes, alarms and the IPTV list survive a power cut.** All three stores were
  written with a bare `os.WriteFile`, which truncates the file before writing the
  new bytes: pulling the power, or filling the card mid-save, left a half-written
  document, and the loaders then started empty without a word — a reader's notes
  simply disappeared. They now save through the same temp+fsync+rename path as
  the config, keep a `.bak` of the previous version, and a store that cannot be
  parsed comes back from that backup with a line in the log instead of reading as
  deleted. The IPTV save had even documented itself as atomic.
  ([player/notes.go](player/notes.go), [player/alarm.go](player/alarm.go),
  [player/ipstreams.go](player/ipstreams.go),
  [player/config_health.go](player/config_health.go); tests in
  [player/store_durability_test.go](player/store_durability_test.go)).
- **The reader's form controls draw glyphs the bundled font has.** The first cut
  of the form work drew the tick box with U+2610/U+2611, the radio with U+25C9
  and the select arrow with U+25BE — none of which Inter carries, so the font
  sanitiser replaced each with an ellipsis and a checkbox read as "…". They draw
  with □ ■ ○ ● ▼ now, and a test walks every control's drawn text past
  `fontCoversRune` so a symbol that is not in the face cannot come back.
- **An unclosed form control no longer swallows the rest of the page.** A
  `<textarea>` or `<select>` whose closing tag never arrived collected every
  following block as its own value, so the page below it vanished into one field.
  It now ends at the next element that starts a new context (a paragraph, a
  table, the next field), while its own `<option>`s still belong to it.
- **An empty field's prompt reads as a prompt.** The reader has one face and
  never painted `webStyle.Italic`, so a placeholder looked exactly like a value;
  it is now dimmed toward the paper, and stops being dimmed the moment the field
  holds something.
- **Typing into a page's field no longer overwrites the page.** The keyboard's
  text went to the browser element's own variable, so filling a form control put
  the half-typed query in place of the page behind the keyboard; it now goes to
  the field's own buffer and the page stays on screen
  ([player/core.go](player/core.go)).
- **The browser opens on its start page, not on whatever the last run was
  reading.** The content and the address are saved with the rest of the config,
  and the first-open check read a filled content variable as a page this run had
  opened — so every launch came back to yesterday's page (a news home page in the
  report that prompted this) behind a stale address bar, with no request going
  out at all. The first open now always loads DuckDuckGo Lite; returning to a
  browser already used in the same run still keeps the page on screen, and a
  link from chat or the Home card still claims the session first.
  ([player/browser.go](player/browser.go); test in
  [player/browser_test.go](player/browser_test.go)).
- **The browser's bottom chips fit their labels again.** Thirteen actions now
  share the row, and equal shares sized it by the count alone, so the widest
  label ("Forward") overflowed its chip on every panel and "Search" did the same
  at 960×544 and below — a control whose word is clipped is one the user has to
  guess at. A chip is now as wide as its own word needs to be at that panel's
  size, and the column's surplus is shared out pro rata, which fits with room to
  spare at 1280×720, 960×544 and 800×480.
  ([player/browser.go](player/browser.go); guard in
  [player/browsercookies_site_test.go](player/browsercookies_site_test.go)).
- **Five ways a "Wi-Fi stopped working" report turned out to be code.** A
  launcher's PATH can miss the sbin folders where firmware images keep
  `wpa_cli` and the DHCP clients, so the picker claimed they were not
  installed — tools are now resolved on PATH and then in the standard system
  directories. A soft-blocked radio surfaced as wpa_cli's bare `FAIL`; the
  scan now says "Wi-Fi is turned off". WPA3-personal routers could not be
  joined at all (the default key management is WPA-PSK-only) — secured joins
  offer `WPA-PSK SAE` and optional management-frame protection, falling back
  to the old defaults when the supplicant rejects either. A stored password
  the router no longer accepted was unfixable from the panel (the row retried
  the same key forever) — it now opens the passphrase keyboard, and the new
  key replaces the stale supplicant entry only after it joins. An image whose
  only DHCP client is `dhclient` never got a lease attempt.
  ([player/wifi.go](player/wifi.go), [player/wifi_ui.go](player/wifi_ui.go);
  tests in [player/wifi_resilience_test.go](player/wifi_resilience_test.go),
  docs in [docs/WIFI.md](docs/WIFI.md)).
- **A tap in the Media gallery plays the row it lands on.** The list layout
  draws 100px rows at a 114px pitch, but the click path always used the
  grid's cell math — 81px cells against the shipped six-row list — so a tap
  on a visible row addressed the wrong result, and both layouts stopped
  hit-testing at the first screenful (`cols*rows`), which made every row
  after the sixth untappable once paging had grown the list. The renderer
  and the pointer now share one row measure (`searchResultIndexAt`), a grid
  or empty frame clears the list's Audio/Video/Chat pill rects instead of
  leaving them to answer taps aimed at nothing, and the results box rejects
  presses aimed at the controls above it
  ([player/core.go](player/core.go), [player/main.go](player/main.go),
  [player/media_paging.go](player/media_paging.go); tests in
  [player/searchresults_click_test.go](player/searchresults_click_test.go)).
- **The browser's toolbar and cookie strip are full-size tap targets.** The
  drawn chips are 32px tall and the cookie strip is a font height — fine for
  a D-pad, short for a fingertip on the Brick's panel. The click path now
  grows every chrome hit area to the 44px handheld touch minimum, centred on
  the drawn rect and with its horizontal geometry untouched, so neighbours'
  gaps still guard against fat-finger overlaps while the drawn chrome looks
  exactly as before. This is the browser's slice of the touch-target audit in
  the modernization plan
  ([player/textbrowser.go](player/textbrowser.go),
  [player/main.go](player/main.go),
  [player/browsercookies_picker.go](player/browsercookies_picker.go)).
- **The Internet Browser's toolbar chips fit their labels on both panels.**
  SEARCH and SCROLL drew 97px and 94px of text inside fixed 84px and 80px
  chips at the Smart Pro's 1280 canvas — the words ran past the chip's
  border — and on the Brick those fixed widths left the address field short
  enough to truncate a URL that fits on the Smart Pro. Every chip now sizes
  itself from the same `small` font the frame draws with (label + padding,
  re-clamped so the row always fits the panel), the ad-filter chip takes
  what is left rather than a fraction of the panel, and the address field
  gets the remainder. The chips' click rects come from that same layout, and
  a two-canvas render test now fails if any chip's label is wider than the
  chip
  ([player/textbrowser.go](player/textbrowser.go),
  [player/browser_devices_render_test.go](player/browser_devices_render_test.go)).
- **The screensaver's hint line no longer runs under the Cookies row in
  Settings → General.** The injected helper ("Idle time before the screensaver
  starts. Tap to change.") draws 620px wide; at y=576 its tail ran beneath the
  retention row's band (556..604) in the right column and was cut mid-word by
  the button. It now sits at y=612 — under its own button, 8px below the row
  and above the panel's bottom edge — and a geometry assertion in the row's
  render test keeps the two boxes apart on every canvas
  ([player/widgets.go](player/widgets.go)).
- **The toolbox scan-gate test no longer races the tool it launches.** The
  second press on a scanner row runs the tool on a goroutine that resolves the
  install through the tools-dir override, while the test's cleanup restored
  that override the moment the assertions passed — under `-race` the detector
  saw the cleanup write against the runner's read, and outside it the runner's
  view of the fake install was a coin flip. The test now waits the async
  runner out before any cleanup can land on it
  (`player/toolbox_scan_gate_test.go`, `player/powertools_ui_test.go`).
- **The screensaver auto-tick tests no longer depend on what earlier tests
  left behind.** `screensaverAutoTick` reads the panels behind
  `ambientEntryBlocked`, the dock screen, the embedded player, the install
  bar, the patch label and the music state — none of which the tick tests
  owned, so a predecessor that left one of them raised held the saver off
  silently and the test failed for a reason it never touched. Both tests now
  pin that whole gate (save → clear → restore), and a failure names the
  blocking input instead of reporting a bare "did not start"
  (`player/screensaver_test.go`).
- **The first-run tour's controls hint no longer runs across the card.** The
  line was anchored 18px above the card's bottom edge, but the band under the
  buttons is only 28px deep and the line is 22px tall on the Brick's compact
  canvas (30px at 1280): a first-run photo showed it drawn through the card's
  bottom border and against the Next button. It now sits centred in the dimmed
  band below the card, clamped to stay on screen on short panels. The step
  body was also cut by pixel count rather than at word boundaries — "then"
  broke as "th" / "n shows" on the Brick, and a "…" was appended even when
  nothing was cut; it is word-wrapped to the same two lines
  ([player/onboarding.go](player/onboarding.go), `player/onboarding_render_test.go`).
- **Buttons now measure the label they draw, so a row's derived value is
  neither clipped nor printed raw.** `UIButton` ellipsized the label template
  (`Screensaver · $screensaverTimeoutLabel`) and then drew the *substituted*
  text: a value longer than its template overran the button, and a template
  longer than the value was cut mid-placeholder, so the literal `$screens…`
  reached the screen. Every Settings row that shows a live value was affected —
  **Screensaver**, **Saved cookies** and **Video snaps**. The substitution now
  happens once, before the fit, so the measurement and the drawing agree
  ([player/components.go](player/components.go)).
- **A video snap ffmpeg cannot decode is now remembered, instead of being
  restarted on the next frame.** The session memo was written under the clip's
  path and read under the game's ROM path, so the two never matched: a broken
  clip was marked, the mark was then looked up under a name nothing wrote, and
  the library panel spawned a new ffmpeg for it again immediately — the process
  storm the memo exists to prevent, on the one device with a single core to
  spare. Both reads now use the clip path the decoder was handed, and the
  `TestSnapPreviewBrokenClipIsNotRetried` test asserts the armed state rather
  than counting goroutines, which is what made it flaky in both directions
  ([player/snappreview.go](player/snappreview.go), `player/snappreview_test.go`).

### Changed

- **Background polling stops while JukaHub is not on screen.** The weather ticker,
  the connectivity probe, the audio-output probe and the LED animation ran for
  the whole life of the process, so a handheld sitting in a game — or a desktop
  window minimised in the background — kept forking `bluetoothctl`, dialling a
  probe host, fetching a geolocated forecast and writing LED sysfs thirty times
  a second for a panel it could not see. They now wait through one gate,
  `backgroundPaused` ([player/backgroundwork.go](player/backgroundwork.go)),
  which is true while a launched game/emulator/port owns the framebuffer or the
  window is minimised; the disk pie's existing per-scene skip is the same rule.
  The tick is kept, so returning costs one interval at most, and the transition
  back to the foreground — the last screen-taking child exiting, or a
  store/restore of the window — broadcasts a wake that refreshes the slow
  readings at once: a forecast on a ten-minute timer no longer stays stale for
  the rest of the session after a game
  ([player/gamesuspend.go](player/gamesuspend.go), [player/main.go](player/main.go);
  tests in [player/backgroundwork_test.go](player/backgroundwork_test.go)).

- **The handheld keeps its emulator cores current by itself, and stops piling
  up the builds it replaces.** Updating every installed core meant opening
  Emulators and pressing Update (or L1), so a card could sit on a nightly from
  months ago with no sign on screen that anything was stale. `startCoreAutoUpdate`
  now runs the same pass once a day on the Trimui — after boot, only where
  cores are installable, only online, and taking the one patch slot so it can
  never overlap a manual install — with `JUKAHUB_NO_CORE_AUTOUPDATE` as the
  opt-out. The download is visible while it runs: the install card grew an
  optional title, so it reads "Updating emulator cores" and draws over whatever
  screen is up, with the core's name and the count of cores in its detail line.
  A pass that could not read the index does not record a check, so an offline
  boot tries again next launch instead of waiting a day. Each replacement now
  keeps at most five `<core>.jukahub-<stamp>.bak` builds and deletes the older
  ones: every install, activation, rollback and automatic update goes through
  `installCoreFile`, so the limit lives in one place, and pruning reuses the
  parser that lists backups for the rollback action, so a `.bak` from another
  tool is never touched. The downloaded builds behind them are bounded the same
  way, at five dated folders per core, because a self-updating card would
  otherwise collect one per core per day; the build just fetched is never a
  pruning candidate. Rollback is unchanged — the newest build is always the one
  it offers
  ([player/emulatorautoupdate.go](player/emulatorautoupdate.go),
  [player/emulatorinstall.go](player/emulatorinstall.go),
  [player/installbar.go](player/installbar.go),
  [player/compat.go](player/compat.go); tests in
  [player/emulatorautoupdate_test.go](player/emulatorautoupdate_test.go)).
- **A home frame no longer pays for work it covers up, and the focus lift no
  longer mints half a megabyte of texture per step.** Profiling the 720p home
  frame turned up two costs that were not the drawing anyone sees. The home path
  painted the backdrop image or theme gradient and then a full-screen blended
  overlay, and the layout's own backdrop filled the screen opaquely on top, so
  those pixels were never shown — and `renderScene` ran the same block again
  before calling the home renderer, which made three passes and one visible. The
  backdrop is now drawn only for the config-driven scenes. The second was
  texture churn: the focused card's hover ease grows it a pixel at a time and
  the shape cache keys textures by size, so each step created a fresh card
  surface and focus ring (485 KB each at 720p) and pushed them into a 12 MB
  cache the handheld budgets for the whole screen — every D-pad move evicted the
  rest of the screen's chrome. The cache now takes a base geometry
  (`fillRoundedRectAt`, `strokeRoundedRectAt`, `DrawHomeCardSurfaceAt`) and
  blits into the grown rect, so one texture per card serves the whole animation.
  Measured on the desktop proxy: a warm home frame 110–124 µs → 46–53 µs, a
  resting frame creating zero new shape textures, and one full lift of the
  480×258 Continue card 2,972 KB → under 64 KB of shape memory
  ([player/shapeCache.go](player/shapeCache.go), [player/core.go](player/core.go),
  [player/homelayout.go](player/homelayout.go), [player/home.go](player/home.go),
  [player/shape_cache_animation_test.go](player/shape_cache_animation_test.go)).
- **The scene-navigation memory harness measures the box-art ceiling that
  actually ships.** Its library phase sizes its synthetic card from the art
  budget, but it read whichever branch the host takes, so a desktop run measured
  the 96 MB ceiling while the Linux CI runner and the device measured 32 MB: the
  budget on the card went unverified on the machine the work was done on. The
  harness now pins the handheld branch (`libraryArtBudgetForce`, an atomic seam
  so the eviction path can read it from the render loop), asserts the pinned
  budget is the 32 MB one, and therefore walks the handheld fixture — 26 covers
  against 32 MB, settling at 17 — under the `-race` CI job as well as locally.
  `TestLibraryArtBudgetBranches` pins both ceilings and the derivation, so
  forcing one branch does not leave the other untested
  ([player/game_library_ui.go](player/game_library_ui.go),
  [player/memory_growth_test.go](player/memory_growth_test.go),
  [player/game_library_test.go](player/game_library_test.go)).
- **One top bar on every screen, and a Back button you can find at arm's
  length.** Home and every scene already drew the same band, but the music
  player and the terminal still drew their own strips: the player had the words
  "Audio Player" and a `B Back` hint where the bar should be, and the terminal a
  `< BACK` text pill, so two of the app's screens had a header the rest did not.
  Both now draw the shared bar — wordmark and version, the scene in the caption,
  the same Back and Menu pills, the release offer and the system cluster — with
  the player's StockOS layout tag moved into its now-playing strip and the
  terminal's Keyboard chip and working directory kept beside the app menu. The
  Back pill is a step taller than the bar's other buttons and is drawn in a 20px
  face twice, one pixel apart (the bundled Inter has no bold face, and on a
  5-inch panel weight is what makes it read as the primary control); its chevron
  points the way the control moves instead of the opposite way, and its colour
  changes with focus: accent-tinted with accent type while idle, solid accent
  with dark type while the D-pad holds it, and a ring around it in that state.
  The release offer now keeps the same corner gap to the bell on every screen,
  so the pill no longer sits 16px from the bell in a scene and 10px on Home.
  `TestEveryScreenDrawsTheSharedBar` renders every scene in the shipped config
  and fails if one of them is missing the bar, the Back pill, the offer or the
  bell ([player/header.go](player/header.go),
  [player/musicplayer.go](player/musicplayer.go),
  [player/musicskins.go](player/musicskins.go),
  [player/terminal.go](player/terminal.go), [player/extras.go](player/extras.go);
  tests in [player/header_test.go](player/header_test.go)).

- **Text is measured once instead of on every frame, which is what the handheld
  was spending its frame on.** Profiling a warm home frame put ~40% of it inside
  `ellipsize` and another ~20% in the font-coverage walk, because every card,
  chip, footer hint and status-bar label re-measured the same unchanged string
  every frame — and each measurement is a `TTF_SizeUTF8` call through cgo into
  FreeType, which on the Smart Pro's CPU costs several times what it does on a
  desktop. Three bounded caches now sit on those paths: the fitted label, the
  plain width and the character substitution, all keyed by face + string and
  cleared by the `ClearTextCache` hook every theme and font-size change already
  calls. On the desktop proxy the home frame went from ~342 µs to ~90–125 µs and
  a launcher page from ~300 µs to ~86 µs, with ~40 fewer allocations per frame;
  the clipped, measured and substituted text is identical — each cache is
  pinned to the uncached function by `textcache_fit_test.go`
  ([player/textcache.go](player/textcache.go), [player/homelayout.go](player/homelayout.go),
  [player/extras.go](player/extras.go), [player/statusbar_status.go](player/statusbar_status.go),
  [player/ps4tile.go](player/ps4tile.go), [player/ps5tile.go](player/ps5tile.go)).

### Added

- **`render_bench_test.go` also benches a config-driven page.** The existing
  benchmarks measured the home screen alone, but most of a session is spent on
  launcher and list pages, which build their header, section labels, tiles and
  footer through a different path. `BenchmarkMenuPageFrame720p` renders the
  Settings launcher page as the user opens it, so a change to a shared
  component shows up in the frame that actually draws it.

- **The diagnostics bundle carries the cookie session's date and retention
  window.** A bug report about being signed out arrived without the two
  numbers that decide it. The bundle now has a `Saved cookies` section between
  the report and the log — `Session start: Oct 12 2026 (40 days ago)` from the
  oldest first-seen stamp still on the card, and `Retention    : 30 days` (or
  `Keep`) from the setting — so a retention contradiction ("30-day window,
  40-day session") is visible at a glance. A jar with no cookies says
  `none stored` instead of a zero date, and a stamp ahead of the device's
  clock says so — handhelds with a reset clock are a real support case
  ([player/diagnostics_bundle.go](player/diagnostics_bundle.go),
  `player/diagnostics_bundle_test.go`).
- **Each site's row in the saved-cookie picker carries its own session start.**
  The Forget chip's count said how much would go, but two sites signed in on
  different days looked the same age — the one date on the Settings row is the
  whole jar's oldest stamp. A picker row now reads `2 cookies · since Oct 12`,
  the oldest first-seen stamp among that host's cookies alone, so the age of
  one site's login is visible before the second press that drops it. The site
  name's budget shrinks by the label's real width so the two right-aligned
  strings can never be drawn through each other, a host with no stamp shows
  its count alone, and the forget-all row keeps no date because it is not a
  site
  ([player/browsercookies.go](player/browsercookies.go),
  [player/browsercookies_picker.go](player/browsercookies_picker.go),
  `player/browsercookies_picker_age_test.go`).
- **The Games Library header carries the clip count, the same figure Settings
  reports.** The Video snaps row could say `On · 12`, but the screen where the
  previews actually play said only how many games and covers it held. The
  status line under the title now appends `· 12 clip(s)` — or `· no clips` on
  a card with none, and nothing at all until the first count lands, exactly
  the row's own rule — read from the same census over `gameSnapPath`, so the
  two can never disagree. Opening the library panel is what asks for the
  count: one scene-work scan, reused for 30 seconds
  ([player/game_library_ui.go](player/game_library_ui.go),
  `player/snappreview_count_test.go`).
- **Pressing an installed package row offers Remove or Reinstall instead of
  running Install again.** A press always started a fresh download, even on a
  row already marked ✓ on the card. A press on an installed row now opens a
  menu over the details pane — **Reinstall** (wipe the old copy, then fetch a
  fresh one; the row's mark goes with the wipe so a failed download cannot
  leave a tick on an empty directory), **Remove** (take the directory off the
  card and unmark the row), **Cancel** — on the pad, the keyboard, and by
  tapping the focused row, which is how a touch user presses a row at all.
  Uninstalled rows still install in one press
  ([player/packages.go](player/packages.go), `player/packages_test.go`).
- **The Settings cookie row says when the saved session was first stored.**
  `Saved cookies · 30 days` answered "30 days from when?" with nothing to point
  at. The row now carries the reference point — `Cookies · 30d · since Oct 12` —
  the oldest first-seen stamp still on the card, re-read when Settings → General
  is entered and after a prune that drops it, and cleared with the session by
  **Forget now**. The window is shortened to `30d` because the date and the full
  `30 days` cannot both fit the row's cell on the 800px panel
  ([player/browsercookies_retention.go](player/browsercookies_retention.go),
  [player/widgets.go](player/widgets.go), `player/browsercookies_age_test.go`).
- **The Video snaps row says how many games on the card actually have a clip.**
  The switch could read **On** on a card holding no clips at all, which is a
  setting that changes nothing and no way to find out without opening the
  library. The row's value now carries the count — `Video snaps · On · 12` — and
  a card with none reads `On · none`. It counts the same `gameSnapPath` the
  preview plays, so a clip left behind for a ROM that is gone is not counted,
  and the row's state stays first because the count describes what the switch
  has to work with, not whether it is on. The scan runs off the render thread
  when **Settings → General** opens — one library walk, reused for 30 seconds —
  so the page never blocks on the card and the figure lands on the row as soon
  as it is known ([player/snappreview.go](player/snappreview.go),
  [player/snappreview_count.go](player/snappreview_count.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#video-snaps),
  `player/snappreview_count_test.go`).
- **The Browser can forget one site's cookies instead of the whole jar.**
  Signing one site out meant dropping every other login with it and retyping
  them on a D-pad. The toolbar gains **Sites** — also the controller route into
  the picker — and the strip under the page opens the same panel when tapped,
  since that line is already the one naming the hosts. The panel lists each host
  with its cookie count plus a **Forget all** row; a row asks twice, the first
  press naming what would go in the header line, and a confirmed press drops that
  site's records, rewrites `data/browser-cookies.json` with what is left (or
  deletes it when that was the last site) and reloads the page. The jar is
  rebuilt from the remaining records, because the standard jar has no way to
  delete a single cookie: dropping the record alone would leave the jar still
  sending it. An arm belongs to one row, and **Forget all** shares the chip's
  confirm window ([player/browsercookies.go](player/browsercookies.go),
  [player/browsercookies_picker.go](player/browsercookies_picker.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-one-site),
  `player/browsercookies_site_test.go`).
- **The Package Manager list marks what is already on the card.** Installed state
  lived only in the details pane's button, so finding out what was still missing
  meant stepping the cursor down every row and reading it. A row whose package
  directory exists now carries the app's check glyph in a measured left gutter,
  in the success colour — the same ✓ the install bar ends on — and names line up
  whether or not a package is installed. The flags come from one in-memory set
  refreshed with a single directory scan when the screen loads, rather than a
  stat per row per frame, and the install path, the installed check and the mark
  now share one name sanitiser so they can never disagree about which directory
  a package means ([player/packages.go](player/packages.go),
  [docs/CONFIGURATION.md](docs/CONFIGURATION.md), `player/packages_test.go`).

- **Clearing the saved browser session is one press from Settings.** The
  retention row explained how long a cookie is kept but the only way to drop one
  now was to open the Browser, find the toolbar chip and confirm a reload.
  **Settings → General** now pairs it with **Forget now**, in the bottom-right
  cluster above **Save**: the same two-press drop, the same toast, the same
  deletion of `data/browser-cookies.json`, and the same shared arm, so a press in
  either place can be finished in the other. The two cannot share one row — at
  this font `Saved cookies · 30 days` is 275px and `Forget now` 127px, so
  splitting the 360px band would ellipsize both ([player/widgets.go](player/widgets.go),
  [player/browsercookies_retention.go](player/browsercookies_retention.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-them),
  `player/browsercookies_forgetrow_test.go`).
- **Every page resolves D-pad direction the way its arrows were drawn.** The
  focus graph's fallback — the pass that guarantees each control has at least
  one neighbour per direction — classified an edge by the *sign* of its offset
  rather than by the axis it was built along. A control off to the side and
  slightly higher therefore counted as the neighbour above, and the control that
  really was directly above became unreachable: on Settings → General, the
  "Replay tour" button in the top-right corner stole `Up` from the Groq API key
  field, stranding **Weather** behind it. Edges are now classified by their
  dominant axis, which is the same rule that decided they existed
  ([player/focus.go](player/focus.go), `player/dpad_audit_test.go`).

### Changed

- **The Browser's saved-cookie count now says which sites it belongs to.**
  **Forget 3** on the toolbar told the user that a session was remembered but
  not where, and the hosts only appeared in a toast after pressing. The strip
  under the page panel names them — **Saved cookie: youtube.com**, **Saved
  cookies: youtube.com, github.com**, with **+N more** after the first three —
  built from the same jar records the count is, sorted by cookie count with ties
  by name. A cookie scoped to a domain is named by that domain (a `.google.com`
  cookie reads `google.com`) and a `www.` prefix is dropped. An empty jar says
  **No saved cookies** instead of leaving the strip blank, and the strip's height
  is reserved for the session so a page does not reflow when a login arrives
  ([player/browsercookies.go](player/browsercookies.go),
  [player/browser.go](player/browser.go),
  [player/textbrowser.go](player/textbrowser.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-them),
  `player/browser_cookie_sites_test.go`).
- **The Package Manager's rows are half their old height.** Each row drew the
  name on one line and version · author on a second one beneath it, which
  doubled the pitch and still left the right half of the row empty. A row is now
  one measured line — name on the left, version · author right-aligned and
  capped at two fifths of the row so a long author cannot squeeze the name out —
  so the 516px pane holds roughly twice as many packages (13 instead of 7 with
  the default metrics) and no longer wastes a row's worth of width. The pitch
  still comes off the loaded fonts and the click handler shares it, so a tap
  cannot land on a differently paced row ([player/packages.go](player/packages.go),
  `player/packages_test.go`).
- **The Wi-Fi picker's connected row now reads the same signal chain as the
  corner meter.** Every row used to draw from the scan table's RSSI, which for
  the network the device is actually on can be missing, stale, or a driver's
  `0` — one thinly-filled bar on an image without `/proc/net/wireless`, while
  the meter beside it showed a real level. The **CONNECTED** row now draws the
  meter's reading (the `/proc/net/wireless` → `wpa_cli signal_poll` chain, via
  the same `statusWiFiSignal` seam), and the other rows keep their own scan
  values, because they are not the link the device is on. A meter that reports
  "unknown" or "no link" leaves the scan value in place rather than drawing a
  state the row has no room to explain ([player/wifi.go](player/wifi.go),
  [player/wifi_ui.go](player/wifi_ui.go),
  [docs/WIFI.md](docs/WIFI.md#where-the-number-comes-from),
  `player/wifi_row_bars_test.go`).

### Added

- **Video snaps can be switched off from Settings.** The library's preview box
  was gated by the `gameSnaps` key in `jukauser.json` and nothing wrote it, so
  the only way to stop the preview moving was to hand-edit the file. Settings →
  General now carries **Video snaps · On/Off** beside the Swap A/B toggle: a
  press flips the key, saves it through the usual debounced writer and stops a
  preview that is already playing on the next frame. The row is a button rather
  than a toggle element because the feature is on by default — a toggle would
  have shown "off" for an unset key while previews were playing. An explicit
  `"false"` string (what a hand-edited file or a toggle element writes) is now
  read as off instead of being ignored, and the newly injected row also moved
  **Replay tour**, which sat exactly on top of the screensaver row
  ([player/snappreview.go](player/snappreview.go), [player/core.go](player/core.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#video-snaps),
  `player/snappreview_setting_test.go`).
- **The first-run tour and the controller legend now point at the Toolbox and
  Security & trust pages.** Both live behind Tools, which is fine once you know
  that and invisible if you do not. The tour gains a last step that says what
  each page is for, reports the live install count and posture summary beside
  it, and whose button opens the Tools panel — the tour steps aside while the
  panel is up and returns to the same step when it closes. The controller legend
  (shown once when the tour finishes, and any time from Quick Settings → **?**)
  gains a **Find it in Tools** section with the same two pages, because neither
  has a key of its own to list as a binding ([player/onboarding.go](player/onboarding.go),
  [player/controlslegend.go](player/controlslegend.go),
  [docs/TOUR.md](docs/TOUR.md), `player/onboarding_test.go`,
  `player/controlslegend_test.go`).
- **Three network scanners in the Toolbox, behind a one-time prompt.**
  `naabu` (open TCP ports), `httpx` (which hosts answer HTTP, with status,
  title and server) and `dnsx` (DNS records) install and run like the rest of
  the catalog — official releases, allowlisted repo, typed arguments, no shell —
  and are the only entries whose job is to send traffic to other machines. The
  first run of any of them does not start: a toast says to scan only networks
  you own or may test, and a second press records `networkScanAuthorized` in
  `jukauser.json` and runs it. After that every scanning tool runs immediately,
  and deleting the key brings the prompt back. Their rows always carry the
  warning, and their default run is `-version` because `httpx` and `dnsx` read
  stdin when given no arguments. Installing is not gated — only running
  ([player/toolbox.go](player/toolbox.go),
  [player/powertools.go](player/powertools.go),
  [docs/TOOLBOX.md](docs/TOOLBOX.md#network-scanning),
  `player/toolbox_scan_gate_test.go`).
- **Saved browser cookies can expire on their own.** Keeping the session is the
  point of `data/browser-cookies.json` — the launcher restarts every time the
  handheld sleeps — but keeping it forever also keeps a consent gate you cleared
  once for a page you will never open again. Settings → General → **Saved
  cookies** now cycles **Keep** (the default), 7, 14, 30 and 90 days, and a
  cookie first stored longer ago than the window is not restored. Choosing a
  window prunes what it already makes stale straight away, so the count on the
  Browser's Forget chip never promises more than the setting keeps, and the
  press reports what it removed. Age is measured from a per-cookie `firstSeen`,
  not from the last refresh, so a site that re-issues its token every visit does
  not look new forever; a file written before that field existed keeps
  everything on the first launch and starts its clock there. The row lives on
  the Settings page in memory only, like the screensaver row beside it, so no
  shipped config is rewritten ([player/browsercookies_retention.go](player/browsercookies_retention.go),
  [player/widgets.go](player/widgets.go),
  [docs/BROWSER.md](docs/BROWSER.md#dropping-them-after-a-while),
  `player/browsercookies_retention_test.go`).
- The Settings row for the screensaver timeout now renders its value. Its label
  was `$screensaverTimeoutLabel`, but substitutions only read the custom
  variable block, so the page showed the placeholder itself; the derived label
  is now seeded there at injection and refreshed by every change
  ([player/widgets.go](player/widgets.go), [player/screensaver.go](player/screensaver.go)).
- **The Browser toolbar now says how many saved cookies are on the card.**
  The count only appeared in the toast after pressing **Forget**, so the one
  fact that answers "am I still signed in somewhere?" was hidden behind the
  action that discards it. The chip reads **Forget 3** while three cookies are
  saved (capped at **Forget 99+** so the label always fits its share of the
  row), falls back to plain **Forget** when the jar is empty, and is refreshed
  as the page is drawn — so a login that just set a cookie shows up without
  leaving the screen. Nothing new is read from the card: it is the same jar the
  browser already holds ([player/browser.go](player/browser.go),
  [player/textbrowser.go](player/textbrowser.go),
  [docs/BROWSER.md](docs/BROWSER.md#forgetting-them),
  `player/browserforgetchip_test.go`).
- **The Games Library can play a video snap for the focused game.** Box art
  tells you what a game is; a few seconds of motion tells you whether you want
  it. Drop clips in `data/game-snaps/<System>/<rom name>.mp4` (or `.webm`) —
  the same tree shape as `data/game-art/`, so an existing collection copies
  straight across — and the preview box plays one while the highlight rests on
  that game. Nothing is downloaded and no scraper account is involved. The
  delay (about a third of a second) is the feature: holding **Down** through a
  long list must not start a decoder per row. The decode is its own ffmpeg
  process at 320×180 and 10fps, muted and looped, so it never touches the video
  player's session, the queue or the now-playing dock; closing the panel,
  moving the focus or opening the player stops it. The clip replaces the cover
  in the same box and carries a **SNAP** chip so it is not mistaken for
  animated art, and the cover comes back when there is no frame yet or the clip
  belongs to another game. A clip that produces no frames is remembered for the
  session and never retried. `gameSnaps: false` turns it off and
  `gameSnapsDir` points at a collection stored elsewhere
  ([player/snappreview.go](player/snappreview.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#video-snaps),
  `player/snappreview_test.go`, `player/snappreview_render_test.go`).
- **The Browser keeps a session across a relaunch**, and there is one press to
  drop it. A page that handed out a cookie — a consent gate, an age gate, a
  redirect that only sticks once the session is sent back — used to start signed
  out every time the launcher restarted, which on this device is every sleep.
  The jar is now written to `data/browser-cookies.json` (`0600`, one
  `schemaVersion`, one `savedAt`): per cookie only the host, optional `Domain`,
  path, name, value, the `Secure`/`HttpOnly` flags and the expiry — no
  addresses, no titles, no visit times. Session cookies are saved on purpose,
  because that is the thing being fixed; expired cookies are never written and
  are dropped on load; a page's own sign-out removes it from the file; the list
  is capped at 256 records, sorted so the truncation is deterministic; and a
  corrupt file costs the session and nothing else. The new **Forget** chip in
  the Browser answers "how do I get rid of this": first press arms it and a
  toast names the count, a second press within five seconds empties the jar,
  deletes the file and reloads the page, and an empty jar deletes the file
  instead of leaving it. Clearing the page cache still leaves cookies alone
  ([player/browsercookies.go](player/browsercookies.go),
  `player/browsercookies_persist_test.go`).
- The **Wi-Fi meter in the status corner now tells the truth about a missing
  link**. Four hollow bars were the "no Wi-Fi" state, and hollow bars read as a
  *weak signal* — the opposite of what they meant. The meter has three states
  now: solid bars for a measured link quality, hollow bars outlined in the accent
  for a link whose strength the kernel does not report (connected, unknown), and
  hollow bars with a slash through them when there is no wireless link at all.
  The slash is warning-coloured when nothing else is keeping the device online
  and muted when something is, because the mark is about Wi-Fi while the `!
  Offline` text beside it is about the internet. The reader also stopped
  inventing a level: an interface that is up without a readable quality used to
  report two bars on every link nobody could measure, and now reports *unknown*
  ([player/statusbar_status.go](player/statusbar_status.go),
  `player/statusbar_status_test.go`).
- The Browser reads more of the CSS real sheets are written in, and draws the
  parts it read correctly. **Borders now belong to the edge the sheet named**:
  `border-right` used to set the *left* flag, so a sidebar divider appeared on
  the wrong side, and `border-left` painted a fixed grey 3px bar regardless of
  `border-left: 4px solid #c00`. Each edge now keeps its own colour (including
  the colour carried by a shorthand), its declared weight (`1px` stays a
  hairline, `4px` stays heavy, `border-width` and the per-edge widths count), and
  `border-right` has a bar of its own. An `<hr>` keeps its styling instead of
  always drawing the reader's grey rule. The **`font` shorthand** is read
  (`font: italic 700 18px/1.8 Georgia, serif` sets the size, weight, style and
  leading at once; a bare number is a weight, never a size), **vertical and
  logical spacing** reaches the layout (`padding-top`/`padding-bottom`,
  `margin-inline`, `padding-block`, `margin-inline-start`, ... — `padding-top:
  24px` used to do nothing at all), and **stylesheets are applied for the right
  medium**: a width or feature `@media` block still applies, a block that only
  targets `print` or `speech` no longer does (a print sheet hides navigation
  with `display: none`, so the page arrived with its header missing), and
  `@supports`/`@layer` blocks now count, because this reader implements the
  properties they gate ([player/webpage.go](player/webpage.go),
  `player/webpage_css_test.go`).
- The **Settings** menu is a launcher page, the same treatment the Tools menu
  got. It was the last screen still drawn as the fixed grid it was authored in —
  four wide buttons with no icons, no title and no grouping — next to a Home
  grid, an Apps grid and a Tools page that all read as one shell. It now opens
  with the shared page header (Settings glyph, title, subtitle) and groups its
  rows by what they change: APPEARANCE, SOUND, CONNECTIVITY, SYSTEM, each with
  its own glyph chip. Rather than a second copy of the layout, a
  `menuPageProfile` now describes a menu — its sections, how a row is sorted
  into one, the glyph each row and heading gets, the subtitle and the header's
  status read-out — so Tools and Settings are two profiles over one layout, and
  the next menu is a profile too. Two measuring fixes came with it: columns are
  capped at the widest section (a sparse page no longer spreads two tiles across
  four columns with half of every row empty), and the exact section and gap
  height is counted instead of approximated, which is what lets a short page be
  centred in its panel rather than heaped at the top while a tall one still
  cannot run over the footer
  ([player/menutiles.go](player/menutiles.go), `player/menutiles_test.go`,
  `player/launcher.go`).
- Two shared components, and the three screens that were drawing them by hand
  moved onto them. `UIPageHeader` is the page bar — optional glyph well, title,
  subtitle and a right-aligned status read-out, with a centred form for the
  About brand block — and `uiPageHeaderMetricsFor` is the one place that says
  how tall the band is, so a layout reserving room for the header and the draw
  filling it cannot disagree. The Tools overlay, the Tools page and About each
  had their own copy with their own offsets, which is why the two Tools titles
  differed by 4 px. `UICategoryTile` is the tile recipe (card, tinted icon well,
  two-line fitted label, status dot, hover and focus ring, press dip) and has a
  raised-surface variant for tiles standing on a panel; the Tools overlay and
  the Tools page both use it. The section heading the two Tools screens
  duplicated is now `UISectionLabel`
  ([player/components.go](player/components.go), `components_test.go`).
- CSS gradients in the text-browser are painted as real multi-stop ramps. Every
  gradient used to collapse to the average of its stops, which turned a header's
  light-to-dark sweep into one flat band and made a body gradient invisible.
  `linear-gradient()` is now parsed into positioned stops — `#1e3a8a 0%,
  #3b82f6 100%`, evenly spaced three-stop lists, `to top`/`to left` mirroring,
  and `deg`/`turn`/`grad` angles — and drawn band by band (one band per row on a
  small box, at most 96 on a tall one) for block backgrounds, absolutely
  positioned cards and the page's own paper. A radial or conic gradient is not a
  ramp and keeps the averaged fill, the average is still stored for text
  contrast and opacity, and inline run backgrounds stay flat
  ([player/webpage_css.go](player/webpage_css.go), `player/webpage.go`).
- A **SCROLL** / **LINKS** chip in the web browser's toolbar. Switching the D-pad
  between scrolling and walking links only told you about it in a toast, so once
  that faded the page gave no sign of which mode the D-pad was in. The chip reads
  the live mode, turns accent-coloured while links are the target, and is a tap
  target of its own for touch. On a narrow panel it is the last control to be
  dropped, after Search and the filter counter, because it is the only one that
  explains the D-pad
  ([player/textbrowser.go](player/textbrowser.go), `player/browser_test.go`).
- Per-button remapping in the Accessibility panel. The physical → logical keymap
  has been applied before dispatch for a while, but only the layout preset and
  the Select row could reach it: remapping L1, R1 or a face button meant editing
  `a11y_keymap` by hand on the card. A, B, X, Y, L1 and R1 now each have a row
  that shows the action the button currently sends — preset included — and walks
  the list left and right, ending at **Disabled**, which swallows the press. The
  last thing touched wins: changing a face button folds the Nintendo preset into
  an explicit entry so one rule describes it, and applying a preset clears the
  individual rows. Select and Start keep their single paired row on purpose, so
  they cannot be stranded
  ([player/a11y.go](player/a11y.go), `player/a11y_keymap_test.go`).
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
  including links to an Android-on-A133P study that was later removed; current
  engine status is documented in [docs/ANDROID.md](docs/ANDROID.md).
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

- Android engine work: AndroidPak launches gained a memory pre-flight, and
  AndroidRT gained a host-testable probe for the GBM+EGL surface stack
  (`player/androidpak.go`, `player/androidrt/src/surface.c`). Current scope and
  hardware-validation status are in [docs/ANDROID.md](docs/ANDROID.md) and
  [player/androidrt/README.md](player/androidrt/README.md).

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

### Added

- **Tools → Toolbox: install popular command-line tools on demand, and run them
  from the app.** `rclone` (cloud/SSH sync), `jq` (JSON), `ripgrep` and `fd`
  (search), `pandoc` (document conversion) and `age` (encryption) are fetched
  from each project's own GitHub releases into `required/`, then run with
  visible output: **A** installs the first time and runs afterwards, **X** opens
  the on-screen keyboard for arguments (quotes group words; there is no shell,
  so `;`, `|`, `*` and `$()` are literal), **Y** copies the installed path.
  Installed tools are also on `PATH` for Power tools → Script snippets, so any
  of them can become a button. A download can only come from a repo listed in
  `allowedToolRepos`, assets are matched on stable name parts (never a version)
  and checksum/signature side-cars are skipped, so a token match cannot fetch a
  `.sha256` by mistake
  ([player/toolbox.go](player/toolbox.go),
  [player/tools.go](player/tools.go)).
- **Tools → Security & trust** reports the device's posture without a PC:
  plaintext secrets, the storage key in use, package count, listening FTP/SFTP
  servers, the browser session on the card and telemetry state — each row naming
  the mechanism behind it, with [SECURITY.md](SECURITY.md) as the long form. The
  Tools row carries an amber dot while something is worth reviewing
  ([player/toolbox.go](player/toolbox.go)).

### Fixed

- **The Wi-Fi picker reads the right adapter when Windows reports several.**
  `netsh wlan show interfaces` lists the physical NIC alongside the software
  adapters that share the radio — the Wi-Fi Direct one arrives as "Local Area
  Connection* 10" — and their order is not stable across a resume or a driver
  reload, so taking the first block could report "no network" on a machine that
  was already connected, and point the status meter at an adapter that never
  associates. Selection is now explicit (`pickNetshIface`): the linked adapter
  wins, a real NIC beats a virtual one, the stronger signal breaks a tie, and the
  first block is only the last resort. The status and signal reads re-read the
  report for a fresh association and match it back by adapter name, so the panel
  cannot start reading a different adapter mid-session when the ranking between
  two connected adapters flips. ([player/wifi_windows.go](player/wifi_windows.go),
  [docs/WIFI.md](docs/WIFI.md); tests in
  [player/wifi_windows_test.go](player/wifi_windows_test.go)).

- **The Package Manager shows which package is selected.** The cursor was a
  50-alpha accent wash plus a 3px spine, and the list's pane border was drawn in
  that same accent — so on a dim panel the screen read as "one card is slightly
  lighter" with an accent frame around everything. The focused row now carries
  the same focus ring the rest of the app's overlay panels draw, in the colour
  that survives a theme whose accent sits close to the row surface, with its
  version/author line stepped up a tone; the two panes take the neutral border
  every other panel uses, so the ring is the only accent outline on the screen.
  Rows are also clipped to the list column, and the shared row renderer
  ([player/components.go](player/components.go)) gained the ring for every list
  that uses it ([player/packages.go](player/packages.go),
  `player/packages_test.go`).
- **The Wi-Fi meter shows a real level on images without `/proc/net/wireless`.**
  A stock kernel built without wireless extensions — or a driver that lists an
  interface with quality `0` while associated — left the corner meter stuck on
  *unknown* for the whole session. It now falls back to the supplicant's own
  `wpa_cli signal_poll` RSSI and maps it onto the same four bars
  ([player/wifi.go](player/wifi.go),
  [player/statusbar_status.go](player/statusbar_status.go)). The poll runs off
  the render thread and caches its answer for the same four seconds as the
  kernel read, so a hung supplicant can never freeze the UI; a non-negative
  `RSSI` (the `0` a disconnected supplicant prints) is not treated as a
  measurement, and a failed poll clears the cached level instead of leaving a
  stale one on screen. **Tools → Wi-Fi signal** reports through the same chain
  and names the source it used.
- **The plaintext-secret warning never fired.** `isPlaceholderSecret` kept an
  empty string in its placeholder list, and `strings.Contains(s, "")` is true
  for every string — so every API key looked like a placeholder and
  `ConfigValidator.SecurityCheck` reported no plaintext secret at all. The
  posture page surfaced it; the empty value is now handled before the substring
  loop, and `TestPlaceholderSecretKeepsTheWarningAlive` pins both directions.
- **The handheld build no longer loses apps to a stale scene list.**
  `trimui/make-config.ps1` carried a hand-maintained whitelist of "scenes that
  stay on the handheld build". It had gone bad in both directions: it named
  `Emulator`, which no longer exists, so the script threw before writing
  anything; and it omitted `JukaLand`, `LiveTV`, `Podcasts`, `Tickers`,
  `Weather`, `UnitConverter`, `CanvasSandbox`, `IPStream` and `MusicPlayer`. The
  committed device config was also a hand edit, not the script's output: its
  Main carried the old six tiles *and* the eight new ones stacked on each other,
  and it kept a Settings row the script was supposed to remove. Those nine
  scenes are rows in the Apps launcher (`builtinApplets`), and
  `builtinAppEntries` hides a row whose scene the config does not ship — so the
  handheld silently lost nine working apps with no error anywhere. The script
  now keeps every scene verbatim and applies only the device defaults; trimming
  is an explicit `-Drop` list that refuses to remove a scene a kept scene still
  opens, the whole run fails if any `change_scene:` target is missing, and
  `TestDeviceConfigShipsEveryBuiltinApplet` guards the committed file
  ([player/trimui/make-config.ps1](player/trimui/make-config.ps1),
  [player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json)).
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
