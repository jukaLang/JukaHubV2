# Private C runtime on TrimUI

Stock TrimUI firmware provides glibc 2.33. Some programs need more:
GreenOvercast's ARM64 binary needs glibc 2.38, and PortMaster's scripts need a
real `bash` where the firmware's `/bin/bash` is BusyBox. JukaHub keeps a
private copy of Ubuntu 24.04's C runtime on the SD card for them. Only the
programs below start through its loader; JukaHub, firmware tools and every
other game keep the firmware's own libraries. Nothing in `/lib` is replaced.

| Program | How it uses the runtime |
|---------|-------------------------|
| GreenOvercast | A temporary copy of its PortMaster launcher starts only the game binary through the private loader |
| PortMaster and its ports | When the firmware's bash cannot run a bash array, `bash` on their `PATH` is a two-line wrapper that starts Ubuntu's bash through the private loader (see [PortMaster](PORTMASTER.md#on-a-stock-trimui)) |
| RetroArch | A game whose libretro core needs a newer glibc than the firmware's (the buildbot's newest nightlies) starts RetroArch through the private loader, in the game's own mount namespace (see [RetroArch](#retroarch) below) |

## RetroArch

A libretro core is loaded into RetroArch's own process, so it can only use the
C library RetroArch started on. The libretro buildbot builds its
`linux/aarch64` nightlies on a newer Ubuntu than TrimUI's: in November 2026
`mgba_libretro.so` needed `GLIBC_2.35`. Before this, JukaHub's core updater
refused such builds and the newest cores never reached the handheld.

Settings → Games & emulators → **RetroArch's C library** (`retroArchGlibc`):

| Choice | What happens |
|--------|--------------|
| **Automatic** (default) | Only a game whose core cannot load on the firmware's glibc and can on this runtime starts RetroArch on it (RetroArch's own menu too, once the runtime is installed, since it can load any core). The core updater and the start-up core check read this runtime's libraries first, so the newest nightlies install and are not "fixed" back |
| **JukaHub's glibc 2.39** | Every RetroArch game JukaHub starts runs on it |
| **Firmware's only** | Never; a build that needs a newer glibc is refused, and one already on the card is put back ([COMPATIBILITY](COMPATIBILITY.md#a-core-that-is-already-broken)) |

The runtime is downloaded the first time a core needs it (the game starts by
itself when it is ready); offline, the game starts on the firmware's glibc as
before.

How, without changing the firmware or the card's RetroArch
(`player/ra_glibc.go`): the game runs in a mount namespace of its own. A setup
script there bind-mounts a small wrapper over the RetroArch binary, so the
firmware's `launch.sh`, which calls `$RA_DIR/ra64.trimui` by path, starts the
wrapper; it execs the private loader with this runtime first on the library
path, then `LD_LIBRARY_PATH` as the launch script set it, then the firmware's
`/etc/ld.so.conf` folders and system folders, and the real binary as the
program. Two more bind mounts of RetroArch's folder under
`data/private-glibc/ra/ns/` keep its view of itself: the loader appears as a
file named `ra64.trimui` among RetroArch's own files, so `/proc/self/exe`, the
process name and RetroArch's `:` paths stay right. RetroArch's children
(anything it runs) use the firmware's loader and libraries as before. The
mounts exist only inside that game's namespace and end with it; JukaHub,
MainUI and the Emulators page see the card unchanged.

Before a RetroArch first runs on a runtime build, JukaHub probes it: its
libraries are listed and bound (as `ldd -r` does) once by the firmware's
loader and once through the same namespace, wrapper and private loader a game
uses. A library or symbol only the second run cannot find — or no mount
namespace, no `mount`, no `readlink` — keeps that RetroArch on the firmware's
glibc. The answer is kept per RetroArch build, runtime build and library path
in `data/private-glibc/ra/probe.json` (a refusal is asked again after a week).
A game on the runtime that stops in the loader (`symbol lookup error`,
`GLIBC_PRIVATE`) marks it too: the card says so, and cores that needed the
runtime get builds that run on the firmware.

Not covered: a game started from the firmware's own menu runs RetroArch on the
firmware's glibc, so a core that needs the runtime does not load there. On the
runtime, RetroArch's own **Restart RetroArch** closes it instead of restarting
(it re-runs `/proc/self/exe`, which is the private loader).

## What is installed

The newest build Ubuntu publishes for noble (24.04) of these arm64 packages:
`libc6` (glibc 2.39), `libgcc-s1`, `libstdc++6`, `libtinfo6` and `bash`. Their
libraries are copied flat into `<JukaHub>/data/private-glibc/runtime-<libc6
version>-<hash of all five versions>/`, bash into its `bin/`, and `data/private-glibc/current` names the
folder in use. Links in the packages are made as copies (the card is FAT).

## How JukaHub knows it is Ubuntu's

The chain is apt's, with one key (the code is shared with Linux packages):

- `dists/noble`, `noble-updates` and `noble-security` `Release` files must
  carry a valid signature by the Ubuntu Archive Automatic Signing Key (2018),
  which is embedded in JukaHub and pinned by fingerprint;
- each suite's `main/binary-arm64/Packages` index must match the SHA-256 in
  that verified `Release`;
- every `.deb` must match the SHA-256 and size in that verified index.

A `Release` dated before one JukaHub already accepted is refused, so an old,
validly signed index cannot roll the card back. HTTPS is tried first; stock
TrimUI's certificate bundle could not validate the Ubuntu server in our
device test, so plain HTTP is used after a transport failure — no byte is used
before it matched the signed chain. A build is installed only after its loader
printed its version and its bash ran a bash array on this device.

## Updates

When the **Ports** panel opens online and a runtime is installed, or a core
update needs the runtime, JukaHub asks Ubuntu for a newer build in the
background — once a session and at most once a week
(`data/private-glibc/checked`). A newer one is downloaded, checked and
probed in a new folder, then `current` points at it; the old folder stays
until the panel first opens in a later session (a port started meanwhile may
still use it). A build older than the installed one, or than the pinned
`2.39-0ubuntu8.12` below, is never taken.

## Fallback

When the signed index cannot be read at all, a first install for GreenOvercast
uses the single package pinned in the source: Ubuntu Noble's arm64
[`libc6_2.39-0ubuntu8.12_arm64.deb`](https://ports.ubuntu.com/ubuntu-ports/pool/main/g/glibc/libc6_2.39-0ubuntu8.12_arm64.deb),
exactly 2,774,244 bytes with SHA-256
`2578bc27d4e077f5e93c283d2502bc693c516d9590a20a27840b548a4d436820`, into
`data/private-glibc/glibc-2.39-0ubuntu8.12/`. That build has no bash; the next
signed install replaces it.

## Tested

The pinned loader was tested on a stock TrimUI with Linux 4.9 and glibc 2.33.
It resolved the installed GreenOvercast binary and JukaHub, ran the firmware's
`curl`, and started GreenOvercast past its glibc requirement in a display-free
test. A full GreenOvercast sign-in and stream, the signed-index install and
Ubuntu's bash running PortMaster through this code remain to be tested on a
device. GreenOvercast selected `cedar-h616` at startup on the tested A133
TrimUI, but live playback is not verified. Use 720p at 30 fps if playback
stutters.

Other ports keep the firmware compatibility check: a port whose manifest needs
a newer glibc than the firmware's is still refused unless it is GreenOvercast.

RetroArch on this runtime (above) is untested on a device: the wrapper,
bind-mount setup and probe parsing were exercised off-device with a host
loader and busybox/dash; mount namespaces, the PowerVR/Mali GPU libraries and
the TrimUI RetroArch builds under glibc 2.39 remain to be checked on the
handhelds.
