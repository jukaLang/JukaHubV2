# PortMaster ports

JukaHub installs [PortMaster](https://portmaster.games/), lists the ports it
manages, and runs them as games: in the **Games** library (console **Ports**),
in **Continue**, in playtime and trophies, with the in-game menu and the game
assistant. PortMaster itself still installs, updates and runs the ports' own
machinery; JukaHub's job is to start each port exactly as the firmware's Ports
tab would, and to look after it around that.

> **Status:** stock Smart Pro 1.1.0 paths and manifest fields were checked over
> SSH on 2026-11-17. Port launch, controls, sound, update and uninstall still
> need a hands-on run; see [the hardware log](HARDWARE_VALIDATION.md).

Open **Emulators → PortMaster Ports**, or **Start → PortMaster Ports**. The
panel shows the PortMaster app first, then every installed port.

| Button | Installed list | Catalogue | A port's options |
|--------|----------------|-----------|------------------|
| D-pad Up / Down | Move | Move | Move |
| A / Enter | Open PortMaster, play a port, install PortMaster | Install the port | Run the option |
| Y / F / O | The focused port's **options** | **May Run Here** filter | — |
| X / Tab | Catalogue | Installed list | — |
| R1 / S | — | Search | — |
| B / Esc | Close (back to Emulators when opened there) | Close | Back to the list |

Tapping a row is **A**; tapping outside closes the panel (or the options).

## Playing a port

A port starts from the Ports panel, from **Games → Ports**, from **Continue**
or from search. While it runs:

- **Hold Select + L2**: the in-game menu (resume, sleep, close background
  apps, ask Juka, quit to JukaHub). The port is paused under it.
- **Hold Select + Start for 3 seconds**: end the port from the pad alone, even
  on a black screen. JukaHub interrupts the port's process group (so it can
  save), and kills it 6 seconds later if it is still there. Most ports also
  quit on a short Select + Start themselves through PortMaster's key mapper.
- **Hold Menu**: screenshot. **Power**: sleep; the port carries on after.
- **Select + L1 (0.7 s)**: the pad becomes a mouse, when *Mouse in games and
  apps* is on. Off again with the same chord or Menu. While it is on, the port
  (and PortMaster's key mapper) does not see the pad.

When the port ends JukaHub ends the helpers a port can leave behind after a
crash or a forced quit (`gptokeyb`, `gptokeyb2`, `oga_controls`, and Weston /
Xwayland / westonwrap when no Wine program is running), gives the sound device
back to JukaHub and reads the port lists again.

A port that quits within seconds gets a diagnosis card automatically. Any time,
**Y → Why didn't it start?** (or *Check why it won't start* on the port's page
in Games) shows:

- PortMaster missing, the port's build is 32-bit only, or its required GLIBC
  is newer than the firmware's;
- a runtime its `port.json` names (Mono, Godot, Java, WestonPack…) that is not
  in PortMaster's `libs/` folder — open PortMaster with Wi-Fi, or **Update**;
- the game's own data folder empty — many ports need files you own (a Steam
  or GOG copy); the port's install note and `README.md` say which;
- the last lines of the port's `log.txt`, or of JukaHub's own launch log for
  it when the script died before writing one.

## A port's options (Y)

| Option | What it does |
|--------|--------------|
| Play | Starts it, or shows the reason when the manifest requires another architecture or newer GLIBC |
| Update | Shown when PortMaster's catalogue has the port. *A newer build is in the catalogue* when the md5 PortMaster recorded at install differs from the catalogue's; otherwise it downloads the same build again. Either way the verified zip goes to PortMaster's `autoinstall` folder and PortMaster opens to install it |
| Check for updates | Shown before the catalogue is read (needs Wi-Fi) |
| Why didn't it start? | The diagnosis card above |
| Uninstall | **Press A twice.** Deletes the launcher script and what its `port.json` lists (`items`, `items_opt`) from the ports folders — the port's folder, **saves inside it included**. A name with a path in it, `..`, or PortMaster itself is never touched |

Rows of ports with a newer catalogue build say **Update available**. The
PortMaster row says when a newer PortMaster is released (compared with the
`version` file in PortMaster's folder — confirmed on stock Smart Pro 1.1.0); PortMaster
updates itself when opened.

## How a port is started

JukaHub aims for parity with the firmware's own Ports tab:

1. **Command.** A script under `SDCARD/Roms/…` goes through the firmware's own
   Ports launcher (the `launch` entry in `Emus/PORTS/config.json`) when the card has one — the same
   command MainUI runs. On stock Smart Pro 1.1.0 this is
   `Emus/PORTS/launch_balanced.sh`. Otherwise the script runs under `bash` (port scripts
   use bash features) from its own folder. Setting `portLaunchVia` in the user
   config: `auto` (default), `firmware` or `direct`.
2. **Environment.** The firmware's `HOME`, `PATH` and `LD_LIBRARY_PATH` as they
   were before JukaHub's `launch.sh` changed them (JukaHub's bundled libraries
   are not on the port's library path), without JukaHub's own variables.
   `SDL_AUDIODRIVER=alsa` stays pinned (`portAudioWorkaround`, `portAudioDriver`,
   `portAudioDevice`).
3. **Not set by JukaHub:** `CFW_NAME`, `DEVICE_NAME`, `DEVICE_ARCH`, `ESUDO`,
   `SDL_GAMECONTROLLERCONFIG`, the screen size. The port script sources
   PortMaster's `control.txt`, which sets them from its own device probe, and
   then PortMaster's `mod_<CFW_NAME>.txt` — overriding them would only make
   PortMaster apply another firmware's fixes. (Windows programs under
   WestonPack are different: there JukaHub stands in for PortMaster.)
4. **Finding PortMaster.** Port scripts look for PortMaster in
   `/opt/system/Tools/PortMaster`, `/opt/tools/PortMaster`,
   `${XDG_DATA_HOME:-$HOME/.local/share}/PortMaster`, then
   `/roms/ports/PortMaster`. If that search would not find a `control.txt`
   (JukaHub OS boots without MainUI's environment), JukaHub links
   `~/.local/share/PortMaster` to the real folder (`Apps/PortMaster/PortMaster`
   on TrimUI). Only when no link can be made does it set `XDG_DATA_HOME` to
   PortMaster's parent folder — then a port that does not set its own may keep
   its saves there. An empty leftover `…/PortMaster` folder without
   `control.txt`, which makes every port fail, is removed.
5. **SD card guard.** A bare `udevadm trigger` (seen in a WestonPack preview)
   makes TrimUI unmount the SD card; ports get a `udevadm` on `PATH` that skips
   only that call.
6. **Your overrides.** `data/ports.env` (`NAME=VALUE` lines) wins over all of
   the above — for testing a variable without a new build.
7. **Log.** Each launch writes `data/gamediag/ports/<script>.log`: the command,
   the PortMaster folder the script will find, `HOME`, `XDG_DATA_HOME`, `PATH`,
   `LD_LIBRARY_PATH`, the audio variables, what JukaHub changed, then the
   script's own output until it redirects to the port's `log.txt`. Attach it to
   a bug report together with that `log.txt`.

PortMaster itself runs its own `launch.sh` directly, as MainUI runs apps.
Before it starts JukaHub creates `Data/home` and `Data/ports` if they are
missing (PortMaster's TrimUI launcher is reported to stop without them). When
PortMaster closes, JukaHub reads the ports again, so a port it installed is in
the panel and in Games at once.

## On a stock TrimUI

PortMaster's TrimUI package is written for firmware with TRIMUI_EX (the
bootstrap from the PortMaster team that CrossMix includes). On a stock Smart
Pro or Brick, or under JukaHub OS, it used to close the moment it opened:

- its `launch.sh` sources `System/etc/ex_config`, TRIMUI_EX's file, and stops
  there without it;
- its menu (`pugwash`) and installer (`harbourmaster`) need `python3`, which
  the firmware does not have;
- the firmware's `/bin/bash` is BusyBox, and PortMaster's scripts use bash
  arrays;
- the Python SDL2 binding finds SDL2 only through `PYSDL2_DLL_PATH`.

So on an ARM64 TrimUI JukaHub checks, once a session and in the environment
the port gets, whether the firmware's bash runs a bash array and a process
substitution and whether its `python3` can import what PortMaster needs.
Whatever fails is supplied first on `PATH` for PortMaster, its ports and
harbourmaster only:

| Missing | What JukaHub adds | Where |
|---------|-------------------|-------|
| `python3` | python-build-standalone CPython 3.11 for `aarch64-unknown-linux-gnu` (3.12 or 3.13 if the latest release has no 3.11). It runs on the firmware's glibc. The archive is the one listed in that release's `SHA256SUMS` and must match its SHA-256 there; the test suite, Tk, IDLE, pip and headers are left out (about 30 MB download) | `data/portmaster-runtime/python/` |
| a real `bash` | the firmware's own real bash when one is further down `PATH` (CrossMix's `System/bin`), through a one-line shim; else Ubuntu's bash on JukaHub's [private C runtime](PRIVATE_GLIBC.md) (glibc 2.39, the newest signed Ubuntu build) | `/tmp/jukahub-port-runtime/bash` or `data/private-glibc/` |
| `PYSDL2_DLL_PATH` | links in tmpfs to the firmware's SDL2 libraries under the names pysdl2 tries (not with TRIMUI_EX's Python, whose `ex_config` sets it) | `/tmp/jukahub-port-runtime/sdl2` |

With JukaHub's Python, `SSL_CERT_FILE` names the firmware's CA bundle and an
inherited `PYTHONHOME`/`PYTHONPATH` is dropped. `/usr/trimui/lib` is appended
to `LD_LIBRARY_PATH` when it is missing, and `/dev/fd` is linked to
`/proc/self/fd` when the firmware did not make it (bash's process
substitution needs it; it lives in RAM until the next boot). When `launch.sh`
reads `ex_config` and the card has none, JukaHub runs a copy beside it,
`Apps/PortMaster/.jukahub-launch.sh`, in which that line runs only if the file
exists and TRIMUI_EX's `/mnt/SDCARD/System/bin/python3` names the Python in
use; the copy is deleted when PortMaster closes. PortMaster's own files are
not edited. A card with TRIMUI_EX or CrossMix, whose bash and Python work, is
left as it is. A script a port starts by its own `#!/bin/bash` line still gets
the firmware's `/bin/bash`; JukaHub does not replace it.

The first open downloads what is missing (Wi-Fi) with the install card up,
then PortMaster opens by itself — unless the card's X stopped the download or
a game was started meanwhile. Installing or repairing PortMaster fetches it
at once. Offline, PortMaster without any Python says why instead of opening;
without a full bash it starts with the firmware's shell and says so once. Each
launch log (`data/gamediag/ports/<script>.log`) names the bash and Python the
launch used.

**Y on the PortMaster row** has **Open PortMaster**, **Repair PortMaster** and
**Why didn't it open?** (checks the firmware again and lists what is missing,
with the last lines of PortMaster's log). A PortMaster that closes within
seconds says the likeliest reason.

> **Unverified on a device:** PortMaster's menu on the downloaded Python and
> the firmware's SDL2, Ubuntu's bash running PortMaster's scripts, and the
> exact `ex_config` line in the current `launch.sh`. PortMaster 2026.09.19+
> also counts no analog sticks on TrimUI (NX Redux PR #142); JukaHub does not
> change that yet.

## Installing PortMaster

On a fresh card the first row is **PortMaster • Install**. **A** downloads the
official TrimUI package (`trimui.portmaster.zip`, about 24 MB, Wi-Fi) from the
[PortMaster GUI releases](https://github.com/PortsMaster/PortMaster-GUI/releases)
and unpacks it to `SDCARD/Apps/PortMaster/` (`launch.sh`, `config.json`,
`icon.png`, `PortMaster/`). It is staged on the card and moved into place only
after a complete download and unpack that contains `launch.sh` and
`PortMaster/control.txt`. An existing `Apps/PortMaster` is never replaced by an
install. `Data/home` and `Data/ports` are created, and on a TrimUI the Python
and bash PortMaster needs are fetched right after.

**Repair PortMaster** (Y on its row) downloads the same package again and
writes its files over `Apps/PortMaster`, each through a temporary file and a
rename. It mends a damaged or half-updated install; ports, the runtimes in
`libs/`, themes and settings are not in the package and stay.

## Where ports live

JukaHub reads launcher scripts from `SDCARD/Ports`, `SDCARD/Roms/Ports`,
`SDCARD/Roms/PORTS` and `SDCARD/Data/ports`; duplicates are listed once.
PortMaster on TrimUI keeps each port's files in `Data/ports/<port>/`, with its
`port.json` manifest. The title in Games comes from `port.json`
(`attr.title`), else `gameinfo.xml`, else the script's name; the cover from
the library's artwork, the firmware's `Imgs/<script>.png`, or the port's own
`cover.png` / screenshot (copied once into the library's artwork folder).
On the inspected card, the installed zip's MD5 was in `port.json` under
`status.md5`, and the parser reads it there. An ARM64 port with
`attr.min_glibc` above the firmware's GLIBC is marked incompatible before it
starts, with the required version shown in **Why didn't it start?**.

## The catalogue

**X** shows PortMaster's live catalogue. Its first row opens the **Ports
Store** (below). **May Run Here** hides ports whose own metadata rules out this
handheld (ARM32-only builds, sticks the Brick lacks, desktop OpenGL, a newer
glibc). **A** queues the port in the same install queue the store uses.

## Ports Store

**Start → Games → Ports Store** (also Home search: "ports store", "ready to
run"; the Games tab; the catalogue's first row) is PortMaster's whole
catalogue as a store (new games for the consoles themselves — Game Boy, NES,
Mega Drive, PICO-8 … — come from [Get Homebrew](HOMEBREW.md) instead):

- **Discover** — shelves of the most downloaded *Ready to Run* ports (free
  games that need no files), new ports, recently updated ones, popular ports
  that run your own copy of a game, and the biggest genres. Each shelf ends in
  **See all**.
- **All ports**, **Ready to Run**, **Bring your game** — a grid with a search
  chip, a sort chip (most downloaded, newest, recently updated, A–Z) and genre
  chips.
- **Installed** — the ports on the card, which ones the catalogue has a newer
  build of, and **Update all**.

A card shows the port's screenshot, title, whether it needs your game files
and its download count. **A** opens the details: the big screenshot, porters,
genres, size, dates, the description, what the port needs from you, its
runtimes and whether its own metadata says it can run here, with **Install**,
**Play**, **Update/Reinstall**, **Uninstall** (press twice) and **Why didn't it
start?**.

| Button | Store | Details |
|--------|-------|---------|
| D-pad | Move between tabs, chips and cards | Up / Down scroll · Left / Right buttons |
| A / Enter | Open the card, tab, chip or See all | Press the button |
| X | Install (or update) the focused port; clear the search on the search chip | — |
| Y | Options: sort, genre, **May run here**, search | — |
| L1 / R1 · Tab / Shift+Tab | Switch tabs | Previous / next port of the list |
| / , S or F | Search | — |
| B / Esc | Back | Close the details |

**Installing.** Ports go through one queue (several can wait; the status strip
shows each download). JukaHub downloads the zip into
`data/ports-store/incoming`, checks the MD5 PortMaster's catalogue lists and
then lets PortMaster's own installer put it on the card: first headless —
PortMaster's `harbourmaster --no-check install <zip>`, run with `control.txt`
sourced and the `HM_*` paths PortMaster's launcher exports, logged to
`data/gamediag/ports/store-install.log` — and, when that cannot run, by moving
the zip into PortMaster's `autoinstall` folder and opening PortMaster once the
queue is empty. PortMaster is installed first when the card has none. Set
`portsStoreInstaller` to `portmaster` in `jukauser.json` to always use the
PortMaster app.

**Where the data comes from.** The catalogue is PortMaster's `ports.json`
(saved in `data/ports-store/ports.json` for offline browsing), download counts
are PortMaster-Info's `port_stats.json`, and screenshots come from each port's
folder in PortMaster-New (`ports/<name>/screenshot.jpg|png`), shrunk to 320
pixels and kept in `data/ports-store/shots` (at most about 40 MB). On a PC the
store is for browsing; ports install and run on the handheld.

> **Unverified on a device:** the headless harbourmaster run (it needs the
> Python PortMaster brings on stock TrimUI), the screenshot address pattern and
> the `HM_*` export lines. Each failure falls back to the PortMaster app or a
> glyph card.

## Steam games you own

**Apps → Steam** on the handheld matches your Steam library with the
catalogue: a port whose `port.json` store links name the game's Steam app id
matches it, and a port with no Steam link matches a game with exactly its
title (case, punctuation and a "(…)" suffix aside; a bracketed year must
agree). Names Steam also sells as a remake (Doom, Tomb Raider, Thief…) match
only by store link, and a name two of your games share matches neither.
Matches that may run on
this handheld are listed in the **Handheld** tab (L1/R1) and say *Runs here*,
*Port needs game files* or *PortMaster port available* on their row.

On the Handheld tab **A** takes the next step: **Install port** (the same
verified download as the catalogue), **Finish in PortMaster** for a zip still
waiting in `autoinstall`, **Copy files from <PC>** and finally **Play here**.
A ready port plays with **A** from every tab, and counts as installed.

**Copy files** needs JukaHub on a PC paired under Nearby devices with the game
installed in Steam there. The handheld lists the game's Steam folder
(`/peer/v1/steam/files`) and copies it into the port's data folder — its
`gamedata`, `data`, `assets` or `game` folder, or the one the install note
names, else the port's own folder. The card shows the progress. A record of
what was copied (`.jukahub-steam-copy.json` in that folder) keeps a copy that
stopped marked *Copy not finished* until it resumes, and a file the port
shipped itself is never replaced. Only regular files inside that game's own
Steam folder are served (no links or junctions, nothing while Steam updates
the game), and only to a paired device. **Y → Port setup notes** shows the
port's install note: some ports want one store's build or only part of the
folder.

The matched catalogue entries are kept in `data/steam/ports.json`, so the tab
works offline after one look.

> **Unverified on a device:** the catalogue's `store` field layout (read as
> any list of links), and copying a multi-GB game over Wi-Fi.

## Minecraft

**Start → Games → Minecraft** installs three PortMaster ports in one press each
(Java Edition, ClassiCube, Luanti · Mineclonia) through the same catalogue
path. See [MINECRAFT.md](MINECRAFT.md).

## Troubleshooting

| Symptom | Look at |
|---------|---------|
| Black screen, back to JukaHub after a second | **Y → Why didn't it start?**; `data/gamediag/ports/<script>.log` (did it find `control.txt`?) |
| "No such file" for a `.squashfs` | The runtime is missing: open PortMaster with Wi-Fi, or **Update** |
| The port starts but its folder is empty | It needs your game files; see its `README.md` |
| Buttons swapped or wrong | PortMaster's device probe; try `portLaunchVia: firmware` (if your card has `Emus/PORTS`) and report the log |
| Saves land in `Apps/PortMaster/` | The XDG fallback was used; the port log says why the link could not be made |
| PortMaster closes at once | The toast and `data/gamediag/ports/PortMaster.log` |

## Limitations

- PortMaster is an ARM Linux handheld application; Windows builds of JukaHub
  do not run ports.
- A port's own requirements still apply (game data you own, runtimes,
  OpenGL ES only).
- Framebuffer mode, brightness and volume a port changed are not reset after
  it; the firmware's keys own brightness and volume, and no port is known to
  change the panel mode on TrimUI (*unverified on device*).
