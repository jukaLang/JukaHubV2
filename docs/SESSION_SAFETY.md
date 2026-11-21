# Never lose a session

A handheld that runs out of battery in the middle of a game can lose more
than the last few minutes of play: if the emulator was writing to the SD card
at that moment, the save file (or the card itself) can be damaged. JukaHub
has always protected **RetroArch** games against this. It now protects the
**standalone emulators** too — PPSSPP, DraStic, Flycast, Mupen64Plus, ports,
Wine games and the rest — wherever they allow it.

## What happens, and when

### At 10% battery

If the game on screen runs in an emulator that **cannot save a state by
itself**, JukaHub warns you once for that game:

> Battery 10% — Mupen64Plus can't be saved automatically: save in the game now

The device buzzes so you notice it during the game, and the message waits in
the notification bell. Save in the game (its own save point, or the
emulator's own save-state menu) and plug in if you can.

RetroArch games do not get this warning: JukaHub can save them itself. Nor do
streams (Moonlight, Remote Play, Xbox Remote Play): the game runs on the other
machine and nothing is lost on the handheld.

### At 3% battery

With **Save and suspend at 3%** on (Settings → Power & battery), JukaHub:

1. asks the emulator to quit politely — the same request as closing its
   window. An emulator whose **save state on exit** option is on writes a
   save state now, and every emulator writes the game's own saves (battery
   saves, memory cards) on its way out. The request goes to the emulator
   itself, so its launch script still finishes its own tidying up afterwards;
2. waits up to about 15 seconds for it to finish;
3. checks that a new, non-empty save state file really appeared;
4. writes everything to the card (`sync`) and suspends the device.

JukaHub never force-kills the emulator before it had its chance to save. One
that does not quit at all is left as it is, frozen in memory by the suspend.

Only games are closed. An app that is not a game — KOReader, a file manager, a
tool from the Apps screen — is never closed by the battery guard, so unsaved
work in it is not thrown away; it is not warned about at 10% either.

### When the device powers itself off after sleeping

**Power off after sleeping** (Settings → Power & battery) turns off a handheld
left asleep for hours. With a standalone game in its sleep:

- an emulator whose save-state-on-exit option is on is woken with the screen
  off just long enough to save and quit, and the device powers off;
- an emulator that cannot save keeps the device asleep, as before — your
  progress is never traded for battery — unless the battery is down to 5%,
  when a clean quit and a power off are better than dying asleep;
- a stream is simply closed and the device powers off.

### When you choose Power off with a game running

The emulator gets its own time to save and quit before the shutdown closes
everything, instead of the shutdown's two seconds.

## Picking up where you left off

What was running and which save state was written is remembered in
`sessionsafe-last.json` in JukaHub's data folder, and shown under
**Protect standalone emulators** in Settings.

- If the emulator **loads the state by itself** at its next start (see the
  table), starting the game again is all it takes: after a power off,
  **Resume game at power on** does it at the next power-on; any other time the
  **Continue** row does. The emulator puts you back where you were.
- If it does **not**, the notification says so: open the emulator's own
  **Load state** menu once the game starts.

The record is dropped the next time that game starts.

## Emulators

| Emulator | Saves a state as it quits | Loads it again by itself | Turn it on in the emulator |
| --- | --- | --- | --- |
| PPSSPP | Builds that have **Save state on exit** (the NextUI/MinUI builds of the Stapta-nl fork); upstream PPSSPP does not | **Auto load savestate** not Off | `ppsspp.ini`: `SaveStateOnExit = True`, `AutoLoadSaveState = 2` |
| DraStic | When its auto save state is on | Not claimed (load it from DraStic's menu) | `config/drastic.cfg`: `enable_autosavestate = 1` |
| Flycast | **Auto save state** | **Auto load state** | `emu.cfg`: `Dreamcast.AutoSaveState = yes`, `Dreamcast.AutoLoadState = yes` |
| Mednafen | `autosave` | `autosave` | `mednafen.cfg`: `autosave 1` |
| DuckStation | **Save state on exit** (on by default) | No (load it from its menu) | `settings.ini`: `SaveStateOnExit = true` |
| Mupen64Plus, Yaba Sanshiro, ScummVM, DOSBox, OpenBOR, PICO-8, fake-08, Godot, PortMaster ports, MinArch, hakuX, Java, ClassiCube, Luanti, Wine | No — closed cleanly, the game's own saves are kept | — | — |
| Moonlight, Remote Play, Xbox Remote Play | Nothing to save: a stream | — | — |
| RetroArch | Yes, through its network commands (as before) | Yes | (set up by JukaHub) |

JukaHub reads these options but never changes them: an emulator's settings
belong to the emulator.

An emulator JukaHub does not recognise, started as a game (from the library,
or from the `Roms`, `Emus` or ports folders), is treated like the "No" row:
asked to quit the way JukaHub's own exit asks, then the card is synced.

## Turning it off

Settings → Power & battery → **Protect standalone emulators** (on by
default). Off, the guard and the automatic power offs treat standalone games
as before: only RetroArch games are saved and closed first.

**Save and suspend at 3%** switches the whole low-battery guard, RetroArch
included.

## Safe power off

After everything above — the game saved and closed, JukaHub's own settings,
saves and logs written, the card synced — **Restart** and **Power off** take
one more step before the handheld goes dark (safe shutdown):

1. Any other program that still has a file open on the SD card (a helper a
   script started, a server, a daemon) is asked to close (SIGTERM). The
   system itself, JukaHub and the scripts that started JukaHub are left alone.
2. Up to 1.5 seconds for them to finish.
3. The card is synced once more.
4. The card is switched to read-only, so nothing can be half-written as the
   power goes. If a file is still open for writing the card stays writable;
   the sync already happened.

All of it takes at most 3 seconds, and anything that fails just lets the
power off go on as before. If the power off itself fails, the card is made
writable again.

Settings → Power & battery → **Safe power off** (on by default; TrimUI only).

## Known limits

- The save-state-on-exit behaviour of each emulator is taken from its
  documentation and other handheld projects; it has not yet been checked on
  every TrimUI model. If an emulator does not write its state when asked to
  quit, JukaHub notices (no new state file) and treats it like one that cannot
  save.
- JukaHub cannot press an emulator's save-state hotkey for you: the TrimUI
  emulators read the pad, not a keyboard, and a keyboard added while they run
  is not seen by them.
- The warning at 10% appears under the game until it ends; the buzz is what
  tells you at the time.
