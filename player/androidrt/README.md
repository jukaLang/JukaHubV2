# AndroidRT — the bionic translation runtime

The C side of the **AndroidPak** Tier-1 path ([docs/ANDROID.md](../../docs/ANDROID.md)):
NativeActivity NDK games running on the Smart Pro as ordinary native
processes, with a few megabytes of runtime instead of the Android system
image the 1 GB device cannot afford.

It is **experimental and never run on hardware**: the directory contract
the Go launcher resolves, its own bionic ELF loader with a compatibility
layer, a JNI environment that answers a game's Java calls (with class packs
per game family, Minecraft first — [docs/ANDROID-MINECRAFT.md](../../docs/ANDROID-MINECRAFT.md)),
NativeActivity and androidx GameActivity entry points, an EGL present layer
that uses the stock firmware's own PowerVR libEGL (or lets the firmware's
SDL2 own the window and context), and honest failure messages that name
exactly which bionic symbol or Java member a game needs next. The host smoke
suite (about 200 assertions) is the proof so far.

**CI builds it; every TrimUI archive carries it at `Apps/JukaHub/androidrt`.**
The release's helper job (`.github/workflows/remote-play-helpers.yml`) runs
`build-tsp.sh` natively on an arm64 runner inside an Ubuntu 20.04 root (glibc
2.31, older than the Smart Pro's 2.33) and
[`../native/build-runtimes.sh`](../native/build-runtimes.sh) checks and packages
the result: AArch64, the standard glibc loader, nothing NEEDED beyond the C
library, no glibc symbol newer than 2.31, `apak_tls` at TLS offset 0, and
`loader --version` starts. JukaHub's TrimUI archives carry that folder beside the
binary, and each release also attaches it alone as
`JukaHub-AndroidRT-linux-arm64.zip`, which the APK Store installs
(`../androidrt_install.go`) for a JukaHub that has no copy. The same job runs the
host smoke suite on arm64 as an advisory step, and the weekly checks run it on
x86_64 (TEST-4).

## Directory contract

```text
androidrt/            (Apps/JukaHub/androidrt, Apps/AndroidRT, or JUKAHUB_ANDROIDRT)
  loader              the runner: the bionic loader, the compat layer and the
                      liblog/libandroid/OpenSL ES/EGL-present shims, all in one
                      aarch64 executable (src/*.c); `loader --version` proves it
                      starts on a device without running anything
  launch.sh           the launcher form (used when `loader` is missing)
  surface-probe       the milestone-3 GPU stack measurement
  props.txt.example   ro.* property overrides (copy to props.txt)
  BUILDINFO.txt       CI packages only: sources, compiler, glibc ceiling, SHA-256s
  SHA256SUMS          CI packages only: every other file, checked before an install
```

`build-tsp.sh` produces the first four. The shims are not separate libraries:
the runner is linked with `-fvisibility=hidden` and `--export-dynamic`, so it
exports exactly the game-facing Android ABI (the `ANDROIDRT_EXPORT` functions),
and its own bionic loader binds a game's `liblog.so`/`libandroid.so`/
`libOpenSLES.so` imports to those. The Go launcher (`../androidpak.go`) finds the
folder (`JUKAHUB_ANDROIDRT`, then a copy the user put on the card such as
`Apps/AndroidRT`, then the one beside the binary, then the APK Store's download)
and either runs `loader <apk>` or `sh launch.sh <apk>` — with `ANDROIDPAK_APK`,
`ANDROIDPAK_STAGED`, `ANDROIDPAK_MAINLIB`, `ANDROIDPAK_ABIS` and `ANDROIDPAK_RT`
in the child environment.

## Smoke test (host, no device needed)

`tests/smoke/run-smoke.sh` builds the runner and a fake NativeActivity
 game with the *host* gcc (no cross toolchain, works in Git Bash / MinGW
 and on Linux), stages a tree and drives the full lifecycle headlessly:

* dlopen of the fake game from the staged tree,
* `onCreate` served, paths + asset manager handed over,
* onStart → onResume → window 1280x720 → N loop frames →
  onPause → onStop → onDestroy → exit 0.

Three standalone probes print verdicts beyond the lifecycle check: the
surface probe (milestone 3), the **input probe** — it drives the same
`android_inputInject*` producer hooks the device's evdev reader feeds and
asserts the whole AInputQueue contract: `AInputQueue_getEvent` returns
`-EWOULDBLOCK` when empty, `ALooper_pollAll` reports `LOOPER_ID_INPUT`
with the queue as user data while events are pending, axis cadences
coalesce into one MOVE, an event the app holds is never overwritten,
`finishEvent` frees the slot — and the **audio probe**, which runs the
OpenSL ES shim's whole game path (engine routing, `CreateAudioPlayer`,
per-buffer callback delivery, `GetPosition` advancing through underrun,
stop dropping queued content, and `Destroy` from inside the buffer-queue
callback). No ALSA on the host is fine: the shim's silent wall-clock
pacer keeps the callback order and timing truthful.

The `dlport.c` shim (dlopen on Linux, LoadLibrary on Windows) is what lets
the device sources run two smoke paths on this repo host.

## Build

```sh
CROSS=aarch64-linux-gnu- ./build-tsp.sh            # distro cross-gcc
# or
SDK_ROOT=/opt/sdk-tg5050 ./build-tsp.sh            # TG5050 SDK toolchain
# or, on an aarch64 machine, its own gcc (what CI does)
./build-tsp.sh
ANDROIDRT_OUT=/mnt/SDCARD/Apps/AndroidRT ./build-tsp.sh   # straight to card
ANDROIDRT_VERSION=v1.2 ./build-tsp.sh              # what `loader --version` prints (default "dev")
```

A cross gcc from a current distribution links against its own, newer glibc
(2.34 and later version `pthread_create` and friends), and such a loader does
not start on the Smart Pro's glibc 2.33. The release package therefore comes
from an Ubuntu 20.04 arm64 root; to make the same package locally, run
`sh ../native/build-runtimes.sh androidrt` on an aarch64 machine with glibc 2.31 or
older (a 20.04 arm64 container works). It writes
`../native/bin/JukaHub-AndroidRT-linux-arm64.zip`, which `release.sh` and
`package-sdcard.sh` then put into the TrimUI archives.

Host-side sanity (this is Windows here; `run-smoke.sh` above also cross-builds
the runner natively with no cross toolchain):

```sh
bash tests/smoke/run-smoke.sh   # host lifecycle check, exit 0 on success
```

## What works vs. what is next

| Piece | Status |
|---|---|
| Runtime directory contract, launch.sh env, ANDROIDPAK handoff | **Done** (`player/androidpak.go`); its Go tests were retired in the November 2026 suite cut — the smoke suite drives the env contract end to end. |
| A prebuilt runtime in every release (AND-14) | **Wired, first CI run pending.** The helper job builds and checks it on arm64 (glibc 2.31), the TrimUI archives carry `androidrt/`, the release attaches `JukaHub-AndroidRT-linux-arm64.zip`; *unproven until that job has run green once*. |
| Lifecycle bridge (`ANativeActivity` struct layout, callback order) | **Done** — proven end-to-end by the host smoke test (`tests/smoke/run-smoke.sh`, exit 0); *unproven on a real bionic game*. |
| `liblog.so` | **Done.** Full `__android_log_*` surface to stderr. |
| `libandroid.so` | **Subset.** Window/ALooper/assets finish + `AConfiguration` minimum. The *surface* is a stub: games get a real number-sized window, no pixels. |
| `libOpenSLES.so` | **Audio path done.** `slCreateEngine` → `CreateAudioPlayer` (S16 1/2-ch BufferQueue → OutputMix) → `Enqueue`/`SetPlayState` streams PCM through ALSA `default` (`snd_pcm_*` via `dlopen("libasound.so.2")`); missing libasound degrades to silence at real-time pacing instead of failing. Per-buffer callbacks fire from the worker, `Destroy` from inside a callback is safe, and unknown interfaces answer `SL_RESULT_FEATURE_UNSUPPORTED` with the real UUID table. Proven by the host audio probe; *unproven with real device sound on the A133P*. |
| Loading bionic-linked game `.so` files | **Own loader** (`src/bionicld.c`, `src/elfdyn.c`): versioned `@LIBC` imports bound through the compat resolver; missing functions become named traps (`ANDROIDPAK_MISSING=trap|zero|strict`), `loader --scan lib.so` lists them. |
| Java side (JNI) | **Shipped** (`src/jni.h`, `src/jnienv.c`, `src/jnibase.c`): JNI 1.6 tables over a C object model, manifest + DEX class index (`src/apkmeta.c`), `JNI_OnLoad` in dependency order, `RegisterNatives`, once-per-name log of everything unimplemented and an exit report in the order the game asked (JukaHub's failure toast names its first entry). Class packs: Minecraft (`src/jnipack_minecraft.c`). GameActivity bridge: `src/gameactivity.c`. Smoke: `tests/smoke/fake_jnigame.c` (JNI + Minecraft pack), `tests/smoke/fake_gameact.c` (GameActivity glue). |
| PowerVR EGL present | **Shipped, mock-tested** (`src/eglpresent.c`): gbm/KMS when a DRM card exists; on the stock TrimUI firmware the firmware's `/usr/lib/libEGL.so` with SDL's mali-fbdev `{width, height}` window. **SDL-hosted backend** (`ANDROIDPAK_PRESENT=sdl`, `src/sdlhost.h`): the firmware's `libSDL2-2.0.so.0` (dlopen'ed, `ANDROIDPAK_SDL_LIB` overrides) creates the window, EGL surface and GLES context and the game gets exactly those — `eglMakeCurrent` → `SDL_GL_MakeCurrent`, `eglSwapBuffers` → `SDL_GL_SwapWindow`; `auto` falls back to it when the game's `eglInitialize` of the default display fails (retried once after SDL's video driver ran) or the native window surface is refused — a context the game made before that window stays raw EGL, and the log names `ANDROIDPAK_PRESENT=sdl`. Smoke: `tests/smoke/fake_sdl.c` (a fake firmware SDL2 that drives the mock EGL like SDL's mali driver). Which backend the device accepts is unverified. |
| Input (`AInputQueue` from the TSP's controller) | **Shim shipped.** `src/inputshim.c` implements the NDK queue semantics (getEvent/hasEvents/preDispatch/finish, axis coalescing, held-slot immutability, `LOOPER_ID_INPUT` wakeups through `ALooper_pollAll`), and `src/evdev.c` reads `/dev/input/event*` on the device (`-DJUKAHUB_EVDEV`, off via `ANDROIDPAK_INPUT=off`). *Unverified on hardware*: the TSP's actual evdev nodes and the glue-thread wakeup pairing. |

## Milestone order

1. **Measure** — run the loader against a real NDK game APK (any older
   NativeActivity title), and record every undefined symbol it prints.
   JukaHub now performs this capture itself: after a non-zero AndroidPak
   exit it scans `data/game-profiles/logs/<id>.log` for the
   `[androidrt] could not load … undefined symbol:` line and names the
   symbol in a toast and the JukaHub log — or, when the game ran, the first
   Java method/class/field the JNI layer could not provide. The
   GPU half of the same measurement now has dedicated tooling: the surface
   probe (`src/surface.c`, see milestone 3) reports which layer of this
   firmware's GPU stack works, without a game involved.
2. **Fill** — each symbol goes where it belongs: libc semantics into the
   runner's own translation pass (compare
   [bionic_translation](https://gitlab.com/android_translation_layer/bionic_translation)),
   Android services into the shims.
3. **Surface** — the PowerVR EGL backend: single fullscreen EGL window,
   swap-chain driven by `ANativeWindow_setBuffersGeometry`. The candidate
   backend is the **glibc GE8300 userspace** (the KNULLI
   [ge8300-drivers](https://github.com/knulli-cfw/ge8300-drivers) package,
   proven on Linux A133P devices, stock firmware's own GLES userspace as the
   other path) — not the Android/Bionic GPU blobs, which a glibc process cannot
   load.   Current engine scope: [docs/ANDROID.md](../../docs/ANDROID.md).
   The measurement step ships: `src/surface.c` walks the whole stack
   (libEGL → client extensions → gbm device → platform display → config →
   GBM surface → window surface) and prints exactly which layer holds; the
   runner triggers it per launch with `ANDROIDPAK_SURFACE=1`, and
   `tests/smoke/run-smoke.sh` builds the standalone probe binary. glibc
   userspace paths: install the package under the card, or use
   `ANDROIDPAK_EGL_LIB` / `ANDROIDPAK_GBM_LIB`.
4. **Audio** — **shim side done** (Enqueue/SetPlayState → libasound with
   per-buffer callbacks and graceful silent pacing; smoke-proven on the
   host). What remains is hardware verification on the A133P: the
   `default` ALSA device naming on stock firmware, latency tuning
   (`ALSA_LATENCY_US` in [src/openslessim.c](src/openslessim.c)), and
   end-to-end sound from a real bionic game.
5. **Input** — **shim side done** (AInputQueue semantics + evdev reader,
   smoke-proven on the host); what remains is hardware verification of the
   Smart Pro's evdev nodes — which `/dev/input/event*` is the controller,
   whether the reader process needs the `input` group, and whether the
   BT controllers appear as separate nodes (the mapping covers the Linux
   gamepad spec buttons/axes; anything exotic gets a `g_keys`/`g_axes`
   row like any other keymap).

## Related

* [docs/ANDROID.md](../../docs/ANDROID.md) — why Tier 1 exists and how the launcher stages APKs.
* [tests/smoke/run-smoke.sh](tests/smoke/run-smoke.sh) — one-command lifecycle check on the host.
* [../gameprofiles.go](../gameprofiles.go) — the `androidpak` profile runtime.
