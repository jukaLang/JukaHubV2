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
| Library and apps | LIB-14, LIB-17, LIB-18, LIB-24, LIB-26, APP-9, APP-16, APP-17, APP-18, APP-20, APP-21 |
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

### Regression checks without separate task IDs

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
