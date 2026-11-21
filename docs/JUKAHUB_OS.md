# JukaHub OS

JukaHub does most of what a handheld's operating system shell does, so on the
TrimUI (Smart Pro, Brick, Brick Pro, Smart Pro S) it can run **as** the OS:
**Settings → JukaHub OS**.
It is a session mode on top of the app and keeps the stock bootloader and
kernel. Leaving the session brings back the stock menu, and a reboot clears
the session's driver and library changes. On verified stock Smart Pro
firmware, *Boot into JukaHub OS* also saves and changes writable startup files
to make later boots faster; switching it off restores them.

## Modes

| Mode | What happens |
|---|---|
| **App mode** (default) | JukaHub is an app started from the firmware's menu (MainUI). This is how it always worked. |
| **OS mode** | `launch.sh` starts JukaHub as `JukaHub --os-session`. JukaHub applies the layers you switched on (below), labels itself as the OS and runs everything — games, emulators, apps — inside its session. |

**Boot into JukaHub OS** turns OS mode and *Start JukaHub at login* on
together, so the handheld powers on straight into JukaHub OS. **Start OS
session now** (or **Restart JukaHub OS** inside a session) restarts JukaHub
through `launch.sh` so a change applies at once.

**Settings → JukaHub OS → Status** reads *OS session* inside one, with what
it applied (layers, files replaced, values set, drivers); **Applied at this
start** names the layers in force and anything left out, and why. Outside a
session with OS mode on, the status line says why this start was the app
(OS mode off, the stock system chosen for this boot, not started through
`launch.sh`, …).

In a session the power card's **Exit** becomes **Return to CrossMix** (or
*TrimUI* / *NextUI* / *spruceOS*): the session's changes are undone and the firmware's menu
comes back exactly as when leaving the app. The same action is at the bottom
of Settings → JukaHub OS (press A twice).

## The OS package (`JukaHub-OS-Trimui.zip`)

Every release also carries **`JukaHub-OS-Trimui.zip`**: the same JukaHub as
`JukaHub-Trimui.zip`, laid out as an SD-card **root** that boots straight into
JukaHub OS — no need to open the app once and switch anything on.

1. Turn the handheld off and put its SD card in a computer.
2. Extract everything in the zip to the **root** of the card, merging the
   `Apps` and `System` folders with the ones already there. Choose
   **Replace all** for files that exist: an older `launch.sh` or an
   `os/os.json` with OS mode off, kept from an app install, starts JukaHub
   as the plain app.
3. Put the card back and power on.

### How it boots

The stock bootloader and kernel still run. JukaHub OS uses the firmware's
launcher loop (`runtrimui.sh`), which runs the boot
scripts in `System/starts`, then its menu, then whatever the menu queued in
`/tmp/cmd_to_run.sh`. The boot hook queues JukaHub there, and (in OS mode
since 2026-11-17, for the plain app too since 2026-11-18) it also
bind-mounts a tiny stand-in over `/usr/trimui/bin/MainUI` for this boot
(a RAM mount, gone at the next boot; no file is written). When the loop runs
"MainUI" the first time, the stand-in takes itself off and returns at once,
and the loop starts JukaHub OS through `launch.sh` with the firmware's own
environment and services — **the stock menu never shows**; the screen goes
from the firmware's boot logo to JukaHub's boot. Every later run of MainUI
(after JukaHub exits, or when another app was queued, e.g. a resume at boot)
is the real menu, so the device can never end up without one. A firmware
whose loop does not run `/usr/trimui/bin/MainUI`, or without `mount`, gets
the older way: the menu opens, and the hook closes it once it is up. So does
a boot where the stand-in has not run within 20 seconds: the hook takes it
off again.
**Settings → JukaHub OS → Skip the stock menu at boot** off (`"fastBoot":
false` in `os/os.json`) chooses the older way on purpose — try it if Wi-Fi,
brightness or volume are not set up after a boot (the stock menu may set them
up at its first start). **Settings → General → Skip the stock menu at boot**
(below **Start JukaHub at login**) is the same choice for every boot, the app's
and JukaHub OS's: off, it leaves `.autostart-keep-menu` in the JukaHub folder,
the hook lets the menu open first, and the JukaHub OS switch greys out.

`Apps/JukaHub/autostart.log` says which way each boot went ("the firmware
started JukaHub OS in place of the stock menu", or why not), and
`errors.txt` says "starting JukaHub OS" and then the session's `jukaos:`
lines — or why the start was the app instead. Once Home is up the log also
has a `boot times` line (when the firmware's launcher started, when the hook
ran, when the loop reached the menu's place, when `launch.sh` and JukaHub
started, when Home appeared), so the stock firmware's share of the boot can be
told from JukaHub's. `System/starts` is the first point the stock launcher runs
anything from the card. The verified Smart Pro speed hook mounts that card
earlier in the stock init sequence. A separate SD image
([Standalone image development](#standalone-image-development)) would have its
own init sequence, but remains untested on hardware.

On stock Smart Pro firmware, the boot hook was reached about 7.9 seconds after
kernel start in a hardware check. `launch.sh` raises read-ahead on the mounted
SD card from the stock 128 KB to 1024 KB before loading the JukaHub binary,
then restores the old value when JukaHub exits. It leaves internal eMMC
untouched. This read-ahead change alone helps after the firmware reaches the
card. The launcher also
closes the verified TrimUI quick-menu service (`trimui_osdd`) while JukaHub OS
owns the screen, so ordinary pad buttons cannot bring up TrimUI's menu over
JukaHub. JukaHub draws its own volume level when the hardware volume buttons
are pressed. It restarts that service when the stock menu returns. OS mode also
rejoins saved Wi-Fi in the background after Home, starts one supplicant if the
firmware has not started it, and obtains an address without opening Wi-Fi
Settings. These changes apply to OS sessions started through `launch.sh`,
including the OS package's boot path.

On the verified stock Smart Pro TG5040 firmware, **Boot into JukaHub OS** also
installs a reversible speed path. An early init hook mounts the SD card; the
next hook reads up to 32 MiB of JukaHub into the kernel's file cache while
other stock services start (limited to one second so slow cards do not hold
up boot); the stock launcher skips a second splash draw after U-Boot has
already shown the
boot logo; completed TRIMUI_EX setup and network services finish after
JukaHub's first frame; and a brief CPU boost ends at that frame or after a
timeout. Pending firmware updates still run before JukaHub, and a previous
failed JukaHub start leaves this boot on the stock menu. The installer accepts
only the inspected stock startup files, saves originals, and restores them
when OS boot is switched off. For an OS package installed with boot already on,
the hooks are installed after its first Home and apply from the next power-on.
Its manual recovery command is
`sh /mnt/SDCARD/Apps/JukaHub/stock_fastboot/stock-fastboot.sh restore` (use
`Apps/JukaHub-Trimui` for an app installed under that name). It changes the
firmware's writable startup overlay and the SD card, not U-Boot or the kernel.
On one device, six optimized boots showed Home 9.31–10.42 seconds after the
**kernel** began. On a fresh 2026-11-19 reboot, the A133's generic timer
measured 3.806 seconds before the kernel; the stock kernel mounted its root
at 4.182 seconds, JukaHub started at 7.410 seconds, and Home drew at 9.695
seconds after kernel start. That is approximately **13.5 seconds from chip
reset to usable Home** on this boot. The delay from pressing Power to chip
reset was not measured. In a later paired check on the
same card, Home appeared 9.39 seconds after kernel start without early file
read-ahead and 8.86 seconds with it. This is a small measured gain, not a
sub-10-second power-on result: the time before the kernel still applies.
One boot with a second boot-time Wi-Fi starter repeatedly disconnected during
association; removing that starter let JukaHub's connection keeper join the
saved network automatically with one supplicant running on two consecutive
reboots. Those boots drew Home at 9.17 and 9.23 seconds after kernel start.
The keeper now checks sooner on Smart Pro OS boots, using that same
single-supplicant guard. On two reboots with this change, Home drew at 9.17
and 8.94 seconds after kernel start, and the device became reachable over
Wi-Fi 24.6 and 23.4 seconds after the restart request. Create
`Apps/JukaHub/data/boot-prefetch.off` to disable the early file read, or
delete that file to enable it again. A boot of about two seconds needs a
different, minimal device-specific boot chain (the Brick image on plastron has
one). Reaching that range on Smart Pro would require a separate bootloader, kernel, and small system built
and tested for this board; the stock boot hook cannot skip the time before its
own Linux kernel starts.

When JukaHub starts it also checks the hook: on a card installed from this
package (`"edition": "os"`) the package's hook is kept even if the settings
already on the card had *Start JukaHub at login* off. (Before 2026-11-17 those
settings removed it at the first start, and every later boot was the stock
menu with JukaHub opened as an app.)

### What it changes — and what it does not

The kernel is the firmware's own, and so are its drivers' code: TrimUI has
published no kernel source for these handhelds, so no newer kernel module
ships in the package. What the package does set up is **how those drivers
run**: a **driver layer** per chip family (below) with driver settings the
stock kernels already have, plus the Speed layer and the OS session itself
(the power card's *Return to …*, the escape hatch, the guard). Settings →
JukaHub OS → Status then reads e.g. "Drivers: A523 driver layer (4 values)";
without a driver layer it still says "no driver layer (the firmware's own
drivers)" rather than "0 drivers loaded". A newer kernel module can only come
as a layer whose `.ko` was built for exactly this kernel (`uname -r`, e.g.
`4.9.191`) — see *Building a kernel-module pack* below.

**Driver layers.** Both are shipped and switched on; each names its models
and kernel (`"devices"`, `"kernel"`), so on the other family it reads *Not for
this handheld* and does nothing, and one card works on all four. Their values
are `sysfsIfPresent`: a value the handheld has no file for is skipped quietly
(counted in the layer's line). Each is journaled and put back like any layer
value. Source: `player/trimui/os-layers/drivers-*/layer.json`, whose `"why"`
explains every value.

| Value | `drivers-a133` (Smart Pro, Brick, Brick Pro, Hammer; 4.9) | `drivers-a523` (Smart Pro S; 5.15) | Why |
|---|---|---|---|
| `usbcore` `autosuspend` | `-1` | `-1` | USB pads and USB Wi-Fi dongles plugged in during the session are never auto-suspended (default 2 s), which old BSP kernels drop them on. |
| `bluetooth` `disable_ertm` | `Y` | `Y` | Older Xbox One pad firmware connects over Bluetooth (the setting Batocera and RetroPie use; Series pads use Bluetooth LE and do not need it). New connections only. |
| `usbhid` `jspoll` | `2` | `2` | A USB pad that reports itself as a joystick (many DirectInput pads; not Xbox pads, which `xpad` drives) is polled every 2 ms (500 Hz) instead of its own interval. The parameter came in Linux 4.12, so on the 4.9 kernels it is normally absent and skipped. Taken when a pad is plugged in; the built-in controls are not USB HID. |
| SD/eMMC `queue/scheduler` | `noop` | `none` | Flash has no seek to schedule around. The name differs between the kernels, which is why it moved here from the Speed layer. |
| GPU devfreq `max_freq` | — | `600000000` | The Mali-G57 capped at 600 MHz in every mode, so the A523 runs cooler and the fan quieter; the device is matched as `/sys/class/devfreq/*gpu*`. |

Left out on purpose: Wi-Fi power save (the AIC8800 / XR829 drivers take it as
a load-time option or through `iw`, not as a value that can be set and put
back), zram (it needs `mkswap`/`swapon`, not a value), and the CPU governor
and clocks (JukaHub's power modes own them). The fan of the Smart Pro S is the
fan setting's, not a layer's.

| On the card | What it is |
|---|---|
| `Apps/JukaHub/` | the app, byte for byte the release's `JukaHub-Trimui` folder; it updates itself like the app |
| `Apps/JukaHub/os/os.json` | `{"enabled": true, "edition": "os"}`: OS mode on (Settings → JukaHub OS's status names the edition) |
| `Apps/JukaHub/os/layers/README.txt` | where layers go |
| `Apps/JukaHub/os/layers/speed`, `drivers-a133`, `drivers-a523` | the shipped layers, all switched on (each driver layer acts only on its own family) |
| `Apps/JukaHub/stock_fastboot/` | version-checked stock Smart Pro startup acceleration and its restore command; inactive on other handhelds |
| `System/starts/jukahub-autostart.sh` | the boot hook, already installed: the exact file *Start JukaHub at login* writes, for `/mnt/SDCARD/Apps/JukaHub`; in OS mode it skips the stock menu (above) |
| `JukaHub-OS-README.txt` | install and the ways back to the stock menu |

**The Speed layer.** The package ships one layer, switched on:
`os/layers/speed` (source: `player/trimui/os-layers/speed/layer.json`). It is
load-time tuning done with values the stock kernel already has: the SD card and internal storage
(`mmcblk0`, `mmcblk1`) get the `noop` scheduler (flash has no seek to schedule
around; since 2026-11-17 set by the driver layer, as `noop` on 4.9 and `none`
on 5.15), 512 KB read-ahead and no entropy or I/O accounting work;
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
| Bootloader, kernel, firmware base files in `/usr` and `/lib` | Kept as shipped; the bootloader picture is a separately restorable image. |
| Writable `/etc` startup overlay on verified stock Smart Pro | An early SD hook and a one-line splash skip are installed with backups, then restored when OS boot is turned off. |
| SD card startup files on verified stock Smart Pro | A backed-up `TRIMUI_EX` script is wrapped to defer routine work, and a short CPU boost hook is added. Both are restored when OS boot is turned off. |
| Files a layer replaces (libraries, configs) | Bind mounts in a **private mount namespace**: only JukaHub and the programs it starts see them. MainUI, the firmware's services and everything outside never do, and the namespace disappears with the last program in it. `/` is made a *slave* mount, so new SD/USB mounts still appear inside, while the session's mounts never leak out. |
| Libraries for games (`libs`) | Put first on `LD_LIBRARY_PATH` for JukaHub (and, with `"children": true`, for the games it starts). An environment variable — gone with the process. |
| Kernel drivers (`modules`) | Loaded into the running kernel **only if built for exactly this kernel**, written to a journal in `/tmp/jukaos/journal.json` (RAM) and unloaded when the session ends. |
| System values (`sysfs`) | A short whitelist (`/proc/sys/vm/*`, `/proc/sys/kernel/sched_*`, a block device's `queue/` scheduler and read-ahead, transparent huge pages, module parameters, and a GPU's devfreq `max_freq` and `governor` — a `/sys/class/devfreq/` device whose name says `gpu`). The old value is journaled first and put back when the session ends. The CPU governor and clocks are not on the list: JukaHub's power modes own them. |
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
| `devices` | Optional: the handhelds the layer is for, as model ids (`trimui-smart-pro`, `trimui-brick`, `trimui-brick-pro`, `trimui-hammer`, `trimui-smart-pro-s`) or model codes (`TG5040`, `TG3040`, `TG4040`, `TG5050`). Anywhere else the layer reads *Not for this handheld: built for Smart Pro S, this is Brick* and nothing of it is applied; a handheld whose model cannot be told takes no layer that names models. |
| `kernel` | Optional: `uname -r` exactly (`4.9.191`) or a prefix ending at a version step (`5.15` fits `5.15.147`, `4.9.1` does not fit `4.9.191`). Same refusal otherwise. |
| `kind` | `"drivers"` marks a driver layer: the status line names it ("Drivers: A523 driver layer (4 values)"). |
| `sysfsIfPresent` | Like `sysfs`, but a value whose file this handheld does not have is skipped without a note (a driver layer covers several chips). |
| `binds` | `src` (inside the layer) is mounted over `dst` (an existing system file or folder of the same kind). Read-only unless `"rw": true` (a bind the kernel will not make read-only is left out). Refused: `src` that leaves the layer (`..`, absolute paths, symlinks pointing out); `dst` under `/proc`, `/sys`, `/dev`, `/tmp`, `/` itself, the SD card's root or anything above it, and JukaHub's own folder; an ELF library built for another machine than the file it covers. |
| `core` | Binds over the C runtime (`ld-linux*`, `libc.so*`, `libpthread*`, `libdl*`, `libm*`, `librt*`, or a folder holding them) need `"core": true`. A broken C runtime stops every program in the session, JukaHub included — the OS guard (below) is the way out. |
| `libs` | Folders (inside the layer) put first on JukaHub's library path. |
| `children` | `true`: the `libs` folders also go first for the games and apps JukaHub starts. |
| `modules` | `.ko` files, loaded in the order listed (list dependencies first). |
| `sysfs` | Path → value, from the whitelist above. One part of a path may hold `*` (`/sys/class/devfreq/*gpu*/max_freq`, for a device whose name differs between boards): it is expanded on the handheld (at most 8 files) and every file is checked against the whitelist again. |

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

### Building a kernel-module pack

Newer drivers (pads the 4.9 kernel does not know, `uinput` where it is
missing, USB Wi-Fi dongles) can only be modules built against **the exact
kernel** of one handheld. The tools are in `os/drivers/`:

1. On the handheld, `sh /mnt/SDCARD/dump-kernel-facts.sh` (read-only; it
   writes `kernel-facts/<MODEL>-<release>/` to the card): model, `uname -a`,
   `/proc/version` (the compiler), `/proc/config.gz`, the vermagic and symbol
   CRCs of a few stock modules, `/proc/modules`, `/lib/modules`, input
   devices and the kernel log's driver lines.
2. Commit that folder under `os/drivers/facts/` and run the **OS driver
   pack** workflow (`.github/workflows/os-drivers.yml`, manual only) with the
   handheld, the kernel source (git URL + ref) and the facts folder.
3. `build-driver-pack.sh` configures the tree with the device's own
   `config.gz`, builds the candidates in `os/drivers/modules.txt`
   (uinput, xpad, hid-microsoft, hid-sony, hid-playstation on 5.12+,
   rtl8xxxu, mt7601u; hid-nintendo is out of tree and not built), and writes
   `drivers-<model>-<release>/` with `layer.json` (`devices`, exact `kernel`,
   `modules` in load order) — copy it into `Apps/JukaHub/os/layers/`.

The script stops rather than ship a module the kernel would refuse: the tree
must build exactly the device's release, take its config without changing a
built-in option, a candidate that would change the built-in kernel is
skipped, the vermagic must equal the stock modules', and with MODVERSIONS
every symbol CRC shared with the saved stock modules must match. TrimUI has
published no kernel source for these models; the closest public trees are
Allwinner's A133 BSP (linux-4.9, as used by plastron and the Creality Sonic
Pad SDK) and the A523/A527/T527 linux-5.15 BSP of the board vendors (in one
tree with its `bsp/` driver folder: `REG-Linux/linux-t527`; or Radxa's
`radxa/kernel` branch `allwinner-aiot-linux-5.15` with `radxa/allwinner-bsp`
checked out as `bsp/`, which the workflow's single clone does not do).
Whether
either reproduces the firmware's CRCs is exactly what the check finds out —
until a pack passes on real facts, **no kernel-module pack exists**, and the
module list stays *candidates*.

## Per handheld

| | Smart Pro (TG5040) | Brick (TG3040) / Hammer | Brick Pro (TG4040) | Smart Pro S (TG5050) |
|---|---|---|---|---|
| Driver layer | `drivers-a133` | `drivers-a133` | `drivers-a133` | `drivers-a523` |
| Kernel | 4.9.191 | 4.9.191 | 4.9 | 5.15.147 |
| Boot into JukaHub OS | the stock launcher loop (`runtrimui.sh`, `System/starts`, MainUI stand-in) | same | same layout as the Brick on firmware 1.1.1 (unverified) | assumed the same (unverified); without `/usr/trimui/bin/MainUI` the hook closes the stock menu once it is up instead |
| Standalone image | `os/smartpro` | `os/plastron` | refused (another panel: Brick images show a striped screen) | refused by both builders (A523; `--experimental-model` only, unverified) |

Nothing on the Brick Pro and the Smart Pro S has been tested on hardware yet,
and neither have the driver layers on any model. `autostart.log` and
`errors.txt` (`jukaos:` lines) say what each boot did.

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
| `Apps/JukaHub/os/os.json` | Mode (`"enabled"`), layers switched on, `"dropCaches"`, `"fastBoot"` (skip the stock menu at boot, default on), `"edition"`. |
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

**Boot time.** Home within two seconds is a development target; it has
**not** been measured on a Smart Pro. The Smart
Pro image still uses TrimUI's bootloader and kernel, so it cannot promise that
time. It boots from the card without flashing an `.awimg` (TrimUI's format for
rewriting the internal firmware), which JukaHub OS never does. On
the images JukaHub boots with no animation unless one is picked, the launcher
shows the picture of Home kept at the last power-off as soon as the card is
mounted, and Wi-Fi and Bluetooth start after JukaHub's first frame. Each boot
is measured: `.userdata/jukahub/boot.log` has U-Boot's timings and when the
launcher, the picture and JukaHub started; JukaHub's `startup:` log line splits
the boot into the system's part and its own; Settings → JukaHub OS shows
**Last boot**. On the Smart Pro card process 1 is JukaHub's own compiled
booter, `jukaboot` (`player/cmd/jukaboot`): it starts JukaHub as soon as the
card is mounted, loads the drivers in parallel, writes a step timeline to
`os.log` and the kernel's log to `boot-dmesg.txt`, and hands over to the
start-up script if anything goes wrong; and the bootloader itself shows the
saved Home from power-on (**Show Home from power-on**). Both READMEs have the
details.
