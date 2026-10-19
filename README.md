# JukaHub

[![Netlify Status](https://api.netlify.com/api/v1/badges/dea0cf4b-3799-41d9-8f2a-b4fd2a270b15/deploy-status)](https://generate.jukalang.com)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)](https://golang.org/dl/)
[![SDL2](https://img.shields.io/badge/SDL2-2.0+-3A75C0?logo=sdl)](https://www.libsdl.org/)
[![License](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](LICENSE)

A media hub for the **TrimUI Smart Pro**: YouTube search and trending, Shorts,
saved favourites and a few handy tools, in one controller-friendly app.

[Download](https://github.com/jukaLang/JukaHubV2/releases/latest) ·
[Web builder](https://generate.jukalang.com) ·
[Discord](https://discord.gg/R9qgJjh5jG) ·
[Developer guide](docs/DEVELOPMENT.md)

---

## Install on a TrimUI Smart Pro

Works on the stock OS and on CrossMix. You need the handheld, its SD card, and
Wi-Fi.

1. Download **`JukaHub-Trimui.zip`** from
   [Releases](https://github.com/jukaLang/JukaHubV2/releases/latest).
2. Turn the device off and put the SD card in your computer.
3. On the card, open the **`Apps`** folder and create a folder named
   **`JukaHub`** inside it.
4. Extract **all files** from the download into `Apps/JukaHub`.
5. Put the card back in, power on, and open **Apps → JukaHub**.

When you are done the folder looks like this:

```text
SDCARD/Apps/JukaHub/
├── JukaHub            <- the application
├── launch.sh
├── config.json        <- the Apps-menu entry
├── jukaconfig.json    <- the interface
├── JukaPlayer.png
├── Inter-Regular.ttf
└── background.jpg
```

That's the whole installation. There is nothing else to set up:

* the firmware already ships SDL2, so there are no extra libraries,
* no Python and no build tools are needed on the device,
* `yt-dlp` and `ffmpeg` download themselves on first launch (needs Wi-Fi; the
  first start takes a minute or two, later starts are instant).

If JukaHub does not appear in **Apps**, reboot the device — the stock launcher
only scans `Apps/` at boot. Also check that the folder is exactly
`Apps/JukaHub` and that `launch.sh` is inside it.

## First run

Home is a grid of tiles: **Continue**, **Media** (YouTube), **Shorts**,
**Favorites**, **Apps**, **Settings**, **Logs** and **Exit**.

* **Media / Tube** — YouTube search and trending.
* **Favorites** — save links and reopen them later.
* **Apps** — other apps on the card, plus built-in extras (JukaLand, Weather).
* **Settings** — playback resolution, weather and 25 colour themes
  (Dark, Light, High Contrast, Anime, Cute, Girly, Fallout, Matrix and more).
* **Logs** — view or export the app log if something misbehaves.
* **Tools** — this device's IP address, a built-in FTP server for quick file
  drops over Wi-Fi, and an SSH/SFTP server for browsing the file system from a
  PC (also under **MISC → FTP & IP**).

Turn Wi-Fi on before using YouTube or weather. Your settings are stored in
`Apps/JukaHub/jukauser.json` and survive updates.

## Copy files from your PC

1. Connect the handheld to the same Wi-Fi as your PC.
2. Open **Tools** and note the **IP address** (press A to copy it).
3. Start the **FTP server**. The panel shows the address to type, such as
   `ftp://192.168.1.42:2121`.
4. On your PC, paste that address into Explorer/Finder (or any FTP client) and
   drop files into the SD card (on Windows the share is a `shared` folder next
   to the app, so your settings and saved logins are never exposed).

The server stays on until you stop it or close JukaHub. Turn it on only on a
network you trust: while it runs, anyone on the same Wi-Fi can read and write
the shared folder. Login accepts any username and password by design.

## Browse files over SFTP

FTP is fine for dropping a file on the SD card, but some tools (editors, `scp`,
backup scripts) want SFTP. **Tools → SSH / SFTP** starts an SSH server that only
serves the SFTP subsystem:

1. Connect the handheld and the PC to the same Wi-Fi.
2. Open **Tools** and start **SSH / SFTP**. The row shows an address such as
   `sftp://juka@192.168.1.42:2222`, and the panel shows the one-time password
   for the `juka` account (also `ssh` in the built-in terminal).
3. On the PC, connect with WinSCP, FileZilla, Cyberduck, Finder or the
   `sftp`/`scp` command line, using the shown password. (Windows Explorer only
   speaks FTP, so use it with the server above or install an SFTP client.)

On the handheld the session is rooted at `/`, so you can reach the whole Linux
file system, not just the SD card. On a Windows build it starts at your home
folder.

Unlike FTP, this server is not open to the network: the password is random and
regenerated every time the server starts, password guesses are limited, and
only SFTP is enabled. Shell commands, port forwarding and tunnels are refused,
and paths cannot escape the root (including through symlinks). The host key is
saved in the app data folder, so the SFTP client only asks you to trust it
once. Press **A** on the row (or run `ssh off`) to stop it.

The random password is shown on the device only and is never written to
`jukauser.json` or the log. For unattended access, put your public key in
`Apps/JukaHub/data/ssh_authorized_keys` (OpenSSH format) and keep it across
updates. Advanced settings:

| Variable | Effect |
|----------|--------|
| `JUKAHUB_SSH_PORT` | Port to listen on (default `2222`), or set `ssh_port` in the config |
| `JUKAHUB_SSH_ROOT` | Folder to expose instead of the whole file system |
| `JUKAHUB_SSH_READONLY=1` | Refuse every upload, rename and delete; or set `ssh_readonly` in the config |

## Controls

The top bar holds the system status: network, weather, clock, battery and the
notification bell at the top right, with the **Apps** pill and a **back** pill on
the left. The bottom of every menu is reserved for controller-hint buttons, so
there is no second clock or battery indicator down there.

**Menus**

| Button | Action |
|--------|--------|
| D-pad | Move |
| A | Open / confirm |
| B | Back |
| L1 / R1 | Previous / next section |
| X | Secondary action |

**Live TV**

Put `.m3u` / `.m3u8` playlists in the `tv` folder next to the app, or press
**Load Playlist** to fetch the public iptv-org list (cached for six hours).
Channels are grouped by their playlist category, so a long file no longer has
to be scrolled channel by channel.

| Button | Action |
|--------|--------|
| Left / Right | Previous / next category (**All**, **Favorites**, then groups) |
| Up / Down | Move through the channel list |
| A | Play the highlighted channel |
| X | Search channels (filters while you type) |
| Y | Add / remove the channel from Favorites |

Favorites are saved in the app data folder (`data/tvfavorites.json` on the
handheld), so they survive playlist reloads and app updates.

**Video player**

| Button | Action |
|--------|--------|
| A | Play / pause |
| B | Stop and go back |
| Left / Right | Seek |
| Up / Down | Volume |
| L1 / R1 | Playback speed |

**On-screen keyboard** (search, chat, input fields)

| Button | Action |
|--------|--------|
| D-pad | Move between keys |
| A | Type the selected key |
| L1 | Shift (upper / lower case) |
| Y | Done / save |
| B | Close the keyboard |

## Updating

JukaHub updates itself. On launch it checks the latest release on GitHub and,
when a newer build exists, downloads the archive for the platform
(`JukaHub-Windows.zip` on Windows, `JukaHub-Trimui.zip` on TrimUI), replaces the
app files and restarts. Your `jukauser.json` settings, favorites and media are
untouched, and an interrupted update is rolled back automatically at next
launch.

The version line shows the release build date, e.g. `v0.4.0 · 2026-10-17`, so
you can tell which build is installed. **MISC → Patch → Check for updates**
runs the same check manually.

You can also update by extracting a release archive over `Apps/JukaHub` by
hand. Set `JUKAHUB_NO_AUTOUPDATE=1` to disable automatic updates.

To uninstall, delete the `Apps/JukaHub` folder.

## Something is wrong?

Logs are on the SD card, so you can read them on a PC without SSH.

| Problem | What to do |
|---------|-----------|
| Not in the Apps menu | Reboot. Check the folder is `Apps/JukaHub` and contains `launch.sh`. |
| Black screen or closes at once | Send `Apps/JukaHub/errors.txt` — the launcher's log. |
| YouTube search fails | Check Wi-Fi; open **Logs** on Home for the app's own log. |
| Video stutters | Settings → playback resolution → try `480` or `360`. |
| Text shows as boxes | `Inter-Regular.ttf` is missing from `Apps/JukaHub`. |

`Apps/JukaHub/errors.txt` is the launcher log. For the app's own log, open
**Logs → Export** on the device; it writes `Apps/JukaHub/logs.txt`. If you need
help, attach one of them to your report on
[Discord](https://discord.gg/R9qgJjh5jG) or in an issue.

## Other devices

The same package is built for **TrimUI Smart Pro S**, **TrimUI Brick** and
**Brick Pro** (pick the matching file on the Releases page). Windows builds are
for trying JukaHub on a PC.

## Developers

JukaHub is a Go + SDL2 player driven by `jukaconfig.json`, plus a browser
builder that generates that config. Build commands, the config reference, the
package manager and the design system are in
**[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)**. TrimUI packaging notes are in
[player/trimui/README-ID.md](player/trimui/README-ID.md).

## License and thanks

AGPL-3.0 — see [LICENSE](LICENSE).

Thanks to Nevrdid for finding and documenting bugs, Arthur Gregorio for Go
guidance, StarDrive and DigestPrism for supporting the app, and everyone in the
Juka Discord for making it possible.
