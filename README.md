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

1. Download **`JukaHub-trimui-smart-pro-linux-arm64.tar.gz`** from
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
* **Settings** — playback resolution, themes, weather and more.
* **Logs** — view or export the app log if something misbehaves.

Turn Wi-Fi on before using YouTube or weather. Your settings are stored in
`Apps/JukaHub/jukauser.json` and survive updates.

## Controls

**Menus**

| Button | Action |
|--------|--------|
| D-pad | Move |
| A | Open / confirm |
| B | Back |
| L1 / R1 | Previous / next section |
| X | Secondary action |

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
