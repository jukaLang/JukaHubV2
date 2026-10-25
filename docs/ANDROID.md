# Android engine

The **Android engine** is JukaHub's integration with an Android container on the
handheld. It detects the container the device already has, lists the apps
installed in it, and launches them with the display bridged the same way
[Windows apps](WINDOWS_APPS.md) bridge Wine.

It does **not** contain an Android runtime. That is not a limitation of effort
but of what an Android app is.

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
  opts out. See [ANDROID-GAMMAOS.md](ANDROID-GAMMAOS.md#4-a-memory-pre-flight-for-androidpak-launches-proposal) —
  the item that came out of that study.
* **Input.** The runtime hands games a real `AInputQueue` fed from the
  device's `/dev/input/event*` by the evdev reader; `ANDROIDPAK_INPUT=off`
  launches without it (the game keeps running, just blind), and a node set
  the process cannot read prints the missing-`input`-group note instead of
  failing the launch. See [player/androidrt/README.md](../player/androidrt/README.md).

A **headless smoke check** of the runtime's lifecycle lives in
[`player/androidrt/tests/smoke/`](../player/androidrt/tests/smoke/) — it builds
a stub game with the host gcc and proves loader → onCreate → window → frames →
clean teardown without a device, exit 0.

The runtime itself (loader + shims) is not shipped with JukaHub yet; the
directory contract above is the integration point for the C shim layer, and a
profile without it shows exactly what is missing. Camera, identity, privacy
and framework features stay out of scope by design.

## The ways Android could exist here, ranked

| Approach | Verdict |
|----------|---------|
| **Container integration (Waydroid)** — Android runs in a container on the device kernel; JukaHub detects, lists and launches. | **Shipped.** The only approach that runs real Android apps, and it costs JukaHub no root: the one-time root step belongs to Waydroid. |
| **QEMU / full emulation** | Not viable. There is no KVM on the handheld, so it is software emulation on a 1.8 GHz A53: single-digit FPS, plus the system image's RAM. |
| **A custom ART/Dalvik interpreter** | Out of scope. DEX execution is the smaller half; the framework, bionic and SurfaceFlinger are the rest. |
| **AndroidPak — NDK-only games via the bionic translation runtime** | **Shipped as a profile runtime.** The hub triages APKs, stages the native subset to tmpfs and launches the installed loader; the loader/shims themselves are a user install ([GAME_PROFILES.md](GAME_PROFILES.md)). |
| **Sideloading APKs into the container** | Next slice. `waydroid app install <file>` is a container feature; the hub's job is validating the package and handing it over. |
| **Full Android firmware (GammaOS Core/Nano)** | Real, community-maintained, and not an integration point: an Android 13/14 image flashed to the microSD — which is then the boot medium, so a JukaHub boot and a GammaOS boot are different cards. What the study of it changes here: [ANDROID-GAMMAOS.md](ANDROID-GAMMAOS.md). |
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
* Nothing here is verified on hardware yet. Detection, listing, the launch
  argv, the display bridge and the injection guard are unit-tested against a
  fake host, which is the part that can be proven without a device.

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

* [GammaOS study](ANDROID-GAMMAOS.md) — what Android-on-A133P firmware proves and retargets here.
* [Windows apps](WINDOWS_APPS.md) — the same integration pattern for Wine.
* [Game profiles](GAME_PROFILES.md) — the display and power model both reuse.
* [PortMaster ports](PORTMASTER.md) — where WestonPack comes from.
