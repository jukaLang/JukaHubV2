# JukaHub OS on plastron

This build makes an **experimental, separate SD-card boot image** for the
TrimUI Brick. It has not yet been boot-tested. Its
bootloader, kernel, display, sound, input and card setup come from
[plastron](https://github.com/ericreinsmidt/plastron). JukaHub supplies the
shell, boot service and splash. RetroArch has one predictable layout on the
card.

The Brick build of plastron currently includes TortOS by default. The image
builder removes TortOS's shell, card payload, boot service and updater, then
installs JukaHub's own. It may reuse the image's separately licensed libretro
cores with their notices. The final image still needs plastron's closed Brick
GPU libraries and XR829 firmware, obtained as described in plastron's README.

**Brick (and Hammer) only.** The builder refuses `--device trimui-brick-pro`
and `--device trimui-smart-pro-s`: Brick images (NextUI, Knulli) show a
garbled, striped screen on the Brick Pro (TG4040), whose panel needs another
init, and the Smart Pro S (TG5050) is an Allwinner A523, not the A133P
plastron's bootloader and kernel are built for. The running image carries
plastron's Brick device tree, so it cannot tell a Brick Pro itself: never
write a Brick image to a Brick Pro or Smart Pro S card.

## Build a Brick image

Use a Linux machine with Python 3, `squashfs-tools`, `xz-utils` and `binutils`.
Building plastron itself also needs Docker and its Brick hardware inputs.

1. Build `brick-sdcard.img` with plastron's `make brick-kernel` and
   `make brick`, or use a checksum-verified Brick release image built by
   plastron. The latter is also accepted as `.img.xz`.
2. Build JukaHub's AArch64 TrimUI package from this version of the repository's
   release workflow and extract `JukaHub-Trimui.zip` to a directory. The image
   builder requires its `native-os-capability.txt` marker; earlier releases
   lack the native OS changes.
3. Build RetroArch against the same plastron Brick sysroot:

   ```sh
   PLASTRON_OUTPUT=/path/to/plastron-output-brick \
     sh os/plastron/build_retroarch.sh /path/to/new-retroarch-directory
   ```

   Plastron's usual build keeps that directory in its `plastron-work` Docker
   volume. From the JukaHub repository, after plastron has built its
   `plastron-builder` image, the equivalent is:

   ```sh
   mkdir -p out
   docker run --rm \
     -v plastron-work:/work -v "$PWD":/src:ro -v "$PWD/out":/out \
     plastron-builder env PLASTRON_OUTPUT=/work/output-brick \
     sh /src/os/plastron/build_retroarch.sh /out/retroarch
   ```

4. Pack the image:

   ```sh
   sudo python3 os/plastron/build_image.py \
     --base /path/to/brick-sdcard.img \
     --app /path/to/JukaHub-Trimui \
     --retroarch /path/to/new-retroarch-directory \
     --output /path/to/JukaHub-OS-brick.img.xz
   ```

   `--cores /path/to/cores` adds or replaces AArch64 libretro cores. The
   builder checks executable architecture, direct shared libraries, the
   plastron partition name and root capacity.
   `--base-sha256` can pin the exact base image. The output has a SHA-256
   manifest beside it.

5. Write the image to a **new or backed-up microSD card**. Writing it erases
   the card. On first boot plastron creates a JUKAHUB-labeled exFAT partition;
   JukaHub OS seeds `Apps/JukaHub`, `RetroArch`, `Emus`, `Roms`, `Bios` and
   `Saves`. Copy your own games, BIOS and saves to those folders afterward.
   Removing the card boots the device's original firmware.

The first boot copies the app and emulator to the new partition and will be
slower. Later boots copy and scan nothing: they start JukaHub directly (see
[Boot time](#boot-time)).

RetroArch's binary and configuration live in `RetroArch/`. Its cores are in
`RetroArch/.retroarch/cores`, BIOS in `Bios/`, saves in
`Saves/RetroArch/`, and games in `Roms/<system>/`. Every generated `Emus`
launcher passes that same configuration to RetroArch. The JukaHub RetroArch
menu tile does too. The image includes only systems for which a core exists.

Wi-Fi and Bluetooth start at boot when `.userdata/jukahub/boot.env` says
`WIFI=1` and `BLUETOOTH=1`, one per line. JukaHub writes these lines itself
whenever Wi-Fi or Bluetooth is switched on or off in it, keeping any other
line, so the next boot starts the radios the way they were left; the file can
also be edited by hand. On this image JukaHub switches Wi-Fi with plastron's
own service (`/etc/init.d/wpa_supplicant`, `wpa_cli -i wlan0`, `udhcpc`) and
starts `/etc/init.d/bluetooth` before Bluetooth's usual bring-up. The radios
start only after JukaHub's first frame, so they never delay it.

## Boot time

TortOS shows its shelf about two seconds after power-on. That speed comes
from plastron itself: its own U-Boot and TF-A built from source, a trimmed
Allwinner 4.9 kernel and a small root, all on the SD card. It does not come
from an `.awimg`: that is TrimUI's firmware-flash format, written to the
internal storage with POWER + VOL DOWN. TortOS 2.0 writes nothing to the
device, and neither does JukaHub OS: the image lives on the card, so taking
the card out always boots the untouched stock firmware, a bad image can never
leave the handheld unbootable, and nobody needs TrimUI's flashing tool.
Flashing would only change where the same bootloader and kernel are read from.

JukaHub OS uses the same fast boot chain and keeps JukaHub's own start short:

- **Boot frame.** JukaHub keeps a copy of the screen as it showed Home (the
  last time Home was on screen and left alone, taken before the power card or
  the goodbye card) in `.userdata/jukahub/bootframe.fb`, described by
  `bootframe.info` (`width height bits-per-pixel stride`). At the next
  power-on, as soon as the card is mounted, `launch.sh` stops plastron's
  splash and writes that picture to every page of the framebuffer, before
  JukaHub is started. Home is on screen at once; JukaHub's first frame
  replaces the same picture. It is only shown when its size, depth and stride
  match `/sys/class/graphics/fb0` exactly, otherwise the splash stays. With
  the startup lock on there is no boot frame (turning the lock on deletes it):
  Home must not show before the code is asked.
- **Nothing optional first.** Normal boots copy and scan nothing; seeding runs
  only when `seeded-v1` is missing. JukaHub starts before anything else, and
  Wi-Fi and Bluetooth wait for its first frame (twenty seconds at most).
- **Only the pages JukaHub uses are read ahead.** Reading the whole binary
  ahead (`cat > /dev/null`) is left out: there is no idle time on the card
  before JukaHub starts, so it would mostly read pages JukaHub never touches.
  Instead JukaHub records, on a cold start, which parts of its program and
  libraries it actually touched before its first frame
  (`Apps/JukaHub/data/boot-prefetch.list`), and at the next start a small
  loader asks the kernel for exactly those, in order and a little at a time,
  while the Go runtime is still starting (`player/internal/bootprefetch`).
  `data/boot-prefetch.off` or `JUKAHUB_BOOT_PREFETCH=0` turns it off; the
  `startup:` log line's `prefetch=` field shows what it read.
- **Bigger reads from the card.** `launch.sh` raises the read-ahead of the
  card's disk (the one `/mnt/SDCARD` is on, and only when sysfs calls it an SD
  card) to 1024 KB before anything else, never lowering it; `READAHEAD_KB=` in
  `boot.env` picks another size, `0` leaves the kernel's. JukaHub's binary is
  tens of megabytes read by page faults, and each fault then reads a larger
  piece around it in one request: an SD card's cost is per request (about a
  millisecond each), not per byte.
- **No forks it can avoid before JukaHub starts.** The launcher makes its
  folders only on the first boot, reads `boot.env` with shell builtins, and
  logs U-Boot's bootstage timings after JukaHub's first frame instead of
  before it starts (they come from the device tree, so nothing is lost).
- **Where the service starts.** plastron's `inittab` runs its `::sysinit`
  entries one after another; JukaHub's service takes TortOS's place and
  returns at once (the launcher runs in the background). `build_image.py`
  prints and records in `.build.json` (`sysinit_before_jukahub`) every entry
  that runs before it, so a device boot can show whether one of them is slow
  enough to be worth moving JukaHub ahead of it. Nothing is reordered blind:
  an entry before it may be what sets up the controls or the sound.

What is measured, and where (`.userdata/jukahub/boot.log`, renamed to
`boot.log.1` at each boot):

- `bootstage NAME mark N us`: U-Boot's own timings, in microseconds since the
  power button, from `/proc/device-tree/bootstage` (plastron's U-Boot records
  them; the lines are missing if a build does not).
- `launcher at Ns since boot`: the kernel's uptime when the card was mounted
  and `launch.sh` began; `read-ahead of mmcblkN 128 -> 1024 KB` when it was
  raised; `boot frame on screen at Ns` when the picture went up.
- `JukaHub starting at Ns since boot`: uptime when JukaHub was started.
- `JukaHub first frame at Ns since boot`: from `/tmp/jukahub-first-frame`,
  which JukaHub writes once its first frame is drawn.

Uptime starts when the kernel starts; add U-Boot's last bootstage mark for
time since power-on. Settings → JukaHub OS → Last boot shows the same numbers.
**No boot time has been measured on hardware yet.**

## Smart Pro

plastron supports the Brick, Brick Hammer and GKD Pixel 2, not the TrimUI
Smart Pro, so the Smart Pro image is built differently: it boots the Smart
Pro's own bootloader and kernel, copied from the device. See
[`../smartpro/README.md`](../smartpro/README.md). A Brick image must never be
written to a Smart Pro card, nor the Smart Pro image to a Brick.

## Release gate

Before distributing an image, boot and test it on a Brick: first boot,
subsequent boot and first-frame timing (boot.log); the boot frame (Home at
once on the second boot, no black flash before JukaHub's first frame, none
with the startup lock on); Wi-Fi and Bluetooth switched in JukaHub coming
back after a restart; controls and screen orientation;
speaker/headphones; a RetroArch game, BIOS, save and resume; Wi-Fi and
Bluetooth; clean power-off; and recovery by removing the card. Publish the
matching JukaHub, plastron, RetroArch and libretro-core sources and notices
with any binary image. The image builder itself has automated transformation
tests, but neither handheld is connected to this workspace for a boot test.
The included Snes9x 2010 and Genesis Plus GX cores have noncommercial terms;
an image containing them must not be sold.
