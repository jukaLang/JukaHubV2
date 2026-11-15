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
slower. Later boots start JukaHub directly. The launcher records its start
time in `.userdata/jukahub/boot.log`; JukaHub records the first drawn frame
in its log. **No JukaHub first-frame boot time has been measured on hardware
yet.** The image uses JukaHub's short startup greeting to keep the first
screen on the fast path. Plastron's reported roughly two-second TortOS shelf
time does not establish JukaHub's startup time.

RetroArch's binary and configuration live in `RetroArch/`. Its cores are in
`RetroArch/.retroarch/cores`, BIOS in `Bios/`, saves in
`Saves/RetroArch/`, and games in `Roms/<system>/`. Every generated `Emus`
launcher passes that same configuration to RetroArch. The JukaHub RetroArch
menu tile does too. The image includes only systems for which a core exists.

Wi-Fi and Bluetooth startup can be requested in
`.userdata/jukahub/boot.env` with `WIFI=1` and `BLUETOOTH=1`, one per line.
They start after the splash, so they do not delay the first screen.

## Smart Pro

plastron supports the Brick, Brick Hammer and GKD Pixel 2, not the TrimUI
Smart Pro, so the Smart Pro image is built differently: it boots the Smart
Pro's own bootloader and kernel, copied from the device. See
[`../smartpro/README.md`](../smartpro/README.md). A Brick image must never be
written to a Smart Pro card, nor the Smart Pro image to a Brick.

## Release gate

Before distributing an image, boot and test it on a Brick: first boot,
subsequent boot and first-frame timing; controls and screen orientation;
speaker/headphones; a RetroArch game, BIOS, save and resume; Wi-Fi and
Bluetooth; clean power-off; and recovery by removing the card. Publish the
matching JukaHub, plastron, RetroArch and libretro-core sources and notices
with any binary image. The image builder itself has automated transformation
tests, but neither handheld is connected to this workspace for a boot test.
The included Snes9x 2010 and Genesis Plus GX cores have noncommercial terms;
an image containing them must not be sold.
