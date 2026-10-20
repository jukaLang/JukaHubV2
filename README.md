# JukaHub

[![Netlify Status](https://api.netlify.com/api/v1/badges/dea0cf4b-3799-41d9-8f2a-b4fd2a270b15/deploy-status)](https://generate.jukalang.com)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org/dl/)
[![SDL2](https://img.shields.io/badge/SDL2-2.0+-3A75C0?logo=sdl)](https://www.libsdl.org/)
[![License](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](LICENSE)

<img width="1284" height="722" alt="Image" src="https://github.com/user-attachments/assets/c7c17162-7b10-4b12-8f9e-cc90fdc9edc1" />

A media hub for the **TrimUI Smart Pro**: YouTube search and trending, Shorts,
saved favourites, RetroAchievements progress, and a few handy tools, in one
controller-friendly app.

[Download](https://github.com/jukaLang/JukaHubV2/releases/latest) ·
[Web builder](https://generate.jukalang.com) ·
[Discord](https://discord.gg/R9qgJjh5jG) ·
[Controls](docs/CONTROLS.md) ·
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
* **Apps** — other apps on the card, plus built-in extras (JukaLand, Weather,
  and **Browser**, a text web browser with GitHub issue quick links; see
  [docs/BROWSER.md](docs/BROWSER.md)).
* **Settings** — playback resolution, weather and 25 colour themes
  (Dark, Light, High Contrast, Anime, Cute, Girly, Fallout, Matrix and more).
  **Smart Widgets** (from General) holds the settings that react to the device
  on their own: battery saver, which drops to the slowest cadence, stops UI
  motion and switches to the OLED theme below 20% until it is charged again;
  the optional time-of-day theme (dawn, day, evening, night); and start at
  login, which is off by default.
* **Game Profiles** — launch Windows games through Box64/Wine, native
  binaries and scripts. Profiles live in `Apps/JukaHub/data/game-profiles`;
  see [docs/GAME_PROFILES.md](docs/GAME_PROFILES.md) for the format and the
  import/export workflow.
* **Windows Apps** — scan for `.exe` files and launch basic Windows programs
  through Wine. On TrimUI's no-X11 firmware it can route through WestonPack's
  DRM/KMS compositor instead of failing on a missing display. If DXVK is
  installed beside Wine and a Vulkan loader is present, Direct3D DLL overrides
  are enabled automatically; see [docs/WINDOWS_APPS.md](docs/WINDOWS_APPS.md)
  for the runtime setup.
* **PortMaster Ports** — install PortMaster from the Emulators screen and launch
  the ports it manages without leaving JukaHub. Installed launchers are read
  from `Ports/` and the older `Roms/Ports` locations; see
  [docs/PORTMASTER.md](docs/PORTMASTER.md).
* **Logs** — view or export the app log if something misbehaves.
* **Tools** — this device's IP address, a built-in FTP server for quick file
  drops over Wi-Fi, an SSH/SFTP server for browsing the file system from a PC
  (also under **MISC → FTP & IP**), the phone companion that turns a phone
  into a keyboard, remote and file drop, and **Community & support** for the
  changelog, bug reports and the anonymous-diagnostics switch. **Power tools**
  adds a gateway or clipboard-host port scan, ping/traceroute, DNS and Wi-Fi
  checks, a hex/JSON/log viewer, clipboard-saved script buttons, rsync and Syncthing checks,
  jukauser.json snapshots with diff/rollback, and a read/write benchmark
  board.

Turn Wi-Fi on before using YouTube or weather. Your settings are stored in
`Apps/JukaHub/jukauser.json` and survive updates.

## Controls

D-pad moves, **A** confirms, **B** goes back, **X** and **Y** are the secondary
and tertiary actions, **L1/R1** move between sections and pages, **Start** opens
the start menu.

Press **F1** on a desktop build for the full list as an in-app overlay. Per-screen
tables (Live TV, video player, on-screen keyboard, music player, Pomodoro,
alarm, calculator, terminal, JukaLand, desktop keys) are in
**[docs/CONTROLS.md](docs/CONTROLS.md)**.

## Copy files from your PC

1. Connect the handheld to the same Wi-Fi as your PC.
2. Open **Tools** and note the **IP address** (press A to copy it).
3. Start the **FTP server**. The panel shows the address to type, such as
   `ftp://192.168.1.42:2121`.
4. On your PC, paste that address into Explorer/Finder (or any FTP client) and
   drop files into the SD card (on Windows the share is a `shared` folder next
   to the app, so your settings and saved logins are never exposed).

The FTP server stays on until you stop it or close JukaHub. Turn it on only on
a network you trust: while it runs, anyone on the same Wi-Fi can read and write
the shared folder — login accepts any username and password by design.

## Browse files over SFTP

For `scp`, backup scripts and editors, **Tools → SSH / SFTP** starts a server
that only speaks SFTP. The panel shows an address such as
`sftp://juka@192.168.1.42:2222` and a one-time password for the `juka` account;
connect with WinSCP, FileZilla, Cyberduck, Finder or the `sftp` command line.
(Windows Explorer only speaks FTP, so use the server above or install an SFTP
client.)

Unlike FTP this is not open to the network: the password is random and
regenerated every start, guesses are limited, and shell commands, port
forwarding and tunnels are refused. Paths cannot escape the root, including
through symlinks, and the host key is saved once in the app data folder. Press
**A** on the row (or run `ssh off`) to stop it.

The password is shown on the device only and is never written to
`jukauser.json` or the log. For unattended access, put your public key in
`Apps/JukaHub/data/ssh_authorized_keys` (OpenSSH format). Advanced settings:

| Variable | Effect |
|----------|--------|
| `JUKAHUB_SSH_PORT` | Port to listen on (default `2222`), or set `ssh_port` in the config |
| `JUKAHUB_SSH_ROOT` | Folder to expose instead of the whole file system |
| `JUKAHUB_SSH_READONLY=1` | Refuse every upload, rename and delete; or set `ssh_readonly` in the config |
| `JUKAHUB_COMPANION_PORT` | Port for the phone companion page (default `8317`) |

## Use your phone as a keyboard

Typing a search on a D-pad is slow. **Tools → Phone companion** shows a QR
code; scan it with your phone camera (phone and handheld on the same Wi-Fi) and
the phone opens a small page with:

* **Type** — a text box whose contents appear in whichever field has focus,
  with an optional Enter after each send.
* **Remote** — D-pad, A/B/X/Y, Start, Back and the shoulder buttons.
* **Send a link** — paste a YouTube, direct-video or stream link and it starts
  playing on the handheld.
* **Send a file** — uploads land in the `PhoneDrop` folder of the shared folder
  (the same one FTP publishes), ready for the file browser or player.

The QR code carries a fresh six-digit PIN, so nothing has to be typed on the
device. The server stops when the card closes, and **Y** issues a new PIN on the
spot. `JUKAHUB_COMPANION_PORT` overrides the port (default `8317`).

## Share and remix a design

In the [web builder](https://generate.jukalang.com), **Share** turns the current
theme, fonts and layout into one link. Nothing is uploaded: the design is
compressed into the link itself, and account keys, tokens and recent activity
are stripped first. Opening the link shows **Remix**, which loads a fresh copy
into that person's workspace; their existing design is only replaced when they
choose it. Very large projects may exceed chat-app URL limits — in that case
use **Export → JSON** and send the file.

## Updating

JukaHub updates itself. On launch it checks the latest release on GitHub and,
when a newer build exists, downloads the archive for the platform
(`JukaHub-Windows.zip` on Windows, `JukaHub-Trimui.zip` on TrimUI), replaces the
app files and restarts. Your `jukauser.json` settings, favorites and media are
untouched, and an interrupted update is rolled back automatically at next
launch.

After the restart, **What's new** opens once with that release's notes (the
notes are cached before the swap, so it also works offline). You can reopen it
any time from **Tools → Community & support**. The version line shows the
release build date, e.g. `v0.4.0 · 2026-10-17`, so you can tell which build is
installed. **MISC → Patch → Check for updates** runs the same check manually.
Set `JUKAHUB_NO_AUTOUPDATE=1` to disable automatic updates, or extract a release
archive over `Apps/JukaHub` by hand.

## Privacy

Anonymous diagnostics are **off by default**. The switch is under **Tools →
Community & support**, and the app never sends anything unless you turn it on.
When enabled, the only data is the build/version, device model, platform and
OS/architecture — plus a crash count when the app panics. There is no install
ID, account, filename, config value or usage history. **Report a bug** opens a
prefilled GitHub issue with a redacted diagnostics block for you to review
before submitting.

To uninstall, delete the `Apps/JukaHub` folder.

## Something is wrong?

Logs are on the SD card, so you can read them on a PC without SSH.

| Problem | What to do |
|---------|-----------|
| Not in the Apps menu | Reboot. Check the folder is `Apps/JukaHub` and contains `launch.sh`. |
| Black screen or closes at once | Send `Apps/JukaHub/errors.txt` — the launcher's log. |
| A plugin or a bad update broke startup | Create an empty `safe-mode.flag` next to the app (or launch with `--safe-mode`). |
| YouTube search fails | Check Wi-Fi; open **Logs** on Home for the app's own log. |
| Video stutters | Settings → playback resolution → try `480` or `360`. |
| Text shows as boxes | `Inter-Regular.ttf` is missing from `Apps/JukaHub`. |

`Apps/JukaHub/errors.txt` is the launcher log. For the app's own log, open
**Logs → Export** on the device; it writes `Apps/JukaHub/logs.txt`. If you need
help, **Tools → Community & support → Report a bug** opens a GitHub issue with
redacted diagnostics already filled in and copied to the clipboard; you can
still attach a log or ask on [Discord](https://discord.gg/R9qgJjh5jG).

## Other devices

The same package is built for **TrimUI Smart Pro S**, **TrimUI Brick** and
**Brick Pro** (pick the matching file on the Releases page). Windows builds are
for trying JukaHub on a PC.

## Documentation

| Page | Covers |
|------|--------|
| [Controls](docs/CONTROLS.md) | every button and key, screen by screen |
| [Configuration](docs/CONFIGURATION.md) | `jukaconfig.json`, variables, elements, all triggers |
| [Building](docs/BUILDING.md) | Windows/Linux/ARM64 builds, SD-card packaging, releases, CI |
| [Windows Apps](docs/WINDOWS_APPS.md) | Wine, Box64, WestonPack and the no-X11 display bridge |
| [Packages](docs/PACKAGES.md) | the package format and the Patch package manager |
| [Plugins](docs/PLUGINS.md) | plugin manifests and the script execution contract |
| [Developer guide](docs/DEVELOPMENT.md) | architecture, design system, key files |
| [Contributing](CONTRIBUTING.md) | local setup, tests, what to commit |
| [TrimUI packaging](player/trimui/README-ID.md) | device package notes (Indonesian) |
| [Security](player/SECURITY.md) | secret storage and the security model |

## License and thanks

AGPL-3.0 — see [LICENSE](LICENSE).

Thanks to Nevrdid for finding and documenting bugs, Arthur Gregorio for Go
guidance, StarDrive and DigestPrism for supporting the app, and everyone in the
Juka Discord for making it possible.
