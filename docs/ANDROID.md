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

## The ways Android could exist here, ranked

| Approach | Verdict |
|----------|---------|
| **Container integration (Waydroid)** — Android runs in a container on the device kernel; JukaHub detects, lists and launches. | **Shipped.** The only approach that runs real Android apps, and it costs JukaHub no root: the one-time root step belongs to Waydroid. |
| **QEMU / full emulation** | Not viable. There is no KVM on the handheld, so it is software emulation on a 1.8 GHz A53: single-digit FPS, plus the system image's RAM. |
| **A custom ART/Dalvik interpreter** | Out of scope. DEX execution is the smaller half; the framework, bionic and SurfaceFlinger are the rest. |
| **Sideloading APKs into the container** | Next slice. `waydroid app install <file>` is a container feature; the hub's job is validating the package and handing it over. |
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

* [Windows apps](WINDOWS_APPS.md) — the same integration pattern for Wine.
* [Game profiles](GAME_PROFILES.md) — the display and power model both reuse.
* [PortMaster ports](PORTMASTER.md) — where WestonPack comes from.
