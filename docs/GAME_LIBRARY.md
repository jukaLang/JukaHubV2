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

Box art comes from `data/game-art/<Console_Name>/<Title>.png`. JukaHub fills
it from the libretro thumbnail set by itself, in the background
([Found automatically](#found-automatically)), and the library fetches the rest
on a press: **L1** (or **F**) downloads covers for every scanned game that does
not have one. A game without art shows its initials on an accent tile instead, so
the layout stays the same while the download is missing.

The fetch runs on a worker; covers appear when it finishes, without reopening
the panel. While it runs the footer shows **Stop** instead of **Fill Gaps**, and
the status line counts the covers already handled (`fetching 42/120`). Pressing
the action again stops within a moment — a cover still downloading is dropped,
but nothing already saved is thrown away, and starting again scans the card and
queues only what is still missing, so an interrupted or crashed run resumes
where it left off. A title the
thumbnail set does not have is remembered in
`data/game-art/.missing-art.json` and not requested again for 30 days, so a
library full of unlisted homebrew does not re-download 200 404s every time.
Network errors are not cached — only a real 404 is.

### Found automatically

Covers are also found without a press. About a minute and a half after JukaHub
starts on a card that has never had a pass, and then at most once a day, a
background pass scans the card once and asks the thumbnail set for the games
that still have no cover, skipping the titles it already answered "not found"
for within the 30 days above. Every half hour it also looks at the ROM folders
— the names in each console folder and the time of a game's sub-folder, not a
walk of the card — so games copied onto the card get their covers within about
half an hour instead of the next day.

It only runs at a moment that costs nothing: online, never while a game or an
emulator has the screen (a game that starts stops the pass after the cover it
is on), never beside another fetch or a running install, and on battery only
at 20 % or more and once the device has been left alone for a minute and a half; on
the charger, or on a PC, at once. It is the same fetch as **L1** / **F**, so
the status line shows its progress, and pressing the action while it runs stops
it, and the automatic passes then wait a day. Covers land in the library without reopening it.
When a pass saved any, one quiet notification says so ("Found box art for 12
games") and stays in the notification centre; a pass that found nothing says
nothing.

A pass is stamped in `data/game-art/.auto-art.json` only when every title got an
answer — a saved cover or a real 404 — so a pass cut short by a game, by Wi-Fi
dropping or by a power cut resumes at the next good moment, and network errors
are retried after half an hour (doubling, at most a day). What leaves the device
is what **L1** / **F** sends: game titles, as plain GET requests to
`thumbnails.libretro.com`. Nothing is uploaded. **Settings → Games & emulators →
Find box art automatically** turns it off (`artAutoFetch`, on by default), and
safe mode skips it for that launch.

## Disc-based systems

A PlayStation or Sega CD rip is only half a game as far as the emulator is
concerned: a bare `.bin` needs a cue sheet before RetroArch will open it, and a
set spread over `Game (Disc 1).chd` and `Game (Disc 2).chd` needs an `.m3u`
before the disc-switch menu has anything to switch.

**Tools → Disc helpers** adds both, in place, to the folders that need them
(`PSX`, `PS1`, `PlayStation`, `Saturn`, `Sega CD`, `PC Engine CD`, and their
`Sega - …` spellings):

- a one-track `.cue` next to every `.bin` that has no sheet of its own, using
  the sector mode the system uses — `MODE2/2352` on PlayStation,
  `MODE1/2352` on the Sega and NEC CD systems;
- one `.m3u` per multi-disc set, listing the discs in order and preferring the
  `.cue` over the `.bin` it describes.

Nothing is overwritten. An existing sheet or playlist — including a
hand-written one with audio tracks — is left exactly as it is, and files named
`(Track 1)` are skipped because they already belong to a multi-track sheet.
Cartridge folders are not touched at all: a `.bin` in `Roms/GBA` is a game
file, not a disc image. The action reports what it added, so pressing it again
on an already-fixed card says so instead of writing anything.

## Android games

Games added with **Add to Games** in the APK Store ([ANDROID.md](ANDROID.md))
appear as one more console, **Android**. The title is the APK's own label and
the cover is the launcher icon the store saved when it added the game
(`data/game-art/Android/<APK name>-<hash>.png`, the hash of the APK's path
keeping apart the many APKs named `base.apk`; a cover an earlier build saved as
`<APK name>.png` is still shown until the game's own is there); a game added
before covers were saved gets its icon at a later scan, once the store has read
that APK again. **A** opens the game's page like any other, and **Play** starts
it through its game profile — the same path as the store's Play and Game
Profiles, in the background: the APK is triaged again, the free memory is
checked, the game's files are staged into memory and AndroidRT starts it. While
it starts, a second **Play** (here, in the store or in Game Profiles) says the
game is already starting instead of starting it twice. A launch that stops shows the launch-failure
dialog with the game's launch log; with no AndroidRT on the device, Play says
so and points at the APK Store's Runtime tab, which installs it.

The row lists only the store's own profiles, read from
`data/game-profiles/local/apkstore-*.json` at every library scan (at most 300;
no APK is opened for it), so **Remove from Games** in the store takes a game off
the row at the next scan. A hand-written AndroidPak profile stays in Game
Profiles. The cover fetch (**L1** / **F**) leaves Android games alone, LAN play
(**X**) says it is for emulator games, **E** opens the game in Game Profiles,
and a game started here joins Continue.

## Completeness

The header carries a ring that fills as the library gains covers, with the
percentage inside it, and every console row has a thin meter showing that
console's share. The status line spells the same figure out
(`412 game(s) in 9 console(s) · 330 cover(s) (80%)`), so a card with a few gaps
is visible without opening a settings panel.

The numbers are counted from the files on the card, not from the download
attempts: a cover that exists is counted whatever put it there, including one
copied from a PC or a scraper. Deleting a cover lowers the figure again.
The library can also look up genre, developer, release year and player count
from libretro-database's separate DAT files. Descriptions and notes are yours to
write; manuals and screenshots are not fetched.

## Launching

Pressing **A** on a game resolves the emulator in this order:

1. `data/game-launchers.json`, if it names a launcher for that console and the
   file still exists;
2. the Emulators catalog: the first available row whose name matches the
   console's aliases exactly (`GBA`, `mGBA` for Game Boy Advance), then the
   first row whose name contains one (`RetroArch GBA`);
3. nothing — the panel toasts which console has no emulator instead of starting
   something random.

On the handheld, an `Emus/<system>/` folder's launcher is the script its
`config.json` names in `"launch"`, which is what the firmware's own menu
starts: `launch.sh` on the stock OS, the default emulator's own script on
CrossMix (`ppsspp_1.17.1_gl.sh` for PSP, `drastic.sh` for NDS). After that
comes the first `"launchlist"` script that exists, then `launch.sh`, then a
script named `launch`. A declared script must be a `.sh` file inside the
folder. When the launcher is a declared script rather than `launch.sh` or
`launch`, the Emulators tile names it, so you can see which emulator a console
starts.

The launcher is started detached with the ROM path as its first argument,
which is what stock TrimUI and CrossMix launcher scripts expect:

```sh
sh /mnt/SDCARD/Emus/GBA/launch.sh "/mnt/SDCARD/Roms/GBA/Pokemon Emerald (USA).gba"
```

Windows `.bat`/`.cmd` launchers go through `cmd /c`, and an `.exe` (PPSSPP,
RetroArch) is started directly with the ROM as its argument. JukaHub raises the
game power profile for the child process and restores it when the emulator
exits, exactly like the Emulators screen.

On the handheld the launcher also gets the environment the firmware's own menu
would have given it, not JukaHub's. `launch.sh` records `HOME`, `PATH` and
`LD_LIBRARY_PATH` before it points them at JukaHub's folder
(`JUKAHUB_ORIG_ENV` names them, `JUKAHUB_ORIG_<NAME>` holds each value that was
set), and every game, emulator and app JukaHub starts gets those originals
back; a variable that was unset stays unset. Before this, a launcher that did
not reset them ran RetroArch with JukaHub's library folders ahead of the
firmware's — on CrossMix, the stock `/usr/lib` copies ahead of
`/mnt/SDCARD/System/lib` — so the same game could behave differently from
JukaHub and from the firmware's menu. With an older `launch.sh` that records
nothing, only JukaHub's own folders are taken out of `PATH` and
`LD_LIBRARY_PATH`. The time zone chosen in Settings (`TZ`) is kept on purpose.
JukaHub's own tools — the Terminal, Power Tools snippets, PortMaster ports, Wine
and Android titles — keep JukaHub's environment.

The Emulators page lists RetroArch itself on the handheld, beside its cores:
the tile names its folder, how many cores it holds and the config it reads,
and **A** opens RetroArch's own menu, started the way the firmware's launchers
start it (from its folder, with `HOME` there). **A** on a core row opens this
library searched for the console that core plays (the first **B** clears the
search).

### Per-game launch options

**Start** (or **O** on a keyboard) opens the options sheet for the highlighted
game. The first four rows are saved library view settings; the rest apply to
that launch only:

- **Display title** — the name shown in the library. It is stored beside the
  game, not written into the ROM filename, so the file stays exactly as it is.
  Leave it empty to go back to the name derived from the file.
- **Visibility** — hide this game, or make it visible again. Hidden games are
  left out of the console lists, Continue, search and tag filters until
  **Show hidden** is on.
- **Library sort** — Title, Last played, Playtime, Rating or Year. The order is
  saved and applies to every console. Games with no value for the chosen sort
  (never played, unrated, no year) come last.
- **Show hidden** — reveal the games hidden from the library; turn it off to
  tuck them away again.
- **CPU limit** — `Auto`, then 2.0, 1.8, 1.6, 1.4 and 1.2 GHz. `Auto` follows the
global `powerProfileMaxFreq`; any other value caps the clock for this game's  launch and nothing else. **A** and **Right** step one rung cooler, **Left** warms
  back up. A cap can only lower the chip's top OPP, never invent a faster one, and
  the firmware's own ceiling is written back when the emulator exits. With
  `powerProfiles: false` the value is still stored, but JukaHub leaves cpufreq
  alone, as that switch asks.
- **Extra arguments** — appended after the ROM path:

  ```sh
  sh /mnt/SDCARD/Emus/PSP/launch.sh "/mnt/SDCARD/Roms/PSP/Game.iso" --fast
  ```

  Double quotes group a value with spaces (`--profile "My Game"`). This row is
  for a launcher that forwards its arguments (`"$@"`), or for an emulator binary
  started directly. Stock TrimUI and CrossMix `launch.sh` scripts end in
  `ra64.trimui … "$*"`, which joins the ROM path and everything after it into
  one argument: with extra arguments RetroArch is handed a ROM path that does
  not exist. Local play's player order is therefore not passed to those scripts
  either.
- **Reset** — clears both rows for the game and puts it back on the defaults.
- **Open in…** — hands this game straight to the tool that owns its next step
  (see *Handing a game to its tool* below), the same as **E** in the library.

A game with a cap shows it in the library list (`PSP · God of War.iso ·
1.8 GHz`), so a deliberately cool title is recognisable without opening the
sheet. Settings live in `Apps/JukaHub/data/game-options.json`, keyed by ROM
path; only games that differ from the defaults are written:

```json
{
  "schemaVersion": 1,
  "games": {
    "/mnt/SDCARD/Roms/GBA/Zelda.gba": { "max_freq_khz": 1200000 },
    "/mnt/SDCARD/Roms/PSP/God of War.iso": { "max_freq_khz": 1800000, "args": "--fast" }
  }
}
```

Because the key is the path, moving or renaming a ROM returns it to the defaults
rather than inheriting another game's settings. A broken file reads as "no
overrides" — the library still opens. The clock is applied through the same
profile code as the global setting, so a firmware with a read-only
`scaling_max_freq` logs one line and runs the game at its own clock instead.

Display titles, hidden flags and the sort mode are a library view, not launch
options. They live in `Apps/JukaHub/data/game-library.json`, keyed by a hash of
the ROM's content (its size plus the first and last 256 KiB), so moving or
renaming the file keeps its name and hidden state:

```json
{
  "version": 1,
  "sort": "rating",
  "show_hidden": false,
  "games": {
    "9c1f...": { "display_title": "My Zelda", "hidden": true }
  }
}
```

The launch options above remain keyed by path because they belong to one
launcher's invocation; the view state follows the cartridge instead.

### Resume and auto-save

When the resolved launcher runs RetroArch (its path contains a `RetroArch`
segment, or the `launch.sh` script mentions `retroarch`, `ra64.`/`ra32.` or a
`_libretro` core), JukaHub turns on two switches in `retroarch.cfg` before the
game starts:

```ini
savestate_auto_save = "true"
savestate_auto_load = "true"
```

RetroArch then writes an auto state when the emulator exits and loads it the
next time that content starts, so the Continue row and the library both put you
back where you stopped. The edit is line-based — comments, ordering and every
other key survive — and the original file is kept once as
`retroarch.cfg.jukahub-backup`. Only the auto state is involved; manual save
slots are untouched.

The switches hold for JukaHub's launch only. Before the edit, JukaHub writes
what it changes (and the value you had, or that you had none) to
`Apps/JukaHub/data/retroarch-launch-scope.json`; when the game exits, your
values go back. RetroArch may rewrite the whole file as it exits, so only these
keys are touched, and only where they still hold JukaHub's value — a setting you
changed in RetroArch's menu during the game is yours. A crash or a power cut
mid-game leaves the journal behind, and the next JukaHub start restores from it;
a launch whose emulator never started is put back about twenty seconds later.
The same applies to the recording folder (`recording_output_directory`,
[RECORDINGS.md](RECORDINGS.md)) and to the in-game OSD preset. So a game started
from the firmware's own menu — CrossMix's or the stock one — behaves exactly as
it did before JukaHub, with its own resume and save-on-MENU features. Earlier
versions left the switches on for good; the first start of this version puts
back the values the backup shows you had, for keys that still hold JukaHub's
value.

On TrimUI cards JukaHub edits `RetroArch/retroarch.cfg`, the file beside
`ra64.trimui` that the firmware's launchers make RetroArch read, and a
`RetroArch/.retroarch/retroarch.cfg` too when one exists; `JUKAHUB_RETROARCH_CFG`
names a different file.

This is deliberately RetroArch-only. mGBA, PPSSPP, DraStic and the other
standalone emulators own their own state formats, so the hub does not pretend it
can resume them. To opt out entirely, set `savestateResume` to `false` in
`variables.Custom` in `jukauser.json`; JukaHub will stop editing the config.

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
console ("Game Boy Advance · 2h ago"). The same timeline carries YouTube,
local video, Live TV and podcast rows — a started episode is labelled
**Podcast** and reopens the audio player at its saved position, so "pick up
where I left off" does not stop at games. Pressing it relaunches the game through
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
  itself. Install the core from **Emulators** — **A** on the system's tile, or
  **X** on any tile — or point `JUKAHUB_RETROARCH` at a different build.
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
| **Start** / **O** | open the game options sheet: display title, visibility, library sort, launch options |
| **X** | LAN play for the highlighted game (host or join over Wi-Fi) |
| **E** | open the highlighted game in its tool: a covering Game Profile, its exact Compatibility row, or the Emulators page with the console's tile highlighted. Closing that tool (**B**) comes back to the library where you left it |
| **L1** / **F** | fill the gaps: fetch covers for scanned games without one; while a fetch runs, stop it |
| **S** / **/** / **R1** | search the library (on-screen keyboard; the list filters as you type) |
| **R** / **R3** | roll a random game from this console or the whole library |
| **Y** | rescan the card (after copying new ROMs) |
| **B** / Back | clear an active search, then close the panel |
| Mouse | click a console to select it, a game to play it, outside to close |

## Handing a game to its tool

**E** (or the **Open in…** row in the options sheet) hands the highlighted
game to the panel that owns its next step:

1. a ready **Game Profile** whose game folder covers the ROM wins — that is a
   PC or port game and the profile is its launcher;
2. an exact **Compatibility** row (matched by path key) is next, so an already
   assessed title opens on its own report and boost plan;
3. the **Emulators** page follows, with the console's tile highlighted (folder
   names like `GBA`, `PS` and `Genesis` are matched to the console, installed
   tiles preferred), falling back to the first installed tile;
4. anything else opens **Compatibility**, which can scan for a title it does
   not know yet.

The tool opens as the only overlay, and closing it with **B** reopens the
library exactly where the handoff left it — same console, cursor, scroll and
active search — instead of dropping on a default screen.

## Search

**S** (or **R1**) opens the on-screen keyboard on a search field. The whole
library is searched at once, not just the highlighted console:

- a game matches on its title, its console, or its file name, so `zelda`,
  `gba` and `minish` all find the same cart;
- typing a console keeps that console's whole list, which is the quickest way
  to browse one system;
- the list narrows on every keystroke and the header shows the match count;
- the **Continue** row is left out while searching, because it lists the same
games a second time;
- **B** clears the search first; a second **B** closes the library.

Matching is a case-insensitive substring test, not fuzzy: `zeld` finds *Zelda*
but `zld` does not.

## Random pick

**R** (or clicking in the right stick, **R3**) opens a small sheet that rolls a
game for you, like CrossMix's random picker — but it lands the highlight on the
pick instead of starting it, so **A** plays it and **R** rolls again without
leaving the library.

| Row | What it rolls from |
|-----|--------------------|
| **This console** | only the console on screen; when **Continue** is highlighted, the console of that recent |
| **Whole library** | every console at once, recents left out so a recently played game is not counted twice |

The detail line under each row counts what the roll will actually draw from, so
it follows an active search: with `metroid` typed, both rows roll only the
matching games and the whole-library row says *matches*. The roll never picks
the game already under the highlight while the scope has another candidate, so
a second **R** always moves. **B** cancels the sheet.

Picking jumps the library to the game's console, row and scroll position and
shows a toast naming it (`Random: ... — A plays it, R for another`). A game
that has disappeared from the card since the last scan is reported instead of
moving the highlight somewhere unrelated, and **R** in an empty library explains
that there are no games yet rather than opening an empty sheet.

## Video snaps

A short clip of the focused game can play in the preview box, the way Knulli
and the Batocera family do it. Nothing is downloaded: the clips are files you
put on the card, mirroring the box-art tree.

```
<SD>/data/game-snaps/Nintendo_Entertainment_System/Super Mario Bros.mp4
<SD>/data/game-snaps/Arcade/1943.webm
```

- The folder is the console name with spaces turned into underscores, and the
  file is the ROM's name without its extension — exactly the layout of
  `data/game-art/`, so an existing collection can be copied across.
- There is no snap for a whole set, and **no scraper**: no account, no key, no
  network request. The library checks for a file and plays it.
- `.mp4` and `.webm` are tried in that order. Other containers are ignored on
  purpose — the bundled ffmpeg could decode them, but a menu preview is not
  worth a container matrix.
- Moving the collection elsewhere is a config key: `variables.Custom.gameSnapsDir`
  points at a different tree, and `gameSnaps: false` switches the feature off.
  It is on by default, and a card with no clips costs nothing.
- Switching it off does not need the file: **Settings → General → Video snaps**
  shows `On` or `Off` and flips on a press, writing `gameSnaps` in
  `jukauser.json`. A preview already playing stops on the next frame, and no
  clip is started again until the row is switched back on — the decoder asks
  the same setting the row writes, so the two cannot disagree.
- The row also reports what the switch has to work with: `Video snaps · On · 12`
  counts the library's games that have a clip, and a card with none reads
  `On · none` — the one case where the switch is on but nothing can play. The
  count is the same `gameSnapPath` the preview uses, so it never claims a clip
  the panel would not play, and a clip left behind for a ROM that is gone does
  not count. It is a library scan, taken when the page opens on the scene's own
  goroutine and reused for 30 seconds, so opening Settings never walks the card
  twice or blocks a frame.
- The same figure rides on the Games Library's own header: the status line
  under the title carries `· 12 clip(s)` (or `· no clips` on a card with none,
  and nothing at all before the first count lands), read from that same census
  when the panel opens — so the number is visible exactly where the previews
  play, and the two screens can never disagree.

How it behaves while you browse:

- The clip starts only after the focus has rested on one game for about a third
  of a second, so holding **Down** through 200 titles does not start 200
  decoders. Scrolling past it is free.
- While it plays, the clip replaces the cover in the same box and carries a
  small **SNAP** chip, so a moving picture is never mistaken for a very
  animated box art. Until the first frame arrives — and whenever a frame is
  not available — the cover stays, so opening the library never flashes an
  empty box.
- The decoder is a separate ffmpeg process at 320×180, 10fps, muted and looped.
  It is not the video player's session: playback, the queue and the now-playing
  dock are untouched, and closing the panel, moving the focus or entering the
  video player stops it immediately.
- A clip that produces no frames is remembered for the rest of the session and
  never retried, so one bad file cannot stutter the list.

This is a browse-time aid, not a scraper. Covers come from the public libretro
thumbnail set; game facts use libretro-database DAT files, and descriptions and
notes are yours to write. Manuals and screenshots are not fetched. Snaps are
whatever you put on the card.

## Notes and limits

- The scan only reads filenames; it never moves, renames or edits a game.
- A console with games but no emulator still appears, with its games listed —
  the toast on launch tells you what is missing instead of hiding the game.
- The library is a modal overlay, so it works on the desktop config too, where
  there is no `Roms` folder and the panel simply reports that it found none.
- Artwork is never uploaded. The fetch only reads the public libretro
  thumbnail set; local PNGs in `data/game-art/` are used as-is and never
  replaced.
- A video snap is decoration: it is decoded on the A133P's CPU, so a card full
  of 1080p clips will cost more than one of 480p clips. The 320×180 decode size
  is chosen to keep that cost small.
- Resume is the emulator's auto state, not a JukaHub snapshot. Quitting the
  emulator in a way that skips its shutdown hook (a hard power-off, for example)
  can lose the most recent state.

## What is deliberately not here yet

The library is the foundation, not the whole front end. These are the next
pieces that fit the same data:

| Next | Why it is not in this version |
|------|-------------------------------|
| Create and manage save states in JukaHub | The game page lists RetroArch's existing auto and numbered states and can start from one. Saving a new state or stepping to an exact frame still uses RetroArch's own menu. |
| Cheats from the libretro database | Needs a downloaded cheat catalogue per core and a `.cht` writer. The covered consoles are the ones the catalog reports, so it can reuse `findGameLauncher`. |
| Curated collections | Favorites and custom tags already filter the library. A separate collection editor with its own saved order is not included. |
| Managed emulator overlays and hotkey profiles | Per-game extra arguments and the player-order setting cover some launch needs, but JukaHub does not create or manage RetroArch overlay layouts and named hotkey profiles. |
