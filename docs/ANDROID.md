# Android engine

The **Android engine** integrates with an Android container on the handheld.
It detects the container the device already has, lists the apps installed in
it, and launches them with the display bridged the same way [Windows
apps](WINDOWS_APPS.md) bridge Wine.

JukaHub does not ship a complete Android runtime. The separate AndroidPak path
supports a limited class of native-only games; its runtime source is included,
but a target-built runtime must be installed separately and real-game support
is still unverified on hardware. Running arbitrary Android apps would require
more than the loader and shims described below.

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
  (`$TMPDIR/jukahub-android/pak-<id>`, 512 MiB cap), so play sessions never
  re-read the APK from the SD card and never soak the whole package into RAM —
  `classes.dex` and signatures are never extracted.
* **Launch.** The user-installed runtime is started as `loader <apk>` or via
  its own `launch.sh`, with `ANDROIDPAK_APK`, `ANDROIDPAK_STAGED`,
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
points, and presents through the device's EGL. The host smoke suite (186
assertions, about 15 s with the host compiler) exercises the loader, the
bionic compatibility layer, the JNI environment with the Minecraft class pack,
the GameActivity bridge with a fake GameActivity glue,
the present layer against a mock GPU stack (including the stock-TrimUI
default) and its SDL-hosted backend against a fake firmware SDL2, the
looper/input queue and the OpenSL ES path — without a device.
JukaHub's AndroidPak profile support triages and stages an APK and hands it to
an installed runtime; JukaHub does not ship a prebuilt AndroidRT package
(`player/androidrt/build-tsp.sh` builds it with an aarch64 toolchain). **No
real game has been run on hardware yet.** Camera, identity, privacy and
framework features stay out of scope by design.

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

### Display: the stock firmware's own EGL

The stock TrimUI Smart Pro and Brick firmware has no DRM card: its PowerVR
GE8300 userspace draws straight to the panel, and the firmware's SDL2 reaches
it through SDL's `mali` fbdev video driver, which passes
`eglCreateWindowSurface` a `{uint16 width, height}` struct as the native
window. On that firmware (`ANDROIDPAK_DEVICE=trimui-*`, or `/usr/trimui`
present) the present layer loads the firmware's `/usr/lib/libEGL.so` first and
hands the game's window to EGL as that struct, sized to the panel (1280×720
Smart Pro, 1024×768 Brick). A DRM/KMS + GBM stack (for example a community GPU
driver package) still selects the `gbm` backend automatically.

**SDL-hosted fallback (`sdl`).** A public report from this firmware says raw
EGL contexts on the display of SDL's `mali` driver stayed black while
SDL-created ones showed. The `sdl` backend therefore lets the firmware's own
SDL2 (`/usr/lib/libSDL2-2.0.so.0`, loaded at run time — nothing links against
it) create the fullscreen window, its EGL surface and the GLES context, and
hands the game exactly those: its window surface is SDL's, its context is
SDL's (re-created when the game asks for another GLES version; a context the
game makes before its window, on a window config, builds SDL's window),
`eglMakeCurrent` becomes `SDL_GL_MakeCurrent` and every `eglSwapBuffers` an
`SDL_GL_SwapWindow`. SDL is pointed at the game's own libEGL and the GLES
library beside it, the game's config sizes (colour, depth, stencil, samples)
become SDL's GL attributes, and SDL's signal handlers stay off so JukaHub can
still stop the game. `SDL_GL_SwapWindow` returns nothing, so on this backend
a failed swap (even a lost context) never reaches the game.
`auto` switches to it by itself when the native backend fails — the game's
`eglInitialize` of the default display fails (SDL starts there and the call
is made once more), or EGL refuses the game's window surface — and says so in
the log (`backend sdl (fallback: ...)`). Only the window surface is SDL's
after that second kind of fallback when the game made its GLES context
before its window: that context stays raw EGL, its frames go through plain
`eglSwapBuffers`, and the log says so. A black screen without an error is
invisible to the runtime: then add `ANDROIDPAK_PRESENT=sdl` to the profile's
`env`, which gives the game SDL's context from its first `eglCreateContext`.

`ANDROIDPAK_PRESENT=auto|gbm|native|sdl|off`, `ANDROIDPAK_EGL_WINDOW=fbdev|null`,
`ANDROIDPAK_EGL_LIB=<path>` and `ANDROIDPAK_SDL_LIB=<path>` override each
choice (an explicit `native` never falls back, for diagnostics; an explicit
`sdl` whose SDL2 does not start is chosen like `auto`). The first
device run's `[androidrt][present] backend ...` line is the evidence of which
one works.

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

1. Build the runtime (`CROSS=aarch64-linux-gnu- player/androidrt/build-tsp.sh`)
   and copy the output to `/mnt/SDCARD/Apps/AndroidRT`.
2. Add an `androidpak` [game profile](GAME_PROFILES.md) pointing at the APK.
3. Launch it, then send back the profile's launch log: the
   `[androidrt] manifest:`, `[androidrt][present] backend`, `undefined symbol`
   and `[androidrt][jni] report:` lines say exactly what is missing.
4. If the game runs (sound, input) but the panel stays black, launch it once
   more with `ANDROIDPAK_PRESENT=sdl` in the profile's `env` and send both
   logs.

## The ways Android could exist here, ranked

| Approach | Verdict |
|----------|---------|
| **Container integration (Waydroid)** — Android runs in a container on the device kernel; JukaHub detects, lists and launches. | **Shipped.** The only approach that runs real Android apps, and it costs JukaHub no root: the one-time root step belongs to Waydroid. |
| **QEMU / full emulation** | Not viable. There is no KVM on the handheld, so it is software emulation on a 1.8 GHz A53: single-digit FPS, plus the system image's RAM. |
| **A custom ART/Dalvik interpreter** | Out of scope. DEX execution is the smaller half; the framework, bionic and SurfaceFlinger are the rest. |
| **AndroidPak — NDK games via AndroidRT** | **Profile support and runtime source shipped; experimental.** The hub triages APKs, stages the native subset to tmpfs and launches a separately built/installed runtime with its own bionic loader, JNI environment and EGL present layer; real-game hardware support is unverified ([GAME_PROFILES.md](GAME_PROFILES.md), [AndroidRT](../player/androidrt/README.md)). |
| **Sideloading APKs into the container** | Next slice. `waydroid app install <file>` is a container feature; the hub's job is validating the package and handing it over. |
| **Full Android firmware** | A separate OS/image for the device, not a JukaHub integration path. It does not change whether the stock firmware exposes the binder device required by a container. |
| **ADB bridge to a real phone** | A different product: install and drive apps on a paired phone. Not part of this engine. |

## What ships now

`player/android.go` implements the engine's core:

* **Backend detection.** Finds `waydroid`, honours `JUKAHUB_ANDROID_BACKEND`
  (only `waydroid` is supported), and checks the piece that cannot be worked
  around from userspace: a binder device (`/dev/binder`, `/dev/binderfs/binder`,
  `/dev/anbox-binder`). A stock TrimUI kernel does not expose one, and the
  engine says exactly that instead of launching something that dies inside the
  container.
* **Session state.** Reads `waydroid status` and distinguishes a running
  session from a stopped one, because a stopped session cannot launch an app.
  `androidSessionStartCommand` starts it once and keeps it for later launches.
* **Package list.** Parses `waydroid app list`, which prints `Name:` before
  `Package:`; a package-first list and a tab-separated list are accepted too,
  and duplicate packages collapse.
* **Launch.** `waydroid app launch <package>`, with the package name validated
  first. The name reaches `argv`, so it must not be readable as an option,
  a shell word or a path — `-rf`, `com.app; rm -rf /` and `com.app/../etc` are
  refused before the container is contacted.
* **Display bridge.** Android draws through Wayland, so the engine reuses
  WestonPack's `westonwrap.sh` on DRM/KMS exactly as the Wine launcher does,
  passing `XDG_RUNTIME_DIR` through so the wrapped app finds Waydroid's socket.
  There is no X11 mode: Wine can use X11, SurfaceFlinger cannot. There is no
  headless mode either — the surface *is* the app.

## Requirements

1. A kernel with the binder module (`binder_linux`). This is the hard gate.
2. Waydroid installed and initialised — a one-time root step on the device.
3. A Wayland compositor: an existing `WAYLAND_DISPLAY`, or WestonPack
   (`JUKAHUB_WESTON` points at an existing `westonwrap.sh`).

## Limits

* The engine starts apps the container already has; it does not run arbitrary
  APKs on its own.
* 3D games remain out of reach in practice: the container needs a working GPU
  stack, and the handheld's driver is not one Waydroid supports out of the box.
* Memory is shared with everything else on a 1 GB device. A heavy app can make
  the hub itself get killed while the user works inside it.
* **Nothing here is verified on hardware yet.** Waydroid detection, listing,
  launch argv, display-bridge selection and package-name validation are
  unit-tested against a fake host. AndroidPak staging and its runtime contract
  also have host-side tests/smokes, but those do not prove real APK execution.

## Next slices

1. **Panel and scene** — an **Android** scene with the runtime status card
   (backend, session, display, what is missing) and one row per package,
   following [windowsapps_ui.go](../player/windowsapps_ui.go).
2. **Per-app profiles** — reuse [game profiles](GAME_PROFILES.md) so an app
   gets a power profile and a display mode on launch.
3. **APK sideload** — read the package name, min SDK and ABI out of an APK
   before handing it to `waydroid app install`, so an incompatible file is
   refused with a reason.
4. **Container data** — expose `waydroid`'s data directory to the Save Manager
   so an app's state can be snapshotted like a game save.

## Related

* [Windows apps](WINDOWS_APPS.md) — the same integration pattern for Wine.
* [Game profiles](GAME_PROFILES.md) — the display and power model both reuse.
* [PortMaster ports](PORTMASTER.md) — where WestonPack comes from.
