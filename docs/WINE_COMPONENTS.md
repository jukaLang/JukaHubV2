# WINE components and apps

Two Community cards make the Windows runtime easier to use without turning
JukaHub into a download site.

## WINE components

Community → **WINE components** reads `<data>/wine/components.json`, a list you
write yourself. Each entry names a file, an HTTPS URL and its SHA-256:

```json
{
  "version": 1,
  "components": [
    {
      "name": "Wine Mono",
      "url": "https://dl.winehq.org/wine/wine-mono/9.4.0/wine-mono-9.4.0-x86.msi",
      "sha256": "<the digest published with the file>",
      "file": "wine-mono-9.4.0-x86.msi",
      "envDir": "WINE_MONO_CACHE_DIR",
      "detail": "For .NET programs that ask Wine to install Mono"
    }
  ]
}
```

**A** downloads the highlighted component. The shared install bar shows the
name, bytes received, a percentage and a bar; the card repeats the numbers.
There is no boot-time or launch-time download. The rules are strict:

- The URL must be HTTPS on an approved host (GitHub release hosts and the
  Wine project's own `dl.winehq.org`).
- The SHA-256 must be written in the file and match the download. A mismatch
  removes the partial file; nothing is cached.
- `file` is a plain file name, not a path. The file is cached in
  `<data>/wine/`; JukaHub never executes it.
- `envDir`, when present, names an environment variable pointed at the folder
  holding the file (Wine's Mono and Gecko cache variables take a folder).
- After a download, a stamp beside the file records its size, time and
  digest. A launch only checks the stamp, never re-hashes a large file.
  **X** on the card re-hashes the highlighted file: a file that changed is
  removed so **A** can fetch it again. The Windows Apps setup page shows how
  many optional components are missing.

The card shows how many files are downloaded out of those listed, the space
they use, and each file's size. **Del** on the keyboard (or **L1** on a pad)
removes the highlighted cached component and its stamp; a
file you supplied yourself in `<data>/wine/` is never removed by JukaHub.

**Y** writes an example `components.json` if you do not have one; its empty
SHA-256 means nothing can be downloaded until you fill it in. A component can
also name `"local": "file.msi"` for a file you already copied into
`<data>/wine/` — the SHA-256 is still checked, and no network is used.

Nothing is bundled with JukaHub, and no component can overwrite a file Wine
already uses. If you do not need an optional component, leave the file out.

## Wine apps

Community → **Wine apps** is a reading list of free, open-source programs made
for Windows that are known to work under Wine: 7-Zip, Notepad++, WinSCP,
WinMerge, Sumatra PDF, KeePass, MPC-HC, Explorer++ and PuTTY. **A** shows the project's own page —
a QR code on the handheld, a browser tab on Windows. JukaHub does not download
or bundle these programs.

Get the installer from the project, then put its folder in your Windows Apps
folder and start it from that panel. Use these tools with files and servers
you own or are allowed to use, and think twice before putting a password
manager's master password on a handheld that can be lost.

## Emulator crash reports

A game that ends with a signal or a non-zero status now writes
`emulator-crash-<time>.txt` beside the app's own crash reports (the newest ten
are kept). It records the game, launcher, command, exit status and signal,
memory free at start, and the last 40 lines of the launcher log. The launcher's
output is captured for every library launch now, including on Windows, so a
crash after the first few seconds is still explained.
