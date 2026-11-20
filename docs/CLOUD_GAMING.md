# Cloud Gaming

Start → Games → **Cloud Gaming** (also a Games-tab tile and in Home search:
"game pass", "geforce now", "xcloud") streams games that run on someone
else's servers: **Xbox Cloud Gaming** (Game Pass) and **GeForce NOW**. The
handheld only decodes the picture and sends your buttons, which is why a small
machine can play them at all.

## Does Game Pass work on a TrimUI?

Game Pass has two halves.

- **Downloaded games: no.** The games Game Pass downloads to a PC or an Xbox
  are x86-64 Windows/Xbox programs that need the Xbox app, Microsoft's copy
  protection, DirectX 12 and a desktop graphics card. A TrimUI is an ARM
  computer with a phone-class GPU; Wine on the handheld
  ([Windows Apps](WINDOWS_APPS.md)) runs small, older Windows games, not these.
- **Cloud gaming: yes, with limits.** Xbox Cloud Gaming comes with Game Pass
  Essential, Premium and Ultimate. Since November 2026 each includes a number
  of hours a month (Essential 5, Premium 10, Ultimate 15) and extra hours can
  be bought. Only games marked for the cloud in the Xbox app stream.

Your own Xbox at home streams through [X1](REMOTE_PLAY.md#xbox-x1) instead.

## On the handheld: GreenOvercast

JukaHub does not speak either service's protocol. It installs
[GreenOvercast](https://github.com/Producdevity/GreenOvercast), an open-source
(MPL-2.0) streaming client for small ARM64 Linux handhelds, as a PortMaster
port, and launches it. Sign-in happens inside GreenOvercast: choose Xbox or
GeForce NOW, and it shows a code to enter at an address on your phone.

GreenOvercast comes in two builds that install as the same port, so one is on
the card at a time:

| Build | From | Streams |
| --- | --- | --- |
| Cloud build | Producdevity, v0.3.0 and later | Xbox Cloud Gaming, GeForce NOW |
| xHome build | doomL's fork (v0.3.0-xhome, made from v0.2.0) | Xbox Cloud Gaming, your own Xbox |

Cloud Gaming installs the cloud build; X1 installs the xHome build. Either
screen launches whatever is installed and says when it lacks what you asked
for. GreenOvercast's row opens its details, which switch between them; a
switch downloads the other zip and installs it over the port. JukaHub records
the build it installed (or left waiting for PortMaster) in
`data/greenovercast.json`; a port installed by hand is recognised from its
files when it can be, and otherwise shows as "build not known" and may try
anything. A zip that is only waiting for PortMaster never counts as the build
on the card.

The download is checked (a zip shaped like the GreenOvercast port) and handed
to JukaHub's port installer, the same one the [Ports Store](PORTMASTER.md)
uses: PortMaster's own installer when it can run headless, else the zip goes
into PortMaster's `autoinstall` folder. Then PortMaster opens to finish only
when nothing else waits in the install queue, the screen that asked is still
up and no game holds the screen; otherwise a toast says the zip waits and
PortMaster installs it the next time it opens. When PortMaster itself is
missing, JukaHub installs it first and you press again once it is done. A zip
already waiting is replaced only after the new download has arrived.

### Will it run?

- **Compatibility runtime on TrimUI.** Every GreenOvercast build needs glibc
  2.38 or newer, while stock TrimUI firmware has 2.33. On the first launch,
  JukaHub downloads a checked, private glibc 2.39 to its data folder and runs
  GreenOvercast's binary through that loader. The firmware's libraries and
  PortMaster's shell setup stay unchanged. Wi-Fi is needed for that first
  download. See [Private C runtime](PRIVATE_GLIBC.md) for details and limits.
- **Video decoding.** On the tested A133 TrimUI, GreenOvercast selected its
  `cedar-h616` decoder at startup. A live stream has not been checked yet; if
  the picture stutters, pick 720p and 30 fps in GreenOvercast's settings.
- **Xbox boost.** While GreenOvercast runs, JukaHub gives it full CPU speed,
  every core, a full-speed GPU where the firmware allows it, its own memory and
  priority, and switches Wi-Fi power saving off; all of it comes back when the
  stream ends. Settings → Games & emulators → **Xbox boost** turns it off. See
  [Device tuning](DEVICE_TUNING.md#xbox-boost).
- Its authors have tested streaming on Anbernic H700 devices (muOS, Knulli,
  ROCKNIX), the Miyoo Flip (SpruceOS) and the R36S. The private loader and
  binary startup have been tested on a TrimUI; sign-in and a live stream still
  need a device test.

## On a PC

| Service | JukaHub opens |
| --- | --- |
| Xbox Cloud Gaming | Greenlight or XStreaming when installed, else <https://www.xbox.com/play> |
| GeForce NOW | NVIDIA's app, else OpenNOW, else <https://play.geforcenow.com> |

NVIDIA's app is looked for at
`%LOCALAPPDATA%\NVIDIA Corporation\GeForceNOW\CEF\GeForceNOW.exe` on Windows
and as the Flatpak `com.nvidia.geforcenow` on Linux (x86-64 only). OpenNOW, an
open-source GeForce NOW client, at `%LOCALAPPDATA%\Programs\OpenNOW\OpenNOW.exe`
or `opennow` on the `PATH`. Y on the GeForce NOW row links to both apps.

## GeForce NOW in short

GeForce NOW plays PC games you own on Steam, Epic Games Store, Ubisoft
Connect, Xbox (many PC Game Pass games) and others; link your stores in its
library first. A free membership plays with ads, queues and short sessions;
paid memberships get priority access and play longer at higher quality.

## Files

| Path | What |
| --- | --- |
| `data/greenovercast.json` | The GreenOvercast build JukaHub installed (build, address, MD5) |
| `data/private-glibc/` | The private glibc 2.39 runtime (Ubuntu's signed packages) used by GreenOvercast, and by PortMaster's bash, on older TrimUI firmware |
| `data/ports-store/incoming/` | Where the download waits for the port installer |
