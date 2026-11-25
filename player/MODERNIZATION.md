# JukaHub engineering backlog

Open tasks: **169**.

Open work for the player, web builder and AndroidRT, plus an ideas parking lot.
Finished work lives in the [changelog](../CHANGELOG.md) and the feature docs.

Only work that can be finished and checked without a person at the device is
tracked here (builds, tests, static checks, scripted probes, screenshots an
agent can read). Device work is recorded in the
[hardware validation log](../docs/HARDWARE_VALIDATION.md).

---

## Format

Each task is `### ID — title (priority, size) [tags]`, followed by:

- **Where.** the files to change.
- **Done when.** the acceptance criteria.
- **Verify.** how to check it (tests, build, static checks, scripted probes).

P0 blocks a release; P1 is current user pain; P2 planned value; P3 polish.
S is at most half a day, M at most two days, L at most a week; split XL.

When a task is finished: delete it, add the change under `[Unreleased]` in the
changelog, and add a follow-up task if anything is left open.

Constraints: the 1 GiB Smart Pro stays responsive (no blocking or per-frame
allocation); use the shared design and input-hint helpers; log with
`logDebug`/`logInfo`/`logWarn`/`logError`, never `log`; test data integrity,
security, installers and external parsers; keep UI and timing tests lean. See
[development](../docs/DEVELOPMENT.md) and [contributing](../CONTRIBUTING.md).

---

## P0 — build

### BUILD-1 — Windows build and `make check` green on one revision (P0, S) [WIN]
**Where.** `player/build.bat`, `Makefile`.
**Done when.** `player\build.bat nolaunch` and `make check` (vet, player tests,
web tests, workflow checks, backlog check) pass on the same source revision.
**Verify.** The clean `make check` tail.

### BUILD-6 — The Box64 pack reaches a release (P1, S) [NET]
**Where.** `.github/workflows/wine-runtime.yml`,
`.github/workflows/docker-image.yml` (`build-wine-runtime`,
`attach-wine-runtime`), `player/native/build-runtimes.sh` (`wine`,
`ALLOWED_NEEDED`), `.github/scripts/build-root.sh`.
**Done when.** A push's release carries `JukaHub-Wine-linux-arm64.zip` whose
BUILDINFO shows only `ALLOWED_NEEDED` libraries, no symbol newer than GLIBC 2.31
and `box64 --version` naming 0.4.4; the advisory step prints `wine-10.0` or its
log names the next fix; the next push with unchanged inputs says "Reused … from
run N".
**Verify.** The run's "Box64 (the Windows-app runtime)" step summary and the
release's asset list.

### SYNC-1 — Save sync: first-sync choice, more save folders, safe launch order (P2, M)
**Where.** `savesync_auto.go`, `gdrive_sync.go`, `romm_autosync.go`,
`romm_server.go`, `ports.go` (early return in `launchLibraryGame`).
**Done when.** A never-synced device's first sync asks "Use Drive's copy / Keep
this one / Keep both" per game (with the comparison card's per-file choice);
ports' and standalone
emulators' save folders take part; hashing on the launch path is budgeted; the
409 text is right; a launch waits for that game's running push.

### MAIL-2 — Calendars that write back (P2, L) [NET]
**Where.** `calsync_caldav.go` (PUT/DELETE of a VEVENT with If-Match),
`mail_msauth.go` (a Graph `Calendars.ReadWrite` scope), `calendarpopover.go`.
**Done when.** The clock's calendar can add an event (title, day, time) to a
CalDAV calendar or a Microsoft account's calendar through Graph, edit and
delete one it shows, and a conflicting ETag reloads instead of overwriting.

### MAIL-3 — Push mail with IMAP IDLE (P2, M)
**Where.** `mail_imap.go`, `mail_sync.go`.
**Done when.** With JukaHub in the foreground and the server advertising IDLE,
one connection per enabled account waits in IDLE (renewed every 25 min,
dropped on sleep and restored on wake) and a new message notifies within
seconds; polling remains the fallback.

### MAIL-4 — HTML mail through the browser engine (P3, M)
**Where.** `mail_ui.go`, `mail_mime.go`, the browser engine (`webdom*.go`,
`browser.go`).
**Done when.** A message with an HTML part opens in the reader through the
browser engine with remote images off by default (one "Show images" press),
scripts never run, links open in the Browser, and the plain-text view stays
one press away.

---

## Defects found by reading (FIX)

Found by reading, not by running: confirm each on a build before fixing, and
delete the task with the fix. Security defects are under SEC.

### FIX-14 — Release pipelines disagree (P1, M) [NET]
**Done when.** One release workflow (see REL-8); PRs build artifacts but never
tag.

### FIX-15 — The handoff pull and the Sync job slot (P2, S)
**Where.** `hoff.go` (the pull before a continued game starts,
`ssyncPullForGame` in `ssync.go`).
**Done when.** The hoff pull takes Save sync's job slot or waits for it, so it
never writes the same files as a sync started on the Sync page or by Automatic
sync (`synchub_peers.go`). Full Save sync already leaves one-sided files whose
name differs only in case alone and lists them (2026-11-24).
**Verify.** Reading: no path in `hoff.go` writes saves without holding the slot.

---

## Performance (PERF)

### PERF-3 — Dirty-frame presentation (P1, M)
**Where.** `main.go` loop, `adaptive.go`, toast/notification, download and clock
code paths.
**Done when.** A frame is drawn and presented only when something changed
(input, scene/overlay change, toast, progress tick, clock minute, video frame,
animation, network status) plus a 1 Hz safety redraw; no visible staleness.
**Verify.** A unit test around the loop's redraw decision passes: an idle
screen asks for at most one frame a second, and input, a scene change, a toast,
a progress tick or a clock minute asks for one at once.

### PERF-7 — Trim heavy dependencies (P2, L) [WIN]
**Where.** goja: the Browser's DOM pass (`webdom_runtime.go` with the embedded
`webdom/*.js`, `webdom_parse.go`, `webdom_module.go`, `webdom_net.go`, the
legacy pass in `webscript.go`); gopsutil: `processes.go`, `systool_disk.go`,
`systool_hw.go`, `terminal.go`, `textbrowser.go`, `plugnative.go`; zeroconf:
`textbrowser.go`, `moonlight_discovery.go`; discordgo: `chat.go`.
**Done when.** goja and the DOM pass sit behind a `//go:build !nojs` split (in
the lite build a stub `webJSRunPageFor` returns the page as it arrived and
Settings → Browser → JavaScript is hidden); gopsutil is replaced by `/proc` +
`statfs` on Linux and two Win32 calls; zeroconf by a one-shot mDNS query that
still finds Moonlight hosts; `go mod tidy && go mod vendor` run; features
unchanged.
**Verify.** Binary size and cold-cache build time before/after in
`docs/PERFORMANCE.md`; `make check` green.

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

### PERF-13 — Binary size budget (P3, S) [WIN]
**Done when.** Size per package measured (`go tool nm -size -sort size`); the top
10 contributors listed in `docs/PERFORMANCE.md` with a plan; `-trimpath` added to
release builds.

---

## Graphics and GPU (GPU)

### GPU-2 — Fewer render-target switches (P2, M)
**Where.** `shapeCache.go`, `dynamicbg.go` (`bgGrads`), `iconatlas.go`,
`warmSceneCaches` in `bootwork.go`.
**Done when.** Bakes are batched (one target switch per cache per frame, or
software surfaces uploaded once), and a scene change on the device has no frame
> 50 ms caused by baking.
**Verify.** framestats histogram during scene changes before/after.

### GPU-3 — Geometry instead of textures for flat shapes (P3, M)
**Where.** `shapeCache.go`, `ui_glass.go`, `backgroundfx.go`. `liquidbg.go`
(the liquid background) is the first `RenderGeometry` user: reuse its
allocation-free vertex/index pattern and its fall-back to the plain path when
the renderer refuses geometry, and measure before converting more.
**Done when.** Backdrop glows and glass fills draw through geometry where that
is cheaper (measured), with the texture path kept as fallback for the software
renderer.

---

## Architecture and build (ARCH)

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

### ARCH-4 — Package split, step 4: feature islands (P3, XL) [WIN]
**Where.** chat (Discord), Wi-Fi, party/peer, JukaLand, minigames, smart home,
RetroAchievements.
**Done when.** Each island talks to `main` through a small interface; split into
one task per island before starting.

### ARCH-5 — Shrink the main loop (P2, L) [WIN]
**Done when.** The loop body is a short sequence of named phases (input, update,
draw, present, pace) in their own files; `core.go` is split by responsibility
(render helpers, triggers, video entry points) without behaviour change.

### ARCH-6 — Context cancellation for the remaining feature fetches (P2, M)
**Where.** Bare GETs: `thumbstore.go:91` (`httpClientLong.Get`),
`chat.go:215,396`, `themecatalog.go:121`, `tools.go:764`; plus the files that
still build requests with `http.NewRequest` instead of `NewRequestWithContext`
(grep). `scenetasks.go` is the pattern.
**Done when.** Every scene-owned fetch runs under the scene context.
**Verify.** Leaving Podcasts, Weather or Library art mid-fetch closes the
request (server-side observation in a temporary test).

### ARCH-7 — Retire the 0.4.0 migrations on 2027-05-01 (P3, S)
**Done when.** Each shim carries a `// remove after 2027-05-01` comment now; on
or after that date the shims are removed with their tests and `make check` stays
green.

---

## Tests and CI (TEST)

CI runs two jobs on a push (`ci.yml`) and the slow checks weekly (`weekly.yml`);
keep it that small: new checks go to `weekly.yml` or to `make` targets.

### TEST-1 — A scene-dump tool, not a test (P1, M) [WIN]
**Where.** New `player/tools/scenedump/` (a `go run` program reusing the player
package through an exported entry point or a build tag).
**Done when.** `go run ./tools/scenedump -out tmp/scenes` writes every config
scene and the main Go pages at 1280×720 and 960×720 (SDL dummy driver, software
renderer) in under a minute.
**Verify.** The PNGs exist and look right; `docs/DEVELOPMENT.md` documents it.

### TEST-2 — Tests for the riskiest paths the cut left uncovered (P1, M)
**Done when.** One fast table test each for: the self-update restart handshake
(`patch_swap.go` markers ↔ `launch.sh` protocol), browser cookie "forget" taking
the `.bak` with it (`browsercookies*.go`), and the Wi-Fi supplicant socket
transport (`wifi_ctrl.go`, stubbed socket). (`isLaunchableAppPath` is covered by
`paths_test.go`.)
**Verify.** `go test -run` for the three; total suite time stays under ~10 s.

### TEST-8 — Fuzz the parsers (P2, M)
**Where.** `weekly.yml` (`fuzz`, CGO off — parsers in package main need a CGO
job or a move to `internal/`, see ARCH-3), the parser files.
**Done when.** `go test -fuzz` targets exist for M3U, CUE/GDI/M3U sheets, the
PDF and EPUB readers, zip/tar extraction (`extractTarMembers`), package
manifests, `/proc/bus/input/devices` and the XML config; the weekly checks run
each for 30 s and keep the corpus.

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

### TEST-19 — Upload before the advisory steps, bound the weekly smoke, fix the cross-build docs (P2, S) [NET]
**Where.** `.github/workflows/remote-play-helpers.yml`,
`.github/workflows/wine-runtime.yml`, `.github/workflows/weekly.yml`,
`docs/BUILDING.md`, `player/native/build-runtimes.sh` (usage text).
**Done when.** Each package's upload and keep steps run right after its build,
before the advisory step; the weekly smoke step has `timeout-minutes: 5`; the
docs say a cross build needs a sysroot whose glibc is no newer than 2.31.

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

### UX-15 — A motion system (P3, M)
**Done when.** Enter, exit, focus and sheet transitions use the curves in
`motion.go` everywhere, all honour reduced motion and the Animations speed
(Comfy / Snappy, `motionspeed.go`, which today scales scene
fades, overlay entrances and the focus glide), and the Start menu, sheets and
toasts share one entrance.

---

## Games and emulation (GAME)

### GAME-3 — Named checkpoints for RetroArch games (P2, M)
**Where.** [docs/CHECKPOINTS.md](../docs/CHECKPOINTS.md) (Phases 0 and 1);
`savestate_resume.go`, `gmeta_states.go`, `savemanager.go` (snapshot format),
`game_options_ui.go`.
**Done when.** Phase 0's numbers (state size per core, free RAM during a
RetroArch session) are in the design doc; the options sheet's **Checkpoints**
row pins the auto state after a clean exit under a typed name (manifest with a
SHA-256 per file under `save-manager/checkpoints/<game>/`), and lists, restores
(verified, before launch), renames and deletes checkpoints; other launchers get
a one-line explanation. Suspended emulators (Phase 3) stay out.

### GAME-4 — Per-game controller remaps for RetroArch (P2, M)
**Where.** `gameoptions.go`, `game_options_ui.go`, `inmap*.go`.
**Done when.** The game options sheet writes a RetroArch per-content remap file
(and `--appendconfig` where needed) from a small editor; standalone emulators
are explicitly out.

### GAME-6 — Download RetroArch's shader and overlay assets (P3, S) [NET]
**Where.** `videofilter.go`, `ratuning.go` (overlay folders), `cheats_db.go`
(the buildbot download pattern to copy).
**Done when.** The picture and overlay rows offer a download when nothing is
found: libretro's buildbot `shaders_glsl.zip` and `overlays.zip` (the host
`cheats.zip` comes from) download over HTTPS with progress and cancel, unpack
into the folders RetroArch's config names, and the rows list them afterwards.

### GAME-8 — Rich metadata provider (P3, M) [NET] [OWNER]
**Done when.** Descriptions/genres/dates come from a provider with an account
(ScreenScraper or TheGamesDB) behind the existing downloader, opt-in, with the
key stored through the secret store.

### GAME-10 — Netplay: invites, passwords, plain RetroArch hosts (P3, S)
**Where.** `netplay_rooms.go`, `netplay_ui.go`, `companion.go`.
**Done when.** A hosted game can be shared as a link/QR the phone companion
forwards to a friend's JukaHub; a password-protected online room asks for the
password (vkTarget, submit-only) and passes it via the host overlay's
`netplay_password`; RetroArch's own LAN discovery reply on UDP 55435 is parsed
so a PC running plain RetroArch is listed too (read the ad-packet layout from
RetroArch's `netplay_private.h` first; it changed in 1.10).

### GAME-13 — Per-game environment, and options that survive a rename (P2, S)
**Where.** `gameoptions.go`, `game_options_ui.go`, `game_library.go` (where
`gameOptionsFor` feeds the launch).
**Done when.** The options sheet edits environment variables for one game
(validated names, bounded like `Args`) and the launch passes them to RetroArch
and standalone emulators; options under a path that no longer exists are
re-attached to the same content (size and hash) after a rename or move; a test
pins argument order and that an empty override changes nothing.

### GAME-19 — The game assistant, second round (P3, M)
**Where.** `gameassist.go`, `gameassist_linux.go`, `voiceorb.go`.
**Done when.** A follow-up in the overlay keeps the last exchange; the D-pad
scrolls a long answer; launches from Apps and ports are named in the prompt; on
a PC **Juka can see the game screen** captures the game's window, and the answer
shows in an always-on-top overlay.

### GAME-23 — FreeCell in Solitaire (P3, M)
**Where.** `solitaire.go`, `solitaire_ui.go` (`solLayout`, `solTopRow`,
`solColumnOf`, `solTopAt` and the `solK*` constants are Klondike's).
**Done when.** A FreeCell variant (4 cells, 4 foundations, 8 columns, all 52
cards face up, 7/7/7/7/6/6/6/6) is chosen from a Game row in the menu; deals
are numbered with Microsoft's generator so deal 1 matches Microsoft FreeCell's
#1; runs obey the supermove limit, (free cells + 1) × 2^(empty columns),
halved onto an empty column; the smart move may use a free cell last;
auto-finish knows FreeCell; statistics are kept per variant (save version 2,
version 1's block read as Klondike's), with a Reset statistics row that asks
first.
**Verify.** A saved FreeCell game resumes exactly; a version-1 save still loads
with its Klondike statistics.

---

## PS Remote Play (PSR)

### PSR-8 — Remote Play through PSN, without port forwarding (P2, L) [NET]
**Where.** `psn_signin.go` (keeps only the Account-ID today), `psremote*.go`
(consoles from the PSN device list, a "through PSN" stream),
`native/chiaki-stream.c` (chiaki-ng's `lib/include/chiaki/remote/holepunch.h`:
session init, UPnP discovery, session create and start with the console's device
id, the control and data holes, handed to the session through
`ChiakiConnectInfo`; chiaki-lib at the pinned commit already builds the curl and
miniupnpc it uses). Read that header at the pinned commit before writing a call.
**Done when.** The PSN refresh token is kept only after the user turns on **Play
away from home**: encrypted with the player's key like the other API tokens
(`EncryptAPIToken`), never logged or put in a report, deleted on sign-out. A
paired console then streams from another network with no port forward, the token
renews itself, a refused or expired token says so, and the away-from-home
section of the docs describes it. Nothing changes for someone who never signs
in.

---

## Xbox and Xbox 360 (XBX)

hakuX runs original-Xbox games on the Smart Pro; on Windows, xemu runs them and
Xenia Canary runs Xbox 360 games (both installed and updated from the Emulators
page).

### XBX-3 — Reproducible hakuX build (P3, M) [NET]
**Done when.** If the port documents its build (TrimUI SDK or
`ghcr.io/loveretro/tg5040-toolchain`), `.github/workflows/hakux.yml`
(workflow_dispatch, pinned tag) builds the package and attaches its SHA-256. Do
not guess build steps.

### XBX-7 — Xenia Canary on a Windows PC (P2, S) [WIN] [NET]
**Where.** `emuwininstall.go` (the Xenia Canary spec, `emuWinHasVCRuntime`),
`emuxbox.go` (`emuStandaloneGameArgs`), `emusystems.go`, `game_library.go`.
**Done when.** On a Windows PC the newest Canary release installs
(`xenia_canary_windows.7z`; `xenia_canary.exe` found at the top or one folder
down), the manifest's tag is the release tag, a later build shows as an update
and R1 restores the one before, an `.iso` in `Roms\XBOX360` starts full screen
from the Games Library (`--fullscreen=true`), as do an extracted game (one
entry under its folder's name, with its own cover) and a `.zar`, and the Visual
C++ hint shows only
on a PC without the redistributable (also on Windows on ARM).

### XBX-8 — xemu on Windows, end to end (P2, S) [WIN] [NET]
**Where.** `emuwininstall.go` (the xemu rules), `emuxbox.go` (`emuXemuScan`,
`emuXboxApply`, `emuStandaloneLaunchCheck`), `game_library.go`.
**Done when.** v0.8.139 or newer installs the non-debug build (the ARM64 one
from a windows/arm64 JukaHub), the HDD image lands in `BIOS\xbox`, BIOS files
dropped into that folder turn green on the open sheet within seconds, the
library refuses an ISO with the "xemu needs …" sentence until they are there and
then starts it full screen with `-dvd_path`, and a portable xemu whose
`xemu.toml` names its files by relative path launches with that file unchanged.

---

## Android games (AND)

### AND-4 — Text input for Android games (P2, M)
**Done when.** `showKeyboard` opens JukaHub's on-screen keyboard over the game
(IPC runner ↔ hub) and the text reaches the game through its natives.

### AND-7 — Class packs by demand (P3, M)
**Done when.** SDL2's Java glue (`org/libsdl/app/SDLActivity`) and Unity's
`UnityPlayer` packs exist, built from real compatibility reports.

### AND-10 — Terraria through FNA with the user's PC files (P3, M) [NET]
**Done when.** A package recipe (PortMaster-style) runs the PC version with FNA +
Mono on aarch64 from files the user copies; docs explain where they go.

### AND-13 — Auto's window fallback adopts a context-first game's context (P3, M)
**Where.** `androidrt/src/eglpresent.c` (`pr_eglMakeCurrent`,
`pr_eglCreateContext`, `pr_eglDestroyContext`), `tests/smoke`
(`EGLTEST_CONTEXT_FIRST`).
**Done when.** A raw context that has never been current (so holds no GL
objects) is mapped to SDL's context at its first `eglMakeCurrent` with SDL's
surface (destroy and share handled), one that was current with a pbuffer stays
raw with today's log, and the window-fallback context-first smoke case asserts 5
`SDL_GL_SwapWindow` frames.

### AND-14 — The AndroidRT package: strict loader check and reproducible zip (P2, S) [NET]
**Where.** `player/native/build-runtimes.sh` (`build_androidrt`, `make_zip`),
`player/androidrt/build-tsp.sh`, `player/androidrt_install.go`.
**Done when.** `build-runtimes.sh` refuses a `loader --version` answer other
than `AndroidRT loader <version> (aarch64, bionic loader)`, as the device's
probe does; the version is stamped from the last commit that touched
`player/androidrt` or the runtime build scripts, BUILDINFO's `built:` line is
that commit's date, and the tree is `touch -d`'d to it before `zip -X`, so a
rebuild from unchanged sources gives a byte-identical zip.
**Verify.** Two runs on unchanged sources print the same SHA-256 for
`JukaHub-AndroidRT-linux-arm64.zip`.

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
**Done when.** Screenshots open as a gallery with one section per game, newest
first: JukaHub's own captures and RetroArch's are grouped by the game named in
the file name, anything else under "Other"; A opens a picture full screen and
L1/R1 step between games.

### MED-6 — Subtitle style and language preferences (P2, M)
**Done when.** A preferred audio and subtitle language picks the matching
tracks in any new video, and subtitles can be styled (size, background).

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

### MED-11 — Comics (P3, M)
**Done when.** CBZ/CBR/CB7 open in a reader with fit width/height, two-page
spread and right-to-left (manga) mode, remembering the page.

### MED-13 — TV guide (P3, M) [NET]
**Done when.** XMLTV EPG for M3U playlists that name one (`url-tvg`), shown as
now/next in the channel list.

### MED-15 — Search, tracks and folders in the music library (P3, S)
**Where.** `music_shelf.go`, `mlib.go`, `musicplayer.go`.
**Done when.** The music library has Tracks and Folders tabs with natural sort,
and a search (on-screen keyboard) over title, artist and album.
**Verify.** A benchmark over a generated 2,000-track index answers a search
query without a visible stall, and unit tests cover the Tracks and Folders
ordering.

### MED-18 — Trim, rename and delete a recording (P2, M)
**Where.** `recordings.go` (`scanRecordings`, `loadRecordings`), the scene that
draws them (`core.go` callers), the video player for the trim preview.
**Done when.** A recording can be renamed and deleted after a two-press
confirmation (moved to the APP-4 trash once it lands), and trimmed to in/out
points with ffmpeg stream copy where the codec allows it (re-encode otherwise),
keeping the original until the trimmed file plays; the list shows duration and
size.
**Verify.** An integration test trims a generated clip with ffmpeg stream copy,
checks the result with ffprobe, and asserts the original is intact until the
trim is confirmed.

### MED-19 — A JavaScript runtime for yt-dlp on Windows (P1, M) [WIN] [NET]
**Where.** `ytdlp_update.go` (the download, SHA-256 check and swap to copy),
`patch_engine.go` (`helperToolEnv` already puts the tools folder first on
PATH), `streaming.go` (`streamAttempts`).
**Done when.** JukaHub installs pinned, checksum-verified Deno (≥ 2.3) next to
yt-dlp on Windows; yt-dlp is pointed at it (`--js-runtimes`), stream attempts
prefer yt-dlp's default clients while it is present, and the Patch screen shows
its version.
**Verify.** On Windows a video resolves through the default clients with Deno,
and still plays through format 18 without it.

### MED-24 — A YouTube client table that updates without a release (P2, M) [NET]
**Where.** `youtubenative.go`, `youtubesearch.go`, the update channel.
**Done when.** A small JSON (client name, version, user agent, client ID,
order), fetched at most daily and checked (signature or a pinned hash list), is
cached and used ahead of the built-in table; a bad or unverifiable file is
ignored with a log line.

### MED-38 — WebP, second round: chat pictures and covers without a refresh (P3, S)
**Where.** `webpdecode.go` (`webpDecodeImage`, `webpCoverWorker`), `chat.go`,
`game_library_ui.go` (`decodeLibraryArt`).
**Done when.** A local .webp in Chat draws as a picture on the stock firmware;
a converted cover shows without a library refresh (the open library rebuilds
its art paths once when the worker drains, or `decodeLibraryArt` uses the
viewer's ffmpeg fallback).

### MED-39 — Audiobook chapters, second round (P3, S)
**Where.** `musicskins.go` (`renderMusicStockOS`), `musicplayer.go` (row hits,
`ctrlMusicPlayer`), `music_shelf.go`, `audiobook_chapters.go`.
**Done when.** Both layouts list the chapters; Up/Down and A, a click or a tap
start the chosen one; the shelf counts a probed file's chapters.

### MED-40 — Plex sign-in, libraries and transcoding (P3, M) [NET]
**Where.** A `plex.go` beside `musicsrc_jellyfin.go`; routes in
`musicproxy.go`; rows in `media_movies.go`.
**Done when.** A plex.tv/link PIN sign-in (code on screen, no password typed
on the handheld) finds the account's servers, picks a home-network connection
before a relay, lists On Deck, Movies and TV shows, plays a file the handheld
decodes directly and anything else through the universal transcoder at 720p
H.264, and reports the timeline so Plex keeps where it stopped. The token stays
in the loopback proxy, never on ffmpeg's command line.
**Verify.** Against a Plex Media Server on the home network: sign in, play a
film and an HEVC episode, stop half-way, and the Plex web app shows both in
Continue Watching at the right place.

---

## System and connectivity (SYS)

### SYS-4 — Focus mode with a PIN (P3, M)
**Done when.** A persisted Simple mode shows only Home, Media, Games and
Settings → Simple; leaving needs a PIN (hashed in the secret store); the setting
says plainly it is not parental security.

### SYS-8 — Storage you can manage (P2, M)
**Done when.** Settings → Storage shows usage by kind (games, media, downloads,
saves, caches, backups, old versions, trash) with one-press clean-ups and a
warning at 90% full; the card's filesystem dirty flag is checked at boot
(`sdhealth.go`).

### SYS-9 — Move to a new device (P3, M)
**Done when.** Send my settings also copies the wallpaper and custom theme
files it names, and the installed package list (apps, emulators, cores) can be
reinstalled on the other device in one flow, with a summary of what moved.

### SYS-10 — Import from the stock launcher and other firmwares (P3, M)
**Done when.** First run offers to import TrimUI MainUI favourites, recents and
playtime, and muOS collections where their files are on the card (NextUI's
`Collections/*.txt` are shared both ways,
`collections_nextui.go`).

### SYS-19 — Encrypted save backups to your own WebDAV server (P3, M) [NET]
**Where.** `ssync.go`, `ssyncui.go`, the secret store, and
`docs/SAVE_MANAGER.md`.
**Done when.** Save Sync can add a user-selected HTTPS WebDAV target (including
Nextcloud), upload versioned client-encrypted save snapshots, and list and
restore them through the existing import preview; the WebDAV app password stays
in the secret store, TLS certificates are validated, interrupted uploads leave
the previous snapshot usable, and restoring always takes a Save Manager snapshot
first. No save or passphrase is uploaded in plaintext.

### SYS-21 — Juka and pictures, second round (P3, M)
**Where.** `vision_chat.go`, `vision_video.go`, `chatactions.go`,
`imageviewer.go`.
**Done when.** A Community message's actions offer **Ask Juka** for its picture
(the Discord file is downloaded first); the image viewer sends the picture on
screen to Juka; the transcript upload is compressed (FLAC or Opus) when the
firmware's ffmpeg has the encoder, so a ten-minute clip goes faster on slow
Wi-Fi.

### SYS-23 — JukaHub OS, second round (P2, M)
**Where.** `jukaos*.go`, `emulators.go` (`deviceRetroArchCoresDir`),
`gamelaunch_firmware.go` (`firmwareROMArg`), `elfcompat.go`, `ports_catalog.go`,
`files_extract_ui.go`, `home_power.go`.
**Done when.** A generated per-firmware paths layer dir-binds one stable tree
(RetroArch cores/system/saves, `Emus/*/`) so RetroArch, the firmware's emulators
and netplay stop guessing among five roots and MainUI's `../../Roms` form;
`elfcompat.go`/`ports_catalog.go` also search the session's
`JUKAHUB_OS_CHILD_LIBS`; the Files archive sheet offers **Install as JukaHub OS
layer** for a zip with `layer.json`; driver and value changes apply live where
possible; a per-session report page lists what was bound, loaded and skipped;
the power card can restart JukaHub OS.

### SYS-25 — Bluetooth, third round (P2, M)
**Where.** `bt_autoreconnect.go`, `bluetooth_bringup*.go`, `audioroute*.go`,
`bluetooth_devices_ui.go`.
**Done when.** JukaHub remembers that Bluetooth was on and brings it up at start
(bounded, off the render thread); a headset that connects during a RetroArch
game gets its sound, through a BlueALSA-aware ALSA plug or a resume-safe restart
the user agrees to; a headset's row and sheet show its battery from BlueZ's
`Battery1` ("Battery Percentage" in `bluetoothctl info`) when it reports one.

---

## Sound and haptics (SND)

### SND-2 — One audio mixer on the speaker too (P2, M)
**Where.** `audioroute.go` (the `out*` functions and the mixer), `sound.go`,
`bootchime.go`, `ledsync.go` (`audioBus`).
**Done when.** The speaker route uses the same mixer on one stereo device (the
per-output passthrough is gone), and the LED analyser is fed from the mix.

### SND-3 — Sound packs (P3, M)
**Done when.** Chime and UI-sound variants install as a package category
(scores + constants as data, not samples) and are selectable in Settings.

### SND-9 — The game plugin's interfaces against the real headers (P1, S) [NET]
**Where.** `player/spatialfx/jukahub_spatial.h`, `jukahub_spatial.c`,
`README.md`; libretro-common's `libretro_dspfilter.h` and `dsp_filter.c`;
`ladspa.h`.
**Done when.** Every struct, typedef, flag and entry point matches the headers
(or is fixed) and the README says so; then DSP API v2 (RetroArch's int16
`process_i16` path) is decided.

### SND-10 — Discrete 5.1 and 7.1 sources (P3, M)
**Where.** `videoplayer.go` (`startFFmpegAudio`), `audioresume.go`,
`spatial_dsp.go`, `jh_spatial_ref.py`.
**Done when.** With spatial audio on, a 5.1 or 7.1 source is decoded with its
own channels, each plays from its own virtual loudspeaker (centre ahead,
surrounds behind, LFE to both ears), the decoders' frame arithmetic follows the
channel count, and the spec has golden vectors for the multichannel path.

### SND-11 — The game filter on Windows (P3, S) [WIN]
**Where.** `player/spatialfx/build.sh` (`TARGET=windows`), the Windows release
job, `spatial_games.go` (where it looks).
**Done when.** The Windows archive carries the DLL where `spatial_games.go`
looks, and a RetroArch game started from the library on Windows plays through
it.

### SND-14 — Juka's Groq voice, second round (P3, S)
**Where.** `tts_play.go`, `tts_groq.go`, `tts_settings.go`.
**Done when.** If the first-word delay is the download (`tts_play.go` already
logs the timings), the WAV body streams into the feeder as it arrives; a 400 or
404 for the speech model is remembered for the run like the terms; a voice
volume slider sits under **Voice**; a second language follows INP-18's choice
where Groq has a voice for it.

---

## Web builder (WEB)

### WEB-1 — Split `script.js` into modules (P1, L)
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
**Where.** `patch_engine.go` (`extractZipSafe`/`extractTarGzSafe`, used by
`patch_flow.go`, `ports.go`, `psremote_helpers_install.go`), `emu7z.go`,
`ports_store_install.go`, `ppsspp_managed.go`, `tts_install.go`,
`emulatorupdates.go`, `androidpak.go`, `relpack.go`, `ssyncbundle.go`, `bkup.go`
(restore).
**Done when.** Every archive extraction writes through an `os.Root` opened on
its destination (reusing `pkgExtractZip`'s refusals); traversal tests stay
green.

### SEC-6 — Plugin permissions and signing (P1, L) [OWNER]
**Done when.** Manifests declare permissions (network, files outside the
plugin folder, run programs, settings); the install sheet lists them; scripts run
with a minimal environment, a working directory inside the plugin, a timeout and
(on Linux) a dropped-privilege user if the image allows; catalogue plugins are
signed and sideloaded ones say so.

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

### SEC-16 — Signed package catalogue, hashes mandatory (P1, M) [OWNER] [NET]
**Where.** jukaLang/Packages (`packages.json`), `player/pkgverify.go`
(`pkgApplyIntegrity`), `player/pkgstate.go` (`pkgCatalogFetcher`),
`player/patch_signed.go`, `docs/PACKAGES.md`.
**Done when.** The registry's CI writes `sha256` and the exact `size` for every
entry and a detached Ed25519 signature (`packages.json.sig`, canonical bytes, key
id) with the production key from REL-3; the store verifies the signature when
fetching the list (offline keeps the last verified list); once every entry has a
hash, an entry without one is refused and the "Not verified" path is removed; a
test feeds a tampered list and a missing hash.

### SEC-19 — The pairing code never crosses the network in clear (P2, M)
**Where.** `livebuilder.go`, `builder-device.js`.
**Done when.** Requests are authenticated with an HMAC of the method, path, body
hash and a timestamp/nonce keyed by the code (or TLS with a pinned self-signed
certificate shown as a QR), the code itself is never sent, replays are refused,
and tests cover a replay and a stale timestamp.

### SEC-29 — Build the arm64 parts as an unprivileged user inside the build root (P3, S)
**Where.** `.github/scripts/build-root.sh` (`in_root`, `build`, `check`),
`player/native/build-helpers.sh` and `build-runtimes.sh` (the folders they
write: `DEPS_PREFIX`, `/tmp/jukahub-*`, `player/native/bin` through `/src`).
**Done when.** `build` and `check` run the scripts as a build user (`chroot
--userspec`) that owns only the folders they write, `create` and the package
installs stay root, and the builds still pass.
**Verify.** One helper and one Box64 run on GitHub, and `id` in a build log.

### SEC-30 — Startup lock, the remaining paths (P2, M)
**Where.** `peernet.go`, `livebuilder.go`, the Steam OpenID return
(`/steam/return`), `powersleep.go` and the parked game loop in `main.go`, the
Reset settings, backup restore and config-history paths, plugin startup hooks,
`bootlock_ui.go`.
**Done when.** Paired devices' save sync and hand-off and the Live Builder are
refused while locked; a Steam sign-in cannot complete behind the lock; with
"after sleep" a game paused by the sleep stays paused under the lock instead of
resuming playable; Reset settings, a backup restore or a config rollback cannot
remove or change the lock (or "Lock when") without the code; plugin startup
actions wait for the unlock; a PC mouse can click the lock's power choices; the
PBKDF2 cost is timed on the A133 ("Checking…" under about a second, else lower
the stored iteration count for new hashes).

---

## Docs and project (DOC)

### DOC-4 — Decisions recorded (P3, S)
**Done when.** ADRs exist for the input vocabulary (`inputhints.go`), the
string catalogue (I18N-1), the plugin sandbox (SEC-6) and the overlay approach
(`useroverlays.go`).

---

## Input and controls (INP)

The hint layer (`inputhints.go`) decides whether hints name the pad's buttons or
the keyboard's keys; these tasks make the rest of the input match it.

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

### INP-8 — Keyboard profiles in the control-profile engine (P3, M)
**Where.** `inmap*.go` (profiles map pad buttons only).
**Done when.** A profile can bind keys to actions; the hint layer reads it, so
a remapped key is what the hint shows.

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

## Shell, multitasking and notifications (SHELL)

### SHELL-6 — Back and focus that remember (P2, M)
**Done when.** Each scene saves focus + scroll on leave and restores it on
return (bounded map); Home restores the last focused card after a game.

### SHELL-7 — Recent screens switcher (P3, S)
**Done when.** Holding Start (or Alt+Tab on a keyboard) shows the last five
screens as cards; release opens the highlighted one.

### SHELL-9 — The tip of the day takes A, a click and a tap (P2, S)
**Where.** `helpdoc.go` (`homeTipClick`, `homeTipOpen`), `homefocus.go` /
`homelayout.go` (a focus item for the tip), `home.go`
(`handleHomeMouseClick`), `hometabs*.go` for the Tabs Home, `cfgdoc.go`
(`cfgdocRuntimeKeys`).
**Done when.** The tip is focusable on Standard, Hero and Tabs (Down from the
last row, Up from the footer); A opens its CONTROLS.md section and X hides it
for the day; a click or tap does the same; `homeTipHiddenDay` is in
`cfgdocRuntimeKeys`.

### SHELL-10 — Clock faces and a real standby screen (P3, M)
**Done when.** The screensaver and dock offer three clock faces (analogue,
digital, minimal) with OLED-safe pixel shift; the choice is a theme-able token.

---

## Power, sleep and battery (PWR)

### PWR-6 — Profiles that follow the situation (P2, M)
**Where.** `powerprofile.go`, `raemu.go`, the game options sheet.
**Done when.** Every launch path goes through `beginLaunchPowerProfile`;
profiles exist per game, per system and per power source (battery / charger),
most specific wins; the game options sheet shows which one applies.

### PWR-9 — Battery estimates per activity (P3, M)
**Where.** `battery_eta.go` (the learned rate in `data/battery_eta.json`),
`native/stream_sys.c` (the helper's status file), `psremote_lastrun.go`.
**Done when.** The learned rate is kept per activity and the estimate uses the
one that is running; the helper's status file carries the battery level at the
start and the end of a stream, and JukaHub learns the stream's rate from it.

### PWR-12 — Standalone emulators save on exit for JukaHub's launches (P2, M)
**Where.** `sessionsafe_recipes.go` (the recipe table and the config each
emulator reads), `racfgscope.go` (the journaled one-launch edit to copy),
`instantresume.go` (the plan).
**Done when.** A library launch through DraStic, Flycast or Mednafen turns that
emulator's own save state on exit on for the launch only — DraStic
`enable_autosavestate`, Flycast `Dreamcast.AutoSaveState`, Mednafen `autosave`
— in the config its last run read (sessionSafeProcBases), journaled and put
back when the game exits as RetroArch's switches are; the game's next start
loads that state where the emulator can be told to (Flycast's `-config`
override, Mednafen's own autosave); such games then take the plan's Save step
at the battery and the power offs instead of the hold.
**Verify.** A test over fixture configs: the edit, the restore after an exit
and after a crash (from the journal), and a value the player changed during the
game kept.

---

## Reliability, updates and recovery (REL)

The boot guard counts starts and keeps the last good settings (`bootguard.go`);
these build on it.

### REL-3 — Signed updates only (P1, M) [OWNER]
**Done when.** CI signs every release manifest with a production key held as a
secret (ceremony in `docs/UPDATER_TUF.md`), listing every asset the release
carries — the app archives and the `JukaHub-RemotePlay`, `JukaHub-AndroidRT` and
`JukaHub-Wine` zips, which the helper installer and `relpack.go` check against
it when present (`docs/PACKAGES.md` shows the command) — the app embeds that key
plus one rotation slot, and an unsigned or expired manifest is refused with a
clear message (sideload keeps its explicit trust prompt).

### REL-8 — Reproducible releases with an SBOM (P1, M) [NET]
**Where.** `.github/workflows/release.yml`, `.github/scripts/trimui-build.sh`,
`.github/workflows/docker-image.yml`, `player/build.bat`, `player/release.sh`.
**Done when.** Every release build uses `-trimpath` and `SOURCE_DATE_EPOCH`
instead of the date stamp, toolchains are pinned, an SPDX SBOM and SLSA
provenance are attached; asset names match `patch_swap.go`; release notes come
from the tag's CHANGELOG section (the in-app changelog reads them).

### REL-13 — Turn on the bug-report service (P1, S) [OWNER] [NET]
**Where.** Netlify → generate.jukalang.com → Environment variables;
[docs/BUG_REPORTS.md](../docs/BUG_REPORTS.md).
**Done when.** `JUKAHUB_GITHUB_TOKEN` (a bot account's token with Issues
read/write on jukaLang/JukaHubV2) is set and deployed; `curl -i` on the address
answers 405 with `"configured": true`; a report from a device becomes an issue
with the `from-device` label and the sent sheet shows its number.

### REL-14 — A package counts as installed only while its files are there (P3, S)
**Where.** `patch_db.go` (`dbIsInstalled`), `patch_resolve.go`, `patch_flow.go`
(`patchInstalledRelPath`).
**Done when.** A package counts as installed only while at least one file it
owns (mapped with `patchInstalledRelPath`) exists; a record with none left shows
as "files missing" with Reinstall and Forget.

### REL-15 — Watch updates on the JukaHub OS SD image too (P2, M)
**Where.** `player/cmd/jukaboot/launch_linux.go`, `os/smartpro/launch.sh`,
`player/updrescue.go`, `player/startcheck.go`.
**Done when.** jukaboot exports the variable and runs the same watchdog (ready
within the limit, a `SIGABRT` dump, one restore, `.update-rescued`), and
a stub launcher that hangs before its first frame is put back by the same
watchdog.

### REL-16 — Finish a restore that a power cut interrupted (P3, S)
**Where.** `player/launch.sh` (restore), `player/updrescue.go`.
**Done when.** A synced marker is written before the renames and removed after
them; a start that finds it finishes the remaining renames from the
`.rescue-tmp` copies before starting JukaHub and writes `.update-rescued` as
usual; a shell simulation stopped between two renames recovers.

### REL-17 — Version history says how each version started here (P3, S)
**Where.** `upch_ui.go`, `uhist.go`, `updrescue.go`.
**Done when.** The running version and each saved one show one line from those
records ("Started fine on this Smart Pro" / "Didn't start on this Smart Pro —
not offered again"), with a date only when the clock was plausible.

---

## Languages and locales (I18N)

### I18N-1 — The string catalogue, finished (P1, M)
**Where.** `i18n.go`, `player/i18n/`, the `SizeUTF8` call sites, a check under
`tools/`.
**Done when.** Plurals follow CLDR rules for each shipped language; every
measurement goes through the translated text; the first start picks the OS or
firmware locale when a catalogue exists; a CI check lists untranslated strings
per language; missing translations fall back to English.

### I18N-UK-1 — The Ukrainian catalogue, finished (P3, M)
**Where.** `player/i18n/uk.json`, a new `player/i18n/uk.tour.json`.
**Done when.** Every key of `ru.json` and `ru.tour.json` has a Ukrainian value
written or checked by a Ukrainian speaker, and the catalogue test passes.

### I18N-4 — Input methods (P2, L)
**Done when.** The on-screen keyboard has pinyin and kana composition with a
candidate strip (`vkeyboard.go` already has a suggestion strip); physical
keyboards get `SDL_TEXTEDITING` handling.

### I18N-5 — Human translations (P2, M) [OWNER]
**Done when.** A translation platform (Weblate or Crowdin, free for OSS) is set
up on the catalogue; Spanish, Portuguese (Brazil), Russian, German and French
are reviewed for Home and Settings; Japanese, Korean and Chinese (Simplified),
machine-translated on 2026-11-24 (about 1,070 strings each), are reviewed by
speakers.

### I18N-CJK-1 — Japanese, Korean and Chinese: the tour and line breaks (P2, M)
**Where.** `player/i18n/{ja,ko,zh-CN}.tour.json` (new), `core.go`
(`wrapTextLinesRaw`), `notiftext.go` (`wrapWordsMeasured`, the model),
`uilangface.go`.
**Done when.** The first-run tour has its three catalogues; `wrapTextLines`
breaks a run of CJK characters between characters (never before 、。」 or
after 「) instead of ellipsizing a sentence that has no spaces, the way
`wrapWordsMeasured` already cuts one; the strings added after the catalogues
were written are translated.
**Verify.** A wrap case in `notiftext_test.go`; the catalogue test.

### I18N-6 — Right-to-left layout (P3, L)
**Done when.** Arabic/Hebrew mirror the Home grid, lists and footers; text uses
bidi reordering (golang.org/x/text/unicode/bidi).

### I18N-7 — Dates in the mail, calendar, ports and Steam screens (P2, S)
**Where.** `mail_ui.go` (437-465, `mailSizeText`), `mail_compose.go:95`,
`mailcal_ui.go:653-673`, `calsync_remind.go:331`, `ports_store.go:828`,
`steam_ui.go:1482-1484`, `notiftext.go`; helpers in `clockformat.go`
(`formatDate`, `formatDateNear`, `formatDateBrief`, `formatWeekdayShort`,
`formatMonthYear`, `clockDigits`, `formatSize`, `isLineBreakSpace`).
**Done when.** None of those files calls `Format` with a month, weekday or clock
layout for text on screen, `mailSizeText` is `formatSize`, and
`wrapWordsMeasured` splits with `strings.FieldsFunc(para, isLineBreakSpace)`.

---

## Network and connectivity (NET)

### NET-7 — Network shares in Files and the library (P2, L)
**Done when.** SMB2/3 (go-smb2) and WebDAV locations can be added in Files
(credentials in the secret store), browsed, copied from, and used as a read-only
ROM or media source with local caching.

### NET-15 — FTPS (explicit TLS) for the FTP server (P3, M)
**Where.** `ftp_server.go`, `nettoolsscreen.go`.
**Done when.** `AUTH TLS` with a persisted self-signed certificate whose
fingerprint the sheet shows, `PROT P` data channels, and the sheet recommends
FTPS or SFTP.

### NET-16 — The script guard knows where the page itself lives (P2, M)
**Where.** `textbrowser.go` (`fetchTextBrowserOnce`), `webdom_net.go`
(`webJSNetPageGuard`, `newWebJSNet`), `webdom_runtime.go`.
**Done when.** The page fetch records the address it actually connected to
(`httptrace.ClientTrace.GotConn`) and the guard classes the page by that address:
a page whose server is on the LAN reaches the LAN, a public page still cannot,
and a test covers both.

### NET-20 — Windows notices a network change made outside JukaHub (P3, S) [WIN]
**Where.** `wifi_windows.go`, `netmon.go` (`netNudge`).
**Done when.** `NotifyIpInterfaceChange` (or `NotifyAddrChange`) calls
`netNudge()`, and switching networks in Windows updates JukaHub's status within
about a second.

---

## Game library and emulation (LIB)

### LIB-3 — Collections that build themselves (P1, M)
**Where.** `hometabs_collections.go`, `gmeta_dat.go`, `game_library_ui.go`.
**Done when.** Smart collections — by genre, by decade, by player count (from
`gmeta_dat.go` metadata) — join the automatic three; the Games library shows the
same collections as Home (one store, `collections.json`); Settings can hide the
automatic ones; the 32-tag limit becomes a bounded list per collection.

### LIB-5 — Rich media per game (P2, M) [NET]
**Where.** `artwork_match.go`, `gmeta_ui.go`, the PDF reader, `cachebudget.go`.
**Done when.** Snaps and title screens from libretro-thumbnails show beside the
box art, and (with GAME-8's account) descriptions, screenshots, videos and
manuals come from ScreenScraper; a manual opens in the PDF reader; everything is
bounded by the cache budget.

### LIB-6 — Verified dumps (P3, M)
**Done when.** No-Intro/Redump DATs install as a package; the library verifies
CRC/SHA-1 in the background and badges games Verified / Unknown / Bad dump.

### LIB-12 — RetroAchievements, all the way (P2, M) [NET]
**Done when.** Per-game achievement lists with progress, a hardcore toggle that
writes `cheevos_*`, the key in the secret store, and an LED flourish on unlock
(`ledsync.go`).

### LIB-13 — Playtime you can trust (P2, M)
**Done when.** Sessions are appended to their own bounded log keyed by content
hash; 400 days are kept; Wrapped covers a year; muOS-style export (CSV/HTML) to
the card; the user config is no longer rewritten for playtime.

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

### APP-6 — The phone as a second screen (P2, M)
**Done when.** The companion page lists the library with art and launches a
game, uploads a save to the right folder, and mirrors JukaHub notifications;
every action goes through the existing pairing and queue.

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

### APP-11 — Linked stylesheets for page scripts (P2, S)
**Where.** `textbrowser.go` (`browseWebPageResult`), `webpage.go`
(`fetchWebStylesheets`), `webdom_runtime.go` (boot), `webdom/45_css.js`.
**Done when.** The first linked sheets (the renderer's limits) are fetched beside
the page's scripts and handed to the prelude before the parser scripts run, the
renderer reuses the same bytes instead of a second download, and a test shows
`.show()` on an element a linked sheet hides.

### APP-12 — Ranges that follow the document (P3, S)
**Done when.** Live ranges are tracked per document and their boundaries move on
insert, remove, `splitText` and `normalize` as the DOM standard says, covered by
the jsdom comparison in `tests/web/webdom_dom.test.mjs`.

### APP-15 — 7z without 7-Zip (P3, M)
**Done when.** A 7z whose folders use LZMA or LZMA2 (with or without the BCJ
x86 filter) and whose header is not encrypted unpacks in-process through the
same staging and `os.Root` rules, with fixtures made by 7-Zip in the tests;
other coders (BCJ2, PPMd, AES) still go to 7-Zip.

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

### DESK-1 — No console window, a real log (P1, S) [WIN]
**Done when.** Release builds are GUI-subsystem; WARN+ lines are appended to
`%LOCALAPPDATA%\JukaHub\logs\errors.txt` with the handheld's 1 MiB rotation;
Report a bug and Diagnostics name the folder; Settings → Help → "Open logs
folder".

### DESK-2 — Sharp on high-DPI screens (P1, S) [WIN]
**Done when.** Per-monitor-v2 awareness (manifest in `JukaHub.rc` +
hint), rendering at native pixels with the logical canvas scaled by SDL; text
is crisp at 150% and 200%.

### DESK-3 — Windows that remember (P2, S) [WIN]
**Done when.** Size, position, monitor and maximised state persist; a
borderless-window mode exists; the monitor can be chosen (today always display
0, `main.go:388`); the fullscreen cleanup lands first.

### DESK-4 — An installer and package-manager listings (P2, M) [WIN] [NET]
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

### DESK-7 — Start with Windows (and a desktop login) that works (P2, S) [WIN]
**Where.** `autostart.go` (`autoStartLauncherFor`), `main.go`, `xmlconfig.go`.
**Done when.** A relative config path is resolved from the executable's folder
(covering every launcher at once); Windows autostart writes a `.lnk` (or a `Run`
registry value) instead of the `.cmd`, with the working directory set and no
console.

### DESK-8 — Discord Rich Presence (P3, S) [WIN]
**Done when.** Opt-in presence over Discord's local IPC pipe shows the game or
the screen; no account tokens involved.

### DESK-9 — Open with JukaHub (P3, S) [WIN]
**Done when.** Command-line arguments open a file (media, ROM, PDF) or a scene
(`--scene Settings`); the installer registers "Open with JukaHub" for media and
ROM types.

### DESK-10 — Windows notifications (P3, S) [WIN]
**Done when.** While the window is minimised, download/alarm/update
notifications appear as Windows toasts (AUMID set by the installer).

### DESK-11 — Brightness on Windows, on real machines (P2, S) [WIN]
**Where.** `brightness_windows.go`, `brightnessctl.go`, `shutdown.go`,
`bootos.go`.
**Done when.** On a laptop (and a ROG Ally / Legion Go class handheld) the card
moves the same level as Windows' slider and follows it while open; on a desktop
with a DDC/CI monitor it moves the monitor's brightness; with neither (a VM,
Remote Desktop) the card says it dims JukaHub only and the dim survives a
restart; no console window flashes; the startup probe does not delay the first
frame; if a PowerShell run per step feels slow, one PowerShell process is kept
for the session (or WMI is called through COM); `shutdown.go` and `bootos.go`
call `brightnessSoftRender` before their `Present`.

### DESK-12 — Emulator downloads on a PC that resume and are checked (P2, S) [WIN]
**Where.** `emuautoupdate_desktop.go`, `emuwininstall.go`, `emulators.go`.
**Done when.** A cut-off emulator download resumes where it stopped;
`RetroArch.7z` is checked against a second channel before it is installed.
(Pinning an emulator's version is a separate task.)

### DESK-13 — Install Azahar like the other Windows emulators (P2, S) [WIN] [NET]
**Where.** `emuwininstall.go` (`emuWinSpecs`), `emu_azahar.go`.
**Done when.** An `emuWinSpecs` rule for `azahar-emu/azahar` (assets
`azahar-windows-msvc-<ver>.zip` from 2125, `azahar-<ver>-windows-msvc.zip` up
to 2124.3; not `installer`, `msys2` or `room`; launcher `azahar.exe`) makes
Install, Update and Roll back work like Xenia Canary, and a `Roms\3DS` game
starts full screen from the library.
**Verify.** A unit test over `emuWinSpecs` matches each published Azahar asset
name and picks `azahar.exe` as the launcher.

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
  console (XL), pixel editor (L).
- **Per-game colour filters** (S, muOS has them): a colour matrix per game on
  top of the accessibility palettes.
- **"Play style" profile** (S): when and what you play, from LIB-13's session
  log.
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
