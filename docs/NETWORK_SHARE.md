# WebDAV share: the handheld as a drive on a computer

Copy games, saves and screenshots between the handheld and a computer with
the computer's own file manager: **Settings → Network → WebDAV share** (also
**Tools → WebDAV share** and **Network tools**) turns on a drive that Windows
Explorer maps, macOS Finder connects to and Linux file managers open — no app
to install. It speaks WebDAV, which all three systems have built in. The [SMB share](SMB_SHARE.md) is the Samba
kind, without Windows' 50 MB WebDAV limit.

This is the other direction from [Games on a network drive](NETWORK_DRIVE.md)
(**Network drives**), where the handheld reads games from a NAS.

It is new and **not yet checked against every system**: Windows' WebDAV client
has quirks (below) that are handled from Microsoft's documentation and other
servers' experience, not from a run on each Windows version.

## Turning it on

Switch **WebDAV share** on. The screen shows:

- **Address** — `http://192.168.1.42:8325/abcd-efgh-jkmn-pqrs/`. The last
  part is a key made at every start: whoever has the address can open the
  drive, so it is shown on the handheld only. **A** shows it as a code.
- **Or sign in (Mac, Linux)** — the address without the key, the user name
  `juka` and a password made at every start, for a file manager that asks.
- **Connected** — the computers that have the drive open, and what they copy.

It shares the same folders as the FTP server: **Roms, BIOS, Saves, States,
Screenshots, Imgs, Music, Videos, Books, Documents** and the others that
exist on the card, and JukaHub's downloads — never the card's program
folders (Apps, Emus, RetroArch, System). Folders whose files run as programs
(Roms/Ports, game profiles, script systems) are **read-only**, as over FTP;
use SFTP to change those.

## Connecting

### Windows

1. Open File Explorer, right-click **This PC** and choose **Map network
   drive**.
2. In **Folder**, type the address exactly as the handheld shows it.
3. Untick **Reconnect at sign-in** (the address changes every time the drive
   starts) and choose **Finish**.

Pasting the address into Explorer's address bar works too.

- **"The folder you entered does not appear to be valid"**: Windows' WebClient
  service is not running. Open **Services**, set **WebClient** to
  *Automatic* and start it.
- **Files over 50 MB fail** (error 0x800700DF): Windows limits a WebDAV
  drive's files to 50 MB until the limit is raised. In PowerShell as
  administrator:

  ```
  Set-ItemProperty HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters FileSizeLimitInBytes 4294967295
  Restart-Service WebClient
  ```

  4 GB is Windows' ceiling. The SMB share, FTP and SFTP (**Network tools**)
  have no such limit.
- Windows will not sign in with a password over plain HTTP, which is why the
  address carries the key instead.

### Mac

**Finder → Go → Connect to Server** (⌘K), type the address and click
**Connect**. The drive appears under *Locations* in Finder's sidebar; eject it
there when you are done.

### Linux

- GNOME Files: **Other Locations → Connect to Server** →
  `dav://192.168.1.42:8325/abcd-efgh-jkmn-pqrs/`
- KDE Dolphin: `webdav://192.168.1.42:8325/abcd-efgh-jkmn-pqrs/` in the
  address bar.
- A terminal: `sudo mount -t davfs http://…/ /mnt/jukahub` (the davfs2
  package).

A web browser opening the address gets a plain list of the folders with
download links.

## When it stops

The drive is for a quick copy at home, and it stops by itself, as FTP and
SFTP do:

- after **15 minutes** without use (opening a folder, copying, deleting);
- when the handheld joins another network or loses Wi-Fi;
- when the startup lock comes up, and when JukaHub quits.

A new start makes a new key and password: map the drive again with the new
address. Ten wrong keys or passwords from one address lock it out for ten
minutes. **Settings → About → Security & trust** lists the drive while it
runs, with a button to stop it.

## For the curious

- WebDAV over plain HTTP, port **8325** (the next free one up to 8330 when it
  is taken). OPTIONS, PROPFIND, PROPPATCH, GET/HEAD (with ranges), PUT,
  DELETE, MKCOL, COPY, MOVE, LOCK and UNLOCK.
- Write locks (exclusive and shared, at most an hour, refreshed by the
  client) are what Explorer and Finder need before they write; they live in
  memory and end with the drive.
- An upload arrives under a hidden temporary name and is renamed when
  complete, so a cut connection never leaves half a game. Uploads stop with
  "the card is full" before they fill the card.
- Windows' file dates (Win32LastModifiedTime) are kept on copied files; free
  space shows in Explorer and Finder.
