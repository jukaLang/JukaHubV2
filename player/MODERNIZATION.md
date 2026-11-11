# JukaHub engineering backlog

This is the **one task list** for the JukaHub player, the web builder and the
AndroidRT runtime. It holds only open work. Shipped work is described by the code,
the docs under [`docs/`](../docs/) and the [CHANGELOG](../CHANGELOG.md); it is not
tracked here, so nothing in this file is ever "done".

Last full rewrite: **2026-11-09**, after the November pass (startup/shutdown
chimes, test cut, legacy log removal, dead-code sweep, perf/GPU pass, AndroidRT
JNI + Minecraft class pack, hakuX Xbox on the Smart Pro, UX review). The Windows
build of that pass compiled the same morning; `make check` and the device are
still ahead — start with [BUILD-1](#build-1--first-compile-of-the-november-pass-p0-s-win).

Extended **2026-11-09 (afternoon)** with the
[North star](#north-star-the-best-handheld-os): a benchmark against the best
handheld systems of 2026, the defects a whole-code inventory found
([FIX](#p0p1--defects-found-by-the-2026-11-09-inventory-fix)), and new areas —
input, shell, power, reliability, accessibility, languages, network, library,
apps and Windows — 144 new tasks. Sections:
[P0](#p0--build-and-first-hardware-run) ·
[FIX](#p0p1--defects-found-by-the-2026-11-09-inventory-fix) ·
[PERF](#performance-perf) · [GPU](#graphics-and-gpu-gpu) ·
[ARCH](#architecture-and-build-arch) · [TEST](#tests-and-ci-test) ·
[UX](#ux-and-design-ux) · [GAME](#games-and-emulation-game) ·
[PSR](#ps-remote-play-psr) ·
[XBX](#xbox-on-the-smart-pro-xbx) · [AND](#android-games-and) ·
[MED](#media-med) · [SYS](#system-and-connectivity-sys) ·
[SND](#sound-and-haptics-snd) · [WEB](#web-builder-web) ·
[SEC](#security-and-privacy-sec) · [DOC](#docs-and-project-doc) ·
[INP](#input-and-controls-inp) ·
[SHELL](#shell-multitasking-and-notifications-shell) ·
[PWR](#power-sleep-and-battery-pwr) ·
[REL](#reliability-updates-and-recovery-rel) ·
[A11Y](#accessibility-a11y) · [I18N](#languages-and-locales-i18n) ·
[NET](#network-and-connectivity-net) ·
[LIB](#game-library-and-emulation-lib) · [APP](#apps-platform-app) ·
[DESK](#windows-desktop-desk) · [HW queue](#hardware-validation-queue) ·
[Parking lot](#ideas-parking-lot).

Afternoon pass, part 2 (**2026-11-09**, unbuilt): the boot guard counts failed
starts instead of trusting the clock, a crash leaves a report the next start
offers, and the recovery chord opens a recovery card; Settings → Date & time with
an on-device zone picker and network time; FTP/SFTP with passwords, confined
shares and auto-stop, and a live builder that is off on the handheld,
rate-limited and holds risky designs for the owner's approval; `.bat`/`.cmd`
launches quoted for cmd.exe; verified, staged package installs with a rollback,
and core updates that notify; hints that follow Accessibility remaps; AndroidRT's
SDL-hosted present fallback. Every workstream had an independent review whose
findings were fixed, but none of it is compiled yet —
[BUILD-1](#build-1--first-compile-of-the-november-pass-p0-s-win) covers it.
Finished tasks were deleted, partly done ones rewritten, and the follow-ups
added. A same-day feature sweep of the shipped subsystems (music, recordings,
the browser, per-game launch options, Wine apps) added seven more — MED-15, MED-17, MED-18, APP-8, APP-9, GAME-13
(two have since shipped).

Evening pass (**2026-11-09**, unbuilt): the TrimUI's X and Y fixed where every
press enters the app (with a **Swap X and Y** switch), a volume button and card
in the status corner with a tg5040 codec backend, the charging bolt, a
selectable Home pairing QR, the PS Remote Play keyboard and four other panel
keyboards, and YouTube resolution reworked (android client first, yt-dlp that
updates itself with a rollback, failures that name their cause). Follow-ups:
MED-19 to MED-25, SND-5, SND-6, INP-15 and INP-16; PWR-4 and INP-13 were
rewritten for what shipped: 264 tasks in the file.

Night fix (**2026-11-09**, unbuilt): a stream ffmpeg could not open used to
close the player silently (reported as "YouTube videos open a black canvas for
a second, but then crash"); a web video now falls back to the download and
anything else says why (`videorecover.go`), and the decoders ask as the browser
the stream was resolved for. Follow-ups: MED-26 to MED-29.

Files in chat (**2026-11-09**, unbuilt): the chat sends files — a tray above
the composer, Files in pick mode (X), real Discord uploads that stream from the
card, file cards with View / Play / Open, saving into Downloads, and text files
for Juka AI (`chatfiles.go`, `chatupload.go`, `chatfilesview.go`). Follow-up:
SYS-14.

Late-night pass (**2026-11-09**, unbuilt): the Browser runs page JavaScript
against a real DOM (`webdom_*.go`, `webdom/*.js`: a Go HTML parser, an ES-module
rewrite for goja, a network layer that keeps scripts off the LAN and away from
other sites' cookies, timers on a virtual clock), switched by Settings → Browser
→ JavaScript; the reader's CSS matcher now evaluates attribute selectors and
pseudo-classes instead of assuming they match, honours `hidden`, and no longer
hangs on a tag with a stray quote. Follow-ups: PERF-15, TEST-12, SEC-25, SEC-26,
NET-16 and APP-10 to APP-13; PERF-7 was rewritten: 275 tasks in the file.

Archives (**2026-11-10**, unbuilt): **A** on an archive in Files opens its
sheet, and **Extract** / **Extract here** unpack zip, tar, tar.gz / .xz / .bz2
and single .gz / .xz / .bz2 files in-process, and 7z and RAR through 7-Zip, on a
background queue with progress, cancel and a staging folder that is moved into
place at the end (`files_extract.go`, `files_extract_ui.go`); the old
unzip-on-A went. Follow-ups: SYS-15, SEC-27, APP-14 and APP-15; APP-4 was
rewritten.

Remote Play wake and Bluetooth controllers (**2026-11-10**, unbuilt): A on a
resting PS4 or PS5 wakes it (chiaki-ng's WAKEUP, sent only to the console's
address with the pairing key as credential), waits until it is up and streams;
X only wakes; a console that got a new address is followed by its MAC
(`psremote_wake.go`). Settings → Network → Bluetooth devices pairs controllers
and headphones through `bluetoothctl` (`bluetooth_devices*.go`); the stream
helper takes every pad, mid-stream ones too, with a PlayStation pad's touchpad
(`native/chiaki-stream.c`); and a helper the firmware cannot load is caught
before JukaHub quits (`psremote_preflight.go`). Follow-ups: the new
[PSR](#ps-remote-play-psr) section, PSR-1 to PSR-7; SYS-1 was rewritten for
what is left.

Remote Play controls, display and other networks (**2026-11-10**, unbuilt):
the helpers are finally built by CI and packed into every TrimUI archive
(`remote-play-helpers.yml`: an Ubuntu 20.04 root for glibc 2.31, with FFmpeg,
opus, json-c and OpenSSL linked statically); the stream maps every DualSense
input (a Menu tap is the PS button and the stream menu's Hold PS (Home) the
long press, Select the touchpad, Menu combos for Create, swipes, a finger and
the D-pad as the left stick), draws an on-screen display
(connecting card, a stream menu on a held Menu, passcode entry, battery
warnings, an error card), enters a saved login passcode, drives the TrimUI
motor and writes how it ended for JukaHub's next start (`native/stream_*.c`,
`psremote_lastrun.go`); a console's options take an internet address and a
passcode (`psremote_console_opts.go`, `psremote_stream_opts.go`); and JukaHub
estimates the battery's time left (`battery_eta.go`). Follow-ups: BUILD-4,
PSR-8 to PSR-11, TEST-13 to TEST-17 and PWR-9; PSR-2 and PSR-6 were rewritten
for the new helper: 299 tasks in the file.

Tester batch (**2026-11-10**, unbuilt): leaving JukaHub no longer starts a second
MainUI next to the firmware supervisor's (the "glitchy screen"), and PS Remote
Play hands the screen over through `launch.sh` and comes back to JukaHub;
children get the firmware's environment back (`childenv.go`); JukaHub's
`retroarch.cfg` edits last for its own launch only (`racfgscope.go`); emulator
cores install themselves by default after an ELF check that they can load
(`elfcompat.go`); a managed, auto-updating TrimUI PPSSPP (`ppsspp_managed*.go`);
mouse mode on hold Select + L1 and a uinput mouse for games (`vcursor_*.go`);
PSN sign-in from the phone for the Account-ID (`psn_signin.go`); WPE Browser
hand-off, install and a bigger ad filter (`brw_wpe*.go`, `brw_adblock.go`); and
background box art (`boxart_auto.go`). Follow-ups: HW-2, GAME-14 to GAME-16,
PSR-12, INP-17, LIB-19 and APP-16.

Liquid background, animated wallpapers and Screen Share (**2026-11-10**,
unbuilt): Settings → Display & theme → Liquid background turns the backdrop
into a wave-simulated pool that focus moves, the sticks, the pointer and tilt
stir, drawn through `SDL_RenderGeometry` (`liquidbg.go`, `liquidbg_motion.go`);
GIF wallpapers play, decoded frame by frame within a memory budget, and the
redone picker finally remembers the pick (`gifwall.go`, `wallpaper.go`); and
Remote Cast became Screen Share — games read from `/dev/fb0`, encoding off the
render thread, a view-only link token, quality presets and a TV-friendly
viewer (`castcap*.go`, `companion_cast.go`, `castui.go`). Follow-ups: GPU-5,
PERF-17, PERF-18 and SYS-18; UX-12 lost `castui.go`: 315 tasks in the file.

Feature pass (**2026-11-10**): promoted encrypted WebDAV/Nextcloud save backups
and user-directed save-sync conflict resolution from the parking lot to SYS-19
and SYS-20.

Quick implementation pass (**2026-11-10**): MED-21, MED-32 and DOC-2 are
shipped and recorded in the [CHANGELOG](../CHANGELOG.md).

Remote Play installs its own helpers (**2026-11-10**, evening, unbuilt): a
JukaHub without `bin/chiaki-stream` and `bin/chiaki-regist` (every install so
far) downloads `JukaHub-RemotePlay-linux-arm64.zip` from the newest release that
has it when PS Remote Play opens, checks it (GitHub's digest, a signed
`release.json` when there is one, its `SHA256SUMS`, one test start of each
helper) and installs the missing helpers beside the binary; a PS5 pairing waits
for it and Play streams afterwards (`psremote_helpers_install.go`).
`docker-image.yml` and `release.sh` attach the package; the helper lookup now
starts beside the binary, where an update puts `bin/`; PSR-4 is code-done.
Follow-ups: PSR-13 and PSR-14 (two new tasks); BUILD-4 got the first run's
fix (an empty libavformat for chiaki-ng's configure).

Faster TrimUI release build (**2026-11-10**, evening, not yet run on GitHub):
the handheld binary compiles in its own job, `build-trimui`, which starts at
once, runs the pinned toolchain image through `docker run` with no network
(`.github/scripts/trimui-build.sh`), carries the Go build cache between
releases like the Windows job and no longer builds six unused C libraries;
packaging waits for it and for the Remote Play helpers, which are reused when
their sources did not change (`go-build-cache.sh fetch`). TEST-14 was
rewritten for what is left. Follow-up: BUILD-5 (one new task).

## How to use this file (agents read this first)

1. **Pick** the highest-priority task you can actually verify. Tags tell you what
   a task needs: **[WIN]** a Windows machine with the Go toolchain
   (`player\build.bat`), **[HW]** a TrimUI device, **[NET]** internet access,
   **[OWNER]** a product decision from the owner (Juka) before code.
2. **Read** the task's *Where* files and the docs it names before writing code.
   Do not rebuild something that exists — the [map](#map-of-the-player) below
   says where every subsystem lives.
3. **Finish** when every *Done when* line holds and the *Verify* step passed.
   Then, in the same change: delete the task from this file, add a line to
   `CHANGELOG.md` → `[Unreleased]`, and add follow-up tasks you discovered (with
   *Why* and *Done when*).
4. **New ideas** become tasks only with a *Why* and a *Done when*; otherwise put
   them in the [parking lot](#ideas-parking-lot).

Priorities: **P0** broken or blocks everything · **P1** users feel it now ·
**P2** clear value, plan it · **P3** nice to have. Sizes: **S** ≤ ½ day ·
**M** ≤ 2 days · **L** ≤ 1 week · **XL** bigger, split it first.

### Ground rules that apply to every task

- **The device decides.** TrimUI Smart Pro: Allwinner A133P (4× Cortex-A53
  1.8 GHz), **1 GB RAM**, PowerVR GE8300 (GLES 3.2), 4.96" 1280×720 panel, 30 fps
  UI cadence. No per-frame allocations, nothing blocking on the render thread,
  every cache bounded by bytes *and* entries (see `docs/DEVELOPMENT.md`).
- **Look:** every screen uses Home's glass (`player/ui_glass.go` tokens,
  `docs/STYLE.md`); text ≥ 14 px at 1280×720 (≥ 12 px on the Brick's 960×720
  canvas, enforced by `uiLegibleFontSize` in `design.go`).
- **Logging:** never import `log`. Use `logDebug/logInfo/logWarn/logError(sys,
  format, args...)` or `Log()/LogScene(...)` (slog) from `logging.go`. Only WARN+
  reaches `errors.txt` on the device; the Logs screen shows the ring buffer.
- **Tests:** the suite is a lean core (~70 files): data integrity, security,
  updater/installers, parsers. Add a fast unit test when you touch those areas.
  No SDL/renderer/pixel tests (see `CONTRIBUTING.md`).
- **Modules:** never edit `go.mod`/`go.sum` without a Go toolchain (the
  git-ignored `vendor/` would become inconsistent). With one, finish with
  `go mod tidy && go mod vendor`.
- **No toolchain?** `tmp/gocheck/gocheck.py --tests player/` (git-ignored copy;
  see [TEST-6](#test-6--ship-the-static-checker-in-the-repo-p2-s)) catches syntax,
  undefined names, unused imports and gofmt alignment for linux/arm64 and
  windows/amd64. It does not type-check — read the code you call.
- **Line endings:** `.go/.json/.sh` are LF; most other text files are CRLF on
  Windows checkouts — preserve what a file has.
- **Honesty rules:** never fake a license, entitlement or purchase for a game or
  app (AndroidRT store answers stay "unavailable"); never bundle BIOS files, ROMs
  or copyrighted assets.
- **Both input vocabularies (owner rule, 2026-11-09):** when no controller with
  a D-pad is connected (a PC) or a keyboard is plugged into the handheld, every
  hint names keyboard keys. Hints go through `inputhints.go`: use `FooterHint`
  with the shared renderers (they translate), measure a badge you draw yourself
  with `hintKeyFor(button, label)`, write a pad button with its keyboard twin as
  `"X / C"` when they differ, and branch wording on `hintsUseKeyboard()` — never
  on `P().IsHandheld()`. A new key binding needs a keyboard twin (INP-1).

## Map of the player

What exists, by area, so nobody rebuilds it. `player/` is one Go `package main`
(~600 files) plus `internal/` packages and C code in `androidrt/`.

| Area | Where |
|---|---|
| Boot, splash, shutdown, chimes, recovery | `main.go`, `startup.go`, `bootwork.go`, `bootchecks.go`, `splash.go`, `bootos.go` (power-on vs launch boot), `bootarrive.go` (first screen arriving), `bootlogo.go` + `bootlogo_linux.go` (firmware boot logo), `bootlogo_layout.go`, `stageui.go`, `bootliquid.go`, `shutdown.go`, `exitguard.go`, `bootchime.go` (+ `branding/sound/`), `safeboot.go` (recovery chord), `bootguard.go` (failed-start counter, rollback), `crashreport.go`, `recoverycard.go` + `recoverysettings.go` (recovery card) |
| Render loop, pacing, caches | `main.go`, `adaptive.go` (`FrameRateController.Pace`), `framestats.go`, `textcache.go`, `shapeCache.go`, `iconatlas.go`, `thumbstore.go`, `cachebudget.go` |
| Design system | `design.go`, `ui_glass.go`, `components.go`, `themes.go`, `themeloader.go`, `motion.go`, `anim.go`; backdrop: `backgroundfx.go`, `dynamicbg.go`, `liquidbg.go` + `liquidbg_motion.go` (liquid background, tilt), `gifwall.go` + `wallpaper.go` (animated wallpapers, picker) |
| Home and navigation | `home*.go`, `homelayout.go`, `homefocus.go`, `focus.go`, `navrepeat.go`, `navmode.go`, `startmenu.go`, `quicksettings.go`, `globalsearch.go`, `homesearch_*.go` |
| Input | `controller.go`, `brickkeys.go`, `inputhints.go` (pad or keyboard words in every hint, keyboard detection), `inmap*.go` (input mapping language), `vkeyboard.go`/`vk*.go`, `touchtarget.go`, `exit_chord.go`, `shortcuts.go` (keyboard sheet, global F1/Ctrl+Q), `voicecmd*.go` (Voice command: capture, intent parser, resolver, the companion's `/voice` page) |
| Settings | `setmodel.go` (rows), `setui.go` (page), `a11y.go`, `powermode.go`, `powerprofile.go`, `datetime.go` + `tzpicker.go` (Date & time, zone picker) |
| Games | `game_library*.go`, `emulators.go`, `emu*.go`, `emulatorinstall.go`, `hakux*.go` (Xbox on Smart Pro), `retroachievements.go`, `savemanager*.go`, `savestate_resume.go`, `gamesuspend.go`, `gmeta_*.go`, `romsetup.go`, `romm_*.go`, `ports.go`, `netplay*.go`, `hotseat*.go`, `trophy_*.go`, `playtime.go`, `wrapped.go` |
| Android games | `androidpak*.go` (Go side) and `androidrt/` (C runtime, `docs/ANDROID.md`, `docs/ANDROID-MINECRAFT.md`); `apkstore*.go` (APK Store: scan, manifest and resource reader, verdicts, profiles, catalogue, Waydroid install) |
| Media | `videoplayer.go` (ffmpeg decode → IYUV texture), `streaming.go`, `musicplayer.go`, `media_hub*.go`, `podcasts.go`, `tvchannels.go`, `ipstreams.go`, `shorts.go`, `youtube*.go`, `playbackqueue.go`; `spatial.go` + `spatial_dsp.go` + `spatial_ui.go` (spatial audio for JukaHub's media, hooked into `QueueVideoAudio`), `spatial_games.go` + `spatialfx/` (the same DSP as a RetroArch filter / LADSPA plugin; `spatialfx/jh_spatial_ref.py` is the spec) |
| Web and apps | `browser*.go`, `textbrowser.go`, `webpage*.go`, `webscript.go` + `webdom_*.go` + `webdom/*.js` (page JavaScript on goja: DOM prelude, HTML parser, module rewrite, the scripts' network), `apps.go`, `winbatch*.go` (cmd.exe quoting for `.bat`/`.cmd` launches), `appgrid.go`, `windowsapps*.go`, `plugx.go`/`plug*.go` (plugins), `toolbox.go`, `xpack*.go` |
| Files and transfer | `files_*.go`, `fileeditor.go`, `ftp_server.go`, `ssh_server.go`, `sftp_root.go`, `fileserver_guard.go` (FTP/SFTP auto-stop, clients), `filetransfer.go`, `companion*.go` (Phone Companion; `companion_cast.go` serves Screen Share), `castcap*.go` + `castui.go` (Screen Share capture, framebuffer source, panel) |
| System | `wifi*.go`, `systemcontrol.go`, `audiooutput.go`, `bluetooth_devices*.go` (Bluetooth devices page), `hardware.go`, `systool_*.go`, `sdhealth.go`, `batteryhealth.go`, `ledsync.go`, `haptics.go`, `screensaver*.go`, `alarm*.go`, `sched*.go`, `ntpsync.go` + `clockjump.go` (network time, re-anchoring after a clock jump) |
| Social and LAN | `chat*.go` (Discord), `party*.go`, `peer*.go`, `nearby*.go`, `psremote*.go`, `smart_home*.go` |
| Packages, updates, backup | `patch_*.go`, `packages.go`, `pkg*.go` (`pkgstage.go`/`pkgverify.go`: verified, staged installs with a rollback), `autoupdate.go`, `emulatorautoupdate.go` + `corebuilds_history.go` (core updates, build history), `upch*.go` (offline update channel), `jdelta_update.go`, `bkup*.go`, `ssync*.go` (save sync), `cfgdoc*.go` (config as code) |
| Config and storage | `config*.go`, `xmlconfig.go`, `jukaconfig.json` + `trimui/jukaconfig.device.json` (device twin), `paths.go`, `platform.go`, `cache.go` (bbolt) |
| Logging and diagnostics | `logging.go`, `hardware.go` (`appLog` ring), `systool_logs.go`, `diagnostics_bundle.go`, `bugrep*.go`, `telemetry.go` |
| Web builder | `index.html`, `script.js`, `style.css`, `builder-*.js`, `gallery/`, tests in `tests/web` + `tests/e2e`; the device-side receiver is `player/livebuilder.go` |

---

## North star: the best handheld OS

JukaHub is a launcher on a vendor Linux image (TrimUI) and an app on Windows,
but users judge it as an OS: does it wake instantly, show the right buttons,
never lose a save, recover from a bad update, read in their language, and stay
out of the way of games? This part of the file turns that question into work.
It was written on 2026-11-09 from a code inventory of every subsystem (four
read-only sweeps) and a benchmark of what the best systems shipped in 2026:

| Reference | What it sets the bar for |
|---|---|
| NextUI 6.9 (TrimUI) | Onion-style game switcher, instant deep sleep + light sleep, auto-save on quit and auto-resume, rewind, power-off protection, Paks |
| muOS 2601 "Jacaranda" | Overlays (brightness, volume, low battery) injected into any SDL/GLES emulator, per-content colour filters, activity tracker with export, inotify instead of polling |
| Knulli Gladiator II / Scarab (Batocera) | MTP over USB, quick resume into the last game at boot, automatic BT↔speaker audio switching, auto player assignment, assignable TrimUI toggle |
| TrimUI stock 1.1.1 | OSD hot-function board (Menu+Select), SSH, hotspot, factory reset |
| SteamOS 3.8 / Xbox full-screen experience | Quick Access Menu, per-game performance profiles, performance overlay, boot straight into the launcher, background work paused while playing, "handheld optimised" compatibility labels |
| Nintendo Switch 2 (23.x) | Device-to-device system transfer, system-wide text-to-speech, speech-to-text chat |
| ES-DE 3.5 | Multi-scraper (images, videos, manuals), metadata editor, theme engine, translations |
| Xbox Accessibility Guidelines 3.2 | Narration of focused UI (XAG 106), every screen usable by keyboard *and* controller alone (XAG 112) |
| iOS 26 "Liquid Glass" | Reduce Transparency / Increase Contrast / Tinted glass — the accessibility switches a glass UI needs |

### Principles every task below serves

1. **Instant and quiet.** Wake in under a second, idle at near-zero CPU, never
   poll what can be notified, never work while a game owns the device.
2. **Never lose anything.** Saves, settings and the install itself survive power
   loss, bad updates and bad configs; every destructive action is undoable or
   confirmed.
3. **The controls you have.** Pad, keyboard, mouse and touch are all first-class;
   hints name the device in use (see the ground rule above); nothing is pad-only
   or keyboard-only without a reason.
4. **Everyone.** Narration, contrast, text size, colour filters and languages
   are platform features, not per-screen favours.
5. **Private and safe by default.** Network services are off until asked for,
   authenticated when on, and switch themselves off; nothing runs unsigned
   without the user saying so.
6. **Honest.** No fake notifications, no fake licences, no hint that names a key
   that does nothing.
7. **One device, two platforms.** The handheld is the product; Windows is a
   first-class second home, not a dev harness.

### Where JukaHub stands (2026-11-09)

| Capability | JukaHub today | Task |
|---|---|---|
| Game switcher / resume | RetroArch auto-save/load only; no switcher; no resume-at-boot | SHELL-1, LIB-10 |
| Sleep | No power-button sleep; "screen off" is a black frame with the backlight on | PWR-1, PWR-2 |
| Overlay over games | Declared impossible; muOS proves injection works | SHELL-2 |
| Recovery | Boot guard counts failed starts (safe mode after three; one rollback, only with crash evidence), crash reports offered at the next start, a recovery card with previous version / last good settings / reset (Nov 2026, unbuilt) | REL-10, REL-5 |
| Updates | Signed manifests optional per release; dev key only; no channels | REL-3, REL-4, REL-5 |
| Packages | Store installs streamed, hash-checked when the catalogue has a hash, staged and swapped with one rollback; the catalogue has no hashes or signature yet; core updates notify by default (Nov 2026, unbuilt) | SEC-16, SEC-21, LIB-17 |
| Keyboard | Hints follow the device and Accessibility remaps (Nov 2026); a few screens and sentences left | INP-1, INP-2, INP-13, INP-14 |
| Accessibility | Text size, palettes, remaps; no narration, no reduce-transparency | A11Y-1, A11Y-2 |
| Languages | English only, Latin/Cyrillic/Greek glyphs only, no IME | I18N-1 … I18N-6 |
| Network services | Live builder off on the handheld, rate-limited, risky designs held for approval; FTP/SFTP need a password, share confined folders and stop on their own (Nov 2026, unbuilt); all plaintext on the wire | SEC-19, NET-15, SEC-17 |
| Clock | Zone picker and network time on the handheld (Nov 2026, unbuilt); whether the device keeps time across a power cycle is unknown | FIX-2, NET-3, NET-12 |
| Library | Box art only, synchronous rescans, ~25 systems, no smart collections | LIB-1 … LIB-5 |
| Windows | Console window, no DPI awareness, no installer or signing | DESK-1 … DESK-5 |

### Release trains

The tasks carry their own priorities; this is the order they make sense in.

| Train | Theme | Contents |
|---|---|---|
| **0.5 "Trustworthy"** | Nothing breaks, nothing leaks | BUILD-*, HW-1, every FIX-*, REL-3, REL-10, SEC-8, SEC-16, SEC-18, SEC-21, INP-1, INP-2, INP-6, PWR-3, NET-3, NET-11, DESK-1 |
| **0.6 "Console-grade"** | Feels like a real handheld OS | SHELL-1 … SHELL-6, PWR-1, PWR-2, PWR-4, PWR-6, LIB-1 … LIB-4, LIB-10, A11Y-1, A11Y-2, I18N-1, I18N-2, DESK-2 … DESK-4, UX-12 |
| **0.7 "Platform"** | Others build on it | APP-*, SEC-6, I18N-3 … I18N-6, LIB-5 … LIB-9, MED-6 …, NET-6 … NET-9, SYS-8 … SYS-10, SYS-12, SYS-13, DESK-5 … DESK-10 |

---

## P0 — build and first hardware run

### BUILD-1 — First compile of the November pass (P0, S) [WIN]
**Why.** The 2026-11-08/09 passes changed ~600 files without a Go toolchain
(statically checked only). Status 2026-11-09: `player\build.bat` produced
`JukaHub.exe` at 09:29, 11:52, 12:12 and 16:06; every `.go` file is older than
the 16:06 build, so the Windows build compiles everything up to and including
the second afternoon pass (boot guard, crash reports and recovery card; Date &
time and network time; FTP/SFTP and the live builder; `.bat` quoting; verified,
staged packages and core-update notify; remap-aware hints). Still unknown: the
tests and vet (`make check` was not reported), the linux/arm64 build (files
under `_linux`/`!windows` tags such as `clockset_linux.go` and
`tzdata_linux.go` were never compiled), and the AndroidRT C code on the device
toolchain (smoke-tested on the host: 186 assertions).
**Where.** Whole `player/`. Most likely spots for type errors in the second
afternoon pass: `livebuilder.go`, `pkgstage.go`/`pkgverify.go`/`pkgstate.go`,
`compat.go` (the new `copyCompatFile`/`coreHistoryNoteReplaced` signatures),
`bootguard.go`/`crashreport.go`/`recoverycard.go`/`recoverysettings.go`,
`ntpsync.go`/`datetime.go`/`tzpicker.go`/`clockjump.go`,
`ftp_server.go`/`ssh_server.go`/`fileserver_guard.go`/`winbatch*.go`/`game_library.go`,
`inputhints.go`/`browser.go`. Earlier: `bootchime.go`, `logging.go`
(`logRingHandler`, printf wrappers), `hakux.go`/`hakuxpage.go`,
`androidpak_titles.go`, `homelayout.go`/`homefocus.go`/`home_continue.go`,
`game_library_ui.go`, `startmenu.go`, `adaptive.go` (`Pace`), `startup.go`
(`runDeferredStartupFor`), and files that lost dead code (unused-variable fallout
the static checker cannot see inside build-tagged files).
**Done when.** `player\build.bat nolaunch` builds, `make check` (vet + tests +
lint) passes, `go vet` printf checks pass on the new `log*` helpers.
**Verify.** Paste the clean `make check` tail in the PR.

### BUILD-2 — Cross-build and package for the TrimUI image (P0, S) [NET]
**Why.** The device binary and `AndroidRT` have never been built from this tree.
**Where.** `.github/workflows/build-sdcard.yml`, `player/package-sdcard.sh`,
`player/androidrt/build-tsp.sh`.
**Done when.** CI (or a local TG5040 toolchain, e.g. `ghcr.io/loveretro/tg5040-toolchain`)
produces the SD-card zip including `Apps/AndroidRT`; artifact attached to the run.
**Verify.** Unzip the artifact; `file JukaHub` reports aarch64; `loader` runs `--help`.

### BUILD-3 — Refresh or drop the local `vendor/` tree (P0, S) [WIN]
**Why.** `player/vendor/` (git-ignored) pins older goja/pprof versions than
`go.mod` requires; a `go build` that uses it stops with "inconsistent
vendoring", and API checks against it read the wrong versions.
**Done when.** Either `go mod vendor` is run (and `make check` passes with
`-mod=vendor`) or the folder is deleted and builds use the module cache;
`docs/BUILDING.md` says which.

### BUILD-4 — First green build of the Remote Play helpers (P0, S) [NET]
**Why.** No release ever carried `bin/chiaki-stream` and `bin/chiaki-regist`.
The 2026-11-10 pass added `.github/workflows/remote-play-helpers.yml` (called
by `docker-image.yml`, `release.yml` and `build-sdcard.yml`) and rewrote the
stream helper, but neither has met the real chiaki-ng, SDL 2.26.1 and FFmpeg
6.1 headers. The job may fail without blocking anything: the release then
ships without `bin/`, and the run says "Remote Play helpers: not built".
**Where.** `player/native/build-helpers.sh` (native mode),
`player/native/CMakeLists.txt`, `player/native/*.c`. Likely first fixes:
chiaki-ng's configure at the pinned commit asking for a dependency or an
option that is not given; the calls new to the helper
(`chiaki_session_set_login_pin`, `login_pin_request.pin_incorrect`,
`chiaki_controller_state_start_touch`, `_set_touch_pos`, `_stop_touch`,
`CHIAKI_CONTROLLER_BUTTON_TOUCHPAD`); static link order; an unexpected NEEDED
library (`libatomic`, `libz`); the pip wheels, or nanopb with protobuf 4. The
script rewrites chiaki-ng's `CURL::libcurl_shared` to `libcurl_static` in
`lib/CMakeLists.txt` (distribution packages patch the same line) and stops when
the cache still says `BUILD_SHARED_LIBS=ON`; if chiaki-ng moved that line, the
log says so.
**Done when.** The workflow's artifact holds both binaries, `BUILDINFO.txt`
shows only SDL2 and the C library as NEEDED and no symbol newer than GLIBC
2.31, the smoke runs pass, `JukaHub-Trimui.zip` contains `bin/`, and the
release also carries `JukaHub-RemotePlay-linux-arm64.zip` (what a JukaHub
without `bin/` downloads by itself); PSR-7 and PSR-13 then take them to a
handheld.
**Verify.** The run's step summary and an unzip of the release archive.
**Status 2026-11-10 (evening).** The first run got through every pinned
dependency and stopped at chiaki-ng's configure: `Could NOT find FFMPEG
(missing: avformat)` — the pinned commit's `find_package(FFMPEG ...)` names
avformat, and `CHIAKI_ENABLE_FFMPEG_DECODER=ON` makes that fatal.
`build_ffmpeg` now also builds libavformat, with no demuxer, muxer or protocol
in it (a prefix without `libavformat.pc` is rebuilt). Next run: whatever comes
after the configure.

### BUILD-5 — First run of the split TrimUI release build (P0, S) [NET]
**Why.** The 2026-11-10 evening pass moved the handheld compile out of the
toolchain container job into `build-trimui`: the same pinned image, started by
`.github/scripts/trimui-build.sh` through `docker run` without a network, the
runner's Go and both Go caches mounted at their own paths, and the build cache
carried by `go-build-cache.sh`. The six C libraries the old job built every
time are gone. None of it has run on GitHub, and the image's own environment
(its `SYSROOT`, `PATH`, the user it runs as) was worked out from the old job
and `build-sdcard.yml`, not inspected.
**Where.** `.github/workflows/docker-image.yml` (`build-trimui`,
`build-and-publish-ubuntu`), `.github/scripts/trimui-build.sh`.
**Done when.** `build-trimui` is green and its check step passes (AArch64,
`/lib/ld-linux-aarch64.so.1`, SDL2 from the firmware, no glibc symbol newer
than 2.33); the next push's step summary says the build cache was restored and
`go build` took well under the first run's time; and the `JukaHub` of that
release starts on a Smart Pro and a Brick.
**Verify.** The two runs' step summaries (`go build: N s`, `Go build cache: ...`)
and a device start.

### HW-1 — First device run of the November pass (P0, M) [HW]
**Why.** Nothing from this pass has run on a Smart Pro or a Brick.
**Where.** Device; `startup-bench.sh`, the Logs screen, `errors.txt`.
**Done when.** On a Smart Pro and a Brick: the startup chime plays with the
splash and the shutdown chime with the teardown (level comfortable at default
volume, no clicks); Home shows the Games card and the first-run Continue card;
the log has a `render pipeline … renderer=opengles2 acceleration=gpu` line; a
video plays at 30 fps without dropping after 10 s idle; `errors.txt` rotates past
1 MiB; quitting via Start menu → Quit and via Select+Start works.
**Verify.** Record results and firmware version in `docs/PERFORMANCE.md`
(create it; see [PERF-1](#perf-1--measure-the-november-perf-pass-on-hardware-p1-m-hw)).

---

### HW-2 — First device run of the 2026-11-10 tester batch (P0, M) [HW]
**Why.** A Smart Pro tester on CrossMix reported a glitchy screen after leaving
JukaHub, RetroArch "being weird", PPSSPP that would not update and pairing that
needed a Base64 Account-ID; the fixes and the new features (core and PPSSPP
auto-update, mouse mode, PSN sign-in, WPE Browser hand-off, background box art)
were written without a device, from search snippets for every third-party fact.
**Where.** Device (stock 1.1.x and CrossMix), `errors.txt`, `psremote.log`,
`cat /proc/mounts`, `cat /proc/<pid>/environ`, `cat /proc/bus/input/devices`.
**Done when.** On a Smart Pro with CrossMix and one with stock firmware:
(1) leaving JukaHub (Start → Quit, Select+Start, the power card) shows ONE MainUI
— `pidof MainUI` prints one pid and the log has no "no launcher came back"
line; a start over SSH still gets MainUI back after 10 s;
(2) a PS Remote Play stream ends back in JukaHub (`screen handed to helper pid`
in `errors.txt`), and the power key during a stream hands the screen back;
(3) a GBA game started from JukaHub and from MainUI has the same
`LD_LIBRARY_PATH`/`PATH`/`HOME` in `/proc/<ra pid>/environ`, and after the
JukaHub game exits `grep savestate_auto RetroArch/retroarch.cfg` shows the
pre-launch values and `data/retroarch-launch-scope.json` is gone;
(4) a core update pass runs within ~20 min of a fresh start on the charger: which
cores install, which are refused and why (`needs GLIBC_…`) — if every nightly is
refused, the buildbot aarch64 channel cannot serve TrimUI and needs a TrimUI-built
source; CrossMix's own cores come back with Emulators **R1**;
(5) PSP tile → **X** → Install PPSSPP: the real `PSP.pak.zip` layout matches the
discovery (binary `PPSSPPSDL_tg5040`, assets, bundled `libstdc++`), a game starts,
firmware saves are visible both ways, no bind is left in `/proc/mounts` after the
game or after a crash fallback, and `--pause-menu-exit` returns to JukaHub;
(6) hold Select + L1: pointer on, D-pad moves it (Brick too), Select's release
does not go Back; with **Mouse in games and apps** on, ScummVM/DOSBox/a port
shows "JukaHub Mouse" in `/proc/bus/input/devices` and the pad is given back by
the chord, Menu and game exit (does an SDL app see a mouse that appears
mid-run? if not, export `SDL_EVDEV_DEVICES` to the child);
(7) PSN sign-in from iOS Safari and Android Chrome fills the Account-ID and a
real PS5 and a PS4 (fw ≥ 7) pair;
(8) **Open in WPE Browser** opens the page (does `launch.sh` take the URL as
`$1`?), **Get WPE Browser** installs the real release (does the zip hold
symlinks? `extractZipSafe` refuses them), `free -m` with JukaHub parked;
(9) a fresh card with ~100 ROMs on the charger gets covers ~2 min after Home and
one "Found box art for N games" toast.
**Verify.** Record results, firmware and CrossMix versions in the HW log; file a
FIX task for anything that differs.

---

## P0/P1 — defects found by the 2026-11-09 inventory (FIX)

Found by reading, not by running: confirm each on a build before fixing, and
delete the task with the fix. Security defects are under SEC.

### FIX-2 — Does the handheld keep time across a cold boot? (P2, S) [HW]
**Why.** The boot guard no longer depends on it (it counts starts,
`bootguard.go`), and network time now sets the clock once Wi-Fi is up
(`ntpsync.go`), but release checks, bug-report clock checks, crash-file names and
anything stamped before the first sync still read the wall clock.
**Done when.** On a Smart Pro and a Brick, `ls /sys/class/rtc` and `hwclock -r`
after a cold boot with no Wi-Fi are recorded in `docs/DEVICE_TUNING.md`; if there
is no RTC, the `minSaneClock` users are reviewed and NET-12 is closed as moot.

### FIX-13 — Critical battery saves RetroArch only (P1, M) [HW]
**Why.** The 3% guard (`lowbattery.go` `protectLowBattery`, ~:412-478) acts only
while RetroArch runs and saves through its UDP command; PPSSPP, DraStic, hakuX,
AndroidRT and ports get no save, no sync and no suspend.
**Done when.** See PWR-3.

### FIX-14 — Release pipelines disagree (P1, M) [NET]
**Why.** `release.sh` cross-builds with cgo and no `CC` (release.yml cannot
succeed), its Windows zip omits the SDL DLLs and its asset names differ from what
`patch_swap.go:146,149` fetches; `docker-image.yml` creates a tag and draft
release for every same-repo PR and publishes a release on every push to main —
the builds the updater installs.
**Done when.** One release workflow (see REL-8); PRs build artifacts but never
tag.

---

## Performance (PERF)

### PERF-1 — Measure the November perf pass on hardware (P1, M) [HW]
**Why.** Render batching, frame pacing, lazy regexps and deferred boot work were
estimated, not measured.
**Where.** On the device: launch JukaHub with `JUKAHUB_STARTUP_BENCH=1` (it
exits after the first frame) and read the `startup:` line from `errors.txt`;
`GODEBUG=inittrace=1` adds package-init costs. `startup-bench.sh` is the host
harness (bash + Go); a `JUKAHUB_BENCH_BIN` option to skip its build would let it
drive a prebuilt binary. `framestats.go` logs one histogram line a minute.
**Done when.** `docs/PERFORMANCE.md` records, for Smart Pro and Brick: cold-boot
median of 5 runs + top package-init costs, warm boot, frame-time histogram while
navigating Home and while a video plays, idle CPU of JukaHub on Home over 60 s
(`top -d 5`), RSS after 10 minutes of use — and the same numbers for commit
`36794cd` as the baseline.
**Verify.** Numbers + method in the doc; regressions filed as new tasks.

### PERF-3 — Dirty-frame presentation (P1, M)
**Why.** Idle screens still redraw 15–24 times a second; on a handheld that is
battery.
**Where.** `main.go` loop, `adaptive.go`, toast/notification, download and clock
code paths.
**Done when.** A frame is drawn and presented only when something changed
(input, scene/overlay change, toast, progress tick, clock minute, video frame,
animation, network status) plus a 1 Hz safety redraw; no visible staleness.
**Verify.** [HW] idle Home CPU drops ≥ 50% vs PERF-1's baseline.

### PERF-4 — Brick scaling quality (P1, S) [HW]
**Why.** The 960×720 canvas is scaled to the 1024×768 panel with nearest
sampling (uneven 1.067× pixel doubling on text).
**Where.** `uiprofile.go`, renderer setup in `main.go`.
**Done when.** Text renders evenly on the Brick: either linear scale quality for
UI textures (nearest kept for pixel art: minigames, JukaLand) or native
1024×768 rendering; decision recorded in `docs/DEVICE_TUNING.md`.
**Verify.** Before/after photos of Home and Settings on a Brick.

### PERF-5 — Verify shared UI font loading on the handheld (P2, S) [HW]
**Why.** UI fonts now open from a shared, pinned in-memory source, but the
TrimUI still needs a read-count and rendering check across the sizes it uses.
**Where.** `home_fonts.go`, `core.go` (`getCachedFont`, `getFontSized`),
`canvas.go`.
**Done when.** `strace -e openat` shows one read of `Inter-Regular.ttf` for all
font sizes, and the UI and canvas preview render normally at the 14 px floor,
the regular role sizes and the larger headings.
**Verify.** Record the open count and check the fonts on a Smart Pro.

### PERF-6 — Config watcher only while the live builder is connected (P2, S)
**Why.** `hotreload.go` stats the config every 2 s for the whole session.
**Where.** `hotreload.go`, `livebuilder.go`, `main.go` (`StartConfigWatcher`).
**Done when.** On the handheld, polling runs only while a Live Builder session is
paired (or uses inotify on Linux); desktop dev keeps today's behaviour.
**Verify.** `strace` shows no periodic `stat` with the builder idle.

### PERF-7 — Trim heavy dependencies (P2, L) [WIN]
**Why.** Compile time, binary size (~36 MB) and page-in time on the SD card.
**Where.** goja: since the late-night pass of 2026-11-09 it runs every page's
scripts against the DOM prelude — `webdom_runtime.go` (+ the embedded
`webdom/*.js`), `webdom_parse.go`, `webdom_module.go`, `webdom_net.go` and the
legacy pass in `webscript.go`; `processes.go`/`systool_disk.go`/`systool_hw.go`/
`terminal.go`/`textbrowser.go` (gopsutil), `textbrowser.go` (zeroconf), `chat.go`
(discordgo).
**Done when.** goja and the whole DOM pass sit behind a `//go:build !nojs`
split: the `webdom_*.go` files and `webscript.go` in the full build, and in the
lite build a stub `webJSRunPageFor` that returns the page as it arrived, with
Settings → Browser → JavaScript hidden; gopsutil replaced by `/proc` + `statfs`
on Linux and two Win32 calls; zeroconf replaced by a one-shot mDNS query;
`go mod tidy && go mod vendor` run; features unchanged.
**Verify.** Binary size and cold-cache build time before/after in
`docs/PERFORMANCE.md`; `make check` green.

### PERF-8 — Deferred-startup audit (P2, S) [HW]
**Why.** `runDeferredStartupFor` spreads post-first-frame work at ~8 ms per
frame; some tasks may not need the main thread at all.
**Where.** `startup.go`, `main.go` (`deferStartupTask` call sites).
**Done when.** Tasks > 20 ms on the device are listed (they log at Info) and every
goroutine-safe one (no SDL, no main-loop state) runs off the main thread.
**Verify.** The first interactive second on the device has no frame > 50 ms.

### PERF-9 — SDL init cost per subsystem (P3, S) [HW]
**Where.** `main.go` (`sdl.Init`).
**Done when.** Time per subsystem (video/audio/joystick/gamecontroller) is known;
slow ones initialise on demand (`sdl.InitSubSystem`).
**Verify.** Numbers in `docs/PERFORMANCE.md`.

### PERF-10 — VSync evaluation (P3, S) [HW]
**Why.** The renderer has no `PRESENTVSYNC`; pacing is in software.
**Done when.** Tearing and pacing compared with `SDL_HINT_RENDER_VSYNC=1` on both
devices; the smoother option is the default.

### PERF-11 — Release scene-owned textures when a scene closes (P2, M)
**Why.** Caches are bounded but stay full after leaving a heavy scene (Browser,
Chat, Media hub, Library art) on a 1 GB device.
**Where.** `scene.go` (module registry), the per-scene caches listed in
`docs/DEVELOPMENT.md` (cache inventory).
**Done when.** Leaving a scene trims its scene-specific caches (not the shared
text/shape caches); re-entry warms them again without a visible pop.
**Verify.** `JUKAHUB_TEXSTATS=1` shows the drop after leaving Browser/Chat.

### PERF-12 — Per-frame allocation sweep (P2, M) [WIN]
**Why.** Per-frame text layout and some list snapshots still allocate (e.g.
`downloadRowsFor` snapshots and groups the whole queue every frame).
**Where.** `downloadsui.go`, list renderers, `textcache.go`.
**Done when.** `go test -bench` style allocation probes (temporary, not committed
as tests) show 0 allocs/frame on Home, Library and Downloads at rest.
**Verify.** pprof alloc profile of a 60 s idle session on Windows attached.

### PERF-13 — Binary size budget (P3, S) [WIN]
**Done when.** Size per package measured (`go tool nm -size -sort size`); the top
10 contributors listed in `docs/PERFORMANCE.md` with a plan; `-trimpath` added to
release builds.

### PERF-14 — Lazy regexps in the sub-packages (P3, S)
**Why.** The November pass removed the last eager package-level regexp from
`package main`; `internal/`, `web/` and `tools/` still compile theirs during init.
**Where.** `player/internal/`, `web/`, `player/tools/`.
**Done when.** No package-level `regexp.MustCompile` outside a `sync.OnceValue`
remains in those trees and their tests pass.

### PERF-15 — What page scripts cost on the handheld (P1, S) [HW]
**Why.** The Browser's JavaScript pass has only run under node. On the device the
550 KB DOM prelude is compiled once per run (estimated at about a second on the
A133P, started beside the first page's download by `webDOMPreludeWarm`), runs
again for every page (thousands of accessors), and goja interprets every page
script on four Cortex-A53 cores inside a 10-second budget.
**Where.** `webdom_runtime.go` (`webDOMPrelude`, `webDOMPreludeWarm`, `runPage`,
the `scripts on …` summary line), `webdom/*.js` (work done while the prelude
loads), `docs/PERFORMANCE.md`.
**Done when.** `docs/PERFORMANCE.md` records, on a Smart Pro and a Brick, the
prelude compile, the per-page start (prelude run and boot) on a near-empty page,
and the whole pass on a jQuery site, a framework single-page app and a news
article with lazy images (add a debug line for compile and start if the
summary is not enough); the per-page start is under 300 ms, or the interfaces
pages rarely touch are installed on first use until it is.
**Verify.** The `scripts on …` lines in `logs.txt` with
`JUKAHUB_LOG_LEVEL=debug`.

### PERF-17 — Animated wallpapers on the handheld: memory and pacing (P2, S) [HW]
**Why.** GIF wallpapers (2026-11-10) are decoded frame by frame on a goroutine
and uploaded a few megabytes per UI frame within a 40 MB budget; the budget,
the 3 MB-per-frame upload step and the decode time were estimated for the A53
and the PowerVR driver, not measured, and no device has shown one yet.
**Where.** `gifwall.go` (log tag `wallpaper`: frames kept, read limits, memory
short), `wallpaper.go` (picker), `adaptive.go` (`EvictCachesForPressure` keeps
the first frame under critical pressure), Power tools → memory.
**Done when.** On a Smart Pro and a Brick: a 1280×720 photo GIF and a 1080p
100-frame GIF show their first frame within about half a second of being picked
and animate within a few seconds, with no frame over 50 ms in `framestats`
while frames upload; textures stay near the 40 MB budget and the big GIF logs
how many frames it kept; a 160×144 pixel-art GIF is crisp; Battery Saver and
**Animate wallpaper** off hold the first frame; a restart keeps the pick,
*None* survives a restart, and an experience pack applied after a pick (and its
Undo) behaves as the newest choice; critical memory pressure leaves the first
frame and logs it.
**Verify.** `framestats` histogram while a GIF loads, Power tools → memory,
`logs.txt`.

### PERF-18 — Give the animated wallpaper's memory back before big tenants (P2, S) [HW]
**Why.** An animated wallpaper keeps up to 40 MB of textures for as long as it
is the wallpaper. Only a wallpaper change or critical memory pressure frees
them — not the WPE Browser hand-off, not a game, not shutdown — and on a 1 GB
device the WPE Browser (~260 MB) and the heavy emulators want that memory most.
**Where.** `gifwall.go` (`gifWallEvict` releases the textures and forgets the
path, so the next backdrop draw decodes again), `brw_wpe.go`
(`brwWpeFreeMemory`), `shutdown.go` (`shutdownReleaseTextures`), `main.go` (the
branch that parks the loop while a game owns the screen). Every call stays on
the render thread.
**Done when.** The WPE hand-off and the shutdown card call `gifWallEvict`, and a
game launch frees all but the first frame (the decoder already sleeps during a
game) and decodes again after it; Power tools → memory shows the drop on a
Smart Pro with a 1080p GIF wallpaper.
**Progress (2026-11-10).** The WPE hand-off (`brwWpeFreeMemory`) and the
shutdown card (`shutdownReleaseTextures`) call `gifWallEvict`. On game entry,
the decoder is cancelled and all but the first frame texture are released; on
return, the retained frame is released and the current wallpaper is decoded
again. The Smart Pro memory measurement is still open.

---

## Graphics and GPU (GPU)

### GPU-1 — Confirm the GLES2 path and batching on the stock image (P0, S) [HW]
**Why.** `opengles2` + `SDL_RENDER_BATCHING=1` are set on the device; whether
the stock SDL2 (2.30.8, `mali` fbdev-EGL backend on PowerVR) honours both is
unproven.
**Where.** `main.go` (hints), `adaptive.go` (`renderPipelineInfo`).
**Done when.** The `render pipeline` log line on Smart Pro and Brick reads
`renderer=opengles2 acceleration=gpu`; if not, the cause (video backend, missing
blob) is documented in `docs/DEVICE_TUNING.md` with a workaround.

### GPU-2 — Fewer render-target switches (P2, M)
**Why.** PowerVR is a tile-based GPU: every `SetRenderTarget` flushes the tile
memory. Shape/gradient/icon caches bake through render targets on demand, so a
cold screen can switch targets dozens of times in one frame.
**Where.** `shapeCache.go`, `dynamicbg.go` (`bgGrads`), `iconatlas.go`,
`warmSceneCaches` in `bootwork.go`.
**Done when.** Bakes are batched (one target switch per cache per frame, or
software surfaces uploaded once), and a scene change on the device has no frame
> 50 ms caused by baking.
**Verify.** framestats histogram during scene changes before/after.

### GPU-3 — Geometry instead of textures for flat shapes (P3, M)
**Why.** `SDL_RenderGeometry` (SDL ≥ 2.0.18) draws gradients and rounded fills
as vertex-coloured triangles: no texture memory, one batched draw.
**Where.** `shapeCache.go`, `ui_glass.go`, `backgroundfx.go`. `liquidbg.go`
(the liquid background) is the first `RenderGeometry` user: reuse its
allocation-free vertex/index pattern and its fall-back to the plain path when
the renderer refuses geometry, and read GPU-5's numbers before converting more.
**Done when.** Backdrop glows and glass fills draw through geometry where that
is cheaper (measured), with the texture path kept as fallback for the software
renderer.

### GPU-4 — Hardware video decode through the firmware's CedarX libraries (P2, L) [HW]
**Why.** ffmpeg software decode is the largest CPU cost while playing video.
**Where.** `docs/VIDEO_VPU_OFFLOAD.md` (design + five probe questions),
`videoplayer.go` (`ffmpegDecoderArgs` pipe contract: yuv420p).
**Done when.** The probes are answered on a TG5040 image; if `/dev/cedar_dev` +
the vendor libs work, a small aarch64 helper decodes H.264 to the same yuv420p
pipe with automatic ffmpeg fallback. Never generalise to the A523 Smart Pro S.
**Verify.** CPU during 720p H.264 playback before/after.

### GPU-5 — Liquid background on the stock GE8300 (P1, S) [HW]
**Why.** The liquid background (2026-11-10) is the first code that draws through
`SDL_RenderGeometry`, and it was written and reviewed without a device. Whether
the stock SDL2 (2.26.1 / 2.30.8, `opengles2` on PowerVR) takes a 2,400-vertex
indexed mesh and an additive glint mesh every frame at 30 fps, and how strong
the refraction and the glints look on the 5-inch panel, were chosen by
reasoning.
**Where.** `liquidbg.go` (log tag `liquidbg`: "the renderer cannot draw the
water" means the geometry was refused; "the water costs … ms a frame" is the
perf guard stepping down), `liquidbg_motion.go` ("tilt from …"), Settings →
Display & theme → Liquid background.
**Done when.** On a Smart Pro and a Brick with the device defaults (Low Power
and Reduce Motion on), Gentle and then Lively: a focus move ripples under the
tile on Home, in Settings and on a module screen; the header band shows no
seam when a list scrolls under it; with a wallpaper (a still and a GIF) the
picture bends and the dim and blooms still sit over it; a light theme keeps
the ripples readable; `framestats` shows the moving water at no more than about
3 ms a frame over the calm baseline and no step-down line; two idle minutes
later the rain has stopped and frame cost is back to the no-water baseline. If
a TrimUI unit with an accelerometer turns up (Smart Pro S?), tilting its right
edge down piles the water on the right; if the axes come out rotated, apply the
IIO mount matrix.
**Verify.** `framestats` before/after and the `liquidbg` lines in `logs.txt`
with `JUKAHUB_LOG_LEVEL=debug`; retune the refraction and glint strengths in
`liquidbg.go` from photos of the panel if they read too strong or too weak.

---

## Architecture and build (ARCH)

### ARCH-1 — Package split, step 1: pure leaves (P1, M) [WIN]
**Why.** Every edit recompiles ~300k lines of `package main`; that is the
compile-time problem. Only leaves can move without a rewrite.
**Where.** `vecraster.go`, `icondefs.go`, `retry.go`, `browserpsl.go`,
`ramsearch*.go`, `ppsspp_calibration.go`, `port_compat.go`, `lyrics.go`,
`textutil.go`, `scenetasks.go` (they reference no other file of the package).
**Done when.** Each lives in `player/internal/<name>` with exported API; `main`
keeps thin aliases (`var retryDo = retry.Do`) so call sites stay unchanged;
`make check` green after each move.
**Verify.** `go build -x` shows the moved packages cached on an edit to `core.go`.

### ARCH-2 — Package split, step 2: logging (P2, S) [WIN]
**Where.** `logging.go`, the `appLog` ring in `hardware.go`.
**Done when.** `internal/logx` owns the handler and ring; `main` keeps
`logDebug = logx.Debug` aliases; 127 dependents untouched.

### ARCH-3 — Package split, step 3: models and parsers (P2, L) [WIN]
**Where.** PDF (`pdfreader.go`, `pdfv_doc.go`), EPUB/HTML reader (`reader.go`),
web page model (`webpage*.go`: split layout from drawing), feeds/podcasts,
M3U playlists, patch engine (`patch_*.go`: journal, staging, signatures),
emulator catalogs (`emusystems.go`, compat tables), media library scanner
(`mlib.go`), JSON stores (atomic write + `.bak` from `config_health.go`).
**Done when.** Each moves with SDL-free code only; one PR per package.

### ARCH-4 — Package split, step 4: feature islands (P3, XL) [WIN]
**Where.** chat (Discord), Wi-Fi, party/peer, JukaLand, minigames, smart home,
RetroAchievements.
**Done when.** Each island talks to `main` through a small interface; split into
one task per island before starting.

### ARCH-5 — Shrink the main loop (P2, L) [WIN]
**Why.** `main.go` (~3.5k lines) still owns the loop, input routing and
teardown; `core.go` (~7.8k) is referenced by 330 files.
**Done when.** The loop body is a short sequence of named phases (input, update,
draw, present, pace) in their own files; `core.go` is split by responsibility
(render helpers, triggers, video entry points) without behaviour change.

### ARCH-6 — Context cancellation for the remaining feature fetches (P2, M)
**Why.** Leaving a screen should stop its network work, not just hide it.
**Where.** Fetches that still use bare clients (search `http.Get(`,
`httpClientLong.Get(`); `scenetasks.go` is the pattern.
**Done when.** Every scene-owned fetch runs under the scene context.
**Verify.** Leaving Podcasts/Weather/Library art mid-fetch closes the request
(server-side observation in a temporary test).

### ARCH-7 — Retire 0.4.0 migrations on a schedule (P3, S) [OWNER]
**Why.** Upgrade shims (`home_migrate.go`, `migrateLegacyUserFiles` in
`extras.go`, `pkgMigrateLegacy`, `setLegacySection`, the Tools hub's legacy
`Misc` scene, `legacyDecryptionKeys` in `crypto.go`) protect users upgrading from
0.4.0 and must not go before they have had a release to migrate.
**Done when.** The owner picks the release after which they go; each shim carries
a `// remove after vX.Y` comment; the removal is a task with that version.

### ARCH-8 — Production code only tests use (P3, S)
**Why.** 29 production functions are referenced only by tests (the dead-code
scan lists them), e.g. `EncryptWithAuth`/`DecryptWithAuth`, `SaveConfig`,
`ParsePatchManifest`, `NewerVersion`, `pdfExtractTextFromBytes`, `runBounded`.
**Done when.** Each is either a deliberate test seam (named `…ForTest` or
documented) or removed together with its test.

---

## Tests and CI (TEST)

### TEST-1 — A scene-dump tool, not a test (P1, M) [WIN]
**Why.** The visual audit was deleted with the render tests; design work needs
every scene rendered to PNG.
**Where.** New `player/tools/scenedump/` (a `go run` program reusing the player
package through an exported entry point or a build tag).
**Done when.** `go run ./tools/scenedump -out tmp/scenes` writes every config
scene and the main Go pages at 1280×720 and 960×720 (SDL dummy driver, software
renderer) in under a minute.
**Verify.** The PNGs exist and look right; `docs/DEVELOPMENT.md` documents it.

### TEST-2 — Tests for the riskiest paths the cut left uncovered (P1, M)
**Why.** The test cut removed coverage of a few things that hurt silently.
**Done when.** One fast table test each for: the self-update restart handshake
(`patch_swap.go` markers ↔ `launch.sh` protocol), browser cookie "forget" taking
the `.bak` with it (`browsercookies*.go`), app launch path refusals
(`isLaunchableAppPath`, `apps.go`), and the Wi-Fi supplicant socket transport
(`wifi_ctrl.go`, stubbed socket).
**Verify.** `go test -run` for the four; total suite time stays under ~10 s.

### TEST-3 — D-pad reachability check as a tool (P2, S)
**Why.** The dpad audit test was cut; an unreachable control on the Brick ships
unnoticed.
**Where.** `focus.go` (`BuildGraph`), both shipped configs.
**Done when.** `go run ./tools/scenedump -reach` (or a sibling tool) walks every
scene's focus graph at both canvases and reports unreachable focusables; run it
in CI as a non-blocking job.

### TEST-4 — CI builds the device binary and runs AndroidRT's smoke suite (P1, S) [NET]
**Where.** `.github/workflows/ci.yml`, `build-sdcard.yml`,
`player/androidrt/tests/smoke/run-smoke.sh` (103 asserts, ~7 s, host gcc).
**Done when.** Every PR cross-builds linux/arm64 and runs the smoke suite.

### TEST-5 — CI keeps `go vet` for both GOOS (P2, S) [NET]
**Done when.** CI runs `GOOS=windows go vet ./...` (cgo off where possible) as
well as Linux, so Windows-only files cannot rot.

### TEST-6 — Ship the static checker in the repo (P2, S)
**Why.** Agents without a toolchain rely on `gocheck.py`, which lives in the
git-ignored `tmp/`.
**Done when.** `tools/gocheck/` holds `gocheck.py`, `alignchk.py`,
`importchk.py` and a README; `CONTRIBUTING.md` mentions it.

### TEST-7 — Relative-link check for the docs in CI (P3, S)
**Why.** The Go test that checked doc links went with the test cut; links to
deleted files then pile up (the November cut left 98).
**Done when.** A small script (Python or Node, no Go) fails CI on a broken
relative link in any `*.md` outside `node_modules`/`vendor`.

### TEST-8 — Fuzz the parsers (P2, M)
**Done when.** `go test -fuzz` targets exist for M3U, CUE/GDI/M3U sheets, the
PDF and EPUB readers, zip/tar extraction (`extractTarMembers`), package and
update manifests, `/proc/bus/input/devices` and the XML config; CI runs each for
30 s and keeps the corpus.

### TEST-9 — gofmt and a race/checkptr job in CI (P2, S) [NET]
**Done when.** CI fails on unformatted Go, and a `-race` job (which enables
checkptr) runs the synthetic event, input, companion and download tests.

### TEST-10 — A device release checklist (P1, S) [HW]
**Done when.** `docs/RELEASE_CHECKLIST.md` lists the 20-minute walk before a
release on a Smart Pro and a Brick (boot, sleep/wake, a game per launcher
family, video, Wi-Fi join, update + rollback, keyboard plugged in, controller
on Windows) and the release workflow links it.

### TEST-11 — The backlog checks itself (P2, S) [WIN]
**Why.** Two review passes drifted the file's own bookkeeping: the header said
234 tasks while 238 existed, and the recovery row cited a task that had already
been deleted. A reader cannot see a dangling ID or a stale count.
**Where.** `player/MODERNIZATION.md`; a dependency-free script under `tools/`
(Python or Node, like TEST-7).
**Done when.** The script fails on a duplicate task ID, an ID named in prose
that matches no heading, an anchor that matches no heading, and a header count
that differs from the number of task headings; CI runs it beside the docs link
check (TEST-7).

### TEST-12 — First run of the browser-engine tests (P1, S) [WIN]
**Why.** The Browser's DOM pass (`webdom_runtime.go`, `webdom_parse.go`,
`webdom_net.go`, `webdom_module.go`) and the reader's new selector matcher
(`webpage.go`, `webpage_cssmatch_test.go`) were written without a Go toolchain:
their tests have never run, including `TestWebDOMPreludeCompilesInGoja`, which
compiles the embedded prelude in goja — the first time it meets the real engine.
The prelude itself is tested under node (`tests/web/webdom_*.test.mjs`, run by
the web suite's `npm test` against goja's set of built-ins).
**Where.** The files above and their `_test.go` files, `player/go.mod`
(`github.com/dlclark/regexp2/v2` is now a direct import).
**Done when.** `go test ./...` passes on Windows and in CI with the new files,
`go mod tidy && go mod vendor` has been run, and the web job's node run includes
the two `webdom` suites.
**Verify.** The `go test` tail and the CI run in the PR.

### TEST-13 — Run the Remote Play helper's host tests in CI (P2, S) [NET]
**Why.** `player/native/tests/run-host-tests.sh` checks the helper's button
map, session and status files, battery reader, motor and its whole `main()`
against fakes with nothing but a C compiler, and nothing runs it.
**Where.** `.github/workflows/ci.yml`.
**Done when.** A job runs it (with ASan and UBSan) on every push and pull
request that touches `player/native/`, and a failure fails the check.

### TEST-14 — One Remote Play helper build per push (P3, S) [NET]
**Why.** `remote-play-helpers.yml` reuses the helpers an earlier push-to-main run
built from the same sources (the artifact `remote-play-helpers-<hash>`, taken by
`go-build-cache.sh fetch`), but each calling workflow keeps its own: a push that
changes `player/native/**` still builds them twice, for `docker-image.yml` and
for `build-sdcard.yml`, about a quarter of an hour of arm64 runner each.
**Where.** `.github/workflows/remote-play-helpers.yml`, `build-sdcard.yml`,
`.github/scripts/go-build-cache.sh` (`trusted_run` accepts the same workflow
only).
**Done when.** One push builds the helpers once for both workflows, and the runs
an artifact may come from are still only push-to-main runs of the release
workflows, never one a pull request or another workflow can start.

### TEST-15 — A reproducible build root for the helpers (P3, S) [NET]
**Why.** The Ubuntu 20.04 root is debootstrapped from the day's focal-updates
and the pip packages are pinned by version only, so two runs of one commit can
link different OpenSSL or compiler builds.
**Where.** `.github/workflows/remote-play-helpers.yml`,
`player/native/build-helpers.sh`.
**Done when.** The root comes from a dated `snapshot.ubuntu.com` archive, pip
installs with `--require-hashes`, and `BUILDINFO.txt` names the snapshot.

### TEST-16 — A supported OpenSSL in the Remote Play helpers (P2, S) [NET]
**Why.** The helpers link OpenSSL statically from Ubuntu 20.04: 1.1.1f with
Ubuntu's security fixes up to the end of focal's standard support (May 2025),
frozen since then. Today the stream uses it only for chiaki's own crypto, not
for TLS (curl is linked but only PSN code calls it), but PSR-8 would put TLS on
it, and a newer chiaki-ng may expect OpenSSL 3 APIs.
**Where.** `player/native/build-helpers.sh` (native mode), `remote-play-helpers.yml`
(add `perl` for OpenSSL's configure).
**Done when.** OpenSSL 3.0 or 3.5 LTS is built `no-shared` from a
checksum-verified tarball into `DEPS_PREFIX` and found through
`-DOPENSSL_ROOT_DIR`; `BUILDINFO.txt` names its version; NEEDED is unchanged.

### TEST-17 — Every helper dependency from a verified source, offline configure (P2, S) [NET]
**Why.** miniupnpc comes in through chiaki-ng's external-project flags taken
over from the original port's build, and chiaki-ng newer than v1.10 may also
want libevent for its PSN code; if its configure fetches either itself, that
download is not checksum-verified (`BUILDINFO.txt` lists anything it fetched).
**Where.** `player/native/build-helpers.sh`.
**Done when.** miniupnpc (and libevent if the pinned chiaki-ng needs it) are
built statically into `DEPS_PREFIX` from verified tarballs or commits,
configure runs with `-DFETCHCONTENT_FULLY_DISCONNECTED=ON`, and BUILDINFO lists
nothing fetched by configure.

---

## UX and design (UX)

The review behind these is [docs/UX_REVIEW.md](../docs/UX_REVIEW.md) (30
findings, 21 implemented in November).

### UX-1 — Role-based type scale (P1, M)
**Why.** Screens still pass literal pixel sizes (the media hub uses seven).
**Where.** `media_hub_render.go`, `media_hub_sheet.go`, `pkgui.go`, `tbxui.go`,
`wifiscreen.go`, `toolsgrid.go`, `favorites.go`, `jland_render.go`,
`homesearch_render.go`, `vksettings.go`; roles in `design.go`.
**Done when.** Five roles only — Caption 14, Label 16, Body 18, Heading 22,
Title 28 — through `FontForRole`; no literal sizes outside `design.go`; the Brick
canvas stays ≥ 12 px.
**Verify.** [TEST-1](#test-1--a-scene-dump-tool-not-a-test-p1-m-win) dumps of six
scenes on both canvases reviewed.

### UX-2 — Each screen shows its name once (P1, S)
**Why.** The bar shows "JukaHub - <Screen>" while most pages repeat the title in
a `UIPageHeader` under it.
**Where.** `header.go`, `extras.go` (`renderStatusBar`), `UIPageHeader` users.
**Done when.** One pattern everywhere (the page header owns the title, the bar
shows brand + status), at Heading size or larger.

### UX-3 — Copy sweep (P2, S)
**Done when.** No user-visible `(s)` plurals (use `pluralizeCount`), no `N/A`
(say "Unknown" or hide the row), no raw placeholders like `<system>`.
**Verify.** `grep` for `(s)"`, `"N/A"`, `<system>` in user-visible strings is
empty.

### UX-4 — One icon stroke weight (P2, M)
**Where.** `icondefs.go`, `icondraw.go`, `iconatlas.go`.
**Done when.** Every 32 px glyph uses one stroke weight and outline style (the
Packages glyph is a filled 3D box among outlines); atlas re-baked.

### UX-5 — Status cluster legibility (P2, S) [HW]
**Done when.** A connected Wi-Fi meter draws at full contrast (dim only for "no
signal"); battery/clock/bell readable on both canvases at arm's length.

### UX-6 — Home composition prototype (P2, M) [OWNER]
**Why.** Ten equal-weight destinations dilute hierarchy on a gaming handheld.
**Done when.** An HTML mockup in `mockups/` of a content row (Continue + Games +
Media, large) over a compact system dock (Files, Packages, Apps, Tools,
Settings) in the glass tokens is approved by the owner; then implemented with
`homelayout.go` and `homefocus.go` kept in agreement.

### UX-7 — On-device design walk (P1, S) [HW]
**Done when.** Home, Start menu, Games library, Exit, Settings, Tools and Media
are walked on a Smart Pro and a Brick: focus halo visible over bright box art,
14 px floor text fits inside pills (package card overline, media hub badges,
Wi-Fi badge), Remote Play capsule width, footer hints on the 960 px canvas, the
first-run Continue card. Findings filed as tasks with photos.

### UX-8 — Touch targets and safe area everywhere (P2, M)
**Why.** `growTouchTarget` covers Home, the top bar, the bell and the browser;
scene-specific panels and the panel's rounded-corner margin are not done.
**Where.** `touchtarget.go`, scene panels.
**Done when.** Every pressable control has a ≥ 44 px hit area centred on what is
drawn; no control sits in the 32 px corner margin.

### UX-9 — Every state for every component (P2, M)
**Why.** Empty/loading/error/offline/success should look the same everywhere.
**Done when.** The ten most-used scenes use `UIEmptyState`/`UIErrorState`/
`UILoadingSkeleton` and the offline treatment from `offlineaware.go`; a list of
remaining exceptions is in the PR.

### UX-10 — First-run flow review (P2, S) [HW]
**Where.** `onboarding.go`, `setupchecklist.go`, `controlslegend.go`, Home's
first-run Continue card.
**Done when.** A new user reaches "add games / connect Wi-Fi / pick a theme" in
≤ 3 presses from first boot; the controller legend matches the device.

### UX-11 — Quiet mode for notifications (P3, S)
**Done when.** A Do-not-disturb switch in Quick Settings holds toasts, chimes and
UI sounds (alarms excepted) until turned off.

### UX-12 — Every screen on the tokens (P1, M)
**Why.** Hard-coded colours and sizes bypass themes, High Contrast and the
colour-blind palettes: `companionui.go` (opaque slate cards),
`quicksettings.go:109-133` (a literal colour per toggle),
`notifications.go:607-641` (literal priority dots), `dashboard.go` (16 literal
colours), `pomodoro.go`, `media_hub_render.go` (19 literal sizes),
`pkgui.go:349,400` (white/black overlays that break light themes), and
`videoplayer.go`, `dynamicbg.go`, `games.go`, `canvas.go`.
**Done when.** Each uses `ui_glass.go`/`design.go` tokens and roles; a grep for
`colorNew(`/`sdl.Color{R:` in UI code lists only documented exceptions (art,
game palettes).

### UX-13 — One hint vocabulary (P2, S)
**Why.** The D-pad is spelled seven ways ("D-Pad", "Up/Down", "↑ ↓", "←/→",
"←→", "↑↓", "Arrows"); badges mix "A / Enter" with pad-only rows.
**Done when.** `docs/STYLE.md` defines the spellings ("D-Pad", "Up/Down",
"Left/Right", pad name first, keyboard twin after " / "), every hint follows it,
and `inputhints.go` relies on it.

### UX-14 — Destructive confirmations default to safety (P2, S) [OWNER]
**Why.** UX principle 8 (docs/UX_REVIEW.md) says confirmations open on the safe
choice; the new Home Exit card focuses Exit on purpose (`home_power.go:55`, see
its comment at :11-13), so A, A quits. Quitting is a clean shutdown, so the
owner decides whether Exit is the exception.
**Done when.** Exit, delete, forget, reset and quit cards open focused on
Cancel; the choice is remembered nowhere.

### UX-15 — A motion system (P3, M)
**Done when.** Enter, exit, focus and sheet transitions use the curves in
`motion.go` everywhere (one duration scale), all honour reduced motion, and the
Start menu, sheets and toasts share one entrance.

### UX-16 — Trophy set two on a device, and its leftovers (P2, S) [HW]
**Why.** The 2026-11-10 pass took Trophies from 101 to 401 (`trophy_set2.go`,
the event API in `trophy_events.go`, about 175 one-line hooks across the
features) — all from reading, none of it compiled or run. A few counters were
left short on purpose: RetroAchievements counts at most 12 unlocks per sync
(`trophyRANewUnlocksMax`), an artist is only known once Media's library was
opened in the session (`mlibCurrentModel`), WPE Browser page loads are not seen,
and games marked Completed or JukaLand blocks placed before the update are not
carried over (`gmeta` milestones, the saved JukaLand stats could seed them).
**Where.** `trophy_events.go`, `playtime.go`, `trophy_set2.go`, `trophy_room.go`
and the hook sites (grep `trophyEvent`).
**Done when.** On a Smart Pro and on Windows: the first start on an old save
shows its catch-up as a few cards (golds first) and one notification row; the
Category chip lists the groups with their counts; a screenshot, a track, a page
and a Snake round move their trophies' progress; the platinum's count leaves out
what the setup cannot award and the bonus trophies; the leftovers above are
done or written off here.

---

## Games and emulation (GAME)

### GAME-2 — Save timeline (P1, M)
**Why.** Lost saves are the loss that actually happens; snapshots already exist.
**Where.** `savemanager.go`, `savemanager_ui.go`, `timelapse.go` (knows when a
launch starts/ends).
**Done when.** While a game runs, its save roots are snapshotted every N minutes
(setting, default 10, bounded count); Save Manager shows a per-game timeline
with "restore this moment" (using the existing integrity-checked restore).
**Verify.** [HW] a RetroArch session's mid-play snapshot restores after exit.

### GAME-3 — Checkpoints and quick switch (P2, XL)
**Where.** Design in [docs/CHECKPOINTS.md](../docs/CHECKPOINTS.md).
**Done when.** Split into the design's phases as separate tasks before code; a
1 GB device cannot hold two suspended emulators, so phase 1 is named per-game
checkpoints only.

### GAME-4 — Per-game controller remaps for RetroArch (P2, M)
**Where.** `gameoptions.go`, `game_options_ui.go`, `inmap*.go`.
**Done when.** The game options sheet writes a RetroArch per-content remap file
(and `--appendconfig` where needed) from a small editor; standalone emulators
are explicitly out.

### GAME-5 — Cheats (.cht) per game (P2, M) [NET]
**Done when.** libretro cheat files install as a package category; the game
options sheet toggles cheats per game and hands RetroArch the file at launch.

### GAME-6 — Shader, bezel and overlay packs (P3, M)
**Done when.** RetroArch shader/overlay packs install through the package engine
(new category) and are selectable per system in game options.

### GAME-7 — Box-art: choose image and alias editor (P2, S)
**Where.** `romsetup.go` (`artworkTitleVariants`), `game_library_ui.go`.
**Done when.** A "Choose image" action picks a local image or a libretro
thumbnail candidate list; users can add their own title aliases without a
rebuild.

### GAME-8 — Rich metadata provider (P3, M) [NET] [OWNER]
**Done when.** Descriptions/genres/dates come from a provider with an account
(ScreenScraper or TheGamesDB) behind the existing downloader, opt-in, with the
key stored through the secret store.

### GAME-9 — RetroArch hotkeys per device, measured (P2, S) [HW]
**Why.** Hotkey binds are numeric button codes; guessing breaks controls.
**Where.** `osdpreset.go` (never writes `input_*` today, by design).
**Done when.** Measured profiles for Smart Pro and Brick exist and the OSD preset
can write menu/screenshot/fast-forward hotkeys for those devices only.

### GAME-10 — Netplay lobby with QR invite (P2, M)
**Where.** `netplay.go`, `netplay_ui.go`, `nearby*.go`, `party*.go`.
**Done when.** Hosts announce a room on the LAN; joiners see a list and can join
from a QR invite; the RetroArch netplay launch path is reused.

### GAME-11 — PC game streaming (Moonlight) tile (P2, M) [NET]
**Why.** Streaming PC games is one of the best uses of a handheld on home Wi-Fi.
**Done when.** A Moonlight build for aarch64 (PortMaster's or a pinned
release) installs from the Apps/Packages flow and launches with the device's
controls; pairing PIN shown in-app.

### GAME-12 — Daily challenge over the library (P3, S)
**Where.** `game_random.go`, `playtime.go`, `trophy_*.go`.
**Done when.** A daily pick with a simple goal (play 15 minutes of X) appears on
Home's Continue card when nothing is in progress; completion earns a trophy.

### GAME-13 — Per-game launch options (P2, M)
**Why.** `game-launchers.json` sets flags per system, not per game, so one
troublesome ROM cannot get its own core, flags or environment; the library
docs list this as the next piece of the same data.
**Where.** `game_library.go` (`game-launchers.json`), `emulators.go`,
`gameoptions.go` / `game_options_ui.go`, keyed by content hash like LIB-4.
**Done when.** The game options sheet edits extra arguments, a core override and
environment variables for one game; the launch path uses them for RetroArch and
standalone emulators; the override survives a rename and goes with the game's
metadata when it is removed; a test pins argument order and that an empty
override changes nothing.

---

### GAME-14 — Core update hygiene on a full CrossMix card (P2, S)
**Why.** Emulator updates now install by default. CrossMix ships ~100 cores, some
custom builds, and every update keeps a pool copy (up to 5) plus up to 5 `.bak`
files; the CRC cache is in memory, so the first pass after every boot re-reads
every installed core; MAME-family nightlies change the romset version.
**Where.** emulatorupdates.go (`maxCorePoolBuilds`, refusals), emulatorinstall.go,
emuupdatecheck.go (`emuCachedCRC32`), cfgdoc.go (inspector reset).
**Done when.** Auto-pass builds are not also pooled on the handheld (or the pool
cap is 2); the CRC cache persists by path+size+mtime; `mame*` and `fbneo` are
never auto-updated (a press still can); a core whose CRC matches a known
CrossMix-shipped build is asked about once instead of replaced; resetting
`coreUpdateMode` in the inspector also drops `emuAutoUpdate` and
`coreUpdateModeChosen`.
**Verify.** Unit tests for each rule; a device pass on CrossMix (HW-2 step 4).

---

### GAME-15 — JukaHub's PPSSPP, second round (P2, S)
**Why.** The managed PPSSPP (ppsspp_managed.go) shipped without a few pieces.
**Where.** ppsspp_managed.go, ppsspp_managed_ui.go, emulatorinstall.go.
**Done when.** Emulators **L1** "Update all" includes JukaHub's PPSSPP; the card has
"Go back to the previous build"; `state.json` is written before
`game-launchers.json` so a power cut between them cannot lose the user's original
PSP pin; save binds left by a SIGKILL are unmounted at JukaHub's next start (not
only at the next PSP launch), and the mount/process checks resolve symlinks; the
pak's own extras (Brick aspect ratio, RetroAchievements sync) are considered.
**Verify.** Unit tests; HW-2 step 5.

---

### GAME-16 — What else differs between JukaHub's and CrossMix's RetroArch (P2, S) [HW]
**Why.** The scoped retroarch.cfg edits and the restored environment fix the two
causes found by reading; others are possible and unverified.
**Where.** sound.go (`initAudioDevice` stays open during games), netplay.go (starts
`ra64.trimui` directly, bypassing CrossMix's common launcher: CPU, audio
switcher, inputd MENU hotkey), savemanager.go `resolveRetroArchSaveDir` and
recordings.go (`~` resolved with JukaHub's HOME, RetroArch's is its folder),
ports.go (PortMaster keeps JukaHub's environment), lowbattery.go
`retroArchConfigValue` (last occurrence of a duplicated key wins; RetroArch's
reader may keep the first).
**Done when.** Each is checked on a device (`/proc/asound/card0/pcm0p/sub0/status`
while RetroArch runs from JukaHub; netplay on CrossMix) and fixed or closed.
**Verify.** HW notes in the task; tests for any code change.

### GAME-17 — CrossMix's per-system RetroArch configs (P2, S) [HW]
**Why.** JukaHub's launch-time RetroArch settings (save-state resume,
recordings, the OSD preset and now the spatial-audio filter) go into the
configs `raScopeTargets` finds. A CrossMix launcher may start RetroArch with a
per-system config that never sees them.
**Where.** `racfgscope.go` (`raLaunchCandidates`, `raScopeTargets`),
`savestate_resume.go`, `recordings.go`, `osdpreset.go`, `spatial_games.go`.
**Done when.** The configs CrossMix's launchers pass are known and are part of
a launch's targets for all four, and a CrossMix game's RetroArch log shows the
values.

---

## PS Remote Play (PSR)

### PSR-1 — Wake from rest mode on real consoles (P1, S) [HW]
**Why.** The wake follows chiaki-ng's protocol byte for byte but has never met
a console, and its timings are guesses.
**Where.** `psremote_wake.go` (`psWakeSettle`, `psWakeReach`, `psWakeTimeout`),
`psremote_ui.go`.
**Done when.** On a PS5 and a PS4 in rest mode, A wakes the console and the
stream starts (raise `psWakeSettle`, 3 s, if the first stream after a wake
fails); X wakes without streaming; B stops waiting; a console shut down
completely ends in "did not answer" after 6 s and its row offers Try anyway;
the rest-mode menus the failure toast names match both consoles' firmware; a
console the router gave a new address is followed by its MAC on the next scan.
**Verify.** [HW] the above from a Smart Pro, and once from away: with the
router forwarding UDP 9302 (PS5) or 987 (PS4) to the console and its Internet
address set, does it answer discovery from outside its network and wake there?
Write the answer into `docs/REMOTE_PLAY.md`.

### PSR-2 — Every pad in a real stream (P1, S) [HW]
**Why.** `chiaki-stream` now opens every controller, merges them and follows
hotplug; only a harness with faked SDL and chiaki calls has run it.
**Where.** `native/chiaki-stream.c` (`pad_open`, `pad_close`, `apply_rumble`),
`psremote.log`.
**Done when.** On a Smart Pro: the built-in pad works as before (the log lists
it as added) and Start + Select quits; a Bluetooth DualSense or DualShock 4
paired before the stream, and one switched on during it, both appear in the
log, and their buttons, sticks, triggers, touchpad (click and fingers) and
rumble reach the console; switching a pad off while it holds a button leaves
nothing held; with two pads, drift on the built-in stick does not override the
Bluetooth stick; a Switch-layout pad's bottom button is Cross.
**Verify.** [HW] a stream for each case, `psremote.log` attached.

### PSR-3 — Motion from a DualShock 4 or DualSense (P2, M)
**Why.** Some PS4 and PS5 games aim or steer with the pad's gyro; the stream
sends idle motion.
**Where.** `native/chiaki-stream.c`; chiaki-ng's `orientation.h` at the pinned
commit.
**Done when.** SDL sensor events (gyro, accelerometer in g) fill the merged
`ChiakiControllerState` through chiaki-ng's orientation tracker for the pad in
use, written only after `chiaki_orientation_tracker_update`'s signature at the
pinned commit has been read.

### PSR-4 — Check chiaki-regist before a PS5 pairing (P2, S)
**Why.** A stream helper the firmware cannot load is stopped before JukaHub
quits; PS5 pairing still runs `chiaki-regist` unchecked and fails with a loader
error.
**Where.** `psremote_preflight.go` (`psNativeHelperProblem`),
`psremote_regist.go` (`ps5Register`).
**Done when.** `ps5Register` asks `psNativeHelperProblem` about
`engine.Regist` with its runtime folder first, and a refusal is the pairing's
error message.
**Status 2026-11-10 (evening).** Code done, unbuilt: `ps5Register` asks
`psNativeHelperProblem` with `psRegistHelperLibraryPath` (the Brick runtime,
then JukaHub's own library path) and returns "the PS5 pairing helper won't run
here: …"; the helper installer starts both helpers once before installing them.
Still to do: a device run that shows the message for a refused helper.

### PSR-5 — The helper check off the render thread (P2, S)
**Why.** The first Play starts the helper once, up to about two seconds, on
the main loop before the hand-off; only later presses use the cached answer.
**Where.** `psremote_preflight.go`, `psremote_ui.go` (`psOpenPanelFor`).
**Done when.** Opening the panel warms the answer in the background, and Play
never waits for the probe.

### PSR-6 — Rumble between the pad in use and the handheld's motor, on a device (P3, S) [HW]
**Why.** Since the 2026-11-10 helper the console's rumble goes to the pad with
the latest input, the handheld's motor runs only while that is the built-in
pad (or before anything was pressed), and the Rumble setting stops both. The
motor's strength floor (30 %), its scale and the 10 s safety stop are guesses.
**Where.** `native/chiaki-stream.c` (`rumble_target`, `apply_rumble`),
`native/stream_sys.c` (the motor), `psremote_stream_opts.go` (the motor nodes
JukaHub passes on).
**Done when.** On a Smart Pro with a Bluetooth pad in hand the motor stays
still and the pad rumbles, and back on the built-in controls the motor takes
over; game rumble feels like rumble, not a buzz or nothing, on a Smart Pro and
a Brick; a long rumble stops when the game stops it.

### PSR-7 — The CI helpers start on every handheld (P1, S) [HW] [NET]
**Why.** No release carried `chiaki-stream` and `chiaki-regist` before
`remote-play-helpers.yml`; they are now built against glibc 2.31 for firmware
with glibc 2.33, and nothing has started them on a handheld yet.
**Where.** `.github/workflows/remote-play-helpers.yml`,
`native/build-helpers.sh`, `psremote_preflight.go`.
**Done when.** Each TrimUI archive has `bin/` with both helpers, their glibc
check passes, and on a Smart Pro and a Smart Pro S the panel's check lets them
run; a refusal names what is missing in the toast and in the log.
**Verify.** [HW] install a CI build, open PS Remote Play, start a stream.

### PSR-8 — Remote Play through PSN, without port forwarding (P2, L) [NET] [OWNER]
**Why.** Away from home, Remote Play needs a router port forward or a VPN
(`docs/REMOTE_PLAY.md` → Playing away from home). Sony's app and chiaki-ng
reach a console anywhere through PSN ("remote connection via PSN": a session
through Sony's servers, then UDP hole punching with UPnP), and testers ask
whether it works "on a different network".
**Where.** `psn_signin.go` (the sign-in trades the code for an access token
and keeps only the Account-ID, by design), `psremote*.go` (consoles from the
PSN device list, a "through PSN" stream), `native/chiaki-stream.c` (chiaki-ng's
`lib/include/chiaki/remote/holepunch.h` — session init, UPnP discovery, session
create and start with the console's device id, the control and data holes —
handed to the session through `ChiakiConnectInfo`; chiaki-lib at the pinned
commit already builds the curl and miniupnpc it uses). Read that header at the
pinned commit before writing a call.
**Done when.** The owner has decided whether JukaHub may keep a PSN refresh
token (where, how it is protected, how a sign-out deletes it); with that, a
paired console streams from another network with no port forward, the token
renews itself, a refused or expired token says so, and the away-from-home
section of the docs describes it. Nothing changes for someone who never signs
in.

### PSR-9 — Choose the home or the internet address off the event thread (P2, S)
**Why.** With an Internet address saved, Play first asks the home address
(unicast discovery, up to 700 ms) on the event thread (`psChooseStreamHost`),
so the panel stops for that long whenever the console is away.
**Where.** `psremote_stream_opts.go` (`psChooseStreamHost`,
`psStreamProbeLAN`), `psremote_ui.go` (`psPanelPlay`), `psremote_wake.go` (its
worker already runs off the event thread and asks the same address); see also
PSR-5.
**Done when.** The choice is made in the wake worker or a goroutine of its own
while the row shows that it is busy, the hand-off starts from there, and a test
covers the home and the away outcome.

### PSR-10 — The new stream controls and display on a device (P1, S) [HW]
**Why.** The 2026-11-10 helper (button map, on-screen display, login passcode,
codec choice, status file) has run only against host fakes
(`native/tests/run-host-tests.sh`).
**Where.** `native/stream_input.c`, `native/stream_osd.c`,
`native/chiaki-stream.c`, `psremote_lastrun.go`, `psremote_stream_opts.go`.
**Done when.** On a Smart Pro and a Brick with a PS5 (and a PS4 if one is at
hand): Menu reaches the helper as the guide button and a tap opens the PS5's
control center, while a held Menu opens the stream menu instead and its
**Hold PS (Home)** reaches the PS5's home screen (the PS4's quick menu); Select clicks
the touchpad and Menu + Select is Create; Menu + D-pad swipes work in a game
that uses swipes, Menu + right stick moves a finger, and touches land where the
console expects them (PS5 1919×1079, PS4 1920×942); the display costs no
visible frames with the stream menu and the stats open; 720p30 in H.264 and in
H.265 on the A133P confirms the Auto codec choice; the log names the firmware's
SDL version (2.0.14 or newer enables a PlayStation pad's own touchpad); a
profile with a login passcode is asked for it and a saved one is entered; a
failed stream's toast and **Last stream** row appear on the next start; a
console with an Internet address streams through a port forward or a VPN; the
battery warnings appear at 15, 10 and 5 %.
**Verify.** [HW] `psremote.log` for each case; anything that is off becomes a
fix or a task.

### PSR-11 — Stream to the home address only when this console answers there (P2, S)
**Why.** With an Internet address saved, Play first asks the home address
(`psStreamProbeAt` in `psChooseStreamHost`) and takes any discovery answer as
the console. On another network a different PlayStation can have the same
private address, and the stream then goes to it with this console's keys and
fails with a refusal instead of using the internet address. The wake already
refuses an answer from another console (`psWakeSameConsole` compares the
answer's `host-id` with the pairing's `server_mac`); the stream probe does not.
**Where.** `psremote_stream_opts.go` (`psStreamProbeAt`, `psStreamProbeLAN`),
`psremote_wake.go` (`psWakeSameConsole`).
**Done when.** The probe accepts only an answer that can be this console (both
MACs known and equal, or one unknown), a mismatch counts as "not at home", and
a test covers match, mismatch and an unknown MAC.

---

### PSR-12 — Every psremote.json writer holds the lock (P2, S)
**Why.** Two writers of `psremote.json` — `psSaveStreamSettings`
(psremote_stream_opts.go) and `psUpdateConsole` (psremote_console_opts.go) — load
it without `psHostsMu` and save with it, so a pairing or an address move saved
by a goroutine in between is written over (the panel's own quality, volume and
Account-ID writes were fixed the same way on 2026-11-10).
**Where.** psremote.go (`psHostsMu`, `savePSHostsLocked`), psremote_stream_opts.go,
psremote_console_opts.go.
**Done when.** Both hold `psHostsMu` from load to save (`psPanelReload` outside it).
**Verify.** A unit test with two goroutines saving at once.
**Status 2026-11-10.** Code done, unbuilt: `psUpdateConsole` and
`psSaveStreamSettings` now hold `psHostsMu` from load to save and save with
`savePSHostsLocked` (`psPanelReload` and `psStreamSettingsSet` run after the
unlock). The two-goroutine test is still to write.

### PSR-13 — The helper install on a handheld (P1, S) [HW] [NET]
**Why.** A JukaHub without `bin/` now downloads the helpers when PS Remote Play
opens (`psremote_helpers_install.go`), but no release has carried
`JukaHub-RemotePlay-linux-arm64.zip` yet, and the install has only run against
a test server (`psremote_helpers_install_test.go`).
**Where.** `psremote_helpers_install.go`, `psremote_ui.go` (`psOpenPanelFor`,
`psPanelPair`, `psPanelPlay`), `psremote_wake.go` (`psPanelPlayOrWake`),
`.github/workflows/docker-image.yml` (Package Artifacts, Upload the Remote Play
helper package).
**Done when.** After BUILD-4, on a Smart Pro with `Apps/JukaHub/bin/` deleted:
opening PS Remote Play shows *Installing Remote Play…* with a moving
percentage, then the toast *PS Remote Play is installed*; `bin/` holds both
helpers and no `.remote-play-install/`; A on an unpaired PS5 during the
download says the PIN comes later, and the PIN keyboard opens by itself when
it ends (and the pairing works with that PIN); Play pressed during the
download streams when it ends, and B before then stops it; with Wi-Fi off the
list offers *Install Remote Play* and Retry works once it is back; a download
cut off mid-way resumes.
**Verify.** [HW] the steps above; the Logs screen shows `Remote Play helpers
installed from JukaHub-…`.

### PSR-14 — An address typed by hand gets a row while it pairs (P3, S)
**Why.** **Add console by address** pairs an address that no scan found, and
the console list only shows it once the pairing has succeeded: up to about a
minute of PS4 and PS5 handshakes (three PS4 generations, then PS5) with
nothing on screen but the PIN toast, and no row to say *Pairing*. Found while
reviewing the helper install, which no longer adds a wait of its own there.
**Where.** `psremote_ui.go` (`psPanelApplyEdit` for `psEditHost`,
`psPanelPair`, `chiakiRows`).
**Done when.** A typed address shows a row (an unsaved, unpaired console named
by its address, without a PS4/PS5 guess) from the PIN prompt until the pairing
ends, with the busy line while the handshakes run; a failure leaves it
listed as unpaired until the panel closes.
**Verify.** A unit test on `chiakiRows` with a typed host and a busy pairing.

---

## Xbox on the Smart Pro (XBX)

### XBX-1 — On-device hakuX validation (P1, M) [HW]
**Where.** `hakux.go`, `hakuxpage.go`.
**Done when.** On a Smart Pro: install from the Xbox tile, confirm the real zip
layout and launcher name, launch one XISO from the Games Library and from the
stock launcher, exit with Start+Select, R1 rollback after a second release;
provenance (`Emus/XBOX/.jukahub-hakux.json`) lists every file.

### XBX-2 — Settle the port's firmware needs (P2, S) [NET]
**Done when.** From the port's README: if it needs MCPX/flash/HDD files, their
paths are added to `hakuxFirmwareRows`; if not, the firmware row is removed.

### XBX-3 — Reproducible hakuX build (P3, M) [NET]
**Done when.** If the port documents its build (TrimUI SDK or
`ghcr.io/loveretro/tg5040-toolchain`), `.github/workflows/hakux.yml`
(workflow_dispatch, pinned tag) builds the package and attaches its SHA-256. Do
not guess build steps.

### XBX-4 — Brick Pro model code (P1, S) [HW]
**Why.** TrimUI's store lists the Brick Pro as TG4070; `device.go` maps it to
TG4040. Detection (and the hakuX offer) may miss real units.
**Done when.** The code read from a real Brick Pro is in `device.go`.

### XBX-5 — Per-game hakuX settings in the options sheet (P3, S)
**Done when.** Once the package's per-title settings layout is known, the game
options sheet opens it for Xbox games.

### XBX-6 — hakuX housekeeping (P3, S)
**Where.** `hakux.go`, `hakuxpage.go`.
**Done when.** Rollback (renames + deleting the kept copy) runs off the event
thread like the install; files an older release shipped but a newer one dropped
move into the kept copy instead of lingering in `Data/`; on equal version
numbers the asset without a pre-release suffix wins (`0.1.3` over `0.1.3-rc1`).

---

## Android games (AND)

### AND-1 — First device run of AndroidRT (P1, M) [HW]
**Where.** `player/androidrt/build-tsp.sh`, `docs/ANDROID.md` (device trial
steps).
**Done when.** On Smart Pro and Brick: the aarch64 fake games run, then one real
NativeActivity NDK game; the log shows which present backend worked
(`[androidrt][present] backend …`) and a game frame appears on the panel. If the
native backend shows a black panel, the same run with `ANDROIDPAK_PRESENT=sdl` is
logged (`SDL2 … video driver mali`, SDL loading `/usr/lib/libGLESv2.so*` — set
`ANDROIDPAK_GLES2_LIB` if `SDL_GL_LoadLibrary` fails), and the backend that shows
a frame becomes the stock-TrimUI default in `eglpresent.c` (today native, with
sdl as the automatic fallback). With sdl, the hub can still stop the game
(SIGTERM), the controller still reaches it, and a game that pauses and resumes
keeps presenting. An `auto` run that logs "the game made its GLES context before
its window" decides AND-13.

### AND-3 — Minecraft Bedrock first light (P2, L) [HW] [OWNER]
**Why.** The class pack is built from public knowledge only; it needs an owned
arm64 APK.
**Done when.** Every `[androidrt][jni] report:` entry and `undefined symbol` from
real runs becomes an implementation, run over run; the reached state (title
screen / license screen) is documented in `docs/ANDROID-MINECRAFT.md`. Store and
license answers stay honest.

### AND-4 — Text input for Android games (P2, M)
**Done when.** `showKeyboard` opens JukaHub's on-screen keyboard over the game
(IPC runner ↔ hub) and the text reaches the game through its natives.

### AND-5 — FMOD audio path on the device (P2, S) [HW]
**Done when.** FMOD falls back to OpenSL ES (`supportsAAudio=false`) and
`openslessim.c` latency is acceptable; an AAudio shim only if FMOD insists.

### AND-6 — Memory of big APKs (P2, S) [HW]
**Where.** `androidpak.go` (`androidPakRuntimeOverhead`, preflight floor).
**Done when.** RSS and page-cache pressure of a large APK run are measured and
the preflight refuses runs that cannot fit in 1 GB, with a clear message.

### AND-7 — Class packs by demand (P3, M)
**Done when.** SDL2's Java glue (`org/libsdl/app/SDLActivity`) and Unity's
`UnityPlayer` packs exist, built from real compatibility reports.

### AND-10 — Terraria through FNA with the user's PC files (P3, M) [NET]
**Why.** The Android build is Xamarin/Mono; AndroidRT refuses it at triage.
**Done when.** A package recipe (PortMaster-style) runs the PC version with FNA +
Mono on aarch64 from files the user copies; docs explain where they go.

### AND-11 — Bind the game's GLES library next to the chosen libEGL (P3, S)
**Why.** The present layer binds the game's `libEGL.so` to its exact library
(`apak_ldSetHostLib`), but `libGLESv2.so`/`libGLESv1_CM.so` are still found
through the search path; with a GPU package outside it (ge8300-drivers under
`/mnt/SDCARD/Apps`) the game's GL imports can miss or hit another driver than its
EGL.
**Where.** `androidrt/src/eglpresent.c` (`apak_presentInit`), `bionicld.c`
(`kVirtual`).
**Done when.** The GLES libraries beside the chosen libEGL (same directory, same
naming) are bound with `apak_ldSetHostLib`; `ANDROIDPAK_GLES2_LIB` still wins; a
smoke case proves the binding.

### AND-12 — Surface probe measures the SDL path (P3, S)
**Why.** `ANDROIDPAK_SURFACE=1` reports the GBM and native layers but not whether
the firmware SDL2 loads, which video driver it picks and whether its EGL comes up
— exactly what the first device run needs to choose between `native` and `sdl`.
**Where.** `androidrt/src/surface.c`, `eglpresent.c` (share `sdlLoadLocked`),
`tests/smoke`.
**Done when.** The probe prints an `SDL:` verdict line (library, version, driver,
EGL display up or why not) without creating a window, and the smoke suite asserts
it against `fake_sdl.c`.

### AND-13 — Auto's window fallback adopts a context-first game's context (P3, M)
**Why.** When `auto` hands over to SDL at the window surface, a game that created
its GLES context first (GLSurfaceView order) keeps that raw EGL context, so its
frames bypass `SDL_GL_SwapWindow` — exactly the path the stock-firmware report
says stays black. Today the log names `ANDROIDPAK_PRESENT=sdl` and the docs say
so; moot if AND-1 makes `sdl` the stock-TrimUI default.
**Where.** `androidrt/src/eglpresent.c` (`pr_eglMakeCurrent`,
`pr_eglCreateContext`, `pr_eglDestroyContext`), `tests/smoke`
(`EGLTEST_CONTEXT_FIRST`).
**Done when.** A raw context that has never been current (so holds no GL objects)
is mapped to SDL's context at its first `eglMakeCurrent` with SDL's surface
(destroy and share handled), one that was current with a pbuffer stays raw with
today's log, and the window-fallback context-first smoke case asserts 5
`SDL_GL_SwapWindow` frames.

### AND-14 — A prebuilt AndroidRT in the release (P1, M)
**Why.** The APK Store reads, judges and adds APKs, but no release carries
AndroidRT: on a fresh card its Runtime tab says the runtime is missing, and
building it needs an aarch64 toolchain few users have.
**Where.** `player/androidrt/build-tsp.sh`, the TrimUI packaging
(`.github/workflows/docker-image.yml`, `player/release.sh`,
`player/package-sdcard.sh`), `androidpak.go` (`resolveAndroidRuntime`),
`apkstore.go` (Runtime tab).
**Done when.** CI builds AndroidRT for the handheld with the host smoke suite
passing, every TrimUI archive carries it where `resolveAndroidRuntime` looks (or
the Runtime tab installs it, hash-checked), and on a fresh card the Runtime row
says it is ready.

### AND-15 — Verified downloads in the APK Store catalogue (P2, S) [NET]
**Why.** A catalogue entry may offer a free-software APK with its SHA-256, but
the built-in list has none: no URL or hash could be checked when it was
written.
**Where.** `player/apkstore_catalog.json` and the identical `/apk-catalog.json`
(served by the builder site), `apkstore_catalog.go`.
**Done when.** At least one free NDK game whose own GitHub release publishes an
arm64 APK is listed with its URL, size and SHA-256 checked by hand, and it
downloads, verifies and shows under Your APKs as a native game to try.

### AND-16 — Android games in the Games Library (P2, M)
**Why.** A game added from the APK Store becomes a game profile, and only Game
Profiles lists those; the Games Library never reads profiles.
**Where.** `game_library*.go`, `gameprofiles.go`, `apkstore_profile.go`.
**Done when.** An added APK shows in the library with its icon and title,
launches from there through the profile path, and leaves the library when the
store removes it.

### AND-17 — First APK Store run on a Smart Pro (P1, S) [HW]
**Done when.** On a real card the scan lists the APKs in the store's folders and
`PhoneDrop`; a Minecraft arm64 APK gets the class-pack verdict and a Java app
the "needs Android's Java runtime" one; labels and icons read right; Add puts
the game in Game Profiles; and Play reaches AndroidRT's launch log, or names the
missing runtime. The scan's time and memory on a full card are noted in
`docs/ANDROID.md`.

### AND-18 — Split APKs and app bundles (P3, M)
**Why.** Apps copied off a phone often come as split APKs (`base.apk` plus
`split_config.arm64_v8a.apk`) or as `.xapk`/`.apks` bundles. The native
libraries then sit in a split, and AndroidPak stages one APK, so such an app
cannot be added as one game yet.
**Where.** `apkstore_scan.go`, `apkstore_verdict.go`, `androidpak.go` (staging).
**Done when.** The store groups a base APK with its splits (same package and
version, side by side or inside a bundle), judges them as one app, and
AndroidPak stages the native libraries from the matching split.

---

## Media (MED)

### MED-1 — Cancellable downloads end to end (P2, M)
**Where.** `downloads.go`, `downloadsui.go`, `dl_library.go`.
**Done when.** Cancelling stops the transfer immediately, removes partials, and
survives a restart (no zombie entries).

### MED-2 — Jellyfin/Plex client (P2, L) [NET]
**Done when.** A server can be added (address + login through the secret store),
libraries browse as shelves in the media hub, and items play through the existing
decoder with resume.

### MED-3 — Reactive visualiser from real audio (P3, S)
**Where.** `musicplayer.go` (synthetic bars today), `ledsync.go` (`audioBus`).
**Done when.** The bars come from an FFT of the decoded PCM (bounded cost per
frame).

### MED-4 — Scrobbling (P3, S) [NET]
**Done when.** ListenBrainz (token via secret store) receives plays from the
music player, queued offline.

### MED-5 — Screenshot gallery by game (P3, S)
**Where.** `screenshot.go`, `imageviewer.go`, `recordings.go`.
**Done when.** Screenshots group by game/system in the image viewer.

### MED-6 — Audio and subtitle tracks (P2, M)
**Why.** Only the first embedded subtitle or a sidecar file plays; there is no
audio-track picker.
**Done when.** The video settings sheet lists audio and subtitle tracks (from
ffprobe), remembers the choice per language, and styles subtitles (size,
background, A11Y-4).

### MED-7 — Gapless music (P3, M)
**Done when.** Album playback is gapless (pre-decoding the next track into the
same stream) with optional crossfade and ReplayGain.

### MED-8 — Podcasts offline (P2, M)
**Done when.** Episodes download for offline listening (Downloads queue), OPML
import/export works, chapters are shown, played episodes clean up.

### MED-9 — Audiobooks (P3, M)
**Done when.** `.m4b` chapters, a book shelf with covers, per-book speed and
sleep timer (`audioresume.go` keeps position today).

### MED-10 — Internet radio directory (P3, M) [NET]
**Why.** Promoted from the parking lot.
**Done when.** Stations from radio-browser.info by country, tag and search,
favourites, buffering state and now-playing metadata.

### MED-11 — Comics (P3, M)
**Done when.** CBZ/CBR/CB7 open in a reader with fit width/height, two-page
spread and right-to-left (manga) mode, remembering the page.

### MED-12 — E-books that remember (P2, S)
**Why.** Reading position and bookmarks are not saved (`reader.go`,
`pdfv_doc.go`).
**Done when.** Position, bookmarks and text size persist per book; the library
shows progress.

### MED-13 — TV guide (P3, M) [NET]
**Done when.** XMLTV EPG for M3U playlists that name one (`url-tvg`), shown as
now/next in the channel list.

### MED-14 — Media servers on the LAN (P3, M)
**Done when.** DLNA/UPnP servers are discovered (SSDP) and browsable as a Media
source; a Kodi remote (parking lot) uses JSON-RPC.

### MED-15 — A music library you can browse (P2, M)
**Why.** The player walks one folder at a time (`musicLoadPlaylist`), so a card
with 40 GB of music has no artist, album or search view; `docs/MUSIC_PLAYER.md`
lists the index as the first roadmap phase.
**Where.** `musicplayer.go` (playlist helpers), `musiclibrary.go` (queue and
favourites today), `musicuifeatures.go`; a cached index beside the other data
files.
**Done when.** A scan of the music root caches path, size, mtime, title, artist,
album and duration; Audio browses by folder, artist, album and all tracks, with
search and natural sort; a rescan picks up added or removed files without
reading every tag again; the cache is bounded like `cachebudget.go` and never
lives in `jukaconfig.json`.
**Verify.** 2,000 tracks on a card: first open and a one-file rescan each under
a second.

### MED-17 — Listening history and continue listening (P3, S)
**Why.** Playtime exists for games, but music keeps no history: no recently
played, no top artists, no "continue this album", which the player's roadmap
lists as phase 5.
**Where.** `musicplayer.go`, `musiclibrary.go` (the favourites store as the
pattern), `playtime.go` for the bounded-log shape.
**Done when.** Plays append to a bounded local log (path, title, artist, start
and seconds played, 400 days like LIB-13); Audio shows Recently played, Top
artists and Continue listening; LIB-13's export includes the log and a
Settings row clears it.

### MED-18 — Trim, rename and delete a recording (P2, M)
**Why.** The Recordings screen only lists and plays captures;
`docs/RECORDINGS.md` says trimming, renaming and deleting are a PC job, which is
exactly the moment the user is away from one.
**Where.** `recordings.go` (`scanRecordings`, `loadRecordings`), the scene that
draws them (`core.go` callers), the video player for the trim preview.
**Done when.** A recording can be renamed and deleted after a two-press
confirmation (moved to the APP-4 trash once it lands), and trimmed to in/out
points with ffmpeg stream copy where the codec allows it (re-encode otherwise),
keeping the original until the trimmed file plays; the list shows duration and
size.
**Verify.** Trim a 2-minute clip on Windows and on a Smart Pro; the result
plays and the original is intact until the trim is confirmed.

### MED-19 — A JavaScript runtime for yt-dlp (P1, M) [HW]
**Why.** Since late 2025 yt-dlp needs an external JavaScript runtime (Deno by
default) for full YouTube support. Without one it is down to the android
client's 360p single-file format 18, which YouTube may stop serving at any time
— late-October 2026 reports already disagree on whether it plays without a
token.
**Where.** `ytdlp_update.go` (the download, SHA-256 check and swap to copy),
`patch_engine.go` (`helperToolEnv` already puts the tools folder first on
PATH), `streaming.go` (`streamAttempts`).
**Done when.** JukaHub installs a pinned, checksum-verified Deno (≥ 2.3) next
to yt-dlp on Windows and — if the TrimUI's glibc and 1 GB of RAM allow it
(about 100 MB unpacked) — on the handheld; yt-dlp is pointed at it
(`--js-runtimes`), the stream attempts prefer yt-dlp's default clients while a
runtime is present, and the Patch screen shows its version. Anything above
360p also needs MED-20.
**Verify.** On a Smart Pro a video resolves through the default clients with
the runtime, and still plays through format 18 without it.

### MED-20 — Two-stream and HLS playback (P1, L) [HW]
**Why.** The player opens one URL that carries audio and video together, which
caps YouTube at 360p (format 18) and fails outright the day YouTube drops it.
The download fallback (`core.go` `playWithTempFile`) stops at five minutes, so
a long video on slow Wi-Fi cannot play that way either.
**Where.** `videoplayer.go` (`ffmpegDecoderArgs`, `startFFmpegAudio`),
`streaming.go` (`resolveStreamSource`, `probePlayableStream`), `ffmpegtls.go`
(the https relay already rewrites HLS playlists), `core.go`
(`playWithTempFile`).
**Done when.** A separate video + audio pair (adaptive formats) plays in sync
and seeks; an HLS master (`hlsManifestUrl`) plays as one input — yt-dlp's
visionos client reportedly returns one with muxed H.264 variants (itags 91–96)
and is the first thing to try when android's format 18 is refused; the
resolver prefers these over format 18 when they exist; and the download
fallback's limit follows size and speed instead of a fixed five minutes.
**Verify.** First by hand on a device: `yt-dlp -g -f 91/92/93 --extractor-args
youtube:player_client=visionos <id>` and play the URL with ffplay. Then a
10-minute 720p video plays and seeks on a Smart Pro and on Windows.



### MED-22 — Send the stream's own user agent to the player (P2, S) [HW]
**Why.** The native CDN check decides with the player's identity and, on a
refusal, logs whether the producing client's own user agent would have been
accepted ("…HTTP 403 to the player's identity but HTTP 2xx to the ANDROID
app's own user agent…"). If YouTube ties ANDROID URLs to that agent, the player
has to send it.
**Where.** `youtubenative.go` (`ytNativeClientUserAgent`), `videoplayer.go`
(`ffprobeStream`, `ffmpegDecoderArgs`, `startFFmpegAudio`), `ffmpegtls.go` (the
relay's default headers).
**Done when.** For googlevideo URLs whose `c=` names a client (native, or
yt-dlp `-g` with `player_client=android`), the probe, the decoder and the relay
send that client's user agent; other hosts are unchanged.
**Verify.** Needed only once the log line above appears on a device.
**Note.** Since 2026-11-09 the decoders send `streamUserAgent` like the probe
(`ffmpegStreamInputArgs`, `videorecover.go`), so this task only changes which
agent that is.

### MED-23 — "Update yt-dlp now" on the Patch screen (P2, S)
**Why.** `ytdlp_update.go` updates on its own schedule (Media opened with a copy
over ten days old, or a failure an update can fix), so a user who hears of a
YouTube change cannot ask for the new release; and the first-run install
(`patch_engine.go` `downloadYtDlpCtx`) is not checked against the release's
SHA-256 list the way updates are.
**Where.** `ytdlp_update.go` (`requestYtDlpUpdate`), the Patch screen's
helper-tool rows, `patch_engine.go`, `toolbox.go` (its manifest records a tag
and a hash).
**Done when.** The yt-dlp row shows the installed version and an "Update now"
action that runs the updater without the age gate (still waiting for a playing
video); the first install verifies SHA2-256SUMS too; the tools manifest records
the release tag and hash.

### MED-24 — A YouTube client table that updates without a release (P2, M) [NET]
**Why.** The native path's client names and versions are constants
(`youtubenative.go` `ytNativeClients`; the web search client takes the live
version from the homepage but falls back to a constant), and YouTube retires
versions every few months: the ANDROID_VR refusal of 2026-08-17 broke every
install until an app change.
**Where.** `youtubenative.go`, `youtubesearch.go`, the update channel.
**Done when.** A small JSON (client name, version, user agent, client ID,
order), fetched at most daily and checked (signature or a pinned hash list), is
cached and used ahead of the built-in table; a bad or unverifiable file is
ignored with a log line.

### MED-25 — YouTube on a stock Smart Pro (P1, S) [HW]
**Why.** The evening pass of 2026-11-09 reworked how Media resolves YouTube
(android client first with a CDN check, yt-dlp auto-update with rollback,
failure messages that name the cause) from web reports alone; none of it has
run on a device.
**Where.** `youtubenative.go`, `streaming.go`, `ytdlp_update.go`,
`ytfailure.go`; log lines tagged `youtube`, `stream` and `tools`.
**Done when.** With the tools folder's yt-dlp deleted, Media installs it and a
search result streams within a few seconds (not "Downloading…"); an old yt-dlp
(or one with its mtime set three weeks back) is updated on the next Media visit
and Patch shows the new version; a failing search names its cause and says no
API key is needed; a browser link without text gets its title on the Continue
card; on Windows an update swaps a busy `yt-dlp.exe`. Record whether android's
format 18 still plays — if it does not, MED-19 and MED-20 become P0.

### MED-26 — The stream fallback on a device (P1, S) [HW]
**Why.** `videorecover.go` (2026-11-09 night) turns a decoder that never
delivered a frame into a download or a message, instead of a player that
closed after a second; it was written from reading alone, for a user report
("YouTube videos open a black canvas for a second, but then crash").
**Where.** `videorecover.go`, `videoplayer.go` (`startFFmpegDecoder`),
`streaming.go` (`tryStreamPlayback`); errors.txt lines starting `decoder for`.
**Done when.** On a stock Smart Pro: a YouTube result either streams or shows
"Streaming didn't work — downloading the video instead" and then plays; the
errors.txt line says why the stream failed (record it here: "Protocol not
found", "403 Forbidden" or something else decides between the relay, the user
agent and the client); after two such failures the next video goes straight to
the download; a live YouTube stream still streams; a video paused for ten
minutes carries on when resumed (`-reconnect`) instead of ending; closing the
player during the loading picture shows no message.

### MED-27 — An old session's end takes a newer play's page (P2, S)
**Why.** `StopEmbeddedPlayback`, run by `finish()` on a decoder goroutine,
clears `currentPlaybackURL` and the play origin and calls `partyNoteStop` with
whatever page is current. When the user starts another video while the old
one is failing or ending, those belong to the new play: it then starts with the
CDN URL as its page (no browser user agent, no download fallback, resume and
Quality keyed to the wrong URL). `currentPlaybackURL` is also a plain string
written from several goroutines.
**Where.** `videoplayer.go` (`StopEmbeddedPlayback`, `startEmbeddedPlayback`),
`core.go` (`playVideoInfo`), `media_hub.go` (`mediaForgetPlayOrigin`).
**Done when.** The clears and `partyNoteStop` use the stopped session's own
page and leave a newer play's alone; `currentPlaybackURL` is read and written
through a mutex-guarded accessor.

### MED-28 — Watch Party guests open the watch page in ffmpeg (P2, S)
**Why.** `partynet.go` applies the room's video with `StartEmbeddedPlayback`
on the page URL the host published, so a YouTube room plays nothing on a
guest; since 2026-11-09 the guest at least sees "Couldn't play this stream".
**Where.** `partynet.go` (the apply path), `streaming.go`
(`resolveAndStartPlayback`).
**Done when.** A page URL from the room goes through the same load as Play
(stream, else download) at the room's position.

### MED-29 — Decoders have no read timeout (P3, S)
**Why.** ffprobe gets `-rw_timeout`; the two decoders do not, so a stream that
stalls without closing is a frozen picture (or the loading picture) forever,
which nothing can recover from.
**Where.** `videorecover.go` (`ffmpegStreamInputArgs`), `videoplayer.go`.
**Done when.** http inputs get a read timeout that `-reconnect` retries
through, and a stall that outlasts it ends as a failure `mediaRecoverStream`
handles; checked on handheld Wi-Fi that normal playback never trips it.

### MED-30 — Subtitles and saves on a device (P1, S) [HW] [NET]
**Why.** The 2026-11-10 pass (tester: "when subtitles enabled it says no
subtitles available") rebuilt CC around YouTube's own caption list, with yt-dlp
as the fallback, and added Save offline / Extract audio — all from reading, none
of it run. Whether the ANDROID client's caption URLs answer without a
proof-of-origin token decides whether the fast path ever works.
**Where.** `ytcaptions.go`, `videosubs.go`, `videosheet.go`, `mediaextract.go`,
`downloads.go` (the idle-queue subtitle step).
**Done when.** On a stock Smart Pro: CC on a YouTube video shows "Getting
English subtitles…" and then cues within a few seconds; the log says whether
"native captions" answered or stood down (record which here); auto-generated
captions show each line once; CC stays on for the next video; Y → Save offline →
Download video lands in Media → Downloads with `<name>.en.srt` beside it, and
that video plays with CC in flight mode; Extract audio on it gives a track in
the music player within seconds; a second Download press says "Already
downloading".

### MED-31 — Kill yt-dlp's whole tree on Windows (P3, S) [WIN]
**Why.** A cancelled subtitle fetch kills the helper's process group on Linux
(`processGroupConfigure` + `screenChildKill`), but on Windows `screenChildKill`
is `Process.Kill`, which stops only the one-file launcher of `yt-dlp.exe`: its
unpacked child keeps running and `%TEMP%\juka-subs-*` folders pile up.
**Where.** `videosubs.go` (`ytdlpSubtitles`), `screengroup_windows.go`.
**Done when.** A Windows-only tree kill (`taskkill /T /F /PID`, hidden window,
falling back to `Process.Kill`) stops every process of a cancelled run and its
temp folder is gone.



---

## System and connectivity (SYS)

### SYS-1 — Bluetooth devices screen on a device (P1, M) [HW]
**Status 2026-11-10.** Built, unverified on hardware: Settings → Network →
Bluetooth devices (`bluetooth_devices.go`, `bluetooth_devices_ui.go`; also in
the Start menu, search and PS Remote Play's Controllers row) scans, pairs,
trusts, connects, disconnects and forgets through bounded `bluetoothctl`
sessions (a pseudo-terminal, pipes as the fallback) and calls a pad ready only
with evidence from `/proc/bus/input/devices`.
**Why.** The page was written without a device. TrimUI's firmware notes promise
only "BLE hid", so classic-Bluetooth pads (DualShock 4, DualSense) may connect
without input, and spruceOS found stock pairings lost after a reboot or a radio
toggle.
**Done when.** Audio follows a connected headset (NET-10); pairings survive a
reboot and a radio toggle where the firmware keeps `/var/lib/bluetooth` in
tmpfs or changes the adapter address; which pads work on which firmware is
written in `docs/REMOTE_PLAY.md`.
**Verify.** [HW] On a Smart Pro and a Brick, stock 1.1.x and CrossMix:
`bluetoothctl` output arrives line by line in the session, not only at exit;
`agent NoInputNoOutput` and `default-agent` answer; a search lists a
DualSense, a DualShock 4, an Xbox pad and an 8BitDo pad in pairing mode within
10 s; record which get an input device (Bus=0005 with the pad's address) and
which show **No input**; pairings after a reboot and a radio toggle;
`bluetoothd` running when the stock menu has Bluetooth off; Remote Play
stutter with a pad connected against Wi-Fi alone.

### SYS-2 — Wi-Fi verified on both stock images (P1, S) [HW]
**Where.** `wifi*.go`, `docs/WIFI.md`.
**Done when.** Interface name, `wpa_cli`/socket path and DHCP client confirmed on
Smart Pro and Brick images; join, forget and the diagnostics sheet work.

### SYS-3 — LAN Play end to end (P2, S) [HW]
**Done when.** A RetroArch netplay session between two devices on one network
completes through the LAN Play panel.

### SYS-4 — Focus mode with a PIN (P3, M) [OWNER]
**Done when.** A persisted Simple mode shows only Home, Media, Games and
Settings → Simple; leaving needs a PIN (hashed in the secret store); the setting
says plainly it is not parental security.

### SYS-5 — Playtime limits and break reminders (P3, S)
**Where.** `playtime.go`, toast pipeline.
**Done when.** Optional daily limit and break reminder; a launch over the limit
asks for confirmation.

### SYS-6 — Event webhooks (P3, S)
**Done when.** Opt-in HTTP POSTs for "game launched", "trophy unlocked",
"install finished" (bounded queue, no secrets in payloads).

### SYS-7 — Suspend/resume behaviour (P2, S) [HW]
**Done when.** Sleeping and waking the device (power button) with JukaHub on
screen, in a game and during video is tested; pollers pause, audio resumes, the
clock and Wi-Fi status refresh on wake.

### SYS-8 — Storage you can manage (P2, M)
**Why.** Disk usage is totals per volume; there is no "free up space".
**Done when.** Settings → Storage shows usage by kind (games, media, downloads,
saves, caches, backups, old versions, trash) with one-press clean-ups and a
warning at 90% full; the card's filesystem dirty flag is checked at boot
(`sdhealth.go`).

### SYS-9 — Move to a new device (P3, M)
**Why.** Switch 2 ships device-to-device transfer; JukaHub has handoff and save
sync but no "move my setup".
**Done when.** Two paired devices (`peerpair.go`) can transfer settings,
themes, collections, playtime, saves and the package list over the LAN in one
flow, with a summary of what moved.

### SYS-10 — Import from the stock launcher and other firmwares (P3, M)
**Done when.** First run offers to import TrimUI MainUI favourites, recents and
playtime, and NextUI/muOS collections where their files are on the card.

### SYS-12 — The device's zone travels with backup and transfer (P3, S)
**Why.** The zone lives in `jukatimezone` next to the binary, not in
`jukauser.json`, so Backup & restore and a future system transfer (SYS-9) leave
it behind.
**Where.** `bkup.go`, `datetime.go` (`dateTimeApplyZone`).
**Done when.** A backup carries the zone and a restore applies it through
`dateTimeApplyZone` on the handheld (ignored on desktops, where the OS owns it).

### SYS-13 — Scheduled tasks after a clock jump (P2, S)
**Why.** Alarms and playtime re-anchor after network time moves the clock
(`clockjump.go`); scheduled tasks (`sched_model.go`) compute their next run from
the wall clock and were not checked.
**Where.** `sched_model.go`, `sched.go`, `clockjump.go` (`clockJumpView`).
**Done when.** A jump forward does not fire every missed run at once and a jump
back does not run a task twice; a table test covers both.

### SYS-14 — Chat files against the real channel (P1, S) [NET] [HW]
**Why.** Sending files in chat (2026-11-09) uploads with a hand-written
streaming multipart POST (`chatupload.go`: `payload_json` + `files[n]`, the
session's `Authorization`/`User-Agent`, `nonce` + `enforce_nonce`), and nothing
of it has met Discord yet. The limits it assumes (20 MiB a file, 50 / 100 MiB
on boost tier 2 / 3) changed in 2026 and may change again; text messages still
go through discordgo without a nonce, so a text post whose answer is lost can
appear twice.
**Where.** `chatupload.go` (`chatDiscordUpload`, `chatUploadAnswer`,
`chatEchoCandidate`), `chatfiles.go` (downloads, `chatRefreshAttachmentURL`),
`chatsync.go`; docs/CONTROLS.md → "Sending files".
**Done when.** From a Smart Pro and from Windows: a picture, a 15 MB zip and a
log reach the channel with progress and show on the other device and in
Discord; a file over the server's limit is refused with "too big"; pulling
Wi-Fi mid-upload resumes without a second post; a picture older than a day
renews its link; Juka AI answers about an attached log. If a lost text answer
still duplicates, text posts move to the same raw POST with a nonce.

### SYS-15 — Archives on the card and on Windows (P1, S) [HW] [WIN]
**Why.** Extracting archives from Files (2026-11-10) has only been checked
statically: the in-process zip and tar readers, the `os.Root` writes onto an
exFAT card, the free-space check, the staging folder's rename into place and
the hand-off to 7-Zip (`7zz` from JukaCrossMix's `System/bin` on the handheld;
an installed 7-Zip, the fetched `7zr.exe` or `tar.exe` on Windows) have never
run.
**Where.** `files_extract.go` (`feExtractRun`, `feExtractZip`,
`feExtractTarStream`, `feExtractFinish`), `files_extract_tools.go`
(`fe7ZipPreflight`, `feRun7Zip`), `files_extract_ui.go` (the sheet,
`feExtractDrain`); docs/CONTROLS.md → "Archives".
**Done when.** On a Smart Pro: a 700 MB PS1 zip (cue and bins) extracts into its
own folder with the bar moving, and the library finds the game; a one-ROM zip
lands beside itself; **Extract here** over a folder that has the files asks
twice and replaces them; a .7z and a .rar unpack through `7zz`; a cancel
mid-way, and pulled power mid-way, leave no `.jukahub-extract-*` behind (the
second is swept by the next extraction there); a zip bigger than the free space
is refused before anything is written; a file over 4 GB on a FAT32 card fails
with the FAT32 sentence. On Windows: the same zip and a tar.gz, a .7z with and
without 7-Zip installed (`7zr.exe` fetched once), a .rar through `tar.exe`, and
`go test -run 'FEExtract|Unzip|FEArchive|FE7Zip'` passes.

### SYS-16 — Screen brightness on a device (P1, S) [HW]
**Why.** The header's sun button and every other brightness control now drive
the TrimUI backlight through the Allwinner display driver's ioctl on
`/dev/disp` (DISP_LCD_SET/GET_BRIGHTNESS, raw 1..255, linear) in the
firmware's 10% steps, and write the level to `/mnt/UDISK/system.json`
"brightness" (assumed 0..10) so keymon's brightness keys carry on from it
(`brightnessctl.go`, `brightness_disp_linux.go`, `brightness_trimui.go`). The
ioctl layout, the key, its scale and keymon re-reading the file come from
MinUI, spruceOS and TriMux, not from a device, and the firmware's own
level-to-backlight curve is unknown.
**Where.** `brightnessctl.go` (`brightnessFindBackend` logs the backend once,
tag `brightness`, and `trimuiLogBrightnessEvidence` logs the file's
"brightness" and "lumination" beside the panel's raw level; each step is
written with the raw level the firmware was seen to use for it,
`brightnessFirmwareCurve`), `brightness_disp_linux.go`, `brightness_trimui.go`
(`trimuiSystemBrightnessStep`, `trimuiBrightnessFlush`, the in-place
`trimuiSystemJSONRewrite`), `qrzoom.go`.
**Done when.** On a Smart Pro and a Brick the log names the display driver; the
card's ten steps run from dim-but-readable to full and feel even; the
firmware's brightness keys (START + L/R, or MENU + volume) carry on from a
level set in JukaHub and the open card follows them; after sleep and wake, and
after quitting to the stock UI, the level JukaHub set stays (a firmware curve
unlike the linear one shows as a jump when keymon re-applies the file — then
match its curve); the pairing QR goes to full and gives the level back; a
`system.json` whose "brightness" is not 0..10 is left alone (the log says so);
and, booted straight into JukaHub with no keymon running, it is decided whether
JukaHub applies the firmware's saved level itself. If the log shows that
"brightness" is not the backlight (it does not follow the firmware's keys) or
not 0..10, stop writing it (only `trimuiBrightnessSyncLater` writes) and keep
the display-driver path. Also check that keymon still reacts to changes of
`system.json` after JukaHub wrote it (the file now keeps its inode:
`ls -i /mnt/UDISK/system.json` before and after).

### SYS-17 — Boot logo and the power-on boot on a device (P1, S) [HW]
**Why.** The power-on boot (2026-11-10) and the firmware boot logo installer
have only been checked statically. The installer assumes Allwinner's layouts —
`bootlogo.bmp` on a FAT `boot-resource`/`bootloader` partition, or a raw
`bootlogo` partition — found by name through `/dev/by-name`, the kernel's
`partitions=` and sysfs; TrimUI's actual storage (and what firmware 1.1.x's
"22 MB boot partition for the boot logo" changed) was never seen. The power-on
detection leans on the autostart hook's `.autostart-pending` marker and the
kernel uptime.
**Where.** `bootlogo.go`, `bootlogo_linux.go` (log tag `bootlogo`: the store,
its size and depth, every refusal), `bootos.go` (`bootOSDecide`, log line
`power-on boot (…)`), `bootarrive.go`, `bootlogo_layout.go`; docs/BOOT.md.
**Done when.** On a Smart Pro and a Brick (stock 1.0.4 and 1.1.x, and CrossMix):
the Boot logo row names a store or an honest reason; install shows the JukaHub
logo at the next power-on with nothing cropped or rotated, Restore brings the
TrimUI logo back, and the backup sits in `Apps/JukaHub/data/bootlogo/`; a
power-on into JukaHub plays the full boot (log says why) with the firmware's
picture handing over to the first frame without a visible jump, the bells on
the blooms and Home arriving card by card; a launch from the stock menu gets
the quick hello; a restart within the boot does not replay it; and the boot
holds 30 fps on the Smart Pro with motion on and with reduced motion.

### SYS-18 — Screen Share on the Smart Pro and the Brick (P1, M) [HW]
**Why.** Screen Share (2026-11-10, formerly Remote Cast) is JukaHub's answer to
the Smart Pro having no HDMI port, and none of it has run on a device: whether
`/dev/fb0` carries a game's GLES frames on the stock and CrossMix images (the
time-lapse recorder rests on the same assumption), which way a portrait
framebuffer must be turned, what the render-thread readback costs on the
PowerVR and how fast the pure-Go encoder is on the A53 were all estimated.
**Where.** `castcap.go` (UI capture, encoder, supervisor), `castcap_fb.go`
(framebuffer source, ffmpeg or JukaHub's reader; log tag `cast`: "sharing the
game from /dev/fb0 …"), `companion_cast.go` (viewer page, `/status`),
`castui.go`; `timelapse.go` shares the framebuffer assumption.
**Done when.** On a Smart Pro and a Brick, stock and CrossMix: Smooth to a
laptop browser keeps the UI at its normal frame rate in `framestats` while
watched; a RetroArch game started with a viewer connected streams live through
ffmpeg — not black, not sideways (else record the right `castRotate` per
device and make it the default) — and again with ffmpeg removed through
JukaHub's reader, with CPU and fps noted; a game whose picture stays black
brings up the `/status` note (then `/dev/fb0` does not carry GLES frames on
that image and the time-lapse is broken the same way); the Brick's 1024×768
panel under its 960×720 canvas keeps its aspect; a Samsung (Tizen) or LG
(webOS) TV browser shows the stream or its single-frame fallback, and `&tv=1`
fills the screen; **Y** cuts the old link off within a second; the LIVE pill
shows on the handheld and in neither the stream nor a screenshot.
**Verify.** `framestats` with and without a viewer, `top` during a shared game,
`logs.txt`, a photo of the TV.

### SYS-19 — Encrypted cloud save backups through WebDAV (P2, M) [NET]
**Why.** Save Sync can copy saves between paired JukaHubs or export an encrypted
`.jukasaves` file, but a broken or lost device still depends on someone having
made and carried that export elsewhere.
**Where.** `ssync.go`, `ssyncui.go`, the secret store, and
`docs/SAVE_MANAGER.md`.
**Done when.** Save Sync can add a user-selected HTTPS WebDAV target (including
Nextcloud), upload versioned client-encrypted save snapshots, and list and
restore them through the existing import preview; the WebDAV app password stays
in the secret store, TLS certificates are validated, interrupted uploads leave
the previous snapshot usable, and restoring always takes a Save Manager snapshot
first. No save or passphrase is uploaded in plaintext.

### SYS-20 — Resolve save-sync conflicts from the comparison card (P2, M)
**Why.** `ssyncDecide` safely keeps both versions when both devices changed a save,
but it selects one as the filename the emulator will load; the user has to
inspect and rename files manually to choose the active version.
**Where.** `ssync.go` (`ssyncDecide`, `ssyncConflictName`), `ssyncui.go`, and
`docs/SAVE_MANAGER.md`.
**Done when.** A comparison with conflicts can pause for a per-file choice to
use this device's copy, the other device's copy, or leave the conflict
unresolved; applying a choice snapshots both devices first, sets the selected
version as the active filename on both, keeps the other version under its
conflict name, and never advances the sync base for an unresolved file.

---

## Sound and haptics (SND)

### SND-1 — Chime listening test and level (P1, S) [HW]
**Where.** `bootchime.go`, `branding/sound/` (C twin + `analyze.py`).
**Done when.** On both devices the chimes are comfortable at the default UI
sound level, have no clicks, and do not delay boot (first sound within ~100 ms
of the splash). Adjust `chimeStartGain`/`chimeStopGain` in both the Go file and
the C twin together.

### SND-2 — One audio mixer device (P2, M)
**Why.** UI sounds (mono), chimes (stereo) and video (stereo) each open their own
SDL device; three ALSA streams on a 1 GB handheld cost memory and wakeups.
**Where.** `sound.go`, `bootchime.go`, `alarmtone.go`, `trophy_sound.go`.
**Done when.** One stereo device with a small Go mixer serves all sources, with
the LED analyser fed from the mix.

### SND-3 — Sound packs (P3, M)
**Done when.** Chime and UI-sound variants install as a package category
(scores + constants as data, not samples) and are selectable in Settings.

### SND-4 — A haptic tap on the chime's last bell (P3, S)
**Done when.** When vibration is on, the startup chime's final bell fires a soft
pulse through `playHaptic`.

### SND-5 — The tg5040 mixer, checked on a device (P1, S) [HW]
**Why.** Volume on the Smart Pro and the Brick now drives the codec's "digital
volume" (an attenuation: 0 is loudest, 63 quietest), mutes through "DAC
Volume" and keeps `/mnt/UDISK/system.json` "vol" in step (`volume_trimui.go`).
The control names, the curve (raw ≈ −43·log10(pct/100)) and the interplay with
the firmware's keymon come from MinUI and NextUI reports, not from a device.
**Where.** `volume_trimui.go` (`trimuiAttenuationFor`; the probe logs the
controls it found once, tag `volume`), `systemcontrol.go`, `volumectl.go`,
`alarmvol.go`.
**Done when.** On both devices 100% is the loudest and 0% is silent, the steps
feel even, un-mute restores the level, the hardware keys carry on from a level
set in JukaHub (or the drift is written down), and the alarm's ramp leaves
`system.json` alone.

### SND-6 — Follow the firmware's own volume changes (P2, M) [HW]
**Why.** The stock keymon owns the hardware keys and may re-apply its own level
— for example when headphones are plugged in, with a separate "hpvol" in
`system.json` — over a level JukaHub set. And a USB DAC present at boot becomes
ALSA card 0 (NextUI issue #865), so the codec's controls are not where `amixer`
looks.
**Where.** `volume_trimui.go` (probe and poll), `audiooutput.go` (the output
re-probe already sees headphones and Bluetooth), `volumectl.go`.
**Done when.** The volume card follows what keymon did after a headphone plug
or unplug (reading back, never fighting it), the probe finds the codec's card
by name instead of assuming card 0, and a USB or Bluetooth output gets the
generic mixer path or an honest "volume is set on the device" note.

### SND-7 — Spatial audio listening test (P1, S) [HW]
**Why.** The virtual surround is a parametric model (a spherical head, four
virtual loudspeakers, a small room) checked against its reference by numbers,
never by ear; the defaults were chosen on paper.
**Where.** `player/spatialfx/jh_spatial_ref.py` (the spec: constants change
there only, then `player/testdata/spatial` is regenerated and both ports
follow), `spatial_dsp.go`, `spatialfx/jukahub_spatial.c`,
`docs/SPATIAL_AUDIO.md`.
**Done when.** On a Smart Pro and a Brick, with wired headphones and one
Bluetooth headset: the three rooms at 50, 75 and 100% and the Speakers mode on
the built-in pair are heard with music, a spoken-word track and a game; the
centre image, the width behind you and the loudness match are judged; the CPU
cost while a video plays is measured; the defaults are confirmed or tuned; and
the notes go into `docs/SPATIAL_AUDIO.md`.

### SND-8 — Spatial audio in RetroArch games, on a device (P1, S) [HW]
**Why.** Games get the effect through a RetroArch audio DSP filter that JukaHub
ships. Whether the firmware's RetroArch (`ra64.trimui`) and CrossMix's builds
load filter libraries is unknown.
**Where.** `spatial_games.go` (the probe and its cache), `player/spatialfx`,
RetroArch's log (`[DSP]` lines).
**Done when.** On a Smart Pro the probe's verdict for the firmware's RetroArch
is recorded; with **Spatial audio in games** on, RetroArch's log shows the
filter loading and the effect is heard; a demanding core (N64, PSP) keeps full
speed with it on; and `retroarch.cfg` has the user's own value back after the
game.

### SND-9 — The game plugin's interfaces against the real headers (P1, S) [NET]
**Why.** The RetroArch DSP-filter and LADSPA declarations in
`player/spatialfx` were written from memory, without the upstream headers at
hand.
**Where.** `player/spatialfx/jukahub_spatial.h`, `jukahub_spatial.c`,
`README.md`; libretro-common's `libretro_dspfilter.h` and `dsp_filter.c`;
`ladspa.h`.
**Done when.** Every struct, typedef, flag and entry point matches the headers
(or is fixed) and the README says so; then DSP API v2 (RetroArch's int16
`process_i16` path) is decided.

### SND-10 — Discrete 5.1 and 7.1 sources (P3, M)
**Why.** Media reaches the spatializer already folded to stereo by ffmpeg
(`-ac 2`), so a film's surround channels come back only as far as the stereo
difference carries them.
**Where.** `videoplayer.go` (`startFFmpegAudio`), `audioresume.go`,
`spatial_dsp.go`, `jh_spatial_ref.py`.
**Done when.** With spatial audio on, a 5.1 or 7.1 source is decoded with its
own channels, each plays from its own virtual loudspeaker (centre ahead,
surrounds behind, LFE to both ears), the decoders' frame arithmetic follows the
channel count, and the spec has golden vectors for the multichannel path.

### SND-11 — The game filter on Windows (P3, S) [WIN]
**Why.** `player/spatialfx/build.sh` can build a Windows DLL with MinGW, but no
Windows package carries one, so **Spatial audio in games** says it is not
available there.
**Where.** `player/spatialfx/build.sh` (`TARGET=windows`), the Windows release
job, `spatial_games.go` (where it looks).
**Done when.** The Windows archive carries the DLL where `spatial_games.go`
looks, and a RetroArch game started from the library on Windows plays through
it.

### SND-13 — Spatial audio for standalone emulators (P3, M) [HW]
**Why.** Only RetroArch has a filter system JukaHub can use per launch. The same
library is also a LADSPA plugin, which ALSA can put in front of the device
(`pcm.!default` through a `ladspa` PCM, set per child with `ALSA_CONFIG_PATH`
listing the stock `alsa.conf` first), but nothing shows that the firmware's
alsa-lib has the `ladspa` PCM or that games open `default`.
**Where.** `spatial_games.go`, the launch paths' child environment
(`game_library.go`, `childenv.go`), `player/spatialfx`.
**Done when.** A probe (a child that opens SDL audio with that environment)
proves the route on the device; only then does an opt-in switch, off by
default, send standalone emulators through the plugin; where the probe fails,
the row says why.

---

## Web builder (WEB)

### WEB-1 — Split `script.js` into modules (P1, L)
**Why.** ~330 KB single file: the largest single-file risk in the repo.
**Done when.** ES modules by concern (canvas, inspector, scenes, export/import,
validation, gallery, AI panel), the CSP still holds (`_headers`), and the jsdom
+ Playwright suites pass unchanged.

### WEB-2 — PWA with stale-cache recovery (P3, M)
**Done when.** A service worker with versioned cache and a "new version" prompt;
favicon/icons meet install minimums (branding assets exist).

### WEB-3 — Focus-order editor (P2, M)
**Done when.** The builder shows and edits D-pad order per scene, exported in a
form `focus.go` reads.

### WEB-4 — Reusable element components (P3, M)
**Done when.** Element markup comes from one component factory instead of per
type assembly.

---

## Security and privacy (SEC)

### SEC-1 — One extraction path through `os.Root` (P2, M)
**Why.** hakuX and the Packages store (`pkgstage.go`, `pkgExtractZip`) extract
through `os.Root`; the other installers still guard paths by hand.
**Where.** `tools.go`, `windows_runtime_install.go`, `emu7z.go`,
`patch_engine.go` (`extractZipSafe`/`extractTarGzSafe`, used by `patch_flow.go`
and `ports.go`), `upch*.go`, `xpack*.go`.
**Done when.** Every archive extraction writes through an `os.Root` opened on its
destination (reusing `pkgExtractZip`'s refusals); traversal tests stay green.

### SEC-2 — Secrets audit after the November pass (P2, S)
**Done when.** No token, PIN or pairing code is written to `errors.txt`, the Logs
ring or a diagnostics bundle (grep the new `log*` call sites for secrets), and
`.livebuilder-code` stays untracked.

### SEC-6 — Plugin permissions and signing (P1, L) [OWNER]
**Why.** Plugin scripts run with JukaHub's privileges, unsigned
(`plugins.go` `plugExecute`, `docs/PLUGINS.md:8`).
**Done when.** Manifests declare permissions (network, files outside the
plugin folder, run programs, settings); the install sheet lists them; scripts run
with a minimal environment, a working directory inside the plugin, a timeout and
(on Linux) a dropped-privilege user if the image allows; catalogue plugins are
signed and sideloaded ones say so.

### SEC-8 — Secrets that are actually secret (P1, M)
**Why.** The default key is a public constant (`crypto.go:30-37`), so the secret
store is obfuscation; `crypto.go:105` logs the first 8 hex characters of
`JUKAHUB_CRYPTO_KEY`; the RetroAchievements key is plain JSON; Discord login
accepts user tokens (`chat.go:930`, against Discord's terms).
**Done when.** The key is device-bound (Windows DPAPI; TrimUI: random key in a
`0600` file outside the user-visible tree, created on first run), existing
secrets are re-encrypted, the log line is removed, RA and every API key go
through the store, and Discord uses bot or OAuth tokens only.

### SEC-10 — Supply chain in CI (P2, S) [NET]
**Done when.** govulncheck is blocking for reachable vulnerabilities, CodeQL runs
on Go and JavaScript (`.github/codeql/` is empty), the container image and SDL
tarballs are covered by dependabot or a pinned-digest check.

### SEC-11 — A privacy page that tells everything (P3, S)
**Done when.** Settings → Privacy lists every network feature, what it sends,
where, and whether it is on; "Export my data" writes a zip of every user file.

### SEC-14 — Legacy terminal_run / emulator_run triggers (P2, S) [OWNER]
**Why.** `runTerminal` (`morefeatures.go` ~:800) runs the `terminal_cmd` /
`emulator_cmd` custom variables through `cmd /c` / `sh -c` from config triggers
with no timeout. Its trust comment is in, and the Live Builder holds pushed
designs that use these triggers for the owner's approval, but a command that
hangs blocks until JukaHub exits.
**Where.** `morefeatures.go` (`runTerminal`), `core.go` (`terminal_run`,
`emulator_run`), `powertools_backend.go` (`runPowerScript`, the pattern).
**Done when.** Either the triggers are removed (the Terminal scene replaces them)
or `runTerminal` gets a timeout and a process-group kill like `runPowerScript`.

### SEC-15 — The update swap helper survives special characters in the install path (P3, S) [WIN]
**Why.** `patch_swap.go` (~:287) starts the generated `apply-update.bat`
directly; Windows runs it through cmd.exe with Go's argument quoting, which
breaks for an install folder with `&` or parentheses plus spaces.
**Where.** `patch_swap.go`, `winbatch.go`.
**Done when.** The helper starts through `winBatchCommand`, and a test pins its
command line.

### SEC-16 — Signed package catalogue, hashes mandatory (P1, M) [OWNER] [NET]
**Why.** The store checks a `sha256` when the jukaLang/Packages registry publishes
one, but the registry publishes none yet and is not signed, so every store
install is "Not verified" and whoever controls the registry controls the hashes.
`patch_signed.go` signs only the Patch repository's own index format, with the
development key (REL-3 owns the production key).
**Where.** jukaLang/Packages (`packages.json`), `player/pkgverify.go`
(`pkgApplyIntegrity`), `player/pkgstate.go` (`pkgCatalogFetcher`),
`player/patch_signed.go`, `docs/PACKAGES.md`.
**Done when.** The registry's CI writes `sha256` and the exact `size` for every
entry and a detached Ed25519 signature (`packages.json.sig`, canonical bytes, key
id) with the production key from REL-3; the store verifies the signature when
fetching the list (offline keeps the last verified list); once every entry has a
hash, an entry without one is refused and the "Not verified" path is removed; a
test feeds a tampered list and a missing hash.

### SEC-17 — FTP's read-only set follows what actually runs (P3, S)
**Why.** Over FTP only `Roms/Ports` and game-profile folders are read-only; a
system whose launcher executes the ROM itself (a scripts or apps "system" on some
firmwares), a symlink already inside a share that points into Ports (ext4 cards;
FAT has none) and a profile added while FTP runs (covered from the next start)
are not.
**Where.** `ftp_server.go` (`ftpPath.protected`, `ftpProfileDirs`), `ports.go`,
`gameprofiles.go`, `emulators.go`.
**Done when.** The locked set is derived from the launchers that execute their
argument and refreshed when the profile cache rebuilds, and writes through
symlinks inside shares are refused; a test covers each.

### SEC-18 — yt-dlp options from an allowlist, not a blocklist (P1, S)
**Why.** `rejectYtDlpExecFlags` (`core.go`, `ytDlpExecFlags`) blocks a fixed
list, but yt-dlp accepts unique prefixes of long options (`--exec-before-d`),
`--alias`, `--netrc-cmd`, `--config-locations`, and `-o` to any path for a
direct link. A design's `yt_search` target is that command line; the Live
Builder holds such designs for approval, but experience packs (`xpackscene.go`
allows `yt_search`) and hand-edited configs still reach it.
**Where.** `core.go` (`ytDlpExecFlags`, `rejectYtDlpExecFlags`,
`planMediaSearchTrigger`, the yt-dlp exec path), `xpackscene.go`
(`xpackSceneTriggers`).
**Done when.** A config-supplied yt-dlp command may only use options from an
allowlist (exact long names, no prefixes or aliases), packs cannot carry a
`yt_search` command, and a table test covers the prefix, alias, netrc,
config-locations and `-o` variants.

### SEC-19 — The pairing code never crosses the network in clear (P2, M)
**Why.** The Live Builder is plain HTTP: someone on the same Wi-Fi who sees one
request learns the code. Risky designs still wait for the owner, but an ordinary
layout push or a (redacted) pull would go through.
**Where.** `livebuilder.go`, `builder-device.js`.
**Done when.** Requests are authenticated with an HMAC of the method, path, body
hash and a timestamp/nonce keyed by the code (or TLS with a pinned self-signed
certificate shown as a QR), the code itself is never sent, replays are refused,
and tests cover a replay and a stale timestamp.

### SEC-20 — Labels cannot show protected settings (P3, S)
**Why.** `substituteVars` (`core.go`) puts any `$name` into a label, so a design
can print `$discord_token` on screen, in the companion's screen view or in a
screenshot.
**Where.** `core.go` (`substituteVars`), `livebuilder.go`
(`liveBuilderSensitiveKey`).
**Done when.** Substitution skips protected names and `ENC:` values, with a
test.
**Status 2026-11-10.** Protected names and encrypted values now remain as their
`$name` placeholder in labels, keeping the stored value out of the UI. The
focused substitution test is still to add.

### SEC-21 — Package swap on Windows and FAT, on the device (P2, S) [HW]
**Why.** The store's crash safety rests on sync/fsync ordering on the handheld and
on per-file flushes plus rename retries on Windows; none of it has run on
hardware.
**Where.** `player/pkgstage.go`, `player/pkgverify.go`, `player/pkgstate.go`.
**Done when.** On a Smart Pro (exFAT), pulling power during "Installing" of an
update ten times leaves exactly one version with the user's files after each
restart; on Windows, updating a package while its folder is open in Explorer and
while the store scans either retries and succeeds or fails with "the installed
version is unchanged"; a card with less free space than the download is refused
before any byte is written; an entry with a wrong `sha256` is refused and a
correct one shows "Verified download".

### SEC-22 — Kept values in lists restore whole or not at all (P3, S)
**Why.** A pull replaces protected values with `<kept on device>` and a push
puts the device's values back (`liveBuilderRestoreKept`), but inside a list it
does so by position, so a reordered or edited list can pick up a different kept
item.
**Where.** `livebuilder.go` (`liveBuilderRestoreKept`).
**Done when.** A list holding a placeholder is restored only when its length
and other items match the device's list (otherwise the push is refused with a
clear message), with a test.

### SEC-23 — Accepting a waiting design keeps today's secrets (P2, S)
**Why.** A held design's kept values were filled in at push time, so applying
it later puts back a token or command the owner changed on the device while it
waited.
**Where.** `livebuilder.go` (`AcceptPending`, `liveBuilderRestoreKept`).
**Done when.** Accept fills the kept fields from the device's current file (or
refuses when one changed since the push), with a test.

### SEC-24 — No yes/no answer about a protected value (P3, S)
**Why.** A push that spells out a guessed protected value is "nothing new" when
it equals the device's value (applied, 200) and held when it does not (202). A
wrong guess puts the approval card up and blocks pushes until the owner
decides, so guessing is slow, but a boolean or short value can be learnt.
**Where.** `livebuilder.go` (`liveBuilderDesignRisks`, the capability keys).
**Done when.** A protected value that a pull would hide counts as a change
unless it comes back as the placeholder (the card then shows the pushed value),
with a test.

### SEC-25 — Mute errors from cross-origin scripts (P3, S)
**Why.** A browser reports an error thrown by a cross-origin classic script that
was loaded without CORS as "Script error." with no file, line or message, so a
page cannot read another site's source or URLs through `window.onerror`. The
Browser's DOM pass hands the page the real message and URL
(`webdom/85_scripts.js`, `scrReportResult`), although `fetchScript` already
marks such a script `opaque` (`webdom_net.go`).
**Where.** `webdom_runtime.go` (script responses), `webdom/85_scripts.js`.
**Done when.** An error from an opaque script reaches page handlers as "Script
error." with an empty filename and zero line and column, the debug log keeps the
real text, and a runtime test shows both.

### SEC-26 — Cookie records keyed like the jar (P2, S)
**Why.** `browserCookieKey` is host + path + name, while the jar keys a cookie by
its domain + path + name: a host-only and a domain cookie of one name, set from
the same host, share one record, so an `HttpOnly` flag can be lost — and with it
the protection page scripts get through `document.cookie` (`webdom_net.go`).
The records also rebuild an IPv6 host without its brackets, which is why
`webJSNetJarURL` exists.
**Where.** `browsercookies.go` (`browserCookieKey`, `trackLocked`,
`browserCookieURL`), `webdom_net.go`.
**Done when.** Records are keyed by domain-or-host + path + name and keep the
request's host spelling, an existing `browser-cookies.json` migrates on load,
`webJSNetJarURL` is gone, and a test sets a host-only and a domain cookie of one
name and keeps both flags.

### SEC-27 — What the archive extraction still trusts (P3, S)
**Why.** Two reviews of the Files extraction (2026-11-10) left these open. The
space check counts declared bytes, not clusters (200 000 small files can take
far more of a 128 KiB-cluster exFAT card), and a member over 4 GiB on a FAT32
card is only refused when the write fails. The 7zr.exe fetch (`emu7z.go`)
renames the download into place before it checks the digest. Windows'
`tar.exe` (RAR without a full 7-Zip) gets no listing first, so no entry cap or
up-front space check. A Windows cloud placeholder of the same name is kept
rather than replaced by Extract here. The vendored LZMA2 decoder accepts chunk
properties with lc + lp > 4, which xz refuses and which make every chunk
allocate a 6 MiB table — slow (minutes per megabyte) though bounded and
cancellable; a one-line check in `lzma/reader2.go` `startChunk`, or a check of
the chunk headers in `files_extract_xz.go`, would close it. An `.xz` made with
BCJ or delta is refused, where 7-Zip (when present) could unpack it.
**Where.** `files_extract.go` (`feExtractBudget`, `feZipPlan`),
`files_extract_xz.go`, `files_extract_tools.go` (`feRunWindowsTar`), `emu7z.go`.
**Done when.** Each is closed, or accepted in SECURITY.md with the reason, with
a test where one can be written.

---

## Docs and project (DOC)

### DOC-1 — `docs/PERFORMANCE.md` (P1, S) [HW]
**Done when.** Created by PERF-1 and kept as the place for every device number.



### DOC-4 — Decisions recorded (P3, S)
**Done when.** ADRs exist for the input vocabulary (`inputhints.go`), the
string catalogue (I18N-1), the plugin sandbox (SEC-6) and the overlay approach
(SHELL-2).

---

## Input and controls (INP)

The hint layer (`inputhints.go`, November 2026) decides whether hints name the
pad's buttons or the keyboard's keys and translates where it can do so
truthfully. These tasks make the rest of the input story match it.

### INP-1 — Keyboard parity, screen by screen (P1, M)
**Why.** XAG 112: every screen must be usable by keyboard *and* controller
alone. The November passes made the browser (Y and / search), the video player
(X/Y), the Settings option sheet, the Tools pages, Playtime, the config
inspector, the image viewer, the PDF reader, game details, Local play, keyboard
languages, Alarm, Chat and Patch match, and `docs/CONTROLS.md` has a "Keyboard
keys, screen by screen" table. Known gaps left: the Terminal's history list and
card show "Start Run" in keyboard words with no key behind it (`terminal_ui.go`
~:667 and the history footer); Core builds' "Start Auto-update" has no key in
`handleCoreBuildsKey`, and "L1/R1 Filter" should read "L1/R1 / [ ]"
(`corebuilds_ui.go`).
**Where.** Every `handle*Key` and its `*FooterHints`; `inputhints.go`
(`hintKeyboardTwin`, the `"pad / key"` badge convention).
**Done when.** For each screen in `docs/CONTROLS.md`, every footer hint is
pressable on a keyboard: either the generic twin is true there or the hint is
written `"<pad> / <key>"`; the table in CONTROLS.md stays in step.
**Verify.** INP-12's checker reports zero mismatches.

### INP-2 — Sentences that name buttons (P1, M)
**Why.** Prose such as "Press Y on any video…" or "A to type" bypasses the hint
layer and lies on a keyboard. `hintSentence`/`hintSentence2` (`inputhints.go`)
write such sentences in the hints' words and follow remaps; 27 sentences in 15
files use them so far. Left (grep
`'"[^"]*\b(Press|press|Hold|hold) [ABXY]\b'`): `memtool.go`,
`files_locations.go`, `files_manager.go`, `fileeditor.go`, `netmon.go`,
`netplay_ui.go`, `partyscene.go`, `auditscene.go`, `bugrep.go`,
`componentreport.go`, `emuwininstall.go`, `emuxbox.go`, `game_library_ui.go`,
`game_random.go`, `musicuifeatures.go`, `musicpeq.go`, `patch.go` (decide what
"Release" means on a keyboard), `patch_engine.go`, `vkui.go` (`vkStripTip`),
`datetime.go`, and the `inmap*.go` Select+R1 prose (a pad-only chord: remap
only, no keyboard words). Deliberately physical: hakuX's exit chord, "Press A on
a controller to join" (hotseat) and the one-handed help text.
**Where.** The files above; `hintSentence` in `inputhints.go`.
**Done when.** Each goes through `hintSentence` or is rewritten without a
button, and the grep lists only the deliberate exceptions.

### INP-3 — Controller glyph sets (P2, M)
**Why.** A DualSense or Switch Pro pad on Windows shows Xbox letters; on a
PlayStation pad "A" is ✕ in the wrong place. SteamOS and Xbox show the glyphs
of the pad in hand.
**Where.** `inputhints.go` (a third vocabulary dimension: pad family), SDL
`GameControllerGetType` (go-sdl2 v0.4.40), `gamepad_hotplug.go`.
**Done when.** Xbox, PlayStation (✕ ○ □ △, L1/R1), Nintendo (B/A swapped
positions, L/R, ZL/ZR) and TrimUI families are named correctly in every hint;
Settings → Button hints gains "Pad style: Auto / Xbox / PlayStation / Nintendo".

### INP-5 — Type on a real keyboard everywhere (P2, M) [HW]
**Why.** With a keyboard plugged into the handheld, Chat (`chatinput.go:212`)
and the terminal (`terminal.go` ~1389) open the on-screen keyboard because the
code asks `P().IsHandheld()`, and search opens it unconditionally
(`features.go:109`).
**Done when.** Text fields accept physical typing whenever a keyboard is
attached (`hintAttachedKeyboardName() != ""`); the on-screen keyboard opens only
from the pad; verified with a USB keyboard on a Smart Pro.

### INP-6 — Verify keyboard detection on hardware (P1, S) [HW]
**Why.** `hintParseInputDevices` follows udev's keyboard rule and skips gpio and
uinput devices, but the firmware's own devices were never seen.
**Done when.** `/proc/bus/input/devices` from a Smart Pro and a Brick (stock
and with PortMaster's key mapper running) is pasted into a test fixture; a USB
keyboard (OTG), a Bluetooth keyboard and a Logitech Unifying receiver are tried;
the `keyboard attached:` log lines are recorded in `docs/CONTROLS.md`. Known
limit: a receiver or gaming mouse that exposes a keyboard interface counts as a
keyboard (the D-pad switches the hints back).

### INP-7 — Mouse and touch parity (P2, M)
**Why.** Many footer actions exist only as buttons (Clear all in the
notification centre is mouse-only; many are pad-only). On Windows the mouse is
the third input.
**Done when.** Every footer hint badge is clickable (generalise `emuHintHits`),
right-click is the screen's secondary action (X), the wheel scrolls every list.

### INP-8 — Keyboard profiles in the control-profile engine (P3, M)
**Where.** `inmap*.go` (profiles map pad buttons only).
**Done when.** A profile can bind keys to actions; the hint layer reads it, so
a remapped key is what the hint shows.

### INP-9 — Controller database and HIDAPI on Windows (P2, S) [WIN]
**Why.** Only one X360 mapping is added (`main.go:534`, `platform:Linux`, so
none on Windows) and no `SDL_HINT_JOYSTICK_HIDAPI*` hints are set. SDL's
defaults and built-in database cover most pads, but an unknown pad is a
"joystick" SDL cannot open as a controller — and then the hints say "keyboard"
(correctly, but the pad is useless).
**Done when.** `gamecontrollerdb.txt` (pinned, embedded) is loaded at boot,
HIDAPI hints enable DualSense/DS4/Switch Pro/8BitDo; a "Controller not
recognised — map it" flow uses the controller test (`ctrldiag.go`) to write a
mapping.

### INP-10 — Stick dead zone and repeat timing (P2, S)
**Where.** `controller.go:513` (fixed UI dead zone), `navrepeat.go`
(`a11yRepeatMs`: Normal or Slow).
**Done when.** Settings → Controls has a dead-zone slider (with the live
calibration view) and repeat delay/rate steps; drifting sticks stop moving focus.

### INP-11 — Players and pad status (P3, M)
**Done when.** Each connected pad shows its player number (LED where the pad has
one), battery level (`SDL_JoystickCurrentPowerLevel`) in Quick Settings, and a
"pad battery low" toast; hotseat reuses the assignment.

### INP-12 — Hint truth checker (P2, M) [WIN]
**Why.** INP-1 regresses silently unless something checks it.
**Where.** A `tools/hintcheck` program (or a scenedump mode, TEST-1) that, for
each scene and state, renders the footer in both vocabularies and replays each
hint's key through the scene's key handler with a recording stub.
**Done when.** It runs in CI as a non-blocking job and lists mismatches.

### INP-13 — A key that opens a text field types its own letter (P2, S)
**Why.** SDL sends a key's text right after the key, so a printable key that
opens the keyboard is also typed into it: X on Live TV (`tvchannels.go` →
`vkOpenTVSearch`), X and / on Media (`mediaHubOpenSearch`) and similar shortcuts
start the query with "x" or "/". The browser drops that one event
(`browserDropKeyText`) and ignores the opener's repeats
(`browserTextOpenerRepeat`); Home, Notes and Alarm use per-screen `skipText`
fields. A shared helper exists since 2026-11-09 — `vkDropOpenerText`
(`vkopenertext.go`) — and the Audio panel, Podcasts, PS Remote Play, LAN play
and Downloads use it; the screens named above do not yet.
**Where.** `browser.go` (`browserDropKeyText`, `browserTextOpener`), `core.go`
(`runInputModal`), `tvchannels.go`, `media_hub_input.go`, `globalsearch.go`,
`notes.go`, `alarm.go`, `main.go` (`handleTextInputEvent` → `vkInsertText`).
**Done when.** One shared helper next to the hint layer is called by every
printable key that opens a text field, the main-loop keyboard honours the
opener's repeats too, the per-screen `skipText` copies are retired where it
covers them, and a keyboard check of each listed screen starts with an empty
field.

### INP-14 — A held key acts once unless holding means repeat (P2, M)
**Why.** SDL auto-repeats keyboard keys, but no pad face button or shoulder
repeats (`navrepeat.go` repeats the D-pad only), and most key handlers ignore
`e.Repeat`: a held key repeats toggles, deletes and open/close actions the pad
does once. Fixed so far: alarm Delete/X, Local play Y/Delete, keyboard languages
X/Delete, video C/X, S/Y and F, the browser's openers. Not audited: Enter/Space
on lists that open or toggle, Esc stepping back several screens, the video
settings sheet's Enter, the browser's X/L link panel.
**Where.** Each scene module's `handleKey`; the key dispatch in `main.go`;
`shortcuts.go` (already Repeat-aware).
**Done when.** A central rule in the key dispatch (repeats reach a scene only for
arrows, PgUp/PgDn, Home/End, Backspace/Delete in text fields, volume and zoom
keys) or per-handler checks, and a held key acts once on every screen.

### INP-15 — The firmware button swap for the built-in pad only (P3, S) [HW]
**Why.** `applyButtonSwap` (`core.go`) flips A/B and, since 2026-11-09, X/Y for
every pad on a handheld, so an external Xbox-layout pad on a TrimUI (Local play
opens extra pads, `hotseat.go`) comes out mirrored on both pairs.
**Where.** `core.go` (`applyButtonSwap`, `padFaceButton`), the polling helpers
that mirror it (`inmapPhysicallyHeld`, `jlandPadHeld`, `vkLogicalHeld`), and
the synthetic presses (`padRepeatWhich`, `inmapSynthWhich`, the phone remote's
pushes, which use `Which` 0 — also the first real pad's instance).
**Done when.** The swaps apply to the firmware pad ("TRIMUI Player1",
045e:028e) and to synthetic presses only, the polling helpers match per pad,
and an external pad on a Smart Pro reports its printed letters.

### INP-16 — Undo the old X/Y workarounds once (P3, S)
**Why.** Until 2026-11-09 a TrimUI's X and Y arrived swapped; a user who fixed
that with Accessibility (the Button layout or an X↔Y remap) or a control
profile now has them swapped again. The CHANGELOG says to set them back, which
few will read.
**Where.** `extras.go` (`migrateHandheldButtonMapping` and its marker key are
the pattern), `a11y.go` (`a11y_swap_xy`, the per-button keymap), the control
profiles (`inmap*.go`).
**Done when.** On a handheld, the first start after the update offers (not
forces) to clear an X/Y swap or an X↔Y remap it finds, with a marker so it
asks once.

---

### INP-17 — Mouse mode, second round (P2, S) [HW]
**Why.** Mouse mode (hold Select + L1) and the games mouse (vcursor_bridge_linux.go)
shipped with known gaps.
**Where.** vcursor_mode.go, vcursor_bridge_linux.go, inputhints.go, the footer.
**Done when.** The footer says the D-pad moves the pointer while mouse mode is on;
the chord works from a second pad; the pointer hides over full-screen video in
mouse mode; the games mouse honours the Mouse mode switch, offers the right
stick as a wheel and a speed setting, and — if HW-2 step 6 shows SDL apps miss a
mouse that appears mid-run — is created idle when the child starts or handed to
it through `SDL_EVDEV_DEVICES`; an LED colour while it is on (ledpanel).
**Verify.** Unit tests for the state machine; HW-2 step 6.

### INP-18 — Voice command in more languages (P2, M)
**Why.** Voice command understands English only: its phrase tables are English
and it asks Whisper for English.
**Where.** `voicecmd_intent.go` (phrase tables), `voicecmd.go` (the
transcription request), `voicecmd_intent_test.go`; the keyboard layout's
language (`vkCurrentLayout().Lang`) or the UI language as the source.
**Done when.** At least one more language has its phrase table and tests, is
picked from the layout or the UI language, and English stays the fallback.

### INP-19 — Offline voice recognition on Windows (P3, M) [WIN]
**Why.** The microphone path needs a Groq key and internet. Many Windows PCs
have an offline recogniser (System.Speech) that understands a fixed grammar —
which is what a list of command phrases and names is. No recogniser small
enough for the handheld's 1 GB is known.
**Where.** `voicecmd.go` (a second recogniser in front of the same intent
parser), a PowerShell/System.Speech helper probed with
`InstalledRecognizers()`.
**Done when.** On a Windows PC with a recogniser installed, Voice command works
offline with a grammar of the current phrases and names; without one it says
so and keeps the Groq path.

### INP-20 — Voice on the handheld's microphone: the hardware proof (P2, S) [HW]
**Why.** The voice orb, the Voice command page and voice typing share one
microphone now (`voicemic.go`): SDL's default device, then the kernel's capture
PCMs through `plughw`, SDL's own list and `arecord`, a 1.2 s silence check that
moves on, and the tg5040 codec's microphone switches ("ADCL Input MIC1 Boost
Switch", "ADCR Input MIC2 Boost Switch") turned on before the first recording.
All of it comes from the Allwinner driver's control names, not from a Smart Pro
on a desk, and the orb's adaptive end-of-speech thresholds (`orbVAD`) are
guesses until a real microphone is measured; the page still uses fixed ones.
**Where.** `voicemic.go` (candidates, mixer, channel choice), `voiceorb.go`
(`orbVAD`, `orbSilencePeak`), `voicecmd.go` (the page's fixed levels).
**Done when.** On a Smart Pro and a Brick the log's `mic` lines say which device
answered and which switches were set; a spoken command is captured and a clip
ends about a second after the speech without cutting words; the thresholds
come from measured levels and the page uses `orbVAD` too; if the stock firmware
has no capture path at all, the orb says so and the docs say which firmware
does.

### INP-22 — The voice orb from a pad on Windows (P3, S) [WIN]
**Why.** On a PC the orb's key is F4; a couch player with only a pad has no
voice button. The handheld's Menu is GUIDE, but on Windows the Xbox button
opens Game Bar first, and a PlayStation pad's PS button is GUIDE too.
**Where.** `voiceorb.go` (`voiceOrbMenuOwnsGuide`), Settings → Controls (the
"Menu button: voice" row is handheld-only).
**Done when.** An opt-in switch on Windows makes the pad's Guide (or a long
press of Back) open the orb, with the Game Bar caveat in its second line and
`docs/CONTROLS.md`.
---

## Shell, multitasking and notifications (SHELL)

### SHELL-1 — Game switcher (P1, L)
**Why.** NextUI, Onion and nx-redux made the switcher the handheld's signature
feature: the last games as big cards with their save-state screenshot, resume
in one press. JukaHub has the parts (recents in `game_library.go`, state PNGs in
`gmeta_states.go`, the Continue card) but no switcher.
**Where.** New `switcher.go`; `home_continue.go`, `gmeta_states.go`,
`savestate_resume.go`; docs/CHECKPOINTS.md for the long-term design (GAME-3).
**Done when.** Select (short press) on Home or Menu+Select anywhere in JukaHub
opens a horizontal carousel of the last 10 games with their latest state
thumbnail, playtime and "Resume / Start fresh / Remove"; resume launches with
the auto state; only resumable games are listed by default.
**Verify.** [HW] three RetroArch games resume from the switcher on a Smart Pro.

### SHELL-2 — A quick menu over running games (P1, XL) [HW] [OWNER]
**Why.** SteamOS's Quick Access Menu, TrimUI's own Menu+Select board and muOS
2601's injected overlays all put brightness, volume, battery, save/quit and
performance over a game. The "Known impossibles" list said drawing over an
emulator cannot be done; muOS shows that an `LD_PRELOAD` hook on SDL/GLES swap
can draw it, and a hotkey daemon can pause the emulator (`SIGSTOP`) and show
JukaHub instead.
**Where.** A small C hook library next to `androidrt/` (swap-buffers hook,
own GLES drawing), a hotkey reader on evdev (`volumebuttons.go` pattern),
`gamesuspend.go`, `alarm_proc_unix.go` (SIGSTOP precedent).
**Done when.** Phase 1 (no injection): Menu+Select while a game runs stops the
emulator, JukaHub draws a quick panel (brightness, volume, save state, load
state, performance profile, quit game) and resumes it. Phase 2 (injection): a
volume/brightness/low-battery indicator drawn into RetroArch and one standalone
emulator. Each phase is its own task before code.

### SHELL-3 — Notifications that can be acted on and survive (P1, M)
**Why.** The centre (`notifications.go`) keeps 50 entries in memory, has no
actions, loses everything on restart, toasts replace each other, and "Clear all"
is mouse-only.
**Done when.** Entries persist (bounded file), carry a source and an optional
action ("Install update", "Open download", "Retry"), group by source, A runs the
action and X dismisses, Y clears all; toasts queue by priority instead of
replacing; quiet hours and "while playing" are settings (extends UX-11).

### SHELL-5 — Commands in global search (P2, M)
**Done when.** Search understands verbs: "wifi off", "brightness 40",
"screenshot", "sleep", "update", "new note"; results show the current value;
typos within one edit still match (no fuzzy library, a bounded Damerau check).

### SHELL-6 — Back and focus that remember (P2, M)
**Why.** Leaving a screen and coming back resets focus in places; the back
stack (`sceneHistory`) restores the screen but not where the user was.
**Done when.** Each scene saves focus + scroll on leave and restores it on
return (bounded map); Home restores the last focused card after a game.

### SHELL-7 — Recent screens switcher (P3, S)
**Done when.** Holding Start (or Alt+Tab on a keyboard) shows the last five
screens as cards; release opens the highlighted one.

### SHELL-8 — Placeable widgets (P2, L)
**Why.** Promoted from the parking lot. `widgets.go` holds behaviours, not
widgets; Home's slots are fixed.
**Done when.** Home layouts expose widget slots; built-in widgets: clock, next
alarm, battery, now playing, weather, today's playtime, storage; a community
widget is a sandboxed JSON descriptor (no code), per the parking-lot rule.
**Progress (2026-11-10).** Done, unbuilt: Home's **Widgets** layout is the
slots, a six-column grid of tiles the user moves, resizes, adds and removes in
Edit Home (`home_edit.go`, `home_tiles.go`; stored in `homeTiles`), with
clock, weather, battery, storage, now playing, calendar and today's playtime
widgets (`home_widgetdraw.go`); the new **Carousel** layout reads the same
tile list. Left: the next alarm is folded into the clock widget (its wide
sizes show it) instead of being a widget of its own, and community widgets as
sandboxed JSON descriptors are not done.

### SHELL-9 — Tips and an on-device manual (P2, S)
**Why.** No tips exist; the manual lives on GitHub. `sectrustdoc.go` already
renders SECURITY.md in-app.
**Done when.** Settings → Help renders `docs/QUICK_START.md` and
`docs/CONTROLS.md` offline; Home shows one dismissible tip a day drawn from a
bounded list (never the same twice in a row).

### SHELL-10 — Clock faces and a real standby screen (P3, M)
**Done when.** The screensaver and dock offer three clock faces (analogue,
digital, minimal) with OLED-safe pixel shift; the choice is a theme-able token.

---

## Power, sleep and battery (PWR)

### PWR-1 — Sleep on the power button (P0, L) [HW]
**Why.** JukaHub never sleeps the device: the power key quits it, idle only
dims, and deep sleep exists only in the 3% guard (`lowbattery.go`
`suspendDevice`). NextUI's instant-on deep sleep is the bar.
**Where.** evdev reader for KEY_POWER (`volumebuttons.go` pattern),
`lowbattery.go`, `gamesuspend.go`, `saver_power.go`, `backgroundwork.go`.
**Done when.** A short press sleeps: a running game is stopped (`SIGSTOP`),
audio paused, backlight off, Wi-Fi/BT per setting, then `mem` (fallback
`freeze`) via `/sys/power/state`; any key wakes; the game resumes; the clock,
Wi-Fi and battery refresh (SYS-7); a long press keeps today's quit. Idle →
sleep after N minutes is a setting.
**Verify.** 20 sleep/wake cycles in a game and in a video, battery drop
overnight recorded in `docs/PERFORMANCE.md`.

### PWR-2 — Backlight-off screen off (P1, S) [HW]
**Why.** "Screen off" (ambient dock, music) draws a black frame and leaves the
backlight on (`ambient.go` `ambientDarken`).
**Done when.** Screen off writes `bl_power`/brightness 0 on
`/sys/class/backlight/*` and restores it on wake; the panel is really dark.

### PWR-3 — Save every game before the battery dies (P1, M) [HW]
**Why.** FIX-13. The 3% guard saves RetroArch only.
**Done when.** Each launcher family has a "save and stop" action (RetroArch
UDP, PPSSPP's save-on-exit, DraStic's SIGTERM handling, hakuX/AndroidRT/ports:
`SIGSTOP` + sync, at least never killed mid-write); the guard runs it with a
grace period, then sleeps (PWR-1) rather than powering off.

### PWR-4 — Charging and battery history (P1, M)
**Why.** Since 2026-11-09 the gauge shows a bolt while a charger is connected
(`statusbar_status.go`, the charger input in `widgets.go`), but there is no
drain history or time-left estimate, and battery health is a diagnostics line.
**Done when.** The gauge also marks "Full"; Settings →
Battery shows a 48-hour graph (sampled every 5 minutes into a bounded ring on
disk), time left at the current drain, health and cycle count.

### PWR-5 — Charge limit (P3, S) [HW]
**Done when.** If the PMIC exposes `charge_control_end_threshold` (or an AXP
equivalent), Settings offers "Stop charging at 80%"; otherwise the probe result
is recorded in `docs/DEVICE_TUNING.md` and the task closes.

### PWR-6 — Profiles that follow the situation (P2, M)
**Why.** Per-game CPU caps apply only to launches from the library
(`emulators.go:870`, `ports.go:468`, `netplay.go:509` use the global profile);
nothing switches on the charger (SteamOS issue #3034 asks for the same).
**Done when.** Every launch path goes through one `launchPowerProfile(target)`;
profiles exist per game, per system and per power source (battery / charger),
most specific wins; the game options sheet shows which one applies.

### PWR-8 — Low-battery warning visible in games (P2, S) [HW]
**Why.** The 5% toast is drawn by JukaHub, which is not on screen during a game.
**Done when.** The LED pulses red (`ledsync.go`) and, with SHELL-2 phase 2, an
overlay icon appears; the toast waits for the return.

### PWR-9 — Battery estimates per activity (P3, M)
**Why.** `battery_eta.go` learns one drain rate for everything, while the menu,
an emulator, video and a Remote Play stream drain very differently; and
JukaHub does not run during a stream, so the estimate starts over when it
comes back.
**Where.** `battery_eta.go` (the learned rate in `data/battery_eta.json`),
`native/stream_sys.c` (the helper's status file), `psremote_lastrun.go`.
**Done when.** The learned rate is kept per activity and the estimate uses the
one that is running; the helper's status file carries the battery level at the
start and the end of a stream, and JukaHub learns the stream's rate from it.

---

## Reliability, updates and recovery (REL)

The boot guard counts starts and keeps the last good settings (`bootguard.go`);
REL-10 proves it on a device, and these build on it.

### REL-3 — Signed updates only (P1, M) [OWNER]
**Why.** A release without `release.json` installs on the GitHub checksum alone
(`autoupdate.go:690`); the only embedded key is the development key
(`patch_signed.go:24-31`); no workflow publishes `release.json` today, so every
update is checked by the GitHub digest alone; TUF is a design note
(`docs/UPDATER_TUF.md`).
**Done when.** CI signs every release manifest with a production key held as a
secret (ceremony in `docs/UPDATER_TUF.md`), the app embeds that key plus one
rotation slot, and an unsigned or expired manifest is refused with a clear
message (sideload keeps its explicit trust prompt).

### REL-4 — Update channels (P2, S)
**Done when.** Settings → Updates offers Stable / Beta; Beta follows
prereleases; the channel shows in About and in bug reports.

### REL-5 — Health-checked installs (P2, M)
**Why.** `patch_swap.go` copies files with a journal; a new binary that starts
but cannot draw is rolled back only after three failed starts (`bootguard.go`).
**Where.** `patch_swap.go`, `bootguard.go` (`bootGuardDecide` already counts a
build's boots and marks healthy starts: first frame plus 90 s).
**Done when.** After an update the first boot must reach "first frame + input
handled" within 20 s and write a health mark; otherwise the next boot rolls
back automatically and says so.

### REL-6 — Factory reset (P2, S)
**Done when.** Settings → System → Reset offers "Reset settings" (keeps games,
saves, media) and "Erase everything JukaHub created"; both make a backup first
(`bkup.go`) and ask twice.

### REL-7 — JukaHub as the default shell (P1, M) [HW]
**Why.** The autostart hook (`autostart.go`; on by itself from a TrimUI's first
start since 2026-11-09, `autostart_firstrun.go`) queues JukaHub in the
firmware's `/tmp/cmd_to_run.sh` and closes MainUI once it is up (since
2026-11-10; the first hook never started JukaHub, see CHANGELOG), so MainUI
still shows for about two seconds where the loop opens it before reading the
queue. A start that hangs makes the next boot stay on stock once
(`.autostart-pending`), but a crash simply hands back to MainUI; the
desktop `.cmd`/`.desktop` do not set the working directory although
`main.go:83` reads `jukaconfig.json` from it. Device check owed: read
`Apps/JukaHub/autostart.log` on stock 1.1.x and on CrossMix (which path ran —
queue before the menu, after closing it, or the direct fallback).
**Done when.** "Start JukaHub at boot" launches before MainUI, a crash relaunches
JukaHub once and then falls back to stock with a note, the chord to reach stock
is documented; desktop autostart sets the working directory (or the config path
is resolved from the executable).

### REL-8 — One release pipeline (P1, M) [NET]
**Why.** FIX-14; builds are not reproducible (date stamps, no `-trimpath`,
unpinned mingw/brew), no SBOM or provenance.
**Done when.** One `release.yml` builds TrimUI + Windows from a tag, with
`-trimpath`, `SOURCE_DATE_EPOCH`, pinned toolchains, an SPDX SBOM and SLSA
provenance attached; asset names match `patch_swap.go`; release notes come from
the CHANGELOG section of the tag (the in-app changelog reads them).

### REL-9 — Install when it suits the user (P3, S)
**Done when.** A downloaded update can be set to install at the next boot or
when idle on the charger (`upch.go` already downloads then); never during a game.

### REL-10 — Prove recovery on a device (P1, S) [HW]
**Why.** The boot guard, crash reports, the recovery card and the `.restart-now`
restart were written without a compiler or a device.
**Where.** `bootguard.go`, `crashreport.go`, `recoverycard.go`,
`recoverysettings.go`, `safeboot.go`, `launch.sh`.
**Done when.** On a Smart Pro: a forced panic (on the main goroutine and on
another one) leaves `crash-*.txt` and `errors.txt` shows "JukaHub CRASHED"; the
next start shows the crash card; Send report opens a prefilled form; Restart in
safe mode comes back in safe mode through `launch.sh`; three kills before the
first frame start safe mode, while three kills after it give safe mode but no
rollback; a fresh build that crashes three starts is rolled back from Version
history, and a restored build that fails three more gets safe mode, not a second
rollback; `patch/lastgood/` appears 90 s into a normal start and is offered
first; on an install without Version history "Previous version" restores the
update backup with Restart now; the chord card's choices work with the pad and a
USB keyboard. On Windows: the respawn after Restart, crash files in
`%LOCALAPPDATA%\JukaHub\logs`, and `JukaHub.exe.old` gone after the next start.

### REL-11 — A goroutine panic leaves a running game behind (P2, S)
**Why.** A panic on the main goroutine stops the screen-owning children now; one
on another goroutine ends the process at once (the runtime has no hook), and
`launch.sh` then brings MainUI back over a game that is still running.
**Where.** `player/launch.sh`, `crashreport.go`, the long-lived goroutines.
**Done when.** `launch.sh` (or a tiny supervisor) stops JukaHub's leftover
process group after a crash exit before handing the screen back, or the
long-lived goroutines recover and report instead of dying.

### REL-12 — Count starts that die in SDL init (P3, S) [HW]
**Why.** The boot guard now runs right after `img.Init`, but a build that fails
`sdl.Init`/`ttf.Init`/`img.Init` (logFatal, exit 1) is never recorded: the chord
check needs SDL, and the record notes whether the chord was held.
**Where.** `player/main.go` (startup), `player/bootguard.go`.
**Done when.** The record is written (running, chord unknown) before `sdl.Init`
and amended after the chord check, or REL-10's device run shows it is not worth
it and the decision is recorded here.

### REL-13 — Turn on the bug-report service (P1, S) [OWNER] [NET]
**Why.** Since 2026-11-10 every build posts Report a bug to
`https://generate.jukalang.com/api/bug-report` (`netlify/functions/bug-report.mjs`),
which files a public GitHub issue — but only once the site has a token; until
then devices fall back to the phone code.
**Where.** Netlify → generate.jukalang.com → Environment variables;
[docs/BUG_REPORTS.md](../docs/BUG_REPORTS.md).
**Done when.** `JUKAHUB_GITHUB_TOKEN` (a bot account's token with Issues
read/write on jukaLang/JukaHubV2) is set and deployed; `curl -i` on the address
answers 405 with `"configured": true`; a report from a device becomes an issue
with the `from-device` label and the sent sheet shows its number.

---

## Accessibility (A11Y)

### A11Y-1 — Narration of the focused control (P1, L)
**Why.** XAG 106 and every 2026 console ship system text-to-speech; JukaHub has
none. On the handheld this is the difference between usable and not for a
blind player.
**Where.** A `narrate(text)` service: Windows SAPI (`ISpVoice` via
go-ole or a `powershell` fallback), TrimUI `espeak-ng` as a package (Toolbox),
both off-thread; focus changes in `focus.go`, Home, Settings, lists and toasts.
**Done when.** Settings → Accessibility → Narration reads the focused control's
label, value and hint ("Vibration, on, A toggles"); a new focus interrupts the
old speech (XAG 106); speed and voice are settings; narration of the settings
dialog itself works.

### A11Y-2 — Reduce transparency and increase contrast (P1, M)
**Why.** The glass UI (`ui_glass.go`) has no opacity override; High Contrast is
just another palette that hard-coded colours ignore. iOS 26 needed Reduce
Transparency, Increase Contrast and a Tinted mode for the same reason.
**Done when.** "Reduce transparency" makes every glass surface opaque
(`glassA` returns solid tokens), "Increase contrast" adds 2 px outlines to
focusables and raises text contrast to ≥ 7:1, both applying to every screen
that uses the tokens (UX-12 finds the rest).

### A11Y-3 — Bold text (P2, S)
**Done when.** A switch loads Inter SemiBold for every role (the font ships in
`fonts/`), with the 14 px floor respected.

### A11Y-4 — Audio access: mono, balance, captions (P2, S)
**Where.** `videoplayer.go:1122` forces stereo (`-ac 2`); captions are on/off
per session (`videosubs.go`).
**Done when.** Mono audio and L/R balance apply to video, music and UI sounds;
captions can be always on with size and background settings.

### A11Y-5 — Timing (P2, S)
**Done when.** Long-press duration, repeat delay/rate (INP-10) and toast
duration are adjustable; chords can be "sticky" (press in sequence).

### A11Y-6 — Accessibility is a Settings section (P2, S)
**Why.** It is a modal reached from Display & theme or the Start menu.
**Done when.** Settings has an Accessibility section with every row; a shortcut
(hold Select + Start for 3 s, or Win+Ctrl+Enter-like chord on keyboard) toggles
narration.

### A11Y-7 — Magnifier (P3, M)
**Done when.** A zoom lens (2×/3×) follows the focus, toggled from Quick
Settings; uses a render-target copy, bounded memory.

### A11Y-8 — Reading fonts (P3, S)
**Done when.** Atkinson Hyperlegible (OFL) is offered as an interface font;
licence noted in `docs/DEPENDENCIES.md`.

### A11Y-9 — Switch access (P3, L)
**Done when.** A one- or two-button scanning mode walks the focus graph
(`focus.go` `BuildGraph`) with a configurable interval.

---

## Languages and locales (I18N)

### I18N-1 — A string catalogue (P1, L)
**Why.** Every UI string is an English literal; no translation is possible.
**Where.** New `internal/i18n` (message catalogue in `go:embed` JSON, keys are
the English source, CLDR plural rules), an extraction tool under `tools/`, and
`T("…")` at call sites, screen by screen.
**Done when.** Settings → Language exists; Home, Settings, the Start menu,
Quick Settings, footers and toasts go through `T`; a CI check lists untranslated
strings per language; missing translations fall back to English.

### I18N-2 — Dates, numbers and units (P1, S)
**Why.** Date layouts are still hard-coded in ~60 files and inconsistent ("2 Jan
2006" vs "Jan 2, 2006"; "Jan 2, 15:04" ignores the 12-hour setting).
**Where.** `clockformat.go` already has `formatDate`/`formatDateShort`/
`formatDateLong`/`formatDayLong`/`formatDateTime`/`formatDateTimeNear` and the
Date format setting (used by the alarm clock, the idle dock and Settings).
**Done when.** Every user-facing date goes through them; `formatNumber`/
`formatBytes` exist; both follow the locale (I18N-1) and the clock setting.

### I18N-3 — Fonts for every script (P1, M) [HW]
**Why.** Only Inter ships (`fonts.go` swaps anything else for look-alikes or
"…"): Chinese, Japanese, Korean, Arabic, Thai and Hindi titles are unreadable.
**Done when.** A fallback chain loads Noto Sans CJK / Arabic / Thai / Devanagari
subsets on demand (downloaded as a package, never bundled in the binary),
bounded by `cachebudget.go`; SDL_ttf's HarfBuzz shaping is used if the stock
library has it (check the firmware's SDL_ttf version first).

### I18N-4 — Input methods (P2, L)
**Done when.** The on-screen keyboard has pinyin and kana composition with a
candidate strip (`vkeyboard.go` already has a suggestion strip); physical
keyboards get `SDL_TEXTEDITING` handling.

### I18N-5 — First translations (P2, M) [OWNER]
**Done when.** A translation platform (Weblate/Crowdin, free for OSS) is set up;
Spanish, Portuguese (Brazil), Russian, German, French, Chinese (Simplified) and
Japanese reach Home + Settings coverage.

### I18N-6 — Right-to-left layout (P3, L)
**Done when.** Arabic/Hebrew mirror the Home grid, lists and footers; text uses
bidi reordering (golang.org/x/text/unicode/bidi).

---

## Network and connectivity (NET)

### NET-2 — Wi-Fi for real networks (P2, M) [HW]
**Why.** No hidden SSIDs, WPA-Enterprise, static IP, proxy or QR join
(`wifi*.go`).
**Done when.** "Add network" takes a hidden SSID and security type, EAP-PEAP
with identity/password (through the secret store), static IPv4/DNS, and an
HTTP proxy used by every client in `httpclients`; a Wi-Fi QR (`WIFI:T:…;S:…;`)
can be shown for sharing and scanned through the phone companion.

### NET-3 — Verify network time on the device (P1, S) [HW]
**Why.** Clock sync is implemented (`ntpsync.go`, `clockset_linux.go`) but was
written without a toolchain or a device.
**Where.** `ntpsync.go`, `clockjump.go`, `datetime.go`, `launch.sh` (TZ block).
**Done when.** On a Smart Pro and a Brick after a cold boot: joining Wi-Fi sets
the clock within ~20 s (the Logs show "clock set from …"), Settings → Date &
time → Clock last set shows it, the process really runs as root (settimeofday
succeeds), an alarm due in the corrected time rings, blocking UDP 123 falls back
to the HTTPS date, and with HTTPS blocked a clock more than 10 minutes off is
left alone (Clock last set says the HTTPS date did not confirm it). The image
has a CA bundle (system store or one of `bugrepCAPaths`); without one a cold
boot's clock is set from SNTP alone (errors.txt: "HTTPS confirmation is
unavailable"), which is recorded here either way.

### NET-5 — Hotspot and tethering (P3, S) [HW]
**Done when.** The probe for AP mode (`iw list`, hostapd on the image) is
recorded; if supported, Quick Settings offers a hotspot with a QR to join.

### NET-6 — USB modes: file transfer and networking (P2, M) [HW]
**Why.** Knulli ships MTP; JukaHub users must pull the card to copy ROMs.
**Done when.** If the kernel has configfs USB gadget support, connecting to a PC
offers "File transfer (MTP)" (umtprd as a package) or "Network (RNDIS/ECM)"; the
probe result is recorded either way.

### NET-7 — Network shares in Files and the library (P2, L)
**Done when.** SMB2/3 (go-smb2) and WebDAV locations can be added in Files
(credentials in the secret store), browsed, copied from, and used as a read-only
ROM or media source with local caching.

### NET-8 — VPN that can be switched on (P3, M)
**Where.** `smart_home.go` (Tailscale up/down, WireGuard interfaces listed only).
**Done when.** A WireGuard config can be imported (file or QR) and brought
up/down from Quick Settings, with status in the Wi-Fi chip; Tailscale shows its
IP and peers.

### NET-9 — mDNS discovery (P2, M)
**Why.** Peers are found by UDP broadcast only; mDNS/DNS-SD is how phones,
Kodi, Moonlight hosts and printers announce themselves.
**Done when.** JukaHub announces `_jukahub._tcp` (companion, live builder when
on, peer port) and browses for Moonlight/Kodi/peers; the one-shot query also
replaces zeroconf (PERF-7).

### NET-10 — Bluetooth audio that follows the user (P2, M) [HW]
**Why.** Knulli switches audio between Bluetooth and the speaker by itself and
reconnects controllers; JukaHub only detects a headset (`audiooutput.go`).
**Done when.** After SYS-1: a connecting headset takes the audio (video, music,
UI sounds), a disconnect returns it to the speaker without a pop, paired pads
reconnect after sleep (PWR-1).

### NET-11 — Re-probe the moment the network or the sign-in page changes (P2, S)
**Why.** The probe runs every 20 s and while a game owns the screen. After a
captive-portal sign-in the user returns to JukaHub and waits up to 20 s
before downloads resume, and switching Wi-Fi networks can leave the old
verdict on screen.
**Where.** `netmon.go`, `wifi_ui.go`, `browser.go` (visitor close), the
Wi-Fi supplicant change callback.
**Done when.** Joining or leaving a network, and closing the Browser after a
portal sign-in, each trigger an immediate probe, and a test covers the
portal-to-online transition without waiting for a tick.

### NET-12 — Keep the corrected time across a power cycle (P3, S) [HW]
**Why.** Network time only fixes the clock while the device is on; if FIX-2 finds
an RTC, the next cold boot could start near the right time instead of the stale
one.
**Where.** `ntpsync.go` (after `clockSetSystem`), FIX-2's findings.
**Done when.** When `/dev/rtc0` exists, a network-time set also writes the RTC
(`RTC_SET_TIME` ioctl, or `hwclock -w` without a shell), logged at Info; with no
RTC nothing changes.

### NET-14 — A new time zone reaches games without a restart (P3, S)
**Why.** JukaHub no longer calls `setenv("TZ")` at run time (libc's setenv races
with SDL's getenv on other threads), so a zone chosen on the device reaches games
only after JukaHub restarts; the Time zone row says so meanwhile.
**Where.** `datetime.go` (`dateTimeChildrenHaveZone`); the launch paths
`launchLibraryGame` (`game_library.go`), `launchEmulatorEntry` (`emulators.go`),
`launchGameProfile` (`gameprofiles.go`), `ports.go`.
**Done when.** One helper builds a game child's environment (`os.Environ` with TZ
replaced by the zone in force), every game and emulator launch path uses it, a
test covers the replacement, and the row's "after JukaHub restarts" note goes.

### NET-15 — FTPS (explicit TLS) for the FTP server (P3, M)
**Why.** FTP sends the password and files in plaintext; SFTP covers clients that
speak it, but Windows Explorer only speaks FTP.
**Where.** `ftp_server.go`, `nettoolsscreen.go`.
**Done when.** `AUTH TLS` with a persisted self-signed certificate whose
fingerprint the sheet shows, `PROT P` data channels, and the sheet recommends
FTPS or SFTP.

### NET-16 — The script guard knows where the page itself lives (P2, M)
**Why.** Scripts on a public page may not reach the LAN (`webdom_net.go`). A
public DNS name that resolves to a LAN address — split-horizon DNS, a Tailscale
`*.ts.net` name — is therefore classed as public, and its own scripts cannot
reach their own site. Looking the name up again to classify it would reopen the
DNS-rebinding hole the dial-time check closes.
**Where.** `textbrowser.go` (`fetchTextBrowserOnce`), `webdom_net.go`
(`webJSNetPageGuard`, `newWebJSNet`), `webdom_runtime.go`.
**Done when.** The page fetch records the address it actually connected to
(`httptrace.ClientTrace.GotConn`) and the guard classes the page by that address:
a page whose server is on the LAN reaches the LAN, a public page still cannot,
and a test covers both.

---

## Game library and emulation (LIB)

### LIB-1 — An incremental library index (P1, M)
**Why.** FIX-8: every open rescans the card synchronously; `mlib.go` already has
an incremental index pattern.
**Done when.** A background indexer keeps a bbolt index of ROM paths, sizes,
mtimes, CRCs (≤ 64 MiB) and metadata; the library opens from it instantly and
refreshes changed folders after a launch, a copy or a manual "Rescan".

### LIB-2 — More systems (P2, S)
**Why.** `romSystemAliases` maps 55 folder names to 20 systems: no
Arcade/FBNeo/MAME, Atari 2600/7800/Lynx, WonderSwan, Neo Geo Pocket, Sega
CD/32X, PICO-8, DOS.
**Done when.** The alias table covers the systems the TrimUI cores run (from
`compat.go`'s core inventory), with the right default core per system.

### LIB-3 — Collections that build themselves (P1, M)
**Done when.** Smart collections — Recently added, Most played, Never played,
by genre, by decade, by player count (from `gmeta_dat.go` metadata) — appear in
the library and can be pinned to Home as shelves; custom collections can be
ordered and pinned; the 32-tag limit becomes a bounded list per collection.

### LIB-5 — Rich media per game (P2, M) [NET]
**Why.** Art is libretro `Named_Boxarts` only.
**Done when.** Snaps and title screens from libretro-thumbnails, and (with
GAME-8's account) descriptions, screenshots, videos and manuals from
ScreenScraper; a manual opens in the PDF reader; everything is bounded by the
cache budget.

### LIB-6 — Verified dumps (P3, M)
**Why.** Promoted from the parking lot.
**Done when.** No-Intro/Redump DATs install as a package; the library verifies
CRC/SHA-1 in the background and badges games Verified / Unknown / Bad dump.

### LIB-7 — BIOS doctor with hashes (P2, S)
**Where.** `biosdoctor.go` (filenames for PS1/Dreamcast/Saturn; SHA-256 shown,
never compared).
**Done when.** A hash table (libretro `System.dat`) covers every system with a
BIOS; results say which file is missing or wrong and where it goes.

### LIB-8 — Standalone emulators updated on the handheld (P2, M) [HW]
**Why.** PPSSPP, mGBA, Flycast updates are only downloaded and reported
(`emulatorupdates.go`).
**Done when.** They install with the same backup/rollback flow as cores
(`corebuilds*.go`).

### LIB-9 — Ports are games (P2, M)
**Where.** `ports.go`, `port_compat.go`.
**Done when.** PortMaster ports appear in the library, recents, playtime and the
switcher; the PortMaster catalogue can be browsed inside JukaHub.

### LIB-10 — Resume everywhere, and at boot (P1, M) [HW]
**Why.** Auto save/load covers RetroArch only (`savestate_resume.go`); Knulli
boots straight back into the last game.
**Done when.** Standalone emulators that support it save on exit (PPSSPP,
DraStic, Flycast settings); an opt-in "Resume last game at boot" starts it after
the splash with a 3-second "Home instead" prompt.

### LIB-11 — State thumbnails (P2, S)
**Why.** `gmeta_states.go` shows thumbnails only if the user enabled
`savestate_thumbnail_enable` in RetroArch.
**Done when.** JukaHub sets it in the config it manages (`osdpreset.go`
conventions) and the switcher (SHELL-1) uses the images.

### LIB-12 — RetroAchievements, all the way (P2, M) [NET]
**Why.** `retroachievements.go` reads profile, recent unlocks and progress only;
the API key sits in plain JSON.
**Done when.** Per-game achievement lists with progress, a hardcore toggle that
writes `cheevos_*`, the key in the secret store, and an LED flourish on unlock
(`ledsync.go`).

### LIB-13 — Playtime you can trust (P2, M)
**Why.** Per-game time is keyed by title, capped at 64 games, days are pruned
at 90 (no year in review) and `jukauser.json` is rewritten every 30 s.
**Done when.** Sessions are appended to their own bounded log keyed by content
hash; 400 days are kept; Wrapped covers a year; muOS-style export (CSV/HTML) to
the card; the user config is no longer rewritten for playtime.

### LIB-14 — "Runs well here" labels (P2, M) [HW]
**Why.** Xbox's handheld labels and SteamOS's verified badges answer "will it
run?" before launch; `compat.go` has local data only.
**Done when.** A curated per-system/per-game table (package-delivered,
community-editable) shows Great / Playable / Poor on the game page with the
recommended core and profile.

### LIB-15 — A game page worth opening (P2, M)
**Done when.** The game page (`gmeta_ui.go`) shows the hero art, description,
screenshots, manual, achievements, last played, playtime graph, the save-state
strip and Play / Resume / Options as the first three actions.

### LIB-17 — Pin core builds the user trusts (P2, S) [HW]
**Why.** The libretro buildbot has no stable per-core tree for linux/aarch64 and
states only CRC32 (checked over HTTPS), so "Install automatically" always takes
the newest nightly. JukaHub records each installed build's SHA-256 and keeps the
replaced one, but cannot tell a good nightly from a bad one.
**Where.** `player/emulatorautoupdate.go`, `player/emulatorinstall.go`,
`player/corebuilds_ui.go`, `player/corebuilds_history.go`.
**Done when.** A core can be pinned on the Core builds page (the automatic pass
and "Update all" skip it, the badge says "pinned"), the automatic pass skips a
build whose CRC32 the user rolled back from before, and a test covers both.

### LIB-18 — Save-state warning on the device (P2, S) [HW]
**Why.** The game page's "made with another build" line is driven by
`core-history.json` and state file times; FAT time resolution, a clock read
before the first network sync and launch scripts that pick the core through
variables can all make it miss a state (it must never invent one).
**Where.** `player/corebuilds_history.go`, `player/gmeta_ui.go`.
**Done when.** On a Smart Pro: updating a core with states on the card makes the
game page show the line and name the build; rolling back with R1 makes it
disappear; a stock `Emus/<system>/launch.sh` for GBA, SNES and PS resolves to the
core it really starts (adjust `gameCoreFromLauncher` if not); a core replaced
while the clock read before 2020 produces no line until the next replacement
made with a sane clock; a replacement JukaHub did not record (a wrong clock, or
RetroArch's own updater) is labelled "another build", never with the wrong
build's date.

---

### LIB-19 — Box art: reuse the library's scan (P3, S)
**Why.** Each automatic box-art pass (boxart_auto.go) walks the card once because
the Games Library publishes no goroutine-safe cached scan, the tour's button
fetches without the not-found cache, and the fetcher saves that cache only when
a fetch ends.
**Where.** boxart_auto.go, game_library_ui.go, romsetup.go, onboarding.go.
**Done when.** The pass reads the library's cached scan; the tour's button uses
`startLibraryArtworkFetch(romsMissingArtwork(…))`; the fetcher saves misses every
N titles; the first pass waits for a trusted clock on the handheld.
**Verify.** Unit tests; time a pass on a 5 000-ROM card.

---

## Apps platform (APP)

### APP-1 — A store worth browsing (P2, M) [NET]
**Why.** The package catalogue (`pkgcatalog.go`, `jukaLang/Packages`) has no
screenshots, ratings or real dependencies (displayed only, `pkgui.go:1614`).
**Done when.** Catalogue entries carry screenshots, a description, size,
licence, source link and dependencies that install first; "Update all"; a
changelog per update; installs stay verified and staged (`pkgstage.go`) and
the catalogue is signed (SEC-16).

### APP-2 — A plugin SDK (P2, M)
**Why.** Plugins (`plugins.go`, `plugx.go`) can add themes, ROM formats,
profiles and packs, but there is no versioned API, sample or scaffold.
**Done when.** `docs/PLUGINS.md` documents a versioned manifest (with the
permissions of SEC-6), a sample plugin lives in `examples/`, and
`JukaHub --new-plugin <name>` (or a script) scaffolds one.

### APP-3 — A terminal with a real TTY (P3, M)
**Why.** `terminal_other.go` runs one `sh -c` per line without a pseudo-terminal,
so `vim`, `htop`, `ssh` and anything interactive fail.
**Done when.** Linux uses a PTY (creack/pty) with a minimal VT100 parser; Windows
uses ConPTY; the on-screen keyboard gains Ctrl/Esc/Tab/arrow keys.

### APP-4 — Files that forgive (P1, M)
**Why.** Deletes are permanent (`feDeleteEntry` in `files_manager.go`,
`os.RemoveAll`); Files can unpack archives but not make one; there is no
in-folder search.
**Done when.** Delete moves to a `.trash` per volume (bounded by size and age,
"Empty trash" in the storage view, SYS-8), **Compress to zip** makes an archive
of the marked rows, Files has a filter-as-you-type search, and copy and move
run off-thread with progress (the extraction queue in `files_extract.go` is
the model: one job at a time, a staging folder, a drain on the SDL thread).

### APP-5 — Calendar with events (P3, M)
**Done when.** The calendar popover (`calendarpopover.go`) shows events from an
imported `.ics` (CalDAV later), and an event can set an alarm (`alarm*.go`).

### APP-6 — The phone as a second screen (P2, M)
**Why.** The companion (`companion.go`) types, acts as a remote, uploads and
views the screen, but cannot browse the library or launch a game.
**Done when.** The companion page lists the library with art and launches a
game, uploads a save to the right folder, and mirrors JukaHub notifications;
every action goes through the existing pairing and queue.

### APP-7 — Notes that travel (P3, S)
**Done when.** Notes sync between paired devices through `ssync` and render
Markdown (headings, lists, checkboxes) in the reader view.

### APP-8 — Forms that POST (P2, M)
**Why.** The browser fills forms but sends every submission as a GET
(`browser.go` near the form submit path, documented in `docs/BROWSER.md`), so a
login or search form that requires POST fails even when cookies work.
**Where.** `browser.go` (form fill and submit), `webpage.go` (`webForm`, field
values), `browsercookies.go`.
**Done when.** A form with `method=post` is sent as
`application/x-www-form-urlencoded` (and `multipart/form-data` when it has a
file field), the response replaces the page, cookies set by the response are
kept, and redirects follow the GET rules; a local test server asserts the
method, body and cookie round-trip.

### APP-9 — A profile per Windows app (P3, M) [HW]
**Why.** Every Wine app shares one prefix and one controller mapping; the docs
say conflicting DLL overrides need their own `WINEPREFIX` and the Game Profiles
controller field is informational, so two apps that disagree cannot both work.
**Where.** `windowsapps.go`, `windowsapps_ui.go`, `gameprofiles.go`,
`docs/WINDOWS_APPS.md`.
**Done when.** An app can opt into its own prefix (created on first launch),
DLL overrides and a controller mapping, stored beside its profile; the setup
page shows which prefix an app uses; a second app with a conflicting override
launches with its own.

### APP-10 — Live bindings between ES modules (P2, M)
**Why.** goja has no modules, so `webdom_module.go` rewrites each one into a
function, and an imported name is a copy taken when the importing module starts.
Inside an import cycle a name read before its module ran gets a forwarding stub
(`webdom/85_scripts.js`), which covers functions and objects but not a `let` the
other module assigns later.
**Where.** `webdom_module.go` (references to imported names),
`webdom/85_scripts.js` (`modContext`, the namespaces).
**Done when.** References to an imported name in a module body read the
exporting module's namespace (with scope analysis in the rewrite, so local
variables and property names of the same spelling stay untouched), cycles
evaluate in the standard's order, and a test links a two-module cycle that
reassigns an exported `let`.

### APP-11 — Linked stylesheets for page scripts (P2, S)
**Why.** `getComputedStyle` (`webdom/45_css.js`) cascades inline styles and the
page's own `<style>` sheets only; linked sheets are fetched after the script
pass, for the renderer (`fetchWebStylesheets`). A script asking whether a panel a
linked sheet hides is visible hears "yes", and jQuery's `.show()` on such an
element writes nothing.
**Where.** `textbrowser.go` (`browseWebPageResult`), `webpage.go`
(`fetchWebStylesheets`), `webdom_runtime.go` (boot), `webdom/45_css.js`.
**Done when.** The first linked sheets (the renderer's limits) are fetched beside
the page's scripts and handed to the prelude before the parser scripts run, the
renderer reuses the same bytes instead of a second download, and a test shows
`.show()` on an element a linked sheet hides.

### APP-12 — Ranges that follow the document (P3, S)
**Why.** A `Range` (`webdom/55_traversal.js`) keeps its boundary nodes and
offsets when the DOM changes under it, so editors and highlighters that keep a
range across a change act on the wrong text.
**Done when.** Live ranges are tracked per document and their boundaries move on
insert, remove, `splitText` and `normalize` as the DOM standard says, covered by
the jsdom comparison in `tests/web/webdom_dom.test.mjs`.

### APP-13 — Customized built-in elements (P3, S)
**Why.** `customElements.define(name, cls, { extends: "button" })` is
registered, but a `<button is="name">` is never upgraded
(`webdom/50_shadow_ce.js`), so components built that way do nothing.
**Done when.** `is=` elements made by the parser, `innerHTML` and
`createElement(tag, { is })` upgrade like autonomous custom elements, with a node
test.

### APP-14 — Archives with a password (P3, S)
**Why.** An encrypted zip, 7z or RAR is refused (`errFeArchiveEncrypted` in
`files_extract.go`): the standard zip reader cannot decrypt, and 7-Zip is run
with a password nobody uses so that it fails instead of waiting for one.
**Done when.** The archive sheet offers **Password…** for an encrypted archive,
the on-screen keyboard takes it with submit-only semantics (a dedicated
vkTarget like `vkTargetGroqKey`; never written to the config), and the
extraction hands it to 7-Zip — or says that an encrypted zip needs 7-Zip.

### APP-15 — 7z without 7-Zip (P3, M)
**Why.** A handheld without `7zz` cannot open a .7z at all, although the
vendored `github.com/ulikunitz/xz/lzma` decodes the LZMA and LZMA2 that most 7z
archives use (`emu7z.go` chose 7-Zip because no compiler was at hand to test a
reader).
**Done when.** A 7z whose folders use LZMA or LZMA2 (with or without the BCJ
x86 filter) and whose header is not encrypted unpacks in-process through the
same staging and `os.Root` rules, with fixtures made by 7-Zip in the tests;
other coders (BCJ2, PPMd, AES) still go to 7-Zip.

---

### APP-16 — More from WPE Browser (P2, M) [HW]
**Why.** The tester pointed at [WPE Browser](https://github.com/matweew/wpe-tsp);
JukaHub now hands pages to it, installs it and grew its ad filter, but WPE has
more worth taking.
**Where.** videoplayer.go / browser_youtube.go, browser_engines.go (home page
choice), browserzoom.go, apps.go, installbar.go, ci.yml.
**Done when.** Videos play through mpv-tsp (A133P hardware H.264) when installed
(the plan is [docs/VIDEO_VPU_OFFLOAD.md](../docs/VIDEO_VPU_OFFLOAD.md)); YouTube
streams resolve through WPE's RustyPipe `ytp` when present (≈2 s and ≈28 MB
instead of ≈9 s and ≈330 MB with yt-dlp, per its release notes); the home page
choice has "Last page"; zoom is remembered per site; the Apps screen cannot start
`Apps/WPE` while an update swaps it (`brwWpeGate`); the install bar's "Ready"
card stops saying "restart JukaHub" after an app install; CI's `RACE_TESTS`
includes `BrwWpe`.
**Verify.** Unit tests; HW-2 step 8 for the device parts.

---

## Windows desktop (DESK)

### DESK-1 — No console window, a real log (P1, S) [WIN]
**Why.** Release builds lack `-H windowsgui` (`build.bat:354`, `release.sh`), so
a console opens with the app. Crash reports already land in
`%LOCALAPPDATA%\JukaHub\logs` (`crashreport.go`), but WARN+ lines still go only
to stderr, which a GUI build does not have, and `errors.txt` exists only on the
handheld (`launch.sh`).
**Done when.** Release builds are GUI-subsystem; WARN+ lines are appended to
`%LOCALAPPDATA%\JukaHub\logs\errors.txt` with the handheld's 1 MiB rotation;
Report a bug and Diagnostics name the folder; Settings → Help → "Open logs
folder".

### DESK-2 — Sharp on high-DPI screens (P1, S) [WIN]
**Why.** No DPI manifest, no `SDL_HINT_WINDOWS_DPI_AWARENESS`, no
`WINDOW_ALLOW_HIGHDPI`: Windows bitmap-stretches the window above 100%.
**Done when.** Per-monitor-v2 awareness (manifest in `JukaHub.rc` +
hint), rendering at native pixels with the logical canvas scaled by SDL; text
is crisp at 150% and 200%.

### DESK-3 — Windows that remember (P2, S) [WIN]
**Done when.** Size, position, monitor and maximised state persist; a
borderless-window mode exists; the monitor can be chosen (today always display
0, `main.go:388`); the fullscreen cleanup lands first.

### DESK-4 — An installer and package-manager listings (P2, M) [WIN] [NET]
**Why.** Only portable zips exist; no VERSIONINFO (`JukaHub.rc` has an icon
only); no Start-menu entry or uninstaller.
**Done when.** An Inno Setup (or MSIX) installer, winget and Scoop manifests
generated by the release workflow, VERSIONINFO from the version in
`jukaconfig.json`; the portable zip remains.

### DESK-5 — Code signing (P2, S) [WIN] [OWNER]
**Done when.** Release executables and the installer are Authenticode-signed
(Azure Trusted Signing or SignPath's free OSS programme) so SmartScreen stops
warning.

### DESK-6 — Steam and Big Picture (P3, S) [WIN]
**Done when.** "Add JukaHub to Steam" writes a non-Steam shortcut
(`shortcuts.vdf`) with art from `branding/`; JukaHub behaves under Steam Input
(the virtual X360 pad is the hint vocabulary).

### DESK-7 — Start with Windows that works (P2, S) [WIN]
**Why.** The autostart `.cmd` (written by `autoStartLauncherFor` in `autostart.go`) flashes a
console and sets no working directory, while the config path is relative
(`main.go:83`): "Start with Windows" likely fails with "Failed to load config".
**Done when.** Autostart writes a `.lnk` (or a `Run` registry value) with the
working directory set.

### DESK-8 — Discord Rich Presence (P3, S) [WIN]
**Why.** Promoted from the parking lot.
**Done when.** Opt-in presence over Discord's local IPC pipe shows the game or
the screen; no account tokens involved.

### DESK-9 — Open with JukaHub (P3, S) [WIN]
**Done when.** Command-line arguments open a file (media, ROM, PDF) or a scene
(`--scene Settings`); the installer registers "Open with JukaHub" for media and
ROM types.

### DESK-10 — Windows notifications (P3, S) [WIN]
**Done when.** While the window is minimised, download/alarm/update
notifications appear as Windows toasts (AUMID set by the installer).

### DESK-11 — Brightness on Windows (P2, S) [WIN]
**Why.** On Windows the header's sun drives the monitor JukaHub is on over
DDC/CI (dxva2) — tried first, since a laptop's own panel never answers it —
else a laptop or handheld panel through WMI (Windows PowerShell's CIM cmdlets
on the first active instance, one process per read or write; a panel with
eleven or fewer levels gets a 10% step), else dims JukaHub's own picture
(`brightness_windows.go`, `brightnessSoftRender` in `brightnessctl.go`). None
of it has run on a Windows machine.
**Done when.** On a laptop (and a ROG Ally / Legion Go class handheld) the card
moves the same level as Windows' slider and follows it while open; on a desktop
with a DDC/CI monitor it moves the monitor's brightness; with neither (a VM,
Remote Desktop) the card says it dims JukaHub only and the dim survives a
restart; no console window flashes; the startup probe does not delay the first
frame. If a PowerShell run per step feels slow, keep one PowerShell process for
the session or call WMI through COM. Also open: the software dimmer is not
drawn on the shutdown card and the boot animation (`shutdown.go`, `bootos.go`
present their own frames), so a dimmed PC flashes to full brightness there —
call `brightnessSoftRender` before their `Present`.

---

## Hardware validation queue

Every **[HW]** task in one list, so a device session can work through them:
HW-1, PERF-1, PERF-4, PERF-8, PERF-9, PERF-10, GPU-1, GPU-4, UX-5, UX-7, UX-10,
GAME-2 (verify), GAME-9, XBX-1, XBX-4, AND-1, AND-3, AND-5, AND-6, SYS-1
(verify), SYS-2, SYS-3, SYS-7, SND-1, FIX-13 (with PWR-3), DOC-1; from the North star: INP-5, INP-6,
SHELL-1 (verify), SHELL-2, PWR-1, PWR-2, PWR-3, PWR-5, PWR-8, REL-7, NET-2,
NET-5, NET-6, NET-10, LIB-8, LIB-10, LIB-14, I18N-3, TEST-10, APP-9; from the second
afternoon pass: FIX-2, NET-3, NET-12, REL-10, REL-12, LIB-17, LIB-18, SEC-21;
from the evening pass: MED-19, MED-20, MED-22, MED-25, SND-5, SND-6, INP-15;
from the late-night pass: PERF-15; archives: SYS-15; brightness: SYS-16.
From the Remote Play passes: PSR-1, PSR-2, PSR-6, PSR-7, PSR-10, PSR-13.
From the liquid background, animated wallpaper and Screen Share pass: GPU-5,
PERF-17, PERF-18, SYS-18.
From the APK Store, Voice command and spatial audio pass: AND-17, INP-20,
SND-7, SND-8, SND-13, GAME-17.
Also still open from earlier passes: the browser's touch accuracy and link-mode
ergonomics on real hardware (`docs/BROWSER.md`).

Device checks of finished work that have no task of their own: the Live Builder
(switch it on in Settings → Network, push from a laptop's builder at
`http://localhost`: an ordinary design applies, one with a program waits for
approval, a second push gets 409 while it waits, New pairing code works; on
Windows the builder reaches `localhost:8422` and `JUKAHUB_LIVE_LAN=1` opens the
LAN); FTP with Windows Explorer and FileZilla (sign-in as `juka`, folders listed
as folders, uploads into a share, `Roms/Ports` refused, idle and network-change
stop with a toast; started before Wi-Fi at a cold boot, FTP keeps running when
network time sets the clock and stops 15 minutes after its last use) and SFTP's
card root and "Share whole system…"; the Date &
time picker and the first-run Wi-Fi and time-zone buttons on the Brick's 960×720
canvas; on Windows, `.bat` launchers and ROMs whose names contain `&`, spaces and
parentheses, and the refusal for `%`. From the evening pass: on a Smart Pro
and a Brick the Controller test lights X for the printed X (and **Swap X and
Y** off brings the old mapping back), the phone remote's X and Y do X and Y,
the charging bolt appears within a second of plugging in and goes when
unplugged, the volume button and card work by pad and by USB keyboard (and by
wheel on Windows), the Home pairing QR's preview opens and puts the backlight
back, and the PS Remote Play pairing keyboard answers the pad and a USB
keyboard.
From the CrossMix report: on a CrossMix card the Emulators page shows PSP and
NDS as Available with the script their `config.json` names (`ppsspp_*.sh`,
`drastic.sh`), and a PSP and an NDS game start from the Games library through
it (CrossMix's `common_launcher.sh` gets an absolute ROM path, where MainUI
passes `Emus/<system>/../../Roms/...`); LAN play finds `ra64.trimui` and
RetroArch comes up with its own config; Stick Calibration edits the ini of the
PPSSPP build that is played; the save manager lists the PSP saves under
`Emus/PSP/PPSSPP_<version>/`.
From the JavaScript pass: on a Smart Pro and a Brick, a page whose content its
scripts build (a framework app, a jQuery site, a news page with lazy images)
draws that content; with **Settings → Browser → JavaScript** off, **Reload**
draws the same page as its server sent it; a public page cannot fetch from the
LAN (the debug log names the refusal); and `JUKAHUB_LOG_LEVEL=debug` shows one
`scripts on …` line per page.

---

## Ideas parking lot

Not tasks yet: each needs a *Why* and a *Done when* before it moves up. Sizes are
rough.

- **Theme from an image** (M): median-cut palette into the 16 theme roles, built
  on `themeloader.go`.
- **Seasonal themes by date** (S): the time-of-day hook exists; add a date table.
- **Time capsule / sealed notes** (S), **secret modes** (S), **playable credits**
  (S), **virtual pet** (M), **retro OS skin pack** (M), **arcade kiosk** (M).
- **Save forensics** (M): list + diff two snapshots.
- **Randomizer suite** (L): start with an IPS/BPS patch applier.
- **Speedrun timer/splits** (M): no auto-split (needs memory reads).
- **LAN arcade leaderboard** (S–M) for JukaHub's own games.
- Shipped on 2026-11-10: voice search via the phone, as Voice command's phone
  page (`docs/VOICE.md`). Not planned: an always-listening wake word (battery,
  privacy — the microphone opens only when asked).
- **Home Assistant kiosk** (M), **Steam Deck pairing** (L), **Juka playground** (M),
  **fantasy console** (XL), **chiptune tracker / pixel editor** (L).
- Promoted on 2026-11-09: widgets → SHELL-8, internet radio → MED-10, Kodi
  remote → MED-14, ROM verifier → LIB-6, Discord Rich Presence → DESK-8, QR
  Wi-Fi → NET-2, manuals → LIB-5.
- Promoted on 2026-11-10: encrypted cloud save backups → SYS-19 and save-sync
  conflict resolution → SYS-20.
- **Per-game colour filters** (S, muOS has them): a colour matrix per content on
  top of the accessibility palettes.
- **Rewind per system** (S): RetroArch rewind on for light cores, off for heavy
  ones, as a game-options row.
- **Speech-to-text in chat** (L): needs an on-device model or the phone; Switch 2
  ships it for voice chat.
- **Netplay beyond the LAN** (L): RetroArch's lobby and relay servers.
- **"Play style" profile** (S): muOS-style summary of when and what you play,
  from LIB-13's session log.
- **Screen Share sound** (L): the stock firmware has no ALSA loopback to capture
  the output from, and a tap in JukaHub's own mixer would only cover its media,
  never a game.

**Known impossibles** (don't spend time here): live translation and input
display over a running emulator (drawing *something* over it is possible after
all — muOS 2601 injects overlays into SDL/GLES content; see SHELL-2), a "last 30
seconds" clip of emulator play, phone second screens that need game RAM, HDMI couch mode
(Screen Share puts the screen in a TV's browser instead — see SYS-18),
OpenCL/Vulkan compute on the GE8300 (no working runtime on these images), and
anything that fakes a store license.
