# Games Library

The Games Library is JukaHub's front end for the ROM folders on the card: one
screen that lists every console, every game, the box art for the highlighted
title and the emulator that will run it. It opens from **Emulators → Games
Library** (the pinned row above Game Profiles).

## What it reads

The same scan that onboarding and the compatibility reports already use:

```
SDCARD/Roms/
├── FC/Contra.nes
├── GBA/Pokemon Emerald (USA).gba
├── SNES/Yoshi's Island (USA) [!].sfc
└── PSX/Final Fantasy VII (Disc 1).chd
```

Folder names are matched through the alias table in `player/romsetup.go`, so
`FC`, `NES`, `Nintendo - Nintendo Entertainment System` all mean the same
console, and so on for SNES/SFC, GB, GBC, GBA, NDS, N64, MD/Genesis,
Saturn, Dreamcast, PlayStation and PSP.

Titles are the filename with the extension, underscores and trailing release
tags removed: `Pokemon_Emerald_(USA).gba` appears as **Pokemon Emerald**.
Nothing is renamed on the card.

Box art comes from `data/game-art/<Console_Name>/<Title>.png`, which is what
**onboarding → fetch artwork** fills from the libretro thumbnail set. A game
without art shows its initials on an accent tile instead, so the layout stays
the same while the download is missing.

## Launching

Pressing **A** on a game resolves the emulator in this order:

1. `data/game-launchers.json`, if it names a launcher for that console and the
   file still exists;
2. the Emulators catalog: the first available row whose name matches the
   console's aliases exactly (`GBA`, `mGBA` for Game Boy Advance), then the
   first row whose name contains one (`RetroArch GBA`);
3. nothing — the panel toasts which console has no emulator instead of starting
   something random.

The launcher is started detached with the ROM path as its first argument,
which is what stock TrimUI and CrossMix `launch.sh` scripts expect:

```sh
sh /mnt/SDCARD/Emus/GBA/launch.sh "/mnt/SDCARD/Roms/GBA/Pokemon Emerald (USA).gba"
```

Windows `.bat`/`.cmd` launchers go through `cmd /c`, and an `.exe` (PPSSPP,
RetroArch) is started directly with the ROM as its argument. JukaHub raises the
game power profile for the child process and restores it when the emulator
exits, exactly like the Emulators screen.

### Pinning a launcher by hand

If your card uses folder names the catalog does not recognise, create
`Apps/JukaHub/data/game-launchers.json`:

```json
{
  "SNES": "/mnt/SDCARD/Emus/snes9x/launch.sh",
  "PlayStation Portable": "/mnt/SDCARD/Emus/PPSSPP/PPSSPP.sh"
}
```

Keys are console names as the library shows them (case-insensitive). A path
that no longer exists is ignored, so detection takes over again instead of
failing.

## Continue

Every launch is written to `Apps/JukaHub/data/game-recents.json` (last twelve
games, most recent first, no duplicate paths). The library shows that list as a
**Continue** row above the consoles, so resuming the game you were playing is
one press from the Emulators screen. It survives updates and restarts, and it
is plain JSON if you want to clear it.

The Home screen's **Continue** card reads the same file: games and media
recents are merged into one timeline, newest first, and the card names the
console ("Game Boy Advance · 2h ago"). Pressing it relaunches the game through
the same launcher resolution as the library — a configured override first, then
a detected emulator — and reports a missing ROM or emulator as a toast instead
of doing nothing.

Recents are capped and de-duplicated on every write, and the file is written
through a temporary file so a power loss cannot leave half a document behind.

## LAN play (link cable over Wi-Fi)

Handhelds that support a link cable — Game Boy, Game Boy Color, Game Boy
Advance, SNES, Mega Drive, NES, and the other consoles listed below — can play
together without a cable, over the Wi-Fi both devices are already on. Highlight
a game and press **X** to open **LAN Play**:

| Row | What happens |
|-----|--------------|
| **Host this game** | RetroArch starts the game as the host. The panel prints this device's address (`192.168.1.5:55435`) *before* launching, because the emulator takes the screen immediately. The other player types exactly that address. |
| **Join a game** | Type the host's address on the on-screen keyboard (`192.168.1.5` or `192.168.1.5:55435`) and press Start. The panel remembers the last address, so a rematch is one press. |

How it runs: JukaHub resolves RetroArch and a netplay-capable core for the
console, then launches `retroarch -L <core> <rom> --host` (or `--connect <ip>`)
with `--port` and `--nick`. The port is passed on the command line, so your
`retroarch.cfg` is never edited and the session does not depend on what is saved
there. Nothing is announced publicly: the host address is only shared by you.

**Both devices need the same core and the same ROM.** Netplay compares the core
build and the content during the handshake; a different core, a different ROM
revision or a cheat code that changes game state will fail the handshake or
desync. Each side launches its own copy of the game — the hub is not streaming
video.

| Console | RetroArch core looked for |
|---------|---------------------------|
| Game Boy / Game Boy Color | `gambatte`, then `sameboy` |
| Game Boy Advance | `mgba`, then `vba_next`, then `gpsp` |
| SNES / NES | `snes9x`, `fceumm` (plus the other installed build) |
| Mega Drive / Game Gear | `genesis_plus_gx`, then `picodrive` |
| Nintendo 64 / PlayStation / Saturn / Dreamcast | `mupen64plus_next`, `pcsx_rearmed`, `yabause`, `flycast` |
| Arcade / PC Engine / Atari 2600 | `fbneo`, `mednafen_pce`, `stella` |

What it refuses, and why it says so on the panel instead of launching:

- **No RetroArch, or no core for that console.** The stock `Emus/<system>/launch.sh`
  scripts do not reliably forward extra arguments, so the hub starts RetroArch
  itself. Install it (or its core) from **Emulators**, or point
  `JUKAHUB_RETROARCH` at a different build.
- **No Wi-Fi address.** Hosting needs a LAN address; connect to a network first.
- **Port already in use, or nothing answering at the address.** Both are probed
  before RetroArch starts, so a typo or a second session is a one-line message,
  not a black screen and a hang.
- **PSP and Nintendo DS.** Their link play lives inside PPSSPP's Ad-Hoc and
  melonDS's own multiplayer; RetroArch netplay cannot carry either.

On Windows the first host may get a firewall prompt for `retroarch.exe`; allow
it on private networks. On a handheld, keep both devices on the same access
point and within range — netplay tolerates a few dropped packets, not a
reconnect.

## Controls

| Input | Action |
|-------|--------|
| D-pad up/down | move inside the console column or the game list |
| D-pad right | enter the game list from a console |
| D-pad left | back to the console column |
| **A** | open the highlighted console, or play the highlighted game |
| **X** | LAN play for the highlighted game (host or join over Wi-Fi) |
| **Y** | rescan the card (after copying new ROMs) |
| **B** / Back | close the panel |
| Mouse | click a console to select it, a game to play it, outside to close |

## Notes and limits

- The scan only reads filenames; it never moves, renames or edits a game.
- A console with games but no emulator still appears, with its games listed —
  the toast on launch tells you what is missing instead of hiding the game.
- The library is a modal overlay, so it works on the desktop config too, where
  there is no `Roms` folder and the panel simply reports that it found none.
- Artwork is only read locally. The library does not download art by itself;
  run the onboarding artwork step (or drop PNGs into `data/game-art/`).

## What is deliberately not here yet

The library is the foundation, not the whole front end. These are the next
pieces that fit the same data, in the order they are worth building:

| Next | Why it is not in this version |
|------|-------------------------------|
| Save-state slots and resume-at-frame | The library would have to write core-specific state files and know each core's slot naming. Recents resume the game; the emulator still owns the save state. |
| Cheats from the libretro database | Needs a downloaded cheat catalogue per core and a `.cht` writer. The covered consoles are the ones the catalog reports, so it can reuse `findGameLauncher`. |
| Collections and pinned games | A second index over the same paths (`Collections` is already a protected folder in the patch engine). |
| Per-game launch options | The library can pass extra flags today via `game-launchers.json`; per-game overrides need a profile file keyed by ROM path. |
| Per-game overlays and hotkeys | An overlay has to be drawn by the emulator, not behind it. The working path is a JukaHub-managed RetroArch overlay preset and hotkey block, which is separate from the launcher. |
