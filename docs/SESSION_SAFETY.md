# Never lose a session

A handheld that runs out of battery in the middle of a game can lose more
than the last few minutes of play: if the emulator was writing to the SD card
at that moment, the save file (or the card itself) can be damaged. JukaHub
has always protected **RetroArch** games against this. It now protects the
**standalone emulators** too — PPSSPP, DraStic, Flycast, Mupen64Plus, ports,
Wine games and the rest — wherever they allow it.

## Universal instant resume

Press power and the game sleeps; press it again and you are exactly where you
were. JukaHub's sleep has always done that for every game, because the game
stays in memory. What used to end the session was everything that ends a
sleep without you: the battery at its last percent, **Power off after
sleeping**, and **Power off** itself. Now each kind of game gets the best
that kind allows:

| Game | Battery at its save level (3%) | Power off after sleeping | Power off |
| --- | --- | --- | --- |
| RetroArch | State saved; resumes at the next power-on | State saved; resumes at power-on | State saved; resumes at power-on |
| PSP in JukaHub's own PPSSPP build | State saved; resumes at the next start | Same | Same |
| An emulator whose own "save state on exit" is on | Saves as it quits; resumes where it loads that state itself | Same | Same |
| Anything that cannot save a state: ports, Wine, hakuX, Android games, Java, an emulator with that option off | **Held asleep where it is** — plug in and press power to carry on | Stays asleep; closed cleanly at the battery's last 5% | Asked to quit; its own saves are kept |
| A stream (Moonlight, Remote Play) | Closed — the game goes on on the other machine | Closed | Closed |

"Held" means the game is stopped in memory, the card synced and the handheld
put into JukaHub's own sleep with its power-off time ten minutes away. Plug in
and press power within that time and the game carries on exactly where it
was. Without a charger, JukaHub wakes itself, asks the game to quit (so it
writes its own saves) and powers off cleanly before the battery dies. A held
game is never killed in the middle of a write.

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

With **Save and suspend at 3%** on (Settings → Power & battery), a game whose
state can be saved is saved and closed. JukaHub:

1. asks JukaHub's own PPSSPP build for a save state first, when that is what
   runs (see [PSP games](#psp-games-in-jukahubs-own-ppsspp-build));
2. asks the emulator to quit politely — the same request as closing its
   window. An emulator whose **save state on exit** option is on writes a
   save state now, and every emulator writes the game's own saves (battery
   saves, memory cards) on its way out. The request goes to the emulator
   itself, so its launch script still finishes its own tidying up afterwards;
3. waits up to about 15 seconds for it to finish;
4. checks that a new, non-empty save state file really appeared;
5. writes everything to the card (`sync`) and suspends the device.

A state saved here also brings the game back at the next power-on, as after
Power off: if the battery runs out after the save, charge and power on.

A game **no state can be saved for** is held asleep instead (see the table
above): it stays where it is, and the handheld goes to sleep with the
power-off time ten minutes away. Woken without a charger, it gets 45 seconds
to find one; after that it is closed cleanly as above. Plugging in starts over:
the next time the battery is that low, the game is held again. The hold needs
JukaHub's sleep and an RTC that can wake the device; where either is missing,
the game is closed cleanly as before.

JukaHub never force-kills the emulator before it had its chance to save. One
that does not quit at all is left as it is, frozen in memory by the suspend.

Only games are closed. An app that is not a game — KOReader, a file manager, a
tool from the Apps screen — is never closed by the battery guard, so unsaved
work in it is not thrown away; it is not warned about at 10% either.

### When the device powers itself off after sleeping

**Power off after sleeping** (Settings → Power & battery) turns off a handheld
left asleep for hours. With a standalone game in its sleep:

- an emulator whose save-state-on-exit option is on, or JukaHub's own PPSSPP
  build, is woken with the screen off just long enough to save and quit, and
  the device powers off;
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
- A state JukaHub's own PPSSPP build saved is handed to PPSSPP by JukaHub at
  the game's next start, the same way.
- If it does **not**, the notification says so: open the emulator's own
  **Load state** menu once the game starts.

The record is dropped the next time that game starts. A power-on resume saved
at 3% is dropped too when you start another game before the next power-on, so
a power-on always brings back what you played last.

## PSP games in JukaHub's own PPSSPP build

Upstream PPSSPP can load a save state at start, but nothing outside it can ask
it to save one. JukaHub's own build of PPSSPP (Emulators → PSP → the PPSSPP
card) carries one small change for that: when JukaHub asks, it writes a save
state of the game into JukaHub's data folder — never into one of your own
save-state slots — and says when it is done. JukaHub asks before a power off
and at 3%, waits for the answer, then lets PPSSPP quit as before; the game's
next start begins from that state. Only the newest such state is kept (they
are tens of megabytes).

The TrimUI build from minui-psp, which PSP games use by default on the Smart
Pro and the Brick, does not have this change. To use JukaHub's build, choose
**Resume PSP games after a power off** on the PPSSPP card; your saves and save
states are shared either way. On the Smart Pro S, only the minui-psp build
exists.

## Games JukaHub closes for

An Xbox game, and a PSP game that needs the screen to itself, play while
JukaHub is closed (see [Launching](GAME_LIBRARY.md#launching)), so JukaHub's own battery guard is not
running. The small script that starts the game watches the battery instead: at
the same save level, off the charger, for two readings in a row, it asks
JukaHub's PPSSPP build for a state (when that is what runs), asks the game to
quit, and syncs the card. JukaHub then starts again, keeps the state for the
game's next start and at once powers off cleanly unless a charger is in. A
game that will not quit is never killed: the handheld is suspended under it.

## Emulators

| Emulator | Saves a state as it quits | Loads it again by itself | Turn it on in the emulator |
| --- | --- | --- | --- |
| PPSSPP (JukaHub's own build) | Yes, when JukaHub asks (not into your slots) | Yes: JukaHub starts the game from it | (built in) |
| PPSSPP (other builds) | Builds that have **Save state on exit** (the NextUI/MinUI builds of the Stapta-nl fork); upstream PPSSPP does not | **Auto load savestate** not Off | `ppsspp.ini`: `SaveStateOnExit = True`, `AutoLoadSaveState = 2` |
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
  tells you at the time. The same goes for the hold at 3%: the buzz says it,
  and the notification bell keeps what happened.
- A game held at 3% lives in memory: if the battery runs out before the
  ten-minute power off (a battery gauge that is far off near empty), the
  progress since the game's own last save is lost — but nothing was being
  written, so the card and the saves are whole.
- A real power off cannot keep a game in memory: the handheld's kernel has no
  hibernation (`/sys/power/state` offers `freeze mem` on the Smart Pro). A
  game that cannot save a state is closed by Power off, its own saves kept.
- PPSSPP writes a requested state on its next frame: one left in PPSSPP's own
  pause menu does not answer, and is closed without a state.
