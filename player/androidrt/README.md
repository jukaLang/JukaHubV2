# AndroidRT — the bionic translation runtime skeleton

The C side of the **AndroidPak** Tier-1 path ([docs/ANDROID.md](../../docs/ANDROID.md)):
NativeActivity NDK games running on the Smart Pro as ordinary native
processes, with a few megabytes of runtime instead of the Android system
image the 1 GB device cannot afford.

This is a **skeleton**, not a finished runtime. Its deliverable is the
directory contract the Go launcher resolves, the honest failure messages
that name exactly which bionic symbol a game needs next, and the shims the
first games reach for. Loading *real* bionic-linked game libraries on
glibc is the milestone sequence in the next section.

## Directory contract (what `/mnt/SDCARD/Apps/AndroidRT` must look like)

```text
AndroidRT/
  loader            the runner (aarch64 ELF, main() from src/runner.c)
  launch.sh         the launcher form (used when `loader` is missing)
  shims/
    liblog.so       __android_log_* — from src/logshim.c
    libandroid.so   ANativeActivity/ANativeWindow/ALooper/AAsset/AInputQueue —
                    src/androidshim.c + src/inputshim.c
    libOpenSLES.so  OpenSL ES audio path over libasound — src/openslessim.c
```

`build-tsp.sh` produces exactly this. The Go launcher resolves the
directory via `JUKAHUB_ANDROIDRT` or the `Apps/AndroidRT`/PortMaster
candidate list, and either runs `loader <apk>` or
`sh launch.sh <apk>` — with `ANDROIDPAK_APK`, `ANDROIDPAK_STAGED`,
`ANDROIDPAK_MAINLIB`, `ANDROIDPAK_ABIS` and `ANDROIDPAK_RT` in the child
environment. For the direct-loader form the Go side itself sets
`ANDROIDPAK_SHIMS` and folds staged `lib/<abi>` + `shims/` first into
`LD_LIBRARY_PATH`; `launch.sh` arranges the same for itself when `loader`
is missing. Every shim is linked with its exact SONAME (`build-tsp.sh`
`-Wl,-soname`), so glibc satisfies a game's `DT_NEEDED
liblog.so`/`libandroid.so`/`libOpenSLES.so` straight out of the preloaded
`RTLD_GLOBAL` shims instead of resolving them to a different bionic copy
on the search path.

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
ANDROIDRT_OUT=/mnt/SDCARD/Apps/AndroidRT ./build-tsp.sh   # straight to card
```

Host-side sanity (this is Windows here; `run-smoke.sh` above also cross-builds
the runner natively with no cross toolchain):

```sh
bash tests/smoke/run-smoke.sh   # host lifecycle check, exit 0 on success
```

## What works vs. what is next

| Piece | Status |
|---|---|
| Runtime directory contract, launch.sh env, ANDROIDPAK handoff | **Done** — covered by `player/androidrt_test.go`. |
| Lifecycle bridge (`ANativeActivity` struct layout, callback order) | **Done** — proven end-to-end by the host smoke test (`tests/smoke/run-smoke.sh`, exit 0); *unproven on a real bionic game*. |
| `liblog.so` | **Done.** Full `__android_log_*` surface to stderr. |
| `libandroid.so` | **Subset.** Window/ALooper/assets finish + `AConfiguration` minimum. The *surface* is a stub: games get a real number-sized window, no pixels. |
| `libOpenSLES.so` | **Audio path done.** `slCreateEngine` → `CreateAudioPlayer` (S16 1/2-ch BufferQueue → OutputMix) → `Enqueue`/`SetPlayState` streams PCM through ALSA `default` (`snd_pcm_*` via `dlopen("libasound.so.2")`); missing libasound degrades to silence at real-time pacing instead of failing. Per-buffer callbacks fire from the worker, `Destroy` from inside a callback is safe, and unknown interfaces answer `SL_RESULT_FEATURE_UNSUPPORTED` with the real UUID table. Proven by the host audio probe; *unproven with real device sound on the A133P*. |
| Loading bionic-linked game `.so` files | **Deliberately naive** (`dlopen`). Expected to fail with a printable undefined symbol; the runner turns that into next-shim instructions. |
| PowerVR EGL surface backend | **Probe shipped** — `src/surface.c` measures the whole GBM+EGL chain (glibc GE8300 userspace per [docs/ANDROID-GAMMAOS.md](../../docs/ANDROID-GAMMAOS.md)); the swap chain/present loop is still open. |
| Input (`AInputQueue` from the TSP's controller) | **Shim shipped.** `src/inputshim.c` implements the NDK queue semantics (getEvent/hasEvents/preDispatch/finish, axis coalescing, held-slot immutability, `LOOPER_ID_INPUT` wakeups through `ALooper_pollAll`), and `src/evdev.c` reads `/dev/input/event*` on the device (`-DJUKAHUB_EVDEV`, off via `ANDROIDPAK_INPUT=off`). *Unverified on hardware*: the TSP's actual evdev nodes and the glue-thread wakeup pairing. |

## Milestone order

1. **Measure** — run the loader against a real NDK game APK (any older
   NativeActivity title), and record every undefined symbol it prints.
   JukaHub now performs this capture itself: after a non-zero AndroidPak
   exit it scans `data/game-profiles/logs/<id>.log` for the
   `[androidrt] could not load … undefined symbol:` line and names the
   symbol in a toast and the JukaHub log. The
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
   load. Context: [docs/ANDROID-GAMMAOS.md](../../docs/ANDROID-GAMMAOS.md).
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
