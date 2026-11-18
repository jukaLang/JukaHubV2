# Supported TrimUI handhelds and their drivers

JukaHub runs on four TrimUI handhelds (and the Hammer, a Brick variant). They share a
firmware family but not a board. The table is what JukaHub expects on each model
(`player/devicedrivers.go`, `deviceHWTable`); every driver still checks for its device node
first and falls back when it is missing, so a wrong guess costs an order, not a feature.

| | Smart Pro | Brick (and Hammer) | Smart Pro S | Brick Pro |
|---|---|---|---|---|
| Model code | TG5040 | TG3040 | TG5050 | TG4040 (store: TG4070) |
| Chip | Allwinner A133P, 4× Cortex-A53 | A133P | Allwinner A523, 8× Cortex-A55 (two cpufreq policies) | A133P |
| GPU / display | PowerVR GE8300, fbdev EGL (`/dev/fb0`, PowerVR DRM render node on stock 1.1.0) | same GPU family | Mali-G57 (`/dev/mali0`), DRM/KMS (`/dev/dri/card0`) | same GPU family as Smart Pro |
| Kernel | 4.9 BSP | 4.9 | 5.15 BSP | 4.9 |
| Panel | 1280×720, 4.96" | 1024×768, 3.2" | 1280×720, 4.96" | 1024×768, 3.95" |
| Backlight tried first | `/dev/disp` ioctl | `/dev/disp` | sysfs backlight | `/dev/disp` |
| Wi-Fi / Bluetooth | XR829 (BT on ttyS1) | XR829 | AIC8800D80 | XR829 (BT 4.2) |
| Fan | — | — | pwm fan | — |
| Analog sticks | yes | — | yes | yes (Hall) |
| RGB lights | led_anim | led_anim | led_anim | led_anim |
| Rumble ceiling | 3.3 V | 3.3 V | 3.3 V | 2.5 V |
| Video out | — | — | — | USB-C DP alt mode (firmware) |

## How the model is identified

In order: what the launcher exports (`DEVICE`, `PLATFORM`, `TRIMUI_MODEL`), a model code in
the device tree, `/proc/cpuinfo` or the hostname, the model names in the firmware's own files
and launcher, then the panel. The panel only tells the 4:3 Brick family from the 16:9 Smart
Pro family, so the chip settles the Smart Pro / Smart Pro S pair: `sun55i`/A523 in the device
tree, Cortex-A55 cores, or a Mali GPU node mean a Smart Pro S; `sun50iw10`, Cortex-A53 cores
or a PowerVR node mean a Smart Pro. A Brick Pro whose image names no model and no code reads
as a Brick (same chip and panel resolution).

## Checking on the device

Settings → About → **Drivers & hardware** lists every part with OK / not found / not used on
this model, and the driver and node behind it. Settings → About → About JukaHub shows the
identification evidence (model, how it was identified, panel, firmware names, chip).

## If something does not work

Open **Drivers & hardware** once — that writes the whole table to the log — then send a bug
report (Settings → About → Report a bug), which carries the log.
The lines that matter:

- `device:` — how the model was named, with the raw device-tree, cpuinfo, firmware, panel,
  chip (`soc=`) and kernel strings. A wrong model starts here.
- `drivers:` — one line per part, as the Drivers & hardware page showed it.
- `brightness:` — which backlight backend was chosen.
- `render pipeline` — SDL's video backend and renderer (a black or garbled screen).
- `display:` — another program drawing beside JukaHub (flicker).
- `cast:` — Screen Share: no `/dev/fb0`, or a DRM display where a game's picture may not be
  shareable.

- `handover` — on a DRM display: "DRM master dropped … for the game" and "DRM display
  taken back after the game".

On a stock Smart Pro with firmware 1.1.0, the observed GPU node is
`/dev/dri/renderD128` with `DRIVER=pvrsrvkm` in the DRM device's `uevent`.
`/dev/pvr_sync` is absent there. The display uses `/dev/fb0`, brightness uses
`/dev/disp`, and the battery is `axp2202-battery`. The firmware's
`trimui_osdd` keeps `/dev/fb0` open for its volume and brightness overlays;
that alone is not a display conflict.
The same image provides a configfs USB gadget bound with ADB, FunctionFS for
MTP, an RTC and CA bundle, AP-mode Wi-Fi with the firmware's hotspot tools,
and CPU/GPU/DDR/battery thermal zones. Its kernel does not include the
configfs RNDIS/ECM functions needed for USB networking. See the
[hardware validation log](HARDWARE_VALIDATION.md) for the observed paths and
the remaining live checks.

Known open points: on the Smart Pro S, when SDL runs on KMSDRM, a DRM device has one master,
so JukaHub drops master before a game starts and takes it back (and sets the mode again)
when the game is gone (`drmhandoff.go`; other video drivers are untouched). If a game still
starts black, or JukaHub is black after one, send the `render pipeline`, `display:` and
`handover` lines; starting JukaHub with `JUKAHUB_DRM_HANDOFF=0` turns the handoff off for
comparison. NextUI/Knulli Brick images show a garbled panel on the Brick Pro — that is the
firmware's panel setup, not something JukaHub can change; run JukaHub on the stock firmware
(1.1.1 or later).
