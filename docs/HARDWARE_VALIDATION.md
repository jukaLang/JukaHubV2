# Hardware validation log

## 2026-11-19 — Smart Pro stock firmware, JukaHub OS boot

The active OS package at `/mnt/SDCARD/Apps/JukaHub-Trimui` was checked over
SSH and with a user button test. Before the boot change, the firmware launched
at 5.85 seconds, reached the card's boot hook at 7.84 seconds, started JukaHub
at 8.89 seconds, and showed Home at 14.51 seconds. Two boots after the change
reached JukaHub at 8.91 and 9.32 seconds, then Home at 13.89 and 14.49 seconds.
JukaHub's part of boot was 5.62 seconds before and 4.98–5.17 seconds after;
the total power-to-Home time still varied with firmware startup. These few
boots suggest a modest improvement, not a stable benchmark. The stock firmware
still determines the roughly 8-second delay before anything on the card can run.

During the new OS session, the launcher raised only the SD card's read-ahead
from 128 to 1024 KB; internal eMMC stayed at 128 KB. Stopping the verified
`/usr/trimui/osd/trimui_osdd` service made the unwanted TrimUI quick menu
disappear on A and D-pad presses. The user confirmed this both in the live
service test and after a reboot with the final launcher. On a clean JukaHub
exit, the launcher restored SD read-ahead to 128 KB and the OSD service, and
the firmware returned to one MainUI. The installed launcher was backed up at
`/mnt/SDCARD/Apps/JukaHub-Trimui/.launch.sh.pre-bootfix-20261119`.

Later that day the stock 396×66, 32-bit `bootlogo.bmp` was copied to
`Apps/JukaHub-Trimui/data/bootlogo/before-fc453f0ee982.bmp` on the SD card
and to a local build folder. Its SHA-256 was
`fc453f0ee982c51490f863a7b85133df0e69b842497568bb81d5d565549c64c8`.
Only the pixels were changed; the 104600-byte length and BMP header were kept.
The JukaHub replacement's SHA-256 is
`f9e7f065c2ccd46ed9cd1e61de07008455b5aaef914c5534653c9b007fb7c130`.
It read back identically after a fresh read-only mount, and the user confirmed
the JukaHub picture and Home appeared on restart. The SD card's
`data/bootlogo/state.json` records the restore path. The app's Settings
install and restore actions themselves were not exercised in this run.

The stock kernel has no `/proc/self/pagemap`, so the installed app could not
record a boot read-ahead list. A one-time ARM helper used `mincore` after a
fresh first frame to make a valid 32 MiB list for the installed binary; the
temporary recording hook was then removed. Two boots without the list reached
Home at 12.607 and 12.890 seconds after kernel start; two with it reached
Home at 12.468 and 12.585 seconds. JukaHub's own start-to-Home share fell
from 3.90/3.71 to 3.46/3.58 seconds. The list remains on the SD card because
these tests showed no regression and a possible gain of roughly 0.2–0.3
seconds. More boots would be needed to separate that small gain from normal
variation. Source now has a `mincore` fallback for future builds, but the
installed binary was not replaced in this run.

The stock-card fast-boot pass then measured where the remaining delay came
from. The kernel saw the SD partition at about 3.4 seconds, but the stock
startup did not mount it until about 7.2 seconds. A reversible init hook at
`S41` mounted it at 5.2–5.7 seconds, before the stock launcher. The stock
`runtrimui` init script's second splash draw was skipped; U-Boot had already
shown the JukaHub picture. The installed `TRIMUI_EX` startup script was
wrapped so its completed update checks, SSH and SFTP setup run after
JukaHub's first frame. Pending updates, a previous failed JukaHub start, OS
mode off, or a request to show the stock menu run the original script
synchronously. The original startup files were saved before replacement.
No bootloader code, kernel or partition table was changed in this pass.

Before these hooks, one boot reached Home at 14.51 seconds after kernel start.
Two with early SD mount, splash skip and deferred `TRIMUI_EX` reached Home at
9.311 and 10.082 seconds. A short `performance` governor window around
JukaHub's process then reached Home at 10.422, 9.522, 9.693 and 9.498 seconds. Each
window ended at first frame, and the governor was `ondemand` afterwards.
The last boot had JukaHub running, no `trimui_osdd`, a mounted SD card and
the installed hook status. The boot-speed installer was also exercised with
restore, status, re-install and status on the live card; all returned
success. A simulated failed start then booted into one stock MainUI with its
OSD service, no JukaHub process, working SSH, and a cleared recovery marker.
The following reboot returned directly to JukaHub Home at 9.498 seconds.
These are kernel-to-Home times. The user could not time the full
power-button-to-Home path, which also includes U-Boot, so the under-10-second
end-to-end goal is not yet verified. Under five seconds is not supported by
these stock-kernel timings.

For a separate SD boot image, the Smart Pro's boot area and system were read
from internal storage and saved on the card, then copied to a private build
folder. Its U-Boot environment says `mmc_root=/dev/mmcblk0p7`, while the
running kernel used `root=/dev/mmcblk0p5`; U-Boot rewrites the root at boot.
The original image builder therefore refused a direct-root image. An SD
initramfs loader now finds the card's `rootfs` itself before running
`jukaboot`. An 8 GB experimental image was built with this path and a FAT32
data partition. Its GPT, boot image, initramfs contents, JukaHub boot logo,
root filesystem and compressed-file integrity were checked after packing.
The image has **not** been booted on hardware because no spare card was
available; its boot time and input, audio and Wi-Fi behavior remain unknown.

## 2026-11-17 — stock TrimUI Smart Pro, firmware 1.1.0

This session inspected one Smart Pro over local SSH. Port 22 answered as
Dropbear SSH; port 21 answered as FTP. The active app is
`/mnt/SDCARD/Apps/JukaHub-Trimui` (the boot hook points there), not the older
`Apps/JukaHub` path. The installed binary predates the source changes in this
session. Brief smoke runs of that binary exited normally and returned to one
MainUI. No new JukaHub build was installed. Screen, sound,
button, sleep, controller and external-service outcomes still require a
hands-on pass.

| Area | Device evidence | Result |
|---|---|---|
| Model and CPU | Device tree `sun50iw10`, `allwinner,a133`; four Cortex-A53 cores; kernel 4.9.191; one cpufreq policy, `ondemand`; `cpuinfo_max_freq=2000000` kHz | Confirms the Smart Pro A133P family. The Smart Pro hardware table now records this firmware's 2 GHz ceiling; live CPU status already reads cpufreq |
| Display and GPU | 1280×720 framebuffer; `/dev/fb0` and `/dev/disp`; `/dev/dri/renderD128` and `DRIVER=pvrsrvkm`; no `/dev/pvr_sync` or sysfs backlight | GPU status had a false missing-node result; probe now accepts the PowerVR DRM render node |
| Backlight | An ARM64 probe called `DISP_LCD_GET_BRIGHTNESS` on `/dev/disp`. The user moved the stock Settings slider; firmware `system.json` steps 5, 6, 7 and 10 gave raw 112, 133, 155 and 220. A reversible `DISP_LCD_SET_BRIGHTNESS` probe changed raw 112 to 113, read 113 back and restored 112 | The display driver read and write ioctls and firmware setting are confirmed on this Smart Pro. JukaHub's unlearned-step fallback now uses the stock maximum of 220 instead of the driver ceiling of 255. A new ARM build is needed to test JukaHub's UI path, wake restoration and firmware sync. The stock Smart Pro has no brightness shortcut; use stock Settings for that comparison |
| Boot logo | `/dev/by-name/bootloader` resolves to internal `/dev/mmcblk0p1`, a 24 MiB FAT partition. Mounted read-only, it contains `bootlogo.bmp` (104600 bytes, 396×66, 32-bit uncompressed). A copy decoded as the stock TrimUI wordmark | The installer previously refused the short banner as implausible. It now accepts exactly this format and composes a horizontal JukaHub mark. Installation, restore and power-on appearance still need a new ARM build and hands-on check; the partition was not written during this inspection |
| Firmware display owner | `/usr/trimui/osd/trimui_osdd` keeps `/dev/fb0` open beside stock MainUI; older JukaHub logs warn about it on every launch and exit | The expected OSD service is now excluded from display-conflict warnings by exact executable path |
| Power | `axp2202-battery` and `axp2202-usb` present; `/sys/power/state` offers `freeze mem`; RTC wakealarm exists and was empty; battery was discharging | Capability confirmed. The installed app's earlier log has one `suspend (mem): device or resource busy`; repeat and trace during a hands-on sleep test |
| Charge limit | The AXP2202 power supplies expose read-only `constant_charge_current`, `input_current_limit` and `charge_full`, but no charge-end threshold or charge-stop control | The proposed 80% charge limit cannot be implemented on this stock image. PWR-5 is closed and the limitation is recorded in `docs/DEVICE_TUNING.md` |
| Battery gauge | AXP2202 reports `serial_number=xpower,axp2202`, `capacity=71`, `charge_counter=3550`, `charge_full=5000`, `energy_full_design=5000`, `time_to_empty_now=15000`, and `temp=365` (36.5 °C). Its capacity counters are plain mAh, unlike standard power-supply micro units; the time figure is plausible as seconds, not minutes | Diagnostics and the Drivers page now show a 5000 mAh nominal pack rather than a false 5 mAh full capacity. Wear stays unknown because this gauge does not expose measured full capacity. The time-left reader converts the AXP2202 charge counters to micro units and ignores its mislabeled energy value |
| Audio | ALSA `audiocodec`; `amixer -c audiocodec scontrols` works; simple mixer controls `digital volume` (0–63) and `DAC volume`; `audio` group exists; kernel has `CONFIG_SND_USB_AUDIO=y`. With headphones connected, stock volume step 1 gave raw digital attenuation 45 and step 4 gave 38, matching the stock keymon formula; DAC stayed at 160. A reversible mixer write moved raw 38 to 39 and restored 38; firmware `vol` stayed at step 4 | The volume backend now selects the built-in codec by ALSA ID even if a USB DAC takes card 0, and uses this stock Smart Pro curve. The selector and curve have focused tests. No JukaHub volume write, listening pass or USB DAC playback was attempted. An earlier log has an ALSA busy/underrun error |
| Wired headphone jack | The stock Smart Pro has no `/sys/class/switch` or extcon jack state. `/proc/bus/input/devices` advertises `audiocodec sunxi Audio Jack` on `event2` with `B: SW=14` (headphone and microphone switch capabilities). A read-only `EVIOCGSW` probe returned `00` while unplugged and `04` after headphones were inserted. With headphones inserted, ALSA reported `Headphone` playback on and `LINEOUT` off | Audio-output detection now discovers an event node with a headphone/line-out switch and reads its current state by ioctl. A build of that detector run on the device reported `false` then `true`; it did not change the mixer level. Showing “Headphones” in the full JukaHub UI still needs a new ARM app build |
| Input and network | `TRIMUI Player1` on `js0`/`event3`; `/dev/uinput`; Wi-Fi on `wlan0` with `wpa_supplicant`; `/dev/ttyS1` present; Bluetooth switched off in firmware settings | Nodes confirmed. Button mapping, Bluetooth attach/audio and wake reconnection remain untested; earlier log records an unresponsive supplicant after a wake |
| Hotspot | `iw list` reports AP mode; firmware has `softap_up`, `softap_down`, `hostapd`, `udhcpd` and `dnsmasq`, plus `/etc/wifi/hostapd.conf`; the binary's usage text confirms `softap_up "ssid" "psk/open" "broadcast/hidden"` | NET-5 prerequisites and the launch arguments match this image. A phone joining JukaHub's hotspot and Wi-Fi restoration still need a live test |
| USB device mode | Configfs gadget and UDC `5100000.udc-controller` present; firmware gadget `g1` is bound with `ffs.adb`; `/dev/net/tun` exists; kernel has FunctionFS and configfs mass storage, but `CONFIG_USB_CONFIGFS_ECM`, `ECM_SUBSET` and `RNDIS` are disabled | The native MTP design has a usable FunctionFS base; PC transfer is untested. USB networking cannot use ECM/RNDIS on this stock kernel |
| Clock | `sunxi-rtc`, `/dev/rtc0` and a CA bundle present; RTC and system UTC agreed during this session | FIX-2/NET-3/NET-12 prerequisites confirmed. Cold boot retention and network correction remain untested |
| Thermal and lights | CPU, GPU, DDR and AXP2202 battery thermal zones read approximately 41, 43, 43 and 34 °C at inspection; RGB `sunxi_led*` and writable `/sys/class/led_anim/effect_enable` nodes exist | Driver nodes confirmed; no thermal, LED animation or sustained-load pass was run |
| Motor and sticks | `/sys/class/motor/voltage`, GPIO 227, `/dev/ttyS3`, `/dev/ttyS4` and `trimui_inputd` are present; the Wi-Fi interface's driver symlink resolves to `xradio_wlan` | The configured motor, stick and Wi-Fi driver paths match this firmware. Rumble strength, stick motion and reconnection still need hands-on checks |
| Kernel driver candidates | `/proc/config.gz` exists. `CONFIG_INPUT_UINPUT`, `CONFIG_JOYSTICK_XPAD`, `CONFIG_HID_MICROSOFT`, `CONFIG_HID_SONY`, `CONFIG_BT_HIDP`, and `CONFIG_SND_USB_AUDIO` are built in; `CONFIG_MODVERSIONS` and module signing are off. The optional A133 driver layer's USB autosuspend, Bluetooth ERTM and two SD-card scheduler nodes exist with stock values `2`, `N` and `[cfq]`; `usbhid` has no `jspoll` parameter | No extra module is needed for these pad and audio capabilities. The optional JukaHub OS driver layer can apply its present nodes and skips `jspoll` on this firmware; it was not activated in this session. Other proposed modules still need exact kernel source and a matching-device test before installation |
| PortMaster | `Apps/PortMaster/launch.sh`; version in `Apps/PortMaster/PortMaster/version`; installed launcher in `Data/ports`; `Roms/PORTS` empty; `Emus/PORTS/config.json` selects `launch_balanced.sh` | Port discovery already includes `Data/ports`. Firmware launcher lookup now reads the configured script when a port is under `Roms/…` |
| Port metadata | Installed `greenovercast/port.json` stores MD5 at `status.md5` (the parser already handles it); it declares `aarch64` and `min_glibc: 2.38`, while JukaHub's prior core-update logs show firmware GLIBC up to 2.33 | Installed-port compatibility now reports a newer GLIBC requirement and blocks normal launch unless the user enables the existing compatibility override |
| Command Deck | BusyBox `top -b -n 1`, `find -iname`, `ip addr` work; `uptime -p` does not, but the template's `|| uptime` fallback works. `logread` has no circular buffer even though `/var/log/messages` is current | The System log command now reads the file first and falls back to `logread` on other images. Picker, macro and pad operation remain untested |
| Stock launcher | One live `MainUI` before and after the smoke run; the installed JukaHub process exited with code 0 after SIGTERM, and MainUI returned after 1 s | The existing launch/exit path works in this short run. The old binary still reported the expected OSD service as a display holder; the source fix needs a new build |
| Image codecs | Stock `/usr/trimui/lib/libSDL2_image-2.0.so.0.2.3` reports that WebP is unsupported; no system `libwebp` is installed. Firmware FFmpeg does decode WebP | The reader gates local WebP by an actual decode probe, and browser image requests now list only formats the reader can draw. Local WebP still needs a decoder path |
| Time-lapse codecs | Stock `/usr/bin/ffmpeg` 6.1 has MJPEG and MPEG-4 encoders, but no PNG or libx264 encoder. A framebuffer grab to JPEG and a short MPEG-4 MP4 both succeeded over SSH | The recorder now uses JPEG frames and MPEG-4 MP4 in source. A new ARM build and a film of a running game are still needed |
| HTTPS tools and fonts | The firmware CA bundle at `/etc/ssl/certs/ca-certificates.crt` verifies GitHub, raw GitHub and jsDelivr, but stock `curl` rejects these hosts without `SSL_CERT_FILE` because its built-in CA path differs. A small Go program on the device found 129 trusted roots and fetched both font hosts successfully. All 20 catalogue source URLs returned 200 from the development PC; a verified Atkinson font download on the device returned a real TTF | The active launcher now exports the firmware CA path, and a short JukaHub run confirmed the environment and returned to one MainUI. The source launcher carries the fix for future packages. Lato's catalogue size estimate was corrected in source from 150 KB to about 1.3 MB |

The smoke run also logged `setterm: not found` and an ALSA underrun. The
`setterm` calls come from the stock SDL2 fbdev library, which has no matching
firmware command. A small launcher-path shim was installed in the active app
folder and its two supported calls were verified over SSH with no terminal
output. A second short app launch then exited cleanly, returned to exactly one
MainUI, and added no `setterm` errors to `errors.txt`. It still logged the
expected OSD display holder because the installed binary predates that source
fix. Older logs contain many ALSA underruns, but recent brief launches have
only a few, so audio still needs a listening test before changing its
buffering. Five old
voice-settings checksum failures are already addressed by the current source's
shape validation; that change has not reached the device binary. The
installed app's core-update log shows many nightly cores rejected for
GLIBC/GLIBCXX requirements above this firmware. The compatibility guard kept
the installed cores; a compatible update source remains to be established.

The live HTTPS launcher change kept the previous script at
`/mnt/SDCARD/Apps/JukaHub-Trimui/.launch.sh.pre-ca-20261117` for rollback.

A later 13-second launch captured JukaHub's Collections screen from the
framebuffer. It drew normally, added no ALSA underrun during that interval,
exited with code 0 after SIGTERM, and returned to one MainUI. The firmware's
`trimui_osdd` owned the ALSA hardware playback node during the capture; the
framebuffer capture itself briefly appeared in the old binary's display-holder
log because FFmpeg opened `/dev/fb0`. Its fdinfo flags were `02404000`
(read-only); the source display guard now ignores read-only framebuffer handles.
The source fix for expected OSD holders has not reached the installed binary.
Longer playback and listening tests are
still needed before changing ALSA buffering.

### Backlog coverage

There are 101 open headings tagged `[HW]` in `player/MODERNIZATION.md`. This SSH
session contributes evidence to **HW-1, HW-2, HW-3, HW-4, HW-5, HW-7, FIX-2,
SYS-1, SYS-2, SYS-16, SYS-17, SND-5, SND-6, INP-6, NET-3, NET-5, NET-6, NET-12, GAME-18, LIB-26,
MED-37** and **GPU-1**.
That is partial evidence for each, not completion. The other tagged checks
require the current build on screen, physical input or accessories, a second
model, a console, or account/server access. None are marked done by this log.

For a complete inventory, the tagged headings are:

| Area | IDs |
|---|---|
| Device passes | HW-1, HW-2, HW-3, HW-4, HW-5, HW-6, HW-7, HW-11, HW-12 |
| Mail and fixes | MAIL-1, FIX-2 |
| Performance and graphics | PERF-1, PERF-4, PERF-8, PERF-9, PERF-10, PERF-15, PERF-17, GPU-1, GPU-4, GPU-5 |
| Design | UX-5, UX-7, UX-10, UX-16 |
| Games and Remote Play | GAME-9, GAME-11, GAME-16, GAME-17, GAME-18, GAME-20, PORT-HW-2, PORT-HW-3, STEAM-HW-1, PSR-1, PSR-2, PSR-7, PSR-10, PSR-13, XBX-1 |
| Android and media | AND-1, AND-3, AND-5, AND-6, AND-17, AND-20, MED-20, MED-22, MED-25, MED-30, MED-33 |
| System and sound | SYS-1, SYS-2, SYS-3, SYS-14, SYS-15, SYS-16, SYS-17, SYS-18, SYS-22, SYS-24, SND-5, SND-6, SND-7, SND-13, SND-16 |
| Security and documentation | SEC-21 |
| Input, shell and power | INP-5, INP-6, INP-15, INP-17, INP-20, SHELL-1, SHELL-2, SHELL-11, PWR-3, PWR-10 |
| Reliability, languages and network | REL-7, REL-10, REL-12, I18N-3, NET-2, NET-3, NET-5, NET-6, NET-8, NET-12, NET-17, NET-22 |
| Library and apps | LIB-14, LIB-17, LIB-18, LIB-24, LIB-26, APP-9, APP-16, APP-17, APP-18, APP-20, APP-21, OPENWITH-HW-1, APPC-HW-1, TERM-HW-1 |
| Cloud gaming | CLOUD-1 |

### Next hands-on run

Install a compiled ARM build containing these fixes, then open **Settings → About
→ Drivers & hardware** and retain its log. Confirm the Display row is OK and
that quitting JukaHub returns to exactly one MainUI without an OSD warning or
`setterm` error. Record one time-lapse of an actual game and check its frames.
In PortMaster, check the installed GreenOvercast compatibility message, then
try a port whose GLIBC requirement the firmware meets. Test volume through
speakers and headphones, sleep/wake with the power key, Bluetooth, an actual
USB DAC, and a game launch/exit while watching the screen. The remaining
model-specific HW-7 checks need a Brick, Smart Pro S and Brick Pro.

The quickest way through the Smart Pro items above is now **Settings → About →
Device check** (DEVCHECK-HW-1): run all nine checks, save the report and paste
it from the SD card's `JukaHub reports` folder here as a new dated section. The
update self-repair has its own run (UPDFIX-HW-1), and what JukaHub records per
game while playing is checked by VERIFY-HW-1 and SAVELOG-HW-1. See
[Pick up and play](PICK_UP_AND_PLAY.md).

### Regression checks without separate task IDs

- **Launch-failure card with Submit report** (CHANGELOG Added and Fixed,
  2026-11-25). On a handheld, two failures that change nothing on the card's
  emulators: (1) an empty `Roms/GBA/empty.gba`, rescanned and started
  (Game Ready may hold it first: X plays it anyway) — RetroArch quits within
  seconds; (2) a game whose file you deleted from a PC since the last scan —
  "ROM is missing". Each
  time the card says why, its Bug report section fills in (Collecting the
  launch details… then the details), and **Submit report** is focused: A
  sends it, the card closes, and Notifications shows *Bug report sent ·
  issue #…*; the issue has the `launch-failure` label and an open **Launch
  details** block with the command, environment, files (`MISSING` for the
  deleted game), the launcher's output, RetroArch's log and JukaHub's log.
  Start the same game again: the card says it was reported at HH:MM; choose
  **Cancel** (Left, then A, or B) and nothing is sent. On the Emulators page,
  a tile whose launcher is a `#!/bin/bash` script or an extensionless program
  opens; a tile whose emulator quits at once gets the card too. Offline,
  Submit queues the report (it goes when online). A time limit, "one moment"
  and a network drive that is offline stay toasts. On Windows: Enter/Esc, the
  wheel and a click on either button work.
- **Words in other scripts in the browser** (CHANGELOG Fixed, 2026-11-25).
  On Windows open wikipedia.org: 日本語, 中文, Русский and فارسی read in their
  own letters (the Persian word joined and right to left), and no donation
  bar is laid out under "Read Wikipedia in your language". On a handheld with
  Settings → General → Language set to 日本語 once (so the Noto face is on
  the card), the Japanese and Chinese names draw too; without it they stay
  "…". A Latin-only page is unchanged.
- **html5test.co header and index menu** (CHANGELOG Fixed, 2026-11-25).
  Open html5test.co: no blue index menu covers the left of the page; "your
  browser", "other browsers" and "compare" sit in one row on the left and
  "about the test" on one line at the right. A site whose menu list is reset
  to `padding: 0` shows its items flush with the column, and a page with a
  skip link or a slide-in drawer shows neither until it is opened.
- **CPU frequency set by hand** (CHANGELOG Added, 2026-11-25). On a Smart Pro
  open Settings → Power & battery → CPU frequency: A lists Automatic and the
  chip's clocks (2000 … 408 MHz). Pick 1200 MHz: within a second the row reads
  "1200 MHz in menus and games · now 1200 MHz", `cat
  /sys/devices/system/cpu/cpu0/cpufreq/scaling_{min,max}_freq` gives 1200000
  twice, the log has `CPU frequency set by hand: 1200 MHz`, and pressing around
  Home no longer lifts the clock (Faster menus says "Resting"). Start a GBA and
  an N64 game: about 3 s in both read 1200000 (CPU boost's row says "Resting"),
  and on exit the hub is back at 1200 MHz. Turn **Slow down when idle** on: on
  Home the min goes back to the firmware's value and `scaling_cur_freq` drops
  while idle. Set **CPU frequency for** → Games only: Home goes back to
  Automatic (Automatic CPU speed's row shows its own line again). Leave the idle
  screen up 5 s and wake it: the clock comes back. Pick Automatic: the min file
  returns to the firmware's value, the max to the level the Automatic CPU speed
  row names, and the log says `CPU frequency: automatic again`. On a Smart Pro
  S check both clusters (`policy0`, `policy4`) follow.
  A game with its own CPU limit in its options sheet keeps that limit.

- **Notes on the on-screen keyboard, as files and on the phone** (CHANGELOG
  Added, 2026-11-25). On a Smart Pro with no keyboard: Notes → **Y** opens the
  keyboard on the new note's title with "New Note" selected; type a title,
  **Next** moves to the body; type two lines with **New line** between them and
  a sentence long enough to wrap — the note above the sheet shows both lines,
  wrapped, the caret where the next letter goes, and the sheet's field shows the
  caret's line; **Start** closes and the list shows the note. Reopen it: **A**
  on the body, **B** on the keyboard, **B** again — nothing typed is lost.
  **L1/R1** move it between folders. **Y** in the note → **Save as file**:
  `SD card/Notes/<folder>/<title>.txt` exists and opens on a PC; save again
  after renaming the title — the old file is gone, the new one there. Select on
  the list → **Open on phone**: the phone's camera opens the Notes page at the
  list; edit a note there and save — the handheld's list shows it at once; with
  that note open (and untouched) on the handheld its editor follows; type on
  both and save on the handheld last — a "… (phone copy)" note appears. Add a
  `.txt` file from the phone, download one note and the `.zip`. **X** on the
  list asks before deleting.
- **QR maker Save image** (CHANGELOG Added, 2026-11-25). Tools → QR maker, any
  text, **Show**, then **X**: a toast names `screenshots/qr_….png`; the file is
  a clean black-on-white code a phone scans from a PC screen. The pairing code
  (Home's header QR) has no Save hint and **X** closes it.
- **Theme wallpapers** (CHANGELOG Added, 2026-11-25). On a Smart Pro with no
  wallpaper picked: Settings → Theme & look → Theme presets → Synthwave — Neon
  Horizon is under the glass (toast "… · Neon Horizon wallpaper"); Paper → Paper
  Garden; Dark → the glass backdrop again. Restart: the theme's wallpaper is
  still there. Pick your own picture in Wallpaper, then Midnight: yours stays.
  The picker's **JukaHub wallpapers** row lists the eight. On a Brick the
  pictures are cropped, not squeezed. Experience → Retro Arcade brings Neon
  Horizon; Undo puts back what was there.
- **Wine test** (CHANGELOG Added, 2026-11-25). With the Windows runtime
  installed, Windows Apps lists **Wine Test (built in)**; **A** opens a
  full-screen window "Wine is working!" with the Wine version, Linux and the
  kernel, Windows 10, x64 · translated by Box64, 1280 × 720 and the memory; the
  ball moves and the clock ticks; the stick moves the pointer and **A**/**B**/
  **X**/L2/R2/D-pad/Start/L1/R1 are named on the "Last input" line; a chime
  plays (or the sound line says why not); **Y**, the Close button or Select +
  Start closes it and a toast says "Wine test passed…". The app's log has the
  `jukahub-winetest:` lines. Try the 1024 × 768 Brick too (it scales and
  centres).
- **Video memory (VRAM)** (CHANGELOG Added, 2026-11-25). On a Smart Pro set
  Settings → Games & emulators → Video memory (VRAM) to 512 MB, scroll the
  library's covers and open a web page, then start a PlayStation or SNES game
  (one Heavy-game boost leaves alone). With less than 512 MB free the log has
  `video memory: 512 MB assigned, … JukaHub's textures and caches emptied` and
  `freed … for video memory`, and back on Home the row reads "Made room for the
  last game: … free"; with room already it reads "The last game started with …
  free". Covers and an animated wallpaper come back (it animates again) and the
  game ran as before; with Auto there is no `video memory` line. Start the Wine
  test in Windows Apps with 256 MB set: the log says `Windows programs see 256
  MB of video memory`.
- **Smart Home tab** (CHANGELOG Added, 2026-11-25). On the Smart Pro and on
  Windows, with an old Kasa plug, a Tapo plug or bulb and a Nest speaker or
  Chromecast on the same Wi-Fi: the Smart Home tab sits after Entertainment;
  within a few seconds the Kasa plug and the speaker appear by name and the
  Tapo device says "Sign in with your TP-Link ID". Add devices › TP-Link ›
  Sign in on this device (the password shows as dots), and once from the phone
  page: a wrong password is refused naming the device, the right one makes the
  Tapo device switchable. A on a plug or bulb switches it (the tile at once,
  the device right after); X on a bulb: Brighter, Dimmer, Warm white; A on the
  speaker while music plays: Pause, Play, Volume up and down, Mute, Stop.
  Unplug a device: its tile says Not answering. Hide one and bring it back from
  Add devices › Hidden devices. After a restart the devices are listed at once.
  Home Assistant from the phone page: a wrong token is refused, a right one
  adds its lights and switches. A Tapo device on the newest firmware says
  "Needs Third-Party Compatibility" and works after that switch in the Tapo
  app and Find devices. On another Home layout, Start → Smart Home lists the
  same devices in the panel.
- **DuckDuckGo Lite results** (CHANGELOG Fixed, 2026-11-25). In the
  browser search "hello", "cnn.com" and "google chrome" with DuckDuckGo: each
  title is one line, one row tall, right beside its number; the snippet and
  the green address start under the title; results are parted by a blank
  line; "10." is whole. The top "Next Page >" button sits at the left edge
  (not mid-screen). The search box is a grey-rimmed pill as tall as the blue
  Search pill, whose label lines up with the box's text; the region and time
  selects are two rimmed pills; there is a gap above the logo and between
  the logo, the form and the results. A page with a plain table, and a
  form with a text field and a button, still look as before.
- **First-run tour fits its card** (CHANGELOG Fixed, 2026-11-25). Settings →
  General → Replay tour on Windows (1280×720), on a Brick (960×720 canvas) and
  once at Accessibility text size 145 %: on all 13 pages no text runs past the
  card or across a row's border, "Open date & time settings" is whole, and the
  controls hint under the card stays clear of it.
- **Unpacking through a rooted folder** (CHANGELOG Changed, 2026-11-25). On
  the Smart Pro and on Windows: a self-update, a package from the Patch
  screen, a port and the Remote Play helpers still install; the log shows no
  `open staging dir` or `path escapes` error.
- **Leaving a screen stops its downloads** (CHANGELOG Changed, 2026-11-25).
  Open Smart Home (with Home Assistant set up) or the community theme gallery
  on a slow connection and leave at once: no "unreachable" or error line
  greets the next visit, and it loads again. In the Dictionary, type a word
  and leave before it answers: the next visit shows no "Network error".
- **Windows: a network changed outside JukaHub** (CHANGELOG Added,
  2026-11-25). Unplug the cable or pick another Wi-Fi network in Windows'
  own menu: JukaHub's network status follows within about a second.
- **Windows: started from another folder** (CHANGELOG Fixed, 2026-11-25).
  With Start JukaHub at login on, sign out and in, or start `JukaHub.exe`
  from a terminal in another folder: it loads the config beside the
  executable, and the log says "working from the executable's folder".
- **Windows brightness on real machines** (from the backlog, 2026-11-25). On
  a laptop (and a ROG Ally or Legion Go class handheld) the brightness card
  moves the same level as Windows' slider and follows it while open; on a
  desktop with a DDC/CI monitor it moves the monitor; with neither (a VM,
  Remote Desktop) the card says it dims JukaHub only, the dim survives a
  restart and now covers the boot and the goodbye screen; no console window
  flashes, and the first frame is not delayed.
- **Xenia Canary and xemu on a Windows PC** (from the backlog, 2026-11-25).
  Xenia Canary: the newest release installs (`xenia_canary_windows.7z`), a
  later build shows as an update and R1 restores the one before; an `.iso` in
  `Roms\XBOX360`, an extracted game (one entry under its folder's name, with
  its cover) and a `.zar` start full screen; the Visual C++ hint shows only
  without the redistributable (Windows on ARM too). xemu: v0.8.139 or newer
  installs the non-debug build (ARM64 from a windows/arm64 JukaHub), the HDD
  image lands in `BIOS\xbox`, BIOS files dropped there turn green on the open
  sheet within seconds, an ISO is refused with "xemu needs …" until they are
  there and then starts full screen with `-dvd_path`, and a portable xemu
  whose `xemu.toml` uses relative paths launches with that file unchanged.
- **An update restore cut short** (CHANGELOG Fixed, 2026-11-24). On a
  handheld over SSH, with an update on probation whose build does not start
  (a sideloaded build that hangs, `JUKAHUB_PROBATION_LIMIT=3`), wait for
  errors.txt's `update check: … putting … back` line and `kill -9` launch.sh
  at once, then start JukaHub from the menu: errors.txt says `a restore of
  the previous version was interrupted; finishing it`, no `*.rescue-tmp` is
  left in `Apps/JukaHub`, and the previous version starts with its "didn't
  start, so JukaHub put back the version you had" card.
- **Continue a game while a sync runs** (CHANGELOG Fixed, 2026-11-24). Two
  paired handhelds: on one start Sync › the other › Sync now, then at once
  continue a game the other one offers: the toast says "Waiting for the sync
  in progress…", the Sync page then shows "Bringing saves from <device>…",
  and the game starts with the other device's newest saves.
- **Dates in another language** (CHANGELOG Changed, 2026-11-24). With
  Settings → Language → Deutsch and the date format on day first, the mail
  list shows "Gestern" and "Mo." for the last days, the reader "Montag, 9.
  November 2026 · 15:04", the clock's calendar line "Morgen 09:00", and the
  Ports store and Steam their dates the same way; with the 12-hour clock on,
  each time ends in AM or PM.
- **Azahar from JukaHub on a Windows PC** (CHANGELOG Added, 2026-11-24). On a
  PC without Azahar, A on the Azahar tile installs the newest release's
  `azahar-windows-msvc-<version>.zip` into `Emulators\Azahar`
  (`.jukahub-emu.json` names the tag and the asset), a `.3ds` in `Roms\3DS`
  starts full screen from the Games Library, and once a newer Azahar is
  published the tile offers the update and R1 puts the previous one back.

- **GameCube with the buildbot's Dolphin and the libXi stand-in** (CHANGELOG
  Fixed, 2026-11-24). On a TrimUI whose RetroArch is on the card, online,
  press the Emulators page's GameCube tile (or A on a GameCube game): while
  JukaHub's own build is unpublished the setup takes the buildbot's, and the
  log reads `device libraries for RetroArch on JukaHub's glibc: JukaHub's
  stand-ins … libXi.so.6 (N functions) …`, `data/private-glibc/devlibs/devlibs.json`
  lists `libXi.so.6` beside `libusb-1.0.so.0`, and the install ends with
  "Dolphin is ready". A game then starts in RetroArch on JukaHub's glibc with
  no `libXi.so.6: cannot open shared object file` in its log; a card that
  refused the 2026-11-24 build before ("was refused here before: … needs
  libXi.so.6 and libusb-1.0.so.0") fetches it again. Log what the game does
  next (picture, speed, the video driver dolphin_video.go settles on).
- **hakuX's build for the screen at an Xbox game's start** (CHANGELOG Fixed,
  2026-11-24). On a Brick Pro (or Brick, Hammer, Smart Pro S) whose hakuX
  JukaHub installed before spruceUI's build was used — its
  `Emus/XBOX/.jukahub-hakux.json` has no `screen_build` — start an Xbox game
  from the library online: the install card reads "Getting hakuX for this
  screen", the log says which file it replaced (`hakuX: … replaced with
  spruceUI/hakuX-spruce's beta-main build`), the provenance file gains
  `screen_build`, and the game starts by itself: JukaHub closes (`screen handed
  to helper pid …` in errors.txt) and hakuX's own picture fills the panel. Its
  menu takes the buttons, Menu opens hakuX's settings, Start + Select held
  about 1.2 s quits back to JukaHub, and the log has `back from <game> after
  …`. Offline, the first start says what it waits for and the second plays.
  If the picture still does not show with JukaHub gone, send
  `Apps/JukaHub/data/gamediag/launcher.log` and errors.txt.
- **In-game indicators** ([Configuration](CONFIGURATION.md#in-game-indicators-ingamehud),
  2026-11-24). Needs a package whose `bin/` has `libjukaosd.so` (its block in
  `bin/BUILDINFO.txt`). Turn Settings → Games & emulators → In-game indicators
  on, start a RetroArch game and a standalone PPSSPP game from the library:
  the volume keys show a Volume card with a bar at the top, Menu + volume a
  Brightness card, each fading out after about 1.5 s; RetroArch's own FPS
  readout shows no drop while a card is up or after; a notification (with
  Notifications while playing set to Over the game) appears as a card; a port
  or a Wine program runs exactly as before; switching the setting off and
  launching again leaves the picture alone. Log a game's `LD_PRELOAD` line and
  any card that does not show (which emulator, which video driver). In the
  in-game menu, **Performance** steps Auto → Battery saver → Balanced →
  Performance, `scaling_governor` follows at once, the game's options sheet
  shows the new CPU profile, and the hub's governor is back after the game.
- **Ghost Challenges between two handhelds** ([Ghost Challenges](GHOST_CHALLENGES.md),
  2026-11-24): with Nearby devices paired, play Flappy on one, **Y** on the
  game-over card → Send to the other: the other gets a toast and **Play** in
  the bell; racing shows the same pipes and the ghost bird; the result reaches
  the screen's row ("Beaten"). Save as a file, copy the `.jghost` to the other
  card's `Ghosts` folder: it is taken in when Ghost Challenges opens.
- **Japanese, Korean and Chinese** ([Configuration](CONFIGURATION.md),
  2026-11-24): online, pick 日本語 (shown as "Japanese"), 한국어 and 简体中文 in
  Settings → General → Language: each downloads its Noto Sans (JP 4.5 MB, KR
  4.6 MB, SC about 8.4 MB) from noto-cjk's Sans2.004 subsets and switches when
  it lands; Home, Settings and the Start menu read in the language with no
  "…" and no label running off its row; dates read 2026年11月24日; offline the
  pick stays English and switches once Wi-Fi is back. Note any clipped label.

- **Universal instant resume** ([Never lose a session](SESSION_SAFETY.md),
  2026-11-24). Settings → Power & battery → save level 10 % makes these quick.
  (1) A port or a Wine program at the save level off the charger: the device
  buzzes and sleeps with the game stopped; plug in, press power — the game
  carries on where it was. Again without a charger: after about ten minutes it
  wakes, the port quits (its own save written) and the device powers off; the
  bell names the hold. Woken without a charger, the guard closes it 45 s later.
  (2) A RetroArch game at the save level: saved and suspended; wake it and
  power off from the menu without starting another game, then power on — it
  resumes. Start another game first and it does not.
  (3) JukaHub's own PPSSPP build (PPSSPP card → **Resume PSP games after a power
  off**): Power off mid-game; the log shows "asked PPSSPP for a save state", a
  `.ppst` and `.ppst.done` ("ok") appear in `data/emulators/ppsspp/resume/`,
  and the next power-on starts the game at that point (`jukahub-ppsspp.status`:
  "resuming from …"). Check that the build's release notes carry
  `Resume: save-on-request` and that PSP saves and the player's own state slots
  are untouched. (4) The same PSP game with the screen to itself (hand-off) at
  the save level: JukaHub comes back, its log says the game "was closed by its
  battery watch" with the state kept, and it powers off; the next power-on
  resumes the game. (5) An
  Android game: logged as "This Android game", held like a port.
- **Browser ads removed** ([Browser](BROWSER.md), 2026-11-24): open a news page
  with ads (cnn.com, theguardian.com, a recipe blog) and a DuckDuckGo search with
  sponsored results. No ad slot, "Advertisement" or "Ad Feedback" line,
  sponsored card, blank picture placeholder or empty band is drawn; the
  paragraphs round a removed ad meet, a card grid has no hole, and an article
  beside a removed ad rail spans the page. Site info → **Allow ads on this site**
  brings the slots back and **Block ads on this site** removes them again. A
  classifieds site (OLX, Gumtree) keeps every listing.
- **Sync, one code** ([Sync](SYNC.md), 2026-11-24): with Nearby devices off on
  two handhelds, open Tools → Sync on both; each lists the other under Found on
  this Wi-Fi within seconds. Pair once from a screen and once from the phone's
  `/sync` page (the code shows on the phone and on the other screen; Codes match
  on both); afterwards Nearby devices is on on both and the first background
  sync runs about half a minute later. Play a game on one, exit, start it on the
  other: "Saves updated from …" and the newer save; a save changed on both says
  "changed here and on …" and copies nothing; with Automatic sync off nothing
  moves by itself. On the phone page: Google's code and link appear after Sign
  in; WebDAV connects; a RomM 4.8+ Pair device code connects RomM (a pasted
  token still works); Syncthing turns on, shows its ID and a PC's request is
  accepted from the phone. The big code on the handheld closes when the phone
  opens the page.
- **Live Builder:** Enable it in Settings → Network and push from a laptop's
  builder at `http://localhost`. An ordinary design applies; one with a program
  waits for approval; another push gets 409 while approval is pending; New
  pairing code works. On Windows, the builder reaches `localhost:8422`, and
  `JUKAHUB_LIVE_LAN=1` opens the LAN.
- **File transfer:** With Windows Explorer and FileZilla, sign in to FTP as
  `juka`, list folders, upload into a share and confirm `Roms/Ports` is refused.
  Idle and network changes stop FTP with a toast. When started before Wi-Fi on a
  cold boot, it stays up through network time correction and stops 15 minutes
  after last use. Check SFTP's card root and “Share whole system…” separately.
- **Input and layout:** On a Brick's 960×720 canvas, check the date/time picker
  and first-run Wi-Fi and time-zone buttons. On a Smart Pro and Brick, the
  Controller test lights X for the printed X; disabling **Swap X and Y** restores
  the old mapping. Check the phone remote's X/Y, charging bolt on plug/unplug,
  volume button and card with pad and USB keyboard (wheel on Windows), the Home
  pairing QR preview and restored backlight, and the PS Remote Play pairing
  keyboard with pad and USB keyboard.
- **Windows launchers:** Launch `.bat` files and ROM names containing `&`, spaces
  or parentheses, and check that `%` is refused.
- **CrossMix games:** PSP and NDS show Available with the scripts named by
  `config.json` (`ppsspp_*.sh`, `drastic.sh`). Launch both from the Games library;
  CrossMix's `common_launcher.sh` receives an absolute ROM path. LAN play finds
  `ra64.trimui` and starts RetroArch with its own config. Stick Calibration
  edits the played PPSSPP build's ini; Save Manager lists saves under
  `Emus/PSP/PPSSPP_<version>/`.
- **Brick Pro:** With CrossMix, launch a PSP and a RetroArch game from Games.
  On failure, the launch card appears promptly with the script tail, signal and
  free memory; `errors.txt` records the start and end lines.
- **JavaScript:** On Smart Pro and Brick, check pages built by scripts, including
  a framework app, jQuery site and lazy images. Turning JavaScript off and
  pressing Reload shows the server's page. A public page cannot fetch from the
  LAN and the debug log names the refusal; `JUKAHUB_LOG_LEVEL=debug` writes one
  `scripts on …` line per page.
