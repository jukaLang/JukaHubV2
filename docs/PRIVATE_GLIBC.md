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
| RetroArch that JukaHub installed | A card without RetroArch gets TrimUI's newest build for the model; every launch JukaHub makes of it runs on this runtime, whatever **Automatic** would decide (see [RetroArch on a card without one](#retroarch-on-a-card-without-one)) |

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
before. Since 2026-11-23 the complete runtime — with GCC 14's C++ library,
whose `GLIBCXX` versions (up to 3.4.33) are what the buildbot's nightlies ask
for most: 3.4.29 and 3.4.30, GCC 11's and 12's, not glibc versions — is also
installed ahead, a few minutes after JukaHub starts on a handheld whose
RetroArch can start on it, unless **Emulator updates** is Off
([COMPATIBILITY](COMPATIBILITY.md#cores-the-handheld-does-not-offer), which
also covers how the Emulators page offers only the cores that load here).

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

When a downloaded core still cannot load because the runtime could not be
used, the refusal says which of these stopped it — the setting is
**Firmware's only**, no 64-bit RetroArch beside the cores folder, the runtime
did not install (and why), the glibc-only fallback without the C++ library, no
mount namespace, or the probe's answer — before the firmware's gap
(`core … JukaHub's glibc 2.39 could not be used: …` in the log). The
Emulators page then installs JukaHub's own TrimUI build of the core, when the
`cores-aarch64` release has one ([COMPATIBILITY](COMPATIBILITY.md)); it needs
no newer C library at all.

Not covered: a game started from the firmware's own menu runs RetroArch on the
firmware's glibc, so a core that needs the runtime does not load there. On the
runtime, RetroArch's own **Restart RetroArch** closes it instead of restarting
(it re-runs `/proc/self/exe`, which is the private loader).

## RetroArch on a card without one

RetroArch is not part of the TrimUI firmware: it comes on the card, in each
model's SD base package (`RetroArch/ra64.trimui`). On a card set up without
that package — or one that lost it, or whose binary cannot start — JukaHub
installs it (`player/raget.go`).

"No RetroArch" is what the games see (`player/racard.go`): the stock
firmware's and CrossMix-OS's launch scripts (`Emus/<console>/launch.sh`) all
start `RA_DIR=/mnt/SDCARD/RetroArch` … `$RA_DIR/ra64.trimui`, so JukaHub reads
them for the binary they call and looks at that file: there, readable, and a
program for the handheld's ARM processor whose loader is on the system. When
no script names a RetroArch, the card's `RetroArch/ra64.trimui` is the one
looked at, and any RetroArch program JukaHub's wider search finds does
instead. Until 2026-11-25 the question went to a search that also took any
`retroarch` on `PATH`, in `/usr/bin` or beside JukaHub, wrapper scripts
included, so a card whose launch scripts' RetroArch was gone could still count
as having one and nothing was installed (JH-X6HRVE: "not tried this session"
under every game stopping at `ra64.trimui: not found`).

- **Which build.** TrimUI's own, from its SD base packages on GitHub:
  `trimui/assets_smartpro`, `assets_brick` and `assets_brickpro` for the A133P
  handhelds (Smart Pro, Brick, Hammer, Brick Pro: one chip, GPU and SDK), and
  `assets_smartpro_s` for the Smart Pro S. A first install takes the
  handheld's own model's newest build first (TrimUI's build for exactly this
  screen, pad and firmware, with its folder in the same package), then the
  rest of the family's, newest first. Only each package's directory and the
  files used are downloaded (HTTP range requests). Most of TrimUI's packages —
  all of the Smart Pro's, and by its set-up guides the Brick Pro's — are
  `.7z` archives, read with JukaHub's own 7z reader (`player/raget_7z.go`):
  the directory in a few range requests, then one pass over the solid blocks
  that hold RetroArch's files, decoded from their start, so most of the
  package may be downloaded (nothing but RetroArch's files is written).
  7-Zip 23 and later put its ARM64 branch filter in front of LZMA2 for every
  64-bit ARM program, ELF ones included — `ra64.trimui` and the cores — and
  the reader undoes it, and the ARM and ARM-Thumb filters (`player/sevenzip.go`;
  since 2026-11-25 — before, every such block failed). Blocks packed with the
  x86 filters (BCJ, BCJ2), PPMd or BZip2 are not decoded: that package is
  skipped with the methods in the log, and the next build is tried. The pass
  waits while a game is on screen, and a `.7z` package whose build did not
  start here, or that cannot be decoded, is passed over by later starts for a
  week (`data/retroarch-get-refused.json`; a refusal from an older decoder is
  tried again). Until 2026-11-24 a `.7z` package was skipped outright, so a
  Smart Pro could only take the Brick's November 2024 build.
- **Is it working here.** Up to three builds are downloaded and checked
  before anything is written: a 64-bit ARM program whose libraries the
  firmware's loader and this runtime's loader both list and bind (as
  `ldd -r` does), every one found and every symbol version defined. A build
  is taken when it starts on the firmware's glibc, or on this runtime where
  JukaHub can start it so (the setting allows it, a game can have a mount
  namespace). A first install prefers one that starts on the firmware's own
  glibc — the firmware's menu and every launch script can start that one too,
  and JukaHub can still start it on this runtime for a core that needs it: a
  build of the handheld's own model that starts on this runtime only (TrimUI's
  newest package can be made for a newer firmware than the handheld's — the
  Smart Pro's 20250430 package came with firmware 1.1.0 and its SDL2 2.30.8)
  is kept while that model's older builds are tried, and taken when none of
  them does. The update pass never replaces a build that starts on the
  firmware's glibc with one that does not.
- **What is written.** Only into the card's `RetroArch` folder, and only files
  the card does not have yet: the RetroArch folder of the chosen build's own
  package when it is the handheld's model's (binary, cores and settings made
  for one firmware), else of the model's newest package, else the chosen
  build's — without the menu pictures, shaders, overlays and databases when
  the card is short of space — then `ra64.trimui` last, so an install cut short
  leaves nothing to start and the next start finishes it. A `ra64.trimui` that
  is there but cannot start (another processor's, its loader missing, empty)
  is kept as `ra64.trimui.bak`. A `retroarch.cfg` from another model's package
  gets its full-screen size and custom viewport set back to `0` (the screen's
  own). `RetroArch/.jukahub-retroarch.json` records what was installed.
- **On this runtime.** The runtime goes in first, with its C++ library. Every
  launch JukaHub makes of that RetroArch — a game, netplay, its own menu —
  runs on glibc 2.39, as with **JukaHub's glibc 2.39**, once the probe above
  passed; when it refuses, RetroArch starts on the firmware's glibc.
- **When.** About a minute after start, and whenever a later look (every
  three minutes) finds the games' RetroArch gone, online and between games,
  unless Settings → Games & emulators → **Emulator updates** is Off (a note
  then says Emulators → RetroArch installs it); **A** on the Emulators page's
  RetroArch tile; **Install** on a system's tile or **Get more cores** (also
  from a game's "no emulator yet" card), which put RetroArch on first and the
  cores after it (a core that came in TrimUI's package is kept, not swapped for
  the buildbot's); or a game whose launch script could not start RetroArch —
  its card says the install is running. The
  Emulator updates pass keeps it on TrimUI's newest build: the binary only,
  the one it replaces kept as `ra64.trimui.bak`, and never from another
  model's `.7z` package (unpacking its binary costs most of that package). A
  RetroArch that came with the card, or one replaced by hand, is never
  touched while it can start. The bug report's diagnostics carry an
  `RA install` line — what JukaHub installed, this session's last try and why
  it failed, or, when none ran, what the start-up's last look found — and an
  `RA games` line: the binary the launch scripts call, whether it can start,
  and when it was last seen if it went missing during the session (a card that
  loses files has errors or holds less than it claims: Tools → System →
  **Card check**).
- **Cores before RetroArch.** Until RetroArch is there, a core installed any
  other way (Get more cores, the PS2, GameCube or J2ME set-up), or after a
  RetroArch download that failed, goes into `RetroArch/.retroarch/cores` on
  the card JukaHub runs from — the folder TrimUI's RetroArch loads from, made
  with the first core. A RetroArch whose cores folder was never made gets it
  the same way. These used to stop at "RetroArch cores folder not found"
  (`retroArchCoresHome` in `player/compat.go`).

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

It has no C++ runtime either (`libstdc++.so.6`, `libgcc_s.so.1`), so it cannot
help a RetroArch core built with a newer GCC than the firmware's (the
buildbot's PPSSPP needed `GLIBCXX_3.4.30` in November 2026). Since 2026-11-22
such a core — at install, at a game's launch, in the start-up core check —
first replaces the fallback with the signed runtime, which has both; until
then that fallback counted as installed for good and every such core stayed
refused. When the signed index still cannot be read, the refusal says so
("the copy here is glibc alone, and the complete one with the C++ library did
not install (…)"), and the Settings row reads "installed without its C++
library".

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

RetroArch on a card without one (above) is untested too: TrimUI's package
names, formats and folder layout come from its release notes and setup
guides, not from opening them, and a build from another model's package (a
Smart Pro running the Brick Pro's) is only known to load, not to fill the
screen or map the pad right. The ARM64 filter's decoding was checked against
a second, independently written form of the conversion and the ARM and
ARM-Thumb ones against liblzma, not against a package TrimUI made; reading
the launch scripts was checked on the stock firmware's and CrossMix-OS's
script text, not on a card. What a handheld shows goes in the
[hardware validation log](HARDWARE_VALIDATION.md).
