# JukaHub engineering backlog

Open tasks: **111**.

Open work an agent can finish and check without a person at the device —
builds, tests, static checks, scripted probes, screenshots it can read — plus
an ideas list. Finished work goes to the [changelog](../CHANGELOG.md); checks
on a handheld or a PC go to the
[hardware validation log](../docs/HARDWARE_VALIDATION.md).

Each task is `### ID — title (priority, size) [tags]` with **Where.** (the
files), **Done when.** (acceptance) and, where it is not obvious, **Verify.**
P0 blocks a release, P1 is current user pain, P2 planned value, P3 polish; S
is at most half a day, M two days, L a week (split anything bigger). [WIN]
needs a Windows build, [NET] the network, [OWNER] something only the owner has.

When a task is done: delete it, add the change under `[Unreleased]` in the
changelog, and put any device check in the hardware log. Every change keeps
`make check` and both release builds green. Ground rules: the 1 GiB Smart Pro
stays responsive (nothing blocking or allocating per frame on the render
thread); use the shared design and input-hint helpers; log with
`logDebug`/`logInfo`/`logWarn`/`logError`, never `log`; tests only for data
integrity, security, installers and outside parsers. See
[development](../docs/DEVELOPMENT.md) and [contributing](../CONTRIBUTING.md).

---

## Mail and calendar (MAIL)

---

## Performance and graphics (PERF, GPU)

### PERF-7 — Trim heavy dependencies (P2, L) [WIN]
**Where.** goja (the Browser's DOM pass: `webdom*.go`, `webscript.go`),
gopsutil (`processes.go`, `systool_*.go`, `terminal.go`, `textbrowser.go`,
`plugnative.go`), zeroconf (`textbrowser.go`, `moonlight_discovery.go`,
`nas_ui.go`).
**Done when.** goja and the DOM pass sit behind a `//go:build !nojs` split (the
lite build shows pages as they arrived and hides Settings → Browser →
JavaScript); gopsutil gives way to `/proc` + `statfs` and two Win32 calls, and
zeroconf to a one-shot mDNS query that still finds Moonlight hosts and SMB
shares; features unchanged.
**Verify.** Size per package (`go tool nm -size -sort size`, the top ten) and
cold-cache build time before and after, in a new `docs/PERFORMANCE.md`.

### PERF-11 — Release scene-owned textures when a scene closes (P2, M)
**Where.** `scene.go` (module registry), the per-scene caches listed in
`docs/DEVELOPMENT.md` (cache inventory).
**Done when.** Leaving a scene trims its scene-specific caches (not the shared
text/shape caches); re-entry warms them again without a visible pop.
**Verify.** `JUKAHUB_TEXSTATS=1` shows the drop after leaving Browser/Chat.

### PERF-12 — Per-frame allocation sweep (P2, M) [WIN]
**Where.** `downloadsui.go`, list renderers, `textcache.go`.
**Done when.** `go test -bench` style allocation probes (temporary, not committed
as tests) show 0 allocs/frame on Home, Library and Downloads at rest.
**Verify.** pprof alloc profile of a 60 s idle session on Windows attached.

### GPU-2 — Fewer render-target switches (P2, M)
**Where.** `shapeCache.go`, `dynamicbg.go` (`bgGrads`), `iconatlas.go`,
`warmSceneCaches` in `bootwork.go`.
**Done when.** Bakes are batched (one target switch per cache per frame, or
software surfaces uploaded once), and a scene change on the device has no frame
> 50 ms caused by baking.
**Verify.** framestats histogram during scene changes before/after.

### GPU-3 — Geometry instead of textures for flat shapes (P3, M)
**Where.** `shapeCache.go`, `ui_glass.go`, `backgroundfx.go`; `glass_real.go`
and `liquidbg.go` show the allocation-free `RenderGeometry` pattern.
**Done when.** Backdrop glows and glass fills draw through geometry where a
measurement says it is cheaper; the texture path stays for the software
renderer.

---

## Architecture (ARCH)

### ARCH-1 — Package split, step 1: pure leaves (P1, M) [WIN]
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

### ARCH-5 — Shrink the main loop (P2, L) [WIN]
**Done when.** The loop body is a short sequence of named phases (input, update,
draw, present, pace) in their own files; `core.go` is split by responsibility
(render helpers, triggers, video entry points) without behaviour change.

### ARCH-7 — Retire the 0.4.0 upgrade shims on 2027-05-01 (P3, S)
**Where.** The six functions and tables marked `remove after 2027-05-01`.
**Done when.** On or after that date they go with their tests (a caller that
still names an old Settings page is pointed at its section first), and `make
check` stays green.

---

## Build, tests and CI (BUILD, TEST)

CI runs two jobs on a push (`ci.yml`) and the slow checks weekly
(`weekly.yml`); new checks go to `weekly.yml` or to `make` targets, never to a
new per-push job.

### BUILD-6 — The Box64 pack reaches a release (P1, S) [NET]
**Where.** `.github/workflows/wine-runtime.yml`, `docker-image.yml`
(`build-wine-runtime`, `attach-wine-runtime`), `player/native/build-runtimes.sh`
(`wine`, `ALLOWED_NEEDED`), `.github/scripts/build-root.sh`.
**Done when.** A push's release carries `JukaHub-Wine-linux-arm64.zip` whose
BUILDINFO shows only `ALLOWED_NEEDED` libraries, nothing newer than GLIBC 2.31
and `box64 --version` 0.4.4; the advisory step prints `wine-10.0` or names the
next fix; a push with unchanged inputs says "Reused … from run N".

### TEST-1 — A scene-dump tool, not a test (P1, M) [WIN]
**Where.** New `player/tools/scenedump/` (a `go run` program reusing the player
package through an exported entry point or a build tag).
**Done when.** `go run ./tools/scenedump -out tmp/scenes` writes every config
scene and the main Go pages at 1280×720 and 960×720 (SDL dummy driver, software
renderer) in under a minute.
**Verify.** The PNGs exist and look right; `docs/DEVELOPMENT.md` documents it.

### TEST-8 — Fuzz the parsers in package main (P2, M)
**Where.** `weekly.yml` (`fuzz` covers `internal/jdelta` and
`internal/releasesign`; package main needs a CGO job or ARCH-3's moves).
**Done when.** Fuzz targets for M3U, CUE/GDI sheets, the PDF and EPUB readers,
tar extraction (`extractTarMembers`), package manifests,
`/proc/bus/input/devices` and the XML config run 30 s each in the weekly job,
keeping their corpus.

### TEST-15 — A reproducible build root for the helpers (P3, S) [NET]
**Where.** `.github/scripts/build-root.sh` (`create`, which every arm64 build
uses), `player/native/build-helpers.sh`, `player/native/build-runtimes.sh`.
**Done when.** The root comes from a dated `snapshot.ubuntu.com` archive, pip
installs with `--require-hashes`, and the `BUILDINFO.txt` of the helpers,
AndroidRT and the Box64 pack (built in the same root, Box64 with gcc 10 from
focal's universe) names the snapshot.

### TEST-16 — A supported OpenSSL in the Remote Play helpers (P2, S) [NET]
**Where.** `player/native/build-helpers.sh` (native mode), `remote-play-helpers.yml`
(add `perl` for OpenSSL's configure).
**Done when.** OpenSSL 3.0 or 3.5 LTS is built `no-shared` from a
checksum-verified tarball into `DEPS_PREFIX` and found through
`-DOPENSSL_ROOT_DIR`; `BUILDINFO.txt` names its version; NEEDED is unchanged.

### TEST-17 — Every helper dependency from a verified source, offline configure (P2, S) [NET]
**Where.** `player/native/build-helpers.sh` (`MINIUPNPC_*`, `LIBEVENT_VERSION`).
**Done when.** miniupnpc is built statically into `DEPS_PREFIX` like the other
dependencies; configure runs with `-DFETCHCONTENT_FULLY_DISCONNECTED=ON` and
BUILDINFO lists nothing fetched by configure; libevent is 2.1.13-stable with its
tarball's SHA-256 checked against the signed release.

### TEST-18 — A packager's digest for the Box64 source (P3, S) [NET]
**Where.** `player/native/build-runtimes.sh` (`BOX64_COMMIT`).
**Done when.** A packager's digest for the v0.4.4 release tarball (AUR, or
Debian or Alpine once they reach it) is checked once against the tree at the
pinned commit, and the comment above `BOX64_COMMIT` names that source.

---

## UX and design (UX)

The review behind these is [docs/UX_REVIEW.md](../docs/UX_REVIEW.md).

### UX-4 — One icon stroke weight (P2, M)
**Where.** `icondefs.go`, `icondraw.go`, `iconatlas.go`.
**Done when.** Every 32 px glyph uses one stroke weight and outline style (the
Packages glyph is a filled 3D box among outlines); atlas re-baked.

### UX-8 — Touch targets and safe area everywhere (P2, M)
**Where.** `touchtarget.go`, scene panels.
**Done when.** Every pressable control has a ≥ 44 px hit area centred on what is
drawn; no control sits in the 32 px corner margin.

### UX-9 — Every state for every component (P2, M)
**Done when.** The ten most-used scenes use `UIEmptyState`/`UIErrorState`/
`UILoadingSkeleton` and the offline treatment from `offlineaware.go`; a list of
remaining exceptions is in the PR.

### UX-12 — Every screen on the tokens (P1, M)
**Done when.** Each uses `ui_glass.go`/`design.go` tokens and roles; a grep for
`colorNew(`/`sdl.Color{R:` in UI code lists only documented exceptions (art,
game palettes).

---

## Games and emulation (GAME, PSR, XBX)

### GAME-4 — A per-button remap editor for RetroArch games (P2, M)
**Where.** `racontrols_remap.go` (already writes a one-launch `.rmp` for the
Buttons choice's swaps and turbo), `racontrols.go`, `game_options_ui.go`.
**Done when.** The game options sheet maps each RetroPad button of a game
beyond the swap presets, and the launch writes it through the same remap file
and cleanup; standalone emulators stay out.

### GAME-6 — RetroArch's overlay pack, and a cancel for Download looks (P3, S) [NET]
**Where.** `shader_looks_download.go` (shaders and SkyWalker541's overlays
already), `ratuning.go`, `cheats_db.go` (the buildbot pattern).
**Done when.** With no overlay found, the Overlay row offers libretro's
buildbot `overlays.zip` (with progress and the same file-ownership rules), and
a running Download looks can be cancelled.

### GAME-10 — Netplay: invites, passwords, plain RetroArch hosts (P3, S)
**Where.** `netplay_rooms.go`, `netplay_ui.go`, `companion.go`.
**Done when.** A hosted game can be shared as a link or QR that the phone
companion forwards to a friend's JukaHub; a password-protected room asks for
the password (submit-only vkTarget) and passes it as `netplay_password`;
RetroArch's LAN discovery reply on UDP 55435 is parsed so a PC running plain
RetroArch is listed (the packet layout is in `netplay_private.h`; it changed
in 1.10).

### GAME-19 — The game assistant, second round (P3, M)
**Where.** `gameassist.go`, `gameassist_linux.go`, `voiceorb.go`.
**Done when.** A follow-up in the overlay keeps the last exchange; the D-pad
scrolls a long answer; launches from Apps and ports are named in the prompt; on
a PC **Juka can see the game screen** captures the game's window, and the answer
shows in an always-on-top overlay.

### GAME-23 — FreeCell in Solitaire (P3, M)
**Where.** `solitaire.go` (rules over typed pile lists, ready for a second
layout), `solitaire_ui.go` (`solLayout`, `solTopRow`, `solColumnOf`,
`solTopAt` and the `solK*` constants are Klondike's).
**Done when.** A Game row picks FreeCell: 4 cells, 4 foundations, 8 columns
dealt 7/7/7/7/6/6/6/6 face up, deals numbered with Microsoft's generator (deal
1 is its #1); runs obey the supermove limit, (free cells + 1) × 2^(empty
columns), halved onto an empty column; the smart move uses a cell last;
auto-finish knows FreeCell; statistics are per variant (save version 2 reads
version 1 as Klondike's) with a Reset statistics row that asks first.
**Verify.** A saved FreeCell game resumes exactly; a version-1 save still
loads with its Klondike statistics.

### PSR-8 — Remote Play through PSN, without port forwarding (P2, L) [NET]
**Where.** `psn_signin.go` (keeps only the Account-ID today), `psremote*.go`,
`native/chiaki-stream.c` with chiaki-ng's `lib/include/chiaki/remote/holepunch.h`
at the pinned commit (read it before writing a call; chiaki-lib already builds
the curl and miniupnpc it uses).
**Done when.** After the user turns on **Play away from home**, the PSN refresh
token is kept (encrypted like the other API tokens, never logged, deleted on
sign-out) and renews itself; a paired console streams from another network
with no port forward; a refused or expired token says so; nothing changes for
someone who never signs in; the docs describe it.

### XBX-3 — Reproducible hakuX build (P3, M) [NET]
**Done when.** If the port documents its build (TrimUI SDK or
`ghcr.io/loveretro/tg5040-toolchain`), `.github/workflows/hakux.yml`
(workflow_dispatch, pinned tag) builds the package and attaches its SHA-256. Do
not guess build steps.

---

## Game library (LIB)

### LIB-5 — Videos and manuals per game (P2, M) [NET]
**Where.** `artwork_screenscraper.go` (box art and details for a signed-in
user), `artstyle_fetch.go` (snaps and title screens), `gmeta_ui.go`, the PDF
reader, `cachebudget.go`.
**Done when.** With a ScreenScraper account a game's video and manual are
fetched on request, the manual opens in the PDF reader, and both stay inside
the cache budget.
**Blocked (owner credential).** The first criterion — "With a ScreenScraper
account" — needs the owner's ScreenScraper login (the account's `devid`/
`devpassword` for the API, and a member account, since videos are served to
signed-in members) which no agent has. It cannot be skipped and passed: the
videos and manuals exist only behind that authenticated API, so there is
nothing to fetch, open or measure against the cache budget without it. Left
open for the owner.

### LIB-12 — RetroAchievements: the game's list, and the key (P2, M) [NET]
**Where.** `retroachievements.go`, `raach_settings.go` (hardcore and the other
switches), `raach_unlocks.go` (the LED flash on unlock), `gmeta_ui.go`.
**Done when.** A game's page lists its achievements with the player's
progress, and the RetroAchievements key is kept in the secret store (SEC-8).

### LIB-15 — A game page worth opening (P2, M)
**Done when.** The game page (`gmeta_ui.go`) shows the hero art, description,
screenshots, achievements, last played, playtime graph, the save-state strip
and Play / Resume / Options as the first three actions (manuals and guides are
under More → Manual & guide).

### LIB-30 — PSP and Dolphin saves in the save handoff (P3, M)
**Where.** `savehandoff.go`, `ppsspp_*.go`, `dolphin*.go`.
**Done when.** A PSP game's SAVEDATA folders and PPSSPP states (by the ID read
from its ISO/CSO/PBP) and a GameCube game's GCI saves move with the game.

### LIB-31 — Share Game Lab setups from the phone (P3, S)
**Where.** `gamelab_share.go`, `companion_files.go`.
**Done when.** Share in Game Lab offers a QR to download the profile on a
phone, and the companion can upload a profile into `game-lab/import` (JSON
only, 64 KB cap).

### PROF-2 — Per-person RetroAchievements and standalone-emulator saves (P2, M)
**Where.** `retroachievements*.go`, `profiles_saves.go`, the managed PPSSPP
launch (its memory stick folder), DraStic's launch environment,
`ports_library.go`.
**Done when.** A profile can sign in to its own RetroAchievements account;
PPSSPP gets a memory stick folder per profile; ports show the shared-saves
notice.

---

## Android games (AND)

### AND-4 — Text input for Android games (P2, M)
**Done when.** `showKeyboard` opens JukaHub's on-screen keyboard over the game
(IPC runner ↔ hub) and the text reaches the game through its natives.

### AND-13 — Auto's window fallback adopts a context-first game's context (P3, M)
**Where.** `androidrt/src/eglpresent.c` (`pr_eglMakeCurrent`,
`pr_eglCreateContext`, `pr_eglDestroyContext`), `tests/smoke`
(`EGLTEST_CONTEXT_FIRST`).
**Done when.** A raw context never yet current is mapped to SDL's at its first
`eglMakeCurrent` with SDL's surface (destroy and share handled); one made
current with a pbuffer stays raw; the context-first smoke case asserts 5
`SDL_GL_SwapWindow` frames.

### AND-18 — Split APKs and app bundles (P3, M)
**Where.** `apkstore_scan.go`, `apkstore_verdict.go`, `androidpak.go` (staging).
**Done when.** The store groups a base APK with its splits (same package and
version, side by side or inside a bundle), judges them as one app, and
AndroidPak stages the native libraries from the matching split.

### AND-21 — Minecraft copy: pick only what the port lacks (P3, S)
**Done when.** The plan skips the ones the port provides (by the port's own file list),
and the copy row shows the size before A.

---

## Media (MED)

### MED-3 — Reactive visualiser from real audio (P3, S)
**Where.** `musicplayer.go` (synthetic bars today), `ledsync.go` (`audioBus`).
**Done when.** The bars come from an FFT of the decoded PCM (bounded cost per
frame).

### MED-5 — Screenshot gallery by game (P3, S)
**Where.** `screenshot.go`, `imageviewer.go`, `recordings.go`.
**Done when.** Screenshots open as a gallery with one section per game, newest
first: JukaHub's own captures and RetroArch's are grouped by the game named in
the file name, anything else under "Other"; A opens a picture full screen and
L1/R1 step between games.

### MED-7 — Crossfade and ReplayGain (P3, S)
**Where.** `musicplayer.go`, `musicsettings.go`.
**Done when.** An optional crossfade (0–10 s) overlaps a track's end with the
next one, and ReplayGain track/album tags (read by ffprobe) set the level when
switched on; both stay off on the bit-perfect path.

### MED-8 — Podcasts offline (P2, M)
**Done when.** Episodes download for offline listening (Downloads queue), OPML
import/export works, chapters are shown, played episodes clean up.

### MED-10 — Radio buffering and now-playing titles (P3, M) [NET]
**Done when.** The Radio Browser directory (Music → Radio,
`radiodir.go`) and saved stations show a buffering state while a stream
connects and the station's now-playing title (ICY metadata) while it plays.

### MED-15 — Search, tracks and folders in the music library (P3, S)
**Where.** `music_shelf.go`, `mlib.go`, `musicplayer.go`.
**Done when.** The music library has Tracks and Folders tabs with natural sort,
and a search (on-screen keyboard) over title, artist and album that answers
over a 2,000-track library without a visible stall.

### MED-18 — Trim, rename and delete a recording (P2, M)
**Where.** `recordings.go`, the scene that lists them, the video player.
**Done when.** A recording can be renamed, deleted after a two-press
confirmation (into the APP-4 trash once it exists) and trimmed to in and out
points (ffmpeg stream copy where the codec allows, else a re-encode), keeping
the original until the trimmed file plays; the list shows duration and size.
**Verify.** A test trims a generated clip by stream copy, checks it with
ffprobe, and finds the original intact until the trim is confirmed.

### MED-19 — A JavaScript runtime for yt-dlp on Windows (P1, M) [WIN] [NET]
**Where.** `ytdlp_update.go`, `ytdownload.go` (`ytDlpJSRuntimeArgs` already
uses a `qjs` next to yt-dlp), `streaming.go` (`streamAttempts`).
**Done when.** JukaHub installs a pinned, checksum-verified runtime
(QuickJS-NG, or Deno ≥ 2.3) next to yt-dlp on Windows; stream attempts prefer
yt-dlp's default clients while it is there; the Patch screen shows its
version.

### MED-24 — A YouTube client table that updates without a release (P2, M) [NET]
**Where.** `youtubenative.go`, `youtubesearch.go`, the update channel.
**Done when.** A small JSON (client name, version, user agent, client ID,
order), fetched at most daily and checked (signature or a pinned hash list), is
cached and used ahead of the built-in table; a bad or unverifiable file is
ignored with a log line.



---

## System and sync (SYS)

### SYNC-1 — Sync: first-sync choice, more save folders, safe launch order (P2, M)
**Where.** `synchub*.go`, `savesync_auto.go`, `gdrive_sync.go`,
`romm_autosync.go`, `romm_server.go`, `ports.go` (`launchLibraryGame`).
**Done when.** A never-synced device's first sync asks per game "Use Drive's
copy / Keep this one / Keep both"; ports' and standalone emulators' save
folders take part; hashing on the launch path is budgeted; a launch waits for
that game's running push.

### SYS-8 — Storage you can clean up (P2, M)
**Where.** `systool_disk.go` (Storage), `spaceuse.go` (What's using space),
`cardtidy.go`, `sdhealth.go`.
**Done when.** Storage shows usage by kind (games, media, downloads, saves,
caches, backups, old versions, trash) with one-press clean-ups and warns at
90 % full; the card's file-system dirty flag is checked at boot.

### SYS-19 — Encrypted, versioned save snapshots on WebDAV (P3, M) [NET]
**Where.** `webdav_sync.go` (WebDAV is a Sync target already, as plain files),
`ssync.go`, `ssyncui.go`.
**Done when.** A WebDAV target can also keep versioned, client-encrypted save
snapshots, restored through the import preview; nothing leaves in plaintext,
an interrupted upload keeps the previous snapshot usable, and a restore takes
a Save Manager snapshot first.

### SYS-21 — Juka and pictures, second round (P3, M)
**Where.** `vision_chat.go`, `vision_video.go`, `chatactions.go`,
`imageviewer.go`.
**Done when.** A Community message's actions offer **Ask Juka** for its picture
(the Discord file is downloaded first); the image viewer sends the picture on
screen to Juka; a video's transcript upload is compressed (FLAC or Opus) when
the firmware's ffmpeg has the encoder.

### SYS-23 — JukaHub OS, second round (P2, M)
**Where.** `jukaos*.go`, `emulators.go` (`deviceRetroArchCoresDir`),
`gamelaunch_firmware.go` (`firmwareROMArg`), `elfcompat.go`,
`ports_catalog.go`, `files_extract_ui.go`, `home_power.go`.
**Done when.** A per-firmware paths layer dir-binds one stable tree (RetroArch
cores, system and saves, `Emus/*/`) so nothing guesses among five roots;
`elfcompat.go` and `ports_catalog.go` also search `JUKAHUB_OS_CHILD_LIBS`;
Files offers **Install as JukaHub OS layer** for a zip with `layer.json`;
driver and value changes apply live where possible; a per-session report lists
what was bound, loaded and skipped; the power card can restart JukaHub OS.

### SYS-25 — Bluetooth, third round (P2, M)
**Where.** `audioroute*.go`, `bt_audiofollow.go`, `bluetooth_devices_ui.go`
(Bluetooth already comes back on at start: `bt_bootswitch.go`).
**Done when.** A headset that connects during a RetroArch game gets its sound
(a BlueALSA-aware ALSA plug, or a resume-safe restart the user agrees to), and
a headset's row and sheet show its battery from BlueZ's `Battery1` when it
reports one.

---

## Network (NET)

### NET-7 — Network drives in Files and for media (P2, M)
**Where.** `nas*.go` (SMB, WebDAV and SFTP drives already feed the Games
library), `files_locations.go`, the music and video libraries.
**Done when.** A network drive can be browsed and copied from in Files, and
used as a read-only music or video source streamed through its `nasFS`.

### NET-15 — FTPS (explicit TLS) for the FTP server (P3, M)
**Where.** `ftp_server.go`, `nettoolsscreen.go`.
**Done when.** `AUTH TLS` with a persisted self-signed certificate whose
fingerprint the sheet shows, `PROT P` data channels, and the sheet recommends
FTPS or SFTP.

### NET-23 — Matter: Apple Home and Google Home accessories without Home Assistant (P2, L) [NET]
**Where.** New `matter*.go` beside `smarthome_hub.go` (a fourth source of the
Smart Home tab), `companion_smarthome.go` (the Apple Home card).
**Why.** Apple Home and Google Home let no other app switch their accessories;
the one open door is Matter multi-admin (Home app › Turn On Pairing Mode, Google
Home › Linked Matter apps & services), which today needs Home Assistant in
between (docs/SMART_HOME.md).
**Done when.** JukaHub commissions a Matter device over IP from its 11-digit
pairing code (PASE with SPAKE2+, its own fabric's root and operational
certificates, CASE) and reads and switches OnOff and LevelControl; SPAKE2+
(RFC 9383), AES-CCM and the Matter TLV certificate encoding pass their
published test vectors in one test file, and a run against connectedhomeip's
Linux `chip-all-clusters-app` in a container commissions it and toggles OnOff.

---

## Sound (SND)

### SND-2 — One audio mixer on the speaker too (P2, M)
**Where.** `audioroute.go` (the `out*` functions and the mixer), `sound.go`,
`bootchime.go`, `ledsync.go` (`audioBus`).
**Done when.** The speaker route uses the same mixer on one stereo device (the
per-output passthrough is gone), and the LED analyser is fed from the mix.

### SND-9 — The game plugin's interfaces against the real headers (P1, S) [NET]
**Where.** `player/spatialfx/jukahub_spatial.h`, `jukahub_spatial.c`,
`README.md`; libretro-common's `libretro_dspfilter.h` and `dsp_filter.c`;
`ladspa.h`.
**Done when.** Every struct, typedef, flag and entry point matches the headers
(or is fixed) and the README says so; then DSP API v2 (RetroArch's int16
`process_i16` path) is decided.

### SND-11 — The game filter on Windows (P3, S) [WIN]
**Where.** `player/spatialfx/build.sh` (`TARGET=windows`), the Windows release
job, `spatial_games.go` (where it looks).
**Done when.** The Windows archive carries the DLL where `spatial_games.go`
looks, and a RetroArch game started from the library on Windows plays through
it.

### SND-14 — Juka's Groq voice, second round (P3, S)
**Where.** `tts_play.go`, `tts_groq.go`, `tts_settings.go`.
**Done when.** The first-word delay is logged (`tts_play.go` logs only
failures) and, if it is the download, the WAV streams into the feeder as it
arrives; a 400 or 404 for the speech model is remembered for the run; a voice
volume slider sits under **Voice**; a second language follows INP-18 where
Groq has a voice for it.

---

## Input and controls (INP)

The hint layer (`inputhints.go`) decides whether hints name the pad's buttons or
the keyboard's keys; these make the rest of the input match it.

### INP-1 — Keyboard parity, screen by screen (P1, M)
**Where.** Every `handle*Key` and its `*FooterHints`; `inputhints.go`
(`hintKeyboardTwin`, the `"pad / key"` badge convention); the "Keyboard keys,
screen by screen" table in `docs/CONTROLS.md`.
**Done when.** For each screen in `docs/CONTROLS.md`, every footer hint is
pressable on a keyboard: either the generic twin is true there or the hint is
written `"<pad> / <key>"`; the table stays in step.
**Verify.** INP-12's checker reports zero mismatches.

### INP-2 — Sentences that name buttons (P1, M)
**Where.** The files the grep lists; `hintSentence` in `inputhints.go`.
**Done when.** Each match goes through `hintSentence` or is rewritten without a
button, and the grep lists only the deliberate physical exceptions: hakuX's exit
chord, "Press A on a controller to join" (hotseat), the one-handed help text and
the `inmap*.go` Select+R1 chord (pad-only: remap, no keyboard words).

### INP-3 — Controller glyph sets (P2, M)
**Where.** `inputhints.go` (a third vocabulary dimension: pad family), SDL
`GameControllerGetType` (go-sdl2 v0.4.40), `gamepad_hotplug.go`.
**Done when.** Xbox, PlayStation (✕ ○ □ △, L1/R1), Nintendo (B/A swapped
positions, L/R, ZL/ZR) and TrimUI families are named correctly in every hint;
Settings → Button hints gains "Pad style: Auto / Xbox / PlayStation / Nintendo".

### INP-7 — Mouse and touch parity (P2, M)
**Done when.** Every footer hint badge is clickable (generalise `emuHintHits`),
right-click is the screen's secondary action (X), the wheel scrolls every list.

### INP-9 — Controller database and HIDAPI on Windows (P2, S) [WIN]
**Done when.** `gamecontrollerdb.txt` (pinned, embedded) is loaded at boot,
HIDAPI hints enable DualSense/DS4/Switch Pro/8BitDo; a "Controller not
recognised — map it" flow uses the controller test (`ctrldiag.go`) to write a
mapping.

### INP-11 — Players and pad status beyond the Bluetooth page (P3, M)
**Where.** `bt_padinfo.go`, `quicksettings.go`, `gamepad_hotplug.go`,
`hotseat.go`.
**Done when.** Quick Settings lists each connected pad with its player number
and battery (Bluetooth from `bt_padinfo.go`, others from
`SDL_JoystickCurrentPowerLevel`, so Windows too); the player LED is set where
the pad has one; hotseat uses the same numbering.

### INP-12 — Hint truth checker (P2, M) [WIN]
**Where.** A `tools/hintcheck` program (or a scenedump mode, TEST-1) that, for
each scene and state, renders the footer in both vocabularies and replays each
hint's key through the scene's key handler with a recording stub.
**Done when.** A `make` target runs it and lists mismatches.

### INP-18 — Voice command in more languages (P2, M)
**Where.** `voicecmd_intent.go` (phrase tables), `voicecmd.go` (the
transcription request); the keyboard layout's language
(`vkCurrentLayout().Lang`) or the UI language as the source.
**Done when.** At least one more language has its phrase table, is picked from
the layout or the UI language, and English stays the fallback.

### INP-19 — Offline voice recognition on Windows (P3, M) [WIN]
**Where.** `stt_local.go`, `stt_install.go`, the release workflow that builds
the pack, `voicecmd.go`.
**Done when.** Decision: the same engines, not System.Speech — a
`JukaHub-OfflineSpeech-windows-amd64.zip` pack is built and published, installs
like the arm64 one, and on a PC without a Groq key Voice command, the orb and
voice typing work offline; with the pack missing they say so and keep the Groq
path.

### INP-22 — The voice orb from a pad on Windows (P3, S) [WIN]
**Where.** `voiceorb.go` (`voiceOrbMenuOwnsGuide`), Settings → Controls (the
"Menu button: voice" row is handheld-only).
**Done when.** An opt-in switch on Windows makes the pad's Guide (or a long
press of Back) open the orb, with the Game Bar caveat in its second line and
`docs/CONTROLS.md`.

---

## Shell (SHELL)

### SHELL-6 — Back and focus that remember (P2, M)
**Done when.** Each scene saves focus + scroll on leave and restores it on
return (bounded map); Home restores the last focused card after a game.

### SHELL-9 — The tip of the day takes A, a click and a tap (P2, S)
**Where.** `helpdoc.go` (`homeTipClick`, `homeTipOpen`), `homefocus.go`,
`homelayout.go`, `home.go` (`handleHomeMouseClick`), `hometabs*.go`,
`cfgdoc.go` (`cfgdocRuntimeKeys`).
**Done when.** The tip is focusable on the Standard, Hero and Tabs Homes (Down
from the last row, Up from the footer); A opens its CONTROLS.md section and X
hides it for the day, a click or a tap the same; `homeTipHiddenDay` is a
runtime key.

---

## Power and battery (PWR)

### PWR-6 — Profiles that follow the situation (P2, M)
**Where.** `powerprofile.go`, `raemu.go`, the game options sheet.
**Done when.** Every launch path goes through `beginLaunchPowerProfile`;
profiles exist per game, per system and per power source (battery / charger),
most specific wins; the game options sheet shows which one applies.

### PWR-12 — Standalone emulators save on exit for JukaHub's launches (P2, M)
**Where.** `sessionsafe_recipes.go`, `racfgscope.go` (the journaled one-launch
edit to copy), `instantresume.go`.
**Done when.** A library launch through DraStic (`enable_autosavestate`),
Flycast (`Dreamcast.AutoSaveState`) or Mednafen (`autosave`) turns the
emulator's save-on-exit on for that launch only, in the config its last run
read, journaled and put back when the game exits; the next start loads that
state where the emulator can be told to; such games then take the plan's Save
step at the battery and power offs instead of the hold.
**Verify.** A test over fixture configs: the edit, the restore after an exit
and after a crash, and a value the player changed during the game kept.

---

## Reliability and updates (REL)

### REL-8 — One reproducible release pipeline with an SBOM (P1, M) [NET]
**Where.** `.github/workflows/release.yml` (tags) and `docker-image.yml` (each
push to main), `.github/scripts/trimui-build.sh`, `player/build.bat`,
`player/release.sh`.
**Done when.** One release workflow (pull requests build but never tag); every
release build uses `-trimpath`, `SOURCE_DATE_EPOCH` and pinned toolchains and
carries an SPDX SBOM and SLSA provenance; asset names match `patch_swap.go`;
release notes come from the tag's CHANGELOG section.

### REL-15 — Watch updates on the JukaHub OS SD image too (P2, M)
**Where.** `player/cmd/jukaboot/launch_linux.go`, `os/smartpro/launch.sh`,
`player/updrescue.go`, `player/startcheck.go`.
**Done when.** jukaboot exports the variable and runs the same watchdog (ready
within the limit, a `SIGABRT` dump, one restore, `.update-rescued`), and
a stub launcher that hangs before its first frame is put back by the same
watchdog.

### REL-17 — Version history says how each version started here (P3, S)
**Where.** `upch_ui.go`, `uhist.go`, `updrescue.go`.
**Done when.** The running version and each saved one show one line from those
records ("Started fine on this Smart Pro" / "Didn't start on this Smart Pro —
not offered again"), with a date only when the clock was plausible.

---

## Languages (I18N)

### I18N-1 — The string catalogue, finished (P1, M)
**Where.** `i18n.go`, `player/i18n/`, the `SizeUTF8` call sites, a check under
`tools/`.
**Done when.** Plurals follow CLDR rules for each shipped language; every
measurement goes through the translated text; the first start picks the OS or
firmware locale when a catalogue exists; a CI check lists untranslated strings
per language; missing translations fall back to English.

### I18N-4 — Input methods (P2, L)
**Done when.** The on-screen keyboard has pinyin and kana composition with a
candidate strip (`vkeyboard.go` already has a suggestion strip); physical
keyboards get `SDL_TEXTEDITING` handling.

### I18N-CJK-1 — Japanese, Korean and Chinese: the tour and line breaks (P2, M)
**Where.** `player/i18n/{ja,ko,zh-CN}.tour.json` (new), `core.go`
(`wrapTextLinesRaw`), `notiftext.go` (`wrapWordsMeasured` already cuts a CJK
run), `uilangface.go`.
**Done when.** The first-run tour has the three catalogues; `wrapTextLines`
breaks a CJK run between characters (never before 、。」 or after 「) instead
of ellipsizing a sentence without spaces; strings added since the catalogues
were written are translated.
**Verify.** A wrap case in `notiftext_test.go`; the catalogue test.

---

## Security and privacy (SEC)

### SEC-1 — Every extraction through `os.Root` (P2, M)
**Where.** `emu7z.go`, `ports_store_install.go`, `ppsspp_managed.go`,
`tts_install.go`, `emulatorupdates.go`, `androidpak.go`, `relpack.go`,
`ssyncbundle.go`, `bkup.go` (restore). `extractZipSafe` and `extractTarGzSafe`
(`patch_engine.go`, `openExtractRoot`) and `pkgExtractZip` are the pattern.
**Done when.** Each writes through an `os.Root` opened on its destination;
traversal tests stay green.

### SEC-6 — Plugin permissions and signing (P1, L) [OWNER]
**Done when.** Manifests declare permissions (network, files outside the
plugin folder, run programs, settings); the install sheet lists them; scripts run
with a minimal environment, a working directory inside the plugin, a timeout and
(on Linux) a dropped-privilege user if the image allows; catalogue plugins are
signed and sideloaded ones say so; an ADR in `docs/adr/` records the sandbox.

### SEC-8 — Secrets that are actually secret (P1, M)
**Where.** `crypto.go`, `retroachievements.go`, `chat.go`.
**Done when.** The key is device-bound (Windows DPAPI; TrimUI: random key in a
`0600` file outside the user-visible tree, created on first run); existing
secrets are re-encrypted; a config copied to another device asks for its secrets
again instead of failing; RA and every API key go through the store; Discord
uses bot or OAuth tokens only.

### SEC-10 — Supply chain in CI (P2, S) [NET]
**Done when.** The weekly govulncheck is blocking for reachable vulnerabilities,
CodeQL runs weekly on Go and JavaScript (`.github/codeql/` is empty; not a new
per-push job), the container image and SDL
tarballs are covered by dependabot or a pinned-digest check.

### SEC-11 — A privacy page that tells everything (P3, S)
**Done when.** Settings → Privacy lists every network feature, what it sends,
where, and whether it is on; "Export my data" writes a zip of every user file.

### SEC-19 — The pairing code never crosses the network in clear (P2, M)
**Where.** `livebuilder.go`, `builder-device.js`.
**Done when.** Requests are authenticated with an HMAC of the method, path, body
hash and a timestamp/nonce keyed by the code (or TLS with a pinned self-signed
certificate shown as a QR), the code itself is never sent, replays are refused,
and tests cover a replay and a stale timestamp.

### SEC-29 — Build the arm64 parts as an unprivileged user (P3, S)
**Where.** `.github/scripts/build-root.sh` (`in_root`, `build`, `check`),
`player/native/build-helpers.sh`, `build-runtimes.sh`.
**Done when.** `build` and `check` run the scripts as a build user (`chroot
--userspec`) owning only the folders they write (`DEPS_PREFIX`,
`/tmp/jukahub-*`, `player/native/bin`); `create` and package installs stay
root; one helper and one Box64 run still pass, with `id` in the log.

### SEC-30 — Startup lock, the remaining paths (P2, M)
**Where.** `peernet.go`, `livebuilder.go`, the Steam OpenID return,
`powersleep.go` and the parked game loop in `main.go`, Reset settings, backup
restore, config history, plugin startup hooks, `bootlock_ui.go`.
**Done when.** While locked, paired devices' sync and hand-off, the Live
Builder and a Steam sign-in are refused; a game paused by sleep stays paused
under the lock; Reset settings, a restore or a config rollback cannot change
the lock without the code; plugin startup actions wait for the unlock; a PC
mouse can click the lock's power choices; the PBKDF2 cost is timed on the A133
(under about a second, else fewer iterations for new hashes).

---

## Apps platform (APP)

### APP-1 — A store worth browsing (P2, M) [NET]
**Done when.** Catalogue entries carry screenshots, a description, size,
licence, source link and dependencies that install first; "Update all"; a
changelog per update; installs stay verified and staged (`pkgstage.go`) and
the catalogue is signed (SEC-16).

### APP-2 — A plugin SDK (P2, M)
**Done when.** `docs/PLUGINS.md` documents a versioned manifest (with the
permissions of SEC-6), a sample plugin lives in `examples/`, and
`JukaHub --new-plugin <name>` (or a script) scaffolds one.

### APP-4 — Files that forgive (P1, M)
**Done when.** Delete moves to a `.trash` per volume (bounded by size and age,
"Empty trash" in the storage view, SYS-8), **Compress to zip** makes an archive
of the marked rows, Files has a filter-as-you-type search, and copy and move
run off-thread with progress (the extraction queue in `files_extract.go` is
the model: one job at a time, a staging folder, a drain on the SDL thread).

### APP-6 — Notifications on the phone companion (P2, S)
**Where.** `companion*.go` (the Games page already lists, starts and takes
saves: `companion_library.go`), `notifications*.go`.
**Done when.** The companion page mirrors JukaHub's notifications, with their
actions where they are safe from a phone, through the existing pairing.

### APP-7 — Notes that travel (P3, S)
**Done when.** Notes sync between paired devices through `ssync` and render
Markdown (headings, lists, checkboxes) in the reader view.

### APP-8 — File uploads in forms (P2, M)
**Where.** `browser.go` (form submission), `webpage.go` (file control), Files
picker, `docs/BROWSER.md`.
**Done when.** A file input opens a bounded local-file picker and sends
`multipart/form-data` with the selected file, while a local test server asserts
the filename, bytes, other fields and cookie round-trip.

### APP-10 — Live bindings between ES modules (P2, M)
**Where.** `webdom_module.go` (references to imported names),
`webdom/85_scripts.js` (`modContext`, the namespaces).
**Done when.** References to an imported name in a module body read the
exporting module's namespace (with scope analysis in the rewrite, so local
variables and property names of the same spelling stay untouched), cycles
evaluate in the standard's order, and a test links a two-module cycle that
reassigns an exported `let`.

### APP-12 — Ranges that follow the document (P3, S)
**Done when.** Live ranges are tracked per document and their boundaries move on
insert, remove, `splitText` and `normalize` as the DOM standard says, covered by
the jsdom comparison in `tests/web/webdom_dom.test.mjs`.

### APP-15 — 7z extraction without 7-Zip (P3, M)
**Where.** `sevenzip.go` (reads 7z directories and decodes Copy, LZMA, LZMA2
and the ARM filters in-process), `files_extract.go`, `emu7z.go`.
**Done when.** Files extracts a 7z whose coders `sevenzip.go` knows (plus the
x86 BCJ filter, to add) in-process through the same staging and `os.Root`
rules, with fixtures made by 7-Zip in the tests; other coders (BCJ2, PPMd,
AES) still go to 7-Zip.

### APP-19 — A pinned checksum for WestonPack (P2, S) [NET]
**Where.** `windows_runtime_install.go` (`installWestonRuntime`,
`windowsWestonRuntimeURL`).
**Done when.** The download URL names a PortMaster-New commit instead of `main`,
the file's SHA-256 is pinned beside it (from portmaster.games' runtimes page,
PortMaster-New's `.md5` files or harbourmaster's runtime index), and
`wineDownload` checks it before the rename.

### APP-22 — Typing in Windows programs (P2, M)
**Where.** `vcursor_bridge_linux.go` (the "JukaHub Mouse" uinput device, which
already declares keys 1–255), `companion*.go`.
**Done when.** Text typed on the phone companion (or picked letter by letter
with a pad chord) reaches the running program as key presses through the
JukaHub Mouse device, and the pad is the mouse again afterwards.

---

## Windows desktop (DESK)

### DESK-2 — Sharp on high-DPI screens (P1, S) [WIN]
**Done when.** Per-monitor-v2 awareness (manifest in `JukaHub.rc` +
hint), rendering at native pixels with the logical canvas scaled by SDL; text
is crisp at 150% and 200%.

### DESK-3 — Windows that remember (P2, S) [WIN]
**Done when.** Size, position, monitor and maximised state persist; a
borderless-window mode exists; the monitor can be chosen (the only window is
`sdl.CreateWindow` in `main()`, which names no display); the fullscreen cleanup
lands first.

### DESK-4 — An installer and package-manager listings (P2, M) [WIN] [NET]
**Done when.** An Inno Setup (or MSIX) installer, winget and Scoop manifests
generated by the release workflow, VERSIONINFO from the version in
`jukaconfig.json`; the portable zip remains.

### DESK-12 — Emulator downloads on a PC that resume and are checked (P2, S) [WIN]
**Where.** `emuautoupdate_desktop.go`, `emuwininstall.go`, `emulators.go`.
**Done when.** A cut-off emulator download resumes where it stopped, and
`RetroArch.7z` is checked against a second channel before it is installed.

---

## Web builder (WEB)

### WEB-1 — Split `script.js` into modules (P1, L)
**Done when.** ES modules by concern (canvas, inspector, scenes, export/import,
validation, gallery, AI panel), the CSP still holds (`_headers`), and the jsdom
+ Playwright suites pass unchanged.

### WEB-3 — Focus-order editor (P2, M)
**Done when.** The builder shows and edits D-pad order per scene, exported in a
form `focus.go` reads.

---

## Waiting on Juka (OWNER)

Each needs something only the owner can give — an account, a key, a secret,
a person — before an agent can finish it.

### REL-13 — Turn on the bug-report service (P1, S) [OWNER] [NET]
**Where.** Netlify → generate.jukalang.com → Environment variables;
[docs/BUG_REPORTS.md](../docs/BUG_REPORTS.md).
**Done when.** `JUKAHUB_GITHUB_TOKEN` (a bot account's token with Issues
read/write on jukaLang/JukaHubV2) is set and deployed; `curl -i` on the address
answers 405 with `"configured": true`; a report from a device becomes an issue
with the `from-device` label and the sent sheet shows its number.

### REL-3 — Signed updates only (P1, M) [OWNER]
**Done when.** CI signs every release manifest with a production key held as a
secret (ceremony in `docs/UPDATER_TUF.md`), listing every asset including the
`JukaHub-RemotePlay`, `JukaHub-AndroidRT` and `JukaHub-Wine` zips (the helper
installer and `relpack.go` check them against it); the app embeds the key plus
one rotation slot; an unsigned or expired manifest is refused with a clear
message (sideload keeps its explicit trust prompt).

### SEC-16 — Signed package catalogue, hashes mandatory (P1, M) [OWNER] [NET]
**Where.** jukaLang/Packages (`packages.json`), `pkgverify.go`
(`pkgApplyIntegrity`), `pkgstate.go` (`pkgCatalogFetcher`), `patch_signed.go`,
`docs/PACKAGES.md`.
**Done when.** The registry's CI writes `sha256` and `size` for every entry and
a detached Ed25519 `packages.json.sig` with the production key (REL-3); the
store verifies it on fetch (offline keeps the last verified list); once every
entry has a hash, one without is refused and "Not verified" goes; a test feeds
a tampered list and a missing hash.

### DESK-5 — Code signing (P2, S) [WIN] [OWNER]
**Done when.** Release executables and the installer are Authenticode-signed
(Azure Trusted Signing or SignPath's free OSS programme) so SmartScreen stops
warning.

### I18N-5 — Human translations (P2, M) [OWNER]
**Done when.** A translation platform (Weblate or Crowdin, free for OSS) is set
up on the catalogue; Spanish, Portuguese (Brazil), Russian, German and French
are reviewed for Home and Settings; Japanese, Korean and Chinese (Simplified),
machine-translated on 2026-11-24 (about 1,070 strings each), are reviewed by
speakers.

### I18N-UK-1 — The Ukrainian catalogue, finished (P3, M)
**Where.** `player/i18n/uk.json`, a new `player/i18n/uk.tour.json`.
**Done when.** Every key of `ru.json` and `ru.tour.json` has a Ukrainian value
written or checked by a Ukrainian speaker, and the catalogue test passes.

---

## Ideas parking lot

Not tasks yet: each needs a *Why* and a *Done when* before it moves up. Sizes
are rough.

- **Theme from an image** (M): median-cut palette into the 16 theme roles, on
  `themeloader.go`.
- **Seasonal themes by date** (S): the time-of-day hook exists; add a date
  table.
- **Toys** (S–M each): time capsule / sealed notes, secret modes, playable
  credits, virtual pet, retro OS skin pack, arcade kiosk.
- **Save forensics** (M): list + diff two snapshots.
- **Randomizer suite** (L): start with an IPS/BPS patch applier.
- **Speedrun timer/splits** (M): no auto-split (needs memory reads).
- **LAN arcade leaderboard** (S–M) for JukaHub's own games.
- **Bigger projects**: Steam Deck pairing (L), Juka playground (M), fantasy
  console (XL). (Paint already draws pixel art.)
- **Per-game colour filters** (S, muOS has them): a colour matrix per game on
  top of the accessibility palettes.
- **"Play style" profile** (S): when and what you play, from the playtime
  session log.
- **Tabs Home extras** (S each): an unread dot on the tab that holds Chat; a
  per-tab accent tint of the strip's pill; each tab's scroll kept across
  restarts; hold A on the strip to arrange; a crossfade between tabs under
  Reduce motion.
- **Per-game hakuX settings** (S): once the package's per-title settings layout
  is known, the game options sheet opens it for Xbox games.
- **Event webhooks** (S): opt-in POSTs for game launched / trophy unlocked /
  install finished (bounded queue, no secrets in payloads).
- **Community widgets** (M): a widget for Home's Widgets layout
  (`home_widgetdraw.go`) as a sandboxed JSON descriptor (text or a number from a
  local source, refresh interval) delivered as a package; no code.
- **Ghost Challenges for RetroArch games** (L): the arcade games race a
  friend's run since 2026-11-24 (`ghost.go`); for an emulated game, record
  RetroArch's input replay (`-R` / `-P`, a BSV file that starts from a save
  state) with the ROM's CRC and the core's name and version, send it the same
  way, and race on the same core and ROM; replays only hold for the same core
  build, core options and ROM, so the challenge card has to check all three.
- **Pocket Studio** (L): make a tiny playable game with the
  D-pad (a tile map, a sprite, a few rules) and share its cartridge with
  another JukaHub; a single-file cart (like the fantasy consoles' picture
  carts) and the Nearby devices transfer are the natural carriers.
- **Kodi remote** (S–M): drive a Kodi box on the home network over its
  JSON-RPC API (play, pause, seek, browse) from the Media screen.
- **Xbox 360 digital games** (S): list STFS packages under `Roms/XBOX360`
  (no extension; `LIVE`, `PIRS` or `CON ` magic), titled from the package
  header, for Xenia Canary.
- **Package split, step 4** (XL): chat, Wi-Fi, party and peers, JukaLand,
  minigames, smart home and RetroAchievements each behind a small interface to
  `main`, one task per island, once the earlier split steps have landed.
- **Android class packs on demand** (M): SDL2's Java glue
  (`org/libsdl/app/SDLActivity`) and Unity's `UnityPlayer`, when real
  compatibility reports ask for them.
- **Terraria through FNA** (M): a PortMaster-style recipe that runs the PC
  version with FNA and Mono on aarch64 from files the user copies.
- **A built-in comic reader** (M): CBZ/CBR/CB7 with fit width or height, a
  two-page spread and right-to-left; "Open with KOReader" covers them today.
- **Sound packs** (M): chime and UI-sound variants as a package category, as
  scores and constants rather than samples.
- **Builder element factory** (M): the web builder's element markup from one
  component factory instead of per-type assembly.
- **Clock faces** (M): analogue, digital and minimal faces for the screensaver
  and the dock, with OLED-safe pixel shift, as a theme token.
- **Right-to-left layout** (L): a mirrored Home grid, lists and footers with
  `golang.org/x/text/unicode/bidi`, once an Arabic or Hebrew catalogue exists.
- **Discord Rich Presence on a PC** (S): opt-in, over Discord's local IPC pipe;
  no account tokens.
- **A motion system** (M): enter, exit, focus and sheet transitions on
  `motion.go`'s curves everywhere, honouring reduced motion and the Animations
  speed, with one entrance for the Start menu, sheets and toasts.
- **Battery estimates per activity** (M): a learned drain rate per activity in
  `battery_eta.go`, and a stream's rate from the Remote Play helper's status
  file.
- **Recent screens switcher** (S): hold Start (Alt+Tab on a keyboard) for the
  last five screens as cards.
- **TV guide** (M): XMLTV now/next for M3U playlists that name one (`url-tvg`).
- **Move to a new device** (M): Send my settings also carries the wallpaper,
  the theme files and the installed package list, reinstalled in one flow.
- **Import from the stock launcher** (M): MainUI favourites, recents and
  playtime, and muOS collections, offered at the first start.
- **Discrete 5.1 and 7.1 under spatial audio** (M): each channel from its own
  virtual loudspeaker (`spatial_dsp.go`), with golden vectors.
- **Add JukaHub to Steam** (S): a non-Steam shortcut with art from `branding/`,
  and Steam Input's virtual pad as the hint vocabulary.
- **Open with JukaHub** (S): files and `--scene` on the command line,
  registered by the installer (DESK-4).
- **Windows toasts** (S): downloads, alarms and updates as Windows
  notifications while minimised (the AUMID comes with the installer).
- **Keyboard profiles** (M): key-to-action bindings in the control-profile
  engine (`inmap*.go`), read by the hint layer.
- **The builder as a PWA** (M): a service worker with a versioned cache and a
  "new version" prompt.
- **Verified dumps** (M): No-Intro and Redump DATs as a package, background
  CRC/SHA-1 checks, Verified / Unknown / Bad dump badges.
