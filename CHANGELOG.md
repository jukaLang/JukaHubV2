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

- **Emulators offers only the cores that load on this TrimUI, and JukaHub's
  glibc is ready before they need it** (Juka, 2026-11-23: "Make sure all
  emulators that get updated work on glibc...we want to show the emulators
  that Trimui devices can run...many of emulator cores are asking for glibc
  4.29...but even our private glibc is at 2.39"). What those cores ask for is
  `GLIBCXX_3.4.29` and `3.4.30`: versions of the C++ library (GCC 11's and
  12's libstdc++), not of glibc. JukaHub's private runtime is Ubuntu 24.04's —
  glibc 2.39 with GCC 14's libstdc++, which defines every GLIBCXX up to 3.4.33
  — so it covers every buildbot nightly seen so far, once the complete runtime
  is installed and RetroArch starts on it. Now: JukaHub installs that complete
  runtime by itself a few minutes after start on a handheld whose RetroArch
  can use it, and probes RetroArch with it, instead of fetching it in the
  middle of an install; JukaHub's cores workflow surveys every linux/aarch64
  build the buildbot publishes each week (the libraries each links, the symbol
  versions it needs) into `buildbot-needs.json`, and the Emulators page and
  **Get more cores** check each core against this handheld's libraries and
  the runtime before anything is downloaded — a core that loads neither way
  (desktop OpenGL, a library the handheld lacks, a version newer than the
  runtime has) is not offered unless JukaHub builds it for TrimUI itself, in
  which case an install takes that build at once; the same build is refused
  before a download by the daily update, so no update puts a core that cannot
  load in front of a game. Tiles say "runs on JukaHub's glibc 2.39" or
  "JukaHub's build for TrimUI", and a refusal that names a GLIBCXX version
  says whose it is. See
  [COMPATIBILITY](docs/COMPATIBILITY.md#cores-the-handheld-does-not-offer).

- **RetroArch for a card that has none, on JukaHub's glibc** (Juka, 2026-11-22:
  "Download latest version of working RetroArch (if it's not installed)...make
  sure GLIBC is private library that we use and that it's compatible with
  Trimui Smart pro and other Trimui devices"). RetroArch is not part of the
  TrimUI firmware: it comes on the card, in TrimUI's SD base package, so a card
  set up without that package had none, and every game, core install and
  core update stopped at "RetroArch not found". JukaHub now installs TrimUI's
  newest `ra64.trimui` for the model from TrimUI's own packages on GitHub
  (`trimui/assets_*`; the A133P handhelds — Smart Pro, Brick, Hammer, Brick
  Pro — share builds, the Smart Pro S has its own), reading only each zip's
  directory and the files it uses. Up to three builds, newest first, are
  checked on the handheld before anything is written — the firmware's loader
  and JukaHub's private one list and bind every library and symbol version —
  then the RetroArch folder of the model's own package goes in (never over a
  file the card has; without menu pictures and shaders when space is short) and
  the binary last. The private glibc 2.39, with its C++ library, is installed
  first, and every launch JukaHub makes of that RetroArch runs on it. It
  happens a minute and a half after start (unless Emulator updates is Off),
  from the Emulators page's RetroArch tile, or when a game finds no RetroArch;
  the daily pass keeps it on TrimUI's newest build. A RetroArch that came with
  the card is never touched. See
  [PRIVATE_GLIBC](docs/PRIVATE_GLIBC.md#retroarch-on-a-card-without-one).

- **Faster menus: the CPU speeds up the moment you press** (Juka, 2026-11-22:
  "Optimize JukaHub to run even faster and have better performance on Trimui
  devices...feel free to manage cpu freq"). Automatic CPU speed kept Home and
  the menus at 1.2 GHz, 816 MHz once quiet, under the firmware's `ondemand`
  governor, so the frames a press asked for were drawn at that clock until its
  next two-second sample. Now a press (and a held button's repeats) lifts the
  hub session's ceiling to the top the heat guard allows and its floor to the
  first operating point at 75% of the top (1.6 GHz on the Smart Pro) for 1.5 s
  after the last press, 4 s after a game hands the screen back and 6 s at the
  start, always in the Performance power mode, and wakes Automatic CPU speed's
  ticker at once. Settings → Games & emulators → **Faster menus**
  (`menuBoost`, on); it rests under Battery Saver, while the chip is warm or
  the kernel throttles, on the idle screens (which end it before lowering the
  clock), during the voice assistant's own boost and in games. While you
  press, Low power no longer holds the menus at 24 fps (its promise is a
  slower frame rate when nothing moves): they draw at 30 fps, and at up to
  60 fps while the clock is up and the screen's frames cost 11 ms or less. A
  press also ends the frame's sleep at once (polled every 8 ms: up to 66 ms
  less wait on a menu left alone), and the screen's thread runs at nice −5
  with `SCHED_RESET_ON_FORK`, ahead of background work, while everything it
  starts begins at nice 0 (`uiboost.go`, `uiprio_linux.go`, `autoperf.go`,
  `adaptive.go`, `saver_power.go`). Not run on a device yet (MENUBOOST-HW-1).
- **The music player has everything UIPod has** (Juka, 2026-11-21: "Make sure
  to have all features in our music player as https://famiclone.dev/UIPod/").
  A third Now Playing layout, **iPod** (`musicpod*.go`): click-wheel menus
  (Music, Videos, Podcasts, Audiobooks, Extras, Settings, Shuffle Songs, Now
  Playing) over this device, Navidrome, Jellyfin or all of them folded together
  (`musicsrc*.go`), 38 four-shade themes, six screen transitions, Bayer-dithered
  album art, the wheel's tick, On-The-Go, Play Next, Info, Rename and Delete in
  a context menu. **Sound effects** (`musicfx*.go`, R2 or X): Walkman Tape Mode
  (wow and flutter, hiss, saturation, roll-off, tape speed), reverb, ping-pong
  delay, chorus, flanger, and speed from 1/16× to 2×. **Ten real visualizers**
  (`musicviz.go`) on a 1024-point FFT of what the speaker plays, drawn in four
  dithered shades; the artwork tile's bars follow the real spectrum too.
  **AutoEQ** headphone profiles with the AutoEq index kept for offline search
  (`musicautoeq.go`), and the iPod's twenty EQ presets on the 31-band PEQ (L2
  opens it on a pad now). **Navidrome and Jellyfin** (`musicsrc_subsonic.go`,
  `musicsrc_jellyfin.go`): browse, stream through a loopback proxy that keeps
  tokens off command lines and caches songs (`musicproxy.go`, 2 GB by default),
  Download Offline into Music/Navidrome and Music/Jellyfin, Jellyfin films and
  series (direct stream or HLS transcode, resume reported back), Quick Connect.
  **Scrobbling** (`musicscrobble.go`): Last.fm signed in by a QR code,
  ListenBrainz, Navidrome play counts, with listens queued in `scrobbles.json`
  while offline; the phone's **Music servers** page sets it all up
  (`companion_music.go`). **Games**: Music Quiz and Sound Racer (song maps kept
  in `racer-maps/`, chiptunes included) on the iPod's Extras, Brick Breaker on
  the Games screen. The library reads genre and composer tags (index v3 reads
  the songs' tags once more), and **Ukrainian** is an interface language
  (`i18n/uk.json`: menus, labels and the player; long help texts stay English).
  Release builds need the `JUKAHUB_LASTFM_API_KEY`/`JUKAHUB_LASTFM_SECRET`
  secrets for one-tap Last.fm (docs/MUSIC_SERVERS.md). Not run on a device or
  compiled yet (UIPOD-HW-1).
- **More speed for heavy games** (Juka, 2026-11-21: "improve performance even
  further"). Heavy-game boost now also moves JukaHub's own threads behind the
  game (nice 10, idle I/O class; each thread's own value comes back after, and
  a worker that was lower stays lower) and, on the Smart Pro S, holds the game
  to its 2.0 GHz cluster from the start (`xboxboost_hub_linux.go`). Emulator
  speed's Fast and Fastest give the bigger consoles an audio buffer of at least
  64 ms, so the cores' automatic frameskip skips only when a game really falls
  behind, and on the Smart Pro and Brick turn on RetroArch's threaded video
  while Threaded video is on Auto. Not run on a device yet (EMUSPEED-HW-1).
- **Silent boot: the bootloader starts Linux quietly** (Juka, 2026-11-21: "Can
  we hook JukaHub to load even faster? Maybe we can modify the bootloader on
  Trimui devices (slightly) ... so we don't have to wait 9 seconds"). TrimUI's
  bootloader passes `loglevel=8`, so the stock kernel prints every message to
  an unused 115200-baud serial port and waits for it (the stock Smart Pro's
  kernel took 4.2 s to reach its root). **Settings →
  General → Silent boot** (A twice) sets that one value of the bootloader's
  saved settings to 1, the way U-Boot's own `saveenv` writes: into the copy
  U-Boot is not loading, with the next serial, so a power cut mid-write
  changes nothing; only where two copies exist and the value demonstrably
  drives the running kernel (and the kernel image names no level of its own);
  both copies saved to `data/bootenv/` first; A twice puts the original back,
  and the next boot's `/proc/cmdline` shows whether it took (**No effect**
  otherwise). `JukaHub silent-boot [status|on|off]` does the same over SSH.
  Not run on a device yet (BOOT-HW-5).
- **A quieter, quicker start on the stock firmware** (same request): before
  SDL opens the GPU, sound and input devices, JukaHub on a TrimUI sets the
  kernel console's level to 1 and expedites RCU grace periods (SDL's joystick
  setup closes every input device, and each close waits for one), and puts
  both back at exit (`JUKAHUB_KERNEL_TUNE=0`
  leaves them). With **Boot into JukaHub OS** on a verified stock Smart Pro a
  new first-service hook (`jukahub-quietboot`, `S01`) does both for the whole
  boot, installed and restored with the other stock speed hooks. The Smart Pro
  SD image now boots `quiet loglevel=1` (warnings no longer print either).
- **GameCube, Wii, PS2 and Xbox on every TrimUI model** (Juka, 2026-11-21:
  "GameCube, PS2, and XBOX should all work on all Trimui devices (even if
  performance is not great)"). GameCube & Wii's Automatic now plays them on the
  Smart Pro, Brick, Brick Pro and Hammer too (before, those needed Try
  anyway), at GameCube speed Fastest; PS2 was already on everywhere. hakuX
  (Xbox) is offered on every model, marked untested away from the Smart Pro:
  there, after the Smart Pro package is installed, spruceUI's build of the same
  emulator (`spruceUI/hakuX-spruce`, release `beta-main`, checked against
  GitHub's SHA-256 digest) replaces its binary — it draws at the panel's own
  mode (the Brick's 1024x768) and runs on any Vulkan driver, the Smart Pro S's
  Mali included (`hakux_fit.go`). An existing install gets it at the next
  press of X. Not run on a device yet (EMUSPEED-HW-1).
- **CPU boost in games, and a heat guard on every TrimUI** (Juka, 2026-11-21:
  "boost performance when running emulator by adjusting CPU frequency
  safely...make sure to not overheat device by checking temperature (All
  Trimui devices)"). Settings → Games & emulators → **CPU boost in games**
  (Auto / Always / Off): three seconds after a heavy game starts, once the
  firmware's `cpufreq.sh` has run (it used to put emulators back on `ondemand`
  or under a lower ceiling, and Automatic CPU speed then left them alone),
  parked cores come online and the clock is held at the top of what the launch
  may use, the floor too under a dynamic governor so none idles it between
  frames. Auto takes N64, PSP, Dreamcast, DS, Saturn, Jaguar, GameCube, Wii,
  PS2, ports and the Heavy-game boost's games (PlayStation and the 8/16-bit
  consoles keep following Automatic CPU speed); a game's own Performance
  option is now a boost, even with Full speed while playing off. The **heat
  guard** (no off switch) reads the hottest CPU/GPU zone and the battery every
  2 s, whoever set the clock (Automatic CPU speed, a boost, a game's own CPU
  setting, a launch script, or Automatic CPU speed switched off), and lowers
  the ceiling one operating point at a time past a per-model limit, replacing
  Automatic CPU speed's fixed 80/88 °C: Smart Pro 80/87/95 °C, Smart Pro S
  82/88/95, Brick Pro 76/83/92, Brick and Hammer 75/82/92, battery 45/50/55,
  always 10 °C under the kernel's own critical trip. **Heat limit** (Cooler /
  Standard / Warmer) moves them. Steps stop at 1.0 GHz (critical ones at
  600 MHz); full speed returns step by step once it has cooled, and a level
  that proved too hot is retried only every 2 minutes (tuned on a thermal model
  of the Brick and the Smart Pro so the clock settles instead of rocking). A
  RetroArch game gets "Hot (84 °C): CPU slowed to 1.6 GHz to cool down" and
  "Cooled down: full CPU speed again" on its message line, JukaHub's in-game
  menu shows "(cooling)", and the Drivers page's Thermal line and the AI's
  performance check say where the guard starts. Floors and ceilings are put
  back compare-and-restore in the order the 4.9 kernel accepts, and the crash
  marker now covers floors and woken cores too (`heatguard.go`, `cpuboost.go`,
  `autoperf.go`). Not run on a device yet (HEAT-HW-1).
- **Emulator speed, per console, and a wider Heavy-game boost** (Juka,
  2026-11-21: "improve performance of all emulators...especially on Trimui
  Smart Pro and Trimui Brick XBOX, PS2, Gamecube, Wii support"). Settings →
  Emulation has a **Speed** row (per console under "Adjust for"): **Fast**
  skips a frame only when the handheld falls behind — the cores' own `auto`
  frameskip in PCSX-ReARMed, FBNeo, MAME 2003 / 2003-Plus, PicoDrive, Genesis
  Plus GX, Snes9x, gpSP, mGBA, Handy and Beetle PCE Fast, PPSSPP's automatic
  frameskip, Flycast's automatic skip, YabaSanshiro's frameskip — turns on
  Mupen64Plus-Next's and melonDS's threaded renderers, N64 frame duping,
  DeSmuME's JIT, Virtual Jaguar's fast blitter and Opera's DSP thread, and
  switches RetroArch's run-ahead, preemptive frames and rewind off above the
  8/16-bit consoles; **Fastest** adds PSP lazy textures, low curve detail and
  skipped GPU read-backs, Dreamcast without its sound DSP, mipmaps or
  anisotropic filtering and with per-strip sorting, an asynchronous PCSX-ReARMed
  GPU thread, DeSmuME frameskip 1, ARMSX2's EE at 75% with mild cycle
  skipping, and N64 Light graphics. **Auto** leaves the 8/16-bit consoles
  alone, gives PlayStation, N64, DS, arcade and the CD consoles Fast, and PSP,
  Dreamcast, Saturn, Jaguar and PS2 Fastest on the Smart Pro, Brick, Brick Pro
  and Hammer (Fast on the Smart Pro S). Everything is written for one launch
  and put back when RetroArch exits; only options still at the core's default
  change, and a game's own options file is never touched (`emuspeed.go`).
  JukaHub's own PPSSPP gets the PSP's level in `ppsspp.ini` the same way
  (`emuspeed_ppsspp.go`); Games & emulators → **PS2 speed** is the PS2's level
  beside its switch, and the Speed row with GameCube or Wii picked is GameCube
  speed. A heavy console's core logs warnings only once it has played a game in
  that run, instead of writing every info line to the card while it plays.
  **Heavy-game boost** (formerly Xbox boost; key `xboxBoost` unchanged) now
  also takes PSP, Dreamcast (NAOMI, Atomiswave), Saturn, Jaguar, N64 and DS
  games on the A133P models, raises a memory-controller devfreq node where the
  kernel has one, gives the game's disk a 1024 KB read-ahead for streamed disc
  images, and sets the swap up at once when such a game starts in the first
  minutes after power-on; all of it is put back after the game. hakuX (Xbox)
  is now offered on the Brick and Hammer too, marked untested and never
  installed by itself (no sticks: hakuX's Menu maps controls per game). The
  cores-aarch64 builds of mGBA and WASM-4 are tuned for the Cortex-A53 like
  Dolphin's. Not run on a device yet (EMUSPEED-HW-1).
- **Pads: a 64-pad sample sequencer** (Juka, 2026-11-21: "add features of
  PocketPads"). A new app beside the Synth (Apps, the Tools hub, the Start
  menu, Home search "pads" / "sampler"): four banks of sixteen pads, eight
  patterns of 16 to 128 steps with a note and an accent per step, row painting
  (hold A and move), and a song chain of up to 64 slots. Each pad has volume,
  pitch and fine tune, a one-knob low/high-pass filter, trim, fades, reverse,
  one-shot / hold / loop play, choke groups, pan, echo and reverb sends, lo-fi
  and mute; **Chop now** cuts a sound into 4, 8 or 16 pads in equal slices or
  at the hits it hears. Select on the Play page is finger drumming (the D-pad
  and Y X B A play two rows of four); Record live hits writes them into the
  playing pattern. The sticks play effects, chosen per direction on the FX
  page: filter sweeps, tempo bend, fade out, echo, reverb, stutter, crush,
  auto-pan and tape stop, with "Hold" amounts for the Brick. A master
  compressor, a tempo-synced echo, a small reverb and **Export to WAV**
  (`Downloads/music/Pads 001.wav`). Sounds come from a built-in kit rendered by
  the Synth's engine, Voice Memos ("Record a new sample" opens it), a
  `Samples` folder on the card, GrainBrick installs (their saved WAV files;
  `PP_GB_DIR` is honoured), Music and Downloads/music; WAV is read directly,
  other formats through ffmpeg, 30 s per sound and ~96 MB in all. The project
  saves to `pads.json` (in backups). No MIDI. New files `player/pads.go`,
  `pads_ui.go`, `pads_engine.go`, `pads_samples.go`; docs/PADS.md and
  CONTROLS.md. Not compiled or tried on a device yet (PADS-HW-1).

- **Windows Apps downloads a program to try Wine with** (Juka, 2026-11-21:
  "Improve running WINE by downloading a simple application to run on trimui
  smart pro"). An empty Windows Apps list now says "A downloads Notepad++
  (about 16 MB) to try Windows apps with", and Setup has a fourth row,
  **Sample program: Notepad++ 8.9.9.1**. A press fetches Notepad++'s official
  portable zips from its GitHub release: the 64-bit build, which Box64 runs
  directly, and the 32-bit one, which also goes through Wine's WoW64 layer as
  most old games do, so the two rows tell which path works. Each zip must
  match the SHA-256 the release publishes (pinned in
  `player/windowsapps_sample.go`); it is unpacked into
  `data/windows-apps/Notepad++/64-bit` or `32-bit` without its updater, its
  `notepad++.exe` must be a Windows program for that CPU, and the folder is
  renamed into place only when complete, so the list shows **Notepad++
  (64-bit)** and **Notepad++ (32-bit)** and never half a build. If the panel
  is still open the 64-bit one then starts, or the setup page offers the
  runtime first (Install & Run); a folder there that JukaHub did not make is
  never replaced. The row and the empty list show the download's bytes, or why
  it failed, and A retries. Not run on a device yet (APP-18).
- **Fast forward keeps the speed you pick in RetroArch** (Juka, 2026-11-21,
  quoting a Reddit post: "When fast forwarding it defaults to the max speed
  possible ... when I go into retroarch and change it it saves for that
  instance but if I power the device off ir put it to sleep it defaults back
  to max speed"). Settings → Emulation → Fast-forward speed used to write its
  own value at every launch, so a limit changed in RetroArch's menu lasted one
  game. Now, when RetroArch saves a different `fastforward_ratio` on exit,
  JukaHub takes it as the setting that launch used (the console's own if it had
  one, All consoles otherwise), says so in a toast, and writes it next time. The
  row gained 1.5×, 5× and 10× and shows a kept odd speed such as 2.5×, and the
  in-game menu's Fast forward line names the speed ("Run the game at 3× ...").
  A power cut mid-game stops RetroArch before it saves, so a change made then
  is not seen.
- **The text editor colours code, and a hex editor opens any file** (Juka,
  2026-11-21: "improve built in file editor...highlight keyworsds...suppport
  languages like perl, python, json, javascript, bash, and c++...also allow
  "open with" hex editor"). Files' text page now colours keywords, types and
  builtins, strings, numbers, comments, function names, variables and
  preprocessor/decorator lines for shell, Perl, Python, JavaScript/TypeScript,
  JSON, C/C++, Java and config files (INI, RetroArch `.cfg`/`.opt`, TOML, YAML,
  `.env`), picking the language from the file's name or its `#!` line.
  Block comments, Python docstrings, template strings, shell and Perl
  here-documents and Perl POD are followed across lines; a regex against a
  division, a Perl hash against the modulus and a shell word as a command are
  told apart by the token before them. The colours come from One Dark / One
  Light and are run through the readable-ink check of the theme (7:1 under
  Increase Contrast). View → **Syntax colours** (H on a keyboard) switches
  them off, and View → **Highlight as** colours the open file as another
  language. Those languages' files (`.py`, `.pl`, `.cpp`, `.bashrc` …) now
  count as text everywhere Files asks, and the keyboard no longer capitalises
  the first letter of a line of code. **Hex editor** is in Open with for every
  file (and in the text page's View menu); a binary file opens there instead
  of the old read-only hex view. A on a byte edits it with the D-pad (Up/Down
  count a digit, Left/Right step to the next, A keeps, B puts the bytes back),
  X types a run of bytes (`4A 75 6B 61` or `"text"`), a keyboard types hex
  digits straight in, View has Go to offset (hex or `50%`), Undo and Undo every
  change, and changed bytes show in the warning colour. Bytes are only
  overwritten — the file never grows or shrinks — and nothing is written until
  Y: the save refuses a file that changed on disk since it was opened, writes
  files up to 64 MB as an atomic copy and larger ones (disc images) in place.
  New: `fileeditor_syntax.go` (the lexer), `fileeditor_syntax_draw.go`
  (colours, per-line state cache, coloured row pieces), `fileeditor_hex.go`
  and `fileeditor_hex_ui.go`; `docs/TEXT_EDITOR.md`.

- **The browser draws pages closer to a real browser** (Juka, 2026-11-20:
  "Improve ... Webbrowser (it needs to render more accurately)"). **SVG** is
  drawn: inline `<svg>` icons and logos (in the text colour for
  `currentColor`, with the page's CSS `fill`/`stroke`, sprite sheets used
  through `<use>`), `<img src="….svg">`, SVG `<picture>` sources and the
  `data:image/svg+xml` URIs CSS backgrounds use — paths, shapes, transforms,
  `<style>` classes, opacity and linear/radial gradients, by a small
  rasteriser of the reader's own (`player/webpage_svg.go`); they used to be
  empty boxes. **Images inside a line stay in it**: README badges, emoji
  pictures and the icon in a link or a button sit in the text instead of
  breaking it into separate rows, and a link that is only an image is a
  D-pad stop. **Runs share a baseline**, so a large word, small print and
  code no longer sit at different heights. **`display: inline-block`** boxes
  (nav items, pagination, tag lists, button rows) sit side by side, each as
  wide as its content and aligned like the parent's text, and an inline or
  inline-block `<li>` loses its bullet. **Bold is real** (Inter SemiBold, or an
  emboldened copy of a picked face) instead of the text drawn twice a pixel
  apart; `<pre>`, `<code>`, `<kbd>` and a monospace `font-family` use a
  monospace face when the machine has one (Consolas or Cascadia on Windows,
  DejaVu/Liberation/Noto Mono on Linux, or a `*Mono*.ttf` in JukaHub's fonts
  folder), so ASCII art and code columns line up; icon fonts no longer leave
  stray "…" marks, and Material Icons names become the nearest arrow, tick or
  cross Inter has instead of words like `arrow_forward`. **Headings keep
  their size** up to twice the body text (they stopped at 1.5×). **Images are
  sized like the text** (CSS px at the same scale; a 120px picture was drawn
  at two thirds of that beside the text) and their `width`/`height`
  attributes win over the file's own size, so a high-resolution avatar no
  longer balloons. **Shadows are soft** (`box-shadow` blur and spread, the
  first visible outer layer of a list), **rounded images are round**
  (`border-radius` clips the picture; avatars are circles), positioned boxes
  honour `transform: translate()` and percentage insets (the
  `left: 50%; transform: translate(-50%, …)` centring), a nested inline
  element keeps its parent's highlight, and a `<code>` chip shows inside a
  tinted block. CSS_SUPPORT.md lists the details and what is still skipped.

- **GameCube speed** (Juka, 2026-11-21: "are there any optimizations we can do
  for Trimui Smart Pro to get Gamecube sort of running at least a few fps?").
  **Settings → Games & emulators → GameCube speed** sets how much of Dolphin's
  accuracy each launch trades for frames, written for that launch and put back
  when RetroArch exits. **Fast** changes no game's picture: Dolphin's CPU on a
  thread of its own (dual core), shaders compiled in the background, native
  resolution, no duplicate frames, EFB copies written back only when read, and
  no run-ahead, preemptive frames or rewind. **Fastest** adds three trades:
  Dolphin skips the CPU's reads of the picture (effects that read the screen
  vanish), the GameCube's CPU runs at half its clock (busy scenes slow down
  inside the game instead of the whole game crawling) and vsync is off (the
  picture may tear). Automatic is Fastest on the Smart Pro, Brick and Brick
  Pro and Fast on the Smart Pro S. Options saved for a game in RetroArch stay,
  and so does a value set in Core Options, except dual core, background
  shaders, duplicate frames and deferred EFB copies. The Emulation page's
  Automatic audio now uses the fast resampler for GameCube, Wii and PS2, and
  JukaHub's own Dolphin build is tuned for the Cortex-A53 (`-mtune`). These are
  the settings that cost Dolphin most on these chips; how many frames they buy
  depends on the game, and nothing here was compiled or measured on a device
  yet (`dolphin_speed.go`; DOLPHIN-HW-1 step 8;
  [Game library](docs/GAME_LIBRARY.md#gamecube-and-wii-on-the-handheld)).
- **Audit toolkit: eight more tools that install on the Smart Pro**
  (2026-11-20). For TLS and SSH set-ups, testssl.sh, sslscan and ssh-audit;
  on the network, tcpdump, which captures on the handheld to a file Wireshark
  opens on a computer; for code, OSV-Scanner (known vulnerabilities in a
  project's dependencies) and Gitleaks (passwords and keys committed to code);
  for files, YARA (rule-based malware scans) and Binwalk (what is inside a
  firmware image). All are open source. On the handheld six come from Ubuntu's
  signed arm64 packages through Linux packages (testssl.sh with Ubuntu's
  `bash`, and its data found through `TESTSSL_INSTALL_DIR`), and OSV-Scanner
  and Gitleaks from the project's arm64 release, checked against its SHA-256
  list (the Toolbox now reads OSV-Scanner's `osv-scanner_SHA256SUMS`). Windows
  and desktop Linux get winget, the project's build or the package-manager
  line. Not compiled or tried on a device yet (AUDIT-HW-2)
  ([Toolbox](docs/TOOLBOX.md#audit-toolkit)).
- **Quick menu, game art, colours, Home views, Device Sync and more**
  (2026-11-20). None of the entries here was compiled or tried on a device yet
  (QMENU-HW-1,
  ART-HW-1, COLOUR-HW-1, HOMEVIEW-HW-1, DEVSYNC-HW-1, NETLINK-HW-1, MEDIA-HW-1,
  FILES-HW-1, QSET-HW-1, SAFEOFF-HW-1 and EMUTUNE-HW-1 in the backlog).
- **Quick menu.** A full-screen menu: big Recents, Collections, Games and
  Tools tiles over round Wi-Fi, Sleep, Restart, Power off and Settings buttons.
  Open it from Start → Device, a Home tile, a control-profile button
  (`quick_menu`), the Brick's F1/F2 or the Smart Pro S's Home key, or set
  Settings → Controls → Menu button on Home to Quick menu; Settings → Power &
  battery → Start in can open it at power-on. Restart and Power off ask first
  with Cancel picked. A NextUI theme's `.media/quick_<Name>.png` pictures fill
  the screen for the focused entry, and Backgrounds only hides the tiles
  ([Quick menu](docs/QUICK_MENU.md)).
- **"Turning on Wi-Fi…" and "Turning on Bluetooth…"** show in the activity
  strip with a spinner while the radio comes up, wherever it was switched on.
- **Game art settings and the Artwork Manager.** Settings → Games & emulators:
  Game art off for text-only lists; Art type box art, screenshot or title
  screen (fetched from the libretro thumbnails with Fill gaps, kept beside the
  box art and renamed, moved and deleted with the game); Art style thumbnail or
  a dimmed full-height background behind the list; art width and corner radius
  (list layout; the defaults look as before). A console's own picture (NextUI's
  `Roms/<console>/.media/bg.png`, or `data/backgrounds/<System>.png`) sits
  behind its game list. The Artwork Manager (Start → Tools, Settings or the
  library's options) counts every console's pictures, fetches one console or
  all and shows which pictures each game has; Reset artwork moves a console's
  pictures in JukaHub's art folder to the trash after a question that says what
  goes ([Game library](docs/GAME_LIBRARY.md)).
- **Colours of your own.** Every colour row in Theme & look opens an RGB picker
  (sliders, a live swatch, #RRGGBB and contrast, X to type, Y for presets). New
  Background, Surface, Text and Focus ring colours sit on top of any theme,
  Menu colour takes any colour, eight palettes (Classic, Night, Paper, Sakura,
  Forest, Ocean, Amber, Mono) set them all at once and Reset colours puts the
  theme's back. Glass opacity (10–100 %) lets the wallpaper show through glass
  and the selection pill, and Animations picks Comfy, Snappy or Off (Off is
  Reduce motion) ([Themes](docs/THEMES.md)).
- **Tabs Home: Backdrop and Minimal views, a Tools tab, pinned games.**
  Backdrop fills the screen with the selected game's screenshot (or its
  save-state picture) under its box art and name; Minimal shows big names only.
  An optional Tools tab lists the Start menu's screens and tools, and Grid
  selection ring turns the ring off. Pin to Home (game options, the game page's
  More sheet or X on the Tabs Home) makes a game a cover tile on the Carousel
  and Widgets Homes and puts it first on Main; Remove from recents and Add to
  collection… sit beside it ([The Tabs Home](docs/HOME_TABS.md)).
- **Home dashboard.** The Continue card shows where you left the game (its
  newest save-state picture), and two new widgets show your Most played game
  and This month's play time (it waits for the clock after a cold boot).
- **NextUI collections.** Lists in the card's `Collections` folder appear as
  JukaHub collections, changes are written back in NextUI's own format (lines
  for games not on the card are kept), new JukaHub collections appear in
  NextUI, and a file edited or deleted outside JukaHub is read again rather
  than overwritten (Settings → Games & emulators → Share collections with
  NextUI; [Collections](docs/COLLECTIONS.md)).
- **Device Sync.** Tools → Save sync between two paired handhelds now also
  carries save states (or leaves them out), BIOS files, collections, recents
  and play time and — off by default — the ROMs one device lacks, per console
  with the size shown first. BIOS and ROMs copy only what is missing, never
  replace anything, need the switch on on both handhelds, and ports or scripts
  never travel. Send my settings offers this device's look and controls to the
  other, which applies them only after Use them; Wi-Fi and accounts stay. An
  older JukaHub on the other side still syncs saves ([Device
  Sync](docs/DEVICE_SYNC.md)).
- **Game Boy link cable and the GBA wireless adapter over Wi-Fi.** X on a Game
  Boy or Game Boy Color game → Link cable lets two handhelds trade and battle
  with their own games (Pokémon Red with Blue) through Gambatte's network link:
  the host is found on the network or joined by address, and no Wi-Fi opens the
  hotspot. On a GBA game, Wireless adapter (Pokémon FireRed/LeafGreen/Emerald)
  starts a Union Room session with gpSP. Everything set for the session is put
  back afterwards ([Game library](docs/GAME_LIBRARY.md)).
- **Netplay for up to four players.** Nintendo 64, Dreamcast, arcade and
  multitap games take three guests, a guest picks player 2, 3 or 4 with
  Left/Right, and the host sees "Netplay: N of 4 players here". Netplay
  sessions sync the card before they start.
- **Audio and subtitle tracks in videos.** The video settings sheet picks a
  film's audio track and steps Subtitles through Off, the subtitle files beside
  it and its text tracks; both choices are remembered per file. `.ass`/`.ssa`
  and MicroDVD `.sub` files are read too (also `Film.en.ass`), and a film whose
  first subtitle track is a picture track now shows its first text track.
- **Lyrics inside songs and a radio directory.** Lyrics stored in MP3, FLAC and
  M4A tags show when there is no `.lrc` file, and untimed lyrics scroll with
  the song. A new Radio shelf in Music opens the free Radio Browser directory —
  top stations, your country's, by genre or by name — where A plays and X saves
  the station to your stations; the last lists stay readable offline, and Kid
  mode keeps it shut ([Music player](docs/MUSIC_PLAYER.md)).
- **Pictures, two panes and play records.** In the picture viewer X opens
  Rotate right (saved for PNG and JPEG), Rename and Delete. Files → Y → Two
  panes shows two folders side by side: L1/R1 pick a pane, and Copy / Move to
  other pane run in the background. Play history shows each game's play count
  and average session, and X deletes a game's play record after a question
  (saves are never touched).
- **Quick settings in games.** The in-game menu's new Quick settings card sets
  brightness, volume, mute, Wi-Fi, Bluetooth, stick lights, rumble and
  vibration (and the Music in game level while music plays) without leaving the
  game, shows CPU, temperature, memory and battery time left, and has a Power
  off that saves the game first ([Controls](docs/CONTROLS.md)).
- **Launch scripts and screenshots for scripts.** `.sh` files in
  `JukaHub/data/hooks/pre-launch` and `post-exit` (or a console's subfolder)
  run before and after every game and app with `JUKAHUB_*` details, 5 s each; a
  pre-launch script that exits with 3 stops the start. Scripts and paks can
  create `/tmp/jukahub/screenshot.request` to save a picture of the game; its
  path lands in `/tmp/jukahub/screenshot.last`
  ([Development](docs/DEVELOPMENT.md)).
- **Safe power off.** Before Restart and Power off, programs still using the SD
  card are asked to close and the card is synced and made read-only, all within
  3 s (Settings → Power & battery → Safe power off, on by default; [Session
  safety](docs/SESSION_SAFETY.md)).
- **Awake on USB, a stock-system check and a Developer page.** The idle screen
  and Auto sleep wait while a computer is connected for USB file transfer. If
  another firmware changed the handheld's own start-up files, JukaHub says so
  once and suggests reflashing the stock firmware if the handheld misbehaves
  (Settings → About → Stock system). Settings → Developer: Debug logging
  without a restart, Copy logs to the card and the Launch scripts switch.
- **Fast-forward speed, light N64 graphics and one button layout.** Settings →
  Emulation → Fast-forward speed caps RetroArch's fast forward at 2× to 8× or
  Unlimited, for every console or one. N64 graphics Light uses the Rice
  renderer in ParaLLEl N64 or a lighter Mupen64Plus-Next setup (Auto is Light
  on the Brick and Brick Pro; core options you set yourself stay). Settings →
  Controls → Button layout Nintendo or Xbox swaps confirm in JukaHub's menus
  and A/B and X/Y in RetroArch games (a console or game with its own Buttons
  keeps them), and Button hints show printed letters or roles
  ([Controls](docs/CONTROLS.md)).
- **Library layouts, in-game hotkeys, battery warnings, music, play time and
  .7z games** (2026-11-20). None of the entries below was compiled or tried on
  a device yet (FLOW-HW-1, GAMEKEYS-HW-1, BATTWARN-HW-1, MUSICX-HW-1,
  PLAYTIME-HW-1, SEVENZ-HW-1, OVERLAY-HW-1, COMPUPD-HW-1 in the backlog).
- **Cover Flow and Cube library views.** A console's games can be shown as
  Cover Flow — the highlighted cover large in the middle, the others turned in
  perspective on both sides over a reflection, sliding as you move — or as a
  Cube with the cover on its front face: Up/Down roll it to the previous or
  next game, Left/Right turn it to the previous or next console, B goes back.
  Library view in the game options (Start) picks them
  ([Game library](docs/GAME_LIBRARY.md)).
- **Games start sooner.** When a game stays highlighted for a moment (or sits
  on Home's Continue card), JukaHub reads RetroArch, the game's core and the
  ROM into memory at the lowest priority, so A waits less on the SD card. It
  stops when you move on, never runs during play or on a nearly empty battery,
  and Settings → Games & emulators → Warm up games before launch turns it off.
- **In-game hotkeys.** In a RetroArch game hold **F1** on a Brick to rewind and
  press **F2** for fast forward (L3 / R3 on handhelds with sticks), and press
  **Menu + Select** together to save and quit — the next start of the game
  carries on from there. The in-game menu lists them; Settings → Controls →
  Game hotkeys and Menu + Select: save and quit turn them off
  ([Controls](docs/CONTROLS.md)).
- **Battery warnings you can set.** Settings → Power → Low battery warning at
  (5–30%) moves the red battery icon, the lights' low-battery pulse and the
  plug-in message together; Blink from picks where the icon blinks; Show over
  games puts "Battery 9%" on RetroArch's message line as the charge drops; Save
  and suspend at picks 3–10% for saving the game and sleeping. Auto keeps
  today's levels ([Battery warnings](docs/BATTERY_WARNINGS.md)).
- **Menu + volume sets the brightness**, in games too, wherever the firmware
  does not already do it — JukaHub OS boots and cards without the stock key
  daemon (Settings → Display → Menu + volume sets brightness).
- **Battery level on the lights.** L2 + R2 together on Home flashes the charge
  on the stick lights for two seconds (more lights the fuller, green, amber or
  red), and Charging shows a filling bar shows the charge in green quarters
  while plugged in.
- **Tidy the Music folder.** After a library scan, loose songs move to
  Music/Singles and an album folder dropped straight into Music moves to
  Music/Artist/Album when its tags agree — never overwriting, never while a
  file is still copying, and favourites, playlists and saved places follow.
  Handheld only; Audio settings turns it off ([Music player](docs/MUSIC_PLAYER.md)).
- **Every audiobook keeps its own listening speed**; music keeps the global one.
- **Music from inside a game.** The in-game menu's Music row opens a
  now-playing panel (song, artist, previous / play-pause / next); music started
  there quiets the RetroArch game until it stops (Quiet the game while music
  plays).
- **Audio channels: Stereo, Mono, Mono left or Mono right** (Accessibility)
  replaces Mono audio. Mono left/right play in one ear on headphones,
  Bluetooth or a USB DAC (Mono on the speaker), and the choice now reaches the
  games JukaHub starts too.
- **Play time by game.** The Playtime screen's new By game tab ranks your games
  by time played today, this week, this month, this year or ever; X adds them
  up by console, and Game options shows a game's week and lifetime. Ever is
  complete from the start; the other periods count from this version on.
- **Games in .7z archives** get what .zip games get: Game Boy Color games are
  recognised and sorted, and covers, game details and the ROM manager use the
  game's own checksum from the archive (JukaHub's own small 7z reader).
- **More box art.** Games the libretro thumbnails lack get a front box from the
  LaunchBox Games Database automatically, and Choose cover has Search
  LaunchBox… to pick one by hand. Signing in to ScreenScraper (Settings →
  Games & emulators) adds covers plus year, genre, developer and synopsis by
  checksum, filling only what is empty; it needs developer credentials in the
  build (`JUKAHUB_SS_DEVID`, `JUKAHUB_SS_DEVPASSWORD`) ([Box art](docs/BOX_ART.md)).
- **Your own overlays.** Every console with games gets a folder in Overlays on
  the card; drop a screen-sized picture (or a RetroArch overlay) in it and that
  console's games show it. Settings → Emulation → Overlay picks between them,
  and pictures can be dropped from a computer over FTP or from the phone's
  Files card ([Looks](docs/LOOKS.md)).
- **Left stick as D-pad, per console**, on handhelds with sticks (Settings →
  Controls): on for consoles that never had a stick, off for N64, PSP and
  Dreamcast, and PlayStation games on the DualShock keep both sticks.
- **Phone Companion: updates, logs and folders that ask first.** When a new
  JukaHub is out on your update channel the page shows it with Install (not
  during a game, and on a low battery only on the charger) and the handheld
  says your phone started it; Download logs saves a zip of recent logs with
  passwords, Wi-Fi names, account names, e-mail, Bluetooth and LAN addresses
  masked; renaming or deleting a folder JukaHub needs (a console's folder,
  BIOS, Saves, Overlays, PortMaster) asks first and says what it is for
  ([USB and Wi-Fi file transfer](docs/USB_FILE_TRANSFER.md)).
- **Tidy the card at start** (Settings → About, on by default): once per start
  JukaHub removes the `._` files, .DS_Store, Thumbs.db and desktop.ini a Mac or
  PC leaves in your game and media folders, and a Mac's .Trashes,
  .Spotlight-V100 and .fseventsd at the top of the card.
- **Servers, sharing, widescreen, discs and look packs** (2026-11-20). None of
  the entries below was compiled or tried on a device yet (the `*-HW-1` checks
  in the backlog).
- **VNC server: see and control the handheld from any VNC app.** Tools →
  Network tools → VNC screen & control serves port 5900 to RealVNC Viewer,
  TigerVNC, macOS Screen Sharing, bVNC or AVNC on the same network: JukaHub's
  screens and, on the handheld, the game on screen. Keys behave like a USB
  keyboard (Enter is A, Esc is B, the arrows are the D-pad, typing goes into
  text fields) and in a game press the handheld's own buttons; the mouse
  drives JukaHub's pointer in mouse mode and copied text lands on JukaHub's
  clipboard. Only the 64×64 squares that changed are sent, ZRLE-compressed,
  and pictures are taken only while someone watches. Off by default, LAN and
  Tailscale addresses only, an 8-character password kept until New password,
  two viewers, and it stops itself like the file servers
  ([VNC and Telnet](docs/VNC.md)).
- **Telnet server.** Tools → Network tools → Telnet gives a computer on the
  same network the Terminal's shell (port 23 on the handheld) behind a
  password made for each start. The sheet says plainly that Telnet is not
  encrypted and SSH is the safe choice; three wrong tries end the connection
  and the window size follows the client.
- **SMB share: the SD card as a Windows or Mac network drive.** Tools →
  Network tools → SMB share serves the card as `\\IP\JukaHub` (Windows),
  `smb://IP/JukaHub` (Mac, Linux, phone file apps; QR code) and
  `\\DEVICENAME\JukaHub` through a small NetBIOS name service — no 50 MB file
  limit like Windows' WebDAV client. It is JukaHub's own SMB 2.1 server (no
  Samba program) with signing required, so Windows 11 24H2 connects: one
  account `jukahub` with a kept 12-character password, a read-only switch,
  wrong-password lockout, home-network and tailnet addresses only, off by
  default and off after 15 idle minutes like FTP/SFTP. Handheld only
  ([SMB share](docs/SMB_SHARE.md)).
- **Tailscale built in: reach the handheld from anywhere.** Smart Home's
  Tailscale row needed a tailscale program on PATH, which a TrimUI never has.
  Tools → Network tools → Tailscale (and Settings → Network) now installs
  Tailscale's official static build from pkgs.tailscale.com, checked against
  its published SHA-256, and runs it in userspace mode: no system changes,
  but SSH, FTP, the phone companion, VNC and the SMB share answer on the
  tailnet address from anywhere. Sign in by scanning a QR code with a phone or
  with an auth key; the screen lists your devices. It runs only while switched
  on, with Wi-Fi and above 15 % battery, stops with JukaHub, and the emulator
  updates pass keeps it current. On a PC the screen shows Tailscale for
  Windows' state ([Tailscale](docs/TAILSCALE.md)).
- **Syncthing beyond saves.** The Syncthing page has **Also sync** switches
  for Screenshots, Recordings, Music, Books and Roms (each its own folder,
  shared with every paired device like the saves; Roms warns about its size)
  and **Web GUI on this network**, which opens Syncthing's own page to the
  Wi-Fi with user `jukahub` and a password shown on screen so a PC can add any
  folder. Both are off by default; turning the web GUI off closes it behind a
  new unknown password ([Save Manager](docs/SAVE_MANAGER.md)).
- **PlayStation analog controller.**
  PlayStation games that read the sticks — Ape Escape, Spyro, Crash Team
  Racing, Gran Turismo, Metal Gear Solid and about 65 more — start with a
  DualShock plugged in, while digital-only games keep the standard pad.
  Settings → Controls → Game buttons for → PlayStation → Analog controller is
  Auto, Always or Never, and a game can pick DualShock or Standard pad in its
  options; Auto stays off on the Brick, which has no sticks. Set for one
  launch only, with the right device for PCSX-ReARMed, SwanStation and Beetle
  PSX.
- **Widescreen (16:9).** The Nintendo 64, PlayStation, Dreamcast and Mega
  Drive emulators that have a widescreen hack can draw 3D games across the
  Smart Pro's whole screen: per console in Settings → Emulation → Adjust for,
  or per game in its options; off by default because hacks can glitch at the
  edges. JukaHub sets the emulator's option and RetroArch's 16:9 picture for
  that launch only, logs when the emulator has no such mode (PCSX-ReARMed: use
  SwanStation or Beetle PSX) and hides it all on the 4:3 Brick
  ([Widescreen](docs/WIDESCREEN.md)).
- **CUE generator.** CD games ripped as a lone .bin, or as Redump's split
  "(Track 1).bin, (Track 2).bin …" sets, get a .cue sheet: the track mode is
  read from the image and split sets get one multi-track sheet with Redump's
  gaps, automatically when the library scans and on request from the ROM
  manager's "Make missing .cue files" and the Tools hub's "CUE generator".
  Existing sheets are never replaced.
- **Look packs, console icon packs and console names.** Experience packs now
  carry more in one press: a pack.json may name
  an icon pack, a console-name style, a menu colour, the startup sound, a
  preview and a boot logo (staged for Settings → Boot logo, written only after
  its own confirm); Undo takes all of it back. Packs are also read from the
  card's Themes/Packs folder, and three built-in packs (Super Famicom,
  Genesis, Pocket Tags) show it off. Settings → Theme & look gains **Console
  icons** (icon packs from Icons/<pack>/Emus, drawn in place of
  the vector glyphs) and **Console names**: International, North America
  (Genesis, TurboGrafx-16), Japan (Super Famicom, Mega-CD) or Short (MD, PCE,
  GBA), changing only what is shown ([Look packs](docs/LOOK_PACKS.md)).
- **Start on tab and a power-off picture.** The Tabs Home's **Start on** row
  opens on the last used tab or always the same one, and **Power-off screen**
  → Picture ends a power-off or restart on "See you soon", a clock-and-battery
  card or your own picture from Themes/Shutdown, held as the power goes; the
  shutdown never waits for it.
- **Achievement options, hotkeys, game translation, Kid mode, idle and
  lights** (2026-11-20). None of the eight entries below was compiled or tried
  on a device yet (the `*-HW-1` checks in the backlog).
- **RetroAchievements in games.** A new Settings → RetroAchievements page sets
  RetroArch's own achievement options: hardcore,
  leaderboards (on, trackers only, notifications only, off), challenge
  indicators, unlock sound, screenshot on unlock, encore, rich presence,
  badges and verbose messages. Every row starts on "RetroArch's own", which
  writes nothing, and a chosen value lasts only for JukaHub's launch (owner
  profile only). **Sign in for games** types RetroArch's login on the
  handheld; RetroArch swaps the password for its token at the first game and
  JukaHub keeps neither. Each unlock flashes the stick lights and a message
  after the game counts them. With hardcore chosen here, JukaHub no longer
  offers Load state, rewind, cheats or a power-on resume RetroArch would
  refuse ([RetroAchievements](docs/RETROACHIEVEMENTS.md)).
- **Custom RetroArch hotkeys and aspect ratio, per console and per game.**
  Settings → Controls sets RetroArch's hotkey button and its shortcuts (menu,
  quit, save/load state, slots, fast forward, rewind, screenshot, pause,
  restart, translate screen) for every console or one console, and a game's
  options sheet for that game alone. Button codes are copied from RetroArch's
  own player-1 bindings, nothing is written until a hotkey button exists,
  JukaHub's Select chords stay free and everything is undone when the game
  exits. Settings → Emulation adds Aspect ratio (Core provided, 4:3, 16:9,
  3:2, 10:9, 8:7, Square pixels, Full screen) and Hard GPU sync per console,
  with a per-game Aspect ratio row ([Game hotkeys](docs/GAME_HOTKEYS.md),
  [Looks](docs/LOOKS.md)).
- **Translate the game screen** (Settings → Voice & AI → Translate games, off
  until turned on): RetroArch's AI service.
  In a RetroArch game, Select + L2 → Translate screen (or a RetroArch AI
  hotkey) sends the screen to JukaHub's own small service on 127.0.0.1, which
  asks Groq's vision model with your key for the on-screen text in your
  language and shows it like subtitles, or reads English aloud with the
  handheld's voice. A RetroArch-compatible service of your own (vgtranslate,
  ZTranslate) works too, picture mode included. Nothing is stored
  ([Game translation](docs/GAME_TRANSLATION.md)).
- **Kid mode and Kiosk mode** (Settings → Kid & Kiosk mode). Kiosk is
  play-only: games, favourites, the switcher,
  music and video work, and Settings shrinks to volume, brightness, Wi-Fi and
  Leave; stores and installs (also Xbox, PS5 and Steam helpers), Files, the
  Terminal, updates, profiles, Edit Home, search outside games and quitting to
  the stock menu are locked. Kid mode also shows only the games marked "For
  kids" (a game's page → More, per game or the whole console) everywhere games
  are listed, refuses the others at launch and hides the internet apps and web
  streams. A parent code set when it is turned on, or the Konami code, leaves;
  a forgotten code is removed by deleting `ui-mode.json` from a computer or
  starting with `JUKAHUB_UI_MODE=full` ([Kid mode](docs/KID_MODE.md)).
- **The FN switch, Silky lights and GPU boost.** On the Smart Pro, Settings →
  Power & battery → **FN switch does** turns the slide switch into Travel lock
  (as before), Mute, Lights off or Airplane mode; it works in games, and
  sliding it back restores exactly what it changed (the volume it muted from,
  your lights, only the radios that were on). The stick lights add a fast red
  pulse at 5% battery, an optional **Follow screen brightness** and a
  three-second rainbow when a RetroAchievement unlocks. **GPU boost in games**
  (Auto for N64, PSP, Dreamcast, DS, Saturn, PlayStation, PS2, GameCube/Wii
  and ports; Always; Off) runs the GPU at full speed for one launch with Xbox
  boost's plan and puts it back afterwards, even after a crash; the row only
  shows where the GPU has a frequency control
  ([Device tuning](docs/DEVICE_TUNING.md)).
- **Idle in games, idle hooks and the battery in games.** Settings → Power &
  battery → **Dim when idle in games** dims the screen after 5–30 minutes
  without input while a game runs (any button brings back the exact
  brightness), and **Then sleep or power off after** 5–60 more minutes either
  sleeps like the in-game menu's Sleep or saves and powers off like Power off,
  never on a charger or while music plays. Shell scripts in the data folder's
  `hooks/idle`, `hooks/extended` and `hooks/active` run on those changes, on
  the idle screen and around every sleep (not while the startup lock is up).
  **Battery in games** shows "Battery 54% · about 1 h 20 min left" on
  RetroArch's message line at 20/15/10/5% or every 15 or 5 minutes, optionally
  with the time ([Idle](docs/IDLE.md)).
- **Card check and a WebDAV share.** Tools → System → **Card check** (also in
  Settings → About) looks at the SD card that holds the games: partition and
  file system, the file system's own health flags read from the card (FAT32
  dirty/hard-error bits, exFAT VolumeDirty/MediaFailure) and the kernel's
  messages, a read of every file that lists the ones the card can no longer
  read, an optional fake card test that fills free space with checkable data
  and always removes it, and a read-only fsck where one exists; the report is
  saved as `card-check.txt` with repair steps for a computer
  ([Card check](docs/CARD_CHECK.md)). Settings → Network → **WebDAV share**,
  next to the SMB share, shares the card as a drive Windows Explorer maps,
  macOS Finder connects to and Linux file managers open, with a fresh key in
  the address and password at every start and FTP's folders, read-only
  program folders and automatic stop
  ([WebDAV share](docs/NETWORK_SHARE.md)).
- **Automatic collections and two game idle screens.** The Tabs Home's
  Collections tab now starts with folders that fill themselves (Last played,
  Most played, All games, Recently added and Never played by default; also
  Multiplayer and one per genre), each switched on or off in Settings → Games
  & emulators → **Automatic collections**; Recently added follows JukaHub's
  own library scans, never file dates or the clock. A game's page says how
  many times it was played. Settings → Power & battery → **Idle screen** adds
  **Game slideshow** (your games' pictures, drifting and crossfading, title
  and console only) and **Game videos** (random muted video snaps, offered
  when the card has some) ([Collections](docs/COLLECTIONS.md)).
- **Offline achievements, USB storage, keys, power settings and five more
  consoles** (2026-11-20). None of the entries below was compiled or tried on
  a device yet (RAOFF-HW-1, USBSTOR-HW-1, KEYPOWER-HW-1 and MORESYS-HW-1 in the
  backlog).
- **Offline RetroAchievements.** RetroArch games keep unlocking achievements
  with no internet (in Casual), and the unlocks are sent by themselves when the
  handheld is back online; a game works offline after one online play. JukaHub
  runs a small RetroAchievements relay on the handheld itself, only around
  RetroArch games. Settings → RetroAchievements → Offline achievements (on by
  default): send waiting unlocks now, cache your recent cartridge games for
  offline, or clear the cache. Right after waking, an achievement game waits
  up to 8 s for Wi-Fi so RetroArch can sign in (press Play again to start at
  once) — Wait for Wi-Fi before achievement games
  ([RetroAchievements](docs/RETROACHIEVEMENTS.md)).
- **USB storage mode.** Settings → Network → USB storage mode lends the SD card
  to a computer as a plain USB drive, like a card reader. JukaHub closes while
  the card is lent and comes back by itself when you eject the drive on the
  computer (or press a button). Smart Pro, Brick and Hammer on stock firmware;
  USB file transfer stays the everyday way
  ([USB file transfer](docs/USB_FILE_TRANSFER.md)).
- **In-game menu entries, Start in and Hold Menu.** Settings → Controls →
  In-game menu entries hides the rows you never use (Resume game and Quit to
  JukaHub always stay). Settings → Power & battery → Start in opens the Game
  switcher, a random game or the last game played when the handheld is
  switched on (Home by default; a game Resume at power on brings back comes
  first; hold B to stay on Home). Settings → Controls → Hold Menu in a game
  keeps the screenshot, or saves the game and jumps to the Game switcher, or
  does nothing. Start → Games → **Play a random game** starts one
  in one press, and each game's play history is kept for the 500 most recent
  games (was 128).
- **Brick F1 / F2 keys, the Smart Pro S Home key and D-pad as stick.**
  Settings → Controls → F1 key / F2 key choose what the Brick's front keys do
  (Controller card and Quick Settings by default; also Screenshot, Game
  switcher, Search, Start menu, Sleep, Mute, Home or Nothing). On the Smart
  Pro S, Home key / Home key in a game make a tap on the top Home key go Home,
  open Quick Settings or the game switcher, and in a game open the in-game
  menu, RetroArch's own menu or the game switcher (the rows show once the key
  is found; `homeKeyCode` overrides its code). N64, PSP and Dreamcast games on
  a Brick now walk with the D-pad, which pushes the left stick (Settings →
  Controls, under Turbo, per console). Settings → Emulation → Threaded video:
  Auto turns it off on the Smart Pro S for smoother frame pacing.
- **Power, battery and lights.** Settings → Power & battery → Ask before power
  off: turn it off and the Power tile on Home and holding the power button
  switch the handheld off at once (a RetroArch game is still saved first).
  Battery percentage shows the level in the top bar always, only at 20% or
  less, or never. Settings → Stick lights → Lights in games: off keeps the
  sticks dark while a game plays, except the low-battery, charging and
  standby lights.
- **Stock firmware, card space, emulator versions and Tasks.** Settings →
  About → Stock firmware shows the TrimUI firmware version, finds an update
  file on the card, says if it is in the wrong place, still zipped, renamed or
  for another model, checks the battery and card format, and shows the buttons
  that start TrimUI's updater on your model (JukaHub never flashes anything).
  Card space shows used, free and total space; Emulator versions lists
  RetroArch, PPSSPP and every core. Tasks resets RetroArch's settings, core
  options, overrides or PPSSPP's settings (files go to a reset trash, never
  deleted) and gathers Clear cache, Rebuild library index, Library doctor and
  Report a bug ([Recovery](docs/RECOVERY.md), [Devices](docs/DEVICES.md)).
- **New consoles and a smarter name sort.** NAOMI and Atomiswave
  (Roms/NAOMI, Roms/ATOMISWAVE — RetroArch's Flycast core with naomi.zip /
  awbios.zip), Nintendo DSi (Roms/NDSI — melonDS DS in DSi mode with your own
  DSi's BIOS, firmware and NAND files), OpenBOR (Roms/OPENBOR, .pak) and Tandy
  CoCo (Roms/COCO). Get more cores fetches Flycast and melonDS DS; Game Ready
  names missing DSi files; OpenBOR and CoCo games say what to install. The
  library now sorts "The Legend of Zelda" under L, also for the L2/R2 letter
  jumps (Settings → Games & emulators → Ignore 'The' when sorting, on by
  default) ([Game library](docs/GAME_LIBRARY.md)).
- **PlayStation 2 on the TrimUI handhelds** (Juka, 2026-11-20: "Add support
  for PS2", with a report of ARMSX2 running light games at full speed on a
  Brick Hammer, then "Focus on getting it to work on Trimui Smart Pro...not on
  windows"). PS2 was "not supported" on every handheld; it now plays in
  RetroArch's ARMSX2 core (a PCSX2 fork with ARM64 recompilers) on every
  TrimUI model, on by default (**Settings → Games & emulators → PlayStation
  2**). A game's first start downloads ARMSX2's own Linux arm64 libretro core
  from its GitHub releases (the newest stable release that has one, else the
  newest nightly; the archive checked against GitHub's SHA-256, the core
  through the usual load check, on JukaHub's glibc 2.39 since it needs a newer
  glibc than the firmware's) and the matching `resources` folder the core
  cannot boot without (`pcsx2/resources` in RetroArch's system folder),
  copies the player's PS2 BIOS there from wherever it is (the BIOS folders,
  `BIOS/PS2`, the game's folder; checked the way PCSX2 checks one), then
  plays. With no BIOS anywhere the start says where to put one. The core's
  first start gets OpenGL ES on the PowerVR models (ARMSX2 defaults to Vulkan,
  which their firmware lacks) and skips the BIOS intro; a start whose renderer
  could not draw moves it to the next one (OpenGL ES, then Software) and the
  card says so. PS2 games get Xbox boost. **Settings → PS2 emulator** and the
  Emulators page's PS2 tile install ARMSX2 or update it to the newest release.
  `Roms/PS2` lists ISO, CHD, CSO, ZSO, CUE, M3U and ELF (not a stray `.bin`,
  which is usually the BIOS). Windows keeps PCSX2. Untested on a device
  (PS2-HW-1). docs/GAME_LIBRARY.md, "PlayStation 2 on the handheld".
  (`player/ps2.go`, `player/ps2_setup.go`, `player/ps2_video.go`)
- **Ports without PortMaster's app** (Juka, 2026-11-20: "Can we try to bypass
  PortMaster? … download required files directly via PortMaster API … launch
  the apps directly without needing to use PortMaster?"). Ports already started
  from their own scripts; now JukaHub also installs and updates them itself,
  with no Python and without opening PortMaster's menu. The Ports Store, the
  Ports panel's catalogue, **Update** and the Minecraft screen download the
  port from PortMaster's catalogue, check its MD5 and unpack it the way
  PortMaster's `harbourmaster install` does: the zip is checked first (a path
  that could land outside its folder, PortMaster's own update, or more than
  100,000 files or 16 GB refuse it before anything is written); folders and
  scripts go where PortMaster puts them on this card (its `HM_PORTS_DIR` /
  `HM_SCRIPTS_DIR`, else where installed ports are, else `Data/ports`); each
  file is written beside its target and renamed over it, and saves or game
  files the zip does not have stay; `port.json` gets harbourmaster's `status`
  block so PortMaster, **Update available** and **Uninstall** see the port as
  PortMaster installed it. Each runtime a port declares is downloaded from
  where the catalogue lists it and checked against its MD5 — at install, and
  before the port starts if it is still missing (its script would otherwise
  call harbourmaster, which needs Python). Port zips in PortMaster's
  `autoinstall` folder are installed the same way when the Ports panel or the
  Ports Store opens, and **Y → Update PortMaster** replaces PortMaster's
  self-update. Only a zip JukaHub does not install itself goes to PortMaster's
  installer or app; `portsStoreInstaller` (`auto`, `jukahub`, `harbourmaster`,
  `portmaster`) picks the route. See
  [Without PortMaster's app](docs/PORTMASTER.md#without-portmasters-app)
  ([player/ports_native_install.go](player/ports_native_install.go)).
- **PortMaster installs and updates itself** (Juka, 2026-11-20: "can we pretty
  much do everything including installing PortMaster? and auto updating it?").
  Opening the Ports panel or the Ports Store online on a card without
  PortMaster installs it by itself, and a card with ports but no PortMaster
  gets it from the daily Emulator updates pass. That pass now keeps PortMaster
  current too, under the same setting and rules (Install automatically by
  default; never over a game or port; on the charger or when idle): it compares
  PortMaster's `version` file with its latest release and writes the new
  package over `Apps/PortMaster`, `version` last, keeping ports, runtimes and
  settings (a release whose tag and `version` file are written differently is
  fetched at most once). Repair and Update PortMaster also write `version` last
  now, and after an install JukaHub prefetches only what ports need — Python
  waits until PortMaster's own menu is opened
  ([player/portmaster_autoupdate.go](player/portmaster_autoupdate.go)).
- **Tested on this device** (Juka, 2026-11-19: "Show whether a game, port,
  emulator or app has actually been tested for launch, controls, sound, sleep
  and return on the user's model. Include the tested version and any known
  issue"). JukaHub now notes, per model, what it saw work: **Starts** (still
  running after 20 s; a quick exit or crash is noted with its reason),
  **Controls** (15 presses in a minute or more), **Sound** (the game's own audio
  stream seen playing on the handheld), **Sleep and wake** (still running 10 s
  after a real suspend) and **Quits back** (JukaHub came back with nothing left
  running; Select + Start held, a Quit that needed a kill, leftover programs or
  a crash count against it). Sound and controls are never marked as problems
  automatically; JukaHub's own stops never count. Each result carries the
  JukaHub release, firmware and emulator or core it was seen with, and a
  game's passes also go to its emulator. A game's page shows **Verified on
  Smart Pro**, **3 of 5 checks on Smart Pro** or **Known issue**; More →
  **Tested on this device** lists the five checks with where each came from,
  lets the player mark them, describe a problem or forget the results; and
  Settings → About → **Tested on this device** lists games, ports, apps and
  emulators, with **Export results** for sharing (another device of the same
  model reads it from `data/verified/import`; maintainers can ship results in
  `player/verified_seed.json`, empty for now). Results stay with the model, so
  a card moved to another handheld starts fresh there. Kept in
  `data/verified/verified.json`. See
  [Pick up and play](docs/PICK_UP_AND_PLAY.md).
- **Your progress is safe — and JukaHub shows it** (2026-11-19: "Save safely
  before sleep or low battery … Show the last successful save"). Every save of
  a library game is read back after the emulator writes it (on the card,
  complete, readable; RetroArch states checked too), and the game's page shows
  the last good one: "Saved 4 min ago", "Saved before power off", or —
  honestly — "Not saved since it closed unexpectedly" after a crash or power cut
  with no save. More → **Saves** lists the last few in plain words. Waking a
  RetroArch game says "Progress safe: in-game save 3 min ago" on its message
  line, and a game resumed at power-on says "Back where you left off". Nothing
  asks the emulator to save before an ordinary sleep, so save slots are never
  overwritten by it. Kept in `data/save-ledger.json`.
- **A low battery shortens the sleep.** On battery at 25 % or less, a handheld
  put to sleep with a game running wakes itself within 2 h (30 min at 15 % or
  less) to save the game and power off, even with Power off after sleeping
  turned Off, so the game is not lost when the battery runs out asleep.
- **In-game saves reach the card every 10 seconds** in RetroArch games started
  from JukaHub (`autosave_interval`, only when you had none, put back after the
  game). The new `saveFlushSeconds` setting changes it; 0 leaves RetroArch's
  own setting alone.
- **Updates fix themselves** (2026-11-19: "Check that an updated JukaHub
  reaches a usable screen and accepts input. If it fails, restore the working
  version and explain what happened"). The launcher now watches an updated
  build's first starts: if it hangs before its first screen — even in Go
  package init, before any of its own code runs, as the 2026-11-19 release did
  — crashes, or its buttons don't respond, the previous build is put back
  within two minutes (a goroutine dump goes to `errors.txt` first), started in
  the same session, and an **Update undone** card explains what happened, with
  Send report. That release is not offered again until a newer one is out; the
  boot guard does not count the rescue as a failed start. Games, saves and
  settings are never touched. Protection starts with the update after the one
  that brings it.
- **Device check** (2026-11-19: a guided hands-on pass for the Smart Pro).
  Settings → About → **Device check** (also Tools → System) walks nine checks —
  buttons and sticks (through the Controller test), speaker, headphones, sleep
  and wake, Bluetooth, Wi-Fi, a USB DAC, a game's launch and return, and the
  update self-check — and records what JukaHub measured beside each answer: the
  buttons and sticks seen, the output and volume the test sound took, how long
  the handheld really slept and how long Wi-Fi and sound took to come back, how
  many Bluetooth devices answered (a count, never names), the Wi-Fi link and
  signal (never the network's name or an address), the game's run and its
  Tested on this device results. An unfinished check resumes where it stopped;
  **Save report** (Y) writes a Markdown section shaped for
  `docs/HARDWARE_VALIDATION.md` into the SD card's `JukaHub reports` folder —
  dates only, no times of day.
- **Covers for the arcade games** (Juka, 2026-11-19: "in Built-in games there
  is MineSweeper, Floppy, 2048, Pong, Snake... but they don't have images").
  Snake, Tetris, 2048, Pong, Minesweeper and Flappy now have covers drawn in
  each game's own colours (`branding/mkcovers.py`, embedded like JukaLand's).
  Every card on the Games screen shows its picture, with the name on a band
  along the bottom; Games → **Built-in games** in the library lists the six
  beside JukaLand, the Synth and Solitaire, and A there starts the game right
  after the screen change. The Games tab on the Tabs Home gives each one a
  tile with its cover too. The built-in games stay out of title matching: a
  handoff of "Tetris (World).gb", Party Mode's Guess the Game (their covers
  print the answer), Nearby's shared titles and the AI's game diagnose no
  longer pick them.
- **Open with and default apps** (Juka, 2026-11-19: "“Open with” and default
  apps would add a lot to JukaHub … Someone could select a PDF manual, choose
  KOReader, and make it the default"). Files → Y → **Open with…** lists every
  way to open the highlighted file: JukaHub's own viewers that handle it, Open
  with Windows on a PC, installed TrimUI apps that open the type (marked
  verified when the App Center's catalogue says how the file is passed and the
  app's launch script really passes it on, or declared by the app's own
  `config.json` as `"jukahub": {"opens": [".epub"]}`), "Get <App>" rows that
  lead to the App Center, and Another app…. A opens once, X makes it the
  default for that extension, Y forgets the default. With a default saved, A in
  Files opens the file there; games in console folders always play in the
  library's emulator, and a default app that left the card is passed over with
  one toast. Formats JukaHub cannot show (MOBI, AZW3, FB2, DjVu, CBZ/CBR …)
  open in the one verified app on the card that reads them. **Default apps…**
  (Files' actions, or search Home) reviews and resets the choices. The text
  page and the reader have Open with… in their menus, and a document the
  reader cannot show offers it on Y. Not yet checked on a TrimUI
  (OPENWITH-HW-1). docs/OPEN_WITH.md.
- **App Center** (Juka, 2026-11-19: "a verified TSP App Center: show which
  apps work on stock firmware or CrossMix, with the required dependencies;
  install and update them from the handheld, with a way back if an update
  fails"). Apps → App Center (or search Home) is an honest catalogue of
  TrimUI apps — KOReader, Ritder, CrossMix's Screen Recorder, Xbox Pad BT,
  Gamepad a PC and TrimUI Drop — with where each comes from, how it fares on
  the stock firmware and on CrossMix, what it needs (checked live: Bluetooth,
  a network, CrossMix tools), what it opens, and "Checked" only where the
  app's own source documents it. Get, Update, Roll back and Remove work on the
  handheld: the release is downloaded from the catalogue's own GitHub
  repository (size and published SHA-256 checked), unpacked safely, must hold
  one app folder, and is swapped in with the previous version kept for Roll
  back; the app's own settings move over, a failed update puts the old version
  back, and an install cut short by a power cut is finished or undone. Apps
  installed by hand are only changed when asked. Apps started from the App
  Center or Open with hand back cleanly: CPU settings are left to Auto
  performance or put back, the stick input daemon is restarted if the app
  stopped it, and a MainUI the app leaves behind is stopped. The Recordings
  page offers a screen recorder where JukaHub's own cannot record, and
  controller apps point to Controller Mode. Offline and on a PC the page
  browses only. Not yet checked on a TrimUI (APPC-HW-1). docs/APP_CENTER.md.
- **Interactive Terminal** (Juka, 2026-11-19: "a real interactive Terminal …
  would unlock SSH, `htop`, and text editors"). `shell` (bash or sh; cmd.exe on
  Windows) and full-screen programs typed in the Terminal — htop, top, vi/vim,
  nano, less, man, mc, tmux, watch, REPLs such as python3, and pipelines into
  a pager like `dmesg | less` — now run on a real terminal: a PTY on the
  handheld, ConPTY on Windows 10 and newer, drawn by JukaHub's own VT100/xterm
  emulator (256 and 24-bit colour, alternate screen, line drawing, wide
  characters, 1,000 lines of scrollback). On the pad: D-pad arrows, A Enter,
  B Esc (hold to leave), X Backspace, Y a keyboard that types straight into the
  program, L2 Tab, R2 Ctrl, Select special keys (Ctrl+C, Esc, F1–F12, `:wq` …),
  Start the session menu, L1/R1 page or scroll back. With a keyboard every key
  goes to the program; Ctrl+Shift+Q leaves and resumes, Ctrl+Shift+M opens the
  menu, Ctrl+Shift+V pastes. A password a program asks for (sudo, su, ssh)
  shows as dots on the on-screen keyboard. Leaving keeps the session running
  ("Start resumes …"); End session or quitting JukaHub stops it and everything
  it started. `ssh user@host` connects to another computer with the system's
  ssh, or a built-in client with password sign-in and trust-on-first-use host
  keys (`ssh forget host`); `ssh on|off|status` still runs the SFTP server. The
  Command Deck gains Interactive shell and Live process monitor. Not yet
  checked on a TrimUI (TERM-HW-1); closes backlog APP-3. docs/TERMINAL.md.
- **Light-gun mode** (Juka, 2026-11-19: "Light-gun mode — Aim with the right
  stick, fire with a shoulder button, and calibrate per game for supported
  arcade and console cores"). Gun games in RetroArch play with the pad as the
  gun: the right stick aims (the D-pad on a Brick or Hammer), R1 fires, L1
  reloads with a shot off the screen, R3 puts the crosshair back in the middle,
  and the game keeps Start, Select, the D-pad and the face buttons. A game's
  page → More → **Light gun** turns it on and tunes it per game — **Move** (the
  stick steers, speed 1–5) or **Point** (the stick's position is the spot in an
  aim area: the 4:3 picture, the whole screen or a custom box), R1/R2 to fire,
  L1/L2 to reload, a buzz per shot — with a practice target to try the
  settings. About a hundred known gun games (Duck Hunt, Super Scope 6, Time
  Crisis, Lethal Enforcers, Operation Wolf …) get it once by themselves when
  the whole title matches; games that mix gun and pad play (Die Hard Trilogy,
  Resident Evil Survivor, Super Mario Bros. + Duck Hunt) are left alone, and
  switching one off sticks. For each start JukaHub plugs the console's gun into
  the right port (NES Zapper, Super Scope / Justifier, Menacer, Light Phaser,
  GunCon, arcade guns in FCEUmm, Nestopia, Snes9x, Genesis Plus GX,
  PCSX-ReARMed, Beetle PSX, SwanStation, FinalBurn Neo and MAME 2003-Plus),
  turns the core's crosshair on and makes a mouse the game aims with; all of it
  is put back when RetroArch closes. On Windows the PC mouse is the gun. Not
  yet checked on a TrimUI (LIGHTGUN-HW-1). docs/LIGHT_GUN.md.
- **CHIP-8 player and Get CHIP-8 games** (Juka, 2026-11-19: "WASM-4 and CHIP-8
  players — Tiny fantasy-console games should run comfortably on the A133P. A
  tested launcher and homebrew shelf would complete the experience"). CHIP-8,
  SUPER-CHIP and XO-CHIP games play inside JukaHub on every TrimUI and on
  Windows, with no emulator to install: `.ch8`, `.c8`, `.sc8` and `.xo8` files
  in `Roms/CHIP8` are the CHIP-8 console. The picture is scaled by whole
  pixels, the pad follows Octo's convention (D-pad 5 7 8 9, A 6, B 4), holding
  Y shows a 4×4 keypad, and Start opens a menu with Speed, Mode (the machine's
  quirks), a per-game key map, sound and a quick save. **Start → Games → Get
  CHIP-8 games** lists John Earnest's public-domain chip8Archive and installs a
  game with the quirks, speed and colours it was written for, and its own
  screen as its cover. docs/CHIP8.md.
- **WASM-4 games and Get WASM-4 carts** (same request). WASM-4 is a built-in
  console now: `.wasm` carts in `Roms/WASM4` are its shelf, and a `.wasm`
  anywhere else is not a game (the Retro formats plugin no longer handles
  them). The first start installs the WASM-4 core once and then plays — on the
  handheld JukaHub's own `cores-aarch64` build (the cores workflow now builds
  `wasm4`), else the buildbot's nightly through the usual load check, on the
  private glibc when it needs it; on Windows the buildbot's DLL into the PC's
  RetroArch — and the square picture is scaled by whole pixels unless you
  picked a picture mode. **Get WASM-4 carts** (Start → Games, or More on any
  WASM-4 game) lists wasm4.org's carts with screenshots and authors, installs
  one with its screenshot as the cover, shows its page as a QR code, deletes
  only what it installed (saves stay) and works offline from the last list. Not
  yet run on a device (WASM4-HW-1). docs/WASM4.md.
- **Retro soundtrack player** (Juka, 2026-11-19: "Retro soundtrack player —
  Play NSF, SPC, GBS, VGM and SID music with game artwork, playlists and
  screen-off playback"). The music player plays game music: NES (NSF, NSFE),
  SNES (SPC), Game Boy (GBS), Sega (VGM, VGZ, GYM), PC Engine (HES), MSX (KSS),
  ZX Spectrum (AY), Atari 8-bit (SAP) and Commodore 64 (SID) tunes, plus MOD,
  XM, S3M and IT modules. A file holding a whole soundtrack opens as an album
  with one row per song, named from the file's own tags; Now Playing shows the
  console, "Song 3 of 24" and the game, with the game's cover from the Games
  library (else the folder's picture) in the player, on the lock screen and on
  the dock. Looping songs play for the length the file gives (else 2:30) and
  fade out; playlists, favorites, history, M3U, the screen-off mode and music
  under games work as for any song, and the Media index files a soundtrack
  under its game with the console as the artist. Console formats play through
  ffmpeg's libgme and modules through libopenmpt (an ffmpeg without them says
  so once); SID tunes play on JukaHub's own small C64 emulation (PSID; RSID is
  refused). CHIPTUNE-HW-1 checks the handheld's ffmpeg. docs/SOUNDTRACKS.md.
- **TV mode** (Juka, 2026-11-19: "Brick Pro TV mode — Use its USB-C video
  output with a larger interface and automatic external-controller setup").
  Settings → Display → **TV mode** is Auto, On or Off. On a Brick Pro, Auto
  watches the kernel's video-out status every two seconds and turns TV mode on
  while a TV is connected, with a toast each way; On shows the TV interface on
  any device. In TV mode text is larger (115–145%, your own Text size
  untouched), a USB or Bluetooth controller is player 1 in RetroArch games
  started from JukaHub (the handheld's buttons the next player, for that launch
  only), a TV without a controller points at Pair a controller, and an optional
  switch darkens the built-in screen while the TV is connected. A 16:9 screen
  at start gets the wide layout; one that changes shape later offers a restart.
  Drivers & hardware's Video out row says what the probe sees. Built without a
  Brick Pro on a TV: which file flips, mirroring and RetroArch's pad numbering
  are TVMODE-HW-1. docs/TV_MODE.md.
- **Game Boy Printer scrapbook** (Juka, 2026-11-19: "Game Boy Printer scrapbook
  — Collect pictures printed by supported emulators, then view or send them
  through the phone companion"). A Game Boy or Game Boy Color game's page has
  More → **Game Boy Printer** (at the top for games known to print: Pokémon
  Yellow/Gold/Silver/Crystal/Pinball/TCG, Game Boy Camera, Link's Awakening DX,
  Super Mario Bros. Deluxe and more). **Play with the printer** downloads
  DoubleCherryGB once — the only libretro core with an emulated printer — makes
  it that game's emulator, brings the battery save along when RetroArch keeps
  saves per core (a copy never overwrites a save changed on both sides, and
  keeps a backup), and starts the game; **Usual emulator** puts everything
  back. Pictures printed in the game land in the scrapbook when it ends ("2
  prints saved to the scrapbook"): a grid with a sharp big view and delete,
  also at **Tools → Printer scrapbook** for every game. The Phone Companion's
  new card lists the prints with Save, 2× and 4× downloads for paper, read-only
  behind the PIN; Y in the scrapbook shows its QR code. Where DoubleCherryGB
  writes its prints is not documented, so JukaHub compares the folders a core
  can write to before and after the game (GBPRINT-HW-1). docs/GB_PRINTER.md.
- **USB controller** (Juka, 2026-11-19: "TrimUI as a USB gamepad — Plug the
  handheld into a PC and use its controls as a controller. This needs a kernel
  check for USB HID gadget support before committing to it"). The USB
  controller engine behind Controller Mode's USB cable link (`usbpad*.go`)
  makes the USB-C port a **PC (Xbox 360 compatible)** controller — Microsoft's
  IDs over FunctionFS, which Windows' own driver, XInput games, Steam and
  Linux's xpad take, with the PC's rumble on the handheld's motor — or a
  **Generic HID gamepad** (16 buttons, hat, two sticks) for macOS, Android,
  Linux and DirectInput games, through the kernel's HID function when it has
  one and HID over FunctionFS when not. Its own screen, **Tools → USB
  controller** (also Settings → Network), first checks what the firmware's
  kernel offers and says so on screen and in the log, then works on its own
  with a live tester; hold Select + Start for 2 seconds to leave (the PC never
  sees that chord). USB file transfer pauses and comes back after, host mode
  can't be on at the same time, and the firmware's ADB gadget is given back on
  leaving, quitting or after a crash. Not verified on hardware (USBPAD-HW-1).
  docs/USB_GAMEPAD.md.
- **Controller Mode** (Juka, 2026-11-19: "let someone use their TrimUI as a
  controller for a PC or phone, then return to JukaHub in one press"). Tools →
  Controller Mode (also Apps, Start, Home search and Quick Settings) turns the
  handheld into a game controller through one of four links: a **USB cable**
  (an Xbox 360 controller for Windows and Steam, or a generic gamepad, through
  the USB controller engine), **Wi-Fi to a PC** with the free JukaPad receiver
  the handheld serves itself (`player/ctlmode/jukapad_receiver.py`: an Xbox 360
  or DualShock 4 pad on Windows through vgamepad, an Xbox-style uinput pad on
  Linux; paired once with a 6-digit code on both screens — X25519 with
  commit-then-reveal — then every packet authenticated, the PC releasing all
  buttons within a second of a lost link, rumble back to the handheld; `--tsp`
  for networks that block discovery), **DSU for PC emulators** (Dolphin, Cemu,
  the yuzu forks read `<handheld IP>:26760` with nothing installed), or a
  **Bluetooth gamepad (beta)** that phones and PCs pair with as a standard BLE
  HID gamepad (a small D-Bus client in `dbusmini.go` drives BlueZ; the code is
  confirmed on the handheld). Installed GamePad to PC and Bluetooth pad apps are
  listed too. While connected every press goes to the other side before any
  JukaHub screen sees it and a live picture shows it; **tap Menu** to come back
  (the link stays up, A resumes), **hold Menu** for Guide. Layouts — face
  buttons by position or label, D-pad as the left stick, stick dead zone,
  inverted right stick, Menu-for-Guide — are saved per PC, and the screen turns
  off while you play unless set to stay on. `ctlmode*.go`, `dbusmini.go`,
  docs/CONTROLLER_MODE.md.
- **Record the screen** from JukaHub: Quick Settings → **Record screen**, a
  game page's More → **Record this game**, or the in-game menu (Select + L2)
  records whatever is on the panel — JukaHub, RetroArch, ports and standalone
  emulators — as an MP4 (640 px, 30 fps, video only) in the Recordings folder,
  with a ● REC pill while it runs. It stops by itself at 30 minutes, on a
  nearly full card or a low battery, and a clip cut short still plays. The
  Recordings page also lists CrossMix's Screen Recorder clips. `screenrec*.go`,
  docs/RECORDINGS.md.
- **Open with KOReader**: with KOReader in the card's Apps folder (CrossMix
  ships it), comics (CBZ/CBR/CB7/CBT), DjVu, FB2, MOBI/AZW, XPS, CHM and Word
  files open in it from Files; the reader's Select menu has **Open in
  KOReader** (your page is saved first), a game manual's list has Y for the
  other reader, and Settings → Media & playback → **Open books with** can send
  every PDF and EPUB there (JukaHub's reader stays the default and the
  fallback). JukaHub comes back where you were when KOReader quits.
  `koreader.go`, `extapps.go`.
- **Send whole folders from the phone.** The Phone Companion's drop card has
  **Send a folder**, and folders can be dropped on the page: the handheld reads
  the folder's file list first and answers one place for all of it — books to
  `Books/<Folder>`, an album to `Music/<Folder>`, videos and pictures to their
  folders, a disc game or one console's games to `Roms/<console>/<Folder>`, a
  DOS or ScummVM game to its shelf, a folder named after a console merged into
  it — asks when unsure, and keeps anything with programs or scripts in
  `PhoneDrop`. Paths that would leave the folder are refused, each folder shows
  one progress line with Cancel, and the handheld says "Phone sent the folder
  Asimov (42 books)". `companion_dropdir.go`.

- **Move to a new SD card** (Juka, 2026-11-19: "a guided transfer for games,
  saves, BIOS files, ports, artwork and settings, with a check that the new
  card is complete before the old one is retired"). Settings → About and
  Tools → Your setup (handheld) copy everything on the card — games, saves,
  BIOS files, ports, artwork, emulators, JukaHub and its settings — onto a new
  card in a USB card reader. The plan shows each category with its size and
  checks room (in the new card's real block size), FAT32's 4 GB limit, the
  battery and running games, and never leaves anything out without asking.
  Every file is written under a temporary name, flushed and renamed, then read
  back from the new card (not from memory) and compared with the old card's
  SHA-256; a stop, a pulled reader or a flat battery resumes where it was. A
  failing old card does not stop the move: unreadable files are listed, and
  the old card is never called a complete backup while anything is missing.
  After the swap JukaHub checks the new card by itself and says when the old
  one can be reused. JukaHub OS system cards are detected. Files:
  `sdmove*.go`; docs/SD_CARD_MOVE.md; device check SDMOVE-HW-1.
- **Profiles for a shared handheld, and Guest** (same list: "each person gets
  their own saves, Continue list, achievements and preferences"). Settings →
  General → Profiles adds people; each gets their own RetroArch saves and
  states, Continue list, playtime, trophies, favourites and look (theme,
  wallpaper, Home layout, language, text size and accessibility). Switch from
  Start → Switch profile or the chip on the top bar, or turn on "Ask who is
  playing at start". Your existing data simply is your profile: nothing moves,
  and with no other profile nothing changes. Guest plays without touching your
  progress — no history, playtime or trophies, and its saves are wiped when the
  session ends. Standalone emulators that keep one save folder for everyone
  (PPSSPP, DraStic) say so before the first game instead of mixing saves
  silently; backups, Save Manager, reset and cloud sync stay the owner's.
  Files: `profiles_*.go`; docs/PROFILES.md; device check PROF-HW-1, follow-up
  PROF-2.
- **Never lose a session in standalone emulators** (same list). The 3% battery
  guard, Power off and Power off after sleeping now protect PPSSPP, DraStic,
  Flycast, Mednafen, DuckStation, Mupen64Plus, ports and Wine games too, not
  just RetroArch: the emulator is asked to quit cleanly (writing a save state
  on the way out where its own "save state on exit" option is on — JukaHub
  checks the file really appeared), then the card is synced before the suspend
  or power off. Emulators that load that state by themselves resume at the next
  power-on. At 10% a game whose emulator cannot save a state by itself gets one
  warning and a buzz: "save in the game now". Apps from the Apps screen are
  left alone. Settings → Power & battery → Protect standalone emulators.
  Files: `sessionsafe*.go`; docs/SESSION_SAFETY.md; device check SAFE-HW-1.
- **Spoken navigation** (same list: "read the focused button, its current value
  and what pressing A will do"). Settings → Accessibility → Narration reads
  whatever is focused — its name, value and what A does ("Vibration, on.
  Switch with A.") — across Settings, Home, the Start menu, the Games library,
  dialogs, the top bar and the on-screen keyboard, then pop-up messages. A new
  focus cuts the old speech off, a held direction reads only where it stops,
  and Juka's own answers always come first; it is silent while a game runs.
  Turn it on from anywhere without looking: hold L1 + R1 for three seconds
  (Ctrl+Alt+Z on a keyboard). It uses the offline voice (downloaded once if
  needed) or Windows' voice, never an online one, keeps the words it has said
  so it answers at once the next time, and speaks at 0.8×–2.5×. Files:
  `narrate*.go`; docs/ACCESSIBILITY.md; device check A11Y-9.
- **Big game cards open at once** (same list: "keep an index that updates as
  files change"). The Games library and Home keep an index of the card
  (`data/library-index.db`): the library shows straight away as you last saw
  it, and only folders that changed are read again. Games copied over USB, FTP
  or from your phone appear by themselves a few seconds after the copy ends,
  your selected game stays selected, and a game's folder is checked again when
  it quits. A damaged index is rebuilt by itself. Game fingerprints (CRC32, up
  to 64 MB) are worked out slowly in the background for faster game details;
  Settings → Games & emulators has **Rebuild library index** and a switch for
  the fingerprints. Files: `libindex*.go`, `romsetup.go`; docs/GAME_LIBRARY.md;
  device check LIB-HW-1.
- **Games on a home network drive** (same list: "browse a NAS from the library
  and download a game to the card when needed, with a clear offline
  indicator"). Settings → Network → Network drives adds a NAS or PC share —
  SMB (found on the Wi-Fi by itself), WebDAV or SFTP, and on a PC any
  `\\server\share` folder; passwords are kept encrypted like mail passwords.
  Its games appear on their consoles' shelves in the Games library with a
  network badge; **Download and play** copies one to the card (multi-disc and
  folder games as one unit, a stopped copy resumes), and More → **Remove
  downloaded copy** frees the space again while the saves stay. A drive that
  does not answer dims its games and marks them Offline. SMB uses JukaHub's own
  read-only SMB2/3 client (no mounting): user or guest sign-in with NTLMv2,
  signed messages, SMB 3.1.1 encryption and the server's share list. Files:
  `nas*.go`, `smb2*.go`; docs/NETWORK_DRIVE.md; device checks NAS-HW-1 and
  SMB-HW-1.
- **Voice tab in Chat: a free voice room, one click, nothing to sign up for**
  (Juka, 2026-11-19: "is there a free option that we can add to chat as a
  tab … that allows with 1 click to connect to this voice channel … do all the
  setup so I don't have to sign up"). Chat's tab strip ends in **Voice**: a
  click joins the community's live voice room, and L1 / R1 step onto it like
  any tab with Join selected (passing by never opens the microphone). Calls run
  on Mumble, the free voice chat for gamers, through a public server that
  welcomes guests (GameMania's `mumble.gamemania.org:42069`, room
  "Private User-Channels/JukaHub", opened on demand and removed when empty) —
  no account, no relay to host. Private Voice Channels from People now open a
  random-named room there too, so they work out of the box; the self-hosted
  relay stays as an option. One TLS connection carries control and Opus voice
  (pure-Go `github.com/thesyncim/gopus`, about 20 kbit/s instead of the
  relay's 256), a voice gate sends sound only while you speak and resists the
  speaker's echo, the Voice page shows who talks and who is muted (Y mutes),
  and **Join from a phone or PC** shows a QR code of the `mumble://` address
  that Mumla and Mumble open. Like Mumble's apps, each device makes one
  self-signed certificate (servers need it before a guest may open a room).
  Server and room are settings (`voiceServer`, `voiceRoom`); an invitation for
  another server is refused. Stepping onto the Voice tab and back keeps Chat's
  pictures loaded. Run `player\update-deps.bat` once for the new
  module. Files: `chatvoice_mumble*.go`, `chatvoice_opus.go`,
  `chatvoice_scene.go`; docs/VOICE_CHANNEL.md; device check VOICE-HW-1.
- **Everyday apps: Tasks, Voice Memos, Paint, Stopwatch, Solitaire and Create
  ZIP** (Juka, 2026-11-19: "the small OS apps I'd add next … my first three
  would be Paint, Voice Memos and Solitaire"). A new **Everyday apps** section
  in Tools gathers them with Notes, the Calculator, Alarms and Pomodoro; they
  are on the Apps screen, in the Start menu and in Home search too. Notes and
  Pomodoro had no way in on a stock config before; now they do. Every control
  has a keyboard key and a mouse works. docs/EVERYDAY_APPS.md; device checks
  VMEMO-HW-1, PAINT-HW-1, TASKS-HW-1 and SWZIP-HW-1.
  - **Paint** draws pixel art (16×16 to 128×128, or a screen-sized sketch) and
    marks up screenshots: the D-pad moves one pixel at a time (hold to speed
    up), the left stick glides, A draws, and the Line, Rectangle, Circle and
    Arrow tools circle or point at things. Undo and redo are on L1/R1, zoom on
    L2/R2, tools on X and PICO-8's palette on Y; on a screenshot the eraser
    brings the original back. Select opens File: save a PNG to
    `Pictures/Paint` (the screenshot itself is never changed), use it as the
    wallpaper, or as a game's cover — pixel art is enlarged without blur.
    **Start → Mark up** in the picture viewer opens the picture in Paint. Work
    in progress is autosaved and offered under Continue.
  - **Voice Memos** records 16 kHz WAVs into the card's `VoiceMemos` folder
    (also shared over FTP and in the Phone Companion's Files card) with a live
    level and waveform, pause and resume, then lets you name the memo. Memos
    play back with ±5-second seeking; the X menu renames, deletes, or sends one
    to your phone as a QR code that downloads it through the Phone Companion.
    A recording streams to the card, so a crash or a flat battery leaves a
    "Recovered memo"; music and video pause while a memo records or plays. The
    first visit runs a **Microphone test** that names the device it opened,
    records a 3-second sample, plays it back and says plainly whether the
    microphone works, hears nothing or is missing.
  - **Solitaire** is Klondike among the built-in games (Games screen, Games
    library, Home Tabs): draw one or three, A picks up and puts down, Y is a
    smart move, X draws from anywhere, L1/R1 undo and redo back to the deal.
    Safe cards fly to the foundations by themselves, the game finishes itself
    once every card is face up, and a win ends in the bouncing-card cascade
    with your time, moves and streak. The game and its undo history are saved
    after every move, so you can leave at any moment and resume exactly. The
    Games screen fits a third row of cards. FreeCell is backlog GAME-23.
  - **Tasks & reminders** keeps checklists (Inbox and Shopping to start, a
    Today view across lists). A task can have a day and a time, a reminder
    (toast, bell entry and a RetroArch message during a game, after the clock
    is trusted), a one-time alarm with its name in Alarms, and a daily or
    weekly repeat. Tasks due on a day show in the clock's calendar, and the
    Phone Companion has a **Tasks** card for typing tasks on the phone. Backup
    & restore carries `tasks.json`.
  - **Stopwatch** has a lap stopwatch and a countdown with presets; both keep
    running anywhere in JukaHub and in games, on a clock that network time
    cannot make jump. The countdown chimes and files a notification at zero,
    and during a RetroArch game it says so (with 5 min, 1 min and 10 s
    warnings) on RetroArch's own message line. A started clock gets a row in
    the in-game menu (A start/pause, Right lap) and a Quick Settings button
    that shows its time.
  - **Create ZIP** in Files (Y → Create ZIP) bundles the marked files and
    folders into a `.zip` beside them, in the background with its progress on
    the row (press it twice to stop), after a free-space check; links are
    never followed, dates are kept, and pictures, music, video and archives
    are stored as they are while the rest is compressed quickly.
  - The on-screen keyboard has one prompt every app's "name it" field uses
    (`osPromptOpen` in osapps.go): it acts on Done only, and B drops the text.
- **Party Mode: Guess the Game** (Juka, 2026-11-19: "a signature TrimUI
  feature"). **Tools → Party Mode** makes the handheld a game-show host:
  friends scan its QR code and play from their phone's browser — no app, no
  account, no internet. Each round shows a small crop of a cover from your own
  library that zooms out in five steps (500 down to 100 points); a phone buzzes,
  the reveal pauses and the buzzer picks among four titles (decoys from the same
  console). A wrong pick costs 100 and sits that player out for the round. The
  handheld shows the round, the cover and the scoreboard, then a winner screen
  with a podium and confetti; phones show the picture too, with sounds and
  vibration. The handheld can take a seat itself (**Y**). With the hotspot on,
  the lobby shows a second QR code to join the handheld's Wi-Fi, so a party
  needs nothing else. The server (port 8319) runs only while the screen is open
  and serves only the game; Security & trust lists it. docs/PARTY_MODE.md;
  device check PARTY-HW-1. Ghost Challenges and Pocket Studio are in the
  backlog's ideas list.
- **Game Ready** (Juka, 2026-11-19: the "smarter library" list). Before a
  library game starts, JukaHub checks what it needs: the game file (empty,
  damaged, a `.cue`/`.gdi`/`.m3u` whose files are missing), the emulator and
  the core its launch script names, and the BIOS that core needs in the folder
  this RetroArch really reads. A game that cannot start shows "*Game* isn't
  ready yet" with the file names and the folder to copy them into, instead of a
  black screen: **A** fixes what it can (Open BIOS Doctor, Get cores, the
  Emulators page on the right console, Remove missing players for Local play),
  **Y** checks again and starts the game once it is fixed, **X** plays anyway
  (twice turns the card off for that game). Things that only may matter never
  stop a game, and the resume at power-on is never stopped. The game page shows
  a chip (Ready, Needs BIOS, Missing disc file, No emulator…) and **More →
  Ready check** the full report. docs/GAME_LIBRARY.md, "Game Ready".
- **More on a game's page.** The page has a **More** button (keyboard **M**)
  for what belongs to one title — Ready check, Manual & guide, Controls, DOS
  settings, Saves on another device, Game Lab — and chips under the rating with
  the short answers. docs/GAME_LIBRARY.md, "More on a game's page".
- **ScummVM and DOS shelves.** A ScummVM adventure or a DOS game kept as a
  folder is one game on its shelf, titled after its folder. JukaHub writes the
  missing `.scummvm` file for the adventures it recognises (the free classics,
  the LucasArts and Sierra adventures, Broken Sword, Simon the Sorcerer,
  Discworld, Kyrandia, Myst — never over a file of yours) and names the folders
  it could not. ScummVM games play with a ready-made pointer layout (D-pad or
  stick, A/B clicks, a slow pointer on L1). A DOS folder starts with its own
  program (never SETUP or INSTALL); **More → DOS settings** picks another and
  sets the PC speed, graphics chip, memory and mouse speed for that game, for
  each start only. DOSBox Pure's on-screen keyboard is on **Select** on every
  TrimUI, the Brick included. Files' **Y → Add to DOS games** copies a game
  folder or `.zip`/`.dosz` into `Roms/DOS`. Each game shows its controls once,
  before its first start. docs/GAME_LIBRARY.md, "ScummVM and DOS shelves".
- **Per-game control layouts.** **More → Controls** (or Start on a game →
  **Controls layout**) picks a named layout with its button chart: **Arcade
  6-button**, **Arcade 4-button (Neo Geo)**, **DS stylus** (right stick, or the
  D-pad on a Brick, moves the stylus; R2 taps; L2 swaps the screens) and the
  ScummVM and DOS layouts — for this game (A) or every game of the console (X).
  It goes into the same one-launch remap file as Buttons (your own RetroArch
  remap still wins), with the DS cores' touch option for that start. Your own
  layouts are JSON files in `control-layouts/`. docs/CONTROLS.md, "Control
  layouts".
- **Game manuals.** **More → Manual & guide** lists the game's controls and
  every manual and guide found for it — next to the ROM, in the console's
  manuals folder, in Batocera/RetroBat gamelists, ES-DE and LaunchBox media
  folders, or a folder of scanned pages — and opens them in the Reader, the
  text viewer, the picture viewer or the Browser, coming back to the game's
  page. Add your own file (Files → **Y → Use as this game's manual**) or a guide
  link. In a game, the in-game menu (Select + L2) has **Manual** with the
  controls and text guides. docs/GAME_LIBRARY.md, "Game manuals".
- **Game Lab.** **More → Game Lab** plays a game in two setups — two
  emulators or cores, or one at two CPU settings — for one launch each and
  compares the frames it really showed, the sound glitches RetroArch logged and
  the battery's draw (on the handheld), with a one-line verdict; **Use A/B**
  makes the winner the game's emulator and CPU setting. **Share** writes a
  compatibility profile; profiles in `game-lab/import` show up as Shared setups
  for the same game. Nothing is measured that was not measured.
  docs/GAME_LIBRARY.md, "Game Lab".
- **Save handoff.** **More → Saves on *device*** shows this game's saves here
  and on a paired JukaHub (PC ↔ handheld, or two handhelds) side by side and
  moves them with one press, **Send to** or **Get from**. When both changed,
  both versions are shown and you keep one; the other stays as a "(from
  *device*)" copy and in a Save Manager snapshot — nothing is deleted. Content
  and the last sync decide which is newer, never a wrong clock alone. A chip
  says "Saves: newer on Juka's PC" and a quiet hint after a session says when
  this device's are newer. docs/GAME_LIBRARY.md, "Save handoff".
- **TIC-80 and RPG Maker in Get Homebrew.** Two more shelves: TIC-80 carts
  (`.tic`) and RPG Maker 2000/2003 games played by EasyRPG, installed as a
  folder in `Roms/EASYRPG` from the game's zip (checked for `RPG_RT.ldb`, later
  RPG Maker versions and setup programs refused with the reason; saves kept on
  update and delete). A page that only plays in the browser says so. The
  handheld gets the cores from Get more cores. docs/HOMEBREW.md.
- **Nintendo 3DS on Windows with Azahar.** `Roms\3DS` is a 3DS shelf that
  starts games full screen in the player's own Azahar (installer, a portable
  folder or PATH); its tile opens the download page until JukaHub installs it.
  Handhelds say plainly that 3DS is not supported. docs/CONTROLS.md, "Azahar".
- **GameCube and Wii on the TrimUI handhelds** (Juka, 2026-11-19: "Improve
  Dolphin or Gamecube support on Trimui devices"). They were "not supported" on
  every handheld; the Smart Pro S now plays them in RetroArch's Dolphin core,
  and the A133P models (too slow for most games) can **Try anyway** in
  **Settings → Games & emulators → GameCube & Wii**. A game's first start
  installs the buildbot's Dolphin core and Dolphin's system files
  (`dolphin-emu/Sys`, only what is missing) and then plays; a core from before
  libretro/dolphin's fix for the freeze ten seconds into a game (23 October
  2026) is replaced first. The core's first start gets background shader
  compilation instead of a pause at every new effect, a start that RetroArch's
  video driver could not draw switches Dolphin's override between OpenGL ES and
  Vulkan for the next, and the games get Xbox boost (every core and the GPU at
  full speed, JukaHub's memory handed back). `Roms/GC` and `Roms/WII` list
  only the formats Dolphin opens (ISO, GCM, RVZ, GCZ, CISO, WIA, WBFS, M3U).
  docs/GAME_LIBRARY.md, "GameCube and Wii on the handheld".
- **Java programs** (Juka, 2026-11-19: "can we add Java or ability to run Java
  files into JukaHub?"). **Tools → Java** installs Eclipse Temurin — Java 25,
  21, 17, 11 or 8, as a JRE (about 50 MB) or a JDK (about 200 MB, for javac,
  jshell and `java Hello.java`) — from Adoptium's GitHub releases, checked
  against the published SHA-256 and started once before it is used; a Java the
  computer already has (JAVA_HOME, PATH, /usr/lib/jvm, the Windows registry)
  works too. A on a `.jar`, `.java` or `.class` in Files runs it: text programs
  in the Terminal, windowed ones (Swing, JavaFX, LWJGL) on their own on a PC,
  while the handheld says plainly that it has no desktop for them. The Terminal
  gains `java`, `javac`, `jshell`, `jar`, `javaw` and
  `java install|update|remove|status`, and a running program now reads what you
  type (Enter or Start sends the line, Ctrl+D ends input). A Java folder comes
  with a `Hello.java` sample. This Java cannot run Android apps (an APK's code
  needs Android itself); AndroidRT still plays native Android games.
  docs/JAVA.md.
- **J2ME phone games** (Juka, 2026-11-19: "add J2ME and both"). Old Java phone
  games (`.jar`, `.kjx`) in `Roms/J2ME` are a **J2ME** shelf in Games, played
  in RetroArch with FreeJ2ME-Plus. The first start sets everything up: Java
  (Tools → Java offers it), then on the handheld JukaHub's own aarch64 build of
  the FreeJ2ME-Plus core and its jar (a few MB, checked against the release's
  checksums). A `.jar` counts as a game only in a J2ME folder, so PortMaster,
  Minecraft and app jars are never listed. On a PC, a FreeJ2ME core and jar in
  RetroArch are used. The "RetroArch cores for TrimUI" workflow now also builds
  FreeJ2ME-Plus (core and jar from one commit) into `cores-aarch64`; run it once
  before the first J2ME game on the handheld. docs/J2ME.md.
- **Godot games on the handheld, through the TrimUI Smart Pro Godot port**
  (Juka, 2026-11-19: "add Godot... There is Godot Trimui Smart Pro port
  somewhere so use that"). Godot games (`.pck`) in `Roms/GODOT` are a **Godot**
  shelf in Games, started through the port's `Emus/GODOT/launch.sh` as the
  firmware's menu starts them; a picture beside a game is its cover, and the
  port's `.runtime`/`.gptk` side files are not listed. On TrimUI handhelds the
  Emulators page has one **Godot** tile: X (or A) downloads
  [ogregorio/trimui-tsp-godot-port](https://github.com/ogregorio/trimui-tsp-godot-port)
  from GitHub (Kiwi's `Emus/GODOT` as the fallback when its launcher stands
  alone), lays it over the card's `Emus/GODOT` (a port file you had edited is
  kept in `Emus/GODOT/.jukahub-replaced/<date>`, told apart by the checksums
  recorded at install), makes `Roms/GODOT` and copies in the port's own files
  there without replacing any; once installed A opens the Godot games and X
  updates. Each start logs the game's Godot version from its `.pck` header and
  the engines the port carries, and a game that ends at once gets a
  launch-report line for a file that is no Godot pack or a version the port has
  no engine for. Home search "Godot", `.pck` from Files and the phone drop, and
  Close all apps know Godot games too. The library now opens on a console's
  shelf once its scan lands (`openGameLibraryOnSystem`), which also fixes the
  Xbox tile's A landing on the console grid with "No Xbox games yet" on the
  first open after boot. Untested on a device (GODOT-HW-1). (`player/godot.go`,
  `romsetup.go`, `emulators.go`, `emulatorinstall.go`, `emusystems.go`,
  `game_library.go`, `gamediag.go`, `artwork_match.go`, `boxart_auto.go`,
  `files_open.go`, `companion_drop_route.go`, `homesearch_index.go`,
  `closeall.go`, `game_library_ui.go`, `hakuxpage.go`; docs/GODOT.md)
- **Xbox boost: the whole handheld for an Xbox game or stream** (Juka,
  2026-11-19: "Improve XBOX performance"). On TrimUI handhelds, a hakuX game
  or a GreenOvercast stream (X1, Cloud Gaming, the Ports row) now starts with
  every parked core online, the performance governor at the top clock even
  with Full speed while playing off (a global clock cap and the game's own CPU
  options still win) and Automatic CPU speed standing aside, a devfreq GPU on
  its performance governor, JukaHub's textures and caches emptied, its Go heap
  returned, the warm speech recognizer, media scan and link prefetch stopped
  and the page cache dropped, the game's process group at nice −5, "Hey Juka"
  closed for the game, and for streams Wi-Fi power saving off. Everything is
  put back (compare-and-restore) when the game exits, and a crash marker lets
  the next start undo what a crash left. Settings → Games & emulators → **Xbox
  boost** (`xboxBoost`, on by default) turns it off and shows the last
  session. Untested on a device (XBX-11). (`player/xboxboost.go`,
  `xboxboost_linux.go`, `xboxboost_other.go`, `game_library.go`, `ports.go`,
  `powerprofile.go`, `autoperf.go`, `wakeword.go`, `main.go`, `setmodel.go`;
  docs/DEVICE_TUNING.md, docs/CONFIGURATION.md, docs/CLOUD_GAMING.md,
  docs/GAME_LIBRARY.md)

- **hakuX installs itself on the Smart Pro and plays Xbox games from Games**
  (Juka, 2026-11-19: "Xbox haiku X should auto install (in games show as
  launcher for Xbox games)"). With Emulator updates on **Install
  automatically** (the default), the daily pass installs hakuX once on a Smart
  Pro whose card has no Xbox launcher: on the charger or after two idle
  minutes, never during a game, through the same verified install as X on the
  Xbox tile, and only when 256 MB stays free besides the download. The card
  remembers it (`data/hakux/auto-install.json`), so a hakuX you delete is not
  put back and an `Emus/XBOX` you made is never replaced; Notify me does not
  announce it, and the Brick Pro (untested) keeps it a press. In Games, A on an
  Xbox game before hakuX is there installs it at once and starts the game when
  it is in (if you started something else meanwhile, a toast says it is ready
  instead); a model without hakuX says why instead of "No emulator found". The
  Xbox shelf lists only `.iso`/`.xiso` images, the formats hakuX and xemu boot.
  Untested on a device (XBX-10). (`player/hakux_auto.go`,
  `emuautoupdate_hakux.go`, `hakux.go`, `hakuxpage.go`, `emupicker.go`,
  `romsetup.go`; docs/COMPATIBILITY.md, docs/GAME_LIBRARY.md)

- **Python and Perl arrive by themselves for the Audit toolkit** (Juka,
  2026-11-19). With Emulator updates on **Install automatically** (the
  default), the daily quiet-moment pass installs Ubuntu's Python 3, Perl and
  the Perl modules the toolkit's script tools need (TLS, XML::Writer, JSON)
  through Linux packages when one is missing, also on the first start after
  this update, never over a game or another install, and only with 400 MB free
  on the card; a failed attempt is tried again at a later pass. Where the
  firmware has its own Python, Ubuntu's is not added; Notify me and Off leave
  them alone. Untested on a device (AUDIT-HW-1).
  (`player/audit_interp_auto.go`; docs/TOOLBOX.md, docs/LINUX_PACKAGES.md)

- **Legible reading font downloads a readable face by itself** (Juka,
  2026-11-19). Settings → Accessibility → **Legible reading font** used only
  faces already installed, so a TrimUI said "no system face found, using
  Inter". Turning it on now fetches one through Display → Font's downloader:
  Atkinson Hyperlegible (about 110 KB) for the Latin-script languages, SIL's
  **Andika** (about 2.4 MB, with Cyrillic) for Russian, and the screen switches
  the moment it lands. Offline it waits and fetches once JukaHub is online; a
  face missing at start is fetched about 20 s in, and switching to Russian
  fetches Andika. A face that cannot draw the interface language is never used,
  so Russian shows no "…" labels. A new **Readable font** row (while the
  setting is on) picks Auto, Atkinson Hyperlegible, Andika or a readable face
  the device has: Left/Right step only through installed faces that can draw
  the language and never download; A opens the full list, where picking a
  missing face downloads it. A face removed in Display → Font is not fetched
  back until you turn the setting on again or pick it. Andika is also in
  Display → Font. Untested on a device (A11Y-8). (`player/legiblefont.go`,
  `legiblefont_get.go`, `uifont.go`, `a11y.go`, `a11y_settings.go`;
  docs/ACCESSIBILITY.md)

- **Memory is freed by itself so the offline AI starts** (Juka, 2026-11-19:
  "only 140 MB left on Juka and AI doesn't launch sometimes"). When the offline
  model or a speech recognizer would refuse for memory, JukaHub first makes
  room, cheapest first, and stops once there is enough: the Go heap, its own
  picture and byte caches, idle helpers the start does not need (a loaded Piper
  voice, the idle recognizer or model server, web-video link lookups, the media
  library scan) and the system's file cache. For a question you asked, with no
  game or app on screen, it may also close apps left running in the background
  that **Close all apps & games** would close (never JukaHub's own jobs, such
  as a Wine prefix set-up or a Ports Store install); a toast names them. "Hey
  Juka" and warm-ups only get the light steps, at most once a minute and never
  while a game is on screen. With less than 200 MB free on a 1 GB handheld and
  nothing playing, the light steps also run by themselves every few minutes,
  never closing an app. The log's `mem` lines say what was freed. Untested on a
  device (MEMSWAP-HW-1). (`player/memfree.go`, `adaptive.go`, `closeall.go`,
  `offline_ai.go`, `offline_ai_server.go`, `stt_local.go`, `stt_stream.go`;
  docs/LOCAL_AI.md)

- **Extra memory (swap) on 1 GB handhelds** (Juka, 2026-11-19). Settings →
  Games & emulators → **Extra memory (swap)**, on by default on a TrimUI with
  1.5 GB of RAM or less: two minutes after start, with no game on screen,
  JukaHub turns on compressed swap in RAM (zram, half the RAM up to 512 MB)
  when the kernel has it, otherwise a 256 MB `jukahub.swap` in its data folder
  (only with that plus 1 GB free; swappiness 10, so the card is written only
  under real pressure). A card whose file system refuses a swap file is
  remembered and not written again, and swap that is already on is left alone.
  Turning it off while the swapped-out pages would not fit in memory keeps it
  on until restart rather than risk JukaHub being killed. The AI's and speech's
  memory checks count a cautious part of the free swap; memory read-outs still
  show RAM only. Untested on a device (MEMSWAP-HW-1). (`player/memswap.go`,
  `memswap_linux.go`, `setmodel.go`; docs/CONFIGURATION.md, docs/LOCAL_AI.md)

- **RetroArch on JukaHub's glibc 2.39, so the newest cores install and run**
  (Juka, 2026-11-19). The libretro buildbot's aarch64 nightlies need a newer
  glibc than TrimUI's 2.33 (mGBA needed GLIBC_2.35), so the core updater
  refused them and RetroArch's own updater broke GB/GBC/GBA. RetroArch can now
  start on the private Ubuntu 24.04 runtime JukaHub already keeps for
  PortMaster and GreenOvercast (glibc 2.39, libstdc++ of GCC 14):
  Settings → Games & emulators → **RetroArch's C library** — **Automatic**
  (default: only games whose core needs it, and RetroArch's own menu once the
  runtime is installed), **JukaHub's glibc 2.39** (every RetroArch game) or
  **Firmware's only**. The game runs in a mount namespace of its own where a
  small wrapper is bind-mounted over `ra64.trimui`, so the firmware's
  `launch.sh` starts RetroArch through the private loader; the card's
  RetroArch, the firmware and everything outside the game are unchanged, and
  RetroArch still sees itself as `ra64.trimui` in its own folder. The load
  check reads the runtime's libraries first, so the daily pass installs the
  newest nightlies (builds refused only for glibc are fetched again), and the
  start-up core look no longer swaps them for old builds. The runtime is
  downloaded the first time a core needs it and the game then starts by
  itself; offline, games start on the firmware's glibc as before. Each
  RetroArch is probed first (its libraries listed and bound by the firmware's
  loader and through the namespace and private loader, `data/private-glibc/ra/probe.json`);
  one that fails, or a game that stops in the loader, stays on the firmware's
  glibc and its cores get builds that run there. Games started from the
  firmware's own menu keep the firmware's glibc. Untested on a device
  (RAGLIBC-HW-1). See docs/PRIVATE_GLIBC.md.
  (`player/ra_glibc.go`, `ra_glibc_linux.go`, `compat.go`, `corefix.go`,
  `emulatorupdates.go`, `game_library.go`, `netplay.go`, `raemu.go`,
  `gamelaunch_firmware.go`)

- **Internet radio on the idle screen when the card has no music** (Juka,
  2026-11-19). Play on the dock/idle screen used to end in "No music found".
  With no songs on the card it now plays music radio: up to three of the
  IPStream library's own music or radio channels first, then a random pick of
  popular stations from a public radio playlist on GitHub
  (junguler/m3u-radio-music-playlists, one calm genre chosen at random, or the
  iptv-org music channels when no genre list answers), then a few long-running
  built-in stations (SomaFM, Radio Paradise, Radio Swiss Jazz, FIP), so the
  first Play works before the list has downloaded. The list downloads in the
  background when the idle screen opens with nothing to play, and is kept for
  a week (`saverradio.json` in the data folder). A station that does not play
  is skipped for the next one on its own, even with repeat-one on, and one
  that goes quiet while playing moves on too (repeat-one reconnects it), since
  nobody may be awake to press Play, and each skip is silent (no click); R1
  picks another station by hand, and when the list runs out, Play tunes in to
  a fresh random set. The card says "Tuning in…"
  until the sound starts, then "Live radio", and a station named like
  "1.FM - Chillout" keeps its whole name. Offline, the toast says to connect
  to Wi-Fi. Not yet built. `player/saverradio.go`, `ambient.go`,
  `musicplayer.go`.

- **A look for every console, chosen for you** (Juka, 2026-11-18). Settings →
  Emulation now has a **Look** row per console next to the overlay: CRT, LCD
  grid, scanlines, sharp or smooth pixels, and SkyWalker541's **Pixel
  transparency** (PT), the Game Boy screen whose bright pixels let the backing
  show through, made for the TrimUI Brick. The new **Auto** choice gives the
  three Game Boys PT, the TV consoles a CRT and the other handhelds an LCD
  grid, and leaves PSP, N64, DS, Saturn, Dreamcast and Jaguar clean so they
  keep full speed. Auto is what everyone gets who never picked a look; it only
  uses shaders (never a slow CPU filter) and does nothing on a card without
  the files. A game can still choose its own look in its options, which wins
  over its console's, which wins over all consoles. **Download looks** fetches
  the PT, CRT and LCD shaders from libretro (with RetroArch's full shader pack
  as a fallback) and SkyWalker541's Brick overlays into RetroArch's folders,
  so the overlay list shows them; run it again to update, and a file you
  changed yourself is never replaced. **Tune look** lists a look's own
  settings (scanline strength, PT's grid or LCD dots, backing colour …) with
  Left/Right and a Reset, saved per console. **Crop overscan** per console
  cuts the border a TV hid, or keeps the whole picture. When PT slows a game
  down, the shader speed check now suggests the faster Grid effect. Not yet
  built. `player/shader_looks.go`, `shader_looks_tune.go`,
  `shader_looks_download.go`, `videofilter.go`, `ratuning.go`,
  `ratuning_settings.go`, `shader_speed.go`; docs/LOOKS.md.

- **Get Homebrew: new games from itch.io** (Juka, 2026-11-18). Start → Games →
  **Get Homebrew** (also on the Games tab, in Home's Store folder, on
  Packages' More stores strip, in Home search and the ROM manager) lists new
  homebrew from itch.io for ten consoles — Game Boy, Game Boy Color, Game Boy
  Advance, NES, SNES, Mega Drive, Master System, Game Gear, PC Engine and
  PICO-8 — in one list you can filter by console, sort (Popular, Newest, Top
  rated, Free, Yours) and search. Each game has a page with its cover, author,
  price and description. With your own itch.io API key, sent from the phone's
  companion page or typed on the handheld, free games and the ones you bought
  install straight onto their console's shelf with their cover; JukaHub checks
  them for updates when the store opens and once a day, and an update keeps
  the file name so saves still match. Delete removes the game and its cover,
  never its saves. A paid game you don't own shows a code to buy it on
  itch.io; nothing is downloaded around a payment. Lists are kept for 12 hours
  and still show offline. Not yet built. `player/homebrew_store.go`,
  `homebrew_store_itch.go`, `homebrew_store_install.go`,
  `homebrew_store_key.go`, `homebrew_store_images.go`, `homebrew_store_ui.go`;
  docs/HOMEBREW.md.

- **Game Boy Color games on the Game Boy shelf play in color** (Juka,
  2026-11-18). Many indie Game Boy Color games come as `.gb` files and end up
  under Game Boy. JukaHub now reads each cartridge's header and starts those
  games with your Game Boy Color emulator, so they play in color (a game's own
  emulator choice and NextUI paks still win). The first time the library finds
  some it says so once, and the ROM manager can move them to Game Boy Color
  together with their saves, save states, play time, covers, options, buttons
  and favourites. Nothing is overwritten: a game whose name is taken there
  stays where it is. Not yet built. `player/gbcsort.go`, `game_library.go`,
  `romm_extras.go`; docs/GAME_LIBRARY.md.

- **Multi-disc games become one game by themselves, with a Disc row**
  (Juka, 2026-11-18). Copy "Game (Disc 1)" and "Game (Disc 2)" files, or
  "Disc 1" / "Disc 2" folders, into your PlayStation, Saturn, Sega CD or PC
  Engine CD folder and the library makes them one game (a playlist) when it
  scans, with .chd, .cue, .pbp, .iso and .bin discs alike; your own playlists
  and cue sheets are never changed, and NextUI's own folders are left alone
  unless you ask. A multi-disc game's options have a **Disc** row ("Starts on
  disc 2 of 3") that RetroArch follows, and the in-game menu (hold Select +
  L2) has a **Disc** row to put in another disc while you play. Not yet
  built. `player/disctools.go`, `discpage.go`, `romsetup.go`,
  `game_options_ui.go`, `gamemenu.go`; docs/GAME_LIBRARY.md, docs/CONTROLS.md.

- **More in the ROM manager** (Juka, 2026-11-18). Under its summary, "More for
  your games": move Game Boy Color games out of Game Boy, combine multi-disc
  games now, and one press to your RomM server (games, saves and BIOS),
  Syncthing (saves kept the same on all your devices) and Get Homebrew; each
  says when it needs setting up first. Not yet built. `player/romm_extras.go`,
  `romm_ui.go`.

- **Game buttons and turbo** (Juka, 2026-11-18). RetroArch games can swap A
  and B, X and Y, or both, and make A or B fire by itself while you hold it, at
  a Fast, Normal or Slow speed. Set it per console in Settings → Controls →
  **Game buttons for**, or per game with the **Buttons** and **Turbo** rows of
  its options in the library. It applies to that start only and is gone when
  RetroArch closes, so games started from the firmware keep RetroArch's own
  settings, and a remap file you saved yourself in RetroArch is never
  replaced (the row says so). Resetting, renaming, moving or deleting a game
  takes its choice along or clears it. Not yet built.
  `player/racontrols.go`, `racontrols_remap.go`, `racontrols_turbo.go`,
  `racontrols_ui.go`, `game_options_ui.go`, `inmap_settings.go`;
  docs/CONTROLS.md.

- **Cheats in the in-game menu** (Juka, 2026-11-18). A RetroArch game started
  with cheats switched on in its options now has a **Cheats** row in the
  in-game menu (hold Select + L2): the game's whole list of Game Genie, Action
  Replay and GameShark codes from libretro's cheat database, with On/Off
  switches. Changes take effect as soon as the game carries on and are kept
  for its next start; if RetroArch cannot be reached, the list says they apply
  from the next start. Not yet built. `player/gamemenu_cheats.go`,
  `gamemenu.go`; docs/CONTROLS.md.

- **Letter scrubbing in the Games library** (Juka, 2026-11-18). On a console's
  page L2 and R2 jump to the previous and next letter (PgUp / PgDn on a
  keyboard, Right in the list and the cover column); holding a trigger keeps
  going, and the letter shows big over the games for a moment. The jump
  follows the sort you chose: decades for Year, stars for Rating, play-time
  bands, or months for Last played and Recently added. Not yet built.
  `player/game_library_scrub.go`, `game_library_ui.go`, `shoulders.go`,
  `navrepeat.go`; docs/GAME_LIBRARY.md.

- **Menu colour** (Juka, 2026-11-18). Settings → Theme & look → **Menu
  colour** changes only the highlight colour (focus rings, pills, bars) to one
  of ten colours — Sky, Mint, Lime, Amber, Coral, Rose, Violet, Indigo, Teal or
  White — while keeping the theme, and it stays when the theme changes on a
  schedule, with Battery Saver or from a pack. Theme's own brings the theme's
  colour back. Not yet built. `player/menucolour.go`, `design.go`;
  docs/THEMES.md.

- **JukaHub's own booter, and Home from the moment the Smart Pro powers on**
  (Juka, 2026-11-18: "maybe we can have our own booter that's faster"). The
  Smart Pro JukaHub OS card now starts with `jukaboot`, a small compiled
  process 1 in place of the start-up script: it mounts the card and starts
  JukaHub at once, then loads the kernel's drivers several at a time along
  their dependencies instead of one command after another, keeps a millisecond
  timeline of every step in `os.log`, and saves the kernel's own log to
  `boot-dmesg.txt` once Home is up. Anything it cannot do hands the boot to
  the old script, which stays on the card (`BOOTER=0` in `boot.env` picks it
  on purpose, `MODULES_PARALLEL=0` loads drivers one at a time). The card's
  bootloader logo becomes a full-screen JukaHub picture, and from then on
  JukaHub writes the saved Home over it at power-off, so the bootloader itself
  shows Home (Settings → JukaHub OS → **Show Home from power-on**; the startup
  lock or a boot animation puts the logo back). The image builder gains
  `--boot-profile` and `--initcall-blacklist` for trimming the kernel from a
  measured boot, and `--shell-init`. On stock firmware the boot hook is
  already the earliest place a card can start anything without changing the
  device; its log now splits each boot into the firmware's share and JukaHub's
  and keeps a copy of the firmware's start-up script. Not yet built or booted.
  `player/cmd/jukaboot/`, `bootframe_ubootlogo.go`,
  `bootframe_ubootlogo_linux.go`, `jukaos_settings.go`, `autostart.go`;
  `os/smartpro/build_image.py`; `.github/scripts/trimui-build.sh`, `Makefile`.

- **Select + R1 takes a screenshot in a game** (Juka, 2026-11-18). In any
  game, emulator or port JukaHub started on a TrimUI, pressing Select + R1
  together saves the screen as a PNG at once: the picture from the moment both
  buttons were down, no hold, into the same folder as Hold Menu's game shots
  (`game_<time>.png`). Each press is one picture (at most one a second), the
  motor buzzes when it is written and "While you were playing" counts it.
  Settings → Controls → **Select + R1: screenshot** (on by default) turns it
  off for a game that uses the pair; JukaHub's menus keep Select + R1 for the
  next control profile. `player/gameassist_linux.go`, `menushot.go`.

- **Boot prefetch.** On a cold start JukaHub records which parts of its
  program and libraries it touched before its first frame
  (`data/boot-prefetch.list`); at the next start a small loader asks the
  kernel for exactly those, in order and a little at a time, while the Go
  runtime is still starting, so the card reads them in long runs instead of
  one page fault at a time. The `startup:` line's `prefetch=` field shows what
  it read; `data/boot-prefetch.off` or `JUKAHUB_BOOT_PREFETCH=0` turns it off.
  `player/internal/bootprefetch`, `bootprefetch_record_linux.go`.

- **Home's first frame comes ready from the last start.** The labels and icons
  Home's first frame draws are kept in `data/first-frame.cache` and uploaded
  at the next start instead of being rasterised again (a theme, language,
  font, text-size, screen or build change just misses); the `startup:` line
  shows `labels=H/N icons=H/N` and `JUKAHUB_FRAME_CACHE=0` turns it off. Also
  off the start's critical path now: the stock firmware's scan of the whole
  MainUI program for the model (remembered per firmware in
  `data/device-probe.json`), the boot guard's card flushes (its record is
  still written before anything can crash), the second read of the Inter font,
  icon parsing and the old crash-report clean-up.
  `player/startup_framecache.go`, `startup_preload.go`,
  `device_probe_cache.go`, `bootguard.go`, `crashreport.go`.

- **JukaHub OS shows Home almost at once when it boots** (Juka, 2026-11-18:
  "can we get down to <2 seconds boot time"). On the SD-card JukaHub OS images
  (the Brick on plastron, the Smart Pro on its own kernel) JukaHub keeps a
  picture of Home whenever Home sits untouched, and the image's launcher puts
  it on the screen as soon as the card is mounted, before JukaHub has started;
  JukaHub's first frame then takes over. No picture is kept while a startup
  lock is set or when you picked a boot animation, and turning the lock on
  deletes it. Wi-Fi and Bluetooth start after JukaHub's first frame and come
  back the way they were switched in JukaHub (`boot.env`); the Smart Pro image
  boots with `quiet` and no bootloader delay and loads its radio drivers last.
  `boot.log` records U-Boot's own timings and when the launcher, the picture
  and JukaHub started, and Settings → JukaHub OS shows **Last boot**. A
  two-second boot comes from a bootloader and kernel on the card, not
  from an `.awimg`; JukaHub OS works the same way and still never writes the
  handheld's internal storage. Not yet boot-tested. `player/bootframe.go`,
  `bootframe_linux.go`, `jukaos_native.go`, `jukaos_settings.go`,
  `systemcontrol.go`, `bluetooth_bringup.go`, `bootlock.go`;
  `os/plastron/launch.sh`, `os/smartpro/` (`init`, `launch.sh`,
  `build_image.py`, `dump-board.sh`).

- **JukaHub reaches Home sooner, and the log says where the boot went.** On
  JukaHub OS the boot animation is off unless you pick one, so the device goes
  straight to Home, and update checks, box-art fetches, plugin scans and "Hey
  Juka" wait three seconds after Home appears. Everywhere, the design file
  loads while the screen starts, the card reads the fonts and settings ahead,
  the garbage collector waits for the first frame (8 s at most, under a memory
  cap), and a free-space read and an SSH random-number read no longer hold up
  the start. The `startup:` log line ends with `kernel→start` and
  `start→first-frame`, and JukaHub writes `/tmp/jukahub-first-frame` for the
  OS image's scripts. `player/startup.go`, `startup_linux.go`,
  `startup_config.go`, `bootstyles.go`, `perf.go`, `main.go`, `ssh_server.go`,
  `hardware.go`, `quicksettings.go`, `wakeword.go`.

- **Console look: Pixel names and Pixel cards.** The Games library's console
  buttons can now be the console's name in big pixel letters, or a
  cartridge-style card with the name on a pixel label, the system icon and the
  game count. Landing on a console, or coming back to the buttons from its
  page, makes its letters fly together square by square in about half a second
  (in place with Reduce motion or Battery Saver). **X** on the console buttons
  switches the look; it is also in a game's options and in Settings → Games &
  emulators, saved as `console_look` in `game-library.json`. Names with
  accents and `& ' ! : , ( )` now spell out instead of showing "?".
  `player/game_library_pixel.go`, `game_library_pixel_look.go`,
  `game_library_ui.go`, docs/GAME_LIBRARY.md.

- **Drop games, covers and music from any browser, and each file goes where it
  belongs.** The Phone Companion page opens with a **Send to the handheld**
  card: drop files on it (or anywhere on the page) or tap it to pick several
  on a phone. Games land in their console's folder in `Roms` (this card's
  layout, NextUI's included), discs and zips are recognised from their first
  bytes or their file list, and a `.chd` or `.7z` asks which console it is
  for; a picture named after a game becomes its cover; songs and videos go to
  Music and Videos; BIOS files to BIOS (`Bios/<TAG>` on NextUI); anything else
  to PhoneDrop. Each file shows where it is going and its progress, a file
  already there is only replaced after you confirm, and when the batch is done
  the handheld says "Phone sent 3 games, 1 cover and 12 songs" and the library
  rescans. Untick **Put each file where it belongs** for the old PhoneDrop
  behaviour. `player/companion_drop.go`, `companion_drop_route.go`,
  `companion_files.go`, `companion.go`; README.

- **Browser over USB** (Settings → Network, under USB file transfer, on by
  default). Where the firmware's kernel has a USB network function (RNDIS, ECM
  or NCM), the cable also carries a small network of its own: open
  http://10.73.73.1 on the computer, type the PIN and use the same page as
  over Wi-Fi. Windows and Linux use drivers they already have; a Mac is
  switched to ECM after 20 quiet seconds. The computer keeps its own internet
  (no router or DNS is handed out), USB file transfer always comes first (if
  the port cannot fit both, the line says so), and every change to the
  firmware's USB setup is put back on exit or after a crash. The stock TrimUI
  4.9 kernels checked so far have none of these functions: the row then says
  so and USB file transfer works as before. `player/usbnet.go`,
  `usbnet_dhcp.go`, `usbnet_linux.go`, `usbmtp.go`, `usbmtp_linux.go`;
  docs/USB_FILE_TRANSFER.md.

- **Rename file, Remove cover and Delete game** (2026-11-18). The game options
  sheet (**Start** / **O** on a console's page) manages the game itself, not
  only how the library shows it. **Rename file** renames the ROM on the card
  (on-screen keyboard, extension kept) together with everything named after
  it — same-name files beside it (a `.cue`'s `.bin`, with the sheet's lines
  rewritten), its pictures (`Imgs`, `.media`, `.res`, JukaHub's cover), its
  RetroArch saves, states, core override, options and remap, NextUI's saves —
  and moves JukaHub's records along: game options, cheats, recents,
  collections, Home's rows, the RomM link, the quick-resume marker, Name for
  covers and, when the title the file gives changes, playtime, the game page
  and its history. Nothing is overwritten (a taken name, or one another game
  already has, stops it before a file moves), a failure beside the ROM puts
  everything back, files another game of the same name shares are copied, and
  arcade sets keep their names. **Remove
  cover** shows the lettered tile for good: the fetch, gameplay pictures and a
  port's own picture skip the game (a marker in `data/game-art/.nocover/`),
  JukaHub's copies go to the reset trash and, when asked, the card's own
  `Imgs` picture too; the row then reads **Restore cover**. **Delete game…**
  takes the ROM, the tracks and discs its sheet lists, its same-name disc
  parts, patches and manuals and JukaHub's pictures — to the reset trash (the
  three newest are kept, restore.txt written as each file lands) or for good;
  saves and states stay. Both questions open on Cancel. **Display
  title** is now called **Rename title**
  ([Game library](docs/GAME_LIBRARY.md#renaming-and-deleting-a-game)). Not yet
  built.

- **Covers from gameplay** (2026-11-18). A game the box-art lookup finds no
  cover for — homebrew, a hack, a translation, an oddly named dump — no longer
  stays a lettered tile: two minutes into playing it (time it really runs, not
  paused under the in-game menu, an alarm or the sleep), JukaHub reads the
  screen and keeps the picture as the game's cover in the library, Continue
  and Home. Black bars around a game that does not fill the screen are
  trimmed; a black, white or flat frame is never kept, nor one read within
  30 s of **Menu** (the emulator's menu may be open) or while a library
  RetroArch says it is paused, nor — for the first four minutes — one that is
  not moving (a pause screen) or dark nearly all over (a loading line; games
  on black get theirs after four minutes); it tries again every 20 s of play
  up to eight minutes. On the handheld it reads the framebuffer the way
  Hold Menu: screenshot does; on Windows the game's own window, only while it
  is in front. The picture is saved apart from the downloaded covers
  (`data/game-art/.gameplay/`), so the background fetch still looks for real
  box art and one found later, or picked in Choose cover, replaces it; Clean up
  covers also takes those of games that are gone or have a real cover now. The return banner says "1
  cover taken from the game". **Settings → Games & emulators → Covers from
  gameplay** stops new pictures (`gameplayCovers`, on by default)
  ([Game library](docs/GAME_LIBRARY.md#covers-from-gameplay)). Not yet built.

- **DNS changer** (2026-11-18). Settings → Network → **DNS** picks the servers
  that turn names into addresses: Automatic (the network's own, as before),
  Cloudflare, Google, Quad9, AdGuard (blocks ads and trackers), Cloudflare for
  Families or OpenDNS, or up to three of your own — a Pi-hole at home — typed
  on the keyboard (**Custom DNS servers**). On the handheld the choice covers
  the whole system — games, ports, Android apps and JukaHub — through
  `resolv.conf`, written back every time DHCP renews the address and put back
  on Automatic, in safe mode, at quit and after a crash; on Windows it covers
  JukaHub's own requests. A VPN whose configuration names DNS servers takes
  over while it runs. The row says where the choice applies and warns when
  the servers do not answer on the current network, and **Find the fastest
  DNS** times every choice from where you are and lists them fastest first
  ([DNS](docs/DNS.md)). Not yet built.

- **Linux packages: apt for the TrimUI** (2026-11-18). The handheld can now
  install Ubuntu 24.04's arm64 programs — Python 3, Perl, git, curl, htop, nano,
  rsync, the OpenSSH client, Nmap, John the Ripper, Aircrack-ng, SQLite and the
  rest of Ubuntu's main and universe — onto the SD card, and keep them up to
  date. Use the new Tools → **Linux packages** page (status, Check for updates /
  Update all, search, one-press installs, Remove), `apt update | upgrade |
  install | remove | search | show | list | clean` in the Terminal, or
  `<JukaHub>/data/linux/bin/apt` from an SSH shell. Downloads are checked the
  way apt checks them, with only Ubuntu's archive key trusted: the signed
  Release, then the SHA-256 of every index and package. Programs run from the
  card with Ubuntu's own C library (glibc 2.39) through small wrappers; the
  firmware and its libraries are not changed, and nothing runs as a service
  ([LINUX_PACKAGES](docs/LINUX_PACKAGES.md)). Not yet built.
- **Get more cores** (2026-11-18). One press — Settings → Games & emulators →
  **Get more cores**, a tile on the Emulators page, the last row of **Which
  emulator?**, or X on a game's options → Emulator — downloads, for every
  console that has games on the card, the RetroArch cores worth having that are
  not installed yet: the ranked cores for the consoles JukaHub ranks, and a
  short list of the cores the TrimUI firmwares ship for the others (Master
  System, Game Gear, PC Engine, Arcade, Neo Geo Pocket, WonderSwan, Atari,
  Lynx…). Each goes through the Emulators page's own install (checksum, size
  limit, "can this firmware load it", the free space a core update needs).
  Cores that draw with the GPU (N64, Dreamcast, PSP, Saturn 3D) are fetched only
  for a console with no emulator at all and otherwise stay one press each on the
  Emulators page, whose row now warns that the build may not run here. With
  Emulator updates on Install automatically, the daily pass does it by itself
  once per console (`emu-morecores.json` remembers which, so a core you delete
  is not fetched again). Not yet built.
- **Google Drive sign-in you can set up from your phone, and WebDAV save sync**
  (2026-11-18). A build without the project's Google client no longer
  dead-ends: Tools → Save sync → **Set up Google sign-in** shows a code that
  opens the Phone Companion's new **Cloud save sync** page, which walks through
  making a free "TVs and Limited Input devices" client in Google Cloud Console;
  pick the downloaded JSON on the phone (or paste the ID and secret), the
  handheld checks it with Google (Desktop and Web clients are refused with the
  type to make), keeps the secret encrypted, and the sign-in starts at once with
  the code shown on the phone. With no Google project at all: **WebDAV** save
  sync (Nextcloud, ownCloud, Koofr, InfiniCLOUD, a NAS), with the address and an
  app password typed on the phone, HTTPS required except on the home network,
  the same rules as Drive (no clock decides, both copies kept on a conflict),
  JukaHub's own checksum file on the server and conditional uploads, so nothing
  changed on the server is overwritten. The automatic before/after-game sync
  uses Drive or, when Drive is not signed in, WebDAV. The page also explains
  reaching Google Drive through a computer (Syncthing into Google Drive for
  desktop) ([CLOUD_SYNC](docs/CLOUD_SYNC.md)). Not yet built.
- **A new core build is on probation until a game runs** (2026-11-18). Every
  core build JukaHub activates — the daily pass, Update on the Emulators page,
  the compatibility panel — is watched through its first game. If that game
  cannot start because of the core (RetroArch could not open it, the content or
  GPU context failed, it crashed or exited with an error within seconds, and no
  missing game file or BIOS explains it), the build before it is put back, the
  new one is remembered so no automatic pass downloads it again, and one toast
  says "The new N64 core (Mupen64Plus-Next) didn't start here — went back to the
  one that worked". The device log now names the core build every RetroArch game
  runs (file, checksum, GPU library, where it came from) and each update pass's
  counts, and the "didn't start" card says when a core build needs desktop
  OpenGL. Not yet built.
- **Packages: Run installed apps** (2026-11-18). An installed package only
  offered **Open folder**, so starting an app meant finding its `launch.sh`
  in Files. Its page and the banner now lead with **Run** (**Play** for a
  game), and Open folder follows it. The program is found once per scan of
  the card (`pkglaunch.go`): a TrimUI app folder whose `config.json` names
  its launch script, at the top of the package or up to four folders down
  (`<Name>/`, `Apps/<Name>/`), else a file named like the entry's
  `mainfilename`, which the folder's `.jukahub-package.json` now keeps. A
  bundle of several apps, none named like the package, gets no Run and lists
  each app on the Apps screen instead. It starts through the Apps screen's
  guarded launcher, which accepts a package's installed folder but never its
  staging, rollback or trash copies — on a TrimUI too, where the packages
  folder sits inside `/mnt/SDCARD/Apps` and used to pass that folder's
  blanket rule. Installed apps are listed on the **Apps** screen too, and an
  app kept one folder down now lends its card its own icon. `launch.sh` is
  offered only on arm64 Linux, a PC offers Run only for Windows programs; the
  package page's buttons squeeze on a short screen instead of losing the last
  one.

- **Quick Settings: Tiles, Compact and List layouts, and L2 to edit it**
  (2026-11-18). The panel Select opens on Home was a narrow list of
  twenty-five rows with text marks for icons. It now opens as **Tiles**: a
  four-column grid of glass tiles with the shell's icons, where a switch that
  is on lights up in its own colour, Volume and Brightness show their level as
  a bar, and the mode rows show their value. **Compact** fits every button on
  one screen as a six-column grid of icon buttons, naming the focused one under
  the title; **List** keeps the old rows (now with icons). **L2** (Q, or the
  pencil in the title with a mouse) edits the panel the way Edit Home edits
  Home: L1/R1 switch the layout, A picks a button up and the D-pad carries it,
  **X resizes a tile a cell at a time** (Volume and Brightness from 2×1,
  anything up to the grid's width and two rows), Y hides a button or shows it
  again, B or L2 is Done, and Reset puts the default buttons back. On a grid the D-pad only moves the focus: A on a slider
  starts adjusting it (Left/Right, then A or B), and L1/R1 move a focused
  slider or step a focused mode row at any time. A new **Power** button opens
  the power card (Sleep, Restart, Power off) from any screen, and Airplane Mode
  has an airplane icon. Stored as `quickSettingsLayout` and
  `quickSettingsTiles` ([CONFIGURATION](docs/CONFIGURATION.md#quick-settings-quicksettingslayout-quicksettingstiles),
  [CONTROLS](docs/CONTROLS.md#quick-settings)).
- **Every emulator a console has, offered when its game starts** (2026-11-18).
  TrimUI owners asked how to play a game in another core: the stock OS and
  CrossMix keep a console's other emulators behind Select on the game list (the
  `launchlist` of `Emus/<system>/config.json`), and a core copied into
  RetroArch's cores folder has no launch script, so nothing ever starts it.
  Game options → **Emulator** (and **Which emulator?** when it opens) now list,
  after the console's own launchers, every other script its launchlist names
  ("N64 · Mupen64Plus Standalone") and, on the handheld, every installed
  RetroArch core that plays the console and no script already starts — one
  installed from the Emulators page included. A core picked there starts
  directly: the RetroArch that owns it loads it with `-L` from its own folder,
  as the firmware's scripts do; a core built for another chip (a 32-bit core
  beside `ra64.trimui`) is not offered. Nothing changes by itself: the question
  still opens only for a console with two or more launchers of its own, and a
  console keeps starting where it did until another emulator is picked (the
  Emulator row's hint says how many more there are). Each row of the question
  says what its kind means — RetroArch: save slots, Continue, achievements;
  standalone: no JukaHub save slots or Continue — and **X** makes the
  highlighted one the emulator for every game of that console (not offered for
  Arcade, whose CPS/MAME folders each need their own; Game options →
  **Emulator**, Left, asks again). An N64, Dreamcast or PSP core
  started this way that has never run here begins with the handheld's speed
  settings (N64 at 320×240, Dreamcast at 640×480 with threaded rendering, PSP at
  its native resolution), written as its options file only when it has none.
  Not yet built.
- **Touch screens work outside the keyboard, and the Tabs Home takes swipes**
  (2026-11-18, backlog SHELL-12). On a Windows touch screen a tap now clicks
  whatever it lands on, exactly once; a sideways swipe over the Tabs Home
  changes tab with the L1/R1 slide and an up or down swipe scrolls it; the
  picture viewer and the Media screen take swipes again. A swipe no screen takes
  now does nothing — it no longer jumps to the next screen of the design — and
  none reaches past an alarm, the recovery card or a game. Touchpads only move
  the pointer; the TrimUI handhelds have no touch screen. Not yet built.
- **The manual on the device, and a tip a day** (2026-11-18, backlog SHELL-9).
  Settings → About → Controls and Quick start open docs/CONTROLS.md and
  docs/QUICK_START.md offline in the SECURITY.md reader; the build copies them
  from docs/, and the long Controls page lays out without freezing the screen.
  Home shows one tip a day above the footer (Standard, Hero and Tabs), never the
  same two days running and only tips true for the device: keys on a keyboard
  (F10, PgUp / PgDn, Alt+F4), buttons on a pad, nothing about sticks or the
  Power button where there are none. Settings → About → Show tips and Hide
  today's tip turn them off; the tip cannot be selected yet. Not yet built.
- **Update channels: Stable and Beta** (2026-11-18, backlog REL-4). Settings →
  About → Update channel picks Stable (the default: tested releases only) or
  Beta (the newest release, prereleases included). Every update path follows it
  — the startup check, Check for updates, the offline update cache and Install
  update — and going back to Stable never downgrades: a device on a beta waits
  for the next stable release. The channel shows on the About screen and in bug
  reports (an issue the report service files does not show it yet). Not yet
  built.
- **JukaHub's PPSSPP signs in to RetroAchievements with your account**
  (2026-11-18, backlog GAME-21). Before each PSP game, JukaHub's own PPSSPP is
  signed in with the RetroAchievements account RetroArch uses — the account name
  and RetroArch's login token, never a password — with hardcore off. An account
  you signed in with inside PPSSPP is left alone, and a "RetroAchievements
  sign-in" switch on the PPSSPP card turns it off. The token is kept only in
  PPSSPP's own token file, never in a log or a bug report. PPSSPP's setting and
  file names were written from memory and wait for a device check. Not yet
  built.
- **Update the Windows app runtime from its setup page** (2026-11-18, backlog
  LIB-25). When the daily Emulator updates check finds a newer Windows-app
  runtime pack (with Notify me, or after an automatic install failed), the
  Windows Apps setup page's runtime row says which build is out and A updates
  it, running the same install as the automatic pass with its progress on the
  install card. The answer is kept in memory only, so after a restart the row
  offers it again after the next check. Not yet built.
- **Choose a game's cover, and the name its covers are found by** (2026-11-18,
  backlog GAME-7). Game options has **Choose cover**: the five closest box-art
  names on the thumbnail server and the closest pictures in the console's Imgs
  folder, each previewed, and A saves it as the game's cover (an Imgs photo is
  scaled to cover size first; the Imgs files are only read). **Name for covers**
  types the title the game's box art is looked up by; it is kept in
  `data/artwork-aliases.json`, read before the built-in aliases, and the game is
  looked up again at once. Not yet built.
- **Chapters inside one audiobook file** (2026-11-18, backlog MED-9). An .m4b
  (now recognised as audio at all) or a long .m4a/.mp3 with embedded chapters
  shows its chapter list in the player, Next and Previous move between chapters
  (Previous restarts the current one first), and the now-playing line names the
  chapter. The remembered place keeps the chapter's name, so the Audiobooks
  shelf says "Continue at <chapter>". Chapters are read once with ffprobe, off
  the render thread and bounded; the list-first (Stock OS) layout does not show
  them yet. Not yet built.
- **Auto power off after sleep: 15 minutes to 48 hours, NextUI games too**
  (2026-11-17). Not yet built or tried on hardware.
  - Power off after sleeping now offers Off, 15 min, 30 min and 1, 2, 4, 6, 8,
    12, 24 and 48 h (6 h stays the default), and Quick
    Settings has an **Auto power off** row: A turns it off or back on at the
    last delay, Left/Right step it.
  - A game sleeping in a NextUI pak (minarch's own deep sleep, which JukaHub
    left alone) now gets the same deadline: when minarch saves the game for
    its sleep (NextUI's `auto_resume.txt` for that game plus its slot-9
    state), JukaHub sets the RTC wake alarm, and a wake on that alarm powers
    the handheld off; the next power-on resumes the game. A core without save
    states gets no deadline, so nothing is ever powered off unsaved.
  - Time asleep is read on the RTC as well as BOOTTIME and the longer counts,
    so a kernel that leaves the suspend out of BOOTTIME no longer hides the
    sleep; the wake log notes when the two clocks disagree (the pak's v1.0.1
    lesson).
- **Factory reset** (2026-11-17, backlog REL-6). Settings → About → Reset offers
  Reset settings, Erase everything JukaHub created (settings, library, history,
  trophies, notes, accounts and sign-ins, paired devices, caches) and Undo an
  erase, each asked twice with Cancel selected. The erase first copies every
  file it will remove into one undo file (`JukaHub-erased-….jkbackup` in
  "JukaHub Backups", Downloads on a PC) and does nothing if that cannot be
  written whole; it turns the VPN off, restarts and erases before anything
  loads, moving each item aside first so a power cut never leaves half an erase,
  and only if the undo file is still there. Undo an erase puts every file back.
  ROMs, BIOS, every save, downloads, box art, Version history, emulators,
  themes, plugins, your own cheat files and the battery calibration are never
  touched. The undo file holds your sign-ins: keep it private. Not yet built.
- **Install an update when it suits you** (2026-11-17, backlog REL-9). A
  downloaded update can be set to install at the next start or after 15
  untouched minutes on the charger, from the update prompt or Tools → Patch →
  Install it later. The choice survives restarts until that release is installed
  or replaced; an install never starts over a game, a playing video or music,
  and a low battery postpones it. At the next start it looks for a quiet moment
  for two minutes, then says it will wait for the next start; a failed install
  is never retried at every start. On a PC the second choice reads "while
  plugged in and idle". Not yet built.
- **Listening history in Music** (2026-11-17, backlog MED-17). The music player
  keeps a local log of what you play (title, artist, when, how long) for up to
  400 days, and Music gains a History shelf with Continue listening, Recently
  played and Top artists. Clear listening history (choose it twice) forgets it
  all; backups carry the log. Radio and podcasts are not logged, nor is a song
  playing when JukaHub crashes. Not yet built.
- **Daily challenge** (2026-11-17, backlog GAME-12, GAME-22). Once the clock is
  known to be right, Home's Continue card offers one library game a day with a
  goal: play it for 15 minutes of real play, for the new trophy Challenge
  Accepted, which the platinum counts. The day's game is drawn from the whole
  library, skipping hidden games and systems with no emulator. It stands in for
  a Continue item only when that item is from before today and more than 8 hours
  old (still one press away in the game switcher). On the Tabs Home the
  challenge is Recent's first item, with its cover and minutes left, beside the
  Continue item; A starts it. Not yet built.
- **Passwords for encrypted archives** (2026-11-17, backlog APP-14). The archive
  sheet in Files offers **Password…** for a protected zip, 7z or RAR: typed on a
  masked keyboard field and handed to 7-Zip for that one extraction, never saved
  or logged (though visible in 7-Zip's command line while it runs). A wrong
  password says so; without 7-Zip the sheet says an encrypted archive needs it.
  On Windows a password containing `"` is refused (7-Zip cannot receive it),
  and 7-Zip's own error text never reaches a toast or the log. Not yet built.
- **PC streaming: JukaHub's own Moonlight client, made for Vibepollo**
  (2026-11-17, backlog GAME-11). A **PC** pill closes the Home header's Remote
  Play group (also Start → Games, the Games tab and Search). PCs running
  Vibepollo, Apollo, Sunshine or GeForce Experience are found on the network
  (mDNS) or added by address; pairing shows a 4-digit PIN and a QR of the PC's
  PIN page; the games page is a grid with the PC's covers (Vibepollo's Playnite
  library), a Running badge, Play / Resume and Quit; each PC has quality presets
  (Balanced, Steady 30, Light, Sharp, Custom), codec, sound, rumble and address
  settings, and X sends Wake-on-LAN. JukaHub asks for exactly the panel's size
  and sends Artemis's `virtualDisplay=1`, so Vibepollo and Apollo stream a
  virtual screen of that size (the Brick gets 4:3, no bars); pairing names the
  handheld for their per-client permissions. On the handheld the stream runs in
  the new native helper `moonlight-stream` (player/native: moonlight-common-c,
  FFmpeg H.264 on four slice threads, Opus under 80 ms, the pads as one Xbox
  pad with the TrimUI layout, rumble, mouse mode, a stream menu with Disconnect
  and Quit the game on the PC), handed the screen like PS Remote Play; the next
  start reports how it ended with port or preset advice. It is built after the
  PS helpers as an optional step, ships in `bin/` and the Remote Play package,
  and JukaHub downloads it when missing. On a PC, Play opens Moonlight PC
  (Nonary's VRR build if installed). Docs: docs/PC_STREAMING.md. Not yet built;
  moonlight-common-c is not pinned yet (`MOONLIGHT_COMMON_COMMIT`).
- **PC streaming on the Smart Pro: Check this handheld, Auto quality, CPU and
  Wi-Fi for the stream** (2026-11-17). A "Check this handheld" row on the PC
  list runs `moonlight-stream` without a PC: it opens the screen as a stream
  does, decodes a built-in 720p Sunshine-like H.264 clip
  (player/native/moonlight-selftest.h264, four slices, embedded with
  `.incbin`) with the stream's decoder settings and times it, plays a chime,
  opens Opus and moonlight-common-c, lists the pads (TRIMUI Player1), buzzes the
  motor and waits for A; JukaHub then shows one verdict and keeps it in
  `moonlight/device-check.json`. PCs whose Quality was never changed stream at
  **Auto**: Balanced when a full-size 60 fps frame decodes and uploads in
  under 11 ms (scaled to the panel), else Light, else Steady 30. During a
  stream or the check the hand-off sets the CPU governor to `performance` and
  switches Wi-Fi power saving off, and puts both back afterwards (the PS Remote
  Play hand-off is unchanged). Not yet built.
- **JukaHub's PPSSPP: Update all and Go back** (2026-11-17, backlog GAME-15).
  Emulators → L1 "Update all" now also updates JukaHub's own PPSSPP when a newer
  release is out, and its card has "Go back to the previous build" (the build
  you leave is never switched back to by itself). Its state is saved before the
  PSP launcher entry, so a power cut between the two cannot lose your original
  PSP choice; save folders a force-quit launch left mounted come down at the
  next start and at every check outside a game, and the checks follow
  symlinks; on the Brick the pak's 0.848 aspect ratio is applied unless you set
  your own. Not yet built.
- **Xbox Remote Play and Steam in Start → Games** (2026-11-17, backlog
  SHELL-13). Both are now Start-menu rows after PS Remote Play, so they open
  from a favourite, a collection, a Home tile and Home search alike (outside the
  Tabs Home's Games tab they used to say "not available"). An experience pack
  that picks the Tabs Home shows a miniature tab strip on its card. Not yet
  built.
- **AndroidRT: the surface probe measures SDL, and GLES comes from beside
  libEGL** (2026-11-17, backlog AND-11, AND-12). `ANDROIDPAK_SURFACE=1` and
  `surface-probe` end with an `SDL:` verdict line (library, version, video
  driver, whether SDL's EGL display comes up) without creating a window. The
  game's `libGLESv2`/`v3`/`v1_CM` are bound to the libraries in the chosen
  libEGL's folder, so a GPU package outside the search path cannot mix drivers;
  `ANDROIDPAK_GLES2_LIB`/`_GLES1_LIB` still win. Inside a game run the probe no
  longer shuts down the EGL display SDL is using. Smoke suite: 229 checks. Not
  yet built for the device.

- **Settings → Display: the screen in one place** (Juka, 2026-11-17: "For
  display, make sure to be able to control Brightness, LED, LED Brightness, Lock
  Screen Time, Contrast, Saturation, Exposure"). Not yet built or tried on
  hardware. A new Display section (id `screen`, `player/displaysettings.go`)
  sits after General with, up front: Brightness, **Lock screen after** (the
  idle-screen timeout that was "Screensaver after" under Power & battery; Quick
  Settings' row is now "Lock screen" too), Contrast, Saturation, Exposure, then
  Colour temperature and Reset picture, **LED lights** and **LED brightness**
  (the Stick lights page's switch and level under their own ids; picking a level
  turns the lights on) with a door to the colours and modes, Font (below), Text
  size and Bold text. The old Display & theme page is now **Theme & look**
  (same id `display`) and keeps themes, Home, wallpaper, glass and motion. Moved
  rows keep their ids, so search, voice and deep links follow them. The picture
  rows existed (2026-11-17) but were folded under More and hidden wherever the
  display driver lacked its `enhance_*` node — now they always show: with the
  node they move the panel itself as before, and without it (the Windows build,
  a firmware without disp2's enhancement) JukaHub draws contrast, exposure and
  colour temperature over its own finished frame with GPU blending
  (`displaytune_soft.go`: MOD/ADD fills plus two custom modes, no read-back;
  JukaHub's screens only, games keep their picture, and the row says so).
  Saturation cannot be blended, so without the node its row stays greyed with
  the reason. The driver's nodes are written again after a wake.
- **Hotspot** (Tools → Hotspot, Settings → Network → Hotspot; Juka, 2026-11-17:
  "In Tools, make sure to add 'HotSpot' option and use Trimui hotspot"). Not
  yet built or tried on hardware. The handheld becomes a Wi-Fi network of its
  own for netplay and the phone Companion without a router. It uses the
  firmware's own hotspot tool (Allwinner's `softap_up`/`softap_down`, which the
  TrimUI BSP ships with) and otherwise the firmware's hostapd, with udhcpd,
  dnsmasq or a small built-in DHCP server (`player/hotspot*.go`). The name comes
  from the firmware's hotspot configuration when one is on the card (else
  JukaHub-XXXX), its password too unless it is a weak shared default (else a
  random one); both edit on the keyboard, X makes a new password, and a QR code
  joins a phone with no typing. The screen lists who
  joined and the Companion's address on the hotspot. The radio does one thing at
  a time: Wi-Fi steps aside while the hotspot is on and comes back when it goes
  off, after a sleep and on quit (the quit card has its own "Turning the hotspot
  off" step); while it is on the Wi-Fi page says to turn it off in Tools →
  Hotspot rather than tearing it down. A firmware with neither tool says so
  instead of pretending. See
  `docs/HOTSPOT.md`.
- **Fonts you can download** (Settings → Display → Font; Juka, 2026-11-17:
  "do not automatically download all fonts... keep default one and add option
  for person to install additional... it should auto install and allow one to
  apply"). Not yet built or tried on hardware. Inter stays the built-in default
  and nothing is downloaded until you pick a face: the list offers a dozen free
  fonts (Atkinson Hyperlegible, Lato, Fira Sans, Ubuntu, Poppins, Kanit, Comic
  Neue, Share Tech Mono, VT323, Silkscreen, Press Start 2P and Noto Sans JP for
  Japanese game names), each with its download size. Picking one downloads it
  over HTTPS (GitHub, then jsDelivr), checks it is a real font with the basic
  letters and digits, installs it in the data folder's `fonts` folder and
  applies it; Left/Right switch between installed fonts only, so they never
  start a download. A .ttf or .otf copied into that folder shows up too, and
  "Remove a font" deletes one (the one in use goes back to Inter). Pixel faces
  are drawn at scaled sizes so rows still fit; Legible reading text still takes
  precedence (`player/uifont.go`, `uifont_ui.go`).

- **Emulator upgrades are easier to spot.** On Windows, a missing emulator
  that would improve a console already in the library now has a translated
  **Better** pill on its install tile. Device and layout review remain pending.
- **Device release walk.** A Smart Pro, Brick and Windows checklist now sits
  in `docs/RELEASE_CHECKLIST.md`, linked from the release workflow.

- **Goodbye and greetings said in your name** (Juka, 2026-11-17: "When name
  changes, build a small file (using offline AI) to say 'Good bye, {Name}'... to
  play when JukaHub quits... so it's more personal and so that it plays
  instantly", then "Cache 'Good morning, {Name}' 'Good afternoon, {Name}', and
  'Good evening, {Name}' too so it plays instantly rather than waiting and
  generating each time"). Not yet built or tried on hardware. With Settings →
  Your name set, the shutdown card's "Goodbye, *name*" ("See you soon, *name*"
  for a restart) is now spoken too, as the line comes in, and the launch
  greeting plays without Piper loading at boot. New `player/byevoice.go`: the
  main loop hands the name to a worker twice a second (not while the on-screen
  keyboard is up); about two seconds after it last changed (Settings saves on
  every keystroke) the device's offline voice (`ttsLocalSynth`: Piper on the
  handheld, Windows' voice on a PC) makes the five lines, trimmed and faded,
  into `voice-name/<line>-<words hash>-<voice hash>.wav` in the data folder,
  replacing the line's older files; another name's files (and its cached
  greetings) go as soon as the name changes. The three greetings are made only
  where the launch speaks one (Offline answers installed, as
  `bootVoiceGreetingReady` asks) and are also stored in `voice-cache/` under the
  start-up session's exact key (device voice, no gender, `ttsPrepare`'s one
  chunk), restored from `voice-name/` when the cache's LRU drops them. A name
  already set at start-up is loaded at once and made, when missing or made by an
  older voice (Kristin → Lessac), after a 20 s boot quiet; nothing is made while
  a game runs, Juka speaks or a talk with the orb was under 15 s ago; a failed
  line waits 10 minutes (one that came out silent or too long is not tried again
  that run), and a line still being made when the app quits is cancelled with
  it. On the way out `byeVoiceForShutdown` plays only a line made for this very
  name, from memory, on its own mono device registered with the chimes so the
  exit waits for it; the held card lengthens to cover it (at most 2.6 s) when
  the budget has room, and a line that could not end inside the teardown's 5 s
  budget is not said, so the exit watchdog is never reached. Silent while Juka's
  voice is Off, in Quiet Mode, on a signal stop or a long sleep's silent power
  off; clearing the name deletes the files; the log never carries the name. See
  [docs/BOOT.md](docs/BOOT.md#shutting-down).
- **Voice orb: a size bigger, and "Type instead"** (Juka, 2026-11-17: "make
  voice orb slightly bigger and text larger.... Also add option to type just
  below add groq key"). Not yet built. The sphere's radius goes from 34 to 40
  (720-line canvas) and the bubble from 44% to 50% of the width (at most 620,
  66% over a paused game); its text is one step up the type scale
  (`orbLoadFonts`: Heading for what Juka says, Body for the line under it, Label
  for the controls). Under **Add Groq key** — while Juka thinks without a key,
  and on an answer whose fix is the key — a **Type instead** button of the same
  width: D-pad up/down (arrows) move the focus and A's key with it, A, Enter or
  a click runs it. It drops the wait and opens the keyboard ("Ask Juka"); the
  typed text goes through `voiceOrbCommand` as if heard and the orb comes back
  with the result. See [docs/VOICE.md](docs/VOICE.md#the-voice-orb).
- **Ports Store** (Juka, 2026-11-17: "how about a ports store that can
  download those ports?"). Not yet built or tried on hardware. Start → Games →
  **Ports Store** (also Home search, a Games-tab tile and the first row of the
  Ports panel's catalogue) is the answer to "how do I get ports": PortMaster's
  whole catalogue as a store. See [docs/PORTMASTER.md](docs/PORTMASTER.md#ports-store);
  device checks are PORT-HW-2.
  - **Discover** shelves: the most downloaded *Ready to Run* ports (free games
    that need no files), new ports, recently updated ones, popular ports for
    your own game files and the biggest genres, each ending in See all. **All
    ports**, **Ready to Run** and **Bring your game** are grids with search,
    sort and genre chips; **Installed** shows update badges and Update all.
  - Cards show the port's screenshot, whether it needs your files and its
    download count (PortMaster-Info's `port_stats.json`). **A** opens the
    details — big screenshot, porters, genres, size, dates, description, what
    it needs from you, runtimes and whether its own metadata says it can run
    here — with Install, Play, Update, Uninstall and Why didn't it start?;
    **X** installs straight from a card.
  - **One install queue** for every PortMaster zip JukaHub downloads (the
    store, the Ports panel, Cloud Gaming's GreenOvercast): each port is
    downloaded and MD5-checked, then PortMaster's own installer runs headless
    (`harbourmaster --no-check install`); when it cannot, the zip goes into
    PortMaster's `autoinstall` folder and PortMaster opens once at the end.
    PortMaster is installed first when missing; `portsStoreInstaller:
    portmaster` always uses the PortMaster app.
  - The catalogue is saved for offline browsing; on a PC the store is
    browse-only.

- **Cloud Gaming: Xbox Cloud Gaming (Game Pass) and GeForce NOW** (Juka,
  2026-11-17: "what about game pass compatibility… GeForce Now game streaming
  would be awesome"). Not yet built or tried on hardware. Start → Games →
  **Cloud Gaming** (also a Games-tab tile and in search;
  [docs/CLOUD_GAMING.md](docs/CLOUD_GAMING.md); device checks are HW-11).
  - On the handheld both services stream through **GreenOvercast**, an
    open-source ARM64 client JukaHub installs as a PortMaster port through the
    shared queue. Its cloud build streams Xbox Cloud Gaming and GeForce NOW,
    its xHome build Xbox Cloud Gaming and your own Xbox; the screen installs,
    recognises and switches between them (`data/greenovercast.json`).
  - GreenOvercast needs **glibc 2.38**, and stock TrimUI firmware and CrossMix
    have 2.33, so the screen says so before installing or launching and still
    lets you try (newer firmwares can run it; a newer-glibc runtime
    for stock firmware is CLOUD-1). Its hardware decoding targets H700 and
    Rockchip chips, so a TrimUI decodes in software (untested).
  - **What works here?** answers the Game Pass question: downloaded Game Pass
    games are x86 Windows/Xbox programs that cannot run on the handheld; cloud
    gaming streams them within each plan's monthly hours (Essential 5,
    Premium 10, Ultimate 15 since November 2026).
  - On a PC the rows open Greenlight/XStreaming or xbox.com/play, and NVIDIA's
    GeForce NOW app, OpenNOW or play.geforcenow.com.
  - X1 shares the build manager and the installer, and asks for a second
    press when the installed GreenOvercast is the cloud build (which cannot
    stream a console) or the firmware's glibc is too old.

- **Steam: more of your games play, in more setups** (Juka, 2026-11-17: "work
  on Steam and improve compatibility"). Not yet built or tried on hardware.
  - **Steam games on the handheld itself.** Apps → Steam matches your library
    with PortMaster's catalogue (the ports' Steam store links, else an exact
    title) and lists the matches that may run here in a new **Handheld** tab.
    A takes the next step there — install the port, finish it in PortMaster,
    **copy the game's files from a paired JukaHub PC** that has it installed
    in Steam (into the port's `gamedata`/`data`/`assets` folder, resumable,
    shown on the install card), then **Play here**. A ready port plays with
    A from every tab and counts as installed; Y → **Port setup notes** shows
    the port's own install note. New paired-device calls
    `/peer/v1/steam/files` and `/peer/v1/steam/file` serve only an installed
    game's own Steam folder. See [docs/PORTMASTER.md](docs/PORTMASTER.md).
  - **Steam Deck ratings.** Rows and the game's sheet say Deck Verified,
    Playable or Unsupported (Valve's own rating from the store, read only for
    the games on screen and kept a week in `data/steam/compat.json`).
  - **Non-Steam games** added to the Steam app (emulators, other stores'
    games) are in the library with the pictures set in Steam and start
    through Steam, on the PC and from a paired handheld.
  - **Linux PCs:** Steam from Flathub or the Snap Store starts games as well as
    the native package and SteamOS (then the install's own `steam.sh`, then the
    desktop's link handler), in its own session with the desktop's environment.
  - Remote installs and launches on your Steam PCs retry as a POST when Steam
    refuses a GET, and fall back to the unfiltered app list when the filtered
    ones fail.
- **Email and calendar** (Juka, 2026-11-17: "Email and calendar sync"). Not
  yet built or tried on hardware ([docs/EMAIL_CALENDAR.md](docs/EMAIL_CALENDAR.md);
  device checks are MAIL-1).
  - **Tools → Email & calendar** (also Settings → Network): mail accounts with
    their status, Sync now, Mail and Calendar switches and Remove; calendars
    with their switches; how often to sync, new-mail notifications, event
    reminders and how early.
  - **Set up from the phone**: the page's code opens the Phone Companion at
    its new Email or Calendars card. Typing an address fills the servers for
    Gmail, Outlook, iCloud, Yahoo, AOL, Fastmail, GMX, Zoho, Yandex and
    Mail.ru and says where that provider makes app passwords; Advanced takes
    any IMAP/SMTP server and a CalDAV address. The handheld signs in before
    it saves; a refused password is the card's error. Calendars: a Google
    secret iCal address, an Outlook published ICS link, any `.ics`/`webcal://`
    link, or CalDAV with a login. Passwords, sign-ins and links are stored
    encrypted and never sent back to the phone.
  - **Outlook** signs in with Microsoft (device code and QR, no password) in
    builds with a Microsoft client ID (`ms_client_id`).
  - **Mail app** in the Start menu, the Apps list, search and the Tabs Home,
    pinnable to Home with an unread badge; the Calendar widget shows the next
    event; the clock's calendar lists events, with reminders before them.
  - **Juka AI** gains two read-only tools, `mail_inbox` and
    `calendar_agenda`.

- **Pick up and play: sleep that lasts, Syncthing, a USB DAC player, NextUI
  in place** (Juka, 2026-11-17, relaying a Smart Pro/Brick owner: "a quick
  pick-up-and-play console with good deep sleep or quick boot, Syncthing for
  saves, PortMaster, a USB DAC lossless DAP that works screen-off and while
  gaming, and staying on NextUI so ROM/BIOS locations don't change"). Not yet
  built or tried on hardware. (PortMaster is its own entry.)
  - **Power off after sleeping** (Settings → Power & battery; 6 h by default,
    1 h on kernels that only light-sleep): a sleeping handheld wakes itself on
    the RTC alarm with the screen off, saves the RetroArch game the sleep was
    holding (save state + quit through RetroArch's network commands; only
    RetroArch's exit counts as proof the state was written) and powers off
    without the shutdown chime. On a charger, or with a game it cannot save
    that way, it just sleeps again, so progress is never traded for battery.
    Alarms still ring: the RTC is armed for whichever comes first.
  - **Resume game at power on** (on by default): the game that power off saved
    starts again at the next power-on, boot animation skipped, back at its
    state. Hold B to skip; the one-shot marker is deleted when read, so a game
    that crashes cannot start a loop.
  - **Power button while music plays: Screen off** (default; or Sleep): the
    screen goes dark, the music keeps playing and the CPU drops to the music
    profile; X, R1/L1 and Left/Right play/pause, skip and seek, the power
    button wakes the screen at once, and music stopped for two minutes lets
    the handheld sleep as usual.
  - **Built-in Syncthing** (Save Manager → Syncthing): one button downloads
    the official release (SHA-256 checked against the release's list) and
    runs JukaHub's own instance on 127.0.0.1:8386, separate from a Syncthing
    you run yourself. It shares every save folder (RetroArch, PPSSPP, DraStic,
    Flycast, NextUI) under the same Folder IDs as before, shows this device's
    ID as a QR code for Syncthing-Fork or a PC, accepts pairing requests with
    A and finds other JukaHubs nearby. It pauses the moment a game starts and
    resumes with a rescan when you are back, pauses on low battery and without
    Wi-Fi, runs at the lowest CPU priority, has a "LAN only" switch, gets its
    API key through the environment (not the command line) and stops its whole
    process tree, for good, when JukaHub quits. See
    [docs/SAVE_MANAGER.md](docs/SAVE_MANAGER.md#syncthing).
  - **USB DAC:** plug a USB-C DAC in and JukaHub's sound (games too) moves to
    it, ahead of a Bluetooth headset ("USB DAC · <name>" in the header);
    unplugging pauses playback. A kernel without `snd-usb-audio` is named
    plainly. **Bit-perfect** lossless stereo tracks play at their own rate and
    bit depth, unscaled, straight to the DAC ("FLAC 24/96 → DAC 96 kHz" in Now
    Playing); the DAC is let go at once for a game, a video or an alarm.
  - **Music keeps playing in games** (Start → Audio, on; Music volume in games
    60 %): music playing at a launch carries on under the game, mixed through
    ALSA dmix on the speaker or the DAC. JukaHub probes the mix before the game
    starts; on a Bluetooth headset, a 32-bit game, or a card that cannot be
    shared, the music pauses and says why, and the game's sound is untouched.
    The in-game menu (Select + L2) gains a **Music** row: A play/pause,
    Left/Right previous/next. See [docs/MUSIC_PLAYER.md](docs/MUSIC_PLAYER.md).
  - **Gapless playback:** the next track starts while the last one's final
    second still plays, with no click between them.
  - **NextUI cards as they are** (only on a card with NextUI's `.system`): the
    library reads `Roms/Name (TAG)` folders (the TAG found the way MinUI does,
    map.txt names and hidden entries, unknown TAGs under their own name), and
    each game starts through the same `.pak` NextUI uses — your own in
    `Emus/<platform>` first — with NextUI's environment (DEVICE brick,
    brickpro or smartpro as NextUI sets it), so BIOS stay in `Bios/<TAG>`,
    saves in `Saves/<TAG>` and states in `.userdata`. Covers in `.media`/`.res`
    come first and are never changed; the Save Manager and Syncthing see
    `Saves` and the state folders; NextUI's Recently played joins Continue
    once; a `JukaHub` entry in NextUI's Tools list opens JukaHub, and quitting
    returns to NextUI's menu. See
    [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#on-nextui).
  - **NextUI games keep NextUI's sleep:** while a NextUI game (minarch) is on
    screen the power button is left to it — its own deep sleep, and its power
    off after sleeping with the slot-9 quick save — and when minarch powers
    off, JukaHub turns the handheld off cleanly. When JukaHub is the first
    thing the handheld starts, Resume at power on also brings that NextUI
    quick save back.
  - The low-battery guard keeps watching while music plays with the screen
    off; the music in games mix keeps ~43 ms of sound (not alsa-lib's
    ~341 ms) so game sound stays in sync, and a power-on with the travel lock
    engaged keeps the pending resume for the next one.

- **USB file transfer** (Juka, 2026-11-17: "i just want something with
  support for USB via cable to pc to transfer files"). Not yet built or tried
  on hardware. Plug the handheld into a computer with a USB-C data cable and
  the SD card opens in Windows' File Explorer (This PC → the handheld), Linux
  file managers, or OpenMTP on a Mac: copy in and out, make folders, rename,
  move, delete, files over 4 GiB on exFAT — while JukaHub keeps running. It is
  MTP, served by JukaHub itself through the kernel's USB gadget framework
  (configfs + FunctionFS; `usbmtp.go`, `usbmtp_proto.go`, `usbmtp_linux.go`),
  added next to the firmware's ADB so both keep working, and taken out again
  when switched off or when JukaHub closes. On by default: Settings → Network
  → USB file transfer, whose line says ready / connected / why not. Copies in
  land under a hidden temporary name until complete; the card is hidden while
  the startup lock is up. See [docs/USB_FILE_TRANSFER.md](docs/USB_FILE_TRANSFER.md).

- **Emulation settings, cheats, the game switcher, display and lights**
  (2026-11-17). Not yet built or tried on hardware.
  - **Settings → Emulation** for RetroArch games started from the library:
    Audio quality (Auto / Best / Balanced / Fast resampling, Auto picks per
    console), Latency (Auto / Standard / Low / Lowest — shorter swap chain,
    automatic frame delay, lower audio latency, one frame of run-ahead on light
    consoles at Lowest), Smooth motion (v-sync + audio sync + rate control
    against tearing and stutter), Overlay (the overlays in RetroArch's folders),
    Rumble strength, Arcade rotation (upright or sideways TATE for FBNeo /
    MAME 2003-Plus) and PAL mode. Audio, latency and overlay can be set per
    console ("Adjust for"). Every value holds for that launch only.
  - **Cheats:** Game options → Cheats lists the game's cheats from libretro's
    database (RetroArch's own cheat folder, or a one-time download of
    cheats.zip, read in place) with an on/off switch each; switched-on cheats
    are applied when the game starts and removed when it exits. "Choose another
    cheat file" when a game has several. Reset game clears them.
  - **Game switcher**: recent games side by side, each
    with a screenshot of where you left it (save-state picture, else box art).
    A carries on from the auto save, X takes a game off the list. Open it with
    X on Home (Standard and Hero layouts), Start → Games, search, or the
    in-game menu's new "Game switcher" row, which quits the game and opens it.
  - **Display controls:** Settings → Display & theme → Contrast, Saturation,
    Exposure, Colour temperature and Reset picture, on the panel's own picture
    engine (games and videos too); rows hide where the driver has no control.
  - **Stick lights → Screen colours:** the rings glow with the colours at the
    edges of the picture, Ambilight style, in games too (off per console or for
    all games). **Low battery warning** (red pulse at 10 %, on by default),
    **Charging light** and **Standby glow** show over any lights mode with their
    own brightness.
  - **Battery history:** Settings → Power & battery → Battery history graphs the
    last 2 / 8 / 24 hours (L1/R1) with charging shaded, time left, drain per
    hour on screen and in games, and the last full charge.
  - **Scrolling titles:** a focused game name that does not fit slides to show
    its end (library list, cover views, Carousel Home).
  - **Hardware clock:** network time is written to the RTC, so a TrimUI that was
    off starts at the right time.
- **Driver update for all four TrimUI handhelds** (Juka, 2026-11-17: "Update
  drivers... make sure it works on Trimui Smart Pro, Trimui Brick, Trimui
  Smart Pro S, and Trimui Brick Pro"). Not yet tried on hardware; see
  [docs/DEVICES.md](docs/DEVICES.md).
  - **Settings → About → Drivers & hardware** lists every part of the
    handheld — screen, backlight, CPU, sound, battery, charger, fan,
    temperature, lights, rumble, sticks, Wi-Fi, Bluetooth, kernel, firmware,
    JukaHub OS — as OK / not found / not used on this model, with the driver
    and device file it uses. One table of what each model has
    (`devicedrivers.go`) feeds it.
  - **Smart Pro S (A523):** no longer mistaken for a Smart Pro (the chip
    decides); both CPU clusters now follow Auto performance, the idle saver and
    per-game limits (the fast cluster was held at the slow one's top speed);
    new **Settings → Fan** (Auto, Quiet, Max — Auto leaves the firmware's own
    fan control alone, any mode goes full speed past 85 °C, and fan speeds
    refused on firmware 1.0.2 now apply); volume finds the chip's own control;
    Bluetooth can be started by JukaHub (AIC8800 on its own serial port) when
    the firmware left it off; games get the display to themselves on a DRM
    screen and JukaHub takes it back afterwards (`drmhandoff.go`).
  - **Brick Pro:** its TG4070 code is recognised; a Brick is no longer read as
    a Brick Pro on a launcher that names both; brightness uses the display
    driver of its A133P first; its motor keeps the 2.5 V cap; its sticks stay
    calibratable.
  - **All models:** temperature uses the hottest CPU/GPU sensor; every light
    zone the handheld has follows the light modes, missing ones are skipped;
    each model only touches its own motor pins; a paired controller's battery
    is never taken for the handheld's, and time left knows each pack size;
    sleep finds the clock that can wake the device; headphones are detected on
    newer kernels; Wi-Fi never picks a P2P side-interface over wlan0 and names
    its chip; Screen Share stops retrying on a display without a framebuffer.
  - **JukaHub OS driver layers:** `drivers-a133` (Smart Pro, Brick, Brick Pro,
    Hammer) and `drivers-a523` (Smart Pro S) ship switched on: USB pads and
    dongles stay awake, Xbox pads connect over Bluetooth, the SD card skips the
    disk scheduler (the name each kernel uses — the Speed layer's `noop` failed
    on the Smart Pro S), and the Smart Pro S GPU is capped at 600 MHz to run
    cooler. Layers can now name the models and kernel they are for and show
    "Not for this handheld" elsewhere, so one OS card works on all four.
    `os/drivers/` + the manual `os-drivers.yml` workflow build kernel-module
    packs for one exact kernel; none is published yet (TrimUI has not released
    kernel source). The image builders refuse the wrong handheld.

- **Native PICO-8, a new Home look, full setting descriptions and more covers**
  (2026-11-17).
  - **Play PICO-8 on the real PICO-8, with Splore.** If you own PICO-8, copy
    `pico8_64` and `pico8.dat` from your Raspberry Pi download into `Bios/P8`
    on the card (on a PC, an installed PICO-8 is found by itself) and your
    PICO-8 carts start in the official PICO-8 instead of fake-08 — it is
    listed first and marked Recommended when JukaHub asks which emulator to
    use, and fake-08 stays one press away. Cart saves go to `Saves/PICO-8`.
    The new Splore entry (Start menu, Emulators, search) opens PICO-8's own
    cart browser; carts you favourite there are copied into your PICO-8 games
    when you leave it, and `.p8.png` carts now show up in the library.
    JukaHub never downloads PICO-8: without your files nothing changes, and
    Splore tells you which two files to copy where. `pico8native.go`.
  - **New Home look.** Covers you have not selected are greyed out so the
    selected game stands out, long lists and grids fade softly at the edges
    when there is more to scroll to, and the Tabs Home can put its tabs along
    the bottom (Tabs position: Top / Bottom). All three are in Settings →
    Display & theme and the Arrange tabs sheet (L2 on Home); dimming and the
    fade are on by default, the tabs stay at the top unless you move them.
    `homelook.go`, `listfade.go`.
  - **Full setting descriptions.** The selected setting now grows to show its
    whole description instead of cutting it off, and Accessibility → Reset to
    defaults asks before resetting (the shared Keep / Reset sheet now works
    inside Settings).
  - **More covers already on the card.** Covers beside your ROMs are read as
    `.jpg`/`.jpeg` and `.webp` too (when no `.png` exists), and NextUI's /
    NX Redux's `.media` folder is read next to the stock `Imgs` and MinUI
    `.res` folders, so a card moved over from NX Redux keeps its box art.

- **Voice unlock for the startup lock** (Juka, 2026-11-17: "voice
  authentication ... maybe just listening for keyword", with the code because
  voice "can be glitchy"). Settings → Privacy & data → Voice unlock, shown
  while a button code or password is set: **Code or voice** (the lock screen
  listens for a spoken phrase by itself for 90 s when offline speech is
  installed, R2 or V listens again; the code always works too) or **Code and
  voice** (the code, then the phrase). The phrase is set after the current
  code by saying it twice; the two must be heard the same. It is stored as a
  salted PBKDF2 hash with the engine that heard it (offline speech first,
  else Groq, which only listens on R2). Five misses in a row rest the phrase
  until the right code ("both": back to the code, counted as one wrong code).
  It recognizes words, not voices. Both the button code and the password were
  already settable with the pad alone (the password on the on-screen
  keyboard). `bootlock_voice.go`.
- **USB & OTG tools** (Juka, 2026-11-17, from an ArkOS fan's Smart Pro S
  wishlist: "OTG tools"). Tools → USB & OTG (handheld only) names what is
  plugged into the USB-C port from its USB descriptors: pads, keyboards, mice,
  sticks, USB-C DACs and headsets, wired network adapters, hubs. Where the
  kernel exposes a USB role switch or Allwinner's `usbc0` manager, Auto / Host /
  Device buttons switch the port and a Host or Device choice is restored at
  each start; otherwise the row says the firmware fixes it. USB drives are
  listed per partition (label, size, format), mount at `/mnt/usb` (FAT32,
  exFAT, ext4, NTFS), show up in Files → Locations, and eject with a flush and
  "Safe to remove"; an optional switch mounts new drives by themselves. A
  stick's `Roms/` and `BIOS/` folders import into the card with progress and
  Cancel; nothing on the card is ever replaced or deleted. `usbotg*.go`,
  `docs/USB_OTG.md`.
- **Battery calibration** (same wishlist). Tools → Battery calibration runs a
  guided cycle (charge to full, play it down until it switches itself off,
  switch it on again) so the fuel gauge relearns the cell, while a sampler that
  keeps running behind games logs the raw gauge and voltage once a minute to a
  power-cut-safe file. From that drain JukaHub works out how far off the % was
  ("Your battery read 14% when it actually shut down") and can correct the %
  everywhere it is used, so the low-battery warning and the save-and-suspend
  guard act at a true 5% and 3%. Live read-out of level, status, voltage,
  current, capacity, cycles and temperature. Nothing is written to the battery
  chip; Reset undoes everything. `batterycal*.go`, `docs/DEVICE_TUNING.md`.
- **Community themes update over the air** (same wishlist: "updateable
  themes, pref OTA"). JukaHub records which installed themes came from the
  gallery and a fingerprint of what it wrote; when the gallery is read (the
  panel, or once a day in the background while online) newer versions install
  by themselves with Settings → Display & theme → Update themes automatically
  (on by default), and the theme on screen repaints. With it off the panel
  tags the row "A update". A theme edited on the device is never replaced
  behind your back: it is tagged "Edited", A asks again and keeps a backup.
  Gallery entries may carry an optional `"updated"` date. `themeupdate.go`.

- **Google Drive save sync, built in** (Juka, 2026-11-17: "a feature missing
  from the current crop is built-in cloud sync to Google Drive"). Tools → Save
  sync → Google Drive: sign in by scanning a QR code with a phone (Google's
  device flow, `drive.file` scope only, no typing), then a library game's saves
  and states come down from Drive before it starts (≤ 6 s budget, the game
  starts anyway after that) and go up after it ends, with a background sync of
  everything 30 s later that stops when a game takes the screen. Reuses Save
  sync's merge rules: changed-on-both keeps both copies, the device clock never
  decides, Drive keeps earlier versions of every file. Release builds stamp the
  OAuth client from the `JUKAHUB_GOOGLE_CLIENT_ID`/`SECRET` secrets; without
  them the page says so (`docs/CLOUD_SYNC.md`). `gdrive*.go`,
  `savesync_auto.go`.
- **RomM saves sync by themselves** (Juka: "romm sync would be nice"). Every
  library game found on the RomM server (linked at download, else matched by
  console + name + size + checksum, misses cached 12 h) pulls before it starts
  and pushes after it ends; Tools → RomM → Connection → Automatic save sync (on
  once a server is set) and "Sync all saves now" (background, two at a time,
  summary toast). Fixed on the way: save hashes are MD5 as RomM computes them
  (SHA-256 never matched), downloads are confirmed to the server, uploads carry
  the device id, backups beside a replaced save are capped at three, and only
  real save/state names are read or written. Needs RomM 5.2+; older servers get
  one clear message. `romm_autosync.go`.
- **PortMaster that behaves like on the firmware** (Juka: "plays really nicely
  with PortMaster… completely functional"). Ports start with the firmware's
  environment (not JukaHub's libraries/PATH), through the firmware's own Ports
  launcher when the card has one, and always find PortMaster's folder, also
  when JukaHub OS boots without MainUI; `data/ports.env` overrides anything;
  every launch writes `data/gamediag/ports/<port>.log`; a bare `udevadm
  trigger` can no longer unmount the card. Ports are games: a "Ports" console
  in Games with titles from `port.json`/`gameinfo.xml` and the port's own
  cover, Continue, search, playtime, trophies, and the game assistant names
  them. Y on a port: Play, Update (newer catalogue build flagged), "Why didn't
  it start?" (missing PortMaster/runtime, 32-bit build, empty game-data folder,
  the port's note, last log lines) and Uninstall (never outside the port's own
  folders). Leftover gptokeyb/oga_controls/Weston are ended after a port; the
  lists refresh when PortMaster closes; the PortMaster row says when a newer
  PortMaster is out. Closes LIB-9. `ports_env.go`, `ports_library.go`,
  `ports_manage.go`.
- **Juka AI with device tools** (Juka: "if it's just a chatbot it's not
  useful"). 23 tools over Groq tool calling: device status, logs, "why won't my
  game start" (game diagnosis), list/find games, folders, duplicates, what fills
  a folder, read a text file, settings search/get/set, RetroArch options,
  performance profile, network check, processes, open a screen. Looking is free;
  every change waits for a card in the chat (A Allow, B Deny, Y Allow for this
  chat — never for destructive steps); deletes go to `.jukahub-trash` first;
  terminal commands are off until Settings → Voice & AI allows them. File tools
  are confined to the card/ROM/data folders, protected folders cannot be moved
  or deleted, and every result is scrubbed of keys, tokens and passwords before
  it leaves the device. Offline or without a key Juka answers device questions
  from read-only checks ("lite", labelled). The voice orb uses the read-only
  tools. Starter and follow-up chips above the Juka AI composer; R2 speaks a
  question. `aitools*.go`, `aiagent.go`, `docs/AI_TOOLS.md`.
- **Command Deck: run common commands without typing** (Juka: "not easy to
  type on a handheld"). Terminal → Select (Ctrl+P), or Commands in Tools,
  the Start menu and Home search: ready-made commands by category (System,
  Storage, Network, Files, Processes, Logs, Emulators, PortMaster, JukaHub, Ask
  Juka) whose blanks are filled with pickers — folder/file browser, choices,
  number stepper, process list, recent values — with a live preview; only
  commands this device can run are listed, every value is quoted for its
  shell, deleting and killing ask first. Favorites and macros (X saves, Y pins a
  history entry, a macro from recent commands), and token chips above the
  on-screen keyboard in the Terminal (Select takes the first). Tab completion
  reads at most 200 entries of a folder. `cmddeck*.go`, `docs/TERMINAL.md`.

- **Travel lock: the FN switch holds the power button** (Juka, 2026-11-17:
  "prevent the device from accidentally turning on during transit when the
  power button gets pressed. A hardware lock."). Settings → Power & battery →
  **Travel lock** (Smart Pro only). Turning it on takes the FN switch's
  current position as "unlocked"; sliding it the other way locks the power
  button. While locked, a press does not wake a sleeping device: every wake
  that is not a JukaHub alarm's goes straight back to sleep with the screen
  dark, also after a sleep the firmware made itself. A power-on from off
  cannot be refused in software (the PMIC switches the board on), so a boot
  hook, `System/starts/00-jukahub-travel-lock.sh`, reads the switch first
  thing and powers the device straight back off (it only acts on the board it
  was written on; JukaHub OS's own init does the same check at JukaHub's
  start). Sliding the switch shows "Power button locked / unlocked".
- **RetroAchievements and RomM from your phone; pairing code in the Start
  menu** (Juka, 2026-11-17: "allow use of companion app phone to enter
  RetroAchievements key etc. like for many other settings… also in main menu
  display QR code"). The Phone Companion page gains a **RetroAchievements**
  card (username + Web API key) and a **RomM server** card (address + client
  API token), beside the Groq key, Steam and VPN cards. The handheld checks
  what arrives with RetroAchievements / the RomM server before saving it, a
  field left empty keeps the saved value, and keys and tokens are never sent
  back (only their last four characters). The RetroAchievements setup screen
  and RomM's Connection tab get a **From your phone** row whose code opens the
  page at that card and closes itself when the account arrives. The Start menu
  shows the pairing code under its rail (tap to enlarge; Screen Share covers
  it) and lists **Device → Phone Companion**.
- **Useful plugins instead of toys** (Juka, 2026-11-17: "remove plugins that
  will never be used like Hello… we need useful plugins"). Two new bundled
  plugins: **Save guard** backs up every emulator's saves quietly each time
  JukaHub starts — only when a save really changed — keeps its last five
  snapshots and opens the save manager to restore one; **Library doctor**
  finds the same game copied twice, zips cut off mid-copy, empty files and
  Mac/Windows litter in the Roms folders, deletes the litter and moves extra
  copies to `Roms/.duplicates` (never deletes a game, never touches BIOS files
  or disc sets). **System monitor** (was System info) adds totals, the battery
  time left, chip temperature and uptime; **Random game** now lands on its
  pick in the Games library, ready to play; **Quick notes** lists all notes
  and can clear them (two presses). Hello, Dice, Daily quote, Clock, World
  clock and Handheld tips are gone; a copy an older JukaHub installed moves to
  `plugins/.retired/`. Plugin manifests gain `startupQuiet` and the
  `@random-game` / `@open-saves` actions (docs/PLUGINS.md).

- **Automatic CPU speed** (Juka, 2026-11-17: "an automatic performance
  changer that lowers or turns up performance for the app you are on";
  Settings, on by default on the handheld). The CPU slows down on Home and the
  menus and speeds up for games, videos, the Synth and HTML5 games, following
  how busy the processor really is, and remembers what each game and screen
  needed so the next start begins at the right speed. A game with its own CPU
  setting keeps it. Firmwares that don't let apps change the CPU speed say so
  in the setting.

- **The Tabs Home (JukaOS v2).** A strip of tabs in the middle of the top bar
  — Recent, Main, Games, Entertainment, Favourites, Collections — fading out
  at both ends, with the system controls either side. L1/R1 (Tab / Shift+Tab,
  PgUp / PgDn) change tab and the page slides in from the side you travel to
  (an instant swap under Reduce motion). Every tab and folder has its own view
  — Grid, Rows, List (with a large preview), Carousel or Compact — stepped by A
  on the strip; L2 (Q) opens a sheet to reorder, hide and set the view of each
  tab. Recent holds Continue, your last games with their covers and your last
  apps; Main holds Settings, Trophies, RetroAchievements, Chat, Browser, a
  Store folder (Android apps, Packages, PortMaster, Windows Apps, Downloads,
  Themes, Wallpapers), Tools, Files, Bluetooth, Wi-Fi and the phone's pairing
  code; Entertainment holds YouTube, Shorts, Live TV, Downloaded, Music,
  Podcasts and Game clips. Settings → Display & theme gains Tabs and Remember
  last tab (`player/hometabs*.go`, [docs/HOME_TABS.md](docs/HOME_TABS.md)).

- **Games, Favourites and Collections on the Tabs Home.** Games lists your
  consoles (recently played first) with covers and game counts, then
  JukaGames, PS and Xbox Remote Play, Steam, Minecraft, PortMaster, Windows
  Apps and the Emulators page. Consoles without games are hidden, and one
  whose games have no emulator is dimmed with an **Install** badge that opens
  the Emulators page on it (Settings → Games & emulators → **Hide emulators
  without games** off lists every console the device can play). X on any game
  or app opens its sheet: Play, Add to Favourites (a game's favourite is the
  same Favorite tag the Games library shows) and Add to collection…; your own
  collections can be renamed, reordered and deleted, each with its own view,
  and Most played, Recently added and Never played fill themselves. Stored in
  `collections.json`, written atomically with a backup
  (`player/hometabs_games.go`, `player/hometabs_favourites.go`,
  `player/hometabs_collections*.go`).

- **Boot animation styles.** Settings → General → **Boot animation** chooses
  what the logo does at power-on: **Cinematic** (the logo crashes onto a floor
  that cracks, with a buzz of the vibration motor, a shake, dust and sparks and
  a knock in the startup chime), **Roll** (the rounded logo tumbles in and
  stops upright in the middle), **Still logo**, **Classic** (the glass light),
  **Surprise me**, or **None** (no animation and no quick hello: the fastest
  start). Watch the boot animation previews the chosen style; Reduce motion and
  battery saver play Cinematic and Roll as the still logo, and opening JukaHub
  as an app plays a short version (`player/bootstyles.go`,
  [docs/BOOT.md](docs/BOOT.md#boot-animation-styles)).

- **Bluetooth that behaves like a phone's.** Remembered headphones and pads
  reconnect by themselves after a start, a wake from sleep or switching
  Bluetooth on (three tries, then quiet; a device you disconnected on the
  Bluetooth page is left alone, and nothing runs during a game). A headset that
  drops pauses the playing video or song, and the speaker and each headset keep
  their own volume (`bt-output-volume.json`). The Bluetooth page shows a pad's
  battery and its order (Pad 1, Pad 2), warns once when a pad falls to 15 %,
  names a headset's codec (SBC, AAC, aptX) with what it means for delay, and
  **Identify** makes a connected pad buzz and blink (`player/bt_autoreconnect.go`,
  `player/bt_audiofollow.go`, `player/bt_padinfo.go`,
  `player/bt_identify_linux.go`).

- **The recommended emulator for each console.** A ranked list per console for
  the handheld and for Windows (`player/emubest.go`; DraStic for DS,
  standalone PPSSPP, Mupen64Plus, Flycast, YabaSanshiro…, with sources in
  [docs/GAME_LIBRARY.md](docs/GAME_LIBRARY.md#the-recommended-emulator)).
  **Which emulator?** lists it first with a **Recommended** badge and
  highlights it unless you picked another last time, and on Windows the Emulators
  page marks the install tile of a better emulator for a console you have
  games for. Never installs anything by itself.

- **Natural offline voice for TrimUI handhelds** (removed again on
  2026-11-17, see Changed: the handheld only keeps the lightest voice).
  Juka could speak offline
  in Gradium's Phonon 31M voice (Apache-2.0), which sounds far more like a
  person than Piper's Kristin. It downloads by itself after Piper (or from
  Settings → Voice & AI → Natural offline voice), is timed on the device
  before it is installed — refused, with Piper kept, if the A133P needs more
  than 1.4 s of work per second of speech — and falls back to Piper whenever
  it cannot speak. The server stays loaded during a conversation, streams the
  first words in short pieces and renders each sentence while the previous
  one plays (`player/tts_phonon.go`, `player/tts_phonon_install.go`). CI builds
  it for the Cortex-A53 (Gradium's ARM64 build needs dotprod and fp16, which
  the A53 lacks), checks the binary for those instructions, and only ships it
  after it speaks offline under qemu's Cortex-A53
  (`.github/scripts/build-voice.sh`, `.github/workflows/offline-voice.yml`,
  `JukaHub-Voice-linux-arm64.zip`). Not yet heard on a handheld.

- **Hold Select + Start for three seconds to end a stuck game** (TrimUI).
  Any game, emulator or port JukaHub started is asked to quit and, after six
  seconds, stopped — nothing is drawn and the game is not paused, so it works
  on a black screen or a game that ignores the pad, which used to leave only
  a hard restart (Juka, 2026-11-15, a PSP game). A tap of the pair is still
  the game's. The pad watcher reads it beside the game
  (`player/gameassist_linux.go`, `gameForceQuit` in `player/gamemenu.go`), and
  when a launcher ends but something it started keeps the screen, the log now
  names what is still running.

- **JukaHub OS SD-card image for the Smart Pro (experimental, not yet
  boot-tested).** plastron has no Smart Pro board and TrimUI never published
  the Smart Pro's bootloader or kernel, so `os/smartpro/` boots the device's
  own: `dump-board.sh` copies the boot area, the boot partitions and the
  system files from a Smart Pro (reading only), and `build_image.py` lays
  them out on a card at the stock sectors and partition numbers, points
  U-Boot's environment at the card's read-only system copy by PARTUUID (only
  after proving the stock environment made the stock command line) and starts
  JukaHub from its own process 1, which replays the stock modules, GPIO lines
  and hardware daemons and never mounts the internal storage. Settings →
  JukaHub OS names the base it booted on. The PortMaster catalogue filter is
  now called **May Run Here**.

- **JukaHub OS** (Settings → JukaHub OS, TrimUI). JukaHub can now run as the
  handheld's OS instead of an app, and still installs like an app. Layers in
  `Apps/JukaHub/os/layers/<id>/layer.json` bring newer libraries (for JukaHub
  and, if the layer says so, every game it starts), replaced files, kernel
  drivers built for this exact kernel (vermagic must match `uname -r`) and a
  few whitelisted system values. All of it is temporary: files are
  bind-mounted read-only inside JukaHub's own private mount namespace, so only
  JukaHub and its games see them, and drivers and values are journaled in RAM
  (`/tmp/jukaos/journal.json`) and undone on quit, on **Return to CrossMix /
  TrimUI**, or at the next start after a crash. Nothing on the firmware is
  written and a reboot is always the stock system. Rows: **Boot into JukaHub
  OS**, **Start OS session now**, a switch per layer with the reason one can't
  be used, and a built-in **JukaHub libraries for games** layer (JukaHub's
  `libs/` for games; off by default). Ways out: the power card's Exit becomes
  **Return to <firmware>**; holding Select or B through the power-on animation
  starts the stock menu for that boot (not while the startup lock is up); two
  failed starts with layers start without them; safe mode skips layers; a
  damaged layer or a failed re-exec falls back to a plain start. See
  [docs/JUKAHUB_OS.md](docs/JUKAHUB_OS.md).
- **JukaHub OS package** (`JukaHub-OS-Trimui.zip` on every release). The
  same JukaHub laid out as an SD-card root that powers on straight into
  JukaHub OS: OS mode already on (`os/os.json` with `"edition": "os"`) and the
  boot hook already in `System/starts` — the app's own hook, cut out of
  `autostart.go` at build time. Extract it to the card's root and turn the
  handheld on. Built by `player/trimui/make-os-package.sh` from any TrimUI
  build (`make os-package`, and `package-sdcard.sh` writes
  `JukaHub-OS-SDCARD.zip`); optional in CI, so the app packages ship anyway.
  It ships the **Speed** layer, switched on: load-time tuning with the
  stock kernel's own values — `noop` scheduler, 512 KB read-ahead and no
  entropy/I-O accounting on the SD card and storage, folder listings cached
  longer, fewer background wake-ups — journaled and undone when the session
  ends (`player/trimui/os-layers/speed/layer.json`).
- **Startup lock** (Settings → Privacy & data). A button code (4–8 presses of
  the D-pad, A, B, X, Y, L1 and R1 — Start unlocks, Select deletes; read as the
  physical buttons, so a later A/B swap doesn't change it) or a password on the
  on-screen keyboard, asked at power-on (the default on the handheld, also with
  the Quick boot animation), on every start (the default on a PC), or on every
  start and after sleep. A start after a crash or restart that never got past
  the lock locks again. Codes are stored only as a salted PBKDF2-SHA256 hash;
  after 5 wrong tries each try waits 30 s, doubling to 5 minutes, and a restart
  doesn't reset it. While locked nothing behind it shows or acts — no toasts,
  voice, Hey Juka, Quick Settings or game assistant; the phone companion answers
  "JukaHub is locked" (HTTP 423); FTP/SSH stop — but Menu opens Sleep, Restart
  and Power off, and alarms still ring. It locks the screen, it does not encrypt
  the SD card: a forgotten code is removed by setting `"startupLockSecret": ""`
  in `Apps/JukaHub/jukauser.json` on a computer.
- **Close all apps & games.** One action closes every game, emulator, port,
  Wine program, Remote Play client, the video player and the Terminal's command
  — politely first (games save and quit), firmly after a few seconds — then
  says how much memory came back ("Closed 3 apps · 412 MB free now"). On the
  handheld it also finds apps a crash left running and frees the page cache.
  JukaHub itself, its process group, the firmware loops it paused, its services
  and Juka's offline AI and speech servers are never closed. In Quick Settings,
  Power tools, the Processes panel (X), the in-game menu (**Close background
  apps** keeps your game) and by voice ("close all apps", "free up memory").
  When offline AI, game hints or offline speech run out of memory, Juka now
  offers **Close apps and try again** and asks your question again by itself.
- **The stick lights react to Juka.** Cyan and brighter with your voice while
  Juka listens, a slow violet breath while it thinks, blue following Juka's
  voice while it answers (amber for a problem) — in the voice orb and in the
  in-game assistant over a paused game, whatever the lights normally show. They
  ease back to your own mode (or the firmware's colour) about half a second
  after Juka is done; colour changes pass through dark so the TrimUI rings
  don't blink. Settings → Stick lights → **React to Juka**: Always (default; the
  lights wake for Juka even when they are off), When lights are on, or Off.
- **Xbox One Style** and **Minimal UI** themes. Xbox One Style is the 2013 Metro dashboard (square flat tiles on near-black, lime green). Minimal UI turns Home into plain text: a list of names you walk with Up and Down, no tiles or art (`carouselStyle: "text"`, `player/thememinimal.go`).
- **Switch 2 Style is now the console's home screen.** The Switch themes,
  renamed **Switch 2 Style** and **Switch 2 Style Dark**, draw the Switch home
  on a Carousel Home: your picture in the top-left corner (A opens Trophies),
  the time, signal and battery in the top-right, and one row of square tiles —
  the games you played last first, each its box art edge to edge, then your
  Home tiles as colour squares — with the focused tile's name over it in cyan
  and the cyan outline around it. Under the row is a centred line of round
  buttons: Games, Chat (with its unread count), Store, Search, PlayStation
  Remote Play (on the handheld), Xbox Remote Play, Phone Companion, Settings
  and Power. A hairline, a controller glyph and **Start Menu · A Start** close
  the screen. Down from the row reaches the round buttons and Up goes back; Up
  from the row's left end, or Left from the status row, reaches the picture.
  After a game, it is the first tile and the focus is back on it. The
  greeting, search pill, Remote Play pills, pairing code and power pill give
  way to the round buttons; Edit Home (L2) still arranges the tiles with its
  usual strip. Theme files get the same Home with `"carouselStyle": "switch"`,
  while `"strip"` keeps the plain row
  ([player/themeswitch.go](player/themeswitch.go),
  [docs/THEMES.md](docs/THEMES.md#console-styles)).

- **PS5 Style is now the console's welcome hub; the old look is PS4 Style.**
  Settings → Appearance → Themes → Consoles → **PS5 Style** lays Home out like
  the PS5 home screen: "Games  Media" across the top (a tap on Media opens
  it), your Home tiles as one row of small rounded icon tiles — the focused one
  larger at the left with its name beside it, the row ending at both sides —
  then two pills (trophy progress, the game library) and a board of flat
  graphite cards on a near-black page: battery with a ring gauge, Continue
  with its artwork, Chat with the unread count, Trophies by tier, the last
  game played, Packages, Storage with a free-space bar, Now playing and
  today's playtime. Down from the row enters the board, the D-pad walks it,
  A opens what a card shows; Edit Home arranges the row and hides the board
  while it is open. The previous PS5 look (the top-left shelf on deep-blue
  navy) is kept as **PS4 Style**, with its palette as **PS4 Night**. Theme
  files can ask for the hub with `"carouselStyle": "hub"` (docs/THEMES.md).

- **Wi-Fi, Bluetooth and Power tiles you can pin to Home.** Testers asked
  for one-press Wi-Fi, Bluetooth and power-off shortcuts.
  Edit Home's **Add to Home** card (L2, then **+**) now starts with a **Quick
  controls** section: **Wi-Fi** and **Bluetooth** switch the radio with one
  press — the round badge is lit while it is on, and the tile names the
  network or how many devices are connected — and **Power** opens the power
  card with Power off focused, beside Sleep and Restart (Exit on a desktop).
  The switches run in the background with Quick Settings' own Wi-Fi and
  Bluetooth code, so the panel, voice commands and the tile agree; turning
  Wi-Fi on also starts the supplicant where the handheld needs it, so it
  rejoins the saved network. Where the radio cannot be switched (Windows,
  macOS) the tiles open the Wi-Fi or Bluetooth page. Pinning works from every
  layout (Standard switches to Widgets, as any Edit Home change does); the
  refs are `w:wifi`, `w:bluetooth` and `w:power` (CONFIGURATION.md).

- **Focus bounce on every screen, on the Smart Pro too, with a setting.** The
  highlight that slides to the next button, runs a little past it and swings
  back used to play only on Home and only on the PC: it followed Reduce Motion,
  which the handheld ships switched on. It now has its own row, **Settings →
  Display & theme → Focus bounce** (Off / Subtle / Normal / Bouncy, Normal by
  default), and plays wherever a press moves the focus ring — Settings, lists,
  panels, sheets, Chat, Files, Media, the store, the on-screen keyboard. It
  starts only on a direction step (D-pad, stick, arrow keys, Tab), and the
  ring rides along with a list or carousel that is still moving, so nothing
  wobbles on its own; it gets the full frame rate while it moves even in Low
  Power (Battery Saver keeps its cap). Reduce Motion still turns off the card
  lifts and the scene fades.

- **Which emulator? when several can play a game.** Starting a game from the
  Games library, Continue or Files used to take the first emulator that
  matched its console silently. When two or more fit (CrossMix's GBA and mGBA
  folders, RetroArch beside a standalone emulator on a PC), a small sheet now
  asks: A plays with the highlighted one, which is highlighted again next time;
  Y always uses it for that game; B cancels. Game options has a new
  **Emulator** row to pin one or go back to *Ask each time*. A console pinned
  in `game-launchers.json` and a console with one emulator start at once, as
  before. Save-state list, diagnosis and Reset game follow the emulator the
  game actually uses.

- **X1: Xbox Remote Play next to PS4 and PS5.** The Remote Play pills in the
  Home header now end with an X1 pill that streams your Xbox One or Series
  console. On the handheld it starts GreenOvercast (the xHome build), a native
  Xbox streaming client for ARM handhelds; the first press downloads it into
  PortMaster's autoinstall folder and opens PortMaster to install it (and
  installs PortMaster first when it is missing). Sign-in happens in
  GreenOvercast with a code on your phone; the console needs Remote features
  turned on. GreenOvercast needs glibc 2.38, so stock TrimUI firmware (2.33)
  is warned before it tries. On a PC, X1 starts Greenlight or XStreaming when
  installed, else opens xbox.com/play's console page. The dot lights when a
  client is installed. X1 also shows on PCs, where the PS pills stay hidden.

- **Realistic glass (Settings → Display & theme → Glass).** Classic stays the
  default; Realistic is an Apple Liquid Glass-style look for every glass
  surface: what is behind a card or button shows through blurred and slightly
  magnified, a sheen lights the top of the pane, a bright rim catches the light
  from the top left with a softer glint on the opposite corner, and Home cards
  float on a soft shadow. Sheets, small chips and panes over pictures, videos or
  games get the light and rim but not the blur. Off while Reduce Transparency or
  High contrast is on; falls back to Classic where the renderer cannot draw it.

- **Consoles and Experience tabs in Theme presets.** Themes now has four tabs:
  Colour · Total conversion · Consoles · Experience. Consoles gathers the
  console looks — PS5, Switch, Xbox, Sega Blue, Game Boy — and eight new
  classic-console themes: PS1, PS2, PSP, Super Nintendo, Nintendo 64, GameCube,
  Mega Drive and Dreamcast, each with its own Home layout, tile corners, focus
  ring and glows, plus a plain palette in the Colour tab (colours and shapes
  only, no logos or artwork). Experience opens the Experience packs screen, which
  shows the same tabs; Tab, L1/R1 or a click moves between them and B leaves to
  where you came from. The separate "Experience pack" row in Display & theme is
  gone (packs are still in Tools, Quick Settings and Home search).

- **Simpler Settings.** Each section opens on its most-used settings, with
  **More settings (N)** under them to unfold the rest in place; **Settings →
  About → Show all settings** brings back the full lists. **Search** sits at
  the top of the rail: press **Y** (Ctrl+F, or just type on a PC) and matching
  settings from every section appear as you type — everyday words like "wifi",
  "dark mode", "sleep" or "volume" work — and A goes straight to the row.
  Date & time moved into General and Keyboard into Controls, so the rail is two
  entries shorter; old links still land on the right rows.

- **Juka can see the screen.** Ask the voice orb about what you are looking at —
  "what's this?", "read this", "which one should I pick?" — and a picture of
  JukaHub's screen, taken as the orb starts listening and without the orb on
  it, goes to Groq's vision model with the question. It is never saved and is
  dropped once the answer is done; the Phone Companion pairing code is blanked
  out. Every question also tells Juka which screen and item you are on.
  Settings → Voice & AI → Let Juka see the screen: Off / When you ask about it
  (default) / Every question. Offline answers get the screen's name only.

- **Minecraft (Start → Games → Minecraft).** One screen with every way to play
  it here. On the handheld: Java Edition through PortMaster's Minecraft port,
  ClassiCube and Luanti · Mineclonia (free, no files needed) — A installs the
  port straight from PortMaster's catalogue (MD5-checked, handed to PortMaster's
  autoinstall) and then plays it — and Bedrock Edition with your own APK
  through AndroidRT. Java Edition's game files come from your own copy: with
  JukaHub on a paired PC, every version in its `.minecraft` shows as "Copy
  1.12.2 from <PC>", and one press copies the version, its libraries and assets
  with every checksum verified (never the account, worlds or settings). On a PC
  the screen starts the Minecraft Launcher, Minecraft for Windows or Prism
  Launcher. See [docs/MINECRAFT.md](docs/MINECRAFT.md).

- **"Hey Juka" wake word (Smart Pro).** Settings → Voice & AI → Hey Juka (off
  by default) opens the voice orb when you say "Hey Juka", or the game
  assistant during a game ("Hey Juka in games", on once the wake word is on).
  "Hey Juka, open YouTube" works in one breath. It listens with a tiny word
  list at low priority, only decodes when there is sound, and pauses while
  anything else uses the microphone, while Juka speaks, during video or music,
  with the screen off and when memory is low. Runs on the handheld only.

- **The voice assistant changes settings.** Say "turn on dark mode", "turn off
  background music", "enable low power mode", "make the text bigger", "set
  screen timeout to 5 minutes", or ask "is Wi-Fi on?" / "what is my
  brightness?" — any switch or list in Settings, offline too, through the
  row's own control so it saves like a press. Pages, text fields, keys and
  resets only open, never press. With a Groq key Juka can also make a change
  it was asked for (checked against Settings first; a few sensitive rows are
  never offered to it). In a game, "turn the volume down" or "make it
  brighter" works the same.

- **Spatial audio for every game and app.** On the handheld, the virtual
  surround now reaches the sound of everything JukaHub starts — RetroArch,
  PPSSPP, DraStic and the other standalone emulators, ports and apps — not
  only RetroArch games that could load the filter. JukaHub runs its plugin in
  front of the speakers (or the Bluetooth headset) through the system's sound
  library, for that program only, with nothing on the firmware changed; a
  silent check at start-up proves the route on the device first, and until it
  has, games sound as before. **Tools → Audio → Spatial audio in games &
  apps** is now on by default, so choosing a Spatial audio mode is all it
  takes; it reads *On · RetroArch only* where the route is not available. The
  background music plays through the effect too
  ([player/spatial_alsa.go](player/spatial_alsa.go),
  [docs/SPATIAL_AUDIO.md](docs/SPATIAL_AUDIO.md)).

- **The chat's paperclip is selectable.** On the composer, Left steps onto
  the paperclip (it lights up with the focus ring), A opens Files to pick
  what to attach, and Right or B goes back to the text field. Before, the
  paperclip only answered a mouse click or tap; X still attaches from
  anywhere in Chat. On the TrimUI Smart Pro the pointer now works in Chat
  too: tilt the right stick sideways and it comes up (up / down still
  scrolls the history), the paperclip rings under it, and A clicks it.
  The D-pad puts the pointer away and the stick scrolls again.
- **Start sends from the on-screen keyboard.** Start is now the keyboard's
  send button: in chat it sends the message to the tab on screen, and every
  other field runs its action key (Search, Save, Join, Go…), the same as Enter
  on a real keyboard. Y still asks Juka AI in chat; B closes. While dictating,
  Start keeps the words like A does. The footer shows **Start Send** (the
  keyboard-mode Enter hint in chat said "Ask AI" though Enter sends; fixed).
- **Console-style themes.** The theme gallery's Total conversion tab (Settings
  → Appearance) opens with PS5 Style, Switch 2 Style, Switch 2 Style Dark and
  Xbox Style — colour, tile shape, layout and focus only, no logos or artwork. PS4
  Style (first shipped as PS5 Style) puts the Carousel on a top-left shelf of
  small rounded tiles on a deep-blue night; the focused tile grows, takes a
  white ring and shows its name beside it. Switch 2 Style (first shipped as
  Switch Style) is the console's home screen, see above. Xbox Style is
  the Widgets grid in charcoal rounded rectangles with green highlights and a
  white ring. The palettes alone (PS5 Night, PS4 Night, Switch Light, Switch
  Dark, Xbox Green) are in the Colour tab. Theme files can use the new optional
  `carouselStyle`, `tileRadius`, `focusRing` and `focusRing2` keys
  (docs/THEMES.md).
- **Stick lights: Smooth effect.** Settings → Stick lights → Effect now has
  Smooth beside Solid, Pulse and Rainbow: a slow six-second glow between about
  half and full brightness, eased in perceived brightness so neither end looks
  like a blink. Only the level moves, so the TrimUI rings are never re-armed
  for it. Charging now breathes on the same slow curve instead of the
  two-second pulse.
- **R2 steps the liquid background, on any screen.** Off → Slight → Medium →
  Lively, with a toast naming the level, wherever the screen's footer gives R2
  no job of its own (Media's "R2 More", the browser's and reader's zoom and the
  stores keep theirs); the footer then shows **R2 Water** right after the
  navigation hints, and Home's footer always names it (**W** on Home with a
  keyboard). Once on, the water is behind every screen. Slight is a new,
  softer level; Medium is the old Gentle (a saved `gentle` still reads as
  Medium).
- **Voice orb: "Speed up by providing a Groq key".** While Juka is thinking (Menu, or F4 on a PC)
  with no Groq key set, the bubble shows that line with an **A  Add Groq key** button (A, Enter or a
  click) that stops the wait and opens Settings → General's Groq key sheet. Groq is much faster and
  better than offline speech and answers; with a key the nudge never shows. It replaces the old
  once-per-launch tip text.
- **Home's focus highlight bounces between buttons.** Moving around Home no
  longer teleports the cyan ring: it slides to the next card or button, runs a
  little past it in the direction you moved and swings back onto it (glow
  included). The overshoot is capped at a few pixels so long jumps stay tidy;
  Reduced motion, Battery saver and Focus motion "None" turn it off.
- **The Synth in the Games library, and covers for the built-in games.** Games
  → **Built-in games** now lists the **Synth** beside JukaLand, so you can
  open the groovebox and make music straight from your game list (A opens it,
  like JukaLand). Both now have real covers instead of a plain icon: a voxel
  island for JukaLand and a glowing step grid for the Synth, drawn by
  `branding/mkcovers.py` and built into JukaHub, so they show offline. The
  Games screen's JukaLand and Synth cards show the same covers.

- **Stick lights that react to your voice.** Settings → Stick lights → **Lights
  show** → **Microphone** turns the stick lights on and makes them follow the
  microphone: talk, sing or clap and the rings glow with the loudness, coloured
  by pitch. **Mic sensitivity** (Low / Normal / High) sets how loud a sound has
  to be; a noise floor that follows the room keeps a fan or hum dark. It is
  also LED mode **Microphone** in Quick Settings and the Light style panel.
  The microphone is only open while JukaHub is on screen, a voice feature takes
  it over while it listens, and nothing is recorded. The TrimUI stick rings
  are now driven at all — they sit under `/sys/class/led_anim`, which the LED
  sync never looked at — and get the firmware's own colour back when the
  lights stop or a game starts. (`ledmic.go`, `ledanim.go`; `ledsync.go`,
  `voicemic.go` hand-over, Settings rows, i18n.)

- **Stick lights have their own page in Settings.** Settings → **Stick
  lights** (below Controls) sets the LEDs around the analog sticks without the
  overlay: the on/off switch, what the lights show (**Colour**, Music,
  Microphone, Battery, Charging, Temperature), a **Colour** row of 15 swatches
  (the theme's accent first; A opens the full list), **Effect** (Solid / Pulse /
  Rainbow), **Brightness** from Off to 100% and a **Colour wheel** row for any
  hue in between. Picking a colour, effect or mode turns the lights on, so the
  change shows at once instead of being dropped when the old style overlay
  closed; switching them off hands the sticks back to the firmware's colour,
  while Brightness Off keeps them dark. A device without writable lights says
  why. The rows moved out of Sound & haptics. (`ledsettings.go`, i18n.)

- **Screenshots flash and show what they caught.** Taking a screenshot in
  JukaHub (hold Menu, F12, the Start menu, a voice command) now fires a quick
  white camera flash, and the picture itself shrinks from full screen into a
  card in the bottom-right corner — "Screenshot saved" under it — instead of
  a text-only toast. Clicking the card opens the picture in the image viewer.
  A game's screenshot has no flash (the game owns the screen then); its
  preview card appears when you are back in JukaHub. Reduced motion keeps the
  flash soft and puts the card in place without the fly-in.
  (`shotpreview.go`; `screenshot.go`, `menushot.go`, `main.go`.)
- **Hold Menu for a screenshot, anywhere — in a game too.** On the handheld a
  tap of Menu still opens the voice orb; hold it for 0.7 s (a light tick) and
  let go, and the screen is saved (a buzz when the file is written). It works on every JukaHub screen —
  including panels where Menu means Back, where Back now comes on the release
  so a hold can be a screenshot — and inside games and emulators JukaHub
  launched: the pad watcher reads the framebuffer the moment Menu goes down,
  before RetroArch or PPSSPP draws its own menu, and keeps the picture if Menu
  was held alone (a hotkey, a trigger or Menu + volume brightness cancels it).
  Game shots are counted in the "While you were playing" summary. With voice
  off, Menu no longer stands in for Select in chords such as Menu + L1. Shots are PNGs now (not BMPs) in
  `/mnt/SDCARD/Screenshots` on a TrimUI, `Pictures\JukaHub` on a PC. Settings
  → Controls → **Hold Menu: screenshot** turns it off, which brings back
  hold-to-talk. (`menushot.go`; `voiceorb.go` Menu reader, `screenshot.go`,
  `gameassist_linux.go` watcher, Controls row and controller card.)

- **Notifications during videos (Settings → Media), off by default.** A
  full-screen video that is playing is no longer covered by toasts nobody asked
  for (a finished download, an update, a sent bug report): they still go into
  the bell, and when the video pauses, stops or leaves full screen one line says
  how many came in. Feedback for what you just pressed ("Subtitles on",
  "Quality: 720p") and errors still show. Turn the switch on to see alerts over
  the video as before, plus new chat messages as "Sender: message" toasts.
  (`videonotif.go`; hooks in `core.go` renderToast, `setmodel.go`,
  `adaptive.go` LastInput; i18n for 6 languages.)

- **Videos that have to be downloaded first can download in the background.**
  When a stream will not play and JukaHub downloads the file instead, the
  loading card now offers **Y Download in background** next to **B Cancel**
  (keyboard Y, or a tap). The card closes and the handheld is free; the download
  keeps going with a progress chip in the activity strip (its ✕ stops it). A
  finished file is saved to Media → Downloads, a toast/notification says it is
  ready, and the chip becomes *Ready* — clicking it plays the video from where
  Play was pressed. Nothing auto-plays over what you are doing. At most two run
  at once, Play on a video still downloading says how far it is instead of
  starting a second copy, auto-sleep waits for them, and quitting stops them.
  The download watchdog also
  no longer counts time the app spent paused as a stalled connection.
  (`ytdownload_bg.go`; hooks in `ytdownload.go`, `loadingcard.go`,
  `activity.go`, `main.go`, `core.go`.)

- **JukaHub builds its own PPSSPP for the Smart Pro and Brick.** A new weekly
  workflow (`.github/workflows/ppsspp-trimui.yml`, also runnable by hand) builds
  the newest upstream PPSSPP release (hrydgard/ppsspp; 1.20.4 today) for tg5040
  in the Ubuntu 20.04 arm64 build root (`player/native/build-ppsspp.sh`,
  `build-root.sh build ppsspp`): the handheld's own SDL2, GLES and glibc at run
  time, linked against the TrimUI SDK's GLES libraries when the toolchain image
  has them, gcc 9's libstdc++ bundled, NEEDED and glibc checked. It publishes the
  rolling release `ppsspp-tg5040` (never GitHub's "latest", so JukaHub's own
  updater is unaffected) and skips weeks with no new upstream version. On a
  tg5040 JukaHub's PPSSPP manager now asks both minui-psp and this release and
  installs whichever names the newer PPSSPP (a tie keeps what is installed,
  else minui-psp), with the same digest check, firmware-library check and
  fallback to the firmware's PPSSPP. The Smart Pro S (tg5050) still uses
  minui-psp only. Not yet run on GitHub or tried on a device.

- **Unread chat badge on Home.** The Chat tile shows a red count of unread messages (community
  channel + Juka AI; Saved never counts) in its top-right corner and "*N* new messages" under its
  name, in every Home layout. A count that goes up pops with a fading ring. The community channel now
  connects in the background ~15 s after start when a Discord token is set, so live messages count
  without opening Chat first. Opening Chat marks every tab read and clears the badge; the "New
  messages" divider still shows where each tab's new messages begin (`chatbadge.go`).
- **Liquid background switch in the Start menu.** Start → Device → **Liquid background**
  shows On or Off and switches the effect immediately. It starts on at Gentle,
  remembers a chosen Lively level when turned off, and restores that level when
  turned back on.
- **Background music.** Settings → Sound & haptics has a **Background music** switch (off by
  default), a **Music** choice and a **Music level**. The music is one of three built-in songs —
  **Calm**, **Lo-fi** and **Chiptune**, written as Synth projects and rendered live, so nothing ships as
  audio — your own **Synth song**, or **My music**, the music library shuffled through ffmpeg (with
  **Next song**). It fades out whenever anything else wants the speaker: the music player, a video,
  the Synth, Quiet Mode, a sleeping screen, the voice orb, dictation and an alarm; it ducks under Juka's
  voice, and a launched game closes its device at once. A library song carries on where it stopped.
  Stored as `bgMusic`, `bgMusicSource`, `bgMusicVolume` (`bgmusic.go`).
- **The boot and the shutdown greet you by name.** With **Settings → Your name** set, the boot
  animation fades in "Welcome back, *name*" under the JukaHub wordmark (the full power-on boot, the
  quick app-launch hello and Watch the boot animation), and the shutdown card ends on "Goodbye,
  *name*" ("See you soon, *name*" for a restart), held about a second before the light goes out.
  No name, no change; screenshots leave it out; the phrases are translated in all six languages
  (`bootgreet.go`). The first-run tour now asks for the name right after the welcome (**What should we
  call you?**: A opens the keyboard, optional, same setting; `onboarding_name.go`).
- **Arrange the top bar.** Edit Home (L2 on Home) has a **Top bar** row: Up from the strip, or its
  **Top bar** chip, shows one chip per control on the right of the top bar — brightness, volume,
  Bluetooth, Wi-Fi, weather, clock, battery, now playing, the update offer, Trophies, Report a bug and
  Notifications — in their order. **A** picks the focused one up and **Left / Right** carry it (the bar
  above follows live, with the control ringed), **Y** hides it or shows it again (the bell always stays),
  and **X** / **Reset** puts the shipped order back. The order holds on every screen; the pointer can
  drag the chips and use the hide badge. Stored as `headerLayout` (docs/CONFIGURATION.md).
- **Steam library in Apps.** Apps → **Steam** lists every game of your Steam account with its cover,
  play time, size and where it is installed, in three tabs (L1/R1: All games, Installed, Downloads),
  sorted by last played, name or play time (R2) and narrowed by a search (L2). **Sign in with the Steam
  app** (X → Account): scan the code with the Steam app on your phone (Steam Guard → Scan a QR code)
  and approve — no password is typed and JukaHub never sees one; that sign-in also lists a private
  profile's games. Steam's website sign-in (phone or the PC's browser) and a profile link identify the
  account; if Steam asks for a login to show its games, use the Steam app sign-in or a Web API key.
  The companion page's Steam card accepts profile links and keys. On a PC with the Steam
  app, A plays a game or installs it, Y opens the game's sheet (bigger cover, details, Uninstall, store
  page), and downloads show a progress bar until Steam finishes them. On the handheld, A plays the game
  on one of your PCs: with the Steam app sign-in, any PC signed in to Steam shows what it has installed
  and is downloading, and A installs or starts a game there with nothing else running on the PC (pick
  the PC in the game's sheet); a paired JukaHub PC (Nearby devices) works without it. The list is kept
  for offline use in `data/steam/`; it is also in Search.
- **PS4 / PS5 controllers over Bluetooth in Remote Play.** A DualShock 4 or DualSense paired with
  the handheld now works fully in a stream: its gyroscope and accelerometer reach the console with an
  orientation worked out from them (motion aiming and steering), next to Create, the touchpad and
  rumble. A pad that reaches SDL only as a raw joystick (a DualShock 4 on the TrimUI's 4.9 kernel,
  hid-generic or hid-playstation) gets a mapping for its GUID in its driver's button order, in the
  Remote Play helper and in JukaHub's menus alike, instead of being read scrambled or ignored. A
  DualSense whose touchpad is a separate kernel node keeps the Select layout, so its Create button can
  still click the touchpad. The Bluetooth diagnostics add a `Pad HID` line (HIDP, uhid, Sony drivers).
- **Save slots in the in-game menu.** Hold Select + L2 in a RetroArch game started from the library:
  **Save state** and **Load state** open the game's slots (three per page, six in all), each with its
  picture and when it was saved; replacing a used slot takes a second press and empty slots cannot be
  loaded. Uses RetroArch's `SAVE_STATE_SLOT`/`LOAD_STATE_SLOT` commands and falls back to the slot keys
  on builds without them.
- **Automatic save with a previous copy.** Before a RetroArch game starts, its last auto save is kept as
  the "Previous auto save" (one copy, with picture and date) in the game's states strip; starting from it
  swaps it with the current auto save, so it can be undone.
- **Sleep on the power button, and in the in-game menu.** A short press of the power key sleeps the
  handheld from Home, any screen, or mid-game (RetroArch, PPSSPP, ports): the game pauses, the screen goes
  dark, the card is synced and the device suspends; on wake it carries on and the wake press is ignored.
  Hold 1.5 s on a JukaHub screen for the Exit card. Settings → Power & battery → Power button: Sleep, Exit
  card or Nothing. If the firmware already sleeps on the key, JukaHub leaves it to it. A JukaHub alarm
  wakes the device in time to ring. The in-game menu has a **Sleep** row too.
- **Game diagnosis.** When a game closes within seconds of starting, JukaHub says why: the BIOS file the
  core needs (names, accepted alternatives, region variants) and the exact folder this device's RetroArch
  reads it from, a missing or unloadable core, a game file that is gone, empty or a damaged zip, a
  .cue/.m3u/.gdi with missing tracks, missing arcade ROM files or parent/BIOS sets (neogeo.zip) named from
  the core's log, and PortMaster ports whose game files were never copied in. Otherwise it says so and
  shows the emulator's last log lines. Game options → "Check why it won't start" runs it any time.
  RetroArch's log goes to JukaHub's data folder for JukaHub launches only. The launch-failure card wraps
  long paths, lists several problems and scrolls; BIOS Doctor now knows which BIOS each core really needs.
- **Video filters for RetroArch games.** Settings → Video filter, and per game in the options sheet:
  Sharp, Smooth, Pixel perfect, plus Scanlines, LCD grid and CRT when a matching shader or filter is
  installed. Applied for JukaHub launches only (a temporary global auto-preset for shader looks) and undone
  when the game exits; your own presets are never overwritten.
- **CPU profile per game.** Game options → CPU profile: Auto, Battery saver, Balanced or Performance (the
  CPU limit row still caps the clock). It also applies when the game starts from Ports, Emulators or LAN
  play, and the library shows it beside the game.
- **Interface language.** Settings → General → Language: English (default), Español, Português (Brasil),
  Français, Deutsch, Italiano, Русский — immediate, saved as `uiLanguage`. ~1,100 strings per language
  (machine-translated, awaiting native review); anything untranslated stays English. Web pages, PDFs, chat,
  notes, subtitles, typed text, file names and game titles are never translated.

- **Synth: a pocket groovebox** (Start → Games → Synth, right under JukaLand; a card on the
  Games screen; Tools → Synth), in
  the spirit of HUM on the TrimUI Brick. Seven lanes — kick, snare, hat,
  clap, bass, lead, chords — in a 16-step sequencer with eight patterns,
  accents, ratchets, swing, mutes and pattern chaining; a Play page whose
  D-pad and Y / X / A are seven pads in the song's key and scale (record
  them into the running pattern); a Sound page per instrument (presets,
  wave, low-pass filter with resonance and envelope, ADSR, length, glide,
  detune, octave, level, pan, echo, "Surprise me") and drum kits with tune,
  decay and level per drum; a Mix page (tempo with tap, key, nine scales,
  steps, ping-pong echo, volume, copy / clear / random beat / demo) and
  Export to WAV into Downloads/music. All sound is synthesized on the device
  (polyBLEP oscillators, TPT state-variable filters, synthesized drums) on
  its own stereo device that closes when the page is left. Start plays and
  stops; keyboard twins throughout. Saved to `synth.json` (in backups);
  four trophies. `synth.go`, `synth_engine.go`. Reachable from the Apps
  screen (built-in tile), Start → Tools, the Tools hub, Home search and voice
  ("open synth", "make music", "drum machine").
- **Ideas from Mosaik CFW (TrimUI Smart Pro / Brick Pro).** A pass over
  diapolon/mosaik_cfw's features that JukaHub did not have yet:
  - **Phone Companion → Files.** Browse the folders the FTP server shares
    (games, BIOS, saves, screenshots, media; the shared folder on a computer)
    from a phone or PC browser and download, upload several files with
    progress, make folders, rename and delete — no SD card removal. Uploads
    stream onto the card under a hidden name and appear only when complete;
    replacing asks first and never loses the old copy; delete takes a file or
    an empty folder only; Roms/Ports and game-profile folders stay read-only.
  - **Power card: Sleep, Restart, Power off** (TrimUI only) above Cancel /
    Exit. Restart and Power off quit JukaHub cleanly first (games stopped,
    settings, playtime and saves written, cleanup hooks run), then reboot or
    switch off. Sleep uses the power button's sleep (`sleepNow`).
  - **Auto sleep** — Settings → Power (`autoSleep`, Off by default; 3–30
    min): sleeps after that long idle, never while a game runs, charging,
    something plays, downloads, installs or is shared, or an alarm is set.
  - **Real Screen off:** the dock's Screen off now switches the TrimUI
    backlight off (DISP_LCD_BACKLIGHT_DISABLE, level-0 fallback); every wake
    path and the next start after a crash turn it back on.
  - **Reset game…** in game options: *Reset settings* drops the game's options
    plus its RetroArch override/.opt/.rmp and MAME 2003-Plus / FBNeo DIP and
    high-score files (exact ROM name only); *…and delete save states* also
    takes states, their screenshots and arcade NVRAM. ROM and .srm saves
    always stay; files go to `reset-trash/<date>/` with a `restore.txt`.
  - **Clean up covers** (game options): unused downloaded box art goes to the
    same trash after a confirmation; skipped when a games folder can't be read.
  - **Tags:** X renames and Y deletes your own tags in the tag filter (L3 / T).
  - A game with its own CPU limit says so when it hands the screen back.
  - Header battery: red below 15%, blinks below 5%, fills in a loop on the
    charger; the Wi-Fi meter breathes while a network is joining (both steady
    with Reduced motion / Battery Saver).
  - RetroArch games started from JukaHub write a changed battery save every
    10 s (`autosave_interval`, only when the user had none; restored on exit).
  - A JukaHub update installs below 30% battery only on the charger.
  - **Playtime is per console:** the same title on two consoles keeps its own
    hours. Old hours move under their console wherever only one console is
    known for the title; totals and trophies are unchanged.

- **Netplay: rooms on your Wi-Fi and online play.** **X** on a game now opens
  **Netplay** instead of LAN Play. A JukaHub hosting on your network shows up
  live in everyone else's panel, and **A** joins it with your own copy of the
  ROM and the host's core, whatever game you opened the panel on: no more
  typing an IP address. **Host online** hosts through one of libretro's public
  relays (←/→ picks New York, Madrid, Montreal or Sao Paulo), so a friend on
  another network joins without port forwarding, and **Online rooms** lists
  RetroArch's public lobby, filtered to games in your library (**Y**
  refreshes). A LAN host now always starts RetroArch with the public announce
  and relay switched off, whatever `retroarch.cfg` says. Join by address is
  still there for everything else. Password-protected online rooms are listed
  but must be joined from RetroArch's own menu for now.

- **In-game menu: hold Select + L2 to leave any game.** On a TrimUI, holding
  **Select + L2** for half a second in any game JukaHub launched — RetroArch,
  PPSSPP, a port — pauses it under a small glass menu: **Resume game**,
  **Ask Juka** (with the game assistant on) and **Quit to JukaHub**, which asks
  the emulator to quit the way quitting JukaHub does (SRAM and settings are
  written) and brings Home back, ending one that hangs after six seconds. It
  reuses the game assistant's pad grab and pause, so it needs nothing from the
  emulator. D-pad moves, A chooses, B or the chord again resumes. Settings →
  Controls → **In-game menu** turns it off (`gameMenu`).

- **Paste the Groq key from your phone.** Settings → General → **Groq API
  key** now sits right under Your name, with the weather after it. A opens a
  sheet like the VPN configuration's: **Paste it from your phone** shows a
  full-screen QR code that opens the Phone Companion straight at its Groq key
  card — moved to the top, cursor in the field, with a link to
  console.groq.com/keys — and the code closes itself when the key arrives;
  **Type or paste it here** opens the key field (Paste chip, L1 + Y, Ctrl+V);
  **Get a free key** opens Groq's console on Windows; **Remove the key** asks
  first. Chat's "Add key" and the voice orb's "Add Groq key" land on the same
  sheet (`player/groqkey_sheet.go`, `player/companion_phonecard.go`,
  `player/companion.go`).

- **L2 edits Home.** On Home, in every layout, **L2** (**Q** on a keyboard)
  opens Edit Home and L2 again is Done; the footer says **L2 Edit**. From
  Standard or the Hero grid it switches to Widgets, and leaving without
  changing anything puts the layout back. R2 still steps to the next section
  (`player/shoulders.go`, `player/home_edit.go`).

- **Resize any Home tile, on square cells.** The Widgets layout is now a grid
  of square cells, seven across (160 × 152 design px), and every tile can be
  any size from one cell up to the grid's width and three rows (Continue from
  2 × 2, Media from 2 × 1). In Edit Home, **X** resizes the focused tile: Right
  / Down grow it a cell, Left / Up shrink it, A, B or X finish. With a mouse,
  drag the handle on the tile's bottom-right corner, or click the size tab to
  step through 1×1, 2×1, 2×2, 3×2, 4×2 and 3×3. The default tiles were laid
  out again for seven columns (`player/home_tiles.go`, `player/home_edit.go`,
  `player/home_dynbody.go`).

- **The game assistant is on out of the box, and talks back.** Settings →
  Voice & AI → **Game assistant** now starts on, so on a TrimUI Smart Pro
  holding **Select + R2** in any game JukaHub launched pauses it and Juka
  listens. The answer shows in a wider bubble (up to 12 lines) and is read
  aloud by the new **Read answers aloud in games** switch (on by default,
  independent of Juka's voice). Pressing **Select + R2** again — a press, no
  hold — goes straight back to the game. **Juka can see the game screen**
  stays off until turned on (`player/gameassist*.go`, `player/tts_api.go`,
  `player/voiceorb.go`).

- **Set the date and time by hand.** Settings → Date & time has a new **Set
  date & time** row. On the handheld it opens a sheet — Year, Month, Day, Hour,
  Minute with Left / Right, Y or Set clock to apply — that sets the system
  clock, re-anchors the alarms and turns Set time automatically off so the next
  sync does not undo it. On Windows, macOS and Linux PCs the row (and the
  former read-only "Clock" row, now **Set time automatically** with Windows'
  own On / Off) opens the system's date & time page. Home search finds it under
  "set time", "adjust time" and "auto sync".

- **Bluetooth in every top bar.** A Bluetooth button now sits immediately
  left of the Wi-Fi meter on Home and on every screen: struck through while
  the radio is off, in the accent with a dot either side while a device is
  connected. A, a click or a tap opens the Bluetooth page with the ring on the
  first paired device, and the D-pad reaches the button between the speaker
  and the meter. Its state comes from the kernel — the adapters and live links
  in `/sys/class/bluetooth` and the adapter's powered flag — at most every two
  seconds while the bar is on screen, never from a command. A crowded scene
  bar gives it up after the forecast; on a crowded Home the trophy cup and the
  update offer give way a little sooner
  ([player/statusbar_bluetooth.go](player/statusbar_bluetooth.go),
  [player/bluetooth_quick.go](player/bluetooth_quick.go),
  [player/homelayout.go](player/homelayout.go),
  [player/homefocus.go](player/homefocus.go), [player/focus.go](player/focus.go),
  [player/icondefs.go](player/icondefs.go)).
- **Connect or disconnect a Bluetooth device with one press.** On the
  Bluetooth page A on a paired device connects it or disconnects it, Y opens
  its details and Forget, and X searches. The page opens on the first paired
  device (a connected one first) and searches by itself only when nothing is
  paired; a device that connects or drops on its own — a pad woken with its PS
  button, a headset switched off — shows on its row within about two seconds.
  Until the list has loaded, A on the switch row cannot turn a powered radio
  off by accident
  ([player/bluetooth_devices_ui.go](player/bluetooth_devices_ui.go),
  [player/bluetooth_devices.go](player/bluetooth_devices.go)).
- **Bluetooth starts itself when the firmware left it stopped.** Switching
  Bluetooth on, Connect, Pair and Search first bring the stack up on the
  TrimUI handhelds, a step at a time and only where one is missing: the
  firmware's own bring-up script when the chip is not attached, `bluetoothd`
  when it is not running, an rfkill block, then power. Every step has a time
  limit and runs only the firmware's own programs at fixed paths. A radio
  whose `bluetoothd` is not running reads as off, so the switch offers **Turn
  on** instead of contradicting the error under it; pairing still registers
  its own agent, so a pairing is bonded and kept
  ([player/bluetooth_bringup.go](player/bluetooth_bringup.go),
  [player/bluetooth_bringup_linux.go](player/bluetooth_bringup_linux.go)).
- **The system's own Bluetooth settings, one press from JukaHub on a PC.** On
  Windows and macOS the top bar's Bluetooth button opens a page whose **Open
  Bluetooth settings** row goes to Settings → Bluetooth & devices or the macOS
  Bluetooth pane; a Linux PC without `bluetoothctl` gets the same row for
  GNOME's or Blueman's settings ([player/onboarding.go](player/onboarding.go),
  [player/bluetooth_devices_ui.go](player/bluetooth_devices_ui.go)).
- **The APK Store is one press from Packages.** A **More stores** strip under
  the Packages toolbar — in the rows, grid and list views and in the empty and
  offline states, since none of it needs the package list — opens the APK
  Store (Android games) and PortMaster on the handheld and Windows Apps
  everywhere. Up from the top of a view reaches it, Left and Right pick a
  store, A opens it, and Back from the APK Store lands on its pill again. Home
  search now finds the three by what they hold ("android", "apk", "play
  store", "wine", "ports") ([player/pkgstores.go](player/pkgstores.go),
  [player/pkginput.go](player/pkginput.go), [player/pkgui.go](player/pkgui.go),
  [player/homesearch_index.go](player/homesearch_index.go)).
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

- **Fuzz tests for the updater's decoders.** Go native fuzz targets cover delta
  patches, delta bundles, bundle file names and the signed `release.json`
  (`player/internal/jdelta`, `player/internal/releasesign`). Plain `go test`
  runs their seeds; the weekly workflow fuzzes each for 20 s and uploads any
  crashing input. (OpenSSF Scorecard `Fuzzing`.)

### Changed

- **No more buttons the handheld does not have** (Juka, 2026-11-22: "I see
  some spots on the os where it says r3 as a button, which is not included on
  trimui smart pro...or many other devices... reconfigure menu
  appropriately...make sure buttons for the most part are consistent"). The
  original Smart Pro's sticks do not press in (only the Smart Pro S and the
  Brick Pro click theirs; the Brick and Hammer have no sticks and send their
  F1 / F2 keys as L3 / R3), yet the Games library put the random pick on R3 and
  the tag filter on L3, the browser link mode on L3 and Site info on R3, the
  music player lyrics on L3 and the sleep timer on R3, the light gun's
  re-centre on R3, and Game hotkeys gave the Smart Pro "L3 · R3". No JukaHub
  screen needs a stick click now (`stickclicks.go`, `deviceHW.ClickSticks`):
  - **Games library: Select opens the library's menu** (M on a keyboard,
    `game_library_menu.go`): *Random game from* the console on screen or the
    whole library (rolled at once), *Show only a tag…*, *Search*, *Fill missing
    box art* / *Stop*, *Rescan the card* and, on the consoles grid, *Next
    console look*, each naming the button that also does it. It opens again on
    the row used last, so **Select**, **A** rolls another random game (the toast
    says so). The Menu key still goes back like B. The footer shows
    **Select Menu** instead of "R / R3 Random" and "L3 / T Tags".
  - **Browser:** link mode and Site info are the Select menu's rows (Tab and I
    on a keyboard); the toasts name B (Esc) to leave link mode instead of
    "Tab / L3".
  - **Music player: Start opens its options** — the Audio panel with new
    *Now playing* rows on top: the full-screen visualizer, lyrics, the sleep
    timer, shuffle, repeat, speed, auto-exit and song search
    (`music_nowplaying_rows.go`). Start used to open the Start menu there (the
    player never claimed it, so the documented Start → visualizer never ran);
    Start or B closes the panel, and over the full-screen visualizer Start
    puts it away. The iPod layout's Start now plays and pauses, as its docs
    said; its lyrics and sleep timer stay on X's Now Playing menu.
  - **Light gun:** the R shoulder that does not fire (R2 when R1 fires, R1 when
    R2 does) re-centres the crosshair, in the game and on the practice target;
    R3 still does where the sticks click.
  - **Game hotkeys** (rewind / fast forward on one button) are offered only
    where the sticks click or a Brick's F1 / F2 exist: on the original Smart
    Pro the row is gone and the in-game menu no longer names L3 / R3.
  - **Lists that let you pick a button** leave L3 / R3 out where nothing sends
    them and call them F1 / F2 on a Brick: RetroArch hotkeys' Hotkey button and
    shortcut buttons, the one-handed Chord button (a stored L3 falls back to
    L1), the controller test's chips, and the DS stylus chart's "L3 Closes the
    lid" (its tap is named R2, not "R2 or R3").
  - **A Brick's F1 / F2 do the same on every screen:** the browser and the
    music player no longer keep them as L3 / R3; *Nothing* means nothing.
- **Hints name the keys in your hands on every screen.** Several footers still
  named keyboard keys on a pad: the music player's two lines ("ENTER Play
  SPACE Pause … T Sleep … L Lyrics") now list the pad's buttons (A Play, X
  Pause, Left/Right Seek, L1/R1 Track, Y Favorite, Start Options, Select Screen
  off, L2 EQ, R2 Effects, B Back) and the keys only on a keyboard; so do the
  StockOS line, the visualizer's (Start / V Close, X / Space Pause), the
  equaliser's (B / G Close), the mini-games (Start / R Restart, B / Q Quit, X /
  F Flag), the dictionary, the IP streams and podcasts panels (Start / Del
  Remove in every width) and Chat (V Voice clip on a keyboard only). The
  library's toasts name the search, art and back buttons in the same words;
  the Controller card shows the music player's real buttons and drops "F1
  Keyboard shortcuts" on a handheld without a keyboard. Pad names are spelt
  one way: L1/R1, Left/Right. Not run on a device yet (BUTTONS-HW-1).
- **Last boot says "Kernel to Home" on the stock firmware.** Settings →
  JukaHub OS → Last boot used to read "Power to Home" where the bootloader
  leaves no timings, but the number starts at the kernel: on one Smart Pro
  about 3.8 s of bootloader came before it. It says "Power to Home" only where
  U-Boot records its own share (the JukaHub OS cards).
- **Calculator: a real scientific calculator** (Juka, 2026-11-20: "Improve
  Calculator"). A new expression engine reads the line with the usual rules:
  `2^3^2` is 512, `−2²` is −4, `2+−3`, `5×−(2)` and `2(3)`, `2π`, `2sin30`
  work, brackets left open are closed by **=**, and `200 + 10%` is 220 the way
  phone and desktop calculators add a percentage. Answers show twelve
  significant digits (0.1 + 0.2 is 0.3, sin 180° is 0, large and tiny values
  as 1.5E+15), the big line groups thousands, and an error says why ("Can't
  divide by 0", "Not defined", "Too big", "Syntax error") while the sum stays
  on the line to fix. New keys: **2nd** (sin⁻¹ cos⁻¹ tan⁻¹, eˣ, 10ˣ, x³, ³√,
  ʸ√x), **DEG/RAD** (degrees by default), ln, log, n! (Γ between whole
  numbers), %, π, e and **Ans**; a faint "= …" previews the answer while
  typing. The keypad fits the Brick's and the Smart Pro's screens and any PC
  window. The **history** keeps 30 sums across restarts (`calculator.json`
  beside JukaHub) and the D-pad walks into it from the right-hand keys: **A**
  uses an answer, **X** puts its sum back to edit; a tap does the same. The
  **graph** labels its y range, draws a thicker line and spans ±360° in
  degrees. On a **keyboard** sums are typed directly (digits, operators,
  brackets and names like `sqrt` or `sin`), **Enter** or **=** answers,
  **Backspace** deletes a whole `sin(`, **Del** clears, **Ctrl+C / Ctrl+V**
  copy the answer and paste a sum; typing **q** or **e** there no longer
  switches screens. The Terminal's `calc` command uses the same engine.

- **JukaHub's own PPSSPP build is PPSSPP 1.20.3, with no X11** (Juka,
  2026-11-20: "trimui doesn't have x11...use 1.20.3 which exists for Trimui
  for PPSSPP"). The `PPSSPP for TrimUI (tg5040)` workflow chased the newest
  upstream release that still builds with SDL2 (1.20.4) every day and failed
  every day: the SDL2 it builds only to link against looked for X11 (Mesa's
  development packages pull `libx11-dev` in) and stopped at a missing
  `Xext.h`. That SDL2 is now configured with every desktop back end off (X11,
  Wayland, KMS/DRM, Vulkan, desktop GL, PulseAudio/PipeWire/JACK, D-Bus) and
  the build stops if X11 or Wayland still reaches `SDL_config.h`; PPSSPP runs
  on the firmware's own SDL2 as before. The workflow builds 1.20.3 — the
  version TrimUI builds exist for, the one minui-psp packages — and runs only
  when its files change (a push always rebuilds) or on a press (Actions → Run,
  which can name another tag); the daily schedule is gone. On the handheld nothing changes for PSP
  games: with both sources on 1.20.3, JukaHub keeps minui-psp's unless that
  one is missing or did not start here
  (`.github/workflows/ppsspp-trimui.yml`, `player/native/build-ppsspp.sh`).

- **Downloads panel avoids repeated queue sorting** (Juka, 2026-11-19).
  Each redraw now shares one media and browser download snapshot across the
  header, tabs and rows, cutting seven queue sorts to two per frame. The full
  size panel also skips formatting a status line it does not show.
  (`player/downloadsui.go`)

- **More RAM returns when picture-heavy screens close** (Juka, 2026-11-19).
  Media now releases its built card lists, library snapshot, and the thumbnail
  images it used after its exit fade. The Ports and Homebrew stores release
  screenshot textures on exit and cap decoded screenshots at 8 MB each on a
  handheld (24 MB on desktop). The shared thumbnail texture cache now also
  has an entry ceiling, so very small images cannot accumulate unlimited GPU
  objects. A long Media session keeps tracking images still in memory so exit
  can release them. Chat releases decoded pictures, avatars and message
  layouts when it closes; a picture still open in the viewer stays until that
  viewer closes.
  (`player/media_hub.go`, `media_hub_render.go`, `chatstate.go`, `chat.go`,
  `imageviewer.go`,
  `ports_store_images.go`, `homebrew_store_images.go`, `thumbstore.go`,
  `core.go`)

- **Browser releases page memory when closed** (Juka, 2026-11-19). Leaving
  Browser stops unfinished page, stylesheet and image requests. After the exit
  fade, it frees parsed pages, page text, downloaded image bytes, browser image
  textures, temporary page searches and site-info reports, then returns unused
  heap pages to the system for apps such as offline AI. Reopening refetches the
  last address; URL history, bookmarks, cookies, downloads and saved site data
  remain. (`player/browser_memory.go`, `textbrowser.go`, `webpage.go`,
  `webpage_image.go`, `thumbstore.go`, `browser.go`, `core.go`; docs/LOCAL_AI.md)

- **Pointers hide after 10 seconds unused** (Juka, 2026-11-19: "Mouse should
  auto hide when not used after 10 seconds"). The right-stick pointer is put
  away after 10 seconds instead of 30. Mouse mode's pointer, which never timed
  out, now hides where it is after 10 seconds without a move, click or scroll
  while mouse mode stays on: the next D-pad or stick move, or **L2**/**R2**,
  brings it back at the same spot, the first **A** only brings it back so
  nothing is clicked unseen, and a panel that closes meanwhile leaves it
  hidden. A real mouse's cursor (over the PC window, or a USB or Bluetooth
  mouse on a handheld) hides the same way, also while the address bar's
  keyboard is open, and comes back on the next move or click; the wheel alone
  does not bring it back. The games mouse (**Mouse in games and apps**) is
  unchanged: the game draws that pointer. (`player/mousehide.go`,
  `virtualcursor.go`, `vcursor_mode.go`, `core.go`; docs/CONTROLS.md,
  docs/CONFIGURATION.md)

- **The dead-code scan now runs with `make check`** (2026-11-19).
  Three test-only wrappers were removed so tests exercise the production
  functions directly; the remaining test seams explain their purpose.

- **Quick start in Help now guides players through JukaHub** (2026-11-19).
  It covers first start, Home, games, Settings and where to find controls.
  The build steps embed `docs/GETTING_STARTED.md`; `docs/QUICK_START.md`
  remains the developer guide.

- **The Patch package menu only lists real choices** (Juka, 2026-11-19).
  Rows and menus repeated each other: a package that was not installed opened
  a menu whose only entry was **Install** (the row already said **A Install**),
  and every menu ended in a **Cancel** row that did nothing B does not already
  do. **A** on a package that is not installed now installs it, straight to the
  progress bar, and an installed package's menu is just **Update**/**Reinstall**
  and **Remove**, closed with B, Escape or a click outside the card. Choosing
  **Install** used to build a confirmation modal and confirm it in the same
  breath, so it was never drawn; it now runs the install directly. The menu's
  keyboard and controller handling share one focus mover and one close path
  instead of repeating them in four places (`player/patch.go`,
  `docs/PACKAGES.md`).

- **The Browser is JukaHub's built-in browser again, on every device — no WPE**
  (Juka, 2026-11-19: "For browser, do not use WPE... use the built in that we
  have always... especially on TrimuiSmartPro"). The 2026-11-18 change that
  made Browser entries open WPE Browser on a handheld where it was installed
  (and the desktop's browser on a PC) is undone: Home, Start, chat links and
  Wi-Fi sign-in pages always open the built-in browser, and Settings → Browser
  no longer has **Open pages with** (a saved `browserRenderMode` is ignored).
  JukaHub no longer starts, offers, installs or updates WPE Browser at all: the
  Select menu's **Open in WPE Browser** / **Get WPE Browser** row, the Google
  card's WPE buttons and Settings → Browser → **WPE Browser** went with
  `brw_wpe.go`, `brw_wpe_install.go` and `browser_full_engine.go`. The menu row
  is now **Open on your phone** on a handheld (the page's address as a
  full-screen QR code) and stays **Open in web browser** on a PC
  ([player/brw_elsewhere.go](player/brw_elsewhere.go),
  [docs/BROWSER.md](docs/BROWSER.md#taking-a-page-elsewhere)). A WPE Browser
  already in `Apps/WPE` stays on the card untouched; JukaHub just never opens it.

- **Cheats work for NextUI games too** (Juka, 2026-11-18: "any cheat support,
  and is it compatible with the Brick Pro?"). Game options → Cheats used to
  apply only when a game starts in RetroArch, so on a NextUI card (the Brick,
  Brick Pro and Smart Pro all run NextUI's tg5040 build) the switched-on cheats
  were silently not used: a NextUI pak's `launch.sh` names a `_libretro.so`,
  passed for RetroArch, and the file went to a RetroArch folder minarch never
  reads. A game that starts through a NextUI pak now gets
  `Cheats/<TAG>/<ROM file>.cht` on the card, where minarch looks (TAG worked
  out as minarch does), written in the database's own layout with the
  switched-on cheats first (older NextUI handed PCSX-ReARMed only those, at
  their numbers) and removed after the game like the RetroArch one (and as
  JukaHub quits, so a power off minarch asks for cannot leave it for a NextUI
  launch after the next power-on); a cheat
  file of the user's own there (also under the older name without the ROM's
  extension) wins, and the sheet says so. Nothing in `retroarch.cfg` changes
  for such a launch, and NextUI's own Cheats folders are never removed. The
  game also lists them in NextUI's menu → Options → Cheats. Brick Pro: the
  RetroArch path was already the same on every TrimUI model.
  `player/cheats.go`, `cheats_db.go`, `cheats_ui.go`, `game_options_ui.go`.

- **Boot as fast as possible: no boot animation and no stock menu by default**
  (Juka, 2026-11-18: "we need it as fast as possible"). The boot animation now
  defaults to **None** on every device and the PC, so Home appears as soon as
  it is ready and the startup chime plays over it; a style you picked (or
  Startup animation: Full every time) still plays. On stock firmware **Start
  JukaHub at login** now skips the stock menu by default (Settings → General →
  **Skip the stock menu at boot**): the one-boot stand-in JukaHub OS used goes
  over MainUI, so the firmware starts JukaHub instead of opening the menu,
  waiting for it to settle and closing it, which cost several seconds every
  boot. A start that hangs still makes the next boot stay on the stock menu.
  The boot hook and `launch.sh` start far fewer processes and no longer sync
  first. On the Smart Pro image JukaHub starts while the display and sound
  drivers are still loading and waits only right before it opens the screen;
  both images raise the SD card's read-ahead to 1 MB (`READAHEAD_KB=0` in
  `boot.env` keeps the kernel's). `player/bootstyles.go`, `bootchime.go`,
  `bootos.go`, `autostart.go`, `launch.sh`, `startup_displaywait.go`,
  `setmodel.go`; `os/smartpro/init`, `os/smartpro/launch.sh`,
  `os/plastron/launch.sh`.

- **"Which emulator?" asks whenever a game has more than one** (2026-11-18).
  Installed cores and a folder's other launchers now count, so a game with one
  launcher and an installed core asks. New setting Settings → Games &
  emulators → **Ask which emulator**: First time per game (the default — A
  plays and remembers it for that game, Y plays just once), Every launch (A
  plays, Y always uses it), or Never (the one used last, else the recommended
  one). X still sets the whole console. Save-state starts, the power-on resume,
  save-sync relaunches and the game switcher never ask. Not yet built.
- **Audit toolkit installs what its tools need on the handheld** (2026-11-18).
  On a TrimUI, installing sqlmap or Nikto now installs Ubuntu's Python 3 or Perl
  (with the TLS modules Nikto uses) first, on the same install bar, instead of
  refusing; Nmap, John the Ripper and Aircrack-ng (analysis only — the Wi-Fi
  chip has no monitor mode) can now be installed there too, through Linux
  packages. Not yet built.
- **Logs hide the exact secrets kept in JukaHub's settings** (2026-11-18,
  backlog SEC-31). Besides the `password=`/`token=`/Bearer patterns, every log
  line (Logs screen, logs.txt, errors.txt, bug reports) now has the exact values
  of the secrets kept in JukaHub's settings removed, also URL-encoded: the Groq
  key, the Discord token, other settings named as credentials, mail and calendar
  passwords (app passwords included) and secret calendar links. The list is
  rebuilt when the settings are loaded or saved; Steam, Drive,
  RetroAchievements, RomM, PSN, Wi-Fi and VPN secrets live in their own stores
  and are not in it. Not yet built.
- **Dates, numbers and sizes follow your language and clock** (2026-11-18,
  backlog I18N-2). Dates on every screen, the Home clock, calendar and weather
  widgets included, follow Settings → Date & time's order and 12-hour clock, and
  in Spanish, Portuguese, French, German, Italian and Russian the month and
  weekday names, "Today"/"Yesterday" and number separators follow the language
  ("lunes, 9 de noviembre de 2026", "12 345"), switching at once. Sizes read one
  way everywhere ("512 B", "1.5 MB", "3.0 GB"): screens that said MiB now say
  MB, still counted in 1024s. Mail, the calendars, the Ports store and Steam
  keep English dates for now. Not yet built.
- **Confirmations open on the safe answer** (2026-11-18, backlog UX-14). Every
  card that asks before deleting, forgetting, resetting, uninstalling or
  removing something now opens with Cancel (or Keep) focused, so an A pressed by
  habit changes nothing. Media's "Remove this download?" and a game's "Reset
  settings" and "Clean up covers", which opened on the destructive answer, are
  fixed, and the Browser row in Security & trust opens on Close instead of
  Forget cookies. The Home power card keeps Exit focused, the one exception. Not
  yet built.
- **Radio switches refresh the network status at once** (2026-11-18, backlog
  NET-19). Turning Wi-Fi or airplane mode on or off from Quick Settings, the
  Home Wi-Fi tile, a voice command, the Settings row or the hotspot now counts
  as a network change, as the Wi-Fi page's own actions already did: the old
  "online" or "offline" gives way to "checking…" at once and the connectivity
  probe runs the moment the switch ends, instead of 5–20 seconds later. Not yet
  built.
- **Automatic box art reuses the library's scan** (2026-11-18, backlog LIB-19).
  A background box-art pass now takes the games from the Games Library's last
  scan when the ROM folders have not changed, instead of walking the card again;
  the tour's Fetch game artwork asks only about games without a cover and
  remembers "not found" answers; a long fetch saves those answers every 50
  games; and on a handheld where JukaHub keeps the time, the very first pass
  waits up to half an hour for network time to set the clock. Not yet built.
- **Dead code behind the test seams removed** (2026-11-18, backlog ARCH-9).
  Twenty functions that only tests still called are gone — the
  authenticated-encryption wrapper, the old update-manifest path, a second
  config saver and several download and probe wrappers — and their tests now
  exercise the production functions instead. `tests/scripts/deadcode-scan.py`
  lists what only tests call and fails on any function whose comment does not
  say why a test needs it (five remain, so it is not in `make check` yet). Not
  yet built.
- **The chime reference plays the boot styles' knocks** (2026-11-18, backlog
  SND-17). `branding/sound/chime_reference.c` now renders the Cinematic and Roll
  power-on chimes as the device plays them: the startup chime pushed back behind
  the style's entrance plus a knock on every landing, the same voice as
  `chimeThudNote` in `bootchime.go`. `./chime cinematic … [lift]` and `./chime
  roll … [WIDTH HEIGHT]` print each knock's time, pitch and level; the knocks
  leave the peak under the start chime's, so the gain is unchanged. Not yet
  built.
- **FTP keeps every folder that holds programs read-only** (2026-11-17, backlog
  SEC-17). FTP and the Phone Companion's Files card also refuse changes, even
  through a symlink, to the ROM folders of systems whose launcher runs the game
  itself (script and app systems), to Roms/PC (programs run through Wine) and to
  profile folders added while the server runs. PC games can no longer be copied
  over FTP: use SFTP or USB. Not yet built.
- **Save-state pictures for every library game** (2026-11-17, backlog LIB-11).
  RetroArch games started from JukaHub now always save a picture beside each
  save state, for that launch only (your own RetroArch setting is back when the
  game exits), even with the game switcher off, so the game page's states strip
  shows each state. Not yet built.
- **Test-only functions marked** (2026-11-17, backlog ARCH-8). A scan of the
  player found 61 functions that only tests call; 38 now carry a test-seam
  comment, the rest were already named seams, interface methods or in files left
  alone. Retiring the dead ones is ARCH-9. Nothing changes for users. Not yet
  built.
- **Tabs Home layout** (2026-11-17). The default grid now uses larger,
  centered cards on sparse pages, with more room for names and game counts;
  full grids still scroll and Compact stays dense. Collections gains a short
  explanation of automatic lists and a clearer New collection caption. Cover
  labels have a solid theme-aware backing, and the Store caption fits its card.
- **PS Remote Play: Play no longer freezes the panel** (2026-11-17, backlog
  PSR-5, PSR-9). Play now says "Starting the stream…" on the console's row
  while JukaHub checks, in the background, that the stream helper runs here
  and, for a console with an Internet address, whether it is at home; B stops
  it. Before, up to 2 s of helper test and 0.7 s of home probe ran on the event
  thread. A console that had to be woken streams to the address its wake found.
  Not yet built.
- **Core updates on a full CrossMix card** (2026-11-17, backlog GAME-14,
  LIB-20). On the handheld an updated core is no longer also kept in the core
  pool (the `.bak` beside it is the way back); the checksum cache survives a
  restart (`data/performance/core-crc.json`, at most 512 entries), so the first
  pass after boot no longer re-reads every core; MAME and FinalBurn Neo cores
  are never updated by the automatic pass (a press still can); a build CrossMix
  ships is named once instead of replaced (its checksum table is still empty:
  GAME-20); resetting `coreUpdateMode` in the config inspector also resets
  `emuAutoUpdate` and `coreUpdateModeChosen`. A PC's automatic pass now shares
  the core-update loop through a filter. Not yet built.
- **Runtime updates show their progress** (2026-11-17, backlog LIB-21). The
  daily pass's Windows-app runtime and AndroidRT installs drive the install
  card with phases and a byte bar; on Notify me a new AndroidRT appears on the
  APK Store's Runtime tab, and an automatic install tells the store. Builds are
  labelled with the release tag plus the first 8 characters of the asset
  digest, so a pack re-uploaded under the same tag is seen as new; an old
  tag-only record is upgraded quietly, with no repeat install. Not yet built.
- **Game Profiles start games in the background** (2026-11-17, backlog
  AND-19). Every runtime but Wine starts off the render thread with its start
  claimed first; the row reads "Starting…", and A on another row waits for the
  first game. An Android profile with AndroidRT missing offers "Install and
  play". Not yet built.
- **Smoother moving backgrounds** (2026-11-17, backlog PERF-19). The Weather
  page's rain and snow ask for full-rate frames (the sun one every 66 ms), the
  living background draws at sub-pixel positions, and wallpapers are created
  with linear filtering (the water's one-off conversion is gone). Not yet
  built.

- **Tabs Home action tiles.** Experience packs accept `homeLayout: "tabs"`,
  and Xbox Remote Play and Steam resolve through the Start menu from
  favourites, collections and Home search.

- **Maintainer checks.** The backlog now validates its task count, references
  and anchors in `make check`; CI checks Go formatting, and weekly checks cover
  Windows `go vet` plus input and download cases under the race detector.
  `docs/BUILDING.md` explains how to refresh the optional local vendor snapshot.

- **Screenshots show where they were saved and stay in JukaHub's folder**
  (Juka, 2026-11-17: "the handheld should save to JukaHub/screenshots"). The
  preview card shows the file's path, in the menus and after a game. Shots go
  to `Apps/JukaHub/screenshots` on the handheld (was `/mnt/SDCARD/Screenshots`)
  and to `screenshots` beside `JukaHub.exe` on a PC (was `Pictures\JukaHub`,
  often synced to OneDrive). When that folder is read-only the old places are
  the fallback; `JUKAHUB_SCREENSHOT_DIR` overrides all of them. The wallpaper
  picker lists the new folder.
- **Cover art matching is much less strict** (Juka: "better to get more pics
  than less"). A lower bar for the closest title; a title clearly ahead of the
  rest is taken even below it; a sequel's box stands in for another number of
  the series; sister consoles' lists are searched (Game Boy/Color, Master
  System/Game Gear, Mega Drive/Sega CD/32X, Neo Geo/CD/FBNeo, …); a game with
  no box art gets its title screen or an in-game picture. Earlier "not found"
  answers are retried at once and then every 14 days instead of 30.

- **Faster offline voice on the TrimUI handhelds** (Juka, 2026-11-17: "text
  to speech needs to be faster … the device is only 1 GB"). Piper now speaks
  with its 16 kHz Lessac voice instead of the 22 kHz Kristin: the same size
  of network, about a quarter less work per second of speech, so on the A53
  it keeps ahead of what it says. A handheld with Kristin keeps her until it
  is next online, then swaps by itself (about 60 MB, no card unless it
  works; Kristin is deleted only after Lessac has said a word); Lessac's
  SHA-256 and size are read from the Git LFS pointer of the same pinned
  piper-voices commit, and a first install that cannot read it gets Kristin
  as before. The first piece Juka says is now at most about six words (cut at
  a comma when it can; a first sentence of up to ten words stays whole) and
  the next lines grow from 56 to 80 bytes, so the first word comes sooner
  without gaps after it; the log notes how long the first sound took.
  The optional natural voice (Phonon) is gone — its code, its Settings row,
  its CI build (`offline-voice.yml`, `build-voice.sh`, `package-voice.sh`)
  and the `JukaHub-Voice-linux-arm64.zip` release asset; a handheld that
  downloaded it deletes `required/juka-voice/` (about 70 MB) at the next
  start. Offline Whisper (languages other than English) is now tiny with
  8-bit weights, quantized by `build-stt.sh` from the pinned model (about
  44 MB instead of 78, which also shrinks the speech package to about
  90 MB); an installed package with the full model keeps working and is
  replaced once when a newer release is out. Whisper decodes greedily for
  commands (`-bs 1`, was 3) and with two beams for dictation (was five), on
  three cores for both. Every model is now the lightest of its kind: Vosk
  small for English, Whisper tiny 8-bit, Piper "low" Lessac, SmolLM2-135M
  Q4_0 (`player/tts_install.go`, `tts_text.go`, `stt_local.go`,
  `stt_install.go`, `player/native/build-stt.sh`, `package-stt.sh`,
  `player/package-sdcard.sh`). A failed automatic replacement of a working
  speech package or voice shows no card and is not retried by the same
  JukaHub version (Settings still can).

- **JukaOS v2 defaults: the Tabs Home and the Cinematic boot** (Juka,
  2026-11-16). A setup that never saved a Home layout (`homeArrangement`
  unset) now opens on the Tabs Home; a layout you picked before — Standard,
  Hero grid, Carousel, Widgets or a console theme — is kept, and Settings →
  Display & theme → Home layout switches back. The boot animation defaults to
  Cinematic for everyone; Classic, the previous look, is one row away in
  Settings → General → Boot animation. Where JukaHub names a game's emulator
  without asking (no pin, no last choice: save states, the diagnosis, Reset
  game), it now assumes the console's recommended one rather than the
  catalog's first match.

- **Juka's offline voice answers faster** (Juka, 2026-11-16). Short everyday
  lines ("Going Home", "Taking a screenshot", the launch greeting) are kept
  as WAVs in the data folder's `voice-cache/` the first time Groq's voice or
  Piper says them and play from the file afterwards: no request, no Piper,
  no wait (`player/tts_cache.go`; at most 160 files / 12 MB, least recently
  heard go first; Hear Juka still asks the voice). While Piper sits loaded
  after a conversation it makes the common voice-command answers in the
  background, one per second, without the CPU boost; a line that wants Piper
  meanwhile raises the CPU and waits for that one instead of loading a second
  Piper. Piper now lets go 10 s after the conversation ends (was 45 s after
  its last line; the orb being up counts as the conversation, so a slow
  offline answer never finds it unloaded; Chat and the Voice command page
  keep 45 s), runs on three of the handheld's four cores so the
  interface and Hey Juka keep one (`player/tts_cpu_linux.go`; Piper has no
  thread option, so its threads are pinned), and writes its per-sentence
  WAVs to /dev/shm (memory) when it can.

- **Chat uses the device name when Your name is empty.** Messages and the
  rename chip now show "{Device name}'s user" in place of "You". Renaming the
  device updates that default; a name entered in Settings or Chat still wins.

- **The Games library fills the screen, with a button for every console.**
  The old card of three narrow columns (a console list, a game list and a
  preview) is now a full-screen page in Home's glass style under the top bar.
  It opens on a grid of console buttons, Continue first: each carries the
  system's icon, its name, its game count, a cover meter and the first cover
  it has (its name in the library's pixel letters until there is one; covers
  load one at a time in the background). A or a click opens a console's page
  — its games as a list, 3D shelf or cover column next to a large box-art or
  video-snap preview, with that console's cover ring in the header — and B
  goes back to the buttons, then clears a search or tag, then closes. A search
  or tag shows the consoles with hits, counting matches on each button, or
  opens the only console that has any (Juka, 2026-11-15: "the list doesn't
  fit the whole look of JukaHub"). A game's actions (Options, LAN Play, E)
  live on a console's page; with a mouse, the page title goes back and the
  top bar closes. `player/game_library_ui.go`, `player/game_library_pixel.go`,
  docs/GAME_LIBRARY.md.

- **RetroArch is searched for, not just looked up.** When RetroArch is not in
  RetroArch/ or Apps/RetroArch/, JukaHub now also looks in more folders on
  every card (Emus/RetroArch, Emus/RA, System/RetroArch, RetroArch64…) and
  the firmware's, at a RetroArch that is running, at the paths the firmware's
  launch scripts name (folder variables and `$(dirname "$0")` filled in,
  CrossMix's shared launcher scripts included), and finally walks each card
  three folders deep, skipping ROMs, art and saves. Any RetroArch build name
  counts (ra64.<model>, retroarch64), and only a real program, never a wrapper
  script. The Emulators page's RetroArch tile, core installs, netplay, the
  launch card and RetroArch's settings all use it; the log says where it was
  found, or every place searched (`player/rafind.go`).

- **The release no longer waits for the offline AI build.** llama.cpp and the
  SmolLM2 model now build in their own workflow (`offline-ai.yml`) on their
  own runner, and `JukaHub-OfflineAI-linux-arm64.zip` is attached to the
  release when it is ready, like the Box64 pack. A release that goes out
  without it makes the Smart Pro install the model from the newest release
  that has one.
- **Offline voice commands are faster.** The Voice command page, voice
  shortcuts and English dictation use the speech model that is already
  loaded instead of loading it again for every clip, and English recognition
  works with less free memory.
- **Juka's offline voice no longer goes quiet between sentences.** Long
  answers are spoken sentence by sentence as Piper renders them, and a short
  opening like "Sure." plays at once.

- **Offline speech answers right after you stop talking.** The Smart Pro now
  recognises English while you speak (a warm Vosk recogniser fed as the
  microphone records) instead of loading the model after the recording; the
  orb shows your words as you say them and ends sooner when the sentence is
  clearly over. Older speech packages update once by themselves on Wi-Fi.

- **Offline answers start in seconds.** The offline model (llama-server) loads
  while you are still talking, keeps Juka's instructions cached, streams words
  into the bubble and stops after a minute idle, at once when a game starts or
  memory runs low; it listens on the handheld only (random port and key).
  llama-cli stays the fallback, now on 4 cores. Piper stays loaded between
  lines, so replies like "Opening YouTube" are spoken sooner. The CPU runs at
  full speed while the assistant listens, thinks and speaks, and returns to
  normal right after (never during a game). Old offline-AI packages update
  once on Wi-Fi, or from Settings → Offline answers → Update.

- **JukaHub's own PPSSPP for TrimUI is built automatically.** The
  `ppsspp-tg5040` workflow now runs daily and when its build script changes, building
  the newest upstream PPSSPP that still uses SDL2 (1.21 moved to SDL3, which
  the TrimUI firmware lacks, so 1.20.x is the newest that runs there for now).

- **Offline speech is no longer inside JukaHub-Trimui.zip.** The Smart Pro's
  speech recognizer (Vosk + Whisper) made the app download about 120 MB
  bigger. It now ships only as JukaHub-OfflineSpeech-linux-arm64.zip: once
  ffmpeg is installed, a Smart Pro on Wi-Fi downloads and verifies it by itself
  (before the offline voice, which waits for it), quietly unless it fails;
  Settings → Voice & AI → Install offline speech still starts it by hand.
  Nothing is fetched while speech recognition is set to Groq only, when the
  card has under ~490 MB free, or after the recognizer failed to start on that
  firmware (until the row is pressed). Devices that already have the bundled
  copy keep using it — updates never delete it. Voice pages say "downloading"
  instead of "missing" while it is on its way. release.sh and the TrimUI
  release job stop copying juka-whisper/ into the archives (the release job
  still fails without a verified speech package).

- **The liquid background is off by default.** A setup that never chose a
  level has no water until R2, Settings → Display & theme → Liquid background
  or Start → Device switches it on (the first switch-on picks Medium). R2 no
  longer steps Home to the next section (E still does on a keyboard) or jumps
  to the last row of a plain list (L2 still jumps to the first).
- **HTML5 games run at full speed on the handheld.** The built-in game view
  was drawing the Home screen under every game frame, ran at the menus' 24–30
  fps on the powersave clock, and replayed every canvas call onto the screen
  each frame, so games that move by the frame or cap their time step crawled.
  Now an open game gets the same CPU performance profile an emulator launch
  gets (put back when you press B), a 60 fps frame budget, and the panel to
  itself; the canvas is kept in a texture that only the new drawing goes into,
  shapes are filled as triangles on the GPU and lines are batched; and the
  gamepad, timers, input events and score line no longer go through JSON
  every frame. Along the way: `cancelAnimationFrame` and `clearInterval`
  inside a callback work, `arc` honours anticlockwise, `moveTo` starts a new
  shape, `clearRect` leaves the canvas transparent, a see-through overlay no
  longer erases the picture under it, and key events carry `code`/`keyCode`.
  (`html5_inapp.go`, `html5_inapp_render.go`, `main.go`.)

- **The SDL DLLs are no longer kept in git.** The Windows release now ships the
  `SDL2.dll`, `SDL2_image.dll` and `SDL2_ttf.dll` from the checksum-verified SDL
  MinGW packages it compiles against (it used to ship 2024 copies from
  `player/`, a different SDL version from its headers, and a 68 MB
  `SDL2_ttf.dll`). `build.bat` already staged the downloaded DLLs for local
  builds. CI installs staticcheck by module version (`@v0.8.2`). (Scorecard
  `Binary-Artifacts`, `Pinned-Dependencies`.)

- **Juka answers out loud sooner.** When you ask the voice orb (or the game
  assistant) a question, Juka starts speaking as soon as the first sentence of
  the answer is written instead of waiting for the whole answer; the rest
  joins the same line as it streams in (`tts_live.go`; Piper on the handheld
  gets each sentence as it comes too). The orb also stops listening after
  0.8 s of quiet instead of 1.2 s, and opens the connection to Groq while you
  are still talking, so the clip goes out without a fresh TLS handshake
  (`voicewarm.go`).

- **Microphone stick lights stay on and move smoothly.** In Lights show →
  Microphone the rings now keep a soft, steady glow in your colour (soft blue
  when none was picked) and brighten smoothly when you talk — including while
  you talk to Juka, whose own capture now feeds them — and a little while
  Juka answers. The colour no longer jumps with pitch, which on the TrimUI
  rings blinked on every syllable. (`ledmic.go` `ledMicColor`.)

- **The Smart Pro's offline voice downloads by itself and is the default
  without a Groq key.** Once the startup tools check has nothing left to fetch
  (ffmpeg first), JukaHub downloads Piper + Kristin once per run when it is
  missing, once no install card or tool download has been busy for 10 s (an
  install that takes the card over makes it wait and continue; a network
  failure retries on the next reconnect, other failures only from Settings).
  With no Groq key and no voice chosen,
  Juka speaks in the offline voice; a one-time toast per run says a Groq key
  (Settings → General) brings Groq's voices. A firmware that cannot load Piper
  is remembered (`required/.juka-piper-unsupported`) so startup stops trying;
  pressing **Download offline voice** checks again. Piper now renders one WAV
  per chunk from a single process (`--output_dir`), so long answers start
  after the first sentence; an engine without that falls back to rendering the
  whole line. (`tts_install.go` ttsAutoInstallOfflineVoice, `netmon.go`
  refreshToolHealth, `tts_settings.go` ttsVoiceImplied, `tts_api.go`,
  `tts_play.go`, `tts_local*.go`.)

- **Box art, emulator updates and PPSSPP on start.** A card's first start now
  looks for box art as soon as the device is online (about twenty seconds in)
  instead of waiting a minute and a half and then for the user to put the
  handheld down; that first pass also no longer waits for the clock to be set
  or for a first boot's helper download, and says "Finding box art for N games
  in the background". The emulator pass (cores, JukaHub's PPSSPP, hakuX, the
  other managed builds, and a PC's managed emulators) and the daily box-art
  pass, when due, now run right after JukaHub starts rather than at the next
  idle minute — still never over a game, an install or a video, and still at
  most once a day (`emuStartWindow`, 15 minutes). JukaHub's PPSSPP now reaches
  the newest minui-psp build on more cards: a release whose notes name no
  PPSSPP version takes the version from the release history, and a firmware
  PPSSPP whose version cannot be read (or none) no longer turns the install
  into a notification; such a build is also switched to, with the firmware's
  PPSSPP as the fallback if it does not start.

- **Sharper, ring-shaped ripples on the Smart Pro.** Under Low Power (the
  handheld's default) the water ran on a 96-column grid with its light
  stretched from a texel every 13 px, and each dip was lit as one bright half
  and one dark half — blurry, big blobs. The grid is now 128 columns there
  too, while the refracting mesh takes every second node (every fourth once
  the budget steps in), so SDL expands a quarter of the vertices it did; the
  light keeps a texel per node. Crests now catch a line of light and troughs
  a line of shadow (curvature shading), with half the slope light and a
  tighter glint, and ripples are about a third smaller
  (`player/liquidbg.go`).
- **Ripples spill the way the focus went, and overlays bounce.** A focus
  arriving from the right now lands its wave on the move: stretched along the
  path, its rim leaning ahead and the water shoved that way, so the spill
  runs on to the left instead of dripping straight down (a step the focus
  ring does not answer leans the same way). The Start menu, dropdowns and
  sheets overshoot further and swing back once before settling, over 210 /
  230 ms instead of 170 / 190 (`player/liquidbg.go`, `player/anim.go`).
- **The liquid background moves like water, not stamped circles.** Ripples
  are now short wave trains (a dip with a raised rim, so two or three crests
  roll out) with a stretched, wobbly outline, and each lands a little off the
  mark and a little larger or smaller than the last. Moving the focus rolls a
  wave along the way it went instead of dropping one ring at the end, and a
  slow swell of three long waves drifts across the whole pool while you use
  it, easing out 25 s after the last touch so the water can rest. Overlapping
  drops round off instead of hitting the height limit (`player/liquidbg.go`).
- **The liquid background is on, and moving around stirs it.** It was off
  unless switched on, so Home showed nothing; a setup that never chose a level
  now gets Gentle (choosing Off still turns it off). Every step through a menu
  ripples the water near the highlight even on screens without a glass focus
  ring, A / Enter / a click splashes under the choice, and B / Escape swells
  the water up under the focus (`player/liquidbg.go` `liquidNoteEvent`, hooked
  in the main event loop in `player/main.go`).
- **The liquid background is easy to see.** The calmer water was hard to see at
  all: bending Home's soft gradients shows next to nothing, the wallpaper's dim
  hid most of the shading, and wide drops made fog instead of rings. The grid
  is now twice as fine (the same vertex count — the mesh is the grid itself),
  drops are rings again, and the light and a real shadow are two small
  textures, a texel per node, that the GPU stretches with linear filtering over
  everything, dim included — no Gouraud staircase on a ring's edge. Rain is
  more frequent, and Reduce Motion (on by default on the handheld) now keeps
  rain at half the rate and 85% ripples instead of none and 60%
  (`player/liquidbg.go`).
- **The liquid background is calm and smooth.** A tester still found the water
  pixelated and not calming after the smoother mesh: its ripples were a node or
  two of the simulation grid across, so they rang as jagged crumbs with
  flickering glints, and they crossed the screen at ~13 px a frame. Waves now
  spread at a quarter of the screen width a second, a little
  viscosity fades the short waves a grid draws badly, drops are at least 2.5
  grid nodes wide, pressed in over four steps and mostly from rest (no
  every-frequency kick, no one-frame pop), rain is rarer and wider, and the
  glint is broad and half as bright. A numerical replay of the Low Power grid
  measured the picture's frame-to-frame change falling from 8.0 to 1.9 per
  channel (`player/liquidbg.go`).
- **A small Home bounce.** Focused cards briefly grow one extra pixel before settling, and the
  scrollable Widgets grid eases up to three pixels past its resting row. Overlays now settle in their
  arrival direction: dropdowns dip a few pixels below their resting point, while rising sheets pass
  slightly above it before returning. Reduced Motion still snaps.
- **Phone Companion shows only the IP address.** The `jukahub.local` name (mDNS) is gone: many
  routers and phones answered it with an NXDOMAIN error. The card, the QR code and every pairing link
  now use the handheld's numeric address (`http://<IP>`, plus `:8317` when port 80 is taken).
- **The VPN's "Send one from your phone" opens the VPN card.** Its QR code
  now opens the companion page at the VPN card (moved to the top, the cursor
  in the text box) and closes itself when the configuration arrives, instead
  of showing the general pairing card (`player/vpn.go`).

- **Alarm editor rebuilt for the pad.** It is one page of rows — Hour,
  Minute, Label, Ringtone, then Save and Delete buttons: Up / Down moves,
  Left / Right changes (held, it repeats), L1 / R1 move the time 10 minutes, Y
  saves from any row, and B asks once before throwing changes away. The label
  is typed on the on-screen keyboard (A on Label, or a tap); before, it only
  took a physical keyboard, so on the handheld it could not be edited at all.
  The page previews the time and says when it rings ("Rings tomorrow · in 9 h
  12 min"); the day-of-week strip, which suggested repeat days alarms do not
  have, is gone.

- **Bluetooth's state comes from the kernel, not from `bluetoothctl show`.**
  The Bluetooth page and the PS Remote Play panel read the radio from the
  adapter's powered flag (one ioctl) and `/sys/class/bluetooth` instead of
  starting `bluetoothctl show` every five seconds, which also sat out its
  three-second timeout whenever `bluetoothd` was down; `bluetoothctl` stays the
  fallback where the kernel cannot be asked
  ([player/bluetooth_devices.go](player/bluetooth_devices.go),
  [player/bluetooth_bringup_linux.go](player/bluetooth_bringup_linux.go)).
- **Quick Settings switches Bluetooth on through the same bring-up.** The
  switch flips at once and the bring-up runs off the render thread, so the
  panel no longer freezes while `bluetoothctl` answers; the switch then shows
  what the radio really did, and a failure says why
  ([player/quicksettings.go](player/quicksettings.go),
  [player/bluetooth_bringup.go](player/bluetooth_bringup.go)).
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

- **Emulator downloads on a Smart Pro no longer all stop at "needs
  GLIBCXX_3.4.30"** (Juka, 2026-11-22, photo of Emulators → ppsspp: "Could not
  install PPSSPP core · the 2026-11-10 build cannot load on this firmware: needs
  GLIBCXX_3.4.30; this firmware's libstdc…", and "every time I try to download
  an emulator it gives me this error"). The buildbot's aarch64 nightlies need a
  newer glibc and libstdc++ than the firmware's, and JukaHub's private glibc
  2.39 is what lets them load. A runtime that came from the pinned fallback
  (GreenOvercast's first install, or a day Ubuntu's signed index could not be
  read) is glibc alone, with no libstdc++ or libgcc_s, yet it counted as
  installed for good: every core built with a newer GCC stayed refused, and the
  message only quoted the firmware's gap. Now a core that needs the C++ runtime
  first replaces a glibc-only runtime with the complete signed one (at install,
  at a game's launch and in the start's core look; a failed try waits two
  minutes, not one per core); a refusal says what stopped JukaHub's C library
  before the firmware's gap (it did not install and why, the glibc-only copy,
  no mount namespace, no 64-bit RetroArch, RetroArch's probe answer, the
  setting on Firmware's only), and the Emulators page no longer rewords it as a
  download error; and when the buildbot's build cannot load, a press installs
  JukaHub's own TrimUI build of the core from the `cores-aarch64` release when
  it has one (mGBA, Gambatte, gpSP, Snes9x, FCEUmm, Nestopia, PicoDrive,
  Genesis Plus GX, PCSX-ReARMed, the Beetle cores, FinalBurn Neo…), which needs
  no newer C library at all; an update leaves a working installed core alone
  (`glibc_runtime.go`, `ra_glibc.go`, `emulatorupdates.go`,
  `emulatorinstall.go`). Not run on a device yet (RAGLIBC-HW-1 step 7); the
  photo's PPSSPP is not one JukaHub builds, so if it is still refused the toast
  now names the cause.
- **A Bluetooth headset gets its sound back when BlueALSA stops answering**
  (Juka, 2026-11-22: "Bluetooth audio is not working still", no log). A
  bluealsa that kept its D-Bus name but stopped answering it was left alone for
  good, so JukaHub's sound and every game stayed on the speaker until a
  reboot; spruceOS found bluez-alsa 3.1 doing exactly that on the same TrimUI
  boards and restarts it. JukaHub now continues a stopped one and restarts a
  silent one after 5 s (three times a run at most), then asks the headset for
  its audio profile again. And a headset that stays connected for 45 s without
  JukaHub's sound now gets one toast saying why — no BlueALSA on the firmware,
  no BlueALSA plugin for ALSA, BlueALSA off D-Bus, silent or exiting at start
  (with its last words), the profile missing, or ALSA's open error — with the
  Bluetooth description written to the log (`audioroute_bluealsa.go`,
  `audioroute.go`). Not run on a device yet (HW-3 step 14).
- **JukaHub builds, and so releases, again.** No release had been published
  since the 2026-11-21 evening build (`JukaHub-20261122000223`): the music
  player's video rows (Jellyfin films and episodes, the card's Videos) set
  and read a `Video` field that `musicItem` did not have, so the "Build and
  Release" workflow stopped at the compiler on both platforms for every push
  after it (tags `JukaHub-20261122033944` and `JukaHub-20261122081333` stayed
  unpublished drafts). Handhelds kept the evening build and never received
  the fixes made since, the Dolphin libusb fix and the hakuX screen hand-off
  among them: a tester's Brick Pro, 2026-11-22, still refused Dolphin for
  libusb and still kept JukaHub running beside an Xbox game. The field is
  added (`player/musicsrc.go`).
- **The menus get every core back after a game.** TrimUI's launch scripts
  (`cpufreq.sh`) park cores for the lighter consoles and set a floor and a
  ceiling of their own, and the launch profile put back only the governor and
  the ceiling it had changed itself: after a GBA game the menus ran on two
  cores with the script's floor until JukaHub restarted. A launch now notes
  each policy's floor and ceiling and every online core before it starts, and
  afterwards puts back what the script moved and brings back the cores it
  parked, never parking one and none while the kernel's thermal limit is
  active (`powerprofile.go`; MENUBOOST-HW-1).
- **Dolphin no longer stops at "libusb-1.0.so.0" on TrimUI handhelds** (Juka,
  2026-11-22: "On Trimui devices (and Trimui Brick Pro) libusb-1.0.so ...make
  sure to fix it and make sure we can run Dolphin on Trimui Smart Pro"). The
  libretro buildbot's Dolphin, which the GameCube setup falls back to while
  JukaHub's own TrimUI build is not published, links libusb and libudev, and
  no TrimUI firmware has them. The morning's fix fetched Ubuntu's copies, but
  only when JukaHub's own updater installed the core, and only after tens of
  megabytes of package index: a Dolphin core RetroArch's own Online Updater
  put in started without them (and on the firmware's C library at that), so
  did one loaded from RetroArch's own menu, and a failed download left the
  build refused. JukaHub now writes those libraries itself: small stand-ins
  (a few KB, no download) that export exactly the functions and symbol
  versions the core imports and answer "no such device" — libusb's "not
  supported", a NULL udev, no Bluetooth adapter — which is all a handheld
  could give Dolphin's USB, GameCube-adapter and Wii Remote code anyway, and
  what JukaHub's own Dolphin build does with its built-in stub. They are made
  sure of when the updater checks a build, before every start of a core that
  links them, at the start-up look at the installed cores, and for
  RetroArch's own menu on JukaHub's glibc, and they are widened when a newer
  build imports more. Ubuntu copies the morning build installed are replaced.
  libevdev gets no stand-in (Dolphin's evdev input needs a udev that works):
  a build that links it is refused with libevdev named alone instead of a
  sentence that starts with libusb (`player/coredevstub.go`,
  `player/coredevlibs.go`, `player/ra_glibc.go`, `player/corefix.go`).
- **Dolphin installs on a Brick Pro when JukaHub's own build is not there**
  (a tester, relayed by Juka, 2026-11-22: "Could not install GameCube games —
  the 2026-11-21 build was refused here before: cannot load on this firmware:
  needs libusb-1.0.so…"). The GameCube setup takes JukaHub's own Dolphin build
  for TrimUI first and falls back to the libretro buildbot's nightly, which
  RetroArch runs on JukaHub's glibc 2.39. That nightly is a desktop Linux
  build: it also links libusb and libudev (Dolphin's GameCube-adapter and Wii
  Remote code), which no TrimUI firmware has, so the load check refused it.
  JukaHub now supplies them in `private-glibc/devlibs/` (Ubuntu's copies at
  first; since the same day its own stand-ins, see above), last on the
  library path of a launch of a core that links them. A build these
  libraries do not complete is still refused, now with every library it
  lacks named (one that also needed X11 would say so instead of installing
  and failing each start), and a refusal of the old kind is tried once more.
  When no source gives a build, the error says why for each, JukaHub's own
  first ("JukaHub's build for TrimUI: no Dolphin build published yet; the
  libretro buildbot: …"), and a press on a Dolphin tile reads JukaHub's list
  fresh rather than an hour-old copy, so a build the cores workflow just
  published is found (`player/coredevlibs.go`, `player/dolphin_setup.go`).
- **JukaHub's own Dolphin build can no longer pick up libudev in CI.** The
  cores workflow links a stub libudev into Dolphin, but the bundled libusb's
  CMake `find_library` could hand the linker the build root's real
  `libudev.so` by full path, which no `-L` order overrides, and the workflow's
  check then refuses a core that needs `libudev.so.1`. The stub is now given
  to CMake as `LIBUDEV_LIBRARY` and the root's `libudev.so` development link is
  removed before the build (`player/native/build-cores.sh`).
- **Chat no longer freezes for seconds on a photo.** A tester's log on
  2026-11-22 showed the screen stalled 5–9 s at each start while Chat (on
  screen) decoded a 4 MB picture on the render thread, pixel by pixel. Chat
  pictures now decode in the background — the bubble fills in a frame later —
  and are shrunk to 2048 pixels on the longer side (a phone photo was 48 MB as
  a texture); every decode through the shared image decoder (Chat, the stores'
  screenshots, artwork, music themes) converts with the standard library's
  fast paths instead of one interface call per pixel (`player/chat.go`,
  `player/bounded.go`).
- **Xbox games no longer stick on JukaHub's screen, and Menu opens hakuX**
  (a tester, relayed by Juka, 2026-11-21: the first game "stuck in the menu"
  with the game's sound playing, and the hotkeys "only opened RetroArch menu
  and not the standalone hakuX emulator"). The hakuX package is a launcher
  made for the stock menu, which quits before a game starts; from JukaHub it
  ran beside a JukaHub that stays alive — its window still on the
  framebuffer, its pad watcher beside the game, its memory taken from a 1 GB
  handheld — and hakuX, which presents through its own Vulkan layer rather
  than SDL's framebuffer path, could leave JukaHub's last picture on the panel
  while Menu met the firmware's quick menu or JukaHub's RetroArch-minded
  in-game layer. An Xbox game now gets the handheld to itself, the way PS
  Remote Play and Moonlight do: JukaHub hands the screen to a small script and
  quits, the script runs `Emus/XBOX/launch.sh` exactly as the stock menu would
  (the firmware's environment, the game in MainUI's form, its own
  interpreter), and launch.sh starts JukaHub again when the game ends — back in
  the Games Library on the game when it was started there. Menu opens hakuX's
  settings and holding Start + Select about 1.2 s quits back. Heavy-game boost
  still applies (cores, GPU and memory clocks and the card's read-ahead before
  JukaHub quits, put back at its next start; the performance governor for the
  game's length), the time played counts in the game's playtime and history,
  and a game that ends within seconds with an error opens the launch card with
  the launcher's last lines (`gamediag/launcher.log`). A JukaHub started by
  hand or under an older launch.sh starts the game beside itself as before
  (`player/hakux_handoff.go`).
- **GameCube and Wii have their folders now** (Juka, 2026-11-21: "I can't find
  GameCube or Wii folders that using dolphin emulator and subfolder for both
  Wii and GameCube inside ROMS file"). Nothing made them: TrimUI's firmware
  has no GameCube or Wii folder, and the Emulators page listed a console's
  folder only once the card had it, and dropped it as soon as the Dolphin core
  was in. On a handheld that plays them (**GameCube & Wii** not Off), the card
  gets `Roms/GC` and `Roms/WII` at start-up and after a Dolphin setup; a
  folder the card already has in another spelling (`GAMECUBE`, `NGC`, `Wii`)
  is used instead, and one you delete stays deleted (`dolphin.json` remembers)
  until you press its tile or turn the setting on. The Emulators page always
  lists a **GameCube** and a **Wii** tile: Dolphin, where the games go and how
  many are there; **A** opens that console's games, or sets Dolphin up while
  it is not installed, and **X** sets it up or looks for a newer build. A
  launcher folder of your own in `Emus` (a standalone Dolphin) keeps its tile.
  Games still start in the Dolphin core from JukaHub; the stock TrimUI menu
  does not list the two consoles (`player/dolphin_folders.go`).
- **Pulling a Go dependency update no longer stops the build** (Juka,
  2026-11-21, passing on Dependabot PRs #63, #64, #66 and #67).
  `player/vendor/` is a git-ignored local copy of the Go modules, so after a
  merged Dependabot PR changed `go.mod`, `build.bat` (and `make test`/`vet`)
  stopped with "inconsistent vendoring" until `go mod vendor` was run by hand.
  They now check the copy with `go list -m` and refresh it when it no longer
  matches. The four open updates are in the tree as well: regexp2 2.13.1,
  gjson 1.20.4 (with tidwall/pretty 1.3.0), golang.org/x/net 0.63.0,
  actions/setup-go 7.1.0, and for the test tools jsdom 31.0.1 and
  @playwright/test 1.65.1.
- **"Can't update the hub, it failed during update"** (Juka, 2026-11-20,
  passing on a report with no log). Update now no longer depends on GitHub's
  API answering at the moment it is pressed. That API allows 60 lookups an
  hour per network without a sign-in, and every handheld and PC running
  JukaHub on the network spends them on emulators, cores, PortMaster and
  runtimes too; once they were gone, Update failed at "checking JukaHub
  releases" with an HTTP 403, minutes after the release was offered, and kept
  failing for up to an hour. When the API does not answer, the repository's
  latest-release page on github.com (which that limit does not count) names
  the release, and the archive downloads from the release's own folder,
  checked against the SHA-256 the API gave when the release was offered
  (kept with the offer since this build). Other fixes on the same path:
  - **Room for the update is checked first.** The only check asked for 96 MB
    free whatever the release weighed; the program alone is about 70 MB now,
    and the update needs the archive, the unpacked release and a backup of
    every file it replaces at once (about five and a half times the archive,
    less when a retry reuses the unpacked copy). A card
    short of that is told how much is needed and how much is free before
    anything downloads, after JukaHub removes what unfinished updates left in
    its staging area. A card that still fills up says so in plain words
    instead of "no space left on device", and the cached-update install
    checks the same.
  - **The downloaded archive is deleted once it is unpacked**, instead of
    staying on the card until the next update.
  - **A download cut off for one release is never resumed into another's.**
    The archive is now named by its release; under the one shared name the
    next release's download continued the old part, failed its checksum and
    cost one "Update failed" first. Old archives in `patch/downloads` go.
  - A refusal from GitHub's API that is its hourly limit now says so ("…is
    used up; it resets at 21:40") wherever it is shown, and the whole update
    error goes to `errors.txt` (the toast shows its first line).
  - A release may hold up to 1024 files (was 128; the TrimUI archive has about
    40, and a release over the limit could only be fixed by an update).

  Not tried on a device. If an update still fails, the bell keeps the toast's
  reason and `errors.txt` has the `[patch] update:` lines
  (`player/autoupdate.go`, `autoupdate_channel.go`, `patch_engine.go`,
  `upch.go`, `updateprompt.go`).

- **Dolphin "cannot load on this firmware", and cores that cannot work on a
  TrimUI handheld** (Juka, 2026-11-20, on a Brick Pro: "dolphin cannot load on
  this firmware...find dolphine that works on Trimui and use that...also do
  not show cores that will not work on Trimui"). Installing Dolphin from the
  Emulators page said "the 2026-11-20 build cannot load on this firmware:
  needs GLIBCXX_3.4.32": the buildbot's nightly is built with GCC 13, whose
  libstdc++ no TrimUI firmware has. JukaHub's private glibc 2.39 (RetroArch's
  C library on Automatic) runs such builds, but the load check of a core the
  card did not have yet looked at no cores folder, so it never counted that
  runtime or installed it. Every core install now checks a new core as the
  file it becomes, so a first install of a build that only needs a newer
  glibc or libstdc++ sets the runtime up and installs; a runtime download
  that fails no longer marks the build refused for good.
  Dolphin also comes from a build made for TrimUI now: the "RetroArch cores
  for TrimUI" workflow builds libretro/dolphin with GCC 13 from Ubuntu's
  toolchain PPA, libstdc++ linked in and a stand-in libudev that finds no
  devices, so it loads on the firmware's own C library with nothing in
  between. Dolphin's setup takes that build first and the buildbot's (on the
  private glibc) only while it is not published, never puts a nightly over
  JukaHub's build, and the daily core updates leave Dolphin to it. A Dolphin
  tile on the Emulators page (the core, or a GameCube or Wii folder) runs that
  setup with Dolphin's system files and looks for a newer build; on an A133P
  model (Smart Pro, Brick, Brick Pro) whose **GameCube & Wii** is Automatic,
  the press sets it to Try anyway. The workflow runs on the push of this
  change (or Actions → RetroArch cores for TrimUI → Run with `dolphin`); until
  it has published, the buildbot's build is used.
  The Emulators page and **Get more cores** no longer offer cores that cannot
  work on these handhelds: the 3DS's and Wii U's (and other desktop-only
  consoles'), PS2 cores other than ARMSX2, Kronos (desktop OpenGL 4.3), Beetle
  Saturn and the accuracy-first bsnes and higan builds (a desktop-class CPU),
  Doom 3, RemoteJoy, the video processor, Dolphin's launcher core, RetroArch's
  developer tests, and a build this device already refused for a reason no
  new try changes (its next build is offered again). Installed cores stay
  listed. The load check also refuses a build that links a device library
  the firmware lacks (libudev, libusb, BlueZ, libevdev) instead of installing
  one that fails at every start. Not compiled or tried on a device yet
  (`dolphin_setup.go`, `coretrimui.go`, `emulatorupdates.go`,
  `player/native/build-cores.sh`; DOLPHIN-HW-1;
  [Game library](docs/GAME_LIBRARY.md#gamecube-and-wii-on-the-handheld),
  [Compatibility](docs/COMPATIBILITY.md#cores-the-handheld-does-not-offer)).

- **Voice tab: "This voice server does not let guests open rooms here"**
  (Juka, 2026-11-20: "fix voice"). JukaHub asked GameMania's server for the
  JukaHub room from the server's lobby, but the server only lets people who
  are inside **Private User-Channels** open rooms there (its instructions say
  to change to that channel first), so the first person to join was always
  refused. JukaHub now steps into the parent channel first and asks from
  there. A server that still says no gets two more tries before the Voice
  page gives up: a lasting channel (voice room only; never for private Voice
  Channels), then registering the device's certificate under the Chat name
  once, as Mumble's own **Register** does, and reconnecting. Each refusal is
  now logged with the permission and channel the server named, and other
  refusals no longer end the call by mistake (`chatvoice_mumble.go`,
  `chatvoice_mumble_proto.go`; docs/VOICE_CHANNEL.md; VOICE-HW-1).

- **A game and another screen taking turns after a sleep** (Juka, 2026-11-20:
  "when the emulator goes to sleep … one press powerbutton … during wake up
  emulator and stock screen shows up at same time and they blink each
  other"). Three ways a second picture could come back at the wake are
  closed. An emulator JukaHub found by its display (one it did not start, or
  whose launcher ended before it) stopped counting as on screen the moment a
  sleep paused it, so JukaHub drew Home under the paused game and the two took
  turns once the game ran again; the emulator last seen running now keeps the
  screen while a sleep (JukaHub's or the firmware's) has it paused, for up to
  two minutes of awake time. The check that stops a stock MainUI drawing
  beside JukaHub was off while a game ran, so a MainUI that came back at the
  wake was left drawing beside the emulator; every power press and every wake
  now opens that check for a minute, in games too (the game itself is never
  touched), and three seconds later the log names every process holding the
  display (`after the power key or a wake, the display is held by: …`), so a
  report says who drew. And JukaHub told the firmware's own power-key sleep
  from none by `CLOCK_BOOTTIME` alone: on a kernel that adds no suspend time
  to it, JukaHub saw no sleep, put the device to sleep a second time right
  after the firmware's wake and took the wake press for a new press. It now
  reads the RTC as well, as its own sleep's timing already does
  ([player/foreignscreen_linux.go](player/foreignscreen_linux.go),
  [player/displayguard_linux.go](player/displayguard_linux.go),
  [player/powerkey.go](player/powerkey.go)).
- **The exit status after a stop** (launch.sh). When JukaHub was asked to stop
  (the power key, a shutdown) and quit at once, `.last-exit` could record 143 or
  127 instead of JukaHub's own status, so the next start's boot guard could
  misread a clean stop. The launcher now always waits a second time after a stop
  and keeps the first status if that wait has nothing to report.
- **Tetris froze, 2048 stopped adding tiles** (Juka, 2026-11-19: "Tetris
  crashes when run...2048 works only twice and then no new block appears").
  Tetris drew its preview piece after taking it as the first piece, so the
  first game after start-up began with an empty piece; an empty piece fits at
  every depth, and the ghost-piece loop that looks for where it lands never
  ended, freezing the screen. The preview now comes first, and an empty piece
  never fits. Pieces also turn inside their own square with a small wall kick
  (the O block used to jump two columns right on every turn), and the
  mini-games' 60 Hz tick now takes the same lock as input and drawing, so a
  falling piece is no longer moved mid-frame or placed twice when a hard drop
  meets a gravity step. 2048 only added a tile after a move that merged
  tiles; a move that just slid them brought none, so the board ran dry after
  a couple of turns. Every move that changes the board now adds one
  ([player/games.go](player/games.go)).

- **PortMaster stuck on its splash** (Juka, 2026-11-20: testers on a TrimUI
  Brick left on the "PM — Your Ultimate Handheld Linux Port Manager" picture).
  That picture is PortMaster's own splash, a LÖVE program its launcher starts
  in the background while its Python menu (pugwash) loads; pugwash ends it once
  it has a window. When pugwash died first, its launcher ended but the splash
  went on running in the launch's process group, and JukaHub kept the screen
  parked until that group was gone — so the splash stayed until the handheld
  was switched off. Now, the moment a port's or PortMaster's launcher ends,
  JukaHub looks at what is left: when it is only helpers that served the
  launch (PortMaster's splash, a key mapper, `sdl2imgshow`, `portmsg`, shells
  waiting for those) they are ended and JukaHub is back at once, with a toast
  that names the cause from PortMaster's `log.txt`; a game a script left
  running in the background keeps the screen as before. While PortMaster
  runs, JukaHub ends the splash when it is still drawing 5 seconds after
  PortMaster's menu has the screen, and ends the launch when the splash has
  been up for 4 minutes with nothing else on the screen (time paused by the
  in-game menu, an alarm or sleep does not count). A splash started outside
  the launch's group is ended with the other leftovers. **Why didn't it
  open?**, the launch log's end line and the bug report's PortMaster section
  say what JukaHub ended, and PortMaster's `log.txt` is now also read for a
  device check that found no CPU architecture (`KeyError: 'primary_arch'`)
  and for a menu that could not open the screen
  ([player/portmaster_splash.go](player/portmaster_splash.go)).

- **PortMaster on a stock Brick or Smart Pro, again** (report JH-QCKJ9M,
  2026-11-20: "Fix PortMaster", TrimUI Brick, no description). Three gaps the
  November 18 runtime work left open:
  - Since PortMaster's launcher rework of October 2026 (PortMaster-GUI PR
    #350) its default start hook exports `PYSDL2_DLL_PATH=/usr/lib` just
    before pugwash, overriding the folder JukaHub had set. TRIMUI_EX links
    SDL2 there; stock firmware keeps it in `/usr/trimui/lib`, so the menu died
    on "could not find any library for SDL2". JukaHub's Python for PortMaster
    now carries a `sitecustomize.py` that, as Python starts, replaces a
    `PYSDL2_DLL_PATH` with no SDL2 in it by JukaHub's folder of SDL2 links (an
    installed Python gets the file at its next launch)
    ([player/portmaster_python.go](player/portmaster_python.go)).
  - A script started by its own `#!/bin/bash` line — `device_info.txt` run on
    its own, WestonPack's `westonwrap.sh`, a port's helper scripts — went to
    the firmware's `/bin/bash`, which on the Brick is BusyBox's sh or missing.
    When `/bin/bash` is not bash, each
    PortMaster or port launch now runs in a mount namespace of its own whose
    `/bin` is a read-only tmpfs folder of links to the firmware's `/bin` plus
    the real bash the launch already uses. The firmware is not changed; the mounts end
    with the launch; anything that fails leaves the launch as before and its
    log says why. `portBinBash: "off"` turns it off
    ([player/portmaster_binbash.go](player/portmaster_binbash.go)).
  - **Why didn't it open?** and the closed-at-once toast now also read
    PortMaster's own `log.txt`, where its launcher sends pugwash's output, and
    name an SDL2, Python or BusyBox error found there. A port's launch log
    ends with how long it ran and how it ended.
  ([docs/PORTMASTER.md](docs/PORTMASTER.md))
- **A bug report says what PortMaster did.** The diagnostics report has a
  PortMaster section on a handheld with PortMaster or a port launch: the bash,
  `/bin/bash` and `python3` it gets, how the last launch ended, and, for a
  launch in the last day, the last lines of its launch log and of PortMaster's
  `log.txt` — read from the card, no program started, scrubbed like the rest
  ([player/portmaster_report.go](player/portmaster_report.go)).
- **Sending a bug report no longer freezes the screen.** The report's
  component list hashed JukaHub's own binary, ffmpeg and ffprobe on the UI
  thread at every Preview and Send (a 1.7 s frame on a Brick in JH-QCKJ9M).
  The digests are now kept by size and change time and worked out in the
  background when Report a bug opens (a report sent that very moment waits at
  most 0.3 s and says "still being hashed"), and a helper whose version probe
  failed — yt-dlp here, up to 3 s each time — is asked again only after ten
  minutes ([player/componentreport.go](player/componentreport.go)).
- **Next no longer drops a song from the play-next queue.** The decoder a skip
  stops used to pick the next track as if it had finished, so with songs queued
  every Next press also took one off the queue; only a decoder that really
  reached its end moves the list on now
  ([player/musicplayer.go](player/musicplayer.go)).
- **A port that stops at once now says why** (Juka, 2026-11-19, TrimUI
  Brick: SuperTuxKart from the Ports Store said "Launching SuperTuxKart" and
  nothing else happened). A port's exit status is ignored, and the quick-exit
  check only spoke when the port's `log.txt` had a line saying loading failed,
  so a script that died before that — under BusyBox's sh, sourcing a
  `control.txt` it could not find, on a program the firmware cannot load —
  came back to the details page without a word. Now a port JukaHub did not
  stop that ends within 8 seconds always gets the "didn't start" card (within
  20 seconds when its logs name a cause, so leaving a cloud-gaming client
  early stays quiet), and both its logs are read: JukaHub's `data/gamediag/ports/<script>.log` (the
  launch notes and everything the script printed, also before it redirected
  its output) and the port's `log.txt`. The lines are sorted into causes, the
  surest first: bash syntax refused by BusyBox (in a file PortMaster sourced,
  or a script started by its own `#!/bin/bash`), PortMaster not found or
  hidden by an empty folder, a newer glibc or a missing library, a program for
  another processor, JukaHub's own bash not starting, graphics (EGL, OpenGL
  ES, SDL video) failing, a crash, the system stopping it for memory, a
  runtime that is missing or did not mount, a file it may not run, and
  commands the firmware lacks (a "maybe"). The card shows the lines behind
  each cause, or the port's last lines when nothing is recognised. **Why didn't
  it start?** reads the same logs. And with `portLaunchVia` on `auto`, a port
  under `Roms/` on a firmware with no full bash anywhere no longer goes through
  the firmware's Ports launcher (BusyBox would run it) but runs directly with
  the bash JukaHub supplies, once that is downloaded. The cause of the
  SuperTuxKart failure itself is
  not known yet: the card on the next try names it. (`player/gamediag_port.go`,
  `gamediag.go`, `ports_env.go`, `ports_manage.go`, `portmaster_runtime.go`,
  `ports_test.go`; docs/PORTMASTER.md)

- An offline AI question can now clear the full set of caches immediately after
  an automatic light trim, instead of waiting for the trim's 15-second cooldown.
  A timed-out clear request is withdrawn so it cannot evict textures later.
  Leaving Chat releases older processed AI pictures and video frames while
  keeping the newest one for a follow-up.

- **Video speed really changes the speed** (Juka, 2026-11-19: "Media player 2x
  (or changing to any other speed) is not working"). The speed pill, the
  settings sheet's Speed row and keys 1–4 only changed a label; both ffmpeg
  decoders kept playing at 1×. A speed change now restarts picture and sound
  where the video was, like a seek: the picture is retimed and held to the
  source frame rate (no more frames to draw at 2× than at 1×), the sound goes
  through `atempo` so the pitch stays, and A/V sync works as before. The clock,
  seek bar, subtitles, resume point and Watch Party position move at the new
  rate, and a paused video stays paused. The speed stays for the same video and
  carries to the next one when the player moves on (L2/R2, previous/next, Up
  Next, Shorts autoplay); another video opened from a screen starts at 1×. Live
  streams and Watch Parties play at 1× with a toast, and hosting or joining a
  party drops a faster video to 1×. Above 1× the handheld skips deblocking
  frames nothing refers to and, if the picture still falls a second behind the
  sound, decodes lighter from then on (logged). Untested on a device
  (VSPEED-HW-1). (`player/videoplayer.go`, `avsync.go`, `videochrome.go`,
  `videosheet.go`, `partyscene.go`; docs/CONTROLS.md)

- **Music and audiobooks keep their place at another speed** (2026-11-19). At
  1.5× or 2× the position divided the decoded sound by the speed instead of
  multiplying it, so at 2× the clock and the saved resume point ran at a
  quarter of the real rate, and a speed change or a resume jumped back that
  far. (`player/audioresume.go`; docs/MUSIC_PLAYER.md)

- **Aircrack-ng and Nikto install and run on the handheld** (Juka, 2026-11-19).
  Nikto stopped at start with "Required module not found: XML::Writer":
  Ubuntu's Perl lacks the XML::Writer and JSON modules it checks for. Install
  and Update now bring `libxml-writer-perl` and `libjson-perl` with Perl, and
  for a Nikto installed earlier **Terminal** or **Version** installs what is
  missing first; a Perl "module not found" in a version report names the
  command that installs it (`apt install` on the handheld, `cpanm` with
  Strawberry Perl on Windows). Aircrack-ng did not install and nothing said
  why: a failed Linux-packages install showed only a toast. The sheet now shows
  the reason with the last lines Linux packages printed, and the log gets
  `audit: aircrack-ng install failed: …`. The handheld recipe leaves out the
  adapter set-up helpers, hwloc's viewer with its cairo/X11/font chain, and
  Python; that is recorded with the package, so upgrade, repair, remove and
  autoremove keep it (`apt show` lists it). Linux packages no longer fails a
  whole package for a member name the card cannot hold or a file whose name is
  already a folder, writes every program wrapper even when one fails, and
  rebuilds missing wrappers after an install that ended early. An audit press
  while Linux packages is busy says so instead of cancelling the running
  install. Untested on a device (AUDIT-HW-1). (`player/secaudit.go`,
  `auditscene.go`, `linuxpkg_install.go`, `linuxpkg_cli.go`,
  `linuxpkg_runtime.go`, `linuxpkg_ui.go`; docs/TOOLBOX.md,
  docs/LINUX_PACKAGES.md)

- Juka's vision cache now has an 8 MB handheld / 12 MB desktop byte limit for
  processed pictures and video frames, and releases them under memory pressure.
  Generated video frames are limited to 16 MB before they are read into memory.

- Chat image downloads now use one pending slot instead of accumulating multiple
  32 MB buffers; avatar downloads use four 1 MB slots. Leaving Chat or clearing
  caches cancels active requests and drops queued bytes. Late results cannot
  restore them, and oversized avatar pictures are refused before decoding.

- **Release builds embed current Help pages and filed reports show the update channel**
  (2026-11-19). The Windows release job and both packaging scripts refresh
  Getting started and Controls before building. The report service accepts the
  optional channel and shows it beside the version in the filed issue.

- **Cancelling Windows subtitle fetches stops the entire yt-dlp process tree**
  (2026-11-19). The fetch uses the existing hidden `taskkill /T /F` path, with
  a direct kill fallback, before removing its temporary folder.

- **Windows update helpers start safely from folders with `&` or parentheses**
  (2026-11-19). The detached update and rollback helper now starts through
  the existing batch launcher, which quotes its path for `cmd.exe` and rejects
  characters it cannot safely quote.

- **New mail and calendar secrets join log redaction on the next frame**
  (2026-11-19). A successful credential change queues a main-thread refresh;
  if the mail store is busy, it retries until the latest secrets are covered.

- **Destructive choices on Tabs Home and Remote Play now open on Keep**
  (2026-11-19). Deleting a collection focuses Keep on its confirmation page.
  Forgetting a PlayStation console, including with Y, and removing its saved
  passcode ask before changing anything; B or Keep returns without a change.

- **John the Ripper runs on the handheld instead of stopping at its config**
  (Juka, 2026-11-19: "fopen: /etc/john/john.conf: No Such file or directory").
  Ubuntu builds john 1.9.0 with `JOHN_SYSTEMWIDE=1` and
  `-DCFG_FULL_NAME="/etc/john/john.conf"`; it reads `~/.john/john.conf` first
  and then that built-in path, which is fatal when missing. The handheld's
  `/etc` is the firmware's root, so the package's config only ever lands in
  `data/linux/root/etc/john` and every run died at the second read. Linux
  packages now copies the config to `~/.john/john.conf` (the home the password
  database gives, which is what john uses, not `$HOME`) after every change,
  with `$JOHN/` spelled out as the card's `root/usr/share/john` so the
  charsets, `password.lst` and the wordlist default resolve too; a `john.conf`
  that did not come from JukaHub is left alone. It is rewritten after every
  package change and once at startup, so a john installed before this fix
  works without installing anything again. (`player/linuxpkg_runtime.go`,
  `docs/LINUX_PACKAGES.md`, `docs/TOOLBOX.md`)

- **The X1 pill is green only while an Xbox is actually there** (Juka,
  2026-11-19: "XBOX1 on main menu showing green even though it's not
  connected....or no available devices"). The dot was lit whenever an Xbox
  streaming client was installed (GreenOvercast on the handheld, Greenlight or
  XStreaming on a PC), so it read as "connected" with no console on and nothing
  to stream. It now lights only while an Xbox on this network answers
  SmartGlass discovery (a 16-byte request to UDP 5050, the plain-text part of
  the protocol, so no crypto): Home asks every 10 seconds while the pill is
  drawn, on a goroutine, and the dot goes dark 25 seconds after the last answer
  or as soon as the handheld wakes from sleep. A console that is off or asleep,
  has Remote features off, or is on another network leaves it dark; pressing X1
  still opens the client, which can wake and stream it over the internet
  (`player/xremote_discovery.go`, `player/xremote.go`,
  `docs/REMOTE_PLAY.md`).

- **Quick Settings' Wi-Fi and Airplane Mode switches work without a working
  `rfkill` program** (Juka, 2026-11-19: "it shows wifi off, bluetooth off, and
  airplane off. When i click Wifi it gives rfkill unlock wifi: exit status
  127"). The switches ran the `rfkill` found on PATH, and this handheld's
  cannot run (127 is a BusyBox without the applet or a library that would not
  load), so every reading failed too and the panel drew Wi-Fi and Airplane Mode
  off whatever they were. The radios are now read from the kernel's own
  `/sys/class/rfkill` files with no process, and switched the way the rfkill
  programs do it — one "change all" event written to `/dev/rfkill` — or
  through each switch's `soft` file (`state` on old kernels), read back after
  the write (a hardware block says so; Airplane Mode off still succeeds). On
  the handhelds, off and Airplane Mode move a radio's own stack switch (`phy0`,
  `hci0`) and never the board's Bluetooth power switch (`sunxi-bt`) while
  `hci0` is registered: cutting the XR829's power under an attached adapter
  left Bluetooth dead until a restart. The program is only the fallback: found
  beyond PATH (`rfkill.elf` too), run with the firmware's environment and the
  `wlan` type BusyBox understands, its own message in the error, and set aside
  for the session once it cannot run. Bluetooth's rfkill fallback and bring-up
  unblock use the same path. Bluetooth reading off is the kernel's answer (no
  adapter, or no bluetoothd on the handheld); its switch starts it. Not yet
  built. `player/rfkill_sysfs.go`, `player/systemcontrol.go`,
  `player/bluetooth_bringup.go`.

- **The in-game menu fits short screens** (2026-11-18). With Music,
  Rewind, Disc, Cheats, Ask Juka and the game switcher all on, its rows ran off
  a 720-pixel screen; they now scroll with the selection, with small arrows
  where more rows hide. Not yet built. `player/gamemenu.go`.

- **Game Boy, Game Boy Color and GBA games start again when a core update
  broke mGBA** (Juka, 2026-11-18, photos of the card on a stock Smart Pro:
  "004 Pokemon Fire Red didn't start … mgba_libretro.so needs GLIBC_2.35; this
  firmware's libm.so.6 has up to GLIBC_2.32"). The libretro buildbot builds its
  aarch64 nightlies on a newer Ubuntu than the firmware; JukaHub's own updates
  refuse such builds, but RetroArch's Online Updater installs them with no
  check, and stock firmware runs all three Game Boy consoles on mGBA. A core
  RetroArch cannot open here is now replaced: by a build of it the card kept
  (the card's original first), else by JukaHub's own build for the device. A
  new workflow compiles those weekly from upstream libretro sources in the
  Ubuntu 20.04 (glibc 2.31) arm64 build root and publishes them as the rolling
  release `cores-aarch64` in the buildbot's index format (mGBA, Gambatte, gpSP,
  Snes9x, Snes9x 2005, FCEUmm, Nestopia, PicoDrive, Genesis Plus GX,
  PCSX-ReARMed, Beetle PCE Fast, WonderSwan and NeoPop, FinalBurn Neo), so the
  download takes the usual checks. The fix runs after a game that fails this
  way (the card's first finding says JukaHub is fixing the core, a toast says
  when to start the game again) and a minute after every start over the cores
  that changed since the last look, where it acts only on a sure finding (the
  wrong architecture or a glibc symbol version the firmware lacks) and
  downloads only when Emulator updates is Install automatically. The broken
  build is kept as a `.bak`. JukaHub's Beta channel no longer takes the
  repository's emulator releases (`cores-aarch64`, `ppsspp-tg5040`) for
  JukaHub updates.
  The card also drops RetroArch's harmless `Couldn't find any input driver
  named "sdl"` line, and a file named under a finding with nothing missing
  (the core itself) is labelled "Location:" rather than "Copy to:". Not yet
  built; JukaHub's builds
  exist once `retroarch-cores.yml` has run. `player/corefix.go`, `gamediag.go`,
  `launch_errors.go`, `emulatorautoupdate.go`, `autoupdate_channel.go`;
  `player/native/build-cores.sh`,
  `.github/workflows/retroarch-cores.yml`, `.github/scripts/build-root.sh`;
  docs/COMPATIBILITY.md ("A core that is already broken").

- **JukaHub's icon in the TrimUI Apps menu is the size of the others**
  (Juka, 2026-11-18, photo of the Apps page: "too large"). MainUI stretches a
  config.json `"icon"` over the whole grid cell and prints the name across the
  cell's lower part, and the logo filled its 300 px canvas, so the tile came
  out about half again as wide as PortMaster's with "JukaHub" written over the
  J. `player/trimui/Apps/JukaHub/icon.png` now keeps the 300 px canvas but
  draws the logo 176 px wide, centred and 8% from the top, where a stock-sized
  icon sits, clear of the label; release folders ship it as `icon.png` too and
  `player/config.json` names it instead of `JukaPlayer.png` (still shipped,
  still the window icon). An update brings both; MainUI shows the new icon
  the next time it lists Apps. `branding/mklogo.py` (`render_app_icon`).

- **PS4/PS5 Remote Play reconnects after the handheld sleeps** (Juka,
  2026-11-18: "if I press power it turns off the screen, and when I turn it on
  the application is stuck — it should try to reconnect, and if it cannot,
  exit"). JukaHub is not running during a stream, so the power key is the
  firmware's: it suspends the handheld (Wi-Fi off) and the console drops the
  session; the helper then sat on the last picture for good. `chiaki-stream`
  now notices the pause (`CLOCK_BOOTTIME` jumped), gives the stream 5 s to
  show fresh picture or sound and otherwise shows **Reconnecting to …**: it
  closes the dead session and opens new ones with the same pairing once Wi-Fi has a
  route again, 20 s per try, entering a login passcode the console already
  took. Picture and sound gone for 15 s without a sleep, or an error ending
  right after a pause or a silence, reconnect the same way; an ending while
  the picture still flows (rest mode, another device) still just ends. After
  a minute it gives up with the error card (`connection_lost`, rc 11) and
  JukaHub comes back with "The stream from … dropped … and could not be
  reconnected". Start + Select stops it at any time. Needs the next helper
  build. `player/native/chiaki-stream.c`, `stream_sys.c/.h` (`ss_net_up`),
  `player/psremote_lastrun.go`, docs/REMOTE_PLAY.md, native/README.md.

- **The shader speed check measures something now.** It asked RetroArch's
  command port for a frame counter and a fast-forward flag RetroArch does not
  have, so every check ended "Unavailable". On the handheld it now counts the
  screen's page flips over about 16 seconds of clean play (skipping loading,
  pauses, JukaHub's menus, sleep and fast-forward); where flips cannot be
  seen, and on a PC, it reads RetroArch's own frame timing from the game's log
  after it closes. A game's options say Full speed or Below full speed with
  the FPS reached, and a new verdict shows once when JukaHub is back ("CRT
  keeps this game at full speed — 59.9 of 60 FPS."). Handheld systems are
  judged against 60 FPS whatever the region, the Virtual Boy against 50.
  `player/shader_speed.go`, `shader_speed_linux.go`, `shader_speed_other.go`,
  `gamediag.go`, `gamepause_ra.go`.

- **Stick lights no longer blink** (2026-11-18). The TrimUI rings went dark
  for a moment on every colour change, because a new colour was put in by
  switching the ring's effect off and on again while it was lit. JukaHub now
  never does that on a lit ring: where the driver allows it the colour goes
  straight to the LEDs (through the driver's own frame, or a static effect
  written without the off step — each used only after reading back that it
  really took), and otherwise the brightness slips down to dark for a fraction
  of a second, the colour changes there, and it comes back up. In steady light
  nothing is rewritten any more; when TrimUI's own service changes the rings,
  JukaHub paints them once more, and stops after two such changes instead of
  repainting every minute. The Music mode keeps a soft glow between songs
  instead of going dark and flashing at every menu click, and its colour
  follows the music calmly; Rainbow and Screen colours change colour less often
  where every change costs a dip. A low-battery warning or charging light no
  longer flickers on and off when the battery reading wobbles. The log (scene
  "led") now says which way the colour reaches the rings and why each change
  was written. Not yet built.
- **N64 (and other GPU cores) no longer broken by the automatic core update**
  (2026-11-18). The daily core update replaced every core whose bytes differed
  from the libretro buildbot's nightly, including the GPU-rendered ones —
  Mupen64Plus-Next, ParaLLEl N64, Flycast, PPSSPP — whose generic linux/aarch64
  builds are made for desktop OpenGL, while the handheld's RetroArch draws with
  OpenGL ES only: such a build cannot be opened, or asks for a GPU context
  RetroArch cannot make, and the game is back in the menu a second after A. A
  build that needs desktop OpenGL's libGL.so.1 is now refused before it
  replaces anything; the automatic pass leaves a GPU core on the firmware's or
  CrossMix's own build and says so once (a press still installs the buildbot
  build, with a warning); and on its first start this build gives such a core,
  replaced by an earlier JukaHub update, its kept firmware build back once, with
  a toast (R1 on the Emulators page undoes it). The build the card came with is
  no longer pruned from the kept builds, and core-history.json remembers it
  however often the core is updated. Spatial audio also leaves alone a game
  that brings its own ALSA library (a standalone emulator's), whose sound could
  otherwise fail to open. Not yet built.
- **Boost no longer resets a game's other core options** (2026-11-18). A game's
  own `.opt` is the only options file RetroArch reads for it, so Boost's
  two-line override switched every other option of the core back to its
  default; it now carries the options RetroArch read for the game before. Not
  yet built.
- **PortMaster opens on a stock TrimUI Smart Pro** (2026-11-18, backlog
  PORT-HW-3). PortMaster's TrimUI package is written for TRIMUI_EX (which
  CrossMix includes): its `launch.sh` sources `System/etc/ex_config`, its menu
  needs `python3`, its scripts need a real bash, and pysdl2 needs
  `PYSDL2_DLL_PATH` — on stock firmware, where `/bin/bash` is BusyBox and there
  is no Python, it closed the moment it opened. On an ARM64 TrimUI JukaHub now
  checks the firmware's bash and python3 once a session, in the port's own
  environment, and supplies what fails from its own folder for PortMaster,
  its ports and harbourmaster only: a python-build-standalone CPython 3.11
  (runs on the firmware's glibc; the archive must match its SHA-256 in the
  release's `SHA256SUMS`; `portmaster_python.go`) and Ubuntu's bash on the
  private C runtime, plus links to the firmware's SDL2 for pysdl2, and
  `/usr/trimui/lib` and the firmware CA bundle when missing
  (`portmaster_runtime.go`). When `launch.sh` reads an `ex_config` the card
  does not have, a copy beside it (`.jukahub-launch.sh`) runs instead, with
  that line made conditional. The first open downloads what is missing with
  the install card and then opens PortMaster by itself; installing PortMaster
  fetches it at once. Installing now checks the package has
  `PortMaster/control.txt` and creates `Data/home` and `Data/ports`. **Y on
  the PortMaster row** adds **Repair PortMaster** (downloads the package again
  and writes its files over the install; ports, `libs/` and settings stay) and
  **Why didn't it open?**; a PortMaster that closes at once says the likeliest
  reason, and each launch log names the bash and Python used.
- **The private glibc is Ubuntu's newest signed build, with bash and the C++
  runtime** (2026-11-18). It was one pinned `libc6` 2.39-0ubuntu8.12. JukaHub
  now installs the newest noble `libc6`, `libgcc-s1`, `libstdc++6`,
  `libtinfo6` and `bash` through apt's trust chain (Ubuntu's archive key on
  the `Release`, the `Release`'s SHA-256 on the `main` index, the index's
  SHA-256 on every `.deb`; an older `Release` than one already accepted is
  refused), probes its loader and bash on the device, and switches
  `data/private-glibc/current` to it. It looks for a newer build at most
  weekly when the Ports panel opens online. The pinned package stays as the
  fallback when the signed index cannot be read (`glibc_runtime.go`,
  `docs/PRIVATE_GLIBC.md`).
- **Quick Settings ignored the mouse, and L2/R2 never reached it**
  (2026-11-18). A click while the panel was open went to the screen behind it,
  and the triggers were swallowed by the overlay guard before the panel saw
  them. The panel is modal now (a click outside closes it, the wheel walks its
  buttons) and takes the triggers first.
- **The Patch manager remembers installed packages, and Remove deletes them**
  (2026-11-18, backlog ARCH-9). The package database (`db.json`) was written on
  every install but never read back, so after a restart Remove did not know
  earlier packages, History was empty and the next install rewrote the file
  without them; it is now read on first use. Remove of a package from an earlier
  session now deletes its files (it looked for them under the install folder
  twice over, found nothing and dropped the record) and refuses a file outside
  that folder; a db.json that cannot be read is never overwritten. Not yet
  built.
- **A restore writes only JukaHub's own data files** (2026-11-18, backlog
  SEC-33). Restoring a `.jkbackup` now writes into the data folder only the
  top-level stores a backup carries and the folders it names (the curation
  lists), so a backup changed by hand can no longer place a program among the
  emulators, runtimes and packs that live there; anything else is listed as
  skipped. Backups no longer carry the battery calibration (`battery_cal.json`)
  or the VPN's DNS copy (`vpn-resolv.json`), which describe this device; in an
  older backup they are listed as skipped. Not yet built.
- **Browser cookies: fewer writes to the card** (2026-11-18, backlog SEC-32). A
  site that re-sends a Max-Age cookie with every response no longer rewrites
  `browser-cookies.json` each time: a saved cookie's expiry is only updated when
  it moves by more than an hour (or by more than half the time a short-lived
  cookie has left, so a 10-minute session cookie is never saved already
  expired), or when the cookie's value or flags change. Not yet built.
- **Scene Terminal commands stop after two minutes** (2026-11-18, backlog
  SEC-14). A command run by a design's Terminal or emulator button
  (`terminal_run`, `emulator_run`) is now stopped after two minutes with
  everything it started, ending "(stopped after 2m0s)"; before, a hung one ran
  until JukaHub exited. An emulator started that way is stopped too: the trigger
  runs commands, it does not launch games. Only the end of the output is held
  while it runs (the last 4 000 characters are shown), so a chatty command can
  no longer fill the memory. Not yet built.
- **Radio and podcasts no longer go silent for good when the stream stops**
  (2026-11-18, backlog MED-36). The music player's ffmpeg now gets the same
  15-second read timeout and reconnect the video player uses, so a short Wi-Fi
  drop plays on and a stream that stays quiet ends with "Playback stopped early
  — press Play to resume" instead of silence. A podcast cut off part way keeps
  its place instead of counting as finished, an HLS stream whose reads hang is
  stopped after 90 s, and a live station no longer remembers a place, so Play
  after Stop starts it live. Not yet built.
- **WebP pictures open on the stock Smart Pro** (2026-11-18, backlog MED-37).
  Where SDL_image has no WebP decoder (stock firmware 1.1.0, the Windows build)
  but ffmpeg is installed, .webp files show in Files with a thumbnail, open in
  the picture viewer, work as the lock screen and in the photo frame, and NX
  Redux-style .webp covers show as box art after the next library refresh;
  ffmpeg decodes them off the render thread after a size check. Covers are
  converted once into a cache capped at 256 MB, and one ffmpeg cannot read no
  longer blocks a downloaded cover. Chat cannot show a .webp yet. Not yet built.
- **Picture and video questions to Juka survive a sleep** (2026-11-18, backlog
  NET-21). A question with a picture, video or recording that was in flight when
  the handheld slept used to wait out its six-minute limit before it was asked
  again; it is now called off at the wake like any other Juka question (ffmpeg
  and the transcript stop too) and asked again once the network answers. Half an
  answer cut by the sleep is no longer kept as the reply. Not yet built.
- **The surface probe measures the EGL a game run uses on the stock firmware**
  (2026-11-18, backlog AND-23). On a stock TrimUI the AndroidRT surface probe
  now loads the firmware's own `/usr/lib/libEGL.so` first, like the present
  layer, instead of whatever libEGL the library path finds first, and points its
  SDL check at that same file. Both share one firmware path list
  (`apak_gpuFirmwareEgl`), and the smoke suite checks the probe's `libraries:`
  line against a staged firmware tree with a decoy on the library path. Not yet
  built.
- **A stream that stops sending no longer freezes the player** (2026-11-17,
  backlog MED-29). The video decoders reconnect, then give up, when an http(s)
  stream sends nothing for 15 seconds, so a short Wi-Fi drop plays on and a dead
  stream ends within about a minute like any broken one: a web video carries on
  from a download, anything else says "Playback stopped — the connection timed
  out". A live or HLS stream whose read waits 90 s is ended the same way (time
  the device spent asleep does not count). Radio and podcasts are MED-36. Not
  yet built.
- **Browser cookies keep their HttpOnly flag** (2026-11-17, backlog SEC-26).
  Saved cookie records are keyed like the browser's cookie jar (domain or host,
  path, name), so a script-hidden session cookie and an ordinary cookie of the
  same name no longer share a record, which had let page scripts read the
  session token. Page scripts now see only cookies whose record says they may,
  and a cookie sent with Max-Age keeps its real lifetime on the card. A
  `browser-cookies.json` from an older build is merged on load, keeping HttpOnly
  when in doubt. Not yet built.
- **A crash no longer leaves a game running under the menu** (2026-11-17,
  backlog REL-11). After a crash or the out-of-memory killer, a game JukaHub had
  started kept running under the TrimUI menu. Now `launch.sh` stops exactly the
  games JukaHub put on screen — checking that each is still the very process
  JukaHub started — (save-and-quit, a kill after 3 s) before the menu returns,
  touching nothing else; a crash within milliseconds of a game start is not
  covered. Not yet built.
- **Xbox 360 library lists what Xenia opens** (2026-11-17, backlog XBX-9).
  Roms/XBOX360 now lists disc images (.iso, .xiso), extracted games (once, under
  the folder's name, with their own cover) and Xenia Canary's .zar archives; a
  .zip, .7z, .bin or .chd there no longer shows as a game. The ROM manager tells
  a 360 disc from an original Xbox one by its boot file, full XGD2/XGD3 dumps
  included. Not yet built.
- **Windows launchers keep their game on screen** (2026-11-17, backlog APP-23).
  A launcher that starts the real program and quits no longer ends the session:
  JukaHub waits while Windows programs remain in the prefix (`wineserver -w`),
  so WestonPack, the pad-as-mouse and the parked screen stay. If a hidden
  background program keeps the session waiting, the pad buzzes after 10 s and
  the Windows Apps row says why; Select + Start still ends everything, and the
  wait gives up after 12 hours. Not yet built.
- **A backup can no longer replace JukaHub's own files** (2026-11-17). Restoring
  a `.jkbackup` writes into JukaHub's folder only the settings files a backup
  takes from there, so a changed or foreign backup cannot place `launch.sh`, the
  program or a file that makes the next start erase. Not yet built.
- **Stock TrimUI HTTPS tools and system log** (2026-11-17). The launcher now
  points OpenSSL helpers at the firmware's CA bundle; the stock `curl` rejected
  the tested download hosts without it even though Go and that bundle verified
  them. The launcher change is installed on the test device and a
  verified font download succeeded. In source, Command Deck's System log now
  reads the current `/var/log/messages` file when the firmware has no
  `logread` buffer, and the Lato font row now gives its actual approximate
  download size, 1.3 MB. These source changes need a new ARM build.

- **Stock Smart Pro startup and time-lapse codecs** (2026-11-17). Release
  packaging now includes a launcher-path `setterm` shim for the stock SDL2
  fbdev backend,
  which calls `setterm` even though firmware 1.1.0 does not provide it. The
  shim is installed on the test device; a short app launch returned to one
  MainUI with no `setterm` errors. Time-lapse now
  records JPEG stills and assembles MPEG-4 MP4, matching the device FFmpeg's
  encoders; the old PNG/H.264 path failed there. The time-lapse source still
  needs an ARM build and a game recording. Browser image requests now name only
  formats the loaded image reader can decode, so negotiating image hosts can
  return supported art on this firmware.

- **PortMaster catalog screenshots.** The catalog accepts a safe screenshot
  name from a single value, list or image object, so Ports Store entries keep
  their artwork when PortMaster uses the object form.

- **Watch Party guests play YouTube rooms** (2026-11-17, backlog MED-28). A
  room's video now opens on a guest the way Play does: the stream where it can
  be read directly, otherwise the download, with the loading card and at the
  room's position. Before, the watch-page address went straight to the decoder
  and every YouTube room showed "Couldn't play this stream". A guest only opens
  stream links from a room (never a path on the card); B on the loading card
  leaves the video until Join; a video the guest is loading itself is not
  cancelled for the room's; a room that moves on cancels the old load; and a
  load sent to a background download no longer freezes the room. Not yet
  built.
- **Wi-Fi sign-in pages and network changes** (2026-11-17, backlog NET-11).
  Wi-Fi → Sign in now really opens the portal page in the Browser (a PC opens
  its own browser), and the Browser loads it and its links while JukaHub counts
  itself offline behind the portal. Joining, leaving or forgetting a network,
  turning the radio off from the Wi-Fi page, the background keeper losing a
  network, and leaving the Browser while a portal is up each probe at once,
  and the old network's verdict is never shown for the new one. Not yet built.
- **Juka after a sleep** (2026-11-17, backlog NET-18). A wake cancels a Juka
  request that was in flight and asks it again once the network answers, with
  no error; right after a wake JukaHub no longer reports the online status from
  before the sleep. Not yet built.
- **The Downloads queue never picks AV1 on the handheld** (2026-11-17, backlog
  MED-35). The queue's last resort could still choose an AV1 stream, which the
  firmware's ffmpeg cannot decode; on the handheld it now skips AV1 (streams
  whose codec a site does not name still count). A PC is unchanged. Not yet
  built.
- **Time-lapse for Wine profiles** (2026-11-17, backlog APP-24). A Wine game
  profile records its time-lapse again: the settings are read on the main
  thread and the capture starts once the program runs, as for Android
  profiles; it stops without making the screen wait for the encode. Not yet
  built.
- **Home search opens pages, not rows** (2026-11-17, backlog INP-23).
  Searching "emulators" or "voice shortcuts" (and Wi-Fi, Downloads, Phone
  companion, Report a bug, Security & trust, Theme presets) opens that page
  instead of focusing a Settings row of the same name, and Home search lists
  the Emulators page's tiles by name and by the systems they play. Not yet
  built.
- **The Games tab's Install badge knows per-game emulators** (2026-11-17,
  backlog LIB-22). A console whose games are pinned to an emulator per game no
  longer reads "Install"; the check reads the game options already in memory,
  off the render thread and bounded. Not yet built.
- **Command Deck and Juka tools** (2026-11-17, backlog TERM-1). The Command
  Deck form has a clickable Run; its favourites load in the background instead
  of on the render thread; Juka's confirmation card wraps a long command over
  up to three lines instead of cutting it at the screen edge; a port's game
  options show only its CPU rows. Not yet built.
- **Archive extraction trusts less** (2026-11-17, backlog SEC-27). Files counts
  the room an archive needs in the card's clusters, so thousands of tiny files
  on a large-cluster exFAT card are refused up front, and a file of 4 GB or
  more bound for a FAT32 card is refused before anything is written. RAR files
  unpacked with Windows' own `tar.exe` are listed first and held to the same
  limits as 7-Zip; the 7-Zip helper JukaHub fetches on Windows is checked
  before it takes its name; `.xz` chunks with settings xz itself refuses are
  stopped before decoding; and `.xz` files made with BCJ or delta filters go to
  7-Zip when it is installed. Not yet built.
- **Secrets stay out of every log** (2026-11-17, backlog SEC-2). Every log line
  is cleaned of credentials before it reaches the Logs screen, logs.txt,
  errors.txt, the diagnostics bundle or a bug report: keys and tokens in URLs
  and JSON, OAuth codes, Authorization headers, passwords, PINs, WireGuard
  keys, cookies and tokens recognisable by their shape. The updater's and
  ffmpeg's direct writes to errors.txt are cleaned too, and a
  `JUKAHUB_CRYPTO_KEY` is identified by a fingerprint instead of its first
  eight characters (older values still decrypt). Not yet built.

- **PS5 pairing helper errors.** Registration checks whether this firmware
  can load `chiaki-regist` and reports the reason before starting pairing.
- **Remote Play helper check.** Opening the panel warms the helper check in
  the background; Play finishes any pending check and chooses the home or
  internet address off the event thread.
- **Wine profile timelapses.** A profile now reads capture settings on the
  main thread and starts recording once its program is running.

- **Music: no lost last second, no silent race through the playlist in games**
  (found while adding the USB DAC player for Juka's 2026-11-17 request). Not
  yet built or tried on hardware. Every natural track end cleared the queued
  sound, dropping up to a second of the old track's tail plus a UI click, and
  the end of a list cut its last second; now the queue plays out (gapless, see
  Added). Music left playing when a game started ran through the whole
  playlist in silence while the game held the sound card, because the decoder
  saw an empty queue and never waited; it now keeps playing under the game or
  pauses, and at the launch itself it is held still while the outputs close,
  so it no longer jumps seconds ahead. A NextUI card's folders
  (`Roms/Name (TAG)`) showed an empty library and its console icon could be
  taken as a disc game's cover; both are fixed by "NextUI cards as they are"
  (Added).

- **Internet browser: pages load whole and styled** (Juka, 2026-11-17: "Fix
  internet browser layout engine... many things do not load"). Not yet built.
  A site's main stylesheet past 256 KiB was dropped entirely and only the
  first 3 sheets were fetched, so most real sites drew unstyled; now up to 12
  sheets (2 MiB) load, an oversized sheet keeps its complete rules, link
  `media` is honoured (print sheets no longer apply) and the preload/print
  async-CSS loaders count. A page with any `<main>` or `<article>` lost
  everything outside it — on news and shop fronts, whose cards are
  `<article>`s, that was most of the page; every block is kept now. Images:
  48 per page instead of 16 (10 MiB instead of 4), no more `image/avif` in the
  Accept header (CDNs answered with AVIF, which this build cannot decode), and
  one pooled connection per host instead of a fresh TLS handshake for every
  stylesheet and picture. Pages in windows-1251, KOI8-R, ISO-8859-x and other
  single-byte charsets are decoded instead of drawing as replacement
  characters; text, JSON and XML files open as text and image links open the
  picture instead of the "not a web page" card; a 404/500 that comes with the
  site's own page shows that page. CSS matching indexes rules by id, class and
  tag, so the larger sheets do not slow the parse. Long pages keep 4,000
  blocks (was 1,800).
- **Code-scanning alerts, November batch** (Juka, 2026-11-17: the open CodeQL
  list). WPE Browser gets its address through the environment instead of the
  shell's argument list; tool archives are staged under the installer's own
  file names; the path helpers end in checks the current CodeQL recognises
  (it dropped `filepath.Rel` in 2.26.2), and box art, launch-script probing,
  background music, the Downloads copy and the Minecraft peer's files use what
  they return; allocation sizes, integer narrowing and the Steam link patterns
  were tightened; the web test harness escapes what it splices into code.
  Details and the remaining false positives are in `SECURITY.md`.

- **A smooth focus bounce on the Smart Pro** (Juka, 2026-11-17: "the bounce
  effect looks choppy on TrimuiSmartPro"). The handheld draws its menus at 30
  frames a second, so the highlight's 300 ms bounce was nine frames with the
  overshoot and swing back squeezed into three. While the highlight travels the
  handheld now draws as fast as it can, up to 60 frames a second, only while
  the highlight moves (Battery Saver keeps its cap). Holding a direction, or
  tapping quickly, used to restart the overshoot every 70 ms so the highlight
  lurched; it now glides after the focus at an even speed and settles without
  overshooting — a single step still bounces (`player/homefocusglide.go`,
  `player/adaptive.go`).

- **JukaHub OS boots straight into JukaHub.** With the OS package the
  handheld used to show the stock menu for a few seconds and then open JukaHub
  like an ordinary app; on a card that already had JukaHub with Start at login
  off, JukaHub even removed the OS boot hook at its first start. The firmware
  now starts JukaHub OS in place of its menu for that one boot (Settings →
  JukaHub OS → Skip the stock menu at boot turns that off; if the firmware
  can't run the stand-in within 20 s the old way takes over), the package's
  boot hook is kept, and Settings → JukaHub OS shows what the session applied,
  or why a start was the app. The package ships no drivers (the kernel's
  drivers are the firmware's own); Settings and the README now say so instead
  of "0 drivers loaded".
- **The interface no longer hangs** (Juka, 2026-11-17: "fix UI hangs …
  always responsive"). Video thumbnails download in the background (a dead link
  is retried once a minute, not every frame); Quick Settings sliders and
  switches no longer wait for a settings save still being written to the SD
  card, and theme changes save in the background; videos no longer stutter
  every five seconds while their position is saved, and coming back from a
  game no longer pauses to write the game history; the Wi-Fi and Airplane
  switches respond at once (the last press wins; a failure puts the switch back
  with a message); the top-bar Wi-Fi icon, the idle screen's and low-battery
  guard's "is a game running" check and the Downloads storage card no longer
  hitch the screen. If the interface ever stops for more than 1.5 s, the log
  records where it was stuck.

- **Bluetooth headphones: a BlueALSA that is running but not on D-Bus is
  dealt with.** A Smart Pro report (JH-RT2AJY) showed bluealsa running with
  `-p a2dp-source` while D-Bus said `org.bluealsa` was owned by no one, so the
  headset never opened ("No such file or directory"). JukaHub now follows the
  daemon to a `-B`-suffixed name (command line or the bus's own list), wakes a
  stopped one once, and restarts one that stays off the bus for 20 s (5 min
  apart, at most 3 times a run), instead of opening the headset into an
  error. What a bluealsa JukaHub starts prints is kept in
  `/tmp/jukahub-audio/bluealsa.log` and its last lines go into the Bluetooth
  diagnostics. When ALSA does not know the `bluealsa` PCM at all (ENOENT: an
  `alsa.conf` that never loads `alsa.conf.d`, or a plugin outside ALSA's
  folder), JukaHub defines bluez-alsa's PCM under its own name
  (`jukahub_bluealsa`, plugin by path) for its process and the games it starts
  on the headset — once per run, never over an `ALSA_CONFIG_PATH` the firmware
  set, kept away from other children, and undone at once if the retry still
  does not reach BlueALSA.
- **Battery "Cold" no longer tells you to stop charging.** The Smart Pro's
  gauge reported health "Cold" in normal indoor use and Diagnostics said
  "stop charging it and have the pack checked". "Cold"/"Overheat" are now
  findings only when the cell's temperature agrees; otherwise the line says
  "flag only" with the temperature. "Dead"/"Failure" keep the warning.
- **jukauser.json is no longer rewritten when nothing changed, nor ten times a
  second from Accessibility rows.** A save whose bytes match the file this run
  last wrote (same file, untouched since) is skipped, history snapshot
  included; Accessibility settings go through the debounced writer like
  every other Settings row.
- **Diagnostics' frame-time length read "31m8000000000s".** The seconds were
  printed in nanoseconds; now "31m8s".
- **Hey Juka stopped swapping microphones in a quiet room.** A device that gave
  sound earlier in the run now gets 6 s of quiet instead of 1.2 s before the
  next one is tried; the report showed the codec's two names traded back and
  forth all evening, each swap closing a device on the drawing thread.
- **Diagnostics name where RetroArch was found** ("RetroArch : ra64.trimui
  found by its folder", or "not found"), from the last search only — the
  first question behind "The launch script could not find RetroArch".
- **Automatic box art no longer re-reads a console's cover list for every
  game it cannot get.** When the thumbnail server's list named a cover the
  server then did not give (a link to a file that is gone, a file that is not
  a PNG), the list was thrown away and fetched again — for each such game, on
  every pass — and those games stayed "unanswered" forever (report JH-SQRMFB,
  2026-11-15: an Atari Lynx list read a dozen times, 23 games left over). A
  list read from the server within the last day is now believed: the cover is
  tried by the names guessed from the file, then filed as not found. When
  several downloads find the same old list, the first refresh is kept
  (`player/artwork_match.go`).
- **Diagnostics wrote "30m3000000000s" for a frame-time window** of 30
  minutes 3 seconds, and the log "last 1m1000000000s" (reports JH-SQRMFB
  and JH-RT2AJY; `shortDuration` in `player/framestats.go`).
- **Screen time is written to the card every two minutes instead of every 30
  seconds.** Each save rewrites the whole `jukauser.json` on a FAT32 card with
  no journal; a handheld sitting on Home did it 120 times an hour. Trophy
  unlocks still save within 30 seconds, and quitting saves everything
  (`player/playtime.go`).

- **PSP games: the pad works and Menu gets you out.** JukaHub's PPSSPP
  mapped Pause to a code PPSSPP's SDL front end never sends, so nothing on the
  pad opened its pause menu; Menu does now (and its Exit comes back to
  JukaHub), and a settings folder an older JukaHub wrote gets it added. The
  launcher also hands PPSSPP the TrimUI pad's own controller mapping, so PPSSPP
  treats the pad as a game controller even when its controller database does
  not know it — without that only the keyboard mapping was left. A PSP game in
  JukaHub's PPSSPP that is ended with Select + Start within a minute of
  starting marks that build as not working here, so the next PSP game opens in
  the firmware's PPSSPP with a toast; choosing JukaHub's PPSSPP on the PSP tile
  tries it again (`player/ppsspp_trimui_setup.go`, `player/ppsspp_managed.go`).

- **PSP games start again on the TrimUI Smart Pro.** When JukaHub's own
  PPSSPP was the PSP emulator, its launcher ran with JukaHub's home folder
  and libraries instead of the firmware's, so even the firmware PPSSPP it
  falls back to opened with a blank configuration. PSP games get the
  firmware's environment again; the fallback starts the firmware launcher the
  way a normal launch does; a game started in the first seconds after boot no
  longer marks JukaHub's build as broken; and a build that does not start says
  why: PPSSPP's last lines are in the toast, the launch card and
  `jukahub-ppsspp.log` in the build's folder (`player/ppsspp_managed.go`).
- **RetroArch games and saves are found on the handheld.** WonderSwan, Neo
  Geo Pocket, Lynx, Atari, CPS, FBNeo, MAME and other systems said "no
  launcher" even though the card had their emulator folder (Emus/WS,
  Emus/NGP…); the Emus folder named like the game's Roms folder is now tried
  first. Saves, save states and recordings written as `~/…` in
  `retroarch.cfg` were looked for in JukaHub's app folder instead of
  RetroArch's. Games started after JukaHub opens at boot now get the
  firmware's tool and library folders (CrossMix's `System/bin` and
  `System/lib`). When something is still missing, the launch card names the
  missing library or where it looked for RetroArch, and the log lists the
  folders checked (`player/game_library.go`, `player/savemanager.go`,
  `player/childenv.go`, `player/gamelaunch_firmware.go`).
- **Wi-Fi reconnects by itself on TrimUI.** The stock menu is what fetches an
  address after the radio joins and re-selects a saved network after a drop;
  it is not running under JukaHub, so nothing did. JukaHub now does it
  (`player/wifi_keeper.go`): saved networks are joined again at start-up,
  after sleep and when the router comes back, with backing-off retries, never
  after you pressed Disconnect or turned Wi-Fi off, and never on a network
  whose password was refused twice. Connecting from the Wi-Fi page works first
  time more often: it waits longer for the slow radio, no longer asks again
  for a password that was right, always fetches a fresh address, and never
  starts a second wpa_supplicant. Each attempt is logged as `wifi: keeper:`.
- **CI no longer sits on "Build optional offline AI".** The reuse check
  still looked for the old 360M model after the switch to the 135M one, so
  every push rebuilt llama.cpp; it now reads the model pins from
  `build-llm.sh`. Every phase of the build is bounded: a stalled download is
  cut off and retried (it could hang for 40 minutes), the compile and smoke
  runs have time limits, and `llama-server` is killed if it ignores SIGTERM
  instead of a bare `wait`. Each phase prints its elapsed time.
- **Offline answers no longer stop at "3." in "3.14" or at "U.S.",** a long
  or non-Latin question no longer makes every offline answer fail, and the
  model is not loaded in the background when a game starts.
- **Offline game hints start faster:** the model preloaded while you talk is
  kept until your question arrives, and the in-game idle limit only starts
  after an answer.
- **Settings no longer offers an offline AI update that would download the
  same files again,** and a fixed package re-enables the warm server.
- **Hey Juka no longer switches itself off for the session when the SD card
  is slow at startup,** and it no longer reloads the speech model again and
  again during games that hold the microphone.
- **Offline game hints are read out to the end** even when the model writes
  slowly; "Dr.", "Mr." and similar no longer end a spoken sentence.
- **A stalled offline-voice download stops after a minute** with a message
  instead of sitting still for half an hour, and if Groq's voice does not
  answer within 10 seconds the offline voice takes over.

- **Restores and reloads keep current settings.** Backup and Recovery now
  merge against the live settings and playtime, a temporary read failure
  keeps the latest saved values, and restoring a config-history snapshot
  updates the running app before its next save. An explicit five-minute
  screen timeout also survives a reload.
- **Box art reached only the first consoles; numbered PSP ISOs got none.** The
  fetch walked the card in path order one cover at a time, so on a big card
  (8,700 games) it spent hours on 32X and NES and Super Nintendo, PSP and the
  rest never got a turn before a game or the batch limit cut it short. It now
  takes the consoles in turns of 25 games (the one shown in Games first when
  you press Fill gaps) and downloads four covers at once. Scene-set names such
  as "0381 - 007 - From Russia with Love.iso" are matched without the
  release number, the placeholder no longer reads "0-", and titles filed as
  "not found" before are asked once more.
- **Bluetooth headphones on the Smart Pro got no sound.** The firmware never
  starts BlueALSA, so a headset connected without an audio link and every
  sound fell back to the speaker. JukaHub now starts BlueALSA as soon as
  Bluetooth is up, restarts one without the A2DP source role, asks a connected
  headset for its audio profile, and sets LIBASOUND_THREAD_SAFE=0 on TrimUI
  (BlueALSA stalls otherwise). Games started while the headset is in use play
  through it too (a JukaHub-owned /tmp ALSA file for that launch only; no
  system file changes). The Bluetooth diagnostics now show the headset's audio
  services, the BlueALSA daemon and streams, the exact open error and the
  route history.

- **PPSSPP did not update itself on the Smart Pro.** JukaHub now also checks
  at every start once the last check is two hours old (and at once after a
  JukaHub update), switches PSP games to an installed newer build at the next
  check, never goes back to an older PPSSPP, and the PSP card shows when it
  last checked and what it found.

- **Asking "is Wi-Fi on?" turned Wi-Fi on.** Questions about a setting now
  read it out.

- **CrossMix: RetroArch games start from JukaHub again.** Every system that
  runs in RetroArch stopped with RetroArch "not found" on CrossMix, while PSP
  (PPSSPP's own script) played. MainUI gives a system's launch script the
  game as `Emus/<system>/../../Roms/<system>/<file>`, and CrossMix's RetroArch
  launchers (its shared `common_launcher.sh`) work out their folders from that
  argument; JukaHub passed the plain `/mnt/SDCARD/Roms/...` path. A script in
  an `Emus/<system>` folder now gets a game on the same card in MainUI's form
  (the same file, so the stock scripts and PPSSPP are unaffected). A launch
  script's own output is also kept now (`gamediag/launcher.log`, emptied each
  launch, capped at 1 MB): when a game closes within seconds the card says
  what the script could not find ("The launch script could not find
  RetroArch") and shows its last lines, where before the message went
  nowhere.
- **TrimUI Brick Pro on CrossMix: games that did not start said nothing.**
  Every game answered with "Playing …" and the game page again: its launch
  script was over within a second or two with exit status 0 (or 137, a
  program the system killed), and JukaHub took both for the player quitting.
  The launch card now also opens on the handheld for a library game that is
  over within three seconds, or that a signal JukaHub did not send ended,
  unless JukaHub stopped it itself (the in-game menu's Quit, quitting JukaHub,
  the low-battery save). It says which signal ("the system stopped it (signal
  9, SIGKILL)", "it crashed (signal 11)"), how much memory was free when that
  may be why, and the script's own last lines. JukaHub's log (`errors.txt`)
  now has a line for every library launch — the exact command, its folder,
  the pid and the memory free — and one when it ends, with how long it ran
  and its status. A launch script also runs under the interpreter its `#!`
  line names, as MainUI runs it (bash for a bash script; a launcher that is a
  program runs as itself), instead of always under sh. The likely cause, a
  CrossMix launcher that reads its folders from MainUI's form of the game
  path, is the fix above.
- **Saved videos and tracks finish again.** A download in Media → Downloads
  that completed was read as paused (the runner cancelled its own context and
  then asked it whether the queue had stopped the job), so the row went back
  to Queued and yt-dlp ran again, forever, and nothing reached "Saved". The
  queue now also stops yt-dlp's whole process group (a paused or stalled row
  no longer hangs), counts a growing staging file as progress so ffmpeg's
  quiet join of a long video is not killed as a stall, stops sending YouTube a
  desktop browser's name (a 403), passes the QuickJS runtime and JukaHub's
  ffmpeg when they sit next to yt-dlp, saves H.264/AAC at the playback quality
  instead of the biggest (often AV1) stream, logs why a download failed and
  checks for a newer yt-dlp after a YouTube refusal (`player/downloads.go`).
- **Stick rings blinking every two seconds.** The rings' drift check ran every
  2 s, and every repaint it made goes through effect 0 — a visible blink —
  whenever something (a firmware service, a node that reads back oddly) made a
  check see a change; a quiet check also reset the back-off, so a service
  changing them every other check kept the blink going. The check now runs every
  10 s, backs off to 60 s after two repaints in a take and never resets, and a
  switch found off is turned back on by the repaint instead of in the check. The
  driver's state is logged once per take (scene `led`).
- **Games with no exact box-art match got no picture.** The cover scraper
  only took a cover whose title was the ROM's own title, so files named
  "Zelda Minish Cap", "Pokemon Saphire", "Sonic 2" or "SuperMarioWorld2"
  stayed blank. When no exact match exists it now takes the closest title on
  the server's list (the one sharing the most words, near-misses and
  joined-up names included; a sequel's number counts), as long as it is
  plausibly the same game or series; a title that shares almost nothing still
  gets no cover rather than a random one. Games skipped before are tried again
  on the next fetch.

- **Stick lights stayed dark on the TrimUI.** The firmware leaves
  `/sys/class/led_anim` read-only (0444), and sysfs refuses a write open on such
  a node even for root, so JukaHub never found the rings: Settings → Stick
  lights only said the driver could not be written, and the switch failed.
  JukaHub now makes the nodes writable first (what every CFW does with
  `chmod -R 777`). It also checks every 2 s that TrimUI's hardwareservice has
  not taken the rings back (master switch off, brightness, effect or colour
  rewritten on charge or wake) and repaints them, so a steady colour stays put.
  The rings no longer blink every couple of seconds: a colour change re-arms
  the driver through effect 0, which shows as a blink, and Pulse and Charging
  flipped the computed hue at a rounding step once per 2 s breath. Small hue
  wobbles are now ignored, nothing is rewritten unless it reads back changed,
  and a node whose read-back never matches what was written stops being
  watched. If something keeps taking the rings back, JukaHub checks only every
  30 s instead of fighting it, and the log names the node it keeps changing.
  (`ledanim.go`.)

- **Settings → Stick lights is tidier.** With the switch off only the switch
  is left; when on, it says what the lights are showing. **Lights show** lists
  Colour / Music / Microphone / Battery / Charging / Temperature and its value
  uses the same names (it read "Custom" and "Audio" before). Colour, Effect and
  Colour wheel appear only in Colour mode, Mic sensitivity only in Microphone
  mode, and Brightness for every mode, last. The **Lights follow your voice**
  switch is gone because it repeated Lights show → Microphone. A swatch colour
  shows its name instead of "Custom · #FF0000". (`ledsettings.go`, `ledmic.go`.)

- **Bluetooth pass (switch, pairing page, headphones, controllers).** Switching
  Bluetooth off in Quick Settings no longer freezes the panel: it runs in the
  background, a later On press wins over it, and the switch follows the radio
  afterwards. With no adapter or no Bluetooth service running, switching off or
  reading the radio asks the kernel instead of waiting out bluetoothctl's
  timeout, and the bring-up re-checks rfkill through sysfs when TrimUI's rfkill
  tool reports success without unblocking. On the Bluetooth page every
  bluetoothctl session waits (up to 2 s) until it has the adapter before typing,
  so actions no longer fail with "no Bluetooth adapter answered" right after the
  service starts; prompts of devices with brackets in their name ("[TV]
  Soundbar") are recognised; when the focused row disappears the focus stays in
  place instead of jumping to the Bluetooth switch (where the next A turned
  Bluetooth off); no search or re-read starts after the page was left; a
  successful search clears the old "Could not read the paired devices" card.
  Headphones no longer lose up to ~0.1 s at the start of each sound (the mixer
  padded short top-ups with silence), a headset that connects or drops while the
  screen saver runs moves the sound, and PS Remote Play's sound follows a
  connected headset. On the handheld a controller paired while JukaHub runs is
  now opened alongside the built-in buttons; before, its presses went nowhere.

- **Offline voice: "voice settings download: checksum did not match".** The
  pinned digest for Kristin's small settings file (`en_US-kristin-medium.onnx.json`)
  was wrong, so every install failed after the 63 MB model had downloaded. That
  file is now checked for the shape Piper reads (sample rate, English espeak
  voice, phoneme table, at most 256 KB) instead of a digest; Piper's archive and
  the model stay pinned by size and SHA-256. A retry reuses the already verified
  archive and model from `required/.juka-voice-download/`. The unpacked engine
  must now also contain espeak-ng's phoneme tables, and Piper runs with its own
  folder on `LD_LIBRARY_PATH`. (`tts_install.go`, `tts_local_other.go`.)

- **Video and sound stay together.** Some videos — downloads especially —
  played with the sound ahead of or behind the picture, sometimes drifting
  further as they went. The player's picture and sound decoders were never tied
  to each other: the picture was timed by a frame rate ffmpeg did not actually
  use (off on variable-rate files), streams that start at slightly different
  times kept that offset, every hiccup over a quarter second left the picture
  behind for good, and a seek played up to a second of the old position's
  sound first. Now the picture follows the sound: the decoder is pinned to the
  rate the frames are timed by, both decoders start exactly at the seek point
  (silence or a held first frame fills any gap), the first frame waits for the
  sound, a late picture catches up, and a seek clears the old sound. A
  Bluetooth headset's extra delay is allowed for. Without sound the picture
  runs on its own clock as before (`player/avsync.go`).
- **Background music that started by itself can be found and stopped.** With
  Background music set to **My music**, the menus played Calm until ffmpeg was
  installed; the first **Extract audio** (or video) installed it, and from then
  on your own tracks — including the extracted ones — played under the menus
  with nothing on screen saying what they were. Now a toast names the music the
  first time your own songs play, or when the music changes by itself, and says
  where to stop it; **Start → Quick Settings** has a **Background music**
  switch; and saying "stop the music" turns it off when the music player is
  idle.

- **A RetroArch game keeps its sound after the in-game menu (Brick report).**
  Opening the in-game menu, the game assistant or an alarm over a game froze it
  with SIGSTOP in the middle of playing; its ALSA stream ran dry while stopped,
  and RetroArch drops its sound for the rest of the session the first time its
  audio driver fails a write, so the game played on silent after Resume. A
  RetroArch game the library started now pauses itself first over its command
  port (PAUSE_TOGGLE, confirmed with GET_STATUS), the same audio stop/start its
  own menu does, and is unpaused once it runs again — only while it says PAUSED,
  with one owner for overlapping freezes, so a toggle never pauses a running
  game. After the hand-back the new part of RetroArch's log is checked and any
  audio-driver errors go to JukaHub's log (`gamepause` category).
  `player/gamepause_ra.go`; hooks in `gameassist.go` and `alarmvol.go`. Other
  emulators (PPSSPP, ports) are frozen as before. Not yet tried on a device.

- **YouTube streams again once JukaHub's own ffmpeg is installed (TrimUI).**
  The static ffmpeg/ffprobe the tool installer fetches has OpenSSL and, like
  current ffmpeg, checks certificates — but it looks for the CA bundle under
  the folder it was built in, which does not exist on the handheld. Every
  googlevideo URL failed with "certificate verify failed", so videos only
  played after a full download (report JH-AUBDUA). JukaHub now exports the
  bundle the firmware has (or a `cacert.pem` next to the app) as
  `SSL_CERT_FILE`/`SSL_CERT_DIR` for the tools it starts, and if a tool still
  fails a certificate check, https streams go through the local relay (Go's
  TLS) for the rest of the run — ffprobe retries that way at once, so the
  video that hit it streams too.
- **RetroArch is found wherever it is installed.** On Windows, Netplay never
  looked at the Emulators catalog: it searched only for a `retroarch` with no
  `.exe` beside JukaHub and on PATH, so it said "RetroArch not found" even when
  JukaHub had installed RetroArch itself. It now uses the catalog's RetroArch.
  The catalog also finds more installs: folders installers recorded in the
  Windows registry (any drive, any folder name), every Steam library (not only
  the one in Program Files), a Scoop install, `RetroArch-Win64` folders (for
  example one unzipped to Downloads), and portable copies in Desktop, Downloads,
  Documents, `C:\RetroArch`, `C:\Emulators` or `C:\Games`. That RetroArch's
  `cores`, `config` and `retroarch.cfg` are used for cores, overrides and
  RetroAchievements. On the handheld, the RetroArch tile and its cores also come
  from the card JukaHub runs from, `Apps/RetroArch` or `/usr/trimui/RetroArch`
  when `/mnt/SDCARD/RetroArch` has none, and core installs, overrides and
  config readers follow that same RetroArch; a `retroarch64` binary counts too
  (`emuwinregistry_windows.go`, `winExtraSearchDirs`, `deviceRetroArchCoresDir`).
  The registry and Steam-library scan is cached and refreshed in the
  background, and the RetroArch lookup for a few seconds, so per-frame callers
  (Cores tab, Netplay panel) stay cheap.

- **Bluetooth headphones get JukaHub's sound.** A connected headset stayed silent: every
  output (UI sounds, Juka's voice, video and music, background music, the synth) was opened once
  on ALSA's default, the speaker, and nothing ever moved it. Pointing each at the headset would not
  have worked either, because BlueALSA takes only one client per headset. Outputs now go through
  `audioroute.go`: on the speaker they behave as before, and when an A2DP headset is connected
  they all feed one small mixer that owns the single headset stream
  (`bluealsa:SRV=org.bluealsa,DEV=<addr>,PROFILE=a2dp`), resampling and summing them. The output
  monitor reads the kernel's link list every 2 s and switches within seconds of a connect, a drop
  or a Bluetooth-page action. It retries while the headset's stream appears and starts a missing
  `bluealsa` once (then reconnects the headset). Games and apps get the headset: JukaHub lets go of
  it when a screen-taking child starts, gives SDL children `AUDIODEV`, and takes it back when the
  game exits. JukaHub's volume control scales the headset too. The Bluetooth diagnostics block has
  an "Audio route" line. Not yet tried on a device; Windows and desktop Linux are unchanged.

- **PS Remote Play Play button now streams from a renamed JukaHub folder.** The release zip unpacks
  as `Apps/JukaHub-Trimui`, but the stream's log, helper folder and Brick runtime were looked up in
  a hard-coded `Apps/JukaHub`. The log redirect into that missing folder made the shell skip
  `chiaki-stream` entirely, so JukaHub closed, came back with "Remote Play did not start", and no
  `psremote.log` existed. Every Remote Play path now hangs off the folder JukaHub runs from; the
  hand-off script probes its log and falls back to `/tmp/psremote.log`, records each step in the
  stream note (shown in the toast when the stream never starts), keeps the helper's libraries and the
  OpenSSL preload (now `LD_PRELOAD`, as pairing uses) out of its own commands, and copes with a
  busybox `sleep` without fractions.
- **APK Store appears in Packages on Windows.** It can inspect APKs from the
  PC's Downloads folder and download verified APKs there; Start → Games and
  Home search also show it. AndroidRT playback remains on the handheld.
- **One name for you everywhere.** Settings → Your name, the chat's name chip and the Home greeting
  are one setting: change it in either place and the other follows, and Home now says "Good
  afternoon, Andy" (it looked the name up under a key Settings never writes). Clearing it, or typing
  "You", signs the chat as "You" again. A chat name set on an older build carries over.
- **The Continue card follows the theme.** With no thumbnail it was a fixed navy card with a violet
  glow, and its label scrim was black even over thumbnails, so Paper got a blue card with dark ink on
  near-black. It now uses the theme's surfaces, backdrop blooms and page colour.
- **Settings audit.** Volume, Brightness, Text size and the Accessibility delays stop at their ends
  instead of wrapping (one more Right no longer mutes the speaker; one more Left no longer sets full
  brightness). Display → Text size confirms the new size; list sheets no longer pick a row hidden
  under their title; cut-off Accessibility and "Set time automatically" details are shorter.
  "Startup & shutdown sound" is greyed with a reason under Quiet Mode; Network rows that need
  Nearby devices say so; the Games button-swap rows mention the Accessibility swap that cancels
  them; About → Keep previous versions asks before deleting saved versions; a plugin switched on in
  Settings runs its enable hook; break reminders give the real time played; Clear cache reports a
  failure instead of always "Cache cleared".

- **Bluetooth can start the XR829 on the TrimUI Smart Pro when the firmware
  left it off.** The fallback now enables the chip's `btwrite` wake control,
  resets its power through the firmware's rfkill `state` file, and starts
  `rtk_hciattach` at 115200 baud if the stock `hciattach`
  is unavailable. Chip-specific firmware scripts are preferred over generic
  ones, TrimUI's `rfkill.elf` is recognized, and diagnostics record the wake
  control's value. A failed attach restores the previous wake state
  (`player/bluetooth_attach.go`, `player/bluetooth_bringup.go`,
  `player/systemcontrol.go`, `player/bluetooth_diag.go`).

- **Box art is found for games again, automatically and from Fill gaps.** The
  fetcher only guessed file names, so a ROM not named exactly like the
  No-Intro title on the libretro thumbnail server ("Super Mario World.sfc",
  "Pokemon Emerald (U).gba", "The Legend of Zelda - …") never got a cover, and
  the month-long not-found cache kept it that way. It now reads the server's
  cover list for each console once (kept on the card for two weeks under
  `data/game-art/.server-index`) and matches against it: exact name, then the
  title with tags, "The", "&"/"and", punctuation and Roman numerals folded,
  then "Version"/"Edition" and spaces ignored, then a subtitle the ROM's name
  left out; the ROM's region, disc and revision tags pick the release
  (otherwise USA, World, Europe, Japan; betas and demos last). A title the list
  does not know gets one more try through the game database's CRC match when
  that database is already on the card. Also fixed: requests used Go's default
  client, so on a firmware with no certificate bundle every one failed and the
  automatic pass backed off for good (now the firmware CA fallback, then plain
  http for these public pictures, as RetroArch does); the name guesses spent
  half their budget on underscored names the server does not have and never
  applied its rule that `&`, `:`, `?`, `*` and similar characters become `_`; Atari, Sega CD and arcade
  games asked for folders the server does not have ("Atari - 2600",
  "Sega - Mega-CD - Sega CD", "MAME" now); covers the firmware or CrossMix
  already keep in `Roms/<console>/Imgs` (or MinUI's `.res`) are shown and no
  longer looked up; a cover cut short by a power cut no longer counts as
  present. CrossMix's `ATARI`, `PICO`, `CPS1-3` and `MEGACD` folders are now
  scanned. The not-found cache is reset once so every title is asked again.

- **Bluetooth on the TrimUI Smart Pro: the chip is found and started.** With
  TrimUI's own switch off there is no adapter (`hci0`) at all, and JukaHub's
  bring-up relied on a generic Allwinner script (`/etc/bluetooth/bt_init.sh`)
  that no TrimUI image was shown to have, so Turn on, Search and Connect ended
  in "Bluetooth is not started". On the Smart Pro, Brick, Brick Pro and Hammer
  JukaHub now attaches the XR829 chip on `ttyS1` itself when no script does
  (rfkill power pulse, then `hciattach -n ttyS1 xradio` or
  `rtk_hciattach -n -s 115200 ttyS1 xradio`, a stuck attach stopped first),
  and also tries chip-named scripts such as `bt_init.sh_xr829`. Every Bluetooth
  tool (`bluetoothctl`, `rfkill`, `hciattach`, `bluetoothd`) is now found
  beyond the launcher's PATH (system sbin/libexec directories,
  `/usr/trimui/bin`, CrossMix's `System/bin`) and run with the firmware's
  library path; `bluetoothctl show`/`power` used to run with JukaHub's. The
  connected-headset name no longer asks BlueZ 5.54 for `devices Connected`,
  which it refuses. A **Bluetooth** section — tools and where they are,
  adapters and links, HCI_UP, rfkill switches, the chip port, bluetoothd /
  hciattach / trimui_btmanager processes and their command lines, pairing
  stores per adapter address, `system.json`'s Bluetooth keys — is written to
  the log when the Bluetooth page first opens and on every failed bring-up,
  and rides in the diagnostics bundle (`player/bluetooth_tools.go`,
  `player/bluetooth_attach.go`, `player/bluetooth_diag.go`,
  `player/bluetooth_bringup.go`, `player/systemcontrol.go`,
  `player/audiooutput.go`).
- **Quick Settings no longer leaves Bluetooth unpowered behind an rfkill
  block.** With the radio soft-blocked, the switch lifted the block but never
  asked the adapter for power again, so it went back to off a moment later;
  switching on now unblocks and then powers the adapter
  ([player/quicksettings.go](player/quicksettings.go),
  [player/bluetooth_bringup.go](player/bluetooth_bringup.go)).
- **A Bluetooth device that does not connect gets advice on the stock
  firmware.** BlueZ 5.54, which the TrimUI handhelds run, reports a failed
  connection only as "Failed", and the page said "Bluetooth said: Failed"; it
  now says the device did not answer — switch it on and keep it close — and
  newer BlueZ's reasons ("Host is down", "le-connection-abort-by-local",
  "Connection refused") get advice of their own
  ([player/bluetooth_devices.go](player/bluetooth_devices.go)).
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
  them with the other textures (`player/brw_wpe.go`, removed 2026-11-19;
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
  browser (`player/brw_wpe.go` and `player/brw_wpe_install.go`, both removed
  on 2026-11-19 when the Browser went back to built-in only).

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
  path the firmware and MinUI use), in the firmware's own ten steps,
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
  the reading MinUI uses) now counts too, and a latched "Full" no
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
  steps measured on the same codec), mutes for real through "DAC
  Volume", reads the level back from the codec so it follows the hardware
  keys (headphones included), and keeps the firmware's own level in
  /mnt/UDISK/system.json in step so the keys carry on from it. The next level
  JukaHub sets lifts any mute, and the hardware keys lift one JukaHub made;
  the alarm's temporary 100% is no longer written to the firmware's settings;
  a level set just before quitting still reaches them; and JukaHub no longer
  puts its own saved level over the firmware's at launch. Quick Settings'
  writes moved off the render thread too. The mixer names and the curve come
  from MinUI's work on the same hardware and still need a check on a
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
