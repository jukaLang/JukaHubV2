# Game Boy Printer

The Game Boy Printer was a small thermal printer for the Game Boy. Many games
could print on it: the Pokédex in Pokémon, photos from the Game Boy Camera,
the photo album in Link's Awakening DX. JukaHub emulates the printer for Game
Boy and Game Boy Color games, and keeps every print in a **scrapbook**. You can
look at the prints on the handheld or download them on your phone.

## Which emulator prints

DoubleCherryGB is the only libretro core that emulates the printer. It is a
fork of TGB Dual, and RetroArch's core downloader has it. Its project page lists
"Gameboy Printer Emulation with PNG-Print". It also has a link-cable hub that
switches the cable over to the printer when a game starts printing.

The cores Game Boy games usually use (Gambatte, Gearboy, TGB Dual, and the
libretro builds of SameBoy and mGBA) cannot print. Standalone SameBoy and mGBA
can, but they are not part of RetroArch
([RetroArch issue #13080](https://github.com/libretro/RetroArch/issues/13080)).

## Printing from a game

1. Open the game's page in the library and press **More**.
2. Choose **Game Boy Printer**. For a game known to print, this row is near the
   top of More. Other Game Boy games have it at the bottom and can still try.
3. Press **Play with the printer**.

The first time, JukaHub downloads DoubleCherryGB (a few MB). It uses the same
install as the Emulators page, and the install card shows progress. JukaHub
then makes DoubleCherryGB this game's emulator and starts the game. In the game,
print from its own menu, for example Pokédex → Print, or the Camera's album →
Print. When you quit the game, JukaHub copies the new prints into the scrapbook
and shows a message such as "2 prints saved to the scrapbook".

The game stays on DoubleCherryGB, and **Play** starts it there with the printer
until you change it back. To go back, open the same card and press **Usual
emulator**, which restores the emulator the game had before. DoubleCherryGB is
not added to the Game Boy emulator choices: otherwise, after it was installed,
every Game Boy game would ask which emulator to use.

### Games known to print

Pokémon Yellow, Gold, Silver, Crystal, Pinball and Trading Card Game; Game Boy
Camera (Pocket Camera); The Legend of Zelda: Link's Awakening DX; Super Mario
Bros. Deluxe; Harvest Moon; Donkey Kong Country; Mario Golf; Mario Tennis;
Kirby Tilt 'n' Tumble; Alice in Wonderland; Perfect Dark; E.T. Digital
Companion; 102 Dalmatians; Trade & Battle: Card Hero; Medarot. JukaHub matches
these by title. The list is not complete, so any Game Boy game can use the row.

### Your save

RetroArch can keep one save folder per core ("Sort saves into folders by core
name"). With that on, Gambatte's save and DoubleCherryGB's save are separate
files. **Play with the printer** copies the game's battery save (`.srm`, and
`.rtc` for a clock) from the game's previous emulator to DoubleCherryGB, so you
continue where you left off. **Usual emulator** copies it back.

A copy never deletes anything. JukaHub keeps the SHA-256 of each copy in
`data/gbprinter/state.json`. It overwrites a save only when that file has not
changed since its last copy, and first keeps the old version as
`<name>.jukahub-before-printer`. If both saves changed, JukaHub copies neither
and tells you. When both cores share one save folder, which is RetroArch's
default, no copy is needed.

## The scrapbook

There are two ways to open it:

- **More → Game Boy Printer → Scrapbook** on a game's page shows that game's
  prints.
- **Tools → Printer scrapbook** shows the prints of every game.

The prints appear in a grid, newest first. The pixels stay sharp because each
print is drawn in whole-pixel steps.

| Pad | Keyboard | What it does |
|-----|----------|--------------|
| D-pad | Arrows | Move |
| A | Enter | View the print large; in the large view, change the zoom |
| Left / Right | Left / Right | In the large view, show the previous or next print |
| L1 / R1 | - / + | Zoom the large view |
| Up / Down | Up / Down | Scroll a long print |
| X | Delete or X | Delete the print (asks first) |
| Y | Y | Show a QR code that opens the scrapbook on your phone |
| B | Esc | Back |

The prints are stored in JukaHub's data folder:
`data/gbprinter/scrapbook/<game>/0001.png`, `0002.png`, and so on.
`data/gbprinter/scrapbook.json` lists each print with its game, console, and
the order it arrived in. The order comes from a counter, not a date, because the
handheld can start before it has the right time. A picture the scrapbook
already has is not added again.

## On your phone

The Phone Companion page has a **Game Boy Printer scrapbook** card. It lists
every print with three download buttons: **Save** (the print as it is), and
**2×** and **4×**, larger copies with sharp pixels that are better for printing
on paper. Press **Y** in the scrapbook to show a QR code that opens the page at
this card.

The card cannot change anything and needs the companion's PIN, like the Files
card. A request names a print by its number only, never by a path. JukaHub
opens the file from inside the scrapbook folder, so a request cannot reach any
other file.

## How prints are found

We could not confirm where DoubleCherryGB saves its PNG files, so JukaHub does
not rely on one folder. Before a game starts in DoubleCherryGB, JukaHub lists
the PNG files in the folders a core can write to. After the game, it lists them
again. The folders are:

- the game's own folder;
- RetroArch's save and system folders;
- the folder RetroArch runs in;
- RetroArch's screenshot folder, but only files with "print" in their name;
- inside those folders, any subfolder whose name contains "print",
  "DoubleCherry" or "dcgb", or has the game folder's name.

A PNG counts as a print if it is new, or if its size or file date changed
between the two lists. File dates are only compared with each other, never with
the clock.

Some PNGs are skipped:

- RetroArch's screenshots (`<game>-YYMMDD-HHMMSS.png`);
- save-state pictures;
- files that do not decode;
- files narrower than the printer's 160-pixel paper;
- pictures of more than about 16 million pixels, which the handheld could not
  show;
- files whose width is not a multiple of 160, unless their name or folder says
  "print".

## Core options

DoubleCherryGB's option names start with `dcgb_`. The project says to keep
`dcgb_emulated_gameboys` at 1 for one player, so each printing launch sets it to
`1`. The setting is written to the options file RetroArch reads for the game and
is restored after the game, the same way JukaHub's other one-launch settings
are. If the core has never run, JukaHub creates its options file with this
value.

We could not confirm the name of the option that picks the printer. The core's
own hub is expected to connect the printer automatically. If a device needs a
setting, `data/gbprinter/state.json` can list one without a new JukaHub build:

```json
{ "core_options": { "dcgb_emulated_gameboys": "1", "<key>": "<value>" } }
```

After each game played with the printer, JukaHub logs DoubleCherryGB's options
about the printer and the link cable, as RetroArch saved them. It also stores
them in `state.json` under `seen_options`.

## Not verified yet

These points have not been checked on a TrimUI yet:

- that DoubleCherryGB loads there;
- that the core printer works;
- where its prints land.

Libretro's core list still shows version 0.17.0, and printing arrived in
0.18. A build from the downloader may therefore be too old to print. In that
case the game plays, but nothing reaches the scrapbook. Please report it if
this happens.
