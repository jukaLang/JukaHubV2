# PortMaster ports

JukaHub can install [PortMaster](https://portmaster.games/) and launch the ports
it manages, without leaving the Emulators screen.

Open **Emulators → PortMaster Ports**, or **Start → PortMaster Ports**. The
panel shows the PortMaster app first, followed by every port launcher it has
installed.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through PortMaster and installed ports |
| A | Install PortMaster, open it, or launch the selected port |
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

Use the PortMaster app itself (**A** on the PortMaster row) to browse the port
catalog, install, update and remove ports. JukaHub lists what PortMaster has
installed; it does not maintain a second catalog or modify port files.

## Limitations

- PortMaster is an ARM Linux handheld application. The installer is hidden on
  Windows builds, which cannot run its ARM binaries.
- A port's own runtime requirements still apply. Ports that need Box64,
  WestonPack or a specific GL runtime install those through PortMaster.
- Reinstalling or updating the PortMaster app is done from PortMaster's own
  update flow; JukaHub's installer is for the first install.
