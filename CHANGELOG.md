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

- **Juka talks.** The voice orb says Juka's answers out loud — and the time,
  the date, the battery and the help line — and keeps its bubble up until it
  has finished; any button stops it. **Settings → Voice & AI** (a new page)
  picks when (the voice assistant, by default; Chat's AI tab too; or never)
  and which of Groq's six Orpheus voices, and **Hear Juka** tests it. Groq's
  free voice allows a few minutes a day, so only an answer's first 600
  characters are spoken, a sentence at a time; without it (no key, offline,
  the allowance used up, its one-time terms not accepted — Settings links
  them) a PC speaks with Windows' own voice and the handheld keeps the answer
  on screen. Music and video duck while Juka talks (`player/tts_*.go`,
  `player/voiceai_settings.go`, `player/voiceorb.go`, `player/sound.go`).
- **Teach the voice assistant your own words.** **Settings → Voice & AI →
  Voice shortcuts** (or the Voice command page's **Shortcuts**, or "open voice
  shortcuts"): say or type a word, pick what it opens — any screen, setting,
  app, game, file or emulator, a voice command such as "brightness 10", or a
  web page — and save. The orb, the Voice command page and the phone then put
  it before the built-in commands ("cancel" and "help" excepted); up to 50
  words (`player/voiceshortcuts.go`, `player/voiceshortcuts_ui.go`).
- **Emulators and their menus by voice.** Every tile of the Emulators page is
  a voice target ("open PPSSPP", "the GBA emulator"), as are "RetroArch menu",
  "control profiles" and "update emulators": RetroArch, a PC emulator and
  JukaHub's own PPSSPP start without a game, on their own menu; other tiles
  open the Emulators page on that tile, and nothing is installed by voice.
  "Open the emulators" used to land on the Settings row of that name
  (`player/voiceemu.go`, `player/voicecmd_resolve.go`).
- **Ask Juka about the game you're playing.** With **Settings → Voice & AI →
  Game assistant** on (it is off by default), Juka is told which library game
  is running, or ran in the last half hour, so "how do I beat this boss?" in
  Chat or the orb means that game. On a TrimUI, hold **Select + R2** in a
  game: the game pauses, its picture dims behind the voice orb and Juka
  listens; **A** asks again, **B** or **Menu** goes back to the game, which
  also resumes by itself after three minutes. **Juka can see the game screen**
  sends the paused picture with the question. Nothing runs during a game but a
  small button watcher (`player/gameassist*.go`, `player/main.go`).
- **Juka looks at pictures and videos.** In the Juka AI tab a picture goes to
  Groq's vision model (Qwen 3.8; Muse for more than three), a video as three
  frames plus what is said in its first ten minutes, a recording as its words;
  follow-ups keep them, the bubble says "Watching the video…" while ffmpeg
  works, and HEIC and other formats nothing here opens are refused when
  attached (`player/groqvision.go`, `player/vision_chat.go`,
  `player/vision_video.go`, `player/chatsync.go`).
- **R2 is the voice typing button.** With the on-screen keyboard up, R2 starts
  listening and the next press sends — in the browser's address bar too; the
  footer says "R2 Voice" (F4 on a keyboard). **Settings → Voice & AI → Start
  voice typing when the keyboard opens** listens as soon as a Chat message's
  keyboard opens and stops quietly when nothing is said; passwords, keys,
  addresses and searches never listen (`player/vkvoice_button.go`,
  `player/vkvoice_settings.go`, `player/controller.go`).
- **JukaHub shows how much memory it uses.** The Hardware screen's Memory tile
  and About's System card read "JukaHub N MB · M MB free" (resident memory on
  Linux, the working set on Windows), and the log gets a memory line a minute
  in and every five minutes after (`player/memstat*.go`,
  `player/systool_hw.go`, `player/about.go`).
- **Quick Settings opens the Bluetooth page.** A **Bluetooth Devices** row sits
  under Wi-Fi Networks wherever `bluetoothctl` is present, and Home search
  finds the page for "connect headphones", "bluetooth speaker", "keyboard" and
  "mouse" (`player/quicksettings.go`, `player/homesearch_index.go`).
- **The Smart Home Tailscale row names the device's address and its peers.**
  `tailscale status --json` now yields each node's name, address and online
  state, listed in a stable order beside this device's own address
  (`player/smart_home.go`, `player/smart_home_ui.go`).
- **Customized built-in elements work in the web DOM.** `customElements.define(name, cls, { extends: "button" })` now upgrades `<button is="name">` from parsed markup, `innerHTML` and `createElement(tag, { is })`, instead of registering a definition that never ran (`player/webdom/20_nodes.js`, `player/webdom/25_elements.js`, `player/webdom/50_shadow_ce.js`).
- **A typed console address is listed while it pairs.** Adding a console by
  address now shows an unsaved row with the busy line during the PS4 and PS5
  handshakes, and a failure stays listed until the panel closes
  (`player/psremote_ui.go`).

### Changed

- **The liquid background moves smoothly.** The water is drawn between its
  30 Hz steps, on a mesh twice as fine as its grid with smooth heights and a
  linearly filtered wallpaper, so ripples no longer jump in 33 ms steps or show
  facets and blocky pixels, and while it moves it asks for the active frame
  rate instead of idling down to about 15 fps — 30 fps on the handheld, also
  under Low Power (its factory setting); Battery Saver keeps 24. A playing GIF
  wallpaper and the living background ask for the frames they need the same
  way (`player/liquidbg.go`, `player/gifwall.go`, `player/adaptive.go`).
- **Back online faster after the handheld sleeps.** The TrimUI firmware turns
  Wi-Fi off for every sleep; JukaHub now notices the wake within a second and
  checks the internet every 1.5 s until it answers, instead of on its next
  20-second tick, nudges a slow radio (reconnect, reassociate, a DHCP renew)
  without starting anything behind the firmware's back, and reopens the
  community chat at once so Discord replays what was missed. Meanwhile Chat
  says "Reconnecting to Wi-Fi…", a question for Juka waits and goes out the
  moment the network is back, and the orb and voice typing hold a clip for up
  to 15 s; one log line per wake says how long it took
  (`player/wake_resume*.go`, `player/wifi_resume*.go`, `player/netmon.go`,
  `player/chatsync.go`).
- **Starting with the device is one hand-over.** When the boot hook has to
  close the stock menu, the screen goes black as it closes and JukaHub's boot
  fades in from there, instead of freezing on the menu and then jumping to the
  boot logo; a firmware that reopens the menu instead of starting JukaHub sees
  it once less (`player/autostart.go`, `player/bootos.go`).

- **A time zone picked in Settings reaches games without a restart.** Every
  game child's `TZ` is replaced by the zone JukaHub is using now, so a new zone
  applies to the next launch; the Date & time row no longer says a restart is
  needed (`player/childenv.go`, `player/gameprofiles.go`, `player/ports.go`).

- **The config watcher sleeps while the Live Builder is unpaired.** On the
  handheld the two-second poll now runs only while a builder session is up, so
  an idle speaker stops statting the config; a push still reloads through
  `NotifyWritten`, and pairing takes a fresh baseline instead of replaying what
  changed meanwhile (`player/hotreload.go`, `player/livebuilder.go`).

### Fixed

- **Leaving JukaHub no longer leaves CrossMix glitchy.** `launch.sh` started a
  second stock menu when the firmware took more than ten seconds to bring its
  own back, and both drew to the screen; it now waits for the firmware's
  launcher while it is alive and closes its own if the firmware's appears. It
  also pauses and resumes the launcher loop by process id (where busybox names
  every script "sh", pausing "runtrimui.sh" by name found nothing), records
  what it paused in `/tmp/jukahub-paused`, starts only one JukaHub, and puts
  the CPU governor and limits back after any exit, a kill or a crash included.
  For a minute after starting and after each game JukaHub logs any MainUI or
  other program drawing beside it (and stops a stray MainUI), and as it quits
  it lists what is still running (`player/launch.sh`, `player/autostart.go`,
  `player/displayguard_linux.go`, `player/exitguard.go`).
- **A video that would not stream now really downloads, and shows it.** The
  fallback asked YouTube only for a single muxed file, which it no longer
  gives a handheld without a JavaScript runtime, and its progress was never
  drawn, so the card spun on "Loading…" until it failed. It now joins the
  separate picture and sound with ffmpeg, tries the HLS copies and the muxed
  file next and finally fetches the stream in 10 MB ranges without yt-dlp; the
  card shows the percentage, speed and time left (**B** cancels), a slow
  download runs while data keeps coming instead of stopping at five minutes,
  cancelling stops yt-dlp's whole process group, and one toast says why when
  nothing worked (`player/ytdownload.go`, `player/ytdownload_ui.go`,
  `player/loadingcard.go`, `player/streaming.go`, `player/videorecover.go`).
- **Saving YouTube videos uses yt-dlp's own clients.** The Downloads queue
  forced the android and web clients (with a malformed second argument), which
  YouTube refuses without tokens the handheld cannot make
  (`player/downloads.go`).

- **Labels cannot print a protected setting.** `$name` substitution leaves
  protected names and `ENC:` values as their placeholder, so a design label
  cannot show a token in a screenshot or the companion's screen view
  (`player/core.go`).

- **Long About values stay inside their cards.** Build and credit values are ellipsized against the width their label leaves, so a long platform or device name cannot run through the card border (`player/about.go`).
- **Remote Play uses the home address only when this console answers there.**
  The home-network probe compares the discovery answer's host-id with the
  pairing's MAC, so another PlayStation at the same private address counts as
  not at home and the internet address is used
  (`player/psremote_stream_opts.go`).
- **PS Remote Play's console options cannot overwrite a pairing saved at the
  same moment.** `psUpdateConsole` and `psSaveStreamSettings` load and save
  `psremote.json` under one lock, and a two-writer test now holds both to it
  (`player/psremote_writers_test.go`).
- **The Phone Companion's PIN budget is per address.** A paired phone polling
  on its own address no longer clears a guesser's failures and lockout, and a
  success clears only its own address's budget (`player/companion.go`,
  `player/companion_cast.go`, `player/psn_signin.go`, `player/voicecmd_phone.go`).
- **A clock jump cannot replay a scheduled task's missed runs.** The
  scheduler's run decision is jump-aware: a forward jump collapses everything it
  passed into a single run and records the last of them, and a backward jump
  cannot re-arm a run already handled (`player/clockjump.go`,
  `player/sched_model.go`).
- **A cross-origin script's error no longer leaks its text or address.** A
  classic script served by another origin reports `Script error.` with an empty
  filename and a zero line and column, while the debug log keeps the real text
  (`player/webdom/85_scripts.js`).

- **An x86-64 PC no longer asks for Box64 to run its own Wine.** A Wine on
  `PATH` is used as before; without one, Setup's first row installs the same
  Wine 10.0 build natively, pinned to its published SHA-256
  ([player/gameprofiles.go](player/gameprofiles.go),
  [player/wine_pack.go](player/wine_pack.go)).
- **The Smart Pro no longer installs Hangover, which its firmware cannot
  load.** Hangover needs glibc 2.36 and the Smart Pro has 2.33: an existing
  copy the firmware refuses is removed (about 2.5 GB) before the new runtime is
  unpacked, and its wrapper no longer counts as an installed Wine. Hangover is
  opt-in now (`JUKAHUB_WINE_FLAVOR=hangover`), HTTPS only, and its download is
  no longer cut off after 30 seconds
  ([player/windows_runtime_install.go](player/windows_runtime_install.go)).
- **Runtime archives unpack on exFAT and FAT cards.** Hard links inside an
  archive are copied, and a link the card cannot hold is skipped and logged
  instead of failing the install; each install still checks its essential files
  afterwards
  ([player/windows_runtime_install.go](player/windows_runtime_install.go)).
- **A WestonPack that resets every device is refused.** CrossMix's preview
  build runs a bare `udevadm trigger`, after which the stock firmware unmounts
  the SD card JukaHub runs from; JukaHub refuses it with the reason, no longer
  counts it as installed, and Setup fetches the official build
  ([player/wine_display.go](player/wine_display.go),
  [player/wine_runtime.go](player/wine_runtime.go)).
- **An empty or broken `Apps/AndroidRT` no longer hides the AndroidRT that
  ships with JukaHub.** A folder counts only when its loader can start on this
  firmware (CPU and glibc checked as for emulator cores) or it has `launch.sh`;
  the folders passed over are named, and a loader that lost its execute
  permission is repaired before launch
  ([player/androidpak.go](player/androidpak.go),
  [player/androidrt/launch.sh](player/androidrt/launch.sh)).
- **xemu installs can no longer take the debug build.** Debug builds and symbol
  files are never picked, xemu's versioned asset names are matched beside the
  old ones, and a Windows-on-ARM build of JukaHub takes xemu's ARM64 build
  ([player/emuwininstall.go](player/emuwininstall.go)).
- **A final release beats its release candidate.** Updates read `0.1.3-rc1` as
  newer than `0.1.3` and offered it; release numbers now come first and a
  pre-release ranks below its final release, for hakuX and every Windows
  emulator ([player/emuwininstall.go](player/emuwininstall.go),
  [player/hakux.go](player/hakux.go)).
- **hakuX housekeeping (XBX-6).** R1 no longer freezes the screen while it
  restores files; files a newer package dropped move into the kept copy, and R1
  puts them back, instead of lingering in `Data/`; the launcher a folder's
  `config.json` names is honoured; and a different release that is not newer no
  longer "repairs" the install ([player/hakux.go](player/hakux.go),
  [player/hakuxpage.go](player/hakuxpage.go)).
- **Install failures say what went wrong.** A damaged or cut-off download, full
  storage, an unusable archive or a clock that breaks the secure connection
  (the TrimUI loses its time) is named in plain words for every emulator
  install ([player/emuwininstall.go](player/emuwininstall.go)).
- **Rolling back waits for a running update.** R1 on a Windows emulator, hakuX
  or a core, and a kept build on the Core builds page, now take the lock
  installs hold, so a rollback can no longer mix two versions in one folder;
  meanwhile they say "An update is running — roll back when it finishes", and a
  Windows emulator's rollback runs off the screen thread
  ([player/emuwininstall.go](player/emuwininstall.go),
  [player/emulatorinstall.go](player/emulatorinstall.go),
  [player/corebuilds_ui.go](player/corebuilds_ui.go)).
- **The Core builds page no longer says "Auto-update on" on a Linux desktop.**
  Nothing updates by itself there
  ([player/corebuilds_ui.go](player/corebuilds_ui.go)).
- **Starting JukaHub no longer hands out trophies.** Every start awarded the
  same dozen at once — Warming Up, Settling In, Came Back, High Five, Back to
  Back, Triple Feature, Long Haul, Weekly Check-In, Look Around, Homebody and
  Home Owner, plus the collection rungs they pushed you past — because an old
  save's history already met them. Those eleven are gone, with the three
  "Start JukaHub N times" trophies and the 375 rung the cut left out of reach:
  386 trophies now. What a loaded save already earns is recorded quietly — no
  card, no notification row, no count on the trophy cup — and a save's unlocks
  of the retired trophies are dropped, with any collection rung only they had
  made up ([player/trophy_set2.go](player/trophy_set2.go),
  [player/playtime.go](player/playtime.go),
  [player/header_trophy.go](player/header_trophy.go)).
- **What changed in a session survives the restart.** A favourites or chat
  save wrote the user settings as they were read at boot (the cache keeps them
  for its sections) over everything saved since — every five seconds while a
  video played, and at every exit right after the playtime was saved — so
  jukauser.json went back to its boot state: settings, recent searches, the
  trophy cup's record and the trophies themselves, which the next start then
  awarded again. A section save now writes the newest full save's settings and
  never takes the debounced slot from a full save waiting there; the LED panel,
  the telemetry switch and the Power tools sync paths save the live config
  instead of the boot copy; and every save writes the Netplay switch, not only
  Quick Settings ([player/extras.go](player/extras.go),
  [player/ledpanel.go](player/ledpanel.go),
  [player/telemetry.go](player/telemetry.go),
  [player/powertools_backend.go](player/powertools_backend.go)).
- **No `(s)` plurals and no `N/A` in the UI.** Counts read "1 file" /
  "2 files" through `pluralizeCount` (BIOS Doctor, the compatibility report,
  Emulators, game profiles, Ports, Saves, Windows apps, curation, the Patch
  engine and more), a hardware reading that found nothing says "Unknown"
  (`hwUnknown`) instead of "N/A", Quick Settings rows say "Unavailable", and
  footer hints name "D-Pad", "Up/Down" and "Left/Right" instead of arrow
  glyphs (Screen Share, Clipboard, the memory tool, Music, the terminal and
  JukaLand), and the brightness, volume and onboarding footers spell them
  `Left/Right` and `Up/Down` (UX-3, UX-13). The second sweep caught the
  leftovers: the Browser's links line, the bookmarks and cookies panels,
  curation, emulator and save-manager toasts, onboarding's ROM scan, the
  terminal's skipped-line note, Syncthing's skipped-save note and the
  diagnostics report now say "3 files", never "3 file(s)".
- **The progress track follows the theme.** The track token was a hardcoded
  white 34/255 for every preset, so on the shipped Light theme the track of
  every progress bar (Download rows, the storage bar, installs, seeks, the
  activity rail) was white on a #f0f2f5 page and vanished, leaving a fill
  floating in space. `ApplyThemeColors` now re-inks the track from the
  preset's text colour at the same alpha. Two Download bars that forced the
  token to opaque white — a solid white "track" that read as a full bar at
  0% — draw the translucent token, and the legend key uses its opaque swatch.
- **A scrollbar thumb stays in its track.** The thumb geometry floored at
  24 px and offset by `h - thumbH`, so a list body shorter than 24 px drew its
  thumb above or below the track it belonged to. The geometry is one helper
  now, clamps the thumb to the track and treats a NaN fraction as a thumbless
  track instead of an undefined offset.
- **The full-screen now-playing title centres on what it draws.** The screen
  measured the raw filename, then drew the ellipsized line at that x, so a long
  filename started left of the canvas (a 2000 px title on a 1280 px screen
  began at -360) and lost its left half off the edge. The line is measured
  after truncation now, the artist/meta line is ellipsized to the same budget,
  and the placement never starts left of the screen.
- **The Downloads list stays inside its panel.** When the tabs and the storage
  card left no room, the viewport's fallback set the list bottom to
  `listTop + rowH`, past the panel's footer, so the clipped list painted over
  the card frame. A degenerate panel collapses the viewport instead.
- **Regexps compile on first use.** The last package-level patterns in
  `package main` — Chat's links and mentions, label substitution, the APK
  store's ID and digest checks, bluetoothctl parsing, disc and PPSSPP version
  parsing, the theme and Xbox asset checks and the per-track rip match — are
  `sync.OnceValue`, so boot no longer compiles patterns a session may never
  reach (PERF-16).
- **Attribute values serialize like a browser's.** The Browser's DOM prelude
  escapes `<` and `>` in attribute values in `innerHTML`/`outerHTML`, matching
  jsdom and the current HTML serialization rules, so a value such as
  `title="<b>"` can no longer be read back as markup by a page or by the
  reader's tag scanner (found by `make check`'s WebDOM parity suite).

### Added

- **The D-pad reachability check is a test now.** `TestShippedConfigsAreDpadReachable`
  walks every scene's focus graph in both shipped designs and names any control
  a pad cannot reach, instead of relying on a manual sweep
  (`player/reachability_test.go`).
- **More console folders in the game library.** `romSystemAliases` now covers
  Arcade/FBNeo/MAME, Atari 2600/5200/7800/Jaguar/Lynx, WonderSwan and its
  Color, Neo Geo Pocket and its Color, Neo Geo CD, Sega CD, 32X, PICO-8 and
  DOS under the folder names the stock firmware, CrossMix and No-Intro sets
  use; every system with a libretro core names it in `defaultCoreForSystem`
  (`fbneo`, `stella`, `prosystem`, `mednafen_wswan`, `mednafen_ngp`, `neocd`,
  `genesis_plus_gx`, `picodrive`, `dosbox`), and the scan recognises their
  cart extensions (LIB-2).

- **Windows programs on the Smart Pro.** Windows Apps now runs `.exe` programs
  under Box64 and Wine 10.0. One press — **A** on a program, or Setup's first
  row — installs everything, with progress on the page: JukaHub's own Box64
  build (`JukaHub-Wine-linux-arm64.zip` from JukaHub's releases, checked
  against the signed manifest or GitHub's digest and its `SHA256SUMS`, and
  started once before it replaces anything), Kron4ek's Wine 10.0 WoW64 build
  for 32- and 64-bit programs (checked against its pinned SHA-256 and unpacked
  in-process; Wine 11 is refused, as it aborts on the handheld's 4.9 kernel),
  WestonPack, and a Windows folder prepared once without a display; then the
  program starts. About 130 MB is downloaded and about 700 MB of space used.
  Box64 runs with its own `box64.box64rc` and with its DynaCache off (Box64
  0.4.4 writes up to 2 GB of cache, and a stale one has crashed Wine's
  first-run setup), and the wrappers run through `sh`, so a card that drops the
  execute bit cannot stop them. Nothing from this pass — Windows apps, Android,
  Xbox, emulator updates — has run on a handheld yet
  ([player/wine_pack.go](player/wine_pack.go),
  [player/windows_runtime_install.go](player/windows_runtime_install.go),
  [docs/WINDOWS_APPS.md](docs/WINDOWS_APPS.md)).
- **WestonPack puts Windows programs on the handheld's screen.** JukaHub uses
  its own copy or PortMaster's image when the card has one, otherwise downloads
  it from PortMaster's repository and checks that it is a real SquashFS image;
  it mounts the image read-only for the program, labels input devices for udev
  so Weston starts on the stock TrimUI firmware, and cleans up and unmounts
  afterwards. **Y** on a program switches it between Windows (GDI) and OpenGL
  display, a Wine game profile can ask for `"display": "weston-gl"`, and
  `data/windows-runtime/weston.env` tries other WestonPack modes without a
  rebuild ([player/wine_display.go](player/wine_display.go),
  [player/windowsapps.go](player/windowsapps.go)).
- **The pad is a mouse and keyboard in Windows programs.** The sticks move the
  pointer, A / B / X click left, right and middle (hold A to drag), L2 / R2
  scroll, the D-pad types arrows, Y is Esc, Start Enter, L1 Tab, R1 Space and
  Select + Start Alt+F4; holding Select + Start for 2 seconds ends a stuck
  program, and Select + L1 hands the pad to the program as a gamepad and back.
  The mouse and keys stay quiet until the program has started, so they cannot
  press anything in JukaHub
  ([player/vcursor_bridge_linux.go](player/vcursor_bridge_linux.go),
  [docs/CONTROLS.md](docs/CONTROLS.md)).
- **Windows Apps on Windows.** The Start menu and the Emulators page open
  Windows Apps on a PC too: it lists the `.exe`, `.bat` and `.cmd` files in
  `%APPDATA%\JukaHub\windows-apps` and runs them directly, with nothing to
  install, and Wine game profiles run directly there and are listed as ready
  ([player/windowsapps.go](player/windowsapps.go),
  [player/gameprofiles.go](player/gameprofiles.go)).
- **AndroidRT ships with JukaHub.** Every TrimUI archive carries the
  Android-app runtime beside the binary (`Apps/JukaHub/androidrt`), where
  JukaHub finds it. Without it, the APK Store's Runtime tab installs it from
  JukaHub's releases — checked against the signed manifest or GitHub's digest
  and its `SHA256SUMS`, and kept only if its loader starts on the device — and
  offers its updates; **Play** or **Add** on a game with no runtime installs it
  with one press and then plays or adds the game (only while the store is still
  on screen). Nothing downloads without that press or the Emulator updates
  setting ([player/androidrt_install.go](player/androidrt_install.go),
  [player/relpack.go](player/relpack.go),
  [player/apkstore.go](player/apkstore.go),
  [docs/ANDROID.md](docs/ANDROID.md)).
- **Android games in the Games Library (AND-16).** Games added in the APK Store
  appear on the library's own **Android** row with the APK's label and launcher
  icon, start through the game-profile path, show on Home's Continue card and
  leave the row when the store removes them. Each APK keeps its own cover
  (games pulled from a phone are all called `base.apk`), the toast says
  "Starting" until the game is up, and a second Play while it starts says so
  instead of starting it twice
  ([player/game_library_androidgames.go](player/game_library_androidgames.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md)).
- **Xbox 360 on Windows.** The Emulators page installs Xenia Canary from its
  official releases (the digest-checked 7z; each Canary build is its own
  release, so a new build shows as an update and R1 rolls it back), and
  `Roms/XBOX360` becomes the Games Library's Xbox 360, whose games start full
  screen. A PC without the Visual C++ Redistributable gets a hint, never a
  block; on the handheld the folder says what Xbox 360 needs (an x86-64 PC with
  AVX and D3D12 or Vulkan) ([player/emuwininstall.go](player/emuwininstall.go),
  [player/emusystems.go](player/emusystems.go),
  [player/game_library.go](player/game_library.go)).
- **xemu sets itself up.** Starting xemu, from its tile or the library, writes
  the Xbox files the setup sheet found into the `xemu.toml` xemu reads, filling
  only missing or dead entries (a relative path counts from xemu's folder); a
  game started without the MCPX boot ROM, the flash ROM or the HDD image says
  which file is missing and where it goes; library games start full screen. On
  Windows the setup sheet looks at the folder again every few seconds, shows
  the running download and offers **Install xemu**, and the Games Library tells
  Xbox players to install xemu instead of hakuX
  ([player/emuxbox.go](player/emuxbox.go),
  [player/hakuxpage.go](player/hakuxpage.go)).
- **Emulator updates keep everything JukaHub installed current.** The daily
  pass (Settings → Games & emulators → Emulator updates) now brings hakuX, the
  Windows-app runtime and AndroidRT forward after the cores, never while one of
  them is in use, at the same quiet moment and under one install card and one
  toast, and stops when a game starts. On a PC it updates the emulators JukaHub
  installed (Xenia Canary included) and its own RetroArch's cores — never while
  one of them runs, under a full-screen game or within 3 minutes of input
  anywhere on the PC, and on a laptop's battery only when three days overdue —
  keeping the old version for Roll back. A build you rolled back from is not
  installed again (one rolled back before this update may come back once with
  the first pass; roll back again and it stays), and Notify me names each new
  build once and installs nothing
  ([player/emuautoupdate_runtimes.go](player/emuautoupdate_runtimes.go),
  [player/emuautoupdate_desktop.go](player/emuautoupdate_desktop.go),
  [player/emuautoupdate_hakux.go](player/emuautoupdate_hakux.go)).
- **CI builds AndroidRT for the handheld.** The Remote Play helper job builds
  AndroidRT natively on arm64 in its Ubuntu 20.04 root (glibc 2.31) and checks
  it — AArch64, nothing needed but the C library, no glibc symbol newer than
  2.31, the TLS slot NDK code reads, `loader --version` starts — and runs its
  host smoke suite there as an advisory step. `JukaHub-Trimui.zip`, the release
  script's five device archives and the SD-card package carry it as
  `androidrt/`, and each release also attaches
  `JukaHub-AndroidRT-linux-arm64.zip` (one `androidrt/` folder with
  `BUILDINFO.txt` and a `SHA256SUMS` of every file)
  ([player/native/build-runtimes.sh](player/native/build-runtimes.sh),
  [.github/workflows/remote-play-helpers.yml](.github/workflows/remote-play-helpers.yml)).
- **CI builds a Box64 for the handheld.** `wine-runtime.yml` builds Box64
  v0.4.4 in the same kind of root — fetched by the full id of its release
  commit; a generic ARM64 build without BOX32, since Wine's WoW64 build runs
  32-bit programs under Box64 alone — holds it to the same checks
  (`box64 --version` must name 0.4.4) and attaches
  `JukaHub-Wine-linux-arm64.zip` to the release: `box64`, the x86-64 libraries
  it installs for itself, its default settings and licence, and a `wine.json`
  naming Kron4ek's Wine 10.0 WoW64 build with its pinned SHA-256. An advisory
  step starts `box64 wine --version` on the runner. Neither pack has come out
  of a green CI run yet
  ([player/native/build-runtimes.sh](player/native/build-runtimes.sh),
  [.github/workflows/wine-runtime.yml](.github/workflows/wine-runtime.yml)).
- **The static checker ships with the repo.** `tools/gocheck/` holds
  `gocheck.py`, `alignchk.py` and `importchk.py` with a README, so a checkout
  without a Go toolchain runs the same syntax, scope, alignment and import
  checks the `tmp/` copy did (TEST-6).
- **A broken doc link fails `make check`.** `tests/scripts/docs-links.py`
  checks every relative link in every `*.md` outside `node_modules`/`vendor`
  and names the file and target when one does not resolve; `make docs-links`
  runs it alone (TEST-7).

- **A VPN for the whole device.** Settings → Network → VPN sends all traffic —
  games and netplay, the browser, YouTube, downloads, Remote Play away from
  home — through a WireGuard tunnel while the switch is on and a configuration
  is set. A provider's or your own server's `.conf` comes from the card
  (Settings lists the ones it finds), from **A** on it in Files, or from the
  phone companion's new VPN card (paste or pick the file). **Send through the
  VPN** is Everything (the default; the Wi-Fi network itself stays reachable)
  or only the networks the file lists; the file's DNS servers answer every
  lookup meanwhile, IPv6 cannot slip past an IPv4-only tunnel, and broadcast
  and multicast stay on the Wi-Fi so LAN play and device discovery still work.
  Quick Settings has the switch, and the Wi-Fi meter wears a lock — in the
  warning colour while the VPN is on but not connected. On the handheld
  JukaHub runs its own WireGuard in a small root process
  (`JukaHub --vpn-daemon`) that makes the `jukavpn0` interface, adds
  `0.0.0.0/1` + `128.0.0.0/1` beside the network's routes, keeps its own
  packets on Wi-Fi with `SO_BINDTODEVICE` (bound again when the network or the
  Wi-Fi driver changes), puts `resolv.conf` back even after a crash, survives
  Remote Play's screen handoff and stops with the JukaHub session; handshake
  timestamps never go backwards, so a handheld that boots in the past is not
  refused. The handshake and transport were checked byte for byte against the
  Linux kernel's WireGuard, and the routes in a network namespace
  (`tmp/wgref`, `wg_test.go`). On Windows the tunnel is WireGuard for
  Windows' service, changed only through a permission prompt naming JukaHub,
  with the key in an administrators-only folder. The configuration lives in
  `data/vpn.json`, never in `jukauser.json` or backups
  ([player/vpn.go](player/vpn.go), [player/vpnd_linux.go](player/vpnd_linux.go),
  [player/wg_device.go](player/wg_device.go), [docs/VPN.md](docs/VPN.md)).

- **Each screen names itself once.** The status bar now carries only the
  JukaHub wordmark and the system status, and page headers own the screen
  title at Heading size (22 px) or larger, Title for a big header, so
  "JukaHub - Settings" no longer repeats what the page says
  ([player/extras.go](player/extras.go),
  [player/components.go](player/components.go)).

- **One five-step type scale.** Every screen now asks for Caption, Label, Body,
  Heading or Title (14 / 16 / 18 / 22 / 28 px) instead of passing a pixel size,
  and `FontForRole` is the single resolver, so the accessibility text size and
  the legibility floor apply to all of them at once. The former literal sizes
  across the media hub, Packages, files, Wi-Fi, Tools and the rest of the UI
  are gone, and a test rejects new ones
  ([player/design.go](player/design.go)).

- **A legible reading font.** Settings → Accessibility → Legible reading font
  draws the whole UI in the best low-vision face the device already has —
  Atkinson Hyperlegible, Lexend, Verdana, Tahoma or Segoe UI, in that fixed
  order — and keeps bundled Inter when none is installed, so nothing is
  downloaded and text never disappears. Bold text follows the chosen face's
  bold cut, the choice persists with the rest of the accessibility store, and
  it takes effect without a restart
  ([player/legiblefont.go](player/legiblefont.go),
  [player/home_fonts.go](player/home_fonts.go)).

- **Switch access.** Accessibility now offers one- and two-button scanning for
  players who cannot use a d-pad: the focus walks a screen's controls on its
  own at a configurable interval, A presses the highlighted control, and a
  two-button setup adds B as an early next. It drives the same focus the pad
  drives, so every screen's ring and activation path work unchanged
  ([player/switchaccess.go](player/switchaccess.go),
  [player/focus.go](player/focus.go)).

- **A magnifier that follows the focus.** Quick Settings → Magnifier draws a
  2× or 3× lens beside the focused control; the copy is one bounded read-back
  per refresh into a single reusable texture, and the lens is placed clear of
  the picture it magnifies. Settings → Accessibility carries the same switch
  and the zoom choice, and Quick Settings now scrolls its rows so they fit a
  720-line screen ([player/magnifier.go](player/magnifier.go),
  [player/quicksettings.go](player/quicksettings.go)).

- **Accessibility is a Settings section.** Text size, contrast, transparency,
  bold text, the colour-blind palettes, mono audio and balance, captions, input
  timing, button remaps, one-handed mode, key repeat, power mode and vibration
  strength now sit on one Settings page instead of only inside the
  Accessibility panel, and the Start menu's Accessibility entry opens it
  ([player/a11y_settings.go](player/a11y_settings.go),
  [player/a11y.go](player/a11y.go), [player/setmodel.go](player/setmodel.go)).

- **Adjustable timing and sticky chords.** Accessibility now exposes the
  long-press delay, held-direction repeat delay and speed, and toast
  duration. The values feed the on-screen keyboard, menu repeat and toast
  timer directly. Sticky chords leave the one-handed chord armed after a
  direction so several face buttons can be pressed in sequence
  ([player/a11y.go](player/a11y.go), [player/navrepeat.go](player/navrepeat.go),
  [player/vkedit.go](player/vkedit.go), [player/core.go](player/core.go)).

- **Audio access and readable captions.** Accessibility now has Mono audio,
  an adjustable left/right balance for video and music, captions that start
  on, and Small/Medium/Large caption text with an optional background. The
  audio settings run in the shared PCM queue used by both the video decoder
  and the music player, and the caption settings apply in the live subtitle
  renderer ([player/a11y.go](player/a11y.go),
  [player/sound.go](player/sound.go), [player/videosubs.go](player/videosubs.go)).

- **The weekly weather card now brings each forecast to life.** Sunlight
  pulses, rain and snow fall, fog drifts, and storm clouds flash; condition
  icons get a color strip for the day's high temperature. Reduce Motion and
  Battery Saver keep the effects still ([player/weather_popover.go](player/weather_popover.go)).
- **An old video finishing no longer clears a newer play.** The embedded
  player's stop path now owns a session: a decoder that finishes after the
  next video started only stops itself, keeps its own page for the Watch
  Party dismissal, and cannot erase the new play's page or opening scene.
  The shared playback page is read and written through a mutex-guarded
  accessor ([player/videoplayer.go](player/videoplayer.go),
  [player/core.go](player/core.go)).
- **Liquid background animates as soon as it is enabled.** A gentle centered
  ripple previews the water immediately, with its depth reduced by Reduce
  Motion ([player/liquidbg.go](player/liquidbg.go)).
- **A guessed protected value always counts as a change in Live Builder.**
  Push approval now compares the design as the sender wrote it with the
  device as a pull shows it, so sending the exact device secret no longer
  gets a different answer from sending a wrong one. A placeholder still
  goes through as “keep the device's value”
  ([player/livebuilder.go](player/livebuilder.go)).
- **A waiting Live Builder design restores secrets as they are at approval time.**
  The held copy now stores `<kept on device>` placeholders instead of the
  device's tokens and commands, and accepting it fills them from the current
  config. If a kept value changed while the card waited, the waiting design
  is refused and discarded rather than putting the old secret back
  ([player/livebuilder.go](player/livebuilder.go)).
- **A kept value inside a list is restored only into the list it came from.**
  Live Builder pushes now refuse a list that was reordered or edited around
  a `<kept on device>` placeholder, instead of putting the device's value at
  a different position. A matching list still restores, and the refusal never
  quotes a kept value ([player/livebuilder.go](player/livebuilder.go)).
- **Held keys act once, except where holding means moving or editing.** The
  keyboard dispatch now drops SDL auto-repeats for actions such as Enter,
  Space, letters, Esc and function keys, while arrows, PgUp/PgDn, Home/End,
  Backspace/Delete, volume and zoom still repeat. This stops a held key from
  opening a screen several times, stepping back through several screens or
  toggling the same switch repeatedly
  ([player/main.go](player/main.go)).
- **yt-dlp search commands are allowlisted.** A scene or edited config can
  no longer add an option outside the search builder's exact allowlist; unique
  prefixes, aliases, `--netrc-cmd`, `--config-locations`, `-o`/`--output` and
  execution options are refused before yt-dlp starts. Experience packs can no
  longer carry `yt_search`, while the fixed `youtube_search` and
  `youtube_smart` triggers still work ([player/core.go](player/core.go),
  [player/xpackscene.go](player/xpackscene.go)).
- **Bold text uses Inter SemiBold everywhere.** A new **Bold text** switch
  loads the bundled `Inter-SemiBold.ttf` for every UI role, including Home
  and widget labels, while the existing 14 px legibility floor still applies.
  It is saved as `a11y_bold_text`; switching it clears the font, Home and
  button-fit caches so the next frame uses the new face without a restart
  ([player/home_fonts.go](player/home_fonts.go),
  [player/core.go](player/core.go), [player/a11y.go](player/a11y.go),
  [player/setmodel.go](player/setmodel.go)).
- **Reduce Transparency and Increase Contrast work on every screen.** Two
  accessibility switches now live beside High Contrast and are saved as
  `a11y_reduce_transparency` / `a11y_increase_contrast` in `jukauser.json`.
  Reduce Transparency makes `glassA` blend its ink into an opaque token, so
  tiles, rows, sheets, chips and hairlines stop showing the wallpaper through
  while keeping their hierarchy; modal scrims stay translucent because they
  dim a scene rather than act as a glass surface. Increase Contrast adds
  24/255 to the same glass tokens, widens every shared focus ring from 3 px to
  5 px, and makes the semantic text accessors reach 7:1 on the background and
  both surfaces. Both settings are also rows in Settings → Display & theme
  ([player/ui_glass.go](player/ui_glass.go), [player/design.go](player/design.go),
  [player/a11y.go](player/a11y.go), [player/setmodel.go](player/setmodel.go)).
- **A backup carries the handheld's time zone.** The zone file
  (`jukatimezone`) is now part of the Settings category, and a restore hands
  its value to `dateTimeApplyZone` on the main thread before the stores
  reload. A desktop restore leaves the OS-owned zone alone; the file is carried
  but not applied there ([player/bkup.go](player/bkup.go), [player/datetime.go](player/datetime.go)).
- **Books remember where you left them.** The PDF/EPUB reader now saves the
  page, scroll position, text size and per-page bookmarks in
  `reader-state.json`; the Files list shows the page and bookmark count beside
  a book. Options has Bookmark/Remove and Previous/Next bookmark actions, and M
  bookmarks the current page from a keyboard
  ([player/reader_state.go](player/reader_state.go),
  [player/pdfreader.go](player/pdfreader.go),
  [player/pdfv_view.go](player/pdfv_view.go)).
- **Quiet Mode in Quick Settings.** A new Do-not-disturb switch holds JukaHub's
  toasts, UI sounds and chimes until it is turned off, without silencing an
  alarm's ringtone; alarm snooze and dismiss messages still appear. The switch
  is saved as `quietMode` in `jukauser.json`
  ([player/quietmode.go](player/quietmode.go),
  [player/quicksettings.go](player/quicksettings.go)).
- **PS Remote Play gets ready by itself, and the phone can send the PSN
  Account-ID.** A JukaHub without its Remote Play helpers now downloads them in
  the background about a minute after it starts — once the handheld is online,
  its clock is past the build's date and no game is running — instead of
  waiting for PS Remote Play to open; a try the network, GitHub or the clock
  failed is repeated after 2 to 60 minutes, any other failure waits until
  JukaHub starts again, an Account-ID is saved or you press Retry. A
  **Remote Play** chip on every other screen follows the download (tap it for
  PS Remote Play, ✕ stops it), and the panel now says **Downloading Remote
  Play…** with the percentage and the MB that have arrived, then **Installing
  Remote Play…** while the download is checked and put in place. Settings →
  Games & emulators → **Download Remote Play automatically** turns the
  background download off (`remotePlayAutoDownload`). The Phone Companion page
  has a **PlayStation Remote Play** card: it says whether an Account-ID is
  saved, takes the Account-ID (or the long account number, or Sony's
  *redirect* address) and saves it on the handheld, opens the sign-in page, and
  follows Remote Play's download on the handheld with **Download now** or
  **Try again** — with the switch on, sending an Account-ID also starts that
  download when it is needed
  ([player/psremote_helpers_bg.go](player/psremote_helpers_bg.go),
  [player/companion_psremote.go](player/companion_psremote.go),
  [player/psremote_helpers_install.go](player/psremote_helpers_install.go),
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md#installing-the-helpers)).
- **Optional daily game limits and break reminders.** Settings → Games &
  emulators can cap game time per day and show a reminder after a stretch of
  use. A launch over the cap asks for a second press before starting, so the
  limit stays a speed bump rather than a lock; both settings default to off
  ([player/playtime_limits.go](player/playtime_limits.go),
  [player/playtime.go](player/playtime.go)).
- **Patch can update yt-dlp on demand.** An installed helper's row shows its
  version and offers **Update yt-dlp now**, which checks GitHub immediately
  instead of waiting for the ten-day routine (still postponing the transfer
  while a video plays). The first install now verifies the release's
  SHA2-256SUMS before the binary enters the tools folder, and
  `toolbox-installed.json` records every install's tag and hash
  ([player/ytdlp_update.go](player/ytdlp_update.go),
  [player/patch_engine.go](player/patch_engine.go), [player/patch.go](player/patch.go)).

### Changed

- **Wine's Windows folder lives where symbolic links work.** A Wine prefix
  needs links, which the exFAT/FAT card cannot hold, so JukaHub uses, in order:
  `JUKAHUB_WINEPREFIX`, a profile's own prefix, `data/windows-runtime/prefix`
  on a card that takes links, the Smart Pro's internal storage
  (`/mnt/UDISK/jukahub/wine-prefix`, with 1 GB free), or a 1–2 GB ext4 image on
  the card, mounted only while a program runs. An existing prefix is kept, and
  Windows is prepared once without a display, so later starts are quick
  ([player/wine_prefix.go](player/wine_prefix.go)).
- **DXVK only where it can run.** Wine's own Direct3D is the default; an
  installed DXVK is used only when this device's Vulkan driver can run its
  version (2.x needs Vulkan 1.3, 1.x needs 1.1), and its DLLs are now copied
  into the prefix, so an enabled DXVK actually loads. A PC whose driver reports
  Vulkan 1.3 gets the latest DXVK automatically, and a failed DXVK download no
  longer keeps a program from starting; on the Smart Pro (Vulkan 1.1) Setup
  offers DXVK 1.10.3 as an experiment, installed on a second press
  ([player/wine_runtime.go](player/wine_runtime.go),
  [player/windows_runtime_install.go](player/windows_runtime_install.go)).
- **A Windows program starts in the background.** Its row says **Starting…**
  with the step on its second line ("Preparing Windows…" for a few minutes the
  first time), the list stays open and is back when the program ends, and one
  Windows program runs at a time. Wine game profiles start the same way, with
  the same runtime, display, pad and clean-up, and a failure of either opens
  the launch-failure card with the log. A start is refused with a note while
  the runtime is being installed or updated, Install & Run starts the program
  only if Setup is still open when the install ends, and a game started
  meanwhile keeps the screen. The game power profile covers the session, and
  Rescan moved to **Start** ([player/windowsapps.go](player/windowsapps.go),
  [player/windowsapps_ui.go](player/windowsapps_ui.go)).
- **Android games draw through the firmware's SDL2 on stock TrimUI.** On the
  PowerVR models (Smart Pro, Brick, Brick Pro, Hammer) AndroidRT now starts on
  its SDL-hosted backend, because other ports found raw EGL stays black there;
  the Smart Pro S (Mali) keeps the native one, and `ANDROIDPAK_PRESENT=native`
  brings the old path back
  ([player/androidrt/src/eglpresent.c](player/androidrt/src/eglpresent.c),
  [docs/ANDROID.md](docs/ANDROID.md)).
- **Big Android games stay on the card.** When a game's assets unpack to more
  than 64 MiB they are read from the APK on the card instead of being copied
  into memory, so games with hundreds of MB of assets pass the memory check;
  `JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB=0` now really turns that check off, as its
  refusal says ([player/androidpak.go](player/androidpak.go)).
- **The Emulator updates setting says what it covers.** Its Settings row, the
  boot card, the Core builds page and a one-time notice name what updates on
  this device, and the row now shows on Windows. The pass and the Emulators
  page's check ask GitHub about each project once per pass and wait an hour
  after GitHub refuses a lookup instead of failing one by one
  ([player/emulatorautoupdate.go](player/emulatorautoupdate.go),
  [player/emuupdatecheck.go](player/emuupdatecheck.go)).
- **The weekly checks run AndroidRT's host smoke suite (TEST-4).** It stays
  advisory until it has been green on the runner's compiler
  ([.github/workflows/weekly.yml](.github/workflows/weekly.yml)).
- **CI checks less on every push, and the test suite is back to its core.** A
  push or pull request now runs two CI jobs — the web builder checks, and the
  player's `go vet`, staticcheck and `go test` — instead of eight, beside the
  release build. The race
  detector, the Go vulnerability scan, the cold-boot budget, the builder's
  Chromium smoke test, the CI helper scripts' and game-audio plugin's tests and
  the player's native Windows build and tests run once a week and from the
  Actions tab (`weekly.yml`, which replaces the nightly macOS and Windows run;
  macOS is not a shipped build). The SD-card package
  builds by hand only, since on every push it compiled the handheld binary and
  the Remote Play helpers a second time beside the release, and OpenSSF
  Scorecard runs only weekly and when branch protection changes. The Go suite
  had doubled to 180 test files (1,675 tests) in two days, nearly all of it
  tests of features, screens and effect maths; 61 files were deleted, four kept
  only their security and persistence cases, and the security tests among the
  deleted ones were kept in files that stayed or in four small files, leaving
  123 files and about 1,005 tests. `make
  check` matches CI again (the CI-script and game-audio tests are `make
  test-scripts` and `make test-spatialfx`, and run weekly), and
  [CONTRIBUTING.md](CONTRIBUTING.md#tests-for-what-you-changed) now says which
  changes get a test and which do not
  ([.github/workflows/ci.yml](.github/workflows/ci.yml),
  [.github/workflows/weekly.yml](.github/workflows/weekly.yml)).

### Fixed

- **The Remote Play helper build gets past chiaki-ng's libevent check, with a
  readable log.** The second helper build stopped at `find_package(libevent)`
  in chiaki-ng's `lib/CMakeLists.txt`. The build now makes libevent
  2.1.12-stable from its checksum-verified release, static like the other
  dependencies, and adds the lowercase package file name that lookup asks for
  (libevent installs only `LibeventConfig.cmake`, which a case-sensitive
  lookup does not see). Each step's output, including the hundreds of
  "Performing Test HAVE_…" compiler probes curl's and the libraries' configures
  print, now goes to a log file, and the job shows one line per step; a failed
  step prints the end of its log, a failed configure also prints the lines of
  chiaki-ng's CMake files around the error and every package and option they
  ask for, and the run's summary repeats it
  ([player/native/build-helpers.sh](player/native/build-helpers.sh),
  [.github/workflows/remote-play-helpers.yml](.github/workflows/remote-play-helpers.yml)).
- **A key that opens a text field no longer types its own letter on Live TV
  or Media.** X on Live TV and X or / on Media now drop the key's character
  through the shared opener guard, so a search starts empty instead of with
  "x" or "/" ([player/vkopenertext.go](player/vkopenertext.go),
  [player/tvchannels.go](player/tvchannels.go),
  [player/media_hub_input.go](player/media_hub_input.go)).
- **"Use JukaHub on 90 days" meant 90 of the last 91.** The days trophies read
  the 90-day window the tracker keeps, so they could only be earned with an
  almost unbroken run; they now count every day ever, from a lifetime record
  seeded from that window, so nothing earned is lost. Shorts and Live TV, which
  play in Media's player, now credit their watching time to their own screens,
  so "Watch Shorts for 2 hours" can be earned, and several first-set
  descriptions now say where the time counts ("Spend 1 hour in Media", "Play
  the minigames for 30 minutes"). Coming back to Trophies starts a fresh visit
  again — the Trophies tab, the top of the grid, no details sheet left open —
  which the screen's same index on return used to hide
  ([player/playtime.go](player/playtime.go),
  [player/trophy_room.go](player/trophy_room.go)).
- **An update that kept coming back after it was installed is fixed.** The first
  release that ships files in folders an installation does not have yet — `bin/`
  for the PS Remote Play helpers, `lib/` for the game-audio plugin — could not
  be installed: the swap helper only copies files, so it stopped at
  `bin/chiaki-regist` right after replacing the JukaHub binary. The new build
  started, the startup rollback put every other file back, `release-info.json`
  still named the previous release, and the same update was offered again after
  every install. The helpers now create the folders they copy into (JukaHub
  creates them first too, so a folder that cannot be made is an error on screen
  rather than a helper failing in the background), and the POSIX helper keeps
  its errors in `apply-update.log`. A swap that stopped after the new binary was
  in place is now finished at the next start, its new design loaded at once,
  instead of half undone — which is also what gets an installation already
  caught in the loop out of it with one
  more press of **Update**: its old helper still stops at `bin/`, but only after
  it has put the fixed build in place. A rollback of a swap that never reached
  the binary now says so in the notifications instead of passing silently, and
  the trees staged by updates that never finished are deleted once a release is
  confirmed ([player/update_resume.go](player/update_resume.go),
  [player/patch_swap.go](player/patch_swap.go),
  [player/autoupdate.go](player/autoupdate.go),
  [player/patch_engine.go](player/patch_engine.go),
  [player/hotreload.go](player/hotreload.go)).
- **Thermal throttling is visible on the handheld.** JukaHub watches the
  kernel's CPU cooling state while its screen is visible, shows that state in
  Hardware, and raises one toast when the thermal limit engages
  ([player/thermal.go](player/thermal.go), [player/systool_hw.go](player/systool_hw.go)).
- **Labels no longer expand protected settings.** Secret variable names and
  encrypted `ENC:` values stay as placeholders instead of exposing their stored
  contents in the UI ([player/core.go](player/core.go),
  [player/livebuilder.go](player/livebuilder.go)).
- **Animated wallpapers release frames while a game owns the screen.** The
  decoder stops and extra frame textures are freed; when JukaHub returns, it
  reloads the current wallpaper ([player/gifwall.go](player/gifwall.go),
  [player/main.go](player/main.go)).
- **Focused game covers no longer decode during drawing.** The selected cover
  is decoded and reduced on one background worker, then uploaded as a texture
  on the render thread. Missing, damaged or oversized images keep the instant
  monogram fallback ([player/game_library_ui.go](player/game_library_ui.go)).
- **UI font sizes share one in-memory source.** The font file is read once per
  path; SDL_ttf opens each size from pinned bytes, held until the fonts close
  at shutdown ([player/home_fonts.go](player/home_fonts.go),
  [player/core.go](player/core.go), [player/canvas.go](player/canvas.go)).
- **Voice finds the Smart Pro's microphone.** Voice typing (and the Voice
  command page) said "No microphone found" on a TrimUI Smart Pro, which has
  one: they only ever asked SDL for its default capture device, ALSA's
  "default" PCM, and gave up when that would not open. All three voice
  features now share one microphone that also tries every capture device the
  kernel lists (through ALSA's plug layer, `plughw:0,0`), SDL's own list and
  `arecord`, swaps a device that opens but stays silent for the next one (on
  a PC only one that fails or gives no data: silence there is a muted
  microphone), and remembers the one that worked. Before the first recording
  it switches on the codec's microphone inputs, which the Smart Pro's and the
  Brick's codec ships switched off ("ADCL Input MIC1 Boost Switch" and its
  MIC2 twin, by numid; never a playback path), lifts a microphone level parked
  at a muted bottom, and puts both back on quit; every attempt is logged under
  `mic`. The mic chip no longer hides when
  SDL lists no capture device but the kernel does
  ([player/voicemic.go](player/voicemic.go), [player/vkvoice.go](player/vkvoice.go),
  [player/voicecmd.go](player/voicecmd.go), [docs/VOICE.md](docs/VOICE.md)).
- **The power-on boot moves on the handheld.** The TrimUI comes with Reduce
  motion on for its menus, and the boot honoured that default, so on the very
  device it was made for it was a series of fades: no light round the logo's
  pane, no glint, no cards rising into place. The boot now follows Reduce
  motion only once it has been switched by hand (Settings → Display & theme,
  or Quick Settings), and Battery Saver still stills it; on Windows, where
  Reduce motion starts off, it is followed as before
  ([player/bootos.go](player/bootos.go), [docs/BOOT.md](docs/BOOT.md)).
- **WPE Browser gets the animated wallpaper's memory too.** Handing a page to
  WPE freed every cache the low-memory path frees except an animated
  wallpaper's frames, up to 40 MB on the handheld; they are let go as well
  now and decoded again when JukaHub is back, and the shutdown card releases
  them with the other textures ([player/brw_wpe.go](player/brw_wpe.go),
  [player/shutdown.go](player/shutdown.go)).
- **A release offer that is gone no longer answers a tap.** The top bar's
  update pill kept its last rect after it stopped being drawn (the release
  installed, screenshot mode, Home), so a tap there could still start the
  install and the D-pad could still stop on it; the rect is now measured
  again every frame ([player/extras.go](player/extras.go)).
- **Updated the game library and accessibility docs to match the app.** They now
  distinguish libretro cover images from game metadata, describe the existing
  save-state and per-game options, and cover Power mode and vibration strength.
  The Home quit flow and the mise Go pin now match the shipped code and Go
  toolchain ([docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md),
  [docs/ACCESSIBILITY.md](docs/ACCESSIBILITY.md),
  [docs/UX_REVIEW.md](docs/UX_REVIEW.md), [mise.toml](mise.toml)).
- **Launch failure help survives wrapped errors.** Missing game folders,
  profile executables and runtimes, plus unsupported launcher types, now carry
  typed causes so the dialog can suggest a fix without parsing the displayed
  error text ([player/launch_errors.go](player/launch_errors.go),
  [player/gameprofiles.go](player/gameprofiles.go),
  [player/emulators.go](player/emulators.go)).

### Added

- **285 more trophies — 386 in all.** The first hundred only counted time; the
  new ones count what you do: games launched, finished and played on different
  systems, the minigames' best scores (Snake, Tetris lines and four-line clears,
  the 2048 tile, Pong goals, Minesweeper boards, Flappy pipes), blocks placed
  and ores mined in JukaLand, tracks and artists, videos and Shorts, downloads
  and gigabytes, web pages and sites, ads blocked, chat messages, files copied,
  moved and extracted, Terminal commands, Pomodoros, alarms, screenshots,
  themes, wallpapers and Home edits — plus the clock (Night Owl, Dawn
  Patrol, weekends, staying up past midnight, the longest sitting), the
  calendar (New Year's Day, Pi Day, Halloween, a Friday the 13th, your
  Hubiversary…), every day JukaHub was ever used, the longest run of days, a
  visit to each screen and eleven more collection rungs, up to 350. Features
  report what happens through a small goroutine-safe API that the tracker folds
  into the saved playtime record; distinct things (sites, games, networks) are
  kept as hashes, never as names, and dates from a clock that was never set
  (a handheld before network time) are not believed. An old save is credited
  from what it already holds — the last 90 days and the per-game time —
  without a celebration, and a burst of unlocks earned in play gets its cards,
  the rarest first, and one notification row instead of dozens. A trophy that
  needs something this setup lacks (Remote
  Play or Bluetooth on Windows, an AI key, a Home Assistant server, a
  RetroAchievements account) stays out of the room until it has it, and
  **bonus** trophies (another device, another player, more controllers, a leap
  day) do not hold up the platinum. The room gets a **Category** chip: a list
  of the 24 groups with "12 of 40 earned" each, so 386 cards stay browsable
  ([player/trophy_set2.go](player/trophy_set2.go),
  [player/trophy_events.go](player/trophy_events.go),
  [player/playtime.go](player/playtime.go),
  [player/trophy_room.go](player/trophy_room.go),
  [player/trophy_celebrate.go](player/trophy_celebrate.go)).
- **The phone companion keeps the handheld's screen at the top.** The screen
  picture moved from the bottom of the page into a bar pinned at the top, so it
  stays in view while the cards (Type, Voice, Remote, links, files) scroll under
  it; on a phone held sideways it gets a column of its own on the left. It
  refreshes a moment after every remote press, text, link or upload, **Live**
  follows it continuously (only while the page is in view), and **Hide screen**
  folds it away, remembered on that phone
  ([player/companion.go](player/companion.go)).
- **Enter the Groq API key from your phone.** A **Groq API key** card on the
  phone companion page takes the key pasted from console.groq.com. The handheld
  checks it with Groq first (a key Groq refuses is not saved; a handheld that
  cannot reach Groq saves it anyway and says it could not check) and saves it
  exactly as the key field in Keyboard settings does. The key is never sent back
  to the page — only whether one is saved and its last four characters
  ([player/companion_groq.go](player/companion_groq.go)).
- **The voice orb: press Menu and talk.** On a TrimUI the Menu button (F4 on a
  keyboard) now opens a glowing, Siri-style orb in the bottom-right corner of
  any screen that says "Listening…" and follows your voice. It stops by itself
  when you stop talking, or listens for as long as Menu is held, then runs the
  Voice command page's commands — open a screen, app, game or setting, volume,
  brightness, Wi-Fi, music, screenshots — saying what it did in a bubble,
  counting down before it opens something (B stops it), listing matches to
  pick from, and handing a question that names nothing on the handheld to Juka,
  whose answer streams into the bubble. Missing pieces come with their fix one
  press away (add the Groq key, use the phone, search for it). With the
  keyboard up, Menu is voice typing; music and video pause while it listens.
  Menu is read like the TrimUI's own launchers read it — on release, so a
  chord stays a chord: the firmware's Menu + volume brightness never opens the
  orb, and Menu + Start still quits. Menu was Back before: it still is over
  modal panels, the dock, the tour and JukaHub's own games, and Settings →
  Controls → **Menu button: voice** turns the binding off
  ([player/voiceorb.go](player/voiceorb.go), [player/main.go](player/main.go),
  [docs/VOICE.md](docs/VOICE.md), [docs/CONTROLS.md](docs/CONTROLS.md)).
- **Eight new themed Total conversions.** Moonbase opens a lunar dashboard;
  Sunset Strip and Deep Reef put the Home tiles on glowing carousels; Sakura
  Night Market, Radioactive Vault and Neon Raceway build themed widget boards;
  Pixel Rain adds a phosphor carousel, and Velvet Arcade turns the grid into a
  walnut-and-amber cabinet. Each conversion pairs its palette, Home layout and
  backdrop glow ([player/themes.go](player/themes.go)).
- **Six new palettes and six complete Home conversions.** Arctic and Orchid
  use the dashboard, Ruby and Terminal use the carousel, Cobalt becomes an
  editable widget board, and Tangerine uses the hero grid
  ([player/themes.go](player/themes.go), [README.md](README.md)).
- **Four new palettes and four complete Home conversions.** Arcade and Moss
  conversions use the carousel, E-Ink becomes an editable widget board, and
  Ember uses the hero grid. Each conversion applies its palette and layout
  together ([player/themes.go](player/themes.go), [README.md](README.md)).
- **Theme authors have a complete file reference.** The new guide lists every
  theme JSON key, its color format, and the shared glass behavior. The plugin
  guide's refresh instructions now match how scans rebuild the live catalog
  ([docs/THEMES.md](docs/THEMES.md), [docs/PLUGINS.md](docs/PLUGINS.md)).
- **Spatial audio is one step away in Quick Settings.** Its mode row cycles
  Off, Auto, Headphones and Speakers, shows what Auto resolves to, and uses the
  same saved setting as the audio and game controls
  ([player/quicksettings.go](player/quicksettings.go),
  [player/spatial.go](player/spatial.go)).
- **PS Remote Play installs its own helpers.** A JukaHub without
  `Apps/JukaHub/bin/chiaki-stream` and `chiaki-regist` (every install so far:
  no release carried them) no longer stops at "the PS5 pairing helper is
  missing (chiaki-regist)". Opening **PS Remote Play** online downloads
  `JukaHub-RemotePlay-linux-arm64.zip` from the newest recent release that has
  it (stable before beta, so a release whose helper build failed is skipped),
  checks it against GitHub's digest (and a signed `release.json` when the
  release has one) and its own `SHA256SUMS`, starts each helper once to make
  sure this firmware loads it, and installs only the missing ones beside the
  binary, `chiaki-stream` last; a cut-off download resumes, and a helper the
  firmware refuses is not installed, remembered (`bin/.remote-play-refused`)
  and only downloaded again by Retry. The console list shows the progress; a
  PS5 (and an address typed by hand, while an install can come) is asked for
  its PIN once its pairing helper is in (the PIN Link Device shows lasts five
  minutes), and Play streams by itself when the install is done; the console's
  row says which, and **B** or closing the panel lets go of either. With no
  internet or after a failure the row says why, in words that fit it, and
  **A** tries again. `docker-image.yml` and `release.sh` attach the package to every
  release that built the helpers
  ([player/psremote_helpers_install.go](player/psremote_helpers_install.go),
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md#installing-the-helpers)).
- **Trophies in the top bar.** A gold trophy now sits immediately left of the
  notification bell, on Home and on every screen: **A** on it (the D-pad
  reaches it from the bell and from the release offer), a click or a tap opens
  **Trophies** from anywhere, and inside the room the same press goes back to
  the top of the Trophies tab. A gold count on it shows the trophies earned
  since the room was last open (since JukaHub started, until the room has
  been opened once), and it is outlined while the room is on screen. The
  release offer moved one place left, so the corner's two fixed controls never
  move; and a screen's status readings now leave room for the whole corner,
  so a crowded bar drops a reading instead of running under the Menu pill
  ([player/header_trophy.go](player/header_trophy.go),
  [docs/CONTROLS.md](docs/CONTROLS.md)).
- **Bug reports in one press, no typing.** **Report a bug** is in **Start →
  Device** now, and Submit no longer asks for a title first: with nothing
  typed it sends the redacted diagnostics and recent log under a title such as
  *Quick report from TrimUI Smart Pro*. The preview (**Y**, from anywhere on
  the form) has a **Send** button that starts focused, so **Y** then **A**
  sends what it just showed. With the diagnostics and the log both switched
  off, a title or a description is still needed
  ([player/bugrep.go](player/bugrep.go), [docs/BUG_REPORTS.md](docs/BUG_REPORTS.md)).
- **APK Store: which Android games this handheld might run, in one screen.**
  **Start → Games → APK Store** (not on Windows) finds the APKs on the card,
  in the Phone Companion's `PhoneDrop` folder and in the download folders,
  reads each one — its manifest, name and icon, its native code and the engine
  it was made with — and says plainly whether AndroidRT can try it, and if
  not, why and what works instead (a PortMaster build of the same game, for
  example); an app that needs Android's Java runtime is never offered. One
  press adds a game to **Game Profiles** and plays it through the AndroidPak
  launch path, and *Remove from Games* takes away only the store's own
  profile. A **Store** tab lists known titles and kinds of APK with how to get
  them legally (a download only ever with a published SHA-256; the built-in
  list has none yet), and a **Runtime** tab shows AndroidRT, this device and,
  on a Linux machine that has it, Waydroid. Experimental: no Android game has
  run on a handheld yet, and no release carries AndroidRT yet. docs/ANDROID.md
  was brought up to date as well — it still described a Waydroid engine
  (`android.go`) that is no longer in the tree
  ([player/apkstore.go](player/apkstore.go),
  [player/apkstore_axml.go](player/apkstore_axml.go),
  [player/apkstore_verdict.go](player/apkstore_verdict.go),
  [docs/ANDROID.md](docs/ANDROID.md)).
- **Voice command: say what to open.** "Open YouTube", "play Mario Kart",
  "volume 50", "take a screenshot", "turn off Wi-Fi", "next song" or "what
  time is it": **Start → Tools → Voice command**, **Y** in the Home search
  sheet or **F4** on a keyboard opens a page with one big microphone. **A**
  listens through the handheld's microphone and Groq's Whisper (the Groq key
  from Settings → General, and internet); the clip goes to Groq with a short
  list of this handheld's screen, app and game names so they come back spelled
  right, and neither the audio nor the words are kept or logged. Without a
  key, a working microphone or internet, **Y** shows a QR code for the Phone
  Companion's new **Voice** page, where the phone's own keyboard dictation
  does the listening. Names are matched against everything Home search finds:
  a clear match opens after a short "Opening…" pause that **B** cancels, an
  unclear one lists the matches, and nothing found offers a search instead.
  English only for now; not yet tried with a handheld's microphone
  ([player/voicecmd.go](player/voicecmd.go),
  [player/voicecmd_intent.go](player/voicecmd_intent.go),
  [player/voicecmd_phone.go](player/voicecmd_phone.go),
  [docs/VOICE.md](docs/VOICE.md)).
- **Spatial audio (virtual surround).** **Settings → Sound & haptics → Spatial
  audio**, or **Tools → Audio**: *Off* (the default), *Auto*, *Headphones* or
  *Speakers*, with **Spatial strength** and **Virtual room** (small, medium,
  large). On headphones everything JukaHub plays — videos, music, podcasts,
  audiobooks, YouTube — comes from four virtual loudspeakers around you in a
  small virtual room: a parametric model of the head (Brown & Duda's spherical
  head), early reflections and a short reverb, loudness-matched and
  soft-limited so it never clips. On the handheld's own speakers it widens the
  stereo instead, and *Auto* follows the output in use (the 3.5 mm jack or
  Bluetooth headphones, or the speakers). Bit-perfect bypasses it, and
  choosing a mode switches Bit-perfect off. The DSP is specified once, by a
  reference script, and both the Go player and the game plugin below carry
  tests against its golden vectors. Not yet listened to on a device
  ([player/spatial.go](player/spatial.go),
  [player/spatial_dsp.go](player/spatial_dsp.go),
  [player/spatialfx/jh_spatial_ref.py](player/spatialfx/jh_spatial_ref.py),
  [docs/SPATIAL_AUDIO.md](docs/SPATIAL_AUDIO.md)).
- **Spatial audio in RetroArch games.** With **Tools → Audio → Spatial audio
  in games** on, a RetroArch game started from the library plays through the
  same virtual surround: a small native library built into the handheld
  package, which RetroArch loads as its own audio DSP filter, with a preset
  JukaHub writes for that launch (mode, strength and room; *Auto* is resolved
  when the game starts). `retroarch.cfg` gets its own value back when the game
  exits, and a filter you chose in RetroArch is never replaced. JukaHub checks
  once whether the installed RetroArch can load filter libraries, and the row
  says when it cannot (or when the plugin is missing, as on Windows for now).
  The same library is also a LADSPA plugin. Its host tests drive both plugin
  interfaces through `dlopen` against the reference; it has not run on a
  handheld, and whether the firmware's RetroArch loads filters is unknown
  ([player/spatial_games.go](player/spatial_games.go),
  [player/spatialfx/README.md](player/spatialfx/README.md)).
- **The backdrop can be water.** **Settings → Display & theme → Liquid
  background** (*Off*, the default, *Gentle* or *Lively*) turns the backdrop
  under the glass into a shallow pool that answers the controls you already
  use: moving the focus drops a ripple under the tile it lands on, the Smart
  Pro's left stick stirs the water round the focused tile and its right stick
  drags a wake (or follows the virtual cursor while it is up), the pointer
  does the same on a Brick in mouse mode and the mouse on a PC, a device with
  an accelerometer sloshes it when tilted (**Tilt to slosh**, shown only where
  a sensor answers — none is known on the Smart Pro or the Brick), and a drop
  of rain now and then keeps an untouched screen alive for two minutes. The
  wallpaper — an animated one included — or Home's own colours and blooms are
  bent through the water with `SDL_RenderGeometry` and lit along the ripples:
  a calm pool costs one copy, a moving one a 30 Hz step of a 64×36 grid (48
  columns on Low Power) and two draw calls, and the header-band repaint sends
  only the rows under it. Reduce Motion (the handheld's default) keeps the
  ripples you make and drops the rain and the slosh; the handheld steps down
  to the coarse grid by itself if the water costs more than 5 ms a frame; a
  renderer without geometry support gets the plain backdrop and Settings says
  so. With Living background on, the water bends its flat colour and its
  lights drift over it
  ([player/liquidbg.go](player/liquidbg.go),
  [player/liquidbg_motion.go](player/liquidbg_motion.go)).
- **Animated wallpapers.** A GIF picked as the wallpaper plays on every
  screen, Home included. The file is walked block by block and decoded one
  frame at a time on a goroutine — never every frame at full size, which a
  few megabytes of GIF can turn into hundreds — then cropped to the screen's
  shape, kept no larger than the screen and uploaded a few megabytes per UI
  frame; frames play as they arrive, so the first appears at once. The
  decoded frames stay within a budget (40 MB on the handheld, 192 MB on a
  PC): a big GIF is shrunk, then a very long one drops frames and gives their
  time to the frame before. Pixel art (a third of the screen or less) keeps
  its own pixels at a whole-number scale with nearest filtering. Low Power
  plays at up to about 15 fps; Battery Saver and the new **Settings → Display
  & theme → Animate wallpaper** switch hold the first frame, while plain
  Reduce Motion does not, because the handheld ships with it on and picking
  an animated wallpaper is itself a choice of motion. A GIF too large to
  animate is refused at the picker with a reason, critical memory pressure
  leaves the wallpaper on its first frame, and a theme change decodes it again
  (transparent areas take the theme's base colour)
  ([player/gifwall.go](player/gifwall.go)).
- **The wallpaper picker, redone.** It is on Home's glass over the live
  backdrop, so an animated wallpaper keeps playing behind it, with a preview
  of the highlighted picture decoded in the background and its facts
  (*Animated GIF · 48 frames · 480 × 270 · 2.1 MB*). A *None — the glass
  backdrop* row clears the wallpaper, an *Up a folder* row is always there,
  folders come first, GIFs wear a chip and the wallpaper in use is marked. It
  opens in the current wallpaper's folder; on the handheld the card's
  `Wallpapers`, JukaHub's `data/wallpapers`, the Phone Companion's
  `PhoneDrop` (a GIF sent from a phone is right there), `Pictures` and
  `Screenshots` folders come next. Right opens a folder and Left (or
  Backspace) goes up to the folder you came from; a mouse click highlights a
  row and a second click uses it
  ([player/wallpaper.go](player/wallpaper.go)).
- **Screen Share: the Smart Pro's missing HDMI port, in a browser.** **Start →
  Tools → Screen Share** (Remote Cast until now; search finds it as *screen
  share*, *cast*, *tv*, *hdmi* or *mirror*) shows a link and a QR code: open it
  on a smart TV, a laptop, a tablet or a phone on the same Wi-Fi — or cast that
  laptop tab to a TV — and the handheld's screen plays there, games included.
  JukaHub stops drawing while a game owns the screen, which used to freeze the
  picture on the last menu frame; now the game is read from `/dev/fb0` through
  the bundled ffmpeg when it is installed, otherwise by JukaHub's own reader
  (at most 8 fps and 640 px, so it never takes more than about half a core
  from the game). Quality is *Smooth* (640 px, 20 fps; the handheld's
  default), *Balanced* (960 px, 15 fps; a PC's) or *Sharp* (full size,
  10 fps); the handheld captures its menus at most about twelve times a
  second, fewer when the GPU is slow to hand a frame back, and a screen that
  is not changing is re-sent once a second instead of at the frame rate.
  **Games too** can be switched off (viewers then see a pause card). The
  viewer page goes full screen (button, double-click or tap, **F**), cycles
  Fit / Fill / Pixel-perfect (**M**), hides its controls after three idle
  seconds, has a `&tv=1` mode with no controls for TV browsers, falls back to
  single frames for a browser that cannot show MJPEG, and waits while sharing
  is off, then starts by itself. Up to six browsers watch at once, and a red
  **LIVE** pill shows on the handheld while anyone does (never in the stream
  or in screenshots). Sound stays on the handheld
  ([player/castcap.go](player/castcap.go),
  [player/castcap_fb.go](player/castcap_fb.go),
  [player/companion_cast.go](player/companion_cast.go),
  [player/castui.go](player/castui.go)).
- **Report a bug sends the report from the device.** Submit used to end on a
  QR code for a phone signed in to GitHub: no build carried a report
  collector. Every build now posts the redacted report to the JukaHub report
  service — a Netlify function on the builder's site
  (`https://generate.jukalang.com/api/bug-report`) that files it as a public
  GitHub issue, not under your name — and the sent sheet shows the issue
  number, with an optional code to follow it on a phone. The preview names
  where the report goes and shows its reference and creation time, so it is
  exactly what is sent. The phone path is only the fallback now, with **Send
  later** when the service is not answering. The outbox backs off while the
  collector fails (90 s, doubling, up to 6 h), stops for the session when it
  is not there, never sends a delivered report twice, and retires a report it
  could not deliver in a month to a readable file instead of retrying it for
  ever; a web page, a proxy's refusal or a redirect is never taken for the
  collector's answer. Maintainers set `JUKAHUB_GITHUB_TOKEN` in Netlify
  ([docs/BUG_REPORTS.md](docs/BUG_REPORTS.md),
  [netlify/functions/bug-report.mjs](netlify/functions/bug-report.mjs),
  [player/bugrepsend.go](player/bugrepsend.go),
  [player/bugrep.go](player/bugrep.go)).
- **Save a video, or just its sound, from the player.** The video settings
  sheet (**Y**) has a **Save offline** row that opens a second page in the
  same sheet: for a web video **Download video** (MP4, into Media →
  Downloads) and **Audio only** (an M4A for the music player); for a file on
  the card **Extract audio** and **Get subtitles** (from the page a download
  came from). **B** steps back to the settings. The Media details sheet
  offers the same: **Save audio only** beside **Download for offline**, and
  **Extract audio** on a downloaded video. Extracting copies the video's
  sound track with ffmpeg — offline, in seconds for a clip and minutes for a
  long film — or encodes it to AAC when it cannot be copied, and files the
  track with the video's title, channel and picture; it refuses a sound that
  is already saved and stops when JukaHub quits. A second press on a
  download that is already queued says so instead of saving the file twice
  ([player/videosheet.go](player/videosheet.go),
  [player/mediaextract.go](player/mediaextract.go),
  [player/media_hub_sheet.go](player/media_hub_sheet.go)).
- **Downloaded videos come with their subtitles.** Once the download queue is
  idle, each newly saved video gets its subtitles in your language beside it
  as `<name>.<lang>.srt`, so it plays with CC offline (and in any other
  player); a video kept after watching gets the subtitles it was watched
  with, and removing the download removes them too. A subtitle problem never
  fails or holds up a download ([player/downloads.go](player/downloads.go),
  [player/ytcaptions.go](player/ytcaptions.go)).
- **Emulator cores now update by themselves.** Settings → Games & emulators →
  **Emulator updates** (it was "Core updates") defaults to **Install
  automatically**: once a day, while the handheld charges or after two idle
  minutes and never during a game or a video, JukaHub installs the buildbot's
  newer build of every installed core where RetroArch loads it, and keeps the
  one it replaced for a one-press roll back (Emulators **R1**). It stops before
  a core that would leave the card nearly full. Choices people made are kept —
  an old "off" stays Off, and a mode picked in Settings, on the Core builds page
  or in an imported settings file stays picked — while the "Notify me" earlier
  builds stored for users who never chose moves to the new default once, with a
  toast that says where to change it. The same setting keeps JukaHub's own
  PPSSPP current ([player/emulatorautoupdate.go](player/emulatorautoupdate.go),
  [player/emulatorinstall.go](player/emulatorinstall.go),
  [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md)).

- **A core build that cannot run on this firmware is never installed.** Before
  a download reaches the core pool, and before an activation or a roll back
  replaces a core, JukaHub reads the binary's ELF metadata: it must be the
  architecture the card's RetroArch runs, and every library symbol version it
  needs (`GLIBC_2.34`, `GLIBCXX_3.4.30`, …) must exist in the libraries the
  firmware's loader would find, in the order it searches them. A refused build
  says why ("needs GLIBC_2.34; this firmware's libc.so.6 has up to
  GLIBC_2.33"), the working core stays, and that build is remembered
  (`data/performance/core-refused.json`) so the daily check does not download
  it again; a core over the 96 MB download limit is handled the same way instead
  of failing every day ([player/elfcompat.go](player/elfcompat.go),
  [player/emulatorupdates.go](player/emulatorupdates.go),
  [player/compat.go](player/compat.go)).

- **PSP games can run JukaHub's own, current PPSSPP on the handheld, and the
  Emulators page says which PPSSPP plays them.** A tester asked which PPSSPP
  JukaHub uses and why it never updated: it had none of its own — PSP games ran
  the card's (1.18.1 in the stock 2025-04-30 package, or the build CrossMix's
  PSP folder picks) — and its "update" only staged an archive the PPSSPP
  project does not publish for this hardware. The PSP tile now leads with the
  answer ("PPSSPP 1.18.1 (firmware) · 1.20.3 available", "PPSSPP 1.20.3
  (JukaHub) · firmware 1.18.1 kept"), and **X** on it opens a PPSSPP card:
  install or update the TrimUI build the minui-psp project packages (PowerVR
  for the Smart Pro and Brick, Mali for the Smart Pro S; the Brick Pro is
  offered as untested and never installed for automatically), choose which
  PPSSPP plays PSP games, remove JukaHub's, or install RetroArch's PPSSPP core
  as **X** did before. The package is installed only with GitHub's SHA-256
  digest, unpacked into JukaHub's data folder (never over the firmware's
  PPSSPP) after a check that its binary loads here, and one previous build is
  kept. JukaHub's PPSSPP keeps its settings, cheats and textures in one folder
  of its own that every update reads, starts from the package's tuned defaults
  plus the firmware's button mapping and per-game configs, and shares the
  firmware's saves and save states (bind mounts, as minui-psp does), so
  switching either way loses nothing. If it does not start, or the saves cannot
  be shared, the game opens in the firmware's PPSSPP instead and JukaHub
  switches back by itself with one toast. **Emulator updates** drives a daily
  check: Notify says once per release; Install automatically installs it and
  switches when it is newer than the firmware's — never over a launcher you
  pinned in `game-launchers.json`, which is restored exactly, in its place,
  when you switch back. The stick calibration edits whichever ini the PPSSPP in
  use reads ([player/ppsspp_managed.go](player/ppsspp_managed.go),
  [player/ppsspp_managed_ui.go](player/ppsspp_managed_ui.go),
  [player/ppsspp_calibration.go](player/ppsspp_calibration.go)).

- **Mouse mode: hold Select + L1 for a pointer anywhere in JukaHub.** Like
  GammaOS's mouse emulation, a held chord (about 0.7 s; **F6** on a keyboard)
  turns a mouse pointer on and off. While it is on the pointer stays up, the
  D-pad (slow at first, faster while held) and both sticks move it, **A**
  clicks or drags and **L2** / **R2** scroll, while **B** and every other
  button keep their jobs — which gives the TrimUI Brick, with no sticks, a
  pointer for the first time. Panels that own the pad (Quick Settings, the
  calibrations, the controller test, an alarm) and the screensaver put it away
  until they close, the on-screen keyboard keeps the D-pad, and Chat keeps its
  right-stick scroll. GammaOS uses Select + R1, but that already switches
  control profiles here, so mouse mode takes the mirror image; a control
  profile line on Select + L1 now gets a warning. Settings → Controls →
  **Mouse mode** turns the chord off
  ([player/vcursor_mode.go](player/vcursor_mode.go),
  [player/virtualcursor.go](player/virtualcursor.go),
  [docs/CONTROLS.md](docs/CONTROLS.md#mouse-mode-hold-select--l1)).

- **Mouse in games and apps (TrimUI, opt-in).** With Settings → Controls →
  **Mouse in games and apps** on, the same Select + L1 inside a running game,
  emulator or port turns the pad into a mouse for it: JukaHub lets go of the
  buttons the game saw pressed, takes the pad and adds a uinput mouse that the
  sticks and D-pad move — **A** left button, **B** right, **X** middle,
  **L2** / **R2** the wheel; a vibration pulse says on or off. The chord again,
  Menu, leaving the game or quitting JukaHub gives the pad back. Only programs
  that draw their own pointer show one (ScummVM, DOSBox, PC ports, WPE
  Browser), and a program that only looks for devices at start-up may not see
  it ([player/vcursor_bridge_linux.go](player/vcursor_bridge_linux.go),
  [player/vcursor_evdev.go](player/vcursor_evdev.go)).

- **Sign in with PlayStation to pair a PS5 or PS4: no more hunting for the
  Base64 Account-ID.** Pairing a console that needs the PSN Account-ID, with
  none saved, now opens an Account-ID card instead of a bare keyboard. On the
  handheld it shows a QR code for the Phone Companion's new sign-in page: the
  phone opens Sony's login, and after you sign in you paste the address of the
  page that says "redirect". The handheld trades its one-time code with Sony
  (the same exchange as chiaki's psn-account-id script), keeps only the
  Account-ID and goes straight to the PIN step with the console's exact menu
  path. On a PC, **Sign in in your web browser** opens Sony's page and the
  Account-ID field takes the pasted address. **Type or paste it** also takes
  the Account-ID itself, the long account number other tools print, or `0x`
  hex. The code and tokens are never logged or saved, and errors say what
  happened in plain words ("That sign-in code has expired — sign in again")
  ([player/psn_signin.go](player/psn_signin.go),
  [player/psremote_ui.go](player/psremote_ui.go),
  [player/companion.go](player/companion.go),
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md#getting-the-psn-account-id)).

- **Sites the reader can't show open in WPE Browser on the handheld.** The
  Browser's Select menu has **Open in WPE Browser**, and Google's "needs a full
  web browser" card puts it first: the page (or the search) opens in
  [WPE Browser](https://github.com/matweew/wpe-tsp), the community WebKit
  browser for the Smart Pro, Brick and Brick Pro, while JukaHub steps aside the
  way it does for a game — freeing its caches first, since WebKit wants most of
  the 1 GB — and comes back to the same page when WPE Browser closes. Where it
  is not installed, the menu and the card offer **Get WPE Browser**, and
  Settings → Browser → **WPE Browser** shows the version and updates it: the
  latest GitHub release is downloaded straight to the SD card with progress,
  verified against GitHub's SHA-256, resumed after a dropped connection,
  unpacked into `Apps/WPE`, and an update keeps `settings.conf` and everything
  you or WPE Browser added to its folder (the old folder is only deleted once
  they are across). On a PC the menu row opens the page in the desktop's
  browser ([player/brw_wpe.go](player/brw_wpe.go),
  [player/brw_wpe_install.go](player/brw_wpe_install.go),
  [docs/BROWSER.md](docs/BROWSER.md#wpe-browser)).

- **The Browser blocks far more ads and trackers, and says how many.** The
  filter's host list grew from about 65 to about 370 entries — JukaHub's own
  curated list of ad exchanges, native-ad widgets, pop-under and affiliate
  networks, data brokers, attribution SDKs, analytics, session replay and A/B
  testing (not EasyList) — with a first-party rule, so a listed company's own
  site still loads its own files. Site info shows "14 ads and trackers
  blocked" with **Allow ads on this site** (**X**), and Settings → Browser →
  **Block ads and trackers** (`browserBlockAds`, on by default) turns the filter
  off everywhere ([player/brw_adblock.go](player/brw_adblock.go),
  [player/adblock.go](player/adblock.go),
  [docs/BROWSER.md](docs/BROWSER.md#ads-and-trackers)).

- **Box art finds itself.** Covers used to download only when you asked — the
  tour's "Fetch game artwork" step or the library's Fill gaps (**L1** / **F**).
  JukaHub now looks them up in the background: about a minute and a half after
  it starts on a card that never had a pass, at most once a day after that, and
  within half an hour of new games appearing in the ROM folders (a cheap look
  at the names in each console folder, not a walk of the card). A pass waits
  for a moment that costs nothing: online, never while a game or emulator has
  the screen (a game that starts stops it after the cover it is on), never
  beside another fetch or a running install, and on battery only at 20 % or
  more and after a minute and a half without input — counted from the end of
  the last game. It is stamped only when every title got an answer, so a pass
  cut short resumes at the next good moment, and network errors back off from
  half an hour to a day. When it saved covers, one quiet notification says so
  ("Found box art for 12 games"). Only game titles are sent, as the same GET
  requests Fill gaps makes. **Settings → Games & emulators → Find box art
  automatically** (`artAutoFetch`, on by default) turns it off; safe mode skips
  it ([player/boxart_auto.go](player/boxart_auto.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#found-automatically)).

- **RetroArch has a tile on the handheld's Emulators page, and core rows lead
  to their games.** The page listed every core but no RetroArch, and **A** on a
  core said "start it from RetroArch". The RetroArch tile names its folder,
  core count and the config it reads, and **A** opens RetroArch's own menu the
  way the firmware's launchers start it; **A** on a core row (its hint now says
  **Games**) opens the Games library searched for the console it plays
  ([player/raemu.go](player/raemu.go), [player/emulators.go](player/emulators.go)).

- **Booting into JukaHub looks like an operating system starting.** When the
  handheld starts straight into JukaHub (Settings → General → Start JukaHub
  at login), the start is no longer a half-second card but a boot: the
  JukaHub logo the firmware showed stays on the black screen, a light runs
  once round its glass pane, the backdrop's four blooms ignite one by one
  with the startup chime's four bells and drift out from behind the logo
  while the theme's colours dawn, the wordmark catches a glint, and a quiet
  progress line reports the real boot steps. Then the first screen arrives
  instead of cutting in: Home's cards rise into place one after another in
  reading order over Home's own backdrop, the header and the greeting fade
  in round them, and the boot's wordmark flies up into the header, where the
  same word lives on Home, so the last frame of the boot is the first frame
  of Home. The queued startup work runs first, behind the still logo (so no
  light ever stalls on its lap), then the beats take under three seconds and
  the arrival about one more; it never waits for the network, and any button
  skips to the arrival. A power-on is detected, not configured: the TrimUI
  autostart hook's marker, or a start the hook's queued command made within
  the first minutes of the kernel's uptime with autostart on, or
  `JUKAHUB_BOOT=1` / `--boot` (the Windows and Linux login launchers now pass
  it); a start since the same kernel boot (recorded with the boot id, so a
  clock set by the network cannot fool it) never replays it, and Recovery's
  restart drops the trigger.
  **Settings → General → Startup animation** picks *Full at power-on* (the
  default), *Full every time* or *Always quick*, and **Watch the boot
  animation** plays it on demand. Reduced motion (the handheld's default)
  keeps every beat as a fade: nothing travels, drifts, scales or flies
  ([player/bootos.go](player/bootos.go),
  [player/bootarrive.go](player/bootarrive.go),
  [player/bootlogo_layout.go](player/bootlogo_layout.go),
  [docs/BOOT.md](docs/BOOT.md)).
- **A JukaHub boot logo for the TrimUI firmware.** **Settings → General →
  Boot logo** (A twice) writes the JukaHub boot screen — the logo over the
  wordmark on black, on exactly the pixels the boot animation opens with —
  into the picture the bootloader shows before Linux starts, so a power-on
  reads as one continuous boot; A twice again puts the previous picture
  back. It is the one place JukaHub writes outside the SD card, so it is
  narrow: the logo is found by partition *name* only (a FAT `boot-resource`
  or `bootloader` partition holding `bootlogo.bmp`, or a raw `bootlogo`
  partition that starts with a BMP — Allwinner's two layouts), every source
  that names partitions must agree, and the SD card and the running system
  are never touched; the firmware's own file decides the format, its header
  is kept byte for byte and only the pixel rows are rewritten, in place and
  at the same length; the current picture is saved to
  `Apps/JukaHub/data/bootlogo/` and read back before anything is written,
  and every write is read back by SHA-256 (a write that does not read back
  puts the previous picture straight back). Anything unexpected — another
  layout, compression, a palette, a portrait logo, two stores — leaves the
  row at *Unavailable* with the reason instead of guessing. On CrossMix the
  same picture is also left in `Apps/BootLogo/Images/JukaHub.bmp` for its
  BootLogo app. Not run on a device yet: TrimUI's exact storage (firmware
  1.1.x enlarged its boot partition) is unconfirmed, which is why the
  installer probes before it writes. `branding/mkbootlogo.py` renders the
  same screen as 24-bit BMPs for flashing by hand
  (`branding/bootlogo/jukahub-bootlogo-1280x720.bmp` for the Smart Pro and
  Smart Pro S, `-1024x768.bmp` for the Brick and Hammer)
  ([player/bootlogo.go](player/bootlogo.go),
  [player/bootlogo_linux.go](player/bootlogo_linux.go),
  [branding/mkbootlogo.py](branding/mkbootlogo.py),
  [docs/BOOT.md](docs/BOOT.md)).
- **A brightness button beside the volume button.** A sun now leads the
  status cluster, immediately left of the speaker, on Home and on every
  screen, and the D-pad reaches it the same way. It shows the level at a
  glance (a smaller sun below half); A, a click or a tap opens a brightness
  card that matches the volume card: the level, a slider to click or drag,
  and a Dim button (A, Enter) that drops to the lowest level and back again.
  The D-pad, the arrow keys or the wheel move the level and B (Esc) closes.
  What it moves depends on the device: on the TrimUI Smart Pro and Brick the
  panel's backlight through the Allwinner display driver (`/dev/disp`, the
  path the firmware, MinUI and spruceOS use), in the firmware's own ten steps,
  with the level written back to the firmware's `system.json` so its
  brightness keys carry on from it (and each step written with the panel level
  the firmware itself was seen to use for it, so nothing jumps when the
  firmware applies its own); on a Linux PC the sysfs backlight; on a Windows
  desktop the monitor's own brightness over DDC/CI, and on a laptop or
  handheld the panel brightness through WMI. Where none of those
  exists, the card dims JukaHub's picture instead and says so. Writes run on
  one worker that applies only the newest level, so a held D-pad never stalls
  the screen (on Windows a write is a PowerShell run or a monitor exchange).
  Quick Settings' Brightness slider, the Settings brightness row, the control
  profiles' brightness actions and the pairing QR's full-brightness boost now
  go through the same controller, so they all show one number; the saved
  level is restored at launch only where JukaHub is its sole owner (never over
  the TrimUI firmware's or Windows' own level)
  ([player/brightnessctl.go](player/brightnessctl.go),
  [player/brightnesspopover.go](player/brightnesspopover.go),
  [player/brightness_disp_linux.go](player/brightness_disp_linux.go),
  [player/brightness_trimui.go](player/brightness_trimui.go),
  [player/brightness_windows.go](player/brightness_windows.go)).

- **The Browser fills the screen, and Select opens a menu with everything.**
  The page used to sit inside the top bar, a header band, the address toolbar,
  a cookie strip, a row of fourteen chips and the footer, which on the Smart
  Pro's 720-line panel left it a little over half the screen. It is now drawn
  edge to edge, the way the WPE browser for the device does it, and **Select**
  (M or the Menu key on a keyboard, a right click with a mouse) opens a sheet
  with every action: Back, Forward, Reload and Home across the top, then the
  address bar, web search, the page's links, find, zoom, link mode, saving and
  the saved pages, site info, the ad filter, sign-ins, the layout switch,
  Browser settings, the issue tracker and Close. Rows keep their places (one
  that cannot act is dimmed), so Select then A goes back a page; Zoom and Find
  change in place with Left/Right. A pill in the top-right corner names the
  site for a moment after each page loads and while the pointer is near the
  top, the find count and link mode sit under it, and the menu's footer has
  the clock and battery. Backspace / Alt+← / Alt+→ / F5 work as in a desktop
  browser. **Settings → Browser → Full-screen pages** (`browserFullScreen`)
  brings the classic layout back
  ([player/brw_fullscreen.go](player/brw_fullscreen.go),
  [player/brw_menu.go](player/brw_menu.go)).

- **Home can be a carousel, or widgets you arrange.** Next to Standard (your
  config's dashboard) and the Hero grid, **Settings → Display & theme → Home
  layout** offers two layouts built from your own list of tiles. **Carousel**
  is one row of big cards centred on the focus, wrapping around, with the
  focused card's title and description under it and position dots.
  **Widgets** is a six-column grid of tiles in the sizes you give them, which
  scrolls past three rows. A tile is a Home card, any Start-menu entry (a
  screen, a tool, a device action, a plugin's button) or an info widget:
  Clock (with the next alarm), Weather, Battery, Storage, Now playing,
  Calendar or Playtime, each opening its screen on **A**. **Edit Home** —
  **X** on Home, the footer's **Edit** pill, a right-click on a tile,
  **Start → Device → Edit Home** or the new **Edit Home** row in Settings —
  moves a tile with **A** and the D-pad, cycles its size with **X**, removes
  it with **Y** (Delete), adds tiles from a sheet of widgets, cards and apps,
  switches layout with **L1**/**R1** (Tab) or the strip at the top, and
  resets to the default tiles; every change is saved as it is made, in the
  `homeTiles` setting. The mouse edits too: drag a tile, click its remove
  badge or its size chip. Experience packs can name the new layouts, and the
  theme and pack galleries draw a sketch of each
  ([player/homelayout.go](player/homelayout.go),
  [player/home_tiles.go](player/home_tiles.go),
  [player/home_edit.go](player/home_edit.go),
  [player/home_widgetdraw.go](player/home_widgetdraw.go),
  [docs/CONTROLS.md](docs/CONTROLS.md#home-layouts),
  [docs/CONFIGURATION.md](docs/CONFIGURATION.md#home-tiles-hometiles)).

- **Files extracts archives.** **A** on a zip, a tar (plain, `.tar.gz` /
  `.tgz`, `.tar.xz`, `.tar.bz2`), a lone `.gz`, `.xz` or `.bz2`, a 7z or a RAR
  opens its sheet: the format and size and, for a zip (or a 7z or RAR that
  7-Zip can list), how many files it holds, how big it unpacks and the first
  few things in it. **Extract** puts a single top-level item (one folder, one
  game) next to the archive and several into a new folder named after it, and
  never overwrites anything — a taken name gets " (2)", as Paste does.
  **Extract here** merges into the archive's own folder and replaces files of
  the same name, after a second press; every move is decided before the first
  is made, and the archive itself is never replaced. A zipped game in a
  console folder still starts on **A**; **Y → Extract…** unpacks it, or every
  marked archive one after another (the new row sits under **Edit text**, so
  the rows below it moved down one). The work runs in the background with its
  progress in the sheet, along the bottom of the Files bar and in the activity
  strip on other screens, and can be cancelled; a cancel or a failure while it
  unpacks leaves the folder exactly as it was, because everything is unpacked
  into a hidden folder first and moved into place at the end, and after
  **Extract** the cursor lands on what came out when it was still on the
  archive. The built-in readers make names safe for FAT cards and Windows
  (`a:b.txt` → `a_b.txt`, `CON` → `_CON`), skip a name that climbs out with
  `..` and keep an absolute one inside, leave links, devices and macOS
  metadata out (a tar's hard link becomes a copy) and hold every member to the
  bytes it declares; 32 MB of the card are kept free (a zip is checked before
  it starts, a tar as it goes, a 7z or RAR from 7-Zip's listing, and 7-Zip is
  stopped when the card gets down to it); an `.xz`'s container is read by
  JukaHub itself, with its checks verified, so no block can make the decoder
  take more than 128 MB of memory (one made with a BCJ or delta filter is
  refused); old DOS zips' names are read as code page 437, and file dates are
  kept. 7z, RAR
  and zips packed with something other than Deflate go to 7-Zip —
  JukaCrossMix's `7zz`, an installed 7-Zip on Windows (else 7-Zip's own
  `7zr.exe`, fetched once and checked against its published digest, for 7z),
  or Windows' `tar.exe` for RAR — after 7-Zip has listed them: an archive it
  cannot list, or one holding links, is not unpacked, and the sheet says why,
  as it does when no 7-Zip is there. Password-protected archives are refused
  with that reason. This
  replaces **A** on a zip, which unpacked it on the render thread into
  `name_unzipped` without a word about how it went
  ([player/files_extract.go](player/files_extract.go),
  [player/files_extract_ui.go](player/files_extract_ui.go),
  [player/files_extract_tools.go](player/files_extract_tools.go),
  [docs/CONTROLS.md](docs/CONTROLS.md#archives)).

- **The Games Library opens without walking the card on the render
  thread.** The scan and build now run in the background, with a scanning
  state, a cached filtered list, and stale-result protection when the panel
  closes or rescans ([player/game_library_ui.go](player/game_library_ui.go)).

- **The library remembers how you like to browse.** Games can be sorted
  by title, last played, playtime, rating or year; hidden with a Show hidden
  toggle; and given a display title without renaming the ROM. The view is
  keyed by a content hash, so it survives a file rename
  ([player/game_library_view.go](player/game_library_view.go),
  [player/game_options_ui.go](player/game_options_ui.go)).

- **Plugin ROM formats reach the library.** A switched-on plugin's
  `romParsers` extensions and system folders are now indexed by the ROM scan,
  including `.zip` sets, while built-in systems and extensions keep precedence
  ([player/romsetup.go](player/romsetup.go)).

- **Settings search opens the exact row.** Home search indexes every
  Settings row by label, detail line and id, so "vibration" or "button hints"
  lands on that row with the focus on it instead of the top of its section
  ([player/setui.go](player/setui.go),
  [player/homesearch_index.go](player/homesearch_index.go)).

- **The Browser runs page JavaScript against a real document.** A page's
  scripts used to get a stand-in document whose every lookup answered `null`,
  so a page that builds its content in script — a framework app, a list fetched
  as JSON, lazy-loaded images, a web component — drew as an empty shell. They
  now run against a DOM built from the page (selectors up to `:has()`, events,
  forms, `innerHTML`, `MutationObserver`, custom elements with shadow DOM and
  slots, declarative shadow roots) in a browser's order: classic scripts and
  their `document.write`, deferred and module scripts (`import`/`export`,
  `import()` and import maps, rewritten for the embedded engine, which has no
  modules), async and script-inserted scripts, `DOMContentLoaded` and `load`.
  Timers run on a virtual clock, so a page that waits three seconds before
  showing its content costs no time; `fetch`, `XMLHttpRequest`,
  `document.cookie`, `localStorage` (in memory, per site), `IntersectionObserver`
  (everything counts as visible, so lazy images load) and an `Intl` stand-in are
  there. What the scripts leave is handed to the reader as HTML, with shadow
  roots drawn in place and `data-src` images promoted, and a script that sends
  the page elsewhere is followed like a meta refresh. Like any browser that runs
  JavaScript, the reader now ignores a `<meta>` refresh inside `<noscript>` (the
  usual bounce to a "turn on JavaScript" or `/nojs` page) and runs the page
  instead. Google Search still ends on its card, which now says why: its
  results sit behind a robot check only full desktop and phone browsers pass,
  and the reader will not disguise itself as one. The pass is bounded for
  the handheld — 10 s in all, 6 s for one script, 5 s of patience for a slow
  request, 192 MiB of growth, 4 MiB / 60 000-node pages, 2 s for any one
  regular-expression match — and a page the pass cannot take, or whose scripts
  wipe most of its text, is drawn as before. Under node, jQuery 3, Bootstrap 4,
  Underscore templates, highlight.js and Modernizr run through it; nothing has
  run on the handheld yet
  ([player/webdom_runtime.go](player/webdom_runtime.go),
  [player/webdom/](player/webdom/), [player/webdom_parse.go](player/webdom_parse.go),
  [player/webdom_module.go](player/webdom_module.go),
  [docs/BROWSER.md](docs/BROWSER.md#javascript)).

- **Settings → Browser → JavaScript.** Turns page scripts off — every page is
  then drawn as its server sent it, the quickest test of whether something odd
  on a page came from its scripts — and back on. It is the `browserJavaScript`
  key in `jukauser.json`, on by default
  ([docs/CONFIGURATION.md](docs/CONFIGURATION.md)).

- **Send files in chat.** **X** in Chat (or the paperclip by the composer)
  opens Files to pick files: **A** on a file, or on the rows **X** marked,
  attaches it and comes back, and **B** calls the pick off; Files' **Y** sheet
  has **Send to chat**, and on a PC a file dropped on the window attaches too.
  Attached files wait above the composer (**Up** reaches them, **A** takes one
  off) and go with the next message, words optional. In the community tab they
  go to the Discord channel as real attachments, up to 10 a message and 20 MB
  each (50 / 100 MB on a boosted server), with the progress under the bubble;
  the upload streams from the card instead of loading the file into memory,
  keeps the messages in the order they were written, waits out a dropped
  connection and tries again without posting twice, can be cancelled (deleting
  a message while it goes out takes it back off the channel), and says why when
  Discord refuses a file. Files other people send now show as cards — not just
  the first picture: a picture or a clip is viewed or played from the chat,
  anything else is saved into Downloads (the SD card's `Downloads` folder on
  the handheld) and opened from there; **Files** lists a message's files and
  **Download** saves them, and expired Discord file links are renewed. A script
  in Downloads now runs from Files on a second press only. Juka AI reads text
  files: attach a log or a config and ask about it (the start and the end of a
  long file go with the question, and follow-ups keep it). Saved keeps
  references to files on this device. Renaming yourself moved off **X**: press
  **R** on a keyboard, click your name, or open your own card in People, which
  is there before you have written anything. The `send_to_chat` and
  `attach_image` triggers send real files now
  ([player/chatfiles.go](player/chatfiles.go),
  [player/chatupload.go](player/chatupload.go),
  [player/chatfilesview.go](player/chatfilesview.go),
  [docs/CONTROLS.md](docs/CONTROLS.md#chat)).

- **Light & dark by time of day.** Settings → Display & theme → **Light &
  dark by time of day** switches between a day theme and a night theme at two
  times you choose (07:00 and 19:00 to start, in half-hour steps, a window
  across midnight included); the two themes have rows of their own. It replaces
  the four fixed palettes the switch used to cycle through every hour, which
  undid a theme picked by hand within the hour: the theme now changes at the
  two switch times only, and a colour theme picked by hand becomes the theme of
  that part of the day. High contrast, the colour-blind palettes, a total
  conversion and the battery saver's OLED pause it, and a switch time that
  passes during a game waits for the game to end
  ([player/themeschedule.go](player/themeschedule.go)).

- **JukaHub starts with your TrimUI.** The first time JukaHub starts on a
  TrimUI where nobody has set it, **Start JukaHub at login** turns on by itself
  and the boot hook is written. The first-run tour has a new **Start with your
  device** step that says so and turns it off (on a desktop the same step
  offers to turn it on); when the tour is skipped before that step, a toast
  says it. It happens once per device (`autoStartDefaulted`, kept out of
  exported configs): turning it off is final, and a value set before is never
  overridden. The boot hook no longer starts a second JukaHub when one is
  already running ([player/autostart_firstrun.go](player/autostart_firstrun.go),
  [player/autostart.go](player/autostart.go)).

- **Plugins do something when you switch them on.** Switching a plugin on
  now runs it straight away and shows the result ("Clock is on: Mon 09 Nov ·
  18:41"); its buttons join a new **Plugins** group in the Start menu (and Home
  search), so they work from any screen; switching it off says what went with
  it. The Daily quote greets you on the first start of each day. Theme, pack
  and controls plugins get one-press buttons — **Use it now** / **Back to my
  theme** for Neon Grid, **Apply it now** / **Undo** for Pocket Classic, **Use
  Couch remote** / **Use default controls** — and Quick notes' **Add note**
  opens the keyboard. All thirteen bundled plugins now also run in Go, so on
  Windows (no `sh`) they work instead of saying "Can't run here". Manifests
  gain optional `onEnable`, `onStartup`, `startupDaily`, `quick` and
  JukaHub-run `@` actions; older installed copies of the bundled plugins are
  upgraded in place, keeping your notes
  ([player/plughooks.go](player/plughooks.go),
  [player/plugnative.go](player/plugnative.go),
  [docs/PLUGINS.md](docs/PLUGINS.md#what-switching-a-plugin-on-does)).
- **Choose the browser's search engine and home page.** Settings → **Browser**
  sets where a search goes — DuckDuckGo (still the default), Google, Bing,
  Startpage, Brave Search, Yahoo, Mojeek, Wikipedia, or a results address of
  your own with `%s` for the words — and which page **Home** opens: the engine's
  start page, the page open in the Browser, a saved page, or an address you
  type (applied on Save only, so B keeps the old one). **Y** in Saved pages
  makes the highlighted page home. The address bar, the Search chip and Y
  (whose keyboard now names the engine), and Home search's "Search the web" all
  follow the choice. Google shows results only to browsers
  that pass its JavaScript test, which the reader does not, so a search lands on a
  card instead of Google's "turn on JavaScript" page: the same words on
  Startpage (Google's results as plain HTML), Startpage from now on, or — on a
  PC — the desktop's own browser. Result links from DuckDuckGo, Google, Bing and
  Yahoo now open their destination directly, which is what makes Bing's results
  open at all ([player/browser_engines.go](player/browser_engines.go),
  [docs/BROWSER.md](docs/BROWSER.md#search-engine-and-home-page)).

- **Stick calibration and dead zones.** Settings → Controls → **Calibrate
  sticks** measures both sticks in about ten seconds (let go, roll them round
  their edges, let go again): where each one rests, how much it jitters there
  and how far it reaches. Stick navigation, the virtual cursor and the
  screensaver / frame-rate wake read the sticks through it, so a drifting stick
  no longer steps the focus, creeps the pointer or keeps the screen awake, and
  a stick that falls short of its rim still reaches full tilt. **Left / Right
  stick dead zone** set each one by hand; the Controller test draws the
  calibrated rings. The firmware's calibration app still sets the hardware
  range for games ([player/stickcal.go](player/stickcal.go),
  [player/stickcal_ui.go](player/stickcal_ui.go)).
- **Virtual cursor on the right stick.** Moving the right stick brings up a
  mouse pointer and moves it; A clicks (hold to drag) and L2 / R2 scroll under
  it. The left stick stays the D-pad's twin, and a D-pad press or left-stick
  step puts the pointer away, as do 30 s without the right stick. It drives
  the app through ordinary mouse events, so hover, clicks, drags and the wheel
  work wherever a mouse does; it is never drawn into screenshots. Chat keeps
  its right-stick scrolling; Settings → Controls → Virtual cursor turns it off
  ([player/virtualcursor.go](player/virtualcursor.go)).
- **Screenshot mode, for pictures you can publish.** `JukaHub
  --screenshot-tour ../screenshots` (run from `player/`) opens Home, Media,
  Settings and the dock in turn, saves each as a 1280×720 PNG and quits;
  `--screenshot-mode` is the same session by hand, with F12 saving PNGs. In
  both, the hints name the pad's buttons, every clock reads 9:41, no weather
  is drawn, the LAN address and the pairing QR use 192.168.1.42, the Wi-Fi
  name reads HomeNet, and the user name, wallpaper, toasts, recent searches,
  update offer and the dock's chat and photos are left out; the dock's alarm
  reads 10:45. It is how the README's screenshots are meant to be retaken
  ([player/screenshotmode.go](player/screenshotmode.go),
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#screenshots)).

- **M3U8 playlists go both ways.** Audio → Settings → Playlists exports the
  selected list as `.m3u8` with **Y**, and opening an `.m3u8`/`.m3u` in Files
  imports it as a named playlist and starts it; relative entries resolve
  against the playlist's folder, `#EXTINF` titles and durations are read, and
  missing files are skipped with a count
  ([player/musicm3u8.go](player/musicm3u8.go),
  [player/musicsettings.go](player/musicsettings.go),
  [player/features.go](player/features.go)).

- **Settings → Date & time.** One place for the clock: *Set time
  automatically*, *Time zone*, *12-hour clock* (moved here from Display &
  theme, same setting), *Date format* (Auto from your region, 9 Nov 2026,
  Nov 9, 2026, 2026-11-09 or 09.11.2026) and *Clock last set*, which says when
  and how JukaHub last set the clock. On Windows, macOS and Linux desktops the
  zone and clock rows show what the system uses and open its own settings.
  Home search finds it under "date", "time zone" or "12 hour"
  ([player/datetime.go](player/datetime.go),
  [player/setmodel.go](player/setmodel.go),
  [player/clockformat.go](player/clockformat.go)).

- **A time-zone picker on the handheld.** The IANA time zones grouped by region
  with their current UTC offset, the zone in force ticked, and search by city,
  country or an old name (Calcutta, Kiev) — type on a keyboard, or press Y for
  the on-screen keyboard; L1/R1 jump between regions. *Set automatically*
  takes the zone from the same location lookup the weather uses, and never
  overrides a zone you picked while it was looking. The zone applies to
  JukaHub at once and is kept on the card (`jukatimezone`); `launch.sh`
  exports it as `TZ`, so games started from JukaHub use it from the next
  start. The zone database is built into the Linux binary, so this works on
  images without `/usr/share/zoneinfo`
  ([player/tzpicker.go](player/tzpicker.go),
  [player/tzzones.go](player/tzzones.go),
  [player/tzdata_linux.go](player/tzdata_linux.go),
  [player/launch.sh](player/launch.sh)).

- **The clock sets itself from the network.** Each time Wi-Fi comes online,
  and hourly while it stays, JukaHub asks an internet time server
  (pool.ntp.org, then Cloudflare and Google; the HTTPS date of the
  connectivity check if none answers) and sets the clock when it is more than
  two seconds off — only on the handheld, where nothing else does. A
  correction of more than ten minutes (counted with the unconfirmed ones
  before it, of which ten minutes a day are forgiven) is made only when the
  HTTPS date confirms it, so a forged time-server answer on the Wi-Fi cannot
  move the clock far; on a firmware with no CA certificates at all, where
  HTTPS cannot be checked, a clock still dated before this release is set from
  the time server alone. A failed attempt is retried after 1, 2, 5, 10 and 30
  minutes.
  Windows, macOS and Linux PCs keep their own time; JukaHub never asks a time
  server there by itself. Nothing is sent offline, in airplane mode or behind
  a captive portal. After the clock or the zone changes, a snoozed alarm moves
  with the clock and still rings, and playtime recorded under a wrong date
  moves to the right day (never more than the change can have misdated)
  ([player/ntpsync.go](player/ntpsync.go),
  [player/clockjump.go](player/clockjump.go),
  [player/netmon.go](player/netmon.go)).

- **JukaHub saves a crash report and offers it at the next start.** Every
  session hands a crash file to the Go runtime, so a panic on any goroutine or
  a fatal error is written down next to the logs (TrimUI: the app folder beside
  `errors.txt`; Windows: `%LOCALAPPDATA%\JukaHub\logs`). The next start files it
  as `crash-<time>.txt` (the newest five are kept, and the newest is never
  pruned even when the clock is behind) and shows "JukaHub closed
  unexpectedly" with Send report (Report a bug, prefilled; nothing is sent
  until you press Submit), Restart in safe mode and Recovery options
  ([player/crashreport.go](player/crashreport.go),
  [player/recoverycard.go](player/recoverycard.go)).

- **The recovery chord opens a recovery card.** Holding L + R + Start
  (Ctrl + Alt + R on a keyboard) while JukaHub starts no longer replaces the
  binary on its own; it offers the previous version (the one released before
  this build in Version history; on older installs the build an update backup
  kept, with Restart now), the last good settings (what the last healthy start
  read, then the settings' `.bak` or the newest weekly backup), staying in
  safe mode, and Reset settings (keep games and saves). The settings choices
  ask first and write a safety backup before anything changes, a crash report
  filed at that start can be sent from the same card, and holding the chord
  keeps the automatic rollback away: the card says the last starts failed and
  offers the previous version first. It works with the pad, a keyboard and a
  tap ([player/safeboot.go](player/safeboot.go),
  [player/recoverycard.go](player/recoverycard.go),
  [player/recoverysettings.go](player/recoverysettings.go)).

- **Three failed starts in a row start safe mode by themselves**, whatever the
  update history, with a card that says why and how to leave it. A start
  counts as healthy once it has drawn its first frame and run for 90 seconds,
  or quit cleanly, and a start that was asked to stop is not counted; the
  check runs before the settings load, so a crash
  while loading them counts too ([player/bootguard.go](player/bootguard.go)).

- **A healthy start keeps the settings it started with.** A start that runs
  90 seconds outside safe mode keeps the settings files it read at boot in
  `patch/lastgood/`; *Last good settings* on the recovery card offers them
  first, because the `.bak` beside each file is rewritten by every save
  ([player/bootguard.go](player/bootguard.go),
  [player/recoverysettings.go](player/recoverysettings.go)).

- **Roll back a package.** The previous version of an updated package is kept,
  and its page offers **Roll back to v…**, which swaps the two; the button then
  reads **Switch back to v…**. Files the app or you wrote into its folder
  follow the installed version — if some cannot move, the next update says
  which, and after three tries keeps that old copy whole in
  `packages/.jukahub-rollback/` rather than block or delete it (as it also
  does when the installed version's list of its files cannot be read) — and an
  install cut short by a power loss is finished or undone the next time
  Packages opens. Removing
  a package moves every copy into the trash by rename before deleting it
  ([player/pkgstage.go](player/pkgstage.go),
  [player/pkgstate.go](player/pkgstate.go), [player/pkgui.go](player/pkgui.go)).

- **Save states made with another core build are flagged.** JukaHub now
  records which build of each core was active when, and a game's page says in
  one line beside **SAVE STATES** when some states were written by a build
  other than the installed one — the cue to roll the core back with **R1**.
  Times from a clock that was wrong (before 2020, as the handheld reads before
  it reaches the network) are never used
  ([player/corebuilds_history.go](player/corebuilds_history.go),
  [player/gmeta_ui.go](player/gmeta_ui.go)).

- **See who is connected to FTP and SFTP.** The Network tools page lists every
  connected computer, since when and what it is uploading or downloading, and
  the tiles show "· 2 connected"
  ([player/nettoolsscreen.go](player/nettoolsscreen.go)).

- **Android games can present through the firmware's own SDL2.** A public
  report from the stock TrimUI firmware says raw EGL contexts on SDL's `mali`
  display stayed black while SDL-created ones showed, so AndroidRT has a new
  present backend, `ANDROIDPAK_PRESENT=sdl`: the firmware's
  `libSDL2-2.0.so.0` (loaded at run time, never linked; `ANDROIDPAK_SDL_LIB`
  overrides) creates the fullscreen window, its EGL surface and the GLES
  context, and the game gets exactly those — `eglMakeCurrent` becomes
  `SDL_GL_MakeCurrent`, every `eglSwapBuffers` an `SDL_GL_SwapWindow`, and SDL
  is pointed at the game's own libEGL and the GLES library beside it, with its
  signal handlers off so JukaHub can still stop the game. `auto` switches to
  it by itself when the native backend fails — the game's own `eglInitialize`
  of the default display fails (SDL starts there and the call is made once
  more) or EGL refuses the window surface — and logs `backend sdl (fallback:
  …)`; a game that made its GLES context before that window keeps it as a raw
  EGL context, and the log names `ANDROIDPAK_PRESENT=sdl` for it.
  Mock-tested with a fake firmware SDL2 (186 smoke assertions); not yet run on
  a device ([player/androidrt/src/eglpresent.c](player/androidrt/src/eglpresent.c),
  [player/androidrt/src/sdlhost.h](player/androidrt/src/sdlhost.h),
  [docs/ANDROID.md](docs/ANDROID.md)).

- **Hints follow Accessibility's button remaps.** With the Nintendo layout, an
  A/B or X/Y swap or a per-button remap, every hint now names the physical
  button that does the job ("B Open" when A and B are swapped) instead of the
  button that now does something else. An action no button sends any more
  (its button switched off, or moved to another job with nothing in its
  place) reads "—", and an alternative with no button is dropped ("B / Start"
  with Start switched off reads "B"). Screens that keep hint text between
  frames (keyboard languages, clipboard history, the on-screen keyboard, the
  Wi-Fi and Network tools sheets, the recovery card) rebuild it when the remap
  changes. Keyboard keys, the Select + Start quit chord and the controller
  test (which read physical buttons) are left as printed
  ([player/inputhints.go](player/inputhints.go),
  [player/inputhints_test.go](player/inputhints_test.go)).

- **Sentences that name a button speak the hints' words.** A new
  `hintSentence` helper writes prose such as "Press A to retry" as "Press
  Enter to retry" on a keyboard and follows a remap; the empty states and
  status lines of Media, Live TV, Files, Downloads, the arcade, Alarm,
  Accessibility, Compatibility, Game profiles, Curation, Patch, the browser's
  link, saved-page and cookie pickers and site info now use it
  ([player/inputhints.go](player/inputhints.go) and the screens' files).

- **A volume button in the top-right corner.** A speaker now leads the
  status cluster, immediately left of the Wi-Fi meter, on Home and on every
  screen, and the D-pad reaches it like the meter. It shows the level at a
  glance (a speaker with a cross when muted); A, a click or a tap opens a
  glass volume card with the level, a slider to click or drag and a mute
  button. The D-pad, the arrow keys or the wheel move the level in 5% steps,
  A (Enter) mutes and un-mutes, and B (Esc) closes. Mixer writes run on one
  worker that applies only the newest level, so a held D-pad never stalls the
  screen, and the level follows the hardware volume keys. On the TrimUI it
  drives the codec itself (see Fixed); on Windows it is JukaHub's own entry in
  the Windows volume mixer, not the whole PC; on a Linux PC it uses PulseAudio
  or ALSA as before. Where no mixer is available the button is not drawn
  ([player/volumepopover.go](player/volumepopover.go),
  [player/volumectl.go](player/volumectl.go),
  [player/statusbar_status.go](player/statusbar_status.go),
  [player/volume_windows.go](player/volume_windows.go)).

- **The Home pairing code is a control.** The D-pad reaches the QR in the
  header (Right from the PS5 launcher, Down from the bell), and A, a click or
  a tap shows it full screen at full brightness, with the PIN and address
  under it for a phone that cannot scan; A or B closes it and the backlight
  goes back. A click used to open the Phone Companion card, which is still in
  Settings, Network tools and search. With no code drawn, those presses go
  where they always went ([player/homefocus.go](player/homefocus.go),
  [player/homelayout.go](player/homelayout.go),
  [player/qrzoom.go](player/qrzoom.go)).

- **yt-dlp keeps itself up to date.** It was installed once and never
  updated, so every YouTube change broke Media until the binary was deleted
  by hand. Opening Media now checks for a newer release when the installed
  one is more than ten days old (at most twice a day), and a failure an update
  can fix (a 403, no playable format, the bot check, an extractor error)
  checks at once, at most every 90 minutes. The download waits for a playing
  video to end, is checked against the release's SHA-256 list and keeps the
  previous copy; the new one is run once, and a release that cannot run on the
  device is put back and skipped for 30 days. A search that failed in the last
  three minutes runs again by itself once the update lands, and a yt-dlp you
  installed yourself is never replaced. The tools folder also goes first on
  the helper's PATH, so yt-dlp finds ffmpeg and a JavaScript runtime (Deno)
  dropped next to it ([player/ytdlp_update.go](player/ytdlp_update.go),
  [player/patch_engine.go](player/patch_engine.go)).

- **Wake your PS4 or PS5 from the handheld.** A console in rest mode no longer
  has to be switched on by hand before Remote Play: A on its row asks the
  console how it is, sends the wake signal when it rests, waits while it comes
  up (the row counts the seconds, up to a minute) and starts the stream by
  itself; X wakes a console without streaming, and B stops waiting. The row
  says when a console is in rest mode and when it did not answer — A then
  connects anyway, for consoles behind a VPN. A console with an Internet
  address is also asked there when it does not answer at home, so a router
  that forwards its discovery port lets it be woken from away. The signal is
  chiaki-ng's own WAKEUP packet with the pairing key as its credential, sent
  only to an address that has just answered as this console (its MAC must
  match the pairing's), never broadcast; the console needs Stay Connected to
  the Internet and Enable Turning On PS5/PS4 from Network in its rest-mode
  settings, and a failed wake names that setting
  ([player/psremote_wake.go](player/psremote_wake.go),
  [player/psremote_ui.go](player/psremote_ui.go); tests in
  [player/psremote_wake_test.go](player/psremote_wake_test.go)).

- **Pair Bluetooth controllers and headphones in JukaHub.** Settings →
  Network → Bluetooth devices (also **Bluetooth** in the Start menu, search,
  and the PS Remote Play panel's new **Controllers** row) switches the radio,
  lists the paired devices, searches for ten seconds and pairs, trusts and
  connects a device in one step — controllers first, with a sheet that says
  which buttons put a DualSense, a DualShock 4, an Xbox or an 8BitDo pad in
  pairing mode. Every bluetoothctl conversation runs off the render thread
  with its own deadline (the stock BusyBox has no `timeout`), in a
  pseudo-terminal, because a search and a pairing last only as long as the
  client that asked for them. A pad is called ready only when the kernel has
  made a controller for it; one that connects without one — what firmware
  with Bluetooth LE input only can do with a classic-Bluetooth DualShock 4 or
  DualSense — shows **No input**, with a USB cable or a 2.4 GHz adapter as the
  way out. The page and the Controllers row also say that Bluetooth shares the
  radio with Wi-Fi here, so a stream can stutter while it is on
  ([player/bluetooth_devices.go](player/bluetooth_devices.go),
  [player/bluetooth_devices_ui.go](player/bluetooth_devices_ui.go); tests in
  [player/bluetooth_devices_test.go](player/bluetooth_devices_test.go)).

- **Remote Play uses every controller, including one connected mid-stream.**
  The stream helper opened one controller as it started — the first one SDL
  listed, else joystick 0 — and never looked again, so a Bluetooth or USB pad
  listed after the built-in pad, or switched on during the stream, did
  nothing. `chiaki-stream` now opens every pad (up to six) at the start and
  whenever one connects, lets go of what a pad held when it leaves, and sends
  the console the merge of all of them: any pad's buttons, each stick axis
  from the pad pushed furthest on it (the way chiaki-ng merges pads, so a
  resting pad does not cancel the one in use) and the deeper trigger. An
  external pad keeps its own layout while the built-in pad is there: its PS
  button is the console's PS button for as long as it is held (the stream
  menu stays on the handheld's Menu), its D-pad stays a D-pad when the
  Brick's *D-pad = left stick* mode is on, and a DualShock 4's or DualSense's
  Create is Create while its touchpad passes the click and up to two fingers.
  The built-in pad is told apart by its USB id and the kernel's name for it
  ("TRIMUI Player1"), because SDL names it "Xbox 360 Controller" after that
  id; that is what keeps the handheld's motor rumbling and the handheld-only
  options on the handheld's own pad. Rumble goes to the pad in use, handed to
  the main loop instead of called from chiaki's thread, and is sent again
  every second so a long rumble does not stop after SDL's ten seconds.
  Start + Select ends the stream only when both are held on one pad; the
  fallback that read raw buttons 8 and 9 as Select and Start now applies only
  to raw joysticks SDL has no mapping for, because on the TrimUI's own
  X360-mapped pad those numbers are Menu and L3 and on a DualSense they are
  other buttons. Pads with Nintendo labels are read by position, so Cross is
  the bottom button ([player/native/chiaki-stream.c](player/native/chiaki-stream.c),
  [player/native/stream_input.c](player/native/stream_input.c),
  [player/native/stream_sys.c](player/native/stream_sys.c); host tests in
  [player/native/tests/](player/native/tests/)).

- **PS Remote Play maps every DualSense input.** The face buttons go by
  position, so the bottom button is Cross whatever letter the TrimUI prints on
  it. A **Menu** tap is the PS button and the stream menu's **Hold PS (Home)**
  gives the long press (the PS5's home screen, the PS4's quick menu);
  **Select** is the touchpad click and **Menu + Select** Create (the two swap
  in the stream menu or with the panel's new **Select button** row);
  **Menu + D-pad** swipes the touchpad, **Menu + right stick** puts a finger on
  it, **Menu + R3** clicks where that finger is, and **Menu + L3** makes the
  D-pad drive the left stick, which is how the stickless Brick and Hammer
  start. L2 and R2 stay analog. The helper registers the handheld pad's X360
  mapping itself, so the layout no longer depends on what the firmware's SDL
  knows, and reads a six-axis raw joystick in that order (the old raw table
  turned its triggers into a right stick pinned to one side). Game rumble now
  also drives the handheld's own motor — a GPIO the old helper never touched —
  whenever the built-in controls are the pad in use, and the motor is switched
  off however the stream ends, a crash or a signal included
  ([player/native/stream_input.c](player/native/stream_input.c),
  [player/native/chiaki-stream.c](player/native/chiaki-stream.c),
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md#controls-in-a-stream)).

- **The Remote Play stream has an on-screen display.** Instead of a black
  screen while it connects, a card names the console, its address and the
  codec and counts the seconds; the controls show for six seconds when the
  picture starts; holding **Menu** for about a second opens the stream menu —
  resume, quit, **Hold PS (Home)**, the Select button, the D-pad mode, rumble,
  the stream's volume, statistics and the controls table — while the console
  is sent an idle pad; toasts say when a mode changes or a pad comes or goes,
  and warn at 15, 10 and 5 % battery, because JukaHub's own warnings cannot
  show while the stream has the screen; holding **Start + Select** on one pad
  fills a bar before it quits, and once both are down neither reaches the
  console until it is let go, so letting go of one early sends nothing; and a
  stream that fails, or that the console ends, says why before it exits. The
  text comes from a glyph atlas generated from the UI font
  ([player/native/stream_osd.c](player/native/stream_osd.c),
  [player/native/gen-osd-font.py](player/native/gen-osd-font.py)).

- **A PlayStation login passcode no longer ends the stream.** A profile that
  asks for its 4-digit passcode when Remote Play starts used to stop the helper
  with exit code 12. The stream now enters the one saved under the console's
  options (**Start** on its row → **Login passcode**) once, or asks for it on
  screen: Up / Down set a digit, Left / Right move, **A** sends it (Start does
  not: it is half of the quit combo) and **B** gives up, also while the console
  is checking it. A refused saved passcode is reported on the next start, and a
  console that ends the connection at the passcode step is reported as that,
  not as one that could not be reached. The passcode field in the console's
  options is hidden like a password and cleared when the keyboard closes
  ([player/native/chiaki-stream.c](player/native/chiaki-stream.c),
  [player/psremote_console_opts.go](player/psremote_console_opts.go)).

- **PS Remote Play from another network.** A console's options take an
  **Internet address**: the home's public address or a dynamic-DNS name, with
  the router forwarding TCP 9295 and UDP 9296–9297 (and UDP 9302 to wake the
  console), or a VPN address: an IPv4 address or a host name (IPv6 does not
  work yet). A port typed with it is left out, with a note when it was not
  9295, since the Remote Play ports are fixed. JukaHub streams to the home
  address when the console answers there and to the internet address when it
  does not, with the same pairing. There is no PSN relay yet — the hole
  punching through Sony's servers that Sony's app and chiaki-ng's "remote
  connection via PSN" use (backlog PSR-8)
  ([player/psremote_stream_opts.go](player/psremote_stream_opts.go),
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md#playing-away-from-home)).

- **Stream settings and console options in PS Remote Play.** The panel gains
  **Video codec** (Auto, H.264 or H.265; a PS4 always sends H.264), **Select
  button** (touchpad click or Create) and **Rumble** rows, and **Start** (O on a
  keyboard) on a console's row opens its options: Play or Pair, Internet
  address, Login passcode, Forget. Both text fields save only on the keyboard's
  Save ([player/psremote_console_opts.go](player/psremote_console_opts.go),
  [player/psremote_stream_opts.go](player/psremote_stream_opts.go)).

- **Remote Play says how a stream ended.** The helper writes its result to
  `Apps/JukaHub/data/psremote-last.txt` — the console could not be reached, the
  passcode was not entered or the console refused the saved one, the video was
  too heavy, the console ended the stream, or a crash with its exit code — and
  the next start of JukaHub turns a bad ending into one toast and a **Last
  stream** row whose **A** opens that console's options. A refused saved
  passcode gets its own toast even after a good stream, and a video the
  handheld could not keep up with names the other codec to try (or a lower
  quality on a PS4). When the helper died before it could write,
  `psremote.log` is read from where that stream began — the note left at the
  hand-off records the log's size — so even a long stream's crash is reported
  with its exit code
  ([player/psremote_lastrun.go](player/psremote_lastrun.go),
  [player/native/stream_sys.c](player/native/stream_sys.c)).

- **Battery time left.** JukaHub now estimates how long the battery lasts and
  how long it takes to fill: a new **Battery** row in Settings → Power &
  battery reads "72% · about 3 h 10 min left" (or "full in about 40 min"),
  Quick Settings' footer shows the longest form that fits ("72% · 3h 10m left"
  on the Smart Pro), and the battery-saver and low-battery toasts say how long
  is left. The estimate comes from the battery's own report when the driver
  has one (the Smart Pro's AXP2202 reports minutes, which is recognised), else
  the current draw, else how fast the level is actually falling over the last
  45 minutes. A rate learned from earlier use, or a driver figure the level has
  not confirmed yet, gives only a rough figure — "roughly 3 h 30 min left",
  "~3h 30m" — that is dropped when the level does not bear it out and never
  goes into a toast. Nothing is shown until there is evidence, a charger that
  cannot keep up reads "still draining", and the figure is smoothed so it does
  not jump with every change of load
  ([player/battery_eta.go](player/battery_eta.go)).

- **Host tests for the Remote Play helper.**
  `sh player/native/tests/run-host-tests.sh` needs only a C compiler: it tests
  the button mapping, the session and status files, the battery reader and the
  motor, and runs the helper's whole `main()` against scripted fakes of SDL,
  chiaki and FFmpeg, under ASan and UBSan
  ([player/native/tests/run-host-tests.sh](player/native/tests/run-host-tests.sh)).

### Changed

- **Remote Cast is now Screen Share.** The Start menu row, the panel and the
  viewer page say Screen Share; the panel is on Home's glass with *Share*,
  *Quality* and *Games too* rows, the link and its code, and a live line (the
  frame rate, kB/s and picture size viewers get). **Y** makes a new link
  without restarting the Phone Companion or changing its PIN, and in safe mode
  or without a network the panel says why sharing cannot start instead of
  switching on a link nothing serves ([player/castui.go](player/castui.go)).
- **Trophies are one press from Home.** The trophy room was injected at
  startup and so sat last in Start → Screens, where testers never found it.
  It is now the row right under Home, with your earned count beside it
  ("12/101"); the Apps screen lists it as a built-in app, the Tools hub tile
  shows "12 of 101 earned", and tapping a trophy card as it pops up opens the
  room (over an open panel the tap only puts the card away)
  ([player/startmenu.go](player/startmenu.go),
  [player/playtime.go](player/playtime.go), [player/apps.go](player/apps.go),
  [player/trophy_celebrate.go](player/trophy_celebrate.go)).
- **In a Remote Play stream, Select is the touchpad and the PS button fires
  when Menu is let go.** Select used to send Share/Create, which games rarely
  use, while the touchpad click (maps, inventories) had no button at all; now
  Select is the touchpad and Menu + Select is Create, swappable. Menu sends the
  PS button when it is released after a tap (up to about a second, nothing
  else pressed), so that a longer hold can open the stream menu, whose **Hold
  PS (Home)** row gives the long press; Start + Select still quits after 1.2 s
  held, now with a bar and only when both are held on one pad. A PS5's
  **Auto** codec is H.264 on the original Smart Pro and the Brick family,
  whose A133P decodes in software, and H.265 only on the Smart Pro S; it used
  to be H.265 on every handheld
  ([player/native/stream_input.c](player/native/stream_input.c),
  [player/psremote_stream_opts.go](player/psremote_stream_opts.go)).
- **The startup splash became the boot's quick hello.** Opening JukaHub from
  a launcher keeps the two-second budget, but the glass card and its list of
  steps (unreadable in the half second a warm start left it) gave way to the
  boot's own picture, compressed: the logo and wordmark come up, the light
  runs round the pane, the blooms come in with the chime's bells, and Home
  arrives card by card in under half a second, starting before the budget
  runs out so a launch takes about as long as before. The shutdown keeps its
  glass card. A pad that connects while the boot holds the event queue (a
  Bluetooth pad pairing at power-on) is now handed to the main loop instead
  of being dropped, and a start right after a reboot no longer counts as a
  warm start (a quick power cycle skipped the greeting)
  ([player/splash.go](player/splash.go), [player/startup.go](player/startup.go)).
- **DuckDuckGo Lite is the default search engine and home page.** Its Lite
  page is built for a simple browser, so a fresh install searches and opens
  Home there; Settings → Browser still offers the other engines and pages
  ([player/browser_engines.go](player/browser_engines.go)).

- **A panic no longer exits as a normal quit.** It exits with status 2 after
  the cleanup still ran (the running game stopped, LEDs and CPU governor put
  back), bounded to 5 seconds — the deadline is armed before anything is
  written — so a crash cannot leave a frozen frame, and `launch.sh` writes
  "JukaHub CRASHED" (or the startup failure, or the signal that ended it) into
  `errors.txt` instead of a bare exit code, leaves that status in
  `.last-exit` for the boot guard, and does not relaunch after a crash ([player/crashreport.go](player/crashreport.go),
  [player/main.go](player/main.go), [player/exitguard.go](player/exitguard.go),
  [player/launch.sh](player/launch.sh)).

- **Core updates ask before they install.** Settings → Games & emulators →
  **Core updates** is now Off / Notify me / Install automatically, default
  Notify me: the daily check says once which cores have a different build and
  installs nothing. An explicit off stays off and an explicit on becomes
  Install automatically; the setting is stored once at start-up, with the old
  switch beside it. Each fetched build's SHA-256 is recorded beside it; the
  buildbot publishes only CRC32 checksums and no stable per-core tree for this
  device, which the docs now say plainly
  ([player/emulatorautoupdate.go](player/emulatorautoupdate.go),
  [player/setmodel.go](player/setmodel.go),
  [player/corebuilds_ui.go](player/corebuilds_ui.go),
  [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md)).

- **A failed Android game's toast names the Java piece it was missing.**
  After a run that ended badly, JukaHub already named the first missing bionic
  symbol; when the game got as far as running, the toast now names the first
  Java method it called that AndroidRT does not implement (else the first
  class it could not get, else the first member it looked up), read from the
  runtime's `[androidrt][jni] report:` — or, when the game crashed before
  printing it, from the first-seen log lines. Loader traps and load failures
  still come first; table-tested
  ([player/androidpak.go](player/androidpak.go),
  [player/androidpak_report_test.go](player/androidpak_report_test.go)).

- **AndroidRT builds without a single compiler warning.** The
  `-Wformat-truncation` spots now check what `snprintf` wrote: an over-long
  asset name or library path is "not found" instead of a cut name that could
  match another file, and an activity name that does not fit is "unknown"
  ([player/androidrt/src/androidshim.c](player/androidrt/src/androidshim.c),
  [player/androidrt/src/apkmeta.c](player/androidrt/src/apkmeta.c),
  [player/androidrt/src/bionicld.c](player/androidrt/src/bionicld.c)).

- **Dates follow one format.** The alarm clock screen and the idle clock dock
  write the date in the order chosen in Settings → Date & time instead of two
  different hard-coded layouts; Network tools' clock check uses the same
  four-timestamp SNTP client as the clock sync, and its Device line is the
  device time of the exchange that answered
  ([player/clockformat.go](player/clockformat.go),
  [player/saver_status.go](player/saver_status.go),
  [player/nettools2.go](player/nettools2.go)).

- **The controls guide lists the keyboard keys screen by screen.**
  `docs/CONTROLS.md` gains a "Keyboard keys, screen by screen" table (pad
  buttons → keyboard keys for about 40 screens) and a paragraph on keyboard
  twins and Accessibility remaps (an action no button sends shows "—"); the
  in-app keyboard sheet lists the new browser, video and alarm keys
  ([docs/CONTROLS.md](docs/CONTROLS.md), [player/shortcuts.go](player/shortcuts.go)).

- **A damaged config no longer stops JukaHub when its backup is good.**
  `LoadLastKnownGood` loaded the `.bak` and still returned an error, and
  startup treated that as fatal before safe mode or the recovery chord could
  run. The backup is now a successful launch: the broken file is kept as
  `jukaconfig.json.corrupt-<time>`, a toast says the settings were restored,
  and a test feeds a truncated config plus a good backup
  ([player/config_health.go](player/config_health.go),
  [player/main.go](player/main.go)).

- **macOS and Windows metadata no longer appear as games, albums or files.**
  Hidden entries (`.DS_Store`, the `._Name` AppleDouble companions),
  `__MACOSX` and `desktop.ini` are skipped by the game library, the media
  index and the text browser through one shared filter
  ([player/filejunk.go](player/filejunk.go)).

- **Scene-change toasts name the screen, not its internal ID** — switching to
  General no longer flashes "SettingsGeneral"
  ([player/core.go](player/core.go)). The trophy card's caption asks for the
  Caption role's size instead of 12 px, which the font helper lifted anyway
  ([player/trophy_celebrate.go](player/trophy_celebrate.go)).

- **The remaining package-level regexps compile on first use.** The 32
  patterns in `compat.go`, `core.go`, `media_paging.go`,
  `nettoolsscreen.go`, `podcasts.go`, `plugx.go`, `sectrustdoc.go`,
  `toolssheet.go` and `webscript.go` now sit behind `sync.OnceValue`, so
  startup does not pay for screens and features that may never open.

- **Synthetic input events are safe on every path.** Events the app pushes
  into SDL itself — the phone Companion's buttons, the left stick's D-pad
  presses, held-direction repeats and touch taps — were bare Go structs that
  `SDL_PushEvent` read as 56-byte `SDL_Event`s, over-reading 20–40 bytes, and
  presses arrived with `State` 0 (a release). Every push now goes through one
  padded constructor that sets the state (`player/sdlevents.go`), the four
  call sites use it, and the race job exercises the queue
  ([player/sdlevents_test.go](player/sdlevents_test.go)).

- **Contributor guide has a first-task path.** Its ten steps point contributors
  to the backlog, the build and run commands, the project checks and how to
  handle work that needs hardware ([CONTRIBUTING.md](CONTRIBUTING.md)).

### Removed

- **The invented "social notifications" feed is gone.** With
  `socialNotifs` on, the app generated fake "Storage 80% full" and "New
  follower" alerts in the real notification centre. The generator, its
  settings row, its panel and its `socialNotifs` key are deleted; older
  configs drop the key instead of re-saving it
  (`player/social.go` removed).

### Fixed

- **The Remote Play helper build gets past chiaki-ng's configure.** Its first
  CI run stopped at `Could NOT find FFMPEG (missing: avformat)`: the pinned
  chiaki-ng asks for libavformat whenever its FFmpeg decoder is on, and the
  static FFmpeg was built without it. `build-helpers.sh` now builds an empty
  libavformat too (no demuxer, muxer or protocol), and rebuilds a cached
  prefix that lacks it ([player/native/build-helpers.sh](player/native/build-helpers.sh)).
- **Remote Play looks for its helpers where an update puts them.** The lookup
  only knew `/mnt/SDCARD/Apps/JukaHub/bin`, while an update unpacks a release's
  `bin/` beside the running binary; it now looks there first. A PS5 pairing
  checks that `chiaki-regist` loads before the handshake instead of failing at
  "start". A failed PS4 pairing no longer turns into a PS5 error: only an
  address no scan has identified tries the PS5 handshake after the PS4 ones
  (and only with the PS5 helper in place, never waiting for it), and its error
  replaces the PS4's only when a PS5 answered and refused. While 192.168.1.60
  pairs, 192.168.1.6's row no longer says it is pairing too. On
  Windows the panel says streaming runs on the TrimUI handhelds instead of
  naming a missing Linux file, and a status row's footer no longer offers an A
  that does nothing
  ([player/psremote.go](player/psremote.go),
  [player/psremote_regist.go](player/psremote_regist.go)).
- **The Smart Pro has a microphone.** A comment in voice typing said it had
  none; it has a mono one. Voice typing and Voice command both use whatever
  capture device SDL lists, so whether it records depends on the firmware
  routing it ([player/vkvoice.go](player/vkvoice.go),
  [docs/VOICE.md](docs/VOICE.md)).
- **A wallpaper picked in the picker is kept.** It was set for the session
  only — nothing saved it — so every restart went back to the stock backdrop;
  it is now kept in `jukauser.json` (`wallpaper`, or `none`) and put back at
  start, and whichever came last, the pick or an experience pack's wallpaper,
  wins. The picker also had no way back to no wallpaper, an unreadable folder
  left it with no rows at all, keys went to the Home tiles behind it when it
  was opened from Home, and on a PC clicks went through it to the screen
  underneath ([player/wallpaper.go](player/wallpaper.go),
  [player/gifwall.go](player/gifwall.go)).
- **Watching the screen no longer stutters the handheld.** Every shared frame
  was JPEG-encoded at full size inside the render loop — well over 100 ms a
  frame on the A53 — for as long as anyone watched. The render loop now only
  reads a frame back when one is due and the encoder is free, and a goroutine
  scales and encodes it; the Phone Companion's screenshot PNG moved off the
  render loop too ([player/castcap.go](player/castcap.go),
  [player/companion_screen.go](player/companion_screen.go)).
- **YouTube subtitles say "no subtitles" much less often, and say why when
  they do.** CC asked yt-dlp for the exact code ("en"), which missed
  `en-US`, `en-GB`, named tracks and the auto-generated original, and a
  format check could fail the whole run. Subtitles now come from YouTube's
  own caption list first (about a second, no helper run): a written track in
  your language, then another region of it, then the auto-generated track,
  then a translation YouTube makes on the fly; yt-dlp is the fallback, asked
  for the code and its regions (without YouTube's machine translations) with
  `--ignore-no-formats-error`, and cancelling it now stops the helper's whole
  process group. Auto-generated captions no longer show every line twice.
  The toast says "Getting English subtitles…" and then how it went — it used
  to say "Subtitles on" over a download that then failed silently. Fetched
  subtitles are cached (`subtitles/` in the data folder), so a replay or a
  quality change shows them at once. A seek no longer cancels a running
  fetch (and no longer left CC unable to start again), and CC stays on for
  the next video until you turn it off — a YouTube video then brings its
  subtitles up by itself (quietly when it has none), anything else waits for
  a press of CC ([player/videosubs.go](player/videosubs.go),
  [player/ytcaptions.go](player/ytcaptions.go)).
- **Leaving JukaHub no longer puts two launchers on the TrimUI's screen.** On
  stock and CrossMix firmware MainUI starts JukaHub's `launch.sh` through
  `/tmp/cmd_to_run.sh` and exits, and the firmware's supervisor
  (`runtrimui.sh`) starts MainUI again the moment the script returns. The
  script used to start a MainUI of its own two seconds before returning, so
  the supervisor's made two, both drawing to `/dev/fb0` — the glitchy,
  flickering screen a tester saw every time they left JukaHub. The supervisor
  now always gets the first go; a watcher left behind starts MainUI only if
  none has appeared ten seconds after the script is gone (a firmware without a
  supervisor, a start over SSH), checking once more right before it does
  ([player/launch.sh](player/launch.sh)).

- **PS Remote Play hands the screen over cleanly and brings you back to
  JukaHub.** The stream's hand-off script had the same double-MainUI ending,
  and passed several MainUI pids to `kill` as one quoted argument. `launch.sh`
  now speaks a screen hand-off protocol (`jukahub-screen-handoff: 1`, exported
  as `JUKAHUB_SCREEN_HANDOFF=1` by the running script): JukaHub leaves the
  helper's pid in `.screen-handoff` as it quits, `launch.sh` waits for it with
  MainUI and the firmware supervisors still stopped — a stop request during the
  wait ends the helper's session first — and then starts JukaHub again, so
  nothing draws over the stream and the stream ends where it started. With an
  older `launch.sh` the script stops and restores the firmware UI itself as
  before, without starting a second MainUI
  ([player/psremote.go](player/psremote.go), [player/launch.sh](player/launch.sh),
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md)).

- **RetroArch started from CrossMix's or the stock menu behaves as it did
  before JukaHub.** Before a RetroArch game, JukaHub turns on the two save-state
  switches behind Continue, points the record toggle at its Recordings folder
  and applies the chosen OSD preset — and those edits stayed in the one global
  `retroarch.cfg`, so after a single JukaHub launch every game started from the
  firmware's own menu also loaded an old auto state and saved one on exit,
  fighting CrossMix's resume and save-on-MENU features. The values now hold for
  JukaHub's launch only: a journal in `data/` records what changed before the
  edit, and the user's values go back when the game exits — only JukaHub's
  keys, and only where they still hold JukaHub's value, so RetroArch's own save
  on exit and a change made in its menu survive. A crash or power cut is
  repaired at the next start, a launch that never started 20 s later, and the
  first start of this version undoes what earlier versions left (from
  `retroarch.cfg.jukahub-backup`). Each line keeps its own line ending, so a
  config with mixed CRLF and LF lines is no longer merged into one line. The
  OSD preset row only stores the choice now. On TrimUI cards
  `RetroArch/retroarch.cfg`, the file beside `ra64.trimui`, is read first, and
  a Windows core launch edits the config beside its `retroarch.exe`
  ([player/racfgscope.go](player/racfgscope.go),
  [player/savestate_resume.go](player/savestate_resume.go),
  [player/recordings.go](player/recordings.go),
  [player/osdpreset.go](player/osdpreset.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#resume-and-auto-save)).

- **Games and emulators start with the firmware's environment, not
  JukaHub's.** `launch.sh` points `HOME`, `PATH` and `LD_LIBRARY_PATH` at
  JukaHub's folder, and every game, emulator and app inherited that: RetroArch
  started from JukaHub loaded the stock `/usr/lib` libraries ahead of
  CrossMix's `/mnt/SDCARD/System/lib`, and launch scripts found JukaHub's tools
  first. `launch.sh` now records the originals (`JUKAHUB_ORIG_ENV` and
  `JUKAHUB_ORIG_<NAME>`, set only for a variable that was set), and every
  screen-taking child gets them back — an unset variable stays unset, and the
  Settings time zone is kept. With an older `launch.sh` only JukaHub's own
  folders are taken out of the paths. The Terminal, Power Tools snippets,
  PortMaster ports, Wine and Android titles keep JukaHub's environment; LAN
  play starts `ra64.trimui` with the firmware's environment too
  ([player/childenv.go](player/childenv.go),
  [player/screengroup_unix.go](player/screengroup_unix.go),
  [player/launch.sh](player/launch.sh)).

- **Local play and per-game options no longer break RetroArch on stock and
  CrossMix launchers.** Their scripts end in `ra64.trimui … "$*"`, which joins
  the ROM path and every option into one argument, so the player-order
  `--appendconfig` or a game's own extra arguments turned the ROM path into a
  file that does not exist. Those scripts now count as not forwarding options,
  and the options are left out for them (a script that forwards with `"$@"`
  still gets them) ([player/hotseat.go](player/hotseat.go),
  [player/game_library.go](player/game_library.go)).

- **Core updates land where RetroArch loads them, and the first one comes
  soon.** An update replaces the active core file in its own folder (it used
  to be copied into the first cores folder, where a core living elsewhere was
  never loaded), cores on the firmware's read-only partition are left alone, a
  fresh card gets its first pass after at most one 20-minute wait for a quiet
  moment, and a pass a game interrupts resumes at the next quiet moment instead
  of waiting a day — while one that could not finish a single core waits for
  the next day instead of downloading again all afternoon
  ([player/compat.go](player/compat.go),
  [player/emulatorautoupdate.go](player/emulatorautoupdate.go)).

- **No more phantom PPSSPP, mGBA and Flycast "updates" on the handheld.** The
  compatibility panel's **L1** check no longer downloads desktop emulator
  releases to `data/emulator-updates/` (none of them publishes a build for
  this firmware, so nothing was ever installed); it checks the libretro cores
  only, and JukaHub's own PPSSPP updates through its card and the daily pass
  ([player/emulatorupdates.go](player/emulatorupdates.go)).

- **The PS Remote Play panel no longer sends people to Link Device for the
  Account-ID, and PS4 pairing asks for it too.** Link Device only shows the
  8-digit PIN. The Account-ID row, the pairing toasts and the PS5 helper's
  error now say where the ID really comes from, and the PIN prompt names the
  right menu: PS5 Settings → System → Remote Play → Link Device, PS4 Settings →
  Remote Play Connection Settings → Add Device. A PS4 on firmware 7.0 or newer
  checks the Account-ID like a PS5, so it now gets the Account-ID card before
  the PIN, and a refusal with reason 80108b02 says the console did not accept
  the Account-ID instead of asking for a fresh PIN that cannot help. The README
  no longer promises Remote Play "on the train": away from home it needs a
  hotspot and a way back to the console (a VPN or port forwarding), which
  [docs/REMOTE_PLAY.md](docs/REMOTE_PLAY.md#playing-away-from-home) now
  explains ([player/psremote_ui.go](player/psremote_ui.go),
  [player/psremote_ps4.go](player/psremote_ps4.go)).

- **PS Remote Play's native helpers finally ship.** No JukaHub release ever
  contained `bin/chiaki-stream` or `bin/chiaki-regist`: the release workflow
  packed no `bin/`, `native/build-helpers.sh` wrote `player/bin` while
  `release.sh` and `package-sdcard.sh` read `native/bin`, and nothing in CI
  built them, so the Remote Play panel said "Helper missing" on every handheld.
  A new `remote-play-helpers` workflow builds both natively on an arm64 runner
  inside a signature-verified Ubuntu 20.04 root (glibc 2.31; the Smart Pro has
  2.33), links FFmpeg (the H.264 and HEVC decoders only), opus, json-c and
  OpenSSL statically, leaves only SDL2 and the C library to the firmware, and
  refuses the result if anything else is needed, if a symbol needs a newer
  glibc, or if a binary does not start. `JukaHub-Trimui.zip`, the `v*` release
  archives and the SD-card package now carry them in `Apps/JukaHub/bin/`; a
  failed helper build costs that run its helpers, never its release
  ([.github/workflows/remote-play-helpers.yml](.github/workflows/remote-play-helpers.yml),
  [player/native/build-helpers.sh](player/native/build-helpers.sh),
  [docs/BUILDING.md](docs/BUILDING.md)).

- **Two stream-helper bugs that ended streams.** With the built-in pad the
  helper read Menu and L3 (raw buttons 8 and 9 of the firmware's X360 mapping)
  as Select and Start for its quit combo, so holding Menu + L3 could end a
  stream and letting go of Menu forgot a held Select; only the pad's own Start
  and Select count now. And a PS5 paired with the generic PS5 target (1000000)
  was refused with "protocol/target mismatch" before it connected
  ([player/native/chiaki-stream.c](player/native/chiaki-stream.c)).

- **Remote Play's PIN fields open on the digits, and the helpers run without
  their execute bit.** The 8-digit pairing PIN and the 4-digit login passcode
  open the keyboard on its number layer, and Play and PS5 pairing set the
  helpers' execute bit first: a copy that dropped it made both fail with
  "permission denied"
  ([player/psremote_stream_opts.go](player/psremote_stream_opts.go),
  [player/psremote_ui.go](player/psremote_ui.go)).

- **The TrimUI firmware keeps hearing its settings file.** JukaHub's writes to
  `/mnt/UDISK/system.json` (the volume's `"vol"` since the volume button, now
  the brightness too) replaced the file with a new one renamed over it. The
  firmware's keymon watches that file with inotify, and a watch on a file
  that is renamed over is lost, so the firmware could stop noticing the file
  — its own later changes included — until a reboot. The file is now
  rewritten in place on the same inode, padded with spaces when it got
  shorter so no stale tail is left, and a file that does not parse (the
  firmware half-way through writing it) is retried instead of being taken as
  "nothing to change"
  ([player/brightness_trimui.go](player/brightness_trimui.go),
  [player/volume_trimui.go](player/volume_trimui.go)).

- **Files' action sheet: a held key no longer confirms what it armed.** Holding
  Enter used to repeat into the second press of **Delete**; repeats are ignored
  now. The sheet's details (file names) are shortened to the room the label
  leaves instead of running under it, and the system-folder confirmation names
  the keyboard's key when that is what is in use
  ([player/files_manager.go](player/files_manager.go)).

- **"Start JukaHub at login" now really boots a TrimUI into JukaHub.** The
  boot hook waited for the stock menu and ran `launch.sh`, which paused the
  firmware's launcher loop and killed MainUI. With its parent paused, the
  killed MainUI stays a zombie, busybox `pidof` keeps listing it, and
  `launch.sh` gave up after eight kills ("MainUI still holds the display")
  without starting JukaHub, so every boot ended on the stock menu. The hook
  now starts JukaHub the way the stock menu starts an app: it queues it in
  `/tmp/cmd_to_run.sh` and closes the menu once it is up, and the firmware's
  loop runs it with its own environment and services and brings the menu back
  when JukaHub exits (a firmware that reads the queue first starts JukaHub
  without showing the menu at all). The hook is safe when the firmware sources
  its boot scripts, starts JukaHub at most once per boot, steps aside when
  another app or a CrossMix resume is queued, and a start that hangs makes the
  next boot stay on the stock menu once instead of locking the device up.
  A card with the old hook gets the new one the next time JukaHub starts,
  `launch.sh` no longer counts a zombie MainUI as running, and
  `Apps/JukaHub/autostart.log` says what happened at each boot
  ([player/autostart.go](player/autostart.go), [player/launch.sh](player/launch.sh),
  [player/trimui/README.md](player/trimui/README.md#starting-automatically-at-boot)).

- **A tag with a stray quote no longer freezes a page load.** The reader's
  attribute scanners stopped advancing on a `>` that a quote had pulled into a
  tag (`<img alt=don't>` with another `'` further down the page), so the load
  spun at full speed until JukaHub was closed
  ([player/browser.go](player/browser.go), [player/webpage.go](player/webpage.go)).

- **Pages no longer draw blank because of attribute or pseudo selectors.** The
  reader's CSS matcher assumed every `[attribute]` and `:pseudo` selector
  matched, so a site's `[hidden] { display: none }`, Vue's `[v-cloak]`, Alpine's
  `[x-cloak]` or a web component's `:not(:defined)` rule hid the whole page,
  `::selection`/`::placeholder`/`::-webkit-scrollbar` rules painted every
  element and `a:hover` coloured every link. Attribute selectors and the
  pseudo-classes a static page can answer are now evaluated, `::before`-style
  rules never style the element itself, a rule that would hide something applies
  only on a certain match, specificity follows CSS, and escaped Tailwind class
  names (`md:hidden`) match. Every `<style>` block now applies to the whole
  page (a `:root` palette declared in one reaches everything), `@media` width,
  height and orientation queries are answered for the handheld's screen (so
  Tailwind's paired `md:hidden`/`max-md:hidden` copies keep one), `:target`
  works, a rule that reveals content on `:hover`/`:focus` still applies, and
  `opacity: 0` hides only on a certain match ([player/webpage.go](player/webpage.go),
  [docs/BROWSER.md](docs/BROWSER.md#what-it-can-do)).

- **The `hidden` attribute hides.** What a page keeps out of sight with
  `hidden` — a dialog, a template, a panel it opens on a click — was drawn as
  ordinary text; it is skipped as in a browser, and `hidden="until-found"` stays
  readable ([player/webpage.go](player/webpage.go)).

- **Long notifications are shown whole.** A toast was cut by pixel count into
  two lines of a 420×96 card, so a message past about 60 characters ended
  mid-word in "…" ("…shares every fil" / "e on the device, system files i…"),
  and at the larger text sizes the 96 px ceiling clipped even the second line.
  The notification centre then stopped every row at two lines, so the end of a
  long error could not be read anywhere. Toasts now wrap at word boundaries to
  four lines in a card that grows to 45% of a wide screen (576 px at 1280, 432
  on the Brick); paths and URLs break after a `/`; and a long toast stays up
  long enough to read (1.5 s plus 50 ms a character, at most 10 s, never
  shorter than before). One that is still too long ends in "…", and a click on
  it opens the centre on that message. In the centre the highlighted
  notification shows its whole message (up to a card-full, some 650
  characters at the default text size), and the list scrolls: the D-pad,
  L1/R1, L2/R2 and the mouse wheel reach all 50 kept notifications, where the
  cursor used to stop at the first card-full or be paged onto rows that were
  never drawn. A notification that arrives while you read keeps the highlight
  on your row, and a toast no longer covers the open centre when its
  notification is already on the card
  ([player/notiftext.go](player/notiftext.go),
  [player/notifications.go](player/notifications.go),
  `player/notiftext_test.go`).

- **A picture attached in Chat left Files in attach mode.** Backing out of
  Files without picking kept the flag set, so the next picture opened in Files,
  however much later, became a chat message instead of opening; and the picture
  never reached the Discord channel at all. Picking is now one visit to Files,
  and what is picked is sent ([player/chatfiles.go](player/chatfiles.go)).
- **Pictures attached in Chat stopped showing after Files moved.** A picture
  was only drawn from inside the location Files was browsing when it was
  attached, so one from Home, Downloads or a drive went blank as soon as Files
  moved on, and after every restart; files the user attached are now drawn
  where they are ([player/chat.go](player/chat.go)).
- **Another device's message no longer vanishes as an "echo".** Every device
  posts through one bot, and a message from somebody else with the same words
  as one of yours, sent within five minutes, was taken for your own coming back
  and dropped; the sender now has to match too, and only a message of yours
  that is going out right now (or whose answer from Discord was lost) is
  matched ([player/chatstate.go](player/chatstate.go)).
- **A chat message's last actions could be out of reach.** On a narrow screen
  the action bar cut off what did not fit — often Delete and Close — and no
  press could get there; the row now follows the focused action and shows "…"
  where more are ([player/chatactions.go](player/chatactions.go)).
- **A video that will not stream no longer flashes black and vanishes.** When
  ffmpeg could not open a stream, the player showed its dark loading picture
  for about a second and then simply closed, with no message — reported as
  "YouTube videos open a black canvas for a second, but then crash". The
  decoder now says how it stopped. A web video (YouTube and every other site
  played through yt-dlp) whose stream would not open is downloaded and played
  from the card instead, with the usual loading card (B cancels) and a toast
  that says so; one whose stream broke off well before its end carries on the
  same way from where it stopped while the player is on screen (floating over
  another screen it only says so, and Play resumes). A file, a download, a
  direct link or a live channel says why it could not play instead ("the
  server refused it (HTTP 403)", "it has no picture"). A page whose stream
  would not open is downloaded first for half an hour, and after two YouTube
  streams in a row that would not open every YouTube video is, so the next
  play does not pay for the failed attempt again (live playlists are still
  streamed). The decoders also ask for a resolved stream as the browser
  yt-dlp, the native client, the CDN check and ffprobe already announced,
  instead of as ffmpeg's own "Lavf/…" — the one request in the chain a CDN
  that refuses ffmpeg's name turned away — and reconnect where a dropped
  connection broke off (a long pause used to end the video there).
  errors.txt gets one line per failure ("decoder for … stopped before its
  first frame: Server returned 403 Forbidden") without the signed media
  address ([player/videorecover.go](player/videorecover.go),
  [player/videoplayer.go](player/videoplayer.go),
  [player/streaming.go](player/streaming.go)).

- **YouTube plays on a stock TrimUI.** The firmware's own `ffmpeg` and
  `ffprobe` (6.1, built without TLS) are on PATH, so JukaHub used them and never
  installed its own, and every stream failed with "https protocol not found". A
  video only played when the stream check gave up and the whole file was
  downloaded instead, which is why YouTube "sometimes" failed. JukaHub now asks
  each tool once whether it opens https (`-protocols`); when it does not, the
  stream reaches it through a loopback relay inside JukaHub — Range requests
  passed through for seeking, HLS playlists rewritten so variants, keys and
  segments follow, cookies kept for the CDNs that check them — with no extra
  download. Radio and podcast streams over https gain the same, and the
  decoders no longer write their banner into `errors.txt` on every start
  ([player/ffmpegtls.go](player/ffmpegtls.go),
  [player/videoplayer.go](player/videoplayer.go),
  [player/musicplayer.go](player/musicplayer.go)).

- **Game details no longer draw over each other.** Every shipped config leaves
  the small and medium text roles at 24px, and the game list's fixed 58px rows
  were short of a title over its "PlayStation Portable · game.iso" line, which
  ran into the next row; the "1-6 of 40" line also sat on the last row. Rows
  are now measured from the type (the detail line a step smaller), the range
  line has its own space, the console name under the cover is fitted to its
  column, and the same overflow is fixed in the Random picker, Game options,
  the Smart Pro Compatibility list and RetroAchievements' recent unlocks
  ([player/game_library_ui.go](player/game_library_ui.go),
  [player/game_random.go](player/game_random.go),
  [player/game_options_ui.go](player/game_options_ui.go),
  [player/compat_ui.go](player/compat_ui.go),
  [player/retroachievements.go](player/retroachievements.go)).

- **Quitting no longer writes a goroutine dump.** On a Smart Pro the exit
  watchdog fired while `SDL_DestroyWindow` was still inside the GPU driver, so
  the last frame stayed up for six seconds and `errors.txt` got every
  goroutine's stack for an ordinary quit. Each SDL teardown call now has its
  own 1.5 s deadline, after which JukaHub runs the cleanup hooks still due (the
  volume flush, a restart Recovery asked for) and leaves with the normal exit
  code; the watchdog skips the dump while SDL is what it is waiting for, and a
  crash keeps its own path ([player/sdlteardown.go](player/sdlteardown.go),
  [player/exitguard.go](player/exitguard.go)).

- **CrossMix's emulators, PPSSPP and RetroArch are found.** An `Emus/<system>/`
  folder only counted when it held `launch.sh`, but the firmware's own menu
  starts the script `config.json` names in `"launch"`, and CrossMix gives each
  default emulator its own (`ppsspp_1.17.1_gl.sh` for PSP, `drastic.sh` for
  NDS), so those consoles read "Unavailable — no launcher" and their games "No
  emulator found". JukaHub now takes that script, then the first `"launchlist"`
  script that exists, then `launch.sh`, and the tile names the script it runs.
  LAN play looked for a file called `retroarch`, but TrimUI's stock and CrossMix
  images ship `ra64.trimui`, so it said "RetroArch not found" on every handheld;
  it now finds `ra64.trimui` and starts it as the firmware does, from its own
  folder with `HOME` set there. Stick calibration and the save manager also look
  in CrossMix's `Emus/PSP/PPSSPP_<version>/.config/ppsspp` (with several builds,
  the ini written last wins), so CrossMix PSP saves are in snapshots, backups and
  sync, and a PPSSPP that has never run says to play a PSP game first instead of
  "not installed". The plain save-folder names that backup and sync keys are
  made of go to one build only, the newest that holds saves, and never away
  from a folder that already had them; other builds are named after their
  folder. A folder FAT reaches under two spellings (`RetroArch/saves`,
  `retroarch/saves`) is one save root, not two. The stock firmware's
  `Roms/PPSSPP` folder now appears in the library as PlayStation Portable
  ([player/emulators.go](player/emulators.go),
  [player/netplay.go](player/netplay.go),
  [player/ppsspp_calibration.go](player/ppsspp_calibration.go),
  [player/savemanager.go](player/savemanager.go),
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#launching)).

- **Fullscreen is one mode everywhere.** Settings triggers, Save and F11
  each chose a different mode — exclusive `SetFullscreen(1)` in three places,
  desktop fullscreen in one — and a Windows start ignored the saved choice.
  Every path now uses desktop fullscreen through one helper, and the next
  start honours the choice ([player/fullscreen.go](player/fullscreen.go)).

- **A huge download size can no longer crash the free-space message.**
  `formatInstallBytes` indexed a four-letter unit table, so a package size above
  1 TB panicked while wording "not enough free space"; it now renders through
  exabytes ([player/installbar.go](player/installbar.go)).

- **A config import's safety backup can no longer prune itself.** The import
  wrote the pre-import archive and then pruned the folder by file time without
  sparing it, so on a handheld whose clock was behind the archive just written
  looked oldest and was deleted; the prune now keeps it
  ([player/cfgdocui.go](player/cfgdocui.go)).

- **Damaged configs and patch databases stay bounded.** A recovered config left
  every damaged predecessor on the card, and two recoveries in the same second
  overwrote each other's evidence; at most five `.corrupt-*` copies are kept,
  newest first, for both the config and `patch/db/db.json`
  ([player/config_health.go](player/config_health.go),
  [player/patch_db.go](player/patch_db.go)).

- **A backup restore reloads the design file once.** `bkupRestoreFiles` told the
  config watcher about an absolute path while the watcher keys the relative
  `jukaconfig.json`, so the poller reloaded and toasted after the restore had
  already installed the config; it now names both
  ([player/bkup.go](player/bkup.go)).

- **The SD-card package ships the one launcher script.** Packaging copied
  `trimui/Apps/JukaHub/launch.sh`, which had drifted from `player/launch.sh`:
  no restart protocol, no `TZ`, no log rotation, no crash lines. The stale copy
  is deleted and every packager ships `player/launch.sh`
  ([player/launch.sh](player/launch.sh),
  [player/package-sdcard.sh](player/package-sdcard.sh)).

- **The boot guard can now roll back a bad update on a handheld without a
  clock.** It counts starts instead of comparing saved clock times, treats a
  build as new for its first ten starts, and runs the rollback through Version
  history after the update state is loaded — before, it read the update time
  before that state was loaded and could never fire. A rollback needs at least
  one real crash among the failed starts — a crash report, or the exit status
  `launch.sh` now leaves for the next start in `.last-exit` (a crash, a fatal
  error, an abort, a segfault or an out-of-memory kill; without it, a start
  that died before its first frame) — so power-offs and quits in a working
  session never roll an update back; a start that was asked to stop (power
  button, stop signal, Quit) is not a failed start even when the exit watchdog
  has to force it during a slow splash; a loop gets one automatic rollback (a restored build
  that fails too gets safe mode); the target is the version released before the
  running one, not the newest saved; and a quit is recorded as soon as the main
  loop ends, before the teardown ([player/bootguard.go](player/bootguard.go)).

- **Rolling back to a backed-up executable works while JukaHub is running.**
  The copy lands beside the binary and is renamed over it, instead of writing
  into a running executable, which Linux and Windows refuse; on Windows the
  replaced build, moved aside as `.old`, is deleted at the next start
  ([player/safeboot.go](player/safeboot.go)).

- **A safety backup can no longer be deleted right after it is written.** A
  backup made before a recovery restore, a reset, a Backup & restore or the
  weekly backup was pruned with the others by file time, so on a handheld
  whose clock was behind the new archive looked oldest and went first; the
  prune now always keeps the one just written ([player/bkup.go](player/bkup.go)).

- **First-run "Connect to Wi-Fi" and "Set your time zone" work on the
  handheld.** Both buttons tried `gnome-control-center` /
  `nm-connection-editor`, which do not exist on TrimUI, so on a new device they
  were dead (Wi-Fi showed a raw `exec … not found` toast). They now open
  JukaHub's Wi-Fi page and the time-zone picker, and the tour waits until you
  come back; desktop tools are used only on a real Linux desktop
  ([player/onboarding.go](player/onboarding.go)).

- **An alarm can no longer be silenced by a clock that ran ahead.** A wrong
  clock could mark an alarm done for a day that had not come yet; after
  network time corrects the clock, such alarms are re-armed
  ([player/clockjump.go](player/clockjump.go), [player/alarm.go](player/alarm.go)).

- **On a Linux computer the file servers no longer take your home folder for
  the SD card.** JukaHub run from `~/Apps/JukaHub` made FTP share
  `~/Documents` and `~/Downloads`; the card layout is now trusted only on an
  identified TrimUI device ([player/fileserver_guard.go](player/fileserver_guard.go)).

- **FTP folders show as folders.** Directory listings marked every folder as a
  plain file, so FTP clients offered to download folders instead of opening
  them; old files now show their year
  ([player/ftp_server.go](player/ftp_server.go)).

- **An endless FTP command line can no longer exhaust memory.** The command
  reader collected a line until its newline, which anyone on the network could
  withhold before signing in ([player/ftp_server.go](player/ftp_server.go)).

- **A failed reinstall no longer removes the package.** Reinstall used to empty
  the folder before downloading; it is now an ordinary staged install, so the
  installed copy keeps working if the download fails
  ([player/pkgstate.go](player/pkgstate.go)).

- **Replacing an emulator core can no longer leave it missing or empty.** The
  old binary was deleted before the new one was renamed in, and the new one
  was written without a sync; it is now fsynced and renamed over the old one
  in one step, its backup is fsynced too, and a backup that failed is deleted
  instead of being offered as a rollback ([player/compat.go](player/compat.go)).

- **A buildbot index with a size column no longer reads as empty.** Lines with
  a fourth (size) field are accepted, on either side of the file name
  ([player/emulatorupdates.go](player/emulatorupdates.go)).

- **A game asking for a missing Java class every frame no longer grows
  memory.** `FindClass` added a report entry on every miss, so such a game
  leaked a little per call and its exit report repeated one class thousands of
  times; misses are now recorded once per name, and the report lists entries
  in the order the game asked
  ([player/androidrt/src/jnienv.c](player/androidrt/src/jnienv.c)).

- **Android games use exactly the EGL library the present layer chose.** The
  game's `libEGL.so` was resolved again through the library search path, which
  could pick another copy than the firmware's `/usr/lib/libEGL.so` (or none,
  for a GPU package outside the path); it is now bound to the present layer's
  own handle ([player/androidrt/src/eglpresent.c](player/androidrt/src/eglpresent.c)).

- **The browser's Search works from a keyboard.** Y or / opens the web search
  (the pad's Y), and the footer names "/" on a keyboard; the zoom hint names the
  minus and plus keys. Keys that open a keyboard (/, Y, F for Find, Space or A
  on the address bar) no longer type their own character into the field they
  opened, and a held one opens it once without typing its repeats; a key that
  types nothing (a Ctrl, Alt or Win chord, a Phone Companion or control-profile
  key) no longer swallows the next character typed
  ([player/browser.go](player/browser.go), [player/core.go](player/core.go),
  [player/extras.go](player/extras.go), [player/brw_offline.go](player/brw_offline.go)).

- **Keyboard X and Y in the video player do what the pad's X and Y do.** X
  toggles captions and Y the settings sheet (C and S still work); X used to
  fall through to the Media screen's search behind the playing video. Holding
  X, Y or F acts once instead of flickering captions, the settings sheet or
  full screen ([player/videoplayer.go](player/videoplayer.go)).

- **Footer hints that named only pad buttons now name their keys.** Playtime
  and the config inspector switch views with Tab, the image viewer zooms with
  the minus and plus keys and steps with PgUp / PgDn, the PDF reader steps
  chapters with [ ] and zooms with minus and plus, game details open options
  with O, Local play moves players with PgUp / PgDn, keyboard languages move
  with [ ], the Tools pages jump sections with PgUp / PgDn, and Patch's
  "Hold A" reads "Hold Enter"
  ([player/inputhints.go](player/inputhints.go),
  [player/trophy_room.go](player/trophy_room.go),
  [player/cfgdocui.go](player/cfgdocui.go),
  [player/imageviewer.go](player/imageviewer.go),
  [player/pdfreader.go](player/pdfreader.go),
  [player/gmeta_ui.go](player/gmeta_ui.go),
  [player/hotseat_ui.go](player/hotseat_ui.go),
  [player/vksettings.go](player/vksettings.go)).

- **Missing keyboard twins added.** Delete removes an alarm (the pad's L1, two
  separate presses: holding Delete does not count twice), Ctrl+Enter sends a
  chat draft (the pad's Start), X ends a Local play game and Y removes one
  player per press, X removes one keyboard language per press and Y adds one,
  and Left / Right page the Settings option sheet like the D-pad
  ([player/alarm.go](player/alarm.go), [player/chatinput.go](player/chatinput.go),
  [player/hotseat_ui.go](player/hotseat_ui.go),
  [player/vksettings.go](player/vksettings.go),
  [player/setsheet.go](player/setsheet.go)).

- **Holding X on the alarm list no longer flips the alarm on and off**
  ([player/alarm.go](player/alarm.go)).

- **The controller test speaks keyboard on a keyboard** ("Reset (Enter)", "Esc
  Close"); on the pad the chip reads "Reset" instead of "Reset (A)" (A only
  registers a press there; click or tap Reset), and its footer is translated
  with no remap, so its pad names stay physical
  ([player/ctrldiag.go](player/ctrldiag.go)).

- **A Wi-Fi login page no longer looks like internet.** The connectivity
  probe was a TCP dial to `1.1.1.1:80`, which a hotel or airport portal
  answers happily: the app said online while every request failed. It now
  requests an HTTP `generate_204` endpoint and requires a 204 with no body;
  a redirect or a page is reported as a captive portal and its sign-in URL
  is kept. The Wi-Fi page then shows a **Sign in** control (and
  "Connected · Sign in required") that opens the portal in the in-app
  Browser, while downloads and updates stay paused until the probe passes
  ([player/netmon.go](player/netmon.go),
  [player/portalsignin.go](player/portalsignin.go)).
- **Opening a link can no longer run a command on Windows.** A config
  trigger reached the shell as `cmd /c start "" <url>`, so `&`, `|` and `^`
  in the URL were parsed as cmd syntax and ordinary query strings were cut at
  the first `&`. Links now go through one allowlist (http, https, mailto) and
  `rundll32 url.dll,FileProtocolHandler`, with a test for
  `https://x/?a=1&calc` ([player/openurl.go](player/openurl.go)). The platform
  file opener uses the same shell-free handler.

- **An Xbox game with no launcher names the fix.** The Games Library now
  says "Install hakuX: Emulators → Xbox → X" where the device is offered the
  port, and the model's own reason on a Smart Pro S or Brick
  ([player/hakuxpage.go](player/hakuxpage.go)).

- **The metadata filter covers every scanner it was meant to.** The
  downloads library, package list, save snapshots, ambient photo slideshow
  and hakuX's Xbox folder now use the shared hidden/junk filter, so
  `._Name`, `__MACOSX` and `desktop.ini` no longer appear as content
  ([player/filejunk.go](player/filejunk.go)).

- **Links opened in the browser from another screen load.** A link from Chat,
  Home search, a paired device or a package page opened the Browser on
  "Loading …" and stopped there: the flag that keeps the start page from
  replacing the link also kept the link from loading once the screen arrived
  ([player/browser.go](player/browser.go)).

- **The video player's settings sheet answers taps where it is drawn.** The
  windowed player drew the sheet inside its own frame but hit-tested it as if
  it filled the screen, so a tap could change a setting it was not on; in full
  screen the sheet no longer fades out while the pad is still using it
  ([player/videochrome.go](player/videochrome.go), [player/videoplayer.go](player/videoplayer.go)).

- **A click no longer seeks a video that is not on screen.** The seek bar's
  last position stayed live after the player shrank to the corner window, so a
  click on the screen underneath could seek the floating video
  ([player/main.go](player/main.go)).

- **Ctrl+Q no longer switches the screen too.** The Q/E screen keys ignored
  modifiers, so the Quick Settings chord also stepped back one screen
  ([player/main.go](player/main.go)).

- **Pomodoro's keyboard matches its footer and the pad.** X skips the phase and
  Esc goes back; Esc used to skip, which left no key to leave the timer by
  ([player/pomodoro.go](player/pomodoro.go)). Likewise Esc now leaves the
  Calculator, which consumed every key and had no way out on a keyboard
  ([player/calculator.go](player/calculator.go)).

- **Notes' shortcut letters stay out of the text.** N (and now Y) start a new
  note and S opens search; the letter's own text event used to follow and land in
  the new title or the query ([player/notes.go](player/notes.go)).

- **Navigation runs at the frame rate it promises.** The main loop slept the
  whole frame budget *after* drawing, so the handheld's 30 fps profile ran at
  about 20 fps while navigating (17 in Low Power). The loop now subtracts the
  frame's own cost while something moves (`FrameRateController.Pace`), and a
  playing video keeps the active cadence even in Low Power instead of dropping to
  ~17 fps after 3 s and ~12 fps after 10 s without input
  ([player/adaptive.go](player/adaptive.go), [player/main.go](player/main.go)).

- **SDL batches draw calls on the device again.** Naming the `opengles2` render
  driver silently turns SDL's batching off, so every blit was its own GL draw
  call on the PowerVR GPU. Batching is requested explicitly on the handheld
  (Windows is unchanged) ([player/main.go](player/main.go)).

- **The live-builder pairing code is no longer in the repository.** The six-digit
  code that guards pushing a design over Wi-Fi was committed, so every clone
  shared it. It is generated per install, as the code always allowed, and is
  git-ignored now ([player/livebuilder.go](player/livebuilder.go)).

- **An EPUB without a title no longer crashes the app.** The reader indexed the
  title match without checking it, so a package document with no `<dc:title>`
  killed JukaHub; the file name stands in now ([player/reader.go](player/reader.go)).

- **The Hero grid fits the Brick.** That arrangement scaled its cards to the
  960-wide canvas twice, leaving a quarter of the screen empty and the gutters
  too thin for the focus ring; on the Hero grid (and on a Home whose tiles the
  owner moved) the D-pad now steps by the drawn geometry, and Up from the top
  row goes to the header instead of sideways ([player/homelayout.go](player/homelayout.go)).

- **An x86-only Android package is refused up front.** It used to be staged
  and launched, then fail inside the runner; it now gets the "no ABI this
  device can run" answer ([player/androidpak.go](player/androidpak.go)).

- **`errors.txt` stops growing on the card.** Every log line was mirrored to
  stderr, which `launch.sh` appends to `errors.txt` forever. Only warnings and
  errors reach it now, and the launcher rotates it to `errors.txt.1` past 1 MiB
  ([player/logging.go](player/logging.go), [player/launch.sh](player/launch.sh)).

- **The handheld's Appearance page has the clock row again.** The desktop
  config grew a Clock label and the 12-hour toggle, and the TrimUI config did
  not, so the handheld shipped an Appearance page two rows shorter and no way to
  switch the clock format ([player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json)).

- **The gallery dialog uses the screen.** `.gallery-content` asked for 1080px
  but inherited `.guide-content`'s 620px cap, so every layout stacked in a single
  column on a desktop and the thumbnails grew taller than the panel. It is
  1080px now (96vw on a small screen) and the grid lays out three cards per row
  ([style.css](style.css)).

- **Home's grid matches the cards again.** Left from Packages stepped over the
  Files card to the Continue hero, and Down from the full-width Media card
  skipped Files for Packages, so the tile that opens the file browser was the
  hardest thing to reach. Up/Down/Left/Right now follow the drawn rows, the
  Continue card's focus ring is four pixels outside a dark hairline so it reads
  over its own thumbnail, and a test pins the graph to the geometry
  ([player/homefocus.go](player/homefocus.go),
  [player/homelayout.go](player/homelayout.go);
  `player/homefocus_grid_test.go`).

- **B leaves Apps and Favorites.** The launcher grid and the favorites list take
  the pad while they are focused, and neither answered B, so on a handheld the
  only way out was a key the pad cannot send. `b` now works on a desktop
  keyboard too, where the grid has no text field to type into
  ([player/appgrid.go](player/appgrid.go), [player/favorites.go](player/favorites.go)).

- **The Windows release build no longer waits for a macOS runner.** The
  `build-and-publish-windows` job cross-compiled on `macos-latest`, the scarcest
  hosted runner, so a release could sit on "Waiting for a runner" long after the
  handheld build had finished, and publishing waits for both. It now
  cross-compiles on `ubuntu-24.04` with the distribution's MinGW-w64 and the same
  checksum-verified SDL2 tarballs, merged into one prefix, and before packaging
  it checks every DLL the exe imports is part of Windows or ships in the archive
  ([.github/workflows/docker-image.yml](.github/workflows/docker-image.yml)).

- **The Windows release build reuses the previous release's compile work.**
  It compiled the Go standard library, every module and the cgo-heavy SDL
  bindings for windows/amd64 from nothing on every release, because its build
  cache was off on purpose: `actions/cache` can be written by any workflow on
  `main`, and a restored cache decides what goes into the published binary. The
  job now starts from the build cache the previous release uploaded as an
  artifact, accepted only from an earlier push-to-main run of the same workflow
  whose commit is on `main`, so a release normally compiles just the player.
  The cache is pruned to what the last build used and uploaded again only when
  `go build -v` shows a dependency was recompiled; a build that fails on a
  restored cache retries once from an empty one, and every cache step is
  time-bounded, so the cache cannot fail or stall a release. The MinGW install,
  the SDL download, `go mod download` (instead of `go mod tidy`, so a release
  builds the committed module graph; CI now warns when `go.mod` is not tidy) and
  the cache download run side by side, apt installs one MinGW flavour (the one
  the build already used) instead of two, and the exe is linked with `-s -w` as
  `build.bat` links it, which also makes the download smaller
  ([.github/workflows/docker-image.yml](.github/workflows/docker-image.yml),
  [.github/scripts/go-build-cache.sh](.github/scripts/go-build-cache.sh), tested
  by [tests/scripts/go-build-cache.test.sh](tests/scripts/go-build-cache.test.sh)
  in `make test-scripts` and in CI).

- **The TrimUI release build is fast too.** The handheld job rebuilt zlib,
  libpng, libjpeg, bzip2, libtiff and libwebp from source on every release
  (leftovers of a fully static link given up in January 2025, which the
  dynamic link never used), ran `go mod tidy`, and compiled everything from
  nothing in the toolchain container. The compile is now its own job,
  `build-trimui`, which starts at once: it pulls the same pinned image while
  `go mod download` and the previous release's build cache download beside it
  (the Windows job's provenance-checked artifact cache, named after the image
  digest), then compiles in the image through `docker run` with no network
  and the runner's Go and both Go caches mounted at their own paths, so a
  release normally compiles only the player. The binary is linked with
  `-s -w`, which also shrinks the download, and a new check refuses one the
  handhelds could not start: not AArch64, another loader, no SDL2 from the
  firmware, or a glibc symbol newer than 2.33. Packaging is a short job that
  waits for the compile, the tag and the Remote Play helpers, instead of the
  compile waiting a quarter of an hour for the helpers, and the helpers are
  reused from an earlier push-to-main run whenever nothing they are built from
  changed (`go-build-cache.sh fetch`, under the same provenance rules). The
  same script builds the binary locally on Linux or WSL 2 with Docker, and the
  SD-card workflow now caches the Go folders it actually uses
  ([.github/workflows/docker-image.yml](.github/workflows/docker-image.yml),
  [.github/scripts/trimui-build.sh](.github/scripts/trimui-build.sh),
  [.github/workflows/remote-play-helpers.yml](.github/workflows/remote-play-helpers.yml),
  [.github/workflows/build-sdcard.yml](.github/workflows/build-sdcard.yml);
  tested by [tests/scripts/trimui-build.test.sh](tests/scripts/trimui-build.test.sh)
  and [tests/scripts/go-build-cache.test.sh](tests/scripts/go-build-cache.test.sh)
  in `make test-scripts` and in CI). `check-workflows.sh` now also fails when a
  job that calls a workflow of this repository grants less than that workflow
  asks for, which GitHub answers by refusing the whole run before it starts.

- **JukaHub-Trimui.zip no longer waits for the Box64 build, and the Remote Play
  helpers build faster and less often.** The helpers' arm64 job had grown into
  one long queue (the Ubuntu 20.04 build root, the helpers, AndroidRT, then
  about ten minutes of Box64 and a Wine start-up check), and the TrimUI
  packaging job waited for all of it, although the Box64 pack is not in the
  archive. Box64 now builds in a workflow of its own on a runner of its own,
  started with everything else, and a small job attaches its pack to the
  release whenever it is ready: neither the TrimUI archive nor the
  publication waits for it (JukaHub installs the pack from the newest release
  that carries one). The reuse keys of the three parts no longer hash the
  workflow file, which every one of the last four pushes had edited: what
  decides a build's output (the build root and the command that builds each
  part in it) moved into `.github/scripts/build-root.sh`, and the keys hash
  that instead, so an edit to a workflow costs no rebuild. In a helper build,
  SDL2, opus, FFmpeg, json-c and libevent now build side by side while
  chiaki-ng and miniupnpc download, instead of one mostly single-threaded
  configure after another (`DEPS_PARALLEL=0` keeps the old order), and the
  build root no longer has dpkg sync every file to disk or unpack manuals,
  documentation and translations no build reads. Tag releases build the
  helpers and Box64 side by side as well, and the SD-card workflow, which does
  not ship the pack, no longer builds Box64 at all
  ([.github/workflows/remote-play-helpers.yml](.github/workflows/remote-play-helpers.yml),
  [.github/workflows/wine-runtime.yml](.github/workflows/wine-runtime.yml),
  [.github/workflows/docker-image.yml](.github/workflows/docker-image.yml),
  [.github/scripts/build-root.sh](.github/scripts/build-root.sh),
  [player/native/build-helpers.sh](player/native/build-helpers.sh)).

- **staticcheck is clean again.** An unconditional `strings.TrimSuffix`
  (S1017), a struct conversion instead of a field-by-field literal (S1016), a
  merged declaration (S1021) and a `delete` without its redundant guard (S1033)
  ([player/files_textview.go](player/files_textview.go),
  [player/musicplaylists.go](player/musicplaylists.go),
  [player/netmon_test.go](player/netmon_test.go),
  [player/themecatalog.go](player/themecatalog.go)).

- **X and Y match their labels on the TrimUI Smart Pro and Brick.** The
  firmware's pad reports button positions the Xbox way under the handhelds'
  Nintendo labels. A and B were already turned back where every press enters
  the app, but X and Y still arrived swapped: the top button ran every "Y"
  action, the left one every "X" action, and the Controller test lit the wrong
  letter. Both pairs now go through one helper at that entry point and in
  every place that queues a press or polls a held button — the phone remote,
  control profiles, JukaLand and the on-screen keyboard's long presses. A new
  **Swap X and Y** switch sits beside **Swap A and B** in Settings → Games &
  emulators (on by default on the handheld, off on Windows) and, like it,
  stays on the device when a backup or design comes from another platform.
  Anyone who remapped X and Y in Accessibility (the Button layout or the
  per-button rows) to work around this should set them back
  ([player/core.go](player/core.go),
  [player/companionui.go](player/companionui.go),
  [player/inmap_runtime.go](player/inmap_runtime.go),
  [player/jland_input.go](player/jland_input.go),
  [player/vkedit.go](player/vkedit.go), [player/setmodel.go](player/setmodel.go),
  [player/bkup.go](player/bkup.go), [player/cfgdoc.go](player/cfgdoc.go)).

- **The PS Remote Play keyboard types again.** While the on-screen keyboard
  was up for a console address, the PSN Account-ID or the pairing PIN, the
  Remote Play panel behind it still took every pad press and key: the D-pad
  moved the hidden list, A ran a row again, and B, Backspace or Esc closed the
  panel under the keyboard. The panel now steps aside while its keyboard is
  open. Around it: a PIN typed with the console's grouping ("1234 5678") is
  accepted; a PIN that is not 8 digits, an Account-ID that is not base64 or an
  address with a space in it goes back to the keyboard with the text still
  there, instead of failing after the handshake times out; an empty Account-ID
  in the middle of a PS5 pairing stops it rather than asking for a PIN the
  console would refuse; an abandoned pairing no longer turns a later
  Account-ID edit into a PIN prompt; a PIN typed while another console is
  pairing says to wait instead of vanishing; the action key reads **Next**
  where it leads on to the PIN; and **Y** forgets a console from a keyboard,
  as the footer says ([player/controller.go](player/controller.go),
  [player/main.go](player/main.go),
  [player/psremote_ui.go](player/psremote_ui.go),
  [player/vkeyboard.go](player/vkeyboard.go)).

- **Four more panels let their keyboard have the input, and the key that
  opens a keyboard no longer types into it.** LAN play's host address, the
  Downloads link field and the Audio panel's playlist name had the Remote
  Play fault on the pad; the Audio panel and the Podcast library had it on a
  USB or Bluetooth keyboard, where Backspace while typing a feed address
  removed a show. Separately, the key that opened a keyboard typed its own
  character into it: X in the Audio panel gave "Playlist 3x", A for a podcast
  feed started the address with "a", and Space on the Remote Play, LAN play,
  Downloads, Podcasts and Audio rows added a space
  ([player/controller.go](player/controller.go),
  [player/main.go](player/main.go),
  [player/vkopenertext.go](player/vkopenertext.go)).

- **Charging shows in the corner.** Plugged in, the battery gauge looked the
  same as on battery. It now turns green with a bolt over the cell while a
  charger is connected, on Home and every screen, and Quick Settings adds
  "Charging" to its battery line. The Smart Pro's battery driver does not
  report "Charging" reliably, so a charger input that is online (axp2202-usb,
  the reading MinUI and NextUI use) now counts too, and a latched "Full" no
  longer shows the bolt on battery. The low-battery guard and the battery
  saver stand down for a cable only while the level holds: a charger too weak
  to keep up no longer silences them, and the warning then says the battery
  is still draining on this charger ([player/widgets.go](player/widgets.go),
  [player/statusbar_status.go](player/statusbar_status.go),
  [player/lowbattery.go](player/lowbattery.go)).

- **Volume control works on the Smart Pro and the Brick.** Quick Settings'
  slider asked amixer for Master or PCM, which the tg5040 codec does not
  have, so every change failed. JukaHub now drives the codec's "digital
  volume" control (an attenuation, written through a curve fitted to the
  steps NextUI measured on the same codec), mutes for real through "DAC
  Volume", reads the level back from the codec so it follows the hardware
  keys (headphones included), and keeps the firmware's own level in
  /mnt/UDISK/system.json in step so the keys carry on from it. The next level
  JukaHub sets lifts any mute, and the hardware keys lift one JukaHub made;
  the alarm's temporary 100% is no longer written to the firmware's settings;
  a level set just before quitting still reaches them; and JukaHub no longer
  puts its own saved level over the firmware's at launch. Quick Settings'
  writes moved off the render thread too. The mixer names and the curve come
  from MinUI and NextUI work on the same hardware and still need a check on a
  device ([player/volume_trimui.go](player/volume_trimui.go),
  [player/systemcontrol.go](player/systemcontrol.go),
  [player/quicksettings.go](player/quicksettings.go),
  [player/alarmvol.go](player/alarmvol.go)).

- **YouTube stops leaning on a client it retired — the best current bet, not
  yet tried on a device.** Since 2026-08-17 YouTube refuses every stream of
  the Android VR client the fast path asked as, and a refused stream went
  straight to downloading the whole file, or failed with "Download failed.
  Check network." The fast path now asks as the Android client first, checks
  each stream with the video CDN before using it, and hands a refused one to
  yt-dlp instead of the download; a refusal about the video itself (private,
  removed, age-gated) goes straight to yt-dlp, and an age gate no longer
  benches a working client as if it were the bot check. yt-dlp is asked for
  the clients that may still carry a playable single-file format (android,
  then its own defaults, then ios/tv — no longer android_vr). Whether YouTube
  still serves Android's 360p file without a token has not been tried on a
  device, and late-October reports disagree; if it does not, the yt-dlp
  update (Added) and the messages below are what still help
  ([player/youtubenative.go](player/youtubenative.go),
  [player/streaming.go](player/streaming.go)).

- **YouTube failures say what happened, and that no API key is needed.**
  "Check network", "No results" and "The service did not answer" are replaced
  by the cause and what happens next — no internet, a wrong clock, YouTube's
  bot check or rate limit, a refused stream and the yt-dlp check it started, a
  private or removed video — in the toast and in the Search, Trending and
  Shorts tabs, which also say that no API key is needed (the tabs' own reason
  used to be wiped the moment it was written). A download that hits the
  five-minute limit says so instead of ending silently, and a failed refresh
  keeps the results already on screen
  ([player/ytfailure.go](player/ytfailure.go), [player/core.go](player/core.go),
  [player/media_hub_render.go](player/media_hub_render.go)).

- **A google_api_key no longer slows search down.** yt-dlp has no use for a
  Data API key, but passing one made every search skip the native fast path;
  JukaHub needs no API key for YouTube, and a key that is set is now ignored
  ([player/core.go](player/core.go)).

- **Videos opened from a link get their title.** A video played from a
  browser link with no text, Play Link or a pasted address showed its raw URL
  on the Continue card; the title is now looked up through YouTube's oEmbed,
  which needs no key ([player/ytoembed.go](player/ytoembed.go),
  [player/browser_youtube.go](player/browser_youtube.go)).

- **Remote Play says why its helper cannot run, before JukaHub quits.**
  Starting a stream hands the screen to `chiaki-stream` and quits JukaHub; a
  helper the firmware's loader refuses (linked against a newer glibc than the
  handheld's, a missing library, another processor) failed only after that,
  with one line in `psremote.log`. The helper is checked first now: the ELF
  reading the core installer uses, then one start with no arguments in the
  hand-off's library path (it exits with its usage before it touches the
  screen), with immediate symbol binding so a missing SDL function shows too;
  the check runs as the panel opens, so Play does not wait for it. A refusal
  stays in the panel as "Could not start Remote Play — the helper won't run
  here: …", naming what is missing
  ([player/psremote_preflight.go](player/psremote_preflight.go),
  [player/psremote.go](player/psremote.go); tests in
  `player/psremote_preflight_test.go`).

- **Starting a stream reset Quality and Volume and erased the PSN Account-ID.**
  A on a console closed the panel before the stream was started, and closing
  it cleared the panel's state: the stream ran at the default 720p30 whatever
  Quality said, and the settings saved as it started were the cleared ones —
  Quality 0, Volume 0 and an empty Account-ID, which the next PS5 pairing then
  asked for again. The panel now closes once the stream is under way, after
  the real settings are saved, and `psremote.json` writes are serialised so
  a scan that moves a console cannot overwrite a pairing saved at the same
  moment ([player/psremote_ui.go](player/psremote_ui.go),
  [player/psremote.go](player/psremote.go)).

- **A console that got a new address from the router kept its pairing on the
  old one.** Discovery now reads the console's MAC (`host-id`), and a scan
  that finds a paired console at a new address moves the pairing there, keys
  included; a console added by host name keeps its name
  ([player/psremote_wake.go](player/psremote_wake.go),
  [player/psremote_discovery.go](player/psremote_discovery.go)).

- **Embedded subtitle extraction is bounded.** ffmpeg output stops after
  one byte beyond 8 MiB; an over-limit track kills the helper and is rejected,
  keeping long subtitle streams from growing memory without bound
  ([player/videosubs.go](player/videosubs.go)).
- **Native YouTube client cooldowns survive restarts.** Bot-check and CDN-refusal
  cooldowns are saved in the shared cache with a client-version key and their
  remaining time-to-live; a newer version starts with a fresh record
  ([player/youtubenative.go](player/youtubenative.go)).

### Security

- **The Screen Share link is view-only.** It used to carry the Phone
  Companion's PIN, which also unlocks the keyboard, the remote and uploads, so
  "a picture only" was not true. It now carries its own 80-bit token, kept in
  `jukauser.json` (`castToken`, redacted from exported configs) so a TV's
  bookmark survives restarts; **Y** replaces it and the old link stops at once,
  even for a viewer waiting on its next frame. Token requests never reset the
  PIN's wrong-guess lockout, and wrong tokens count toward it. The pairing
  code — Home's header QR, the pairing card, and the Screen Share panel's own
  code and link — is painted over in the shared picture, no frames go out
  while an enlarged QR is on screen, and the setup checklist hides the pairing
  PIN while sharing is on
  ([player/companion_cast.go](player/companion_cast.go),
  [player/castcap.go](player/castcap.go)).
- **Page scripts are kept off the handheld and the LAN, and away from other
  sites' cookies.** The requests a page's JavaScript makes — `fetch`,
  `XMLHttpRequest`, the scripts and modules it loads — cannot reach this device
  (where the Live Builder and the companion listen) or the LAN from a public
  site: the address is checked when the connection is dialled, so DNS rebinding
  gains nothing, and numeric spellings like `127.1` are refused by name; a script
  cannot navigate there either. They carry cookies only for the page's own site,
  re-checked on every redirect hop, read another origin's response only when its
  CORS headers allow it, never see `Set-Cookie`, and an https page cannot load
  plain http. `document.cookie` hides `HttpOnly` cookies under every spelling of
  the host and cannot overwrite or shadow them, and the `Secure`, `__Host-` and
  `__Secure-` rules hold. A page gets at most 160 requests, 24 MiB, 6 at a time
  and 15 s a request, and the ad filter sees every hop
  ([player/webdom_net.go](player/webdom_net.go),
  [docs/BROWSER.md](docs/BROWSER.md#what-a-page-cannot-do)).

- **The live builder can no longer be driven by a web page, a guess or a
  tricky design.** On the handheld it is off until Settings → Network → Live
  Builder turns it on; on a PC it listens on that computer only unless
  `JUKAHUB_LIVE_LAN=1`; everywhere it turns itself off after 30 minutes
  without use. The pairing code is shown on the device only (never on the
  status page, in a log or the notification history), travels in a header
  only, and is replaced from Settings or automatically when wrong codes pile
  up; five wrong codes lock a sender out (an IPv6 sender by its /64). Only the
  builder's own origins and host names get answers, which also stops DNS
  rebinding. A design whose field names differ only in letter case is refused,
  and a design that can do more than the device's current one — run a
  program, a command, a yt-dlp command line or a plugin action, start a file
  server, bind or change a protected setting, move where tools run, change the
  chat channel or load from a network share — waits in
  `jukaconfig.json.pending` until the owner applies it on the device, where
  the card says to apply only one's own design and lists every reason whole
  (Keep the current design is the default); nothing else lands while it
  waits. Pulled designs leave secrets on the device, pushing them back keeps
  them, and no answer to a push quotes one; an install that still holds the
  published code gets a new one at start ([player/livebuilder.go](player/livebuilder.go),
  [builder-device.js](builder-device.js), [SECURITY.md](SECURITY.md)).

- **The live builder's pairing code file is out of the repository.** An
  earlier change git-ignored `player/.livebuilder-code`, but the file stayed
  tracked, so every clone still shared one code; it is now removed from git
  (the removal is staged; the file each install generates stays ignored) and an
  install still holding the published code gets a new one
  ([player/.gitignore](player/.gitignore),
  [player/livebuilder.go](player/livebuilder.go)).

- **FTP now asks for a password and shares only your games, saves and
  media.** The FTP server used to accept any user name and password and serve
  the whole SD card. It now signs in as `juka` with a password made up for
  each start and shown on the Network tools page, waits two seconds after a
  wrong password, closes the connection after three failed sign-ins, locks an
  address out for ten minutes after ten, and drops a connection that has not
  signed in a minute after it connected; there is no anonymous login. The top
  level is a read-only list of the card's user folders (Roms, BIOS, Saves,
  screenshots, media), JukaHub's downloads and PhoneDrop (so phone uploads can
  be collected), each opened as an `os.Root` so `..` and symlinks cannot leave
  it; Apps, Emus, RetroArch and System are never shared, and `Roms/Ports`,
  whose PortMaster scripts the Ports panel runs, and the folders of game
  profiles stay read-only over FTP (change them over SFTP). A data connection
  is only accepted from the signed-in computer, and stopping the server also
  ends transfers in flight ([player/ftp_server.go](player/ftp_server.go)).

- **SFTP shares the SD card, not the whole system.** On the handheld the SFTP
  server's root is the card (your home folder on a computer); the whole file
  system is shared only after **Share whole system…** on its panel is pressed
  twice, for that run, or with `ssh_full_root` / `JUKAHUB_SSH_FULL_ROOT=1`. On
  the handheld the SFTP tile opens that panel first, so the choice can be made
  before the server starts ([player/ssh_server.go](player/ssh_server.go),
  [player/nettoolsscreen.go](player/nettoolsscreen.go)).

- **The file servers switch themselves off.** FTP and SFTP stop after 15
  minutes nobody uses them and when the handheld joins another Wi-Fi network or
  loses its address, with a toast that says why; keepalives (NOOP, PWD,
  TYPE…) do not count as use, and if the check itself fails the servers stop;
  the idle time runs on the device's uptime clock, so the clock being set from
  the network neither stops the servers early nor keeps them on longer
  ([player/fileserver_guard.go](player/fileserver_guard.go)).

- **Batch files start without cmd.exe reading their names as commands.**
  Apps, emulators and games launched through `.bat`/`.cmd` files get a command
  line quoted for cmd.exe (`/d /v:off /s /c`), so the launcher, the ROM path
  and per-game arguments with `&`, `|` or parentheses reach the right file — a
  ROM named `Tom&calc&.sfc` no longer runs a command — and a name cmd.exe
  cannot take literally (`%`, quotes) is refused with a message instead of
  being run ([player/winbatch.go](player/winbatch.go),
  [player/apps.go](player/apps.go), [player/emulators.go](player/emulators.go),
  [player/game_library.go](player/game_library.go)).

- **Package installs are checked, staged and swapped whole.** A registry entry
  may now publish `"sha256"` (and an exact `"size"`); a download that does not
  match is refused before anything is unpacked, and an entry whose checksum is
  malformed does not install at all. Entries without a hash still install but
  the package page says **Not verified** and the log records the download's
  own SHA-256. Downloads stream to a file beside the package instead of a
  64 MiB memory buffer, archives unpack through an `os.Root` into a staging
  folder (absolute paths, `..`, links, devices, duplicate names and reserved
  Windows names are refused), and only a complete copy replaces the installed
  one, by rename. Every step is on the card before the next one relies on it,
  so a power cut leaves the old version or the new one, never a copy of empty
  files; a swap that fails and cannot be undone is finished when the package
  is installed again, or when Packages opens after a restart;
  and a download is refused up front when the card (Windows included) lacks
  room for it plus 64 MiB ([player/pkgverify.go](player/pkgverify.go),
  [player/pkgstage.go](player/pkgstage.go),
  [player/packages.go](player/packages.go), [docs/PACKAGES.md](docs/PACKAGES.md)).

### Changed

- **The video player has new controls.** A frosted bar across the bottom of the
  picture holds a thick seek track with a handle and the time at both ends
  (time left counts down), a centred transport — rewind and fast-forward 10 s, a
  round accent play button, and previous / next when the video came from a
  list — the volume (a mute button, a level bar you can click, the percentage)
  and speed, captions, settings and full screen. Every button is a vector icon
  that lights up under the pointer and wears Home's focus ring under the D-pad;
  captions and full screen stay tinted while on. A chip and the title sit
  top-left, minimise and close top-right; a paused picture dims behind a large
  play button; a skip flashes "−10 s" / "+10 s" on its side of the picture; a
  live stream says LIVE instead of showing a track; the settings sheet lists
  each setting with its value. The chrome no longer takes the theme's text
  colour, so a light theme cannot draw dark icons over the video. The music
  player's transport (previous, play, next, stop, volume) draws the same icons
  instead of "|<", ">|" and "[]" ([player/videochrome.go](player/videochrome.go),
  [player/videoplayer.go](player/videoplayer.go), [player/icondefs.go](player/icondefs.go),
  [player/musicplayer.go](player/musicplayer.go)).

- **The Home pairing QR code is bigger.** The tile grew from 72 px to the full
  height between the status corner and the cards (about 100 px), and the code
  now fills it: the bitmap's own four-module margin used to be drawn inside the
  tile at one pixel per module, so the 72 px tile held a 37 px code. A pairing
  URL now draws with 3 px modules — 87 px of code — in one batched draw call
  ([player/companionui.go](player/companionui.go), [player/homelayout.go](player/homelayout.go)).

- **One log, with real levels.** The standard-library `[legacy]` log channel is
  gone: its 484 call sites now go through levelled helpers
  (`logDebug/logInfo/logWarn/logError`) into one slog handler that feeds the
  in-app Logs screen and the diagnostics bundle, formats a debug line only when
  debug is on, and colours the Logs screen by the line's real level instead of
  guessing from its words. `JUKAHUB_LOG_LEVEL` and `JUKAHUB_DEBUG=1` still work
  ([player/logging.go](player/logging.go), [player/systool_logs.go](player/systool_logs.go)).

- **Home puts games first and reads at arm's length** (the UX review's first
  fixes, see [docs/UX_REVIEW.md](docs/UX_REVIEW.md)). A **Games** card sits beside
  the hero and opens the game library; a new device shows a "Get started" card
  instead of an empty "Nothing played recently"; the version left the header;
  the greeting knows it is night after midnight; captions are at least 14 px
  (12 px on the Brick canvas); the focus ring stands off the card as a halo; the
  bell counts only what needs attention and caps at "9+"; the Exit pill left the
  footer (quit from the Start menu, or Select+Start); the PS4/PS5 pills read
  "Remote Play"; the Start menu draws real icons; the Exit screen asks "Quit
  JukaHub?" with Back focused, and its misleading Save button is gone. Untouched
  0.4.0 and early-November layouts migrate automatically
  ([player/homelayout.go](player/homelayout.go), [player/homefocus.go](player/homefocus.go),
  [player/home_migrate.go](player/home_migrate.go), [player/design.go](player/design.go),
  [player/startmenu.go](player/startmenu.go), [player/notifications.go](player/notifications.go),
  [player/game_library_ui.go](player/game_library_ui.go)).

- **Faster start on the handheld.** 42 regular expressions compile on first use
  instead of before `main`, a dead package registry no longer reads a directory
  at program start, five folder-setup steps moved off the path before the window,
  work deferred past the first frame runs in ~8 ms slices instead of freezing the
  first interactive second, and the minigame ticker no longer wakes 60 times a
  second when no game is open. `JUKAHUB_BENCH_INITTRACE=1 ./startup-bench.sh`
  shows package-init costs ([player/startup.go](player/startup.go),
  [player/startup-bench.sh](player/startup-bench.sh)).

- **A lean test suite.** 402 test files (about 3,000 tests, 113k lines) that
  rendered pixels, pinned layout numbers, audited the docs or slept were deleted;
  73 files and 469 fast tests remain and guard saved data, config migration,
  security checks, the updater and installers, and the parsers. No test opens an
  SDL window now, so `go test` finishes in seconds and `make test` uses Go's test
  cache ([CONTRIBUTING.md](CONTRIBUTING.md#tests-for-what-you-changed)).

- **About 5,300 lines of dead code removed.** A reference scan found ~380
  functions, types and variables nothing used — among them the old Waydroid
  Android host, an analog clock that was never drawn, a `TaskGroup` with no
  users, layout constants from the first Home, and "compile-time marker"
  variables — plus a test that lived in a production file (it never ran and
  linked the `testing` package into the release binary).

- **One engineering backlog.** `player/MODERNIZATION.md` is now the single open
  task list — about 100 tasks with priority, size, files, "done when" and how to verify,
  a hardware validation queue and an ideas parking lot — and the completed
  checklists and audit logs left it. `FEATURE-SLATE`, `FEATURE-BACKLOG`,
  `IMPROVEMENTS`, `PROJECT_STATUS` and `ROADMAP` were folded into it and deleted
  ([player/MODERNIZATION.md](player/MODERNIZATION.md)).

- **The builder forgets Font Awesome.** The migration that redrew icons in
  autosaves from the Font Awesome era is removed along with its test; icons are
  the builder's own SVGs ([script.js](script.js)).

- **A new logo, in the glass design.** The metallic circuit-board J did not
  belong to the glass app it opened. The logo is now Home made into an icon: the
  navy backdrop with its cyan and violet blooms, a pane of glass whose rim
  catches both lights, and a white J set like the Inter wordmark. It replaces
  the old artwork everywhere: the TrimUI Apps-menu icon (the SD-card package's
  copy had a grey checkerboard painted in where its transparency should have
  been), the Windows executable, window and taskbar icon, the web builder's
  favicon and app bar, the README and the docs site; and it now stands beside
  the wordmark on the About screen. One script renders every copy from one
  construction. The small Windows sizes (16-32 px, plus new 20 and 40 px entries
  for 125% display scaling) are a simpler mark drawn for their pixel grid
  rather than a shrunk 300 px picture, and the script writes the compiled icon
  resource itself, so changing the logo needs Python with numpy and Pillow and
  no MinGW. `mkwinres.sh` no longer remakes the `.ico` from `JukaPlayer.png`,
  which would throw that hinting away
  ([branding/mklogo.py](branding/mklogo.py), [branding/README.md](branding/README.md),
  [player/brandlogo.go](player/brandlogo.go), [player/about.go](player/about.go)).

- **The goodbye scene's leftovers are gone.** The farewell animation was already
  replaced by the glass teardown, but the exit request still lived in
  `goodbye.go` and the README and controls docs still promised a "goodbye
  screen". The request now sits with the signal guard in
  [player/exitguard.go](player/exitguard.go), that file is deleted, and every
  comment and doc describes the teardown card that actually runs.

- **Startup and shutdown are one glass card each.** The boot splash was a PS2
  starfield that looked like a different app, and its bar filled on the
  animation's own clock. It is Home's glass sheet now, and the bar is the boot
  queue: every row is a step that actually ran, with its own result — including a
  write probe on the card and the helper-tool inventory, whose missing tools
  start downloading during the splash instead of after the first frame. The card
  ends as soon as the queue is empty, so a fast boot gets a shorter splash. The
  farewell animation is gone with it: shutdown draws the teardown while it
  performs it — children and playback stopped, playtime, favorites, notes and the
  pending settings flushed, the CPU governor and frequency ceiling put back the
  way the firmware had them, and the shared cache and texture caches closed
  ([player/splash.go](player/splash.go), [player/stageui.go](player/stageui.go),
  [player/bootwork.go](player/bootwork.go), [player/bootchecks.go](player/bootchecks.go),
  [player/shutdown.go](player/shutdown.go), [player/powerprofile.go](player/powerprofile.go);
  tests in `player/bootstage_test.go`,
  `player/shutdown_test.go` and
  `player/powerprofile_boot_test.go`).

- **Button sounds are a soft thump.** Navigation, activate, back and toggle were
  flat beeps (880 Hz and up) with a linear envelope, which the handheld speaker
  rendered as a hard click. They are now short falling-pitch thumps with an
  exponential decay, so a tap reads as a body knock rather than a beep
  ([player/sound.go](player/sound.go)).

- **The legacy log is silent unless asked for.** Every colour pushed while a
  theme applies, and every parsed page, wrote a `[legacy] ... [DEBUG]` line to
  stderr, where it buried the boot log. Set `JUKAHUB_DEBUG=1` for a support
  session to get it back ([player/logging.go](player/logging.go)).

### Added

- **YouTube links in the browser play in Media.** Following a link to a YouTube
  video — a tap, A on a focused link, the link picker, a play card, a typed or
  pasted address, a bookmark, a shortener that redirects to a watch page, or a
  link another screen opens in the browser — now plays it on the Media screen
  instead of loading a page the reader cannot show. DuckDuckGo's and Google's
  click-tracking links and YouTube's EU consent page are seen through, a `t=`
  start time is kept, a watch link's playlist is dropped (yt-dlp would fetch the
  whole list into one file), and the result's text becomes the video's title.
  Back returns to the page the link was on, where it was scrolled, with the
  video in the corner window ([player/browser_youtube.go](player/browser_youtube.go)).

- **The power button is back on Home.** The Exit pill returns to the left end of
  the footer with a power glyph; Favorites drops down to it. It now asks first:
  an "Exit JukaHub?" card opens above it with Exit focused, so quitting is still
  two presses of A, but a stray press, a double click or a held key no longer
  quits by accident ([player/home_power.go](player/home_power.go)).

- **Hints name the keys you actually have.** On a PC without a game controller
  every hint (the footer, the Controls card, the on-screen keyboard, the hint
  lines in panels) now names keyboard keys: Enter instead of A, Esc instead of
  B, the arrow keys instead of the D-pad, F10 for the Start menu, PgUp/PgDn,
  Home/End and Tab where the shoulders page, jump or switch tabs. On a TrimUI the
  same happens when a USB or Bluetooth keyboard is plugged in (the device's own
  power and volume keys and key mappers do not count). With both connected, the
  one pressed last wins; a short message says why the words changed. Shoulders
  whose job differs on a screen keep their pad names rather than promise a key
  that does something else. Settings → Controls → Button hints overrides it
  (Auto / Controller / Keyboard) ([player/inputhints.go](player/inputhints.go),
  [docs/CONTROLS.md](docs/CONTROLS.md#keyboard-or-controller-what-the-hints-name)).

- **F10 opens the Start menu from the keyboard,** and closes it. Before, a
  keyboard could only reach the menu by walking to the top bar's pill. PgUp/PgDn
  and Home/End now page the Start menu, the notification centre and Quick
  Settings; Ctrl+Q closes Quick Settings as well as opening it; Tab switches the
  Downloads tabs; Y works as the "New" and "Graph" keys in Notes and the
  Calculator, as their footers say ([player/startmenu.go](player/startmenu.go),
  [player/core.go](player/core.go), [player/quicksettings.go](player/quicksettings.go)).

- **F1 shows the keys for the screen you are on.** It opens the Controls card,
  written in keyboard keys, and F1 again opens the full list, which was
  rewritten: every row of a category used to be drawn on the same line, its Home
  entries described keys that had changed, and what did not fit was cut off (it
  scrolls now). F1, F2 (search), F12 (screenshot) and Ctrl+Q now work on every
  screen, including those that take every key (Notes, Calculator, Pomodoro) or
  have nothing to focus, where they used to do nothing; the music player keeps
  F2 for its library search
  ([player/shortcuts.go](player/shortcuts.go), [player/controlslegend.go](player/controlslegend.go)).

- **Startup and shutdown chimes.** JukaHub now starts and closes with a sound,
  in the spirit of a console boot: as the glass splash comes up, a breath of air,
  a warm D♭ major-ninth pad and four glass bells climbing left to right, then a
  sparkle as the glint crosses the wordmark; closing plays the mirror, three bells
  falling back home. Both are synthesised (no audio files), streamed a block
  ahead of the speaker on their own stereo device so the first sound leaves within
  milliseconds, and follow the interface-sound level. **Settings → Sound &
  haptics → Startup & shutdown sound** turns them off (a toggle that previews the
  chime); a warm relaunch and a firmware power-off stay silent
  ([player/bootchime.go](player/bootchime.go),
  [branding/sound/README.md](branding/sound/README.md)).

- **Xbox on the Smart Pro, through hakuX.** The "xemu — not supported" tile is now
  an installer for the experimental hakuX port for the TG5040: X installs or
  updates it from its GitHub releases (digest-checked, SHA-256 recorded), unpacked
  on the card and merged into `Data/` and `Emus/` without touching files the
  package does not ship; R1 rolls back the last update, Y shows a setup sheet
  with the in-game controls, and Xbox games in `Roms/XBOX` launch from the Games
  library. The Brick Pro gets the same offer marked untested; Brick, Hammer and
  Smart Pro S say why they cannot; Windows keeps xemu
  ([player/hakux.go](player/hakux.go), [player/hakuxpage.go](player/hakuxpage.go),
  [docs/CONTROLS.md](docs/CONTROLS.md)).

- **AndroidRT grows a Java side.** NDK games now get a real JNI environment
  (classes, methods, strings, arrays, references, exceptions, `RegisterNatives`)
  where anything unimplemented returns a safe default and is reported once, so a
  game's first run lists exactly what it is missing. A Minecraft Bedrock class
  pack answers the platform questions the game asks — store, licence and Xbox
  Live honestly report "unavailable", never a fake success — GameActivity games
  (recent Minecraft builds) start through their own glue, and on TrimUI firmware
  the present layer uses the stock PowerVR `libEGL` first. Minecraft needs its
  64-bit build; Terraria, a Xamarin/Mono app, is refused with the reason and the
  PC route. Host smoke suite: 103 checks. Nothing has run on a device yet
  ([docs/ANDROID.md](docs/ANDROID.md), [docs/ANDROID-MINECRAFT.md](docs/ANDROID-MINECRAFT.md)).

- **A UX review.** [docs/UX_REVIEW.md](docs/UX_REVIEW.md) is an expert critique of
  every main screen for a 5-inch handheld at arm's length: principles, 30
  findings rated P0–P3, and which ones shipped.

- **Screens that need the internet say so before the press.** Launched before
  WiFi is joined, Live TV, Podcasts, Shorts, Tickers, video search and the
  internet diagnostics used to open and then fail somewhere inside with a raw
  dial error. Those tiles are now drawn dimmed with an **Offline** tag, the Start
  menu marks its network scenes the same way, and pressing one explains itself
  ("Offline — Live TV needs WiFi") instead of navigating. Local apps are
  untouched — JukaLand still plays, the Browser still shows cached and local
  pages, and FTP/SSH/companion/netplay stay live because they only need the LAN,
  not the internet. Separately, resolver and route failures are rewritten to
  "No internet connection — connect WiFi and try again" in the Browser error
  card, the JSON browser, the Downloads panel and the network tools, instead of
  "dial tcp: lookup … no such host"
  ([player/offlineaware.go](player/offlineaware.go),
  [player/menutiles.go](player/menutiles.go), [player/startmenu.go](player/startmenu.go),
  [player/core.go](player/core.go); tests in
  `player/offlineaware_test.go`).

- **A ringing alarm takes over the screen.** The alarm was a three-second
  arpeggio and a toast that any open panel or game could paint over, which is
  not a clock you can sleep through. A due alarm now owns the screen: a red wash
  blinking over everything, the time it is ringing for, its label and ringtone,
  and two buttons — **Snooze 9 min** and **Dismiss** — that are the only things
  the pad, the keyboard and a finger can press while it rings (A/X snooze, B/Esc
  dismiss, left/right choose, S and D shortcuts, taps on the buttons). The sound
  is a loop with a louder peak than any UI sound and keeps ringing until one of
  the buttons is pressed; snooze re-arms the same alarm without touching
  tomorrow's schedule, and dismiss clears the snooze. Each alarm also picks its
  own synthesized ringtone (Classic Chime, Brass Bell, Digital Pulse, Sunrise,
  Loud Buzz, Morning Birds) on a new **Ringtone** row in the editor, previewed as
  it cycles ([player/alarmring.go](player/alarmring.go),
  [player/alarm.go](player/alarm.go), [player/sound.go](player/sound.go);
  tests in `player/alarm_ring_test.go`).

- **Community theme library.** The theme gallery on Settings → Appearance has a
  **C** shortcut (**Y** on a gamepad) now: it opens a browser of the community
  themes published in the generator's gallery index. The index is fetched in the
  background, cached
  beside the other app data so the list still browses offline, and **A** installs
  an entry as `themes/<id>.json` — the same format a hand-made theme uses — then
  applies it. Bad entries are skipped instead of hiding the library, and an entry
  whose id matches a built-in palette is refused
  ([player/themecatalog.go](player/themecatalog.go),
  [player/themecommunity.go](player/themecommunity.go)).

- **The web gallery reads a shared community index.** `gallery/community.json`
  now carries community layouts as well as themes, and the builder merges its
  `layouts` into the bundled ones when the Gallery opens — so publishing a design
  is a pull request against one JSON file, not an edit to `layouts.js`
  ([gallery/README.md](gallery/README.md)).

- **Continue now includes podcasts.** Starting an episode puts it on the same
  timeline as videos and games, so the Continue card answers "what was I in the
  middle of?" across every media type; opening it returns to the Podcasts player
  with the saved position. Episodes de-duplicate by audio link and the row is
  labelled as a Podcast, not a video
  ([player/favorites.go](player/favorites.go), [player/recent.go](player/recent.go);
  tests in `player/recent_podcast_test.go`).

- **Saved playlists for music.** Audio Settings has a **Playlists** row: X names
  and saves the current queue ("Road Trip", "Relaxing"), left/right picks a
  saved list and A plays it. Names are case-insensitive, saving the same name
  replaces the list, and the store is plain JSON next to the other app data with
  atomic writes and a 40-playlist / 500-track bound
  ([player/musicplaylists.go](player/musicplaylists.go); tests in
  `player/musicplaylists_test.go`).

- **In-game OSD presets.** Settings → Smart Widgets has an **In-game OSD** row
that cycles *leave as is* (the default), *quiet*, *notifications* and
*notifications + stats*, writing RetroArch's own display switches — the message
font, menu widgets, FPS/framecount/memory counters — before a RetroArch launch,
with the original config kept once as `retroarch.cfg.jukahub-backup`. It never
touches a hotkey: those are numeric button codes that differ per firmware, so a
guess would break controls ([player/osdpreset.go](player/osdpreset.go); tests in
`player/osdpreset_test.go`).

- **Box art finds more of your library.** A cover lookup now also tries the same
title spelled the other way: accents folded (`Pokémon` → `Pokemon`), Roman
numerals as numbers and back (`Zelda II` ↔ `Zelda 2`), `&` for `and`, a colon
where the folder used a dash, and no trailing period — plus a small alias table
for the names that differ by more than spelling, starting with `Zelda II` →
`Zelda II - The Adventure of Link`. The exact name is still asked for first, and
the guesses come after every structural one
([player/romsetup.go](player/romsetup.go); tests in
`player/artwork_aliases_test.go`).

- **A 12-hour clock, if you want one.** Settings → Appearance has a **12-hour
  clock** switch: the status cluster, the dock clock, the saver, the clock app
  and the alarm list then read `3:04 PM` (and `12:05 AM` at midnight) instead of
  `15:04`, and every clock keeps working as the button it already was — the
  header still finds the clock and opens the calendar. Unset, everything stays
  24-hour, so existing configs and screenshots do not change
  ([player/clockformat.go](player/clockformat.go),
  [player/config.go](player/config.go); tests in
  `player/clockformat_test.go`).

- **The web builder ships its own fonts.** Inter and the Font Awesome faces the
  markup uses now live in `fonts/` instead of a CDN, so the builder opens on
  a handheld's own hotspot and never waits on a third-party request; the shipped
  policy no longer has to allow those hosts (`index.html`, `_headers`;
  `tests/web/selfhost.test.mjs`).

- **The design check lints for the handheld panel.** Saving a design that will
  run at 1280×720 on a five-inch screen now warns about text under 14 px, a
  control inside the rounded-corner margin the panel clips, and a scene with more
  than 24 controls that will scroll — alongside the existing contrast and
  tap-target checks (`script.js`; `tests/web/builder.test.mjs`).

- **The idle screen can take its own backdrop photo away.** The starfield sleep
  screen is the only place the photo is drawn, so it is also where it can be
  forgotten: **Y** (pad or keyboard) offers **Y clear photo** above the wake
  hint, and a second **Y** inside five seconds drops the backdrop without
  waking the panel or opening Files. The first press only arms it — every other
  input on this screen means "wake up", and an ordinary wake must not throw the
  photo away — so the line turns amber as **Y again to clear**, then green as
  **Backdrop cleared · any key wakes** for a moment. The picture on the card is
  untouched; only the setting is forgotten, and a default saver with no photo
  shows no line and wakes on **Y** like any other key
  ([player/screensaver.go](player/screensaver.go),
  [player/main.go](player/main.go); tests in
  `player/screensaver_backdrop_test.go`).

- **The Benchmark screen's Run button says it is running.** While a measurement
  is in flight — and the screen starts one as it opens, so on a slow card that
  is the whole first visit — the screen's one Run button is drawn disabled and
  reads **Running…** instead of **Run**, so it no longer looks like a press
  would do something. A press that lands mid-run is refused at the control: no
  activate tap, no press dip, and no second 100 MB pass over the first, which
  the panel's progress line already answers. The button takes its configured
  width back as soon as the run ends, and the busy label is fitted to that width
  rather than ellipsized
  ([player/core.go](player/core.go),
  [player/morefeatures.go](player/morefeatures.go); test in
  `player/benchmark_test.go`).

- **Picture and video thumbnails in Files.** A row whose file is a photo or a
  video now shows the picture itself in the leading chip instead of **IMG** or
  **VID**: 44×28, cropped from the centre to fill rather than squashed, with a
  play mark on a video frame so a still and a photo are told apart. The chip's
  footprint is unchanged, so names, sizes and dates stay in their columns.
  Frames are made off the render thread — a photo is decoded and averaged down
  by the Go image decoders, and one ffmpeg grab pulls a video's frame a second
  in, falling back to the opening frame for a clip shorter than that — and both
  the bytes and the decoded texture are cached, so a folder is decoded once and
  again only when the file changes. A row keeps its type chip when there is nothing to show: no ffmpeg
  installed, a codec the tool cannot read, or a picture past the
  24-megapixel decode ceiling
  ([player/filethumb.go](player/filethumb.go),
  [player/files_manager.go](player/files_manager.go); tests in
  `player/filethumb_test.go`).

- **A playback queue: results can be lined up, and each one starts when the last
  finishes.** The **Queue** pill on a search-result row adds that video to Up
  Next and reads *Queued* while it is waiting; pressing it again takes it back
  out. The list survives leaving the results screen, the player overlay names
  what comes next (`Up next: <title> (+3)`), and a video that reaches its
  natural end starts the head of the list by itself — stopping one by hand
  never does. The player's settings sheet gained an **Up Next: N queued** row
  that empties the list once the rows it was filled from are gone, and the
  Shorts feed keeps its own hand-off while it is armed. Entries are
  deduplicated by link and capped at 50, and the list is session-only on
  purpose ([player/playbackqueue.go](player/playbackqueue.go),
  [player/videoplayer.go](player/videoplayer.go),
  [player/result_actions.go](player/result_actions.go); tests in
  `player/playbackqueue_test.go`).

- **Vibration strength: Soft, Normal or Strong.** A new row in the Accessibility
  panel scales the drive level of every pulse — navigation ticks, confirmations,
  errors — so the motor can be quiet at night without switching feedback off
  entirely, which the Vibration switch in Settings still does. The pulse length
  is left alone: a coin motor needs tens of milliseconds to spin up, so a
  shorter pulse would read as unreliable rather than gentle. The step is stored
  as `hapticsIntensity` beside the switch and a Strong error pulse is capped at
  full scale ([player/haptics.go](player/haptics.go),
  [player/a11y.go](player/a11y.go); tests in
  `player/haptics_test.go`).

- **Box art for ROM names that do not match the reference title.** The lookup
  now asks for the exact name first, then the underscored twin, the name without
  dump tags (`(USA)`, `(Rev 1)`, `[!]`), the part before the subtitle separator,
  and finally the article-stripped form — each of them spaced and underscored,
  capped at eight requests, and a hit stops the guesses so a full library fetch
  is not multiplied by the variant count. The not-found cache is stamped with
  the matcher version, so titles the older lookup gave up on are retried once
  instead of being hidden for the month-long window
  ([player/romsetup.go](player/romsetup.go); tests in
  `player/artwork_match_test.go`).

- **Files is a file manager, and it moves files between two consoles.** The
  screen opens on a breadcrumb bar where every folder in the path is its own
  chip — one press to any ancestor instead of five **B** presses — carrying the
  folder's item count, its size, the card's free space and a sort chip that
  cycles name / size / newest. Rows gained a type chip (**DIR**, **IMG**,
  **AUD**, **VID**, **PDF**, **ZIP**, **BOOK**, **TXT**, **UP**), the size and the
  date, and the parent row names the folder it goes back to. **X** marks rows
  (a marked row is tinted and its chip becomes a tick), and **Y** opens one
  action sheet: new folder, rename, copy, cut, paste, delete, select all, sort,
  connect and remote files. Actions apply to the marked rows or, with nothing
  marked, to the row under the cursor; a paste never overwrites, landing as
  `name (2).ext`; delete arms on the first press and acts on the second, and
  every path is still resolved against the Files root before it is touched.
  **Connect to device** takes `user@host:port/folder`, and **Remote files**
  browses the other console over SFTP — download into the open folder, upload
  the marked files to the remote one — as one background job per transfer with
  its progress in the panel's subtitle. The first connection to a device pins
  its host key and a later change is refused
  ([player/files_manager.go](player/files_manager.go),
  [player/filetransfer.go](player/filetransfer.go),
  [player/features.go](player/features.go), [player/main.go](player/main.go);
  tests in [player/files_manager_test.go](player/files_manager_test.go)).

- **The lock-screen image can be cleared again, and clearing it never touches
  the photo.** The picture a Files viewer handed to the idle screen stays
  reachable: opened in the viewer, the same action now reads **Clear lock
  screen** and puts the starfield idle screen back — the button, the **L** key
  and the pad's **X** are one toggle, so the second press undoes the first. Only
  the `screenlockImage` setting is forgotten, so the file stays on the card and
  can be set again from the same place; the decoded picture is dropped in the
  same call, so the default look returns on the next frame instead of after the
  reload window, and clearing a screen that already has no picture is a quiet
  no-op that writes nothing
  ([player/screenlock.go](player/screenlock.go),
  [player/imageviewer.go](player/imageviewer.go),
  [player/controller.go](player/controller.go); tests in
  `player/screenlock_test.go` pin the restored default, the untouched file, the
  saved state after a reload and the toggle's remote-image guard).

- **The Wi-Fi diagnostics sheet can be copied, and it is in the support
  bundle.** A **Copy** pill (also `c` and pad **Y**) puts the sheet's report on
  the clipboard — header, collection time and every row, aligned — and writes a
  diagnostics bundle on the card in the same press, so the radio's own answer
  reaches a support thread however it gets shared. The two destinations are
  attempted independently: a platform without a clipboard still gets the file,
  and a full card still gets the paste. The sheet's last collection also joins
  every bundle, redacted like the log, and a session that never opened the sheet
  says so instead of reading like a device with no Wi-Fi facts
  ([player/wifi_diag.go](player/wifi_diag.go),
  [player/diagnostics_bundle.go](player/diagnostics_bundle.go),
  [player/wifi_ui.go](player/wifi_ui.go); tests in
  `player/wifi_diag_test.go`,
  `player/diagnostics_bundle_test.go`).

- **Podcasts are a subscription library, not a URL box.** The Podcasts screen
  now keeps a saved list of feeds in `podcasts.json` next to the app's other
  data, gives each feed its show title, author and episodes, and opens on a
  two-column library: shows on the left (with an **+ Add a feed** row) and that
  show's episodes on the right. Episodes keep their published date and feed
  duration, sort newest first, carry the resume position and speed the audio
  player already stores for long-form listening, and play as a one-show
  playlist through the music player's streaming path — the same path the URL
  box used, but with a title and a next episode instead of a bare URL. Feeds are
  fetched in parallel, four curated shows seed a fresh install, and subscribing
  or removing a feed is written immediately, so an offline boot opens the saved
  library instead of an empty panel. The `podcast_panel` action and the screen's
  own Library button open it; the pad drives the two columns (Left/Right
  switches, A opens a show or plays an episode, X adds a feed, Y refreshes,
  Start deletes a subscription, B closes), and the keyboard and pointer use the
  same row map
  ([player/podcasts.go](player/podcasts.go), [player/core.go](player/core.go),
  [player/main.go](player/main.go), [player/controller.go](player/controller.go);
  tests in [player/podcasts_test.go](player/podcasts_test.go)).

- **The reader opens EPUB books, and PDF pages are page-accurate.** The old PDF
  extractor scanned every `BT…ET` block in the file and handed the whole
  document back for each page, so page one showed the entire book and page two
  showed it again. The reader now walks the PDF page tree from the catalog,
  decompresses each page's content streams, and extracts only that page's text
  (with `TJ` kerning above the word-space threshold becoming spaces and `Td`,
  `T*` and `Tm` becoming line breaks); a file whose page tree cannot be read
  still falls back to the old whole-document scan rather than showing an empty
  sheet. EPUB support is new: container → OPF → spine, one chapter per spine
  document with its `<title>` or first heading, paginated to about 1,200
  characters a page, entities decoded and paragraphs kept apart. The scene keeps
  its `PDFReader` name so favourites, playtime and saved configs still resolve,
  but it is **Reader** everywhere in the UI: the title line shows the format and
  page count, a book adds a chapter line, L1/R1 turn chapters (or pages for a
  PDF), `[`/`]` do the same on a keyboard, and Files accepts `.epub` as well as
  `.pdf`. Entering the scene no longer wipes the document that Files just
  opened, which was why an opened PDF used to show "No document open"
  ([player/reader.go](player/reader.go), [player/pdfreader.go](player/pdfreader.go),
  [player/features.go](player/features.go), [player/extras.go](player/extras.go),
  [player/core.go](player/core.go); tests in
  [player/reader_test.go](player/reader_test.go)).

- **An LED Style panel for mode, effect, colour and brightness.** The LED sync
  could already drive `multi_intensity`, `color` and decorative brightness LEDs,
  but only the policy was reachable from the UI and the rest meant editing
  `jukauser.json`. **Quick Settings → LED Style** now opens a panel with Mode
  (Audio, Battery, Charging, Temperature, Custom), Effect (Solid, Pulse,
  Rainbow), a hue strip with a live swatch and hex value, a 10–100% brightness
  trim, and the LED Sync switch itself. Every change is written to the
  debounced user config, so it survives a reboot, and the animation runs while
  the panel is open so the lights show the change on the next frame; closing the
  panel stops a preview it started, leaving the saved setting as it was.
  Brightness is applied to the colour (so it dims every LED mode the same way)
  and `rainbow` is a new custom effect. The panel is modal for the pad, the
  keyboard and the pointer, and its row is dimmed with a reason on a device with
  no addressable LED
  ([player/ledpanel.go](player/ledpanel.go), [player/ledsync.go](player/ledsync.go),
  [player/quicksettings.go](player/quicksettings.go), [player/main.go](player/main.go),
  [player/controller.go](player/controller.go); tests in
  `player/ledpanel_test.go`).

- **Favorites is a library, not a flat list of links.** The page now opens on
  **Continue** and has four tabs with their counts on the strip — Continue
  (videos started but not finished), Videos (everything saved from a result,
  grouped by channel), Streams (the channels kept on the IP Stream screen) and
  Recent (grouped into Today / Yesterday / This week / Earlier). Every row shows
  a 16:9 frame or a drawn tile, the title, the channel or stream host, a
  duration badge and a resume line, with a hairline along the card's foot for
  how far in the video is; a row without artwork still reads as a video because
  the frame gets a border and a play glyph. The list scrolls only as far as the
  focused row needs and a group heading is never scrolled over its row. Empty
  tabs say what they hold and how to fill them. Saved videos inherit the resume
  offset recorded in Recent, so Continue and Videos agree on where playback
  starts. The pad drives the whole page (Left/Right tabs, Up/Down rows with the
  Play / Remove buttons reachable at the ends, A plays, X removes, B backs out,
  L1/R1 and L2/R2 jump), the keyboard and the pointer use the same row map the
  renderer records, and the scene's controller block now delegates to one
  handler instead of its own copy of the old two-tab logic. Results gained a
  fourth pill: **Save** keeps a video in the library and reads **Saved** when it
  is already there, then takes it back out on the next press
  ([player/favorites.go](player/favorites.go), [player/result_actions.go](player/result_actions.go),
  [player/core.go](player/core.go), [player/main.go](player/main.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  `player/favorites_library_test.go`,
  `player/result_actions_test.go`).

- **Files can keep the picture it opens as the lock-screen image.** The
  full-screen viewer in Files now has a **Set lock screen** action — the button,
  the **L** key or the pad's **X** — which saves the picture and paints it across
  the idle screen under the clock, quote and chat card, cropped to fill and
  dimmed so that text stays legible. The saved setting is a path, so it is
  treated as best-effort: a photo that was deleted, left on an unplugged card or
  no longer decodable simply leaves the default starfield idle screen in place.
  The miss is checked (and logged) once, not once per frame, and a replaced file
  is reloaded instead of the saver keeping a stale picture; a remote chat image
  cannot set it, because that file lives in the download cache
  ([player/screenlock.go](player/screenlock.go),
  [player/imageviewer.go](player/imageviewer.go),
  [player/screensaver.go](player/screensaver.go); tests in
  `player/screenlock_test.go` pin the validation, storage, the drawn frame and
  the missing-file fallback).

- **The Streams button opens a menu that can always start content.** The IP
  stream screen was a single URL box, and the Media screen's button (labelled
  "IPStream") plus the screen's own "Library" button opened a list of saved
  links that is empty on a fresh install — a dead end on a handheld, where the
  button is the only way in. Both now open one menu with two rows that are
  always there — **Add a stream link** and **Browse channels** (the public IPTV
  playlist import) — above the saved channels, so there is never a press with
  nothing behind it. The D-pad walks every row including the action rows, A or
  Enter opens or plays the highlighted one, X saves the URL box as a channel, Y
  imports, Start or Delete removes, and B or Esc closes. The two buttons are
  named the way the screens name themselves (**Streams** on Media, **Channels**
  on the stream screen), the hint row now gives each pad button its keyboard
  twin (`A / Enter`, `B / Esc`) the way the rest of the overlays do, and the
  injected Media button is a real focus target: a test walks the D-pad from
  Shorts to it on the compact Brick canvas and checks that confirming it opens
  the menu ([player/ipstreams.go](player/ipstreams.go); tests in
  `player/ipstreams_test.go`).

- **Earned badges show their unlock date.** The unlock time was already saved
  with each badge (RFC3339, from the moment the check passed) and never shown,
  so the list could say what you earned but not when. Each earned badge now
  carries "Unlocked 2 Mar 2026" on a third line under its description, in the
  accent colour; locked badges leave the line empty, and a save whose timestamp
  cannot be read shows nothing rather than a zero date. Badge rows are 74px so
  the third line cannot be clipped by the row below ([player/playtime.go](player/playtime.go);
  `TestPlaytimeUnlockLabel` and `TestEarnedBadgesShowTheirUnlockDate` in
  `player/playtime_achievements_test.go`).

- **Playtime has 100 badges on a page that scrolls, earned badges first.**
  The screen shipped six badges in a grid that stopped drawing once the card was
  full, so a badge set of any real size would have been invisible. It now has
  exactly 100 — lifetime time, days used, streaks, single-day records, weekly
  totals, screens explored, 57 per-screen milestones (games, music, video,
  browser, files, settings, notes, terminal, emulators and the rest) and ten
  that count the badges you already earned — and the whole page scrolls under a
  pinned title band with a scrollbar, driven by the D-pad, the wheel, Page
  Up/Down or L1/R1, with Home/End jumping to either end. Earned badges are
  listed **first** under an "Achieved first" tag and keep their fixed number
  wherever they sit, so nothing jumps around as you unlock it. Every check reads
  the saved totals, so a screen you have not used cannot unlock anything, and
  the badge count in the read-out is pinned to the list by a test. Two layout
  defects the bigger page exposed are fixed with it: the MOST USED value used to
  be drawn on top of its own bar, and on the compact (Brick) canvas the insight
  stats ran off the card edge. ([player/playtime.go](player/playtime.go),
  [player/main.go](player/main.go), [docs/CONTROLS.md](docs/CONTROLS.md); tests
  in `player/playtime_achievements_test.go`).

- **About now shows the device identification evidence, not just the log.**
  `[device] …` was the only place the model, the source that named it, the panel
  and the firmware's own model strings were visible, and a handheld has no
  shell to read it. About has a **Device** card with those rows: the model and
  the source that decided it (`launcher env`, `device tree`, `cpuinfo`,
  `hostname`, `firmware name`, `trimui firmware + panel`, `override` or an
  honest `not identified`), the panel the image reports, the model names found
  in the firmware (longest match only, so a Pro S does not read as two models),
  and the kernel's device-tree model and compatible list. Empty sources are
  omitted rather than printed as blank labels. The four About cards now share
  two measured rows, so the new card cannot push the licence list into the
  footer band on either the wide or the compact canvas, and a card too short
  for the whole list says `+N more` instead of clipping it. ([player/about.go](player/about.go);
  tests in `player/about_device_test.go`).

- **A Wi-Fi diagnostics sheet, for a handheld that cannot connect and has no
  shell.** The picker could only say "no networks", which is the same line for
  a missing radio, a supplicant that is not running, a control socket in a
  directory nothing looked in, and a permission error — and the device that
  hits it has nowhere to type `wpa_cli status`. **L1** (or `i`, or the new
  **Diagnostics** pill) opens a sheet that names every deciding fact: the
  interface and which probe found it, the backend (`wpa_cli` or the supplicant
  socket), the control directory and whether its socket is actually there, the
  directories that were searched, the supplicant config, whether JukaHub started
  a supplicant and what happened, the radio's state and its switch, the station's
  `wpa_state`/SSID/IP, and the last error with the phase it came from and its
  age. It opens even when the list cannot load — that is the state it exists for
  — refreshes itself while open, and **B** steps back to the list instead of
  closing the picker. The last error is recorded at the single function every
  command failure already passes through, so the sheet and the panel cannot
  disagree. ([player/wifi_diag.go](player/wifi_diag.go),
  [player/wifi_ui.go](player/wifi_ui.go), [player/wifi.go](player/wifi.go),
  [docs/WIFI.md](docs/WIFI.md); tests in
  `player/wifi_diag_test.go`).

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
  `player/wrapped_test.go`,
  `player/wrapped_render_test.go`).

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
  `player/wifi_windows_test.go`).

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
  `player/activity_test.go`).

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
  `player/videoquality_delivered_test.go`).

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
  (`player/session_memory_test.go`,
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
  `player/memory_growth_test.go`,
  `player/cachebudget_test.go`).

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
  `player/cachebudget_test.go`).

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
  tests in `player/browserforms_test.go`).

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
  `player/emulators_test.go` and
  `player/page_header_consistency_test.go`).
- **The browser remembers where you were reading.** Following a link and
  pressing Back (or Forward, or Reload) returns you to the paragraph you were
  on instead of the top of the page. Positions are kept per address for the
  session, bounded so a long browse cannot grow them without limit; a fresh
  navigation — a typed address, a link, Home — still starts at the top, because
  that is a new place, not a return. The restore is applied on the render thread
  once the page is laid out and clamped to its length, so a page that changed
  since it was last read is never scrolled out of range.
  ([player/browserscroll.go](player/browserscroll.go); tests in
  `player/browserscroll_test.go`).
- **Text zoom in the browser.** The reader's page can be scaled 75%–160% with
  L2/R2 on a pad or `-`/`+` on a keyboard, independent of the global
  accessibility text size. The whole styled page is re-measured from the new
  body size, so the wrap, row height and headings scale together; the toolbar
  chip names the scale whenever it is off 100%, and the choice is remembered across
  pages and relaunches. On a 5-inch panel the size of the words is a first-order
  control, so the web page's L2/R2 moved from jump-to-top/bottom (still one held
  D-pad away) to zoom.
  ([player/browserzoom.go](player/browserzoom.go); test in
  `player/browserzoom_test.go`).
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
  test in `player/a11y_powermode_test.go`).
- **Dropping a file on the window plays it (desktop).** A video or audio file
  dragged onto JukaHub plays through the embedded player, an image opens in the
  viewer, a folder points at Files, and an unknown extension says so instead of
  silently ignoring the drop; a path that does not exist is refused up front.
  The handheld has no pointer, so this never fires there.
  ([player/dropfile.go](player/dropfile.go); test in
  `player/dropfile_test.go`).
- **Podcast feeds survive an offline boot.** The last-known-good copy of each
  feed is kept on the card (30-day window, 1 MiB cap) and served only when the
  network fetch fails outright — a live response, a 304 or an HTTP error status
  all win first, so a feed that is genuinely gone is never replayed as if it
  were live. ([player/feedcache.go](player/feedcache.go); tests in
  `player/feedcache_test.go`).
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
  `player/touchtarget_test.go`).
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
  tests in `player/header_test.go` and
  `player/calendar_popover_test.go`).

- **The top-bar calendar is a month view you can page through.** The clock's
  popover borrowed the alarm screen's corner widget: 28×22 cells drawn with the
  24px body font, so a day numeral was taller than its own row and two-digit
  days nearly touched, and it could only ever show the current month — there was
  no way to look at last week or at next month. The popover now has a card of its
  own with 46×38 cells, a month header (`‹ October 2026 ›`) and a fixed six-row
  grid, so the arrows do not move when a month needs five rows. The arrows are
  tap targets; Left/Right (or PageUp/PageDown, the D-pad, the shoulders) step a
  month and roll the year, and the month title — tinted while you are away from
  it — returns to today, as does A or `t`. The alarm's compact corner widget is
  unchanged. ([player/calendarpopover.go](player/calendarpopover.go),
  [player/home.go](player/home.go), [player/main.go](player/main.go),
  [player/controller.go](player/controller.go); tests in
  `player/calendarpopover_test.go`).

### Fixed

- **Whole-program audit, November 2026.** Ten reviewers went through every
  subsystem and about a hundred defects were fixed; the entries below are the
  ones a user could run into. Each fix carries a regression test where the
  failure can be reproduced off the device.

- **Brightness and the stick LEDs were never found on the Smart Pro or the
  Brick.** Every entry in `/sys/class/backlight` and `/sys/class/leds` is a
  kernel symlink, and the discovery kept only real directories, so Quick
  Settings had no brightness row and the LED sync had nothing to drive. Both
  now follow the class links ([player/systemcontrol.go](player/systemcontrol.go),
  [player/ledsync.go](player/ledsync.go)). The LED loop also stopped reading
  the live settings map from its own goroutine — a fatal concurrent map access
  once the LEDs were actually found.

- **Joining one Wi-Fi network disabled every other saved network.**
  `select_network` disables the rest, and the join saved the config straight
  after it, so the handheld never rejoined home after one visit elsewhere —
  and a failed join left everything disabled too. The other networks are now
  enabled again after the join either way, and the config is saved only once
  the join worked. Passwords and names with `"` or `\` were escaped into
  something else (wpa_supplicant reads quoted values verbatim), non-ASCII names
  shown as `\xNN` could not be joined at all, and a 64-digit raw key was always
  refused; names now travel as hex, passwords exactly as typed. The control
  socket used on images without `wpa_cli` could never connect (it is a
  datagram socket) and sent commands the supplicant does not accept; it now
  binds its own address and speaks the real protocol
  ([player/wifi.go](player/wifi.go), [player/wifi_ctrl.go](player/wifi_ctrl.go)).

- **Performance mode never switched the CPU — and its restore would have left
  the hub at the lowest clock.** The governor list was read from a file no
  kernel has, so every launch said "no performance governor". With that fixed,
  the way back after a game is now the governor the hub was running before it,
  not `powersave` ([player/powerprofile.go](player/powerprofile.go)). The
  low-battery guard now recognises TrimUI's own RetroArch (`ra64.trimui`) and
  its `.retroarch` config ([player/lowbattery.go](player/lowbattery.go)).

- **The self-update could kill its own helper.** The script that swaps the new
  binary in was started on a context cancelled a moment later, so it could die
  before the swap; it now runs detached. The Windows helper broke on paths with
  spaces, a half-extracted update was reused on the next try, and JukaHub could
  offer the release it was already running ([player/patch_swap.go](player/patch_swap.go),
  [player/autoupdate.go](player/autoupdate.go)). Patch journals overwrote one
  backup with another of the same name and never cleaned up after themselves,
  large downloads were cut off by a 25-second total timeout, and one install
  could cancel — or paint its error over — another install's card
  ([player/patch_engine.go](player/patch_engine.go),
  [player/installbar.go](player/installbar.go), [player/toolbox.go](player/toolbox.go)).

- **Settings came back after Settings → Save, and some never saved at all.**
  The hot reload that a save triggers merged the settings read at boot, undoing
  every change made since; it also ran off the main thread. Every config load
  wrote design values over the user's settings, Power Mode was never saved, a
  handheld boot forced Reduced Motion and Low Power on, and a setting changed in
  the second before quitting was lost. The debounced save marshalled live maps
  on a timer goroutine — a fatal crash waiting to happen — and web-form values
  (passwords included) and page text went into `jukauser.json`
  ([player/hotreload.go](player/hotreload.go), [player/extras.go](player/extras.go),
  [player/config.go](player/config.go), [player/main.go](player/main.go)).

- **Files: Delete on the ".." row deleted the parent folder.** Delete, Cut and
  the X mark now skip that row and refuse anything that holds the folder being
  browsed, a symlink is removed as a link instead of emptying what it points
  at (in Files and over SFTP), and a rename that only changes letter case
  works ([player/files_manager.go](player/files_manager.go),
  [player/sftp_root.go](player/sftp_root.go)). Restoring a save snapshot could
  prune away the very snapshot being restored
  ([player/savemanager.go](player/savemanager.go)).

- **Transfers and the phone companion.** An FTP resume wrote the client's tail
  over byte 0 of a truncated file, and long transfers were cut at 30 minutes.
  Companion uploads were buffered in RAM-backed `/tmp` and now stream to the
  card; repeated wrong PINs lock out for longer each time, and idle
  connections are closed. Expired cache entries are finally deleted
  ([player/ftp_server.go](player/ftp_server.go), [player/companion.go](player/companion.go),
  [player/cache.go](player/cache.go)).

- **Pausing and seeking a video could freeze the whole UI.** The audio device
  was paused and never resumed while the queue waited on it holding a lock.
  Muted playlists raced through, a one-track list repeated forever, a decoder
  that failed half way through counted as a finished track (and forgot the
  resume point), podcast episodes never started, downloads could adopt the
  wrong file, temporary video files were never removed, and Live TV froze on
  entry while it fetched its playlist ([player/sound.go](player/sound.go),
  [player/videoplayer.go](player/videoplayer.go), [player/musicplayer.go](player/musicplayer.go),
  [player/podcasts.go](player/podcasts.go), [player/downloads.go](player/downloads.go),
  [player/tvchannels.go](player/tvchannels.go)).

- **Input.** The dock's "screen off" was switched straight back on by the
  release of the button that turned it off, and a press that woke the screen
  also acted on it (B left the dock). A press now only wakes, and a drifting
  stick no longer keeps the screen awake. L2/R2 reached the screen under
  overlays, the controller test could not be closed once SELECT was remapped,
  closing the window during the splash froze it, the on-screen keyboard
  ignored quit and exit requests, a pad plugged in after launch did nothing,
  and typed text leaked into the note or terminal behind an open panel
  ([player/main.go](player/main.go), [player/screensaver.go](player/screensaver.go),
  [player/gamepad_hotplug.go](player/gamepad_hotplug.go), [player/splash.go](player/splash.go),
  [player/core.go](player/core.go), [player/shoulders.go](player/shoulders.go)).

- **Games.** A video snap kept decoding at full speed — and kept running
  under the game it launched; it now plays at its own pace and stops when a
  game starts. CD games were listed once per track and multi-disc `.m3u` sets
  not at all, an emulator was matched by substring ("nes" inside "snes"),
  Boost overwrote the user's own per-game options and revert deleted them, the
  BIOS doctor hashed hundreds of megabytes on the UI thread, the Apps grid
  listed JukaHub's own launch script, and relaunching an AndroidPak game within
  ten seconds had its files deleted under it
  ([player/snappreview.go](player/snappreview.go), [player/romsetup.go](player/romsetup.go),
  [player/game_library.go](player/game_library.go), [player/compat.go](player/compat.go),
  [player/biosdoctor.go](player/biosdoctor.go), [player/apps.go](player/apps.go),
  [player/androidpak.go](player/androidpak.go)).

- **Browser.** A page in a legacy encoding could crash the reader (and with it
  the hub); scanners now keep byte offsets intact and a parser panic shows an
  error page. Every page leaked the decoded pixels of its images, the 4 MiB
  image budget never held, a site could set a cookie for every `.com` site,
  and a sign-in form would have put the password in the address. Scripts are
  capped in output, recursion, memory and total time; deeply nested CSS and
  exponential `var()` chains are bounded. Find opened on the second match,
  zoom was never saved, the first rule after `@charset` was dropped, the last
  line hid behind the cookie strip, and Forget left signed-in pages in the
  Back cache ([player/textutil.go](player/textutil.go), [player/webpage.go](player/webpage.go),
  [player/webpage_image.go](player/webpage_image.go), [player/browserpsl.go](player/browserpsl.go),
  [player/webscript.go](player/webscript.go), [player/browserfind.go](player/browserfind.go),
  [player/browserzoom.go](player/browserzoom.go)).

- **Chat, Watch Party and Smart Home.** A chat image could declare a size
  that ran the handheld out of memory, Watch Party polled the network on the
  UI thread, Discord connections raced and leaked sessions, Smart Home toggles
  froze the UI for up to twelve seconds, Pi-hole sessions leaked, and AI chat
  replies were mirrored to Discord ([player/chat.go](player/chat.go),
  [player/partynet.go](player/partynet.go), [player/smart_home.go](player/smart_home.go),
  [player/smart_home_ui.go](player/smart_home_ui.go)).

- **Apps.** Daily alarms rang once and never again; a missed minute is now
  caught up, Y on the pad makes a new alarm, and L1 deletes only after a
  confirming second press. The terminal's `cd` moved the whole app — every
  relative save then went to the folder the user had wandered into — and an
  external command froze the UI until it ended; the terminal now keeps its own
  directory, runs commands in the background with a timeout, and Ctrl+C (or B)
  stops them. The dictionary closed the shared cache database and fetched
  suggestions over the network on every frame. Pomodoro resumed the wrong
  phase, the calculator cut π to six decimals and misread `2pi` and printed
  exponents, the forecast labelled yesterday "Today", a note open in the editor
  was lost on exit, and saved tickers vanished after a restart
  ([player/alarm.go](player/alarm.go), [player/terminal.go](player/terminal.go),
  [player/dictionary.go](player/dictionary.go), [player/pomodoro.go](player/pomodoro.go),
  [player/calculator.go](player/calculator.go), [player/weather_cache.go](player/weather_cache.go),
  [player/notes.go](player/notes.go), [player/morefeatures.go](player/morefeatures.go)).

- **The handheld config opened the new Home on the old grid.**
  `player/trimui/jukaconfig.device.json`, which the Smart Pro and Brick builds
  ship, still carried the 0.4.0 Home tile geometry, so the redesigned Home drew
  its cards in the old rectangles there. Its Main scene now matches the desktop
  config. Because `jukaconfig.json` is kept across updates, a config whose
  twelve Home tiles still sit exactly on the old geometry is also moved to the
  new grid in memory when it loads; a layout with any tile moved or resized is
  left alone ([player/home_migrate.go](player/home_migrate.go); tests in
  `player/home_migrate_test.go`).

- **Colour blends towards a darker colour came back unchanged.** `lerpColor`
  took each channel difference in `uint8`, which wraps whenever the target is
  darker, so a blend towards black returned the start colour: every Home
  glyph's shade and deep shade were plain white, and the parcel, folder and
  gamepad icons lost the planes they are drawn with
  ([player/core.go](player/core.go)).

- **A wallpaper that failed to load was retried on every frame.** Each frame
  reopened the file, attempted a decode and logged the failure. A failed path
  now waits 15 seconds before its next attempt, which still lets a wallpaper on
  a card that mounts late appear on its own ([player/core.go](player/core.go)).

- **Smaller fixes found on the way.** A toggle drawn before its font loaded
  dereferenced a nil font. White **GO** text on the browser's cyan button was
  about 1.7:1 and now takes the accent's contrast ink. The loading card's square
  outline was drawn straight across its rounded corners, the text browser's
  scrollbar ran down the side of its toolbar, and the unit converter's result
  rule poked out of its rounded corners. The scrollbar, the Files folder colour
  and the search spinner were dark-theme-only colours that vanished or clashed
  on light presets ([player/core.go](player/core.go),
  [player/textbrowser.go](player/textbrowser.go),
  [player/loadingcard.go](player/loadingcard.go),
  [player/unit.go](player/unit.go), [player/features.go](player/features.go)).

- **An SFTP client's `stat` of a folder described one of its children.** The
  server answered stat from the same listing it uses to browse, and the protocol
  reads only the first entry, so a folder looked like whatever file sorted first
  inside it and an empty folder looked missing — enough to make "create
  directory" fail in a client that calls `mkdir -p` underneath. Stat now answers
  with the entry itself, and a loopback test drives the console's own transfer
  client against its own server, covering connect, host-key pinning, download
  and upload ([player/sftp_root.go](player/sftp_root.go); tests in
  `player/filetransfer_loopback_test.go`).

- **A damaged snapshot can no longer half-overwrite live saves.** A restore
  copied the snapshot's files one at a time, so a truncated copy, a flipped byte
  or a file the card lost was only noticed after earlier saves had already been
  replaced. The whole snapshot is now planned and verified first — the folders
  are matched by path or by a unique name, no entry may escape its folder, a
  save subfolder may not be a link out of the tree, and every file must still
  match the size and SHA-256 recorded at capture time — and a snapshot that
  fails any of that is refused as a whole with "nothing was changed", before the
  safety snapshot is taken and before a live file is touched. Each file the
  restore does write is re-hashed, so a card that fills up part way through is
  caught there instead of at the next boot
  ([player/savemanager.go](player/savemanager.go); tests in
  [player/savemanager_test.go](player/savemanager_test.go)).

- **An archive cannot write outside its folder through a link it shipped.** The
  Windows-runtime extractor used to create the archive's symlinks while members
  were still being written, so a later member could be written *through* a link
  the archive itself supplied. Links are now collected during extraction and
  built once the tree is complete, with both ends re-checked against the
  extraction root, and every member name and link text now also passes the same
  prefix test the tool-download extractor uses — the check the analyser can see
  without following the helper — on top of the existing resolution guard. On the
  FAT/exFAT card, where links are impossible, the alias is still copied as
  before ([player/windows_runtime_install.go](player/windows_runtime_install.go);
  fuzz targets in `player/fuzz_test.go`).

- **The search log can no longer be derived from the API key.** The tool name a
  refused search command reports is now taken from the scene's own command text,
  before the config-provided `--extractor-args youtube:api_key=…` and
  `--cookies` arguments are appended to the argument list, so the string that
  reaches the log has nothing to do with the key. The cache key and the
  remembered paging command still describe the whole invocation, so the next
  page is built exactly as before ([player/core.go](player/core.go)). Three
  allocations stopped sizing themselves from a sum of lengths — merged search
  results, the yt-dlp argument vector and the MQTT length-prefixed string — and
  the battery reader now works in one integer type, which also keeps its charge
  counters from overflowing the percentage math on a 32-bit build
  ([player/media_paging.go](player/media_paging.go),
  [player/streaming.go](player/streaming.go),
  [player/smart_home.go](player/smart_home.go),
  [player/widgets.go](player/widgets.go)).

- **The parsers are fuzzed instead of only reviewed.**
  `player/fuzz_test.go` adds native Go fuzz targets for the
  archive entry and symlink names and for the transfer address field: each
  asserts an invariant (a name either gets refused or resolves below the
  extraction root; an address either gets refused or round-trips through its own
  canonical form), and `go test` runs every seed as an ordinary test. The nightly
  fuzz job now lists its targets per package — `-fuzz` accepts one package at a
  time — and installs the SDL headers the player package needs to build
  (`.github/workflows/nightly.yml`,
  [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#fuzzing)).

- **The browser follows a redirect, and the address bar says where it landed.**
  A `<meta http-equiv="refresh">` stub — "this page has moved" — arrives with a
  200 and a body, so nothing followed it automatically, and the reader sat on the
  stub with the link that would have continued stripped along with the page's
  scripts. A load now follows up to three in-page redirects with the same client
  and cookie jar, resolving the target against the page (quoted, unquoted,
  relative, `URL=` in any case) and refusing a non-web scheme, a target that
  names nothing, or a page that only asks to be reloaded on a longer timer; a
  loop stops at the limit. HTTP redirects were already followed by the client,
  but the toolbar and Back still named the address the reader typed, so they now
  move to the URL the load actually ended on — from the render pass, never while
  the address bar is being edited, and never for a page the reader has already
  navigated past ([player/textbrowser.go](player/textbrowser.go),
  [player/browser.go](player/browser.go); tests in
  `player/browser_redirect_test.go`).

- **The dark blue palette now separates, and its text is readable.** The shipped
  dark blue was one flat field: panels sat 1.11:1 above the page, rows 1.09:1
  above a panel and a focused row 1.27:1 above a resting one, so on the Brick's
  panel every card, row and button read as the same colour as the page behind
  it, and tertiary text (hints, captions, placeholders) sat at 3.4:1 on a card —
  under WCAG AA. The ramp is rebuilt so each step is a measured step (1.20:1,
  1.34:1 and 1.43:1), borders and hairlines rise with the surfaces they sit on,
  secondary and tertiary text clear 4.5:1 on the brightest surface, and the
  shipped configs' `$buttonColor`/`$inputColor` — the fill a scene button and an
  input field actually use — moved from `#1E293B`/`#0F1420` to
  `#2E4061`/`#1C2839`, where they read at 1.85:1 and 1.29:1 instead of 1.31:1 and
  1.04:1. Two defects the lighter buttons exposed are fixed with it: the
  label-colour chooser used a weighted 8-bit sum whose crossover sat at a 23.5%
  mean channel, so a mid-dark fill was given near-black text (~1.7:1) instead of
  white, and a focused tile was tinted with the raw accent — which is the
  config's button colour — so a dark accent painted the focused card darker
  than its neighbours, a hole instead of the brightest state on the page
  ([player/design.go](player/design.go), [player/themes.go](player/themes.go),
  [player/components.go](player/components.go), [player/appgrid.go](player/appgrid.go),
  [player/emutiles.go](player/emutiles.go), [player/toolsui.go](player/toolsui.go),
  [player/jukaconfig.json](player/jukaconfig.json),
  [player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json),
  [script.js](script.js), [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); tests in
  `player/themes_test.go`).

- **The Emulators page is the same page as every other overlay.** It drew the
  shared title bar inside a card that ran out to the screen edge (20px of
  margin, a 22px radius and a hand-reserved 96px band), so its header sat in the
  corner at a different inset from Tools, Downloads and Compatibility and the
  band below it was a guess. The page now uses the shared full-page card —
  centred, 1040px wide (screen-clamped), 20px radius, the same 18px hairline and
  the same 24px body padding — and reserves the strip above the grid with
  `UIPanelHeaderBand`, the measurement the header itself is drawn from, placed
  at `UIPanelHeaderInset` like the rest. Because the card is narrower than the
  old edge-to-edge one, the widest control line (a core to update, a build to
  roll back to and the L1/R2/Y page keys all live) no longer fits: the page now
  sheds whole hints by measurement instead of letting the line be ellipsized,
  dropping the D-pad line first and always keeping Back
  ([player/emutiles.go](player/emutiles.go); tests in
  `player/emulators_test.go`,
  [player/emulatorinstall_test.go](player/emulatorinstall_test.go) and
  `player/page_header_consistency_test.go`).

- **Play Link asks for a link and plays it.** The Media screen's Play Link button
  only acted on a link that was already typed into the URL field, so with nothing
  highlighted it showed a toast and did nothing — and reaching that small field
  first is the very thing a handheld pad makes awkward. The button now opens the
  field on the on-screen keyboard with an empty buffer and the hint "Paste a video
  link, then Save"; Save plays what was typed instead of searching for it, an
  empty Save plays nothing, and dismissing the prompt disarms it so the next
  search still searches. A highlighted result still plays first, and a link
  already in the field still plays without asking again
  ([player/features.go](player/features.go), [player/core.go](player/core.go),
  [player/vkeyboard.go](player/vkeyboard.go); tests in
  `player/media_playlink_test.go`).

- **The Playtime badge list scrolls with the D-pad and the arrow keys.** The
  screen has no config elements, so its only focus nodes were the top-bar
  buttons: focus started on the Back pill, `handleChromeKey` claimed every arrow
  press, and the page's own key handler was never reached, so only the wheel,
  Page Up/Down, L1/R1 and Home/End moved the page while the D-pad and the arrow
  keys did nothing. A scene can now declare that it owns the page keys
  (`claimsPageKeys`), and the focus graph leaves the top bar out for it, so no
  arrow can be consumed by a button the screen is not offering. The bar stays a
  tap target there and is reachable through the Start menu ([player/focus.go](player/focus.go),
  [player/scene.go](player/scene.go); pinned by `TestPlaytimePageKeepsThePadFromTheTopBar`).

- **The Files, Packages and Emulator cards draw real pictures.** The Emulator
  card asked for an `emulator` glyph and the painter only knew `emulators`, so it
  fell through to the unknown-icon ring — a bare circle on the home screen.
  Files and Packages were each drawn in one flat tone (a folder whose tab and
  body were both white, a cube whose faces were 16 and 30 shades apart, which on
  a white glyph is still white), so at the 32px tile both read as blank
  rectangles with a detached tab or seam. Files is now a folder in three planes —
  a dark tab and back panel, a light sheet with a folded corner and two ruled
  lines, and a front pocket whose seam crosses the paper — and Packages is a
  taped parcel: shaded lid, tape band down the middle, shipping label off to one
  side. ([player/homelayout.go](player/homelayout.go); tests in
  `player/homelayout_icon_test.go` pin both the
  missing-glyph fallback and that the two redrawn glyphs paint three or more
  tones).

- **A Brick is identified even when its firmware names no model.** Identification
  trusted a model code in the device tree and a hostname that said "TrimUI",
  and a Brick image is not obliged to provide either: its kernel can describe
  only the SoC (`sun50iw10`, `allwinner,sun50i-a100`), and then the board stayed
  unnamed — no front F1/F2 keys, none of the handheld budgets, and the 4:3 canvas
  only because the panel fallback guessed it. Detection now reads every source
  the image offers and classifies the union: the launcher's exported `DEVICE`
  before its `PLATFORM` code (NextUI reports a Brick as `tg5040` plus
  `DEVICE=brick`), a model code in the device tree, `/proc/cpuinfo` or the
  hostname, the model names in `/etc/os-release` and the vendor launcher's own
  string table (the source community launchers use), and finally the panel —
  1024x768 for the Brick family, 1280x720 for the Smart Pro family — when the
  image is demonstrably TrimUI but nothing names a model. The startup log and the
  new `Device id` diagnostics line print the evidence, so an unidentified device
  reports what its firmware actually exposed instead of leaving it a guess.
  ([player/device.go](player/device.go), [player/device_probe.go](player/device_probe.go),
  [player/device_detect_first_run.go](player/device_detect_first_run.go),
  [docs/DEVICE_TUNING.md](docs/DEVICE_TUNING.md); tests in
  [player/device_probe_test.go](player/device_probe_test.go)).

- **Close and back are two buttons on the video player, and the windowed player
  answers taps.** Backing out of a playing video was one X that only meant
  "step down a size": it floated the video into the mini window and nothing on
  the overlay could end the session, so closing a windowed video meant leaving it
  playing or finding Stop at the far end of the bar. The corner now has Close,
  which ends playback where it stands, and the chevron beside it keeps the old
  behaviour (full screen → the scene's window → the floating window). Both are in
  the D-pad ring, so a pad user reaches either one. The windowed player's controls
  also take taps again: they were drawn over the video element but the hit test
  only ran in full screen, so touches in the window did nothing, and the bound is
  now the laid-out window itself, leaving the rest of the scene to the scene.
  ([player/videoplayer.go](player/videoplayer.go); tests in
  `player/videoplayer_close_test.go`,
  `player/videoplayer_controls_test.go`).

- **The Wi-Fi switch no longer turns the radio off.** `rfkill`'s verbs are
  `block`/`unblock`, and `runRfkill` had them the wrong way round: asking for
  Wi-Fi *on* ran `rfkill block wifi`, and *off* unblocked it. On an image whose
  only radio control is rfkill — every TrimUI handheld, since none ships
  `nmcli` — the Quick Settings switch and the picker's "press A to turn Wi-Fi
  on" both did the opposite of what they said, which is why the list stayed
  empty after turning Wi-Fi on. ([player/systemcontrol.go](player/systemcontrol.go);
  test in `player/wifi_supplicant_start_test.go`).

- **The Brick's Wi-Fi picker finds (and if needed starts) its supplicant.** Three
  gaps kept the picker from ever opening on the Brick. The supplicant's
  `ctrl_interface` was read when building a command but not when looking for the
  socket that *names the interface*, so discovery and the command path could
  disagree about the same device; P2P-device and `_global` sockets sort ahead of
  `wlan0` and were taken as the interface; and the TrimUI images start
  `wpa_supplicant` from the firmware's own Wi-Fi menu, so a device that had never
  had Wi-Fi on there had a radio and no socket — discovery then answered "no
  wireless interface" and the start menu dropped the Wi-Fi row, taking with it
  the only control that could create the socket. Discovery now reads the config's
  own directory first, skips P2P/global sockets, prefers a station name, and the
  picker starts the supplicant itself (once per interface, radio on only) and
  re-probes. The row and the Quick Settings link stay reachable while the radio is
  simply off, opening the picker in "turn Wi-Fi on" mode instead of turning the
  user away. ([player/wifi.go](player/wifi.go), [player/wifi_ui.go](player/wifi_ui.go),
  [player/startmenu.go](player/startmenu.go), [player/quicksettings.go](player/quicksettings.go),
  [docs/WIFI.md](docs/WIFI.md); tests in `player/wifi_supplicant_start_test.go`).

- **A Brick is identified by its model code, not only by its panel.** TrimUI
  images do not all stamp a name into `/etc/hostname`, so the Brick fell through
  to the 4:3 panel fallback: the canvas was compact while the board stayed
  unnamed, which left its front F1 / F2 keys dead and kept it on the desktop
  budgets. Detection now reads the kernel's own board description —
  `/proc/device-tree/model` and `/proc/device-tree/compatible`, with the sysfs
  mirror as a fallback — and accepts the `TG3040`-style model code found there as
  the device, so the Brick family gets its keys and handheld caps. A marketing
  name or an explicit override still works, conflicting codes are refused, and
  the result is cached instead of re-read every frame. ([player/device.go](player/device.go),
  [docs/DEVICE_TUNING.md](docs/DEVICE_TUNING.md); test in
  [player/patch_engine_test.go](player/patch_engine_test.go)).

- **The header stops offering an update the device already has.** The "Update"
  chip and the boot notification read a release recorded in patch state, and
  only a successful GitHub check cleared it — so a Brick without working Wi-Fi,
  or an install that replaced the files without going through the swap helper,
  kept showing the offer for a build it was already running. The advertisement
  is now compared with the running build's own stamp at startup and again
  before it is drawn, so "Update" appears only while it is a real update, and the
  stale entry is dropped from state instead of lingering. ([player/autoupdate.go](player/autoupdate.go),
  [player/updateprompt.go](player/updateprompt.go), [player/patch.go](player/patch.go),
  [player/toolsui.go](player/toolsui.go); test in
  [player/autoupdate_test.go](player/autoupdate_test.go)).

- **Wi-Fi works on a TrimUI Brick, and on any image that ships no `wpa_cli`.**
  The picker drives `wpa_supplicant`, but every command went through `wpa_cli`,
  and the start menu deletes the Wi-Fi row when the backend is unavailable — so
  a Brick that was online and downloading showed no Wi-Fi entry at all. TrimUI's
  TG5040 images (Brick and Smart Pro) run the supplicant with
  `ctrl_interface=/etc/wifi/sockets` and do not all include the client, so
  JukaHub now speaks the supplicant's own control socket when the CLI is absent:
  one command per write, the same parsers, the same password rules, and no
  process per poll (the status bar's signal read gets cheaper too).
  `/etc/wifi/sockets` is part of interface discovery now, and the unavailable
  message names both routes instead of claiming a missing `wpa_cli` that the
  image never had. ([player/wifi_ctrl.go](player/wifi_ctrl.go),
  [player/wifi.go](player/wifi.go); tests in
  `player/wifi_ctrl_test.go`).

- **A 4:3 panel fills the screen even when the hardware cannot be named.** The
  shipped design is 1280x720 and a 4:3 display letterboxes it into a strip with
  bars above and below — exactly what a Brick whose hostname and device tree
  name no TrimUI model showed, with the whole UI sitting in the middle 75% of
  the panel. The compact 960x720 canvas used to be reachable only through device
  detection; now the panel's own aspect picks it, and the startup log names the
  panel it chose. A design that is already 4:3, and the 16:9 Smart Pro panels,
  are left alone. ([player/uiprofile.go](player/uiprofile.go),
  [player/main.go](player/main.go); tests in
  `player/uiprofile_test.go`).

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
  `player/mpv_removal_test.go`).

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
  `player/brickkeys_test.go`).

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
  tests in `player/updatechip_test.go`).

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
`player/layout_bounds_test.go`).

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
  `player/videoquality_revert_test.go`).
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
  tests in `player/wifi_interface_test.go`).
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
  `player/playprepare_cancel_test.go`).
- **The Wi-Fi picker finds the radio on a device that is online.** It reported `No wireless network interface on this device` while the handheld was connected. Interface discovery listed `/sys/class/net` and kept only entries that `DirEntry.IsDir` called a directory — but every entry there is a symlink into the device tree, so the link itself is never a directory and every real radio was filtered out. The probe now resolves the entry before testing it, accepts both the old `wireless` marker and the cfg80211 `phy80211` one, and falls back to `/proc/net/wireless`, to the supplicant's control sockets, and finally to a Wi-Fi interface name when a vendor driver exposes none of those. The status meter's "interface is up" check reads the same list, so a cfg80211 image no longer shows the no-link icon while connected. ([player/wifi.go](player/wifi.go), [player/statusbar_status.go](player/statusbar_status.go); tests in `player/wifi_interface_test.go`).
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
  tests in `player/textcache_test.go`).
- **A launcher full of broken icons no longer grows the icon cache.** An icon
  that fails to decode is cached as a miss so the file is not re-read every
  frame, but the miss skipped the eviction order: the count cap only ever
  counted icons that loaded, while the map grew with every missing image. Misses
  now go through the same bound. ([player/appicons.go](player/appicons.go); test
  in `player/appicons_test.go`).
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
  `player/browser_test.go`).
- **The browser's bottom chips fit their labels again.** Thirteen actions now
  share the row, and equal shares sized it by the count alone, so the widest
  label ("Forward") overflowed its chip on every panel and "Search" did the same
  at 960×544 and below — a control whose word is clipped is one the user has to
  guess at. A chip is now as wide as its own word needs to be at that panel's
  size, and the column's surplus is shared out pro rata, which fits with room to
  spare at 1280×720, 960×544 and 800×480.
  ([player/browser.go](player/browser.go); guard in
  `player/browsercookies_site_test.go`).
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
  tests in `player/wifi_resilience_test.go`,
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
  `player/searchresults_click_test.go`).
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
  `player/browser_devices_render_test.go`).
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

- **Every screen now wears Home's design.** Home's frosted glass — the navy
  backdrop with its cyan and violet blooms, translucent tiles with a hairline
  edge, the 3px cyan focus ring that lifts a control with a soft bloom behind
  it, and the inset footer band with glass button badges — is now one shared
  recipe in [player/ui_glass.go](player/ui_glass.go) that every other screen
  draws with, instead of the older slate recipe that made leaving Home look
  like opening a different app. Scenes stand on Home's backdrop rather than the
  stock wallpaper behind an opaque panel; the status row has no bar chrome; the
  footer is Home's inset band with right-aligned hint badges at Home's 16px
  type; and buttons, inputs, toggles, list rows, page headers, category tiles,
  overlay sheets and their dim, the Tools and Settings tile pages, Quick
  Settings, the Start menu, notifications, the calendar and weather cards,
  Files, Apps, Emulators, Downloads, Favorites, Chat, the on-screen keyboard,
  the browser's toolbar and panels, the unit converter, Shorts, Live TV, the
  disk chart, the loading card and the module screens (Calculator, Notes,
  Alarm, Pomodoro, Dictionary, PDF reader, Terminal, Music, Dashboard, About,
  Playtime, Patches, Games) all draw those surfaces
  ([player/components.go](player/components.go), [player/core.go](player/core.go),
  [player/extras.go](player/extras.go), [player/header.go](player/header.go)
  and the screens themselves). The glass is theme-aware: on a light preset it
  is mixed from a deep slate instead of white, and text that was hard-coded
  light now takes the theme's inks, so the six light presets get visible cards
  and readable type.

- **Focus, pills and highlights use the theme's accent.** The shell's accent
  was `$buttonColor`, a surface colour (slate by default), so every scene's
  focus ring and selection was a navy that barely showed beside Home's cyan. It
  is now the active theme's accent, and a config reload applies it after the
  preset instead of overwriting the preset's accent with the button colour.
  `$buttonColor` stays as a light tint under scene buttons
  ([player/main.go](player/main.go), [player/ui_glass.go](player/ui_glass.go)).

- **The dark palette's surfaces are Home's blue-grey.** The opaque panel, card,
  raised and focus surfaces were a saturated slate that clashed with the glass;
  they are now the neutral blue-grey white glass makes over the navy
  (#1B2029, #242B37, #2D3644, #404D62) at the same luminance, so every contrast
  ratio the palette tests pin is unchanged
  ([player/design.go](player/design.go), [player/themes.go](player/themes.go)).

- **Lighter frames on the handheld.** The stock 2560x1440 `background.jpg` —
  about 15 MB of texture — is no longer loaded, because nothing draws it. A
  wallpaper the user picks is scaled once to the canvas (1280x720, or 960x720
  on the Brick) and kept as an opaque static texture, so a frame copies a
  canvas-sized image with blending off instead of minifying a full-size photo.
  The scene panel's 1232x590 rounded texture, its three stacked shadows, sheen
  lines and corner dots, and the drop shadows under buttons, chips and cards
  are gone as well ([player/core.go](player/core.go),
  [player/ui_glass.go](player/ui_glass.go)).

- **The release offer is a quiet system chip, not a warning banner.** It sat in
  the same corner as the notification bell but at the labelled buttons' full
  height, filled with a warning tint and outlined at 200 alpha — the loudest
  thing in the bar, next to a bell that is a plain surface chip. It now takes the
  bell's height (the shared button height minus the same step), the same
  `ColorIconSurface` fill with no outline, a secondary-colour label and a
  smaller, dimmer arrow, so an available release reads as one more system
  control. Geometry still comes from one helper, so Home and every scene draw
  the same pill, and the focused state is the same accent ring the bell uses
  ([player/updateprompt.go](player/updateprompt.go); pinned by
  `TestReleaseOfferWearsTheBellsSurface`, `TestSceneHeaderUsesTheSharedButtonHeight`
  and `TestHomeUpdateChipSitsLeftOfTheBell`).

- **The Benchmark screen has one Run button that measures both phases.** Run and
  Random were two buttons running two tests, so pressing Run reported half of what
  the screen measures and recorded the board entry from the sequential pass alone;
  the random pass happened only if someone remembered the second button, and its
  numbers then replaced the first pass's text. One press now runs the sequential
  50 MB stream and then the 10 MB scattered 4 KiB test automatically, reports all
  four numbers in one panel (or which phase failed, and the other phase still
  runs), keeps recording the board entry from the sequential pass, and still
  stops on leaving the screen. The old `benchmark_random` trigger is left handled
  for hand-edited configs ([player/morefeatures.go](player/morefeatures.go),
  [player/jukaconfig.json](player/jukaconfig.json),
  [player/trimui/jukaconfig.device.json](player/trimui/jukaconfig.device.json);
  tests in `player/benchmark_test.go`).

- **The notification bell is a header button, not a headline.** It was sized off
  the status font and came out a 54×40 capsule with a 32px glyph on the 1280
  canvas — taller than the Back pill and nearly twice the height of the caption
  text beside it, with an unread badge that hung over the bar's top edge. It now
  takes the shared header button height minus a step (a square 32px chip), the
  glyph is inset to 20px so it reads with the clock, and the count is drawn on
  its own small font inside the corner. Home and the scene bar draw it through
  one sizing rule, so the corner is identical on both ([player/notifications.go](player/notifications.go),
  [player/homelayout.go](player/homelayout.go); pinned by `TestBellChipMatchesHeaderMetrics`
  in `player/notifications_test.go`).

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
  tests in `player/backgroundwork_test.go`).

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
  `player/shape_cache_animation_test.go`).
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
  `player/memory_growth_test.go`,
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
  tests in `player/header_test.go`).

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
  (`player/docs_links_test.go`): every relative
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
  `player/wifi_windows_test.go`).

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
  `player/errorwrap_audit_test.go` fails on a
  new `%v` wrap instead of relying on review.
- The intermittent `SDL_RenderReadPixels` crash in the test suite is fixed at
  its cause. The static-layer caches (on-screen keyboard, chat, video overlay)
  kept textures after their renderer was destroyed; a later probe then reused
  or freed a dangling pointer and corrupted SDL's allocator. The caches now
  record their owning renderer, drop a foreign texture without touching it, and
  the test renderer releases them before destroying its renderer
  ([player/vkeyboard.go](player/vkeyboard.go),
  `player/renderer_cache_owner_test.go`).

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

### Fixed

- **Chat overlays let go of the pad when you leave the chat scene.** The
  message action bar, the member list and the profile card are all drawn inside
  the chat panel, but their input guards only asked whether they were open — so
  a bar or a card left behind by a scene change kept eating the next press on
  the screen you were actually looking at (the floating video's **X** on Home
  was one of them). Each now checks that the panel that draws it is the scene on
  screen and drops itself when it is not, so the press lands where it looks like
  it will ([player/chatactions.go](player/chatactions.go),
  [player/chatprofile.go](player/chatprofile.go),
  [player/controller.go](player/controller.go), [player/main.go](player/main.go),
  [docs/CONTROLS.md](docs/CONTROLS.md); tests in
  `player/chat_profile_test.go`).
