# USB file transfer

> **Device status (2026-11-17):** stock Smart Pro firmware 1.1.0 has a bound
> configfs gadget with ADB, a USB device controller and FunctionFS support.
> These prerequisites were confirmed over SSH. MTP transfer to a computer
> still needs a hands-on test with a build containing this feature; see
> [the hardware log](HARDWARE_VALIDATION.md).

USB networking is unavailable on this stock image: its kernel config has
`CONFIG_USB_CONFIGFS_ECM` and `CONFIG_USB_CONFIGFS_RNDIS` disabled. This does
not prevent MTP file transfer through FunctionFS.

Plug the handheld into a computer with a USB-C data cable and its SD card
opens there, the way a phone's storage does. Copy ROMs, BIOS files, saves,
screenshots and music in either direction while JukaHub keeps running — no
Wi-Fi, no app on the computer, no taking the card out.

It is on by default: **Settings → Network → USB file transfer**. Turned off,
the cable only charges (and ADB, if the firmware runs it, works as before).

## On the computer

- **Windows 10 and 11** — the handheld appears in File Explorer under
  **This PC**, named like your device in Settings → Network → Device name.
  Open it, then **SD card**. Nothing to install.
- **Linux** — GNOME Files, Dolphin and other file managers open it (they use
  libmtp/gvfs/KIO). `jmtpfs` or `go-mtpfs` mount it on the command line.
- **macOS** — Finder does not speak MTP; use [OpenMTP](https://openmtp.ganeshrvel.com/)
  or Android File Transfer.

A toast on the handheld says when a computer is connected.

## What works

Browse folders, copy files in and out (including files over 4 GiB on an
exFAT card), create folders, rename, move and delete — in Explorer that is
drag and drop, F2 and Delete. A file being copied in is written under a
hidden temporary name and renamed when the last byte arrives, so a pulled
cable never leaves a broken ROM under the real name; leftovers of an
interrupted copy are cleaned up the next time that folder is opened.

Things JukaHub writes while the computer is connected (a new screenshot, a
download) show up after you refresh the folder on the computer (F5 in
Explorer).

## Startup lock

While the startup lock is up the computer sees the handheld but no storage,
and Explorer shows it empty. Unlock JukaHub and the SD card appears.

## How it works

The handheld acts as an MTP device, the protocol Android phones use. MTP
shares files rather than a raw disk, which is why it works while JukaHub
runs from the same card: a USB "card reader" mode has to unmount the card
first, so it can only run with everything else stopped.

JukaHub serves MTP itself (`player/usbmtp_proto.go`) through the kernel's USB
gadget framework (configfs + FunctionFS, `player/usbmtp_linux.go`); nothing is
downloaded. TrimUI firmwares already run ADB over the same port from a gadget
of their own. JukaHub adds MTP to that gadget, so the computer sees one device
with both, as with a phone; with no gadget running it makes its own. Turning
the switch off, or closing JukaHub, takes MTP out again and leaves the port as
the firmware had it.

## Troubleshooting

- **Nothing appears on the computer** — use a data cable (many USB-C cables
  only charge) and the port the handheld charges from. Look at the line under
  Settings → Network → USB file transfer: it says whether the port is ready,
  connected, or why it is not available on this firmware.
- **"Not available: this firmware's kernel has no USB gadget support"** (or
  "no FunctionFS") — the firmware's kernel cannot act as a USB device for
  programs. Use FTP (Settings → Network → Servers & network tools) or the
  phone companion instead.
- **Tools → USB & OTG has the port in Host mode** — a computer cannot see the
  handheld then; set the port to Auto or Device ([USB_OTG.md](USB_OTG.md)).
- **"restart JukaHub to use the USB port again"** — a transfer did not stop
  when asked; restarting JukaHub frees the port.
- **Windows shows an old name or no files after an update** — unplug, then in
  Device Manager → Portable Devices uninstall the handheld and plug it in
  again.
