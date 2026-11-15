# JukaHub OS

JukaHub does most of what a handheld's operating system shell does, so on the
TrimUI (Smart Pro, Brick) it can run **as** the OS: **Settings → JukaHub OS**.
It is a session mode on top of the app, not a new firmware: nothing on the
firmware's partitions is ever written, and **a reboot always brings back the
stock system**.

## Modes

| Mode | What happens |
|---|---|
| **App mode** (default) | JukaHub is an app started from the firmware's menu (MainUI). This is how it always worked. |
| **OS mode** | `launch.sh` starts JukaHub as `JukaHub --os-session`. JukaHub applies the layers you switched on (below), labels itself as the OS and runs everything — games, emulators, apps — inside its session. |

**Boot into JukaHub OS** turns OS mode and *Start JukaHub at login* on
together, so the handheld powers on straight into JukaHub OS. **Start OS
session now** (or **Restart JukaHub OS** inside a session) restarts JukaHub
through `launch.sh` so a change applies at once.

In a session the power card's **Exit** becomes **Return to CrossMix** (or
*TrimUI* / *NextUI*): the session's changes are undone and the firmware's menu
comes back exactly as when leaving the app. The same action is at the bottom
of Settings → JukaHub OS (press A twice).

## The OS package (`JukaHub-OS-Trimui.zip`)

Every release also carries **`JukaHub-OS-Trimui.zip`**: the same JukaHub as
`JukaHub-Trimui.zip`, laid out as an SD-card **root** that boots straight into
JukaHub OS — no need to open the app once and switch anything on.

1. Turn the handheld off and put its SD card in a computer.
2. Extract everything in the zip to the **root** of the card, merging the
   `Apps` and `System` folders with the ones already there.
3. Put the card back and power on.

| On the card | What it is |
|---|---|
| `Apps/JukaHub/` | the app, byte for byte the release's `JukaHub-Trimui` folder; it updates itself like the app |
| `Apps/JukaHub/os/os.json` | `{"enabled": true, "edition": "os"}`: OS mode on (Settings → JukaHub OS's status names the edition) |
| `Apps/JukaHub/os/layers/README.txt` | where layers go |
| `System/starts/jukahub-autostart.sh` | the boot hook, already installed: the exact file *Start JukaHub at login* writes, for `/mnt/SDCARD/Apps/JukaHub` |
| `JukaHub-OS-README.txt` | install and the ways back to the stock menu |

**The Speed layer.** The package ships one layer, switched on:
`os/layers/speed` (source: `player/trimui/os-layers/speed/layer.json`). It is
the tuning fast handheld systems such as TortOS are known for, done with
values the stock kernel already has: the SD card and internal storage
(`mmcblk0`, `mmcblk1`) get the `noop` scheduler (flash has no seek to schedule
around), 512 KB read-ahead and no entropy or I/O accounting work;
`vm.vfs_cache_pressure` 50 keeps folder listings (ROM lists, box art) cached
longer; `swappiness` 10 and `stat_interval` 10 cut background work. Every
value is journaled and put back when the session ends, a missing device or
value is skipped, and the OS guard switches it off with the other layers after
two failed starts. Turn it off in Settings → JukaHub OS like any layer.

It is still not a firmware: the stock system stays on the device's own storage
and the ways out below all work. To make it an app again, turn **Boot into
JukaHub OS** off (or delete `System/starts/jukahub-autostart.sh` on a
computer); JukaHub stays in **Apps**.

**Building it.** `player/trimui/make-os-package.sh <app> [out.zip]` takes any
built TrimUI app — the `JukaHub-Trimui` folder, `JukaHub-Trimui.zip`, or
`player/dist/JukaHub-SDCARD/Apps/JukaHub` — and writes the OS zip; from the
repository root, `make os-package APP=JukaHub-Trimui.zip`. It needs bash,
`unzip` for a zip input, and `zip` or `python3` (Git Bash on Windows has the
first; use WSL or CI otherwise). The boot hook is cut out of
`player/autostart.go` (`trimuiAutostartTemplate`) and filled in the way
`trimuiAutostartScript` does, so it is the app's own hook; `SD_ROOT` changes
the card's mount point (default `/mnt/SDCARD`). The release workflow
(`docker-image.yml`, *Package Artifacts*) runs it after `JukaHub-Trimui.zip`
and attaches the result; `package-sdcard.sh` writes
`dist/JukaHub-OS-SDCARD.zip` beside its card (`WITH_OS=0` skips it). Both are
optional: a failure warns and the app packages still ship.

## What is touched, and what is not

| | |
|---|---|
| Firmware partitions, `/usr`, `/lib`, `/etc` on disk | **Never written.** |
| Files a layer replaces (libraries, configs) | Bind mounts in a **private mount namespace**: only JukaHub and the programs it starts see them. MainUI, the firmware's services and everything outside never do, and the namespace disappears with the last program in it. `/` is made a *slave* mount, so new SD/USB mounts still appear inside, while the session's mounts never leak out. |
| Libraries for games (`libs`) | Put first on `LD_LIBRARY_PATH` for JukaHub (and, with `"children": true`, for the games it starts). An environment variable — gone with the process. |
| Kernel drivers (`modules`) | Loaded into the running kernel **only if built for exactly this kernel**, written to a journal in `/tmp/jukaos/journal.json` (RAM) and unloaded when the session ends. |
| System values (`sysfs`) | A short whitelist (`/proc/sys/vm/*`, `/proc/sys/kernel/sched_*`, a block device's `queue/` scheduler and read-ahead, transparent huge pages, module parameters). The old value is journaled first and put back when the session ends. The CPU governor and clocks are not on the list: JukaHub's power modes own them. |
| Memory | Once the first screen is up the kernel's page cache is freed (`drop_caches`, page cache only) so games start with more free RAM. `"dropCaches": false` in `os/os.json` turns it off. |

The journal is undone, newest first, when JukaHub quits (the teardown shows
*Undoing JukaHub OS changes*), on **Return to …**, and — if a crash or a kill
left it behind — at the next JukaHub start, in App mode too. Because it lives in
RAM, a reboot clears it together with the changes it describes. A value someone
else changed in the meantime is left as it is; a driver still in use stays
loaded until the next reboot (the log says so).

## Layers

A layer is a folder in `Apps/JukaHub/os/layers/<id>/` with a `layer.json`.
Copying the folder there is all an install takes; open Settings → JukaHub OS
again and switch it on. The id (the folder name) may use letters, digits, `.`,
`-` and `_`.

```json
{
  "name": "SDL 2.30 for games",
  "description": "Newer SDL2 for ports that need it",
  "version": "1.0",
  "arch": "aarch64",
  "binds": [
    { "src": "files/usr/lib/libSDL2-2.0.so.0", "dst": "/usr/lib/libSDL2-2.0.so.0" },
    { "src": "files/retroarch", "dst": "/mnt/SDCARD/RetroArch/.retroarch/shaders", "rw": false }
  ],
  "libs": ["lib"],
  "children": true,
  "modules": ["drivers/8821cu.ko"],
  "sysfs": { "/proc/sys/vm/swappiness": "10" },
  "core": false
}
```

| Field | Meaning |
|---|---|
| `name`, `description`, `version` | Shown in Settings. |
| `arch` | `aarch64`, `armhf`, … or empty/`any`. A layer for another machine is greyed out with the reason (an aarch64 handheld also accepts 32-bit ARM layers; each ELF file is still checked). |
| `binds` | `src` (inside the layer) is mounted over `dst` (an existing system file or folder of the same kind). Read-only unless `"rw": true` (a bind the kernel will not make read-only is left out). Refused: `src` that leaves the layer (`..`, absolute paths, symlinks pointing out); `dst` under `/proc`, `/sys`, `/dev`, `/tmp`, `/` itself, the SD card's root or anything above it, and JukaHub's own folder; an ELF library built for another machine than the file it covers. |
| `core` | Binds over the C runtime (`ld-linux*`, `libc.so*`, `libpthread*`, `libdl*`, `libm*`, `librt*`, or a folder holding them) need `"core": true`. A broken C runtime stops every program in the session, JukaHub included — the OS guard (below) is the way out. |
| `libs` | Folders (inside the layer) put first on JukaHub's library path. |
| `children` | `true`: the `libs` folders also go first for the games and apps JukaHub starts. |
| `modules` | `.ko` files, loaded in the order listed (list dependencies first). |
| `sysfs` | Path → value, from the whitelist above. |

A part that fails a check is skipped and named in the layer's line in Settings
and in `errors.txt`; the rest of the layer still applies. One failing part never
blocks a start.

**Built-in layer — "JukaHub libraries for games"** (off by default): hands the
`libs/` folder beside JukaHub (where `launch.sh` already looks first for
JukaHub's own libraries) to the games and apps too. Drop a newer
`libSDL2-2.0.so.0` there and switch the layer on to try games against it.

## Drivers and vermagic

A kernel module only loads into the kernel it was built for. Before loading,
JukaHub reads the module's ELF `.modinfo` section and compares its `vermagic`
(e.g. `4.9.191 SMP preempt mod_unload aarch64`) with `uname -r` and the
machine: the release must match exactly. A module for another kernel is skipped
with "built for kernel X, this is Y". The kernel version is shown in the status
line of Settings → JukaHub OS. Modules are loaded with `finit_module`
(`init_module` on kernels without it); a module already loaded is left to the
system and not unloaded later.

## The escape hatch

At a power-on into JukaHub OS, **hold Select or B until the boot animation
ends**: JukaHub quits to the firmware's menu, and the rest of this boot is the
stock system's (a JukaHub opened from the menu runs as a plain app, without
layers). The next power-on is JukaHub OS again.

## Recovery

- **OS guard**: two OS starts with layers in a row that did not become healthy
  (first frame plus 90 seconds, or a clean quit) make the next starts run
  **without layers** until a layer is switched in Settings (which gives them a
  fresh start). A notice says so once.
- **Safe mode** (`--safe-mode`, `JUKAHUB_SAFE_MODE=1`, `safe-mode.flag`) starts
  without layers too.
- **Reboot**: every change is gone. Nothing persists outside
  `Apps/JukaHub/os/`.
- **By hand**: delete `Apps/JukaHub/os/os.json` (App mode) or a layer folder on
  a computer.

## Files

| Path | What |
|---|---|
| `Apps/JukaHub/os/os.json` | Mode (`"enabled"`), layers switched on, `"dropCaches"`. |
| `Apps/JukaHub/os/guard.json` | The OS guard's count. |
| `Apps/JukaHub/os/layers/<id>/layer.json` | A layer. |
| `/tmp/jukaos/journal.json` | The session's drivers and values (RAM). |
| `/tmp/jukaos/session.json` | What the session start applied and skipped (RAM). |
| `/tmp/jukaos/stock-once` | The escape hatch's "stock system for this boot" (RAM). |

## Not yet

- **A unified folder view** for RetroArch and the firmware's emulators (one
  path for ROMs, BIOS, saves whatever the firmware). A layer can already bind a
  folder over the path an emulator expects; a generated layer per firmware is a
  follow-up.
- Installing a layer from a `.zip` in Files.
- A released standalone image for both devices. Both image builders below
  still need a hardware boot and timing test.

## Standalone image development

The separate, plastron-based Brick image build lives in
[`os/plastron/README.md`](../os/plastron/README.md). It boots from an SD card
with JukaHub as its own shell. The Smart Pro image build lives in
[`os/smartpro/README.md`](../os/smartpro/README.md): plastron has no Smart Pro
board, so that card boots the Smart Pro's own bootloader, kernel and drivers,
saved from the device by `dump-board.sh`, with JukaHub's own process 1 in
place of the stock startup and menu; the internal storage is never written.
The package described above is still the stock-firmware session edition; it
is a different install.
