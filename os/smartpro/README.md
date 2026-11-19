# JukaHub OS on the TrimUI Smart Pro

This builds an **experimental SD-card image** that boots the TrimUI Smart Pro
(TG5040, Allwinner A133P) straight into JukaHub. It has **not been boot-tested
on hardware yet**; the first boot on a real Smart Pro is the release gate
below.

plastron, the system under the Brick image in [`../plastron`](../plastron/README.md),
has no Smart Pro board, and TrimUI has not published the Smart Pro's bootloader
or kernel sources. So this image boots the Smart Pro's **own** bootloader,
kernel, drivers and system libraries, copied from the device itself, and
replaces only the startup and the stock menu with JukaHub. The display, GPU,
sound, sticks, Wi-Fi and battery therefore run on exactly the software that
already runs them on your device. Community systems such as Knulli also boot
the Smart Pro from SD with the stock firmware's own bootloader and kernel; they
take them from TrimUI's firmware file, this image from the device's internal
storage, which is the part the first boot has to prove (see the release gate).

**Never write a Brick image to a Smart Pro card, or this image to a Brick.**
The builder refuses a board folder that is not from a Smart Pro: the panel
must be 1280x720 and the device tree the Smart Pro's. `dump-board.sh`
records the model code (`MODEL=` in `facts.txt`: TG5040 Smart Pro, TG5050
Smart Pro S, TG3040 Brick, TG4040 Brick Pro) and stops on anything but a
TG5040; `JUKAHUB_ANY_MODEL=1 sh dump-board.sh` saves another model's files
for development, and `build_image.py --experimental-model` is the only way
to build from them. The Smart Pro S (A523) has another boot ROM, boot0
layout and kernel; nothing about booting it this way has been verified.

## How it boots

1. The boot ROM finds the Smart Pro's boot0 and U-Boot on the card, at the
   same sectors as on the internal storage (the card's partition table keeps
   every stock partition in front of the system at its stock sector and
   number).
2. U-Boot reads the card's copy of its environment. The changes there:
   `root=PARTUUID=<the card's system partition> rootwait ro`,
   `init=/jukahub/jukaboot` (or `init=/jukahub/init`, see
   [The booter](#the-booter)), and for a faster boot `quiet`, `bootdelay=0` and,
   when known, `lpj=` (see [Boot time](#boot-time)). The builder makes these
   changes only after checking that the stock environment produces the stock
   kernel command line, so the bootloader is known to take `root=` and `init=` from it. The boot image's
   ramdisk, if it has one, is dropped so the kernel mounts that partition
   itself.
3. The stock kernel mounts the system copy (squashfs, read-only) and runs
   JukaHub OS's process 1: the compiled [booter](#the-booter), or the shell
   [`init`](init) it falls back to. It mounts the `JUKAHUB` partition at
   `/mnt/SDCARD` and starts JukaHub at once (the shell init through
   [`launch.sh`](launch.sh), the booter through the same loop of its own); while JukaHub
   starts, it loads the kernel modules the stock system had loaded, sets the
   GPIO lines it had set (the sticks' power) and starts the stock stick daemon
   (`trimui_inputd`), then writes `/tmp/jukahub-display-ready`, which JukaHub
   waits for just before it opens the display. The Wi-Fi and Bluetooth modules
   and the Wi-Fi daemons wait for JukaHub's first frame (see
   [Boot time](#boot-time)).
4. Power off and restart from JukaHub go through process 1, which stops
   everything, saves the Wi-Fi networks to the card and unmounts it.

The internal eMMC is **never mounted or written**, at build time or at boot:
process 1 only uses partitions on the card (sysfs type `SD`), switches off the
kernel's hotplug helper (so no stock rule can mount a disk), and starts no
stock program except the stick daemon, the Wi-Fi pair (`wpa_supplicant`,
`udhcpc`) and any you name with `--start-daemon`. Taking the card out boots
the stock system.

## 1. Save the board files from your Smart Pro

On the Smart Pro, running its stock system (stock or CrossMix), run
[`dump-board.sh`](dump-board.sh). Either over SSH:

```sh
sh /mnt/SDCARD/dump-board.sh
```

or from the stock menu: copy the [`app`](app) folder to `Apps/JukaHubOSBoard`
on the card, put `dump-board.sh` in it, and open **Save board for JukaHub OS**.
It takes a few minutes and returns to the menu by itself;
`dump-board.log` says how it went.

Turn Wi-Fi on first if JukaHub OS should start it. The script only reads the
internal storage, and stops unless the folder it writes to is on the SD card.
It writes `JukaHub-SmartPro-board/` to the card (about the size of the system, so leave
1–2 GB free): the boot area, the partitions in front of the system, the system
files, and what the stock system loaded and ran. These are TrimUI's files from
your device: keep them and the image to yourself.

## 2. Build the image

On a Linux computer with Python 3, GNU tar, `squashfs-tools`, `exfatprogs` (or
`dosfstools`) and `xz-utils`, as root:

```sh
sudo python3 os/smartpro/build_image.py \
  --board /path/to/JukaHub-SmartPro-board \
  --app /path/to/JukaHub-Trimui \
  --stock-card /path/to/a-copy-of-your-current-card \
  --card-size 64G \
  --output JukaHub-OS-SmartPro.img.xz
```

`--app` is this repository's TrimUI package (`JukaHub-Trimui.zip`, extracted)
from a build that has `smartpro-stock-kernel-v1` in its
`native-os-capability.txt`. `--stock-card` is optional: it reuses your card's
`RetroArch`, `Emus` and `Bios` folders, which were built for this system.
`--card-size` is the size printed on the card; the `JUKAHUB` partition fills
96 % of it, so a card slightly smaller than its label still fits.

The builder checks, and stops on, anything it cannot vouch for: a board
folder that is incomplete, from another device or fails its checksums; boot0
with a bad checksum; no U-Boot package at 16400 KiB; a GPT that disagrees with
what the kernel reported; an environment that does not produce the stock
command line; a kernel that needs its ramdisk to mount a system; a missing
`devtmpfs`; a stock system without the commands process 1 uses. It reads the
finished image back (boot0, U-Boot, both partition tables) before writing the
output, and leaves a `.build.json` beside it listing the modules, daemons and
GPIO lines it will replay and the stock processes it will not start.
`--start-daemon NAME` and `--skip-daemon NAME` adjust that list. The system
copy uses the squashfs compression the stock kernel already reads (from the
stock system's own superblock), or ext4. Kernel modules are loaded without the
parameters the stock scripts may have given them.

## 3. Write and boot

Write the image to a **new or backed-up microSD card** with any image writer
(it erases the card), put it in the Smart Pro and power on. The JukaHub boot
logo shows while the kernel starts: a full-screen 1280x720 picture in the
card's copy of the `bootloader`/`boot-resource` partition, which replaces the
stock banner there (`--keep-stock-logo` keeps the banner). From then on
JukaHub writes the saved Home over the card's `bootlogo.bmp` at power-off
(never the handheld's own), so the bootloader itself shows Home from
power-on; Settings → JukaHub OS → **Show Home from power-on** turns that off,
and the image's logo is kept in `.userdata/jukahub/ubootlogo-original.bmp` and
comes back with the startup lock or a boot animation.
Copy games to `Roms/<system>` and BIOS files to `Bios` on the `JUKAHUB`
partition from a computer. `.userdata/jukahub/os.log` records when process 1
started and mounted the card, `boot.log` when JukaHub started (see
[Boot time](#boot-time)). `WIFI=0` in `.userdata/jukahub/boot.env` keeps
Wi-Fi off; JukaHub writes that line itself when Wi-Fi is switched in it, so
the next boot starts Wi-Fi the way it was left.

If it does not reach JukaHub: take the card out and power on to use the stock
system, then send `os.log`/`boot.log` (if the card got that far) and the
`.build.json`. A screen that stays on the boot logo means the kernel did not
find or start the system on the card.

## Boot time

The image keeps the Smart Pro's own boot chain, so its speed is the stock
bootloader's and kernel's plus JukaHub's start. The builder trims what it
safely can in the card's copy of U-Boot's environment, after the same check
that the environment produces the stock command line: `bootdelay=0` (no wait
at U-Boot's prompt), `quiet` in place of the stock `loglevel=8` (every kernel
message printed to the 115200-baud serial console makes the kernel wait for
the UART, which costs seconds), and `lpj=` with the stock kernel's own
delay-loop value when `dump-board.sh` found one in the boot messages (`LPJ=`
in `facts.txt`; an A133 kernel that takes it from the timer, "skipped" in
dmesg, has none to save). `.build.json` lists `kernel_lpj`.

Process 1 mounts the card first (the SD host and its partitions are in the
kernel, since the system copy on the card is the root) and starts JukaHub
right away, with `JUKAHUB_WAIT_DISPLAY=/tmp/jukahub-display-ready`. JukaHub's
own start (the Go runtime, its logs, the design file) then runs while process
1 loads the stock system's modules except the radios' (display, GPU, input,
sound and the rest), sets the GPIO lines, restores the mixer and starts the
stick daemon; when the daemon's pad has appeared (0.4 s at most) it writes the
marker, and JukaHub, which polls for it every 3 ms (8 s at most,
`player/startup_displaywait.go`), opens the display. A card whose file system
is a module (exFAT on a kernel without it built in) mounts only after the
modules, and then JukaHub starts after them, as before; so does
`EARLY_START=0` in `.userdata/jukahub/boot.env` (for an A/B on the device). A
JukaHub from before the wait fails to open the display while the drivers
load: `launch.sh` does not count that start as a failure and starts it again
once the marker is there (twice a boot at most). Process 1 also raises the
card disk's read-ahead to 1024 KB (only upwards; `READAHEAD_KB=` in boot.env,
0 leaves the kernel's): JukaHub's binary is read by page faults, and a larger
read around each fault means fewer card requests, which cost far more than
the bytes. The Wi-Fi and Bluetooth modules and the Wi-Fi daemons follow
JukaHub's first frame (twenty seconds at most). Before JukaHub is started,
`launch.sh` writes JukaHub's boot frame to the screen: the copy of Home that
JukaHub keeps (`.userdata/jukahub/bootframe.fb` and `bootframe.info`, the last
time Home was on screen and left alone, never with the startup lock on), only
when its size, depth and stride match `/sys/class/graphics/fb0` exactly. So
Home is on screen at once, over the boot logo, and JukaHub's first frame
replaces the same picture. With a display driver that is a module, `/dev/fb0`
is not there yet when `launch.sh` runs, and the boot frame is skipped
(`boot.log` says so).

What is measured, and where (each file keeps the previous boot's as `.1`):

- `os.log`: when process 1 started, when the card was ready, when JukaHub was
  started (beside or after the drivers), when the display, input and sound
  were ready (and how long the stick pad took), the read-ahead change,
  JukaHub's first frame and when the radios began (kernel uptime, seconds).
  JukaHub's own `startup: display ready after N ms` line (in `boot.log`) is
  how long it waited for the drivers.
- `boot.log`: `launcher at`, `boot frame on screen at`, `JukaHub starting at`
  (uptime when JukaHub was started).
- `/tmp/jukahub-first-frame`: written by JukaHub with the uptime of its first
  frame. Settings → JukaHub OS → Last boot shows the numbers.
- With the booter: the `timeline` block at the end of `os.log` (every step,
  each module, to the millisecond) and `boot-dmesg.txt` (the kernel's own
  messages, see below).

The stock U-Boot records no bootstage, so the time before the kernel is only
visible on the serial console. TrimUI's `.awimg` firmware files are flashed to
the internal storage; this image never writes there, so taking the card out
always boots the stock system. **No boot time has been measured on hardware
yet.**

## The booter

Process 1 on the card is **jukaboot** ([`player/cmd/jukaboot`](../../player/cmd/jukaboot)),
a small static Go program that does exactly what the shell [`init`](init)
does — the same files, markers, `boot.env` keys, log lines and shutdown — but
with system calls instead of a program started for every step, and with the
independent steps side by side:

- `proc`, `sys`, `/dev`, `/tmp` mounted directly; the hotplug helper switched
  off before anything else, so no stock rule can mount the internal storage;
  only partitions of a disk whose sysfs type is `SD` are ever used.
- The writable copies of `/etc`, `/var` and `/root` are made while the
  `JUKAHUB` partition is found and mounted.
- JukaHub starts at once, through the same loop as [`launch.sh`](launch.sh)
  (same environment, `boot.log` lines, boot frame checks and restart rules),
  written in Go. When `/dev/fb0` is not there yet (a display driver that is a
  module), the boot frame goes on the panel as soon as the driver is in, still
  before JukaHub draws.
- The kernel modules are loaded with `finit_module`, several at once along
  their dependencies (`MODULE_DEPS` in `board.env`: the stock `/proc/modules`
  "used by" column and `modules.dep`). Modules of one kind (display, GPU,
  input, sound, the rest) keep their stock order among themselves, so devices
  are numbered as at the stock boot; a GPU module waits for the display
  modules before it, and buses and regulators come first. Display, GPU,
  input and sound are preferred when several are ready. The display-ready
  marker still waits for all of them, the GPIO lines, the mixer and the stick
  daemon, as before (ten seconds at most for a module that hangs).
- Process 1's own duties: every child and orphan is reaped; busybox's
  `poweroff` (SIGUSR2), `reboot` (SIGTERM) and `halt` (SIGUSR1, and SIGPWR)
  lead to the same clean shutdown as before (halt powers off).

**Fallback.** The shell `init` stays in the image. If anything unexpected
happens before JukaHub or a stock daemon is running — the card not found, a
mount that fails, a crash in the booter — jukaboot writes why to the kernel
log and `os.log` and hands the boot to `/jukahub/init`, which starts over.
`BOOTER=0` in `.userdata/jukahub/boot.env` does the same on purpose, for an
A/B of the two; `MODULES_PARALLEL=0` keeps the booter but loads the modules
one after another in the stock order. The builder installs jukaboot only when
the TrimUI package (`--app`) has one (or `--jukaboot PATH` names one) and it
is a static AArch64 program with no loader; otherwise, or with
`--shell-init`, the card boots with the shell init as before.

**What it measures.** `os.log` gets one line for the modules ("modules: 9 of
9 loaded in 0.41 s, up to 4 at once (one by one they took 1.20 s)") and, once
the boot is done, a `timeline` block: every step and every module with its
start (seconds since boot, `CLOCK_BOOTTIME`) and how long it took. After
JukaHub's first frame the kernel's message buffer is saved to
`.userdata/jukahub/boot-dmesg.txt` (the previous one as `.1`), so the
kernel's own start can be read even though the card boots `quiet`.

**Profiling the kernel's start.** To see which kernel start-up calls cost the
most:

1. Build the image with `--boot-profile`. The command line gets
   `initcall_debug printk.time=1 log_buf_len=1M`: every kernel start-up call
   is logged with its time, in a buffer big enough to keep them all. `quiet`
   stays: it only keeps those messages off the serial console, which would
   slow the very boot being measured.
2. Boot the card once and let JukaHub come up.
3. Send `.userdata/jukahub/boot-dmesg.txt` (and `os.log`, whose
   "slowest kernel start-up calls" line lists the worst twelve).
   `jukaboot initcalls boot-dmesg.txt` prints the 25 slowest on a PC.
4. Build again with `--initcall-blacklist name1,name2` to skip calls the
   handheld does not need (C function names from those lines, at most 32;
   the builder refuses a command line over 1024 bytes). Skipping a call
   the hardware needs can stop the card from booting: the handheld itself is
   not touched, so take the card out, or write the image again without it.

## Release gate

The first boots belong on a Smart Pro with a serial console on its RX/TX/GND
pads (115200 baud), to confirm what cannot be checked without hardware: boot0
copied from the eMMC loads U-Boot from the card; U-Boot says it booted from
the card and reads the card's environment (not the eMMC's); the kernel command
line shows `root=PARTUUID=` of the card's system partition and
`init=/jukahub/jukaboot` (or `/jukahub/init`); nothing on the eMMC is mounted
(`/proc/mounts`).

Then, before anyone else gets this, on a real Smart Pro: first boot to JukaHub and
its time (os.log, boot.log); the boot frame on the second boot (Home at once,
no black flash before JukaHub's first frame); that `quiet` and `bootdelay=0`
reached the kernel (`/proc/cmdline`); sticks, buttons, both shoulder pairs and the menu key; speaker,
headphones and volume; brightness; a RetroArch game with BIOS, save and
resume; Wi-Fi joining and surviving a restart; Bluetooth; sleep and wake;
clean power-off and restart from JukaHub; battery level and charging; and the
stock system still booting with the card out.
