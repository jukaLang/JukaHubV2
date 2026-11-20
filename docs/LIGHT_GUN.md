# Light gun

Duck Hunt, Time Crisis, Lethal Enforcers and the other light-gun games were
made for a gun pointed at a TV. On the handheld JukaHub turns the pad into
that gun for RetroArch: **the right stick aims, R1 fires, L1 reloads**, and
each game keeps its own aim settings.

It is new and **not yet checked on a TrimUI**: the device types and core
options below come from the cores' documentation and other front ends'
settings. If the crosshair does not move or sits off the target, see
[When it does not work](#when-it-does-not-work).

## Turning it on

Open a game's page → **More** → **Light gun**, and set **Light gun for this
game** to **On**. It is the *Light gun* control layout, so it also shows in
**More** → **Controls** (and **X** there gives it to every game of the
console). It is offered for NES, SNES, Mega Drive, Master System, Sega CD,
PlayStation and Arcade games.

**Known gun games get it by themselves.** About a hundred gun games (the NES
Zapper, Super Scope, Menacer, Light Phaser, GunCon and arcade gun games such
as Operation Wolf) start with the light gun the first time, when the game has
no control layout of its own and its emulator is one JukaHub knows (below).
After that game a message says how to aim and fire. Their page shows a
**Light gun game** chip until then. Turn the gun off for a game and it stays
off; turn the automatic choice off for every game in the same screen
(**Known gun games get it by themselves**).

Only a game whose whole title is in the list gets it by itself: a compilation
such as *Super Mario Bros. + Duck Hunt* or *Hang-On & Safari Hunt* does not
(the other game needs the pad), and neither do games that mix gun stages with
pad play (*The Adventures of Bayou Billy*, *The Lone Ranger*, *Snatcher*, *Die
Hard Trilogy*, *Resident Evil Survivor*). Turn it on for those yourself; the
right gun is still chosen.

## The controls

| Button | In the game |
|--------|-------------|
| Right stick | Aims the crosshair |
| R1 (or R2) | Fires |
| L1 (or L2) | Reloads: a shot off the screen, which is how these games reload |
| R3 | Puts the crosshair back in the middle of the aim area |
| A, B, X | The gun's side buttons (GunCon A and B, Super Scope cursor and turbo) |
| Start, Select | Start and Select; Select is the coin in arcade games |

The game does not see the shoulders or R3 while the gun is on; the D-pad and
the face buttons still reach it. JukaHub's own in-game chords keep working
(Select + L2 opens the in-game menu, Select + R1 takes a screenshot — and
fires, when R1 is the trigger).

**On a Brick or Hammer** (no sticks) the D-pad aims and is not the game's
D-pad while the gun is on; aim is always *Move* there.

**On Windows** the PC's own mouse is the gun: the left button fires, the right
one reloads. JukaHub still plugs the gun into the right port for the game.

## Tuning a game

In **More** → **Light gun** (Left / Right change a row, **A** toggles it,
**Y** puts the game's settings back, **B** goes back and saves):

- **Aim**: *Move* — the stick steers the crosshair, further for faster (with a
  **Speed** from 1 to 5) — or *Point* — the stick's position is the spot: at
  rest the middle of the aim area, pushed all the way its edge. Move is
  steadier; Point is quicker for games such as Duck Hunt.
- **Aim area** (Point): where a full push reaches. RetroArch aims at the game
  picture, not the whole screen, so the default is a 4:3 picture in the
  middle of the screen. *Whole screen* suits a stretched picture; *Custom*
  sets the width, height and position in percent, for an integer-scaled or
  cropped picture.
- **Fire with** R1 or R2, **Reload with** L1 or L2.
- **Buzz on each shot**: a short vibration with every shot.
- **Try it**: a practice target with these settings — the game's trigger and
  reload buttons, six shots to a magazine. Change a setting, try again. With a
  keyboard the arrows aim, Space fires, R reloads and C re-centres.

The settings are kept in `data/light-gun.json`.

## How it works

For each start of a game with the light gun on, and only for that start (the
values are put back when RetroArch closes, as the other launch settings are):

- the gun is plugged into the port the console used for it
  (`input_libretro_device_p<N>`), the mouse aims it, its left button is the
  trigger and its right one the reload (`gun_trigger_mbtn = 1`,
  `gun_offscreen_shot_mbtn = 2`);
- the gun's Start, Select, side buttons and D-pad are bound like the pad's
  (from the config, or the pad's autoconfig file when the config leaves them
  out), because on port 1 the gun replaces the pad;
- RetroArch's menu ignores the mouse (`menu_mouse_enable = false`), so a shot
  never clicks a menu row;
- the core's own options turn its crosshair on (start the game once from
  RetroArch first, so the core has an options file).

Then JukaHub makes a mouse (a uinput device) before RetroArch starts, reads the
pad beside the game without taking it, and moves the mouse at 125 Hz. For
*Point* it pushes the pointer into the top-left corner first, so it knows
where the pointer is; it does that 3 seconds after the start (for *Point*
again at 12 seconds), when you press R3, and when the game comes back from
JukaHub's in-game menu — the crosshair then starts from the middle. Nothing is
drawn by JukaHub during the game: a buzz says the gun is ready.

| Console | Emulator | Gun, port | Device type | Core options |
|---------|----------|-----------|-------------|--------------|
| NES | FCEUmm | Zapper, 2 | 258 | `fceumm_zapper_mode = clightgun`, `fceumm_show_crosshair = enabled` |
| NES | Nestopia | Zapper, 2 | left to the core's game list | `nestopia_zapper_device = lightgun`, `nestopia_show_crosshair = enabled` |
| SNES | Snes9x, Snes9x 2010 | Super Scope, 2 (Justifier 516) | 260 | — |
| Mega Drive, Sega CD | Genesis Plus GX | Menacer, 2 (Justifiers 772) | 516 | `genesis_plus_gx_gun_input = lightgun`, `genesis_plus_gx_gun_cursor = enabled` |
| Master System | Genesis Plus GX | Light Phaser, 1 | 260 | as above |
| PlayStation | PCSX-ReARMed | GunCon, 1 (Konami gun 516) | 260 | `pcsx_rearmed_crosshair1 = red` |
| PlayStation | Beetle PSX, SwanStation | GunCon, 1 | 260 | — (Beetle's defaults show a cross) |
| Arcade | FinalBurn Neo | Lightgun, 1 | 4 | `fbneo-lightgun-crosshair-emulation = always show` |
| Arcade | MAME 2003-Plus | read through its X-Y device | left alone | `mame2003-plus_xy_device = lightgun`, `mame2003-plus_crosshair_enabled = enabled` |

The Konami Justifier games (Lethal Enforcers, the American Laser Games on the
Sega CD, Area 51 and Maximum Force on the PlayStation) get the Justifier
device. Any other emulator gets nothing: the screen says JukaHub does not know
its gun, and the layout changes no button there. Standalone emulators and
NextUI paks keep their own controls.

## When it does not work

- **The crosshair does not move.** RetroArch has to read a mouse. The
  `sdl2` and `udev` input drivers do; `linuxraw` does not, and the screen
  says so after a start. Some firmware builds of RetroArch may also never
  look for a mouse; that is the first thing the hardware check will tell.
- **The crosshair is off the target.** Try *Point* with a *Custom* aim area
  that matches the game picture, or press R3 to put the crosshair back in the
  middle.
- **The game still waits for a gun.** Some games want the gun in the other
  port, or the core's own device setting; set the port's device type in
  RetroArch's Quick Menu → Controls, and tell us which game it was.
- **The options did not change.** A core that has never run has no options
  file: start the game once from RetroArch, then again from JukaHub.

The rest of the controls are in [CONTROLS.md](CONTROLS.md).
