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
must be 1280x720 and the device tree the Smart Pro's.

## How it boots

1. The boot ROM finds the Smart Pro's boot0 and U-Boot on the card, at the
   same sectors as on the internal storage (the card's partition table keeps
   every stock partition in front of the system at its stock sector and
   number).
2. U-Boot reads the card's copy of its environment. The only change there:
   `root=PARTUUID=<the card's system partition> rootwait ro` and
   `init=/jukahub/init`. The builder makes this change only after checking
   that the stock environment produces the stock kernel command line, so the
   bootloader is known to take `root=` and `init=` from it. The boot image's
   ramdisk, if it has one, is dropped so the kernel mounts that partition
   itself.
3. The stock kernel mounts the system copy (squashfs, read-only) and runs
   [`init`](init) as process 1. It loads the kernel modules the stock system
   had loaded, sets the GPIO lines it had set (the sticks' power), starts the
   stock stick daemon (`trimui_inputd`), mounts the
   `JUKAHUB` partition at `/mnt/SDCARD` and runs [`launch.sh`](launch.sh).
   Wi-Fi starts after JukaHub is on screen.
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
logo (the stock one, if its format differs) shows while the kernel starts.
Copy games to `Roms/<system>` and BIOS files to `Bios` on the `JUKAHUB`
partition from a computer. `.userdata/jukahub/os.log` records when process 1
started and mounted the card, `boot.log` when JukaHub started.
`WIFI=0` in `.userdata/jukahub/boot.env` keeps Wi-Fi off.

If it does not reach JukaHub: take the card out and power on to use the stock
system, then send `os.log`/`boot.log` (if the card got that far) and the
`.build.json`. A screen that stays on the boot logo means the kernel did not
find or start the system on the card.

## Release gate

The first boots belong on a Smart Pro with a serial console on its RX/TX/GND
pads (115200 baud), to confirm what cannot be checked without hardware: boot0
copied from the eMMC loads U-Boot from the card; U-Boot says it booted from
the card and reads the card's environment (not the eMMC's); the kernel command
line shows `root=PARTUUID=` of the card's system partition and
`init=/jukahub/init`; nothing on the eMMC is mounted (`/proc/mounts`).

Then, before anyone else gets this, on a real Smart Pro: first boot to JukaHub and
its time; sticks, buttons, both shoulder pairs and the menu key; speaker,
headphones and volume; brightness; a RetroArch game with BIOS, save and
resume; Wi-Fi joining and surviving a restart; Bluetooth; sleep and wake;
clean power-off and restart from JukaHub; battery level and charging; and the
stock system still booting with the card out.
