# SMB share — the SD card as a Windows / Mac drive

**Tools → Network tools → SMB share** shares the handheld's SD card over SMB,
the protocol Windows File Explorer, macOS Finder and Linux file managers
speak natively. JukaHub serves it itself (no Samba program), on the handheld only.

It complements the [WebDAV network drive](NETWORK_SHARE.md): Windows' WebDAV
client refuses files over 50 MB, SMB has no such limit, so a whole ROM set or
a 4 GB disc image copies like it would to a USB stick.

It is new and **not yet checked on every system** (see "Not checked yet").

## Turning it on

Press **A** on the **SMB share** tile. It starts and its sheet shows:

- **Windows** — `\\192.168.1.42\JukaHub`
- **Mac** — `smb://192.168.1.42/JukaHub` (**Show QR code** puts it on the
  screen for a phone or tablet file app)
- **By name** — `\\NAME\JukaHub`, where NAME is the device name (Settings →
  Network → Device name) in capitals, at most 15 letters. It works when Windows still
  resolves NetBIOS names (the default on home networks).
- **User** `jukahub` and the **Password** — 12 letters and digits made the
  first time and kept, so Windows can remember it. **New password** makes
  another one and disconnects every computer signed in with the old one.
- **Access** — read and write, or read-only (**Make read-only** /
  **Allow writing**; it applies at once, also to files already open).
- **Connected** — the computers using the share and what they copy.

The share is **the whole SD card**: Roms, BIOS, Saves, Apps and the rest,
like SFTP. Turn on read-only when you only want to copy files off.

## Connecting

**Windows 10 / 11.** In File Explorer's address bar type the Windows address
and press Enter, sign in with `jukahub` and the password and tick
*Remember my credentials*. To get a drive letter: right-click *This PC* →
*Map network drive…*, folder `\\192.168.1.42\JukaHub`, *Connect using
different credentials*. From a command prompt:
`net use J: \\192.168.1.42\JukaHub /user:jukahub PASSWORD`.

**macOS.** Finder → *Go* → *Connect to Server…* (⌘K), type the Mac address,
choose *Registered User* and sign in.

**Linux.** In Files / Dolphin / Thunar type `smb://192.168.1.42/JukaHub`. To
mount it: `sudo mount -t cifs //192.168.1.42/JukaHub /mnt/juka -o
username=jukahub,vers=2.1`.

**Android / iOS.** Any file app with SMB (Files on iOS, CX File Explorer,
Solid Explorer…): scan the QR code or type the Mac address.

## Safety

- **Off by default**, and it switches itself off after 15 minutes nobody uses
  it and when the handheld joins another network — the same rules as FTP and
  SFTP. It stops when JukaHub exits.
- Only computers on the home network (private, link-local addresses), this
  device and a Tailscale tailnet (100.64.0.0/10) can connect; at most four at a
  time.
- One account. Guests and anonymous sign-ins are refused; a wrong password
  waits a second, and ten in a minute lock that computer out for a minute.
- Every session is **signed** (SMB signing is required), which Windows 11
  24H2 insists on. The files themselves are **not encrypted** on the network:
  use it at home, like FTP.

## What it does not do

- SMB 2.0.2 and 2.1 only: no SMB1 (old Windows XP-era devices), no SMB 3
  encryption, no multichannel. Reads and writes go 64 KiB at a time.
- No file locking between computers, no oplocks or leases, no "previous
  versions", no change notifications (press F5 in Explorer to refresh), no
  alternate data streams, no Recycle Bin — **deleting is permanent**.
- File times: the card keeps one time per file, so created / modified /
  accessed all show the modification time.
- Port 445 must be free: if another SMB server (Samba from the stock firmware
  or another OS) already runs, the start fails and says so.
- Not on the Windows build of JukaHub: Windows keeps port 445 for its own
  sharing.

## Not checked yet

On real hardware: Windows 11 24H2 signing, Finder, the share list Explorer
shows for `\\192.168.1.42`, copy speed on the handheld's Wi-Fi, and the
NetBIOS name. If one of them fails, a bug report from the device (Start → Device →
Report a bug) includes the "smb" log lines.
