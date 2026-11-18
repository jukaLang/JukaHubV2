# USB & OTG

**Tools → USB & OTG** shows what is plugged into the handheld's USB-C port and
does the things OTG firmwares (ArkOS and friends) do with it: switch the port,
mount a USB stick, copy games off it and eject it safely. It is meant for the
TrimUI handhelds — the Smart Pro S does host mode on its USB-C socket. On
Windows the screen only says that the tools are for the handheld.

The screen refreshes every two seconds while it is open. The Tools hub tile
shows a short summary ("2 devices · 1 drive", "Nothing plugged in").

## Port mode

- **Host** — the handheld powers the port and talks to what is plugged in:
  sticks, card readers, pads, keyboards, USB-C DACs and headsets, wired network
  adapters.
- **Device** — the handheld is the one plugged in, for a computer to see. A
  computer only sees something if the firmware set up a USB function (ADB or a
  drive); sticks and pads stop working.
- **Auto** — the firmware decides. JukaHub never touches the port at startup.

JukaHub looks for the kernel's standard role switch
(`/sys/class/usb_role/*/role`) first, then the Allwinner BSP USB manager
(`usbc0/otg_role`, with its `usb_host` / `usb_device` switch files). The mode
buttons appear only when one of those can be changed; otherwise the row says
**Fixed by the firmware**. A Host or Device choice is re-applied every time
JukaHub starts; choosing Auto stops that from the next start (the firmware
re-detects after a restart). The choice is kept in `usb-otg.json` in the app's
data folder.

## Drives

Every USB disk (`/dev/sd*`) is listed per partition with its label, size and
format, and whether it is mounted — by JukaHub or by anything else. **A** on a
drive opens its options:

- **Mount** — mounts it at `/mnt/usb/<name>` (FAT32, exFAT, ext4 or NTFS,
  whichever the kernel supports; read-only when the stick is write-protected).
- **Open in Files** — opens the Files screen on the drive.
- **Import games** — shown when the drive has a `Roms/` (or `BIOS/`) folder at
  its root; see below.
- **Eject** — flushes everything to the stick and unmounts every partition of
  it, then says **Safe to remove**. If a program still has a file open, the
  drive is detached anyway and the message asks you to wait a few seconds
  before unplugging.

**Mount drives when plugged in** (off by default) checks for new drives every
three seconds while JukaHub is open and mounts them under `/mnt/usb`. It waits
one extra check before mounting, so a firmware that mounts sticks itself gets
there first.

## Import games from a stick

Lay the stick out like the card:

```
USB stick/
├── Roms/
│   ├── GBA/…
│   └── SNES/…
└── BIOS/…
```

Each system folder in `Roms/` is copied into the card's Roms folder (the one
the Games library scans, `/mnt/SDCARD/Roms` on the handheld). A folder whose
name matches one on the card in any case ("gba" and "GBA") goes into the card's
folder; any other name becomes a new folder, and the summary lists it so you
can rename it to the name your firmware uses. `BIOS/` is copied into the BIOS
folder RetroArch reads.

The import never replaces or deletes anything: a file already on the card with
the same name and size is skipped, one with the same name and a different size
is left alone (the card's copy wins), and each copy is written under a
temporary name and renamed only when it is complete. It checks there is room on
the card first. The progress sheet can be hidden (the copy carries on) or
cancelled (files already copied stay). An open Games library rescans when the
copy finishes; otherwise the new games appear the next time it opens.

## Devices

Everything else on the port, with what it is, read from the USB interface
classes: game controllers (HID pads, XInput and Xbox One pads), keyboards, mice,
storage, audio (USB-C DACs and headsets), wired network adapters, Wi-Fi and
Bluetooth chips, and hubs. **A** shows the maker, IDs, speed and the drivers the
kernel bound. Devices the kernel marks as built in are labelled so and not
counted on the Tools tile.
