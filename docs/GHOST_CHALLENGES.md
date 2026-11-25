# Ghost Challenges

Race a friend's run of JukaHub's own arcade games — Flappy, Snake, Tetris,
2048 and Brick Breaker — or your own best. Start it from **Games → Ghost
Challenges**, from **Y** on a game's game-over card, or from **Play** on a
challenge in the notification bell.

## What a run is

Every run of the five games is recorded while you play: the seed its
randomness came from, your score every 100 ms and, in Flappy, the bird's height
every 50 ms. That is a few kilobytes a minute. The best run of each game is
kept in the data folder (`ghosts/best-<game>.json`).

Racing a run plays the game with the run's seed, so everything the game itself
decides is the same: Flappy's pipes, Tetris's pieces, 2048's new tiles, Brick
Breaker's first serve. Snake places its food away from the snake, so two snakes
that went different ways see different food after the first bite; the score is
still the race.

While you race, the top of the screen shows the ghost's score at the same
moment of its run beside yours ("Ana's ghost 14 · You 12"), and "Ana finished
on 23" once the ghost's run has ended. In Flappy the ghost bird flies the run
beside yours, through the same pipes. The game-over card says who won and by
how much; **Start** races again.

## Sending a run

**Y** on the game-over card, or **Y** on one of **Your best runs**, opens the
send sheet:

- **Send to …** — a JukaHub paired under Settings → Network → Nearby devices
  (the two seen most recently). The run goes over the paired, pinned HTTPS link
  (`POST /peer/v1/ghost`, paired devices only); the other JukaHub checks it,
  keeps it and puts **Play** in its bell.
- **Save as a file** — a `.jghost` file in the Ghosts folder: `Ghosts` at the
  top of the SD card on a handheld, `Documents/JukaHub Ghosts` on a PC. Send it
  any way you like (Chat, a card, a cable); a `.jghost` file in the receiving
  JukaHub's Ghosts folder is taken in the next time Ghost Challenges opens.

A received run is checked before it is kept: one of the five games, numbers in
range, an hour at most, the file under 900 KB. The inbox keeps 30 runs, the
oldest going first; **X** deletes one. Each row shows your best against it and
"Beaten" once you have.

## The screen

| Button | Does |
|---|---|
| D-pad | Move between rows |
| A | Race the run |
| X | Delete a friend's run |
| Y | Send one of your best runs |
| B | Back |

## Not yet

Emulated games (RetroArch input replays) are not part of Ghost Challenges: a
replay only holds for the same ROM, core build and core options on both sides,
and the ghost cannot be drawn into the friend's game. See the ideas parking lot
in [MODERNIZATION.md](../player/MODERNIZATION.md).
