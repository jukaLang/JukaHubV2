# PortMaster ports

JukaHub can install [PortMaster](https://portmaster.games/) and launch the ports
it manages, without leaving the Emulators screen.

Open **Emulators → PortMaster Ports**, or **Start → PortMaster Ports**. The
panel shows the PortMaster app first, followed by every port launcher it has
installed.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through the rows |
| A | Install PortMaster, open it, launch an installed port, or install a catalogue port |
| X / Tab | Switch between installed ports and the catalogue |
| Y / F | Toggle **May Run on ChaOS**, a filter using the port's architecture, hardware requirements and minimum glibc |
| R1 / S | Search the catalogue by name or description |
| B / Esc | Back (to Emulators when opened there) |

Tapping a row does the same thing as **A**; tapping outside closes the panel.

## Installing PortMaster

On a fresh card the first row is **PortMaster • Install**. Pressing **A**
downloads the official TrimUI package
(`trimui.portmaster.zip`) from the
[PortMaster GUI releases](https://github.com/PortsMaster/PortMaster-GUI/releases)
and unpacks it to:

```text
SDCARD/Apps/PortMaster/
├── launch.sh
├── config.json
├── icon.png
└── PortMaster/
```

The download is about 24 MB and needs Wi-Fi. JukaHub stages it on the SD card
and only moves it into `Apps/PortMaster` after a complete download and unpack,
so a dropped connection never leaves a half-installed app. PortMaster also
appears in **Apps** afterwards, because it is a standard TrimUI app folder with
a `config.json`.

JukaHub never overwrites an existing `Apps/PortMaster`. To repair a damaged
install, remove that folder with the file browser or FTP first.

## Where ports live

PortMaster on TrimUI keeps port launchers in `Ports/` and port data in
`Data/ports/`. JukaHub scans these folders, in order:

1. `SDCARD/Ports`
2. `SDCARD/Roms/Ports`
3. `SDCARD/Roms/PORTS`
4. `SDCARD/Data/ports`

Every top-level `.sh` script becomes a row, except PortMaster's own launcher.
Duplicate names are listed once. Ports are started with `sh` from their own
folder, which is the same way the firmware's Ports tab runs them.

The catalogue tab reads PortMaster's live `ports.json`; its number of ports
changes as that catalogue changes. **May Run on ChaOS** removes ports whose own
metadata rules out this handheld, including ARM32-only builds and ports that
require sticks the Brick lacks. It is a compatibility hint, not a guarantee
that every game file or dependency is present.

Pressing **A** on a catalogue port downloads its zip, verifies PortMaster's MD5
when one is published, and puts the complete archive in PortMaster's
`autoinstall` folder. JukaHub then opens PortMaster, which installs the port and
fetches the runtimes it declares. PortMaster remains the place to update and
remove ports. Some ports also require game files you own; their catalogue
description and installation note explain that requirement.

## Minecraft

**Start → Games → Minecraft** installs three PortMaster ports in one press each
(the Minecraft Java Edition port, ClassiCube, Luanti · Mineclonia): JukaHub reads
PortMaster's catalogue, checks the zip's MD5, puts it in PortMaster's
`autoinstall` folder and opens PortMaster to install it. See
[MINECRAFT.md](MINECRAFT.md).

## Limitations

- PortMaster is an ARM Linux handheld application. The installer is hidden on
  Windows builds, which cannot run its ARM binaries.
- A port's own runtime requirements still apply. Ports that need Box64,
  WestonPack or a specific GL runtime install those through PortMaster.
- Reinstalling or updating the PortMaster app is done from PortMaster's own
  update flow; JukaHub's installer is for the first install.
