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

When the **Ports** panel opens online and a runtime is installed, JukaHub asks
Ubuntu for a newer build in the background — once a session and at most once
a week (`data/private-glibc/checked`). A newer one is downloaded, checked and
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
