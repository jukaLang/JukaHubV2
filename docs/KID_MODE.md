# Kid mode and Kiosk mode

Sometimes the handheld goes to someone else: a child, a friend at a party, a
shop window. **Kiosk mode** and **Kid mode** let them play without changing or
breaking anything. **Full mode** is JukaHub as it always is.

It is new and **not yet checked on a TrimUI**. The steps below are how it is
meant to work; if something is still reachable that should not be, see
[Leaving without the code](#leaving-without-the-code) and tell us.

## The three modes

| Mode | What works | What is locked |
|------|------------|----------------|
| **Full** | Everything | Nothing |
| **Kiosk** | Games (library, game page's Play, game switcher, random game), favourites and collections, the music and video players, internet radio and video, remote play and cloud gaming, Trophies, the built-in games, Paint, Stopwatch, PDF manuals, Quick Settings' switches, screenshots, sleep | Settings (except one small page), the game options sheet, the game page's tags and More sheet, Files, Terminal, every store and install (Get Homebrew, Ports store, App Center, packages, emulators and cores, themes, and the clients, ports and games remote play, Cloud Gaming and Steam would download or copy), updates, profiles, Edit Home, Quick Settings' edit, Tools, the Browser, Chat, Mail, Party and Voice rooms, the voice assistant and the AI, the phone companion, quitting to the firmware's menu |
| **Kid** | Kiosk, but only the games marked for kids, and videos on the device | Everything Kiosk locks, plus what comes from the internet: streaming video and radio, podcasts, remote play, cloud gaming, Steam |

While Kiosk or Kid mode is on, **Settings** is one page: **Volume**,
**Brightness**, **Wi-Fi** on or off, and **Leave Kid mode** (or Kiosk). The
Start menu keeps only what still works and ends with **Unlock**. Home search
finds games and the screens that still open, not apps, files or settings.
Anything locked that is still pressed somewhere says *Locked in Kid mode*.
In Kid mode a favourite or the Continue card that would stream from the web
(a video, a stream, a podcast, a YouTube link) says so too; videos and music
on the card play.

The mode is for the whole device, not for one profile: profiles are switched
from the Start menu by whoever holds the handheld, so a lock that belonged to
one profile would be one switch away from gone. Switching profiles is locked
too.

## Turning it on

1. **Settings → Kid & Kiosk mode → Interface mode**, and choose **Kiosk** or
   **Kid**.
2. Choose a **parent code**: 4 to 8 presses of the D-pad, A, B, X, Y, L1 and
   R1 (the same kind of code as the startup lock), then **Start**. Enter it
   again and press **Start** to save. On a keyboard: the arrows and A, B, X,
   Y, L, R type, **Enter** goes on, **Backspace** deletes, **Esc** closes.
3. JukaHub goes to Home in the new mode.

Pressing **Start** with nothing typed sets **no code**. Then the Konami code
leaves the mode instead (below), as on Batocera. Anyone who knows the trick
can leave, so choose a code for a child who might look it up.

The code is never stored: `ui-mode.json` keeps a salted, slow hash of it.

## Which games are for kids

In Kid mode the library, Home's game rows, Home search, the game switcher and
the random game show only:

- games marked **For kids**: open a game's page (A on it in the library) →
  **More** → **For kids**. A goes round **No** → **This game** → **Every
  *console* game**;
- every game of a console marked **Every *console* game** (the same row, on
  any of its games);
- JukaHub's own built-in games (JukaLand, Synth, Solitaire, Snake, Tetris,
  2048, Pong, Minesweeper, Flappy).

The mark follows the game, not its file name: it is kept with the game's
details (console and title), so renaming or moving the file keeps it.
**Settings → Kid & Kiosk mode → Games for kids** says how many are marked and
opens the library. A game that is not for kids does not start in Kid mode
from anywhere — a favourite, a collection or a shortcut included.

JukaHub's game details have no age rating, so there is no "from the rating"
choice yet.

## Time limits

The **daily game limit** and the **break reminder** are in **Settings → Games
& emulators**. **Settings → Kid & Kiosk mode → Time limits** goes straight to
them. They work in every mode; Kid mode cannot change them, because its
Settings page does not have them.

## Leaving

- **Start menu → Unlock**, or **Settings → Leave Kid mode**, then the parent
  code and **Start**.
- **The Konami code**, on any screen: **Up Up Down Down Left Right Left Right
  B A** on the pad. On a keyboard: the arrows, then **B A** or **Esc Enter**.
  Without a parent code it leaves at once. With a parent code it opens the
  Unlock card, which asks for the code: a combination anyone can look up does
  not undo a code. B A or A B both count, so a swapped A/B (or a pad whose
  letters are the other way round) does not matter.

Five wrong codes are free; after that each try waits 30 seconds, doubling to
5 minutes. The count survives turning the handheld off and on.

Leaving forgets the code: the next time the mode is turned on it asks for a
code again.

## Leaving without the code

JukaHub never locks its owner out. Either of these is Full mode again:

- **Delete `ui-mode.json`** (and `ui-mode.json.bak` beside it) from a
  computer, while JukaHub is not running (it reads the file when it starts
  and writes it again on the next change). On a TrimUI it is in the `data`
  folder beside JukaHub on the SD card (usually
  `Apps/JukaHub/data/ui-mode.json`); on Windows in
  `%APPDATA%\uk.co.jukahub\JukaHub\`. Delete it rather than editing it: a
  file JukaHub cannot read at all is replaced by the `.bak` copy, which may
  still be in Kid mode.
- **Start JukaHub with `JUKAHUB_UI_MODE=full`**: add the line
  `export JUKAHUB_UI_MODE=full` near the top of JukaHub's `launch.sh` on the
  SD card (remove it again afterwards), or on a PC set the variable before
  starting `JukaHub.exe`. JukaHub starts in Full mode and saves it.

Like the startup lock, Kid mode is a lock for the screen, not for the SD card:
the games and saves are plain files that a computer can read.

## The file

`data/ui-mode.json`:

| Key | Meaning |
|-----|---------|
| `mode` | `full`, `kiosk` or `kid`; anything else is `full` |
| `code` | the parent code's salted PBKDF2 hash (`jhlock1$code$…`), or absent for none; a value JukaHub cannot read is no code |
| `fails` | wrong codes in a row |
| `kidConsoles` | consoles whose every game is for kids |

The per-game mark is the tag `For kids` on the game's record in
`data/game-meta/games.json`.
