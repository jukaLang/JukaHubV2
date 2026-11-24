# Games Library

The Games Library lists ROMs, Android games and local HTML5 games in one place,
with covers and a Continue row. It opens from **Emulators → Games Library** (the
pinned row above Game Profiles). New homebrew for ten of its consoles comes
from itch.io through [Get Homebrew](HOMEBREW.md), straight onto the shelves.

It is a full-screen page in Home's glass style, under the top bar, in two
levels. The first is a grid of buttons, one per console (Continue first): each
shows the system's icon, its name, how many games it has, a cover meter and the
first cover it has (the console's name in pixel letters until one is on the
card). **A** — or a click — opens a console's page: its games (as a list, 3D
shelf or cover column) beside a large preview of the highlighted game's box art
or video snap. **B** goes back to the consoles, and **B** on the consoles
closes the library.

### Console look

How the console buttons look is a saved choice, **Covers** by default:

- **Covers** — the buttons above: icon, name, game count, cover meter and the
  first cover.
- **Pixel names** — each button *is* the console's name in big pixel letters,
  with its game count small underneath. Only the highlighted button is a glass
  card; the others are a faint plate.
- **Pixel cards** — a card per console like a cartridge: the name in pixel
  letters on an inset label, the system's icon and game count above it, and a
  grip along the bottom edge.

When you land on a console its letters fly together square by square: every
square starts somewhere across the button (and a little past it), and they set
off left to right and settle in about half a second. **Reduce motion** (and
Battery Saver) shows them in place. Names use a 5×7 pixel face; accented
letters are spelled plain (Pokémon is POKEMON) and `& ' ! : , ( )` have their
own letters.

**X** on the console buttons steps to the next look. It is also the game
options sheet's **Console look** row and **Settings → Games & emulators →
Console look**. It is saved in `game-library.json` as `console_look`
(`covers`, `pixel_names` or `pixel_cards`). The pixel looks load no covers for
the buttons; a console's page is the same in every look.

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

### More consoles: NAOMI, Atomiswave, DSi, OpenBOR, CoCo

The folders spruceOS and Batocera cards use for these consoles are
read too (`player/extra_systems.go`):

| Folder | Console | Games | What plays them |
|---|---|---|---|
| `NAOMI` | NAOMI | `.zip`, `.7z`, `.lst`, `.bin` | RetroArch's Flycast core (the Dreamcast's), with `naomi.zip` in the BIOS folder's `dc` folder or beside the games |
| `ATOMISWAVE` | Atomiswave | `.zip`, `.7z`, `.lst`, `.bin` | Flycast, with `awbios.zip` in the BIOS folder's `dc` folder or beside the games |
| `NDSI` (or `DSI`) | Nintendo DSi | `.nds`, `.dsi`, `.ids`, `.zip`, `.7z` | RetroArch's melonDS DS core in DSi mode, with `dsi_bios7.bin`, `dsi_bios9.bin`, `dsi_firmware.bin` and `dsi_nand.bin` from your own DSi in the BIOS folder (or its `melonDS DS` folder) |
| `OPENBOR` | OpenBOR | `.pak` | the OpenBOR engine: an `Emus/OPENBOR` folder whose `launch.sh` takes the `.pak`, or PortMaster's OpenBOR (copy the `.pak` into its `paks` folder and start it from Ports) |
| `COCO` | Tandy CoCo | `.ccc`, `.rom`, `.cas`, `.dsk`, `.vdk`, `.dmk`, `.jvc`, `.os9`, `.bin` | XRoar, in an `Emus/COCO` folder with the CoCo ROMs (`bas13.rom`, `extbas11.rom`, `disk11.rom`); it has no RetroArch core |

`naomi.zip` and `awbios.zip` beside the games are BIOS files, not games, and are
not listed. An installed Flycast or melonDS DS core is offered for these games
like any other installed core (see [Other emulators for a console](#other-emulators-for-a-console)),
and **Get more cores** fetches it for a console with games. A DSi game started in
melonDS DS gets the core's **Console mode** set to DSi for that one launch (put
back afterwards); DSi mode has no save states. Game Ready names the DSi files
that are missing before the game starts. With nothing to play OpenBOR or CoCo
games, starting one says what to add instead of "no launcher". Covers come from
the libretro thumbnails for NAOMI, Atomiswave and the DSi; OpenBOR and CoCo
games have none there and use [covers from gameplay](#covers-from-gameplay).

Titles are the filename with the extension, underscores and trailing release
tags removed: `Pokemon_Emerald_(USA).gba` appears as **Pokemon Emerald**.
Nothing is renamed on the card.

### Big cards open at once (the library index)

JukaHub keeps an index of the card in `data/library-index.db`, so a library of
thousands of games opens at once instead of walking the whole card first:

- **Opening.** The first time the library (or Home's Games tab) opens after
  JukaHub starts, it shows the library exactly as you last saw it, straight
  from the index, while the card is checked in the background. Any change
  slides in a moment later. Your selected game stays selected.
- **Only what changed is read.** The index remembers every ROM folder: when
  it was last changed and what was in it. A folder that has not changed is
  not read again. The work the old scan repeated on every open is kept too:
  the fingerprint that keeps a game's name and hidden flag when you rename the
  file, and which tracks a `.cue`, `.gdi` or `.m3u` uses.
- **Changes while JukaHub runs.** On the handheld, JukaHub notices files
  arriving or leaving as they happen — over USB (MTP), FTP or SFTP, from the
  Files app or a phone drop, or saves an emulator writes — and an open library
  refreshes itself a few seconds after the copying stops. After a game quits,
  its folder is checked again. On a PC, every check reads each folder's list
  of names (a PC's FAT or exFAT drive does not always mark a folder as
  changed), and an open library also looks for changes once a minute.
- **A card edited in a computer.** Windows and macOS do not always mark a
  FAT or exFAT folder as changed when a file is added inside it. So the first
  check after JukaHub starts reads every folder's list of names (only the
  names, nothing is opened), and so does **Y** (Rescan) in the library.
- **Fingerprints in the background.** Games up to 64 MB get their CRC32,
  which the game details and cover matching look up first. This runs one file
  at a time at the lowest priority, waits while a game or video plays, and
  stops when the battery is under 30% and not charging. Turn it off in
  **Settings → Games & emulators → Fingerprint games in the background**
  (`libraryFingerprints`, on by default). Disc images and bigger files are
  matched by name, as before.
- **Rebuild.** **Settings → Games & emulators → Rebuild library index** deletes
  the index and reads the card again; its line says how many games and folders
  the index knows. A damaged index, or one from another JukaHub version, is
  rebuilt by itself. The index is only a copy: deleting the file loses
  nothing.

Box art comes from `data/game-art/<Console_Name>/<Title>.png`. JukaHub fills
it from the libretro thumbnail set by itself, in the background
([Found automatically](#found-automatically)), and the library fetches the rest
on a press: **L1** (or **F**) downloads covers for every scanned game that does
not have one. A game without art shows its initials on an accent tile instead, so
the layout stays the same while the download is missing.

A cover is matched to the ROM's title by the server's list: the exact name
first, then the title without tags, articles and spacing. When none of those
finds one, the closest title on the list is taken (the most shared words,
near-misses like "Saphire" and joined-up names like `SuperMarioWorld2`
included), so "Zelda Minish Cap" gets *The Legend of Zelda - The Minish Cap*
and "Street Fighter Alpha 3" at worst another Street Fighter Alpha box. More
pictures beat fewer: a sister console's list is asked too (Game Boy and Game Boy
Color, Master System and Game Gear, Mega Drive with Sega CD and 32X, Neo Geo
with Neo Geo CD and FBNeo…), first for the exact title, then for the closest,
and a game with no box art anywhere gets its title screen or an in-game
picture instead. A title that shares no real word with anything on those lists
gets no picture.

The **Built-in games** console lists JukaLand, the Synth, Solitaire and the
Games screen's arcade minigames (Snake, Tetris, 2048, Pong, Minesweeper,
Flappy); A opens them, an arcade game straight into play. Their covers ship
inside JukaHub (`player/builtin_covers/`, drawn by `branding/mkcovers.py`) and
are copied to `data/builtin-covers/`; the Games screen's cards show the same
pictures.

The fetch runs on a worker; covers appear when it finishes, without reopening
the panel. While it runs the footer shows **Stop** instead of **Fill Gaps**, and
the status line counts the covers already handled (`fetching 42/120`). Pressing
the action again stops within a moment — a cover still downloading is dropped,
but nothing already saved is thrown away, and starting again scans the card and
queues only what is still missing, so an interrupted or crashed run resumes
where it left off. A title the
thumbnail set does not have is remembered in
`data/game-art/.missing-art.json` and not requested again for 14 days, so a
library full of unlisted homebrew does not re-download 200 404s every time.
Network errors are not cached — only a real 404 is.

### Found automatically

Covers are also found without a press. About a minute and a half after JukaHub
starts on a card that has never had a pass, and then at most once a day, a
background pass takes the games from the library's last scan when the ROM
folders have not changed since (else it scans the card once) and asks the
thumbnail set for the games that still have no cover, skipping the titles it
already answered "not found" for within the 14 days above. Every half hour it also looks at the ROM folders
— the names in each console folder and the time of a game's sub-folder, not a
walk of the card — so games copied onto the card get their covers within about
half an hour instead of the next day. On a handheld where JukaHub keeps the
time, the very first pass waits up to half an hour for network time to set the
clock, because the not-found answers and the pass stamp are dated.

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

### Covers from gameplay

A game no cover is found for — homebrew, a hack, a translation, a dump named
unlike any box art — gets a picture of itself instead of a lettered tile. Two
minutes into playing it (counting only the time it runs: not while it is
paused under the in-game menu or Juka, an alarm or the screen-off sleep),
JukaHub reads the screen — the framebuffer on the handheld, as **Hold Menu**
screenshots do; on Windows the game's own window, only while that window is in
front — and keeps the picture as the game's cover in the library, Continue and
Home. The return banner says so ("1 cover taken from the game").

The black bars around a game that does not fill the screen are trimmed and the
picture is scaled to at most 640×480. A frame that is black, white or one flat
colour is never kept, nor one read within 30 seconds of **Menu** going down
(the emulator's menu may be open), while a library RetroArch says it is
paused, or in the 5 seconds after a pause or an alarm. For the first four
minutes neither is one that did not move in under a second (a pause screen)
nor one dark nearly all over (a loading line); a game played on black — most
shoot-'em-ups — gets its picture after four minutes. Each is tried again
after 20 more seconds of play, up to eight minutes; a game that already has
any cover, or a picture from an earlier session, is left alone.

The picture is saved as `data/game-art/.gameplay/<console>/<game>.png`, apart
from the downloaded covers: the box-art fetch still looks for a real cover for
the game, and one it finds later — or one picked in **Choose cover** — takes
its place. **Clean up covers** also takes the pictures of games that are gone
and of games a downloaded cover shows for now. **Settings → Games & emulators
→ Covers from gameplay** stops new pictures (`gameplayCovers`, on by default;
those already taken stay until a cover replaces them). A game in exclusive full screen on a PC can
read as black; it then keeps its tile.

### Choosing a cover by hand

When a game's cover is wrong or missing, **Game options → Choose cover** lists
the five box-art names on the thumbnail server closest to the game's, then the
closest pictures already in the console's `Imgs` folder. The highlighted one is
shown beside the list; **A** saves it as the game's cover in `data/game-art`
(an `Imgs` picture is scaled to cover size and saved as PNG; the files in
`Imgs` are only read).

**Game options → Name for covers** sets the title the game's box art is looked
up by, for a file named differently from the game ("Zelda Minish" →
"Legend of Zelda, The - The Minish Cap"). It is kept in
`data/artwork-aliases.json`, read before the built-in aliases; the file's own
tags such as `(USA)` still pick the release. Saving it looks the game up again
at once (a cover the firmware or you put on the card is left alone); an empty
name goes back to the file name. The file can be edited by hand —
`{"version": 1, "aliases": {"Zelda Minish": "Legend of Zelda, The - The Minish Cap"}}` —
and one JukaHub cannot read is left as it is, never overwritten.

### Removing a cover

**Game options → Remove cover** takes a game's picture away for good: the
library, Continue and Home show its lettered tile, and nothing brings a
picture back on its own — the box-art fetch, **Covers from gameplay** and a
port's own picture all skip it. The question opens on **Cancel**; **Remove the
cover** moves JukaHub's copies (the downloaded cover and a picture from the
game) to the reset trash. When the firmware or another front end keeps a
picture beside the ROM (`Imgs`, `.media`, MinUI's `.res`), **Also remove the
card's picture** moves that one too, so the stock menu shows none either;
otherwise it stays on the card and is only hidden in JukaHub. The choice is a
small marker, `data/game-art/.nocover/<console>/<game>.off`, which follows the
game through **Rename file**. The row then reads **Restore cover**: **A**
removes the marker, shows the card's picture if there is one and otherwise
looks the game's cover up at once. **Choose cover** and **Name for covers**
also bring covers back. A removed cover also takes the game's screenshot and
title screen (below) to the trash, and none is fetched for it.

### Artwork: game art settings, screenshots, title screens

**Settings → Games & emulators** has the game-art rows
(`artstyle.go`). They shape the list layout's picture; the 3D shelf, the cover
column, Cover Flow and the Cube are unchanged.

| Row | Choices | What it does |
|---|---|---|
| **Game art** (`gameArt`) | On (default) / Off | Off leaves the list text only: no picture beside or behind it. |
| **Art type** (`gameArtType`) | Box art (default) / Screenshot / Title screen | Which picture the game shows. A game without the chosen one shows its box art. NextUI's `.media` pictures (and `Imgs`, `.res`) count as box art. |
| **Art style** (`gameArtStyle`) | Thumbnail (default) / Background | Thumbnail is the picture beside the list. Background draws the focused game's picture full height behind the list, against the right edge and dimmed — darker on the left where the titles are — and the list takes the whole width. |
| **Art width** (`gameArtWidth`) | Standard (default: 34 %, square, capped by the page height) / 30–60 % | The picture's share of the page. A frame wider than the page is tall becomes landscape, which gives a screenshot room. |
| **Art corner radius** (`gameArtRadius`) | Square (default) / 4–24 px | How round the picture's own corners are. |
| **Console backgrounds** (`consoleBackgrounds`) | On (default) / Off | A console's picture behind its game list, drawn to fill and dimmed with the theme's base colour: `data/backgrounds/<System>.png` (the console's name with spaces as `_`, like the game-art folder, or as written; `.png` or `.jpg`), else NextUI's `Roms/<console folder>/.media/bg.png`. The consoles grid keeps Home's backdrop. |
| **Artwork Manager** | | Opens the screen below. |

**Where the pictures live.** Box art stays where it was:
`data/game-art/<System>/<stem>.png`, or the card's own picture. Screenshots
and title screens go in hidden folders of the game-art folder, named like the
box art: `data/game-art/.snap/<System>/<stem>.png` (the thumbnail server's
`Named_Snaps`) and `data/game-art/.title/<System>/<stem>.png`
(`Named_Titles`). **Clean up covers** looks inside both with the box art's
rules (only pictures of games no longer on the card); **Rename file** and the
Game Boy Color sort carry them; **Delete game** and **Remove cover** take them.

**Fetching them.** With Art type set to Screenshot or Title screen, **Fill
gaps** (**L1** / **F**) fetches the missing covers first, then the missing
pictures of that type — matched like the box art (exact name, without a
release number, the game database's name, a sister console's list, then the
closest title). It is the same downloader: one run at a time, the same
progress, and **L1** / **F** stops the run and anything queued behind it. A
title the server has no picture for is not asked again for two weeks (the
same not-found file, `data/game-art/.missing-art.json`, under keys of its
own). The automatic box-art pass fetches box art only.

**Artwork Manager** (Settings → Games & emulators, Start → Tools, or Game
options → Artwork Manager in the library). One row per console with how many
of its games have the shown picture type, a bar and a state (Complete, N
missing, Queued, Fetching…). The top row switches the type shown (it starts
on Settings' Art type), **Fetch all missing** (or **Stop fetching** while a
run or the queue is busy) and **Clear done**, which hides the complete
consoles (**Show all** brings them back).

- On a console row, **A** opens its games: each with **Box**, **Shot** and
  **Title** lit for the pictures it has (**A** on a game says where they are).
- **Right** reaches the row's **Fetch** button: **A** queues that console's
  missing pictures of the shown type. **Y** queues every console.
- **X** is **Reset artwork** for the console, after a question that opens on
  **Cancel**: the box art, screenshots and title screens JukaHub downloaded
  for it (covers picked with Choose cover too) go to the reset trash, and its
  not-found answers are forgotten so the next fetch asks again. The console
  folder's `.media` / `Imgs` / `.res` pictures, pictures taken from games and
  "no cover" choices stay. It waits while a fetch runs.

Games whose cover was removed are left out of the counts. Opened from the
library, **B** on the console list goes back into the library on the console
it was on. The screen has no mouse targets yet: the pad and keyboard drive it.

## Disc-based systems

A PlayStation or Sega CD rip is only half a game as far as the emulator is
concerned: a bare `.bin` needs a cue sheet before RetroArch will open it, and a
set spread over `Game (Disc 1).chd` and `Game (Disc 2).chd` needs an `.m3u`
before the disc-switch menu has anything to switch — and before the library
shows it as one game instead of one row per disc.

The library adds both by itself. Every library scan starts a small pass on its
own thread over the disc consoles' folders (`PS`, `PSX`, `PS1`,
`PlayStation`, `Saturn`, `Sega CD`, `PC Engine CD` and their `Sega - …`
spellings; NextUI's own `Name (TAG)` folders only on request, below). It only reads
a folder whose modification time moved since the last look, so a scan with
nothing new costs a stat per game folder; what it writes is logged, and an
open library rescans once so the discs show as one game. It makes:

- a one-track `.cue` next to every `.bin` of 1 MiB or more that no sheet
  names, with the track mode read from the image's first data sector
  (`MODE2/2352` or `MODE1/2352`; a `.bin` with no CD sync gets no sheet, and a
  smaller `.bin` is a BIOS, not a disc);
- one multi-track `.cue` for a Redump split set — `Game (Track 1).bin`,
  `Game (Track 2).bin` … — with each track's mode read past its gap, audio
  tracks as `AUDIO` and Redump's `INDEX 00 00:00:00` / `INDEX 01 00:02:00`
  gaps, made before the `.m3u` step (the CUE generator). The ROM
  manager's **Make missing .cue files** and the Tools hub's **CUE generator**
  run the same pass on request and say how many sheets they made;
- one `.m3u` per multi-disc set of loose discs — `(Disc 1)`, `[CD 2]`,
  `- Disc 3` — in the console's folder or in a game's own folder, listing the
  discs in order and preferring the `.cue` over the `.bin` it describes
  (`.chd`, `.cue`, `.pbp`, `.iso`, `.img` and `.bin` discs alike);
- one `.m3u` for a game kept in disc folders, `Game/Disc 1/…` and
  `Game/Disc 2/…`: named after the game's folder, beside the disc folders,
  naming each disc's `.cue` (else its `.chd`, `.pbp`, `.iso` or `.img`, else a
  lone `.bin`, which gets its cue first). A disc folder still being copied is
  looked at again on the next scan.

Nothing is overwritten. Files are created only where none exists, an existing
sheet or playlist — including a hand-written one with audio tracks, or a
playlist under another name that already lists the discs — is left exactly as
it is, and files named `(Track 1)` are skipped because they already belong to
a multi-track sheet. Cartridge folders are not touched at all: a `.bin` in
`Roms/GBA` is a game file, not a disc image. A disc added to a set that
already has its playlist is not added to it. **Tools → Disc helpers** and the
ROM manager's **Combine multi-disc games** run the same pass over everything
at once — on NextUI its tagged disc folders too — and say what they added.

### Which disc a game starts on

A multi-disc game's options (**Start** in the library) have a **Disc** row:
*Disc 1 of 3*, or *Starts on disc 2 of 3*. **Left/Right** (or **A**) picks
the disc RetroArch starts it on. JukaHub writes RetroArch's own record for it,
`<game>.ldci` in the folder RetroArch keeps the game's saves in
(`savefile_directory`, sorted by content folder and core when RetroArch sorts
saves, else beside the `.m3u`): `{"image_index": 1, "image_path": "…"}`, the
index counting from 0 and the path the one the core reports for that disc.
RetroArch hands it to cores that take a first disc (PCSX-ReARMed, Beetle PSX
and others) and updates it itself whenever the disc changes, so the row then
shows the last disc played. Another emulator starts on disc 1; the row says
so. Each launch puts the record's path right for the launcher in use (a
firmware script is given the game as `Emus/<system>/../../Roms/…`).

In the game, the in-game menu (hold **Select + L2**) has a **Disc** row for a
multi-disc RetroArch game the library started: *Disc 1 of 3*. **Left/Right**
picks another disc, **A** puts it in (with the row on the current disc, the
next one): the game carries on and RetroArch is sent what its own hotkeys do —
eject, next or previous disc once per step, close — and shows its own message.
JukaHub tracks the disc from the one the game started on.

## ScummVM and DOS shelves

Some games are folders, not files. JukaHub lists each of them once, titled
after its folder (`romshelf.go`, `shelf_*.go`).

**ScummVM.** Put each adventure in its own folder in `Roms/SCUMMVM` (or
`Roms/ScummVM`). The ScummVM core starts a game from a small `.scummvm` file in
its folder that holds the game's ScummVM ID (`sky`, `tentacle`). When a folder
has none, JukaHub writes one for the games it recognises — from the game's
files (the free classics such as Beneath a Steel Sky and Flight of the Amazon
Queen, the LucasArts and Sierra adventures, Broken Sword, Simon the Sorcerer,
Discworld, Kyrandia, Myst), or, for the Sierra games and the early LucasArts
ones, from the folder's name ("King's Quest IV", "Space Quest 3", "Loom"): name
such folders after the game. It never overwrites a `.scummvm` file and never
writes outside a ScummVM folder of the library. Folders it cannot recognise
are named under **More** on any ScummVM game's page; add a `.scummvm` file with
the ID from scummvm.org/compatibility yourself. Shortcuts straight in the
ScummVM folder (CrossMix's importer, muOS, spruce) are listed as they are.

Controls (the **ScummVM point and click** layout, ScummVM 2.8 or later; older
cores keep their own mapping): D-pad or stick moves the pointer, **A** left
click, **B** right click, **X** Escape (skip a scene), **Y** skip a line, hold
**L1** for a slow exact pointer, **R1** F5 (save/load in most games), **Start**
the ScummVM menu, **Select** the on-screen keyboard.

**DOS.** Put a game in `Roms/DOS` as a `.zip`/`.dosz`, a disc image, a
program straight in the folder, or a folder. A folder is one game: JukaHub
starts its start script or main program and never an installer (SETUP,
INSTALL…). **More → DOS settings** on the game's page picks another start
program and sets, for that game only, the PC speed, graphics chip, memory,
mouse speed and what Select opens; they are applied for each start through
DOSBox Pure's core options and put back when RetroArch closes (start the core
once from RetroArch first so it has an options file). **Select** opens DOSBox
Pure's on-screen keyboard on every TrimUI, the Brick included (its menu is one
tab away). By default DOSBox Pure maps the buttons for the games it knows (the
**DOS: the game's own mapping** layout); for a point-and-click game choose **DOS mouse** and, once
per game, the preset "Mouse with Left Analog Stick" in DOSBox Pure's menu
(Select → Controller Mapper).

In Files, **Y → Add to DOS games** copies a game folder or a `.zip`/`.dosz`
into `Roms/DOS` (under a hidden name until the copy is whole, never over an
existing game) and puts it on the shelf.

The first time a ScummVM or DOS game starts in its RetroArch core, a short card
shows its controls; **A** starts the game, **B** goes back. It is not shown
again for that game.

## ROM manager: more for your games

**Settings → Games & emulators → ROM manager** (also in Tools) files loose
games, BIOS files and duplicates. Under its summary card, **More for your
games** has, one press each:

- **Move N Color games out of Game Boy** — only when the Game Boy shelf has
  Game Boy Color cartridges ([below](#game-boy-color-games-on-the-game-boy-shelf));
  it asks once, listing them.
- **Combine multi-disc games** — the disc pass above, now, with a toast that
  says what it added.
- **RomM server** — games, covers, saves and BIOS from your own RomM server
  ([ROMM_SERVER.md](ROMM_SERVER.md)); without an address and API key the row
  says to set it up first and the page opens on its settings.
- **Syncthing** — the Save Manager's Syncthing page, which keeps your saves
  the same on all your paired devices ([SAVE_MANAGER.md](SAVE_MANAGER.md));
  off or not installed, the row says to set it up first.
- **Get Homebrew** — the homebrew store.

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

## HTML5 games

The **HTML5** row includes three offline demos: **Neon Snake**, **Brick Bounce**
and **Star Dodger**. Their source is bundled with JukaHub under
`player/html5_demos/` and licensed under MIT. The first library scan copies
them into `data/HTML5/Demos/` on the handheld (or the JukaHub data folder on a
PC) without replacing changes you make there.

To add a game, copy its whole folder, including `index.html` and its assets, to
`Games/HTML5/<Game name>/` beside JukaHub or at the SD card root. A standalone
`Games/HTML5/<Game name>.html` also works. JukaHub scans game folders up to
three levels deep; it treats HTML files *inside* a folder with `index.html` as
that game's assets. Put `cover.png`, `cover.jpg` or `cover.jpeg` beside
`index.html` for artwork. Press **Y** in the library after copying files.

On the TrimUI Smart Pro, **Play** opens JukaHub's own full-screen game browser.
The three demos run offline with the D-pad or stick; press **A** to start and
**B** to return to the library. A touchscreen or mouse can use the on-screen
arrows, move the paddle or ship on the board, and tap **Start** below it. No
separate browser install or Wi-Fi connection is needed. The built-in game view
supports Canvas 2D drawing, JavaScript animation, local scripts, keys, pointer
input and local storage. WebGL, image and audio loading, and JavaScript modules
are not yet supported in this game view. Games that need those features may not
run on the stock handheld; JukaHub shows a launch error when it can detect one.
While a game is open it gets up to 60 frames a second and the same CPU
performance profile as an emulator launch; the normal profile comes back when
you press **B**.

On a PC, **Play** uses your system browser with a private local address. That
browser can handle games with more web features, relative assets and its own
storage.

HTML5 launches join **Continue** like other games. Their saves, if a game has
them, live in the browser. JukaHub's RetroArch save states, LAN play and cover
download do not apply to this row. On a PC, the browser is a separate app, so
JukaHub records the launch but cannot measure time spent in the browser tab.

## Completeness

The header carries a ring that fills as the library gains covers, with the
percentage inside it, and every system row has a thin meter showing that
system's share. The status line spells the same figure out
(`412 games in 9 systems · 330 covers (80%)`), so a card with a few gaps
is visible without opening a settings panel.

The numbers are counted from the files on the card, not from the download
attempts: a cover that exists is counted whatever put it there, including one
copied from a PC or a scraper. Deleting a cover lowers the figure again.
The library can also look up genre, developer, release year and player count
from libretro-database's separate DAT files. Descriptions and notes are yours to
write; manuals and screenshots are not fetched.

## Launching

Pressing **A** on a game (in the library, on Continue, or on a ROM in Files)
resolves the emulator in this order:

1. the game's own **Emulator** (Game options, or the one remembered from
   **Which emulator?** below), if its file still exists;
2. `data/game-launchers.json`, if it names a launcher for that console and the
   file still exists;
3. the Emulators catalog: every available row whose name matches the console's
   aliases exactly (`GBA`, `mGBA` for Game Boy Advance), then whose bracketed
   part or a whole word does (`RetroArch GBA`), followed by the console's
   [other emulators](#other-emulators-for-a-console) — the other scripts its
   `Emus` folder lists and, on the handheld, every installed RetroArch core
   that plays it. One choice starts at once; two or more open
   **Which emulator?**, listing each with its kind, what that kind means for
   the game (RetroArch: JukaHub's save slots, Continue and RetroAchievements;
   standalone: no JukaHub save slots or Continue) and its folder — the one
   [recommended for this hardware](#the-recommended-emulator) first, with a
   **Recommended** badge. The highlight starts on the one picked last, else the
   recommended one. How often it asks is Settings → Games & emulators →
   **Ask which emulator**:
   - *First time per game* (the default): **A** plays and remembers that
     emulator for the game, so its next start goes straight in;
     **Y · Just once** plays without remembering, so it asks again.
   - *Every launch*: **A** plays (and is highlighted next time); **Y · Always
     use** remembers it for that game.
   - *Never — use the best one*: no question; the game starts in the one
     picked last, else the recommended one.

   In both asking modes **X** uses it for every game of that console (written
   to `game-launchers.json`, step 2; not offered for a console split over
   three or more folders of its own, like Arcade's CPS1, CPS2 and MAME sets),
   **B** cancels, and when the buildbot has cores for the console that are not
   on the card, a last row **Get more cores for …** downloads them
   ([More cores](#more-cores)). Game options → **Emulator** changes the
   remembered one or sets a game back to asking (*Ask next time* / *Ask each
   time*); while the console has an entry in `game-launchers.json` (from **X**
   or written by hand), Left there removes it and its games ask again;
4. nothing — the panel toasts which console has no emulator instead of starting
   something random. The Xbox is the exception on a handheld the hakuX port is
   offered on (the Smart Pro; the Brick Pro, untested): with no Xbox launcher
   on the card, the start installs hakuX — the install **X** on the Emulators
   page's Xbox tile runs — and starts the game once it is in, if you are still
   where you pressed and no game or video took the screen (else the toast says
   the game is ready). On other models the toast says why they have no Xbox
   emulator. Once installed, hakuX's `Emus/XBOX/launch.sh` is the Xbox games'
   emulator like any `Emus` folder; the Xbox shelf lists `.iso` and `.xiso`
   (XISO) images only, the formats hakuX and xemu boot. On the handheld an
   Xbox game gets the device to itself: JukaHub closes while it plays and
   comes back, on the game, when it ends (the screen hand-off in
   [CONTROLS.md](CONTROLS.md#hakux-on-the-smart-pro)), with
   [Heavy-game boost](DEVICE_TUNING.md#heavy-game-boost) for its CPU, GPU and
   memory.

**PSP games and the picture (A133P models: Smart Pro, Brick, Hammer, Brick
Pro).** Their panel shows one ARGB framebuffer that can blend by each pixel's
alpha, and PPSSPP copies the PSP's own alpha (its stencil, mostly 0) into
it, which can show as a black screen while the game's sound plays. So
JukaHub's PPSSPP keeps one post-processing shader last in its chain, *JukaHub
opaque picture* (`PSP/shaders/jukahub-opaque.*` in its memstick, put back
before every game, after any shaders you chose), and for the first 45 seconds
of a PSP game JukaHub looks at what the panel shows. When the game's picture
never reaches it — transparent, still JukaHub's last picture, or black — the
log says what it saw (`picture check:` lines with the display layers and
PPSSPP's last lines), the game is started again with the device to itself
(the Xbox's screen hand-off: JukaHub closes while it plays, so its in-game
menu, sleep and hotkeys are not there; PPSSPP's own menu, **Menu**, has
**Exit**), and PSP games keep that way. A Select + Start hold that ends a PSP
game whose picture was never seen chooses it too. The PPSSPP card (**X** on
the PSP tile of the Emulators page) says so and has **Play PSP games beside
JukaHub again** and **Give PSP games the screen to themselves**.

Starts that already know their emulator never ask: a save state on the game
page uses the emulator the states were listed for (the game's remembered one,
else the console's, else the one picked last, else the recommended one) and
says so when that is not RetroArch; the resume after power-on uses the
launcher it recorded; a relaunch after a save sync uses the one it was
started with; the game switcher resumes in the emulator the game last ran in.
Continue goes through the same question as the library, with the highlight on
the emulator the game ran in, so continuing is one press of **A**.

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
in the form MainUI passes it — the emulator folder, then `../../Roms/...` —
because CrossMix's RetroArch launchers (`common_launcher.sh`) work out their
folders from that argument:

```sh
sh /mnt/SDCARD/Emus/GBA/launch.sh "/mnt/SDCARD/Emus/GBA/../../Roms/GBA/Pokemon Emerald (USA).gba"
```

It names the same file, so the stock scripts and PPSSPP read it as before. A
game on another card, or a launcher outside an `Emus/<system>` folder, gets the
plain path. The script runs under the interpreter its `#!` line names, as
MainUI runs it (a `#!/bin/bash` script under bash, a launcher that is a program
as itself); a plain `sh` script, one with no `#!` line and an interpreter the
device lacks keep `sh`. The script's output goes to `gamediag/launcher.log` in
JukaHub's data folder (emptied each launch); when a game closes within seconds,
the launch card names what the script could not find and shows its last lines.

The launch card opens when a game closes within eight seconds with an error
status, a finding or a log line that says loading failed, or after a signal
JukaHub did not send ("the system stopped it (signal 9, SIGKILL)" — on a
handheld, memory running out — with the memory that was free at the start);
on the handheld it also opens for any library game over within three seconds,
whatever its status. Stops JukaHub sends itself — the in-game menu's Quit,
quitting JukaHub, the low-battery save — never open it. JukaHub's log
(`errors.txt` beside it on the handheld) gets a line for every library launch,
with the exact command, its folder, the pid and the memory free, and one when
it ends, with how long it ran and its exit status or signal:

```text
library: starting "Manhunt 2" (PlayStation Portable): sh /mnt/SDCARD/Emus/PSP/launch.sh "/mnt/SDCARD/Emus/PSP/../../Roms/PSP/Manhunt 2 (USA).iso" in /mnt/SDCARD/Emus/PSP, pid 1812, 412 MB free
library: "Manhunt 2" ended after 1.4s: exit status 0
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

### Warm up games before launch

On a TrimUI (and any Linux), a game that stays highlighted in the library for
about a third of a second — or the game on Home's Continue card — has its
launch files asked into memory in the background: RetroArch, the core the
launch will use and its `.info`, and the ROM when it is 64 MB or smaller
(160 MB at most in all). The start that follows reads them from memory instead
of the card. It gives way to everything else (lowest CPU and disk priority),
stops when the highlight moves or a game starts, never runs while a game is
playing or at 15 % battery or less off the charger, needs twice the files'
size free in memory, and does not read the same file again within five
minutes. The log's launch lines say whether it had finished
(`launch warm-up for "…": finished, 3 files, 41 MB`). Switch it off with
**Settings → Games & emulators → Warm up games before launch**.

### Other emulators for a console

The firmware's own menu keeps a console's other emulators behind Select on the
game list: the rest of its `"launchlist"` (two entries on the stock OS, more on
CrossMix). JukaHub lists them in Game options → **Emulator**, and in
**Which emulator?** when it opens, right after the folder's launcher, named the
way the launchlist names them (`{"name": "…", "launch": "…"}`; a plain string
entry is named after its script): *N64 · Mupen64Plus Standalone*. Each must be
a `.sh` script inside the folder. A game in a folder that has an `Emus` folder
of the same name (`Roms/CPS2`) still starts in that folder's launcher without a
question.

On the handheld the list then holds every RetroArch core in RetroArch's cores
folder that plays the console and that none of those scripts already starts —
a core installed from the Emulators page, or one copied there by hand. Picked,
such a core starts directly: the RetroArch that owns the cores folder
(`ra64.trimui`) runs from its folder with `HOME` there and loads the core with
`-L`, the way the firmware's scripts start it, with the firmware's environment.
A core whose ELF header says it was built for another chip than that RetroArch
(a 32-bit ARM core beside the 64-bit RetroArch) is left out of the list, and a
core that cannot load for another reason (a library or glibc version the
firmware lacks) is named on the card JukaHub shows when the game stops at once.

These extra choices count for the question: a console with one launcher of
its own and one installed core asks which (by default the first time per
game, see [Launching](#launching)). They are never the recommended or
automatic one while the console has a launcher of its own, so the highlight,
and a start with **Ask which emulator** set to *Never*, stay on the console's
own emulator. A console that has no launcher at all (games in `Roms/N64`, no
`Emus/N64`, a Mupen64Plus-Next core installed) starts in its core, and asks
when it has two.

A core started directly gets none of the per-core settings a firmware ships
with its own cores. For the consoles this chip only just runs, a core of
Mupen64Plus-Next, Flycast or PPSSPP that has no options file yet (no
`config/<Core>/<Core>.opt`, no options for that game or folder, nothing for it
in `retroarch-core-options.cfg`) starts with the handheld's speed settings —
N64 at 320×240, Dreamcast at 640×480 with threaded rendering, PSP at its native
480×272 — written as `config/<Core>/<Core>.opt`. RetroArch reads it as one it
wrote itself; changing a core option in RetroArch's own menu replaces it. With
RetroArch's global core options switched on, nothing is written.

### More cores

On the handheld, **Get more cores** downloads, for every console that has
games on the card, the RetroArch cores worth having that are not installed
yet: for a console in the [ranking](#the-recommended-emulator), its cores in
rank order; for the others a short list of the cores the TrimUI firmwares ship
for them (Master System and Game Gear: Genesis Plus GX, PicoDrive, Gearsystem,
SMS Plus; PC Engine: Beetle PCE Fast, Beetle PCE; Arcade: FBNeo, MAME 2003-Plus;
Neo Geo Pocket: Beetle NeoPop, RACE; Atari 2600: Stella 2014, Stella; Lynx:
Handy, Beetle Lynx; and so on). It is in Settings → Games & emulators, a tile
on the Emulators page, and, for one console, the last row of
**Which emulator?** and **X** on Game options → **Emulator**.

A core is downloaded only when the buildbot's aarch64 index lists it, it is not
on the card, and that exact build was not refused here before. Each goes
through the Emulators page's own install: the CRC32 the index states, the size
limit, the check that this firmware can load it (a build that needs a newer
glibc is refused before anything is written), and the free-space floor of a
core update (256 MB plus room for the core). Cores that draw with the GPU —
Mupen64Plus-Next and ParaLLEl N64, Flycast, PPSSPP, YabaSanshiro — are
downloaded this way only for a console with no emulator at all: the
buildbot's generic builds of them may load and show nothing under TrimUI's
OpenGL ES RetroArch. For a console that plays already they stay one press each
on the Emulators page, whose row says *draws with the GPU: this build may not
run here*, and the summary names them. One toast says what was installed; the
next start of a game of that console asks which emulator to use.

With **Emulator updates** on *Install automatically*, the daily pass does the
same by itself, once per console, at its usual quiet moment (never over a
game). The consoles it has done are recorded in `emu-morecores.json` beside the
patch state, so a core you delete is not downloaded again, and a console whose
first games arrive later gets its cores at a later pass. *Notify me* and *Off*
download nothing.

### The recommended emulator

The TrimUI firmwares ship more than one emulator for the
heavier consoles, and which one plays best on this chip (Allwinner A133P,
PowerVR GE8300, 1 GB) is mostly word of mouth. JukaHub keeps that knowledge as
a ranked list per console (`player/emubest.go`), one for the handheld and one
for a Windows PC, and uses it in three places — never against a choice you made:

- **Which emulator?** lists the recommended installed emulator first, with a
  **Recommended** badge. The highlight still starts on the one you picked last.
- When a game has no emulator of its own, its console has none in
  `game-launchers.json` and nothing was picked last, the emulator a save state,
  the diagnosis or Reset game assume is the recommended one rather than the
  catalog's first match. Every pin still wins.
- On Windows, the Emulators page marks the install tile of a better emulator for
  a console you have games for but only a weaker emulator installed —
  *Better emulator available: DuckStation plays PlayStation better than the
  PCSX-ReARMed (RetroArch) you use now* — and lists it right after the
  installed ones. Nothing installs until you press Install. The handheld shows
  no such tile: its firmware's launch scripts choose the core a game starts in
  by default, and an installed core is
  [one more choice](#other-emulators-for-a-console), not the default.

JukaHub recognises which emulator an `Emus` folder starts from its launcher's
name (`drastic.sh`, `ppsspp_1.17.1_gl.sh`), the one core a RetroArch launch
script names, the folder's name, and the emulator a script runs. When it cannot
tell, it only puts a recognised emulator ahead of that folder if it is the
console's very best.

| Console | Handheld, best first | Windows, best first |
|---------|---------------------|---------------------|
| Nintendo DS | DraStic · melonDS DS core · melonDS core · DeSmuME core | melonDS · melonDS DS core · DeSmuME core |
| PSP | PPSSPP · PPSSPP core | PPSSPP · PPSSPP core |
| Nintendo 64 | Mupen64Plus · Mupen64Plus-Next core · ParaLLEl N64 core | Mupen64Plus-Next core · ParaLLEl N64 core |
| Dreamcast | Flycast · Flycast core | Flycast core |
| Saturn | YabaSanshiro · YabaSanshiro core · Yabause core | Beetle Saturn core · Kronos core · YabaSanshiro core |
| PlayStation | PCSX-ReARMed core · SwanStation core · DuckStation core · Beetle PSX core | DuckStation · SwanStation core · Beetle PSX HW core · PCSX-ReARMed core |
| Game Boy Advance | mGBA core · mGBA · gpSP core · VBA Next core | mGBA · mGBA core |
| SNES | Snes9x core · Snes9x 2005 core · bsnes core | Snes9x core · Snes9x · bsnes |
| NES | FCEUmm core · Nestopia core · QuickNES core | Mesen core · FCEUmm core · Nestopia core · FCEUX |
| Game Boy, Game Boy Color | Gambatte core · SameBoy core · mGBA core · Gearboy core | the same |
| Mega Drive | Genesis Plus GX core · PicoDrive core | the same |
| GameCube, Wii | Dolphin core · Dolphin (Smart Pro S; see below) | Dolphin |
| PS2 | — | PCSX2 |

Where the handheld ranking comes from: what the TrimUI firmwares ship,
emulator release notes and community measurements on the A133P (standalone
DraStic, PPSSPP, Mupen64Plus, Flycast, YabaSanshiro and mGBA; `mgba_libretro`
for GBA; PCSX-ReARMed for PlayStation; standalone YabaSanshiro for Saturn;
standalone Mupen64Plus with GLideN64 at full speed on the Smart Pro and
Brick). Judgement calls rather than
measurements: standalone Flycast ahead of its core, the mGBA core ahead of
standalone mGBA, and the order of the light 8- and 16-bit cores, which all run
at full speed on this chip.

### GameCube and Wii on the handheld

The Smart Pro S plays GameCube games in RetroArch's Dolphin core: the lighter
ones at full speed or close, the heavy 3D ones well below it, and few Wii games
reach full speed. The A133P models (Smart Pro, Brick, Brick Pro, Hammer) play
them too since 2026-11-21, most games well below full speed (GameCube speed
is Fastest there); **Settings → Games & emulators → GameCube & Wii** (`gamecube`
in [CONFIGURATION.md](CONFIGURATION.md)) turns them off. Put games in `Roms/GC`
(or `GAMECUBE`, `NGC`) and `Roms/WII` as `.iso`, `.gcm`, `.rvz`, `.gcz`,
`.ciso`, `.wia`, `.wbfs` (Wii) or `.m3u` for a two-disc game; Dolphin cannot
open a `.zip` or `.7z`, so those are not listed. RVZ, Dolphin's own
compressed format (Dolphin on a PC converts to it), keeps a game much smaller
than its ISO.

- **The folders are made for you.** TrimUI's firmware has no GameCube or Wii
  folder, so JukaHub makes `Roms/GC` and `Roms/WII` on the card: at start-up,
  after Dolphin is set up, when **GameCube & Wii** is turned on, and when you
  press the console's tile on the Emulators page. A folder the card already
  has in another spelling (`GAMECUBE`, `NGC`, `Wii`) is used instead. Delete
  one you do not want and it stays deleted (`dolphin.json` remembers) until
  you press its tile or turn **GameCube & Wii** on again. Nothing is made on a
  NextUI card, whose menu lists only its own tagged folders.
- **The first start sets Dolphin up.** With no emulator for the console yet, A
  on a game installs a Dolphin core that loads here and Dolphin's system files
  (`dolphin-emu/Sys` in RetroArch's system folder, from the buildbot's
  `Dolphin.zip`, adding only what is missing), then starts the game. The core
  is JukaHub's own build for TrimUI (the `cores-aarch64` release, built with
  GCC 13 and libstdc++ linked in, so it runs on the firmware's own C library);
  while that is not published, the buildbot's nightly, which needs a newer
  libstdc++ than any TrimUI firmware has and so runs on JukaHub's private
  glibc (**RetroArch's C library** on Automatic), with JukaHub's stand-ins
  for libusb and libudev beside it (a desktop build links them; JukaHub
  writes them itself, no download, before the first start). Both go through
  the checks every core install has
  ([COMPATIBILITY.md](COMPATIBILITY.md#fetching-newer-builds)). When neither
  gives a core, the message says why for each, JukaHub's own build first.
- **The Emulators page.** It always lists a **GameCube** and a **Wii** tile,
  each naming Dolphin, the folder its games go in and how many are there.
  **A** on either opens that console's games, or sets Dolphin up while it is
  not installed; **X** there (or on the Dolphin core's own tile) runs the same
  setup, system files included, and later looks for a newer build that loads
  here. A launcher folder of your own in `Emus` (a standalone Dolphin, say)
  keeps its tile instead, and the games start in it. The daily core updates
  and **Update all** leave Dolphin alone (it is among the largest cores and
  the buildbot has a new build nearly every day), JukaHub's own build is only
  ever replaced by a newer one of its own, and **Get more cores** does not
  fetch it.
- **No freeze ten seconds in.** A Dolphin core built before 23 October 2026
  froze an aarch64 RetroArch about ten seconds into a game with its dual-core
  mode on (libretro/dolphin#462). A start in such a core fetches the newest one
  first (offline it starts with a warning).
- **Smoother first play.** The core's first start here gets shaders compiled in
  the background, skipping an object for the frame or two its shader takes,
  instead of a pause at every new effect. Change it in RetroArch's core options
  (Quick Menu → Core Options → Graphics); JukaHub never writes over options the
  core already has.
- **GameCube speed.** **Settings → Games & emulators → GameCube speed** sets
  how much of Dolphin's accuracy each launch trades for frames (`gamecubeSpeed`
  in [CONFIGURATION.md](CONFIGURATION.md)), for that launch only. **Fast**
  changes no game's picture: dual core, shaders in the background, native
  resolution, no duplicate frames, EFB copies written back only when read, no
  run-ahead, preemptive frames or rewind. **Fastest** also skips the CPU's
  reads of the picture (screen-reading effects such as Sunshine's goop or a
  lens flare go), runs the GameCube's CPU at half its clock (busy scenes slow
  down inside the game) and turns vsync off (the picture may tear).
  **Automatic** is Fastest on the A133P models and Fast on the Smart Pro S;
  **Dolphin's own** changes nothing. Options you saved for a game in RetroArch
  stay, and so does a value you set in Core Options, except dual core,
  background shaders, duplicate frames and deferred EFB copies. On the A133P
  even Fastest leaves the heavy 3D games far from full speed; the lighter games
  gain the most. Disc images load fastest as ISO, or as RVZ compressed with
  Zstandard: LZMA and bzip2 take these chips long to unpack.
- **The video driver.** When a start stops because RetroArch's video driver
  could not draw for Dolphin, the card says so and Dolphin's own override
  (`config/dolphin-emu/dolphin-emu.cfg`) switches between OpenGL ES and Vulkan
  for the next start; Vulkan only where the firmware has a Vulkan driver.
- **The whole handheld.** A GameCube or Wii game gets what **Heavy-game
  boost** gives an Xbox game: every core, the GPU and memory at full speed,
  JukaHub's memory handed back, "Hey Juka" closed while it plays.

### PlayStation 2 on the handheld

Every TrimUI model plays PS2 games in RetroArch's ARMSX2 core, a PCSX2 fork
with ARM64 recompilers. Expect the light games (2D, menu-heavy, simple 3D) at
or near full speed on the Smart Pro, Brick and Hammer, and most 3D games well
below it; the Smart Pro S does a little better. **Settings → Games & emulators
→ PlayStation 2** turns it off (`ps2` in [CONFIGURATION.md](CONFIGURATION.md)).
Put games in `Roms/PS2` as `.iso`, `.chd`, `.cso`, `.zso`, `.cue` (with its
`.bin` tracks), `.m3u` for a multi-disc game, or `.elf` homebrew; CHD keeps a
game much smaller than its ISO. A stray `.bin` is not listed as a game.

- **The BIOS is yours to add.** PS2 games need a BIOS dumped from your own
  console (a 4 MB file such as `SCPH-70012.bin`). Put it in `BIOS/PS2` (or
  `Bios/PS2`, or the BIOS folder itself, or `Roms/PS2`): the first start
  copies it to `pcsx2/bios` in RetroArch's system folder, where the core reads
  it, after checking it the way PCSX2 does (4–8 MB, with the PS2 ROM
  directory). With no BIOS anywhere the start says where to put one and
  downloads nothing.
- **The first start sets ARMSX2 up.** With no emulator for PS2 yet, A on a
  game downloads ARMSX2's own Linux arm64 libretro core from its GitHub
  releases (the newest stable release that has one, else the newest nightly;
  the archive must match the SHA-256 GitHub states) and installs it like any
  core: the load check, and RetroArch on JukaHub's newer glibc since ARMSX2
  needs one. The core cannot boot without ARMSX2's `resources` folder (the
  per-game fixes, the renderer's shaders) from the same build, so the same
  release's handheld archive gives `pcsx2/resources` in RetroArch's system
  folder (a resources folder you put there yourself is left alone). Then the
  game starts. The libretro buildbot's ARMSX2 core is not used: it needs
  libraries the TrimUI firmware does not have; a start in such a core installs
  ARMSX2's own build first.
- **Updates.** **Settings → Games & emulators → PS2 emulator**, or A on the PS2
  tile of the Emulators page, installs the newest release over the one there
  (the old core kept as a backup, the first game watched like any core
  update). JukaHub's automatic core updates leave ARMSX2 alone.
- **The renderer.** ARMSX2 draws with Vulkan by default, which the PowerVR
  GE8300 in the Smart Pro, Brick and Hammer has no driver for, so the core's
  first start here gets OpenGL ES and the BIOS intro skipped (JukaHub never
  writes over options the core already has). When a start stops because the
  renderer could not draw, the card says so and the core's options
  (`config/ARMSX2/ARMSX2.opt`) move to the next renderer for the next start:
  OpenGL ES, then the software renderer (slow, but it draws anywhere). Change
  it in RetroArch (Quick Menu → Core Options → GS Renderer).
- **Speed.** Native resolution and multi-threaded VU1 are the core's defaults
  and stay. Settings → Games & emulators → **PS2 speed** (the PS2's own
  [Emulator speed](DEVICE_TUNING.md#emulator-speed) level) is **Fastest** on
  the A133P models by default: the emulated EE at 75% (`armsx2_ee_cycle_rate`)
  with mild cycle skipping (`armsx2_ee_cycle_skip`), for that launch only — a
  game with cycles to spare plays as before, a busy scene slows inside the game
  instead of the whole game crawling, and a game that breaks plays at **Fast**
  (the PS2's own timing) or **Emulators' own**. A value you set in RetroArch's
  Core Options stays. A PS2 game gets what **Heavy-game boost** gives an Xbox
  game. The Smart Pro's two sticks are the DualShock 2's; the Brick and Hammer
  have none, so a game that needs a stick is hard to play there.

### Per-game launch options

**Start** (or **O** on a keyboard) opens the options sheet for the highlighted
game. The first rows are its name and the saved library view settings, the
launch rows apply to that launch only, and the last rows manage its files
(*Renaming and deleting a game* below; **Remove cover** is under *Removing a
cover* above):

- **Rename title** — the name shown in the library. It is stored beside the
  game, not written into the ROM filename, so the file stays exactly as it is.
  Leave it empty to go back to the name derived from the file.
- **Rename file** — renames the ROM on the card, keeping its extension (see
  below). The row shows the file's current name.
- **Visibility** — hide this game, or make it visible again. Hidden games are
  left out of the console lists, Continue, search and tag filters until
  **Show hidden** is on.
- **Library sort** — Title, Last played, Recently added, Playtime, Rating or Year. The order is
  saved and applies to every console. Games with no value for the chosen sort
  (never played, unrated, no year) come last.
- **Library view** — List, 3D shelf, Cover column, Cover Flow or Cube. The
  shelf uses left/right to browse games and up to return to the console
  buttons; Cover column keeps the list's up/down navigation. The choice is
  saved for the next visit (see [Cover Flow and Cube](#cover-flow-and-cube)).
- **Console look** — Covers, Pixel names or Pixel cards for the console
  buttons (*Console look* above). Saved for every visit.
- **Shader speed** — after playing with a look (Scanlines, LCD grid, CRT,
  Pixel transparency — see [Looks](LOOKS.md)),
  says whether the game held full speed with it: **Full speed** at 97 % of its
  50 or 60 FPS or better, else **Below full speed**, with the frames per second
  measured. On the handheld it counts the frames the game really put on screen
  (the framebuffer's page flips) over about 16 seconds of play, leaving out
  loading, pauses, JukaHub's in-game menu and fast-forward; where the screen
  cannot be counted (and on a PC) it reads RetroArch's own frame timing from
  its log after the game, which needs about a minute of play and threaded
  video off. A game that ends with a new verdict says it in a toast once
  JukaHub is back. **Unavailable** says why instead of guessing.
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
- **Buttons** and **Turbo** — for a RetroArch game: swap A and B, X and Y or
  both, and a button that fires by itself while held (A or B). *Default · …*
  follows the console's choice in **Settings → Controls → Game buttons for**
  (see [Controls](CONTROLS.md#game-buttons-and-turbo-retroarch-games)); A steps
  through the choices and goes round. They are written for that start only and
  stored in `Apps/JukaHub/data/game-controls.json`. When the last start could
  not apply them (your own RetroArch remap file was there, or an older
  RetroArch lacks the pad's buttons for turbo), the row's line says so.
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

Because the key is the path, moving or renaming a ROM outside JukaHub returns
it to the defaults rather than inheriting another game's settings; **Rename
file** takes them along. A broken file reads as "no
overrides" — the library still opens. The clock is applied through the same
profile code as the global setting, so a firmware with a read-only
`scaling_max_freq` logs one line and runs the game at its own clock instead.

Display titles, hidden flags, sort and cover layout are a library view, not launch
options. They live in `Apps/JukaHub/data/game-library.json`, keyed by a hash of
the ROM's content (its size plus the first and last 256 KiB), so moving or
renaming the file keeps its name and hidden state:

```json
{
  "version": 1,
  "sort": "rating",
  "layout": "covers_horizontal",
  "show_hidden": false,
  "games": {
    "9c1f...": { "display_title": "My Zelda", "hidden": true }
  },
  "console_look": "pixel_cards"
}
```

`console_look` is the consoles grid's look (*Console look* above); it is not
per game.

The launch options above remain keyed by path because they belong to one
launcher's invocation; the view state follows the cartridge instead.

### Renaming and deleting a game

**Game options → Rename file** opens the on-screen keyboard on the ROM's name
without its extension (typing the extension too is fine; it is kept either
way). **Rename** renames the file and everything named after it, so the game
keeps its saves, states, settings and cover:

- beside the ROM: files of the same name with one extension (`Game.bin`,
  `Game.srm`, CloneCD's `.img`/`.sub`), its save states and their pictures,
  and files named after the whole file (`Game.gba.sav`) — never a file that is
  a library game itself or a part another game's sheet lists. A `.cue`, `.gdi`
  or `.m3u` among them has its lines that name a renamed file rewritten;
- its pictures: `Imgs/<name>` and `.media/<name>` beside it, MinUI's
  `.res/<file>.png`, JukaHub's cover, picture from the game and **Remove
  cover** marker in `data/game-art`, and its video snap in `data/game-snaps`;
- RetroArch's battery saves and save states (at the top of its saves and states
  folders and in the game's core and content sub-folders), its per-game core
  override, core options and input remap; on NextUI the saves and states it
  names after the whole file, in the game's own console folders
  (`Saves/<TAG>/`, `.userdata/…/<TAG>-<core>/`); DraStic's for a DS game and
  Flycast's for a Dreamcast game;
- JukaHub's records: game options, cheats, recents, collections and
  favourites, Home's console rows, the RomM link, the quick-resume marker,
  **Name for covers** and — when the title the file gives changes ("Kart" →
  "Mario Kart") — its playtime, game page (tags, rating, notes) and session
  history. A title another game of the console shares ("Kart (USA)" and
  "Kart (Europe)") keeps those where they are.

Nothing is overwritten: a name already taken stops the rename before any file
moves (a cover already there under the new name is kept instead), and so does
a name another library game already has on any console — RetroArch keeps saves
by name, so the two games would share one. A sheet that would still name an
old file afterwards (a line written in a way the rewrite does not know) puts
everything back too. While the library is scanning, wait for it. The files
beside the ROM go first, and if one of them cannot be renamed every one is put
back; a file elsewhere that cannot be renamed keeps its old name and the toast
counts it. When another game has the same name — `USA/Game.sfc` and
`Hacks/Game.sfc` share one RetroArch save — the shared files are copied rather
than moved, and a save at the top of RetroArch's shared folders is left alone.
Names a FAT card cannot hold (`\ / : * ? " < > |`, a leading dot, device names
such as `CON`) are refused. Arcade sets keep their names (the emulator finds the
set by it: use **Rename title**), an extracted Xbox 360 game is named after its
folder (rename that in Files), and Android, browser and PortMaster games are
not files the library renames. Saves a standalone emulator keeps under another
name (PPSSPP's by game ID) need nothing.

**Game options → Delete game…** removes a game from the card. It takes the
ROM, the files its sheet lists when they are inside its folder and no other
game in that folder lists them (a `.cue`'s tracks, an `.m3u`'s discs and
theirs), the files beside it of the same name that are disc parts, patches,
manuals or pictures (`.bin`, `.img`, `.sub`, `.ips`, `.txt`, `.png`…), and
JukaHub's pictures of it; a folder named after the game that is left empty
goes too. The ROM goes first: if it cannot, nothing else is touched. Saves
and save states stay — they are your progress, and **Reset game** is where
they go. The question opens on **Cancel**: **Move to the trash** puts
everything in `data/reset-trash/<date>-deleted/`, the discs' sub-folders kept,
with a `restore.txt` naming where each file came from, written as each file
lands (the three newest deleted games are kept); **Delete for good** frees the
space at once. The game then leaves
the library, Continue, Home's console rows and its game options and cheats; a
collection keeps its entry under the title it was added with.

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
failing. **X** in **Which emulator?** writes the same entry for you (a core
path is fine: `".../RetroArch/.retroarch/cores/parallel_n64_libretro.so"`),
keeping the file's other entries and their order.

### Game Boy Color games on the Game Boy shelf

Homebrew and indie Game Boy Color games are often published as `.gb` files,
so they land in `Roms/GB`. The cartridge header says what each one is: the
Nintendo logo at `0x104` and the CGB flag at `0x143` — `0x80` (uses the
Color's features, also runs on a Game Boy) or `0xC0` (Game Boy Color only).
JukaHub reads those 336 bytes (of a `.gb`, a `.gbc`, or the one cartridge in a
`.zip`), cached by file size and time, and:

- **starts them in colour** — such a game on the Game Boy shelf is offered the
  Game Boy Color shelf's emulators (the same list, the same **Which
  emulator?**). A card with no Game Boy Color emulator keeps the Game Boy one.
  A game's own **Emulator**, a `game-launchers.json` pin for Game Boy and a
  NextUI pak (whose Gambatte reads the flag itself) are left as they are;
- **says so once** — the first library visit that finds them shows one toast
  pointing at the ROM manager (`gbcSortNoticed` in the user config keeps it
  from repeating);
- **moves them on request** — the ROM manager's **Move N Color games out of
  Game Boy** files them in the card's Game Boy Color folder (the one the
  library already reads, else `GBC`, or `Game Boy Color (GBC)` on NextUI),
  keeping any sub-folder. Each game takes along its saves and states beside
  it, its `Imgs`/`.media`/`.res` pictures, JukaHub's cover, gameplay picture,
  Remove-cover marker and video snap, RetroArch saves and states sorted by
  content folder (`saves/GB/…` → `saves/GBC/…`, also under a core's folder),
  NextUI's `Saves/GB` and `GB-<core>` states, its options, cheats, recents,
  collections, favourites, RomM link and resume marker, and its play time,
  game page and history (kept by console and title; left in place when a Game
  Boy game that stays has the same title). Nothing is overwritten: a game
  whose name is already in the Game Boy Color folder stays where it is, and a
  cover already there is kept. RetroArch's per-game core settings and the
  firmware's launchers are never touched; every move is logged.

## More on a game's page

A game's page (A on a game) has a **More** button (keyboard: **M**). It lists
what else belongs to that one title: whether it is ready to start, its manual
and guides, its control layout (docs/CONTROLS.md, "Control layouts"), its DOS
settings, its saves on another device and Game Lab. Chips under the rating
show the short answers — *Ready*, *Manual*, *Controls: Arcade 6-button*,
*Lab: …*, *Saves: newer on Juka's PC* (`gamepage_more.go`).

### Game Ready

Before a game from the library starts, JukaHub checks the things that most
often stop a game (`gameready.go`): the game file itself (empty, damaged, an
empty zip, a `.cue`/`.gdi`/`.m3u` whose files are missing), the emulator and
the RetroArch core its launch script names, and the BIOS that core needs — in
the folder this device's RetroArch really reads (`system_directory` in its
`retroarch.cfg`). On a PC it also says when no controller is connected, and
with Local play it notices a player whose controller is gone.

Only a certain problem stops the start. Then a card says "*Game* isn't ready
yet", lists each problem with the missing file names and the full folder to
copy them into, and offers:

- **A** — the fix: Open BIOS Doctor, Get cores (handheld), Open Emulators (the
  console's tile is highlighted) or Remove missing players (Local play).
  Closing BIOS Doctor or the Emulators page brings you back to the library.
- **X** — Play anyway (not when there is no emulator at all).
- **Y** — Check again; if everything is there now, the game starts.
- **B** — Back.

Things that only *may* matter (a BIOS most games run without, a core that may
not load, no controller on a PC) are listed under "Good to know" and never stop
a game. A start nobody pressed for — the resume at power-on — is never
stopped. The page's chip shows the answer (Ready, Needs BIOS, Missing disc
file, Core missing, No emulator, Needs a PC, BIOS may be needed) and
**More → Ready check** shows the full report, with a tick for everything that
was fine. Pressing Play anyway twice for a game turns the card off for it;
More → Ready check → **X** turns it back on. The choice is kept in
`data/game-ready.json`.

### Game manuals

**More → Manual & guide** lists the game's controls, then every manual and
guide JukaHub found for it, then rows to add your own; a *Manual* chip on the
page means one was found (`gamemanual*.go`). Names are matched loosely, so
"(USA)", case, punctuation and "The" do not matter:

| Where | Example |
|---|---|
| Next to the game | `Roms/SFC/Super Metroid (USA).pdf`, `… (Manual).pdf`, `…-manual.pdf`, `….txt` / `.md` (a guide) |
| The console's manuals folder | `Roms/SFC/manuals/Super Metroid-manual.pdf` (also `manual/`, `docs/`, `guides/`, `media/manuals/`) |
| A folder game's own folder | `Roms/SCUMMVM/Monkey Island/Manual.pdf`, `readme.txt` |
| Batocera / RetroBat | the `<manual>` entry in the console's `gamelist.xml` |
| ES-DE media | `downloaded_media/snes/manuals/Super Metroid (USA).pdf` next to the Roms folder (PC: also in your user folder) |
| LaunchBox | `LaunchBox/Manuals/Super Nintendo Entertainment System/Super Metroid-01.pdf` |
| A Manuals folder | `Manuals/Super Metroid.pdf` or `Manuals/SNES/…` next to the Roms folder |
| Scanned pages | a folder of pictures named like the game in a manuals folder, or `Super Metroid (Manual)/` next to it |

PDF and EPUB open in the Reader, TXT and Markdown in the text viewer, scanned
pages in the picture viewer and links in the Browser; Back returns to the
game's page. **Add a manual or guide** opens Files on the game's folder, where
**Y → Use as this game's manual** copies a file into
`data/game-manuals/<console>/<game>/`; **Add a guide link** keeps a web page
for the game (in `data/game-manuals.json`). X twice removes what you added.

In a game on the handheld, hold **Select + L2**: the in-game menu's **Manual**
shows the controls and the text guides (up to 64 KB each); PDFs and scans are
listed there to open from the game's page afterwards.

### Game Lab

**More → Game Lab** compares two setups of a game (`gamelab*.go`). A setup is
one of the game's emulators (firmware launcher, RetroArch core or standalone
emulator) plus a CPU profile (Auto, Battery saver, Balanced, Performance) and
an optional clock limit. A starts as what the game uses now, B as the next
emulator, or the same one at another speed.

**Run A** / **Run B** starts the game with that setup for that one launch —
nothing is saved to the game. Play a minute or two, then quit. JukaHub
measures:

- **Speed** — frames shown per second and the share of full speed. On a TrimUI
  the framebuffer's page flips are counted while RetroArch reports PLAYING
  (pauses, menus, loading and fast-forward left out, as the shader speed check
  does); on a PC, RetroArch's own "Average monitor Hz". Standalone emulators:
  not measured.
- **Sound** — the underrun and recovery lines RetroArch or ALSA logged, per
  minute. "None logged" means none were reported (most builds recover
  silently); "Sound lost" when RetroArch gave up on audio.
- **Battery** — the median draw in watts (or % per hour), sampled every 5 s;
  not measured on the charger or on a PC.
- **Played** — runs under 45 s are kept but marked too short.

A verdict line sums it up ("B is smoother and uses 12% less power"). **Use A**
/ **Use B** makes that the game's emulator and CPU setting (Game options shows
them too), and the page shows *Lab: …* while they still match. Results:
`data/game-lab/labs.json`.

**Sharing.** **Share** writes `data/game-lab/export/<system>-<title>.json`.
Profiles put in `data/game-lab/import/` that match the game (same system, and
the same CRC32 or title) appear under **Shared setups**; A loads one as setup B
when its emulator is installed here — it never starts the game by itself.
Files over 64 KB, of another schema or a newer version, or with out-of-range
values are ignored. Format (schemaVersion 1):

```json
{
  "schema": "jukahub.gamelab.profile",
  "schemaVersion": 1,
  "device": {"model": "trimui-smart-pro", "firmware": "CrossMix", "os": "linux/arm64"},
  "game": {"system": "GBA", "title": "Golden Sun (USA)", "file": "Golden Sun (USA).gba", "size": 8388608, "crc32": "a2e0f1c9"},
  "setup": {"emulator": "mGBA", "kind": "core", "core": "mgba", "coreVersion": "", "launcher": "cores/mgba_libretro.so",
            "cpuProfile": "balanced", "maxFreqKHz": 1600000},
  "results": {"fps": 59.7, "targetFps": 60, "fpsMethod": "frames", "slowShare": 0.02, "audioChecked": true,
              "glitchesPerMinute": 0, "soundLost": false, "watts": 2.9, "batteryPctPerHour": 0,
              "playedSeconds": 124, "tooShort": false}
}
```

`kind`: core, retroarch or standalone. `cpuProfile`: auto, battery, balanced
or performance. `fpsMethod`: frames or log. Optional fields may be absent; zero
means not measured.

### Save handoff (desktop ↔ handheld)

**More → Saves on *device*** compares this game's saves with a paired
JukaHub — a Windows PC and a TrimUI, or two handhelds (Settings → Network →
Paired devices; both need **Nearby devices** on and the same Wi-Fi) — and moves
them with one press (`savehandoff*.go`, over Save sync's paired connection).

- **What is compared:** the game's battery saves (`<rom>.srm`, `.sav`, `.rtc`…)
  and RetroArch save states (`<rom>.state`, `.state1`…, `.state.auto` and
  their screenshots) in the folders Save Manager finds, including RetroArch's
  per-core and per-content folders (never another console's core folder);
  NextUI saves and DraStic states too. PSP SAVEDATA is not yet. They are
  matched by the ROM's file name, so the ROM may live in a different folder on
  each device but should have the same name.
- **Which is newer:** content first. Identical files are "the same"; after any
  sync or handoff between the two devices, JukaHub knows which side changed
  since. Only for files the two devices never synced do the file times decide,
  and the screen then says **probably newer** (a handheld's clock can be wrong
  until it is online).
- **One press:** **Send to *device*** makes the other device's copy this one;
  **Get from *device*** the reverse. Files only one device has are copied
  either way. The receiving device takes a Save Manager snapshot first.
- **Both changed:** the two versions are shown side by side with **Keep this
  one** under each. The other is kept beside it as `<name> (from <device>).<ext>`
  on both devices — nothing is deleted. Two names that differ only in upper
  and lower case are left alone and the screen asks you to rename one.
- **Chip and hint:** the page shows "Saves: newer on *device*", "Saves: newer
  here", "Saves: changed on both" or "Saves in sync" for devices it can reach.
  After you play for at least a minute, a quiet hint says when this device's
  saves are newer than the other's.
- **Away or busy:** a device that is not on the Wi-Fi, has Nearby devices off
  or is running a game says so; Y checks again.

## On NextUI

On a NextUI card (one with `.system/tg5040` or `.system/tg5050`) JukaHub uses
NextUI's layout in place. Nothing is moved, and nothing in `.system`, `Roms`,
`Saves` or `.userdata` is written.

- **Folders.** `Roms/Display Name (TAG)` folders are read by their TAG (GB, GBC,
  GBA, MGBA, FC, SFC, SUPA, MD, PS, PCE, FBN, …). The TAG is found the way
  MinUI finds it: from the last `(` to the `)` after it, so `Game Boy (GB)
  Hacks` is GB. Folders of the same console (GBA and MGBA) are grouped under
  one console, as NextUI shows them. A TAG JukaHub does not know is still listed,
  under the folder's own name. Every file NextUI would list in such a folder is
  a game (pictures, notes, saves and `map.txt` excepted). These `Name (TAG)`
  folders and `map.txt` are only read on a NextUI card; on CrossMix, stock or
  Windows the library keeps reading the plain folder names it always did.
- **map.txt.** Names from `map.txt` are shown unless you gave the game a title in
  JukaHub, and entries it hides (a name starting with `.`) stay hidden.
  JukaHub only reads the file.
- **Launch.** A game in a `Name (TAG)` folder starts through
  `Emus/<platform>/<TAG>.pak` if you added one, else NextUI's own `<TAG>.pak`,
  with the variables NextUI's launcher exports (`PLATFORM`, `SDCARD_PATH`,
  `BIOS_PATH`, `SAVES_PATH`, `CHEATS_PATH`, `USERDATA_PATH`, `LOGS_PATH`, …) and
  NextUI's `bin` and `lib` first on the paths. `DEVICE` is `brick` on the Brick
  (and Hammer), `brickpro` on the Brick Pro and `smartpro` on the Smart Pro, as
  NextUI sets it. So BIOS stay in `Bios/<TAG>`, saves in `Saves/<TAG>`, cheats in
  `Cheats/<TAG>` and save states in `.userdata/shared/<TAG>-<core>`, shared with
  NextUI. NextUI's own slot-9 auto-resume is left alone (a stale
  `/tmp/resume_slot.txt` is cleared before a launch). A game whose pak is
  missing says so ("no emulator for VB games — NextUI needs VB.pak in
  Emus/tg5040 on the card"). To use another emulator for one game, pick it in
  the game's options → Emulator (it shows "Automatic · GBA.pak (NextUI)" until
  you do); a pin or `game-launchers.json` still wins.
- **Covers.** Covers already in `.media` (or MinUI's `.res`) are shown first and
  never changed. A folder's own console icon is never taken as a game's cover.
- **Saves.** The Save Manager and Syncthing see `Saves` and each state folder
  ([SAVE_MANAGER.md](SAVE_MANAGER.md)).
- **Recently played.** NextUI's list is copied into Continue once, after
  JukaHub's own entries, for games still on the card.
- **Tools entry.** JukaHub adds `Tools/<platform>/JukaHub.pak` to NextUI's
  Tools list once, when it lives in `Apps/JukaHub`. Opening it starts JukaHub;
  quitting JukaHub returns to NextUI's menu (the stock menu is never started
  over it). Delete the `JukaHub.pak` folder to remove the entry; JukaHub does not
  add it again.

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

## Netplay (LAN and online)

Handhelds that support a link cable — Game Boy, Game Boy Color, Game Boy
Advance, SNES, Mega Drive, NES, and the other consoles listed below — can play
together without a cable, over the Wi-Fi both devices are already on or across
the internet. Highlight a game and press **X** to open **Netplay**:

| Row | What happens |
|-----|--------------|
| **Host on this network** | RetroArch starts the game as the host. Other JukaHubs on the same Wi-Fi see the room in their Netplay panel; the panel also prints this device's address (`192.168.1.5:55435`) *before* launching, for anything that cannot discover it. |
| **Host online · <relay>** | RetroArch hosts through one of libretro's public relay servers and announces the room in RetroArch's public lobby, so a friend on another network joins without port forwarding. **←/→** picks the relay (New York, Madrid, Montreal, Sao Paulo; saved as `netplay_relay`). The room is public: anyone with the same game and core can see it. |
| *Rooms on your network* | Every JukaHub hosting on the Wi-Fi, live. **A** joins with *your* copy of the ROM (matched by file name and size) and the host's core, whatever game the panel was opened on. |
| *Online rooms* | Rooms from `lobby.libretro.com` whose game is in your library (matched by name), this game's rooms first, up to 30. Fetched when the panel opens (at most once a minute); **Y** refreshes. Relayed rooms join through the relay. Password-protected rooms are listed but refused with a reason — join those from RetroArch's own Netplay menu. |
| **Join by address** | Type the host's address on the on-screen keyboard (`192.168.1.5` or `192.168.1.5:55435`). The panel remembers the last address, so a rematch is one press; **X** edits it. |

LAN discovery is a UDP query (`JUKANETPLAY?1`) on port 55437 that a hosting
JukaHub answers with the game's file name, size, core and port while RetroArch
runs (`JUKAHUB_NETPLAY_DISCOVERY_PORT` moves it). Plain RetroArch hosts are not
discovered: join those by address.

How it runs: JukaHub resolves RetroArch and a netplay-capable core for the
console, then launches `retroarch -L <core> <rom> --host` (or `--connect <ip>`)
with `--port` and `--nick`; a relayed online room is joined with
`--connect "<relay>|<port>|<session>"`. A host also gets
`--appendconfig netplay-host.cfg` (in JukaHub's data folder) that sets
`netplay_public_announce` and `netplay_use_mitm_server` — both **off** for a LAN
game, so a `retroarch.cfg` with them on can never publish a living-room game,
and both on for **Host online**. RetroArch may write those values back into
`retroarch.cfg` when it saves its config on exit.

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

### Link cable, wireless adapter and more players

Netplay runs *one* game on both devices. A Pokémon trade needs two — Red on one
handheld, Blue on the other — so the panel has extra rows for that
(`netplay_link*.go`):

| Console | Rows | How it runs |
|---------|------|-------------|
| Game Boy / Game Boy Color (when the **Gambatte** core is installed) | **Link cable · host**, the link cables heard on the network, **Link cable · join by address** | Gambatte's own network link cable. The host's game starts with `gambatte_gb_link_mode = "Network Server"` on port 56400; a joiner's with `"Network Client"` and the host's IPv4 address in `gambatte_gb_link_network_server_ip_1`…`_12` (one digit each, octets padded to three) and, for older Gambatte builds, `…_ip_octet1`…`4`. Each side plays **its own** game. A console that usually runs in another Game Boy core uses Gambatte for that session only. The link comes up within about 30 seconds of both games entering their link room. |
| Game Boy Advance (when **gpSP** is installed) | **Wireless adapter (Pokémon FireRed/LeafGreen/Emerald)**, the wireless rooms heard, **Wireless adapter · join by address** | RetroArch netplay with gpSP and `gpsp_serial = "rfu"` (the wireless adapter over RetroArch's packet interface, RetroArch 1.17 or newer). Each side again plays its own game: FireRed and LeafGreen meet in the Union Room. mGBA stays the console's emulator; gpSP is used for that session only. |

A room heard on the network joins with **your** game — the one the panel was
opened on — not the host's. Link hosts answer a separate UDP query
(`JUKALINK?1` on port 55438), so an older JukaHub never mistakes a Gambatte link
for a netplay room. With no Wi-Fi, **A** on a link host row opens the
**Hotspot** screen: turn it on, join it from the other handheld, host again.
The core options are set for that launch only and go back when RetroArch exits
(the same journal as every launch setting); a core that never ran here gets an
empty options file where RetroArch would create one.

**Up to four players.** Nintendo 64, Dreamcast and Arcade games seat four;
SNES, NES, Mega Drive, PlayStation, PC Engine and Saturn games do too when the
game supports a multitap. The host's `netplay-host.cfg` also sets
`netplay_max_connections = "3"` (three guests; RetroArch's default, in case
`retroarch.cfg` says fewer) and `netplay_start_as_spectator = "false"`. On those
consoles **←/→** on a room or on **Join by address** picks the player slot to ask
for (Auto, 2, 3, 4 — `netplay_request_device_pN` for that launch); Auto takes
the first free one. While a LAN host plays, the number of players connected is
shown on RetroArch's message line whenever it changes (counted from the
kernel's TCP table; RetroArch's command port is on for that launch).

**Saves.** A link cable session is an ordinary library launch, so cloud saves
come down first and the save ledger watches it. Every netplay start syncs the
card first, and a wireless-adapter session gets RetroArch's periodic battery
save flush, so a trade saved in the game is on the card even if the session
ends badly.

A USB-C cable between two handhelds cannot do any of this: the stock TrimUI
kernels have no USB networking, so there is no link to carry it. Wi-Fi or the
hotspot is the cable.

## Controls

| Input | Action |
|-------|--------|
| D-pad | move between the console buttons; on a console's page, up/down move in its games |
| D-pad left (list, cover column) / up (3D shelf, Cover Flow) | from a console's games back to the console buttons |
| D-pad left / right (Cube) | turn the cube to the previous / next console; up / down roll it to the previous / next game |
| **A** | open the highlighted console's page, or a game's page; **Play** on that page launches the game |
| **L2** / **R2** (**PgUp** / **PgDn**) | on a console's page, jump to the previous / next letter; hold to keep jumping (see [Letter scrubbing](#letter-scrubbing)) |
| D-pad right (list, cover column) | on a console's page, jump to the next letter |
| **Start** / **O** | on a console's page, open the game options sheet: rename title or file, visibility, library sort and view, launch options, covers, reset and delete |
| **X** | Netplay for the highlighted game (host or join, on the LAN or online); on the console buttons, the next Console look |
| **E** | open the highlighted game in its tool: a covering Game Profile, its exact Compatibility row, or the Emulators page with the console's tile highlighted. Closing that tool (**B**) comes back to the library where you left it |
| **L1** / **F** | fill the gaps: fetch covers for scanned games without one; while a fetch runs, stop it |
| **S** / **/** / **R1** | search the library (on-screen keyboard; the list filters as you type) |
| **Select** / **M** | the library's menu: a random game from this console or the whole library, the tag filter (**T**), search, fill missing box art, rescan and, on the console buttons, the next Console look. Each row names the button that also does it; the menu opens again on the row used last |
| **R** | (keyboard) roll a random game: a sheet asks whether from this console or the whole library |
| **Y** | rescan the card (after copying new ROMs) |
| **B** (or the Menu key) | one level back: a console's page → the console buttons → clear an active search or tag filter → close the library |
| Mouse | click a console button to open it, a game to open its page, the page title to go one level back, the top bar to close |

A search or tag filter shows only the consoles with hits, each button counting
its matches; when every hit is on one console, that console's page opens
straight away.

### Cover Flow and Cube

Two more views for a console's page, chosen with **Library view** in the game
options sheet (**Start** / **O**). Both use the whole page: the highlighted
cover is drawn large, and its video snap plays on it once you stop.

- **Cover Flow** — the highlighted cover faces you in the middle, the games
  before and after it turned in perspective on both sides over a soft
  reflection. **Left** / **Right** slide the row, **Up** goes back to the
  console buttons (as on the 3D shelf). A click on a side cover opens that
  game.
- **Cube** — the highlighted cover is the front of a cube. **Up** / **Down**
  roll the cube to the previous / next game; **Left** / **Right** turn it to
  the previous / next console (its first game), whose names are shown beside
  the cube. **B** goes back to the console buttons, as on every page.

**L2** / **R2** jump by letter in both. With reduced motion or Battery Saver
the row and the cube move without animating.

**Start → Games → Play a random game** (or "random game" in Home search) skips
the highlight step: it opens the library, waits for the card to be read, rolls
the whole library the same way R does (the game you played last is avoided),
and starts the pick. When it ends you are back in the library on that game.
**Settings → Power & battery → Start in → Random game** does the same each time
the handheld is switched on.

### Letter scrubbing

On a console's page **L2** and **R2** jump to the previous and next letter of
the list, in every view (list, 3D shelf, cover column); **Right** does the same
forward in the list and the cover column, where it had nothing to do, and a
keyboard uses **PgUp** / **PgDn**. Holding a trigger keeps jumping, after the
same first pause as a held D-pad (Accessibility's repeat delay). Each jump shows
the letter big over the games for about half a second.

A "letter" follows the sort chosen in the options sheet, so a jump always lands
at the start of a block the list really has: the first letter of the title
(digits and symbols are one **#** group; accented and non-Latin letters are
their own group, where the sort puts them), the decade for **Year**, the stars
for **Rating**, 10h+ / 1h+ / under 1h / not played for **Play time**, and the
month of the date for **Last played** and **Recently added** (the date's own
month, never "today", so a handheld that started before its clock was set does
not mislabel anything). Back from the middle of a block goes to its first game,
and from there to the block before. At either end nothing moves. The Continue
row, which is in play order, does not scrub.

### Sorting without "The"

With Settings → Games & emulators → **Ignore 'The' when sorting** on (the
default), a leading "The", "A" or "An" does not count for the name order:
**The Legend of Zelda** sorts, and scrubs, under **L**, next to No-Intro's
**Legend of Zelda, The - A Link to the Past** (a trailing ", The", ", A" or
", An" before the end of the name or a subtitle is skipped too). The names on
screen do not change. It applies to the Title sort, to the name every other sort
falls back on, and to Home's Favourites. Off sorts by the full name.

On a game's page, **History** (or **H** / **Select**) shows the total time,
how many times the game was played, completed sessions, the average and the
longest session, the last 7 and 30 days, and a scrollable session list. A
launch interrupted by a restart remains in the list with an unknown duration
rather than being added to playtime.

**X** (or **Delete** on a keyboard) in History deletes this game's record,
after a confirm (**A** Delete, **B** Keep): its launches and sessions, its play
time and its days in Playtime → By game go. Saves and save states, favourites,
tags, notes and the Recently played list are never touched. Your overall time
in JukaHub, trophies already earned and the daily play limit's count for today
stay as they are, so deleting a record never hands back limited play time.

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

**Select** opens the library's menu, whose first rows roll a game for you —
**Random game from** the console on screen, or **from the whole library** —
and land the highlight on the pick instead of starting it, so **A** plays it.
The menu opens again on the row used last, so **Select**, **A** rolls another
without leaving the library. On a keyboard **R** opens a small sheet that asks
which of the two to roll from. The stick click (R3) that used to do this is
gone: the original Smart Pro's sticks do not press in, and a Brick's F2 is its
own key.

| Row | What it rolls from |
|-----|--------------------|
| **This console** | only the console on screen; when **Continue** is highlighted, the console of that recent |
| **Whole library** | every console at once, recents left out so a recently played game is not counted twice |

The detail line under each row counts what the roll will actually draw from, so
it follows an active search: with `metroid` typed, both rows roll only the
matching games and the whole-library row says *matches*. The roll never picks
the game already under the highlight while the scope has another candidate, so
a second roll always moves. **B** cancels the sheet.

Picking jumps the library to the game's console, row and scroll position and
shows a toast naming it (`Random: ... — A plays it, Select, A for another`;
`R for another` on a keyboard). A game
that has disappeared from the card since the last scan is reported instead of
moving the highlight somewhere unrelated, and a roll in an empty library explains
that there are no games yet rather than opening an empty sheet.

## Video snaps

A short clip of the focused game can play in the preview box, the way the
Batocera family does it. Nothing is downloaded: the clips are files you
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
| Curated collections | Favorites and custom tags already filter the library. A separate collection editor with its own saved order is not included. |
| Managed emulator overlays and hotkey profiles | Per-game extra arguments, the player-order setting and the named control layouts (docs/CONTROLS.md) cover button layouts, but JukaHub does not create or manage RetroArch overlay layouts and named hotkey profiles. |
