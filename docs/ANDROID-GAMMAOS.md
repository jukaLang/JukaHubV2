# Android on the A133P: what GammaOS proves, and what it does not

**Status: design study, not a feature.** Nothing on this page ships in JukaHub.
The hub neither requires nor installs GammaOS; the page exists because the
[Android engine](ANDROID.md) and the [AndroidPak runtime](../player/androidrt/README.md)
both had to decide how far Android on this hardware can be pushed, and the
community firmware is the cheapest way to see the ceiling without writing a
container.

## 1. What GammaOS is

GammaOS Core and GammaOS Nano are community Android images (Android 13/14) built
for this class of Allwinner A133P handheld. They are written to the microSD card
and the card becomes the boot medium. Booting Android therefore means the device
is running GammaOS instead of the stock firmware — it is not a package, an app or
a service the hub can start.

That distinction is the whole page. "Android runs on this handheld" is true; "the
hub can host Android" is a different claim, and only the first one follows from
GammaOS.

## 2. Why it cannot be an integration point

- **Boot medium, not a package.** The SD card holds one set of boot files. A
  JukaHub boot and a GammaOS boot are two different cards, never one session.
- **No shared runtime.** While GammaOS runs, JukaHub does not: the hub is an
  application of the stock firmware, and swapping cards swaps everything,
  including `data/`, installed packages and save states.
- **The container gate is unchanged.** The Android engine integrates with
  Waydroid, which needs a binder device. Stock firmware still exposes no
  `/dev/binder`, and installing GammaOS does not change that for a stock boot.
- **Support cost moves, it does not disappear.** Updates, recovery and driver
  fixes belong to the image's maintainers; a hub feature built on top of a
  flashed OS would inherit all of that.

What the hub can do about full Android is what [ANDROID.md](ANDROID.md) already
says: integrate a container the device already has, and be explicit that a
binder-less kernel is the hard gate.

## 3. What it does say about the hardware

Three useful facts, none of which require GammaOS to be installable from here.

**The display stack works.** GammaOS drives the panel, DRM/KMS output and the
device's input, so the SoC and its display pipeline are capable of hosting a
modern Android surface stack. That is evidence about the hardware, not a path
for the hub.

**The GPU is PowerVR GE8300, and the userspace matters more than the chip.** The
Android images ship Android/bionic GPU userspace. A glibc process cannot load
bionic blobs, so an AndroidPak game cannot reuse them and the hub's surface path
has to be glibc: the KNULLI `ge8300-drivers` package or the stock firmware's own
GLES userspace. `player/androidrt/src/surface.c` measures exactly that chain —
libEGL, client extensions, GBM device, platform display, config, surface — and
prints the layer that fails instead of guessing which one is missing.

**Memory is the binding constraint.** An Android system image plus framework
takes a large share of the 1 GB before any app starts. That is the number behind
the next section: it is also the reason a game runtime has to budget RAM rather
than assume it.

## 4. A memory pre-flight for AndroidPak launches (proposal)

This is the item the study actually produced, and it is implemented.

An AndroidPak launch extracts a game's native subset (`lib/<abi>`, `assets/`,
`res/`, manifest) into a tmpfs staging directory and starts the user-installed
loader against it. tmpfs is RAM: staging a large game can leave the hub itself
without enough memory to keep drawing, and the failure mode would look like the
hub crashing rather than like a game that does not fit.

The pre-flight, in [player/androidpak.go](../player/androidpak.go), runs before
anything is extracted:

1. Read the free memory from `/proc/meminfo`.
2. Compare it against the bytes that would be staged plus a runtime budget for
   the loader and its shims, leaving a reserve for the hub.
3. Refuse the launch with the numbers in the message — free memory, staged
   bytes, reserve — instead of staging into a doomed session.
4. `JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB` (MiB) tunes the reserve, and `0` disables
   the check for someone who wants to try anyway.

The rule the study added is the one worth keeping: a launch that cannot leave the
hub alive is refused up front, by name and with numbers, rather than discovered
as a crash mid-game.

## 5. Open questions that still need a device

- Which `/dev/input/event*` nodes the firmware exposes, and whether the evdev
  reader can open them without the `input` group.
- Which GLES/EGL userspace the stock firmware actually ships, and whether the
  GE8300 driver package loads against its kernel.
- Thermals and suspend behaviour under a long Android session; neither can be
  inferred from a desktop test.
- Whether the loader's `dlopen` path survives real bionic libraries, which is
  what the smoke test and the undefined-symbol capture are for.

## Related

- [Android engine](ANDROID.md) — what ships: container detection, AndroidPak
  triage, staging and launch.
- [Game profiles](GAME_PROFILES.md) — the runtime contract AndroidPak uses.
- [androidrt runtime](../player/androidrt/README.md) — the loader, shims and
  surface probe measured against this firmware.
