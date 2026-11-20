# CHIP-8 games

CHIP-8 is a tiny game format from 1977: programs of a few kilobytes for a
64×32 one-bit screen and a sixteen-key hex keypad. Its two extensions are
still written for every year — SUPER-CHIP (128×64, scrolling, a big font) and
XO-CHIP (four colours, 64 KB, sound patterns) — and the yearly Octojam fills
the [chip8Archive](https://github.com/JohnEarnest/chip8Archive) with new games,
all in the public domain.

JukaHub plays them itself. There is no emulator to install or core to
download: the player is part of JukaHub, the same on every TrimUI model and on
Windows.

## The shelf

CHIP-8 games go in `Roms/CHIP8` (the names `chip8`, `CHIP-8` and `chip 8` work
too, as does a NextUI `CHIP-8 (CHIP8)` folder). The Games library lists the
`.ch8`, `.c8`, `.sc8` and `.xo8` files there as the **CHIP-8** console. Those
extensions are games only in that folder.

A game starts like any other: from its page, Continue, Home or the recents.
Its play time, recents and first-launch milestone count as for any game. The
page's **More** sheet has **Get more CHIP-8 games**, and its details show which
machine the game plays as.

## Getting games

**Start → Games → Get CHIP-8 games** lists the chip8Archive: title, authors,
machine and the event it was made for. The chips at the top show all games or
one machine's.

- **A** opens a game's sheet: its description, release date and the speed the
  archive asks for, with **Install** (or **Play** once it is on the shelf).
- **X** installs the highlighted game straight away.
- **Y** (F5 on a keyboard) reads the list again.

Installing puts the game in the CHIP-8 folder under its title (`.ch8`, `.sc8`
or `.xo8` after its machine) and remembers the archive's settings for it: its
quirks, speed and colours. Its cover is the game itself — JukaHub runs it for a
few seconds and keeps the screen. The list is saved, so it opens offline too
(the header says when it is the saved copy). Wi-Fi is needed to install.

## Playing

The picture fills the screen at the largest whole-number size, so every pixel
is the same size. Games that ask for a rotated screen get it.

| Pad | Keyboard | CHIP-8 key |
| --- | --- | --- |
| D-pad (or left stick) | Arrows, or W A S D | 5 7 8 9 (up, left, down, right) |
| A | Enter, Space, or E | 6 |
| B | Backspace, or Q | 4 |
| Hold Y | Hold Tab | On-screen keypad |
| Start, Select | Esc | Menu |

This is the convention most modern CHIP-8 games are written for. On a keyboard
the whole hex keypad is there as `1 2 3 4 / Q W E R / A S D F / Z X C V`. A game
that uses other keys can be played two ways:

- **Hold Y** (Tab on a keyboard) to show a 4×4 keypad; the D-pad picks a key and
  **A** presses it. Let go of Y to hide it.
- Change the map on the menu's **Keys** page: each pad button (the D-pad, A, B,
  X, L1, R1) can press any hex key, or none. The map is kept for that game.

## The menu

Start (or Esc) pauses the game:

- **Resume**, **Reset** (the game starts over).
- **Speed**: instructions per frame. CHIP-8 games were written for very
  different machines; a game that crawls or races needs another speed.
- **Mode**: the machine the game runs as. **Game's own** is what the archive
  (or an options file) says; **Automatic** is JukaHub's guess for other files;
  or pick CHIP-8, SUPER-CHIP or XO-CHIP. Each machine does a few instructions
  differently (the "quirks"), and a game written for one can misbehave on
  another — when a game draws garbage or stops, try another mode.
- **Keys** (above), **Sound** on or off.
- **Quick save** and **Quick load**: the whole game, one save per game.
- **Quit** goes back to where the game was started from.

Speed, Mode, Keys and Sound apply at once and are remembered for the game. A
game that stops by itself — it ended, or ran into something no CHIP-8 can do —
opens the menu with the reason.

## Without the archive's settings

A game copied in from elsewhere has no settings. JukaHub guesses its machine
from its extension (`.sc8` SUPER-CHIP, `.xo8` XO-CHIP), its size and the
instructions it uses, then uses that machine's usual quirks and speed. A
plain `.ch8` plays like the original COSMAC VIP.

A game can bring its own settings: an Octo options file beside it with the same
name (`Game.json` next to `Game.ch8`) — either Octo's options object
(`{"tickrate": 30, "shiftQuirks": true, "fillColor": "#FFCC00"}`) or a
chip8Archive entry with `platform` and `options`.

## Saves and settings

Everything is in JukaHub's data folder, under `chip8`:

- `games.json`: each game's Speed, Mode, Keys, Sound and its SUPER-CHIP "user
  flags" (some games keep a high score there), keyed by the file's contents, so
  renaming or moving a game keeps them;
- `installed.json`: the archive's settings for the games the shelf installed;
- `states/`: the quick saves;
- `programs.json`: the saved archive list.

A game with no cover gets one from its own screen after a few seconds of play.
To keep it coverless, use **Game options → Remove cover**.

## Limits

- The 1802 machine-code calls some 1977 VIP programs make (`0NNN`) cannot run
  anywhere but a VIP; such a program stops with that reason.
- Octo's font styles and touch modes are not offered; every game gets the
  standard font.
- SUPER-CHIP games that scroll in low resolution scroll whole low-resolution
  pixels, as in Octo (the chip8Archive's reference), not the half steps of the
  original HP 48.
- A game file larger than XO-CHIP's 64 KB memory leaves room for (65,024
  bytes) is not a CHIP-8 game and is refused.
