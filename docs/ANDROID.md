# Android engine

JukaHub does not ship a complete Android. The **AndroidPak** path runs a
limited class of native-only (NDK) games through **AndroidRT**, a small runtime
JukaHub builds and ships: every TrimUI package carries it beside the app, and
the APK Store installs the same build from JukaHub's releases on a device that
lacks it. Real-game support is still unverified on hardware. The **APK Store**
(Packages → More stores, or Start → Games → APK Store) is the front door to
it: it finds the APKs on the card, says what each one is and whether it can
run, and adds the ones worth trying to Games in one press — the Games
Library's Android row and Game Profiles. Java apps (almost every Android app)
cannot run on AndroidRT and the store says so; where a real Android container
(Waydroid) exists, on Linux machines with a binder kernel, the store can
install APKs into it.

**On a Smart Pro, in short:** copy an NDK game's APK to `/mnt/SDCARD/APKs`
(or send it from a phone), open the APK Store, press **A** on the game →
**Add to Games**, then play it from the Games Library's **Android** row (or the
store's Play). If AndroidRT is missing, the store offers to install it with one
press first.

## APK Store

The screen has three tabs ([player/apkstore.go](../player/apkstore.go)):

* **Your APKs.** A bounded scan (two folders deep, at most 300 files, a few
  seconds) of `/mnt/SDCARD/APKs`, `Android`, `Roms/ANDROID`, `Roms/APK`,
  `Downloads`, the Phone Companion's `PhoneDrop` folder (a phone sends an APK
  there in one tap), the store's own download folder and JukaHub's downloads;
  on a computer, the user's Downloads. `JUKAHUB_APK_DIRS` (a PATH-style list)
  adds folders. Each APK is read once and cached by path, size and time:
  * the zip directory (ABIs, native libraries, DEX files, icons);
  * the binary `AndroidManifest.xml`, decoded in Go
    ([apkstore_axml.go](../player/apkstore_axml.go)): package, version, SDK
    levels, the label, the launcher activity (MAIN/LAUNCHER, activity-aliases
    followed), NativeActivity's `android.app.lib_name`, `hasCode`, the GLES
    version, whether the file is one piece of a split app — and, when the
    label or icon is a resource reference, just enough of `resources.arsc` to
    resolve it (default locale first; the densest PNG or JPEG icon;
    adaptive-icon XML and WebP are skipped). Both decoders are bounded in
    memory as well as in offsets: each pool string is decoded at most once
    and shared, a pool decodes at most 8 MiB of text, an element's attributes
    past 128 and a resource's configurations past 256 are not read, and a
    value is decoded only when it is used — running out of memory is not a
    panic that a recover() could catch;
  * AndroidPak's own triage (`inspectAndroidPak`, the titles it knows);
  * the engine the libraries and activities give away (Unity, Unreal, Godot,
    GameMaker, LÖVE, SDL, Cocos2d-x, Flutter, React Native, Xamarin,
    NativeActivity, GameActivity);
  * when the start is Java and nothing else says, whether a library of the
    chosen ABI exports an entry point AndroidRT can start
    (`ANativeActivity_onCreate`, GameActivity's `GameActivity_onCreate` or the
    JNI name of its `initializeNativeCode` — what `runner.c` looks for): an
    uncompressed library is read through its ELF section headers (only its
    dynamic string table), a compressed one as a stream, at most 64 MiB per
    APK. None means a Java app with helper libraries. Only when one is found
    are the DEX files searched (bounded) for a NativeActivity or GameActivity
    subclass.

  The verdict is one of: **Native game — try it (experimental)**, **Minecraft
  class pack (experimental)**, **Native entry, Java start — may not run**,
  **Needs Android's Java runtime — can't run on AndroidRT** (with the reason and
  the route that works instead, such as a PortMaster build or PortMaster's
  GMLoader for GameMaker games — Java apps that carry helper libraries
  included), **One piece of a split APK** (a `base.apk` or `split_config…` piece
  of an App Bundle install: it needs a single, universal APK), **Wrong CPU**
  (x86-only builds, and 32-bit-only `armeabi-v7a` builds: AndroidRT's loader
  takes 64-bit `arm64-v8a` libraries only) or **Not a readable APK**. A Java
  app is never offered as something to try.

  A on an APK offers **Add to Games**: the store writes one profile per APK to
  `<data>/game-profiles/local/apkstore-<id>.json` (runtime `androidpak`; the
  manifest's NativeActivity library — `android.app.lib_name` X is `libX.so`,
  as Android maps it — as `mainLib` whenever the APK carries it) and saves the
  launcher icon as the game's cover. The game then appears on the **Games
  Library's Android row** ([GAME_LIBRARY.md](GAME_LIBRARY.md#android-games))
  and in **Start → Games → Game Profiles**. Two APKs of one package (two
  versions of a game) get two profiles: the second id carries a short hash of
  its path. **Play** launches through the same path (triage, memory pre-flight,
  tmpfs staging, launch log, first-run report) in the background — the row
  says "Starting…" while a big game is staged, and a second Play meanwhile
  (here, in the Games Library or in Game Profiles) says it is already starting
  — and **Remove from Games** deletes only the store's own profile file and the
  cover it saved; the APK stays. When AndroidRT is missing, Play offers
  **Install and play** and Add offers **Install and add**: one press installs
  the runtime (below), then the game starts or is added; nothing downloads
  without that press, and while it is being installed the sheets say so
  instead of offering a second install. X shows
  everything the store read, the profile's state and how the last run ended
  (from its launch log). The APK must sit inside the folders game profiles
  launch from (the card on a handheld). A hand-written profile for an APK the
  verdict says cannot run shows as "In Games · won't run", with Play offered
  but not suggested.
* **Store.** A short catalogue ([apkstore_catalog.json](../player/apkstore_catalog.json),
  also served as `apk-catalog.json` from the builder site) of titles and kinds
  of APK: status (experimental / won't run / works), how to get the APK
  legally (your own APK — JukaHub never hands out paid apps) and what works
  instead. An entry may carry a direct download for free software, but only
  with a SHA-256 and a size, from the HTTPS hosts JukaHub's patch downloader
  trusts (GitHub and the libretro buildbot); the file is checked before it is
  kept, it is saved as `<id>.apk` (or `<id>-<hash>.apk` when a different file
  already has that name — a download never replaces a file it did not write),
  and an entry without a hash offers no download. The built-in list carries
  none yet: none could be verified when it was written. A newer list is
  fetched at most once a day (256 KiB cap, schema-checked) and used only when
  it is newer, dated no more than three days past today (or past the built-in
  list's date, on a handheld whose clock has not been set), and at least half
  of its entries are readable.
* **Runtime.** Whether AndroidRT is ready and which copy is in use (bundled
  with JukaHub, installed from its releases, or your own), with **Install**
  when there is none and **Update to …** when a newer release than the
  installed copy is out (asked at most every half hour, only for the copy the
  store installed); whether this machine can run it (aarch64 Linux only — on a
  PC the store reads APKs but games run on the handheld), the RAM, whether
  Waydroid is usable here, and what AndroidRT can and cannot run. An install
  shows its progress on the row and on the install bar (Stop on the AndroidRT
  sheet ends it), and a failure says why in one line (offline, GitHub
  refusing, the card full, the download not verifying, the runtime not
  starting on this device).

**Waydroid.** Where the `waydroid` command exists, the kernel exposes a
binder device and Waydroid's session is running, A on an APK also offers
**Install into Android**: `waydroid app install <apk>` with the path validated
first (absolute, a regular `.apk`, no name that reads as an option). Waydroid's
exit status proves nothing — with the session stopped it logs the failure and
exits 0, and the container finishes an install on its own — so the session is
checked again just before, failure words in the output count as a failure,
and the store calls the APK installed only when `waydroid app list` shows its
package; otherwise the toast says it was handed over. The app then opens from
Waydroid's own window. A stock TrimUI kernel has no binder device, so this is
for Linux machines and community firmware
([apkstore_waydroid.go](../player/apkstore_waydroid.go)).

## Why an APK cannot simply be run

An `.apk` is a zip holding DEX bytecode, optional native libraries, and
resources. Running it needs:

* **ART** to execute the DEX — there is no interpreter for it in this app;
* **bionic** for the native `.so` files — they are linked against Android's
  libc, not glibc, so a Linux loader cannot start them;
* **the Android framework** — every app's lifecycle, resources, intents and
  permissions live in `system_server`;
* **SurfaceFlinger** to put pixels on the panel.

Reimplementing that stack is a multi-year project, and it would not fit the
device: the Android system image alone wants several hundred megabytes of RAM
before an app starts, on a 1 GB handheld.

## AndroidPak: NDK-only games without Android (Tier 1)

A large class of Android games are **NDK games**: the DEX layer is thin glue
that calls into a native `lib/<abi>/*.so` main entry. Those games can run as
ordinary native processes if their bionic-linked libraries are loaded by the
[bionic translation loader](https://gitlab.com/android_translation_layer/bionic_translation)
plus shims for `liblog`/`libandroid`/OpenSL ES — a few megabytes of runtime
instead of the system image's several hundred. The `androidpak` [game
profile runtime](GAME_PROFILES.md) is the hub's side of exactly this path:

* **Triage.** An APK is inspected read-only before launch: which ABIs it ships
  (`arm64-v8a` wins, matching the A133P), which `.so` is the native entry
  (the largest of the best ABI, or the profile's `mainLib`), and a hard refusal
  with a reason for packages that are pure Java/Dalvik or ship only x86 ABIs.
* **Staging.** The native subset (`lib/<chosen abi>`, `assets/`, `res/`,
  manifest, `resources.arsc`) is extracted into a bounded tmpfs staging dir
  (`$TMPDIR/jukahub-android/pak-<id>`, 512 MiB cap), so a small game's
  session never re-reads the APK from the SD card and never soaks the whole
  package into RAM — `classes.dex` and signatures are never extracted. A game
  whose `assets/` and `res/` unpack to more than 64 MiB stages only its
  libraries, the manifest and `resources.arsc`: AndroidRT reads its assets
  straight from the APK on the card (stored entries in place, deflated ones
  inflated), because hundreds of MB of assets in tmpfs are RAM the handheld
  does not have. A tmpfs that fills up anyway is named in the error, with
  `JUKAHUB_ANDROID_STAGE` (a folder on the card) as the way round it.
* **Launch.** AndroidRT (see [Where AndroidRT comes from](#where-androidrt-comes-from))
  is started as `loader <apk>` or via its own `launch.sh`, with `ANDROIDPAK_APK`, `ANDROIDPAK_STAGED`,
  `ANDROIDPAK_MAINLIB`, `ANDROIDPAK_ABIS` and `ANDROIDPAK_RT` in the child
  environment. The direct-loader form additionally gets `ANDROIDPAK_SHIMS`
  (the runtime's `shims/` directory, which the runner preloads with
  `RTLD_GLOBAL`) and a staged-first `LD_LIBRARY_PATH`, the same paths
  `launch.sh` arranges for itself. The first run's failure is captured:
  when the profile exits non-zero, the launch log is scanned for the
  runner's undefined-symbol line and the symbol is named in a toast and the
  JukaHub log — that symbol is the next shim to write
  ([player/androidrt/README.md](../player/androidrt/README.md)). The staging
  dir is deleted after the game exits.
* **Room for the launch.** Before anything is staged the free memory
  (`/proc/meminfo`) is checked against the staged bytes plus a runtime
  budget; a launch that would not leave the hub itself alive is refused with
  the numbers, and `JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB` (MiB, `0` disables)
  opts out. The pre-flight lives in [player/androidpak.go](../player/androidpak.go).
* **Input.** The runtime hands games a real `AInputQueue` fed from the
  device's `/dev/input/event*` by the evdev reader; `ANDROIDPAK_INPUT=off`
  launches without it (the game keeps running, just blind), and a node set
  the process cannot read prints the missing-`input`-group note instead of
  failing the launch. See [player/androidrt/README.md](../player/androidrt/README.md).

The AndroidRT source, cross-build script, and **headless smoke check** live in
[`player/androidrt/`](../player/androidrt/README.md). AndroidRT loads the game
with its own bionic ELF loader (NDK imports are versioned `@LIBC` symbols that
glibc's `dlopen` refuses), gives it a Java side through its own JNI
environment (below), drives NativeActivity and androidx GameActivity entry
points, and presents through the firmware's SDL2 and EGL. The host smoke
suite (202 assertions, about 15 s with the host compiler) exercises the loader
(and its harmless `loader --version`), the bionic compatibility layer, the JNI
environment with the Minecraft class pack, the GameActivity bridge with a fake
GameActivity glue, the present layer against a mock GPU stack (including the
stock-TrimUI defaults) and its SDL-hosted backend against a fake firmware
SDL2, the looper/input queue and the OpenSL ES path — without a device.
JukaHub's AndroidPak profile support triages and stages an APK and hands it to
AndroidRT, which JukaHub's CI builds for the handheld
(`player/androidrt/build-tsp.sh`) and ships. **No real game has been run on
hardware yet.** Camera, identity, privacy and framework features stay out of
scope by design.

### Where AndroidRT comes from

* **Bundled.** Every TrimUI JukaHub package carries the runtime beside the app,
  in `Apps/JukaHub/androidrt/` (`loader`, `launch.sh`, `surface-probe`,
  `props.txt.example`, `BUILDINFO.txt`, `SHA256SUMS`). It is replaced with
  JukaHub itself, so nothing else updates it.
* **Installed from JukaHub's releases.** Each release also carries
  `JukaHub-AndroidRT-linux-arm64.zip`. The Runtime tab's **Install** (or the
  one-press offer when Play or Add finds no runtime) puts it in
  `<data>/androidrt` (`Apps/JukaHub/data/androidrt` on the handheld): the
  newest release carrying it, stable before beta; the archive must match the
  release's signed manifest (or GitHub's checksum) and every file its own
  `SHA256SUMS`; and its loader has to start on this device —
  `loader --version`, which touches no game, file or GPU — before the new copy
  replaces anything ([player/relpack.go](../player/relpack.go),
  [player/androidrt_install.go](../player/androidrt_install.go)). The daily
  **Emulator updates** pass keeps this copy current under the same setting as
  the emulators (Settings → Games & emulators → Emulator updates: Off / Notify
  me / Install automatically), never while an Android game runs.
* **Your own.** A runtime you built (`CROSS=aarch64-linux-gnu-
  player/androidrt/build-tsp.sh`) and copied to `/mnt/SDCARD/Apps/AndroidRT`
  (also `Apps/androidrt`, `Apps/bionic-translation`, `PortMaster/AndroidRT`,
  `PortMaster/runtimes/androidrt`, `Emus/PortMaster/AndroidRT`) is used before
  both; `JUKAHUB_ANDROIDRT` points at any folder and is used as given.

A launch takes `JUKAHUB_ANDROIDRT`, then your own copy, then the bundled one,
then the installed one. A folder counts only when it can start games: it holds
a `loader` this device can run — the right CPU, and no glibc newer than the
firmware's (the same ELF check JukaHub applies to emulator cores) — or a
`launch.sh`, so an empty or half-copied `Apps/AndroidRT` does not hide the
bundled runtime; the folders passed over are named in the Runtime tab and in
the "not installed" message. A loader that lost its execute bit (unpacked onto
an ext4 card by a tool that drops permissions) gets it back; FAT and exFAT
cards give every file that bit, which is how JukaHub itself runs from the
card.

### The Java side: AndroidRT's JNI environment

There is no Java VM on the device, but most NDK games still call Java through
JNI (`activity->env`, `JNI_OnLoad`). AndroidRT answers those calls itself
(`src/jni.h`, `src/jnienv.c`, `src/jnibase.c`):

* **The JNI 1.6 function tables** (`JavaVM`, `JNIEnv`) over a small object
  model: classes registered by name whose methods and fields are written in
  C, strings, primitive and object arrays, local/global/weak references,
  pending exceptions, `RegisterNatives` (the runtime calls the game's natives
  back), `AttachCurrentThread` for game threads.
* **`android.os.Build`** answers with the handheld (manufacturer TrimUI, the
  model JukaHub detected, `SDK_INT` 28).
* **What the APK says about itself.** The binary `AndroidManifest.xml` names
  the package and the launcher activity; the `classes*.dex` index says which
  Java classes the app defines and what they extend (`src/apkmeta.c`). An app
  class becomes a known class whose unimplemented members answer defaults.
* **Nothing unimplemented is faked silently.** `FindClass` of a class nobody
  has returns `null`; a member nobody implements gets an ID whose calls return
  `0`/`false`/`null`; each such name is logged **once**, and the run ends with
  a `[androidrt][jni] report:` — the list of what to implement next, in the
  order the game asked. When a run fails, JukaHub's toast names the first
  Java method the game called that has no implementation (else the first
  class it could not get, else the first member it looked up), read from that
  report — or, when the game crashed before the report, from the first-seen
  log lines ([player/androidpak.go](../player/androidpak.go)).
* **System.loadLibrary semantics.** Every loaded library's `JNI_OnLoad` runs
  before the activity starts, dependencies first.
* **Class packs** (`src/jnipack_*.c`) implement one game family's Java
  methods and are selected by the manifest's package name. The first is
  Minecraft: Bedrock Edition — see [ANDROID-MINECRAFT.md](ANDROID-MINECRAFT.md).
* **GameActivity** (`src/gameactivity.c`): androidx GameActivity games have no
  `ANativeActivity_onCreate`; the runtime plays the Java side of
  `com.google.androidgamesdk.GameActivity` and drives its natives
  (`initializeNativeCode`, `onStartNative`, `onSurfaceCreatedNative`, input),
  host-tested against a fake glue (`tests/smoke/fake_gameact.c`).

Environment switches: `ANDROIDPAK_JNI=off` (hand the game a null env, the old
behaviour), `ANDROIDPAK_JNI_TRACE=1` (log every lookup), `ANDROIDPAK_JNI_UNKNOWN=phantom`
(unknown classes become empty classes instead of `null`), `ANDROIDPAK_PACKAGE`
(package name when the manifest cannot be read; JukaHub sets it for titles it
recognises) and `ANDROIDPAK_DEVICE` (JukaHub's device id: panel size, DPI and
the display contract).

### Display: the stock firmware's own SDL2 and EGL

The stock TrimUI Smart Pro and Brick firmware has no DRM card: its PowerVR
GE8300 userspace draws straight to the panel, and the firmware's SDL2 reaches
it through SDL's `mali` fbdev video driver — SDL's generic fbdev-EGL driver,
whatever the GPU is called. On that firmware (`ANDROIDPAK_DEVICE=trimui-*`, or
`/usr/trimui` present) the present layer loads the firmware's
`/usr/lib/libEGL.so` first. A DRM/KMS + GBM stack (for example a community GPU
driver package) still selects the `gbm` backend automatically.

**The stock default: the SDL-hosted backend (`sdl`).** Two ports on this
firmware found that GLES contexts made with raw EGL draw nothing — a black
panel, sound playing, no error — while contexts SDL owns show:
[NextOS ports issue #1](https://github.com/NextOs-Ports/nextos-universal-ports/issues/1)
(Horizon Chase on the Brick Pro, fixed by `HC_PURE_SDL_CONTEXTS=1`) and
[nx-redux PR #152](https://github.com/mohammadsyuhada/nx-redux/pull/152)
(keep the contexts SDL-owned on tg5040). A black panel without an error is the
one failure the runtime cannot notice and fall back from, and every program
that draws on the stock firmware — JukaHub, RetroArch, the ports — draws
through that SDL2. So on the PowerVR models (Smart Pro, Brick, Brick Pro,
Hammer) `auto` starts on `sdl`: the firmware's own SDL2
(`/usr/lib/libSDL2-2.0.so.0`, loaded at run time — nothing links against it)
creates the fullscreen window, its EGL surface and the GLES context, and the
game gets exactly those: its window surface is SDL's, its context is SDL's
(re-created when the game asks for another GLES version; a context the game
makes before its window, on a window config, builds SDL's window and is SDL's
too), `eglMakeCurrent` becomes `SDL_GL_MakeCurrent` and every `eglSwapBuffers`
an `SDL_GL_SwapWindow`. SDL is pointed at the game's own libEGL and the GLES
library beside it, the game's config sizes (colour, depth, stencil, samples)
become SDL's GL attributes, and SDL's signal handlers stay off so JukaHub can
still stop the game. `SDL_GL_SwapWindow` returns nothing, so on this backend
a failed swap (even a lost context) never reaches the game. The log says
`stock firmware (Smart Pro): auto chose the SDL-hosted backend` followed by
`backend sdl (...)`. When the firmware's SDL2 does not start, the log says why
and `auto` uses the native backend.

**Native (`native`).** The game's window goes to EGL as the `{uint16 width,
height}` struct SDL's `mali` driver passes, sized to the panel (1280×720 Smart
Pro, 1024×768 Brick). It is `auto`'s choice on the Smart Pro S (tg5050), whose
GPU really is a Mali, and the fallback above. On native, `auto` switches to
`sdl` by itself when the native backend fails — the game's `eglInitialize` of
the default display fails (SDL starts there and the call is made once more), or
EGL refuses the game's window surface — and says so in the log
(`backend sdl (fallback: ...)`). Only the window surface is SDL's after that
second kind of fallback when the game made its GLES context before its window:
that context stays raw EGL, its frames go through plain `eglSwapBuffers`, and
the log says so (then set `ANDROIDPAK_PRESENT=sdl`). TrimUI's own GE8300 build
of SDL2 is said to create its surface with a NULL native window
([mpv-tsp](https://github.com/matweew/mpv-tsp)); if a device run ever needs
the native backend and EGL refuses the struct, `ANDROIDPAK_EGL_WINDOW=null` is
the setting to try.

`ANDROIDPAK_PRESENT=auto|gbm|native|sdl|off`, `ANDROIDPAK_EGL_WINDOW=fbdev|null`,
`ANDROIDPAK_EGL_LIB=<path>` and `ANDROIDPAK_SDL_LIB=<path>` override each
choice (an explicit `native` never falls back, for diagnostics — it is how to
try raw EGL on the stock firmware; an explicit `sdl` whose SDL2 does not start
is chosen like `auto`). The first device run's `[androidrt][present] backend
...` line is the evidence of which one works.

### Titles triage recognises

* **Minecraft: Bedrock Edition** (`libminecraftpe.so`): needs the arm64-v8a
  build (a 32-bit-only APK is refused with that reason); launched with the
  Minecraft class pack. Experimental — [ANDROID-MINECRAFT.md](ANDROID-MINECRAFT.md).
* **Terraria and other Xamarin/Mono apps** (`libmonodroid.so`,
  `libmonosgen-2.0.so`, `assemblies/`): the game is C#/.NET code run by a Mono
  runtime inside the Java app, which AndroidRT does not provide, so triage
  refuses them with that reason. Terraria's PC version is an XNA game; on
  Linux handhelds such games run through FNA on Mono with the user's own game
  files (the PortMaster model, [PORTMASTER.md](PORTMASTER.md)).

### Trying a game on the device

1. Open **Start → Games → APK Store → Runtime**. The AndroidRT row should say
   **Ready · Bundled with JukaHub**. If it says **Missing**, press **A** →
   **Install** (Wi-Fi needed); if it names folders it passed over, those say
   why (a copy for another CPU, a glibc too new, no loader).
2. Put the APK in `/mnt/SDCARD/APKs` (or send it with the Phone Companion),
   press **Y** in the store to read the card again, then **A** on the game →
   **Add to Games** (or write an `androidpak` [game profile](GAME_PROFILES.md)
   by hand).
3. Play it from the Games Library's **Android** row or the store's **Play**,
   then send back the profile's launch log
   (`Apps/JukaHub/data/game-profiles/logs/<id>.log`; **X** on the APK in the
   store names it). The lines that matter: `[androidrt] manifest:`,
   `[androidrt][present] stock firmware …` and `[androidrt][present] backend …`
   (which display path ran), `SDL2 … video driver …`, `undefined symbol`,
   `trap:` and `[androidrt][jni] report:` (what the game needed that AndroidRT
   lacks), and the final `exit:` line.
4. If the game runs (sound, input) but the panel stays black, launch it once
   more with `ANDROIDPAK_PRESENT=native` in the profile's `env` (raw EGL, the
   other display path) and send both logs. JukaHub's own log (**Tools →
   Logs**) adds the `androidpak`, `androidrt` and `relpack` lines of the launch
   and of an install.

## The ways Android could exist here, ranked

| Approach | Verdict |
|----------|---------|
| **Container integration (Waydroid)** — Android runs in a container on the device kernel. | **Install only.** The only approach that runs real Android apps. The APK Store detects Waydroid (binary + binder device) and installs APKs into it; the earlier engine that listed and launched container apps through a display bridge (`android.go`) is no longer in the tree. A stock TrimUI kernel has no binder device. |
| **QEMU / full emulation** | Not viable. There is no KVM on the handheld, so it is software emulation on a 1.8 GHz A53: single-digit FPS, plus the system image's RAM. |
| **A custom ART/Dalvik interpreter** | Out of scope. DEX execution is the smaller half; the framework, bionic and SurfaceFlinger are the rest. |
| **AndroidPak — NDK games via AndroidRT** | **Shipped; experimental.** The hub triages APKs, stages the native subset to tmpfs and launches AndroidRT — bundled with JukaHub's TrimUI packages, or installed from its releases by the APK Store — with its own bionic loader, JNI environment and SDL2/EGL present layer; real-game hardware support is unverified ([GAME_PROFILES.md](GAME_PROFILES.md), [AndroidRT](../player/androidrt/README.md)). |
| **Sideloading APKs into the container** | **Shipped in the APK Store.** The package is read and triaged first; `waydroid app install <file>` gets a validated path. |
| **Full Android firmware** | A separate OS/image for the device, not a JukaHub integration path. It does not change whether the stock firmware exposes the binder device required by a container. |
| **ADB bridge to a real phone** | A different product: install and drive apps on a paired phone. Not part of this engine. |

## What ships now

* **The APK Store** (above): scan, manifest and resource-table reading,
  triage, engine and verdict, one-press Add to Games (game profile, Games
  Library row and cover), Play/Remove, the catalogue and its verified
  downloads, the runtime (Install / Update) and Waydroid status.
* **AndroidPak** profile support: triage, the memory pre-flight, tmpfs
  staging, the runtime contract and the first-run report
  ([player/androidpak.go](../player/androidpak.go)).
* **AndroidRT**, built by CI for the handheld: in every TrimUI package
  (`Apps/JukaHub/androidrt`) and as `JukaHub-AndroidRT-linux-arm64.zip` on each
  release, which the Runtime tab installs and the Emulator updates pass keeps
  current; its source and host smoke suite are in
  [player/androidrt](../player/androidrt/README.md).
* **The Games Library's Android row**
  ([GAME_LIBRARY.md](GAME_LIBRARY.md#android-games)).
* **Waydroid**: detection (the `waydroid` command, a binder device at
  `/dev/binder`, `/dev/binderfs/binder`, `/dev/binderfs/anbox-binder` or
  `/dev/anbox-binder`, `waydroid status`) and APK install. Listing and
  launching container apps from JukaHub, and the WestonPack display bridge the
  removed `android.go` had, are not part of this tree.

## Requirements

* **AndroidPak / AndroidRT:** an aarch64 Linux handheld (the TrimUI Smart Pro
  and Brick), AndroidRT (bundled with JukaHub's TrimUI package, or installed
  from the Runtime tab), and the APK on the card. A PC reads APKs in the store
  but does not run them.
* **Waydroid install:** a kernel with the binder module (`binder_linux`) —
  the hard gate — and Waydroid installed, initialised and with its session
  running (a one-time root step that belongs to Waydroid).

## Limits

* AndroidRT runs native (NDK) games only; Java apps and the engines that start
  in Java (Unity, Unreal, Godot, GameMaker, LÖVE, SDL, Cocos2d-x) cannot run
  on it, and the store says so instead of offering them — with the route that
  works instead where there is one (a PortMaster build, PortMaster's GMLoader
  for GameMaker games). JukaHub has no Java runtime and does not try to fake
  one. Real Android apps need Waydroid, which needs a binder kernel: Linux PCs
  and community firmware, not the stock TrimUI kernel.
* Memory is shared with everything else on a 1 GB device: a launch that would
  not leave the hub alive is refused with the numbers.
* **Nothing here is verified on hardware yet.** The store's reading, triage,
  verdicts, profiles, catalogue and Waydroid probe are unit-tested with
  generated APKs and fakes; AndroidPak staging and AndroidRT have host-side
  tests and smokes. None of that proves a real game running on the handheld.

## Next slices

1. **The first device run** — the bundled runtime on a Smart Pro and a Brick:
   which display path shows a frame, then one real NativeActivity game.
2. **Verified catalogue entries** — free NDK games whose authors publish APKs
   on GitHub releases, with their SHA-256 and size, so Store rows can download.
3. **Container data** — expose Waydroid's data directory to the Save Manager
   so an app's state can be snapshotted like a game save.

## Related

* [Windows apps](WINDOWS_APPS.md) — the same integration pattern for Wine.
* [Game profiles](GAME_PROFILES.md) — the display and power model both reuse.
* [PortMaster ports](PORTMASTER.md) — where WestonPack comes from.
