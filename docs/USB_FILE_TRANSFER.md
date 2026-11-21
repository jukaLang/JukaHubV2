# USB file transfer

> **Device status (2026-11-17):** stock Smart Pro firmware 1.1.0 has a bound
> configfs gadget with ADB, a USB device controller and FunctionFS support.
> These prerequisites were confirmed over SSH. MTP transfer to a computer
> still needs a hands-on test with a build containing this feature; see
> [the hardware log](HARDWARE_VALIDATION.md).

USB networking is unavailable on this stock image: its kernel config has
`CONFIG_USB_CONFIGFS_ECM` and `CONFIG_USB_CONFIGFS_RNDIS` disabled. This does
not prevent MTP file transfer through FunctionFS. **Browser over USB** (below)
therefore says "This firmware's kernel has no USB network function" there;
it works on a firmware whose kernel has RNDIS, ECM or NCM.

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

While a computer is connected the handheld stays awake: the idle screen
does not come up and Auto sleep waits, so a long copy is never cut off
halfway (`usbawake.go`).

## Open JukaHub in your browser over the cable

The same cable also carries a small network of its own, so the computer's
browser can open the Phone Companion page — drag games, covers and music in
from any browser, exactly as over Wi-Fi, with no Wi-Fi at all.

1. Leave **Settings → Network → Browser over USB** on (it is on by default
   and sits under USB file transfer, which it needs).
2. Plug the handheld into the computer.
3. Open **http://10.73.73.1** in the computer's browser and type the PIN.
   The line under the switch shows the address and the PIN, and says
   "Connected" once the computer has its address.

The handheld is always `10.73.73.1`; the computer gets an address from
`10.73.73.2` to `10.73.73.6` from the handheld's own DHCP server. That answer
names no router and no DNS server, so the computer keeps using its own
network for the internet — only the handheld's page goes over the cable.
While the startup lock is up the page says JukaHub is locked.

- **Windows 10 and 11** — the handheld shows up as a network adapter
  ("Remote NDIS") using Windows' own driver; nothing to install. Windows may
  call it an unidentified network without internet: that is expected.
- **Linux** — NetworkManager and most distributions pick it up by themselves.
- **macOS** — macOS has no driver for the Windows kind of USB network. When
  the computer stays silent on it for about 20 seconds, the handheld switches
  to the kind macOS understands (ECM) and the port re-connects once; the
  page opens after that. The next computer that stays silent switches it
  back.

### When it is not available

The line under the switch says why:

- **"This firmware's kernel has no USB network function"** — the kernel was
  built without RNDIS, ECM and NCM (the stock Smart Pro 1.1.0 kernel, see
  above). Use the page over Wi-Fi, or USB file transfer.
- **"Not enough USB endpoints next to adb and file transfer — file transfer
  only"** — the USB controller has a fixed number of endpoints, and the
  firmware's ADB, file transfer and the network did not all fit. File
  transfer is kept, and JukaHub does not try again until the handheld
  restarts.
- **"Phone companion is off …"** — the page belongs to the Phone Companion,
  which starts by itself once the handheld has an address (safe mode keeps it
  off).

### Updates, logs and folders that ask first

The same page, over the cable or over Wi-Fi, also:

- **Installs updates.** When a new JukaHub is out on your update channel
  (Stable or Beta), a **JukaHub update** card at the top says which version,
  with **Install**. The handheld then downloads and installs it exactly as its
  own update prompt does, says that your phone started it, and restarts
  JukaHub when it is ready. Not while a game is running, and below 30 %
  battery only on the charger.
- **Downloads the logs.** **Download logs** at the bottom of the page saves a
  zip of JukaHub's recent logs (newest first, 20 MB at most) for a bug
  report. Passwords and keys, Wi-Fi network names, account and display
  names, e-mail addresses, Bluetooth and Wi-Fi hardware addresses and LAN
  addresses are masked; game names and folders stay, so read it before you
  share it.
- **Asks before renaming or deleting what JukaHub needs.** A console's folder
  in Roms, BIOS, Saves, Overlays, PortMaster and JukaHub's own files can still
  be renamed or deleted from the Files card, but only after a dialog that
  says what the folder is for ("JukaHub keeps your Game Boy Advance games
  here"). The handheld enforces it, whatever page or program sends the
  request.

### How it works

`player/usbnet_linux.go` adds a network function to the USB gadget that file
transfer sets up, in the same configuration (Windows only gives each part of
a device its own driver when the device has a single configuration, so a
second configuration would cost file transfer and ADB on Windows). RNDIS
comes first, with Microsoft OS descriptors (`os_desc`: vendor code `0xcd`,
`MSFT100`; `interface.rndis` compatible ID `RNDIS`, sub-compatible
`5162001`) and the interface-association device class `EF/02/01`, so
Windows 10/11 bind their in-box driver; Linux binds `rndis_host`. When the
computer sets the device up but sends nothing over the network for 20
seconds, the function is swapped for ECM (NCM when ECM is not built). The
handheld's addresses are fixed (from the USB serial number), so the computer
sees the same network adapter every time.

If the port will not bind with the network function, it is taken out and
the port is bound exactly as before. Whatever JukaHub changed on the
firmware's gadget (device class, revision, OS descriptors) is saved to
`data/jukahub-usbnet.state` first and put back when file transfer stops,
JukaHub quits, or the next start after a crash. `player/usbnet_dhcp.go` is
the DHCP server (DISCOVER/OFFER, REQUEST/ACK/NAK, RELEASE, DECLINE, INFORM;
subnet mask and a 10-minute lease only).

## Startup lock

While the startup lock is up the computer sees the handheld but no storage,
and Explorer shows it empty. Unlock JukaHub and the SD card appears.

## How it works

The handheld acts as an MTP device, the protocol Android phones use. MTP
shares files rather than a raw disk, which is why it works while JukaHub
runs from the same card: a USB "card reader" mode has to unmount the card
first, so it can only run with everything else stopped (that is **USB
storage mode**, below).

JukaHub serves MTP itself (`player/usbmtp_proto.go`) through the kernel's USB
gadget framework (configfs + FunctionFS, `player/usbmtp_linux.go`); nothing is
downloaded. TrimUI firmwares already run ADB over the same port from a gadget
of their own. JukaHub adds MTP to that gadget, so the computer sees one device
with both, as with a phone; with no gadget running it makes its own. Turning
the switch off, or closing JukaHub, takes MTP out again and leaves the port as
the firmware had it.

## USB storage mode

**Settings → Network → USB storage mode** turns the SD card into a plain USB
drive on the computer, exactly as if the card were in a card reader. JukaHub
closes while the card is lent out and comes back when you are done.

> **Device status (2026-11-20):** built from the Smart Pro 1.1.0 kernel's
> configuration (configfs mass storage is enabled, see
> [the hardware log](HARDWARE_VALIDATION.md)); not yet tried on a handheld.

### When to use it instead of USB file transfer

- **A Mac** — Finder opens the drive directly; no OpenMTP needed.
- **Big copies** — a whole ROM set or a library rebuild goes at the card's
  full USB 2 speed, and any program can use the drive: a sync tool, a
  backup program, a disk checker, a ROM manager writing straight to the card.
- **Things MTP cannot do** — formatting-free repairs (`chkdsk`, Disk Utility's
  First Aid), changing file dates, opening a file in place.

USB file transfer stays the everyday way: it works while JukaHub keeps
running, and the startup lock protects it.

### How to use it

1. Open **Settings → Network → USB storage mode** and press A. JukaHub checks
   that the card can go (see below) and says what will happen.
2. Choose **Start**. Games and JukaHub close and everything is saved; the
   screen then says **USB storage mode**.
3. Plug the handheld into the computer with a USB data cable (before or after
   step 2). The card appears as a drive.
4. When you are done, **eject the drive on the computer**: Eject in File
   Explorer or Finder, or Safely Remove Hardware. JukaHub comes back by
   itself a few seconds later.

You can also press any button on the handheld to come back, or unplug the
cable. Eject first if you can: a button pressed while the computer is still
writing cuts that file short, the same as pulling a USB stick. With no
computer for 10 minutes, or after two hours, JukaHub comes back on its own.

### If the computer offers to repair the drive

Windows sometimes says "There's a problem with this drive" or "Scan and
fix", and macOS may show a disk check, when the card was last unplugged
without an eject (from the handheld or a card reader). Letting it scan is
safe and does no harm to your files. Whenever the mode ends without an eject,
JukaHub also checks the card itself before it mounts it again (where the
firmware has `fsck.vfat` or `fsck.exfat`), and the toast after it starts says
if something was repaired. Never format the drive from that dialog.

### When it cannot start

The sheet says why. The usual reasons:

- **"Other programs are using the SD card"** — an SSH session, a script from
  the card, or another app keeps files open there. Close it and try again.
- **"JukaHub was started straight from the boot hook this time"** — open
  JukaHub from the stock menu once, then try again.
- **"The swap file on the SD card holds more than fits in free memory"** —
  close a game, or turn off Settings → Games & emulators → Extra memory.
- **Not shown at all** — the mode is for the Smart Pro, the Brick and the
  Hammer on their stock firmware (with or without JukaHub OS mode). The Smart
  Pro S and Brick Pro firmware treat the card as removed when its USB drive
  closes, and the JukaHub OS image and NextUI run from the card themselves;
  use USB file transfer there.

If the card does not mount again after the computer had it (badly damaged by
a pulled cable), the handheld restarts so the firmware can mount it at boot.

### How it works

The card cannot be lent out while anything runs from it, and JukaHub,
`launch.sh` and the shell the stock menu started them from all do. So after
**Start** JukaHub saves a picture of the screen and a small helper script into
the handheld's memory (`/tmp`) and quits; `launch.sh` pauses the firmware's
menu loop and starts the helper on its own. The helper
(`player/usbstorage_helper.sh`) checks that nothing else uses the card, turns
a swap file on it off, unmounts the card (never lazily), and makes a USB
gadget with a mass-storage function backed by the card's partition in place
of the firmware's ADB gadget. When the computer ejects the drive (or a button,
the cable or the time limit ends it), it takes the gadget down, gives the port
back to ADB, checks the file system if the drive was not ejected, mounts the
card with its old options and starts JukaHub again. Its log goes to
`errors.txt` in the JukaHub folder.

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
